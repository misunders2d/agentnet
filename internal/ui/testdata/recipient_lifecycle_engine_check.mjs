// Permanent offboarding is exact-key evidence, never inferred from absence.
import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
const {Engine,memoryStore,outText,HubError} = await import(process.env.AGENTNET_TEST_ENGINE_MODULE || '../static/engine.mjs');
let checks=0;
const check=(value,why)=>{assert.ok(value,why);checks++;};
async function fixture(linked=false) {
  const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid'});
  e.keys=await wire.newKeys();e.address='alice/desk';e.pub=await wire.publicEntry(e.keys,e.address);e.fp=await wire.fingerprint(e.pub);
  const roster=await wire.newRoster(e.keys,e.address,'Alice');e.me=await e.personRecord([roster],'self',null);
  const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,'bob/phone'),fp=await wire.fingerprint(pub);
  if(linked) {
    const join=await wire.joinConsent(keys,pub.address,e.me.person,e.me.seq+1,e.me.hash);
    const next=await wire.nextRoster(e.keys,e.address,roster,[e.pub,pub],join,roster.label,[e.fp,fp]);
    await wire.verifyNext(next,roster);e.me=await e.personRecord([roster,next],'self',e.me);
  }
  await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me},{s:'pins',k:pub.address,v:{address:pub.address,fingerprint:fp,json:wire.marshalPublic(pub)}}]);
  const row=(id,state='queued',extra={})=>({id,to:pub.address,recipient_fp:fp,state,envelope:'sealed-'+id,body:'original-'+id,...extra});
  return {e,keys,pub,fp,row};
}
{
  const {e,keys,pub,fp,row}=await fixture();
  const source=row('original','delivered',{to:'carol/desk'}),done=row('done','delivered'),custody=row('custody','custody');
  const rows=[source,done,custody,row('q'),row('w','waiting'),row('rw','receiver_waiting'),row('staged','archive_staged',{archive_chunk:'chunk',sub:wire.SubGroupProof}),row('other-key','queued',{recipient_fp:'f'.repeat(64)})];
  const job={id:'chunk',to:pub.address,recipient_fp:fp,children:['staged'],state:'prepared',ct:'ciphertext'};
  await e.store.write([...rows.map(r=>({s:'outbox',k:r.id,v:r})),{s:'kv',k:'history-archive/out/chunk',v:job},{s:'kv',k:'history',v:{[pub.address]:{device:pub.address,fingerprint:fp,state:'running',pos:'cursor'}}},{s:'kv',k:'history-archive/in/incoming',v:{state:'saved',cursor:1}}]);
  let lookups=0,revoked=false,mismatch=false;
  const rotatedPub=await wire.publicEntry(await wire.newKeys(),pub.address);
  // A real public key delivered through the ordinary signed transport path.
  e.fetch=async(url,options)=>{check(!!options.headers['X-Agentnet-Sig'],'directory uses signed request');lookups++;return new Response(JSON.stringify({public:JSON.parse(wire.marshalPublic(mismatch?rotatedPub:pub)),revoked}),{status:200});};
  e.members={listed:'listed',current:true,truncated:false,list:[{address:pub.address,presence:'suspended'}]};
  await e.reconcileRevokedRecipients();check(lookups===0,'listed suspended recipient is untouched');
  e.members={...e.members,truncated:true,list:[]};await e.reconcileRevokedRecipients();check(lookups>0&&(await e.store.get('outbox','q')).state==='queued','truncated absence permits lookup but never proves revocation');
  e.members={...e.members,truncated:false};await e.reconcileRevokedRecipients();check((await e.store.get('outbox','q')).state==='queued','unenrolled/absent nonrevoked recipient keeps exact copy');
  revoked=true;mismatch=true;e.members.discovery_generation=1;await e.reconcileRevokedRecipients();check((await e.store.get('outbox','q')).state==='queued','different directory key cannot revoke old recipient');
  mismatch=false;e.members.discovery_generation=2;await e.reconcileRevokedRecipients();
  const looked=lookups;let writes=0;const originalWrite=e.store.write.bind(e.store);
  e.store.write=async(...args)=>{writes++;return originalWrite(...args);};
  await e.reconcileRevokedRecipients();
  check(lookups===looked&&writes===0,'cached revoked key is not looked up or rewritten; unchanged generation retains unproven key memo');
  e.store.write=originalWrite;
  for(const id of ['q','w','rw','staged']) { const r=await e.store.get('outbox',id);check(r.state==='not_delivered'&&r.envelope==='sealed-'+id&&r.body==='original-'+id,'only unsent transport ends, immutable original bytes kept: '+id); }
  for(const r of [source,done,custody,rows.at(-1)])check(JSON.stringify(await e.store.get('outbox',r.id))===JSON.stringify({...r,transfer_lid:['',r.id]}),'delivered/custody/other target untouched: '+r.id);
  check((await e.store.get('kv','history-archive/out/chunk')).state==='ended','outgoing prepared archive stops');
  check((await e.store.get('kv','history'))[pub.address].pos==='cursor','cursor retained');
  check((await e.store.get('kv','history-archive/in/incoming')).state==='saved','import job untouched');
  const stale=row('q');await e.receiverOutboxProgress(stale);check((await e.store.get('outbox','q')).state==='not_delivered','late send progress cannot resurrect ended transport');
  await e.store.write([{s:'outbox',k:'restart',v:row('restart')}]);
  const restarted=new Engine({store:e.store,base:e.base});await restarted.load();check((await e.store.get('outbox','restart')).state==='not_delivered','durable exact revocation reconciles on restart without network');
  const n={v:1,id:'1'.repeat(32),from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'new live turn'};
  const raw=await wire.seal(n,keys,e.pub);await e.admit(raw,wire.parseEnvelope(raw));
  check(!await e.store.get('inbox',n.id)&&!!await e.store.get('held',n.id),'pinned revoked sender cannot admit new signed live record');
  check(outText('expired','Bob')==='Not delivered','expired receipt does not invent a session-ended reason');
}
{
  const {e,pub,fp,row}=await fixture(true);
  const linked=await wire.parseRoster(e.me.json),removed=await wire.nextRoster(e.keys,e.address,linked,[e.pub],null);
  await wire.verifyNext(removed,linked);e.me=await e.personRecord([linked,removed],'self',e.me);await e.store.write([{s:'kv',k:'person',v:e.me}]);
  await e.store.write([{s:'kv',k:'history',v:{[pub.address]:{device:pub.address,fingerprint:fp,state:'running',pos:17}}},...['history',wire.SubDeviceHistory,wire.SubRootSync].map((sub,i)=>({s:'outbox',k:'sync'+i,v:row('sync'+i,'queued',{sub,fresh_live:true})})),{s:'outbox',k:'ordinary',v:row('ordinary')},{s:'outbox',k:'custody',v:row('custody','custody',{sub:'history'})}]);
  await e.reconcileDepartedRecipients();
  for(let i=0;i<3;i++)check((await e.store.get('outbox','sync'+i)).state==='not_delivered','departed own device loses replication including live mirror '+i);
  check((await e.store.get('outbox','ordinary')).state==='queued','detached standalone device still receives ordinary messages');
  check((await e.store.get('outbox','custody')).state==='custody','roster departure does not invent relay receipt');
  check((await e.store.get('kv','history'))[pub.address].pos===17,'departure preserves resumable source cursor');
}
{
  const {e,pub,fp,row}=await fixture();await e.store.write([{s:'outbox',k:'racy',v:row('racy')}]);
  const write=e.store.write.bind(e.store);let inject=true;
  e.store.write=async(ops,checks)=>{if(inject&&ops.some(o=>o.s==='outbox'&&o.k==='racy')){inject=false;await write([{s:'outbox',k:'racy',v:row('racy','delivered')}]);}return write(ops,checks);};
  await assert.rejects(e.endRecipient(pub.address,fp,true),/storage changed/);
  check((await e.store.get('outbox','racy')).state==='delivered','CAS preserves concurrent proven delivery');
  check(!await e.store.get('kv','revoked-device/'+pub.address+'/'+fp),'CAS failure rolls back exact evidence with related changes');
}
{
  const {e,pub,fp,row}=await fixture(true);
  const dev={address:pub.address,fingerprint:fp};
  await e.endRecipient(pub.address,fp,true);
  await e.store.write([{s:'kv',k:'read-mark/test',v:{owner:e.me.person,ref:{conv:'',fingerprint:e.fp,lid:'2'.repeat(32)}}}]);
  e.fetch=async()=>{throw Error('revoked recipient producer must not perform network work');};
  await e.syncReadPages();await e.directHistory().step(dev);await e.reconcileHistory();
  check((await e.store.all('outbox')).length===0,'revoked key still on signed roster cannot regenerate rootless sync');
  check(!await e.store.get('kv','device-history/job/'+fp),'revoked key cannot advance direct-history source cursor');
  check(!(await e.historyBook())[pub.address],'revoked key cannot restart history job');
  const descriptor=row('accepted-descriptor','delivered',{sub:wire.SubHistoryArchive}),child=row('retained-child','archive_staged',{sub:'history',archive_chunk:descriptor.id});
  await e.store.write([{s:'outbox',k:descriptor.id,v:descriptor},{s:'outbox',k:child.id,v:child},{s:'kv',k:'history-archive/out/'+descriptor.id,v:{id:descriptor.id,to:pub.address,recipient_fp:fp,children:[child.id],state:'published'}}]);
  await e.endRecipient(pub.address,fp,true);
  check((await e.store.get('outbox',child.id)).state==='archive_staged','delivered descriptor child remains for idempotent acceptance reconciliation');
  check((await e.store.get('kv','history-archive/out/'+descriptor.id)).state==='published','delivered archive job is not ended during receipt recovery');
}
{
  const {e,pub,fp,row}=await fixture(true),id='3'.repeat(32),childID='4'.repeat(32),expiredID='5'.repeat(32);
  const job={id,to:pub.address,recipient_fp:fp,children:[childID],state:'prepared',ct:'private ciphertext'};
  await e.store.write([{s:'kv',k:'history-archive/out/'+id,v:job}]);
  const prefix=e.store.prefix.bind(e.store);let outgoingReads=0,looked=0,uploaded=0;
  e.store.prefix=async(s,p)=>{
    const rows=await prefix(s,p);
    if(p==='history-archive/out/'&&++outgoingReads===2){await e.endRecipient(pub.address,fp,true);return rows;}
    return rows;
  };
  e.ctlSupport=async()=>{looked++;return [true,''];};e.uploadBlob=async()=>{uploaded++;};e.connected=true;
  await e.archives().wake();e.connected=false;e.store.prefix=prefix;
  check(looked===0&&uploaded===0,'stale prepared archive snapshot makes no capability/upload request after exact revocation');
  const ended=await e.store.get('kv','history-archive/out/'+id);
  check(ended.state==='ended'&&ended.error==='That device was removed from the server.','upload catch never rewrites terminal archive job');
  await assert.rejects(e.archives().prepare([row('new','queued',{sub:'history'})],[],[],{address:pub.address,fingerprint:fp}),/storage changed/);
  check(looked===0,'revoked prepare never falls back to queued legacy carriers');
  await e.store.write([{s:'outbox',k:id,v:row(id,'not_delivered',{sub:wire.SubHistoryArchive,detail:'That device was removed from the server.'})},{s:'outbox',k:childID,v:row(childID,'not_delivered',{sub:'history',archive_chunk:id})},{s:'outbox',k:expiredID,v:row(expiredID,'not_delivered',{detail:'That device was removed from the server.'})},{s:'kv',k:'history-archive/out/'+id,v:{...ended,children:[childID],state:'ended'}}]);
  e.runHistory=async()=>{};e.syncRoots=async()=>{};e.syncReadMarks=async()=>{};e.syncTopicTitles=async()=>{};e.syncInvitations=async()=>{};
  await e.dispatch('receipt',JSON.stringify({id:expiredID,state:'expired',seq:1}));
  check((await e.store.get('outbox',expiredID)).state==='expired','actual relay expiry overrides local not-delivered after in-flight race');
  await e.dispatch('receipt',JSON.stringify({id,state:'delivered',seq:2}));await e.archiveReceiptRun;
  check((await e.store.get('outbox',id)).state==='delivered','actual relay delivered proof wins over local cleanup');
  check((await e.store.get('outbox',childID)).state==='archive_accepted','delivered raced descriptor reconciles its exact retained child');
  check((await e.store.get('kv','history-archive/out/'+id)).state==='accepted','actual receipt settles previously ended outbound archive job');
}
console.log('PASS recipient lifecycle: '+checks+' checks');

