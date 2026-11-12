import {useMemo, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Grid} from '@astryxdesign/core/Grid';
import {Card} from '@astryxdesign/core/Card';
import {Text, Heading} from '@astryxdesign/core/Text';
import {Button} from '@astryxdesign/core/Button';
import {Badge} from '@astryxdesign/core/Badge';
import {Code} from '@astryxdesign/core/Code';
import {Collapsible} from '@astryxdesign/core/Collapsible';
import {MetadataList, MetadataListItem} from '@astryxdesign/core/MetadataList';
import {Breadcrumbs, BreadcrumbItem} from '@astryxdesign/core/Breadcrumbs';
import {navigate, onLinkClick} from './router';
import {appendWindow, orderSpans, spanChildren, toMessages, timelineLabels} from './traces';
import type {TextWindow} from './traces';
import {ErrorLine, pageStyles, getJSON, useJSON, fmtTime, fmtCost, fmtMs, spanFields} from './telemetry';
import type {Span, TraceDetail, SpanDetail, SpanField} from './telemetry';

const styles = stylex.create({
  trace: {display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 360px), 1fr))', gap: 16, alignItems: 'start'},
  card: {minWidth: 0},
  indent: {paddingInlineStart: 12},
  treeButton: {height: 'auto', minHeight: 44, padding: '6px 8px', borderRadius: 2, width: '100%', textAlign: 'start',
    backgroundColor: 'transparent', borderWidth: 0, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: 'transparent', whiteSpace: 'normal'},
  span: {display: 'grid', gridTemplateColumns: {default: '64px minmax(0, 1fr) auto 90px', '@media (max-width: 640px)': '64px minmax(0, 1fr) auto'},
    gap: 8, alignItems: 'center', width: '100%', textAlign: 'start'},
  spanSel: {backgroundColor: 'var(--color-background-muted)', borderLeftColor: 'var(--color-accent)'},
  pre: {margin: 0, padding: 10, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', backgroundColor: 'var(--color-background-muted)', borderRadius: 2},
  code: {whiteSpace: 'inherit', overflowWrap: 'anywhere', padding: 0},
});

function ContentText({text}: {text: string}) {
  return <pre {...stylex.props(styles.pre)}><Code xstyle={styles.code}>{text || '—'}</Code></pre>;
}
function Messages({text}: {text: string}) {
  const messages = toMessages(text);
  if (!messages) return <ContentText text={text} />;
  return <>{messages.map((message, index) => <VStack key={index} gap={1} width="100%">
    <Text type="supporting" color="secondary" weight="semibold">{message.role.toUpperCase()}</Text>
    <ContentText text={message.text} />
  </VStack>)}</>;
}

function SpanContent({id, selected, field, initial}: {id: string; selected: string; field: SpanField; initial: TextWindow}) {
  const [window, setWindow] = useState(initial);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const label = field[0].toUpperCase() + field.slice(1);
  const loadMore = async () => {
    if (window.next_offset === null || loading) return;
    setLoading(true);
    setError('');
    try {
      const query = new URLSearchParams({format: 'json', field, offset: String(window.next_offset), len: '2000'});
      const result = await getJSON<SpanDetail>(`/spans/${id}/${selected}?${query}`);
      setWindow(current => appendWindow(current, result[field]));
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setLoading(false);
    }
  };
  const contents = <VStack gap={2} width="100%">
    {(field === 'input' || field === 'output') && window.next_offset === null
      ? <Messages text={window.text} /> : <ContentText text={window.text} />}
    {window.invalid_utf8 && <Text color="accent">Invalid UTF-8 was replaced by the JSON API.</Text>}
    {error && <ErrorLine message={error} />}
    {window.next_offset !== null && <>
      <Text type="supporting" color="secondary">{window.next_offset} of {window.total_chars} characters loaded</Text>
      <Button label="Show more" aria-label={`Show more ${label}`} onClick={loadMore} isLoading={loading} />
    </>}
  </VStack>;
  return <section aria-label={label} data-field={field} data-next-offset={window.next_offset ?? 'done'}>
    {field === 'input' || field === 'output'
      ? <VStack gap={2}><Heading level={3}>{label}</Heading>{contents}</VStack>
      : <Collapsible trigger={<Text weight="semibold">{label}</Text>} defaultIsOpen={field === 'attributes'}>{contents}</Collapsible>}
  </section>;
}

