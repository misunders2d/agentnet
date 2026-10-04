// One real first-contact disclosure, using the existing isolated person world.
// No fake provider/fingerprint; production Engine, Hub, IDB and page renderer.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_PERSON_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
assert(world && evidence && process.env.AGENTNET_PERSON_BINARY);
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const cli = (...args) => execFileSync(process.env.AGENTNET_PERSON_BINARY, ['--home', path.join(world, 'alice'), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const errors = [], external = [], assets = {};
let browser;
async function records(page) {
  return page.evaluate(async () => {
    const e = agentnetWorkspaces.shell.members.get('default').engine;
    return JSON.stringify(await Promise.all(['pins', 'persons', 'convs'].map(async s => [s, await e.store.all(s)])));
  });
}
(async () => {
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM,
    args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const context = await browser.newContext({ viewport: { width: 390, height: 900 } });
  await context.route('**/*', route => {
    if (new URL(route.request().url()).origin === origin) return route.continue();
    external.push(new URL(route.request().url()).origin); return route.abort();
  });
  const page = await context.newPage(); page.setDefaultTimeout(15000);
  page.on('pageerror', e => errors.push(e.message));
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('response', async response => {
    const file = new URL(response.url()).pathname;
    if (['/assets/engine.mjs', '/assets/wire.mjs', '/assets/device.mjs', '/assets/app.js', '/assets/app.css'].includes(file)) {
      try { assets[file] = createHash('sha256').update(await response.body()).digest('hex'); } catch { /* completed responses only */ }
    }
  });
  const raw = fs.readFileSync(path.join(world, 'link'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url'));
  const invite = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  // Existing fixture's exact disposable SPKI authenticates the same-origin
  // endpoint; no change to the user's trust store or production TLS policy.
  delete invite.cert;
  offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  await page.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await page.locator('#join-name').fill('tablet'); await page.locator('button.join-go').click();
  let request;
  for (let attempt = 0; attempt < 100 && !request; attempt++) {
    request = cli('person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*tablet/m)?.[1];
    if (!request) await new Promise(r => setTimeout(r, 100));
  }
  assert(request, 'actual device-link request'); cli('person', 'approve', request);
  await page.waitForFunction(() => typeof state !== 'undefined' && state.overview?.device.online && state.overview.directory.current);
  await page.locator('#search').fill('Bob');
  await page.waitForFunction(() => ['bob/desk', 'carol/desk'].every(a => state.overview.people.some(p => p.address === a && p.label === 'Bob' && p.state === 'listed')));
  const claims = await page.evaluate(() => state.overview.people.filter(p => p.label === 'Bob').map(p => ({ key: personKey(p), state: p.state, address: p.address })));
  assert.deepEqual(claims.map(p => p.key).sort(), ['listed:bob/desk', 'listed:carol/desk']);
  assert(claims.every(p => p.state === 'listed'));
  // Independently derive the expected standard fingerprint from the real
  // directory public keys; the UI value does not define its own expectation.
  const profile = await page.evaluate(() => agentnetWorkspaces.shell.members.get('default').engine.profile('carol/desk'));
  const device = profile.person.devices.find(d => d.address === 'carol/desk');
  const hash = createHash('sha256').update(Buffer.from(device.sign_key, 'base64')).update(device.box_recipient, 'utf8').digest('hex').slice(0, 32);
  const expected = hash.match(/.{8}/g).join('-');
  const before = await records(page);
  await page.evaluate(async () => {
    const e = agentnetWorkspaces.shell.members.get('default').engine;
    await e.fillListed(); await e.overview();
  });
  await page.locator('.side').getByText(/via carol\/desk · not checked yet/).click();
  await page.getByText('Bob on 1 device', { exact: true }).click();
  const disclosure = page.locator('#hub .identity-details');
  await disclosure.getByText(/First contact: keys come from your workspace directory and are not pinned here yet/).waitFor();
  await disclosure.getByText(/display name is self-chosen, not proof of who owns the keys/).waitFor();
  await disclosure.getByText(/Compare a device fingerprint with its owner through another channel/).waitFor();
  await disclosure.getByText('desk · carol/desk · Key: ' + expected, { exact: true }).waitFor();
  assert.equal(await records(page), before, 'fillListed/overview/disclosure do not change pins/persons/convs/grants');
  assert.equal(await page.evaluate(() => state.overview.people.find(p => p.address === 'carol/desk').state), 'listed');
  assert.equal(await page.evaluate(() => state.overview.dms.length), 0);
  const sizes = await page.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth,
    details: [...document.querySelectorAll('#hub .identity-details')].map(n => n.scrollWidth > n.clientWidth + 1) }));
  assert.equal(sizes.document, false); assert(sizes.details.every(x => !x));
  const shot = path.join(evidence, 'listed-first-contact-390.png'); await page.screenshot({ path: shot });
  assert.deepEqual(errors, []); assert.deepEqual(external, []);
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, width: 390, expected, claims,
    unchangedRecords: true, noDM: true, unpinned: true, sizes, errors, external, assets, world, shot }, null, 2), { mode: 0o600 });
  console.log('PASS real listed first-contact: exact directory fingerprint, 390px disclosure, duplicate labels distinct, no pin/person/DM/grant writes');
})().catch(async e => {
  console.error(e.message);
  if (browser) for (const context of browser.contexts()) {
    const page = context.pages()[0];
    if (page) await page.screenshot({ path: path.join(evidence, 'failure.png') });
  }
  process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); });
