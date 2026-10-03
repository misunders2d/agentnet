// Browser parity for deleting a conversation from all of a person's own
// devices (client convclear.go, exact names only). Isolated real-key Engine world (as human_engine_check.mjs): no
// real peer, browser or network. stdin: {vectors, body} from
// convclear_browser_test.go (judged by envelope.ValidateControl).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0, sequence = 1;
const id = () => (sequence++).toString(16).padStart(32, '0');
const now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };
const refuses = async (fn, re) => { await assert.rejects(fn, re); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
const defaults = [wire.CapEnv2, wire.CapPerson, wire.CapControl];
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
    const next = await wire.nextRoster(owner.keys, owner.address, owner.roster, [...owner.me.devices.map((d) => pubs.get(d.address)), e.pub], join);
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
  const drain = async (e) => { for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); } };
  const settle = async (e) => { while (e.erasing) await e.erasing; };
  return { alice, bob, users, posts, caps, sibling, receive, drain, settle, offline: (v) => { offline = v; } };
}

const { vectors, body } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
// The shared Go vectors, judged by the browser's checkVersion2; the Go payload bytes round-trip exactly.
const shapes = {};
for (const { name, inner } of vectors) {
  const n = { reply_to: '', status: '', conv: '', lid: '', sub: '', replica: false, origin: '', emotion: '', target: null, pid: '', fan: null, ref: null, agent_id: '', receiver_route: null, human: null,
    ...inner, root: inner.root ? JSON.stringify(inner.root) : '', attachments: inner.attachments || [] };
  try { await wire.checkVersion2(n); shapes[name] = true; } catch (e) { shapes[name] = false; }
}
check(wire.clearJSON(wire.parseControl(wire.SubClear, body)) === body, 'browser encodes exactly the Go clear payload');
check(/newCaps\([^\n]*CapConvClear/.test(readFileSync(new URL('../static/engine.mjs', import.meta.url), 'utf8')), 'the browser publishes clr1 in its caps');

const w = await world(), { alice: a, bob: b } = w;
const phone = await w.sibling(a, 'alice/phone');
const conv = await a.newDM(b.address);
await w.drain(b); await w.drain(phone);
const fileBytes = new TextEncoder().encode('OLD FILE BYTES');
await a.sendDM({ conv, body: 'OLD FROM ALICE', files: [{ name: 'old.txt', size: fileBytes.length, bytes: fileBytes }] });
await w.drain(b); await w.drain(phone);
await b.sendDM({ conv, body: 'OLD FROM BOB' });
await w.drain(a); await w.drain(phone);
await b.sendDM({ conv, body: 'BOB TO PHONE ONLY LATE' }); // reaches alice/desk later: alice never holds it when deleting
await w.drain(phone);
check((await phone.dm(conv)).messages.some((m) => m.body === 'OLD FROM ALICE') && (await phone.dm(conv)).messages.some((m) => m.body === 'OLD FROM BOB'), 'own linked device holds the conversation');
check(!!(await a.store.get('files', 'kept/' + wire.hex(await wire.sha256(fileBytes)))), 'a kept copy of the sent file is here before deletion');

// Unfinished work: a copy queued while offline is kept (hidden) until handed over.
w.offline(true); a.connected = false;
await a.sendDM({ conv, body: 'QUEUED WHILE OFFLINE' });
const queued = (await a.store.all('outbox')).filter((r) => r.body === 'QUEUED WHILE OFFLINE');
check(queued.length && queued.every((r) => ['queued', 'waiting'].includes(r.state)), 'an unsent copy is kept to send');
w.offline(false); a.connected = true;

// Deleting on alice/desk: exact names here, hidden here, queued for alice/phone (clr1 off there: waiting).
await refuses(() => a.deleteConversation({ conv, peer: b.address }), /one conversation/);
await refuses(() => a.deleteConversation({ conv: 'f'.repeat(64) }), /No such conversation/);
const done = await a.api('/api/conversation/delete', { conv });
check(/Queued for your 1 other device/.test(done.note) && /Others in it keep their copies/.test(done.note) && /still in progress keep running/.test(done.note), 'truthful queued note: ' + done.note);
await w.settle(a);
const aView = await a.dm(conv);
check(!aView.messages.some((m) => m.body && /OLD|QUEUED/.test(m.body)), 'deleted turns are not shown here');
check(!(await a.overview()).dms.some((d) => d.id === conv), 'the deleted conversation leaves the list');
check((await a.store.all('inbox')).filter((r) => r.conv === conv && !r.control && !r.sub).every((r) => r.body === ''), 'received text erased here');
check((await a.store.all('outbox')).filter((r) => r.body === 'QUEUED WHILE OFFLINE').length === queued.length, 'the unsent copy keeps its text until handed over');
check(!(await a.store.get('files', 'kept/' + wire.hex(await wire.sha256(fileBytes)))), 'the kept sent-file copy is dropped');
check(!!(await a.store.get('convs', conv)) && (await a.store.all('erased')).every((r) => r.conv === conv && r.shared), 'membership/root kept; every name told');
const clears = (await a.store.all('outbox')).filter((r) => r.sub === wire.SubClear);
check(clears.length === 1 && clears[0].to === phone.address && clears[0].required_cap === wire.CapConvClear && clears[0].state === 'waiting', 'one clear part for the own phone only, waiting for clr1');
check((await b.dm(conv)).messages.some((m) => m.body === 'OLD FROM ALICE'), 'the other person keeps their copy');
check((await b.store.all('inbox')).every((r) => r.sub !== wire.SubClear), 'nothing is told to the other person');

// Handing the queued copy over ends its work: the change erases its text.
await a.flushOutbox(); await w.settle(a);
check((await a.store.all('outbox')).filter((r) => r.conv === conv && r.lid === queued[0].lid).every((r) => r.body === '' && !['queued', 'waiting'].includes(r.state)), 'handed-over copy erased by the change that ended its work');

// Release on capability: the phone applies exactly the named turns.
check((await w.drain(phone), (await phone.dm(conv)).messages.some((m) => m.body === 'OLD FROM BOB')), 'nothing applies before clr1');
await w.caps(phone, [...defaults, wire.CapConvClear]);
await a.flushOutbox(); await w.drain(phone); await w.settle(phone);
const pView = await phone.dm(conv);
check(!pView.messages.some((m) => /OLD FROM/.test(m.body || '')), 'own linked device erased the named turns');
check(pView.messages.some((m) => m.body === 'BOB TO PHONE ONLY LATE'), 'a turn the deleting device never held stays (not inferred)');
check((await phone.overview()).dms.some((d) => d.id === conv), 'with an unnamed later turn the conversation stays listed there');

// Late copies never restore: the same turn again (retry/other route) stays a skeleton.
await w.drain(a); // BOB TO PHONE ONLY LATE now reaches alice/desk: newer, unnamed, shown
check((await a.dm(conv)).messages.some((m) => m.body === 'BOB TO PHONE ONLY LATE') && (await a.overview()).dms.some((d) => d.id === conv), 'a later turn reopens the conversation with only itself');
const oldBob = (await phone.store.all('inbox')).find((r) => r.conv === conv && r.from === b.address && !r.sub && r.body === '');
const late = await wire.seal({ v: 2, id: id(), from: b.address, to: phone.address, ts: Math.floor(now / 1000), conv, root: (await b.store.get('convs', conv)).root, lid: oldBob.lid, kind: 'message', body: 'OLD FROM BOB' }, b.keys, phone.pub);
await w.receive(late, phone);
check(!(await phone.dm(conv)).messages.some((m) => m.body === 'OLD FROM BOB') && (await phone.store.all('inbox')).filter((r) => r.lid === oldBob.lid).every((r) => r.body === ''), 'a late copy of an erased turn stays a skeleton');

// A device linked later learns every name and gets no deleted history.
const tablet = await w.sibling(a, 'alice/tablet');
await w.caps(tablet, [...defaults, wire.CapConvClear]);
await a.startHistory(tablet.address, tablet.fp); await a.runHistory(); await a.flushOutbox(); await w.drain(tablet); await tablet.retryHeld();
const tView = await tablet.dm(conv).catch(() => ({ messages: [] }));
check(!tView.messages.some((m) => /OLD FROM/.test(m.body || '')), 'new link: no deleted turn in its history');
check((await tablet.store.all('erased')).length >= (await a.store.all('erased')).length, 'new link learns every erased name');

// Another person cannot delete anything here.
const ref = { id: (await a.store.all('outbox')).find((r) => r.conv === conv && r.lid).lid, fingerprint: a.fp };
const foreign = await wire.seal({ v: 3, id: id(), from: b.address, to: a.address, ts: Math.floor(now / 1000), kind: 'message', sub: wire.SubClear,
  body: wire.clearJSON({ deletion: id(), part: 1, parts: 1, turns: [] }), ref, conv, lid: id(), replica: true, fan: [{ person: b.me.person, roster: b.me.hash }] }, b.keys, a.pub);
const fid = await w.receive(foreign, a);
check((await a.store.get('held', fid))?.reason === 'invalid' && !(await a.store.get('inbox', fid)), "another person's deletion is refused");

// A device thread is erased exactly, here only.
const v1 = async (bodyText) => { const raw = await wire.seal({ v: 1, id: id(), from: b.address, to: a.address, ts: Math.floor(now / 1000), kind: 'message', body: bodyText }, b.keys, a.pub); await w.receive(raw, a); return wire.parseEnvelope(raw).id; };
const t1 = await v1('THREAD ONE'), t2 = await v1('THREAD TWO');
const before = (await a.store.all('outbox')).length;
const td = await a.deleteConversation({ peer: b.address, thread: t1 });
check(/stored on this device only/.test(td.note), 'thread deletion says this device only');
const threads = (await a.overview()).threads;
check(!threads.some((t) => t.id === t1) && threads.some((t) => t.id === t2), 'exactly that thread is gone; the other stays');
check((await a.store.all('outbox')).length === before, 'a thread deletion tells no one');

console.log(JSON.stringify({ shapes, checks }));
