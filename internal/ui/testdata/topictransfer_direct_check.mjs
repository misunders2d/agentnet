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


const bytes=new TextEncoder().encode('Exact retained direct file'),direct=await a.sendV1({queued:true,to:b.address,kind:'question',body:'Original direct @literal code x@y.invalid [@Team](agentnet:collective/v1/p.'+a.me.person+')',replyTo:'',files:[{name:'direct.txt',size:bytes.length,bytes}],status:''});
a.now=()=>1790000001000;const child=await a.sendV1({queued:true,to:b.address,kind:'message',body:'Second direct source',replyTo:direct.id,files:[],status:''});
const get=a.store.get.bind(a.store);let ciphertextReads=0;a.store.get=async(s,k)=>{if(s==='files'){ciphertextReads++;throw Error('Preview read attachment ciphertext.');}return get(s,k);};let p=await a.previewTopicTransfer({peer:b.address,thread:direct.id,destination:conv});check(p.total_items===2&&p.total_files===1,'complete concrete legacy thread enumerated');const status=await a.topicTransferStatus({peer:b.address,thread:direct.id});check(status.id===p.id,'restart/resume finds latest source header without extra state');
let page=await a.topicTransferPage({id:p.id,token:p.token,after:0});check(page.body.includes('x@y.invalid')&&!page.body.includes('](agentnet:collective/'),'quotation preserves literal emails/code @ while collective mention stays inert');
check(ciphertextReads===0,'preview and text page use file-key existence without loading ciphertext');a.store.get=get;for(let cursor=0;cursor<p.total_batches;cursor++)p=await a.applyTopicTransfer({id:p.id,token:p.token,cursor});check(p.state==='queued','legacy snapshot can continue into full-member DM');const copies=(await a.store.all('outbox')).filter(r=>r.conv===conv);check(copies.every(r=>r.kind==='message'&&!r.target&&!r.pid&&!r.reply_to),'legacy questions import as inert DM quotes');
const file=copies.find(r=>r.attachments?.length),opened=await wire.open(file.envelope,b.keys,b.address,a.pub);same([...await wire.decryptFile(file.files[0].ct,opened.attachments[0],b.keys)],[...bytes],'legacy retained attachment encrypted unchanged to destination');
const pointer=await a.topicTransferLocation({peer:b.address,thread:direct.id});check(pointer?.available&&pointer.scope===b.address&&pointer.topic===direct.id&&pointer.destination===conv,'private direct source location follows the native scope/topic contract');
// Group sources use the same explicit current full-member boundary.
await a.sendGroupTurn(await a.groupRecord(destination),{queued:true,body:'Exact group source',topic:'',files:[]});let fromGroup=await a.previewTopicTransfer({source:destination,destination:conv});check(fromGroup.total_items===1,'whole group Main source enumerates its canonical original once');fromGroup=await a.applyTopicTransfer({id:fromGroup.id,token:fromGroup.token,cursor:0});check(fromGroup.state==='queued','group source can continue in eligible DM');
// Missing bytes stop only that exact batch; restoring retained bytes keeps its review valid.
let acquiring=await a.previewTopicTransfer({peer:b.address,thread:direct.id,destination:conv}),fileRef=(await a.store.prefix('kv','topic-transfer-ref/'+acquiring.id+'/')).find(r=>r.files.length),sourceFile=(await a.store.get('outbox',direct.id)).attachments[0],kept=await a.store.get('files','kept/'+sourceFile.sha256);
a.store.get=async(s,k)=>{if(s==='files')throw Error('Text batch read attachment ciphertext.');return get(s,k);};for(let cursor=0;cursor<fileRef.index;cursor++)acquiring=await a.applyTopicTransfer({id:acquiring.id,token:acquiring.token,cursor});a.store.get=get;await a.store.write([{s:'files',k:'kept/'+sourceFile.sha256}]);const unavailable=await a.topicTransferPage({id:acquiring.id,token:acquiring.token,after:fileRef.index});check(unavailable.items[0].files[0].available===false&&unavailable.items[0].files[0].selected===true&&wire.validHash(unavailable.items[0].hash),'bounded public page preserves hash and selected-file DTO fields');await refuses(()=>a.applyTopicTransfer({id:acquiring.id,token:acquiring.token,cursor:fileRef.index}),/unavailable/);await a.store.write([{s:'files',k:'kept/'+sourceFile.sha256,v:kept}]);const commit=a.commitReceiverCopies;a.commitReceiverCopies=async function(...args){await a.store.write([{s:'files',k:'kept/'+sourceFile.sha256}]);return commit.apply(this,args);};try{await refuses(()=>a.applyTopicTransfer({id:acquiring.id,token:acquiring.token,cursor:fileRef.index}),/storage changed|changed/);}finally{a.commitReceiverCopies=commit;await a.store.write([{s:'files',k:'kept/'+sourceFile.sha256,v:kept}]);}check((await a.topicTransferStatus({id:acquiring.id})).cursor===fileRef.index,'file loss at commit leaves cursor and encrypted outbox unadvanced');for(let cursor=fileRef.index;cursor<acquiring.total_batches;cursor++)acquiring=await a.applyTopicTransfer({id:acquiring.id,token:acquiring.token,cursor});check(acquiring.state==='queued','exact acquired file resumes without changing immutable review');
// Delete and acquisition checks use exact source facts, never retained preview text.
const single=await a.sendV1({queued:true,to:b.address,kind:'message',body:'Erase exact source',replyTo:'',files:[],status:''});const post=a.post;a.post=async()=>{};const revision=await a.sendDeviceControl(b.address,{id:single.id,fingerprint:a.fp},wire.SubRevision,JSON.stringify({rev:1,text:'Current edited source'}));a.post=post;p=await a.previewTopicTransfer({peer:b.address,thread:single.id,destination:conv});const revisionRow=await a.store.get('outbox',revision.id);await wire.verifyEnvelope(wire.parseEnvelope(revisionRow.envelope),a.pub.sign_key);await a.store.write([{s:'outbox',k:revision.id,v:{...revisionRow,state:'custody',detail:'Delivery only',handover_started:true}}]);p=await a.applyTopicTransfer({id:p.id,token:p.token,cursor:0});check(p.state==='queued','source control transport progress does not change reviewed semantics');p=await a.previewTopicTransfer({peer:b.address,thread:single.id,destination:conv});await a.store.write([{s:'erased',k:'|'+a.fp+'|'+single.id,v:{}}]);await refuses(()=>a.applyTopicTransfer({id:p.id,token:p.token,cursor:0}),/changed/);
let freshTopic=await a.previewTopicTransfer({peer:b.address,thread:direct.id,destination,new_topic:true,title:'Continued direct topic'});for(let cursor=0;cursor<freshTopic.total_batches;cursor++)freshTopic=await a.applyTopicTransfer({id:freshTopic.id,token:freshTopic.token,cursor});check(freshTopic.state==='queued'&&freshTopic.total_batches>1,'own new-topic title and ordinary parts do not stale later batches');const archived=await a.previewTopicTransfer({peer:b.address,thread:direct.id,destination,destination_topic:freshTopic.destination_topic});await a.changeChatTopic('archive',{conv:destination,id:freshTopic.destination_topic,count:freshTopic.total_batches});await refuses(()=>a.applyTopicTransfer({id:archived.id,token:archived.token,cursor:0}),/changed/);await a.changeChatTopic('reopen',{conv:destination,id:freshTopic.destination_topic,count:freshTopic.total_batches});
const collision=await a.previewTopicTransfer({peer:b.address,thread:direct.id,destination,new_topic:true,title:'Planned fresh topic'});await a.sendGroupTurn(await a.groupRecord(destination),{queued:true,body:'Concurrent colliding destination',topic:collision.destination_topic,files:[]});await refuses(()=>a.applyTopicTransfer({id:collision.id,token:collision.token,cursor:0}),/changed/);
console.log(JSON.stringify({checks,batches:p.total_batches}));
if(typeof window!=='undefined'){
 for(const e of users){e.stop();await Promise.all([e.topicSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun].filter(Boolean));e.store.close();}
 for(const e of users)await new Promise(resolve=>{const r=indexedDB.deleteDatabase(e.fixtureStoreName);r.onsuccess=r.onerror=r.onblocked=resolve;});
 window.topicTransferDirectResult={ok:true,checks,storage:'IndexedDB'};
}
