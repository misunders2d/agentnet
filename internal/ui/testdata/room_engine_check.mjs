// ROOM_V1 §2.2 and §8 in the browser engine, as the core resolves rooms
// (client participation.go): real Engine methods over synthetic records,
// no network. A room invitation and its exact scope count; the
// participation carries its audience and end time; scopes standing for an
// invitation not held must agree on audience, end time and group binding;
// with the invitation held only its exact projection counts; in a group a
// room scope counts once its binding verifies (dmMembers' groupInvites),
// never for an invitation held here;
// past its end time, by this device's clock, it counts as ended.
import assert from "node:assert/strict";
import * as wire from "../static/wire.mjs";
import { Engine, memoryStore } from "../static/engine.mjs";

const now = Date.UTC(2026, 9, 4, 12, 0, 0), sec = Math.floor(now / 1000);
const e = new Engine({ store: memoryStore(), base: "https://synthetic.invalid", now: () => now, fetch: async () => { throw Error("no network in this check"); } });
const id = (c) => c.repeat(32), h64 = (c) => c.repeat(64), fp = (c) => [c, c, c, c].map((x) => x.repeat(8)).join("-");
const alice = { person: id("a"), hashes: [h64("a")], devices: [{ address: "alice/desk", fingerprint: fp("a") }] };
const bob = { person: id("b"), hashes: [h64("b")], devices: [{ address: "bob/laptop", fingerprint: fp("b") }] };
const members = (group) => { const m = new Map([[alice.person, alice], [bob.person, bob]]); m.hosts = new Map(); if (group) { m.group = {}; m.epochs = new Map([[fp("a"), h64("1")], [fp("b"), h64("2")]]); m.groupInvites = new Set(); } return m; };
const conv = h64("c");
const author = (group) => ({ person: alice.person, roster: h64("a"), address: "alice/desk", fingerprint: fp("a"), ...(group ? { group_admission: h64("1") } : {}) });
const invite = (fields = {}) => ({ v: 1, conv, pid: fields.pid || wire.newID(), type: "invite", prev: "", author: author(!!fields.group), ts: sec - 60,
  host: { person: bob.person, address: "bob/laptop", fingerprint: fp("b") }, grant: null, audience: "room", task_keys: null, note: "", ...fields });
const rec = async (ev) => ({ e: ev, hash: await wire.eventHash(ev) });
const accept = async (inv) => ({ v: 1, conv, pid: inv.pid, type: "accept", prev: await wire.eventHash(inv), ts: sec - 30, author: { person: bob.person, roster: h64("b"), address: "bob/laptop", fingerprint: fp("b"), ...(inv.group ? { group_admission: h64("2") } : {}) },
  host: null, grant: null, audience: "", task_keys: null, note: "" });
for (const ev of [invite(), invite({ until: sec + 3600 })]) wire.validateEvent(ev);

// The invitation, its exact scope and the host's accept: active, a room participant.
const inv = invite({ until: sec + 3600 }), scope = await wire.scopeOf(inv, sec - 50), acc = await accept(inv);
let info = e.resolveAgent(inv.pid, [await rec(inv), await rec(scope), await rec(acc)], members(false));
assert.equal(info.state, "active");
assert.equal(info.audience, "room");
assert.equal(info.until, sec + 3600);
assert.equal(info.scope, await wire.eventHash(scope), "the exact projection counts");
// With the invitation held, a scope that disagrees on the end time is not its scope.
const other = { ...scope, until: sec + 7200 };
info = e.resolveAgent(inv.pid, [await rec(inv), await rec(other), await rec(acc)], members(false));
assert.equal(info.state, "active");
assert.equal(info.scope, "", "a scope with another end time is not the invitation's projection");

// Scopes standing for an invitation not held: equal ones count; ones that
// disagree on audience or end time are a conflict.
const lone = invite({ until: sec + 3600 });
info = e.resolveAgent(lone.pid, [await rec(await wire.scopeOf(lone, sec - 50)), await rec(await wire.scopeOf(lone, sec - 40))], members(false));
assert.equal(info.state, "invited");
assert.equal(info.audience, "room");
assert.equal(info.until, sec + 3600);
for (const change of [{ until: sec + 1 }, { audience: "conversation", until: 0 }]) {
  const x = { ...await wire.scopeOf(lone, sec - 40), ...change };
  info = e.resolveAgent(lone.pid, [await rec(await wire.scopeOf(lone, sec - 50)), await rec(x)], members(false));
  assert.equal(info.state, "conflict", "scopes disagreeing on " + Object.keys(change).join(","));
}

