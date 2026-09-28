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
  const p = strict(v, "directory entry", { address: "string", sign_key: "string", box_recipient: "string", box_sig: "string" });
  const out = { address: p.address || "", sign_key: unb64(p.sign_key || "", "signing key"),
    box_recipient: p.box_recipient || "", box_sig: unb64(p.box_sig || "", "key signature") };
  if (out.sign_key.length !== 32) throw new Error("bad signing key");
  if (!recipientPattern.test(out.box_recipient)) throw new Error("bad encryption key");
  try { new Encrypter().addRecipient(out.box_recipient); } catch (e) { throw new Error("bad encryption key"); }
  if (!(await verifyBytes(out.sign_key, bindingMessage(out.address, out.box_recipient), out.box_sig))) {
    throw new Error("encryption key not signed by signing key");
  }
  return out;
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
export async function joinRequest(keys, address, secret) {
  text(secret, "invite secret");
  const pub = await publicEntry(keys, address);
  const signed = (sig) => '{"secret":' + goString(secret) + ',"public":' + marshalPublic(pub) + ',"sig":' + goBytes(sig) + "}";
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
  if (withSig && e.sig && e.sig.length) s += ',"sig":' + goBytes(e.sig);
  return s + "}";
}

const envelopeSigned = (e) => utf8.encode("agentnet-envelope-v1\n" + marshalEnvelope(e, false));

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
  return s + "}";
}

function checkBlob(b) {
  if (!validID(b.id) || !Number.isSafeInteger(b.size) || b.size <= 0 || typeof b.sha256 !== "string" || b.sha256.length !== 64) {
    throw new Error("invalid attachment reference");
  }
}

// seal encrypts a message to the recipient's directory entry and signs it
// with this device's key (envelope.Seal). m has id, from, to, ts, kind, body
// and optionally reply_to, status, session, fallback and attachments (each
// {blob: {id, size, sha256}, name, size, sha256}). It returns the envelope as
// the JSON POST /v1/messages takes. The same stored JSON is sent again on a
// retry, never sealed again.
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
  const inner = { v: Version, id: m.id, from: m.from, to: m.to, ts: m.ts, kind: m.kind, body: text(m.body, "message"),
    reply_to: m.reply_to || "", attachments, session: m.session || "", fallback: !!m.fallback, status: text(m.status || "", "status") };
  const e = new Encrypter();
  e.addRecipient(recipient.box_recipient);
  const ct = await e.encrypt(utf8.encode(marshalInner(inner)));
  if (ct.length > MaxCiphertext) throw new Error("message too large (" + ct.length + " bytes encrypted, max " + MaxCiphertext + ")");
  const env = { v: Version, id: m.id, from: m.from, to: m.to, ts: m.ts, kind: m.kind, ct,
    blobs: attachments.map((a) => a.blob), session: inner.session, fallback: inner.fallback };
  env.sig = await signBytes(keys, envelopeSigned(env));
  return marshalEnvelope(env, true);
}

// parseEnvelope reads an envelope as the Hub sends or stores it, strictly.
export function parseEnvelope(json) {
  const v = strict(typeof json === "string" ? JSON.parse(json) : json, "envelope", { v: "int", id: "string", from: "string",
    to: "string", ts: "int", kind: "string", ct: "string", blobs: "array", session: "string", fallback: "boolean", sig: "string" });
  const blobs = (v.blobs || []).map((b) => strict(b, "attachment reference", { id: "string", size: "int", sha256: "string" }))
    .map((b) => ({ id: b.id || "", size: b.size || 0, sha256: b.sha256 || "" }));
  return { v: v.v || 0, id: v.id || "", from: v.from || "", to: v.to || "", ts: v.ts || 0, kind: v.kind || "",
    ct: unb64(v.ct || "", "ciphertext"), blobs, session: v.session || "", fallback: !!v.fallback,
    sig: v.sig ? unb64(v.sig, "signature") : new Uint8Array(0) };
}

// verifyEnvelope checks the signature and shape as envelope.VerifySig does.
export async function verifyEnvelope(e, signKey) {
  if (e.v !== Version) throw new Error("unsupported envelope version " + e.v);
  if (!e.id || !e.from || !e.to || !e.kind || !e.ct.length) throw new Error("incomplete envelope");
  if (!kinds.has(e.kind)) throw new Error("unknown message kind " + e.kind);
  if (e.ct.length > MaxCiphertext) throw new Error("envelope too large");
  if (e.blobs.length > MaxAttachments) throw new Error("too many attachments (max " + MaxAttachments + ")");
  if (e.session && !validID(e.session)) throw new Error("invalid session id");
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
    reply_to: "string", attachments: "array", session: "string", fallback: "boolean", status: "string" });
  const n = { v: f.v || 0, id: f.id || "", from: f.from || "", to: f.to || "", ts: f.ts || 0, kind: f.kind || "", body: f.body || "",
    reply_to: f.reply_to || "", session: f.session || "", fallback: !!f.fallback, status: f.status || "",
    attachments: (f.attachments || []).map((a) => {
      const x = strict(a, "attachment", { blob: "object", name: "string", size: "int", sha256: "string" });
      const b = strict(x.blob || {}, "attachment reference", { id: "string", size: "int", sha256: "string" });
      return { blob: { id: b.id || "", size: b.size || 0, sha256: b.sha256 || "" }, name: x.name || "", size: x.size || 0, sha256: x.sha256 || "" };
    }) };
  if (n.v !== e.v || n.id !== e.id || n.from !== e.from || n.to !== e.to || n.ts !== e.ts || n.kind !== e.kind ||
    n.session !== e.session || n.fallback !== e.fallback) {
    throw new Error("encrypted header does not match signed envelope");
  }
  if (n.attachments.length !== e.blobs.length) throw new Error("encrypted manifest does not match signed attachments");
  n.attachments.forEach((a, i) => {
    const b = e.blobs[i];
    if (a.blob.id !== b.id || a.blob.size !== b.size || a.blob.sha256 !== b.sha256 || a.size < 0 || a.sha256.length !== 64) {
      throw new Error("encrypted manifest does not match signed attachments");
    }
  });
  return n;
}
