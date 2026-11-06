// spanbox UI built from Astryx components. Reads spanbox's own ?format=json endpoints (same origin, cookie auth).
import {useCallback, useEffect, useMemo, useState} from 'react';
import * as stylex from '@stylexjs/stylex';
import {Theme} from '@astryxdesign/core/theme';
import {neutralTheme} from '@astryxdesign/theme-neutral/built';
import {HStack, VStack} from '@astryxdesign/core/Layout';
import {Text, Heading} from '@astryxdesign/core/Text';
import {TextInput} from '@astryxdesign/core/TextInput';
import {Button} from '@astryxdesign/core/Button';
import {Link} from '@astryxdesign/core/Link';
import {Badge} from '@astryxdesign/core/Badge';
import {Table, proportional, pixel} from '@astryxdesign/core/Table';

type TraceItem = {
  trace_id: string; name: string; service: string; models: string; start_ns: number;
  span_count: number; input_tokens: number; output_tokens: number; cost_usd: number | null;
  duration_ms: number; has_error: boolean;
};
type Span = {
  span_id: string; parent_span_id: string; kind: string; name: string; model: string;
  duration_ms: number; start_offset_ms: number; input_tokens: number | null; output_tokens: number | null;
  cost_usd: number | null; status_code: number;
};
type TraceDetail = TraceItem & {session_id: string; spans: Span[]};
type SpanDetail = Span & {
  provider: string; status_message: string;
  input: {text: string}; output: {text: string}; attributes: {text: string};
};
type Msg = {role: string; text: string};

async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, {headers: {Accept: 'application/json'}});
  if (r.status === 401) {
    location.href = '/login';
    throw new Error('unauthorized');
  }
  if (!r.ok) throw new Error(`${r.status} ${(await r.text()).slice(0, 120)}`);
  return r.json() as Promise<T>;
}

function useJSON<T>(url: string | null) {
  const [state, set] = useState<{data?: T; error?: string; url?: string}>({});
  useEffect(() => {
    if (!url) return;
    let live = true;
    getJSON<T>(url).then(
      (data) => live && set({data, url}),
      (e: Error) => live && set({error: e.message, url}),
    );
    return () => { live = false; };
  }, [url]);
  const ready = state.url === url;
  return {data: ready ? state.data : undefined, error: ready ? state.error : undefined, loading: !!url && !ready};
}

function toMessages(text: string): Msg[] | null {
  try {
    let v: unknown = JSON.parse(text);
    if (v && typeof v === 'object' && !Array.isArray(v) && typeof (v as {messages?: unknown}).messages === 'string') {
      v = JSON.parse((v as {messages: string}).messages);
    }
    if (!Array.isArray(v)) return null;
    return v.map((m: {role?: string; parts?: {content?: string}[]; content?: string}) => ({
      role: m.role ?? 'message',
      text: m.parts ? m.parts.map((p) => p.content ?? '').join('') : (m.content ?? ''),
    }));
  } catch {
    return null;
  }
}

const fmtTime = (ns: number) => new Date(ns / 1e6).toISOString().replace('T', ' ').slice(0, 19);
const fmtCost = (c: number | null | undefined) => (c == null ? '—' : `$${c.toFixed(6)}`);
const fmtMs = (ms: number) => `${ms.toFixed(1)} ms`;

const styles = stylex.create({
  nav: {display: 'flex', flexWrap: 'wrap', gap: '8px 24px', alignItems: 'center', padding: '12px 24px',
    borderBottomWidth: 1, borderBottomStyle: 'solid', borderBottomColor: 'var(--color-border)'},
  page: {padding: 24, display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', alignContent: 'start', gap: 20, backgroundColor: 'var(--color-background-body)', minHeight: '100vh'},
  trace: {display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 360px), 1fr))', gap: 16, alignItems: 'start'},
  card: {borderWidth: 1, borderStyle: 'solid', borderColor: 'var(--color-border)', borderRadius: 2, padding: 16},
  span: {display: 'grid', gridTemplateColumns: {default: '84px minmax(0, 1fr) 64px 90px', '@media (max-width: 640px)': '84px minmax(0, 1fr) auto'},
    gap: 8, alignItems: 'center', padding: '6px 8px', borderRadius: 2, width: '100%', textAlign: 'start',
    backgroundColor: {default: 'transparent', ':hover': 'var(--color-background-muted)'},
    borderWidth: 0, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: 'transparent', cursor: 'pointer', color: 'inherit', font: 'inherit'},
  spanSel: {backgroundColor: 'var(--color-background-muted)', borderLeftColor: 'var(--color-accent)'},
  track: {height: 6, borderRadius: 2, backgroundColor: 'var(--color-background-muted)', position: 'relative',
    gridColumn: {default: 'auto', '@media (max-width: 640px)': '1 / -1'}},
  num: {fontVariantNumeric: 'tabular-nums', textAlign: 'right', display: 'block'},
  code: {fontFamily: 'var(--font-family-code)', fontSize: 'var(--text-supporting-size)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
    backgroundColor: 'var(--color-background-muted)', padding: 10, borderRadius: 2},
});

