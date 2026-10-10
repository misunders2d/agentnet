// The browser receive path keeps going (t5-delivery): one envelope whose
// admission fails unexpectedly is kept aside, unacknowledged, while the
// rest of the stream is stored; a heartbeat behind a long catch-up is
// answered while the page makes progress; a page shown again reconnects a
// down or silent stream at once. Isolated relay fixtures, generated keys.
const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 equal:(a,b,m)=>{if(a!==b)throw Error((m||'Values differ')+': '+a+' !== '+b);},
 deepEqual:(a,b,m)=>{if(JSON.stringify(a)!==JSON.stringify(b))throw Error((m||'Structures differ')+': '+JSON.stringify(a));},
 ok:(x,m)=>{if(!x)throw Error(m||'Expected truthy');},
};
import * as wire from '../static/wire.mjs';
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
const enc=new TextEncoder(),wait=ms=>new Promise(r=>setTimeout(r,ms));
const until=async(f,ms,why)=>{const end=Date.now()+ms;while(!await f()){if(Date.now()>end)throw Error('Timed out: '+why);await wait(5);}};
const stores=[];let checks=0;
const check=(x,m)=>{assert.ok(x,m);checks++;};
async function ident(address){const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,address);return {address,keys,pub,fp:await wire.fingerprint(pub)};}
const phone=await ident('fixture/phone'),host=await ident('fixture/host');
async function open(name){const st=typeof window==='undefined'?(stores.find(s=>s.name===name)?.st||memoryStore()):await openIDB(name);stores.push({name,st});return st;}
async function engine(name,fetch){
 const st=await open(name),e=new Engine({store:st,base:'https://isolated.invalid',fetch});
 Object.assign(e,{keys:phone.keys,address:phone.address,pub:phone.pub,fp:phone.fp});e.onConnect=async()=>{};
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'pins',k:host.address,v:{address:host.address,json:wire.marshalPublic(host.pub),fingerprint:host.fp,pending:null}}]);
 return e;
}
const seal=async(kind,body,id=wire.newID())=>{const raw=await wire.seal({id,from:host.address,to:phone.address,ts:1,kind,body},host.keys,phone.pub);return [wire.parseEnvelope(raw).id,raw];};
// relay keeps what is not acknowledged and pushes all of it, in order, on
// every connection (hub handleStream); a receipt removes it (setDisposition).
function relay(){
 const r={custody:new Map(),connects:0,acked:[],pings:0};
 r.fetch=async(url,o={})=>{
  const p=new URL(url).pathname;
  if(p==='/v1/stream'){r.connects++;const frames=[...r.custody.values()].map(f=>'event: message\ndata: '+f+'\n\n').join('');
   return new Response(new ReadableStream({start(c){if(frames)c.enqueue(enc.encode(frames));o.signal?.addEventListener('abort',()=>{try{c.error(new DOMException('Aborted','AbortError'));}catch{}});}}));}
  const m=p.match(/^\/v1\/messages\/([0-9a-f]{32})\/ack$/);if(m){r.custody.delete(m[1]);r.acked.push(m[1]);return new Response(JSON.stringify({id:m[1],state:JSON.parse(o.body).state}));}
  if(p==='/v1/stream/ack'){r.pings++;return new Response(null,{status:204});}
  return new Response('{"error":"isolated"}',{status:503});
 };
 return r;
}
// poisoned makes admission of one envelope fail unexpectedly (a bug, not a hold).
const poisoned=(e,id)=>{let calls=0;const inner=e.admitInner.bind(e);e.admitInner=async(data,env,admission)=>{if(env.id===id){calls++;throw new TypeError('injected unexpected admission failure');}return inner(data,env,admission);};return ()=>calls;};

