// Actual CDP PWA install/launch in one disposable default Chrome profile.
// Product Engine, IndexedDB, loader, manifest and app bytes stay untouched.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_PWA_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
const binary = process.env.AGENTNET_PWA_BINARY, manifestPath = process.env.AGENTNET_PWA_ASSET_MANIFEST;
assert(world && evidence && binary && manifestPath, 'explicit frozen binary/world/evidence/asset-manifest required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const manifestId = origin + '/';
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const expectedAssets = Object.fromEntries(fs.readFileSync(manifestPath, 'utf8').trim().split('\n').map(line => {
  const [hash, file] = line.trim().split(/\s+/); return [path.basename(file), hash];
}));
const assetNames = ['app.js', 'app.css', 'lenses.js', 'engine.mjs', 'wire.mjs', 'device.mjs', 'loader.js', 'sw.js'];
for (const name of assetNames) assert(expectedAssets[name], 'frozen asset manifest missing ' + name);
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const cli = (name, ...args) => execFileSync(binary, ['--home', path.join(world, name), ...args], { encoding: 'utf8', env: process.env });
const ready = page => page.waitForFunction(() => typeof state !== 'undefined' && state.overview, null, { timeout: 20000 });
const api = (page, url, body) => page.evaluate(({ url, body }) => window.agentnet.api(url, body), { url, body });
const identity = page => page.evaluate(() => ({ address: state.overview.me.address, fingerprint: state.overview.me.fingerprint,
  person: state.overview.person.person, devices: state.overview.person.devices.map(d => d.address).sort() }));
const idle = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(label, predicate, timeout = 30000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) { if (await predicate()) return; await idle(100); }
  throw Error('Timed out: ' + label);
}
const result = { pass: false, headless: true, installed: false, standalone: false, closeRelaunch: false, reload: false,
  offlineQueuedOnce: false, reconnect: false, attachment: false, binarySHA256: digest(fs.readFileSync(binary)),
  assetManifestSHA256: digest(fs.readFileSync(manifestPath)), expectedAssets, servedAssets: {}, screenshots: [], errors: [], external: [], checks: [] };
