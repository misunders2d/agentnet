// Browser parity for blocking a person (client blocksync.go): marks follow
// the person's own human devices under rem2, a blocked person's direct
// messages are held quietly and never released, another person cannot set
// a block, and an older mark never replaces a newer one. Isolated real-key
// Engine world (as convclear_engine_check.mjs): no real peer or network.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0, sequence = 1;
const id = () => (sequence++).toString(16).padStart(32, '0');
const now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
const defaults = [wire.CapEnv2, wire.CapPerson, wire.CapControl, wire.CapRootSync, wire.CapReadSyncV2, wire.CapOwnSyncV2, wire.CapReminderSyncV2];
async function world() {
  const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), blobs = new Map(), posts = [];
  let offline = false;
  const fetch = async (url, o = {}) => {
    if (offline) throw new TypeError('Failed to fetch');
    const p = new URL(url).pathname;
    if (p === '/v1/version') return json({ features: ['env2', 'env3', 'person2', 'caps', 'notify1'], realm_id: 'test-realm' });
    if (p.endsWith('/profile')) return json(profiles.get(p.slice(11, -8)));
    if (p.startsWith('/v1/persons/') && p.endsWith('/chain')) {
      const pid = p.slice(12, -6), after = Number(new URL(url).searchParams.get('after'));
      return json({ records: (rosters.get(pid) || []).filter((r) => r.seq > after), more: false });
    }
    if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(p.slice(11)))) });
    if (p === '/v1/messages') { const env = wire.parseEnvelope(o.body); await wire.verifyEnvelope(env, pubs.get(env.from).sign_key); posts.push(o.body); return json({ state: 'custody' }); }
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
  const caps = async (e, names = defaults) => {
    const session = id(); profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, names)))] });
  };
  for (const name of ['alice', 'bob']) {
    const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
    e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
    e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
    await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
    e.connected = true; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
  }
  for (const e of users) { await caps(e); for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  } }
  const [alice, bob] = users;
  const sibling = async (owner, address) => { // human_engine_check.mjs: a device linked to owner's person
    const e = new Engine({ store: memoryStore(), now: () => now, base: owner.base, fetch });
    e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
    const join = await wire.joinConsent(e.keys, address, owner.me.person, owner.me.seq + 1, owner.me.hash);
    const humans = [...await wire.rosterHumans(owner.roster), e.fp]; // a linked human phone, not an agent host
    const next = await wire.nextRoster(owner.keys, owner.address, owner.roster, [...owner.me.devices.map((d) => pubs.get(d.address)), e.pub], join, owner.roster.label, humans);
    await wire.verifyNext(next, owner.roster);
    const steps = [...await Promise.all(rosters.get(owner.me.person).map((r) => wire.parseRoster(r))), next];
    owner.me = await owner.personRecord(steps, 'self', owner.me); owner.roster = next;
    e.me = await e.personRecord(steps, 'self', null); e.roster = next;
    pubs.set(address, e.pub); rosters.set(owner.me.person, steps.map((r) => JSON.parse(wire.rosterJSON(r))));
    await owner.store.write([{ s: 'kv', k: 'person', v: owner.me }]); await owner.pinDevices(owner.me);
    await e.store.write([{ s: 'kv', k: 'person', v: e.me }, { s: 'kv', k: 'identity', v: { keys: e.keys, address, fingerprint: e.fp } },
      ...(await owner.store.all('persons')).filter((p) => p.person !== e.me.person).map((p) => ({ s: 'persons', k: p.person, v: p })),
      ...(await owner.store.all('pins')).map((p) => ({ s: 'pins', k: p.address, v: p }))]);
    e.connected = true; await caps(e); await caps(owner); users.push(e); await e.load(); return e;
  };
  const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
  const drain = async (e) => {
    // MEL-547 returns after saving; wait for the synthetic relay to have
    // the asynchronously posted copies before delivering them to a peer.
    await Promise.all(users.map(user => user.outboxPass));
    for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); }
  };
  const settle = async (e) => { while (e.erasing) await e.erasing; };
  return { alice, bob, users, posts, caps, sibling, receive, drain, settle, offline: (v) => { offline = v; } };
}


