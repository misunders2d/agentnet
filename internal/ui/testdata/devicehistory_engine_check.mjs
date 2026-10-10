const assert={ok:(x,m)=>{if(!x)throw Error(m)},rejects:async(fn,re)=>{try{await fn()}catch(e){if(re.test(e.message))return;throw e}throw Error('Expected rejection '+re)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const blobs=new Map();
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[],acks=[],engines=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p==='/v1/blobs'){const b=JSON.parse(o.body);blobs.set(b.id,{...b,received:0,state:'uploading',bytes:new Uint8Array(b.size)});return json(blobs.get(b.id));}
 if(p.startsWith('/v1/blobs/')){const id=p.split('/')[3],b=blobs.get(id);if(p.endsWith('/data'))return new Response(b.bytes);if(o.method==='PUT'){const bytes=new Uint8Array(o.body);b.bytes.set(bytes,Number(u.searchParams.get('offset')));b.received+=bytes.length;}if(p.endsWith('/complete'))b.state='stored';return json(b);}
 if(p==='/v1/version')return json({features:['env2','env3','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);posts.push(o.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack')){acks.push({id:p.split('/')[3],body:JSON.parse(o.body)});return json(null,204);}
 throw Error('Unexpected synthetic route '+p);
};
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl]){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='direct-history-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
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
async function receive(raw,e){const env=wire.parseEnvelope(raw);await e.admit(raw,env);if(e.topicSyncRun)await e.topicSyncRun;return env.id;}
async function rootCopies(e,conv){return(await e.store.all('outbox')).filter(r=>r.sub===wire.SubRootSync&&(!conv||r.conv===conv));}

const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
const copies=async(e,sub=wire.SubDeviceHistory)=>(await e.store.all('outbox')).filter(r=>r.sub===sub);
const drain=async(e,target)=>{for(let i=0;i<8;i++){const more=await e.directHistory().step(target.me.devices.find(d=>d.address===target.address));if(!more)return;}throw Error('direct source did not finish');};
const receiveHistory=async(raw,e)=>{const id=await receive(raw,e);if(e.historyRun)await e.historyRun;return id;};
const id=wire.newID(),answer=wire.newID();
const original={id,from:a.address,to:b.address,ts:1,kind:'question',body:'original direct question',attachments:[]};
const envelope=await wire.seal(original,a.keys,b.pub);
await a.store.write([{s:'outbox',k:id,v:{...original,v:1,at:1000,fp:b.fp,envelope,state:'delivered'}}]);
const response=await wire.seal({id:answer,from:b.address,to:a.address,ts:2,kind:'answer',body:'original direct answer',reply_to:id,attachments:[]},b.keys,a.pub);
await receiveHistory(response,a);await drain(a,p);
const first=(await copies(a)).filter(r=>r.to===p.address);check(first.length===2,'old direct request and answer get exact inert own3 copies');
const readCarrier=async(refs)=>wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'message',sub:wire.SubReadSync,replica:true,body:JSON.stringify({v:1,person:a.me.person,roster:a.me.hash,refs})},a.keys,p.pub);
await receive(await readCarrier([{conv:'',fingerprint:b.fp,lid:answer}]),p);
for(const r of first)await receiveHistory(r.envelope,p);
check((await p.store.get('inbox',answer)).read,'signed read marker before direct history marks only exact original');
let thread=await p.thread(id);check(thread.peer===b.address&&thread.messages.length===2&&thread.messages[0].id===id&&thread.messages[0].dir==='out'&&thread.messages[0].from===a.address&&thread.messages[1].dir==='in','original target and request direction survive linked import');
check(thread.messages.every(m=>m.history&&m.synced_from===a.address&&!m.state)&&thread.messages[0].can.length===0,'copied requests retain provenance without executable state or sender-key edit authority');
for(const r of first)await receiveHistory(r.envelope,p);check((await p.thread(id)).messages.length===2,'duplicate encrypted history is one logical message');
await drain(p,a);check(!(await copies(p)).some(r=>r.to===a.address&&first.some(f=>JSON.parse(f.body).item.id===JSON.parse(r.body).item.id)),'history the exact desk forwarded is not echoed back to it');
const before=(await copies(a)).length;await drain(a,p);check((await copies(a)).length===before,'same wake retains exact ciphertext ledger');
const late=wire.newID(),lateEnv=await wire.seal({id:late,from:b.address,to:a.address,ts:1,kind:'answer',body:'late older original',reply_to:id},b.keys,a.pub);await receiveHistory(lateEnv,a);await drain(a,p);
const lateCopy=(await copies(a)).find(r=>JSON.parse(r.body).item.id===late);check(!!lateCopy,'late older arrival after completed snapshot is queued');await receiveHistory(lateCopy.envelope,p);check((await p.thread(id)).messages.length===3,'late history joins original direct thread');
check(!(await p.store.get('inbox',late)).read,'new unseen direct reply survives earlier read marker');
await receive(await readCarrier([{conv:'',fingerprint:b.fp,lid:late}]),p);
check((await p.store.get('inbox',late)).read,'signed read marker after direct history converges');
const originalSource=await a.directHistory().source(await a.store.get('outbox',id),true),replicaSource=await p.directHistory().source(await p.store.get('inbox',id),false);check(originalSource.hash===replicaSource.hash,'forwarded history preserves exact immutable content hash');
const body=JSON.parse(first[0].body),seal=async(sender,record,target=p)=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:target.address,ts:1,kind:'message',sub:wire.SubDeviceHistory,replica:true,body:JSON.stringify(record)},sender.keys,target.pub);
const bad={...body,item:{...body.item,body:'conflicting text'}};let held=await receiveHistory(await seal(a,bad),p);check((await p.store.get('held',held)).reason==='conflicting_copy','conflicting same original remains held');
held=await receiveHistory(await seal(b,body),p);check((await p.store.get('held',held)).reason==='invalid','foreign forwarder cannot import personal device history');
for(const mode of ['agent-host','removed','frozen','pending-pin','changed-pin']){
 const own=await p.store.get('kv','person'),pin=await p.store.get('pins',a.address),changed=mode==='agent-host'?{...own,human_keys:[p.fp]}:mode==='removed'?{...own,devices:own.devices.filter(d=>d.address!==a.address)}:mode==='frozen'?{...own,state:'conflict'}:own;
 await p.store.write([{s:'kv',k:'person',v:changed},{s:'pins',k:a.address,v:mode==='pending-pin'?{...pin,pending:{fingerprint:b.fp}}:mode==='changed-pin'?{...pin,fingerprint:b.fp}:pin}]);
 held=await receive(await seal(a,body),p);check(!!await p.store.get('held',held),'own authority fence '+mode);
 await p.store.write([{s:'kv',k:'person',v:own},{s:'pins',k:a.address,v:pin}]);
}
const oldCaps=[wire.CapEnv2,wire.CapPerson,wire.CapRoom,wire.CapOwnSyncV2];await caps(p,oldCaps);const postCount=posts.length;await a.post(first[0]);check(posts.length===postCount&&(await a.store.get('outbox',first[0].id)).state==='waiting','old own2 reader is not sent a new direct carrier');await caps(p);
const pin=await a.store.get('pins',p.address);await a.store.write([{s:'pins',k:p.address,v:{...pin,pending:{fingerprint:b.fp}}}]);await assert.rejects(()=>a.directHistory().gate(first[0]),/key changed/);checks++;await a.store.write([{s:'pins',k:p.address,v:pin}]);

