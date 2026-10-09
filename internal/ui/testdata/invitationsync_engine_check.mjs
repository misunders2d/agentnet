const assert={ok:(x,m)=>{if(!x)throw Error(m)},rejects:async(fn,re)=>{try{await fn()}catch(e){if(re.test(e.message))return;throw e}throw Error('Expected rejection '+re)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
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
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2]){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='invitation-sync-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;
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
const realm=wire.newID(),title='Shared pending invitation';
const root=await wire.signGroupRoot(a.keys,{v:wire.GroupRootVersion,kind:'group',realm,title,creator:{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},members:[{person:a.me.person,roster:a.me.hash}],admins:[a.me.person],nonce:wire.newID(),created:1});
const conv=await wire.rootID(root),admission=await wire.signGroupAdmission(a.keys,{conv,realm,person:a.me.person,roster:a.me.hash,seq:0,prev:'',history:null,by:a.fp});
const state=await wire.signGroupState(a.keys,{v:1,conv,realm,seq:0,prev:'',title,members:[{person:a.me.person,roster:a.me.hash,admin:true,admission}],actor:a.me.person,actor_roster:a.me.hash,by:a.fp});
const proposal={v:1,root,state,withdrawals:null,target:b.me.person,roster:b.me.hash,seq:1,prev:await wire.groupStateHash(state),history:null,nonce:wire.newID()},id=await wire.groupInvitationID(proposal);
const intent={type:'group-invitation',id,direction:'out',status:'pending',proposal,inviter:a.address,owner:a.me.person,fp:a.fp},intentKey='group-invitation/out/'+id;
await a.store.write([{s:'kv',k:intentKey,v:intent}]);await a.syncInvitations();
const copies=async(e)=>(await e.store.all('outbox')).filter(o=>o.sub===wire.SubInvitationSync);
let rows=await copies(a);check(rows.length===1&&!wire.parseEnvelope(rows[0].envelope).attn,'one quiet encrypted own-device view');
const first=rows[0];await a.post(first);await receive(first.envelope,p);
let views=await p.groupInvitations();check(views.length===1&&views[0].id===id&&views[0].status==='pending'&&views[0].direction==='out'&&!views[0].can_cancel&&!views[0].can_refresh,'linked device shows exact pending invitation without action authority');
check(!await p.store.get('kv',intentKey),'mirror is never local consent/publication intent');
await assert.rejects(()=>p.cancelGroup({id}),/No outgoing invitation/);checks++;
await assert.rejects(()=>p.decideGroup({id,accept:true}),/No invitation/);checks++;
await a.store.write([{s:'kv',k:intentKey,v:{...intent,status:'cancelled'}}]);await a.syncInvitations();rows=await copies(a);check(rows.length===2,'changed status creates one newer revision');
const newest=rows.find(o=>o.invitation_revision===2);await receive(newest.envelope,p);
const seal=async(sender,r)=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:p.address,ts:1,kind:'message',sub:wire.SubInvitationSync,replica:true,body:wire.invitationSyncJSON(r)},sender.keys,p.pub);
await receive(await seal(a,wire.parseInvitationSync(first.body)),p);
check((await p.groupInvitations())[0].status==='cancelled','delayed earlier revision cannot restore pending');
const conflict=await receive(await seal(a,{...wire.parseInvitationSync(newest.body),status:'accepted'}),p);check((await p.store.get('held',conflict)).reason==='invalid','same revision conflict fails closed');
const foreign=await receive(await seal(b,wire.parseInvitationSync(newest.body)),p);check((await p.store.get('held',foreign)).reason==='invalid','foreign person cannot supply own invitation view');
if(typeof window!=='undefined'){a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});
await restarted.syncInvitations();check((await copies(a)).length===2,'restart does not resend unchanged views');
await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapRoom]);const sent=posts.length;await restarted.post(newest);check((await a.store.get('outbox',newest.id)).state==='waiting'&&posts.length===sent,'older reader waits without receiving unknown subtype');
await caps(p);await restarted.post(await a.store.get('outbox',newest.id));check(posts.length===sent+1,'upgraded reader releases original ciphertext');
await p.store.write([{s:'pins',k:a.address,v:{...(await p.store.get('pins',a.address)),pending:{fingerprint:b.fp}}}]);check((await p.groupInvitations()).length===0,'changed source key hides trusted view');
await a.store.write([{s:'kv',k:'person',v:{...a.me,human_keys:[a.fp]}}]);await assert.rejects(()=>a.invitationSyncGate(newest),/current own-human/);checks++;
check(!wire.capsReads({caps:[wire.CapRoom]},wire.CapOwnSyncV2),'legacy room capability never implies invitation sync');
await assert.rejects(()=>wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'task',sub:wire.SubInvitationSync,replica:true,body:first.body},a.keys,p.pub),/quiet rootless/);checks++;
await assert.rejects(()=>wire.validateInvitationSync({...wire.parseInvitationSync(first.body),id:'f'.repeat(64)}),/proposal differs/);checks++;
console.log('invitation sync browser checks',checks);

if(typeof window!=='undefined'){window.invitationResult={ok:true,checks,storage:'IndexedDB'};for(const e of [a,b,p]){e.store.close();await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(e.fixtureStoreName);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});}}
