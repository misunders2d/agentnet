// ROOM_V1 §2.3, §2.4, §3 and §8: the browser engine reads room turns as the
// core does (client room_readers_test.go, room_group_readers_test.go).
// Isolated real-key Engine world as human_engine_check.mjs (synthetic
// transport, no network); group admission over stubbed group evidence.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0, sequence = 1;
const id = () => (sequence++).toString(16).padStart(32, '0');
const now = 1790000000123, ts = Math.floor(now / 1000);
const check = (v, why) => { assert.ok(v, why); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });

// ---- humanAuthorization as a pure rule (evidence as humanEvidence returns it).
{
  const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch: async () => { throw Error('no network'); } });
  const dev = (a) => ({ address: a, fingerprint: a + '-fp' });
  const evidence = (scopes) => ({ members: new Map([['m', { devices: [dev('member/desk')] }]]), scopes: new Map(scopes.map((p) => [p.pid, p])) });
  const part = (pid, host, fields) => ({ pid, host: dev(host), role: '', audience: 'room', state: 'active', held: 0, ...fields });
  const follower = part('f'.repeat(32), 'follower/desk'), guest = part('g'.repeat(32), 'guest/desk', { role: 'human', audience: 'conversation' });
  const proof = (pid, role) => [{ type: 'scope', pid, role }];
  const h = (author, role) => ({ ...(author ? { author_pid: author } : {}), audience: [], proof: author ? proof(author, role) : [] });
  e.humanAuthorization(h('', ''), evidence([follower]), 'member/desk', 'member/desk-fp', 'follower/desk', 'follower/desk-fp'); checks++; // a follower reads
  e.humanAuthorization(h(follower.pid, ''), evidence([follower, guest]), 'follower/desk', 'follower/desk-fp', 'member/desk', 'member/desk-fp'); checks++; // an agent author
  assert.throws(() => e.humanAuthorization(h('', ''), evidence([{ ...follower, state: 'dismissed' }]), 'member/desk', 'member/desk-fp', 'follower/desk', 'follower/desk-fp'), /ended or is held/); checks++;
  assert.throws(() => e.humanAuthorization(h(guest.pid, ''), evidence([guest]), 'guest/desk', 'guest/desk-fp', 'member/desk', 'member/desk-fp'), /role differs/); checks++;
  assert.throws(() => e.humanAuthorization(h(follower.pid, 'human'), evidence([follower]), 'follower/desk', 'follower/desk-fp', 'member/desk', 'member/desk-fp'), /role differs/); checks++;
}

