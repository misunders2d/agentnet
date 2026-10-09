// Actual signed DM requests and statuses over isolated browser stores.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0;
const check = (value, why) => { assert.ok(value, why); checks++; };
const pubs = new Map(), chains = new Map(), devices = [];
const json = value => new Response(JSON.stringify(value));
const fetch = async url => {
  const u = new URL(url), path = u.pathname;
  if (path.startsWith('/v1/persons/') && path.endsWith('/chain')) return json({ records: chains.get(path.slice(12, -6)).filter(r => r.seq > Number(u.searchParams.get('after'))), more: false });
  if (path.endsWith('/profile')) return json({ person: JSON.parse(wire.rosterJSON(devices.find(e => e.address === path.slice(11, -8)).roster)) });
  if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(path)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(path.slice(11)))) });
  throw Error('Unexpected fixture transport ' + path);
};
async function device(address) {
  const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch });
  e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
  e.changed = () => {}; e.runHistory = async () => {}; e.discloseHumanAudience = () => {}; e.recoverHumanExcerpts = async () => {}; e.flushReceipts = async () => {};
  pubs.set(address, e.pub); devices.push(e); return e;
}
async function person(address) {
  const e = await device(address); e.roster = await wire.newRoster(e.keys, address, address.split('/')[0]);
  chains.set(e.roster.person, [JSON.parse(wire.rosterJSON(e.roster))]); return e;
}
async function sibling(owner, address) {
  const e = await device(address), old = owner.roster;
  const consent = await wire.joinConsent(e.keys, address, old.person, old.seq + 1, await wire.rosterHash(old));
  const oldPubs = old.devices.map(d => pubs.get(d.address));
  owner.roster = await wire.nextRoster(owner.keys, owner.address, old, [...oldPubs, e.pub], consent, old.label, [...await wire.rosterHumans(old), e.fp]);
  chains.get(old.person).push(JSON.parse(wire.rosterJSON(owner.roster))); return e;
}
const host = await person('own/host');
let phone = await sibling(host, 'own/phone');
const laptop = await sibling(host, 'own/laptop'), peer = await person('peer/desk'), outsider = await person('outsider/desk');
for (const e of devices) {
  e.roster = e.address.startsWith('own/') ? host.roster : e.roster;
  e.me = await e.personRecord(await Promise.all(chains.get(e.roster.person).map(wire.parseRoster)), 'self', null);
  await e.store.write([{ s: 'kv', k: 'person', v: e.me }]);
  for (const owner of [host, peer, outsider]) {
    const p = await e.personRecord(await Promise.all(chains.get(owner.roster.person).map(wire.parseRoster)), owner.roster.person === e.me.person ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }]);
  }
  for (const d of devices) await e.store.write([{ s: 'pins', k: d.address, v: { address: d.address, json: wire.marshalPublic(d.pub), fingerprint: d.fp, pending: null } }]);
}
const root = await wire.newRoot(host.keys, host.author(), { person: peer.me.person, roster: peer.me.hash }), conv = await wire.rootID(root), pid = wire.newID();
await phone.store.write([{ s: 'convs', k: conv, v: { id: conv, root: wire.rootJSON(root), peer: peer.me.person, creator: host.address, created: root.created } }]);
const from = async (sender, fields) => wire.seal({ v: 2, id: wire.newID(), from: sender.address, to: phone.address, ts: 1, conv, root: wire.rootJSON(root), lid: wire.newID(), kind: 'message', body: '', replica: sender.me.person === phone.me.person, ...fields }, sender.keys, phone.pub);
const receive = async raw => { const env = wire.parseEnvelope(raw); await phone.onMessage(raw); return env.id; };
const invite = await wire.signEvent(host.keys, { conv, pid, type: 'invite', ts: 1, author: host.author(), host: { person: host.me.person, address: host.address, fingerprint: host.fp }, audience: 'conversation' });
await receive(await from(host, { sub: 'event', pid, body: wire.eventJSON(invite) }));
const accepted = await wire.signEvent(host.keys, { conv, pid, type: 'accept', prev: await wire.eventHash(invite), ts: 2, author: host.author() });
await receive(await from(host, { sub: 'event', pid, body: wire.eventJSON(accepted) }));
if (phone.retrying) await phone.retrying;
const request = async (sender = laptop, overrides = {}) => {
  const lid = wire.newID(), raw = await from(sender, { lid, kind: 'question', body: 'Immutable request', pid, target: { address: host.address, fingerprint: host.fp }, ...overrides });
  return { lid, raw, sender };
};
const status = async (original, sender = host, ref = { id: original.lid, fingerprint: original.sender.fp }, extra = {}) => wire.seal({ v: 3, id: wire.newID(), from: sender.address, to: phone.address, ts: 3, kind: 'message', conv, lid: wire.newID(), replica: sender.me.person === phone.me.person, sub: wire.SubStatus, body: JSON.stringify({ state: 'answered', n: 1, at: 3, attempt: 1 }), ref, ...extra }, sender.keys, phone.pub);
const original = await request(), raw = await status(original), statusID = await receive(raw);
check((await phone.store.get('held', statusID))?.reason === 'proof_pending', 'status before exact original waits for proof');
check(!await phone.store.get('inbox', statusID), 'missing original never grants status admission');
await receive(raw);
check((await phone.store.all('held')).filter(r => r.id === statusID).length === 1, 'duplicate pending status is durable once');
const previous = phone;
phone = new Engine({ store: previous.store, base: previous.base, fetch });
for (const field of ['keys', 'address', 'pub', 'fp', 'me', 'roster', 'changed', 'runHistory', 'discloseHumanAudience', 'recoverHumanExcerpts', 'flushReceipts']) phone[field] = previous[field];
check((await phone.store.get('held', statusID))?.envelope === raw, 'pending exact encrypted status survives engine reload');
await receive(original.raw);
if (phone.retrying) await phone.retrying;
check(!await phone.store.get('held', statusID) && (await phone.store.get('inbox', statusID))?.control, 'ordinary own-sibling original automatically recovers status');
check((await phone.store.get('receipts', statusID))?.state === 'delivered', 'receipt upgrades only after ordinary admission');
await receive(raw);
check((await phone.store.all('inbox')).filter(r => r.id === statusID).length === 1, 'accepted duplicate is stored once');
const foreignOriginal = await request(peer); await receive(foreignOriginal.raw);
const foreignStatus = await receive(await status(foreignOriginal));
check((await phone.store.get('inbox', foreignStatus))?.control, 'verified other DM member original binds exact host status');
for (const sender of [peer, outsider]) {
  const bad = await receive(await status(original, sender));
  check((await phone.store.get('held', bad))?.reason === 'invalid' && !await phone.store.get('inbox', bad), 'known wrong or foreign executor is invalid');
}
const plainLID = wire.newID(); await receive(await from(peer, { lid: plainLID, body: 'Ordinary text grants no executor' }));
const nonrequest = await receive(await status({ lid: plainLID, sender: peer }));
check((await phone.store.get('held', nonrequest))?.reason === 'invalid', 'known ordinary message cannot authorize execution status');
const wrongRef = await receive(await status(original, host, { id: original.lid, fingerprint: outsider.fp }));
check((await phone.store.get('held', wrongRef))?.reason === 'proof_pending' && !await phone.store.get('inbox', wrongRef), 'different requester key cannot borrow an existing logical ID');
const pin = await phone.store.get('pins', host.address);
await phone.store.write([{ s: 'pins', k: host.address, v: { ...pin, pending: { fingerprint: outsider.fp } } }]);
const changed = await receive(await status(original));
check((await phone.store.get('held', changed))?.reason === 'key_changed' && !await phone.store.get('inbox', changed), 'changed executor pin blocks admission');
await phone.store.write([{ s: 'pins', k: host.address, v: pin }]);
const ownPerson = await phone.store.get('kv', 'person');
await phone.store.write([{ s: 'kv', k: 'person', v: { ...ownPerson, human_keys: ownPerson.human_keys.filter(fp => fp !== laptop.fp) } }]);
const removed = await receive(await status(original));
check((await phone.store.get('inbox', removed))?.control, 'requester no longer human does not invalidate inert completion of retained original');
await phone.store.write([{ s: 'kv', k: 'person', v: ownPerson }]);
const historical = await request(), historyStatus = await receive(await status(historical));
const heldOriginal = await wire.open(historical.raw, phone.keys, phone.address, laptop.pub);
const item = { v: 1, id: heldOriginal.id, lid: heldOriginal.lid, from: laptop.address, from_key: laptop.fp, ts: 1, at: 1000, kind: 'question', body: heldOriginal.body, target: heldOriginal.target, pid, attachments: [] };
await receive(await from(host, { sub: 'history', body: JSON.stringify(item) }));
if (phone.retrying) await phone.retrying;
check((await phone.store.get('inbox', heldOriginal.id))?.history, 'own-synced original admitted through real signed history path');
check((await phone.store.get('inbox', historyStatus))?.control, 'exact inert own-history request authorizes observed completion only');
const atomicRaw = await status(original), atomicEnv = wire.parseEnvelope(atomicRaw), atomicInner = await wire.open(atomicRaw, phone.keys, phone.address, host.pub);
const atomicOps = await phone.admitControl(atomicInner, atomicEnv, pin);
await phone.store.write([{ s: 'pins', k: host.address, v: { ...pin, pending: { fingerprint: outsider.fp } } }]);
await assert.rejects(() => phone.store.write(atomicOps, atomicOps.checks), /storage changed/); checks++;
check(!await phone.store.get('inbox', atomicEnv.id), 'pin changed after status review prevents atomic admission');
await phone.store.write([{ s: 'pins', k: host.address, v: pin }]);
const outgoing = await request(phone), outgoingID = wire.newID();
await phone.store.write([{ s: 'outbox', k: outgoingID, v: { id: outgoingID, lid: outgoing.lid, conv, to: host.address, kind: 'question', body: 'Stored outgoing request', target: { address: host.address, fingerprint: host.fp } } }]);
check((await phone.store.get('inbox', await receive(await status(outgoing))))?.control, 'existing outgoing request status still admitted');
// An old held lookup can remain blocked while the original that wakes it and
// a later fresh turn finish normal foreground admission.
const blocked = await request(), blockedID = await receive(await status(blocked));
let release; const gate = new Promise(resolve => { release = resolve; });
const admitInner = phone.admitInner.bind(phone); let reached;
const started = new Promise(resolve => { reached = resolve; });
phone.admitInner = async (data, env) => { if (env.id === blockedID) { reached(); await gate; } return admitInner(data, env); };
await receive(blocked.raw); await started;
const fresh = await from(peer, { body: 'Fresh after original' }), freshID = await receive(fresh);
check((await phone.store.get('inbox', freshID))?.body === 'Fresh after original', 'held proof retry never blocks later foreground turn');
release(); if (phone.retrying) await phone.retrying;
phone.admitInner = admitInner;
check((await phone.store.get('inbox', blockedID))?.control, 'background held retry completes after lookup release');
// The ongoing pass has already inspected this missing status when its
// original arrives. The coalesced rerun must return to the earlier row.
const late = await request(), laterBlock = await request();
const lateID = await receive(await status(late, host, undefined, { id: '0'.repeat(31) + '1' }));
const laterID = await receive(await status(laterBlock, host, undefined, { id: 'f'.repeat(31) + 'e' }));
let releasePass, enteredPass;
const passGate = new Promise(resolve => { releasePass = resolve; }), passEntered = new Promise(resolve => { enteredPass = resolve; });
phone.admitInner = async (data, env) => { if (env.id === laterID) { enteredPass(); await passGate; } return admitInner(data, env); };
const runningPass = phone.retryHeld(); await passEntered;
check(!await phone.store.get('inbox', lateID), 'running pass inspected missing earlier status before original');
await receive(late.raw);
check(phone.retryAgain, 'original arriving during held pass requests coalesced rerun');
releasePass(); await runningPass; phone.admitInner = admitInner;
check((await phone.store.get('inbox', lateID))?.control, 'coalesced pass revisits and recovers earlier status');
// The old browser verifier kept own-inbox statuses as proof_pending even
// when their original was already here. Its normal startup retry repairs
// that retained ciphertext; no invalid-status migration is required.
const legacyOriginal = await request(); await receive(legacyOriginal.raw);
const legacyRaw = await status(legacyOriginal), legacyEnv = wire.parseEnvelope(legacyRaw);
await phone.hold(legacyEnv, legacyRaw, 'proof_pending', 'no request of this device that own/host executes is here (yet)');
const untouched = await status(original, outsider), untouchedEnv = wire.parseEnvelope(untouched);
await phone.hold(untouchedEnv, untouched, 'invalid', 'known wrong executor');
await phone.store.write([{ s: 'kv', k: 'identity', v: { keys: phone.keys, address: phone.address, fingerprint: phone.fp } }]);
const oldEngine = phone;
phone = new Engine({ store: oldEngine.store, base: oldEngine.base, fetch });
for (const field of ['pub', 'roster', 'changed', 'runHistory', 'discloseHumanAudience', 'recoverHumanExcerpts', 'flushReceipts']) phone[field] = oldEngine[field];
await phone.load();
check((await phone.store.get('held', legacyEnv.id))?.envelope === legacyRaw, 'real reload preserves old exact proof-pending status ciphertext');
await phone.retryHeld();
check((await phone.store.get('inbox', legacyEnv.id))?.control && !await phone.store.get('held', legacyEnv.id), 'normal startup retry repairs retained old own-inbox status with original already here');
check((await phone.store.get('receipts', legacyEnv.id))?.state === 'delivered', 'retained old status receipt upgraded only after verified admission');
check((await phone.store.get('held', untouchedEnv.id))?.reason === 'invalid' && !await phone.store.get('inbox', untouchedEnv.id), 'unrelated invalid status remains blocked without an upgrade scan');
check(!(await phone.store.all('outbox')).some(r => r.kind === 'task'), 'status admission creates no task or replay');
console.log(JSON.stringify({ ok: true, checks }));
