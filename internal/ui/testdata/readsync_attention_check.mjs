import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[],blobs=new Map();
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p.startsWith('/v1/blobs/'))return new Response(blobs.get(p.split('/')[3])||null);
 if(p.startsWith('/v1/groups/')&&p.endsWith('/publish')){const r=JSON.parse(o.body).commit;return json({seq:r.seq,hash:r.hash});}
 if(p==='/v1/version')return json({features:['env2','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);posts.push(o.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack'))return json(null,204);
 throw Error('Unexpected synthetic route '+p);
};
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom]){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch});
 e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);e.connected=false;pubs.set(address,e.pub);return e;
}
async function person(address){
 const e=await device(address);e.roster=await wire.newRoster(e.keys,address,address.split('/')[0]);e.me=await e.personRecord([e.roster],'self',null);
 chains.set(e.me.person,[JSON.parse(wire.rosterJSON(e.roster))]);
 await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);await caps(e);return e;
}
async function sibling(owner,address){
 const e=await device(address),join=await wire.joinConsent(e.keys,address,owner.me.person,owner.me.seq+1,owner.me.hash);
 const humans=[...await wire.rosterHumans(owner.roster),e.fp];
 const next=await wire.nextRoster(owner.keys,owner.address,owner.roster,[...owner.me.devices.map(d=>pubs.get(d.address)),e.pub],join,owner.roster.label,humans);
 await wire.verifyNext(next,owner.roster);
 const steps=[...await Promise.all(chains.get(owner.me.person).map(r=>wire.parseRoster(r))),next];
 owner.roster=next;owner.me=await owner.personRecord(steps,'self',owner.me);e.roster=next;e.me=await e.personRecord(steps,'self',null);
 chains.set(owner.me.person,steps.map(r=>JSON.parse(wire.rosterJSON(r))));
 await owner.store.write([{s:'kv',k:'person',v:owner.me}]);await owner.pinDevices(owner.me);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}}]);await e.pinDevices(e.me);await caps(e);await caps(owner);return e;
}
async function receive(raw,e){const env=wire.parseEnvelope(raw);await e.admit(raw,env);return env.id;}
async function rootCopies(e,conv){return(await e.store.all('outbox')).filter(r=>r.sub===wire.SubRootSync&&(!conv||r.conv===conv));}

// Exercise the producer with one genuine signed profile containing rd1+rd2 sessions.
let receiptSeq=0;
async function acknowledge(e,row){
 await e.post(row);const current=await e.store.get('outbox',row.id);
 check(['custody','delivered'].includes(current.state),'signed carrier handed to synthetic relay');
 await e.dispatch('receipt',JSON.stringify({id:row.id,state:'delivered',seq:++receiptSeq}));await e.readSyncRun;
}
async function drainAttention(e){
 for(let n=0;n<64;n++){
  await e.readSyncRun;
  const row=(await e.store.outboxSub(wire.SubReadSync)).find(r=>r.required_cap===wire.CapReadSyncV2&&['queued','waiting'].includes(r.state));
  if(!row)return;
  await acknowledge(e,row);
 }
 throw Error('bounded attention fixture drain exhausted');
}
{
 const c=await person('fair/desk'),d=await sibling(c,'fair/phone');
 await caps(d,[wire.CapEnv2,wire.CapPerson,wire.CapReadSyncV2,wire.CapRoom]);
 const current=profiles.get(d.address),oldSession=wire.newID(),oldCaps=JSON.parse(wire.capsJSON(await wire.newCaps(d.keys,d.address,oldSession,[wire.CapEnv2,wire.CapPerson,wire.CapReadSync,wire.CapRoom])));
 profiles.set(d.address,{...current,sessions:[...current.sessions,oldSession],caps:[...current.caps,oldCaps]});
 const refs=Array.from({length:2},()=>({conv:'c'.repeat(64),fingerprint:c.fp,lid:wire.newID()}));
 const mark=async(rev)=>c.store.write(refs.map(ref=>{const key=c.attentionKey(c.me.person,ref);return{s:'kv',k:key,v:{key,owner:c.me.person,ref,state:'unread',rev,writer:c.fp}};}));
 const rows=async()=> (await c.store.outboxSub(wire.SubReadSync)).filter(r=>r.required_cap===wire.CapReadSyncV2);
 await mark(1);await c.syncReadMarks();const first=(await rows())[0];
 await mark(2);await c.syncReadMarks();check((await rows()).length===1,'second producer before first handover keeps one queued carrier');
 await c.post(first);check((await c.store.get('outbox',first.id)).state==='waiting','true concurrent signed rd1 reader holds rd2');
 for(let rev=3;rev<=62;rev++){await mark(rev);await c.syncReadMarks();}
 check((await rows()).length===1,'60 subsequent generations coalesce behind one waiting carrier');
 check((await c.store.get('kv',c.attentionKey(c.me.person,refs[0]))).rev===62,'latest latent generation remains canonical');
 const legacy={...refs[0],lid:wire.newID()},key=c.readMarkKey(c.me.person,legacy);await c.store.write([{s:'kv',k:key,v:{owner:c.me.person,ref:legacy}}]);await c.syncReadMarks();
 const v1=(await c.store.outboxSub(wire.SubReadSync)).find(r=>r.required_cap===wire.CapReadSync);check(v1,'waiting attention leaves ordinary V1 production independent');await acknowledge(c,v1);
 const restart=new Engine({store:c.store,base:c.base,fetch});Object.assign(restart,{keys:c.keys,address:c.address,pub:c.pub,fp:c.fp,me:c.me,connected:false});await restart.syncReadMarks();check((await rows()).length===1,'restart preserves one pending ciphertext and latest register');
 profiles.set(d.address,{...current,sessions:[...current.sessions,oldSession],caps:[...current.caps,JSON.parse(wire.capsJSON(await wire.newCaps(d.keys,d.address,oldSession,[wire.CapEnv2,wire.CapPerson,wire.CapReadSyncV2,wire.CapRoom])))]});
 await acknowledge(c,await c.store.get('outbox',first.id));
 check((await c.store.get('outbox',first.id)).envelope===first.envelope,'upgrade releases same original ciphertext');
 let latest=(await rows()).find(r=>r.id!==first.id);check(latest&&wire.parseReadSync(latest.body).marks.every(m=>m.rev===62),'ordinary receipt wake produces latest without a user action');
 for(const reverse of [false,true]){
  const ref={...refs[0],lid:wire.newID()},old={...ref,rev:1,writer:c.fp,state:'unread'},newest={...old,rev:62,state:'read'},order=reverse?[newest,old]:[old,newest];
  for(const mark of order){const raw=await wire.seal({v:2,id:wire.newID(),from:c.address,to:d.address,ts:1,kind:'message',sub:wire.SubReadSync,replica:true,body:JSON.stringify({v:2,person:c.me.person,roster:c.me.hash,refs:[],marks:[mark]})},c.keys,d.pub);await receive(raw,d);}
  const got=await d.store.get('kv',d.attentionKey(d.me.person,ref));check(got.rev===62&&got.state==='read','fresh signed carriers converge in both arrival orders');
 }
 await acknowledge(c,latest);
 for(const state of ['failed','expired']){
  latest=(await rows()).at(-1);await c.store.write([{s:'outbox',k:latest.id,v:{...await c.store.get('outbox',latest.id),state}}]);const before=(await rows()).length;await c.syncReadMarks();check((await rows()).length===before+1,'terminal '+state+' permits reseal');await acknowledge(c,(await rows()).at(-1));
 }
 const bulk=Array.from({length:130},()=>({conv:refs[0].conv,fingerprint:c.fp,lid:wire.newID()}));
 await c.store.write(bulk.map(ref=>{const key=c.attentionKey(c.me.person,ref);return{s:'kv',k:key,v:{key,owner:c.me.person,ref,state:'unread',rev:1,writer:c.fp}};}));const before=(await rows()).length;
 await c.syncReadMarks();await drainAttention(c);const pages=(await rows()).slice(before);
 check(pages.length===3&&pages.map(r=>wire.parseReadSync(r.body).marks.length).join(',')==='64,64,2','healthy130 marks use three serialized bounded carriers');
 check((await Promise.all(pages.map(r=>c.store.get('outbox',r.id)))).every(r=>r.state==='delivered'),'all130 facts handed over and receipted');
 console.log('attention fairness',JSON.stringify({pending:1,generations:62,ordinaryV1:true,receiptWake:true,healthyPages:pages.map(r=>wire.parseReadSync(r.body).marks.length)}));
}

