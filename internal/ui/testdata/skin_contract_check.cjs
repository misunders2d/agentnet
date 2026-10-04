// Contract-only check (docs/UI_SKINS.md): mounts skin packages from copied
// package bytes at an unrelated path, with only the public Host API v1 that
// this file implements itself (no loader.js, no workspace shell, no page
// globals), and fails on what a standalone skin must never do:
//   - read a private page global (window.agentnet*, the host's own),
//   - talk to /api or /events itself instead of through the host,
//   - request a file its manifest does not declare (documented host
//     modules such as /assets/typing.mjs excepted),
//   - write to the DOM outside the root it was given,
// and requires mount/unmount A/B/A to leave nothing behind.
//
//   node skin_contract_check.cjs DAEMON_URL_WITH_TOKEN SKIN_DIR...
// DAEMON_URL is a demo daemon (ui.NewFixture); AGENTNET_PLAYWRIGHT names an
// installed playwright-core, AGENTNET_CHROMIUM a Chromium, and optional
// AGENTNET_SCREENSHOTS a private directory for evidence.
const fs = require('fs'), path = require('path'), http = require('http'), crypto = require('crypto'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target, ...dirs] = process.argv.slice(2);
if (!target || !dirs.length) { console.error('usage: skin_contract_check.cjs DAEMON_URL SKIN_DIR...'); process.exit(2); }
const daemon = new URL(target), token = daemon.searchParams.get('t');
const hostModules = { '/assets/typing.mjs': path.join(__dirname, '..', 'static', 'typing.mjs') };
const marker = crypto.randomBytes(8).toString('hex'); // the harness host's own requests carry it
const types = { '.mjs': 'text/javascript', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.woff2': 'font/woff2', '.png': 'image/png', '.svg': 'image/svg+xml', '.webp': 'image/webp' };

// Packages: a snapshot of each manifest's declared files, served under a
// random unrelated prefix; nothing else of the package directory.
const packages = dirs.map((dir) => {
  const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'skin.json'), 'utf8'));
  const base = '/x/' + crypto.randomBytes(6).toString('hex') + '/pkg-' + manifest.id + '/';
  const files = new Map([['skin.json', fs.readFileSync(path.join(dir, 'skin.json'))]]);
  for (const f of manifest.files) files.set(f, fs.readFileSync(path.join(dir, f)));
  return { manifest, base, files };
});

