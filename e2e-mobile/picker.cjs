// Drives the Astryx DateTimeInput (popover presentation: calendar dialog for the date, typed 24-hour time).
const field = (page, name) => page.locator('main form label', {hasText: `${name} (UTC)`}).locator('..');
const monthIndex = iso => Number(iso.slice(0, 4)) * 12 + Number(iso.slice(5, 7));
const dateInput = (page, name) => page.getByLabel(`${name} (UTC)`, {exact: true});
const timeInput = (page, name) => page.getByLabel(`${name} (UTC) time`, {exact: true});

// Taps on a touch profile (a dialog that closes under the finger must not ghost-click what is beneath).
const press = async (page, locator) => await page.evaluate(() => navigator.maxTouchPoints > 0) ? locator.tap() : locator.click();

// Range is a Selector: open the menu, then tap or click the option, the way a user does.
const rangeTrigger = page => page.getByRole('combobox', {name: /^Range/});
async function chooseRange(page, label) {
  await press(page, rangeTrigger(page));
  await press(page, page.getByRole('option', {name: label, exact: true}));
  await page.waitForFunction(text => [...document.querySelectorAll('[role="combobox"]')].some(node => node.textContent.includes(text)), label);
}
async function chooseCustom(page) {
  await chooseRange(page, 'Custom');
  await dateInput(page, 'From').waitFor();
}

async function pick(page, name, iso, time) {
  await press(page, field(page, name).getByRole('button', {name: 'Open calendar'}));
  const dialog = page.getByRole('dialog', {name: 'Choose date and time'});
  await dialog.waitFor();
  const day = dialog.locator(`button[data-date="${iso}"]`);
  for (let step = 0; step < 14 && !await day.count(); step++) {
    const shown = await dialog.locator('[role="grid"]').getAttribute('aria-label');
    const forward = monthIndex(iso) > monthIndex(new Date(`${shown} 1 UTC`).toISOString().slice(0, 10));
    await press(page, dialog.getByRole('button', {name: forward ? 'Next month' : 'Previous month'}));
  }
  await press(page, day);
  await dialog.waitFor({state: 'hidden'});
  await timeInput(page, name).fill(time);
  await page.keyboard.press('Tab');
}

async function shown(page, name) {
  const date = await dateInput(page, name).inputValue();
  const time = await timeInput(page, name).inputValue();
  return {iso: /^\d{4}-/.test(date) ? date : new Date(`${date} UTC`).toISOString().slice(0, 10), time};
}
module.exports = {field, pick, shown, chooseCustom, chooseRange, rangeTrigger, dateInput, press};
