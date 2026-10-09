// Held-back codes in the browser (ui.QuarantineItem.Code): the engine names
// every quarantine reason with exactly the code the daemon gives it
// (live.go holdCode), and its sentences send no one to a terminal. Reads
// {reasons: {reason: code}} from the Go test on stdin.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { holdCode, quarantineItem, heldDiagnosticCode, Engine, memoryStore } from '../static/engine.mjs';

const { reasons, diagnostics, proofRecovery } = JSON.parse(readFileSync(0, 'utf8'));
let checks = 0;
for(const [code,want] of Object.entries(diagnostics)) {
 const got=quarantineItem({id:'parity',from:'alice/laptop',reason:'invalid',detail_code:code,at:0});
 assert.equal(got.detail,want.detail,'native/browser diagnostic detail '+code);
 assert.equal(got.recovery,want.recovery,'native/browser recovery '+code);
 checks++;
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
  assert.deepEqual(Object.keys(item).sort(), ['at', 'can_archive', 'code', 'detail', 'id', 'peer', 'reason', 'recovery'], 'item fields for ' + JSON.stringify(reason));
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
