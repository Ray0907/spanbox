import test from 'node:test';
import assert from 'node:assert/strict';
import {filtersFromSearch, filtersToSearch, rangeOptions, appendWindow, orderSpans, toMessages} from '../src/traces.ts';
import * as traceLogic from '../src/traces.ts';

test('legacy filter names and exact values round-trip including whitespace and custom UTC bounds', () => {
  const search = '?range=custom&from=2026-01-01T00%3A00%3A00Z&to=2026-01-02T00%3A00%3A00Z&model=+gpt%2B4+&service=api&min_duration=0&session=s%26x&user=%E7%94%A8%E6%88%B7&errors=1';
  assert.deepEqual([...new URLSearchParams(filtersToSearch(filtersFromSearch(search)))].sort(), [...new URLSearchParams(search)].sort());
});

test('empty defaults, errors=1 checkbox semantics, and filters reset paging', () => {
  const filters = filtersFromSearch('?errors=true&cursor=ignored&limit=1');
  assert.equal(filters.errors, false);
  const query = new URLSearchParams(filtersToSearch(filters));
  assert.equal(query.get('range'), '');
  assert.equal(query.has('errors'), false);
  assert.equal(query.has('cursor'), false);
  assert.equal(query.has('limit'), false);
  assert.equal(new URLSearchParams(filtersToSearch({...filters, errors: true})).get('errors'), '1');
});

test('all six legacy Range labels and values', () => {
  assert.deepEqual(rangeOptions.map(option => [option.value, option.label]), [
    ['', 'All retained'], ['15m', '15 minutes'], ['1h', '1 hour'],
    ['24h', '24 hours'], ['7d', '7 days'], ['custom', 'Custom'],
  ]);
});

test('Unicode windows concatenate without computing offsets from UTF-16 lengths', () => {
  const first = {text: '😀a', total_chars: 6, next_offset: 2};
  const second = appendWindow(first, {text: '中z', total_chars: 6, next_offset: 4});
  assert.equal(first.text.length, 3);
  assert.equal(second.next_offset, 4);
  assert.deepEqual(appendWindow(second, {text: '🎈!', total_chars: 6, next_offset: null}), {text: '😀a中z🎈!', total_chars: 6, next_offset: null});
  assert.equal(appendWindow({...first, invalid_utf8: true}, second).invalid_utf8, true);
});

test('span tree orders siblings and includes orphans and cycles exactly once', () => {
  const span = (span_id: string, parent_span_id: string, start_offset_ms: number) => ({span_id, parent_span_id, start_offset_ms});
  const ordered = orderSpans([span('b', 'r', 2), span('r', '', 0), span('a', 'r', 1), span('o', 'missing', 3), span('x', 'y', 4), span('y', 'x', 5)]);
  assert.deepEqual(ordered.map(({span_id, depth}) => [span_id, depth]), [['r', 0], ['a', 1], ['b', 1], ['o', 0], ['x', 0], ['y', 1]]);
});

test('span ordering handles a 6,000-deep chain without overflowing the stack', () => {
  const spans = Array.from({length: 6000}, (_, i) => ({span_id: String(i), parent_span_id: i ? String(i - 1) : '', start_offset_ms: i}));
  const ordered = orderSpans(spans);
  assert.equal(ordered.length, 6000);
  assert.deepEqual(ordered.map(span => span.depth), spans.map((_, i) => i));
  assert.equal(ordered[5999].span_id, '5999');
});

