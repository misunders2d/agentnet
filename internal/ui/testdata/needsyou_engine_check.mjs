// Needs-you and agent views with an invitation's claimed time (the inviter
// writes it): real Engine methods over synthetic records, no network. A
// claim no Date can hold (9e12 s) or past year 9999 never throws: the
// invitation is listed at the time its first record reached this browser,
// or at a plausible claim when that record is not a row here, else now;
// the agent view leaves an implausible claim out.
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";

const now = Date.UTC(2026, 9, 4, 12, 0, 0);
const e = new Engine({ store: memoryStore(), base: "https://synthetic.invalid", now: () => now,
  fetch: async () => { throw Error("no network in this check"); } });
e.address = "admin/tablet"; e.fp = "f".repeat(8); e.me = { person: "p".repeat(64), label: "Alice", devices: [] };
const conv = "c".repeat(64), pid = "1".repeat(32), received = Date.UTC(2026, 9, 3, 9, 30, 0);
const info = (invited) => ({ pid, role: "", state: "invited", held: 0, grant: [], taskKeys: [], note: "help", invited,
  host: { person: e.me.person, address: "admin/laptop", fingerprint: "a".repeat(8), label: "Alice", devices: [] },
  inviter: { person: "b".repeat(64), address: "bob/desk", fingerprint: "b".repeat(8), label: "Bob", devices: [] } });
const listed = (invited, msgs) => {
  const needs = [], held = [];
  e.needsYouOf(conv, [info(invited)], msgs, [], needs, held);
  assert.equal(needs.length, 1, "the invitation is listed");
  assert.equal(held.length, 0);
  return needs[0];
};
const record = [{ sub: "event", pid, at: received, body: "" }, { sub: "event", pid, at: received + 5000, body: "" }];
for (const claim of [9e12, 1e12, 253402300799, 1759500000, -5, NaN])
  assert.equal(listed(claim, record).at, new Date(received).toISOString(), "listed at its first record's arrival, claim " + claim);
for (const [claim, at] of [[9e12, now], [1e12, now], [253402300799, now], [-5, now], [NaN, now], [0, now], [1759500000, 1759500000e3]])
  assert.equal(listed(claim, []).at, new Date(at).toISOString(), "no record row here: claim " + claim);
for (const [claim, shown] of [[9e12, ""], [1e12, ""], [253402300799, ""], [-5, ""], [0, ""], [1759500000, new Date(1759500000e3).toISOString()]])
  assert.equal(e.agentView(info(claim), [], null).invited, shown, "the agent view's invited, claim " + claim);
// This browser's request to the laptop's agent is listed, read-only, in
// the states client.PageReview lists it in on the laptop: awaiting,
// needs_human, interrupted, and running. A running request remains visible
// while the laptop executes it; opening this browser never stops it.
const active = { ...info(1759500000), state: "active" };
const req = { id: "2".repeat(32), lid: "3".repeat(32), pid, kind: "task", body: "rotate the key", at: received,
  target: { address: "admin/laptop", fingerprint: "a".repeat(8) } };
const status = (state, at = now / 1000 - 60) => ({ sub: "status", from: "admin/laptop", lid: "4".repeat(32), ref: { id: req.lid, fingerprint: e.fp },
  body: JSON.stringify({ state, n: 1, at }) });
