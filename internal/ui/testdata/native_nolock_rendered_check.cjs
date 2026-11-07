// A page whose WebView has no Web Locks: two pages of the real daemon page and
// built Comic bundle on one origin (one localStorage). navigator.locks is
// removed, or its request refused, before any page script runs. One writer is
// the invariant: two pages must never both write this origin's saved state.
//  - A plain browser page is refused before anything writes.
//  - A native shell's page (desktop marker __agentnetNativeAppJSON, or the
//    Android marker) opens read-only: chats, Settings and update stay usable and
//    Saved messages can be read and copied, but nothing writes drafts, the send
//    journal or send recovery, and nothing is sent or retried. Storage seeded
//    with an older version's draft map, a saved send and a kept original stays
//    byte-identical in both pages and after a reload.
//  - With locks, the same native page writes as usual and a second page waits.
//   node native_nolock_rendered_check.cjs <page URL with token> <DM id>
// NOLOCK_EVIDENCE=<dir> receives the result JSON.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const out = process.env.NOLOCK_EVIDENCE, results = {cases: []};
const conv = process.argv[3];
const readOnly = /Drafts are read-only in this window/;
const pageErrors = [];
let current = null; // the page under check, for a failure screenshot

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
  for (const [variant, native] of [['rejected', ''], ['missing', ''], ['rejected', 'desktop'], ['missing', 'desktop'], ['rejected', 'android'], ['missing', 'android'], ['lock', 'desktop'], ['lock', 'android']]) {
    const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width: 1280, height: 900}});
    await ctx.addInitScript(({variant, native}) => {
      if (variant === 'missing') Object.defineProperty(Navigator.prototype, 'locks', {get: () => undefined, configurable: true});
      else if (variant === 'rejected') LockManager.prototype.request = function () { return Promise.reject(new DOMException('synthetic refusal', 'SecurityError')); };
      if (native === 'desktop') Object.defineProperty(window, '__agentnetNativeAppJSON', {value: async () => ({})});
      if (native === 'android') { window.__agentnetPlatform = 'android';
        window.__agentnetAndroid = {visible: () => true, onVisibility: () => () => {}, connection: async () => ({enabled: true}), notificationStatus: async () => ({granted: true, app_granted: true}), copyText: async t => { window.__copied = t; return {}; }, legacyDrafts: async () => ({drafts: window.__legacyRecords || []}), acknowledgeLegacyDraft: async () => { window.__legacyAcks = (window.__legacyAcks || 0) + 1; return {removed: true}; }}; }
    }, {variant, native});
    const sends = []; await ctx.route('**/api/dm/send', r => { sends.push(r.request().url()); return r.continue(); });
    // Queued rows of this person's own (as another window's sends leave them): one
    // still pending, one held. Row Retry/Cancel/Discard reach /api/schedule/*.
    const rowsOn = variant === 'rejected' && !!native, schedule = []; if (rowsOn) results.schedule = schedule;
    const held = 'Stopped. Its files are kept on this device: Retry sends it, Discard removes it.';
    if (rowsOn) {
      await ctx.route('**/api/dm?*', async route => {
        const response = await route.fetch(), view = await response.json();
        if (new URL(route.request().url()).searchParams.get('id') === conv) {
          const own = {dir: 'out', kind: 'message', at: new Date().toISOString(), from: view.me?.address, send_group_author: view.me?.fingerprint, can: [], actions: [], reactions: []};
          view.messages = [...(view.messages || []),
            {...own, id: 'c'.repeat(32), lid: 'c'.repeat(32), body: 'queued pending words', state: 'queued', state_text: 'Sending…', queued_send: {state: 'pending', rev: 1}},
            {...own, id: 'd'.repeat(32), lid: 'd'.repeat(32), body: 'queued held words', state: 'failed', state_text: held, detail: held, queued_send: {state: 'needs_review', rev: 2}},
            // The kept original's send, now stored by the host (another window sent it).
            {...own, id: 'b'.repeat(32), lid: 'b'.repeat(32), body: 'kept original words', state: 'delivered'}];
        }
        await route.fulfill({response, json: view});
      });
      await ctx.route('**/api/schedule/*', r => { const path = new URL(r.request().url()).pathname; schedule.push(path);
        return r.fulfill({json: path.endsWith('/hold_send') ? {id: 'c'.repeat(32), state: 'needs_review', detail: held} : {id: r.request().postDataJSON()?.id, state: 'cancelled'}}); });
    }
    const origin = new URL(process.argv[2]).origin;
    const label = (native || 'browser') + '-' + variant;
    const first = await ctx.newPage(); first.setDefaultTimeout(8000); current = first;
    const errors = []; first.on('pageerror', e => { errors.push(String(e)); pageErrors.push(label + ': ' + String(e)); });
    await first.goto(process.argv[2]); await first.goto(origin + '/?skin=comic');
    const mode = p => p.evaluate(() => window.__agentnetSingleWindow ?? null);
    const snapshot = p => p.evaluate(() => { const v = {}; for (let i = 0; i < localStorage.length; i++) { const k = localStorage.key(i); if (/drafts|send-journal/.test(k)) v[k] = localStorage.getItem(k); } return JSON.stringify(Object.entries(v).sort()); });
    if (!native) {
      await first.waitForTimeout(800);
      const second = await ctx.newPage(); await second.goto(origin + '/?skin=comic'); await second.waitForTimeout(800);
      for (const p of [first, second]) {
        assert.equal(await mode(p), 'none'); assert.equal(await p.locator('[data-part="chat-row"]').count(), 0);
        assert.equal(await p.getByRole('status').filter({hasText: /cannot run AgentNet/}).count(), 1);
      }
      assert.equal(await snapshot(first), '[]', 'nothing written to drafts');
      results.cases.push({case: label, refused: true}); await ctx.close(); continue;
    }
    // Seed raw storage as earlier work left it: an older version's whole draft map
    // (no revisions), a saved send in the journal, and a kept original.
    await first.locator('[data-part="chat-row"]').first().waitFor();
    await first.evaluate(async ({conv, origin}) => {
      const me = (await (await fetch('/api/overview')).json()).me;
      const id1 = 'a'.repeat(32), id2 = 'b'.repeat(32);
      localStorage.clear();
      localStorage.setItem('agentnet.messenger.drafts.default', JSON.stringify({[conv]: {text: 'older version draft words'}}));
      localStorage.setItem('agentnet.android.send-journal.v1.default', JSON.stringify([{id: id1, conversation: conv, endpoint: '/api/dm/send', request: {id: id1, conversation: conv, body: 'saved send words'}}]));
      localStorage.setItem('agentnet.messenger.drafts.default.send-recovery.v1.' + id2, JSON.stringify({version: 1, id: id2, conversation: conv,
        owner: {workspace: 'default', handle: '', endpoint: origin, realm: '', address: me.address, fingerprint: me.fingerprint}, draft: {text: 'kept original words'}, revision: 'legacy:null', hadFiles: false}));
    }, {conv, origin});
    await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], {origin});
    const before = await snapshot(first);
    await first.reload(); await first.locator('[data-part="chat-row"]').first().waitFor();
    const second = await ctx.newPage(); second.setDefaultTimeout(8000); second.on('pageerror', e => { errors.push(String(e)); pageErrors.push(label + ' second: ' + String(e)); }); await second.goto(origin + '/?skin=comic');
    await first.waitForTimeout(1200);
    const openChat = async p => { await p.locator('[data-part="chat-row"][data-kind="person"]').filter({hasNotText: 'Device conversation'}).first().click(); };
    const saved = p => p.getByRole('button', {name: /^Saved messages \(/});
    if (variant === 'lock') {
      // The usual path: this page holds the lock and writes; the other waits.
      assert.equal(await mode(first), 'lock');
      assert.equal(await second.getByRole('status').filter({hasText: /open in another tab or window/}).count(), 1, 'second page waits');
      assert.equal(await second.locator('[data-part="chat-row"]').count(), 0);
      const sheet = () => first.getByRole('dialog', {name: 'Saved messages'});
      const openSaved = async () => { if (!(await sheet().count())) await saved(first).click(); await sheet().waitFor(); return sheet(); };
      const item = async text => (await openSaved()).locator('section').filter({hasText: text});
      // Kept original: its chat is not loaded yet, so Open chat (then it can be checked).
      await (await item('kept original words')).getByRole('button', {name: 'Open chat', exact: true}).click();
      await sheet().waitFor({state: 'detached'});
      const box = first.getByRole('form', {name: 'Write a message'}).getByRole('textbox').first(); await box.waitFor();
      await box.fill('newer draft words'); await first.waitForTimeout(500);
      // Copy text, then Discard: the newer draft stays; only the kept copy goes.
      await (await item('kept original words')).getByRole('button', {name: 'Copy text', exact: true}).click();
      await first.getByText(/^Copied$|Couldn’t copy here/).first().waitFor();
      const copied = native === 'android' ? await first.evaluate(() => window.__copied) : await first.evaluate(() => navigator.clipboard.readText());
      assert.equal(copied, 'kept original words', 'Copy text copies the kept text');
      await (await item('kept original words')).getByRole('button', {name: 'Discard', exact: true}).click();
      await first.getByText('kept original words').waitFor({state: 'detached'});
      assert.equal(await box.inputValue(), 'newer draft words', 'Discard leaves the newer draft');
      let snap = await snapshot(first);
      assert(snap.includes('newer draft words'), 'the lock holder saves its draft');
      assert(!snap.includes('send-recovery.v1.' + 'b'.repeat(32)), 'Discard removes the kept original');
      // The earlier preview's legacy records name 32/64-hex conversations (legacy-drafts.mjs); this
      // fixture's chats are dm0001-style, so the legacy normal path is not reachable here.
      results.cases.push({case: label, writes: true, copied: true, kept_discarded: true, legacy: 'not reachable in this fixture'});
      if (out) await first.screenshot({path: out + '/native-lock-' + label + '.png'});
      await ctx.close(); continue;
    }
    const state = async p => ({mode: await mode(p), chats: await p.locator('[data-part="chat-row"]').count(),
      notice: await p.getByText(readOnly).count()});
    const a = await state(first), b = await state(second);
    results.pages = {...results.pages, [label]: {first: a, second: b}};
    for (const s of [a, b]) { assert.equal(s.mode, 'readonly'); assert(s.chats > 0, 'native page opens'); assert(s.notice > 0, 'read-only state is shown'); }
    for (const p of [first, second]) {
      // Saved messages: readable and copyable, nothing that writes or sends.
      await saved(p).click(); const sheet = p.getByRole('dialog', {name: 'Saved messages'});
      await sheet.getByText('kept original words').waitFor();
      if (native === 'android') await sheet.getByText('saved send words').waitFor();
      assert(await sheet.getByRole('button', {name: 'Copy text'}).count() >= 1, 'Copy text offered');
      assert.equal(await sheet.getByRole('button', {name: /Retry saved send|Put back in draft|Discard|Restore draft/}).count(), 0, 'nothing that writes or sends');
      if (p === first) {
        // Read-only Saved messages: Copy text copies; Open chat opens the chat to read.
        const kept = sheet.locator('section').filter({hasText: 'kept original words'});
        await kept.getByRole('button', {name: 'Copy text', exact: true}).click();
        await p.getByText(/^Copied$|Couldn’t copy here/).first().waitFor();
        assert.equal(native === 'android' ? await p.evaluate(() => window.__copied) : await p.evaluate(() => navigator.clipboard.readText()), 'kept original words', 'read-only Copy text');
        await kept.getByRole('button', {name: 'Open chat', exact: true}).click(); await sheet.waitFor({state: 'detached'});
        await p.getByText(readOnly).nth(1).waitFor(); // the opened chat's closed composer
      } else { await p.keyboard.press('Escape'); await sheet.waitFor({state: 'detached'}); }
      // The chat opens to read; its composer says why nothing is written.
      current = p; await openChat(p);
      // The banner and the chat's closed composer both say it.
      await p.getByText(readOnly).nth(1).waitFor();
      assert.equal(await p.getByRole('form', {name: 'Write a message'}).count(), 0, 'no composer');
      // Settings, with its update control, stays reachable.
      await p.getByRole('navigation', {name: 'Main', exact: true}).getByRole('button', {name: 'Settings', exact: true}).click();
      await p.getByRole('button', {name: /^About/}).click();
      if (native === 'desktop') await p.getByRole('button', {name: 'Check for updates', exact: true}).waitFor();
      await p.getByRole('navigation', {name: 'Main', exact: true}).getByRole('button', {name: 'Chats', exact: true}).click();
    }
    if (rowsOn && native === 'desktop') {
      // Row controls: Retry sends nothing, Discard of a held message destroys nothing;
      // Cancel stops the pending one and the host keeps it (hold), as when a draft can't be put back.
      current = first; await openChat(first);
      const discard = first.locator('[data-send-discard]');
      await first.getByText('queued held words').waitFor();
      await first.getByText('Stopped and kept. Drafts are read-only in this window, so it can’t be retried or discarded here.').first().waitFor();
      assert.equal(await first.locator('[data-send-retry]').count(), 0, 'no Retry offered on a held row');
      assert.equal(await discard.filter({hasText: 'Discard'}).count(), 0, 'no Discard offered on a held row');
      assert.equal(await first.getByText(/Retry sends it, Discard removes it/).count(), 0, 'no promise of Retry or Discard');
      assert.equal(await discard.filter({hasText: 'Cancel'}).count(), 0, 'pending row says Stop, not Cancel');
      await discard.filter({hasText: 'Stop'}).first().click(); await first.waitForTimeout(500);
      assert.deepEqual(schedule, ['/api/schedule/hold_send'], 'only a hold reaches the host');
      // Reply on the stored message: the draft change is refused and says so truthfully.
      const stored = first.locator('[data-mid="' + 'b'.repeat(32) + '"]'); await stored.hover();
      await stored.getByRole('button', {name: 'Reply', exact: true}).click();
      await first.getByText('Drafts are read-only in this window: this change wasn’t saved.').first().waitFor();
      assert.equal(await first.getByText(/Keep this window open/).count(), 0, 'no keep-open advice');
      assert.equal(await first.getByText(/original draft recovery could not be cleared/).count(), 0, 'stored send releases nothing and reports no failure');
      await first.getByRole('navigation', {name: 'Main', exact: true}).getByRole('button', {name: 'Chats', exact: true}).click();
    }
    if (rowsOn && native === 'android') {
      // The phone at 390: Cancel on a pending row holds it; nothing else reaches the host.
      current = first; await first.setViewportSize({width: 390, height: 844});
      if (!(await first.locator('[data-send-discard]').count())) await openChat(first);
      await first.locator('[data-send-discard]').filter({hasText: 'Stop'}).last().click(); await first.waitForTimeout(500);
      await first.getByText(/Stopped and kept/).first().waitFor();
      assert.deepEqual(schedule, ['/api/schedule/hold_send'], 'phone Cancel only holds');
      assert.equal(await first.getByText(/original draft recovery could not be cleared/).count(), 0, 'stored send releases nothing and reports no failure');
      if (out) await first.screenshot({path: out + '/native-readonly-android-cancel-390.png'});
      await first.setViewportSize({width: 1280, height: 900});
    }
    assert.equal(await snapshot(first), before, 'saved drafts, journal and recovery unchanged');
    await first.reload(); await first.locator('[data-part="chat-row"]').first().waitFor(); await first.waitForTimeout(800);
    assert.equal(await mode(first), 'readonly');
    assert.equal(await snapshot(second), before, 'unchanged after a reload');
    assert.deepEqual(sends, [], 'nothing sent or replayed');
    assert.equal(await first.evaluate(() => window.__legacyAcks || 0), 0, 'no legacy draft acknowledged');
    assert.deepEqual(errors, []);
    results.cases.push({case: label, readonly: true, unchanged: true});
    if (out) await first.screenshot({path: out + '/native-readonly-' + label + '.png'});
    await ctx.close();
  }
    results.pass = true;
    console.log('Native without locks rendered PASS ' + JSON.stringify(results));
  } catch (e) {
    results.pass = false; results.error = String(e.stack || e);
    results.page_errors = pageErrors;
    try { results.buttons = await current?.getByRole('button').allInnerTexts(); if (out) await current?.screenshot({path: out + '/native-nolock-failure.png'}); } catch { /* evidence only */ }
    console.error(JSON.stringify(results)); process.exitCode = 1;
  } finally {
    if (out) fs.writeFileSync(out + '/native-nolock.json', JSON.stringify(results, null, 1) + '\n');
    await browser.close();
  }
})();
