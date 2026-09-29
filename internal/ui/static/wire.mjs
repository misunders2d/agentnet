// The AgentNet wire format for a browser device: the same signed requests,
// joins, session ads, directory entries and envelopes as the Go client
// (internal/protocol, internal/identity, internal/envelope), with WebCrypto
// keys that cannot be exported and age (vendor/age.mjs, the official
// TypeScript implementation) for encryption. Everything signed is built
// byte for byte as Go's encoding/json writes it; wire_test.go checks this
// module against the Go code in both directions.

import { Encrypter, Decrypter, identityToRecipient } from "./vendor/age.mjs";

const subtle = globalThis.crypto.subtle;
const utf8 = new TextEncoder();
const fromUTF8 = new TextDecoder("utf-8", { fatal: true });

export const Version = 1;                // envelope.Version
export const Version2 = 2;               // envelope.Version2: conversation messages
export const MaxCiphertext = 256 << 10;  // envelope.MaxCiphertext
export const MaxAttachments = 8;         // envelope.MaxAttachments
export const MaxBody = 1 << 20;          // protocol.MaxBody
const kinds = new Set(["message", "question", "answer", "task", "result"]);
const invitePrefix = "agentnet-invite-v1:";

// ---- names and ids ----------------------------------------------------------

const namePattern = /^[a-z][a-z0-9-]{0,31}$/;
const idPattern = /^[0-9a-f]{32}$/;
const sha256Pattern = /^[0-9a-f]{64}$/;
// An age X25519 recipient: "age1" and 58 lowercase bech32 characters.
const recipientPattern = /^age1[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{58}$/;

export const validName = (s) => typeof s === "string" && namePattern.test(s);
export const validID = (s) => typeof s === "string" && idPattern.test(s);

// validAddress is protocol.SplitAddress: "label/agent", both valid names.
export function validAddress(a) {
  if (typeof a !== "string") return false;
  const i = a.indexOf("/");
  return i > 0 && validName(a.slice(0, i)) && validName(a.slice(i + 1));
}

export function newID() {
  return hex(globalThis.crypto.getRandomValues(new Uint8Array(16)));
}

// ---- bytes --------------------------------------------------------------------

export const hex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");

export function b64(bytes) {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

// unb64 accepts only what Go's encoding/json writes for []byte: standard,
// padded base64 with nothing else.
export function unb64(s, what) {
  let bytes;
  try {
    const bin = atob(s);
    bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  } catch (e) {
    throw new Error(what + ": not base64");
  }
  if (b64(bytes) !== s) throw new Error(what + ": not canonical base64");
  return bytes;
}

const unb64url = (s) => atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
const b64url = (bytes) => b64(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

function concat(...parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let o = 0;
  for (const p of parts) { out.set(p, o); o += p.length; }
  return out;
}

export async function sha256(bytes) {
  return new Uint8Array(await subtle.digest("SHA-256", bytes));
}

// ---- Go's encoding/json --------------------------------------------------------

// wellFormed reports whether s has no lone surrogates: such a string has no
// UTF-8 form, so it is refused rather than silently changed.
function wellFormed(s) {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      const d = s.charCodeAt(i + 1);
      if (!(d >= 0xdc00 && d <= 0xdfff)) return false;
      i++;
    } else if (c >= 0xdc00 && c <= 0xdfff) return false;
  }
  return true;
}

function text(s, what) {
  if (typeof s !== "string" || !wellFormed(s)) throw new Error(what + " is not valid text");
  return s;
}

// goString is a JSON string exactly as Go's json.Marshal writes it: HTML
// characters, U+2028 and U+2029 escaped, the short escapes Go uses.
export function goString(s) {
  text(s, "text");
  let out = '"';
  for (const ch of s) {
    const c = ch.codePointAt(0);
    if (c < 0x80) {
      if (c >= 0x20 && c !== 0x22 && c !== 0x5c && c !== 0x3c && c !== 0x3e && c !== 0x26) out += ch;
      else if (c === 0x22 || c === 0x5c) out += "\\" + ch;
      else if (c === 0x08) out += "\\b";
      else if (c === 0x0c) out += "\\f";
      else if (c === 0x0a) out += "\\n";
      else if (c === 0x0d) out += "\\r";
      else if (c === 0x09) out += "\\t";
      else out += "\\u00" + c.toString(16).padStart(2, "0");
    } else if (c === 0x2028 || c === 0x2029) out += "\\u" + c.toString(16);
    else out += ch;
  }
  return out + '"';
}

const goBytes = (b) => (b ? '"' + b64(b) + '"' : "null");

function goInt(n, what) {
  if (!Number.isSafeInteger(n)) throw new Error(what + " is not a whole number");
  return String(n);
}

// ---- keys ---------------------------------------------------------------------

// newKeys makes this device's keys: Ed25519 to sign and X25519 to decrypt,
// neither of which can be exported. They are kept as CryptoKey objects
// (IndexedDB stores them as they are) and are lost with the browser's data.
export async function newKeys() {
  const sign = await subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]);
  const box = await subtle.generateKey({ name: "X25519" }, false, ["deriveBits"]);
  return { sign: sign.privateKey, signPublic: sign.publicKey, box: box.privateKey };
}

async function signBytes(keys, msg) {
  return new Uint8Array(await subtle.sign("Ed25519", keys.sign, msg));
}

async function verifyBytes(signKey, msg, sig) {
  if (!(sig instanceof Uint8Array) || sig.length !== 64) return false;
  const key = await subtle.importKey("raw", signKey, { name: "Ed25519" }, false, ["verify"]);
  return subtle.verify("Ed25519", key, sig, msg);
}

// support reports what this browser lacks for a device, if anything.
export async function support() {
  const missing = [];
  try {
    const k = await newKeys();
    await signBytes(k, new Uint8Array(1));
    await identityToRecipient(k.box);
  } catch (e) {
    missing.push("Ed25519 and X25519 keys in WebCrypto");
  }
  return missing;
}

// ---- directory entries (identity.Public) -------------------------------------------

const bindingMessage = (address, recipient) => utf8.encode("agentnet-box-binding-v1\n" + address + "\n" + recipient);

// publicEntry is this device's directory entry for address.
export async function publicEntry(keys, address) {
  if (!validAddress(address)) throw new Error("invalid address " + address);
  const signKey = new Uint8Array(await subtle.exportKey("raw", keys.signPublic));
  const recipient = await identityToRecipient(keys.box);
  return { address, sign_key: signKey, box_recipient: recipient, box_sig: await signBytes(keys, bindingMessage(address, recipient)) };
}

export const marshalPublic = (p) => '{"address":' + goString(p.address) + ',"sign_key":' + goBytes(p.sign_key) +
  ',"box_recipient":' + goString(p.box_recipient) + ',"box_sig":' + goBytes(p.box_sig) + "}";

// parsePublic reads a directory entry as the Hub sends it, and checks it as
// identity.Public.Verify does: the encryption key is signed by the signing
// key. Whether it is the key trusted before is the caller's question.
export async function parsePublic(v) {
  const out = parsePublicShape(v);
  await checkPublic(out);
  return out;
}

// parsePublicShape reads a directory entry's fields (checkPublic checks it).
export function parsePublicShape(v) {
  const p = strict(v, "directory entry", { address: "string", sign_key: "string", box_recipient: "string", box_sig: "string" });
  return { address: p.address || "", sign_key: unb64(p.sign_key || "", "signing key"),
    box_recipient: p.box_recipient || "", box_sig: unb64(p.box_sig || "", "key signature") };
}

// checkPublic is identity.Public.Verify: the keys' shapes, and the
// encryption key signed by the signing key for this address.
export async function checkPublic(p) {
  if (!(p.sign_key instanceof Uint8Array) || p.sign_key.length !== 32) throw new Error("bad signing key");
  if (!recipientPattern.test(p.box_recipient)) throw new Error("bad encryption key");
  try { new Encrypter().addRecipient(p.box_recipient); } catch (e) { throw new Error("bad encryption key"); }
  if (!(await verifyBytes(p.sign_key, bindingMessage(p.address, p.box_recipient), p.box_sig))) {
    throw new Error("encryption key not signed by signing key");
  }
}

