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


const ops=[];for(let i=0;i<1000;i++){const id=wire.newID();ops.push({s:'outbox',k:id,v:{id,lid:id,conv,to:b.address,kind:'message',origin:'ui',body:'row '+i+' '+'x'.repeat(1300),at:1790000000000+i,ts:1790000000,state:'delivered',recipient_fp:b.fp,attachments:[]}});}for(let i=0;i<1000;i++){const id=wire.newID();ops.push({s:'outbox',k:id,v:{id,lid:id,conv:destination,to:b.address,kind:'message',origin:'ui',body:'Existing destination '+i+' '+'y'.repeat(1300),at:1790000000000+i,ts:1790000000,state:'delivered',recipient_fp:b.fp,attachments:[]}});}await a.store.write(ops);
const start=performance.now(),p=await a.previewTopicTransfer({source:conv,destination}),previewMs=performance.now()-start; a.chatTopicSummaries=async()=>{throw Error('Apply projected the complete destination.');};const applyStart=performance.now();await a.applyTopicTransfer({id:p.id,token:p.token,cursor:0});const applyMs=performance.now()-applyStart;
check(p.total_items===1000&&p.total_bytes>(1<<20),'synthetic source shape matches1000rows/>1MiB');console.log(JSON.stringify({checks,rows:p.total_items,destination_rows:1000,bytes:p.total_bytes,preview_ms:previewMs,apply_ms:applyMs}));
