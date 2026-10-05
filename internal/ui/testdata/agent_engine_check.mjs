// Actual browser engine paths, real keys and temporary in-memory transport.
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
let serial = 1, checks = 0;
const id = () => (serial++).toString(16).padStart(32, "0"), now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };
const refuses = async (fn, why) => { await assert.rejects(fn, why); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
async function fixture() {
  const store = memoryStore(), profiles = new Map(), pubs = new Map(), catalogs = new Map(), posts = [], calls = [];
  let offline = false, profileReads = 0, profileLimit = Infinity;
  const e = new Engine({ store, now: () => now, base: "https://synthetic.invalid", fetch: async (url, o = {}) => {
    const p = new URL(url).pathname; calls.push({ p, ...o });
    if (offline) throw new Error("synthetic offline");
    if (p === "/v1/version") return json({ features: ["env2", "person2", "caps", "notify1"], realm_id: id() });
    if (p.endsWith("/profile")) { if (++profileReads > profileLimit) throw new Error("synthetic profile offline"); return json(profiles.get(p.slice(11, -8))); }
    if (p.endsWith("/agent-catalog")) return json(catalogs.get(p.slice(11, -14)) || []);
    if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(p.slice(11)))) });
    if (p.startsWith("/v1/persons/") && p.endsWith("/chain")) return json({ records: [], more: false });
    if (p === "/v1/messages") {
      check(o.method === "POST" && o.headers["X-Agentnet-Agent"] === e.address && !!o.headers["X-Agentnet-Sig"], "signed existing transport used");
      await wire.verifyEnvelope(wire.parseEnvelope(o.body), (await wire.publicEntry(e.keys, e.address)).sign_key);
      posts.push(o.body); return json({ state: "custody" });
    }
    if (p.endsWith("/wait")) return json({ state: "custody" });
    if (p.endsWith("/ack") || p === "/v1/caps") return json(null, 204);
    throw new Error("unexpected synthetic request " + p);
  } });
  e.keys = await wire.newKeys(); e.address = "self/phone";
  const selfPub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(selfPub);
  const selfRoster = await wire.newRoster(e.keys, e.address, "Self"); e.me = await e.personRecord([selfRoster], "self", null);
  const peerKeys = await wire.newKeys(), peerAddress = "peer/desk", peerPub = await wire.publicEntry(peerKeys, peerAddress);
  const peerRoster = await wire.newRoster(peerKeys, peerAddress, "Peer"), peer = await e.personRecord([peerRoster], "pinned", null);
  const root = await wire.newRoot(e.keys, { person: e.me.person, roster: e.me.hash, address: e.address, fingerprint: e.fp }, { person: peer.person, roster: peer.hash });
  const conv = await wire.rootID(root), c = { id: conv, root: wire.rootJSON(root), peer: peer.person, created: Math.floor(now / 1000), creator: e.address };
  await store.write([{ s: "kv", k: "identity", v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: "kv", k: "person", v: e.me },
    { s: "persons", k: peer.person, v: peer }, { s: "pins", k: peerAddress, v: { address: peerAddress, json: wire.marshalPublic(peerPub), fingerprint: peer.fingerprint, pending: null } }, { s: "convs", k: conv, v: c }]);
  pubs.set(peerAddress, peerPub); e.connected = true;
  const session = id();
  async function caps(names = [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity]) {
    profiles.set(peerAddress, { person: JSON.parse(wire.rosterJSON(peerRoster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(peerKeys, peerAddress, session, names)))] });
  }
  await caps();
  const agentA = await wire.signAgent(peerKeys, { id: id(), host: peerAddress, host_key: peer.fingerprint, label: "Same label", ts: Math.floor(now / 1000) });
  const agentB = await wire.signAgent(peerKeys, { id: id(), host: peerAddress, host_key: peer.fingerprint, label: "Same label", ts: Math.floor(now / 1000) });
  catalogs.set(peerAddress, [agentA, agentB].map((r) => JSON.parse(wire.agentJSON(r))));
  const fromPeer = (fields = {}) => wire.seal({ id: id(), from: peerAddress, to: e.address, ts: Math.floor(now / 1000), kind: "answer", body: "answer", ...fields }, peerKeys, selfPub);
  async function receive(raw, engine = e) { const env = wire.parseEnvelope(raw); await engine.admit(raw, env); return env.id; }
  return { e, store, profiles, pubs, catalogs, posts, calls, peerKeys, peerAddress, peerPub, peerRoster, peer, selfPub, selfRoster, c, conv, agentA, agentB, caps, fromPeer, receive,
    offline: (v) => { offline = v; }, profileLimit: (v) => { profileLimit = v; profileReads = 0; } };
}

