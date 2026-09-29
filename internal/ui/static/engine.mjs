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

  personView(p, extra) {
    return p && { person: p.person, label: p.label, address: p.address, fingerprint: p.fingerprint, state: p.state, ...extra };
  }

  async createPerson(label) {
    if (this.me) throw new Error("This device already speaks for \"" + this.me.label + "\"; a second person is not created.");
    const r = await wire.newRoster(this.keys, this.address, String(label || "").trim());
    const me = { person: r.person, label: r.label, address: this.address, fingerprint: r.devices[0].fingerprint,
      hash: await wire.rosterHash(r), json: wire.rosterJSON(r), state: "self", published: false };
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
    if (!(await this.features()).includes("person")) throw new Error("your server does not hold persons (it needs an update)");
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

  async personByAddress(address) {
    return (await this.store.all("persons")).find((p) => p.address === address);
  }

  async profile(address) {
    const [label, agent] = address.split("/");
    return this.call("GET", "/v1/agents/" + label + "/" + agent + "/profile");
  }

  // personOf returns the person the device at address speaks for, pinning
  // its published record (verified against the device's pinned key) the
  // first time. A conflict is frozen, never replaced.
  async personOf(address, pin) {
    const known = await this.personByAddress(address);
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
    const pub = await this.pubOf(pin);
    let r;
    try {
      r = wire.parseRoster(prof.person);
      await wire.verifyRoster(r, pub.sign_key);
      if (r.devices[0].address !== address || r.devices[0].fingerprint !== pin.fingerprint) throw new Error("the record does not name this device's key");
    } catch (e) {
      throw new Hold("proof_pending", address + "'s person record: " + e.message);
    }
    return this.pinPerson(r);
  }

  async pinPerson(r) {
    const hash = await wire.rosterHash(r);
    const d = r.devices[0];
    const byID = await this.store.get("persons", r.person);
    const byAddr = await this.personByAddress(d.address);
    const other = byID || byAddr;
    if (other) {
      if (other.hash === hash) return other;
      await put(this.store, "persons", other.person, { ...other, state: "conflict" });
      this.changed();
      throw new Hold("identity_conflict", "a different person record was seen for " + d.address + "; it is frozen");
    }
    const p = { person: r.person, label: r.label, address: d.address, fingerprint: d.fingerprint, hash, json: wire.rosterJSON(r), state: "pinned" };
    await put(this.store, "persons", r.person, p);
    return p;
  }

  // observePerson compares a record the server lists for a pinned person's
  // device with the pinned one: only a verifying, different record counts,
  // and it freezes the person.
  async observePerson(address, raw) {
    if (!raw) return;
    const p = await this.personByAddress(address);
    if (!p || p.state !== "pinned") return;
    const pin = await this.store.get("pins", address);
    if (!pin || pin.fingerprint !== p.fingerprint) return;
    try {
      const r = wire.parseRoster(raw);
      await wire.verifyRoster(r, (await this.pubOf(pin)).sign_key);
      if (r.devices[0].address !== address || r.devices[0].fingerprint !== p.fingerprint) return;
      if ((await wire.rosterHash(r)) === p.hash) return;
    } catch (e) {
      return; // no proof of anything
    }
    await put(this.store, "persons", p.person, { ...p, state: "conflict" });
    this.changed();
  }

  async supports(address, pin) {
    const f = await this.features();
    if (!f.includes("env2") || !f.includes("caps")) return [false, "your server cannot carry conversations (it needs an update)"];
    const prof = await this.profile(address);
    await this.observePerson(address, prof && prof.person);
    const key = (await this.pubOf(pin)).sign_key;
    if (!(await wire.profileSupports(prof || {}, address, key, wire.CapEnv2))) {
      return [false, address + "'s AgentNet cannot read conversations now (an older program, or it has not connected since updating)"];
    }
    // The third answer: whether it reads the attention hint (every session
    // of it says so, and the relay takes it).
    return [true, "", f.includes("notify1") && (await wire.profileSupports(prof, address, key, "notify1"))];
  }

  // ---- DMs

  async newDM(address) {
    if (!this.me) throw new Error("Set up your person first.");
    const f = await this.features();
    if (!["env2", "person", "caps"].every((x) => f.includes(x))) throw new Error("Your server cannot carry conversations (it needs an update).");
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
    if (!now || now.state !== "pinned" || now.hash !== them.hash) throw new Error("This person's record conflicts with the one kept here; it is frozen.");
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
  async sendDM({ conv, body, reply_to: replyTo }) {
    body = String(body || "").trim();
    if (!body) throw new Error("Write a message first.");
    const c = await this.store.get("convs", conv);
    if (!c) throw new Error("No conversation " + conv + " here.");
    if (replyTo) {
      const m = (await this.store.get("inbox", replyTo)) || (await this.store.get("outbox", replyTo));
      if (!m || m.conv !== conv) throw new Error("A reply stays within its conversation.");
    }
    return this.sendConv(c, { kind: "message", body, reply_to: replyTo || "", origin: "ui" });
  }

  // sendConv stores the sealed envelope before it is sent, and sends that
  // exact envelope on every retry: a message, a participation record (sub
  // "event") or a request to another device's agent (pid and target).
  async sendConv(c, { kind, body, reply_to: replyTo = "", origin = "", sub = "", pid = "", target = null }) {
    const { why: stop, peer, pin } = await this.gate(c);
    if (stop) throw new Error(stop);
    let ok = false, why = "", notify = false;
    try {
      [ok, why, notify] = await this.supports(peer.address, pin);
    } catch (e) {
      if (this.revoked) throw new Error("This device was removed from its server: nothing more is sent or received here.");
      why = "cannot reach your server";
    }
    const again = (await this.gate(c)).why; // the profile just read may have frozen the person
    if (again) throw new Error(again);
    const id = wire.newID(), lid = wire.newID();
    const at = this.now();
    // A turn a person typed asks for the recipient's attention, on this DM's
    // channel for them, when their device and the relay read the hint.
    const attention = notify && origin === "ui" && sub === "" && ["message", "question", "task"].includes(kind);
    const chan = attention ? await wire.notifyChannel(c.id, pin.fingerprint) : "";
    const envelope = await wire.seal({ v: wire.Version2, id, from: this.address, to: peer.address, ts: Math.floor(at / 1000), kind,
      body, reply_to: replyTo, conv: c.id, lid, root: c.root, origin, sub, pid, target, chan }, this.keys, await this.pubOf(pin));
    const rec = { id, conv: c.id, lid, body, reply_to: replyTo, kind, origin, sub, pid, target, at, to: peer.address, envelope,
      state: ok ? "queued" : "waiting", detail: why };
    await put(this.store, "outbox", id, rec);
    this.changed();
    if (ok) await this.post(rec);
    return { id, state: rec.state, detail: rec.detail };
  }

  async post(rec) {
    try {
      const r = await this.call("POST", "/v1/messages", rec.envelope);
      rec.state = (r && r.state) || "custody";
      rec.detail = "";
    } catch (e) {
      if (retryable(e)) {
        rec.detail = e.message;
      } else {
        rec.state = "failed";
        rec.detail = e.code === "recipient_revoked" ? "that device was removed from the server" : e.message;
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
  // may), with the other member's person and pin: their person must be
  // kept and not in conflict, and the key pinned for their device must be
  // the one that person names, with no change waiting. A new message and
  // every kept one pass it before they are sent.
  async gate(c) {
    const peer = c && (await this.store.get("persons", c.peer));
    if (!peer) return { why: "The other member's person record is not kept here." };
    if (peer.state === "conflict") return { why: "This person's record conflicts with the one kept here; the conversation is frozen.", peer };
    const pin = await this.pinned(peer.address);
    if (pin.pending || pin.fingerprint !== peer.fingerprint) return { why: peer.address + "'s key is no longer the one its person record names; the conversation is frozen.", peer, pin };
    return { why: "", peer, pin };
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
      try { g = await this.gate(c); } catch (e) { continue; }
      if (!g.why && rec.state === "waiting") {
        let ok = false;
        try { [ok] = await this.supports(g.peer.address, g.pin); } catch (e) { continue; }
        if (!ok) continue;
        g = await this.gate(c);
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
    const base = { id: env.id, from: env.from, kind: n.kind, body: n.body, reply_to: n.reply_to, at: this.now(), fp: pin.fingerprint, read: false };
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
    if (wire.rootMember(root, this.me.person) !== this.me.hash) throw new Hold("invalid", "this device's person is not a member");
    let conv = await this.store.get("convs", n.conv);
    const ops = [];
    if (!conv) {
      if (env.from !== root.creator.address || pin.fingerprint !== root.creator.fingerprint) {
        throw new Hold("proof_pending", "only its creator's device can introduce a conversation");
      }
      try {
        await wire.verifyRoot(root, (await this.pubOf(pin)).sign_key);
      } catch (e) {
        throw new Hold("invalid", e.message);
      }
      const creator = await this.personOf(env.from, pin);
      if (creator.person !== root.creator.person || creator.hash !== root.creator.roster) {
        throw new Hold("identity_conflict", "the creator's person record differs from the one its root names");
      }
      conv = { id: n.conv, root: wire.rootJSON(root), peer: creator.person, created: root.created, creator: env.from };
      ops.push({ s: "convs", k: n.conv, v: conv });
    }
    const sp = await this.personOf(env.from, pin);
    if (sp.fingerprint !== pin.fingerprint) throw new Hold("identity_conflict", "the sender's key is not the one its person record names");
    if (wire.rootMember(root, sp.person) !== sp.hash) throw new Hold("invalid", "the sender is not a member of this conversation");
    if (n.reply_to) {
      const m = (await this.store.get("inbox", n.reply_to)) || (await this.store.get("outbox", n.reply_to));
      if (m && m.conv !== n.conv) throw new Hold("invalid", "it replies to a message outside its conversation");
    }
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
      [n.kind, n.body, n.reply_to, n.conv, n.sub, n.origin, n.emotion, n.target, n.pid, n.replica, n.status]))));
    const key = pin.fingerprint + "/" + n.lid;
    const seen = await this.store.get("lids", key);
    if (seen) {
      if (seen.hash !== hash) throw new Hold("conflicting_duplicate", "a message with the same key and logical id but other content is stored");
      return ops; // the same message again: acknowledged, not stored twice
    }
    // A request to another device's agent is history here (as the core
    // keeps it); any other question or task is held for the person. This
    // browser never runs anything.
    const request = n.kind === "question" || n.kind === "task";
    const rec = { ...base, v: 2, conv: n.conv, lid: n.lid, sub: n.sub, pid: n.pid, origin: n.origin, emotion: n.emotion, replica: n.replica,
      target: n.target || null, state: request && !(n.target && n.target.address !== this.address) ? "conv_held" : "" };
    // The attention hint is the sender's claim: recorded, checked against
    // this DM's channel here, never used to route anything.
    if (env.attn) rec.attn = env.chan === (await wire.notifyChannel(n.conv, this.fp)) ? "ok" : "mismatch";
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
      if (f.includes("caps")) await this.call("PUT", "/v1/caps", wire.capsJSON(await wire.newCaps(this.keys, this.address, this.session, [wire.CapEnv2, "notify1"])));
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
      for (const x of this.members.list) if (x.person) await this.observePerson(x.address, x.person);
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

  // ---- what the page reads (the daemon page API's shapes)

  async overview() {
    const persons = await this.store.all("persons");
    const people = persons.map((p) => this.personView(p));
    const byAddr = new Set([this.address, ...persons.map((p) => p.address)]);
    for (const m of this.members.list) {
      if (!m.person || byAddr.has(m.address)) continue;
      try {
        const r = wire.parseRoster(m.person);
        if (r.devices[0].address === m.address) people.push({ label: r.label, address: m.address, state: "listed" });
      } catch (e) { /* not shown */ }
    }
    const inbox = await this.store.all("inbox");
    const outbox = await this.store.all("outbox");
    const dms = [], links = new Map(); // person → the agents their device runs in DMs here
    for (const c of await this.store.all("convs")) {
      const peer = persons.find((p) => p.person === c.peer);
      const msgs = [...inbox.filter((m) => m.conv === c.id), ...outbox.filter((m) => m.conv === c.id)].sort((a, b) => a.at - b.at);
      for (const info of await this.agentsOf(c)) {
        if (!info.host) continue;
        const ls = links.get(info.host.person) || [];
        let l = ls.find((x) => x.address === info.host.address);
        if (!l) ls.push(l = { address: info.host.address, dms: [] });
        l.dms.push({ conv: c.id, pid: info.pid, state: info.state });
        links.set(info.host.person, ls);
      }
      const line = (m) => (m.sub === "event" ? this.eventText(m.body, peer) : firstLine(m.body));
      dms.push({ id: c.id, peer: this.personView(peer), created: iso(c.created * 1000), mine: c.creator === this.address, count: msgs.length,
        title: msgs[0] ? line(msgs[0]) : "", last: msgs.length ? line(msgs[msgs.length - 1]) : "",
        last_at: iso(msgs.length ? msgs[msgs.length - 1].at : c.created * 1000),
        unread: msgs.filter((m) => m.fp && !m.read).length, held: msgs.filter((m) => m.state === "conv_held").length,
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
      persons: true, agents: true, // agents on the other person's computer: invited, asked and dismissed here, never run here
      person: this.personView(this.me, this.me ? { published: !!this.me.published } : undefined) || undefined, people, dms,
    };
  }

  async dm(id) {
    const c = await this.store.get("convs", id);
    if (!c) throw new Error("No conversation with that id.");
    const peer = await this.store.get("persons", c.peer);
    const msgs = [...(await this.store.all("inbox")).filter((m) => m.conv === id), ...(await this.store.all("outbox")).filter((m) => m.conv === id)]
      .sort((a, b) => a.at - b.at);
    return { id, peer: this.personView(peer), created: iso(c.created * 1000), mine: c.creator === this.address,
      frozen: peer && peer.state === "conflict" ? peer.address + " published a different person record than the one kept here, so this conversation is frozen: nothing more is sent in it." : "",
      agents: (await this.agentsOf(c)).map((info) => this.agentView(info, msgs, peer)),
      messages: msgs.map((m) => {
        const out = !m.fp;
        const event = m.sub === "event" ? this.eventText(m.body, peer) : "";
        return { id: m.id, dir: out ? "out" : "in", from: out ? this.address : m.from, kind: m.kind, body: event ? "" : m.body, reply_to: m.reply_to || "",
          origin: m.origin || "", state: m.state, detail: m.detail || "", at: iso(m.at), unread: !out && !m.read, replica: !!m.replica,
          pid: m.pid || "", to: m.target ? m.target.address : "", event,
          state_text: event ? "" : out ? outText(m.state, peer ? peer.address : "", m.detail) : m.state === "conv_held" ? "Held for you: nothing runs it. Answer here if you want to." : "" };
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
      if (p && p.state !== "conflict" && wire.rootMember(root, p.person) === p.hash) out.set(p.person, p);
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
    const author = (a) => { const p = m.get(a.person); return p && p.hash === a.roster && p.address === a.address && p.fingerprint === a.fingerprint ? p : null; };
    const host = (h) => { const p = h && m.get(h.person); return p && p.address === h.address && p.fingerprint === h.fingerprint ? p : null; };
    const memberKey = (fp) => [...m.values()].some((p) => p.fingerprint === fp);
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
      note: info.note, shared, missing, tasks_from: info.taskKeys.map((fp) => this.personView(people.find((p) => p.fingerprint === fp))).filter(Boolean),
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
    const hp = [...m.values()].find((p) => p.address === host);
    if (!hp) throw new Error(host + " is not the device of a member of that conversation (or its person is frozen).");
    const msgs = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].filter((x) => x.conv === conv);
    const grant = share.map((id) => {
      const x = msgs.find((y) => y.id === id && !y.sub && !y.replica);
      if (!x || !x.lid) throw new Error("A message chosen to share is not an earlier message of this DM that can be shared.");
      return { lid: x.lid, fingerprint: x.fp || this.fp };
    });
    for (const fp of tasks) if (![...m.values()].some((p) => p.fingerprint === fp)) throw new Error(fp + " is not the key of a member of that conversation.");
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
      const pin = await this.store.get("pins", p.address);
      if (pin && !pin.pending && pin.fingerprint === p.fingerprint) out.push({ address: p.address, fingerprint: p.fingerprint });
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