// fingerprint is identity.Public.Fingerprint: both keys, shortened for people
// to compare.
export async function fingerprint(p) {
  const h = hex((await sha256(concat(p.sign_key, utf8.encode(p.box_recipient)))).subarray(0, 16));
  return [h.slice(0, 8), h.slice(8, 16), h.slice(16, 24), h.slice(24, 32)].join("-");
}

// ---- invites, joins, requests, session ads -----------------------------------------

// decodeInvite is protocol.DecodeInvite.
export function decodeInvite(code) {
  const s = String(code).trim();
  if (!s.startsWith(invitePrefix)) throw new Error("not an AgentNet invite code");
  let v;
  try {
    v = JSON.parse(fromUTF8.decode(Uint8Array.from(unb64url(s.slice(invitePrefix.length)), (c) => c.charCodeAt(0))));
  } catch (e) {
    throw new Error("damaged invite code");
  }
  const inv = { hub: v.hub, label: v.label, secret: v.secret, cert: v.cert || "" };
  if (typeof inv.secret !== "string" || inv.secret === "" || !validName(inv.label)) throw new Error("incomplete invite code");
  inv.hub = hubOrigin(inv.hub);
  return inv;
}

// hubOrigin is protocol.NormalizeHubURL: a bare https origin.
function hubOrigin(raw) {
  let u;
  try { u = new URL(String(raw).trim()); } catch (e) { throw new Error("invalid Hub URL"); }
  if (u.protocol !== "https:" || !u.hostname || u.username || u.password || u.search || u.hash ||
    (u.pathname !== "" && u.pathname !== "/")) {
    throw new Error("Hub URL must be a bare https origin");
  }
  return "https://" + u.host;
}

// joinRequest is the body of POST /v1/join: this device's entry, signed to
// prove it holds the key (protocol.SignJoin).
export async function joinRequest(keys, address, secret, link) {
  text(secret, "invite secret");
  const pub = await publicEntry(keys, address);
  const linkJSON = link ? ',"link":{"offer":' + goString(link.offer) + ',"join":' + goBytes(link.join) + ',"mac":' + goBytes(link.mac) + "}" : "";
  const signed = (sig) => '{"secret":' + goString(secret) + ',"public":' + marshalPublic(pub) + linkJSON + ',"sig":' + goBytes(sig) + "}";
  const sig = await signBytes(keys, utf8.encode("agentnet-join-v1\n" + signed(null)));
  return signed(sig);
}

// signRequest returns the headers that authenticate a Hub request as agent
// (protocol.SignRequest). target is the path and query exactly as sent.
export async function signRequest(keys, agent, method, target, body) {
  const data = typeof body === "string" ? utf8.encode(body) : body || new Uint8Array(0);
  const ts = Math.floor(Date.now() / 1000);
  const nonce = newID();
  const msg = "agentnet-request-v1\n" + agent + "\n" + method + "\n" + target + "\n" + ts + "\n" + nonce + "\n" + hex(await sha256(data));
  return {
    "X-Agentnet-Agent": agent,
    "X-Agentnet-Time": String(ts),
    "X-Agentnet-Nonce": nonce,
    "X-Agentnet-Sig": b64(await signBytes(keys, utf8.encode(msg))),
  };
}

// sessionAd is the signed announcement a push stream needs (?ad=), for a
// device that takes no direct deliveries (protocol.SessionAd).
export async function sessionAd(keys, address, session) {
  if (!validAddress(address) || !validID(session)) throw new Error("invalid session");
  const ad = (sig) => '{"address":' + goString(address) + ',"session":' + goString(session) + ',"sig":' + goBytes(sig) + "}";
  const sig = await signBytes(keys, utf8.encode("agentnet-session-v1\n" + ad(null)));
  return b64url(utf8.encode(ad(sig)));
}

// ---- envelopes ------------------------------------------------------------------

// strict checks that v is an object with only the named fields, each of the
// named type (null is taken as absent, as Go does).
function strict(v, what, fields) {
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new Error(what + " is not an object");
  const out = {};
  for (const [k, x] of Object.entries(v)) {
    const want = fields[k];
    if (!want) throw new Error(what + " has an unknown field " + k);
    if (x === null) continue;
    const ok = want === "array" ? Array.isArray(x) : want === "int" ? Number.isSafeInteger(x) : want === "object"
      ? typeof x === "object" && !Array.isArray(x) : typeof x === want;
    if (!ok) throw new Error(what + " field " + k + " has the wrong type");
    out[k] = x;
  }
  return out;
}

const blobJSON = (b) => '{"id":' + goString(b.id) + ',"size":' + goInt(b.size, "size") + ',"sha256":' + goString(b.sha256) + "}";

// marshalEnvelope is json.Marshal of an envelope.Envelope; without sig it is
// what the sender signs.
function marshalEnvelope(e, withSig) {
  let s = '{"v":' + goInt(e.v, "version") + ',"id":' + goString(e.id) + ',"from":' + goString(e.from) +
    ',"to":' + goString(e.to) + ',"ts":' + goInt(e.ts, "time") + ',"kind":' + goString(e.kind) + ',"ct":' + goBytes(e.ct);
  if (e.blobs.length) s += ',"blobs":[' + e.blobs.map(blobJSON).join(",") + "]";
  if (e.session) s += ',"session":' + goString(e.session);
  if (e.fallback) s += ',"fallback":true';
  if (e.attn) s += ',"attn":true';
  if (e.chan) s += ',"chan":' + goString(e.chan);
  if (withSig && e.sig && e.sig.length) s += ',"sig":' + goBytes(e.sig);
  return s + "}";
}

// A version 2 envelope is signed in its own domain, so neither version's
// signature passes for the other.
const envelopeSigned = (e) => utf8.encode((e.v === Version2 ? "agentnet-envelope-v2\n" : "agentnet-envelope-v1\n") +
  marshalEnvelope(e, false));

// marshalInner is json.Marshal of an envelope.Inner.
function marshalInner(n) {
  let s = '{"v":' + goInt(n.v, "version") + ',"id":' + goString(n.id) + ',"from":' + goString(n.from) +
    ',"to":' + goString(n.to) + ',"ts":' + goInt(n.ts, "time") + ',"kind":' + goString(n.kind) + ',"body":' + goString(n.body);
  if (n.reply_to) s += ',"reply_to":' + goString(n.reply_to);
  if (n.attachments.length) {
    s += ',"attachments":[' + n.attachments.map((a) => '{"blob":' + blobJSON(a.blob) + ',"name":' + goString(a.name) +
      ',"size":' + goInt(a.size, "size") + ',"sha256":' + goString(a.sha256) + "}").join(",") + "]";
  }
  if (n.session) s += ',"session":' + goString(n.session);
  if (n.fallback) s += ',"fallback":true';
  if (n.status) s += ',"status":' + goString(n.status);
  if (n.conv) s += ',"conv":' + goString(n.conv);
  if (n.lid) s += ',"lid":' + goString(n.lid);
  if (n.root) s += ',"root":' + n.root; // the signed root's JSON as it is
  if (n.sub) s += ',"sub":' + goString(n.sub);
  if (n.replica) s += ',"replica":true';
  if (n.origin) s += ',"origin":' + goString(n.origin);
  if (n.emotion) s += ',"emotion":' + goString(n.emotion);
  if (n.target) s += ',"target":{"address":' + goString(n.target.address) + ',"fingerprint":' + goString(n.target.fingerprint) + "}";
  if (n.pid) s += ',"pid":' + goString(n.pid);
  if (n.fan && n.fan.length) s += ',"fan":[' + n.fan.map((f) => '{"person":' + goString(f.person) + ',"roster":' + goString(f.roster) + "}").join(",") + "]";
  return s + "}";
}

