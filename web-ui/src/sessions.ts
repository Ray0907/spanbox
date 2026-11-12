export type SessionRow = {
  session_id: string; service: string; models: string; first_ns: number; last_ns: number;
  trace_count: number; llm_count: number; input_tokens: number | null; output_tokens: number | null; cost_usd: number | null;
};
export type SessionTurn = {
  trace_id: string; name: string; has_root: boolean; number: number; prompt: string; output: string;
  start_ns: number; span_count: number; input_tokens: number | null; output_tokens: number | null;
  cost_usd: number | null; duration_ms: number; has_error: boolean;
};
export type SessionDetail = {
  session_id: string; traces: SessionTurn[]; truncated: boolean;
  totals: {turns: number; traces: number; input_tokens: number | null; output_tokens: number | null; cost_usd: number | null};
};
export function sessionListQuery(search: string) {
  const input = new URLSearchParams(search);
  const query = new URLSearchParams();
  for (const name of ['range', 'from', 'to', 'cursor']) if (input.has(name)) query.set(name, input.get(name)!);
  query.set('format', 'json');
  query.set('limit', '50');
  return query;
}
export const sessionFiltersToSearch = (filters: {range: string; from: string; to: string}) =>
  new URLSearchParams({range: filters.range, from: filters.from, to: filters.to}).toString();
export const sessionDetailQuery = (id: string) => '/sessions?' + new URLSearchParams({id, format: 'json'});
export function sessionNextURL(search: string, cursor: string | null) {
  if (!cursor) return null;
  const query = new URLSearchParams(search);
  query.delete('format');
  query.set('cursor', cursor);
  return '/sessions?' + query;
}
export const turnLabel = (turn: Pick<SessionTurn, 'has_root' | 'number' | 'name'>) => turn.has_root ? `Turn ${turn.number}` : turn.name;
export const countLabel = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`;
// shortcut: JS tie rounding differs from Go; use exact formatting if last-digit UI parity becomes necessary.
export const compactDuration = (ms: number) => ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`;
// shortcut: Date displays milliseconds; preserve raw timestamps if sub-ms UI timing becomes necessary.
export const sessionTime = (ns: number) => new Date(ns / 1e6).toISOString();
