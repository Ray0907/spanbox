// Search result structure (mark, short UTC time, EmptyState) and the Traces custom range picker.
const {pick, field, shown, chooseCustom, dateInput, press} = require('./picker.cjs');
const shortTime = ns => new Date(ns / 1e6).toISOString().replace('T', ' ').slice(0, 19);

async function rowMatches(row, hit) {
  return await row.locator('.search-snippet').innerText() === hit.snippet.replace(/\[([^\[\]]+)\]/g, '$1')
    && await row.locator('mark').count() > 0
    && (await row.innerText()).includes(shortTime(hit.start_ns))
    && await row.locator('time').getAttribute('datetime') === new Date(hit.start_ns / 1e6).toISOString();
}
async function emptyState(page) {
  await page.getByRole('heading', {name: 'No matching spans', exact: true}).waitFor();
  await page.getByText('Try a shorter literal phrase.', {exact: true}).waitFor();
}

// Custom range: pick a date and time, apply, and read the URL and the table.
async function rangeFlow(page, kind) {
  const day = offset => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);
  const check = (name, passed, detail) => flowCheck(`picker (${kind}): ${name}`, passed, detail);
  await chooseCustom(page);
  await pick(page, 'From', day(-2), '10:30');
  await pick(page, 'To', day(-2), '10:45');
  await press(page, page.getByRole('button', {name: 'Apply', exact: true}));
  await page.getByText('0 shown', {exact: true}).waitFor();
  const query = new URL(page.url()).searchParams;
  check('non-zero minutes become RFC 3339 UTC in from/to', query.get('range') === 'custom' && query.get('from') === `${day(-2)}T10:30:00Z` && query.get('to') === `${day(-2)}T10:45:00Z`, page.url());
  check('empty window shows no trace rows', await page.locator('tbody a').count() === 0);
  check('focus is not lost after Apply', await page.evaluate(() => document.activeElement !== document.body));
  await pick(page, 'From', day(-1), '00:00');
  await pick(page, 'To', day(1), '23:59');
  await press(page, page.getByRole('button', {name: 'Apply', exact: true}));
  await page.getByText('2 shown', {exact: true}).waitFor();
  await page.reload();
  await page.getByText('2 shown', {exact: true}).waitFor();
  check('URL loads back into the same picker values', JSON.stringify(await shown(page, 'From')) === JSON.stringify({iso: day(-1), time: '00:00'}) && JSON.stringify(await shown(page, 'To')) === JSON.stringify({iso: day(1), time: '23:59'}));
  const width = await page.evaluate(() => ({viewport: innerWidth, html: document.documentElement.scrollWidth}));
  check('no horizontal overflow with the range open', width.html <= width.viewport, JSON.stringify(width));
}
let flowCheck = () => {};

module.exports = function ({base, token, check}) {
  flowCheck = check;
  const day = offset => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);
  return async function searchTime(page) {
    await page.goto(`${base}/search?q=weather`);
    await page.locator('.search-results article').first().waitFor();
    const text = await page.locator('.search-results').innerText();
    check('search time: a mark exists and no match text is wrapped in [brackets]', await page.locator('.search-results mark').count() > 0 && !/\[[^\]]+\]/.test(text), text.slice(0, 160));
    const phrase = await page.getByLabel('Phrase', {exact: true}).boundingBox();
    const button = await page.getByRole('button', {name: 'Search', exact: true}).boundingBox();
    check('search time: phrase field takes the width, button keeps its own', phrase.width > 200 && button.width < 150 && Math.abs(phrase.x + phrase.width - button.x) < 40, JSON.stringify({phrase: phrase.width, button: button.width}));
    check('search time: one divider between hits, header time at the end', await page.locator('.search-results li').count() === await page.locator('.search-results article').count());

    const times = await page.locator('.search-results article time').evaluateAll(nodes => nodes.map(node => {
      const box = node.parentElement, line = parseFloat(getComputedStyle(box).lineHeight) || 20;
      return {text: box.textContent.trim(), oneLine: box.getBoundingClientRect().height < line * 1.5};
    }));
    check('search time: every hit time names UTC and stays on one line at 390px', times.length > 0 && times.every(item => /^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d UTC$/.test(item.text) && item.oneLine), JSON.stringify(times.filter(item => !item.oneLine || !item.text.endsWith(' UTC')).slice(0, 3)));
    await page.goto(`${base}/traces`);
    await page.getByText('2 shown', {exact: true}).waitFor();
    await rangeFlow(page, 'touch');
    const browser = page.context().browser();
    const desktop = await browser.newContext({viewport: {width: 1440, height: 1000}, colorScheme: 'dark'});
    try {
      const other = await desktop.newPage();
      const problems = [];
      await other.addInitScript(() => document.addEventListener('securitypolicyviolation', event => (window.__csp = window.__csp || []).push(event.effectiveDirective)));
      other.on('console', message => { if (message.type() === 'error') problems.push(message.text()); });
      other.on('pageerror', error => problems.push(error.message));
      await other.goto(`${base}/login`);
      await other.locator('input[name="token"]').fill(token);
      await other.getByRole('button').click();
      await other.waitForURL(`${base}/traces`);
      await other.getByText('2 shown', {exact: true}).waitFor();
      await rangeFlow(other, 'mouse');
      // Keyboard: open with the keyboard, dismiss with Escape, focus stays in the field.
      await dateInput(other, 'From').focus();
      await other.keyboard.press('ArrowDown');
      const dialog = other.getByRole('dialog', {name: 'Choose date and time'});
      const opened = await dialog.waitFor({timeout: 3000}).then(() => true, () => false);
      if (opened) await other.keyboard.press('Escape');
      check('picker: keyboard opens the calendar and Escape dismisses it', opened && await dialog.count() === 0);
      check('picker: focus stays inside the From field after dismiss', await field(other, 'From').evaluate(box => box.contains(document.activeElement)));
      const csp = await other.evaluate(() => window.__csp || []);
      check('picker: no CSP violation, console error or runtime STYLE in a mouse browser', csp.length === 0 && problems.length === 0 && await other.evaluate(() => document.head.querySelectorAll('style').length) === 0, JSON.stringify({csp, problems}));
    } finally {
      await desktop.close();
    }
  };
};
module.exports.rowMatches = rowMatches;
module.exports.emptyState = emptyState;
