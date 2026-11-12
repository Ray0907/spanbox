export type SearchHit = {
  trace_id: string; span_id: string; trace_name: string; span_name: string;
  kind: string; model: string; snippet: string; start_ns: number;
};
export type SearchPage = {items: SearchHit[]; next_cursor: string | null};
export const searchQuery = (search: string) => new URLSearchParams(search).get('q') ?? '';
// HTML search has always shown the first 100 recent hits, with no paging controls.
export function searchPageQuery(q: string, cursor: string | null, shown: number) {
  if (!q || shown >= 100 || (shown > 0 && !cursor)) return null;
  const query = new URLSearchParams({q, format: 'json', limit: String(Math.min(50, 100 - shown))});
  if (cursor) query.set('cursor', cursor);
  return '/search?' + query;
}

// The server marks matches with SQLite snippet() '[' and ']' and never escapes the text, so a literal
// bracket is ambiguous. A '[' opens a match only when a ']' follows with no other '[' between.
export function snippetParts(snippet: string): {text: string; match: boolean}[] {
  const parts: {text: string; match: boolean}[] = [];
  const push = (text: string, match: boolean) => { if (text) parts.push({text, match}); };
  let plain = '', i = 0;
  while (i < snippet.length) {
    const open = snippet[i] === '[';
    const close = open ? snippet.indexOf(']', i + 1) : -1;
    if (close > i + 1 && !snippet.slice(i + 1, close).includes('[')) {
      push(plain, false); plain = '';
      push(snippet.slice(i + 1, close), true);
      i = close + 1;
    } else plain += snippet[i++];
  }
  push(plain, false);
  return parts;
}
