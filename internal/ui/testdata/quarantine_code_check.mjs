// Held-back codes in the browser (ui.QuarantineItem.Code): the engine names
// every quarantine reason with exactly the code the daemon gives it
// (live.go holdCode), and its sentences send no one to a terminal. Reads
// {reasons: {reason: code}} from the Go test on stdin.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { holdCode, quarantineItem, heldDiagnosticCode, heldLogicalKey, Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';

const { reasons, diagnostics, proofRecovery, parity } = JSON.parse(readFileSync(0, 'utf8'));
let checks = 0;
for(const [code,want] of Object.entries(diagnostics)) {
 const got=quarantineItem({id:'parity',from:'alice/laptop',reason:'invalid',detail_code:code,at:0});
 assert.equal(got.detail,want.detail,'native/browser diagnostic detail '+code);
 assert.equal(got.recovery,want.recovery,'native/browser recovery '+code);
 checks++;
}
// Every field of every held item is the daemon's (live.go quarantineItems):
// who sent it (sender_verified), who can act (action), and the record and
// size each copy is counted by. Times are compared as instants; the reason
// sentence differs on purpose (a browser trusts no keys).
for (const [i, q] of parity.held.entries()) {
 const want = parity.items[i], got = quarantineItem({ id: q.id, from: q.sender, reason: q.reason, detail_code: q.detail_code || '', at: Date.parse(q.received_at), envelope: 'x'.repeat(q.size || 0), ...(q.logical ? { logical: q.logical } : {}) });
 assert.equal(Date.parse(got.at), Date.parse(want.at));
 assert.deepEqual({ ...got, at: '', reason: '' }, { ...want, at: '', reason: '' }, 'native/browser held item ' + q.reason + '/' + q.detail_code);
 checks++;
}
// A v0.8.16 browser stored its pre-open "local recipient identity changed"
// hold under the catch-all admission_failed, and never rechecks it: the
// catch-all names no stage, so it proves nothing about the sender. Only a
// logical record, kept once a copy opened, does.
{
 const v0816 = { id: 'v0816', from: 'admin/bezos', reason: 'invalid', detail_code: 'admission_failed', at: 0, envelope: 'x'.repeat(2000) };
 assert.equal(quarantineItem(v0816).sender_verified, undefined, 'v0.8.16 pre-open catch-all row names no verified sender');
 assert.equal(quarantineItem({ ...v0816, logical: 'ab'.repeat(16) }).sender_verified, true, 'an opened catch-all row does');
 assert.equal(quarantineItem({ ...v0816, detail_code: 'recipient_identity_changed', logical: 'ab'.repeat(16) }).sender_verified, undefined, 'a pre-open cause outranks a kept record');
 checks += 3;
}
// The logical record of a held copy (client.heldLogicalKey): shared vectors.
for (const v of JSON.parse(readFileSync(new URL('./held_logical.json', import.meta.url), 'utf8'))) {
 assert.equal(await heldLogicalKey(v.inner, v.fp), v.key, 'logical key ' + v.inner.id);
 checks++;
}
// The browser's own words for the causes the daemon codes precisely.
for (const [why, code] of Object.entries({
 'named participation has no unambiguous verified invitation': 'participation_invite_unresolved',
 'outside traffic has no unambiguous invitation proof yet': 'participation_invite_unresolved',
 'outside host has no verified invitation root': 'participation_invite_unresolved',
 'history outside host has no invitation proof yet': 'participation_invite_unresolved',
 'Human turn waits for its original invitation root.': 'participation_invite_unresolved',
 'group current context missing': 'group_context_unavailable',
 'group context behind latest head': 'group_context_unavailable',
 'group current context is behind latest head': 'group_context_unavailable',
 'Group current state is behind the latest head.': 'group_context_unavailable',
 'Group custody is behind the latest known head.': 'group_context_unavailable',
 'Group PID current context is missing.': 'group_context_unavailable',
 'Group status context missing.': 'group_context_unavailable',
 'the conversation is not here (yet)': 'conversation_unavailable',
 "the request's conversation is not here (yet)": 'conversation_unavailable',
 "named participation's conversation is not here yet": 'conversation_unavailable',
 "only the sender's person edits or deletes a message": 'control_not_author',
 'only the sender edits or deletes a message': 'control_not_author',
 'Only current members forward historical participation ends.': 'history_forwarder_not_member',
 'local recipient identity changed': 'recipient_identity_changed',
})) { assert.equal(heldDiagnosticCode(why), code, why); checks++; }
// A copy held after it opened records the logical record it carries: two
// copies of one forwarded item under new envelope and carrier IDs share it.
// The record names identifiers only, never the encrypted text.
{
 const store = memoryStore(), bob = new Engine({ store, base: 'https://isolated.invalid', fetch: async () => { throw Error('held copy attempted network'); } });
 bob.keys = await wire.newKeys(); bob.address = 'admin/zenbook'; bob.fp = await wire.fingerprint(await wire.publicEntry(bob.keys, bob.address));
 const aliceKeys = await wire.newKeys(), alice = await wire.publicEntry(aliceKeys, 'admin/bezos'), aliceFP = await wire.fingerprint(alice);
 await store.write([{ s: 'pins', k: alice.address, v: { address: alice.address, json: wire.marshalPublic(alice), fingerprint: aliceFP, pending: null } }]);
 const recipient = await wire.publicEntry(bob.keys, bob.address), conv = 'c'.repeat(64);
 const item = (id, lid) => JSON.stringify({ v: 1, from: 'admin/phone', from_key: 'ab'.repeat(32), id, lid, ts: 1, kind: 'message', body: 'SECRET_HELD_BODY', at: 2 });
 const first = item('1'.repeat(32), '2'.repeat(32)), second = item('3'.repeat(32), '4'.repeat(32));
 const ids = [];
 for (const [n, body] of [first, first, second].entries()) {
  const id = (n + 10).toString(16).padStart(32, '0');
  const raw = await wire.seal({ v: wire.Version2, id, from: alice.address, to: bob.address, ts: 1, kind: 'message', body, conv, lid: (n + 20).toString(16).padStart(32, '0'), sub: 'history', replica: true, root: '{"not":"a root"}' }, aliceKeys, recipient);
  await bob.onMessage(raw);
  ids.push(id);
 }
 const held = await Promise.all(ids.map(id => store.get('held', id)));
 assert.ok(held.every(h => h?.reason === 'invalid' && /^[0-9a-f]{32}$/.test(h.logical || '')), 'every copy kept with its record: ' + JSON.stringify(held.map(h => [h?.reason, h?.detail_code, h?.logical])));
 assert.equal(held[0].logical, held[1].logical, 're-sent copy names the same record');
 assert.notEqual(held[0].logical, held[2].logical, 'another record differs');
 assert.equal(held[0].logical, await heldLogicalKey({ conv, sub: 'history', from: alice.address, id: ids[0], body: first }, aliceFP));
 const item0 = quarantineItem(held[0]);
 assert.equal(item0.sender_verified, true, 'opened under its pinned key: not an unverified sender');
 assert.equal(item0.logical, held[0].logical); assert.equal(item0.size, held[0].envelope.length);
 assert.equal((await store.all('inbox')).length, 0);
 // A later hold that does not know the record keeps it.
 await bob.hold({ id: ids[0], from: alice.address }, held[0].envelope, 'invalid', 'its conversation root is not valid');
 assert.equal((await store.get('held', ids[0])).logical, held[0].logical);
 bob.stop(); checks += 8;
}

