import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
import { pendingSends, sendID } from '../static/optimistic.mjs';
const deferred = () => { let resolve; const promise = new Promise(r => resolve=r); return {promise,resolve}; };
const bounded = p => Promise.race([p,new Promise((_,reject)=>setTimeout(()=>reject(Error('send waited for posting')),1000))]);
const store=memoryStore(), e=new Engine({store,base:'https://isolated.invalid'});
e.keys=await wire.newKeys(); e.address='alice/laptop';
const recipient=await wire.publicEntry(await wire.newKeys(),'bob/desk');
const pin={fingerprint:'f'.repeat(64)};
e.sendKey=async()=>pin; e.pubOf=async()=>recipient;
e.connected=true; await store.write([{s:'pins',k:'bob/desk',v:pin}]);
const gate=deferred(), entered=deferred(), calls=[];
e.postStored=async rec=>{ calls.push(rec.id);entered.resolve();await gate.promise;await store.write([{s:'outbox',k:rec.id,v:{...rec,state:'custody'}}]); };
const first=await bounded(e.sendV1({id:'f'.repeat(32),queued:true,to:'bob/desk',kind:'message',body:'first',files:[]}));
assert.equal(first.state,'queued');await entered.promise;
const second=await bounded(e.sendV1({id:'1'.repeat(32),queued:true,to:'bob/desk',kind:'message',body:'second',files:[]}));
assert.equal(second.state,'queued');assert.deepEqual(calls,[first.id]);
await assert.rejects(e.sendV1({id:first.id,queued:true,to:'bob/desk',kind:'message',body:'reused',files:[]}),/already exists/);
const pass=e.outboxPass;gate.resolve();await pass;assert.deepEqual(calls,[first.id,second.id]);
await Promise.all([e.flushOutbox(),e.flushOutbox()]);assert.equal(calls.length,2);
// A failed attempt remains in the same durable row; a later pass sends once.
let attempts=0;e.postStored=async rec=>{attempts++;await store.write([{s:'outbox',k:rec.id,v:{...rec,state:attempts===1?'queued':'custody'}}]);};
const failed=await e.sendV1({queued:true,to:'bob/desk',kind:'message',body:'retry',files:[]});await e.outboxPass;
assert.equal((await store.get('outbox',failed.id)).state,'queued');await e.flushOutbox();await e.flushOutbox();assert.equal(attempts,2);
// Close waits for the current post while leaving restart recovery in the outbox.
const closing=deferred(), began=deferred();e.postStored=async rec=>{began.resolve();await closing.promise;await store.write([{s:'outbox',k:rec.id,v:{...rec,state:'custody'}}]);};
await e.sendV1({queued:true,to:'bob/desk',kind:'message',body:'close',files:[]});await began.promise;
let closed=false;const close=e.close().then(()=>closed=true);await new Promise(r=>setTimeout(r,30));assert.equal(closed,false);closing.resolve();await close;
// Workspace close also waits for a UI send that has not reached its save yet.
const preparing=new Engine({store:memoryStore(),base:'https://isolated.invalid'}), prepared=deferred();
preparing.sendDM=async()=>{await prepared.promise;await preparing.store.write([{s:'outbox',k:'kept',v:{id:'kept',state:'queued'}}]);return {id:'kept',state:'queued'};};
const request=preparing.api('/api/dm/send',{body:'preparing'});
let preparedClosed=false;const preparationClose=preparing.close().then(()=>preparedClosed=true);
await new Promise(r=>setTimeout(r,30));assert.equal(preparedClosed,false);
await assert.rejects(preparing.api('/api/dm/send',{body:'too late'}),/closing/);
prepared.resolve();await request;await preparationClose;assert.equal((await preparing.store.get('outbox','kept')).state,'queued');
// UI correlation merges a pushed durable copy before the send response arrives.
const host={workspace:{id:'one'}}, ui=pendingSends(host),id=sendID();let retried=false;
ui.begin('dm',{id,lid:id,dir:'out',body:'pending'},()=>retried=true);assert.equal(ui.merge('dm',[]).length,1);
assert.equal(ui.merge('dm',[{id:'2'.repeat(32),lid:id,dir:'out',body:'pending'}]).length,1);assert.equal(ui.has(id),false);
const bad=sendID();ui.begin('dm',{id:bad,lid:bad,dir:'out',body:'bad'},()=>retried=true);ui.fail(bad,'refused');const preview=ui.merge('dm',[])[0];assert.equal(preview._failed,true);preview._retry();assert.equal(retried,true);assert.equal(ui.merge('elsewhere',[]).length,0);
// Pending results notify the replacement renderer in this membership only.
const scope={}, bound={workspace:{id:'one'},workspaces:{state:()=>scope}};
let oldChanges=0,newChanges=0;
const oldUI=pendingSends(bound,()=>oldChanges++), ongoing=sendID();oldUI.begin('dm',{id:ongoing,dir:'out',body:'switch'});
const newUI=pendingSends(bound,()=>newChanges++);oldUI.dispose();oldUI.fail(ongoing,'kept for Retry');
assert.equal(oldChanges,1);assert.equal(newChanges,1);assert.equal(newUI.merge('dm',[])[0]._failed,true);newUI.dispose();
console.log('queued engine send PASS: durable return, FIFO, retry, no duplicate post, Close drain; optimistic merge/failure/retry PASS');