// Exact file bytes use the existing retained-file/upload/offer path.
const bytes=new TextEncoder().encode('original encrypted direct file\n'),fileID=wire.newID();await a.keepSent([{name:'direct.txt',bytes}]);
const sealed=await wire.encryptFile(bytes,'direct.txt',b.pub),fileEnv=await wire.seal({id:fileID,from:a.address,to:b.address,ts:3,kind:'message',body:'file',attachments:[sealed.attachment]},a.keys,b.pub);
await a.store.write([{s:'outbox',k:fileID,v:{v:1,id:fileID,to:b.address,fp:b.fp,kind:'message',body:'file',at:3000,envelope:fileEnv,state:'delivered',attachments:[sealed.attachment]}}]);await drain(a,p);
const fileCopy=(await copies(a)).find(r=>JSON.parse(r.body).item.id===fileID);await receiveHistory(fileCopy.envelope,p);
check((await p.thread(fileID)).messages[0].files[0].availability==='requestable','synced outgoing file exposes requestable manifest');
const fileSource=await a.directHistory().source(await a.store.get('outbox',fileID),true),fileMessage={v:1,type:'offer',lid:fileID,author:a.fp,hash:fileSource.hash,index:0,name:'direct.txt',size:bytes.length,sha256:sealed.attachment.sha256};
const unsolicited=await wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'message',sub:wire.SubDeviceFile,replica:true,body:JSON.stringify({v:1,person:a.me.person,roster:a.me.hash,item:fileMessage})},a.keys,p.pub);
const unasked=await receive(unsolicited,p);check((await p.store.get('held',unasked))?.reason==='invalid','unsolicited file offer cannot claim a requested or stored file');
await p.requestFile(fileID,0);const request=(await copies(p,wire.SubDeviceFile)).at(-1);await receive(request.envelope,a);const serve=await a.store.get('kv','serve/'+request.id);await a.directHistory().serveFile(serve);
const offer=(await copies(a,wire.SubDeviceFile)).at(-1);check((await a.store.get('outbox',offer.id)).state==='custody','file offer passes normal encrypted upload and handover: '+JSON.stringify({state:(await a.store.get('outbox',offer.id)).state,detail:(await a.store.get('outbox',offer.id)).detail}));await receive(offer.envelope,p);const opened=await p.openFile(fileID,0,'out');check(new TextDecoder().decode(opened.bytes)===new TextDecoder().decode(bytes),'linked outgoing file opens exact original bytes');

const reoffer=await wire.seal({...await wire.open(offer.envelope,p.keys,p.address,a.pub),id:wire.newID()},a.keys,p.pub);
const offered=await receive(reoffer,p);check(!await p.store.get('held',offered)&&await p.store.get('receipts',offered),'reissued exact file offer acknowledges already stored bytes');

const edit=await a.sendDeviceControl(b.address,{id:fileID,fingerprint:a.fp},wire.SubRevision,JSON.stringify({rev:1,text:'edited direct file label'}));
if(a.historyRun)await a.historyRun;
const editCopy=(await copies(a)).find(r=>r.to===p.address&&JSON.parse(r.body).item.id===edit.id);
check(!!editCopy,'new direct edit wakes continuous own-history without reconnect');
await receiveHistory(editCopy.envelope,p);check((await p.thread(fileID)).messages[0].text==='edited direct file label','direct edit folds onto original author tuple on sibling');