const vector={v:2,person:wire.newID(),roster:'a'.repeat(64),refs:[],marks:[{conv:'b'.repeat(64),fingerprint:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd',lid:wire.newID(),state:'unread',rev:1,writer:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'}]};
check(wire.parseReadSync(JSON.stringify(vector)).marks.length===1,'read sync V2 deliberate mark parses');
const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
await b.store.write([{s:'persons',k:a.me.person,v:{...a.me,state:'pinned'}},{s:'pins',k:a.address,v:{address:a.address,json:wire.marshalPublic(a.pub),fingerprint:a.fp,pending:null}}]);
await caps(a,[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSyncV2,wire.CapRoom]);await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSyncV2,wire.CapRoom]);
const conv=await a.newDM(b.address);for(const row of await rootCopies(a,conv))await receive(row.envelope,p);
await a.sendDM({conv,body:'Own sent deliberate row'});
const sent=(await a.store.byConv('outbox',conv)).find(m=>!m.sub);check(sent,'production send made canonical signed row');
await receive((await a.store.byConv('outbox',conv)).find(m=>!m.sub&&m.to===b.address).envelope,b);
const permuted=await a.readView();for(const rows of permuted.byConv.outbox.values())rows.reverse();check((await a.dm(conv,permuted)).messages.some(m=>m.id===sent.id),'canonical ID survives complete versus scoped row ordering');
const target=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===sent.id);check(target&&!target.unread,'sent row initially read');
await a.api('/api/act',{do:'unread',id:sent.id,ids:[sent.id]});await a.readSyncRun;await drainAttention(a);
let shown=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===sent.id);check(shown.unread&&shown.attention.rev===1,'exact outgoing unread action');
const firstGen={...shown.attention},copies=async e=>(await e.store.outboxSub(wire.SubReadSync)).filter(r=>wire.parseReadSync(r.body).v===2);
let carriers=await copies(a),carrier=carriers.find(r=>wire.parseReadSync(r.body).marks.some(m=>m.lid===sent.lid));check(carrier?.required_cap===wire.CapReadSyncV2&&!wire.parseEnvelope(carrier.envelope).attn,'quiet signed V2 producer uses rd2');
await a.post(carrier);await receive(carrier.envelope,p);await p.readSyncRun;await drainAttention(p);
check(!(await p.store.byConv('inbox',conv)).some(m=>!m.sub&&!m.control),'mark before original does not fetch or create history');
check((await p.store.prefix('kv','attention/'+p.me.person+'/')).length===1,'signed linked-own-human replica admitted latest unknown register');
await a.api('/api/act',{do:'read',ids:[sent.id]});check((await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===sent.id).unread,'legacy read does not clear deliberate outgoing attention');
await a.api('/api/act',{do:'read',ids:[sent.id],attention:{[sent.id]:firstGen}});await a.readSyncRun;await drainAttention(a);
shown=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===sent.id);check(!shown.unread&&!shown.attention,'matching full generation clears own outgoing attention');
const tombstone=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.state==='read'&&m.lid===sent.lid));await receive(tombstone.envelope,p);await receive(carrier.envelope,p);
const ref={conv,fingerprint:a.fp,lid:sent.lid},fact=()=>p.store.get('kv',p.attentionKey(p.me.person,ref));check((await fact()).state==='read'&&(await fact()).rev===2,'read tombstone survives delayed unread replay');
const seal=async(sender,marks,target=p,refs=[])=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:target.address,ts:1,kind:'message',sub:wire.SubReadSync,replica:true,body:JSON.stringify({v:2,person:a.me.person,roster:a.me.hash,refs,marks})},sender.keys,target.pub);
const low=[a.fp,p.fp].sort()[0],high=[a.fp,p.fp].sort().at(-1),winner={...ref,state:'unread',rev:7,writer:high};
await receive(await seal(a,[{...winner,writer:low,state:'read'}]),p);await receive(await seal(a,[winner]),p);check((await fact()).writer===high&&(await fact()).state==='unread','equal revision deterministic writer ordering');
await receive(await seal(a,[winner]),a); // self sender must be refused
await receive(await seal(p,[winner],a),a);await receive(await seal(p,[{...winner,writer:low,state:'read'}],a),a);check((await a.store.get('kv',a.attentionKey(a.me.person,ref))).writer===high,'opposite arrival order chooses same writer');
await a.api('/api/act',{do:'read',ids:[sent.id],attention:{[sent.id]:{rev:7,writer:low}}});check((await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===sent.id).unread,'equal revision different writer is stale CAS');
const bad=await receive(await seal(a,[{...winner,state:'read'}]),p);check((await p.store.get('held',bad)).reason==='conflicting_duplicate','exact generation other state quarantines');
const unknown={...ref,lid:wire.newID(),rev:1,writer:a.fp,state:'unread'},badBatch=await receive(await seal(a,[unknown,{...winner,state:'read'}]),p);check(!await p.store.get('kv',p.attentionKey(p.me.person,unknown)),'conflicting batch rolls back earlier unknown mark');
// True incoming production message, sibling topic, and exact snapshot semantics.
await b.sendDM({conv,body:'Incoming main'});let incoming=(await b.store.byConv('outbox',conv)).find(m=>!m.sub&&m.to===a.address);await receive(incoming.envelope,a);
await a.markRead([incoming.id]);await a.readSyncRun;await drainAttention(a);
await a.api('/api/act',{do:'unread',id:incoming.id,ids:[incoming.id]});await a.readSyncRun;await drainAttention(a);
check((await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===incoming.id).unread,'ordinary incoming can be deliberately unread after legacy read');
await b.sendDM({conv,body:'Sibling topic',topic:'new'});const siblingRow=(await b.store.byConv('outbox',conv)).find(m=>m.topic&&m.to===a.address);await receive(siblingRow.envelope,a);
await assert.rejects(()=>a.api('/api/act',{do:'unread',id:incoming.id,ids:[incoming.id,siblingRow.id]}),/topic/);checks++;
await assert.rejects(()=>a.api('/api/act',{do:'unread',id:incoming.lid,ids:[incoming.lid]}),/unavailable/);checks++;
const view=await a.api('/api/dm?id='+conv),main=view.main_topic;check(main?.unread===2,'Main count includes deliberate sent and incoming normal rows');
check((await a.overview()).dms.find(d=>d.id===conv).unread===3,'chat total includes Main and ordinary sibling');
// Full snapshot count does not depend on any rendered window; only specified IDs change.
const beforeSibling=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===siblingRow.id).attention;
// Admission already launched history production. Finish that captured-set
// change before this stationary action; authority-race refusal stays below.
await a.historyRun;await a.historyProduction;
await a.api('/api/act',{do:'unread',id:sent.id,ids:[sent.id,incoming.id]});await a.readSyncRun;await drainAttention(a);
check(!(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===siblingRow.id).attention&&!beforeSibling,'from-here exact IDs leave sibling untouched');
// Restart retries same ciphertext and latest per-recipient ledger without replay growth.
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});
const oldCarriers=await copies(a);await restarted.syncReadMarks();check((await copies(a)).length===oldCarriers.length,'restart does not create repeated generations');
await a.store.write([{s:'outbox',k:carrier.id,v:{...carrier,state:'queued'}}]);const offline=a.fetch;a.fetch=async()=>{throw new TypeError('Failed to fetch');};await a.post({...carrier,state:'queued'});check((await a.store.get('outbox',carrier.id)).envelope===carrier.envelope,'offline exact ciphertext retained');a.fetch=offline;
await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapReadSync,wire.CapRoom]);const postCount=posts.length;await a.post({...carrier,state:'queued'});check(posts.length===postCount&&(await a.store.get('outbox',carrier.id)).state==='waiting','rd1-only peer waits for marks without downgrade');await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapReadSyncV2,wire.CapRoom]);await a.post(await a.store.get('outbox',carrier.id));check(posts.length===postCount+1&&(await a.store.get('outbox',carrier.id)).envelope===carrier.envelope,'rd2 upgrade sends identical ciphertext');
// New 65+ facts use two bounded carriers. Latest-copy records stay one per ref/recipient.
const ops=[];for(let n=0;n<65;n++){const ref={conv,fingerprint:b.fp,lid:wire.newID()},key=a.attentionKey(a.me.person,ref);ops.push({s:'kv',k:key,v:{key,owner:a.me.person,ref,state:'unread',rev:1,writer:a.fp}});}await a.store.write(ops);const count=(await copies(a)).length;await a.syncReadMarks();await drainAttention(a);const added=(await copies(a)).slice(count);check(added.length===2&&added.every(r=>wire.parseReadSync(r.body).marks.length<=64),'65 attention facts use bounded 64-item production');
const held=added.find(r=>wire.parseReadSync(r.body).marks.length===64);await a.store.write([{s:'outbox',k:held.id,v:{...held,state:'quarantined'}}]);await a.syncReadMarks();await a.syncReadMarks();check((await copies(a)).length===count+2&&(await a.store.get('outbox',held.id)).envelope===held.envelope,'quarantine wake retains exact carrier without repeats');
const own=await p.store.get('kv','person'),pin=await p.store.get('pins',a.address);
for(const mode of ['agent-host','removed','frozen','pending-pin','changed-pin','revoked']){
 const changed=mode==='agent-host'?{...own,human_keys:own.human_keys.filter(fp=>fp!==a.fp)}:mode==='removed'?{...own,devices:own.devices.filter(d=>d.fingerprint!==a.fp)}:mode==='frozen'?{...own,state:'conflict'}:own;
 await p.store.write([{s:'kv',k:'person',v:changed},{s:'pins',k:a.address,v:mode==='pending-pin'?{...pin,pending:{fingerprint:b.fp}}:mode==='changed-pin'?{...pin,fingerprint:b.fp}:pin}]);
 if(mode==='revoked')await p.store.write([{s:'kv',k:'revoked-device/'+a.address+'/'+a.fp,v:{address:a.address,fingerprint:a.fp}}]);
 const denied=await receive(await seal(a,[{...unknown,lid:wire.newID()}]),p);check(!!await p.store.get('held',denied),'signed admission refuses '+mode);
 await p.store.write([{s:'kv',k:'person',v:own},{s:'pins',k:a.address,v:pin},{s:'kv',k:'revoked-device/'+a.address+'/'+a.fp}]);
}
// A delayed ordinary signed V1 read cannot erase deliberate attention.
const incomingMark=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.lid===incoming.lid&&m.state==='unread'));
const legacyRead=(await a.store.outboxSub(wire.SubReadSync)).find(r=>wire.parseReadSync(r.body).v===1&&wire.parseReadSync(r.body).refs.some(ref=>ref.lid===incoming.lid));
await receive(incomingMark.envelope,p);await receive(legacyRead.envelope,p);
const phoneIncoming=await wire.open(incoming.envelope,a.keys,a.address,b.pub);await receive(await wire.seal({...phoneIncoming,to:p.address,id:wire.newID()},b.keys,p.pub),p);
const phoneIncomingView=(await p.api('/api/dm?id='+conv)).messages.find(m=>m.lid===incoming.lid);check(phoneIncomingView.unread&&phoneIncomingView.attention,'delayed V1 and later original preserve remote deliberate unread');
// Shared unread provider is warmed before a genuine signed linked read arrives.
const linkedBefore=await p.overview(false),linkedIndex=p.unreadIndex;
check((await p.api('/api/unread')).rows.some(r=>r.conv===conv&&r.lid===incoming.lid),'linked unread is in compact Review index');
const localIncoming=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.lid===incoming.lid);
await a.api('/api/act',{do:'read',ids:[localIncoming.id],attention:{[localIncoming.id]:localIncoming.attention}});await a.readSyncRun;await drainAttention(a);
const linkedRead=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.lid===incoming.lid&&m.state==='read'));
check(linkedRead,'actual signed linked read produced');await receive(linkedRead.envelope,p);
const linkedAfter=await p.overview(false);
check(linkedAfter.unread_total===linkedBefore.unread_total-1&&p.unreadIndex!==linkedIndex,'signed linked read invalidates totals and compact index');
check(!(await p.api('/api/unread')).rows.some(r=>r.conv===conv&&r.lid===incoming.lid),'linked read removes only its effective reference');
// Retain this fixture's later stale-clear race input with a fresh local mark.
await a.api('/api/act',{do:'unread',id:localIncoming.id,ids:[localIncoming.id]});await a.readSyncRun;await drainAttention(a);

