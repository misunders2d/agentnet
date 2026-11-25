// MEL-821 in Chromium: Office is built in and what a profile that never
// chose a skin opens, with no network beyond this program. A saved choice
// (Comic included) is kept; a gallery Office moves to the built-in one with
// its theme; nothing is recorded for a profile that never chose; an older
// program's catalog without Office opens Comic. MEL-823: Office's message
// column fills a laptop's conversation pane; Comic keeps its 800px column.
//   node office_default_rendered.cjs DAEMON_URL_WITH_TOKEN
const path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
const origin = new URL(target).origin;
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  const lines = [];
  // One fresh profile per case; anything outside this program fails (offline).
  const open = async ({ storage = {}, width = 1280, height = 800, scheme = 'light', touch = false, catalog = null, url = target } = {}) => {
    const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme, hasTouch: touch, isMobile: touch });
    const outside = [];
    await ctx.route((u) => u.origin !== origin, (route) => { outside.push(route.request().url()); return route.abort(); });
    if (catalog) await ctx.route(/\/assets\/skins\/index\.json$/, async (route) => { const r = await route.fetch(); await route.fulfill({ response: r, json: catalog(await r.json()) }); });
    await ctx.addInitScript((s) => { if (!sessionStorage.getItem('seeded')) { sessionStorage.setItem('seeded', '1'); for (const [k, v] of Object.entries(s)) localStorage.setItem(k, v); } }, storage);
    const page = await ctx.newPage(), errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await page.goto(url);
    await page.waitForFunction(() => document.getElementById('skin')?.shadowRoot?.querySelector('[data-part=rail], [data-part=tabs], nav[aria-label=Main]'), null, { timeout: 15000 });
    return { ctx, page, errors, outside };
  };
  const state = (page) => page.evaluate(() => {
    const sr = document.getElementById('skin')?.shadowRoot;
    const on = [...(sr?.querySelectorAll('link[rel=stylesheet]') || [])].filter((l) => l.media !== 'not all').map((l) => new URL(l.href).pathname).filter((p) => p.startsWith('/assets/skins/'));
    return { skin: on.map((p) => p.split('/')[3]).join(','), saved: localStorage.getItem('agentnet.skin'), bar: !!document.getElementById('skin-bar'),
      theme: sr?.querySelector('.an-root')?.getAttribute('data-theme') || '', card: !!document.querySelector('.skin-card') || !!sr?.querySelector('.skin-card') };
  });
  // MEL-823: the message column's width and its pane's, with a chat open.
  const column = async (page) => {
    await page.locator('[data-part=chat-row]').filter({ hasText: 'Vitalii' }).first().click();
    await page.locator('[data-part=timeline] [data-part=bubble]').first().waitFor();
    return page.evaluate(() => {
      const tl = document.getElementById('skin').shadowRoot.querySelector('[data-part=timeline]');
      const a = tl.getBoundingClientRect(), b = tl.parentElement.getBoundingClientRect();
      const bubble = getComputedStyle(tl.querySelector('[data-part=bubble]').parentElement).maxWidth;
      return { pane: Math.round(b.width), timeline: Math.round(a.width), left: Math.round(a.left - b.left), bubble };
    });
  };
  try {
    for (const view of [{ tag: 'desk-light', width: 1280, height: 800, scheme: 'light' }, { tag: 'phone-dark', width: 390, height: 844, scheme: 'dark', touch: true }]) {
      // A new profile, offline: built-in Office, nothing saved, no host skin bar.
      const t0 = Date.now();
      const fresh = await open(view);
      let s = await state(fresh.page);
      assert.deepEqual([s.skin, s.saved, s.bar, s.card], ['office', null, false, false], view.tag + ' fresh: ' + JSON.stringify(s));
      lines.push(view.tag + ' fresh profile opens Office offline in ' + (Date.now() - t0) + ' ms; agentnet.skin stays unset; outside requests blocked: ' + fresh.outside.length);
      if (shots) await fresh.page.screenshot({ path: path.join(shots, 'office-default-' + view.tag + '.png') });
      await fresh.page.reload(); await fresh.page.waitForFunction(() => document.getElementById('skin')?.shadowRoot?.querySelector('.an-root'));
      s = await state(fresh.page);
      assert.deepEqual([s.skin, s.saved], ['office', null], view.tag + ' fresh reload');
      // MEL-823: no empty side gutters on a laptop; the phone is unchanged.
      const col = await column(fresh.page);
      assert.equal(col.timeline, col.pane, view.tag + ' Office message column fills its pane ' + JSON.stringify(col));
      lines.push(view.tag + ' Office message column ' + JSON.stringify(col));
      if (shots) await fresh.page.screenshot({ path: path.join(shots, 'office-default-' + view.tag + '-chat.png') });
      assert.deepEqual(fresh.errors, [], view.tag + ' fresh errors');
      await fresh.ctx.close();
    }
    if (process.env.OFFICE_FRESH_ONLY) { console.log(lines.join('\n')); console.log('office default rendered PASS'); return; }
    const cases = [
      ['saved Comic stays Comic', { 'agentnet.skin': 'comic', 'agentnet.skin.package': 'comic' }, ['comic', 'comic']],
      ['saved Office stays Office', { 'agentnet.skin': 'office' }, ['office', 'office']],
      ['legacy default opens Comic', { 'agentnet.skin': 'default' }, ['comic', 'comic']],
      ['gallery Office moves to built-in Office with its theme', { 'agentnet.skin': 'local:office', 'agentnet.skin.package': 'local:office', 'office.theme': 'dark' }, ['office', 'office'], 'dark'],
      ['a saved skin missing for now opens Comic and is kept', { 'agentnet.skin': 'local:pulp', 'agentnet.skin.package': 'local:pulp' }, ['comic', 'local:pulp']],
    ];
    for (const [name, storage, want, theme] of cases) {
      const c = await open({ storage });
      const s = await state(c.page);
      assert.deepEqual([s.skin, s.saved], want, name + ': ' + JSON.stringify(s));
      if (theme) assert.equal(s.theme, theme, name + ': theme kept');
      assert.equal(s.bar, false, name + ': built-in skins carry their own skin choice');
      assert.deepEqual(c.errors, [], name);
      lines.push(name + ': ' + s.skin + ', agentnet.skin=' + s.saved + (theme ? ', theme ' + s.theme : ''));
      await c.ctx.close();
    }
    { // MEL-823: Office's column stops at 1040px on a big screen; Comic keeps 800px.
      const big = await open({ width: 1920, height: 1080 });
      const col = await column(big.page);
      assert.deepEqual([col.timeline, col.left], [1040, Math.round((col.pane - 1040) / 2)], 'Office on 1920 ' + JSON.stringify(col));
      await big.ctx.close();
      const comic = await open({ storage: { 'agentnet.skin': 'comic' } });
      const c = await column(comic.page);
      assert.equal(c.timeline, 800, 'Comic keeps its 800px column at 1280 ' + JSON.stringify(c));
      lines.push('Office 1920 column ' + JSON.stringify(col) + '; Comic 1280 column ' + JSON.stringify(c));
      await comic.ctx.close();
    }
    // Choosing Comic on a new profile (Settings selectSkin = ?skin=comic) is recorded.
    const chose = await open();
    assert.equal((await state(chose.page)).skin, 'office');
    await chose.page.goto(origin + '/?skin=comic'); await chose.page.waitForFunction(() => document.getElementById('skin')?.shadowRoot?.querySelector('.an-root'));
    assert.deepEqual(Object.values(await state(chose.page)).slice(0, 2), ['comic', 'comic'], 'chosen Comic is recorded');
    await chose.page.goto(origin + '/'); await chose.page.waitForFunction(() => document.getElementById('skin')?.shadowRoot?.querySelector('.an-root'));
    assert.equal((await state(chose.page)).skin, 'comic', 'chosen Comic opens next time');
    lines.push('choosing Comic on a new profile is recorded and kept');
    await chose.ctx.close();
    // A catalog without Office (an older program serving this page's files).
    const older = await open({ catalog: (list) => list.filter((s) => s.id !== 'office') });
    assert.deepEqual(Object.values(await state(older.page)).slice(0, 2), ['comic', null], 'catalog without Office opens Comic, records nothing');
    lines.push('catalog without Office: Comic, nothing recorded');
    await older.ctx.close();
    console.log(lines.join('\n'));
    console.log('office default rendered PASS');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
