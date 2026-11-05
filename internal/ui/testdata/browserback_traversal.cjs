// The Back guard's traversal contract, with real delays and a modelled browser:
// a Back this page began ends only on an event from the browser, however late, so
// neither a slow traversal nor input during it can rewrite history or count as a new
// Back. Real Chromium covers what it can reach in browserback_limits_rendered.cjs;
// a late event from a loaded browser and a cancelled traversal cannot be forced there.
const assert = require('node:assert/strict');
const path = require('node:path');
const {pathToFileURL} = require('node:url');
const sleep = ms => new Promise(r => setTimeout(r, ms));

// entries: {doc, origin?, state?}. Only entries of doc 'app' are this document; landing
// on any other leaves it. slow(entry) is the delay of the traversal landing on it.
const world = ({entries, index, api = false, activation = 'push', refuse = false, loaded, slow = () => 0, droppedStillListed = 0}) => {
  const listeners = {}, log = {writes: 0, backs: 0, left: null};
  let nextKey = 0;
  const fire = (type, event) => [...(listeners[type] || [])].forEach(f => f(event));
  const land = to => setTimeout(() => {
    if (log.left) return;
    w.index = to;
    if (entries[to].doc === 'app') fire('popstate', {state: entries[to].state ?? null});
    else log.left = entries[to].doc;
  }, slow(entries[to]));
  const w = {
    index, entries, log,
    crypto: {randomUUID: () => 'id'}, location: {href: 'app'},
    setTimeout, clearTimeout,
    ...(loaded ? {performance: {getEntriesByType: () => [{type: loaded}]}} : {}),
    addEventListener: (t, f) => { (listeners[t] ||= new Set()).add(f); },
    removeEventListener: (t, f) => { listeners[t]?.delete(f); },
    fire,
    inputs: () => (listeners.pointerdown?.size || 0) + (listeners.keydown?.size || 0),
    history: {
      get state() { return entries[w.index].state ?? null; },
      get length() { return entries.length; },
      replaceState(s) { entries[w.index].state = s; log.writes++; },
      pushState(s) { entries.splice(w.index + 1); entries.push({doc: 'app', state: s}); w.index = entries.length - 1; log.writes++; },
      back() { log.backs++; if (w.index > 0) land(w.index - 1); },
    },
  };
  if (api) {
    // Only entries of this origin that touch the current one, as the HTML standard lists them.
    const here = () => {
      let from = w.index, to = w.index;
      while (from > 0 && !entries[from - 1].origin) from--;
      while (to < entries.length - 1 && !entries[to + 1].origin) to++;
      return {from, to};
    };
    w.navigation = {
      get currentEntry() { return {index: droppedStillListed + w.index - here().from}; },
      entries() {
        const {from, to} = here();
        // Firefox past its cap: going to one of these raises nothing and goes nowhere.
        return [...Array.from({length: droppedStillListed}, () => ({key: 'dropped', sameDocument: true})), ...entries.slice(from, to + 1).map(e => { e.key ??= 'key' + nextKey++; e.sameDocument = e.doc === 'app'; return e; })];
      },
      activation: {navigationType: activation},
      traverseTo(key) {
        log.backs++;
        if (refuse) { const failed = Promise.reject(new Error('AbortError')); return {committed: failed, finished: failed}; }
        if (key !== 'dropped') land(entries.findIndex(e => e.key === key));
        return {committed: Promise.resolve(), finished: Promise.resolve()};
      },
    };
  }
  return w;
};

// [earlier, older app entry, the entry the guard sits on]: leaving walks through all of them.
const app = () => ({doc: 'app'}), earlier = () => ({doc: 'earlier'}), outside = () => ({doc: 'outside', origin: 'other'});