{
  const {e,row}=await fixture();
  const ops=Array.from({length:121},(_,i)=>({s:'outbox',k:'bounded'+i,v:row('bounded'+i,'queued',{to:'peer'+i+'/desk',recipient_fp:'key'+i})}));
  ops.push({s:'pins',k:'historical/desk',v:{address:'historical/desk',fingerprint:'old'}});
  ops.push({s:'inbox',k:'request',v:{id:'request',from:'request/desk',fp:'request-key',kind:'question',state:'held'}});
  await e.store.write(ops);
  e.members={current:true,listed:'listed',list:[],truncated:true,discovery_generation:5};
  let calls=0;
  e.directory=async address=>{calls++;if(calls===1){e.members={...e.members,list:[{address:'peer120/desk',presence:'offline'}]};e.reconcileRevokedRecipients();}return {revoked:false,fingerprint:'unrelated'};};
  await e.reconcileRevokedRecipients();
  check(calls===242,'bounded rounds finish then coalesce changed membership; newly present tail skipped');
  const done=calls;
  e.members={...e.members,list:[{address:'peer120/desk',presence:'connected'}]};
  await e.reconcileRevokedRecipients();check(calls===done,'presence-only event performs no directory reprobes');
  await e.store.write([{s:'outbox',k:'new',v:row('new','queued',{to:'new/desk'})}]);
  await e.reconcileRevokedRecipients();check(calls===done+1,'new open exact-key target checked within unchanged generation');
  check(!await e.store.get('kv','revoked-device/historical/desk/old'),'historical pin alone is not a discovery target');
  e.members={...e.members,discovery_generation:6};await e.reconcileRevokedRecipients();
  check(calls===done+123,'explicit directory generation refreshes all open targets');
}
console.log('Recipient discovery focused checks complete:',checks);

