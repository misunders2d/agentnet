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
  await refuses(() => e.api("/api/send", { to: f.peerAddress, kind: "task", body: "t", agent_id: f.agentB.id }), /cannot read named agents/);
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
  check((await f.store.get("inbox", held)).agent_id === f.agentA.id && !(await f.store.get("held", held)), "new signed invite releases named proof");
  const accept = await wire.signEvent(f.peerKeys, { conv: f.conv, pid: futurePID, type: "accept", prev: await wire.eventHash(inv), ts: Math.floor(now / 1000), author: { person: f.peer.person, roster: f.peer.hash, address: f.peerAddress, fingerprint: f.peer.fingerprint } });
  await f.receive(await f.fromPeer({ v: 2, kind: "message", conv: f.conv, lid: id(), root: f.c.root, sub: "event", pid: futurePID, body: wire.eventJSON(accept) }));
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
  e.stop();
}
console.log("named-agent engine checks passed: " + checks);
