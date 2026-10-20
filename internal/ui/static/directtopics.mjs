// Personal, inert direct-chat placement. Original history and execution remain
// untouched; own-human authority and normal durable encrypted copies are reused.
import * as wire from './wire.mjs';
import {stateWindow} from './historywindow.mjs';
const changed='The selected messages, topic or audience changed; review the move again.';
const prefix=person=>'direct-topic/'+person+'/';
const factKey=(person,p)=>prefix(person)+p.author+'/'+p.id;
const generation=(person,peer)=>'direct-topic-generation/'+person;
const operation=id=>'direct-topic-operation/'+id;
const copyKey=(person,fp,p)=>'direct-topic-copy/'+person+'/'+fp+'/'+p.author+'/'+p.id;
const fields=p=>Object.fromEntries(['peer','id','author','topic','revision','writer','synthetic','destination'].map(k=>[k,p[k]]));
const sameDestination=(a,b)=>!!a&&!!b&&a.host===b.host&&a.fingerprint===b.fingerprint&&(a.agent_id||'')===(b.agent_id||'');
const hash=async v=>wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify(v))));
export function directTopics(e,Hold,StoreConflict){
 const read=(checks,s,k)=>e.groupRead(checks,s,k);
 async function owner(checks=[]){const p=await read(checks,'kv','person');if(!e.ownTopicWriter(p))throw Error('Only a current own human device may organize direct topics.');return p;}
 async function placements(view){if(view?.directPlacements)return view.directPlacements;const own=await e.store.get('kv','person'),out=e.ownTopicWriter(own)?await e.store.prefix('kv',prefix(own.person)):[];if(view)view.directPlacements=out;return out;}
 async function preview(input){
  const operation=input?.new&&input.topic?input.topic:wire.newID(),title=wire.topicTitle(input?.title||'');
  const request={conv:'',peer:input?.peer,ids:input?.ids,topic:input?.new?operation:input?.topic,...(input?.merge?{merge:input.merge}:{}),...(input?.new?{new:true,title}:{})};
  if(!input||Object.keys(input).some(k=>!['peer','conv','ids','topic','merge','new','title','operation','token','moves','parent','destination','synthetic_topics'].includes(k))||input.conv||input.new!=null&&typeof input.new!=='boolean'||input.title!=null&&typeof input.title!=='string'||!wire.validAddress(request.peer)||!wire.validID(request.topic)||input.new&&!title||!input.new&&input.title||[...title].length>120||!Array.isArray(request.ids)||!request.ids.length||request.ids.length>200||request.merge&&(!wire.validID(request.merge)||request.merge===request.topic))throw Error('Choose at most 200 messages and an existing or named new topic in this agent chat.');
  const checks=[],own=await owner(checks);await e.authorityRows({thread_peer:request.peer},checks);
  const view={inbox:await e.store.byThreadPeer('inbox',request.peer),outbox:await e.store.byThreadPeer('outbox',request.peer)};
  await read(checks,'kv',generation(own.person,request.peer));await read(checks,'pins',request.peer);
  const groups=await e.v1Threads(view),all=groups.flat(),destination=groups.find(g=>g[0].display_topic===request.topic);
  if(input.new?(destination||all.some(m=>m.id===request.topic)):!destination)throw Error(changed);
  let binding=destination?.[0].destination;
  if(destination&&destination.some(m=>!sameDestination(m.destination,binding)))throw Error(changed);
  const seen=new Set(),moves=[];
  for(const id of request.ids){
   const m=all.find(m=>m.id===id);
   if(!wire.validID(id)||seen.has(id)||!m||m.status==='review_notice'||m.display_topic===request.topic||request.merge&&m.display_topic!==request.merge||!wire.validDirectTopicDestination(m.destination))throw Error(changed);
   binding ||= m.destination;if(!sameDestination(binding,m.destination))throw Error(changed);seen.add(id);
   const original=await e.directHistory().original(id,checks);if(!original||original.item.from_key!==m.author_fp)throw Error(changed);
   moves.push({lid:id,author:m.author_fp,hash:await wire.deviceHistoryHash(original.item,original.recipient),topic:m.display_topic});
  }
  moves.sort((a,b)=>a.lid.localeCompare(b.lid));
  const virtual=new Set((await placements(view)).filter(p=>p.synthetic).map(p=>p.topic));if(input.new)virtual.add(request.topic);
  const synthetic_topics=[...new Set([request.topic,...moves.map(m=>m.topic)].filter(id=>virtual.has(id)))].sort();
  const token=await hash({request,moves,checks,destination:binding,synthetic_topics});
  return {review:{...request,operation:input.new?request.topic:operation,token,moves,destination:binding,...(synthetic_topics.length?{synthetic_topics}:{})},checks,own};
 }
 async function apply(review){
  if(!wire.validID(review?.operation)||!wire.validHash(review?.token))throw Error(changed);
  const key=operation(review.operation),kept=await e.store.get('kv',key);if(kept){if(JSON.stringify(kept.review)!==JSON.stringify(review))throw Error(changed);return {id:review.operation,lid:review.operation,state:'stored'};}
  let current;try{current=await preview(review);}catch{throw Error(changed);}
  if(current.review.token!==review.token||JSON.stringify(current.review.moves)!==JSON.stringify(review.moves)||!sameDestination(current.review.destination,review.destination)||JSON.stringify(current.review.synthetic_topics||[])!==JSON.stringify(review.synthetic_topics||[])||review.new&&review.topic!==review.operation)throw Error(changed);
  const {checks,own}=current;await read(checks,'kv',key);const prior=await placements();
  const revision=Math.max(0,...prior.map(p=>p.revision))+1;if(!Number.isSafeInteger(revision))throw Error('Direct topic revision limit reached.');
  const ops=review.moves.map(m=>{const p={peer:review.peer,id:m.lid,author:m.author,topic:review.topic,revision,writer:e.fp,...((review.synthetic_topics||[]).includes(review.topic)?{synthetic:true}:{}),destination:review.destination},k=factKey(own.person,p);return {s:'kv',k,v:{...p,key:k,person:own.person}};});
  if(review.new){const titleKey='topic/'+review.peer+'/'+review.topic,prior=await read(checks,'kv',titleKey);ops.push({s:'kv',k:titleKey,v:{...prior,type:'topic-state',peer:review.peer,topic:review.topic,title:review.title}},...await e.topicTitleOps(review.peer,review.topic,review.title,checks));}
  ops.push({s:'kv',k:key,v:{review,revision}},{s:'kv',k:generation(own.person,review.peer),v:wire.newID()});
  try{await e.store.write(ops,checks);}catch(x){if(x instanceof StoreConflict)throw Error(changed);throw x;}
  e.changed();if(review.new)e.syncTopicTitles().catch(()=>{});sync().catch(()=>{});return {id:review.operation,lid:review.operation,state:'stored'};
 }
 async function undo(request){
  if(request.conv||!wire.validID(request.lid)||!wire.validID(request.id)||!wire.validAddress(request.peer))throw Error('Choose the exact topic move.');
  const checks=[],own=await owner(checks),key=operation(request.id),kept=await read(checks,'kv',key);
  if(kept){if(JSON.stringify(kept.undo)!==JSON.stringify(request))throw Error(changed);return [{id:request.id,lid:request.id,state:'stored'}];}
  const original=await read(checks,'kv',operation(request.lid)),review=original?.review;
  if(!review||review.peer!==request.peer)throw Error('The exact topic move is unavailable on this device.');
  await read(checks,'kv',generation(own.person,request.peer));const prior=await placements(),revision=Math.max(0,...prior.map(p=>p.revision))+1;if(!Number.isSafeInteger(revision))throw Error('Direct topic revision limit reached.');
  const held=(await e.v1Threads({inbox:await e.store.byThreadPeer('inbox',request.peer),outbox:await e.store.byThreadPeer('outbox',request.peer)})).flat();
  for(const m of review.moves){const row=held.find(r=>r.id===m.lid&&r.author_fp===m.author);if(!row||!sameDestination(row.destination,review.destination))throw Error(changed);}

  const ops=[];for(const m of review.moves){const k=factKey(own.person,{peer:review.peer,author:m.author,id:m.lid}),p=await read(checks,'kv',k);if(!p||p.topic!==review.topic||p.revision!==original.revision||p.writer!==e.fp||!!p.synthetic!==(review.synthetic_topics||[]).includes(review.topic)||!sameDestination(p.destination,review.destination))throw Error(changed);ops.push({s:'kv',k,v:{...p,topic:m.topic,synthetic:(review.synthetic_topics||[]).includes(m.topic),revision,writer:e.fp}});}
  ops.push({s:'kv',k:key,v:{undo:request,revision}},{s:'kv',k:generation(own.person,request.peer),v:wire.newID()});
  try{await e.store.write(ops,checks);}catch(x){if(x instanceof StoreConflict)throw Error(changed);throw x;}
  e.changed();sync().catch(()=>{});return [{id:request.id,lid:request.id,state:'stored'}];
 }
 let running=null,again=false;
 function sync(){again=true;if(!running)running=(async()=>{do{again=false;try{await e.withHistoryProduction(syncPages);}catch(x){if(x instanceof StoreConflict)again=true;else throw x;}}while(again);})().finally(()=>{running=null;});return running;}
 async function syncPages(){
  const own=await e.store.get('kv','person');if(!e.ownTopicWriter(own))return;let after=prefix(own.person);
  for(;;){const page=(await e.store.after('kv',after,64)).filter(r=>r?.key?.startsWith(prefix(own.person)));if(!page.length)return;after=page.at(-1).key;
   const checks=[],current=await owner(checks),ops=[];if(current.person!==own.person)return;
   for(const dev of current.devices){
    if(dev.address===e.address||!current.human_keys.includes(dev.fingerprint)||!await e.recipientAvailable(dev,checks)||!await stateWindow(e.store,dev))continue;
    const pointer='direct-topic-carrier/'+own.person+'/'+dev.fingerprint,last=await read(checks,'kv',pointer);if(last?.id&&(await read(checks,'outbox',last.id))?.state==='waiting')continue;
    const facts=[];for(const saved of page){const fact=await read(checks,'kv',saved.key);if(!fact)continue;const c=await read(checks,'kv',copyKey(own.person,dev.fingerprint,fact)),row=c?.id&&await read(checks,'outbox',c.id);if(c&&!wire.directTopicNewer(fact,c)&&!wire.directTopicNewer(c,fact)&&(c.seen||row&&['queued','waiting','custody','delivered','quarantined'].includes(row.state)))continue;facts.push(fields(fact));}
    if(!facts.length)continue;const body=JSON.stringify({v:1,person:current.person,roster:current.hash,placements:facts}),r=wire.parseDirectTopicSync(body);try{await e.readSyncAuthority(r,e.address,e.fp,dev.address,dev.fingerprint,checks);}catch{continue;}
    const id=wire.newID(),at=e.now(),envelope=await wire.seal({v:wire.Version2,id,from:e.address,to:dev.address,ts:Math.floor(at/1000),kind:'message',sub:wire.SubDirectTopicSync,replica:true,body},e.keys,await wire.parsePublic(JSON.parse(dev.json)));
    ops.push({s:'outbox',k:id,v:{id,to:dev.address,recipient_fp:dev.fingerprint,required_cap:wire.CapDirectTopicSync,sub:wire.SubDirectTopicSync,body,envelope,at,state:'queued',aside:true}},{s:'kv',k:pointer,v:{id}});
    for(const p of facts)ops.push({s:'kv',k:copyKey(own.person,dev.fingerprint,p),v:{...p,id}});
   }
   if(ops.length){await e.store.write(ops,checks);e.changed();if(e.connected)e.queueOutbox();}if(page.length<64)return;
  }
 }
 async function gate(rec){await e.refreshPerson(e.me);const r=wire.parseDirectTopicSync(rec.body);await e.readSyncAuthority(r,e.address,e.fp,rec.to,rec.recipient_fp);const pin=await e.store.get('pins',rec.to),features=await e.features(),profile=await e.profile(rec.to);if(!features.includes('env2')||!features.includes('caps')||!await wire.profileSupports(profile,rec.to,(await e.pubOf(pin)).sign_key,wire.CapDirectTopicSync))throw Object.assign(Error('This device needs to update AgentNet to synchronize direct-agent topic organization.'),{code:'topic_sync_unsupported'});await e.readSyncAuthority(r,e.address,e.fp,rec.to,rec.recipient_fp);return {why:'',pin};}
 async function admit(n,env,pin,admission){const r=wire.parseDirectTopicSync(n.body),checks=[],ops=[],peers=new Set();if(!e.me||e.me.person!==r.person)throw new Hold('invalid','Direct topics belong to another person.');await e.refreshPerson(e.me,admission?{...admission,roster:r.roster}:null);try{await e.readSyncAuthority(r,env.from,pin.fingerprint,e.address,e.fp,checks);}catch(x){throw new Hold('invalid',x.message);}
  for(const p of r.placements){const k=factKey(r.person,p),old=await read(checks,'kv',k);if(wire.directTopicNewer(p,old)){ops.push({s:'kv',k,v:{...p,key:k,person:r.person}});peers.add(p.peer);}const ck=copyKey(r.person,pin.fingerprint,p),copy=await read(checks,'kv',ck);if(wire.directTopicNewer(p,copy))ops.push({s:'kv',k:ck,v:{...p,seen:true}});}
  if(peers.size){const k=generation(r.person);await read(checks,'kv',k);ops.push({s:'kv',k,v:wire.newID()});}ops.checks=checks;ops.topicSync=true;ops.syncCarrier=true;return ops;
 }
 return {placements,preview,apply,undo,sync,gate,admit};
}
