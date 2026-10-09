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
const src = readFileSync(new URL('../static/engine.mjs', import.meta.url), 'utf8');
assert.ok(!/agentnet trust/.test(src), 'the engine still sends people to "agentnet trust"');
checks++;
console.log(JSON.stringify({ checks }));
