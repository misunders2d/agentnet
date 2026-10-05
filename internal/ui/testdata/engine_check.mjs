// The browser device's engine for engine_test.go: runs static/engine.mjs in
// node against a real test Hub (trusted by its own certificate through
// NODE_EXTRA_CA_CERTS), with an in-memory store, answering one JSON request
// per line on stdin with one JSON line on stdout.
import { Engine, memoryStore, probeStore, sameOrigin, autoNameTries, inviteDays } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";

let store = memoryStore();
let engine = null;
let offline = false, dropPosts = false, receiptReads = 0;
const realFetch = globalThis.fetch.bind(globalThis);
// A network that can be switched off, as a phone on a train, or that
// loses only the messages posted.
const fetchNet = (url, opts) => (offline || (dropPosts && opts && opts.method === "POST" && url.endsWith("/v1/messages"))
  ? Promise.reject(new TypeError("fetch failed")) : realFetch(url, opts));
// notifyCalls stands in for the relay's notification API (NOTIFY.md §3)
// until the Hub has it: the relay then lists notify1, and every call to it
// is recorded with its signature headers present.
let notifyCalls = null;
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
// forged stands in for a server that lies: for a person id, a different
// (validly signed) record at a step already pinned, served as that
// person's chain and in its device's profile. An honest Hub never does.
const forged = new Map(); // person id -> { record, address }
async function fetchImpl(url, opts = {}) {
  const u = new URL(url);
  if ((opts.method || "GET") === "GET" && u.pathname.startsWith("/v1/messages/")) receiptReads++;
  for (const [person, f] of forged) {
    if (u.pathname === "/v1/persons/" + person + "/chain") return json({ records: [JSON.parse(f.record)], more: false });
    if (u.pathname === "/v1/agents/" + f.address + "/profile") {
      const real = await (await fetchNet(url, opts)).json();
      return json({ ...real, person: JSON.parse(f.record) });
    }
  }
  if (notifyCalls && u.pathname.startsWith("/v1/notify")) {
    notifyCalls.push({ method: opts.method || "GET", path: u.pathname, body: opts.body ? JSON.parse(opts.body) : null,
      signed: !!(opts.headers && opts.headers["X-Agentnet-Agent"]) });
  }
  return fetchNet(url, opts);
}
// A browser's push service, as the page's adapter gives it to the engine:
// a subscription of the right shape (an FCM endpoint, a P-256 key, 16
// bytes of auth). Nothing is ever pushed to it in the tests.
const b64url = (b) => Buffer.from(b).toString("base64url");
const fakePush = { supported: () => true, subscribed: 0,
  async subscribe(key) {
    this.subscribed++;
    this.key = key;
    if (!this.sub) {
      const kp = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
      this.sub = { endpoint: "https://fcm.googleapis.com/fcm/send/agentnet-test-" + wire.newID(),
        p256dh: b64url(new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey))), auth: b64url(crypto.getRandomValues(new Uint8Array(16))) };
    }
    return this.sub;
  },
  async current(key) { return this.subscribe(key); } };

