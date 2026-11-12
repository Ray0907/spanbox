const { chromium, devices } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const { once } = require('node:events');

const root = path.resolve(__dirname, '..');
const port = Number(process.env.E2E_PORT || 14318);
const token = 'mock-dev-token';
const base = `http://127.0.0.1:${port}`;
const log = [];
const errors = [];
let failures = 0;
function report(message) {
  console.log(message);
  log.push(message);
  fs.writeFileSync(path.join(__dirname, 'results.log'), log.join('\n') + '\n');
}
function check(name, passed, detail = '') {
  failures += !passed;
  report(`${passed ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}
async function ready(server) {
  for (let attempt = 0; attempt < 100; attempt++) {
    assert.equal(server.exitCode, null, 'Server exited during startup');
    try {
      if ((await fetch(`${base}/healthz`)).ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('Server startup timed out');
}
const seed = require('./seed.cjs')({root, base, token, report});
const traceFilters = require('./traces.cjs')({base, check});
const traceParity = require('./trace.cjs')({base, check});
const searchTime = require('./search-time.cjs')({base, token, check});
const rangeTap = require('./range-tap.cjs')({base, token, check});
const filterLayout = require('./filter-layout.cjs')({base, token, check});

const {inspect, rowTap} = require('./inspect.cjs')(check);
const {trackStyles, scratchLayerSmoke} = require('./csp.cjs')({root, base, check});
const timePicker = require('./time-picker.cjs')({base, check, trackStyles});
const resubmit = require('./resubmit.cjs')({base, check});
const spanFocus = require('./focus.cjs')({base, check});
const sessionsSearch = require('./sessions-search.cjs')({base, token, check, inspect, rowTap});
const block5 = require('./dashboard-sql-login.cjs')({root, base, token, check, inspect});
const reviewFixes = require('./review-fixes.cjs')({root, base, token, check});

(async () => {
  let server, browser;
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'spanbox-mobile-'));
  const output = fs.openSync(path.join(__dirname, 'server.log'), 'w');
  report(`Started ${new Date().toISOString()}\nCommand: pnpm --dir e2e-mobile test\nServer: ${base}\nAuth: isolated throwaway credential (redacted)\nBrowser: Playwright Chromium, iPhone 13, hasTouch=true, viewport=390x844`);
  try {
    // The optional UI at /ui is built from web-ui and embedded into the binary.
    execFileSync('pnpm', ['--dir', 'web-ui', 'install', '--frozen-lockfile'], { cwd: root, stdio: 'inherit' });
    execFileSync('pnpm', ['--dir', 'web-ui', 'build'], { cwd: root, stdio: 'inherit' });
    const binary = path.join(temporary, 'spanbox');
    execFileSync('go', ['build', '-o', binary, './cmd/spanbox'], { cwd: root, stdio: 'inherit' });
    server = spawn(binary, [], { cwd: root, env: { ...process.env, PORT: String(port), AUTH_TOKEN: token, DATA_DIR: path.join(temporary, 'data'), RETENTION_DAYS: '0' }, stdio: ['ignore', output, output] });
    server.on('error', error => report(`FAIL server process: ${error.message}`));
    await ready(server);
    check('real Go server built and started', true, base);
    await seed();
    browser = await chromium.launch();
    const context = await browser.newContext({ ...devices['iPhone 13'], hasTouch: true, viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
    const page = await context.newPage();
    page.on('console', message => { if (message.type() === 'error') errors.push(`${page.url()}: ${message.text()}`); });
    page.on('pageerror', error => errors.push(`${page.url()}: ${error.message}`));
    const assets = path.join(root, 'internal/web/static/ui/assets');
    const cssFile = fs.readdirSync(assets).find(name => /^index-.*\.css$/.test(name));
    assert.ok(cssFile, 'SPA application stylesheet must exist');
    const css = fs.readFileSync(path.join(assets, cssFile), 'utf8');
    check('CSS: no remaining 100vh', !css.includes('100vh'));
    check('CSS: SPA body/theme 100dvh and panel calc(100dvh - 72px)', /body\{[^}]*min-height:100dvh/.test(css) && /max-height:calc\(100dvh\s*-\s*72px\)/.test(css) && /\[data-astryx-theme\]\{[^}]*min-height:100dvh/.test(css));
    check('CSS: safe-area top and bottom with 0px fallback', /env\(safe-area-inset-top,\s*0px\)/.test(css) && /env\(safe-area-inset-bottom,\s*0px\)/.test(css));
    check('CSS: touch active feedback retained', ['a:active', 'summary:active', 'tbody tr:active', 'button:active'].every(selector => css.includes(selector)));

    await page.goto(`${base}/login`);
    await block5.loginShell(page);
    await inspect(page, 'login', true);
    const guards = await page.evaluate(async () => {
      const sheet = new CSSStyleSheet();
      const link = document.querySelector('link[rel="stylesheet"][href*="/assets/index-"]');
      if (!link) throw new Error('SPA application stylesheet link missing');
      const response = await fetch(link.href);
      if (!response.ok) throw new Error('SPA application stylesheet failed to load');
      sheet.replaceSync(await response.text());
      const hoverRules = [];
      function visit(rules, media = []) {
        for (const rule of rules) {
          const parents = rule instanceof CSSMediaRule ? [...media, rule.conditionText] : media;
          if (rule.selectorText?.includes(':hover')) hoverRules.push({ selector: rule.selectorText, media: parents });
          if (rule.cssRules) visit(rule.cssRules, parents);
        }
      }
      visit(sheet.cssRules);
      return hoverRules;
    });
    const hoverCount = guards.reduce((count, rule) => count + (rule.selector.match(/:hover/g) || []).length, 0);
    check('CSSOM: SPA application hover selectors guarded by fine pointer + hover', hoverCount > 0 && guards.every(rule => rule.media.some(media => media.includes('(hover: hover)') && media.includes('(pointer: fine)'))), `${hoverCount} selectors, ${guards.length} guarded rules`);
    await page.locator('input[name="token"]').fill(token);
    await page.getByRole('button').tap();
    await page.waitForURL(`${base}/traces`);
    const shell = await page.request.get(`${base}/traces`);
    const hasShell = shell.status() === 200 && (await shell.text()).includes('id="root"');
    check('production traces: authenticated route is the SPA shell', hasShell);
    assert.ok(hasShell, 'production /traces shell required before parity checks');
    await page.goto(`${base}/traces`);
    await page.getByText('2 shown', {exact: true}).waitFor();
    await inspect(page, 'traces');
    await rowTap(page, 'traces');
    await traceFilters(page);
    await page.locator('tbody a').first().tap();
    await page.waitForURL(/\/traces\//);
    await inspect(page, 'trace-detail');
    const scrollers = await page.locator('.span-tree, .span-panel').evaluateAll(elements => elements.map(element => ({ class: element.className, overscroll: getComputedStyle(element).overscrollBehavior })));
    check('trace-detail: tree and panel overscroll contain', scrollers.length === 2 && scrollers.every(element => element.overscroll === 'contain'), JSON.stringify(scrollers));
    check('trace-detail: mobile panel has no height cap', await page.locator('.span-panel').evaluate(element => getComputedStyle(element).maxHeight === 'none'));
    await page.setViewportSize({ width: 800, height: 844 });
    const panelHeight = await page.locator('.span-panel').evaluate(element => ({ maxHeight: parseFloat(getComputedStyle(element).maxHeight), viewport: innerHeight }));
    check('trace-detail: panel computed max-height = 100dvh - 72px', Math.abs(panelHeight.maxHeight - (panelHeight.viewport - 72)) < 1, JSON.stringify(panelHeight));
    await page.setViewportSize({ width: 390, height: 844 });
    const spanLinks = page.locator('.span-tree button[data-span-id]');
    await spanLinks.first().tap();
    await page.waitForFunction(() => document.querySelector('.span-tree button[data-span-id]')?.getAttribute('aria-current') === 'true');
    const spanResponse = page.waitForResponse(response => response.url().includes('/spans/') && response.status() === 200);
    await spanLinks.nth(1).tap();
    await spanResponse;
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent.includes('chat gpt-4o'));
    check('trace-detail: touch span selection loads real panel', true);
    await page.screenshot({ path: path.join(__dirname, 'trace-detail-selected.png') });
    await traceParity(page);

    await page.goto(`${base}/search`);
    await page.locator('input[name="q"]').fill('weather');
    await page.getByRole('button', { name: 'Search', exact: true }).tap();
    await page.waitForURL(/\/search\?q=weather/);
    await page.locator('.search-results article').first().waitFor();
    check('search: seeded content returned', await page.locator('.search-results article').count() > 0);
    await inspect(page, 'search');
    await page.goto(`${base}/sessions`);
    await page.locator('tbody tr').nth(1).waitFor();
    await inspect(page, 'sessions');
    await rowTap(page, 'sessions');
    const dashboardData = page.waitForResponse(response => response.url().includes('/dashboard/data') && response.status() === 200);
    await page.goto(`${base}/dashboard`);
    await dashboardData;
    await page.locator('.dashboard-chart canvas').nth(3).waitFor();
    check('dashboard: four real charts rendered', await page.locator('.dashboard-chart canvas').count() === 4);
    await inspect(page, 'dashboard');

    // Exercise an actual textarea, not just a CSS source assertion.
    await page.goto(`${base}/sql`);
    const textareaSize = await page.locator('textarea').evaluate(element => parseFloat(getComputedStyle(element).fontSize));
    check('SQL textarea computed font-size >=16px', textareaSize >= 16, `${textareaSize}px`);

    // Optional React UI at /ui: same real server, same login cookie, same CSP.
    await page.addInitScript(trackStyles);
    await page.goto(`${base}/ui`);
    await page.locator('tbody tr').nth(1).waitFor();
    check('ui traces: both seeded traces listed', await page.locator('tbody tr').count() === 2);
    const layerLink = await page.evaluate(() => document.getElementById('astryx-layer-slide-rules')?.tagName);
    check('ui CSP: Layer id belongs to an external LINK', layerLink === 'LINK', layerLink || 'missing');
    const density = page.getByRole('radiogroup', {name: 'Table density', exact: true});
    const mounted = await density.count() === 1;
    check('ui CSP: Layer-backed SegmentedControl mounted', mounted);
    if (mounted) {
      await density.getByRole('radio', {name: 'Comfortable', exact: true}).tap();
      check('ui CSP: SegmentedControl interaction works', await density.getByRole('radio', {name: 'Comfortable', exact: true}).getAttribute('aria-checked') === 'true');
      await density.getByRole('radio', {name: 'Compact', exact: true}).tap();
    }
    const runtimeStyles = await page.evaluate(() => ({added: window.__headStyles, remaining: document.head.querySelectorAll('style').length, violations: window.__cspViolations}));
    check('ui CSP: no runtime STYLE added to head', runtimeStyles.added.length === 0 && runtimeStyles.remaining === 0, JSON.stringify(runtimeStyles));
    check('ui CSP: no policy violations', runtimeStyles.violations.length === 0, JSON.stringify(runtimeStyles.violations));
    const uiWidth = async () => page.evaluate(() => ({ viewport: innerWidth, html: document.documentElement.scrollWidth, body: document.body.scrollWidth }));
    let width = await uiWidth();
    check('ui traces: no horizontal page overflow at 390px', width.viewport === 390 && width.html <= 390 && width.body <= 390, JSON.stringify(width));
    await page.screenshot({ path: path.join(__dirname, 'ui-traces.png') });
    await page.locator('tbody a').first().tap();
    await page.getByRole('heading', { name: 'Span tree' }).waitFor();
    const spanButtons = await page.locator('.span-tree button[data-span-id]').count();
    check('ui trace: every span is a button', spanButtons === 3, `${spanButtons} buttons`);
    // The first LLM span is selected by default; picking the root span must load its own panel.
    check('ui trace: first LLM span is selected by default', await page.locator('.span-tree button[data-span-id][aria-current="true"]').innerText().then(text => text.includes('chat gpt-4o')));
    const rootSpan = page.locator('.span-tree button[data-span-id]').first();
    const rootText = await rootSpan.innerText();
    const uiSpan = page.waitForResponse(response => response.url().includes('/spans/') && response.status() === 200);
    await rootSpan.tap();
    await uiSpan;
    await page.waitForFunction(() => document.querySelector('.span-tree button[data-span-id]')?.getAttribute('aria-current') === 'true');
    check('ui trace: touch span selection loads the panel and marks it current', true, rootText.replace(/\s+/g, ' '));
    width = await uiWidth();
    check('ui trace: no horizontal page overflow at 390px', width.viewport === 390 && width.html <= 390 && width.body <= 390, JSON.stringify(width));
    await page.screenshot({ path: path.join(__dirname, 'ui-trace.png') });
    await searchTime(page);
    await rangeTap(page);
    await filterLayout(page);
    await sessionsSearch(page);
    await resubmit(page);
    await spanFocus(page);
    await block5.run(page, browser);
    await reviewFixes(page);
    await timePicker(page, browser);
    check('zero console errors / uncaught page errors', errors.length === 0, JSON.stringify(errors));
    await scratchLayerSmoke(browser, temporary);
  } catch (error) {
    failures++;
    report(`FAIL fatal: ${error.stack}`);
    check('zero console errors / uncaught page errors', errors.length === 0, JSON.stringify(errors));
  } finally {
    if (browser) await browser.close();
    if (server && server.exitCode === null) {
      const exited = once(server, 'exit');
      server.kill('SIGTERM');
      await exited;
    }
    fs.closeSync(output);
    fs.rmSync(temporary, { recursive: true, force: true });
    report(`RESULT: ${failures === 0 ? 'PASS' : 'FAIL'} (${failures} failed assertions)\nReal phone still required: tap delay, notch/safe-area rendering, keyboard resize, real Safari hover stickiness.`);
    process.exitCode = failures ? 1 : 0;
  }
})();