// Public catalog uses exact host key/ID; it contains no local executor or grants.
{
  const f = await fixture(), { e } = f;
  const v = await e.api("/api/agents?host=" + f.peerAddress);
  check(v.host === f.peerAddress && v.local === false && v.agents.length === 2 && v.agents.every((a) => a.enabled && !a.responder), "native public catalog DTO");
  check(v.agents[0].record.id !== v.agents[1].record.id && v.agents[0].record.label === v.agents[1].record.label, "label grants no selection identity");
  await refuses(() => e.api("/api/agents"), /no local agents or responders/);
  await refuses(() => e.api("/api/agents", { action: "create", label: "local" }), /no local agents or responders/);
  await refuses(() => e.api("/api/send", { to: f.peerAddress, kind: "question", body: "q", agent_id: id() }), /not in this host/);
  const good = f.catalogs.get(f.peerAddress);
  for (const bad of [[good[0], good[0]], [{ ...good[0], host: "other/desk" }], [{ ...good[0], id: "program" }], [{ ...good[0], program: "claude" }], Array(33).fill(good[0]), [{ ...good[0], sig: "AAAA" }]]) {
    f.catalogs.set(f.peerAddress, bad); await refuses(() => e.agentCatalog(f.peerAddress));
  }
  f.catalogs.set(f.peerAddress, good);
  await f.caps([wire.CapEnv2, wire.CapPerson]);
  await refuses(() => e.agentCatalog(f.peerAddress), /cannot read named agents/);
  check(!(await f.store.all("outbox")).length && !(await f.store.all("inbox")).length, "refused catalog/ID has no message fallback");
  e.stop();
}

// Named device request, exact answer/result binding, and current older-peer refusal.
{
  const f = await fixture(), { e } = f;
  const r = await e.api("/api/send", { to: f.peerAddress, kind: "question", body: "q", agent_id: f.agentA.id });
  const out = await f.store.get("outbox", r.id), opened = await wire.open(out.envelope, f.peerKeys, f.peerAddress, f.selfPub);
  check(opened.target.agent_id === f.agentA.id && opened.target.address === f.peerAddress && opened.target.fingerprint === f.peer.fingerprint && out.required_cap === wire.CapAgentIdentity, "device exact host/ID and durable capability");
  const progress = await f.receive(await f.fromPeer({ kind: "message", status: wire.StatusProgress, body: "checking", reply_to: r.id, agent_id: f.agentA.id }));
  check((await f.store.get("inbox", progress))?.status === wire.StatusProgress && (await e.threadSummaries()).find((t) => t.peer === f.peerAddress)?.waiting === true, "named executor progress stored; the request still waits");
  const wrongProgress = await f.receive(await f.fromPeer({ kind: "message", status: wire.StatusProgress, body: "checking", reply_to: r.id, agent_id: f.agentB.id }));
  check((await f.store.get("held", wrongProgress))?.reason === "invalid", "progress for another named agent refused");
  const answer = await f.receive(await f.fromPeer({ reply_to: r.id, agent_id: f.agentA.id }));
  check((await f.store.get("inbox", answer)).agent_id === f.agentA.id, "matching named answer stored");
  for (const fields of [{ reply_to: r.id, agent_id: f.agentB.id }, { reply_to: id(), agent_id: f.agentA.id }]) {
    const bad = await f.receive(await f.fromPeer(fields));
    check(!(await f.store.get("inbox", bad)) && (await f.store.get("held", bad)).reason === "invalid", "wrong ID/request answer refused");
  }
  const changed = await f.store.get("outbox", r.id);
  await f.store.write([{ s: "outbox", k: r.id, v: { ...changed, target: { ...changed.target, fingerprint: e.fp } } }]);
  const wrongKey = await f.receive(await f.fromPeer({ reply_to: r.id, agent_id: f.agentA.id }));
  check((await f.store.get("held", wrongKey)).reason === "invalid", "answer must match outbound host fingerprint");
  const thread = await e.thread(r.id);
  const shown = thread.messages.find((m) => m.id === answer);
  check(shown.agent_id === f.agentA.id && shown.from === f.peerAddress && shown.author.label === "Agent " + f.agentA.id, "named author preserved in device view");
  await f.caps([wire.CapEnv2, wire.CapPerson]);
  const before = (await f.store.all("outbox")).length;
  await refuses(() => e.api("/api/send", { to: f.peerAddress, kind: "task", body: "t", agent_id: f.agentB.id }), { code: "agent_identity_unsupported", message: "Peer’s app needs an update first." });
  check((await f.store.all("outbox")).length === before, "known older peer explicitly refused before persistence");
  e.stop();
}

// Offline queue retains ciphertext and capability after reload; resumed old peers refuse.
for (const oldAfterReload of [false, true]) {
  const f = await fixture(), { e } = f;
  f.profileLimit(1); // catalog proof succeeds, subsequent preflight/post profile reads lose network
  const r = await e.api("/api/send", { to: f.peerAddress, kind: "task", body: "offline", agent_id: f.agentB.id });
  const queued = await f.store.get("outbox", r.id);
  check(queued.state === "queued" && queued.required_cap === "agi1" && !f.posts.length, "offline selected request queued with agi1");
  await e.load(); e.connected = true; f.profileLimit(Infinity);
  if (oldAfterReload) await f.caps([wire.CapEnv2, wire.CapPerson]);
  await e.flushOutbox();
  const done = await f.store.get("outbox", r.id);
  check(done.envelope === queued.envelope && done.required_cap === "agi1", "retry preserves exact ciphertext and required cap");
  check(oldAfterReload ? done.state === "failed" && !f.posts.length && done.detail.includes("cannot read named agents") : done.state === "custody" && f.posts.length === 1, "latest signed capability decides retry, no default fallback");
  e.stop();
}

