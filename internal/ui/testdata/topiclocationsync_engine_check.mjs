// Topic Done/Reopen/Archive marks follow the person's own human devices as
// names do (client topicstatesync.go): two own devices converge, an older
// reader gets nothing and blocks nothing, another person cannot set them.
const assert={ok:(x,m)=>{if(!x)throw Error(m)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[],engines=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p==='/v1/version')return json({features:['env2','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);posts.push(o.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack'))return json(null,204);
 throw Error('Unexpected synthetic route '+p);
};
const current=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapTopicStateSyncV2].sort();
async function caps(e,names=current){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='topic-locations-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
 e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);e.connected=false;pubs.set(address,e.pub);return e;
}
async function person(address){
 const e=await device(address);e.roster=await wire.newRoster(e.keys,address,address.split('/')[0]);e.me=await e.personRecord([e.roster],'self',null);
 chains.set(e.me.person,[JSON.parse(wire.rosterJSON(e.roster))]);
 await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);await caps(e);return e;
}
async function sibling(owner,address){
 const e=await device(address),join=await wire.joinConsent(e.keys,address,owner.me.person,owner.me.seq+1,owner.me.hash);
 const humans=[...await wire.rosterHumans(owner.roster),e.fp];
 const next=await wire.nextRoster(owner.keys,owner.address,owner.roster,[...owner.me.devices.map(d=>pubs.get(d.address)),e.pub],join,owner.roster.label,humans);
 await wire.verifyNext(next,owner.roster);
 const steps=[...await Promise.all(chains.get(owner.me.person).map(r=>wire.parseRoster(r))),next];
 owner.roster=next;owner.me=await owner.personRecord(steps,'self',owner.me);e.roster=next;e.me=await e.personRecord(steps,'self',null);
 chains.set(owner.me.person,steps.map(r=>JSON.parse(wire.rosterJSON(r))));
 await owner.store.write([{s:'kv',k:'person',v:owner.me}]);await owner.pinDevices(owner.me);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}}]);await e.pinDevices(e.me);await caps(e);await caps(owner);return e;
}
const settle=async e=>{for(let i=0;i<4;i++){if(e.topicSyncRun)await e.topicSyncRun;if(e.topicMarkSyncRun)await e.topicMarkSyncRun;}};
async function receive(raw,e){const env=wire.parseEnvelope(raw);await e.admit(raw,env);await settle(e);return env.id;}
const carriers=async(e,to)=>(await e.store.all('outbox')).filter(o=>o.sub===wire.SubTopicStateSync&&(!to||o.to===to.address));
const mark=async(e,scope,topic)=>await e.store.get('kv','topic/'+scope+'/'+topic)||{};
// deliver: every carrier from one device to another, as the relay would.
async function deliver(from,to){
 const rows=(await carriers(from,to)).filter(r=>r.state==='queued');
 for(const r of rows){await from.post(r);const after=await from.store.get('outbox',r.id);if(after.state==='custody')await receive(r.envelope,to);}
 return rows.length;
}