// 1. Head of line: an unexpected admission failure no longer ends the
// stream (the relay would push it first again on every connection). Later
// envelopes are stored and acknowledged; it alone stays aside, exact and
// unacknowledged, with only its failure's kind recorded.
const name='receive-resilience-'+wire.newID(),r=relay();let kept;
{
 const e=await engine(name,r.fetch),[ok,okRaw]=await seal('message','older ok'),[bad,badRaw]=await seal('message','older poison'),[ans,ansRaw]=await seal('answer','the answer');
 for(const [id,raw] of [[ok,okRaw],[bad,badRaw],[ans,ansRaw]])r.custody.set(id,raw);
 const calls=poisoned(e,bad);kept=bad;e.running=true;const loop=e.loop();
 await until(()=>r.acked.includes(ans),5000,'answer behind a failing envelope stored and acknowledged');
 check(r.acked.includes(ok)&&!r.acked.includes(bad)&&r.connects===1,'one connection: the failure did not end the stream ('+r.connects+' connects)');
 check((await e.store.get('inbox',ans)).body==='the answer'&&!await e.store.get('inbox',bad)&&!await e.store.get('held',bad)&&!await e.store.get('receipts',bad),'failed envelope is neither shown, held, nor acknowledged');
 check((await e.store.get('kv','receive-pending/'+bad)).envelope===badRaw,'its exact ciphertext is kept');
 const failure=await e.store.get('kv','receive-error/'+bad);
 assert.deepEqual(Object.keys(failure).sort(),['at','attempts','error','id']);check(failure.error==='TypeError'&&failure.attempts===1,'only the failure kind is recorded, never its message');
 const status=await e.receiveStatus();check(status.failed===1&&status.waiting===0&&status.error==='TypeError'&&status.attempts===1,'overview status counts it: '+JSON.stringify(status));
 // The relay pushes it again on the next connection; it is not admitted again there.
 e.abort.abort();await until(()=>r.connects===2,3000,'reconnect');await wait(50);
 check(calls()===1&&!r.acked.includes(bad),'a redelivered set-aside envelope is skipped by the stream');
 // 2. Recovery retries it on the existing wakes, at most three times a page load.
 for(let i=0;i<4;i++)await e.retryPendingReceives();
 check(calls()===3&&(await e.store.get('kv','receive-error/'+bad)).attempts===3,'bounded retries this page load: '+calls());
 e.stop();await loop;
 if(typeof window!=='undefined')e.store.close();
}
// A new page load (an update that fixes the cause) admits it once, and the
// set-aside records go with the receipt in one write.
{
 const e=await engine(name,r.fetch);await e.retryPendingReceives();
 const bad=kept;
 check(!!await e.store.get('inbox',bad),'next page load admits the kept envelope');
 await until(()=>r.acked.includes(bad),2000,'and acknowledges it delivered');
 check(!await e.store.get('kv','receive-pending/'+bad)&&!await e.store.get('kv','receive-error/'+bad)&&await e.receiveStatus()===undefined,'its set-aside records are removed with admission');
 e.stop();if(typeof window!=='undefined')e.store.close();
}
// 3. A carrier whose retry itself fails (its failure cannot even be
// recorded) stays as it was; the pass goes on to the next one.
{
 const e=await engine('receive-resilience-'+wire.newID(),relay().fetch),[bad,badRaw]=await seal('message','first fails',('0'.repeat(31))+'1'),[good,goodRaw]=await seal('message','second fine',('0'.repeat(31))+'2');
 const at=e.now();await e.store.write([bad,good].map((id,i)=>({s:'kv',k:'receive-pending/'+id,v:{id,address:phone.address,fingerprint:phone.fp,envelope:[badRaw,goodRaw][i],at}})));
 poisoned(e,bad);const write=e.store.write;e.store.write=async(ops,c)=>{if(ops.some(o=>o.k==='receive-error/'+bad))throw Error('fixture disk failure');return write(ops,c);};
 await e.retryPendingReceives();
 check(!!await e.store.get('inbox',good)&&!await e.store.get('kv','receive-pending/'+good),'the pass continues past a carrier whose retry throws');
 check((await e.store.get('kv','receive-pending/'+bad)).envelope===badRaw&&!await e.store.get('receipts',bad),'the failing one stays kept and unacknowledged');
 e.stop();
}
// 4. A heartbeat behind a long catch-up is answered as it is read while
// the page makes progress; a page stuck on one event still answers it only
// in order (so the relay still closes a stuck page's stream).
for(const stuck of [false,true]){
 const r=relay(),e=await engine('receive-resilience-'+wire.newID(),r.fetch),[slow,slowRaw]=await seal('message','slow to verify'),[next,nextRaw]=await seal('answer','after it');
 let controller,release,entered;const gate=new Promise(x=>release=x),started=new Promise(x=>entered=x);
 e.fetch=async(url,o)=>new URL(url).pathname==='/v1/stream'?new Response(new ReadableStream({start(c){controller=c;c.enqueue(enc.encode('event: message\ndata: '+slowRaw+'\n\nevent: message\ndata: '+nextRaw+'\n\n'));}})):r.fetch(url,o);
 const onMessage=e.onMessage.bind(e);e.onMessage=async(data,admission)=>{if(wire.parseEnvelope(data).id===slow){entered();await gate;}return onMessage(data,admission);};
 const now=Date.now;let skew=0;Date.now=()=>now()+skew;
 try{
  const run=e.streamOnce();await started;if(stuck)skew=90_000+1000;
  controller.enqueue(enc.encode('event: ping\ndata: {"conn":"busy"}\n\n'));
  if(stuck){await wait(100);check(r.pings===0,'a page stuck beyond a heartbeat does not answer early');}
  else{await until(()=>r.pings===1,1000,'ping answered while an earlier envelope is still being verified');check(!await e.store.get('inbox',slow)&&!await e.store.get('inbox',next),'answered before the earlier envelopes are stored');}
  release();await until(()=>r.pings===1,2000,'ping answered in order after the stuck event');
  controller.close();await run;
  check(!!await e.store.get('inbox',slow)&&!!await e.store.get('inbox',next)&&r.pings===1,'events still dispatched in order, ping answered once');
 }finally{Date.now=now;release();}
 e.stop();
}
// 5. Reading ahead is bounded: a page that is behind stops reading (and
// with it the relay's writes) instead of holding a backlog in memory.
{
 const r=relay(),e=await engine('receive-resilience-'+wire.newID(),r.fetch);let pulls=0,release,entered;const gate=new Promise(x=>release=x),started=new Promise(x=>entered=x);
 const frames=['event: hold\ndata: first\n\n',...Array.from({length:12},()=>'event: filler\ndata: '+'y'.repeat(1<<20)+'\n\n'),'event: ping\ndata: {"conn":"bound"}\n\n'];
 e.fetch=async(url,o)=>new URL(url).pathname==='/v1/stream'?new Response(new ReadableStream({pull(c){if(pulls<frames.length)c.enqueue(enc.encode(frames[pulls++]));else c.close();}},{highWaterMark:0})):r.fetch(url,o);
 const dispatch=e.dispatch.bind(e);e.dispatch=async(event,data,acked)=>{if(event==='hold'){entered();await gate;}return dispatch(event,data,acked);};
 const run=e.streamOnce();await started;await wait(150);
 check(pulls>=9&&pulls<=11&&r.pings===0,'reader stops about 8 MiB ahead of the event being admitted: '+pulls+' reads');
 release();await run;await until(()=>r.pings>0,1000,'heartbeat answered after catch-up');
 check(pulls===frames.length&&r.pings===1,'the rest is read and the heartbeat answered once the page catches up');
 e.stop();
}
// 6. A page shown again (or back online) replaces a stream that is down or
// silent beyond a missed heartbeat at once, from the first backoff step;
// a live stream is kept, and visibility churn cannot reconnect in a storm.
{
 let streams=0,mode='fail';const e=await engine('receive-resilience-'+wire.newID(),async(url,o={})=>{
  if(new URL(url).pathname!=='/v1/stream')return new Response('{"error":"isolated"}',{status:503});
  streams++;if(mode==='fail')return new Response('{"error":"down"}',{status:503});
  if(mode==='hang')return new Promise((_,reject)=>o.signal?.addEventListener('abort',()=>reject(new DOMException('Aborted','AbortError'))));
  return new Response(new ReadableStream({start(c){o.signal?.addEventListener('abort',()=>{try{c.error(new DOMException('Aborted','AbortError'));}catch{}});}}));
 });
 e.running=true;const loop=e.loop();
 await until(()=>streams===1,1000,'first attempt');await wait(20);
 e.resume();await until(()=>streams===2,300,'shown again: the waiting reconnect starts at once');
 e.resume();await wait(150);check(streams===2,'a second resume within the gap starts nothing');
 mode='open';e.online();await until(()=>e.connected&&streams===3,300,'back online: reconnects at once, gap or not');
 e.resumedAt=0;e.resume();await wait(100);check(streams===3&&e.connected,'a live stream is kept');
 e.streamHeard=Date.now()-(90_000+15_000+1);e.resumedAt=0;e.resume();
 await until(()=>streams===4&&e.connected,500,'a silent stream (missed heartbeat) is replaced at once');
 mode='hang';e.abort.abort();await until(()=>streams===5&&!e.connected,2000,'a connection attempt that hangs');
 mode='open';e.resumedAt=0;e.resume();await wait(100);check(streams===5,'a young attempt is let finish');
 e.connecting=Date.now()-10_001;e.resumedAt=0;e.resume();
 await until(()=>streams===6&&e.connected,500,'an attempt left hanging while the page slept is replaced at once');
 e.stop();await loop;
}
console.log('receive resilience PASS: '+checks+' checks');
for(const {st} of stores)try{st.close();}catch{}
if(typeof window!=='undefined')window.receiveResilienceResult={ok:true,checks,storage:'IndexedDB'};
