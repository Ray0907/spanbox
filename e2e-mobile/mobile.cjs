const { chromium, devices } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { execFileSync, spawn } = require('node:child_process');
const { once } = require('node:events');

const root = path.resolve(__dirname, '..');
const port = Number(process.env.E2E_PORT || 14318);
const token = crypto.randomBytes(16).toString('hex');
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
async function seed() {
  // Reuse the real OTLP fixture, with current timestamps and two sessions.
  const fixture = JSON.parse(fs.readFileSync(path.join(root, 'internal/otlp/testdata/trace.json')));
  const now = BigInt(Date.now()) * 1000000n;
  for (let index = 0; index < 2; index++) {
    const data = structuredClone(fixture);
    const spans = data.resourceSpans[0].scopeSpans[0].spans;
    for (const span of spans) {
      span.traceId = Buffer.alloc(16, index + 1).toString('base64');
      span.startTimeUnixNano = String(now + BigInt(span.startTimeUnixNano) - 1300000000n);
      span.endTimeUnixNano = String(now + BigInt(span.endTimeUnixNano) - 1300000000n);
      span.attributes.push({ key: 'gen_ai.conversation.id', value: { stringValue: `mobile-session-${index + 1}` } });
      for (const event of span.events || []) {
        event.timeUnixNano = String(now + BigInt(event.timeUnixNano) - 1300000000n);
      }
    }
    const response = await fetch(`${base}/v1/traces`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(data)
    });
    assert.equal(response.status, 200, await response.text());
  }
  report('PASS seed — two real OTLP traces, three spans each, two sessions');
}
async function inspect(page, name, login = false) {
  const state = await page.evaluate(() => {
    const css = element => getComputedStyle(element);
    const html = css(document.documentElement);
    const body = css(document.body);
    const topbar = document.querySelector('.topbar');
    return {
      viewport: document.querySelector('meta[name="viewport"]').content,
      themes: [...document.querySelectorAll('meta[name="theme-color"]')].map(meta => ({ content: meta.content, media: meta.media })),
      background: css(topbar || document.body).backgroundColor,
      tap: html.webkitTapHighlightColor,
      textAdjust: html.getPropertyValue('-webkit-text-size-adjust'),
      bodySelect: body.userSelect || body.webkitUserSelect,
      navSelect: [...document.querySelectorAll('nav a')].map(a => css(a).userSelect || css(a).webkitUserSelect),
      controlSelect: [...document.querySelectorAll('button, .button, nav a, summary, .tree-select, [role="tab"], .tab, .chip')].map(control => css(control).userSelect),
      touchAction: [...document.querySelectorAll('a, button, summary, [role="button"], .tree-select')].map(control => css(control).touchAction),
      controls: [...document.querySelectorAll('input, select, textarea')].map(input => ({ tag: input.tagName, size: parseFloat(css(input).fontSize) })),
      bodyHeight: parseFloat(body.minHeight),
      viewportHeight: innerHeight,
      width: innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      bodyWidth: document.body.scrollWidth,
      overscroll: [html.overscrollBehavior, body.overscrollBehavior],
      coarse: matchMedia('(pointer: coarse)').matches,
      hover: matchMedia('(hover: hover) and (pointer: fine)').matches,
      touch: navigator.maxTouchPoints
    };
  });
  check(`${name}: iPhone touch profile`, state.coarse && !state.hover && state.touch > 0, JSON.stringify({ coarse: state.coarse, hover: state.hover, touch: state.touch }));
  check(`${name}: viewport-fit / keyboard resize / zoom allowed`, state.viewport.includes('viewport-fit=cover') && state.viewport.includes('interactive-widget=resizes-content') && !/user-scalable|maximum-scale/.test(state.viewport), state.viewport);
  check(`${name}: two scheme-specific theme colors`, state.themes.length === 2 && ['dark', 'light'].every(scheme => state.themes.some(meta => meta.media === `(prefers-color-scheme: ${scheme})`)), JSON.stringify(state.themes));
  check(`${name}: transparent tap highlight`, ['rgba(0, 0, 0, 0)', 'transparent'].includes(state.tap), state.tap);
  check(`${name}: text-size-adjust 100%`, state.textAdjust === '100%', state.textAdjust);
  check(`${name}: inputs/select/textarea >=16px`, state.controls.every(control => control.size >= 16), JSON.stringify(state.controls));
  check(`${name}: ${login ? '100svh login' : '100dvh body'} computed min-height`, Math.abs(state.bodyHeight - state.viewportHeight) < 1, `${state.bodyHeight}px / ${state.viewportHeight}px viewport`);
  check(`${name}: content remains selectable`, state.bodySelect !== 'none', state.bodySelect);
  check(`${name}: control-only selection disabled`, state.controlSelect.every(value => value === 'none'), JSON.stringify(state.controlSelect));
  check(`${name}: links/buttons/summary touch-action manipulation`, state.touchAction.every(value => value === 'manipulation'), JSON.stringify(state.touchAction));
  if (!login) check(`${name}: nav links user-select none`, state.navSelect.length > 0 && state.navSelect.every(value => value === 'none'), JSON.stringify(state.navSelect));
  check(`${name}: page overscroll none`, state.overscroll.every(value => value === 'none'), JSON.stringify(state.overscroll));
  check(`${name}: no horizontal page overflow at 390px`, state.width === 390 && state.documentWidth <= 390 && state.bodyWidth <= 390, JSON.stringify({ viewport: state.width, html: state.documentWidth, body: state.bodyWidth }));

  for (const scheme of ['dark', 'light']) {
    await page.emulateMedia({ colorScheme: scheme });
    const colors = await page.evaluate(scheme => {
      const meta = document.querySelector(`meta[name="theme-color"][media="(prefers-color-scheme: ${scheme})"]`);
      const components = meta.content.slice(1).match(/../g).map(hex => parseInt(hex, 16));
      const theme = `rgb(${components.join(', ')})`; // No inline styles: respect the real server's CSP.
      const background = getComputedStyle(document.querySelector('.topbar') || document.body).backgroundColor;
      return { theme, background };
    }, scheme);
    check(`${name}: ${scheme} theme matches topbar/page`, colors.theme === colors.background, JSON.stringify(colors));
  }
  await page.emulateMedia({ colorScheme: 'dark' });
  // Viewport screenshots avoid Playwright 1.61's full-page capture resetting Chromium touch emulation.
  await page.screenshot({ path: path.join(__dirname, `${name}.png`) });
}
async function rowTap(page, name) {
  const rows = page.locator('tbody tr');
  assert.ok(await rows.count() >= 2, `${name} needs two nonempty rows`);
  const before = await rows.first().evaluate(row => getComputedStyle(row).backgroundColor);
  await rows.first().locator('td').first().tap();
  // Poll until press feedback has finished; a sticky hover never reaches the original color.
  await page.waitForFunction(background => getComputedStyle(document.querySelector('tbody tr')).backgroundColor === background, before, { timeout: 2000 }).catch(() => {});
  const after = await rows.first().evaluate(row => getComputedStyle(row).backgroundColor);
  const untouched = await rows.nth(1).evaluate(row => getComputedStyle(row).backgroundColor);
  check(`${name}: tapped row has no sticky hover background`, before === after && after === untouched, JSON.stringify({ before, after, untouched }));
  await page.screenshot({ path: path.join(__dirname, `${name}-after-tap.png`) });
}