function Nav({route}: {route: string}) {
  const items: [string, string, boolean][] = [
    ['Traces', '#/', route === 'traces' || route === 'trace'],
  ];
  return (
    <nav aria-label="Primary" {...stylex.props(styles.nav)}>
      <Text weight="semibold">spanbox</Text>
      {items.map(([label, href, on]) => (
        <Button key={label} label={label} size="sm" href={href} aria-current={on ? 'page' : undefined} variant={on ? 'secondary' : 'ghost'} />
      ))}
      <Text type="supporting" color="secondary">Other pages: <Link href="/sessions">Sessions</Link> · <Link href="/dashboard">Dashboard</Link> · <Link href="/search">Search</Link></Text>
    </nav>
  );
}

function ErrorLine({message}: {message: string}) {
  return <Text color="accent" role="alert">Unable to load data: {message}</Text>;
}

function Traces() {
  const [draft, setDraft] = useState({model: '', service: '', min_duration: '', session: ''});
  const [query, setQuery] = useState('limit=50');
  const [extra, setExtra] = useState<TraceItem[]>([]);
  const [cursor, setCursor] = useState<string | null | undefined>(undefined);
  const {data, error, loading} = useJSON<{items: TraceItem[]; next_cursor: string | null}>(`/?format=json&${query}`);

  useEffect(() => { setExtra([]); setCursor(undefined); }, [query]);
  const more = useJSON<{items: TraceItem[]; next_cursor: string | null}>(cursor ? `/?format=json&${query}&cursor=${encodeURIComponent(cursor)}` : null);
  useEffect(() => {
    if (more.data) { setExtra((e) => [...e, ...more.data!.items]); setCursor(undefined); setTail(more.data.next_cursor); }
  }, [more.data]);
  const [tail, setTail] = useState<string | null>(null);
  useEffect(() => { if (data) setTail(data.next_cursor); }, [data]);

  const rows = useMemo(() => [...(data?.items ?? []), ...extra], [data, extra]);
  const apply = () => {
    const p = new URLSearchParams({limit: '50'});
    for (const [k, v] of Object.entries(draft)) if (v.trim()) p.set(k, v.trim());
    setQuery(p.toString());
  };
  const field = (k: keyof typeof draft) => ({value: draft[k], onChange: (v: unknown) => setDraft({...draft, [k]: typeof v === 'string' ? v : (v as {target: {value: string}}).target.value})});
  const exportQuery = new URLSearchParams(query); exportQuery.delete('limit');

  return (
    <main {...stylex.props(styles.page)}>
      <HStack justify="between" width="100%" align="end" wrap="wrap" gap={2}>
        <VStack gap={1}>
          <Heading level={1}>Traces</Heading>
          <Text color="secondary">Requests, model calls, tools, and their full telemetry.</Text>
        </VStack>
        <Link href={`/export?${exportQuery.toString()}`}>Export NDJSON</Link>
      </HStack>
      <form onSubmit={(e) => { e.preventDefault(); apply(); }}>
        <HStack gap={2} align="end" wrap="wrap">
          <TextInput label="Model" placeholder="gpt-4o" {...field('model')} />
          <TextInput label="Service" placeholder="api" {...field('service')} />
          <TextInput label="Min duration (ms)" {...field('min_duration')} />
          <TextInput label="Session" {...field('session')} />
          <Button label="Apply" variant="primary" type="submit" />
        </HStack>
      </form>
      {error ? <ErrorLine message={error} /> : (
        <Text type="supporting" color="secondary" role="status">{loading ? 'Loading…' : `${rows.length} shown`}</Text>
      )}
      <Table<TraceItem & Record<string, unknown>> data={rows} density="compact" hasHover
        columns={[
          {key: 'start_ns', header: 'Time (UTC)', width: pixel(150), renderCell: (r) => fmtTime(r.start_ns)},
          {key: 'name', header: 'Name', width: proportional(1.4), renderCell: (r) => <Link href={`#/t/${r.trace_id}`}>{r.name}</Link>},
          {key: 'service', header: 'Service', width: proportional(1)},
          {key: 'models', header: 'Models', width: proportional(1.2)},
          {key: 'span_count', header: <span {...stylex.props(styles.num)}>Spans</span>, width: pixel(60), renderCell: (r) => <span {...stylex.props(styles.num)}>{r.span_count}</span>},
          {key: 'input_tokens', header: <span {...stylex.props(styles.num)}>Tokens in / out</span>, width: pixel(120), renderCell: (r) => <span {...stylex.props(styles.num)}>{r.input_tokens} / {r.output_tokens}</span>},
          {key: 'cost_usd', header: <span {...stylex.props(styles.num)}>Cost</span>, width: pixel(90), renderCell: (r) => <span {...stylex.props(styles.num)}>{fmtCost(r.cost_usd)}</span>},
          {key: 'duration_ms', header: <span {...stylex.props(styles.num)}>Duration</span>, width: pixel(100), renderCell: (r) => <span {...stylex.props(styles.num)}>{fmtMs(r.duration_ms)}</span>},
          {key: 'has_error', header: 'Status', width: pixel(80), renderCell: (r) => <Badge variant={r.has_error ? 'error' : 'success'} label={r.has_error ? 'ERROR' : 'OK'} />},
        ]} />
      {tail && !loading && <HStack><Button label="Load more" onClick={() => setCursor(tail)} isLoading={more.loading} /></HStack>}
    </main>
  );
}