// A real signed one-person group with two current linked human devices.
a.realm=p.realm=wire.newID();const group=await a.createGroup('Attention group');
for(const row of await a.store.byConv('outbox',group))if([wire.SubGroupProof,wire.SubGroupContext].includes(row.sub)){for(const f of row.files||[])blobs.set(f.attachment.blob.id,f.ct);await receive(row.envelope,p);}
await a.sendDM({conv:group,body:'Group sent row'});
const groupSent=(await a.api('/api/dm?id='+group)).messages.find(m=>!m.event);check(groupSent,'production group has canonical normal row');
for(const row of await a.store.byConv('outbox',group))if(!row.sub)await receive(row.envelope,p);
await a.api('/api/act',{do:'unread',id:groupSent.id,ids:[groupSent.id]});await a.readSyncRun;await drainAttention(a);
check((await a.api('/api/dm?id='+group)).messages.find(m=>m.id===groupSent.id).unread&&(await a.overview()).dms.find(m=>m.id===group).unread===1,'group row and total include deliberately unread outgoing');
for(const row of await copies(a))if(wire.parseReadSync(row.body).marks.some(m=>m.conv===group))await receive(row.envelope,p);
check((await p.api('/api/dm?id='+group)).messages.some(m=>m.lid===groupSent.lid&&m.unread&&m.attention),'linked group replica projects exact signed attention');
// A production signed file-only turn is a normal attention row.
await b.sendDM({conv,body:'',files:[{name:'one.txt',bytes:new Uint8Array([65])}]});
const fileRow=(await b.store.byConv('outbox',conv)).find(m=>m.to===a.address&&m.attachments?.length);await receive(fileRow.envelope,a);
await a.api('/api/act',{do:'unread',id:fileRow.id,ids:[fileRow.id]});await a.readSyncRun;await drainAttention(a);
check((await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===fileRow.id).unread,'file-only row deliberate unread');
// Direct own-agent/native-thread normal rows retain exact author and scope.
const directID=wire.newID(),directEnvelope=await wire.seal({v:1,id:directID,from:b.address,to:a.address,ts:1,kind:'answer',body:'Native agent answer'},b.keys,a.pub);await receive(directEnvelope,a);
await a.markRead([directID]);await a.readSyncRun;await drainAttention(a);await a.api('/api/act',{do:'unread',id:directID,ids:[directID]});await a.readSyncRun;await drainAttention(a);
check((await a.thread(directID)).messages.find(m=>m.id===directID).unread&&(await a.threadSummaries()).find(t=>t.id===directID).unread===1,'direct thread row and topic summary effective attention');
await a.changeTopic('archive',{peer:b.address,id:directID});if(a.topicMarkSyncRun)await a.topicMarkSyncRun;
const archived=await a.topicOverview(false);check(!archived.threads.some(t=>t.id===directID)&&archived.topics.find(t=>t.peer===b.address).archived_unread===1,'archived aggregate retains deliberate unread');
await assert.rejects(()=>a.api('/api/act',{do:'read',ids:[directID],attention:{[directID]:{rev:1}}}),/generation/);checks++;
await assert.rejects(()=>a.api('/api/act',{do:'read',ids:[],attention:{[directID]:{rev:1,writer:a.fp}}}),/generation/);checks++;
await assert.rejects(()=>a.api('/api/act',{do:'unread',id:wire.newID(),ids:[wire.newID()]}),/Choose/);checks++;
// A selected granted excerpt remains read-only while accepting personal attention.
const {world}=await import('./human_engine_check.mjs');
{
 const w=await world(),owner=w.alice,guest=w.carol;
 const hidden=await owner.sendDM({conv:w.conv,body:'Hidden original'}),selected=await owner.sendDM({conv:w.conv,body:'Visible selected original'});await w.drain(w.bob);
 const invitation=await owner.inviteAgent({conv:w.conv,host:guest.address,agent_id:w.agent.id,share:[selected.id]});await w.drain(guest);
 let excerpt=(await guest.api('/api/dm?id='+w.conv)).messages.find(m=>m.excerpt_pid===invitation.pid);check(excerpt&&excerpt.can.length===0,'signed exact grant yields visible immutable excerpt');
 await guest.api('/api/act',{do:'unread',id:excerpt.id,ids:[excerpt.id]});await guest.readSyncRun;
 excerpt=(await guest.api('/api/dm?id='+w.conv)).messages.find(m=>m.id===excerpt.id);check(excerpt.unread&&excerpt.attention&&excerpt.can.length===0,'visible read-only excerpt accepts personal unread without edit/react permission');
 const originalRef={conv:w.conv,fingerprint:owner.fp,lid:selected.lid},mark=await guest.store.get('kv',guest.attentionKey(guest.me.person,originalRef));check(mark?.state==='unread','excerpt register preserves exact original author and logical identity');
 await assert.rejects(()=>guest.api('/api/act',{do:'unread',id:excerpt.id,ids:[excerpt.id,hidden.id]}),/unavailable/);checks++;
 await guest.api('/api/act',{do:'read',ids:[excerpt.id],attention:{[excerpt.id]:excerpt.attention}});check(!(await guest.api('/api/dm?id='+w.conv)).messages.find(m=>m.id===excerpt.id).unread,'excerpt exact generation read CAS');
}
// Concurrent authority loss cannot partially commit an exact snapshot action.
{
 const before=new Map((await a.store.prefix('kv','attention/'+a.me.person+'/')).map(m=>[m.key,JSON.stringify(m)])),write=a.store.write.bind(a.store);let race=true;
 a.store.write=async(ops,guards)=>{if(race&&ops.some(o=>o.k.startsWith('attention/')&&o.v?.state==='unread')){race=false;await write([{s:'kv',k:'person',v:{...a.me,state:'conflict'}}]);}return write(ops,guards);};
 await assert.rejects(()=>a.api('/api/act',{do:'unread',id:sent.id,ids:[sent.id,incoming.id]}),/changed/);checks++;a.store.write=write;
 check((await a.store.prefix('kv','attention/'+a.me.person+'/')).every(m=>before.get(m.key)===JSON.stringify(m)),'authority race writes no partial attention generation');await a.store.write([{s:'kv',k:'person',v:a.me}]);
}
// T3: two valid local actions against the same generation converge after delivery.
const sentToPhone=(await a.store.byConv('outbox',conv)).find(m=>!m.sub&&m.lid===sent.lid&&m.to===p.address);await receive(sentToPhone.envelope,p);
const phoneID=(await p.api('/api/dm?id='+conv)).messages.find(m=>m.lid===sent.lid).id,common={...ref,rev:100,writer:a.fp,state:'unread'};
await receive(await seal(a,[common]),p);await receive(await seal(p,[common],a),a);
await Promise.all([a.api('/api/act',{do:'read',ids:[sent.id],attention:{[sent.id]:{rev:100,writer:a.fp}}}),p.api('/api/act',{do:'unread',id:phoneID,ids:[phoneID]})]);await Promise.all([a.readSyncRun,p.readSyncRun]);await drainAttention(a);await drainAttention(p);
const a101=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.conv===conv&&m.lid===sent.lid&&m.rev===101)),p101=(await copies(p)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.conv===conv&&m.lid===sent.lid&&m.rev===101));
check(a101&&p101,'both concurrent local actions produce a new generation');await receive(a101.envelope,p);await receive(p101.envelope,a);await Promise.all([a.readSyncRun,p.readSyncRun]);await drainAttention(a);await drainAttention(p);
const winnerState=a.fp>p.fp?'read':'unread',am=await a.store.get('kv',a.attentionKey(a.me.person,ref)),pm=await fact();check(am.rev===101&&pm.rev===101&&am.writer===high&&pm.writer===high&&am.state===winnerState&&pm.state===winnerState,'true read/unread concurrency converges by full generation order');
// A queued stale clear racing a newer register retries without erasing it.
{
 const row=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===incoming.id),generation=row.attention,key=a.attentionKey(a.me.person,{conv,fingerprint:b.fp,lid:incoming.lid}),old=await a.store.get('kv',key),write=a.store.write.bind(a.store);let race=true;
 a.store.write=async(ops,guards)=>{if(race&&ops.some(o=>o.k===key&&o.v?.state==='read')){race=false;await write([{s:'kv',k:key,v:{...old,rev:old.rev+1,writer:p.fp}}]);}return write(ops,guards);};
 await a.api('/api/act',{do:'read',ids:[incoming.id],attention:{[incoming.id]:generation}});a.store.write=write;await a.readSyncRun;await drainAttention(a);
 const after=await a.store.get('kv',key);check(after.state==='unread'&&after.rev===old.rev+1&&after.writer===p.fp,'newer concurrent register survives delayed read CAS');
}
// One atomic action accepts 65 actual admitted normal rows, independently of DOM.
{
 const ids=[];
 for(let n=0;n<65;n++){
  const sent=await b.sendDM({conv,body:'Atomic admitted batch '+n}),row=(await b.store.byConv('outbox',conv)).find(m=>m.lid===sent.lid&&m.to===a.address);await receive(row.envelope,a);ids.push(row.id);
 }
 const before=(await copies(a)).length;await a.api('/api/act',{do:'unread',id:ids[0],ids});await a.readSyncRun;await drainAttention(a);
 const admitted=(await a.api('/api/dm?id='+conv)).messages.filter(m=>ids.includes(m.id));check(admitted.length===65&&admitted.every(m=>m.unread&&m.attention?.rev===1),'65 exact admitted rows commit deliberate unread atomically');
 const produced=(await copies(a)).slice(before);check(produced.length===2&&produced.every(r=>wire.parseReadSync(r.body).marks.length<=64),'65-row action schedules two bounded signed carriers');
}
// Attention contention must defer only CAS, preserving legacy reads in a mixed batch.
{
 const sent=await b.sendDM({conv,body:'Ordinary unread during attention contention'}),ordinary=(await b.store.byConv('outbox',conv)).find(m=>m.lid===sent.lid&&m.to===a.address);await receive(ordinary.envelope,a);
 const ordinaryRef={conv,fingerprint:b.fp,lid:ordinary.lid},ordinaryKey=a.readMarkKey(a.me.person,ordinaryRef);
 check(!(await a.store.get('inbox',ordinary.id)).read&&!await a.store.get('kv',ordinaryKey),'contention probe begins with an unmarked unread row and no legacy read fact');
 const marked=await a.store.get('inbox',incoming.id);await a.store.write([{s:'inbox',k:incoming.id,v:{...marked,read:false}}]);
 const shown=(await a.api('/api/dm?id='+conv)).messages.find(m=>m.id===incoming.id),generation=shown.attention,key=a.attentionKey(a.me.person,{conv,fingerprint:b.fp,lid:incoming.lid}),before=await a.store.get('kv',key),write=a.store.write.bind(a.store);let attempts=0,failure='';
 a.store.write=async(ops,guards)=>{if(ops.some(o=>o.k===key&&o.v?.state==='read')){attempts++;const row=await a.store.get('inbox',incoming.id);await write([{s:'inbox',k:row.id,v:{...row,read:!row.read}}]);}return write(ops,guards);};
 try{await a.api('/api/act',{do:'read',ids:[incoming.id,ordinary.id],attention:{[incoming.id]:generation}});}catch(e){failure=e.message;}finally{a.store.write=write;}
 const outcome={attempts,error:failure,registerUnchanged:JSON.stringify(await a.store.get('kv',key))===JSON.stringify(before),markedLegacyRead:!!(await a.store.get('inbox',incoming.id)).read,ordinaryLegacyRead:!!(await a.store.get('inbox',ordinary.id)).read,ordinaryLegacyMark:JSON.stringify((await a.store.get('kv',ordinaryKey))?.ref)===JSON.stringify(ordinaryRef)};
 console.log('mixed attention contention',JSON.stringify(outcome));
 check(!failure&&attempts===4&&outcome.registerUnchanged,'attention CAS stops after four conflicts, defers without error and leaves its register byte-identical');
 check(outcome.markedLegacyRead&&outcome.ordinaryLegacyRead&&outcome.ordinaryLegacyMark,'mixed contention still acknowledges both inbox rows and creates the ordinary legacy read fact');
}
// Cold pressure cannot consume state capacity; state pressure remains bounded
// and resumes the same pending attention generation after restart.
{
 const reservedRef={conv,fingerprint:b.fp,lid:wire.newID()},reservedKey=a.attentionKey(a.me.person,reservedRef),cold=Array.from({length:50},()=>{const id=wire.newID();return {s:'outbox',k:id,v:{id,to:p.address,recipient_fp:p.fp,sub:'history',aside:true,state:'custody'}};});
 await a.store.write([...cold,{s:'kv',k:reservedKey,v:{key:reservedKey,owner:a.me.person,ref:reservedRef,state:'unread',rev:1,writer:a.fp}}]);
 await a.syncReadMarks();
 const reserved=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.lid===reservedRef.lid));
 check(reserved,'full cold window leaves capacity for the exact attention generation');
 await receive(reserved.envelope,p);
 check((await p.store.get('kv',p.attentionKey(p.me.person,reservedRef)))?.state==='unread','quiet encrypted attention converges while cold history stays in custody');
 await acknowledge(a,reserved);
 const blockedRef={conv,fingerprint:b.fp,lid:wire.newID()},key=a.attentionKey(a.me.person,blockedRef),fill=Array.from({length:50},()=>{const id=wire.newID();return {s:'outbox',k:id,v:{id,to:p.address,recipient_fp:p.fp,sub:wire.SubRootSync,aside:true,state:'custody'}};});
 await a.store.write([...fill,{s:'kv',k:key,v:{key,owner:a.me.person,ref:blockedRef,state:'unread',rev:1,writer:a.fp}}]);const before=(await copies(a)).length;
 await a.syncReadMarks();check((await copies(a)).length===before,'full state window defers new attention without growing the queue');
 const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});await restarted.syncReadMarks();check((await copies(a)).length===before,'restart preserves state backpressure and the pending generation');
 await a.store.write([...fill,...cold].map(o=>({...o,v:{...o.v,state:'delivered'}})));await restarted.syncReadMarks();
 const resumed=(await copies(a)).find(r=>wire.parseReadSync(r.body).marks.some(m=>m.lid===blockedRef.lid));check(resumed,'state capacity recovery resumes the exact pending mark');await restarted.syncReadMarks();check((await copies(a)).length===before+1,'window recovery never repeats generation');
 const latest=await a.store.prefix('kv','attention/'+a.me.person+'/'),ledger=await a.store.prefix('kv','attention-copy/'+a.me.person+'/'+p.fp+'/');check(ledger.length<=latest.length,'copy ledger bounded by latest refs per recipient');
}
// Actual direct producer stores the recipient fp; attention keeps the author fp.
const ownDirect=await a.sendV1({to:b.address,kind:'question',body:'Own direct question',files:[]});await a.api('/api/act',{do:'unread',id:ownDirect.id,ids:[ownDirect.id]});await a.readSyncRun;await drainAttention(a);
let ownDirectView=(await a.thread(ownDirect.id)).messages.find(m=>m.id===ownDirect.id);check(ownDirectView.unread&&ownDirectView.attention,'actual outgoing direct row resolves local author despite recipient fp metadata');
await a.api('/api/act',{do:'read',ids:[ownDirect.id],attention:{[ownDirect.id]:ownDirectView.attention}});await a.readSyncRun;await drainAttention(a);check(!(await a.thread(ownDirect.id)).messages.find(m=>m.id===ownDirect.id).unread,'outgoing direct exact generation clears');
// Provider/replica operations must not scan all history once per reference.
await a.readSyncRun;const cost={},snapshots={},originals={},titleBatches=[];
// This fixture has one named DM topic and a group containing Main only.
const titleTopics=new Map([[conv,[siblingRow.topic]],[group,[]]]),titleKeys=new Map();
for(const [id,topics] of titleTopics){
 const main=wire.hex(await wire.sha256(new TextEncoder().encode('agentnet/main-flow-preference/v1\0'+id)));
 titleKeys.set(id,[...topics.map(t=>'topic/'+id+'/'+t),'topic/'+main+'/'+('0'.repeat(32)),...topics.map(t=>'topic-shared-title/'+id+'/'+t)].sort());
}
const bump=(key,n=1)=>cost[key]=(cost[key]||0)+n;
const failureCost=()=>console.log('attention namespace cost at failure',JSON.stringify(cost));
process.on('uncaughtExceptionMonitor',failureCost);
for(const name of ['all','prefix','byConv','get','getMany']){
 originals[name]=a.store[name];a.store[name]=async(...args)=>{
  const key=name+':'+args[0]+(name==='prefix'?':'+args[1].split('/')[0]:'');bump(key);
  const result=await originals[name](...args);
  if(name==='prefix'||name==='byConv')bump('rows:'+key,result.length);
  if(name==='getMany'){
   const keys=args[1],kinds=new Set(keys.map(k=>k.startsWith('attention/'+a.me.person+'/')?'attention':k.startsWith('topic/')||k.startsWith('topic-shared-title/')?'titles':'unexpected'));
   const kind=args[0]==='kv'&&keys.length&&kinds.size===1?[...kinds][0]:'unexpected',loaded=result.filter(Boolean).length;
   bump('batches');bump('requested',keys.length);bump('loaded',loaded);
   bump(kind+'Batches');bump(kind+'Requested',keys.length);bump(kind+'Loaded',loaded);
   if(kind==='titles')titleBatches.push([...keys].sort());
   if(kind==='unexpected')cost.unexpectedNamespaces=[...(cost.unexpectedNamespaces||[]),[...kinds].sort()];
  }
  return result;
 };
}
const reset=()=>{for(const k of Object.keys(cost))delete cost[k];titleBatches.length=0;};
const capture=name=>{
 snapshots[name]={...cost};console.log('attention namespace cost',name,JSON.stringify(snapshots[name]));
 check(!cost.unexpectedBatches,'no unexpected, empty or mixed namespace batch is hidden');
 check((cost.batches||0)===(cost.attentionBatches||0)+(cost.titlesBatches||0),'total batch count includes attention and title work');
 check((cost.requested||0)===(cost.attentionRequested||0)+(cost.titlesRequested||0)&&(cost.loaded||0)===(cost.attentionLoaded||0)+(cost.titlesLoaded||0),'total requested/loaded keys include both namespaces');
};
const checkTitles=ids=>{
 check(titleBatches.length===ids.length,'one title batch per expected conversation');
 for(const id of ids)check(titleKeys.get(id).length===2*titleTopics.get(id).length+1,'private/shared/Main title key bound');
 const ordered=rows=>rows.map(keys=>JSON.stringify(keys)).sort();
 assert.deepEqual(ordered(titleBatches),ordered(ids.map(id=>titleKeys.get(id))),'exact known private/shared/Main title keysets, without unrelated or repeated keys');checks++;
};
const measuredDM=await a.api('/api/dm?id='+conv);capture('dm');check(cost.attentionBatches===1,'one attention register snapshot for complete DM/Main/topics operation');checkTitles([conv]);reset();
const observed=measuredDM.messages.find(m=>m.id===incoming.id).attention;await a.markRead([incoming.id],{[incoming.id]:{rev:observed.rev,writer:observed.writer===a.fp?p.fp:a.fp}});await a.readSyncRun;capture('singleChatCAS');check(cost.attentionBatches===1&&!cost['all:inbox']&&!cost['all:outbox'],'single-chat attention CAS reads only its conversation snapshot');checkTitles([conv]);reset();
await a.thread(directID);capture('direct');check(cost.attentionBatches===1&&cost.attentionRequested===1&&cost.attentionLoaded===1,'direct view loads only its admitted ref instead of all owner attention');checkTitles([]);reset();
await a.overview();capture('overview');check(cost.attentionBatches===1,'one attention register snapshot shared by overview/group/threads/counts');checkTitles([conv,group]);reset();
const refs=Array.from({length:64},()=>({...ref,lid:wire.newID(),state:'unread',rev:1,writer:p.fp}));await receive(await seal(p,refs,a),a);await a.readSyncRun;capture('admit64');
check(!cost['all:kv']&&(cost['all:inbox']||0)<=2,'64 marks do not rescan all inbox/KV per reference');reset();
await a.syncReadMarks();capture('sync');check(!cost['all:kv']&&!cost['all:inbox'],'recovery wake reuses latest/copy prefixes without whole-history scans');
for(const name of Object.keys(originals))a.store[name]=originals[name];
process.removeListener('uncaughtExceptionMonitor',failureCost);
// Real published v31 parser/caps snapshot, optional outside the release packet.
const {readFile}=await import('node:fs/promises'),{createHash}=await import('node:crypto');
let compatibility='not executed: AGENTNET_COMPAT_V31_WIRE unset';
if(process.env.AGENTNET_COMPAT_V31_WIRE){
 const original=await readFile(process.env.AGENTNET_COMPAT_V31_WIRE,'utf8');check(createHash('sha256').update(original).digest('hex')==='221482a6cee99dbb548b979ef9e87b0372d739b113603e99e9792f7c8d3a57f6','published v31 wire bytes unchanged');
 const imported=original.replace('./pictures.mjs',new URL('../static/pictures.mjs',import.meta.url).href).replace('./vendor/age.mjs',new URL('../static/vendor/age.mjs',import.meta.url).href),old=await import('data:text/javascript;base64,'+Buffer.from(imported).toString('base64'));
 const legacy=(await a.store.outboxSub(wire.SubReadSync)).find(r=>wire.parseReadSync(r.body).v===1);check(legacy&&JSON.stringify(old.parseReadSync(legacy.body))===JSON.stringify(wire.parseReadSync(legacy.body)),'actual current ordinary V1 carrier parses identically with v31');
 const r=wire.parseReadSync(legacy.body);check(legacy.body===JSON.stringify({v:1,person:r.person,roster:r.roster,refs:r.refs}),'ordinary V1 producer bytes and required fields unchanged');
 assert.throws(()=>old.parseReadSync(carrier.body));checks++;
 check(!old.capsReads({caps:[wire.CapReadSyncV2]},wire.CapReadSync)&&wire.capsReads({caps:[wire.CapReadSyncV2]},wire.CapReadSync),'v31 capability availability limit and current rd2 implies rd1');
 compatibility='published v0.8.31 1894c6cd parser/caps executed; dependency import URLs only rewritten';
}
const advertisement=Engine.prototype.onConnect.toString().match(/wire\.newCaps\(this\.keys, this\.address, this\.session, (\[.*\])\)\)/)[1],advertised=Function('wire','f','return '+advertisement)(wire,['signals1']);
check(wire.MaxAdvertisedCaps===29&&wire.MaxCaps===32&&advertised.length===wire.MaxAdvertisedCaps&&new Set(advertised).size===wire.MaxAdvertisedCaps&&advertised.includes(wire.CapSharedTopicTitle)&&advertised.includes(wire.CapTopicOrganization)&&advertised.includes(wire.CapRequestSteer)&&!advertised.includes(wire.CapRequestFollowup)&&advertised.includes(wire.CapReadSyncV2)&&!advertised.includes(wire.CapReadSync),'actual browser advertisement keeps ttl1/org1 and strict flw2/rd2 replacements within the reviewed29 slots');
await wire.newCaps(a.keys,a.address,wire.newID(),advertised);
console.log('attention browser checks',checks,JSON.stringify({cost:snapshots,carriers:(await copies(a)).length,compatibility,advertised:advertised.length}));
