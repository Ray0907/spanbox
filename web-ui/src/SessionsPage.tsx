import {useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {Button} from '@astryxdesign/core/Button';
import {Link} from '@astryxdesign/core/Link';
import {Table, pixel, proportional} from '@astryxdesign/core/Table';
import {navigate, onLinkClick} from './router';
import {filtersFromSearch} from './traces';
import {RangeSelector, CustomBounds} from './RangeFilter';
import {sessionListQuery, sessionNextURL, sessionFiltersToSearch, countLabel, sessionTime} from './sessions';
import type {SessionRow} from './sessions';
import {ErrorLine, pageStyles, useJSON, fmtCost} from './telemetry';

export default function SessionsPage({search}: {search: string}) {
  const [draft, setDraft] = useState(() => filtersFromSearch(search));
  const {data, error, loading} = useJSON<{items: SessionRow[]; next_cursor: string | null}>('/sessions?' + sessionListQuery(search));
  const next = sessionNextURL(search, data?.next_cursor ?? null);
  return <main id="main" tabIndex={-1} {...stylex.props(pageStyles.page)}>
    <VStack gap={1}><Heading level={1}>Sessions</Heading><Text color="secondary">Model calls grouped by conversation.</Text></VStack>
    <form onSubmit={event => {
      event.preventDefault();
      navigate('/sessions?' + sessionFiltersToSearch(draft));
    }}>
      <VStack gap={2} align="start">
        <RangeSelector value={draft.range} onChange={range => setDraft({...draft, range})} />
        {draft.range === 'custom' && <CustomBounds from={draft.from} to={draft.to} onChange={(name, value) => setDraft({...draft, [name]: value})} />}
        <HStack gap={2} align="end" wrap="wrap">
          <Button label="Apply" type="submit" variant="primary" />
        </HStack>
      </VStack>
    </form>
    {error ? <ErrorLine message={error} /> : <Text role="status" type="supporting" color="secondary">{loading ? 'Loading…' : `${data?.items.length ?? 0} shown`}</Text>}
    <Table<SessionRow & Record<string, unknown>> data={data?.items ?? []} density="compact" hasHover columns={[
      {key: 'last_ns', header: 'Last seen', width: pixel(190), renderCell: row => <time dateTime={sessionTime(row.last_ns)}>{sessionTime(row.last_ns)}</time>},
      {key: 'session_id', header: 'Session', width: proportional(1.5), renderCell: row => <Link href={'/sessions?' + new URLSearchParams({id: row.session_id})} onClick={onLinkClick}>{row.session_id}</Link>},
      {key: 'service', header: 'Service', width: proportional(1)},
      {key: 'models', header: 'Models', width: proportional(1), renderCell: row => row.models || '—'},
      {key: 'trace_count', header: 'Traces', width: pixel(70), renderCell: row => <Link href={'/traces?' + new URLSearchParams({session: row.session_id})} onClick={onLinkClick} aria-label={`${countLabel(row.trace_count, 'trace')} in ${row.session_id}`}>{row.trace_count}</Link>},
      {key: 'llm_count', header: 'LLM calls', width: pixel(90)},
      {key: 'input_tokens', header: 'Tokens in / out', width: pixel(120), renderCell: row => `${row.input_tokens ?? '—'} / ${row.output_tokens ?? '—'}`},
      {key: 'cost_usd', header: 'Cost', width: pixel(100), renderCell: row => fmtCost(row.cost_usd)},
      {key: 'first_ns', header: 'First seen', width: pixel(190), renderCell: row => <time dateTime={sessionTime(row.first_ns)}>{sessionTime(row.first_ns)}</time>},
    ]} />
    {!loading && !error && !data?.items.length && <Text>No sessions match these filters. <Link href="/sessions" onClick={onLinkClick}>Clear filters</Link></Text>}
    {next && <nav aria-label="Pagination"><Button label="Next page" href={next} onClick={onLinkClick} /></nav>}
  </main>;
}
