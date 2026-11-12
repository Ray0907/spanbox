import {useEffect, useId, useMemo, useRef} from 'react';
import * as stylex from '@stylexjs/stylex';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import {VStack, HStack} from '@astryxdesign/core/Layout';
import {Grid} from '@astryxdesign/core/Grid';
import {Card} from '@astryxdesign/core/Card';
import {Heading, Text} from '@astryxdesign/core/Text';
import {RangeSelector} from './RangeFilter';
import {Table, proportional} from '@astryxdesign/core/Table';
import {VisuallyHidden} from '@astryxdesign/core/VisuallyHidden';
import {dashboardCharts, dashboardRange, dashboardRanges, type DashboardData} from './dashboard-sql';
import {fmtCost, fmtMs, useJSON} from './telemetry';
import {Banner} from '@astryxdesign/core/Banner';
import {navigate} from './router';

const styles = stylex.create({
  page: {padding: 24, display: 'flex', flexDirection: 'column', gap: 24},
  chart: {width: '100%', minWidth: 0, height: 250},
  canvas: {display: 'block', width: '100%', height: 250},
  accent: {color: 'var(--color-accent)'}, neutral: {color: 'var(--color-chart-neutral)'},
});

const dashboardOptions = dashboardRanges.map(([value, label]) => ({value, label}));
type ChartData = ReturnType<typeof dashboardCharts>[number];
function Chart({chart, data}: {chart: ChartData; data: DashboardData}) {
  const host = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const accent = useRef<HTMLSpanElement>(null), neutral = useRef<HTMLSpanElement>(null);
  const valuesID = useId();
  useEffect(() => {
    const node = host.current!, visible = canvas.current!;
    let plot: uPlot | undefined;
    const draw = () => {
      const width = Math.floor(node.clientWidth);
      if (!width) return;
      plot?.destroy();
      // uPlot's layout uses inline styles. Keep its DOM detached and copy only
      // its canvas bitmap; the document uses canvas attributes and bundled CSS.
      const primary = getComputedStyle(accent.current!).color;
      const secondary = getComputedStyle(neutral.current!).color;
      const colors = [primary, secondary];
      plot = new uPlot({width, height: 250,
        tzDate: timestamp => uPlot.tzDate(new Date(timestamp * 1000), 'UTC'),
        scales: {x: {time: true, range: [data.RangeFrom, data.RangeTo]}},
        cursor: {show: false}, legend: {show: false}, select: {show: false, left: 0, top: 0, width: 0, height: 0},
        axes: [{stroke: secondary, grid: {stroke: secondary, width: 0.5}}, {stroke: secondary, size: 64, grid: {stroke: secondary, width: 0.5}}],
        series: [{label: 'UTC day'}, ...chart.labels.map((label, i) => ({label, stroke: colors[i], width: 2, points: {show: true, stroke: colors[i], fill: colors[i], size: 6}}))],
        hooks: {draw: [self => {
          const source = self.ctx.canvas;
          visible.width = source.width;
          visible.height = source.height;
          visible.getContext('2d')?.drawImage(source, 0, 0);
        }]},
      }, chart.values as uPlot.AlignedData);
    };
    draw();
    const resize = new ResizeObserver(draw);
    resize.observe(node);
    const scheme = matchMedia('(prefers-color-scheme: dark)');
    scheme.addEventListener('change', draw);
    const theme = new MutationObserver(draw);
    theme.observe(document.documentElement, {subtree: true, attributes: true, attributeFilter: ['class', 'style', 'data-astryx-theme']});
    return () => {resize.disconnect(); theme.disconnect(); scheme.removeEventListener('change', draw); plot?.destroy();};
  }, [chart, data.RangeFrom, data.RangeTo]);
  return <Card><VStack gap={3}>
    <Heading level={2}>{chart.title}</Heading>
    <Text type="supporting" color="secondary">{chart.labels.join(' / ')}{chart.id === 'latency' ? ' (ms)' : chart.id === 'errors' ? ' (%)' : ''} · UTC day</Text>
    <div ref={host} className={'dashboard-chart ' + stylex.props(styles.chart).className}>
      <canvas ref={canvas} aria-label={`${chart.title} chart`} aria-describedby={valuesID} role="img" {...stylex.props(styles.canvas)} />
      <span ref={accent} aria-hidden="true" {...stylex.props(styles.accent)} /><span ref={neutral} aria-hidden="true" {...stylex.props(styles.neutral)} />
    </div>
    <VisuallyHidden as="div" id={valuesID} data-chart-values={chart.id}>
      <Text>{chart.title} values by UTC day. {chart.labels.join(' / ')}</Text>
      <table><caption>{chart.title}</caption><thead><tr><th>UTC day</th>{chart.labels.map(label => <th key={label}>{label}</th>)}</tr></thead>
        <tbody>{(data.Days ?? []).map((day, i) => <tr key={day}><th>{day}</th>{chart.values.slice(1).map((values, j) => <td key={j}>{values[i] ?? '—'}</td>)}</tr>)}</tbody></table>
    </VisuallyHidden>
  </VStack></Card>;
}

export default function DashboardPage({search, refresh}: {search: string; refresh: number}) {
  const range = dashboardRange(search);
  const params = new URLSearchParams(search);
  params.set('range', range);
  const {data, loading, error} = useJSON<DashboardData>('/dashboard/data?' + params, refresh);
  const charts = useMemo(() => data ? dashboardCharts(data) : [], [data]);
  return <main id="main" {...stylex.props(styles.page)}>
    <VStack gap={2}><Heading level={1}>Dashboard</Heading><Text color="secondary">Trace cost, token volume, errors, and LLM latency by UTC day.</Text></VStack>
    <HStack><RangeSelector value={range} options={dashboardOptions} onChange={value => navigate('/dashboard?' + new URLSearchParams({range: value}))} /></HStack>
    {error && <Banner status="error" title={error.message} role="alert" />}{loading && <Text role="status">Loading…</Text>}
    {data && <>
      <section aria-label="Summary"><Grid columns={{minWidth: 140, max: 5}} gap={3}>
        {[['Total cost', fmtCost(data.TotalCost)], ['Input tokens', data.TotalInput ?? '—'], ['Output tokens', data.TotalOutput ?? '—'], ['Traces', data.TraceCount], ['Error rate', `${(data.ErrorRate * 100).toFixed(1)}%`]].map(([label, value]) =>
          <Card key={label}><VStack gap={2}><Text color="secondary">{label}</Text><Text weight="semibold">{value}</Text></VStack></Card>)}
      </Grid></section>
      {!data.TraceCount && <Text>No traces in this range.</Text>}
      <Grid columns={{minWidth: 300, max: 2}} gap={4}>{charts.map(chart => <Chart key={chart.id} chart={chart} data={data} />)}</Grid>
      <Heading level={2}>Models</Heading>
      <Table data={(data.Models ?? []).map(row => ({...row}))} density="compact" columns={[
        {key: 'Model', header: 'Model', width: proportional(1.5)}, {key: 'Calls', header: 'Calls', width: proportional(1)},
        {key: 'Input', header: 'Input tokens', width: proportional(1), renderCell: row => row.Input ?? '—'},
        {key: 'Output', header: 'Output tokens', width: proportional(1), renderCell: row => row.Output ?? '—'},
        {key: 'Cost', header: 'Cost', width: proportional(1), renderCell: row => fmtCost(row.Cost)},
        {key: 'AvgMs', header: 'Avg duration', width: proportional(1), renderCell: row => fmtMs(row.AvgMs)},
      ]} />
      {!data.Models?.length && <Text>No model calls in this range.</Text>}
    </>}
  </main>;
}