// Signed selected participation, proof-held named turns, invitation copies and named ask.
{
  const f = await fixture(), { e } = f;
  const futurePID = id();
  const raw = await f.fromPeer({ v: 2, conv: f.conv, lid: id(), root: f.c.root, pid: futurePID, reply_to: id(), agent_id: f.agentA.id, origin: "agent:stub", emotion: "plain" });
  const held = await f.receive(raw);
  check((await f.store.get("held", held)).reason === "proof_pending" && !(await f.store.get("inbox", held)), "named turn waits for signed participation");
  const inv = await wire.signEvent(e.keys, { conv: f.conv, pid: futurePID, type: "invite", ts: Math.floor(now / 1000), author: e.author(), host: { person: f.peer.person, address: f.peerAddress, fingerprint: f.peer.fingerprint, agent_id: f.agentA.id }, audience: "conversation" });
  await e.sendConv(f.c, { kind: "message", body: wire.eventJSON(inv), sub: "event", pid: futurePID });
  await e.retryHeld();
  check((await f.store.get("held", held)).reason === "proof_pending" && !(await f.store.get("inbox", held)), "a named output waits while its participation is only invited");
  const accept = await wire.signEvent(f.peerKeys, { conv: f.conv, pid: futurePID, type: "accept", prev: await wire.eventHash(inv), ts: Math.floor(now / 1000), author: { person: f.peer.person, roster: f.peer.hash, address: f.peerAddress, fingerprint: f.peer.fingerprint } });
  await f.receive(await f.fromPeer({ v: 2, kind: "message", conv: f.conv, lid: id(), root: f.c.root, sub: "event", pid: futurePID, body: wire.eventJSON(accept) }));
  await e.retryHeld();
  check((await f.store.get("inbox", held)).agent_id === f.agentA.id && !(await f.store.get("held", held)), "the host's signed acceptance releases its named output");
  const asked = await e.api("/api/dm/agent/ask", { pid: futurePID, body: "selected ask" });
  const askRow = await f.store.get("outbox", asked.id);
  check(askRow.target.agent_id === f.agentA.id && askRow.required_cap === "agi1", "ask derives ID only from signed participation");
  const wrong = await f.receive(await f.fromPeer({ v: 2, conv: f.conv, lid: id(), root: f.c.root, pid: futurePID, reply_to: asked.id, agent_id: f.agentB.id }));
  check((await f.store.get("held", wrong)).reason === "invalid", "foreign selected agent refused");
  const ownRaw = await wire.seal({ v: 2, id: id(), from: e.address, to: e.address, ts: Math.floor(now / 1000), kind: "answer", body: "foreign host", conv: f.conv, lid: id(), root: f.c.root, pid: futurePID, reply_to: asked.id, agent_id: f.agentA.id }, e.keys, f.selfPub);
  await f.store.write([{ s: "pins", k: e.address, v: { address: e.address, json: wire.marshalPublic(f.selfPub), fingerprint: e.fp } }]);
  const foreign = await f.receive(ownRaw);
  check((await f.store.get("held", foreign)).reason === "invalid", "member other than exact selected host cannot claim author");
  const invited = await e.api("/api/dm/agent/invite", { conv: f.conv, host: f.peerAddress, agent_id: f.agentB.id });
  const inviteRow = (await f.store.all("outbox")).find((r) => r.pid === invited.pid);
  check(wire.parseEvent(inviteRow.body).host.agent_id === f.agentB.id && inviteRow.required_cap === "agi1", "optional DM selected invite uses exact verified catalog");
  const dm = await e.dm(f.conv);
  check(dm.agents.find((a) => a.pid === futurePID).agent_id === f.agentA.id && dm.messages.find((m) => m.id === held).agent_id === f.agentA.id, "participation/message view preserves named identity");

  // A device linked later receives original author/ID as sibling-vouched history.
  const siblingKeys = await wire.newKeys(), siblingAddress = "self/later", siblingPub = await wire.publicEntry(siblingKeys, siblingAddress);
  const join = await wire.joinConsent(siblingKeys, siblingAddress, e.me.person, e.me.seq + 1, e.me.hash);
  const next = await wire.nextRoster(e.keys, e.address, f.selfRoster, [f.selfPub, siblingPub], join); await wire.verifyNext(next, f.selfRoster);
  e.me = await e.personRecord([f.selfRoster, next], "self", e.me); await e.pinDevices(e.me);
  const sibling = e.me.devices.find((d) => d.address === siblingAddress);
  const item = e.itemOf(await f.store.get("inbox", held), false), copy = await e.historyCopy(sibling, f.c, item);
  check(item.agent_id === f.agentA.id && item.from === f.peerAddress && copy.required_cap === "agi1", "history preserves exact author/ID and durable cap");
  const laterStore = memoryStore(), later = new Engine({ store: laterStore, now: () => now, base: e.base, fetch: e.fetch });
  later.keys = siblingKeys; later.address = siblingAddress; later.fp = await wire.fingerprint(siblingPub); later.me = { ...e.me, address: siblingAddress, fingerprint: later.fp };
  await laterStore.write([{ s: "kv", k: "identity", v: { keys: siblingKeys, address: siblingAddress, fingerprint: later.fp } }, { s: "kv", k: "person", v: later.me },
    { s: "persons", k: f.peer.person, v: f.peer }, { s: "convs", k: f.conv, v: f.c },
    ...(await f.store.all("pins")).map((p) => ({ s: "pins", k: p.address, v: p }))]);
  await f.receive(copy.envelope, later);
  check((await laterStore.get("held", copy.id)).reason === "proof_pending" && !(await laterStore.get("inbox", held)), "late-linked answer held until invitation history");
  const invCopy = await e.historyCopy(sibling, f.c, e.itemOf(inviteRow, true));
  // The original participation rather than the second agent's invite is the required proof.
  const originalInvite = (await f.store.all("outbox")).find((r) => r.pid === futurePID && r.sub === "event");
  const originalCopy = await e.historyCopy(sibling, f.c, e.itemOf(originalInvite, true));
  await f.receive(originalCopy.envelope, later); await later.retryHeld();
  const history = await laterStore.get("inbox", held);
  check(history.history && history.agent_id === f.agentA.id && history.from === f.peerAddress && history.fp === f.peer.fingerprint && history.synced_from === e.address, "late-linked admission keeps original host and ID");
  const historyView = await later.dm(f.conv);
  check(historyView.messages.find((m) => m.id === held).agent_id === f.agentA.id, "late-linked view retains agent ID");
  check(invCopy.required_cap === "agi1" && originalCopy.required_cap === "agi1", "named invitation history remains gated");
  e.stop(); later.stop();
}

