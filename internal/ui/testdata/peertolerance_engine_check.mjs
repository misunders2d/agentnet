// One device never stops everyone (v0.8.17 wave 2, CG-1/2/13/14 and the
// suspended-peer rule): real Engine methods over synthetic records and a
// synthetic relay; nothing reaches a network.
//  - DM with an active human guest: a member device without hgp1 keeps its
//    sealed copy waiting; the others get the turn (CG-14), and the waiting
//    copy goes once that device reads it. A changed key still refuses the
//    turn, as a DM turn's does (fail closed: never a turn only the guest
//    and this person's own devices get).
//  - Group turn: a device without grp1 or never connected waits, a changed
//    key gets nothing (that device only), the rest are sent (CG-1, CG-13).
//    The message keeps that device as not sent, with why: after a reload
//    it is listed and the message's delivery is not everyone's.
//  - Group publication carriers: a device that cannot read groups yet keeps
//    a waiting carrier; an accepted invitation that cannot be published
//    yet says why (CG-2).
//  - A suspended device: its waiting copies are not re-checked (no profile
//    reads), the headline reads as everyone else's, and its copy says so.
import assert from 'node:assert/strict';
import { Engine, HubError, memoryStore, deliveryOf, outText } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0, sequence = 1;
const id = () => (sequence++).toString(16).padStart(32, '0');
const now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
async function world() {
  const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), catalogs = new Map(), blobs = new Map(), posts = [], groups = new Map();
  let offline = false;
  const fetch = async (url, o = {}) => {
    if (offline) throw new TypeError('Failed to fetch');
    const p = new URL(url).pathname;
    if (p === '/v1/version') return json({ features: ['env2', 'person2', 'caps', 'notify1'], realm_id: 'f'.repeat(32) });
    if (p.startsWith('/v1/groups/') && p.endsWith('/chain')) { const records=groups.get(p)||[]; if(o.method==='POST'){const record=JSON.parse(o.body);records[record.seq]=record;groups.set(p,records);return json({seq:record.seq,hash:record.hash});} const after=Number(new URL(url).searchParams.get('after')||-1);return json({records:records.filter(r=>r.seq>after),more:false}); }
    if (p.endsWith('/profile')) return json(profiles.get(p.slice(11, -8)));
    if (p.endsWith('/agent-catalog')) return json(catalogs.get(p.slice(11, -14)) || []);
    if (p.startsWith('/v1/persons/') && p.endsWith('/chain')) {
      const pid = p.slice(12, -6), after = Number(new URL(url).searchParams.get('after'));
      return json({ records: (rosters.get(pid) || []).filter((r) => r.seq > after), more: false });
    }
    if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(p.slice(11)))) });
    if (p === '/v1/messages') {
      const env = wire.parseEnvelope(o.body);
      await wire.verifyEnvelope(env, pubs.get(env.from).sign_key);
      check(!!o.headers['X-Agentnet-Sig'], 'existing signed transport');
      posts.push(o.body); return json({ state: 'custody' });
    }
    if (p.endsWith('/wait')) return json({ state: 'custody' });
    if (p.endsWith('/ack') || p === '/v1/caps') return json(null, 204);
    if (p === '/v1/blobs') { const b = JSON.parse(o.body); if (!blobs.has(b.id)) blobs.set(b.id, { ...b, received: 0, state: 'uploading', ct: new Uint8Array(b.size) }); return json(blobs.get(b.id)); }
    if (p.startsWith('/v1/blobs/')) {
      const b = blobs.get(p.split('/')[3]);
      if (p.endsWith('/data')) return new Response(b.ct);
      if (p.endsWith('/complete')) { b.state = 'stored'; return json(b); }
      if (o.method === 'PUT') { const bytes = new Uint8Array(o.body); b.ct.set(bytes, b.received); b.received += bytes.length; }
      return json(b);
    }
    throw new Error('unexpected synthetic path ' + p);
  };
  for (const name of ['alice', 'bob', 'carol', 'mallory']) {
    const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
    e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
    e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
    await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
    e.connected = false; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
  }
  const caps = async (e, names = [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity, wire.CapExternalParticipation, wire.CapHumanParticipation]) => {
    const session = id(); profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, names)))] });
  };
  for (const e of users) { await caps(e); for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  } }
  const [alice, bob, carol, mallory] = users;
  const agent = await wire.signAgent(carol.keys, { id: id(), host: carol.address, host_key: carol.fp, label: 'Carol agent', ts: Math.floor(now / 1000) });
  catalogs.set(carol.address, [JSON.parse(wire.agentJSON(agent))]);
  const conv = await alice.newDM(bob.address), c = await alice.store.get('convs', conv);
  const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
  // Sending saves first (P12); this isolated network posts the captured
  // queued copies before delivering them. No real connection/polling runs.
  const drain = async (e, predicate = () => true) => {
    for (const sender of users) {
      const rows=(await sender.store.all('outbox')).sort((a,b)=>(a.send_order??a.at)-(b.send_order??b.at));
      for(const row of rows) if(row.to===e.address && ['queued','waiting'].includes(row.state)) await sender.post(row);
    }
    for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && predicate(env) && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); }
  };
  const from = async (sender, receiver, fields) => wire.seal({ v: 2, id: id(), from: sender.address, to: receiver.address, ts: Math.floor(now / 1000), conv, root: c.root, lid: id(), kind: 'message', body: '', ...fields }, sender.keys, receiver.pub);
  const decision = async (sender, pid, type = 'accept', prev) => {
    const { info } = await alice.agentConv(pid);
    return wire.signEvent(sender.keys, { conv, pid, type, prev: prev || info.invite, ts: Math.floor(now / 1000), author: sender.author() });
  };
  const accept = async (pid) => { const ev = await decision(carol, pid); for (const e of [alice, bob, carol]) await receive(await from(carol, e, { sub: 'event', pid, body: wire.eventJSON(ev) }), e); return ev; };
  const sibling = async (owner, address, human = false) => {
    const e = new Engine({ store: memoryStore(), now: () => now, base: owner.base, fetch });
    e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
    const join = await wire.joinConsent(e.keys, address, owner.me.person, owner.me.seq + 1, owner.me.hash);
    const next = await wire.nextRoster(owner.keys, owner.address, owner.roster, [...owner.me.devices.map((d) => pubs.get(d.address)), e.pub], join, owner.roster.label, human ? [...await wire.rosterHumans(owner.roster), e.fp] : null);
    await wire.verifyNext(next, owner.roster);
    const steps = [...await Promise.all(rosters.get(owner.me.person).map((r) => wire.parseRoster(r))), next];
    owner.me = await owner.personRecord(steps, 'self', owner.me); owner.roster = next;
    e.me = await e.personRecord(steps, 'self', null); e.roster = next;
    pubs.set(address, e.pub); rosters.set(owner.me.person, steps.map((r) => JSON.parse(wire.rosterJSON(r))));
    await owner.store.write([{ s: 'kv', k: 'person', v: owner.me }]); await owner.pinDevices(owner.me);
    await e.store.write([{ s: 'kv', k: 'person', v: e.me }, { s: 'kv', k: 'identity', v: { keys: e.keys, address, fingerprint: e.fp } },
      ...(await owner.store.all('persons')).filter((p) => p.person !== e.me.person).map((p) => ({ s: 'persons', k: p.person, v: p })),
      ...(await owner.store.all('pins')).map((p) => ({ s: 'pins', k: p.address, v: p }))]);
    await caps(e); await caps(owner); users.push(e); return e;
  };
  return { alice, bob, carol, mallory, users, conv, c, agent, posts, blobs, caps, catalogs, receive, drain, from, decision, accept, sibling, offline: (v) => { offline = v; } };
}

