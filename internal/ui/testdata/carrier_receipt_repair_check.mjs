import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';

export async function carrierReceiptRepair(realIDB=false) {
 const name='agentnet-carrier-repair-'+wire.newID(),store=realIDB?await openIDB(name):memoryStore(),keys=await wire.newKeys(),address='self/phone',pub=await wire.publicEntry(keys,address),fingerprint=await wire.fingerprint(pub);
 const id=n=>n.toString(16).padStart(32,'0'),calls=[],labels=[],check=(ok,label)=>{if(!ok)throw Error(label);labels.push(label);};
 const make=()=>new Engine({store,base:'https://fixture.invalid',fetch:async(url,options)=>{if(!new URL(url).pathname.endsWith('/ack'))throw Error('unexpected migration network');calls.push({id:new URL(url).pathname.split('/')[3],...JSON.parse(options.body)});return new Response(null,{status:204});}});
 try {
  await store.write([{s:'kv',k:'identity',v:{keys,address,fingerprint}},...Array.from({length:55},(_,i)=>({s:'kv',k:'group-carrier/'+id(i+1),v:true})),
   {s:'kv',k:'group-carrier/'+id(56),v:false},{s:'kv',k:'group-carrier/not-an-id',v:true},{s:'kv',k:'group-carrier/'+id(57),v:true},
   {s:'held',k:id(57),v:{id:id(57),reason:'key_changed',envelope:'retained ciphertext'}},{s:'held',k:id(58),v:{id:id(58),reason:'invalid',envelope:'unverified ciphertext'}},
   ...[1,57,58].map(n=>({s:'receipts',k:id(n),v:{id:id(n),state:'quarantined'}}))]);
  const beforeHeld=JSON.stringify(await store.all('held')),write=store.write.bind(store);let maxBatch=0;
  store.write=async(ops,checks)=>{check(!ops.some(o=>['inbox','outbox'].includes(o.s)),'upgrade only changes receipt metadata');maxBatch=Math.max(maxBatch,ops.filter(o=>o.s==='receipts'&&o.v!==undefined).length);return write(ops,checks);};
  let engine=make();await engine.load();
  check((await store.all('receipts')).filter(r=>r.state==='delivered').length===50,'startup repairs at most one bounded page');
  check(!(await store.get('kv','delivered-carrier-repair-v1')).done&&!calls.length,'offline startup keeps resumable cursor and performs no network');
  engine=make();await engine.load();
  check((await store.get('kv','delivered-carrier-repair-v1')).done,'restart finishes the next page');
  check((await store.all('receipts')).filter(r=>r.state==='delivered').length===55,'only valid previously admitted markers get success receipts');
  check((await store.get('receipts',id(57))).state==='quarantined'&&(await store.get('receipts',id(58))).state==='quarantined','pending identity and quarantine alone never imply admission');
  await engine.flushReceipts();
  check(calls.some(r=>r.id===id(1)&&r.state==='delivered'),'old quarantined receipt upgrades to delivered');
  check(maxBatch<=50&&JSON.stringify(await store.all('held'))===beforeHeld,'bounded repair retains every blocked ciphertext unchanged');
  engine=make();await engine.load();
  check((await store.all('receipts')).length===0,'completed migration never requeues receipts after restart');
  return {ok:true,storage:realIDB?'IndexedDB':'memory',checks:labels.length,labels};
 } finally {store.close();if(realIDB)await new Promise((resolve,reject)=>{const q=indexedDB.deleteDatabase(name);q.onsuccess=resolve;q.onerror=()=>reject(q.error);});}
}
