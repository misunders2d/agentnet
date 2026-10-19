// Real Engine signing/encryption and admission over the existing isolated world.
import {assert,world} from './human_engine_check.mjs';
import * as wire from '../static/wire.mjs';
const caps=[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapConversationAudience,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom,wire.CapTopicParticipation,wire.CapTopicContext,wire.CapSharedTopicTitle,wire.CapConversationResources];
const w=await world();
async function ready(){for(const e of w.users){e.realm='f'.repeat(32);e.featureList=null;const fetch=e.fetch;e.fetch=async(url,o)=>new URL(url).pathname==='/v1/version'?new Response(JSON.stringify({features:['env2','env3','person2','caps','notify1'],realm_id:'f'.repeat(32)})):fetch(url,o);await w.caps(e,caps);}}
await ready();const {alice:a,bob:b,carol:g}=w;
const conv=await a.createGroup('Minimal shared links');
const invite=await a.inviteGroup({conv,person:b.me.person});await w.drain(b);await b.retryHeld();await b.decideGroup({id:invite.id,accept:true});await w.drain(a);if(a.recoveringGroups)await a.recoveringGroups;await a.recoverGroupIntents();await w.drain(b);await b.retryHeld();
assert.equal(!!await a.sharedTopicTitle(conv,''),false);
assert.equal((await a.store.all('outbox')).some(r=>r.conv===conv&&wire.conversationResources(r.topic_event)),false);
const links=[{name:'Notes',url:'https://example.com/notes?a=1&b=2'},{name:'Notes',url:'https://example.org/second'}];
const first=await a.setConversationResources({conv,links,rev:0,writer:''});await w.drain(b);
assert.deepEqual(await b.conversationResources(conv),first);
assert.deepEqual((await b.api('/api/dm?id='+conv)).resources,first);
assert.equal((await b.convMessages(conv,await b.store.all('inbox'),await b.store.all('outbox'))).some(m=>wire.conversationResources(m.topic_event)),false);
await assert.rejects(()=>b.setConversationResources({conv,links:[],rev:0,writer:''}),e=>e.code==='conversation_resources_stale');
const left=await a.setConversationResources({conv,links:[{name:'Alice edit',url:'https://example.com/alice'}],rev:first.rev,writer:first.writer});
const right=await b.setConversationResources({conv,links:[{name:'Bob edit',url:'https://example.com/bob'}],rev:first.rev,writer:first.writer});
await w.drain(a);await w.drain(b);
assert.deepEqual(await a.conversationResources(conv),await b.conversationResources(conv));
const winner=a.fp>b.fp?left:right;assert.deepEqual(await a.conversationResources(conv),winner);
const invited=await a.changeHuman('invite',{conv,host:g.address});await w.drain(g);await w.drain(b);await g.changeHuman('decide',{pid:invited.pid,accept:true});await w.drain(a);await w.drain(b);
await assert.rejects(()=>g.conversationResources(conv),/member|group/i);
assert.equal(Object.hasOwn(await g.api('/api/dm?id='+conv),'resources'),false);
const current=await a.conversationResources(conv);let last=await a.setConversationResources({conv,links:[{name:'Current member notes',url:'https://example.com/current'}],rev:current.rev,writer:current.writer});
await w.drain(b);await w.drain(g);
assert.equal((await a.store.all('outbox')).some(r=>r.to===g.address&&wire.conversationResources(r.topic_event)),false);
// A real admission with no selected history must carry a newly signed live
// register. Interrupt the local atomic install after relay custody to exercise
// recovery without allowing a separate follow-up send.
async function join(room,member,interrupt=false){
  const invitation=await a.inviteGroup({conv:room,person:member.me.person});
  await w.drain(member);await member.retryHeld();await member.decideGroup({id:invitation.id,accept:true});
  const recover=a.recoverGroupIntents;
  a.recoverGroupIntents=async()=>{};
  try{await w.drain(a);if(a.recoveringGroups)await a.recoveringGroups;}finally{a.recoverGroupIntents=recover;}
  assert.deepEqual((await a.store.get('kv','group-invitation/out/'+invitation.id)).proposal.history||[],[]);
  if(interrupt){
    const write=a.store.write.bind(a.store);let stopped=false;
    a.store.write=async(ops,checks)=>{
      if(ops.some(op=>op.s==='kv'&&op.k==='group-invitation/out/'+invitation.id&&op.v?.status==='published')){stopped=true;throw Error('fixture interrupted atomic admission install');}
      return write(ops,checks);
    };
    try{await assert.rejects(()=>a.publishGroupInvitation(invitation.id),/fixture interrupted/);}finally{a.store.write=write;}
    assert.deepEqual(await a.conversationResources(room),last);
    assert.equal(stopped,true);
  }
  await a.recoverGroupIntents();
  await w.drain(member);await member.retryHeld();
  return invitation;
}
const beforeJoin=last,admission=await join(conv,w.mallory,true);
last=await a.conversationResources(conv);
assert.deepEqual(last.links,beforeJoin.links);assert.equal(last.rev,beforeJoin.rev+1);assert.equal(last.writer,a.fp);assert.notEqual(last.lid,beforeJoin.lid);
await w.drain(b);await b.retryHeld();await w.drain(g);
assert.deepEqual(await w.mallory.conversationResources(conv),last);
assert.deepEqual(await b.conversationResources(conv),last);
assert.equal((await w.mallory.store.all('inbox')).some(r=>r.conv===conv&&r.sub==='history'),false);
assert.equal((await a.store.all('outbox')).some(r=>r.to===g.address&&wire.conversationResources(r.topic_event)),false);
assert.equal(Object.hasOwn(await g.api('/api/dm?id='+conv),'resources'),false);
const publication=(await a.store.all('outbox')).filter(r=>r.conv===conv&&r.lid===last.lid).map(r=>({id:r.id,envelope:r.envelope}));
await a.publishGroupInvitation(admission.id);await a.recoverGroupIntents();await w.mallory.recoverGroupIntents();await b.recoverGroupIntents();
assert.deepEqual(await a.conversationResources(conv),last);
assert.deepEqual((await a.store.all('outbox')).filter(r=>r.conv===conv&&r.lid===last.lid).map(r=>({id:r.id,envelope:r.envelope})),publication);
const phone=await w.sibling(a,'alice/resource-phone',true);await ready();
const dev=a.me.devices.find(d=>d.address===phone.address),chat=await a.groupRecord(conv);
for(const carrier of await a.groupHistoryCarriers(chat,dev,[])){await a.store.write([{s:'outbox',k:carrier.id,v:carrier}]);await a.post(carrier);}
await w.drain(phone);
const job={state:'running',device:dev.address,fingerprint:dev.fingerprint,own_human:a.fp};await a.store.write([{s:'kv',k:'history',v:{[dev.address]:job}}]);
for(let page=0;page<4;page++)await a.historyCatchupPage(dev,(await a.store.get('kv','history'))[dev.address]);
await w.drain(phone);await phone.retryHeld();
assert.deepEqual(await phone.conversationResources(conv),last);
// The fixture adds an Alice device without running normal background roster
// propagation. Resolve that changed roster before Bob's unrelated new edit.
await b.refreshPerson(await b.store.get('persons',a.me.person));
const cleared=await b.setConversationResources({conv,links:[],rev:last.rev,writer:last.writer});await w.drain(a);
assert.deepEqual(await a.conversationResources(conv),cleared);
// A present cleared register carries its clock too: the next member's first
// edit must outrank the clear on every existing member.
const clearedRoom=await a.createGroup('Cleared before admission');
await join(clearedRoom,b);
const seeded=await a.setConversationResources({conv:clearedRoom,links,rev:0,writer:''});await w.drain(b);
const empty=await a.setConversationResources({conv:clearedRoom,links:[],rev:seeded.rev,writer:seeded.writer});await w.drain(b);
await join(clearedRoom,w.mallory);await w.drain(b);await b.retryHeld();
const carried=await w.mallory.conversationResources(clearedRoom);
assert.deepEqual(carried.links,[]);assert.equal(carried.rev,empty.rev+1);
const newcomerEdit=await w.mallory.setConversationResources({conv:clearedRoom,links,rev:carried.rev,writer:carried.writer});
await w.drain(a);await w.drain(b);await a.retryHeld();await b.retryHeld();
assert.deepEqual(await a.conversationResources(clearedRoom),newcomerEdit);
assert.deepEqual(await b.conversationResources(clearedRoom),newcomerEdit);
console.log('PASS group resources: member sync/DM projection, quiet timeline, stale CAS, concurrency/reorder, guest omission/no copies, own-linked recovery, admission bootstrap/recovery/idempotence, clear/join/edit convergence');
