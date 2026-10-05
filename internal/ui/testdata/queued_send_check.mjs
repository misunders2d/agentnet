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
// A standalone skin loads its own module instance, without a host asset URL.
const {readFile} = await import('node:fs/promises');
const optimisticSource=await readFile(new URL('../static/optimistic.mjs',import.meta.url),'utf8');
const standalone=await import('data:text/javascript,'+encodeURIComponent(optimisticSource));
const newUI=standalone.pendingSends(bound,()=>newChanges++);oldUI.dispose();oldUI.fail(ongoing,'kept for Retry');
assert.equal(oldChanges,1);assert.equal(newChanges,1);assert.equal(newUI.merge('dm',[])[0]._failed,true);
const otherUI=standalone.pendingSends({workspace:{id:'two'},workspaces:{state:()=>({})}});
assert.equal(otherUI.merge('dm',[]).length,0,'pending previews stay in their own membership');newUI.dispose();
console.log('queued engine send PASS: durable return, FIFO, retry, no duplicate post, Close drain; optimistic merge/failure/retry PASS');

// Execute each legacy skin's actual captured-send functions: a second press
// during a new device topic's local save must target its pending first turn.
const {runInNewContext} = await import('node:vm');
for (const skin of ['classic','zoom']) {
  const src=await readFile(new URL(`../skins/${skin}/src/entry.mjs`,import.meta.url),'utf8');
  const begin=src.slice(src.indexOf('function startSend('),src.indexOf('function failSend('));
  const send=src.slice(src.indexOf('async function send(ev, retry)'),src.indexOf('function kindHint(',src.indexOf('async function send(ev, retry)')));
  const host={workspace:{id:skin}}, sends=pendingSends(host), body={value:'new topic seed'}, error={textContent:''}, submitted=[], gates=[], opened=[];
  const root='c'.repeat(32), state={sending:false,dm:null,data:{id:root,peer:'bob/desk',key:{pending:false},messages:[{id:root}]},draftKey:root,files:[],answering:null,mentions:[],overview:{}};
  const noop=()=>{};
  const scope={state,sends,sendID,alive:true,topicFresh:{[root]:true},currentHost:host,typingUI:null,
    $:id=>id==='body'?body:error,kindValue:()=> 'question',wsNow:()=>skin,boundElsewhere:()=>'',overLimit:()=>'',receiverCapture:()=>({}),
    prepareReplyReceiverSelection:async()=>null,preparedFiles:async()=>[],sentStaged:noop,grow:noop,setDMReply:noop,setAnswering:noop,renderPending:noop,keepDraft:noop,syncComposer:noop,announce:noop,loadThread:async()=>{},updated:noop,sentElsewhere:noop,
    openThread:async id=>opened.push(id),failSend:()=>{throw Error('unexpected captured-send failure');},
    api:async(path,draft)=>{assert.equal(path,'/api/send');submitted.push({...draft});const wait=deferred();gates.push(wait);await wait.promise;return {id:draft.id,state:'queued'};},
  };
  runInNewContext(begin+'\n'+send+'\nglobalThis.capturedSend=send;',scope);
  const firstRun=scope.capturedSend();
  for(let i=0;!submitted.length&&i<100;i++)await new Promise(r=>setTimeout(r,1));
  assert.equal(submitted.length,1,skin+' first save started');assert.equal(body.value,'',skin+' instant clear');
  body.value='rapid follow-up';const secondRun=scope.capturedSend();await new Promise(r=>setTimeout(r,10));
  assert.equal(body.value,'',skin+' second press captured');assert.equal(submitted.length,1,skin+' follow-up waits only for prior local save');
  gates[0].resolve();await firstRun;
  for(let i=0;submitted.length<2&&i<100;i++)await new Promise(r=>setTimeout(r,1));
  assert.equal(submitted.length,2);assert.equal(submitted[0].reply_to,'');assert.equal(submitted[1].reply_to,submitted[0].id,skin+' follows pending new topic');
  assert.deepEqual(opened,[submitted[0].id]);gates[1].resolve();await secondRun;
  const real=submitted.map(m=>({id:m.id,dir:'out',body:m.body}));assert.equal(sends.merge(submitted[0].id,real).length,2,skin+' topic previews merge once');assert.equal(sends.merge(root,[]).length,0,skin+' old topic has no stray preview');
}
console.log('classic/zoom captured device topic sends PASS: immediate clear, rapid follow-up parent, FIFO save, one bubble');
