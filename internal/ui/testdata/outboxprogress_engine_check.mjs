import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
async function device(address){const e=new Engine({store:memoryStore(),base:'https://fixture.invalid',fetch:async()=>{throw Error('unexpected network');}});e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}}]);return e;}
const phone=await device('alice/phone'),host=await device('alice/laptop');
for(const [e,p] of [[phone,host],[host,phone]])await e.store.write([{s:'pins',k:p.address,v:{address:p.address,json:wire.marshalPublic(p.pub),fingerprint:p.fp,pending:null}}]);

const id=wire.newID(),request={v:1,id,from:phone.address,to:host.address,kind:'question',ts:1,body:'Actual flush stale custody race'};
const envelope=await wire.seal(request,phone.keys,host.pub),row={...request,fp:host.fp,envelope,at:1000,state:'queued'};
await phone.store.write([{s:'outbox',k:id,v:row}]);
phone.connected=true;
phone.fetch=async(url,opts)=>{if(new URL(url).pathname!=='/v1/messages')throw Error('Unexpected network '+url);await host.admit(envelope,wire.parseEnvelope(envelope));return new Response(JSON.stringify({state:'custody'}));};
const get=phone.store.get.bind(phone.store);let entered,release,once=true;
const started=new Promise(r=>entered=r),blocked=new Promise(r=>release=r);
phone.store.get=async(s,k)=>{if(once&&s==='pins'&&k===host.address){once=false;entered();await blocked;}return get(s,k);};
const flush=phone.flushOutboxOnce();await started;
await phone.post(row);assert.equal((await get('outbox',id)).state,'custody');
const pin=await get('pins',host.address);await phone.store.write([{s:'pins',k:host.address,v:{...pin,pending:{fingerprint:wire.newID().repeat(2)}}}]);release();await flush;
assert.equal((await get('outbox',id)).state,'custody');
await phone.receiverOutboxProgress({...row,state:'failed',detail:'stale failure'});
assert.equal((await get('outbox',id)).state,'custody','a stale failure cannot erase proven server custody');
const preserved=await get('outbox',id);
assert.equal(await phone.receiverOutboxProgress({...row,envelope:'different ciphertext',state:'delivered'},true),false);
assert.deepEqual(await get('outbox',id),preserved,'different immutable ciphertext cannot update this copy');
for(const state of ['quarantined','expired','delivered']){
 await phone.store.write([{s:'outbox',k:id,v:{...row,state:'custody'}}]);
 await phone.dispatch('receipt',JSON.stringify({id,state,seq:2}));
 assert.equal((await get('outbox',id)).state,state,'authoritative receipt advances custody '+state);
 if(state==='delivered'){
  await phone.receiverOutboxProgress({...row,state:'queued'});
  assert.equal((await get('outbox',id)).state,'delivered','stale progress cannot regress delivered');
 }
}
await phone.store.write([{s:'outbox',k:id,v:{...row,state:'queued'}}]);
await phone.receiverOutboxProgress({...row,state:'failed',detail:'definitive initial failure'});
assert.equal((await get('outbox',id)).state,'failed','ordinary initial failure remains a failure');
await phone.store.write([{s:'outbox',k:id,v:{...row,state:'failed',delivery_cancelled:true}}]);
await phone.receiverOutboxProgress({...row,state:'queued'});
assert.equal((await get('outbox',id)).state,'failed','cancelled local sending cannot resume');
await phone.receiverOutboxProgress({...row,state:'delivered'},true);
assert.equal((await get('outbox',id)).state,'delivered','genuine proven receipt can still reach a locally cancelled copy');
await phone.store.write([{s:'outbox',k:id,v:{...row,body:'',receiver_redacted:true,state:'queued'}}]);
await phone.receiverOutboxProgress({...row,state:'custody'},true);
assert.equal((await get('outbox',id)).state,'custody');
assert.equal((await get('outbox',id)).body,'','custody metadata cannot restore redacted plaintext');
assert.ok((await get('outbox',id)).receiver_redacted);
// Existing relay custody is demoted with byte-identical signed ciphertext;
// ordinary turns are untouched and a concurrent delivered receipt wins.
const legacyID=wire.newID(),legacyBody=JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),recipient:host.address,recipient_key:host.fp,item:JSON.parse(wire.historyJSON({id:request.id,lid:request.id,from:phone.address,from_key:phone.fp,kind:'question',body:request.body,ts:1,at:1000,attachments:[]}))});
const legacyEnvelope=await wire.seal({v:2,id:legacyID,from:phone.address,to:host.address,ts:1,kind:'message',sub:wire.SubDeviceHistory,replica:true,body:legacyBody},phone.keys,host.pub);
await phone.store.write([{s:'outbox',k:legacyID,v:{id:legacyID,to:host.address,recipient_fp:host.fp,sub:wire.SubDeviceHistory,body:legacyBody,envelope:legacyEnvelope,at:1000,state:'custody'}}]);
const ordinary=await phone.store.get('outbox',id),demotions=[];
phone.fetch=async(url,opts)=>{
 const u=new URL(url);assert.equal(u.pathname,'/v1/messages');assert.equal(u.searchParams.get('lane'),'sync');assert.equal(opts.body,legacyEnvelope);await wire.verifyEnvelope(wire.parseEnvelope(opts.body),phone.pub.sign_key);demotions.push(opts.body);
 await phone.dispatch('receipt',JSON.stringify({id:legacyID,state:'delivered',seq:10}));return new Response(JSON.stringify({state:'custody'}));
};
await phone.migrateSyncCustody();if(phone.syncOutboxPass)await phone.syncOutboxPass;
assert.equal(demotions.length,1);assert.equal((await phone.store.get('outbox',legacyID)).state,'delivered','late delivered receipt never regresses during demotion');
assert.deepEqual(await phone.store.get('outbox',id),ordinary,'ordinary custody ciphertext/state remain unchanged');
await phone.migrateSyncCustody();assert.equal(demotions.length,1,'durable completed migration does not reannounce again');
phone.connected=false;await phone.close();await host.close();
console.log('Signed stale custody progress and authoritative receipt ordering PASS');
