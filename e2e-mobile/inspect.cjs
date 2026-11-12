const assert = require('node:assert/strict');
const path = require('node:path');

module.exports = function (check) {
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

  return {inspect, rowTap};
};