function Messages({text}: {text: string}) {
  const msgs = toMessages(text);
  if (!msgs) return <div {...stylex.props(styles.code)}>{text || '—'}</div>;
  return (
    <>
      {msgs.map((m, i) => (
        <VStack key={i} gap={1}>
          <Text type="supporting" color="secondary" weight="semibold">{m.role.toUpperCase()}</Text>
          <div {...stylex.props(styles.code)}>{m.text}</div>
        </VStack>
      ))}
    </>
  );
}

function orderSpans(spans: Span[]): (Span & {depth: number})[] {
  const kids = new Map<string, Span[]>();
  for (const s of spans) kids.set(s.parent_span_id, [...(kids.get(s.parent_span_id) ?? []), s]);
  const known = new Set(spans.map((s) => s.span_id));
  const roots = spans.filter((s) => !s.parent_span_id || !known.has(s.parent_span_id));
  const out: (Span & {depth: number})[] = [];
  const walk = (s: Span, depth: number) => {
    out.push({...s, depth});
    [...(kids.get(s.span_id) ?? [])].sort((a, b) => a.start_offset_ms - b.start_offset_ms).forEach((c) => walk(c, depth + 1));
  };
  roots.sort((a, b) => a.start_offset_ms - b.start_offset_ms).forEach((r) => walk(r, 0));
  return out;
}

