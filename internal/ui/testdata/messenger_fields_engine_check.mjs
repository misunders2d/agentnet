// What the new messenger reads from the browser device's engine, as the
// daemon's page gives it (internal/ui livedm.go, live.go): real Engine
// methods over a real DM root and person records, with the conversation's
// rows written directly (no network).
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

let serial = 1, checks = 0;
const id = () => (serial++).toString(16).padStart(32, "0"), now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };

async function fixture() {
  const store = memoryStore();
  const e = new Engine({ store, now: () => now, base: "https://synthetic.invalid", fetch: async (url) => { throw new Error("no network in this check: " + url); } });
  e.keys = await wire.newKeys(); e.address = "self/tablet";
  const selfPub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(selfPub);
  const selfRoster = await wire.newRoster(e.keys, e.address, "Self"); e.me = await e.personRecord([selfRoster], "self", null);
  const peerKeys = await wire.newKeys(), peerAddress = "peer/desk", peerPub = await wire.publicEntry(peerKeys, peerAddress);
  const peerRoster = await wire.newRoster(peerKeys, peerAddress, "Peer"), peer = await e.personRecord([peerRoster], "pinned", null);
  const root = await wire.newRoot(e.keys, { person: e.me.person, roster: e.me.hash, address: e.address, fingerprint: e.fp }, { person: peer.person, roster: peer.hash });
  const conv = await wire.rootID(root), c = { id: conv, root: wire.rootJSON(root), peer: peer.person, created: Math.floor(now / 1000), creator: e.address };
  await store.write([{ s: "kv", k: "identity", v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: "kv", k: "person", v: e.me },
    { s: "persons", k: peer.person, v: peer }, { s: "pins", k: peerAddress, v: { address: peerAddress, json: wire.marshalPublic(peerPub), fingerprint: peer.fingerprint, pending: null } }, { s: "convs", k: conv, v: c }]);
  e.members = { listed: "listed", current: true, at: 1, list: [], truncated: false };
  return { e, store, conv, peer, peerAddress, peerKeys };
}
const sent = (f, fields) => ({ conv: f.conv, kind: "message", body: "", reply_to: "", origin: "ui", sub: "", pid: "", target: null, at: now, own: false, state: "delivered", ...fields });
const received = (f, fields) => ({ v: 2, from: f.peerAddress, fp: f.peer.fingerprint, kind: "message", body: "", reply_to: "", ts: Math.floor(now / 1000), at: now, read: true, status: "",
  conv: f.conv, sub: "", pid: "", origin: "", emotion: "", replica: false, target: null, own: false, state: "", ...fields });
const shown = (view, body) => view.messages.find((m) => m.body === body);

// 1. A reply names its author's copy: an answer names its executor's copy
// of the request, which this device shows under another copy's id when it
// sent the request (two copies, one shown), or under its logical id. The
// view links each to the request as shown here, and an answered request
// shows no execution state.
{
  const f = await fixture(), { e, store } = f;
  const pid = id(), lid = id(), a = id(), b = id(), target = { address: f.peerAddress, fingerprint: f.peer.fingerprint };
  await store.write([
    { s: "outbox", k: a, v: sent(f, { id: a, lid, kind: "question", body: "is it green?", pid, target, to: f.peerAddress }) },
    { s: "outbox", k: b, v: sent(f, { id: b, lid, kind: "question", body: "is it green?", pid, target, to: "self/laptop", own: true }) },
  ]);
  const asked = shown(await e.dm(f.conv), "is it green?");
  const executorCopy = asked.id === a ? b : a; // the copy not shown
  const lidAsk = id(), inID = id();
  await store.write([
    { s: "inbox", k: id(), v: received(f, { id: "ans".padEnd(32, "0"), lid: id(), kind: "answer", body: "green", reply_to: executorCopy, pid }) },
    { s: "inbox", k: inID, v: received(f, { id: inID, lid: lidAsk, body: "see this" }) },
    { s: "inbox", k: id(), v: received(f, { id: "rep".padEnd(32, "0"), lid: id(), body: "and that", reply_to: lidAsk }) },
    { s: "inbox", k: id(), v: received(f, { id: "unk".padEnd(32, "0"), lid: id(), body: "about something else", reply_to: "f".repeat(32) }) },
  ]);
  const view = await e.dm(f.conv);
  check(shown(view, "green").reply_to === asked.id, "an answer naming the executor's copy links to the request as shown: " + shown(view, "green").reply_to + " vs " + asked.id);
  check(!shown(view, "is it green?").exec, "the answered request shows no execution state");
  check(shown(view, "and that").reply_to === inID, "a reply naming a logical id links to that message as shown");
  check(shown(view, "about something else").reply_to === "f".repeat(32), "a reply to a message not here stays as sent");
  e.stop();
}

