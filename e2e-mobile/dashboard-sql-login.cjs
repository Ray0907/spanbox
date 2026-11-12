const assert = require('node:assert/strict');

module.exports = ({root, base, token, check, inspect}) => ({
  async loginShell(page) {
    const shell = await page.locator('#root').count() === 1;
    check('login: logged-out GET serves the SPA shell', shell);
    assert.ok(shell, 'logged-out /login must serve the SPA shell');
  },
  async run(page, browser) {
    await page.goto(`${base}/`);
    await page.waitForURL(`${base}/traces`);
    check('root: shell routes to /traces', await page.locator('#root').count() === 1);
    await page.getByRole('link', {name: 'Dashboard', exact: true}).click();
    await page.waitForURL(`${base}/dashboard`);
    await page.locator('canvas').nth(3).waitFor();
    await inspect(page, 'dashboard-port');
    check('dashboard: five summary metrics and Models table', (await page.getByRole('region', {name: 'Summary'}).innerText()).includes('Error rate') && await page.getByRole('heading', {name: 'Models', exact: true}).count() === 1);
    check('dashboard: four accessible charts with hidden numeric values', await page.locator('canvas[aria-label][aria-describedby]').count() === 4 && await page.locator('[data-chart-values] tbody tr').count() >= 4);
    const pixelData = () => page.locator('canvas').evaluateAll(canvases => canvases.map(canvas => {
      const data = canvas.getContext('2d').getImageData(0, 0, canvas.width, canvas.height).data;
      const primary = getComputedStyle(canvas.parentElement.querySelector('span')).color.match(/[\d.]+/g).map(Number);
      const neutral = getComputedStyle(canvas.parentElement.querySelectorAll('span')[1]).color.match(/[\d.]+/g).map(Number);
      let nonempty = 0, colored = 0, secondary = 0, hash = 0;
      for (let i = 0; i < data.length; i += 4) {
        if (data[i + 3]) nonempty++;
        if (data[i + 3] > 128 && primary.every((c, j) => Math.abs(data[i + j] - c) < 5)) colored++;
        if (data[i + 3] > 128 && neutral.every((c, j) => Math.abs(data[i + j] - c) < 5)) secondary++;
        hash = (hash + data[i] * 3 + data[i + 1] * 5 + data[i + 2] * 7) >>> 0;
      }
      return {width: canvas.width, nonempty, colored, secondary, hash, primary};
    }));
    const dark = await pixelData();
    check('dashboard: all four dark canvases have drawn pixels', dark.length === 4 && dark.every((c, i) => c.nonempty > 30 && (i === 2 ? c.secondary > 5 : c.colored > 5)), JSON.stringify(dark));
    check('dashboard: uPlot injects no connected inline styles or STYLE tags', await page.locator('.dashboard-chart[style], .dashboard-chart [style], style').count() === 0);
    await page.emulateMedia({colorScheme: 'light'});
    await page.waitForTimeout(120);
    const light = await pixelData();
    check('dashboard: light scheme redraws every canvas from tokens', light.every((c, i) => c.nonempty > 30 && (i === 2 ? c.secondary > 5 : c.colored > 5) && c.hash !== dark[i].hash), JSON.stringify(light));
    await inspect(page, 'dashboard-light');
    const before = light[0].width;
    await page.setViewportSize({width: 800, height: 844});
    await page.waitForFunction(width => document.querySelector('canvas')?.width !== width, before);
    check('dashboard: canvas attributes resize with container', (await pixelData())[0].width !== before);
    await page.setViewportSize({width: 390, height: 844});
    await page.emulateMedia({colorScheme: 'dark'});
    const range = page.getByRole('combobox', {name: /^Range/});
    await range.click();
    check('dashboard: all four legacy ranges', await page.getByRole('option').count() === 4);
    await page.keyboard.press('Escape');
    for (const [label, value] of [['15 minutes', '15m'], ['1 hour', '1h'], ['24 hours', '24h'], ['7 days', '7d']]) {
      await range.click();
      await page.getByRole('option', {name: label, exact: true}).click();
      await page.waitForURL(`${base}/dashboard?range=${value}`);
      await page.locator('canvas').nth(3).waitFor();
      check(`dashboard: ${value} range selected`, (await range.innerText()).includes(label));
    }
    await page.goto(`${base}/dashboard?range=custom&from=2099-01-01T00:00:00Z&to=2099-01-02T00:00:00Z`);
    await page.getByText('No traces in this range.', {exact: true}).waitFor();
    check('dashboard: explicit empty state and models empty row', await page.getByText('No model calls in this range.', {exact: true}).count() === 1);

    await page.goto(`${base}/sql`);
    await page.getByRole('textbox', {name: /SQL query/}).waitFor();
    await inspect(page, 'sql-port');
    const area = page.getByRole('textbox', {name: /SQL query/});
    check('sql: query remains natively required', await area.evaluate(el => el.required && !el.checkValidity()));
    check('sql: native mobile font >=16px', await area.evaluate(el => parseFloat(getComputedStyle(el).fontSize) >= 16));
    check('sql: enabled nav entry', await page.getByRole('navigation', {name: 'Primary'}).getByRole('link', {name: 'SQL', exact: true}).count() === 1);
    await page.getByRole('button', {name: 'Schema', exact: true}).click();
    await page.getByText('spans_fts', {exact: true}).waitFor();
    check('sql: schema opens a Collapsible', await page.getByRole('button', {name: 'Schema', exact: true}).getAttribute('aria-expanded') === 'true');
    await area.fill("SELECT '<script>' AS literal, count(*) AS n FROM spans");
    await page.getByRole('button', {name: 'Run', exact: true}).click();
    await page.getByRole('cell', {name: '<script>', exact: true}).waitFor();
    check('sql: real SELECT results rendered as escaped Table cells', await page.locator('script:not([src])').count() === 0);
    await area.fill('SELECT 42 AS keyboard');
    await area.press('Control+Enter');
    await page.getByRole('cell', {name: '42', exact: true}).waitFor();
    await area.fill('SELECT 43 AS keyboard');
    await area.press('Meta+Enter');
    await page.getByRole('cell', {name: '43', exact: true}).waitFor();
    check('sql: Ctrl and Cmd Enter run', true);
    await area.fill('SELECT id FROM spans WHERE 0');
    await page.getByRole('button', {name: 'Run', exact: true}).click();
    await page.getByText('Query returned no rows.', {exact: true}).waitFor();
    check('sql: empty result state retains column headers', await page.getByRole('columnheader', {name: 'id', exact: true}).count() === 1);
    await area.fill('SELECT a.id FROM spans a, spans b, spans c, spans d, spans e, spans f, spans g, spans h');
    await page.getByRole('button', {name: 'Run', exact: true}).click();
    await page.getByText('Results truncated.', {exact: true}).waitFor();
    check('sql: row cap Banner and 1000 result rows', await page.locator('table tbody tr').count() === 1000);
    await inspect(page, 'sql-results');
    check('SQL: no injected STYLE tags or CSP violations', await page.locator('style').count() === 0 && await page.evaluate(() => window.__cspViolations.length === 0));

    // Deliberate HTTP failures use a separate page so the main zero-error check
    // still detects every unexpected console error on the happy paths.
    const ctx = await browser.newContext({viewport: {width: 390, height: 844}, isMobile: true, hasTouch: true});
    const login = await ctx.newPage();
    const unexpected = [], expectedHTTP = [];
    login.on('pageerror', e => unexpected.push(e.message));
    login.on('console', message => {
      if (message.type() !== 'error') return;
      if (/Failed to load resource.*(400|401)/.test(message.text())) expectedHTTP.push(message.text());
      else unexpected.push(message.text());
    });
    await login.goto(`${base}/login?next=https://example.invalid/`);
    await inspect(login, 'login-port', true);
    // Password inputs do not have the implicit textbox role.
    const password = login.locator('input[name="token"]');
    await password.waitFor();
    check('login: form fallback posts the token, never a URL query', await login.locator('form').getAttribute('method') === 'post' && await login.locator('form').getAttribute('action') === '/login');
    check('login: token remains natively required', await password.evaluate(el => el.required && !el.checkValidity()));
    check('login: password input and autocomplete off', await password.getAttribute('type') === 'password' && await password.getAttribute('autocomplete') === 'off');
    await password.fill('deliberately-wrong-throwaway');
    await password.press('Enter');
    await login.getByText('Invalid token.', {exact: true}).waitFor();
    check('login: 401 error Banner and no cookie', (await ctx.cookies()).length === 0);
    await password.fill(token);
    await password.press('Enter');
    await login.waitForURL(`${base}/traces`);
    check('login: keyboard success ignores external target and routes to traces', login.url() === `${base}/traces` && !login.url().includes(token));
    const cookie = (await ctx.cookies()).find(c => c.name === 'spanbox_session');
    check('login: cookie remains HttpOnly Strict with non-plaintext value', cookie && cookie.httpOnly && cookie.sameSite === 'Strict' && cookie.value !== token && cookie.path === '/');
    await login.goto(`${base}/login`);
    await password.waitFor();
    check('login: authenticated GET still renders login, no new redirect', login.url() === `${base}/login`);
    await login.goto(`${base}/sql`);
    const query = login.getByRole('textbox', {name: /SQL query/});
    await query.fill('DELETE FROM spans');
    await login.getByRole('button', {name: 'Run', exact: true}).click();
    await login.getByRole('alert').filter({hasText: /rejected|read.only|SELECT/i}).waitFor();
    check('sql: server write guard error shown as Banner', true);
    const oversized = 'SELECT 1 -- ' + 'x'.repeat(21000);
    const oversizedResponse = await login.request.post(`${base}/sql?format=json`, {form: {sql: oversized}});
    const serverError = (await oversizedResponse.json()).error;
    let oversizedPosts = 0;
    const countPosts = request => { if (request.method() === 'POST' && request.url().includes('/sql')) oversizedPosts++; };
    login.on('request', countPosts);
    await query.fill(oversized);
    await login.getByRole('button', {name: 'Run', exact: true}).click();
    await login.getByText(serverError, {exact: true}).waitFor();
    check('sql: >20KB form blocked client-side with matching server error', oversizedResponse.status() === 400 && oversizedPosts === 0 && await login.getByText(/20 KiB/).count() > 0);
    login.off('request', countPosts);
    await query.fill('SELECT ' + ' '.repeat(17000) + '1');
    await login.getByRole('button', {name: 'Run', exact: true}).click();
    await login.getByRole('alert').filter({hasText: /16 KiB|too large|too long/i}).waitFor();
    check('sql: server 16KiB query limit remains visible', true);
    await ctx.clearCookies();
    await query.fill('SELECT 1');
    await login.getByRole('button', {name: 'Run', exact: true}).click();
    await login.waitForURL(`${base}/login`);
    check('sql: expired authentication returns to login', true);
    check('negative flows: no token in URL or console, no unexpected errors', unexpected.length === 0 && !expectedHTTP.some(line => line.includes(token)) && !login.url().includes(token), JSON.stringify(unexpected));
    await ctx.close();

    const disabled = await browser.newContext({viewport: {width: 390, height: 844}});
    const hidden = await disabled.newPage();
    await hidden.route('**/sql?format=json', route => route.fulfill({status: 404, json: {error: 'disabled'}}));
    // Exercise the existing nav availability probe without changing server config.
    await disabled.addCookies(await page.context().cookies());
    const unavailable = hidden.waitForResponse(response => response.url() === `${base}/sql?format=json`);
    await hidden.goto(`${base}/traces`);
    await unavailable;
    await hidden.locator('tbody tr').first().waitFor();
    check('sql: nav hidden when availability JSON returns 404', await hidden.getByRole('navigation').getByRole('link', {name: 'SQL', exact: true}).count() === 0);
    await disabled.close();

    const fixture = JSON.parse(require('node:fs').readFileSync(require('node:path').join(root, 'internal/otlp/testdata/trace.json')));
    const resource = fixture.resourceSpans[0];
    resource.resource.attributes.find(a => a.key === 'service.name').value.stringValue = 'block5-xss';
    const span = resource.scopeSpans[0].spans[0];
    resource.scopeSpans[0].spans = [span];
    span.traceId = Buffer.alloc(16, 0x78).toString('base64');
    span.name = '<script>x</script>';
    span.startTimeUnixNano = String(BigInt(Date.now()) * 1000000n);
    span.endTimeUnixNano = String(BigInt(span.startTimeUnixNano) + 1000000n);
    const ingest = await page.request.post(`${base}/v1/traces`, {headers: {Authorization: `Bearer ${token}`}, data: fixture});
    assert.equal(ingest.status(), 200, 'XSS fixture must ingest');
    await page.goto(`${base}/?service=block5-xss`);
    await page.waitForURL(`${base}/traces?service=block5-xss`);
    await page.getByRole('link', {name: '<script>x</script>', exact: true}).waitFor();
    check('root traces: malicious telemetry renders literally, never a script', await page.locator('main script').count() === 0);
  }
});