test('span children index keeps normal trees and caps a 6,000-deep render tree', () => {
  const normal = orderSpans([
    {span_id: 'root', parent_span_id: '', start_offset_ms: 0},
    {span_id: 'a', parent_span_id: 'root', start_offset_ms: 1},
    {span_id: 'b', parent_span_id: 'root', start_offset_ms: 2},
    {span_id: 'orphan', parent_span_id: 'missing', start_offset_ms: 3},
  ]);
  const children = traceLogic.spanChildren(normal);
  assert.deepEqual(children.get(''), [normal[0], normal[3]]);
  assert.deepEqual(children.get('root'), [normal[1], normal[2]]);
  const deep = Array.from({length: 6000}, (_, depth) => ({span_id: String(depth), depth}));
  const capped = traceLogic.spanChildren(deep);
  assert.equal(capped.size, 9);
  assert.equal(capped.get('7')?.length, 5992);
  assert.equal([...capped.values()].reduce((sum, rows) => sum + rows.length, 0), 6000);
  assert.equal(capped.get('7')?.at(-1)?.span_id, '5999');
});

test('timeline labels retain legacy milliseconds and seconds formatting', () => {
  assert.deepEqual(traceLogic.timelineLabels(300), ['0 ms', '75ms', '150ms', '225ms', '300ms']);
  assert.deepEqual(traceLogic.timelineLabels(2000), ['0 ms', '500ms', '1.0s', '1.5s', '2.0s']);
  assert.deepEqual(traceLogic.timelineLabels(-1), ['0 ms', '0ms', '0ms', '0ms', '0ms']);
});

test('messages preserve text and fall back to raw data for unsupported payloads', () => {
  assert.deepEqual(toMessages('[{"role":"user","content":"hello"}]'), [{role: 'user', text: 'hello'}]);
  const messages = [{role: 'assistant', parts: [{content: 'one'}, {content: 'two'}]}];
  assert.deepEqual(toMessages(JSON.stringify({messages: JSON.stringify(messages)})), [{role: 'assistant', text: 'one\n\ntwo'}]);
  assert.deepEqual(toMessages(JSON.stringify({messages})), [{role: 'assistant', text: 'one\n\ntwo'}]);
  assert.equal(toMessages('[{"role":"user","content":[{"type":"image","url":"image"}]}]'), null);
  assert.equal(toMessages('{"unfinished":'), null);
  assert.equal(toMessages('plain text'), null);
});

test('custom range: picker value is the UTC wall clock, never shifted by the browser zone', async () => {
  const {fromPickerValue, toPickerValue} = await import('../src/traces.ts');
  for (const zone of ['UTC', 'Asia/Taipei', 'America/Los_Angeles']) {
    process.env.TZ = zone;
    assert.equal(fromPickerValue('2026-09-15T00:00' as never), '2026-09-15T00:00:00Z');
    assert.equal(fromPickerValue('2026-09-15T13:47' as never), '2026-09-15T13:47:00Z');
    assert.equal(toPickerValue('2026-09-15T13:47:00Z'), '2026-09-15T13:47');
  }
  assert.equal(fromPickerValue(undefined), '');
  assert.equal(toPickerValue(''), undefined);
  assert.equal(toPickerValue('not a time'), undefined);
});

test('custom range: URLs from before the picker still load, including offsets and non-zero minutes', async () => {
  const {fromPickerValue, toPickerValue} = await import('../src/traces.ts');
  assert.equal(toPickerValue('2026-09-15T23:59:59Z'), '2026-09-15T23:59');
  assert.equal(toPickerValue('2026-09-16T08:30:00+08:00'), '2026-09-16T00:30');
  const filters = filtersFromSearch('?range=custom&from=2026-01-01T00%3A07%3A00Z&to=2026-01-02T10%3A45%3A00Z');
  const from = toPickerValue(filters.from)!, to = toPickerValue(filters.to)!;
  assert.deepEqual([from, to], ['2026-01-01T00:07', '2026-01-02T10:45']);
  const search = filtersToSearch({...filters, from: fromPickerValue(from as never), to: fromPickerValue(to as never)});
  assert.equal(new URLSearchParams(search).get('from'), '2026-01-01T00:07:00Z');
  assert.equal(new URLSearchParams(search).get('to'), '2026-01-02T10:45:00Z');
});