const subs = new Set(["", "event", "excerpt", "history", "file"]);
const agentOrigin = (o) => typeof o === "string" && o.startsWith("agent:");

// checkV2 is envelope.checkVersion2: the conversation fields, only in
// version 2, and their shapes. (The pid rules follow the core as it is now;
// participation is still in review there.)
function checkV2(n) {
  if (n.v !== Version2) {
    if (n.conv || n.lid || n.root || n.sub || n.replica || n.origin || n.emotion || n.target || n.pid || n.fan) {
      throw new Error("conversation fields in a version 1 message");
    }
    return;
  }
  if (!validHash(n.conv) || !validID(n.lid)) throw new Error("invalid conversation or logical id");
  if (!n.root || utf8.encode(n.root).length > MaxConvRoot) throw new Error("missing or oversized conversation root");
  if (!subs.has(n.sub)) throw new Error("unknown sub " + n.sub);
  if (n.origin && n.origin !== "ui" && !(agentOrigin(n.origin) && validToken(n.origin.slice(6), 32))) throw new Error("invalid origin " + n.origin);
  if (n.emotion && !validToken(n.emotion, 24)) throw new Error("invalid emotion " + n.emotion);
  if (n.fan) {
    if (n.fan.length > 2) throw new Error("a conversation message names at most its two member persons");
    n.fan.forEach((f, i) => {
      if (!validID(f.person) || !validHash(f.roster) || (i > 0 && f.person === n.fan[0].person)) throw new Error("invalid fan");
    });
  }
  if (n.target) {
    if (n.kind !== "question" && n.kind !== "task") throw new Error("only a question or task has an execution target");
    if (!validAddress(n.target.address) || !validFingerprint(n.target.fingerprint)) throw new Error("invalid execution target");
  }
  if (n.pid) {
    if (!validID(n.pid)) throw new Error("invalid participation id");
    const request = n.sub === "" && (n.kind === "question" || n.kind === "task") && n.target;
    const output = n.sub === "" && (n.kind === "answer" || n.kind === "result");
    if (n.sub !== "event" && !request && !output) throw new Error("a participation id is not allowed on this message");
  }
}

function checkBlob(b) {
  if (!validID(b.id) || !Number.isSafeInteger(b.size) || b.size <= 0 || typeof b.sha256 !== "string" || b.sha256.length !== 64) {
    throw new Error("invalid attachment reference");
  }
}

// seal encrypts a message to the recipient's directory entry and signs it
// with this device's key (envelope.Seal). m has id, from, to, ts, kind, body
// and optionally reply_to, status, session, fallback and attachments (each
// {blob: {id, size, sha256}, name, size, sha256}); with v: Version2 also a
// conversation message's conv, lid, root (the signed root's JSON), sub,
// replica, origin, emotion and target. It returns the envelope as the JSON
// POST /v1/messages takes. The same stored JSON is sent again on a retry,
// never sealed again.
export async function seal(m, keys, recipient) {
  if (!kinds.has(m.kind)) throw new Error("unknown message kind " + m.kind);
  if (!validID(m.id) || !validAddress(m.from) || !validAddress(m.to)) throw new Error("invalid message header");
  if (recipient.address !== m.to) throw new Error("recipient key does not belong to " + m.to);
  if (m.reply_to && !validID(m.reply_to)) throw new Error("invalid reply id");
  if (m.session && !validID(m.session)) throw new Error("invalid session id");
  const attachments = m.attachments || [];
  if (attachments.length > MaxAttachments) throw new Error("too many attachments (max " + MaxAttachments + ")");
  for (const a of attachments) {
    checkBlob(a.blob);
    if (!Number.isSafeInteger(a.size) || a.size < 0 || !sha256Pattern.test(a.sha256)) throw new Error("invalid attachment");
    text(a.name, "attachment name");
  }
  const v = m.v === Version2 ? Version2 : Version;
  const inner = { v, id: m.id, from: m.from, to: m.to, ts: m.ts, kind: m.kind, body: text(m.body, "message"),
    reply_to: m.reply_to || "", attachments, session: m.session || "", fallback: !!m.fallback, status: text(m.status || "", "status"),
    conv: m.conv || "", lid: m.lid || "", root: m.root || "", sub: m.sub || "", replica: !!m.replica,
    origin: text(m.origin || "", "origin"), emotion: text(m.emotion || "", "emotion"), target: m.target || null, pid: m.pid || "",
    fan: m.fan && m.fan.length ? m.fan : null };
  checkV2(inner);
  if (v === Version2 && agentOrigin(inner.origin) && inner.sub === "" && !inner.emotion) throw new Error("an agent's turn must carry an emotion");
  const e = new Encrypter();
  e.addRecipient(recipient.box_recipient);
  const ct = await e.encrypt(utf8.encode(marshalInner(inner)));
  if (ct.length > MaxCiphertext) throw new Error("message too large (" + ct.length + " bytes encrypted, max " + MaxCiphertext + ")");
  // A turn that asks for the recipient's attention names its channel
  // (envelope.SealAttention); only version 2 carries it.
  const chan = m.chan || "";
  if (chan && (v !== Version2 || !validChannel(chan))) throw new Error("attention needs a version 2 message and a notification channel");
  const env = { v, id: m.id, from: m.from, to: m.to, ts: m.ts, kind: m.kind, ct,
    blobs: attachments.map((a) => a.blob), session: inner.session, fallback: inner.fallback, attn: !!chan, chan };
  env.sig = await signBytes(keys, envelopeSigned(env));
  return marshalEnvelope(env, true);
}

// parseEnvelope reads an envelope as the Hub sends or stores it, strictly.
export function parseEnvelope(json) {
  const v = strict(typeof json === "string" ? JSON.parse(json) : json, "envelope", { v: "int", id: "string", from: "string",
    to: "string", ts: "int", kind: "string", ct: "string", blobs: "array", session: "string", fallback: "boolean",
    attn: "boolean", chan: "string", sig: "string" });
  const blobs = (v.blobs || []).map((b) => strict(b, "attachment reference", { id: "string", size: "int", sha256: "string" }))
    .map((b) => ({ id: b.id || "", size: b.size || 0, sha256: b.sha256 || "" }));
  return { v: v.v || 0, id: v.id || "", from: v.from || "", to: v.to || "", ts: v.ts || 0, kind: v.kind || "",
    ct: unb64(v.ct || "", "ciphertext"), blobs, session: v.session || "", fallback: !!v.fallback, attn: !!v.attn, chan: v.chan || "",
    sig: v.sig ? unb64(v.sig, "signature") : new Uint8Array(0) };
}

// verifyEnvelope checks the signature and shape as envelope.VerifySig does.
export async function verifyEnvelope(e, signKey) {
  if (e.v !== Version && e.v !== Version2) throw new Error("unsupported envelope version " + e.v);
  if (!e.id || !e.from || !e.to || !e.kind || !e.ct.length) throw new Error("incomplete envelope");
  if (!kinds.has(e.kind)) throw new Error("unknown message kind " + e.kind);
  if (e.ct.length > MaxCiphertext) throw new Error("envelope too large");
  if (e.blobs.length > MaxAttachments) throw new Error("too many attachments (max " + MaxAttachments + ")");
  if (e.session && !validID(e.session)) throw new Error("invalid session id");
  if (e.attn !== (e.chan !== "") || (e.attn && (e.v !== Version2 || !validChannel(e.chan)))) throw new Error("invalid attention hint");
  const seen = new Set();
  for (const b of e.blobs) {
    checkBlob(b);
    if (seen.has(b.id)) throw new Error("invalid attachment reference");
    seen.add(b.id);
  }
  if (!(await verifyBytes(signKey, envelopeSigned(e), e.sig))) throw new Error("envelope signature invalid");
}

