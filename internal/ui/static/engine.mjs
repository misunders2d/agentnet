// The browser device: an AgentNet installation that lives in this browser,
// talks to the relay that served the page (same origin only) and holds its
// keys and messages in IndexedDB. It is human-only: nothing runs here. It
// speaks the wire exactly as the Go client does (wire.mjs) and follows the
// Go client's rules for pinning keys and persons, admitting DM messages,
// storing before acknowledging and keeping the exact envelope it sends.
// The page talks to it through the same JSON shapes as the daemon's page
// API (api), so every view works unchanged.
//
// Limits it does not hide: the relay serves this code and could serve other
// code (MESSENGER_ARCHITECTURE §6); keys cannot be copied out or backed up,
// and the browser can clear this storage, which loses the device (join again
// and have the old one revoked).

import * as wire from "./wire.mjs";

const HEARTBEAT = 90_000;
const MAX_BACKOFF = 60_000;
const stores = ["kv", "pins", "persons", "convs", "inbox", "outbox", "held", "lids", "receipts"];
// heldPage bounds the held messages read in one step (the Go client's proofPage).
const heldPage = 50;

// ---- storage -------------------------------------------------------------------------------

const request = (r) => new Promise((res, rej) => { r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });

// openIDB opens this device's database. Every write is a strict-durability
// transaction, so "stored" means stored before anything is acknowledged or
// sent.
export async function openIDB(name = "agentnet") {
  const db = await new Promise((res, rej) => {
    const r = indexedDB.open(name, 1);
    r.onupgradeneeded = () => { for (const s of stores) r.result.createObjectStore(s); };
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
    r.onblocked = () => rej(new Error("storage is blocked by another tab"));
  });
  const done = (t) => new Promise((res, rej) => {
    t.oncomplete = () => res();
    t.onerror = () => rej(t.error);
    t.onabort = () => rej(t.error || new Error("storage write aborted"));
  });
  return {
    get: (s, k) => request(db.transaction(s).objectStore(s).get(k)),
    all: (s) => request(db.transaction(s).objectStore(s).getAll()),
    // after returns up to n values whose keys follow k, in key order ("" is the start).
    after: (s, k, n) => request(db.transaction(s).objectStore(s).getAll(k === "" ? null : IDBKeyRange.lowerBound(k, true), n)),
    async write(ops) {
      const t = db.transaction([...new Set(ops.map((o) => o.s))], "readwrite", { durability: "strict" });
      const finished = done(t);
      try {
        for (const o of ops) {
          if (o.v === undefined) t.objectStore(o.s).delete(o.k);
          else t.objectStore(o.s).put(o.v, o.k);
        }
      } catch (e) {
        // A request that cannot be made (a value that cannot be stored)
        // aborts the others made before it: all of a write or none.
        t.abort();
        await finished.catch(() => {});
        throw e;
      }
      await finished;
    },
    close: () => db.close(),
  };
}

// memoryStore is the same store in memory (tests). Values are structured
// clones, as IndexedDB keeps them.
export function memoryStore() {
  const data = Object.fromEntries(stores.map((s) => [s, new Map()]));
  return {
    get: async (s, k) => (data[s].has(k) ? structuredClone(data[s].get(k)) : undefined),
    all: async (s) => [...data[s].values()].map((v) => structuredClone(v)),
    after: async (s, k, n) => [...data[s].keys()].filter((x) => x > k).sort().slice(0, n).map((x) => structuredClone(data[s].get(x))),
    async write(ops) {
      for (const o of ops) {
        if (o.v === undefined) data[o.s].delete(o.k);
        else data[o.s].set(o.k, structuredClone(o.v));
      }
    },
    close() {},
  };
}

const put = (st, s, k, v) => st.write([{ s, k, v }]);

// probeStore proves this browser really keeps data here: a strict write of
// a record holding a CryptoKey, read back, the key still usable, removed.
export async function probeStore(st) {
  const keys = await wire.newKeys();
  const n = wire.newID();
  await put(st, "kv", "probe", { n, keys });
  const back = await st.get("kv", "probe");
  if (!back || back.n !== n) throw new Error("this browser did not keep what was written");
  await wire.publicEntry(back.keys, "probe/probe"); // signs with the stored key
  await put(st, "kv", "probe", undefined);
}

// ---- errors ------------------------------------------------------------------------------------

export class HubError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status; // 0: the server could not be reached
    this.code = code;
  }
}
const retryable = (e) => e instanceof HubError && (e.status === 0 || e.status >= 500 || e.status === 429);
class Hold extends Error {
  constructor(reason, why) {
    super(why);
    this.reason = reason;
  }
}

// ---- words the page shows --------------------------------------------------------------------

const holdWords = {
  key_changed: (p) => "Held until " + p + "'s changed key is trusted (on a computer with agentnet trust).",
  proof_pending: () => "Held until the conversation or person it names can be checked here. Nothing runs it.",
  identity_conflict: (p) => "Held: it disagrees with the person record kept here for " + p + ". Nothing runs it.",
  conflicting_duplicate: (p) => "Held: " + p + " sent different content under a message it already sent. Nothing runs it.",
};
const holdText = (reason, peer) => (holdWords[reason] ? holdWords[reason](peer) : "It did not verify, so its content is not shown.");

function outText(state, peer, detail) {
  switch (state) {
  case "waiting": return "Kept here, not sent yet: " + (detail || peer + " cannot read conversations now");
  case "queued": return "Waiting to send; retries automatically";
  case "custody": return "Waiting on the server until " + peer + " connects";
  case "delivered": return "Delivered to " + peer;
  case "expired": return "Not delivered: that session ended first";
  case "failed": return "Not sent" + (detail ? ": " + detail : "");
  case "quarantined": return peer + " could not verify it";
  }
  return "";
}

const iso = (ms) => new Date(ms).toISOString();

// copyOrder is the least advanced of a message's copies (its state is the
// message's, as the core reports it).
const copyRank = { failed: 0, waiting: 1, queued: 2, custody: 3, delivered: 4 };
const copyOrder = (recs) => recs.reduce((a, b) => ((copyRank[b.state] ?? 2) < (copyRank[a.state] ?? 2) ? b : a));
const firstLine = (s) => {
  const l = (s || "").split("\n")[0];
  return [...l].length > 120 ? [...l].slice(0, 119).join("") + "…" : l;
};
const loopback = (host) => host === "127.0.0.1" || host === "localhost" || host === "[::1]";

// sameOrigin checks that an invitation is for the server this page came
// from: a device only ever talks to its own origin, over https (loopback
// http only, for testing on this computer).
export function sameOrigin(hub, base) {
  const h = new URL(hub), b = new URL(base);
  if (h.host !== b.host) {
    throw new Error("This invitation is for " + h.host + ", but this page comes from " + b.host + ". Open the invitation link on its own server.");
  }
  if (b.protocol !== "https:" && !(b.protocol === "http:" && loopback(b.hostname))) throw new Error("A device joins only over https.");
}

// ---- the engine -------------------------------------------------------------------------------

export class Engine {
  constructor({ store, base, fetch: f, now, push } = {}) {
    this.store = store;
    // push is the page's Web Push adapter (device.mjs): supported(),
    // subscribe(key), current(). Tests pass their own.
    this.push = push || null;
    this.base = base;
    this.fetch = f || globalThis.fetch.bind(globalThis);
    this.now = now || (() => Date.now());
    this.seq = 0;
    this.listeners = new Set();
    this.pubs = new Map(); // address -> parsed directory entry, from the pins
    this.members = { listed: "unknown", current: false, at: 0, list: [], truncated: false };
    this.connected = false;
    this.revoked = false;
    this.running = false;
    this.version = "";
    this.featureList = null;
  }

  // ---- lifecycle

  async load() {
    const id = await this.store.get("kv", "identity");
    if (id) {
      this.keys = id.keys;
      this.address = id.address;
      this.fp = id.fingerprint;
      this.revoked = !!id.revoked;
    }
    this.me = (await this.store.get("kv", "person")) || null;
    return !!id;
  }

  get joined() { return !!this.address; }