// ---- a DM world: Alice and Bob members (Alice's browser reads), Carol an outside host.
const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), posts = [];
const fetch = async (url, o = {}) => {
  const p = new URL(url).pathname;
  if (p === '/v1/version') return json({ features: ['env2', 'person2', 'caps', 'notify1'], realm_id: 'test-realm' });
  if (p.endsWith('/profile')) return json(profiles.get(p.slice(11, -8)));
  if (p.startsWith('/v1/persons/') && p.endsWith('/chain')) {
    const pid = p.slice(12, -6), after = Number(new URL(url).searchParams.get('after'));
    return json({ records: (rosters.get(pid) || []).filter((r) => r.seq > after), more: false });
  }
  if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(p.slice(11)))) });
  if (p === '/v1/messages') { posts.push(o.body); return json({ state: 'custody' }); }
  if (p.endsWith('/wait')) return json({ state: 'custody' });
  if (p.endsWith('/ack') || p === '/v1/caps') return json(null, 204);
  throw new Error('unexpected synthetic path ' + p);
};
for (const name of ['alice', 'bob', 'carol']) {
  const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
  e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
  e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
  await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
  e.connected = false; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
}
for (const e of users) {
  const session = id();
  profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, [wire.CapEnv2, wire.CapPerson, wire.CapRoom])))] });
  for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  }
}
const [alice, bob, carol] = users;
const conv = await alice.newDM(bob.address), c = await alice.store.get('convs', conv);
const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
const seal = (sender, receiver, fields) => wire.seal({ v: 2, id: id(), from: sender.address, to: receiver.address, ts, conv, root: c.root, lid: id(), kind: 'message', body: '', ...fields }, sender.keys, receiver.pub);
const read = async (e, envID) => !!(await e.store.get('inbox', envID));
const held = async (e, envID) => !!(await e.store.get('held', envID));
const host = (e) => ({ person: e.me.person, address: e.address, fingerprint: e.fp });
const event = (keys, fields) => wire.signEvent(keys, { conv, ts, ...fields });
const participation = async (inviter, hostDevice, fields) => {
  const inv = await event(inviter.keys, { pid: id(), type: 'invite', author: inviter.author(), host: host(hostDevice), ...fields });
  const scope = await wire.signEvent(inviter.keys, await wire.scopeOf(inv, ts));
  const acc = await event(hostDevice.keys, { pid: inv.pid, type: 'accept', prev: await wire.eventHash(inv), author: hostDevice.author() });
  return { inv, scope, acc, pid: inv.pid, entry: { pid: inv.pid, invite: await wire.eventHash(inv), decision: await wire.eventHash(acc) } };
};
const deliver = async (sender, e, ev) => receive(await seal(sender, e, { sub: 'event', pid: ev.pid, body: wire.eventJSON(ev) }), e);
// Bob brings in Carol's agent as a follower (F), his own agent as a room
// participant (B, member-hosted), and Carol as a person guest (G).
const F = await participation(bob, carol, { audience: 'room' });
const B = await participation(bob, bob, { audience: 'room' });
const G = await participation(bob, carol, { audience: 'conversation', role: 'human' });
for (const p of [F, B, G]) { await deliver(bob, alice, p.inv); await deliver(bob, alice, p.scope); await deliver(p === B ? bob : carol, alice, p.acc); }
const infos = await alice.participationsOf(c), info = (pid) => infos.find((p) => p.pid === pid);
check(info(F.pid).state === 'active' && info(F.pid).audience === 'room' && info(F.pid).external, 'an outside follower is active at a member');
check(info(B.pid).state === 'active' && info(B.pid).audience === 'room' && !info(B.pid).external, 'a member-hosted room participant is active');
const human = (author, ...ps) => ({ ...(author ? { author_pid: author } : {}), audience: ps.map((p) => p.entry), proof: ps.flatMap((p) => [p.scope, p.acc]) });

// A member's turn captured for the room's participants and guest.
let envID = await receive(await seal(bob, alice, { body: 'to the room', origin: 'ui', human: human('', F, B, G) }), alice);
check(await read(alice, envID), 'a member reads a member turn captured for followers, a member-hosted participant and a guest');
// The follower asks the member-hosted participant as itself.
envID = await receive(await seal(carol, alice, { kind: 'question', body: 'an agent asks', pid: B.pid, target: { address: bob.address, fingerprint: bob.fp }, origin: 'agent:claude', emotion: 'curious', human: human(F.pid, F, B) }), alice);
check(await read(alice, envID), "a member reads a follower's labelled ask");
check((await alice.store.get('inbox', envID)).human.author_pid === F.pid, 'the ask keeps its agent author');
// The guest's turn, then edits carrying its audience.
const lid = id();
envID = await receive(await seal(carol, alice, { body: 'a guest line', pid: G.pid, origin: 'ui', lid, human: human(G.pid, G) }), alice);
check(await read(alice, envID), "a member reads the guest's turn");
const edit = async (sender, author, ps, key = carol.fp) => wire.seal({ v: 3, id: id(), from: sender.address, to: alice.address, ts, conv, lid: id(), kind: 'message', sub: wire.SubRevision,
  body: JSON.stringify({ rev: 1, text: 'a corrected guest line' }), ref: { id: lid, fingerprint: key }, human: human(author, ...ps) }, sender.keys, alice.pub);
for (const [what, raw] of [['another key', await edit(bob, G.pid, [G])], ['another author', await edit(carol, '', [G])], ['a broader audience', await edit(carol, G.pid, [G, F])]]) {
  envID = await receive(raw, alice);
  check(await held(alice, envID) && !await read(alice, envID), 'an edit from ' + what + ' is held');
}
envID = await receive(await edit(carol, G.pid, [G]), alice);
check(await read(alice, envID), "the guest's own edit is read");
check((await alice.store.get('inbox', envID)).person === '', "a guest's edit is kept under no member's person (shown as the core shows it)");

