// A native shell whose WebView has no Web Locks: two pages of the real daemon
// page and built Comic bundle on one origin (one localStorage). The desktop
// shell's marker (__agentnetNativeAppJSON) is defined and navigator.locks is
// removed before any page script runs. One writer is the invariant: two pages
// must never both run and write this origin's drafts.
//   node native_nolock_rendered_check.cjs <page URL with token>
// NOLOCK_EVIDENCE=<dir> receives the result JSON.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const out = process.env.NOLOCK_EVIDENCE, results = {cases: []};

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
  // Both ways a page has no lock: navigator.locks missing, or its request refused.
  // A browser page is refused before anything writes. A native shell's page keeps
  // the shipped fallback (it runs, so its update and Saved messages stay reachable);
  // what two such pages do is recorded, not accepted (B5 stays open).
  for (const [variant, native] of [['rejected', false], ['missing', false], ['rejected', true], ['missing', true]]) {
    const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width: 1280, height: 900}});
    await ctx.addInitScript(({variant, ...rest}) => {
      variant = {name: variant, ...rest};
      if (variant.name === 'missing') Object.defineProperty(Navigator.prototype, 'locks', {get: () => undefined, configurable: true});
      else LockManager.prototype.request = function () { return Promise.reject(new DOMException('synthetic refusal', 'SecurityError')); };
      if (variant.native) Object.defineProperty(window, '__agentnetNativeAppJSON', {value: async () => ({})});
    }, {variant, native});
    const origin = new URL(process.argv[2]).origin;
    const first = await ctx.newPage(); first.setDefaultTimeout(8000);
    await first.goto(process.argv[2]); await first.goto(origin + '/?skin=comic');
    const second = await ctx.newPage(); second.setDefaultTimeout(8000);
    await second.goto(origin + '/?skin=comic');
    await new Promise(r => setTimeout(r, 1500));
    const state = async p => ({mode: await p.evaluate(() => window.__agentnetSingleWindow ?? null), chats: await p.locator('[data-part="chat-row"]').count(),
      refusal: await p.getByRole('status').filter({hasText: /tab locks|another tab or window|cannot run AgentNet/}).count()});
    const a = await state(first), b = await state(second);
    const drafts = await first.evaluate(() => { const k = []; for (let i = 0; i < localStorage.length; i++) if (/\.drafts/.test(localStorage.key(i))) k.push(localStorage.key(i)); return k.length; });
    const label = (native ? 'native-' : 'browser-') + variant;
    results.pages = {...results.pages, [label]: {first: a, second: b, draft_keys: drafts}};
    const running = [a, b].filter(s => s.chats > 0).length;
    // When both run, show what two writers do to one chat's draft.
    if (running === 2) {
      const open = async p => { await p.locator('[data-part="chat-row"][data-kind="person"]').filter({hasNotText: 'Device conversation'}).first().click();
        const box = p.getByRole('form', {name: 'Write a message'}).getByRole('textbox').first(); await box.waitFor(); return box; };
      const boxA = await open(first), boxB = await open(second);
      await boxA.fill('typed in the first window'); await first.waitForTimeout(400);
      await boxB.fill('typed in the second window'); await second.waitForTimeout(400);
      const stored = await first.evaluate(() => { const v = []; for (let i = 0; i < localStorage.length; i++) { const k = localStorage.key(i); if (/\.drafts/.test(k)) v.push(localStorage.getItem(k)); } return v.join('\n'); });
      results.draft = {...results.draft, [label]: {first_composer: await boxA.inputValue(), second_composer: await boxB.inputValue(),
        first_text_in_storage: stored.includes('typed in the first window'), second_text_in_storage: stored.includes('typed in the second window')}};
    }
    results.cases.push({case: label + '-two-pages', running});
    if (native) {
      // The shipped fallback: the app opens (update, Saved messages reachable).
      for (const s of [a, b]) { assert.equal(s.mode, 'shell'); assert(s.chats > 0, 'native page opens'); }
    } else {
      for (const s of [a, b]) { assert.equal(s.mode, 'none'); assert.equal(s.chats, 0); assert.equal(s.refusal, 1); }
      assert.equal(drafts, 0, 'nothing written to drafts');
    }
    await ctx.close();
  }
    results.pass = true;
    console.log('Native without locks rendered PASS ' + JSON.stringify(results));
  } catch (e) {
    results.pass = false; results.error = String(e.stack || e);
    console.error(JSON.stringify(results)); process.exitCode = 1;
  } finally {
    if (out) fs.writeFileSync(out + '/native-nolock.json', JSON.stringify(results, null, 1) + '\n');
    await browser.close();
  }
})();
