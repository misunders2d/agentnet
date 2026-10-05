// Native-produced sealed carrier vectors through actual Engine. Synthetic
// signed-request fetch only; Chrome mode uses real IndexedDB, Node memory is unit-only.
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, openIDB } from '../static/engine.mjs';
let keys, address = 'browser/desk', roster, pub;
const decode = new TextDecoder();
const assert = (ok, label) => { if (!ok) throw Error(label); };
const bytes = (s) => wire.unb64(s);
export async function setup() {
 keys = await wire.newKeys(); pub = await wire.publicEntry(keys,address); roster = await wire.newRoster(keys,address,'Browser');
 return {public:wire.marshalPublic(pub),roster:wire.rosterJSON(roster)};
}
export async function consent(challenge) {
 const root = wire.parseGroupRoot(challenge.root);
 const a = await wire.signGroupAdmission(keys,{conv:await wire.rootID(root),realm:root.realm,person:roster.person,roster:await wire.rosterHash(roster),seq:0,prev:'',history:null,by:await wire.fingerprint(pub)});
 return wire.groupAdmissionJSON(a);
}
export async function checks(v, realIDB=false, requireWarmRecovery=false) {
 const labels=[], check=(ok,label)=>{assert(ok,label);labels.push(label);};
 const root=wire.parseGroupRoot(v.challenge.root), conv=await wire.rootID(root), states=v.states.map(wire.parseGroupState), records=v.commits.map(wire.parseGroupCommit), c=v.carriers;
 const invitation=await wire.validateGroupInvitation(wire.parseGroupInvitation(v.invitation_json));
 check(wire.groupInvitationJSON(invitation)===v.invitation_json,'native group invitation JSON exact bytes');
 check(await wire.groupInvitationID(invitation)===v.invitation_id,'native group invitation ID exact hash');
 check(wire.groupConsentJSON(wire.parseGroupConsent(v.decline_json))===v.decline_json,'native explicit group decline JSON exact bytes');
 check(wire.groupConsentJSON(wire.parseGroupConsent(v.accept_json))===v.accept_json,'native signed accepted consent selected history exact bytes');
 const liveHistory=wire.parseHistory(v.live_history_json);
 check(wire.historyJSON(liveHistory)===v.live_history_json,'native linked-live history admission stamp exact bytes');
 check(await wire.groupHistoryContentHash('1'.repeat(32),liveHistory)===v.history_hash,'native exact ordered selected file content hash');
 const alicePub=await wire.parsePublic(v.challenge.rosters[0].devices[0]);
 const aliceKeys={sign:await crypto.subtle.importKey('pkcs8',bytes(v.challenge.alice_private),{name:'Ed25519'},false,['sign'])};
 const names=[];
 const world=async()=>{
  const name='agentnet-group-1002h-'+wire.newID();names.push(name);
  let st=realIDB?await openIDB(name):memoryStore();
  const chains=new Map([...v.challenge.rosters,v.invited_roster].map(r=>[r.person,[r]])), blobs=new Map(), receipts=[];
  let mode='', extraChains=new Map();
  for(const x of Object.values(c))blobs.set(x.blob,bytes(x.ct));
  for(const x of v.participations.files)blobs.set(x.blob,bytes(x.ct));
  const fetch=async(url,o)=>{
   const u=new URL(url);
   if(u.pathname.startsWith('/v1/blobs/')){const id=u.pathname.split('/')[3],b=blobs.get(id);if(!b)return new Response('',{status:404});if(mode==='offline')throw Error('fixture offline');if(mode==='short')return new Response(b.slice(0,-2));if(mode==='empty')return new Response(null);if(mode==='oversize'){const x=new Uint8Array(b.length+1);x.set(b);return new Response(x);}if(mode==='stream-error')return new Response(new ReadableStream({start(controller){controller.enqueue(b.slice(0,16));controller.error(Error('fixture interrupted'));}}));if(mode==='corrupt'){const x=b.slice();x[x.length-1]^=1;return new Response(x);}return new Response(b);}
   if(u.pathname.startsWith('/v1/persons/')){const person=u.pathname.split('/')[3],chain=extraChains.get(person)||chains.get(person)||[];const after=Number(u.searchParams.get('after'));return new Response(JSON.stringify({records:chain.filter(r=>r.seq>after),more:false}));}
   if(u.pathname==='/v1/receipts'){receipts.push(JSON.parse(o.body));return new Response('{}');}
   throw Error('unexpected fixture fetch '+u.pathname);
  };
  const recovery=new Set(),track=engine=>{const run=engine.recoverGroupIntents.bind(engine);engine.recoverGroupIntents=(...args)=>{const work=run(...args);recovery.add(work);work.then(()=>recovery.delete(work),()=>recovery.delete(work));return work;};};
  const drain=async()=>{if(e.retrying)await e.retrying;while(recovery.size)await Promise.allSettled([...recovery]);};
  let e=new Engine({store:st,base:'http://127.0.0.1:1',fetch});track(e);e.keys=keys;e.address=address;e.fp=await wire.fingerprint(pub);e.realm=root.realm;
  for(const raw of [...v.challenge.rosters,v.invited_roster]){const r=await wire.parseRoster(raw);await wire.verifyFirst(r);const p=await e.personRecord([r],r.person===roster.person?'self':'pinned',null);if(r.person===roster.person){e.me=p;await st.write([{s:'kv',k:'person',v:p}]);}else await st.write([{s:'persons',k:p.person,v:p}]);await e.pinDevices(p);}
  await st.write([{s:'kv',k:'identity',v:{keys,address,fingerprint:e.fp}}]);
  const receive=async(x)=>{await e.onMessage(x.envelope);await e.retryHeld();};
  const make=async(sub,payload,seq,hash,fields={})=>{
   const file=await wire.encryptFile(new TextEncoder().encode(JSON.stringify(payload)),sub+'.json',pub);blobs.set(file.attachment.blob.id,file.ct);
   const inner={v:2,id:wire.newID(),from:alicePub.address,to:address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),lid:wire.newID(),sub,body:wire.groupCarrierJSON({v:1,seq,hash,to_key:e.fp}),attachments:[file.attachment],...fields};
   return {envelope:await wire.seal(inner,aliceKeys,pub),blob:file.attachment.blob.id};
  };
  return {get e(){return e;},st,name,receive,make,blobs,receipts,mode:x=>mode=x,extraChains,
   async reload(){await drain();st.close();st=realIDB?await openIDB(name):st;e=new Engine({store:st,base:'http://127.0.0.1:1',fetch});track(e);await e.load();e.realm=root.realm;this.st=st;},
   async close(){await drain();st.close();if(realIDB)await new Promise((res,rej)=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=res;r.onerror=()=>rej(r.error);r.onblocked=()=>rej(Error('fixture database blocked after '+labels.at(-1)));});}};
 };
 const quiet=async(w)=>{for(const s of ['inbox','outbox','convs','lids'])check((await w.st.all(s)).length===0,'quiet '+s);check((await w.e.overview()).threads.length===0,'no visible threads');for(const x of await w.st.all('held'))check(!('body'in x)&&!('plaintext'in x),'held has ciphertext only');for(const x of await w.st.all('files'))check(x.ct instanceof Uint8Array&&!('body'in x),'files ciphertext only');};
 let w;
 try{
  // A set check must notice records that did not exist during verification,
  // not just changes to already-read keys. A second real IDB connection is
  // the competing admission; no synthetic lock or store replacement.
  w=await world();
  {
   const id=wire.newID(),row={id,conv:'receipt-scope',body:'SIGNED',state:'custody',detail:''};
   await w.st.write([{s:'outbox',k:id,v:row}]);
   const checks=[];await w.e.authorityRows({conv:'receipt-scope'},checks);
   const other=realIDB?await openIDB(w.name):w.st;
   await other.write([{s:'outbox',k:id,v:{...row,state:'delivered',detail:''}}]);
   await w.st.write([{s:'kv',k:'receipt-scope-pass',v:true}],checks);
   check(await w.st.get('kv','receipt-scope-pass')===true,'delivery update from another connection preserves authority');
   await other.write([{s:'outbox',k:id,v:{...row,body:'ALTERED'}}]);
   let refused=false;try{await w.st.write([{s:'kv',k:'receipt-scope-pass',v:false}],checks);}catch(e){refused=e.message==='storage changed during verification';}
   check(refused,'signed content mutation still invalidates authority');
   if(other!==w.st)other.close();await w.st.write([{s:'outbox',k:id}]);
  }
  for(const mode of ['new-dismissal','new-invite','new-request','delete-request','change-request','unrelated']){
   const pid=wire.newID(),lid=wire.newID(),scope=mode.includes('request')?{conv,lid}:{conv,pid,sub:'event'};
   const initial={id:wire.newID(),conv,pid,lid,sub:scope.sub||'',body:'verified source',kind:scope.sub?'message':'question'};
   await w.st.write([{s:'inbox',k:initial.id,v:initial}]);
   const checks=[];await w.e.authorityRows(scope,checks);
   const other=realIDB?await openIDB(w.name):w.st;
   const added={...initial,id:wire.newID(),body:mode};
   const mutation=mode==='delete-request'?{s:'inbox',k:initial.id}:mode==='change-request'?{s:'inbox',k:initial.id,v:{...initial,body:mode}}:{s:'inbox',k:added.id,v:mode==='unrelated'?{...added,conv:wire.newID()}:added};
   await other.write([mutation]);if(other!==w.st)other.close();
   const stored=wire.newID();let conflict=false;
   try{await w.st.write([{s:'inbox',k:stored,v:{id:stored,conv,body:'new output'}},{s:'receipts',k:stored,v:{state:'delivered'}}],checks);}catch(e){conflict=e.message==='storage changed during verification';}
   check(conflict===(mode!=='unrelated'),'authority conditional set '+mode+' exact scope');
   check(!!await w.st.get('receipts',stored)===(mode==='unrelated'),'authority set abort prevents receipt '+mode);
   check(!!await w.st.get('inbox',stored)===(mode==='unrelated'),'authority set abort prevents partial output '+mode);
   if(conflict){await w.reload();const fresh=[];await w.e.authorityRows(scope,fresh);await w.st.write([{s:'kv',k:'fresh-authority-read',v:mode}],fresh);check(await w.st.get('kv','fresh-authority-read')===mode,'authority set fresh durable verification '+mode);}
   const remove=(await w.st.all('inbox')).map(r=>({s:'inbox',k:r.id}));if(remove.length)await w.st.write(remove);
  }
  await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  const pv=v.participations;
  for(const i of [0,1])for(const type of ['invite','scope','accept'])await w.receive(pv['p6-'+i+'-'+type]);
  check((await w.e.agentConv(pv['p6-source'])).info.member && (await w.e.agentConv(pv['p6-target'])).info.member,'permanent group agent membership has no expiry');
  await w.receive(pv['p6-ordinary']);check((await w.e.groupThread(conv)).messages.some(m=>m.body==='P6 future group message'),'native captured future group message is admitted without execution');
  await w.receive(pv['p6-root']);await w.receive(pv['p6-ask']);await w.receive(pv['p6-status']);
  let p6=await w.e.groupThread(conv),q=p6.messages.find(m=>m.lid===pv['p6-ask'].inner.lid);
  check(q?.verified_agent && q.agent_author_pid===pv['p6-source'] && q.pid===pv['p6-target'],'agent asks retain verified author distinct from target');
  check(q?.exec?.state==='awaiting','agent request owner approval is correlated to its exact request');
  await w.receive(pv['p6-answer']);
  p6=await w.e.groupThread(conv);
  check(p6.messages.some(m=>m.body==='P6 verified agent answer'&&m.verified_agent&&m.reply_to===q.id),'captured group answer read and correlated');
  await w.receive(pv['p6-wrong-correlation']);check((await w.st.get('held',pv['p6-wrong-correlation'].inner.id))?.reason==='invalid','output to a different agent request rejected');
  await w.receive(pv['p6-wrong-author']);check((await w.st.get('held',pv['p6-wrong-author'].inner.id))?.reason==='invalid','claimed asking agent does not replace exact host author');
  await w.receive(pv['p6-share']);check((await w.e.agentConv(pv['p6-target'])).info.grant.length===1,'signed sharing adds exact grant without a second membership');
  await w.reload();p6=await w.e.groupThread(conv);check(p6.agents.filter(a=>a.pid===pv['p6-target']).length===1 && p6.agents.some(a=>a.pid===pv['p6-target']&&a.member),'browser reload keeps one permanent agent card');

  for(const role of ['member','visitor']){
   await w.receive(pv[role+'-invite']);await w.receive(pv[role+'-accept']);
   check((await w.e.agentConv(pv[role+'-pid'])).info.state==='active','native '+role+' named group participation consciously accepted');
   await w.receive(pv[role+'-question']);await w.receive(pv[role+'-status']);
   check((await w.e.groupThread(conv)).messages.find(m=>m.id===pv[role+'-question'].inner.id)?.exec?.state==='running','native '+role+' status bound to exact request host');
   await w.receive(pv[role+'-answer']);
   const shown=await w.e.groupThread(conv),answer=shown.messages.find(m=>m.agent_id===pv[role+'-agent']&&m.kind==='answer');
   check(!!answer&&answer.pid===pv[role+'-pid']&&answer.reply_to===pv[role+'-question'].inner.id&&shown.messages.find(m=>m.id===answer.reply_to)?.lid===pv[role+'-question'].inner.lid,'native '+role+' output exact PID AgentID linked to its shared request LID as shown');
   check(!shown.messages.find(m=>m.id===pv[role+'-question'].inner.id)?.exec,'native '+role+' answer supersedes running snapshot status');
   await w.receive(pv[role+'-wrong-agent']);check((await w.st.get('held',pv[role+'-wrong-agent'].inner.id))?.reason==='invalid','native '+role+' wrong named output refused');
   // The assistant's own reaction: its exact host, PID and agent; stored only
   // with this device's own exact member admission; shown as the assistant.
   await w.receive(pv[role+'-assistant-reaction']);
   const reacted=await w.st.get('inbox',pv[role+'-assistant-reaction'].inner.id),ownStamp=(await w.e.dmMembers(await w.e.groupRecord(conv))).epochs.get(w.e.fp);
   check(!!ownStamp&&reacted?.group_admission===ownStamp&&reacted.pid===pv[role+'-pid']&&reacted.agent_id===pv[role+'-agent']&&reacted.person==='','native '+role+' assistant reaction stored with exact own member admission');
   const rq=(await w.e.groupThread(conv)).messages.find(m=>m.id===pv[role+'-question'].inner.id),rg=rq?.reactions?.find(r=>r.emoji==='🎉'),rb=rg?.by||[];
   check(rb.length===1&&rb[0].id==='assistant:'+pv[role+'-pid']&&rb[0].assistant&&rb[0].pid===pv[role+'-pid']&&rb[0].agent_id===pv[role+'-agent']&&rb[0].host===pv[role+'-assistant-reaction'].inner.from&&!rg.mine,'native '+role+' group reactor is the assistant, never its host');
   await w.receive(pv[role+'-assistant-wrong-agent']);check((await w.st.get('held',pv[role+'-assistant-wrong-agent'].inner.id))?.reason==='invalid','native '+role+' reaction naming another agent refused');
  }
  await w.receive(pv['ordinary-reaction']);check((await w.st.get('held',pv['ordinary-reaction'].inner.id))?.reason==='proof_pending','group reordered reaction held until exact ordinary original');
  await w.receive(pv.ordinary);await w.e.retryHeld();
  check((await w.e.groupThread(conv)).messages.find(m=>m.id===pv.ordinary.inner.id)?.reactions?.[0]?.emoji==='👍','native authenticated current member group reaction resolves after original');
  await w.receive(pv['ordinary-revision']);check((await w.e.groupThread(conv)).messages.find(m=>m.id===pv.ordinary.inner.id)?.text==='Exact ordinary revised','native exact author group revision shown');
  for(const sub of ['reaction','revision','retraction']){await w.receive(pv['visitor-'+sub]);check((await w.st.get('held',pv['visitor-'+sub].inner.id))?.reason==='invalid','outside visitor has no ordinary group '+sub+' authority');}
  // Insert a newly signed dismissal after verification but before the same
  // transaction commits the reply and receipt. Fresh admission must refuse.
  const role='visitor',originalWrite=w.st.write.bind(w.st);let racedPID=false;
  await w.st.write([{s:'inbox',k:pv[role+'-answer'].inner.id},{s:'lids',k:(await wire.fingerprint(await wire.parsePublic(v.invited_roster.devices[0])))+'/'+pv[role+'-answer'].inner.lid}]);
  w.st.write=async(ops,checks)=>{if(!racedPID&&ops.some(o=>o.s==='inbox'&&o.k===pv[role+'-answer'].inner.id)){
    racedPID=true;const n=pv[role+'-dismiss'].inner,other=realIDB?await openIDB(w.name):w.st;
    await(other===w.st?originalWrite:other.write.bind(other))([{s:'inbox',k:n.id,v:{...n,at:n.ts*1000,fp:await wire.fingerprint(alicePub),read:true}}]);if(other!==w.st)other.close();
   }return originalWrite(ops,checks);};
  const answerEnv=wire.parseEnvelope(pv[role+'-answer'].envelope);await w.e.admit(pv[role+'-answer'].envelope,answerEnv);w.st.write=originalWrite;
  check(racedPID&&!await w.st.get('inbox',answerEnv.id)&&(await w.st.get('held',answerEnv.id))?.reason==='invalid','real competing signed dismissal forces fresh PID refusal without partial output');
  await w.reload();check((await w.e.agentConv(pv[role+'-pid'])).info.state==='dismissed','competing dismissal and refused output survive real reload');await w.close();
  // Actual native HistoryItem bytes, forwarded by a newly linked own key.
  // The original participant events stay signed; all replicas remain inert.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  const linkedKeys=await wire.newKeys(),linkedAddress='browser/phone',linkedPub=await wire.publicEntry(linkedKeys,linkedAddress);
  const linkedRoster=await wire.nextRoster(keys,address,roster,[...roster.devices,linkedPub],await wire.joinConsent(linkedKeys,linkedAddress,roster.person,1,await wire.rosterHash(roster)));
  await wire.verifyNext(linkedRoster,roster);w.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
  const linkedPerson=await w.e.personRecord([roster,linkedRoster],'self',null);w.e.me=linkedPerson;await w.st.write([{s:'kv',k:'person',v:linkedPerson}]);await w.e.pinDevices(linkedPerson);
  const historyEnvelope=async body=>wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:linkedAddress,to:address,ts:1700000101,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body},linkedKeys,pub);
  for(const role of ['member','visitor']) {
   for(const suffix of ['invite','accept','question','status','answer','assistant-reaction']) {
    const body=pv['history-'+role+'-'+suffix];check(wire.historyJSON(wire.parseHistory(body))===body,'native '+role+' '+suffix+' linked history exact bytes');
    const env=await historyEnvelope(body);await w.receive({envelope:env});
    check(!await w.st.get('held',wire.parseEnvelope(env).id),'native '+role+' '+suffix+' own linked history admitted');
   }
  }
  const linkedView=await w.e.groupThread(conv);
  check(linkedView.agents.filter(a=>a.state==='active').length===2,'late linked browser restores exact two named participations');
  check(linkedView.messages.filter(m=>m.kind==='answer').length===2&&linkedView.messages.every(m=>!m.unread&&!!m.synced_from),'late linked PID outputs retain source attribution and remain quiet');
  for(const role of ['member','visitor']){const lq=linkedView.messages.find(m=>m.id===pv[role+'-question'].inner.id),lb=lq?.reactions?.find(r=>r.emoji==='🎉')?.by||[];
   check(lb.length===1&&lb[0].id==='assistant:'+pv[role+'-pid']&&lb[0].assistant,'late linked browser restores the '+role+' assistant reactor from own history');}
  const otherStamp=JSON.parse(pv['history-member-assistant-reaction']);otherStamp.group_admission='e'.repeat(64);const otherStampEnv=await historyEnvelope(JSON.stringify(otherStamp));await w.receive({envelope:otherStampEnv});
  check((await w.st.get('held',wire.parseEnvelope(otherStampEnv).id))?.reason==='invalid','assistant reaction history refuses a different own admission');
  // This browser's own history copy of an assistant reaction for its linked
  // phone keeps grp1 and waits there for agr1 (client.deliver parity).
  {
   const phoneSession=wire.newID(),phoneProfile=async names=>({live:true,sessions:[phoneSession],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(linkedKeys,linkedAddress,phoneSession,names)))]});
   let profile=await phoneProfile([wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapGroup,wire.CapAgentIdentity,wire.CapExternalParticipation]);
   const posted=[],call=w.e.call.bind(w.e);
   w.e.profile=async a=>a===linkedAddress?profile:null;w.e.groupSupport=async()=>{};w.e.ctlSupport=async()=>[true,''];
   w.e.call=async(m,p,b)=>p==='/v1/messages'?(posted.push(b),{state:'custody'}):call(m,p,b);
   const source=await w.st.get('inbox',pv['member-assistant-reaction'].inner.id),phoneDev=w.e.me.devices.find(d=>d.address===linkedAddress);
   const copy=await w.e.historyCopy(phoneDev,await w.e.groupRecord(conv),w.e.itemOf(source,false));
   check(copy.required_cap===wire.CapGroup&&wire.historyAssistantReaction(wire.parseHistory(copy.body)),'own history copy keeps its group requirement');
   await w.st.write([{s:'outbox',k:copy.id,v:copy}]);await w.e.post(copy);
   check((await w.st.get('outbox',copy.id)).state==='waiting'&&!posted.length,'an own device without agr1 waits; nothing posted');
   profile=await phoneProfile([wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapGroup,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapAgentReaction]);
   await w.e.post({...(await w.st.get('outbox',copy.id)),state:'queued',detail:''});
   const after=await w.st.get('outbox',copy.id);
   check(after.state==='custody'&&posted.length===1&&wire.parseEnvelope(posted[0]).id===copy.id,'agr1 signed releases and posts the exact copy: '+after.state+' '+(after.detail||''));
   w.e.call=call;
  }
  const linkedRequest=await w.st.get('inbox',pv['member-question'].inner.id),memberHistory=wire.parseHistory(pv['history-member-question']);
  check(linkedRequest.attachments.map(f=>f.name).join(',')==='z.txt,a.txt','native historical PID manifests retain original unsorted index order');
  const filePacket=(await w.e.groupTurnEvidence(conv)),selfMember=wire.groupMember(filePacket.packet.state,w.e.me.person);
  for(const [index,file] of linkedRequest.attachments.entries()) {
   const exactFile={v:1,type:'request',lid:linkedRequest.lid,author:linkedRequest.claimed_key,hash:await wire.groupHistoryContentHash(conv,linkedRequest),index,name:file.name,size:file.size,sha256:file.sha256,group_admission:await wire.groupAdmissionHash(selfMember.admission)};
   const original=await w.e.groupFileSource(conv,exactFile);check(original.source_dir==='in'&&original.pid===linkedRequest.pid,'own linked exact PID file source direction '+file.name);
   await w.e.groupFileAuthorized(filePacket.packet,filePacket.members,address,w.e.fp,exactFile);
   let wrongIndex=false;try{await w.e.groupFileSource(conv,{...exactFile,index:1-index});}catch{wrongIndex=true;}check(wrongIndex,'historical PID file wrong manifest index refused '+file.name);
   const nativeFile=pv.files.find(f=>f.name===file.name),nativeAttachment=pv['member-question'].inner.attachments[index];
   check(decode.decode(await wire.decryptFile(bytes(nativeFile.ct),nativeAttachment,keys))===nativeFile.bytes,'actual native PID current file decrypts exact bytes '+file.name);
   let foreign=false;try{await w.e.groupFileAuthorized(filePacket.packet,filePacket.members,alicePub.address,await wire.fingerprint(alicePub),{...exactFile,group_admission:await wire.groupAdmissionHash(wire.groupMember(filePacket.packet.state,wire.parseEvent(pv['member-invite'].inner.body).author.person).admission)});}catch{foreign=true;}check(foreign,'PID historical bytes cannot be shared to another group person '+file.name);
  }
  w.e.groupSupport=async()=>{}; // Existing synthetic signed transport boundary; real capability negotiation is the rendered gate.
  await w.e.requestGroupFile(linkedRequest,0);
  const requestedPID=await w.st.get('inbox',linkedRequest.id), requestJobs=(await w.st.all('kv')).filter(x=>x?.message?.type==='request'&&x.message.lid===linkedRequest.lid), requestOut=(await w.st.all('outbox')).filter(x=>x.sub==='file');
  check(requestedPID.attachments[0].availability==='requested'&&linkedRequest.attachments[0].availability==='requestable'&&requestJobs.length===1&&requestOut.length===1,'actual own-linked PID file request preserves expected snapshot and commits one atomic request');
  await w.e.requestGroupFile(requestedPID,0);check((await w.st.all('outbox')).filter(x=>x.sub==='file').length===1,'repeat exact pending PID file request does not enqueue duplicate');
  try {
  const fileOffer={...requestJobs[0].message,type:'offer',available:true}, offered=await wire.encryptFile(new TextEncoder().encode(pv.files[0].bytes),fileOffer.name,pub);
  w.blobs.set(offered.attachment.blob.id,offered.ct);
  const fileOfferEnv=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:linkedAddress,to:address,ts:1700000102,kind:'message',conv,root:wire.rootJSON(root),sub:'file',replica:true,body:wire.groupFileMsgJSON(fileOffer),attachments:[offered.attachment]},linkedKeys,pub);
  const exactOffer=wire.parseEnvelope(fileOfferEnv), offerOps=await w.e.admitInner(fileOfferEnv,exactOffer);
  offerOps.push({s:"receipts",k:exactOffer.id,v:{id:exactOffer.id,state:"delivered"}});
  const offerExpected=offerOps.checks.find(c=>c.s==='inbox'&&c.k===linkedRequest.id&&!c.scope),beforeOffer=await w.st.get('inbox',linkedRequest.id);
  if(JSON.stringify(offerExpected.v)!==JSON.stringify(beforeOffer))return {error:'Signed file offer mutates its own transaction expected snapshot',pendingUnchanged:beforeOffer.attachments[0].availability==='requested',receiptAbsent:!await w.st.get('receipts',exactOffer.id)};
  await w.st.write(offerOps,offerOps.checks);
  const offeredPID=await w.st.get('inbox',linkedRequest.id), offeredHeld=await w.st.get('held',wire.parseEnvelope(fileOfferEnv).id);
  check(!offeredHeld&&offeredPID.attachments[0].blob?.id===offered.attachment.blob.id,'actual exact own-linked PID file offer installs without snapshot conflict '+JSON.stringify(offeredHeld));
  check(!(await w.st.all('kv')).some(x=>x?.message?.type==='request'&&x.message.lid===linkedRequest.lid),'exact file offer clears pending request atomically');
  const openedOwnHistory=await w.e.openFile(linkedRequest.id,0,'in');check(decode.decode(openedOwnHistory.bytes)===pv.files[0].bytes,'installed signed own-linked PID file opens exact received bytes');
  let wrongPhysical=false;try{await w.e.openFile(linkedRequest.id,0,'out');}catch{wrongPhysical=true;}check(wrongPhysical,'outbox direction does not silently fall back to received group history');

  } catch(e) {return {error:e.stack,stage:'signed own-linked PID file offer transaction'};}
  const blobHistory=JSON.parse(pv['history-member-question']);blobHistory.attachments[0].blob={id:wire.newID(),size:1,sha256:'e'.repeat(64)};const blobWrapper=await historyEnvelope(JSON.stringify(blobHistory));await w.receive({envelope:blobWrapper});check((await w.st.get('held',wire.parseEnvelope(blobWrapper).id))?.reason==='invalid','PID history refuses ciphertext descriptors before normalization');
  const wrongStamp=JSON.parse(pv['history-member-question']);wrongStamp.group_admission='e'.repeat(64);const wrongHistory=await historyEnvelope(JSON.stringify(wrongStamp));await w.receive({envelope:wrongHistory});
  check((await w.st.get('held',wire.parseEnvelope(wrongHistory).id))?.reason==='invalid','late linked history refuses different own admission');
  const alien=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000101,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:pv['history-member-answer']},aliceKeys,pub);
  await w.receive({envelope:alien});check((await w.st.get('held',wire.parseEnvelope(alien).id))?.reason==='invalid','other member cannot forward own linked PID history');
  await w.reload();check((await w.e.groupThread(conv)).messages.filter(m=>m.kind==='answer').length===2,'late linked participation history survives durable reload');await w.close();
  w=await world();const outside=v.outside_browser, outsideConv=outside.state.conv;
  for(const sub of ['group-proof','group-context'])w.blobs.set(outside[sub+'-file'].blob,bytes(outside[sub+'-file'].ct));
  await w.receive(outside['group-context']);check((await w.st.get('held',outside['group-context'].inner.id))?.reason==='proof_pending','outside browser context before exact signed invite stays held');
  await w.receive(outside.invite);check((await w.st.get('kv','group/'+outsideConv))?.context==='','outside browser signed invitation installs pending root only');
  await w.receive(outside['group-proof']);await w.e.retryHeld();
  const visitor=await w.e.groupThread(outsideConv);check(visitor.role==='visitor'&&!visitor.frozen&&visitor.agents[0]?.state==='invited','outside browser receives exact current context as visitor only');
  check(!visitor.agents[0]?.can_ask&&!visitor.agents[0]?.can_dismiss&&visitor.messages.every(m=>!m.can.length),'outside browser visitor has no ordinary member actions or executor');
  await w.receive(outside.ordinary);check(!await w.st.get('inbox',outside.ordinary.inner.id),'outside browser visitor cannot admit ordinary room plaintext');
  let visitorSendRefused=false;try{await w.e.sendDM({conv:outsideConv,body:'visitor cannot send'});}catch(e){visitorSendRefused=true;}check(visitorSendRefused&&(await w.st.all('outbox')).length===0,'outside browser visitor cannot send ordinary group turn or stage fallback');
  await w.reload();check((await w.e.groupThread(outsideConv)).role==='visitor','outside browser visitor role survives actual store reload');await w.close();
  w=await world();const ownRoster=await wire.parseRoster(v.challenge.rosters[0]),ownNext={...ownRoster,seq:1,prev:await wire.rosterHash(ownRoster),devices:[...ownRoster.devices,pub],by:await wire.fingerprint(alicePub),sig:null,join:await wire.joinConsent(keys,address,ownRoster.person,1,await wire.rosterHash(ownRoster))};
  const ownUnsigned=JSON.parse(wire.rosterJSON(ownNext));delete ownUnsigned.sig;delete ownUnsigned.join;ownNext.sig=new Uint8Array(await crypto.subtle.sign('Ed25519',aliceKeys.sign,new TextEncoder().encode('agentnet-person-v2\n'+JSON.stringify(ownUnsigned))));
  await wire.verifyNext(ownNext,ownRoster);w.extraChains.set(ownRoster.person,[ownRoster,ownNext].map(r=>JSON.parse(wire.rosterJSON(r))));w.e.me=await w.e.personRecord([ownRoster,ownNext],'self',null);await w.st.write([{s:'kv',k:'person',v:w.e.me}]);await w.e.pinDevices(w.e.me);
  const ownAdmissionErrors=[],ownAdmit=w.e.admitInner.bind(w.e);w.e.admitInner=async(...args)=>{try{return await ownAdmit(...args);}catch(e){ownAdmissionErrors.push({reason:e.reason,message:e.message});throw e;}};
  for(const sub of ['group-proof','group-context'])w.blobs.set(outside[sub+'-file'].blob,bytes(outside[sub+'-file'].ct));
  await w.receive(outside['own-group-proof']);await w.receive(outside['own-group-context']);
  await w.receive(outside['own-invite']);await w.receive(outside['own-accept']);
  check(!!await w.st.get('inbox',outside['own-invite'].inner.id),'native own invite admitted '+JSON.stringify({errors:ownAdmissionErrors,held:(await w.st.all('held')).map(({id,reason})=>({id,reason}))}));
  check((await w.e.agentConv(outside['own-pid'])).info.state==='active','real native own-linked PID events need no ordinary-person replica flag');
  await w.receive(outside['own-question']);const ownQuestion=await w.st.get('inbox',outside['own-question'].inner.id);
  check(!!ownQuestion&&ownQuestion.replica&&ownQuestion.own&&ownQuestion.state==='','real native non-target own request replica remains quiet display only');
  w.blobs.set(outside['own-ordinary-file'].blob,bytes(outside['own-ordinary-file'].ct));await w.receive(outside['own-ordinary']);
  const selectedSource=await w.st.get('inbox',outside['own-ordinary'].inner.id), selectedMembers=await w.e.dmMembers(await w.e.groupRecord(outsideConv));
  const selectedItem=await w.e.groupAgentSelection(outsideConv,selectedSource,selectedMembers);
  check(selectedSource.replica&&selectedItem.from_key===await wire.fingerprint(alicePub),'native ordinary own-linked replica selects exact original author');
  const selectedFile=await w.e.openFile(selectedSource.id,0,'in');check(new TextDecoder().decode(selectedFile.bytes)===outside['own-ordinary-file'].bytes,'native ordinary own-linked selected file exact bytes');
  const inviteSend=w.e.sendConv.bind(w.e);let selectedInvite;w.e.sendConv=async(c,n)=>{const ev=wire.parseEvent(n.body);if(ev.type==="invite"||ev.type==="share")selectedInvite=ev;const id=wire.newID();await w.st.write([{s:"outbox",k:id,v:{...n,id,conv:c.id,to:alicePub.address,at:1700000100000}}]);return {id};};
  await w.e.inviteAgent({conv:outsideConv,host:alicePub.address,share:[selectedSource.id]});
  check(selectedInvite.grant.length===1&&selectedInvite.grant[0].lid===selectedSource.lid&&selectedInvite.grant[0].fingerprint===selectedItem.from_key,'production invite retains native own-linked selected logical ID and original author');
  const selectedInfo={...(await w.e.agentConv(outside['own-pid'])).info,grant:selectedInvite.grant};
  check(w.e.agentView(selectedInfo,[selectedSource],null,[w.e.me],"member").shared[0]===selectedSource.id,'group shared context projection retains verified own-linked original');
  for(const change of [{fp:'f'.repeat(64)},{group_admission:'e'.repeat(64)},{pid:wire.newID()},{history:true},{excerpt_pid:wire.newID()}]) {let refused=false;try{await w.e.groupAgentSelection(outsideConv,{...selectedSource,...change},selectedMembers);}catch{refused=true;}check(refused,'group selected context refuses forged/PID/history/changed epoch '+Object.keys(change)[0]);}
  const retract=w.e.isRetracted.bind(w.e);w.e.isRetracted=async()=>true;let retractedRefused=false;try{await w.e.groupAgentSelection(outsideConv,selectedSource,selectedMembers);}catch{retractedRefused=true;}check(retractedRefused,'retracted original cannot become agent context');w.e.isRetracted=retract;w.e.sendConv=inviteSend;
  await w.receive(outside.ordinary);check((await w.st.get('held',outside.ordinary.inner.id))?.reason==='invalid','ordinary group own-copy replica rule remains strict');await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);w.e.connected=true;w.e.session=wire.newID();w.e.resetTyping(true,true);
  const allPeople=[w.e.me,...await w.st.all('persons')];w.e.members={...w.e.members,current:true,list:allPeople.flatMap(p=>p.devices.map(d=>({address:d.address,presence:'online',person:{id:p.person,hash:p.hash}})))};
  const hint=async(publicKey,signing)=>wire.sealTyping({v:1,id:wire.newID(),from:publicKey.address,to:address,ts:w.e.now(),session:w.e.session,realm:root.realm,conv,origin:'human',active:true},signing,pub);
  await w.e.onTyping(await hint(alicePub,aliceKeys));check((await w.e.typingView({conv})).entries.some(x=>x.address===alicePub.address),'signed current human group typing visible');
  const typingTargets=await w.e.typingTargets({conv});check(typingTargets.length===2&&!typingTargets.some(x=>x.pub.address==='dana/desk'),'group typing audience is current effective humans only, outside host excluded');
  await w.receive(c.withdrawn);check((await w.e.typingTargets({conv})).length===1,'ordinary signed withdrawal removes typing destination without inferred room grant');
  await w.reload();check(!(await w.e.typingView({conv})).entries.length,'typing remains ephemeral across real reload');await w.close();
  w=await world();const createRequests=[],createWrite=w.st.write.bind(w.st),createCall=w.e.call.bind(w.e);let createConflict=false;
  w.e.call=async(method,path,body)=>{if(method==='POST'&&path.startsWith('/v1/groups/')){createRequests.push(body);return {seq:body.seq,hash:body.hash};}return createCall(method,path,body);};
  w.st.write=async(ops,checks)=>{if(!createConflict&&ops.some(o=>o.k?.startsWith('group-publication/')&&o.v===undefined)){createConflict=true;const person=await w.st.get('kv','person');await createWrite([{s:'kv',k:'person',v:{...person,fixtureLabel:'create dependency changes after custody'}}]);}return createWrite(ops,checks);};
  const exactCreate=await w.e.createGroup('Direct create after conditional conflict');check(createConflict&&createRequests.length===2&&JSON.stringify(createRequests[0])===JSON.stringify(createRequests[1]),'direct create repeats exact recorded original ciphertext after conditional conflict');check((await w.e.groupCurrent(exactCreate)).state.seq===0&&(await w.st.all('outbox')).length===0,'direct create installs once without fabricated audience');await w.close();
  w=await world();
  const call=w.e.call.bind(w.e), posted=[];
  w.e.call=async(method,path,body,options)=>{if(method==='POST'&&path.startsWith('/v1/groups/')){posted.push(body);return {seq:body.seq,hash:body.hash};}return call(method,path,body,options);};
  const created=await w.e.createGroup('Explicit browser group');
  check(posted.length===1&&posted[0].seq===0,'explicit browser create uses one original journal publication');
  check((await w.e.groupCurrent(created)).state.title==='Explicit browser group','created context verified before install');
  check((await w.e.dm(created)).members[0].admin,'browser group DTO exposes current person administrator');
  check((await w.e.overview()).dms.some(d=>d.id===created&&d.kind==='group'),'browser created group appears as distinct conversation');
  let invalid=false;try{await w.e.createGroup('');}catch(_){invalid=true;}check(invalid&&posted.length===1,'invalid title refused before journal request');

  const createdPacket=await w.e.groupCurrent(created), aliceRoster=await wire.parseRoster(v.challenge.rosters[0]);
  const retryProposal={v:1,root:createdPacket.root,state:createdPacket.state,withdrawals:null,target:aliceRoster.person,roster:await wire.rosterHash(aliceRoster),seq:1,prev:await wire.groupStateHash(createdPacket.state),history:null}, retryID=await wire.groupInvitationID(retryProposal);
  const retryAdmission=await wire.signGroupAdmission(aliceKeys,{conv:created,realm:root.realm,person:retryProposal.target,roster:retryProposal.roster,seq:1,prev:retryProposal.prev,history:null,by:await wire.fingerprint(alicePub)});
  await w.st.write([{s:'kv',k:'group-invitation/out/'+retryID,v:{type:'group-invitation',id:retryID,direction:'out',status:'accepted',proposal:retryProposal,owner:roster.person,inviter:address,fp:w.e.fp,consent:{v:1,invitation:retryID,decision:'accepted',admission:retryAdmission}}}]);
  w.e.groupSupport=async()=>{};w.e.post=async()=>{};
  const publicationWrite=w.st.write.bind(w.st);let conflicted=false,conflictSeq=1;
  w.st.write=async(ops,checks)=>{if(!conflicted&&ops.some(o=>o.k==='group/'+created)&&ops.some(o=>o.k==='group-publication/'+created+'/'+conflictSeq&&o.v===undefined)){conflicted=true;const me=await w.st.get('kv','person');await publicationWrite([{s:'kv',k:'person',v:{...me,fixtureLabel:'same authority during async verification '+conflictSeq}}]);}return publicationWrite(ops,checks);};
  await w.e.recoverGroupIntents();
  check(conflicted&&(await w.st.get('kv','group-invitation/out/'+retryID)).status==='published','conditional conflict after custody recovers exact accepted invitation automatically');
  check(posted.length===3&&JSON.stringify(posted[1])===JSON.stringify(posted[2]),'publication recovery repeats same original encrypted commit bytes');
  const copies=(await w.st.all('outbox')).length;check(copies===2,'conflicting install leaves no duplicate proof/context outbox');await w.e.recoverGroupIntents();check(posted.length===3&&(await w.st.all('outbox')).length===copies,'completed invitation recovery does not repeat install or outbox');
  const publish=w.e.publishGroupInvitation.bind(w.e);for(const failure of ['stale','invalid','ambiguous network']){let calls=0;const id=wire.hex(await wire.sha256(new TextEncoder().encode(failure)));await w.st.write([{s:'kv',k:'group-invitation/out/'+id,v:{type:'group-invitation',id,direction:'out',status:'accepted'}}]);w.e.publishGroupInvitation=async()=>{calls++;throw Error(failure);};await w.e.recoverGroupIntents();check(calls===1&&(await w.st.get('kv','group-invitation/out/'+id)).status==='accepted','publication '+failure+' does not retry or fabricate completion');await w.st.write([{s:'kv',k:'group-invitation/out/'+id,v:undefined}]);}w.e.publishGroupInvitation=publish;
  conflicted=false;conflictSeq=2;await w.e.manageGroup({conv:created,action:'rename',title:'Direct rename after conditional conflict'});
  check(conflicted&&posted.length===5&&JSON.stringify(posted[3])===JSON.stringify(posted[4]),'direct rename repeats only exact pending encrypted custody after conditional conflict');
  check((await w.e.groupCurrent(created)).state.title==='Direct rename after conditional conflict','direct rename completes without repeated user action');

  const beforeHeadCopies=(await w.st.all('outbox')).length;let headConflict=false;
  w.st.write=async(ops,checks)=>{if(!headConflict&&ops.some(o=>o.k==='group-publication/'+created+'/3'&&o.v===undefined)){headConflict=true;await publicationWrite([{s:'kv',k:'group-head/'+created,v:{conv:created,bootstrap:createdPacket.root.creator.fingerprint,seq:4,hash:'e'.repeat(64)}}]);}return publicationWrite(ops,checks);};
  let staleHead=false;try{await w.e.manageGroup({conv:created,action:'rename',title:'Must not install behind changed head'});}catch(e){staleHead=e.reason==='proof_pending';}
  check(staleHead&&headConflict&&wire.parseGroupContext((await w.st.get('kv','group/'+created)).context).state.seq===2&&(await w.st.all('outbox')).length===beforeHeadCopies,'changed higher head refuses retried custody with no stale install or copies');
  check(posted.length===7&&JSON.stringify(posted[5])===JSON.stringify(posted[6]),'changed-head conflict reuses original ciphertext and stops on authority refusal');
  await w.reload();let frozenHead=false;try{await w.e.groupCurrent(created);}catch(_){frozenHead=true;}check(frozenHead&&wire.parseGroupContext((await w.st.get('kv','group/'+created)).context).state.seq===2,'changed-head refusal and last verified state survive actual store reload');
  await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  const np=wire.parseGroupInvitation(v.new_proposal), ni=await wire.groupInvitationID(np);
  await w.st.write([{s:'kv',k:'group-invitation/out/'+ni,v:{type:'group-invitation',id:ni,direction:'out',status:'pending',proposal:np,owner:root.creator.person,inviter:alicePub.address,fp:root.creator.fingerprint}}]);
  await w.receive(c['new-consent']);
  check((await w.st.get('kv','group-invitation/out/'+ni)).status==='accepted','new member native signed selected consent resolves exact independently pinned target roster');
  check(!await w.st.get('held',wire.parseEnvelope(c['new-consent'].envelope).id),'valid new-member consent not quarantined');
  await w.close();
  for(const mode of ['target','roster','signature']){
   w=await world();await w.receive(c.proof);await w.receive(c.context);
   const proposal=structuredClone(np);if(mode==='target')proposal.target=root.creator.person;if(mode==='roster')proposal.roster='e'.repeat(64);
   await w.st.write([{s:'kv',k:'group-invitation/out/'+ni,v:{type:'group-invitation',id:ni,direction:'out',status:'pending',proposal,owner:root.creator.person,inviter:alicePub.address,fp:root.creator.fingerprint}}]);
   const input=mode==='signature'?c['bad-consent-signature']:c['new-consent'];await w.receive(input);
   check((await w.st.get('kv','group-invitation/out/'+ni)).status==='pending'&&(await w.st.get('held',wire.parseEnvelope(input.envelope).id)).reason==='invalid','changed consent '+mode+' refused without invitation or membership mutation');await w.close();
  }
  w=await world();
  const fileBytes=new Uint8Array([0,1,255,7]), sentFile=await wire.encryptFile(fileBytes,'exact-selected.bin',alicePub), ownRow={id:wire.newID(),conv,lid:wire.newID(),kind:'message',body:'selected sent file',origin:'ui',from:address,at:1700000000000,attachments:[sentFile.attachment]};
  ownRow.to=alicePub.address;ownRow.envelope=await wire.seal({v:2,id:ownRow.id,lid:ownRow.lid,from:address,to:alicePub.address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),body:ownRow.body,origin:'ui',attachments:ownRow.attachments},keys,alicePub);
  await w.st.write([{s:'outbox',k:ownRow.id,v:ownRow}]);await w.e.keepSent([{name:'old-kept-name.bin',bytes:fileBytes}]);
  const fileMessage={lid:ownRow.lid,author:w.e.fp,hash:await wire.groupHistoryContentHash(conv,ownRow),index:0,name:sentFile.attachment.name,size:fileBytes.length,sha256:sentFile.attachment.sha256};
  w.e.groupTurnEvidence=async()=>({packet:{state:{conv}},members:[{devices:[{address,fingerprint:w.e.fp}]}]});w.e.groupFileAuthorized=async()=>{};
  let offered;w.e.offerGroupFile=async(job,files)=>{offered=files[0];};
  await w.e.serveGroupFile({conv,device:address,fp:w.e.fp,message:fileMessage});
  check(offered.attachment.name==='exact-selected.bin'&&wire.hex(await wire.decryptFile(offered.ct,offered.attachment,keys))===wire.hex(fileBytes),'sent group selected file uses exact self-kept bytes and requested filename');
  await w.st.write([{s:'inbox',k:ownRow.id,v:{...ownRow,to:undefined,conv:'e'.repeat(64)}}]);await w.e.serveGroupFile({conv,device:address,fp:w.e.fp,message:fileMessage});check(wire.hex(await wire.decryptFile(offered.ct,offered.attachment,keys))===wire.hex(fileBytes),'same-ID foreign inbox cannot retarget exact own outbox direction');let collision=false;try{await w.e.groupFileSource(conv,{...fileMessage,author:await wire.fingerprint(alicePub)});}catch(_){collision=true;}check(collision,'same-ID different direction author cannot retarget exact sent group file');
  await w.st.write([{s:'inbox',k:ownRow.id,v:undefined}]);const wrongKept=await wire.encryptFile(new Uint8Array([9,9,9,9]),'wrong.bin',pub);await w.st.write([{s:'files',k:'kept/'+fileMessage.sha256,v:{attachment:wrongKept.attachment,ct:wrongKept.ct}}]);let wrongBytes=false;try{await w.e.serveGroupFile({conv,device:address,fp:w.e.fp,message:fileMessage});}catch(_){wrongBytes=true;}check(wrongBytes,'kept group file must match exact requested size and digest');await w.close();
  w=await world();await w.receive(c.proof);
  const invite=await w.make(wire.SubGroupInvite,JSON.parse(wire.groupInvitationJSON(invitation)),invitation.state.seq,await wire.groupStateHash(invitation.state));
  await w.receive(invite);
  check((await w.e.groupInvitations()).length===1,'verified invitation retained separately');
  check(!(await w.e.groupRecord(conv)),'invitation does not install room access before consent');
  check((await w.st.all('inbox')).length===0&&(await w.st.all('outbox')).length===0,'invitation stays quiet until explicit choice');
  await w.reload();check((await w.e.groupInvitations())[0].status==='pending','invitation survives store reload');
  let unknown=false;try{await w.e.decideGroup({id:wire.newID(),accept:true});}catch(_){unknown=true;}check(unknown&&(await w.st.all('outbox')).length===0,'unknown invitation cannot generate consent');
  await w.close();
  w=await world();await w.receive(c.context);check((await w.st.get('held',wire.parseEnvelope(c.context.envelope).id)).reason==='proof_pending','context before proof held');await w.reload();await w.receive(c.proof);check(!!(await w.e.groupCurrent(conv)).state,'proof arrival retries context across reload');await w.receive(c.proof);check(!(await w.st.all('held')).length,'duplicate physical carrier quiet');const n=await wire.open(c.proof.envelope,keys,address,alicePub);const twin={...n,id:wire.newID()};w.blobs.delete(c.proof.blob);await w.receive({envelope:await wire.seal(twin,aliceKeys,pub)});check(!await w.st.get('held',twin.id)&&!!await w.st.get('kv','group-carrier/'+twin.id),'logical carrier dedup before ciphertext fetch');const conflict={...n,id:wire.newID(),body:wire.groupCarrierJSON({v:1,seq:0,hash:'e'.repeat(64),to_key:w.e.fp})};await w.receive({envelope:await wire.seal(conflict,aliceKeys,pub)});check((await w.st.get('held',conflict.id)).reason==='conflicting_duplicate','logical carrier conflicting content held');check((await w.st.get('kv','group/'+conv)).records[0]===wire.groupCommitJSON(records[0]),'original opaque proof byte equal');await quiet(w);await w.close();
  w=await world();await w.receive(c.proofx);check((await w.st.get('held',wire.parseEnvelope(c.proofx.envelope).id)).reason==='proof_pending','missing public prefix held');await w.receive(c.proof);check((await w.st.get('kv','group/'+conv)).records.length===2,'out of order proof resumes');await w.receive(c.contextx);check((await w.e.groupCurrent(conv)).state.seq===1,'cold current admission from exact public slots');await w.receive(c.context);check((await w.st.get('kv','group/'+conv)).context===wire.groupContextJSON({root,proof:null,state:states[1],withdrawals:null}),'old context cannot rewind');await quiet(w);await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(c.withdrawn);check((await w.e.groupCurrent(conv)).withdrawals.length===1,'ordinary native signed withdrawal retained');await w.reload();check((await w.e.groupCurrent(conv)).withdrawals.length===1,'withdrawal durable reload');await quiet(w);await w.close();
  w=await world();for(let i=0;i<=3;i++)await w.receive(c['proof'+'x'.repeat(i)]);await w.receive(c['promoted-leave']);check((await w.st.get('kv','group/'+conv)).pending.length===1,'signature-only promoted leave retained pending without context');check(!(await w.st.get('kv','group/'+conv)).context,'promoted withdrawal never installs');await w.receive(c.contextxxx);check(!(await w.st.get('kv','group/'+conv)).context,'pending admission blocks promoted admin');await w.receive(c.proofxxxx);await w.receive(c.contextxxxx);check((await w.e.groupCurrent(conv)).withdrawals.length===1,'later ordinary admission resolves pending withdrawal');await w.close();
  w=await world();for(let i=0;i<=5;i++)await w.receive(c['proof'+'x'.repeat(i)]);await w.receive(c.contextxxxxx);check(!(await w.st.get('kv','group/'+conv)).context,'removed recipient is refused despite founding root');await w.close();
  for(const mode of ['short','offline','empty','stream-error','corrupt','oversize']){w=await world();w.mode(mode);let thrown=false;try{await w.receive(c.proof);}catch(_){thrown=true;}const id=wire.parseEnvelope(c.proof.envelope).id;if(mode==='corrupt'||mode==='oversize'){check(!thrown&&(await w.st.get('held',id)).reason==='invalid','permanent signed digest corruption quarantined');}else{check(thrown&&!await w.st.get('receipts',id)&&!await w.st.get('held',id)&&!await w.st.get('kv','group/'+conv),'interrupted transfer no acknowledgment '+mode);w.mode('');await w.receive(c.proof);check(!!await w.st.get('kv','group/'+conv),'interrupted transfer retries '+mode);}await w.close();}
  w=await world();await w.receive(c.proof);const wrong=await w.make(wire.SubGroupContext,{root:JSON.parse(wire.rootJSON(root)),state:JSON.parse(wire.groupStateJSON(states[0])),withdrawals:null},0,records[0].hash,{body:wire.groupCarrierJSON({v:1,seq:0,hash:records[0].hash,to_key:'ffffffff-ffffffff-ffffffff-ffffffff'})});await w.receive(wrong);check((await w.st.get('held',wire.parseEnvelope(wrong.envelope).id)).reason==='invalid','wrong exact recipient key');await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);await w.e.dispatch('groups',JSON.stringify([{conv,bootstrap:root.creator.fingerprint,seq:1,hash:records[1].hash}]));await w.e.dispatch('groups','[]');let pending=false;try{await w.e.groupCurrent(conv);}catch(_){pending=true;}check(pending&&(await w.st.get('kv','group-head/'+conv)).seq===1,'head batches merge and empty batch does not clear');await w.reload();pending=false;try{await w.e.groupCurrent(conv);}catch(_){pending=true;}check(pending,'latest known head durable');await w.e.noteGroupHead({conv,bootstrap:root.creator.fingerprint,seq:0,hash:records[0].hash});check((await w.st.get('kv','group-head/'+conv)).seq===1,'lower head cannot rewind');await w.close();
  // Real IDB race: second connection changes latest head after signature
  // verification but before conditional install. Retry must hold, never install.
  w=await world();await w.receive(c.proof);const original=w.st.write.bind(w.st);let raced=false;
  w.st.write=async(ops,checks)=>{if(!raced&&checks&&ops.some(o=>o.k==='group-carrier/'+wire.parseEnvelope(c.context.envelope).id)){raced=true;const other=realIDB?await openIDB(w.name):w.st;await (other===w.st?original:other.write.bind(other))([{s:'kv',k:'group-head/'+conv,v:{conv,bootstrap:root.creator.fingerprint,seq:1,hash:records[1].hash}}]);if(other!==w.st)other.close();}return original(ops,checks);};
  await w.receive(c.context);check(raced&&!(await w.st.get('kv','group/'+conv)).context,'compare conflict aborts install and retries admission');await w.reload();check(!(await w.st.get('kv','group/'+conv)).context,'race abort durable reload');await w.close();
  w=await world();await w.st.write([{s:'kv',k:'atomic',v:1}]);let refused=false;try{await w.st.write([{s:'kv',k:'leak',v:1},{s:'kv',k:'atomic',v:2}],[{s:'kv',k:'atomic',v:0}]);}catch(_){refused=true;}check(refused&&!await w.st.get('kv','leak')&&(await w.st.get('kv','atomic'))===1,'conditional write all or none');await w.reload();check((await w.st.get('kv','atomic'))===1,'conditional abort reload');await w.close();
  w=await world();await w.receive(c.proof);
  for(const kind of ['descriptor','realm','root-signature','proof-fork','proof-bound','withdrawal-signature']){
   let x;
   if(kind==='proof-bound'){x=await w.make(wire.SubGroupProof,JSON.parse(wire.groupJournalJSON({records:Array(17).fill(records[0]),more:false})),0,records[0].hash);}
   else if(kind==='proof-fork') {const fork=await wire.signGroupCommit(aliceKeys,{...records[0],hash:'f'.repeat(64)});x=await w.make(wire.SubGroupProof,JSON.parse(wire.groupJournalJSON({records:[fork],more:false})),0,fork.hash);}
   else {const payload={root:JSON.parse(wire.rootJSON(root)),state:v.states[0],withdrawals:null};let fields={};
    if(kind==='descriptor')fields.body=wire.groupCarrierJSON({v:1,seq:0,hash:'f'.repeat(64),to_key:w.e.fp});
    if(kind==='realm'){const foreign=await wire.signGroupRoot(aliceKeys,{...root,realm:wire.newID()});fields={root:wire.rootJSON(foreign),conv:await wire.rootID(foreign)};}
    if(kind==='root-signature'){const altered={...root,sig:root.sig.slice()};altered.sig[0]^=1;fields.root=wire.rootJSON(altered);}
    if(kind==='withdrawal-signature'){const bad=wire.parseGroupWithdrawal(v.withdrawal);bad.sig[0]^=1;payload.withdrawals=[JSON.parse(wire.groupWithdrawalJSON(bad))];}
    x=await w.make(wire.SubGroupContext,payload,0,records[0].hash,fields);
   }
   await w.receive(x);check((await w.st.get('held',wire.parseEnvelope(x.envelope).id)).reason==='invalid','signed rejection '+kind);
  }
  await w.close();
  w=await world();await w.receive(c.proof);
  const fresh=await wire.newKeys(),nextPub=await wire.publicEntry(fresh,'browser/other');
  const join=await wire.joinConsent(fresh,nextPub.address,roster.person,1,await wire.rosterHash(roster));
  const next=await wire.nextRoster(keys,address,roster,[nextPub],join);await wire.verifyNext(next,roster);
  w.extraChains.set(roster.person,[JSON.parse(wire.rosterJSON(roster)),JSON.parse(wire.rosterJSON(next))]);
  w.e.me=await w.e.personRecord([roster,next],'self',w.e.me);await w.st.write([{s:'kv',k:'person',v:w.e.me}]);
  await w.receive(c.context);check(!(await w.st.get('kv','group/'+conv)).context&&(await w.st.get('held',wire.parseEnvelope(c.context.envelope).id)).reason==='invalid','removed exact own key cannot install');
  const staleLeave=await w.make(wire.SubGroupContext,{root:JSON.parse(wire.rootJSON(root)),state:v.states[0],withdrawals:[v.withdrawal]},0,records[0].hash);
  // Bob's changed pinned head makes his old signer/roster inadmissible.
  const bob=await w.st.get('persons',v.withdrawal.person);await w.st.write([{s:'persons',k:bob.person,v:{...bob,hash:'e'.repeat(64),state:'conflict'}}]);
  await w.receive(staleLeave);check(!(await w.st.get('kv','group/'+conv)).context,'frozen withdrawal signer cannot install');await w.close();
  w=await world();await w.receive(c.proof);const write=w.st.write.bind(w.st);let keyRace=false;
  w.st.write=async(ops,checks)=>{if(!keyRace&&checks&&ops.some(o=>o.k==='group-carrier/'+wire.parseEnvelope(c.context.envelope).id)){keyRace=true;const other=realIDB?await openIDB(w.name):w.st;const pin=await other.get('pins',alicePub.address);await(other===w.st?write:other.write.bind(other))([{s:'pins',k:alicePub.address,v:{...pin,pending:{fingerprint:'ffffffff-ffffffff-ffffffff-ffffffff'}}}]);if(other!==w.st)other.close();}return write(ops,checks);};
  await w.receive(c.context);check(keyRace&&!(await w.st.get('kv','group/'+conv)).context&&(await w.st.get('held',wire.parseEnvelope(c.context.envelope).id)).reason==='key_changed','pin race aborts verified install');await w.reload();check(!(await w.st.get('kv','group/'+conv)).context,'pin race remains refused after reload');await w.close();
  for(const race of ['own-roster','withdrawal-overlay']){
   w=await world();await w.receive(c.proof);const original=w.st.write.bind(w.st);let fired=false;
   let nextSelf,leave,changedChain;
   if(race==='own-roster'){
    const fresh=await wire.newKeys(),np=await wire.publicEntry(fresh,'browser/linked');const join=await wire.joinConsent(fresh,np.address,roster.person,1,await wire.rosterHash(roster));const next=await wire.nextRoster(keys,address,roster,[np],join);await wire.verifyNext(next,roster);
    nextSelf=await w.e.personRecord([roster,next],'self',w.e.me);changedChain=[JSON.parse(wire.rosterJSON(roster)),JSON.parse(wire.rosterJSON(next))];
   }else{
    const m=wire.groupMember(states[0],roster.person);leave=await wire.signGroupWithdrawal(keys,{conv,realm:root.realm,person:roster.person,admission:await wire.groupAdmissionHash(m.admission),roster:await wire.rosterHash(roster),by:w.e.fp});
    await wire.verifyGroupWithdrawal(leave,states[0],(p,h)=>p===roster.person?roster:null);
   }
   w.st.write=async(ops,checks)=>{if(!fired&&checks&&ops.some(o=>o.k==='group-carrier/'+wire.parseEnvelope(c.context.envelope).id)){fired=true;const other=realIDB?await openIDB(w.name):w.st;let change;
    if(nextSelf){w.extraChains.set(roster.person,changedChain);change={s:'kv',k:'person',v:nextSelf};}else{const g=await other.get('kv','group/'+conv);change={s:'kv',k:'group/'+conv,v:{...g,pending:[wire.groupWithdrawalJSON(leave)]}};}
    await(other===w.st?original:other.write.bind(other))([change]);if(other!==w.st)other.close();}return original(ops,checks);};
   await w.receive(c.context);check(fired&&!(await w.st.get('kv','group/'+conv)).context,'atomic install refuses concurrent '+race);await w.reload();check(!(await w.st.get('kv','group/'+conv)).context,'atomic refusal survives reload '+race);await w.close();
  }
  w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(c.proofx);await w.receive(c.proofxx);await w.receive(c.contextxx);
  check(!await w.st.get('held',wire.parseEnvelope(c.contextxx.envelope).id),'warm original replay resolves context2');
  check((await w.e.groupCurrent(conv)).state.seq===2,'warm reader recovers via original encrypted transitions');
  await w.reload();check((await w.e.groupCurrent(conv)).state.seq===2,'warm recovered state survives real reload');await quiet(w);await w.close();
  w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(c.withdrawn);await w.receive(c.proofx);await w.receive(c.proofxx);await w.receive(c.contextxx);
  check((await w.e.groupCurrent(conv)).state.seq===2&&(await w.e.groupCurrent(conv)).withdrawals.length===1,'warm replay retains pinned ordinary withdrawal');await w.close();
  for(const mode of ['fresh','same-old','older-different','known-removed','invalid-decrypted','malformed-decrypted','cipher-corrupt','join-slot-mismatch']){
   w=await world();
   const self0=wire.groupMember(states[0],roster.person);
   const state=async(prev,seq,present,admission=self0.admission)=>{
    const members=prev.members.filter(m=>m.person!==roster.person).map(m=>({...m}));if(present)members.push({...self0,admission});members.sort((a,b)=>a.person.localeCompare(b.person));
    return wire.signGroupState(aliceKeys,{...prev,seq,prev:await wire.groupStateHash(prev),title:'Recovery '+seq,members});
   };
   const commit=async(snapshot,include=true,mode='')=>{
    let plain=wire.groupContextJSON({root,proof:null,state:snapshot,withdrawals:null});if(mode==='malformed-decrypted')plain=plain.slice(0,-1)+',"unknown":1}';
    const f=await wire.encryptFile(new TextEncoder().encode(plain),'original.json',include?pub:alicePub);if(mode==='cipher-corrupt')f.ct[f.ct.length-1]^=1;
    return wire.signGroupCommit(aliceKeys,{...records[0],seq:snapshot.seq,prev:snapshot.prev,hash:await wire.groupStateHash(snapshot),admins:wire.groupAdmins(snapshot),ciphertext:f.ct});
   };
   const p1=await state(states[0],1,false),a2=await wire.signGroupAdmission(keys,{...self0.admission,seq:2,prev:await wire.groupStateHash(p1)}),p2=await state(p1,2,true,a2);
   let ps=[states[0],p1,p2],rs=[records[0],await commit(p1,false),await commit(p2)],start=0,target=p2;
   if(mode==='same-old'){target=await state(p1,2,true);ps[2]=target;rs[2]=await commit(target);}
   if(mode==='fresh'||mode==='join-slot-mismatch'){
    let a=a2;if(mode==='join-slot-mismatch')a=await wire.signGroupAdmission(keys,{...a2,history:[{lid:wire.newID(),author:await wire.fingerprint(pub),hash:'e'.repeat(64)}]});
    target=await state(p2,3,true,a);ps.push(target);rs.push(await commit(target));
   }
   if(mode==='older-different'||mode==='known-removed'){
    start=2;const p3=await state(p2,3,false),p4=await state(p3,4,false);target=await state(p4,5,true,mode==='known-removed'?a2:self0.admission);
    ps.push(p3,p4,target);rs.push(await commit(p3,mode==='known-removed'),await commit(p4,false),await commit(target));
   }
   if(['invalid-decrypted','malformed-decrypted','cipher-corrupt'].includes(mode)){
    const a1=await wire.signGroupAdmission(keys,{...self0.admission,seq:1,prev:records[0].hash});const bad=await state(states[0],1,true,a1),removed=await state(bad,2,false),a3=await wire.signGroupAdmission(keys,{...self0.admission,seq:3,prev:await wire.groupStateHash(removed)});
    target=await state(removed,3,true,a3);ps=[states[0],bad,removed,target];rs=[records[0],await commit(bad,true,mode),await commit(removed,false),await commit(target)];
   }
   const proof=async(from,to)=>w.make(wire.SubGroupProof,JSON.parse(wire.groupJournalJSON({records:rs.slice(from,to),more:false})),rs[to-1].seq,rs[to-1].hash);
   await w.receive(await proof(0,start+1));const initial=ps[start];await w.receive(await w.make(wire.SubGroupContext,JSON.parse(wire.groupContextJSON({root,state:initial,proof:null,withdrawals:null})),start,rs[start].hash));
   check((await w.e.groupCurrent(conv)).state.seq===start,'recovery initial verified '+mode);
   await w.receive(await proof(start+1,rs.length));let stale=false;try{await w.e.groupCurrent(conv);}catch(_){stale=true;}check(stale,'new proof blocks stale usable state '+mode);
   const current=await w.make(wire.SubGroupContext,JSON.parse(wire.groupContextJSON({root,state:target,proof:null,withdrawals:null})),target.seq,await wire.groupStateHash(target));await w.receive(current);
   if(mode==='fresh')check((await w.e.groupCurrent(conv)).state.seq===3,'exact fresh self slot then sequential suffix recovers');
   else {check(!!await w.st.get('held',wire.parseEnvelope(current.envelope).id),'stale or invalid recovery refused '+mode);let denied=false;try{await w.e.groupCurrent(conv);}catch(_){denied=true;}check(denied,'partial replay never grants stale state '+mode);}
   await w.reload();if(mode==='fresh')check((await w.e.groupCurrent(conv)).state.seq===3,'fresh self recovery durable real reload');else check(wire.parseGroupContext((await w.st.get('kv','group/'+conv)).context).state.seq===start,'invalid recovery never partially installs '+mode);
   await quiet(w);await w.close();
  }
  // Deleting a group conversation (client convclear.go): this device's
  // copy of its turns is erased by name, membership and admission epoch stay
  // exactly as they were, and a later group turn reopens it with only itself.
  {
   w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(pv.ordinary);await w.e.retryHeld();
   check((await w.e.groupThread(conv)).messages.some(m=>m.id===pv.ordinary.inner.id),'group turn shown before deletion');
   const groupBefore=JSON.stringify(await w.st.get('kv','group/'+conv)),epochBefore=(await w.e.dmMembers(await w.e.groupRecord(conv))).epochs.get(w.e.fp);
   const done=await w.e.deleteConversation({conv});
   check(/No other device of yours was linked/.test(done.note),'group deletion with no other own device says so');
   check(!(await w.e.groupThread(conv)).messages.some(m=>m.id===pv.ordinary.inner.id)&&!(await w.e.overview()).dms.some(d=>d.id===conv),'group turn hidden and group left out of the list');
   check((await w.st.all('inbox')).filter(r=>r.conv===conv&&!r.control&&!r.sub).every(r=>r.body===''),'group turn text erased here');
   check(JSON.stringify(await w.st.get('kv','group/'+conv))===groupBefore&&(await w.e.dmMembers(await w.e.groupRecord(conv))).epochs.get(w.e.fp)===epochBefore,'group membership and own admission epoch unchanged');
   for(const k of ['member-invite','member-accept','member-question'])await w.receive(pv[k]);
   const later=await w.e.groupThread(conv);
   check(later.messages.some(m=>m.id===pv['member-question'].inner.id)&&!later.messages.some(m=>m.id===pv.ordinary.inner.id)&&(await w.e.overview()).dms.some(d=>d.id===conv),'a later group turn reopens it with only itself');
   await w.reload();check(!(await w.e.groupThread(conv)).messages.some(m=>m.id===pv.ordinary.inner.id),'group deletion survives durable reload');
   await w.close();
  }
  check(!(await wire.newCaps(keys,address,wire.newID())).caps.includes(wire.CapGroup),'grp1 stays off');
  return {ok:true,storage:realIDB?'real IndexedDB':'memory unit only',checks:labels.length,labels};
 }catch(e){if(w)await w.close().catch(()=>{});throw e;}
}
if(globalThis.process?.versions?.node){const {createInterface}=await import('node:readline');for await(const line of createInterface({input:process.stdin})){let out;try{const r=JSON.parse(line);out=r.op==='setup'?await setup():r.op==='consent'?{consent:await consent(r.challenge)}:await checks(r.vectors,false,r.op==='warm-regression');}catch(e){out={error:e.stack};}process.stdout.write(JSON.stringify(out)+'\n');}}