// open verifies an envelope from sender (its trusted directory entry),
// decrypts it with this device's key and checks that the encrypted fields
// match the signed ones (envelope.Open). It returns the message.
export async function open(json, keys, selfAddress, sender) {
  const e = parseEnvelope(json);
  if (e.to !== selfAddress) throw new Error("envelope addressed to another agent");
  if (e.from !== sender.address) throw new Error("sender key does not belong to envelope sender");
  await verifyEnvelope(e, sender.sign_key);
  let plain;
  try {
    const d = new Decrypter();
    d.addIdentity(keys.box);
    plain = await d.decrypt(e.ct);
  } catch (err) {
    throw new Error("decrypt: " + err.message);
  }
  if (plain.length > MaxCiphertext) throw new Error("inner: too large");
  let v;
  try {
    v = JSON.parse(fromUTF8.decode(plain));
  } catch (err) {
    throw new Error("inner: " + err.message);
  }
  const f = strict(v, "inner", { v: "int", id: "string", from: "string", to: "string", ts: "int", kind: "string", body: "string",
    reply_to: "string", attachments: "array", session: "string", fallback: "boolean", status: "string",
    conv: "string", lid: "string", root: "object", sub: "string", replica: "boolean", origin: "string", emotion: "string",
    target: "object", pid: "string", fan: "array" });
  const target = f.target ? strict(f.target, "target", { address: "string", fingerprint: "string" }) : null;
  const fan = f.fan ? f.fan.map((x) => { const y = strict(x, "fan", { person: "string", roster: "string" }); return { person: y.person || "", roster: y.roster || "" }; }) : null;
  const n = { v: f.v || 0, id: f.id || "", from: f.from || "", to: f.to || "", ts: f.ts || 0, kind: f.kind || "", body: f.body || "",
    reply_to: f.reply_to || "", session: f.session || "", fallback: !!f.fallback, status: f.status || "",
    conv: f.conv || "", lid: f.lid || "", root: f.root ? JSON.stringify(f.root) : "", sub: f.sub || "", replica: !!f.replica,
    origin: f.origin || "", emotion: f.emotion || "", pid: f.pid || "", fan,
    target: target ? { address: target.address || "", fingerprint: target.fingerprint || "" } : null,
    attachments: (f.attachments || []).map((a) => {
      const x = strict(a, "attachment", { blob: "object", name: "string", size: "int", sha256: "string" });
      const b = strict(x.blob || {}, "attachment reference", { id: "string", size: "int", sha256: "string" });
      return { blob: { id: b.id || "", size: b.size || 0, sha256: b.sha256 || "" }, name: x.name || "", size: x.size || 0, sha256: x.sha256 || "" };
    }) };
  if (n.v !== e.v || n.id !== e.id || n.from !== e.from || n.to !== e.to || n.ts !== e.ts || n.kind !== e.kind ||
    n.session !== e.session || n.fallback !== e.fallback) {
    throw new Error("encrypted header does not match signed envelope");
  }
  checkV2(n);
  if (n.attachments.length !== e.blobs.length) throw new Error("encrypted manifest does not match signed attachments");
  n.attachments.forEach((a, i) => {
    const b = e.blobs[i];
    if (a.blob.id !== b.id || a.blob.size !== b.size || a.blob.sha256 !== b.sha256 || a.size < 0 || a.sha256.length !== 64) {
      throw new Error("encrypted manifest does not match signed attachments");
    }
  });
  return n;
}

// ---- signed records (internal/protocol/person.go) ------------------------------------------
//
// A person's roster, a DM's root (E0) and a device session's capabilities.
// Each is signed by one device over a domain line and the record's JSON as
// Go writes it (struct field order, no signature); a hash is the hex SHA-256
// of those bytes. Records from others are parsed strictly and checked here
// exactly as the Go client checks them; what is pinned and when a
// different record is a conflict is the engine's job.

export const MaxPersonLabel = 64;
export const MaxPersonDevices = 8;
export const MaxPersonRecord = 4096;
export const MaxConvRoot = 2048;
export const MaxCaps = 16;
export const MaxCapsRecord = 1024;
export const CapEnv2 = "env2";
export const CapPerson = "person2"; // reads person roster chains, roots v2, fan-out and history
const personDomain = "agentnet-person-v2\n";
const personJoinDomain = "agentnet-person-join-v2\n";
const rootDomain = "agentnet-conv-root-v2\n";
const capsDomain = "agentnet-caps-v1\n";
const fingerprintPattern = /^[0-9a-f]{8}-[0-9a-f]{8}-[0-9a-f]{8}-[0-9a-f]{8}$/;
// Go's unicode.IsPrint: letters, marks, numbers, punctuation, symbols and
// the ASCII space. (Both follow their own Unicode version; a character
// assigned in one and not the other can be judged differently.)
const printable = /^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u;

export const validHash = (s) => typeof s === "string" && sha256Pattern.test(s);
export const validFingerprint = (s) => typeof s === "string" && fingerprintPattern.test(s);
const validToken = (s, max) => typeof s === "string" && s.length >= 1 && s.length <= max && /^[a-z0-9-]+$/.test(s);
const hashOf = async (bytes) => hex(await sha256(bytes));
function fitsRecord(json, max, what) {
  if (utf8.encode(json).length > max) throw new Error(what + ": record too large");
}
const goStrings = (list) => (list ? "[" + list.map(goString).join(",") + "]" : "null");
const sigJSON = (r, withSig) => (withSig && r.sig && r.sig.length ? ',"sig":' + goBytes(r.sig) : "");

// validLabel is person.go's validLabel: 1–64 bytes of printable text
// without surrounding spaces.
export function validLabel(s) {
  if (typeof s !== "string" || s === "" || !wellFormed(s) || utf8.encode(s).length > MaxPersonLabel || s.trim() !== s) {
    throw new Error("person: label must be 1-" + MaxPersonLabel + " bytes of text without surrounding spaces");
  }
  if (!printable.test(s)) throw new Error("person: label has a control character");
  return s;
}

// strictRecord parses a record's JSON (a string of at most max UTF-8
// bytes, or an already parsed value) with only the named fields. A parsed
// value is measured as Go would write it (fitsRecord), which is how devices
// write records.
function strictRecord(json, max, what, fields) {
  if (typeof json === "string" && utf8.encode(json).length > max) throw new Error(what + ": record too large");
  return strict(typeof json === "string" ? JSON.parse(json) : json, what, fields);
}

// Person roster (version 2), a chain: seq 0 is one device signed by it;
// each later step names the step before it (prev) and the device of it
// that signed (by), adds at most one device, whose consent is join (its
// signature over joinBytes), and may remove any. A device is its directory
// entry (address and keys), so old records stay verifiable. Canonical
// bytes leave out sig and join (protocol/person.go).

function marshalRoster(r, withSig) {
  return '{"person":' + goString(r.person) + ',"label":' + goString(r.label) + ',"seq":' + goInt(r.seq, "seq") +
    ',"prev":' + goString(r.prev) + ',"devices":' + (r.devices ? "[" + r.devices.map(marshalPublic).join(",") + "]" : "null") +
    (r.by ? ',"by":' + goString(r.by) : "") + sigJSON(r, withSig) + (withSig && r.join && r.join.length ? ',"join":' + goBytes(r.join) : "") + "}";
}
export const rosterJSON = (r) => marshalRoster(r, true);
const rosterCanonical = (r) => utf8.encode(personDomain + marshalRoster(r, false));
export const rosterHash = (r) => hashOf(rosterCanonical(r));

// joinBytes are what a device signs to join person at seq, after the
// roster whose hash is prev, as the entry dev (protocol.JoinBytes).
export const joinBytes = (person, seq, prev, dev) => utf8.encode(personJoinDomain + '{"person":' + goString(person) +
  ',"seq":' + goInt(seq, "seq") + ',"prev":' + goString(prev) + ',"device":' + marshalPublic(dev) + "}");