// ---- CG-14: a DM with an active guest, one member device without hgp1
{
  const w = await world(), a = w.alice, b = w.bob, c = w.carol, conv = w.conv;
  const invite = await a.changeHuman('invite', { conv, host: c.address });
  await w.drain(c); await w.drain(b);
  await c.changeHuman('decide', { pid: invite.pid, accept: true });
  await w.drain(a); await w.drain(b);
  const control = await a.sendDM({ conv, body: 'ALL CURRENT' });
  check(control.copies.every((x) => x.state === 'queued'), 'all current: every copy queued');
  const phone = await w.sibling(b, 'bob/phone');
  await a.refreshPerson(await a.store.get('persons', b.me.person)).catch(() => {});
  await w.caps(phone, [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity]); // an older program: no hgp1
  const sent = await a.sendDM({ conv, body: 'AFTER BOB PHONE IS OLD' });
  const copy = (to) => sent.copies.find((x) => x.to === to);
  check(copy(b.address)?.state === 'queued' && copy(c.address)?.state === 'queued', 'the other devices get the turn');
  check(copy(phone.address)?.state === 'waiting' && copy(phone.address).detail.startsWith('peer_update: '), 'the old device keeps its copy waiting, saying why');
  await w.drain(b); await w.drain(c);
  check((await b.dm(conv)).messages.some((m) => m.body === 'AFTER BOB PHONE IS OLD'), 'bob has it');
  check((await c.dm(conv)).messages.some((m) => m.body === 'AFTER BOB PHONE IS OLD'), 'the guest has it');
  const held = (await a.store.all('outbox')).find((r) => r.body === 'AFTER BOB PHONE IS OLD' && r.to === phone.address);
  await w.caps(phone); // it updates
  await w.drain(phone);
  const later = await a.store.get('outbox', held.id);
  check(later.state === 'custody' && later.envelope === held.envelope, 'the same sealed copy goes once the device reads it');
}

