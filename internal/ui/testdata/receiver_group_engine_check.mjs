// Existing native Go proof/context vectors, real Engine/age; relay/ready signer synthetic.
import assert from 'node:assert/strict';
import {createInterface} from 'node:readline';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let keys,pub,roster;const address='browser/desk',now=1790000000123;
async function setup(){keys=await wire.newKeys();pub=await wire.publicEntry(keys,address);roster=await wire.newRoster(keys,address,'Browser');return{public:wire.marshalPublic(pub),roster:wire.rosterJSON(roster)};}
async function consent(challenge){const root=wire.parseGroupRoot(challenge.root);return{consent:wire.groupAdmissionJSON(await wire.signGroupAdmission(keys,{conv:await wire.rootID(root),realm:root.realm,person:roster.person,roster:await wire.rosterHash(roster),seq:0,prev:'',history:null,by:await wire.fingerprint(pub)}))};}
async function checks(v){
 const root=wire.parseGroupRoot(v.challenge.root),conv=await wire.rootID(root),blobs=new Map(),posts=[],publics=new Map(),profiles=new Map(Object.entries(v.receiver_profiles)),chains=new Map();
 for(const r of [...v.challenge.rosters,v.invited_roster]){chains.set(r.person,[r]);for(const d of r.devices)publics.set(d.address,await wire.parsePublic(d));}
 for(const x of Object.values(v.carriers))blobs.set(x.blob,{ct:wire.unb64(x.ct)});
 for(const x of v.participations.files)blobs.set(x.blob,{ct:wire.unb64(x.ct)});
 const json=x=>new Response(JSON.stringify(x));
 const fetch=async(url,o={})=>{const u=new URL(url);assert.equal(u.origin,'https://synthetic.invalid');const p=u.pathname;
  if(p==='/v1/version')return json({features:['env2','env3','person2','caps'],realm_id:root.realm});
  if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
  if(p.startsWith('/v1/agents/'))return json({public:JSON.parse(wire.marshalPublic(publics.get(p.slice(11))))});
  if(p.startsWith('/v1/persons/')){const rows=chains.get(p.split('/')[3])||[],after=Number(u.searchParams.get('after'));return json({records:rows.filter(r=>r.seq>after),more:false});}
  if(p==='/v1/receipts')return json({});
  if(p==='/v1/messages'){await wire.verifyEnvelope(wire.parseEnvelope(o.body),publics.get(wire.parseEnvelope(o.body).from).sign_key);posts.push(o.body);return json({state:'custody'});}
  if(p.endsWith('/wait'))return json({state:'custody'});
  if(p==='/v1/blobs'){const b=JSON.parse(o.body);if(!blobs.has(b.id))blobs.set(b.id,{...b,ct:new Uint8Array(b.size),received:0,state:'uploading'});return json(blobs.get(b.id));}
  if(p.startsWith('/v1/blobs/')){const b=blobs.get(p.split('/')[3]);if(p.endsWith('/data'))return new Response(b.ct);if(p.endsWith('/complete'))b.state='stored';else if(o.method==='PUT'){b.ct.set(new Uint8Array(o.body),b.received);b.received+=o.body.byteLength;}return json(b);}
  throw Error('unexpected synthetic path '+p);
 };
 const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch,now:()=>now});e.keys=keys;e.address=address;e.fp=await wire.fingerprint(pub);e.realm=root.realm;e.connected=true;
 for(const raw of [...v.challenge.rosters,v.invited_roster]){const r=await wire.parseRoster(raw),p=await e.personRecord([r],r.person===roster.person?'self':'pinned',null);if(r.person===roster.person){e.me=p;await e.store.write([{s:'kv',k:'person',v:p}]);}else await e.store.write([{s:'persons',k:p.person,v:p}]);await e.pinDevices(p);}
 await e.store.write([{s:'kv',k:'identity',v:{keys,address,fingerprint:e.fp}}]);
 for(const carrier of [v.carriers.proof,v.carriers.context])await e.onMessage(carrier.envelope);
 if(e.retrying)await e.retrying;
 assert.equal((await e.groupCurrent(conv)).state.seq,0,'existing native original prefix/state admitted');
 // Link selected receiver with existing signed own-person transition; no group membership install.
 const laptopKeys=await wire.newKeys(),laptopPub=await wire.publicEntry(laptopKeys,'browser/laptop'),laptopFP=await wire.fingerprint(laptopPub),join=await wire.joinConsent(laptopKeys,laptopPub.address,roster.person,1,await wire.rosterHash(roster)),linked=await wire.nextRoster(keys,address,roster,[pub,laptopPub],join);
 await wire.verifyNext(linked,roster);e.me=await e.personRecord([roster,linked],'self',e.me);await e.store.write([{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);publics.set(laptopPub.address,laptopPub);chains.set(roster.person,[JSON.parse(wire.rosterJSON(roster)),JSON.parse(wire.rosterJSON(linked))]);
 const session=wire.newID();profiles.set(laptopPub.address,{live:true,sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(laptopKeys,laptopPub.address,session,[wire.CapGroup,wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapReplyReceiver])))]});
 const receiver={kind:'managed_agent',agent_id:'e'.repeat(32),instructions:'Selected local continuation',mode:'question',host:{address:laptopPub.address,fingerprint:laptopFP}};
 const prepared=[];
 const file={name:'chosen.txt',bytes:new TextEncoder().encode('EXACT GROUP RECEIVER BYTES')};
 async function accepted(sent,expect){
  await e.outboxPass;
  assert.equal(sent.state,'receiver_waiting');const setupRow=(await e.store.all('outbox')).find(r=>r.receiver_setup?.request.conv===conv&&r.receiver_setup.request.lid===sent.lid);assert.ok(setupRow);prepared.push(setupRow);
  const operation=await wire.parseReceiverOperation(setupRow.body,setupRow.receiver_route,'',setupRow.attachments||[]);expect(operation.request);
  assert.equal(operation.request.group_admission,await wire.groupAdmissionHash(wire.groupMember(v.states[0],roster.person).admission));
  assert.equal(new TextDecoder().decode(await wire.decryptFile(blobs.get(setupRow.attachments[0].blob.id).ct,setupRow.attachments[0],laptopKeys)),'EXACT GROUP RECEIVER BYTES');
  const originals=(await e.store.all('outbox')).filter(r=>r.lid===sent.lid);assert.ok(originals.length);for(const r of originals)assert.equal(r.state,'receiver_waiting');
  const before=posts.length,route={...setupRow.receiver_route,op:'ready'},raw=await wire.seal({v:1,id:wire.newID(),from:laptopPub.address,to:address,ts:Math.floor(now/1000),kind:'message',reply_to:setupRow.id,body:wire.receiverOperationJSON({v:1}),receiver_route:route},laptopKeys,pub);
  await e.admit(raw,wire.parseEnvelope(raw));for(const r of originals)assert.equal((await e.store.get('outbox',r.id)).envelope,r.envelope);
  for(const r of originals)await e.post(await e.store.get('outbox',r.id));assert.equal(posts.length,before+originals.length);
  await e.admit(raw,wire.parseEnvelope(raw));assert.equal(posts.length,before+originals.length,'duplicate ready does not rerun');
 }
 await accepted(await e.sendDM({conv,body:'Ordinary group selected receiver',files:[file],reply_receiver:receiver}),r=>{assert.equal(r.kind,'message');assert.equal(r.group_replies.length,2);assert.ok(r.group_replies.every(k=>k.admission));});
 for(const role of ['member','visitor']){
  for(const kind of ['invite','accept']){
   const raw=v.participations[role+'-'+kind].envelope;await e.onMessage(raw);
   const id=wire.parseEnvelope(raw).id;assert.ok(await e.store.get('inbox',id),'native '+role+' '+kind+' ingress: '+JSON.stringify(await e.store.get('held',id)?.then(x=>x&&{reason:x.reason})));
  }
  const pid=v.participations[role+'-pid'];
  await accepted(await e.askAgent({pid,body:'Exact '+role+' addressed question',files:[file],reply_receiver:receiver}),r=>{assert.equal(r.pid,pid);assert.equal(r.group_replies.length,1);assert.equal(r.group_replies[0].key,r.target.fingerprint);assert.equal(!!r.group_replies[0].admission,role==='member');assert.equal(r.id,r.lid);});
 }
 assert.equal(prepared.length,3);
 // Stop before release when persisted own identity changed; receipt and copies atomic.
 const last=await e.sendDM({conv,body:'Held across identity change',files:[file],reply_receiver:receiver}),row=(await e.store.all('outbox')).find(r=>r.receiver_setup?.request.lid===last.lid),old=await e.store.get('kv','person');
 await e.store.write([{s:'kv',k:'person',v:{...old,devices:old.devices.filter(d=>d.address!==laptopPub.address)}}]);
 const route={...row.receiver_route,op:'ready'},raw=await wire.seal({v:1,id:wire.newID(),from:laptopPub.address,to:address,ts:Math.floor(now/1000),kind:'message',reply_to:row.id,body:wire.receiverOperationJSON({v:1}),receiver_route:route},laptopKeys,pub);await e.admit(raw,wire.parseEnvelope(raw));
 assert.equal((await e.store.get('held',wire.parseEnvelope(raw).id)).reason,'invalid');for(const r of row.receiver_setup.originals)assert.equal((await e.store.get('outbox',r.id)).state,'receiver_waiting');assert.equal((await e.store.get('receipts',wire.parseEnvelope(raw).id)).state,'quarantined');assert.equal(await e.store.get('inbox',wire.parseEnvelope(raw).id),undefined);
 return{ok:true,evidence:'existing native proof + ordinary/member-PID/visitor-PID prepared encrypted files; original audience epochs; exact ready/dedup; changed current-own identity refuses atomically; native executor untested'};
}
for await(const line of createInterface({input:process.stdin})){let result;try{const r=JSON.parse(line);result=r.op==='setup'?await setup():r.op==='consent'?await consent(r.challenge):await checks(r.vectors);}catch(e){result={error:e.stack};}process.stdout.write(JSON.stringify(result)+'\n');}
