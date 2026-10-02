// Local report dismissal only: real Engine/store behavior, no network/model.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
const store=memoryStore(), reportID='1'.repeat(32), outID='2'.repeat(32), taskID='3'.repeat(32);
let network=0, changes=0, writes=0;
const fetch=async()=>{network++;throw Error('local dismissal must not use network')};
const engine=new Engine({store,base:'https://synthetic.invalid',fetch});engine.listen(()=>changes++);
const report={id:reportID,v:1,from:'bot/host',to:'human/browser',kind:'message',status:'review_notice',body:'6 request(s) wait for a person on bot/host',at:1,attachments:[],read:true};
const collision={id:reportID,v:1,to:'other/host',kind:'task',body:'outbox must stay unchanged',state:'queued'};
await store.write([{s:'inbox',k:reportID,v:report},{s:'outbox',k:reportID,v:collision},{s:'outbox',k:outID,v:{...report,id:outID}},{s:'inbox',k:taskID,v:{...report,id:taskID,kind:'task',status:''}}]);
const write=store.write;store.write=async ops=>{await write(ops);writes++};
assert.equal(engine.reportItems(await store.all('inbox')).length,1);
await engine.api('/api/act',{do:'resolve',id:reportID});
assert.deepEqual(await store.get('inbox',reportID),{...report,resolved:true});assert.deepEqual(await store.get('outbox',reportID),collision);
assert.equal(changes,1);assert.equal(writes,1);assert.equal(network,0);assert.equal(engine.reportItems(await store.all('inbox')).length,0);
await engine.api('/api/act',{do:'resolve',id:reportID});assert.equal(writes,1);assert.equal(changes,1,'already dismissed is idempotent');
const reload=new Engine({store,base:engine.base,fetch});await reload.load();assert.equal(reload.reportItems(await store.all('inbox')).length,0,'resolved flag survives fresh Engine reload');
for(const id of [outID,taskID,'f'.repeat(32),'invalid'])await assert.rejects(()=>engine.api('/api/act',{do:'resolve',id}));
const malformed=[{v:2,conv:'a'.repeat(64)},{control:true},{sub:'status'},{conv:'a'.repeat(64)},{kind:'question'},{status:''},{reply_to:'4'.repeat(32)},{attachments:[{name:'secret.txt'}]},{attachments:{}},{id:'9'.repeat(32)}];
for(let i=0;i<malformed.length;i++){
 const id=(100+i).toString(16).padStart(32,'0'),m={...report,id,...malformed[i]};await write([{s:'inbox',k:id,v:m}]);
 await assert.rejects(()=>engine.api('/api/act',{do:'resolve',id}));assert.deepEqual(await store.get('inbox',id),m);
}
for(const action of ['accept','approve','unapprove','resolve-task'])await assert.rejects(()=>engine.api('/api/act',{do:action,id:taskID}),/Nothing runs/);
assert.equal(network,0);assert.equal(writes,1);assert.equal(changes,1);
// Failed durable commit neither alters source object nor emits UI change.
const failID='a'.repeat(32),m={...report,id:failID};await write([{s:'inbox',k:failID,v:m}]);
store.write=async()=>{throw Error('disk write failed')};await assert.rejects(()=>engine.api('/api/act',{do:'resolve',id:failID}),/disk write failed/);assert.deepEqual(await store.get('inbox',failID),m);assert.equal(changes,1);
console.log('PASS local report dismissal: reload/idempotence/collision/shape/task refusal/durable failure/no network');
