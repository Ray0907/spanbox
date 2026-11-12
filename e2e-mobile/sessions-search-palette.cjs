module.exports = async function (page, check) {
  for (const colorScheme of ['dark', 'light']) {
    await page.emulateMedia({colorScheme});
    // Link colors transition when the OS scheme changes; compare their settled colors.
    await page.waitForFunction(() => getComputedStyle(document.querySelector('main [role="status"]').firstElementChild).color === getComputedStyle(document.querySelector('.turn-card a')).color, null, {timeout: 2000}).catch(() => {});
    const state = await page.locator('main [role="status"]').evaluate(element => {
      const vars = getComputedStyle(element);
      const header = getComputedStyle(element.firstElementChild);
      const link = getComputedStyle(document.querySelector('.turn-card a'));
      return {sameBackground: vars.getPropertyValue('--color-info-muted') === vars.getPropertyValue('--color-accent-muted'),
        sameForeground: vars.getPropertyValue('--color-on-info-muted') === vars.getPropertyValue('--color-text-accent'),
        actualForeground: header.color === link.color};
    });
    check(`session: ${colorScheme} truncation Banner uses only spanbox colors`, state.sameBackground && state.sameForeground && state.actualForeground, JSON.stringify(state));
  }
  await page.emulateMedia({colorScheme: 'dark'});
};
