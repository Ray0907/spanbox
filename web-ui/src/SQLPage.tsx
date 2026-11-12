import {useRef, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {VStack} from '@astryxdesign/core/Layout';
import {Heading, Text} from '@astryxdesign/core/Text';
import {TextArea} from '@astryxdesign/core/TextArea';
import {Button} from '@astryxdesign/core/Button';
import {Collapsible} from '@astryxdesign/core/Collapsible';
import {Banner} from '@astryxdesign/core/Banner';
import {Table, proportional} from '@astryxdesign/core/Table';
import {useJSON} from './telemetry';
import {sqlBodyLimit, sqlTable, type SQLResult} from './dashboard-sql';

const styles = stylex.create({page: {padding: 24, display: 'flex', flexDirection: 'column', gap: 24}, cell: {whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}});
export default function SQLPage() {
  const {data: schema, error: schemaError} = useJSON<{tables: {name: string; columns: string[]}[]}>('/sql?format=json');
  const [query, setQuery] = useState('');
  const [result, setResult] = useState<SQLResult | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const form = useRef<HTMLFormElement>(null);
  const body = new URLSearchParams({sql: query});
  const tooLarge = body.toString().length > sqlBodyLimit; // Form encoding is ASCII, so length is the byte count.
  async function run() {
    if (busy) return;
    if (tooLarge) {setError('invalid form'); setResult(null); return;}
    setBusy(true); setError(''); setResult(null);
    try {
      const response = await fetch('/sql?format=json', {method: 'POST', headers: {Accept: 'application/json'}, body});
      if (response.status === 401) {location.assign('/login'); return;}
      const json = await response.json();
      if (!response.ok) {setError(json.error || `SQL failed (${response.status})`); return;}
      try {setResult(sqlTable(json));} catch {setError('Unexpected response from the server.');}
    } catch {setError('Could not run the query. Please try again.');}
    finally {setBusy(false);}
  }
  return <main id="main" {...stylex.props(styles.page)}>
    <VStack gap={2}><Heading level={1}>SQL console</Heading><Text color="secondary">Read-only access for authenticated operators. Queries stop after five seconds; results are limited to 1,000 rows.</Text></VStack>
    {schemaError && <Banner status="error" title={schemaError.message} role="alert" />}
    <Collapsible trigger="Schema" defaultIsOpen={false}><VStack gap={3}>{schema?.tables.map(table => <VStack key={table.name} gap={1}>
      <Text weight="semibold">{table.name}</Text><Text>{table.columns.join(', ')}</Text>
    </VStack>)}</VStack></Collapsible>
    <form ref={form} method="post" action="/sql" onSubmit={event => {event.preventDefault(); void run();}} onKeyDown={event => {
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {event.preventDefault(); form.current?.requestSubmit();}
    }}><VStack gap={3}>
      <TextArea ref={input => {if (input) input.required = true;}} label="SQL query" htmlName="sql" value={query} onChange={setQuery} rows={9} width="100%" hasSpellCheck={false} isRequired />
      {tooLarge && <Banner status="warning" title="The encoded form exceeds 20 KiB. Shorten the query before running." />}
      <Button label={busy ? 'Running…' : 'Run'} type="submit" variant="primary" isDisabled={busy} />
    </VStack></form>
    {error && <Banner status="error" title={error} role="alert" />}
    {result && <>
      {result.truncated && <Banner status="warning" title="Results truncated." description="Limits: 1,000 rows, 64 KiB per cell, and 4 MiB of result text." />}
      <Text role="status">{result.rows.length} rows</Text>
      {!result.rows.length && <Text>Query returned no rows.</Text>}
      {result.columns.length > 0 && <Table data={result.rows.map(cells => ({cells}))} density="compact"
        columns={result.columns.map((header, i) => ({key: `column-${i}`, header, width: proportional(1), renderCell: (row: {cells: string[]}) => <Text xstyle={styles.cell}>{row.cells[i]}</Text>}))} />}
    </>}
    <Heading level={2}>Examples</Heading>
    <Text>SELECT * FROM traces ORDER BY start_ns DESC LIMIT 20;</Text>
    <Text>SELECT request_model, count(*) FROM spans WHERE kind='llm' GROUP BY request_model;</Text>
    <Text>SELECT trace_id, name, cost_usd FROM traces WHERE has_error=1;</Text>
  </main>;
}