(async () => {
  let server, browser;
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'spanbox-mobile-'));
  const output = fs.openSync(path.join(__dirname, 'server.log'), 'w');
  report(`Started ${new Date().toISOString()}\nCommand: pnpm --dir e2e-mobile test\nServer: ${base}\nAUTH_TOKEN=${token}\nBrowser: Playwright Chromium, iPhone 13, hasTouch=true, viewport=390x844`);
  try {
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
    const css = fs.readFileSync(path.join(root, 'internal/web/static/app.css'), 'utf8');
    check('CSS: no remaining 100vh', !css.includes('100vh'));
    check('CSS: body 100dvh / panel calc(100dvh - 72px) / login 100svh', /body\s*\{\s*min-height: 100dvh/.test(css) && css.includes('max-height: calc(100dvh - 72px)') && /\.login-page\s*\{[^}]*min-height: 100svh/.test(css));
    check('CSS: safe-area top and bottom with 0px fallback', css.includes('env(safe-area-inset-top, 0px)') && css.includes('env(safe-area-inset-bottom, 0px)'));
    check('CSS: touch active feedback retained', ['a:active', 'summary:active', '.tree-select:active', 'tbody tr:active', 'button:active'].every(selector => css.includes(selector)));

    await page.goto(`${base}/login`);
    await inspect(page, 'login', true);
    const guards = await page.evaluate(() => {
      const hoverRules = [];
      function visit(rules, media = []) {
        for (const rule of rules) {
          const parents = rule instanceof CSSMediaRule ? [...media, rule.conditionText] : media;
          if (rule.selectorText?.includes(':hover')) hoverRules.push({ selector: rule.selectorText, media: parents });
          if (rule.cssRules) visit(rule.cssRules, parents);
        }
      }
      visit([...document.styleSheets].find(sheet => sheet.href.endsWith('/app.css')).cssRules);
      return hoverRules;
    });
    const hoverCount = guards.reduce((count, rule) => count + (rule.selector.match(/:hover/g) || []).length, 0);
    check('CSSOM: all 16 hover selectors guarded by fine pointer + hover', hoverCount === 16 && guards.every(rule => rule.media.some(media => media.includes('(hover: hover)') && media.includes('(pointer: fine)'))), `${hoverCount} selectors, ${guards.length} guarded rules`);
    await page.locator('input[name="token"]').fill(token);
    await page.getByRole('button').tap();
    await page.waitForURL(`${base}/`);
    await inspect(page, 'traces');
    await rowTap(page, 'traces');
    await page.locator('a.trace-link').first().tap();
    await page.waitForURL(/\/traces\//);
    await inspect(page, 'trace-detail');
    const scrollers = await page.locator('.span-tree, .span-panel').evaluateAll(elements => elements.map(element => ({ class: element.className, overscroll: getComputedStyle(element).overscrollBehavior })));
    check('trace-detail: tree and panel overscroll contain', scrollers.length === 2 && scrollers.every(element => element.overscroll === 'contain'), JSON.stringify(scrollers));
    check('trace-detail: mobile panel has no height cap', await page.locator('.span-panel').evaluate(element => getComputedStyle(element).maxHeight === 'none'));
    await page.setViewportSize({ width: 800, height: 844 });
    const panelHeight = await page.locator('.span-panel').evaluate(element => ({ maxHeight: parseFloat(getComputedStyle(element).maxHeight), viewport: innerHeight }));
    check('trace-detail: panel computed max-height = 100dvh - 72px', Math.abs(panelHeight.maxHeight - (panelHeight.viewport - 72)) < 1, JSON.stringify(panelHeight));
    await page.setViewportSize({ width: 390, height: 844 });
    const spanLinks = page.locator('.tree-select');
    const spanResponse = page.waitForResponse(response => response.url().includes('/spans/') && response.status() === 200);
    await spanLinks.nth(1).tap();
    await spanResponse;
    await page.waitForFunction(() => document.querySelector('#panel h2')?.textContent.includes('chat gpt-4o'));
    check('trace-detail: touch span selection loads real panel', true);
    await page.screenshot({ path: path.join(__dirname, 'trace-detail-selected.png') });

    await page.goto(`${base}/search`);
    await page.locator('input[name="q"]').fill('weather');
    await page.getByRole('button', { name: 'Search', exact: true }).tap();
    await page.waitForURL(/\/search\?q=weather/);
    check('search: seeded content returned', await page.locator('.search-results article').count() > 0);
    await inspect(page, 'search');
    await page.goto(`${base}/sessions`);
    await inspect(page, 'sessions');
    await rowTap(page, 'sessions');
    const dashboardData = page.waitForResponse(response => response.url().includes('/dashboard/data') && response.status() === 200);
    await page.goto(`${base}/dashboard`);
    await dashboardData;
    await page.locator('.uplot').nth(3).waitFor();
    check('dashboard: four real charts rendered', await page.locator('.uplot').count() === 4);
    await inspect(page, 'dashboard');

    // Exercise an actual textarea, not just a CSS source assertion.
    await page.goto(`${base}/sql`);
    const textareaSize = await page.locator('textarea').evaluate(element => parseFloat(getComputedStyle(element).fontSize));
    check('SQL textarea computed font-size >=16px', textareaSize >= 16, `${textareaSize}px`);
    check('zero console errors / uncaught page errors', errors.length === 0, JSON.stringify(errors));
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
