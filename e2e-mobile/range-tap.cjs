// Choosing a Range option (Selector) by tap or click selects that option, keeps one row, and never overflows.
// Traces and Sessions: 6 options, kept as a draft until Apply. Dashboard: 4 options, applied on change.
const pages = [
  {path: '/traces', labels: ['All retained', '15 minutes', '1 hour', '24 hours', '7 days', 'Custom']},
  {path: '/sessions', labels: ['All retained', '15 minutes', '1 hour', '24 hours', '7 days', 'Custom']},
  {path: '/dashboard', labels: ['15 minutes', '1 hour', '24 hours', '7 days']},
];
const trigger = page => page.getByRole('combobox', {name: /^Range/});
const press = async (page, locator) => await page.evaluate(() => navigator.maxTouchPoints > 0) ? locator.tap() : locator.click();

async function run(page, base, check, kind, widths, height) {
  // Strict CSP: the menu must open without a blocked style or an injected <style>.
  await page.addInitScript(() => {
    window.__rangeCsp = [];
    document.addEventListener('securitypolicyviolation', event => window.__rangeCsp.push(event.effectiveDirective));
  });
  for (const {path, labels} of pages) {
    for (const width of widths) {
      const name = `range ${kind} ${path} ${width}px`;
      await page.setViewportSize({width, height});
      await page.goto(base + path);
      await page.locator('main').waitFor();
      const found = await trigger(page).count();
      check(`${name}: Range is one Selector`, found === 1, `${found} comboboxes named Range`);
      if (found !== 1) continue;
      const box = await trigger(page).boundingBox();
      const rowHeight = await trigger(page).evaluate(node => parseFloat(getComputedStyle(node).lineHeight) || 20);
      check(`${name}: control stays on one row inside the viewport`, box.height < rowHeight * 2.6 && box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));
      // Start from a different option each time so a tap on the already selected one cannot hide a misfire.
      const order = [...labels.slice(1), labels[0], ...labels.slice(1)];
      const wrong = [], off = [];
      for (const label of order) {
        await press(page, trigger(page));
        const option = page.getByRole('option', {name: label, exact: true});
        await option.waitFor({timeout: 3000}).catch(() => {});
        const rect = await option.boundingBox();
        if (!rect || rect.x < 0 || rect.y < 0 || rect.x + rect.width > width || rect.y + rect.height > height) off.push(`${label} ${JSON.stringify(rect)}`);
        if (rect) await press(page, option);
        await page.waitForTimeout(500);
        const text = (await trigger(page).innerText()).trim();
        if (!text.includes(label)) wrong.push(`${label} -> ${text}`);
      }
      check(`${name}: every option is inside the viewport when the menu is open`, off.length === 0, off.join('; '));
      check(`${name}: choosing an option selects it 500 ms later`, wrong.length === 0, wrong.join('; '));
      await press(page, trigger(page));
      await page.getByRole('option').first().waitFor();
      const styled = await page.evaluate(() => {
        const list = document.querySelector('[role="listbox"]');
        let node = list, background = 'rgba(0, 0, 0, 0)';
        for (let depth = 0; node && depth < 4 && background === 'rgba(0, 0, 0, 0)'; depth++, node = node.parentElement) background = getComputedStyle(node).backgroundColor;
        return {background, optionHeight: document.querySelector('[role="option"]').getBoundingClientRect().height, violations: window.__rangeCsp, styles: document.head.querySelectorAll('style').length};
      });
      check(`${name}: menu is styled and the strict CSP blocks nothing`, styled.background !== 'rgba(0, 0, 0, 0)' && styled.optionHeight >= 32 && styled.violations.length === 0 && styled.styles === 0, JSON.stringify(styled));
      await page.keyboard.press('Escape');
      const overflow = await page.evaluate(() => ({viewport: innerWidth, html: document.documentElement.scrollWidth, body: document.body.scrollWidth}));
      check(`${name}: no horizontal overflow`, overflow.html <= width && overflow.body <= width, JSON.stringify(overflow));
    }
  }
}

module.exports = function ({base, token, check}) {
  return async function rangeTap(page) {
    const original = page.viewportSize();
    await run(page, base, check, 'touch', [320, 360, 390, 430], 844);
    await page.setViewportSize(original);
    const desktop = await page.context().browser().newContext({viewport: {width: 1440, height: 1000}, colorScheme: 'dark'});
    try {
      const other = await desktop.newPage();
      await other.goto(`${base}/login`);
      await other.locator('input[name="token"]').fill(token);
      await other.getByRole('button').click();
      await other.waitForURL(`${base}/traces`);
      await run(other, base, check, 'mouse', [1440], 1000);
    } finally {
      await desktop.close();
    }
  };
};