const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone'),bp=await sibling(b,'bob/phone');
for(const e of[a,p])for(const other of[b,bp])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:other.address,v:{address:other.address,json:wire.marshalPublic(other.pub),fingerprint:other.fp,pending:null}}]);
const scope='a'.repeat(64),topic='0'.repeat(32),fact={scope,topic,destination:'b'.repeat(64),destination_topic:wire.newID(),operation:wire.newID(),at:1790000000,writer:a.fp,revision:1},key='topic-location/'+a.me.person+'/'+scope+'/'+topic;
await a.store.write([{s:'kv',k:key,v:{...fact,key,person:a.me.person}}]);await a.syncTopicLocations();
let rows=await carriers(a,p);check(rows.length===1&&rows[0].required_cap===wire.CapTopicStateSyncV2&&wire.parseTopicLocationSync(rows[0].body).v===2&&!wire.parseEnvelope(rows[0].envelope).attn,'one quiet encrypted V2 private location carrier');
check(!(await carriers(a,b)).length&&!(await carriers(a,bp)).length,'independent second person and linked phone receive no private location');
await deliver(a,p);const received=await p.store.get('kv',key);check(received?.operation===fact.operation,'own phone admits private location');
await p.syncTopicLocations();check(!(await carriers(p,a)).length,'exact sender ledger prevents echo');
const bobFact={...fact,scope:'c'.repeat(64),destination:'d'.repeat(64),writer:b.fp,operation:wire.newID()},bobKey='topic-location/'+b.me.person+'/'+bobFact.scope+'/'+bobFact.topic;await b.store.write([{s:'kv',k:bobKey,v:{...bobFact,key:bobKey,person:b.me.person}}]);await b.syncTopicLocations();await deliver(b,bp);check((await bp.store.get('kv',bobKey))?.operation===bobFact.operation,'second independent person converges its own private pointer');check(!await p.store.get('kv',bobKey)&&!await bp.store.get('kv',key),'neither person sees the other private register');
// V1 does not infer V2. Waiting carrier neither leaks nor blocks V1 marks.
await caps(p,current.filter(c=>c!==wire.CapTopicStateSyncV2).concat(wire.CapTopicStateSync));
const next={...fact,revision:2,operation:wire.newID(),at:fact.at+1};await a.store.write([{s:'kv',k:key,v:{...next,key,person:a.me.person}}]);await a.syncTopicLocations();rows=(await carriers(a,p)).filter(x=>x.state==='queued');const waiting=rows.at(-1),postsBefore=posts.length;await a.post(waiting);const parked=await a.store.get('outbox',waiting.id);check(parked.state==='waiting'&&posts.length===postsBefore,'V1-only reader retains exact location carrier without delivery');
const original=wire.newID();await a.store.write([{s:'inbox',k:original,v:{v:1,id:original,from:b.address,fp:b.fp,kind:'message',body:'Ordinary independent topic',at:Date.now(),read:true}}]);await a.changeTopic('done',{peer:b.address,id:original});if(a.topicMarkSyncRun)await a.topicMarkSyncRun;const markRows=(await carriers(a,p)).filter(x=>x.state==='queued'&&JSON.parse(x.body).v===1);check(markRows.length===1,'unrelated V1 mark still queues while private location waits');await a.post(markRows[0]);await receive(markRows[0].envelope,p);check((await mark(p,b.address,original)).mark==='done','unrelated V1 synchronization remains delivered');
const later={...next,revision:3,operation:wire.newID(),at:next.at+1};await a.store.write([{s:'kv',k:key,v:{...later,key,person:a.me.person}}]);await a.syncTopicLocations();check((await carriers(a,p)).filter(x=>JSON.parse(x.body).v===2&&x.state!=='custody').length===1,'further locations remain unsealed behind one exact waiting carrier');
await caps(p);await a.post({...parked,state:'queued'});await receive(parked.envelope,p);await a.syncTopicLocations();await deliver(a,p);check((await p.store.get('kv',key)).operation===later.operation,'upgrade releases retained carrier and backfills latest location');
// A late own phone receives the same private register without source content.
const q=await sibling(a,'alice/later');await a.syncTopicLocations();await deliver(a,q);check((await q.store.get('kv',key))?.operation===later.operation,'late current own human device catches up');
const own=await a.store.get('kv','person'),foreign=await wire.seal({v:2,id:wire.newID(),from:b.address,to:p.address,ts:1,kind:'message',sub:wire.SubTopicStateSync,replica:true,body:JSON.stringify({v:2,person:own.person,roster:own.hash,locations:[{...later,revision:99}]})},b.keys,p.pub);const foreignID=await receive(foreign,p);check((await p.store.get('held',foreignID))?.reason==='invalid'&&(await p.store.get('kv',key)).revision===3,'another person cannot set private source location');
const good={v:2,person:own.person,roster:own.hash,locations:[fact]};for(const mutate of[x=>x.extra=true,x=>x.v=1,x=>delete x.locations[0].destination_topic,x=>x.locations[0].revision=0,x=>x.locations.push({...fact}),x=>x.locations[0].task=true]){const value=structuredClone(good);mutate(value);let refused=false;try{wire.parseTopicLocationSync(JSON.stringify(value));}catch{refused=true;}check(refused,'strict V2 parser rejects malformed location');}
check(wire.capsReads({caps:[wire.CapTopicStateSyncV2]},wire.CapTopicStateSync)&&!wire.capsReads({caps:[wire.CapTopicStateSync]},wire.CapTopicStateSyncV2),'V2 implies V1 in one direction only');
console.log('topic location sync checks',checks);
if(typeof window!=='undefined'){
 await Promise.all(engines.flatMap(e=>[e.topicSyncRun,e.topicMarkSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun]).filter(Boolean).map(run=>run.catch(()=>{})));
 for(const s of new Set(engines.map(e=>e.store)))try{s.close();}catch{}
 for(const name of new Set(engines.map(e=>e.fixtureStoreName).filter(Boolean)))await new Promise(resolve=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=r.onerror=r.onblocked=resolve;});
 window.topicLocationSyncResult={ok:true,checks,storage:'IndexedDB'};
}
