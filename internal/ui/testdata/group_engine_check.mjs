// Native-produced sealed carrier vectors through actual Engine. Synthetic
// signed-request fetch only; Chrome mode uses real IndexedDB, Node memory is unit-only.
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, openIDB } from '../static/engine.mjs';
import { checkControlHistory } from './group_control_history_check.mjs';
import {receiverProfile} from './group_receiver_profile.mjs';
import {carrierReceiptRepair} from './carrier_receipt_repair_check.mjs';
import {participationTopics} from './participation_topic_check.mjs';
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
export async function checks(v, realIDB=false, requireWarmRecovery=false, controlHistoryOnly=false, backgroundOnly=false,receiverOnly=false,receiptRepairOnly=false,topicOnly=false) {
 if(receiptRepairOnly)return carrierReceiptRepair(realIDB);
 const labels=[], check=(ok,label)=>{assert(ok,label);labels.push(label);};
 const root=wire.parseGroupRoot(v.challenge.root), conv=await wire.rootID(root), states=v.states.map(wire.parseGroupState), records=v.commits.map(wire.parseGroupCommit), c=v.carriers;
 const invitation=await wire.validateGroupInvitation(wire.parseGroupInvitation(v.invitation_json));
 check(wire.groupInvitationJSON(invitation)===v.invitation_json,'native group invitation JSON exact bytes');
 check(await wire.groupInvitationID(invitation)===v.invitation_id,'native group invitation ID exact hash');
 const nativeNonce=await wire.validateGroupInvitation(wire.parseGroupInvitation(v.nonce_invitation_json));
 check(wire.groupInvitationJSON(nativeNonce)===v.nonce_invitation_json&&await wire.groupInvitationID(nativeNonce)===v.nonce_invitation_id,'native nonce invitation canonical bytes and ID exactly match browser');
 check(wire.groupConsentJSON(wire.parseGroupConsent(v.cancel_consent_json))===v.cancel_consent_json,'native cancellation consent canonical bytes exactly match browser');
 check(wire.groupConsentJSON(wire.parseGroupConsent(v.decline_json))===v.decline_json,'native explicit group decline JSON exact bytes');
 check(wire.groupConsentJSON(wire.parseGroupConsent(v.accept_json))===v.accept_json,'native signed accepted consent selected history exact bytes');
 const liveHistory=wire.parseHistory(v.live_history_json);
 check(wire.historyJSON(liveHistory)===v.live_history_json,'native linked-live history admission stamp exact bytes');
 check(await wire.groupHistoryContentHash('1'.repeat(32),liveHistory)===v.history_hash,'native exact ordered selected file content hash');
 const alicePub=await wire.parsePublic(v.challenge.rosters[0].devices[0]), bobPub=await wire.parsePublic(v.challenge.rosters[1].devices[0]);
 const aliceKeys={sign:await crypto.subtle.importKey('pkcs8',bytes(v.challenge.alice_private),{name:'Ed25519'},false,['sign'])};
 const names=[];
 const world=async()=>{
  const name='agentnet-group-1002h-'+wire.newID();names.push(name);
  let st=realIDB?await openIDB(name):memoryStore();
  const chains=new Map([...v.challenge.rosters,v.invited_roster].map(r=>[r.person,[r]])), blobs=new Map(), profiles=new Map(), receipts=[];
  let mode='', extraChains=new Map();
  for(const x of Object.values(c))blobs.set(x.blob,bytes(x.ct));
  for(const x of v.participations.files)blobs.set(x.blob,bytes(x.ct));
  const fetch=async(url,o)=>{
   const u=new URL(url);
   if(u.pathname.startsWith('/v1/blobs/')){const id=u.pathname.split('/')[3],b=blobs.get(id);if(!b)return new Response('',{status:404});if(mode==='offline')throw Error('fixture offline');if(mode==='short')return new Response(b.slice(0,-2));if(mode==='empty')return new Response(null);if(mode==='oversize'){const x=new Uint8Array(b.length+1);x.set(b);return new Response(x);}if(mode==='stream-error')return new Response(new ReadableStream({start(controller){controller.enqueue(b.slice(0,16));controller.error(Error('fixture interrupted'));}}));if(mode==='corrupt'){const x=b.slice();x[x.length-1]^=1;return new Response(x);}return new Response(b);}
   if(u.pathname.startsWith('/v1/persons/')){const person=u.pathname.split('/')[3],chain=extraChains.get(person)||chains.get(person)||[];const after=Number(u.searchParams.get('after'));return new Response(JSON.stringify({records:chain.filter(r=>r.seq>after),more:false}));}
   if(u.pathname==='/v1/receipts'){receipts.push(JSON.parse(o.body));return new Response('{}');}
   if(receiverOnly&&u.pathname==='/v1/version')return new Response(JSON.stringify({version:'receiver-fixture',realm_id:root.realm,features:['env2','caps','person2']}));
   if(receiverOnly&&/^\/v1\/agents\/[^/]+\/[^/]+\/profile$/.test(u.pathname)){const who=u.pathname.split('/').slice(3,5).join('/');if(!profiles.has(who))throw Error('Missing signed profile '+who);return new Response(JSON.stringify(profiles.get(who)));}
   if(receiverOnly&&/^\/v1\/messages\/[0-9a-f]{32}\/ack$/.test(u.pathname)){receipts.push({id:u.pathname.split('/')[3],...JSON.parse(o.body)});return new Response('{}');}
   throw Error('unexpected fixture fetch '+u.pathname);
  };
  const recovery=new Set(),track=engine=>{const run=engine.recoverGroupIntents.bind(engine);engine.recoverGroupIntents=(...args)=>{const work=run(...args);recovery.add(work);work.then(()=>recovery.delete(work),()=>recovery.delete(work));return work;};};
  const drain=async()=>{if(e.retrying)await e.retrying;if(e.disclosing)await e.disclosing;while(recovery.size)await Promise.allSettled([...recovery]);if(e.historyRun)await e.historyRun;if(e.erasing)await e.erasing;};
  let e=new Engine({store:st,base:'http://127.0.0.1:1',fetch});track(e);e.keys=keys;e.address=address;e.fp=await wire.fingerprint(pub);e.realm=root.realm;
  for(const raw of [...v.challenge.rosters,v.invited_roster]){const r=await wire.parseRoster(raw);await wire.verifyFirst(r);const p=await e.personRecord([r],r.person===roster.person?'self':'pinned',null);if(r.person===roster.person){e.me=p;await st.write([{s:'kv',k:'person',v:p}]);}else await st.write([{s:'persons',k:p.person,v:p}]);await e.pinDevices(p);}
  await st.write([{s:'kv',k:'identity',v:{keys,address,fingerprint:e.fp}}]);
  const receive=async(x)=>{await e.onMessage(x.envelope);await e.retryHeld();};
  const make=async(sub,payload,seq,hash,fields={})=>{
   const file=await wire.encryptFile(new TextEncoder().encode(JSON.stringify(payload)),sub+'.json',pub);blobs.set(file.attachment.blob.id,file.ct);
   const inner={v:2,id:wire.newID(),from:alicePub.address,to:address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),lid:wire.newID(),sub,body:wire.groupCarrierJSON({v:1,seq,hash,to_key:e.fp}),attachments:[file.attachment],...fields};
   return {envelope:await wire.seal(inner,aliceKeys,pub),blob:file.attachment.blob.id};
  };
  return {get e(){return e;},st,name,receive,make,blobs,profiles,receipts,mode:x=>mode=x,extraChains,
   async reload(){await drain();st.close();st=realIDB?await openIDB(name):st;e=new Engine({store:st,base:'http://127.0.0.1:1',fetch});track(e);await e.load();e.realm=root.realm;this.st=st;},
   async close(){await drain();st.close();if(realIDB)await new Promise((res,rej)=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=res;r.onerror=()=>rej(r.error);/* close is pending until active read transactions finish; onsuccess proves deletion. */});}};
 };
 const quiet=async(w)=>{for(const s of ['inbox','outbox','convs','lids'])check((await w.st.all(s)).length===0,'quiet '+s);check((await w.e.overview()).threads.length===0,'no visible threads');for(const x of await w.st.all('held'))check(!('body'in x)&&!('plaintext'in x),'held has ciphertext only');for(const x of await w.st.all('files'))check(x.ct instanceof Uint8Array&&!('body'in x),'files ciphertext only');};
 if(receiverOnly){const profile=await receiverProfile({world,check,realIDB,c,root,conv,keys,address,roster,pub});return {ok:true,storage:profile.storage,checks:labels.length,labels,profile};}
 if(topicOnly){await participationTopics({world,check,c,root,conv,keys,address,pub,alicePub,aliceKeys,v});return {ok:true,storage:realIDB?"real IndexedDB":"memory unit only",checks:labels.length,labels};}
 let w;
 try{
  // Background disclosure coalesces a burst, including a failed active pass.
  w=await world();
  {
   const original=w.e.discloseHumanPass;let passes=0,start,release;
   const started=new Promise(r=>{start=r;}),gate=new Promise(r=>{release=r;});
   w.e.discloseHumanPass=async()=>{if(++passes===1){start();await gate;throw Error('synthetic first-pass failure');}};
   try {
    const first=w.e.discloseHumanAudience();await started;
    const wakes=Array.from({length:64},()=>w.e.discloseHumanAudience());
    release();await Promise.all([first,...wakes]);
    check(passes===2,'disclosure burst drains one pending pass even after a failed active pass');
    await w.e.discloseHumanAudience();
    check(passes===3&&!w.e.disclosing,'later disclosure wake runs and returns to idle');
   } finally {release();if(w.e.disclosing)await w.e.disclosing;w.e.discloseHumanPass=original;}
  }
  await w.close();
  w=await world();
  {
   const context=wire.groupContextJSON({root,state:states[0]}),accepted='1'.repeat(32),pending='2'.repeat(32),published='3'.repeat(32);
   await w.st.write([{s:'kv',k:'group/'+conv,v:{root:wire.rootJSON(root),context,records:[wire.groupCommitJSON(records[0])],withdrawals:[],pending:[],rosters:{}}},
    {s:'kv',k:'group-publication/'+conv+'/0',v:{type:'group-publication',conv,seq:0,context}},
    ...[[accepted,'accepted'],[pending,'pending'],[published,'published']].map(([id,status])=>({s:'kv',k:'group-invitation/out/'+id,v:{type:'group-invitation',direction:'out',id,status}})),
    ...Array.from({length:3600},(_,i)=>({s:'kv',k:'group-carrier/perf-'+String(i).padStart(4,'0'),v:true}))]);
   const all=w.st.all.bind(w.st),publishPacket=w.e.publishGroupPacket,publishInvite=w.e.publishGroupInvitation;
   let kvReads=0;const publishedCalls=[];
   w.st.all=async s=>{if(s==='kv')kvReads++;return all(s);};
   w.e.publishGroupPacket=async p=>{publishedCalls.push('packet:'+p.state.conv);};
   w.e.publishGroupInvitation=async id=>{publishedCalls.push('invite:'+id);};
   try {
    await w.e.recoverGroupIntents();if(w.e.invitationSyncRun)await w.e.invitationSyncRun;
    await w.e.discloseHumanPass();
    check(kvReads===0,'history background recovery never scans unrelated kv carrier ledger');
    check(JSON.stringify(publishedCalls)===JSON.stringify(['invite:'+accepted,'packet:'+conv]),'prefix recovery retains exact accepted invitation and publication, skipping pending and published');
    check(await w.st.get('kv','group-carrier/perf-3599')===true,'background enumeration preserves unrelated durable carrier evidence');
   } finally {w.st.all=all;w.e.publishGroupPacket=publishPacket;w.e.publishGroupInvitation=publishInvite;}
  }
  await w.close();
  if(backgroundOnly)return {ok:true,storage:realIDB?"real IndexedDB":"memory unit only",checks:labels.length,labels};
  await checkControlHistory({world,check,realIDB,c,root,conv,keys,address,roster,pub,alicePub});
  if(controlHistoryOnly)return {ok:true,storage:realIDB?'real IndexedDB':'memory unit only',checks:labels.length,labels};
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
  // Storage order must remain distinct from history display order. A v3
  // database upgrades in place; later read-state writes create no new arrival.
  {
   const name='agentnet-history-upgrade-'+wire.newID(),a={id:wire.newID(),lid:wire.newID(),conv,at:200,kind:'message',body:'newer display'},b={id:wire.newID(),lid:wire.newID(),conv,at:100,kind:'message',body:'older delayed display'};
   let st;
   if(realIDB) {
    const old=await new Promise((resolve,reject)=>{const request=indexedDB.open(name,3);request.onupgradeneeded=()=>{for(const s of ['kv','pins','persons','convs','inbox','outbox','held','lids','receipts','files','erased'])request.result.createObjectStore(s);};request.onsuccess=()=>resolve(request.result);request.onerror=()=>reject(request.error);});
    await new Promise((resolve,reject)=>{const tx=old.transaction('inbox','readwrite');tx.objectStore('inbox').put(a,a.id);tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);});old.close();st=await openIDB(name);
   } else {st=memoryStore();await st.write([{s:'inbox',k:a.id,v:a}]);}
   await st.write([{s:'inbox',k:b.id,v:b}]);
   const ordered=await st.historyRows('inbox',{conv,reverse:true}),arrivals=await st.historyRows('inbox',{arrival:true});
   check(ordered.map(r=>r.id).join()===a.id+','+b.id&&arrivals.map(r=>r.id).join()===a.id+','+b.id,'upgraded bounded indexes separate display order from late acceptance order');
   const stored=await st.get('inbox',a.id),before=await st.get('kv','history-arrival');await st.write([{s:'inbox',k:a.id,v:{...stored,read:true}}]);
   check(await st.get('kv','history-arrival')===before&&(await st.get('inbox',a.id)).history_arrival===stored.history_arrival,'read-state update preserves original durable arrival');
   const rejected={...a,id:wire.newID(),lid:wire.newID()};let conflict=false;
   try{await st.write([{s:'inbox',k:rejected.id,v:rejected}],[{s:'inbox',k:a.id,v:stored}]);}catch{conflict=true;}
   check(conflict&&!await st.get('inbox',rejected.id)&&await st.get('kv','history-arrival')===before,'aborted storage transaction advances neither arrival watermark nor accepted row');
   const other=realIDB?await openIDB(name):st,x={...a,id:wire.newID(),lid:wire.newID()},y={...a,id:wire.newID(),lid:wire.newID()};
   await Promise.all([st.write([{s:'inbox',k:x.id,v:x}]),other.write([{s:'inbox',k:y.id,v:y}])]);
   check(new Set([(await st.get('inbox',x.id)).history_arrival,(await st.get('inbox',y.id)).history_arrival]).size===2&&await st.get('kv','history-arrival')===before+2,'competing store connections allocate distinct durable arrivals');
   if(other!==st)other.close();st.close();if(realIDB)await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});
  }
  // Chat alerts use verified current members, not a separate DM/grant.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  {
   const quiet='f'.repeat(64),next='e'.repeat(64);
   await w.st.write([{s:'convs',k:quiet,v:{id:quiet,kind:'dm',peer:'a'.repeat(32),creator:'other/device'}},{s:'kv',k:'notify',v:{enabled:true,allowed:[],mutes:[conv],rev:1,synced:0}}]);
   const migrated=await w.e.notifyState();
   check(migrated.mutes.includes(quiet)&&migrated.mutes.includes(conv),'legacy quiet DM and explicit group mute preserved');
   check((await w.e.notifyState()).rev===migrated.rev,'quiet migration happens once');
   await w.st.write([{s:'convs',k:quiet},{s:'convs',k:next,v:{id:next,kind:'dm',peer:'a'.repeat(32),creator:'other/device'}}]);
   check(!(await w.e.notifyState()).mutes.includes(next),'future chat defaults unmuted');
   await w.st.write([{s:'convs',k:next}]);
   const peer=await w.st.get('persons',states[0].members.find(m=>m.person!==w.e.me.person).person);
   const makeChat=async()=>{const r=await wire.newRoot(keys,{person:w.e.me.person,roster:w.e.me.hash,address,fingerprint:w.e.fp},{person:peer.person,roster:peer.hash}),id=await wire.rootID(r);await w.st.write([{s:'convs',k:id,v:{id,kind:'dm',peer:peer.person,root:wire.rootJSON(r),creator:address}}]);return id;};
   const firstChat=await makeChat();
   await w.e.muteDM(firstChat,true);
   const secondChat=await makeChat();
   check((await w.e.effectiveNotifyMutes(await w.e.notifyState())).includes(secondChat),'future member root inherits person-chat mute');
   await w.e.muteDM(secondChat,false);
   const cleared=await w.e.effectiveNotifyMutes(await w.e.notifyState());
   check(!cleared.includes(firstChat)&&!cleared.includes(secondChat)&&cleared.includes(conv),'unmute clears all person roots but preserves group mute');
   await w.st.write([{s:'convs',k:firstChat},{s:'convs',k:secondChat}]);
   const senders=await w.e.notifySenders(await w.e.notifyState());
   check(senders.some(x=>x.address===alicePub.address)&&senders.some(x=>x.address===bobPub.address),'group members notify with no DM or sender grant');
   await w.e.muteDM(conv,false);check(!(await w.e.notifyState()).mutes.includes(conv),'group mute can be cleared');
   await w.e.muteDM(conv,true);check((await w.e.notifyState()).mutes.includes(conv),'group mute persists');
   check(await w.e.resolveChannel(await wire.notifyChannel(conv,w.e.fp))===conv,'group push opens exact group');
   const call=w.e.call,info=w.e.notifyInfo,seen=[];
   w.e.notifyInfo=async()=>({});w.e.call=async(method,path,body)=>{seen.push({method,path,body});return {};};
   await w.e.notifySeen(conv,[wire.newID()]);check(seen.length===1&&seen[0].path==='/v1/notify/seen'&&seen[0].body.channel===await wire.notifyChannel(conv,w.e.fp),'visible group suppresses matching push');
   w.e.call=call;w.e.notifyInfo=info;
   w.e.groupSupport=async()=>true;w.e.post=async()=>{};
   const sent=await w.e.sendDM({conv,body:'GROUP_ALERT_NATIVE_CHANNEL'}),copies=(await w.st.all('outbox')).filter(x=>x.lid===sent.lid);
   check(copies.length>0&&copies.every(x=>wire.parseEnvelope(x.envelope).attn&&wire.parseEnvelope(x.envelope).chan),'group ordinary sends retain signed push hints');
  }
  await w.close();
  // Receiving a delayed turn cannot turn the current roster into proof
  // that the newly admitted person held that old sealed copy.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  await w.receive(c['p26-proof']);await w.receive(c['p26-context']);
  {
   const pv=v.participations,old=pv['p26-before-join'].inner,authorFP=await wire.fingerprint(alicePub);
   check(!(await w.st.all('inbox')).some(r=>r.lid===old.lid),'delayed pre-join turn has not arrived');
   for(const type of ['invite','scope','accept'])await w.receive(pv['p6-0-'+type]);
   await w.receive(pv['p26-before-join']);
   check(!!await w.st.get('inbox',old.id),'pre-join sealed turn admitted after later membership');
   const packet=await w.e.groupCurrentState(conv),ref={lid:old.lid,fingerprint:authorFP};
   for(const p of packet.state.members) {
    const reader=!!await w.st.get('kv',w.e.roomReaderKey(conv,ref,p.person,await wire.groupAdmissionHash(p.admission)));
    check(reader===old.fan.some(f=>f.person===p.person),'received copy proves only signed fan '+p.person);
   }
   check(!old.human,'pre-agent turn carries no consent for future agent context');
   await w.receive(pv['p26-share-late-reader']);
   const denied=(await w.e.agentConv(pv['p6-source'])).info,share=pv['p26-share-late-reader'].inner;
   check(!!await w.st.get('inbox',share.id)&&denied.shares.includes(share.body),'valid current member share recorded under exact admission');
   check(denied.grant.length===0,'new member cannot share an unreceived pre-join turn');
   await w.receive(pv['p26-share-author']);
   const positive=(await w.e.agentConv(pv['p6-source'])).info;
   check(positive.grant.length===1&&positive.grant[0].lid===old.lid,'verified original author may still share selected earlier turn');
   await w.receive(pv['p26-no-fan']);
   check(!!await w.st.get('inbox',pv['p26-no-fan'].inner.id),'legacy group copy admitted without fabricating reader proof');
   const legacy={lid:pv['p26-no-fan'].inner.lid,fingerprint:authorFP};
   for(const p of packet.state.members)check(!await w.st.get('kv',w.e.roomReaderKey(conv,legacy,p.person,await wire.groupAdmissionHash(p.admission))),'missing fan grants no inferred reader '+p.person);
   await w.reload();check((await w.e.agentConv(pv['p6-source'])).info.grant.length===1,'reader proof and accepted exact share survive reload');
   // Exercise the real sealed outbox path, with fixture transport only.
   w.e.groupSupport=async()=>{};w.e.requireHumanSupport=async()=>{};w.e.requireGroupHumanSupport=async()=>{};w.e.supports=async()=>[true,''];w.e.post=async()=>{};
   const sent=await w.e.sendDM({conv,body:'P26 sender exact sealed roster'}),copies=(await w.st.all('outbox')).filter(r=>r.lid===sent.lid);
   check(copies.length===packet.state.members.length-1&&copies.every(r=>r.envelope&&r.group_admission),'sender seals copies for every exact current recipient');
   for(const p of packet.state.members)check(!!await w.st.get('kv',w.e.roomReaderKey(conv,{lid:sent.lid,fingerprint:w.e.fp},p.person,await wire.groupAdmissionHash(p.admission))),'sender retains exact sealed roster reader '+p.person);
  }
  await w.close();
  // Current authority is independent of the sequence chosen by an inviter.
  w=await world();for(let i=0;i<=4;i++)await w.receive(c['proof'+'x'.repeat(i)]);await w.receive(c.contextxxxx);
  {
   const forged=v.p6_backdated.map(wire.parseEvent),sender=await wire.parsePublic(v.challenge.rosters[1].devices[0]);
   await wire.verifyEvent(forged[0],sender.sign_key);
   const events=await Promise.all(forged.map(async e=>({e,hash:await wire.eventHash(e)}))),members=await w.e.dmMembers(await w.e.groupRecord(conv),events);
   for(const {e,hash} of events.filter(x=>['invite','scope'].includes(x.e.type)))check(!members.groupInvites.has(hash),'demoted admin old '+e.type+' slot grants no outside authority');
   await w.receive(c['p6-backdated-memberships']);
   check((await w.st.get('held',wire.parseEnvelope(c['p6-backdated-memberships'].envelope).id))?.reason==='invalid','current admin carrier cannot import demoted admin backdated outside invite');
   check(!await w.st.get('kv','room-memberships/'+conv),'rejected backdated carrier installs no membership proof');
  }
  await w.close();
  // A non-admin or outside host cannot revive an agent by replaying its old
  // signed lifecycle evidence. The independently signed state still installs.
  for(const who of ['nonadmin','outsider']) {
   w=await world();await w.receive(c.proof);await w.receive(c.context);
   const carried=c['p6-'+who+'-memberships'];await w.receive(carried);
   check(!await w.st.get('held',wire.parseEnvelope(carried.envelope).id),'untrusted '+who+' lifecycle claims do not block valid group state');
   check(!await w.st.get('kv','room-memberships/'+conv),'untrusted '+who+' carrier installs no old lifecycle authority');
   check((await w.e.groupThread(conv)).agents.length===0&&!(await w.e.groupCurrent(conv)).memberships?.length,'untrusted '+who+' replay creates no group agent or stored membership payload');
   w.e.groupSupport=async()=>{};await w.e.sendDM({conv,body:'AFTER_REMOVED_OUTSIDE_HOST'});
   check((await w.st.all('outbox')).every(r=>r.to!==v.invited_roster.devices[0].address),'untrusted '+who+' replay makes no copy to outside host');
   await w.close();
  }
  // Ends count in a late member's first carrier, only under the original
  // author's removal right; a carrier publisher cannot confer that right.
  for(const mode of ['host','inviter','admin','non-admin','wrong-parent','bad-signature']) {
   w=await world();const last=mode==='admin'?3:0;
   for(let i=0;i<=last;i++)await w.receive(c['proof'+'x'.repeat(i)]);
   await w.receive(c['context'+'x'.repeat(last)]);
   await w.receive(c['p20-'+mode]);
   const valid=['host','inviter','admin'].includes(mode),id=wire.parseEnvelope(c['p20-'+mode].envelope).id;
   check(!!await w.st.get('held',id)===!valid,'carried '+mode+' end requires original removal authority and exact parent/signature');
   if(valid) {
    check((await w.e.agentConv(v.participations['p6-target'])).info.state==='dismissed','late member imports '+mode+' dismissal');
    const record=await w.e.groupRecord(conv),events=await w.e.convEvents(conv),members=await w.e.dmMembers(record,events);
    const proof=await w.e.roomMemberships(record,events,members);
    check(proof.some(e=>e.pid===v.participations['p6-target']&&e.type==='dismiss'),'next carrier retains counted '+mode+' end');
    await w.reload();check((await w.e.agentConv(v.participations['p6-target'])).info.state==='dismissed','carried '+mode+' end survives reload');
    w.e.groupSupport=async()=>{};w.e.requireHumanSupport=async()=>{};w.e.requireGroupHumanSupport=async()=>{};w.e.supports=async()=>[true,''];w.e.post=async()=>{};await w.e.sendDM({conv,body:'AFTER_CARRIED_END'});
    check((await w.st.all('outbox')).every(r=>r.to!==v.invited_roster.devices[0].address),'carried '+mode+' end prevents future outside copy');
   } else check(!await w.st.get('kv','room-memberships/'+conv),'invalid '+mode+' end installs no lifecycle authority');
   await w.close();
  }
  // A declined-then-dismissed agent never had a carried acceptance.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  for(const name of ['p6-1-invite','p6-1-scope','p20-declined-decline','p20-declined-dismiss'])await w.receive(v.participations[name]);
  {
   const record=await w.e.groupRecord(conv),events=await w.e.convEvents(conv),members=await w.e.dmMembers(record,events);
   check((await w.e.agentConv(v.participations['p6-target'])).info.state==='dismissed','declined membership can end without becoming active');
   check((await w.e.roomMemberships(record,events,members)).length===0,'producer omits never-accepted dismissed membership');
   await w.e.discloseHumanAudience();check((await w.st.all('outbox')).length===0,'never-accepted membership has no automatic end fan-out');
  }
  await w.close();
  // Previously counted former-admin ends stay local, without poisoning a
  // newly published context or forwarding a bare acceptance as active again.
  w=await world();for(let i=0;i<=3;i++)await w.receive(c['proof'+'x'.repeat(i)]);await w.receive(c.contextxxx);await w.receive(c['p20-admin']);
  await w.receive(c.proofxxxx);await w.receive(c.contextxxxx);
  {
   const record=await w.e.groupRecord(conv),events=await w.e.convEvents(conv),members=await w.e.dmMembers(record,events),proof=await w.e.roomMemberships(record,events,members);
   check((await w.e.agentConv(v.participations['p6-target'])).info.state==='dismissed','originally counted admin end remains locally counted after demotion');
   check(proof.length===3&&proof.every(e=>e.pid!==v.participations['p6-target']),'producer omits former-admin end and its acceptance together');
   const fresh=await world();for(let i=0;i<=4;i++)await fresh.receive(c['proof'+'x'.repeat(i)]);
   const packet={root,state:states[4],memberships:proof},carried=await fresh.make(wire.SubGroupContext,JSON.parse(wire.groupContextJSON(packet)),4,records[4].hash);
   await fresh.receive(carried);
   check(!await fresh.st.get('held',wire.parseEnvelope(carried.envelope).id)&&(await fresh.e.groupCurrent(conv)).state.seq===4,'former-admin end cannot poison valid new context carrier');
   await fresh.close();
  }
  await w.close();
  // A delayed end follows an already carried acceptance without requiring a
  // new group publication. An unrelated author or changed signature cannot.
  for(const mode of ['host','non-admin','bad-signature']) {
   w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(c['p6-memberships']);
   await w.receive(v.participations['p20-forward-'+mode]);
   const valid=mode==='host',info=(await w.e.agentConv(v.participations['p6-target'])).info;
   check(info.state===(valid?'dismissed':'active'),'forwarded '+mode+' exact counted end authority');
   check(!!await w.st.get('held',wire.parseEnvelope(v.participations['p20-forward-'+mode].envelope).id)===!valid,'forwarded '+mode+' author signature checked before admission');
   if(valid) {
    await w.e.discloseHumanAudience();
    const endRows=()=>w.st.all('outbox').then(rows=>rows.filter(r=>r.pid===info.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='dismiss'));
    const before=await endRows();
    check(before.length===2&&before.every(r=>r.recipient_fp&&r.group_admission&&r.required_cap===wire.CapGroup),'counted end queues exact current member copies');
    await w.e.discloseHumanAudience();await w.e.discloseHumanAudience();
    check((await endRows()).length===before.length,'end retry idempotent per exact member key/admission');
    const nativeEnd=v.participations['p20-forward-host'].inner.body;
    check(before.every(r=>r.body===nativeEnd),'end forwarding retains exact native signed bytes');
    const gated=before[0],pin=await w.st.get('pins',gated.to);
    await w.st.write([{s:'pins',k:gated.to,v:{...pin,pending:{fingerprint:'changed-key'}}}]);
    check(!!(await w.e.externalGate(await w.e.groupRecord(conv),gated,[])).why,'queued end holds on recipient key change');
    await w.st.write([{s:'pins',k:gated.to,v:pin}]);
   }
   await w.close();
  }
  // A new reader receives the original signed membership records quietly,
  // with no earlier message body and no host-local permission grants.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  await w.receive(c['p6-bad-memberships']);
  check((await w.st.get('held',wire.parseEnvelope(c['p6-bad-memberships'].envelope).id))?.reason==='invalid','membership carrier rejects altered original signature');
  check(!await w.st.get('kv','room-memberships/'+conv),'rejected carrier installs no membership metadata');
  await w.receive(c['p6-memberships']);
  for(const pid of [v.participations['p6-source'],v.participations['p6-target']])check((await w.e.agentConv(pid)).info.member&&(await w.e.agentConv(pid)).info.state==='active','quiet original membership carrier restores same accepted PID');
  check((await w.e.groupThread(conv)).messages.length===0,'membership carrier supplies no earlier body');
  await w.reload();
  check((await w.e.agentConv(v.participations['p6-target'])).info.member,'membership proof survives reload');
  check((await w.e.groupCurrent(conv)).memberships.length>0,'departure fixture retains private membership payload before leave');
  {
   const contextPayloads=[],copy=w.e.groupCarrierCopy.bind(w.e);
   w.e.groupCarrierCopy=async(...args)=>{if(args[1]===wire.SubGroupContext&&args[5]?.pid)contextPayloads.push(wire.parseGroupContext(args[3]));return copy(...args);};
   await w.e.leaveGroup(conv);
   check(contextPayloads.length>0&&contextPayloads.every(p=>!p.memberships?.length&&p.withdrawals.length>0),'departure visitor contexts strip every private original invite and retain signed withdrawal');
   check((await w.st.all('outbox')).filter(r=>r.sub===wire.SubGroupContext&&r.pid).length===contextPayloads.length,'stripped visitor contexts produce real sealed outbox copies');
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
  // A different own human approves the phone. This browser already holds
  // history that approver lacks; following its signed roster must start the
  // missing local snapshot through ordinary history upkeep.
  w=await world();await w.receive(c.proof);await w.receive(c.context);await w.receive(v.participations.ordinary);
  {
   const approverAddress='browser/approver',approverKeys=await wire.newKeys(),approverPub=await wire.publicEntry(approverKeys,approverAddress),approverFP=await wire.fingerprint(approverPub);
   const withApprover=await wire.nextRoster(keys,address,roster,[...roster.devices,approverPub],await wire.joinConsent(approverKeys,approverAddress,roster.person,1,await wire.rosterHash(roster)),roster.label,[...await wire.rosterHumans(roster),approverFP]);
   const phoneAddress='browser/third',phoneKeys=await wire.newKeys(),phonePub=await wire.publicEntry(phoneKeys,phoneAddress),phoneFP=await wire.fingerprint(phonePub);
   const withPhone=await wire.nextRoster(approverKeys,approverAddress,withApprover,[...withApprover.devices,phonePub],await wire.joinConsent(phoneKeys,phoneAddress,roster.person,2,await wire.rosterHash(withApprover)),roster.label,[...await wire.rosterHumans(withApprover),phoneFP]);
   await wire.verifyNext(withApprover,roster);await wire.verifyNext(withPhone,withApprover);
   const chain=[roster,withApprover,withPhone],before=await w.e.personRecord(chain.slice(0,2),'self',null);
   w.e.me=before;await w.st.write([{s:'kv',k:'person',v:before}]);await w.e.pinDevices(before);
   w.extraChains.set(roster.person,chain.map(r=>JSON.parse(wire.rosterJSON(r))));
   w.e.groupSupport=async()=>{};
   await w.e.refreshPerson(w.e.me);await w.e.runHistory();
   const copies=(await w.st.all('outbox')).filter(r=>r.to===phoneAddress),history=copies.filter(r=>r.sub==='history');
   check((await w.e.historyBook())[phoneAddress]?.state==='done'&&history.length===1,'another own human learns phone roster and supplies its missing existing snapshot');
   const target=await world(),person=await w.e.personRecord(chain,'self',null);
   target.extraChains.set(roster.person,chain.map(r=>JSON.parse(wire.rosterJSON(r))));
   target.e.keys=phoneKeys;target.e.address=phoneAddress;target.e.fp=phoneFP;target.e.me=person;
   await target.st.write([{s:'kv',k:'identity',v:{keys:phoneKeys,address:phoneAddress,fingerprint:phoneFP}},{s:'kv',k:'person',v:person}]);await target.e.pinDevices(person);
   for(const row of copies){for(const file of row.files||[])target.blobs.set(file.attachment.blob.id,file.ct);await target.receive({envelope:row.envelope});}
   const original=v.participations.ordinary.inner,stored=await target.st.get('inbox',original.id);
   check(stored?.lid===original.lid&&stored.claimed_key===await wire.fingerprint(alicePub)&&stored.history&&stored.replica&&stored.state===''&&stored.synced_from===address,'discovered browser snapshot retains exact original attribution and remains inert');
   // The approver is now offline. A delayed accepted original arrives after
   // this sender completed the phone's snapshot; its old display timestamp
   // cannot strand it behind that snapshot's cursor.
   const delayed={...w.e.itemOf(await w.st.get('inbox',original.id),false),id:wire.newID(),lid:wire.newID(),body:'late accepted person history',ts:original.ts-3600};
   const delayedEnvelope=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:approverAddress,to:address,ts:1700000200,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:wire.historyJSON(delayed)},approverKeys,pub);
   await w.reload();w.e.groupSupport=async()=>{};await w.receive({envelope:delayedEnvelope});await w.e.runHistory();
   const onward=(await w.st.all('outbox')).filter(r=>r.to===phoneAddress&&r.sub==='history'&&wire.parseHistory(r.body).lid===delayed.lid);
   check(onward.length===1,'late accepted original follows completed own-human snapshot after restart');
   await target.receive({envelope:onward[0].envelope});
   const lateStored=await target.st.get('inbox',delayed.id);
   check(lateStored?.lid===delayed.lid&&lateStored.claimed_key===await wire.fingerprint(alicePub)&&lateStored.history&&lateStored.state==='','late accepted original keeps author and remains inert on phone');
   await target.close();
   const checkpoint=JSON.stringify(await w.e.historyBook()),count=(await w.st.all('outbox')).length;
   await w.e.runHistory();await w.reload();w.e.groupSupport=async()=>{};await w.e.runHistory();
   check(JSON.stringify(await w.e.historyBook())===checkpoint&&(await w.st.all('outbox')).length===count,'discovered browser snapshots keep existing completed cursors and copies over repeat wake and restart');
   const valid=await w.st.get('kv','person'),phonePin=await w.st.get('pins',phoneAddress),sourcePin=await w.st.get('pins',address),savedBook=await w.e.historyBook();
   for(const mode of ['source-agent','recipient-agent','removed','foreign','frozen','changed-key','pending-recipient','pending-source']) {
    const altered=structuredClone(valid),pin=structuredClone(phonePin),selfPin=structuredClone(sourcePin);
    if(mode==='source-agent')altered.human_keys=altered.human_keys.filter(fp=>fp!==w.e.fp);
    if(mode==='recipient-agent')altered.human_keys=altered.human_keys.filter(fp=>fp!==phoneFP);
    if(mode==='removed')altered.devices=altered.devices.filter(d=>d.address!==phoneAddress);
    if(mode==='foreign'||mode==='frozen')altered.state=mode==='foreign'?'pinned':'conflict';
    if(mode==='changed-key')pin.fingerprint=approverFP;
    if(mode==='pending-recipient')pin.pending={...pin};
    if(mode==='pending-source')selfPin.pending={...selfPin};
    w.e.me=altered;
    await w.st.write([{s:'kv',k:'person',v:altered},{s:'pins',k:phoneAddress,v:pin},{s:'pins',k:address,v:selfPin},{s:'kv',k:'history',v:{}}]);
    await w.e.reconcileHistory();
    check(!(await w.e.historyBook())[phoneAddress],'browser history discovery refuses '+mode);
    await w.st.write([{s:'kv',k:'history',v:savedBook}]);
    let refused=false;try{await w.e.discoveredHistoryDelivery(history[0]);}catch(e){refused=true;}
    check(refused,'already queued automatic browser history retains authority guard after '+mode);
    const call=w.e.call;let sends=0;w.e.call=async()=>{sends++;throw Error('unexpected history send');};
    await w.e.postStored(history[0]);w.e.call=call;
    check(sends===0&&(await w.st.get('outbox',history[0].id)).detail.includes('current own human'),'browser post refuses guarded history before transport after '+mode);
    w.e.me=valid;await w.st.write([{s:'kv',k:'person',v:valid},{s:'pins',k:phoneAddress,v:phonePin},{s:'pins',k:address,v:sourcePin},{s:'outbox',k:history[0].id,v:history[0]}]);
   }
   for(const state of ['running','done','ended']) {
    const kept={...savedBook[phoneAddress],fingerprint:'original-key',state,pos:{conv:'kept',ms:12,id:'kept'},own_human:undefined};
    await w.st.write([{s:'kv',k:'history',v:{[phoneAddress]:kept}}]);await w.e.reconcileHistory();
    check(JSON.stringify((await w.e.historyBook())[phoneAddress])===JSON.stringify(kept),'browser discovery preserves existing '+state+' job and key/cursor/policy');
   }
   const running={...savedBook[phoneAddress],state:'running',pos:null},dev=valid.devices.find(d=>d.address===phoneAddress),originalWrite=w.st.write.bind(w.st);let raced=false;
   const racingItem={...delayed,id:wire.newID(),lid:wire.newID(),body:'accepted before concurrent human role loss'};
   const racingEnvelope=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:approverAddress,to:address,ts:1700000200,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:wire.historyJSON(racingItem)},approverKeys,pub);
   const admitted=await w.e.admitInner(racingEnvelope,wire.parseEnvelope(racingEnvelope));await w.st.write(admitted,admitted.checks);
   await w.st.write([{s:'kv',k:'history',v:{[phoneAddress]:running}}]);
   w.st.write=async(ops,expected)=>{if(!raced&&ops.some(o=>o.s==='outbox'&&o.v?.sub==='history')){raced=true;const changed={...valid,human_keys:valid.human_keys.filter(fp=>fp!==phoneFP)};await originalWrite([{s:'kv',k:'person',v:changed}]);}return originalWrite(ops,expected);};
   let refused=false;try{await w.e.historyStep(dev,running);}catch(e){refused=e.message==='storage changed during verification';}finally{w.st.write=originalWrite;}
   check(raced&&refused&&(await w.st.all('outbox')).length===count&&(await w.e.historyBook())[phoneAddress].state==='running','browser transaction aborts new history and cursor when human role changes after preparation');
   await w.st.write([{s:'kv',k:'person',v:valid},{s:'kv',k:'history',v:savedBook}]);w.e.me=valid;
   await w.e.runHistory();const restoredCheckpoint=JSON.stringify(await w.e.historyBook());
   const reconcile=w.e.reconcileHistory.bind(w.e);let scans=0;
   w.e.reconcileHistory=async()=>{scans++;await reconcile();if(scans===1)w.e.runHistory();};
   await w.e.runHistory();w.e.reconcileHistory=reconcile;
   check(scans===2&&JSON.stringify(await w.e.historyBook())===restoredCheckpoint,'history wake during an active browser pass is drained once without changing completed jobs');

   // More than one page of legitimately admitted older originals: a newly
   // linked reader gets the newest useful page before resumable backfill.
   const pageItems=[],at=Math.max(...(await w.st.all('inbox')).map(r=>r.at||0))+1;
   const accept=async item=>{const envelope=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:approverAddress,to:address,ts:1700000200,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:wire.historyJSON(item)},approverKeys,pub);const ops=await w.e.admitInner(envelope,wire.parseEnvelope(envelope));await w.st.write(ops,ops.checks);};
   for(let i=0;i<57;i++){const item={...delayed,id:wire.newID(),lid:wire.newID(),at:at+i,body:'newest-page-'+i,...(i===56?{reply_to:pageItems[0].id}:{})};await accept(item);pageItems.push(item);}
   const fourthAddress='browser/fourth',fourthKeys=await wire.newKeys(),fourthPub=await wire.publicEntry(fourthKeys,fourthAddress),fourthFP=await wire.fingerprint(fourthPub);
   const fourthRoster=await wire.nextRoster(keys,address,withPhone,[...withPhone.devices,fourthPub],await wire.joinConsent(fourthKeys,fourthAddress,roster.person,3,await wire.rosterHash(withPhone)),roster.label,[...await wire.rosterHumans(withPhone),fourthFP]);
   await wire.verifyNext(fourthRoster,withPhone);const four=await w.e.personRecord([...chain,fourthRoster],'self',valid);w.e.me=four;
   w.extraChains.set(roster.person,[...chain,fourthRoster].map(r=>JSON.parse(wire.rosterJSON(r))));await w.st.write([{s:'kv',k:'person',v:four}]);await w.e.pinDevices(four);await w.e.reconcileHistory();
   const fourth=four.devices.find(d=>d.address===fourthAddress),fourthCopies=()=>w.st.all('outbox').then(rows=>rows.filter(r=>r.to===fourthAddress&&r.sub==='history').sort((a,b)=>(a.send_order??a.at)-(b.send_order??b.at)));
   await w.e.historyStep(fourth,(await w.e.historyBook())[fourthAddress]);
   const firstPage=(await fourthCopies()).map(r=>wire.parseHistory(r.body));
   const filling=(await w.e.overview()).history.find(j=>j.device===fourthAddress);check(filling.state==='running'&&filling.done===0&&filling.total===0,'recent-first backfill does not reuse a completed conversation count');
   check(firstPage.length===51&&firstPage[0].lid===pageItems[0].lid&&firstPage.slice(1).every((h,i)=>h.body==='newest-page-'+(56-i)),'first bounded group history page is newest first with its exact old reply dependency queued before display');
   const during={...delayed,id:wire.newID(),lid:wire.newID(),at:at-86400000,body:'late arrival while older backfill runs'};await accept(during);
   await w.e.historyStep(fourth,(await w.e.historyBook())[fourthAddress]);
   check((await fourthCopies()).some(r=>wire.parseHistory(r.body).lid===during.lid),'arrival tail supplies old timestamp immediately while recent/older backfill remains');
   await w.reload();w.e.groupSupport=async()=>{};await w.e.runHistory();
   const complete=await fourthCopies();
   check(pageItems.every(item=>complete.filter(r=>wire.parseHistory(r.body).lid===item.lid).length===1)&&complete.filter(r=>wire.parseHistory(r.body).lid===during.lid).length===1,'recent/older/tail resume after restart with each exact original queued once');
   const oldPos=JSON.stringify((await w.e.historyBook())[fourthAddress].pos);
   // A missing group context must retain its source ref while a healthy DM
   // continues in the same page. Restored proof wakes the existing deferral.
   const blocked={...delayed,id:wire.newID(),lid:wire.newID(),at:at-86400001,body:'wait for exact context'};await accept(blocked);
   check((await w.e.overview()).history.find(j=>j.device===fourthAddress).state==='running','accepted late history keeps progress unfinished until its arrival tail is queued');
   const alice=await w.st.get('persons',v.challenge.rosters[0].person),dmRoot=await wire.newRoot(keys,{person:four.person,roster:four.hash,address,fingerprint:w.e.fp},{person:alice.person,roster:alice.hash}),dm=await wire.rootID(dmRoot);
   const dmInner={v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000000,kind:'message',conv:dm,root:wire.rootJSON(dmRoot),body:'healthy other chat while group proof waits',origin:'ui'};
   const dmEnvelope=await wire.seal(dmInner,aliceKeys,pub),dmOps=await w.e.admitInner(dmEnvelope,wire.parseEnvelope(dmEnvelope));await w.st.write(dmOps,dmOps.checks);
   const group=await w.st.get('kv','group/'+conv);await w.st.write([{s:'kv',k:'group/'+conv,v:{...group,context:null}}]);
   const more=await w.e.historyStep(fourth,(await w.e.historyBook())[fourthAddress]);
   check(!more&&(await fourthCopies()).some(r=>wire.parseHistory(r.body).lid===dmInner.lid)&&!(await fourthCopies()).some(r=>wire.parseHistory(r.body).lid===blocked.lid),'missing exact group context defers one source without blocking healthy chat or busylooping');
   const deferred=await w.st.prefix('kv','history-deferred/'+fourthFP+'/');
   check(deferred.some(r=>r.id===blocked.id)&&JSON.stringify((await w.e.historyBook())[fourthAddress].pos)===oldPos,'unavailable accepted source stays durable while legacy completed cursor is preserved');
   const pendingProgress=(await w.e.overview()).history.find(j=>j.device===fourthAddress);
   check(pendingProgress.state==='running'&&pendingProgress.done===0&&pendingProgress.total===0,'persisted deferred history never claims all chats queued');
   const durableBook=JSON.stringify(await w.e.historyBook());await w.reload();w.e.groupSupport=async()=>{};
   check((await w.e.overview()).history.find(j=>j.device===fourthAddress).state==='running'&&JSON.stringify(await w.e.historyBook())===durableBook,'unfinished history projection survives restart without changing stored cursors or jobs');
   const heldCount=(await fourthCopies()).length;await w.e.historyStep(fourth,(await w.e.historyBook())[fourthAddress]);
   check((await fourthCopies()).length===heldCount,'unchanged proof does not repeat a deferred attempt during internal drain');
   await w.st.write([{s:'kv',k:'group/'+conv,v:group}]);await w.e.runHistory();
   check((await fourthCopies()).filter(r=>wire.parseHistory(r.body).lid===blocked.lid).length===1&&!(await w.st.prefix('kv','history-deferred/'+fourthFP+'/')).some(r=>r.id===blocked.id),'existing proof wake recovers retained source exactly once after context restoration');
   check((await w.e.overview()).history.find(j=>j.device===fourthAddress).state==='done','history projection finishes only after deferred source is actually queued');
   // A v1 completed job had neither acceptance watermark nor copy metadata.
   // Upgrade reconciles accepted rows once, retaining its exact old cursor.
   const missed={...delayed,id:wire.newID(),lid:wire.newID(),at:at-86400002,body:'accepted before upgrade after legacy snapshot completed'};await accept(missed);
   const legacyBook=await w.e.historyBook(),legacy={...legacyBook[fourthAddress]};delete legacy.catchup;
   const copyKey='history-copy/'+fourthFP+'/'+conv+'/'+pageItems[0].from_key+'/'+pageItems[0].lid;
   await w.st.write([{s:'kv',k:'history',v:{...legacyBook,[fourthAddress]:legacy}},{s:'kv',k:copyKey}]);await w.e.runHistory();
   const migrated=await fourthCopies();
   check(migrated.filter(r=>wire.parseHistory(r.body).lid===missed.lid).length===1&&JSON.stringify((await w.e.historyBook())[fourthAddress].pos)===oldPos,'one-time accepted-row upgrade fills pre-upgrade gap without resetting completed cursor');
   check(migrated.filter(r=>wire.parseHistory(r.body).lid===pageItems[0].lid).length===2,'legacy missing tuple evidence reissues one inert copy with the same original logical identity');
   await w.e.runHistory();check((await fourthCopies()).length===migrated.length,'completed v2 migration does not resnapshot on later wake');
   const ledger=await w.st.get('kv',copyKey),tuple=conv+'/'+pageItems[0].from_key+'/'+pageItems[0].lid,deferredKey='history-deferred/'+fourthFP+'/'+tuple;
   await w.st.write([{s:'kv',k:copyKey,v:{...ledger,hash:'0'.repeat(64)}},{s:'kv',k:deferredKey,v:{key:deferredKey,tuple,conv,id:pageItems[0].id,dir:'inbox'}}]);await w.e.runHistory();
   check((await fourthCopies()).length===migrated.length&&(await w.st.get('kv',deferredKey))?.why==='conflicting_duplicate'&&(await w.st.get('kv',copyKey)).hash==='0'.repeat(64),'conflicting local tuple evidence stays deferred without a new carrier or overwritten authority');
   await w.st.write([{s:'kv',k:copyKey,v:ledger}]);await w.e.runHistory();
   check(!await w.st.get('kv',deferredKey)&&(await fourthCopies()).length===migrated.length,'restored exact tuple evidence clears deferral without duplicate sending');
   const retained=(await w.st.all('outbox')).filter(r=>r.to===fourthAddress&&['history','group-proof','group-context'].includes(r.sub));
   await w.st.write(retained.map(r=>({s:'outbox',k:r.id,v:{...r,state:'quarantined'}})));
   // Reconsidering a logical source may encounter a receiver-held carrier;
   // neither that pass nor a restart should mint a second ciphertext ID.
   await w.st.write([{s:'kv',k:deferredKey,v:{key:deferredKey,tuple,conv,id:pageItems[0].id,dir:'inbox'}}]);
   await w.e.runHistory();await w.reload();w.e.groupSupport=async()=>{};await w.e.runHistory();
   check((await w.st.all('outbox')).filter(r=>r.to===fourthAddress&&['history','group-proof','group-context'].includes(r.sub)).length===retained.length&&!await w.st.get('kv',deferredKey),'unchanged quarantined history and context survive wake/restart without fresh carrier IDs');
   const blockedProgress=(await w.e.overview()).history.find(j=>j.device===fourthAddress);
   check(blockedProgress.state==='done'&&blockedProgress.delivery_known&&blockedProgress.blocked===(await w.st.prefix('kv','history-copy/'+fourthFP+'/')).length&&!blockedProgress.queued&&!blockedProgress.custody&&!blockedProgress.delivered,'producer done remains distinct from receiver-held current exact copies');
   const reader=await world();reader.extraChains.set(roster.person,[...chain,fourthRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
   reader.e.keys=fourthKeys;reader.e.address=fourthAddress;reader.e.fp=fourthFP;reader.e.me=four;
   await reader.st.write([{s:'kv',k:'identity',v:{keys:fourthKeys,address:fourthAddress,fingerprint:fourthFP}},{s:'kv',k:'person',v:four}]);await reader.e.pinDevices(four);
   for(const row of (await w.st.all('outbox')).filter(r=>r.to===fourthAddress)){for(const file of row.files||[])reader.blobs.set(file.attachment.blob.id,file.ct);await reader.receive({envelope:row.envelope});}
   const received=await reader.st.all('inbox');
   check([...pageItems,during,blocked,missed].every(item=>received.filter(r=>r.lid===item.lid&&r.claimed_key===item.from_key&&r.history&&r.state==='').length===1),'actual receiver stores each reordered/migrated original once under original author with no execution');
   await reader.close();
  }
  await w.close();
  // Actual native HistoryItem bytes, forwarded by a newly linked own key.
  // The original participant events stay signed; all replicas remain inert.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  const linkedKeys=await wire.newKeys(),linkedAddress='browser/phone',linkedPub=await wire.publicEntry(linkedKeys,linkedAddress);
  const linkedRoster=await wire.nextRoster(keys,address,roster,[...roster.devices,linkedPub],await wire.joinConsent(linkedKeys,linkedAddress,roster.person,1,await wire.rosterHash(roster)),roster.label,[...await wire.rosterHumans(roster),await wire.fingerprint(linkedPub)]);
  await wire.verifyNext(linkedRoster,roster);w.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
  const linkedPerson=await w.e.personRecord([roster,linkedRoster],'self',null);w.e.me=linkedPerson;await w.st.write([{s:'kv',k:'person',v:linkedPerson}]);await w.e.pinDevices(linkedPerson);
  // A new own device has no group proof/context yet, even for an empty
  // group. Snapshot carriers use the existing signed journal and blob path.
  {
   const dev=linkedPerson.devices.find(d=>d.address===linkedAddress),job={device:dev.address,fingerprint:dev.fingerprint,state:'running',pos:null};
   await w.st.write([{s:'kv',k:'history',v:{[dev.address]:job}}]);
   const support=w.e.groupSupport.bind(w.e);w.e.groupSupport=async()=>{};
   await w.e.historyStep(dev,job);
   const carriers=()=>w.st.all('outbox').then(rows=>rows.filter(r=>r.to===dev.address&&[wire.SubGroupProof,wire.SubGroupContext].includes(r.sub)&&!r.pid));
   const original=await carriers();
   check(original.some(r=>r.sub===wire.SubGroupProof)&&original.some(r=>r.sub===wire.SubGroupContext),'empty own-linked group snapshot queues original proof and context');
   const target=await world();
   target.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
   target.e.keys=linkedKeys;target.e.address=linkedAddress;target.e.fp=dev.fingerprint;target.e.me=linkedPerson;
   await target.st.write([{s:'kv',k:'identity',v:{keys:linkedKeys,address:linkedAddress,fingerprint:dev.fingerprint}},{s:'kv',k:'person',v:linkedPerson}]);await target.e.pinDevices(linkedPerson);
   for(const row of original){const file=row.files[0];target.blobs.set(file.attachment.blob.id,file.ct);await target.receive({envelope:row.envelope});}
   check((await target.e.groupCurrent(conv)).state.seq===states[0].seq&&!(await target.e.groupThread(conv)).messages.length,'new linked browser admits empty group from original signed carriers');
   await target.close();
   const done=(await w.e.historyBook())[dev.address],before=JSON.stringify(done.pos);
   await w.reload();w.e.groupSupport=async()=>{};
   await w.e.historyPasses();check((await carriers()).length===original.length,'completed browser snapshot does not duplicate carrier batch after restart');
   for(const row of original)await w.st.write([{s:'outbox',k:row.id}]);
   await w.e.historyPasses();const recovered=await carriers();
   check(recovered.length===original.length&&JSON.stringify((await w.e.historyBook())[dev.address].pos)===before,'legacy done browser snapshot recovers missing group carriers without cursor reset');
   const context=recovered.find(r=>r.sub===wire.SubGroupContext);await w.st.write([{s:'outbox',k:context.id,v:{...context,state:'expired'}}]);
   await w.e.historyPasses();const count=(await carriers()).length;
   check(count===2*original.length,'expired browser group carrier batch retries');
   await w.e.historyPasses();check((await carriers()).length===count,'usable recovered browser carrier batch deduplicates');
   for(const row of await carriers())await w.st.write([{s:'outbox',k:row.id}]);
   let wrongKey=false;try{await w.e.groupHistoryCarriers(await w.e.groupRecord(conv),{...dev,fingerprint:await wire.fingerprint(alicePub)},[]);}catch(e){wrongKey=true;}
   check(wrongKey&&!(await carriers()).length,'browser snapshot refuses changed exact own recipient key');
   await w.st.write([{s:'kv',k:'history',v:{[dev.address]:job}}]);
   const originalWrite=w.st.write.bind(w.st),oldPerson=await w.e.personRecord([roster],'self',null);let raced=false;
   w.st.write=async(ops,expected)=>{if(!raced&&ops.some(o=>o.s==='outbox'&&o.v?.sub===wire.SubGroupContext)){raced=true;await originalWrite([{s:'kv',k:'person',v:oldPerson}]);}return originalWrite(ops,expected);};
   let refused=false;try{await w.e.historyStep(dev,job);}catch(e){refused=e.message==='storage changed during verification';}finally{w.st.write=originalWrite;}
   check(raced&&refused&&!(await carriers()).length&&(await w.e.historyBook())[dev.address].state==='running','competing own device removal prevents browser carrier commit and cursor advance');
   await w.st.write([{s:'kv',k:'person',v:linkedPerson}]);w.e.me=linkedPerson;
   await w.st.write([{s:'kv',k:'history',v:{}}]);w.e.groupSupport=support;
  }
  const historyEnvelope=async body=>wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:linkedAddress,to:address,ts:1700000101,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body},linkedKeys,pub);
  // Exact old member-host admission survives removal/rejoin only as own
  // inert history, under the original signed state and current own roster.
  {
   const past=await world(),hv=v.historical_witness,hs=hv.states.map(wire.parseGroupState),hr=hv.records.map(wire.parseGroupCommit),original=wire.parseHistory(hv.history);
   try{
    past.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));past.e.me=linkedPerson;
    await past.st.write([{s:'kv',k:'person',v:linkedPerson}]);await past.e.pinDevices(linkedPerson);
    await past.receive(await past.make(wire.SubGroupProof,JSON.parse(wire.groupJournalJSON({records:hr,more:false})),3,hr[3].hash));
    await past.receive(await past.make(wire.SubGroupContext,JSON.parse(wire.groupContextJSON({root,state:hs[3]})),3,hr[3].hash));
    const current=await past.e.groupCurrent(conv),before=JSON.stringify(await past.st.get('kv','group/'+conv));
    check(current.state.seq===3&&await wire.groupAdmissionHash(wire.groupMember(current.state,alicePub.person||v.challenge.rosters[0].person).admission)!==original.target.group_admission,'host removal and readmission changes exact native-signed admission');
    check(wire.historyJSON(original)===hv.history,'native historical witness exact JSON parity');
    check(wire.capsReads({caps:[wire.CapRoom]},wire.CapConvClear)&&!wire.capsReads({caps:[wire.CapRoom]},wire.CapOwnSyncV2),'released room capability preserves clear support without implying own2');
    let missing=false;try{await past.e.groupParticipationHistoryCheck(conv,{...original,group_history:undefined},{address:linkedAddress,fingerprint:await wire.fingerprint(linkedPub)});}catch{missing=true;}check(missing,'old PID without witness remains unavailable');
    const env=await historyEnvelope(hv.history);await past.receive({envelope:env});
    const stored=await past.st.get('inbox',original.id);
    check(!await past.st.get('held',wire.parseEnvelope(env).id)&&stored?.history&&stored.state===''&&stored.group_history,'own linked browser accepts historical witness without job');
    check(JSON.stringify(await past.st.get('kv','group/'+conv))===before,'historical witness never replaces live group context');
    for(const name of ['p6-ask','p6-answer','p6-status']){
     const item=wire.parseHistory(pv['history-'+name]);item.group_history=original.group_history;
     const carrier=await historyEnvelope(wire.historyJSON(item));await past.receive({envelope:carrier});
     check(!await past.st.get('held',wire.parseEnvelope(carrier).id)&&(await past.st.get('inbox',item.id))?.state==='','original request binds inert historical '+name+' across host readmission');
    }

    const fileItem=wire.parseHistory(pv['history-member-question']);fileItem.group_history={...original.group_history,memberships:['member-invite','member-accept'].map(name=>wire.parseEvent(pv[name].inner.body))};
    const fileCarrier=await historyEnvelope(wire.historyJSON(fileItem));await past.receive({envelope:fileCarrier});
    check(!await past.st.get('held',wire.parseEnvelope(fileCarrier).id),'old PID file manifests admit under exact witness');
    const filePacket=await past.e.groupTurnEvidence(conv),fileRow=await past.st.get('inbox',fileItem.id),file=fileRow.attachments[0],descriptor={v:1,type:'request',lid:fileRow.lid,author:fileRow.fp,hash:await wire.groupHistoryContentHash(conv,fileRow),index:0,name:file.name,size:file.size,sha256:file.sha256,group_admission:fileRow.group_admission};
    await past.e.groupFileSource(conv,descriptor);await past.e.groupFileAuthorized(filePacket.packet,filePacket.members,address,past.e.fp,descriptor);
    check(true,'old PID attachment source reuses exact verified witness');
    let changedFile=false;try{await past.e.groupFileSource(conv,{...descriptor,sha256:'f'.repeat(64)});}catch{changedFile=true;}check(changedFile,'old witnessed attachment still rejects changed file hash');
    past.e.groupSupport=async()=>{};await past.e.requestGroupFile(fileRow,0);
    const offered=await wire.encryptFile(new TextEncoder().encode(pv.files[0].bytes),file.name,pub);past.blobs.set(offered.attachment.blob.id,offered.ct);
    const offer=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:linkedAddress,to:address,ts:1700000102,kind:'message',conv,root:wire.rootJSON(root),sub:'file',replica:true,body:wire.groupFileMsgJSON({...descriptor,type:'offer',available:true}),attachments:[offered.attachment]},linkedKeys,pub);
    await past.receive({envelope:offer});const opened=await past.e.openFile(fileRow.id,0,'in');check(decode.decode(opened.bytes)===pv.files[0].bytes,'old witnessed attachment recovers exact encrypted bytes');
    const live=await past.e.dmMembers(await past.e.groupRecord(conv),await past.e.convEvents(conv)),info=past.e.resolveAgent(original.pid,await past.e.convEvents(conv),live);
    check(!info.invite&&!(await past.e.convEvents(conv)).some(r=>r.e.pid===original.pid),'historical evidence never enters live participation ledger');
    for(const mode of ['other-pid','wrong-state','forged-state','unbound-roster','wrong-task-epoch','unknown-own-admission','foreign-forwarder']){
     const bad=wire.parseHistory(hv.history);let forwarder={address:linkedAddress,fingerprint:await wire.fingerprint(linkedPub)};
     if(mode==='other-pid')bad.pid=wire.newID();
     if(mode==='wrong-state')bad.group_history.state=hs[3];
     if(mode==='forged-state')bad.group_history.state.title+=' forged';
     if(mode==='unbound-roster')bad.group_history.memberships[0].author.roster='f'.repeat(64);
     if(mode==='wrong-task-epoch'){const index=bad.group_history.memberships.findIndex(e=>e.type==='invite');bad.group_history.memberships[index]=await wire.signEvent(aliceKeys,{...bad.group_history.memberships[index],task_keys:[await wire.fingerprint(alicePub)],group:{...bad.group_history.memberships[index].group,task_admissions:['e'.repeat(64)]}});}
     if(mode==='unknown-own-admission')bad.group_admission='e'.repeat(64);
     if(mode==='foreign-forwarder')forwarder={address:alicePub.address,fingerprint:await wire.fingerprint(alicePub)};
     let refused=false;try{await past.e.groupParticipationHistoryCheck(conv,bad,forwarder,[]);}catch{refused=true;}check(refused,'historical witness refuses '+mode);
    }
    await past.reload();check(wire.groupContextJSON((await past.st.get('inbox',original.id)).group_history)===wire.groupContextJSON(original.group_history),'original historical witness survives reload exactly');
    past.e.groupSupport=async()=>{};past.e.sendGroupCopy=async()=>'';
    const dev=linkedPerson.devices.find(d=>d.address===linkedAddress),again=await past.e.historyCopy(dev,await past.e.groupRecord(conv),past.e.itemOf(await past.st.get('inbox',original.id),false));
    check(wire.groupContextJSON(wire.parseHistory(again.body).group_history)===wire.groupContextJSON(original.group_history),'new device forwards exact retained witness');
    // The scheduler must also forward an accepted event whose predecessor
    // exists only inside its verified witness, without making a live event.
    const accept=wire.parseHistory(pv['history-member-accept']);accept.group_history=fileItem.group_history;
    await past.receive({envelope:await historyEnvelope(wire.historyJSON(accept))});
    check(!!(await past.st.get('inbox',accept.id))?.group_history&&!(await past.e.convEvents(conv)).some(r=>r.e.pid===accept.pid),'witness-only predecessor stays outside the live event ledger');
    await past.e.runHistory();
    const copies=(await past.st.all('outbox')).filter(r=>r.to===dev.address&&r.sub==='history').map(r=>wire.parseHistory(r.body));
    check(copies.filter(h=>h.id===accept.id&&h.lid===accept.lid&&h.from_key===accept.from_key).length===1&&copies.some(h=>h.id===original.id),'normal catch-up queues witnessed event and old request with exact original identities');
    check(!(await past.st.prefix('kv','history-deferred/'+dev.fingerprint+'/')).some(r=>[accept.id,original.id].includes(r.id)),'verified witness-only dependencies leave no unresolved source reference');

    // An unused task key may have left its person's roster since this
    // signed invite. Only its exact original author roster proves it.
    const aliceRoster=await wire.parseRoster(v.challenge.rosters[0]),retiredKeys=await wire.newKeys(),retiredPub=await wire.publicEntry(retiredKeys,'alice/retired'),retiredFP=await wire.fingerprint(retiredPub);
    const signAliceRoster=async r=>{const unsigned=JSON.parse(wire.rosterJSON(r));delete unsigned.sig;delete unsigned.join;r.sig=new Uint8Array(await crypto.subtle.sign('Ed25519',aliceKeys.sign,new TextEncoder().encode('agentnet-person-v2\n'+JSON.stringify(unsigned))));return r;};
    const added=await signAliceRoster({...aliceRoster,seq:1,prev:await wire.rosterHash(aliceRoster),devices:[...aliceRoster.devices,retiredPub],by:await wire.fingerprint(alicePub),sig:null,join:await wire.joinConsent(retiredKeys,retiredPub.address,aliceRoster.person,1,await wire.rosterHash(aliceRoster))});
    const retiredRoster=await signAliceRoster({...added,seq:2,prev:await wire.rosterHash(added),devices:aliceRoster.devices,sig:null,join:null});
    await wire.verifyNext(added,aliceRoster);await wire.verifyNext(retiredRoster,added);
    const aliceChain=[aliceRoster,added,retiredRoster],currentAlice=await past.e.personRecord(aliceChain,'pinned',await past.st.get('persons',aliceRoster.person));
    past.extraChains.set(aliceRoster.person,aliceChain.map(r=>JSON.parse(wire.rosterJSON(r))));await past.st.write([{s:'persons',k:aliceRoster.person,v:currentAlice}]);await past.e.pinDevices(currentAlice);
    const oldInvite=original.group_history.memberships.find(e=>e.type==='invite'),taskInvite=await wire.signEvent(aliceKeys,{...oldInvite,pid:wire.newID(),author:{...oldInvite.author,roster:await wire.rosterHash(added)},task_keys:[retiredFP],group:{...oldInvite.group,task_admissions:[oldInvite.author.group_admission]}});
    const taskHistory={...wire.parseHistory(pv['history-member-invite']),id:wire.newID(),lid:wire.newID(),pid:taskInvite.pid,body:wire.eventJSON(taskInvite),group_history:{...original.group_history,memberships:[taskInvite]}};
    const taskEnvelope=await historyEnvelope(wire.historyJSON(taskHistory));await past.receive({envelope:taskEnvelope});
    check(!!(await past.st.get('inbox',taskHistory.id))?.history&&!await past.st.get('held',wire.parseEnvelope(taskEnvelope).id),'verified historical author roster admits removed unused task device');
    const taskEvents=[{e:taskInvite,hash:await wire.eventHash(taskInvite)}],taskPacket=await past.e.groupHistoryWitness(await past.e.groupRecord(conv),taskHistory),taskMembers=await past.e.dmMembers(await past.e.groupRecord(conv),taskEvents,[],taskPacket);
    check(past.e.resolveAgent(taskInvite.pid,taskEvents,taskMembers).invite===taskEvents[0].hash&&!taskMembers.epochs.has(retiredFP)&&![...taskMembers.values()].some(p=>p.devices.some(d=>d.fingerprint===retiredFP)),'historical task proof resolves invite without restoring current device or epoch');
    const ordinaryMembers=await past.e.dmMembers(await past.e.groupRecord(conv),taskEvents,[],wire.parseGroupContext(wire.groupContextJSON(taskHistory.group_history)));
    check(!past.e.resolveAgent(taskInvite.pid,taskEvents,ordinaryMembers).invite&&!(await past.e.convEvents(conv)).some(r=>r.e.pid===taskInvite.pid),'unverified context and live ledger gain no historical task authority');
    for(const mode of ['wrong-task-admission','unknown-author-roster','key-absent-from-exact-roster','unknown-task-key']){
     const fields={...taskInvite,author:{...taskInvite.author},group:{...taskInvite.group}};
     if(mode==='wrong-task-admission')fields.group.task_admissions=['e'.repeat(64)];
     if(mode==='unknown-author-roster')fields.author.roster='f'.repeat(64);
     if(mode==='key-absent-from-exact-roster')fields.author.roster=await wire.rosterHash(aliceRoster);
     if(mode==='unknown-task-key')fields.task_keys=[await wire.fingerprint(await wire.publicEntry(await wire.newKeys(),'alice/unknown'))];
     const event=await wire.signEvent(aliceKeys,fields),bad={...taskHistory,body:wire.eventJSON(event),group_history:{...taskHistory.group_history,memberships:[event]}};
     let refused=false;try{await past.e.groupParticipationHistoryCheck(conv,bad,dev,[]);}catch{refused=true;}check(refused,'removed task witness refuses '+mode);
    }
    const reassigned=await wire.nextRoster(keys,address,linkedRoster,[...linkedRoster.devices,retiredPub],await wire.joinConsent(retiredKeys,retiredPub.address,roster.person,2,await wire.rosterHash(linkedRoster)));
    await wire.verifyNext(reassigned,linkedRoster);const reassignedPerson=await past.e.personRecord([roster,linkedRoster,reassigned],'self',linkedPerson);
    past.e.me=reassignedPerson;await past.st.write([{s:'kv',k:'person',v:reassignedPerson}]);
    let epochConflict=false;try{await past.e.groupParticipationHistoryCheck(conv,taskHistory,dev,[]);}catch{epochConflict=true;}
    check(epochConflict,'current task device in another admission is never repaired by historical fallback');
    past.e.me=linkedPerson;await past.st.write([{s:'kv',k:'person',v:linkedPerson}]);
    await past.reload();past.e.groupSupport=async()=>{};past.e.sendGroupCopy=async()=>'';
    const taskStored=await past.st.get('inbox',taskHistory.id),taskCopy=await past.e.historyCopy(dev,await past.e.groupRecord(conv),past.e.itemOf(taskStored,false));
    check(wire.parseHistory(taskCopy.body).id===taskHistory.id&&taskStored.state===''&&JSON.stringify(await past.st.get('kv','group/'+conv))===before,'removed task witness revalidates after restart and forwards inert original without live group changes');

    const ownPin=await past.st.get('pins',linkedAddress);await past.st.write([{s:'pins',k:linkedAddress,v:{...ownPin,pending:{fingerprint:"changed-key"}}}]);
    let pendingIn=false,pendingOut=false;try{await past.e.groupParticipationHistoryCheck(conv,original,dev,[]);}catch{pendingIn=true;}try{await past.e.historyCopy(dev,await past.e.groupRecord(conv),past.e.itemOf(stored,false));}catch{pendingOut=true;}
    check(pendingIn&&pendingOut,'historical witness refuses pending own forwarder and reader pins');await past.st.write([{s:'pins',k:linkedAddress,v:ownPin}]);
    // Reconstruct source evidence from original ciphertext, with no inferred old epoch.
    const source={...stored,group_history:undefined};await past.st.write([{s:'inbox',k:source.id,v:source}]);
    const rebuilt=await past.e.historyCopy(dev,await past.e.groupRecord(conv),past.e.itemOf(source,false));
    check(wire.parseHistory(rebuilt.body).group_history.state.seq===0,'source reconstructs original encrypted state only when current historical checks fail');
    const removed={...linkedPerson,devices:linkedPerson.devices.filter(d=>d.address!==address)};await past.st.write([{s:'kv',k:'person',v:removed}]);past.e.me=removed;
    let stale=false;try{await past.e.groupParticipationHistoryCheck(conv,original,{address:linkedAddress,fingerprint:await wire.fingerprint(linkedPub)},[]);}catch{stale=true;}check(stale,'removed own reader cannot use historical witness');
   }finally{await past.close();}
  }
  // The addressed assistant remains current while a captured sibling's
  // old consent needs its original signed group state after readmission.
  {
   const source=await world(),hv=v.historical_witness,bobKeys={sign:await crypto.subtle.importKey('pkcs8',bytes(hv.bob_private),{name:'Ed25519'},false,['sign'])};
   try{
    source.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));source.e.me=linkedPerson;await source.st.write([{s:'kv',k:'person',v:linkedPerson}]);await source.e.pinDevices(linkedPerson);await source.receive(c.proof);await source.receive(c.context);
    const bobRoster=await wire.parseRoster(v.challenge.rosters[1]),bobFP=await wire.fingerprint(bobPub),admission=await wire.groupAdmissionHash(wire.groupMember(states[0],bobRoster.person).admission),author={person:bobRoster.person,roster:await wire.rosterHash(bobRoster),address:bobPub.address,fingerprint:bobFP,group_admission:admission};
    const invite=await wire.signEvent(bobKeys,{conv,pid:wire.newID(),type:'invite',ts:1700000100,author,host:{person:bobRoster.person,address:bobPub.address,fingerprint:bobFP,agent_id:wire.newID()},audience:'room',group:{seq:0,hash:await wire.groupStateHash(states[0]),host_role:'member',host_admission:admission}}),scope=await wire.signEvent(bobKeys,await wire.scopeOf(invite,invite.ts)),accept=await wire.signEvent(bobKeys,{conv,pid:invite.pid,type:'accept',ts:invite.ts,author,prev:await wire.eventHash(invite)});
    for(const e of [invite,scope,accept])await source.receive({envelope:await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:bobPub.address,to:address,ts:e.ts,kind:'message',conv,root:wire.rootJSON(root),pid:e.pid,sub:'event',body:wire.eventJSON(e)},bobKeys,pub)});
    for(const name of ['p6-0-invite','p6-0-scope','p6-0-accept'])await source.receive(pv[name]);
    const sibling=wire.parseEvent(pv['p6-0-scope'].inner.body),siblingAccept=wire.parseEvent(pv['p6-0-accept'].inner.body),human={audience:[{pid:invite.pid,invite:await wire.eventHash(invite),decision:await wire.eventHash(accept)},{pid:sibling.pid,invite:sibling.prev,decision:await wire.eventHash(siblingAccept)}],proof:[scope,accept,sibling,siblingAccept]};
    const inner={v:2,id:wire.newID(),lid:wire.newID(),from:bobPub.address,to:address,ts:1700000101,kind:'question',conv,root:wire.rootJSON(root),pid:invite.pid,target:{address:bobPub.address,fingerprint:bobFP,agent_id:invite.host.agent_id,group_admission:admission},human,body:'Captured two assistants before sibling readmission'};
    await source.receive({envelope:await wire.seal(inner,bobKeys,pub)});const saved=await source.st.get('inbox',inner.id);check(!!saved,'captured two-assistant request is legitimately accepted before epoch change');
    const hr=hv.records.map(wire.parseGroupCommit),hs=hv.states.map(wire.parseGroupState);await source.receive(await source.make(wire.SubGroupProof,JSON.parse(wire.groupJournalJSON({records:hr,more:false})),3,hr[3].hash));await source.receive(await source.make(wire.SubGroupContext,JSON.parse(wire.groupContextJSON({root,state:hs[3]})),3,hr[3].hash));
    const item=source.e.itemOf(saved,false),device=linkedPerson.devices.find(d=>d.address===linkedAddress);let pending='';try{await source.e.groupParticipationHistoryCheck(conv,item,device,[]);}catch(e){pending=e.reason+':'+e.message;}
    check(pending==='proof_pending:Captured human consent differs from local proof.','captured sibling epoch mismatch already exposes exact pending recovery signal');
    source.e.groupSupport=async()=>{};source.e.sendGroupCopy=async()=>'';const copy=await source.e.historyCopy(device,await source.e.groupRecord(conv),item),restored=wire.parseHistory(copy.body);
    check(restored.id===item.id&&restored.group_history?.state.seq===0&&wire.humanJSON(restored.human)===wire.humanJSON(item.human),'existing automatic source fallback restores exact captured consent without a new exception');
    const bad=wire.parseHistory(copy.body);bad.human.audience.find(s=>s.pid===sibling.pid).decision='f'.repeat(64);let refused=false;try{await source.e.groupParticipationHistoryCheck(conv,bad,device,[]);}catch{refused=true;}check(refused,'witnessed captured consent mismatch remains refused');
   }finally{await source.close();}
  }
  // A current human forwarded an exact signed end before its own key was
  // removed. Later own devices keep that transport attribution, inertly.
  {
   const source=await world(),oldKeys=await wire.newKeys(),oldAddress='browser/retired',oldPub=await wire.publicEntry(oldKeys,oldAddress),oldFP=await wire.fingerprint(oldPub);
   try{
    const joined=await wire.nextRoster(keys,address,linkedRoster,[...linkedRoster.devices,oldPub],await wire.joinConsent(oldKeys,oldAddress,roster.person,2,await wire.rosterHash(linkedRoster)),roster.label,[...await wire.rosterHumans(linkedRoster),oldFP]);
    const left=await wire.nextRoster(keys,address,joined,linkedRoster.devices,null,roster.label,await wire.rosterHumans(linkedRoster));await wire.verifyNext(joined,linkedRoster);await wire.verifyNext(left,joined);
    const joinedPerson=await source.e.personRecord([roster,linkedRoster,joined],'self',null),leftPerson=await source.e.personRecord([roster,linkedRoster,joined,left],'self',null);
    source.extraChains.set(roster.person,[roster,linkedRoster,joined].map(r=>JSON.parse(wire.rosterJSON(r))));source.e.me=joinedPerson;await source.st.write([{s:'kv',k:'person',v:joinedPerson}]);await source.e.pinDevices(joinedPerson);
    await source.receive(c.proof);await source.receive(c.context);await source.receive(pv['member-invite']);await source.receive(pv['member-accept']);
    const invite=wire.parseEvent(pv['member-invite'].inner.body),accepted=wire.parseEvent(pv['member-accept'].inner.body),end=await wire.signEvent(aliceKeys,{conv,pid:invite.pid,type:'dismiss',prev:await wire.eventHash(accepted),ts:1700000210,author:invite.author});
    const inner={v:2,id:wire.newID(),lid:wire.newID(),from:oldAddress,to:address,ts:end.ts,kind:'message',conv,root:wire.rootJSON(root),pid:end.pid,sub:'event',body:wire.eventJSON(end)};
    await source.receive({envelope:await wire.seal(inner,oldKeys,pub)});const original=await source.st.get('inbox',inner.id);
    check(!!original&&original.fp===oldFP&&original.state==='','current own human originally forwards exact counted signed dismissal');
    source.extraChains.set(roster.person,[roster,linkedRoster,joined,left].map(r=>JSON.parse(wire.rosterJSON(r))));source.e.me=leftPerson;await source.st.write([{s:'kv',k:'person',v:leftPerson}]);
    source.e.groupSupport=async()=>{};source.e.sendGroupCopy=async()=>'';
    const device=leftPerson.devices.find(d=>d.address===linkedAddress),copy=await source.e.historyCopy(device,await source.e.groupRecord(conv),source.e.itemOf(original,false)),item=wire.parseHistory(copy.body);
    check(item.from===oldAddress&&item.from_key===oldFP&&item.id===inner.id&&item.lid===inner.lid&&!!item.group_history,'automatic witness preserves retired own-human end transport and original identity');
    const savedGroup=await source.st.get('kv','group/'+conv),uncached=structuredClone(savedGroup);delete uncached.rosters[await wire.rosterHash(joined)];await source.st.write([{s:'kv',k:'group/'+conv,v:uncached}]);
    const fetch=source.e.fetch;source.e.fetch=async()=>{throw Error('fixture offline');};let pending=false;
    try{await source.e.groupParticipationHistoryCheck(conv,item,device,[]);}catch(e){pending=e.reason==='proof_pending';}finally{source.e.fetch=fetch;}
    check(pending,'missing historical human roster remains pending offline');
    await source.e.groupParticipationHistoryCheck(conv,item,device,[]);
    check((await source.st.get('kv','person')).hash===leftPerson.hash&&JSON.stringify(await source.st.get('kv','group/'+conv))===JSON.stringify(uncached),'existing signed chain fills historical proof without advancing current person or group');
    await source.st.write([{s:'kv',k:'group/'+conv,v:savedGroup}]);
    const oldPin=await source.st.get('pins',oldAddress);
    for(const mode of ['pending-transport-pin','changed-transport-pin','unknown-transport','non-end','wrong-parent','bad-signature','uncounted-end','conflicting-consent','wrong-own-admission']){
     const bad=wire.parseHistory(copy.body);let event=end;
     if(mode==='pending-transport-pin')await source.st.write([{s:'pins',k:oldAddress,v:{...oldPin,pending:{fingerprint:'changed'}}}]);
     if(mode==='changed-transport-pin')await source.st.write([{s:'pins',k:oldAddress,v:{...oldPin,fingerprint:await wire.fingerprint(alicePub)}}]);
     if(mode==='unknown-transport'){bad.from=alicePub.address;bad.from_key=oldFP;}
     if(mode==='non-end')event=accepted;
     if(mode==='wrong-parent')event=await wire.signEvent(aliceKeys,{...end,prev:'f'.repeat(64)});
     if(mode==='bad-signature'){event=wire.parseEvent(wire.eventJSON(end));event.sig[0]^=1;}
     if(mode==='uncounted-end'){const other=await wire.signEvent(aliceKeys,{...end,ts:end.ts+1});event=await wire.eventHash(other)>await wire.eventHash(end)?other:end;bad.group_history.memberships.push(event===end?other:end);}
     if(mode==='conflicting-consent')bad.group_history.memberships.push(await wire.signEvent(aliceKeys,{...accepted,type:'decline'}));
     if(mode==='wrong-own-admission')bad.group_admission='f'.repeat(64);
     bad.body=wire.eventJSON(event);bad.group_history.memberships=bad.group_history.memberships.filter(e=>e.type!=='dismiss'||mode==='uncounted-end');if(!bad.group_history.memberships.some(e=>wire.eventJSON(e)===bad.body))bad.group_history.memberships.push(event);
     let refused=false;try{await source.e.groupParticipationHistoryCheck(conv,bad,device,[]);}catch{refused=true;}check(refused,'retired own transport refuses '+mode);
     await source.st.write([{s:'pins',k:oldAddress,v:oldPin}]);
    }
    const agentJoined=await wire.nextRoster(keys,address,linkedRoster,[...linkedRoster.devices,oldPub],joined.join,roster.label,await wire.rosterHumans(linkedRoster)),agentLeft=await wire.nextRoster(keys,address,agentJoined,linkedRoster.devices,null);
    await wire.verifyNext(agentJoined,linkedRoster);await wire.verifyNext(agentLeft,agentJoined);
    const agentChain=[roster,linkedRoster,agentJoined,agentLeft],agentPerson=await source.e.personRecord(agentChain,'self',null);
    source.extraChains.set(roster.person,agentChain.map(r=>JSON.parse(wire.rosterJSON(r))));source.e.me=agentPerson;await source.st.write([{s:'kv',k:'person',v:agentPerson}]);
    let agentDenied=false;try{await source.e.groupParticipationHistoryCheck(conv,item,device,[]);}catch{agentDenied=true;}check(agentDenied,'historically enrolled agent-host transport never gains human history provenance');
    source.extraChains.set(roster.person,[roster,linkedRoster,joined,left].map(r=>JSON.parse(wire.rosterJSON(r))));source.e.me=leftPerson;await source.st.write([{s:'kv',k:'person',v:leftPerson}]);
    const reader=await world();try{
     reader.extraChains.set(roster.person,[roster,linkedRoster,joined,left].map(r=>JSON.parse(wire.rosterJSON(r))));reader.e.me=leftPerson;await reader.st.write([{s:'kv',k:'person',v:leftPerson}]);await reader.e.pinDevices(leftPerson);await reader.receive(c.proof);await reader.receive(c.context);
     check(!await reader.st.get('pins',oldAddress),'fresh reader has no prior transport pin');
     const carrier=await historyEnvelope(copy.body);await reader.receive({envelope:carrier});await reader.receive({envelope:await historyEnvelope(copy.body)});await reader.reload();
     const kept=await reader.st.get('inbox',inner.id),events=await reader.e.convEvents(conv),members=await reader.e.dmMembers(await reader.e.groupRecord(conv),events);
     check(kept?.history&&kept.state===''&&kept.read&&kept.from===oldAddress&&kept.fp===oldFP&&(await reader.st.all('inbox')).filter(r=>r.lid===inner.lid).length===1,'fresh own reader stores retired transport original once and inert across restart');
     check(!members.epochs.has(oldFP)&&!events.some(e=>e.e.pid===end.pid)&&!(await reader.st.all('outbox')).some(r=>r.kind==='question'||r.kind==='task'),'retired transport imports no live device, participation or executable work');
    }finally{await reader.close();}
   }finally{await source.close();}
  }
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
  for(const i of [0,1])for(const type of ['invite','scope','accept'])await w.receive(pv['p6-'+i+'-'+type]);
  for(const name of ['p6-root','p6-ask','p6-answer']) {
    const n=pv[name].inner,body=pv['history-'+name];
    check(wire.historyJSON(wire.parseHistory(body))===body,'native captured '+name+' history exact bytes');
    const env=await historyEnvelope(body);await w.receive({envelope:env});
    const stored=await w.st.get('inbox',n.id);
    check(!await w.st.get('held',wire.parseEnvelope(env).id)&&stored?.history&&stored.state===''&&wire.humanJSON(stored.human)===wire.humanJSON(wire.parseHumanTurn(n.human)),'captured '+name+' own history keeps signed audience and remains inert');
  }
{
 const ended=await world();
 try {
  ended.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
  ended.e.me=linkedPerson;
  await ended.st.write([{s:'kv',k:'person',v:linkedPerson}]);
  await ended.e.pinDevices(linkedPerson);
  await ended.receive(c.proof);await ended.receive(c.context);
  for(const i of [0,1])for(const type of ['invite','scope','accept'])await ended.receive(pv['p6-'+i+'-'+type]);
  for(const name of ['p6-root','p6-ask','p6-answer','p6-status'])await ended.receive(pv[name]);
  for(const name of ['visitor-invite','visitor-accept'])await ended.receive(pv[name]);
  await ended.receive({envelope:await historyEnvelope(pv['history-visitor-excerpt'])});
  await ended.receive(pv['visitor-dismiss']);
  const sendGroup='a'.repeat(32),groupedInner={...pv['p6-root'].inner,id:wire.newID(),root:wire.rootJSON(root),send_group:sendGroup};
  await ended.receive({envelope:await wire.seal(groupedInner,aliceKeys,pub)});
  check((await ended.st.get('inbox',pv['p6-root'].inner.id)).send_group===sendGroup,'signed optional grouping on duplicate preserves exact child without extra request');
  const pid=pv['p6-source'],scope=wire.parseEvent(pv['p6-0-scope'].inner.body),accept=wire.parseEvent(pv['p6-0-accept'].inner.body);
  const dismissal=await wire.signEvent(aliceKeys,{conv,pid,type:'dismiss',prev:await wire.eventHash(accept),ts:1700000200,author:scope.author});
  const envelope=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000200,kind:'message',conv,root:wire.rootJSON(root),pid,sub:'event',body:wire.eventJSON(dismissal)},aliceKeys,pub);
  await ended.receive({envelope});
  const record=await ended.e.groupRecord(conv),info=(await ended.e.agentConv(pid)).info;
  check(info.state==='dismissed'&&!info.held&&!!info.decision,'historical member-hosted assistant ends with exact counted acceptance');
  const source=await ended.st.get('inbox',pv['p6-root'].inner.id),item=ended.e.itemOf(source,false),dev=linkedPerson.devices.find(d=>d.address===linkedAddress);
  const original=wire.humanJSON(item.human),evidence=await ended.e.groupHumanEvidence(record,item.human,[]);
  let liveRefused=false;
  try{await ended.e.humanTurnAuthorization({...item,conv},evidence,info,item.from,item.from_key,info.host.address,info.host.fingerprint);}catch{liveRefused=true;}
  check(liveRefused,'dismissed assistant remains unavailable to live human delivery');
  ended.e.groupSupport=async()=>{};ended.e.sendGroupCopy=async(_address,_pin,group)=>group || "";
  const copy=await ended.e.historyCopy(dev,record,item);
  check(copy.required_cap===wire.CapGroup&&wire.humanJSON(wire.parseHistory(copy.body).human)===original,'dismissed assistant leaves exact captured own-member history export intact');
  await ended.e.humanTurnAuthorization({...item,conv},evidence,info,item.from,item.from_key,info.host.address,info.host.fingerprint,true);
  check(true,'historical original-member reader keeps authority independently of its dismissed assistant');
  const carriers=await ended.e.groupHistoryCarriers(record,dev,[]);
  const linkedWorld=async(withContext=true)=>{
   const target=await world();
   target.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
   target.e.keys=linkedKeys;target.e.address=linkedAddress;target.e.fp=dev.fingerprint;target.e.me=linkedPerson;
   await target.st.write([{s:'kv',k:'identity',v:{keys:linkedKeys,address:linkedAddress,fingerprint:dev.fingerprint}},{s:'kv',k:'person',v:linkedPerson}]);await target.e.pinDevices(linkedPerson);
   for(const row of carriers){const file=row.files[0];target.blobs.set(file.attachment.blob.id,file.ct);if(withContext)await target.receive({envelope:row.envelope});}
   return target;
  };
  const legacyHeld=async(target,envelope)=>{
   const env=wire.parseEnvelope(envelope),held={id:env.id,from:env.from,reason:'invalid',envelope,at:1700000300000};
   await target.st.write([{s:'held',k:env.id,v:held}]);return held;
  };
  const target=await linkedWorld();
  try {
   // v0.8.6 rejected these signed bytes as "Request does not name the
   // exact active assistant." Its held row retained ciphertext/reason only.
   const oldHeld=await legacyHeld(target,copy.envelope);
   await target.reload();await target.e.retryHeld();
   await target.receive({envelope:copy.envelope});
   const imported=await target.st.get('inbox',item.id);
   check(imported?.history&&imported.read&&imported.state===''&&wire.humanJSON(imported.human)===original&&!await target.st.get('held',copy.id),'own linked browser admits immutable historical request quietly after dismissal');
   check(await target.st.get('kv','held-group-history-recovery-v1')===true,'legacy history startup recovery completes durably');
   const reissued=await ended.e.historyCopy(dev,record,item);await target.receive({envelope:reissued.envelope});
   check((await target.st.all('inbox')).filter(r=>r.lid===item.lid&&r.claimed_key===item.from_key).length===1&&await target.st.get('kv','group-carrier/'+oldHeld.id)&&!(await target.st.all('outbox')).some(r=>!r.sub&&['question','task'].includes(r.kind)),'recovered old history and fresh signed reissue deduplicate without execution requests');
   check(imported.send_group===sendGroup,'grouped human request survives own linked history without changing request LID');
   const groupedView=(await target.e.groupThread(conv)).messages.find(m=>m.lid===item.lid);
   check(groupedView?.send_group===sendGroup&&groupedView.send_group_author===item.from_key,'grouped history DTO binds original exact author key');
   await target.reload();
   check((await target.st.get('inbox',item.id)).send_group===sendGroup,'grouped linked history remains after browser engine reload');
   const later=await ended.e.historyCopy(dev,record,item),laterHeld=await legacyHeld(target,later.envelope);
   await target.reload();let recoveryCalls=0;const recovery=target.e.heldHistoryChecks.bind(target.e);
   target.e.heldHistoryChecks=(...args)=>{recoveryCalls++;return recovery(...args);};
   await target.e.retryHeld();await target.e.retryHeld();
   check(recoveryCalls===0&&JSON.stringify(await target.st.get('held',later.id))===JSON.stringify(laterHeld),'completed upgrade never sweeps later invalid records on wake or reload');
   target.e.heldHistoryChecks=recovery;
   for(const name of ['p6-ask','p6-answer','p6-status']) {
    const old=await ended.st.get('inbox',pv[name].inner.id),retained=ended.e.itemOf(old,false);
    const historical=await ended.e.historyCopy(dev,record,retained);
    await target.receive({envelope:historical.envelope});
    check(!await target.st.get('held',historical.id)&&!!await target.st.get('inbox',retained.id),'ended asking assistant retains exact '+name+' history without live authority');
   }
   for(const name of ['visitor-invite','visitor-accept','visitor-dismiss']) {
    const eventItem=ended.e.itemOf(await ended.st.get('inbox',pv[name].inner.id),false),eventCopy=await ended.e.historyCopy(dev,record,eventItem);
    await target.receive({envelope:eventCopy.envelope});
   }
   const excerpt=ended.e.itemOf(await ended.st.get('inbox',pv['visitor-excerpt'].inner.id),false),excerptCopy=await ended.e.historyCopy(dev,record,excerpt);
   await target.receive({envelope:excerptCopy.envelope});await target.receive({envelope:excerptCopy.envelope});
   const retainedExcerpt=await target.st.get('inbox',excerpt.id);
   check(retainedExcerpt?.history&&retainedExcerpt.read&&retainedExcerpt.state===''&&retainedExcerpt.body===excerpt.body&&!await target.st.get('held',excerptCopy.id),'clean ended assistant keeps exact selected excerpt in own history without live authority');
   const visitor=await ended.e.agentConv(pv['visitor-pid']);let liveExcerpt=false;
   try{await ended.e.externalRole({...excerpt,conv,replica:true},visitor.info,visitor.members,excerpt.from,excerpt.from_key);}catch{liveExcerpt=true;}
   check(liveExcerpt,'ended assistant cannot receive a new live excerpt');
   const badExcerpt={...excerpt,body:wire.historyJSON({...wire.parseHistory(excerpt.body),lid:wire.newID()})};let grantRefused=false;
   try{await ended.e.groupParticipationHistoryCheck(conv,badExcerpt,{address:ended.e.address,fingerprint:ended.e.fp},[]);}catch{grantRefused=true;}
   check(grantRefused,'historical excerpt still requires the exact signed selection');
   const forwarded={...item,send_group:undefined,id:wire.newID(),lid:wire.newID(),ts:1700000200,from:address,from_key:ended.e.fp,kind:'message',sub:'event',pid,body:wire.eventJSON(dismissal),human:undefined,target:null,agent_id:'',reply_to:'',status:''};
   const forward=async h=>wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:address,to:linkedAddress,ts:1700000200,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:wire.historyJSON(h)},keys,linkedPub);
   const forwardedEnvelope=await forward(forwarded);await target.receive({envelope:forwardedEnvelope});
   check(!await target.st.get('held',wire.parseEnvelope(forwardedEnvelope).id)&&!!await target.st.get('inbox',forwarded.id),'own history retains a member-forwarded dismissal under the original signed author');
   const tampered={...forwarded,id:wire.newID(),lid:wire.newID(),body:wire.eventJSON({...dismissal,ts:dismissal.ts+1})},tamperedEnvelope=await forward(tampered);await target.receive({envelope:tamperedEnvelope});
   check(!!await target.st.get('held',wire.parseEnvelope(tamperedEnvelope).id)&&!await target.st.get('inbox',tampered.id),'forwarded historical dismissal cannot bypass the original signature');
   for(const bad of [{...info,held:1},{...info,state:'conflict'},{...info,host:{...info.host,fingerprint:'f'.repeat(64)}}]){
    let refused=false;try{await ended.e.humanTurnAuthorization({...item,conv},evidence,bad,item.from,item.from_key,info.host.address,info.host.fingerprint,true);}catch{refused=true;}
    check(refused,'historical reader never bypasses held/conflict/exact target evidence');
   }
  } finally {await target.close();}
  // Arrival order after an upgrade cannot consume the one-time recovery
  // while history still needs its current signed group context.
  const pending=await linkedWorld(false);
  try {
   await legacyHeld(pending,copy.envelope);await pending.reload();await pending.e.retryHeld();
   check((await pending.st.get('held',copy.id))?.reason==='proof_pending'&&!await pending.st.get('inbox',item.id),'legacy history without current context joins existing proof retry');
   for(const row of carriers)await pending.receive({envelope:row.envelope});
   check((await pending.st.get('inbox',item.id))?.history&&!await pending.st.get('held',copy.id),'fresh signed carriers recover same persisted historical request');
  } finally {await pending.close();}
  const changedWhilePending=await linkedWorld(false);
  try {
   await legacyHeld(changedWhilePending,copy.envelope);await changedWhilePending.reload();await changedWhilePending.e.retryHeld();
   check((await changedWhilePending.st.get('held',copy.id))?.history_recovery,'missing-proof recovery retains its current-human restriction');
   const changed={...linkedPerson,human_keys:linkedPerson.human_keys.filter(fp=>fp!==ended.e.fp)};
   await changedWhilePending.st.write([{s:'kv',k:'person',v:changed}]);await changedWhilePending.reload();
   for(const row of carriers)await changedWhilePending.receive({envelope:row.envelope});
   check(!await changedWhilePending.st.get('inbox',item.id)&&!!await changedWhilePending.st.get('held',copy.id),'later proof cannot recover history after source human authority changes');
  } finally {await changedWhilePending.close();}
  for(const mode of ['changed-key','foreign','agent-host','reader-agent','frozen','live-request','bad-signature','conflicting-content','wrong-admission','held-proof','conflicting-proof','sender-race']) {
   const refused=await linkedWorld();
   try {
    let envelope=copy.envelope;
    if(mode==='foreign')envelope=await wire.seal({...await wire.open(copy.envelope,linkedKeys,linkedAddress,pub),id:wire.newID(),from:alicePub.address},aliceKeys,linkedPub);
    if(mode==='live-request')envelope=await wire.seal({...pv['p6-root'].inner,id:wire.newID(),lid:wire.newID(),root:wire.rootJSON(root),from:address,to:linkedAddress},keys,linkedPub);
    if(mode==='bad-signature'){const env=JSON.parse(envelope);env.sig=wire.b64(new Uint8Array(64));envelope=JSON.stringify(env);}
    if(mode==='conflicting-content'||mode==='wrong-admission'){
     if(mode==='conflicting-content')await refused.receive({envelope:copy.envelope});
     const h={...wire.parseHistory(copy.body),...(mode==='conflicting-content'?{body:'different historical body'}:{group_admission:'e'.repeat(64)})};
     envelope=await wire.seal({...await wire.open(copy.envelope,linkedKeys,linkedAddress,pub),id:wire.newID(),lid:wire.newID(),body:wire.historyJSON(h)},keys,linkedPub);
    }
    if(mode==='changed-key'){const pin=await refused.st.get('pins',address);await refused.st.write([{s:'pins',k:address,v:{...pin,pending:{fingerprint:'f'.repeat(64)}}}]);}
    if(['agent-host','reader-agent','frozen'].includes(mode)){
     const own={...linkedPerson,...(mode==='frozen'?{state:'conflict'}:{human_keys:linkedPerson.human_keys.filter(fp=>fp!==(mode==='agent-host'?ended.e.fp:dev.fingerprint))})};
     await refused.st.write([{s:'kv',k:'person',v:own}]);
    }
    if(mode==='held-proof'){
     const person=await refused.st.get('persons',scope.author.person);await refused.st.write([{s:'persons',k:person.person,v:{...person,state:'conflict'}}]);
    }
    if(mode==='conflicting-proof'){
     const conflict=await wire.signEvent(aliceKeys,{...accept,type:'decline',ts:accept.ts+1});
     await refused.receive({envelope:await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:linkedAddress,ts:conflict.ts,kind:'message',conv,root:wire.rootJSON(root),pid,sub:'event',body:wire.eventJSON(conflict)},aliceKeys,linkedPub)});
     check(!!(await refused.e.agentConv(pid)).info.conflict,'signed conflicting proof is present before legacy recovery');
    }
    const held=await legacyHeld(refused,envelope);await refused.reload();
    const write=refused.st.write.bind(refused.st);let raced=false;
    if(mode==='sender-race')refused.st.write=async(ops,checks)=>{
     if(!raced&&ops.some(o=>o.s==='inbox'&&o.k===item.id)){
      raced=true;const other=realIDB?await openIDB(refused.name):refused.st;
      await(other===refused.st?write:other.write.bind(other))([{s:'kv',k:'person',v:{...linkedPerson,human_keys:linkedPerson.human_keys.filter(fp=>fp!==ended.e.fp)}}]);if(other!==refused.st)other.close();
     }return write(ops,checks);
    };
    await refused.e.retryHeld();refused.st.write=write;
    if(mode==='sender-race')check(raced&&!await refused.st.get('kv','held-group-history-recovery-v1'),'concurrent human role removal prevents history storage and premature migration completion');
    const kept=await refused.st.get('held',held.id),rows=(await refused.st.all('inbox')).filter(r=>r.lid===item.lid);
    check(kept?.envelope===held.envelope&&!await refused.st.get('kv','group-carrier/'+held.id)&&!await refused.st.get('receipts',held.id),'upgrade history '+mode+' stays held without success receipt');
    check(rows.length===(mode==='conflicting-content'?1:0)&&rows.every(r=>r.body===item.body),'upgrade history '+mode+' cannot replace accepted content or execute');
    const before=JSON.stringify(kept);await refused.e.retryHeld();await refused.reload();await refused.e.retryHeld();
    check(JSON.stringify(await refused.st.get('held',held.id))===before,'upgrade history '+mode+' recovery is bounded across wake and reload');
   } finally {await refused.close();}
  }
 } finally {await ended.close();}
}

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
  w.e.requireGroupHumanSupport=async()=>{};w.e.groupSupport=async()=>{}; // Existing synthetic signed transport boundary; real capability negotiation is the rendered gate.
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
  await w.reload();check((await w.e.groupThread(conv)).messages.filter(m=>m.kind==='answer').length===3,'late linked participation and captured history survive durable reload');
  // A departed original author must not prevent our own linked device from
  // restoring messages received under our still-current admission.
  await w.receive(c.withdrawn);
  const past={...JSON.parse(v.live_history_json),id:wire.newID(),lid:wire.newID(),from:bobPub.address,from_key:await wire.fingerprint(bobPub),body:'before departure',attachments:[],group_admission:await wire.groupAdmissionHash(wire.groupMember(states[0],roster.person).admission)};
  const pastEnvelope=await historyEnvelope(JSON.stringify(past));await w.receive({envelope:pastEnvelope});
  const restored=await w.st.get('inbox',past.id);
  check(restored?.history&&restored.body===past.body&&restored.state===''&&restored.read&&!restored.fp,'departed author ordinary history restored quietly under linked-device provenance');
  const badPast={...past,id:wire.newID(),lid:wire.newID(),group_admission:'e'.repeat(64)},badPastEnvelope=await historyEnvelope(JSON.stringify(badPast));await w.receive({envelope:badPastEnvelope});
  check(!await w.st.get('inbox',badPast.id)&&(await w.st.get('held',wire.parseEnvelope(badPastEnvelope).id))?.reason==='invalid','departed author history still requires exact own admission');
  const foreignPast={...past,id:wire.newID(),lid:wire.newID()},foreignPastEnvelope=await wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000101,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body:JSON.stringify(foreignPast)},aliceKeys,pub);await w.receive({envelope:foreignPastEnvelope});
  check(!await w.st.get('inbox',foreignPast.id)&&(await w.st.get('held',wire.parseEnvelope(foreignPastEnvelope).id))?.reason==='invalid','another person cannot assert our past group history');
  await w.reload();check((await w.st.get('inbox',past.id))?.history,'departed author history survives reload');await w.close();
  // Last-person deletion keeps a durable local control even without another
  // device, never reviving withdrawn recipients or posting to the relay.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  {
   const self=wire.groupMember(states[0],roster.person),stamp=await wire.groupAdmissionHash(self.admission),id=wire.newID(),lid=wire.newID();
   const original={v:2,id,lid,from:address,to:alicePub.address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),body:'my old message',fan:[{person:roster.person,roster:await wire.rosterHash(roster)}]};
   const sealed=await wire.seal(original,keys,alicePub);
   await w.st.write([{s:'outbox',k:id,v:{...original,fp:w.e.fp,at:1700000000000,state:'delivered',envelope:sealed,group_admission:stamp,recipient_fp:await wire.fingerprint(alicePub),required_cap:wire.CapGroup}}]);
   const delivered=await w.st.get('outbox',id), lagID=wire.newID(),person=v.challenge.rosters[0].person;
   await w.st.write([{s:'outbox',k:lagID,v:{...delivered,id:lagID,to:'alice/legacy',person,state:'waiting'}}]);
   check((await w.e.overview()).dms.find(d=>d.id===conv)?.waiting===0,'group sidebar does not call a delivered person unsent because another device waits');
   check((await w.e.convMessages(conv,await w.st.all('inbox'),await w.st.all('outbox'))).find(m=>m.lid===lid)?.copies.some(x=>x.state==='waiting'),'group delayed device copy is retained');
   await w.st.write([{s:'outbox',k:id,v:{...delivered,state:'waiting'}}]);
   check((await w.e.overview()).dms.find(d=>d.id===conv)?.waiting===1,'group genuinely unreached recipient stays counted once');
   await w.st.write([{s:'outbox',k:id,v:delivered},{s:'outbox',k:lagID}]);
   await w.receive(c['solo-proof']);await w.receive(c['solo-context']);
   check((await w.e.dmMembers(await w.e.groupRecord(conv))).size===1,'signed admin transition leaves only our person');
   w.e.post=async()=>{throw Error('local control must not contact relay');};
   await w.e.messageControl('edit',{conv,id,dir:'out',text:'edited alone'});
   check((await w.e.groupThread(conv)).messages.find(m=>m.id===id)?.text==='edited alone','last browser person can edit');
   check((await w.e.overview()).dms.find(d=>d.id===conv)?.last==='edited alone','sidebar uses latest revision');
   await w.e.messageControl('delete',{conv,id,dir:'out'});
   check((await w.st.get('outbox',id)).body===''&&(await w.e.groupThread(conv)).messages.find(m=>m.id===id)?.deleted,'last browser person can delete and erase text');
   await w.reload();check((await w.e.groupThread(conv)).messages.find(m=>m.id===id)?.deleted,'last-person browser deletion survives reload');
   check((await w.e.overview()).dms.find(d=>d.id===conv)?.last==='Message deleted','sidebar shows deletion after reload');
  }
  await w.close();
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
  const selectedPID=selectedInvite.pid;
  check(selectedInvite.grant.length===1&&selectedInvite.grant[0].lid===selectedSource.lid&&selectedInvite.grant[0].fingerprint===selectedItem.from_key,'production invite retains native own-linked selected logical ID and original author');
  const selectedInfo={...(await w.e.agentConv(outside['own-pid'])).info,grant:selectedInvite.grant};
  check(w.e.agentView(selectedInfo,[selectedSource],null,[w.e.me],"member").shared[0]===selectedSource.id,'group shared context projection retains verified own-linked original');
  // Tasks without asking (Comic's per-person choice, MEL-528): a member key is named with exactly the admission a receiver checks it against.
  const taskKey=selectedItem.from_key;await w.e.inviteAgent({conv:outsideConv,host:alicePub.address,tasks_from:[taskKey]});
  check(selectedInvite.type==='share'&&selectedInvite.pid===selectedPID&&!selectedInvite.task_keys&&!selectedInvite.group.task_admissions,'sharing an existing membership never widens its accepted task permission');
  const existingAgents=w.e.agentsOf.bind(w.e);w.e.agentsOf=async()=>[]; // fresh invitation vector
  await w.e.inviteAgent({conv:outsideConv,host:alicePub.address,tasks_from:[taskKey]});
  check(JSON.stringify(selectedInvite.task_keys)===JSON.stringify([taskKey])&&selectedInvite.group.task_admissions?.length===1&&!!selectedInvite.group.task_admissions[0]&&selectedInvite.group.task_admissions[0]===selectedMembers.epochs.get(taskKey),'group invite with tasks from a member names its key with that exact admission');
  let strangerRefused=false;try{await w.e.inviteAgent({conv:outsideConv,host:alicePub.address,tasks_from:['f'.repeat(32)]});}catch(e){strangerRefused=/not the key of a member/.test(e.message);}check(strangerRefused,'group invite refuses tasks from a key that is not a member');
  w.e.agentsOf=existingAgents;
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

  const newTarget=await wire.parseRoster(v.challenge.rosters[1]);
  const [firstLive,secondLive]=await Promise.all([w.e.inviteGroup({conv:created,person:newTarget.person}),w.e.inviteGroup({conv:created,person:newTarget.person})]);
  const controlCopies=(await w.st.all('outbox')).filter(x=>x.group_lifecycle===firstLive.id&&x.sub===wire.SubGroupInvite);
  check(controlCopies.length>0&&controlCopies.every(x=>x.required_cap===wire.CapGroupInvitationControl),'nonce proposal requires explicit gic1 at final handoff');
  const oldSession=wire.newID(),newSession=wire.newID();
  const oldCaps=JSON.parse(wire.capsJSON(await wire.newCaps(keys,address,oldSession,[wire.CapEnv2,wire.CapRoom]))),newCaps=JSON.parse(wire.capsJSON(await wire.newCaps(keys,address,newSession,[wire.CapEnv2,wire.CapRoom,wire.CapGroupInvitationControl])));
  let controlProfile={live:true,sessions:[oldSession,newSession],caps:[oldCaps,newCaps]};
  const controlGate={pubOf:async()=>pub,profile:async()=>controlProfile};
  let oldBlocked=false;try{await Engine.prototype.requireGroupInvitationControl.call(controlGate,address,{});}catch(e){oldBlocked=e.message.includes('gic1');}
  check(oldBlocked&&!wire.RoomImplies.includes(wire.CapGroupInvitationControl),'mixed signed grp1/rm1 sessions cannot receive nonce or cancellation; gic1 not implied');
  const sealedBefore=JSON.stringify(controlCopies[0]);
  const finalControlGate={pinned:async()=>({fingerprint:controlCopies[0].recipient_fp}),requireGroupInvitationControl:(a,p)=>Engine.prototype.requireGroupInvitationControl.call(controlGate,address,p)};
  const finalControl=await Engine.prototype.groupLifecycleGate.call(finalControlGate,controlCopies[0]);
  check(finalControl.why.includes('gic1')&&JSON.stringify(controlCopies[0])===sealedBefore,'after-queue old session blocks final lifecycle handoff without changing sealed copy');

  controlProfile={live:true,sessions:[newSession],caps:[newCaps]};await Engine.prototype.requireGroupInvitationControl.call(controlGate,address,{});
  check(true,'explicit signed gic1 current session permits invitation controls');
  check(firstLive.id===secondLive.id,'concurrent repeated browser Invite keeps one exact live proposal');
  await w.e.cancelGroup({id:firstLive.id});await w.e.cancelGroup({id:firstLive.id});
  check((await w.st.get('kv','group-invitation/out/'+firstLive.id)).status==='cancelled','browser cancel is durable and idempotent');
  const refreshed=await w.e.refreshGroup({id:firstLive.id}),again=await w.e.refreshGroup({id:firstLive.id});
  check(refreshed.id!==firstLive.id&&refreshed.id===again.id,'browser explicit Refresh maps one exact new successor across retries');
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
  // Own original files are bound to the signed original, even after the
  // timeline presents a later revision of its caption.
  w=await world();await w.receive(c.proof);await w.receive(c.context);
  {
   const bytes=new Uint8Array([3,1,4,1,5]),file=await wire.encryptFile(bytes,'original.bin',pub),author=await wire.fingerprint(alicePub);
   w.blobs.set(file.attachment.blob.id,file.ct);
   const original={v:2,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),body:'original caption',origin:'ui',attachments:[file.attachment]};
   await w.receive({envelope:await wire.seal(original,aliceKeys,pub)});
   const edited={v:3,id:wire.newID(),lid:wire.newID(),from:alicePub.address,to:address,ts:1700000001,kind:'message',conv,sub:wire.SubRevision,ref:{id:original.lid,fingerprint:author},body:JSON.stringify({rev:1,text:'edited caption'})};
   await w.receive({envelope:await wire.seal(edited,aliceKeys,pub)});
   check((await w.e.groupThread(conv)).messages.find(m=>m.id===original.id)?.text==='edited caption','signed ordinary file revision is visible before original-file recovery');
   const row=await w.st.get('inbox',original.id),evidence=await w.e.groupTurnEvidence(conv),message={v:1,type:'request',lid:row.lid,author,hash:await wire.groupHistoryContentHash(conv,row),index:0,name:file.attachment.name,size:bytes.length,sha256:file.attachment.sha256,group_admission:row.group_admission};
   await w.e.groupFileAuthorized(evidence.packet,evidence.members,address,w.e.fp,message);
   let offered;w.e.offerGroupFile=async(job,files)=>{offered=files[0];};await w.e.serveGroupFile({conv,device:address,fp:w.e.fp,message});
   check(offered&&wire.hex(await wire.decryptFile(offered.ct,offered.attachment,keys))===wire.hex(bytes),'current own exact admission serves immutable original file after signed caption revision');
   for(const change of [{hash:'f'.repeat(64)},{group_admission:''},{name:'other.bin'}]) {
    let refused=false;try{await w.e.groupFileAuthorized(evidence.packet,evidence.members,address,w.e.fp,{...message,...change});}catch{refused=true;}check(refused,'original file still refuses changed '+Object.keys(change)[0]);
   }
   let foreign=false;try{await w.e.groupFileAuthorized(evidence.packet,evidence.members,alicePub.address,author,{...message,group_admission:await wire.groupAdmissionHash(wire.groupMember(evidence.packet.state,v.challenge.rosters[0].person).admission)});}catch{foreign=true;}check(foreign,'foreign current member gains no original file without selected history grant');
  }
  await w.close();
  for(const declineMode of ['fresh','stale','roster','missing-key','frozen','removed']) {
   const declineWorld=await world();
   try {
    await declineWorld.receive(c.proof);
    const incoming=await declineWorld.make(wire.SubGroupInvite,JSON.parse(wire.groupInvitationJSON(invitation)),invitation.state.seq,await wire.groupStateHash(invitation.state));await declineWorld.receive(incoming);
    const k='group-invitation/in/'+v.invitation_id,row=await declineWorld.st.get('kv',k),originalMe=await declineWorld.st.get('kv','person');
    if(declineMode==='stale')await declineWorld.st.write([{s:'kv',k,v:{...row,status:'stale'}}]);
    if(['roster','frozen','removed'].includes(declineMode))await declineWorld.st.write([{s:'kv',k:'person',v:{...originalMe,...(declineMode==='roster'?{hash:'e'.repeat(64)}:declineMode==='frozen'?{state:'conflict'}:{devices:[]})}}]);
    if(declineMode==='missing-key')await declineWorld.st.write([{s:'pins',k:row.inviter}]);
    declineWorld.e.post=async()=>{};
    declineWorld.e.verifyGroupProposal=async()=>{throw Error('stale head or offline: never needed for decline');};
    let accepted=false;try{await declineWorld.e.decideGroup({id:v.invitation_id,accept:true});accepted=true;}catch{}
    check(!accepted,'stale acceptance remains refused '+declineMode);
    if(['frozen','removed'].includes(declineMode)) {
     let refused=false;try{await declineWorld.e.decideGroup({id:v.invitation_id,accept:false});}catch{refused=true;}
     check(refused&&(await declineWorld.st.all('outbox')).length===0,'inactive own identity cannot sign decline '+declineMode);
    } else {
     await declineWorld.e.decideGroup({id:v.invitation_id,accept:false});await declineWorld.e.decideGroup({id:v.invitation_id,accept:false});
     const outgoing=await declineWorld.st.all('outbox');
     check((await declineWorld.st.get('kv',k)).status==='declined'&&outgoing.length===(declineMode==='missing-key'?0:1),'one exact offline decline or honest local-only hide '+declineMode);
     if(outgoing.length){const gate=await declineWorld.e.groupLifecycleGate(outgoing[0]);check(!gate.why,'decline delivery retains exact signer and inviter key without fresh proposal '+declineMode);}
     await declineWorld.reload();check((await declineWorld.st.get('kv',k)).status==='declined','decline survives restart '+declineMode);
    }
    check(!await declineWorld.e.groupRecord(conv),'decline never creates membership '+declineMode);
   }finally{await declineWorld.close();}
  }
  w=await world();await w.receive(c.proof);
  const invite=await w.make(wire.SubGroupInvite,JSON.parse(wire.groupInvitationJSON(invitation)),invitation.state.seq,await wire.groupStateHash(invitation.state));
  await w.receive(invite);
  check((await w.e.groupInvitations()).length===1,'verified invitation retained separately');
  check(!(await w.e.groupRecord(conv)),'invitation does not install room access before consent');
  check((await w.st.all('inbox')).length===0&&(await w.st.all('outbox')).length===0,'invitation stays quiet until explicit choice');
  await w.reload();check((await w.e.groupInvitations())[0].status==='pending','invitation survives store reload');
  let unknown=false;try{await w.e.decideGroup({id:wire.newID(),accept:true});}catch(_){unknown=true;}check(unknown&&(await w.st.all('outbox')).length===0,'unknown invitation cannot generate consent');
  const withNonce={...invitation,nonce:wire.newID()},nonceID=await wire.groupInvitationID(withNonce);
  check(nonceID!==v.invitation_id&&wire.parseGroupInvitation(wire.groupInvitationJSON(withNonce)).nonce===withNonce.nonce,'fresh nonce changes proposal identity while preserving exact admission binding');
  const wrongCancel=await w.make(wire.SubGroupConsent,{v:1,invitation:v.invitation_id,decision:'cancelled'},invitation.seq,'e'.repeat(64));
  await w.receive(wrongCancel);check((await w.st.get('kv','group-invitation/in/'+v.invitation_id)).status==='pending','signed cancellation with wrong exact transition cannot cancel proposal');
  const cancel=await w.make(wire.SubGroupConsent,{v:1,invitation:v.invitation_id,decision:'cancelled'},invitation.seq,invitation.prev);
  await w.receive(cancel);await w.reload();check((await w.st.get('kv','group-invitation/in/'+v.invitation_id)).status==='cancelled','signed exact cancellation durable across reload');
  let cancelledJoin=false;try{await w.e.decideGroup({id:v.invitation_id,accept:true});}catch(e){cancelledJoin=/cancelled/.test(e.message);}check(cancelledJoin&&(await w.st.all('outbox')).length===0,'cancelled invitation cannot emit consent or gain membership');
  const earlyCancel=await w.make(wire.SubGroupConsent,{v:1,invitation:nonceID,decision:'cancelled'},withNonce.seq,withNonce.prev);
  await w.receive(earlyCancel);await w.reload();
  const delayedInvite=await w.make(wire.SubGroupInvite,JSON.parse(wire.groupInvitationJSON(withNonce)),withNonce.state.seq,await wire.groupStateHash(withNonce.state));await w.receive(delayedInvite);
  check((await w.st.get('kv','group-invitation/in/'+nonceID)).status==='cancelled','cancellation before proposal prevents delayed proposal revival');
  const nextProposal={...invitation,nonce:wire.newID()},nextID=await wire.groupInvitationID(nextProposal),nextInvite=await w.make(wire.SubGroupInvite,JSON.parse(wire.groupInvitationJSON(nextProposal)),nextProposal.state.seq,await wire.groupStateHash(nextProposal.state));await w.receive(nextInvite);
  check((await w.st.get('kv','group-invitation/in/'+nextID)).status==='pending'&&!await w.e.groupRecord(conv),'new nonce at unchanged head remains explicit pending invitation with no membership');
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
  const next=await wire.nextRoster(keys,address,roster,[nextPub],join,roster.label,[await wire.fingerprint(nextPub)]);await wire.verifyNext(next,roster);
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
    const fresh=await wire.newKeys(),np=await wire.publicEntry(fresh,'browser/linked');const join=await wire.joinConsent(fresh,np.address,roster.person,1,await wire.rosterHash(roster));const next=await wire.nextRoster(keys,address,roster,[np],join,roster.label,[await wire.fingerprint(np)]);await wire.verifyNext(next,roster);
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
if(globalThis.process?.versions?.node){const {createInterface}=await import('node:readline');for await(const line of createInterface({input:process.stdin})){let out;try{const r=JSON.parse(line);out=r.op==='setup'?await setup():r.op==='consent'?{consent:await consent(r.challenge)}:await checks(r.vectors,false,r.op==='warm-regression',r.op==='control-history',r.op==='background-regression',r.op==='receiver-regression',r.op==='receipt-repair',r.op==='topic-participation');}catch(e){out={error:e.stack};}process.stdout.write(JSON.stringify(out)+'\n');}}
