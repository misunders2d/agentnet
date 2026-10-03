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
export const Version3 = 3;               // envelope.Version3: a control (reaction, revision, retraction) about one message
export const CapControl = "ctl3";        // protocol.CapControl: this device reads version 3 controls
export const SubReaction = "reaction", SubRevision = "revision", SubRetraction = "retraction";
export const SubStatus = "status", SubDecision = "decision"; // headless: a host's per-request state; an operator's decision to a host (hdl1)
export const CapHeadless = "hdl1";        // protocol cap: reads status controls and version 2 reports, sends decisions
export const CapExternalParticipation = "apx1"; // selected DM excerpts and exact outside-host participation
export const CapHumanParticipation = "hgp1"; // protocol.CapHumanParticipation: reads human guests' scoped turns
export const MaxHumanAudience = 16, MaxHumanProof = 32;
export const SubGroupProof = "group-proof", SubGroupContext = "group-context"; // bounded quiet carriers; no capability advertisement
export const SubGroupInvite = "group-invite", SubGroupConsent = "group-consent", SubGroupWithdrawal = "group-withdrawal";
export const CapGroup = "grp1"; // protocol constant only; absent from advertised defaults until Engine parity
export const SubDriveSpace = "drive-space"; // envelope.SubDriveSpace: a conversation's shared Drive space record (version 2, quiet)
export const CapDrive = "drv1";           // protocol.CapDriveSpace: this device reads Drive space records
export const CapTyping = "typing1", SignalTTL = 5000, TypingThrottle = 3000;
export const MaxSignalCiphertext = 2048, MaxSignalBody = 4096;
export const isControl = (sub) => sub === SubReaction || sub === SubRevision || sub === SubRetraction || sub === SubStatus || sub === SubDecision;
// A person's deletion of their copy of one conversation, to their own devices
// only (envelope.SubClear, version 3). Never history (protocol.CapConvClear).
export const SubClear = "clear", CapConvClear = "clr1", MaxClearParts = 1024, MaxClearTurns = 2000;
export const ExecStates = ["queued", "awaiting", "running", "needs_human", "resolved", "stopped", "not_run", "declined", "failed", "cancelled", "interrupted", "answered"]; // envelope.statusStates
export const DecisionActions = ["accept", "decline", "resolve", "reply", "cancel"];
export const MaxDetailBytes = 400;
export const MaxRevisionBytes = 64 << 10, MaxReasonBytes = 200, MaxEmojiBytes = 64, MaxEmojiRunes = 12;
export const MaxDecisionText = 16 << 10; // envelope.MaxDecisionText
// validStateToken is envelope.validStateToken: a host's own state name as
// its report showed it (never interpreted here).
export const validStateToken = (s) => typeof s === "string" && /^[a-z_]{1,32}$/.test(s);
export const MaxCiphertext = 256 << 10;  // envelope.MaxCiphertext
export const MaxAttachments = 8;         // envelope.MaxAttachments
export const CapReplyReceiver = "rcv1"; // additive selected return route; not advertised
// envelope.StatusProgress: a version 1 plain-text nonterminal update replying
// to one exact request (never an answer); protocol.CapProgress reads it.
export const StatusProgress = "progress", CapProgress = "prg1";
// protocol.CapAgentReaction: this device reads an assistant's own reactions
// (envelope.AssistantReaction).
export const CapAgentReaction = "agr1";
// assistantReaction is envelope.AssistantReaction: a version 3 reaction that
// names an assistant: a device thread's named executor (agent_id), a
// conversation participation (pid, maybe agent_id), or a device thread's
// default responder (defaultAssistantReaction: no ids, origin "agent:TOKEN").
export const assistantReaction = (n) => n.v === Version3 && n.sub === SubReaction && (!!n.agent_id || !!n.pid || defaultAssistantReaction(n));
export const defaultAssistantReaction = (n) => n.v === Version3 && n.sub === SubReaction && !n.conv && !n.agent_id && !n.pid && agentOrigin(n.origin);
// historyAssistantReaction is AssistantReaction of a history item's inner
// (client.HistoryItem.inner makes a control version 3): conversation only.
export const historyAssistantReaction = (h) => h.sub === SubReaction && !!h.ref && (!!h.agent_id || !!h.pid);
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