// An agent's turn, named or not, or any agent origin, comes only from its
// participation's exact host key while it is active, history from its
// original key; the view marks only those (client
// TestAgentTurnsComeOnlyFromTheExactHost). The host runs its default agent.
{
  const f = await fixture(), { e } = f;
  const pid = id(), turn = (fields) => ({ v: 2, conv: f.conv, lid: id(), root: f.c.root, pid, origin: "agent:stub", emotion: "plain", ...fields });
  const inv = await wire.signEvent(e.keys, { conv: f.conv, pid, type: "invite", ts: Math.floor(now / 1000), author: e.author(), host: { person: f.peer.person, address: f.peerAddress, fingerprint: f.peer.fingerprint }, audience: "conversation" });
  await e.sendConv(f.c, { kind: "message", body: wire.eventJSON(inv), sub: "event", pid });
  const early = await f.receive(await f.fromPeer(turn({ reply_to: id() })));
  check((await f.store.get("held", early)).reason === "proof_pending" && !(await f.store.get("inbox", early)), "an unnamed output waits while its participation is only invited");
  const accept = await wire.signEvent(f.peerKeys, { conv: f.conv, pid, type: "accept", prev: await wire.eventHash(inv), ts: Math.floor(now / 1000), author: { person: f.peer.person, roster: f.peer.hash, address: f.peerAddress, fingerprint: f.peer.fingerprint } });
  await f.receive(await f.fromPeer({ v: 2, kind: "message", conv: f.conv, lid: id(), root: f.c.root, sub: "event", pid, body: wire.eventJSON(accept) }));
  await e.retryHeld();
  check(!!(await f.store.get("inbox", early)) && !(await f.store.get("held", early)), "the host's acceptance releases its unnamed output");
  const asked = await e.api("/api/dm/agent/ask", { pid, body: "which branch?" });
  const answer = await f.receive(await f.fromPeer(turn({ reply_to: asked.id })));
  check(!!(await f.store.get("inbox", answer)), "the exact host's unnamed answer stored");
  // This browser's own key is a member device, never the host.
  await f.store.write([{ s: "pins", k: e.address, v: { address: e.address, json: wire.marshalPublic(f.selfPub), fingerprint: e.fp } }]);
  const fromSelf = (fields) => wire.seal({ id: id(), from: e.address, to: e.address, ts: Math.floor(now / 1000), kind: "answer", body: "forged", ...fields }, e.keys, f.selfPub);
  for (const [name, fields] of [["unnamed answer", turn({ reply_to: asked.id })], ["unnamed answer as a person", turn({ reply_to: asked.id, origin: "ui", emotion: "" })],
    ["unnamed progress", turn({ kind: "message", status: wire.StatusProgress, body: "checking", reply_to: asked.id })], ["agent origin alone", turn({ kind: "message", pid: "" })]]) {
    const forged = await f.receive(await fromSelf(fields));
    check((await f.store.get("held", forged))?.reason === "invalid" && !(await f.store.get("inbox", forged)), name + " from a member that is not the host refused");
  }
  // Rows an older reader admitted: marked only from the host's key.
  const old = { v: 2, conv: f.conv, sub: "", replica: false, read: true, at: now, state: "", reply_to: "", origin: "agent:claude", emotion: "plain" };
  const originOnly = { ...old, id: id(), lid: id(), from: f.peerAddress, fp: f.peer.fingerprint, kind: "message", body: "old origin claim", pid: "", own: false };
  const nonHost = { ...old, id: id(), lid: id(), from: e.address, fp: e.fp, kind: "answer", body: "old forged answer", pid, reply_to: asked.id, own: true };
  // The key half: the host's address under any other key (a re-keyed
  // device, or a key that only claims the address) is not the host, live or
  // as history, and the view marks only the exact key.
  const otherFp = await wire.fingerprint(await wire.publicEntry(await wire.newKeys(), f.peerAddress));
  const rekeyedTurn = turn({ reply_to: asked.id });
  for (const historical of [false, true]) {
    await refuses(() => e.checkConversationAgent(rekeyedTurn, f.peerAddress, otherFp, f.c, historical), (err) => err.reason === "invalid");
    check((await e.checkConversationAgent(rekeyedTurn, f.peerAddress, f.peer.fingerprint, f.c, historical)) === undefined, "the host's exact key passes (history " + historical + ")");
  }
  const rekeyed = { ...old, id: id(), lid: id(), from: f.peerAddress, fp: otherFp, kind: "answer", body: "re-keyed answer", pid, reply_to: asked.id, own: false };
  const rekeyedHistory = { ...rekeyed, id: id(), lid: id(), body: "re-keyed history", replica: true, history: true, synced_from: "self/other" };
  await f.store.write([{ s: "inbox", k: originOnly.id, v: originOnly }, { s: "inbox", k: nonHost.id, v: nonHost }, { s: "inbox", k: rekeyed.id, v: rekeyed }, { s: "inbox", k: rekeyedHistory.id, v: rekeyedHistory }]);
  const shown = (view, x) => view.messages.find((m) => m.id === x);
  const view = await e.dm(f.conv);
  check(shown(view, answer).verified_agent === true && shown(view, early).verified_agent === true, "the host's outputs are marked as the agent's");
  check(shown(view, asked.id).verified_agent === false, "a person's request is not marked");
  check(shown(view, originOnly.id).verified_agent === false && shown(view, nonHost.id).verified_agent === false, "an origin-only or non-host claim is not marked");
  check(shown(view, rekeyed.id).verified_agent === false && shown(view, rekeyedHistory.id).verified_agent === false, "the host's address under another key is not marked, received or as history");
  await e.api("/api/dm/agent/dismiss", { pid });
  const late = await f.receive(await f.fromPeer(turn({ reply_to: asked.id })));
  check((await f.store.get("held", late))?.reason === "invalid" && !(await f.store.get("inbox", late)), "an output after the participation ended refused");

  // A device linked later: the host's answer is the host's as history,
  // after the end too; the same shape under this device's own key is held.
  const siblingKeys = await wire.newKeys(), siblingAddress = "self/later", siblingPub = await wire.publicEntry(siblingKeys, siblingAddress);
  const join = await wire.joinConsent(siblingKeys, siblingAddress, e.me.person, e.me.seq + 1, e.me.hash);
  const next = await wire.nextRoster(e.keys, e.address, f.selfRoster, [f.selfPub, siblingPub], join); await wire.verifyNext(next, f.selfRoster);
  e.me = await e.personRecord([f.selfRoster, next], "self", e.me); await e.pinDevices(e.me);
  const sibling = e.me.devices.find((d) => d.address === siblingAddress);
  const laterStore = memoryStore(), later = new Engine({ store: laterStore, now: () => now, base: e.base, fetch: e.fetch });
  later.keys = siblingKeys; later.address = siblingAddress; later.fp = await wire.fingerprint(siblingPub); later.me = { ...e.me, address: siblingAddress, fingerprint: later.fp };
  await laterStore.write([{ s: "kv", k: "identity", v: { keys: siblingKeys, address: siblingAddress, fingerprint: later.fp } }, { s: "kv", k: "person", v: later.me },
    { s: "persons", k: f.peer.person, v: f.peer }, { s: "convs", k: f.conv, v: f.c },
    ...(await f.store.all("pins")).map((p) => ({ s: "pins", k: p.address, v: p }))]);
  const events = [...(await f.store.all("outbox")).filter((r) => r.pid === pid && r.sub === "event").map((r) => e.itemOf(r, true)),
    ...(await f.store.all("inbox")).filter((r) => r.pid === pid && r.sub === "event").map((r) => e.itemOf(r, false))];
  for (const item of events) await f.receive((await e.historyCopy(sibling, f.c, item)).envelope, later);
  await f.receive((await e.historyCopy(sibling, f.c, e.itemOf(await f.store.get("inbox", answer), false))).envelope, later);
  await later.retryHeld();
  const kept = (await later.dm(f.conv)).messages.find((m) => m.body === "answer" && m.reply_to === asked.id);
  check(kept?.verified_agent === true && kept.from === f.peerAddress, "the host's answer as history keeps its original key and is marked");
  const forgedItem = { v: 1, from: e.address, from_key: e.fp, id: id(), lid: id(), ts: Math.floor(now / 1000), kind: "answer", body: "forged in history", reply_to: asked.id, pid, origin: "agent:claude", emotion: "plain", at: now };
  const forgedCopy = await e.historyCopy(sibling, f.c, forgedItem);
  await f.receive(forgedCopy.envelope, later);
  check((await laterStore.get("held", forgedCopy.id))?.reason === "invalid" && !(await laterStore.get("inbox", forgedItem.id)), "history of a non-host's answer is checked against its original key");
  e.stop(); later.stop();
}