// joinConsent is this device's consent to join person as the step seq
// after the roster whose hash is prev (its join signature).
export async function joinConsent(keys, address, person, seq, prev) {
  return signBytes(keys, joinBytes(person, seq, prev, await publicEntry(keys, address)));
}

// checkJoin reports whether join is dev's consent to join person as the
// step seq after the roster whose hash is prev.
export const checkJoin = (person, seq, prev, dev, join) => verifyBytes(dev.sign_key, joinBytes(person, seq, prev, dev), join);

// validateRoster is PersonRoster.Validate: its shape and bounds, and each
// device's entry (not the signatures of the record or the chain).
export async function validateRoster(r) {
  if (!validID(r.person)) throw new Error("person: invalid id");
  validLabel(r.label);
  if (!Number.isSafeInteger(r.seq) || r.seq < 0) throw new Error("person: invalid seq");
  if (r.seq === 0 && (r.prev !== "" || r.by || (r.join && r.join.length) || !r.devices || r.devices.length !== 1)) {
    throw new Error("person: a first roster has one device and nothing before it");
  }
  if (r.seq > 0 && (!validHash(r.prev) || !validFingerprint(r.by))) throw new Error("person: a later roster names the roster before it and its signer");
  if (!r.devices || r.devices.length === 0 || r.devices.length > MaxPersonDevices) throw new Error("person: 1-" + MaxPersonDevices + " devices");
  const addrs = new Set(), fps = new Set();
  for (const d of r.devices) {
    if (!validAddress(d.address)) throw new Error("person: invalid address " + d.address);
    await checkPublic(d);
    const fp = await fingerprint(d);
    if (addrs.has(d.address) || fps.has(fp)) throw new Error("person: a device is listed twice");
    addrs.add(d.address);
    fps.add(fp);
  }
}

// rosterDevice is the device of r with key fingerprint fp, or null.
export async function rosterDevice(r, fp) {
  for (const d of r.devices) if ((await fingerprint(d)) === fp) return d;
  return null;
}

// rosterHas reports whether the device at address with fingerprint fp is in r.
export async function rosterHas(r, address, fp) {
  const d = await rosterDevice(r, fp);
  return !!d && d.address === address;
}

// newRoster creates this device's person, as the person asked: a random
// id, the name they give, and this device (seq 0).
export async function newRoster(keys, address, label) {
  const r = { person: newID(), label, seq: 0, prev: "", devices: [await publicEntry(keys, address)], by: "", sig: null, join: null };
  await validateRoster(r);
  r.sig = await signBytes(keys, rosterCanonical(r));
  if (utf8.encode(rosterJSON(r)).length > MaxPersonRecord) throw new Error("person: the record is too large; use a shorter label");
  return r;
}

// nextRoster is the step after prev with devices, signed by this device
// (at address, a device of prev); join is an added device's consent (null
// when none is added).
export async function nextRoster(keys, address, prev, devices, join) {
  const by = await fingerprint(await publicEntry(keys, address));
  if (!(await rosterHas(prev, address, by))) throw new Error("person: this device is not in the roster it would follow");
  const r = { person: prev.person, label: prev.label, seq: prev.seq + 1, prev: await rosterHash(prev), devices, by, sig: null, join: join || null };
  await validateRoster(r);
  r.sig = await signBytes(keys, rosterCanonical(r));
  if (utf8.encode(rosterJSON(r)).length > MaxPersonRecord) throw new Error("person: the record is too large");
  return r;
}

export async function parseRoster(json) {
  const f = strictRecord(json, MaxPersonRecord, "person", { person: "string", label: "string", seq: "int", prev: "string", devices: "array",
    by: "string", sig: "string", join: "string" });
  const devices = f.devices ? await Promise.all(f.devices.map((d) => parsePublicShape(d))) : null;
  const r = { person: f.person || "", label: f.label || "", seq: f.seq || 0, prev: f.prev || "", devices, by: f.by || "",
    sig: f.sig ? unb64(f.sig, "person signature") : null, join: f.join ? unb64(f.join, "join signature") : null };
  await validateRoster(r);
  fitsRecord(rosterJSON(r), MaxPersonRecord, "person");
  return r;
}

// verifyFirst checks r as the first roster of its person: signed by its one device.
export async function verifyFirst(r) {
  await validateRoster(r);
  if (r.seq !== 0) throw new Error("person: not a first roster");
  if (!(await verifyBytes(r.devices[0].sign_key, rosterCanonical(r), r.sig))) throw new Error("person: signature invalid");
}

// verifyNext checks r as the step after prev (itself verified): the chain
// link, the signer, and the consent of an added device. It returns the
// added device, or null.
export async function verifyNext(r, prev) {
  await validateRoster(r);
  if (r.person !== prev.person || r.seq !== prev.seq + 1 || r.prev !== (await rosterHash(prev))) throw new Error("person: not the next roster of that chain");
  const signer = await rosterDevice(prev, r.by);
  if (!signer) throw new Error("person: signed by a device that is not in the roster before it");
  if (!(await verifyBytes(signer.sign_key, rosterCanonical(r), r.sig))) throw new Error("person: signature invalid");
  let added = null;
  for (const d of r.devices) {
    const old = await rosterDevice(prev, await fingerprint(d));
    if (old && old.address !== d.address) throw new Error("person: a device changed its address");
    if (!old && added) throw new Error("person: more than one device added in one step");
    if (!old) added = d;
  }
  if (!added) {
    if (r.join && r.join.length) throw new Error("person: a join without an added device");
    return null;
  }
  if (!(await verifyBytes(added.sign_key, joinBytes(r.person, r.seq, r.prev, added), r.join))) {
    throw new Error("person: the added device did not consent (join signature invalid)");
  }
  return added;
}

// Linking a new device to a person (protocol/link.go): the existing device
// shows a LinkOffer (its person's current step, itself, a device invite
// and a 32-byte secret the Hub never sees); the new device joins with the
// invite, adding its consent (join) and an HMAC under the secret over a
// fixed transcript; the existing device checks both and asks its person.

export const LinkPrefix = "agentnet-link-v2:";
const linkDomain = "agentnet-link-v2\n";

function marshalOffer(o) {
  return '{"v":' + goInt(o.v, "version") + ',"invite":' + goString(o.invite) + ',"offer":' + goString(o.offer) +
    ',"expires":' + goInt(o.expires, "expiry") + ',"person":' + goString(o.person) + ',"seq":' + goInt(o.seq, "seq") +
    ',"roster":' + goString(o.roster) + ',"approver":{"address":' + goString(o.approver.address) + ',"fingerprint":' +
    goString(o.approver.fingerprint) + '},"secret":' + goBytes(o.secret) + "}";
}

// encodeOffer is LinkOffer.Encode: the prefix and the offer's JSON in
// unpadded base64url.
export const encodeOffer = (o) => LinkPrefix + b64url(utf8.encode(marshalOffer(o)));

// decodeOffer is protocol.DecodeLinkOffer: the code alone, as a fragment,
// or at the end of a whole URL; its shape checked.
export function decodeOffer(text) {
  let s = String(text).trim();
  const i = s.indexOf("#" + LinkPrefix);
  if (i >= 0) s = s.slice(i + 1);
  if (!s.startsWith(LinkPrefix)) throw new Error("not an AgentNet device link code");
  let f;
  try {
    const raw = s.slice(LinkPrefix.length);
    if (!/^[A-Za-z0-9_-]*$/.test(raw)) throw new Error("");
    f = strict(JSON.parse(fromUTF8.decode(Uint8Array.from(unb64url(raw), (c) => c.charCodeAt(0)))), "device link",
      { v: "int", invite: "string", offer: "string", expires: "int", person: "string", seq: "int", roster: "string", approver: "object", secret: "string" });
  } catch (e) {
    throw new Error("device link code damaged");
  }
  const a = strict(f.approver || {}, "device link approver", { address: "string", fingerprint: "string" });
  const o = { v: f.v || 0, invite: f.invite || "", offer: f.offer || "", expires: f.expires || 0, person: f.person || "", seq: f.seq || 0,
    roster: f.roster || "", approver: { address: a.address || "", fingerprint: a.fingerprint || "" }, secret: f.secret ? unb64(f.secret, "link secret") : new Uint8Array(0) };
  if (o.v !== 2 || !validID(o.offer) || !(o.expires > 0) || !validID(o.person) || o.seq < 0 || !validHash(o.roster) ||
    !validFingerprint(o.approver.fingerprint) || o.secret.length !== 32 || !validAddress(o.approver.address)) {
    throw new Error("device link code damaged");
  }
  decodeInvite(o.invite); // throws if it is not one
  return o;
}

