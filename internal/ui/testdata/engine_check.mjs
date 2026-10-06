// The browser device's engine for engine_test.go: runs static/engine.mjs in
// node against a real test Hub (trusted by its own certificate through
// NODE_EXTRA_CA_CERTS), with an in-memory store, answering one JSON request
// per line on stdin with one JSON line on stdout.
import { Engine, HubError, memoryStore, probeStore, sameOrigin, autoNameTries, inviteDays } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";

// stats counts what a catch-up costs (backlog_test.go): requests by method
// and path shape, store reads and writes, and page changes. latency delays
// every request as a phone's network does; writeDelay every store write as
// a phone's strict IndexedDB commit does.
const stats = { req: {}, reads: 0, rows: 0, scans: {}, writes: 0, changed: 0, renders: 0, pushed: 0, acking: 0, acksAtOnce: 0 };
const scanned = (s, v) => { stats.reads++; stats.rows += v.length; stats.scans[s] = (stats.scans[s] || 0) + v.length; return v; };
let latency = 0, writeDelay = 0;
let ackGate = null; // holdAcks: receipts wait here until released
// page stands in for an open skin: on a change it reads the overview and
// the open DM again, one read at a time (a change meanwhile reads again
// once), as Classic's refetch does.
let pageConv = "", pageBusy = false, pageAgain = false;
async function pageRefetch() {
  if (pageBusy) { pageAgain = true; return; }
  pageBusy = true;
  try {
    do { pageAgain = false; stats.renders++; await engine.api("/api/overview"); await engine.api("/api/dm?id=" + pageConv); } while (pageAgain);
  } catch (e) { /* the next change reads again */ } finally { pageBusy = false; }
}
const onChange = () => { stats.changed++; if (pageConv) pageRefetch(); };
// counting is the engine with the messages its stream pushes counted.
const counting = (e) => { const d = e.dispatch.bind(e); e.dispatch = (event, data) => { if (event === "message") stats.pushed++; return d(event, data); }; e.listen(onChange); return e; };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const counted = (st) => ({ ...st,
  all: async (s) => scanned(s, await st.all(s)),
  after: async (s, k, n) => scanned(s, await st.after(s, k, n)),
  prefix: async (s, p) => scanned(s, await st.prefix(s, p)),
  async write(ops, checks) { stats.writes++; if (writeDelay) await sleep(writeDelay); return st.write(ops, checks); } });