const w = await world(), { alice: a, bob: b } = w;
const phone = await w.sibling(a, 'alice/phone');
// settle: finish the block sync, then post its carriers as the outbox pass would.
const settle = async (e) => { for (let i = 0; i < 4; i++) if (e.blockSyncRun) await e.blockSyncRun; for (const r of (await e.store.all('outbox')).filter(r => r.sub === wire.SubBlockSync && r.state === 'queued')) await e.post(r); };
const bodies = async (e) => (await e.dm(conv)).messages.map(m => m.body);
const blockedHeld = async (e) => (await e.store.all('held')).filter(h => h.reason === 'blocked');
const conv = await a.newDM(b.address);
await w.drain(b); await w.drain(phone);
await a.sendDM({ conv, body: 'HELLO' });
await w.drain(b); await w.drain(phone);
await b.sendDM({ conv, body: 'BEFORE BLOCK' });
await w.drain(a); await w.drain(phone);
check((await bodies(a)).includes('BEFORE BLOCK') && (await bodies(phone)).includes('BEFORE BLOCK'), 'direct messages arrive before blocking');

let self = false; try { await a.api('/api/person/block', { person: a.me.person, blocked: true }); } catch { self = true; }
check(self, 'a person cannot block themself');
const set = await a.api('/api/person/block', { person: b.me.person, blocked: true });
check(set.note === 'Blocked.' && set.blocked === true, 'the block route answers what it saved');
check((await a.blockedPersons()).join() === b.me.person, 'the block is saved at once on this device');
const o = await a.overview();
check(o.block === true && o.blocked?.join() === b.me.person, 'the overview lists the blocked person');
await settle(a);
const carriers = (await a.store.all('outbox')).filter(r => r.sub === wire.SubBlockSync);
check(carriers.length === 1 && carriers[0].to === phone.address && carriers[0].required_cap === wire.CapReminderSyncV2 && carriers[0].aside, 'one quiet rem2 carrier, to the own phone only');
await w.drain(phone);
check((await phone.blockedPersons()).join() === b.me.person, 'the block follows the own human device');
check(!(await phone.store.all('inbox')).some(r => r.sub === wire.SubBlockSync), 'a block mark is never a timeline turn');

await b.sendDM({ conv, body: 'WHILE BLOCKED' });
await w.drain(a); await w.drain(phone);
for (const e of [a, phone]) {
  const held = await blockedHeld(e);
  check(held.length === 1 && held[0].notice_archived === true, 'held quietly on ' + e.address);
  check(!(await bodies(e)).includes('WHILE BLOCKED'), 'the blocked message never reaches the chat on ' + e.address);
  check(!(await e.overview()).quarantine.some(q => q.notice_reason === 'blocked'), 'no held notice for a blocked message on ' + e.address);
}
check((await b.blockedPersons()).length === 0, 'the blocked person learns nothing of the block');

// Another person's device cannot set our blocks.
const seal = async (sender, marks, target = a) => wire.seal({ v: wire.Version2, id: wire.newID(), from: sender.address, to: target.address, ts: Math.floor(now / 1000), kind: 'message', sub: wire.SubBlockSync, replica: true, body: JSON.stringify({ v: 1, person: a.me.person, roster: a.me.hash, blocks: marks }) }, sender.keys, target.pub);
const other = wire.newID();
const foreign = await w.receive(await seal(b, [{ person: other, blocked: true, revision: now + 5, writer: b.fp }]), a);
check((await a.store.get('held', foreign))?.reason === 'invalid' && !(await a.blockedPersons()).includes(other), 'another person cannot block anyone for us');

// An older mark never replaces a newer one, whatever the arrival order.
const mark = (await a.store.get('kv', a.blockMarkKey(a.me.person, b.me.person)));
await w.receive(await seal(phone, [{ person: b.me.person, blocked: false, revision: mark.revision - 1, writer: phone.fp }]), a);
check((await a.blockedPersons()).join() === b.me.person, 'a stale unblock leaves the newer block');

const unset = await phone.api('/api/person/block', { person: b.me.person, blocked: false });
check(unset.note === 'Unblocked.' && unset.blocked === false, 'unblock answers what it saved');
await settle(phone); await w.drain(a);
check((await a.blockedPersons()).length === 0, 'unblocking follows the own devices');
await b.sendDM({ conv, body: 'AFTER UNBLOCK' });
await w.drain(a); await w.drain(phone);
for (const e of [a, phone]) {
  const got = await bodies(e);
  check(got.includes('AFTER UNBLOCK') && !got.includes('WHILE BLOCKED'), 'new messages arrive again and the held one stays held on ' + e.address);
}
for (const e of w.users) e.stop();
console.log('blocksync Engine checks: ' + checks + ' PASS');
