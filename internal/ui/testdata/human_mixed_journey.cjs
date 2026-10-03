// Actual mixed native/browser human-guest journey (H5) over a real loopback
// Hub, real native daemons and real linked browser Engines/IndexedDB. Visible
// controls perform every user action; the CLI only seeds Bob's earlier
// history, approves browser links and restarts one daemon (offline check).
//
// Fixture capability seam (test-only): a session that already advertises the
// capabilities a stage needs (hgp1, agr1, clr1; released clients do) is left
// as it is; for a session whose published caps lack one, the journey signs
// one more record for that same session with that device's own key: the
// session's published caps plus the missing ones, at the current time
// (strictly after the session's own record, normal validity), then reads the
// signed profile back. A device publishes caps only when it connects, so the
// record holds until that device reconnects; the journey signs again after it
// restarts or reloads a device.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const { X509Certificate, createHash, webcrypto } = require('node:crypto');
const { pathToFileURL } = require('node:url');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_HUMAN_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS, binary = process.env.AGENTNET_HUMAN_BINARY;
assert(world && evidence && binary, 'explicit private fixture paths required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const uiURL = text => text.match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const nativeURL = n => uiURL(fs.readFileSync(path.join(world, n + '-ui'), 'utf8'));
const allowed = new Set([origin, ...['alice', 'bob', 'dana', 'carol'].map(n => new URL(nativeURL(n)).origin)]);
const cli = (n, ...args) => execFileSync(binary, ['--home', path.join(world, n), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const assetNames = ['app.js', 'app.css', 'lenses.js', 'engine.mjs', 'wire.mjs', 'device.mjs'];
const expectedAssets = process.env.AGENTNET_HUMAN_ASSET_MANIFEST
  ? Object.fromEntries(fs.readFileSync(process.env.AGENTNET_HUMAN_ASSET_MANIFEST, 'utf8').trim().split('\n').map(l => { const [h, f] = l.trim().split(/\s+/); return [path.basename(f), h]; }))
  : Object.fromEntries(assetNames.map(n => [n, digest(fs.readFileSync(path.join('internal/ui/static', n)))]));
const widths = [1440, 390];
const errors = [], httpErrors = [], external = [], shots = [], checks = [], assetReads = [], served = {}, fixtureCaps = [], spawned = [];
const idle = ms => new Promise(r => setTimeout(r, ms));
let browser, stage = 'setup', wire;
const ready = p => p.waitForFunction(() => typeof state !== 'undefined' && state.overview && state.overview.directory.current, null, { timeout: 25000 });
async function until(label, predicate, timeout = 30000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await predicate()) return; await idle(150); }
  throw Error('Timed out: ' + label);
}
async function capture(p, name, focus) {
  for (const width of widths) {
    await p.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    await idle(250);
    await p.waitForFunction(() => !document.querySelector('#zoom .zoom-arrive-in, #zoom .zoom-arrive-out'));
    if (focus) { await p.locator(focus).scrollIntoViewIfNeeded(); await idle(150); } // the gated state itself, at every width
    const o = await p.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth, dialogs: [...document.querySelectorAll('dialog[open]')].some(d => d.scrollWidth > d.clientWidth + 1) }));
    assert(!o.document && !o.dialogs, name + ' ' + width + ': horizontal overflow');
    const file = path.join(evidence, `human-mixed-${name}-${width}.png`);
    await p.screenshot({ path: file }); shots.push(file);
  }
  await p.setViewportSize({ width: 1440, height: 900 });
}
async function home(p) {
  if (await p.locator('#dialog').isVisible()) await p.locator('#dialog-cancel, #dialog-ok').first().click();
  if (await p.locator('#settings').isVisible()) await p.locator('#settings-close').click();
  for (let i = 0; i < 3 && !(await p.locator('#nav-chats').isVisible()); i++) await p.locator('#back').click();
  await p.locator('#nav-chats').click(); await p.locator('#search').waitFor({ state: 'visible' });
}
// openDM is external_agent_journey.cjs's: the visible person, then the topic.
async function openDM(p, id) {
  await home(p);
  await p.waitForFunction(id => (state.overview.dms || []).some(d => d.id === id), id, { timeout: 30000 });
  const label = await p.evaluate(id => (state.overview.dms || []).find(d => d.id === id).peer.label, id);
  await p.locator('#conv-list .contact-item').filter({ hasText: label }).locator('button.contact').click();
  if (await p.evaluate(id => state.dm !== id, id)) {
    const summary = await p.evaluate(id => { const d = (state.overview.dms || []).find(d => d.id === id); return { title: d.title, count: d.count }; }, id);
    const rows = p.locator('#hub .thread-row').filter({ hasText: summary.title });
    const row = await rows.count() === 1 ? rows : rows.filter({ has: p.locator('.thread-count').filter({ hasText: new RegExp('^' + summary.count + '$') }) });
    assert.equal(await row.count(), 1, 'visible title/count identifies intended topic');
    await row.click();
  }
  await p.waitForFunction(id => state.dm === id && state.dmData?.id === id, id);
}
const readDM = (p, id) => p.evaluate(id => agentnet.api('/api/dm?id=' + id), id);
const has = async (p, conv, body) => (await readDM(p, conv)).messages.some(m => m.body === body);
const msgOf = async (p, conv, body) => (await readDM(p, conv)).messages.find(m => m.body === body);
async function arrives(conv, body, pages) { for (const [n, p] of Object.entries(pages)) await until(n + ' receives ' + body, () => has(p, conv, body)); }
// absent: after the sender's copies are delivered and every other recipient
// has it, the excluded device still has nothing (checked for a while).
async function absent(conv, body, pages) {
  await idle(3000);
  for (const [n, p] of Object.entries(pages)) assert.equal(await has(p, conv, body), false, n + ' must not receive ' + body);
}
async function send(p, body, file) {
  if (file) { await p.locator('#file-input').setInputFiles(file); await p.locator('#attach-list').getByText(file.name, { exact: true }).waitFor(); }
  await p.locator('#body').fill(body); await p.locator('#send').click();
  await p.waitForFunction(body => state.dmData.messages.some(m => m.body === body), body);
  assert.equal(await p.locator('#compose-error').innerText(), '', 'no compose error for ' + body);
}
async function download(p, conv, body, name, bytes, label) {
  await openDM(p, conv);
  await p.waitForFunction(({ body, name }) => state.dmData.messages.some(m => m.body === body && (m.attachments || []).some(f => f.name === name)), { body, name });
  const m = await p.evaluate(body => state.dmData.messages.find(m => m.body === body), body);
  let file = p.locator('[id="m-' + m.id + '"] .file').filter({ hasText: name });
  const get = file.getByRole('button', { name: /^Get it from / });
  if (await get.count()) { await get.click(); await idle(500); file = p.locator('[id="m-' + m.id + '"] .file').filter({ hasText: name }); }
  const link = file.getByRole('link', { name: 'Download ' + name, exact: true });
  for (let i = 0; i < 40 && !(await link.count()); i++) {
    const openBtn = file.getByRole('button', { name: 'Open', exact: true });
    if (await openBtn.count() && await openBtn.isVisible()) await openBtn.click();
    await idle(500);
  }
  const pending = p.waitForEvent('download');
  await link.click();
  const saved = path.join(world, 'download-' + label);
  await (await pending).saveAs(saved);
  assert.deepEqual(fs.readFileSync(saved), bytes, label + ': exact bytes');
  checks.push({ gate: 'exact file bytes', label, name, sha256: digest(bytes), size: bytes.length });
}
// Sender-side evidence: the copies a device made for one turn.
function nativeCopies(n, conv, body) {
  const out = execFileSync('python3', ['-c', 'import sqlite3,sys,json;c=sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True);print(json.dumps([r[0] for r in c.execute("SELECT recipient FROM outbox WHERE conv=? AND body=?",(sys.argv[2],sys.argv[3]))]))',
    path.join(world, n, 'agent.db'), conv, body], { encoding: 'utf8' });
  return JSON.parse(out);
}
const browserCopies = (p, conv, body) => p.evaluate(async ({ conv, body }) => {
  const db = await new Promise((res, rej) => { const r = indexedDB.open('agentnet'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
  const rows = await new Promise((res, rej) => { const r = db.transaction('outbox').objectStore('outbox').getAll(); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
  db.close();
  return rows.filter(r => r.conv === conv && r.body === body).map(r => r.to);
}, { conv, body });
async function participant(p, label) {
  await p.locator('#agents .human-participant').filter({ hasText: label }).first().click();
  await p.locator('#dialog').waitFor({ state: 'visible' });
}
async function guestAction(p, label, button, shot, names = []) {
  await participant(p, label);
  await p.locator('#dialog').getByRole('button', { name: button, exact: true }).click();
  const text = await p.locator('#dialog').innerText();
  for (const n of names) assert(text.includes(n), shot + ': dialog names ' + n + ': ' + text);
  if (shot) await capture(p, shot);
  await p.locator('#dialog-ok').click(); await p.locator('#dialog').waitFor({ state: 'hidden' });
}
async function invite(p, conv, host, selected, shot, expectError) {
  await openDM(p, conv);
  await p.locator('#agents').getByRole('button', { name: '+ Add participants', exact: true }).click();
  await p.locator('#dialog').getByRole('button', { name: 'Invite a person…', exact: true }).click();
  await p.locator('#guest-host').fill(host);
  for (const body of selected) await p.locator('#dialog label.choice').filter({ hasText: body }).locator('input').check();
  await p.locator('#dialog label.choice').filter({ hasText: 'Share files attached' }).locator('input').check();
  if (shot) await capture(p, shot);
  await p.locator('#dialog-ok').click();
  if (expectError) {
    await until('refusal shown', async () => (await p.locator('#dialog-error').innerText()).includes(expectError));
    if (shot) await capture(p, shot + '-refused');
    await p.locator('#dialog-cancel').click(); await p.locator('#dialog').waitFor({ state: 'hidden' });
    return;
  }
  await p.locator('#dialog').waitFor({ state: 'hidden' });
}
const guestOf = async (p, conv, address) => (await readDM(p, conv)).guests?.find(g => g.host.address === address);
async function guestState(conv, address, want, pages) {
  for (const [n, p] of Object.entries(pages)) await until(n + ': ' + address + ' ' + want, async () => (await guestOf(p, conv, address))?.state === want);
}

// ---- fixture caps --------------------------------------------------------------------------
async function hubCall(keys, address, method, target, body = '') {
  const headers = { ...(await wire.signRequest(keys, address, method, target, body)), ...(body ? { 'Content-Type': 'application/json' } : {}) };
  const r = await fetch(origin + target, { method, headers, body: body || undefined });
  if (!r.ok) throw Error(method + ' ' + target + ': ' + r.status + ' ' + await r.text());
  const t = await r.text(); return t ? JSON.parse(t) : null;
}
async function nativeHGP(account, address, extra = [wire.CapHumanParticipation]) {
  const kf = JSON.parse(fs.readFileSync(path.join(world, account, 'identity.json'), 'utf8'));
  const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.from(kf.sign_seed, 'base64')]);
  const keys = { sign: await webcrypto.subtle.importKey('pkcs8', pkcs8, { name: 'Ed25519' }, false, ['sign']) };
  const call = (m, t, b) => hubCall(keys, address, m, t, b);
  await until(address + ' live caps', async () => { const p = await call('GET', '/v1/agents/' + address + '/profile'); return p.live && p.sessions?.length && p.sessions.every(s => (p.caps || []).some(c => c.session === s)); });
  const profile = await call('GET', '/v1/agents/' + address + '/profile');
  const records = [];
  for (const session of profile.sessions) {
    const own = (profile.caps || []).map(c => wire.parseCaps(c)).filter(c => c.address === address && c.session === session).sort((a, b) => b.ts - a.ts)[0];
    if (extra.every(c => own.caps.includes(c))) continue;
    while (Math.floor(Date.now() / 1000) <= own.ts) await idle(100); // strictly newer, current time
    const rec = await wire.newCaps(keys, address, session, [...new Set([...own.caps, ...extra])]);
    await call('PUT', '/v1/caps', wire.capsJSON(rec));
    records.push({ session, ts: rec.ts, own_ts: own.ts });
  }
  const pub = wire.parsePublic((await call('GET', '/v1/agents/' + address)).public);
  for (const c of extra) assert(await wire.profileSupports(await call('GET', '/v1/agents/' + address + '/profile'), address, (await pub).sign_key, c), address + ' fixture ' + c);
  fixtureCaps.push({ address, kind: 'native', records });
  return address;
}
// browserHGP is nativeHGP inside the page, with the Engine's own stored key.
async function browserHGP(p, extra = [wire.CapHumanParticipation]) {
  const r = await p.evaluate(async (extra) => {
    const wire = await import('/assets/wire.mjs');
    const db = await new Promise((res, rej) => { const r = indexedDB.open('agentnet'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    const id = await new Promise((res, rej) => { const r = db.transaction('kv').objectStore('kv').get('identity'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    db.close();
    const { keys, address } = id;
    const call = async (method, target, body = '') => {
      const headers = { ...(await wire.signRequest(keys, address, method, target, body)), ...(body ? { 'Content-Type': 'application/json' } : {}) };
      const r = await fetch(target, { method, headers, body: body || undefined, cache: 'no-store' });
      if (!r.ok) throw Error(method + ' ' + target + ': ' + r.status);
      const t = await r.text(); return t ? JSON.parse(t) : null;
    };
    let profile;
    for (let i = 0; i < 200; i++) {
      profile = await call('GET', '/v1/agents/' + address + '/profile');
      if (profile.live && profile.sessions?.length && profile.sessions.every(s => (profile.caps || []).some(c => c.session === s))) break;
      await new Promise(r => setTimeout(r, 150));
    }
    const records = [];
    for (const session of profile.sessions) {
      const own = (profile.caps || []).map(c => wire.parseCaps(c)).filter(c => c.address === address && c.session === session).sort((a, b) => b.ts - a.ts)[0];
      if (extra.every(c => own.caps.includes(c))) continue;
      while (Math.floor(Date.now() / 1000) <= own.ts) await new Promise(r => setTimeout(r, 100));
      const rec = await wire.newCaps(keys, address, session, [...new Set([...own.caps, ...extra])]);
      await call('PUT', '/v1/caps', wire.capsJSON(rec));
      records.push({ session, ts: rec.ts, own_ts: own.ts, prg1: own.caps.includes(wire.CapProgress) });
    }
    const pub = await wire.parsePublic((await call('GET', '/v1/agents/' + address)).public);
    let ok = true;
    for (const c of extra) ok = ok && await wire.profileSupports(await call('GET', '/v1/agents/' + address + '/profile'), address, pub.sign_key, c);
    return { address, records, ok };
  }, extra);
  assert(r.ok, r.address + ' fixture ' + extra.join(','));
  fixtureCaps.push({ address: r.address, kind: 'browser', records: r.records });
  return r.address;
}

(async () => {
  wire = await import(pathToFileURL(path.resolve('internal/ui/static/wire.mjs')).href);
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM, args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const pages = {};
  for (const name of ['phone', 'cphone', 'alice', 'bob', 'dana', 'carol']) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, acceptDownloads: true });
    await context.route('**/*', r => { const o = new URL(r.request().url()).origin; if (allowed.has(o)) return r.continue(); external.push(o); return r.abort(); });
    const p = pages[name] = await context.newPage(); p.setDefaultTimeout(20000);
    p.on('pageerror', e => errors.push(name + ': ' + (e.stack || e.message)));
    p.on('console', m => { if (m.type() === 'error') errors.push(name + ': ' + m.text()); });
    p.on('response', r => { if (r.status() >= 400) httpErrors.push({ page: name, status: r.status(), method: r.request().method(), path: new URL(r.url()).pathname, stage }); });
    p.on('response', r => { const asset = new URL(r.url()).pathname.split('/').pop(); if (expectedAssets[asset]) assetReads.push((async () => {
      const hash = digest(await r.body()); assert.equal(hash, expectedAssets[asset], name + ':' + asset); served[name + '/' + asset] = hash;
    })().catch(e => errors.push(e.message))); });
  }
  const { phone, cphone, alice, bob, dana, carol } = pages;
  stage = 'link browsers';
  for (const [p, account] of [[phone, 'alice'], [cphone, 'carol']]) {
    const raw = fs.readFileSync(path.join(world, 'link-' + account), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
    const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url')), inv = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
    delete inv.cert; offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(inv)).toString('base64url');
    await p.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
    await p.locator('#join-name').fill('phone'); await p.locator('button.join-go').click();
    let request;
    await until(account + ' browser link', () => { request = cli(account, 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*phone/m)?.[1]; return !!request; });
    cli(account, 'person', 'approve', request); await ready(p);
  }
  for (const n of ['alice', 'bob', 'dana', 'carol']) { await pages[n].goto(nativeURL(n)); await ready(pages[n]); }
  const A = await alice.evaluate(() => state.overview.me.address), PHONE = await phone.evaluate(() => state.overview.me.address);
  const CPHONE = await cphone.evaluate(() => state.overview.me.address), DANA = await dana.evaluate(() => state.overview.me.address);
  assert.equal(PHONE.split('/')[1], 'phone'); assert.equal(CPHONE, 'carol/phone'); assert.equal(DANA, 'dana/desk');

  stage = 'seed original private history';
  const conv = cli('bob', 'dm', 'new', A).trim();
  const bobSelected = Buffer.from('BOB_SELECTED_EXACT_BYTES\n'), bobExcluded = Buffer.from('BOB_EXCLUDED_PRIVATE_BYTES\n');
  fs.writeFileSync(path.join(world, 'bob-selected.txt'), bobSelected); fs.writeFileSync(path.join(world, 'bob-excluded.txt'), bobExcluded);
  cli('bob', 'dm', 'send', '--file', path.join(world, 'bob-selected.txt'), conv, 'Chosen native context');
  cli('bob', 'dm', 'send', '--file', path.join(world, 'bob-excluded.txt'), conv, 'Unselected private native history');
  await openDM(phone, conv);
  await until('phone has seeded history', () => has(phone, conv, 'Unselected private native history'));
  const phoneSelected = Buffer.from([0, 1, 2, 250, 251, 7, 0, 9]);
  await send(phone, 'Chosen browser context', { name: 'phone-selected.bin', mimeType: 'application/octet-stream', buffer: phoneSelected });
  await send(phone, 'Unselected private browser history');
  await arrives(conv, 'Unselected private browser history', { bob, alice });
  const rootDM = (await readDM(phone, conv));
  assert.equal(rootDM.role, 'member');

  if (process.env.AGENTNET_HUMAN_FOCUS === 'delete') {
    // Whole-conversation deletion on Alice's own devices: the browser phone
    // deletes (Cancel first), native alice/laptop applies it, Bob keeps his
    // copy; a new message reopens with only itself; then the native laptop
    // deletes and the phone applies it. clr1 as advertised, fixture-signed only where a session lacks it.
    stage = 'delete fixture caps';
    const sql = (n, q, ...args) => JSON.parse(execFileSync('python3', ['-c', 'import sqlite3,sys,json;c=sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True);print(json.dumps(c.execute(sys.argv[2],sys.argv[3:]).fetchall()))', path.join(world, n, 'agent.db'), q, ...args], { encoding: 'utf8' }));
    await nativeHGP('alice', A, [wire.CapConvClear]); await browserHGP(phone, [wire.CapConvClear]);
    const OLD = ['Chosen native context', 'Unselected private native history', 'Chosen browser context', 'Unselected private browser history'];
    const listed = (p) => p.evaluate(async (id) => ((await agentnet.api('/api/overview')).dms || []).some((d) => d.id === id), conv);
    const phoneRows = () => phone.evaluate(async () => {
      const db = await new Promise((res, rej) => { const r = indexedDB.open('agentnet'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
      const all = (s) => new Promise((res, rej) => { const r = db.transaction(s).objectStore(s).getAll(); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
      const keys = await new Promise((res, rej) => { const r = db.transaction('files').objectStore('files').getAllKeys(); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
      const rows = [...await all('inbox'), ...await all('outbox')], erased = await all('erased'); db.close();
      return { bodies: rows.map((r) => r.body || ''), keys, erased: erased.length };
    });
    const deleteVia = async (p, shot) => {
      await openDM(p, conv); await p.locator('#conversation-details').click();
      await p.locator('#dialog').getByRole('button', { name: 'Delete conversation…', exact: true }).click();
      await p.locator('#dialog').getByText('Delete the messages in this conversation from your devices?').waitFor();
      const text = await p.locator('#dialog').innerText();
      assert(/Others in this conversation keep their copies/.test(text) && /once it is connected and updated/.test(text) && !/everyone|all participants/i.test(text), shot + ': truthful dialog: ' + text);
      if (shot) await capture(p, shot);
    };
    stage = 'browser phone cancels, then deletes';
    await deleteVia(phone, 'delete-confirm-browser');
    await phone.locator('#dialog-cancel').click(); await phone.locator('#dialog').waitFor({ state: 'hidden' });
    assert(await listed(phone) && await has(phone, conv, 'Chosen native context'), 'Cancel deletes nothing');
    await deleteVia(phone);
    await phone.locator('#dialog-ok').click(); await phone.locator('#dialog').waitFor({ state: 'hidden' });
    await until('phone list drops the conversation', async () => !(await listed(phone)));
    const note = await phone.locator('#live').textContent();
    assert(/Deleted here\. Queued for your 1 other device/.test(note) && /Others in it keep their copies/.test(note), 'truthful result note: ' + note);
    await capture(phone, 'deleted-list-browser');
    const pr = await phoneRows();
    assert(pr.erased > 0 && !pr.bodies.some((b) => OLD.some((o) => b.includes(o))), 'phone keeps no deleted text');
    assert(!pr.keys.includes('kept/' + digest(phoneSelected)), 'phone dropped its kept copy of the sent file');
    checks.push({ gate: 'browser delete: Cancel keeps; Delete hides list/chat, erases text and kept file here', note, erased: pr.erased });
    stage = 'native own linked device applies';
    await until('alice/laptop list drops the conversation', async () => !(await listed(alice)), 60000);
    const left = sql('alice', "SELECT count(*) FROM inbox WHERE conv=?1 AND body<>'' AND coalesce(sub,'')=''", conv)[0][0] + sql('alice', "SELECT count(*) FROM outbox WHERE conv=?1 AND body<>'' AND coalesce(sub,'')=''", conv)[0][0];
    assert.equal(left, 0, 'native own device keeps no deleted text');
    assert(await has(bob, conv, 'Chosen browser context') && await has(bob, conv, 'Unselected private native history'), 'the other person keeps their copy');
    await home(alice); await capture(alice, 'deleted-list-native-own');
    checks.push({ gate: 'native own linked device applied the browser deletion; Bob unchanged' });
    stage = 'new message reopens with only itself';
    await openDM(bob, conv); await send(bob, 'Bob after deletion');
    for (const p of [phone, alice]) await until('reopened with the new message', async () => (await listed(p)) && (await has(p, conv, 'Bob after deletion')));
    for (const p of [phone, alice]) { const v = await readDM(p, conv); assert(!v.messages.some((m) => OLD.includes(m.body)), 'no old body after reopening'); assert(!v.messages.some((m) => (m.attachments || []).length), 'no old files after reopening'); }
    await openDM(phone, conv); await capture(phone, 'reopened-browser');
    checks.push({ gate: 'a later message reopens the conversation with only itself on both own devices' });
    stage = 'native laptop deletes; browser phone applies';
    await deleteVia(alice, 'delete-confirm-native');
    await alice.locator('#dialog-ok').click(); await alice.locator('#dialog').waitFor({ state: 'hidden' });
    await until('alice list drops it', async () => !(await listed(alice)));
    await until('phone applies the native deletion', async () => !(await listed(phone)), 60000);
    assert(!(await phoneRows()).bodies.some((b) => b.includes('Bob after deletion')), 'phone erased the newly deleted turn');
    assert(await has(bob, conv, 'Bob after deletion'), 'Bob keeps it');
    await home(phone); await capture(phone, 'deleted-again-browser');
    checks.push({ gate: 'native deletion applied by the browser own device' });
    await Promise.all(assetReads);
    assert.deepEqual(errors, []); assert.deepEqual(external, []);
    fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: 'actual mixed native/browser own-device conversation deletion (clr1 as advertised, fixture-signed only where missing)', checks, fixtureCaps, shots, expectedAssets, served, errors, httpErrors, external, world }, null, 2), { mode: 0o600 });
    console.log('PASS conversation deletion: ' + checks.length + ' gates, ' + shots.length + ' screenshots');
    return;
  }
  stage = 'fixture caps (no dana yet)';
  await nativeHGP('alice', A); await nativeHGP('bob', 'bob/desk');
  for (const p of [phone, cphone]) await browserHGP(p);

  stage = 'unsupported guest refusal';
  await invite(phone, conv, DANA, ['Chosen native context'], 'browser-invite-unsupported', 'cannot read human participation yet');
  assert.equal(((await readDM(phone, conv)).guests || []).length, 0, 'refused invitation leaves no guest');
  assert.equal(await dana.evaluate(id => (state.overview.dms || []).some(d => d.id === id), conv), false);
  checks.push({ gate: 'unsupported guest refused before any invitation', host: DANA });
  await nativeHGP('dana', DANA);

  stage = 'browser member invites native guest';
  await invite(phone, conv, DANA, ['Chosen native context'], 'browser-invite-native-guest');
  await openDM(dana, conv);
  let dv = await readDM(dana, conv);
  assert.equal(dv.role, 'human_guest'); assert.equal(dv.guests.find(g => g.host_here)?.state, 'invited');
  assert(!dv.messages.some(m => m.body), 'no selected context before consent: ' + JSON.stringify(dv.messages.map(m => m.body)));
  for (const n of ['Alice', 'Bob']) assert.equal(await dana.locator('#agents .participant').filter({ hasText: n }).count(), 1, 'pending native guest sees original ' + n);
  await capture(dana, 'native-guest-invited');
  await guestAction(dana, 'You', 'Join conversation…', 'native-guest-join-dialog', ['Alice', 'Bob']);
  await guestState(conv, DANA, 'active', { dana, phone, alice, bob });
  await until('dana gets selected context', () => has(dana, conv, 'Chosen native context'));
  dv = await readDM(dana, conv);
  assert(!dv.messages.some(m => /Unselected|Chosen browser/.test(m.body)), 'selected-only for dana');
  await download(dana, conv, 'Chosen native context', 'bob-selected.txt', bobSelected, 'dana-selected-native-file');
  checks.push({ gate: 'browser-invited native guest: consent, selected-only text+file', pid: (await guestOf(phone, conv, DANA)).pid });

  stage = 'native member invites browser guest';
  await invite(bob, conv, CPHONE, ['Chosen browser context'], 'native-invite-browser-guest');
  await openDM(cphone, conv);
  let cv = await readDM(cphone, conv);
  assert.equal(cv.role, 'human_guest'); assert(!cv.messages.some(m => m.body), 'no selected context before browser consent');
  for (const n of ['Alice', 'Bob']) assert.equal(await cphone.locator('#agents .participant').filter({ hasText: n }).count(), 1, 'pending browser guest sees original ' + n);
  await capture(cphone, 'browser-guest-invited');
  await guestAction(cphone, 'You', 'Join conversation…', 'browser-guest-join-dialog', ['Alice', 'Bob']);
  await guestState(conv, CPHONE, 'active', { cphone, phone, alice, bob });
  await until('carol gets selected context', () => has(cphone, conv, 'Chosen browser context'));
  cv = await readDM(cphone, conv);
  assert(!cv.messages.some(m => /Unselected|Chosen native/.test(m.body)), 'selected-only for carol');
  await download(cphone, conv, 'Chosen browser context', 'phone-selected.bin', phoneSelected, 'carol-selected-browser-file');
  assert.equal(await carol.evaluate(id => (state.overview.dms || []).some(d => d.id === id), conv), false, 'guest participation is device-bound: carol/desk gets no conversation');
  checks.push({ gate: 'native-invited browser guest: consent, selected-only text+file, device-bound', pid: (await guestOf(bob, conv, CPHONE)).pid });

  if (process.env.AGENTNET_HUMAN_FOCUS === 'typing') {
    stage = 'guest typing';
    for (const [n, p, other, label] of [['dana', dana, CPHONE, 'Carol'], ['cphone', cphone, DANA, 'Dana']]) await until(n + ' knows the other accepted guest', async () => (await guestOf(p, conv, other))?.state === 'active');
    const line = p => p.locator('#typing-line');
    const typing = async (who, watchers, name) => {
      await openDM(who, conv); for (const p of watchers) await openDM(p, conv);
      await who.locator('#body').click();
      let on = true; const keys = (async () => { while (on) { await who.locator('#body').pressSequentially('x'); await idle(700); } })();
      try {
        for (const [n, p] of watchers.map((p, i) => [i, p])) await until('typing shown to watcher ' + n + ' for ' + name, async () => (await line(p).isVisible()) && (await line(p).innerText()).includes(name + ' is typing'));
        for (const p of watchers) await capture(p, 'typing-' + name.toLowerCase() + '-' + (p === dana ? 'dana' : p === cphone ? 'cphone' : p === bob ? 'bob' : 'phone'));
      } finally { on = false; await keys; await who.locator('#body').fill(''); }
    };
    for (const [n, p] of Object.entries({ dana, cphone })) {
      const lines = (await readDM(p, conv)).messages.filter(m => m.event).map(m => m.event);
      assert.equal(new Set(lines).size, lines.length, n + ': one timeline row per signed record: ' + JSON.stringify(lines));
    }
    await typing(cphone, [dana, bob, phone], 'Carol');   // browser guest -> native guest, native member, browser member
    await typing(dana, [cphone, phone], 'Dana');         // native guest -> browser guest, browser member
    await typing(phone, [dana, cphone], 'Alice');        // browser member -> both guests
    // End removes a shown indicator at view time, before its expiry.
    await openDM(dana, conv); await openDM(cphone, conv); await cphone.locator('#body').click(); await cphone.locator('#body').pressSequentially('y');
    const typed = Date.now();
    await until('dana shows carol typing', async () => (await line(dana).innerText()).includes('Carol is typing'));
    await participant(cphone, 'You'); await cphone.locator('#dialog').getByRole('button', { name: 'Leave conversation…', exact: true }).click(); await cphone.locator('#dialog-ok').click();
    await until('dana applies carol leaving', async () => (await guestOf(dana, conv, CPHONE))?.state === 'dismissed', 10000);
    await until('indicator removed after end', async () => !(await line(dana).isVisible()) || !(await line(dana).innerText()).includes('Carol'), 3000);
    const removedAfter = Date.now() - typed;
    checks.push({ gate: 'guest typing visible both directions at 1440/390; removed after end', removedAfterMs: removedAfter, beforeExpiry: removedAfter < 5000 });
    await capture(dana, 'typing-removed-after-end');
    await Promise.all(assetReads);
    assert.deepEqual(errors, []); assert.deepEqual(external, []);
    fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: 'focused guest names + typing over actual mixed world (not a full H5 rerun)', checks, fixtureCaps, shots, expectedAssets, served, errors, httpErrors, external, world }, null, 2), { mode: 0o600 });
    console.log('PASS focused guest names + typing: ' + checks.length + ' gates, ' + shots.length + ' screenshots');
    return;
  }
  if (process.env.AGENTNET_HUMAN_FOCUS === 'assistant') {
    // Guests address Bob's own (member-hosted) assistant under Bob's own
    // permissions; the stub responder proves transport only (no model).
    stage = 'member-hosted assistant';
    for (const [n, p, other] of [['dana', dana, CPHONE], ['cphone', cphone, DANA]]) await until(n + ' knows the other accepted guest', async () => (await guestOf(p, conv, other))?.state === 'active');
    const sql = (n, q, ...args) => JSON.parse(execFileSync('python3', ['-c', 'import sqlite3,sys,json;c=sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True);print(json.dumps(c.execute(sys.argv[2],sys.argv[3:]).fetchall()))', path.join(world, n, 'agent.db'), q, ...args], { encoding: 'utf8' }));
    const bobState = id => sql('bob', 'SELECT state FROM inbox WHERE id=?', id)[0]?.[0] || '';
    // The assistant's own reaction to a guest's request needs agr1 beside hgp1 (as advertised; fixture-signed only where a session lacks them).
    for (const [n, addr] of [['alice', A], ['bob', 'bob/desk'], ['dana', DANA]]) await nativeHGP(n, addr, [wire.CapHumanParticipation, wire.CapAgentReaction]);
    for (const p of [phone, cphone]) await browserHGP(p, [wire.CapHumanParticipation, wire.CapAgentReaction]);
    const NOTE = 'PRIVATE_ASSISTANT_NOTE_RENDERED';
    const grantLID = (await msgOf(bob, conv, 'Chosen native context')).lid;
    const apid = cli('bob', 'dm', 'invite', '--grant', grantLID, '--note', NOTE, conv, 'bob/desk').trim().split(/\s+/)[0];
    cli('bob', 'dm', 'accept-agent', apid);
    for (const [n, p] of Object.entries({ dana, cphone })) await until(n + ' may address the assistant', async () => (await readDM(p, conv)).agents?.find(a => a.pid === apid)?.can_ask === true);
    // Privacy: guests hold the assistant's public scope and acceptance only.
    const danaLeak = sql('dana', `SELECT count(*) FROM participation_events WHERE pid=?1 AND (type='invite' OR instr(event,?2)>0 OR instr(event,'"grant"')>0)`, apid, NOTE)[0][0] +
      sql('dana', `SELECT count(*) FROM inbox WHERE instr(coalesce(body,''),?1)>0 OR instr(coalesce(human,''),?1)>0`, NOTE)[0][0];
    assert.equal(danaLeak, 0, 'native guest store holds no assistant invitation, note or grant');
    const cphoneLeak = await cphone.evaluate(async ({ apid, NOTE }) => {
      const db = await new Promise((res, rej) => { const r = indexedDB.open('agentnet'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
      const all = s => new Promise((res, rej) => { const r = db.transaction(s).objectStore(s).getAll(); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
      const rows = [...await all('inbox'), ...await all('outbox')]; db.close();
      return rows.filter(r => JSON.stringify(r).includes(NOTE) || r.pid === apid && r.sub === 'event' && /"type":"invite"|"grant"/.test(r.body)).length;
    }, { apid, NOTE });
    assert.equal(cphoneLeak, 0, 'browser guest store holds no assistant invitation, note or grant');
    checks.push({ gate: 'guests learn the assistant from its public scope only (no invitation/note/grant bytes)', apid });
    await openDM(dana, conv); await send(dana, 'Ambient guest chatter, not for the assistant');
    await arrives(conv, 'Ambient guest chatter, not for the assistant', { bob, cphone });
    const ask = async (p, kind, body, shot) => {
      await openDM(p, conv);
      await p.locator('#mention-button').click();
      await p.locator('#mentions .mention-row').first().waitFor({ state: 'visible' });
      if (shot) await capture(p, shot);
      await p.locator('#mentions .mention-row').first().click();
      await p.waitForFunction(pid => state.dmAgent === pid, apid);
      await p.locator('input[name="kind"][value="' + kind + '"]').check();
      await p.locator('#body').fill(body); await p.locator('#send').click();
      await p.waitForFunction(body => state.dmData.messages.some(m => m.body === body), body);
      assert.equal(await p.locator('#compose-error').innerText(), '', 'no compose error for ' + body);
      return (await msgOf(p, conv, body)).lid;
    };
    const answerOf = async (p, lid) => (await readDM(p, conv)).messages.find(m => m.reply_to === lid && m.kind === 'answer' && m.body.includes('Synthetic assistant answer'));
    const answered = async (lid, pages) => { for (const [n, p] of Object.entries(pages)) await until(n + ' gets the answer to ' + lid, async () => !!(await answerOf(p, lid))); };
    const captureAnswer = async (p, lid, shot) => {
      await openDM(p, conv); const a = await answerOf(p, lid);
      const author = await p.locator('[id="m-' + a.id + '"]').innerText();
      assert(author.includes("Bob's agent") && !author.includes('as their AgentNet says'), shot + ': answer names the participant, not a raw host claim: ' + author);
      await capture(p, shot, '[id="m-' + a.id + '"]');
    };
    stage = 'browser guest question waits, then one-time acceptance';
    const q1 = await ask(cphone, 'question', 'Browser guest asks the assistant', 'browser-guest-mention');
    await until('bob holds it awaiting acceptance', () => bobState(q1) === 'awaiting');
    await idle(1000); assert.equal(bobState(q1), 'awaiting', 'unapproved guest question does not run');
    await arrives(conv, 'Browser guest asks the assistant', { phone, alice, dana });
    const hostAccept = async (lid, button, wording, shot) => { // the host's own visible one-time acceptance (existing row action)
      await openDM(bob, conv);
      const row = bob.locator('[id="m-' + lid + '"]');
      await until('host row offers acceptance for ' + lid, async () => (await row.getByRole('button', { name: button, exact: true }).count()) === 1);
      assert((await row.innerText()).includes(wording), shot + ': truthful awaiting wording: ' + await row.innerText());
      await capture(bob, shot, '[id="m-' + lid + '"]');
      await row.getByRole('button', { name: button, exact: true }).click();
      await bob.locator('#dialog').waitFor({ state: 'visible' }); await bob.locator('#gate').check();
      await bob.locator('#dialog-ok').click(); await bob.locator('#dialog').waitFor({ state: 'hidden' });
    };
    await hostAccept(q1, 'Let your responder answer…', "Needs you: a guest's question runs only if you accept it or approve them", 'host-guest-question-awaiting');
    await answered(q1, { cphone, dana, phone, alice, bob });
    await captureAnswer(cphone, q1, 'browser-guest-answered');
    // The assistant's own reaction to the browser guest's question: its captured audience, shown as the assistant.
    const reactedBy = async (p) => ((await readDM(p, conv)).messages.find((m) => m.lid === q1 && !m.reply_to)?.reactions || []).find((r) => r.emoji === '👀')?.by || [];
    for (const [n, p] of Object.entries({ cphone, dana, phone, alice })) await until(n + ' sees the assistant reaction', async () => (await reactedBy(p)).some((b) => b.assistant));
    for (const [n, p] of Object.entries({ cphone, dana, phone, alice })) {
      const by = await reactedBy(p);
      assert(by.length === 1 && by[0].assistant && !by[0].id.startsWith('person') && by[0].id.startsWith('assistant:'), n + ': exactly the assistant reacts, distinct from any person: ' + JSON.stringify(by));
    }
    await openDM(cphone, conv); { const row = (await readDM(cphone, conv)).messages.find((m) => m.lid === q1 && !m.reply_to); await capture(cphone, 'browser-guest-assistant-reaction', '[id="m-' + row.id + '"]'); }
    checks.push({ gate: 'native assistant reaction to a guest request reaches both guests and members with the exact assistant actor', lid: q1 });
    checks.push({ gate: 'browser guest @question: awaiting until the host accepts once on its own page, answer to whole captured audience', lid: q1 });
    stage = 'native guest question under standing approval';
    cli('bob', 'approve', DANA);
    const q2 = await ask(dana, 'question', 'Native guest asks the assistant', 'native-guest-mention');
    await answered(q2, { dana, cphone, phone, alice, bob });
    await captureAnswer(dana, q2, 'native-guest-answered');
    for (const [n, p] of Object.entries({ dana, cphone })) {
      const texts = (await readDM(p, conv)).messages.filter(m => m.event && m.pid === apid).map(m => m.event);
      assert.equal(texts.length, 2, n + ': the assistant shows once as invited and once as accepted: ' + JSON.stringify(texts));
    }
    const prompts = fs.readFileSync(path.join(world, 'bob-prompts.log'), 'utf8');
    assert(prompts.includes('Chosen native context') && !/Unselected private|Ambient guest chatter/.test(prompts), 'assistant gets granted context and addressed requests only');
    assert.equal(prompts.split('Native guest asks the assistant').length - 1, 1, 'request appears once in its prompt');
    checks.push({ gate: 'native guest @question runs directly under the host approval of that guest; prompt has granted context + request only', lid: q2 });
    stage = 'guest task waits; guest end stops it';
    const t1 = await ask(cphone, 'task', 'Browser guest task for the assistant');
    await until('bob holds the task awaiting', () => bobState(t1) === 'awaiting');
    await openDM(cphone, conv);
    await guestAction(cphone, 'You', 'Leave conversation…');
    await guestState(conv, CPHONE, 'dismissed', { phone, alice, bob, dana });
    await hostAccept(t1, 'Accept and run…', 'Needs you: tasks run only if you accept them', 'host-guest-task-awaiting');
    await until('ended guest task is not run', () => bobState(t1) === 'not_run');
    await until('host row says the guest ended first', async () => (await bob.locator('[id="m-' + t1 + '"]').innerText()).includes('Not run: the guest who asked left or was removed first.'));
    assert(!fs.readFileSync(path.join(world, 'bob-prompts.log'), 'utf8').includes('Browser guest task'), 'ended guest task never reached the assistant');
    await openDM(bob, conv); await capture(bob, 'host-after-guest-end');
    checks.push({ gate: 'guest task needs host decision; after the guest leaves even the host page one-time accept does not run it; row says the guest ended first', lid: t1 });
    await Promise.all(assetReads);
    assert.deepEqual(errors, []); assert.deepEqual(external, []);
    fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: 'actual mixed native/browser guests addressing a member-hosted assistant (synthetic responder, not a model)', checks, fixtureCaps, shots, expectedAssets, served, errors, httpErrors, external, world }, null, 2), { mode: 0o600 });
    console.log('PASS mixed guest-assistant: ' + checks.length + ' gates, ' + shots.length + ' screenshots');
    return;
  }
  stage = 'ordinary traffic both directions';
  const danaBytes = Buffer.from('DANA_NATIVE_GUEST_BYTES\n'), carolBytes = Buffer.from([9, 8, 7, 0, 255, 1]);
  // No original speaks first: the two accepted guests exchange text, files
  // and an exact reply right after the second acceptance.
  for (const [n, p, other, label] of [['dana', dana, CPHONE, 'Carol'], ['cphone', cphone, DANA, 'Dana']]) {
    await until(n + ' knows the other accepted guest', async () => (await guestOf(p, conv, other))?.state === 'active');
    await openDM(p, conv); assert((await p.locator('#to-name').innerText()).includes(label), n + ' composer names ' + label);
  }
  await openDM(dana, conv); await send(dana, 'Dana native guest text', { name: 'dana-guest.txt', mimeType: 'text/plain', buffer: danaBytes });
  await arrives(conv, 'Dana native guest text', { phone, alice, bob, cphone });
  assert(nativeCopies('dana', conv, 'Dana native guest text').includes(CPHONE), 'native guest turn reaches the other accepted guest');
  await download(phone, conv, 'Dana native guest text', 'dana-guest.txt', danaBytes, 'phone-from-native-guest');
  await download(cphone, conv, 'Dana native guest text', 'dana-guest.txt', danaBytes, 'carol-from-native-guest');
  await openDM(cphone, conv);
  const danaTurn = await msgOf(cphone, conv, 'Dana native guest text');
  await cphone.locator('[id="m-' + danaTurn.id + '"]').getByRole('button', { name: 'Reply', exact: true }).click();
  await send(cphone, 'Carol browser guest reply', { name: 'carol-guest.bin', mimeType: 'application/octet-stream', buffer: carolBytes });
  await arrives(conv, 'Carol browser guest reply', { phone, alice, bob, dana });
  const carolTurn = await msgOf(bob, conv, 'Carol browser guest reply');
  assert.equal(carolTurn.reply_to, danaTurn.lid, 'reply names the parent LID');
  const replyRefs = {};
  for (const [n, p] of Object.entries({ phone, alice, bob, dana, cphone })) {
    await openDM(p, conv); const r = await msgOf(p, conv, 'Carol browser guest reply');
    replyRefs[n] = await p.locator('[id="m-' + r.id + '"] .replyref').innerText();
    assert.equal(replyRefs[n], 'Reply to: Dana native guest text', n + ' resolves the exact quoted parent');
  }
  checks.push({ gate: 'guests exchange before any original turn; reply quote resolves on every peer', reply_to: carolTurn.reply_to, replyRefs });
  await openDM(phone, conv); await send(phone, 'Alice browser to everyone');
  await arrives(conv, 'Alice browser to everyone', { alice, bob, dana, cphone });
  await openDM(bob, conv); await send(bob, 'Bob native to everyone');
  await arrives(conv, 'Bob native to everyone', { phone, alice, dana, cphone });
  await download(bob, conv, 'Carol browser guest reply', 'carol-guest.bin', carolBytes, 'bob-from-browser-guest');
  await download(dana, conv, 'Carol browser guest reply', 'carol-guest.bin', carolBytes, 'dana-from-browser-guest');
  for (const [n, p] of Object.entries({ phone, bob })) {
    await openDM(p, conv);
    for (const label of ['Dana', 'Carol']) assert.equal(await p.locator('#agents .human-participant').filter({ hasText: label }).filter({ hasText: 'Guest' }).count(), 1, n + ' names guest ' + label);
  }
  for (const [n, p] of Object.entries({ phone, bob, dana, cphone })) { await openDM(p, conv); await capture(p, n + '-active-two-guests'); }
  const eventLines = {};
  for (const [n, p] of Object.entries({ phone, alice, bob, dana, cphone })) {
    eventLines[n] = [...new Set((await readDM(p, conv)).messages.filter(m => m.event).map(m => m.event))];
    assert(eventLines[n].length && eventLines[n].every(t => !/agent/i.test(t)), n + ': human participation lines use human wording: ' + JSON.stringify(eventLines[n]));
  }
  checks.push({ gate: 'human participation timeline wording (no agent wording)', eventLines });
  checks.push({ gate: 'ordinary text/files/reply both directions with two active guests', danaFile: digest(danaBytes), carolFile: digest(carolBytes) });

  stage = 'browser guest leaves';
  await openDM(cphone, conv);
  await guestAction(cphone, 'You', 'Leave conversation…', 'browser-guest-leave-dialog');
  await guestState(conv, CPHONE, 'dismissed', { cphone, phone, alice, bob, dana });
  assert.equal(await cphone.locator('#send').isDisabled(), true, 'left guest cannot send');
  await openDM(dana, conv); await send(dana, 'Dana after Carol left');
  await arrives(conv, 'Dana after Carol left', { phone, alice, bob });
  assert(!nativeCopies('dana', conv, 'Dana after Carol left').includes(CPHONE), 'remaining guest made no copy for departed guest');
  await openDM(bob, conv); await send(bob, 'Bob after Carol left');
  await arrives(conv, 'Bob after Carol left', { phone, alice, dana });
  assert(!nativeCopies('bob', conv, 'Bob after Carol left').includes(CPHONE));
  await absent(conv, 'Dana after Carol left', { cphone }); await absent(conv, 'Bob after Carol left', { cphone });
  await openDM(cphone, conv); await capture(cphone, 'browser-guest-left');
  checks.push({ gate: 'browser guest self-leave reaches remaining native guest; later turns exclude it', danaCopies: nativeCopies('dana', conv, 'Dana after Carol left'), bobCopies: nativeCopies('bob', conv, 'Bob after Carol left') });

  stage = 'browser member removes offline native guest';
  const danaPID = Number(fs.readFileSync(path.join(world, 'dana.pid'), 'utf8'));
  await dana.goto('about:blank'); // the stopped daemon's page would only log refused connections
  process.kill(danaPID);
  await until('dana daemon stopped', () => { try { process.kill(danaPID, 0); return false; } catch (e) { return true; } });
  await openDM(phone, conv);
  await guestAction(phone, 'Dana', 'Remove Dana…', 'browser-remove-native-guest-dialog');
  await until('phone shows dana ended', async () => (await guestOf(phone, conv, DANA))?.state === 'dismissed');
  const privateBytes = Buffer.from('PRIVATE_AFTER_GUESTS_BYTES\n');
  await send(phone, 'Private browser after guests', { name: 'private.txt', mimeType: 'text/plain', buffer: privateBytes });
  await arrives(conv, 'Private browser after guests', { bob, alice });
  const phoneCopies = await browserCopies(phone, conv, 'Private browser after guests');
  assert(!phoneCopies.includes(DANA) && !phoneCopies.includes(CPHONE), 'browser private copies exclude departed guests: ' + phoneCopies);
  await download(bob, conv, 'Private browser after guests', 'private.txt', privateBytes, 'bob-private-after-guests');
  await openDM(bob, conv); await send(bob, 'Private native after guests');
  await arrives(conv, 'Private native after guests', { phone, alice });
  const bobCopies = nativeCopies('bob', conv, 'Private native after guests');
  assert(!bobCopies.includes(DANA) && !bobCopies.includes(CPHONE), 'native private copies exclude departed guests: ' + bobCopies);
  await openDM(phone, conv); await capture(phone, 'browser-private-after-guests');

  stage = 'offline guest returns';
  const log = fs.openSync(path.join(world, 'dana-restart.log'), 'a');
  const child = spawn(binary, ['--home', path.join(world, 'dana'), 'daemon', '--ui', '127.0.0.1:0'], { stdio: ['ignore', log, log], env: process.env });
  spawned.push(child);
  await idle(1000);
  const danaURL = uiURL(cli('dana', 'ui')); allowed.add(new URL(danaURL).origin);
  await dana.goto(danaURL); await ready(dana);
  await until('returning dana applies removal', async () => (await guestOf(dana, conv, DANA))?.state === 'dismissed');
  dv = await readDM(dana, conv);
  assert(dv.messages.some(m => m.body === 'Dana native guest text') && dv.messages.some(m => m.body === 'Carol browser guest reply'), 'received copies remain');
  await absent(conv, 'Private browser after guests', { dana, cphone }); await absent(conv, 'Private native after guests', { dana, cphone });
  await openDM(dana, conv); assert.equal(await dana.locator('#send').isDisabled(), true, 'removed guest cannot send');
  await capture(dana, 'native-guest-removed');
  checks.push({ gate: 'original removal by browser while native guest offline; private follow-up excludes both departed guests', phoneCopies, bobCopies });

  stage = 'reload';
  for (const [n, p] of Object.entries({ cphone, phone })) {
    await p.reload(); await ready(p); await openDM(p, conv);
    const v = await readDM(p, conv);
    assert(v.messages.some(m => m.body === 'Carol browser guest reply'), n + ' history survives reload');
    if (p === cphone) { assert.equal(v.role, 'human_guest'); assert(!v.messages.some(m => /Private/.test(m.body))); }
    else assert(v.guests.every(g => g.state === 'dismissed'), 'no active guest after reload');
    await capture(p, n + '-after-reload');
  }
  const aliceView = await readDM(alice, conv);
  assert(['Dana native guest text', 'Carol browser guest reply', 'Private browser after guests', 'Private native after guests'].every(b => aliceView.messages.some(m => m.body === b)), 'original own-linked device has guest turns and private follow-ups');
  for (const p of [phone, alice, bob, dana, cphone]) assert.equal((await readDM(p, conv)).id, conv, 'same immutable conversation');
  checks.push({ gate: 'reload persistence, own-linked original device, unchanged conversation id', conv });

  await Promise.all(assetReads);
  assert.deepEqual(errors, []); assert.deepEqual(external, []);
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: 'actual mixed native/browser human-guest H5, loopback Hub, hgp1 as advertised, fixture-signed only where missing', checks, fixtureCaps, shots, expectedAssets, served, errors, httpErrors, external, world }, null, 2), { mode: 0o600 });
  console.log('PASS actual mixed native/browser H5: ' + checks.length + ' gates, ' + shots.length + ' screenshots');
})().catch(async e => {
  console.error('FAIL stage: ' + stage); console.error(e);
  fs.writeFileSync(path.join(evidence, 'failure.json'), JSON.stringify({ stage, error: e.stack, errors, httpErrors, external, checks, fixtureCaps, world }, null, 2), { mode: 0o600 });
  if (browser) for (const context of browser.contexts()) for (const p of context.pages()) {
    const name = 'page-' + browser.contexts().indexOf(context);
    await p.screenshot({ path: path.join(evidence, 'failure-' + name + '.png') }).catch(() => {});
    fs.writeFileSync(path.join(evidence, 'failure-' + name + '.txt'), await p.locator('body').innerText().catch(() => ''), { mode: 0o600 });
  }
  process.exitCode = 1;
}).finally(async () => {
  for (const c of spawned) c.kill();
  if (browser) await browser.close();
});