const harness = `
const realFetch = window.fetch.bind(window), RealES = window.EventSource;
const report = (window.__report = { globals: [], direct: [], outside: [], errors: [] });
// Private page globals: none exists here; touching one is recorded.
for (const name of ['agentnet', 'agentnetOpen', 'agentnetEngine', 'agentnetWorkspace', 'agentnetWorkspaces', 'agentnetLens']) {
  Object.defineProperty(window, name, { configurable: false, get() { report.globals.push(name); return undefined; }, set() { report.globals.push(name + '='); } });
}
const own = (path, init = {}) => realFetch(path, { ...init, headers: { ...(init.headers || {}), 'X-Contract-Host': '${marker}' } });
const json = async (path, body) => {
  if (!path.startsWith('/api/')) throw new Error('Expected an AgentNet API path');
  const r = await own(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.json();
};
let openHandler = null;
const host = Object.freeze({
  version: 1, platform: 'daemon', api: json,
  workspace: Object.freeze({ id: 'default', name: 'Contract check', endpoint: location.origin, address: '', realm: '', state: 'enrolled' }),
  workspaces: null,
  listen(fn) { const es = new RealES('/events?contract-host=${marker}'); es.addEventListener('change', (e) => fn({ type: 'change', seq: Number(e.data) })); es.onerror = () => { es.close(); fn({ type: 'disconnect' }); }; return () => es.close(); },
  async file(id, index, dir) { const r = await own('/api/files/' + encodeURIComponent(id) + '/' + index + (dir ? '?dir=' + dir : '')); if (!r.ok) throw new Error(r.statusText); return { bytes: new Uint8Array(await r.arrayBuffer()) }; },
  async stage(file) { const r = await own('/api/upload?name=' + encodeURIComponent(file.name), { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file }); if (!r.ok) throw new Error((await r.text()).trim() || r.statusText); return (await r.json()).id; },
  onOpen(fn) { openHandler = fn; },
  skins: [{ api: 1, id: 'comic', name: 'Comic', builtin: true }, { api: 1, id: 'example', name: 'Example', digest: '0'.repeat(64) }], onSkinsChange() { return () => {}; },
  selectSkin() { throw new Error('Switching is not part of this check'); },
});
// The skin's own direct transport (around the host's captured originals).
window.fetch = (input, init) => { const u = new URL(typeof input === 'string' ? input : input.url, location.href); if (/^\\/(api|events)\\b/.test(u.pathname)) report.direct.push('fetch ' + u.pathname); return realFetch(input, init); };
window.EventSource = function (url, o) { report.direct.push('EventSource ' + url); return new RealES(url, o); };
const xo = XMLHttpRequest.prototype.open; XMLHttpRequest.prototype.open = function (m, u, ...rest) { if (/\\/(api|events)\\b/.test(String(u))) report.direct.push('xhr ' + u); return xo.call(this, m, u, ...rest); };
window.addEventListener('error', (e) => report.errors.push(String(e.message)));
window.addEventListener('unhandledrejection', (e) => report.errors.push(String(e.reason && e.reason.message || e.reason)));

const base = new URL(location.href).searchParams.get('pkg');
const manifest = await (await realFetch(base + 'skin.json')).json();
// Document rules, as the UI host adopts them: @font-face and @property only, URLs inside the package.
if (manifest.document) {
  const href = new URL(base + manifest.document, location.href), parsed = new CSSStyleSheet(), kept = new CSSStyleSheet();
  parsed.replaceSync(await (await realFetch(href)).text());
  for (const rule of parsed.cssRules) {
    if (rule instanceof CSSPropertyRule) kept.insertRule(rule.cssText, kept.cssRules.length);
    else if (rule instanceof CSSFontFaceRule) kept.insertRule(rule.cssText.replace(/url\\(\\s*(["']?)([^"')]*)\\1\\s*\\)/g, (_, q, u) => 'url("' + new URL(u, href).href + '")'), kept.cssRules.length);
  }
  document.adoptedStyleSheets = [kept];
}
const holder = document.getElementById('skin'), shadow = holder.attachShadow({ mode: 'open' });
if (manifest.style) { const l = document.createElement('link'); l.rel = 'stylesheet'; l.href = base + manifest.style; shadow.append(l); await new Promise((r) => { l.onload = r; l.onerror = r; }); }
const module = await import(base + manifest.entry);
new MutationObserver((list) => { for (const m of list) report.outside.push(m.type + ' ' + m.target.nodeName + (m.attributeName ? '@' + m.attributeName : '')); })
  .observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
let root = null;
window.__mount = async () => { openHandler = null; root = document.createElement('div'); root.className = 'skin-root'; shadow.append(root); await module.mount(root, host); if (!openHandler) throw new Error('no host.onOpen'); return true; };
window.__unmount = async () => { if (typeof module.unmount === 'function') await module.unmount(root); const left = root.childNodes.length; root.remove(); root = null; return left; };
window.__open = (conv) => openHandler && openHandler(conv, 'conversation');
await window.__mount();
window.__ready = true;
`;

const directHits = [], undeclared = [];
// The demo daemon behind this origin, as if the page were its own.
const proxy = (req, res) => {
  const headers = { ...req.headers, host: daemon.host, cookie };
  if (headers.origin) headers.origin = daemon.origin;
  if (headers['sec-fetch-site']) headers['sec-fetch-site'] = 'same-origin';
  delete headers.referer;
  const up = http.request({ host: daemon.hostname, port: daemon.port, path: req.url, method: req.method, headers }, (r) => { const h = { ...r.headers }; delete h['set-cookie']; res.writeHead(r.statusCode || 502, h); r.pipe(res); });
  up.on('error', () => { if (!res.headersSent) res.writeHead(502); res.end(); });
  req.pipe(up);
};
let cookie = '';
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x'), send = (type, data) => { res.setHeader('Content-Type', type + '; charset=utf-8'); res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' blob:; connect-src 'self'"); res.end(data); };
  if (u.pathname === '/') return send('text/html', '<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Contract check</title><div id="skin"></div><script type="module" src="/harness.mjs"></script>');
  if (u.pathname === '/harness.mjs') return send('text/javascript', harness);
  if (/^\/(api|events)\b/.test(u.pathname)) {
    if (req.headers['x-contract-host'] !== marker && u.searchParams.get('contract-host') !== marker) directHits.push(req.method + ' ' + u.pathname);
    return proxy(req, res);
  }
  if (hostModules[u.pathname]) return send('text/javascript', fs.readFileSync(hostModules[u.pathname]));
  for (const p of packages) if (u.pathname.startsWith(p.base)) {
    const name = decodeURIComponent(u.pathname.slice(p.base.length));
    if (p.files.has(name)) { res.setHeader('Content-Type', (types[path.extname(name)] || 'application/octet-stream') + '; charset=utf-8'); return res.end(p.files.get(name)); }
  }
  if (u.pathname === "/favicon.ico") return res.writeHead(204).end();
  undeclared.push(u.pathname);
  res.writeHead(404).end();
});

