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
const out = process.env.NOLOCK_EVIDENCE, results = {cases: [], steps: {}, timing: {}, assertions: []}, failures = [];
// Independent assertions inside a step: each is recorded executed pass/fail and the
// step goes on; any failure fails the variant at its end. Prerequisites stay hard.
let softFailed = [], browserRef = null;
// Diagnostics (b9): every page of every context runs a continuous animation-frame
// loop (frame gaps over 50 ms, callback timestamps), a longtask PerformanceObserver
// and visibility/focus/freeze events. The loop itself keeps a frame requested on
// every page: a controlled scheduling change, recorded as such. Long-task entries
// carry no product stack; the Chromium trace (Save, Stop, Reply) is the source for
// main-thread attribution.
results.diag = {}; results.scheduling_change = 'b9: continuous requestAnimationFrame loop on every page (diagnostics); b8 had none';
const diag = async (label, pages, a, b) => { const o = {}; for (const [name, p] of pages) {
  if (!p || p.isClosed()) { o[name] = 'closed'; continue; }
  o[name] = await p.evaluate(([a, b]) => { const d = window.__diag || {}, w = x => (x || []).filter(e => e.at >= a && e.at <= b);
    return {visibility: document.visibilityState, focus: document.hasFocus(), gaps: w(d.gaps), longtasks: w(d.longtasks), longtask_error: d.longtask_error || null,
      events: w(d.events), frames_in_window: (d.frames || []).filter(t => t >= a && t <= b).length, window_ms: Math.round(b - a)}; }, [a, b]).catch(e => 'error: ' + String(e).slice(0, 120));
} results.diag[label] = o; return o; };
const soft = (variant, name, ok, detail) => { results.assertions.push({variant, name, status: ok ? 'pass' : 'fail', ...(detail === undefined ? {} : {detail})}); if (!ok) softFailed.push(name); };
// A timed click: the page's own pointerdown (epoch ms), the intended state's first DOM
// mutation (selector + pattern, through open shadow roots), the frame after it (two
// animation frames) with its rendered visibility and hit-test, and the Playwright click
// call; the Playwright call log (DEBUG=pw:api, stderr) carries the actionability wait.
const timedClick = async (page, label, target, selector, pattern, seen, opts = {}) => {
  await page.evaluate(({label, selector, pattern}) => {
    const now = () => performance.timeOrigin + performance.now(), m = (window.__timing ||= {})[label] = {}, re = new RegExp(pattern);
    const roots = new Set([document]); const walk = r => { for (const el of r.querySelectorAll('*')) if (el.shadowRoot) { roots.add(el.shadowRoot); walk(el.shadowRoot); } }; walk(document);
    addEventListener('pointerdown', e => { if (!m.input) { m.input = performance.timeOrigin + e.timeStamp; const t = e.composedPath()[0]; m.input_target = String(t.closest?.('button')?.textContent || t.tagName || '').trim().slice(0, 40); } }, {capture: true});
    const find = () => { let hit = null; for (const r of roots) for (const el of r.querySelectorAll(selector)) if (re.test(el.textContent) && (selector !== '*' || ![...el.children].some(c => re.test(c.textContent)))) hit = el; return hit; };
    const check = () => { if (!m.input || m.dom) return; const el = find(); if (!el) return; m.dom = now();
      m.witness = el.tagName.toLowerCase() + (el.getAttribute('role') ? '[role=' + el.getAttribute('role') + ']' : '') + (el.getAttribute('data-mid') ? '[data-mid]' : '') + ' ' + String(el.textContent).trim().slice(0, 50);
      requestAnimationFrame(() => requestAnimationFrame(() => { m.frame = now(); const b = el.getBoundingClientRect(); const c = el.getRootNode().elementFromPoint?.(b.left + Math.min(b.width / 2, 20), b.top + Math.min(b.height / 2, 10));
        m.visible = el.checkVisibility({opacityProperty: true, visibilityProperty: true}) && b.width > 0 && b.height > 0 && b.bottom > 0 && b.top < innerHeight; m.on_top = !!c && (el === c || el.contains(c)); })); };
    for (const r of roots) new MutationObserver(check).observe(r, {subtree: true, childList: true, characterData: true, attributes: true});
  }, {label, selector, pattern});
  let traced = false; if (opts.trace && out) { try { await browserRef.startTracing(page, {path: out + '/trace-' + label + '.json', screenshots: false,
    categories: ['devtools.timeline', 'disabled-by-default-devtools.timeline', 'disabled-by-default-devtools.timeline.frame', 'toplevel', 'blink.user_timing', 'renderer.scheduler']}); traced = true; } catch (e) { results.trace_errors = [...(results.trace_errors || []), label + ': ' + String(e).slice(0, 120)]; } }
  const c0 = Date.now(); console.error('[timed-click start] ' + label + ' ' + new Date(c0).toISOString());
  await target.click(); const c1 = Date.now(); console.error('[timed-click end] ' + label + ' ' + new Date(c1).toISOString());
  let seenOk = true; try { await seen.waitFor(); } catch { seenOk = false; } const c2 = Date.now();
  await page.waitForFunction(l => window.__timing[l].frame, label, {timeout: 3000}).catch(() => {});
  const m = await page.evaluate(l => window.__timing[l], label), d = (a, b) => (a && b ? Math.round(b - a) : null);
  if (traced) await browserRef.stopTracing().catch(e => { results.trace_errors = [...(results.trace_errors || []), label + ' stop: ' + String(e).slice(0, 120)]; });
  if (opts.pages) await diag(label, opts.pages, c0 - 2500, Date.now());
  const t = {click_call_to_input_ms: d(c0, m.input), click_call_resolved_ms: c1 - c0, input_target: m.input_target, input_epoch_ms: m.input ? Math.round(m.input) : null,
    input_to_dom_ms: d(m.input, m.dom), input_to_frame_ms: d(m.input, m.frame), rendered_visible: m.visible ?? null, on_top: m.on_top ?? null, witness: m.witness ?? null, traced,
    click_call_to_playwright_visible_ms: seenOk ? c2 - c0 : null, playwright_saw_it: seenOk};
  results.timing[label] = t; return t;
};
const conv = process.argv[3];
const readOnly = /Drafts are read-only in this window/;
const pageErrors = [];
let current = null; // the page under check, for a failure screenshot

