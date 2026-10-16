// Actual signed Engine sends in the existing isolated world. No relay/model/browser.
import {assert,fixtureAssertionCount,world,id,now} from './human_engine_check.mjs';
import {Engine,HubError,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let passed=0;
const fixtureStores=[];let reopens=0;
const store=typeof process!=='undefined'?undefined:async()=>{const name='schedule-'+wire.newID(),s=await openIDB(name);s.fixtureStoreName=name;fixtureStores.push(s);return s;};
const caps=[wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapConversationAudience,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom,wire.CapTopicParticipation,wire.CapTopicContext,wire.CapSharedTopicTitle];
async function setup(){const w=await world({store});let clock=now;w.clock=()=>clock;w.at=t=>{clock=t;};w.ready=async()=>{for(const e of w.users){e.now=()=>clock;e.realm='f'.repeat(32);e.featureList=null;const f=e.fetch;e.fetch=async(url,o)=>new URL(url).pathname==='/v1/version'?new Response(JSON.stringify({features:['env2','env3','person2','caps','notify1'],realm_id:'f'.repeat(32)})):f(url,o);await w.caps(e,caps);}};await w.ready();return w;}
const input=(w,conv=w.conv,extra={})=>({conv,body:'Scheduled ordinary message',due:new Date(w.clock()+10000).toISOString(),timezone:'Europe/Kyiv',...extra});
const copies=async(e,s)=>(await e.store.byConv('outbox',s.conv)).filter(r=>r.lid===s.id);
async function test(name,fn){await fn();passed++;console.log('PASS '+name);}
async function drained(e, limit = 8) {
  for (let i = 0; i < limit; i++) { const d = e.scheduledDrain; if (!d) return; await d; }
  assert.equal(e.scheduledDrain ?? null, null, 'scheduler did not settle within ' + limit + ' drains');
}
async function restart(e){if(e.fixtureStoreName){e.store.close();Object.assign(e.store,await openIDB(e.fixtureStoreName));reopens++;}const n=new Engine({store:e.store,base:e.base,fetch:e.fetch,now:e.now});for(const k of ['keys','address','pub','fp','me','roster','realm','fixtureStoreName'])n[k]=e[k];if(n.fixtureStoreName){n.keys=(await n.store.get('kv','identity')).keys;assert.deepEqual(await wire.publicEntry(n.keys,n.address),n.pub,'reopened persisted CryptoKeys retain exact identity');}return n;}
async function group(w){const {alice:a,bob:b}=w,conv=await a.createGroup('Scheduled group'),inv=await a.inviteGroup({conv,person:b.me.person});await w.drain(b);await b.retryHeld();await b.decideGroup({id:inv.id,accept:true});await w.drain(a);if(a.recoveringGroups)await a.recoveringGroups;await a.recoverGroupIntents();await w.drain(b);await b.retryHeld();return conv;}
async function guest(w,conv,topic){const {alice:a,bob:b,carol:g}=w,p=await a.changeHuman('invite',{conv,host:g.address,...(topic?{topic}:{})});await w.drain(g);await w.drain(b);await g.changeHuman('decide',{pid:p.pid,accept:true});await w.drain(a);await w.drain(b);return p;}
await test('schedule reserves its ID from ordinary sends and other hooks; dispatch exemption is call scoped',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),send=a.sendDM.bind(a);
 const assertEmpty=async()=>{assert.equal((await copies(a,s)).length,0);assert.equal((await a.store.byConv('inbox',s.conv)).filter(r=>r.lid===s.id).length,0);};
 for(const beforeStore of [undefined,async()=>[]]){
  await assert.rejects(()=>send({id:s.id,conv:s.conv,body:'Unrelated ordinary body',beforeStore}));
  await assertEmpty();assert.equal((await a.scheduledSend(s.id)).state,'pending');
 }
 const unrelated=id();await send({id:unrelated,conv:s.conv,body:'Ordinary unrelated ID'});assert.equal((await a.store.byConv('outbox',s.conv)).filter(r=>r.lid===unrelated).length,1);
 let overlapped=false;a.sendDM=async fields=>{
  if(fields.id===s.id){overlapped=true;for(const beforeStore of [undefined,async()=>[]])await assert.rejects(()=>send({id:s.id,conv:s.conv,body:'Concurrent ordinary body',beforeStore}));await assertEmpty();}
  return send(fields);
 };
 w.at(Date.parse(s.due));assert.equal((await a.dispatchScheduledSend(s.id)).state,'sent');assert.ok(overlapped);
 const rows=await copies(a,s);assert.equal(rows.length,1);assert.equal(rows[0].body,s.body);assert.equal((await wire.open(rows[0].envelope,w.bob.keys,w.bob.address,a.pub)).body,s.body);
 const saved=await a.scheduledSend(s.id);await a.store.write([{s:'kv',k:a.scheduledKey(s.id),v:{...saved,state:'dispatching'}}]);
 const recovered=await restart(a);recovered.sendDM=async()=>{throw Error('must not reseal');};assert.equal((await recovered.dispatchScheduledSend(s.id)).state,'sent');assert.deepEqual(await copies(a,s),rows);
});
await test('ordinary checked write refuses a schedule reserved after its absence read',async()=>{
 const w=await setup(),a=w.alice,lid=id(),write=a.store.write.bind(a.store);let reserved=false,schedule;
 a.store.write=async(ops,checks)=>{
  if(!reserved&&ops.some(o=>o.s==='outbox'&&o.v?.lid===lid)){
   reserved=true;schedule=await a.scheduleSend(input(w,w.conv,{id:lid}));
   assert.ok(checks.some(c=>c.s==='kv'&&c.k===a.scheduledKey(lid)&&c.v===undefined),'reservation absence participates in the ordinary write');
  }
  return write(ops,checks);
 };
 await assert.rejects(()=>a.sendDM({id:lid,conv:w.conv,body:'Planned before reservation'}));assert.ok(reserved);assert.equal((await a.scheduledSend(lid)).state,'pending');assert.equal((await copies(a,schedule)).length,0);assert.equal((await a.store.byConv('inbox',w.conv)).filter(r=>r.lid===lid).length,0);
});
await test('stable create ID, identical retry and conflict before new audience capture',async()=>{
 const w=await setup(),a=w.alice,request=input(w,w.conv,{id:id()}),s=await a.scheduleSend(request);assert.equal(s.id,request.id);
 const capture=a.scheduledAudience;a.scheduledAudience=()=>{throw Error('retry must not capture current audience');};
 assert.deepEqual(await a.scheduleSend(request),s);await assert.rejects(()=>a.scheduleSend({...request,body:'different'}),e=>e.code==='scheduled_id_conflict');a.scheduledAudience=capture;
});
await test('stable retry after dispatch/cancel, file digest conflicts, concurrent create and ID guards',async()=>{
 const w=await setup(),a=w.alice,bytes=new Uint8Array([1,2,3]),file={name:'same.bin',size:3,bytes},request=input(w,w.conv,{id:id(),files:[file]}),s=await a.scheduleSend(request);
 const changed=a.changed.bind(a);let pushes=0;a.changed=(...v)=>{pushes++;return changed(...v);};
 for(const extra of [{body:'different'},{due:new Date(Date.parse(s.due)+1).toISOString()},{topic:id()},{files:[]},{files:[{...file,name:'other.bin'}]},{files:[{...file,bytes:new Uint8Array([3,2,1])}]}])await assert.rejects(()=>a.scheduleSend({...request,...extra}),e=>e.code==='scheduled_id_conflict');
 assert.equal(pushes,0);assert.deepEqual(await a.scheduledSend(s.id),s);
 await assert.rejects(()=>a.editScheduledSend(s.id,s.rev,{...request,id:id()}),e=>e.code==='scheduled_id_conflict');
 for(const invalid of ['BAD','A'.repeat(32),'a'.repeat(64),0])await assert.rejects(()=>a.scheduleSend({...request,id:invalid}));
 w.at(Date.parse(s.due));const sent=await a.dispatchScheduledSend(s.id),count=(await copies(a,s)).length;assert.equal(sent.state,'sent');pushes=0;
 const capture=a.scheduledAudience;a.scheduledAudience=()=>{throw Error('terminal retry cannot replan audience');};assert.deepEqual(await a.scheduleSend(request),sent);assert.equal(pushes,0);assert.equal((await copies(a,s)).length,count);a.scheduledAudience=capture;
 const pending=input(w,w.conv,{id:id()}),[one,two]=await Promise.all([a.scheduleSend(pending),a.scheduleSend(pending)]);assert.deepEqual(one,two);assert.equal(one.id,pending.id);
 const cancelled=await a.cancelScheduledSend(one.id,one.rev);w.at(Date.parse(one.due)+1);assert.deepEqual(await a.scheduleSend(pending),cancelled);
 const ordinary=id();await a.sendDM({id:ordinary,conv:w.conv,body:'Ordinary existing LID'});await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{id:ordinary})),e=>e.code==='scheduled_id_conflict');
});
await test('real group context pending stays pending; a forged error label stays review',async()=>{
 const w=await setup(),a=w.alice,conv=await group(w),s=await a.scheduleSend(input(w,conv)),g=await a.store.get('kv','group/'+conv),packet=wire.parseGroupContext(g.context);
 await a.noteGroupHead({conv,bootstrap:packet.root.creator.fingerprint,seq:packet.state.seq+1,hash:'a'.repeat(64)});w.at(Date.parse(s.due));const waiting=await a.dispatchScheduledSend(s.id);assert.equal(waiting.state,'pending');assert.equal(waiting.detail,'Waiting for connectivity or current chat information.');assert.equal((await copies(a,s)).length,0);
 const dm=await a.scheduleSend(input(w));a.sendDM=async()=>{throw Object.assign(Error('not an authenticated transient error'),{reason:'proof_pending',status:0});};w.at(Date.parse(dm.due));assert.equal((await a.dispatchScheduledSend(dm.id)).state,'needs_review');assert.equal((await copies(a,dm)).length,0);
});
await test('actual API routes, capability, exact lookup, strict verbs/fields and no read mutation',async()=>{
 const w=await setup(),a=w.alice,o=await a.api('/api/overview?topics=1');assert.equal(o.schedules,true);let changes=0;const original=a.changed.bind(a);a.changed=(...v)=>{changes++;return original(...v);};
 const audience=await a.api('/api/schedule/audience?conv='+w.conv);assert.equal(audience.persons.length,2);assert.deepEqual(await a.api('/api/schedules'),[]);assert.equal(changes,0);
 const request=input(w,w.conv,{id:id(),audience,files:[{name:'api.bin',size:2,bytes:new Uint8Array([8,9])}]}),s=await a.api('/api/schedule/create',request);assert.equal(s.id,request.id);assert.ok(changes>0);changes=0;
 assert.deepEqual(await a.api('/api/schedules?id='+s.id),s);assert.deepEqual(await a.api('/api/schedule/create',request),s);assert.equal(changes,0);
 assert.deepEqual(await a.api('/api/schedules?conv='+'e'.repeat(64)),[]);
 for(const [path,body,status] of [['/api/schedules',{},405],['/api/schedule/create',undefined,405],['/api/schedule/audience?conv='+w.conv,{},405],['/api/schedule/unknown',{},404],['/api/schedule/cancel',{id:s.id},409],['/api/schedule/edit',{...request,rev:1,unknown:true},400],['/api/schedule/cancel',{id:s.id,rev:1,conv:w.conv},400],['/api/schedules?all=true',undefined,409],['/api/schedules?all=1&all=0',undefined,409],['/api/schedules?id='+id(),undefined,404]])await assert.rejects(()=>a.api(path,body),e=>e.status===status,path);
 await assert.rejects(()=>a.api('/api/schedule/create',{...request,body:'conflicting'}),e=>e.status===409&&e.code==='scheduled_id_conflict');
 const edited=await a.api('/api/schedule/edit',{...request,rev:s.rev,body:'Edited API',files:[]});assert.equal(edited.rev,2);assert.equal(edited.files.length,0);assert.deepEqual(edited.audience,s.audience);
 const sent=await a.api('/api/schedule/send_now',{id:s.id,rev:edited.rev});assert.equal(sent.state,'sent');assert.equal((await copies(a,s)).length,1);assert.deepEqual(await a.api('/api/schedules'),[]);assert.equal((await a.api('/api/schedules?all=1')).length,1);
 const fresh=await a.api('/api/schedule/create',input(w));const cancelled=await a.api('/api/schedule/cancel',{id:fresh.id,rev:fresh.rev});assert.equal(cancelled.state,'cancelled');
 a.closing=true;await assert.rejects(()=>a.api('/api/schedule/create',input(w)),/workspace is closing/);
});
await test('local durable pending, exact time, edit/cancel CAS and no early outbox',async()=>{
 const w=await setup(),a=w.alice,fetch=a.fetch;a.fetch=async()=>{throw Error('schedule creation must remain local');};const s=await a.scheduleSend(input(w));a.fetch=fetch;assert.equal(s.state,'pending');assert.equal(s.rev,1);assert.equal((await copies(a,s)).length,0);assert.equal(s.due,input(w).due);
 w.at(Date.parse(s.due)-1);assert.equal((await a.dispatchScheduledSend(s.id)).state,'pending');assert.equal((await copies(a,s)).length,0);
 const edited=await a.editScheduledSend(s.id,s.rev,input(w,w.conv,{body:'Edited before deadline'}));assert.equal(edited.rev,2);
 await assert.rejects(()=>a.cancelScheduledSend(s.id,1),e=>e.code==='scheduled_stale');
 const cancelled=await a.cancelScheduledSend(s.id,2);assert.equal(cancelled.state,'cancelled');assert.equal((await a.scheduledSends()).length,0);assert.equal((await a.scheduledSends(true)).length,1);
 await assert.rejects(()=>a.sendScheduledNow(s.id,2),e=>e.code==='scheduled_stale');
 for(const due of [new Date(w.clock()).toISOString(),'2026-10-01T12:00:00.000000001Z','2028-02-30T12:00:00Z'])await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{due})));

 const instant=new Date(w.clock()+60000).toISOString(),precise=await a.scheduleSend(input(w,w.conv,{due:instant.slice(0,-1)+'000000Z'}));assert.equal(precise.due,instant,'exact trailing-zero native nanoseconds preserve the same instant');await a.cancelScheduledSend(precise.id,precise.rev);
 await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{due:instant.slice(0,-1)+'000001Z'})),/millisecond/);
 await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{topic:'new'})));await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{timezone:'Made/Up'})));
});
for(const mode of ['DM','group','DM guest','group guest','DM captured member','group captured member'])await test(mode+' actual ordinary same-LID dispatch/restart',async()=>{
 const w=await setup(),a=w.alice;let conv=mode.includes('group')?await group(w):w.conv;const topic=id();await a.sendDM({conv,topic,body:'Existing topic'});await w.drain(w.bob);
 if(mode.includes('guest')||mode.includes('captured')){await guest(w,conv,topic);await a.sendDM({conv,topic,body:'Current invited topic'});await w.drain(w.carol);}
 const sender=mode.includes('guest')?w.carol:a,s=await sender.scheduleSend(input(w,conv,{topic,body:'Actual '+mode}));assert.equal((await copies(sender,s)).length,0);
 const e=await restart(sender);w.at(Date.parse(s.due));const result=await e.dispatchScheduledSend(s.id);assert.equal(result.state,'sent',JSON.stringify(result));
 const rows=await copies(e,s);assert.ok(rows.length);assert.ok(rows.every(r=>r.lid===s.id&&r.conv===conv&&r.topic===topic&&r.kind==='message'&&!r.target&&!r.topic_event&&r.origin==='ui'));
 assert.ok(rows.every(r=>wire.parseEnvelope(r.envelope).v===2));await e.dispatchScheduledSend(s.id);assert.equal((await copies(e,s)).length,rows.length);
 // An admitted outbox already holding the LID is recovered without another planner/seal.
 const saved=await e.scheduledSend(s.id);await e.store.write([{s:'kv',k:e.scheduledKey(s.id),v:{...saved,state:'dispatching'}}]);
 const recovered=await restart(e);recovered.sendDM=async()=>{throw Error('must not reseal');};assert.equal((await recovered.dispatchScheduledSend(s.id)).state,'sent');assert.equal((await copies(e,s)).length,rows.length);
});
await test('dispatching without outbox safely redoes same ID; state/outbox crash is atomic',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w));w.at(Date.parse(s.due));await a.store.write([{s:'kv',k:a.scheduledKey(s.id),v:{...s,state:'dispatching',rev:2}}]);
 const write=a.store.write.bind(a.store);let crash=true;a.store.write=async(ops,checks)=>{await write(ops,checks);if(crash&&ops.some(o=>o.s==='outbox'&&o.v?.lid===s.id)){crash=false;throw Error('simulated process crash after atomic commit');}};
 const r=await a.dispatchScheduledSend(s.id);assert.equal(r.state,'sent');assert.equal((await copies(a,s)).length,1);a.store.write=write;
 const again=await restart(a);assert.equal((await again.dispatchScheduledSend(s.id)).state,'sent');assert.equal((await copies(a,s)).length,1);
});
await test('strict encrypted files/refcount are atomic on quota and verified on dispatch',async()=>{
 const w=await setup(),a=w.alice,bytes=new TextEncoder().encode('PRIVATE FILE CONTENT'),file={name:'later.bin',size:bytes.length,bytes},write=a.store.write.bind(a.store);
 a.store.write=async(ops,checks)=>{if(ops.some(o=>o.s==='kv'&&o.k.startsWith('schedule/')))throw new DOMException('full','QuotaExceededError');return write(ops,checks);};
 await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{files:[file]})),e=>e.name==='QuotaExceededError');assert.equal((await a.store.prefix('kv','schedule/')).length,0);assert.equal((await a.store.prefix('kv','schedule-file/')).length,0);assert.equal((await a.store.prefix('files','kept/')).length,0);a.store.write=write;
 const s=await a.scheduleSend(input(w,w.conv,{files:[file]})),sha=s.files[0].sha256,kept=await a.store.get('files','kept/'+sha);assert.ok(kept.ct instanceof Uint8Array);assert.ok(!('bytes' in kept)&&!('path' in s.files[0]));assert.equal((await a.store.get('kv','schedule-file/'+sha)).count,1);
 const second=await a.scheduleSend(input(w,w.conv,{files:[{...file,name:'other-name.bin'}]}));assert.deepEqual((await a.store.get('files','kept/'+sha)).ct,kept.ct);assert.equal((await a.store.get('kv','schedule-file/'+sha)).count,2);
 await a.cancelScheduledSend(second.id,second.rev);assert.ok(await a.store.get('files','kept/'+sha));assert.equal((await a.store.get('kv','schedule-file/'+sha)).count,1);
 const r=await a.sendScheduledNow(s.id,s.rev);assert.equal(r.state,'sent',JSON.stringify(r));assert.equal(await a.store.get('kv','schedule-file/'+sha),undefined);
 const row=(await copies(a,s))[0],opened=await wire.open(row.envelope,w.bob.keys,w.bob.address,a.pub);assert.equal(opened.attachments[0].name,'later.bin');assert.equal(opened.attachments[0].sha256,sha);assert.deepEqual(await wire.decryptFile(row.files[0].ct,opened.attachments[0],w.bob.keys),bytes);
});
await test('edit retains files when omitted, explicit empty releases refs without deleting kept',async()=>{
 const w=await setup(),a=w.alice,file={name:'edit.bin',size:2,bytes:new Uint8Array([7,8])},s=await a.scheduleSend(input(w,w.conv,{files:[file]})),sha=s.files[0].sha256;
 const kept=await a.store.get('files','kept/'+sha),retained=await a.editScheduledSend(s.id,s.rev,input(w,w.conv,{body:'Edited retained'}));assert.deepEqual(retained.files,s.files);assert.equal((await a.store.get('kv','schedule-file/'+sha)).count,1);
 const removed=await a.editScheduledSend(s.id,retained.rev,input(w,w.conv,{files:[]}));assert.equal(removed.files.length,0);assert.equal(await a.store.get('kv','schedule-file/'+sha),undefined);assert.deepEqual((await a.store.get('files','kept/'+sha)).ct,kept.ct);
});
for(const mode of ['changed key','ended guest','gone topic'])await test(mode+' moves schedule to review without copying',async()=>{
 const w=await setup(),a=w.alice,topic=id();let sender=a,p;
 if(mode==='ended guest'){p=await guest(w,w.conv);sender=w.carol;await a.sendDM({conv:w.conv,body:'Current context'});await w.drain(sender);}
 if(mode==='gone topic')await a.sendDM({conv:w.conv,topic,body:'Scheduled topic source'});
 const s=await sender.scheduleSend(input(w,w.conv,{...(mode==='gone topic'?{topic}:{})}));
 if(mode==='changed key'){const pin=await a.store.get('pins',w.bob.address);await a.store.write([{s:'pins',k:w.bob.address,v:{...pin,pending:{fingerprint:w.carol.fp}}}]);}
 if(mode==='ended guest'){await a.changeHuman('end',{pid:p.pid});await w.drain(sender);}
 if(mode==='gone topic')await a.changeChatTopic('delete',{conv:w.conv,id:topic});
 w.at(Date.parse(s.due));assert.equal((await sender.dispatchScheduledSend(s.id)).state,'needs_review');assert.equal((await copies(sender,s)).length,0);
});
for(const mode of ['missing','tampered'])await test(mode+' scheduled file stays needs_review without outbox',async()=>{
 const w=await setup(),a=w.alice,bytes=new Uint8Array([1,2,3]),s=await a.scheduleSend(input(w,w.conv,{files:[{name:'tiny',size:3,bytes}]})),key='kept/'+s.files[0].sha256,old=await a.store.get('files',key);
 if(mode==='missing')await a.store.write([{s:'files',k:key}]);else {old.ct[old.ct.length-1]^=1;await a.store.write([{s:'files',k:key,v:old}]);}
 w.at(Date.parse(s.due));assert.equal((await a.dispatchScheduledSend(s.id)).state,'needs_review');assert.equal((await copies(a,s)).length,0);assert.equal((await a.store.get('kv','schedule-file/'+s.files[0].sha256)).count,1);
});
for(const stage of ['before precheck','after precheck','after planner'])await test('audience growth '+stage+' rolls back every schedule copy',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w));let grew=false;
 if(stage==='before precheck'){await guest(w,w.conv);grew=true;}
 if(stage==='after precheck'){const send=a.sendDM.bind(a);a.sendDM=async fields=>{if(fields.id===s.id&&!grew){grew=true;await guest(w,w.conv);}return send(fields);};}
 if(stage==='after planner'){const commit=a.commitReceiverCopies.bind(a);a.commitReceiverCopies=async(...args)=>{if(args[0].some(r=>r.lid===s.id)&&!grew){grew=true;await guest(w,w.conv);}return commit(...args);};}
 w.at(Date.parse(s.due));const r=await a.dispatchScheduledSend(s.id);assert.equal(r.state,'needs_review',JSON.stringify(r));assert.equal((await copies(a,s)).length,0);assert.ok(grew);
});
await test('text/time edits retain ceiling; explicit current preview required for reapproval/scope change',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),ceiling=structuredClone(s.audience);await guest(w,w.conv);
 const edited=await a.editScheduledSend(s.id,s.rev,input(w,w.conv,{body:'Text-only edit after growth'}));assert.deepEqual(edited.audience,ceiling,'editing text never reapproves growth');
 const blocked=await a.sendScheduledNow(s.id,edited.rev);assert.equal(blocked.state,'needs_review');assert.equal((await copies(a,s)).length,0);
 const fetch=a.fetch;a.fetch=async()=>{throw Error('audience preview must remain local');};const preview=await a.scheduleAudience(w.conv,'');a.fetch=fetch;assert.ok(preview.pids.length>ceiling.pids.length);
 await assert.rejects(()=>a.editScheduledSend(s.id,blocked.rev,input(w,w.conv,{audience:ceiling})),e=>e.code==='scheduled_audience');
 const reviewed=await a.editScheduledSend(s.id,blocked.rev,input(w,w.conv,{audience:preview}));assert.deepEqual(reviewed.audience,preview);assert.equal((await a.sendScheduledNow(s.id,reviewed.rev)).state,'sent');
 const fresh=await a.scheduleSend(input(w)),topic=id();await a.sendDM({conv:w.conv,topic,body:'Different topic'});
 await assert.rejects(()=>a.editScheduledSend(fresh.id,fresh.rev,input(w,w.conv,{topic})),e=>e.code==='scheduled_audience');
 const exact=await a.scheduleAudience(w.conv,topic),moved=await a.editScheduledSend(fresh.id,fresh.rev,input(w,w.conv,{topic,audience:exact}));assert.equal(moved.topic,topic);
 await assert.rejects(()=>a.scheduleSend(input(w,w.conv,{audience:{persons:[],pids:[]}})),e=>e.code==='scheduled_audience');
});
await test('actual dispatch cannot substitute another body under the scheduled ID',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),send=a.sendDM.bind(a);a.sendDM=fields=>send({...fields,body:'Different unauthorized body'});w.at(Date.parse(s.due));assert.equal((await a.dispatchScheduledSend(s.id)).state,'needs_review');assert.equal((await copies(a,s)).length,0);
});
await test('growth between commit guard and checked write is rejected atomically',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),before=new Set([...await a.store.all('inbox'),...await a.store.all('outbox')].map(r=>r.id));
 await guest(w,w.conv);const growth=[];
 // Park genuine already-admitted signed invitation/decision rows, then inject
 // those exact rows at the concurrent storage boundary; no invented proof.
 for(const table of ['inbox','outbox'])for(const row of await a.store.byConv(table,w.conv))if(!before.has(row.id)){growth.push({s:table,k:row.id,v:row});await a.store.write([{s:table,k:row.id}]);}
 const write=a.store.write.bind(a.store);let injected=false;a.store.write=async(ops,checks)=>{if(!injected&&ops.some(o=>o.s==='outbox'&&o.v?.lid===s.id)){injected=true;await write(growth);}return write(ops,checks);};
 w.at(Date.parse(s.due));assert.equal((await a.dispatchScheduledSend(s.id)).state,'needs_review');assert.equal((await copies(a,s)).length,0);assert.ok(injected);a.store.write=write;
});
await test('clock-back after planner and stop during planner never enqueue early/stopped copies',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),commit=a.commitReceiverCopies.bind(a);let back=true;
 a.commitReceiverCopies=async(...args)=>{if(back&&args[0].some(r=>r.lid===s.id)){back=false;w.at(Date.parse(s.due)-1);}return commit(...args);};
 w.at(Date.parse(s.due));assert.equal((await a.dispatchScheduledSend(s.id)).state,'pending');assert.equal((await copies(a,s)).length,0);a.commitReceiverCopies=commit;
 let entered,release;const ready=new Promise(r=>entered=r),wait=new Promise(r=>release=r),send=a.sendDM.bind(a);a.sendDM=async fields=>{entered();await wait;return send(fields);};a.loop=()=>{};w.at(Date.parse(s.due));a.start();await ready;const current=await a.scheduledSend(s.id);await assert.rejects(()=>a.editScheduledSend(s.id,current.rev,input(w)),e=>e.code==='scheduled_stale');await assert.rejects(()=>a.cancelScheduledSend(s.id,current.rev),e=>e.code==='scheduled_stale');await assert.rejects(()=>a.sendScheduledNow(s.id,current.rev),e=>e.code==='scheduled_stale');const draining=a.scheduledDrain;a.stop();release();await draining;assert.equal((await copies(a,s)).length,0);assert.equal((await a.scheduledSend(s.id)).state,'pending');
 a.sendDM=send;a.start();await drained(a);assert.equal((await a.scheduledSend(s.id)).state,'sent');assert.equal((await copies(a,s)).length,1);a.stop();
});
await test('new device of an approved person allowed; audience shrink allowed',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),phone=await w.sibling(w.bob,'bob/later',true);await w.ready();w.at(Date.parse(s.due));const r=await a.dispatchScheduledSend(s.id);assert.equal(r.state,'sent',JSON.stringify(r));assert.deepEqual(new Set((await copies(a,s)).map(r=>r.to)),new Set([w.bob.address,phone.address]));
 const p=await guest(w,w.conv),shrunk=await a.scheduleSend(input(w));await a.changeHuman('end',{pid:p.pid});w.at(Date.parse(shrunk.due));assert.equal((await a.dispatchScheduledSend(shrunk.id)).state,'sent');assert.equal((await copies(a,shrunk)).some(r=>r.to===w.carol.address),false);
});
await test('GC retraction/erasure keeps scheduled bytes; concurrent creation fences GC',async()=>{
 const w=await setup(),a=w.alice,bytes=new Uint8Array([4,5,6]),file={name:'keep.bin',size:3,bytes},sent=await a.sendDM({conv:w.conv,body:'Old bytes',files:[file]}),row=(await a.store.all('outbox')).find(r=>r.lid===sent.lid),s=await a.scheduleSend(input(w,w.conv,{files:[file]})),sha=s.files[0].sha256;
 const checks=[],ops=await a.dropCached(row,null,checks);assert.equal(ops.some(o=>o.k==='kept/'+sha),false);assert.ok(checks.some(c=>c.k==='schedule-file/'+sha));
 row.state='custody';await a.store.write([{s:'outbox',k:row.id,v:row}]);a.erased.add(w.conv+'|'+a.fp+'|'+row.lid);const erasedChecks=[],erased=await a.eraseOps(w.conv,[],[row],erasedChecks);assert.equal(erased.some(o=>o.k==='kept/'+sha),false);assert.ok(erasedChecks.some(c=>c.k==='schedule-file/'+sha));
 await a.cancelScheduledSend(s.id,s.rev);const raceChecks=[],deletes=await a.dropCached(row,null,raceChecks);assert.ok(deletes.some(o=>o.k==='kept/'+sha));
 await a.scheduleSend(input(w,w.conv,{files:[file]}));await assert.rejects(()=>a.store.write(deletes,raceChecks),/storage changed/);assert.ok(await a.store.get('files','kept/'+sha));
});
await test('deadline timer no early/clockback/long timeout/stop/new epoch/reload; no hotspin',async()=>{
 const w=await setup(),a=w.alice,s=await a.scheduleSend(input(w)),ordinarySend=a.sendDM.bind(a);let serial=0,timers=new Map();
 const browser=typeof window!=='undefined',windowEvents=new EventTarget(),documentEvents=browser?document:new EventTarget(),saved={add:globalThis.addEventListener,remove:globalThis.removeEventListener,document:globalThis.document},visibility=Object.getOwnPropertyDescriptor(documentEvents,'visibilityState');const listeners=new Set();
 const setVisibility=value=>{if(browser)Object.defineProperty(documentEvents,'visibilityState',{configurable:true,get:()=>value});else documentEvents.visibilityState=value;};setVisibility('visible');
 globalThis.addEventListener=(type,fn)=>{listeners.add(fn);windowEvents.addEventListener(type,fn);};globalThis.removeEventListener=(type,fn)=>{listeners.delete(fn);windowEvents.removeEventListener(type,fn);};if(!browser)globalThis.document=documentEvents;
 try {
a.scheduleSetTimeout=(fn,ms)=>{const id=++serial;timers.set(id,{fn,ms});return id;};a.scheduleClearTimeout=id=>timers.delete(id);a.loop=()=>{};
 a.start();await drained(a);assert.equal(timers.size,1);assert.equal([...timers.values()][0].ms,10000);
 w.at(now+5000);windowEvents.dispatchEvent(new Event('focus'));await drained(a);assert.equal([...timers.values()][0].ms,5000);assert.equal(listeners.size,1);
 setVisibility('hidden');documentEvents.dispatchEvent(new Event('visibilitychange'));assert.equal([...timers.values()][0].ms,5000);
 setVisibility('visible');documentEvents.dispatchEvent(new Event('visibilitychange'));windowEvents.dispatchEvent(new Event('online'));await drained(a);assert.equal((await copies(a,s)).length,0);

 let timer=[...timers.values()][0];timers.clear();w.at(now-1000);timer.fn();await drained(a);assert.equal((await copies(a,s)).length,0);assert.equal([...timers.values()][0].ms,11000);
 const late=[...timers.values()][0];a.stop();w.at(Date.parse(s.due));late.fn();assert.equal((await copies(a,s)).length,0);assert.equal(timers.size,0);
 const loaded=await restart(a);loaded.loop=()=>{};await loaded.load();loaded.start();await drained(loaded);assert.equal((await loaded.scheduledSend(s.id)).state,'sent');loaded.stop();

 const sent=await a.scheduledSend(s.id);await a.store.write([{s:'kv',k:a.scheduledKey(s.id),v:{...sent,state:'dispatching'}}]);w.at(Date.parse(s.due)-1000);
 const recovered=await restart(a);recovered.loop=()=>{};recovered.sendDM=async()=>{throw Error('clock-back recovery must not reseal');};recovered.start();await drained(recovered);assert.equal((await recovered.scheduledSend(s.id)).state,'sent');assert.equal((await copies(a,s)).length,1);recovered.stop();w.at(Date.parse(s.due));
 const long=await a.scheduleSend(input(w,w.conv,{due:new Date(w.clock()+2147483647+5000).toISOString()}));a.start();await drained(a);assert.equal([...timers.values()][0].ms,2147483647);timer=[...timers.values()][0];timers.clear();w.at(w.clock()+2147483647);timer.fn();await drained(a);assert.equal([...timers.values()][0].ms,5000);a.stop();
 // Unexpected TypeError stays needs_review, with no deadline loop.
 a.sendDM=async()=>{throw new TypeError('offline planner');};w.at(Date.parse(long.due));a.start();await drained(a);assert.equal((await a.scheduledSend(long.id)).state,'needs_review');assert.equal(timers.size,0);a.stop();assert.equal(listeners.size,0,'stop removes scheduler lifecycle listeners');
 // Real network identity stays pending, does not wake on changed(), and
 // commits once after a genuine online lifecycle event.
 const network=await a.scheduleSend(input(w));let attempts=0,offline=true;
 a.sendDM=async fields=>{attempts++;if(offline)throw new HubError(0,'network','offline');return ordinarySend(fields);};w.at(Date.parse(network.due));a.start();await drained(a);
 const waiting=await a.scheduledSend(network.id);assert.equal(waiting.state,'pending');assert.equal(waiting.detail,'Waiting for connectivity or current chat information.');assert.equal((await copies(a,network)).length,0);assert.equal(attempts,1);assert.equal(timers.size,0);
 for(let i=0;i<20;i++)a.changed();await Promise.resolve();assert.equal(attempts,1,'changed cannot schedule another attempt');
 offline=false;windowEvents.dispatchEvent(new Event('online'));await drained(a);assert.equal((await a.scheduledSend(network.id)).state,'sent');assert.equal((await copies(a,network)).length,1);assert.equal(attempts,2);
 windowEvents.dispatchEvent(new Event('online'));await drained(a);assert.equal(attempts,2);a.stop();assert.equal(listeners.size,0);
 } finally {
  a.stop();if(browser){if(visibility)Object.defineProperty(documentEvents,'visibilityState',visibility);else delete documentEvents.visibilityState;}
  for(const [key,value] of Object.entries({addEventListener:saved.add,removeEventListener:saved.remove,...(browser?{}:{document:saved.document})})){if(value===undefined)delete globalThis[key];else globalThis[key]=value;}
 }
});
console.log(JSON.stringify({ok:true,checks:passed}));

if(typeof window!=='undefined'){for(const s of fixtureStores)s.close();window.scheduleIDBResult={ok:true,storage:'IndexedDB',checks:passed,assertions:fixtureAssertionCount(),reopens,fixtureStoreNames:fixtureStores.map(s=>s.fixtureStoreName)};}