{
  const {e,pub,fp,row}=await fixture();
  await e.store.write([{s:'outbox',k:'transient',v:row('transient')}]);
  e.members={current:true,listed:'listed',list:[],discovery_generation:7};
  let calls=0;
  e.directory=async()=>{calls++;if(calls===1)throw new HubError(503,'','temporary failure');return {revoked:true,fingerprint:fp};};
  await e.reconcileRevokedRecipients();
  check(calls===1&&(await e.store.get('outbox','transient')).state==='queued','failed lookup does not immediately loop or end copies');
  await e.reconcileRevokedRecipients();
  check(calls===2&&(await e.store.get('outbox','transient')).state==='not_delivered','next existing wake retries failed exact target in unchanged generation');
}
{
  const {e,fp,row}=await fixture();
  await e.store.write([{s:'outbox',k:'generation-race',v:row('generation-race')}]);
  e.members={current:true,listed:'listed',list:[],discovery_generation:8};
  let calls=0;
  e.directory=async()=>{calls++;if(calls===1){e.members={...e.members,discovery_generation:9};e.reconcileRevokedRecipients();return {revoked:false,fingerprint:fp};}return {revoked:true,fingerprint:fp};};
  await e.reconcileRevokedRecipients();
  check(calls===2&&(await e.store.get('outbox','generation-race')).state==='not_delivered','generation changing during directory await gets one coalesced fresh round');
}
console.log('Recipient transient/generation checks complete:',checks);