// ---- A DM with an active guest: the other member's changed key refuses the turn
{
  const w = await world(), a = w.alice, b = w.bob, c = w.carol, conv = w.conv;
  const invite = await a.changeHuman('invite', { conv, host: c.address });
  await w.drain(c); await w.drain(b);
  await c.changeHuman('decide', { pid: invite.pid, accept: true });
  await w.drain(a); await w.drain(b);
  const pin = await a.store.get('pins', b.address);
  await a.store.write([{ s: 'pins', k: b.address, v: { ...pin, pending: { fingerprint: 'f'.repeat(64), json: '{}' } } }]);
  await assert.rejects(() => a.sendDM({ conv, body: 'PEER KEY CHANGED' }), /key changed/); checks++;
  check(!(await a.store.all('outbox')).some((r) => r.body === 'PEER KEY CHANGED'), 'nothing is kept for the guest or own devices alone');
}

// ---- CG-1 / CG-13: a group turn, one device at a time
async function device(address, caps /* null: never connected */) {
  const keys = await wire.newKeys(), pub = await wire.publicEntry(keys, address), fp = await wire.fingerprint(pub);
  let profile = { live: false };
  if (caps) { const session = wire.newID(); profile = { sessions: [session], live: true, caps: [JSON.parse(wire.capsJSON(await wire.newCaps(keys, address, session, caps)))] }; }
  return { address, fingerprint: fp, pin: { address, fingerprint: fp, json: wire.marshalPublic(pub), pending: null }, profile };
}
const NEW = [wire.CapEnv2, 'notify1', wire.CapPerson, wire.CapGroup], OLD = [wire.CapEnv2, 'notify1', wire.CapPerson];
const me = 'a'.repeat(32), mh = 'b'.repeat(64);
const groupEngine = async (devices) => {
  const e = Object.create(Engine.prototype), by = new Map(devices.map((d) => [d.address, d]));
  Object.assign(e, { pubs: new Map(), address: 'browser/me', revoked: false, now: () => now, committed: [], posted: [] });
  e.keys = await wire.newKeys(); e.fp = await wire.fingerprint(await wire.publicEntry(e.keys, e.address));
  e.root = await wire.signGroupRoot(e.keys, { v: 3, kind: 'group', realm: 'e'.repeat(32), nonce: '1'.repeat(32), created: 1, title: 'G',
    creator: { person: me, roster: mh, address: e.address, fingerprint: e.fp }, members: [{ person: me, roster: mh }], admins: [me] });
  e.features = async () => ['env2', 'caps', 'person2', 'notify1'];
  e.profile = async (a) => by.get(a).profile;
  e.pinned = async (a) => { if (!by.has(a)) throw new HubError(404, '', 'unknown agent'); return by.get(a).pin; };
  e.store = { get: async (s, k) => s === 'persons' ? { hash: 'c'.repeat(64) } : undefined };
  e.groupRead = async (_, s, k) => s === 'kv' && k === 'person' ? { person: me, hash: mh } : s === 'pins' ? by.get(k)?.pin : undefined;
  e.convEvents = async () => []; e.dmMembers = async () => ({}); e.roomPlan = async () => null;
  const admission = { conv: 'c', realm: 'r', person: me, roster: mh, seq: 0, prev: '', history: [], by: 'f' };
  e.groupTurnEvidence = async () => ({ packet: { root: e.root, state: { members: [{ person: me, admission }] } },
    members: devices.map((d, i) => ({ person: String(i + 1).repeat(32), devices: [{ address: d.address, fingerprint: d.fingerprint }] })) });
  e.groupReply = async () => ''; e.prepareReceiverRequest = async () => null; e.roomStoredOps = async () => [];
  e.commitReceiverCopies = async (copies) => { e.committed.push(...copies); };
  e.post = async (r) => { e.posted.push(r.to); };
  e.changed = () => {}; e.keepSent = async () => {};
  return e;
};
{
  const cur = await device('alice/laptop', NEW), old = await device('bob/oldlaptop', OLD), fresh = await device('carol/newphone', null), moved = await device('dave/desk', NEW);
  moved.pin = { ...moved.pin, pending: { fingerprint: 'other', json: '{}' } }; // its key changed and is not trusted here
  const e = await groupEngine([cur, old, fresh, moved]);
  const sent = await e.sendGroupTurn({ id: 'd'.repeat(64) }, { body: 'hi' });
  const stored = new Map(e.committed.map((r) => [r.to, r]));
  check(stored.get(cur.address)?.state === 'queued', 'a current device gets the turn');
  check(stored.get(old.address)?.state === 'waiting' && stored.get(old.address).detail.startsWith('peer_update: '), 'a device without grp1 keeps its sealed copy waiting');
  check(stored.get(fresh.address)?.state === 'waiting', 'a never-connected device keeps its sealed copy waiting');
  check(!stored.has(moved.address), 'a changed key gets nothing');
  check(sent.copies.some((x) => x.to === moved.address && x.state === 'not_delivered' && /key changed/.test(x.detail)), 'and says so for that device only');
  check(sent.state === 'not_delivered' && /key changed/.test(sent.detail), 'the send names it, never everyone');
  assert.deepEqual(e.posted, [cur.address], 'only what may go is posted now'); checks++;
  check(e.committed.every((r) => r.skipped?.length === 1 && r.skipped[0].to === moved.address && r.skipped[0].state === 'not_delivered' && /key changed/.test(r.skipped[0].detail) && !r.skipped[0].own), 'the skipped device is stored with the message');
  // After a reload: the stored rows alone list it, and the message is not
  // everyone's even once every copy sent is delivered.
  const reader = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', now: () => now, fetch: async () => { throw Error('no network in this check'); } });
  reader.address = 'browser/me';
  const conv = e.committed[0].conv;
  await reader.store.write(e.committed.map((r) => ({ s: 'outbox', k: r.id, v: { ...r, state: 'delivered' } })));
  const [shown] = await reader.convMessages(conv, [], await reader.store.all('outbox'));
  const listed = shown.copies.find((x) => x.to === moved.address);
  check(listed?.state === 'not_delivered' && /key changed/.test(listed.detail) && shown.copies.length === 4, 'the reloaded message lists the device it was not sent to');
  check(shown.delivery === 'not_delivered' && shown.state === 'not_delivered' && shown.lagging === moved.address, 'and its delivery and state are not everyone\'s');
  check(outText(shown.state, 'Dave’s desk') === 'Not sent to Dave’s desk', 'its line names that device');
  const lonely = await groupEngine([await device('erin/desk', null), { ...await device('frank/desk', NEW), pin: { address: 'frank/desk', fingerprint: 'x', json: '{}', pending: { fingerprint: 'y' } } }]);
  const lonelySent = await lonely.sendGroupTurn({ id: 'd'.repeat(64) }, { body: 'still stored' });
  check(lonely.committed.length === 1 && lonely.committed[0].state === 'waiting' && lonelySent.copies.length === 2, 'a waiting copy still counts as sent to someone');
  const nobody = await groupEngine([{ ...await device('gus/desk', NEW), pin: { address: 'gus/desk', fingerprint: 'x', json: '{}', pending: { fingerprint: 'y' } } }]);
  await assert.rejects(() => nobody.sendGroupTurn({ id: 'd'.repeat(64) }, { body: 'nobody' }), /no other current device/); checks++;
}