export default function TracePage({id, spanId}: {id: string; spanId?: string}) {
  const {data: trace, error} = useJSON<TraceDetail>(`/traces/${id}?format=json`);
  const ordered = useMemo(() => trace ? orderSpans(trace.spans) : [], [trace]);
  const childrenByParent = useMemo(() => spanChildren(ordered), [ordered]);
  const selected = spanId ?? ordered.find(span => span.kind === 'llm')?.span_id ?? ordered[0]?.span_id ?? null;
  const detail = useJSON<SpanDetail>(selected ? `/spans/${id}/${selected}?format=json` : null);
  const total = Math.max(trace?.duration_ms ?? 1, 1);
  const pick = (span: string) => {
    navigate(`/spans/${id}/${span}`);
    if (matchMedia('(max-width: 640px)').matches) document.getElementById('panel')?.scrollIntoView({block: 'start'});
  };
  const renderSpan = (span: Span & {depth: number}): React.ReactNode => {
    const children = childrenByParent.get(span.span_id) ?? [];
    const offset = Math.min(Math.max(Math.round(span.start_offset_ms * 100 / total), 0), 99);
    const width = Math.min(Math.max(Math.round(span.duration_ms * 100 / total), 1), 100 - offset);
    return <VStack key={span.span_id} gap={1} width="100%">
      <Button label={span.name} data-span-id={span.span_id} aria-current={span.span_id === selected ? 'true' : undefined}
        variant="ghost" onClick={() => pick(span.span_id)} xstyle={[styles.treeButton, span.span_id === selected && styles.spanSel]}>
        <Grid xstyle={styles.span}>
          <Text type="supporting" weight="semibold" color={span.kind === 'llm' ? 'accent' : 'secondary'}>{span.kind.toUpperCase()}</Text>
          <VStack gap={0} align="start"><Text>{span.name}</Text><Text type="supporting" color="secondary">{span.model} {span.input_tokens != null || span.output_tokens != null ? `${span.input_tokens ?? '—'} / ${span.output_tokens ?? '—'}` : ''}</Text></VStack>
          <Text type="supporting" color="secondary">{fmtMs(span.duration_ms)}</Text>
          <svg className="waterfall" viewBox="0 0 100 8" preserveAspectRatio="none" aria-hidden="true">
            <rect x={offset} width={width} height="8" fill={span.kind === 'llm' ? 'var(--color-accent)' : 'var(--color-chart-neutral)'} />
          </svg>
        </Grid>
      </Button>
      {!!children.length && <VStack gap={1} width="100%" xstyle={styles.indent}>
        <Collapsible trigger={<Text type="supporting">Children of {span.name}</Text>}>{children.map(renderSpan)}</Collapsible>
      </VStack>}
    </VStack>;
  };
  return <main {...stylex.props(pageStyles.page)}>
    <Breadcrumbs label="Breadcrumb"><BreadcrumbItem href="/traces" onClick={onLinkClick}>Traces</BreadcrumbItem><BreadcrumbItem>{trace?.name ?? id}</BreadcrumbItem></Breadcrumbs>
    {error && <ErrorLine message={error} />}
    {!trace && !error && <Text role="status">Loading…</Text>}
    {trace && <>
      <HStack justify="between" width="100%" align="end" wrap="wrap" gap={2}>
        <VStack gap={1}><Heading level={1}>{trace.name}</Heading><Text color="secondary">{trace.service} · {fmtTime(trace.start_ns)} UTC</Text></VStack>
        <HStack gap={2} align="center" wrap="wrap">
          <Badge label={`${trace.span_count} spans`} /><Badge label={`${trace.input_tokens} / ${trace.output_tokens} tokens`} />
          <Badge label={fmtCost(trace.cost_usd)} /><Badge label={fmtMs(trace.duration_ms)} />
        </HStack>
      </HStack>
      <Grid xstyle={styles.trace}>
        <Card className="span-tree" xstyle={styles.card} padding={4} role="region" aria-label="Span tree">
          <VStack gap={2} width="100%">
            <Heading level={2}>Span tree</Heading>
            <HStack className="time-ruler" justify="between" width="100%" aria-label="Trace timeline">
              {timelineLabels(trace.duration_ms).map((label, index) => <Text key={index} type="supporting" color="secondary">{label}</Text>)}
            </HStack>
            {(childrenByParent.get('') ?? []).map(renderSpan)}
          </VStack>
        </Card>
        <Card id="panel" className="span-panel" xstyle={styles.card} padding={4}>
          {detail.error && <ErrorLine message={detail.error} />}
          {detail.loading && <Text role="status">Loading span…</Text>}
          {!selected && <Text>Select a span to inspect it.</Text>}
          {detail.data && <VStack gap={4} width="100%">
            <HStack gap={2} align="center" wrap="wrap">
              <Text type="supporting" weight="semibold" color={detail.data.kind === 'llm' ? 'accent' : 'secondary'}>{detail.data.kind.toUpperCase()}</Text>
              <Heading level={2}>{detail.data.name}</Heading>
              <Text type="supporting" color="secondary">{detail.data.span_id}</Text>
            </HStack>
            <Text role="status" type="supporting" color="secondary">Selected span: {detail.data.name}</Text>
            <MetadataList>
              {([['Model', detail.data.model || '—'], ['Provider', detail.data.provider || '—'],
                ['Tokens', `${detail.data.input_tokens ?? '—'} in / ${detail.data.output_tokens ?? '—'} out`],
                ['Cost', fmtCost(detail.data.cost_usd)], ['Duration', fmtMs(detail.data.duration_ms)],
                ['Start offset', '+' + fmtMs(Math.max(detail.data.start_offset_ms, 0))],
                ['Status', ({0: 'Unset', 1: 'OK', 2: 'Error'} as Record<number, string>)[detail.data.status_code] ?? '']]).map(([label, value]) =>
                <MetadataListItem key={label} label={label}><Text>{value}</Text></MetadataListItem>)}
            </MetadataList>
            {spanFields.map(field => <SpanContent key={selected + field} id={id} selected={selected!} field={field} initial={detail.data![field]} />)}
            {detail.data.status_message && <Collapsible trigger={<Text weight="semibold">Status message</Text>}><ContentText text={detail.data.status_message} /></Collapsible>}
          </VStack>}
        </Card>
      </Grid>
    </>}
  </main>;
}
