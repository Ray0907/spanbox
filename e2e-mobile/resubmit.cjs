// Pressing Search/Apply again with an unchanged query must refetch (retry after a failure, pick up new data).
module.exports = function ({base, check}) {
  return async function resubmit(page) {
    for (const [path, button, api] of [['/search?q=weather', 'Search', '/search?'], ['/sessions', 'Apply', '/sessions?']]) {
      await page.goto(base + path);
      await page.locator('main').waitFor();
      await page.waitForLoadState('networkidle');
      let calls = 0;
      const count = request => { if (request.url().includes(api) && request.url().includes('format=json')) calls++; };
      page.on('request', count);
      await page.getByRole('button', {name: button, exact: true}).click();
      await page.waitForLoadState('networkidle');
      page.off('request', count);
      check(`resubmit: ${button} on the same query refetches`, calls > 0, `requests=${calls}`);
    }
    // Clicking the active nav link on Dashboard must refetch without remounting the page (keeps Range focus).
    await page.goto(base + '/dashboard');
    await page.locator('main').waitFor();
    await page.waitForLoadState('networkidle');
    let dashboardCalls = 0;
    const countDashboard = request => { if (request.url().includes('/dashboard/data')) dashboardCalls++; };
    page.on('request', countDashboard);
    await page.getByRole('link', {name: 'Dashboard', exact: true}).click();
    await page.waitForLoadState('networkidle');
    page.off('request', countDashboard);
    check('resubmit: Dashboard nav link on the same URL refetches', dashboardCalls > 0, `requests=${dashboardCalls}`);
  };
};
