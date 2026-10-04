// Topics as the messenger renders them (topics_rendered_test.go): the bar,
// the open topic's menu, All topics and the end of a done topic, at
// 1800x960 and 390x844, light and dark. Needs AGENTNET_PLAYWRIGHT;
// optional AGENTNET_SCREENSHOTS (a directory outside the repository).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.TOPICS_URL, total = Number(process.env.TOPICS_TOTAL);
const shots = process.env.AGENTNET_SCREENSHOTS || '';
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
      assert.match(await done.first().innerText(), /You: 41. bath sets/);
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
      assert.match(end, /done/i); assert.match(end, new RegExp('Your answer: ' + (410 + aisle) + ' bath sets')); assert.doesNotMatch(end, /conclusion/);
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
      console.log('ok', name);
    }
    console.log('topics ui check PASS');
  } finally { await browser.close(); }
})().catch((e) => { console.error(e); process.exit(1); });
