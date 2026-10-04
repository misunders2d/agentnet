// BUG-19 rendered check: the host's switcher (real loader.js, skinbar.mjs
// and workspaces.mjs) over a skin from someone else, with a synthetic
// program API. Leaving a joined workspace points to Reconnect, not to
// joining again; Reconnect lists the disconnected membership and routes
// that same one again under a new handle, and the skin is mounted again
// over a host bound to it.
// Uses an installed Playwright via AGENTNET_PLAYWRIGHT; no existing browser
// is attached. Optional AGENTNET_SCREENSHOTS writes only to that directory.
const fs = require('fs'), http = require('http'), path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const base = path.resolve(__dirname, '../static') + '/';
const other = 'c'.repeat(32), hostOf = (n) => n.toString(16).padStart(32, '0'), digest = 'f'.repeat(64);
const skin = `export function mount(root,host){const h=document.createElement('h1');h.textContent='Fixture skin in '+host.workspace.name;root.append(h);host.onOpen(()=>{});}export function unmount(root){root.replaceChildren();}`;

const world = () => {
  let generation = 1;
  const s = {
    posts: [],
    def: { id: 'default', name: 'This computer', endpoint: 'https://home.example', address: 'alice/laptop', state: 'enrolled', handle: hostOf(1) },
    acme: { id: other, name: 'Acme', endpoint: 'https://acme.example', address: 'alice/laptop', realm: 'd'.repeat(32), state: 'enrolled', handle: hostOf(2) },
  };
  s.handles = [s.acme.handle];
  s.reconnect = () => { s.acme = { ...s.acme, state: 'enrolled', handle: hostOf(10 + ++generation) }; s.handles.push(s.acme.handle); return s.acme; };
  return s;
};

let state = world();
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://fixture');
  const send = (status, type, text) => { res.setHeader('Content-Type', type + '; charset=utf-8'); res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob:"); res.writeHead(status).end(text); };
  const json = (v) => send(200, 'application/json', JSON.stringify(v));
  if (req.method === 'POST') {
    let body = '';
    req.on('data', (c) => body += c);
    req.on('end', () => {
      const v = JSON.parse(body || '{}');
      state.posts.push({ path: u.pathname, body: v });
      if (u.pathname === '/api/workspaces/disconnect' && v.id === other && v.handle === state.acme.handle && state.acme.state === 'enrolled') { state.acme = { ...state.acme, state: 'disconnected', handle: '' }; json({}); return; }
      if (u.pathname === '/api/workspaces/reconnect' && v.id === other && state.acme.state === 'disconnected') { json(state.reconnect()); return; }
      send(409, 'text/plain', 'stale workspace');
    });
    return;
  }
  if (u.pathname === '/') return send(200, 'text/html', '<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/assets/core.css"><div id="skin"></div><script src="/assets/loader.js"></script>');
  if (u.pathname === '/assets/skins/index.json') return json([{ api: 1, id: 'comic', name: 'Comic', entry: 'entry.mjs', files: ['entry.mjs'], digest: 'e'.repeat(64) }, { api: 1, id: 'fixture', name: 'Fixture', entry: 'entry.mjs', files: ['entry.mjs'], digest }]);
  if (u.pathname === '/assets/skins/fixture/entry.mjs') return send(200, 'text/javascript', skin);
  if (['/assets/loader.js', '/assets/workspaces.mjs', '/assets/core.css', '/assets/skin-base.css', '/assets/skinbar.mjs', '/assets/skinbar.css'].includes(u.pathname)) {
    return send(200, u.pathname.endsWith('.css') ? 'text/css' : 'text/javascript', fs.readFileSync(base + path.basename(u.pathname)));
  }
  if (u.pathname === '/api/workspaces') return json([state.def, ...(state.acme.state === 'enrolled' ? [state.acme] : [])]);
  if (u.pathname === '/api/workspaces/all') return json([state.def, state.acme]);
  const scoped = u.pathname.match(/^\/workspaces\/([^/]+)\/([^/]+)\/api\/overview$/);
  if (scoped) return json({ me: { address: 'alice/laptop' } });
  send(404, 'text/plain', 'not found');
});

