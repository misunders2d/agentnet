// Real signed/encrypted browser turns over isolated stores, no live service.
import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, chatTopicAssignments, chatTopicRedirects, summarizeChatTopics, topicOrganizationNoticeTitle } from '../static/engine.mjs';
let checks=0;
const check=(v,why)=>{assert.ok(v,why);checks++;};
const same=(a,b,why)=>{assert.deepEqual(a,b,why);checks++;};
const refuses=async(fn,re)=>{await assert.rejects(fn,re);checks++;};
const users=[];
for(const name of ['alice','bob','outsider']){
 const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',now:()=>1790000000000,fetch:async()=>{throw Error('No live network in organization check');}});
 e.keys=await wire.newKeys();e.address=name+'/desk';e.pub=await wire.publicEntry(e.keys,e.address);e.fp=await wire.fingerprint(e.pub);
 e.roster=await wire.newRoster(e.keys,e.address,name);e.me=await e.personRecord([e.roster],'self',null);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}}]);
 e.refreshPerson=async p=>p;e.supports=async()=>[true,'',false];e.sendGroupCopy=async()=>'';e.queueOutbox=()=>{};e.changed=()=>{};e.syncTopicTitles=async()=>{};
 e.ctlSupport=async(address,pin,cap)=>{const other=users.find(u=>u.address===address);return [other?.caps?.includes(cap)??true,'peer_update: org1 required'];};
 e.caps=[wire.CapTopicOrganization];users.push(e);
}
for(const e of users)for(const other of users){
 const p=await e.personRecord([other.roster],other===e?'self':'pinned',null);
 await e.store.write([{s:'persons',k:p.person,v:p},{s:'pins',k:other.address,v:{address:other.address,json:wire.marshalPublic(other.pub),fingerprint:other.fp,pending:null}}]);
}
const [a,b,outsider]=users,root=await wire.newRoot(a.keys,{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},{person:b.me.person,roster:b.me.hash});
// The stored-admission display seam does not grant any new metadata authority.
// Its first canonical agent notice is automatic; explicit human names win.
{
 const conv='d'.repeat(64),topic=wire.newID(),notice=title=>'Moved selected messages to new topic “'+title+'”.';
 same(notice('Fixture organization'),'Moved selected messages to new topic “Fixture organization”.','explicit new-topic phrase is pinned independently');
 for(const title of ['Fixture organization','Розбір помилки','embedded” quote','界'.repeat(120)])same(topicOrganizationNoticeTitle(topic,notice(title)),title,'canonical notice round trip');
 for(const body of ['Moved selected messages to Main flow.','Moved selected messages to topic '+topic+'.','Moved selected messages to “Legacy private name”.','arbitrary prose',notice(''),notice(' spaced '),notice('two  spaces'),notice('line\nbreak'),notice('bid\u202ei'),'Moved selected messages to new topic “broken suffix”',notice('界'.repeat(121)),notice('Name')+' more'])same(topicOrganizationNoticeTitle(topic,body),'','noncanonical or legacy notice cannot supply a name');
 same(topicOrganizationNoticeTitle('',notice('Name')),'','Main stays unnamed');
 const first={id:wire.newID(),lid:wire.newID(),conv,kind:'message',body:'Original automatic name',topic,ts:1,at:1000,to:b.address,state:'delivered'},
  move={id:wire.newID(),lid:wire.newID(),conv,kind:'message',topic,ts:2,at:2000,to:b.address,pid:wire.newID(),origin:wire.OriginAgentOrganization,body:notice('Fixture organization'),topic_event:{action:'move',request:{lid:wire.newID()}}},
  later={...move,id:wire.newID(),lid:wire.newID(),ts:3,at:3000,body:notice('Later move cannot rename')};
 for(const msgs of [[first,move,later],[later,first,move]]){
  const [t]=summarizeChatTopics(conv,msgs,new Map(),10);check(t.title==='Fixture organization'&&!t.renamed,'first stored notice wins regardless of arrival order');
 }
 for(const change of [{origin:'ui'},{pid:''},{topic_event:{action:'move'}},{topic_event:{action:'merge',request:move.topic_event.request}},{topic:wire.newID()},{body:'Arbitrary agent text'}])same(summarizeChatTopics(conv,[first,{...move,...change}],new Map(),10)[0].title,first.body,'unrelated/noncanonical event preserves automatic name');
 // A creator inference from the current group would leak old private names
 // when earlier destination content moved away or was never in this grant.
 const author=a.fp,selected={...first,topic:'',key:author,ts:2,at:2000},existing={...first,id:wire.newID(),lid:wire.newID(),key:author,body:'Existing destination original',ts:1,at:1000},
  legacy={...move,ts:4,at:4000,body:'Moved selected messages to “HOST PRIVATE LABEL”.',topic_event:{...move.topic_event,moves:[{lid:selected.lid,author}]}},
  away={...move,id:wire.newID(),lid:wire.newID(),ts:3,at:3000,topic:wire.newID(),origin:'ui',topic_event:{action:'move',moves:[{lid:existing.lid,author,topic}]}};
 for(const msgs of [[existing,selected,legacy],[existing,selected,away,legacy],[selected,legacy]]){
  const t=summarizeChatTopics(conv,msgs,new Map(),10).find(t=>t.id===topic);check(t&&t.title!=='HOST PRIVATE LABEL'&&!t.renamed,'legacy notice cannot disclose private label with full, moved-away or absent earlier history');
 }
 const key='topic/'+conv+'/'+topic,msgs=[first,move,later];
 await a.store.write([{s:'kv',k:key,v:{type:'topic-state',peer:conv,topic,title:'Private name'}}]);
 let [t]=(await a.chatTopicSummaries(conv,msgs)).topics;
 check(t.title==='Private name'&&t.auto_title==='Fixture organization'&&t.renamed,'private name wins over notice fallback');
 for(const title of ['Shared name','']){
  await a.store.write([{s:'kv',k:a.sharedTopicTitleKey(conv,topic),v:{conv,topic,title,rev:1,writer:a.fp}}]);
  [t]=(await a.chatTopicSummaries(conv,msgs)).topics;
  if(title)check(t.title===title&&t.auto_title==='Fixture organization'&&t.renamed,'shared name wins over private and automatic names');
  else check(t.title==='Fixture organization'&&!t.renamed&&!t.auto_title,'shared reset restores notice automatic title');
 }
}
const conv=await wire.rootID(root),c={id:conv,root:wire.rootJSON(root),peer:b.me.person,created:root.created,creator:a.address};
await a.store.write([{s:'convs',k:conv,v:c}]);
await b.store.write([{s:'convs',k:conv,v:{...c,peer:a.me.person}}]);
await outsider.store.write([{s:'convs',k:conv,v:c}]);
const rows=async e=>e.convMessages(conv,await e.store.all('inbox'),await e.store.all('outbox'));
const deliver=async sent=>{
 for(const r of (await a.store.all('outbox')).filter(r=>r.lid===sent.lid&&r.to===b.address)){
  const env=wire.parseEnvelope(r.envelope);await wire.verifyEnvelope(env,a.pub.sign_key);
  const n=await wire.open(r.envelope,b.keys,b.address,a.pub),ops=await b.admitConv(n,env,await b.pinned(a.address),{...n,to:undefined,at:1790000000000,fp:a.fp,read:false});
  await b.store.write(ops,ops.checks||[]);
 }
};
const from=wire.newID(),to=wire.newID();
const first=await a.sendDM({conv,body:'Selected parent',topic:from});await deliver(first);
const child=await a.sendDM({conv,body:'Unselected child',reply_to:first.lid});await deliver(child);
const sourceOther=await a.sendDM({conv,body:'Other selected source message',topic:from});await deliver(sourceOther);
const fileBytes=new TextEncoder().encode('EXACT_SELECTED_BROWSER_FILE');
const main=await a.sendDM({conv,body:'Selected main-flow message',files:[{name:'selected.txt',bytes:fileBytes}]});await deliver(main);
const destination=await a.sendDM({conv,body:'Destination',topic:to});await deliver(destination);
await a.store.write([{s:'kv',k:'topic/'+conv+'/'+to,v:{type:'topic-state',peer:conv,topic:to,title:'HUMAN PRIVATE LABEL'}}]);
const before=await a.store.all('outbox');
const review=await a.api('/api/topic/organization/preview',{conv,ids:[first.id,main.id],topic:to});
same(review.moves.length,2,'exact noncontiguous cross-topic selection');
check(wire.validHash(review.token)&&wire.validID(review.operation),'review has exact commitment and retry identity');
const moved=await a.api('/api/topic/organization/apply',review);await deliver(moved);
same((await a.store.all('outbox')).find(r=>r.lid===moved.lid).body,'Moved selected messages to topic '+to+'.','outgoing human move does not copy an implicit private label');
same((await a.api('/api/dm?id='+conv)).messages.find(m=>m.lid===moved.lid).body,'Moved selected messages to “HUMAN PRIVATE LABEL”.','local human move still displays own private label');
for(const e of [a,b]){
 const assigned=chatTopicAssignments(await rows(e));
 same([assigned.get(first.lid),assigned.get(main.lid),assigned.get(child.lid),assigned.get(sourceOther.lid)],[to,to,from,from],'only reviewed selection moves on both readers');
 same(await e.outgoingTopic(conv,'',first.lid),from,'implicit later reply keeps original signed placement');
 const grant=[{lid:first.lid,fingerprint:a.fp},{lid:main.lid,fingerprint:a.fp}],snapshots=await e.prepareGrantSnapshots(conv,to,grant);
 same(snapshots.length,2,'moved selection preview requires exact witnesses');
 await e.checkTopicGrants(conv,to,grant,[],snapshots);
}
// A real signed invitation carries only hashes of the exact reviewed history;
// its inert excerpts keep original metadata and bind the selected file bytes.
for(const e of users){e.caps.push(wire.CapTopicParticipation,wire.CapTopicContext,wire.CapRoom,wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity);e.profile=async address=>{const peer=users.find(u=>u.address===address),session=peer.capSession||=(wire.newID());return {sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(peer.keys,peer.address,session,peer.caps)))]};};}
const invitation=await a.inviteAgent({conv,host:b.address,share:[first.id,main.id],topic:to});
const inviteRow=(await a.store.all('outbox')).find(r=>r.pid===invitation.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='invite');
const missing=await wire.signEvent(a.keys,{...wire.parseEvent(inviteRow.body),pid:wire.newID(),snapshots:undefined}),beforeMissing=await a.store.all('outbox');
await wire.verifyEvent(missing,a.pub.sign_key);
await refuses(()=>a.sendConv(c,{queued:true,kind:'message',sub:'event',body:wire.eventJSON(missing),pid:missing.pid}),/changed after review/);
same(await a.store.all('outbox'),beforeMissing,'signed moved grant without witnesses commits no event or copy');
const unchangedGrant=[{lid:destination.lid,fingerprint:a.fp}],unneeded=await wire.signEvent(a.keys,{...missing,pid:wire.newID(),grant:unchangedGrant,snapshots:[{...unchangedGrant[0],hash:await wire.groupHistoryContentHash(conv,await a.grantSnapshotItem(conv,unchangedGrant[0]))}]});
await refuses(()=>a.sendConv(c,{queued:true,kind:'message',sub:'event',body:wire.eventJSON(unneeded),pid:unneeded.pid}),/changed after review/);
same(await a.store.all('outbox'),beforeMissing,'unmoved grant with unneeded witnesses commits no event or copy');
await deliver({lid:inviteRow.lid});
const info=(await b.agentConv(invitation.pid)).info;
same(info.snapshots.length,2,'exact signed snapshots reach actual receiver');
const excerpts=(await a.store.all('outbox')).filter(r=>r.pid===invitation.pid&&r.sub==='excerpt');
same(excerpts.length,2,'only reviewed inert excerpts sent');
for(const row of excerpts){await deliver({lid:row.lid});const received=await b.store.get('inbox',row.id);check(received?.replica&&received.sub==='excerpt'&&!received.state,'received history cannot execute');const h=wire.parseHistory(received.body);same(await wire.groupHistoryContentHash(conv,h),info.snapshots.find(s=>s.lid===h.lid).hash,'receiver verifies original content/files hash');}
const savedExcerpt=wire.parseHistory(excerpts.find(r=>wire.parseHistory(r.body).lid===main.lid).body);
same(savedExcerpt.topic||'','','original Main scope stays unchanged in disclosed snapshot');
same(savedExcerpt.attachments[0].sha256,wire.hex(await wire.sha256(fileBytes)),'selected file manifest is the exact reviewed bytes');
const altered={...savedExcerpt,body:'substituted text'},carrier={conv,pid:info.pid,sub:'excerpt',body:wire.historyJSON(altered),attachments:[],replica:true,kind:'message'};
await refuses(()=>b.verifyGrantedSnapshot(carrier,info),/snapshot/i);
const malformed=wire.parseEvent(inviteRow.body);malformed.snapshots.pop();
assert.throws(()=>wire.parseEvent(wire.eventJSON(malformed)),/snapshot/);checks++;
await refuses(()=>a.checkParticipationTopic(to,{conv,ref:{id:main.lid,fingerprint:a.fp}}),/outside/);
const retainedCiphertext=inviteRow.envelope;
b.caps=b.caps.filter(c=>c!==wire.CapTopicContext);
await a.post({...inviteRow});
same((await a.store.get('outbox',inviteRow.id)).state,'waiting','tps1/org1 does not bypass independent snapshot capability');
same((await a.store.get('outbox',inviteRow.id)).envelope,retainedCiphertext,'unsupported copy keeps the same sealed invitation');
b.caps.push(wire.CapTopicContext);
await a.store.write([{s:'outbox',k:inviteRow.id,v:inviteRow}]);
for(const r of before)same((await a.store.get('outbox',r.id)).envelope,r.envelope,'original IDs/authors/replies/ciphertext not rewritten');
const sentEvent=(await a.store.all('outbox')).find(r=>r.lid===moved.lid);
check(!wire.parseEnvelope(sentEvent.envelope).attn&&!wire.parseEnvelope(sentEvent.envelope).chan,'organization emits no old-mention alerts');
same((await a.applyTopicOrganization(review)).lid,moved.lid,'same-ID exact retry does not resend');
const restarted=new Engine({store:a.store,base:'https://synthetic.invalid',fetch:async()=>{throw Error('No live network after restart');}});
await restarted.load();
same((await restarted.applyTopicOrganization(review)).lid,moved.lid,'durable exact retry after restart needs no network or resend');
await refuses(()=>a.applyTopicOrganization({...review,topic:from}),/changed|invalid/);
await refuses(()=>outsider.previewTopicOrganization({conv,ids:[first.id],topic:to}),/member|membership|current/);

