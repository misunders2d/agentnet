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
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapRoom]){
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
const a=await person('alice/desk'),b=await person('bob/desk');
const bp=await a.personRecord([b.roster],'pinned',null);await a.store.write([{s:'persons',k:bp.person,v:bp}]);await a.pinDevices(bp);
const before=await a.newDM(b.address),raw=(await a.store.get('convs',before)).root;
check((await rootCopies(a)).length===0,'no root copy before linking');
const p=await sibling(a,'alice/phone');await a.syncRoots();
let row=(await rootCopies(a,before))[0];check(row&&row.to===p.address,'empty pre-link root queued to human phone');
check(!wire.parseEnvelope(row.envelope).attn,'root has no notification attention');
await a.post(row);
const sourcePin=await p.store.get('pins',a.address);
await p.store.write([{s:'pins',k:a.address,v:{...sourcePin,pending:{fingerprint:b.fp}}}]);
await receive(row.envelope,p);check(!!await p.store.get('held',row.id),'pending source key holds exact root carrier');
await a.store.write([{s:'outbox',k:row.id,v:{...row,state:'quarantined'}}]);
for(let i=0;i<2;i++)await a.syncRoots();
check((await rootCopies(a,before)).length===1&&(await a.store.get('outbox',row.id)).envelope===row.envelope,'quarantined root is retained without resealing');
await p.store.write([{s:'pins',k:a.address,v:sourcePin}]);await receive(row.envelope,p);
check((await p.store.get('convs',before)).root===raw,'unchanged signed root arrives');
check((await p.dm(before)).messages.length===0,'no visible fabricated message');
check((await p.store.get('inbox',row.id)).aside&&(await p.store.get('inbox',row.id)).read,'quiet and already read');
await receive(row.envelope,p);await a.syncRoots();check((await rootCopies(a,before)).length===1,'replay and reconciliation idempotent');
await p.rootSyncRun;
const after=await p.newDM(b.address);row=(await rootCopies(p,after))[0];check(row?.to===a.address,'phone-created empty root copied automatically');
await p.post(row);await receive(row.envelope,a);check((await a.store.get('convs',after)).root===(await p.store.get('convs',after)).root,'same phone-created signed root');
await a.rootSyncRun;
await p.sendDM({conv:after,body:'first real turn'});
for(const r of await p.store.all('outbox'))if(r.body==='first real turn')await receive(r.envelope,r.to===a.address?a:b);
check((await a.dm(after)).messages.filter(m=>m.body==='first real turn').length===1&&(await b.dm(after)).messages.length===1,'real turns use synced root');
const waiting=await a.newDM(b.address);row=(await rootCopies(a,waiting))[0];await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapRoom]);
const sent=posts.length;await a.post(row);check((await a.store.get('outbox',row.id)).state==='waiting'&&posts.length===sent,'older rm1 reader waits without delivery');
await a.syncRoots();check((await rootCopies(a,waiting)).length===1,'waiting carrier not duplicated');
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:structuredClone(a.keys),address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});
await caps(p);await restarted.syncRoots();await restarted.post(await restarted.store.get('outbox',row.id));await receive(row.envelope,p);
check((await p.store.get('convs',waiting)).root===(await a.store.get('convs',waiting)).root,'restart recovers durable original carrier');
await assert.rejects(()=>a.rootSyncAuthority(wire.parseRoot(raw),a.address,a.fp,b.address,b.fp),/own-human/);checks++;
const foreign=await wire.seal({v:2,id:wire.newID(),from:b.address,to:p.address,ts:Math.floor(Date.now()/1000),kind:'message',conv:before,lid:wire.newID(),root:raw,sub:wire.SubRootSync,replica:true,body:'{"v":1}'},b.keys,p.pub);
const bad=await receive(foreign,p);check(!!await p.store.get('held',bad),'foreign sender held');
const own=await a.store.get('kv','person');await a.store.write([{s:'kv',k:'person',v:{...own,human_keys:[a.fp]}}]);
await assert.rejects(()=>a.rootSyncAuthority(wire.parseRoot(raw),a.address,a.fp,p.address,p.fp),/own-human/);checks++;
await a.store.write([{s:'kv',k:'person',v:own},{s:'persons',k:bp.person,v:{...bp,state:'conflict'}}]);
await assert.rejects(()=>a.rootSyncAuthority(wire.parseRoot(raw),a.address,a.fp,p.address,p.fp),/frozen/);checks++;
await a.store.write([{s:'persons',k:bp.person,v:bp},{s:'pins',k:p.address,v:{...(await a.store.get('pins',p.address)),pending:'changed'}}]);
await assert.rejects(()=>a.rootSyncAuthority(wire.parseRoot(raw),a.address,a.fp,p.address,p.fp),/key changed/);checks++;
console.log(JSON.stringify({checks,scope:'quiet empty DM roots, restart, old reader, own-human authority'}));
