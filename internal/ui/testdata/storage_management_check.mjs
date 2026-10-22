import assert from 'node:assert/strict';
import {Engine, HubError, memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';

const store=memoryStore(), e=new Engine({store,base:'https://storage.example.test'});
e.keys=await wire.newKeys();e.address='alice/laptop';
const peer=await wire.publicEntry(await wire.newKeys(),'bob/laptop'),pin={fingerprint:'f'.repeat(64)};
e.sendKey=async()=>pin;e.pubOf=async()=>peer;e.runHistory=async()=>{};
await store.write([{s:'pins',k:'bob/laptop',v:pin}]);
let failure='relay_quota',posts=0,queued=0;
e.queueOutbox=()=>{queued++;};
e.call=async(method,path,body)=>{
  if(path==='/v1/blobs'){
    if(failure)throw new HubError(failure==='relay_disk_full'?507:413,failure,'PRIVATE_PROVIDER_DETAIL');
    return {state:'stored',received:body.size,size:body.size};
  }
  if(path==='/v1/messages'){posts++;return {id:body.id,state:'custody'};}
  throw Error('unexpected network '+method+' '+path);
};
const sent=await e.sendV1({queued:true,to:'bob/laptop',kind:'message',body:'exact stored text',files:[{name:'image.jpg',bytes:new Uint8Array([1,2,3,4])}]});
let rec=await store.get('outbox',sent.id);
assert.equal(rec.handover_proven,true);assert.equal(rec.handover_started,false);
const envelope=JSON.stringify(rec.envelope),cipher=wire.hex(rec.files[0].ct);
await e.postStored(rec);
rec=await store.get('outbox',sent.id);
assert.equal(rec.state,'failed');assert.match(rec.detail,/^send_error:relay_quota:/);assert(!rec.detail.includes('PRIVATE_PROVIDER_DETAIL'));
for(const guard of [{handover_proven:undefined},{handover_started:true}]){
  const changed={...rec,...guard};await store.write([{s:'outbox',k:rec.id,v:changed}]);
  assert.equal((await e.api('/api/storage/retry_send',{conv:'',id:rec.id})).retried,0);
}
await store.write([{s:'outbox',k:rec.id,v:rec}]);
const retry=await Promise.all([e.api('/api/storage/retry_send',{conv:'',id:rec.id}),e.api('/api/storage/retry_send',{conv:'',id:rec.id})]);
assert.equal(retry.reduce((n,r)=>n+r.retried,0),1,'duplicate button must requeue one exact row');
rec=await store.get('outbox',rec.id);
assert.equal(JSON.stringify(rec.envelope),envelope);assert.equal(wire.hex(rec.files[0].ct),cipher);assert.equal(posts,0);
for(const cause of ['file_too_large','relay_disk_full']){
  failure=cause;await e.postStored(rec);rec=await store.get('outbox',rec.id);
  assert.match(rec.detail,new RegExp('^send_error:'+cause+':'));
  assert.equal(rec.state,cause==='relay_disk_full'?'queued':'failed');
  if(rec.state==='failed'){await e.api('/api/storage/retry_send',{conv:'',id:rec.id});rec=await store.get('outbox',rec.id);}
}
failure='';await e.postStored(rec);rec=await store.get('outbox',rec.id);
assert.equal(rec.state,'custody');assert.equal(rec.handover_started,true);assert.equal(posts,1);
assert.equal((await e.api('/api/storage/retry_send',{conv:'',id:rec.id})).retried,0);

// A browser cache selection is advisory; a destructive action verifies local
// and actual authorized remote bytes. Wrong bytes/offline retain the copy.
const keys=await wire.newKeys(),self=await wire.publicEntry(keys,'bob/browser');
const plain=new Uint8Array([9,8,7,6,5]),sealed=await wire.encryptFile(plain,'cache.dat',self);
const cache=memoryStore(),b=new Engine({store:cache,base:'https://storage.example.test'});b.keys=keys;b.address='bob/browser';
await cache.write([{s:'inbox',k:'a'.repeat(32),v:{id:'a'.repeat(32),conv:'b'.repeat(64),attachments:[sealed.attachment]}},{s:'files',k:'ct/'+sealed.attachment.blob.id,v:{ct:sealed.ct}}]);
let broken=true,fetches=0;
b.fetch=async(url,options)=>{fetches++;assert.equal(options.headers['X-Agentnet-Agent'],b.address);assert(url.endsWith('/data'));return new Response(broken?new Uint8Array(sealed.ct.length):sealed.ct);};
const selection=await b.api('/api/storage/cache');assert.equal(selection.files.length,1);assert.equal(fetches,0,'preview must not scan the server');
await assert.rejects(()=>b.api('/api/storage/cache',{selection:selection.files,confirm:false}),/Confirm/);
let result=await b.api('/api/storage/cache',{selection:selection.files,confirm:true});assert.equal(result.removed.files,0);assert.equal(result.retained.files,1);
broken=false;result=await b.api('/api/storage/cache',{selection:selection.files,confirm:true});assert.equal(result.removed.files,1);
assert.equal(await cache.get('files','ct/'+sealed.attachment.blob.id),undefined);
const restarted=new Engine({store:cache,base:b.base});restarted.keys=keys;restarted.address=b.address;restarted.connected=true;restarted.fetch=b.fetch;
const before=fetches;await restarted.keepFiles();assert.equal(fetches,before,'restart prefetch must respect explicit cleanup');
assert.deepEqual(await wire.decryptFile(await restarted.cipherOf(sealed.attachment),sealed.attachment,keys),plain,'normal download after cleanup');

const quota=[];b.call=async(method,path,body)=>{quota.push({method,path,body});return {};};b.storageSummary=async()=>({local:{},remote:{}});
await assert.rejects(()=>b.api('/api/storage/quota',{quota_bytes:1234,confirm:false}),/Confirm/);assert.equal(quota.length,0);
await b.api('/api/storage/quota',{quota_bytes:1234,confirm:true});assert.deepEqual(quota,[{method:'POST',path:'/v1/admin/storage/quota',body:{quota_bytes:1234,confirm:true}}]);
console.log('PASS storage management: producer error codes, exact retry/CAS/legacy uncertainty, full cache proof and restart/reopen, confirmed quota adapter');
