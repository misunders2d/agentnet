import * as wire from '../static/wire.mjs';
export async function participationTopics({world,check,c,root,conv,keys,address,pub,alicePub,aliceKeys,v}) {
 const w=await world();
 try {
  for(const vector of v.topic_events){const e=wire.parseEvent(vector.event),s=wire.parseEvent(vector.scope);await wire.verifyEvent(e,alicePub.sign_key);check(wire.eventJSON(e)===vector.event&&await wire.eventHash(e)===vector.hash&&wire.eventJSON(s)===vector.scope&&wire.projectsHash(s,e,vector.hash),'native/browser exact canonical scoped event '+(e.topic||'Main'));}
  await w.receive(c.proof);await w.receive(c.context);
  const topic=wire.newID(),aliceFP=await wire.fingerprint(alicePub),packet=await w.e.groupCurrent(conv),stamp=await wire.groupAdmissionHash(wire.groupMember(packet.state,v.challenge.rosters[0].person).admission);
  const author={person:v.challenge.rosters[0].person,roster:await wire.rosterHash(await wire.parseRoster(v.challenge.rosters[0])),address:alicePub.address,fingerprint:aliceFP,group_admission:stamp};
  const invitation=await wire.signEvent(aliceKeys,{conv,pid:wire.newID(),type:'invite',ts:1700000100,author,host:{person:author.person,address:alicePub.address,fingerprint:aliceFP},audience:'room',topic,group:{seq:packet.state.seq,hash:await wire.groupStateHash(packet.state),host_role:'member',host_admission:stamp}});
  const accepted=await wire.signEvent(aliceKeys,{conv,pid:invitation.pid,type:'accept',ts:1700000101,author,prev:await wire.eventHash(invitation)}),scope=await wire.signEvent(aliceKeys,await wire.scopeOf(invitation,1700000100));
  const send=async fields=>{const id=wire.newID(),inner={v:2,id,lid:wire.newID(),from:alicePub.address,to:address,ts:1700000102,kind:'message',conv,root:wire.rootJSON(root),body:'Synthetic scoped turn',...fields};await w.receive({envelope:await wire.seal(inner,aliceKeys,pub)});return inner;};
  for(const e of [invitation,scope,accepted]){const row=await send({pid:e.pid,sub:'event',body:wire.eventJSON(e)});check(!!await w.st.get('inbox',row.id),'signed scoped '+e.type+' admits');}
  const info=(await w.e.agentConv(invitation.pid)).info;
  check(info.topic===topic&&info.state==='active','signed scope remains exact stable topic');
  check(wire.parseEvent(wire.eventJSON(invitation)).topic===topic&&wire.projectsHash(scope,invitation,await wire.eventHash(invitation)),'scoped wire canonical projection matches');
  const unscoped={...scope};delete unscoped.topic;check(!wire.projectsHash(unscoped,invitation,await wire.eventHash(invitation)),'absent scope cannot stand in for topic invitation');
  const human={audience:[{pid:invitation.pid,invite:await wire.eventHash(invitation),decision:await wire.eventHash(accepted)}],proof:[scope,accepted]};
  const original=await send({topic,human});check(!!await w.st.get('inbox',original.id),'same-topic captured turn admits');
  const other=await send({topic:wire.newID(),human});check(!await w.st.get('inbox',other.id)&&(await w.st.get('held',other.id))?.reason==='invalid','other-topic captured audience fails closed');
  const absent=await send({reply_to:wire.newID(),human});check(!await w.st.get('inbox',absent.id)&&(await w.st.get('held',absent.id))?.reason==='proof_pending','absent parent does not become Main');
  const reply=await send({reply_to:original.lid,human});check(!!await w.st.get('inbox',reply.id),'untagged reply inherits verified topic ancestry');
  const before=(await w.st.all('outbox')).length;
  let selectedRefused=false;try{await w.e.commitReceiverCopies([{...original,id:wire.newID(),to:alicePub.address}],{delegation:{receiver_setup:{choice:{kind:'live_session'}}}},[]);}catch(e){selectedRefused=e.message.includes('Choose Yourself');}
  check(selectedRefused&&(await w.st.all('outbox')).length===before,'selected scoped native receiver refuses before partial outbox commit');
  check(await w.e.prepareReceiverRequest({kind:'human'},{conv,topic},[],[])===null,'ordinary human receiver remains unchanged');
  check(!await w.e.topicCopy({conv,kind:'question',body:'whole chat'}),'whole-chat selected receiver is not topic-restricted');
  const current=await w.e.groupRecord(conv),events=await w.e.convEvents(conv),members=await w.e.dmMembers(current,events);
  check((await w.e.roomPlan(current,events,members,topic)).audience.length===1&&!await w.e.roomPlan(current,events,members,''),'ongoing fanout follows topic only');
  let refused=false;try{await w.e.checkTopicGrants(conv,'',[{lid:original.lid,fingerprint:aliceFP}]);}catch{refused=true;}check(refused,'selected grant outside Main refused');
  await w.e.checkTopicGrants(conv,topic,[{lid:reply.lid,fingerprint:aliceFP}]);check(true,'selected descendant exact grant accepted');
  await w.e.checkParticipationTopic('',{conv,kind:'message'});check(true,'explicit Main accepts actual unassigned message');
  const snapshot=[];await w.e.checkParticipationTopic(topic,{conv,reply_to:reply.lid},snapshot);
  const row=await w.st.get('inbox',reply.id);await w.st.write([{s:'inbox',k:row.id,v:{...row,topic:wire.newID()}}]);
  let conflict=false;try{await w.st.write([],snapshot);}catch{conflict=true;}check(conflict,'topic authority change invalidates atomic captured source snapshot');await w.st.write([{s:'inbox',k:row.id,v:row}]);
  const legacy=await wire.signEvent(aliceKeys,{...invitation,pid:wire.newID(),topic:undefined});
  check(await wire.eventHash(legacy)!==await wire.eventHash({...legacy,topic:''}),'explicit Main differs from legacy whole-chat signature');
  const profile=w.e.profile,session=wire.newID(),pin=await w.st.get('pins',alicePub.address);
  try {
   let caps=await wire.newCaps(aliceKeys,alicePub.address,session,[wire.CapEnv2,wire.CapPerson,wire.CapRoom]);
   w.e.profile=async()=>({sessions:[session],caps:[JSON.parse(wire.capsJSON(caps))]});
   let old=false;try{await w.e.requireTopicSupport(alicePub.address,pin);}catch(e){old=e.code==='topic_unsupported';}check(old,'signed old peer room capability does not imply topic scope');
   caps=await wire.newCaps(aliceKeys,alicePub.address,session,[wire.CapEnv2,wire.CapPerson,wire.CapRoom,wire.CapTopicParticipation]);await w.e.requireTopicSupport(alicePub.address,pin);check(true,'explicit signed topic capability accepted');
  } finally {w.e.profile=profile;}
  await w.reload();check((await w.e.agentConv(invitation.pid)).info.topic===topic,'scope survives restart');
  const view=w.e.agentView((await w.e.agentConv(invitation.pid)).info,[],null,[...members.values()],'member');check(view.topic===topic&&view.state_text.includes('only in this topic')&&!view.state_text.includes('every new'),'scoped participant projection avoids whole-group promise');
 } finally {await w.close();}
}
