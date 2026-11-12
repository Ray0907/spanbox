// Selecting a span with the keyboard must keep focus on the span button (WCAG 2.4.3).
module.exports = function ({base, check}) {
  return async function spanFocus(page) {
    const items = (await (await page.request.get(base + '/?format=json&session=mobile-session-1')).json()).items;
    const trace = items[0].trace_id;
    await page.goto(`${base}/traces/${trace}`);
    const target = page.getByRole('button', {name: /chat |lookup-weather|execute_tool|retrieval/}).last();
    await target.waitFor();
    await target.focus();
    await page.keyboard.press('Enter');
    await page.waitForURL('**/spans/**');
    await page.waitForTimeout(300);
    const state = await page.evaluate(() => { const a = document.activeElement; return {tag: a.tagName, current: a.getAttribute('aria-current')}; });
    check('trace: keyboard span selection keeps focus on the span button', state.tag === 'BUTTON' && state.current !== null, JSON.stringify(state));
  };
};
