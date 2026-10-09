import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
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

const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
const conv='c'.repeat(64),old={id:wire.newID(),lid:wire.newID(),conv,from:b.address,fp:b.fp,kind:'message',body:'old',read:false,at:1},fresh={...old,id:wire.newID(),lid:wire.newID(),body:'new',at:2};
for(const e of [a,p])await e.store.write([{s:'inbox',k:old.id,v:old},{s:'inbox',k:fresh.id,v:fresh}]);
await a.markRead([old.id]);await a.syncReadMarks();
let row=(await a.store.all('outbox')).find(r=>r.sub===wire.SubReadSync);check(row&&!wire.parseEnvelope(row.envelope).attn,'quiet encrypted exact read marker');
await a.post(row);await receive(row.envelope,p);await p.readSyncRun;
check((await p.store.get('inbox',old.id)).read,'same human read converged');check(!(await p.store.get('inbox',fresh.id)).read,'newer unseen retained');
const future={conv,fingerprint:b.fp,lid:wire.newID()},record={v:1,person:a.me.person,roster:a.me.hash,refs:[future]},body=JSON.stringify(record);
const envelope=await wire.seal({v:2,id:wire.newID(),from:p.address,to:a.address,ts:1,kind:'message',sub:wire.SubReadSync,replica:true,body},p.keys,a.pub);
await receive(envelope,a);await a.readSyncRun;
const pending={...old,id:wire.newID(),lid:future.lid,read:false},ops=[{s:'inbox',k:pending.id,v:pending}],checks2=[];await a.applyReadArrivals(ops,checks2);await a.store.write(ops,checks2);check((await a.store.get('inbox',pending.id)).read,'marker before future original applies exact tuple');
const changed={...pending,id:wire.newID(),fp:p.fp,read:false},wrong=[{s:'inbox',k:changed.id,v:changed}],wrongChecks=[];await a.applyReadArrivals(wrong,wrongChecks);check(!wrong[0].v.read,'changed author key is another reference');
await assert.rejects(()=>a.readSyncAuthority(record,b.address,b.fp,a.address,a.fp),/current own-human/);checks++;
await p.store.write([{s:'kv',k:'person',v:{...p.me,human_keys:[p.fp]}}]);await assert.rejects(()=>p.readSyncAuthority(record,a.address,a.fp,p.address,p.fp),/current own-human/);checks++;await p.store.write([{s:'kv',k:'person',v:p.me}]);
await a.store.write([{s:'outbox',k:row.id,v:{...row,state:'queued'}}]);const offlinePosts=posts.length;const offlineFetch=a.fetch;a.fetch=async()=>{throw new TypeError('Failed to fetch');};await a.post({...row,state:'queued'});check((await a.store.get('outbox',row.id)).state==='queued'&&posts.length===offlinePosts,'offline carrier durable and retryable');a.fetch=offlineFetch;
await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapRoom]);const sent=posts.length;
await a.store.write([{s:'outbox',k:row.id,v:{...row,state:'queued'}}]);await a.post({...row,state:'queued'});check((await a.store.get('outbox',row.id)).state==='waiting'&&posts.length===sent,'legacy rm1 reader waits');
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:structuredClone(a.keys),address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});
await restarted.syncReadMarks();check((await a.store.all('outbox')).filter(r=>r.sub===wire.SubReadSync&&JSON.stringify(r.read_refs)===JSON.stringify(row.read_refs)).length===1,'restart reconciliation idempotent');
await caps(p);row=await a.store.get('outbox',row.id);const original=row.envelope;await restarted.post(row);check((await a.store.get('outbox',row.id)).envelope===original&&posts.length===sent+1,'restoration retries unchanged ciphertext');
const batchOps=[];for(let i=0;i<65;i++){const ref={conv,fingerprint:b.fp,lid:wire.newID()};batchOps.push({s:'kv',k:restarted.readMarkKey(a.me.person,ref),v:{owner:a.me.person,ref}});}await a.store.write(batchOps);const beforeBatch=(await a.store.all('outbox')).filter(o=>o.sub===wire.SubReadSync).length;await restarted.syncReadMarks();const batches=(await a.store.all('outbox')).filter(o=>o.sub===wire.SubReadSync);check(batches.length===beforeBatch+2&&batches.every(o=>wire.parseReadSync(o.body).refs.length<=64),'65 refs encrypt as two bounded carriers');
const heldBatch=batches.find(o=>wire.parseReadSync(o.body).refs.length===64);await a.store.write([{s:'outbox',k:heldBatch.id,v:{...heldBatch,state:'quarantined'}}]);
for(let wake=0;wake<3;wake++)await restarted.syncReadMarks();
check((await a.store.all('outbox')).filter(o=>o.sub===wire.SubReadSync).length===batches.length,'quarantined full read batch survives repeated recovery wakes without replacement');
check((await a.store.get('outbox',heldBatch.id)).envelope===heldBatch.envelope,'quarantined read carrier retains exact ciphertext');
const nextRef={conv,fingerprint:b.fp,lid:wire.newID()};await a.store.write([{s:'kv',k:restarted.readMarkKey(a.me.person,nextRef),v:{owner:a.me.person,ref:nextRef}}]);await restarted.syncReadMarks();
const afterHeld=(await a.store.all('outbox')).filter(o=>o.sub===wire.SubReadSync);check(afterHeld.length===batches.length+1&&afterHeld.filter(o=>!batches.some(old=>old.id===o.id)).every(o=>wire.parseReadSync(o.body).refs.length===1),'new read fact remains independently sendable beside held batch');
await a.store.write([{s:'pins',k:p.address,v:{...(await a.store.get('pins',p.address)),pending:{fingerprint:b.fp}}}]);await assert.rejects(()=>a.readSyncGate(row),/key changed/);checks++;
await assert.rejects(()=>wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'message',conv,sub:wire.SubReadSync,replica:true,body},a.keys,p.pub),/quiet rootless/);checks++;
await p.store.write([{s:'kv',k:'person',v:{...p.me,state:'conflict'}}]);await assert.rejects(()=>p.readSyncAuthority(record,a.address,a.fp,p.address,p.fp),/current own-human/);checks++;
check(!wire.capsReads({caps:[wire.CapRoom]},wire.CapReadSync),'rm1 does not imply rd1');
console.log('read sync browser checks',checks);