// Past its end time it counts as ended here.
const past = invite({ until: sec - 1 });
info = e.resolveAgent(past.pid, [await rec(past), await rec(await accept(past))], members(false));
assert.equal(info.state, "dismissed");
assert.equal(info.dismissal, "");

// In a group: a room scope with its invitation's binding counts once that
// binding verifies (dmMembers lists its hash), and not otherwise.
const ginv = invite({ group: { seq: 3, hash: h64("e"), host_role: "member", host_admission: h64("2"), task_admissions: null } });
wire.validateEvent(ginv);
const gscope = await wire.scopeOf(ginv, sec - 50), gacc = await accept(ginv);
wire.validateEvent(gscope);
const m = members(true);
info = e.resolveAgent(ginv.pid, [await rec(gscope), await rec(gacc)], m);
assert.equal(info.invite, "", "an unverified group binding does not count");
assert.ok(info.held > 0);
m.groupInvites.add(await wire.eventHash(gscope));
info = e.resolveAgent(ginv.pid, [await rec(gscope), await rec(gacc)], m);
assert.equal(info.state, "active");
assert.equal(info.scope, await wire.eventHash(gscope));
assert.equal(info.external, false);
// The invitation itself held here (its binding does not verify): its
// verified scope does not stand in for it (client resolve).
info = e.resolveAgent(ginv.pid, [await rec(ginv), await rec(gscope), await rec(gacc)], m);
assert.equal(info.invite, "", "a scope does not stand in for an invitation held here");
assert.notEqual(info.state, "active");
assert.ok(info.held > 0);
console.log("PASS room engine: room audience and end time resolved, exact projection only, agreeing scopes, end time by this clock, group scope counted once its binding verifies, never for an invitation held here");

// P6 consent is recorded while the invitation counts, not inferred after leave.
const permanent=invite({group:{seq:3,hash:h64("e"),host_role:"member",host_admission:h64("2"),task_admissions:null}});
const pevents=[await rec(permanent),await rec(await wire.scopeOf(permanent,sec-50)),await rec(await accept(permanent))];
const pm=members(true);for(const x of pevents)pm.groupInvites.add(x.hash);
const pinfo=e.resolveAgent(permanent.pid,pevents,pm),saved=e.roomConsentOps(conv,pevents,pinfo);
assert.equal(saved.length,3);assert.equal(pinfo.member,true);
pm.roomEvents=new Set(pevents.map(x=>x.hash));pm.roomAuthors=new Map([[alice.person,alice],[bob.person,bob]]);pm.delete(alice.person);pm.epochs.delete(fp("a"));
assert.equal(e.resolveAgent(permanent.pid,pevents,pm).state,"active","accepted member survives inviter departure");
const changed=structuredClone(alice);changed.devices[0].fingerprint=fp("f");pm.roomAuthors.set(alice.person,changed);
assert.notEqual(e.resolveAgent(permanent.pid,pevents,pm).state,"active","cached consent does not bypass changed author keys");
console.log("PASS P6 permanent membership: accepted consent persists after inviter leaves; changed keys stay held");

// Record every hosted membership on an ordinary group turn, including another
// PID than the event that just arrived. P26: a received turn's reader fence
// uses its verified signed fan, not the members present at arrival.
const second=invite({group:permanent.group}), secondEvents=[await rec(second),await rec(await wire.scopeOf(second,sec-50)),await rec(await accept(second))];
const all=[...pevents,...secondEvents], cm=members(true);
for(const x of all)cm.groupInvites.add(x.hash);
const turn={lid:id("d"),fan:[{person:alice.person,roster:h64("a")},{person:bob.person,roster:h64("b")}]};
const unproven=await e.roomStoredOps(conv,[],{lid:turn.lid},fp("a"),all,cm);
assert.equal(unproven.filter(o=>o.k.startsWith('room-reader/')).length,0,'missing signed fan fabricates no readers');
const ops=await e.roomStoredOps(conv,[],turn,fp("a"),all,cm);
assert.equal(ops.filter(o=>o.k.startsWith('room-event/')).length,6,'all permanent consents saved from ordinary turn');
assert.equal(ops.filter(o=>o.k.startsWith('room-reader/')).length,2,'exact admissions saved for the turn');
assert(ops.some(o=>o.k===e.roomReaderKey(conv,{lid:id("d"),fingerprint:fp("a")},bob.person,h64("2"))));
cm.roomEvents=new Set(all.map(x=>x.hash));cm.roomAuthors=new Map([[alice.person,alice],[bob.person,bob]]);cm.delete(alice.person);cm.epochs.delete(fp("a"));
for(const x of [permanent,second])assert.equal(e.resolveAgent(x.pid,all,cm).state,'active','every saved consent survives inviter departure');

