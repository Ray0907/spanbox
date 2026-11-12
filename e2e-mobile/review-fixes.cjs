const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

module.exports = function ({root, base, token, check}) {
  return async function reviewFixes(page) {
    await page.goto(`${base}/dashboard?range=15m`);
    const range = page.getByRole('combobox', {name: /^Range/});
    await range.focus();
    for (const [value, label] of [['1h', '1 hour'], ['24h', '24 hours']]) {
      await page.keyboard.press('Enter');
      check(`review range: Enter opens the menu before ${label}`, await range.getAttribute('aria-expanded') === 'true');
      await page.keyboard.press('ArrowDown');
      await page.keyboard.press('Enter');
      await page.waitForTimeout(200);
      check(`review range: ArrowDown and Enter choose ${label} and keep focus on the Range control`,
        new URL(page.url()).searchParams.get('range') === value &&
        await range.evaluate((node, text) => node === document.activeElement && node.textContent.includes(text), label));
    }
    await page.keyboard.press('Space');
    await page.keyboard.press('Escape');
    check('review range: Space opens, Escape dismisses without changing the range and keeps focus',
      await range.getAttribute('aria-expanded') === 'false' && new URL(page.url()).searchParams.get('range') === '24h' &&
      await range.evaluate(node => node === document.activeElement));
    await page.getByRole('link', {name: 'Sessions', exact: true}).click();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    check('review range: navigation to another page still focuses main', true);

    // Use real API shapes, replacing only the span tree with a worst-case chain.
    const sourceID = '01'.repeat(16);
    const source = await (await page.request.get(`${base}/traces/${sourceID}?format=json`)).json();
    const sourceSpan = source.spans[0];
    const detail = await (await page.request.get(`${base}/spans/${sourceID}/${sourceSpan.span_id}?format=json`)).json();
    const traceID = 'de'.repeat(16);
    const spans = Array.from({length: 6000}, (_, i) => ({...sourceSpan, span_id: i.toString(16).padStart(16, '0'), parent_span_id: i ? (i - 1).toString(16).padStart(16, '0') : '', name: `Deep span ${i}`, start_offset_ms: i}));
    await page.route(`**/traces/${traceID}?format=json`, route => route.fulfill({json: {...source, trace_id: traceID, name: 'Deep trace', span_count: spans.length, spans}}));
    await page.route(`**/spans/${traceID}/**`, route => route.fulfill({json: {...detail, ...spans[0]}}));
    await page.goto(`${base}/traces/${traceID}`);
    const rendered = await page.locator('.span-tree button[data-span-id]').first().waitFor({state: 'attached', timeout: 15000}).then(() => true, () => false);
    check('review depth: 6,000-span trace renders without crashing', rendered && await page.locator('.span-tree button[data-span-id]').count() === 6000);
    if (rendered) {
      const last = page.locator('.span-tree button[data-span-id]').last();
      const nesting = await last.evaluate(node => { let depth = 0; for (; node; node = node.parentElement) depth++; return depth; });
      check('review depth: DOM nesting is bounded for the deepest span', nesting < 100, String(nesting));
      for (let level = 0; level < 8; level++) {
        const closed = page.locator('.span-tree button[aria-expanded="false"]');
        if (!await closed.count()) break;
        await closed.first().click();
      }
      await last.click();
      await page.waitForURL(`**/spans/${traceID}/${spans.at(-1).span_id}`);
      check('review depth: deepest span remains selectable', await last.getAttribute('aria-current') === 'true');
    }
    await page.unroute(`**/traces/${traceID}?format=json`);
    await page.unroute(`**/spans/${traceID}/**`);

    const fixture = JSON.parse(fs.readFileSync(path.join(root, 'internal/otlp/testdata/trace.json')));
    const scope = fixture.resourceSpans[0].scopeSpans[0];
    const original = scope.spans[0];
    scope.spans = Array.from({length: 60}, (_, i) => ({...original, traceId: (0x8000 + i).toString(16).padStart(32, '0'), spanId: 'ab'.repeat(8), name: 'review-cursor-collision', parentSpanId: ''}));
    const response = await page.request.post(`${base}/v1/traces`, {headers: {Authorization: `Bearer ${token}`}, data: fixture});
    assert.equal(response.status(), 200, await response.text());
    await page.goto(`${base}/search?q=review-cursor-collision`);
    await page.locator('.search-results article').first().waitFor();
    check('review search: all 60 hits with identical start_ns and span_id appear', await page.locator('.search-results article').count() === 60);
    const links = await page.locator('.search-results article a[href^="/spans/"]').evaluateAll(nodes => nodes.map(node => node.href));
    check('review search: colliding span IDs have 60 unique trace/span links', new Set(links).size === 60);
  };
};
