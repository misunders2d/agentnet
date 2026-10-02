// Actual production browser Engine/IndexedDB and native profile UI over a real
// isolated signed Hub. No provider mocks, explicit receiver refresh or real peers.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_PERSON_WORLD;
const evidence = process.env.AGENTNET_SCREENSHOTS;
assert(world && evidence && process.env.AGENTNET_PERSON_BINARY, 'explicit private fixture paths required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const uiURL = name => fs.readFileSync(path.join(world, name + '-ui'), 'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const nativeURL = uiURL('alice'), remoteURL = uiURL('bob');
const allowed = new Set([origin, new URL(nativeURL).origin, new URL(remoteURL).origin]);
const cli = (name, ...args) => execFileSync(process.env.AGENTNET_PERSON_BINARY, ['--home', path.join(world, name), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
// Trust only this disposable certificate's exact public key, without changing
// HOME, the user's trust store or globally disabling browser TLS checks.
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const errors = [], external = [], checks = [], shots = [], testedAssets = {};
const ready = page => page.waitForFunction(() => typeof state !== 'undefined' && state.overview, null, { timeout: 20000 });
const api = (page, url, body) => page.evaluate(({ url, body }) => window.agentnet.api(url, body), { url, body });
const idle = ms => new Promise(r => setTimeout(r, ms));
let browser;
async function profile(page) {
  for (let up = 0; up < 3 && !(await page.locator('#profile-btn').isVisible()); up++) await page.locator('#back').click();
  await page.locator('#profile-btn').click();
}
async function capture(page, name, width) {
  const sizing = await page.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth,
    dialogs: [...document.querySelectorAll('dialog[open]')].map(n => n.scrollWidth > n.clientWidth + 1) }));
  assert.equal(sizing.document, false, name + ': horizontal document overflow');
  assert(sizing.dialogs.every(x => !x), name + ': horizontal dialog overflow');
  const file = path.join(evidence, `person-live-${name}-${width}.png`);
  await page.screenshot({ path: file }); shots.push(file);
}
async function snapshot(page, conv) {
  return page.evaluate(async conv => {
    const o = await window.agentnet.api('/api/overview'), dm = await window.agentnet.api('/api/dm?id=' + conv);
    const p = o.person;
    return { person: p.person, address: o.me.address, fingerprint: o.me.fingerprint,
      devices: p.devices.map(d => ({ address: d.address, fingerprint: d.fingerprint })).sort((a, b) => a.address.localeCompare(b.address)),
      conv: dm.id, peer: dm.peer.person, history: dm.messages.map(m => ({ id: m.id, from: m.from, body: m.body, kind: m.kind, reply_to: m.reply_to })) };
  }, conv);
}
async function rename(page, label, width, name) {
  await profile(page);
  await page.getByRole('button', { name: 'Change display name', exact: true }).click();
  await page.getByLabel('Display name', { exact: true }).fill(label);
  await capture(page, name + '-save', width);
  await page.getByRole('button', { name: 'Save name', exact: true }).click();
  await page.locator('#dialog').waitFor({ state: 'hidden' });
  await page.locator('#profile-card').getByText(label, { exact: true }).waitFor();
  await capture(page, name + '-confirmed', width);
  await page.locator('#settings-close').click();
}
async function duplicateContacts(page, pinnedID, width, name) {
  for (let up = 0; up < 3 && !(await page.locator('#search').isVisible()); up++) await page.locator('#back').click();
  await page.locator('#search').fill('Bob');
  await page.waitForFunction(() => state.overview.people.some(p => p.address === 'carol/desk' && p.label === 'Bob' && p.state === 'listed'));
  await page.getByText('· @' + pinnedID.slice(0, 8), { exact: true }).waitFor();
  const identities = await page.evaluate(() => state.overview.people.filter(p => p.label === 'Bob').map(p => ({ key: personKey(p), state: p.state, address: p.address })));
  assert(identities.some(p => p.key === pinnedID && p.state === 'pinned'));
  assert(identities.some(p => p.key === 'listed:carol/desk' && p.state === 'listed'));
  const claimedContact = page.locator('.side').getByText(/via carol\/desk · not checked yet/);
  await claimedContact.waitFor();
  await capture(page, name + '-duplicate-contacts', width);
  await claimedContact.click();
  await page.getByText('Bob on 1 device', { exact: true }).click();
  const disclosure = page.locator('#hub .identity-details');
  await disclosure.getByText(/First contact: keys come from your workspace directory/).waitFor();
  await disclosure.getByText(/display name is self-chosen, not proof/).waitFor();
  await disclosure.getByText(/Compare a device fingerprint with its owner through another channel/).waitFor();
  await capture(page, name + '-first-contact', width);
  assert.equal(await page.evaluate(() => state.overview.people.find(p => p.address === 'carol/desk').state), 'listed');
}
(async () => {
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM,
    args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const contexts = [];
  for (let i = 0; i < 3; i++) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }); contexts.push(context);
    await context.route('**/*', route => {
      if (allowed.has(new URL(route.request().url()).origin)) return route.continue();
      external.push(new URL(route.request().url()).origin); return route.abort();
    });
  }
  await contexts[0].addInitScript(() => {
    window.__personPush = [];
    import('/assets/engine.mjs').then(({ Engine }) => {
      const dispatch = Engine.prototype.dispatch;
      Engine.prototype.dispatch = function (event, data) {
        if (event === 'members') {
          const members = JSON.parse(data).members || [];
          window.__personPush.push(members.filter(m => m.person).map(m => ({ address: m.address, ...m.person })));
        }
        return dispatch.call(this, event, data);
      };
    });
  });
  const linked = await contexts[0].newPage(), native = await contexts[1].newPage(), remote = await contexts[2].newPage();
  for (const page of [linked, native, remote]) {
    page.setDefaultTimeout(15000);
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
    page.on('response', async response => {
      const file = new URL(response.url()).pathname;
      if (['/assets/engine.mjs', '/assets/wire.mjs', '/assets/app.js', '/assets/app.css', '/assets/device.mjs'].includes(file)) {
        try { testedAssets[file] = createHash('sha256').update(await response.body()).digest('hex'); } catch { /* only completed assets recorded */ }
      }
    });
  }
  const raw = fs.readFileSync(path.join(world, 'link'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url'));
  const invite = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  // Production browser accepts the same-origin TLS endpoint authenticated by
  // the fixture-only exact SPKI above, rather than a separate invite CA field.
  delete invite.cert;
  offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  await linked.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await linked.locator('#join-name').fill('tablet'); await linked.locator('button.join-go').click();
  let request;
  for (let attempt = 0; attempt < 100 && !request; attempt++) {
    request = cli('alice', 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*tablet/m)?.[1];
    if (!request) await idle(100);
  }
  assert(request, 'actual device-link request'); cli('alice', 'person', 'approve', request);
  await ready(linked);
  await linked.waitForFunction(() => state.overview.device.online && state.overview.directory.current);
  const conv = (await api(linked, '/api/dm/new', { address: 'bob/desk' })).id;
  await api(linked, '/api/dm/send', { conv, body: 'History before display-name changes' });
  await native.goto(nativeURL); await remote.goto(remoteURL); await ready(native); await ready(remote);
  for (const page of [native, remote]) await page.waitForFunction(id => state.overview.dms.some(d => d.id === id), conv);
  await remote.waitForFunction(() => state.overview.people.some(p => p.label === 'Alice' && p.state === 'pinned'));
  const baseline = await Promise.all([linked, native, remote].map(p => snapshot(p, conv)));
  assert.equal(baseline[0].person, baseline[1].person); assert.notEqual(baseline[0].person, baseline[2].person);
  await linked.evaluate(() => { const e = agentnetWorkspaces.shell.members.get('default').engine; window.__personKeys = e.keys; });
  for (const width of [390, 1280]) {
    for (const page of [linked, native, remote]) await page.setViewportSize({ width, height: 900 });
    // Browser UI -> real CAS -> Hub members push -> native linked/remote UI.
    await rename(linked, 'Bob', width, 'browser');
    await native.waitForFunction(() => state.overview.person?.label === 'Bob');
    await remote.waitForFunction(id => state.overview.people.some(p => p.person === id && p.label === 'Bob' && p.state === 'pinned'), baseline[0].person);
    for (const [i, page] of [linked, native, remote].entries()) assert.deepEqual(await snapshot(page, conv), baseline[i]);
    await profile(native); await native.getByText('You on 2 devices', { exact: true }).click();
    await native.getByText(/Your signed person record/).waitFor(); await capture(native, 'linked-native-profile', width); await native.locator('#settings-close').click();
    // Pinned IDs and separate unpinned address claims must never merge by label.
    // Opening actual identity disclosure grants nothing; no Carol DM is opened.
    await duplicateContacts(remote, baseline[0].person, width, 'remote-native');
    await duplicateContacts(linked, baseline[2].person, width, 'linked-browser');
    // Native UI -> real CAS -> Hub members push -> actual linked Engine/IDB UI.
    const pushBefore = await linked.evaluate(() => __personPush.length), label = 'Alice renamed from laptop ' + width;
    await rename(native, label, width, 'native');
    await linked.waitForFunction(label => state.overview.person?.label === label, label);
    await remote.waitForFunction(({ id, label }) => state.overview.people.some(p => p.person === id && p.label === label && p.state === 'pinned'), { id: baseline[0].person, label });
    const ref = await linked.evaluate(({ before, id }) => {
      const head = agentnetWorkspaces.shell.members.get('default').engine.me;
      return __personPush.slice(before).flat().find(r => r.id === id && r.seq === head.seq && r.hash === head.hash);
    }, { before: pushBefore, id: baseline[0].person });
    assert(ref, 'Hub members push carries the exact newly verified person head');
    await profile(linked); await linked.getByText('You on 2 devices', { exact: true }).click(); await capture(linked, 'linked-browser-profile', width); await linked.locator('#settings-close').click();
    for (const [i, page] of [linked, native, remote].entries()) assert.deepEqual(await snapshot(page, conv), baseline[i]);
    assert.equal(await linked.evaluate(() => __personKeys === agentnetWorkspaces.shell.members.get('default').engine.keys), true);
    checks.push({ width, browserAndNativeProfileSave: true, linkedAndRemotePush: true, identityKeysHistoryRetained: true, duplicateLabelsDistinct: true, firstContactHonest: true });
  }
  const persisted = await snapshot(linked, conv);
  await linked.reload(); await ready(linked);
  assert.deepEqual(await snapshot(linked, conv), persisted);
  assert.equal(await linked.evaluate(() => agentnet.platform), 'browser');
  assert.equal(await linked.evaluate(() => state.overview.person.label), 'Alice renamed from laptop 1280');
  assert.deepEqual(errors, []); assert.deepEqual(external, []);
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, checks, shots, errors, external, actualIndexedDBReload: true, world,
    person: baseline[0].person, nativeFingerprint: baseline[1].fingerprint, browserFingerprint: baseline[0].fingerprint, testedAssets }, null, 2), { mode: 0o600 });
  console.log('PASS person live fixture: browser/native profile rename, linked/remote push, IDs/keys/history retained, duplicate labels and first-contact wording, IndexedDB reload, desktop1280/390');
})().catch(async e => {
  console.error(e.message);
  if (browser) for (const [i, context] of browser.contexts().entries()) {
    const page = context.pages()[0];
    if (page) { await page.screenshot({ path: path.join(evidence, 'failure-' + i + '.png') });
      console.error(JSON.stringify({ view: i, errors, text: (await page.locator('body').innerText()).slice(0, 2500),
        overview: await page.evaluate(() => typeof state !== 'undefined' ? state.overview : null).catch(() => null) })); }
  }
  process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); });
