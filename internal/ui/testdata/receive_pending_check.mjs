const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 equal:(a,b,m)=>{if(a!==b)throw Error(m||'Values differ');},
 deepEqual:(a,b,m)=>{if(JSON.stringify(a)!==JSON.stringify(b))throw Error(m||'Structures differ');},
 ok:(x,m)=>{if(!x)throw Error(m||'Expected truthy');},
 rejects:async(fn,re)=>{try{await fn();}catch(e){if(re.test(e.message))return;throw e;}throw Error('Expected rejection '+re);}
};
import * as wire from '../static/wire.mjs';
const {Engine,memoryStore,openIDB}=await import((typeof process!=='undefined'&&process.env.AGENTNET_ENGINE_MODULE)||'../static/engine.mjs');
const engines=[];
const prefix='receive-pending/';
const json=(v,status=200)=>new Response(JSON.stringify(v),{status});
async function identity(address){const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,address);return{address,keys,pub,fp:await wire.fingerprint(pub)};}
const phone=await identity('alice/phone'),old=await identity('old/host'),fresh=await identity('current/host');
const pin=p=>({address:p.address,json:wire.marshalPublic(p.pub),fingerprint:p.fp,pending:null});
const envelope=(sender,id=wire.newID(),body='exact encrypted reply')=>wire.seal({id,from:sender.address,to:phone.address,ts:1,kind:'answer',body},sender.keys,phone.pub);
const pubs=new Map([old,fresh].map(p=>[p.address,p.pub]));
async function engine(store,lookup=async address=>json({public:JSON.parse(wire.marshalPublic(pubs.get(address)))})){
 const name='receive-pending-'+wire.newID();
 store ||= typeof window==='undefined'?memoryStore():await openIDB(name);
 const e=new Engine({store,base:'https://isolated.invalid',fetch:async(url)=>{
  const path=new URL(url).pathname;
  if(path.startsWith('/v1/agents/'))return lookup(path.slice(11));
  throw Error('Unexpected request '+path);
 }});
 Object.assign(e,phone);await store.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}}]);e.fixtureStoreName=name;e.flushReceipts=async()=>{};engines.push(e);return e;
}
const unavailable=()=>json({error:'temporarily unavailable'},503);
// Actual push framing: unknown sender discovery may remain stalled while a
// later cached-key answer (and duplicate of the retained carrier) is admitted.
{
 let entered,release,lookups=0,controller;
 const started=new Promise(resolve=>entered=resolve),gate=new Promise(resolve=>release=resolve);
 const e=await engine(undefined,async()=>{lookups++;entered();await gate;return unavailable();});
 const raw=await envelope(old),id=wire.parseEnvelope(raw).id,reply=await envelope(fresh),replyID=wire.parseEnvelope(reply).id;
 await e.store.write([{s:'pins',k:fresh.address,v:pin(fresh)}]);
 e.running=true;e.onConnect=async()=>{};const fetch=e.fetch;
 e.fetch=async(url,options)=>new URL(url).pathname==='/v1/stream'?new Response(new ReadableStream({start(c){controller=c;c.enqueue(new TextEncoder().encode('event: message\ndata: '+raw+'\n\n'));}})):fetch(url,options);
 const stream=e.streamOnce();await started;
 assert.equal((await e.store.get('kv',prefix+id)).envelope,raw,'unknown ciphertext durable before network discovery');
 controller.enqueue(new TextEncoder().encode('event: message\ndata: '+raw+'\n\nevent: message\ndata: '+reply+'\n\n'));controller.close();
 let timer;try{
  await Promise.race([stream,new Promise((_,reject)=>timer=setTimeout(()=>reject(Error('Cached-key reply blocked by old lookup')),2000))]);
  assert.equal((await e.store.get('inbox',replyID)).body,'exact encrypted reply','later reply readable while old directory request is stalled');
  assert.equal(await e.store.get('receipts',id),undefined,'durable deferral and duplicate push issue no old receipt');
  assert.equal(lookups,1,'active duplicate joins durable retention without starting another lookup');
 }finally{clearTimeout(timer);release();await stream;if(e.receiveRetryRun)await e.receiveRetryRun;}
 await e.close();
}
// Local durable failure cannot launch discovery or imply custody. Ordinary
// stop/close during the write preserves ciphertext without new background work.
for(const mode of ['disk-failure','stop','close']){
 let lookups=0,closing;
 const e=await engine(undefined,async()=>{lookups++;return unavailable();}),raw=await envelope(old),id=wire.parseEnvelope(raw).id,write=e.store.write;
 e.store.write=async(ops,checks)=>{
  if(ops.some(o=>o.s==='kv'&&o.k===prefix+id)){
   if(mode==='disk-failure')throw Error('fixture retention disk failure');
   if(mode==='stop')e.stop();else closing=e.close();
  }
  return write(ops,checks);
 };
 if(mode==='disk-failure')await assert.rejects(()=>e.dispatch('message',raw),/retention disk failure/);else await e.dispatch('message',raw);
 if(closing)await closing;
 assert.equal(lookups,0,'no discovery before durable success or after '+mode);
 assert.equal(await e.store.get('receipts',id),undefined,'no receipt on '+mode);
 assert.equal(await e.store.get('inbox',id),undefined);
 assert.equal(!!await e.store.get('kv',prefix+id),mode!=='disk-failure');
 assert.equal(e.receiving.size,0);e.stop();
}
// Structurally invalid/unbounded frames cannot enter the durable-first queue;
// they retain the existing ordinary malformed-admission behavior.
{
 const e=await engine(),raw=JSON.parse(await envelope(old));let ordinary=0;e.onMessage=async()=>{ordinary++;};
 for(const mutation of [{id:'bad'},{from:'bad'},{kind:'unknown'},{ct:wire.b64(new Uint8Array(wire.MaxCiphertext+1))}])await e.dispatch('message',JSON.stringify({...raw,...mutation}));
 assert.equal(ordinary,4);assert.equal((await e.store.prefix('kv',prefix)).length,0);e.stop();
 const unavailableEngine=await engine(undefined,unavailable);
 for(const mutation of [{id:'bad'},{from:'bad'},{kind:'unknown'},{ct:wire.b64(new Uint8Array(wire.MaxCiphertext+1))}])await assert.rejects(()=>unavailableEngine.dispatch('message',JSON.stringify({...raw,...mutation})),/unavailable/);
 assert.equal((await unavailableEngine.store.prefix('kv',prefix)).length,0,'retryable lookup error cannot retain an ineligible frame');unavailableEngine.stop();
}
// Recovery after the shutdown snapshot does no storage work. An existing
// admission may finish on ordinary stop, but starts no new cursor write.
{
 const closed=await engine();await closed.close();let reads=0;const get=closed.store.get;
 closed.store.get=async()=>{reads++;throw Error('read after close');};await closed.retryPendingReceives();closed.store.get=get;assert.equal(reads,0);
 const e=await engine(),raw=await envelope(fresh),id=wire.parseEnvelope(raw).id;
 await e.store.write([{s:'pins',k:fresh.address,v:pin(fresh)},{s:'kv',k:prefix+id,v:{id,address:e.address,fingerprint:e.fp,envelope:raw,at:e.now()}}]);
 const write=e.store.write;let cursors=0;
 e.store.write=async(ops,checks)=>{if(ops.some(o=>o.s==='inbox'))e.stop();if(ops.some(o=>o.s==='kv'&&o.k==='receive-retry-cursor'))cursors++;return write(ops,checks);};
 await e.retryPendingReceives();assert.equal(cursors,0,'stop generation prevents cursor write after owned admission');assert.equal((await e.store.get('inbox',id)).body,'exact encrypted reply');
}

