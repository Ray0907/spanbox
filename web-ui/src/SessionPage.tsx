import * as stylex from '@stylexjs/stylex';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {Card} from '@astryxdesign/core/Card';
import {Link} from '@astryxdesign/core/Link';
import {Badge} from '@astryxdesign/core/Badge';
import {Banner} from '@astryxdesign/core/Banner';
import {Breadcrumbs, BreadcrumbItem} from '@astryxdesign/core/Breadcrumbs';
import {onLinkClick} from './router';
import {sessionDetailQuery, turnLabel, countLabel, compactDuration, sessionTime} from './sessions';
import type {SessionDetail} from './sessions';
import {ErrorLine, HTTPError, pageStyles, useJSON, fmtCost} from './telemetry';

const styles = stylex.create({
  turns: {listStyle: 'none', padding: 0, margin: 0, display: 'grid', gap: 16},
  text: {whiteSpace: 'pre-wrap'},
  // Preserve reading order while keeping the timestamp at the outside edge, even after wrapping.
  turnMeta: {marginInlineStart: 'auto', flexDirection: 'row-reverse'},
});
export default function SessionPage({id}: {id: string}) {
  const {data, error, loading} = useJSON<SessionDetail>(sessionDetailQuery(id));
  const notFound = error instanceof HTTPError && error.status === 404;
  return <main id="main" tabIndex={-1} {...stylex.props(pageStyles.page)}>
    <Breadcrumbs label="Breadcrumb"><BreadcrumbItem href="/sessions" onClick={onLinkClick}>Sessions</BreadcrumbItem><BreadcrumbItem isCurrent>{id}</BreadcrumbItem></Breadcrumbs>
    <Heading level={1}>{notFound ? 'Session not found' : id}</Heading>
    {loading && <Text role="status">Loading…</Text>}
    {error && (notFound ? <Text>No session found for “{id}”.</Text> : <ErrorLine message={error} />)}
    {data && <>
      <Text className="session-totals">
        {countLabel(data.totals.turns, 'turn')} · {countLabel(data.totals.traces, 'trace')} · {data.totals.input_tokens ?? '—'} / {data.totals.output_tokens ?? '—'} tokens · {data.totals.cost_usd != null && <>{fmtCost(data.totals.cost_usd)} · </>}
        <Link href={'/traces?' + new URLSearchParams({session: data.session_id})} onClick={onLinkClick}>Trace list</Link>
      </Text>
      {data.truncated && <Banner status="info" role="status" title={`Showing the first ${data.traces.length} traces of this session.`} />}
      <ol {...stylex.props(styles.turns)}>
        {data.traces.map(turn => <li key={turn.trace_id} className="turn-card">
          <Card><VStack gap={3} width="100%">
            <HStack gap={2} wrap="wrap" justify="between" width="100%">
              <Heading level={2}>{turnLabel(turn)}</Heading>
              <HStack gap={2} xstyle={styles.turnMeta}>
                <Text type="supporting" color="secondary"><time dateTime={sessionTime(turn.start_ns)}>{sessionTime(turn.start_ns)}</time></Text>
                {turn.has_error && <Badge variant="error" label="error" />}
              </HStack>
            </HStack>
            {turn.prompt && <Text xstyle={styles.text}>{turn.prompt}</Text>}
            {turn.output && <Text xstyle={styles.text} color="secondary">{turn.output}</Text>}
            <HStack gap={3} wrap="wrap" width="100%">
              <Text>{countLabel(turn.span_count, 'span')}</Text>
              <Text>{turn.input_tokens ?? '—'} / {turn.output_tokens ?? '—'} tokens</Text>
              {turn.cost_usd != null && <Text>{fmtCost(turn.cost_usd)}</Text>}
              <Text>{compactDuration(turn.duration_ms)}</Text>
              <Link href={`/traces/${turn.trace_id}`} onClick={onLinkClick} aria-label={`Open trace for ${turnLabel(turn)}`}>Open trace</Link>
            </HStack>
          </VStack></Card>
        </li>)}
      </ol>
    </>}
  </main>;
}