for (const [reason, code] of Object.entries(reasons)) {
  assert.equal(holdCode(reason), code, 'code for ' + JSON.stringify(reason));
  checks++;
}
// Inherited object keys are reasons like any other unknown one.
for (const reason of ['toString', 'constructor', '__proto__']) {
  assert.equal(holdCode(reason), 'unverified');
  assert.equal(quarantineItem({ id: 'h', from: 'alice/laptop', reason, at: 0 }).reason, 'It did not verify, so its content is not shown.');
  checks++;
}
// The overview's held-back item carries the code, as the daemon's does (live.go quarantineItems).
const at = Date.UTC(2026, 9, 5, 12, 0, 0);
const legacy = quarantineItem({id:'legacy-invalid',from:'alice/laptop',reason:'invalid',at});
assert.ok(legacy.reason.includes('failed a check') && legacy.reason.includes('contents stay hidden') && !legacy.reason.includes('did not verify'));
for (const [reason, code] of Object.entries(reasons)) {
  const item = quarantineItem({ id: 'held-' + reason, from: 'alice/laptop', reason, at });
  const decided = ['proof_pending', 'identity_conflict', 'conflicting_duplicate'].includes(reason) ? ['sender_verified'] : [];
  assert.deepEqual(Object.keys(item).sort(), ['at', 'can_archive', 'code', 'detail', 'id', 'peer', 'reason', 'recovery', ...decided, ...(reason === 'proof_pending' ? ['action'] : [])].sort(), 'item fields for ' + JSON.stringify(reason));
  assert.equal(item.code, code, 'item code for ' + JSON.stringify(reason));
  assert.equal(item.peer, 'alice/laptop');
  assert.equal(item.at, new Date(at).toISOString());
  checks++;
}
assert.equal(legacy.can_archive,true);
assert.ok(legacy.detail.includes('not recorded') && legacy.recovery.includes('does not accept'));
for (const why of ['SYNTHETIC_PRIVATE_BODY password=secret','toString','constructor','__proto__']) assert.equal(heldDiagnosticCode(why),'');
assert.equal(heldDiagnosticCode('Consent conflicts with recorded decision.'),'group_consent_mismatch');
for (const why of ["event is not this sending device's own", "event is not its sending device's own"]) assert.equal(heldDiagnosticCode(why),'participation_binding_mismatch');
const store=memoryStore(),engine=new Engine({store,base:'http://127.0.0.1:1',fetch:async()=>{throw Error('archive attempted network');}});
const id='d'.repeat(32),raw=JSON.stringify({id,from:'alice/laptop',invalid:true});
await engine.hold({id,from:'alice/laptop'},raw,'invalid','Consent conflicts with recorded decision.');
const before=await store.get('held',id);
const note=await engine.apiRequest('/api/act',{do:'archive_held',id});
assert.ok(note.note.includes('archived locally'));
await engine.apiRequest('/api/act',{do:'archive_held',id});
await engine.onMessage(raw); // an invalid duplicate must not revive or admit it
const after=await store.get('held',id);
assert.deepEqual(after,{...before,notice_archived:true});
assert.equal((await store.get('receipts',id)).state,'quarantined');
assert.equal((await store.all('inbox')).length,0);assert.equal((await store.all('outbox')).length,0);
assert.equal(after.detail_code,'group_consent_mismatch');
await engine.hold({id,from:'alice/laptop'},raw,'invalid','Group logical lifecycle conflict.');
assert.equal((await store.get('held',id)).notice_archived,false,'different persisted invalid cause resurfaces');
await engine.hold({id,from:'alice/laptop'},raw,'invalid','SYNTHETIC_PRIVATE_BODY password=secret');
assert.equal((await store.get('held',id)).detail_code,'admission_failed','new admission failure preserves stage without arbitrary text');
const malformedID='c'.repeat(32);
await engine.onMessage(JSON.stringify({id:malformedID,from:'alice/laptop',sig:[]}));
assert.equal((await store.get('held',malformedID)).detail_code,'envelope_malformed','producer records malformed-envelope category');
await assert.rejects(()=>engine.archiveHeldNotice('e'.repeat(32)));
await assert.rejects(()=>engine.apiRequest('/api/act',{do:'archive_held',id:'e'.repeat(32)}));
for(const [i,reason] of ['key_changed','identity_conflict','conflicting_duplicate'].entries()) {
 const heldID=(i+1).toString(16).padStart(32,'0');
 await engine.hold({id:heldID,from:'alice/laptop'},'SYNTHETIC_'+reason,reason);
 const beforeReject=await store.get('held',heldID);
 await assert.rejects(()=>engine.apiRequest('/api/act',{do:'archive_held',id:heldID}));
 assert.deepEqual(await store.get('held',heldID),beforeReject,'archive refusal changed '+reason);
 assert.equal((await store.get('receipts',heldID)).state,'quarantined');
 checks++;
}