  listen(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  changed() {
    this.seq++;
    for (const f of this.listeners) {
      try { f(this.seq); } catch (e) { /* a listener's problem stays there */ }
    }
  }

  // ---- the relay

  async call(method, path, body, { signed = true } = {}) {
    const text = body === undefined ? "" : typeof body === "string" ? body : JSON.stringify(body);
    const headers = {};
    if (text) headers["Content-Type"] = "application/json";
    if (signed) Object.assign(headers, await wire.signRequest(this.keys, this.address, method, path, text));
    let r;
    try {
      r = await this.fetch(this.base + path, { method, headers, body: text || undefined, cache: "no-store" });
    } catch (e) {
      throw new HubError(0, "", "cannot reach your server");
    }
    if (!r.ok) {
      let j = {};
      try { j = await r.json(); } catch (e) { /* no JSON body */ }
      const err = new HubError(r.status, j.code || "", j.error || r.statusText || "server error");
      if (err.code === "revoked") await this.setRevoked();
      throw err;
    }
    const t = await r.text();
    return t ? JSON.parse(t) : null;
  }

  // callBytes is call with a raw body (an upload chunk); getBytes fetches
  // raw bytes (a file's ciphertext).
  async callBytes(method, path, bytes) {
    const headers = { "Content-Type": "application/octet-stream", ...(await wire.signRequest(this.keys, this.address, method, path, bytes)) };
    let r;
    try {
      r = await this.fetch(this.base + path, { method, headers, body: bytes, cache: "no-store" });
    } catch (e) {
      throw new HubError(0, "", "cannot reach your server");
    }
    if (!r.ok) {
      let j = {};
      try { j = await r.json(); } catch (e) { /* no JSON body */ }
      throw new HubError(r.status, j.code || "", j.error || r.statusText || "server error");
    }
    const t = await r.text();
    return t ? JSON.parse(t) : null;
  }

  async getBytes(path) {
    const headers = await wire.signRequest(this.keys, this.address, "GET", path, "");
    let r;
    try {
      r = await this.fetch(this.base + path, { method: "GET", headers, cache: "no-store" });
    } catch (e) {
      throw new HubError(0, "", "cannot reach your server");
    }
    if (!r.ok) throw new HubError(r.status, "", r.status === 404 ? "the file is not on your server (any more)" : r.statusText || "server error");
    return new Uint8Array(await r.arrayBuffer());
  }

  // uploadBlob sends one file's ciphertext to the relay for the recipient,
  // resuming from wherever the relay's copy ends (client upload): every
  // step is safe to repeat.
  async uploadBlob(to, blob, ct) {
    let st = await this.call("POST", "/v1/blobs", { id: blob.id, recipient: to, size: blob.size, sha256: blob.sha256 });
    let stale = 0;
    while (st.state !== "stored") {
      const before = st.received;
      try {
        st = st.received < st.size
          ? await this.callBytes("PUT", "/v1/blobs/" + blob.id + "?offset=" + st.received, ct.subarray(st.received, Math.min(st.size, st.received + wire.ChunkSize)))
          : await this.call("POST", "/v1/blobs/" + blob.id + "/complete");
      } catch (e) {
        if (!(e instanceof HubError && e.status === 409) || ++stale > 3) throw e;
        st = await this.call("GET", "/v1/blobs/" + blob.id); // our view of the offset was stale
      }
      if (st.received > before || st.state === "stored") stale = 0;
    }
  }

  async features() {
    if (this.featureList) return this.featureList;
    const v = await this.call("GET", "/v1/version", undefined, { signed: false });
    this.version = v.version || "";
    this.featureList = v.features || [];
    return this.featureList;
  }

  async setRevoked() {
    if (this.revoked) return;
    this.revoked = true;
    this.stop();
    const id = await this.store.get("kv", "identity");
    if (id) await put(this.store, "kv", "identity", { ...id, revoked: true });
    this.changed();
  }

  // ---- joining and the person

  // join enrolls this browser as the device label/agentName with the
  // invitation code, once. The code is never stored or logged.
  async join(code, agentName) {
    if (this.joined) throw new Error("This browser already holds a device.");
    const inv = wire.decodeInvite(code);
    // An invitation that pins the server's own certificate is for the
    // command line: a browser cannot apply that pin, and its own trust is
    // not a substitute. Refused before anything is sent.
    if (inv.cert) throw new Error("This invitation pins its server's own certificate, which a browser cannot use. Ask your admin for an invitation for the browser, from a server with a certificate browsers trust.");
    sameOrigin(inv.hub, this.base);
    const address = inv.label + "/" + String(agentName || "").trim();
    if (!wire.validAddress(address)) throw new Error("Choose a device name of lowercase letters, digits and dashes, such as phone.");
    const v = await this.call("GET", "/v1/version", undefined, { signed: false });
    if (v.protocol !== 1) throw new Error("Your server speaks another protocol version; one of the two needs an update.");
    // The same key is used for a retry, as the Go client does.
    let pending = await this.store.get("kv", "joining");
    if (!pending || pending.address !== address) {
      pending = { address, keys: await wire.newKeys() };
      await put(this.store, "kv", "joining", pending);
    }
    const body = await wire.joinRequest(pending.keys, address, inv.secret);
    try {
      await this.call("POST", "/v1/join", body, { signed: false });
    } catch (e) {
      if (e.status === 409) throw new Error("The name " + address + " is already used on this server. Choose another name.");
      throw new Error("Could not join (" + e.message + "). Check your connection and try again.");
    }
    const fingerprint = await wire.fingerprint(await wire.publicEntry(pending.keys, address));
    await this.store.write([{ s: "kv", k: "identity", v: { address, keys: pending.keys, fingerprint, joined: this.now() } },
      { s: "kv", k: "joining", v: undefined }]);
    await this.load();
    this.changed();
    return address;
  }

  // A person is kept as the chain of roster steps verified here (TOFU on
  // step 0): { person, label, seq, hash, json (the newest step), hashes
  // (every step's hash), devices (the newest step's: address,
  // fingerprint, json), known (every device any step named, for records
  // they signed), state (self, pinned, conflict) }. address and
  // fingerprint are the one device a view is about: this one for your own
  // person, else the first current device.
  personView(p, extra) {
    return p && { person: p.person, label: p.label, address: p.address, fingerprint: p.fingerprint, state: p.state,
      devices: (p.devices || []).map((d) => ({ address: d.address, name: d.address.split("/")[1], fingerprint: d.fingerprint, this: d.address === this.address })),
      ...extra };
  }

  // personRecord makes a person to keep from its verified steps (oldest
  // first), with state, from what was kept before (known devices).
  async personRecord(steps, state, before) {
    const r = steps[steps.length - 1];
    const devices = await Promise.all(r.devices.map(async (d) => ({ address: d.address, fingerprint: await wire.fingerprint(d), json: wire.marshalPublic(d) })));
    const known = [...((before && before.known) || [])];
    for (const st of steps) {
      for (const d of st.devices) {
        const fp = await wire.fingerprint(d);
        if (!known.some((k) => k.address === d.address && k.fingerprint === fp)) known.push({ address: d.address, fingerprint: fp, json: wire.marshalPublic(d) });
      }
    }
    const hashes = [...((before && before.hashes) || [])];
    for (const st of steps) {
      const h = await wire.rosterHash(st);
      if (!hashes.includes(h)) hashes.push(h);
    }
    const self = devices.find((d) => d.address === this.address);
    const one = self && state === "self" ? self : devices[0];
    return { person: r.person, label: r.label, seq: r.seq, hash: await wire.rosterHash(r), json: wire.rosterJSON(r), hashes, devices, known,
      address: one.address, fingerprint: one.fingerprint, state, published: before ? !!before.published : false };
  }

  async createPerson(label) {
    if (this.me) throw new Error("This device already speaks for \"" + this.me.label + "\"; a second person is not created.");
    const r = await wire.newRoster(this.keys, this.address, String(label || "").trim());
    const me = await this.personRecord([r], "self", null);
    await put(this.store, "kv", "person", me);
    this.me = me;
    this.changed();
    let note = "Your person is set up: others can start a DM with you.";
    try {
      await this.publishPerson();
    } catch (e) {
      note = "Your person is set up here, but your server does not hold it yet: it is sent the next time this page connects.";
    }
    return { person: this.personView(me, { published: me.published }), note };
  }

  async publishPerson() {
    if (!this.me || this.me.published) return;
    if (!(await this.features()).includes("person2")) throw new Error("your server does not hold persons (it needs an update)");
    await this.call("PUT", "/v1/person", this.me.json);
    this.me = { ...this.me, published: true };
    await put(this.store, "kv", "person", this.me);
    this.changed();
  }

  // ---- keys and persons of others

  async directory(address) {
    const [label, agent] = address.split("/");
    const d = await this.call("GET", "/v1/agents/" + label + "/" + agent);
    const pub = await wire.parsePublic(d.public);
    if (pub.address !== address) throw new Error("the server answered for another address");
    return { pub, fingerprint: await wire.fingerprint(pub), json: wire.marshalPublic(pub) };
  }

  async pubOf(pin) {
    if (!this.pubs.has(pin.address + pin.fingerprint)) this.pubs.set(pin.address + pin.fingerprint, await wire.parsePublic(JSON.parse(pin.json)));
    return this.pubs.get(pin.address + pin.fingerprint);
  }

  // pinned returns the key pinned for address, pinning it the first time
  // it is seen (trust on first use). A changed key is never used.
  async pinned(address) {
    let pin = await this.store.get("pins", address);
    if (!pin) {
      const d = await this.directory(address);
      pin = { address, json: d.json, fingerprint: d.fingerprint, pending: null };
      await put(this.store, "pins", address, pin);
    }
    return pin;
  }

  // pinDevices pins the keys a person's steps name for their devices: a
  // device pinned before with another key is a changed key, never used.
  async pinDevices(p) {
    for (const d of p.devices) {
      const pin = await this.store.get("pins", d.address);
      if (!pin) await put(this.store, "pins", d.address, { address: d.address, json: d.json, fingerprint: d.fingerprint, pending: null });
      else if (pin.fingerprint !== d.fingerprint && !(pin.pending && pin.pending.fingerprint === d.fingerprint)) {
        await put(this.store, "pins", d.address, { ...pin, pending: { json: d.json, fingerprint: d.fingerprint } });
      }
    }
  }

  // The person whose current devices include address (with fingerprint fp, if given).
  async personByDevice(address, fp) {
    const has = (p) => (p.devices || []).some((d) => d.address === address && (!fp || d.fingerprint === fp));
    if (this.me && has(this.me)) return this.me;
    return (await this.store.all("persons")).find(has);
  }

  async profile(address) {
    const [label, agent] = address.split("/");
    return this.call("GET", "/v1/agents/" + label + "/" + agent + "/profile");
  }

  // chain reads person's roster steps after seq from the server, all pages.
  async chain(person, after) {
    const out = [];
    for (;;) {
      const page = await this.call("GET", "/v1/persons/" + person + "/chain?after=" + after);
      for (const raw of page.records || []) out.push(await wire.parseRoster(raw));
      if (!page.more || !(page.records || []).length) return out;
      after = out[out.length - 1].seq;
    }
  }

  // pinChain pins person the first time: its whole chain, verified from
  // step 0 (trust on first use), and its devices' keys.
  async pinChain(person) {
    const known = person === (this.me && this.me.person) ? this.me : await this.store.get("persons", person);
    if (known) return this.refreshPerson(known);
    let steps;
    try {
      steps = await this.chain(person, -1);
      if (!steps.length) throw new Error("no roster");
      await wire.verifyFirst(steps[0]);
      for (let i = 1; i < steps.length; i++) await wire.verifyNext(steps[i], steps[i - 1]);
    } catch (e) {
      if (retryable(e)) throw e;
      throw new Hold("proof_pending", "person " + person + ": " + e.message);
    }
    const p = await this.personRecord(steps, "pinned", null);
    await put(this.store, "persons", p.person, p);
    await this.pinDevices(p);
    return p;
  }

  // refreshPerson follows p's chain past the step pinned here, verifying
  // each new step; a different record at a pinned step freezes the person
  // (a fork is never replaced). Your own person is followed the same way.
  async refreshPerson(p) {
    if (!p || p.state === "conflict") return p;
    let steps;
    try {
      steps = await this.chain(p.person, p.seq - 1);
    } catch (e) {
      if (retryable(e)) throw e;
      return p;
    }
    if (!steps.length) return p;
    if ((await wire.rosterHash(steps[0])) !== p.hash) return this.freeze(p);
    if (steps.length === 1) return p;
    let prev = await wire.parseRoster(p.json);
    try {
      for (const st of steps.slice(1)) {
        await wire.verifyNext(st, prev);
        prev = st;
      }
    } catch (e) {
      return p; // a step that does not follow proves nothing here
    }
    const next = await this.personRecord([await wire.parseRoster(p.json), ...steps.slice(1)], p.state, p);
    if (p.state === "self") {
      this.me = { ...next, published: true };
      await put(this.store, "kv", "person", this.me);
      this.changed();
      return this.me;
    }
    await put(this.store, "persons", next.person, next);
    await this.pinDevices(next);
    this.changed();
    return next;
  }

  async freeze(p) {
    if (p.state === "self") return p;
    const f = { ...p, state: "conflict" };
    await put(this.store, "persons", p.person, f);
    this.changed();
    return f;
  }

  // personOf returns the person the device at address (key pin) speaks
  // for, as its server lists it: pinned the first time from its whole
  // chain, followed after. A conflict is frozen, never replaced.
  async personOf(address, pin) {
    const known = await this.personByDevice(address, pin.fingerprint);
    if (known) {
      if (known.state === "conflict") throw new Hold("identity_conflict", "this person's record conflicts with the one kept here; it is frozen");
      return known;
    }
    let prof;
    try {
      prof = await this.profile(address);
    } catch (e) {
      if (e.status === 404) throw new Hold("proof_pending", address + " has no person on this server");
      throw e;
    }
    if (!prof || !prof.person) throw new Hold("proof_pending", address + " has no person on this server");
    let r;
    try {
      r = await wire.parseRoster(prof.person);
    } catch (e) {
      throw new Hold("proof_pending", address + "'s person record: " + e.message);
    }
    const p = await this.pinChain(r.person);
    if (p.state === "conflict") throw new Hold("identity_conflict", "this person's record conflicts with the one kept here; it is frozen");
    if (!p.devices.some((d) => d.address === address && d.fingerprint === pin.fingerprint)) {
      throw new Hold("proof_pending", address + " is not a current device of its person here");
    }
    return p;
  }

  // supports says whether the device at address can be sent a copy of a
  // conversation message now: it reads version 2 and person rosters (every
  // session of it says so); the third answer is whether it reads the
  // attention hint. A newer step of its person seen in its profile is
  // followed first.
  async supports(address, pin) {
    const f = await this.features();
    if (!f.includes("env2") || !f.includes("caps") || !f.includes("person2")) return [false, "your server cannot carry conversations (it needs an update)"];
    const prof = await this.profile(address);
    if (prof && prof.person) {
      try {
        const r = await wire.parseRoster(prof.person);
        const p = r.person === (this.me && this.me.person) ? this.me : await this.store.get("persons", r.person);
        if (p && r.seq > p.seq) await this.refreshPerson(p);
        else if (p && r.seq === p.seq && (await wire.rosterHash(r)) !== p.hash) await this.refreshPerson(p);
      } catch (e) { /* no proof of anything */ }
    }
    const key = (await this.pubOf(pin)).sign_key;
    if (!(await wire.profileSupports(prof || {}, address, key, wire.CapEnv2)) || !(await wire.profileSupports(prof || {}, address, key, wire.CapPerson))) {
      return [false, address + " needs to update AgentNet to read this conversation (or has not connected since updating)"];
    }
    return [true, "", f.includes("notify1") && (await wire.profileSupports(prof, address, key, "notify1"))];
  }

  // ---- DMs

  async newDM(address) {
    if (!this.me) throw new Error("Set up your person first.");
    const f = await this.features();
    if (!["env2", "person2", "caps"].every((x) => f.includes(x))) throw new Error("Your server cannot carry conversations (it needs an update).");
    address = String(address || "").trim();
    const pin = await this.pinned(address);
    if (pin.pending) throw new Error(address + "'s key changed; nothing is started until it is trusted.");
    let them;
    try {
      them = await this.personOf(address, pin);
    } catch (e) {
      throw new Error(e.message);
    }
    if (them.person === this.me.person) throw new Error("That is your own person.");
    const [ok, why] = await this.supports(address, pin);
    if (!ok) throw new Error(why);
    const now = await this.store.get("persons", them.person);
    if (!now || now.state !== "pinned") throw new Error("This person's record conflicts with the one kept here; it is frozen.");
    them = now;
    const c = await wire.newRoot(this.keys, { person: this.me.person, roster: this.me.hash, address: this.address, fingerprint: this.me.fingerprint },
      { person: them.person, roster: them.hash });
    const id = await wire.rootID(c);
    await put(this.store, "convs", id, { id, root: wire.rootJSON(c), peer: them.person, created: c.created, creator: this.address });
    this.changed();
    // Starting a DM is deciding on that person: with notifications on, they may alert.
    if ((await this.notifyState()).enabled) this.syncNotify().catch(() => {});
    return id;
  }

  // sendDM sends a message to the person. Never a question or task for
  // them, never v1.
  async sendDM({ conv, body, reply_to: replyTo, files = [] }) {
    body = String(body || "").trim();
    if (!body && !files.length) throw new Error("Write a message or add a file first.");
    if (files.length > 8) throw new Error("A message takes at most 8 files.");
    let total = 0;
    for (const f of files) {
      total += f.size;
      if (f.size > wire.BrowserMaxFile) throw new Error(f.name + " is larger than this browser sends (" + (wire.BrowserMaxFile >> 20) + " MiB); send it from a computer with AgentNet.");
    }
    if (total > wire.BrowserMaxMessage) throw new Error("These files are more than this browser sends in one message (" + (wire.BrowserMaxMessage >> 20) + " MiB); send fewer at a time.");
    const c = await this.store.get("convs", conv);
    if (!c) throw new Error("No conversation " + conv + " here.");
    if (replyTo) {
      const m = (await this.store.get("inbox", replyTo)) || (await this.store.get("outbox", replyTo));
      if (!m || m.conv !== conv) throw new Error("A reply stays within its conversation.");
    }
    return this.sendConv(c, { kind: "message", body, reply_to: replyTo || "", origin: "ui", files });
  }

  // sendConv stores the sealed envelope before it is sent, and sends that
  // exact envelope on every retry: a message, a participation record (sub
  // "event") or a request to another device's agent (pid and target).
  async sendConv(c, { kind, body, reply_to: replyTo = "", origin = "", sub = "", pid = "", target = null, files = [] }) {
    const { why: stop } = await this.gate(c);
    if (stop) throw new Error(stop);
    // Fresh evidence first: newer steps of both persons decide the devices.
    for (const p of [await this.store.get("persons", c.peer), this.me]) {
      try { await this.refreshPerson(p); } catch (e) { /* kept as pinned; its profile is read below */ }
    }
    const { why: again, peer } = await this.gate(c);
    if (again) throw new Error(again);
    const me = this.me;
    // One copy for each device of the other person and each other device of
    // yours (a copy kept as yours there, never run there, unless it is the
    // request's own target), sealed and its files encrypted to that device.
    const fan = [{ person: me.person, roster: me.hash }, { person: peer.person, roster: peer.hash }];
    const devices = [...peer.devices.map((d) => ({ ...d, own: false })), ...me.devices.filter((d) => d.address !== this.address).map((d) => ({ ...d, own: true }))];
    const lid = wire.newID(), at = this.now();
    const attention = origin === "ui" && sub === "" && ["message", "question", "task"].includes(kind);
    const plain = [];
    for (const f of files) plain.push({ name: wire.safeName(f.name), bytes: f.bytes instanceof Uint8Array ? f.bytes : new Uint8Array(await f.arrayBuffer()) });
    const recs = [];
    for (const dev of devices) {
      const pin = await this.store.get("pins", dev.address);
      if (!pin || pin.fingerprint !== dev.fingerprint || pin.pending) continue; // a changed key is never used
      let ok = false, why = "", notify = false;
      try {
        [ok, why, notify] = await this.supports(dev.address, pin);
      } catch (e) {
        if (this.revoked) throw new Error("This device was removed from its server: nothing more is sent or received here.");
        why = "cannot reach your server";
      }
      const recipient = await this.pubOf(pin);
      const sealed = [];
      for (const f of plain) sealed.push({ ...(await wire.encryptFile(f.bytes, f.name, recipient)), uploaded: false });
      const replica = dev.own && !(target && target.address === dev.address && target.fingerprint === dev.fingerprint);
      const chan = notify && !dev.own && attention ? await wire.notifyChannel(c.id, dev.fingerprint) : "";
      const id = wire.newID();
      const envelope = await wire.seal({ v: wire.Version2, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), kind,
        body, reply_to: replyTo, conv: c.id, lid, root: c.root, origin, sub, pid, target, chan, replica, fan, attachments: sealed.map((f) => f.attachment) },
      this.keys, recipient);
      recs.push({ id, conv: c.id, lid, body, reply_to: replyTo, kind, origin, sub, pid, target, at, to: dev.address, own: dev.own, envelope,
        attachments: sealed.map((f) => f.attachment), files: sealed.length ? sealed : undefined, state: ok ? "queued" : "waiting", detail: why });
    }
    if (!recs.length) throw new Error("No device of this conversation can be sent a copy now.");
    const final = (await this.gate(c)).why; // a profile just read may have frozen the person
    if (final) throw new Error(final);
    await this.store.write(recs.map((r) => ({ s: "outbox", k: r.id, v: r })));
    this.changed();
    for (const r of recs) if (r.state === "queued") await this.post(r);
    const least = copyOrder(recs);
    return { id: recs[0].id, lid, state: least.state, detail: least.detail, copies: recs.map((r) => ({ id: r.id, to: r.to, state: r.state, detail: r.detail })) };
  }

