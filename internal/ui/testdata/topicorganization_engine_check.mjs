// Real signed/encrypted browser turns over isolated stores, no live service.
import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, chatTopicAssignments, chatTopicRedirects, summarizeChatTopics } from '../static/engine.mjs';
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
const conv=await wire.rootID(root),c={id:conv,root:wire.rootJSON(root),peer:b.me.person,created:root.created,creator:a.address};
await a.store.write([{s:'convs',k:conv,v:c}]);
await b.store.write([{s:'convs',k:conv,v:{...c,peer:a.me.person}}]);
await outsider.store.write([{s:'convs',k:conv,v:c}]);
const rows=async e=>e.convMessages(conv,await e.store.all('inbox'),await e.store.all('outbox'));
const deliver=async sent=>{
 for(const r of (await a.store.all('outbox')).filter(r=>r.lid===sent.lid&&r.to===b.address)){
  const env=wire.parseEnvelope(r.envelope);await wire.verifyEnvelope(env,a.pub.sign_key);
  const n=await wire.open(r.envelope,b.keys,b.address,a.pub),ops=await b.admitConv(n,env,await b.pinned(a.address),{...n,at:1790000000000,fp:a.fp,read:false});
  await b.store.write(ops,ops.checks||[]);
 }
};
const from=wire.newID(),to=wire.newID();
const first=await a.sendDM({conv,body:'Selected parent',topic:from});await deliver(first);
const child=await a.sendDM({conv,body:'Unselected child',reply_to:first.lid});await deliver(child);
const sourceOther=await a.sendDM({conv,body:'Other selected source message',topic:from});await deliver(sourceOther);
const main=await a.sendDM({conv,body:'Selected main-flow message'});await deliver(main);
const destination=await a.sendDM({conv,body:'Destination',topic:to});await deliver(destination);
const before=await a.store.all('outbox');
const review=await a.api('/api/topic/organization/preview',{conv,ids:[first.id,main.id],topic:to});
same(review.moves.length,2,'exact noncontiguous cross-topic selection');
check(wire.validHash(review.token)&&wire.validID(review.operation),'review has exact commitment and retry identity');
const moved=await a.api('/api/topic/organization/apply',review);await deliver(moved);
for(const e of [a,b]){
 const assigned=chatTopicAssignments(await rows(e));
 same([assigned.get(first.lid),assigned.get(main.lid),assigned.get(child.lid),assigned.get(sourceOther.lid)],[to,to,from,from],'only reviewed selection moves on both readers');
 same(await e.outgoingTopic(conv,'',first.lid),from,'implicit later reply keeps original signed placement');
}
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
const countBefore=(await a.store.all('outbox')).length;
await refuses(()=>a.applyTopicOrganization(stale),/org1|required|update/);
same((await a.store.all('outbox')).length,countBefore,'incompatible audience produces no partial local assignment');b.caps=[wire.CapTopicOrganization];

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
await a.applyTopicOrganization(named);
const namedSummary=(await a.chatTopics(conv)).find(t=>t.id===namedTopic);
check(namedSummary?.title==='Named destination'&&namedSummary.count===1,'named new topic atomically retains exact reviewed title and selection');
const renamedReview=await a.previewTopicOrganization({conv,ids:[first.id],topic:namedTopic});
await a.changeChatTopic('rename',{conv,id:namedTopic,title:'Changed destination'});
await refuses(()=>a.applyTopicOrganization(renamedReview),/changed/);
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
