// Real signed/encrypted browser turns over isolated stores, no live service.
const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 ok:(value,message)=>{if(!value)throw Error(message||'Expected truthy');},
 deepEqual:(actual,expected,message)=>{if(JSON.stringify(actual)!==JSON.stringify(expected))throw Error(message||'Structures differ');},
 rejects:async(fn,re)=>{try{await fn();}catch(error){if(re.test(error.message))return;throw error;}throw Error('Expected rejection '+re);}
};
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, openIDB, chatTopicAssignments } from '../static/engine.mjs';
let checks=0;
const check=(v,why)=>{assert.ok(v,why);checks++;};
const same=(a,b,why)=>{assert.deepEqual(a,b,why);checks++;};
const refuses=async(fn,re)=>{await assert.rejects(fn,re);checks++;};
const users=[];
for(const name of ['alice','bob','outsider']){
 const storeName='topic-transfer-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(storeName);
 const e=new Engine({store,base:'https://synthetic.invalid',now:()=>1790000000000,fetch:async()=>{throw Error('No live network in contribution check');}});
 e.fixtureStoreName=storeName;
 e.keys=await wire.newKeys();e.address=name+'/desk';e.pub=await wire.publicEntry(e.keys,e.address);e.fp=await wire.fingerprint(e.pub);
 e.roster=await wire.newRoster(e.keys,e.address,name);e.me=await e.personRecord([e.roster],'self',null);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}}]);
 e.refreshPerson=async p=>p;e.supports=async()=>[true,'',false];e.sendGroupCopy=async()=>'';e.queueOutbox=()=>{};e.changed=()=>{};e.syncTopicTitles=async()=>{};e.syncTopicLocations=async()=>{};
 e.ctlSupport=async(address,pin,cap)=>{const other=users.find(u=>u.address===address);return [other?.caps?.includes(cap)??true,'peer_update: org1 required'];};
 e.caps=[wire.CapTopicOrganization];users.push(e);
}
for(const e of users)for(const other of users){
 const p=await e.personRecord([other.roster],other===e?'self':'pinned',null);
 await e.store.write([{s:'persons',k:p.person,v:p},{s:'pins',k:other.address,v:{address:other.address,json:wire.marshalPublic(other.pub),fingerprint:other.fp,pending:null}}]);
}
const [a,b,outsider]=users,root=await wire.newRoot(a.keys,{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},{person:b.me.person,roster:b.me.hash});
const conv=await wire.rootID(root),c={id:conv,root:wire.rootJSON(root),peer:b.me.person,created:root.created,creator:a.address};
await a.store.write([{s:'convs',k:conv,v:c}]);
await b.store.write([{s:'convs',k:conv,v:{...c,peer:a.me.person}}]);
await outsider.store.write([{s:'convs',k:conv,v:c}]);
const realm=wire.newID();for(const e of users){e.realm=realm;e.groupSupport=async()=>false;}
const members=users.map(e=>({person:e.me.person,roster:e.me.hash})).sort((x,y)=>x.person.localeCompare(y.person));
const gr=await wire.signGroupRoot(a.keys,{v:wire.GroupRootVersion,kind:'group',realm,title:'Destination group',creator:{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},members,admins:[a.me.person],nonce:wire.newID(),created:1790000000}),destination=await wire.rootID(gr);
const groupMembers=[];for(const m of members){const e=users.find(e=>e.me.person===m.person),admission=await wire.signGroupAdmission(e.keys,{conv:destination,realm,person:m.person,roster:m.roster,seq:0,prev:'',history:null,by:e.fp});groupMembers.push({...m,admin:e===a,admission});}
const state=await wire.signGroupState(a.keys,{v:1,conv:destination,realm,seq:0,prev:'',title:'Destination group',members:groupMembers,actor:a.me.person,actor_roster:a.me.hash,by:a.fp});
await wire.verifyGroupState(state,gr,null,async person=>users.find(e=>e.me.person===person)?.roster,[]);
const packet={root:gr,state,withdrawals:null},context=wire.groupContextJSON(packet);
for(const e of users)await e.store.write([{s:'kv',k:'group/'+destination,v:{root:wire.rootJSON(gr),context,records:['signed bootstrap context verified above'],withdrawals:[],pending:[],rosters:{}}}]);