{
  const {e,fp,row}=await fixture();
  await e.store.write([{s:'outbox',k:'ping-retry',v:row('ping-retry')}]);
  e.members={current:true,listed:'listed',list:[],discovery_generation:10};
  let lookups=0,acks=0;
  e.directory=async()=>{lookups++;if(lookups===1)throw new HubError(503,'','temporary failure');return {revoked:true,fingerprint:fp};};
  e.ackPing=()=>{acks++;};e.flushOutbox=e.flushReceipts=e.retryPendingReceives=async()=>{};
  await e.reconcileRevokedRecipients();
  check(lookups===1&&e.recipientDiscoveryFailed.size===1,'failed discovery retained until an existing maintenance wake');
  await e.dispatch('ping',JSON.stringify({conn:'synthetic-owned-session'}));
  await e.recipientLifecycleRun;
  check(acks===1&&lookups===2&&(await e.store.get('outbox','ping-retry')).state==='not_delivered','existing stream ping retries failed lookup without members or new send');
  await e.dispatch('ping',JSON.stringify({conn:'synthetic-owned-session'}));
  check(lookups===2&&!e.recipientDiscoveryFailed.size,'successful ping retry clears pending discovery and stays idle');
}
console.log('Recipient ping recovery checks complete:',checks);

for (const mode of ['unknown','malformed','wrong-address']) {
  const {e,row}=await fixture();
  await e.store.write([{s:'outbox',k:'definitive',v:row('definitive')}]);
  e.members={current:true,listed:'listed',list:[],discovery_generation:11};
  let http=0;
  e.fetch=async()=>{http++;return mode==='unknown'?new Response('{}',{status:404}):new Response(JSON.stringify({public:mode==='malformed'?{}:JSON.parse(wire.marshalPublic(await wire.publicEntry(await wire.newKeys(),'different/desk')))}),{status:200});};
  e.ackPing=()=>{};e.flushOutbox=e.flushReceipts=e.retryPendingReceives=async()=>{};
  await e.reconcileRevokedRecipients();
  await e.dispatch('ping','{}');
  await e.reconcileRevokedRecipients();
  check(http===1&&!e.recipientDiscoveryFailed.size,'definitive '+mode+' response is memoized, not retried on ping');
  check((await e.store.get('outbox','definitive')).state==='queued','definitive '+mode+' never proves revocation');
  e.members={...e.members,discovery_generation:12};await e.reconcileRevokedRecipients();
  check(http===2,'new generation rechecks definitive '+mode+' response');
}
console.log('Recipient definitive-error checks complete:',checks);
// Only real relay ingress may retain ordinary content accepted before removal.
for (const known of [true,false]) {
  const {e,keys,pub,fp}=await fixture();
  if(!known)await e.store.write([{s:'pins',k:pub.address,v:undefined}]);
  else await e.endRecipient(pub.address,fp,true);
  e.fetch=async()=>new Response(JSON.stringify({public:JSON.parse(wire.marshalPublic(pub)),revoked:true}),{status:200});
  e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  for(const [i,kind] of ['message','answer','result','question','task'].entries()) {
    const n={v:1,id:(200+i+(known?10:0)).toString(16).padStart(32,'0'),from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind,body:'retained ordinary content'};
    const raw=await wire.seal(n,keys,e.pub);
    await e.receiveStreamMessage(raw,true);
    const stored=await e.store.get('inbox',n.id);
    check(['message','answer','result'].includes(kind)?!!stored:!stored,'relay '+kind+' after '+(known?'cached':'directory')+' revocation follows narrow inert-content policy');
    if(stored)check(stored.detail.includes('removed from the server'),'retained relay content has truthful removed-device detail');
  }
  check(!!await e.store.get('kv','revoked-device/'+pub.address+'/'+fp),'unknown relay sender directory revocation is cached for exact verified key');
  const n={v:1,id:(known?'a':'b').repeat(32),from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'not from relay'};
  const raw=await wire.seal(n,keys,e.pub);await e.admit(raw,wire.parseEnvelope(raw));
  check(!await e.store.get('inbox',n.id),'direct/import admission does not receive relay retained-content exception');
}
console.log('Recipient relay retained-content checks complete:',checks);
{
  const {e,keys,pub,fp}=await fixture();
  const roster=await wire.newRoster(keys,pub.address,'Bob'),person=await e.personRecord([roster],'pinned',null);
  const root=await wire.newRoot(keys,{person:person.person,roster:person.hash,address:pub.address,fingerprint:fp},{person:e.me.person,roster:e.me.hash}),conv=await wire.rootID(root);
  await e.store.write([{s:'persons',k:person.person,v:person},{s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:person.person,creator:pub.address,created:root.created}}]);
  await e.endRecipient(pub.address,fp,true);e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  const make=async(extra={})=>{const id=wire.newID();return {id,raw:await wire.seal({v:2,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'scope still checked',conv,lid:wire.newID(),root:wire.rootJSON(root),attachments:[],...extra},keys,e.pub)};};
  const ordinary=await make();await e.receiveStreamMessage(ordinary.raw,true);
  check(!!await e.store.get('inbox',ordinary.id),'version2 relay ordinary content still passes exact pinned member scope');
  const replicated=await make({replica:true,sub:'history'});await e.receiveStreamMessage(replicated.raw,true);
  check(!await e.store.get('inbox',replicated.id),'revoked relay replica/control subtype receives no inert-sync bypass');
  const unscoped=await make({conv:'e'.repeat(64)});await e.receiveStreamMessage(unscoped.raw,true);
  check(!await e.store.get('inbox',unscoped.id),'retained-content exception never bypasses signed conversation scope');
}
console.log('Recipient relay scope checks complete:',checks);

