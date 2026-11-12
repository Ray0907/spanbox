const path = require('node:path');

module.exports = async function (page, check) {
  const original = page.url();
  const measure = () => page.locator('.turn-card').evaluateAll(cards => cards.map(card => {
    const title = card.querySelector('h2');
    const time = card.querySelector('time');
    const badge = [...card.querySelectorAll('span')].find(node => node.textContent === 'error');
    const rect = node => {
      const {left, right, top, bottom, width, height} = node.getBoundingClientRect();
      return {left, right, top, bottom, width, height};
    };
    return {title: rect(title), time: rect(time), badge: badge ? rect(badge) : null,
      card: rect(card), timeText: time.textContent,
      readingOrder: Boolean(title.compareDocumentPosition(time) & Node.DOCUMENT_POSITION_FOLLOWING) && (!badge || Boolean(time.compareDocumentPosition(badge) & Node.DOCUMENT_POSITION_FOLLOWING))};
  }));
  const overlaps = (a, b) => a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
  try {
    for (const long of [false, true]) {
      await page.goto(long ? new URL('/sessions?id=parity-layout-long', original).href : original);
      await page.locator('.turn-card').nth(long ? 1 : 3).waitFor();
      await page.evaluate(() => document.fonts.ready);
      for (const [width, height] of [[1440, 900], [390, 844]]) {
        await page.setViewportSize({width, height});
        const cards = await measure();
        const label = `session turn layout: ${long ? 'long' : 'normal'} ${width}`;
        check(`${label} seeded error and ok turns`, cards.some(card => card.badge) && cards.some(card => !card.badge));
        const edges = cards.map(card => card.time.right);
        check(`${label} all timestamp right edges within 2px`, Math.max(...edges) - Math.min(...edges) <= 2, JSON.stringify(cards));
        check(`${label} title, timestamp and badge do not overlap`, cards.every(card => !overlaps(card.title, card.time) && (!card.badge || (!overlaps(card.badge, card.time) && !overlaps(card.badge, card.title)))));
        check(`${label} no card or page overflow`, await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth) && cards.every(card => [card.title, card.time, card.badge].filter(Boolean).every(rect => rect.left >= card.card.left && rect.right <= card.card.right && rect.width > 0 && rect.height > 0)));
        check(`${label} timestamp stays at card right inset`, cards.every(card => Math.abs(card.card.right - card.time.right - (cards[0].card.right - cards[0].time.right)) <= 2 && card.card.right - card.time.right < 30));
        check(`${label} ok cards retain original 12px insets`, cards.filter(card => !card.badge).every(card => Math.abs(card.card.right - card.time.right - 12) <= 2 && Math.abs(card.title.left - card.card.left - 12) <= 2));
        const snapshots = await Promise.all(cards.map((card, i) => page.locator('.turn-card h2').nth(i).locator('..').ariaSnapshot()));
        check(`${label} reading order stays title, timestamp, badge`, cards.every((card, i) => card.readingOrder && snapshots[i].indexOf('heading') < snapshots[i].indexOf(card.timeText) && (!card.badge || snapshots[i].indexOf(card.timeText) < snapshots[i].indexOf('error'))), JSON.stringify(snapshots));
        if (!long) {
          await page.locator('.turn-card').filter({has: page.getByText('error', {exact: true})}).scrollIntoViewIfNeeded();
          await page.screenshot({path: path.resolve(__dirname, `../tmp/b8-shot-${width}.png`), scale: 'css'});
        }
      }
    }
  } finally {
    await page.setViewportSize({width: 390, height: 844});
    await page.goto(original);
    await page.locator('.turn-card').nth(3).waitFor();
  }
};
