const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 equal:(a,b,m)=>{if(a!==b)throw Error(m||'Values differ');},
 notEqual:(a,b,m)=>{if(a===b)throw Error(m||'Values unexpectedly match');},
 deepEqual:(a,b,m)=>{if(JSON.stringify(a)!==JSON.stringify(b))throw Error(m||'Structures differ');},
 ok:(x,m)=>{if(!x)throw Error(m||'Expected truthy');},
 rejects:async(fn,re)=>{try{await fn();}catch(e){if(re.test(e.message))return;throw e;}throw Error('Expected rejection '+re);}
};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const wait=ms=>new Promise(r=>setTimeout(r,ms));
const stores=[];
async function fixtureStore(){const name='receive-priority-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name);stores.push({store,name});return store;}
async function ident(address,label){const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,address),fp=await wire.fingerprint(pub),roster=await wire.newRoster(keys,address,label);return {address,keys,pub,fp,roster,hash:await wire.rosterHash(roster)};}
const phone=await ident('alice/phone','Alice'),old=await ident('old/host','Old'),fresh=await ident('current/host','Current');
const pin=p=>({address:p.address,json:wire.marshalPublic(p.pub),fingerprint:p.fp,pending:null});
let checks=0;
for(const phase of ['profile','profile-backlog','blob','blob-shared']){
 let release,enter;const entered=new Promise(r=>enter=r),gate=new Promise(r=>release=r);
 const st=await fixtureStore();let first,blob='',realm='',requests=[];
 const creator={person:old.roster.person,roster:old.hash,address:old.address,fingerprint:old.fp};
 const members=[{person:old.roster.person,roster:old.hash},{person:phone.roster.person,roster:phone.hash}].sort((a,b)=>a.person.localeCompare(b.person));
 if(phase.startsWith('profile')){
  const root=await wire.newRoot(old.keys,creator,{person:phone.roster.person,roster:phone.hash}),conv=await wire.rootID(root);
  first=await wire.seal({v:2,id:wire.newID(),from:old.address,to:phone.address,ts:1,kind:'message',body:'old retained turn',conv,root:wire.rootJSON(root),lid:wire.newID()},old.keys,phone.pub);
 }else{
  realm=wire.newID();const root=await wire.signGroupRoot(old.keys,{v:3,kind:'group',creator,members,nonce:wire.newID(),created:1,realm,title:'Signed isolated group',admins:[old.roster.person]}),conv=await wire.rootID(root);
  const groupMembers=[];for(const p of [old,phone])groupMembers.push({person:p.roster.person,roster:p.hash,admin:p===old,admission:await wire.signGroupAdmission(p.keys,{conv,realm,person:p.roster.person,roster:p.hash,seq:0,prev:'',history:null,by:p.fp})});
  groupMembers.sort((a,b)=>a.person.localeCompare(b.person));
  const state=await wire.signGroupState(old.keys,{v:1,conv,realm,seq:0,prev:'',title:root.title,actor:old.roster.person,actor_roster:old.hash,by:old.fp,members:groupMembers}),body=wire.groupContextJSON({root,state,withdrawals:[]});
  const file=await wire.encryptFile(new TextEncoder().encode(body),'group-context.json',phone.pub);blob=file.attachment.blob.id;
  first=await wire.seal({v:2,id:wire.newID(),from:old.address,to:phone.address,ts:1,kind:'message',conv,root:wire.rootJSON(root),lid:wire.newID(),sub:wire.SubGroupContext,body:wire.groupCarrierJSON({v:1,seq:0,hash:await wire.groupStateHash(state),to_key:phone.fp}),attachments:[file.attachment]},old.keys,phone.pub);
 }
 const second=await wire.seal({id:wire.newID(),from:fresh.address,to:phone.address,ts:2,kind:'answer',body:'fresh useful reply'},fresh.keys,phone.pub),firstID=wire.parseEnvelope(first).id,secondID=wire.parseEnvelope(second).id;
 const oldFrames=[first];
 if(phase==='profile-backlog')for(let i=1;i<32;i++){const n=await wire.open(first,phone.keys,phone.address,old.pub);oldFrames.push(await wire.seal({...n,id:wire.newID(),lid:wire.newID()},old.keys,phone.pub));}
 let active=0,peak=0,acks=0;
 const e=new Engine({store:st,base:'https://isolated.invalid',fetch:async url=>{
  const path=new URL(url).pathname;requests.push(path);
  if(path==='/v1/stream')return new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode(`event: message\ndata: ${oldFrames.join('\n\nevent: message\ndata: ')}\n\nevent: message\ndata: ${second}\n\nevent: ping\ndata: {"conn":"isolated"}\n\n`));c.close();}}));
  if(path==='/v1/stream/ack'){acks++;return new Response('{}');}
  if(path==='/v1/agents/'+old.address+'/profile'||path==='/v1/blobs/'+blob+'/data'){active++;peak=Math.max(peak,active);enter();await gate;active--;return new Response('{"error":"isolated unavailable"}',{status:503});}
  throw Error('Unexpected isolated route '+path);
 }});
 Object.assign(e,phone,{running:true,realm});e.onConnect=async()=>{};e.flushReceipts=async()=>{};
 e.me=await e.personRecord([phone.roster],'self',null);
 await st.write([{s:'pins',k:old.address,v:pin(old)},{s:'pins',k:fresh.address,v:pin(fresh)},{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:e.me}]);
 let background;
 if(phase==='blob-shared'){const n=await wire.open(first,phone.keys,phone.address,old.pub);background=e.cipherOf(n.attachments[0],true).catch(()=>{});await entered;}
 const run=e.streamOnce();let timer;
 try{await Promise.race([entered,new Promise((_,rej)=>timer=setTimeout(()=>rej(Error('No expected stall reached')),2000))]);clearTimeout(timer);
  for(let i=0;i<100&&(!(await st.get('inbox',secondID))||acks!==1);i++)await wait(5);
  const before={freshStored:!!await st.get('inbox',secondID),oldPendingDurable:!!await st.get('kv','receive-pending/'+firstID),oldReceipt:!!await st.get('receipts',firstID),requests};
  assert.equal(before.freshStored,true,'fresh cached-key answer precedes '+phase+' remote evidence');
  assert.equal(before.oldPendingDurable,true,'exact carrier durable before '+phase+' remote evidence');
  assert.equal(before.oldReceipt,false,'no receipt before '+phase+' admission');
  assert.equal(acks,1,'same-chunk heartbeat acknowledgment progresses before '+phase+' evidence');
  assert.equal(peak,1,'existing recovery keeps only one remote carrier active');
  await e.dispatch('message',first);
  const original=await wire.open(first,phone.keys,phone.address,old.pub),changed={...original,body:phase.startsWith('profile')?'other plaintext':wire.groupCarrierJSON({v:1,seq:0,hash:'f'.repeat(64),to_key:phone.fp})};
  const collision=await wire.seal(changed,old.keys,phone.pub);await assert.rejects(()=>e.dispatch('message',collision),/identity differs/);
  if(phase==='profile-backlog')assert.equal((await st.prefix('kv','receive-pending/')).length,32,'large old backlog stays durable without admission tasks');
  checks+=7;
  release();await run;if(e.receiveRetryRun)await e.receiveRetryRun;await background;
  const after={freshStored:!!await st.get('inbox',secondID),oldPendingDurable:!!await st.get('kv','receive-pending/'+firstID),oldReceipt:!!await st.get('receipts',firstID)};
  assert.equal(after.freshStored,true);assert.equal(after.oldReceipt,false);checks+=2;
 }finally{clearTimeout(timer);release();await run;await e.close();}
}
// Durable authority changes must defeat old in-memory identity and proof,
// including a pending carrier retried with full network admission.
for(const retry of [false,true])for(const mutation of ['identity-missing','identity-changed','own-missing','own-removed','own-frozen','sender-changed','identity-at-commit','own-at-commit','sender-at-commit','person-frozen-at-commit','person-removed-at-commit']){
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('Unexpected authority-test network');}});
 Object.assign(e,phone);e.flushReceipts=async()=>{};
 const own=await e.personRecord([phone.roster],'self',null),peer=await e.personRecord([fresh.roster],'pinned',null);e.me=own;
 const identity={keys:phone.keys,address:phone.address,fingerprint:phone.fp},root=await wire.newRoot(fresh.keys,{person:fresh.roster.person,roster:fresh.hash,address:fresh.address,fingerprint:fresh.fp},{person:phone.roster.person,roster:phone.hash});
 const raw=await wire.seal({v:2,id:wire.newID(),from:fresh.address,to:phone.address,ts:3,kind:'message',body:'requires current own proof',conv:await wire.rootID(root),root:wire.rootJSON(root),lid:wire.newID()},fresh.keys,phone.pub),id=wire.parseEnvelope(raw).id;
 await st.write([{s:'kv',k:'identity',v:identity},{s:'kv',k:'person',v:own},{s:'persons',k:peer.person,v:peer},{s:'pins',k:fresh.address,v:pin(fresh)}]);
 const changed=mutation.startsWith('person')?{s:'persons',k:peer.person,v:mutation==='person-frozen-at-commit'?{...peer,state:'conflict'}:{...peer,devices:[]}}:mutation.startsWith('identity')?{s:'kv',k:'identity',v:mutation==='identity-missing'?undefined:{...identity,fingerprint:old.fp}}:mutation.startsWith('own')?{s:'kv',k:'person',v:mutation==='own-removed'?{...own,devices:[]}:mutation==='own-frozen'?{...own,state:'conflict'}:undefined}:{s:'pins',k:fresh.address,v:{...pin(fresh),pending:{fingerprint:old.fp,json:wire.marshalPublic(old.pub)}}};
 if(mutation.endsWith('at-commit')){
  const write=st.write;let once=true;st.write=async(ops,snapshots)=>{if(once&&ops.some(o=>o.s==='inbox')){once=false;await write([changed]);}return write(ops,snapshots);};
 }else await st.write([changed]);
 if(retry){await st.write([{s:'kv',k:'receive-pending/'+id,v:{id,address:e.address,fingerprint:e.fp,envelope:raw,at:e.now()}}]);await e.retryPendingReceives();}else await e.dispatch('message',raw);
 assert.equal(await st.get('inbox',id),undefined,mutation+' cannot admit through stale memory, retry='+retry);
 assert.notEqual((await st.get('receipts',id))?.state,'delivered',mutation+' cannot claim delivery, retry='+retry);checks+=2;
 await e.close();
}
// A stale fan roster may prepare encrypted history for a newly linked own
// device, but its current pin must still match at the atomic admission commit.
for(const retry of [false,true])for(const mutation of ['none','pending','replaced']){
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('Unexpected forwarding network');}});Object.assign(e,phone);e.flushReceipts=async()=>{};
 const join=await wire.joinConsent(old.keys,old.address,phone.roster.person,1,phone.hash),next=await wire.nextRoster(phone.keys,phone.address,phone.roster,[phone.pub,old.pub],join,phone.roster.label,[phone.fp,old.fp]);await wire.verifyNext(next,phone.roster);
 const own=await e.personRecord([phone.roster,next],'self',null),peer=await e.personRecord([fresh.roster],'pinned',null);e.me=own;
 const root=await wire.newRoot(fresh.keys,{person:fresh.roster.person,roster:fresh.hash,address:fresh.address,fingerprint:fresh.fp},{person:phone.roster.person,roster:phone.hash}),conv=await wire.rootID(root);
 const raw=await wire.seal({v:2,id:wire.newID(),from:fresh.address,to:phone.address,ts:5,kind:'message',body:'exact stale-fan original',conv,root:wire.rootJSON(root),lid:wire.newID(),fan:[{person:phone.roster.person,roster:phone.hash}]},fresh.keys,phone.pub),id=wire.parseEnvelope(raw).id;
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:own},{s:'persons',k:peer.person,v:peer},{s:'pins',k:fresh.address,v:pin(fresh)},{s:'pins',k:old.address,v:pin(old)},{s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:peer.person,creator:fresh.address,created:root.created}}]);
 if(mutation!=='none'){
  const write=st.write;let once=true;st.write=async(ops,snapshots)=>{if(once&&ops.some(o=>o.s==='outbox')){once=false;await write([{s:'pins',k:old.address,v:mutation==='pending'?{...pin(old),pending:{fingerprint:fresh.fp,json:wire.marshalPublic(fresh.pub)}}:{...pin(old),fingerprint:fresh.fp,json:wire.marshalPublic(fresh.pub)}}]);}return write(ops,snapshots);};
 }
 if(retry){await st.write([{s:'kv',k:'receive-pending/'+id,v:{id,address:e.address,fingerprint:e.fp,envelope:raw,at:e.now()}}]);await e.retryPendingReceives();}else await e.dispatch('message',raw);
 assert.equal((await st.all('outbox')).length,mutation==='none'?1:0,'forwarding pin '+mutation+' rechecked, retry='+retry);
 assert.equal(!!await st.get('inbox',id),true,'valid original survives unrelated forwarding pin change, retry='+retry);assert.equal((await st.get('receipts',id))?.state,'delivered');checks+=3;
 if(mutation!=='none'){
  if(e.historyRun)await e.historyRun;
  const dev=own.devices.find(d=>d.address===old.address),job={device:old.address,fingerprint:old.fp,state:'running',pos:null,done:0,total:1,own_human:e.fp};
  await st.write([{s:'kv',k:'history',v:{[old.address]:job}}]);
  await e.historyCatchupStep(dev,job);assert.equal((await st.all('outbox')).length,0,'normal history recovery refuses changed sibling pin');assert.deepEqual((await e.historyBook())[old.address],job,'blocked recovery does not advance its source cursor');
  await st.write([{s:'pins',k:old.address,v:pin(old)}]);
  await e.historyCatchupStep(dev,job);
  const copies=(await st.all('outbox')).filter(r=>r.sub==='history'&&r.to===old.address);assert.equal(copies.length,1,'existing history step resumes exact retained original after matching trust is restored');
  const h=wire.parseHistory(copies[0].body);assert.equal(h.id,id);assert.equal(h.from_key,fresh.fp);assert.equal(h.body,'exact stale-fan original');checks+=6;
 }
 await e.close();
}
for(const retry of [false,true])for(const removed of [false,true]){
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('Unexpected control network');}});Object.assign(e,phone);e.flushReceipts=async()=>{};
 const own=await e.personRecord([phone.roster],'self',null),peer=await e.personRecord([fresh.roster],'pinned',null);e.me=own;
 const root=await wire.newRoot(fresh.keys,{person:fresh.roster.person,roster:fresh.hash,address:fresh.address,fingerprint:fresh.fp},{person:phone.roster.person,roster:phone.hash}),conv=await wire.rootID(root),originalLID=wire.newID();
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:removed?{...own,devices:[]}:own},{s:'persons',k:peer.person,v:peer},{s:'pins',k:fresh.address,v:pin(fresh)},{s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:peer.person,creator:fresh.address,created:root.created}}]);
 const raw=await wire.seal({v:3,id:wire.newID(),from:fresh.address,to:phone.address,ts:6,kind:'message',body:JSON.stringify({rev:1,text:'exact signed revision'}),conv,lid:wire.newID(),sub:wire.SubRevision,ref:{id:originalLID,fingerprint:fresh.fp}},fresh.keys,phone.pub),id=wire.parseEnvelope(raw).id;
 if(retry){await st.write([{s:'kv',k:'receive-pending/'+id,v:{id,address:e.address,fingerprint:e.fp,envelope:raw,at:e.now()}}]);await e.retryPendingReceives();}else await e.dispatch('message',raw);
 assert.equal(!!await st.get('inbox',id),!removed,'V3 conversation exact current recipient, retry='+retry);
 assert.equal((await st.get('receipts',id))?.state,removed?'quarantined':'delivered');checks+=2;await e.close();
}
{
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>new Response('{"error":"unavailable"}',{status:503})});Object.assign(e,phone);
 await st.write([{s:'pins',k:fresh.address,v:pin(fresh)}]);const admission={localOnly:false,checks:[],state:{deferred:false}};
 assert.equal((await e.sendKey(fresh.address,admission)).fingerprint,fresh.fp,'full recovery retains existing offline key fallback');
 await st.write([{s:'pins',k:fresh.address,v:{...pin(fresh),pending:{fingerprint:old.fp,json:wire.marshalPublic(old.pub)}}}]);
 await assert.rejects(()=>st.write([{s:'outbox',k:wire.newID(),v:{}}],admission.checks),/storage changed during verification/);assert.equal((await st.all('outbox')).length,0);checks+=3;await e.close();
}
// A genuinely enrolled device without a person still reads ordinary V1
// direct replies, as it did before receiver network isolation.
{
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('Unexpected legacy network');}});Object.assign(e,phone);e.flushReceipts=async()=>{};
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'pins',k:fresh.address,v:pin(fresh)}]);
 const raw=await wire.seal({id:wire.newID(),from:fresh.address,to:phone.address,ts:4,kind:'answer',body:'legacy direct reply'},fresh.keys,phone.pub),id=wire.parseEnvelope(raw).id;
 await e.dispatch('message',raw);assert.equal((await st.get('inbox',id))?.body,'legacy direct reply');assert.equal((await st.get('receipts',id))?.state,'delivered');checks+=2;await e.close();
}
// A second encrypted arrival while discovery is active retains its wake.
// Responsive admission drains page-sized batches without waiting for a ping;
// unavailable evidence stops at the cursor instead of running a hot loop.
for(const total of [2,32,102])for(const unavailable of [false,true]){
 const st=await fixtureStore();let release,entered,requests=0,available=!unavailable;
 const gate=new Promise(r=>release=r),started=new Promise(r=>entered=r);
 const fetch=async url=>{const p=new URL(url).pathname;requests++;
  if(p==='/v1/agents/'+old.address+'/profile'){entered();await gate;return available?new Response(JSON.stringify({person:JSON.parse(wire.rosterJSON(old.roster))})):new Response('{"error":"unavailable"}',{status:503});}
  if(p==='/v1/persons/'+old.roster.person+'/chain')return new Response(JSON.stringify({records:[JSON.parse(wire.rosterJSON(old.roster))],more:false}));
  throw Error('Unexpected coalesced receive route '+p);
 };
 let e=new Engine({store:st,base:'https://isolated.invalid',fetch});Object.assign(e,phone);e.flushReceipts=async()=>{};e.me=await e.personRecord([phone.roster],'self',null);
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:e.me},{s:'pins',k:old.address,v:pin(old)}]);
 const root=await wire.newRoot(old.keys,{person:old.roster.person,roster:old.hash,address:old.address,fingerprint:old.fp},{person:phone.roster.person,roster:phone.hash}),conv=await wire.rootID(root);
 const raws=[];for(let i=1;i<=total;i++)raws.push(await wire.seal({v:2,id:i.toString(16).padStart(32,'0'),from:old.address,to:phone.address,ts:7,kind:'message',body:'coalesced retained '+i,conv,root:wire.rootJSON(root),lid:wire.newID()},old.keys,phone.pub));
 try{
  await e.dispatch('message',raws[0]);await started;
  for(const raw of raws.slice(1))await e.dispatch('message',raw);
  release();await e.receiveRetryRun;
  if(available){assert.equal((await st.all('inbox')).length,total,'all responsive signed deferred carriers recover without heartbeat');assert.equal((await st.prefix('kv','receive-pending/')).length,0);}
  else{
   assert.equal((await st.all('inbox')).length,0);assert.equal((await st.prefix('kv','receive-pending/')).length,total);assert.equal((await st.all('receipts')).length,0);
   assert.ok(requests<=17,'coalesced unavailable recovery remains one bounded page plus first active lookup');const before=requests;await wait(30);assert.equal(requests,before,'unavailable recovery never polls');
   await e.close();available=true;e=new Engine({store:st,base:'https://isolated.invalid',fetch});await e.load();e.flushReceipts=async()=>{};
   await e.retryPendingReceives();assert.equal((await st.all('inbox')).length,total,'restart keeps cursor and completes responsive pages');
  }
  checks+=unavailable?6:2;
 }finally{release();await e.close();}
}
// A verified fresh turn can appear while an earlier exact carrier waits for
// proof. Admission completion must preserve that carrier's original arrival.
for(const restart of [false,true]){
 const st=await fixtureStore();let clock=10000,release,entered;
 const gate=new Promise(r=>release=r),started=new Promise(r=>entered=r);
 const fetch=async url=>{const p=new URL(url).pathname;
  if(p==='/v1/agents/'+old.address+'/profile'){entered();await gate;return new Response(JSON.stringify({person:JSON.parse(wire.rosterJSON(old.roster))}));}
  if(p==='/v1/persons/'+old.roster.person+'/chain')return new Response(JSON.stringify({records:[JSON.parse(wire.rosterJSON(old.roster))],more:false}));
  throw Error('Unexpected chronology route '+p);
 };
 let e=new Engine({store:st,base:'https://isolated.invalid',fetch,now:()=>clock});Object.assign(e,phone);e.flushReceipts=async()=>{};e.me=await e.personRecord([phone.roster],'self',null);
 await st.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:e.me},{s:'pins',k:old.address,v:pin(old)}]);
 const root=await wire.newRoot(old.keys,{person:old.roster.person,roster:old.hash,address:old.address,fingerprint:old.fp},{person:phone.roster.person,roster:phone.hash}),conv=await wire.rootID(root);
 const signed=async(kind,body)=>wire.seal({v:2,id:wire.newID(),from:old.address,to:phone.address,ts:7,kind,body,conv,root:wire.rootJSON(root),lid:wire.newID()},old.keys,phone.pub);
 const first=await signed('message','earlier retained original'),second=await signed('question','later useful question'),id=wire.parseEnvelope(first).id,secondID=wire.parseEnvelope(second).id;
 try{
  if(restart){e.retryPendingReceives=()=>Promise.resolve();await e.dispatch('message',first);await e.close();e=new Engine({store:st,base:'https://isolated.invalid',fetch,now:()=>clock});await e.load();e.flushReceipts=async()=>{};e.retryPendingReceives();}
  else await e.dispatch('message',first);
  await started;assert.equal((await st.get('kv','receive-pending/'+id)).at,10000);
  const peer=await e.personRecord([old.roster],'pinned',null);await st.write([{s:'persons',k:peer.person,v:peer}]);clock=20000;
  await e.dispatch('message',second);assert.equal((await st.get('inbox',secondID)).kind,'question','fresh signed turn visible before slow original proof');assert.equal(await st.get('receipts',id),undefined);
  // A concurrent local roster-metadata write forces normal snapshot reverify;
  // its authority and keys stay identical, as a real startup/profile race can.
  const write=st.write;let conflict=true;
  st.write=async(ops,snapshots)=>{if(conflict&&ops.some(o=>o.s==='inbox'&&o.k===id)){conflict=false;const own=await st.get('kv','person');await write([{s:'kv',k:'person',v:{...own,fixtureRevision:1}}]);}return write(ops,snapshots);};
  clock=30000;release();await e.receiveRetryRun;
  const original=await st.get('inbox',id);assert.equal(original.at,10000,'exact original retains first local arrival');assert.equal(original.ts,7,'signed source timestamp unchanged');
  const shown=await e.convMessages(conv,await st.all('inbox'),await st.all('outbox'));assert.deepEqual(shown.map(r=>r.kind),['message','question'],'display order follows first arrival, not proof completion');
  assert.equal((await st.get('receipts',id)).state,'delivered');assert.equal(await st.get('kv','receive-pending/'+id),undefined);checks+=8;
 }finally{release();await e.close();}
}
// Pending removal anchors only a new visible original; source-time copies,
// controls and nonmatching or malformed local pending records keep their time.
for(const mode of ['control','history','replica','device-history','excerpt','existing','invalid-time','wrong-recipient','different-carrier','pending-CAS']){
 const st=await fixtureStore(),e=new Engine({store:st,base:'https://isolated.invalid',fetch:async()=>{throw Error('Unexpected timestamp network');}});Object.assign(e,phone);
 const raw=await wire.seal({id:wire.newID(),from:fresh.address,to:phone.address,ts:7,kind:'message',body:'retained source-time fixture'},fresh.keys,phone.pub),env=wire.parseEnvelope(raw),key='receive-pending/'+env.id;
 const row={id:env.id,address:phone.address,fingerprint:phone.fp,envelope:raw,at:10000};
 if(mode==='invalid-time')row.at=-1;if(mode==='wrong-recipient')row.fingerprint=old.fp;if(mode==='different-carrier')row.envelope=raw+' ';
 const source={id:env.id,from:env.from,kind:'message',body:'retained source-time fixture',ts:7,at:7000};
 if(mode==='control')source.control=true;if(mode==='history')source.history=true;if(mode==='replica')source.replica=true;if(mode==='device-history')source.device_history=true;if(mode==='excerpt')source.sub='excerpt';
 await st.write([{s:'kv',k:key,v:row},...(mode==='existing'?[{s:'inbox',k:env.id,v:source}]:[])]);
 const ops=[{s:'inbox',k:env.id,v:{...source}}],snapshots=[];await e.removeReceivePending(raw,env,ops,snapshots);
 if(mode==='pending-CAS'){
  await st.write([{s:'kv',k:key,v:{...row,at:20000}}]);await assert.rejects(()=>st.write(ops,snapshots),/storage changed during verification/);assert.equal(await st.get('inbox',env.id),undefined);checks+=2;
 }else{
  await st.write(ops,snapshots);assert.equal((await st.get('inbox',env.id)).at,7000,mode+' preserves source/existing timestamp');assert.equal((await st.get('inbox',env.id)).ts,7);
  if(['wrong-recipient','different-carrier'].includes(mode))assert.deepEqual(await st.get('kv',key),row,'nonmatching retained carrier is untouched');checks+=['wrong-recipient','different-carrier'].includes(mode)?3:2;
 }
 await e.close();
}
const result={ok:true,checks,storage:typeof window==='undefined'?'memory unit only':'IndexedDB'};
for(const {store,name} of stores){store.close();if(typeof window!=='undefined')await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(name);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});}
if(typeof window!=='undefined')window.receivePriorityResult=result;
console.log(JSON.stringify(result));
