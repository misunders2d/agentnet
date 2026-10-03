// Actual assistant-reaction journey over a real loopback Hub, real native
// daemons and Alice's real linked browser Engine (reaction_world.sh).
// Charlie's default responder answers through the real worker with a
// deterministic stub harness whose structured final line is a reaction; the
// native client sends that reaction as the assistant's own. Alice's native UI
// (native API) and her linked browser (Engine) render it; screenshots at
// desktop 1440 and mobile 390.
//
// Fixture capability seam (test-only): a reader session that already
// advertises agr1 (released clients do) is left as it is; for a session
// whose published caps lack it, the journey signs one more record for that
// same session with that device's own key: the session's published caps
// plus agr1, at the current time (strictly after the session's own record,
// normal validity). Either way it reads the signed profile back.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash, webcrypto } = require('node:crypto');
const { pathToFileURL } = require('node:url');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_REACTION_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS, binary = process.env.AGENTNET_REACTION_BINARY;
assert(world && evidence && binary, 'explicit private fixture paths required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const uiURL = text => text.match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const nativeURL = n => uiURL(fs.readFileSync(path.join(world, n + '-ui'), 'utf8'));
const allowed = new Set([origin, ...['alice', 'bob', 'charlie'].map(n => new URL(nativeURL(n)).origin)]);
const charlieEnv = { ...process.env, PATH: path.join(world, 'stubbin') + ':/usr/bin:/bin' };
const cli = (n, ...args) => execFileSync(binary, ['--home', path.join(world, n), ...args], { encoding: 'utf8', env: n === 'charlie' ? charlieEnv : process.env });
const firstID = text => text.match(/\b[a-f0-9]{32}\b/)[0];
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const widths = [1440, 390];
const errors = [], httpErrors = [], external = [], shots = [], checks = [], fixtureCaps = [], unavailableCatalog = [];
const idle = ms => new Promise(r => setTimeout(r, ms));
let browser, stage = 'setup', wire;
const pagesRef = {};
const ready = p => p.waitForFunction(() => typeof state !== 'undefined' && state.overview && state.overview.directory.current, null, { timeout: 25000 });
async function until(label, predicate, timeout = 60000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await predicate()) return; await idle(200); }
  throw Error('Timed out: ' + label);
}
async function capture(p, name, target) {
  for (const width of widths) {
    await p.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    await idle(300);
    const o = await p.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth }));
    assert(!o.document, name + ' ' + width + ': horizontal overflow');
    const chip = target || p.locator('.reaction.assistant-reaction').first();
    await chip.scrollIntoViewIfNeeded();
    const box = await chip.boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= width + 1, name + ' ' + width + ': assistant chip within the viewport');
    const file = path.join(evidence, `reaction-${name}-${width}.png`);
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
// openDM is human_mixed_journey.cjs's: the visible person, then the topic.
async function openDM(p, id) {
  await home(p);
  await p.waitForFunction(id => (state.overview.dms || []).some(d => d.id === id), id, { timeout: 30000 });
  const label = await p.evaluate(id => (state.overview.dms || []).find(d => d.id === id).peer.label, id);
  await p.locator('#conv-list .contact-item').filter({ hasText: label }).locator('button.contact').click();
  if (await p.evaluate(id => state.dm !== id, id)) {
    const title = await p.evaluate(id => (state.overview.dms || []).find(d => d.id === id).title, id);
    await p.locator('#hub .thread-row').filter({ hasText: title }).first().click();
  }
  await p.waitForFunction(id => state.dm === id && state.dmData?.id === id, id);
}
const readDM = (p, id) => p.evaluate(id => agentnet.api('/api/dm?id=' + id), id);
const readThread = (p, id) => p.evaluate(id => agentnet.api('/api/thread?id=' + id), id);
const assistantBy = m => (m?.reactions || []).flatMap(r => (r.by || []).filter(b => b.assistant).map(b => ({ emoji: r.emoji, ...b })));