// A signed share is metadata, not authority to read unselected earlier turns.
const grant={lid:id('e'),fingerprint:fp('a')},share={...permanent,type:'share',prev:await wire.eventHash(permanent),grant:[grant]}, sr=await rec(share), sm=members(true);
for(const x of [...pevents,sr])sm.groupInvites.add(x.hash);
sm.shareGrants=new Map();
assert.deepEqual(e.resolveAgent(permanent.pid,[...pevents,sr],sm).grant,[],'unseen share has no granted body');
sm.shareGrants.set(sr.hash,[grant]);
assert.deepEqual(e.resolveAgent(permanent.pid,[...pevents,sr],sm).grant,[grant],'visible reference can be shared');
assert.equal(permanent.grant,null,'resolving a share must not mutate the original signed invite');

// Native/browser parity: legacy visual membership never claims permanence;
// ordinary active member state is preserved separately from real warnings.
e.me={...alice,label:'Alice'};e.address='alice/desk';e.fp=fp('a');
const viewInfo={...pinfo,host:{...bob,address:'bob/laptop',fingerprint:fp('b'),label:'Bob'},inviter:{...alice,address:'alice/desk',label:'Alice'},inviters:[],grant:[],taskKeys:[],shares:[],held:0};
const people=[{...alice,label:'Alice',admin:false},{...bob,label:'Bob'}];
let view=e.agentView({...viewInfo,member:false,audience:'conversation',until:sec+60},[],null,people,'member');
assert.equal(view.member,true);assert(!view.state_text.includes('Stays until explicitly removed'));
view=e.agentView({...viewInfo,member:true,external:true},[],null,people,'member');
assert(view.state_text.includes('receives every new message and file'));
for(const topic of ['',id('e')]){
 const scoped=e.agentView({...viewInfo,member:true,external:true,topic},[],null,people,'member');
 assert(scoped.state_text.includes(topic?'only in this topic':'only in Main flow'));
 assert(!scoped.state_text.includes('every new message and file'),'topic-only member cannot promise whole-chat access');
}
view=e.agentView({...viewInfo,member:true,host:{...alice,address:e.address,label:'Alice'}},[],null,people,'member');
assert(view.state_text.includes('browser'));assert(!view.state_text.includes('Stays until explicitly removed'));
view=e.agentView({...viewInfo,member:true,held:1},[],null,people,'member');
assert(view.state_text.includes('do not count here yet'));assert(!view.state_text.includes('Stays until explicitly removed'));

// Ordinary turns use the captured audience whenever members follow the room.
const route=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch:async()=>{throw Error('no network');}});
route.convEvents=async()=>pevents;route.dmMembers=async()=>sm;
route.roomPlan=async()=>({audience:[{pid:permanent.pid}],proof:[]});
route.sendHumanTurn=async(c,n,h)=>{assert.equal(n.kind,'message');assert.equal(h.audience[0].pid,permanent.pid);return 'captured';};
assert.equal(await route.sendGroupTurn({id:conv,kind:'group'},{body:'future ordinary turn'}),'captured');
// Admission check rejects a non-admin before looking up the outside device.
route.me={person:alice.person};route.address='alice/desk';
route.groupRecord=async()=>({id:conv,kind:'group'});
route.dmMembers=async()=>{const m=new Map([[alice.person,alice]]);m.group={state:{members:[{person:alice.person,admin:false}]}};return m;};
await assert.rejects(()=>route.inviteAgent({conv,host:'outside/desk',agent_id:id('f')}),/administrator/);
const outsideAccept=await accept(inv);
const disclosure=e.eventText(wire.eventJSON(outsideAccept),null,[people[0]]);
assert(disclosure.includes('participates with the scope of its invitation'));
assert(!disclosure.includes('every new message and file'),'acceptance alone cannot infer the invitation’s scope');
assert(!disclosure.includes(outsideAccept.author.address),'outside event has no raw host address');
console.log('PASS P6 fixes engine: all-PID consent, reader epochs, share visibility, legacy/warning/disclosure parity, ordinary captured sends and outside admin gate');
