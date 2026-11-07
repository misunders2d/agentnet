// Saved messages → Retry saved send on the phone (Android host marker, as in
// message_images_rendered.cjs) through the real daemon page and built Comic
// bundle. Only /api/dm/send and the chat view are stubbed:
//  A. The send's answer is lost after the host stored it; the person then holds
//     it; a newer draft is typed. Retry sends nothing, says it is stopped in its
//     chat, and leaves the newer draft.
//  B. The send's answer is lost before the host stored it. Retry reaches the real
//     host route once, under the same ID, and says it is in the outbox; the host's
//     own change notice refreshes the chat, which settles Saved messages.
//   node saved_send_retry_rendered_check.cjs <page URL with token>
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const out = process.env.CANCEL_EVIDENCE, results = {cases: []};
const held = 'Stopped. Its files are kept on this device: Retry sends it, Discard removes it.';

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
    const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width: 390, height: 844}, hasTouch: true});
    await ctx.addInitScript(() => { window.__agentnetPlatform = 'android';
      window.__agentnetAndroid = {visible: () => true, onVisibility: () => () => {}, legacyDrafts: async () => ({drafts: []}), acknowledgeLegacyDraft: async () => ({removed: true})}; });
    const origin = new URL(process.argv[2]).origin;
    let row = null, mode = 'store-then-lose', posts = [];
    await ctx.route('**/api/dm/send', async route => {
      const body = route.request().postDataJSON(); posts.push(body.id);
      if (mode === 'store-then-lose') { row = {id: body.id, lid: body.id, dir: 'out', kind: 'message', body: body.body, at: new Date().toISOString(), state: 'queued', state_text: 'Sending…', queued_send: {state: 'pending', rev: 1}, can: [], actions: [], reactions: []}; return route.abort('failed'); }
      if (mode === 'lose') return route.abort('failed');
      // The host itself accepts it: it stores the message under this ID and announces
      // the change on /events (Fixture.SendDM → bump, as QueueSend → store.changed).
      return route.continue();
    });
    await ctx.route('**/api/dm?*', async route => {
      const response = await route.fetch(), view = await response.json();
      if (row) view.messages = [...(view.messages || []), {...row, from: view.me?.address, send_group_author: view.me?.fingerprint}];
      await route.fulfill({response, json: view});
    });
    const page = await ctx.newPage(); page.setDefaultTimeout(8000);
    const errors = []; page.on('pageerror', e => errors.push(String(e)));
    await page.goto(process.argv[2]); await page.goto(origin + '/?skin=comic');
    const openChat = async () => { await page.locator('[data-part="chat-row"][data-kind="person"]').filter({hasNotText: 'Device conversation'}).first().click(); };
    await openChat();
    const card = () => page.locator('[data-card]').last();
    const box = card().getByRole('form', {name: 'Write a message'}).getByRole('textbox').first();
    const send = async text => { await box.fill(text); await card().getByRole('form', {name: 'Write a message'}).locator('button[type="submit"]').click();
      await page.getByText('Send not confirmed. Saved on this phone', {exact: false}).first().waitFor(); };
    const retry = async (body, receipt) => {
      const sheet = page.getByRole('dialog', {name: 'Saved messages'});
      await card().getByRole('button', {name: /^Saved messages \(1\)/}).tap(); await sheet.waitFor();
      const entry = sheet.locator('section').filter({hasText: body});
      await entry.getByText('Send not yet confirmed').waitFor();
      const t0 = Date.now(); await entry.getByRole('button', {name: 'Retry saved send'}).click();
      await page.getByText(receipt, {exact: false}).first().waitFor(); const ms = Date.now() - t0;
      if (await sheet.count()) { await page.keyboard.press('Escape'); await sheet.waitFor({state: 'detached'}).catch(() => {}); }
      await page.locator('[data-open][role="presentation"]').first().waitFor({state: 'detached'});
      return ms;
    };
    const value = async t => { const end = Date.now() + 3000; while (Date.now() < end) { if (await box.inputValue().catch(() => null) === t) return; await page.waitForTimeout(20); } throw new Error('composer is not ' + JSON.stringify(t) + ': ' + JSON.stringify(await box.inputValue().catch(() => null))); };

    // A. stored, answer lost; then held; a newer draft; Retry.
    await send('original words kept'); const idA = posts[0];
    row = {...row, state: 'failed', state_text: held, detail: held, queued_send: {state: 'needs_review', rev: 2}};
    await page.reload(); await openChat(); await box.waitFor();
    await box.fill('newer words typed after'); await page.waitForTimeout(400);
    const msA = await retry('original words kept', 'stopped in its chat and was not sent');
    assert.deepEqual(posts, [idA], 'Retry sent nothing for a held message');
    await value('newer words typed after');
    assert.equal(await card().getByRole('button', {name: /^Saved messages/}).count(), 0, 'the saved send is settled');
    results.cases.push({case: 'held-after-lost-answer', click_to_receipt_ms: msA, posts: posts.length});
    if (out) await page.screenshot({path: out + '/saved-retry-held-390.png'});

    // B. answer lost before the host stored it; Retry sends once under the same ID.
    row = null; mode = 'lose'; await box.fill('');
    await send('second original'); const idB = posts[1]; mode = 'answer';
    await page.waitForTimeout(600); results.open_layers = await page.getByRole('dialog').evaluateAll(ds => ds.map(d => (d.getAttribute('aria-label') || '') + '|' + (d.textContent || '').slice(0, 120)));
    const msB = await retry('second original', 'Saved send is in the outbox.');
    assert.deepEqual(posts, [idA, idB, idB], 'one retry, same ID');
    await card().getByRole('button', {name: /^Saved messages/}).waitFor({state: 'detached'});
    results.cases.push({case: 'not-stored-retry-once', click_to_receipt_ms: msB, posts: posts.length});
    assert(msA < 1000 && msB < 1000, 'feedback under one second: ' + msA + '/' + msB);
    assert.deepEqual(errors, []);
    results.pass = true;
    console.log('Saved send retry rendered PASS ' + JSON.stringify(results));
  } catch (e) {
    results.pass = false; results.error = String(e.stack || e);
    console.error(JSON.stringify(results)); process.exitCode = 1;
  } finally {
    if (out) fs.writeFileSync(out + '/saved-send-retry.json', JSON.stringify(results, null, 1) + '\n');
    await browser.close();
  }
})();
