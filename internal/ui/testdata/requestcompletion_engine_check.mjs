// Actual signed DM requests and replies over isolated browser stores.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const pubs = new Map(), chains = new Map(), devices = [];
const json = value => new Response(JSON.stringify(value));
const fetch = async url => {
  const u = new URL(url), path = u.pathname;
  if (path.startsWith('/v1/persons/') && path.endsWith('/chain')) return json({ records: chains.get(path.slice(12, -6)).filter(r => r.seq > Number(u.searchParams.get('after'))), more: false });
  if (path.endsWith('/profile')) return json({ person: JSON.parse(wire.rosterJSON(devices.find(e => e.address === path.slice(11, -8)).roster)) });
  if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(path)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(path.slice(11)))) });
  throw Error('Unexpected fixture transport ' + path);
};
async function device(address) {
  const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch });
  e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
  e.changed = () => {}; e.runHistory = async () => {}; e.discloseHumanAudience = () => {}; e.recoverHumanExcerpts = async () => {}; e.flushReceipts = async () => {};
  pubs.set(address, e.pub); devices.push(e); return e;
}
async function person(address) {
  const e = await device(address); e.roster = await wire.newRoster(e.keys, address, address.split('/')[0]);
  chains.set(e.roster.person, [JSON.parse(wire.rosterJSON(e.roster))]); return e;
}
async function sibling(owner, address) {
  const e = await device(address), old = owner.roster;
  const consent = await wire.joinConsent(e.keys, address, old.person, old.seq + 1, await wire.rosterHash(old));
  const oldPubs = old.devices.map(d => pubs.get(d.address));
  owner.roster = await wire.nextRoster(owner.keys, owner.address, old, [...oldPubs, e.pub], consent, old.label, [...await wire.rosterHumans(old), e.fp]);
  chains.get(old.person).push(JSON.parse(wire.rosterJSON(owner.roster))); return e;
}
const host = await person('own/host');
let phone = await sibling(host, 'own/phone');
const laptop = await sibling(host, 'own/laptop'), peer = await person('peer/desk'), outsider = await person('outsider/desk');
for (const e of devices) {
  e.roster = e.address.startsWith('own/') ? host.roster : e.roster;
  e.me = await e.personRecord(await Promise.all(chains.get(e.roster.person).map(wire.parseRoster)), 'self', null);
  await e.store.write([{ s: 'kv', k: 'person', v: e.me }]);
  for (const owner of [host, peer, outsider]) {
    const p = await e.personRecord(await Promise.all(chains.get(owner.roster.person).map(wire.parseRoster)), owner.roster.person === e.me.person ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }]);
  }
  for (const d of devices) await e.store.write([{ s: 'pins', k: d.address, v: { address: d.address, json: wire.marshalPublic(d.pub), fingerprint: d.fp, pending: null } }]);
}
const root = await wire.newRoot(host.keys, host.author(), { person: peer.me.person, roster: peer.me.hash }), conv = await wire.rootID(root), pid = wire.newID();
await phone.store.write([{ s: 'convs', k: conv, v: { id: conv, root: wire.rootJSON(root), peer: peer.me.person, creator: host.address, created: root.created } }]);
const from = async (sender, fields) => wire.seal({ v: 2, id: wire.newID(), from: sender.address, to: phone.address, ts: 1, conv, root: wire.rootJSON(root), lid: wire.newID(), kind: 'message', body: '', replica: sender.me.person === phone.me.person, ...fields }, sender.keys, phone.pub);
const receive = async raw => { const env = wire.parseEnvelope(raw); await phone.onMessage(raw); return env.id; };
const invite = await wire.signEvent(host.keys, { conv, pid, type: 'invite', ts: 1, author: host.author(), host: { person: host.me.person, address: host.address, fingerprint: host.fp }, audience: 'conversation' });
await receive(await from(host, { sub: 'event', pid, body: wire.eventJSON(invite) }));
const accepted = await wire.signEvent(host.keys, { conv, pid, type: 'accept', prev: await wire.eventHash(invite), ts: 2, author: host.author() });
await receive(await from(host, { sub: 'event', pid, body: wire.eventJSON(accepted) }));
if (phone.retrying) await phone.retrying;
const {readFile}=await import('node:fs/promises');
const {stripTypeScriptTypes}=await import('node:module');
const {runInNewContext}=await import('node:vm');
const model=await import('../web/src/model.ts');
const source=stripTypeScriptTypes((await readFile(new URL('../web/src/features/Message.model.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const presentation=runInNewContext(source+'\n({answerTo,requestState,working})',{...model});
const pid2=wire.newID(),group=wire.newID(),targetOf=e=>({address:e.address,fingerprint:e.fp});
const inv2=await wire.signEvent(host.keys,{conv,pid:pid2,type:'invite',ts:1,author:host.author(),host:{person:laptop.me.person,address:laptop.address,fingerprint:laptop.fp},audience:'conversation'});
await receive(await from(host,{sub:'event',pid:pid2,body:wire.eventJSON(inv2)}));
const acc2=await wire.signEvent(laptop.keys,{conv,pid:pid2,type:'accept',prev:await wire.eventHash(inv2),ts:2,author:laptop.author()});
await receive(await from(laptop,{sub:'event',pid:pid2,body:wire.eventJSON(acc2)}));
const asks=[],now=Math.floor(Date.now()/1000);
for(const [p,target] of [[pid,host],[pid2,laptop]]){
 const lid=wire.newID(),rows=[];
 for(const recipient of [host,laptop,peer]){
  const id=wire.newID(),item={v:2,id,lid,conv,root:wire.rootJSON(root),from:phone.address,to:recipient.address,kind:'question',ts:now,body:'Grouped exact-agent question',pid:p,target:targetOf(target),origin:'ui',send_group:group,replica:recipient.me.person===phone.me.person&&recipient!==target};
  const envelope=await wire.seal(item,phone.keys,recipient.pub);
  rows.push({...item,envelope,at:now*1000,own:recipient.me.person===phone.me.person,person:recipient.me.person,state:'queued',handover_started:true});
 }
 await phone.store.write(rows.map(v=>({s:'outbox',k:v.id,v})));
 asks.push({lid,pid:p,target,rows});
}
const answered=asks[0],response=await from(host,{kind:'answer',ts:now,body:'Verified exact first answer',pid:answered.pid,reply_to:answered.lid,origin:'agent:claude',emotion:'happy',replica:false});
const responseID=await receive(response);assert.equal(await phone.store.get('held',responseID),undefined);
let projected=await phone.dm(conv);
assert.ok(projected.messages.find(r=>r.id===responseID).verified_agent);
const first=projected.messages.find(r=>r.lid===asks[0].lid),second=projected.messages.find(r=>r.lid===asks[1].lid),answer=projected.messages.find(r=>r.id===responseID);
assert.equal(answer.reply_to,first.id);assert.equal(first.delivery,'queued');assert.equal(second.delivery,'queued');
assert.equal(presentation.requestState(first,projected.messages,{overview:await phone.overview()}).text,'Answered');
assert.equal(presentation.requestState(second,projected.messages).text,'Sending');
assert.ok((await phone.store.all('outbox')).every(r=>r.state==='queued'));
const wrong=await from(laptop,{kind:'answer',body:'Wrong exact executor',pid:answered.pid,reply_to:answered.lid,origin:'agent:claude',emotion:'happy',replica:false}),wrongID=await receive(wrong);
assert.equal((await phone.store.get('held',wrongID)).reason,'invalid');assert.equal(await phone.store.get('inbox',wrongID),undefined);
const unrelated=await from(host,{kind:'answer',body:'Unrelated reference',pid:answered.pid,reply_to:wire.newID(),origin:'agent:claude',emotion:'happy',replica:false}),unrelatedID=await receive(unrelated);
assert.notEqual((await phone.store.get('inbox',unrelatedID))?.reply_to,asks[0].lid);assert.notEqual((await phone.store.get('inbox',unrelatedID))?.reply_to,asks[1].lid);
const all=projected.messages,ctx={overview:await phone.overview()};
assert.equal(presentation.answerTo(first,all,ctx).id,answer.id);
for(const extra of [{reply_to:second.id},{pid:second.pid},{from:laptop.address},{verified_agent:false},{excerpt_pid:first.pid,history:true},{_local:true},{kind:'message'},{agent_id:wire.newID()}]){
 const changed={...answer,...extra};
 assert.equal(presentation.answerTo(first,[first,changed],ctx),undefined,'different executor, reference or inert copy grants no completion '+JSON.stringify(extra));
}
assert.equal(presentation.answerTo(first,[first,{...answer,reply_to:first.lid},{...second,lid:first.lid}],ctx),undefined,'ambiguous logicalID cannot correlate');
assert.equal(presentation.answerTo(first,[first,{...answer,history:true}],ctx).id,answer.id,'verified retained original can still prove its exact answer');
assert.equal(presentation.requestState({...first,kind:'task'},[{...first,kind:'task'},answer],ctx).text,'Done','existing answer/result completion semantics are retained');
assert.equal(presentation.requestState(first,[first,{...answer,status:'proposal'}],ctx).text,'Suggested a task · not run');
assert.equal(presentation.working({...first,exec:{state:'running'}},[first,answer],ctx),false);
const native=JSON.parse(process.env.AGENTNET_COMPLETION_DTOS||'{}');
if(native.request){
 const nctx={overview:{me:{address:native.local}}};
 assert.equal(presentation.answerTo(native.incoming,[native.incoming,native.manual],nctx).id,native.manual.id,'native incoming request To filled by Conversation supports a local manual reply');
 assert.equal(presentation.answerTo(native.incoming,[native.incoming,{...native.manual,from:'wrong/host'}],nctx),undefined);
 assert.equal(presentation.requestState(native.request,[native.request,native.answer],nctx).text,'Answered','native From-empty self answer uses current device context');
 for(const bad of [{from:'wrong/device'},{agent_id:'wrong-agent'},{reply_to:'unrelated'}])assert.equal(presentation.answerTo(native.request,[native.request,{...native.answer,...bad}],nctx),undefined);
 const remote={...native.request,to:'remote/host',target:{address:'remote/host',fingerprint:'remote-key',agent_id:'chosen'}};
 const reply={...native.answer,from:'remote/host',dir:'in',agent_id:'chosen'};
 assert.equal(presentation.answerTo(remote,[remote,reply],nctx).id,reply.id);
 assert.equal(presentation.answerTo(remote,[remote,{...reply,history:true,from_key:'different-key'}],nctx),undefined,'retained named answer exact key differs');
 assert.equal(presentation.answerTo(remote,[remote,{...reply,history:true,from_key:'remote-key'}],nctx).id,reply.id);
 assert.equal(presentation.answerTo(remote,[remote,native.answer],nctx),undefined,'local human answer cannot impersonate remote executor');
 const defaultAsk={...native.request,target:undefined},defaultAnswer={...native.answer,agent_id:undefined};
 assert.equal(presentation.answerTo(defaultAsk,[defaultAsk,defaultAnswer],nctx).id,defaultAnswer.id,'default local responder does not require a named agent');
}
if(process.env.AGENTNET_COMPLETION_PROJECTION){
 const {writeFile}=await import('node:fs/promises');
 await writeFile(process.env.AGENTNET_COMPLETION_PROJECTION,JSON.stringify({overview:ctx.overview,thread:projected,first:first.id,second:second.id,answer:answer.id}));
}
for(const d of devices)await d.close();
console.log('Signed exact reply completion and native DTO parity PASS');