// goString is a JSON string exactly as Go's json.Marshal writes it: the
// platform's JSON.stringify (the same short escapes, lowercase \u00xx for
// the other controls) plus Go's HTML-safe escapes of <, > and & and of
// U+2028 and U+2029. Ill-formed text (a lone surrogate) is refused first,
// never escaped into a record. Proven byte-equal to Go over every code
// point and a Go-fed corpus (0930cq).
const goEscapes = new RegExp("[<>&" + String.fromCharCode(0x2028, 0x2029) + "]", "g");
export function goString(s) {
  text(s, "text");
  return JSON.stringify(s).replace(goEscapes, (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));
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
const envelopeSigned = (e) => utf8.encode((e.v === Version2 ? "agentnet-envelope-v2\n" : e.v === Version3 ? "agentnet-envelope-v3\n" : "agentnet-envelope-v1\n") +
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
  if (n.target) s += ',"target":{"address":' + goString(n.target.address) + ',"fingerprint":' + goString(n.target.fingerprint) + (n.target.agent_id ? ',"agent_id":' + goString(n.target.agent_id) : "") + (n.target.group_admission ? ',"group_admission":' + goString(n.target.group_admission) : "") + "}";
  if (n.pid) s += ',"pid":' + goString(n.pid);
  if (n.fan && n.fan.length) s += ',"fan":[' + n.fan.map((f) => '{"person":' + goString(f.person) + ',"roster":' + goString(f.roster) + "}").join(",") + "]";
  if (n.ref) s += ',"ref":{"id":' + goString(n.ref.id) + ',"fingerprint":' + goString(n.ref.fingerprint) + "}";
  if (n.agent_id) s += ',"agent_id":' + goString(n.agent_id);
  if (n.receiver_route) s += ',"receiver_route":' + receiverRouteJSON(n.receiver_route);
  if (n.human) s += ',"human":' + humanJSON(n.human);
  return s + "}";
}

const subs = new Set(["", "event", "excerpt", "history", "file", SubDriveSpace, SubGroupProof, SubGroupContext, SubGroupInvite, SubGroupConsent, SubGroupWithdrawal]);
const driveFolderPattern = /^[A-Za-z0-9_-]{1,256}$/;
// parseDriveSpace is gdrive.Space.Validate on a Drive space record's body:
// the conversation, the folder, its name, the owner (a person id, as the
// core verified it), the revision and its predecessor's folder. No tokens,
// emails or grants ever travel in it.
export function parseDriveSpace(body) {
  const r = strict(JSON.parse(body), "drive space", { conv: "string", folder: "string", name: "string", owner: "string", revision: "int", previous: "string", disconnected: "boolean" });
  if (!r.conv || r.conv.length > 256 || !r.owner || r.owner.length > 256 || !(r.revision >= 1) || !driveFolderPattern.test(r.folder || "") || !r.name || r.name.length > 255 ||
    (r.previous && !driveFolderPattern.test(r.previous))) throw new Error("invalid conversation Drive metadata");
  return { conv: r.conv, folder: r.folder, name: r.name, owner: r.owner, revision: r.revision, previous: r.previous || "", disconnected: !!r.disconnected };
}
// driveSpaceJSON marshals a space as the core does (gdrive.Space field order; omitempty).
export function driveSpaceJSON(s) {
  return '{"conv":' + goString(s.conv) + ',"folder":' + goString(s.folder) + ',"name":' + goString(s.name) + ',"owner":' + goString(s.owner) + ',"revision":' + s.revision +
    (s.previous ? ',"previous":' + goString(s.previous) : "") + (s.disconnected ? ',"disconnected":true' : "") + "}";
}

// validEmoji is envelope.ValidEmoji: one emoji, bounded, made of symbol
// runes and the joiners emoji sequences use; no letters, digits, spaces or
// controls (a keycap sequence may hold its one digit, # or *).
export function validEmoji(s) {
  if (typeof s !== "string" || !s || utf8.encode(s).length > MaxEmojiBytes || !wellFormed(s) || [...s].length > MaxEmojiRunes) return false;
  let symbol = false;
  const keycap = s.includes("\u20E3");
  for (const ch of s) {
    const r = ch.codePointAt(0);
    if (r === 0x200D || r === 0xFE0F || r === 0xFE0E || r === 0x20E3 || (r >= 0xE0020 && r <= 0xE007F) || (r >= 0x1F3FB && r <= 0x1F3FF)) continue;
    if (keycap && ((r >= 0x30 && r <= 0x39) || ch === "#" || ch === "*")) { symbol = true; continue; }
    if ((r >= 0x1F1E6 && r <= 0x1F1FF) || r >= 0x1F000 || /\p{S}/u.test(ch)) { symbol = true; continue; }
    return false;
  }
  return symbol;
}

// parseControl reads a control's payload strictly (envelope.checkVersion3):
// a reaction {emoji, op, n}, a revision {rev, text} or a retraction {reason?}.
export function parseControl(sub, body) {
  let v;
  try { v = JSON.parse(body); } catch (e) { throw new Error("malformed control payload"); }
  if (sub === SubReaction) {
    const r = strict(v, "reaction", { emoji: "string", op: "string", n: "int" });
    if (!validEmoji(r.emoji) || (r.op !== "add" && r.op !== "remove") || !(r.n >= 0)) throw new Error("malformed reaction");
    return { emoji: r.emoji, op: r.op, n: r.n || 0 };
  }
  if (sub === SubRevision) {
    const r = strict(v, "revision", { rev: "int", text: "string" });
    if (!(r.rev > 0) || typeof r.text !== "string" || !r.text.trim() || utf8.encode(r.text).length > MaxRevisionBytes || !wellFormed(r.text)) throw new Error("malformed revision");
    return { rev: r.rev, text: r.text };
  }
  if (sub === SubRetraction) {
    const r = strict(v, "retraction", { reason: "string" });
    if ((r.reason || "").length > MaxReasonBytes || (r.reason && !wellFormed(r.reason))) throw new Error("malformed retraction");
    return { reason: r.reason || "" };
  }
  if (sub === SubStatus) { // a host's word on one request: state, its own counter, when, a bounded public detail
    // A host's answer to an operator's decision echoes that decision's id,
    // report and attempt exactly (all three, or none): it is attached to
    // that report's item and never taken as the request's execution state.
    const r = strict(v, "status", { state: "string", n: "int", at: "int", detail: "string", refused: "string", decision: "string", report: "string", attempt: "int" });
    if (!ExecStates.includes(r.state) || !(r.n >= 1) || !(r.at >= 0) || utf8.encode(r.detail || "").length > MaxDetailBytes || !wellFormed(r.detail || "") ||
      utf8.encode(r.refused || "").length > MaxDetailBytes || !wellFormed(r.refused || "")) throw new Error("malformed status");
    if ((r.decision && !validID(r.decision)) || (r.report && !validID(r.report)) || (r.attempt !== undefined && !(r.attempt >= 0)) || (r.refused && !r.decision)) throw new Error("malformed status");
    return { state: r.state, n: r.n, at: r.at || 0, detail: r.detail || "", refused: r.refused || "", decision: r.decision || "", report: r.report || "", attempt: r.attempt || 0 };
  }
  if (sub === SubDecision) { // an operator's decision about one request on a host, bound to the state it saw
    const r = strict(v, "decision", { action: "string", expect: "string", attempt: "int", text: "string", report: "string" });
    if (!DecisionActions.includes(r.action) || !validStateToken(r.expect || "") || !(r.attempt >= 0) || utf8.encode(r.text || "").length > MaxDecisionText || !wellFormed(r.text || "") ||
      (r.report && !validID(r.report)) || ((r.action === "reply" || r.action === "decline") && !(r.text || "").trim())) throw new Error("malformed decision");
    return { action: r.action, expect: r.expect, attempt: r.attempt, text: r.text || "", report: r.report || "" };
  }
  if (sub === SubClear) { // envelope.Clear: exact names only (no cut, anchors or other fields)
    const r = strict(v, "conversation deletion", { deletion: "string", part: "int", parts: "int", turns: "array" });
    const turns = (r.turns || []).map(t => { const x = strict(t, "deleted turn", { id: "string", fingerprint: "string" }); return { id: x.id || "", fingerprint: x.fingerprint || "" }; });
    if (!validID(r.deletion || "") || !(r.part >= 1) || r.part > r.parts || r.parts > MaxClearParts || turns.length > MaxClearTurns || turns.some(t => !validID(t.id) || !validFingerprint(t.fingerprint))) throw new Error("malformed conversation deletion");
    return { deletion: r.deletion, part: r.part, parts: r.parts, turns };
  }
  throw new Error("unknown control " + sub);
}
// clearJSON is the Go encoding of envelope.Clear (turns omitted when empty).
export const clearJSON = (c) => '{"deletion":' + goString(c.deletion) + ',"part":' + goInt(c.part, "part") + ',"parts":' + goInt(c.parts, "parts") +
  (c.turns?.length ? ',"turns":[' + c.turns.map(t => '{"id":' + goString(t.id) + ',"fingerprint":' + goString(t.fingerprint) + "}").join(",") + "]" : "") + "}";

// checkV3 is envelope.checkVersion3: a control is a plain message about
// one exact earlier message and carries nothing of a turn.
function checkV3(n) {
  if (n.kind !== "message" || !n.ref || !validID(n.ref.id) || !validFingerprint(n.ref.fingerprint)) throw new Error("a control is a message about one exact earlier message (ref: id and sender key)");
  if (n.receiver_route || n.root || n.target || n.pid && !assistantReaction(n) || n.attachments.length || n.reply_to || n.origin && !defaultAssistantReaction(n) || n.emotion || n.status || n.session || n.fallback) {
    throw new Error("a control carries nothing but its ref and payload");
  }
  // An assistant's reaction names the assistant: a device thread's named
  // executor or default responder, or a conversation participation.
  if (assistantReaction(n) && (n.agent_id && !validID(n.agent_id) || n.pid && !validID(n.pid) || !n.conv && n.pid || n.conv && !n.pid ||
    n.origin && (n.conv || n.agent_id || !validToken(n.origin.slice(6), 32)))) {
    throw new Error("an assistant reaction names its executor or default responder (device thread) or its participation (conversation)");
  }
  if (n.sub === SubDecision && n.conv) throw new Error("a decision is device-scoped: it goes to the host that holds the request");
  if (n.sub === SubClear && !n.conv) throw new Error("malformed conversation deletion"); // a device thread is never deleted on the wire
  if (!n.conv) {
    if (n.lid || n.replica || (n.fan && n.fan.length)) throw new Error("a device-thread control has no logical id, no fan and is no replica");
  } else {
    if (!validHash(n.conv) || !validID(n.lid)) throw new Error("a conversation control names its conversation and its own logical id");
    // A conversation control may name the member rosters its copies went
    // to (as a turn does), so a device that knows a newer roster forwards it.
    if (n.fan) {
      if (n.fan.length > 2) throw new Error("a conversation message names at most its two member persons");
      n.fan.forEach((f, i) => {
        if (!validID(f.person) || !validHash(f.roster) || (i > 0 && f.person === n.fan[0].person)) throw new Error("invalid fan");
      });
    }
  }
  parseControl(n.sub, n.body);
}
const agentOrigin = (o) => typeof o === "string" && o.startsWith("agent:");
// goBlank is strings.TrimSpace(s) == "": only Unicode White_Space (not JS
// trim's U+FEFF; U+0085 included).
const goBlank = (s) => /^[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/.test(s || "");

// checkV2 is envelope.checkVersion2: the conversation fields, only in
// version 2, and their shapes. (The pid rules follow the core as it is now;
// participation is still in review there.)
async function checkV2(n) {
  // Progress replies to one request in plain text: in version 1 (a named
  // executor's progress names it), or as a participation's nonterminal output.
  if (n.status === StatusProgress && (n.v !== Version && (n.v !== Version2 || !n.pid) || n.kind !== "message" || !n.reply_to || goBlank(n.body) || n.attachments.length || n.target || n.receiver_route || (n.human && n.v !== Version2) || n.sub)) throw new Error("progress is a plain-text update replying to one request, in version 1 or as a participation's output");
  if (n.human && n.v === Version3) {
    // An assistant's reaction to an addressed request, to that request's
    // captured audience (envelope/human.go): its host is the author and a
    // control carries no root. No other control carries an audience.
    if (!assistantReaction(n) || !n.conv || !n.pid || n.human.author_pid || n.root || n.kind !== "message") throw new Error("human: on a control, only an assistant's reaction to its captured audience");
    await validateHumanTurn(n.human, n.conv);
  } else if (n.human) {
    const root = parseRoot(n.root);
    if (await rootID(root) !== n.conv || root.kind !== "dm" || root.members.length !== 2 || n.v !== Version2 || n.sub) throw new Error("human: ordinary non-executing DM turn only");
    const h = n.human, author = h.author_pid || "";
    const ordinary = n.kind === "message" && !n.status && !n.target && !n.agent_id && !agentOrigin(n.origin) && !n.emotion && n.pid === author;
    const request = ["question", "task"].includes(n.kind) && n.target && n.pid && n.pid !== author && !n.agent_id && !agentOrigin(n.origin) && !n.status && !n.receiver_route;
    const output = (["answer", "result"].includes(n.kind) || n.kind === "message" && n.status === StatusProgress) && !n.target && n.pid && !author && !n.receiver_route;
    if (!ordinary && !request && !output) throw new Error("human: ordinary turn, addressed request or assistant output only");
    await validateHumanTurn(n.human, n.conv);
  }
  await validateReceiverRoute(n);
  if (n.target?.group_admission) {
    const root = parseGroupRoot(n.root);
    if (root.kind !== "group" || !n.pid || !validHash(n.target.group_admission) || !["question", "task"].includes(n.kind)) throw new Error("group: requester admission is only for a PID-addressed group request");
  }
  if (n.agent_id && !assistantReaction(n) && (!validID(n.agent_id) || !["answer", "result"].includes(n.kind) && n.status !== StatusProgress || !n.reply_to || n.sub || n.v === Version3)) throw new Error("a named agent author belongs on a reply answer, result or progress");
  if (n.target?.agent_id && !validID(n.target.agent_id)) throw new Error("invalid named agent target");
  if (n.v === Version3) { checkV3(n); return; }
  if (n.ref) throw new Error("a control ref belongs to a version 3 message");
  if (n.v !== Version2) {
    if (n.conv || n.lid || n.root || n.sub || n.replica || n.origin || n.emotion || n.pid || n.fan) {
      throw new Error("conversation fields in a version 1 message");
    }
    if (n.target && (!n.target.agent_id || !["question", "task"].includes(n.kind) || n.target.address !== n.to || !validFingerprint(n.target.fingerprint))) throw new Error("a device message target must name an agent on its exact recipient");
    return;
  }
  if (!validHash(n.conv) || !validID(n.lid)) throw new Error("invalid conversation or logical id");
  if (!n.root || utf8.encode(n.root).length > convRootSizeLimit(n.root)) throw new Error("missing or oversized conversation root");
  if (!subs.has(n.sub)) throw new Error("unknown sub " + n.sub);
  if ([SubGroupProof, SubGroupContext, SubGroupInvite, SubGroupConsent, SubGroupWithdrawal].includes(n.sub)) {
    const root = parseGroupRoot(n.root);
    if (root.v !== GroupRootVersion || root.kind !== "group" || await rootID(root) !== n.conv || n.kind !== "message" || n.attachments.length !== 1 || n.target || (n.pid && (![SubGroupProof, SubGroupContext].includes(n.sub) || !validID(n.pid))) || n.reply_to || n.origin || n.emotion || n.status || n.fan || n.replica) throw new Error("group: carrier must be a plain group message with one attachment");
    parseGroupCarrier(n.body);
    const a = n.attachments[0], limit = [SubGroupContext, SubGroupInvite, SubGroupConsent].includes(n.sub) ? MaxGroupState : MaxBody - 1024;
    if (a.name !== n.sub + ".json" || a.size <= 0 || a.size > limit || a.blob.size > limit + (64 << 10)) throw new Error("group: carrier attachment exceeds bound");
  }
  if (n.sub === "excerpt" && (!n.pid || n.kind !== "message" || !n.replica || n.target || n.reply_to)) throw new Error("a participation excerpt is a non-executing message replica naming its PID");
  if (n.sub === SubDriveSpace) { // a plain message carrying nothing else (envelope.checkVersion2)
    if (n.kind !== "message" || n.target || n.pid || n.attachments.length || n.reply_to || n.origin || n.emotion || n.status) throw new Error("a Drive space record is a plain message carrying nothing else");
    let sp;
    try { sp = parseDriveSpace(n.body); } catch (e) { throw new Error("malformed Drive space record"); }
    if (sp.conv !== n.conv) throw new Error("malformed Drive space record");
  }
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
    const output = n.sub === "" && (n.kind === "answer" || n.kind === "result" || n.kind === "message" && n.status === StatusProgress);
    if (n.sub !== "event" && n.sub !== "excerpt" && ![SubGroupProof, SubGroupContext].includes(n.sub) && !request && !output && !(n.human && !n.sub && n.kind === "message")) throw new Error("a participation id is not allowed on this message");
  }
}

export const checkVersion2 = checkV2; // envelope.checkVersion2, for the shared Go shape vectors

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
  if ([SubGroupProof, SubGroupContext, SubGroupInvite, SubGroupConsent, SubGroupWithdrawal].includes(m.sub) && m.fan != null) throw new Error("group: carrier carries no fan");
  if (attachments.length > MaxAttachments) throw new Error("too many attachments (max " + MaxAttachments + ")");
  for (const a of attachments) {
    checkBlob(a.blob);
    if (!Number.isSafeInteger(a.size) || a.size < 0 || !sha256Pattern.test(a.sha256)) throw new Error("invalid attachment");
    text(a.name, "attachment name");
  }
  const v = m.v === Version2 ? Version2 : m.v === Version3 ? Version3 : Version;
  const inner = { v, id: m.id, from: m.from, to: m.to, ts: m.ts, kind: m.kind, body: text(m.body, "message"),
    reply_to: m.reply_to || "", attachments, session: m.session || "", fallback: !!m.fallback, status: text(m.status || "", "status"),
    conv: m.conv || "", lid: m.lid || "", root: m.root || "", sub: m.sub || "", replica: !!m.replica,
    origin: text(m.origin || "", "origin"), emotion: text(m.emotion || "", "emotion"), target: m.target || null, pid: m.pid || "",
    fan: m.fan && m.fan.length ? m.fan : null, ref: m.ref ? { id: m.ref.id, fingerprint: m.ref.fingerprint } : null, agent_id: m.agent_id || "", receiver_route: m.receiver_route ? parseReceiverRoute(m.receiver_route) : null, human: m.human ? parseHumanTurn(m.human) : null };
  await checkV2(inner);
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
  if (e.v !== Version && e.v !== Version2 && e.v !== Version3) throw new Error("unsupported envelope version " + e.v);
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
    target: "object", pid: "string", fan: "array", ref: "object", agent_id: "string", receiver_route: "object", human: "object" });
  const target = f.target ? strict(f.target, "target", { address: "string", fingerprint: "string", agent_id: "string", group_admission: "string" }) : null;
  const ref = f.ref ? strict(f.ref, "ref", { id: "string", fingerprint: "string" }) : null;
  const fan = f.fan ? f.fan.map((x) => { const y = strict(x, "fan", { person: "string", roster: "string" }); return { person: y.person || "", roster: y.roster || "" }; }) : null;
  const n = { v: f.v || 0, id: f.id || "", from: f.from || "", to: f.to || "", ts: f.ts || 0, kind: f.kind || "", body: f.body || "",
    reply_to: f.reply_to || "", session: f.session || "", fallback: !!f.fallback, status: f.status || "",
    conv: f.conv || "", lid: f.lid || "", root: f.root ? JSON.stringify(f.root) : "", sub: f.sub || "", replica: !!f.replica,
    origin: f.origin || "", emotion: f.emotion || "", pid: f.pid || "", fan,
    target: target ? { address: target.address || "", fingerprint: target.fingerprint || "", ...(target.agent_id ? { agent_id: target.agent_id } : {}), ...(target.group_admission ? { group_admission: target.group_admission } : {}) } : null,
    ...(f.agent_id ? { agent_id: f.agent_id } : {}),
    ...(f.receiver_route ? { receiver_route: parseReceiverRoute(f.receiver_route) } : {}),
    ...(f.human ? { human: parseHumanTurn(f.human) } : {}),
    ref: ref ? { id: ref.id || "", fingerprint: ref.fingerprint || "" } : null,
    attachments: (f.attachments || []).map((a) => {
      const x = strict(a, "attachment", { blob: "object", name: "string", size: "int", sha256: "string" });
      const b = strict(x.blob || {}, "attachment reference", { id: "string", size: "int", sha256: "string" });
      return { blob: { id: b.id || "", size: b.size || 0, sha256: b.sha256 || "" }, name: x.name || "", size: x.size || 0, sha256: x.sha256 || "" };
    }) };
  if (n.v !== e.v || n.id !== e.id || n.from !== e.from || n.to !== e.to || n.ts !== e.ts || n.kind !== e.kind ||
    n.session !== e.session || n.fallback !== e.fallback) {
    throw new Error("encrypted header does not match signed envelope");
  }
  await checkV2(n);
  if (n.attachments.length !== e.blobs.length) throw new Error("encrypted manifest does not match signed attachments");
  n.attachments.forEach((a, i) => {
    const b = e.blobs[i];
    if (a.blob.id !== b.id || a.blob.size !== b.size || a.blob.sha256 !== b.sha256 || a.size < 0 || a.sha256.length !== 64) {
      throw new Error("encrypted manifest does not match signed attachments");
    }
  });
  return n;
}