{
  const {e,keys,pub,fp}=await fixture();
  const request=wire.newID(),agent=wire.newID();
  await e.store.write([{s:'outbox',k:request,v:{id:request,v:1,to:pub.address,target:{address:pub.address,fingerprint:fp,agent_id:agent},kind:'question',state:'custody'}}]);
  await e.endRecipient(pub.address,fp,true);e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  for(const correct of [true,false]){
    const id=wire.newID(),raw=await wire.seal({v:1,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'answer',body:'Previously accepted named agent answer',status:'done',reply_to:request,agent_id:correct?agent:wire.newID()},keys,e.pub);
    await e.receiveStreamMessage(raw,true);
    const stored=await e.store.get('inbox',id);
    check(correct?stored?.agent_id===agent&&stored.status==='done':!stored,'retained named answer preserves exact requested agent/host attribution, rejects wrong ID');
  }
}
console.log('Recipient named-answer checks complete:',checks);
{
  const {e,keys,pub,fp}=await fixture();
  const first=await wire.newRoster(keys,pub.address,'Bob');
  const next=await wire.nextRoster(keys,pub.address,first,[pub],null,'Bob updated',[fp]);
  await wire.verifyNext(next,first);
  const old=await e.personRecord([first],'pinned',null),current=await e.personRecord([first,next],'pinned',old);
  const root=await wire.newRoot(keys,{person:current.person,roster:current.hash,address:pub.address,fingerprint:fp},{person:e.me.person,roster:e.me.hash}),conv=await wire.rootID(root);
  await e.store.write([{s:'persons',k:old.person,v:old},{s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:old.person,creator:pub.address,created:root.created}}]);
  await e.endRecipient(pub.address,fp,true);
  e.fetch=async()=>new Response('{"records":[],"more":false}',{status:200});
  e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};e.discloseHumanAudience=()=>{};
  const id=wire.newID(),raw=await wire.seal({v:2,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'Wait for exact signed roster proof',conv,lid:wire.newID(),root:wire.rootJSON(root),attachments:[]},keys,e.pub);
  await e.receiveStreamMessage(raw,true);
  const held=await e.store.get('held',id);
  check(held?.reason==='proof_pending'&&held.relay_ingress===true,'missing signed roster proof retains exact relay provenance durably');
  await e.store.write([{s:'persons',k:current.person,v:current}]);
  await e.retryPasses();
  check(!!await e.store.get('inbox',id)&&!await e.store.get('held',id),'normal held retry uses durable relay provenance only after exact roster proof arrives');
}
console.log('Recipient durable relay-proof checks complete:',checks);

