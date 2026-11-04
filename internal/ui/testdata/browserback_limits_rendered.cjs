// Back when history's numbers cannot be trusted: entries the browser dropped,
// entries whose mark another writer erased, and a traversal that takes its time.
// Each case reports for itself, so one failure hides none of the others.
const assert = require('node:assert/strict');
const path = require('node:path');
const {pathToFileURL} = require('node:url');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const origin = new URL(process.argv[2]).origin;
const token = new URL(process.argv[2]).searchParams.get('t');
const results = {};
const run = async (name, fn) => {
  try { await fn(); results[name] = 'PASS'; } catch (e) { results[name] = 'FAIL: ' + String(e.message).split('\n')[0].slice(0, 160); }
};
(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox']});
  const open = async name => {
    const context = await browser.newContext(), page = await context.newPage();
    page.setDefaultTimeout(5000);
    await context.addCookies([{name: 'agentnet_ui', value: token, url: origin, httpOnly: true, sameSite: 'Strict'}]);
    await page.route(origin + '/back-*', r => r.fulfill({contentType: 'text/html', body: '<!doctype html><title>Limits fixture</title><body style="height:900px">x</body>'}));
    await page.goto(origin + '/back-earlier');
    await page.goto(origin + '/back-' + name);
    await page.evaluate(async () => {
      const {browserBackNavigation} = await import('/assets/browser-back.mjs');
      window.layers = 0; window.calls = 0;
      browserBackNavigation()(() => { calls++; if (layers) { layers--; return true; } return false; });
    });
    const follow = hash => page.evaluate(hash => new Promise(r => { addEventListener('hashchange', r, {once: true}); location.hash = hash; }), hash);
    return {context, page, follow};
  };

  // Finding 1. More links than the browser keeps: the distance home no longer exists.
  await run('history cap: Back still reaches a layer after it could not leave', async () => {
    const {context, page, follow} = await open('cap');
    for (let i = 0; i < 70; i++) await follow('#cap-' + i);
    await page.goBack().catch(() => {});
    await page.waitForFunction(() => calls === 1);
    await page.waitForTimeout(3000); // walks to the oldest entry kept, then finds nothing behind it
    await page.mouse.click(5, 5);
    await page.evaluate(() => { layers = 1; });
    await page.goBack().catch(() => {});
    await page.waitForFunction(() => calls === 2 && layers === 0, null, {timeout: 3000}).catch(async e => {
      throw new Error(e.message.split('\n')[0] + ' state ' + JSON.stringify(await page.evaluate(() => ({calls, layers, length: history.length, seq: history.state?.agentnetBackSeq, hash: location.hash}))));
    });
    await context.close();
  });

  await run('under the cap: Back with nothing open reaches the earlier document', async () => {
    const {context, page, follow} = await open('few');
    for (let i = 0; i < 20; i++) await follow('#few-' + i);
    await page.goBack({waitUntil: 'commit'}).catch(() => {});
    await page.waitForURL(origin + '/back-earlier', {timeout: 4000});
    await context.close();
  });

  // Finding 2. A writer that erases the mark: the next Back lands on an unmarked entry.
  await run('erased marks: later Back still reaches the earlier document', async () => {
    const {context, page, follow} = await open('erased');
    for (let i = 0; i < 12; i++) {
      await follow('#a' + i);
      await page.evaluate(() => history.replaceState(null, '', location.href));
      await follow('#b' + i);
      await page.evaluate(() => history.replaceState(null, '', location.href));
      await page.goBack();
      await page.goForward();
    }
    await follow('#end-1');
    await follow('#end-2');
    await page.goBack({waitUntil: 'commit'}).catch(() => {});
    await page.waitForURL(origin + '/back-earlier', {timeout: 4000});
    await context.close();
  });

  // Finding 3. A slow traversal, with a controlled clock of real delays: nothing
  // may rewrite history while the browser is still on its way.
  await run('slow traversal: history is not rewritten while it is on its way', async () => {
    const {browserBackNavigation} = await import(pathToFileURL(path.resolve(__dirname, '../static/browser-back.mjs')).href);
    const entries = [{doc: 'earlier', state: null}, {doc: 'app', state: null}, {doc: 'app', state: null}];
    let index = 2, writes = 0, slow = 0;
    const listeners = {};
    const fire = (type, event) => [...(listeners[type] || [])].forEach(f => f(event));
    const win = {
      crypto: {randomUUID: () => 'id'}, location: {href: 'app'},
      setTimeout, clearTimeout,
      addEventListener: (t, f) => { (listeners[t] ||= new Set()).add(f); },
      removeEventListener: (t, f) => { listeners[t]?.delete(f); },
      history: {
        get state() { return entries[index].state; }, get length() { return entries.length; },
        replaceState(s) { entries[index].state = s; writes++; },
        pushState(s) { entries.splice(index + 1); entries.push({doc: 'app', state: s}); index = entries.length - 1; writes++; },
        back() { win.history.go(-1); },
        go(d) {
          const to = index + d;
          if (to < 0) return;
          const wait = entries[to].doc === 'earlier' ? slow : 0;
          setTimeout(() => {
            index = to;
            if (entries[to].doc === 'app') fire('popstate', {state: entries[to].state});
          }, wait);
        },
      },
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const arm = browserBackNavigation(win);
    arm(() => false);
    await sleep(0);
    assert.equal(entries[index].state.agentnetBackSeq, 1, 'armed: marked entry, then its guard, above an unmarked older entry');
    // Back from the guard: the handler finds nothing open, so it leaves past the
    // unmarked entry; the last step, into the earlier document, takes 1500 ms.
    slow = 1500;
    const before = writes;
    win.history.back();
    await sleep(1300);
    assert.equal(writes, before, 'wrote history at ' + (writes - before) + ' while the traversal was pending');
    await sleep(400);
    assert.equal(entries[index].doc, 'earlier', 'reached the earlier document');
    assert.equal(writes, before, 'wrote history after the traversal');
  });
  await browser.close();
  console.log(JSON.stringify(results, null, 1));
  const failed = Object.values(results).filter(r => r !== 'PASS').length;
  if (failed) { console.error(failed + ' Back limit cases failed'); process.exitCode = 1; } else console.log('browser Back limits PASS');
})().catch(e => { console.error(e); process.exitCode = 1; });