const raw = memoryStore(); // the test's own reads (count) are not counted
let store = counted(raw);
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
  const shape = (opts.method || "GET") + " " + u.pathname.replace(/[0-9a-f]{32,}/g, ":id");
  stats.req[shape] = (stats.req[shape] || 0) + 1;
  if (shape.endsWith("/ack") && shape.startsWith("POST /v1/messages/")) { // receipts sent at once, at most
    stats.acksAtOnce = Math.max(stats.acksAtOnce, ++stats.acking);
    try { if (ackGate) await ackGate.held; if (latency) await sleep(latency); return await fetchNet(url, opts); } finally { stats.acking--; }
  }
  if (latency) await sleep(latency);
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
    engine = counting(new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush }));
    await engine.load();
    await probeStore(store);
    return { joined: engine.joined };
  case "join":
    return { address: await engine.join(req.code, req.name) };
  case "googleReviewProbes": {
    const ownerKeys = await wire.newKeys(), ownerAddress = "g-fixture/laptop", email = "person@example.com";
    const head = await wire.newRoster(ownerKeys, ownerAddress, "Person", email), hash = await wire.rosterHash(head);
    const approver = await wire.publicEntry(ownerKeys, ownerAddress), approverFP = await wire.fingerprint(approver);
    for (const scenario of ["first-race", "expired", "roster_stale", "too_many_devices", "bad_step", "network", "server", "address_taken", "landed-expired"]) {
      const mem = memoryStore(), keys = await wire.newKeys(), address = "g-fixture/browser";
      const oldFirst = await wire.newRoster(keys, address, "Person", email);
      const link = { email, person: head.person, seq: head.seq, roster: hash, approver: { address: ownerAddress, fingerprint: approverFP },
        expires: Math.floor(Date.now()/1000) + (scenario.includes("expired") || scenario === "expired" ? -1 : 600), offer: wire.newID(),
        join: await wire.joinConsent(keys, address, head.person, head.seq+1, hash) };
      await mem.write([{s:"kv",k:"google_joining",v:{keys,address,email,first:scenario === "first-race" ? wire.rosterJSON(oldFirst) : null,link:scenario === "first-race" ? null : link}}]);
      const e = new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("external fetch forbidden")}});
      let attempts = 0;
      e.call = async (method,path,body) => {
        if(path.endsWith("prepare")) return {address,email,name:"Person",head:wire.rosterJSON(head),approver:JSON.parse(wire.marshalPublic(approver)),enrolled:scenario === "landed-expired"};
        if(!path.endsWith("join")) throw Error("unexpected request " + path);
        if(typeof body === "string") body = JSON.parse(body);
        attempts++;
        if(scenario === "expired" && body.link.expires <= Math.floor(Date.now()/1000)) throw Error("expired consent reused");
        if(scenario === "landed-expired" && body.link.expires !== link.expires) throw Error("landed consent rewritten");
        if(attempts === 1 && !["expired","landed-expired"].includes(scenario)) {
          const code = scenario === "first-race" ? "roster_stale" : scenario;
          throw new HubError(scenario === "network" ? 0 : scenario === "server" ? 503 : code === "bad_step" ? 400 : 409,code,"fixture refusal");
        }
        if(!body.link) throw Error("fresh retry did not use current head");
        return {};
      };
      try { await e.joinGoogle("mock", "browser"); } catch(error) {
        const saved = await mem.get("kv","google_joining"), definite = ["first-race","roster_stale","too_many_devices","bad_step"].includes(scenario);
        if(definite && (saved.link || saved.first)) throw Error(scenario + " intent not cleared");
        if(!definite && !saved.link) throw Error(scenario + " exact intent lost");
        await e.joinGoogle("mock","browser");
      }
      if(!e.joined) throw Error(scenario + " retry not joined");
    }
    const mem=memoryStore(), e=new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("external fetch forbidden")}});
    const spoofKeys=await wire.newKeys(), spoof=await wire.newRoster(spoofKeys,"g-spoof/phone","Spoof",email);
    e.chain=async id=>id===head.person?[head]:[spoof];
    await e.pinChain(head.person);
    let blocked=false;try { await e.pinChain(spoof.person); } catch { blocked=true; }
    const conflicting=await mem.get("persons",spoof.person);
    if(!blocked || conflicting.state!=="conflict" || e.personView(conflicting).email) throw Error("duplicate email displayed as pinned person");
    return {passed:true};
  }
  case "googleKeys": {
    const nonce = await engine.googleNonce();
    const pending = await store.get("kv", "google_joining");
    return { nonce, public: wire.marshalPublic(await wire.publicEntry(pending.keys, "google/" + req.name)) };
  }
  case "googleJoin":
    return { address: await engine.joinGoogle(req.token, req.name) };
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
  case "holdAcks": // receipts wait until released (on: false)
    if (req.on && !ackGate) { let open; ackGate = { held: new Promise((r) => { open = r; }) }; ackGate.open = open; }
    if (!req.on && ackGate) { ackGate.open(); ackGate = null; }
    return {};
  case "stats": { // what was counted since the last reset; latency and writeDelay are set for what follows
    const out = structuredClone(stats);
    if (req.reset) Object.assign(stats, { req: {}, reads: 0, rows: 0, scans: {}, writes: 0, changed: 0, renders: 0, pushed: 0, acksAtOnce: stats.acking }); // acking: those under way
    if (req.latency !== undefined) latency = req.latency;
    if (req.writeDelay !== undefined) writeDelay = req.writeDelay;
    if (req.page !== undefined) pageConv = req.page;
    return out;
  }
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
    engine = counting(new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush }));
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
    // The stream is stopped to isolate profile reads; a simulated live
    // connection still lets MEL-547's background outbox begin the upload.
    if (req.connected) engine.connected = true;
    held.send = engine.api("/api/dm/send", { conv: req.conv, body: req.body || "", files });
    let timer;
    try {
      await Promise.race([
        held.started,
        held.send.then(() => new Promise(() => {})), // durable return precedes the upload
        new Promise((_, reject) => { timer = setTimeout(() => reject(Error("held upload did not start")), 5000); }),
      ]);
    } finally { clearTimeout(timer); }
    return {};
  }
  case "releaseUploads": {
    held.open();
    const saved = await held.send;
    await engine.outboxPass; // observe the post-upload gate, not just the local save
    return { v: saved };
  }
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
    return { n: (await raw.all(req.store)).length };
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
