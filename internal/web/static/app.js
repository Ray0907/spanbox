(() => {
  const root = document.querySelector('[data-dashboard-endpoint]');
  if (!root || typeof uPlot === 'undefined') return;

  const colors = {
    blue: '#4f83ef',
    violet: '#8b6be8',
    teal: '#35a6ad',
    amber: '#d69a22',
    red: '#d55b52'
  };

  const options = (title, series, width) => ({
    title,
    width: Math.max(width, 280),
    height: 250,
    tzDate: ts => uPlot.tzDate(new Date(ts * 1000), 'UTC'),
    scales: { x: { time: true } },
    axes: [{ stroke: '#78869a', grid: { stroke: '#d5dde733' } }, { stroke: '#78869a', size: 64, grid: { stroke: '#d5dde733' } }],
    series: [{ label: 'UTC day' }, ...series]
  });

  const clean = values => values.map(value => value == null ? null : value);
  const draw = data => {
    const x = data.Days.map(day => Date.parse(`${day}T00:00:00Z`) / 1000);
    const charts = [
      ['cost', 'Daily cost (USD)', [x, clean(data.DailyCost)], [{ label: 'Cost', stroke: colors.blue, width: 2 }]],
      ['tokens', 'Daily tokens', [x, clean(data.DailyInput), clean(data.DailyOutput)], [{ label: 'Input', stroke: colors.blue, width: 2 }, { label: 'Output', stroke: colors.violet, width: 2 }]],
      ['latency', 'LLM latency (ms)', [x, clean(data.DailyP50), clean(data.DailyP95)], [{ label: 'p50', stroke: colors.teal, width: 2 }, { label: 'p95', stroke: colors.amber, width: 2 }]],
      ['errors', 'Trace error rate (%)', [x, data.DailyErrorRate.map(value => value * 100)], [{ label: 'Errors', stroke: colors.red, width: 2 }]]
    ];
    for (const [name, title, values, series] of charts) {
      const element = root.querySelector(`[data-chart="${name}"]`);
      if (element) new uPlot(options(title, series, element.clientWidth), values, element);
    }
  };

  fetch(root.dataset.dashboardEndpoint, { headers: { Accept: 'application/json' } })
    .then(response => {
      if (!response.ok) throw new Error(`Dashboard data failed (${response.status})`);
      return response.json();
    })
    .then(draw)
    .catch(error => {
      root.insertAdjacentText('afterbegin', error.message);
    });
})();
