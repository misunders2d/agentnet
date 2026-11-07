// Rendered Cancel of a queued send through the real daemon page (loader.js)
// and the built Comic bundle. Only the queued-send host endpoints are stubbed:
// /api/dm/send answers "queued", the conversation shows that queued row, and
// /api/schedule/discard_send answers the exact cancelled ID.
//   node cancel_restore_rendered_check.cjs <page URL with token>
// CANCEL_WIDTH=1440|390; CANCEL_EVIDENCE=<dir> receives timings and screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const width = Number(process.env.CANCEL_WIDTH || 1440), out = process.env.CANCEL_EVIDENCE;
const original = '  **Original** text to keep\nsecond line ';
const results = {width, cases: []};

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
    const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width, height: width < 600 ? 844 : 900}, hasTouch: width < 600});
    const origin = new URL(process.argv[2]).origin;
    let queued = null, cancels = 0, sends = 0;
    await ctx.route('**/api/dm/send', async route => {
      const body = route.request().postDataJSON(); sends++;
      queued = {id: body.id, lid: body.id, dir: 'out', kind: 'message', body: body.body, at: new Date().toISOString(), state: 'queued', queued_send: {state: 'pending', rev: 1}, can: [], actions: [], reactions: []};
      await route.fulfill({json: {id: body.id, lid: body.id, state: 'queued'}});
    });
    await ctx.route('**/api/dm?*', async route => {
      const response = await route.fetch(), view = await response.json();
      if (queued) view.messages = [...(view.messages || []), {...queued, from: view.me?.address || queued.from}];
      await route.fulfill({response, json: view});
    });
    await ctx.route('**/api/schedule/discard_send', async route => {
      const body = route.request().postDataJSON(); cancels++;
      assert.equal(body.id, queued?.id, 'Cancel names the exact queued ID'); queued = null;
      await route.fulfill({json: {id: body.id, state: 'cancelled'}});
    });
    const page = await ctx.newPage(); page.setDefaultTimeout(8000);
    results.requests = []; page.on('request', r => { if (r.url().includes('/api/') && !r.url().includes('/api/events')) results.requests.push(r.method() + ' ' + new URL(r.url()).pathname); });
    results.page = page;
    const errors = []; page.on('pageerror', e => errors.push(String(e)));
    await page.goto(process.argv[2]); await page.goto(origin + '/?skin=comic');
    const lock = await page.evaluate(() => window.__agentnetSingleWindow);
    assert.equal(lock, 'lock', 'this page holds the one-window lock');
    const chat = page.locator('[data-part="chat-row"][data-kind="person"]').filter({hasNotText: 'Device conversation'}).first(); await chat.click();
    const form = page.getByRole('form', {name: 'Write a message'}), box = form.getByRole('textbox').first();
    const send = async () => {
      await box.fill(original);
      const t0 = Date.now(); await form.locator('button[type="submit"]').click();
      await page.locator('[data-send-discard]').first().waitFor();
      return Date.now() - t0;
    };
    // Poll the composer through Playwright (it pierces the skin's open shadow root).
    const value = async (b, t) => { const end = Date.now() + 5000; while (Date.now() < end) { if (await b.inputValue().catch(() => null) === t) return; await new Promise(r => setTimeout(r, 10)); } throw new Error('composer never showed ' + JSON.stringify(t)); };
    const cancel = async () => {
      const t0 = Date.now(); await page.locator('[data-send-discard]').first().click();
      await value(box, original);
      return Date.now() - t0;
    };

    // 1. Send then Cancel: the exact original returns, with no second send.
    const sendMs = await send(); assert.equal(await box.inputValue().catch(() => ''), '', 'composer cleared as the send begins');
    const cancelMs = await cancel();
    assert.equal(sends, 1); assert.equal(cancels, 1);
    results.cases.push({case: 'send-cancel-restore', send_to_queued_row_ms: sendMs, cancel_to_restored_text_ms: cancelMs});
    assert(sendMs < 1000 && cancelMs < 1000, 'local feedback under one second: ' + sendMs + '/' + cancelMs);
    if (out) await page.screenshot({path: out + '/restored-' + width + '.png'});

    // 2. Storage refuses the restoration: the text still shows here; after a
    //    reload it is in Saved messages and Put back fills the empty composer.
    await box.fill(''); await send();
    await page.evaluate(() => { const set = Storage.prototype.setItem; window.__restoreSet = set;
      Storage.prototype.setItem = function (k, v) { if (/\.drafts\.[^.]*$/.test(k)) throw new DOMException('synthetic quota', 'QuotaExceededError'); return set.call(this, k, v); }; });
    await cancel();
    await page.evaluate(() => { Storage.prototype.setItem = window.__restoreSet; });
    await page.reload(); await page.locator('[data-part="chat-row"][data-kind="person"]').filter({hasNotText: 'Device conversation'}).first().click();
    await box.waitFor(); assert.equal(await box.inputValue().catch(() => ''), '', 'storage held the cleared draft');
    // A phone's open chat is a card over the tabs; each carries the banner, the card's is the one on top.
    const review = (width < 600 ? page.locator('[data-card]').last() : page).getByRole('button', {name: /^Saved messages \(1\)/}); await review.click();
    const dialog = page.getByRole('dialog', {name: 'Saved messages'});
    await dialog.getByText('Text kept from a message that wasn’t sent').waitFor();
    if (out) await page.screenshot({path: out + '/kept-' + width + '.png'});
    const t0 = Date.now(); await dialog.getByRole('button', {name: 'Put back in draft', exact: true}).click();
    await page.keyboard.press('Escape');
    await value(box, original);
    results.cases.push({case: 'refused-restore-reload-put-back', put_back_to_text_ms: Date.now() - t0, sends, cancels});
    assert.equal(sends, 2, 'no new send from recovery');

    // 3. A second tab of the same page waits and writes nothing until the first closes.
    const second = await ctx.newPage(); second.setDefaultTimeout(8000);
    await second.goto(origin + '/?skin=comic');
    await second.getByRole('status').filter({hasText: 'open in another tab'}).waitFor();
    assert.equal(await second.evaluate(() => window.__agentnetSingleWindow), undefined, 'second tab not started');
    assert.equal(await second.locator('[data-part="chat-row"]').count(), 0, 'second tab shows no chats');
    await page.close();
    await second.locator('[data-part="chat-row"]').first().waitFor();
    assert.equal(await second.evaluate(() => window.__agentnetSingleWindow), 'lock');
    results.cases.push({case: 'second-tab-waits-then-starts'});
    assert.deepEqual(errors, []);
    results.pass = true; delete results.page;
    console.log('Cancel restore rendered PASS ' + JSON.stringify(results));
  } catch (e) {
    results.pass = false; results.error = String(e.stack || e);
    if (out && results.page) await results.page.screenshot({path: out + '/failure-' + width + '.png'}).catch(() => {});
    delete results.page; console.error(JSON.stringify(results)); process.exitCode = 1;
  } finally {
    if (out) fs.writeFileSync(out + '/cancel-restore-' + width + '.json', JSON.stringify(results, null, 1) + '\n');
    await browser.close();
  }
})();