  async post(rec) {
    try {
      // The files first, each resumable; the message names them only once
      // the relay holds them.
      for (const f of rec.files || []) {
        if (f.uploaded) continue;
        await this.uploadBlob(rec.to, f.attachment.blob, f.ct);
        f.uploaded = true;
        await put(this.store, "outbox", rec.id, rec);
      }
      // Uploads take time: a conflict seen meanwhile (another send's
      // profile read, a message received) stops the handover here, and the
      // message stays queued, saying why.
      if (rec.conv) {
        const { why } = await this.gate(await this.store.get("convs", rec.conv), rec);
        if (why) {
          rec.detail = why;
          await put(this.store, "outbox", rec.id, rec);
          this.changed();
          return;
        }
      }
      const r = await this.call("POST", "/v1/messages", rec.envelope);
      rec.state = (r && r.state) || "custody";
      rec.detail = "";
      // The relay has everything: the ciphertext kept here is not needed.
      if (rec.files) rec.files = rec.files.map((f) => ({ ...f, ct: null }));
    } catch (e) {
      if (retryable(e)) {
        rec.detail = e.message;
      } else {
        rec.state = "failed";
        rec.detail = e.code === "recipient_revoked" ? "that device was removed from the server"
          : e.status === 413 ? "your server refuses this file (its size limit or its space is reached): " + e.message : e.message;
      }
    }
    await put(this.store, "outbox", rec.id, rec);
    this.changed();
    if (rec.state === "custody") this.awaitReceipt(rec.id);
  }

