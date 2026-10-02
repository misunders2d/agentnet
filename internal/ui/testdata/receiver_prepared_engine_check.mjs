// Real Engine/key/age/outbox transitions with isolated synthetic relay and host signer.
// Host setup acceptance/execution is deliberately not claimed by this fixture.
import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const now=1790000000123,posts=[],blobs=new Map(),publics=new Map(),profiles=new Map(),chains=new Map();
let holdUpload=null,holdMessage=null;
const json=(x,status=200)=>new Response(JSON.stringify(x),{status});
const fetch=async(url,options={})=>{
 const u=new URL(url);assert.equal(u.origin,'https://synthetic.invalid');const p=u.pathname;
 if(p==='/v1/version')return json({features:['env2','env3','person2','caps']});
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:(chains.get(p.split('/')[3])||[]).filter(r=>r.seq>Number(u.searchParams.get('after'))).map(r=>JSON.parse(wire.rosterJSON(r))),more:false});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/agents/'))return json({public:JSON.parse(wire.marshalPublic(publics.get(p.slice(11))))});
 if(p==='/v1/messages'){const env=wire.parseEnvelope(options.body);await wire.verifyEnvelope(env,publics.get(env.from).sign_key);if(holdMessage){const hold=holdMessage;holdMessage=null;hold.started();await hold.release;}posts.push(options.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p==='/v1/blobs'){const b=JSON.parse(options.body);if(!blobs.has(b.id))blobs.set(b.id,{...b,received:0,state:'uploading',ct:new Uint8Array(b.size)});return json(blobs.get(b.id));}
 if(p.startsWith('/v1/blobs/')){const b=blobs.get(p.split('/')[3]);if(p.endsWith('/complete'))b.state='stored';else if(options.method==='PUT'){if(holdUpload){const hold=holdUpload;holdUpload=null;hold.started();await hold.release;}const bytes=new Uint8Array(options.body);b.ct.set(bytes,b.received);b.received+=bytes.length;}return json(b);}
 throw Error('unexpected synthetic path '+p);
};
async function device(address){const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch,now:()=>now});e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);publics.set(address,e.pub);return e;}
const phone=await device('alice/phone'),laptop=await device('alice/laptop'),bob=await device('bob/desk'),mallory=await device('mallory/desk');
const first=await wire.newRoster(phone.keys,phone.address,'Alice'),join=await wire.joinConsent(laptop.keys,laptop.address,first.person,1,await wire.rosterHash(first)),linked=await wire.nextRoster(phone.keys,phone.address,first,[phone.pub,laptop.pub],join);
await wire.verifyNext(linked,first);
const bobRoster=await wire.newRoster(bob.keys,bob.address,'Bob'),malloryRoster=await wire.newRoster(mallory.keys,mallory.address,'Mallory');
for(const e of [phone,laptop,bob,mallory]){
 const steps=e===phone||e===laptop?[first,linked]:[e===bob?bobRoster:malloryRoster];e.me=await e.personRecord(steps,'self',null);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}},...[phone,laptop,bob,mallory].map(x=>({s:'pins',k:x.address,v:{address:x.address,json:wire.marshalPublic(x.pub),fingerprint:x.fp,pending:null}}))]);
 for(const other of [phone,bob,mallory]){const p=await e.personRecord(other===phone?[first,linked]:[other===bob?bobRoster:malloryRoster],(other===phone?first.person:other===bob?bobRoster.person:malloryRoster.person)===e.me.person?'self':'pinned',null);await e.store.write([{s:'persons',k:p.person,v:p}]);}
}
async function caps(e,names=[wire.CapReplyReceiver,wire.CapEnv2,wire.CapPerson]){const session=wire.newID();profiles.set(e.address,{live:true,sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))]});}
for(const e of [phone,laptop,bob,mallory])await caps(e);
const receiver={kind:'live_session',session_handle:'selected-Codex',host:{address:laptop.address,fingerprint:laptop.fp},on_close:{agent_id:'a'.repeat(32),instructions:'Exact local continuation',mode:'question'}};
const file={name:'selected.txt',bytes:new TextEncoder().encode('SELECTED BYTES')};
const sent=await phone.api('/api/send',{to:bob.address,kind:'question',body:'Frozen request <&>',files:[file],reply_receiver:receiver});
assert.equal(sent.state,'receiver_waiting');assert.equal(posts.length,1);
const delegation=await phone.store.get('outbox',wire.parseEnvelope(posts[0]).id),original=await phone.store.get('outbox',sent.id);
assert.equal(delegation.to,laptop.address);assert.equal(original.state,'receiver_waiting');assert.notEqual(original.attachments[0].blob.id,delegation.attachments[0].blob.id);
const opened=await wire.open(delegation.envelope,laptop.keys,laptop.address,phone.pub),setup=await wire.parseReceiverOperation(opened.body,opened.receiver_route,'',opened.attachments);
assert.equal(setup.request.body,original.body);assert.equal(setup.receiver.session_handle,receiver.session_handle);
assert.equal(new TextDecoder().decode(await wire.decryptFile(blobs.get(delegation.attachments[0].blob.id).ct,delegation.attachments[0],laptop.keys)),'SELECTED BYTES');
await phone.post(original);assert.equal(posts.length,1,'direct post cannot bypass ready');
async function ready(sender,patch={},detail=''){
 const route={...delegation.receiver_route,op:'ready',...patch};const raw=await wire.seal({v:1,id:wire.newID(),from:sender.address,to:phone.address,ts:Math.floor(now/1000),kind:'message',reply_to:route.delegation_id,body:wire.receiverOperationJSON({v:1,detail}),receiver_route:route},sender.keys,phone.pub);return raw;
}
async function receive(e,raw){await e.admit(raw,wire.parseEnvelope(raw));}
// Forged digest and wrong current-key sender cannot release.
const wrong=await ready(laptop,{request_digest:'f'.repeat(64)});await receive(phone,wrong);assert.equal((await phone.store.get('outbox',sent.id)).state,'receiver_waiting');assert.equal((await phone.store.get('held',wire.parseEnvelope(wrong).id)).reason,'invalid');
await assert.rejects(()=>ready(mallory),/committed host/);
const refusal=await ready(laptop,{},'Selected session unavailable');await receive(phone,refusal);assert.equal((await phone.store.get('outbox',sent.id)).detail,'Selected session unavailable');
// Reload retains exact frozen setup; valid ready release is one atomic store operation.
const restarted=new Engine({store:phone.store,base:phone.base,fetch,now:()=>now});await restarted.load();assert.equal(restarted.address,phone.address);
const accepted=await ready(laptop);await receive(restarted,accepted);const released=await restarted.store.get('outbox',sent.id);assert.equal(released.state,'queued');assert.equal(released.envelope,original.envelope);
restarted.connected=true;await restarted.flushOutbox();assert.equal(posts.filter(raw=>wire.parseEnvelope(raw).to===bob.address).length,1);
// Existing pinned prefix follows exactly one verified new-device step.
const oldAlice=await bob.personRecord([first],'pinned',null);await bob.store.write([{s:'persons',k:first.person,v:oldAlice}]);
chains.set(first.person,[first,linked]);
const posted=posts.find(raw=>wire.parseEnvelope(raw).to===bob.address);await receive(bob,posted);
assert.equal((await bob.store.get('persons',first.person)).hash,await wire.rosterHash(linked));const received=await bob.store.get('inbox',sent.id);assert.equal(received.receiver_route.host_key,laptop.fp);
assert.equal(new TextDecoder().decode(await wire.decryptFile(blobs.get(released.attachments[0].blob.id).ct,released.attachments[0],bob.keys)),'SELECTED BYTES');
await receive(restarted,accepted);await restarted.flushOutbox();assert.equal(posts.filter(raw=>wire.parseEnvelope(raw).to===bob.address).length,1,'duplicate ready never resends');
// Human answer reaches origin and exactly selected sibling via one encrypted batch.
const beforeReply=posts.length;await bob.replyV1(sent.id,'Exact human answer');
const replies=posts.slice(beforeReply).filter(raw=>wire.parseEnvelope(raw).kind==='answer');
assert.equal(replies.length,2);assert.deepEqual(new Set(replies.map(raw=>wire.parseEnvelope(raw).to)),new Set([phone.address,laptop.address]));
for(const raw of replies){const env=wire.parseEnvelope(raw),to=env.to===phone.address?phone:laptop,reply=await wire.open(raw,to.keys,to.address,bob.pub);assert.equal(reply.reply_to,original.receiver_route.request_ref);assert.equal(reply.body,'Exact human answer');assert.ok(!reply.receiver_route);}
// Native safe catalog contract uses existing encrypted query and verified own reply.
const query='/api/reply-sessions?host='+laptop.address+'&host_key='+laptop.fp;
const beforeCatalog=posts.length, pending=await phone.api(query);assert.equal(pending.status,'pending');assert.equal(pending.local,false);assert.equal(posts.length,beforeCatalog+1);
assert.equal((await phone.api(query)).status,'pending');assert.equal(posts.length,beforeCatalog+1,'repeated GET dedups outstanding request');
const catalogEnvelope=wire.parseEnvelope(posts.at(-1)),catalogRequest=await wire.open(posts.at(-1),laptop.keys,laptop.address,phone.pub);
const catalogBody=wire.receiverOperationJSON({v:1,sessions:[{handle:'selected-Codex',harness:'codex',label:'Exact selected session',active:true}]});
const catalogReply=await wire.seal({v:1,id:wire.newID(),from:laptop.address,to:phone.address,ts:Math.floor(now/1000),kind:'message',body:catalogBody,reply_to:catalogEnvelope.id,receiver_route:catalogRequest.receiver_route},laptop.keys,phone.pub);
await receive(phone,catalogReply);const catalog=await phone.api(query);assert.equal(catalog.status,'ready');assert.equal(catalog.at,Math.floor(now/1000));assert.equal(catalog.sessions[0].handle,'selected-Codex');
assert.deepEqual(await phone.api('/api/reply-sessions'),{host:phone.address,local:true,sessions:[]});
await assert.rejects(()=>phone.api('/api/reply-sessions?host='+laptop.address),/together/);
await assert.rejects(()=>phone.api('/api/reply-sessions?host='+laptop.address+'&host_key='+mallory.fp),/current device/);
// Ordinary DM uses the same prepared transition across both audiences.
const conv=await phone.newDM(bob.address),dmBefore=posts.length;
const dmSent=await phone.api('/api/dm/send',{conv,body:'Selected DM request',reply_receiver:receiver});assert.equal(dmSent.state,'receiver_waiting');
const dmDelegation=await phone.store.get('outbox',wire.parseEnvelope(posts.at(-1)).id);
assert.equal(posts.length,dmBefore+1);assert.equal(dmDelegation.receiver_setup.request.conv,conv);assert.equal(dmDelegation.receiver_setup.originals.length,2);
const dmReadyRoute={...dmDelegation.receiver_route,op:'ready'};
const dmReady=await wire.seal({v:1,id:wire.newID(),from:laptop.address,to:phone.address,ts:Math.floor(now/1000),kind:'message',reply_to:dmDelegation.id,body:wire.receiverOperationJSON({v:1}),receiver_route:dmReadyRoute},laptop.keys,phone.pub);
await receive(phone,dmReady);phone.connected=true;await phone.flushOutbox();
const dmRows=(await phone.store.all('outbox')).filter(r=>r.conv===conv);assert.equal(dmRows.length,2);for(const row of dmRows)assert.equal(row.state,'custody');
const dmBob=dmRows.find(r=>r.to===bob.address);await receive(bob,dmBob.envelope);const admittedDM=await bob.store.get('inbox',dmBob.id);assert.equal(admittedDM.receiver_route.request_ref,dmSent.lid);
const dmReply=await bob.sendDM({conv,body:'DM human response',reply_to:dmBob.id});const dmResponse=(await bob.store.all('outbox')).find(r=>r.id===dmReply.id);assert.equal(dmResponse.reply_to,dmSent.lid);
// Atomic failure stores neither ready receipt nor released original; exact retry recovers.
phone.connected=false;
const atomicSent=await phone.sendDirect({to:bob.address,kind:'question',body:'Atomic ready gate',reply_receiver:receiver}),atomicSetup=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.id===atomicSent.id),atomicRoute={...atomicSetup.receiver_route,op:'ready'};
const atomicReady=await wire.seal({v:1,id:wire.newID(),from:laptop.address,to:phone.address,ts:Math.floor(now/1000),kind:'message',reply_to:atomicSetup.id,body:wire.receiverOperationJSON({v:1}),receiver_route:atomicRoute},laptop.keys,phone.pub),atomicID=wire.parseEnvelope(atomicReady).id;
const write=phone.store.write.bind(phone.store);phone.store.write=async(ops,checks)=>{if(ops.some(o=>o.s==='inbox'&&o.k===atomicID))throw Error('synthetic ready transaction refusal');return write(ops,checks);};
await assert.rejects(()=>receive(phone,atomicReady),/transaction refusal/);
assert.equal((await phone.store.get('outbox',atomicSent.id)).state,'receiver_waiting');assert.equal(await phone.store.get('inbox',atomicID),undefined);assert.equal(await phone.store.get('receipts',atomicID),undefined);
phone.store.write=write;await receive(phone,atomicReady);assert.equal((await phone.store.get('outbox',atomicSent.id)).state,'queued');assert.equal((await phone.store.get('receipts',atomicID)).state,'delivered');
// Private setup never becomes a direct conversation, preview, or count.
const visible=await phone.v1Threads(),privateIDs=new Set([...(await phone.store.all('inbox')),...(await phone.store.all('outbox'))].filter(r=>r.aside).map(r=>r.id));
assert.ok(privateIDs.size>=4);assert.ok(visible.flat().every(r=>!privateIDs.has(r.id)));
assert.ok(visible.flat().some(r=>r.id===sent.id));assert.ok((await bob.thread(sent.id)).messages.some(r=>r.body==='Exact human answer'));
for(const t of await phone.threadSummaries()){assert.ok(!privateIDs.has(t.id));assert.ok(!t.title.includes('request_digest'));}
await assert.rejects(()=>phone.thread(delegation.id),/No message/);
// Missing or malformed successor proof holds; a previously proved removed host refuses.
const authRow={...received,id:wire.newID(),receiver_route:{...received.receiver_route,request_ref:''}};authRow.receiver_route.request_ref=authRow.id;
await bob.store.write([{s:'persons',k:first.person,v:oldAlice}]);chains.set(first.person,[first,{...linked,sig:new Uint8Array(64)}]);
await assert.rejects(()=>bob.receiverOriginAuthority(authRow,[]),e=>e.reason==='proof_pending');assert.equal((await bob.store.get('persons',first.person)).hash,oldAlice.hash);
chains.set(first.person,[first]);await assert.rejects(()=>bob.receiverOriginAuthority(authRow,[]),e=>e.reason==='proof_pending');
const removed=await wire.nextRoster(phone.keys,phone.address,linked,[phone.pub],null);await wire.verifyNext(removed,linked);
await bob.store.write([{s:'persons',k:first.person,v:await bob.personRecord([first,linked,removed],'pinned',null)}]);
await assert.rejects(()=>bob.receiverOriginAuthority(authRow,[]),e=>e.reason==='invalid');
await bob.store.write([{s:'persons',k:first.person,v:await bob.personRecord([first,linked],'pinned',null)}]);chains.set(first.person,[first,linked]);
// Current caps can disappear after ready: keep exact original waiting, no fallback.
const beforeCap=posts.length,oldProfile=profiles.get(bob.address);await caps(bob,[wire.CapEnv2,wire.CapPerson]);profiles.get(bob.address).caps.push(oldProfile.caps[0]);await phone.post(await phone.store.get('outbox',atomicSent.id));assert.equal((await phone.store.get('outbox',atomicSent.id)).state,'waiting');assert.equal(posts.length,beforeCap);
profiles.set(bob.address,oldProfile);await phone.post(await phone.store.get('outbox',atomicSent.id));assert.equal(posts.length,beforeCap+1);assert.equal((await phone.store.get('outbox',atomicSent.id)).state,'custody');
// A selected host lost from the pinned current own chain stays bound/refused.
const current=await restarted.store.get('kv','person');await restarted.store.write([{s:'kv',k:'person',v:{...current,devices:current.devices.filter(d=>d.address!==laptop.address)}}]);
await assert.rejects(()=>restarted.prepareReceiverRequest(receiver,{id:wire.newID(),to:bob.address,to_key:bob.fp,ts:Math.floor(now/1000),kind:'question',body:'refused'},[],[]),/current device/);
// Restore fixture roster before independent capability gate.
await restarted.store.write([{s:'kv',k:'person',v:current}]);
// Current signed capability, never an older overlapping session, is required.
await caps(bob,[wire.CapEnv2,wire.CapPerson]);await assert.rejects(()=>phone.sendDirect({to:bob.address,kind:'question',body:'no downgrade',reply_receiver:receiver}),/cannot receive selected/);
// Actual UI deletion clears ordinary private snapshots in the same local retention path.
await caps(bob,[wire.CapReplyReceiver,wire.CapEnv2,wire.CapPerson,wire.CapControl]);
const ordinary=await phone.sendDirect({to:bob.address,kind:'message',body:'DELETE ORDINARY PRIVATE SNAPSHOT',files:[file],reply_receiver:receiver});
const ordinarySetup=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.id===ordinary.id),custody=ordinarySetup.state;
// Same physical ID in a foreign scope or author is not this ordinary request.
const foreignScope={...structuredClone(ordinarySetup),id:wire.newID(),receiver_setup:{...structuredClone(ordinarySetup.receiver_setup),request:{...ordinarySetup.receiver_setup.request,conv:wire.newID(),lid:ordinary.id}}};
const foreignAuthor={...structuredClone(ordinarySetup),id:wire.newID(),receiver_setup:{...structuredClone(ordinarySetup.receiver_setup),request:{...ordinarySetup.receiver_setup.request,from_key:mallory.fp}}};
await phone.store.write([foreignScope,foreignAuthor].map(v=>({s:'outbox',k:v.id,v})));
const opposite={id:ordinary.id,v:1,from:bob.address,fp:bob.fp,kind:'message',body:'OPPOSITE DIRECTION SURVIVES',at:now,read:true};await phone.store.write([{s:'inbox',k:ordinary.id,v:opposite}]);
await phone.messageControl('delete',{id:ordinary.id,dir:'out'});
const redacted=await phone.store.get('outbox',ordinarySetup.id),redactedOriginal=await phone.store.get('outbox',ordinary.id);
assert.equal(redacted.state,custody,'published setup keeps truthful custody');assert.equal(redacted.body,'');assert.equal(redacted.receiver_setup.request.body,'');assert.deepEqual(redacted.receiver_setup.request.attachments,[]);assert.equal(redacted.files,undefined);assert.equal(redactedOriginal.state,'failed');assert.equal(redactedOriginal.receiver_redacted,true);
for(const f of [foreignScope,foreignAuthor])assert.equal((await phone.store.get('outbox',f.id)).body,f.body);assert.equal((await phone.store.get('inbox',ordinary.id)).body,opposite.body);
const deletedReadyRoute={...ordinarySetup.receiver_route,op:'ready'},deletedReady=await wire.seal({v:1,id:wire.newID(),from:laptop.address,to:phone.address,ts:Math.floor(now/1000),kind:'message',reply_to:ordinarySetup.id,body:wire.receiverOperationJSON({v:1}),receiver_route:deletedReadyRoute},laptop.keys,phone.pub);
await receive(phone,deletedReady);assert.equal((await phone.store.get('held',wire.parseEnvelope(deletedReady).id)).reason,'invalid');assert.equal((await phone.store.get('outbox',ordinary.id)).state,'failed');
const postsBeforeRetry=posts.length;await phone.post(ordinarySetup);await phone.post({...original,id:ordinary.id});assert.equal(posts.length,postsBeforeRetry,'stale held copies cannot resurrect deletion');
// An upload already awaiting I/O cannot write an old plaintext row over deletion.
let releaseUpload,uploadStarted;const started=new Promise(resolve=>uploadStarted=resolve),release=new Promise(resolve=>releaseUpload=resolve);holdUpload={started:uploadStarted,release};
const racing=phone.sendDirect({to:bob.address,kind:'message',body:'DELETE DURING UPLOAD',files:[file],reply_receiver:receiver});await started;
const racingSetup=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.body==='DELETE DURING UPLOAD'),racingID=racingSetup.receiver_setup.request.id;
await phone.messageControl('delete',{id:racingID,dir:'out'});const beforeRelease=posts.length;releaseUpload();const raced=await racing;
assert.equal(raced.state,'failed');assert.equal(posts.length,beforeRelease);assert.equal((await phone.store.get('outbox',racingSetup.id)).body,'');assert.equal((await phone.store.get('outbox',racingSetup.id)).files,undefined);assert.equal((await phone.store.get('outbox',racingID)).receiver_redacted,true);
// Once handover has started, a real custody response preserves that fact while retaining deletion.
let releaseMessage,messageStarted;const messageStart=new Promise(resolve=>messageStarted=resolve),messageRelease=new Promise(resolve=>releaseMessage=resolve);holdMessage={started:messageStarted,release:messageRelease};
const handing=phone.sendDirect({to:bob.address,kind:'message',body:'DELETE DURING CUSTODY',reply_receiver:receiver});await messageStart;
const handingSetup=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.body==='DELETE DURING CUSTODY'),handingID=handingSetup.receiver_setup.request.id;
await phone.messageControl('delete',{id:handingID,dir:'out'});releaseMessage();await handing;
const custodyAfterDelete=await phone.store.get('outbox',handingSetup.id);assert.equal(custodyAfterDelete.state,'custody');assert.equal(custodyAfterDelete.body,'');assert.ok(custodyAfterDelete.receiver_redacted);assert.equal((await phone.store.get('outbox',handingID)).state,'failed');
// The upload-progress write itself is atomic against a later retention transaction.
const progress=await phone.sendDirect({to:bob.address,kind:'message',body:'DELETE AT PROGRESS COMMIT',files:[file],reply_receiver:receiver}),progressSetup=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.id===progress.id),progressWrite=phone.store.write.bind(phone.store);
let progressRace=true;phone.store.write=async(ops,checks=[])=>{if(progressRace&&ops.some(o=>o.s==='outbox'&&o.k===progressSetup.id&&!o.v?.receiver_redacted)&&checks.some(c=>c.s==='outbox'&&c.k===progressSetup.id)){progressRace=false;const retentionChecks=[],drop=await phone.blankRetracted(await phone.store.get('outbox',progress.id),'outbox',retentionChecks);await progressWrite(drop,retentionChecks);}return progressWrite(ops,checks);};
assert.equal(await phone.receiverOutboxProgress(progressSetup),false);phone.store.write=progressWrite;assert.equal((await phone.store.get('outbox',progressSetup.id)).body,'');assert.equal((await phone.store.get('outbox',progress.id)).receiver_redacted,true);
// A conflict while recording proven custody retries metadata only, never transport.
let custodyConflict=true;phone.store.write=async(ops,checks=[])=>{if(custodyConflict&&ops.some(o=>o.s==='outbox'&&o.k===progressSetup.id)&&checks.some(c=>c.k===progressSetup.id)){custodyConflict=false;const current=await phone.store.get('outbox',progressSetup.id);await progressWrite([{s:'outbox',k:progressSetup.id,v:{...current,detail:'concurrent exact-scope retention'}}]);}return progressWrite(ops,checks);};
assert.equal(await phone.receiverOutboxProgress({...progressSetup,state:'custody'},true),true);phone.store.write=progressWrite;const custodyConflictRow=await phone.store.get('outbox',progressSetup.id);assert.equal(custodyConflictRow.state,'custody');assert.equal(custodyConflictRow.body,'');assert.ok(custodyConflictRow.receiver_redacted);
// Existing ordinary direct deletion clears its own text, preserving same-ID inbound.
const legacy=await phone.sendDirect({to:bob.address,kind:'message',body:'LEGACY ORDINARY DELETE'});await phone.store.write([{s:'inbox',k:legacy.id,v:{...opposite,id:legacy.id}}]);await phone.messageControl('delete',{id:legacy.id,dir:'out'});assert.equal((await phone.store.get('outbox',legacy.id)).body,'');assert.equal((await phone.store.get('inbox',legacy.id)).body,opposite.body);
// Existing Q/T execution baselines remain unchanged by message deletion.
for(const kind of ['question','task']){const q=await phone.sendDirect({to:bob.address,kind,body:'RETAIN '+kind,files:[file],reply_receiver:receiver}),d=(await phone.store.all('outbox')).find(r=>r.receiver_setup?.request.id===q.id);await phone.messageControl('delete',{id:q.id,dir:'out'});assert.equal((await phone.store.get('outbox',q.id)).body,'RETAIN '+kind);const kept=await phone.store.get('outbox',d.id);assert.equal(kept.body,d.body);assert.equal(kept.receiver_setup.request.body,'RETAIN '+kind);assert.ok(kept.receiver_setup.request.attachments.length);assert.ok(!kept.receiver_redacted);}
console.log('Engine prepared receiver ok: encrypted selected file, no original before ready, atomic restart/duplicate/chain/caps, private projection, exact ordinary redaction/upload race, Q/T baseline');