(async () => {
  const browser = browserRef = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
  for (const [variant, native] of [['rejected', ''], ['missing', ''], ['rejected', 'desktop'], ['missing', 'desktop'], ['rejected', 'android'], ['missing', 'android'], ['lock', 'desktop'], ['lock', 'android']]) {
    // Each variant runs on its own context; a failure is recorded with the steps it
    // left unexecuted and the next variant still runs. Any failure fails the run.
    const label = (native || 'browser') + '-' + variant, done = [], step = n => { done.push(n); };
    const planned = !native ? ['refused'] : variant === 'lock' ? ['lock-second-waits', ...(native === 'android' ? ['journal-copy'] : []), 'kept-copy-discard', ...(native === 'android' ? ['legacy-restore-retry'] : [])]
      : ['readonly-state', 'saved-read-copy-closed-composer-settings', ...(variant === 'rejected' ? (native === 'desktop' ? ['rows-reply-desktop'] : ['stop-kept-390', 'reply-refused-390']) : []), ...(variant === 'missing' ? ['outbox-' + native] : []), 'storage-unchanged-reload'];
    const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width: 1280, height: 900}});
    softFailed = [];
    await ctx.addInitScript(() => {
      const now = () => performance.timeOrigin + performance.now(), d = window.__diag = {gaps: [], longtasks: [], events: [], frames: []};
      let last = 0; const tick = () => { const t = now(); if (last && t - last > 50) d.gaps.push({at: t, gap_ms: Math.round(t - last), visibility: document.visibilityState});
        last = t; d.frames.push(t); if (d.frames.length > 3000) d.frames.splice(0, 1000); requestAnimationFrame(tick); }; requestAnimationFrame(tick);
      try { new PerformanceObserver(l => { for (const e of l.getEntries()) d.longtasks.push({at: performance.timeOrigin + e.startTime, duration_ms: Math.round(e.duration), name: e.name,
        attribution: (e.attribution || []).map(a => a.containerType + ':' + a.name)}); }).observe({type: 'longtask', buffered: true}); } catch (e) { d.longtask_error = String(e); }
      for (const ev of ['visibilitychange', 'focus', 'blur', 'pagehide', 'freeze', 'resume']) addEventListener(ev, () => d.events.push({at: now(), ev, visibility: document.visibilityState, focus: document.hasFocus()}), true);
    });
    try {
    await ctx.addInitScript(({variant, native}) => {
      if (variant === 'missing') Object.defineProperty(Navigator.prototype, 'locks', {get: () => undefined, configurable: true});
      else if (variant === 'rejected') LockManager.prototype.request = function () { return Promise.reject(new DOMException('synthetic refusal', 'SecurityError')); };
      if (native === 'desktop') Object.defineProperty(window, '__agentnetNativeAppJSON', {value: async () => ({})});
      if (native === 'android') { window.__agentnetPlatform = 'android';
        window.__agentnetAndroid = {visible: () => true, onVisibility: () => () => {}, connection: async () => ({enabled: true}), notificationStatus: async () => ({granted: true, app_granted: true}), copyText: async t => { window.__copied = t; return {}; }, legacyDrafts: async () => ({drafts: window.__legacyRecords || []}), acknowledgeLegacyDraft: async () => { window.__legacyAcks = (window.__legacyAcks || 0) + 1; return {removed: true}; }}; }
    }, {variant, native});
    // Legacy records name 32/64-hex chats (legacy-drafts.mjs); this fixture's chat is dm0001-style.
    // Two synthetic hex IDs stand for it on the wire only (views and sends), Android with the lock.
    const hexChats = native === 'android' && variant === 'lock' ? ['9'.repeat(32), '8'.repeat(32)] : [];
    const sends = []; await ctx.route('**/api/dm/send', r => { sends.push(r.request().url()); const body = r.request().postDataJSON();
      return hexChats.includes(body?.conv) ? r.continue({postData: JSON.stringify({...body, conv})}) : r.continue(); });
    if (hexChats.length) await ctx.route('**/api/dm?*', async route => {
      const url = new URL(route.request().url()), id = url.searchParams.get('id');
      if (!hexChats.includes(id)) return route.fallback();
      url.searchParams.set('id', conv); const response = await route.fetch({url: url.toString()}), view = await response.json();
      await route.fulfill({response, json: {...view, id}});
    });
    // Outbox in a read-only window (desktop at 1280, Android at 390; missing locks): the daemon fixture has no
    // schedules (Live does; TestLiveScheduleAPI persists edits), so the host's schedule
    // routes are stood in here with one pending row.
    const outboxOn = (native === 'desktop' || native === 'android') && variant === 'missing', scheduleWrites = [];
    if (outboxOn) {
      const row = {id: '7'.repeat(32), conv, topic: '', body: 'scheduled words', due: new Date(Date.now() + 3 * 3600000).toISOString(), timezone: 'UTC', rev: 1, state: 'pending', audience: {persons: [], pids: []}, files: []};
      await ctx.route('**/api/overview*', async route => { const response = await route.fetch(); await route.fulfill({response, json: {...await response.json(), schedules: true}}); });
      await ctx.route('**/api/schedules*', route => route.fulfill({json: [row]}));
      await ctx.route('**/api/schedule/**', route => {
        const path = new URL(route.request().url()).pathname;
        if (path === '/api/schedule/audience') return route.fulfill({json: {persons: [], pids: []}});
        const body = route.request().postDataJSON(); scheduleWrites.push({path, body, at: Date.now()});
        if (path !== '/api/schedule/edit' || body.id !== row.id || body.rev !== row.rev) return route.fulfill({status: 409, body: 'unexpected'});
        Object.assign(row, {body: body.body, due: body.due, rev: row.rev + 1}); return route.fulfill({json: row});
      });
    }
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
      assert.equal(await snapshot(first), '[]', 'nothing written to drafts'); step('refused');
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
    if (hexChats.length) await ctx.addInitScript(r => { window.__legacyRecords = r; }, [{key: 'dm:' + hexChats[0], value: JSON.stringify({body: 'legacy restore words'})}]);
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
      await second.close(); step('lock-second-waits'); // so a reload of this page keeps the lock
      const sheet = () => first.getByRole('dialog', {name: 'Saved messages'});
      const openSaved = async () => { if (!(await sheet().count())) await saved(first).click(); await sheet().waitFor(); return sheet(); };
      const item = async text => (await openSaved()).locator('section').filter({hasText: text});
      if (native === 'android') {
        // Journal Copy text: the saved send's exact words reach the clipboard; nothing changes or sends.
        const j0 = await snapshot(first);
        const tCopy = await timedClick(first, 'android-lock-journal-copy', (await item('saved send words')).getByRole('button', {name: 'Copy text', exact: true}), '*', '^Copied$', first.getByText(/^Copied$|Couldn’t copy here/).first());
        assert(tCopy.playwright_saw_it, 'Copied shown'); const jMs = tCopy.click_call_to_playwright_visible_ms;
        assert(tCopy.input_to_frame_ms !== null && tCopy.input_to_frame_ms < 1000, 'journal Copy rendered under one second from input: ' + JSON.stringify(tCopy));
        await first.waitForFunction(t => window.__copied === t, 'saved send words');
        assert.equal(await snapshot(first), j0, 'journal Copy text changes no saved state');
        assert.equal(sends.length, 0, 'journal Copy text sends nothing');
        assert(await (await item('saved send words')).count() === 1, 'the saved send stays offered');
        assert(jMs < 1000, 'journal Copy feedback under one second: ' + jMs);
        results.journal_copy = {click_to_copied_ms: jMs, clipboard: 'exact', storage: 'unchanged', sends: 0}; step('journal-copy');
      }
      // Kept original: its chat is not loaded yet, so Open chat (then it can be checked).
      await (await item('kept original words')).getByRole('button', {name: 'Open chat', exact: true}).click();
      await sheet().waitFor({state: 'detached'});
      const box = first.getByRole('form', {name: 'Write a message'}).getByRole('textbox').first(); await box.waitFor();
      await box.fill('newer draft words'); await first.waitForTimeout(500);
      // Copy text, then Discard: the newer draft stays; only the kept copy goes.
      await (await item('kept original words')).getByRole('button', {name: 'Copy text', exact: true}).click();
      await first.getByText(/^Copied$|Couldn’t copy here/).first().waitFor();
      if (native === 'android') await first.waitForFunction(t => window.__copied === t, 'kept original words'); // an earlier Copied toast may still show
      else assert.equal(await first.evaluate(() => navigator.clipboard.readText()), 'kept original words', 'Copy text copies the kept text');
      await (await item('kept original words')).getByRole('button', {name: 'Discard', exact: true}).click();
      await first.getByText('kept original words').waitFor({state: 'detached'});
      assert.equal(await box.inputValue(), 'newer draft words', 'Discard leaves the newer draft');
      let snap = await snapshot(first);
      assert(snap.includes('newer draft words'), 'the lock holder saves its draft');
      assert(!snap.includes('send-recovery.v1.' + 'b'.repeat(32)), 'Discard removes the kept original'); step('kept-copy-discard');
      if (hexChats.length) {
        const [hex, hex2] = hexChats, acks = () => first.evaluate(() => window.__legacyAcks || 0);
        // Legacy draft into an empty chat: restored, saved, acknowledged once.
        await (await item('legacy restore words')).getByRole('button', {name: 'Restore draft', exact: true}).click();
        await first.waitForFunction(() => window.__legacyAcks === 1);
        if (await sheet().count()) { await first.keyboard.press('Escape'); await sheet().waitFor({state: 'detached'}); }
        const hexBox = first.getByRole('form', {name: 'Write a message'}).getByRole('textbox').first();
        assert.equal(await hexBox.inputValue(), 'legacy restore words', 'legacy draft restored into its chat');
        assert((await snapshot(first)).includes('legacy restore words'), 'restored draft saved');
        await hexBox.fill('newer hex draft'); await first.waitForTimeout(500);
        assert((await snapshot(first)).includes('newer hex draft'), 'newer draft saved');
        // After a reload: an older legacy draft for that chat is refused over the newer one;
        // an unconfirmed legacy send for another chat is retried once under its ID.
        await ctx.addInitScript(r => { window.__legacyRecords = r; }, [{key: 'dm:' + hex, value: JSON.stringify({body: 'older legacy words'})},
          {key: 'dm:' + hex2, value: JSON.stringify({body: 'legacy retry words', request: {id: 'e'.repeat(32), body: 'legacy retry words', conv: hex2}})}]);
        await first.reload(); await first.locator('[data-part="chat-row"]').first().waitFor();
        await (await item('older legacy words')).getByRole('button', {name: 'Restore draft', exact: true}).click();
        await first.getByText('There is already a draft in this chat. Keep or send it first, then restore this one.').first().waitFor();
        assert.equal(await acks(), 0, 'refused restore acknowledges nothing');
        snap = await snapshot(first);
        assert(snap.includes('newer hex draft') && !snap.includes('older legacy words'), 'newer draft not overwritten');
        await (await item('legacy retry words')).getByRole('button', {name: 'Retry saved send', exact: true}).click();
        await first.getByText('Saved send is in the outbox.').first().waitFor();
        await first.waitForFunction(() => window.__legacyAcks === 1);
        assert.equal(sends.length, 1, 'one request for the legacy send');
        // The fixture host stored it under its ID (read back from the host, not the request).
        const stored = await first.evaluate(async conv => ((await (await fetch('/api/dm?id=' + conv)).json()).messages || []).filter(m => (m.id === 'e'.repeat(32) || m.lid === 'e'.repeat(32)) && m.body === 'legacy retry words').length, conv);
        assert.equal(stored, 1, 'host holds the legacy send once');
        results.legacy = {restore_acks: 1, refused_acks: 0, retry_requests: sends.length, host_rows: stored}; step('legacy-restore-retry');
      }
      results.cases.push({case: label, writes: true, copied: true, kept_discarded: true, legacy: !!hexChats.length});
      if (out) await first.screenshot({path: out + '/native-lock-' + label + '.png'});
      await ctx.close(); continue;
    }
    const state = async p => ({mode: await mode(p), chats: await p.locator('[data-part="chat-row"]').count(),
      notice: await p.getByText(readOnly).count()});
    const a = await state(first), b = await state(second);
    results.pages = {...results.pages, [label]: {first: a, second: b}};
    for (const s of [a, b]) { assert.equal(s.mode, 'readonly'); assert(s.chats > 0, 'native page opens'); assert(s.notice > 0, 'read-only state is shown'); }
    step('readonly-state');
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
    step('saved-read-copy-closed-composer-settings');
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
      step('rows-reply-desktop');
    }
    if (rowsOn && native === 'android') {
      // The phone at 390 (items 3 and 6): Stop on the pending row holds it; both rows are
      // kept and say so, with no Retry or Discard; Reply is refused without a draft write.
      current = first; await first.setViewportSize({width: 390, height: 844});
      if (!(await first.locator('[data-send-discard]').count())) await openChat(first);
      const kept = 'Stopped and kept. Drafts are read-only in this window, so it can’t be retried or discarded here.', v = label;
      await first.getByText('queued held words').waitFor();
      const tStop = await timedClick(first, 'android390-stop', first.locator('[data-send-discard]').filter({hasText: 'Stop'}).last(),
        '[data-mid="' + 'c'.repeat(32) + '"]', 'Stopping…|Stopped and kept', first.locator('[data-mid="' + 'c'.repeat(32) + '"]').getByText(/Stopping…|Stopped and kept/).first(), {pages: [['first', first], ['second', second]], trace: true});
      await first.locator('[data-local-send="stopped"]').filter({hasText: kept}).nth(1).waitFor(); // prerequisite for the state checks
      soft(v, 'stop: only a hold reaches the host', JSON.stringify(schedule) === JSON.stringify(['/api/schedule/hold_send']), schedule);
      soft(v, 'stop: both rows stopped and kept, truthfully', await first.locator('[data-local-send="stopped"]').filter({hasText: kept}).count() === 2);
      soft(v, 'stop: no Retry at 390', await first.locator('[data-send-retry]').count() === 0);
      soft(v, 'stop: no Discard or Stop left at 390', await first.locator('[data-send-discard]').count() === 0);
      soft(v, 'stop: no promise of Retry or Discard', await first.getByText(/Retry sends it, Discard removes it/).count() === 0);
      soft(v, 'stop: stored send releases nothing, no failure shown', await first.getByText(/original draft recovery could not be cleared/).count() === 0);
      soft(v, 'stop: click call to visible under 1 s (19:50Z method)', tStop.click_call_to_playwright_visible_ms !== null && tStop.click_call_to_playwright_visible_ms < 1000, tStop);
      soft(v, 'stop: input to rendered visible frame under 1 s', tStop.rendered_visible === true && tStop.input_to_frame_ms !== null && tStop.input_to_frame_ms < 1000, tStop);
      if (out) await first.screenshot({path: out + '/native-readonly-android-stop-390.png'});
      step('stop-kept-390');
      const r0snap = await snapshot(first);
      await first.locator('[data-mid="' + 'b'.repeat(32) + '"]').getByRole('button', {name: /^(More actions|Message actions)$/}).click(); // prerequisite: the action sheet
      const refusal = 'Drafts are read-only in this window: this change wasn’t saved.';
      const tReply = await timedClick(first, 'android390-reply', first.getByRole('dialog').getByRole('button', {name: 'Reply', exact: true}), '*', 'this change wasn’t saved', first.getByText(refusal).first(), {pages: [['first', first], ['second', second]], trace: true});
      soft(v, 'reply: truthful refusal shown and rendered', tReply.playwright_saw_it && tReply.rendered_visible === true, tReply);
      soft(v, 'reply: no draft written', await snapshot(first) === r0snap);
      soft(v, 'reply: no keep-open advice', await first.getByText(/Keep this window open/).count() === 0);
      soft(v, 'reply: click call to visible under 1 s', tReply.click_call_to_playwright_visible_ms !== null && tReply.click_call_to_playwright_visible_ms < 1000, tReply);
      soft(v, 'reply: input to rendered visible frame under 1 s', tReply.input_to_frame_ms !== null && tReply.input_to_frame_ms < 1000, tReply);
      if (out) await first.screenshot({path: out + '/native-readonly-android-reply-390.png'});
      await first.keyboard.press('Escape');
      await first.setViewportSize({width: 1280, height: 900}); step('reply-refused-390');
    }
    if (outboxOn) {
      // Outbox edit is the host's own schedule: it works here, with one edit and no local write.
      current = first; const v = label;
      if (native === 'android') { await first.setViewportSize({width: 390, height: 844});
        // At 390 an open chat is a card over the tabs: its visible back control returns to them.
        // Conversation.header.tsx: below 1024 px the header shows "‹ Chats" (aria-label
        // "Back to chats") after the resize re-renders; b8 counted it before that (0).
        const back = first.getByRole('button', {name: 'Back to chats', exact: true}).last();
        await back.waitFor(); results.item4_back = {names: await first.getByRole('button').allInnerTexts().catch(() => null)}; await back.click(); }
      const nav = first.getByRole('navigation', {name: 'Main', exact: true});
      await nav.getByRole('button', {name: 'Outbox', exact: true}).click();
      const editing = first.getByRole('article').filter({hasText: 'scheduled words'});
      await editing.getByRole('button', {name: 'Edit schedule', exact: true}).click();
      await first.getByRole('textbox', {name: 'Scheduled message text', exact: true}).fill('edited in read-only');
      // The row's "Saving…" with the edited text, then the host's refreshed row.
      const tSave = await timedClick(first, 'outbox-' + native, editing.getByRole('button', {name: 'Save schedule changes', exact: true}),
        'article', 'edited in read-only[\\s\\S]*Saving…', first.getByRole('article').filter({hasText: 'edited in read-only'}).first(), {pages: [['first', first], ['second', second]], trace: true});
      await first.getByRole('article').filter({hasText: 'edited in read-only'}).filter({hasNotText: 'Saving…'}).first().waitFor(); // the host's row
      const edit = scheduleWrites.find(w => w.path === '/api/schedule/edit');
      tSave.input_to_host_request_ms = edit && tSave.input_epoch_ms ? edit.at - tSave.input_epoch_ms : null;
      soft(v, 'outbox: Saving… rendered visible and on top after the input', tSave.rendered_visible === true && tSave.on_top === true, tSave);
      soft(v, 'outbox: input to rendered visible frame under 1 s', tSave.input_to_frame_ms !== null && tSave.input_to_frame_ms < 1000, tSave);
      soft(v, 'outbox: click call to visible under 1 s (19:50Z method)', tSave.click_call_to_playwright_visible_ms !== null && tSave.click_call_to_playwright_visible_ms < 1000, tSave);
      soft(v, 'outbox: saved drafts unchanged', await snapshot(first) === before);
      results.outbox = scheduleWrites.map(w => w.path);
      soft(v, 'outbox: one edit reaches the host', JSON.stringify(results.outbox) === JSON.stringify(['/api/schedule/edit']), results.outbox);
      soft(v, 'outbox: host received the edited text', scheduleWrites[0]?.body?.body === 'edited in read-only');
      soft(v, 'outbox: no schedule token written', await first.evaluate(() => Object.keys(localStorage).filter(k => k.includes('schedule-save')).length) === 0);
      await nav.getByRole('button', {name: 'Chats', exact: true}).click();
      if (native === 'android') { if (out) await first.screenshot({path: out + '/native-readonly-android-outbox-390.png'}); await first.setViewportSize({width: 1280, height: 900}); }
      step('outbox-' + native);
    }
    assert.equal(await snapshot(first), before, 'saved drafts, journal and recovery unchanged');
    await first.reload(); await first.locator('[data-part="chat-row"]').first().waitFor(); await first.waitForTimeout(800);
    assert.equal(await mode(first), 'readonly');
    assert.equal(await snapshot(second), before, 'unchanged after a reload');
    assert.deepEqual(sends, [], 'nothing sent or replayed');
    assert.equal(await first.evaluate(() => window.__legacyAcks || 0), 0, 'no legacy draft acknowledged');
    assert.deepEqual(errors, []); step('storage-unchanged-reload');
    if (softFailed.length) throw new Error('assertions failed: ' + softFailed.join('; '));
    results.cases.push({case: label, readonly: true, unchanged: true});
    if (out) await first.screenshot({path: out + '/native-readonly-' + label + '.png'});
    await ctx.close();
    } catch (e) {
      const failed = planned.find(n => !done.includes(n)) || null;
      failures.push({case: label, failed_step: failed, unexecuted_after: planned.filter(n => !done.includes(n) && n !== failed), error: String(e.stack || e).split('\n').slice(0, 2).join(' | '),
        buttons: await current?.getByRole('button').allInnerTexts().catch(() => null)});
      if (out) await current?.screenshot({path: out + '/native-nolock-failure-' + label + '.png'}).catch(() => {});
    } finally {
      results.steps[label] = {planned, executed: [...done]}; await ctx.close().catch(() => {});
    }
  }
    results.failures = failures;
    if (failures.length) throw new Error(failures.length + ' variant(s) failed: ' + failures.map(f => f.case + '@' + f.failed_step).join(', '));
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