// What each skin must show and do, through its own words.
const journeys = {
  comic: async (page) => {
    await page.getByRole('heading', { name: 'Chats' }).first().waitFor({ timeout: 20000 });
    await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: /^(Settings|You)$/ }).click();
    await page.getByRole('button', { name: /Appearance/ }).first().click();
    await page.getByRole('heading', { name: 'Skin', exact: true }).waitFor();
    await page.getByRole('radio', { name: 'Dark' }).click();
    await page.getByRole('button', { name: 'Use Example' }).click(); // a sheet (Base UI dialog)
    await page.getByRole('dialog').waitFor();
    await page.waitForTimeout(400);
    await page.keyboard.press('Escape');
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'Chats' }).click();
    await page.getByRole('heading', { name: 'Chats' }).first().waitFor();
    // A conversation with its menus: every popup renders inside the root.
    await page.getByRole('button', { name: /Bob’s agent/ }).first().click();
    await page.getByRole('log').first().waitFor();
    await page.getByRole('button', { name: 'More', exact: true }).click();
    await page.getByRole('menu').waitFor();
    await page.waitForTimeout(300);
    await page.keyboard.press('Escape');
    const plus = page.getByRole('button', { name: 'Add to message' });
    await plus.waitFor();
    await plus.click();
    await page.getByRole('menuitem', { name: /Emoji/ }).click();
    await page.getByLabel('Search emoji').waitFor();
    await page.waitForTimeout(400);
    await page.keyboard.press('Escape');
  },
  notebook: async (page) => {
    await page.getByRole('heading', { name: 'Notebook', exact: true }).waitFor({ timeout: 20000 });
    await page.locator('.notebook nav button').first().click();
    await page.getByRole('textbox', { name: 'Message' }).waitFor();
  },
};

(async () => {
  await new Promise((resolve, reject) => http.get({ host: daemon.hostname, port: daemon.port, path: '/?t=' + token, headers: { host: daemon.host } }, (r) => { cookie = (r.headers['set-cookie'] || []).map((c) => c.split(';')[0]).join('; '); r.resume(); r.on('end', resolve); }).on('error', reject));
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const origin = 'http://127.0.0.1:' + server.address().port;
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  const results = [];
  try {
    for (const p of packages) for (const [w, h] of [[1440, 900], [390, 844]]) {
      const page = await browser.newPage({ viewport: { width: w, height: h } });
      const errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text()); });
      directHits.length = 0; undeclared.length = 0;
      await page.goto(origin + '/?pkg=' + encodeURIComponent(p.base));
      await page.waitForFunction(() => window.__ready === true, null, { timeout: 20000 });
      const journey = journeys[p.manifest.id] && (async (pg) => {
        try { await journeys[p.manifest.id](pg); } catch (e) {
          if (process.env.AGENTNET_SCREENSHOTS) await pg.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, 'contract-' + p.manifest.id + '-' + w + '-FAIL.png') });
          throw e;
        }
      });
      if (journey) await journey(page);
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, 'contract-' + p.manifest.id + '-' + w + '.png') });
      // A/B/A: unmount, mount again, unmount, mount again.
      const left1 = await page.evaluate(() => window.__unmount());
      await page.evaluate(() => window.__mount());
      const left2 = await page.evaluate(() => window.__unmount());
      await page.evaluate(() => window.__mount());
      if (journey) await journey(page);
      const report = await page.evaluate(() => window.__report);
      const outcome = { skin: p.manifest.id, width: w, globals: report.globals, direct: [...report.direct, ...directHits.map((d) => 'request ' + d)], undeclared: [...undeclared], outside: report.outside, leftAfterUnmount: [left1, left2], errors: [...errors, ...report.errors] };
      results.push(outcome);
      await page.close();
    }
  } finally { await browser.close(); server.close(); }
  console.log(JSON.stringify(results, null, 1));
  for (const r of results) {
    assert.deepEqual(r.globals, [], r.skin + ': private page globals');
    assert.deepEqual(r.direct, [], r.skin + ': direct API or event stream');
    assert.deepEqual(r.undeclared, [], r.skin + ': undeclared assets or imports');
    assert.deepEqual(r.outside, [], r.skin + ': DOM writes outside its root');
    assert.deepEqual(r.leftAfterUnmount, [0, 0], r.skin + ': unmount leaves its root empty');
    assert.deepEqual(r.errors, [], r.skin + ': errors');
  }
  console.log('skin contract check PASS: ' + packages.map((p) => p.manifest.id).join(', ') + ' at 1440 and 390, A/B/A');
})().catch((e) => { console.error(e); process.exitCode = 1; })
  .finally(() => process.exit()); // proxied event streams would keep the process alive
