const {pick, shown, chooseCustom, chooseRange, rangeTrigger} = require('./picker.cjs');
const day = offset => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);

module.exports = function ({base, check}) {
  return async function traceFilters(page) {
    check('traces: Traces nav current', await page.locator('nav[aria-label="Primary"] a[aria-current="page"]').innerText() === 'Traces');
    check('traces: light and dark color-scheme', await page.evaluate(() => ['light', 'dark'].every(scheme => getComputedStyle(document.documentElement).colorScheme.includes(scheme))));
    check('traces: visible buttons have 44px tap targets', await page.locator('main button').evaluateAll(buttons => buttons.map(button => button.getBoundingClientRect()).filter(rect => rect.width && rect.height).every(rect => rect.width >= 44 && rect.height >= 44)));
    for (const [label, value] of [['All retained', ''], ['15 minutes', '15m'], ['1 hour', '1h'], ['24 hours', '24h'], ['7 days', '7d'], ['Custom', 'custom']]) {
      if (value === 'custom') await chooseCustom(page); else await chooseRange(page, label);
      check(`traces: custom bounds visibility for ${label}`, await page.getByLabel('From (UTC)', {exact: true}).isVisible() === (value === 'custom') && await page.getByLabel('To (UTC)', {exact: true}).isVisible() === (value === 'custom'));
      if (value === 'custom') {
        await pick(page, 'From', day(-1), '00:00');
        await pick(page, 'To', day(1), '23:59');
      }
      await page.getByRole('button', {name: 'Apply', exact: true}).tap();
      await page.getByText('2 shown', {exact: true}).waitFor();
      check(`traces: Range ${label} uses legacy value`, new URL(page.url()).searchParams.get('range') === value);
    }
    for (const [label, value] of [['Model', 'gpt-4o'], ['Service', 'demo'], ['Min duration (ms)', '100'], ['Session', 'mobile-session-1'], ['User', 'mobile-user-1']]) {
      await page.getByLabel(label, {exact: true}).fill(value);
    }
    await page.getByRole('button', {name: 'Apply', exact: true}).tap();
    await page.getByText('1 shown', {exact: true}).waitFor();
    const expected = {model: 'gpt-4o', service: 'demo', min_duration: '100', session: 'mobile-session-1', user: 'mobile-user-1', range: 'custom'};
    const url = page.url();
    check('traces: every filter query name/value matches old form', Object.entries(expected).every(([key, value]) => new URL(url).searchParams.get(key) === value));
    await page.reload();
    await page.getByText('1 shown', {exact: true}).waitFor();
    check('traces: filters round-trip after reload', await page.getByLabel('Model', {exact: true}).inputValue() === 'gpt-4o' && await page.getByLabel('Service', {exact: true}).inputValue() === 'demo' && await page.getByLabel('Session', {exact: true}).inputValue() === 'mobile-session-1' && await page.getByLabel('User', {exact: true}).inputValue() === 'mobile-user-1' && await page.getByLabel('Min duration (ms)', {exact: true}).inputValue() === '100' && (await rangeTrigger(page).innerText()).includes('Custom'));
    const bounds = new URL(url).searchParams;
    check('traces: custom time bounds are RFC 3339 UTC and round-trip into the pickers', bounds.get('from') === `${day(-1)}T00:00:00Z` && bounds.get('to') === `${day(1)}T23:59:00Z` && JSON.stringify(await shown(page, 'From')) === JSON.stringify({iso: day(-1), time: '00:00'}) && JSON.stringify(await shown(page, 'To')) === JSON.stringify({iso: day(1), time: '23:59'}));
    const exportURL = await page.getByRole('link', {name: 'Export NDJSON'}).getAttribute('href');
    check('traces: export preserves all active filters', [...new URL(url).searchParams].every(([key, value]) => new URL(exportURL, base).searchParams.get(key) === value));
    const exported = await page.request.get(new URL(exportURL, base).href);
    const body = (await exported.text()).trim();
    const lines = body ? body.split('\n').map(line => JSON.parse(line)) : [];
    check('traces: export retains span-level NDJSON semantics', exported.ok() && exported.headers()['content-type'].startsWith('application/x-ndjson') && lines.length === 1 && lines[0].SessionID === 'mobile-session-1' && lines[0].SpanID === '2122232425262728');
    await page.getByLabel('Model', {exact: true}).fill('missing-model');
    await page.getByRole('button', {name: 'Apply', exact: true}).tap();
    await page.getByText('0 shown', {exact: true}).waitFor();
    await page.goBack();
    await page.getByText('1 shown', {exact: true}).waitFor();
    check('traces: browser Back restores query and controls', page.url() === url && await page.getByLabel('Model', {exact: true}).inputValue() === 'gpt-4o');
    await page.goto(`${base}/traces?user=mobile-user-2`);
    await page.getByText('1 shown', {exact: true}).waitFor();
    await page.getByLabel('Errors only', {exact: true}).check();
    await page.getByRole('button', {name: 'Apply', exact: true}).tap();
    await page.getByText('1 shown', {exact: true}).waitFor();
    check('traces: Errors only and User filter real data with errors=1', new URL(page.url()).searchParams.get('errors') === '1' && await page.getByLabel('Errors only', {exact: true}).isChecked() && await page.locator('tbody tr').count() === 1 && (await page.locator('tbody').innerText()).includes('ERROR'));
    const errorExport = await page.getByRole('link', {name: 'Export NDJSON'}).getAttribute('href');
    check('traces: export retains active Errors only and User filters', new URL(errorExport, base).searchParams.get('errors') === '1' && new URL(errorExport, base).searchParams.get('user') === 'mobile-user-2');
    await page.goto(`${base}/traces?limit=1`);
    await page.getByText('1 shown', {exact: true}).waitFor();
    const more = page.waitForResponse(response => response.url().includes('cursor=') && response.url().includes('format=json'));
    await page.getByRole('button', {name: 'Load more', exact: true}).tap();
    const moreResponse = await more;
    await page.getByText('2 shown', {exact: true}).waitFor();
    check('traces: cursor Load more appends without duplicates', new URL(moreResponse.url()).searchParams.has('cursor') && await page.locator('tbody tr').count() === 2 && new Set(await page.locator('tbody a').evaluateAll(links => links.map(link => link.href))).size === 2);
    check('traces: final cursor hides Load more', await page.getByRole('button', {name: 'Load more', exact: true}).count() === 0);
    const pagedExport = await page.getByRole('link', {name: 'Export NDJSON'}).getAttribute('href');
    check('traces: loaded cursor is not exported', !new URL(pagedExport, base).searchParams.has('cursor'));
    await page.goto(`${base}/traces?service=missing`);
    await page.getByText('0 shown', {exact: true}).waitFor();
    check('traces: new filter resets loaded pages to empty', await page.locator('tbody a[href^="/traces/"]').count() === 0);
    await page.getByRole('link', {name: 'Clear filters', exact: true}).tap();
    await page.getByText('2 shown', {exact: true}).waitFor();
    check('traces: Clear filters restores the unfiltered URL and controls', new URL(page.url()).pathname === '/traces' && new URL(page.url()).search === '' && await page.getByLabel('Service', {exact: true}).inputValue() === '' && !await page.getByLabel('Errors only', {exact: true}).isChecked());
  };
};
