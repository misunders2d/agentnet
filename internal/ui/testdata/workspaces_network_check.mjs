import assert from 'node:assert/strict';
import {workspaceRealmFetch} from '../static/workspaces.mjs';
const base='https://home.example',realm='a'.repeat(32),pause=ms=>new Promise(r=>setTimeout(r,ms));
const bounded=p=>Promise.race([p,pause(200).then(()=>{throw Error('workspace check did not settle within test bound');})]);
let called,seen,target=0;
const entered=new Promise(r=>called=r),controller=new AbortController();
const aborted=workspaceRealmFetch(async(u,o)=>{if(u.endsWith('/v1/version')){seen=o.signal;called();await new Promise(()=>{});}target++;return new Response('');},base,{realm},()=>{},()=>{},{timeout:30});
const waiting=aborted(base+'/v1/stream',{signal:controller.signal});await entered;controller.abort();
await bounded(assert.rejects(waiting,e=>e.name==='AbortError'));
assert(seen?.aborted,'last aborted waiter cancels realm request');assert.equal(target,0);
// Cancellation also covers response bodies and refuses pre-aborted calls.
for(const path of ['/v1/stream','/v1/version']){
 let began,signal;const bodyEntered=new Promise(r=>began=r),parent=new AbortController();
 const bodyAbort=workspaceRealmFetch(async(u,o)=>{signal=o.signal;return {ok:true,clone:()=>({json:async()=>{began();await new Promise(()=>{});}})};},base,{realm},()=>assert.fail('aborted body saved realm'),()=>{},{timeout:100});
 const pending=bodyAbort(base+path,{signal:parent.signal});await bodyEntered;parent.abort();
 await bounded(assert.rejects(pending,e=>e.name==='AbortError'));assert(signal.aborted);
 let calls=0;const preAborted=workspaceRealmFetch(async()=>{calls++;},base,{realm},()=>{},()=>{});
 await assert.rejects(preAborted(base+path,{signal:parent.signal}),e=>e.name==='AbortError');assert.equal(calls,0);
}
// A caller's abort must not poison another concurrent caller's valid check.
let release,count=0;const gate=new Promise(r=>release=r),first=new AbortController();
const shared=workspaceRealmFetch(async(u,o)=>{if(u.endsWith('/v1/version')){count++;await gate;assert(!o.signal.aborted);return new Response(JSON.stringify({realm_id:realm}));}target++;return new Response('');},base,{realm},()=>{},()=>{},{timeout:100});
const p1=shared(base+'/v1/ping',{signal:first.signal}),p2=shared(base+'/v1/stream');first.abort();await assert.rejects(p1,e=>e.name==='AbortError');release();await p2;assert.equal(count,1);
for(const path of ['/v1/stream','/v1/version']){
 let signal;
 const headers=workspaceRealmFetch(async(u,o)=>{signal=o.signal;await new Promise(()=>{});},base,{realm},()=>{},()=>{},{timeout:15});
 await bounded(assert.rejects(headers(base+path),/timed out/));assert(signal.aborted);
 let bodySignal,saved=0;
 const body=workspaceRealmFetch(async(u,o)=>{bodySignal=o.signal;return {ok:true,clone:()=>({json:async()=>new Promise(()=>{})})};},base,{realm},()=>saved++,()=>{},{timeout:15});
 await bounded(assert.rejects(body(base+path),/timed out/));assert(bodySignal.aborted);assert.equal(saved,0);
}
let releaseLate,saves=0,remote=0;const late=new Promise(r=>releaseLate=r);
const stalled=workspaceRealmFetch(async(u,o)=>{if(u.endsWith('/v1/version')){await late;return new Response(JSON.stringify({realm_id:realm}));}remote++;return new Response('');},base,{},()=>saves++,()=>{},{timeout:15});
await bounded(assert.rejects(stalled(base+'/v1/stream'),/timed out/));releaseLate();await pause(5);assert.equal(saves,0);assert.equal(remote,0);
// The identity probe deadline never becomes a push-stream lifetime deadline.
let streamSignal,versions=0;
const streamParent=new AbortController();
const live=workspaceRealmFetch(async(u,o)=>{
 if(u.endsWith('/v1/version')){versions++;return new Response(JSON.stringify({realm_id:realm}));}
 streamSignal=o.signal;return new Response(new ReadableStream({start(){}}));
},base,{realm},()=>{},()=>{},{timeout:15});
const stream=await live(base+'/v1/stream',{signal:streamParent.signal});await pause(25);
assert.equal(streamSignal,streamParent.signal);assert(!streamSignal.aborted);await stream.body.cancel();
await live(base+'/v1/ping');assert.equal(versions,1,'ordinary calls reuse checked realm');
await live(base+'/v1/stream');assert.equal(versions,2,'each reconnect rechecks realm');
console.log('workspace realm caller abort, shared waiters, header/body deadlines, late-response no-send and unbounded push lifetime PASS');