// The rest of the rule (client TestAgentTurnsComeOnlyFromTheExactHost and
// TestAgentTurnUnderAHumanParticipationIsRefused): an output after its host
// declined is refused; while a record of an active participation is held
// here, its output waits; under a human participation an agent's turn is
// refused even from its exact host, and never marked.
{
  const f = await fixture(), { e } = f, ts = Math.floor(now / 1000);
  const host = { person: f.peer.person, address: f.peerAddress, fingerprint: f.peer.fingerprint }, peerAuthor = { ...host, roster: f.peer.hash };
  const invite = async (pid, extra = {}) => {
    const inv = await wire.signEvent(e.keys, { conv: f.conv, pid, type: "invite", ts, author: e.author(), host, audience: "conversation", ...extra });
    await e.sendConv(f.c, { kind: "message", body: wire.eventJSON(inv), sub: "event", pid });
    return inv;
  };
  const decide = async (pid, inv, type) => {
    const ev = await wire.signEvent(f.peerKeys, { conv: f.conv, pid, type, prev: await wire.eventHash(inv), ts, author: peerAuthor });
    await f.receive(await f.fromPeer({ v: 2, kind: "message", conv: f.conv, lid: id(), root: f.c.root, sub: "event", pid, body: wire.eventJSON(ev) }));
  };
  const resolved = async (pid) => e.resolveAgent(pid, await e.convEvents(f.conv), await e.dmMembers(f.c));
  const output = async (pid) => f.receive(await f.fromPeer({ v: 2, conv: f.conv, lid: id(), root: f.c.root, pid, reply_to: id(), origin: "agent:stub", emotion: "plain" }));

  const declined = id();
  await decide(declined, await invite(declined), "decline");
  check((await resolved(declined)).state === "declined", "the host declined");
  const afterDecline = await output(declined);
  check((await f.store.get("held", afterDecline))?.reason === "invalid" && !(await f.store.get("inbox", afterDecline)), "an output after its host declined refused");

  const heldPID = id(), heldInv = await invite(heldPID);
  await decide(heldPID, heldInv, "accept");
  // Only the host decides: an accept its inviter signed does not count, and is held here.
  const stray = await wire.signEvent(e.keys, { conv: f.conv, pid: heldPID, type: "accept", prev: await wire.eventHash(heldInv), ts, author: e.author() });
  await e.sendConv(f.c, { kind: "message", body: wire.eventJSON(stray), sub: "event", pid: heldPID });
  const heldInfo = await resolved(heldPID);
  check(heldInfo.state === "active" && heldInfo.held === 1, "an active participation with a record held");
  const whileHeld = await output(heldPID);
  check((await f.store.get("held", whileHeld))?.reason === "proof_pending" && !(await f.store.get("inbox", whileHeld)), "an output while a record of its participation is held waits");

  const human = id();
  await decide(human, await invite(human, { role: "human" }), "accept");
  const humanInfo = await resolved(human);
  check(humanInfo.role === "human" && humanInfo.state === "active" && !humanInfo.held, "a human participation, active");
  const asAgent = await output(human);
  check((await f.store.get("held", asAgent))?.reason === "invalid" && !(await f.store.get("inbox", asAgent)), "an agent's turn under a human participation refused, even from its exact host");
  await refuses(() => e.checkConversationAgent({ v: 2, conv: f.conv, lid: id(), kind: "answer", body: "as an agent", pid: human, reply_to: id(), origin: "agent:stub" }, f.peerAddress, f.peer.fingerprint, f.c, true), (err) => err.reason === "invalid");
  const row = { v: 2, conv: f.conv, sub: "", replica: false, read: true, at: now, state: "", origin: "agent:claude", emotion: "plain", id: id(), lid: id(), from: f.peerAddress, fp: f.peer.fingerprint, kind: "answer", body: "old human as agent", pid: human, reply_to: id(), own: false };
  await f.store.write([{ s: "inbox", k: row.id, v: row }]);
  check((await e.dm(f.conv)).messages.find((m) => m.id === row.id)?.verified_agent === false, "a turn under a human participation is never marked as an agent's");
  e.stop();
}

