import assert from 'node:assert/strict';
import {Engine, memoryStore, HubError} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const realSetTimeout = globalThis.setTimeout;
let deadlines = 0;
globalThis.setTimeout = (fn, ms, ...args) => {
  if (ms === 30_000) { deadlines++; ms = 20; }
  return realSetTimeout(fn, ms, ...args);
};
const bounded = p => Promise.race([p, new Promise((_, reject) => realSetTimeout(() => reject(Error('HTTP stall still blocks recovery')), 300))]);
const stalled = signal => new Promise((_, reject) => {
  const stop = () => reject(new DOMException('aborted', 'AbortError'));
  if (signal?.aborted) stop(); else signal?.addEventListener('abort', stop, {once:true});
});
const retryable = err => err instanceof HubError && err.status === 0;
// A group proof/context is downloaded while the receive stream admits its
// carrier. A suspended download must not hold that stream forever.
for (const phase of ['headers','body','bounded-body']) {
 const e = new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(_, {signal}) => {
  if (phase === 'headers') return stalled(signal);
  return {ok:true,arrayBuffer:()=>stalled(signal),body:{getReader:()=>({read:()=>stalled(signal),cancel:async()=>{}})}};
 }});
 e.keys=await wire.newKeys();e.address='alice/phone';
 await assert.rejects(bounded(e.getBytes('/fixture',phase==='bounded-body'?3:undefined)),retryable);
 e.stop();
}
// Bound stalled headers, successful response body, and error response body.
for (const phase of ['headers','body','error-body']) {
 const e = new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(_, {signal}) => {
  if (phase === 'headers') return stalled(signal);
  return {ok:phase === 'body',status:503,text:()=>stalled(signal),json:()=>stalled(signal)};
 }});
 await assert.rejects(bounded(e.call('GET','/fixture',undefined,{signed:false})),retryable);
 e.stop();
}
// Raw attachment chunks share the same transport deadline. Otherwise an
// upload holds the durable outbox pass across every stream reconnect.
for (const phase of ['headers','body','error-body']) {
 const e = new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(_, {signal}) => {
  if (phase === 'headers') return stalled(signal);
  return {ok:phase === 'body',status:503,text:()=>stalled(signal),json:()=>stalled(signal)};
 }});
 e.keys=await wire.newKeys();e.address='alice/phone';
 await assert.rejects(bounded(e.callBytes('PUT','/fixture',new Uint8Array([1]))),retryable);
 e.stop();
}
// Both caller cancellation and engine shutdown survive the per-request signal.
for (const bound of [undefined,3]) {
 let requested,start;const entered=new Promise(r=>start=r);
 const e=new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(_, {signal})=>{
  requested=signal;start();
  return {ok:true,arrayBuffer:()=>stalled(signal),body:{getReader:()=>({read:()=>stalled(signal),cancel:async()=>{}})}};
 }});
 e.keys=await wire.newKeys();e.address='alice/phone';
 const pending=e.getBytes('/fixture',bound);await entered;e.stop();assert(requested.aborted,'stop cancels the active download immediately, not at its later deadline');await assert.rejects(bounded(pending),retryable);
}
for (const size of [2,3,4]) {
 const e=new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async()=>new Response(new Uint8Array(size))});
 e.keys=await wire.newKeys();e.address='alice/phone';
 if(size===3)assert.equal((await e.getBytes('/fixture',3)).length,3);
 else await assert.rejects(e.getBytes('/fixture',3),size<3?/incomplete/:/exceeds signed ciphertext size/);
 e.stop();
}
for (const closing of [false,true]) {
 const e = new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(_, {signal})=>stalled(signal)}), caller=new AbortController();
 const request = e.call('GET','/fixture',undefined,{signed:false,signal:caller.signal});
 (closing ? e.sendAbort : caller).abort();
 await assert.rejects(bounded(request),retryable);e.stop();
}
// Existing unresolved proof and receipt retries must not stand in front of a
// newly queued user request on foreground reconnect.
{
 const store=memoryStore(),posts=[],e=new Engine({store,base:'https://isolated.invalid',fetch:async(url,{body})=>{
  if(url.endsWith('/v1/version'))return new Response(JSON.stringify({version:'fixture',features:[]}));
  if(url.endsWith('/v1/messages')){posts.push(body);return new Response(JSON.stringify({state:'custody'}));}
  throw Error('Unexpected request '+url);
 }});
 e.keys=await wire.newKeys();e.address='alice/phone';
 const recipient=await wire.publicEntry(await wire.newKeys(),'bob/host'),pin={fingerprint:'f'.repeat(64)};
 e.sendKey=async()=>pin;e.pubOf=async()=>recipient;await store.write([{s:'pins',k:'bob/host',v:pin}]);
 for(const method of ['publishPerson','recoverGroupIntents','retryApproved','syncRoots','syncReadMarks','syncTopicTitles','syncInvitations','discloseHumanAudience','runHistory','runServes','keepFiles','reconcileNotify'])e[method]=async()=>{};
 let release;const background=new Promise(r=>release=r);
 e.retryHeld=()=>background;e.flushReceipts=()=>background;
 const sent=await e.sendDirect({to:'bob/host',kind:'question',body:'fresh request during old proof recovery'});
 const original=await store.get('outbox',sent.id);e.connected=true;
 try {await bounded(e.onConnect());assert.deepEqual(posts,[original.envelope]);assert.equal((await store.get('outbox',sent.id)).state,'custody');}
 finally {release();e.stop();}
}
// A cancelled stream previously awaited its hung onConnect forever. The
// real onConnect must now finish; reconnect can flush the same durable row.
let streams=0, posts=[], releaseStream;
const store=memoryStore(), e=new Engine({store,base:'https://isolated.invalid',fetch:async(url,{signal,body})=>{
 if(url.includes('/v1/stream?')) {
  streams++;
  return new Response(new ReadableStream({start(c){if(streams===1)c.close();else releaseStream=()=>c.close();}}),{headers:{'Agentnet-Members':'1'}});
 }
 if(url.endsWith('/v1/version')) {
  if(streams===1)return stalled(signal);
  return new Response(JSON.stringify({version:'fixture',features:[]}));
 }
 if(url.endsWith('/v1/messages')){posts.push(body);return new Response(JSON.stringify({state:'custody'}));}
 throw Error('Unexpected request '+url);
}});
e.keys=await wire.newKeys();e.address='alice/phone';
const recipient=await wire.publicEntry(await wire.newKeys(),'bob/host'),pin={fingerprint:'f'.repeat(64)};
e.sendKey=async()=>pin;e.pubOf=async()=>recipient;
await store.write([{s:'pins',k:'bob/host',v:pin}]);
for(const method of ['publishPerson','flushReceipts','retryHeld','recoverGroupIntents','retryApproved','syncRoots','syncReadMarks','discloseHumanAudience','runHistory','runServes','keepFiles','reconcileNotify'])e[method]=async()=>{};
const sent=await e.sendDirect({to:'bob/host',kind:'question',body:'durable pending question'});
const original=await store.get('outbox',sent.id);assert.equal(original.state,'queued');
await bounded(e.streamOnce());assert.equal(e.connected,false);assert.equal(posts.length,0);
const resumed=e.streamOnce();
for(let i=0;(await store.get('outbox',sent.id)).state!=='custody'&&i<60;i++)await new Promise(r=>realSetTimeout(r,5));
assert.equal((await store.get('outbox',sent.id)).state,'custody');
assert.deepEqual(posts,[original.envelope],'reconnect sends original encrypted envelope once');
await e.flushOutbox();assert.equal(posts.length,1);releaseStream();await bounded(resumed);e.stop();
// Linked dispatch aborts the approval-only stream after verifying the roster.
// A fetch reader may neither reject nor finish a read after an abort (nor
// support cancel): the stream still ends at once, and is not read again.
{
 let reads=0,afterAbort=0,aborted=false;
 const linked=new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async(url,o)=>{o.signal.addEventListener('abort',()=>{aborted=true;});return {ok:true,headers:new Headers(),body:{getReader:()=>({read:async()=>{if(aborted)afterAbort++;return ++reads===1?{value:new TextEncoder().encode('event: linked\ndata: {}\n\n'),done:false}:new Promise(()=>{});}})}};}});
 linked.keys=await wire.newKeys();linked.address='alice/phone';linked.link={state:'pending'};
 linked.finishLink=async()=>linked.setLink('linked');
 await bounded(linked.streamOnce());
 assert.equal(linked.link.state,'linked');assert.equal(linked.connected,false);
 assert.ok(aborted&&afterAbort===0&&reads<=2,'verified linked transition ends its approval stream without reading it again ('+reads+' reads)');
 linked.stop();
}
assert(deadlines>=8,'bounded all exercised requests');
console.log('request timeout PASS: headers/body/error-body, caller/close cancellation, real onConnect recovery, durable original send once');