const proofID='f'.repeat(32);
await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF','proof_pending');
assert.equal(quarantineItem(await store.get('held',proofID)).can_archive,true);
assert.equal(quarantineItem(await store.get('held',proofID)).recovery,proofRecovery,'native/browser proof recovery copy');
await engine.apiRequest('/api/act',{do:'archive_held',id:proofID});
await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF','proof_pending');
assert.equal((await store.get('held',proofID)).notice_archived,true,'same proof wait stays archived');
for(const reason of ['key_changed','identity_conflict','conflicting_duplicate','future_safety_reason']) {
 await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF',reason);
 assert.equal((await store.get('held',proofID)).notice_archived,false,'new safety reason resurfaces');
 await assert.rejects(()=>engine.apiRequest('/api/act',{do:'archive_held',id:proofID}));
 await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF','proof_pending');
 await engine.archiveHeldNotice(proofID);
}
checks+=16;
// A hidden proof wait that now waits on a more precise cause is not a new
// problem (client.holdOpened); a refusal still resurfaces.
for (const why of ['named participation has no unambiguous verified invitation', 'group current context missing', 'the conversation is not here (yet)']) {
 await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF','proof_pending',why);
 assert.equal((await store.get('held',proofID)).notice_archived,true,'precise proof cause keeps archive: '+why);
 checks++;
}
await engine.hold({id:proofID,from:'alice/laptop'},'SYNTHETIC_PROOF','invalid','Group logical lifecycle conflict.');
assert.equal((await store.get('held',proofID)).notice_archived,false,'refusal resurfaces');
{
 const s=memoryStore(),e=new Engine({store:s,base:'https://isolated.invalid',fetch:async()=>{throw Error('archive attempted network');}});
 const ids=['1','2','3','4'].map(c=>c.repeat(32));
 for(let i=0;i<ids.length;i++) await s.write([{s:'held',k:ids[i],v:{id:ids[i],from:'claimed/device',reason:i===1?'proof_pending':'invalid',detail_code:i===2?'admission_failed':'',envelope:'retained ciphertext',at:1}}]);
 const held=ids.slice(0,3).map((id,i)=>({id,reason:i===1?'proof_pending':'invalid',detail_code:''}));
 const result=await e.apiRequest('/api/act',{do:'archive_held_batch',held});
 assert.match(result.note,/Archived 2 notices/);
 assert.match((await e.archiveHeldNotices(held)).note,/Archived 0 notices/,'retry is idempotent');
 for(let i=0;i<ids.length;i++) {const r=await s.get('held',ids[i]);assert.equal(!!r.notice_archived,i<2);assert.equal(r.envelope,'retained ciphertext');}
 assert.equal((await s.all('receipts')).length,0);assert.equal((await s.all('inbox')).length,0);assert.equal((await s.all('outbox')).length,0);
 await assert.rejects(e.archiveHeldNotices([{id:ids[0],reason:'key_changed',detail_code:''}]));
 await assert.rejects(e.archiveHeldNotices(Array(257).fill(held[0])));
 e.stop();checks+=12;
}
const src = readFileSync(new URL('../static/engine.mjs', import.meta.url), 'utf8');
assert.ok(!/agentnet trust/.test(src), 'the engine still sends people to "agentnet trust"');
checks++;
console.log(JSON.stringify({ checks }));
