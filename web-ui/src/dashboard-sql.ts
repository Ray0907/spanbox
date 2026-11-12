export const dashboardRanges = [['15m', '15 minutes'], ['1h', '1 hour'], ['24h', '24 hours'], ['7d', '7 days']] as const;
export const dashboardRange = (search: string) => new URLSearchParams(search).get('range') || '24h';

type Values = (number | null)[];
export type DashboardSeries = {Days?: string[] | null; DailyCost?: Values | null; DailyInput?: Values | null; DailyOutput?: Values | null; DailyP50?: Values | null; DailyP95?: Values | null; DailyErrorRate?: number[] | null};
export type DashboardData = DashboardSeries & {
  RangeFrom: number; RangeTo: number; TotalCost: number | null; TotalInput: number | null; TotalOutput: number | null; TraceCount: number; ErrorRate: number;
  Models: {Model: string; Calls: number; Input: number | null; Output: number | null; Cost: number | null; AvgMs: number}[] | null;
};
export function dashboardCharts(data: DashboardSeries) {
  const x = (data.Days ?? []).map(day => Date.parse(`${day}T00:00:00Z`) / 1000);
  return [
    {id: 'cost', title: 'Daily cost', labels: ['Cost'], values: [x, data.DailyCost ?? []]},
    {id: 'tokens', title: 'Daily tokens', labels: ['Input', 'Output'], values: [x, data.DailyInput ?? [], data.DailyOutput ?? []]},
    {id: 'latency', title: 'LLM latency', labels: ['p50', 'p95'], values: [x, data.DailyP50 ?? [], data.DailyP95 ?? []]},
    {id: 'errors', title: 'Error rate', labels: ['Errors'], values: [x, (data.DailyErrorRate ?? []).map(n => n * 100)]},
  ];
}
export type SQLResult = {columns: string[]; rows: string[][]; truncated: boolean; error: string};
export const sqlBodyLimit = 20 * 1024;
// The server always sends all four fields; anything else is a contract violation, not an empty result.
export function sqlTable(result: Partial<SQLResult>): SQLResult {
  const {columns, rows, truncated, error} = result;
  if (!Array.isArray(columns) || !Array.isArray(rows) || typeof truncated !== 'boolean' || typeof error !== 'string') throw new Error('malformed SQL response');
  return {columns, rows, truncated, error};
}
