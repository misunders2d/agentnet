import assert from 'node:assert/strict';
import {Engine,memoryStore,HubError} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
async function fixture(store=memoryStore()) {
 const posts=[],e=new Engine({store,base:'https://isolated.invalid',fetch:async(url,{body})=>{
  assert(url.endsWith('/v1/messages'));posts.push(body);return new Response(JSON.stringify({state:'custody'}));
 }});
 e.keys=await wire.newKeys();e.address='alice/phone';e.fp=await wire.fingerprint(await wire.publicEntry(e.keys,e.address));
 const recipient=await wire.publicEntry(await wire.newKeys(),'bob/desk'),pin={fingerprint:await wire.fingerprint(recipient)};
 e.sendKey=async()=>pin;e.pinned=async()=>pin;e.pubOf=async()=>recipient;e.ctlSupport=async()=>[true,''];
 await store.write([{s:'pins',k:'bob/desk',v:pin}]);
 return {e,store,posts};
}
// The actual Delete action fences a queued original before posting its
// retraction; a restart/reconnect cannot release the same execution input.
for(const kind of ['question','task']) {
 const {e,store,posts}=await fixture();
 const sent=await e.sendV1({queued:true,to:'bob/desk',kind,body:'never handed over',files:[]});
 const original=await store.get('outbox',sent.id);
 await e.messageControl('delete',{id:sent.id,dir:'out'});
 const cancelled=await store.get('outbox',sent.id);
 const restarted=new Engine({store,base:e.base,fetch:e.fetch});Object.assign(restarted,{keys:e.keys,address:e.address,fp:e.fp,connected:true});
 await restarted.flushOutbox();assert(!posts.includes(original.envelope),'deleted local request never posts after restart');
 assert.equal(cancelled.delivery_cancelled,true);assert.equal(cancelled.state,'failed');
 assert.match(cancelled.detail,/before handover; not sent/);assert.equal(cancelled.envelope,original.envelope);
 e.stop();restarted.stop();
}
// Delete racing a file upload wins before final handover, and stale upload
// completion cannot resurrect the cancelled durable row.
{
 const {e,store,posts}=await fixture(),entered=deferred(),release=deferred();
 const sent=await e.sendV1({queued:true,to:'bob/desk',kind:'question',body:'upload race',files:[]});
 const original=await store.get('outbox',sent.id);
 await store.write([{s:'outbox',k:sent.id,v:{...original,files:[{attachment:{blob:{id:'fixture'}},ct:new Uint8Array([1]),uploaded:false}]}}]);
 e.uploadBlob=async()=>{entered.resolve();await release.promise;};
 const pending=e.post(await store.get('outbox',sent.id));await entered.promise;
 await e.messageControl('delete',{id:sent.id,dir:'out'});release.resolve();await pending;
 assert.equal((await store.get('outbox',sent.id)).delivery_cancelled,true);
 assert(!posts.includes(original.envelope),'upload race never hands original over');e.stop();
}
// Once final POST starts, Delete cannot prove cancellation. Its response
// and later pushed receipt still record what is proven, without a retry.
for(const lost of [false,true]) {
 const {e,store,posts}=await fixture(),entered=deferred(),release=deferred();
 const sent=await e.sendV1({queued:true,to:'bob/desk',kind:'task',body:'handover race',files:[]});
 const original=await store.get('outbox',sent.id),fetch=e.fetch;
 e.fetch=async(url,options)=>{if(options.body===original.envelope){posts.push(options.body);entered.resolve();await release.promise;if(lost)throw Error('response lost');return new Response(JSON.stringify({state:'custody'}));}return fetch(url,options);};
 const pending=e.post(original);await entered.promise;
 await e.messageControl('delete',{id:sent.id,dir:'out'});
 assert.match((await store.get('outbox',sent.id)).detail,/Cancellation cannot be confirmed/);
 release.resolve();await pending;
 assert.equal((await store.get('outbox',sent.id)).state,lost?'failed':'custody');
 e.connected=true;await e.flushOutbox();assert.equal(posts.filter(x=>x===original.envelope).length,1);
 await e.dispatch('receipt',JSON.stringify({id:sent.id,state:'delivered',seq:1}));
 assert.equal((await store.get('outbox',sent.id)).state,'delivered');
 for(const [state,seq] of [['quarantined',2],['expired',3],['quarantined',1]]) {
  await e.dispatch('receipt',JSON.stringify({id:sent.id,state,seq}));
  assert.equal((await store.get('outbox',sent.id)).state,'delivered','late receipts cannot downgrade proven delivery');
 }
 assert.equal(await store.get('kv','receipt-cursor'),1,'restored Hub replay resets receipt cursor');
 const restored=await e.sendV1({queued:true,to:'bob/desk',kind:'task',body:'restored stream',files:[]});
 await e.dispatch('receipt',JSON.stringify({id:restored.id,state:'delivered',seq:2}));
 assert.equal((await store.get('outbox',restored.id)).state,'delivered','subsequent restored-stream receipt is processed');
 assert.equal((await store.get('outbox',sent.id)).state,'delivered','restored replay preserves prior proven delivery');
 assert.equal(await store.get('kv','receipt-cursor'),2);e.stop();
}
// Device-thread deletion controls themselves recover through the normal
// queue. Neither a changed reader key nor missing capability is bypassed.
{
 const {e,store,posts}=await fixture();let offline=true;const fetch=e.fetch;
 e.fetch=async(...args)=>{if(offline)throw Error('offline');return fetch(...args);};
 const sent=await e.sendV1({queued:true,to:'bob/desk',kind:'question',body:'offline delete',files:[]});
 const ctlSupport=e.ctlSupport;e.ctlSupport=async()=>{throw new HubError(0,'','cannot reach your server');};
 await e.messageControl('delete',{id:sent.id,dir:'out'});
 const control=(await store.all('outbox')).find(r=>r.control);
 assert.equal(control.state,'queued');offline=false;e.connected=true;
 e.ctlSupport=async()=>[false,'peer_update: reader needs an update'];
 await e.flushOutbox();assert.equal((await store.get('outbox',control.id)).state,'waiting');assert.equal(posts.length,0);
 e.ctlSupport=ctlSupport;
 const pin=await store.get('pins','bob/desk');await store.write([{s:'pins',k:'bob/desk',v:{...pin,pending:{}}}]);
 await e.flushOutbox();assert.equal(posts.length,0,'changed reader key keeps deletion sealed for original key');
 await store.write([{s:'pins',k:'bob/desk',v:pin}]);
 await e.flushOutbox();assert.equal((await store.get('outbox',control.id)).state,'custody');
 assert.deepEqual(posts,[control.envelope]);await e.flushOutbox();assert.equal(posts.length,1);e.stop();
}
// Older queued rows have no proof of whether a previous POST was attempted.
// Retraction stops another attempt but does not invent a cancellation receipt.
{
 const {e,store,posts}=await fixture();
 const sent=await e.sendV1({queued:true,to:'bob/desk',kind:'question',body:'legacy pending',files:[]});
 const original=await store.get('outbox',sent.id);delete original.handover_started;
 await store.write([{s:'outbox',k:sent.id,v:original}]);
 await e.messageControl('delete',{id:sent.id,dir:'out'});
 assert.match((await store.get('outbox',sent.id)).detail,/Cancellation cannot be confirmed/);
 e.connected=true;await e.flushOutbox();assert(!posts.includes(original.envelope));
 await e.dispatch('receipt',JSON.stringify({id:sent.id,state:'quarantined',seq:1}));
 assert.equal((await store.get('outbox',sent.id)).state,'quarantined');e.stop();
}
console.log('queued retraction PASS: local question/task, restart, upload race, ambiguous POST/custody/receipt, exact-ID control reconnect');
// Person-conversation and group Delete save their signed controls while
// offline, retaining the existing recipient key and capability fences.
for (const group of [false,true]) {
 const {e,store,posts}=await fixture();
 const pin=await store.get('pins','bob/desk'),dev={address:'bob/desk',fingerprint:pin.fingerprint};
 e.me={person:wire.newID(),hash:'a'.repeat(64),devices:[{address:e.address,fingerprint:e.fp}]};
 const peer={person:wire.newID(),hash:'b'.repeat(64),devices:[dev]},c={id:'c'.repeat(64),kind:group?'group':'dm'};
 e.gate=async()=>({why:'',peer});e.ctlSupport=async()=>{throw new HubError(0,'','offline');};
 e.post=async()=>{};
 if(group){e.groupTurnEvidence=async()=>({packet:{}});e.dmMembers=async()=>new Map([[e.me.person,e.me],[peer.person,peer]]);e.groupControlTarget=async()=>[];e.groupRead=async(_checks,s,k)=>store.get(s,k);e.groupSupport=async()=>{throw Error('offline');};e.groupControlFence=async()=> 'verified-local-epoch';}
 const original={id:wire.newID(),lid:wire.newID(),conv:c.id,kind:'question',fp:e.fp,to:dev.address,state:'queued',handover_started:false,envelope:'exact-original'};
 await store.write([{s:'outbox',k:original.id,v:original}]);
 await e.sendConvControl(c,{id:original.lid,fingerprint:e.fp},wire.SubRetraction,'{}');
 const stopped=await store.get('outbox',original.id),control=(await store.all('outbox')).find(r=>r.control);
 assert.equal(stopped.delivery_cancelled,true);assert.equal(stopped.envelope,original.envelope);
 assert.equal(control.recipient_fp,dev.fingerprint);assert.equal(control.required_cap,group?wire.CapGroup:wire.CapControl);
 assert.equal(control.state,'queued');assert.equal(posts.length,0);e.stop();
}
