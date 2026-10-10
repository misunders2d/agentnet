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
const current=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapTopicStateSync].sort();
async function caps(e,names=current){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='topic-marks-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
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

const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
check(current.includes(wire.CapTopicStateSync)&&!wire.RoomImplies.includes(wire.CapTopicStateSync),'tss1 is an explicit capability, never implied by rm1');

// A device chat with an agent, held by both own devices.
const thread=wire.newID(),original={v:1,id:thread,from:b.address,fp:b.fp,kind:'message',body:'Device topic',at:Date.now(),read:true};
for(const e of [a,p])await e.store.write([{s:'inbox',k:thread,v:original}]);
const deviceState=async e=>(await e.threadSummaries()).find(t=>t.id===thread)?.state;
check(await deviceState(p)==='active','device topic starts active on the phone');
let r=await a.changeTopic('done',{peer:b.address,id:thread});await settle(a);
check(/syncs across your linked devices/.test(r.note),'Mark done says it syncs across linked devices');
let rows=await carriers(a,p);
check(rows.length===1&&rows[0].required_cap===wire.CapTopicStateSync&&rows[0].aside&&!wire.parseEnvelope(rows[0].envelope).attn,'Mark done queues one quiet encrypted tss1 carrier');
check(!(await carriers(a,b)).length,'another person never gets a carrier');
await deliver(a,p);
check((await mark(p,b.address,thread)).mark==='done'&&(await mark(p,b.address,thread)).mark_count===1&&await deviceState(p)==='done','laptop Mark done is done on the phone');
check(!(await p.store.all('inbox')).some(m=>m.sub===wire.SubTopicStateSync),'a mark never becomes a message');
await p.syncTopicMarks();check(!(await carriers(p,a)).length,'the sender already holding the mark gets no echo');

r=await p.changeTopic('reopen',{peer:b.address,id:thread});await settle(p);await deliver(p,a);
check((await mark(a,b.address,thread)).mark==='open'&&await deviceState(a)==='active','phone Reopen reaches the laptop');
r=await a.changeTopic('archive',{peer:b.address,id:thread});await settle(a);
check(/^Archived\. It syncs/.test(r.note),'device Archive names itself and its sync');
await deliver(a,p);check(await deviceState(p)==='archived','laptop Archive is archived on the phone');

// A people chat topic: Archive is the person's; Done stays a shared event.
const conv='c'.repeat(64),topic=wire.newID(),turn={v:2,id:topic,lid:topic,conv,topic,from:b.address,fp:b.fp,kind:'message',body:'Chat topic',read:true,at:Date.now(),ts:Math.floor(Date.now()/1000)};
for(const e of [a,p])await e.store.write([{s:'inbox',k:topic,v:turn}]);
r=await a.changeChatTopic('archive',{conv,id:topic});await settle(a);
check(/syncs across your linked devices/.test(r.note),'chat Archive says it syncs');
await deliver(a,p);check((await p.chatTopics(conv))[0].state==='archived','chat topic Archive reaches the phone');

// Concurrent marks converge by signed time, then writer, in any order.
const own=await a.store.get('kv','person'),seal=async(sender,marks,target=p)=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:target.address,ts:1,kind:'message',sub:wire.SubTopicStateSync,replica:true,body:JSON.stringify({v:1,person:own.person,roster:own.hash,marks})},sender.keys,target.pub);
const base=(await mark(p,b.address,thread)),future=base.mark_at+1000,[low,high]=[a.fp,p.fp].sort();
const x={scope:b.address,topic:thread,mark:'done',count:1,at:future,writer:high},y={...x,mark:'open',writer:low};
await receive(await seal(a,[y]),p);await receive(await seal(a,[x]),p);await receive(await seal(p,[x],a),a);await receive(await seal(p,[y],a),a);
check((await mark(p,b.address,thread)).mark==='done'&&(await mark(a,b.address,thread)).mark==='done','concurrent marks converge on both devices whatever the order');
await receive(await seal(a,[{...x,mark:'archived',at:future-1}]),p);check((await mark(p,b.address,thread)).mark==='done','an older mark changes nothing');
r=await p.changeTopic('reopen',{peer:b.address,id:thread});await settle(p);
check((await mark(p,b.address,thread)).mark_at===future+1,'a choice made over a newer clock replaces it');
await deliver(p,a);check((await mark(a,b.address,thread)).mark==='open','and converges on the other device');
const foreign=await receive(await seal(b,[{...x,at:future+50,writer:b.fp}]),p);
check((await p.store.get('held',foreign))?.reason==='invalid'&&(await mark(p,b.address,thread)).mark==='open','another person cannot set our marks');

