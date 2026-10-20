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
const current=[wire.CapDirectTopicSync,wire.CapOwnSyncV3,wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapTopicStateSync].sort();
async function caps(e,names=current){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='direct-topics-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
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
const id=()=>wire.newID(),source=id(),child=id(),destination=id(),unrelated=id();
const message=(id,body,at,reply_to='')=>({v:1,id,from:b.address,fp:b.fp,kind:'message',body,at,ts:Math.floor(at/1000),read:true,reply_to});
for(const [i,m]of [{...message(source,'selected original',1000000),kind:'task',state:'answered',attempts:1},message(child,'unselected child',1001000,source),message(destination,'existing destination',1002000),message(unrelated,'unrelated',1003000)].entries())await a.store.write([{s:'inbox',k:m.id,v:m}]);
const originals=JSON.stringify(await a.store.all('inbox'));
await a.changeTopic('rename',{peer:b.address,id:source,title:'Source name'});
await a.changeTopic('rename',{peer:b.address,id:destination,title:'Existing name'});
const review=await a.previewTopicOrganization({conv:'',peer:b.address,ids:[source],topic:destination});
const moved=await a.applyTopicOrganization(review);
check(moved.state==='stored','move proves local organization, not delivery or work completion');
check(JSON.stringify(await a.store.all('inbox'))===originals,'original messages and work state unchanged');
const replay=await a.applyTopicOrganization(review);check(replay.id===moved.id,'exact retry is idempotent');
let denied=false;try{await a.applyTopicOrganization({...review,topic:unrelated});}catch{denied=true;}check(denied,'changed retry refused');
let thread=await a.thread(destination);check(thread.topic.id===destination&&thread.topic.title==='Existing name','existing destination identity and name retained');
check(thread.messages.map(m=>m.id).join(',')===[source,destination].join(','),'selected original renders in chronological order in existing destination');
check((await a.thread(child)).topic.id===child,'unselected descendant stays outside moved topic and remains navigable');
check((await a.thread(unrelated)).topic.id===unrelated,'unrelated topic unchanged');
await a.changeTopic('rename',{peer:b.address,id:destination,title:'Renamed destination'});
await a.changeTopic('done',{peer:b.address,id:destination,count:2});
check((await mark(a,b.address,source)).title==='Source name'&&(await mark(a,b.address,destination)).title==='Renamed destination','rename and mark use destination metadata without consuming earlier moved source label');

await a.syncDirectTopics();
const directCarriers=async(e,to)=>(await e.store.all('outbox')).filter(m=>m.sub===wire.SubDirectTopicSync&&(!to||m.to===to.address));
let copies=await directCarriers(a,p);check(copies.length===1&&copies[0].required_cap===wire.CapDirectTopicSync&&!wire.parseEnvelope(copies[0].envelope).attn,'quiet encrypted explicit-capability own-device carrier');
check(!(await directCarriers(a,b)).length,'another person gets no placements');
await receive(copies[0].envelope,p);await p.syncDirectTopics();
check(!(await p.store.all('inbox')).some(m=>m.sub===wire.SubDirectTopicSync),'placement is not a message');
check((await p.store.prefix('kv','direct-topic/'+p.me.person+'/')).length===1,'missing originals retain inert placement');
check(!(await directCarriers(p,a)).length,'sender holding placement gets no echo');
for(let n=0;n<4;n++)await a.directHistory().step(p.me.devices.find(d=>d.address===p.address));
for(const r of (await a.store.all('outbox')).filter(m=>m.sub===wire.SubDeviceHistory&&m.to===p.address))await receive(r.envelope,p);
thread=await p.thread(destination);check(thread.messages.length===2&&thread.messages[0].id===source,'placement arriving before originals applies after history');
check(thread.messages[0].history&&thread.messages[0].to===a.address,'original author and audience survive organization and replication');
const undo={conv:'',peer:b.address,lid:moved.id,author:'',id:id()};await a.undoTopicOrganization(undo);await a.undoTopicOrganization(undo);
check((await a.thread(source)).messages.length===2&&(await a.thread(destination)).messages.length===1,'atomic undo restores source and destination; retry stays inert');
check((await a.thread(source)).topic.title==='Source name'&&(await a.thread(destination)).topic.title==='Renamed destination','undo preserves both independently named topics');
const stale=await a.previewTopicOrganization({conv:'',peer:b.address,ids:[unrelated],topic:destination});
const arrival=id();await a.store.write([{s:'inbox',k:arrival,v:message(arrival,'arrival during review',1004000,destination)}]);
denied=false;try{await a.applyTopicOrganization(stale);}catch{denied=true;}check(denied,'new message invalidates reviewed placement');
const own=await a.store.get('kv','person'),fact={peer:b.address,id:source,author:b.fp,topic:destination,revision:100,writer:b.fp,destination:review.destination};
const seal=async(from,facts)=>wire.seal({v:2,id:id(),from:from.address,to:p.address,ts:1,kind:'message',sub:wire.SubDirectTopicSync,replica:true,body:JSON.stringify({v:1,person:own.person,roster:own.hash,placements:facts})},from.keys,p.pub);
const foreign=await receive(await seal(b,[fact]),p);check((await p.store.get('held',foreign))?.reason==='invalid','another person cannot organize our direct chats');
const qid=id(),rid=id(),statusID=id(),completedDestination=id();
await a.store.write([{s:'outbox',k:completedDestination,v:{v:1,id:completedDestination,from:a.address,to:b.address,recipient_fp:b.fp,kind:'message',body:'same original executor destination',at:1004500,ts:1004,state:'delivered'}}]);
await a.store.write([{s:'outbox',k:qid,v:{v:1,id:qid,from:a.address,to:b.address,fp:b.fp,kind:'task',body:'already completed original task',at:1005000,ts:1005,state:'delivered'}},{s:'inbox',k:rid,v:{...message(rid,'original final result',1006000,qid),kind:'result',status:'done'}},{s:'inbox',k:statusID,v:{v:3,id:statusID,from:b.address,fp:b.fp,kind:'message',sub:wire.SubStatus,control:true,ref:{id:qid,fingerprint:a.fp},body:JSON.stringify({state:'running',n:1,at:1005}),at:1005000}}]);
const completedReview=await a.previewTopicOrganization({conv:'',peer:b.address,ids:[qid],topic:completedDestination});await a.applyTopicOrganization(completedReview);
const completedView=await a.thread(qid);check(!completedView.topic.pending&&completedView.messages.find(m=>m.id===qid).exec?.state==='done','held result outside display topic still proves completion; no pending/running regression: '+JSON.stringify({pending:completedView.topic.pending,exec:completedView.messages.find(m=>m.id===qid).exec}));
const [low,high]=[a.fp,p.fp].sort();
await receive(await seal(a,[{...fact,revision:500,writer:high,topic:destination}]),p);
await receive(await seal(a,[{...fact,revision:500,writer:low,topic:source}]),p);
await receive(await seal(a,[{...fact,revision:499,writer:high,topic:source}]),p);
check((await p.thread(source)).topic.id===destination,'reordered concurrent and older placements converge without rewriting originals');
const imported=await p.store.get('inbox',source);check(imported.replica&&!(imported.state||imported.attempts),'copied original retains no execution state after organization');
// Explicit dts1 capability: old readers retain one waiting copy and block no others.
await caps(p,current.filter(c=>c!==wire.CapDirectTopicSync));await a.syncDirectTopics();copies=(await directCarriers(a,p)).filter(m=>m.state==='queued');
const waiting=copies.at(-1),sent=posts.length;await a.post(waiting);check((await a.store.get('outbox',waiting.id)).state==='waiting'&&posts.length===sent,'old reader receives no unknown subtype');
await a.syncDirectTopics();check((await directCarriers(a,p)).length===copies.length,'old reader accumulates no new carriers');
await caps(p);await a.post({...waiting,state:'queued'});check(posts.length===sent+1,'updated reader receives retained exact carrier');
// Retained copied task originals remain inert, independently of display placement.
for(let n=0;n<4;n++)await a.directHistory().step(p.me.devices.find(d=>d.address===p.address));
for(const r of (await a.store.all('outbox')).filter(m=>m.sub===wire.SubDeviceHistory&&m.to===p.address))await receive(r.envelope,p);
const copiedTask=await p.store.get('inbox',qid);
check(copiedTask?.kind==='task'&&copiedTask.replica&&!(copiedTask.state||copiedTask.attempts),'copied completed task stays inert after history and organization');

const c=await person('organizer/desk'),d=await sibling(c,'organizer/phone');
for(const e of [c,d])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
const agent=id(),differentAgent=id(),one=id(),two=id(),other=id(),childID=id();
const outgoing=(mid,at,agentID=agent,reply='')=>({v:1,id:mid,from:c.address,to:b.address,recipient_fp:b.fp,kind:'question',body:'original named exchange '+mid,at,ts:Math.floor(at/1000),state:'delivered',target:{address:b.address,fingerprint:b.fp,agent_id:agentID},...(reply?{reply_to:reply}:{})});
await c.store.write([{s:'outbox',k:one,v:outgoing(one,2000000)},{s:'outbox',k:childID,v:outgoing(childID,2001000,agent,one)},{s:'outbox',k:two,v:outgoing(two,2002000)},{s:'outbox',k:other,v:outgoing(other,2003000,differentAgent)}]);
const allOriginals=JSON.stringify(await c.store.all('outbox'));
for(const request of [{peer:b.address,ids:[one],topic:other},{peer:b.address,ids:[one,other],new:true,title:'Mixed agents'}]){denied=false;try{await c.previewTopicOrganization(request);}catch{denied=true;}check(denied,'different concrete agents cannot be merged or share a new topic');}
const partial=await c.previewTopicOrganization({peer:b.address,ids:[one],new:true,title:'Partial selection'});await c.applyTopicOrganization(partial);
check(partial.topic===partial.operation&&(await c.thread(partial.topic)).messages.length===1&&(await c.thread(childID)).topic.id===childID,'fresh topic identity is operation; unselected descendant stays outside');
await c.undoTopicOrganization({conv:'',peer:b.address,lid:partial.operation,id:id()});
const fresh=await c.previewTopicOrganization({peer:b.address,ids:[one,childID,two],new:true,title:'  Build   decisions  '});
const freshResult=await c.applyTopicOrganization(fresh),retry=await c.applyTopicOrganization(fresh);
check(freshResult.id===fresh.topic&&retry.id===freshResult.id,'new topic exact retry keeps one operation and topic identity');
thread=await c.thread(fresh.topic);
check(thread.topic.id===fresh.topic&&thread.topic.title==='Build decisions'&&thread.topic.agent_id===agent&&thread.messages.length===3,'named new topic navigates originals with stable concrete destination');
check((await c.threadSummaries()).length===2,'consolidation decreases topic count while other agent stays separate');
check(JSON.stringify((await c.store.all('outbox')).filter(r=>!r.aside))===allOriginals,'new topic preserves every original and its native target');
await c.syncDirectTopics();for(const r of await directCarriers(c,d))await receive(r.envelope,d);
for(let n=0;n<4;n++)await c.directHistory().step(d.me.devices.find(x=>x.address===d.address));
for(const r of (await c.store.all('outbox')).filter(m=>m.sub===wire.SubDeviceHistory&&m.to===d.address))await receive(r.envelope,d);
check((await d.thread(fresh.topic)).messages.length===3,'new-topic placements arriving before originals recover on linked device');
await c.syncTopicTitles();
for(const r of (await c.store.all('outbox')).filter(m=>m.sub===wire.SubTopicSync&&m.to===d.address))await receive(r.envelope,d);
check((await d.thread(fresh.topic)).topic.title==='Build decisions','private new-topic name converges independently of placement/history ordering');
const invalidScope={peer:b.address,id:other,author:c.fp,topic:fresh.topic,synthetic:true,destination:fresh.destination,revision:100,writer:d.fp};
await receive(await wire.seal({v:2,id:id(),from:d.address,to:c.address,ts:1,kind:'message',sub:wire.SubDirectTopicSync,replica:true,body:JSON.stringify({v:1,person:c.me.person,roster:c.me.hash,placements:[invalidScope]})},d.keys,c.pub),c);
check((await c.thread(other)).topic.id===other&&(await c.thread(fresh.topic)).messages.length===3,'authenticated own sync cannot move an original across concrete agents');

const next=await c.previewTopicOrganization({peer:b.address,ids:[one,childID,two],new:true,title:'Second new topic'});await c.applyTopicOrganization(next);
await c.undoTopicOrganization({conv:'',peer:b.address,lid:next.operation,id:id()});
check((await c.thread(fresh.topic)).messages.length===3,'undo restores an emptied synthetic source without inventing a message anchor');
if(typeof window!=='undefined'){
 const reopened=await openIDB(c.fixtureStoreName),saved=await reopened.get('kv','direct-topic-operation/'+fresh.operation);
 check(saved?.review.topic===fresh.topic,'IndexedDB reopen retains exact new-topic operation');
 const resumed=new Engine({store:reopened,base:'https://synthetic.invalid',fetch}),replay=await resumed.applyTopicOrganization(fresh);
 check(replay.id===fresh.operation,'new engine reuses persisted exact operation without repeating an undone effect');reopened.close();
}
console.log('direct topic browser checks',checks);
if(typeof window!=='undefined')window.directTopicsResult={ok:true,checks,storage:'IndexedDB'};
