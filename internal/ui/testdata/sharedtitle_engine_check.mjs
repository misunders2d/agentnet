// Actual signed Engine routes, using the existing isolated human/group world.
import {assert,fixtureAssertionCount,world,id} from './human_engine_check.mjs';
import {Engine,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const caps=[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapConversationAudience,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom,wire.CapTopicParticipation,wire.CapTopicContext,wire.CapSharedTopicTitle];
const fixtureStores=[];let reopens=0;
const store=typeof process!=='undefined'?undefined:async()=>{const name='shared-title-'+wire.newID(),s=await openIDB(name);s.fixtureStoreName=name;fixtureStores.push(s);return s;};
async function ready(w){for(const e of w.users){e.realm='f'.repeat(32);e.featureList=null;const fetch=e.fetch;e.fetch=async(url,o)=>new URL(url).pathname==='/v1/version'?new Response(JSON.stringify({features:['env2','env3','person2','caps','notify1'],realm_id:'f'.repeat(32)})):fetch(url,o);await w.caps(e,caps);}}
async function receiveCopy(w,sender,receiver,lid){const row=(await sender.store.all('outbox')).find(r=>r.lid===lid&&r.to===receiver.address);assert.ok(row,'exact recipient copy');await w.receive(row.envelope,receiver);assert.equal(await receiver.store.get('held',row.id),undefined,JSON.stringify(await receiver.store.get('held',row.id)));return row;}
async function restart(e){if(e.fixtureStoreName){e.store.close();Object.assign(e.store,await openIDB(e.fixtureStoreName));reopens++;}const n=new Engine({store:e.store,now:e.now,base:e.base,fetch:e.fetch});for(const k of ['keys','address','pub','fp','me','roster','realm','fixtureStoreName'])n[k]=e[k];if(n.fixtureStoreName){n.keys=(await n.store.get('kv','identity')).keys;assert.deepEqual(await wire.publicEntry(n.keys,n.address),n.pub,'reopened persisted CryptoKeys retain exact identity');}return n;}
{
 const w=await world({store});await ready(w);const {alice:a,bob:b,conv}=w,topic=id();
 await a.sendDM({conv,topic,body:'Automatic topic'});await w.drain(b);
 await b.store.write([{s:'kv',k:'topic/'+conv+'/'+topic,v:{type:'topic-state',peer:conv,topic,title:'Private before explicit rename'}}]);b.changed();
 assert.equal((await b.api('/api/unread')).rows.find(r=>r.topic===topic)?.topic_title,'Private before explicit rename');assert.equal(await b.sharedTopicTitle(conv,topic),undefined);

 await a.store.write([{s:'kv',k:'topic/'+conv+'/'+topic,v:{type:'topic-state',peer:conv,topic,title:'Old private'}}]);
 assert.equal((await a.chatTopics(conv))[0].title,'Old private');
 const first=await a.renameSharedTopic(conv,topic,' Shared\u202e @agent ');await receiveCopy(w,a,b,first.lid);
 assert.deepEqual(await a.sharedTopicTitle(conv,topic),await b.sharedTopicTitle(conv,topic));
 assert.equal((await b.chatTopics(conv))[0].title,'Shared @agent');
 let titleBatches=0,titleKeys=0;const getMany=b.store.getMany.bind(b.store);b.store.getMany=async(s,keys)=>{if(s==='kv'&&keys.some(k=>k.startsWith('topic/'))){titleBatches++;titleKeys+=keys.length;}return getMany(s,keys);};
 const review=await b.api('/api/unread');assert.equal(review.rows.find(r=>r.topic===topic)?.topic_title,'Shared @agent');assert.equal(titleBatches,1);assert.equal(titleKeys,3,'one topic: private, Main private and shared in one indexed transaction');
 await b.api('/api/unread?limit=1');assert.equal(titleBatches,1,'next page never rereads topic metadata');

 assert.equal((await b.convMessages(conv,await b.store.all('inbox'),await b.store.all('outbox'))).some(m=>m.lid===first.lid),false);
 const reset=await b.renameSharedTopic(conv,topic,'');await receiveCopy(w,b,a,reset.lid);
 await a.store.write([{s:'kv',k:'topic/'+conv+'/'+topic,v:{type:'topic-state',peer:conv,topic,title:'Late private sync'}}]);
 assert.equal((await a.chatTopics(conv))[0].title,'Automatic topic');
 assert.equal((await a.chatTopics(conv))[0].renamed,false);
 await b.store.write([{s:'kv',k:'topic/'+conv+'/'+topic,v:{type:'topic-state',peer:conv,topic,title:'Late private sync'}}]);b.changed();
 assert.equal((await b.api('/api/unread')).rows.find(r=>r.topic===topic)?.topic_title,'Automatic topic','reset suppresses later private title in never-opened unread');

 const left=await a.renameSharedTopic(conv,topic,'Alice concurrent'),right=await b.renameSharedTopic(conv,topic,'Bob concurrent');
 await receiveCopy(w,b,a,right.lid);await receiveCopy(w,a,b,left.lid);
 const winner=a.fp>b.fp?'Alice concurrent':'Bob concurrent';
 assert.equal((await a.sharedTopicTitle(conv,topic)).title,winner);assert.deepEqual(await a.sharedTopicTitle(conv,topic),await b.sharedTopicTitle(conv,topic));
 await receiveCopy(w,a,b,first.lid);assert.equal((await b.sharedTopicTitle(conv,topic)).title,winner,'old reorder cannot win');
 const current=await a.sharedTopicTitle(conv,topic),author=current.writer===a.fp?a:b,other=author===a?b:a;
 const bad=await w.from(author,other,{topic,topic_event:{action:'rename',title:'Conflict',rev:current.rev}}),badID=await w.receive(bad,other);
 assert.equal((await other.store.get('held',badID))?.reason,'invalid');
 await assert.rejects(()=>a.renameSharedTopic(conv,'','Main'),/exact conversation topic/);
 const stale=await Promise.allSettled([a.renameSharedTopic(conv,topic,'First local'),a.renameSharedTopic(conv,topic,'Second local')]);
 assert.equal(stale.filter(r=>r.status==='fulfilled').length,1);assert.equal(stale.find(r=>r.status==='rejected').reason.code,'shared_topic_title_stale');
 // A saved flag survives restart and independently gates an unchanged sealed copy.
 const row=(await a.store.all('outbox')).find(r=>r.lid===stale.find(r=>r.status==='fulfilled').value.lid&&r.to===b.address);
 assert.equal(row.shared_title_cap,true);await w.caps(b,caps.filter(c=>c!==wire.CapSharedTopicTitle));
 const again=await restart(a);await again.post({...row});assert.equal((await a.store.get('outbox',row.id)).state,'waiting');
 await w.caps(b,caps);const updated=await restart(a);await updated.post({...await a.store.get('outbox',row.id),state:'queued'});assert.equal((await a.store.get('outbox',row.id)).state,'custody',JSON.stringify(await a.store.get('outbox',row.id)));assert.equal((await a.store.get('outbox',row.id)).envelope,row.envelope);


 const incomingPin=await b.store.get('pins',a.address),registerBefore=await b.sharedTopicTitle(conv,topic);await b.store.write([{s:'pins',k:a.address,v:{...incomingPin,pending:{fingerprint:'ffffffff-ffffffff-ffffffff-ffffffff'}}}]);
 const pinnedDenied=await w.receive(await w.from(a,b,{topic,topic_event:{action:'rename',title:'Pending key cannot rename',rev:100}}),b);assert.equal((await b.store.get('held',pinnedDenied))?.reason,'key_changed');assert.deepEqual(await b.sharedTopicTitle(conv,topic),registerBefore);await b.store.write([{s:'pins',k:a.address,v:incomingPin}]);
 const beforeRace=(await a.store.all('outbox')).length,write=a.store.write.bind(a.store),prior=await a.sharedTopicTitle(conv,topic);let raced=false;
 a.store.write=async(ops,checks)=>{if(!raced&&ops.some(o=>o.s==='outbox'&&wire.topicRename(o.v?.topic_event))){raced=true;await write([{s:'kv',k:a.sharedTopicTitleKey(conv,topic),v:{...prior,rev:prior.rev+1,title:'Accepted competing winner',lid:id()}}]);}return write(ops,checks);};
 await assert.rejects(()=>a.renameSharedTopic(conv,topic,'Losing transaction'),error=>error.code==='shared_topic_title_stale');a.store.write=write;
 assert.equal((await a.store.all('outbox')).length,beforeRace,'checked-write race stores no partial outbox');assert.equal((await a.sharedTopicTitle(conv,topic)).title,'Accepted competing winner');
 console.log('DM: projection/reset/concurrent/reorder/conflict/stale/restart additional cap passed');
}
async function guests(group){
 const w=await world({store});await ready(w);const {alice:a,bob:b,carol:g}=w;let conv=w.conv;
 if(group){conv=await a.createGroup('Shared titles');const invite=await a.inviteGroup({conv,person:b.me.person});await w.drain(b);await b.retryHeld();await b.decideGroup({id:invite.id,accept:true});await w.drain(a);if(a.recoveringGroups)await a.recoveringGroups;await a.recoverGroupIntents();await w.drain(b);await b.retryHeld();}
 const topic=id();await a.sendDM({conv,topic,body:'Topic automatic'});await w.drain(b);
 const invited=await a.changeHuman('invite',{conv,host:g.address,topic});await w.drain(g);await w.drain(b);await g.changeHuman('decide',{pid:invited.pid,accept:true});await w.drain(a);await w.drain(b);
 const member=await a.renameSharedTopic(conv,topic,'Member with guest');await receiveCopy(w,a,b,member.lid);await receiveCopy(w,a,g,member.lid);
 assert.equal((await g.sharedTopicTitle(conv,topic)).writer,a.fp);
 const guest=await g.renameSharedTopic(conv,topic,'Guest shared');await receiveCopy(w,g,a,guest.lid);await receiveCopy(w,g,b,guest.lid);
 assert.equal((await a.chatTopics(conv))[0].title,'Guest shared');assert.equal((await a.sharedTopicTitle(conv,topic)).writer,g.fp);assert.deepEqual(await a.sharedTopicTitle(conv,topic),await b.sharedTopicTitle(conv,topic));
 await assert.rejects(()=>g.renameSharedTopic(conv,id(),'Wrong topic'),/topic|active|outside|scope|captured/i);
 const source=(await a.store.all('inbox')).find(r=>r.lid===guest.lid);assert.ok(source);
 // Exact captured proof cannot be transplanted to a different topic or original writer.
 const guestCopy=(await g.store.all('outbox')).find(r=>r.to===a.address&&r.lid===guest.lid),opened=await wire.open(guestCopy.envelope,a.keys,a.address,g.pub);
 const wrong=await w.receive(await wire.seal({...opened,id:id(),lid:id(),topic:id()},g.keys,a.pub),a);assert.equal((await a.store.get('held',wrong))?.reason,'invalid');
 await a.changeHuman('end',{pid:invited.pid});await w.drain(g);await w.drain(b);
 await assert.rejects(()=>g.renameSharedTopic(conv,topic,'Dismissed'),/active|captured|current|outside/i);
 const phone=await w.sibling(a,group?'alice/group-phone':'alice/dm-phone',true);await ready(w);const dev=a.me.devices.find(d=>d.address===phone.address),chat=await a.store.get('convs',conv)||await a.groupRecord(conv);
 if(group){const carriers=await a.groupHistoryCarriers(chat,dev,[]);for(const carrier of carriers){await a.store.write([{s:'outbox',k:carrier.id,v:carrier}]);await a.post(carrier);}await w.drain(phone);}
 if(group){const j={state:'running',device:dev.address,fingerprint:dev.fingerprint,own_human:a.fp};await a.store.write([{s:'kv',k:'history',v:{[dev.address]:j}}]);for(let page=0;page<4;page++)await a.historyCatchupPage(dev,(await a.store.get('kv','history'))[dev.address]);await w.drain(phone);await phone.retryHeld();}
 const history=await a.historyCopy(dev,chat,a.itemOf(source,false));await w.receive(history.envelope,phone);
 if(await phone.store.get('held',history.id)){try{await phone.admitInner(history.envelope,wire.parseEnvelope(history.envelope));}catch(error){throw Error('Historical rename: '+error.reason+' '+error.message);}}
 assert.equal((await phone.sharedTopicTitle(conv,topic))?.writer,g.fp,'historical writer is original guest, not own forwarder');
 assert.equal((await phone.sharedTopicTitle(conv,topic))?.title,'Guest shared');
 const h=wire.parseHistory(history.body);
 async function denyHistory(patch){const body=wire.historyJSON({...h,...patch,id:id(),lid:id()});let forged;if(group){const {packet}=await a.groupTurnEvidence(conv,[]);forged=await a.groupDataCopy(packet,'history',body,dev);}else forged=await a.ownCopy(dev,chat,'history',body);await w.receive(forged.envelope,phone);assert.equal((await phone.store.get('held',forged.id))?.reason,'invalid');}
 await denyHistory({from:b.address,from_key:b.fp});await denyHistory({topic:id()});assert.throws(()=>wire.parseHistory(wire.historyJSON({...h,pid:id()})),/invalid shared topic rename/);if(group)await denyHistory({group_admission:'e'.repeat(64)});
 const damaged=structuredClone(h.human);damaged.proof[1].sig[0]^=1;await denyHistory({human:damaged});
 // Winner retention is exact; the existing own-human source gate precedes this exception.
 const erased={conv,key:g.fp,lid:source.lid,at:a.now()},nonwinner={conv,key:a.fp,lid:member.lid,at:a.now()};await a.store.write([{s:'erased',k:conv+'|'+g.fp+'|'+source.lid,v:erased},{s:'erased',k:conv+'|'+a.fp+'|'+member.lid,v:nonwinner}]);await a.loadErased();

 if(group)await assert.rejects(()=>a.groupSelectedItems(conv,[{lid:source.lid,author:g.fp,hash:'a'.repeat(64)}]),/unavailable/);
 else await assert.rejects(()=>a.prepareGrantSnapshots(conv,topic,[{lid:source.lid,fingerprint:g.fp}]),/outside/);
 assert.equal(a.erasedRow(source),true);assert.equal(await a.ownSharedTitleWinner(source),true);assert.equal(await a.ownSharedTitleWinner({...source,lid:id()}),false);assert.equal(await a.ownSharedTitleWinner({...source,fp:b.fp}),false);
 const fresh=await w.sibling(a,group?'alice/group-after-erase':'alice/dm-after-erase',true);await ready(w);const freshDev=a.me.devices.find(d=>d.address===fresh.address);
 const job={state:'running',device:freshDev.address,fingerprint:freshDev.fingerprint,own_human:a.fp};await a.store.write([{s:'kv',k:'history',v:{[freshDev.address]:job}}]);
 for(let page=0;page<4;page++)await a.historyCatchupPage(freshDev,(await a.store.get('kv','history'))[freshDev.address]);
 const recovered=(await a.store.all('outbox')).filter(r=>r.to===freshDev.address&&r.sub==='history').map(r=>{try{return wire.parseHistory(r.body);}catch{return null;}}).filter(Boolean);
 assert.ok(recovered.some(r=>r.lid===source.lid&&r.from_key===g.fp),'erased current winner emitted by own bounded catchup');assert.equal(recovered.some(r=>r.lid===member.lid),false,'erased older rename never exported');
 await w.drain(fresh);await fresh.retryHeld();assert.equal((await fresh.sharedTopicTitle(conv,topic))?.writer,g.fp,'brand-new own link recovers erased current winner');assert.equal((await fresh.sharedTopicTitle(conv,topic))?.title,'Guest shared');
 assert.equal((await fresh.convMessages(conv,await fresh.store.all('inbox'),await fresh.store.all('outbox'))).some(m=>wire.topicRename(m.topic_event)),false,'recovery never restores rename timeline/unread rows');
 const guestPhone=await w.sibling(g,group?'carol/group-phone':'carol/dm-phone',true);await ready(w);
 await assert.rejects(async()=>g.historyCopy(g.me.devices.find(d=>d.address===guestPhone.address),await awaitChat(g,conv),g.itemOf(source,false)));
 console.log((group?'Group':'DM')+': member/guest/exact topic/dismissal/history original writer/tamper/own erased winner passed');
}
async function awaitChat(e,conv){return await e.store.get('convs',conv)||await e.groupRecord(conv);}
await guests(false);await guests(true);
if(typeof window!=='undefined'){for(const s of fixtureStores)s.close();window.sharedTitleIDBResult={ok:true,storage:'IndexedDB',checks:fixtureAssertionCount(),groups:3,reopens,fixtureStoreNames:fixtureStores.map(s=>s.fixtureStoreName)};}