{
  const {e,keys,pub,fp}=await fixture();
  await e.endRecipient(pub.address,fp,true);e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  const id=wire.newID(),raw=await wire.seal({v:1,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'Must not become a review notice',status:'review_notice'},keys,e.pub);
  await e.receiveStreamMessage(raw,true);
  check(!await e.store.get('inbox',id)&&!!await e.store.get('held',id),'signed review_notice is control-like status and cannot use ordinary message exception');
}
{
  const {e,keys,pub,fp}=await fixture();
  await e.endRecipient(pub.address,fp,true);e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  const make=async()=>{const id=wire.newID();return {id,raw:await wire.seal({v:1,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'message',body:'Exact relay admission'},keys,e.pub)};};
  const parent=await make(),child=await make(),opened=e.admitOpened.bind(e);let checkedChild=false;
  e.admitOpened=async(n,env,pin,admission)=>{
    if(env.id===parent.id){await assert.rejects(e.admitInner(child.raw,wire.parseEnvelope(child.raw),admission),/removed from the server/);checkedChild=true;}
    return opened(n,env,pin,admission);
  };
  await e.receiveStreamMessage(parent.raw,true);
  check(checkedChild&&!!await e.store.get('inbox',parent.id)&&!await e.store.get('inbox',child.id),'private relay provenance is bound to exact envelope ID, never inherited by nested child');
}
console.log('Recipient relay control/provenance checks complete:',checks);

{
  const {e,keys,pub,fp}=await fixture();
  await e.endRecipient(pub.address,fp,true);e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  const id=wire.newID(),raw=await wire.seal({v:1,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'answer',body:'Must not finalize a topic',reply_to:wire.newID(),status:'done',topic_done:true},keys,e.pub);
  await e.receiveStreamMessage(raw,true);
  check(!await e.store.get('inbox',id),'topic_done cannot use retained ordinary answer exception');
}
console.log('Recipient topic-state fence checks complete:',checks);