// Entire source is selected server-side; caller supplies no ID list.
const mode=globalThis.topicTransferFixtureMode||'full',sourceMessageCount=mode==='files'?0:66,fileCount=mode==='count'?0:10,includeLarge=mode!=='count';
const source=[];
for(let i=0;i<sourceMessageCount;i++)source.push(await a.sendDM({conv,body:`Item ${i} \n`+'x'.repeat(20)}));
const canonical=`[@Agent](agentnet:agent/${wire.newID()})`;
const largeBody=canonical+'\n'+('🙂@agent\n'.repeat(14000));if(includeLarge)check(new TextEncoder().encode(largeBody).length>(128<<10),'single original exceeds the old 128 KiB quoted-body limit');const large=includeLarge?await a.sendDM({conv,body:largeBody}):null;
for(let i=0;i<fileCount;i++){const bytes=new TextEncoder().encode(('retained file '+i).repeat(8500));await a.sendDM({conv,body:'With file '+i,files:[{name:'file-'+i+'.txt',size:bytes.length,bytes}]});}
const originals=new Map((await a.store.all('outbox')).filter(r=>r.conv===conv).map(r=>[r.id,r.envelope]));
let p=await a.api('/api/topic/transfer/preview',{source:conv,destination,new_topic:true,title:'Whole topic'});
check(p.total_items===sourceMessageCount+Number(includeLarge)+fileCount&&p.total_files===fileCount&&(mode==='count'||p.total_bytes>(1<<20))&&(mode==='files'||p.total_batches>64),'whole topic exceeds old count/file/aggregate bounds');
const manifest=(await a.store.prefix('kv','topic-transfer-ref/'+p.id+'/'));
check(manifest.length===p.total_batches&&manifest.every(r=>!('body'in r)&&wire.validHash(r.hash)),'only hash references and fragment bounds persisted');
let first=await a.api('/api/topic/transfer/page',{id:p.id,token:p.token});
check(new TextEncoder().encode(JSON.stringify(first)).length<=48<<10&&first.items.length===1,'preview page encoded bytes bounded');
const late=await a.sendDM({conv,body:'LATE BACKDATED MUST STAY SOURCE'});
const lateRow=await a.store.get('outbox',late.id);await a.store.write([{s:'outbox',k:late.id,v:{...lateRow,ts:1,at:1790000010000}}]);
p=await a.api('/api/topic/transfer/apply',{id:p.id,token:p.token,cursor:0});
check(p.cursor===1&&p.state==='partial','one bounded apply queues one batch');
const outCount=(await a.store.all('outbox')).length;
const retried=await a.applyTopicTransfer({id:p.id,token:p.token,cursor:0});
check(retried.cursor===1&&(await a.store.all('outbox')).length===outCount,'old exact cursor retry does not advance or duplicate');
check(await a.topicTransferLocation({source:conv})===null,'partial snapshot has no moved location');
for(let index=p.cursor;index<p.total_batches;index++){p=await a.applyTopicTransfer({id:p.id,token:p.token,cursor:index});if(index%25===0)console.log('topic transfer queued '+index+'/'+p.total_batches);}
check(p.state==='queued'&&p.cursor===p.total_batches,'all whole-topic fragments durably queued');
const quotes=(await a.store.all('outbox')).filter(r=>r.conv===destination&&r.topic===p.destination_topic);
check(quotes.every(r=>r.kind==='message'&&!r.target&&!r.pid&&!r.agent_id&&!r.reply_to&&!r.receiver_route&&!r.followup),'quoted imports carry no executable request/receiver ancestry');
check(quotes.every(r=>!r.body.includes('agentnet:agent/')&&!r.body.includes('LATE BACKDATED MUST')),'quoted canonical mentions neutralized and late arrival excluded');
check(quotes.reduce((n,r)=>n+(r.attachments?.length||0),0)===fileCount*2,'all selected retained files copied once per two recipients');
for(const[id,envelope]of originals)check((await a.store.get('outbox',id)).envelope===envelope,'source envelope unchanged');
const moved=await a.topicTransferLocation({source:conv});check(moved?.available&&moved.destination===destination&&moved.operation===p.id,'completed private source location points to accessible destination');
const again=await a.applyTopicTransfer({id:p.id,token:p.token,cursor:0});check(again.state==='queued'&&again.cursor===p.total_batches,'completed old cursor retry remains exact');
// A second plan may be cancelled after partial queue, with no location change.
let cancelled=await a.previewTopicTransfer({source:conv,destination});cancelled=await a.applyTopicTransfer({id:cancelled.id,token:cancelled.token,cursor:0});cancelled=await a.cancelTopicTransfer({id:cancelled.id,token:cancelled.token});
check(cancelled.state==='canceled'&&cancelled.cursor===1,'partial cancel keeps truthful queued cursor');await refuses(()=>a.applyTopicTransfer({id:cancelled.id,token:cancelled.token,cursor:1}),/cancelled/);
// Source edits and recipient-key changes invalidate unqueued portions.
let stale=await a.previewTopicTransfer({source:conv,destination});const staleRef=await a.store.get('kv','topic-transfer-ref/'+stale.id+'/000000000000'),original=await a.store.get('outbox',staleRef.id);await a.store.write([{s:'outbox',k:original.id,v:{...original,body:'Changed source'}}]);await refuses(()=>a.applyTopicTransfer({id:stale.id,token:stale.token,cursor:0}),/changed/);await a.store.write([{s:'outbox',k:original.id,v:original}]);
stale=await a.previewTopicTransfer({source:conv,destination});const pin=await a.store.get('pins',outsider.address);await a.store.write([{s:'pins',k:outsider.address,v:{...pin,pending:pin.json}}]);await refuses(()=>a.applyTopicTransfer({id:stale.id,token:stale.token,cursor:0}),/changed|key/);await a.store.write([{s:'pins',k:outsider.address,v:pin}]);
// Completed cursor survives reopening; no dispatch or source scan is needed.
if(typeof window!=='undefined'){a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:'https://synthetic.invalid',fetch:async()=>{throw Error('Restart offline');}});await restarted.load();const restored=await restarted.applyTopicTransfer({id:p.id,token:p.token,cursor:0});check(restored.cursor===p.total_batches,'lost response/restart exact retry retains queued result');
console.log(JSON.stringify({checks,batches:p.total_batches,bytes:p.total_bytes}));
if(typeof window!=='undefined'){
 for(const e of [...users,restarted]){e.stop();await Promise.all([e.topicSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun].filter(Boolean));e.store.close();}
 for(const e of users)await new Promise((resolve,reject)=>{const request=indexedDB.deleteDatabase(e.fixtureStoreName);request.onsuccess=resolve;request.onerror=()=>reject(request.error);});
 window.topicTransferResult={ok:true,checks,storage:'IndexedDB'};
}