const assetReads = [];
let context, session, stage = 'isolated browser setup';
async function capture(page, name) {
  await page.waitForFunction(() => !document.querySelector('#zoom .zoom-arrive-in, #zoom .zoom-arrive-out'));
  const sizes = await page.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth,
    dialogs: [...document.querySelectorAll('dialog[open]')].map(n => n.scrollWidth > n.clientWidth + 1) }));
  const file = path.join(evidence, 'pwa-' + name + '.png');
  await page.screenshot({ path: file }); result.screenshots.push(file);
  assert.equal(sizes.document, false, name + ': document horizontal overflow');
  assert(sizes.dialogs.every(x => !x), name + ': dialog horizontal overflow');
}
async function launch(url) {
  const [page, launched] = await Promise.all([context.waitForEvent('page', { timeout: 20000 }), session.send('PWA.launch', { manifestId, ...(url ? { url } : {}) })]);
  await ready(page);
  assert.equal(new URL(page.url()).origin, origin);
  assert.equal(await page.evaluate(() => matchMedia('(display-mode: standalone)').matches), true);
  result.windows = result.windows || [];
  result.windows.push(await session.send('Browser.getWindowForTarget', { targetId: launched.targetId }));
  assert(result.windows.at(-1).windowId > 0);
  return page;
}
(async function installedJourney() {
  const browserHome = path.join(world, 'browser-home'), profile = path.join(world, 'chromium-profile');
  const env = { ...process.env, HOME: browserHome };
  fs.mkdirSync(browserHome, { recursive: true, mode: 0o700 });
  for (const [key, dir] of Object.entries({ XDG_DATA_HOME: 'data', XDG_CONFIG_HOME: 'config', XDG_CACHE_HOME: 'cache' })) {
    env[key] = path.join(browserHome, dir); fs.mkdirSync(env[key], { recursive: true, mode: 0o700 });
  }
  context = await chromium.launchPersistentContext(profile, { headless: true, executablePath: process.env.AGENTNET_CHROMIUM, env,
    viewport: { width: 1280, height: 900 }, args: ['--ignore-certificate-errors-spki-list=' + spki,
      '--disable-background-networking', '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost'] });
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin === origin) return route.continue();
    result.external.push(url.origin); return route.abort();
  });
  const attach = page => {
    page.setDefaultTimeout(15000);
    page.on('pageerror', error => result.errors.push(error.stack || error.message));
    page.on('response', response => {
      const name = new URL(response.url()).pathname.split('/').pop();
      if (assetNames.includes(name)) assetReads.push((async () => {
        const hash = digest(await response.body());
        assert.equal(hash, expectedAssets[name], 'source/served mismatch ' + name); result.servedAssets[name] = hash;
      })().catch(error => result.errors.push(error.message)));
    });
  };
  context.on('page', attach); for (const page of context.pages()) attach(page);
  session = await context.browser().newBrowserCDPSession();
  result.chromium = (await session.send('Browser.getVersion')).product;
  result.isolation = { profile, browserHome, XDG_DATA_HOME: env.XDG_DATA_HOME, XDG_CONFIG_HOME: env.XDG_CONFIG_HOME, XDG_CACHE_HOME: env.XDG_CACHE_HOME, exactCertificateSPKI: spki };
  const page = context.pages()[0];
  const raw = fs.readFileSync(path.join(world, 'link'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  // Same exact fixture TLS binding as human_navigation_journey: browser
  // TLS uses certificate SPKI; the native-only invitation pin is omitted.
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url'));
  const invite = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  delete invite.cert;
  offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  stage = 'install and manifest';
  await page.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  const manifest = await page.evaluate(async () => {
    const link = document.querySelector('link[rel="manifest"]');
    if (!link) throw Error('hosted page has no install manifest');
    const r = await fetch(link.href); if (!r.ok) throw Error('manifest unavailable'); return { url: link.href, value: await r.json() };
  });
  const value = manifest.value;
  assert.equal(value.name, 'AgentNet'); assert.equal(value.display, 'standalone');
  for (const field of ['id', 'start_url', 'scope']) {
    const url = new URL(value[field], manifest.url); assert.equal(url.href, manifestId); assert(!url.search && !url.hash);
  }
  result.manifest = manifest;
  result.icons = await page.evaluate(async manifest => Promise.all(manifest.icons.map(async icon => {
    const response = await fetch(new URL(icon.src, location.origin)); if (!response.ok) throw Error('icon unavailable');
    const image = await createImageBitmap(await response.blob()); return { src: icon.src, sizes: icon.sizes, width: image.width, height: image.height };
  })), value);
  assert(result.icons.some(i => i.sizes === '192x192' && i.width === 192 && i.height === 192));
  assert(result.icons.some(i => i.sizes === '512x512' && i.width === 512 && i.height === 512));
  const pageSession = await context.newCDPSession(page);
  let absent = false;
  try { await pageSession.send('PWA.getOsAppState', { manifestId }); } catch (_) { absent = true; }
  assert(absent, 'fresh default profile already has installed app');
  await pageSession.send('PWA.install', { manifestId });
  result.osState = await pageSession.send('PWA.getOsAppState', { manifestId }); result.installed = true;
  assert(fs.existsSync(path.join(profile, 'Default')), 'default installed-browser profile not created');
  console.log('PASS actual PWA.install, registry, manifest identity/start URL/scope and icons');
  stage = 'enrolled identity';
  await page.locator('#join-name').fill('pwa-tablet'); await page.locator('button.join-go').click();
  let request;
  await until('actual device-link request', async () => {
    request = cli('alice', 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*pwa-tablet/m)?.[1]; return !!request;
  });
  cli('alice', 'person', 'approve', request); await ready(page);
  await page.waitForFunction(() => state.overview.device.online && state.overview.directory.current && state.overview.person?.devices.length === 2);
  result.identity = await identity(page);
  assert(result.identity.devices.includes(result.identity.address) && result.identity.devices.includes('admin/laptop'));
  const dm = await api(page, '/api/dm/new', { address: 'bob/desk' }); result.conversation = dm.id;
  await api(page, '/api/dm/send', { conv: dm.id, body: 'Installed PWA exact conversation' });
  await page.close();
  await session.send('PWA.changeAppUserSettings', { manifestId, displayMode: 'standalone' });
  stage = 'standalone launch and profile';
  let app = await launch(origin + '/#conv=' + dm.id);
  await app.waitForFunction(id => state.dm === id && state.dmData?.id === id, dm.id);
  assert.deepEqual(await identity(app), result.identity); result.standalone = true; result.route = true;
  await capture(app, 'installed-conversation-1280');
  await app.locator('#profile-btn').click();
  await app.locator('#profile-card').getByText('One person, 2 devices', { exact: true }).waitFor();
  await app.locator('#profile-devices summary').click();
  const devices = app.locator('#profile-devices .device-row');
  assert.equal(await devices.count(), 2); assert.equal(await devices.filter({ hasText: '(this device)' }).count(), 1);
  await capture(app, 'installed-profile-1280'); result.checks.push('one person, linked laptop + PWA device, one This device');
  await app.locator('#settings-close').click();
  await app.close();
  stage = 'close and installed start-URL relaunch';
  app = await launch(); assert.deepEqual(await identity(app), result.identity);
  assert.equal(new URL(app.url()).pathname, '/'); assert(!new URL(app.url()).hash.includes('invite'));
  result.closeRelaunch = true; result.relaunchURL = app.url();
  await app.goto(origin + '/#conv=' + dm.id); await ready(app);
  await app.waitForFunction(id => state.dm === id && state.dmData?.id === id, dm.id);
  await app.reload(); await ready(app);
  assert.deepEqual(await identity(app), result.identity);
  assert.equal(await app.evaluate(() => matchMedia('(display-mode: standalone)').matches), true);
  await app.evaluate(id => openDM(id), dm.id); result.reload = true;
  console.log('PASS actual standalone app launch, exact conversation, close/relaunch and same enrolled identity');
  stage = 'offline once and reconnect';
  await context.setOffline(true); await app.waitForFunction(() => !state.overview.device.online);
  const body = 'Queued exactly once from installed PWA while offline';
  const queued = await api(app, '/api/dm/send', { conv: dm.id, body });
  assert.equal(queued.state, 'waiting'); result.offlineState = queued.state; result.offlineCopies = queued.copies;
  const offline = await api(app, '/api/dm?id=' + dm.id);
  assert.equal(offline.messages.filter(m => m.body === body).length, 1);
  await capture(app, 'offline-queued-1280'); result.offlineQueuedOnce = true;
  await context.setOffline(false);
  await app.waitForFunction(() => state.overview.device.online && state.overview.directory.current && !loading);
  await until('exact posted Bob copy delivered once', async () => {
    const projection = await api(app, '/api/dm?id=' + dm.id);
    const messages = projection.messages.filter(m => m.body === body); assert.equal(messages.length, 1);
    result.deliveryProjection = messages[0]; return messages[0].copies?.some(c => c.to === 'bob/desk' && c.state === 'delivered');
  });
  await app.waitForFunction(expected => {
    const message = state.dmData?.messages.find(m => m.id === expected.id);
    return message && message.state === expected.state && JSON.stringify(message.copies) === JSON.stringify(expected.copies) && !loading;
  }, result.deliveryProjection);
  const received = cli('bob', 'dm', 'show', dm.id);
  assert.equal(received.split('\n').filter(line => line.trim() === body).length, 1);
  result.nativePeerCopies = 1; result.reconnect = true; await capture(app, 'reconnected-delivered-1280');
  stage = 'attachment in installed window';
  const bytes = Buffer.from('Installed PWA attachment exact bytes\n');
  await app.locator('#file-input').setInputFiles({ name: 'installed-pwa.txt', mimeType: 'text/plain', buffer: bytes });
  await app.locator('#body').fill('Installed PWA file round trip'); await app.locator('#send').click();
  await app.waitForFunction(() => state.dmData?.messages.some(m => m.body === 'Installed PWA file round trip' && m.attachments?.some(f => f.name === 'installed-pwa.txt' && f.openable)));
  const fileCard = app.locator('#timeline .file').filter({ hasText: 'installed-pwa.txt' });
  await fileCard.getByRole('button', { name: 'Open', exact: true }).click();
  const downloadLink = fileCard.getByRole('link', { name: 'Download installed-pwa.txt', exact: true });
  await downloadLink.waitFor();
  const download = await Promise.all([app.waitForEvent('download'), downloadLink.click()]);
  const file = path.join(evidence, 'installed-pwa-download.txt'); await download[0].saveAs(file);
  assert.deepEqual(fs.readFileSync(file), bytes); result.attachment = { bytes: bytes.length, sha256: digest(bytes), file };
  await capture(app, 'attachment-open-1280');
  await app.setViewportSize({ width: 390, height: 844 }); await capture(app, 'installed-conversation-390');
  // Same visible home path as human_navigation_journey: DM, person, Chats.
  for (let up = 0; up < 3 && !(await app.locator('#nav-chats').isVisible()); up++) await app.locator('#back').click();
  await app.locator('#nav-chats').click();
  await app.locator('#profile-btn').click(); await capture(app, 'installed-profile-390');
  await app.locator('#settings-close').click();
  // Fetch exact service-worker bytes without subscribing to cloud push.
  await app.evaluate(async () => { const response = await fetch('/sw.js'); if (!response.ok) throw Error('service worker unavailable'); await response.text(); });
  await Promise.all(assetReads);
  for (const name of assetNames) assert(result.servedAssets[name], 'served asset missing ' + name);
  assert.deepEqual(result.errors, []); assert.deepEqual(result.external, []);
  await app.close(); await session.send('PWA.uninstall', { manifestId }); result.uninstalled = true;
  result.pass = true; console.log('PASS installed offline/reconnect exactly once, attachment Open/download and source/served hashes');
})().catch(async error => {
  result.stage = stage; result.error = error.message; console.error(stage + ': ' + error.message); process.exitCode = 1;
  if (context) for (const page of context.pages()) await page.screenshot({ path: path.join(evidence, 'pwa-first-failure.png') }).catch(() => {});
}).finally(async () => {
  if (context) await context.close();
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify(result, null, 2), { mode: 0o600 });
});
