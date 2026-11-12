import type {ISODateTimeString} from '@astryxdesign/core/DateTimeInput';
export const rangeOptions = [
  {value: '', label: 'All retained'}, {value: '15m', label: '15 minutes'},
  {value: '1h', label: '1 hour'}, {value: '24h', label: '24 hours'},
  {value: '7d', label: '7 days'}, {value: 'custom', label: 'Custom'},
];
const filterNames = ['range', 'from', 'to', 'model', 'service', 'min_duration', 'session', 'user'] as const;
export type Filters = Record<typeof filterNames[number], string> & {errors: boolean};

export function filtersFromSearch(search: string): Filters {
  const query = new URLSearchParams(search);
  return {...Object.fromEntries(filterNames.map(name => [name, query.get(name) ?? ''])) as Omit<Filters, 'errors'>, errors: query.get('errors') === '1'};
}

export function filtersToSearch(filters: Filters): string {
  const query = new URLSearchParams(filterNames.map(name => [name, filters[name]]));
  if (filters.errors) query.set('errors', '1');
  return query.toString();
}

export type TextWindow = {text: string; total_chars: number; next_offset: number | null; invalid_utf8?: boolean};
export function appendWindow(current: TextWindow, next: TextWindow): TextWindow {
  return {...next, text: current.text + next.text, ...(current.invalid_utf8 ? {invalid_utf8: true} : {})};
}

export function orderSpans<T extends {span_id: string; parent_span_id: string; start_offset_ms: number}>(spans: T[]): (T & {depth: number})[] {
  const children = new Map<string, T[]>();
  const sorted = [...spans].sort((a, b) => a.start_offset_ms - b.start_offset_ms);
  for (const span of sorted) {
    const siblings = children.get(span.parent_span_id) ?? [];
    siblings.push(span);
    children.set(span.parent_span_id, siblings);
  }
  const known = new Set(spans.map(span => span.span_id));
  const visited = new Set<string>();
  const ordered: (T & {depth: number})[] = [];
  const walk = (root: T) => {
    const stack = [{span: root, depth: 0}];
    while (stack.length) {
      const {span, depth} = stack.pop()!;
      if (visited.has(span.span_id)) continue;
      visited.add(span.span_id);
      ordered.push({...span, depth});
      const descendants = children.get(span.span_id) ?? [];
      for (let i = descendants.length - 1; i >= 0; i--) stack.push({span: descendants[i], depth: depth + 1});
    }
  };
  for (const span of sorted) if (!span.parent_span_id || !known.has(span.parent_span_id)) walk(span);
  for (const span of sorted) if (!visited.has(span.span_id)) walk(span);
  return ordered;
}

export function spanChildren<T extends {span_id: string; depth: number}>(ordered: T[]): Map<string, T[]> {
  const children = new Map<string, T[]>();
  const parents: string[] = [];
  for (const span of ordered) {
    // Cap nesting at eight levels so deep traces keep every row without recursive render overflow.
    const depth = Math.min(span.depth, 8);
    const parent = depth ? parents[depth - 1] : '';
    const siblings = children.get(parent) ?? [];
    siblings.push(span);
    children.set(parent, siblings);
    parents[depth] = span.span_id;
  }
  return children;
}

export function timelineLabels(ms: number): string[] {
  const duration = Math.max(ms, 0);
  return ['0 ms', ...[.25, .5, .75, 1].map(part => {
    const value = duration * part;
    return value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}ms`;
  })];
}

export function toMessages(text: string): {role: string; text: string}[] | null {
  try {
    let value: unknown = JSON.parse(text);
    if (value && typeof value === 'object' && 'messages' in value) value = value.messages;
    if (typeof value === 'string') value = JSON.parse(value);
    if (!Array.isArray(value) || !value.length) return null;
    const messages = [];
    for (const message of value) {
      if (!message || typeof message !== 'object' || (message.role != null && typeof message.role !== 'string')) return null;
      let content = message.content;
      if (Array.isArray(message.parts)) {
        if (!message.parts.every((part: unknown) => part && typeof part === 'object' && 'content' in part && typeof part.content === 'string')) return null;
        content = message.parts.map((part: {content: string}) => part.content).join('\n\n');
      }
      if (typeof content !== 'string') return null;
      messages.push({role: message.role ?? 'message', text: content});
    }
    return messages;
  } catch {
    return null;
  }
}

// DateTimeInput holds a zone-less "YYYY-MM-DDTHH:MM". It is read as the UTC wall clock, so no Date
// local-time conversion can shift the instant. The server takes RFC 3339 in `from`/`to`.
export const fromPickerValue = (value: string | undefined) => !value ? '' : value.length === 16 ? `${value}:00Z` : `${value}Z`;
export function toPickerValue(rfc3339: string): ISODateTimeString | undefined {
  const ms = Date.parse(rfc3339);
  return Number.isNaN(ms) ? undefined : new Date(ms).toISOString().slice(0, 16) as ISODateTimeString;
}
