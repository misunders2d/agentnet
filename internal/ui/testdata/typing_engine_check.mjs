// Actual engine admission/send/stream paths with real keys, injected time and
// in-memory synthetic transport. No listeners, browser, peers or external calls.
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

const stores = ["kv", "pins", "persons", "convs", "inbox", "outbox", "held", "lids", "receipts", "files"];
let clock = 1790000000123, serial = 1, checks = 0;
const id = () => (serial++).toString(16).padStart(32, "0");
const realm = "11223344556677889900aabbccddeeff", thread = "00112233445566778899aabbccddeeff";
const timerMap = new Map(); let timerID = 0;
const realSet = globalThis.setTimeout, realClear = globalThis.clearTimeout;
globalThis.setTimeout = (fn, delay) => { const n = ++timerID; timerMap.set(n, { fn, at: clock + delay }); return n; };
globalThis.clearTimeout = (n) => timerMap.delete(n);
async function advance(ms) {
  clock += ms;
  for (let guard = 0; guard < 100; guard++) {
    const next = [...timerMap].find(([, t]) => t.at <= clock);
    if (!next) return;
    timerMap.delete(next[0]); await next[1].fn();
  }
  throw new Error("expiry timer storm");
}
const check = (value, what) => { assert.ok(value, what); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

async function fixture() {
  const store = memoryStore(), posts = [], profiles = new Map(), calls = [];
  let profileGate = null, postGate = null, streamFetch = null;
  const e = new Engine({ store, base: "https://synthetic.invalid", now: () => clock, fetch: async (url, options = {}) => {
    const path = new URL(url).pathname; calls.push({ path, ...options });
    if (options.signal?.aborted) throw new Error("aborted");
    if (path === "/v1/stream" && streamFetch) return streamFetch();
    if (path === "/v1/version") return json({ realm_id: realm, features: ["signals1", "caps"] });
    if (path.endsWith("/profile")) { if (profileGate) await profileGate(); return json(profiles.get(path.slice(11, -8))); }
    if (path === "/v1/caps") return json(null, 204);
    if (path === "/v1/signal") {
      check(options.method === "POST" && options.headers["X-Agentnet-Agent"] === e.address && !!options.headers["X-Agentnet-Sig"], "actual signed POST transport");
      if (postGate) await postGate(options.signal);
      const signal = wire.parseSignal(options.body); await wire.verifySignal(signal, ownPub.sign_key, clock);
      posts.push(options.body); return json(null, 202);
    }
    throw new Error("unexpected synthetic request " + path);
  } });
  e.keys = await wire.newKeys(); e.address = "self/phone";
  const ownPub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(ownPub);
  const ownRoster = await wire.newRoster(e.keys, e.address, "Self");
  e.me = await e.personRecord([ownRoster], "self", null);
  const peerKeys = await wire.newKeys(), peerAddress = "peer/desk", peerPub = await wire.publicEntry(peerKeys, peerAddress);
  const peerRoster = await wire.newRoster(peerKeys, peerAddress, "Peer"), peer = await e.personRecord([peerRoster], "pinned", null);
  const root = await wire.newRoot(e.keys, { person: e.me.person, roster: e.me.hash, address: e.address, fingerprint: e.fp }, { person: peer.person, roster: peer.hash });
  const conv = await wire.rootID(root);
  await store.write([
    { s: "kv", k: "identity", v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: "kv", k: "person", v: e.me },
    { s: "persons", k: peer.person, v: peer },
    { s: "pins", k: peerAddress, v: { address: peerAddress, fingerprint: peer.fingerprint, json: wire.marshalPublic(peerPub), pending: null } },
    { s: "convs", k: conv, v: { id: conv, root: wire.rootJSON(root), peer: peer.person } },
    { s: "inbox", k: thread, v: { id: thread, v: 1, from: peerAddress, kind: "message", body: "fixture", at: clock } },
  ]);
  await e.load(); // include normal one-time notice settlement before durable baselines
  e.realm = realm; e.session = id(); e.connected = true; e.resetTyping(true, true);
  e.members = { listed: "listed", current: true, list: [{ address: peerAddress, presence: "idle", person: { id: peer.person, hash: peer.hash, seq: peer.seq } }], at: clock };
  const recipientSession = id();
  profiles.set(peerAddress, { live: true, sessions: [recipientSession], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(peerKeys, peerAddress, recipientSession, [wire.CapTyping])))] });
  const scope = { conv }, legacy = { peer: peerAddress, thread };
  const signal = async (fields = {}, keys = peerKeys) => wire.sealTyping({ v: 1, id: id(), from: peerAddress, to: e.address, ts: clock,
    session: e.session, realm, conv, thread: "", origin: "human", active: true, ...fields }, keys, ownPub);
  const view = (s = scope) => e.api("/api/typing?" + new URLSearchParams(s));
  const count = async () => JSON.stringify(await Promise.all(stores.map(async (s) => [s, await store.all(s)])));
  const reset = () => { e.session = id(); e.connected = true; e.members.current = true; e.resetTyping(true, true); };
  return { e, store, posts, profiles, calls, peer, peerKeys, peerPub, peerRoster, peerAddress, ownPub, scope, legacy, signal, view, count, reset,
    gate: (f) => { profileGate = f; }, postGate: (f) => { postGate = f; }, stream: (f) => { streamFetch = f; } };
}

