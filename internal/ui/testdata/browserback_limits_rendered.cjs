// Back when history's numbers cannot be trusted: entries the browser dropped,
// entries whose mark another writer erased, and a traversal that takes its time.
// Each case reports for itself, so one failure hides none of the others.
const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const origin = new URL(process.argv[2]).origin;
const token = new URL(process.argv[2]).searchParams.get('t');
const results = {}, limit = {};
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
    const walk = Date.now();
    await page.goBack().catch(() => {});
    await page.waitForFunction(() => calls === 1 && navigation.currentEntry.index === 1 && history.state?.agentnetBackSeq === 1, null, {timeout: 3000}).catch(async e => {
      throw new Error('the guard did not return: ' + JSON.stringify(await page.evaluate(() => ({calls, length: history.length, index: navigation.currentEntry.index, seq: history.state?.agentnetBackSeq}))));
    });
    const walked = Date.now() - walk;
    assert.ok(walked < 1000, 'walking to the oldest entry and back took ' + walked + ' ms');
    limit.capWalkMs = walked;
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

  // A traversal the browser really takes its time over, with a real click during it.
  // While it is on its way this harness can no longer evaluate in the page, so the page
  // reports for itself: each write, Back and input as a beacon, in the order they happened.
  const slowLeave = async (name, leaveTo, {reload = false, olderEngine = false, busy = false, legacyTab = false} = {}) => {
    const context = await browser.newContext(), page = await context.newPage();
    page.setDefaultTimeout(8000);
    await context.addCookies([{name: 'agentnet_ui', value: token, url: origin, httpOnly: true, sameSite: 'Strict'}]);
    let delay = 0;
    const trace = [];
    await page.route(leaveTo, async r => {
      if (delay) await new Promise(done => setTimeout(done, delay));
      await r.fulfill({contentType: 'text/html', body: '<!doctype html><title>Earlier</title><body>x</body>'});
    });
    await page.route(origin + '/back-' + name, r => r.fulfill({contentType: 'text/html', body: '<!doctype html><title>Slow fixture</title><body style="height:900px">x</body>'}));
    await page.route(origin + '/back-trace', async r => { trace.push(JSON.parse(r.request().postData())); await r.fulfill({status: 204}); });
    await page.goto(leaveTo);
    await page.goto(origin + '/back-' + name);
    // An earlier version marked this entry, and never wrote what the load saw.
    if (legacyTab) await page.evaluate(() => history.replaceState({agentnetBack: 'agentnet-back-earlier', agentnetBackSeq: 0}, '', location.href));
    const arm = () => page.evaluate(async ({olderEngine, busy}) => {
      if (olderEngine) Object.defineProperty(window, 'navigation', {value: undefined, configurable: true});
      const {browserBackNavigation} = await import('/assets/browser-back.mjs');
      let calls = 0, writes = 0, tracing = false;
      const say = what => { if (tracing) navigator.sendBeacon('/back-trace', JSON.stringify({what, calls, writes})); };
      window.startTrace = () => { tracing = true; };
      browserBackNavigation()(() => {
        calls++; say('skin');
        // A page too busy to hear from the browser for over a second.
        if (busy) setTimeout(() => { const until = performance.now() + 1500; while (performance.now() < until); }, 0);
        return false;
      });
      for (const write of ['pushState', 'replaceState']) {
        const real = history[write].bind(history);
        history[write] = (...args) => { if (tracing) writes++; say(write); return real(...args); };
      }
      addEventListener('pointerdown', () => say('input'), true);
      addEventListener('popstate', () => say('popstate'), true);
    }, {olderEngine, busy});
    // The app arms on every load: the load that saw what is behind, then the reload.
    if (reload && !legacyTab) await page.evaluate(async () => { (await import('/assets/browser-back.mjs')).browserBackNavigation()(() => false); });
    if (reload) await page.reload();
    await arm();
    if (busy) {
      // One more entry of this document, so a step of the walk stays inside it.
      await page.evaluate(() => new Promise(r => { addEventListener('hashchange', r, {once: true}); location.hash = '#late'; }));
    }
    delay = 2500;
    await page.evaluate(() => startTrace());
    const back = page.goBack({waitUntil: 'commit', timeout: 8000}).catch(() => {});
    await new Promise(r => setTimeout(r, 1300));
    await page.mouse.click(5, 5);
    await back;
    await page.waitForURL(leaveTo, {timeout: 8000});
    await new Promise(r => setTimeout(r, 200)); // the last beacons are on their way
    await context.close();
    const input = trace.findIndex(e => e.what === 'input');
    assert.ok(input >= 0, 'the click did not reach the page: ' + JSON.stringify(trace));
    return {trace, input, skin: trace.filter(e => e.what === 'skin').length, writes: trace.filter(e => e.what === 'pushState' || e.what === 'replaceState').length};
  };
  const once = r => assert.equal(r.skin, 1, 'the skin was asked again: ' + JSON.stringify(r.trace));
  const none = r => assert.equal(r.writes, 0, 'wrote history during the traversal: ' + JSON.stringify(r.trace));
  const other = 'http://elsewhere.test/before';

  await run('slow earlier page of this origin: a click during it writes nothing, and Back still leaves', async () => {
    const r = await slowLeave('slow-own', origin + '/back-earlier-own'); once(r); none(r);
  });
  await run('slow page of another origin: a click during it writes nothing, and Back still leaves', async () => {
    const r = await slowLeave('slow-other', other); once(r); none(r);
  });
  await run('a busy page: a step arriving after a second, with a click, writes nothing and runs nothing', async () => {
    const r = await slowLeave('slow-busy', origin + '/back-earlier-busy', {busy: true}); once(r); none(r);
  });

  // What the load saw, kept on the entry, holds across a reload and without the API.
  await run('after a reload: a click during a slow page of another origin writes nothing', async () => {
    const r = await slowLeave('slow-reload', other, {reload: true}); once(r); none(r); limit['after a reload'] = {order: r.trace.map(e => e.what).join(' ')};
  });
  await run('older engine: a click during a slow page writes nothing', async () => {
    const r = await slowLeave('slow-older', origin + '/back-earlier-older', {olderEngine: true}); once(r); none(r); limit['older engine'] = {order: r.trace.map(e => e.what).join(' ')};
  });
  await run('older engine, busy page: a step arriving after a second, with a click, writes nothing', async () => {
    const r = await slowLeave('slow-older-busy', origin + '/back-earlier-older-busy', {olderEngine: true, busy: true}); once(r); none(r); limit['older engine busy'] = {order: r.trace.map(e => e.what).join(' ')};
  });

  // The stated limit: an entry from before this version says nothing about what is behind
  // it. Input past a second brings the guard back, and that Back still leaves. On this
  // Chromium only: Firefox, Safari and real older engines have not run any of this.
  await run('the stated limit, a tab from before this version: a click may write the guard, and Back still leaves', async () => {
    const r = await slowLeave('slow-legacy', other, {reload: true, legacyTab: true}); once(r);
    assert.ok(r.writes === 0 || r.trace.findIndex(e => e.what === 'replaceState') > r.input, 'a write came before any input: ' + JSON.stringify(r.trace));
    assert.ok(r.writes <= 2, 'wrote more than the guard: ' + JSON.stringify(r.trace));
    limit['tab from before this version'] = {writes: r.writes};
  });
  await run('the stated limit, older engine past the cap: Back still reaches a layer after input', async () => {
    const context = await browser.newContext(), page = await context.newPage();
    page.setDefaultTimeout(5000);
    await context.addCookies([{name: 'agentnet_ui', value: token, url: origin, httpOnly: true, sameSite: 'Strict'}]);
    await page.route(origin + '/back-*', r => r.fulfill({contentType: 'text/html', body: '<!doctype html><title>Limits fixture</title><body style="height:900px">x</body>'}));
    await page.goto(origin + '/back-earlier');
    await page.goto(origin + '/back-older-cap');
    await page.evaluate(async () => {
      Object.defineProperty(window, 'navigation', {value: undefined, configurable: true});
      const {browserBackNavigation} = await import('/assets/browser-back.mjs');
      window.layers = 0; window.calls = 0;
      browserBackNavigation()(() => { calls++; if (layers) { layers--; return true; } return false; });
    });
    for (let i = 0; i < 70; i++) await page.evaluate(hash => new Promise(r => { addEventListener('hashchange', r, {once: true}); location.hash = hash; }), '#cap-' + i);
    await page.goBack().catch(() => {});
    await page.waitForFunction(() => calls === 1);
    await page.waitForTimeout(1600); // past the second this limit waits, on the entry where nothing is behind
    await page.mouse.click(5, 5);
    await page.evaluate(() => { layers = 1; });
    await page.goBack().catch(() => {});
    await page.waitForFunction(() => calls === 2 && layers === 0, null, {timeout: 3000});
    await context.close();
  });
  await browser.close();
  console.log(JSON.stringify(results, null, 1));
  console.log(JSON.stringify({limit}));
  const failed = Object.values(results).filter(r => r !== 'PASS').length;
  if (failed) { console.error(failed + ' Back limit cases failed'); process.exitCode = 1; } else console.log('browser Back limits PASS');
})().catch(e => { console.error(e); process.exitCode = 1; });
