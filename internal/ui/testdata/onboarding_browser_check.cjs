const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [origin, phase, widthText, work, shots, token] = process.argv.slice(2);
const width = Number(widthText), errors = [], external = [], requests = [], apiReads = [], layouts = [];

(async () => {
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 900 } });
    await context.addCookies([{ name: 'agentnet_ui', value: token, url: origin }]);
    await context.route('**/*', r => {
      if (r.request().url().startsWith(origin + '/')) return r.continue();
      external.push(r.request().url());
      return r.abort();
    });
    const page = await context.newPage();
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', m => { if (['error', 'warning'].includes(m.type())) errors.push(m.type() + ': ' + m.text()); });
    page.on('request', r => {
      const url = new URL(r.url()).pathname;
      if (r.method() === 'POST') requests.push({ url, body: r.postDataJSON() });
      else if (url.startsWith('/api/')) apiReads.push(url);
    });
    const open = async (browserMode = false) => {
      await page.goto(origin + '/fixture' + (browserMode ? '?browser=1' : ''));
      await page.locator('#profile-btn').click();
      await page.getByRole('tab', { name: 'Agent', exact: true }).click();
      const text = browserMode ? /This browser runs nothing/ : phase === 'fresh' ? /Nothing chosen yet/ : phase === 'manual' ? /No automatic responder/ : /codex answers approved questions/;
      await page.locator('#responder').getByText(text).waitFor();
    };
    const read = () => page.evaluate(async () => (await fetch('/api/responder')).json());
    const capture = async name => {
      await page.locator('#responder').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `onboarding-${name}-${width}.png`) });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      const overflow = await page.locator('#settings').evaluate(d => ({
        scroll: d.scrollWidth, client: d.clientWidth,
        wide: [...d.querySelectorAll('*')].filter(n => n.scrollWidth > n.clientWidth + 1 && getComputedStyle(n).overflowX === 'visible')
          .map(n => ({ tag: n.tagName, id: n.id, class: n.className, scroll: n.scrollWidth, client: n.clientWidth }))
      }));
      assert(overflow.scroll <= overflow.client + 1, JSON.stringify(overflow));
      layouts.push({ name, scroll: overflow.scroll, client: overflow.client });
    };
    await open();
    assert.equal(requests.length, 0, 'opening settings must not choose a responder');
    assert((await page.locator('#settings').innerText()).includes('not logged in or working'));
    if (phase === 'fresh') {
      const before = await read();
      assert.equal(before.chosen, false);
      assert.equal(before.manual, false);
      assert.equal(await page.locator('#responder input:checked').count(), 0);
      assert.equal(await page.locator('#responder input[value="codex"]').isEnabled(), true);
      assert.equal(await page.locator('#responder input[value="pi"]').isDisabled(), true);
      await capture('not-chosen');
      await page.locator('#responder input[value="manual"]').check();
      await page.locator('#responder').getByText('No automatic responder: everything waits for you.', { exact: true }).waitFor();
      const after = await read();
      assert.equal(after.chosen, true);
      assert.equal(after.manual, true);
      assert.deepEqual(requests.filter(r => r.url === '/api/responder').map(r => r.body), [{ manual: true }]);
      await capture('manual');
    } else if (phase === 'manual') {
      const before = await read();
      assert.equal(before.chosen, true);
      assert.equal(before.manual, true);
      assert.equal(await page.locator('#responder input[value="manual"]').isChecked(), true);
      await capture('manual-reopened');
      await page.locator('#responder input[value="codex"]').check();
      await page.locator('#responder-dir').fill(work);
      await page.locator('#responder').getByRole('button', { name: 'Save', exact: true }).click();
      await page.locator('#responder').getByText(/codex answers approved questions/).waitFor();
      const after = await read();
      assert.equal(after.chosen, true);
      assert.equal(after.manual, false);
      assert.equal(after.harness, 'codex');
      assert.equal(after.dir, work);
      assert.equal(after.ready, true);
      assert.deepEqual(requests.filter(r => r.url === '/api/responder').map(r => r.body), [{ harness: 'codex', dir: work }]);
      await capture('selected');
    } else {
      const before = await read();
      assert.equal(before.harness, 'codex');
      assert.equal(before.dir, work);
      assert.equal(before.ready, true);
      assert.equal(await page.locator('#responder input[value="codex"]').isChecked(), true);
      await capture('selected-reopened');
      const refused = await page.evaluate(async work => {
        const r = await fetch('/api/responder', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ harness: 'pi', dir: work }) });
        return { status: r.status, text: await r.text() };
      }, work);
      assert.equal(refused.status, 409);
      assert(refused.text.includes('PATH') && refused.text.includes('unchanged'));
      assert.deepEqual(await read(), before);
      await page.locator('#settings-close').click();
      await page.locator('#profile-btn').click();
      await page.getByRole('tab', { name: 'Agent', exact: true }).click();
      await page.locator('#responder').getByText(/codex answers approved questions/).waitFor();
      assert.equal(await page.locator('#responder input[value="codex"]').isChecked(), true);
      await capture('missing-refused');
      const nativePosts = requests.length, nativeReads = apiReads.length;
      await open(true);
      assert.equal(await page.locator('#responder input').count(), 0);
      assert.equal(requests.length, nativePosts);
      assert(!apiReads.slice(nativeReads).some(p => p === '/api/responder' || p === '/api/agents'));
      assert.equal(await page.getByRole('button', { name: 'Create agent…' }).count(), 0);
      await capture('browser-no-local');
    }
    // The deliberate refusal produces Chrome's expected HTTP error notice.
    const expectedHTTP = phase === 'selected' ? errors.filter(e => e.includes('409 (Conflict)')) : [];
    assert.deepEqual(errors.filter(e => !expectedHTTP.includes(e)), []);
    assert.deepEqual(external, []);
    console.log(JSON.stringify({ pass: true, width, phase, requests, layouts, errors, expectedHTTP, external }));
  } finally {
    if (browser) await browser.close();
  }
})().catch(e => { console.error(e); process.exitCode = 1; });