// ---- delivery (ROOM_V1 §2.5): a room shape keeps its primary requirement
// and needs rm1 besides it at its reader; anything else is unchanged.
check(wire.agentRequirement({ sub: 'event', body: wire.eventJSON(F.inv) }) === wire.CapHumanParticipation, "a DM room event's primary is hgp1");
check(wire.agentRequirement({ sub: 'event', body: wire.eventJSON(B.inv) }) === wire.CapHumanParticipation, "a member-hosted room event's primary is hgp1");
const historyOf = (n) => wire.historyJSON({ v: 1, from: bob.address, from_key: bob.fp, id: id(), lid: id(), ts, at: 1, kind: 'message', body: '', attachments: [], ...n });
check(wire.agentRequirement({ sub: 'history', body: historyOf({ sub: 'event', pid: F.pid, body: wire.eventJSON(F.inv) }) }) === wire.CapHumanParticipation, 'and as history');
for (const [what, rec, want] of [
  ['a room invitation', { conv, sub: 'event', body: wire.eventJSON(F.inv) }, true],
  ["a room participation's accept", { conv, sub: 'event', body: wire.eventJSON(F.acc) }, true],
  ['a DM guest invitation', { conv, sub: 'event', body: wire.eventJSON(G.inv) }, false],
  ['a turn captured with a room scope', { conv, human: human('', F) }, true],
  ['a turn captured for a DM guest only', { conv, human: human('', G) }, false],
  ['an edit carrying an audience', { conv, v: 3, control: true, sub: wire.SubRevision, human: human(G.pid, G) }, true],
  ['a room event as history', { conv, sub: 'history', body: historyOf({ sub: 'event', pid: F.pid, body: wire.eventJSON(F.inv) }) }, true],
  ['an ordinary turn', { conv, sub: '', body: 'hi' }, false],
]) check(await alice.roomCopy(rec) === want, what + (want ? ' is' : ' is not') + ' a room copy');
{
  const R = await participation(alice, bob, { audience: 'room' });
  const rec = { id: id(), conv, to: bob.address, sub: 'event', pid: R.pid, kind: 'message', body: wire.eventJSON(R.inv), state: 'queued', detail: '', at: now,
    envelope: await seal(alice, bob, { sub: 'event', pid: R.pid, body: wire.eventJSON(R.inv) }) };
  const bobCaps = async (names) => { const session = id(); profiles.set(bob.address, { person: JSON.parse(wire.rosterJSON(bob.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(bob.keys, bob.address, session, names)))] }); };
  await bobCaps([wire.CapEnv2, wire.CapPerson, wire.CapHumanParticipation]);
  const before = posts.length;
  await alice.store.write([{s:"outbox",k:rec.id,v:rec}]);
  await alice.post(rec);
  let kept = await alice.store.get('outbox', rec.id);
  check(kept.state === 'waiting' && /room participation/.test(kept.detail) && posts.length === before, 'a room copy waits at a device without rm1');
  await bobCaps([wire.CapEnv2, wire.CapPerson, wire.CapRoom]);
  await alice.post({ ...kept, state: 'queued', detail: '' });
  kept = await alice.store.get('outbox', rec.id);
  check(kept.state === 'custody' && posts.length === before + 1, 'and goes once that device reads rm1');
}

