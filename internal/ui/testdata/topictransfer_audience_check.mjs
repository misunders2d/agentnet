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


await a.sendDM({conv,body:'One exact source'});
const extras=[];
for(const owner of users){const steps=[owner.roster];for(let i=0;i<7;i++){const keys=await wire.newKeys(),address=owner.address.split('/')[0]+'/linked'+i,pub=await wire.publicEntry(keys,address),fp=await wire.fingerprint(pub),join=await wire.joinConsent(keys,address,owner.me.person,owner.me.seq+1,owner.me.hash),humans=[...await wire.rosterHumans(owner.roster),fp],publics=[...owner.me.devices.map(d=>[...users,...extras].find(x=>x.address===d.address).pub),pub],next=await wire.nextRoster(owner.keys,owner.address,owner.roster,publics,join,owner.roster.label,humans);await wire.verifyNext(next,owner.roster);owner.roster=next;steps.push(next);owner.me=await owner.personRecord(steps,'self',owner.me);extras.push({address,pub,fp});}await owner.store.write([{s:'kv',k:'person',v:owner.me}]);}
for(const e of users){for(const other of users)await e.store.write([{s:'persons',k:other.me.person,v:{...other.me,state:other===e?'self':'pinned'}}]);for(const d of [...users,...extras])await e.store.write([{s:'pins',k:d.address,v:{address:d.address,json:wire.marshalPublic(d.pub),fingerprint:d.fp,pending:null}}]);}
const p=await a.previewTopicTransfer({source:conv,destination});check(p.audience.length===16&&p.audience_total===24&&p.audience_next===16,'public header explicitly pages full 24-device audience');
const audience=await a.topicTransferPage({id:p.id,token:p.token,audience_after:p.audience_next});check(audience.audience.length===8&&audience.audience_total===24&&audience.audience_next===0&&!audience.body&&!audience.items.length,'remaining audience has independent bounded page');
check(new Set([...p.audience,...audience.audience].map(x=>x.fingerprint)).size===24,'no audience omission or duplicate between pages');check(new TextEncoder().encode(JSON.stringify(p)).length<(48<<10)&&new TextEncoder().encode(JSON.stringify(audience)).length<(48<<10),'encoded audience/header both bounded');
console.log(JSON.stringify({checks,audience:p.audience_total}));
