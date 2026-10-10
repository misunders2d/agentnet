// MIXED-1 (browser side): own-device sync carriers store no inbox row. The
// engine receipts each as delivered when it admits it, and keeps that it
// did past the receipt's flush: a relay's re-delivery is receipted again,
// never admitted or applied a second time (client store.seen).
import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const pubs=new Map(),chains=new Map(),profiles=new Map(),acks=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p==='/v1/version')return json({features:['env2','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.startsWith('/v1/messages/')&&p.endsWith('/ack')){acks.push({id:p.slice(13,-4),state:JSON.parse(o.body).state});return json({id:p.slice(13,-4),state:JSON.parse(o.body).state});}
 throw Error('Unexpected synthetic route '+p);
};
async function caps(e){
 const names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapOwnSyncV2,wire.CapTopicStateSync,wire.CapRoom];
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
const settle=async e=>{for(let i=0;i<4;i++){if(e.topicSyncRun)await e.topicSyncRun;if(e.topicMarkSyncRun)await e.topicMarkSyncRun;if(e.readSyncRun)await e.readSyncRun;if(e.invitationSyncRun)await e.invitationSyncRun;}};
const delivered=id=>acks.filter(x=>x.id===id&&x.state==='delivered').length;

const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
let admits=0;const admit=p.admit.bind(p);p.admit=async(...x)=>{admits++;return admit(...x);};

// receive: one carrier of sub from the laptop, through the phone's stream
// entry (onMessage), its receipts flushed. It returns the carrier.
async function receive(sub){
 const rows=(await a.store.all('outbox')).filter(o=>o.sub===sub&&o.to===p.address&&o.state==='queued');
 check(rows.length===1,sub+': one carrier queued');
 const row=rows[0];await a.post(row);
 const before=admits;await p.onMessage(row.envelope);await p.flushReceipts();await settle(p);
 check(admits===before+1&&delivered(row.id)===1,sub+': admitted once and receipted delivered');
 check(!await p.store.get('receipts',row.id)&&await p.store.get('kv','sync-carrier/'+row.id),sub+': its admission outlives the flushed receipt');
 return row;
}
// again: the relay pushes the carrier once more (it missed the receipt).
// What it applied is undone first: it is receipted again, never re-applied.
async function again(row,key){
 const sub=row.sub;check(!!await p.store.get('kv',key),sub+': applied');
 await p.store.write([{s:'kv',k:key,v:undefined}]);
 const before=admits,receipts=delivered(row.id);await p.onMessage(row.envelope);await p.flushReceipts();await settle(p);
 check(admits===before&&delivered(row.id)===receipts+1,sub+': a re-delivery is receipted again without admission');
 check(!await p.store.get('kv',key),sub+': a re-delivery applies nothing');
}

// Read state.
const conv='c'.repeat(64),turn={id:wire.newID(),lid:wire.newID(),conv,from:b.address,fp:b.fp,kind:'message',body:'to read',read:false,at:1};
for(const e of [a,p])await e.store.write([{s:'inbox',k:turn.id,v:turn}]);
await a.markRead([turn.id]);await a.syncReadMarks();
let row=await receive(wire.SubReadSync);await again(row,p.readMarkKey(a.me.person,wire.parseReadSync(row.body).refs[0]));

// A private topic name, then a topic mark.
const topic=wire.newID(),original={v:2,id:topic,lid:topic,conv,topic,from:b.address,fp:b.fp,kind:'message',body:'Automatic title',read:false,at:1,ts:1};
await a.store.write([{s:'inbox',k:topic,v:original}]);
await a.changeChatTopic('rename',{conv,id:topic,title:'Private name'});await settle(a);
row=await receive(wire.SubTopicSync);await again(row,p.topicTitleKey(a.me.person,wire.parseTopicSync(row.body).titles[0]));
await a.changeChatTopic('archive',{conv,id:topic});await settle(a);
row=await receive(wire.SubTopicStateSync);await again(row,p.topicMarkKey(a.me.person,wire.parseTopicStateSync(row.body).marks[0]));

// An outgoing invitation's view.
const realm=wire.newID(),title='Receipted invitation';
const root=await wire.signGroupRoot(a.keys,{v:wire.GroupRootVersion,kind:'group',realm,title,creator:{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},members:[{person:a.me.person,roster:a.me.hash}],admins:[a.me.person],nonce:wire.newID(),created:1});
const gconv=await wire.rootID(root),admission=await wire.signGroupAdmission(a.keys,{conv:gconv,realm,person:a.me.person,roster:a.me.hash,seq:0,prev:'',history:null,by:a.fp});
const state=await wire.signGroupState(a.keys,{v:1,conv:gconv,realm,seq:0,prev:'',title,members:[{person:a.me.person,roster:a.me.hash,admin:true,admission}],actor:a.me.person,actor_roster:a.me.hash,by:a.fp});
const proposal={v:1,root,state,withdrawals:null,target:b.me.person,roster:b.me.hash,seq:1,prev:await wire.groupStateHash(state),history:null,nonce:wire.newID()},id=await wire.groupInvitationID(proposal);
await a.store.write([{s:'kv',k:'group-invitation/out/'+id,v:{type:'group-invitation',id,direction:'out',status:'pending',proposal,inviter:a.address,owner:a.me.person,fp:a.fp}}]);
await a.syncInvitations();
row=await receive(wire.SubInvitationSync);await again(row,p.invitationViewKey(a.fp,id));

check(acks.every(x=>x.state==='delivered'),'no carrier was ever receipted as anything but delivered');
console.log('own sync receipt browser checks',checks);
