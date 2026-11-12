import test from 'node:test';
import assert from 'node:assert/strict';
import {dashboardCharts, dashboardRanges, dashboardRange, sqlTable, sqlBodyLimit} from '../src/dashboard-sql.ts';

test('dashboard ranges retain every legacy option and default', () => {
  assert.deepEqual(dashboardRanges, [['15m', '15 minutes'], ['1h', '1 hour'], ['24h', '24 hours'], ['7d', '7 days']]);
  assert.equal(dashboardRange(''), '24h');
  assert.equal(dashboardRange('?range=7d'), '7d');
  assert.equal(dashboardRange('?range=bad'), 'bad'); // server owns validation
});
test('dashboard maps UTC days, nullable series, percentage, and both two-series panels', () => {
  const charts = dashboardCharts({Days: ['2026-01-02'], DailyCost: [null], DailyInput: [12], DailyOutput: [3], DailyP50: [100], DailyP95: [500], DailyErrorRate: [0.25]});
  assert.deepEqual(charts.map(c => [c.id, c.labels]), [['cost', ['Cost']], ['tokens', ['Input', 'Output']], ['latency', ['p50', 'p95']], ['errors', ['Errors']]]);
  assert.equal(charts[0].values[0][0], Date.UTC(2026, 0, 2) / 1000);
  assert.equal(charts[0].values[1][0], null);
  assert.deepEqual(charts[1].values.slice(1), [[12], [3]]);
  assert.deepEqual(charts[2].values.slice(1), [[100], [500]]);
  assert.deepEqual(charts[3].values[1], [25]);
  assert.ok(dashboardCharts({Days: null}).every(c => c.values.every(v => v.length === 0)));
});
test('SQL table preserves duplicate headers, string cells, empty results, truncation and errors', () => {
  assert.deepEqual(sqlTable({columns: ['x', 'x'], rows: [['<script>', 'NULL']], truncated: true, error: ''}), {columns: ['x', 'x'], rows: [['<script>', 'NULL']], truncated: true, error: ''});
  assert.deepEqual(sqlTable({columns: [], rows: [], truncated: false, error: 'query rejected'}), {columns: [], rows: [], truncated: false, error: 'query rejected'});
  assert.throws(() => sqlTable({}), /malformed SQL response/);
  assert.equal(sqlBodyLimit, 20 * 1024);
});
