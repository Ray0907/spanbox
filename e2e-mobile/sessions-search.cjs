const assert = require('node:assert/strict');
const seed = require('./sessions-search-seed.cjs');
const {rowMatches, emptyState} = require('./search-time.cjs');
const {pick, shown, chooseRange} = require('./picker.cjs');
const day = offset => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);

module.exports = function ({base, token, check, inspect, rowTap}) {
  return async function sessionsSearch(page) {
    for (const path of ['/sessions', '/sessions?id=mobile-session-1', '/search', '/search?q=weather']) {
      const response = await page.request.get(base + path);
      const shell = response.status() === 200 && (await response.text()).includes('id="root"');
      check(`sessions/search: ${path} deep link serves shell`, shell);
      assert.ok(shell, `${path} production shell required`);
    }
    const session = await seed({base, token});
    const json = async path => { const response = await page.request.get(base + path); assert.equal(response.status(), 200); return response.json(); };
    const current = async label => check(`${label}: current nav and landmarks`,
      await page.locator('nav[aria-label="Primary"] a[aria-current="page"]').innerText() === label && await page.locator('main').count() === 1 && await page.getByRole('heading', {level: 1}).count() === 1);
    const settledList = async () => { await page.locator('tbody tr').first().waitFor(); };
    const chrome = async () => check('sessions/search: title and skip link retained', (await page.title()).endsWith(' · spanbox') && await page.getByRole('link', {name: 'Skip to main content', exact: true}).count() === 1 && await page.locator('main#main').count() === 1);
    await page.goto(`${base}/sessions`);
    await settledList();
    await current('Sessions');
    await inspect(page, 'sessions');
    await rowTap(page, 'sessions');
    const expected = await json('/sessions?format=json&limit=50');
    const headers = await page.locator('thead th').allTextContents();
    check('sessions: all nine old columns', headers.join('|') === 'Last seen|Session|Service|Models|Traces|LLM calls|Tokens in / out|Cost|First seen');
    check('sessions: old page size is 50', await page.locator('tbody tr').count() === 50);
    const first = expected.items[0];
    const cells = await page.locator('tbody tr').first().locator('td').allTextContents();
    check('sessions: every list field matches JSON', cells.length === 9 && cells[0] === new Date(first.last_ns / 1e6).toISOString() && cells[1] === first.session_id && cells[2] === first.service && cells[3] === first.models && cells[4] === String(first.trace_count) && cells[5] === String(first.llm_count) && cells[6] === `${first.input_tokens} / ${first.output_tokens}` && cells[7] === `$${first.cost_usd.toFixed(6)}` && cells[8] === new Date(first.first_ns / 1e6).toISOString());
    const next = page.getByRole('link', {name: 'Next page', exact: true});
    const nextURL = new URL(await next.getAttribute('href'), base);
    check('sessions: old cursor Next page URL', nextURL.pathname === '/sessions' && nextURL.searchParams.get('cursor') === expected.next_cursor);
    await next.tap();
    await settledList();
    check('sessions: paging replaces rows without duplicates', !(await page.locator('tbody').innerText()).includes(first.session_id));
    const special = page.getByRole('link', {name: session, exact: true});
    check('sessions: encoded detail link and descriptive trace count', new URL(await special.getAttribute('href'), base).searchParams.get('id') === session && new URL(await page.getByRole('link', {name: `4 traces in ${session}`, exact: true}).getAttribute('href'), base).searchParams.get('session') === session);
    check('sessions: last page hides Next page', await page.getByRole('link', {name: 'Next page', exact: true}).count() === 0);
    await page.goBack();
    await page.getByRole('link', {name: first.session_id, exact: true}).waitFor();
    check('sessions: Back restores first page', (await page.locator('tbody').innerText()).includes(first.session_id));
    for (const [label, value] of [['All retained', ''], ['15 minutes', '15m'], ['1 hour', '1h'], ['24 hours', '24h'], ['7 days', '7d'], ['Custom', 'custom']]) {
      await chooseRange(page, label);
      check(`sessions: custom fields visibility ${label}`, await page.getByLabel('From (UTC)', {exact: true}).isVisible() === (value === 'custom'));
      if (value === 'custom') {
        await pick(page, 'From', day(-5), '00:00');
        await pick(page, 'To', day(-4), '00:00');
      }
      await page.getByRole('button', {name: 'Apply', exact: true}).tap();
      await page.getByRole('status').filter({hasText: /shown/}).waitFor();
      check(`sessions: Range ${label} mapping resets cursor`, new URL(page.url()).searchParams.get('range') === value && !new URL(page.url()).searchParams.has('cursor'));
    }
    check('sessions: empty state and Clear filters', await page.getByText('No sessions match these filters.', {exact: false}).isVisible());
    await page.reload();
    await page.getByRole('link', {name: 'Clear filters', exact: true}).waitFor();
    const bounds = new URL(page.url()).searchParams;
    check('sessions: custom bounds are RFC 3339 UTC, survive reload and load back into the pickers', bounds.get('from') === `${day(-5)}T00:00:00Z` && bounds.get('to') === `${day(-4)}T00:00:00Z` && JSON.stringify(await shown(page, 'From')) === JSON.stringify({iso: day(-5), time: '00:00'}) && JSON.stringify(await shown(page, 'To')) === JSON.stringify({iso: day(-4), time: '00:00'}));
    await page.getByRole('link', {name: 'Clear filters', exact: true}).tap();
    await settledList();
    check('sessions: Clear filters clears URL', new URL(page.url()).search === '');
    // Expected HTTP 400/404 console messages are checked separately from the clean happy-path page.
    const negative = await page.context().newPage();
    const negativeErrors = [];
    negative.on('console', message => { if (message.type() === 'error') negativeErrors.push(message.text()); });
    negative.on('pageerror', error => negativeErrors.push(error.message));
    await negative.goto(`${base}/sessions?range=custom&from=bad`);
    // The From and To pickers each carry an empty alert region, so pick the error line by its text.
    const errorLine = negative.locator('main [role="alert"]', {hasText: 'Unable to load data'});
    await errorLine.waitFor();
    check('sessions: invalid filters show error', (await errorLine.innerText()).length > 0);

    const detailURL = '/sessions?id=' + encodeURIComponent(session);
    const detail = await json(detailURL + '&format=json');
    await page.goto(base + detailURL);
    await page.locator('.turn-card').nth(3).waitFor();
    await current('Sessions');
    await inspect(page, 'session-detail');
    await require('./session-turn-layout.cjs')(page, check);
    await chrome();
    check('session: visible links have 44px tap targets', await page.locator('main a').evaluateAll(links => links.every(link => link.getBoundingClientRect().height >= 44)));
    const labels = await page.locator('.turn-card h2').allTextContents();
    check('session: orphan ordered first; only roots numbered', labels.join('|') === 'orphan tool|Turn 1|Turn 2|Turn 3' && await page.locator('.turn-card').count() === detail.traces.length);
    for (let i = 0; i < detail.traces.length; i++) {
      const turn = detail.traces[i], card = page.locator('.turn-card').nth(i), text = await card.innerText();
      check(`session: turn ${i} prompt/output/fields/link preserved`, text.includes(turn.prompt) && text.includes(turn.output) && text.includes(`${turn.input_tokens} / ${turn.output_tokens} tokens`) && text.includes(`${turn.span_count} span`) && text.includes('1ms') && text.includes(new Date(turn.start_ns / 1e6).toISOString()) && await card.getByRole('link', {name: `Open trace for ${labels[i]}`, exact: true}).getAttribute('href') === `/traces/${turn.trace_id}`);
    }
    const totals = await page.locator('.session-totals').innerText();
    check('session: totals plain text not chips', totals.includes(`3 turns · 4 traces · ${detail.totals.input_tokens} / ${detail.totals.output_tokens} tokens`) && totals.includes(`$${detail.totals.cost_usd.toFixed(6)}`) && await page.locator('.session-totals button, .trace-summary').count() === 0);
    check('session: error badge and escaped prompt; no script injected', await page.locator('.turn-card').nth(2).getByText('error', {exact: true}).isVisible() && await page.locator('main script').count() === 0);
    const traceList = page.getByRole('link', {name: 'Trace list', exact: true});
    check('session: trace filter URL encodes session', new URL(await traceList.getAttribute('href'), base).searchParams.get('session') === session);
    await traceList.tap();
    await page.getByText('4 shown', {exact: true}).waitFor();
    check('session: Trace list routes in-app and retains filter', await page.getByLabel('Session', {exact: true}).inputValue() === session);
    await page.goBack();
    await page.locator('.turn-card').nth(3).waitFor();
    await page.getByRole('link', {name: 'Open trace for Turn 1', exact: true}).tap();
    await page.getByRole('heading', {name: 'Span tree', exact: true}).waitFor();
    check('session: Open trace navigates successfully', new URL(page.url()).pathname === `/traces/${detail.traces[1].trace_id}`);
    await page.goto(`${base}/sessions?id=parity-singular`);
    await page.locator('.turn-card').waitFor();
    const singular = await page.locator('main').innerText();
    check('session: clipped prompt and singular labels', singular.includes('x'.repeat(599) + 'y…') && !singular.includes('yz') && singular.includes('1 turn · 1 trace ·') && singular.includes('1 span') && await page.getByRole('link', {name: 'Open trace for Turn 1', exact: true}).count() === 1);
    check('session: unknown cost omitted', !singular.includes('$') && singular.includes('— / — tokens') && await page.locator('.turn-card').getByText('—', {exact: true}).count() === 0);
    await page.goto(`${base}/sessions?id=parity-truncated`);
    await page.locator('.turn-card').nth(199).waitFor();
    check('session: 200-trace limit and Banner notice', await page.locator('.turn-card').count() === 200 && await page.getByRole('status').getByText('Showing the first 200 traces of this session.', {exact: true}).isVisible() && await page.getByRole('heading', {name: 'Turn 200', exact: true}).count() === 1 && await page.getByRole('heading', {name: 'Turn 201', exact: true}).count() === 0);
    await require('./sessions-search-palette.cjs')(page, check);
    await negative.goto(`${base}/sessions?id=missing`);
    await negative.getByRole('heading', {name: 'Session not found', exact: true}).waitFor();
    check('session: unknown JSON 404 renders not-found state', await negative.getByRole('link', {name: 'Sessions', exact: true}).count() > 0);
    check('sessions: negative cases log only expected HTTP errors, no CSP/runtime errors', negativeErrors.length === 2 && negativeErrors.every(text => /^Failed to load resource:.*(?:400|404)/.test(text)));
    await negative.close();

    await page.goto(`${base}/search`);
    await page.getByLabel('Phrase', {exact: true}).waitFor();
    await current('Search');
    await chrome();
    check('search: no-query state has no result section and autofocus', await page.locator('.search-results').count() === 0 && await page.getByLabel('Phrase', {exact: true}).evaluate(el => el === document.activeElement));
    await inspect(page, 'search');
    await page.getByLabel('Phrase', {exact: true}).fill('needle-literal a&b 中');
    await page.getByLabel('Phrase', {exact: true}).press('Enter');
    await page.locator('.search-results article').nth(99).waitFor();
    const query = new URL(page.url()).searchParams;
    check('search: keyboard submit uses old q param', query.get('q') === 'needle-literal a&b 中' && !query.has('query'));
    const a = await json('/search?format=json&limit=50&q=' + encodeURIComponent(query.get('q')));
    const b = await json('/search?format=json&limit=50&q=' + encodeURIComponent(query.get('q')) + '&cursor=' + encodeURIComponent(a.next_cursor));
    const hits = [...a.items, ...b.items];
    check('search: old 100-hit limit, recent order, no visible paging', await page.locator('.search-results article').count() === 100 && await page.getByRole('button', {name: /Load more|Next page/}).count() === 0);
    check('search: 100 exact snippets and all result fields/links', (await Promise.all(hits.map(async (hit, i) => {
      const row = page.locator('.search-results article').nth(i), text = await row.innerText();
      return await rowMatches(row, hit) && text.includes(hit.kind) && text.includes(hit.trace_name) && text.includes(hit.span_name) && text.includes(hit.model) && await row.locator('a[href^="/traces/"]').getAttribute('href') === `/traces/${hit.trace_id}` && await row.locator('a[href^="/spans/"]').getAttribute('href') === `/spans/${hit.trace_id}/${hit.span_id}`;
    }))).every(Boolean));
    check('search: result links have 44px tap targets', await page.locator('.search-results a').evaluateAll(links => links.every(link => link.getBoundingClientRect().height >= 44)));
    await page.reload();
    await page.locator('.search-results article').nth(99).waitFor();
    check('search: deep reload restores input and results', await page.getByLabel('Phrase', {exact: true}).inputValue() === query.get('q'));
    await page.getByLabel('Phrase', {exact: true}).fill('no-matching-literal');
    await page.getByRole('button', {name: 'Search', exact: true}).tap();
    await emptyState(page);
    check('search: empty state', await page.locator('.search-results article').count() === 0);
    await page.goBack();
    await page.locator('.search-results article').nth(99).waitFor();
    check('search: Back restores query and full results', await page.getByLabel('Phrase', {exact: true}).inputValue() === query.get('q'));
    await page.locator('.search-results article').first().locator('a[href^="/spans/"]').tap();
    await page.getByRole('heading', {name: 'Span tree', exact: true}).waitFor();
    check('search: span result deep link works', new URL(page.url()).pathname === `/spans/${hits[0].trace_id}/${hits[0].span_id}`);
    await page.evaluate(() => { window.__sessionsSearchDocument = true; });
    const nav = page.locator('nav[aria-label="Primary"]').getByRole('link', {name: 'Sessions', exact: true});
    await nav.focus();
    await nav.press('Enter');
    await settledList();
    check('sessions/search: keyboard navigation stays in-app and focus moves to main', await page.evaluate(() => window.__sessionsSearchDocument && document.activeElement === document.querySelector('main')));
    const searchNav = page.locator('nav[aria-label="Primary"]').getByRole('link', {name: 'Search', exact: true});
    await searchNav.focus();
    await searchNav.press('Enter');
    await page.getByLabel('Phrase', {exact: true}).waitFor();
    check('sessions/search: Search routes in-app with main focus', await page.evaluate(() => window.__sessionsSearchDocument && document.activeElement === document.querySelector('main')));
    await inspect(page, 'search');
  };
};