// 1b. A logical id is unique per sender key only: the other person may
// send a message under the logical id of a request it saw. A reply naming
// that logical id then names no one message here and stays as sent; a
// reply naming the request's copy still links to it.
{
  const f = await fixture(), { e, store } = f;
  const pid = id(), lid = id(), a = id(), b = id(), target = { address: f.peerAddress, fingerprint: f.peer.fingerprint };
  await store.write([
    { s: "outbox", k: a, v: sent(f, { id: a, lid, kind: "question", body: "is it green?", pid, target, to: f.peerAddress }) },
    { s: "outbox", k: b, v: sent(f, { id: b, lid, kind: "question", body: "is it green?", pid, target, to: "self/laptop", own: true }) },
  ]);
  const asked = shown(await e.dm(f.conv), "is it green?");
  const executorCopy = asked.id === a ? b : a, clash = id();
  await store.write([
    { s: "inbox", k: clash, v: received(f, { id: clash, lid, body: "same logical id", at: now + 1 }) },
    { s: "inbox", k: id(), v: received(f, { id: "byl".padEnd(32, "0"), lid: id(), kind: "answer", body: "by logical id", reply_to: lid, pid, at: now + 2 }) },
    { s: "inbox", k: id(), v: received(f, { id: "byc".padEnd(32, "0"), lid: id(), kind: "answer", body: "by copy", reply_to: executorCopy, pid, at: now + 3 }) },
  ]);
  const view = await e.dm(f.conv);
  check(shown(view, "same logical id").id === clash, "the message under the same logical id is shown");
  check(shown(view, "by logical id").reply_to === lid, "a reply naming a logical id two keys used stays as sent: " + shown(view, "by logical id").reply_to);
  check(shown(view, "by copy").reply_to === asked.id, "a reply naming the request's copy still links to it");
  e.stop();
}

// 2. Participation records carry their type and author plainly (event_type,
// event_by: the author's person label as known here, else the device's
// address), and the chat list says how many guests are present, how many
// requests wait for this person's decision here (none: a browser runs no
// agent) and, when the latest row is a record, what it was (last_event).
// A device conversation names its agent (thread agent_id).
{
  const f = await fixture(), { e, store } = f;
  const pid = id(), ts = Math.floor(now / 1000);
  const inv = await wire.signEvent(e.keys, { conv: f.conv, pid, type: "invite", ts, author: e.author(), host: { person: f.peer.person, address: f.peerAddress, fingerprint: f.peer.fingerprint }, audience: "conversation" });
  const accept = await wire.signEvent(f.peerKeys, { conv: f.conv, pid, type: "accept", prev: await wire.eventHash(inv), ts, author: { person: f.peer.person, roster: f.peer.hash, address: f.peerAddress, fingerprint: f.peer.fingerprint } });
  const invID = id(), acceptID = id();
  await store.write([
    { s: "outbox", k: invID, v: sent(f, { id: invID, lid: id(), sub: "event", pid, body: wire.eventJSON(inv), to: f.peerAddress, at: now + 1 }) },
    { s: "inbox", k: acceptID, v: received(f, { id: acceptID, lid: id(), sub: "event", pid, body: wire.eventJSON(accept), at: now + 2 }) },
  ]);
  const view = await e.dm(f.conv), row = (rid) => view.messages.find((m) => m.id === rid);
  check(row(invID).event_type === "invite" && row(invID).event_by === "Self", "an invitation's type and author: " + JSON.stringify([row(invID).event_type, row(invID).event_by]));
  check(row(acceptID).event_type === "accept" && row(acceptID).event_by === "Peer", "an acceptance's type and author: " + JSON.stringify([row(acceptID).event_type, row(acceptID).event_by]));
  const summary = async () => (await e.overview()).dms.find((d) => d.id === f.conv);
  let s = await summary();
  check(s.guests === 1 && s.decide === 0 && s.last_event && s.last_event.kind === "accept" && s.last_event.pid === pid && s.last_event.by === "Peer", "the chat after the acceptance: " + JSON.stringify(s));
  const askID = id();
  await store.write([{ s: "outbox", k: askID, v: sent(f, { id: askID, lid: id(), kind: "task", body: "tidy", pid, target: { address: f.peerAddress, fingerprint: f.peer.fingerprint }, to: f.peerAddress, at: now + 3 }) }]);
  s = await summary();
  check(s.guests === 1 && s.decide === 0 && !s.last_event, "the chat after a turn: no last record: " + JSON.stringify(s));
  const turn = (await e.dm(f.conv)).messages.find((m) => m.id === askID);
  check(!("event_type" in turn) && !("event_by" in turn), "a turn carries no record fields");

  const agentA = id(), agentB = id(), q = id(), plain = id();
  await store.write([
    { s: "outbox", k: q, v: { v: 1, id: q, to: f.peerAddress, kind: "question", body: "which build?", target: { address: f.peerAddress, fingerprint: f.peer.fingerprint, agent_id: agentA }, at: now + 4, state: "delivered" } },
    { s: "outbox", k: plain, v: { v: 1, id: plain, to: "third/box", kind: "message", body: "hello", at: now + 5, state: "delivered" } },
  ]);
  const thread = async (tid) => (await e.overview()).threads.find((t) => t.id === tid);
  check((await thread(q)).agent_id === agentA, "a device conversation names the agent asked");
  check(!(await thread(plain)).agent_id, "a device conversation with no agent names none");
  await store.write([{ s: "inbox", k: "an".padEnd(32, "0"), v: { v: 1, id: "an".padEnd(32, "0"), from: f.peerAddress, fp: f.peer.fingerprint, kind: "answer", body: "the green one", reply_to: q, agent_id: agentB, at: now + 6, read: true, state: "" } }]);
  check((await thread(q)).agent_id === agentB, "its latest agent named: the answer's author");
  e.stop();
}

