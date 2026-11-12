const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {execFileSync} = require('node:child_process');

module.exports = function ({root, base, check}) {
  function trackStyles() {
    window.__headStyles = [];
    window.__cspViolations = [];
    document.addEventListener('securitypolicyviolation', event => window.__cspViolations.push(event.effectiveDirective));
    new MutationObserver(records => {
      for (const record of records) for (const node of record.addedNodes) {
        if (node.nodeType !== Node.ELEMENT_NODE) continue;
        if (node.matches('head style') || node.querySelector('head style')) window.__headStyles.push(node.tagName);
      }
    }).observe(document, {childList: true, subtree: true});
  }

  async function scratchLayerSmoke(browser, temporary) {
    const directory = path.join(temporary, 'csp-smoke');
    execFileSync('node', ['web-ui/tests/build-csp-smoke.mjs', directory], {cwd: root, stdio: 'inherit'});
    const dist = path.join(directory, 'dist');
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      const scratchErrors = [];
      page.on('console', message => { if (message.type() === 'error') scratchErrors.push(message.text()); });
      page.on('pageerror', error => scratchErrors.push(error.message));
      await page.addInitScript(trackStyles);
      const shell = await page.request.get(`${base}/ui`);
      const csp = shell.headers()['content-security-policy'];
      await page.route(`${base}/ui/csp-smoke`, route => route.fulfill({
        status: 200, headers: {'content-type': 'text/html', 'content-security-policy': csp},
        body: fs.readFileSync(path.join(dist, 'index.html'))
      }));
      const prefix = '/static/ui/csp-smoke/';
      await page.route(`${base}${prefix}**`, route => {
        const file = path.resolve(dist, new URL(route.request().url()).pathname.slice(prefix.length));
        assert.ok(file.startsWith(dist + path.sep), 'scratch assets stay inside their temporary directory');
        const contentType = {'.css': 'text/css', '.js': 'text/javascript', '.woff2': 'font/woff2'}[path.extname(file)] || 'application/octet-stream';
        return route.fulfill({status: 200, contentType, body: fs.readFileSync(file)});
      });
      await page.goto(`${base}/ui/csp-smoke`);
      await page.getByRole('button', {name: 'Open tooltip', exact: true}).focus();
      await page.getByRole('tooltip').waitFor();
      check('scratch CSP: Tooltip opens under the real server CSP', (await page.getByRole('tooltip').innerText()).includes('CSP tooltip smoke'));
      await page.getByRole('combobox', {name: 'Smoke selector', exact: true}).click();
      await page.getByRole('option', {name: 'Two', exact: true}).click();
      check('scratch CSP: Selector opens and selects an option', (await page.getByRole('combobox', {name: 'Smoke selector', exact: true}).innerText()).includes('Two'));
      const state = await page.evaluate(() => ({
        tag: document.getElementById('astryx-layer-slide-rules')?.tagName,
        added: window.__headStyles, remaining: document.head.querySelectorAll('style').length,
        violations: window.__cspViolations
      }));
      check('scratch CSP: external LINK survives Tooltip and Selector', state.tag === 'LINK');
      check('scratch CSP: no runtime STYLE or CSP violation', state.added.length === 0 && state.remaining === 0 && state.violations.length === 0, JSON.stringify(state));
      check('scratch CSP: zero console errors or page errors', scratchErrors.length === 0, JSON.stringify(scratchErrors));
    } finally {
      await context.close();
    }
  }

  return {trackStyles, scratchLayerSmoke};
};