const laptopWords = (a) => a === "admin/laptop" ? "Alice · Laptop" : a; // the page passes its device words
const listedNow = (presence, suspended = []) => { e.members = { listed: "listed", current: true, at: now, list: [{ address: "admin/laptop", presence, joined: 1 }], truncated: false }; e.suspendedDevices = suspended; };
const requestNeeds = (statuses, extra = {}) => { const needs = [], held = []; e.needsYouOf(conv, [active], [{ ...req, ...extra }], statuses, needs, held, laptopWords); return needs; };
listedNow("connected");
for (const [state, reason] of [["awaiting", "agent_awaiting"], ["needs_human", "agent_needs_human"], ["interrupted", "agent_interrupted"], ["running", "agent_running"]]) {
  const needs = requestNeeds([status(state)]);
  assert.deepEqual(needs.map((n) => n.reason), reason ? [reason] : [], "a request the laptop reports " + state);
  if (reason) assert.equal(needs[0].decide_on, "admin/laptop", "decided on the laptop");
  assert.equal(needs[0].stale, undefined, "a current word of a connected laptop");
  if (state === "running") assert.match(needs[0].why, /^Running on Alice · Laptop\./);
  if (state === "awaiting") assert.match(needs[0].why, /^Decide on Alice · Laptop\./, "a current decision waits on the laptop");
}
// Only a current word is a decision: a stale one (the laptop not connected
// now, suspended until it updates, a copy waiting for its update, or a run
// older than an hour) reports no result and asks nothing, whatever it said.
const stale = (needs, words) => {
  assert.equal(needs.length, 1);
  assert.equal(needs[0].stale, true, "marked as no current word");
  assert.equal(needs[0].why, "No result reported · " + words);
  assert.doesNotMatch(needs[0].why, /Decide on|Running on/, "never a pending decision or live work");
  assert.equal(needs[0].decide_on, "admin/laptop", "still listed apart, never decided here");
  assert.equal(needs[0].actions, undefined, "nothing to do here");
};
listedNow("offline");
for (const state of ["awaiting", "needs_human", "running"]) stale(requestNeeds([status(state)]), "Alice · Laptop is not connected now");
listedNow("connected", ["admin/laptop"]);
stale(requestNeeds([status("awaiting")]), "Alice · Laptop is suspended until it updates AgentNet");
listedNow("reconnecting");
stale(requestNeeds([status("running")], { copies: [{ to: "admin/laptop", state: "waiting", detail: "peer_update: admin/laptop cannot read human participation yet" }] }), "Alice · Laptop needs an AgentNet update");
listedNow("connected");
stale(requestNeeds([status("running", now / 1000 - 7200)]), "Alice · Laptop has not reported on it for over an hour");
e.members = { listed: "unknown", current: false, at: 0, list: [], truncated: false }; e.suspendedDevices = [];
// A turn held for the person (conv_held: nothing runs it) is answered by
// the person's own later turn in that conversation, as
// client.turnClosesHeld closes it: one sent from this browser (no key of
// another device) or from another device of theirs (own). A request to an
// agent, an agent's output or an earlier turn answers nothing.
const heldTurn = { id: "5".repeat(32), lid: "6".repeat(32), kind: "question", body: "decide please", at: received, fp: "b".repeat(8), from: "bob/desk", state: "conv_held", read: true };
const turn = (n, extra) => ({ id: String(n).repeat(32), lid: String(n + 1).repeat(32), kind: "message", body: "yes", at: received + 1000, ...extra });
const heldIds = (msgs) => { const needs = [], held = []; e.needsYouOf(conv, [], msgs, [], needs, held); return held.map((h) => h.id); };
assert.deepEqual(heldIds([heldTurn]), [heldTurn.id], "held until answered");
assert.deepEqual(heldIds([heldTurn, turn(7)]), [], "answered from this browser");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000 + 1 })]), [], "answered from another device of the person");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000 - 60, at: received + 5000 })]), [heldTurn.id], "a turn written before it arrived, delivered late, answers nothing");
assert.deepEqual(heldIds([{ ...heldTurn, at: received + 400 }, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000, at: received + 5000 })]), [heldTurn.id], "a turn stamped in the second it arrived may have been written before it: it answers nothing");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), origin: "agent:claude", kind: "answer" })]), [heldTurn.id], "an agent's output answers nothing");
assert.deepEqual(heldIds([heldTurn, turn(7, { kind: "question", target: { address: "admin/laptop", fingerprint: "a".repeat(8) } })]), [heldTurn.id], "a request to an agent answers nothing");
assert.deepEqual(heldIds([turn(7, { at: received - 1000 }), heldTurn]), [heldTurn.id], "an earlier turn answers nothing");
console.log("PASS needs-you claimed invitation times: arrival, plausible claim, or now; agent view leaves implausible claims out; requests listed as their host currently reports them, interrupted included, and a stale word as no result reported, never a decision; held turns answered by the person's later turn");

