// BUG-19 rendered check: the host's workspace bar (real loader.js, real
// workspaces.mjs) over a synthetic program API. Disconnecting a joined
// workspace points to Reconnect, not to joining again; Reconnect lists the
// disconnected membership and routes that same one again under a new handle.
// Uses an installed Playwright via AGENTNET_PLAYWRIGHT; no existing browser
// is attached. Optional AGENTNET_SCREENSHOTS writes only to that directory.
const fs = require('fs'), http = require('http'), path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const base = path.resolve(__dirname, '../static') + '/';
const other = 'c'.repeat(32), hostOf = (n) => n.toString(16).padStart(32, '0');

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
  if (u.pathname === '/assets/skins/index.json') return json([{ id: 'default', api: 1, name: 'AgentNet' }]);
  if (u.pathname === '/assets/default.html') return send(200, 'text/html', '<body><h1>Default fixture</h1></body>');
  if (['/assets/app.js', '/assets/lenses.js'].includes(u.pathname)) return send(200, 'text/javascript', '');
  if (u.pathname === '/assets/app.css') return send(200, 'text/css', '');
  if (['/assets/loader.js', '/assets/workspaces.mjs', '/assets/core.css', '/assets/workspaces.css'].includes(u.pathname)) {
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
      page.on('response', (r) => { const p = new URL(r.url()).pathname; if (r.status() >= 400 && !['/favicon.ico', '/assets/local-skins.mjs'].includes(p)) errors.push(r.status() + ' ' + p); }); // optional files an older program lacks
      page.on('dialog', (d) => d.accept());
      await page.goto(origin + '/');
      const select = page.getByLabel('Active workspace', { exact: true });
      await select.waitFor();
      const options = () => select.locator('option').evaluateAll((os) => os.map((o) => o.value));
      assert.deepEqual(await options(), ['default', other]);
      const tool = async (label) => {
        if (width < 760 && !(await page.locator('.workspace-bar.open').count())) await page.getByRole('button', { name: 'Workspace options', exact: true }).click();
        await page.getByRole('button', { name: label, exact: true }).click();
      };
      const note = page.locator('#workspace-shell .workspace-note');

      await select.selectOption(other);
      await tool('Disconnect…');
      await page.waitForFunction(() => /Disconnected from Acme/.test(document.querySelector('#workspace-shell .workspace-note').textContent));
      const said = await note.textContent();
      assert.match(said, /Reconnect… connects it again/, name + ': disconnect note');
      assert.doesNotMatch(said, /join again|new invitation/, name + ': disconnect note still says to join again');
      assert.deepEqual(await options(), ['default']);

      await tool('Reconnect…');
      const pick = page.getByLabel('Disconnected workspace', { exact: true });
      await pick.waitFor();
      assert.deepEqual(await pick.locator('option').evaluateAll((os) => os.map((o) => [o.value, o.textContent])), [[other, 'Acme · acme.example · alice/laptop']]);
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, 'agentnet-workspace-reconnect-' + name + '.png'), fullPage: true });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name + ': horizontal overflow');
      await tool('Join a workspace…'); // the reconnect form open does not block joining
      assert.equal(await page.locator('.workspace-join:not(.workspace-reconnect)').count(), 1, name + ': join form beside the reconnect form');
      await page.locator('.workspace-join:not(.workspace-reconnect)').getByRole('button', { name: 'Cancel', exact: true }).click();
      await page.getByRole('button', { name: 'Reconnect', exact: true }).click();
      await page.waitForFunction(() => /Reconnected Acme as alice\/laptop/.test(document.querySelector('#workspace-shell .workspace-note').textContent));
      assert.equal(await page.locator('.workspace-reconnect').count(), 0);
      assert.deepEqual(await options(), ['default', other]);
      assert.deepEqual(state.posts.map((p) => p.path), ['/api/workspaces/disconnect', '/api/workspaces/reconnect']);
      assert.deepEqual(state.posts[1].body, { id: other });

      await select.selectOption(other);
      const bound = await page.evaluate(() => [window.agentnet.workspace.id, window.agentnet.workspace.handle]);
      assert.deepEqual(bound, [other, state.acme.handle]);
      assert.notEqual(state.acme.handle, state.handles[0]);

      await tool('Reconnect…');
      await page.waitForFunction(() => document.querySelector('#workspace-shell .workspace-note').textContent === 'No disconnected workspace here.');
      assert.equal(await page.locator('.workspace-reconnect').count(), 0);
      assert.deepEqual(errors, [], name + ': page errors');
      await page.close();
    }
    console.log('workspace reconnect check PASS: desktop/390, disconnect note points to Reconnect, same membership under a new handle, nothing left to reconnect');
  } finally {
    if (browser) await browser.close();
    await new Promise((r) => server.close(r));
  }
})().catch((e) => { console.error(e); process.exitCode = 1; });