// Recipient-encrypted named history waits for signed capability, unchanged over reload/retry.
{
  const f = await fixture(), { e } = f;
  const dev = { address: f.peerAddress, fingerprint: f.peer.fingerprint, json: wire.marshalPublic(f.peerPub) };
  const item = { v: 1, from: f.peerAddress, from_key: f.peer.fingerprint, id: id(), lid: id(), ts: Math.floor(now / 1000), kind: "answer", body: "history", reply_to: id(), agent_id: f.agentA.id, at: now };
  const copy = await e.historyCopy(dev, f.c, item);
  check(copy.body === wire.historyJSON(item), "durable history body matches the exact sealed capability source");
  await f.store.write([{ s: "outbox", k: copy.id, v: copy }]);
  await e.load(); e.connected = true; await f.caps([wire.CapEnv2, wire.CapPerson]);
  await e.flushOutbox();
  const held = await f.store.get("outbox", copy.id);
  check(held.state === "waiting" && held.required_cap === "agi1" && !f.posts.length && held.envelope === copy.envelope, "history ciphertext kept waiting for older reader");
  await f.caps(); await e.flushOutbox();
  check((await f.store.get("outbox", copy.id)).state === "custody" && f.posts[0] === copy.envelope, "upgraded history reader gets same sealed copy");
  e.stop();
}