  async awaitReceipt(id) {
    try {
      const r = await this.call("GET", "/v1/messages/" + id + "/wait?timeout=5s");
      if (r && r.state && r.state !== "custody") await this.setOutState(id, r.state);
    } catch (e) { /* asked again when the DM is opened */ }
  }

  async setOutState(id, state) {
    const rec = await this.store.get("outbox", id);
    if (!rec || rec.state === state) return;
    await put(this.store, "outbox", id, { ...rec, state });
    this.changed();
  }

  // gate says why nothing may be sent in conversation c now ("" when it
  // may), with the other member's person: yours and theirs must be kept
  // and not in conflict, each bound to a step of its pinned chain; for a
  // copy (rec), its device must still be a current device of either, with
  // its key pinned and unchanged. A new message and every kept copy pass
  // it before they are sent.
  async gate(c, rec) {
    const peer = c && (await this.store.get("persons", c.peer));
    if (!peer) return { why: "The other member's person record is not kept here." };
    if (peer.state === "conflict") return { why: "This person's record conflicts with the one kept here; the conversation is frozen.", peer };
    if (!this.me) return { why: "This device has no person." };
    let root;
    try { root = wire.parseRoot(c.root); } catch (e) { return { why: "This conversation's root is not valid.", peer }; }
    for (const p of [this.me, peer]) {
      if (!p.hashes.includes(wire.rootMember(root, p.person))) return { why: "A member's person record here does not follow the one this conversation names.", peer };
    }
    if (rec) {
      const dev = [...peer.devices, ...this.me.devices].find((d) => d.address === rec.to);
      if (!dev) return { why: rec.to + " is no longer a device of this conversation's people: its copy is not sent.", peer };
      const pin = await this.store.get("pins", rec.to);
      if (!pin || pin.pending || pin.fingerprint !== dev.fingerprint) return { why: rec.to + "'s key is no longer the one its person record names; the conversation is frozen.", peer };
      return { why: "", peer, pin };
    }
    return { why: "", peer };
  }

  // flushOutbox sends what is kept: queued messages, and waiting ones once
  // the recipient can read them. Each passes the gate first, again after a
  // profile read that may freeze the person; what does not pass is kept
  // and never sent.
  async flushOutbox() {
    for (const rec of await this.store.all("outbox")) {
      if (!this.connected) return;
      if (rec.state !== "queued" && rec.state !== "waiting") continue;
      const c = await this.store.get("convs", rec.conv);
      let g;
      try { g = await this.gate(c, rec); } catch (e) { continue; }
      if (!g.why && rec.state === "waiting") {
        let ok = false;
        try { [ok] = await this.supports(rec.to, g.pin); } catch (e) { continue; }
        if (!ok) continue;
        g = await this.gate(c, rec);
      }
      if (g.why) {
        if (rec.detail !== g.why) {
          await put(this.store, "outbox", rec.id, { ...rec, detail: g.why });
          this.changed();
        }
        continue;
      }
      await this.post({ ...rec, state: "queued", detail: "" });
    }
  }

  // ---- receiving

  async onMessage(data) {
    let env;
    try {
      env = wire.parseEnvelope(data);
    } catch (e) {
      let head = {};
      try { head = JSON.parse(data); } catch (err) { /* nothing to name */ }
      if (typeof head.id === "string" && wire.validID(head.id)) {
        await this.store.write([{ s: "held", k: head.id, v: { id: head.id, from: String(head.from || ""), reason: "invalid", envelope: data, at: this.now() } },
          { s: "receipts", k: head.id, v: { id: head.id, state: "quarantined" } }]);
        await this.flushReceipts();
      }
      return;
    }
    if ((await this.store.get("inbox", env.id)) || (await this.store.get("held", env.id)) || (await this.store.get("receipts", env.id))) {
      // Seen before: its receipt is sent again, nothing is stored twice.
      const state = (await this.store.get("inbox", env.id)) ? "delivered" : "quarantined";
      if (!(await this.store.get("receipts", env.id))) await put(this.store, "receipts", env.id, { id: env.id, state });
    } else {
      await this.admit(data, env); // stored (or held) before any receipt
    }
    await this.flushReceipts();
  }

  async hold(env, data, reason) {
    await this.store.write([{ s: "held", k: env.id, v: { id: env.id, from: env.from, reason, envelope: data, at: this.now() } },
      { s: "receipts", k: env.id, v: { id: env.id, state: "quarantined" } }]);
    this.changed();
  }

  // admit verifies and stores one envelope as the Go client does
  // (verifyAndStore, admitConv). Failures to ask the server are thrown, so
  // the message stays unacknowledged and comes again.
  async admit(data, env, fromHeld = false) {
    try {
      const ops = await this.admitInner(data, env);
      // A held message that now proves out was acknowledged as held already.
      ops.push(fromHeld ? { s: "held", k: env.id, v: undefined } : { s: "receipts", k: env.id, v: { id: env.id, state: "delivered" } });
      await this.store.write(ops);
      this.changed();
    } catch (e) {
      if (e instanceof Hold) {
        if (!fromHeld) await this.hold(env, data, e.reason);
        else if (e.reason !== "proof_pending") {
          const h = await this.store.get("held", env.id);
          await put(this.store, "held", env.id, { ...h, reason: e.reason });
          this.changed();
        }
        return;
      }
      throw e;
    }
  }

  async admitInner(data, env) {
    if (env.to !== this.address) throw new Hold("invalid", "addressed to another device");
    let pin = await this.store.get("pins", env.from);
    if (!pin) {
      try {
        pin = await this.pinned(env.from);
      } catch (e) {
        if (retryable(e)) throw e;
        throw new Hold("invalid", "no key for " + env.from);
      }
    }
    if (pin.pending) throw new Hold("key_changed", env.from + "'s key changed");
    let n;
    try {
      n = await wire.open(data, this.keys, this.address, await this.pubOf(pin));
    } catch (err) {
      let d;
      try {
        d = await this.directory(env.from);
      } catch (e) {
        if (retryable(e)) throw e;
        throw new Hold("invalid", err.message);
      }
      if (d.fingerprint !== pin.fingerprint) {
        await put(this.store, "pins", env.from, { ...pin, pending: { json: d.json, fingerprint: d.fingerprint } });
        throw new Hold("key_changed", env.from + "'s key changed");
      }
      throw new Hold("invalid", err.message);
    }
    const base = { id: env.id, from: env.from, kind: n.kind, body: n.body, reply_to: n.reply_to, at: this.now(), fp: pin.fingerprint, read: false,
      attachments: n.attachments.length ? n.attachments : undefined };
    if (n.v === 1) return [{ s: "inbox", k: env.id, v: { ...base, v: 1, state: n.kind === "question" || n.kind === "task" ? "held" : "" } }];
    return this.admitConv(n, env, pin, base);
  }

