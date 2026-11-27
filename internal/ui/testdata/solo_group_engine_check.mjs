// MEL-840 in the browser: a group that is one person's alone, on this one
// device, still talks (client sendGroupTurn's !others). An ordinary turn,
// text and a file, is one signed local-only copy, stored as delivered and
// never posted, shown in the chat, kept across a restart and openable.
import {assert,world} from './human_engine_check.mjs';
import * as wire from '../static/wire.mjs';
import {Engine} from '../static/engine.mjs';
const caps=[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapConversationAudience,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom,wire.CapTopicParticipation,wire.CapTopicContext,wire.CapSharedTopicTitle,wire.CapConversationResources];
const w=await world();
for(const e of w.users){e.realm='f'.repeat(32);e.featureList=null;const fetch=e.fetch;e.fetch=async(url,o)=>new URL(url).pathname==='/v1/version'?new Response(JSON.stringify({features:['env2','env3','person2','caps','notify1'],realm_id:'f'.repeat(32)})):fetch(url,o);await w.caps(e,caps);}
const a=w.alice, solo=await a.createGroup('My agents'), posted=w.posts.length;
const text=await a.sendDM({conv:solo,body:'note to self'});
const bytes=new TextEncoder().encode('solo notes');
const file=await a.sendDM({conv:solo,body:'with a file',files:[{name:'notes.txt',size:bytes.length,bytes}]});
for(const sent of [text,file]){assert.equal(sent.state,'delivered');assert.equal(sent.copies.length,1);assert.equal(sent.copies[0].to,a.address);}
const rows=(await a.store.all('outbox')).filter(r=>r.conv===solo&&!r.control&&!r.aside&&[text.lid,file.lid].includes(r.lid));
assert.equal(rows.length,2);
for(const r of rows){assert.equal(r.to,a.address);assert.equal(r.state,'delivered');assert.equal(r.replica,true);const opened=await wire.open(r.envelope,a.keys,a.address,a.pub);assert.equal(opened.to,a.address);assert.equal(opened.lid,r.lid);}
assert.equal(w.posts.slice(posted).some(p=>wire.parseEnvelope(p).conv===solo||rows.some(r=>wire.parseEnvelope(p).id===r.id)),false,'a local-only copy is never posted');
a.connected=true;await a.flushOutboxOnce();a.connected=false;
assert.equal(w.posts.slice(posted).some(p=>rows.some(r=>wire.parseEnvelope(p).id===r.id)),false,'nothing posted by the outbox');
const shown=async e=>(await e.api('/api/dm?id='+solo)).messages.filter(m=>[text.lid,file.lid].includes(m.lid));
const check=async e=>{
  const ms=await shown(e);assert.deepEqual(ms.map(m=>m.body).sort(),['note to self','with a file']);
  const withFile=ms.find(m=>m.lid===file.lid);assert.equal(withFile.attachments.length,1);assert.equal(withFile.attachments[0].name,'notes.txt');
  const f=await e.api('/api/file?id='+encodeURIComponent(withFile.id)+'&i=0&dir=out');assert.equal(new TextDecoder().decode(f.bytes),'solo notes');
};
await check(a);
// The local-only copy is the only holder of its file's bytes: they are kept
// in the same write as the delivered row. A failed file write stores neither,
// and the send fails visibly.
const failing=new TextEncoder().encode('never half kept'),failingKey='kept/'+wire.hex(await wire.sha256(failing)),write=a.store.write;
a.store.write=async(ops,checks)=>{if(ops.some(o=>o.s==='files'&&o.k===failingKey))throw Error('synthetic file write failure');return write.call(a.store,ops,checks);};
await assert.rejects(()=>a.sendDM({conv:solo,body:'file write fails',files:[{name:'lost.txt',size:failing.length,bytes:failing}]}),/synthetic file write failure/);
a.store.write=write;
assert.equal((await a.store.all('outbox')).some(r=>r.body==='file write fails'),false,'no delivered row without its file');
assert.equal(await a.store.get('files',failingKey),undefined,'no file without its row');
assert.equal((await shown(a)).length,2);
assert.equal((await a.api('/api/dm?id='+solo)).messages.some(m=>m.body==='file write fails'),false);
const restarted=new Engine({store:a.store,base:a.base,fetch:a.fetch,now:a.now});
await restarted.load();await restarted.features();
await check(restarted);
const kept=await restarted.store.get('files','kept/'+wire.hex(await wire.sha256(bytes)));
assert.ok(kept?.ct,'the kept bytes are there after a reload');
assert.equal(new TextDecoder().decode(await wire.decryptFile(kept.ct,kept.attachment,restarted.keys)),'solo notes');
console.log('solo group engine check ok');