// ---- live typing signals (internal/protocol/signal.go) -------------------------------------
// Product wire glue over the same age and signing helpers as messages. No custody/history.
const signalDomain = "agentnet-live-signal-v1\n";
function marshalSignal(s, withSig) {
  return '{"v":' + goInt(s.v, "version") + ',"id":' + goString(s.id) + ',"from":' + goString(s.from) +
    ',"to":' + goString(s.to) + ',"ts":' + goInt(s.ts, "time") + ',"session":' + goString(s.session) +
    ',"ct":' + goBytes(s.ct) + (withSig && s.sig && s.sig.length ? ',"sig":' + goBytes(s.sig) : "") + "}";
}
export const signalJSON = (s) => marshalSignal(s, true);
export const signalCanonical = (s) => utf8.encode(signalDomain + marshalSignal(s, false));
export function parseSignal(json) {
  const f = strictRecord(json, MaxSignalBody, "signal", { v: "int", id: "string", from: "string", to: "string", ts: "int", session: "string", ct: "string", sig: "string" });
  const s = { v: f.v || 0, id: f.id || "", from: f.from || "", to: f.to || "", ts: f.ts || 0, session: f.session || "",
    ct: unb64(f.ct || "", "signal ciphertext"), sig: unb64(f.sig || "", "signal signature") };
  if (utf8.encode(signalJSON(s)).length > MaxSignalBody) throw new Error("signal: body too large");
  return s;
}
export async function verifySignal(s, key, now = Date.now()) {
  if (s.v !== 1 || !validID(s.id) || !validID(s.session) || !validAddress(s.from) || !validAddress(s.to) ||
      !(s.ct instanceof Uint8Array) || !s.ct.length || s.ct.length > MaxSignalCiphertext) throw new Error("signal: invalid shape");
  if (!Number.isSafeInteger(s.ts) || s.ts <= 0 || s.ts <= now - SignalTTL || s.ts > now + 1000) throw new Error("signal: expired or future time");
  if (!(key instanceof Uint8Array) || key.length !== 32 || !(await verifyBytes(key, signalCanonical(s), s.sig))) throw new Error("signal: signature invalid");
}
export function typingScope(scope, allowEmpty = false) {
  const f = strict(scope || {}, "typing scope", { conv: "string", peer: "string", thread: "string" });
  const conv = f.conv || "", peer = f.peer || "", thread = f.thread || "";
  if (allowEmpty && !conv && !peer && !thread) return {};
  if (conv) {
    if (!validHash(conv) || peer || thread) throw new Error("typing: invalid conversation scope");
    return { conv };
  }
  if (!validAddress(peer) || !validID(thread)) throw new Error("typing: exact peer and thread required");
  return { peer, thread };
}
function typingPlain(value) {
  const f = strict(value, "typing plaintext", { v: "int", id: "string", from: "string", to: "string", ts: "int", session: "string", realm: "string", conv: "string", thread: "string", origin: "string", active: "boolean" });
  const p = { v: f.v || 0, id: f.id || "", from: f.from || "", to: f.to || "", ts: f.ts || 0, session: f.session || "", realm: f.realm || "",
    conv: f.conv || "", thread: f.thread || "", origin: f.origin || "", active: !!f.active };
  if (p.v !== 1 || !validID(p.id) || !validID(p.session) || !validID(p.realm) || p.origin !== "human") throw new Error("typing: invalid encrypted header");
  typingScope(p.conv ? { conv: p.conv, thread: p.thread } : { peer: p.to, thread: p.thread });
  return p;
}
function typingPlainJSON(p) {
  return '{"v":' + goInt(p.v, "version") + ',"id":' + goString(p.id) + ',"from":' + goString(p.from) + ',"to":' + goString(p.to) +
    ',"ts":' + goInt(p.ts, "time") + ',"session":' + goString(p.session) + ',"realm":' + goString(p.realm) +
    (p.conv ? ',"conv":' + goString(p.conv) : "") + (p.thread ? ',"thread":' + goString(p.thread) : "") +
    ',"origin":' + goString(p.origin) + ',"active":' + String(p.active) + "}";
}
export async function sealTyping(value, keys, recipient) {
  const p = typingPlain(value);
  if (p.to !== recipient.address) throw new Error("typing: wrong recipient key");
  await checkPublic(recipient);
  const e = new Encrypter(); e.addRecipient(recipient.box_recipient);
  const ct = await e.encrypt(utf8.encode(typingPlainJSON(p)));
  if (ct.length > MaxSignalCiphertext) throw new Error("signal: ciphertext too large");
  const s = { v: p.v, id: p.id, from: p.from, to: p.to, ts: p.ts, session: p.session, ct };
  s.sig = await signBytes(keys, signalCanonical(s));
  return signalJSON(s);
}
export async function openTyping(json, keys, address, sender, realm, now = Date.now()) {
  const s = parseSignal(json);
  if (s.to !== address || s.from !== sender.address) throw new Error("signal: wrong destination or sender");
  await verifySignal(s, sender.sign_key, now);
  const d = new Decrypter(); d.addIdentity(keys.box);
  const plain = await d.decrypt(s.ct);
  if (plain.length > MaxSignalCiphertext) throw new Error("signal: invalid plaintext");
  const p = typingPlain(JSON.parse(fromUTF8.decode(plain)));
  if (["v", "id", "from", "to", "ts", "session"].some((k) => p[k] !== s[k]) || !validID(realm) || p.realm !== realm) throw new Error("signal: encrypted header mismatch");
  return p;
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
export const GroupRootVersion = 3, MaxGroupMembers = 256, MaxGroupRoot = 64 << 10;
export const MaxGroupHistory = 64, MaxGroupState = 256 << 10, MaxGroupCiphertext = 384 << 10;
const groupRootDomain = "agentnet-conv-root-v3\n";
export const convRootVersionLimit = (v) => v === GroupRootVersion ? MaxGroupRoot : MaxConvRoot;
const convRootSizeLimit = (json) => { try { return convRootVersionLimit(JSON.parse(json).v); } catch (_) { return MaxConvRoot; } };
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
export async function nextRoster(keys, address, prev, devices, join, label = prev.label) {
  const by = await fingerprint(await publicEntry(keys, address));
  if (!(await rosterHas(prev, address, by))) throw new Error("person: this device is not in the roster it would follow");
  const r = { person: prev.person, label, seq: prev.seq + 1, prev: await rosterHash(prev), devices, by, sig: null, join: join || null };
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
  if (h.target) s += ',"target":{"address":' + goString(h.target.address) + ',"fingerprint":' + goString(h.target.fingerprint) + (h.target.agent_id ? ',"agent_id":' + goString(h.target.agent_id) : "") + (h.target.group_admission ? ',"group_admission":' + goString(h.target.group_admission) : "") + "}";
  if (h.pid) s += ',"pid":' + goString(h.pid);
  if (h.attachments && h.attachments.length) {
    s += ',"attachments":[' + h.attachments.map((a) => '{"blob":{"id":"","size":0,"sha256":""},"name":' + goString(a.name) +
      ',"size":' + goInt(a.size, "size") + ',"sha256":' + goString(a.sha256) + "}").join(",") + "]";
  }
  s += ',"at":' + goInt(h.at, "time");
  if (h.ref) s += ',"ref":{"id":' + goString(h.ref.id) + ',"fingerprint":' + goString(h.ref.fingerprint) + "}";
  return s + (h.agent_id ? ',"agent_id":' + goString(h.agent_id) : "") + (h.group_admission ? ',"group_admission":' + goString(h.group_admission) : "") + (h.receiver_route ? ',"receiver_route":' + receiverRouteJSON(h.receiver_route) : "") + (h.human ? ',"human":' + humanJSON(h.human) : "") + "}";
}

// parseHistory reads a history item strictly (as the core's decodeStrict).
export function parseHistory(json) {
  const f = strict(JSON.parse(json), "history item", { v: "int", from: "string", from_key: "string", id: "string", lid: "string", ts: "int",
    kind: "string", body: "string", reply_to: "string", status: "string", sub: "string", origin: "string", emotion: "string",
    target: "object", pid: "string", attachments: "array", at: "int", ref: "object", agent_id: "string", group_admission: "string", receiver_route: "object", human: "object" });
  if (f.group_admission && !validHash(f.group_admission)) throw Error("a malformed group admission stamp");
  if (f.v !== 1 || !validID(f.id) || !validID(f.lid) || !validAddress(f.from || "") || !validFingerprint(f.from_key || "")) throw new Error("a malformed history item");
  const target = f.target ? strict(f.target, "target", { address: "string", fingerprint: "string", agent_id: "string", group_admission: "string" }) : null;
  const assistant = historyAssistantReaction(f); // a conversation assistant's own reaction: its participation, maybe its agent
  if (assistant && (!validID(f.pid || "") || f.agent_id && !validID(f.agent_id) || f.origin || target)) throw new Error("a malformed assistant reaction history item");
  if (!assistant && f.agent_id && (!validID(f.agent_id) || !["answer", "result"].includes(f.kind) || !f.reply_to || f.sub) || target?.agent_id && !validID(target.agent_id)) throw new Error("a malformed named history item");
  const ref = f.ref ? strict(f.ref, "ref", { id: "string", fingerprint: "string" }) : null;
  if (ref && (!validID(ref.id || "") || !validFingerprint(ref.fingerprint || ""))) throw new Error("a malformed history item");
  if (ref && !isControl(f.sub || "")) throw new Error("a malformed history item");
  const receiver = f.receiver_route ? parseReceiverRoute(f.receiver_route) : null;
  const human = f.human ? parseHumanTurn(f.human) : null;
  if (human) { // the same three shapes as a live human turn (envelope/human.go)
    const pid = f.pid || "", author = human.author_pid || "";
    const ordinary = f.kind === "message" && !target && !f.agent_id && !agentOrigin(f.origin) && !f.emotion && !f.status && pid === author;
    const request = ["question", "task"].includes(f.kind) && target && pid && pid !== author && !f.agent_id && !agentOrigin(f.origin) && !f.status && !receiver;
    const output = (["answer", "result"].includes(f.kind) || f.kind === "message" && f.status === StatusProgress) && !target && pid && !author && !receiver;
    const reaction = f.sub === SubReaction && historyAssistantReaction(f) && f.kind === "message" && pid && !author && !target && !receiver; // an assistant's reaction to its captured audience
    if (!reaction && (f.sub || !ordinary && !request && !output)) throw new Error("human: malformed history turn");
  }
  if (receiver && (receiver.op !== "request" || receiver.request_ref !== f.lid || f.sub || f.agent_id || ref || !["message", "question", "task"].includes(f.kind))) throw Error("receiver: history retains only original inert request routes");
  return { v: f.v, from: f.from, from_key: f.from_key, id: f.id, lid: f.lid, ts: f.ts || 0, at: f.at || 0, kind: f.kind || "", body: f.body || "",
    ref: ref ? { id: ref.id, fingerprint: ref.fingerprint } : null,
    reply_to: f.reply_to || "", status: f.status || "", sub: f.sub || "", origin: f.origin || "", emotion: f.emotion || "", pid: f.pid || "",
    target: target ? { address: target.address || "", fingerprint: target.fingerprint || "", ...(target.agent_id ? { agent_id: target.agent_id } : {}), ...(target.group_admission ? { group_admission: target.group_admission } : {}) } : null,
    ...(f.agent_id ? { agent_id: f.agent_id } : {}),
    ...(f.group_admission ? { group_admission: f.group_admission } : {}),
    ...(receiver ? { receiver_route: receiver } : {}), ...(human ? { human } : {}),
    attachments: (f.attachments || []).map((a) => { const x = strict(a, "attachment", { blob: "object", name: "string", size: "int", sha256: "string" });
      return { name: text(x.name || "", "attachment name"), size: x.size || 0, sha256: x.sha256 || "" }; }) };
}

// The existing HistoryItem is a forwarder's frozen claim, never an original
// signature or executable input. The carrier PID scopes the selected grant;
// the original message PID remains inside this history item.
export const excerptLID = async (pid, ref) => hex((await sha256(utf8.encode(pid + "\0" + ref.lid + "\0" + ref.fingerprint))).slice(0, 16));
export function parseGrantedExcerpt(n, info) {
  const h = parseHistory(n.body), raw = JSON.parse(n.body);
  if (!(h.ts > 0) || h.sub || h.ref || !["message", "question", "task", "answer", "result"].includes(h.kind) ||
      !info.grant.some((g) => g.lid === h.lid && g.fingerprint === h.from_key)) throw new Error("excerpt does not match an exact signed grant reference");
  if (h.attachments.length > 8) throw new Error("too many excerpt files");
  for (const f of raw.attachments || []) {
    const b = f.blob || {};
    if (!f.name || f.size < 0 || f.size > (100 << 20) || !validHash(f.sha256) || b.id || b.size || b.sha256 || Object.keys(b).some((k) => !["id", "size", "sha256"].includes(k))) throw new Error("invalid claimed file manifest");
  }
  const used = new Set();
  for (const f of n.attachments || []) {
    const i = h.attachments.findIndex((a, i) => !used.has(i) && a.name === f.name && a.size === f.size && a.sha256 === f.sha256);
    if (i < 0) throw new Error("excerpt carries a file outside its claimed selected turn");
    used.add(i);
  }
  return h;
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

// Exact native fileMsg group extension. The DM decoder stays unchanged.
export function groupFileMsgJSON(m) {
  let json=fileMsgJSON(m).slice(0,-1)+',"author":'+goString(m.author)+',"hash":'+goString(m.hash)+',"index":'+goInt(m.index,"index")+',"name":'+goString(m.name)+',"size":'+goInt(m.size,"size");
  if(m.group_admission)json+=',"group_admission":'+goString(m.group_admission);return json+'}';
}
export function parseGroupFileMsg(json) {
  const m=strictRecord(json,MaxBody,"group file message",{v:"int",type:"string",lid:"string",sha256:"string",available:"boolean",detail:"string",author:"string",hash:"string",index:"int",name:"string",size:"int",group_admission:"string"});
  if(m.v!==1||!["request","offer"].includes(m.type)||!validID(m.lid)||!validFingerprint(m.author)||!validHash(m.hash)||!validHash(m.sha256)||m.index==null||m.index<0||m.index>=8||!m.name||m.size==null||m.size<0||m.size>(100<<20)||m.group_admission&&!validHash(m.group_admission))throw Error("Group file lacks exact selected manifest binding.");
  return {v:1,type:m.type,lid:m.lid,sha256:m.sha256,available:!!m.available,detail:m.detail||"",author:m.author,hash:m.hash,index:m.index,name:m.name,size:m.size,group_admission:m.group_admission||""};
}

// DM root (E0).

function marshalRoot(c, withSig) {
  const cr = c.creator;
  return '{"v":' + goInt(c.v, "version") + ',"kind":' + goString(c.kind) + ',"creator":{"person":' + goString(cr.person) +
    ',"roster":' + goString(cr.roster) + ',"address":' + goString(cr.address) + ',"fingerprint":' + goString(cr.fingerprint) + "}" +
    ',"members":' + (c.members ? "[" + c.members.map((m) => '{"person":' + goString(m.person) + ',"roster":' + goString(m.roster) + "}").join(",") + "]" : "null") +
    ',"nonce":' + goString(c.nonce) + ',"created":' + goInt(c.created, "time") +
    (c.realm ? ',"realm":' + goString(c.realm) : "") + (c.title ? ',"title":' + goString(c.title) : "") +
    (c.admins?.length ? ',"admins":' + goStrings(c.admins) : "") + sigJSON(c, withSig) + "}";
}
export const rootJSON = (c) => marshalRoot(c, true);
export const rootCanonical = (c) => utf8.encode((c.v === GroupRootVersion ? groupRootDomain : rootDomain) + marshalRoot(c, false));
// rootID is the conversation id: the hash of the root's canonical bytes.
export const rootID = (c) => hashOf(rootCanonical(c));
export const rootMember = (c, person) => ((c.members || []).find((m) => m.person === person) || {}).roster;

export function validateRoot(c) {
  if (c.v === GroupRootVersion) return validateGroupRoot(c);
  if (c.v !== 2 || c.kind !== "dm") throw new Error("conversation: unsupported root");
  if (c.realm || c.title || c.admins?.length) throw new Error("conversation: a DM root carries no realm, title or admins");
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

export function parseConvRoot(json) {
  const f = strictRecord(json, MaxGroupRoot, "conversation", { v: "int", kind: "string", creator: "object", members: "array",
    nonce: "string", created: "int", realm: "string", title: "string", admins: "array", sig: "string" });
  if (typeof json === "string") fitsRecord(json, convRootVersionLimit(f.v), "conversation");
  const cr = strict(f.creator || {}, "conversation creator", { person: "string", roster: "string", address: "string", fingerprint: "string" });
  const c = { v: f.v || 0, kind: f.kind || "", nonce: f.nonce || "", created: f.created || 0, sig: f.sig ? unb64(f.sig, "root signature") : null,
    creator: { person: cr.person || "", roster: cr.roster || "", address: cr.address || "", fingerprint: cr.fingerprint || "" },
    members: f.members ? f.members.map((m) => { const x = strict(m, "conversation member", { person: "string", roster: "string" });
      return { person: x.person || "", roster: x.roster || "" }; }) : null,
    ...(f.realm ? { realm: f.realm } : {}), ...(f.title ? { title: f.title } : {}), ...(f.admins ? { admins: f.admins } : {}) };
  validateRoot(c);
  fitsRecord(rootJSON(c), convRootVersionLimit(c.v), "conversation");
  return c;
}

// Existing DM consumers stay closed to groups until effective membership and
// original public authority proof are implemented in the Engine.
export function parseRoot(json) {
  const c = parseConvRoot(json);
  if (c.v !== 2 || c.kind !== "dm") throw new Error("conversation: unsupported DM root");
  return c;
}
export function parseGroupRoot(json) {
  const c = parseConvRoot(json);
  validateGroupRoot(c);
  return c;
}

// verifyRoot checks c and that creatorKey, the creator device's key, signed it.
export async function verifyRoot(c, creatorKey) {
  validateRoot(c);
  if (!(await verifyBytes(creatorKey, rootCanonical(c), c.sig))) throw new Error("conversation: root signature invalid");
}

// ---- groups: signed public authority and current state (no installation) ----------
// resolve(person, hash) returns an independently verified pinned roster step.
// A verified proof result is only codec evidence: no membership or execution grant.
const groupDomain = "agentnet-group-state-v1\n", groupAdmissionDomain = "agentnet-group-admission-v1\n";
const groupWithdrawalDomain = "agentnet-group-withdrawal-v1\n", groupCommitDomain = "agentnet-group-commit-v1\n";
const groupArray = (a, fn) => a ? "[" + a.map(fn).join(",") + "]" : "null";
const groupSig = (r) => r.sig ? unb64(r.sig, "group signature") : null;
const sameStrings = (a, b) => (a || []).length === (b || []).length && (a || []).every((x, i) => x === b[i]);
const groupSeq = (seq, prev) => Number.isSafeInteger(seq) && seq >= 0 && (seq === 0 ? prev === "" : validHash(prev));

export function validateGroupRoot(c) {
  if (c.v !== GroupRootVersion || c.kind !== "group" || !validID(c.realm) || !validID(c.nonce) || !Number.isSafeInteger(c.created) || c.created <= 0) throw new Error("group root: invalid version, realm, title or nonce");
  validLabel(c.title);
  const cr = c.creator;
  if (!cr || !validID(cr.person) || !validHash(cr.roster) || !validFingerprint(cr.fingerprint) || !validAddress(cr.address)) throw new Error("group root: invalid creator");
  if (!c.members?.length || c.members.length > MaxGroupMembers || !c.admins?.length || c.admins.length > c.members.length) throw new Error("group root: members and admin required");
  let previous = "";
  for (const m of c.members) { if (!validID(m.person) || !validHash(m.roster) || m.person <= previous) throw new Error("group root: distinct sorted person members required"); previous = m.person; }
  if (rootMember(c, cr.person) !== cr.roster) throw new Error("group root: creator must be a bound member");
  previous = "";
  for (const a of c.admins) { if (!rootMember(c, a) || a <= previous) throw new Error("group root: distinct sorted member admins required"); previous = a; }
  if (!c.admins.includes(cr.person)) throw new Error("group root: creator must be admin");
  fitsRecord(marshalRoot(c, false), MaxGroupRoot - utf8.encode(groupRootDomain).length, "group root");
}
export async function signGroupRoot(keys, c) { validateGroupRoot(c); return { ...c, sig: await signBytes(keys, rootCanonical(c)) }; }

function marshalGroupAdmission(a, signed) {
  return '{"conv":' + goString(a.conv) + ',"realm":' + goString(a.realm) + ',"person":' + goString(a.person) + ',"roster":' + goString(a.roster) +
    ',"seq":' + goInt(a.seq, "seq") + ',"prev":' + goString(a.prev) + ',"history":' + groupArray(a.history, r => '{"lid":' + goString(r.lid) + ',"author":' + goString(r.author) + ',"hash":' + goString(r.hash) + "}") + ',"by":' + goString(a.by) + sigJSON(a, signed) + "}";
}
export const groupAdmissionJSON = (a) => marshalGroupAdmission(a, true);
export const groupAdmissionCanonical = (a) => utf8.encode(groupAdmissionDomain + marshalGroupAdmission(a, false));
export const groupAdmissionHash = (a) => hashOf(groupAdmissionCanonical(a));
export function validateGroupAdmission(a) {
  if (!validHash(a.conv) || !validID(a.realm) || !validID(a.person) || !validHash(a.roster) || !validFingerprint(a.by) || !groupSeq(a.seq, a.prev) || (a.history?.length || 0) > MaxGroupHistory) throw new Error("group admission: invalid binding");
  const refs = new Set();
  for (const r of a.history || []) { const key = r.author + ":" + r.lid; if (!validID(r.lid) || !validFingerprint(r.author) || !validHash(r.hash) || refs.has(key)) throw new Error("group admission: invalid selected history"); refs.add(key); }
}
export function parseGroupAdmission(json) {
  const f = strictRecord(json, MaxGroupState, "group admission", { conv: "string", realm: "string", person: "string", roster: "string", seq: "int", prev: "string", history: "array", by: "string", sig: "string" });
  const a = { conv: f.conv || "", realm: f.realm || "", person: f.person || "", roster: f.roster || "", seq: f.seq || 0, prev: f.prev || "", history: f.history ? f.history.map(r => { const x = strict(r, "group history reference", { lid: "string", author: "string", hash: "string" }); return { lid: x.lid || "", author: x.author || "", hash: x.hash || "" }; }) : null, by: f.by || "", sig: groupSig(f) };
  validateGroupAdmission(a); return a;
}
async function pinnedGroupRoster(resolve, person, hash) {
  const r = await resolve(person, hash);
  if (!r || r.person !== person || await rosterHash(r) !== hash) throw new Error("group: independently verified person chain required");
  return r;
}
export async function verifyGroupAdmission(a, resolve) {
  validateGroupAdmission(a);
  const r = await pinnedGroupRoster(resolve, a.person, a.roster), signer = await rosterDevice(r, a.by);
  if (!signer || !await verifyBytes(signer.sign_key, groupAdmissionCanonical(a), a.sig)) throw new Error("group admission: explicit member consent invalid");
}
export async function signGroupAdmission(keys, a) { validateGroupAdmission(a); return { ...a, sig: await signBytes(keys, groupAdmissionCanonical(a)) }; }
export const groupAllowsHistory = (a, r) => (a.history || []).some(x => x.lid === r.lid && x.author === r.author && x.hash === r.hash);

function marshalGroupState(s, signed) {
  return '{"v":' + goInt(s.v, "version") + ',"conv":' + goString(s.conv) + ',"realm":' + goString(s.realm) + ',"seq":' + goInt(s.seq, "seq") + ',"prev":' + goString(s.prev) + ',"title":' + goString(s.title) +
    ',"members":' + groupArray(s.members, m => '{"person":' + goString(m.person) + ',"roster":' + goString(m.roster) + ',"admin":' + (m.admin ? "true" : "false") + ',"admission":' + groupAdmissionJSON(m.admission) + "}") +
    ',"actor":' + goString(s.actor) + ',"actor_roster":' + goString(s.actor_roster) + ',"by":' + goString(s.by) + sigJSON(s, signed) + "}";
}
export const groupStateJSON = (s) => marshalGroupState(s, true);
export const groupStateCanonical = (s) => utf8.encode(groupDomain + marshalGroupState(s, false));
export const groupStateHash = (s) => hashOf(groupStateCanonical(s));
export const groupMember = (s, person) => (s.members || []).find(m => m.person === person);
export const groupAdmins = (s) => (s.members || []).filter(m => m.admin).map(m => m.person);
export function validateGroupState(s) {
  if (s.v !== 1 || !validHash(s.conv) || !validID(s.realm) || !groupSeq(s.seq, s.prev) || !validID(s.actor) || !validHash(s.actor_roster) || !validFingerprint(s.by)) throw new Error("group: invalid identity or title");
  validLabel(s.title);
  if (!s.members?.length || s.members.length > MaxGroupMembers || !groupAdmins(s).length) throw new Error("group: explicit remaining admin required");
  let previous = "";
  for (const m of s.members) {
    const a = m.admission;
    if (!validID(m.person) || !validHash(m.roster) || m.person <= previous || typeof m.admin !== "boolean" || !a || a.person !== m.person || a.roster !== m.roster || a.conv !== s.conv || a.realm !== s.realm || a.seq > s.seq) throw new Error("group: invalid member admission");
    validateGroupAdmission(a); previous = m.person;
  }
  fitsRecord(marshalGroupState(s, false), MaxGroupState - utf8.encode(groupDomain).length, "group state");
}
export function parseGroupState(json) {
  const f = strictRecord(json, MaxGroupState, "group state", { v: "int", conv: "string", realm: "string", seq: "int", prev: "string", title: "string", members: "array", actor: "string", actor_roster: "string", by: "string", sig: "string" });
  const s = { v: f.v || 0, conv: f.conv || "", realm: f.realm || "", seq: f.seq || 0, prev: f.prev || "", title: f.title || "", members: f.members ? f.members.map(m => { const x = strict(m, "group member", { person: "string", roster: "string", admin: "boolean", admission: "object" }); return { person: x.person || "", roster: x.roster || "", admin: !!x.admin, admission: parseGroupAdmission(x.admission || {}) }; }) : null, actor: f.actor || "", actor_roster: f.actor_roster || "", by: f.by || "", sig: groupSig(f) };
  validateGroupState(s); return s;
}
async function verifyGroupSigned(s, root, resolve) {
  validateGroupState(s);
  if (root.v !== GroupRootVersion || await rootID(root) !== s.conv || root.realm !== s.realm) throw new Error("group: foreign root");
  const creator = await pinnedGroupRoster(resolve, root.creator.person, root.creator.roster), device = await rosterDevice(creator, root.creator.fingerprint);
  if (!device || device.address !== root.creator.address) throw new Error("group: original root signature invalid");
  await verifyRoot(root, device.sign_key);
  for (const m of s.members) await verifyGroupAdmission(m.admission, resolve);
  const actor = await pinnedGroupRoster(resolve, s.actor, s.actor_roster), signer = await rosterDevice(actor, s.by);
  if (!signer || !await verifyBytes(signer.sign_key, groupStateCanonical(s), s.sig)) throw new Error("group: invalid admin signature");
}
export const groupWithdrawn = async (s, m, withdrawals) => { const hash = await groupAdmissionHash(m.admission); return (withdrawals || []).some(w => w.conv === s.conv && w.realm === s.realm && w.person === m.person && w.admission === hash); };
export async function effectiveGroupMembers(s, withdrawals) { const out = []; for (const m of s.members) if (!await groupWithdrawn(s, m, withdrawals)) out.push(m); return out; }
export async function verifyGroupState(s, root, previous, resolve, withdrawals = []) {
  await verifyGroupSigned(s, root, resolve);
  if (!previous) {
    if (s.seq !== 0 || s.title !== root.title || !sameStrings(groupAdmins(s), root.admins) || s.members.length !== root.members.length || s.actor !== root.creator.person || s.by !== root.creator.fingerprint || s.actor_roster !== root.creator.roster) throw new Error("group: first state does not match signed root");
    for (let i = 0; i < s.members.length; i++) { const m = s.members[i], r = root.members[i]; if (m.person !== r.person || m.roster !== r.roster || m.admission.seq !== 0 || m.admission.prev !== "") throw new Error("group: root membership mismatch"); }
    return;
  }
  if (s.conv !== previous.conv || s.realm !== previous.realm || s.seq !== previous.seq + 1 || s.prev !== await groupStateHash(previous)) throw new Error("group: stale transition");
  if (!groupMember(previous, s.actor)?.admin) throw new Error("group: actor not a previous admin");
  for (const m of s.members) {
    if (m.admin && await groupWithdrawn(s, m, withdrawals)) throw new Error("group: withdrawn admission cannot become admin");
    const old = groupMember(previous, m.person), fresh = !old || await groupAdmissionHash(old.admission) !== await groupAdmissionHash(m.admission);
    if (fresh && (m.admission.seq !== s.seq || m.admission.prev !== s.prev)) throw new Error("group: new admission must consent to this exact transition");
    if (old && fresh && !await groupWithdrawn(previous, old, withdrawals)) throw new Error("group: active member admission cannot be replaced silently");
  }
}
export async function signGroupState(keys, s) { validateGroupState(s); return { ...s, sig: await signBytes(keys, groupStateCanonical(s)) }; }

function marshalGroupWithdrawal(w, signed) { return '{"conv":' + goString(w.conv) + ',"realm":' + goString(w.realm) + ',"person":' + goString(w.person) + ',"admission":' + goString(w.admission) + ',"roster":' + goString(w.roster) + ',"by":' + goString(w.by) + sigJSON(w, signed) + "}"; }
export const groupWithdrawalJSON = (w) => marshalGroupWithdrawal(w, true);
export const groupWithdrawalCanonical = (w) => utf8.encode(groupWithdrawalDomain + marshalGroupWithdrawal(w, false));
export function parseGroupWithdrawal(json) { const f = strictRecord(json, MaxGroupState, "group withdrawal", { conv: "string", realm: "string", person: "string", admission: "string", roster: "string", by: "string", sig: "string" }); return { conv: f.conv || "", realm: f.realm || "", person: f.person || "", admission: f.admission || "", roster: f.roster || "", by: f.by || "", sig: groupSig(f) }; }
export async function verifyGroupWithdrawal(w, s, resolve) {
  if (w.conv !== s.conv || w.realm !== s.realm || !validHash(w.admission) || !validID(w.person) || !validHash(w.roster) || !validFingerprint(w.by)) throw new Error("group withdrawal: invalid admission binding");
  const m = groupMember(s, w.person);
  if (!m || m.admin || await groupAdmissionHash(m.admission) !== w.admission) throw new Error("group withdrawal: only own ordinary admission may leave");
  await verifyGroupWithdrawalSignature(w, resolve);
}
// Signature/binding only; caller pins the current roster. This grants no ordinary-member leave authority.
export async function verifyGroupWithdrawalSignature(w, resolve) {
  if (!validHash(w.conv) || !validID(w.realm) || !validHash(w.admission) || !validID(w.person) || !validHash(w.roster) || !validFingerprint(w.by)) throw new Error("group withdrawal: invalid admission binding");
  const roster = await pinnedGroupRoster(resolve, w.person, w.roster), signer = await rosterDevice(roster, w.by);
  if (!signer || !await verifyBytes(signer.sign_key, groupWithdrawalCanonical(w), w.sig)) throw new Error("group withdrawal: invalid self signature");
}
export async function signGroupWithdrawal(keys, w) { return { ...w, sig: await signBytes(keys, groupWithdrawalCanonical(w)) }; }

function marshalGroupCommit(c, signed) {
  return '{"bootstrap":' + goString(c.bootstrap) + ',"v":' + goInt(c.v, "version") + ',"conv":' + goString(c.conv) + ',"realm":' + goString(c.realm) + ',"seq":' + goInt(c.seq, "seq") + ',"prev":' + goString(c.prev) + ',"hash":' + goString(c.hash) + ',"admins":' + goStrings(c.admins) + ',"writer":' + goString(c.writer) + ',"actor":' + goString(c.actor) + ',"actor_roster":' + goString(c.actor_roster) + ',"ciphertext":' + goBytes(c.ciphertext) + sigJSON(c, signed) + "}";
}
export const groupCommitJSON = (c) => marshalGroupCommit(c, true);
export const groupCommitCanonical = (c) => utf8.encode(groupCommitDomain + marshalGroupCommit(c, false));
export function validateGroupCommit(c) {
  if (!validFingerprint(c.bootstrap) || c.v !== 1 || !validID(c.actor) || !validHash(c.actor_roster) || !validHash(c.conv) || !validID(c.realm) || !validHash(c.hash) || !groupSeq(c.seq, c.prev) || !c.admins?.length || c.admins.length > MaxGroupMembers || !(c.ciphertext instanceof Uint8Array) || !c.ciphertext.length || c.ciphertext.length > MaxGroupCiphertext || !validAddress(c.writer)) throw new Error("group commit: invalid identity or bounds");
  let previous = ""; for (const p of c.admins) { if (!validID(p) || p <= previous) throw new Error("group commit: distinct sorted admin persons required"); previous = p; }
}
export function parseGroupCommit(json) {
  const f = strictRecord(json, MaxBody, "group commit", { bootstrap: "string", v: "int", conv: "string", realm: "string", seq: "int", prev: "string", hash: "string", admins: "array", writer: "string", actor: "string", actor_roster: "string", ciphertext: "string", sig: "string" });
  const c = { bootstrap: f.bootstrap || "", v: f.v || 0, conv: f.conv || "", realm: f.realm || "", seq: f.seq || 0, prev: f.prev || "", hash: f.hash || "", admins: f.admins || null, writer: f.writer || "", actor: f.actor || "", actor_roster: f.actor_roster || "", ciphertext: f.ciphertext ? unb64(f.ciphertext, "group ciphertext") : null, sig: groupSig(f) };
  validateGroupCommit(c); return c;
}
export async function verifyGroupCommit(c, writer) { validateGroupCommit(c); if (writer.address !== c.writer) throw new Error("group commit: invalid writer signature"); await checkPublic(writer); if (!await verifyBytes(writer.sign_key, groupCommitCanonical(c), c.sig)) throw new Error("group commit: invalid writer signature"); }
export async function signGroupCommit(keys, c) { validateGroupCommit(c); return { ...c, sig: await signBytes(keys, groupCommitCanonical(c)) }; }
export async function groupCommitMatches(c, s) { return c.conv === s.conv && c.realm === s.realm && c.seq === s.seq && c.prev === s.prev && c.hash === await groupStateHash(s) && sameStrings(c.admins, groupAdmins(s)) && c.actor === s.actor && c.actor_roster === s.actor_roster; }
export async function verifyGroupCommitChain(c, root, previous, resolve) {
  const roster = await pinnedGroupRoster(resolve, c.actor, c.actor_roster), writer = roster.devices.find(d => d.address === c.writer);
  if (!writer) throw new Error("group commit: writer absent from actor roster");
  await verifyGroupCommit(c, writer);
  if (c.bootstrap !== root.creator.fingerprint || c.conv !== await rootID(root) || c.realm !== root.realm) throw new Error("group commit: foreign root");
  if (!previous) {
    if (c.seq !== 0 || c.actor !== root.creator.person || c.actor_roster !== root.creator.roster || c.writer !== root.creator.address || await fingerprint(writer) !== root.creator.fingerprint || !sameStrings(c.admins, root.admins)) throw new Error("group commit: root authority mismatch");
    await verifyRoot(root, writer.sign_key);
  } else if (c.seq !== previous.seq + 1 || c.prev !== previous.hash || c.conv !== previous.conv || c.bootstrap !== previous.bootstrap || c.realm !== previous.realm || !previous.admins.includes(c.actor)) throw new Error("group commit: stale or unauthorized append");
}
// authority and slot() must come from independently verified original public
// proof (verifyGroupProofPage), not an unverified relay head or opaque snapshot.
export async function verifyGroupCurrent(s, root, authority, resolve, slot, withdrawals = []) {
  await verifyGroupSigned(s, root, resolve);
  if (!await groupCommitMatches(authority, s) || authority.bootstrap !== root.creator.fingerprint) throw new Error("group: current authority mismatch");
  if (s.seq === 0) return verifyGroupState(s, root, null, resolve, withdrawals);
  for (const m of s.members) {
    const a = m.admission, record = await slot(a.seq);
    if (!record || record.conv !== s.conv || record.realm !== s.realm || record.bootstrap !== root.creator.fingerprint || record.seq !== a.seq || record.prev !== a.prev) throw new Error("group: admission lacks exact verified authority slot");
    if (a.seq === 0 && rootMember(root, m.person) !== m.roster) throw new Error("group: initial admission absent from root");
    if (m.admin && await groupWithdrawn(s, m, withdrawals)) throw new Error("group: withdrawn admission cannot become admin");
  }
}
export const groupJournalJSON = (p) => '{"records":' + groupArray(p.records, groupCommitJSON) + ',"more":' + (p.more ? "true" : "false") + "}";
export function parseGroupJournal(json) { const f = strictRecord(json, MaxBody - 1024, "group proof page", { records: "array", more: "boolean" }); const p = { records: f.records ? f.records.map(parseGroupCommit) : null, more: !!f.more }; if ((p.records?.length || 0) > 16 || p.more && !p.records?.length) throw new Error("group: proof page exceeds bound or empty continuation"); fitsRecord(groupJournalJSON(p), MaxBody - 1024, "group proof page"); return p; }
export async function verifyGroupProofPage(root, page, resolve, realm, { previous = null, known = () => null } = {}) {
  validateGroupRoot(root);
  if (root.realm !== realm || !validID(realm)) throw new Error("group: foreign proof realm");
  parseGroupJournal(groupJournalJSON(page));
  const creator = await pinnedGroupRoster(resolve, root.creator.person, root.creator.roster), device = await rosterDevice(creator, root.creator.fingerprint);
  if (!device || device.address !== root.creator.address) throw new Error("group: proof creator mismatch");
  await verifyRoot(root, device.sign_key);
  // previous/known come from an already verified contiguous prefix. Bind that
  // prefix to this root even on an empty root-only page; it grants no state.
  if (previous && (previous.conv !== await rootID(root) || previous.bootstrap !== root.creator.fingerprint || previous.realm !== root.realm)) throw new Error("group: foreign proof predecessor");
  let head = previous;
  for (let i = 0; i < (page.records || []).length; i++) {
    const c = page.records[i]; validateGroupCommit(c);
    if (c.conv !== await rootID(root) || c.bootstrap !== root.creator.fingerprint || c.realm !== root.realm || i > 0 && c.seq !== page.records[i - 1].seq + 1) throw new Error("group: proof page root or sequence mismatch");
    if (head && c.seq <= head.seq) { const old = await known(c.seq); if (!old || groupCommitJSON(c) !== groupCommitJSON(old)) throw new Error("group: conflicting original proof record"); continue; }
    await verifyGroupCommitChain(c, root, head, resolve); head = c;
  }
  return head;
}

export const groupContextJSON = (p) => '{"root":' + rootJSON(p.root) + ',"proof":' + groupArray(p.proof, groupStateJSON) + ',"state":' + groupStateJSON(p.state) + ',"withdrawals":' + groupArray(p.withdrawals, groupWithdrawalJSON) + "}";
// Exact json.Marshal field order from protocol.GroupInvitation/GroupConsent.
export const groupInvitationJSON = p => '{"v":' + goInt(p.v,"version") + ',"root":' + rootJSON(p.root) + ',"state":' + groupStateJSON(p.state) + ',"withdrawals":' + groupArray(p.withdrawals,groupWithdrawalJSON) + ',"target":' + goString(p.target) + ',"roster":' + goString(p.roster) + ',"seq":' + goInt(p.seq,"seq") + ',"prev":' + goString(p.prev) + ',"history":' + groupArray(p.history,r => '{"lid":'+goString(r.lid)+',"author":'+goString(r.author)+',"hash":'+goString(r.hash)+'}') + '}';
export const groupInvitationID = p => hashOf(utf8.encode("agentnet-group-invitation-v1\n" + groupInvitationJSON(p)));
// client.contentHash: attachment descriptors omit per-recipient ciphertext.
// Exact Go struct field order/null bytes are required by signed history refs.
export function groupHistoryContentHash(conv,n) {
  let json='{"Conv":'+goString(conv)+',"LID":'+goString(n.lid)+',"Kind":'+goString(n.kind)+',"Body":'+goString(n.body || "")+',"ReplyTo":'+goString(n.reply_to || "")+',"Status":'+goString(n.status || "")+',"Sub":'+goString(n.sub || "")+',"Origin":'+goString(n.origin || "")+',"Emotion":'+goString(n.emotion || "")+',"Target":';
  json+=n.target ? '{"address":'+goString(n.target.address)+',"fingerprint":'+goString(n.target.fingerprint)+(n.target.agent_id?',"agent_id":'+goString(n.target.agent_id):'')+(n.target.group_admission?',"group_admission":'+goString(n.target.group_admission):'')+'}' : 'null';
  json+=',"Attachments":'+((n.attachments || []).length ? '['+n.attachments.map(a=>'{"Name":'+goString(a.name)+',"Size":'+goInt(a.size,"size")+',"SHA256":'+goString(a.sha256)+'}').join(',')+']' : 'null');
  if(n.pid)json+=',"PID":'+goString(n.pid);if(n.agent_id)json+=',"AgentID":'+goString(n.agent_id);
  if(n.receiver_route)json+=',"ReceiverRoute":'+receiverRouteJSON(n.receiver_route);
  if(n.human)json+=',"Human":'+humanJSON(n.human);
  return hashOf(utf8.encode(json+'}'));
}
export function parseGroupInvitation(json) {
  const f = strictRecord(json,MaxGroupState,"group invitation",{v:"int",root:"object",state:"object",withdrawals:"array",target:"string",roster:"string",seq:"int",prev:"string",history:"array"});
  const p = {v:f.v || 0,root:parseGroupRoot(f.root || {}),state:parseGroupState(f.state || {}),withdrawals:f.withdrawals ? f.withdrawals.map(parseGroupWithdrawal) : null,target:f.target || "",roster:f.roster || "",seq:f.seq || 0,prev:f.prev || "",history:f.history ? f.history.map(r => {const x=strict(r,"group history ref",{lid:"string",author:"string",hash:"string"});return {lid:x.lid || "",author:x.author || "",hash:x.hash || ""};}) : null};
  if (p.v!==1 || !validID(p.target) || !validHash(p.roster) || p.state.realm!==p.root.realm || p.seq!==p.state.seq+1) throw Error("group: invalid invitation binding");
  validateGroupAdmission({conv:p.state.conv,realm:p.root.realm,person:p.target,roster:p.roster,seq:p.seq,prev:p.prev,history:p.history,by:p.root.creator.fingerprint});
  fitsRecord(groupInvitationJSON(p),MaxGroupState,"group invitation"); return p;
}
export async function validateGroupInvitation(p) {
  if (p.state.conv!==await rootID(p.root) || p.prev!==await groupStateHash(p.state)) throw Error("group: invalid invitation binding");
  parseGroupInvitation(groupInvitationJSON(p)); return p;
}
export const groupConsentJSON = c => '{"v":'+goInt(c.v,"version")+',"invitation":'+goString(c.invitation)+',"decision":'+goString(c.decision)+(c.admission ? ',"admission":'+groupAdmissionJSON(c.admission) : '')+'}';
export function parseGroupConsent(json) {
  const f = strictRecord(json,MaxGroupState,"group consent",{v:"int",invitation:"string",decision:"string",admission:"object"});
  const c={v:f.v || 0,invitation:f.invitation || "",decision:f.decision || "",...(f.admission ? {admission:parseGroupAdmission(f.admission)} : {})};
  if(c.v!==1 || !validHash(c.invitation) || !(c.decision==="declined"&&!c.admission || c.decision==="accepted"&&c.admission)) throw Error("group: explicit accept or decline required");
  if(c.admission) validateGroupAdmission(c.admission); return c;
}
export function parseGroupContext(json) {
  const f = strictRecord(json, MaxGroupState, "group context", { root: "object", proof: "array", state: "object", withdrawals: "array" });
  const p = { root: parseGroupRoot(f.root || {}), proof: f.proof ? f.proof.map(parseGroupState) : null, state: parseGroupState(f.state || {}), withdrawals: f.withdrawals ? f.withdrawals.map(parseGroupWithdrawal) : null };
  if ((p.proof?.length || 0) > 4096) throw new Error("group: authority proof exceeds bound"); fitsRecord(groupContextJSON(p), MaxGroupState, "group context"); return p;
}
export async function verifyGroupContext(p, resolve) {
  if ((p.proof?.length || 0) > 4096) throw new Error("group: authority proof exceeds bound"); fitsRecord(groupContextJSON(p), MaxGroupState, "group context"); validateGroupRoot(p.root);
  const states = [...(p.proof || []), p.state];
  for (const w of p.withdrawals || []) { let valid = false; for (const s of states) { try { await verifyGroupWithdrawal(w, s, resolve); valid = true; break; } catch (_) {} } if (!valid) throw new Error("group: withdrawal lacks verified own admission"); }
  let previous = null; for (const s of states) { await verifyGroupState(s, p.root, previous, resolve, p.withdrawals || []); previous = s; }
}
export const groupCarrierJSON = (c) => '{"v":' + goInt(c.v, "version") + ',"seq":' + goInt(c.seq, "seq") + ',"hash":' + goString(c.hash) + ',"to_key":' + goString(c.to_key) + "}";
export function parseGroupCarrier(json) { const f = strictRecord(json, MaxBody, "group carrier", { v: "int", seq: "int", hash: "string", to_key: "string" }); const c = { v: f.v || 0, seq: f.seq || 0, hash: f.hash || "", to_key: f.to_key || "" }; if (c.v !== 1 || c.seq < 0 || !validHash(c.hash) || !validFingerprint(c.to_key)) throw new Error("group: invalid carrier descriptor"); return c; }

// Host-signed named agents: public identities only, never local programs or grants.
export const CapAgentIdentity = "agi1", MaxAgentCatalog = 32;
const agentDomain = "agentnet-agent-v1\n";
function marshalAgent(r, withSig) {
  return '{"v":' + goInt(r.v, "version") + ',"id":' + goString(r.id) + ',"host":' + goString(r.host) +
    ',"host_key":' + goString(r.host_key) + ',"label":' + goString(r.label) + ',"ts":' + goInt(r.ts, "time") + sigJSON(r, withSig) + "}";
}
export const agentJSON = (r) => marshalAgent(r, true);
export const agentCanonical = (r) => utf8.encode(agentDomain + marshalAgent(r, false));
export const agentHash = (r) => hashOf(agentCanonical(r));
export function validateAgent(r) {
  if (r.v !== 1 || !validID(r.id) || !validAddress(r.host) || !validFingerprint(r.host_key) || !(r.ts > 0)) throw new Error("agent: invalid identity, host key or timestamp");
  validLabel(r.label);
}
export function parseAgentRecord(json) {
  const f = strictRecord(json, 8192, "agent", { v: "int", id: "string", host: "string", host_key: "string", label: "string", ts: "int", sig: "string" });
  const r = { v: f.v || 0, id: f.id || "", host: f.host || "", host_key: f.host_key || "", label: f.label || "", ts: f.ts || 0, sig: f.sig ? unb64(f.sig, "agent signature") : null };
  validateAgent(r); fitsRecord(agentJSON(r), 8192, "agent");
  return r;
}
export async function signAgent(keys, fields) {
  const r = { v: 1, ...fields };
  validateAgent(r); r.sig = await signBytes(keys, agentCanonical(r));
  return r;
}
export async function verifyAgent(r, host) {
  validateAgent(r); await checkPublic(host);
  if (host.address !== r.host || await fingerprint(host) !== r.host_key || !await verifyBytes(host.sign_key, agentCanonical(r), r.sig)) throw new Error("agent: identity is not signed by its exact host device");
}
// The durable outbox keeps this requirement even when ciphertext cannot be reopened.
export function agentRequirement(n) {
  try {
    if (n.sub === "excerpt" && n.pid) return CapExternalParticipation;
    if (n.sub === "history") n = parseHistory(n.body);
    if (assistantReaction(n) || historyAssistantReaction(n)) return CapAgentReaction; // as history too: never stored by an older reader as its host's mark
    if (n.sub === "excerpt" && n.pid) return CapExternalParticipation;
    if (n.human) return CapHumanParticipation;
    if (n.agent_id || n.target?.agent_id) return CapAgentIdentity;
    if (n.sub === "event") {
      const e = parseEvent(n.body);
      if (e.role === "human" || e.type === "scope") return CapHumanParticipation; // a scope exists only for human audiences
      if (e.host?.agent_id) return CapAgentIdentity;
    }
  } catch (e) { /* malformed bodies are rejected by their existing admission path */ }
  return "";
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
const eventTypes = new Set(["invite", "accept", "decline", "dismiss", "scope"]);
// Go's unicode.IsPrint, plus a newline (an invite's note).
const noteText = /^[\p{L}\p{M}\p{N}\p{P}\p{S} \n]*$/u;

function marshalEvent(e, withSig) {
  const a = e.author;
  let s = '{"v":' + goInt(e.v, "version") + ',"conv":' + goString(e.conv) + ',"pid":' + goString(e.pid) + ',"type":' + goString(e.type) +
    ',"prev":' + goString(e.prev) + ',"author":{"person":' + goString(a.person) + ',"roster":' + goString(a.roster) +
    ',"address":' + goString(a.address) + ',"fingerprint":' + goString(a.fingerprint) + (a.group_admission ? ',"group_admission":' + goString(a.group_admission) : "") + '},"ts":' + goInt(e.ts, "time");
  if (e.host) s += ',"host":{"person":' + goString(e.host.person) + ',"address":' + goString(e.host.address) + ',"fingerprint":' + goString(e.host.fingerprint) + (e.host.agent_id ? ',"agent_id":' + goString(e.host.agent_id) : "") + "}";
  if (e.grant && e.grant.length) s += ',"grant":[' + e.grant.map((g) => '{"lid":' + goString(g.lid) + ',"fingerprint":' + goString(g.fingerprint) + "}").join(",") + "]";
  if (e.audience) s += ',"audience":' + goString(e.audience);
  if (e.task_keys && e.task_keys.length) s += ',"task_keys":' + goStrings(e.task_keys);
  if (e.note) s += ',"note":' + goString(e.note);
  if (e.group) s += ',"group":{"seq":' + goInt(e.group.seq,"group sequence") + ',"hash":' + goString(e.group.hash) + ',"host_role":' + goString(e.group.host_role) + (e.group.host_admission ? ',"host_admission":' + goString(e.group.host_admission) : "") + (e.group.task_admissions?.length ? ',"task_admissions":' + goStrings(e.group.task_admissions) : "") + "}";
  if (e.role) s += ',"role":' + goString(e.role);
  return s + sigJSON(e, withSig) + "}";
}
export const eventJSON = (e) => marshalEvent(e, true);
export const eventCanonical = (e) => utf8.encode(participationDomain + marshalEvent(e, false));
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
  if (a.group_admission && !validHash(a.group_admission)) throw new Error("participation: invalid author group admission");
  if (!validAddress(a.address)) throw new Error("participation: invalid address " + a.address);
  if (!eventTypes.has(e.type)) throw new Error("participation: unknown event type " + e.type);
  if (e.type === "invite") {
    if (e.role && e.role !== "human") throw new Error("participation: unknown role");
    if (e.role === "human" && (e.host?.agent_id || e.task_keys?.length || e.group || a.group_admission)) throw new Error("participation: human invite carries no agent or group authority");
    if (e.prev !== "" || !e.host || e.audience !== "conversation") {
      throw new Error("participation: an invite has no prev, and names a host and the conversation audience");
    }
    if (!validID(e.host.person) || !validFingerprint(e.host.fingerprint) || e.host.agent_id && !validID(e.host.agent_id)) throw new Error("participation: invalid host");
    if (!validAddress(e.host.address)) throw new Error("participation: host: invalid address " + e.host.address);
    for (const g of e.grant || []) if (!validID(g.lid) || !validFingerprint(g.fingerprint)) throw new Error("participation: grant: invalid message reference");
    uniqueList((e.grant || []).map((g) => g.lid + "/" + g.fingerprint), MaxGrant, () => true, "grant");
    uniqueList(e.task_keys || [], MaxTaskKeys, validFingerprint, "task keys");
    if (e.group) {
      const g=e.group;
      if (!(g.seq>=0) || !validHash(g.hash) || !validHash(a.group_admission) || !["member","visitor"].includes(g.host_role) || (g.host_role==="member" && !validHash(g.host_admission)) || (g.host_role==="visitor" && g.host_admission) || (g.task_admissions || []).length !== (e.task_keys || []).length) throw new Error("participation: malformed group invitation scope");
      for (const epoch of g.task_admissions || []) if (!validHash(epoch)) throw new Error("participation: invalid task group admission");
    }
    if (!wellFormed(e.note) || utf8.encode(e.note).length > MaxInviteNote) throw new Error("participation: note too long or not text");
    if (!noteText.test(e.note)) throw new Error("participation: note has a control character");
    return;
  }
  if (e.type === "scope") { // the invite's public projection: never its grant, task keys or note
    if (!validHash(e.prev) || !e.host || e.audience !== "conversation" || e.grant !== null || e.task_keys !== null || e.note !== "" || e.group || a.group_admission ||
      (e.role && e.role !== "human") || (e.role === "human" && e.host.agent_id)) throw new Error("participation: a scope names its invite, host, agent and role only");
    if (!validID(e.host.person) || !validFingerprint(e.host.fingerprint) || e.host.agent_id && !validID(e.host.agent_id)) throw new Error("participation: invalid host");
    if (!validAddress(e.host.address)) throw new Error("participation: host: invalid address " + e.host.address);
    return;
  }
  if (!validHash(e.prev) || e.host || e.grant !== null || e.audience !== "" || e.task_keys !== null || e.note !== "" || e.group || e.role) {
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
    author: "object", ts: "int", host: "object", grant: "array", audience: "string", task_keys: "array", note: "string", group: "object", role: "string", sig: "string" });
  const a = strict(f.author || {}, "participation author", { person: "string", roster: "string", address: "string", fingerprint: "string", group_admission: "string" });
  const h = f.host ? strict(f.host, "participation host", { person: "string", address: "string", fingerprint: "string", agent_id: "string" }) : null;
  const group = f.group ? strict(f.group,"participation group",{seq:"int",hash:"string",host_role:"string",host_admission:"string",task_admissions:"array"}) : null;
  if ((group?.task_admissions || []).some(k=>typeof k!=="string")) throw new Error("participation: task admissions must be strings");
  if ((f.task_keys || []).some((k) => typeof k !== "string")) throw new Error("participation: task keys must be strings");
  const e = { v: f.v || 0, conv: f.conv || "", pid: f.pid || "", type: f.type || "", prev: f.prev || "", ts: f.ts || 0,
    author: { person: a.person || "", roster: a.roster || "", address: a.address || "", fingerprint: a.fingerprint || "", ...(a.group_admission ? {group_admission:a.group_admission} : {}) },
    host: h ? { person: h.person || "", address: h.address || "", fingerprint: h.fingerprint || "", ...(h.agent_id ? { agent_id: h.agent_id } : {}) } : null,
    grant: f.grant ? f.grant.map((g) => { const x = strict(g, "participation grant", { lid: "string", fingerprint: "string" });
      return { lid: x.lid || "", fingerprint: x.fingerprint || "" }; }) : null,
    audience: f.audience || "", task_keys: f.task_keys || null, note: f.note || "", ...(f.role ? { role: f.role } : {}), ...(group ? {group:{seq:group.seq || 0,hash:group.hash || "",host_role:group.host_role || "",host_admission:group.host_admission || "",task_admissions:group.task_admissions || null}} : {}), sig: f.sig ? unb64(f.sig, "event signature") : null };
  validateEvent(e);
  fitsRecord(eventJSON(e), MaxParticipationEvent, "participation");
  return e;
}

// scopeOf is protocol.ScopeOf: the unsigned public projection of DM invite
// inv, for its author to sign. projects is ParticipationEvent.Projects.
export async function scopeOf(inv, ts) {
  return { v: 1, conv: inv.conv, pid: inv.pid, type: "scope", prev: await eventHash(inv), author: { ...inv.author }, ts, host: { ...inv.host },
    grant: null, audience: "conversation", task_keys: null, note: "", ...(inv.role ? { role: inv.role } : {}) };
}
export async function projects(s, inv) {
  const same = (x, y) => x.person === y.person && x.address === y.address && x.fingerprint === y.fingerprint && (x.agent_id || "") === (y.agent_id || "");
  return s.type === "scope" && inv.type === "invite" && !!inv.host && !!s.host && s.conv === inv.conv && s.pid === inv.pid && s.prev === await eventHash(inv) &&
    same(s.author, inv.author) && roster(s.author) === roster(inv.author) && same(s.host, inv.host) && (s.role || "") === (inv.role || "") && !inv.group;
}
const roster = (a) => a.roster + "/" + (a.group_admission || "");

export async function verifyEvent(e, authorKey) {
  validateEvent(e);
  if (!(await verifyBytes(authorKey, eventCanonical(e), e.sig))) throw new Error("participation: signature invalid");
}

// ---- scoped ordinary human turns (envelope/human.go) -----------------------------------------
// Shape/hash validation only. Admission must also verify every signature against
// the pinned current roster and the sender/recipient's exact accepted scope.
export function humanJSON(h) {
  return "{" + (h.author_pid ? '"author_pid":' + goString(h.author_pid) + "," : "") +
    '"audience":' + (h.audience ? "[" + h.audience.map(s => '{"pid":' + goString(s.pid) + ',"invite":' + goString(s.invite) + ',"decision":' + goString(s.decision) + "}").join(",") + "]" : "null") +
    ',"proof":' + (h.proof ? "[" + h.proof.map(eventJSON).join(",") + "]" : "null") + "}";
}
export function parseHumanTurn(value) {
  const f = strict(typeof value === "string" ? JSON.parse(value) : value, "human", { author_pid: "string", audience: "array", proof: "array" });
  if (!f.audience?.length || f.audience.length > MaxHumanAudience || (f.proof?.length || 0) > MaxHumanProof) throw new Error("human: invalid audience or proof bound");
  return { ...(f.author_pid ? { author_pid: f.author_pid } : {}), audience: f.audience.map(s => {
    const x = strict(s, "human scope", { pid: "string", invite: "string", decision: "string" });
    return { pid: x.pid || "", invite: x.invite || "", decision: x.decision || "" };
  }), proof: (f.proof || []).map(e => parseEvent(e.sig instanceof Uint8Array ? { ...e, sig: b64(e.sig) } : e)) };
}
export async function validateHumanTurn(h, conv) {
  if ((h.author_pid && !validID(h.author_pid)) || !h.audience?.length || h.audience.length > MaxHumanAudience || (h.proof?.length || 0) > MaxHumanProof) throw new Error("human: invalid author or audience bound");
  const scopes = new Map(), events = new Set();
  for (const s of h.audience) {
    if (!validID(s.pid) || !validHash(s.invite) || !validHash(s.decision) || scopes.has(s.pid)) throw new Error("human: invalid or duplicate scope");
    scopes.set(s.pid, s);
  }
  if (h.author_pid && !scopes.has(h.author_pid)) throw new Error("human: author outside captured audience");
  for (const e of h.proof || []) {
    validateEvent(e);
    const scope = scopes.get(e.pid);
    if (!scope || e.sig?.length !== 64 || utf8.encode(eventJSON(e)).length > MaxParticipationEvent || e.conv !== conv || !["scope", "accept"].includes(e.type) || (e.type === "scope" && e.role !== "human")) throw new Error("human: proof outside audience");
    const hash = await eventHash(e);
    if (events.has(hash) || e.prev !== scope.invite || (e.type === "accept" && hash !== scope.decision)) throw new Error("human: duplicate or unrelated proof");
    events.add(hash);
    if (e.type === "scope") events.add("scope/" + e.pid);
  }
  for (const s of h.audience) if (!events.has("scope/" + s.pid) || !events.has(s.decision)) throw new Error("human: missing scope or acceptance proof");
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
// sealLocal/openLocal keep a record private to this device in its own
// store (age, to this device's own box key): Drive drafts, pending spaces
// and agent grants live encrypted at rest, never as plain rows.
export async function sealLocal(keys, value) {
  const e = new Encrypter();
  e.addRecipient(await identityToRecipient(keys.box));
  return b64(await e.encrypt(utf8.encode(JSON.stringify(value))));
}
export async function openLocal(keys, sealed) {
  const d = new Decrypter();
  d.addIdentity(keys.box);
  return JSON.parse(fromUTF8.decode(await d.decrypt(unb64(sealed, "local record"))));
}

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

// Selected own-device return routing. These are codec/shape primitives only;
// verified own-person chains, consent, generation and execution stay in Engine/client.
export const MaxReceiverSetup = 64 << 10, MaxReceiverInstructions = 4 << 10, MaxReceiverSessions = 32;
export function receiverRequirement(n) {
 if(n.receiver_route)return true;
 if(n.sub==='history')return !!parseHistory(n.body).receiver_route;
 return false;
}
const receiverDigestDomain = "agentnet-receiver-delegation-v1\n";
const receiverHandle = s => typeof s === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(s);
const receiverInstructions = (s, mode) => typeof s === "string" && wellFormed(s) && !!s.trim() && utf8.encode(s).length <= MaxReceiverInstructions && ["question", "task"].includes(mode);
const receiverTargetJSON = t => '{"address":'+goString(t.address)+',"fingerprint":'+goString(t.fingerprint)+(t.agent_id?',"agent_id":'+goString(t.agent_id):'')+(t.group_admission?',"group_admission":'+goString(t.group_admission):'')+'}';
const receiverAttachmentJSON = a => '{"blob":'+blobJSON(a.blob)+',"name":'+goString(a.name)+',"size":'+goInt(a.size,"file size")+',"sha256":'+goString(a.sha256)+'}';
export function parseReceiverRoute(value) {
 const f=strict(typeof value==='string'?JSON.parse(value):value,'receiver route',{op:'string',host:'string',host_key:'string',request_ref:'string',request_digest:'string',delegation_id:'string'});
 const r={op:f.op||'',host:f.host||'',host_key:f.host_key||'',request_ref:f.request_ref||'',request_digest:f.request_digest||'',delegation_id:f.delegation_id||''};
 if(!validAddress(r.host)||!validFingerprint(r.host_key))throw Error('receiver: exact host address and key required');
 if(r.op==='catalog'){if(r.request_ref||r.request_digest||r.delegation_id)throw Error('receiver: catalog has no request commitment');}
 else if(!['delegate','ready','request'].includes(r.op)||!validID(r.request_ref)||!validHash(r.request_digest)||!validID(r.delegation_id))throw Error('receiver: invalid request commitment');
 return r;
}
export function receiverRouteJSON(r) {
 return '{"op":'+goString(r.op)+',"host":'+goString(r.host)+',"host_key":'+goString(r.host_key)+(r.request_ref?',"request_ref":'+goString(r.request_ref):'')+(r.request_digest?',"request_digest":'+goString(r.request_digest):'')+(r.delegation_id?',"delegation_id":'+goString(r.delegation_id):'')+'}';
}
export function parseReceiverChoice(value) {
 const f=strict(typeof value==='string'?JSON.parse(value):value,'receiver choice',{kind:'string',agent_id:'string',session_handle:'string',instructions:'string',mode:'string',on_close:'object'});
 const r={kind:f.kind||'',agent_id:f.agent_id||'',session_handle:f.session_handle||'',instructions:f.instructions||'',mode:f.mode||'',on_close:f.on_close?strict(f.on_close,'receiver backup',{agent_id:'string',instructions:'string',mode:'string'}):null};
 if(r.kind==='human'){if(r.agent_id||r.session_handle||r.instructions||r.mode||r.on_close)throw Error('receiver: human choice has no executor');}
 else if(r.kind==='managed_agent'){if(!validID(r.agent_id)||r.session_handle||!receiverInstructions(r.instructions,r.mode)||r.on_close)throw Error('receiver: managed choice requires exact agent, instructions and mode');}
 else if(r.kind==='live_session'){
  if(!receiverHandle(r.session_handle)||r.agent_id||r.instructions||r.mode)throw Error('receiver: live choice requires only an opaque handle');
  if(r.on_close&&(!validID(r.on_close.agent_id)||!receiverInstructions(r.on_close.instructions,r.on_close.mode)))throw Error('receiver: invalid explicit closed-session backup');
 }else throw Error('receiver: unknown choice');
 return r;
}
export function receiverChoiceJSON(r) {
 return '{"kind":'+goString(r.kind)+(r.agent_id?',"agent_id":'+goString(r.agent_id):'')+(r.session_handle?',"session_handle":'+goString(r.session_handle):'')+(r.instructions?',"instructions":'+goString(r.instructions):'')+(r.mode?',"mode":'+goString(r.mode):'')+(r.on_close?',"on_close":{"agent_id":'+goString(r.on_close.agent_id)+',"instructions":'+goString(r.on_close.instructions)+',"mode":'+goString(r.on_close.mode)+'}':'')+'}';
}
// Extract only the frozen root's raw object text: JSON.parse alone discards the
// exact RawMessage encoding Go compares with its typed signed root encoder.
function receiverRawRoot(json) {
 const match=/(?<!\\)"root"\s*:\s*\{/.exec(json);if(!match)return null;
 const start=match.index+match[0].lastIndexOf('{');let depth=0,quoted=false,escaped=false;
 for(let i=start;i<json.length;i++){const c=json[i];if(quoted){if(escaped)escaped=false;else if(c==='\\')escaped=true;else if(c==='"')quoted=false;}
  else if(c==='"')quoted=true;else if(c==='{')depth++;else if(c==='}'&&--depth===0)return json.slice(start,i+1);}
 throw Error('receiver: incomplete frozen root');
}
export async function parseReceiverRequest(value) {
 const raw=typeof value==='string', source=raw?JSON.parse(value):value;
 const f=strict(source,'receiver request',{id:'string',lid:'string',from:'string',from_key:'string',to:'string',to_key:'string',ts:'int',conv:'string',root:raw?'object':typeof source?.root==='string'?'string':'object',kind:'string',body:'string',reply_to:'string',origin:'string',emotion:'string',target:'object',pid:'string',attachments:'array',group_admission:'string',group_replies:'array',human:'object'});
 const r={id:f.id||'',lid:f.lid||'',from:f.from||'',from_key:f.from_key||'',to:f.to||'',to_key:f.to_key||'',ts:f.ts||0,conv:f.conv||'',root:raw?receiverRawRoot(value)||'':f.root?(typeof f.root==='string'?f.root:rootJSON(parseConvRoot(f.root))):'',kind:f.kind||'',body:f.body||'',reply_to:f.reply_to||'',origin:f.origin||'',emotion:f.emotion||'',target:f.target?strict(f.target,'receiver target',{address:'string',fingerprint:'string',agent_id:'string',group_admission:'string'}):null,pid:f.pid||'',attachments:(f.attachments||[]).map(a=>{const x=strict(a,'receiver attachment',{blob:'object',name:'string',size:'int',sha256:'string'});return {blob:x.blob?strict(x.blob,'receiver blob',{id:'string',size:'int',sha256:'string'}):{id:'',size:0,sha256:''},name:x.name||'',size:x.size||0,sha256:x.sha256||''};}),group_admission:f.group_admission||'',group_replies:(f.group_replies||[]).map(k=>strict(k,'receiver reply key',{key:'string',admission:'string'})),...(f.human?{human:parseHumanTurn(f.human)}:{})};
 if(!validID(r.id)||!validAddress(r.from)||!validFingerprint(r.from_key)||!(r.ts>0)||!['message','question','task'].includes(r.kind)||!wellFormed(r.body)||r.reply_to&&!validID(r.reply_to))throw Error('receiver: invalid original request');
 let group=false;
 if(r.conv){if(r.to||r.to_key)throw Error('receiver: conversation recipient derives from verified root and target');const root=parseConvRoot(r.root);if(rootJSON(root)!==r.root||await rootID(root)!==r.conv)throw Error('receiver: original root must use exact typed signed encoding');group=root.kind==='group';if(r.target&&r.id!==r.lid)throw Error('receiver: targeted conversation copy must use its committed logical ID');}
 else if(!validAddress(r.to)||!validFingerprint(r.to_key)||r.target&&(r.target.address!==r.to||r.target.fingerprint!==r.to_key))throw Error('receiver: direct original requires exact recipient address and key');
 const n={v:r.conv?Version2:Version,id:r.id,from:r.from,to:r.conv?r.from:r.to,ts:r.ts,kind:r.kind,body:r.body,reply_to:r.reply_to,conv:r.conv,lid:r.lid,root:r.root,origin:r.origin,emotion:r.emotion,target:r.target,pid:r.pid,human:r.human,attachments:[],fan:null,sub:'',replica:false,status:'',agent_id:'',session:'',fallback:false,ref:null};
 await checkV2(n);if(n.v===Version2&&agentOrigin(r.origin)&&!r.emotion)throw Error('receiver: agent request requires emotion');
 if(r.attachments.length>MaxAttachments)throw Error('receiver: too many original files');
 for(const a of r.attachments){if(a.blob.id||a.blob.size||a.blob.sha256||!a.name||!wellFormed(a.name)||a.size<0||!validHash(a.sha256))throw Error('receiver: original files require plaintext-only manifests');}
 if(!group){if(r.group_admission||r.group_replies.length)throw Error('receiver: group authority on non-group original');}
 else {if(!validHash(r.group_admission)||!r.group_replies.length||r.group_replies.length>MaxGroupMembers*MaxPersonDevices)throw Error('receiver: group original requires bounded admission and reply keys');let previous='';for(const k of r.group_replies){if(!validFingerprint(k.key)||k.key<=previous||k.admission&&!validHash(k.admission))throw Error('receiver: group reply keys must be sorted, unique and exact');if(!k.admission&&(!r.pid||!r.target||k.key!==r.target.fingerprint))throw Error('receiver: only an exact PID target may carry a visitor reply key');previous=k.key;}}
 if(utf8.encode(receiverRequestJSON(r)).length>MaxReceiverSetup)throw Error('receiver: original snapshot too large');return r;
}
export function receiverRequestJSON(r) {
 let s='{"id":'+goString(r.id)+(r.lid?',"lid":'+goString(r.lid):'')+',"from":'+goString(r.from)+',"from_key":'+goString(r.from_key)+(r.to?',"to":'+goString(r.to):'')+(r.to_key?',"to_key":'+goString(r.to_key):'')+',"ts":'+goInt(r.ts,'time')+(r.conv?',"conv":'+goString(r.conv):'')+(r.root?',"root":'+r.root:'')+',"kind":'+goString(r.kind)+',"body":'+goString(r.body);
 for(const k of ['reply_to','origin','emotion'])if(r[k])s+=',"'+k+'":'+goString(r[k]);
 if(r.target)s+=',"target":'+receiverTargetJSON(r.target);if(r.pid)s+=',"pid":'+goString(r.pid);if(r.attachments?.length)s+=',"attachments":['+r.attachments.map(receiverAttachmentJSON).join(',')+']';
 if(r.group_admission)s+=',"group_admission":'+goString(r.group_admission);if(r.group_replies?.length)s+=',"group_replies":['+r.group_replies.map(k=>'{"key":'+goString(k.key)+(k.admission?',"admission":'+goString(k.admission):'')+'}').join(',')+']';if(r.human)s+=',"human":'+humanJSON(r.human);return s+'}';
}
export async function receiverDigest(route,request,receiver) {
 if(!['request','delegate','ready'].includes(route.op)||route.request_digest&&!validHash(route.request_digest))throw Error('receiver: invalid commitment operation or digest');
 const r=parseReceiverRoute({...route,op:'request',request_digest:'0'.repeat(64)}),q=await parseReceiverRequest(request),c=parseReceiverChoice(receiver);if(r.request_ref!==(q.conv?q.lid:q.id))throw Error('receiver: route reference does not match original');r.request_digest='';
 const json='{"route":'+receiverRouteJSON(r)+',"request":'+receiverRequestJSON(q)+',"receiver":'+receiverChoiceJSON(c)+'}';if(utf8.encode(json).length>MaxReceiverSetup)throw Error('receiver: delegation commitment too large');return hex(await sha256(utf8.encode(receiverDigestDomain+json)));
}
export function receiverOperationJSON(o) {
 return '{"v":'+goInt(o.v,'setup version')+(o.request?',"request":'+receiverRequestJSON(o.request):'')+(o.receiver?',"receiver":'+receiverChoiceJSON(o.receiver):'')+(o.sessions?.length?',"sessions":['+o.sessions.map(s=>'{"handle":'+goString(s.handle)+',"harness":'+goString(s.harness)+',"label":'+goString(s.label)+',"active":'+(s.active?'true':'false')+'}').join(',')+']':'')+(o.detail?',"detail":'+goString(o.detail):'')+'}';
}
export async function parseReceiverOperation(body,route,replyTo='',attachments=[]) {
 if(utf8.encode(body).length>MaxReceiverSetup)throw Error('receiver: setup body too large');const f=strict(JSON.parse(body),'receiver operation',{v:'int',request:'object',receiver:'object',sessions:'array',detail:'string'}),r=parseReceiverRoute(route);if(f.v!==1)throw Error('receiver: unsupported setup version');
 const o={v:f.v,request:null,receiver:null,sessions:f.sessions||null,detail:f.detail||''};
 if(r.op==='catalog'){
  if(f.request||f.receiver||o.detail||attachments.length||(o.sessions||[]).length>MaxReceiverSessions||!replyTo&&o.sessions||replyTo&&!validID(replyTo))throw Error('receiver: invalid catalog fields');let previous='';
  o.sessions=o.sessions?.map(value=>{const s=strict(value,'receiver session',{handle:'string',harness:'string',label:'string',active:'boolean'});if(!receiverHandle(s.handle)||s.handle<=previous||utf8.encode(s.label||'').length>160||!wellFormed(s.label||'')||!['pi','omp','codex','claude'].includes(s.harness))throw Error('receiver: invalid catalog session');previous=s.handle;return {...s,label:s.label||'',active:!!s.active};})||null;
 }else if(r.op==='delegate'){
  if(!f.request||!f.receiver||o.sessions||o.detail||replyTo)throw Error('receiver: invalid delegate fields');const rawRoot=receiverRawRoot(body);o.request=await parseReceiverRequest(rawRoot?{...f.request,root:rawRoot}:f.request);o.receiver=parseReceiverChoice(f.receiver);
  if(await receiverDigest(r,o.request,o.receiver)!==r.request_digest)throw Error('receiver: delegation commitment mismatch');
  if(attachments.length!==o.request.attachments.length)throw Error('receiver: delegated file count mismatch');for(let i=0;i<attachments.length;i++){const a=attachments[i],p=o.request.attachments[i];if(a.name!==p.name||a.size!==p.size||a.sha256!==p.sha256||!validID(a.blob?.id)||!(a.blob.size>0)||!validHash(a.blob.sha256))throw Error('receiver: delegated encrypted file manifest mismatch');}
 }else if(r.op==='ready'){
  if(f.request||f.receiver||o.sessions||attachments.length||replyTo!==r.delegation_id||utf8.encode(o.detail).length>MaxDetailBytes||!wellFormed(o.detail))throw Error('receiver: invalid ready fields');
 }else throw Error('receiver: original request body is not setup JSON');return o;
}
export async function validateReceiverRoute(n) {
 if(!n.receiver_route)return;const r=parseReceiverRoute(n.receiver_route);
 if(n.agent_id||n.status||n.ref||n.session||n.fallback||n.sub||![Version,Version2].includes(n.v))throw Error('receiver: route only belongs on setup or original requests');
 if(r.op==='request'){
  if(!['message','question','task'].includes(n.kind)||r.request_ref!==(n.v===Version2?n.lid:n.id))throw Error('receiver: original route reference mismatch');
  if(n.v===Version2&&n.target?.address===n.to&&!n.replica&&n.id!==n.lid)throw Error('receiver: executable conversation copy must use its committed logical ID');return;
 }
 if(n.v!==Version||n.target||n.pid||n.replica||n.fan||n.origin||n.emotion||n.kind!==(r.op==='delegate'?'task':'message')||!validID(n.id)||!(n.ts>0))throw Error('receiver: setup requires a dedicated direct envelope');
 if(r.op==='delegate'&&n.id!==r.delegation_id)throw Error('receiver: delegation envelope ID differs from commitment');
 if(r.op==='delegate'||r.op==='catalog'&&!n.reply_to){if(r.host!==n.to)throw Error('receiver: setup must address its committed host');}else if(r.host!==n.from)throw Error('receiver: response must originate at its committed host');
 const o=await parseReceiverOperation(n.body,r,n.reply_to||'',n.attachments||[]);if(o.request&&o.request.from!==n.from)throw Error('receiver: delegate must originate at original author');
}