if(typeof window!=='undefined'){await Promise.all(engines.flatMap(e=>[e.historyRun,e.readSyncRun,e.rootSyncRun,e.topicSyncRun,e.invitationSyncRun,e.archiveRun,e.historyProduction,e.syncOutboxPass].filter(Boolean)));a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});const count=(await copies(a)).length;await drain(restarted,p);check((await copies(a)).length===count,'restart resumes durable source cursor and copy ledger');
// E: authenticated own-device history is usable context for a fresh explicit
// proposal confirmation, without turning any imported row into execution.
{
 const proposalID=wire.newID(),suggestion='Write the report in Russian.\nSave report.md.';
 const response=await wire.seal({id:proposalID,from:b.address,to:a.address,ts:4,kind:'answer',status:wire.StatusProposal,body:suggestion,reply_to:id},b.keys,a.pub);
 await receiveHistory(response,a);await drain(a,p);
 const copy=(await copies(a)).find(r=>JSON.parse(r.body).item.id===proposalID);check(!!copy,'proposal joins ordinary own-device history');await receiveHistory(copy.envelope,p);
 const candidate=await p.proposalCandidate(proposalID);check(candidate.shared&&candidate.q.to===b.address&&candidate.p.body===suggestion,'phone binds original requester and exact foreign agent endpoints');
 const revised='Write the report in English.\nSave report.md.';await p.confirmProposal(proposalID,revised);
 const tasks=(await p.store.all('outbox')).filter(r=>r.kind==='task'&&r.reply_to===proposalID);check(tasks.length===1&&tasks[0].body===revised&&tasks[0].to===b.address&&tasks[0].proposal_choice.needs_new,'phone sends one fresh explicit revised task with own3 host gate');
 const task=await wire.open(tasks[0].envelope,b.keys,b.address,p.pub);check(task.body===revised&&task.from===p.address&&task.reply_to===proposalID,'signed task uses confirming phone identity and original proposal');
 await assert.rejects(()=>p.confirmProposal(proposalID),/different text/);checks++;
 check((await p.store.get('inbox',id)).history&&(await p.store.get('inbox',proposalID)).history,'original question/proposal stay inert after confirmation');
 const localView=(await p.thread(tasks[0].id)).messages.find(m=>m.id===tasks[0].id);check(localView.proposal?.edited&&localView.proposal.proposal===suggestion&&localView.proposal.task===revised,'browser shows original and edited outgoing provenance');
 if(p.historyRun)await p.historyRun;await drain(p,a);const back=(await copies(p)).find(r=>r.to===a.address&&JSON.parse(r.body).item.id===tasks[0].id);check(!!back,'chosen task returns through existing own history');await receiveHistory(back.envelope,a);
 check((await a.proposalActions(await a.store.get('inbox',proposalID))).length===0,'linked explicit choice removes original proposal action');
 const importedView=(await a.thread(tasks[0].id)).messages.find(m=>m.id===tasks[0].id);check(importedView.proposal?.edited&&importedView.proposal.confirmed_by===p.address&&importedView.proposal.task===revised&&!importedView.state,'synced edited provenance remains inert and attributed');
 await assert.rejects(()=>a.confirmProposal(proposalID),/different text/);checks++;

}
// Three own devices: preserve the agent host as peer even when both original
// endpoints are this human, and recover a reply delivered before its request.
{
 const host=await person('owner/host'),sender=await sibling(host,'owner/laptop'),reader=await sibling(host,'owner/tablet');
 const requestID=wire.newID(),replyID=wire.newID();
 const q=await wire.seal({id:requestID,from:sender.address,to:host.address,ts:1,kind:'question',body:'own laptop request'},sender.keys,host.pub);
 await receiveHistory(q,host);await host.store.write([{s:'inbox',k:requestID,v:{...await host.store.get('inbox',requestID),at:1000}}]);
 const reply={id:replyID,from:host.address,to:sender.address,ts:2,kind:'answer',reply_to:requestID,body:'own host answer'};
 await host.store.write([{s:'outbox',k:replyID,v:{...reply,v:1,at:2000,fp:sender.fp,state:'delivered',envelope:await wire.seal(reply,host.keys,sender.pub)}}]);
 await drain(host,reader);const cc=(await copies(host)).filter(r=>r.to===reader.address),answerCopy=cc.find(r=>JSON.parse(r.body).item.id===replyID),requestCopy=cc.find(r=>JSON.parse(r.body).item.id===requestID);
 const early=await receiveHistory(answerCopy.envelope,reader);check((await reader.store.get('held',early))?.reason==='proof_pending','own reply before original remains recoverably held');
 await receiveHistory(requestCopy.envelope,reader);await reader.retryHeld();if(reader.retrying)await reader.retrying;
 const view=await reader.thread(requestID);check(view.peer===host.address&&view.messages.length===2&&view.messages.find(m=>m.id===requestID)?.dir==='out'&&view.messages.find(m=>m.id===replyID)?.dir==='in','two own endpoints converge on original agent host with correct direction');
 check(!await reader.store.get('held',early)&&view.messages.every(m=>!m.state&&m.history),'dependency arrival recovers inert own reply');
 const progress={v:1,id:wire.newID(),lid:'',from:host.address,from_key:host.fp,ts:3,at:3000,kind:'message',status:wire.StatusProgress,agent_id:wire.newID(),reply_to:requestID,body:'Working'};progress.lid=progress.id;
 check(wire.parseHistory(wire.historyJSON(progress)).agent_id===progress.agent_id,'named progress history accepts the same signed shape as native');
}
// A legacy missing recipient key is not the current reader's key and cannot
// acquire proposal/status authority from a later duplicate's assertion.
{
 const source=await person('keys/source'),host=await sibling(source,'keys/host'),reader=await sibling(source,'keys/reader');
 const legacyID=wire.newID(),q={id:legacyID,from:source.address,to:host.address,ts:1,kind:'question',body:'legacy exact recipient key unavailable'};
 await source.store.write([{s:'outbox',k:legacyID,v:{...q,v:1,at:1000,state:'delivered',envelope:await wire.seal(q,source.keys,host.pub)}}]);
 await drain(source,reader);
 const copy=(await copies(source)).find(r=>r.to===reader.address&&JSON.parse(r.body).item.id===legacyID),record=JSON.parse(copy.body);
 check(!record.recipient_key,'legacy source exports unknown recipient key explicitly as absent');
 await receiveHistory(copy.envelope,reader);
 check((await reader.directHistory().original(legacyID,[])).recipient_key==='','imported unknown recipient key never becomes this reader key');
 await drain(reader,host);
 const onward=(await copies(reader)).find(r=>r.to===host.address&&JSON.parse(r.body).item.id===legacyID);
 check(onward&&!JSON.parse(onward.body).recipient_key,'onward encrypted export preserves unknown original recipient key');
 await receiveHistory(await seal(source,{...record,recipient_key:host.fp},reader),reader);
 check((await reader.directHistory().original(legacyID,[])).recipient_key==='','duplicate cannot enrich unknown key from a current pin');
 const statusID=wire.newID(),status={v:1,id:statusID,lid:statusID,from:host.address,from_key:host.fp,ts:2,at:2000,kind:'message',sub:wire.SubStatus,ref:{id:legacyID,fingerprint:source.fp},body:JSON.stringify({state:'needs_human',n:1,at:2,attempt:1})};
 const statusCarrier=await receiveHistory(await seal(source,{...record,recipient:source.address,recipient_key:source.fp,item:JSON.parse(wire.historyJSON(status))},reader),reader);
 const waiting=(await reader.thread(legacyID)).messages.find(m=>m.id===legacyID);
 check((await reader.store.get('held',statusCarrier))?.reason==='invalid'&&!waiting.exec&&!waiting.continuation,'duplicate cannot supply exact host binding for own resolution');
 const proposalID=wire.newID(),proposal={v:1,id:proposalID,lid:proposalID,from:host.address,from_key:host.fp,ts:3,at:3000,kind:'answer',status:wire.StatusProposal,reply_to:legacyID,body:'A proposal needing exact original host identity'};
 const proposalCarrier=await receiveHistory(await seal(source,{...record,recipient:source.address,recipient_key:source.fp,item:JSON.parse(wire.historyJSON(proposal))},reader),reader);
 check(!await reader.store.get('held',proposalCarrier)&&!!await reader.store.get('inbox',proposalID),'legacy answer remains visible inert history');
 await assert.rejects(()=>reader.proposalCandidate(proposalID),/exact original question is unavailable/);checks++;
 const knownID=wire.newID(),known={...record,item:{...record.item,id:knownID,lid:knownID},recipient_key:host.fp};
 await receiveHistory(await seal(source,known,reader),reader);
 const blank={...known};delete blank.recipient_key;await receiveHistory(await seal(source,blank,reader),reader);
 check((await reader.directHistory().original(knownID,[])).recipient_key===host.fp,'legacy blank duplicate cannot erase captured original key');
 const conflicting=await receiveHistory(await seal(source,{...known,recipient_key:reader.fp},reader),reader);
 check((await reader.store.get('held',conflicting))?.reason==='conflicting_copy'&&(await reader.directHistory().original(knownID,[])).recipient_key===host.fp,'conflicting nonempty duplicate stays held without replacing original key');
 await reader.deleteThread(host.address,knownID);
 const erasedConflict=await receiveHistory(await seal(source,{...known,recipient_key:reader.fp},reader),reader);
 check((await reader.store.get('held',erasedConflict))?.reason==='conflicting_copy'&&(await reader.store.get('inbox',knownID)).body==='','erased original also keeps exact recipient-key conflict fence');
}
// Explicit corrections from another own human device stay separate signed work.
{
 const owner=await person('followup/desk'),host=await person('followup-host/desk'),phone=await sibling(owner,'followup/phone');
 for(const e of [owner,phone])await e.store.write([{s:'persons',k:host.me.person,v:{...host.me,state:'pinned'}},{s:'pins',k:host.address,v:{address:host.address,json:wire.marshalPublic(host.pub),fingerprint:host.fp,pending:null}}]);
 const oid=wire.newID(),original={v:1,id:oid,from:owner.address,to:host.address,ts:1,kind:'question',body:'Write contribution-margin.md in Russian'};
 const originalEnvelope=await wire.seal(original,owner.keys,host.pub);
 await owner.store.write([{s:'outbox',k:oid,v:{...original,at:1000,fp:host.fp,envelope:originalEnvelope,state:'delivered'}}]);
 await drain(owner,phone);for(const r of await copies(owner))await receiveHistory(r.envelope,phone);
 const ref={id:oid,fingerprint:owner.fp},id=wire.newID(),bytes=new TextEncoder().encode('English terminology'),files=[{name:'terms.md',bytes,size:bytes.length}];
 const note=await phone.queueRequestFollowup({ref,id,body:'Use English instead',files});
 const saved=await phone.store.get('outbox',id),opened=await wire.open(saved.envelope,host.keys,host.address,phone.pub);
 check(note.includes('queued')&&note.includes('not proven native acceptance')&&opened.followup.id===oid&&opened.followup.fingerprint===owner.fp&&opened.kind==='question'&&opened.to===host.address&&opened.reply_to===oid,'linked correction retains exact original human, host, kind and explicit queued state');
 check(opened.attachments.length===1&&opened.attachments[0].sha256===wire.hex(await wire.sha256(bytes)),'correction retains its exact attachment manifest');
 await assert.rejects(()=>phone.followupDeliveryGate(saved),/cannot safely queue bound request follow-ups/);checks++;
 await phone.post(saved);if(phone.outboxPass)await phone.outboxPass;check((await phone.store.get('outbox',id)).state==='waiting','unsupported receiver retains a queued waiting correction, never an ordinary request');
 await caps(host,[wire.CapEnv2,wire.CapPerson,wire.CapRoom,wire.CapRequestFollowup]);await phone.followupDeliveryGate(saved);checks++;
 check((await phone.queueRequestFollowup({ref,id,body:'Use English instead',files})).includes('already queued')&&(await phone.store.get('outbox',id)).envelope===saved.envelope,'identical retry retains one exact sealed correction');
 await assert.rejects(()=>phone.queueRequestFollowup({ref,id,body:'Do different work',files}),/different follow-up/);checks++;
 await assert.rejects(()=>phone.queueRequestFollowup({ref,id,body:'Use English instead',files:[{name:'terms.md',size:1,bytes:new Uint8Array([1])}]}),/different saved attachments/);checks++;
 await receive(saved.envelope,host);check((await host.store.get('inbox',id)).followup.id===oid,'signed received correction retains explicit intent');
 await drain(phone,owner);const history=(await copies(phone)).find(r=>wire.parseDeviceHistory(r.body).item.id===id);check(!!history,'new correction gets onward inert history');await receiveHistory(history.envelope,owner);
 check((await owner.store.get('inbox',id)).followup.id===oid&&(await owner.store.get('inbox',id)).replica,'onward copy retains provenance without executing work');
 const pin=await phone.store.get('pins',host.address);await phone.store.write([{s:'pins',k:host.address,v:{...pin,pending:wire.marshalPublic(owner.pub)}}]);
 await assert.rejects(()=>phone.queueRequestFollowup({ref,id:wire.newID(),body:'Late change'}),/key changed/);checks++;
 check((await owner.store.get('outbox',oid)).body===original.body,'correction never edits or reruns the original request');
}
// An original's exact direct recipient gets the original itself: it gets a
// copy only when this device's send state says it missed the original.
{
 const direct=async state=>{const did=wire.newID(),m={id:did,from:a.address,to:p.address,ts:5,kind:'question',body:'to the phone itself, '+state};await a.store.write([{s:'outbox',k:did,v:{...m,v:1,at:5000,fp:p.fp,recipient_fp:p.fp,envelope:await wire.seal(m,a.keys,p.pub),state}}]);return did;};
 const copied=async did=>(await copies(a)).filter(r=>r.to===p.address&&JSON.parse(r.body).item.id===did).length;
 const pendingDirect=async did=>!!await a.store.get('kv','device-history/pending/'+p.fp+'/out/'+did);
 const transit=await direct('custody'),stored=await direct('delivered'),expired=await direct('expired');
 await drain(a,p);
 check(await copied(transit)===0&&await pendingDirect(transit),'original in transit to its own recipient stays pending, not copied');
 check(await copied(stored)===0&&!await pendingDirect(stored),'delivered original is not copied to its own recipient');
 check(await copied(expired)===1,'expired original is copied to the recipient that missed it');
 await a.store.write([{s:'outbox',k:transit,v:{...await a.store.get('outbox',transit),state:'not_delivered'}}]);a.historyWake=(a.historyWake||0)+1;await drain(a,p);
 check(await copied(transit)===1&&!await pendingDirect(transit),'a later miss is copied from the pending sweep');
}
// A copy never goes back to the own device that forwarded it here, also when
// neither its sender nor its recipient is that device (it came there from a
// third own device): FLOOD-6, native parity.
{
 const desk=await person('via/desk'),phone=await sibling(desk,'via/phone'),tablet=await sibling(desk,'via/tablet'),host=await person('via-host/desk');
 const steps=await Promise.all(chains.get(desk.me.person).map(r=>wire.parseRoster(r)));phone.me=await phone.personRecord(steps,'self',phone.me);await phone.store.write([{s:'kv',k:'person',v:phone.me}]);await phone.pinDevices(phone.me);
 for(const e of [desk,phone,tablet])await e.store.write([{s:'persons',k:host.me.person,v:{...host.me,state:'pinned'}},{s:'pins',k:host.address,v:{address:host.address,json:wire.marshalPublic(host.pub),fingerprint:host.fp,pending:null}}]);
 const qid=wire.newID(),question={id:qid,from:tablet.address,to:host.address,ts:6,kind:'question',body:'asked on the tablet',attachments:[]};
 await tablet.store.write([{s:'outbox',k:qid,v:{...question,v:1,at:6000,fp:host.fp,envelope:await wire.seal(question,tablet.keys,host.pub),state:'delivered'}}]);
 await receiveHistory(await wire.seal({id:wire.newID(),from:host.address,to:tablet.address,ts:7,kind:'answer',body:'answered to the tablet',reply_to:qid,attachments:[]},host.keys,tablet.pub),tablet);
 const forward=async(from,to)=>{await drain(from,to);const sent=(await copies(from)).filter(r=>r.to===to.address);for(const r of sent)await receiveHistory(r.envelope,to);return sent.length;};
 check(await forward(tablet,desk)===2&&await forward(desk,phone)===2,'tablet history reaches the phone through the desk');
 await drain(phone,desk);
 check((await copies(phone)).filter(r=>r.to===desk.address).length===0,'the phone sends no copy back to the desk that forwarded it');
}
// A disconnected own device gets a bounded durable window. Receipt-driven
// refill must eventually copy every cursor-covered original without duplicates.
{
 const desk=await person('paced/desk'),phone=await sibling(desk,'paced/phone'),peer=await person('paced-peer/desk');
 await desk.store.write([{s:'persons',k:peer.me.person,v:{...peer.me,state:'pinned'}},{s:'pins',k:peer.address,v:{address:peer.address,json:wire.marshalPublic(peer.pub),fingerprint:peer.fp,pending:null}}]);
 const originals=[];
 for(let i=0;i<500;i++){
  const id=wire.newID(),m={id,from:desk.address,to:peer.address,ts:i+1,kind:'message',body:'durable backlog '+i,attachments:[]};originals.push(id);
  await desk.store.write([{s:'outbox',k:id,v:{...m,v:1,at:(i+1)*1000,fp:peer.fp,envelope:await wire.seal(m,desk.keys,peer.pub),state:'delivered'}}]);
 }
 const dev=phone.me.devices.find(d=>d.address===phone.address),active=async()=>(await copies(desk)).filter(r=>r.to===phone.address&&['queued','waiting','custody'].includes(r.state));
 await desk.directHistory().step(dev);
 check((await active()).length===50,'offline direct catchup is bounded to one exact-recipient window');
 check(!await desk.directHistory().step(dev),'full window sleeps instead of producing another page');
 const batch=await active();await desk.store.write(batch.map(r=>({s:'outbox',k:r.id,v:{...r,state:'custody'}})));
 check(!await desk.directHistory().step(dev),'custody still occupies offline recipient capacity');
 // Let dispatch own the real history wake. Other source producers are empty;
 // no polling/manual cursor resets are used during terminal receipt refills.
 desk.me=await desk.store.get('kv','person');let seq=0;
 for(let rounds=0;rounds<20;rounds++){
  const pending=await active();if(!pending.length)break;
  for(const row of pending)await desk.dispatch('receipt',JSON.stringify({id:row.id,state:'delivered',seq:++seq}));
  if(desk.historyRun)await desk.historyRun;
  check((await active()).length<=50,'receipt refill retains exact-recipient window');
 }
 const all=(await copies(desk)).filter(r=>r.to===phone.address),ids=all.map(r=>JSON.parse(r.body).item.id);
 check(ids.length===500&&new Set(ids).size===500&&originals.every(id=>ids.includes(id)),'receipt resume completely catches up all 500 durable originals once');
 check((await active()).length===0,'completed catchup has no outstanding carrier');
 // Freeze an actual sync HTTP POST while a late interactive insertion gets
 // its own live sender. The relay fixture still verifies sealed envelopes.
 await desk.store.write(all.map(r=>({s:'outbox',k:r.id,v:{...r,state:'queued'}})));
 let started,release;const entered=new Promise(r=>started=r),stalled=new Promise(r=>release=r),lanePosts=[];let firstSync=true;
 desk.fetch=async(url,opts)=>{const u=new URL(url);if(u.pathname==='/v1/messages'){const env=wire.parseEnvelope(opts.body);await wire.verifyEnvelope(env,pubs.get(env.from).sign_key);lanePosts.push({id:env.id,lane:u.searchParams.get('lane')});if(firstSync&&u.searchParams.get('lane')==='sync'){firstSync=false;started();await stalled;}return json({state:'custody'});}return fetch(url,opts);};
 desk.connected=true;const flushing=desk.flushOutbox();await entered;
 const fresh=wire.newID(),msg={id:fresh,from:desk.address,to:peer.address,ts:501,kind:'message',body:'late live request',attachments:[]};
 await desk.store.write([{s:'outbox',k:fresh,v:{...msg,v:1,at:501000,fp:peer.fp,envelope:await wire.seal(msg,desk.keys,peer.pub),state:'queued'}}]);
 await desk.flushOutboxLane(false);
 check(lanePosts.some(r=>r.id===fresh&&r.lane===null),'late live actual POST completes while first sync POST is stalled');
 check(lanePosts.filter(r=>r.lane==='sync').length===1,'stalled sync loop never blocks live nor launches an unbounded parallel batch');
 release();await flushing;desk.connected=false;
 check((await desk.store.get('outbox',fresh)).state==='custody','interactive send preserves truthful custody');

}
// Archive-capable browsers both contribute and import inert direct history.
// Bulk upload is deliberately stalled while a fresh live POST succeeds.
{
 const desk=await person('archive/desk'),phone=await sibling(desk,'archive/phone'),peer=await person('archive-peer/desk');
 await caps(phone,[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl,wire.CapHistoryArchive]);
 await caps(desk,[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl,wire.CapHistoryArchive]);
 await desk.store.write([{s:'persons',k:peer.me.person,v:{...peer.me,state:'pinned'}},{s:'pins',k:peer.address,v:{address:peer.address,json:wire.marshalPublic(peer.pub),fingerprint:peer.fp,pending:null}}]);
 await phone.store.write([{s:'persons',k:peer.me.person,v:{...peer.me,state:'pinned'}},{s:'pins',k:peer.address,v:{address:peer.address,json:wire.marshalPublic(peer.pub),fingerprint:peer.fp,pending:null}}]);
 const unique=wire.newID(),fromPhone={id:unique,from:phone.address,to:peer.address,ts:102,kind:'message',body:'unique phone history',attachments:[]};
 await phone.store.write([{s:'outbox',k:unique,v:{...fromPhone,v:1,at:102000,fp:peer.fp,envelope:await wire.seal(fromPhone,phone.keys,peer.pub),state:'delivered'}}]);
 const originals=[];
 for(let i=0;i<100;i++){const id=wire.newID(),m={id,from:desk.address,to:peer.address,ts:i+1,kind:'message',body:'archived '+i,attachments:[]};originals.push(id);await desk.store.write([{s:'outbox',k:id,v:{...m,v:1,at:(i+1)*1000,fp:peer.fp,envelope:await wire.seal(m,desk.keys,peer.pub),state:'delivered'}}]);}
 let entered,release;const started=new Promise(r=>entered=r),stalled=new Promise(r=>release=r);let once=true;
 desk.fetch=async(url,o)=>{if(once&&o.method==='PUT'&&new URL(url).pathname.startsWith('/v1/blobs/')){once=false;entered();await stalled;}return fetch(url,o);};
 await desk.reconcileHistory();desk.connected=true;await desk.directHistory().step(phone.me.devices.find(d=>d.address===phone.address));await Promise.race([started,desk.archiveRun.then(()=>{throw Error('Archive upload did not start');})]);
 const staged=(await copies(desk)).filter(r=>r.state==='archive_staged').sort((a,b)=>(a.send_order??a.at)-(b.send_order??b.at)||a.id.localeCompare(b.id));check(staged.length===50,'archive bootstrap atomically stages one bounded page instead of individual POSTs');
 check(JSON.parse(staged[0].body).item.id===originals.at(-1),'browser archive bootstrap selects newest originals first');
 const stagedOverview=(await desk.overview()).history?.find(row=>row.device===phone.address);check(stagedOverview?.queued===50&&!stagedOverview.retained&&!stagedOverview.blocked,'overview reports staged archive children as queued');
 const fresh=wire.newID(),m={id:fresh,from:desk.address,to:peer.address,ts:101,kind:'message',body:'live during archive upload'};
 const live={...m,v:1,at:101000,fp:peer.fp,envelope:await wire.seal(m,desk.keys,peer.pub),state:'queued'};await desk.store.write([{s:'outbox',k:fresh,v:live}]);await desk.post(live);
 check((await desk.store.get('outbox',fresh)).state==='custody','blocked archive upload cannot block fresh live POST');
 await desk.directHistory().step(phone.me.devices.find(d=>d.address===phone.address));
 const freshMirror=(await copies(desk)).find(r=>JSON.parse(r.body).item.id===fresh);
 check(freshMirror?.fresh_live&&!freshMirror.archive_chunk,'full cold window still produces a live own-device mirror');
 await desk.flushOutboxLane(false);check((await desk.store.get('outbox',freshMirror.id)).state==='custody','same-phone fresh mirror posts while its archive upload is blocked');
 await receive(freshMirror.envelope,phone);check((await phone.store.get('inbox',fresh))?.body===m.body,'same-phone fresh mirror is admitted before blocked cold archive upload finishes');
 // The same browser can receive an archive while its outgoing upload stalls.
 phone.connected=true;await phone.directHistory().step(desk.me.devices.find(d=>d.address===desk.address));await phone.archives().wake();if(phone.archiveRun)await phone.archiveRun;if(phone.syncOutboxPass)await phone.syncOutboxPass;
 const reverse=(await phone.store.outboxSub(wire.SubHistoryArchive))[0];check(!!reverse,'a browser owning unique history also contributes an archive');
 await receive(reverse.envelope,desk);if(desk.archiveImportRun)await desk.archiveImportRun;
 check((await desk.store.get('inbox',unique))?.body===fromPhone.body,'incoming archive imports while same-browser archive upload is still blocked');
 phone.connected=false;


 check(!posts.some(raw=>staged.some(r=>r.id===wire.parseEnvelope(raw).id)),'staged child envelopes never become bootstrap relay POSTs');
 desk.connected=false;release();if(desk.archiveRun)await desk.archiveRun;if(desk.syncOutboxPass)await desk.syncOutboxPass;
 const descriptors=async()=>(await desk.store.all('outbox')).filter(r=>r.sub===wire.SubHistoryArchive);
 const firstArchive=(await descriptors())[0];check(firstArchive&&firstArchive.files[0].uploaded&&!firstArchive.files[0].ct,'archive descriptor is enqueued only after upload is complete');
 await caps(phone);await desk.post(firstArchive);
 check((await desk.store.get('outbox',firstArchive.id)).state==='waiting','published archive waits quietly when ha1 disappears');
 const retrySource=new Engine({store:desk.store,base:desk.base,fetch});Object.assign(retrySource,{keys:desk.keys,address:desk.address,pub:desk.pub,fp:desk.fp,me:desk.me});
 await caps(phone,[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl,wire.CapHistoryArchive]);
 retrySource.connected=true;await retrySource.flushOutboxOnce();retrySource.connected=false;
 const retryDescriptor=await desk.store.get('outbox',firstArchive.id);desk.connected=true;
 check(retryDescriptor.state==='custody'&&retryDescriptor.envelope===firstArchive.envelope&&retryDescriptor.id===firstArchive.id,'ha1 return after restart resumes exact published archive signature and ID');

 phone.connected=false;await receive(firstArchive.envelope,phone);
 check(!(await phone.store.all('receipts')).some(r=>r.id===firstArchive.id),'saving descriptor alone does not claim delivery');
 const sourcePin=await phone.store.get('pins',desk.address),ownRoster=await phone.store.get('kv','person');
 await phone.store.write([{s:'pins',k:desk.address,v:{...sourcePin,pending:{fingerprint:peer.fp}}}]);phone.connected=true;await phone.archives().wake();phone.connected=false;
 check(!(await phone.store.get('kv','history-archive/in/'+firstArchive.id)).retained,'changed source key blocks archive retention and admission');
 await phone.store.write([{s:'pins',k:desk.address,v:sourcePin},{s:'kv',k:'person',v:{...ownRoster,devices:ownRoster.devices.filter(d=>d.address!==desk.address)}}]);phone.connected=true;await phone.archives().wake();phone.connected=false;
 check(!(await phone.store.get('kv','history-archive/in/'+firstArchive.id)).retained,'removed own source blocks archive retention and admission');
 await phone.store.write([{s:'kv',k:'person',v:ownRoster}]);

 const archiveBlob=blobs.get(firstArchive.attachments[0].blob.id),goodCipher=archiveBlob.bytes;archiveBlob.bytes=goodCipher.slice();archiveBlob.bytes[0]^=1;
 phone.connected=true;await phone.archives().wake();phone.connected=false;
 check(!(await phone.store.get('kv','history-archive/in/'+firstArchive.id)).retained&&!(await phone.store.all('receipts')).some(r=>r.id===firstArchive.id),'corrupt archive ciphertext is neither retained nor acknowledged');
 archiveBlob.bytes=goodCipher;

 // Pause after one admitted record, then resume with a fresh Engine instance.
 const originalImport=phone.importArchiveChild.bind(phone);let imports=0;
 phone.importArchiveChild=async(...args)=>{await originalImport(...args);if(++imports===1)phone.closing=true;};phone.connected=true;await phone.archives().wake();
 check((await phone.store.get('kv','history-archive/in/'+firstArchive.id)).index===1,'archive import persists bounded progress before interruption');
 check((await phone.store.get('files','archive/'+firstArchive.id)).ct.length>0,'descriptor is retained durably before acknowledgement');
 check((await phone.store.get('kv','history-archive/in/'+firstArchive.id)).retained,'retained archive may acknowledge independently of unfinished import');
 const resumed=new Engine({store:phone.store,base:'https://synthetic.invalid',fetch});Object.assign(resumed,{keys:phone.keys,address:phone.address,pub:phone.pub,fp:phone.fp,me:phone.me});await resumed.archives().wake();phone.closing=false;phone.connected=false;
 check((await phone.store.get('kv','history-archive/in/'+firstArchive.id)).state==='done','fresh Engine resumes interrupted import from durable ciphertext without cursor loss');
 check(!await phone.store.get('files','archive/'+firstArchive.id),'completed import releases duplicate archive ciphertext');
 await receive(firstArchive.envelope,phone);check((await phone.store.get('kv','history-archive/in/'+firstArchive.id)).state==='done','descriptor duplicate after cleanup never downloads or reimports history');
 const childIDs=new Set(staged.map(r=>r.id));check(!(await phone.store.all('receipts')).some(r=>childIDs.has(r.id)),'embedded children never fabricate relay receipts');
 check((await phone.store.all('inbox')).filter(r=>r.device_history&&originals.includes(r.id)).length===50,'archive child admission preserves exact inert originals');
 const relayChild=staged[0],tampered=JSON.parse(relayChild.envelope);tampered.ct=wire.b64(new Uint8Array([1,2,3]));
 phone.connected=true;await phone.onMessage(JSON.stringify(tampered));await phone.flushReceipts();
 check(!acks.some(a=>a.id===relayChild.id)&&!await phone.store.get('receipts',relayChild.id),'tampered same-ID relay delivery of imported child creates no acknowledgement');
 await wire.open(relayChild.envelope,phone.keys,phone.address,desk.pub);
 await phone.onMessage(relayChild.envelope);await phone.flushReceipts();
 check(acks.filter(a=>a.id===relayChild.id).length===1&&acks.find(a=>a.id===relayChild.id).body.state==='delivered','valid actual relay delivery of imported child is acknowledged once');
 await phone.onMessage(JSON.stringify(tampered));await phone.onMessage(JSON.stringify({...tampered,unknown:true}));await phone.flushReceipts();phone.connected=false;
 check(acks.filter(a=>a.id===relayChild.id).length===1&&!await phone.store.get('held',relayChild.id),'tampered duplicate after valid child delivery neither acknowledges nor replaces admission');

 const receiptWrite=desk.store.write.bind(desk.store);let receiptRace=true;
 desk.store.write=async(ops,checks)=>{
  if(receiptRace&&ops.some(o=>o.s==='outbox'&&o.v?.state==='archive_accepted')){
   receiptRace=false;const current=await desk.store.get('outbox',relayChild.id);
   await receiptWrite([{s:'outbox',k:current.id,v:{...current,detail:'concurrent child retry metadata'}}]);
  }
  return receiptWrite(ops,checks);
 };
 await desk.dispatch('receipt',JSON.stringify({id:firstArchive.id,state:'delivered',seq:1}));if(desk.archiveReceiptRun)await desk.archiveReceiptRun;if(desk.historyRun)await desk.historyRun;if(desk.archiveRun)await desk.archiveRun;if(desk.syncOutboxPass)await desk.syncOutboxPass;
 desk.store.write=receiptWrite;check((await desk.store.get('outbox',relayChild.id)).detail==='concurrent child retry metadata','archive receipt CAS preserves concurrent exact child state and mapping');
 const acceptedOverview=(await desk.overview()).history?.find(row=>row.device===phone.address);check(acceptedOverview?.retained===50&&!acceptedOverview.blocked&&!acceptedOverview.delivered,'overview reports accepted archive children as retained, without delivery or import claims');
 check((await Promise.all(staged.map(r=>desk.store.get('outbox',r.id)))).every(r=>!['queued','custody','delivered'].includes(r.state)),'archive staging never pretends individual transport delivery');
 const secondArchive=(await descriptors()).find(r=>r.id!==firstArchive.id);check(!!secondArchive,'descriptor receipt resumes the next bounded bootstrap chunk');
 phone.connected=true;await receive(secondArchive.envelope,phone);if(phone.archiveRun)await phone.archiveRun;phone.connected=false;
 check((await Promise.all(originals.map(id=>phone.store.get('inbox',id)))).every(Boolean),'archive receipt-driven catchup imports all 100 exact originals');
 await desk.dispatch('receipt',JSON.stringify({id:secondArchive.id,state:'delivered',seq:2}));if(desk.archiveReceiptRun)await desk.archiveReceiptRun;if(desk.historyRun)await desk.historyRun;if(desk.archiveRun)await desk.archiveRun;
 const before=(await copies(desk)).length;desk.historyWake=(desk.historyWake||0)+1;await desk.directHistory().step(phone.me.devices.find(d=>d.address===phone.address));await desk.syncRoots();await desk.syncReadMarks();await desk.syncTopicTitles();await desk.syncInvitations();check((await copies(desk)).length===before,'archive accepted mappings suppress copies across all producer/recovery wakes');

 desk.connected=false;
}
// Pending state queries must not materialize ten thousand completed rows.
{
 const e=await device('indexed-pending/desk'),fp=e.fp,target={address:'indexed-pending/phone',fingerprint:fp};
 for(let offset=0;offset<10000;offset+=50)await e.store.write(Array.from({length:50},()=>{const id=wire.newID();return{s:'outbox',k:id,v:{id,to:target.address,recipient_fp:fp,sub:wire.SubHistoryArchive,state:'delivered',at:1,body:'completed archive descriptor',envelope:'completed ciphertext fixture '+'.'.repeat(4096)}};}));
 const all=e.store.all.bind(e.store);e.store.all=async table=>{if(table==='outbox')throw Error('Pending scheduler scanned completed history.');return all(table);};
 const {historyWindow}=await import('../static/historywindow.mjs');
 check(await historyWindow(e.store,target)===50,'10k completed rows neither consume history capacity nor require all-outbox materialization');
 e.connected=true;await e.flushOutbox();await e.archives().wake();e.connected=false;
 check((await e.store.outboxStates(['queued','waiting','archive_staged'])).length===0,'10k completed archives create no new queue pressure');
 e.store.all=all;
}
// Original endpoint identity also governs local deletion of imported outgoing rows.
await p.deleteThread(b.address,id);
check(!(await p.v1Threads()).some(g=>g.some(m=>m.id===id||m.id===answer)), 'local deletion removes imported request and replies');
check((await p.store.get('inbox',id)).body==='', 'deleted imported outgoing text is blanked');
await receiveHistory(await seal(a,JSON.parse(first.find(r=>JSON.parse(r.body).item.id===id).body)),p);
check((await p.store.get('inbox',id)).body===''&&!(await p.v1Threads()).some(g=>g.some(m=>m.id===id)), 'new encrypted duplicate cannot resurrect erased history');
console.log('direct history browser checks',checks);
if(typeof window!=='undefined'){for(const e of engines){await Promise.all([e.topicSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun,e.archiveRun,e.historyProduction,e.syncOutboxPass].filter(Boolean));e.store.close();await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(e.fixtureStoreName);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});}window.deviceHistoryResult={ok:true,checks,storage:'IndexedDB'};}