// Explicitly handling an ordinary human question is local and inert, matching
// native Agent.Resolve. Reading alone never resolves it; no remote job is touched.
const heldRecord = {...heldTurn, v:2, conv, read:true};
const otherHeld = {...heldRecord,id:"7".repeat(32),lid:"8".repeat(32)};
await e.store.write([{s:"inbox",k:heldRecord.id,v:heldRecord},{s:"inbox",k:otherHeld.id,v:otherHeld}]);
// Storage assigns local history indexes on insertion. Resolve may change only
// state: compare against the stored snapshot, including that bookkeeping.
const heldBefore=await e.store.get("inbox",heldRecord.id),otherBefore=await e.store.get("inbox",otherHeld.id);
const projected=[],heldProjected=[];
e.needsYouOf(conv,[],[heldRecord],[],projected,heldProjected);
assert.deepEqual(heldProjected[0].actions,["resolve"],"read human turn still offers explicit local handling");
const resolved=await e.apiRequest("/api/act",{do:"resolve",id:heldRecord.id});
assert.match(resolved.note,/this device/);
assert.deepEqual(await e.store.get("inbox",heldRecord.id),{...heldBefore,state:"resolved"},"preserves original message bytes and stored history indexes");
assert.deepEqual(await e.store.get("inbox",otherHeld.id),otherBefore,"another held turn stays waiting");
const reloaded=new Engine({store:e.store,base:"https://synthetic.invalid",fetch:async()=>{throw Error("resolve attempted network");}});
const afterReload=[],heldAfterReload=[];
reloaded.needsYouOf(conv,[],await e.store.all("inbox"),[],afterReload,heldAfterReload);
assert.deepEqual(heldAfterReload.map(x=>x.id),[otherHeld.id],"local handling survives engine reload");
assert.deepEqual(await e.store.all("outbox"),[],"resolve sends neither reply nor status");
assert.deepEqual(await e.store.all("receipts"),[],"resolve changes no transport receipt");
for(const [i,extra] of [{state:"awaiting"},{state:"needs_human"},{state:"running"},{state:"held"},{kind:"message"},{own:true},{sub:"event"},{control:true},{v:1},{conv:""},{target:{address:"admin/laptop",agent_id:"builder"}}].entries()) {
 const rejected={...heldRecord,id:(i+20).toString(16).padStart(32,"0"),...extra};
 await e.store.write([{s:"inbox",k:rejected.id,v:rejected}]);
 const before=await e.store.get("inbox",rejected.id);
 await assert.rejects(()=>e.apiRequest("/api/act",{do:"resolve",id:rejected.id}));
 assert.deepEqual(await e.store.get("inbox",rejected.id),before,"refused item unchanged");
}
for(const reason of ["invalid","proof_pending","key_changed","identity_conflict","conflicting_duplicate"]) {
 const q={id:"e".repeat(32),reason,envelope:"SYNTHETIC_RETAINED_BYTES"};
 await e.store.write([{s:"held",k:q.id,v:q}]);
 await assert.rejects(()=>e.apiRequest("/api/act",{do:"resolve",id:q.id}));
 assert.deepEqual(await e.store.get("held",q.id),q,"security hold remains blocked");
}
await assert.rejects(()=>e.apiRequest("/api/act",{do:"resolve",id:"missing"}));
console.log("PASS ordinary held turn resolve: exact local state, reload, no sends, unrelated work and security holds refused");

// Full private output stays whole, bound to the current own executor/key,
// the original request/key, and the latest report snapshot.
const hostFP = "a".repeat(8), full = "Which branch?\n\nUse release.\n" + "長い行🙂".repeat(150);
e.me.devices = [{address:active.host.address,fingerprint:hostFP}];
await e.store.write([{s:"pins",k:active.host.address,v:{fingerprint:hostFP}}]);
const request = {...req,target:{address:active.host.address,fingerprint:hostFP}};
const reportItem = {id:request.lid,from:e.address,key:e.fp,kind:"task",state:"needs_human",conv:true,excerpt:full,actionable:false};
const notice = {id:"9".repeat(32),at:now,v:1,kind:"message",status:"review_notice",from:active.host.address,fp:hostFP,body:JSON.stringify({v:2,at:1759500000,host:active.host.address,items:[reportItem]})};
const reports = await e.ownNeedsYouReports([notice], []);
assert.equal(e.needsYouText(request,reports),full,"the whole multi-paragraph message survives");
assert.equal(e.reportItems([notice])[0].report.items[0].actionable,false,"read-only visibility grants nothing");
assert.equal(e.needsYouText({...request,target:{...request.target,fingerprint:"b".repeat(8)}},reports),"","changed target key");
assert.equal(e.needsYouText({...request,lid:"0".repeat(32)},reports),"","another request");
assert.equal(e.needsYouText({...request,fp:"b".repeat(8)},reports),"","another requester's key");
const settled = {...notice,id:"8".repeat(32),body:JSON.stringify({v:2,at:1759500001,host:active.host.address,items:[]})};
assert.equal(e.needsYouText(request,await e.ownNeedsYouReports([notice,settled],[])),"","settled snapshot clears stale needs-you");
await e.store.write([{s:"pins",k:active.host.address,v:{fingerprint:hostFP,pending:{}}}]);
assert.deepEqual(await e.ownNeedsYouReports([notice],[]),[],"pending host key is blocked");
await e.store.write([{s:"pins",k:active.host.address,v:{fingerprint:hostFP}}]);
e.me.devices=[];
assert.deepEqual(await e.ownNeedsYouReports([notice],[]),[],"removed own device is blocked");
console.log("PASS full needs-you reports: whole text, exact current owner and request keys, read-only, latest snapshot, pending/removal fences");