// ---- a group turn over stubbed group evidence: routing and storage.
{
  const e = alice, groupConv = 'a'.repeat(64);
  const keys = alice.keys, me = alice.me;
  const root = await wire.signGroupRoot(keys, { v: 3, kind: 'group', creator: { person: me.person, roster: me.hash, address: alice.address, fingerprint: alice.fp }, members: [{ person: me.person, roster: me.hash }], nonce: id(), created: ts, realm: id(), title: 'Room', admins: [me.person] });
  const gconv = await wire.rootID(root), rootJSON = wire.rootJSON(root);
  const admission = await wire.signGroupAdmission(keys, { conv: gconv, realm: root.realm, person: me.person, roster: me.hash, seq: 0, prev: '', history: null, by: alice.fp });
  const packet = { root, state: { members: [{ person: me.person, roster: me.hash, admin: true, admission }] } };
  const saved = { groupTurnEvidence: e.groupTurnEvidence, groupRecord: e.groupRecord, humanEvidence: e.humanEvidence };
  let evidenceOK = true;
  e.groupTurnEvidence = async () => ({ packet, members: [me] });
  e.groupRecord = async (cv) => ({ id: cv, kind: 'group', root: rootJSON });
  e.humanEvidence = async () => { if (!evidenceOK) throw Object.assign(new Error('pending'), { reason: 'proof_pending' }); return { members: new Map([[me.person, me]]), scopes: new Map([[G.pid, { pid: G.pid, role: 'human', audience: 'room', state: 'active', held: 0, host: { address: carol.address, fingerprint: carol.fp } }]]) }; };
  const gh = { author_pid: G.pid, audience: [G.entry], proof: [G.scope, G.acc] };
  const n = { v: 2, id: id(), from: carol.address, to: alice.address, ts, kind: 'message', body: 'a guest in the group', reply_to: '', conv: gconv, lid: id(), root: rootJSON, sub: '', replica: false, origin: 'ui', emotion: '', pid: G.pid, human: gh, attachments: [], fan: null, target: null, status: '', ref: null };
  const pin = await e.store.get('pins', carol.address), env = { id: n.id, from: carol.address };
  const ops = await e.admitGroupTurn(n, env, pin, { id: n.id, from: carol.address, kind: 'message', body: n.body, at: now, fp: pin.fingerprint, read: false });
  const rec = ops.find((o) => o.s === 'inbox')?.v;
  check(rec && rec.pid === G.pid && rec.human?.author_pid === G.pid && !rec.own, "a group guest's turn goes to the group turn path and keeps its captured audience");
  const memberTurn = { ...n, id: id(), lid: id(), from: bob.address, pid: '', human: { audience: [G.entry], proof: [G.scope, G.acc] } };
  await assert.rejects(e.admitGroupTurn(memberTurn, { id: memberTurn.id, from: bob.address }, await e.store.get('pins', bob.address), { id: memberTurn.id }), /Sender is not a current group device|outside captured authority/); checks++;
  evidenceOK = false;
  await assert.rejects(e.admitGroupTurn({ ...n, id: id(), lid: id() }, env, pin, { id: id() }), /pending/); checks++;
  evidenceOK = true;
  // A group request or output carrying a captured audience is not read yet
  // (client admitConv, ROOM_V1 P3/P4), live or as history.
  const memberHuman = { audience: [G.entry], proof: [G.scope, G.acc] }, bobPin = await e.store.get('pins', bob.address);
  const request = { ...n, id: id(), lid: id(), from: bob.address, kind: 'question', pid: B.pid, target: { address: bob.address, fingerprint: bob.fp }, human: memberHuman };
  await assert.rejects(e.admitGroupTurn(request, { id: request.id, from: bob.address }, bobPin, { id: request.id }), /not enabled|not read yet/); checks++;
  const alicePin = await e.store.get('pins', alice.address);
  const hist = { ...n, id: id(), lid: id(), from: alice.address, pid: '', human: undefined, sub: 'history', replica: true, attachments: [],
    body: historyOf({ from: alice.address, from_key: alice.fp, kind: 'question', origin: 'ui', pid: B.pid, target: { address: bob.address, fingerprint: bob.fp }, human: memberHuman }) };
  await assert.rejects(e.admitGroupTurn(hist, { id: hist.id, from: alice.address }, alicePin, { id: hist.id }), /not enabled|not read yet/); checks++;
  // Selected group history keeps to ordinary member turns (client
  // groupHistorySources: no PID): a guest's turn held here is not offered.
  await e.store.write(ops.filter((o) => o.s === 'inbox'));
  e.groupCurrent = async () => packet;
  const refs = await e.selectGroupHistory(gconv, { last: 8 });
  check(!refs.some((r) => r.lid === n.lid), "a guest's group turn is not offered as selected history");
  check((await e.groupSelectedItems(gconv, refs)).length === refs.length, 'and the offered history publishes');
  Object.assign(e, saved);
  delete e.groupCurrent;
}

// ---- X8: a group guest decision requires verified current group context.
// The public valid-context lifecycle is covered by human_engine_check.mjs.
{
  const e = carol, saved = e.agentConv;
  e.agentConv = async () => ({ c: { id: 'b'.repeat(64), kind: 'group', root: '{}' }, info: { pid: G.pid, role: 'human', state: 'invited', held: 0, host: host(carol), grant: [] } });
  const sentBefore = posts.length, queuedBefore = (await e.store.all('outbox')).length;
  await assert.rejects(e.changeHuman('decide', { pid: G.pid, accept: true }), err => err.reason === 'proof_pending' && /group current context missing/.test(err.message)); checks++;
  check(posts.length === sentBefore && (await e.store.all('outbox')).length === queuedBefore, 'no guest decision is stored or sent without verified group context');
  e.agentConv = saved;
}
console.log('PASS room receiver engine: ' + checks + ' checks (followers and agent authors of a captured audience, guest edits from their author only, group guest turns, group requests carrying an audience not read yet, no guest decision without verified group context)');
