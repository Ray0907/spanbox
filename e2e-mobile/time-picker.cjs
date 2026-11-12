// Pointer-only UTC time selection and shared-control/cursor regressions under the real CSP.
const {devices} = require('playwright');
const path = require('node:path');
const {field, dateInput, press, rangeTrigger} = require('./picker.cjs');
const timeInput = (page, name) => page.getByLabel(`${name} (UTC) time`, {exact: true});

module.exports = ({base, check, trackStyles}) => async function timePicker(source, browser) {
  const storageState = await source.context().storageState();
  for (const width of [1440, 390]) {
    const context = await browser.newContext({
      ...(width === 390 ? devices['iPhone 13'] : {}), storageState,
      viewport: {width, height: width === 390 ? 844 : 900},
      timezoneId: width === 390 ? 'Asia/Tokyo' : 'America/Los_Angeles', colorScheme: 'dark',
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [], violations = [];
    page.on('console', message => {if (message.type() === 'error') errors.push(message.text());});
    page.on('pageerror', error => errors.push(error.message));
    await page.exposeFunction('recordPickerCSP', directive => violations.push(directive));
    await page.addInitScript(trackStyles);
    await page.addInitScript(() => document.addEventListener('securitypolicyviolation', event => window.recordPickerCSP(event.effectiveDirective)));
    const prefix = `time picker ${width}`;
    const cursor = async (name, locator, expected = 'pointer') => {
      await locator.scrollIntoViewIfNeeded();
      if (width === 1440) await locator.hover();
      const actual = await locator.evaluate(element => getComputedStyle(element).cursor);
      check(`${prefix}: cursor ${name}`, actual === expected, `${actual} (expected ${expected})`);
    };
    const apply = async () => {
      await press(page, page.getByRole('button', {name: 'Apply', exact: true}));
      await page.waitForFunction(() => new URL(location.href).searchParams.get('from')?.endsWith('Z'));
    };
    try {
      for (const route of ['traces', 'sessions']) {
        // Legacy links with explicit offsets must still display and submit UTC wall-clock time.
        const query = new URLSearchParams({range: 'custom', from: '2026-05-06T10:07:00+02:00', to: '2026-05-08T20:00:00Z'});
        await page.goto(`${base}/${route}?${query}`);
        await dateInput(page, 'From').waitFor();
        await press(page, timeInput(page, 'From'));
        const list = page.getByRole('listbox');
        const opens = await list.waitFor({state: 'visible', timeout: 2000}).then(() => true, () => false);
        check(`${prefix}: ${route} From time opens by pointer only`, opens);
        if (!opens) break;
        const box = await list.boundingBox();
        check(`${prefix}: ${route} time popover is inside viewport`, !!box && box.x >= 0 && box.y >= 0 && box.x + box.width <= width && box.y + box.height <= (width === 390 ? 844 : 900), JSON.stringify(box));
        if (route === 'traces') await page.screenshot({path: path.join(__dirname, `b7-shot-${width}.png`), scale: 'css'});
        await cursor('time option', list.getByRole('option', {name: '10:30', exact: true}));
        await press(page, list.getByRole('option', {name: '10:30', exact: true}));
        await apply();
        let params = new URL(page.url()).searchParams;
        check(`${prefix}: ${route} pointer-only From reaches UTC URL and preserves To`, params.get('from') === '2026-05-06T10:30:00Z' && params.get('to') === '2026-05-08T20:00:00Z', page.url());
        await page.reload();
        check(`${prefix}: ${route} picker round-trips date and time`, (await dateInput(page, 'From').inputValue()).includes('2026') && await timeInput(page, 'From').inputValue() === '10:30');
        await cursor('date trigger', field(page, 'From').getByRole('button', {name: 'Open calendar'}));
        await cursor('editable time trigger', timeInput(page, 'From'), 'text');
        await cursor('date text input', dateInput(page, 'From'), 'text');
        await press(page, field(page, 'From').getByRole('button', {name: 'Open calendar'}));
        const day = page.getByRole('dialog').locator('button[data-date="2026-05-07"]');
        await cursor('date cell', day);
        await press(page, day);
        await apply();
        params = new URL(page.url()).searchParams;
        check(`${prefix}: ${route} date change preserves selected time`, params.get('from') === '2026-05-07T10:30:00Z' && params.get('to') === '2026-05-08T20:00:00Z', page.url());
        await press(page, timeInput(page, 'To'));
        await press(page, page.getByRole('listbox').getByRole('option', {name: '21:45', exact: true}));
        await apply();
        params = new URL(page.url()).searchParams;
        check(`${prefix}: ${route} To pointer selection preserves both dates and From time`, params.get('from') === '2026-05-07T10:30:00Z' && params.get('to') === '2026-05-08T21:45:00Z');
        await timeInput(page, 'From').fill('11:17');
        await page.keyboard.press('Tab');
        await apply();
        check(`${prefix}: ${route} keyboard accepts minutes between presets`, new URL(page.url()).searchParams.get('from') === '2026-05-07T11:17:00Z');
        await timeInput(page, 'From').focus();
        await page.keyboard.press('Escape');
        await page.keyboard.press('ArrowUp');
        await page.keyboard.press('Tab');
        await apply();
        check(`${prefix}: ${route} closed keyboard arrow still increments one minute`, new URL(page.url()).searchParams.get('from') === '2026-05-07T11:18:00Z');
        await timeInput(page, 'From').focus();
        await page.keyboard.press('Alt+ArrowDown');
        await page.keyboard.press('Home');
        await page.keyboard.press('ArrowDown');
        await page.keyboard.press('Enter');
        await apply();
        check(`${prefix}: ${route} keyboard can select a preset`, new URL(page.url()).searchParams.get('from') === '2026-05-07T00:15:00Z');
      }
      // These are real app controls, not a parallel demo UI.
      await page.goto(`${base}/traces`);
      await cursor('button', page.getByRole('button', {name: 'Apply', exact: true}));
      await cursor('navigation link', page.getByRole('navigation', {name: 'Primary'}).getByRole('link', {name: 'Traces', exact: true}));
      await cursor('Selector trigger', rangeTrigger(page));
      await press(page, rangeTrigger(page));
      await cursor('Selector option', page.getByRole('option', {name: 'Custom', exact: true}));
      await press(page, page.getByRole('option', {name: 'All retained', exact: true}));
      await cursor('density toggle', page.getByRole('radio', {name: 'Comfortable', exact: true}));
      await cursor('table row link', page.locator('tbody a').first());
      await cursor('checkbox', page.getByRole('checkbox', {name: 'Errors only'}));
      await cursor('checkbox label', page.getByText('Errors only', {exact: true}));
      await cursor('text input', page.getByRole('textbox', {name: 'Model', exact: true}), 'text');
      await cursor('non-clickable heading', page.getByRole('heading', {name: 'Traces', exact: true}), 'auto');
      // There are no tabs in the current app. Exercise the semantic global selector with a tiny fixture.
      await page.evaluate(() => {
        const fixture = document.createElement('div');
        fixture.id = 'cursor-fixture';
        for (const attrs of [{role: 'tab'}, {role: 'tab', 'aria-disabled': 'true'}, {disabled: ''}, {role: 'button', 'aria-disabled': 'true'}]) {
          const button = document.createElement('button');
          for (const [name, value] of Object.entries(attrs)) button.setAttribute(name, value);
          button.textContent = 'Cursor fixture';
          fixture.append(button);
        }
        document.querySelector('main').append(fixture);
      });
      await cursor('semantic tab fixture', page.locator('#cursor-fixture > :nth-child(1)'));
      for (let i = 2; i <= 4; i++) {
        const actual = await page.locator(`#cursor-fixture > :nth-child(${i})`).evaluate(element => getComputedStyle(element).cursor);
        check(`${prefix}: disabled fixture ${i} is not pointer`, actual !== 'pointer', actual);
      }
      await page.locator('#cursor-fixture').evaluate(element => element.remove());
      await page.goto(`${base}/sql`);
      await cursor('textarea', page.getByRole('textbox', {name: 'SQL query'}), 'text');
      let release;
      const held = new Promise(resolve => {release = resolve;});
      await page.route('**/sql?format=json', async route => {
        if (route.request().method() === 'POST') await held;
        await route.continue();
      });
      try {
        await page.getByRole('textbox', {name: 'SQL query'}).fill('SELECT 1');
        await press(page, page.getByRole('button', {name: 'Run', exact: true}));
        const running = page.getByRole('button', {name: 'Running…', exact: true});
        await running.waitFor();
        const actual = await running.evaluate(element => ({cursor: getComputedStyle(element).cursor, disabled: element.disabled || element.getAttribute('aria-disabled') === 'true'}));
        check(`${prefix}: real disabled SQL button is not pointer`, actual.disabled && actual.cursor !== 'pointer', JSON.stringify(actual));
      } finally {
        release();
        await page.unrouteAll({behavior: 'wait'});
      }
    } catch (error) {
      check(`${prefix}: regression flow completes`, false, error.stack);
    } finally {
      check(`${prefix}: zero CSP violations across picker and cursor checks`, violations.length === 0, JSON.stringify(violations));
      check(`${prefix}: zero console or page errors`, errors.length === 0, JSON.stringify(errors));
      await context.close();
    }
  }
};
