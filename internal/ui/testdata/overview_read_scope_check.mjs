// Rendering local chats must not materialize unrelated encrypted recovery data.
import * as wire from '../static/wire.mjs';
const {Engine,memoryStore,openIDB}=await import((typeof process!=='undefined'&&process.env.AGENTNET_ENGINE_MODULE)||'../static/engine.mjs');
const check=(value,why)=>{if(!value)throw Error(why);};
const name='overview-scope-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name);
const e=new Engine({store,base:'https://synthetic.invalid',fetch:async()=>{throw Error('Local overview must not require network');}});
e.keys=await wire.newKeys();e.address='owner/phone';e.pub=await wire.publicEntry(e.keys,e.address);e.fp=await wire.fingerprint(e.pub);
const roster=await wire.newRoster(e.keys,e.address,'Owner');e.me=await e.personRecord([roster],'self',null);
await store.write([{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);
// Read-side fixtures represent records already admitted by their separate
// signed model/notice tests. No grant or admission behavior is tested here.
const report={v:1,revision:1,at:1,agent_id:'',executor:'codex',model:'reported model'};
await store.write([{s:'kv',k:e.modelReportKey(e.fp,''),v:{model_report:true,host:e.address,host_key:e.fp,revision:1,report}}]);
const notice={seq:1,id:wire.newID(),person:e.me.person,device:e.address,by:e.address,admin:true,at:1};
await e.onDeviceAdminNotice(JSON.stringify(notice));
const before=await e.overview(),reports=await e.modelReports(),notices=await e.deviceAdminReview(),notification=await e.notifyView();
check(reports.length===1&&notices.length===1,'Positive projection fixtures missing');
// Content is deliberately opaque synthetic storage, not a valid envelope or
// evidence of admission. It must stay untouched by these local projections.
const opaque='x'.repeat(65536),rows=Array.from({length:384},(_,i)=>({s:'kv',k:'receive-pending/'+i.toString(16).padStart(32,'0'),v:{id:i,envelope:opaque}}));
await store.write(rows);
const all=store.all,get=store.get,prefix=store.prefix,recovery=k=>String(k).startsWith('receive-pending/');
store.all=async s=>{if(s==='kv')throw Error('Overview loaded unrelated encrypted recovery data');return all(s);};
store.get=async(s,k)=>{if(s==='kv'&&recovery(k))throw Error('Overview read retained ciphertext');return get(s,k);};
store.prefix=async(s,p)=>{if(s==='kv'&&recovery(p))throw Error('Overview read retained ciphertext');return prefix(s,p);};
const started=performance.now(),after=await e.overview(),elapsed=performance.now()-started;
// Kept-back envelopes are counted by key for the receive status, never read.
const {receive_status:kept,...shown}=after;
check(JSON.stringify(shown)===JSON.stringify(before)&&kept?.waiting===384&&kept.failed===0,'Scoped overview changed visible state');
store.get=get;store.prefix=prefix;
check(JSON.stringify(await e.modelReports())===JSON.stringify(reports),'Model reports disappeared');
check(JSON.stringify(await e.deviceAdminReview())===JSON.stringify(notices),'Device notices disappeared');
check(JSON.stringify(await e.notifyView())===JSON.stringify(notification),'Notification recipients changed');
check((await store.get('kv',rows[0].k)).envelope===opaque,'View changed recovery ciphertext');
store.all=all;await e.close();store.close();
if(typeof window!=='undefined')await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});
const result={ok:true,storage:typeof window==='undefined'?'memory':'real IndexedDB',checks:6,unrelatedMiB:24,overviewMs:elapsed};
if(typeof window!=='undefined')window.overviewReadScopeResult=result;
console.log(JSON.stringify(result));