// ---- CG-2: publication carriers per device; a stuck accepted invitation says why
{
  const cur = await device('alice/laptop', NEW), old = await device('bob/oldlaptop', OLD);
  const e = await groupEngine([cur, old]), root = e.root;
  const carrier = async (d, tolerant) => e.groupCarrierCopy(root, wire.SubGroupContext, { v: 1, seq: 1, hash: 'a'.repeat(64) }, '{"v":1}', { address: d.address, fingerprint: d.fingerprint }, {}, false, tolerant);
  check((await carrier(cur, true)).state === 'queued', 'a current device: carrier queued');
  const waiting = await carrier(old, true);
  check(waiting.state === 'waiting' && waiting.detail.startsWith('peer_update: '), 'a device without grp1: its carrier waits, the publication goes on');
  await assert.rejects(() => carrier(old, false), /cannot read groups/); checks++; // other callers keep their strict check
  check(/not on your server/.test((await e.groupDevicePin({ address: 'dave/desk', fingerprint: 'x' }, [])).skip?.detail), 'a device the server does not know is skipped, never sealed for');
  check(/key changed/.test((await e.groupDevicePin({ address: cur.address, fingerprint: 'other' }, [])).skip?.detail), 'a key other than its roster names is skipped');
  // recoverGroupIntents keeps the last reason on the exact retained intent
  const store = memoryStore(), g = Object.create(Engine.prototype);
  Object.assign(g, { store, changed: () => {}, syncInvitations: async () => {} });
  const intent = { type: 'group-invitation', direction: 'out', status: 'accepted', id: '9'.repeat(32), proposal: { state: { conv: 'c'.repeat(64), title: 'T' }, target: 'p', history: [] }, inviter: 'browser/me', fp: 'f', owner: 'me' };
  await store.write([{ s: 'kv', k: 'group-invitation/out/' + intent.id, v: intent }]);
  g.publishGroupInvitation = async () => { throw Error('bob/oldlaptop cannot read groups with this version.'); };
  await g.recoverGroupIntents();
  check((await store.get('kv', 'group-invitation/out/' + intent.id)).last_error === 'bob/oldlaptop cannot read groups with this version.', 'the retained intent keeps its last reason');
  g.groupInvitationCapabilities = async () => ({});
  const [view] = (await g.groupInvitations()).filter((i) => i.id === intent.id);
  check(view.error === 'bob/oldlaptop cannot read groups with this version.', 'and the invitation view shows it');
}