// A legacy history row cannot abort other deliveries or acquire receiver authority.
{
  const f = await fixture(), { e } = f;
  const dev = { address: f.peerAddress, fingerprint: f.peer.fingerprint, json: wire.marshalPublic(f.peerPub) };
  const item = { v: 1, from: f.peerAddress, from_key: f.peer.fingerprint, id: id(), lid: id(), ts: Math.floor(now / 1000), kind: "message", body: "exact legacy history", at: now };
  const legacy = await e.historyCopy(dev, f.c, item), marked = await e.historyCopy(dev, f.c, item), valid = await e.historyCopy(dev, f.c, item);
  delete legacy.body; delete marked.body; marked.required_receiver_cap = true;
  await f.store.write([legacy, marked, valid].map(v => ({ s: "outbox", k: v.id, v })));
  await e.load(); e.connected = true; await e.flushOutbox();
  const failed = await f.store.get("outbox", legacy.id), waiting = await f.store.get("outbox", marked.id);
  check(failed.state === "failed" && failed.detail.includes("durable capability proof") && failed.envelope === legacy.envelope && failed.body === undefined, "unmarked legacy history keeps ciphertext and persists exact refusal");
  check(waiting.state === "waiting" && waiting.required_receiver_cap && waiting.envelope === marked.envelope && waiting.body === undefined, "marked legacy history retains its established receiver capability gate");
  check(f.posts.length === 1 && f.posts[0] === valid.envelope && (await f.store.get("outbox", valid.id)).state === "custody", "refused legacy history sends nothing and does not starve valid queued history");
  e.stop();
}