// linkTranscript is protocol.LinkTranscript.
export const linkTranscript = (o, dev, join) => utf8.encode(linkDomain + '{"offer":' + goString(o.offer) + ',"expires":' + goInt(o.expires, "expiry") +
  ',"person":' + goString(o.person) + ',"seq":' + goInt(o.seq, "seq") + ',"roster":' + goString(o.roster) + ',"approver":' +
  goString(o.approver.fingerprint) + ',"device":' + marshalPublic(dev) + ',"join":' + goBytes(join) + "}");

// linkMAC is HMAC-SHA256 under the offer's secret over the transcript.
export async function linkMAC(o, dev, join) {
  const key = await subtle.importKey("raw", o.secret, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return new Uint8Array(await subtle.sign("HMAC", key, linkTranscript(o, dev, join)));
}

// checkLinkMAC compares mac with linkMAC (WebCrypto's verify: constant time).
export async function checkLinkMAC(o, dev, join, mac) {
  const key = await subtle.importKey("raw", o.secret, { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  return subtle.verify("HMAC", key, mac, linkTranscript(o, dev, join));
}

// History items (client/history.go): a message a device of your person
// forwards to another as history, with the key it was sent under (the
// forwarding device's word: the original signature covered ciphertext for
// another device). Attachments are manifests only (no blob here).

// historyJSON is json.Marshal of a HistoryItem.
export function historyJSON(h) {
  let s = '{"v":1,"from":' + goString(h.from) + ',"from_key":' + goString(h.from_key) + ',"id":' + goString(h.id) + ',"lid":' + goString(h.lid) +
    ',"ts":' + goInt(h.ts, "time") + ',"kind":' + goString(h.kind) + ',"body":' + goString(h.body);
  if (h.reply_to) s += ',"reply_to":' + goString(h.reply_to);
  if (h.status) s += ',"status":' + goString(h.status);
  if (h.sub) s += ',"sub":' + goString(h.sub);
  if (h.origin) s += ',"origin":' + goString(h.origin);
  if (h.emotion) s += ',"emotion":' + goString(h.emotion);
  if (h.target) s += ',"target":{"address":' + goString(h.target.address) + ',"fingerprint":' + goString(h.target.fingerprint) + "}";
  if (h.pid) s += ',"pid":' + goString(h.pid);
  if (h.attachments && h.attachments.length) {
    s += ',"attachments":[' + h.attachments.map((a) => '{"blob":{"id":"","size":0,"sha256":""},"name":' + goString(a.name) +
      ',"size":' + goInt(a.size, "size") + ',"sha256":' + goString(a.sha256) + "}").join(",") + "]";
  }
  return s + ',"at":' + goInt(h.at, "time") + "}";
}

// parseHistory reads a history item strictly (as the core's decodeStrict).
export function parseHistory(json) {
  const f = strict(JSON.parse(json), "history item", { v: "int", from: "string", from_key: "string", id: "string", lid: "string", ts: "int",
    kind: "string", body: "string", reply_to: "string", status: "string", sub: "string", origin: "string", emotion: "string",
    target: "object", pid: "string", attachments: "array", at: "int" });
  if (f.v !== 1 || !validID(f.id) || !validID(f.lid) || !validAddress(f.from || "") || !validFingerprint(f.from_key || "")) throw new Error("a malformed history item");
  const target = f.target ? strict(f.target, "target", { address: "string", fingerprint: "string" }) : null;
  return { from: f.from, from_key: f.from_key, id: f.id, lid: f.lid, ts: f.ts || 0, at: f.at || 0, kind: f.kind || "", body: f.body || "",
    reply_to: f.reply_to || "", status: f.status || "", sub: f.sub || "", origin: f.origin || "", emotion: f.emotion || "", pid: f.pid || "",
    target: target ? { address: target.address || "", fingerprint: target.fingerprint || "" } : null,
    attachments: (f.attachments || []).map((a) => { const x = strict(a, "attachment", { blob: "object", name: "string", size: "int", sha256: "string" });
      return { name: text(x.name || "", "attachment name"), size: x.size || 0, sha256: x.sha256 || "" }; }) };
}

// File messages (client/historyfiles.go): between devices of one person,
// a request for a history message's file (by its message's logical id and
// the file's SHA-256), and the offer answering it: the file attached,
// encrypted to the asking device, or why not.

// fileMsgJSON is json.Marshal of a fileMsg.
export function fileMsgJSON(m) {
  let s = '{"v":1,"type":' + goString(m.type) + ',"lid":' + goString(m.lid) + ',"sha256":' + goString(m.sha256);
  if (m.available) s += ',"available":true';
  if (m.detail) s += ',"detail":' + goString(m.detail);
  return s + "}";
}

// parseFileMsg reads a file message strictly (as the core's decodeStrict).
export function parseFileMsg(json) {
  const f = strict(JSON.parse(json), "file message", { v: "int", type: "string", lid: "string", sha256: "string", available: "boolean", detail: "string" });
  if (f.v !== 1 || !validID(f.lid) || !validHash(f.sha256) || (f.type !== "request" && f.type !== "offer")) throw new Error("a malformed file message");
  return { type: f.type, lid: f.lid, sha256: f.sha256, available: !!f.available, detail: f.detail || "" };
}

// DM root (E0).

function marshalRoot(c, withSig) {
  const cr = c.creator;
  return '{"v":' + goInt(c.v, "version") + ',"kind":' + goString(c.kind) + ',"creator":{"person":' + goString(cr.person) +
    ',"roster":' + goString(cr.roster) + ',"address":' + goString(cr.address) + ',"fingerprint":' + goString(cr.fingerprint) + "}" +
    ',"members":' + (c.members ? "[" + c.members.map((m) => '{"person":' + goString(m.person) + ',"roster":' + goString(m.roster) + "}").join(",") + "]" : "null") +
    ',"nonce":' + goString(c.nonce) + ',"created":' + goInt(c.created, "time") + sigJSON(c, withSig) + "}";
}
export const rootJSON = (c) => marshalRoot(c, true);
const rootCanonical = (c) => utf8.encode(rootDomain + marshalRoot(c, false));
// rootID is the conversation id: the hash of the root's canonical bytes.
export const rootID = (c) => hashOf(rootCanonical(c));
export const rootMember = (c, person) => ((c.members || []).find((m) => m.person === person) || {}).roster;

export function validateRoot(c) {
  if (c.v !== 2 || c.kind !== "dm") throw new Error("conversation: unsupported root");
  const cr = c.creator;
  if (!validID(cr.person) || !validHash(cr.roster) || !validFingerprint(cr.fingerprint)) throw new Error("conversation: invalid creator");
  if (!validAddress(cr.address)) throw new Error("conversation: invalid address " + cr.address);
  if (!c.members || c.members.length !== 2) throw new Error("conversation: a DM has two members");
  for (const m of c.members) if (!validID(m.person) || !validHash(m.roster)) throw new Error("conversation: invalid member");
  if (c.members[0].person >= c.members[1].person) throw new Error("conversation: members must be distinct and sorted");
  if (rootMember(c, cr.person) !== cr.roster) throw new Error("conversation: the creator is not a member with that roster");
  if (!validID(c.nonce) || !(c.created > 0)) throw new Error("conversation: invalid nonce or time");
}

// newRoot starts a DM between this device's person (me: {person, roster,
// address, fingerprint}) and another person ({person, roster}, as pinned
// here). Each call is a separate conversation.
export async function newRoot(keys, me, other) {
  const members = [{ person: me.person, roster: me.roster }, { person: other.person, roster: other.roster }]
    .sort((a, b) => (a.person < b.person ? -1 : 1));
  const c = { v: 2, kind: "dm", creator: { person: me.person, roster: me.roster, address: me.address, fingerprint: me.fingerprint },
    members, nonce: newID(), created: Math.floor(Date.now() / 1000) };
  validateRoot(c);
  c.sig = await signBytes(keys, rootCanonical(c));
  if (utf8.encode(rootJSON(c)).length > MaxConvRoot) throw new Error("conversation root too large");
  return c;
}

export function parseRoot(json) {
  const f = strictRecord(json, MaxConvRoot, "conversation", { v: "int", kind: "string", creator: "object", members: "array",
    nonce: "string", created: "int", sig: "string" });
  const cr = strict(f.creator || {}, "conversation creator", { person: "string", roster: "string", address: "string", fingerprint: "string" });
  const c = { v: f.v || 0, kind: f.kind || "", nonce: f.nonce || "", created: f.created || 0, sig: f.sig ? unb64(f.sig, "root signature") : null,
    creator: { person: cr.person || "", roster: cr.roster || "", address: cr.address || "", fingerprint: cr.fingerprint || "" },
    members: f.members ? f.members.map((m) => { const x = strict(m, "conversation member", { person: "string", roster: "string" });
      return { person: x.person || "", roster: x.roster || "" }; }) : null };
  validateRoot(c);
  fitsRecord(rootJSON(c), MaxConvRoot, "conversation");
  return c;
}

// verifyRoot checks c and that creatorKey, the creator device's key, signed it.
export async function verifyRoot(c, creatorKey) {
  validateRoot(c);
  if (!(await verifyBytes(creatorKey, rootCanonical(c), c.sig))) throw new Error("conversation: root signature invalid");
}

// Capabilities of one device session.

function marshalCaps(c, withSig) {
  return '{"address":' + goString(c.address) + ',"session":' + goString(c.session) + ',"caps":' + goStrings(c.caps) +
    ',"ts":' + goInt(c.ts, "time") + sigJSON(c, withSig) + "}";
}
export const capsJSON = (c) => marshalCaps(c, true);
const capsCanonical = (c) => utf8.encode(capsDomain + marshalCaps(c, false));

export function validateCaps(c) {
  if (!validAddress(c.address)) throw new Error("caps: invalid address " + c.address);
  if (!validID(c.session) || !(c.ts > 0) || (c.caps && c.caps.length > MaxCaps)) throw new Error("caps: invalid record");
  (c.caps || []).forEach((name, i) => {
    if (!validToken(name, 32) || (i > 0 && c.caps[i - 1] >= name)) throw new Error("caps: names must be sorted, unique [a-z0-9-] tokens");
  });
}

// newCaps is this device session's signed record (for PUT /v1/caps).
export async function newCaps(keys, address, session, caps = [CapEnv2, CapPerson]) {
  const c = { address, session, caps: [...caps].sort(), ts: Math.floor(Date.now() / 1000) };
  validateCaps(c);
  c.sig = await signBytes(keys, capsCanonical(c));
  return c;
}

export function parseCaps(json) {
  const f = strictRecord(json, MaxCapsRecord, "caps", { address: "string", session: "string", caps: "array", ts: "int", sig: "string" });
  if ((f.caps || []).some((x) => typeof x !== "string")) throw new Error("caps: names must be strings");
  const c = { address: f.address || "", session: f.session || "", caps: f.caps || null, ts: f.ts || 0, sig: f.sig ? unb64(f.sig, "caps signature") : null };
  validateCaps(c);
  fitsRecord(capsJSON(c), MaxCapsRecord, "caps");
  return c;
}

export async function verifyCaps(c, signKey) {
  validateCaps(c);
  if (!(await verifyBytes(signKey, capsCanonical(c), c.sig))) throw new Error("caps: signature invalid");
}

// profileSupports is protocol.Profile.Supports for a profile as the relay
// sends it ({person, sessions, live, caps}): there are sessions, and each
// has a record that verifies with signKey, names address and that session,
// and lists name. Sessions are the relay's statement, not signed.
export async function profileSupports(profile, address, signKey, name) {
  const sessions = (profile && profile.sessions) || [];
  if (!sessions.length) return false;
  const has = new Set();
  for (const raw of profile.caps || []) {
    let c;
    try {
      c = parseCaps(raw);
      await verifyCaps(c, signKey);
    } catch (e) {
      continue;
    }
    if (c.address === address && (c.caps || []).includes(name)) has.add(c.session);
  }
  return sessions.every((s) => has.has(s));
}

// ---- participation events (internal/protocol/participation.go) --------------------------------
//
// One signed step of an agent's participation in a DM, carried in a DM
// message (sub "event", with its pid). A browser device is human-only: it
// may invite another member's agent or dismiss one, never host one.

export const MaxGrant = 200;
export const MaxTaskKeys = 16;
export const MaxInviteNote = 1024;
export const MaxParticipationEvent = 8192;
const participationDomain = "agentnet-participation-v1\n";
const eventTypes = new Set(["invite", "accept", "decline", "dismiss"]);
// Go's unicode.IsPrint, plus a newline (an invite's note).
const noteText = /^[\p{L}\p{M}\p{N}\p{P}\p{S} \n]*$/u;

function marshalEvent(e, withSig) {
  const a = e.author;
  let s = '{"v":' + goInt(e.v, "version") + ',"conv":' + goString(e.conv) + ',"pid":' + goString(e.pid) + ',"type":' + goString(e.type) +
    ',"prev":' + goString(e.prev) + ',"author":{"person":' + goString(a.person) + ',"roster":' + goString(a.roster) +
    ',"address":' + goString(a.address) + ',"fingerprint":' + goString(a.fingerprint) + '},"ts":' + goInt(e.ts, "time");
  if (e.host) s += ',"host":{"person":' + goString(e.host.person) + ',"address":' + goString(e.host.address) + ',"fingerprint":' + goString(e.host.fingerprint) + "}";
  if (e.grant && e.grant.length) s += ',"grant":[' + e.grant.map((g) => '{"lid":' + goString(g.lid) + ',"fingerprint":' + goString(g.fingerprint) + "}").join(",") + "]";
  if (e.audience) s += ',"audience":' + goString(e.audience);
  if (e.task_keys && e.task_keys.length) s += ',"task_keys":' + goStrings(e.task_keys);
  if (e.note) s += ',"note":' + goString(e.note);
  return s + sigJSON(e, withSig) + "}";
}
export const eventJSON = (e) => marshalEvent(e, true);
const eventCanonical = (e) => utf8.encode(participationDomain + marshalEvent(e, false));
export const eventHash = (e) => hashOf(eventCanonical(e));

// ---- notifications (docs/revival/NOTIFY.md §2) -------------------------------------------

export const channelPattern = /^[A-Za-z0-9_-]{22}$/;
// validChannel is protocol.ValidNotifyChannel: 22 characters that decode
// to 16 bytes and encode back to themselves (no stray bits in the last).
export function validChannel(s) {
  if (typeof s !== "string" || !channelPattern.test(s)) return false;
  try {
    const raw = Uint8Array.from(unb64url(s), (c) => c.charCodeAt(0));
    return raw.length === 16 && b64url(raw) === s;
  } catch (e) { return false; }
}

// notifyChannel is protocol.NotifyChannel: the recipient device's opaque
// channel for a conversation, the first 16 bytes of
// SHA-256("agentnet-notify-channel-v1\n" + conv + "\n" + fingerprint),
// base64url without padding. The sender and the recipient compute it; the
// relay cannot.
export async function notifyChannel(conv, fingerprint) {
  if (!validHash(conv) || !validFingerprint(fingerprint)) throw new Error("notify channel: invalid conversation or key");
  const sum = await sha256(utf8.encode("agentnet-notify-channel-v1\n" + conv + "\n" + fingerprint));
  return b64url(sum.slice(0, 16));
}

function uniqueList(items, max, valid, what) {
  if (items.length > max) throw new Error("participation: " + what + ": more than " + max);
  const seen = new Set();
  for (const s of items) {
    if (!valid(s) || seen.has(s)) throw new Error("participation: " + what + ": invalid or repeated " + s);
    seen.add(s);
  }
}

// validateEvent is ParticipationEvent.Validate. grant and task_keys are
// null when absent, which matters: an accept, decline or dismiss may not
// carry them even empty.
export function validateEvent(e) {
  if (e.v !== 1 || !validHash(e.conv) || !validID(e.pid) || !(e.ts > 0)) throw new Error("participation: invalid event");
  const a = e.author;
  if (!validID(a.person) || !validHash(a.roster) || !validFingerprint(a.fingerprint)) throw new Error("participation: invalid author");
  if (!validAddress(a.address)) throw new Error("participation: invalid address " + a.address);
  if (!eventTypes.has(e.type)) throw new Error("participation: unknown event type " + e.type);
  if (e.type === "invite") {
    if (e.prev !== "" || !e.host || e.audience !== "conversation") {
      throw new Error("participation: an invite has no prev, and names a host and the conversation audience");
    }
    if (!validID(e.host.person) || !validFingerprint(e.host.fingerprint)) throw new Error("participation: invalid host");
    if (!validAddress(e.host.address)) throw new Error("participation: host: invalid address " + e.host.address);
    for (const g of e.grant || []) if (!validID(g.lid) || !validFingerprint(g.fingerprint)) throw new Error("participation: grant: invalid message reference");
    uniqueList((e.grant || []).map((g) => g.lid + "/" + g.fingerprint), MaxGrant, () => true, "grant");
    uniqueList(e.task_keys || [], MaxTaskKeys, validFingerprint, "task keys");
    if (!wellFormed(e.note) || utf8.encode(e.note).length > MaxInviteNote) throw new Error("participation: note too long or not text");
    if (!noteText.test(e.note)) throw new Error("participation: note has a control character");
    return;
  }
  if (!validHash(e.prev) || e.host || e.grant !== null || e.audience !== "" || e.task_keys !== null || e.note !== "") {
    throw new Error("participation: an accept, decline or dismiss names only the event it follows");
  }
}

// signEvent signs an event this device's person authors (an invite or a
// dismissal): fields as the Go struct, author this device.
export async function signEvent(keys, fields) {
  const e = { v: 1, prev: "", audience: "", note: "", host: null, grant: null, task_keys: null, ...fields };
  validateEvent(e);
  e.sig = await signBytes(keys, eventCanonical(e));
  fitsRecord(eventJSON(e), MaxParticipationEvent, "participation");
  return e;
}

export function parseEvent(json) {
  const f = strictRecord(json, MaxParticipationEvent, "participation", { v: "int", conv: "string", pid: "string", type: "string", prev: "string",
    author: "object", ts: "int", host: "object", grant: "array", audience: "string", task_keys: "array", note: "string", sig: "string" });
  const a = strict(f.author || {}, "participation author", { person: "string", roster: "string", address: "string", fingerprint: "string" });
  const h = f.host ? strict(f.host, "participation host", { person: "string", address: "string", fingerprint: "string" }) : null;
  if ((f.task_keys || []).some((k) => typeof k !== "string")) throw new Error("participation: task keys must be strings");
  const e = { v: f.v || 0, conv: f.conv || "", pid: f.pid || "", type: f.type || "", prev: f.prev || "", ts: f.ts || 0,
    author: { person: a.person || "", roster: a.roster || "", address: a.address || "", fingerprint: a.fingerprint || "" },
    host: h ? { person: h.person || "", address: h.address || "", fingerprint: h.fingerprint || "" } : null,
    grant: f.grant ? f.grant.map((g) => { const x = strict(g, "participation grant", { lid: "string", fingerprint: "string" });
      return { lid: x.lid || "", fingerprint: x.fingerprint || "" }; }) : null,
    audience: f.audience || "", task_keys: f.task_keys || null, note: f.note || "", sig: f.sig ? unb64(f.sig, "event signature") : null };
  validateEvent(e);
  fitsRecord(eventJSON(e), MaxParticipationEvent, "participation");
  return e;
}

export async function verifyEvent(e, authorKey) {
  validateEvent(e);
  if (!(await verifyBytes(authorKey, eventCanonical(e), e.sig))) throw new Error("participation: signature invalid");
}

// ---- attachments (envelope.Attachment; client files.go) ---------------------------------------

// ChunkSize is protocol.ChunkSize: the largest upload piece.
export const ChunkSize = 512 << 10;

// The browser device holds a file in memory and in IndexedDB until it is
// sent, so it takes smaller files than a computer (client.MaxFileSize).
export const BrowserMaxFile = 25 << 20;
export const BrowserMaxMessage = 50 << 20;

// encryptFile is spoolFile: bytes encrypted to the recipient as one age
// file, with the manifest entry (the ciphertext's id, size and digest, the
// name, the plaintext's size and digest) and the ciphertext itself.
export async function encryptFile(bytes, name, recipient) {
  if (!(bytes instanceof Uint8Array)) throw new Error("a file is bytes");
  const e = new Encrypter();
  e.addRecipient(recipient.box_recipient);
  const ct = await e.encrypt(bytes);
  const attachment = { blob: { id: newID(), size: ct.length, sha256: hex(await sha256(ct)) },
    name: text(name, "attachment name"), size: bytes.length, sha256: hex(await sha256(bytes)) };
  return { attachment, ct };
}

// decryptFile takes a file's ciphertext only if it is exactly what the
// signed manifest names, decrypts it with this device's key, and returns
// the plaintext only if it is exactly what the manifest describes.
export async function decryptFile(ct, att, keys) {
  if (!(ct instanceof Uint8Array) || ct.length !== att.blob.size || hex(await sha256(ct)) !== att.blob.sha256) {
    throw new Error("the file is not the one the sender signed");
  }
  let pt;
  try {
    const d = new Decrypter();
    d.addIdentity(keys.box);
    pt = await d.decrypt(ct);
  } catch (e) {
    throw new Error("the file could not be decrypted here");
  }
  if (pt.length !== att.size || hex(await sha256(pt)) !== att.sha256) throw new Error("the decrypted file is not the one described");
  return pt;
}

const windowsReserved = new Set(["CON", "PRN", "AUX", "NUL", ...[1, 2, 3, 4, 5, 6, 7, 8, 9].flatMap((n) => ["COM" + n, "LPT" + n])]);

// safeName is client.SafeName: a sender-chosen name as a plain file name,
// with no directory parts, control characters or characters Windows
// forbids, never empty, "." or "..", at most 200 bytes.
export function safeName(name) {
  let s = [...String(name)].map((c) => { const cp = c.codePointAt(0); return cp < 0x20 || cp === 0x7f || '/\\:*?"<>|'.includes(c) ? "_" : c; }).join("");
  s = s.replace(/^[ .]+|[ .]+$/g, "");
  if (!s) return "attachment";
  if (windowsReserved.has(s.split(".")[0].toUpperCase())) s = "_" + s;
  while (utf8.encode(s).length > 200) s = [...s].slice(0, -1).join("");
  return s;
}

// sniffImage names a raster image type from the bytes themselves (never a
// name or a claimed type): PNG, JPEG, GIF or WebP; "" for anything else,
// SVG and HTML included, which are never shown as images.
export function sniffImage(b) {
  const at = (i, s) => [...s].every((c, j) => b[i + j] === c.charCodeAt(0));
  if (b.length >= 8 && b[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (at(0, "GIF87a") || at(0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return "";
}