// ---- fixture caps --------------------------------------------------------------------------
async function hubCall(keys, address, method, target, body = '') {
  const headers = { ...(await wire.signRequest(keys, address, method, target, body)), ...(body ? { 'Content-Type': 'application/json' } : {}) };
  const r = await fetch(origin + target, { method, headers, body: body || undefined });
  if (!r.ok) throw Error(method + ' ' + target + ': ' + r.status + ' ' + await r.text());
  const t = await r.text(); return t ? JSON.parse(t) : null;
}
async function nativeAGR(account, address) {
  const kf = JSON.parse(fs.readFileSync(path.join(world, account, 'identity.json'), 'utf8'));
  const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.from(kf.sign_seed, 'base64')]);
  const keys = { sign: await webcrypto.subtle.importKey('pkcs8', pkcs8, { name: 'Ed25519' }, false, ['sign']) };
  const call = (m, t, b) => hubCall(keys, address, m, t, b);
  await until(address + ' live caps', async () => { const p = await call('GET', '/v1/agents/' + address + '/profile'); return p.live && p.sessions?.length && p.sessions.every(s => (p.caps || []).some(c => c.session === s)); });
  const profile = await call('GET', '/v1/agents/' + address + '/profile'), records = [];
  for (const session of profile.sessions) {
    const own = (profile.caps || []).map(c => wire.parseCaps(c)).filter(c => c.address === address && c.session === session).sort((a, b) => b.ts - a.ts)[0];
    if (own.caps.includes(wire.CapAgentReaction)) continue; // advertised already: nothing to sign
    while (Math.floor(Date.now() / 1000) <= own.ts) await idle(100); // strictly newer, current time
    const rec = await wire.newCaps(keys, address, session, [...new Set([...own.caps, wire.CapAgentReaction])]);
    await call('PUT', '/v1/caps', wire.capsJSON(rec));
    records.push({ session, ts: rec.ts, own_ts: own.ts });
  }
  const pub = await wire.parsePublic((await call('GET', '/v1/agents/' + address)).public);
  assert(await wire.profileSupports(await call('GET', '/v1/agents/' + address + '/profile'), address, pub.sign_key, wire.CapAgentReaction), address + ' fixture agr1');
  fixtureCaps.push({ address, kind: 'native', records });
}
// browserAGR is nativeAGR inside the page, with the Engine's own stored key.
async function browserAGR(p) {
  const r = await p.evaluate(async () => {
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
      if (own.caps.includes(wire.CapAgentReaction)) continue; // advertised already: nothing to sign
      while (Math.floor(Date.now() / 1000) <= own.ts) await new Promise(r => setTimeout(r, 100));
      const rec = await wire.newCaps(keys, address, session, [...new Set([...own.caps, wire.CapAgentReaction])]);
      await call('PUT', '/v1/caps', wire.capsJSON(rec));
      records.push({ session, ts: rec.ts, own_ts: own.ts });
    }
    const pub = await wire.parsePublic((await call('GET', '/v1/agents/' + address)).public);
    return { address, records, ok: await wire.profileSupports(await call('GET', '/v1/agents/' + address + '/profile'), address, pub.sign_key, wire.CapAgentReaction) };
  });
  assert(r.ok, r.address + ' fixture agr1 ' + (r.why || ''));
  fixtureCaps.push({ address: r.address, kind: 'browser', records: r.records });
}