// 3. Standing grants (liveapprovals.go): a browser holds none, so its list
// is empty and read-only, and revoking one is refused here.
{
  const f = await fixture(), { e } = f;
  const v = await e.api("/api/approvals");
  check(v.read_only === true && Array.isArray(v.questions) && !v.questions.length && Array.isArray(v.tasks) && !v.tasks.length &&
    Array.isArray(v.participations) && !v.participations.length && Object.keys(v).length === 4, "an empty, read-only list: " + JSON.stringify(v));
  await assert.rejects(() => e.api("/api/approvals/revoke", { kind: "question", address: f.peerAddress }), /Nothing runs in this browser/); checks++;
  await assert.rejects(() => e.api("/api/approvals", {}), /read only/); checks++;
  e.stop();
}

// P3: a quote resolves only a unique reference here, independent of threading.
{
  const f = await fixture(), {e, store} = f;
  const parent = id(), copy = id(), lid = id(), clash = id(), ref = id();
  await store.write([
    {s:"outbox",k:parent,v:sent(f,{id:parent,lid,body:"parent",to:f.peerAddress,state:"delivered"})},
    {s:"outbox",k:copy,v:sent(f,{id:copy,lid,body:"parent",to:"self/other",own:true,state:"custody"})},
    {s:"inbox",k:clash,v:received(f,{id:clash,lid,body:"clashing logical id"})},
    {s:"inbox",k:ref,v:received(f,{id:ref,lid:id(),body:"explicit quote",quote:copy,reply_to:clash,ts:Math.floor(now/1000)-86400})},
  ]);
  const view = await e.dm(f.conv), m = shown(view,"explicit quote"), p = shown(view,"parent");
  check(m.quote === p.id && m.reply_to === clash,"quote names the exact copy without changing the thread head");
  check(p.delivery === "delivered","a closed own device cannot hold the other person's delivery tick");
  check(m.sent_at === new Date((Math.floor(now/1000)-86400)*1000).toISOString() && m.at === new Date(now).toISOString(),"sent claim and arrival stay separate");
  await store.write([{s:"inbox",k:ref,v:received(f,{id:ref,lid:id(),body:"explicit quote",quote:lid})}]);
  check(shown(await e.dm(f.conv),"explicit quote").quote === lid,"ambiguous logical quote is not mapped to another message");
  for(const ts of [0,253370764800,Math.floor(now/1000)+1]) {
    await store.write([{s:"inbox",k:ref,v:received(f,{id:ref,lid:id(),body:"guarded time",ts})}]);
    check(shown(await e.dm(f.conv),"guarded time").sent_at === new Date(now).toISOString(),"bad sent claim falls back to arrival");
  }
  e.stop();
}

// A delivered message and its chat-list clock use the same person-level
// status; retained device copies remain visible in the message details.
{
  const f = await fixture(), {e, store} = f;
  const first = id(), extra = id(), lid = id();
  const base = sent(f,{id:first,lid,body:"delivered with a lagging device",to:f.peerAddress,person:f.peer.person});
  await store.write([
    {s:"outbox",k:first,v:base},
    {s:"outbox",k:extra,v:{...base,id:extra,to:"peer/legacy",state:"waiting"}},
  ]);
  const waiting = async () => (await e.overview()).dms.find(d=>d.id===f.conv).waiting;
  check(await waiting()===0,"delivered to recipient despite extra device waiting");
  check(shown(await e.dm(f.conv),base.body).copies.some(c=>c.state==="waiting"),"lagging copy retained in details");
  await store.write([{s:"outbox",k:first,v:{...base,state:"waiting"}}]);
  check(await waiting()===1,"recipient without any delivered copy still counted");
  await store.write([
    {s:"outbox",k:first,v:base},
    {s:"outbox",k:extra,v:{...base,id:extra,to:"self/phone",person:e.me.person,own:true,state:"waiting"}},
  ]);
  check(await waiting()===0,"own linked-device sync does not restore waiting clock");
  e.stop();
}

console.log("PASS messenger fields engine checks: " + checks);