// Edits, deletes, reassignment, topic closure and audience/key changes each
// force a fresh reviewed selection. No side effects reach an incompatible reader.
let stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});
const ctl=wire.newID();
await a.store.write([{s:'outbox',k:ctl,v:{id:ctl,lid:ctl,conv,to:b.address,control:true,sub:wire.SubRevision,body:JSON.stringify({rev:1,text:'Edited child'}),ref:{id:child.lid,fingerprint:a.fp},person:a.me.person,state:'delivered',at:1790000000000}}]);
await refuses(()=>a.applyTopicOrganization(stale),/changed/);
stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});
const erased=conv+'|'+a.fp+'|'+child.lid;
await a.store.write([{s:'erased',k:erased,v:{conv,key:a.fp,lid:child.lid,deletion:wire.newID(),shared:false}}]);await a.loadErased();
await refuses(()=>a.applyTopicOrganization(stale),/changed/);
await a.store.write([{s:'erased',k:erased}]);await a.loadErased();
stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});
await a.changeChatTopic('done',{conv,id:to,count:3});
await refuses(()=>a.applyTopicOrganization(stale),/changed/);
await a.changeChatTopic('reopen',{conv,id:to});
stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});
const pin=await a.store.get('pins',b.address);await a.store.write([{s:'pins',k:b.address,v:{...pin,pending:pin.json}}]);
await refuses(()=>a.applyTopicOrganization(stale),/changed|key/);await a.store.write([{s:'pins',k:b.address,v:pin}]);
stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});b.caps=[];
const older=await a.applyTopicOrganization(stale);
const olderCopies=(await a.store.all('outbox')).filter(r=>r.lid===older.lid&&r.to===b.address);
check(olderCopies.length===1&&olderCopies[0].state==='waiting'&&olderCopies[0].detail.startsWith('peer_update: '),'older reader copy waits sealed, saying why');
same(chatTopicAssignments(await rows(a)).get(child.lid),to,'the author move stands with one older reader');
await a.post({...olderCopies[0]});
same((await a.store.get('outbox',olderCopies[0].id)).state,'waiting','the handover gate keeps it from the older reader');
same(chatTopicAssignments(await rows(b)).get(child.lid),from,'the older reader keeps the old layout');
b.caps=[wire.CapTopicOrganization];await deliver(older);
same(chatTopicAssignments(await rows(b)).get(child.lid),to,'once it reads org1 the same sealed move applies');
await deliver(await a.applyTopicOrganization(await a.previewTopicOrganization({conv,ids:[child.id],topic:from})));