// An older own reader (no tss1) gets nothing, blocks nothing, collects nothing.
await caps(p,current.filter(c=>c!==wire.CapTopicStateSync));
await a.changeTopic('done',{peer:b.address,id:thread});await settle(a);
rows=(await carriers(a,p)).filter(o=>o.state==='queued');const sent=posts.length;
await a.post(rows[0]);const parked=await a.store.get('outbox',rows[0].id);
check(parked.state==='waiting'&&/peer_update/.test(parked.detail)&&posts.length===sent,'older reader waits without receiving the unknown subtype');
const name=wire.newID();await a.store.write([{s:'inbox',k:name,v:{...original,id:name,body:'Another topic'}}]);await p.store.write([{s:'inbox',k:name,v:{...original,id:name,body:'Another topic'}}]);
await a.changeTopic('done',{peer:b.address,id:name});await settle(a);
check((await carriers(a,p)).filter(o=>o.state!=='custody').length===1,'later marks stay unsealed instead of piling up for the older reader');
check((await mark(p,b.address,thread)).mark==='open','older reader still has its own state');
// Restart: retained evidence, no duplicate carriers.
if(typeof window!=='undefined'){a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});engines.push(restarted);
const before=(await carriers(a)).length;await restarted.syncTopicMarks();check((await carriers(a)).length===before,'restart retains per-recipient carrier evidence');
// It updates: the waiting carrier goes, then the marks held back.
await caps(p);await restarted.post({...parked,state:'queued'});check(posts.length===sent+1&&(await a.store.get('outbox',parked.id)).envelope===parked.envelope,'restored capability sends the identical retained carrier');
await receive(parked.envelope,p);await restarted.syncTopicMarks();await deliver(restarted,p);
check((await mark(p,b.address,thread)).mark==='done'&&(await mark(p,b.address,name)).mark==='done','updated reader converges on every held-back mark');

// Marks set before marks synced are the person's too, once; malformed ones stay here.
const q=await sibling(a,'alice/other'),old=wire.newID(),bad=wire.newID();
await restarted.store.write([{s:'kv',k:'topic/'+b.address+'/'+old,v:{type:'topic-state',peer:b.address,topic:old,mark:'archived',mark_at:1234,mark_count:3}},{s:'kv',k:'topic/'+b.address+'/'+bad,v:{type:'topic-state',peer:b.address,topic:bad,mark:'done'}},{s:'kv',k:'topic-mark-seeded/'+own.person,v:undefined}]);
await restarted.syncTopicMarks();await deliver(restarted,q);
check((await mark(q,b.address,old)).mark==='archived'&&(await mark(q,b.address,old)).mark_at===1234&&!(await mark(q,b.address,bad)).mark,'older local marks seed once at their own time; malformed rows stay');
const seeded=(await carriers(restarted)).length;await restarted.syncTopicMarks();check((await carriers(restarted)).length===seeded,'completed seed never reissues marks');

// Strict wire: Go and the browser refuse the same shapes (TestBrowserTopicStateSyncWireMatchesGo).
const good={v:1,person:own.person,roster:own.hash,marks:[x]};wire.parseTopicStateSync(JSON.stringify(good));
for(const mutate of [m=>delete m.marks[0].count,m=>m.marks[0].mark=null,m=>m.marks[0].mark='deleted',m=>m.marks[0].count=0,m=>{m.marks[0].mark='';},m=>m.marks[0].at=0,m=>m.marks.push({...m.marks[0]}),m=>m.extra=true,m=>m.marks[0].task=true]){
 const c=structuredClone(good);mutate(c);let ok=true;try{wire.parseTopicStateSync(JSON.stringify(c));}catch{ok=false;}check(!ok,'strict mark parser refuses '+mutate);
}
let refused=false;try{await wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'task',sub:wire.SubTopicStateSync,replica:true,body:JSON.stringify(good)},a.keys,p.pub);}catch{refused=true;}check(refused,'a mark carrier can never be a task');
console.log('topic state sync browser checks',checks);
if(typeof window!=='undefined'){
 // Background runs of every engine finish before any shared store closes.
 await Promise.all(engines.flatMap(e=>[e.topicSyncRun,e.topicMarkSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun]).filter(Boolean).map(run=>run.catch(()=>{})));
 for(const s of new Set(engines.map(e=>e.store)))try{s.close();}catch{}
 for(const name of new Set(engines.map(e=>e.fixtureStoreName).filter(Boolean)))await new Promise(resolve=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=r.onerror=r.onblocked=resolve;});
 window.topicStateSyncResult={ok:true,checks,storage:'IndexedDB'};
}
