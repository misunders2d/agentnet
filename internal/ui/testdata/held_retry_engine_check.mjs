// Receiver isolation in the browser engine (node, memory stores, generated
// identities only): a history copy's unreadable participation event is
// held instead of ending the stream (RS-2); the held-message retry pass goes
// on past a row that throws and continues where an unreachable server
// stopped it (RS-8); a member list that changes only availability starts no
// retry pass (FLOOD-1).
import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
const {Engine,memoryStore,HubError}=await import(process.env.AGENTNET_ENGINE_MODULE||'../static/engine.mjs');
let checks=0;const failed=[];const check=(x,m)=>{if(!x)failed.push(m);checks++;};
async function ident(address,label){const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,address),fp=await wire.fingerprint(pub),roster=await wire.newRoster(keys,address,label);return {address,keys,pub,fp,roster,hash:await wire.rosterHash(roster)};}
const pinOf=p=>({address:p.address,json:wire.marshalPublic(p.pub),fingerprint:p.fp,pending:null});
const unavailable=async()=>new Response('{"error":"isolated unavailable"}',{status:503});
const engine=(fetch=unavailable)=>new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch});

// RS-2: an own-device history copy whose participation event has a field
// this version does not know.
{
 const phone=await ident('fixture/phone','Owner'),laptop=await ident('fixture/laptop','Laptop'),peer=await ident('peer/host','Peer');
 const join=await wire.joinConsent(laptop.keys,laptop.address,phone.roster.person,1,phone.hash);
 const next=await wire.nextRoster(phone.keys,phone.address,phone.roster,[phone.pub,laptop.pub],join,phone.roster.label,[phone.fp,laptop.fp]);
 const e=engine();Object.assign(e,{keys:phone.keys,address:phone.address,pub:phone.pub,fp:phone.fp});e.flushReceipts=async()=>{};
 const own=await e.personRecord([phone.roster,next],'self',null);e.me=own;
 const peerP=await e.personRecord([peer.roster],'pinned',null),nextHash=await wire.rosterHash(next);
 const root=await wire.newRoot(phone.keys,{person:phone.roster.person,roster:nextHash,address:phone.address,fingerprint:phone.fp},{person:peer.roster.person,roster:peer.hash}),conv=await wire.rootID(root);
 await e.store.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:own},{s:'persons',k:peerP.person,v:peerP},
  {s:'pins',k:laptop.address,v:pinOf(laptop)},{s:'pins',k:peer.address,v:pinOf(peer)},
  {s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:peer.roster.person,created:root.created,creator:phone.address}}]);
 const now=Math.floor(Date.now()/1000);
 const ev=await wire.signEvent(laptop.keys,{conv,pid:wire.newID(),type:'dismiss',prev:'a'.repeat(64),author:{person:phone.roster.person,roster:nextHash,address:laptop.address,fingerprint:laptop.fp},ts:now});
 const body=wire.eventJSON(ev).slice(0,-1)+',"future_field":1}';
 const item={v:1,from:laptop.address,from_key:laptop.fp,id:wire.newID(),lid:wire.newID(),ts:now,kind:'message',body,sub:'event',pid:ev.pid,at:Date.now(),attachments:[]};
 const raw=await wire.seal({v:wire.Version2,id:wire.newID(),from:laptop.address,to:phone.address,ts:now,kind:'message',body:wire.historyJSON(item),conv,lid:wire.newID(),root:wire.rootJSON(root),sub:'history',replica:true},laptop.keys,phone.pub);
 const id=wire.parseEnvelope(raw).id;
 let thrown=null;try{await e.onMessage(raw);}catch(x){thrown=x;}
 check(!thrown,'an unreadable history participation event ended the stream: '+thrown?.message);
 check((await e.store.get('held',id))?.reason==='invalid','an unreadable history participation event is held as invalid');
 check((await e.store.get('receipts',id))?.state==='quarantined','and its receipt says quarantined');
}

// RS-8: a held row that throws is noted and the pass goes on; an unreachable
// server ends it, and the next pass continues from that row.
{
 const me=await ident('retry/phone','Owner'),e=engine();Object.assign(e,{keys:me.keys,address:me.address,pub:me.pub,fp:me.fp});
 e.discloseHumanAudience=()=>{};
 const ids=[];for(let i=0;i<120;i++)ids.push(i.toString(16).padStart(32,'0'));
 const rows=[];for(const id of ids)rows.push({s:'held',k:id,v:{id,from:'retry/peer',reason:'proof_pending',envelope:await wire.seal({id,from:'retry/peer',to:me.address,ts:1,kind:'message',body:'held'},me.keys,me.pub),at:1,detail_code:'context_unavailable'}});
 await e.store.write(rows);
 const broken=ids[3],away=ids[70];let down=true;const seen=[];
 e.admit=async(data,env)=>{seen.push(env.id);if(env.id===broken)throw new TypeError('a record this page cannot read');if(env.id===away&&down){down=false;throw new HubError(503,'','the server is unreachable');}};
 await e.retryHeld();
 check(seen.includes(ids[4])&&seen.includes(ids[69]),'the pass stopped at a row that threw: '+seen.length+' rows looked at');
 check(seen.at(-1)===away&&!seen.includes(ids[71]),'an unreachable server ends the pass at its row');
 check(e.heldFailures?.get(broken)?.n===1,'the throwing row is noted');
 seen.length=0;
 await e.retryHeld();
 check(seen[0]===away,'the next pass began at row '+ids.indexOf(seen[0])+', not where the server was unreachable');
 check(seen.includes(ids[119])&&seen.filter(id=>id===ids[0]).length===1,'the pass reaches the end and then looks once from the first row');
}

// Every member list looks at held messages again (client onMembers): their
// evidence also arrives through local admissions that announce nothing.
{
 const me=await ident('members/phone','Owner'),e=engine();Object.assign(e,{keys:me.keys,address:me.address,pub:me.pub,fp:me.fp});
 let held=0,pending=0,flushed=0;
 Object.assign(e,{retryHeld:async()=>{held++;},retryPendingReceives:async()=>{pending++;},flushOutbox:async()=>{flushed++;},keepMemberFacts:async()=>{},fillListed:async()=>{},refreshTyping:async()=>{},refreshPerson:async()=>{},changed:()=>{}});
 const person=seq=>({id:'a'.repeat(32),seq,hash:'b'.repeat(64)});
 const push=list=>e.dispatch('members',JSON.stringify({members:list}));
 await push([{address:'vitalii/desk',presence:'connected',joined:5,person:person(2)}]);
 await push([{address:'vitalii/desk',presence:'offline',joined:5,version:'v0.8.17',suspended:true,person:person(2)}]);
 await push([{address:'vitalii/desk',presence:'offline',joined:5,person:person(3)}]);
 check(held===3&&pending===3&&flushed===3,'every list looks at held messages and releases waiting copies ('+held+','+pending+','+flushed+')');
}
assert.deepEqual(failed,[],failed.length+' of '+checks+' checks failed');
console.log('held retry engine checks',checks);