  async admitConv(n, env, pin, base) {
    let root;
    try {
      root = wire.parseRoot(n.root);
    } catch (e) {
      throw new Hold("invalid", "its conversation root is not valid");
    }
    if ((await wire.rootID(root)) !== n.conv) throw new Hold("invalid", "its conversation root does not match the conversation");
    if (!this.me) throw new Hold("invalid", "this device has no person");
    const mine = wire.rootMember(root, this.me.person);
    if (!mine) throw new Hold("invalid", "this device's person is not a member");
    if (!this.me.hashes.includes(mine)) {
      await this.refreshPerson(this.me);
      if (!this.me.hashes.includes(mine)) throw new Hold("proof_pending", "this device's person record here does not follow the one the conversation names");
    }
    // The sender: a current device of a member person, as pinned here (one
    // of yours, or the other person's).
    const own = this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint);
    const sp = own ? this.me : await this.personOf(env.from, pin);
    const bound = wire.rootMember(root, sp.person);
    if (!bound) throw new Hold("invalid", "the sender is not a member of this conversation");
    if (!sp.hashes.includes(bound)) {
      const again = await this.refreshPerson(sp);
      if (!again.hashes.includes(bound)) throw new Hold("proof_pending", "the sender's person record here does not follow the one the conversation names");
    }
    let conv = await this.store.get("convs", n.conv);
    const ops = [];
    if (!conv) {
      // Any member device may bring a conversation: its root is checked
      // against the creator's key as its person's chain names it, and every
      // member is pinned.
      const other = root.members.find((m) => m.person !== this.me.person);
      if (!other) throw new Hold("invalid", "a DM with yourself");
      const creator = root.creator.person === this.me.person ? this.me : await this.pinChain(root.creator.person);
      const peer = other.person === (creator && creator.person) ? creator : await this.pinChain(other.person);
      if (!creator.hashes.includes(root.creator.roster) || !peer.hashes.includes(other.roster)) {
        throw new Hold("proof_pending", "a member's person record here does not follow the one the conversation names");
      }
      const dev = creator.known.find((d) => d.address === root.creator.address && d.fingerprint === root.creator.fingerprint);
      if (!dev) throw new Hold("proof_pending", "its creator is not a device of its person here");
      try {
        await wire.verifyRoot(root, (await wire.parsePublic(JSON.parse(dev.json))).sign_key);
      } catch (e) {
        throw new Hold("invalid", e.message);
      }
      conv = { id: n.conv, root: wire.rootJSON(root), peer: other.person, created: root.created, creator: root.creator.address };
      ops.push({ s: "convs", k: n.conv, v: conv });
    }
    if (n.reply_to) {
      const m = (await this.store.get("inbox", n.reply_to)) || (await this.store.get("outbox", n.reply_to));
      if (m && m.conv !== n.conv) throw new Hold("invalid", "it replies to a message outside its conversation");
    }
    if (n.sub === "history") throw new Hold("invalid", "history is not read on this device yet");
    if (n.sub === "event") { // a participation record: the sending device's own, for this very conversation, signed
      let e;
      try {
        e = wire.parseEvent(n.body);
        if (n.kind !== "message" || !n.pid || e.conv !== n.conv || e.pid !== n.pid || e.author.address !== env.from || e.author.fingerprint !== pin.fingerprint) {
          throw new Error("the record is not the sending device's own, for this conversation");
        }
        await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key);
      } catch (err) {
        throw new Hold("invalid", "participation: " + err.message);
      }
    }
    const hash = wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify(
      [n.kind, n.body, n.reply_to, n.conv, n.sub, n.origin, n.emotion, n.target, n.pid, n.status]))));
    const key = pin.fingerprint + "/" + n.lid;
    const seen = await this.store.get("lids", key);
    if (seen) {
      if (seen.hash !== hash) throw new Hold("conflicting_duplicate", "a message with the same key and logical id but other content is stored");
      return ops; // the same message again: acknowledged, not stored twice
    }
    // A copy from another device of yours is yours: shown as sent (from
    // there), never held or run here. A request to another device's agent
    // is history here (as the core keeps it); any other question or task is
    // held for the person. This browser never runs anything.
    const request = n.kind === "question" || n.kind === "task";
    const rec = { ...base, v: 2, conv: n.conv, lid: n.lid, sub: n.sub, pid: n.pid, origin: n.origin, emotion: n.emotion, replica: n.replica,
      target: n.target || null, own, read: own || base.read,
      state: !own && request && !(n.target && n.target.address !== this.address) ? "conv_held" : "" };
    // The attention hint is the sender's claim: recorded, checked against
    // this DM's channel here, never used to route anything.
    if (env.attn && !own) rec.attn = env.chan === (await wire.notifyChannel(n.conv, this.fp)) ? "ok" : "mismatch";
    return [...ops, { s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash } }];
  }

  // retryHeld looks again at messages held for missing proof, when new
  // evidence may have come (a connection, a member list). One pass reads
  // a bounded page at a time, in key order, and continues to the end, so
  // messages that stay unproven never keep later ones from being looked
  // at. Evidence that comes during a pass adds a pass from the beginning
  // once this one ends; an unreachable server ends it (the next connection
  // looks again).
  retryHeld() {
    this.retryAgain = true;
    if (!this.retrying) this.retrying = this.retryPasses();
    return this.retrying;
  }

  async retryPasses() {
    try {
      this.retryAgain = false;
      let pos = "";
      for (;;) {
        const page = await this.store.after("held", pos, heldPage);
        for (const h of page) {
          if (h.reason !== "proof_pending") continue;
          try {
            await this.admit(h.envelope, wire.parseEnvelope(h.envelope), true);
          } catch (e) {
            return;
          }
        }
        if (page.length === heldPage) pos = page[page.length - 1].id;
        else if (this.retryAgain) [this.retryAgain, pos] = [false, ""];
        else return;
      }
    } finally {
      this.retrying = null;
    }
  }

  // flushReceipts sends every stored receipt the server has not taken yet.
  async flushReceipts() {
    for (const r of await this.store.all("receipts")) {
      try {
        await this.call("POST", "/v1/messages/" + r.id + "/ack", { state: r.state });
      } catch (e) {
        if (!(e instanceof HubError && e.status === 404)) return; // kept; sent again later
      }
      await put(this.store, "receipts", r.id, undefined);
    }
  }

  // ---- the stream

  start() {
    if (this.running || this.revoked || !this.joined) return;
    this.running = true;
    this.loop();
  }

  stop() {
    this.running = false;
    if (this.abort) this.abort.abort();
    if (this.wake) this.wake();
  }

  // kick reconnects now, if it is waiting to.
  kick() { if (this.wake) this.wake(); }

  // online: the browser says the network is back. What is kept is tried at
  // once, over the open connection or a new one.
  online() {
    if (this.connected) this.flushOutbox().catch(() => {});
    else this.kick();
  }

  // offline: the browser says the network is gone. The connection is
  // dropped now, so the page does not claim one; it is tried again with
  // backoff, and at once when the network is back.
  offline() {
    if (this.abort) this.abort.abort();
  }

  async loop() {
    let backoff = 1000;
    while (this.running && !this.revoked) {
      let healthy = false;
      try {
        healthy = await this.streamOnce();
      } catch (e) { /* reconnect below */ }
      if (!this.running || this.revoked) break;
      if (healthy) backoff = 1000;
      const wait = backoff / 2 + Math.random() * (backoff / 2);
      await new Promise((r) => { const t = setTimeout(r, wait); this.wake = () => { clearTimeout(t); r(); }; });
      this.wake = null;
      backoff = Math.min(backoff * 2, MAX_BACKOFF);
    }
  }

  async streamOnce() {
    this.featureList = null;
    this.session = wire.newID();
    const path = "/v1/stream?ad=" + (await wire.sessionAd(this.keys, this.address, this.session));
    const ctrl = new AbortController();
    this.abort = ctrl;
    const headers = { ...(await wire.signRequest(this.keys, this.address, "GET", path, "")), Accept: "text/event-stream" };
    let r;
    try {
      r = await this.fetch(this.base + path, { headers, signal: ctrl.signal, cache: "no-store" });
    } catch (e) {
      return false;
    }
    if (!r.ok) {
      let j = {};
      try { j = await r.json(); } catch (e) { /* none */ }
      if (j.code === "revoked") await this.setRevoked();
      return false;
    }
    this.connected = true;
    this.members = { ...this.members, listed: r.headers.get("Agentnet-Members") === "1" ? "listed" : "not_listed", current: false };
    this.changed();
    let watchdog = setTimeout(() => ctrl.abort(), 3 * HEARTBEAT);
    const work = this.onConnect();
    const reader = r.body.getReader();
    const decoder = new TextDecoder();
    let buf = "", event = "", data = "", healthy = true;
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        clearTimeout(watchdog);
        watchdog = setTimeout(() => ctrl.abort(), 3 * HEARTBEAT);
        buf += decoder.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, i).replace(/\r$/, "");
          buf = buf.slice(i + 1);
          if (line === "") {
            if (event) await this.dispatch(event, data);
            event = "";
            data = "";
          } else if (line.startsWith("event: ")) event = line.slice(7);
          else if (line.startsWith("data: ")) data += line.slice(6);
        }
      }
    } catch (e) {
      healthy = !(e instanceof HubError); // a message that could not be processed yet ends the connection
    } finally {
      clearTimeout(watchdog);
      this.connected = false;
      this.members = { ...this.members, current: false };
      this.changed();
      ctrl.abort();
      await work.catch(() => {});
    }
    return healthy;
  }

  async onConnect() {
    try {
      const f = await this.features();
      // This device reads conversations and the attention hint (it never
      // alerts from the stream: its service worker shows the relay's pushes).
      if (f.includes("caps")) await this.call("PUT", "/v1/caps", wire.capsJSON(await wire.newCaps(this.keys, this.address, this.session, [wire.CapEnv2, "notify1", wire.CapPerson])));
      await this.publishPerson().catch(() => {});
      await this.flushReceipts();
      await this.retryHeld();
      await this.flushOutbox();
      await this.reconcileNotify().catch(() => {});
    } catch (e) { /* tried again on the next ping */ }
  }

  async dispatch(event, data) {
    if (event === "message") {
      await this.onMessage(data);
    } else if (event === "members") {
      let m;
      try { m = JSON.parse(data); } catch (e) { this.members = { ...this.members, current: false }; this.changed(); return; }
      this.members = { listed: "listed", current: true, at: this.now(), list: Array.isArray(m.members) ? m.members : [], truncated: !!m.truncated };
      // A member's person reference that is ahead of the step pinned here is
      // followed (verified step by step); others' claimed names are read
      // once per step for the people list, never trusted.
      for (const x of this.members.list) {
        const ref = x.person;
        if (!ref || !wire.validID(ref.id)) continue;
        const p = ref.id === (this.me && this.me.person) ? this.me : await this.store.get("persons", ref.id);
        if (p && (ref.seq > p.seq || (ref.seq === p.seq && ref.hash !== p.hash))) this.refreshPerson(p).catch(() => {});
      }
      this.fillListed().catch(() => {});
      this.changed();
      this.retryHeld().catch(() => {});
    } else if (event === "ping") {
      let p = {};
      try { p = JSON.parse(data); } catch (e) { /* none */ }
      if (p.conn) this.call("POST", "/v1/stream/ack", { conn: p.conn }).catch(() => {});
      this.flushOutbox().catch(() => {});
      this.flushReceipts().catch(() => {});
    }
  }

  // fillListed reads, once per roster step, the person record of members
  // not pinned here: their claimed name and devices for the people list
  // (unverified; pinned only when a DM starts).
  async fillListed() {
    if (!this.listed) this.listed = new Map();
    let reads = 0;
    const want = new Set();
    for (const m of this.members.list) {
      const ref = m.person;
      if (!ref || !wire.validID(ref.id) || !wire.validHash(ref.hash) || m.address === this.address) continue;
      if (ref.id === (this.me && this.me.person) || (await this.store.get("persons", ref.id))) continue;
      want.add(ref.hash);
      if (this.listed.has(ref.hash) || reads >= 64) continue;
      reads++;
      try {
        const prof = await this.profile(m.address);
        const r = await wire.parseRoster(prof.person);
        if (r.person === ref.id && (await wire.rosterHash(r)) === ref.hash) {
          this.listed.set(ref.hash, { person: r.person, label: r.label, devices: r.devices.map((d) => d.address) });
        }
      } catch (e) { /* not shown */ }
    }
    for (const h of [...this.listed.keys()]) if (!want.has(h)) this.listed.delete(h);
    this.changed();
  }

  // convMessages are a conversation's messages as shown: received ones
  // (those from your other devices are yours, sent from there), and each
  // message sent here once, with its copies, its state the least advanced
  // copy's.
  convMessages(convId, inbox, outbox) {
    const groups = new Map();
    for (const r of outbox) {
      if (r.conv !== convId) continue;
      const g = groups.get(r.lid || r.id) || [];
      g.push(r);
      groups.set(r.lid || r.id, g);
    }
    const sent = [...groups.values()].map((g) => {
      const least = copyOrder(g);
      return { ...g[0], state: least.state, detail: least.detail, lagging: least.to, copies: g.map((r) => ({ id: r.id, to: r.to, state: r.state, detail: r.detail })) };
    });
    return [...inbox.filter((m) => m.conv === convId), ...sent].sort((a, b) => a.at - b.at);
  }

  // ---- what the page reads (the daemon page API's shapes)

  async overview() {
    const persons = await this.store.all("persons");
    const people = persons.map((p) => this.personView(p));
    const listedSeen = new Set();
    for (const m of this.members.list) {
      const l = m.person && this.listed && this.listed.get(m.person.hash);
      if (!l || listedSeen.has(l.person) || persons.some((p) => p.person === l.person)) continue;
      listedSeen.add(l.person);
      people.push({ label: l.label, address: l.devices[0], state: "listed",
        devices: l.devices.map((a) => ({ address: a, name: a.split("/")[1], fingerprint: "" })) });
    }
    const inbox = await this.store.all("inbox");
    const outbox = await this.store.all("outbox");
    const dms = [], links = new Map(); // person → the agents their device runs in DMs here
    for (const c of await this.store.all("convs")) {
      const peer = persons.find((p) => p.person === c.peer);
      const msgs = this.convMessages(c.id, inbox, outbox);
      for (const info of await this.agentsOf(c)) {
        if (!info.host) continue;
        const ls = links.get(info.host.person) || [];
        let l = ls.find((x) => x.address === info.host.address);
        if (!l) ls.push(l = { address: info.host.address, dms: [] });
        l.dms.push({ conv: c.id, pid: info.pid, state: info.state });
        links.set(info.host.person, ls);
      }
      const line = (m) => (m.sub === "event" ? this.eventText(m.body, peer)
        : !m.body && (m.attachments || []).length ? "📎 " + m.attachments.map((a) => wire.safeName(a.name)).join(", ") // files only: their names
          : firstLine(m.body));
      dms.push({ id: c.id, peer: this.personView(peer), created: iso(c.created * 1000), mine: c.creator === this.address, count: msgs.length,
        title: msgs[0] ? line(msgs[0]) : "", last: msgs.length ? line(msgs[msgs.length - 1]) : "",
        last_at: iso(msgs.length ? msgs[msgs.length - 1].at : c.created * 1000),
        unread: msgs.filter((m) => m.fp && !m.own && !m.read).length, held: msgs.filter((m) => m.state === "conv_held").length,
        waiting: msgs.filter((m) => m.state === "waiting").length });
    }
    dms.sort((a, b) => b.last_at.localeCompare(a.last_at));
    for (const p of people) if (links.has(p.person)) p.agents = links.get(p.person);
    const threads = inbox.filter((m) => m.v === 1).map((m) => ({ id: m.id, peer: m.from, title: firstLine(m.body), last: firstLine(m.body),
      last_at: iso(m.at), count: 1, review: 0, unread: m.read ? 0 : 1, running: 0, waiting: false, key_changed: false, notices: 0, notice_only: false }));
    const held = await this.store.all("held");
    return {
      demo: false, seq: this.seq, version: this.version, release: "",
      me: { address: this.address, fingerprint: this.fp, responder: "", responder_dir: "", browser: true },
      device: { online: this.connected, revoked: this.revoked, persisted: this.storage ? this.storage.persisted : null },
      threads, review: [], quarantine: held.map((h) => ({ id: h.id, peer: h.from, reason: holdText(h.reason, h.from), at: iso(h.at) })),
      directory: { status: this.members.listed, current: this.members.current, at: this.members.at ? iso(this.members.at) : undefined,
        truncated: this.members.truncated, members: this.members.list.filter((m) => m.address !== this.address)
          .map((m) => ({ address: m.address, presence: this.members.current ? m.presence : "", joined: iso((m.joined || 0) * 1000) })) },
      notify: await this.notifyView(),
      files: { max_file: wire.BrowserMaxFile, max_message: wire.BrowserMaxMessage, max_count: 8 },
      persons: true, agents: true, // agents on the other person's computer: invited, asked and dismissed here, never run here
      person: this.personView(this.me, this.me ? { published: !!this.me.published } : undefined) || undefined, people, dms,
    };
  }

  async dm(id) {
    const c = await this.store.get("convs", id);
    if (!c) throw new Error("No conversation with that id.");
    const peer = await this.store.get("persons", c.peer);
    const msgs = this.convMessages(id, await this.store.all("inbox"), await this.store.all("outbox"));
    return { id, peer: this.personView(peer), created: iso(c.created * 1000), mine: c.creator === this.address,
      frozen: peer && peer.state === "conflict" ? peer.address + " published a different person record than the one kept here, so this conversation is frozen: nothing more is sent in it." : "",
      agents: (await this.agentsOf(c)).map((info) => this.agentView(info, msgs, peer)),
      messages: msgs.map((m) => {
        const here = !m.fp, out = here || !!m.own; // sent here, or from another device of yours
        const event = m.sub === "event" ? this.eventText(m.body, peer) : "";
        return { id: m.id, dir: out ? "out" : "in", from: here ? this.address : m.from, kind: m.kind, body: event ? "" : m.body, reply_to: m.reply_to || "",
          origin: m.origin || "", state: m.state, detail: m.detail || "", at: iso(m.at), unread: !out && !m.read, replica: !!m.replica,
          pid: m.pid || "", to: m.target ? m.target.address : "", event, via: m.own ? m.from : "", copies: here ? m.copies : undefined,
          attachments: (m.attachments || []).map((a, i) => ({ index: i, name: wire.safeName(a.name), size: a.size })),
          state_text: event ? "" : here ? outText(m.state, m.lagging || (peer ? peer.address : ""), m.detail) : m.state === "conv_held" ? "Held for you: nothing runs it. Answer here if you want to." : "" };
      }) };
  }

  // ---- agents in DMs: shown, invited, asked and dismissed here, as the
  // core resolves them; never hosted, accepted or run (this browser runs
  // nothing).

  // dmMembers are c's member persons as pinned here now: this device's
  // person and the other one unless frozen, each with the roster its root
  // names.
  async dmMembers(c) {
    const root = wire.parseRoot(c.root);
    const out = new Map();
    for (const p of [this.me, await this.store.get("persons", c.peer)]) {
      if (p && p.state !== "conflict" && p.hashes.includes(wire.rootMember(root, p.person))) out.set(p.person, p);
    }
    return out;
  }

  // convEvents are the participation records of conv held here, received
  // and sent, oldest first: a set by record hash, as the core keeps them
  // (the same signed record in another message is one record).
  async convEvents(conv) {
    const rows = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))]
      .filter((m) => m.conv === conv && m.sub === "event").sort((a, b) => a.at - b.at);
    const out = [], seen = new Set();
    for (const m of rows) {
      try {
        const e = wire.parseEvent(m.body);
        const hash = await wire.eventHash(e);
        if (!seen.has(hash)) out.push({ e, hash });
        seen.add(hash);
      } catch (err) { /* admitted records parse; nothing else counts */ }
    }
    return out;
  }

  // resolveAgent is the core's resolve (participation.go): a
  // participation's state from the set of its records, never their order,
  // with the members as pinned now. A record that does not count is held:
  // it has no effect until its evidence is here.
  resolveAgent(pid, evs, m) {
    const info = { pid, state: "pending", held: 0, host: null, inviter: null, grant: [], taskKeys: [], note: "", invite: "", decision: "", dismissal: "", conflict: "", invited: 0 };
    // A current device of a member person (the person as seen from it);
    // an author also names a step of that person's chain.
    const at = (p, address, fp) => (p && p.devices.some((d) => d.address === address && d.fingerprint === fp) ? { ...p, address, fingerprint: fp } : null);
    const author = (a) => { const p = m.get(a.person); return p && p.hashes.includes(a.roster) ? at(p, a.address, a.fingerprint) : null; };
    const host = (h) => (h ? at(m.get(h.person), h.address, h.fingerprint) : null);
    const memberKey = (fp) => [...m.values()].some((p) => p.devices.some((d) => d.fingerprint === fp));
    const invites = new Map(), decisions = [], dismisses = [];
    for (const x of evs.filter((y) => y.e.pid === pid)) {
      if (!author(x.e.author)) { info.held++; continue; }
      if (x.e.type === "invite") {
        if (!host(x.e.host) || !(x.e.task_keys || []).every(memberKey)) { info.held++; continue; }
        invites.set(x.hash, x);
      } else if (x.e.type === "dismiss") dismisses.push(x);
      else decisions.push(x);
    }
    const known = new Set(invites.keys());
    let inv = null;
    if (invites.size === 1) {
      [[info.invite, inv]] = [...invites];
      Object.assign(info, { host: host(inv.e.host), inviter: author(inv.e.author), grant: inv.e.grant || [], taskKeys: inv.e.task_keys || [],
        note: inv.e.note, invited: inv.e.ts, state: "invited" });
    } else if (invites.size > 1) {
      Object.assign(info, { state: "conflict", conflict: "different invites share this participation id" });
    }
    const decided = [];
    for (const x of decisions) {
      const h = inv && inv.e.host;
      if (info.state !== "invited" || x.e.prev !== info.invite || x.e.author.person !== h.person || x.e.author.address !== h.address ||
        x.e.author.fingerprint !== h.fingerprint) { info.held++; continue; }
      decided.push(x);
      known.add(x.hash);
    }
    decided.sort((a, b) => (a.hash < b.hash ? -1 : a.hash > b.hash ? 1 : 0));
    if (decided.length === 1) Object.assign(info, { state: decided[0].e.type === "accept" ? "active" : "declined", decision: decided[0].hash });
    else if (decided.length > 1) Object.assign(info, { state: "conflict", decision: decided[0].hash, conflict: "the host decided more than once" });
    for (const x of dismisses) {
      if (!known.has(x.e.prev)) { info.held++; continue; }
      if (info.state !== "dismissed" || x.hash < info.dismissal) info.dismissal = x.hash;
      info.state = "dismissed";
    }
    return info;
  }

  // agentsOf resolves every participation of conv c.
  async agentsOf(c) {
    const m = await this.dmMembers(c), evs = await this.convEvents(c.id);
    return [...new Set(evs.map((x) => x.e.pid))].map((pid) => this.resolveAgent(pid, evs, m));
  }

  // agentView is a participation as the page shows it (ui.AgentView).
  agentView(info, msgs, peer) {
    const hostHere = !!info.host && info.host.address === this.address;
    const label = (p) => (p === this.me ? this.me.label : p ? p.label : "");
    const shared = [], people = [this.me, peer].filter(Boolean);
    let missing = 0;
    for (const g of info.grant) {
      const m = msgs.find((x) => x.lid === g.lid && (x.fp || this.fp) === g.fingerprint && !x.sub && !x.replica);
      if (m) shared.push(m.id); else missing++;
    }
    const whose = hostHere ? "your" : label(info.host) + "'s";
    const v = { pid: info.pid, state: info.state, host: this.personView(info.host), host_here: hostHere, inviter: this.personView(info.inviter),
      note: info.note, shared, missing, tasks_from: info.taskKeys.map((fp) => {
        const p = people.find((x) => x.devices.some((d) => d.fingerprint === fp));
        return p && this.personView({ ...p, fingerprint: fp, address: p.devices.find((d) => d.fingerprint === fp).address });
      }).filter(Boolean),
      held: info.held, invited: info.invited ? iso(info.invited * 1000) : "", can_decide: false, can_dismiss: false, can_ask: false, state_text: "" };
    switch (info.state) {
    case "pending": v.state_text = "Its invitation is not here yet: nothing counts until it is."; break;
    case "invited":
      v.state_text = hostHere ? "It names this browser to run an agent, but this browser runs none: it cannot accept."
        : "Invited. " + label(info.host) + " accepts or declines it on " + info.host.address + ".";
      v.can_dismiss = true;
      break;
    case "active":
      v.state_text = hostHere ? "It names this browser, which runs no agent." : "In this DM. It answers what either of you asks it, on " + info.host.address +
        " with " + whose + " own setup, and is shown only what was shared and what is asked of it here.";
      v.can_dismiss = true;
      v.can_ask = !hostHere && info.held === 0;
      break;
    case "declined": v.state_text = "Declined by " + label(info.host) + "."; break;
    case "conflict": v.state_text = "Its records conflict (" + info.conflict + "): nothing runs it."; v.can_dismiss = true; break;
    case "dismissed": v.state_text = "Dismissed: it gets nothing more from this DM."; break;
    }
    if (info.held > 0) v.state_text += " Some of its records do not count here yet.";
    return v;
  }

  // agentConv finds the conversation and participation of pid.
  async agentConv(pid) {
    const row = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].find((m) => m.pid === pid && m.sub === "event" && m.conv);
    const c = row && (await this.store.get("convs", row.conv));
    if (!c) throw new Error("No such agent in a DM here.");
    const info = this.resolveAgent(pid, await this.convEvents(c.id), await this.dmMembers(c));
    return { c, info };
  }

  author() {
    return { person: this.me.person, roster: this.me.hash, address: this.address, fingerprint: this.fp };
  }

  // inviteAgent invites the agent on the other member's device: never this
  // browser, which runs none. The earlier messages it may be shown and the
  // member keys it takes tasks from without asking are chosen by the person.
  async inviteAgent({ conv, host, share = [], tasks_from: tasks = [], note = "" }) {
    const c = await this.store.get("convs", conv);
    if (!c) throw new Error("No conversation " + conv + " here.");
    if (!this.me) throw new Error("Set up your person first.");
    if (host === this.address) throw new Error("This browser runs no agent: invite the agent on the other person's computer.");
    const m = await this.dmMembers(c);
    if (!m.has(this.me.person)) throw new Error("This device does not speak for a member of that conversation.");
    const hpp = [...m.values()].find((p) => p.devices.some((d) => d.address === host));
    if (!hpp) throw new Error(host + " is not the device of a member of that conversation (or its person is frozen).");
    const hp = { person: hpp.person, address: host, fingerprint: hpp.devices.find((d) => d.address === host).fingerprint };
    const msgs = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].filter((x) => x.conv === conv);
    const grant = share.map((id) => {
      const x = msgs.find((y) => y.id === id && !y.sub && !y.replica);
      if (!x || !x.lid) throw new Error("A message chosen to share is not an earlier message of this DM that can be shared.");
      return { lid: x.lid, fingerprint: x.fp || this.fp };
    });
    for (const fp of tasks) if (![...m.values()].some((p) => p.devices.some((d) => d.fingerprint === fp))) throw new Error(fp + " is not the key of a member of that conversation.");
    const e = await wire.signEvent(this.keys, { conv, pid: wire.newID(), type: "invite", ts: Math.floor(this.now() / 1000), author: this.author(),
      host: { person: hp.person, address: hp.address, fingerprint: hp.fingerprint }, grant: grant.length ? grant : null,
      audience: "conversation", task_keys: tasks.length ? tasks : null, note: String(note).trim() });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid: e.pid });
    return { pid: e.pid, state: "invited" };
  }

  // dismissAgent ends a participation, following the record it ends.
  async dismissAgent(pid) {
    const { c, info } = await this.agentConv(pid);
    if (!this.me || !(await this.dmMembers(c)).has(this.me.person)) throw new Error("This device does not speak for a member of that conversation.");
    let prev = info.decision;
    if (info.state === "invited") prev = info.invite;
    else if (!["active", "declined", "conflict"].includes(info.state) || !prev) throw new Error("The agent's participation is " + info.state + ".");
    const e = await wire.signEvent(this.keys, { conv: c.id, pid, type: "dismiss", prev, ts: Math.floor(this.now() / 1000), author: this.author() });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid });
    return { pid, state: "dismissed" };
  }

  // askAgent sends a question or task to an active participation's agent,
  // on the other person's computer: its one target.
  async askAgent({ pid, kind = "question", body }) {
    body = String(body || "").trim();
    if (!body) throw new Error("Write what to ask first.");
    if (kind !== "question" && kind !== "task") throw new Error("An agent is asked a question or given a task.");
    const { c, info } = await this.agentConv(pid);
    if (info.state !== "active" || info.held > 0) throw new Error("The agent's participation is " + info.state + ": not active.");
    if (info.host.address === this.address) throw new Error("This browser runs no agent.");
    return this.sendConv(c, { kind, body, pid, origin: "ui", target: { address: info.host.address, fingerprint: info.host.fingerprint } });
  }

  // ---- notifications (docs/revival/NOTIFY.md): off until the person turns
  // them on. The service worker shows every alert; this page never shows
  // one. The relay alerts only for senders this device allows, in
  // channels it has not muted, unless this page reports it presented the
  // messages first.

  // notifyState is this device's notification choices. Each change bumps
  // rev; synced is the rev the relay last confirmed, so a change made while
  // the relay could not be told (turning off offline, too) stays pending and
  // is sent when the page reconnects. A device never turned on writes nothing.
  async notifyState() {
    return { enabled: false, allowed: [], mutes: [], rev: 0, synced: 0, ...((await this.store.get("kv", "notify")) || {}) };
  }

  // changeNotify stores a change as pending, then tries to tell the relay;
  // false: not yet (it is sent again on reconnect).
  async changeNotify(fn) {
    const st = await this.notifyState();
    fn(st);
    st.rev++;
    await put(this.store, "kv", "notify", st);
    this.changed();
    try {
      await this.syncNotify();
      return true;
    } catch (e) {
      return false;
    }
  }

  // notifyInfo is what the relay offers (null when it sends no Web Push).
  async notifyInfo() {
    if (!(await this.features()).includes("notify1")) return null;
    if (!this.notifyOffer) this.notifyOffer = await this.call("GET", "/v1/notify", undefined, { signed: false });
    return this.notifyOffer.push_key ? this.notifyOffer : null;
  }

  // notifySenders are the exact keys this device's person decided on: the
  // people they started a DM with, and those they allowed by hand. A key
  // that changed since is not included; receiving a message adds nobody.
  async notifySenders(st) {
    const convs = await this.store.all("convs");
    const out = [];
    for (const p of await this.store.all("persons")) {
      const decided = st.allowed.includes(p.person) || convs.some((c) => c.peer === p.person && c.creator === this.address);
      if (!decided || p.state === "conflict") continue;
      for (const d of p.devices) { // each of their devices, by its unchanged key
        const pin = await this.store.get("pins", d.address);
        if (pin && !pin.pending && pin.fingerprint === d.fingerprint) out.push({ address: d.address, fingerprint: d.fingerprint });
      }
    }
    return out;
  }

  // syncNotify sends this device's preferences to the relay; what it sent
  // is confirmed (a change made meanwhile stays pending).
  async syncNotify() {
    const st = await this.notifyState();
    if (!(await this.notifyInfo())) return;
    const mutes = [];
    for (const conv of st.mutes) mutes.push(await wire.notifyChannel(conv, this.fp));
    await this.call("PUT", "/v1/notify/prefs", { enabled: st.enabled, senders: await this.notifySenders(st), mutes });
    const now = await this.notifyState();
    if (now.synced < st.rev) {
      await put(this.store, "kv", "notify", { ...now, synced: st.rev });
      this.changed();
    }
  }

  // enableNotify subscribes this browser (the page asked for permission
  // first, on the person's click) and turns alerts on.
  async enableNotify() {
    const info = await this.notifyInfo();
    if (!info) throw new Error("Your server does not send notifications.");
    if (!this.push || !this.push.supported()) throw new Error("This browser cannot show notifications for AgentNet here.");
    const sub = await this.push.subscribe(info.push_key);
    await this.call("PUT", "/v1/notify/subscription", sub);
    const told = await this.changeNotify((st) => { st.enabled = true; });
    return { note: told ? "Notifications are on." : "Notifications are on here; your server is told when this page reconnects." };
  }

  async disableNotify() {
    const told = await this.changeNotify((st) => { st.enabled = false; });
    return { note: told ? "Notifications are off." : "Notifications are off here; your server is told when this page reconnects." };
  }

  // reconcileNotify runs when the page starts and connects: a browser may
  // have dropped or replaced the subscription meanwhile.
  async reconcileNotify() {
    const st = await this.notifyState();
    const info = await this.notifyInfo();
    if (!info) return;
    if (st.enabled && this.push && this.push.supported()) {
      const sub = await this.push.current(info.push_key);
      if (sub) await this.call("PUT", "/v1/notify/subscription", sub);
    }
    // On: the senders may have changed. A pending change, off included, is
    // sent. Never turned on: nothing.
    if (st.enabled || st.synced < st.rev) await this.syncNotify();
  }

  async muteDM(conv, muted) {
    if (!(await this.store.get("convs", conv))) throw new Error("No conversation " + conv + " here.");
    await this.changeNotify((st) => { st.mutes = st.mutes.filter((c) => c !== conv).concat(muted ? [conv] : []); });
    return { note: muted ? "This DM is muted." : "This DM notifies you again." };
  }

  async allowSender(person, allowed) {
    if (!(await this.store.get("persons", person))) throw new Error("That person is not known here.");
    await this.changeNotify((st) => { st.allowed = st.allowed.filter((p) => p !== person).concat(allowed ? [person] : []); });
    return { note: allowed ? "Alerts from them are on." : "Alerts from them are off." };
  }

  // notifySeen reports messages this page presented (visible, focused,
  // that DM, newest in view): the relay then does not alert for them. It
  // is not a read receipt; nobody else learns it.
  async notifySeen(conv, ids) {
    const st = await this.notifyState();
    if (!st.enabled || !(await this.notifyInfo()) || !(await this.store.get("convs", conv))) return {};
    const shown = (ids || []).filter((id) => wire.validID(id)).slice(-32);
    if (!shown.length) return {};
    await this.call("POST", "/v1/notify/seen", { channel: await wire.notifyChannel(conv, this.fp), ids: shown });
    return {};
  }

  // resolveChannel finds the conversation a notification names, here and
  // only here ("" when it is not on this device).
  async resolveChannel(chan) {
    if (!wire.validChannel(chan)) return "";
    for (const c of await this.store.all("convs")) if ((await wire.notifyChannel(c.id, this.fp)) === chan) return c.id;
    return "";
  }

  // notifyView is what the page shows about notifications.
  async notifyView() {
    const st = await this.notifyState();
    let info = null;
    try { info = await this.notifyInfo(); } catch (e) { /* unknown now */ }
    const supported = !!(this.push && this.push.supported());
    return { available: !!info && supported, enabled: st.enabled, pending: st.synced < st.rev,
      reason: !info ? "Your server does not send notifications." : !supported ? "This browser cannot show notifications for AgentNet here." : "",
      mutes: st.mutes, allowed: (await this.notifySenders(st)).map((s) => s.address) };
  }

  // openFile fetches a received file's ciphertext, and returns it decrypted
  // only if both match the signed manifest: its safe name, the image type
  // its bytes show (never SVG or HTML), and the bytes. Files this device
  // sent are not kept after sending.
  async openFile(id, i) {
    const m = await this.store.get("inbox", id);
    if (!m) throw new Error((await this.store.get("outbox", id)) ? "Files you sent are not kept here after sending." : "No such message here.");
    const att = (m.attachments || [])[i];
    if (!att) throw new Error("That message has no such file.");
    const bytes = wire.decryptFile(await this.getBytes("/v1/blobs/" + att.blob.id + "/data"), att, this.keys);
    const plain = await bytes;
    return { name: wire.safeName(att.name), size: att.size, image: wire.sniffImage(plain), bytes: plain };
  }

  // eventText says what an agent participation record in a DM does, as
  // the laptop's page does. This browser only shows it: it never invites,
  // hosts or runs an agent.
  eventText(body, peer) {
    let e;
    try { e = wire.parseEvent(body); } catch (err) { return "A record about an agent that cannot be read here."; }
    const me = this.me && this.me.person;
    const who = (id) => (id && id === me ? "You" : peer && id === peer.person ? peer.label : "Someone not in this DM");
    const whose = (id) => (id && id === me ? "your" : peer && id === peer.person ? peer.label + "'s" : "an unknown person's");
    switch (e.type) {
    case "invite": return who(e.author.person) + " invited " + (e.host ? whose(e.host.person) + " agent (on " + e.host.address + ")" : "an agent") + " into this DM.";
    case "accept": return who(e.author.person) + " accepted: the agent joins this DM.";
    case "decline": return who(e.author.person) + " declined the invitation for the agent.";
    case "dismiss": return who(e.author.person) + " dismissed the agent: it gets nothing more from this DM.";
    }
    return "A record about an agent (" + e.type + ").";
  }

  async thread(id) {
    const m = await this.store.get("inbox", id);
    if (!m || m.v !== 1) throw new Error("No message with that id.");
    const pin = await this.store.get("pins", m.from);
    return { id, peer: m.from, key: { pinned: pin ? pin.fingerprint : "", pending: pin && pin.pending ? pin.pending.fingerprint : "" }, approved: false, task_grant: "",
      messages: [{ id, dir: "in", from: m.from, to: this.address, kind: m.kind, body: m.body, at: iso(m.at), unread: !m.read, files: [], actions: [],
        author: { label: m.from, about: "Signed with " + m.from + "'s key. Whether a person or one of their agents wrote it is not recorded." },
        state_text: m.state === "held" ? "Held for you: nothing runs in this browser" : "" }] };
  }

  async markRead(ids) {
    const ops = [];
    for (const id of ids || []) {
      const m = await this.store.get("inbox", id);
      if (m && !m.read) ops.push({ s: "inbox", k: id, v: { ...m, read: true } });
    }
    if (ops.length) {
      await this.store.write(ops);
      this.changed();
    }
  }

  // refreshDM asks once, when a DM is opened, about messages the server
  // still holds, and says what is known about the person's computer.
  async refreshDM(id) {
    const out = (await this.store.all("outbox")).filter((m) => m.conv === id && m.state === "custody").slice(-20);
    for (const m of out) {
      try {
        const r = await this.call("GET", "/v1/messages/" + m.id);
        if (r && r.state) await this.setOutState(m.id, r.state);
      } catch (e) { /* unknown stays unknown */ }
    }
    return { text: "" };
  }

  // api answers the page's requests as the daemon's page API does.
  async api(path, body) {
    if (this.revoked && body !== undefined && !path.startsWith("/api/act")) {
      throw new Error("This device was removed from its server: nothing more is sent or received here.");
    }
    const u = new URL(path, "http://page");
    switch (u.pathname) {
    case "/api/overview": return this.overview();
    case "/api/dm": return this.dm(u.searchParams.get("id"));
    case "/api/thread": return this.thread(u.searchParams.get("id"));
    case "/api/dm/new": return { id: await this.newDM(body.address) };
    case "/api/dm/send": return this.sendDM(body);
    case "/api/dm/agent/invite": return this.inviteAgent(body);
    case "/api/file": return this.openFile(u.searchParams.get("id"), Number(u.searchParams.get("i")));
    case "/api/notify/enable": return this.enableNotify();
    case "/api/notify/disable": return this.disableNotify();
    case "/api/notify/mute": return this.muteDM(body.conv, !!body.muted);
    case "/api/notify/allow": return this.allowSender(body.person, !!body.allowed);
    case "/api/notify/seen": return this.notifySeen(body.conv, body.ids);
    case "/api/notify/resolve": return { conv: await this.resolveChannel(u.searchParams.get("chan") || "") };
    case "/api/dm/agent/dismiss": return this.dismissAgent(body.pid);
    case "/api/dm/agent/ask": return this.askAgent(body);
    case "/api/dm/agent/decide": throw new Error("This browser runs no agent: an agent is accepted on the computer that runs it.");
    case "/api/person": return this.createPerson(body.label);
    case "/api/refresh": return (await this.store.get("convs", body.id)) ? this.refreshDM(body.id) : { text: "Connection unknown" };
    case "/api/act":
      if (body.do === "read") { await this.markRead(body.ids); return { note: "" }; }
      throw new Error("Nothing runs in this browser: accept, approve and grants are made on a computer with AgentNet.");
    case "/api/send": throw new Error("This browser sends DMs only: start a DM with a person.");
    case "/api/simulate": throw new Error("Not available here.");
    }
    throw new Error("Unknown request.");
  }
}