// Normal session publication advertises the qualified named-agent reader.
{
  const f = await fixture(), { e } = f; e.session = id();
  await e.onConnect();
  const ad = f.calls.find((x) => x.p === "/v1/caps");
  check(ad && wire.parseCaps(ad.body).caps.includes(wire.CapAgentIdentity), "named-agent reader advertises agi1");
  check(wire.parseCaps(ad.body).caps.includes(wire.CapRoom) && wire.parseCaps(ad.body).caps.length <= wire.MaxAdvertisedCaps, "room reader advertises rm1 within 16 names (ROOM_V1 §2.1)");
  e.stop();
}
// Proposals: exact selected executor/bytes, only the original asker, and a
// durable single task after concurrent taps or an engine reload (MEL-521).
{
 const f=await fixture(), {e}=f;
 const asked=await e.api("/api/send",{to:f.peerAddress,kind:"question",body:"Should we restart?",agent_id:f.agentA.id});
 const body="Restart the deploy.\nThen verify it.";
 const answer=await f.receive(await f.fromPeer({reply_to:asked.id,status:wire.StatusProposal,body,agent_id:f.agentA.id}));
 check((await e.thread(asked.id)).messages.find(m=>m.id===answer).actions.includes("do_it"),"original asker sees Do it in the device chat");
 await Promise.all([e.api("/api/act",{do:"do_it",id:answer}),e.api("/api/act",{do:"do_it",id:answer})]);
 let tasks=(await f.store.all("outbox")).filter(r=>r.kind==="task");
 check(tasks.length===1 && tasks[0].body===body && tasks[0].reply_to===answer && tasks[0].target.agent_id===f.agentA.id,"double tap keeps one exact task to original executor");
 const taskID=tasks[0].id;
 await e.load(); await e.api("/api/act",{do:"do_it",id:answer});
 tasks=(await f.store.all("outbox")).filter(r=>r.kind==="task");
 check(tasks.length===1 && tasks[0].id===taskID && !(await e.thread(asked.id)).messages.find(m=>m.id===answer).actions.length,"reload confirmation returns existing task and removes action");
 e.stop();
}
for (const bad of ["edited","retracted","replica","history","other-key","other-agent","other-question"]) {
 const f=await fixture(), {e}=f;
 const q=await e.api("/api/send",{to:f.peerAddress,kind:"question",body:"q",agent_id:f.agentA.id});
 const answer=await f.receive(await f.fromPeer({reply_to:q.id,status:wire.StatusProposal,body:"Exact task",agent_id:f.agentA.id}));
 const p=await f.store.get("inbox",answer), request=await f.store.get("outbox",q.id);
 if (bad==="edited" || bad==="retracted") await f.store.write([{s:"inbox",k:id(),v:{id:id(),control:true,from:p.from,fp:p.fp,sub:bad==="edited"?wire.SubRevision:wire.SubRetraction,ref:{id:answer,fingerprint:p.fp}}}]);
 else if (bad==="other-key" || bad==="other-agent") await f.store.write([{s:"outbox",k:q.id,v:{...request,target:{...request.target,...(bad==="other-key"?{fingerprint:e.fp}:{agent_id:f.agentB.id})}}}]);
 else await f.store.write([{s:"inbox",k:answer,v:{...p,...(bad==="other-question"?{reply_to:id()}:{[bad]:true})}}]);
 await refuses(()=>e.api("/api/act",{do:"do_it",id:answer}));
 check(!(await f.store.all("outbox")).some(r=>r.kind==="task"),bad+" cannot mint a confirmation task");
 e.stop();
}
{
 const f=await fixture(), {e}=f, pid=id();
 const inv=await wire.signEvent(e.keys,{conv:f.conv,pid,type:"invite",ts:Math.floor(now/1000),author:e.author(),host:{person:f.peer.person,address:f.peerAddress,fingerprint:f.peer.fingerprint,agent_id:f.agentA.id},audience:"conversation"});
 await e.sendConv(f.c,{kind:"message",body:wire.eventJSON(inv),sub:"event",pid});
 const accept=await wire.signEvent(f.peerKeys,{conv:f.conv,pid,type:"accept",prev:await wire.eventHash(inv),ts:Math.floor(now/1000),author:{person:f.peer.person,roster:f.peer.hash,address:f.peerAddress,fingerprint:f.peer.fingerprint}});
 await f.receive(await f.fromPeer({v:2,kind:"message",conv:f.conv,lid:id(),root:f.c.root,sub:"event",pid,body:wire.eventJSON(accept)}));
 const seed=await e.sendDM({conv:f.conv,body:"Deployment topic",topic:"new"});
 const topic=(await f.store.get("outbox",seed.id)).topic;
 const asked=await e.askAgent({pid,kind:"question",body:"Should we restart?",topic});
 const body="Restart exactly.\nVerify afterwards.";
 const answer=await f.receive(await f.fromPeer({v:2,conv:f.conv,lid:id(),root:f.c.root,pid,reply_to:asked.id,status:wire.StatusProposal,body,topic,agent_id:f.agentA.id,origin:"agent:stub",emotion:"plain"}));
 check((await e.dm(f.conv)).messages.find(m=>m.id===answer)?.actions.includes("do_it"),"conversation topic shows Do it to original asker");
 await Promise.all([e.confirmProposal(answer),e.confirmProposal(answer)]);
 const tasks=(await f.store.all("outbox")).filter(r=>r.kind==="task");
 check(tasks.length===1 && tasks[0].conv===f.conv && tasks[0].pid===pid && tasks[0].topic===topic && tasks[0].body===body && tasks[0].reply_to===(await f.store.get("inbox",answer)).lid,"confirmation remains in exact conversation/participation/topic");
 await e.confirmProposal(answer);
 check((await f.store.all("outbox")).filter(r=>r.kind==="task").length===1,"conversation confirmation retry does not mint more copies");
 e.stop();
}
// The confirmation surface itself belongs only to a current approved human
// device. Agent-host keys and a removed/frozen own device cannot tap Do it.
for (const state of ["agent-host","removed","frozen"]) {
 const f=await fixture(), {e}=f;
 const q=await e.api("/api/send",{to:f.peerAddress,kind:"question",body:"q",agent_id:f.agentA.id});
 const answer=await f.receive(await f.fromPeer({reply_to:q.id,status:wire.StatusProposal,body:"Exact task",agent_id:f.agentA.id}));
 if(state==="agent-host") e.me={...e.me,human_keys:[]};
 else if(state==="removed") e.me={...e.me,devices:[]};
 else e.me={...e.me,state:"conflict"};
 await refuses(()=>e.confirmProposal(answer),/approved current human/);
 check(!(await f.store.all("outbox")).some(r=>r.kind==="task"),state+" refuses before sending");
 e.stop();
}
console.log("named-agent engine checks passed: " + checks);
