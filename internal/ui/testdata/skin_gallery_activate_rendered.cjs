// MEL-819 on the production host (loader.js), GitHub replaced by a local fake:
// Comic's real package published in the gallery as "Pulp" is installed,
// trusted and selected; the next open's gallery check stores a newer
// version, which opens in place with Pulp still selected:
//   - while a field has focus, a dialog is open or a write is under way,
//     the running version stays; typing stays immediate;
//   - once that ends, the new version mounts with the same chat open, its
//     draft text and its attached file kept, without a switch or a reload;
//   - bad bytes, a version needing a newer AgentNet, an untrusted digest
//     and a version that fails to mount leave the running one usable.
// Desktop light 1440 and Android-width dark 412.
//   node skin_gallery_activate_rendered.cjs DAEMON_URL_WITH_TOKEN
const fs = require('fs'), path = require('path'), crypto = require('crypto'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
const LATEST = 'https://api.github.com/repos/misunders2d/agentnet-skins/releases/latest';
const RAW = 'https://raw.githubusercontent.com/misunders2d/agentnet-skins/';
const sha = (b) => crypto.createHash('sha256').update(b).digest();
let lastPage = null;
const comicDir = path.join(__dirname, '..', 'static', 'skins', 'comic');

// Comic's own package as the gallery skin "pulp"; mark appends to its style
// so the version that is applied can be read from the page.
function comicAs(mark, agentnet = 'v0.8.25') {
  const m = JSON.parse(fs.readFileSync(path.join(comicDir, 'skin.json')));
  const files = { 'skin.json': Buffer.from(JSON.stringify({ ...m, id: 'pulp', name: 'Pulp', agentnet })) };
  for (const f of m.files) files[f] = fs.readFileSync(path.join(comicDir, f));
  files['style.css'] = Buffer.concat([files['style.css'], Buffer.from('\n.an-root{--pulp-build:"' + mark + '"}\n')]);
  return files;
}
// A small package for the failure cases.
function tiny(word, { agentnet = 'v0.8.25', throws = false } = {}) {
  const m = { api: 1, id: 'pulp', name: 'Pulp', entry: 'entry.mjs', style: 'style.css', files: ['entry.mjs', 'style.css'], agentnet };
  return {
    'skin.json': Buffer.from(JSON.stringify(m)),
    'entry.mjs': Buffer.from(throws ? 'export function mount(){throw new Error("broken")}'
      : `export function mount(root,host){const h=document.createElement('h1');h.textContent=${JSON.stringify(word)};root.append(h);host.onOpen(()=>{});}export function unmount(root){root.replaceChildren();}`),
    'style.css': Buffer.from('h1 { font: 700 32px serif; } /* ' + word + ' */'),
  };
}
function digest(files) {
  const m = JSON.parse(files['skin.json']);
  return crypto.createHash('sha256').update(Buffer.concat([files['skin.json'], ...m.files.flatMap((n) => [Buffer.from(n), Buffer.from([0]), sha(files[n])])])).digest('hex');
}
const entry = (files, version, agentnet = 'v0.8.25') => ({ id: 'pulp', name: 'Pulp', description: 'Test.', version, api: 1, agentnet, digest: digest(files),
  files: Object.entries(files).map(([p, b]) => ({ path: p, sha256: sha(b).toString('hex'), size: b.length })), previews: [] });

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  const origin = new URL(target).origin;
  const until = async (ok, what, ms = 20000) => { for (const end = Date.now() + ms; !(await ok());) { if (Date.now() > end) assert.fail(what); await new Promise((r) => setTimeout(r, 40)); } };
  try {
    // One context per run: a fresh browser profile, the gallery faked.
    const run = async ({ width, height, scheme }, releases) => {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme, hasTouch: width < 600, isMobile: width < 600 });
      const page = await ctx.newPage(), errors = []; lastPage = page;
      const state = { published: 'v1', hold: null, tamper: false, down: false };
      page.on('pageerror', (e) => errors.push(e.message));
      await ctx.route(LATEST, async (route) => {
        if (state.hold) await state.hold;
        if (state.down) return route.fulfill({ status: 503, headers: { 'Access-Control-Allow-Origin': '*' }, body: '' });
        return route.fulfill({ status: 200, contentType: 'application/json', headers: { 'Access-Control-Allow-Origin': '*' }, body: JSON.stringify({ tag_name: state.published, draft: false, prerelease: false }) });
      });
      await ctx.route(RAW + '**', (route) => {
        const cors = { 'Access-Control-Allow-Origin': '*' }, m = route.request().url().slice(RAW.length).match(/^(v[0-9]+)\/published\/(.+)$/);
        if (!m || !releases[m[1]]) return route.fulfill({ status: 404, headers: cors, body: '' });
        const [cat, files] = releases[m[1]];
        if (m[2] === 'catalog.json') return route.fulfill({ status: 200, headers: cors, contentType: 'text/plain', body: JSON.stringify(cat) });
        let body = files[m[2].slice('pulp/'.length)];
        if (!body) return route.fulfill({ status: 404, headers: cors, body: '' });
        if (state.tamper && m[2].endsWith('entry.mjs')) body = Buffer.concat([body, Buffer.from('\n// changed')]);
        return route.fulfill({ status: 200, headers: cors, contentType: 'text/plain', body });
      });
      await ctx.route(/\/api\/overview/, async (route) => { const r = await route.fetch(); const j = await r.json(); j.version = 'v0.8.25'; await route.fulfill({ response: r, json: j }); });
      // Install v1 by the gallery's own verified path, trust it and choose it.
      await page.goto(target);
      await page.getByRole('navigation', { name: 'Main' }).waitFor({ timeout: 20000 });
      const d1 = digest(releases.v1[1]);
      await page.evaluate(async (d) => {
        const { gallery } = await import('/assets/skin-gallery.mjs');
        const id = await gallery({ running: () => 'v0.8.25' }).install('pulp');
        localStorage.setItem('agentnet.skin.trusted.' + id, d);
        localStorage.setItem('agentnet.skin', id);
      }, d1);
      return { ctx, page, errors, state };
    };
    const applied = (page) => page.evaluate(() => {
      const sr = document.getElementById('skin')?.shadowRoot, root = sr?.querySelector('.an-root');
      const sheets = [...(sr?.querySelectorAll('link[href*="/local-skins/"]') || [])].map((l) => ({ href: l.getAttribute('href'), on: l.media !== 'not all' }));
      return { build: root ? getComputedStyle(root).getPropertyValue('--pulp-build').trim() : '', sheets, h1: sr?.querySelector('h1')?.textContent || '', card: !!sr?.querySelector('.skin-card') || !!document.querySelector('.skin-card') };
    });

    for (const view of [{ tag: 'desk-light', width: 1440, height: 900, scheme: 'light' }, { tag: 'android-dark', width: 412, height: 900, scheme: 'dark' }]) {
      const v1 = comicAs('v1'), v2 = comicAs('v2');
      const releases = { v1: [{ catalog: 1, skins: [entry(v1, '1.0.0')] }, v1], v2: [{ catalog: 1, skins: [entry(v2, '1.1.0')] }, v2] };
      const { ctx, page, errors, state } = await run(view, releases);
      const snap = (n) => shots && page.screenshot({ path: path.join(shots, 'gallery-activate-' + view.tag + '-' + n + '.png') });
      // The gallery publishes v2, but its answer waits until we let it go.
      let release; state.hold = new Promise((r) => { release = r; }); state.published = 'v2';
      await page.goto(origin + '/');
      await until(async () => (await applied(page)).build === '"v1"', view.tag + ': v1 mounted');
      // Open the first chat, write a draft and attach a file.
      await page.locator('[data-part=chat-row]').filter({ hasText: 'Bob' }).first().click();
      const field = page.locator('[data-part=composer] textarea').first();
      await field.waitFor({ timeout: 15000 });
      const head = (await page.locator('[data-part=conversation-head]').first().innerText()).split('\n')[0];
      await page.locator('[data-part=composer] input[type=file]').first().setInputFiles({ name: 'kept-note.txt', mimeType: 'text/plain', buffer: Buffer.from('kept across the update') });
      await page.getByText('kept-note.txt').first().waitFor({ timeout: 10000 });
      await field.click();
      await field.fill('Draft kept across the skin update');
      // The gallery answers while the field has focus: v2 is stored, trust moves, v1 stays.
      release(); state.hold = null;
      await until(async () => (await page.evaluate(() => localStorage.getItem('agentnet.skin.trusted.local:pulp'))) === digest(v2), view.tag + ': v2 stored and trusted');
      await until(async () => (await applied(page)).sheets.length === 2, view.tag + ': v2 prepared beside v1');
      assert.equal((await applied(page)).build, '"v1"', view.tag + ': no swap while typing');
      // Typing during the pending update stays immediate.
      const typed = await field.evaluate((el) => new Promise((resolve) => {
        const t0 = performance.now(); el.focus();
        el.setRangeText('!', el.value.length, el.value.length, 'end'); el.dispatchEvent(new Event('input', { bubbles: true }));
        requestAnimationFrame(() => resolve({ ms: performance.now() - t0, value: el.value }));
      }));
      assert.ok(typed.ms < 1100, view.tag + ': local typing feedback ' + typed.ms + ' ms');
      // A write under way defers the swap after the field is left.
      let free; const held = new Promise((r) => { free = r; });
      await ctx.route(/\/api\/held-write/, async (route) => { await held; await route.fulfill({ status: 404, body: 'no such route' }); });
      await page.evaluate(() => { window.heldWrite = window.agentnet.api('/api/held-write', { x: 1 }).catch(() => 'settled'); });
      const leftAt = Date.now();
      await page.locator('[data-part=conversation-head]').first().click({ position: { x: 5, y: 5 } });
      await page.waitForTimeout(300);
      assert.equal((await applied(page)).build, '"v1"', view.tag + ': no swap while a write is under way');
      free();
      await until(async () => (await applied(page)).build === '"v2"', view.tag + ': v2 opened in place', 5000);
      const swapMs = Date.now() - leftAt - 300;
      const after = await applied(page);
      assert.deepEqual(after.sheets, [{ href: '/local-skins/' + digest(v2) + '/style.css', on: true }], view.tag + ': only v2 styles remain');
      assert.equal(new URL(page.url()).searchParams.get('skin'), null, view.tag + ': no switch through the address');
      assert.equal(await page.evaluate(() => localStorage.getItem('agentnet.skin')), 'local:pulp', view.tag + ': same skin selected');
      // Same chat, same draft, same attached file.
      const field2 = page.locator('[data-part=composer] textarea').first();
      await field2.waitFor({ timeout: 10000 });
      assert.equal((await page.locator('[data-part=conversation-head]').first().innerText()).split('\n')[0], head, view.tag + ': same chat open');
      assert.equal(await field2.inputValue(), 'Draft kept across the skin update!', view.tag + ': draft text kept');
      await page.getByText('kept-note.txt').first().waitFor({ timeout: 5000 });
      await snap('after');
      assert.deepEqual(errors, [], view.tag + ': page errors');
      console.log(view.tag + ': swapped ' + swapMs + ' ms after the write settled; typing ' + typed.ms.toFixed(1) + ' ms');
      await ctx.close();
    }

    // Failure cases (desktop): each leaves the running version usable.
    const desk = { width: 1280, height: 800, scheme: 'light' };
    const cases = [
      ['bad bytes', (s) => { s.tamper = true; }, tiny('v2 bad')],
      ['offline', (s) => { s.down = true; }, tiny('v2 offline')],
      ['needs a newer AgentNet', () => {}, tiny('v2 newer', { agentnet: 'v99.0.0' }), 'v99.0.0'],
      ['untrusted', null, tiny('v2 untrusted')],
      ['fails to mount', () => {}, tiny('v2 broken', { throws: true })],
    ];
    for (const [name, setup, next, needs] of cases) {
      const v1 = tiny('v1 running');
      const releases = { v1: [{ catalog: 1, skins: [entry(v1, '1.0.0')] }, v1], v2: [{ catalog: 1, skins: [entry(next, '1.1.0', needs)] }, next] };
      const { ctx, page, errors, state } = await run(desk, releases);
      let release = null;
      if (setup) setup(state);
      else state.hold = new Promise((r) => { release = r; }); // untrusted: the person's trust changes before the gallery answers
      state.published = 'v2';
      await page.goto(origin + '/');
      await until(async () => (await applied(page)).h1 === 'v1 running', name + ': v1 mounted');
      if (release) { await page.evaluate(() => localStorage.setItem('agentnet.skin.trusted.local:pulp', 'e'.repeat(64))); release(); state.hold = null; }
      await page.waitForTimeout(1500);
      const now = await applied(page);
      assert.equal(now.h1, 'v1 running', name + ': the running version stays');
      assert.equal(now.card, false, name + ': no failure card');
      const kept = await page.evaluate(async () => (await caches.keys()).filter((k) => k.startsWith('agentnet-skin-package-v1-')));
      assert.equal(kept.includes('agentnet-skin-package-v1-' + digest(next)), name === 'untrusted' || name === 'fails to mount', name + ': v2 stored only when verified and compatible');
      assert.deepEqual(errors, [], name + ': page errors');
      await ctx.close();
      console.log('failure case ' + name + ': running version kept');
    }
    console.log('gallery skin activate rendered PASS: desktop light and Android-width dark — in-place update with same skin, chat, draft and file; deferred while typing and while a write runs; bad bytes, offline, newer-AgentNet, untrusted and failed mount keep the running version');
  } catch (e) {
    if (shots && lastPage) await lastPage.screenshot({ path: path.join(shots, 'gallery-activate-failure.png') }).catch(() => {});
    throw e;
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
