import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';

const store=memoryStore(),engine=new Engine({store,base:'https://isolated.invalid',fetch:async()=>{throw Error('offline fixture');}});
engine.keys=await wire.newKeys();engine.address='owner/phone';
const phone=await wire.publicEntry(engine.keys,engine.address);engine.fp=await wire.fingerprint(phone);
const hostKeys=await wire.newKeys(),host=await wire.publicEntry(hostKeys,'owner/desk'),hostFP=await wire.fingerprint(host),pin={fingerprint:hostFP};
engine.me={state:'self',person:'a'.repeat(32),human_keys:[engine.fp],devices:[{address:engine.address,fingerprint:engine.fp},{address:host.address,fingerprint:hostFP}]};
engine.sendKey=async()=>pin;engine.pinned=async()=>pin;engine.pubOf=async()=>host;engine.ctlSupport=async()=>[true,''];
let serial=0;const id=()=> (++serial).toString(16).padStart(32,'0');
const statuses=async ref=>(await store.byThreadPeer('inbox',host.address)).filter(r=>r.sub===wire.SubStatus&&r.ref?.id===ref.id&&r.ref.fingerprint===ref.fingerprint);
async function receive(pay,ref,keys=hostKeys,publicKey=host){
 const raw=await wire.seal({v:3,id:id(),from:publicKey.address,to:engine.address,ts:1700000000,kind:'message',sub:wire.SubStatus,body:JSON.stringify(pay),ref},keys,phone);
 const env=wire.parseEnvelope(raw);await wire.verifyEnvelope(env,host.sign_key);
 const inner=await wire.open(raw,engine.keys,engine.address,host),ops=await engine.admitControl(inner,env,pin);await store.write(ops,ops.checks||[]);
}
for(const [state,attempt] of [['answered',1],['cancelled',1],['resolved',1],['answered',2],['running',2]]){
 const task=id(),decision=id(),ref={id:task,fingerprint:engine.fp},m={v:1,id:task,to:host.address,kind:'task',body:'Exact retained request',at:1,attachments:[]};
 await store.write([{s:'outbox',k:task,v:m}]);
 await receive({state:'running',n:1,at:1700000000,attempt:1},ref);
 const before=engine.execOn(await statuses(ref),host.address,false);assert.equal(before.state,'running');assert.equal(before.stale,true);
 await engine.decide({host:host.address,id:task,key:engine.fp,action:'cancel',expect:'running',attempt:1,send_id:decision});
 assert.equal((await engine.stopOf(m,engine.fp,before)).pending,true,'queueing never proves cancellation');
 const refusal='Retained stale-CAS refusal; no prose is interpreted.';
 await receive({state,n:2,at:1700000001,attempt:1,decision,report:'',refused:refusal},ref);
 const ambiguous=engine.execOn(await statuses(ref),host.address,false);
 assert.equal(ambiguous.state,'running','echoed CAS attempt cannot promote a terminal host snapshot');
 assert.equal(ambiguous.attempt,1);
 assert.equal((await engine.stopOf(m,engine.fp,ambiguous)).result.refused,refusal,'the host refusal remains visible');
 await assert.rejects(receive({state:'answered',n:3,at:1700000002,attempt:2,decision,report:'',refused:refusal},ref),/attempt differ/);
 await assert.rejects(receive({state:'answered',n:3,at:1700000002,attempt:1},{...ref,fingerprint:hostFP}),/no request|did not send/);
 await receive({state,n:4,at:1700000003,attempt},ref);
 const current=engine.execOn(await statuses(ref),host.address,false);
 assert.equal(current.state,state);assert.equal(current.attempt,attempt,'ordinary signed status supplies actual attempt');
 if(state==='running'){
  const stop=await engine.stopOf(m,engine.fp,current);assert.equal(stop.attempt,2);assert.equal(stop.result,undefined,'old terminal snapshot never terminates or answers a newer running attempt');
 }else assert.equal(await engine.stopOf(m,engine.fp,current),null,'terminal execution offers no Stop');
 const restarted=new Engine({store,base:engine.base,fetch:engine.fetch});
 const durable=restarted.execOn(await statuses(ref),host.address,false);assert.equal(durable.state,state);assert.equal(durable.attempt,attempt,'reloaded Engine retains actual-attempt projection');restarted.stop();
 assert((await statuses(ref)).some(r=>JSON.parse(r.body).refused===refusal),'reconciliation preserves durable refusal evidence');
}
const wrongKeys=await wire.newKeys(),wrong=await wire.publicEntry(wrongKeys,host.address);
await assert.rejects(receive({state:'answered',n:99,at:1700000000,attempt:1},{id:id(),fingerprint:engine.fp},wrongKeys,wrong),/signature|verify/i);
engine.stop();
console.log('PASS signed decision reconciliation: stale CAS, exact ordinary attempt, newer running, pending, wrong key/ref/attempt, refusal retention and reload');