(async () => {
  wire = await import(pathToFileURL(path.resolve('internal/ui/static/wire.mjs')).href);
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM, args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const pages = pagesRef;
  for (const name of ['phone', 'alice', 'bob', 'charlie']) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.route('**/*', r => {
      const u = new URL(r.request().url());
      // The browser device reads no host catalog here: a named assistant's
      // name is unavailable to it (the label must say so, never invent one).
      if (name === 'phone' && u.pathname.endsWith('/agent-catalog')) { unavailableCatalog.push(u.pathname); return r.fulfill({ status: 503, body: 'catalog unavailable in this journey' }); }
      if (allowed.has(u.origin)) return r.continue(); external.push(u.origin); return r.abort();
    });
    const p = pages[name] = await context.newPage(); p.setDefaultTimeout(20000);
    p.on('pageerror', e => errors.push(name + ': ' + (e.stack || e.message)));
    p.on('console', m => { if (m.type() === 'error' && !(m.location().url || '').endsWith('/agent-catalog')) errors.push(name + ': ' + m.text()); });
    p.on('response', r => { if (r.status() >= 400) httpErrors.push({ page: name, status: r.status(), method: r.request().method(), path: new URL(r.url()).pathname, stage }); });
  }
  const { phone, alice, bob, charlie } = pages;
  stage = 'link browser';
  const raw = fs.readFileSync(path.join(world, 'link-alice'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url')), inv = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  delete inv.cert; offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(inv)).toString('base64url');
  await phone.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await phone.locator('#join-name').fill('phone'); await phone.locator('button.join-go').click();
  let request;
  await until('alice browser link', () => { request = cli('alice', 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*phone/m)?.[1]; return !!request; });
  cli('alice', 'person', 'approve', request); await ready(phone);
  for (const n of ['alice', 'bob', 'charlie']) { await pages[n].goto(nativeURL(n)); await ready(pages[n]); }
  const A = await alice.evaluate(() => state.overview.me.address), PHONE = await phone.evaluate(() => state.overview.me.address), C = 'charlie/host';
  assert.equal(PHONE.split('/')[1], 'phone');

  stage = 'fixture caps';
  await nativeAGR('alice', A); await nativeAGR('bob', 'bob/desk'); await browserAGR(phone);

  stage = 'browser device thread';
  cli('charlie', 'approve', PHONE);
  const q1 = await phone.evaluate(c => agentnet.api('/api/send', { to: c, kind: 'question', body: 'Browser question for the default responder' }), C);
  let deviceReactor;
  await until('browser device thread shows the assistant reaction', async () => {
    const m = (await readThread(phone, q1.id)).messages.find(x => x.id === q1.id);
    deviceReactor = assistantBy(m)[0]; return !!deviceReactor;
  });
  const expectDefault = { emoji: '🎉', id: 'assistant:' + C + '/default', label: C + ' assistant', assistant: true, host: C };
  assert.deepEqual(deviceReactor, expectDefault, 'browser Engine default responder Reactor');
  const answer = (await readThread(phone, q1.id)).messages.find(x => x.dir === 'in' && x.reply_to === q1.id);
  assert.equal(answer?.body, 'Checked it.', 'the reaction line is not part of the answer');
  await phone.evaluate(id => openThread(id), q1.id);
  await phone.locator('.reaction.assistant-reaction').first().waitFor();
  assert((await phone.locator('.reaction.assistant-reaction').first().innerText()).includes('Assistant on ' + C), 'browser chip: whose assistant, no technical id');
  assert((await phone.locator('.reaction.assistant-reaction').first().getAttribute('title')).includes('default responder on ' + C), 'exact identity in the title');
  await capture(phone, 'browser-device-thread');
  checks.push({ gate: 'browser device thread: default responder assistant reaction', reactor: deviceReactor, answer: answer.body });

  stage = 'native device thread';
  cli('charlie', 'approve', A);
  const q2 = firstID(cli('alice', 'ask', C, 'Native question for the default responder'));
  let nativeReactor;
  await until('native device thread shows the assistant reaction', async () => {
    const m = (await readThread(alice, q2)).messages.find(x => x.id === q2);
    nativeReactor = assistantBy(m)[0]; return !!nativeReactor;
  });
  assert.deepEqual(nativeReactor, expectDefault, 'native API default responder Reactor equals the browser Engine one');
  await alice.evaluate(id => openThread(id), q2);
  await alice.locator('.reaction.assistant-reaction').first().waitFor();
  await capture(alice, 'native-device-thread');
  checks.push({ gate: 'native device thread: same Reactor JSON as the browser Engine', reactor: nativeReactor });

  stage = 'DM participation';
  const conv = cli('alice', 'dm', 'new', 'bob/desk').trim();
  const pid = firstID(cli('alice', 'dm', 'invite', conv, C));
  await until('charlie accepts', () => { try { cli('charlie', 'dm', 'accept-agent', pid); return true; } catch (e) { return false; } });
  await until('active at alice', async () => ((await readDM(alice, conv)).agents || []).some(a => a.pid === pid && a.state === 'active'));
  cli('alice', 'dm', 'ask-agent', pid, 'DM question for the assistant');
  const reactorIn = async (p) => {
    const m = (await readDM(p, conv)).messages.find(x => x.pid === pid && x.kind === 'question');
    return assistantBy(m)[0];
  };
  const expectPID = { emoji: '🎉', id: 'assistant:' + pid, label: 'Charlie assistant', assistant: true, host: C, pid }; // client.assistantLabel: the host person's label
  for (const [n, p] of Object.entries({ alice, phone, bob })) {
    let r;
    await until(n + ' shows the DM assistant reaction', async () => (r = await reactorIn(p)) !== undefined);
    assert.deepEqual(r, expectPID, n + ': participation Reactor');
  }
  checks.push({ gate: 'DM participation: native API (alice, bob) and browser Engine (alice phone) agree', reactor: expectPID });
  // People's own marks beside the assistant's, by the visible pickers: Bob on
  // his native page, then Alice on her linked browser, where the question her
  // laptop sent is her own replica (controlled by its exact row and author key).
  await openDM(bob, conv);
  const qb = (await readDM(bob, conv)).messages.find(x => x.pid === pid && x.kind === 'question');
  await bob.locator('[id="m-' + qb.id + '"] .react-pick summary').click();
  await bob.locator('[id="m-' + qb.id + '"] .react-menu').getByRole('button', { name: 'React 🎉', exact: true }).click();
  const both = async (p, mine) => {
    const m = (await readDM(p, conv)).messages.find(x => x.pid === pid && x.kind === 'question');
    const r = (m?.reactions || []).find(x => x.emoji === '🎉');
    return !!r && r.by.length === 2 && r.by.some(b => b.assistant && b.id === 'assistant:' + pid) && r.by.some(b => !b.assistant) && !!r.mine === mine;
  };
  await until('bob sees his own mark beside the assistant', () => both(bob, true));
  for (const [n, p] of Object.entries({ alice, phone })) await until(n + ' sees Bob\'s mark beside the assistant, as two actors', () => both(p, false));
  await openDM(phone, conv);
  await phone.waitForFunction(id => (state.dmData.messages || []).some(m => m.pid === id && (m.reactions || []).some(r => r.by.length === 2)), pid);
  await capture(phone, 'browser-dm-bob-mark');
  await openDM(phone, conv);
  const qm = (await readDM(phone, conv)).messages.find(x => x.pid === pid && x.kind === 'question');
  assert.equal(qm.dir, 'out', 'the question is shown as Alice\'s own on her browser');
  await phone.locator('[id="m-' + qm.id + '"] .react-pick summary').click();
  await phone.locator('[id="m-' + qm.id + '"] .react-menu').getByRole('button', { name: 'React 🎉', exact: true }).click();
  await until('the linked browser click is accepted', async () => (await phone.locator('#live').innerText()).startsWith('Reacted.'), 20000);
  const three = async (p, mine) => {
    const m = (await readDM(p, conv)).messages.find(x => x.pid === pid && x.kind === 'question');
    const r = (m?.reactions || []).find(x => x.emoji === '🎉');
    return !!r && r.by.length === 3 && r.by.filter(b => b.assistant).length === 1 && !!r.mine === mine;
  };
  for (const [n, p, mine] of [['alice', alice, true], ['phone', phone, true], ['bob', bob, true]]) await until(n + ' sees Alice\'s, Bob\'s and the assistant\'s marks', () => three(p, mine));
  await openDM(phone, conv);
  await phone.waitForFunction(id => { const m = state.dmData.messages.find(x => x.id === id); const r = (m?.reactions || []).find(x => x.emoji === '🎉'); return r && r.by.length === 3; }, qm.id);
  const phoneChips = await phone.locator('[id="m-' + qm.id + '"] .reactions .reaction').allInnerTexts();
  assert(phoneChips.some(t => /^🎉\s*2$/.test(t.trim())) && phoneChips.some(t => t.includes("Charlie's agent") && !/name unavailable|[a-f0-9]{8}/.test(t)), 'browser: one person chip (2, yours) and the default participation chip, named as its participant: ' + JSON.stringify(phoneChips));
  await capture(phone, 'browser-dm');
  await openDM(alice, conv);
  await alice.waitForFunction(id => (state.dmData.messages || []).some(m => m.pid === id && (m.reactions || []).some(r => r.by.length === 3)), pid);
  await capture(alice, 'native-dm');
  checks.push({ gate: 'DM: Bob (native) and Alice (linked browser, own replica) marks stay separate from the assistant mark', chips: phoneChips });

  stage = 'named participation';
  const created = await charlie.evaluate(dir => agentnet.api('/api/agents', { action: 'create', label: 'Reviewer', harness: 'claude', dir }), path.join(world, 'charlie-work'));
  assert(created.saved && created.published && created.agent?.record?.id, 'charlie publishes a named assistant: ' + JSON.stringify(created));
  const agentID = created.agent.record.id;
  const npid = (await alice.evaluate(x => agentnet.api('/api/dm/agent/invite', x), { conv, host: C, agent_id: agentID, share: [], tasks_from: [], note: '' })).pid;
  await until('charlie accepts the named assistant', () => { try { cli('charlie', 'dm', 'accept-agent', npid); return true; } catch (e) { return false; } });
  await until('named active at alice', async () => ((await readDM(alice, conv)).agents || []).some(a => a.pid === npid && a.state === 'active'));
  cli('alice', 'dm', 'ask-agent', npid, 'DM question for the named assistant');
  const expectNamed = { emoji: '🎉', id: 'assistant:' + npid, label: 'Charlie assistant ' + agentID.slice(0, 8), assistant: true, host: C, agent_id: agentID, pid: npid };
  const namedIn = async p => assistantBy((await readDM(p, conv)).messages.find(x => x.pid === npid && x.kind === 'question'))[0];
  for (const [n, p] of Object.entries({ alice, phone, bob })) {
    let r;
    await until(n + ' shows the named assistant reaction', async () => (r = await namedIn(p)) !== undefined);
    assert.deepEqual(r, expectNamed, n + ': named participation Reactor');
  }
  const namedChip = async (p, want) => {
    await p.reload(); await ready(p); // a page reads a host's catalog when it opens the DM: reopened after Charlie published
    await openDM(p, conv);
    const qid = (await readDM(p, conv)).messages.find(x => x.pid === npid && x.kind === 'question').id;
    const chip = p.locator('[id="m-' + qid + '"] .reaction.assistant-reaction');
    await until('named chip reads ' + want, async () => (await chip.count()) === 1 && (await chip.innerText()).includes(want), 30000).catch(async e => {
      throw Error(e.message + ': ' + JSON.stringify({ chips: await chip.allInnerTexts(), names: await p.evaluate(h => state.dmNames?.[h], C), agents: await p.evaluate(() => (state.dmData.agents || []).map(a => ({ pid: a.pid, agent_id: a.agent_id, host: a.host?.address }))) }));
    });
    const text = await chip.innerText(), title = await chip.getAttribute('title');
    assert(title.includes('agent ' + agentID) && title.includes('participation ' + npid), 'exact identity in the title: ' + title);
    // The same agent's other everyday labels: participant bar, the request's
    // target, the answer's author. Readable; the ID only in titles.
    const participant = p.locator('#agents .assistant-participant').filter({ hasText: want.split(' · ')[0] });
    const bar = await participant.allInnerTexts(), barTitles = await participant.evaluateAll(ns => ns.map(n => n.title));
    const meta = await p.locator('[id="m-' + qid + '"] .meta').first().innerText();
    const answerID = (await readDM(p, conv)).messages.find(x => x.pid === npid && x.kind === 'answer').id;
    const author = await p.locator('[id="m-' + answerID + '"] .meta .who').first().innerText();
    const shown = { bar, meta, author };
    for (const t of [...bar, meta, author]) assert(!t.includes(agentID.slice(0, 8)), 'no technical id in everyday text: ' + JSON.stringify(shown));
    assert(bar.some(t => t.includes(want)) && barTitles.some(t => t.includes(agentID)), 'participant bar: ' + JSON.stringify({ bar, barTitles }));
    const named = want.split(' · ')[0].toLowerCase(); // the target tag starts lower case ("To your agent")
    assert(meta.toLowerCase().includes('to ' + named) && author.toLowerCase().includes(named), 'target and author named as the participant: ' + JSON.stringify(shown));
    return { text, title, chip, shown };
  };
  const withCatalog = await namedChip(alice, 'Reviewer');
  assert(!/[a-f0-9]{8}/.test(withCatalog.text), 'catalog name without technical id: ' + withCatalog.text);
  await capture(alice, 'native-dm-named-catalog', withCatalog.chip);
  const withoutCatalog = await namedChip(phone, "Charlie's assistant · name unavailable");
  assert(!withoutCatalog.text.includes(agentID.slice(0, 8)) && unavailableCatalog.length > 0, 'unavailable catalog: truthful wording, no invented name or technical id: ' + withoutCatalog.text);
  await capture(phone, 'browser-dm-named-unavailable', withoutCatalog.chip);
  checks.push({ gate: 'named assistant label: verified catalog name, else name unavailable (reaction, participant bar, target, author)', withCatalog: { text: withCatalog.text, title: withCatalog.title, shown: withCatalog.shown }, withoutCatalog: { text: withoutCatalog.text, title: withoutCatalog.title, shown: withoutCatalog.shown }, reactor: expectNamed });

  stage = 'final';
  assert.deepEqual(errors, [], 'console/page errors');
  assert.deepEqual(external, [], 'no external origins');
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: 'assistant reactions: real Hub, native daemons, stub claude harness, linked browser Engine; agr1 as advertised, fixture-signed only where a session lacked it', checks, fixtureCaps, shots, errors, httpErrors, external, unavailableCatalog, world }, null, 2), { mode: 0o600 });
  console.log('reaction journey passed: ' + checks.length + ' gates, ' + shots.length + ' screenshots');
  await browser.close();
})().catch(async e => {
  let browserOutbox = null; // what the browser device kept of its own controls, for diagnosis
  try { browserOutbox = await pagesRef.phone?.evaluate(async () => {
    const db = await new Promise((res, rej) => { const r = indexedDB.open('agentnet'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    const rows = await new Promise((res, rej) => { const r = db.transaction('outbox').objectStore('outbox').getAll(); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    db.close(); return { controls: rows.filter(r => r.control).map(r => ({ to: r.to, sub: r.sub, state: r.state, detail: r.detail || '' })), announced: document.getElementById('live')?.textContent || '' };
  }); } catch (x) { browserOutbox = String(x); }
  fs.writeFileSync(path.join(evidence, 'failure.json'), JSON.stringify({ stage, error: e.stack, errors, httpErrors, external, checks, fixtureCaps, browserOutbox, world }, null, 2), { mode: 0o600 });
  console.error('reaction journey failed at ' + stage + ': ' + e.stack);
  if (browser) await browser.close();
  process.exit(1);
});
