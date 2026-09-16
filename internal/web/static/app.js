(() => {
  const markSelectedSpan = summary => {
    document.querySelectorAll('.tree-node > summary[aria-current="true"]').forEach(row => {
      row.classList.remove('is-selected');
      row.removeAttribute('aria-current');
    });
    summary.classList.add('is-selected');
    summary.setAttribute('aria-current', 'true');
  };

  const firstSpan = document.querySelector('.tree-node > summary');
  if (firstSpan && document.querySelector('#panel .span-detail')) markSelectedSpan(firstSpan);

  document.addEventListener('htmx:beforeRequest', event => {
    const summary = event.detail.elt.closest('.tree-node > summary');
    if (summary) markSelectedSpan(summary);
  });

  const root = document.querySelector('[data-dashboard-endpoint]');
  if (!root || typeof uPlot === 'undefined') return;

  const styles = getComputedStyle(document.documentElement);
  const color = name => styles.getPropertyValue(name).trim();
  const colors = {
    primary: color('--accent'),
    secondary: color('--chart-secondary'),
    axis: color('--muted'),
    grid: color('--line')
  };

  const options = (series, width) => ({
    width: Math.max(width, 280),
    height: 250,
    tzDate: timestamp => uPlot.tzDate(new Date(timestamp * 1000), 'UTC'),
    scales: { x: { time: true } },
    axes: [
      { stroke: colors.axis, grid: { stroke: colors.grid, width: 1 } },
      { stroke: colors.axis, size: 64, grid: { stroke: colors.grid, width: 1 } }
    ],
    series: [{ label: 'UTC day' }, ...series]
  });

  const clean = values => values.map(value => value == null ? null : value);
  const draw = data => {
    const x = data.Days.map(day => Date.parse(`${day}T00:00:00Z`) / 1000);
    const primary = { stroke: colors.primary, width: 2, points: { stroke: colors.primary, fill: color('--surface'), size: 5 } };
    const secondary = { stroke: colors.secondary, width: 2, points: { stroke: colors.secondary, fill: color('--surface'), size: 5 } };
    const charts = [
      ['cost', [x, clean(data.DailyCost)], [{ label: 'Cost', ...primary }]],
      ['tokens', [x, clean(data.DailyInput), clean(data.DailyOutput)], [{ label: 'Input', ...primary }, { label: 'Output', ...secondary }]],
      ['latency', [x, clean(data.DailyP50), clean(data.DailyP95)], [{ label: 'p50', ...primary }, { label: 'p95', ...secondary }]],
      ['errors', [x, data.DailyErrorRate.map(value => value * 100)], [{ label: 'Errors', ...primary }]]
    ];

    for (const [name, values, series] of charts) {
      const element = root.querySelector(`[data-chart="${name}"]`);
      if (!element) continue;
      const plot = new uPlot(options(series, element.clientWidth), values, element);
      new ResizeObserver(entries => {
        const width = Math.floor(entries[0].contentRect.width);
        if (width > 0 && width !== plot.width) plot.setSize({ width: Math.max(width, 280), height: 250 });
      }).observe(element);
    }
  };

  fetch(root.dataset.dashboardEndpoint, { headers: { Accept: 'application/json' } })
    .then(response => {
      if (!response.ok) throw new Error(`Dashboard data failed (${response.status})`);
      return response.json();
    })
    .then(draw)
    .catch(error => {
      const notice = document.createElement('p');
      notice.className = 'notice error';
      notice.setAttribute('role', 'alert');
      notice.textContent = error.message;
      root.prepend(notice);
    });
})();