// A competing reassignment invalidates a reviewed prior topic. Even a change
// arriving after the final preview is caught by the atomic storage guard.
const otherTopic=wire.newID(),other=await a.sendDM({conv,body:'Other destination',topic:otherTopic});await deliver(other);
stale=await a.previewTopicOrganization({conv,ids:[child.id],topic:otherTopic});
const competing=await a.previewTopicOrganization({conv,ids:[child.id],topic:to});
await a.applyTopicOrganization(competing);
await refuses(()=>a.applyTopicOrganization(stale),/changed/);
const back=await a.previewTopicOrganization({conv,ids:[child.id],topic:from});await a.applyTopicOrganization(back);
stale=await a.previewTopicOrganization({conv,ids:[sourceOther.id],topic:to});
const write=a.store.write.bind(a.store),beforeRace=(await a.store.all('outbox')).length;
let raced=false;
a.store.write=async(ops,guards)=>{
 if(!raced&&ops.some(o=>o.s==='outbox'&&o.v?.organization_review)){
  raced=true;const edit=wire.newID();
  await write([{s:'outbox',k:edit,v:{id:edit,lid:edit,conv,to:b.address,control:true,sub:wire.SubRevision,body:JSON.stringify({rev:1,text:'Edit at commit'}),ref:{id:sourceOther.lid,fingerprint:a.fp},person:a.me.person,state:'delivered',at:1790000000000}}]);
 }
 return write(ops,guards);
};
try{await refuses(()=>a.applyTopicOrganization(stale),/storage changed|changed/);}finally{a.store.write=write;}
check(raced,'commit guard tested after final review');
same((await a.store.all('outbox')).length,beforeRace+1,'storage race keeps only competing edit, no partial organization');

