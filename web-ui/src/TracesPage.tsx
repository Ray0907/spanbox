import {useMemo, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {TextInput} from '@astryxdesign/core/TextInput';
import {SegmentedControl, SegmentedControlItem} from '@astryxdesign/core/SegmentedControl';
import {CheckboxInput} from '@astryxdesign/core/CheckboxInput';
import {Button} from '@astryxdesign/core/Button';
import {Link} from '@astryxdesign/core/Link';
import {Badge} from '@astryxdesign/core/Badge';
import {Table, proportional, pixel} from '@astryxdesign/core/Table';
import {navigate, onLinkClick} from './router';
import {filtersFromSearch, filtersToSearch} from './traces';
import {RangeSelector, CustomBounds} from './RangeFilter';
import type {Filters} from './traces';
import {ErrorLine, pageStyles, getJSON, useJSON, fmtTime, fmtCost, fmtMs} from './telemetry';
import type {TraceItem} from './telemetry';

const styles = stylex.create({
  num: {fontVariantNumeric: 'tabular-nums', textAlign: 'right', display: 'block'},
});

export default function Traces({search}: {search: string}) {
  const [draft, setDraft] = useState(() => filtersFromSearch(search));
  const [density, setDensity] = useState<'compact' | 'balanced'>('compact');
  const [extra, setExtra] = useState<TraceItem[]>([]);
  const [tail, setTail] = useState<string | null>();
  const [paging, setPaging] = useState(false);
  const [pagingError, setPagingError] = useState('');
  const query = new URLSearchParams(search);
  query.set('format', 'json');
  if (!query.has('limit')) query.set('limit', '50');
  const {data, error, loading} = useJSON<{items: TraceItem[]; next_cursor: string | null}>(`/?${query}`);
  const rows = useMemo(() => [...(data?.items ?? []), ...extra], [data, extra]);
  const next = tail === undefined ? data?.next_cursor : tail;
  const loadMore = async () => {
    if (!next || paging) return;
    setPaging(true);
    setPagingError('');
    query.set('cursor', next);
    try {
      const page = await getJSON<{items: TraceItem[]; next_cursor: string | null}>(`/?${query}`);
      setExtra(previous => [...previous, ...page.items]);
      setTail(page.next_cursor);
    } catch (error) {
      setPagingError((error as Error).message);
    } finally {
      setPaging(false);
    }
  };
  const field = (name: Exclude<keyof Filters, 'errors'>) => ({
    htmlName: name, value: draft[name], onChange: (value: string) => setDraft({...draft, [name]: value}),
  });
  const exportQuery = new URLSearchParams(search);
  for (const name of ['cursor', 'partial', 'format', 'limit']) exportQuery.delete(name);

  return <main {...stylex.props(pageStyles.page)}>
    <HStack justify="between" width="100%" align="end" wrap="wrap" gap={2}>
      <VStack gap={1}><Heading level={1}>Traces</Heading><Text color="secondary">Requests, model calls, tools, and their full telemetry.</Text></VStack>
      <Link href={`/export?${exportQuery}`}>Export NDJSON</Link>
    </HStack>
    <SegmentedControl label="Table density" value={density} onChange={value => setDensity(value === 'compact' ? 'compact' : 'balanced')}>
      <SegmentedControlItem value="compact" label="Compact" /><SegmentedControlItem value="balanced" label="Comfortable" />
    </SegmentedControl>
    <form onSubmit={event => { event.preventDefault(); navigate('/traces?' + filtersToSearch(draft)); }}>
      <VStack gap={2} align="start" width="100%">
        <RangeSelector value={draft.range} onChange={range => setDraft({...draft, range})} />
        {draft.range === 'custom' && <CustomBounds from={draft.from} to={draft.to} onChange={(name, value) => setDraft({...draft, [name]: value})} />}
        <HStack gap={2} align="end" wrap="wrap" width="100%">
          <TextInput label="Model" placeholder="gpt-4o" {...field('model')} />
          <TextInput label="Service" placeholder="api" {...field('service')} />
          <TextInput label="Min duration (ms)" inputMode="decimal" {...field('min_duration')} />
          <TextInput label="Session" {...field('session')} />
          <TextInput label="User" {...field('user')} />
          <CheckboxInput label="Errors only" htmlName="errors" value={draft.errors} onChange={errors => setDraft({...draft, errors})} />
          <Button label="Apply" variant="primary" type="submit" />
        </HStack>
      </VStack>
    </form>
    {error ? <ErrorLine message={error} /> : <Text type="supporting" color="secondary" role="status">{loading ? 'Loading…' : `${rows.length} shown`}</Text>}
    <Table<TraceItem & Record<string, unknown>> data={rows} density={density} hasHover columns={[
      {key: 'start_ns', header: 'Time (UTC)', width: pixel(150), renderCell: row => fmtTime(row.start_ns)},
      {key: 'name', header: 'Name', width: proportional(1.4), renderCell: row => <Link href={`/traces/${row.trace_id}`} onClick={onLinkClick}>{row.name}</Link>},
      {key: 'service', header: 'Service', width: proportional(1)},
      {key: 'models', header: 'Models', width: proportional(1.2), renderCell: row => row.models || '—'},
      {key: 'span_count', header: 'Spans', width: pixel(60), renderCell: row => <Text xstyle={styles.num}>{row.span_count}</Text>},
      {key: 'input_tokens', header: 'Tokens in / out', width: pixel(120), renderCell: row => <Text xstyle={styles.num}>{row.input_tokens} / {row.output_tokens}</Text>},
      {key: 'cost_usd', header: 'Cost', width: pixel(90), renderCell: row => <Text xstyle={styles.num}>{fmtCost(row.cost_usd)}</Text>},
      {key: 'duration_ms', header: 'Duration', width: pixel(100), renderCell: row => <Text xstyle={styles.num}>{fmtMs(row.duration_ms)}</Text>},
      {key: 'has_error', header: 'Status', width: pixel(80), renderCell: row => <Badge variant={row.has_error ? 'error' : 'success'} label={row.has_error ? 'ERROR' : 'OK'} />},
    ]} />
    {!loading && !error && !rows.length && <Text>No traces match these filters. <Link href="/traces" onClick={onLinkClick}>Clear filters</Link></Text>}
    {pagingError && <ErrorLine message={pagingError} />}
    {next && !loading && <HStack><Button label="Load more" onClick={loadMore} isLoading={paging} /></HStack>}
  </main>;
}