{
  const {e,keys,pub,fp}=await fixture();await e.endRecipient(pub.address,fp,true);
  e.flushReceipts=e.retryHeld=e.retryPendingReceives=async()=>{};
  for(const status of ['proposal','running']) {
    const id=wire.newID(),raw=await wire.seal({v:1,id,from:pub.address,to:e.address,ts:Math.floor(Date.now()/1000),kind:'answer',body:'Cannot create stale action',reply_to:wire.newID(),status},keys,e.pub);
    await e.receiveStreamMessage(raw,true);
    check(!await e.store.get('inbox',id),'revoked retained answer status '+status+' cannot create pending action');
  }
}
{
  const {e,pub,fp}=await fixture(true);
  const id=wire.newID(),request=wire.newID(),notice={id,v:1,from:pub.address,fp,kind:'message',status:'review_notice',at:Date.now(),body:JSON.stringify({v:2,host:pub.address,at:Math.floor(Date.now()/1000),items:[{id:request,key:e.fp,from:e.address,kind:'task',state:'needs_human',actionable:true,conv:true,excerpt:'Existing remote report'}]})};
  await e.store.write([{s:'inbox',k:id,v:notice}]);
  const persistedNotice=await e.store.get('inbox',id);
  check((await e.overview()).review.some(r=>r.id===id),'actual overview initially exposes current remote report');
  check((await e.activeReportItems([notice])).length===1&&(await e.ownNeedsYouReports([notice],[])).length===1,'current exact-key host report remains active');
  e.members={current:true,listed:'listed',list:[{address:pub.address,presence:'offline',suspended:true}]};
  check((await e.activeReportItems([notice])).length===1,'offline or suspended host report is not suppressed');
  await e.endRecipient(pub.address,'different-key',true);
  check((await e.activeReportItems([notice])).length===1&&(await e.ownNeedsYouReports([notice],[])).length===1,'revocation for different host key cannot hide report');
  await e.endRecipient(pub.address,fp,true);
  check((await e.activeReportItems([notice])).length===0&&(await e.ownNeedsYouReports([notice],[])).length===0,'confirmed exact revoked host report no longer creates active review/action');
  check(!(await e.overview()).review.some(r=>r.id===id),'actual overview retires exact revoked host report without adding a no-action card');
  check(JSON.stringify(await e.store.get('inbox',id))===JSON.stringify(persistedNotice),'retired remote notice remains intact in historical records');
}
console.log('Recipient terminal-status/report projection checks complete:',checks);

{
  const {e,pub,fp}=await fixture(true),id=wire.newID();
  const notice={id,v:1,from:pub.address,fp,kind:'message',status:'review_notice',at:Date.now(),body:'One current remote report',state:''};
  await e.store.write([{s:'inbox',k:id,v:notice}]);
  check((await e.overview()).review.some(r=>r.id===id),'report-only host is initially shown without queued transport or requests');
  e.members={current:true,listed:'listed',list:[],discovery_generation:13};
  let calls=0;
  e.fetch=async()=>{calls++;return new Response(JSON.stringify({public:JSON.parse(wire.marshalPublic(pub)),revoked:true}),{status:200});};
  await e.reconcileRevokedRecipients();
  check(calls===1&&!(await e.overview()).review.some(r=>r.id===id),'missing member exact directory revocation discovers report-only host and quiets actual overview');
  let writes=0;const write=e.store.write.bind(e.store);e.store.write=async(...args)=>{writes++;return write(...args);};
  await e.reconcileRevokedRecipients();
  check(calls===1&&writes===0,'retained removed-host report alone causes no repeated directory or state writes');
  check((await e.store.get('inbox',id)).body===notice.body,'report discovery retains source report content');
}
console.log('Recipient active-report discovery checks complete:',checks);

{
 const {e,pub,fp}=await fixture(true),id=wire.newID(),pid=wire.newID();
 const m={id,lid:id,conv:'a'.repeat(64),pid,kind:'question',body:'Retained actionable original',target:{address:pub.address,fingerprint:fp}};
 const exec={state:'needs_human',host:pub.address,attempt:1};
 const status={id:wire.newID(),from:pub.address,fp,control:true,sub:wire.SubStatus,ref:{id,fingerprint:e.fp},body:JSON.stringify({state:'needs_human',n:1,at:Math.floor(Date.now()/1000),attempt:1})};
 e.members={current:true,listed:'listed',list:[{address:pub.address,presence:'connected'}]};
 check(!!e.continuationOf(m,e.fp,exec),'current exact own host may offer existing continuation');
 await e.endRecipient(pub.address,'different-key',true);
 check(!!e.continuationOf(m,e.fp,exec)&&e.execOn([status],pub.address,false)?.state==='needs_human','wrong-key removal leaves exact current host action/status untouched');
 await e.store.write([{s:'inbox',k:status.id,v:status}]);const kept=await e.store.get('inbox',status.id);
 await e.endRecipient(pub.address,fp,true);
 check(e.continuationOf(m,e.fp,exec)===null,'confirmed exact removed own host cannot offer continuation');
 const projected=e.execOn([status],pub.address,false);
 check(projected?.state==='unavailable'&&projected.stale&&projected.detail.includes('permanently removed'),'retained needs-human execution is read-only unavailable projection');
 check(e.execOn([status],pub.address,true)===null,'actual accepted final reply wins before removed-host projection');
 const interrupted={...status,body:JSON.stringify({...JSON.parse(status.body),state:'interrupted'})};
 check(e.execOn([interrupted],pub.address,false)?.state==='unavailable','removed exact interrupted host closes unusable explicit-decision execution');
 const needs=[];e.needsYouOf(m.conv,[{pid,state:'active',host:{address:pub.address,fingerprint:fp,person:e.me.person}}],[m],[status],needs,[]);
 check(needs.length===0,'removed exact host does not keep unusable remote action in Needs You');
 check(JSON.stringify(await e.store.get('inbox',status.id))===JSON.stringify(kept),'unavailable projection preserves original signed saved status');
}
console.log('Recipient removed-host action checks complete:',checks);

