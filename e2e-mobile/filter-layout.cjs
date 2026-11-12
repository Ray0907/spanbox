// Traces and Sessions filters: Range on its own line, From and To below it, the other filters and Apply last.
// Reading and Tab order follow the same sequence; nothing overflows.
const {rangeTrigger} = require('./picker.cjs');
const rect = async locator => await locator.boundingBox();
const day = '2026-01-01T00:00:00Z', end = '2026-01-02T00:00:00Z';
const pages = [
  {path: '/traces', others: ['Model', 'Service', 'Min duration (ms)', 'Session', 'User', 'Errors only']},
  {path: '/sessions', others: []},
];
const tabOrder = ['Range', 'From (UTC)', 'To (UTC)', 'Model', 'Service', 'Min duration (ms)', 'Session', 'User', 'Errors only', 'Apply'];

async function run(page, base, check, kind, width, height) {
  await page.setViewportSize({width, height});
  for (const {path, others} of pages) {
    const name = `filter layout ${kind} ${path} ${width}px`;
    await page.goto(`${base}${path}?range=custom&from=${day}&to=${end}`);
    await page.getByLabel('From (UTC)', {exact: true}).waitFor();
    const range = await rect(rangeTrigger(page));
    const from = await rect(page.getByLabel('From (UTC)', {exact: true}));
    const to = await rect(page.getByLabel('To (UTC)', {exact: true}));
    const apply = await rect(page.getByRole('button', {name: 'Apply', exact: true}));
    const fields = [];
    for (const label of others) fields.push(await rect(page.getByLabel(label, {exact: true})));
    check(`${name}: Range sits alone on its own line above From and To`, range.y + range.height <= from.y && range.y + range.height <= to.y, JSON.stringify({range, from, to}));
    const boundsBottom = Math.max(from.y + from.height, to.y + to.height);
    check(`${name}: the other filters and Apply sit below From and To`, [...fields, apply].every(box => box.y + box.height > boundsBottom - 1 && box.y >= from.y + from.height - 1), JSON.stringify({boundsBottom, apply}));
    if (width >= 1000) check(`${name}: From and To share one line`, Math.abs(from.y - to.y) < 2, JSON.stringify({from, to}));
    if (width >= 1000 && fields.length) {
      const inputs = fields.slice(0, 5);
      check(`${name}: the text filters share one baseline`, inputs.every(box => Math.abs(box.y + box.height - (inputs[0].y + inputs[0].height)) < 2) && Math.abs(apply.y + apply.height - (inputs[0].y + inputs[0].height)) < 8, JSON.stringify({inputs, apply}));
    }
    const overflow = await page.evaluate(() => ({viewport: innerWidth, html: document.documentElement.scrollWidth}));
    const outside = [range, from, to, apply, ...fields].filter(box => box.x < 0 || box.x + box.width > width);
    check(`${name}: nothing overflows horizontally`, overflow.html <= width && outside.length === 0, JSON.stringify({overflow, outside}));
    // Tab from the Range control through the form; the first time each label is reached must follow the visual order.
    await rangeTrigger(page).focus();
    const seen = [];
    for (let step = 0; step < 30; step++) {
      await page.keyboard.press('Tab');
      const label = await page.evaluate(() => {
        const el = document.activeElement;
        return (el.labels?.[0]?.textContent || el.getAttribute('aria-label') || el.textContent || '').trim();
      });
      seen.push(label);
      if (label === 'Apply') break;
    }
    const order = ['Range', ...tabOrder.slice(1)].filter(label => label !== 'Range' && (label === 'From (UTC)' || label === 'To (UTC)' || label === 'Apply' || others.includes(label)));
    const first = order.map(label => seen.findIndex(item => item.startsWith(label)));
    check(`${name}: Tab order is From, To, ${others.length ? 'the other filters, ' : ''}Apply`, first.every((index, i) => index >= 0 && (i === 0 || index > first[i - 1])), JSON.stringify(seen));
  }
}

module.exports = function ({base, token, check}) {
  return async function filterLayout(page) {
    const original = page.viewportSize();
    await run(page, base, check, 'touch', 390, 844);
    await page.setViewportSize(original);
    const desktop = await page.context().browser().newContext({viewport: {width: 1440, height: 1000}, colorScheme: 'dark'});
    try {
      const other = await desktop.newPage();
      await other.goto(`${base}/login`);
      await other.locator('input[name="token"]').fill(token);
      await other.getByRole('button').click();
      await other.waitForURL(`${base}/traces`);
      await run(other, base, check, 'mouse', 1440, 1000);
    } finally {
      await desktop.close();
    }
  };
};
