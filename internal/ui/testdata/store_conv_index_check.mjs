// Authority reads and their write-time checks cost the rows of one
// conversation (t5-delivery): a conv index on inbox and outbox, added by a
// new database version over existing rows, and exact compare-and-set over
// that conversation's rows, absence included. Generated rows only.
const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 equal:(a,b,m)=>{if(a!==b)throw Error((m||'Values differ')+': '+a+' !== '+b);},
 deepEqual:(a,b,m)=>{if(JSON.stringify(a)!==JSON.stringify(b))throw Error((m||'Structures differ')+': '+JSON.stringify(a));},
 ok:(x,m)=>{if(!x)throw Error(m||'Expected truthy');},
 rejects:async(fn,re)=>{try{await fn();}catch(e){if(re.test(e.message))return;throw e;}throw Error('Expected rejection '+re);},
};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
const realIDB=typeof window!=='undefined';let checks=0;
const check=(x,m)=>{assert.ok(x,m);checks++;};
const convA='a'.repeat(64),convB='b'.repeat(64),id=n=>n.toString(16).padStart(32,'0');
const row=(n,conv,extra={})=>({id:id(n),lid:id(n),conv,at:1000+n,ts:1,kind:'message',body:'generated '+n,fp:'f'.repeat(64),...extra});
const inbox=[row(3,convA),row(1,convA),row(5,convB),row(2,convA,{sub:'event'}),row(4,convB),{id:id(6),v:1,at:7,kind:'message',body:'device thread'}];
const outbox=[row(9,convA,{to:'peer/desk',state:'custody'}),row(8,convB,{to:'peer/desk',state:'delivered'})];
const name='conv-index-'+Math.random().toString(16).slice(2);
let st;
if(realIDB) {
 // The shipped v5 schema with rows already in it: version 6 adds the index over them.
 const old=await new Promise((resolve,reject)=>{const q=indexedDB.open(name,5);q.onupgradeneeded=()=>{const d=q.result;for(const s of ['kv','pins','persons','convs','inbox','outbox','held','lids','receipts','files','erased'])d.createObjectStore(s);
  for(const s of ['inbox','outbox']){const o=q.transaction.objectStore(s);o.createIndex('history_pos','history_pos');o.createIndex('device_arrival','device_arrival');if(s==='inbox')o.createIndex('history_arrival','history_arrival');}};q.onsuccess=()=>resolve(q.result);q.onerror=()=>reject(q.error);});
 await new Promise((resolve,reject)=>{const tx=old.transaction(['inbox','outbox'],'readwrite');for(const r of inbox)tx.objectStore('inbox').put(r,r.id);for(const r of outbox)tx.objectStore('outbox').put(r,r.id);tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);});
 old.close();st=await openIDB(name);
 const v=await new Promise((resolve,reject)=>{const q=indexedDB.open(name);q.onsuccess=()=>{const d=q.result,info={version:d.version,indexes:['inbox','outbox'].map(s=>[...d.transaction(s).objectStore(s).indexNames].sort().join())};d.close();resolve(info);};q.onerror=()=>reject(q.error);});
 check(v.version===6&&v.indexes[0]==='conv,device_arrival,history_arrival,history_pos'&&v.indexes[1]==='conv,device_arrival,history_pos','existing v5 database gains the conv index without losing others: '+JSON.stringify(v));
} else {st=memoryStore();await st.write([...inbox.map(r=>({s:'inbox',k:r.id,v:r})),...outbox.map(r=>({s:'outbox',k:r.id,v:r}))]);}
assert.deepEqual((await st.byConv('inbox',convA)).map(r=>r.id),[id(1),id(2),id(3)],'one conversation, existing rows, key order');checks++;
assert.deepEqual((await st.byConv('outbox',convB)).map(r=>r.id),[id(8)]);checks++;
check((await st.byConv('inbox','c'.repeat(64))).length===0,'unknown conversation reads nothing');
const late=row(7,convA);await st.write([{s:'inbox',k:late.id,v:late}]);
check((await st.byConv('inbox',convA)).map(r=>r.id).join()===[id(1),id(2),id(3),id(7)].join(),'rows written after the upgrade are indexed');
// authorityRows reads only the conversation: no whole-store read.
const e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('no network');}});
const all=st.all;st.all=async s=>{if(s==='inbox'||s==='outbox')throw Error('whole-store read of '+s);return all(s);};
const fresh=async scope=>{const c=[];const rows=await e.authorityRows(scope,c);return {rows,c};};
try{
 const {rows}=await fresh({conv:convA});check(rows.length===5&&rows.every(r=>r.conv===convA),'authority rows of one conversation come from its index');
 const events=await fresh({conv:convA,sub:'event'});check(events.rows.length===1&&events.rows[0].id===id(2),'narrower scopes still filter exactly');
}finally{st.all=all;}
// The write-time check stays exact for the conversation and ignores others.
const conflict=/storage changed during verification/;
const guarded=async(change,why,conflicts=true)=>{
 const {c}=await fresh({conv:convA});await st.write(change);
 if(conflicts){await assert.rejects(()=>st.write([{s:'kv',k:'guarded',v:why}],c),conflict);check(await st.get('kv','guarded')!==why,why);}
 else{await st.write([{s:'kv',k:'guarded',v:why}],c);check(await st.get('kv','guarded')===why,why);}
};
await guarded([{s:'inbox',k:id(4),v:{...inbox[4],body:'other conversation changed'}}],'another conversation changing does not fail the write',false);
await guarded([{s:'outbox',k:id(8),v:{...outbox[1],state:'custody'}}],'another conversation outbox change does not fail it',false);
await guarded([{s:'inbox',k:id(1),v:{...inbox[1],body:'edited'}}],'a changed row of the conversation fails it');
await guarded([{s:'inbox',k:id(10),v:row(10,convA)}],'an added row of the conversation fails it (absence is guarded)');
await guarded([{s:'inbox',k:id(3)}],'a removed row of the conversation fails it');
await guarded([{s:'inbox',k:id(5),v:{...inbox[2],conv:convA}}],'a row moving into the conversation fails it');
await guarded([{s:'outbox',k:id(9),v:{...outbox[0],body:'edited sent'}}],'a changed sent row of the conversation fails it');
await guarded([{s:'outbox',k:id(9),v:{...outbox[0],body:'edited sent',state:'delivered'}}],'delivery progress of a sent row never fails it',false);
console.log('conv index PASS: '+checks+' checks ('+(realIDB?'IndexedDB upgrade':'memory')+')');
st.close();if(realIDB){await new Promise(r=>{const q=indexedDB.deleteDatabase(name);q.onsuccess=q.onerror=r;});window.convIndexResult={ok:true,checks,storage:'IndexedDB'};}
