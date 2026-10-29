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
const current=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapReminderSync].sort();
async function caps(e,names=current){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='reminders-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
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
const settle=async e=>{for(let i=0;i<4;i++){if(e.topicSyncRun)await e.topicSyncRun;if(e.topicMarkSyncRun)await e.topicMarkSyncRun;if(e.reminderSyncRun)await e.reminderSyncRun;}};
async function receive(raw,e){const env=wire.parseEnvelope(raw);await e.admit(raw,env);await settle(e);return env.id;}
const carriers=async(e,to)=>(await e.store.all('outbox')).filter(o=>o.sub===wire.SubReminderSync&&(!to||o.to===to.address));
const mark=async(e,scope,topic)=>await e.store.get('kv','topic/'+scope+'/'+topic)||{};
// deliver: every carrier from one device to another, as the relay would.
async function deliver(from,to){
 const rows=(await carriers(from,to)).filter(r=>r.state==='queued');
 for(const r of rows){await from.post(r);const after=await from.store.get('outbox',r.id);if(after.state==='custody')await receive(r.envelope,to);}
 return rows.length;
}

const a=await person('alice/laptop'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
check(current.includes(wire.CapReminderSync)&&!wire.RoomImplies.includes(wire.CapReminderSync),'rem1 is an explicit reader capability');
const id=wire.newID(),original={v:1,id,from:b.address,fp:b.fp,kind:'message',body:'Remember this exact original',read:false,at:Date.now()};
for(const e of [a,p])await e.store.write([{s:'inbox',k:id,v:original}]);
let threadMuteRefused=false;try{await a.api('/api/notify/mute',{conv:id,muted:true});}catch{threadMuteRefused=true;}
check(threadMuteRefused,'device-thread IDs cannot become DM/group notification mutes');
const pending=async e=>await e.reminders();
let due=Math.floor(Date.now()/1000)+3600;
const created=await a.api('/api/remind',{id,due});
check(created.note==='Reminder set.','existing API creates the personal reminder');
check(created.message===id&&created.reminder.message===id&&Date.parse(created.reminder.due)===due*1000&&!created.reminder.overdue&&Number.isSafeInteger(created.seq)&&created.seq<=a.seq,'existing action returns exact authoritative pending reminder and conservative sequence');await settle(a);
let rows=await carriers(a,p);check(rows.length===1&&rows[0].required_cap===wire.CapReminderSync&&rows[0].aside&&!wire.parseEnvelope(rows[0].envelope).attn,'one quiet encrypted explicitly gated carrier');
check(!(await carriers(a,b)).length,'no carrier to another person');await deliver(a,p);
check((await pending(p)).length===1&&Date.parse((await pending(p))[0].due)===due*1000,'create reaches linked phone');
check(!(await p.store.all('inbox')).some(r=>r.sub===wire.SubReminderSync),'metadata is never a timeline turn');
const stale=rows[0].envelope;
due+=1200;await p.api('/api/remind',{id,due});await settle(p);await deliver(p,a);
check(Date.parse((await pending(a))[0].due)===due*1000,'phone snooze updates exact laptop reminder');
const done=await a.api('/api/remind/done',{id});check(done.message===id&&done.reminder===null&&Number.isSafeInteger(done.seq),'done returns exact ended snapshot');await settle(a);await deliver(a,p);
check(!(await pending(p)).length&&(await p.reminders(true))[0].state==='done','dismissed reminder follows own devices');
await receive(stale,p);check(!(await pending(p)).length,'stale pending carrier never resurrects a tombstone');
await a.api('/api/remind',{id,due});await settle(a);await deliver(a,p);
const cancelled=await p.api('/api/remind/cancel',{id});check(cancelled.message===id&&cancelled.reminder===null&&Number.isSafeInteger(cancelled.seq),'cancel returns exact ended snapshot');await settle(p);await deliver(p,a);
check(!(await pending(a)).length,'cancel follows linked devices');
await a.api('/api/remind',{id,due});await settle(a);await deliver(a,p);
await p.sendDirect({to:b.address,kind:'message',body:'reply ends exact reminder',reply_to:id});await settle(p);await deliver(p,a);
check(!(await pending(a)).length&&(await a.reminders(true))[0].state==='replied','ordinary explicit reply ends reminder across devices');
// Sender may forward a fact, but only a current own-human carrier authorizes it.
const ref={conv:'',fingerprint:b.fp,lid:id,due,state:'pending',revision:Date.now()+100000,writer:b.fp};
const seal=async(sender,marks,target=p)=>wire.seal({v:wire.Version2,id:wire.newID(),from:sender.address,to:target.address,ts:Math.floor(Date.now()/1000),kind:'message',sub:wire.SubReminderSync,replica:true,body:JSON.stringify({v:1,person:a.me.person,roster:a.me.hash,reminders:marks})},sender.keys,target.pub);
const foreign=await receive(await seal(b,[ref]),p);check((await p.store.get('held',foreign))?.reason==='invalid'&&!(await pending(p)).length,'foreign person cannot create our reminder');
// Current authority is checked independently of an otherwise valid signed fact.
const r={person:a.me.person,roster:a.me.hash};
const pin=await p.store.get('pins',a.address);await p.store.write([{s:'pins',k:a.address,v:{...pin,pending:pin.json}}]);
let refused=false;try{await p.readSyncAuthority(r,a.address,a.fp,p.address,p.fp);}catch{refused=true;}check(refused,'pending sender key fails closed');await p.store.write([{s:'pins',k:a.address,v:pin}]);
const own=p.me;await p.store.write([{s:'kv',k:'person',v:{...own,human_keys:[p.fp]}}]);refused=false;try{await p.readSyncAuthority(r,a.address,a.fp,p.address,p.fp);}catch{refused=true;}check(refused,'agent-host sender cannot supply personal state');await p.store.write([{s:'kv',k:'person',v:own}]);
// Both delivery orders converge, and an explicit change advances a slower clock.
const [low,high]=[a.fp,p.fp].sort(),future=Date.now()+200000,first={...ref,writer:low,revision:future},last={...ref,state:'done',writer:high,revision:future};
await receive(await seal(a,[first]),p);await receive(await seal(a,[last]),p);await receive(await seal(p,[last],a),a);await receive(await seal(p,[first],a),a);
check((await a.reminders(true))[0].state==='done'&&(await p.reminders(true))[0].state==='done','concurrent reminders converge independent of arrival order');
await p.api('/api/remind',{id,due});await settle(p);check((await pending(p))[0].fact.revision===future+1,'explicit snooze advances beyond observed future generation');await deliver(p,a);
// Original may arrive after its reminder: preserve intent, do not guess a message.
const later=wire.newID(),before={...ref,lid:later,revision:future+500};await receive(await seal(a,[before]),p);
check((await pending(p)).length===1,'missing original has no fabricated visible row');await p.store.write([{s:'inbox',k:later,v:{...original,id:later,body:'Arrived later'}}]);
check((await pending(p)).length===2,'original arrival resolves durable exact intent');
// Older readers get one waiting record, while an ordinary send still progresses.
await caps(p,current.filter(c=>c!==wire.CapReminderSync));await a.api('/api/remind',{id,due:due+60});await settle(a);
rows=(await carriers(a,p)).filter(o=>o.state==='queued');const sent=posts.length;await a.post(rows.at(-1));
check((await a.store.get('outbox',rows.at(-1).id)).state==='waiting'&&posts.length===sent,'old reader receives no unknown carrier');
await a.api('/api/remind',{id,due:due+120});await settle(a);check((await carriers(a,p)).filter(o=>o.state==='waiting'||o.state==='queued').length===1,'old reader waiting does not accumulate a FIFO');
const ordinaryID=wire.newID(),ordinary=await wire.seal({v:1,id:ordinaryID,from:a.address,to:p.address,kind:'message',body:'ordinary still progresses',ts:Math.floor(Date.now()/1000)},a.keys,p.pub);
await a.store.write([{s:'outbox',k:ordinaryID,v:{id:ordinaryID,to:p.address,kind:'message',body:'ordinary still progresses',envelope:ordinary,at:Date.now(),state:'queued'}}]);await a.post(await a.store.get('outbox',ordinaryID));
check((await a.store.get('outbox',ordinaryID)).state==='custody'&&posts.length===sent+1,'unsupported reminder does not block ordinary delivery');
await caps(p);const parked=(await carriers(a,p)).find(o=>o.state==='waiting');await a.store.write([{s:'outbox',k:parked.id,v:{...parked,state:'queued'}}]);await deliver(a,p);await a.syncReminders();await deliver(a,p);
check(Date.parse((await pending(p)).find(r=>r.message===id).due)===(due+120)*1000,'updated reader catches up to latest intent');
// A local deadline is retained across restart and replay, once per winning generation.
p.now=()=>due*1000+3600000;p.running=true;await p.reminderWake();let ledger=await p.store.prefix('kv','reminder-alert/');check(ledger.length===2,'due reminders receive one local generation ledger each');
await p.reminderWake();check((await p.store.prefix('kv','reminder-alert/')).length===2,'repeated wake does not duplicate alerts');p.stop();
const reopenedStore=typeof window==='undefined'?p.store:(p.store.close(),await openIDB(p.fixtureStoreName));
const reopened=new Engine({store:reopenedStore,base:'https://synthetic.invalid',fetch});Object.assign(reopened,{keys:p.keys,address:p.address,fp:p.fp,me:p.me,now:p.now,running:true});await reopened.reminderWake();check((await reopenedStore.prefix('kv','reminder-alert/')).length===2,'restart retains notified generations');reopened.stop();
check((await reopenedStore.get('inbox',id)).read===false&&(await reopenedStore.get('inbox',id)).body===original.body,'reminder never changes the original or its read state');
const originalLookup=a.reminderOriginal;a.reminderOriginal=async()=>{throw Error('readback unavailable');};
const noteOnly=await a.api('/api/remind',{id,due:due+120});a.reminderOriginal=originalLookup;
check(noteOnly.note==='Reminder set.'&&!('reminder'in noteOnly)&&Date.parse((await pending(a)).find(r=>r.message===id).due)===(due+120)*1000,'readback failure returns note-only success without replaying or losing the saved reminder');
const prior=(await pending(a)).find(r=>r.message===id),expected={revision:prior.revision,writer:prior.writer};
check(expected.revision===prior.fact.revision&&expected.writer===prior.fact.writer,'browser exposes exact revision and writer for conditional UI changes');
const advanced=await a.api('/api/remind',{id,due:due+120,expected});
check(advanced.reminder.revision>expected.revision&&advanced.reminder.writer===a.fp,'same-due move advances the guarded generation');
for(const action of ['', '/done', '/cancel']){
 let refused=false;try{await a.api('/api/remind'+action,{id,due:due+240,expected});}catch(e){refused=/changed/.test(e.message);}
 check(refused,'stale '+(action||'move')+' rejects before storage');
 const current=(await pending(a)).find(r=>r.message===id);check(current.revision===advanced.reminder.revision&&current.due===advanced.reminder.due,'stale action preserves newer pending generation');
}
let wrongWriter=false;try{await a.api('/api/remind/done',{id,expected:{revision:advanced.reminder.revision,writer:b.fp}});}catch(e){wrongWriter=/changed/.test(e.message);}check(wrongWriter,'same numeric revision with different writer is not the selected generation');
await a.api('/api/remind/done',{id,expected:{revision:advanced.reminder.revision,writer:advanced.reminder.writer}});
const recreated=await a.api('/api/remind',{id,due:due+120,expected:{revision:0}});check(recreated.reminder?.message===id,'explicit absent-generation create may follow completion');
let duplicate=false;try{await a.api('/api/remind',{id,due:due+240,expected:{revision:0}});}catch(e){duplicate=/changed/.test(e.message);}check(duplicate,'absent-generation create cannot overwrite a newer pending reminder');
for(const e of engines)e.stop();
if(typeof window!=='undefined'){reopenedStore.close();for(const e of engines){e.store.close();await new Promise((resolve,reject)=>{const req=indexedDB.deleteDatabase(e.fixtureStoreName);req.onsuccess=resolve;req.onerror=()=>reject(req.error);});}window.reminderSyncResult={ok:true,checks};}
console.log('remindersync Engine checks: '+checks+' PASS');