function TracePage({id}: {id: string}) {
  const {data: trace, error} = useJSON<TraceDetail>(`/traces/${id}?format=json`);
  const ordered = useMemo(() => (trace ? orderSpans(trace.spans) : []), [trace]);
  const [picked, setPicked] = useState<string | null>(null);
  const selected = picked ?? ordered.find((s) => s.kind === 'llm')?.span_id ?? ordered[0]?.span_id ?? null;
  const detail = useJSON<SpanDetail>(selected ? `/spans/${id}/${selected}?format=json` : null);
  const total = Math.max(trace?.duration_ms ?? 1, 1);
  const pick = useCallback((s: string) => setPicked(s), []);

  return (
    <main {...stylex.props(styles.page)}>
      <VStack gap={1}>
        <Text type="supporting" color="secondary"><Link href="#/">Traces</Link> / {trace?.name ?? id}</Text>
        {error && <ErrorLine message={error} />}
        {trace && (
          <HStack justify="between" width="100%" align="end" wrap="wrap" gap={2}>
            <VStack gap={1}>
              <Heading level={1}>{trace.name}</Heading>
              <Text color="secondary">{trace.service} · {fmtTime(trace.start_ns)} UTC</Text>
            </VStack>
            <HStack gap={2} align="center" wrap="wrap">
              <Badge label={`${trace.span_count} spans`} />
              <Badge label={`${trace.input_tokens} / ${trace.output_tokens} tokens`} />
              <Badge label={fmtCost(trace.cost_usd)} />
              <Badge label={fmtMs(trace.duration_ms)} />
            </HStack>
          </HStack>
        )}
      </VStack>
      {trace && (
        <div {...stylex.props(styles.trace)}>
          <div {...stylex.props(styles.card)}>
            <Heading level={2}>Span tree</Heading>
            <VStack gap={1}>
              {ordered.map((s) => (
                <button key={s.span_id} type="button" aria-current={s.span_id === selected ? 'true' : undefined}
                  onClick={() => pick(s.span_id)} {...stylex.props(styles.span, s.span_id === selected && styles.spanSel)}>
                  <Text type="supporting" weight="semibold" style={{color: s.kind === 'llm' ? 'var(--color-accent)' : 'var(--color-text-secondary)'}}>{s.kind.toUpperCase()}</Text>
                  <Text style={{paddingInlineStart: s.depth * 12}}>{s.name}</Text>
                  <Text type="supporting" color="secondary">{Math.round(s.duration_ms)} ms</Text>
                  <div {...stylex.props(styles.track)}>
                    <div style={{position: 'absolute', insetInlineStart: `${(s.start_offset_ms / total) * 100}%`, width: `${Math.max((s.duration_ms / total) * 100, 1)}%`, height: 6, borderRadius: 2,
                      background: s.kind === 'llm' ? 'var(--color-accent)' : 'var(--color-chart-neutral)'}} />
                  </div>
                </button>
              ))}
            </VStack>
          </div>
          <div {...stylex.props(styles.card)} aria-live="polite">
            {detail.error && <ErrorLine message={detail.error} />}
            {detail.data && (
              <VStack gap={4}>
                <HStack gap={2} align="center">
                  <Text type="supporting" weight="semibold" style={{color: detail.data.kind === 'llm' ? 'var(--color-accent)' : 'var(--color-text-secondary)'}}>{detail.data.kind.toUpperCase()}</Text>
                  <Heading level={2}>{detail.data.name}</Heading>
                </HStack>
                <HStack gap={6} wrap="wrap">
                  {([['Model', detail.data.model || '—'], ['Provider', detail.data.provider || '—'],
                    ['Tokens', detail.data.input_tokens == null ? '—' : `${detail.data.input_tokens} in / ${detail.data.output_tokens ?? 0} out`],
                    ['Cost', fmtCost(detail.data.cost_usd)], ['Duration', fmtMs(detail.data.duration_ms)]] as [string, string][]).map(([k, v]) => (
                    <VStack key={k} gap={0}><Text type="supporting" color="secondary">{k}</Text><Text>{v}</Text></VStack>
                  ))}
                </HStack>
                {detail.data.status_message && <Text color="accent">{detail.data.status_message}</Text>}
                <VStack gap={2}><Heading level={3}>Input</Heading><Messages text={detail.data.input?.text ?? ''} /></VStack>
                <VStack gap={2}><Heading level={3}>Output</Heading><Messages text={detail.data.output?.text ?? ''} /></VStack>
              </VStack>
            )}
          </div>
        </div>
      )}
    </main>
  );
}

function useRoute() {
  const parse = () => location.hash.replace(/^#\/?/, '');
  const [h, setH] = useState(parse());
  useEffect(() => {
    const f = () => setH(parse());
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);
  return h;
}

export default function App() {
  const h = useRoute();
  const m = /^t\/([0-9a-f]+)$/.exec(h);
  return (
    <Theme theme={neutralTheme}>
      <Nav route={m ? 'trace' : 'traces'} />
      {m ? <TracePage key={m[1]} id={m[1]} /> : <Traces />}
    </Theme>
  );
}
