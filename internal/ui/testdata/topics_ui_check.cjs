// Topics as the messenger renders them (topics_rendered_test.go): the bar,
// the open topic's menu, All topics and the end of a done topic, at
// 1800x960 and 390x844, light and dark. Needs AGENTNET_PLAYWRIGHT;
// optional AGENTNET_SCREENSHOTS (a directory outside the repository).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.TOPICS_URL, total = Number(process.env.TOPICS_TOTAL);
const shots = process.env.AGENTNET_SCREENSHOTS || '';
// slowly: the same conversation over a slow connection (every conversation
// load takes 700 ms). Opening it never shows an empty frame: what was there
// stays, then (after 400 ms) its header with who it is and a visible sketch
// of messages, then the messages, faded in place; the card or pane moves in
// once. Switching topic keeps the last topic's messages until the next's are there.
async function slowly(browser, w, h, scheme, name) {
  name += ' slow';
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  await ctx.route(/\/api\/(thread|dm)\?/, async (r) => { await new Promise((z) => setTimeout(z, 700)); await r.continue(); });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
  const watch = (ms) => p.evaluate((ms) => {
    const sr = document.getElementById('skin').shadowRoot;
    const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth; };
    const ids = new WeakMap(); let next = 0;
    const id = (e) => (e ? (ids.get(e) ?? (ids.set(e, ++next), next)) : 0);
    const moves = (window.__moves = []);
    const onAnim = (e) => { if (/^an-(push-in|nudge-in)$/.test(e.animationName)) moves.push(e.animationName); };
    sr.addEventListener('animationstart', onAnim);
    const bar = sr.querySelector('nav[aria-label^="Topics with"]'), head = bar?.parentElement?.firstElementChild || null;
    const frames = (window.__frames = []), t0 = performance.now();
    const look = () => {
      const logs = [...sr.querySelectorAll('[role="log"]')].filter(vis);
      const busy = [...sr.querySelectorAll('[aria-busy="true"]')].filter((b) => b.tagName === 'SECTION' && vis(b));
      // A sketch bubble is visible: outlined, and filled unlike the page under it.
      const sketch = busy.flatMap((b) => [...b.querySelectorAll('[data-skeleton]')].filter((e) => {
        const c = getComputedStyle(e);
        return vis(e) && parseFloat(c.borderTopWidth) >= 1 && !/, 0\)$/.test(c.borderTopColor) && c.backgroundColor !== getComputedStyle(b).backgroundColor && +c.opacity >= 0.5;
      })).length;
      frames.push({ t: Math.round(performance.now() - t0), busy: busy.length, sketch, title: busy.map((b) => b.querySelector('header')?.textContent || '').join('|'),
        logs: logs.length, msgs: Math.max(0, ...logs.map((l) => l.querySelectorAll('[data-mid]').length)),
        list: [...sr.querySelectorAll('section[aria-label="Chats"]')].some(vis),
        pick: [...sr.querySelectorAll('main p')].some((e) => e.textContent === 'Pick a chat' && vis(e)),
        card: id(sr.querySelector('[data-card]:not([aria-hidden])')),
        bar: !!bar && bar.isConnected && vis(bar) && sr.querySelector('nav[aria-label^="Topics with"]') === bar, head: !!head && head.isConnected });
      if (performance.now() - t0 < ms) requestAnimationFrame(look); else sr.removeEventListener('animationstart', onAnim);
    };
    requestAnimationFrame(look);
  }, ms);
  const frames = async (ms) => { await p.waitForTimeout(ms + 50); return p.evaluate(() => ({ frames: window.__frames, moves: window.__moves })); };

  await watch(1500);
  await p.getByRole('button', { name: /no person linked/ }).first().click();
  const { frames: opened, moves } = await frames(1500);
  const shownAt = opened.findIndex((f) => f.msgs > 0);
  assert.ok(shownAt >= 0, name + ': the conversation opened: ' + JSON.stringify(opened.slice(-3)));
  assert.ok(opened.some((f) => f.busy), name + ': a 700 ms load shows the opening header and sketch');
  for (const f of opened) {
    const sketched = f.busy && f.sketch >= 3 && /\S/.test(f.title);
    assert.ok((w >= 1000 ? f.pick : f.list) || f.msgs > 0 || sketched, name + ': open chat: a frame with nothing (or nothing visible) in it at ' + f.t + ' ms: ' + JSON.stringify(f));
  }
  assert.ok(opened.slice(shownAt).every((f) => f.msgs > 0), name + ': open chat: the messages never went away: ' + JSON.stringify(opened));
  assert.equal(moves.length, 1, name + ': the conversation moved in once: ' + JSON.stringify(moves));
  if (w < 1000) {
    const cards = new Set(opened.map((f) => f.card).filter(Boolean));
    assert.equal(cards.size, 1, name + ': one card from the placeholder to the messages: ' + JSON.stringify([...cards]));
  }

  // A topic switch over the same slow connection.
  const bar = p.locator('nav[aria-label^="Topics with"]');
  await bar.waitFor();
  await p.waitForTimeout(400);
  const other = bar.locator('button[data-topic]:not([aria-current])').first();
  if (await other.count()) {
    const chips = () => p.evaluate(() => [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"] [data-topic]')].map((c) => c.getAttribute('data-topic')));
    const order = await chips(), chosen = await other.getAttribute('data-topic');
    await watch(1200);
    await other.click();
    const { frames: switched } = await frames(1200);
    for (const f of switched) {
      assert.ok(f.bar && f.head && f.msgs > 0 && !f.busy, name + ': topic switch: a blank or redrawn frame at ' + f.t + ' ms: ' + JSON.stringify(f));
    }
    // The bar keeps its order: the chosen chip stays where it was clicked,
    // and chips that stay keep their places.
    const after = await chips();
    assert.equal(after.indexOf(chosen), order.indexOf(chosen), name + ': the chosen chip stays in its slot: ' + JSON.stringify([order, after]));
    const kept = order.filter((id) => after.includes(id));
    assert.deepEqual(after.filter((id) => kept.includes(id)), kept, name + ': chips that stay keep their order');
  }
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    let run = 0;
    for (const [w, h] of [[1800, 960], [390, 844]]) for (const scheme of ['light', 'dark']) {
      const aisle = ++run, title = 'Count the bath sets in aisle ' + aisle; // each run finishes with its own task reopened
      const name = w + 'x' + h + '-' + scheme;
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
      const p = await ctx.newPage();
      const errors = [];
      p.on('pageerror', (e) => errors.push(String(e)));
      await p.goto(url);
      await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
      const fits = async (what) => {
        const o = await p.evaluate(() => ({ page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          bar: [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"]')].map((n) => n.scrollWidth - n.clientWidth) }));
        assert.ok(o.page <= 0 && o.bar.every((x) => x <= 1), name + ' ' + what + ': nothing scrolls sideways ' + JSON.stringify(o));
      };
      const snap = async (what) => { if (shots) await p.screenshot({ path: shots + '/topics-' + name + '-' + what + '.png' }); };
      // Frames: from just before an action, every animation frame records what
      // is on screen: opening placeholders, the most messages any visible
      // timeline shows, and whether the conversation's header and topic bar
      // are still the very elements they were (not drawn again).
      const watch = () => p.evaluate(() => {
        const sr = document.getElementById('skin').shadowRoot;
        const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth; };
        const bar = sr.querySelector('nav[aria-label^="Topics with"]'), head = bar?.parentElement?.firstElementChild || null;
        const frames = (window.__frames = []), t0 = performance.now();
        const look = () => {
          const logs = [...sr.querySelectorAll('[role="log"]')].filter(vis);
          frames.push({ t: Math.round(performance.now() - t0), busy: [...sr.querySelectorAll('[aria-busy="true"]')].filter(vis).length,
            logs: logs.length, msgs: Math.max(0, ...logs.map((l) => l.querySelectorAll('[data-mid]').length)),
            list: [...sr.querySelectorAll('section[aria-label="Chats"]')].some(vis),
            pick: [...sr.querySelectorAll('main p')].some((e) => e.textContent === 'Pick a chat' && vis(e)),
            bar: !!bar && bar.isConnected && vis(bar) && sr.querySelector('nav[aria-label^="Topics with"]') === bar, head: !!head && head.isConnected });
          if (performance.now() - t0 < 900) requestAnimationFrame(look);
        };
        requestAnimationFrame(look);
      });
      const frames = async () => { await p.waitForTimeout(950); return p.evaluate(() => window.__frames); };

      // Opening a conversation never shows an empty frame: the list (or what
      // the pane showed) stays until its messages are there; only a slow load
      // (400 ms) may show the opening placeholder.
      await watch();
      await p.getByRole('button', { name: /no person linked/ }).first().click();
      const opened = await frames();
      const shownAt = opened.findIndex((f) => f.msgs > 0);
      assert.ok(shownAt >= 0, name + ': the conversation opened: ' + JSON.stringify(opened.slice(-3)));
      for (const f of opened) {
        assert.ok((w >= 1000 ? f.pick : f.list) || f.msgs > 0 || (f.busy && f.t >= 350), name + ': open chat: a frame with nothing in it at ' + f.t + ' ms: ' + JSON.stringify(f));
      }
      assert.ok(opened.slice(shownAt).every((f) => f.msgs > 0), name + ': open chat: the messages never went away: ' + JSON.stringify(opened));
      const bar = p.locator('nav[aria-label^="Topics with"]');
      await bar.waitFor();
      await fits('bar');
      await snap('bar');
      const chips = await bar.locator(':scope > button, :scope > [aria-haspopup="menu"]').count();
      assert.ok(chips >= 2 && chips <= 7, name + ': at most six chips and New topic, got ' + chips); // topics + All topics + New topic
      await bar.getByRole('button', { name: new RegExp('^All topics \\(' + total + '\\)') }).waitFor();
      if (w >= 1000) {
        assert.ok(await bar.getByRole('button', { name: /^May I move the Savannah order.*Needs you/ }).count() === 1, name + ': the question waiting for you has a chip saying so');
      } else {
        // A phone: one topic chip (the open one) keeps room for its name
        // beside "All N"; the questions held for the person that are not in
        // the bar mark the All topics chip, and its label says how many.
        const open = await bar.locator('[aria-current="true"]').boundingBox();
        assert.ok(open && open.width >= 180, name + ': the open topic chip keeps room for its name: ' + JSON.stringify(open));
        const label = await bar.getByRole('button', { name: /^All topics/ }).getAttribute('aria-label');
        const openNeeds = /Needs you/.test(await bar.locator('[aria-current="true"]').getAttribute('aria-label')); // the newest topic opens first: it may be one of the four questions
        assert.match(label, new RegExp('\\b' + (4 - openNeeds) + ' need you\\b'), name + ': the All topics chip says what needs you: ' + label);
        assert.match(await bar.getByRole('button', { name: /^All topics/ }).innerText(), new RegExp('^All ' + total + '$'), name + ': the short phone label');
      }
      // All topics: opened from the keyboard, focus in its search; lists, filters, searches, opens.
      await bar.getByRole('button', { name: /^All topics/ }).focus();
      await p.keyboard.press('Enter');
      const all = p.getByRole('dialog', { name: 'All topics' });
      await all.getByRole('list', { name: 'Active topics' }).waitFor();
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('type') === 'search');
      await fits('all topics');
      assert.ok(await all.getByRole('button', { name: /^Active/ }).getAttribute('aria-pressed') === 'true', name + ': Active is the first filter');
      await all.getByRole('searchbox').fill('NOTE 17:');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.querySelectorAll('[role="dialog"] ul li').length === 1, null, { timeout: 5000 });
      await snap('all-search');
      await all.getByRole('searchbox').fill('');
      await all.getByRole('button', { name: /^Done/ }).click();
      await all.getByRole('listitem').filter({ hasText: title }).waitFor();
      const done = all.getByRole('listitem').filter({ hasText: title });
      assert.equal(await all.getByRole('listitem').count(), 5 - aisle, name + ': the finished tasks not reopened yet are done');
      // The results were written by hand here (Reply): the person's words, never the agent's.
      assert.match(await done.first().innerText(), /41. bath sets/);
      // Switching topic swaps only the messages: the header and the topic bar
      // stay the same elements, and every frame shows messages (the last
      // topic's until the next one's are there), never a placeholder.
      await watch();
      await done.first().getByRole('button').click();
      const switched = await frames();
      for (const f of switched) {
        assert.ok(f.bar && f.head && f.msgs > 0 && !f.busy, name + ': topic switch: a blank or redrawn frame at ' + f.t + ' ms: ' + JSON.stringify(f));
      }
      await p.waitForSelector('section[aria-label="Topic state"]');
      const end = await p.locator('section[aria-label="Topic state"]').innerText();
      assert.match(end, /done/i); assert.doesNotMatch(end, /Your answer|conclusion/i);
      await fits('done topic');
      await snap('done');

      // The open topic's menu from the keyboard: Enter opens it on its first
      // item, Escape closes it back onto the chip.
      await bar.locator('[aria-current="true"]').focus();
      await p.keyboard.press('Enter');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('role') === 'menuitem' && /Rename/.test(document.getElementById('skin').shadowRoot.activeElement.textContent));
      await p.keyboard.press('Escape');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('aria-current') === 'true');
      // Reopen, then Mark done, then Rename.
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Reopen' }).click();
      await p.waitForFunction(() => !document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]'));
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Mark done' }).click();
      await p.waitForFunction(() => /marked it done/.test(document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]')?.textContent || ''));
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Rename…' }).click();
      await p.getByRole('textbox', { name: 'Name' }).fill('Aisle 4 count ' + name);
      await snap('rename');
      await p.getByRole('button', { name: 'Save' }).click();
      await p.waitForFunction((t) => document.getElementById('skin').shadowRoot.querySelector('nav[aria-label^="Topics with"] [aria-current="true"]')?.getAttribute('aria-label')?.startsWith(t), 'Aisle 4 count ' + name);
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Rename…' }).click();
      await p.getByRole('button', { name: 'Use its first message' }).click();
      await p.waitForFunction((t) => document.getElementById('skin').shadowRoot.querySelector('nav[aria-label^="Topics with"] [aria-current="true"]')?.getAttribute('aria-label')?.startsWith(t), title);
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Reopen' }).click();
      await p.waitForFunction(() => !document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]'));
      await fits('after changes');
      // Every button in the bar and the dialog has a name.
      const unnamed = await p.evaluate(() => [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"] button')].filter((b) => !(b.getAttribute('aria-label') || b.textContent.trim())).length);
      assert.equal(unnamed, 0, name + ': unnamed controls');
      assert.deepEqual(errors, [], name + ': page errors');
      await ctx.close();
      await slowly(browser, w, h, scheme, name);
      console.log('ok', name);
    }
    console.log('topics ui check PASS');
  } finally { await browser.close(); }
})().catch((e) => { console.error(e); process.exit(1); });
