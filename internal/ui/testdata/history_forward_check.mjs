// Own-device history forwarding (t5-delivery): catch-up does not send a
// device the history that exact device forwarded here, and a sibling copy
// that cannot be made never fails the original's admission (client
// forwardStale). Generated keys, isolated relay.
import assert from 'node:assert/strict';
import {Engine,memoryStore,HubError} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
async function ident(address,label){const keys=await wire.newKeys(),pub=await wire.publicEntry(keys,address),fp=await wire.fingerprint(pub),roster=await wire.newRoster(keys,address,label);return {address,keys,pub,fp,roster,hash:await wire.rosterHash(roster)};}
const pinOf=p=>({address:p.address,json:wire.marshalPublic(p.pub),fingerprint:p.fp,pending:null});
const phone=await ident('owner/phone','Owner'),laptop=await ident('owner/laptop','Owner laptop'),peer=await ident('peer/desk','Peer');
const join=await wire.joinConsent(laptop.keys,laptop.address,phone.roster.person,1,phone.hash);
const next=await wire.nextRoster(phone.keys,phone.address,phone.roster,[phone.pub,laptop.pub],join,phone.roster.label,[phone.fp,laptop.fp]);await wire.verifyNext(next,phone.roster);
const e=new Engine({store:memoryStore(),base:'https://isolated.invalid',fetch:async()=>new Response('{"error":"isolated"}',{status:503})});
Object.assign(e,{keys:phone.keys,address:phone.address,pub:phone.pub,fp:phone.fp});
const own=await e.personRecord([phone.roster,next],'self',null),peerP=await e.personRecord([peer.roster],'pinned',null);e.me=own;
const root=await wire.newRoot(peer.keys,{person:peer.roster.person,roster:peer.hash,address:peer.address,fingerprint:peer.fp},{person:phone.roster.person,roster:own.hash}),conv=await wire.rootID(root);
await e.store.write([{s:'kv',k:'identity',v:{keys:phone.keys,address:phone.address,fingerprint:phone.fp}},{s:'kv',k:'person',v:own},{s:'persons',k:peerP.person,v:peerP},
 {s:'pins',k:peer.address,v:pinOf(peer)},{s:'pins',k:laptop.address,v:pinOf(laptop)},{s:'kv',k:'held-group-history-recovery-v1',v:true},{s:'kv',k:'held-dm-lifecycle-recovery-v1',v:true},
 {s:'convs',k:conv,v:{id:conv,root:wire.rootJSON(root),peer:peerP.person,creator:peer.address,created:root.created}}]);
const at=Date.now()-60_000;
// A: the peer's turn, as history forwarded here by the laptop itself.
const lidA=wire.newID(),item=e.itemOf({id:lidA,lid:lidA,from:peer.address,fp:peer.fp,ts:Math.floor(at/1000),at,kind:'message',body:'forwarded by the laptop',reply_to:'',sub:''},false);
const carrier=await wire.seal({v:2,id:wire.newID(),from:laptop.address,to:phone.address,ts:Math.floor(at/1000),kind:'message',body:wire.historyJSON(item),conv,lid:wire.newID(),root:wire.rootJSON(root),replica:true,sub:'history',attachments:[]},laptop.keys,phone.pub);
await e.admit(carrier,wire.parseEnvelope(carrier));
const a=await e.store.get('inbox',lidA);check(a?.history&&a.synced_from===laptop.address&&a.synced_key===laptop.fp,'received history keeps its exact forwarding key');
// B: forwarded by an earlier key at the laptop's address; C: a legacy row
// without the key. Neither is known to be held by the current laptop key.
const stored=(n,extra)=>{const id=wire.newID();return {id,lid:id,from:peer.address,fp:peer.fp,conv,kind:'message',body:'stored '+n,ts:Math.floor((at+n)/1000),at:at+n,history:true,synced_from:laptop.address,replica:true,read:true,state:'',...extra};};
const b=stored(1,{synced_key:'e'.repeat(64)}),c=stored(2,{});
await e.store.write([{s:'inbox',k:b.id,v:b},{s:'inbox',k:c.id,v:c}]);
await e.runHistory();
const copied=new Set((await e.store.all('outbox')).filter(r=>r.sub==='history'&&r.to===laptop.address).map(r=>wire.parseHistory(r.body).id));
check(!copied.has(lidA),'history that exact device forwarded here is not sent back to it');
check(copied.has(b.id)&&copied.has(c.id),'history from another key, or of unknown key, is still copied');
// A copy for a sibling the sender's older roster step lacked is best effort:
// any failure making it (other than a storage race or the network) leaves
// the original's admission to proceed, as the core logs and continues.
{
 const n={v:2,conv,lid:wire.newID(),kind:'message',body:'to an older step',fan:[{person:own.person,roster:phone.hash}],attachments:[]},env={id:wire.newID(),from:peer.address,ts:Math.floor(Date.now()/1000)},pin=pinOf(peer);
 let tried=0;e.historyCopy=async()=>{tried++;throw Error('copy check failed');};
 check((await e.forwardStale(n,env,pin,{root:wire.rootJSON(root)})).length===0&&tried===1,'a failing sibling copy does not fail the original');
 e.historyCopy=async()=>{throw new HubError(503,'','relay unavailable');};
 await assert.rejects(()=>e.forwardStale(n,env,pin,{root:wire.rootJSON(root)}),/relay unavailable/);checks++;
}
console.log(JSON.stringify({historyForward:true,checks}));