// ---- A suspended device holds nobody
{
  const copies = [{ to: 'bob/desk', person: 'bob', state: 'delivered' }, { to: 'carol/phone', person: 'carol', state: 'waiting', suspended: true }];
  check(deliveryOf(copies) === 'delivered', 'the headline ignores a suspended device');
  check(deliveryOf([{ to: 'carol/phone', person: 'carol', state: 'custody', suspended: true }, { to: 'me/phone', own: true, state: 'delivered' }]) === 'custody', 'unless it is the only one of anyone else');
  check(deliveryOf([{ to: 'bob/desk', person: 'bob', state: 'delivered' }, { to: 'carol/phone', person: 'carol', state: 'waiting' }]) === 'waiting', 'a current waiting device still counts');
  const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', now: () => now, fetch: async () => { throw Error('no network in this check'); } });
  e.address = 'browser/me';
  e.members = { listed: 'listed', current: true, at: now, list: [{ address: 'carol/phone', presence: 'connected', suspended: true }, { address: 'bob/desk', presence: 'connected' }], truncated: false };
  await e.keepMemberFacts({ members: e.members.list });
  check(e.peerSuspended('carol/phone') && !e.peerSuspended('bob/desk'), 'suspension is read from the member list');
  check(JSON.stringify(await e.store.get('kv', 'suspended_devices')) === '["carol/phone"]', 'and kept for offline display');
  const conv = 'c'.repeat(64);
  await e.store.write([{ s: 'outbox', k: 'w1', v: { id: 'w1', conv, to: 'carol/phone', state: 'waiting', detail: 'peer_update: old', at: 1 } }]);
  let looked = 0;
  e.connected = true; e.gate = async () => { looked++; return { why: '' }; }; e.supports = async () => { looked++; return [false]; };
  e.store.get = ((get) => async (s, k) => s === 'convs' ? { id: conv } : get(s, k))(e.store.get.bind(e.store));
  await e.flushOutboxOnce();
  check(looked === 0, 'its waiting copy is not re-checked on every ping');
  e.members = { ...e.members, list: [{ address: 'carol/phone', presence: 'connected' }] };
  await e.keepMemberFacts({ members: e.members.list });
  await e.flushOutboxOnce();
  check(looked > 0, 'listed current again: it is looked at');
  const shown = await e.convMessages(conv, [], [{ id: 'w2', lid: 'l2', conv, to: 'carol/phone', state: 'waiting', at: 2, person: 'carol' }, { id: 'w3', lid: 'l2', conv, to: 'bob/desk', state: 'delivered', at: 2, person: 'bob' }]);
  check(shown[0].copies.every((x) => !x.suspended) && shown[0].delivery === 'waiting', 'not suspended: a waiting device counts again');
}
console.log('PASS one device never stops everyone: ' + checks + ' checks');