// Merge is a reviewed cutoff, with a navigable source name and no cycle.
const merge=await a.previewTopicOrganization({conv,ids:[child.id,sourceOther.id],topic:to,merge:from});
const merged=await a.applyTopicOrganization(merge);await deliver(merged);
const topics=await a.chatTopics(conv),source=topics.find(t=>t.id===from);
check(source?.redirect===to&&source.count===0&&source.title==='Selected parent','source label and explicit destination survive an empty merge');
const future=await a.sendDM({conv,body:'Future source post',reply_to:child.lid});await deliver(future);
same(chatTopicAssignments(await rows(a)).get(future.lid),from,'future source post is outside the reviewed merge');
check((await a.chatTopics(conv)).find(t=>t.id===from)?.count===1,'source remains openable after new activity');
const namedTopic=wire.newID(),named=await a.previewTopicOrganization({conv,ids:[future.id],topic:namedTopic,new:true,title:'Named destination'});
const namedMove=await a.applyTopicOrganization(named);
same((await a.store.all('outbox')).find(r=>r.lid===namedMove.lid).body,'Moved selected messages to “Named destination”.','browser human notice names destination');
const namedSummary=(await a.chatTopics(conv)).find(t=>t.id===namedTopic);
check(namedSummary?.title==='Named destination'&&namedSummary.count===1,'named new topic atomically retains exact reviewed title and selection');
const renamedReview=await a.previewTopicOrganization({conv,ids:[first.id],topic:namedTopic});
await a.changeChatTopic('rename',{conv,id:namedTopic,title:'Changed destination'});
const renamedView=await a.api('/api/dm?id='+conv);
same(renamedView.messages.find(m=>m.lid===namedMove.lid).body,'Moved selected messages to “Changed destination”.','browser view derives renamed destination from canonical topic');
await refuses(()=>a.applyTopicOrganization(renamedReview),/changed/);
const mainReview=await a.previewTopicOrganization({conv,ids:[future.id],topic:'',main:true}),mainMove=await a.applyTopicOrganization(mainReview);
same((await a.store.all('outbox')).find(r=>r.lid===mainMove.lid).body,'Moved selected messages to Main flow.','browser Main move notice names actual destination');
same((await a.api('/api/dm?id='+conv)).messages.find(m=>m.lid===mainMove.lid).body,'Moved selected messages to Main flow.','browser Main view preserves destination');
await refuses(()=>a.previewTopicOrganization({conv,ids:[first.id],topic:from,merge:to}),/cycle/);
// Concurrent independently signed merges still converge: the late cycle is
// ignored even if its author reviewed before the first merge arrived.
await a.sendConv(c,{id:wire.newID(),queued:true,kind:'message',body:'Concurrent merge',topic:from,topic_event:{action:'merge',merge:to,moves:[{lid:first.lid,author:a.fp,hash:review.moves.find(m=>m.lid===first.lid).hash,topic:to}]},reply_to:merged.lid,origin:'ui'});
same(chatTopicAssignments(await rows(a)).get(first.lid),to,'merge cycle cannot move reviewed history back');
const reversed=(await rows(a)).reverse();
same(Object.fromEntries(chatTopicRedirects(reversed)),Object.fromEntries(chatTopicRedirects(await rows(a))),'redirects converge regardless of arrival order');
same(Object.fromEntries(chatTopicAssignments(reversed)),Object.fromEntries(chatTopicAssignments(await rows(a))),'assignments converge regardless of arrival order');
await refuses(()=>a.previewTopicOrganization({conv,ids:Array.from({length:201},()=>wire.newID()),topic:to}),/200/);
// A received organization by no current human device of a full member is
// held invalid, as the core holds it: never an unexpected failure that
// would end the stream and block everything behind it.
{
 const sent=(await a.store.all('outbox')).find(r=>r.lid===moved.lid&&r.to===b.address),env=wire.parseEnvelope(sent.envelope),n=await wire.open(sent.envelope,b.keys,b.address,a.pub);
 const real=b.topicOrganizationAuthor;b.topicOrganizationAuthor=async()=>{throw Error('Only a current human device of a full member may organize topics.');};
 let held;try{await b.admitConv(n,env,await b.pinned(a.address),{...n,at:1790000000000,fp:a.fp,read:false});}catch(e){held=e;}finally{b.topicOrganizationAuthor=real;}
 check(held?.reason==='invalid'&&/organize topics/.test(held.message),'received unauthorized organization is an invalid hold');
}
console.log(JSON.stringify({checks}));