// A real signed envelope needing unavailable sender metadata used to throw
// out of stream dispatch, reconnect, and fail again ahead of the next reply.
{
 let lookups=0;
 const e=await engine(undefined,async()=>{lookups++;return unavailable();});
 await e.store.write([{s:'pins',k:fresh.address,v:pin(fresh)}]);
 const raw=await envelope(old),id=wire.parseEnvelope(raw).id;
 await e.dispatch('message',raw);
 assert.equal(await e.store.get('inbox',id),undefined);
 assert.equal(await e.store.get('held',id),undefined);
 assert.equal(await e.store.get('receipts',id),undefined,'retention is not admission or receipt');
 const pending=await e.store.get('kv',prefix+id);
 assert.deepEqual(Object.keys(pending).sort(),['address','at','envelope','fingerprint','id']);
 assert.equal(pending.envelope,raw,'only exact encrypted carrier is retained');
 const reply=await envelope(fresh);await e.dispatch('message',reply);
 const replyID=wire.parseEnvelope(reply).id;
 assert.equal((await e.store.get('inbox',replyID)).body,'exact encrypted reply');
 assert.equal((await e.store.get('receipts',replyID)).state,'delivered');
 await e.dispatch('message',raw);assert.equal(lookups,1,'replayed blocked carrier does not repeat lookup before recovery wake');
 const collision=await envelope(old,id,'different encrypted content');
 await assert.rejects(()=>e.dispatch('message',collision),/identity differs/);
 assert.equal((await e.store.get('kv',prefix+id)).envelope,raw);
 e.stop();
 const reopen=typeof window==='undefined'?e.store:(e.store.close(),await openIDB(e.fixtureStoreName));
 const resumed=await engine(reopen);await resumed.retryPendingReceives();
 assert.equal(await resumed.store.get('kv',prefix+id),undefined);
 assert.equal((await resumed.store.get('inbox',id)).body,'exact encrypted reply');
 assert.equal((await resumed.store.get('receipts',id)).state,'delivered');
 await resumed.dispatch('message',raw);await resumed.retryPendingReceives();
 assert.equal((await resumed.store.all('inbox')).length,2,'restart/replay admits each exact message once');
 resumed.stop();
}
// Current keys are checked again on recovery. Retaining ciphertext never
// authorizes an old or changed identity and does not turn failure into delivery.
{
 const e=await engine(undefined,unavailable),raw=await envelope(old),id=wire.parseEnvelope(raw).id;
 await e.dispatch('message',raw);if(e.receiveRetryRun)await e.receiveRetryRun;
 await e.store.write([{s:'pins',k:old.address,v:{...pin(old),pending:{fingerprint:fresh.fp,json:wire.marshalPublic(fresh.pub)}}}]);
 await e.retryPendingReceives();
 assert.equal((await e.store.get('held',id)).reason,'key_changed');
 assert.equal((await e.store.get('receipts',id)).state,'quarantined');
 assert.equal(await e.store.get('inbox',id),undefined);
 assert.equal(await e.store.get('kv',prefix+id),undefined);
 e.stop();
}
// Admission and pending removal are one transaction. A failed local write
// leaves retryable ciphertext and no receipt; the next wake stores it once.
{
 const e=await engine(undefined,unavailable),raw=await envelope(old),id=wire.parseEnvelope(raw).id;
 await e.dispatch('message',raw);if(e.receiveRetryRun)await e.receiveRetryRun;await e.store.write([{s:'pins',k:old.address,v:pin(old)}]);
 const write=e.store.write;let fail=true;
 e.store.write=async(ops,checks)=>{if(fail&&ops.some(o=>o.s==='inbox')){fail=false;throw Error('fixture disk failure');}return write(ops,checks);};
 await assert.rejects(()=>e.retryPendingReceives(),/fixture disk failure/);
 assert.ok(await e.store.get('kv',prefix+id));assert.equal(await e.store.get('receipts',id),undefined);
 await e.retryPendingReceives();
 assert.equal(await e.store.get('kv',prefix+id),undefined);assert.equal((await e.store.all('inbox')).length,1);
 e.stop();
}
// Single-flight work is keyed by exact encrypted bytes, not merely by a
// caller-supplied ID. A conflicting concurrent envelope cannot join it.
{
 let release;
 const gate=new Promise(r=>{release=r;}),e=await engine(undefined,async()=>{await gate;return unavailable();});
 const id=wire.newID(),raw=await envelope(old,id),collision=await envelope(old,id,'other bytes');
 const work=e.dispatch('message',raw),duplicate=e.dispatch('message',raw);
 await assert.rejects(()=>e.dispatch('message',collision),/identity differs/);
 release();await Promise.all([work,duplicate]);if(e.receiveRetryRun)await e.receiveRetryRun;
 assert.equal((await e.store.get('kv',prefix+id)).envelope,raw);e.stop();
}
// A bounded durable cursor advances beyond unavailable old carriers, including
// malformed/foreign local entries, without consuming unrelated kv records.
{
 const e=await engine(undefined,unavailable),retry=e.retryPendingReceives.bind(e);e.retryPendingReceives=()=>Promise.resolve();
 for(let i=1;i<=17;i++){
  const sender=i===17?fresh:old,id=i.toString(16).padStart(32,'0');
  await e.dispatch('message',await envelope(sender,id));
 }
 e.retryPendingReceives=retry;
 await e.store.write([{s:'pins',k:fresh.address,v:pin(fresh)},{s:'kv',k:'zzz-unrelated',v:{envelope:'not a carrier',id:'unrelated'}}]);
 await e.retryPendingReceives();
 assert.equal((await e.store.all('inbox')).length,0,'first pass is bounded to 16');
 await e.retryPendingReceives();
 assert.equal((await e.store.all('inbox')).length,1,'next page progresses past persistent failures');
 assert.equal(await e.store.get('kv','receive-retry-cursor'),undefined);
 assert.equal((await e.store.get('kv','zzz-unrelated')).id,'unrelated');
 e.stop();
}
// A single stream chunk can contain several messages. Managed shutdown drains
// the admitted first message and must not begin the rest after its drain snapshot.
{
 const e=await engine(),first=await envelope(fresh),second=await envelope(fresh),secondID=wire.parseEnvelope(second).id;
 e.running=true;e.onConnect=async()=>{};
 e.fetch=async()=>new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode(`event: message\ndata: ${first}\n\nevent: message\ndata: ${second}\n\n`));c.close();}}));
 await e.store.write([{s:'pins',k:fresh.address,v:pin(fresh)}]);
 let closing,closed=false,lateWrites=0;const write=e.store.write;
 e.store.write=async(ops,checks)=>{
  if(!closing&&ops.some(o=>o.s==='inbox'))closing=e.close().then(()=>{closed=true;});
  if(closed)lateWrites++;
  return write(ops,checks);
 };
 await e.streamOnce();await closing;
 assert.ok(closed);assert.equal(lateWrites,0,'no writes start after shutdown drain');
 assert.equal(await e.store.get('inbox',secondID),undefined,'remaining chunk is left unacknowledged for reconnect');
 await e.dispatch('message',second);assert.equal(await e.store.get('receipts',secondID),undefined,'closed engine starts no new admission');
}
console.log('receive pending PASS: signed admission, newer reply progress, ciphertext-only recovery, restart/dedup, changed keys, atomic failure, bounded cursor');

for(const e of engines)e.store.close();
if(typeof window!=='undefined')window.receivePendingResult={ok:true,storage:'IndexedDB'};