try {
  // Receipt, refresh, STOP tie ordering and local expiry: reads never publish changes.
  {
    const f = await fixture(), { e } = f, durable = await f.count();
    let changes = 0; e.listen(() => changes++);
    const start = await f.signal(); await e.dispatch("signal", start);
    check((await f.view()).entries.length === 1 && changes === 1, "real engine decrypts and notifies visible start");
    for (let i = 0; i < 8; i++) await f.view();
    check(changes === 1, "GET/read has no change storm");
    await e.dispatch("signal", start); check(changes === 1 && e.typing.replay.size === 1, "replay refused");
    await e.dispatch("signal", await f.signal({ ts: clock + 1 })); check(changes === 1, "refresh extends expiry without visible transition storm");
    await e.dispatch("signal", await f.signal({ ts: clock + 1, active: false }));
    check((await f.view()).entries.length === 0 && changes === 2, "STOP wins equal signed milliseconds");
    await e.dispatch("signal", await f.signal({ ts: clock + 1 }));
    check((await f.view()).entries.length === 0 && changes === 2, "equal START cannot revive STOP");
    await e.dispatch("signal", await f.signal({ ts: clock + 2 }));
    await advance(5000); check((await f.view()).entries.length === 0 && changes === 4, "one local expiry transition without polling");
    check(await f.count() === durable, "all durable store content/counts unchanged after incoming signals");
    e.stop();
  }
  // Signature/shape/time/scope/realm/session/current pin admission, all encrypted/signed.
  {
    const f = await fixture(), { e } = f, durable = await f.count(), otherKeys = await wire.newKeys();
    const rejects = [
      await f.signal({ session: id() }), await f.signal({ realm: id() }), await f.signal({ conv: "f".repeat(64) }),
      await f.signal({ ts: clock - 5000 }), await f.signal({ ts: clock + 1001 }), await f.signal({}, otherKeys),
      await f.signal({ conv: "", thread: id() }),
    ];
    const valid = JSON.parse(await f.signal());
    rejects.push(JSON.stringify({ ...valid, extra: "draft" }), JSON.stringify({ ...valid, id: "bad" }),
      JSON.stringify({ ...valid, ct: wire.b64(new Uint8Array(2049)) }), "{", " ".repeat(4097));
    for (const raw of rejects) await e.dispatch("signal", raw);
    check((await f.view()).entries.length === 0 && e.typing.replay.size === 0 && e.seq === 0, "malformed/expired/spoof/session/scope/workspace fails closed without persistence or notifications");
    const pin = await f.store.get("pins", f.peerAddress);
    await f.store.write([{ s: "pins", k: f.peerAddress, v: { ...pin, pending: { fingerprint: "changed" } } }]);
    await e.dispatch("signal", await f.signal()); check(e.typing.replay.size === 0, "pending changed key rejected");
    await f.store.write([{ s: "pins", k: f.peerAddress, v: pin }]);
    e.members.list[0].person.hash = "f".repeat(64);
    await e.dispatch("signal", await f.signal()); check(e.typing.replay.size === 0, "current roster mismatch rejected");
    e.members.list[0].person.hash = f.peer.hash;
    await e.dispatch("signal", await f.signal({ ts: clock + 1000 }));
    check((await f.view()).entries.length === 1 && Date.parse((await f.view()).entries[0].expires) === clock + 5000, "future 1s accepted with local TTL clamp");
    e.members.list[0].presence = "offline"; await e.refreshTyping();
    e.members.list[0].presence = "idle";
    check((await f.view()).entries.length === 0, "peer offline invalidates hint; return does not replay it");
    e.resetTyping(true, true); await e.dispatch("signal", await f.signal({ conv: "", thread }));
    check((await f.view(f.legacy)).entries.length === 1 && (await f.view()).entries.length === 0, "known exact legacy peer/thread stays separate from DM");
    const group = "e".repeat(64); await f.store.write([{ s: "convs", k: group, v: { root: '{"v":3,"kind":"group"}' } }]);
    await e.dispatch("signal", await f.signal({ conv: group }));
    await assert.rejects(e.api("/api/typing", { scope: { conv: group }, active: true })); checks++;
    check((await f.view({ conv: group })).entries.length === 0, "groups fail closed without frozen roster fallback");
    // Persistent fixture mutations above are deliberate; no admission wrote any signal row.
    check((await f.store.all("inbox")).length === 1 && (await f.store.all("outbox")).length === 0, "invalid hints never become messages/jobs");
    e.stop();
  }
  // Strict bounds without evicting unexpired replay evidence; reconnect resets memory.
  {
    const f = await fixture(), { e } = f, durable = await f.count();
    for (let i = 0; i < 2048; i++) e.typing.replay.set("seed" + i, clock + 5000);
    await e.dispatch("signal", await f.signal()); check(e.typing.replay.size === 2048 && !e.typing.seen.size && e.typing.replay.has("seed0"), "replay saturation fails closed without early eviction");
    f.reset(); for (let i = 0; i < 256; i++) e.typing.seen.set("seed" + i, { active: false, expires: clock + 5000 });
    await e.dispatch("signal", await f.signal()); check(e.typing.seen.size === 256, "seen state remains bounded256");
    f.reset(); const old = await f.signal(); await e.dispatch("signal", old);
    e.offline(); check(!e.typing.seen.size && !e.typing.replay.size && !e.typing.sent.size && e.typing.timer === null, "disconnect disposes ephemeral state/timer");
    f.reset(); await e.dispatch("signal", old); check(!e.typing.seen.size, "new recipient run discards old-session signal");
    check(await f.count() === durable, "bounds/reconnect changes no durable rows"); e.stop();
  }
  // The current signed person chain identifies two devices; views collapse
  // them to one person, while stopping one does not hide the other's hint.
  {
    const f = await fixture(), { e } = f, keys2 = await wire.newKeys(), address2 = "peer/phone";
    const pub2 = await wire.publicEntry(keys2, address2), fp2 = await wire.fingerprint(pub2);
    const consent = await wire.joinConsent(keys2, address2, f.peer.person, 1, f.peer.hash);
    const next = await wire.nextRoster(f.peerKeys, f.peerAddress, f.peerRoster, [f.peerPub, pub2], consent);
    await wire.verifyNext(next, f.peerRoster);
    const person = await e.personRecord([f.peerRoster, next], "pinned", null);
    await f.store.write([{ s: "persons", k: person.person, v: person }, { s: "pins", k: address2, v: { address: address2, fingerprint: fp2, json: wire.marshalPublic(pub2), pending: null } }]);
    e.members.list[0].person = { id: person.person, hash: person.hash, seq: person.seq };
    e.members.list.push({ address: address2, presence: "idle", person: { id: person.person, hash: person.hash, seq: person.seq } });
    const durable = await f.count();
    await e.dispatch("signal", await f.signal());
    await e.dispatch("signal", await f.signal({ from: address2 }, keys2));
    check((await f.view()).entries.length === 1 && (await f.view()).entries[0].person === person.person, "linked devices collapse to one pinned person");
    await e.dispatch("signal", await f.signal({ active: false }));
    check((await f.view()).entries.length === 1 && (await f.view()).entries[0].address === address2, "one device STOP preserves other current device");
    await e.dispatch("signal", await f.signal({ from: address2, active: false }, keys2));
    check((await f.view()).entries.length === 0 && await f.count() === durable, "both STOP clear person without durable rows"); e.stop();
  }
  // Actual API send path, profiles with signed capabilities, throttle and immediate clear.
  {
    const f = await fixture(), { e } = f, durable = await f.count();
    const sent = await e.api("/api/typing", { scope: f.scope, active: true });
    check(sent.submitted === 1 && !sent.throttled && f.posts.length === 1, "live fanout accepted only");
    const plain = await wire.openTyping(f.posts[0], f.peerKeys, f.peerAddress, f.ownPub, realm, clock);
    check(plain.origin === "human" && plain.active && plain.conv === f.scope.conv, "outgoing sealed header/scope binds human assertion");
    const throttled = await e.api("/api/typing", { scope: f.scope, active: true });
    check(throttled.throttled && f.posts.length === 1, "human START throttle3s");
    const stopped = await e.api("/api/typing", { scope: f.scope, active: false });
    const stop = await wire.openTyping(f.posts[1], f.peerKeys, f.peerAddress, f.ownPub, realm, clock);
    check(stopped.submitted === 1 && !stop.active && stop.ts > plain.ts, "clear immediate, monotonic signed milliseconds");
    await e.api("/api/typing", { scope: f.scope, active: false }); check(f.posts.length === 2, "redundant clear no-op");
    await e.api("/api/typing", { scope: f.scope, active: true }); await advance(2999);
    check((await e.api("/api/typing", { scope: f.scope, active: true })).throttled, "before3s still throttled");
    await advance(1); check((await e.api("/api/typing", { scope: f.scope, active: true })).submitted === 1, "at3s refreshed");
    check(await f.count() === durable, "send/throttle/clear persist no signals, drafts, queues or jobs");
    e.offline(); const n = f.posts.length;
    await e.api("/api/typing", { scope: f.scope, active: true }); f.reset();
    check(f.posts.length === n && !e.typing.sent.size, "offline send neither buffered nor resumed"); e.stop();
  }
  // Preferences defaults, opt-in, persistence only for prefs, malformed API input.
  {
    const f = await fixture(), { e } = f;
    check((await f.view()).preferences.send && (await f.view()).preferences.show, "explicit person defaults on");
    const me = e.me; e.me = null;
    check(!(await f.view()).preferences.send && !(await f.view()).preferences.show, "unset/service-without-person defaults off; no label inference");
    check((await e.api("/api/typing", { scope: f.legacy, active: true })).submitted === 0, "unset default sends nothing");
    const baseline = JSON.parse(await f.count());
    await e.api("/api/typing/preferences", { send: true, show: true });
    check((await e.api("/api/typing", { scope: f.legacy, active: true })).submitted === 1, "explicit local unset opt-in for known legacy scope");
    e.me = me; await e.dispatch("signal", await f.signal());
    await e.api("/api/typing/preferences", { send: false, show: false });
    check((await f.view()).entries.length === 0 && !(await f.view()).preferences.show, "show off immediately removes hints");
    await e.dispatch("signal", await f.signal()); check((await f.view()).entries.length === 0, "show off ignores incoming");
    const loaded = new Engine({ store: f.store, now: () => clock, base: e.base, fetch: e.fetch }); await loaded.load();
    check(!(await loaded.typingPreferences()).send && !(await loaded.typingPreferences()).show && !loaded.typing.seen.size, "reload keeps preferences only");
    for (const [path, body] of [["/api/typing", { scope: f.scope, active: "yes" }], ["/api/typing", { scope: { conv: f.scope.conv, extra: "x" }, active: true }], ["/api/typing/preferences", { show: "yes" }]]) {
      await assert.rejects(e.api(path, body)); checks++;
    }
    baseline.find(([s]) => s === "kv")[1].push({ send: false, show: false });
    assert.deepEqual(JSON.parse(await f.count()), baseline, "sole new durable row is preferences"); checks++;
    assert.deepEqual(await f.store.get("kv", "typing-preferences"), { send: false, show: false }); checks++;
    check(!e.typing.sent.has("draft"), "no draft persisted or buffered"); e.stop(); loaded.stop();
  }
  // Changed current key/caps and disconnection during async profile lookup stop sends.
  {
    const f = await fixture(), { e } = f;
    const prof = f.profiles.get(f.peerAddress);
    f.profiles.set(f.peerAddress, { ...prof, caps: [] });
    check((await e.api("/api/typing", { scope: f.scope, active: true })).skipped === 1 && !f.posts.length, "unsigned/unsupported session rejected");
    f.reset(); f.profiles.set(f.peerAddress, prof);
    const pin = await f.store.get("pins", f.peerAddress);
    f.gate(async () => { await f.store.write([{ s: "pins", k: f.peerAddress, v: { ...pin, pending: { fingerprint: "changed" } } }]); });
    check((await e.api("/api/typing", { scope: f.scope, active: true })).submitted === 0 && !f.posts.length, "key change during profile fetch rejected");
    await f.store.write([{ s: "pins", k: f.peerAddress, v: pin }]); f.reset(); f.gate(async () => e.offline());
    check((await e.api("/api/typing", { scope: f.scope, active: true })).submitted === 0 && !f.posts.length && !e.typing.sent.size, "disconnect during await produces no stale post or durable buffer"); e.stop();
  }
  {
    const f = await fixture(), { e } = f;
    for (let i = 0; i < 256; i++) e.typing.sent.set("seed" + i, { at: clock, ts: clock, active: false });
    check((await e.api("/api/typing", { scope: f.scope, active: true })).submitted === 0 && e.typing.sent.size === 256, "outgoing state bounded256");
    await advance(5001); check((await e.api("/api/typing", { scope: f.scope, active: true })).submitted === 1 && e.typing.sent.size === 1, "expired outgoing state recovers capacity"); e.stop();
  }
  {
    const f = await fixture(), { e } = f;
    let started; const inFlight = new Promise((r) => { started = r; });
    f.postGate((signal) => new Promise((resolve, reject) => { started(); signal.addEventListener("abort", () => reject(new Error("aborted")), { once: true }); }));
    const pending = e.api("/api/typing", { scope: f.scope, active: true });
    await inFlight; e.offline();
    check((await pending).submitted === 0 && !f.posts.length && !e.typing.sent.size, "disconnect aborts signed in-flight typing transport"); e.stop();
  }
  // Real ordered stream/caps hooks using the existing eventsource-parser, no socket.
  {
    const f = await fixture(), { e } = f, durable = await f.count();
    for (const method of ["publishPerson", "flushReceipts", "retryHeld", "flushOutbox", "retryApproved", "runHistory", "runServes", "keepFiles", "reconcileNotify", "fillListed"]) e[method] = async () => {};
    let controller; f.stream(() => new Response(new ReadableStream({ start(c) { controller = c; } }), { headers: { "Agentnet-Members": "1", "Agentnet-Signals": "1" } }));
    const connected = new Promise((resolve) => { const un = e.listen(() => { if (e.connected && e.typing.connected && !e.members.current) { un(); resolve(); } }); });
    const running = e.streamOnce(); await connected;
    const active = new Promise((resolve) => { const un = e.listen(() => { if (e.typing.visible !== "[]") { un(); resolve(); } }); });
    const members = JSON.stringify({ members: e.members.list });
    const raw = await f.signal();
    const frame = new TextEncoder().encode("event:members\r\ndata:" + members + "\r\n\r\nevent:signal\r\ndata:" + raw + "\r\n\r\n");
    controller.enqueue(frame.subarray(0, 13)); controller.enqueue(frame.subarray(13, frame.length - 3)); controller.enqueue(frame.subarray(frame.length - 3));
    await active; check((await f.view()).entries.length === 1, "actual stream parser dispatches ordered members then signal across chunks");
    controller.close(); await running;
    check(!e.typing.connected && !e.typing.seen.size && !e.typing.replay.size && e.typing.timer === null, "actual EOF disconnect clears typing");
    const caps = f.calls.find((c) => c.path === "/v1/caps");
    check(caps && wire.parseCaps(caps.body).caps.includes(wire.CapTyping), "onConnect publishes signed typing1 capability");
    check(await f.count() === durable, "stream admission adds no durable typing rows"); e.stop();
  }
  console.log("typing engine checks passed: " + checks);
} finally { globalThis.setTimeout = realSet; globalThis.clearTimeout = realClear; }