(async () => {
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const origin = 'http://127.0.0.1:' + server.address().port;
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
    for (const [name, width, height] of [['desktop', 1280, 800], ['phone', 390, 844]]) {
      state = world();
      const page = await browser.newPage({ viewport: { width, height } }), errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      page.on('console', (m) => { if (['error', 'warning'].includes(m.type()) && !m.text().startsWith('Failed to load resource')) errors.push(m.type() + ': ' + m.text()); });
      page.on('response', (r) => { const p = new URL(r.url()).pathname; if (r.status() >= 400 && !['/favicon.ico', '/assets/local-skins.mjs'].includes(p)) errors.push(r.status() + ' ' + p); }); // optional files a program may lack
      await page.addInitScript((d) => localStorage.setItem('agentnet.skin.trusted.fixture', d), digest);
      await page.goto(origin + '/?skin=fixture');
      await page.getByRole('heading', { name: 'Fixture skin in This computer' }).waitFor();
      const menu = async () => { await page.getByRole('button', { name: /^Workspace: / }).click(); await page.getByRole('menu', { name: 'Workspaces' }).waitFor(); };
      const rows = () => page.getByRole('menu', { name: 'Workspaces' }).getByRole('menuitemradio').allInnerTexts();
      const notice = page.locator('#skin-bar .notice .text');

      await menu();
      assert.equal((await rows()).length, 2, name + ': both workspaces listed');
      await page.getByRole('menuitemradio', { name: /Acme/ }).click();
      await page.getByRole('heading', { name: 'Fixture skin in Acme' }).waitFor(); // mounted again over Acme's host
      await menu();
      await page.getByRole('menuitem', { name: /Leave Acme/ }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Leave Acme', exact: true }).click();
      await notice.filter({ hasText: 'You left Acme' }).waitFor();
      await page.getByRole('heading', { name: 'Fixture skin in This computer' }).waitFor();
      await menu();
      assert.equal((await rows()).length, 1, name + ': Acme no longer listed as connected');
      const reconnect = page.getByRole('menuitem', { name: /Reconnect Acme/ });
      await reconnect.waitFor();
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, 'agentnet-workspace-reconnect-' + name + '.png') });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name + ': horizontal overflow');
      await reconnect.click();
      await notice.filter({ hasText: 'Reconnected Acme' }).waitFor();
      assert.deepEqual(state.posts.map((p) => p.path), ['/api/workspaces/disconnect', '/api/workspaces/reconnect']);
      assert.deepEqual(state.posts[1].body, { id: other });
      await menu();
      await page.getByRole('menuitemradio', { name: /Acme/ }).click();
      await page.getByRole('heading', { name: 'Fixture skin in Acme' }).waitFor();
      const bound = await page.evaluate(() => [window.agentnet.workspace.id, window.agentnet.workspace.handle]);
      assert.deepEqual(bound, [other, state.acme.handle]);
      assert.notEqual(state.acme.handle, state.handles[0]);
      await menu();
      await page.waitForTimeout(300);
      assert.equal(await page.getByRole('menuitem', { name: /Reconnect/ }).count(), 0, name + ': nothing left to reconnect');
      await page.keyboard.press('Escape');

      // The host a skin gets: host.workspaces lists the memberships
      // disconnected here (list() leaves them out) and reconnects one.
      const before = state.acme.handle;
      await menu();
      await page.getByRole('menuitem', { name: /Leave Acme/ }).click();
      await page.getByRole('dialog').getByRole('button', { name: 'Leave Acme', exact: true }).click();
      await notice.filter({ hasText: 'You left Acme' }).waitFor();
      const viaHost = await page.evaluate(async (id) => {
        const w = window.agentnet.workspaces, gone = await w.disconnected();
        const listed = w.list().some((x) => x.id === id);
        await w.reconnect(id);
        return { gone: gone.map((x) => [x.id, x.state, x.name]), listed, has: w.has(id) };
      }, other);
      assert.deepEqual(viaHost, { gone: [[other, 'disconnected', 'Acme']], listed: false, has: true }, name + ': host.workspaces');
      assert.deepEqual(state.posts.slice(2).map((p) => [p.path, p.body.id]), [['/api/workspaces/disconnect', other], ['/api/workspaces/reconnect', other]]);
      assert.notEqual(state.acme.handle, before, name + ': the same membership under a new handle');
      assert.deepEqual(errors, [], name + ': page errors');
      await page.close();
    }
    console.log('workspace reconnect check PASS: desktop/390, leaving points to Reconnect, same membership under a new handle, skin remounted, nothing left to reconnect, host.workspaces disconnected()/reconnect(id)');
  } finally {
    if (browser) await browser.close();
    await new Promise((r) => server.close(r));
  }
})().catch((e) => { console.error(e); process.exitCode = 1; });