async function handle(req) {
  switch (req.op) {
  case "init":
    if (req.notify) notifyCalls = [];
    engine = new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush });
    await engine.load();
    await probeStore(store);
    return { joined: engine.joined };
  case "join":
    return { address: await engine.join(req.code, req.name) };
  case "joinLink": // a device link from another device of the person (its QR's text)
    return { address: await engine.joinAndLink(req.code, req.name) };
  case "joinAuto": // under an automatic name: base, base-2 … while taken
    return { address: await engine.joinAuto(req.code, req.base) };
  case "joinLinkAuto": // a device link, under an automatic name
    return { address: await engine.joinAndLinkAuto(req.code, req.base) };
  case "tunables": // the engine's copies of Go's tunables, for parity tests
    return { autoNameTries, inviteDays };
  case "decodeInvite": // wire.decodeInvite, for parity with Go
    return { v: wire.decodeInvite(req.code) };
  case "person":
    return await engine.createPerson(req.label);
  case "start":
    engine.start();
    return {};
  case "testHumanCaps": { // signed old/new reader ads, only on this test device
    const profile = await engine.profile(engine.address);
    const old = (profile.caps || []).map(wire.parseCaps).find(c => c.session === engine.session);
    const caps = old.caps.filter(c => ![wire.CapHumanParticipation, wire.CapRoom].includes(c));
    if (req.supported) caps.push(wire.CapHumanParticipation);
    const clock = Date.now;
    let record;
    try { Date.now = () => (old.ts + 1) * 1000; record = await wire.newCaps(engine.keys, engine.address, engine.session, caps); }
    finally { Date.now = clock; }
    await engine.call("PUT", "/v1/caps", wire.capsJSON(record));
    return {};
  }
  case "receiptReads": return { count: receiptReads };
  case "status":
    return { connected: engine.connected, revoked: engine.revoked, members: engine.members.current, link: engine.link ? engine.link.state : "" };
  case "api":
    return { v: await engine.api(req.path, req.body) };
  case "offline":
    offline = req.on;
    if (offline) engine.offline();
    else engine.online();
    return {};
  case "forge": // a lying server: another record for req.person, as its chain and in req.address's profile
    forged.set(req.person, { record: req.record, address: req.address });
    return {};
  case "dropPosts":
    dropPosts = req.on;
    return {};
  case "flush": // what a ping or a connection does with the kept messages
    if (req.connected) { // as a connection does before its member list has come
      engine.connected = true;
      try { await engine.flushOutbox(); } finally { engine.connected = false; }
    } else {
      await engine.flushOutbox();
    }
    return {};
  case "stopStream": // no events come: the next change is seen only by asking
    engine.stop();
    return {};
  case "outbox": { // a kept message; a file's ciphertext as its length
    const rec = (await store.get("outbox", req.id)) || null;
    if (rec && rec.files) rec.files = rec.files.map((f) => ({ ...f, ct: f.ct ? f.ct.length : null }));
    return { rec };
  }
  case "reload": { // the page is reloaded: a new engine over the same stored data
    engine.stop();
    engine = new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush });
    await engine.load();
    engine.start();
    return { joined: engine.joined };
  }
  case "tamperPin": { // the pin of address now names another key (as if it changed); returns the one it named
    const pin = await store.get("pins", req.address);
    await store.write([{ s: "pins", k: req.address, v: { ...pin, json: req.json, fingerprint: req.fingerprint, pending: null } }]);
    engine.pubs.clear();
    return { json: pin.json, fingerprint: pin.fingerprint };
  }
  case "sameOrigin":
    sameOrigin(req.hub, req.base);
    return {};
  case "notifyCalls": { // the notification API calls since the last look
    const out = notifyCalls || [];
    notifyCalls = notifyCalls ? [] : null;
    return { calls: out, subscribed: fakePush.subscribed, key: fakePush.key || "" };
  }
  case "channel":
    return { chan: await wire.notifyChannel(req.conv, req.fp || engine.fp) };
  case "sendFiles": { // a DM message with files, as the page gives File objects
    const files = req.files.map((f) => { const bytes = new Uint8Array(Buffer.from(f.b64, "base64")); return { name: f.name, size: bytes.length, bytes }; });
    return { v: await engine.api("/api/dm/send", { conv: req.conv, body: req.body || "", files }) };
  }
  case "holdUploads": { // uploads wait for releaseUploads; the start of one is awaited by sendFilesLater
    const orig = engine.uploadBlob.bind(engine);
    held = {};
    held.gate = new Promise((r) => { held.open = r; });
    held.started = new Promise((r) => { held.start = r; });
    engine.uploadBlob = async (...args) => { held.start(); await held.gate; return orig(...args); };
    return {};
  }
  case "sendFilesLater": { // sendFiles, answered once its first upload is under way
    const files = req.files.map((f) => { const bytes = new Uint8Array(Buffer.from(f.b64, "base64")); return { name: f.name, size: bytes.length, bytes }; });
    held.send = engine.api("/api/dm/send", { conv: req.conv, body: req.body || "", files });
    held.send.catch(() => {});
    await held.started;
    return {};
  }
  case "releaseUploads":
    held.open();
    return { v: await held.send };
  case "hubStatus": // what the relay says of a message this device sent
    return { v: await engine.call("GET", "/v1/messages/" + req.id) };
  case "convRoot":
    return { root: (await store.get("convs", req.conv)).root };
  case "openFile": {
    const f = await engine.api("/api/file?id=" + req.id + "&i=" + req.i + (req.dir ? "&dir=" + req.dir : ""));
    return { name: f.name, size: f.size, image: f.image, b64: Buffer.from(f.bytes).toString("base64") };
  }
  case "notifyPrefs": // what the relay holds for this device
    return await engine.call("GET", "/v1/notify/prefs");
  case "inboxRec":
    return { rec: (await store.get("inbox", req.id)) || null };
  case "count":
    return { n: (await store.all(req.store)).length };
  case "keys": {
    const id = await store.get("kv", "identity");
    let exported = true;
    try { await crypto.subtle.exportKey("pkcs8", id.keys.box); } catch (e) { exported = false; }
    return { extractable: id.keys.sign.extractable || id.keys.box.extractable, exported, fingerprint: id.fingerprint,
      public: wire.marshalPublic(await wire.publicEntry(id.keys, id.address)) };
  }
  }
  throw new Error("unknown op " + req.op);
}

let held = null; // holdUploads
const rl = createInterface({ input: process.stdin });
for await (const line of rl) {
  let out;
  try {
    out = await handle(JSON.parse(line));
  } catch (e) {
    out = { error: e.message };
  }
  process.stdout.write(JSON.stringify(out) + "\n");
}
process.exit(0);
