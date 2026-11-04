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
// A waiting legacy history copy, and a newly queued clear copy without
// send_order, each keep their own gate without starving a readable turn.
for (const sub of ['history', wire.SubClear]) {
  const store=memoryStore(), engine=new Engine({store,base:'https://isolated.invalid'}), calls=[];
  const conv='c'.repeat(64), aux={id:'a'.repeat(32),conv,to:'bob/desk',sub,aside:true,state:sub==='history'?'queued':'waiting',at:1};
  if(sub===wire.SubClear)Object.assign(aux,{control:true,required_cap:wire.CapConvClear});
  const turn={id:'b'.repeat(32),conv,to:aux.to,kind:'message',body:'later readable turn',state:'queued',at:1,send_order:2};
  engine.connected=true;engine.gate=async()=>({why:'',pin:{}});
  let clearChecks=0;engine.ctlSupport=async()=>{clearChecks++;return [false,'peer_update'];};
  engine.postStored=async rec=>{calls.push(rec.id);await store.write([{s:'outbox',k:rec.id,v:{...rec,state:rec.id===aux.id?'waiting':'custody'}}]);};
  await store.write([{s:'convs',k:conv,v:{id:conv}},...[aux,turn].map(v=>({s:'outbox',k:v.id,v}))]);
  assert.equal(aux.send_order,undefined,sub+' auxiliary has no new-send order');
  await engine.flushOutbox();
  assert.equal((await store.get('outbox',aux.id)).state,'waiting',sub+' delivery gate stays closed');
  assert.equal((await store.get('outbox',turn.id)).state,'custody',sub+' does not starve later readable turn');
  assert.deepEqual(calls,sub==='history'?[turn.id,aux.id]:[turn.id]);
  if(sub===wire.SubClear)assert.equal(clearChecks,1,'clear capability gate still checked');
  engine.stop();
}
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

// A message kept by the host: its row keeps its place, Retry and Discard show at
// once and undo if refused, and a local preview yields to the host's row.
{
  const called=[]; let refuse=null;
  const host={api:async(path,body)=>{called.push([path,body.id]);if(refuse)throw refuse;return {};}};
  let notices=0; const sends=pendingSends(host,()=>{notices++;});
  const at=s=>new Date(Date.parse('2026-11-04T12:00:00Z')+s*1000).toISOString();
  const inbound={id:'i',dir:'in',at:at(180),body:'later'};
  const kept=(state,rev,extra={})=>({id:'q',lid:'q',dir:'out',at:at(0),body:'kept',state:'queued',detail:'Waiting to send.',queued_send:{state,rev,diagnostic:'Get https://hub/chain?after=1: context deadline exceeded'},...extra});
  let shown=sends.merge('c',[kept('pending',2),inbound]);
  assert.equal(shown[0].id,'q');assert.equal(shown[1].id,'i');
  assert.equal(shown[0]._local,true);assert.equal(shown[0].state_text,'Waiting to send.');
  assert.equal(shown[0]._failed,false);assert.ok(!shown[0].state_text.includes('deadline'));assert.ok(shown[0]._diagnostic.includes('deadline'));
  shown=sends.merge('c',[kept('needs_review',3,{detail:'This person is frozen.'}),inbound]);
  assert.equal(shown[0]._failed,true);assert.equal(shown[0].state_text,'This person is frozen.');
  const again=sends.merge('c',shown);assert.equal(again[0].id,'q','a decorated row survives a repeated merge');
  let before=notices; const retried=shown[0]._retry();
  assert.equal(notices,before+1,'retry shows before the host answers');
  assert.equal(sends.merge('c',[kept('needs_review',3,{detail:'x'})])[0].state_text,'Sending…');
  await retried;assert.deepEqual(called.at(-1),['/api/schedule/retry_send','q']);
  assert.equal(sends.merge('c',[kept('needs_review',4,{detail:'x'})])[0]._failed,true,'a new revision is the host speaking again');
  const row=sends.merge('c',[kept('needs_review',4,{detail:'x'})])[0];
  refuse=Object.assign(Error('this message was already sent'),{status:409});
  const refused=row._discard();
  assert.equal(sends.merge('c',[kept('needs_review',4,{detail:'x'})]).length,0,'discard hides at once');
  await assert.rejects(refused,/already sent/);
  assert.equal(sends.merge('c',[kept('needs_review',4,{detail:'x'})]).length,1,'a refused discard restores the row');
  refuse=null;await sends.merge('c',[kept('needs_review',4,{detail:'x'})])[0]._discard();
  assert.equal(sends.merge('c',[kept('needs_review',4,{detail:'x'})]).length,0);
  assert.equal(sends.merge('c',[]).length,0);
  assert.equal(sends.merge('c',[kept('needs_review',4,{detail:'x'})]).length,1,'the host decides once the action is settled');
  // A local preview takes its place by time, keeps it failed, and yields by id.
  const id=sends.begin('d',{id:'l',lid:'l',dir:'out',at:at(10),body:'local'});
  const older={id:'o',dir:'in',at:at(5)},newer={id:'n',dir:'in',at:at(20)};
  assert.deepEqual(sends.merge('d',[older,newer]).map(m=>m.id),['o','l','n']);
  sends.fail(id,'no answer',true);
  const failed=sends.merge('d',[older,newer]);
  assert.deepEqual(failed.map(m=>m.id),['o','l','n']);assert.equal(failed[1].state_text,'Not confirmed: no answer');
  failed[1]._discard();assert.deepEqual(sends.merge('d',[older,newer]).map(m=>m.id),['o','n']);
  sends.begin('d',{id:'r',lid:'r',dir:'out',at:at(10),body:'yields'});
  assert.deepEqual(sends.merge('d',[older,{id:'copy',lid:'r',dir:'out',at:at(10)},newer]).map(m=>m.id),['o','copy','n']);
  assert.equal(sends.has('r'),false);
}
