// Real signed group/roster transitions and normal catch-up/admission; no relay.
import * as wire from '../static/wire.mjs';
import {openIDB} from '../static/engine.mjs';

export async function checkControlHistory({world,check,realIDB,c,root,conv,keys,address,roster,pub,alicePub}) {
 const source=await world(),target=await world();
 try {
  await source.receive(c.proof);await source.receive(c.context);
  const stamp=await wire.groupAdmissionHash(wire.groupMember((await source.e.groupCurrent(conv)).state,roster.person).admission);
  const original={v:2,id:wire.newID(),lid:wire.newID(),from:address,to:alicePub.address,ts:1700000000,kind:'message',conv,root:wire.rootJSON(root),body:'original immutable text',fan:[{person:roster.person,roster:await wire.rosterHash(roster)}]};
  const row={...original,fp:source.e.fp,at:1700000000000,state:'delivered',envelope:await wire.seal(original,keys,alicePub),group_admission:stamp,recipient_fp:await wire.fingerprint(alicePub),required_cap:wire.CapGroup};
  await source.st.write([{s:'outbox',k:row.id,v:row}]);
  source.e.groupSupport=async()=>{};source.e.ctlSupport=async(_address,_pin,cap)=>[cap!==wire.CapHistoryArchive,''];source.e.post=async()=>{};
  await source.e.messageControl('edit',{conv,id:row.id,dir:'out',text:'final edited text'});
  await source.e.messageControl('react',{conv,id:row.id,dir:'out',emoji:'👍'});
  const removed={...original,id:wire.newID(),lid:wire.newID(),body:'deleted text must never export'};
  await source.st.write([{s:'outbox',k:removed.id,v:{...row,...removed,envelope:await wire.seal(removed,keys,alicePub)}}]);
  await source.e.messageControl('edit',{conv,id:removed.id,dir:'out',text:'deleted revision must never export'});
  await source.e.messageControl('delete',{conv,id:removed.id,dir:'out'});
  const revisions=(await source.st.all('outbox')).filter(r=>r.sub===wire.SubRevision&&r.body);
  check(revisions.length>0&&revisions.every(r=>r.aside&&r.control&&r.envelope),'edit retains signed aside controls');
  await source.receive(c['solo-proof']);await source.receive(c['solo-context']);
  check((await source.e.dmMembers(await source.e.groupRecord(conv))).size===1,'all original control readers left signed group');
  // Existing pre-011 rows had no history index. Exercise the actual v3→v4
  // storage migration rather than rewriting them through the new write path.
  if(realIDB) {
   const name='agentnet-control-upgrade-'+wire.newID();
   await new Promise((resolve,reject)=>{const q=indexedDB.open(name,3);q.onupgradeneeded=()=>{for(const s of ['kv','pins','persons','convs','inbox','outbox','held','receipts','lids','files'])q.result.createObjectStore(s);};q.onerror=()=>reject(q.error);q.onsuccess=()=>{const db=q.result,tx=db.transaction('outbox','readwrite');for(const r of revisions){const raw={...r};delete raw.history_pos;tx.objectStore('outbox').put(raw,r.id);}tx.oncomplete=()=>{db.close();resolve();};tx.onerror=()=>reject(tx.error);};});
   const migrated=await openIDB(name);
   try{check((await migrated.historyRows('outbox',{conv})).length===revisions.length,'pre-upgrade outgoing controls acquire sparse history indexes');}
   finally{migrated.close();await new Promise((resolve,reject)=>{const q=indexedDB.deleteDatabase(name);q.onsuccess=resolve;q.onerror=()=>reject(q.error);});}
  }
  const phoneKeys=await wire.newKeys(),phoneAddress='browser/control-phone',phonePub=await wire.publicEntry(phoneKeys,phoneAddress),phoneFP=await wire.fingerprint(phonePub);
  const next=await wire.nextRoster(keys,address,roster,[...roster.devices,phonePub],await wire.joinConsent(phoneKeys,phoneAddress,roster.person,1,await wire.rosterHash(roster)),roster.label,[...await wire.rosterHumans(roster),phoneFP]);
  await wire.verifyNext(next,roster);const chain=[roster,next],person=await source.e.personRecord(chain,'self',null);
  for(const w of [source,target]){w.extraChains.set(roster.person,chain.map(r=>JSON.parse(wire.rosterJSON(r))));w.e.me=person;await w.st.write([{s:'kv',k:'person',v:person}]);await w.e.pinDevices(person);}
  target.e.keys=phoneKeys;target.e.address=phoneAddress;target.e.fp=phoneFP;
  await target.st.write([{s:'kv',k:'identity',v:{keys:phoneKeys,address:phoneAddress,fingerprint:phoneFP}}]);
  await source.e.runHistory();
  const copies=(await source.st.all('outbox')).filter(r=>r.to===phoneAddress),history=copies.filter(r=>r.sub==='history').map(r=>wire.parseHistory(r.body));
  check(history.some(h=>h.lid===original.lid&&h.body===original.body),'catch-up preserves immutable original message');
  check(history.filter(h=>h.sub===wire.SubRevision).length===1,'catch-up includes one exact original revision despite all old readers leaving');
  check(!history.some(h=>h.body.includes('must never export')),'deleted original and revision text never export');
  check((await source.st.prefix('kv','history-deferred/'+phoneFP+'/')).length===0,'deleted targets and erased revisions do not leave deferred catch-up work');
  const dev=person.devices.find(d=>d.address===phoneAddress),group=await source.e.groupRecord(conv),storedRevision=await source.st.get('outbox',history.find(h=>h.sub===wire.SubRevision).id);
  for(const mode of ['signature','ts','id','ref','lid','body']) {
   const item=source.e.itemOf(storedRevision,true);
   if(mode==='signature'){const env=JSON.parse(storedRevision.envelope);env.sig=wire.b64(new Uint8Array(64));await source.st.write([{s:'outbox',k:storedRevision.id,v:{...storedRevision,envelope:JSON.stringify(env)}}]);}
   if(mode==='ts')item.ts++;
   if(mode==='id')item.id=wire.newID();
   if(mode==='ref')item.ref={...item.ref,id:wire.newID()};
   if(mode==='lid')item.lid=wire.newID();
   if(mode==='body')item.body='{"rev":2,"text":"forged"}';
   let refused=false;try{await source.e.historyCopy(dev,group,item);}catch{refused=true;}
   check(refused,'outgoing original control refuses altered '+mode);
   if(mode==='signature')await source.st.write([{s:'outbox',k:storedRevision.id,v:storedRevision}]);
  }
  // The same signed structural prerequisites must also survive actual archive
  // staging, age blob upload/download and normal admission on a fresh phone.
  const archiveTarget=await world();
  try {
   archiveTarget.extraChains.set(roster.person,chain.map(r=>JSON.parse(wire.rosterJSON(r))));
   Object.assign(archiveTarget.e,{keys:phoneKeys,address:phoneAddress,fp:phoneFP,me:person});
   await archiveTarget.st.write([{s:'kv',k:'person',v:person},{s:'kv',k:'identity',v:{keys:phoneKeys,address:phoneAddress,fingerprint:phoneFP}}]);
   await archiveTarget.e.pinDevices(person);
   const priorFetch=source.e.fetch,priorQueue=source.e.queueOutbox,uploads=new Map();
   source.e.fetch=async(url,options)=>{
    const path=new URL(url).pathname;
    if(path==='/v1/blobs'&&options.method==='POST'){
     const blob=JSON.parse(options.body);uploads.set(blob.id,{...blob,received:0,state:'uploading',parts:[]});
     return new Response(JSON.stringify(uploads.get(blob.id)));
    }
    const id=path.split('/')[3],upload=uploads.get(id);
    if(upload&&options.method==='PUT'){
     upload.parts.push(new Uint8Array(options.body));upload.received+=options.body.byteLength;
     return new Response(JSON.stringify(upload));
    }
    if(upload&&path.endsWith('/complete')){
     const ct=new Uint8Array(upload.size);let offset=0;for(const part of upload.parts){ct.set(part,offset);offset+=part.length;}
     archiveTarget.blobs.set(id,ct);upload.state='stored';return new Response(JSON.stringify(upload));
    }
    return priorFetch(url,options);
   };
   source.e.queueOutbox=()=>{};source.e.ctlSupport=async()=>[true,''];
   try {
    await source.st.write(await source.e.archives().prepare(copies,[],[],dev));
    source.e.connected=true;await source.e.archives().wake();source.e.connected=false;
    const descriptors=await source.st.outboxSub(wire.SubHistoryArchive);
    check(descriptors.length>0&&descriptors.every(r=>r.files[0].uploaded&&!r.files[0].ct),'group archive source uploads encrypted chunks before descriptors');
    check(copies.some(r=>[wire.SubGroupProof,wire.SubGroupContext].includes(r.sub)&&r.files?.length),'group archive contains actual signed proof/context blob prerequisites');
    for(const descriptor of descriptors){await archiveTarget.receive({envelope:descriptor.envelope});archiveTarget.e.connected=true;await archiveTarget.e.archives().wake();archiveTarget.e.connected=false;}
    const jobs=await archiveTarget.st.prefix('kv','history-archive/in/');
    check(jobs.length===descriptors.length&&jobs.every(j=>j.state==='done'),'group structural archive finishes normal child admissions');
    check(!(await archiveTarget.st.all('held')).length,'group archive proof/context and history leave no held children');
    check((await archiveTarget.e.groupThread(conv)).messages.find(m=>m.lid===original.lid)?.text==='final edited text','archive fresh phone renders exact final group revision');
    check(!(await archiveTarget.st.all('receipts')).some(r=>copies.some(child=>child.id===r.id))&&!archiveTarget.receipts.some(r=>copies.some(child=>child.id===r.id)),'archived structural children never fabricate relay receipts');
    source.e.connected=false;
    for(const descriptor of descriptors)await source.e.archives().receipt({...descriptor,state:'delivered'});
    const accepted=await Promise.all(copies.map(row=>source.st.get('outbox',row.id)));
    check(accepted.filter(row=>row.files?.length).every(row=>row.state==='archive_accepted'&&row.files.every(file=>file.ct===null&&file.attachment.blob.id)),'retained group archive releases duplicate structural transfer ciphertext while keeping signed metadata');

   } finally {source.e.connected=false;source.e.fetch=priorFetch;source.e.queueOutbox=priorQueue;}
  } finally {await archiveTarget.close();}
  for(const r of copies){for(const f of r.files||[])target.blobs.set(f.attachment.blob.id,f.ct);await target.receive({envelope:r.envelope});}
  await target.e.groupParticipationHistoryCheck(conv,history.find(h=>h.sub===wire.SubRevision),{address,fingerprint:source.e.fp});
  check(!(await target.st.all('held')).length,'all valid linked history admitted: '+JSON.stringify((await target.st.all('held')).map(r=>({reason:r.reason,detail:r.detail}))));
  check((await target.e.groupThread(conv)).messages.find(m=>m.lid===original.lid)?.text==='final edited text','new own reader renders final revision over immutable history');
  check((await target.e.groupThread(conv)).messages.find(m=>m.lid===original.lid)?.reactions?.some(r=>r.emoji==='👍'),'own reaction restores on exact historical original');
  const deletedView=(await target.e.groupThread(conv)).messages.find(m=>m.lid===removed.lid);
  check(!deletedView||deletedView.deleted&&!deletedView.body,'fresh phone has no orphan deletion card or restored deleted text');
  const admitted=await target.st.get('inbox',original.id),revision=history.find(h=>h.sub===wire.SubRevision),forwarder={address,fingerprint:source.e.fp};
  check(admitted.history&&admitted.claimed_key===source.e.fp&&!admitted.fp,'original author comes from admitted own history, not recipient identity');
  for(const mode of ['wrong-claimed-key','wrong-ref-key','wrong-ref-lid','excerpt']) {
   const changed={...admitted},control=structuredClone(revision);
   if(mode==='wrong-claimed-key')changed.claimed_key=phoneFP;
   if(mode==='wrong-ref-key')control.ref.fingerprint=phoneFP;
   if(mode==='wrong-ref-lid')control.ref.id=wire.newID();
   if(mode==='excerpt')changed.excerpt_pid=wire.newID();
   await target.st.write([{s:'inbox',k:admitted.id,v:changed}]);
   let refused=false;try{await target.e.groupParticipationHistoryCheck(conv,control,forwarder);}catch{refused=true;}
   check(refused,'exact admitted control target refuses '+mode);
   await target.st.write([{s:'inbox',k:admitted.id,v:admitted}]);
  }
  await target.reload();check((await target.e.groupThread(conv)).messages.find(m=>m.lid===original.lid)?.text==='final edited text','recovered edit remains visible after reload');
  check(!(await target.st.all('inbox')).some(r=>['running','awaiting','needs_human'].includes(r.state)),'historical edit creates no runnable work');
  const baseline=(await source.st.historyRows('outbox',{conv})).length;
  for(const extra of [{sub:'history'},{sub:'group-context'},{sub:'root-sync'},{sub:'revision',control:false},{sub:'revision',ref:null},{sub:'revision',ref:{id:'bad',fingerprint:'bad'}},{sub:'revision',body:''},{sub:'status'}]) {
   const id=wire.newID();await source.st.write([{s:'outbox',k:id,v:{...revisions[0],id,lid:wire.newID(),...extra}}]);
  }
  check((await source.st.historyRows('outbox',{conv})).length===baseline,'aside carriers and malformed control refs never become history sources');
 } finally {await source.close();await target.close();}
}
