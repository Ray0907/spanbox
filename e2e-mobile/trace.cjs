module.exports = function ({base, check}) {
  return async function traceParity(page) {
    const spans = page.locator('.span-tree button[data-span-id]');
    check('trace-detail: every span is a button', await spans.count() === 3);
    check('trace-detail: light and dark color-scheme', await page.evaluate(() => ['light', 'dark'].every(scheme => getComputedStyle(document.documentElement).colorScheme.includes(scheme))));
    const collapse = page.getByRole('button', {name: 'Children of agent-root', exact: true});
    await collapse.tap();
    check('trace-detail: child branches can collapse', !await spans.nth(1).isVisible());
    await collapse.tap();
    await spans.first().focus();
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => document.querySelector('.span-tree button[data-span-id]')?.getAttribute('aria-current') === 'true');
    await page.locator('#panel h2').filter({hasText: 'agent-root'}).waitFor();
    const traceID = new URL(page.url()).pathname.split('/')[2];
    const rootID = await spans.first().getAttribute('data-span-id');
    const rootJSON = await (await page.request.get(`${base}/spans/${traceID}/${rootID}?format=json`)).json();
    const rootPanel = await page.locator('#panel').innerText();
    check('trace-detail: conditional status message matches JSON', rootJSON.status_message ? rootPanel.includes(rootJSON.status_message) : !rootPanel.includes('Status message'));
    for (let index = 0; index < 10; index++) {
      await page.keyboard.press('Tab');
      if (await spans.nth(1).evaluate(button => button === document.activeElement)) break;
    }
    check('trace-detail: keyboard Tab reaches next span', await spans.nth(1).evaluate(button => button === document.activeElement));
    await page.keyboard.press('Space');
    await page.waitForFunction(() => document.querySelectorAll('.span-tree button[data-span-id]')[1]?.getAttribute('aria-current') === 'true');
    await page.locator('#panel h2').filter({hasText: 'chat gpt-4o'}).waitFor();
    check('trace-detail: keyboard Space selects exactly one current span', await page.locator('.span-tree button[data-span-id][aria-current="true"]').count() === 1);
    for (let index = 0; index < 10; index++) {
      await page.keyboard.press('Tab');
      if (await spans.nth(2).evaluate(button => button === document.activeElement)) break;
    }
    check('trace-detail: keyboard reaches the tool span', await spans.nth(2).evaluate(button => button === document.activeElement));
    await page.keyboard.press('Enter');
    await page.locator('#panel h2').filter({hasText: 'lookup-weather'}).waitFor();
    check('trace-detail: keyboard selects the tool span', await spans.nth(2).getAttribute('aria-current') === 'true' && await page.locator('.span-tree button[data-span-id][aria-current="true"]').count() === 1);
    await spans.nth(1).tap();
    await page.locator('#panel h2').filter({hasText: 'chat gpt-4o'}).waitFor();
    check('trace-detail: visible buttons have 44px tap targets', await page.locator('main button').evaluateAll(buttons => buttons.map(button => button.getBoundingClientRect()).filter(rect => rect.width && rect.height).every(rect => rect.width >= 44 && rect.height >= 44)));
    const tree = await page.locator('.span-tree').innerText();
    check('trace-detail: names, kind, models and tokens preserved', ['agent-root', 'chat gpt-4o', 'lookup-weather', 'LLM', 'gpt-4o', '100 / 20'].every(text => tree.includes(text)));
    check('trace-detail: exact waterfall geometry and ruler', await page.locator('.span-tree svg rect').evaluateAll(rects => rects.length === 3 && rects[0].getAttribute('x') === '0' && rects[0].getAttribute('width') === '100' && rects[1].getAttribute('x') === '17' && rects[1].getAttribute('width') === '67') && (await page.locator('.time-ruler').innerText()).includes('300ms'));
    const metadata = await page.locator('#panel').innerText();
    check('trace-detail: complete metadata', ['Model', 'Provider', 'Tokens', '100 in / 20 out', 'Cost', 'Duration', 'Start offset', '+50.0 ms', 'Status', 'OK', '2122232425262728'].every(text => metadata.includes(text)));
    check('trace-detail: Traces nav current on span deep link', await page.locator('nav[aria-label="Primary"] a[aria-current="page"]').innerText() === 'Traces');
    for (const field of ['input', 'output', 'attributes', 'events', 'links', 'resource', 'scope']) {
      const label = field[0].toUpperCase() + field.slice(1);
      const region = page.getByRole('region', {name: label, exact: true});
      const toggle = region.getByRole('button', {name: label, exact: true});
      if (await toggle.count() && await toggle.getAttribute('aria-expanded') !== 'true') await toggle.tap();
      check(`trace-detail: ${label} exposes paging`, await region.getByRole('button', {name: /Show more/}).count() === 1);
      let pages = 0;
      while (await region.getByRole('button', {name: /Show more/}).count()) {
        const expectedOffset = await region.getAttribute('data-next-offset');
        const response = page.waitForResponse(response => new URL(response.url()).searchParams.get('field') === field);
        await region.getByRole('button', {name: /Show more/}).tap();
        const next = await response;
        const query = new URL(next.url()).searchParams;
        const window = (await next.json())[field];
        check(`trace-detail: ${label} window ${++pages} uses next_offset`, query.get('format') === 'json' && query.get('offset') === expectedOffset && query.get('len') === '2000');
        await page.waitForFunction(({field, offset}) => document.querySelector(`section[data-field="${field}"]`)?.dataset.nextOffset === offset, {field, offset: String(window.next_offset ?? 'done')});
        if (pages > 20) throw new Error('Unbounded span paging');
      }
      const reference = (await (await page.request.get(`${page.url()}?format=json&field=${field}&len=20000`)).json())[field];
      let expected = reference.text;
      if (field === 'input' || field === 'output') {
        let messages = JSON.parse(reference.text);
        if (!Array.isArray(messages)) messages = messages.messages;
        if (typeof messages === 'string') messages = JSON.parse(messages);
        expected = messages[0].content;
      }
      const actual = (await region.locator('code').allTextContents()).join('');
      check(`trace-detail: ${label} exact complete Unicode text`, reference.next_offset === null && actual === expected && actual.includes(`END-${field}`));
    }
    check('trace-detail: complete long telemetry has no 390px overflow', await page.evaluate(() => innerWidth === 390 && document.documentElement.scrollWidth <= 390 && document.body.scrollWidth <= 390));
    const deepLink = page.url();
    await page.reload();
    await page.locator('.span-tree button[data-span-id][aria-current="true"]').waitFor();
    check('trace-detail: deep link reload selects requested span', page.url() === deepLink && (await page.locator('.span-tree button[data-span-id][aria-current="true"]').innerText()).includes('chat gpt-4o'));
    await page.goto(`${base}/ui#/t/${traceID}`);
    await page.waitForURL(`${base}/traces/${traceID}`);
    await page.locator('.span-tree button[data-span-id][aria-current="true"]').waitFor();
    check('trace-detail: old hash link renders the trace', (await page.locator('h1').innerText()).includes('agent-root'));
    check('trace-detail: Traces nav current on trace route', await page.locator('nav[aria-label="Primary"] a[aria-current="page"]').innerText() === 'Traces');
  };
};