{
 const {e,pub,fp}=await fixture(true),ask=wire.newID(),id=wire.newID();
 const question={id:ask,v:1,to:pub.address,kind:'question',body:'Original question',target:{address:pub.address,fingerprint:fp},state:'delivered',at:Date.now()};
 const proposal={id,v:1,from:pub.address,fp,kind:'answer',status:wire.StatusProposal,body:'Retained proposed task',reply_to:ask,at:Date.now()};
 await e.store.write([{s:'outbox',k:ask,v:question},{s:'inbox',k:id,v:proposal}]);
 const kept=await e.store.get('inbox',id);
 check((await e.proposalActions(proposal)).includes('do_it'),'healthy exact own host proposal offers existing confirmation');
 e.members={current:true,listed:'listed',list:[{address:pub.address,presence:'offline',suspended:true}]};
 check((await e.proposalActions(proposal)).includes('do_it'),'offline/suspended proposal host remains usable without permanent revocation evidence');
 await e.endRecipient(pub.address,'different-key',true);
 check((await e.proposalActions(proposal)).includes('do_it'),'wrong-key revoked marker cannot suppress retained proposal');
 await e.endRecipient(pub.address,fp,true);
 check((await e.proposalActions(proposal)).length===0,'confirmed exact removed proposal host no longer offers do_it');
 await assert.rejects(e.confirmProposalOnce(id),/permanently removed/);checks++;
 check((await e.store.outboxStates(['queued'])).length===0,'removed proposal cannot create a new executable task');
 check(JSON.stringify(await e.store.get('inbox',id))===JSON.stringify(kept),'removed proposal source and historical attribution remain stored');
}
console.log('Recipient removed-host proposal checks complete:',checks);

{
 const {e,pub,fp,row}=await fixture();
 const selected=row('selected','queued',{sub:'history',recipient_admission:'exact-selected-admission'});
 await e.store.write([{s:'outbox',k:selected.id,v:selected}]);
 await e.reconcileDepartedRecipients();
 check((await e.store.get('outbox',selected.id)).state==='queued','cross-person selected group history is not own-device replication');
 await e.store.write([{s:'kv',k:'history',v:{[pub.address]:{device:pub.address,fingerprint:fp,state:'running'}}}]);
 await e.reconcileDepartedRecipients();
 check((await e.store.get('outbox',selected.id)).state==='queued','former-own selected group history survives old own history job');
 const own=await e.store.get('kv','person');await e.store.write([{s:'kv',k:'history'},{s:'kv',k:'person',v:{...own,known:[...(own.known||[]),{address:pub.address,fingerprint:fp}]}},{s:'outbox',k:'rootless-own',v:row('rootless-own','queued',{sub:wire.SubRootSync})}]);
 await e.reconcileDepartedRecipients();
 check((await e.store.get('outbox','rootless-own')).state==='not_delivered','exact historical own key terminates rootless own-sync without broadening to foreign recipients');
 const cases=[['Retracted before sending',false,false],['That device was removed from the server.',false,false],['That device was removed from the server.',true,true]];
 for(const [detail,handover,accepted] of cases){const id=wire.newID();await e.store.write([{s:'outbox',k:id,v:row(id,'not_delivered',{kind:'question',detail,handover_started:handover})}]);await e.dispatch('receipt',JSON.stringify({id,state:'delivered',seq:1}));check((await e.store.get('outbox',id)).state===(accepted?'delivered':'not_delivered'),'terminal receipt needs lifecycle termination and possible question handover');}
}
console.log('Recipient selected-history/receipt checks complete:',checks);