(async () => {
  const {browserBackNavigation} = await import(pathToFileURL(path.resolve(__dirname, '../static/browser-back.mjs')).href);
  const results = {};
  const run = async (name, fn) => {
    try { await fn(); results[name] = 'PASS'; } catch (e) { results[name] = 'FAIL: ' + String(e.message).split('\n')[0].slice(0, 170); }
  };
  // Armed on the newest entry, and Back pressed from its guard with nothing open.
  const armed = opts => {
    const w = world(opts), seen = {calls: 0, layers: 0};
    const arm = browserBackNavigation(w);
    const off = arm(() => { seen.calls++; if (seen.layers) { seen.layers--; return true; } return false; });
    return {w, seen, off, arm};
  };
  const press = w => { w.history.back(); };
  const input = w => w.fire('pointerdown', {});
  const settle = () => sleep(30);

  for (const api of [false, true]) {
    const engine = api ? 'navigation API' : 'older engine';
    await run(engine + ': a slow last step writes nothing, input or not', async () => {
      for (const click of [false, true]) {
        if (click && !api) continue; // an older engine cannot tell this from nothing behind: see below
        const {w, seen} = armed({entries: [earlier(), app()], index: 1, api, slow: e => e.doc === 'earlier' ? 1500 : 0});
        const before = w.log.writes;
        press(w);
        await sleep(1200);
        if (click) input(w);
        await sleep(100);
        assert.equal(w.log.writes, before, 'wrote history while the traversal was on its way');
        await sleep(400);
        assert.equal(w.log.left, 'earlier', 'reached the earlier document');
        assert.equal(w.log.writes, before, 'wrote history after the traversal');
        assert.equal(seen.calls, 1, 'one Back asks the skin once');
      }
    });

    await run(engine + ': a popstate after a second is still the same Back', async () => {
      // The step between the app's own entries arrives late, as on a busy page.
      const entries = [earlier(), app(), app(), app()];
      const {w, seen} = armed({entries, index: 3, api, slow: e => e === entries[1] ? 1500 : 0});
      w.entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 0};
      w.entries[2].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 1};
      const before = w.log.writes;
      press(w);
      await sleep(1800);
      assert.equal(seen.calls, 1, 'the late step ran the skin as a new Back');
      assert.equal(w.log.left, 'earlier', 'the Back still leaves');
      assert.equal(w.log.writes, before, 'wrote history during the walk');
    });
  }

  // An engine without the API still knows what its own load saw, and keeps it on the entry.
  await run('older engine, freshly loaded: a slow last step with input writes nothing', async () => {
    const {w, seen} = armed({entries: [earlier(), app()], index: 1, loaded: 'navigate', slow: e => e.doc === 'earlier' ? 1500 : 0});
    const before = w.log.writes;
    press(w);
    await sleep(1200);
    input(w);
    await sleep(100);
    assert.equal(w.log.writes, before, 'wrote history while the traversal was on its way');
    await sleep(400);
    assert.equal(w.log.left, 'earlier');
    assert.equal(seen.calls, 1);
  });

  await run('older engine, freshly loaded: a late step inside the document with input writes nothing and runs nothing', async () => {
    const entries = [earlier(), app(), app()];
    const {w, seen} = armed({entries, index: 2, loaded: 'navigate', slow: e => e === entries[1] ? 1500 : 0});
    const before = w.log.writes;
    // The guard above, then one more of this document's own entries.
    w.history.pushState({agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 2}, '', 'app');
    const pushed = w.log.writes;
    press(w);
    await sleep(100);
    press(w);
    await sleep(1100);
    input(w);
    await sleep(600);
    assert.equal(w.log.writes, pushed, 'wrote history during the walk, from ' + before);
    assert.equal(seen.calls, 1, 'the late step ran the skin as a new Back');
    assert.equal(w.log.left, 'earlier');
  });

  await run('older engine, directly opened and freshly loaded: nothing behind, so Back works at once', async () => {
    const entries = [app()];
    const {w, seen} = armed({entries, index: 0, loaded: 'navigate'});
    press(w);
    await settle();
    assert.equal(seen.calls, 1);
    assert.equal(w.history.state.agentnetBackSeq, 1, 'the guard returned without input');
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0);
  });

  await run('a reload keeps what the load saw: another origin behind stays a certain traversal', async () => {
    // Stamped by the load before: the reload itself says nothing about either side.
    const entries = [outside(), app()];
    const {w} = armed({entries, index: 1, api: true, activation: 'push'});
    const mark = w.entries[1].state;
    assert.equal(mark.agentnetBackBefore, 'other');
    const again = world({entries: [outside(), {doc: 'app', state: mark}], index: 1, api: true, activation: 'reload', slow: e => e.origin ? 1500 : 0});
    const seen = {calls: 0};
    browserBackNavigation(again)(() => { seen.calls++; return false; });
    const before = again.log.writes;
    again.history.back();
    await sleep(1200);
    input(again);
    await sleep(100);
    assert.equal(again.log.writes, before, 'wrote history during the traversal');
    await sleep(400);
    assert.equal(again.log.left, 'outside');
    assert.equal(seen.calls, 1);
  });

  await run('navigation API: a late popstate with input between writes nothing and runs nothing', async () => {
    const entries = [earlier(), app(), app(), app()];
    const {w, seen} = armed({entries, index: 3, api: true, slow: e => e === entries[1] ? 1500 : 0});
    w.entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 0};
    w.entries[2].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 1};
    const before = w.log.writes;
    press(w);
    await sleep(1200);
    input(w);
    await sleep(100);
    assert.equal(w.log.writes, before, 'input wrote history while the traversal was on its way');
    await sleep(400);
    assert.equal(seen.calls, 1, 'the late popstate ran the skin as a new Back');
    assert.equal(w.log.left, 'earlier', 'the Back still leaves');
    assert.equal(w.log.writes, before);
  });

  await run('navigation API: another origin behind a pushed page is a certain traversal', async () => {
    const {w, seen} = armed({entries: [outside(), app()], index: 1, api: true, slow: e => e.origin ? 2200 : 0});
    const before = w.log.writes;
    press(w);
    await sleep(1800);
    input(w);
    await sleep(100);
    assert.equal(w.log.writes, before, 'input wrote history while leaving for another origin');
    await sleep(500);
    assert.equal(w.log.left, 'outside');
    assert.equal(seen.calls, 1);
  });

  await run('navigation API: entries the browser dropped but still lists are never gone to', async () => {
    const entries = [app(), app(), app()];
    const {w, seen} = armed({entries, index: 2, api: true, droppedStillListed: 3});
    entries[0].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 5};
    entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 6};
    press(w);
    await settle();
    await settle();
    assert.equal(seen.calls, 1);
    assert.equal(w.history.state.agentnetBackSeq, 1, 'the guard did not return above the oldest real entry');
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0, 'the next Back did not reach the layer');
  });

  await run('navigation API: leaving goes straight to the entry before this document, not one past it', async () => {
    const entries = [earlier(), earlier(), app(), app(), app()];
    const {w} = armed({entries, index: 4, api: true});
    entries[2].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 0};
    entries[3].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 1};
    press(w);
    await settle();
    await settle();
    assert.equal(w.log.left, 'earlier');
    assert.equal(w.index, 1, 'skipped the document just before this one: index ' + w.index);
    assert.equal(w.log.backs, 2, 'took ' + (w.log.backs - 1) + ' hops after the press');
  });

  await run('navigation API: nothing older: no traversal is begun and Back works at once', async () => {
    const entries = [app(), app(), app()];
    const {w, seen} = armed({entries, index: 2, api: true});
    entries[0].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 5};
    entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 6};
    press(w);
    await settle();
    await settle();
    const backs = w.log.backs;
    assert.equal(w.index, 1, 'the guard is back above the oldest entry: index ' + w.index);
    assert.equal(w.history.state.agentnetBackSeq, 1);
    await sleep(1100);
    assert.equal(w.log.backs, backs, 'asked to go back past the first entry');
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0, 'Back reached the layer, no input needed');
    assert.equal(w.inputs(), 0, 'no input listener is left');
  });

  await run('navigation API: a refused traversal hands Back to the guard at the next input', async () => {
    const {w, seen} = armed({entries: [earlier(), app()], index: 1, api: true, refuse: true});
    const before = w.log.writes;
    // The guard's Back itself is the browser's; only the walk that follows is refused.
    w.index = 1;
    w.fire('popstate', {state: w.entries[1].state});
    await settle();
    assert.equal(seen.calls, 1);
    assert.equal(w.log.writes, before, 'a refusal writes nothing by itself');
    input(w);
    assert.equal(w.history.state.agentnetBackSeq, 1, 'the guard returned with the input');
    assert.equal(w.inputs(), 0);
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0);
  });

  await run('older engine: nothing behind: the guard returns only with input', async () => {
    const entries = [app(), app()];
    const {w, seen} = armed({entries, index: 1});
    entries[0].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 5};
    const before = w.log.writes;
    press(w);
    await sleep(1400);
    assert.equal(seen.calls, 1);
    assert.equal(w.log.writes, before, 'time alone wrote history');
    input(w);
    assert.equal(w.history.state.agentnetBackSeq, 1, 'the guard returned with the input');
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0, 'an ordinary Back after the guard returned reaches the layer');
  });

  await run('the stated limit, no load seen: a traversal over a second with input writes, and is still that Back', async () => {
    const entries = [earlier(), app(), app(), app()];
    const {w, seen} = armed({entries, index: 3, slow: e => e === entries[1] ? 1500 : 0});
    w.entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 0};
    w.entries[2].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 1};
    press(w);
    await sleep(1200);
    input(w); // nothing separates this from nothing behind, so the guard does return
    await sleep(600);
    assert.equal(seen.calls, 1, 'the late popstate ran the skin as a new Back');
    assert.equal(w.log.left, 'earlier', 'the Back still leaves');
  });

  await run('repeated Back during a traversal asks the skin once and writes nothing', async () => {
    for (const api of [false, true]) {
      const entries = [earlier(), app(), app(), app()];
      const {w, seen} = armed({entries, index: 3, api, slow: e => e === entries[1] ? 500 : 0});
      w.entries[1].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 0};
      w.entries[2].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 1};
      const before = w.log.writes;
      press(w);
      await sleep(100);
      press(w);
      await sleep(900);
      assert.equal(seen.calls, 1);
      assert.equal(w.log.writes, before);
      assert.equal(w.log.left, 'earlier');
    }
  });

  await run('unmounting during a traversal leaves no listener and writes nothing', async () => {
    for (const api of [false, true]) {
      const {w, off} = armed({entries: [earlier(), app()], index: 1, api, slow: e => e.doc === 'earlier' ? 1500 : 0});
      const before = w.log.writes;
      press(w);
      await sleep(100);
      off();
      await sleep(1100);
      assert.equal(w.inputs(), 0, 'an input listener outlived the skin');
      input(w);
      assert.equal(w.log.writes, before);
      await sleep(400);
      assert.equal(w.log.left, 'earlier');
    }
  });

  await run('a skin arriving during a traversal writes nothing', async () => {
    const {w, off, arm} = armed({entries: [earlier(), app()], index: 1, api: true, slow: e => e.doc === 'earlier' ? 800 : 0});
    const before = w.log.writes;
    press(w);
    await sleep(100);
    off();
    arm(() => false);
    await sleep(900);
    assert.equal(w.log.writes, before);
    assert.equal(w.log.left, 'earlier');
  });

  await run('a return from the page cache onto the base entry still reaches a layer', async () => {
    const entries = [earlier(), app()];
    const {w, seen} = armed({entries, index: 1, api: true});
    press(w);
    await settle();
    assert.equal(w.log.left, 'earlier');
    // Forward from there restores this page, as it was, on the entry below its guard.
    w.log.left = null;
    w.index = 1;
    w.fire('pageshow', {persisted: true});
    assert.equal(w.history.state.agentnetBackSeq, 1, 'no guard above the base entry');
    seen.layers = 1;
    press(w);
    await settle();
    assert.equal(seen.layers, 0, 'Back left the page with a layer open');
    assert.equal(w.log.left, null);
  });

  await run('pageshow during a wait drops the listeners and the wait', async () => {
    const entries = [app(), app()];
    const {w} = armed({entries, index: 1});
    entries[0].state = {agentnetBack: w.history.state.agentnetBack, agentnetBackSeq: 5};
    const before = w.log.writes;
    press(w);
    await sleep(1100);
    w.fire('pageshow', {});
    assert.equal(w.inputs(), 0, 'an input listener outlived the wait');
    input(w);
    assert.equal(w.log.writes, before);
  });

  console.log(JSON.stringify(results, null, 1));
  const failed = Object.values(results).filter(r => r !== 'PASS').length;
  if (failed) { console.error(failed + ' traversal cases failed'); process.exitCode = 1; } else console.log('browser Back traversal PASS');
  process.exit();
})().catch(e => { console.error(e); process.exit(1); });
