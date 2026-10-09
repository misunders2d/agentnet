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
import { Decrypter, Encrypter } from "./vendor/age.mjs";
import { openDB } from "./vendor/idb.mjs"; // idb: promises over IndexedDB (webvendor, pinned)
import { createParser } from "./vendor/sse.mjs"; // eventsource-parser: the framing of the signed event stream (webvendor, pinned)
import { browserTeams } from "./teams.mjs"; // signed person teams over this engine (native module)
import { browserDriveProvider } from "./drivespace.mjs"; // the optional Google Drive space of a conversation (native module)
import { browserStorageSetupProvider } from "./drivespace-setup.mjs"; // Settings > File storage options (native module)
import * as getapp from "./getapp.mjs"; // where the AgentNet app is downloaded (getapp.json, shared with Go)

const HEARTBEAT = 90_000;
// Tunables mirrored from Go, pinned by parity tests: device names tried
// when one is taken (cmd/agentnet appNameTries), and the days an
// invitation may work (ui.InviteDays).
export const autoNameTries = 20;
export const inviteDays = [1, 7, 30];

// inviteMessage is ui.InviteMessage: what an admin sends with a link.
export const inviteMessage = (from, link) => (from ? from + " invited you" : "You're invited") + " to AgentNet. Open this link to get the app and join: " + link;
const MAX_BACKOFF = 60_000;
// files keeps file ciphertext: received files ("ct/" + blob id) and a copy
// of each file sent here, encrypted to this device ("kept/" + its SHA-256),
// so another device of your person can ask for them.
// erased keeps every turn name a conversation deletion erased here
// (conv|key|lid; client convclear.go conv_erased), so it never comes back.
const stores = ["kv", "pins", "persons", "convs", "inbox", "outbox", "held", "lids", "receipts", "files", "erased"];
// Conversation deletion (client convclear.go): what a deletion erases, and
// what unfinished work keeps until it ends.
const erasableSubs = new Set(["", "excerpt", "reaction", "revision", "retraction"]);
const erasedKey = (conv, key, lid) => conv + "|" + key + "|" + lid;
const retainedIn = (r) => ["question", "task"].includes(r.kind) && ["pending", "accepted", "held", "awaiting", "running", "cancel_requested", "part_waiting", "needs_human"].includes(r.state);
const retainedOut = (r) => ["queued", "waiting", "receiver_waiting"].includes(r.state);
// deletionNote is what the page says after a deletion (ui livedelete.go).
const deletionNote = (thisOnly, devices, kept) => (thisOnly ? "Deleted. This thread was stored on this device only; the other person keeps their copy."
  : devices === 0 ? "Deleted here. No other device of yours was linked; one you link later gets the deletion. Others in it keep their copies."
  : "Deleted here. Queued for your " + devices + " other device(s): each removes it once it is connected and runs an AgentNet version that applies deletions; until then it still shows it there. Others in it keep their copies.")
  + (kept > 0 ? " " + kept + " item(s) still in progress keep running and are removed when they finish." : "");
// heldPage bounds the held messages read in one step (the Go client's proofPage).
const heldPage = 50;
// historyPage bounds the messages one step of copying your chats to a new
// device queues (the Go client's historyPage).
const historyPage = 50;
// linkTTL is how long a device link lives: a minute under the server's
// limit (protocol.MaxLinkTTL), for this browser's clock.
const linkTTL = 9 * 60;
// A phone opened after hours closed catches up on hundreds of messages
// (MEL-546). receiptWidth bounds the receipts sent at once; arrivals tell
// the page at once when they start, then at gaps from burstFirst doubling
// to burstMost while they keep coming, and once burstQuiet ms after the
// last, instead of after every message.
const receiptWidth = 8;
const burstFirst = 1000, burstMost = 8000, burstQuiet = 250;
// retractionWrite: a write that may change which retractions inbox and
// outbox hold (a retraction row put, or any row removed). A row's kind
// never changes under its key, so no other write can.
const retractionWrite = (ops) => ops.some((o) => (o.s === "inbox" || o.s === "outbox") && (o.v === undefined || (o.v.control && o.v.sub === wire.SubRetraction)));

// Sparse source indexes exclude carriers, local harness records and erased
// controls. Arrival is storage order, independent of an old copy's display time.
const historySource = r => r?.conv && r.lid && Number.isFinite(r.at) && !r.local && !r.aside && !["history","file","clear","root-sync","group-proof","group-context","group-invite","group-consent","group-withdrawal"].includes(r.sub);
const historyOrder = (a,b) => { for(let i=0;i<a.length;i++)if(a[i]!==b[i])return a[i]<b[i]?-1:1;return 0; };
const historyIndexed = (row,arrival) => {
  const value=structuredClone(row);delete value.history_pos;
  if(historySource(value))value.history_pos=[value.conv,value.at,value.id];
  else delete value.history_arrival;
  if(arrival)value.history_arrival=arrival;
  return value;
};

// ---- storage -------------------------------------------------------------------------------

// openIDB opens this device's database (idb: promises over IndexedDB).
// Every write is one strict-durability transaction, so "stored" means
// stored before anything is acknowledged or sent; a write is all or none.
// A database blocked by another tab's old connection is refused here as
// before: idb only reports "blocked", so the open is raced against that
// report, and a connection that completes after the refusal is closed at
// once, never left open.
export async function openIDB(name = "agentnet") {
  let onBlocked, refused = false;
  const blocked = new Promise((_, rej) => { onBlocked = () => { refused = true; rej(new Error("storage is blocked by another tab")); }; });
  const opening = openDB(name, 4, { // 4: bounded accepted-history order/arrival indexes
    async upgrade(d,oldVersion,newVersion,tx) {
      for (const s of stores) if (!d.objectStoreNames.contains(s)) d.createObjectStore(s);
      if(oldVersion<4) {
        let arrival=0;
        for(const s of ["inbox","outbox"]) {
          const store=tx.objectStore(s);store.createIndex("history_pos","history_pos");
          if(s==="inbox")store.createIndex("history_arrival","history_arrival");
          for(let c=await store.openCursor();c;c=await c.continue())if(historySource(c.value))await c.update(historyIndexed(c.value,s==="inbox"?++arrival:0));
        }
        await tx.objectStore("kv").put(arrival,"history-arrival");
      }
    },
    blocked: onBlocked,
  });
  opening.then((d) => { if (refused) d.close(); }, () => {}); // late: the tab that blocked it went away after the refusal
  const db = await Promise.race([opening, blocked]);
  let retractionMark = 0;
  return {
    // retractionMark changes after every write that may change the
    // retractions held (Engine.retractions keeps them between such writes).
    retractionMark: () => retractionMark,
    get: (s, k) => db.get(s, k),
    all: (s) => db.getAll(s),
    // after returns up to n values whose keys follow k, in key order ("" is the start).
    after: (s, k, n) => db.getAll(s, k === "" ? null : IDBKeyRange.lowerBound(k, true), n),
    // prefix returns the values whose keys start with p, in key order.
    prefix: (s, p) => db.getAll(s, IDBKeyRange.bound(p, p + "\uffff")),
    async historyRows(s,{after=null,conv="",reverse=false,arrival=false,ceiling=Number.MAX_SAFE_INTEGER,limit=historyPage}={}) {
      let range;
      if(arrival) {if((after||0)>=ceiling)return [];range=IDBKeyRange.bound(after||0,ceiling,true,false);}
      else if(conv)range=IDBKeyRange.bound([conv],[conv,[]]);
      else if(after)range=IDBKeyRange.lowerBound(after,true);
      const tx=db.transaction(s),rows=[];
      for(let c=await tx.store.index(arrival?"history_arrival":"history_pos").openCursor(range,reverse?"prev":"next");c&&rows.length<limit;c=await c.continue())rows.push(c.value);
      await tx.done;return rows;
    },
    async write(ops, checks = []) {
      const names=[...ops,...checks].map(o=>o.s);if(ops.some(o=>o.s==="inbox"&&o.v))names.push("kv");
      const tx = db.transaction([...new Set(names)], "readwrite", { durability: "strict" });
      try {
        for (const c of checks) {
          if (c.scope) {
            const rows = [];
            for (let cursor = await tx.objectStore(c.s).openCursor(); cursor; cursor = await cursor.continue()) {
              if (scopeRow(cursor.value, c.scope)) rows.push({ k: cursor.primaryKey, v: authorityValue(c.s, cursor.value) });
            }
            if (JSON.stringify(rows) !== JSON.stringify(c.rows)) throw new StoreConflict();
            continue;
          }
          const actual = await tx.objectStore(c.s).get(c.k);
          if (JSON.stringify(actual) !== JSON.stringify(c.v)) throw new StoreConflict();
        }
        for (const o of ops) {
          let value=o.v;
          if(value&&(o.s==="inbox"||o.s==="outbox")) {
            let arrival=0;
            if(o.s==="inbox"&&historySource(value)) {
              arrival=(await tx.objectStore("inbox").get(o.k))?.history_arrival||0;
              if(!arrival) {arrival=(await tx.objectStore("kv").get("history-arrival")||0)+1;await tx.objectStore("kv").put(arrival,"history-arrival");}
            }
            value=historyIndexed(value,arrival);
          }
          // Each request's own rejection is the transaction's (tx.done); it is not awaited one by one.
          (value === undefined ? tx.objectStore(o.s).delete(o.k) : tx.objectStore(o.s).put(value, o.k)).catch(() => {});
        }
      } catch (e) {
        // A request that cannot be made (a value that cannot be stored)
        // aborts the others made before it: all of a write or none.
        try { tx.abort(); } catch (_) { /* a failed IDB request may already have aborted */ }
        await tx.done.catch(() => {});
        throw e;
      }
      await tx.done;
      if (retractionWrite(ops)) retractionMark++;
    },
    close: () => db.close(),
  };
}

// memoryStore is the same store in memory (tests). Values are structured
// clones, as IndexedDB keeps them.
export function memoryStore() {
  const data = Object.fromEntries(stores.map((s) => [s, new Map()]));
  let retractionMark = 0;
  return {
    retractionMark: () => retractionMark,
    get: async (s, k) => (data[s].has(k) ? structuredClone(data[s].get(k)) : undefined),
    all: async (s) => [...data[s].values()].map((v) => structuredClone(v)),
    after: async (s, k, n) => [...data[s].keys()].filter((x) => x > k).sort().slice(0, n).map((x) => structuredClone(data[s].get(x))),
    prefix: async (s, p) => [...data[s].keys()].filter((x) => x.startsWith(p)).sort().map((x) => structuredClone(data[s].get(x))),
    historyRows: async(s,{after=null,conv="",reverse=false,arrival=false,ceiling=Number.MAX_SAFE_INTEGER,limit=historyPage}={})=>[...data[s].values()].filter(r=>r.history_pos&&(!conv||r.conv===conv)&&(arrival?r.history_arrival>(after||0)&&r.history_arrival<=ceiling:!after||historyOrder(r.history_pos,after)>0)).sort((a,b)=>(arrival?a.history_arrival-b.history_arrival:historyOrder(a.history_pos,b.history_pos))*(reverse?-1:1)).slice(0,limit).map(r=>structuredClone(r)),
    async write(ops, checks = []) {
      for (const c of checks) {
        const actual = c.scope ? [...data[c.s].entries()].filter(([, v]) => scopeRow(v, c.scope)).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([k, v]) => ({ k, v: authorityValue(c.s, v) })) : data[c.s].get(c.k);
        if (JSON.stringify(actual) !== JSON.stringify(c.scope ? c.rows : c.v)) throw new StoreConflict();
      }
      for (const o of ops) {
        if (o.v === undefined) data[o.s].delete(o.k);
        else {
          let value=structuredClone(o.v);
          if(o.s==="inbox"||o.s==="outbox") {
            let arrival=0;
            if(o.s==="inbox"&&historySource(value)) {
              arrival=data.inbox.get(o.k)?.history_arrival||0;
              if(!arrival) {arrival=(data.kv.get("history-arrival")||0)+1;data.kv.set("history-arrival",arrival);}
            }
            value=historyIndexed(value,arrival);
          }
          data[o.s].set(o.k,value);
        }
      }
      if (retractionWrite(ops)) retractionMark++;
    },
    close() {},
  };
}

class StoreConflict extends Error { constructor() { super("storage changed during verification"); } }
// follows is client ParticipationInfo.follows: of a room's captured audience
// (a human guest or a room participant), whatever its state; roomAgentScope
// a scope of an agent room participant, which a member may host.
const follows = (p) => p.role === "human" || p.audience === "room";
const roomAgentScope = (e) => !e.role && e.audience === "room";

// Authority sets also guard absent records: a new decision or a competing
// request arriving during signature verification must invalidate that read.
// A shared human record copy (forwarded) is a carrier: its record already
// counts from the row it came from, so it is never authority or a room turn.
// Delivery progress never changes the signed authority of an outbox row.
const authorityValue = (store, row) => {
  if (store !== "outbox") return structuredClone(row);
  const {state, detail, delivery, path, ...authority} = row;
  if (authority.files) authority.files = authority.files.map(({uploaded, ct, ...file}) => file);
  return structuredClone(authority);
};
const scopeRow = (row, scope) => !row.forwarded && ["conv", "pid", "sub", "lid"].every((k) => scope[k] === undefined || row[k] === scope[k]);

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

// Why a received message is held back, as the overview's quarantine code
// (ui.Hold*, live.go holdCode): a page words it itself, naming the sender
// from its own people list; holdText is the fallback sentence.
const holdWords = {
  invalid: () => "This message failed a check and was kept out of the chat. Its contents stay hidden and it cannot start any work.",
  key_changed: (p) => "Held until " + p + "'s changed key is trusted in AgentNet on a computer.",
  proof_pending: () => "Held until the conversation or person it names can be checked here. Nothing runs it.",
  identity_conflict: (p) => "Held: it disagrees with the person record kept here for " + p + ". Nothing runs it.",
  conflicting_duplicate: (p) => "Held: " + p + " sent different content under a message it already sent. Nothing runs it.",
};
export const holdCode = (reason) => (Object.hasOwn(holdWords, reason) ? reason : "unverified");
const holdText = (reason, peer) => (holdCode(reason) !== "unverified" ? holdWords[reason](peer) : "It did not verify, so its content is not shown.");
// Persist only audited local diagnostic codes, never arbitrary received text.
export const heldDiagnosticCode = why => {
  if (/^Group advanced from state [0-9]+ to [0-9]+; inviter must refresh the invitation\. Join the fresh proposal\.$/.test(why)) return "group_invitation_outdated";
  const codes = {
  "This invitation is out of date; join the newer invitation or ask the inviter to refresh it.": "group_invitation_outdated",
  "Latest group head differs from this proposal; inviter must refresh the invitation. Join the fresh proposal.": "group_invitation_outdated",
  "Invited person changed; obtain fresh consent.": "group_invitation_outdated",
  "Captured human consent differs from local proof.": "captured_consent_mismatch",
  "Consent conflicts with recorded decision.": "group_consent_mismatch",
  "Consent has no recorded local invitation.": "group_consent_mismatch",
  "Consent is not for this exact invitation and current person.": "group_consent_mismatch",
  "Withdrawal is not this exact current signer and admission.": "group_withdrawal_mismatch",
  "group target admission is absent or withdrawn": "group_admission_unavailable",
  "group pending withdrawal conflicts with admin admission": "group_authority_conflict",
  "group withdrawal conflicts with promoted admin": "group_authority_conflict",
  "Group logical lifecycle conflict.": "group_conflicting_copy",
  "Human history remains with original members' own linked devices.": "history_reader_not_member",
}; return Object.hasOwn(codes,why) ? codes[why] : ""; };
export const heldNoticeText = (code, reason) => {
  const words = {
    group_invitation_outdated: ["This invitation no longer matches the current group.", "If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice."],
    group_consent_mismatch: ["This response does not match the invitation and decision recorded on this device.", "Check the group's current invitation. If you still need to join, ask its administrator for a fresh invitation."],
    group_withdrawal_mismatch: ["This departure record could not be verified against the current group and device identity.", "Check the group's current membership with its administrator. This record has not changed anyone's access."],
    group_admission_unavailable: ["The group admission named by this operation is withdrawn or unavailable.", "Check the group's membership and pending context. Archiving this notice does not restore access."],
    group_authority_conflict: ["The signed departure conflicts with the group's current administrator authority.", "Resolve the group authority or context with its administrator. Do not resend work automatically."],
    group_conflicting_copy: ["This copy conflicts with an existing logical group record.", "Ask the sender to check the original record and its status. Do not automatically resend requests."],
    history_reader_not_member: ["This history copy is not addressed to an original member's current linked device.", "Check the verified membership and device roster. A history copy cannot grant membership."],
    captured_consent_mismatch: ["The captured audience does not match the consent proof stored here.", "Check the participation's invitation and acceptance. This notice cannot grant consent."],
  };
  const [detail,recovery] = (Object.hasOwn(words,code) ? words[code] : null) || (reason === "invalid" ? ["The original detailed reason was not recorded or is unavailable.", "The retained message stays blocked. You can archive this notice locally; this does not accept, resend, or run it."] : reason === "proof_pending" ? ["", "You can archive this notice. Checks continue when connected; the message appears when verified."] : ["", ""]);
  return {detail,recovery};
};
const canArchiveHeld = reason => reason === "invalid" || reason === "proof_pending";
// quarantineItem is one held message as the overview lists it (ui.QuarantineItem, live.go quarantineItems).
export const quarantineItem = (h) => ({ id: h.id, peer: h.from, code: holdCode(h.reason), reason: holdText(h.reason, h.from), at: iso(h.at), can_archive:canArchiveHeld(h.reason), ...(h.detail_code ? {detail_code:h.detail_code} : {}), ...heldNoticeText(h.detail_code,h.reason) });

// deviceWords is client.DeviceWords: a device address in words, as every
// screen shows it ("bohdan/windows-laptop" → "Windows laptop", "admin/iphone"
// → "iPhone"; vectors: internal/ui/testdata/device_words.json).
const deviceSpecial = { iphone: "iPhone", ipad: "iPad", imac: "iMac", mac: "Mac", macbook: "MacBook" };
export function deviceWords(address) {
  const s = String(address || ""), i = s.indexOf("/");
  return (i < 0 ? s : s.slice(i + 1)).split("-").filter(Boolean)
    .map((w, j) => deviceSpecial[w.toLowerCase()] || (j === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(" ");
}

export function outText(state, peer, detail = "") {
  switch (state) {
  case "receiver_waiting": return detail || "Waiting for the selected reply receiver to accept this exact request.";
  case "waiting":
    if (detail.startsWith("peer_update: ")) return peer === "You" || peer === "your other device" ? "Your other device needs to update AgentNet" : "Waiting for " + peer + " to update AgentNet";
    if (detail.startsWith("server_update: ")) return "Your server needs an update before this can be sent";
    if (detail.startsWith("server_unavailable: ")) return "Cannot reach your server; retries automatically";
    return "Kept here, not sent yet" + (detail ? ": " + detail : "");
  case "queued": return "Waiting to send; retries automatically";
  case "custody": return "On the server; delivery to " + peer + " not confirmed yet";
  case "delivered": return "Delivered to " + peer;
  case "expired": return "Not delivered: that session ended first";
  case "failed": return "Not sent" + (detail ? ": " + detail : "");
  case "quarantined": return peer + " could not verify it";
  }
  return "";
}

const iso = (ms) => new Date(ms).toISOString();
// isNotice is a received review notice (client isReviewNotice): a version 1
// plain message with status review_notice, no reply_to, no files.
const isNotice = (m) => m && m.v === 1 && !m.control && !m.conv && !m.sub && m.kind === "message" && m.status === "review_notice" && !m.reply_to && !(m.attachments || []).length;
// noticeReport is a notice's version 2 report body, or null (count text).
const noticeReport = (m) => { try { const v = JSON.parse(m.body); return v && v.v === 2 && Array.isArray(v.items) ? v : null; } catch (e) { return null; } };
// noticeTime is client.noticeTime: a report's at, else the envelope's ts
// (seconds); a row stored before ts was kept: its arrival.
const noticeTime = (m) => { const v = noticeReport(m); return v && Number.isSafeInteger(v.at) && v.at > 0 ? v.at : Number.isSafeInteger(m.ts) ? m.ts : Math.floor((m.at || 0) / 1000); };
// noticeSettled is client.noticeSettled: a report saying nothing waits.
const noticeSettled = (m) => { const v = noticeReport(m); return !!v && v.items.length === 0 && !(Number.isSafeInteger(v.count) && v.count > 0); };
// isoClaim is a time a sender wrote (unix seconds) as the page shows it, or
// "" when no page could show it (not after 1970, or from year 9999 on, as
// ui.maxClaimedUnix): a record's claim never breaks a view.
const isoClaim = (sec) => (Number.isSafeInteger(sec) && sec > 0 && sec < 253370764800 ? iso(sec * 1000) : "");

// checkFiles refuses files beyond what this browser sends.
function checkFiles(files) {
  if (files.length > 8) throw new Error("A message takes at most 8 files.");
  let total = 0;
  for (const f of files) {
    total += f.size;
    if (f.size > wire.BrowserMaxFile) throw new Error(f.name + " is larger than this browser sends (" + (wire.BrowserMaxFile >> 20) + " MiB); send it from a computer with AgentNet.");
  }
  if (total > wire.BrowserMaxMessage) throw new Error("These files are more than this browser sends in one message (" + (wire.BrowserMaxMessage >> 20) + " MiB); send fewer at a time.");
}

// replyKind is the kind of a reply to a message of kind k (client.replyKind).
const replyKind = (k) => (k === "question" ? "answer" : k === "task" ? "result" : "message");
// progressOutput is client.isResponderProgress: a nonterminal output under the
// same authority as an answer or result, never one of them.
const progressOutput = (n) => n.kind === "message" && n.status === wire.StatusProgress && !!n.reply_to;
// agentTurn is client.agentTurn: an ordinary turn that says an agent wrote
// it, a participation's output (answer, result or progress), named or not,
// or any turn whose origin says so. Only the exact host device of its
// participation sends one (checkConversationAgent).
const agentTurn = (n) => !n.sub && (!!n.pid && (n.kind === "answer" || n.kind === "result" || progressOutput(n)) || (n.origin || "").startsWith("agent:"));
// verifiedAgent is client.verifyAgents for one shown row: an agent's turn
// that its participation's exact host key sent (from, fp: this device, the
// key that verified it, or for history the original key its own device
// vouched for). A claimed excerpt, an origin or a name alone never is. It
// speaks for the turn as sent: an edit by another device of the host's
// person is not the host key's.
const verifiedAgent = (m, info, from, fp) => !m.excerpt_pid && !!m.pid && agentTurn(m) && !!info?.invite && !!info.host && info.state !== "conflict" && info.role !== "human" && from === info.host.address && fp === info.host.fingerprint;
const rel0 = (ctls) => ctls.some((x) => x.pid && x.sub === wire.SubReaction); // any assistant reaction to label
// linkReplies is livedm.go linkReplies: a reply names its author's copy
// (an agent's answer, its executor's copy of the request), which this
// device shows under another copy's id or under its logical id. The
// returned function gives the id a reply_to names as shown here; anything
// else stays as sent. A logical id is unique per sender key only (self:
// this device's key, for rows it sent): one that two keys used names no
// one message, so a reply naming it stays as sent.
const linkReplies = (msgs, self) => {
  const keyOf = new Map(), twice = new Set(), shown = new Map();
  for (const m of msgs) {
    const key = m.fp || m.claimed_key || self;
    if (keyOf.has(m.lid) && keyOf.get(m.lid) !== key) twice.add(m.lid);
    keyOf.set(m.lid, key);
  }
  for (const m of msgs) {
    if (m.lid && !twice.has(m.lid)) shown.set(m.lid, m.id);
    for (const c of m.copies || []) shown.set(c.id, m.id);
  }
  for (const m of msgs) shown.set(m.id, m.id); // an exact id is always its own message
  return (replyTo) => (replyTo && shown.has(replyTo) ? shown.get(replyTo) : replyTo || "");
};
// assistantActor is client.assistantWho with its Reactor: an assistant's own
// reaction row's actor (its participation, or its device thread's named
// executor or default responder), and its fallback label (client.assistantLabel:
// the participation host's person label where known, else the host device)
// with host, agent and PID for a label from that host's catalog.
const assistantActor = (c, hostLabel = () => "") => {
  if (c.sub !== wire.SubReaction || !c.from) return null;
  const id = c.pid ? "assistant:" + c.pid : c.agent_id ? "assistant:" + c.from + "/" + c.agent_id : !c.conv && (c.origin || "").startsWith("agent:") ? "assistant:" + c.from + "/default" : "";
  if (!id) return null;
  return { id, label: ((c.pid && hostLabel(c.pid)) || c.from) + " assistant" + (c.agent_id ? " " + c.agent_id.slice(0, 8) : ""), assistant: true, host: c.from, ...(c.agent_id ? { agent_id: c.agent_id } : {}), ...(c.pid ? { pid: c.pid } : {}) };
};

// copyOrder is the least advanced of a message's copies (its state is the
// message's, as the core reports it).
const copyRank = { failed: 0, waiting: 1, queued: 2, custody: 3, delivered: 4 };
export const deliveryRank = state => ({delivered:5,custody:4,waiting:2,quarantined:1,expired:1,failed:0}[state] ?? 3);
export function deliveryOf(copies) {
  const others = copies.some(c => !c.own), people = new Map();
  for (const c of copies) {
    if (others && c.own) continue;
    const key = c.person || c.to, old = people.get(key);
    if (!people.has(key) || deliveryRank(c.state) > deliveryRank(old) || deliveryRank(c.state) === deliveryRank(old) && c.state < old) people.set(key,c.state);
  }
  return [...people.values()].sort((a,b) => deliveryRank(a)-deliveryRank(b) || (a < b ? -1 : a > b ? 1 : 0))[0] || "";
}
const sentAt = m => {const claim=m.to?iso(m.at):isoClaim(m.ts);return claim&&Date.parse(claim)<=m.at?claim:iso(m.at);};
const copyOrder = (recs) => recs.reduce((a, b) => ((copyRank[b.state] ?? 2) < (copyRank[a.state] ?? 2) ? b : a));
const firstLine = (s) => {
  const l = (s || "").split("\n")[0];
  return [...l].length > 120 ? [...l].slice(0, 119).join("") + "…" : l;
};
// ---- topics (client topics.go, docs/plans/TOPICS.md) ---------------------------------------
// Topic tunables: the Go client's (topics.go), the one place to change them
// there; the same values here, pinned by internal/ui topics_browser_test.go.
// Times are seconds.
export const TOPICS = Object.freeze({
  archiveAfter: 7 * 24 * 3600, // quiet this long with nothing pending: archived (TopicArchiveAfter)
  pageDefault: 50,             // topics in one page of the All topics list (TopicPageDefault)
  pageMax: 200,                // the most a page may ask for (TopicPageMax)
  titleMax: 120,               // characters in a name the person gives a topic (TopicTitleMax)
});
const topicOpenIn = new Set(["held", "awaiting", "needs_human", "pending", "accepted", "running", "cancel_requested", "part_waiting"]);
const topicUndelivered = new Set(["failed", "expired", "quarantined"]);
const topicRequest = (k) => k === "question" || k === "task";
// topicOpen: one message keeps its topic pending (client topicOpen).
const topicOpen = (r, replied) => r.notice ? r.state === "needs_human"
  : r.in ? !r.selected && (topicOpenIn.has(r.state) || r.state === "interrupted" && topicRequest(r.kind)) // interrupted: waits for the person (client reviewStates)
  : topicRequest(r.kind) && !replied && !topicUndelivered.has(r.state);
// deriveTopic is a topic's state from its messages (thread order, oldest
// first; facts {id, reply_to, at (seconds), in, kind, state, status,
// notice, selected}), what the person set on it here ({mark, mark_at,
// mark_count}) and now (seconds): client topics.go deriveTopic exactly.
export function deriveTopic(g, local, now) {
  const l = local || {}, v = { state: "active", pending: false, quiet_since: 0 };
  const replied = new Set(g.filter((r) => r.in && r.reply_to && r.status !== "progress").map((r) => r.reply_to));
  for (const r of g) if (topicOpen(r,replied.has(r.id))) v.pending=true;
  const last = g[g.length - 1];
  v.quiet_since = last.at;
  const live = !!l.mark && g.length <= (l.mark_count || 0); // a later message ends the mark
  if (live && (l.mark_at || 0) > v.quiet_since) v.quiet_since = l.mark_at;
  if (live && l.mark === "done") v.done_by = "you";
  else if (live && l.mark === "open") { /* reopened: active */ }
  else if (!v.pending && last.topic_done && last.status === "done" && ["answer","result"].includes(last.kind)) { v.done_by="agent";v.conclusion=last.id; }

  if (v.done_by) v.state = "done";
  if (!v.pending && (live && l.mark === "archived" || now - v.quiet_since >= TOPICS.archiveAfter)) v.state = "archived";
  return v;
}
// Conversation topics: main flow plus opt-in logical chains (client chattopics.go).
export function chatTopicAssignments(msgs) {
 const by=new Map(msgs.filter(m=>!m.sub).map(m=>[m.lid,m])), promoted=new Set(msgs.filter(m=>m.topic_event?.action==="create").map(m=>m.topic)), assigned=new Map();
 const aliases=new Map();for(const m of msgs){if(m.id)aliases.set(m.id,m.lid);for(const c of m.copies||[])if(c.id)aliases.set(c.id,m.lid);}
 for(const m of msgs){let cur=m.lid;const seen=new Set();while(cur&&!seen.has(cur)){seen.add(cur);cur=aliases.get(cur)||cur;const r=by.get(cur);if(!r)break;if(r.topic){assigned.set(m.lid,r.topic);break;}if(promoted.has(cur)){assigned.set(m.lid,cur);break;}cur=r.reply_to;}}
 return assigned;
}
const chatSent=m=>m.ts || (m.envelope?wire.parseEnvelope(m.envelope).ts:Math.floor((m.at||0)/1000));
const chatDepths=g=>{const aliases=new Map();for(const m of g){if(m.id)aliases.set(m.id,m.lid);for(const c of m.copies||[])if(c.id)aliases.set(c.id,m.lid);}const by=new Map(g.map(m=>[m.lid,{...m,reply_to:aliases.get(m.reply_to)||m.reply_to}])),depths=new Map();const depth=(id,seen)=>{if(depths.has(id))return depths.get(id);const m=by.get(id);if(!m||seen.has(id))return 0;seen.add(id);const d=by.has(m.reply_to)?1+depth(m.reply_to,seen):0;seen.delete(id);depths.set(id,d);return d;};for(const m of g)depth(m.lid,new Set());return depths;};
const sortChatTurns=g=>{const d=chatDepths(g);return g.sort((a,b)=>chatSent(a)===chatSent(b)&&d.get(a.lid)!==d.get(b.lid)?d.get(a.lid)-d.get(b.lid):chatOrder(a,b));};
const sortChatEvents=g=>{const d=chatDepths(g);return g.sort((a,b)=>d.get(a.lid)-d.get(b.lid)||chatOrder(a,b));};
const chatOrder=(a,b)=>chatSent(a)-chatSent(b)||(a.lid<b.lid?-1:a.lid>b.lid?1:0);
export function summarizeChatTopics(conv,msgs,locals=new Map(),now=Math.floor(Date.now()/1000)) {
 const assigned=chatTopicAssignments(msgs),groups=new Map(),events=new Map(),out=[];
 for(const m of msgs){if(m.sub)continue;const bag=m.topic_event?events:groups,id=m.topic_event?m.topic:assigned.get(m.lid);if(id)bag.set(id,[...(bag.get(id)||[]),m]);}
 for(const [id,g] of groups){sortChatTurns(g);const first=g[0],last=g.at(-1),local=locals.get(conv+"/"+id)||{};let shared={},by="";
  const aliases=new Map();for(const m of g){if(m.id)aliases.set(m.id,m.lid);for(const c of m.copies||[])if(c.id)aliases.set(c.id,m.lid);}
  const facts=g.map(m=>{const job=m.job||"",state=job||m.state||"";return {id:m.lid,reply_to:aliases.get(m.reply_to)||m.reply_to||"",at:chatSent(m),kind:m.kind,status:m.status||"",topic_done:!!m.topic_done,in:!!job&&!m.to||["answer","result"].includes(m.kind),state};});
  for(const m of sortChatEvents(events.get(id)||[])){if(m.topic_event.action==="create")continue;const seen=new Set(m.topic_event.seen||[]);if(g.every(r=>seen.has(r.lid))){shared={mark:m.topic_event.action,mark_at:chatSent(m),mark_count:g.length};by=m.from||"";}else{shared={};by="";}}
  const l=local.mark==="archived"&&local.mark_count>=g.length+(events.get(id)||[]).length&&local.mark_at>=(shared.mark_at||0)?local:shared,v=deriveTopic(facts,l,now);
  const activity=Math.max(chatSent(last),...(events.get(id)||[]).map(chatSent));
  v.quiet_since=Math.max(v.quiet_since,activity);
  if(v.state==="archived"&&l.mark!=="archived"&&now-v.quiet_since<TOPICS.archiveAfter)v.state=v.done_by?"done":"active";
  const t={id,conv,peer:"",title:firstLine(first.body),last:firstLine(last.body),last_at:iso(activity*1000),count:g.length,state:v.state,pending:v.pending,quiet_since:iso(v.quiet_since*1000),review:g.filter(m=>["held","needs_human"].includes(m.job)).length,unread:0,running:g.filter(m=>m.job==="running").length,waiting:v.pending,key_changed:false,notices:0,notice_only:false};
  if(v.done_by)t.done_by=v.done_by;if(shared.mark==="done"&&v.done_by==="you"){t.done_by="person";t.concluded_by=by;}
  const conclusion=g.find(m=>m.lid===v.conclusion);if(conclusion){t.conclusion=firstLine(conclusion.body);t.concluded_by=conclusion.from||"";}
  if(local.title)Object.assign(t,{auto_title:t.title,title:local.title,renamed:true});out.push(t);
 }
 return out.sort(byNewest);
}

// newerTopic orders topics most recently active first (client newer): by
// whole seconds, as the Go client stores times, then id, then peer. The
// page cursor is "<seconds>|<peer>|<id>", as the Go client's.
const topicAt = (t) => Math.floor(Date.parse(t.last_at) / 1000);
const newerTopic = (a, b) => topicAt(a) !== topicAt(b) ? topicAt(a) > topicAt(b) : a.id !== b.id ? a.id > b.id : a.peer < b.peer;
const byNewest = (a, b) => (newerTopic(a, b) ? -1 : newerTopic(b, a) ? 1 : 0);
const topicCursor = (t) => topicAt(t) + "|" + t.peer + "|" + t.id;
const topicQueryError = "That topic list request is not valid.";
const topicNewer = "A newer message came in, so the topic stays active. Read it, then mark it done again."; // ui topicNewer

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
    this.pictureGeneration = 0;
    this.pictureURLs = new Map();
    this.pictureLoads = new Map();
    this.pubs = new Map(); // address -> parsed directory entry, from the pins
    this.members = { listed: "unknown", current: false, at: 0, list: [], truncated: false };
    this.workspaceName = ""; // the workspace's own name its admin set, as last listed (kept: kv "workspace")
    this.agentDevices = []; // devices that, as last listed, say they run an agent (kept: kv "agent_devices")
    this.connected = false;
    this.revoked = false;
    this.running = false;
    this.version = "";
    this.featureList = null;
    this.sendAbort = new AbortController();
    this.typing = { connected: false, supported: false, generation: 0, seen: new Map(), replay: new Map(), sent: new Map(), timer: null, visible: "[]", abort: new AbortController() };
    this.notKept = new Map(); // received file blob id -> why its ciphertext is not kept here (gone, full, large)
    this.fetching = new Map(); // "ct/" + blob id -> the one fetch of it under way
    this.erased = new Set(); // erased turn names (conv|key|lid), as the erased store holds them
    this.burst = { gap: 0, told: 0, pending: false, quiet: null }; // arrivals told to the page (changed)
    this.keepQueue = []; this.keepScan = false; // received files to keep here (keepFiles)
    this.retractionCache = null; // { mark, rows }: the retractions held, as of the store's retractionMark
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
    this.heldHistoryRecovery = !!id && this.me?.state === "self" && !(await this.store.get("kv", "held-group-history-recovery-v1"));
    // Older DM control copies cached their recipient as the author.
    // These locally signed rows belong to this person; retain their wire bytes.
    if (id && this.me) {
      const rows = (await this.store.all("outbox")).filter(r => r.control && r.conv && r.to && r.fp === this.fp && r.person !== this.me.person);
      if (rows.length) try {
        await this.store.write(rows.map(r => ({s:"outbox",k:r.id,v:{...r,person:this.me.person}})), rows.map(r => ({s:"outbox",k:r.id,v:r})));
      } catch (e) { if (e instanceof StoreConflict) return this.load(); throw e; }
    }
    this.link = (await this.store.get("kv", "link")) || null; // this device's own request to join a person, if it joined with a link
    this.workspaceName = wire.validWorkspaceName(await this.store.get("kv", "workspace"));
    const agents = await this.store.get("kv", "agent_devices");
    this.agentDevices = Array.isArray(agents) ? agents.filter(wire.validAddress) : [];
    await this.loadErased();
    if (this.erased.size) this.scheduleErase(); // after a restart: what a crash left untold or unerased
    if (id) await this.settleLeftoverNotices();
    return !!id;
  }

  get joined() { return !!this.address; }

  listen(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  // changed tells the page something changed. An arrival (a message or
  // receipt the stream brought) is told at once when arrivals start, then
  // at growing gaps while they keep coming, and once when they stop: a
  // catch-up of hundreds of messages had the page read every view again
  // after each one (MEL-546). Anything else is told at once.
  changed(arrival = false) {
    this.seq++;
    if (arrival) this.arrived();
    else this.tell();
    if (this.erased.size) this.scheduleErase(); // a change may end work an erased turn's text was kept for
  }

  tell() {
    this.burst.pending = false;
    this.burst.told = Date.now(); // pacing for the page, not a protocol time
    for (const f of this.listeners) {
      try { f(this.seq); } catch (e) { /* a listener's problem stays there */ }
    }
  }

  arrived() {
    const b = this.burst;
    clearTimeout(b.quiet);
    b.quiet = setTimeout(() => { b.quiet = null; b.gap = 0; if (b.pending) this.tell(); }, burstQuiet);
    if (!b.gap) { b.gap = burstFirst; return this.tell(); }
    if (Date.now() - b.told >= b.gap) { b.gap = Math.min(2 * b.gap, burstMost); return this.tell(); }
    b.pending = true;
  }

  // ---- the relay

  async call(method, path, body, { signed = true, signal } = {}) {
    const text = body === undefined ? "" : typeof body === "string" ? body : JSON.stringify(body);
    const headers = {};
    if (text) headers["Content-Type"] = "application/json";
    if (signed) Object.assign(headers, await wire.signRequest(this.keys, this.address, method, path, text));
    return this.request(method, path, text || undefined, headers, signal);
  }

  async request(method, path, body, headers, signal) {
    // A stalled request must not hold onConnect (and therefore reconnect)
    // forever. Bound headers and body below the stream's heartbeat watchdog.
    const ctrl = new AbortController(), parents = [...new Set([signal, this.sendAbort.signal].filter(Boolean))];
    const abort = () => ctrl.abort();
    for (const parent of parents) {
      parent.addEventListener("abort", abort, { once: true });
      if (parent.aborted) abort();
    }
    const timer = setTimeout(abort, 30_000);
    let r, t;
    try {
      r = await this.fetch(this.base + path, { method, headers, body, cache: "no-store", signal: ctrl.signal });
      if (r.ok) t = await r.text();
      else {
        let j = {};
        try { j = await r.json(); } catch (e) { if (ctrl.signal.aborted) throw e; }
        const err = new HubError(r.status, j.code || "", j.error || r.statusText || "server error");
        if (err.code === "revoked") await this.setRevoked();
        throw err;
      }
    } catch (e) {
      if (e instanceof HubError) throw e;
      throw new HubError(0, "", "cannot reach your server");
    } finally {
      clearTimeout(timer);
      for (const parent of parents) parent.removeEventListener("abort", abort);
    }
    return t ? JSON.parse(t) : null;
  }

  // callBytes is call with a raw body (an upload chunk); getBytes fetches
  // raw bytes (a file's ciphertext).
  async callBytes(method, path, bytes) {
    const headers = { "Content-Type": "application/octet-stream", ...(await wire.signRequest(this.keys, this.address, method, path, bytes)) };
    return this.request(method, path, bytes, headers);
  }

  async getBytes(path, bound) {
    const headers = await wire.signRequest(this.keys, this.address, "GET", path, "");
    let r;
    try {
      r = await this.fetch(this.base + path, { method: "GET", headers, cache: "no-store" });
    } catch (e) {
      throw new HubError(0, "", "cannot reach your server");
    }
    if (!r.ok) throw new HubError(r.status, "", r.status === 404 ? "the file is not on your server (any more)" : r.statusText || "server error");
    if (bound === undefined) return new Uint8Array(await r.arrayBuffer());
    if (!r.body) throw new HubError(0, "", "group carrier transfer incomplete");
    const reader = r.body.getReader(), chunks = [];
    let size = 0;
    try {
      // A linked event ends the approval stream between reads. Some readers
      // do not reject a later read after cancellation; reconnect immediately.
      while (!ctrl.signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        size += value.length;
        if (size > bound) throw new Error("group carrier exceeds signed ciphertext size");
        chunks.push(value);
      }
    } catch (e) {
      await reader.cancel().catch(() => {});
      if (size > bound) throw e;
      throw new HubError(0, "", "group carrier transfer interrupted");
    }
    if (size < bound) throw new HubError(0, "", "group carrier transfer incomplete");
    const bytes = new Uint8Array(size);
    let pos = 0;
    for (const c of chunks) { bytes.set(c, pos); pos += c.length; }
    return bytes;
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
    this.realm = typeof v.realm_id === "string" ? v.realm_id : ""; // the workspace's realm as this authenticated relay states it (pinned per membership by the shell)
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

  async googleNonce() {
    if (this.joined) throw new Error("This browser already joined.");
    let pending = await this.store.get("kv", "google_joining");
    if (!pending) { pending = { keys: await wire.newKeys() }; await put(this.store, "kv", "google_joining", pending); }
    return wire.googleNonce(await wire.publicEntry(pending.keys, "google/browser"));
  }

  async joinGoogle(token, name) {
    if (this.joined) throw new Error("This browser already joined.");
    await this.googleNonce();
    const pending = await this.store.get("kv", "google_joining"), keys = pending.keys;
    const publicHint = await wire.publicEntry(keys, "google/" + name);
    const prepared = await this.call("POST", "/v1/google/prepare", await wire.googleRequest(keys, token, publicHint), { signed: false });
    const pub = await wire.publicEntry(keys, prepared.address);
    let first = pending.first ? await wire.parseRoster(pending.first) : null, link = pending.link;
    if (pending.address && (pending.address !== prepared.address || pending.email !== prepared.email)) throw new Error("Use the same Google account to finish joining.");
    if (link && !prepared.enrolled && link.expires <= Math.floor(this.now() / 1000)) { first = null; link = null; }
    if (!first && !link) {
      if (!prepared.head) first = await wire.newRoster(keys, prepared.address, prepared.name, prepared.email);
      else {
        const h = await wire.parseRoster(prepared.head), ap = await wire.parsePublic(prepared.approver);
        if (h.email !== prepared.email || !(await wire.rosterHas(h, ap.address, await wire.fingerprint(ap)))) throw new Error("Invalid existing person.");
        link = { email: prepared.email, person: h.person, seq: h.seq, roster: await wire.rosterHash(h),
          approver: { address: ap.address, fingerprint: await wire.fingerprint(ap) }, expires: Math.floor(this.now() / 1000) + linkTTL,
          offer: wire.newID(), join: await wire.joinConsent(keys, prepared.address, h.person, h.seq + 1, await wire.rosterHash(h)) };
      }
      await put(this.store, "kv", "google_joining", { keys, address: prepared.address, email: prepared.email, first: first ? wire.rosterJSON(first) : null, link });
    }
    try {
      await this.call("POST", "/v1/google/join", await wire.googleRequest(keys, token, pub, first, link), { signed: false });
    } catch (e) {
      if (["roster_stale", "too_many_devices", "bad_step"].includes(e.code)) await put(this.store, "kv", "google_joining", { keys });
      throw e;
    }
    const fingerprint = await wire.fingerprint(pub);
    const ownLink = link && { google: true, state: "pending", person: link.person, approver: link.approver.address, approver_key: link.approver.fingerprint, expires: link.expires, seq: link.seq + 1 };
    const identity = { address: prepared.address, keys, fingerprint, joined: this.now() };
    this.address = identity.address; this.keys = keys; this.fp = fingerprint;
    const me = first && { ...(await this.personRecord([first], "self", null)), published: true };
    await this.store.write([{ s: "kv", k: "identity", v: identity }, { s: "kv", k: "google_joining", v: undefined },
      ...(ownLink ? [{ s: "kv", k: "link", v: ownLink }] : []), ...(me ? [{ s: "kv", k: "person", v: me }] : [])]);
    await this.load(); if (me) await this.pinDevices(me); this.changed(); return identity.address;
  }

  // join enrolls this browser as the device label/agentName with the
  // invitation code, once. The code is never stored or logged.
  async join(code, agentName) {
    if (this.joined) throw new Error("This browser already holds a device.");
    return this.joinWith(wire.decodeInvite(code), agentName, null);
  }

  // joinAndLink enrolls this browser with a device link code from another
  // device of your person (its QR or its text): it joins as a new device
  // of that person, waiting (the server lets it do nothing else) until
  // that device approves it. The code is never stored or logged.
  async joinAndLink(code, agentName) {
    if (this.joined) throw new Error("This browser already holds a device.");
    const o = wire.decodeOffer(code);
    if (Math.floor(this.now() / 1000) >= o.expires) throw new Error("That link expired: make a new one on your other device.");
    const address = await this.joinWith(wire.decodeInvite(o.invite), agentName, async (keys, addr) => {
      const join = await wire.joinConsent(keys, addr, o.person, o.seq + 1, o.roster);
      return { offer: o.offer, join, mac: await wire.linkMAC(o, await wire.publicEntry(keys, addr), join) };
    }, { state: "pending", person: o.person, seq: o.seq + 1, approver: o.approver.address, approver_key: o.approver.fingerprint, expires: o.expires });
    return address;
  }

  // joinAuto and joinAndLinkAuto join under an automatic device name: base,
  // then base-2, base-3 … (wire.nameCandidates, as the Go app does) while
  // the server says a name is taken. People never name devices.
  async joinAuto(code, base) {
    return this.tryNames(base, (name) => this.join(code, name));
  }

  async joinAndLinkAuto(code, base) {
    return this.tryNames(base, (name) => this.joinAndLink(code, name));
  }

  async tryNames(base, join) {
    let last;
    for (const name of wire.nameCandidates(base, autoNameTries)) {
      try { return await join(name); } catch (e) { if (!e.taken) throw e; last = e; }
    }
    throw new Error("This device's usual names are all taken on your server. Ask your server's admin for help.", { cause: last });
  }

  // joinWith joins with invitation inv (link: the link fields a device
  // link adds, made with the new keys; state: this device's link to keep).
  async joinWith(inv, agentName, link, linkState) {
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
    const body = await wire.joinRequest(pending.keys, address, inv.secret, link ? await link(pending.keys, address) : null);
    try {
      await this.call("POST", "/v1/join", body, { signed: false });
    } catch (e) {
      // Only a taken name is worth another name (client.go join): a stale
      // device link, or a person with too many devices, is not.
      if (e.status === 409 && e.code === "address_taken") throw Object.assign(new Error("The name " + address + " is already used on this server. Choose another name."), { taken: true });
      if (e.status === 409 && e.code === "roster_stale") throw new Error("Your devices changed since that link was made. Make a new one on your other device.");
      if (e.status === 403 && link) throw new Error("That link cannot be used any more (it was used, or it expired): make a new one on your other device.");
      throw new Error("Could not join (" + e.message + "). Check your connection and try again.");
    }
    const fingerprint = await wire.fingerprint(await wire.publicEntry(pending.keys, address));
    await this.store.write([{ s: "kv", k: "identity", v: { address, keys: pending.keys, fingerprint, joined: this.now() } },
      { s: "kv", k: "joining", v: undefined }, ...(linkState ? [{ s: "kv", k: "link", v: linkState }] : [])]);
    await this.load();
    this.changed();
    return address;
  }

  get waitingLink() { return !!(this.link && this.link.state === "pending"); }

  async setLink(state, detail) {
    this.link = { ...this.link, state, detail: detail || "" };
    await put(this.store, "kv", "link", this.link);
    this.changed();
  }

  // finishLink pins this device's person once its server admitted it: the
  // chain must name this device in the step after the offer's, signed by
  // the approving device.
  async finishLink() {
    const s = this.link;
    try {
      const steps = await this.chain(s.person, -1);
      if (!steps.length) throw new Error("no roster");
      await wire.verifyFirst(steps[0]);
      for (let i = 1; i < steps.length; i++) await wire.verifyNext(steps[i], steps[i - 1]);
      const step = steps.find((r) => r.seq === s.seq);
      if (!step || (!s.google && step.by !== s.approver_key) || !(await wire.rosterHas(step, this.address, this.fp))) {
        throw new Error("the person's roster does not name this device in the step its approver signed");
      }
      if (!(await wire.rosterHas(steps[steps.length - 1], this.address, this.fp))) throw new Error("this device is no longer in its person's roster");
      const me = { ...(await this.personRecord(steps, "self", null)), published: true };
      await put(this.store, "kv", "person", me);
      this.me = me;
      await this.pinDevices(me);
      await this.setLink("linked");
    } catch (e) {
      if (retryable(e)) throw e;
      await this.setLink("failed", e.message);
    }
  }

  // A person is kept as the chain of roster steps verified here (TOFU on
  // step 0): { person, label, seq, hash, json (the newest step), hashes
  // (every step's hash), devices (the newest step's: address,
  // fingerprint, json), known (every device any step named, for records
  // they signed), state (self, pinned, conflict) }. address and
  // fingerprint are the one device a view is about: this one for your own
  // person, else the first current device.
  personView(p, extra) {
    return p && { person: p.person, label: p.label, email: p.state === "conflict" ? "" : p.email || "", ...(p.state !== "conflict" && p.picture ? { picture: p.picture, picture_url: this.pictureURL(p.picture) } : {}), address: p.address, fingerprint: p.fingerprint, state: p.state,
      devices: (p.devices || []).map((d) => ({ address: d.address, name: d.address.split("/")[1], fingerprint: d.fingerprint, human:(p.human_keys || []).includes(d.fingerprint), this: d.address === this.address })),
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
    const steps2 = [...((before && before.steps) || [])];
    for (const st of steps) {
      const h = await wire.rosterHash(st);
      if (!steps2.some((x) => x.hash === h)) steps2.push({ hash: h, devices: await Promise.all(st.devices.map(async (d) => d.address + "|" + (await wire.fingerprint(d)))) });
    }
    return { person: r.person, label: r.label, email: r.email || "", picture: r.picture || "", seq: r.seq, human_keys:await wire.rosterHumans(r), hash: await wire.rosterHash(r), json: wire.rosterJSON(r), hashes, devices, known, steps: steps2,
      address: one.address, fingerprint: one.fingerprint, state, published: before ? !!before.published : false };
  }

  async createPerson(label) {
    if (this.me) throw new Error("This device already speaks for \"" + this.me.label + "\"; a second person is not created.");
    if (this.waitingLink) throw new Error("This browser joins as the person of the device that approves it: it creates none.");
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

  // A display label is the person's own claim. Refresh verified published
  // proof first; never invent a person, change device keys, or retry unknown acceptance.
  async personLabelHead(person) {
    const before = this.me;
    if (!before || before.person !== person || before.state !== "self" || !before.published || this.revoked || this.waitingLink) throw new Error("This browser has no published self person available for a display label change.");
    const fp = await wire.fingerprint(await wire.publicEntry(this.keys, this.address));
    if (fp !== this.fp) throw new Error("This browser's signing key changed.");
    const steps = await this.chain(person, -1);
    if (!steps.length || steps[0].person !== person) throw new Error("The published person chain is unavailable; refresh before retrying.");
    await wire.verifyFirst(steps[0]);
    for (let i = 1; i < steps.length; i++) await wire.verifyNext(steps[i], steps[i - 1]);
    const head = steps[steps.length - 1];
    const anchored = async (p) => p && p.person === person && p.state === "self" && steps[p.seq] && await wire.rosterHash(steps[p.seq]) === p.hash;
    if (!await anchored(before) || !await anchored(this.me)) throw new Error("The published person chain conflicts with the pinned self head; refresh before retrying.");
    if (!await wire.rosterHas(head, this.address, fp)) throw new Error("This device no longer speaks for its person.");
    if (await wire.rosterHash(head) !== this.me.hash) {
      const record = { ...(await this.personRecord(steps, "self", this.me)), published: true };
      if (!await anchored(this.me)) throw new Error("Your person changed while refreshing; refresh before retrying.");
      this.me = record;
      await put(this.store, "kv", "person", record); // already-published, fully verified remote proof
      await this.pinDevices(record);
      this.changed();
    }
    return { head, steps };
  }

  personLabelInfo(p, steps) {
    const added = new Map();
    for (const r of steps) {
      const addresses = new Set(r.devices.map((d) => d.address));
      for (const a of added.keys()) if (!addresses.has(a)) added.delete(a);
      for (const a of addresses) if (!added.has(a)) added.set(a, r.seq);
    }
    return { person: p.person, label: p.label, email: p.email || "", ...(p.picture ? { picture: p.picture } : {}), address: this.address, fingerprint: this.fp, roster: p.hash, seq: p.seq, state: p.state,
      devices: p.devices.map((d) => ({ address: d.address, name: d.address.split("/")[1], fingerprint: d.fingerprint,
        ...(d.address === this.address ? { this: true } : {}), added: added.get(d.address) })) };
  }

  async renamePerson(label) { return this.changePersonProfile(label, undefined); }

  async setPersonPicture(png) {
    const bytes = png ? wire.unb64(png, "picture") : new Uint8Array();
    let hash = "";
    if (bytes.length) { wire.validatePicture(bytes); hash = await wire.pictureHash(bytes); await this.callBytes("PUT", "/v1/pictures/" + hash, bytes); }
    return this.changePersonProfile(undefined, hash);
  }

  async changePersonProfile(label, picture) {
    if (label !== undefined) wire.validLabel(label); // no trimming or label-to-identity inference
    const person = this.me?.person;
    if (!person) throw new Error("Set up your person first.");
    for (let attempt = 0; attempt < 2; attempt++) {
      const { head: prev, steps } = await this.personLabelHead(person);
      const name = label === undefined ? prev.label : label;
      const photo = picture === undefined ? prev.picture || "" : picture;
      if (prev.label === name && (prev.picture || "") === photo) return this.personLabelInfo(this.me, steps);
      const next = await wire.nextRoster(this.keys, this.address, prev, prev.devices, null, name, null, photo);
      await wire.verifyNext(next, prev);
      try { await this.call("PUT", "/v1/person", wire.rosterJSON(next)); }
      catch (e) {
        if (attempt === 0 && e instanceof HubError && e.code === "roster_stale") continue;
        throw new Error("Display label change was not confirmed. Refresh your person before retrying.");
      }
      const hash = await wire.rosterHash(next);
      if (!this.me || this.me.person !== person || this.me.state !== "self" || this.revoked) throw new Error("Your self person changed; refresh before retrying.");
      if (this.fp !== next.by || await wire.fingerprint(await wire.publicEntry(this.keys, this.address)) !== next.by || !await wire.rosterHas(next, this.address, this.fp)) throw new Error("This browser's signing identity changed; refresh before retrying.");
      if (this.me.hash !== await wire.rosterHash(prev) && this.me.hash !== hash) {
        const latest = await this.personLabelHead(person); // a delayed response never rolls back newer pinned proof
        return this.personLabelInfo(this.me, latest.steps);
      }
      const me = { ...(await this.personRecord([...steps, next], "self", this.me)), published: true };
      this.me = me;
      await put(this.store, "kv", "person", me); // successful Hub CAS precedes local publication
      this.changed();
      return this.personLabelInfo(me, [...steps, next]);
    }
    throw new Error("Display label change was not confirmed. Refresh your person before retrying.");
  }

  pictureURL(hash) {
    if (!wire.validHash(hash)) return "";
    if (this.pictureURLs.has(hash)) return this.pictureURLs.get(hash);
    if (!this.pictureLoads.has(hash)) {
      const load = this.loadPicture(hash).then(url => { this.pictureURLs.set(hash, url); this.changed(); }).catch(() => {}).finally(() => { if (this.pictureLoads.get(hash) === load) this.pictureLoads.delete(hash); });
      this.pictureLoads.set(hash, load);
    }
    return "";
  }
  async loadPicture(hash) {
    if (this.pictureURLs.has(hash)) return this.pictureURLs.get(hash);
    const generation = this.pictureGeneration;
    const asURL = bytes => { if (generation !== this.pictureGeneration) throw Error("Picture view closed"); const url = URL.createObjectURL(new Blob([bytes], { type: "image/png" })); this.pictureURLs.set(hash,url); return url; };
    let png = await this.store.get("kv", "picture:" + hash);
    if (png) { try { const b = wire.unb64(png, "picture"); wire.validatePicture(b); if (await wire.pictureHash(b) === hash) return asURL(b); } catch {} }
    const resp = await this.fetch(this.base + "/v1/pictures/" + hash, { cache: "force-cache", signal: this.sendAbort.signal });
    if (!resp.ok || !resp.body) throw Error("Picture unavailable");
    const reader = resp.body.getReader(), parts = []; let n = 0;
    try { for (;;) { const { value, done } = await reader.read(); if (done) break; n += value.length; if (n > 65536) throw Error("Picture too large"); parts.push(value); } } catch (e) { await reader.cancel().catch(() => {}); throw e; }
    const bytes = new Uint8Array(n); let at = 0; for (const part of parts) { bytes.set(part, at); at += part.length; }
    wire.validatePicture(bytes);
    if (await wire.pictureHash(bytes) !== hash) throw Error("Picture hash mismatch");
    png = wire.b64(bytes); await put(this.store, "kv", "picture:" + hash, png);
    return asURL(bytes);
  }

  // ---- your devices: this one approves a new device of your person
  // (client/link.go, the existing device's side)

  // linkBook is what this device keeps about its links: offers it made
  // (with their secrets) and the requests that answered them.
  async linkBook() {
    return (await this.store.get("kv", "links")) || { offers: {}, requests: {} };
  }

  // newDeviceLink makes a one-use link a new device of your person joins
  // with: a device invite from the server bound to this offer, and a secret
  // only the link carries (the server never sees it). It opens this page.
  async newDeviceLink() {
    if (!this.me) throw new Error("Set up your person first.");
    if(!await wire.rosterHuman(await wire.parseRoster(this.me.json),this.fp))throw Error("Only your own human devices may link another device; agent hosts cannot.");
    if (!this.me.published) {
      try {
        await this.publishPerson();
      } catch (e) {
        throw new Error("Your person is not on your server yet: this page publishes it when it connects. Try again in a moment.");
      }
    }
    const offer = wire.newID(), expires = Math.floor(this.now() / 1000) + linkTTL;
    const inv = await this.call("POST", "/v1/person/device-invite", { offer, expires });
    const o = { v: 2, invite: inv.code, offer, expires, person: this.me.person, seq: this.me.seq, roster: this.me.hash,
      approver: { address: this.address, fingerprint: this.fp }, secret: globalThis.crypto.getRandomValues(new Uint8Array(32)) };
    const code = wire.encodeOffer(o);
    const book = await this.linkBook();
    book.offers[offer] = { ...o, used: false };
    await put(this.store, "kv", "links", book);
    return { url: this.base + "/#" + code, expires: iso(expires * 1000), app_url: "agentnet://open#" + code };
  }

  // onLinkEvent takes the server's "link" event: a device joined with one
  // of this device's offers. A request that matches its offer (its MAC
  // under the secret, the device's consent to the next step) becomes a
  // request for the person and uses the offer up; anything else changes
  // nothing, so nobody without the link can use it up. Again: no change.
  async onLinkEvent(data) {
    let ev;
    try {
      const j = JSON.parse(data);
      if (j.google) return this.onGoogleLinkEvent(j);
      if (typeof j.offer !== "string" || !wire.validID(j.offer)) throw new Error("offer");
      ev = { offer: j.offer, device: await wire.parsePublic(j.device), join: wire.unb64(j.join, "join"), mac: wire.unb64(j.mac, "mac") };
    } catch (e) {
      return; // malformed: ignored
    }
    const o = (await this.linkBook()).offers[ev.offer];
    if (!o || o.used) return; // not this device's, or used up (the same request again is kept as it was)
    if (!(await wire.checkLinkMAC(o, ev.device, ev.join, ev.mac)) || !(await wire.checkJoin(o.person, o.seq + 1, o.roster, ev.device, ev.join))) return;
    const now = Math.floor(this.now() / 1000);
    if (now >= o.expires || ev.device.address.split("/")[0] !== this.address.split("/")[0] || !this.me || this.me.person !== o.person) return;
    const state = this.me.seq !== o.seq || this.me.hash !== o.roster ? "stale" : "pending"; // shown, never approved
    const book = await this.linkBook();
    if (!book.offers[ev.offer] || book.offers[ev.offer].used) return;
    book.offers[ev.offer].used = true;
    book.requests[ev.offer] = { id: ev.offer, address: ev.device.address, device: wire.marshalPublic(ev.device), fingerprint: await wire.fingerprint(ev.device),
      join: wire.b64(ev.join), requested_at: now, expires: o.expires, state, detail: "" };
    await put(this.store, "kv", "links", book);
    this.changed();
  }

  async onGoogleLinkEvent(ev) {
    const l = ev.google, me = this.me;
    if (!me?.email || l.email !== me.email || l.person !== me.person || !(await wire.rosterHas(await wire.parseRoster(me.json), this.address, this.fp)) || !(await wire.rosterHas(await wire.parseRoster(me.json), l.approver.address, l.approver.fingerprint)) ||
      l.seq !== me.seq || l.roster !== me.hash || l.offer !== ev.offer || !wire.validID(ev.offer) || !Number.isSafeInteger(l.expires) ||
      l.expires <= Math.floor(this.now() / 1000) || l.expires > Math.floor(this.now() / 1000) + wire.MaxLinkTTL) return;
    const dev = await wire.parsePublic(ev.device), join = wire.unb64(ev.join, "join");
    if (dev.address.split("/")[0] !== this.address.split("/")[0]) return;
    if (!(await wire.checkJoin(me.person, me.seq + 1, me.hash, dev, join))) return;
    const book = await this.linkBook();
    if (book.requests[ev.offer]) return;
    book.offers[ev.offer] = { ...l, used: true };
    book.requests[ev.offer] = { id: ev.offer, address: dev.address, device: wire.marshalPublic(dev), fingerprint: await wire.fingerprint(dev),
      join: wire.b64(join), requested_at: Math.floor(this.now() / 1000), expires: l.expires, state: "pending", detail: "" };
    await put(this.store, "kv", "links", book); this.changed();
  }

  async setRequest(id, state, extra) {
    const book = await this.linkBook();
    book.requests[id] = { ...book.requests[id], state, ...extra };
    await put(this.store, "kv", "links", book);
    this.changed();
  }

  // linkRequests are the requests kept here, newest first; a pending one
  // past its time is expired.
  async linkRequests() {
    const now = Math.floor(this.now() / 1000);
    return Object.values((await this.linkBook()).requests)
      .map((r) => (r.state === "pending" && now >= r.expires ? { ...r, state: "expired" } : r))
      .sort((a, b) => b.requested_at - a.requested_at || (a.id < b.id ? -1 : 1));
  }

  // decideLink approves or refuses request id. An approval checks again
  // that the link has not expired and your devices have not changed,
  // signs the roster step that adds the device and publishes it; one the
  // server did not take yet stays approved and is published when this page
  // connects (never as a second, competing step).
  async decideLink(id, accept, agentHost = false) {
    const book = await this.linkBook();
    const r = book.requests[id];
    if (!r) throw new Error("No device link request " + id + " here.");
    const name = r.address.split("/")[1];
    if (r.state === "approved" && accept) return this.publishLink(id);
    if (r.state !== "pending") throw new Error("That request is " + r.state + " already.");
    if (!accept) {
      await this.setRequest(id, "refused");
      await this.call("POST", "/v1/person/device-refuse", { address: r.address }).catch(() => {}); // never admitted; it expires there anyway
      return { note: "Refused: " + name + " did not join as you." };
    }
    if (Math.floor(this.now() / 1000) >= r.expires) {
      await this.setRequest(id, "expired");
      throw new Error("That expired: make a new link on this device and use it again.");
    }
    const o = book.offers[id];
    if (!this.me || this.me.person !== o.person) {
      await this.setRequest(id, "failed", { detail: "this device no longer speaks for that person" });
      throw new Error("That device already belongs to someone.");
    }
    if (this.me.seq !== o.seq || this.me.hash !== o.roster) {
      await this.setRequest(id, "stale");
      throw new Error("Your devices changed meanwhile: make a new link and try again.");
    }
    const prev = await wire.parseRoster(this.me.json);
    if(!await wire.rosterHuman(prev,this.fp))throw Error("Only your own human devices may approve another device; agent hosts cannot.");
    const dev=await wire.parsePublic(JSON.parse(r.device)), humans=await wire.rosterHumans(prev);
    if(!agentHost)humans.push(await wire.fingerprint(dev));
    const next = await wire.nextRoster(this.keys, this.address, prev, [...prev.devices, dev], wire.unb64(r.join, "join"),prev.label,humans);
    await wire.verifyNext(next, prev);
    await this.setRequest(id, "approved", { roster: wire.rosterJSON(next) });
    return this.publishLink(id);
  }

  // publishLink publishes an approved request's roster step, then keeps it
  // as your person here and starts copying your chats to the device.
  async publishLink(id) {
    const r = (await this.linkBook()).requests[id];
    const name = r.address.split("/")[1];
    try {
      await this.call("PUT", "/v1/person", r.roster);
    } catch (e) {
      if (e.code === "roster_stale") {
        await this.setRequest(id, "stale");
        throw new Error("Your devices changed meanwhile: make a new link and try again.");
      }
      if (e.code === "link_expired") {
        await this.setRequest(id, "expired");
        throw new Error("That expired: make a new link on this device and use it again.");
      }
      if (retryable(e)) throw new Error("Approved; your server has not taken it yet. It is sent again when this page connects: keep it open.");
      await this.setRequest(id, "failed", { detail: e.message });
      throw new Error(e.message);
    }
    const next = await wire.parseRoster(r.roster);
    this.me = { ...(await this.personRecord([await wire.parseRoster(this.me.json), next], "self", this.me)), published: true };
    await put(this.store, "kv", "person", this.me);
    await this.pinDevices(this.me);
    await this.setRequest(id, "linked");
    await this.startHistory(r.address, r.fingerprint);
    return { note: name + " is now one of your devices." };
  }

  // retryApproved publishes approved requests the server has not taken yet.
  async retryApproved() {
    for (const r of Object.values((await this.linkBook()).requests)) {
      if (r.state === "approved") await this.publishLink(r.id).catch(() => {});
    }
  }

  // removeDevice takes the device at address out of your person (never the
  // last one); a device a link admitted is revoked on the server with it.
  async removeDevice(address) {
    if (!this.me) throw new Error("Set up your person first.");
    const prev = await wire.parseRoster(this.me.json);
    const keep = prev.devices.filter((d) => d.address !== address);
    if (keep.length === prev.devices.length) throw new Error(address + " is not a device of your person.");
    if (!keep.length) throw new Error("The last device of a person cannot be removed.");
    const next = await wire.nextRoster(this.keys, this.address, prev, keep, null);
    try {
      await this.call("PUT", "/v1/person", wire.rosterJSON(next));
    } catch (e) {
      if (e.code === "roster_stale") {
        await this.refreshPerson(this.me).catch(() => {});
        throw new Error("Your devices changed meanwhile: try again.");
      }
      throw new Error(e.message);
    }
    this.me = { ...(await this.personRecord([prev, next], "self", this.me)), published: true };
    await put(this.store, "kv", "person", this.me);
    this.changed();
    return { note: address + " is no longer one of your devices." };
  }

  // ---- copying your chats to a new device (client/history.go): one job
  // per linked device, each step queuing a page of history copies
  // and where it ended in one write, so it resumes where it stopped (the
  // page must be open for it to run).

  async historyBook() {
    return (await this.store.get("kv", "history")) || {};
  }

  async ownHistoryAuthority(dev, checks=[]) {
    const own=await this.groupRead(checks,"kv","person"),identity=await this.groupRead(checks,"kv","identity");
    if(this.revoked || identity?.revoked || identity?.address!==this.address || identity.fingerprint!==this.fp || own?.state!=="self")return false;
    for(const [address,fp] of [[this.address,this.fp],[dev.address,dev.fingerprint]]) {
      if(!own.devices.some(d=>d.address===address&&d.fingerprint===fp)||!own.human_keys?.includes(fp))return false;
      const pin=await this.groupRead(checks,"pins",address);
      if(pin?.pending || pin && pin.fingerprint!==fp)return false;
    }
    return true;
  }

  // Only missing snapshots: the approver may not hold this device's history.
  // Existing jobs (including ended/done) and their positions remain intact.
  async reconcileHistory() {
    const checks=[],own=await this.groupRead(checks,"kv","person"),saved=await this.groupRead(checks,"kv","history"),book=structuredClone(saved||{}),added=[];
    for(const dev of own?.devices||[]) {
      if(dev.address===this.address || book[dev.address] || !await this.ownHistoryAuthority(dev,checks))continue;
      book[dev.address]={device:dev.address,fingerprint:dev.fingerprint,pos:null,done:0,total:(await this.store.all("convs")).length,state:"running",own_human:this.fp};
      added.push(dev);
    }
    if(!added.length)return;
    await this.store.write([{s:"kv",k:"history",v:book}],checks);
    this.changed();
    for(const dev of added)await this.replayErased(dev);
  }

  async discoveredHistoryDelivery(rec) {
    if(!["history",wire.SubGroupProof,wire.SubGroupContext,wire.SubRootSync].includes(rec.sub))return;
    const job=(await this.historyBook())[rec.to];
    if(job?.own_human && (job.own_human!==this.fp || !await this.ownHistoryAuthority({address:job.device,fingerprint:job.fingerprint})))throw Error("History snapshot requires current own human devices and unchanged keys.");
  }

  async startHistory(address, fingerprint) {
    const book = await this.historyBook();
    if (!book[address]) {
      book[address] = { device: address, fingerprint, pos: null, done: 0, total: (await this.store.all("convs")).length, state: "running" };
      await put(this.store, "kv", "history", book);
      this.changed();
      await this.replayErased({ address, fingerprint }).catch(() => {}); // what was deleted here stays deleted there
    }
    this.syncRoots().catch(() => {});this.syncReadMarks().catch(()=>{});this.syncInvitations().catch(()=>{});
    this.runHistory().catch(() => {});
  }

  runHistory() {
    this.historyWake = (this.historyWake || 0) + 1;
    this.historyAgain = true;
    if (!this.historyRun) this.historyRun = this.historyPasses().finally(() => { this.historyRun = null; });
    return this.historyRun;
  }

  // Quiet signed DM roots need no HistoryItem. Current own-human devices only;
  // the existing outbox is a durable per-root/per-key marker, not a new counter.
  async rootSyncAuthority(root, from, fromFP, to, toFP, checks=[]) {
    const own=await this.groupRead(checks,"kv","person");
    if(!own || own.state!=="self" || from===to || !wire.rootMember(root,own.person) || ![ [from,fromFP],[to,toFP] ].every(([address,fp])=>own.devices.some(d=>d.address===address&&d.fingerprint===fp)&&(own.human_keys||[]).includes(fp)))throw Error("Root sync is only between current own-human devices.");
    for(const [address,fp] of [[from,fromFP],[to,toFP]]) {
      if(address===this.address&&fp===this.fp)continue;
      const pin=await this.groupRead(checks,"pins",address);
      if(!pin || pin.pending || pin.fingerprint!==fp)throw Error("Root sync device key changed.");
    }
    const people=new Map([[own.person,own]]);
    for(const m of root.members) {
      const p=m.person===own.person?own:await this.groupRead(checks,"persons",m.person);
      if(!p || p.state==="conflict" || !p.hashes.includes(m.roster))throw Error("Root sync member proof is unavailable or frozen.");people.set(m.person,p);
    }
    const creator=people.get(root.creator.person),step=creator?.steps.find(s=>s.hash===root.creator.roster);
    const dev=creator?.known.find(d=>d.address===root.creator.address&&d.fingerprint===root.creator.fingerprint);
    if(!dev || !step?.devices.includes(dev.address+"|"+dev.fingerprint))throw Error("Root sync creator is not in its bound roster.");
    await wire.verifyRoot(root,(await wire.parsePublic(JSON.parse(dev.json))).sign_key);
    return own;
  }

  readRef(m) {
    if(!m || m.local || m.control || m.ref || m.aside || [wire.SubInvitationSync,wire.SubReadSync,wire.SubRootSync,wire.SubDriveSpace,wire.SubGroupProof,wire.SubGroupContext,wire.SubGroupInvite,wire.SubGroupConsent,wire.SubGroupWithdrawal].includes(m.sub))return null;
    const ref={conv:m.conv||"",fingerprint:m.fp||m.claimed_key||"",lid:m.lid||m.id};
    if(!wire.validFingerprint(ref.fingerprint) || !wire.validID(ref.lid))return null;
    return ref;
  }
  readMarkKey(owner,ref) { return "read-mark/"+owner+"/"+ref.conv+"/"+ref.fingerprint+"/"+ref.lid; }
  async readSyncAuthority(r,from,fromFP,to,toFP,checks=[]) {
    const own=await this.groupRead(checks,"kv","person");
    if(!own || own.state!=="self" || own.person!==r.person || !own.hashes.includes(r.roster) || from===to || ![[from,fromFP],[to,toFP]].every(([address,fp])=>own.devices.some(d=>d.address===address&&d.fingerprint===fp)&&(own.human_keys||[]).includes(fp)))throw Error("Read sync is only between current own-human devices.");
    for(const [address,fp] of [[from,fromFP],[to,toFP]]) {
      if(address===this.address&&fp===this.fp)continue;
      const pin=await this.groupRead(checks,"pins",address);
      if(!pin || pin.pending || pin.fingerprint!==fp)throw Error("Read sync device key changed.");
    }
    return own;
  }
  syncReadMarks() {
    this.readSyncAgain=true;
    if(!this.readSyncRun)this.readSyncRun=(async()=>{do{this.readSyncAgain=false;await this.syncReadPages();}while(this.readSyncAgain);})().finally(()=>{this.readSyncRun=null;});
    return this.readSyncRun;
  }
  async syncReadPages() {
    for(;;) {
      const own=await this.store.get("kv","person");
      if(!own || own.state!=="self" || !own.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp) || !(own.human_keys||[]).includes(this.fp))return;
      // Upgrade recovery excludes historical replicas automatically marked read
      // at import. An explicit local markRead still captures them.
      const recoveryKey="read-recovered/"+own.person,recovery=[],recoveryChecks=[];
      if(!await this.groupRead(recoveryChecks,"kv",recoveryKey)) {
        for(const m of await this.store.all("inbox")) {
          const ref=this.readRef(m);if(!ref || !m.read || m.history)continue;
          const key=this.readMarkKey(own.person,ref);
          if(!await this.groupRead(recoveryChecks,"kv",key))recovery.push({s:"kv",k:key,v:{owner:own.person,ref}});
        }
        recovery.push({s:"kv",k:recoveryKey,v:{done:true}});await this.store.write(recovery,recoveryChecks);
      }
      const marks=(await this.store.all("kv")).filter(m=>m?.owner===own.person&&m.ref),saved=await this.store.all("outbox"),ops=[],checks=[];let more=false;
      // A single O(mark count + outbox references) pass, never one scan per mark.
      const present=new Set();for(const o of saved)if(o.sub===wire.SubReadSync&&o.read_owner===own.person&&["queued","waiting","custody","delivered"].includes(o.state))for(const key of o.read_refs||[])present.add(o.recipient_fp+"|"+key);
      for(const dev of own.devices) {
        if(dev.address===this.address || !(own.human_keys||[]).includes(dev.fingerprint))continue;
        const refs=marks.filter(m=>!present.has(dev.fingerprint+"|"+this.readMarkKey(own.person,m.ref))).slice(0,64).map(m=>m.ref);if(!refs.length)continue;
        const body=JSON.stringify({v:1,person:own.person,roster:own.hash,refs}),r=wire.parseReadSync(body);
        try{await this.readSyncAuthority(r,this.address,this.fp,dev.address,dev.fingerprint,checks);}catch(e){continue;}
        const id=wire.newID(),at=this.now(),envelope=await wire.seal({v:wire.Version2,id,from:this.address,to:dev.address,ts:Math.floor(at/1000),kind:"message",sub:wire.SubReadSync,replica:true,body},this.keys,await wire.parsePublic(JSON.parse(dev.json)));
        ops.push({s:"outbox",k:id,v:{id,to:dev.address,recipient_fp:dev.fingerprint,required_cap:wire.CapReadSync,sub:wire.SubReadSync,body,envelope,at,state:"queued",aside:true,read_owner:own.person,read_refs:refs.map(ref=>this.readMarkKey(own.person,ref))}});
        if(refs.length===64)more=true;if(ops.length===historyPage)break;
      }
      if(!ops.length)return;
      await this.store.write(ops,checks);this.changed();if(this.connected)this.queueOutbox();
      if(!more && ops.length<historyPage)return;
    }
  }
  async readSyncGate(rec) {
    await this.refreshPerson(this.me);
    const checks=[],r=wire.parseReadSync(rec.body);
    await this.readSyncAuthority(r,this.address,this.fp,rec.to,rec.recipient_fp,checks);
    const pin=await this.store.get("pins",rec.to),features=await this.features(),profile=await this.profile(rec.to);
    if(!features.includes("env2") || !features.includes("caps") || !await wire.profileSupports(profile,rec.to,(await this.pubOf(pin)).sign_key,wire.CapReadSync))throw Object.assign(Error("This device needs to update AgentNet to synchronize read state."),{code:"read_sync_unsupported"});
    // Recheck current admission after the asynchronous profile lookup.
    await this.readSyncAuthority(r,this.address,this.fp,rec.to,rec.recipient_fp);
    return {why:"",pin};
  }
  async admitReadSync(n,env,pin) {
    const r=wire.parseReadSync(n.body),checks=[],ops=[];
    if(!this.me || this.me.person!==r.person)throw new Hold("invalid","Read sync belongs to another person.");
    await this.refreshPerson(this.me);
    try{await this.readSyncAuthority(r,env.from,pin.fingerprint,this.address,this.fp,checks);}catch(e){throw new Hold("invalid",e.message);}
    for(const ref of r.refs) {
      const k=this.readMarkKey(r.person,ref);
      if(!await this.groupRead(checks,"kv",k))ops.push({s:"kv",k,v:{owner:r.person,ref}});
      for(const m of await this.store.all("inbox")) {
        const actual=this.readRef(m);if(!actual || this.readMarkKey(r.person,actual)!==k || m.read)continue;
        const current=await this.groupRead(checks,"inbox",m.id);if(current)ops.push({s:"inbox",k:m.id,v:{...current,read:true}});
      }
    }
    ops.checks=checks;ops.readSync=true;return ops;
  }
  invitationViewKey(fp,id) { return "own-invitation/"+fp+"/"+id; }
  syncInvitations() {
    this.invitationSyncAgain=true;
    if(!this.invitationSyncRun)this.invitationSyncRun=(async()=>{
      do {this.invitationSyncAgain=false;try {await this.syncInvitationPages();}catch(e){if(e instanceof StoreConflict)this.invitationSyncAgain=true;else throw e;}}while(this.invitationSyncAgain);
    })().finally(()=>{this.invitationSyncRun=null;});
    return this.invitationSyncRun;
  }
  async syncInvitationPages() {
    for(;;) {
      const checks=[],ops=[],own=await this.groupRead(checks,"kv","person");
      if(!own || own.state!=="self" || !(own.human_keys||[]).includes(this.fp) || !own.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp))return;
      const local=(await this.store.prefix("kv","group-invitation/out/")).filter(v=>v?.type==="group-invitation"&&v.direction==="out"&&v.inviter===this.address&&v.fp===this.fp&&v.owner===own.person),views=[];
      for(const saved of local) {
        const row=await this.groupRead(checks,"kv","group-invitation/out/"+saved.id);if(!row)continue;
        const k=this.invitationViewKey(this.fp,row.id),old=await this.groupRead(checks,"kv",k);
        const r={v:1,person:own.person,roster:own.hash,id:row.id,revision:old?.record.revision||1,status:row.status,proposal:row.proposal};
        if(old&&wire.invitationSyncJSON(r)!==wire.invitationSyncJSON(old.record))r.revision++;
        await wire.validateInvitationSync(r);
        if(!old||r.revision!==old.record.revision)ops.push({s:"kv",k,v:{type:"own-invitation",from:this.address,fp:this.fp,record:r}});
        views.push(r);
      }
      const saved=await this.store.all("outbox"),present=new Set(saved.filter(o=>o.sub===wire.SubInvitationSync&&["queued","waiting","custody","delivered"].includes(o.state)).map(o=>o.recipient_fp+"/"+o.invitation_id+"/"+o.invitation_revision));
      let count=0;
      outer:for(const dev of own.devices) {
        if(dev.address===this.address||!(own.human_keys||[]).includes(dev.fingerprint))continue;
        for(const r of views) {
          if(present.has(dev.fingerprint+"/"+r.id+"/"+r.revision))continue;
          try{await this.readSyncAuthority(r,this.address,this.fp,dev.address,dev.fingerprint,checks);}catch{continue;}
          const body=wire.invitationSyncJSON(r),id=wire.newID(),at=this.now(),envelope=await wire.seal({v:wire.Version2,id,from:this.address,to:dev.address,ts:Math.floor(at/1000),kind:"message",sub:wire.SubInvitationSync,replica:true,body},this.keys,await wire.parsePublic(JSON.parse(dev.json)));
          ops.push({s:"outbox",k:id,v:{id,to:dev.address,recipient_fp:dev.fingerprint,required_cap:wire.CapOwnSyncV2,sub:wire.SubInvitationSync,body,envelope,at,state:"queued",aside:true,invitation_id:r.id,invitation_revision:r.revision}});
          if(++count===historyPage)break outer;
        }
      }
      if(ops.length){await this.store.write(ops,checks);this.changed();if(this.connected)this.queueOutbox();}
      if(count<historyPage)return;
    }
  }
  async invitationSyncGate(rec) {
    await this.refreshPerson(this.me);
    const r=await wire.validateInvitationSync(wire.parseInvitationSync(rec.body));
    await this.readSyncAuthority(r,this.address,this.fp,rec.to,rec.recipient_fp);
    const pin=await this.store.get("pins",rec.to),features=await this.features(),profile=await this.profile(rec.to);
    if(!features.includes("env2")||!features.includes("caps")||!await wire.profileSupports(profile,rec.to,(await this.pubOf(pin)).sign_key,wire.CapOwnSyncV2))throw Object.assign(Error("This device needs to update AgentNet to synchronize invitations."),{code:"invitation_sync_unsupported"});
    await this.readSyncAuthority(r,this.address,this.fp,rec.to,rec.recipient_fp);
    return {why:"",pin};
  }
  async admitInvitationSync(n,env,pin) {
    const r=await wire.validateInvitationSync(wire.parseInvitationSync(n.body)),checks=[],ops=[];
    if(!this.me||this.me.person!==r.person)throw new Hold("invalid","Invitation view belongs to another person.");
    await this.refreshPerson(this.me);
    try{await this.readSyncAuthority(r,env.from,pin.fingerprint,this.address,this.fp,checks);}catch(e){throw new Hold("invalid",e.message);}
    const k=this.invitationViewKey(pin.fingerprint,r.id),old=await this.groupRead(checks,"kv",k);
    if(old?.record.revision===r.revision&&wire.invitationSyncJSON(old.record)!==wire.invitationSyncJSON(r))throw new Hold("invalid","Invitation view has a conflicting revision.");
    if(!old||old.record.revision<r.revision)ops.push({s:"kv",k,v:{type:"own-invitation",from:env.from,fp:pin.fingerprint,record:r}});
    ops.checks=checks;return ops;
  }

  async applyReadArrivals(ops,checks) {
    const own=await this.groupRead(checks,"kv","person");if(!own || own.state!=="self")return;
    for(const op of ops)if(op.s==="inbox"&&op.v&&!op.v.read) {
      const ref=this.readRef(op.v);if(ref&&await this.groupRead(checks,"kv",this.readMarkKey(own.person,ref)))op.v={...op.v,read:true};
    }
  }

  syncRoots() {
    this.rootSyncAgain=true;
    if(!this.rootSyncRun)this.rootSyncRun=(async()=>{do{this.rootSyncAgain=false;await this.syncRootPages();}while(this.rootSyncAgain);})().finally(()=>{this.rootSyncRun=null;});
    return this.rootSyncRun;
  }

  async syncRootPages() {
    for(;;) {
      const own=await this.store.get("kv","person");
      if(!own || own.state!=="self" || !own.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp) || !(own.human_keys||[]).includes(this.fp))return;
      const ops=[],checks=[];
      for(const c of await this.store.all("convs")) {
        if(c.kind==="group" || this.erasedConv(c.id))continue;
        const root=wire.parseRoot(c.root);if(!wire.rootMember(root,own.person))continue;
        for(const dev of own.devices) {
          if(dev.address===this.address || !(own.human_keys||[]).includes(dev.fingerprint))continue;
          try { await this.rootSyncAuthority(root,this.address,this.fp,dev.address,dev.fingerprint,checks); } catch(e) { continue; }
          const saved=await this.authorityRows({conv:c.id,sub:wire.SubRootSync},checks);
          if(saved.some(r=>r.to===dev.address&&r.recipient_fp===dev.fingerprint&&r.required_cap===wire.CapRootSync&&["queued","waiting","custody","delivered"].includes(r.state)))continue;
          const id=wire.newID(),lid=wire.newID(),body='{"v":1}',at=this.now();
          const envelope=await wire.seal({v:wire.Version2,id,from:this.address,to:dev.address,ts:Math.floor(at/1000),kind:"message",conv:c.id,lid,root:c.root,sub:wire.SubRootSync,replica:true,body},this.keys,await wire.parsePublic(JSON.parse(dev.json)));
          ops.push({s:"outbox",k:id,v:{id,lid,conv:c.id,to:dev.address,recipient_fp:dev.fingerprint,required_cap:wire.CapRootSync,sub:wire.SubRootSync,body,envelope,at,state:"queued",aside:true}});
          if(ops.length===historyPage)break;
        }
        if(ops.length===historyPage)break;
      }
      if(!ops.length)return;
      await this.store.write(ops,checks);this.changed();
      if(this.connected)this.queueOutbox();
      if(ops.length<historyPage)return;
    }
  }

  async rootSyncGate(rec) {
    const c=await this.store.get("convs",rec.conv);if(!c)throw Error("Root sync conversation is unavailable.");
    await this.refreshPerson(this.me);
    await this.rootSyncAuthority(wire.parseRoot(c.root),this.address,this.fp,rec.to,rec.recipient_fp);
    const pin=await this.store.get("pins",rec.to);
    const features=await this.features(),prof=await this.profile(rec.to);
    const ok=features.includes("env2")&&features.includes("caps")&&await wire.profileSupports(prof,rec.to,(await this.pubOf(pin)).sign_key,wire.CapRootSync);
    if(!ok)throw Object.assign(Error("This device needs to update AgentNet to receive empty chats."),{code:"root_sync_unsupported"});
    return {why:"",pin};
  }

  async admitRootSync(n,env,pin) {
    const root=wire.parseRoot(n.root),own=this.me;
    if(!own || !own.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint) || !(own.human_keys||[]).includes(pin.fingerprint))throw new Hold("invalid","Root sync comes only from current own-human devices.");
    for(const m of root.members)if(m.person!==own.person)await this.pinChain(m.person);
    const checks=[];
    try {await this.rootSyncAuthority(root,env.from,pin.fingerprint,this.address,this.fp,checks);}catch(e){throw new Hold("invalid",e.message);}
    const c=await this.groupRead(checks,"convs",n.conv),other=root.members.find(m=>m.person!==own.person),ops=[];
    if(!c)ops.push({s:"convs",k:n.conv,v:{id:n.conv,root:wire.rootJSON(root),peer:other.person,created:root.created,creator:root.creator.address}});
    ops.push({s:"inbox",k:env.id,v:{id:env.id,from:env.from,fp:pin.fingerprint,conv:n.conv,lid:n.lid,kind:"message",sub:wire.SubRootSync,body:n.body,replica:true,aside:true,read:true,at:this.now()}});
    ops.checks=checks;ops.rootSync=true;return ops;
  }

  async historyPasses() {
    this.historyWake = (this.historyWake || 0) + 1;
    for (;;) {
      this.historyAgain = false;
      await this.reconcileHistory();
      let more = false;
      for (const j of Object.values(await this.historyBook())) {
        if (!["running", "done"].includes(j.state)) continue;
        const dev = this.me && this.me.devices.find((d) => d.address === j.device && d.fingerprint === j.fingerprint);
        if (!dev) { // no longer a device of your person
          await put(this.store, "kv", "history", { ...(await this.historyBook()), [j.device]: { ...j, state: "ended" } });
          this.changed();
          continue;
        }
        try{more = (await this.historyStep(dev, j)) || more;}
        catch(e){if(!(e instanceof StoreConflict))throw e;this.historySweeps?.delete(dev.fingerprint);more=true;}
      }
      if (!more && !this.historyAgain) return;
    }
  }

  // historyStep queues the next page of history for dev after job j's
  // position: every conversation's messages in (conv, time, id) order,
  // received ones with the key they came under, each message sent here
  // once (its first copy), none the device itself sent.
  async historyStep(dev, j) {
    if(j.own_human || j.catchup || await this.ownHistoryAuthority(dev))return this.historyCatchupStep(dev,j);
    const checks=[],book=structuredClone(await this.groupRead(checks,"kv","history"));
    if(!book?.[dev.address] || JSON.stringify(book[dev.address])!==JSON.stringify(j))return false;
    if(j.own_human && (j.own_human!==this.fp || !await this.ownHistoryAuthority(dev,checks)))return false;
    const contextOnly=j.state==="done";
    const convs = new Map((await this.store.all("convs")).filter((c) => wire.rootMember(wire.parseRoot(c.root), this.me.person)).map((c) => [c.id, c])); // an outside host never lends ambient room history to its siblings
    for(const g of await this.store.all("kv"))if(g?.root&&g.context&&g.records) {
      const root=wire.parseGroupRoot(g.root),conv=await wire.rootID(root);
      try{await this.groupCurrent(conv);convs.set(conv,await this.groupRecord(conv));}catch(e){ /* unavailable authority exports nothing */ }
    }
    const copies=[];
    for(const c of convs.values())if(c.kind==="group")copies.push(...await this.groupHistoryCarriers(c,dev,checks));
    if(contextOnly && !copies.length)return false;
    const rows = [];
    if(!contextOnly) {
    for (const m of await this.store.all("inbox")) { // never a deleted turn or a deletion record (client historyPage)
      if (m.conv && convs.has(m.conv) && m.fp !== dev.fingerprint && m.sub !== wire.SubRootSync && m.sub !== wire.SubClear && !this.erasedRow(m)) rows.push({ conv: m.conv, ms: m.at, id: m.id, m, here: false });
    }
    const first = new Map();
    for (const r of await this.store.all("outbox")) {
      if (!r.conv || r.aside || !convs.has(r.conv) || this.erasedRow(r)) continue;
      const f = first.get(r.lid);
      if (!f || r.id < f.id) first.set(r.lid, r);
    }
    for (const r of first.values()) rows.push({ conv: r.conv, ms: r.at, id: r.id, m: r, here: true });
    }
    const cmp = (a, b) => (a.conv !== b.conv ? (a.conv < b.conv ? -1 : 1) : a.ms !== b.ms ? a.ms - b.ms : a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
    const page = rows.filter((r) => !j.pos || cmp(r, j.pos) > 0).sort(cmp).slice(0, historyPage);
    for (const r of page) copies.push(await this.historyCopy(dev, convs.get(r.conv), this.itemOf(r.m, r.here)));
    const pos = page.length ? { conv: page[page.length - 1].conv, ms: page[page.length - 1].ms, id: page[page.length - 1].id } : j.pos;
    const state = page.length < historyPage ? "done" : "running";
    const done = state === "done" ? convs.size : [...convs.keys()].filter((c) => pos && c < pos.conv).length;
    book[dev.address] = { ...book[dev.address], pos, state, done, total: convs.size };
    await this.store.write([...copies.map((c) => ({ s: "outbox", k: c.id, v: c })), { s: "kv", k: "history", v: book }],checks);
    this.changed();
    if (this.connected) for (const c of copies) await this.post(c);
    return state === "running";
  }

  // v2 retains the old snapshot cursor. A separate recent/older pass repairs
  // accepted pre-upgrade gaps once, while a storage-arrival tail keeps moving
  // during backfill. Exact original tuples, never timestamps, deduplicate it.
  async historyCatchupStep(dev,j) {
    const checks=[],book=structuredClone(await this.groupRead(checks,"kv","history"));
    if(!book?.[dev.address] || JSON.stringify(book[dev.address])!==JSON.stringify(j))return false;
    if(j.own_human&&j.own_human!==this.fp || j.catchup?.source&&j.catchup.source!==this.fp || !await this.ownHistoryAuthority(dev,checks))return false;
    // Arrivals after this boundary belong to the next tail page; they need
    // not invalidate unrelated source authority or starve this transaction.
    const ceiling=await this.store.get("kv","history-arrival")||0;
    const p=structuredClone(j.catchup||{v:2,source:this.fp,stage:"recent",recent:"",older:null,tail:ceiling,ceiling,started:this.now(),legacy_running:j.state==="running"});
    if(p.v!==2)throw Error("Unsupported local history progress.");
    const convs=new Map();
    for(const c of await this.store.all("convs"))if(c.root&&wire.rootMember(wire.parseRoot(c.root),this.me.person))convs.set(c.id,c);
    for(const g of await this.store.prefix("kv","group/"))if(g?.root) {
      const root=wire.parseGroupRoot(g.root),id=await wire.rootID(root);
      convs.set(id,{id,kind:"group",root:g.root});
    }
    const prefix="history-deferred/"+dev.fingerprint+"/",copyPrefix="history-copy/"+dev.fingerprint+"/";
    this.historySweeps ||= new Map();
    let sweep=this.historySweeps.get(dev.fingerprint);
    if(!sweep||sweep.wake!==this.historyWake) {sweep={wake:this.historyWake,after:prefix,done:false,contextAfter:""};this.historySweeps.set(dev.fingerprint,sweep);}
    const ops=[],copies=[],visiting=new Set(),attempted=new Map(),contexts=new Set();
    const pending=async(ref,why)=>{const k=prefix+ref.tuple;await this.groupRead(checks,"kv",k);ops.push({s:"kv",k,v:{...ref,key:k,why}});};
    const clear=async tuple=>{const k=prefix+tuple;if(await this.groupRead(checks,"kv",k))ops.push({s:"kv",k});};
    const context=async c=>{
      if(c.kind!=="group"||contexts.has(c.id))return;
      const carriers=await this.groupHistoryCarriers(c,dev,checks);copies.push(...carriers);contexts.add(c.id);
    };
    const queue=async(row,here=false)=>{
      const s=here?"outbox":"inbox",m=await this.groupRead(checks,s,row.id);
      if(!m||!historySource(m)||this.erasedRow(m))return;
      const item=this.itemOf(m,here),tuple=m.conv+"/"+item.from_key+"/"+item.lid;
      if(item.from_key===dev.fingerprint)return true;
      if(visiting.has(tuple))throw new Hold("proof_pending","Historical dependency is cyclic.");
      if(attempted.has(tuple))return attempted.get(tuple);
      attempted.set(tuple,false);visiting.add(tuple);
      const ref={tuple,conv:m.conv,id:m.id,dir:s};
      try {
        const hash=item.ref?await this.groupControlHash(m.conv,item):await wire.groupHistoryContentHash(m.conv,item),key=copyPrefix+tuple;
        const saved=await this.groupRead(checks,"kv",key);
        if(saved&&saved.hash!==hash)throw new Hold("conflicting_duplicate","Historical original conflicts with its queued copy.");
        if(saved?.copy) {
          const sent=await this.groupRead(checks,"outbox",saved.copy);
          if(sent?.to===dev.address&&sent.recipient_fp===dev.fingerprint&&sent.sub==="history"&&["queued","waiting","custody","delivered"].includes(sent.state)) {
            const h=wire.parseHistory(sent.body),actual=h.ref?await this.groupControlHash(m.conv,h):await wire.groupHistoryContentHash(m.conv,h);
            if(h.from_key!==item.from_key||h.lid!==item.lid||actual!==hash)throw new Hold("conflicting_duplicate","Historical copy ledger differs from its durable envelope.");
            await clear(tuple);attempted.set(tuple,true);return true;
          }
        }
        if(copies.length>=historyPage*3)throw new Hold("proof_pending","Historical dependencies continue in bounded backfill.");
        const c=convs.get(m.conv);if(!c)throw new Hold("proof_pending","Historical conversation root unavailable.");
        await context(c);
        const dependencies=[];
        if(item.pid&&item.sub!=="event") {
          const events=(await this.authorityRows({conv:m.conv,pid:item.pid,sub:"event"},checks)).filter(historySource);
          events.sort((a,b)=>{const rank=r=>({invite:0,scope:1,accept:2}[wire.parseEvent(r.body).type]??3);return rank(a)-rank(b)||a.at-b.at||a.id.localeCompare(b.id);});
          dependencies.push(...events);
        }
        const target=item.ref?.id||item.reply_to;
        if(target) {
          for(const table of ["inbox","outbox"]) {const r=await this.groupRead(checks,table,target);if(r&&r.conv===m.conv&&historySource(r))dependencies.push(r);}
          if(!dependencies.some(r=>r.id===target))dependencies.push(...(await this.authorityRows({conv:m.conv,lid:target},checks)).filter(historySource));
        }
        for(const d of dependencies)if(!await queue(d,!!d.to))throw new Hold("proof_pending","Historical dependency remains unavailable.");
        const rec=await this.historyCopy(dev,c,item,checks);rec.recipient_fp=dev.fingerprint;
        copies.push(rec);ops.push({s:"kv",k:key,v:{hash,copy:rec.id}});await clear(tuple);attempted.set(tuple,true);return true;
      } catch(e) {
        if(e instanceof StoreConflict)throw e;
        await pending(ref,e instanceof Hold?e.reason:"proof_pending");return false;
      } finally {visiting.delete(tuple);}
    };
    // A bounded fresh-arrival page takes precedence on every step, even while
    // another conversation's older pages or unresolved proof are pending.
    const tail=await this.store.historyRows("inbox",{arrival:true,after:p.tail,ceiling});
    for(const row of tail)await queue(row);
    if(tail.length)p.tail=tail.at(-1).history_arrival;
    if(tail.length<historyPage)p.tail=ceiling;
    const sourcePage=async options=>{
      const rows=[...(await this.store.historyRows("inbox",options)).map(m=>({m,here:false})),...(await this.store.historyRows("outbox",options)).map(m=>({m,here:true}))];
      rows.sort((a,b)=>historyOrder(a.m.history_pos,b.m.history_pos)*(options.reverse?-1:1));return rows.slice(0,historyPage);
    };
    // The existing carrier repair also covers quiet groups and completed jobs.
    // Advance once per group per external wake, including unavailable groups.
    const groups=[...convs.values()].filter(c=>c.kind==="group"&&c.id>sweep.contextAfter).sort((a,b)=>a.id.localeCompare(b.id));
    if(groups.length) {
      const c=groups[0];sweep.contextAfter=c.id;
      try{await context(c);await clear("context/"+c.id);}catch(e){if(e instanceof StoreConflict)throw e;await pending({tuple:"context/"+c.id,conv:c.id},"proof_pending");}
    }
    if(p.stage==="recent") {
      const id=[...convs.keys()].sort().find(id=>id>p.recent);
      if(id) {
        const c=convs.get(id);
        try{await context(c);await clear("context/"+id);}catch(e){if(e instanceof StoreConflict)throw e;await pending({tuple:"context/"+id,conv:id},"proof_pending");}
        for(const {m,here} of await sourcePage({conv:id,reverse:true}))await queue(m,here);
        p.recent=id;
      } else p.stage="older";
    } else if(p.stage==="older") {
      const page=await sourcePage({after:p.older});
      for(const {m,here} of page)if(here?m.at<=p.started:m.history_arrival<=p.ceiling)await queue(m,here);
      if(page.length)p.older=page.at(-1).m.history_pos;
      if(page.length<historyPage)p.stage="tail";
    }
    let pendingMore=false;
    if(!sweep.done) {
      const refs=(await this.store.after("kv",sweep.after,historyPage)).filter(r=>r?.key?.startsWith(prefix));
      for(const ref of refs) {
        sweep.after=ref.key;
        if(ref.tuple.startsWith("context/")) {try{const c=convs.get(ref.conv);if(c){await context(c);await clear(ref.tuple);}}catch(e){if(e instanceof StoreConflict)throw e;}}
        else {
          let row=await this.groupRead(checks,ref.dir,ref.id);
          if(!row&&ref.dir==="inbox") {const lid=ref.tuple.split("/").slice(-2).join("/"),seen=await this.groupRead(checks,"lids",lid);if(seen)row=await this.groupRead(checks,"inbox",seen.id);}
          if(row)await queue(row,ref.dir==="outbox");
        }
      }
      pendingMore=refs.length===historyPage;if(!pendingMore)sweep.done=true;
    }
    const next={...j,own_human:this.fp,catchup:p};
    if(p.stage==="tail"&&p.legacy_running) {next.state="done";next.done=convs.size;next.total=convs.size;if(p.older)next.pos={conv:p.older[0],ms:p.older[1],id:p.older[2]};p.legacy_running=false;}
    // IndexedDB key order is random envelope ID order. Reuse the existing
    // durable outbox ordering field for proof/dependency/recent-page order.
    if(copies.length) {let order=Math.max(p.order||0,this.now());for(const c of copies)c.send_order=++order;p.order=order;}
    book[dev.address]=next;
    if(JSON.stringify(next)!==JSON.stringify(j))ops.push({s:"kv",k:"history",v:book});
    if(ops.length||copies.length) {
      await this.store.write([...copies.map(c=>({s:"outbox",k:c.id,v:c})),...ops],checks);
      this.changed();if(this.connected)this.queueOutbox();
    }
    return p.stage!=="tail"||tail.length===historyPage||pendingMore||groups.length>1;
  }

  // Reuse original signed group journals and existing encrypted carriers.
  // A durable usable batch is the marker; terminal failures and legacy
  // completed jobs without carriers recover without resetting history.
  async groupHistoryCarriers(c,dev,checks) {
    const {packet,g}=await this.groupTurnEvidence(c.id,checks),own=await this.groupTarget(packet.state,packet.withdrawals,checks);
    if(!own.devices.some(d=>d.address===dev.address&&d.fingerprint===dev.fingerprint))throw Error("Group history recipient is not a current own device.");
    const pin=await this.groupRead(checks,"pins",dev.address);
    if(!pin||pin.pending||pin.fingerprint!==dev.fingerprint)throw Error("Group history recipient key changed.");
    const records=g.records.map(wire.parseGroupCommit),payloads=[];
    for(const page of this.groupProofPages(records)){const last=page.at(-1);payloads.push({sub:wire.SubGroupProof,descriptor:{v:1,seq:last.seq,hash:last.hash},body:wire.groupJournalJSON({records:page,more:false})});}
    const events=await this.convEvents(c.id,checks),members=await this.dmMembers(c,events,checks,packet);
    payloads.push({sub:wire.SubGroupContext,descriptor:{v:1,seq:packet.state.seq,hash:await wire.groupStateHash(packet.state)},body:wire.groupContextJSON({...packet,memberships:await this.roomMemberships(c,events,members)})});
    const saved=await this.authorityRows({conv:c.id},checks),seen=new Set(),pub=await wire.publicEntry(this.keys,this.address);
    for(const row of saved) {
      if(row.to!==dev.address||row.recipient_fp!==dev.fingerprint||row.required_cap!==wire.CapGroup||row.pid||!["queued","waiting","custody","delivered"].includes(row.state))continue;
      try {
        const descriptor=wire.parseGroupCarrier(row.body),env=wire.parseEnvelope(row.envelope);
        if(descriptor.to_key!==dev.fingerprint||env.v!==wire.Version2||env.from!==this.address||env.to!==dev.address||env.kind!=="message"||env.blobs.length!==1)continue;
        await wire.verifyEnvelope(env,pub.sign_key);
        for(const [i,p] of payloads.entries())if(row.sub===p.sub&&descriptor.seq===p.descriptor.seq&&descriptor.hash===p.descriptor.hash)seen.add(i);
      }catch(e){ /* invalid local markers never suppress recovery */ }
    }
    if(seen.size===payloads.length)return [];
    const copies=[];
    for(const p of payloads)copies.push(await this.groupCarrierCopy(packet.root,p.sub,p.descriptor,p.body,dev));
    return copies;
  }

  // ---- files between your devices (client/historyfiles.go): a copy of
  // each file sent here is kept encrypted to this device; received files'
  // ciphertext is kept, fetched one at a time while the page is connected
  // (an open and the keeping share one fetch); a device of your person
  // asks for a history message's file and this one answers with it,
  // encrypted to that device, or says why not. Storage that fails (full)
  // never keeps a message from being admitted: the file is then not kept,
  // and the page says so.

  async keepSent(plain) {
    for (const f of plain) {
      try {
        const sha = wire.hex(await wire.sha256(f.bytes));
        if (await this.store.get("files", "kept/" + sha)) continue;
        if (!this.selfPub) this.selfPub = await wire.publicEntry(this.keys, this.address);
        const e = await wire.encryptFile(f.bytes, f.name, this.selfPub);
        await put(this.store, "files", "kept/" + sha, { attachment: e.attachment, ct: e.ct });
      } catch (e) { /* not kept: another device asking for it is told so */ }
    }
  }

  // cipherOf is received file att's ciphertext: kept here, or fetched (one
  // fetch at a time for each file) and kept when it fits.
  cipherOf(att, bounded = false) {
    const k = "ct/" + att.blob.id;
    if (this.fetching.has(k)) return this.fetching.get(k);
    const p = (async () => {
      const kept = await this.store.get("files", k);
      if (kept) return kept.ct;
      const ct = await this.getBytes("/v1/blobs/" + att.blob.id + "/data", bounded ? att.blob.size : undefined);
      if (ct.length !== att.blob.size || wire.hex(await wire.sha256(ct)) !== att.blob.sha256) throw new Error("the file is not the one the sender signed");
      if (att.size > wire.BrowserMaxFile) {
        this.notKept.set(att.blob.id, "large");
      } else {
        try {
          await put(this.store, "files", k, { ct });
          this.notKept.delete(att.blob.id);
        } catch (e) {
          this.notKept.set(att.blob.id, "full");
        }
      }
      return ct;
    })().finally(() => this.fetching.delete(k));
    this.fetching.set(k, p);
    return p;
  }

  // keepFiles keeps received conversation files not kept here yet: atts,
  // the files a message just brought, or without them every file the inbox
  // holds (after each connection). A catch-up with files read every message
  // held again for each file and told the page after each (MEL-546), so
  // arrivals are queued, not looked for.
  keepFiles(atts) {
    if (atts) this.keepQueue.push(...atts);
    else this.keepScan = true;
    if (!this.keeping) this.keeping = this.keepPass().finally(() => { this.keeping = null; });
    return this.keeping;
  }

  // keepPass keeps them newest first, one at a time while connected. The
  // server out of reach ends it, and the next call reads the inbox again
  // (the next connection goes on); storage full ends it too (the next
  // arrival tries once more).
  async keepPass() {
    for (;;) {
      if (!this.connected) return this.keepAgain();
      if (this.keepScan) {
        this.keepScan = false;
        const held = (await this.store.all("inbox")).filter((x) => x.conv).sort((a, b) => a.at - b.at);
        this.keepQueue = [...held.flatMap((m) => m.attachments || []), ...this.keepQueue]; // arrivals meanwhile stay newest
      }
      const a = this.keepQueue.pop();
      if (!a) return;
      if (!a.blob || this.notKept.has(a.blob.id)) continue;
      if (a.size > wire.BrowserMaxFile) { this.notKept.set(a.blob.id, "large"); continue; }
      if (await this.store.get("files", "ct/" + a.blob.id)) continue;
      try {
        await this.cipherOf(a);
      } catch (e) {
        if (retryable(e)) return this.keepAgain();
        this.notKept.set(a.blob.id, e.status === 404 ? "gone" : "failed");
      }
      this.changed(true); // with the arrivals: a file kept is told in their bursts
      if (this.notKept.get(a.blob.id) === "full") return;
    }
  }

  keepAgain() {
    this.keepQueue = [];
    this.keepScan = true;
  }

  // fileState is where a file of a message stands for the page: a history
  // file not here (requestable, requested, unavailable), or a received one
  // neither kept here nor on the server any more.
  fileState(a) {
    if (!a.blob) return { availability: a.availability || "requestable", note: a.detail || "", openable: false };
    const why = this.notKept.get(a.blob.id);
    if (why === "gone") return { availability: "unavailable", note: "Your server no longer holds it, and this browser did not keep it.", openable: false };
    if (why === "full") return { note: "Not kept in this browser: its storage is full.", openable: true };
    if (why === "large") return { note: "Not kept in this browser: it is larger than " + (wire.BrowserMaxFile >> 20) + " MiB.", openable: true };
    return { openable: true };
  }

  // sentState is where a file this device sent stands (the daemon's
  // FileView.openable): kept, so it opens here and your other devices can
  // ask for it; or not kept, so only the recipient has it (sent before
  // copies were kept, or storage failed).
  async sentState(a) {
    if (a.sha256 && (await this.store.get("files", "kept/" + a.sha256))) return { openable: true };
    return { openable: false, note: "Not kept on this device: only the recipient has it." };
  }

  // requestFile asks the device of yours that forwarded history message id
  // for its file index; it opens here once that device's answer comes.
  async requestFile(id, i) {
    const m = await this.store.get("inbox", id);
    if (!m || !m.history) throw new Error("That message is not a history message here.");
    if(await this.groupRecord(m.conv))return this.requestGroupFile(m,i);
    const a = (m.attachments || [])[i];
    if (!a) throw new Error("That message has no such file.");
    if (a.blob) return { note: "It is here already." };
    const dev = this.me && this.me.devices.find((d) => d.address === m.synced_from);
    if (!dev) throw new Error(m.synced_from + " is no longer one of your devices: the file cannot be asked from it.");
    const rec = await this.ownCopy(dev, await this.store.get("convs", m.conv), "file", wire.fileMsgJSON({ type: "request", lid: m.lid, sha256: a.sha256 }));
    m.attachments[i] = { ...a, availability: "requested", detail: "" };
    await this.store.write([{ s: "outbox", k: rec.id, v: rec }, { s: "inbox", k: m.id, v: m }]);
    this.changed();
    if (this.connected) await this.post(rec);
    return { note: "Asked " + dev.address.split("/")[1] + " for it: it opens here once that device sends it (it has to be online)." };
  }

  // admitFile takes a file message from another device of yours: a request
  // is kept for runServes; an offer completes a history message's file.
  async admitFile(n, env, ops) {
    let m;
    try {
      m = wire.parseFileMsg(n.body);
    } catch (e) {
      throw new Hold("invalid", "a malformed file message");
    }
    if (m.type === "request") {
      if (await this.store.get("kv", "serve/" + env.id)) return ops;
      return [...ops, { s: "kv", k: "serve/" + env.id, v: { serve: true, id: env.id, device: env.from, conv: n.conv, lid: m.lid, sha256: m.sha256, state: "pending", at: this.now() } }];
    }
    const rec = (await this.store.all("inbox")).find((x) => x.conv === n.conv && x.lid === m.lid && x.history && (x.attachments || []).some((a) => !a.blob && a.sha256 === m.sha256));
    if (!rec) return ops; // not waiting for it (any more)
    const i = rec.attachments.findIndex((a) => !a.blob && a.sha256 === m.sha256);
    if (!m.available) {
      rec.attachments[i] = { ...rec.attachments[i], availability: "unavailable", detail: m.detail };
    } else {
      if (n.attachments.length !== 1 || n.attachments[0].sha256 !== m.sha256) throw new Hold("invalid", "a file offer without that file");
      const a = rec.attachments[i];
      rec.attachments[i] = { blob: n.attachments[0].blob, name: a.name, size: a.size, sha256: a.sha256 };
    }
    return [...ops, { s: "inbox", k: rec.id, v: rec }];
  }

  runServes() {
    if (!this.serving) this.serving = this.servePass().finally(() => { this.serving = null; });
    return this.serving;
  }

  // servePass answers the file requests kept here, oldest first, while
  // connected; the server out of reach ends it (the next connection goes on).
  async servePass() {
    for (;;) {
      if (!this.connected) return;
      const s = (await this.store.all("kv")).filter((v) => v && v.serve && v.state === "pending").sort((a, b) => a.at - b.at)[0];
      if (!s) return;
      let state = "served", detail = "";
      try {
        await this.serveFile(s);
      } catch (e) {
        if (retryable(e)) return;
        [state, detail] = ["unavailable", e.message];
        await this.offerFile(s, [], detail).catch(() => {});
      }
      await put(this.store, "kv", "serve/" + s.id, { ...s, state, detail });
    }
  }

  // serveFile sends the file request s asks for, encrypted to the asking
  // device: from a received file's ciphertext, or the copy kept of one
  // sent here.
  async serveFile(s) {
    if(s.group)return this.serveGroupFile(s);
    const dev = this.me && this.me.devices.find((d) => d.address === s.device);
    if (!dev) throw new Error(s.device + " is no longer a device of your person");
    let src = null;
    for (const m of await this.store.all("inbox")) {
      const a = m.conv === s.conv && m.lid === s.lid && (m.attachments || []).find((x) => x.blob && x.sha256 === s.sha256);
      if (a) {
        src = { name: a.name, bytes: await wire.decryptFile(await this.cipherOf(a), a, this.keys) };
        break;
      }
    }
    if (!src) {
      const sent = (await this.store.all("outbox")).some((r) => r.conv === s.conv && r.lid === s.lid && !r.aside && (r.attachments || []).some((x) => x.sha256 === s.sha256));
      if (!sent) throw new Error("this device holds no such file");
      const k = await this.store.get("files", "kept/" + s.sha256);
      if (!k) throw new Error("this device kept no copy of that file");
      src = { name: k.attachment.name, bytes: await wire.decryptFile(k.ct, k.attachment, this.keys) };
    }
    const f = await wire.encryptFile(src.bytes, src.name, await wire.parsePublic(JSON.parse(dev.json)));
    await this.offerFile(s, [f], "");
  }

  // offerFile answers request s with files (the file, or none: detail says why).
  async offerFile(s, files, detail) {
    if(s.group)return this.offerGroupFile(s,files,detail);
    const dev = this.me && this.me.devices.find((d) => d.address === s.device);
    const c = await this.store.get("convs", s.conv);
    if (!dev || !c) return; // not your device (any more): nothing is sent to it
    const rec = await this.ownCopy(dev, c, "file", wire.fileMsgJSON({ type: "offer", lid: s.lid, sha256: s.sha256, available: files.length > 0, detail }), files);
    await put(this.store, "outbox", rec.id, rec);
    this.changed();
    await this.post(rec);
  }

  // ---- keys and persons of others

  async directory(address) {
    const [label, agent] = address.split("/");
    const d = await this.call("GET", "/v1/agents/" + label + "/" + agent);
    const pub = await wire.parsePublic(d.public);
    if (pub.address !== address) throw new Error("the server answered for another address");
    return { pub, fingerprint: await wire.fingerprint(pub), json: wire.marshalPublic(pub), revoked: !!d.revoked };
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
    const peers = [this.me, ...(await this.store.all("persons"))].filter(Boolean);
    if (p.email && peers.some(other => other.person !== p.person && other.email === p.email)) {
      p.state = "conflict";
      await put(this.store, "persons", p.person, p);
      this.changed();
      throw new Hold("invalid", "Another account claims this email; the earlier person stays pinned.");
    }
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
      await this.pinDevices(this.me);
      this.changed();
      this.syncRoots().catch(()=>{});this.syncReadMarks().catch(()=>{});this.syncInvitations().catch(()=>{});
      this.runHistory().catch(()=>{});
      return this.me;
    }
    await put(this.store, "persons", next.person, next);
    await this.pinDevices(next);
    this.changed();
    this.runHistory().catch(()=>{}); // newly verified host/member proof may unblock a deferred original
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
    if (!f.includes("env2") || !f.includes("caps") || !f.includes("person2")) return [false, "server_update: your server cannot carry conversations (it needs an update)"];
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
      return [false, "peer_update: " + address + " needs to update AgentNet to read this conversation (or has not connected since updating)"];
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
    await this.syncRoots();await this.syncReadMarks();
    this.changed();
    // Starting a DM is deciding on that person: with notifications on, they may alert.
    if ((await this.notifyState()).enabled) this.syncNotify().catch(() => {});
    return id;
  }

  // sendDM sends a message to the person. Never a question or task for
  // them, never v1.
  async sendDM({ id: sendID = "", conv, body, topic="",topic_event=null,quote="", reply_to: replyTo, files = [], reply_receiver = null, pid = "" }) {
    topic=await this.outgoingTopic(conv,topic,replyTo);
    if(topic&&!replyTo&&!topic_event)replyTo=await this.chatTopicHead(conv,topic);
    body = String(body || "").trim();
    if (wire.blank(body) && !files.length) throw new Error("Write a message or add a file first.");
    checkFiles(files);
    const c = (await this.store.get("convs", conv)) || await this.groupRecord(conv);
    if (!c) throw new Error("No conversation " + conv + " here.");
    if (c.kind === "group") {
      if (pid) {
        const h=await this.humanPlan(c,pid);
        return this.sendHumanTurn(c,{id:sendID,queued:true,kind:"message",body,topic,topic_event,quote,reply_to:replyTo||"",origin:"ui",files,receiver:reply_receiver,pid},h);
      }
      const events=await this.convEvents(c.id),members=await this.dmMembers(c,events);
      if([...new Set(events.map(x=>x.e.pid))].some(pid=>{const p=this.resolveAgent(pid,events,members);return p.external&&follows(p)&&p.state==="active"&&!p.held;}))return this.sendHumanTurn(c,{id:sendID,queued:true,kind:"message",body,topic,topic_event,quote,reply_to:replyTo||"",origin:"ui",files,receiver:reply_receiver},await this.roomPlan(c,events,members));
      return this.sendGroupTurn(c, {id:sendID,queued:true,body, topic,topic_event,quote,reply_to:replyTo || "",files,receiver:reply_receiver});
    }
    try { replyTo=await this.logicalReply(conv,replyTo,true,"Conversation"); } catch(e) { throw Error("A reply stays within its conversation: "+e.message); }
    const human = await this.humanPlan(c, pid);
    if (human) return this.sendHumanTurn(c, { id: sendID, queued: true, kind: "message", body, topic,topic_event,quote,reply_to: replyTo || "", origin: "ui", files, receiver: reply_receiver, pid }, human);
    return this.sendConv(c, { id: sendID, queued: true, kind: "message", body, topic,topic_event,quote,reply_to: replyTo || "", origin: "ui", files, receiver: reply_receiver });
  }

  // sendConv stores the sealed envelope before it is sent, and sends that
  // exact envelope on every retry: a message, a participation record (sub
  // "event") or a request to another device's agent (pid and target).
  async sendGroupCopy(address, pin, group) {
    if (!group) return "";
    try { return await wire.profileSupports(await this.profile(address), address, (await this.pubOf(pin)).sign_key, wire.CapSendGroup) ? group : ""; }
    catch (_) { return ""; } // display metadata cannot block an otherwise compatible request
  }

  async mergeSendGroupOps(ops, n, author) {
    if (n.sub === "history") n = {...wire.parseHistory(n.body),conv:n.conv};
    if (!n.conv || !n.lid || n.sub || !n.pid || !n.target || n.origin !== "ui" || !["question","task"].includes(n.kind)) return ops;
    const checks=ops.checks || [], key="send-group/"+n.conv+"/"+author+"/"+n.lid;
    // Admission has already verified the exact author or own linked history source.
    const old=await this.groupRead(checks,"kv",key), incoming=n.send_group || "";
    const value=old?.conflict ? old : old?.group && incoming && old.group!==incoming ? {group:"",conflict:true} : {group:old?.group || incoming,conflict:false};
    if (!value.group && !value.conflict) return ops;
    ops.push({s:"kv",k:key,v:value});
    const present=ops.find(o=>o.s==="inbox"&&o.v?.lid===n.lid&&o.v.conv===n.conv);
    let row=present?.v;
    if(!row){const seen=await this.groupRead(checks,"lids",author+"/"+n.lid);if(seen)row=await this.groupRead(checks,"inbox",seen.id);}
    if(row){const next={...row,send_group:value.group,send_group_conflict:value.conflict};if(present)present.v=next;else ops.push({s:"inbox",k:row.id,v:next});}
    ops.checks=checks;return ops;
  }

  async sendConv(c, { id: sendID = "", queued = false, kind, body, topic="",topic_event=null,quote="",reply_to: replyTo = "", origin = "", sub = "", pid = "", send_group = "", target = null, files = [], receiver = null }) {
    if (pid) {
      const ev = sub === "event" ? await this.eventRecord(body) : null;
      const events = [...await this.convEvents(c.id), ...(ev ? [ev] : [])];
      const members = await this.dmMembers(c, events), info = this.resolveAgent(pid, events, members);
      if (info.external || members.group) return this.sendExternal(c, { id: sendID, queued, kind, body, topic,topic_event,quote,reply_to: replyTo, origin, sub, pid, send_group, target, files, receiver }, info);
    }
    const required_cap = wire.agentRequirement({ kind, body, sub, target });
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
    const lid = this.localSendID(sendID), firstID = wire.newID(), at = this.now(), checks = [];
    const attention = origin === "ui" && sub === "" && ["message", "question", "task"].includes(kind);
    const plain = [];
    for (const f of files) plain.push({ name: wire.safeName(f.name), bytes: f.bytes instanceof Uint8Array ? f.bytes : new Uint8Array(await f.arrayBuffer()) });
    quote = await this.logicalReply(c.id, quote, true, "Conversation");
    const prepared = await this.prepareReceiverRequest(receiver, { id: target ? lid : firstID, lid, conv: c.id, root: c.root, ts: Math.floor(at/1000), kind, body, topic,topic_event,quote,reply_to: replyTo, origin, target, pid }, plain, checks);
    const recs = [];
    // A turn is for the other person (as client.SendConv): a changed key
    // refuses it until trusted, and so does no device of theirs taking a copy.
    const turn = sub === "";
    for (const dev of devices) {
      const pin = await this.store.get("pins", dev.address);
      if (turn && pin && (pin.pending || pin.fingerprint !== dev.fingerprint)) throw new Error(dev.address + "'s key changed: nothing is sent until the new key is trusted in AgentNet on a computer.");
      if (!pin || pin.fingerprint !== dev.fingerprint || pin.pending) continue; // a changed key is never used
      let ok = false, why = "", notify = false;
      try {
        [ok, why, notify] = await this.supports(dev.address, pin);
        if (ok && sub === wire.SubDriveSpace) [ok, why] = await this.ctlSupport(dev.address, pin, wire.CapDrive); // a Drive space record goes only to devices that read it; the others wait for it
        if (ok && required_cap) {
          try { await this.requireAgentIdentity(dev.address, pin); } catch (e) { ok = false; why = retryable(e) ? "server_unavailable: cannot reach your server" : e.code?.endsWith("_unsupported") ? "peer_update: " + e.message : e.message; }
        }
      } catch (e) {
        if (this.revoked) throw new Error("This device was removed from its server: nothing more is sent or received here.");
        why = "server_unavailable: cannot reach your server";
      }
      if (prepared) await this.receiverSupport(dev.address, pin);
      const recipient = await this.pubOf(pin);
      const sealed = [];
      for (const f of plain) sealed.push({ ...(await wire.encryptFile(f.bytes, f.name, recipient)), uploaded: false });
      const replica = dev.own && !(target && target.address === dev.address && target.fingerprint === dev.fingerprint);
      const chan = notify && !dev.own && attention ? await wire.notifyChannel(c.id, dev.fingerprint) : "";
      const id = prepared && target?.address === dev.address && target.fingerprint === dev.fingerprint ? lid : recs.length ? wire.newID() : firstID;
      const copyGroup=await this.sendGroupCopy(dev.address,pin,send_group);
      const envelope = await wire.seal({ v: wire.Version2, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), kind,
        body, topic,topic_event,quote,reply_to: replyTo, conv: c.id, lid, root: c.root, origin, sub, pid, target, chan, replica, fan, send_group:copyGroup, attachments: sealed.map((f) => f.attachment), receiver_route: prepared?.route },
      this.keys, recipient);
      recs.push({ id, conv: c.id, lid, body, topic,topic_event,quote,reply_to: replyTo, kind, origin, sub, pid, send_group, send_group_wire:!!copyGroup, target, ...(required_cap ? { required_cap } : {}), ...(prepared ? { receiver_route: prepared.route, recipient_fp: dev.fingerprint } : {}), at, to: dev.address, person:dev.own ? me.person : peer.person,own: dev.own, envelope,
        attachments: sealed.map((f) => f.attachment), files: sealed.length ? sealed : undefined, state: ok ? "queued" : "waiting", detail: why });
    }
    if (turn && !recs.some((r) => !r.own)) throw new Error("Not sent: no device of the other person can get a copy now.");
    if (!recs.length) throw new Error("No device of this conversation can be sent a copy now.");
    const final = (await this.gate(c)).why; // a profile just read may have frozen the person
    if (final) throw new Error(final);
    await this.commitReceiverCopies(recs, prepared, checks, [], queued);
    this.changed();
    await this.keepSent(plain);
    if (queued) this.queueOutbox();
    else if (prepared) await this.post(prepared.delegation);
    else for (const r of recs) if (r.state === "queued") await this.post(r);
    const least = copyOrder(recs);
    return { id: recs[0].id, lid, state: least.state, detail: least.detail, copies: recs.map((r) => ({ id: r.id, to: r.to, state: r.state, detail: r.detail })) };
  }

  async post(rec) {
    if (this.closing) return;
    this.posting ??= new Map();
    if (this.posting.has(rec.id)) return this.posting.get(rec.id);
    const run = (async () => {
      const current = await this.store.get("outbox", rec.id);
      if (!current || !["queued", "waiting"].includes(current.state)) return;
      return this.postStored({ ...current, state: rec.state });
    })();
    this.posting.set(rec.id, run);
    try { return await run; } finally { this.posting.delete(rec.id); }
  }

  async postStored(rec) {
    if (rec.state === "receiver_waiting") return;
    try {
      await this.discoveredHistoryDelivery(rec);
      await this.receiverDeliveryGate(rec);
      if(rec.send_group_wire){const pin=await this.pinned(rec.to);if(!await this.sendGroupCopy(rec.to,pin,"present"))throw Object.assign(Error("Recipient needs sg1 again to read this already sealed request."),{code:"send_group_unsupported"});}
      if (rec.required_cap === wire.CapControl) {
        const pin = await this.pinned(rec.to);
        if (pin.pending || pin.fingerprint !== rec.recipient_fp) throw Error("Control recipient key changed.");
        const [ok, why] = await this.ctlSupport(rec.to, pin);
        if (!ok) throw Object.assign(Error(why), {code:"control_unsupported"});
      }
      if(rec.sub===wire.SubInvitationSync)await this.invitationSyncGate(rec);
      if(rec.sub===wire.SubReadSync)await this.readSyncGate(rec);
      if(rec.sub===wire.SubRootSync)await this.rootSyncGate(rec);
      // An assistant's reaction, as history too, needs agr1 at the reader
      // besides the copy's own requirement (client.deliver): never to an
      // older reader as its host's mark.
      let carried = null;
      if (rec.sub === "history") try { carried = wire.parseHistory(rec.body); } catch (e) { carried = null; }
      if (rec.required_cap === wire.CapAgentReaction || carried && wire.historyAssistantReaction(carried)) {
        const pin = await this.store.get("pins", rec.to);
        if (!pin || pin.pending || rec.recipient_fp && pin.fingerprint !== rec.recipient_fp) throw new Error(rec.to + "'s key changed: this reaction is not sent.");
        await this.requireAgentReaction(rec.to, pin, carried, rec.required_cap);
      }

      if(rec.required_cap===wire.CapContinuation) {const pin=await this.pinned(rec.to);if(pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Continuation recipient key changed.");const [ok,why]=await this.ctlSupport(rec.to,pin,wire.CapContinuation);if(!ok)throw Error(why);}
      if(rec.required_cap===wire.CapGroupInvitationControl) { const pin=await this.pinned(rec.to);if(pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Group recipient key changed.");await this.groupSupport(rec.to,pin);await this.requireGroupInvitationControl(rec.to,pin); }
      if(rec.required_cap===wire.CapGroup) {
        const pin=await this.pinned(rec.to);if(pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Group recipient key changed.");await this.groupSupport(rec.to,pin);
        let item=rec;if(rec.sub==="history")item=wire.parseHistory(rec.body);
        if(item.group_history){const [ok,why]=await this.ctlSupport(rec.to,pin,wire.CapOwnSyncV2);if(!ok)throw Error(why);}
        if(item.pid && ![wire.SubGroupProof,wire.SubGroupContext].includes(rec.sub)) {
          const c=await this.groupRecord(rec.conv),events=await this.convEvents(rec.conv),info=this.resolveAgent(item.pid,events,await this.dmMembers(c,events));
          if(info.role==="human") {await this.requireHumanSupport(rec.to,pin);await this.requireGroupHumanSupport(rec.to,pin);} else await this.requireAgentIdentity(rec.to,pin,info.external?wire.CapExternalParticipation:wire.CapAgentIdentity);
        }
        if(rec.control || item.sub===wire.SubStatus || item.ref){const [ok,why]=await this.ctlSupport(rec.to,pin,item.sub===wire.SubStatus?wire.CapHeadless:wire.CapControl);if(!ok)throw Error(why);}
      }
      if (rec.required_cap === wire.CapConvClear) { // a deletion waits until that own device reads clr1
        const { why, pin } = await this.clearGate(rec);
        if (why) throw new Error(why);
        const [ok, detail] = await this.ctlSupport(rec.to, pin, wire.CapConvClear);
        if (!ok) throw Object.assign(new Error(detail), { code: "clear_unsupported" });
      }
      if (rec.required_cap === wire.CapHumanParticipation) {
        const c = await this.store.get("convs", rec.conv) || await this.groupRecord(rec.conv), gate = await this.gate(c, rec);
        if (gate.why) throw Object.assign(new Error(gate.why), { code: gate.code }); // before uploading any file bytes
        await this.requireHumanSupport(rec.to, gate.pin);
        if(c?.kind==="group")await this.requireGroupHumanSupport(rec.to,gate.pin);
      }
      if (await this.roomCopy(rec)) { // a room shape needs rm1 besides its own requirement (ROOM_V1 §2.5)
        const pin = await this.store.get("pins", rec.to);
        if (!pin || pin.pending || rec.recipient_fp && pin.fingerprint !== rec.recipient_fp) throw new Error(rec.to + "'s key changed: this room copy is not sent.");
        await this.requireRoomSupport(rec.to, pin);
      }
      if ([wire.CapAgentIdentity, wire.CapExternalParticipation].includes(rec.required_cap)) {
        const pin = await this.store.get("pins", rec.to);
        if (!pin || pin.pending || rec.v === 1 && pin.fingerprint !== rec.fp) throw new Error(rec.to + "'s key changed: named copy is not sent.");
        await this.requireAgentIdentity(rec.to, pin, rec.required_cap);
      }
      // The files first, each resumable; the message names them only once
      // the relay holds them.
      for (const f of rec.files || []) {
        if (f.uploaded) continue;
        await this.uploadBlob(rec.to, f.attachment.blob, f.ct);
        f.uploaded = true;
        await this.receiverDeliveryGate(rec); // a deletion during upload cannot restore its stale row
        if (!await this.receiverOutboxProgress(rec)) return;
      }
      // Uploads take time: a conflict seen meanwhile (another send's
      // profile read, a message received) stops the handover here, and the
      // message stays queued, saying why.
      if (rec.conv) {
        const { why } = await this.gate((await this.store.get("convs", rec.conv)) || await this.groupRecord(rec.conv), rec);
        if (why) {
          rec.detail = why;
          if (!await this.receiverOutboxProgress(rec)) return;
          this.changed();
          return;
        }
      }
      await this.receiverDeliveryGate(rec);
      if(rec.sub===wire.SubInvitationSync)await this.invitationSyncGate(rec);
      if(rec.sub===wire.SubReadSync)await this.readSyncGate(rec);
      if (!await this.startHandover(rec)) return;
      const r = await this.call("POST", "/v1/messages", rec.envelope);
      rec.state = (r && r.state) || "custody";
      rec.detail = "";
      // The relay has everything: the ciphertext kept here is not needed.
      if (rec.files) rec.files = rec.files.map((f) => ({ ...f, ct: null }));
    } catch (e) {
      if (e.code === "receiver_redacted") return; // keep the newer exact-scope retention transaction
      if (e.code === "receiver_unsupported" || e.code === "read_sync_unsupported" || e.code === "invitation_sync_unsupported" || e.code === "control_unsupported" || rec.conv && ["group_invitation_unsupported", "root_sync_unsupported", "agent_identity_unsupported", "human_unsupported", "group_unsupported", "clear_unsupported", "room_unsupported"].includes(e.code)) {
        rec.state = "waiting"; rec.detail = "peer_update: " + e.message;
      } else if (retryable(e)) {
        rec.detail = e.message;
      } else {
        rec.state = "failed";
        rec.detail = e.code === "recipient_revoked" ? "that device was removed from the server"
          : e.status === 413 ? "your server refuses this file (its size limit or its space is reached): " + e.message : e.message;
      }
    }
    if (!await this.receiverOutboxProgress(rec, rec.state === "custody" || rec.state === "delivered")) return;
    this.changed();

  }

  async setOutState(id, state) {
    const rec = await this.store.get("outbox", id);
    if (!rec || rec.state === state) return;
    await put(this.store, "outbox", id, { ...rec, state });
    this.changed();
  }

  // Persist uncertainty before handing a request to the network. A queued
  // row alone does not prove that a previous POST never reached the relay.
  async startHandover(rec) {
    for (;;) {
      const current = await this.store.get("outbox", rec.id);
      if (!current || current.delivery_cancelled || !["queued", "waiting"].includes(current.state)) return false;
      if (!["question", "task"].includes(rec.kind) || rec.control) return true;
      try {
        await this.store.write([{s:"outbox",k:rec.id,v:{...current,handover_started:true}}],[{s:"outbox",k:rec.id,v:current}]);
        rec.handover_started = true;
        return true;
      } catch (e) { if (!(e instanceof StoreConflict)) throw e; }
    }
  }

  // gate says why nothing may be sent in conversation c now ("" when it
  // may), with the other member's person: yours and theirs must be kept
  // and not in conflict, each bound to a step of its pinned chain; for a
  // copy (rec), its device must still be a current device of either, with
  // its key pinned and unchanged. A new message and every kept copy pass
  // it before they are sent.
  // clearGate: a deletion copy goes only to a current device of this person,
  // under the key it was sealed for.
  async clearGate(rec) {
    const dev = this.me?.devices.find((d) => d.address === rec.to && d.fingerprint === rec.recipient_fp);
    if (!dev || rec.to === this.address) return { why: rec.to + " is no longer a device of yours: this deletion is not sent there." };
    const pin = await this.store.get("pins", rec.to);
    if (!pin || pin.pending || pin.fingerprint !== dev.fingerprint) return { why: rec.to + "'s key changed: this deletion is not sent there." };
    return { why: "", pin };
  }

  async groupHumanReaderGate(c, rec) {
    if (!rec || c?.kind !== "group") return;
    const item = rec.sub === "history" ? wire.parseHistory(rec.body) : rec;
    let human = !!item.human;
    const pid = item.pid || rec.pid;
    if (pid && !human) {
      const events = await this.convEvents(c.id);
      human = this.resolveAgent(pid, events, await this.dmMembers(c, events)).role === "human";
    }
    if (!human) return;
    const pin = await this.pinned(rec.to);
    if (pin.pending || pin.fingerprint !== rec.recipient_fp) throw Error("Group human recipient key changed.");
    await this.groupSupport(rec.to, pin);
    await this.requireGroupHumanSupport(rec.to, pin);
  }

  async gate(c, rec) {
    try { await this.groupHumanReaderGate(c, rec); }
    catch (e) { return { why: e.message, code: e.code }; }
    if(rec?.sub===wire.SubInvitationSync)return this.invitationSyncGate(rec);
    if(rec?.sub===wire.SubReadSync)return this.readSyncGate(rec);
    if(rec?.sub===wire.SubRootSync)return this.rootSyncGate(rec);
    if (rec?.sub === wire.SubClear) return this.clearGate(rec);
    if (rec?.group_lifecycle) return this.groupLifecycleGate(rec);
    if (c?.kind === "group" || rec?.required_cap === wire.CapGroup) return this.groupSendGate(c, rec);
    if (rec?.human) return this.humanGate(c, rec);
    if ([wire.CapExternalParticipation, wire.CapHumanParticipation].includes(rec?.required_cap) && rec.sub !== "history") return this.externalGate(c, rec);
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
  localSendID(id) {
    if (id && !wire.validID(id)) throw Error("Invalid local send id.");
    return id || wire.newID();
  }

  queueOutbox() { if (!this.closing) this.flushOutbox().catch(() => {}); }

  async flushOutbox() {
    if (this.outboxPass) { this.outboxAgain = true; return this.outboxPass; }
    const run = (async () => {
      do { this.outboxAgain = false; await this.flushOutboxOnce(); } while (this.outboxAgain && this.connected);
    })();
    this.outboxPass = run;
    try { return await run; } finally { this.outboxPass = null; if (this.outboxAgain && this.connected && !this.closing) this.queueOutbox(); }
  }

  async flushOutboxOnce() {
    const blocked = new Set();
    const rows = (await this.store.all("outbox")).sort((a,b) => (a.send_order ?? a.at) - (b.send_order ?? b.at));
    for (const rec of rows) {
      if (!this.connected || this.closing) return;
      // FIFO is for readable turns. Auxiliary copies must pass their own
      // gates without blocking turns or unrelated history/deletion copies.
      const key = (rec.aside || rec.control || rec.sub === "history" ? "aux\0" + rec.id : "turn\0" + (rec.conv || "")) + "\0" + rec.to;
      if (blocked.has(key)) continue;
      if (rec.state === "receiver_waiting") { blocked.add(key); continue; }
      if (rec.state !== "queued" && rec.state !== "waiting") continue;
      if (rec.v === 1 || !rec.conv) {
        const pin = await this.store.get("pins", rec.to);
        const expected = rec.recipient_fp || rec.fp;
        const why = !pin || pin.pending || pin.fingerprint !== expected ? rec.to + "'s key changed: it is not sent until the new key is trusted." : "";
        if (why) {
          if (rec.detail !== why) {
            await this.receiverOutboxProgress({ ...rec, detail: why });
            this.changed();
          }
          blocked.add(key); continue;
        }
        await this.post({ ...rec, state: "queued", detail: "" });
        if ((await this.store.get("outbox", rec.id))?.state === "queued") blocked.add(key);
        continue;
      }
      const c = (await this.store.get("convs", rec.conv)) || await this.groupRecord(rec.conv);
      let g;
      try { g = await this.gate(c, rec); } catch (e) { blocked.add(key); continue; }
      if (!g.why && rec.state === "waiting") {
        let ok = false;
        try { [ok] = rec.required_cap === wire.CapConvClear ? await this.ctlSupport(rec.to, g.pin, wire.CapConvClear) : await this.supports(rec.to, g.pin); } catch (e) { blocked.add(key); continue; }
        if (!ok) { blocked.add(key); continue; }
        g = await this.gate(c, rec);
      }
      if (g.why) {
        if (rec.detail !== g.why) {
          await this.receiverOutboxProgress({ ...rec, detail: g.why });
          this.changed();
        }
        blocked.add(key); continue;
      }
      await this.post({ ...rec, state: "queued", detail: "" });
      if (["queued","waiting"].includes((await this.store.get("outbox", rec.id))?.state)) blocked.add(key);
    }
  }

  // Receiver setup uses existing encrypted outbox rows. Originals remain
  // non-deliverable until the exact selected host accepts their commitment.
  async receiverHost(host, checks = []) {
    if (!host || Object.keys(host).some(k => !["address", "fingerprint"].includes(k)) || !wire.validAddress(host.address) || !wire.validFingerprint(host.fingerprint)) throw Error("Reply receiver requires an exact linked host key.");
    const me = await this.groupRead(checks, "kv", "person"), identity = await this.groupRead(checks, "kv", "identity");
    if (!me || me.state !== "self" || !identity || identity.revoked || identity.address !== this.address || identity.fingerprint !== this.fp || !me.devices.some(d => d.address === this.address && d.fingerprint === this.fp) || !me.devices.some(d => d.address === host.address && d.fingerprint === host.fingerprint)) throw Error("Selected reply receiver is not a current device of this person.");
    const pin = await this.groupRead(checks, "pins", host.address);
    if (!pin || pin.pending || pin.fingerprint !== host.fingerprint) throw Error("Selected reply receiver host key changed.");
    return { me, pin };
  }

  async receiverSupport(address, pin) {
    const features = await this.features();
    if (!features.includes("caps") || !await wire.profileSupports(await this.profile(address), address, (await this.pubOf(pin)).sign_key, wire.CapReplyReceiver)) {
      const error = Error(address + " cannot receive selected cross-device replies yet.");
      error.code = "receiver_unsupported";
      throw error;
    }
  }

  async receiverCatalog(host) {
    if (!host) return { host: this.address, local: true, sessions: [] };
    const checks = [], { pin } = await this.receiverHost(host, checks);
    const result = { host: host.address, host_key: host.fingerprint, local: false, sessions: [], status: "pending" };
    const rows = await this.authorityRows({}, checks), requests = rows.filter(r => r.to === host.address && r.fp === host.fingerprint && r.receiver_route?.op === "catalog" && !r.reply_to);
    if (requests.length) {
      const request = requests.sort((a,b) => b.at-a.at)[0];
      const response = rows.filter(r => !r.to && !r.history && r.from === host.address && r.fp === host.fingerprint && r.receiver_route?.op === "catalog" && r.reply_to === request.id).sort((a,b) => b.at-a.at)[0];
      if (response) return { ...result, sessions: (await wire.parseReceiverOperation(response.body, response.receiver_route, response.reply_to)).sessions || [], status: "ready", at: Math.floor(response.at/1000) };
      if (request.state === "failed") return { ...result, status: "unavailable", detail: request.detail };
      return result;
    }
    try { await this.receiverSupport(host.address, pin); } catch (e) { return { ...result, status: "unavailable", detail: e.message }; }
    const id = wire.newID(), at = this.now(), route = wire.parseReceiverRoute({ op: "catalog", host: host.address, host_key: host.fingerprint }), body = wire.receiverOperationJSON({ v: 1 });
    const envelope = await wire.seal({ v: 1, id, from: this.address, to: host.address, ts: Math.floor(at/1000), kind: "message", body, receiver_route: route }, this.keys, await this.pubOf(pin));
    const rec = { v: 1, id, to: host.address, fp: host.fingerprint, kind: "message", body, at, envelope, receiver_route: route, aside: true, required_receiver_cap: true, state: "queued", detail: "" };
    try { await this.store.write([{s:"outbox",k:id,v:rec}],checks); } catch (e) { if (e instanceof StoreConflict) return this.receiverCatalog(host); throw e; }
    await this.post(rec);this.changed();
    return rec.state === "failed" ? { ...result, status: "unavailable", detail: rec.detail } : result;
  }

  // Receiver progress is conditional on the current private row. An upload
  // completion must not overwrite a concurrent exact-scope redaction.
  async receiverOutboxProgress(rec, custody = false) {
    const missingHistoryProof = rec.sub === "history" && rec.body === undefined && !rec.required_receiver_cap && !rec.receiver_route;
    const current = await this.store.get("outbox", rec.id);
    if (!current) return false;
    if (current.delivery_cancelled && !custody) return false;
    if (current.receiver_redacted && (!custody || current.envelope !== rec.envelope)) return false;
    if (missingHistoryProof && (current.sub !== "history" || current.body !== undefined || current.required_receiver_cap || current.receiver_route)) return false;
    // Old unmarked history lacks the plaintext used to determine its receiver
    // requirement. Keep its ciphertext and refuse only this row, without
    // inventing a capability or stopping unrelated queued deliveries.
    const protectedState = ["delivered","expired"].includes(current.state) || current.state === "quarantined" && rec.state !== "delivered";
    let next = missingHistoryProof ? { ...current, state: "failed", detail: "History copy lacks its durable capability proof; encrypted copy kept, not sent." }
      : current.receiver_redacted || current.delivery_cancelled ? { ...current, state: rec.state, detail: custody ? "" : current.detail } : {...rec,...(current.handover_started ? {handover_started:true} : {})};
    if (protectedState) next = {...next,state:current.state,detail:current.detail};
    try { await this.store.write([{ s: "outbox", k: rec.id, v: next }], [{ s: "outbox", k: rec.id, v: current }]); }
    catch (e) {
      if (e instanceof StoreConflict) return custody || !rec.required_receiver_cap && !wire.receiverRequirement(rec) ? this.receiverOutboxProgress(rec, custody) : false; // record proven custody against the new redacted row
      throw e;
    }
    if (missingHistoryProof) { this.changed(); return false; }
    return true;
  }

  async receiverDeliveryGate(rec) {
    if (!rec.required_receiver_cap && !wire.receiverRequirement(rec)) return;
    if (rec.receiver_redacted || (await this.store.get("outbox", rec.id))?.receiver_redacted) {
      const error = Error("Original ordinary request was deleted before selected handover.");
      error.code = "receiver_redacted"; throw error;
    }
    const pin = await this.store.get("pins", rec.to), expected = rec.fp || rec.recipient_fp;
    if (!pin || pin.pending || expected && pin.fingerprint !== expected) throw Error("Receiver copy recipient key changed.");
    await this.receiverSupport(rec.to, pin);
    if (rec.receiver_route?.op === "catalog") {
      if (rec.receiver_route.host !== rec.to && (rec.receiver_route.host !== this.address || rec.receiver_route.host_key !== this.fp)) throw Error("Receiver catalog host changed.");
      await this.receiverHost({ address: rec.to, fingerprint: expected || pin.fingerprint });
    }
    if (rec.receiver_setup || rec.receiver_route?.op === "request") {
      const { pin: hostPin } = await this.receiverHost({ address: rec.receiver_route.host, fingerprint: rec.receiver_route.host_key });
      await this.receiverSupport(rec.receiver_route.host, hostPin);
    }
    if (rec.receiver_return) {
      const source = await this.store.get("inbox", rec.receiver_return.source_id);
      if (!source || source.history || source.replica || source.v !== 1 || source.receiver_route?.host !== rec.receiver_return.host || source.receiver_route?.host_key !== rec.receiver_return.host_key || rec.reply_to !== source.receiver_route.request_ref) throw Error("Reply no longer has its exact original receiver route.");
      await this.receiverOriginAuthority(source, []);
    }
  }

  async receiverReplyCopies(rec, source, plain, checks) {
    if (!source?.receiver_route) return [rec];
    if (source.history || source.replica || source.v !== 1 || source.from !== rec.to || source.id !== source.receiver_route.request_ref) throw Error("Reply requires its directly verified original receiver route.");
    if (source.target && (source.target.address !== this.address || source.target.fingerprint !== this.fp)) throw Error("Human answer must come from the exact originally addressed host key.");
    await this.receiverOriginAuthority(source, checks);
    await this.groupRead(checks, "inbox", source.id);
    const route = source.receiver_route, pin = await this.groupRead(checks, "pins", route.host);
    if (!pin || pin.pending || pin.fingerprint !== route.host_key) throw Error("Selected return receiver key changed.");
    for (const address of new Set([rec.to,route.host])) await this.receiverSupport(address, await this.store.get("pins",address));
    rec.required_receiver_cap = true;
    rec.receiver_return = { source_id: source.id, host: route.host, host_key: route.host_key };
    if (route.host === rec.to) return [rec];
    const files = [], recipient = await this.pubOf(pin);
    for (const f of plain) files.push({ ...await wire.encryptFile(f.bytes,f.name,recipient),uploaded:false });
    const id = wire.newID(), envelope = await wire.seal({v:1,id,from:this.address,to:route.host,ts:Math.floor(rec.at/1000),kind:rec.kind,body:rec.body,reply_to:route.request_ref,status:rec.status,attachments:files.map(f=>f.attachment)},this.keys,recipient);
    return [rec,{...rec,id,to:route.host,fp:route.host_key,envelope,attachments:files.length?files.map(f=>f.attachment):undefined,files:files.length?files:undefined,aside:true}];
  }

  async prepareReceiverRequest(receiver, fields, plain, checks = []) {
    if (!receiver) return null;
    if (typeof receiver !== "object" || Array.isArray(receiver) || Object.keys(receiver).some(k => !["kind", "host", "agent_id", "session_handle", "instructions", "mode", "on_close"].includes(k))) throw Error("Invalid reply receiver selection.");
    const choice = wire.parseReceiverChoice(Object.fromEntries(Object.entries(receiver).filter(([k]) => k !== "host")));
    if (!receiver.host) {
      if (choice.kind === "human") return null;
      throw Error("This browser cannot run a local assistant or native session.");
    }
    if (receiver.host.address === this.address) throw Error("This browser has no native reply receiver.");
    const host = { ...receiver.host }, { pin } = await this.receiverHost(host, checks);
    await this.receiverSupport(host.address, pin);
    const manifests = [];
    for (const f of plain) manifests.push({ blob: { id: "", size: 0, sha256: "" }, name: f.name, size: f.bytes.length, sha256: wire.hex(await wire.sha256(f.bytes)) });
    // A quote is display/context on the original envelope, not part of the
    // selected receiver's existing request commitment (same as Go).
    const { quote, topic, topic_event, topic_done, ...requestFields } = fields;
    const request = await wire.parseReceiverRequest({ ...requestFields, from: this.address, from_key: this.fp, attachments: manifests });
    const route = { op: "delegate", host: host.address, host_key: host.fingerprint, request_ref: request.conv ? request.lid : request.id, delegation_id: wire.newID() };
    route.request_digest = await wire.receiverDigest(route, request, choice);
    const body = wire.receiverOperationJSON({ v: 1, request, receiver: choice }), recipient = await this.pubOf(pin), files = [];
    if (new TextEncoder().encode(body).length > wire.MaxReceiverSetup) throw Error("Selected receiver setup is too large.");
    for (const f of plain) files.push({ ...await wire.encryptFile(f.bytes, f.name, recipient), uploaded: false });
    const envelope = await wire.seal({ v: 1, id: route.delegation_id, from: this.address, to: host.address, ts: request.ts, kind: "task", body, attachments: files.map(f => f.attachment), receiver_route: route }, this.keys, recipient);
    const delegation = { v: 1, id: route.delegation_id, from: this.address, to: host.address, fp: host.fingerprint, kind: "task", body, at: this.now(), envelope, aside: true, receiver_route: route, receiver_setup: { request, choice, originals: [] }, attachments: files.map(f => f.attachment), files: files.length ? files : undefined, state: "queued", required_receiver_cap: true, detail: "" };
    return { route: wire.parseReceiverRoute({ ...route, op: "request" }), delegation, checks };
  }

  async commitReceiverCopies(copies, prepared, checks = [], extraOps = [], queued = false) {
    if (prepared) {
      await this.receiverHost({ address: prepared.route.host, fingerprint: prepared.route.host_key }, checks);
      for (const rec of copies) {
        if (wire.receiverRouteJSON(rec.receiver_route) !== wire.receiverRouteJSON(prepared.route)) throw Error("Prepared original differs from selected receiver commitment.");
        prepared.delegation.receiver_setup.originals.push({ id: rec.id, envelope_hash: wire.hex(await wire.sha256(new TextEncoder().encode(rec.envelope))) });
        rec.receiver_resume_state = rec.state;
        rec.state = "receiver_waiting";
        rec.detail = "Waiting for the selected reply receiver to accept this exact request.";
        rec.required_receiver_cap = true;
      }
    }
    const rows = [...copies, ...(prepared ? [prepared.delegation] : [])];
    // All local batch commits share this short serialization point. No network waits here.
    const previous = this.sendCommit || Promise.resolve();
    const run = previous.catch(() => {}).then(async () => {
      const kept = await this.store.all("outbox");
      let order = kept.reduce((last,r) => Math.max(last,r.send_order ?? r.at ?? 0), this.now());
      if (queued && rows.some(r => r.conv && r.lid && kept.some(old => old.conv === r.conv && old.lid === r.lid))) throw Error("This send is already kept here.");
      for (const r of rows) {
        if (await this.store.get("outbox", r.id)) throw Error("Local send id already exists.");
        r.send_order = ++order;
        if (["question","task"].includes(r.kind)) r.handover_started = false;
      }
      await this.store.write(rows.map(v => ({ s: "outbox", k: v.id, v })).concat(extraOps), checks);
    });
    this.sendCommit = run;
    await run;
  }

  async receiverOriginAuthority(rec, checks) {
    const route = wire.parseReceiverRoute(rec.receiver_route);
    if (route.op !== "request" || route.request_ref !== (rec.conv ? rec.lid : rec.id)) throw new Hold("invalid", "Receiver route differs from its original request.");
    const pin = await this.groupRead(checks, "pins", rec.from);
    if (!pin || pin.pending || pin.fingerprint !== rec.fp) throw new Hold("key_changed", "Receiver original signer key changed.");
    let person = await this.personOf(rec.from, pin);
    const selected = d => d.address === route.host && d.fingerprint === route.host_key;
    // Follow only the already pinned signed prefix when a newly linked host
    // has not been proved here. A known removed host stays refused.
    if (!person.devices.some(selected) && !(person.known || []).some(selected)) person = await this.refreshPerson(person);
    const current = await this.groupRead(checks, person.person === this.me?.person ? "kv" : "persons", person.person === this.me?.person ? "person" : person.person);
    if (!current || current.state === "conflict" || !current.devices.some(d => d.address === rec.from && d.fingerprint === rec.fp)) throw new Hold("invalid", "Receiver original signer is not a current verified device.");
    if (!current.devices.some(selected)) throw new Hold((current.known || []).some(selected) ? "invalid" : "proof_pending", "Selected receiver is not a current device of the verified request signer.");
  }

  async admitReceiverSetup(n, env, pin) {
    const checks = [], route = n.receiver_route, own = await this.groupRead(checks, "kv", "person");
    if (!own || own.state !== "self" || !own.devices.some(d => d.address === env.from && d.fingerprint === pin.fingerprint)) throw new Hold("invalid", "Receiver setup is not from a current own linked device.");
    await this.groupRead(checks, "pins", env.from);
    if (route.op === "delegate") throw new Hold("invalid", "This browser has no native receiver to accept delegated work.");
    const record = { id: env.id, from: env.from, fp: pin.fingerprint, v: 1, kind: n.kind, body: n.body, reply_to: n.reply_to, ts: n.ts, at: this.now(), aside: true, read: true, receiver_route: route, state: "" };
    const ops = [{ s: "inbox", k: env.id, v: record }];
    if (route.op === "ready" || n.reply_to) {
      const original = await this.groupRead(checks, "outbox", n.reply_to);
      if (!original || !original.aside || !original.receiver_route || original.to !== env.from || original.fp !== pin.fingerprint || original.receiver_route.host !== route.host || original.receiver_route.host_key !== route.host_key) throw new Hold("invalid", "Receiver response has no exact local setup.");
      if (route.op === "ready") {
        const selected = original.receiver_setup;
        if (original.receiver_redacted || selected?.redacted) throw new Hold("invalid", "Deleted ordinary request cannot be released by Ready.");
        if (original.receiver_route.op !== "delegate" || !selected || route.host !== env.from || route.host_key !== pin.fingerprint || route.delegation_id !== original.id || route.request_ref !== original.receiver_route.request_ref || route.request_digest !== original.receiver_route.request_digest || await wire.receiverDigest(route, selected.request, selected.choice) !== route.request_digest) throw new Hold("invalid", "Ready differs from the committed receiver delegation.");
        const operation = await wire.parseReceiverOperation(n.body, route, n.reply_to, []);
        await this.receiverHost({ address: route.host, fingerprint: route.host_key }, checks);
        for (const descriptor of selected.originals) {
          const rec = await this.groupRead(checks, "outbox", descriptor.id);
          if (!rec || !rec.receiver_route || wire.receiverRouteJSON(rec.receiver_route) !== wire.receiverRouteJSON({ ...route, op: "request" }) || wire.hex(await wire.sha256(new TextEncoder().encode(rec.envelope))) !== descriptor.envelope_hash) throw new Hold("invalid", "Ready has no byte-identical prepared original.");
          if (rec.state !== "receiver_waiting") continue;
          if (rec.kind !== selected.request.kind || rec.body !== selected.request.body || (rec.conv || "") !== selected.request.conv || (rec.conv ? rec.lid : rec.id) !== route.request_ref || (rec.reply_to || "") !== selected.request.reply_to || JSON.stringify(rec.target || null) !== JSON.stringify(selected.request.target || null) || (rec.pid || "") !== selected.request.pid) throw new Hold("invalid", "Prepared original projection changed before ready.");
          ops.push({ s: "outbox", k: rec.id, v: { ...rec, state: operation.detail ? "receiver_waiting" : rec.receiver_resume_state || "queued", detail: operation.detail } });
        }
        if (!operation.detail) ops.push({ s: "outbox", k: original.id, v: { ...original, receiver_setup: { ...selected, ready: true } } });
      } else if (route.op !== "catalog" || original.receiver_route.op !== "catalog") throw new Hold("invalid", "Receiver catalog differs from its local request.");
    } else {
      if (route.op !== "catalog" || route.host !== this.address || route.host_key !== this.fp) throw new Hold("invalid", "Receiver catalog names another host.");
      const id = wire.newID(), body = wire.receiverOperationJSON({ v: 1 }), recipient = await this.pubOf(pin);
      const envelope = await wire.seal({ v: 1, id, from: this.address, to: env.from, ts: Math.floor(this.now()/1000), kind: "message", body, reply_to: env.id, receiver_route: route }, this.keys, recipient);
      ops.push({ s: "outbox", k: id, v: { v: 1, id, to: env.from, fp: pin.fingerprint, kind: "message", body, at: this.now(), envelope, aside: true, receiver_route: route, required_receiver_cap: true, state: "queued", detail: "" } });
    }
    ops.checks = checks;
    return ops;
  }

  // ---- device conversations (version 1): messages, questions and tasks
  // to one device, as agentnet send sends them, and replies by hand. The
  // recipient's own approvals and acceptance decide what runs there;
  // nothing runs here.

  // sendDirect sends a message, question or task to the device at to (a
  // reply to reply_to, a device message with it), with files.
  async sendDirect({ id: sendID = "", to, kind, body, quote="",reply_to: replyTo = "", files = [], agent_id = "", reply_receiver = null }) {
    body = String(body || "").trim();
    kind = kind || "message";
    to = String(to || "").trim();
    if (wire.blank(body) && !files.length) throw new Error("Write a message or add a file first.");
    if (!["message", "question", "task"].includes(kind)) throw new Error("Choose message, question or task.");
    if (!wire.validAddress(to)) throw new Error("That is not an AgentNet address.");
    if (to === this.address) throw new Error("That is this browser.");
    if (agent_id && !["question", "task"].includes(kind)) throw new Error("A named agent can receive a question or task.");
    const target = agent_id ? await this.namedTarget(to, agent_id) : null;
    checkFiles(files);
    if (replyTo) {
      const m = (await this.store.get("inbox", replyTo)) || (await this.store.get("outbox", replyTo));
      if (!m || m.v !== 1 || (m.from || m.to) !== to) throw new Error("A reply stays in its conversation with that device.");
    }
    return this.sendV1({ id: sendID, queued: true, to, kind, body, quote,replyTo, files, status: "", target, receiver: reply_receiver });
  }

  // sendV1 seals one version 1 message to the key pinned for to (pinned
  // the first time), keeps it with its files before sending, and sends
  // that exact envelope, retried until the server takes it.
  async sendV1({ id: sendID = "", queued = false, to, kind, body, quote="",replyTo, files, status, target = null, receiver = null }) {
    const pin = await this.sendKey(to);
    const required_cap = wire.agentRequirement({ target });
    if (target && (target.address !== to || target.fingerprint !== pin.fingerprint)) throw new Error("Selected agent's host key changed: nothing is sent.");
    if (required_cap) {
      try { await this.requireAgentIdentity(to, pin); } catch (e) { if (!retryable(e)) throw e; }
    }
    const recipient = await this.pubOf(pin);
    const sealed = [], plain = [];
    for (const f of files) {
      const bytes = f.bytes instanceof Uint8Array ? f.bytes : new Uint8Array(await f.arrayBuffer());
      plain.push({ bytes, name: wire.safeName(f.name) });
      sealed.push({ ...(await wire.encryptFile(bytes, wire.safeName(f.name), recipient)), uploaded: false });
    }
    const id = this.localSendID(sendID), at = this.now();
    const attachments = sealed.map((f) => f.attachment), checks = [];
    const prepared = await this.prepareReceiverRequest(receiver, { id, to, to_key: pin.fingerprint, ts: Math.floor(at / 1000), kind, body, reply_to: replyTo, target }, plain, checks);
    if (prepared) await this.receiverSupport(to, pin);
    if (quote) {
      const parent=(await this.store.get("inbox",quote)) || (await this.store.get("outbox",quote));
      if(!parent || parent.conv || (parent.to ? parent.to!==to : parent.from!==to)) throw Error("Quote stays within its device conversation.");
    }
    const envelope = await wire.seal({ id, from: this.address, to, ts: Math.floor(at / 1000), kind, body, quote,reply_to: replyTo, status, target, attachments, receiver_route: prepared?.route }, this.keys, recipient);
    const rec = { v: 1, id, to, fp: pin.fingerprint, kind, body, quote,reply_to: replyTo, status, at, envelope,
      ...(target ? { target } : {}), ...(required_cap ? { required_cap } : {}), ...(prepared ? { receiver_route: prepared.route } : {}),
      attachments: attachments.length ? attachments : undefined, files: sealed.length ? sealed : undefined, state: "queued", detail: "" };
    const source = replyTo ? await this.store.get("inbox", replyTo) : null;
    if (prepared && source?.receiver_route) throw Error("A reply retains its original selected return receiver.");
    const copies = await this.receiverReplyCopies(rec, source, plain, checks);
    await this.commitReceiverCopies(copies, prepared, checks, [], queued);
    this.changed();
    await this.keepSent(plain); // as a DM's files: reopened here, offered to your other devices
    if (queued) this.queueOutbox();
    else if (prepared) await this.post(prepared.delegation);
    else for (const copy of copies) await this.post(copy);
    const now = await this.store.get("outbox", id);
    return { id, state: now.state, detail: now.detail };
  }

  // sendKey is the key to seal to for address (client.sendKey): the
  // server's entry, pinned the first time; one that differs from the pin
  // is a changed key, kept pending and never used; the pin when the server
  // is out of reach (the message then waits here).
  async sendKey(address) {
    const pin = await this.store.get("pins", address);
    const changed = () => new Error(address + "'s key changed: nothing is sent until the new key is trusted in AgentNet on a computer.");
    let d;
    try {
      d = await this.directory(address);
    } catch (e) {
      if (pin && !pin.pending && retryable(e)) return pin;
      if (e.status === 404) throw new Error(address + " is not on your server.");
      throw new Error("Could not find " + address + "'s key on your server: " + e.message);
    }
    if (d.revoked) throw new Error(address + " was removed from your server.");
    if (!pin) {
      const p = { address, json: d.json, fingerprint: d.fingerprint, pending: null };
      await put(this.store, "pins", address, p);
      return p;
    }
    if (pin.pending) throw changed();
    if (d.fingerprint === pin.fingerprint) return pin;
    await put(this.store, "pins", address, { ...pin, pending: { json: d.json, fingerprint: d.fingerprint } });
    this.changed();
    throw changed();
  }

  // replyV1 answers received device message id by hand: an answer to a
  // question, a result (done) for a task, else a message.
  async replyV1(id, body, sendID = "") {
    const m = await this.store.get("inbox", id);
    if (!m || m.v !== 1) throw new Error("No message with that id.");
    body = String(body || "").trim();
    if (!body) throw new Error("Write your answer first.");
    const r = await this.sendV1({ id: sendID, queued: true, to: m.from, kind: replyKind(m.kind), body, replyTo: id, files: [], status: m.kind === "task" ? "done" : "" });
    if (m.state === "held") await put(this.store, "inbox", id, { ...(await this.store.get("inbox", id)), state: "answered" });
    this.changed();
    return { note: r.state === "queued" ? "Your answer waits to be sent; it is retried automatically." : "Answered." };
  }

  // v1Threads are the device conversations: each peer device's messages,
  // received and sent, joined by replies as the core does (peerThreads),
  // each oldest first.
  async v1Threads() {
    const msgs = [...(await this.store.all("inbox")).filter((m) => m.v === 1 && !m.aside && !this.erasedRow(m)).map((m) => ({ ...m, dir: "in", peer: m.from })),
      ...(await this.store.all("outbox")).filter((r) => r.v === 1 && !r.aside && !this.erasedRow(r)).map((r) => ({ ...r, dir: "out", peer: r.to }))]; // a deleted thread's messages are not shown
    const byID = new Map(msgs.map((m) => [m.id, m]));
    const parent = new Map(msgs.map((m) => [m.id, m.id]));
    const find = (x) => { while (parent.get(x) !== x) { parent.set(x, parent.get(parent.get(x))); x = parent.get(x); } return x; };
    for (const m of msgs) {
      const r = m.reply_to && byID.get(m.reply_to);
      if (r && r.peer === m.peer) parent.set(find(m.id), find(r.id));
    }
    const groups = new Map();
    for (const m of msgs) groups.set(find(m.id), [...(groups.get(find(m.id)) || []), m]);
    return [...groups.values()].map((g) => g.sort((a, b) => a.at - b.at || (a.id < b.id ? -1 : 1)));
  }

  // threadSummaries summarizes every device thread, archived topics
  // included, as topics (client peerTopics, topicText), newest first.
  async threadSummaries() {
    const out = [], now = Math.floor(this.now() / 1000), locals = await this.topicLocals(), pins = new Map();
    for (const g of await this.v1Threads()) {
      const peer = g[0].peer;
      if (!pins.has(peer)) pins.set(peer, await this.store.get("pins", peer)); // one read per peer, not per topic
      out.push(this.topicSummary(g, locals, now, pins.get(peer)));
    }
    return out.sort(byNewest);
  }

  topicSummary(g, locals, now, pin) {
    const first = g[0], last = g[g.length - 1];
    const replied = new Set(g.filter((m) => m.status !== wire.StatusProgress).map((m) => m.reply_to).filter(Boolean)); // a progress update never settles Waiting (client.ThreadSummary)
    // A review notice is a report from that machine (client.ThreadSummary):
    // counted while open, and a thread of nothing but reports is not a
    // conversation (the page lists it under reports).
    const notice = (m) => m.kind === "message" && m.status === "review_notice" && !m.reply_to && !(m.attachments || []).length;
    const line = (m) => (notice(m) ? this.noticeLine({...m,from:m.from||this.address}) : firstLine(m.body));
    const local = this.topicLocalOf(locals, first.peer, g);
    // A browser device runs no agent: a received request it answered
    // (replyV1: "answered") was answered by hand, the Go client's "manual".
    const state = (m) => notice(m) ? (m.resolved ? "resolved" : "needs_human") : m.dir === "in" && topicRequest(m.kind) && m.state === "answered" ? "manual" : m.state || "";
    const facts = g.map((m) => ({ id: m.id, reply_to: m.reply_to || "", at: Math.floor(m.at / 1000), in: m.dir === "in", kind: m.kind,
      state: state(m), status: m.status || "", topic_done:!!m.topic_done, notice: notice(m), selected: false }));
    const v = deriveTopic(facts, local, now), concluded = v.conclusion ? g.find((m) => m.id === v.conclusion) : null;
    // The thread's agent (client.ThreadSummary.AgentID): the latest one a
    // request names as its target, or an answer, result or progress names
    // as its author.
    const agent = g.map((m) => (m.kind === "question" || m.kind === "task" ? m.target?.agent_id : m.agent_id) || "").filter(Boolean).at(-1);
    const t = { id: first.id, peer: first.peer, title: line(first), last: line(last), last_at: iso(last.at), count: g.length, ...(agent ? { agent_id: agent } : {}),
      review: g.filter((m) => m.dir === "in" && m.state === "held").length, unread: g.filter((m) => m.dir === "in" && !m.read).length, running: 0,
      waiting: g.some((m) => m.dir === "out" && (m.kind === "question" || m.kind === "task") && !replied.has(m.id)),
      key_changed: !!(pin && pin.pending), notices: g.filter((m) => m.dir === "in" && notice(m) && !m.resolved).length, notice_only: g.every(notice),
      state: v.state, pending: v.pending, quiet_since: iso(v.quiet_since * 1000), ...(v.done_by ? { done_by: v.done_by } : {}),
      ...(concluded ? { conclusion: firstLine(concluded.body), concluded_by: concluded.dir === "in" ? first.peer : this.address } : {}) };
    if (local.title) Object.assign(t, { auto_title: t.title, title: local.title, renamed: true });
    return t;
  }

  // ---- topics: what the person set here, and the All topics list -------------------------------
  // Kept in this device's IndexedDB (kv "topic/<peer>/<id>", type
  // "topic-state"), like read marks: never sent to another device.

  async topicLocals() {
    const out = new Map();
    for (const v of await this.store.prefix("kv", "topic/")) if (v?.type === "topic-state") out.set(v.peer + "/" + v.topic, v);
    return out;
  }

  // topicLocalOf: the row naming the topic's earliest message, else one naming any of its messages (client localOf).
  topicLocalOf(locals, peer, g) {
    for (const m of g) { const l = locals.get(peer + "/" + m.id); if (l) return l; }
    return {};
  }

  // topicOverview: the overview's device threads and each peer's topic
  // counts (client TopicOverview): archived topics only counted unless
  // listArchived (GET /api/overview without topics=1: a page that knows
  // nothing of topics still reaches every thread).
  async topicOverview(listArchived) {
    const all = await this.threadSummaries(), threads = [], counts = new Map();
    for (const t of all) {
      if (listArchived || t.state !== "archived" || t.notice_only) threads.push(t);
      if (t.notice_only) continue;
      const c = counts.get(t.peer) || { peer: t.peer, total: 0, archived: 0, archived_unread: 0, latest: t }; // all is newest first
      c.total++;
      if (t.state === "archived") { c.archived++; c.archived_unread += t.unread; }
      counts.set(t.peer, c);
    }
    return { threads, topics: [...counts.values()].sort((a, b) => (a.peer < b.peer ? -1 : 1)) };
  }

  // topicList is GET /api/topics (ui livetopics.go, client Topics).
  async topicList(q) {
    if(q.get("conv"))return this.chatTopicList(q);
    const state = q.get("state") || "", peer = q.get("peer") || "", before = q.get("before") || "", raw = q.get("limit");
    const limit = raw === null || raw === "" ? 0 : /^-?\d+$/.test(raw) ? Number(raw) : NaN;
    if (!["", "active", "done", "archived"].includes(state) || !(limit >= 0 && limit <= TOPICS.pageMax)) throw new Error(topicQueryError);
    let after = null;
    if (before) {
      const [at, p, id, ...rest] = before.split("|");
      if (rest.length || !/^\d+$/.test(at || "") || p === undefined || !id) throw new Error(topicQueryError);
      after = { last_at: iso(Number(at) * 1000), peer: p, id };
    }
    const words = (q.get("q") || "").toLowerCase().split(/\s+/).filter(Boolean);
    const matched = (await this.threadSummaries()).filter((t) => !t.notice_only && (!peer || t.peer === peer) && (!state || t.state === state)
      && words.every((w) => (t.title + "\n" + (t.auto_title || "") + "\n" + t.last).toLowerCase().includes(w)));
    const start = after ? matched.findIndex((t) => newerTopic(after, t)) : 0;
    const from = start < 0 ? matched.length : start, page = matched.slice(from, from + (limit || TOPICS.pageDefault));
    const end = from + page.length;
    return { topics: page, ...(end < matched.length && page.length ? { next: topicCursor(page[page.length - 1]) } : {}), matched: matched.length };
  }

  // changeTopic is POST /api/topic/{rename,done,reopen} (ui livetopics.go, client setTopic).
  async changeTopic(what, body) {
    if(!body||typeof body!=="object"||Array.isArray(body)||Object.keys(body).some(k=>!["peer","conv","id","ids","counts","title","count"].includes(k))||body.conv!=null&&typeof body.conv!=="string"||["peer","id","title"].some(k=>body[k]!=null&&typeof body[k]!=="string")||body.count!=null&&(!Number.isInteger(body.count)||body.count<0)||body.ids!=null&&(!Array.isArray(body.ids)||body.ids.some(id=>typeof id!=="string")))throw Error("Bad request.");
    if(body.counts!=null&&(typeof body.counts!=="object"||Array.isArray(body.counts)||Object.values(body.counts).some(n=>!Number.isInteger(n)||n<1)))throw Error("Bad topic counts.");
    if(body?.ids) { if(!Array.isArray(body.ids)||!body.ids.length||body.ids.length>TOPICS.pageMax||!["done","archive","delete"].includes(what))throw Error("Bad bulk action.");let changed=0,stale=0;for(const id of [...new Set(body.ids)]){try{const count=body.counts?.[id]??body.count;if(body.counts&&(!Number.isInteger(count)||count<1))throw Error("Invalid topic count.");const r=await this.changeTopic(what,{...body,ids:undefined,id,count});if(r.note===topicNewer)stale++;else changed++;}catch(e){throw Error(changed+" topics changed; remaining topics unchanged: "+e.message);}}return {note:changed+" topics changed."+(stale?" "+stale+" topics kept active because newer messages arrived.":"")}; }
    if(body?.conv)return this.changeChatTopic(what,body);
    if (!["rename", "done", "reopen","archive","delete"].includes(what)) throw new Error("No such topic change.");
    if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some((k) => !["peer", "id", "title", "count","conv","ids","counts"].includes(k))
      || body.count != null && !Number.isInteger(body.count)) throw new Error("Bad request.");
    const peer = String(body.peer || ""), id = String(body.id || "");
    const g = (await this.v1Threads()).find((x) => x[0].id === id && x[0].peer === peer);
    if (!g) throw new Error("No such topic here.");
    if(what==="delete")return this.deleteThread(peer,id);
    const locals = await this.topicLocals(), was = g.find((m) => locals.has(peer + "/" + m.id));
    const l = { ...(was ? locals.get(peer + "/" + was.id) : {}), type: "topic-state", peer, topic: id, updated_at: Math.floor(this.now() / 1000) };
    let note;
    if (what === "rename") {
      const title = String(body.title || "").split(/\s+/).filter(Boolean).join(" ");
      if ([...title].length > TOPICS.titleMax) throw new Error("A topic's name is at most " + TOPICS.titleMax + " characters.");
      if (title) l.title = title; else delete l.title;
      note = title ? "Topic renamed on this device." : "Topic named after its first message again.";
    } else {
      // count: how many messages the page showed; a mark never covers one the person has not seen (client mark).
      const seen = body.count > 0 && body.count < g.length ? body.count : g.length;
      Object.assign(l, { mark: what === "done" ? "done" : what==="archive" ? "archived" : "open", mark_at: Math.floor(this.now() / 1000), mark_count: seen });
      note = what === "done" ? (seen === g.length ? "Marked done on this device. A new message makes it active again." : topicNewer) : "Reopened on this device.";
    }
    const ops = [{ s: "kv", k: "topic/" + peer + "/" + id, v: l }];
    if (was && was.id !== id) ops.push({ s: "kv", k: "topic/" + peer + "/" + was.id, v: undefined });
    await this.store.write(ops);
    this.changed();
    return { note };
  }

  async outgoingTopic(conv,topic,reply) {
    if(topic==="new")return wire.newID();
    if(topic){if(!wire.validID(topic))throw Error("Invalid topic.");return topic;}
    if(!reply)return "";
    const msgs=await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox")),m=msgs.find(m=>m.id===reply||m.lid===reply);
    return m?chatTopicAssignments(msgs).get(m.lid)||"":"";
  }
  async chatTopicHead(conv,topic){const msgs=await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox")),assigned=chatTopicAssignments(msgs);return sortChatTurns(msgs.filter(m=>assigned.get(m.lid)===topic&&!m.sub&&!m.topic_event)).at(-1)?.lid||"";}
  async chatTopics(conv,msgs=null) {
    msgs ||= await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox"));
    // Sent records must carry their actual author, just as native ConvMessage.
    msgs=msgs.map(m=>m.to?{...m,from:this.address}:m);
    const ts=summarizeChatTopics(conv,msgs,await this.topicLocals(),Math.floor(this.now()/1000)),assigned=chatTopicAssignments(msgs);
    for(const t of ts)t.unread=msgs.filter(m=>!m.to&&!m.own&&!m.read&&!m.topic_event&&assigned.get(m.lid)===t.id).length;
    return ts;
  }
  async decorateChatTopics(view) {
    const msgs=await this.convMessages(view.id,await this.store.all("inbox"),await this.store.all("outbox")),shown=new Set(view.messages.map(m=>m.lid)),visible=msgs.filter(m=>shown.has(m.lid)),assigned=chatTopicAssignments(visible);
    view.topics=await this.chatTopics(view.id,visible);
    for(const m of view.messages){m.topic=assigned.get(m.lid)||"";const raw=visible.find(r=>r.lid===m.lid);if(raw?.topic_event)m.topic_event=raw.topic_event;}
    return view;
  }
  async chatTopicList(q) {
    const conv=q.get("conv"),state=q.get("state")||"",words=(q.get("q")||"").toLowerCase().split(/\s+/).filter(Boolean),limit=Number(q.get("limit")||TOPICS.pageDefault)||TOPICS.pageDefault,before=q.get("before")||"";
    if(!["","active","done","archived"].includes(state)||!Number.isInteger(limit)||limit<1||limit>TOPICS.pageMax)throw Error(topicQueryError);
    const matched=(await this.chatTopics(conv)).filter(t=>(!state||t.state===state)&&words.every(w=>(t.title+"\n"+(t.auto_title||"")+"\n"+t.last).toLowerCase().includes(w)));
    const cursor=t=>new Date(t.last_at).toISOString().replace(/[-:TZ.]/g,"").slice(0,14)+"|"+conv+"|"+t.id;
    const start=before?matched.findIndex(t=>cursor(t)===before)+1:0;if(before&&!start)throw Error(topicQueryError);
    const page=matched.slice(start,start+limit);return {topics:page,matched:matched.length,...(start+page.length<matched.length?{next:cursor(page.at(-1))}:{})};
  }
  async changeChatTopic(what,body) {
    if(!["create","done","reopen","rename","archive","delete"].includes(what))throw Error("No such topic change.");
    const {conv,id}=body;if(!wire.validHash(conv)||!wire.validID(id))throw Error("Invalid topic.");
    const msgs=await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox")),assigned=chatTopicAssignments(msgs),topics=await this.chatTopics(conv,msgs),t=topics.find(t=>t.id===id);
    if(what==="create"?!msgs.some(m=>m.lid===id&&!m.sub&&!m.topic_event):!t)throw Error("No such topic here.");
    if(["create","done","reopen"].includes(what)){
      if(what==="done"&&body.count>0&&body.count!==t.count)return {note:topicNewer};
      const seen=msgs.filter(m=>assigned.get(m.lid)===id&&!m.topic_event).map(m=>m.lid).sort(),action=what==="reopen"?"open":what;
      const prior=sortChatEvents(msgs.filter(m=>m.topic===id&&m.topic_event));
      await this.sendDM({conv,topic:id,reply_to:prior.at(-1)?.lid||"",topic_event:{action,seen},body:{create:"Made a topic.",done:"Marked this topic done.",reopen:"Reopened this topic."}[what]});
      return {note:what==="create"?"Topic created. Its replies stay here.":what==="done"?"Marked done for everyone. A new message reopens it.":"Reopened for everyone."};
    }
    if(what==="delete"){
      const inbox=await this.store.all("inbox"),outbox=await this.store.all("outbox"),names=new Map(),deletion=wire.newID(),ops=[];
      for(const r of [...inbox,...outbox])if(r.conv===conv&&assigned.get(r.lid)===id&&this.erasable(r)&&!this.erasedRow(r)){const n=this.rowName(r);if(n.key&&n.lid)names.set(erasedKey(conv,n.key,n.lid),n);}
      for(const [k,n]of names){ops.push({s:"erased",k,v:{conv,key:n.key,lid:n.lid,deletion,shared:false}});this.erased.add(k);}
      try{ops.push(...this.eraseOps(conv,inbox,outbox));await this.store.write(ops);}finally{await this.loadErased();}this.changed();await this.shareErased();return {note:"Deleted for you and your devices. Other people keep their copies."};
    }
    const key="topic/"+conv+"/"+id,l={...(await this.store.get("kv",key)||{}),type:"topic-state",peer:conv,topic:id};
    if(what==="rename"){const title=String(body.title||"").split(/\s+/).filter(Boolean).join(" ");if([...title].length>TOPICS.titleMax)throw Error("Topic name too long.");l.title=title;}else Object.assign(l,{mark:"archived",mark_at:Math.floor(this.now()/1000),mark_count:t.count+msgs.filter(m=>m.topic===id&&m.topic_event).length});
    await put(this.store,"kv",key,l);this.changed();return {note:what==="rename"?"Topic renamed on this device.":"Archived on this device. Nothing deleted."};
  }

  // ---- quiet group proof/current-context carriers (grp1 remains off) ---------------------------

  async groupRecord(conv) {
    const g = await this.store.get("kv", "group/" + conv);
    if (!g?.context) return null;
    const p = wire.parseGroupContext(g.context);
    return { id:conv, kind:"group", root:g.root, created:p.root.created, creator:p.root.creator.address };
  }

  async groupThread(conv) {
    const g=await this.store.get("kv","group/"+conv), packet=wire.parseGroupContext(g.context);
    let frozen="",members=[],infos=[],role="";
    try{const current=await this.groupCurrentState(conv);members=await this.groupPeople(current);infos=await this.participationsOf({id:conv,kind:"group",root:g.root});
      if(members.some(p=>p.person===this.me.person))role="member";
      else if(infos.some(i=>i.external&&i.host?.address===this.address&&i.host.fingerprint===this.fp&&["invited","active","dismissed"].includes(i.state)&&!i.held))role=infos.some(i=>i.role==="human"&&i.host?.address===this.address&&i.host.fingerprint===this.fp)?"human_guest":"visitor";
      else throw Error("Group current admission or visitor invitation is unavailable.");
    }catch(e){frozen=e.message;members=await this.groupPeople(packet).catch(()=>[]);}
    const inbox=await this.store.all("inbox"),outbox=await this.store.all("outbox"),ctls=[...inbox,...outbox].filter(r=>r.control&&r.conv===conv);
    const privateReports = await this.ownNeedsYouReports(inbox, outbox);
    const visitorPIDs=new Set(infos.filter(i=>i.external&&i.host?.address===this.address&&i.host.fingerprint===this.fp).map(i=>i.pid));
    const messages=(await this.convMessages(conv,inbox,outbox)).filter(m=>role==="member"||visitorPIDs.has(m.pid||m.excerpt_pid)||m.human?.audience.some(s=>visitorPIDs.has(s.pid))),shownReply=linkReplies(messages,this.fp);
    const personLabel=pid=>pid===this.me.person?"You":members.find(p=>p.person===pid)?.label||"Someone";
    const agentCards=new Map();
    for(const i of infos) {
      if(!i.host || i.role==="human")continue;
      const v=this.agentView(i,messages,null,members,role),key=v.host.address+"#"+v.host.fingerprint+"#"+(v.agent_id||"")+(["active","invited"].includes(v.state)?"":"#"+v.pid);
      const prior=agentCards.get(key);
      if(prior && ["active","invited"].includes(prior.state) && ["active","invited"].includes(v.state)) {
        const rank=x=>x.state==="active"?0:1;
        const earlier=(a,b)=>(a.invited||"")<(b.invited||"") || a.invited===b.invited && a.pid<b.pid;
        const chosen=rank(prior)<rank(v)||rank(prior)===rank(v)&&(prior.shared.length>v.shared.length||prior.shared.length===v.shared.length&&earlier(prior,v))?prior:v;
        chosen.pids=[...prior.pids,...v.pids];
        chosen.shared=[...new Set([...prior.shared,...v.shared])];
        chosen.tasks_from=[...new Map([...prior.tasks_from,...v.tasks_from].map(p=>[p.person,p])).values()];
        chosen.inviters=[...new Map([...(prior.inviters||[prior.inviter]),...(v.inviters||[v.inviter])].map(p=>[p.person,p])).values()];
        agentCards.set(key,chosen);
      } else agentCards.set(key,v);
    }
    for(const card of agentCards.values()) {
      const grants=new Map(infos.filter(i=>card.pids.includes(i.pid)).flatMap(i=>i.grant).map(r=>[r.fingerprint+"/"+r.lid,r]));
      card.missing=[...grants.values()].filter(ref=>!messages.some(m=>!m.sub&&m.lid===ref.lid&&(m.fp===ref.fingerprint&&!m.replica || !m.fp&&!m.history&&this.fp===ref.fingerprint || card.pids.includes(m.excerpt_pid)&&m.claimed_key===ref.fingerprint&&m.history))).length;
    }
    return {id:conv,kind:"group",title:packet.state.title,peer:{label:packet.state.title,address:"",state:""},role,members,frozen,created:iso(packet.root.created*1000),mine:packet.root.creator.address===this.address,agents:[...agentCards.values()],guests:infos.filter(i=>i.role==="human").map(i=>this.guestView(i,role==="member")),audience_pending:infos.some(i=>i.role==="human"&&i.state==="active"&&i.held),messages:await Promise.all(messages.map(async m=>{
      const here=!m.fp&&!m.history,out=here||!!m.own,event=m.sub==="event"?this.eventText(m.body,null,members):"",fp=m.fp||this.fp,ev=event?this.eventFields(m.body,members):null;
      const targetPerson=await this.personOfFp(fp),rel=ctls.filter(x=>x.ref?.id===m.lid&&x.ref.fingerprint===fp&&x.sub!==wire.SubStatus&&x.sub!==wire.SubDecision);
      const view=event||m.excerpt_pid?{can:[],reactions:[]}:this.controlsOn(rel,targetPerson,x=>x.person||"",personLabel,p=>p===this.me.person,undefined,pid=>infos.find(i=>i.pid===pid)?.host?.label||"");
      view.can=view.deleted||event||m.excerpt_pid||frozen||role!=="member"?[]:["react",...(targetPerson===this.me.person?["edit","delete"]:[])];
      const answered=messages.some(r=>r.pid===m.pid&&(r.reply_to===m.lid||shownReply(r.reply_to)===m.id)&&["answer","result"].includes(r.kind));
      const exec=m.target&&["question","task"].includes(m.kind)?this.execOn(ctls.filter(x=>x.sub===wire.SubStatus&&x.ref?.id===m.lid&&x.ref.fingerprint===fp&&x.from===m.target.address),m.target.address,answered):null;
      return {id:m.id,lid:m.lid,dir:out?"out":"in",from:here?this.address:m.from,kind:m.kind,status:m.status||"",actions:await this.proposalActions(m),body:event?"":m.body,event,...(ev&&ev.type?{event_type:ev.type,event_by:ev.by}:{}),pid:m.pid||"",...(m.human?.author_pid?{agent_author_pid:m.human.author_pid}:{}),...(m.target?{target:m.target,to:m.target.address}:{}),...(m.agent_id?{agent_id:m.agent_id}:{}),...(m.excerpt_pid?{excerpt_pid:m.excerpt_pid}:{}),reply_to:shownReply(m.reply_to),quote:shownReply(m.quote),sent_at:sentAt(m),delivery:m.delivery||"",at:iso(m.at),send_group:m.send_group_conflict?"":m.send_group||"",send_group_author:here?this.fp:m.fp||m.claimed_key||"",origin:m.origin||"",verified_agent:verifiedAgent(m,infos.find(i=>i.pid===(m.human&&wire.agentAuthor(m.human)?m.human.author_pid:m.pid)),here?this.address:m.from,fp),job_detail:this.needsYouText(m,privateReports),state:m.state||"",...this.cancellationView(m),state_text:event?"":here?this.outStateText(m,"the group"):"",detail:m.detail||"",unread:!out&&!m.read,replica:!!m.replica,synced_from:m.history?m.synced_from:"",claimed_key:m.claimed_key||"",via:m.own&&!m.history?m.from:"",copies:here?await this.shownCopies(m.copies):undefined,group_ref:!frozen&&role==="member"&&m.kind==="message"&&!m.sub&&!m.pid&&!m.excerpt_pid?{lid:m.lid,author:m.claimed_key||m.fp||this.fp,hash:await wire.groupHistoryContentHash(conv,m)}:undefined,...view,...(exec?{exec,...(this.continuationOf(m,fp,exec)?{continuation:this.continuationOf(m,fp,exec),actions:["continue"]}:{})}:{}),attachments:await Promise.all((m.attachments||[]).map(async(a,i)=>({index:i,name:wire.safeName(a.name),size:a.size,...(here?await this.sentState(a):this.fileState(a))}))) };
    }))};
  }

  async groupPeople(packet, checks = []) {
    const out = [];
    for (const m of await wire.effectiveGroupMembers(packet.state, packet.withdrawals || [])) {
      const p = await this.groupRead(checks, m.person === this.me?.person ? "kv" : "persons", m.person === this.me?.person ? "person" : m.person);
      if (!p || p.state === "conflict" || !(p.hashes || []).includes(m.roster)) throw new Hold("proof_pending","Group member identity is not current here.");
      out.push({...this.personView(p),admin:m.admin});
    }
    return out;
  }

  async groupSendGate(c, rec) {
    if(rec?.forwarded)return this.disclosureGate(c,rec);
    if(rec?.group_withdrawal)return this.groupWithdrawalGate(rec);
    if(rec?.control && [wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(rec.sub)) {
      try {const members=await this.dmMembers(c);await this.groupCurrent(c.id);await this.groupControlTarget(c.id,rec.ref);
        const pin=await this.pinned(rec.to), fence=await this.groupControlFence(members,this.fp,rec.recipient_fp);
        if(pin.pending||pin.fingerprint!==rec.recipient_fp||fence!==rec.group_admission)throw Error("Group control sender or recipient admission changed.");return {why:"",pin};
      }catch(e){return {why:e.message};}
    }
    if(rec?.human&&(!rec.pid || rec.pid===rec.human.author_pid)) {
      try {
        const members=await this.dmMembers(c);
        if(!rec.human.author_pid && (!rec.group_admission||members.epochs.get(this.fp)!==rec.group_admission))throw Error("Group sender admission changed; saved copy is not sent.");
        return this.humanGate(c,rec);
      } catch(e) {return {why:e.message};}
    }
    if(rec?.pid && ![wire.SubGroupProof,wire.SubGroupContext].includes(rec.sub))return this.externalGate(c,rec);
    if(rec?.pid && [wire.SubGroupProof,wire.SubGroupContext].includes(rec.sub)) {
      try {
        const packet=await this.groupCurrentState(c.id), members=await this.dmMembers(c), pin=await this.pinned(rec.to);
        if(pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Group PID carrier exact key changed.");
        if(![...members.values()].some(p=>p.devices.some(d=>d.address===rec.to&&d.fingerprint===pin.fingerprint))) {
          const inv=await this.groupVisitorInvite(packet.root,rec.pid,[],rec.to,pin.fingerprint);
          const g=await this.store.get("kv","group/"+c.id), original=g.records[inv.e.group.seq]&&wire.parseGroupCommit(g.records[inv.e.group.seq]);
          if(!original||original.hash!==inv.e.group.hash)throw Error("Group visitor original invitation slot differs.");
        }
        return {why:"",pin};
      }catch(e){return {why:e.message};}
    }
    try {
      if (!c) throw Error("Group context is not kept here.");
      const packet = await this.groupCurrent(c.id), members = await this.groupPeople(packet);
      if (rec) {
        const dev = members.flatMap(m => m.devices || []).find(d => d.address === rec.to && d.fingerprint === rec.recipient_fp);
        const self = wire.groupMember(packet.state,this.me.person);
        if (!dev || rec.group_admission && await wire.groupAdmissionHash(self.admission) !== rec.group_admission) throw Error("Group audience or admission changed; this saved copy is not sent.");
        const pin = await this.store.get("pins", rec.to);
        if (!pin || pin.pending || pin.fingerprint !== rec.recipient_fp) throw Error("Group recipient key changed; this saved copy is not sent.");
        if (rec.sub === "history") {
          const h=wire.parseHistory(rec.body), person=members.find(m=>m.devices?.some(d=>d.address===rec.to&&d.fingerprint===rec.recipient_fp));
          const admission=wire.groupMember(packet.state,person.person).admission;
          if(await wire.groupAdmissionHash(admission)!==rec.recipient_admission)throw Error("Saved group history recipient admission changed.");
          if(rec.group_history) {if(person.person!==this.me.person||h.group_admission!==await wire.groupAdmissionHash(admission))throw Error("Saved own group history source admission changed.");if(h.pid||h.ref)await this.groupParticipationHistoryCheck(c.id,h,{address:this.address,fingerprint:this.fp});}
          else if(!wire.groupAllowsHistory(admission,{lid:h.lid,author:h.from_key,hash:await wire.groupHistoryContentHash(c.id,h)}))throw Error("Saved group history no longer has its exact recipient grant.");
        } else if(rec.sub==="file") {
          const m=wire.parseGroupFileMsg(rec.body);
          await this.groupFileAuthorized(packet,members,m.type==="offer"?rec.to:this.address,m.type==="offer"?rec.recipient_fp:this.fp,m);
          await this.groupFileSource(c.id,m);
        }
        return {why:"",pin};
      }
      return {why:""};
    } catch(e) { return {why:e.message}; }
  }

  async requireGroupInvitationControl(address,pin) {
    const key=await this.pubOf(pin),profile=await this.profile(address);
    if(!await wire.profileSupports(profile||{},address,key.sign_key,wire.CapGroupInvitationControl))throw Object.assign(Error(address+" needs an update for refreshable group invitations (gic1)."),{code:"group_invitation_unsupported"});
  }

  async groupSupport(address, pin) {
    const [ok, why, notify] = await this.supports(address,pin);
    if (!ok) throw Object.assign(Error(why), { code: "group_unsupported", address });
    const profile = await this.profile(address), key = await this.pubOf(pin);
    if (!(await wire.profileSupports(profile || {},address,key.sign_key,wire.CapGroup))) throw Object.assign(Error(address + " cannot read groups with this version."), { code: "group_unsupported", address });
    return notify;
  }

  async groupCarrierCopy(root, sub, descriptor, value, device, extra = {}, knownKeysOnly = false) {
    const pin = knownKeysOnly ? await this.store.get("pins",device.address) : await this.pinned(device.address);
    if(!pin)throw Error("Exact group recipient key is not kept here.");
    if (pin.pending || pin.fingerprint !== device.fingerprint) throw Error("Group recipient key changed.");
    if(!knownKeysOnly)await this.groupSupport(device.address,pin);
    const key = await this.pubOf(pin), bytes = new TextEncoder().encode(typeof value === "string" ? value : JSON.stringify(value));
    const limit = [wire.SubGroupContext,wire.SubGroupInvite,wire.SubGroupConsent].includes(sub) ? wire.MaxGroupState : wire.MaxBody-1024;
    if (!bytes.length || bytes.length > limit) throw Error("Group carrier exceeds bound.");
    const sealed = await wire.encryptFile(bytes,sub + ".json",key), id = wire.newID(), lid = wire.newID();
    const body = wire.groupCarrierJSON({...descriptor,to_key:pin.fingerprint});
    const envelope = await wire.seal({v:wire.Version2,id,from:this.address,to:device.address,ts:Math.floor(this.now()/1000),kind:"message",body,conv:await wire.rootID(root),lid,root:wire.rootJSON(root),sub,...(extra.pid?{pid:extra.pid}:{}),attachments:[sealed.attachment]},this.keys,key);
    return {id,lid,conv:await wire.rootID(root),to:device.address,recipient_fp:pin.fingerprint,required_cap:(sub===wire.SubGroupInvite&&wire.parseGroupInvitation(value).nonce || sub===wire.SubGroupConsent&&wire.parseGroupConsent(value).decision==="cancelled")?wire.CapGroupInvitationControl:wire.CapGroup,sub,body,envelope,at:this.now(),state:"queued",aside:true,files:[{...sealed,uploaded:false}],...extra};
  }

  groupProofPages(records) {
    const pages = []; let current=[];
    for (const record of records) {
      const next=[...current,record];
      if (next.length>16 || new TextEncoder().encode(wire.groupJournalJSON({records:next,more:false})).length>wire.MaxBody-1024) { if(!current.length) throw Error("Original proof record exceeds carrier bound."); pages.push(current); current=[record]; }
      else current=next;
    }
    if(current.length) pages.push(current);
    for(const page of pages) wire.parseGroupJournal(wire.groupJournalJSON({records:page,more:false}));
    return pages;
  }

  async createGroup(title) {
    const checks=[], me=await this.groupRead(checks,"kv","person");
    if(!me || me.state!=="self" || !wire.validID(this.realm || "")) throw Error("Set up and connect your person before creating a group.");
    const root=await wire.signGroupRoot(this.keys,{v:wire.GroupRootVersion,kind:"group",realm:this.realm,title,creator:{person:me.person,roster:me.hash,address:this.address,fingerprint:this.fp},members:[{person:me.person,roster:me.hash}],admins:[me.person],nonce:wire.newID(),created:Math.floor(this.now()/1000)});
    const conv=await wire.rootID(root), admission=await wire.signGroupAdmission(this.keys,{conv,realm:this.realm,person:me.person,roster:me.hash,seq:0,prev:"",history:null,by:this.fp});
    const state=await wire.signGroupState(this.keys,{v:1,conv,realm:this.realm,seq:0,prev:"",title,members:[{person:me.person,roster:me.hash,admin:true,admission}],actor:me.person,actor_roster:me.hash,by:this.fp});
    await this.publishGroupPacket({root,proof:null,state,withdrawals:null},null,checks);
    return conv;
  }

  async manageGroup(change) {
    if(!change||typeof change!=="object"||Array.isArray(change)||Object.keys(change).some(k=>!["conv","action","title","person"].includes(k))||!["rename","promote","demote","remove","leave"].includes(change.action))throw Error("Unknown group action.");
    if(change.action==="rename"?change.person:change.action==="leave"?change.person||change.title:change.title||!change.person)throw Error("Group action has unexpected target fields.");
    if(change.action==="leave")return this.leaveGroup(change.conv);
    const checks=[],{packet}=await this.groupTurnEvidence(change.conv,checks), me=await this.groupRead(checks,"kv","person"), member=wire.groupMember(packet.state,me.person);
    if(!member?.admin)throw Error("Current person administrator required.");
    const state=structuredClone(packet.state);
    if(change.action==="rename") {state.title=change.title;if(state.title===packet.state.title)return {queued:false};}
    else {
      const target=wire.groupMember(state,change.person);if(!target)throw Error("Choose an exact current group member.");
      if(change.action==="promote") {if(target.admin)return {queued:false};target.admin=true;}
      else if(change.action==="demote") {if(!target.admin)return {queued:false};target.admin=false;}
      else state.members=state.members.filter(m=>m.person!==change.person);
      if(!wire.groupAdmins(state).length)throw Error("The last administrator must appoint a successor first.");
    }
    state.seq++;state.prev=await wire.groupStateHash(packet.state);state.actor=me.person;state.actor_roster=me.hash;state.by=this.fp;
    await this.publishGroupPacket({root:packet.root,proof:null,state:await wire.signGroupState(this.keys,state),withdrawals:packet.withdrawals || null},null,checks);return {queued:false};
  }

  async leaveGroup(conv) {
    const checks=[],{packet,g,members}=await this.groupTurnEvidence(conv,checks), me=await this.groupRead(checks,"kv","person"), member=wire.groupMember(packet.state,me.person);
    if(member.admin) {
      if(wire.groupAdmins(packet.state).length===1)throw Error("The last administrator must appoint a successor first.");
      return this.manageGroup({conv,action:"remove",person:me.person});
    }
    const withdrawal=await wire.signGroupWithdrawal(this.keys,{conv,realm:packet.root.realm,person:me.person,admission:await wire.groupAdmissionHash(member.admission),roster:me.hash,by:this.fp}), text=wire.groupWithdrawalJSON(withdrawal), hash=wire.hex(await wire.sha256(wire.groupWithdrawalCanonical(withdrawal))), copies=[];
    const visitors=await this.groupVisitorTargets(conv,packet,checks);
    for(const current of members)for(const device of current.devices || []) {
      if(device.address===this.address)continue;
      await this.groupRead(checks,"pins",device.address);
      copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupWithdrawal,{v:1,seq:packet.state.seq,hash},text,device,{group_withdrawal:text,recipient_admission:await wire.groupAdmissionHash(wire.groupMember(packet.state,current.person).admission)},true));
    }
    const next=structuredClone(g);if(!next.withdrawals.includes(text))next.withdrawals.push(text);
    const records=g.records.map(wire.parseGroupCommit),update={...packet,memberships:undefined,withdrawals:[...packet.withdrawals,withdrawal]};
    for(const target of visitors) {
      await this.groupRead(checks,"pins",target.device.address);
      for(const page of this.groupProofPages(records)){const last=page.at(-1);copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupProof,{v:1,seq:last.seq,hash:last.hash},wire.groupJournalJSON({records:page,more:false}),target.device,{pid:target.pid},true));}
      copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupContext,{v:1,seq:packet.state.seq,hash:await wire.groupStateHash(packet.state)},wire.groupContextJSON(update),target.device,{pid:target.pid},true));
    }
    await this.store.write([{s:"kv",k:"group/"+conv,v:next},...copies.map(v=>({s:"outbox",k:v.id,v}))],checks);this.changed();
    if(this.connected)for(const rec of copies)await this.post(rec);
    return {queued:true};
  }

  async groupWithdrawalGate(rec) {
    try {
      const g=await this.store.get("kv","group/"+rec.conv);if(!g?.context||!g.withdrawals.includes(rec.group_withdrawal))throw Error("This exact local departure is not kept here.");
      const packet=wire.parseGroupContext(g.context), w=wire.parseGroupWithdrawal(rec.group_withdrawal), head=await this.store.get("kv","group-head/"+rec.conv);
      if(w.by!==this.fp || w.person!==this.me?.person || w.roster!==this.me.hash || g.records.length-1!==packet.state.seq || head&&(head.conflict||head.seq>packet.state.seq||head.seq===packet.state.seq&&head.hash!==await wire.groupStateHash(packet.state)))throw Error("Departure is not from this exact current local admission.");
      const own=wire.groupMember(packet.state,w.person);if(!own||own.admin||await wire.groupAdmissionHash(own.admission)!==w.admission)throw Error("Departure admission changed.");
      const resolved=await this.groupResolver(packet.root,[],packet,g,[]);await wire.verifyGroupWithdrawal(w,packet.state,resolved.resolve);
      let recipient;
      for(const member of packet.state.members) {
        const p=resolved.people.get(member.person);if(!p?.devices.some(d=>d.address===rec.to&&d.fingerprint===rec.recipient_fp))continue;
        if(await wire.groupAdmissionHash(member.admission)!==rec.recipient_admission || member.person!==w.person&&await wire.groupWithdrawn(packet.state,member,g.withdrawals.map(wire.parseGroupWithdrawal)))throw Error("Departure recipient admission changed.");recipient=p;
      }
      if(!recipient)throw Error("Departure recipient is no longer a current exact device.");
      const pin=await this.store.get("pins",rec.to);if(!pin||pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Departure recipient key changed.");return {why:"",pin};
    }catch(e){return {why:e.message};}
  }

  async verifyGroupProposal(proposal, owner, address, fp, checks = []) {
    await wire.validateGroupInvitation(proposal);
    const g=await this.groupRead(checks,"kv","group/"+proposal.state.conv);
    if(!g || g.root!==wire.rootJSON(proposal.root)) throw new Hold("proof_pending","Original group proof is not complete here.");
    const records=g.records.map(wire.parseGroupCommit), authority=records[proposal.state.seq];
    if(!authority) throw new Hold("proof_pending","Original group authority is missing.");
    const hint=await this.groupRead(checks,"kv","group-head/"+proposal.state.conv);
    if(records.length-1!==proposal.state.seq)throw Error("Group advanced from state "+proposal.state.seq+" to "+(records.length-1)+"; inviter must refresh the invitation. Join the fresh proposal.");
    if(hint && (hint.conflict || hint.seq>proposal.state.seq || hint.seq===proposal.state.seq&&hint.hash!==await wire.groupStateHash(proposal.state)))throw Error("Latest group head differs from this proposal; inviter must refresh the invitation. Join the fresh proposal.");
    const packet={root:proposal.root,state:proposal.state,withdrawals:proposal.withdrawals}, resolved=await this.groupResolver(proposal.root,[],packet,g,checks,[{person:proposal.target,roster:proposal.roster}]);
    const pins=await this.groupContextWithdrawals(packet,structuredClone(g),resolved.resolve,resolved.people,checks);
    await wire.verifyGroupCurrent(proposal.state,proposal.root,authority,resolved.resolve,seq=>records[seq],pins);
    const admin=wire.groupMember(proposal.state,owner), p=resolved.people.get(owner);
    if(!admin?.admin || await wire.groupWithdrawn(proposal.state,admin,pins) || !p?.devices.some(d=>d.address===address&&d.fingerprint===fp)) throw Error("Inviter is not a current group administrator device.");
    const target=proposal.target===this.me?.person ? await this.groupRead(checks,"kv","person") : await this.groupRead(checks,"persons",proposal.target);
    if(!target || target.state==="conflict" || target.hash!==proposal.roster) throw Error("Invited person changed; obtain fresh consent.");
    return {g,records,resolve:resolved.resolve,target};
  }

  async groupInvitationCapabilities(row) {
    if(row.direction!=="out"||row.inviter!==this.address||row.fp!==this.fp||row.owner!==this.me?.person||!["pending","accepted","stale","reissue","cancelled"].includes(row.status))return {can_cancel:false,can_refresh:false};
    try {
      const current=await this.groupCurrent(row.proposal.state.conv),me=await this.store.get("kv","person"),admin=wire.groupMember(current.state,me.person),target=wire.groupMember(current.state,row.proposal.target);
      if(!me.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp)||!admin?.admin||await wire.groupWithdrawn(current.state,admin,current.withdrawals||[])||target&&!await wire.groupWithdrawn(current.state,target,current.withdrawals||[]))throw Error("Current inviting administrator required.");
      await this.groupInvitationPublicationFence(row);
      return {can_cancel:row.status!=="cancelled",can_refresh:true};
    }catch{return {can_cancel:false,can_refresh:false};}
  }

  async groupInvitations() {
    const [intents,views]=await Promise.all([this.store.prefix("kv","group-invitation/"),this.store.prefix("kv","own-invitation/")]),rows=intents.filter(v=>v?.type==="group-invitation");
    for(const view of views.filter(v=>v?.type==="own-invitation"&&v.fp!==this.fp)) {
      try {await this.readSyncAuthority(view.record,view.from,view.fp,this.address,this.fp);}catch{continue;}
      const r=view.record;rows.push({id:r.id,direction:"out",status:r.status,proposal:r.proposal,inviter:view.from,fp:view.fp,owner:r.person});
    }
    return Promise.all(rows.map(async i=>({id:i.id,conv:i.proposal.state.conv,direction:i.direction,status:i.status,title:i.proposal.state.title,inviter:i.inviter,target:i.proposal.target,history:i.proposal.history || [],...await this.groupInvitationCapabilities(i)})));
  }

  async groupInvitationPublicationFence(row,checks=[]) {
    const p=row.proposal,pending=await this.groupRead(checks,"kv","group-publication/"+p.state.conv+"/"+p.seq);
    if(pending) {
      const member=wire.groupMember(wire.parseGroupContext(pending.context).state,p.target);
      if(member&&member.admission.seq===p.seq&&member.admission.prev===p.prev&&member.admission.roster===p.roster)throw Error("Group publication already started; resolve membership before cancelling.");
    }
  }

  async cancelGroup({id}) {
    const checks=[],k="group-invitation/out/"+id,row=await this.groupRead(checks,"kv",k);
    if(!row)throw Error("No outgoing invitation with that exact ID.");
    if(row.inviter!==this.address||row.fp!==this.fp||row.owner!==this.me?.person)throw Error("This is not this device's invitation.");
    if(row.status==="cancelled")return {cancelled:true};
    if(!["pending","accepted","stale","reissue"].includes(row.status))throw Error("Group invitation cannot be cancelled after publication or decline.");
    const {packet}=await this.groupTurnEvidence(row.proposal.state.conv,checks),me=await this.groupRead(checks,"kv","person"),admin=wire.groupMember(packet.state,me.person);
    if(!admin?.admin)throw Error("Current inviting administrator required.");
    const admitted=wire.groupMember(packet.state,row.proposal.target);if(admitted&&!await wire.groupWithdrawn(packet.state,admitted,packet.withdrawals||[]))throw Error("Group membership already published.");
    await this.groupInvitationPublicationFence(row,checks);
    const target=await this.groupRead(checks,"persons",row.proposal.target);if(!target||target.state==="conflict")throw Error("Invited person's pinned current devices unavailable.");
    const copies=[];
    for(const device of target.devices) {
      await this.groupRead(checks,"pins",device.address);
      copies.push(await this.groupCarrierCopy(row.proposal.root,wire.SubGroupConsent,{v:1,seq:row.proposal.seq,hash:row.proposal.prev},wire.groupConsentJSON({v:1,invitation:id,decision:"cancelled"}),device,{group_lifecycle:id,group_direction:"out",group_cancel:true},true));
    }
    await this.store.write([{s:"kv",k,v:{...row,status:"cancelled"}},...copies.map(v=>({s:"outbox",k:v.id,v}))],checks);this.changed();this.syncInvitations().catch(()=>{});
    if(this.connected)for(const rec of copies)await this.post(rec);
    return {cancelled:true};
  }

  async refreshGroup({id}) {
    const saved=await this.store.get("kv","group-invitation-refresh/"+id);
    if(saved) {const next=await this.store.get("kv","group-invitation/out/"+saved.successor);if(!next||next.inviter!==this.address||next.fp!==this.fp||next.owner!==this.me?.person)throw Error("Refresh successor is not this device's intent.");return (await this.groupInvitations()).find(i=>i.id===next.id&&i.direction==="out");}
    const row=await this.store.get("kv","group-invitation/out/"+id);if(!row)throw Error("No outgoing invitation with that exact ID.");
    await this.cancelGroup({id});
    return this.inviteGroup({conv:row.proposal.state.conv,person:row.proposal.target,history:{refs:row.proposal.history||[]}},id);
  }

  async inviteGroup(draft,refreshID="") {
    for(let attempt=0;attempt<4;attempt++) {
      try{return await this.inviteGroupAttempt(draft,refreshID);}catch(e){if(!(e instanceof StoreConflict)||attempt===3)throw e;}
    }
  }

  async inviteGroupAttempt({conv,person,history={}},refreshID="") {
    const checks=[], g=await this.groupRead(checks,"kv","group/"+conv), packet=await this.groupCurrent(conv);
    const me=await this.groupRead(checks,"kv","person"), target=await this.pinChain(person);
    await this.groupRead(checks,"persons",person);
    const refreshKey=refreshID?"group-invitation-refresh/"+refreshID:"",refreshed=refreshKey?await this.groupRead(checks,"kv",refreshKey):null;
    if(refreshed)return (await this.groupInvitations()).find(i=>i.id===refreshed.successor&&i.direction==="out");
    const refs=await this.selectGroupHistory(conv,history);

    const proposal={v:1,root:packet.root,state:packet.state,withdrawals:packet.withdrawals || null,target:person,roster:target.hash,seq:packet.state.seq+1,prev:await wire.groupStateHash(packet.state),history:refs.length?refs:null,nonce:wire.newID()};
    const {records}=await this.verifyGroupProposal(proposal,me.person,this.address,this.fp,checks), id=await wire.groupInvitationID(proposal), k="group-invitation/out/"+id, old=await this.groupRead(checks,"kv",k);
    if(old) return (await this.groupInvitations()).find(i=>i.id===id&&i.direction==="out");
    const semanticID=await wire.groupInvitationID({...proposal,nonce:undefined}),slot="group-invitation-live/"+semanticID,pointer=await this.groupRead(checks,"kv",slot);
    const candidates=(await this.store.all("kv")).filter(i=>i?.type==="group-invitation"&&i.direction==="out"&&["pending","accepted"].includes(i.status)&&i.inviter===this.address&&i.fp===this.fp&&i.owner===me.person&&i.proposal.state.conv===conv);
    for(const saved of candidates){
      if(await wire.groupInvitationID({...saved.proposal,nonce:undefined})!==semanticID)continue;
      const reusable=await this.groupRead(checks,"kv","group-invitation/out/"+saved.id);
      if(!reusable||!["pending","accepted"].includes(reusable.status))throw new StoreConflict();
      const ops=[{s:"kv",k:slot,v:{id:reusable.id}}];if(refreshKey)ops.push({s:"kv",k:refreshKey,v:{successor:reusable.id}});
      await this.store.write(ops,checks);return (await this.groupInvitations()).find(i=>i.id===reusable.id&&i.direction==="out");
    }

    const copies=[];
    for(const device of target.devices) {
      for(const page of this.groupProofPages(records)) {const last=page.at(-1); copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupProof,{v:1,seq:last.seq,hash:last.hash},wire.groupJournalJSON({records:page,more:false}),device,{group_lifecycle:id,group_direction:"out"}));}
      copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupInvite,{v:1,seq:packet.state.seq,hash:await wire.groupStateHash(packet.state)},wire.groupInvitationJSON(proposal),device,{group_lifecycle:id,group_direction:"out"}));
    }
    const intent={type:"group-invitation",id,direction:"out",status:"pending",proposal,inviter:this.address,owner:me.person,fp:this.fp};
    const ops=[{s:"kv",k,v:intent},{s:"kv",k:slot,v:{id}},...copies.map(v=>({s:"outbox",k:v.id,v}))];if(refreshKey)ops.push({s:"kv",k:refreshKey,v:{successor:id}});
    await this.store.write(ops,checks);this.changed();this.syncInvitations().catch(()=>{});
    if(this.connected)for(const rec of copies) await this.post(rec);
    return (await this.groupInvitations()).find(i=>i.id===id&&i.direction==="out");
  }

  async decideGroup({id,accept}) {
    if(typeof accept!=="boolean") throw Error("Choose accept or decline explicitly.");
    const checks=[], k="group-invitation/in/"+id, row=await this.groupRead(checks,"kv",k);
    if(!row) throw Error("No invitation with that exact ID.");
    const status=accept?"accepted":"declined";
    if(row.status===status) return {recorded:true};
    if(row.status==="cancelled")throw Error("Inviter cancelled this exact invitation. It cannot be joined.");
    if(accept&&row.status==="stale")throw Error("This invitation is out of date; join the newer invitation or ask the inviter to refresh it.");
    if(row.status!=="pending" && (accept || row.status!=="stale")) throw Error("Invitation already decided.");
    if(accept)await this.verifyGroupProposal(row.proposal,row.owner,row.inviter,row.fp,checks);
    const me=await this.groupRead(checks,"kv","person"), p=row.proposal;
    if(me.state==="conflict" || me.person!==p.target || accept&&me.hash!==p.roster || !me.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp)) throw Error("Invitation no longer names this exact person and device.");
    const consent={v:1,invitation:id,decision:status};
    if(accept) consent.admission=await wire.signGroupAdmission(this.keys,{conv:p.state.conv,realm:p.root.realm,person:p.target,roster:p.roster,seq:p.seq,prev:p.prev,history:p.history,by:this.fp});
    const pin=await this.groupRead(checks,"pins",row.inviter);
    const canNotify=pin&&!pin.pending&&pin.fingerprint===row.fp;
    const copy=accept||canNotify?await this.groupCarrierCopy(p.root,wire.SubGroupConsent,{v:1,seq:p.seq,hash:p.prev},wire.groupConsentJSON(consent),{address:row.inviter,fingerprint:row.fp},{group_lifecycle:id,group_direction:"in"},!accept):null;
    await this.store.write([{s:"kv",k,v:{...row,status,consent}}, ...(copy?[{s:"outbox",k:copy.id,v:copy}]:[])],checks);this.changed();if(copy)await this.post(copy);return {recorded:true};
  }

  async groupLifecycleGate(rec) {
    try {
      if(rec.required_cap===wire.CapGroupInvitationControl) { const pin=await this.pinned(rec.to);if(pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Group recipient key changed.");await this.requireGroupInvitationControl(rec.to,pin); }

      const row=await this.store.get("kv","group-invitation/"+rec.group_direction+"/"+rec.group_lifecycle);
      if(row?.status==="cancelled"&&rec.group_direction==="out"&&rec.group_cancel&&rec.sub===wire.SubGroupConsent) {
        if(row.inviter!==this.address||row.fp!==this.fp||row.owner!==this.me?.person)throw Error("Cancellation is not this device's intent.");
        const person=await this.store.get("persons",row.proposal.target),pin=await this.store.get("pins",rec.to);
        if(!person||person.state==="conflict"||!person.devices.some(d=>d.address===rec.to&&d.fingerprint===rec.recipient_fp)||!pin||pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Cancellation recipient key changed.");
        return {why:"",pin};
      }
      if(!row || !(rec.group_direction==="out"?["pending","accepted"].includes(row.status):["accepted","declined"].includes(row.status))) throw Error("Group invitation is no longer eligible.");
      if(rec.group_direction==="in"&&row.status==="declined"){
        const me=await this.store.get("kv","person"),pin=await this.store.get("pins",rec.to);
        if(!me||me.state==="conflict"||me.person!==row.proposal.target||!me.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp)||rec.to!==row.inviter||rec.recipient_fp!==row.fp||!pin||pin.pending||pin.fingerprint!==row.fp)throw Error("Decline signer or exact inviter key changed.");
        return {why:"",pin};
      }
      const {target}=await this.verifyGroupProposal(row.proposal,row.owner,row.inviter,row.fp);
      if(rec.group_direction==="out"?!target.devices.some(d=>d.address===rec.to&&d.fingerprint===rec.recipient_fp):rec.to!==row.inviter||rec.recipient_fp!==row.fp) throw Error("Group invitation recipient changed.");
      const pin=await this.store.get("pins",rec.to); if(!pin||pin.pending||pin.fingerprint!==rec.recipient_fp) throw Error("Group recipient key changed.");
      return {why:"",pin};
    } catch(e) {return {why:e.message};}
  }

  async publishGroupPacket(packet, invitation=null, initialChecks=[]) {
    for(let attempt=0;attempt<4;attempt++) {
      try {return await this.publishGroupPacketAttempt(packet,invitation,attempt?[]:initialChecks);}
      catch(e) {if(!(e instanceof StoreConflict)||attempt===3)throw e;}
    }
  }

  async publishGroupPacketAttempt(packet, invitation=null, initialChecks=[]) {
    const checks=[...initialChecks], conv=packet.state.conv, key="group/"+conv, before=await this.groupRead(checks,"kv",key);
    const g=before ? structuredClone(before) : {root:wire.rootJSON(packet.root),records:[],context:"",withdrawals:[],pending:[],rosters:{}};
    if(g.root!==wire.rootJSON(packet.root)) throw Error("Group root differs.");
    const resolved=await this.groupResolver(packet.root,[],packet,g,checks);
    const withdrawals=await this.groupContextWithdrawals(packet,g,resolved.resolve,resolved.people,checks);
    const old=g.context ? wire.parseGroupContext(g.context) : null;
    const visitorTargets=old?await this.groupVisitorTargets(conv,{...old,withdrawals:[...g.withdrawals,...g.pending].map(wire.parseGroupWithdrawal)},checks):[];
    await wire.verifyGroupState(packet.state,packet.root,old?.state || null,resolved.resolve,withdrawals);
    const me=await this.groupRead(checks,"kv","person");
    if(packet.state.actor!==me.person || packet.state.actor_roster!==me.hash || packet.state.by!==this.fp) throw Error("Current local administrator signature required.");
    let historyItems=[];
    if(invitation) {
      const intent=await this.groupRead(checks,"kv","group-invitation/out/"+invitation);
      if(intent?.status!=="accepted" || !intent.consent?.admission)throw Error("Exact accepted invitation missing.");
      const joined=wire.groupMember(packet.state,intent.proposal.target), target=await this.groupRead(checks,intent.proposal.target===this.me.person?"kv":"persons",intent.proposal.target===this.me.person?"person":intent.proposal.target);
      if(!target||target.state==="conflict"||target.hash!==intent.proposal.roster||packet.state.seq!==intent.proposal.seq||packet.state.prev!==intent.proposal.prev||!joined||wire.groupAdmissionJSON(joined.admission)!==wire.groupAdmissionJSON(intent.consent.admission))throw Error("Accepted invitation changed before publication.");
      historyItems=await this.groupSelectedItems(conv,intent.proposal.history || []);
    }
    // Keep exact original encrypted publication before requesting custody. A
    // response-loss retry reuses these bytes, never a re-encrypted slot.
    const pendingKey="group-publication/"+conv+"/"+packet.state.seq, pending=await this.groupRead(checks,"kv",pendingKey);
    let record;
    if(pending) {
      if(pending.context!==wire.groupContextJSON(packet)) throw Error("Another group publication is pending at this exact sequence.");
      record=wire.parseGroupCommit(pending.record);
    } else {
      const encryption=new Encrypter(), seen=new Set();
      const add=publicKey=>{if(!seen.has(publicKey.box_recipient)){seen.add(publicKey.box_recipient);encryption.addRecipient(publicKey.box_recipient);}};
      add(await wire.publicEntry(this.keys,this.address));
      for(const member of await wire.effectiveGroupMembers(packet.state,withdrawals)) {
        const p=resolved.people.get(member.person);
        for(const device of p.devices) {const pin=await this.pinned(device.address);if(pin.pending||pin.fingerprint!==device.fingerprint) throw Error("Exact group recipient key changed.");add(await this.pubOf(pin));}
      }
      const ciphertext=await encryption.encrypt(new TextEncoder().encode(wire.groupContextJSON(packet)));
      record=await wire.signGroupCommit(this.keys,{v:1,conv,realm:packet.state.realm,bootstrap:packet.root.creator.fingerprint,seq:packet.state.seq,prev:packet.state.prev,hash:await wire.groupStateHash(packet.state),admins:wire.groupAdmins(packet.state),writer:this.address,actor:me.person,actor_roster:me.hash,ciphertext});
      await this.store.write([{s:"kv",k:pendingKey,v:{type:"group-publication",conv,seq:packet.state.seq,record:wire.groupCommitJSON(record),context:wire.groupContextJSON(packet),invitation}}],checks);
    }
    const result=await this.call("POST","/v1/groups/"+conv+"/chain?creator="+record.bootstrap,JSON.parse(wire.groupCommitJSON(record)));
    if(result.seq!==record.seq || result.hash!==record.hash) throw Error("Relay returned inconsistent group custody.");
    // Reread authority dependencies after the await. Custody never licenses a
    // stale local install; the retained original publication recovers later.
    const finalChecks=[], current=await this.groupRead(finalChecks,"kv",key);
    if(JSON.stringify(current)!==JSON.stringify(before)) throw new StoreConflict();
    const publication=await this.groupRead(finalChecks,"kv",pendingKey);
    if(!publication || publication.record!==wire.groupCommitJSON(record))throw new StoreConflict();
    const latest=await this.groupRead(finalChecks,"kv","group-head/"+conv);
    if(latest && (latest.conflict || latest.seq>record.seq || latest.seq===record.seq&&latest.hash!==record.hash))throw new Hold("proof_pending","Group custody is behind the latest known head.");
    const finalResolved=await this.groupResolver(packet.root,[record],packet,g,finalChecks);
    const finalPins=await this.groupContextWithdrawals(packet,g,finalResolved.resolve,finalResolved.people,finalChecks);
    const records=g.records.map(wire.parseGroupCommit), previous=records.at(-1)||null;
    await wire.verifyGroupProofPage(packet.root,{records:[record],more:false},finalResolved.resolve,this.realm,{previous,known:seq=>records[seq]});
    if(record.seq>=records.length) records.push(record);
    await wire.verifyGroupCurrent(packet.state,packet.root,record,finalResolved.resolve,seq=>records[seq],finalPins);
    if(invitation) {
      const row=await this.groupRead(finalChecks,"kv","group-invitation/out/"+invitation);
      if(!row?.consent?.admission)throw Error("Exact accepted invitation missing.");
      historyItems=await this.groupSelectedItems(conv,row.proposal.history || [],finalChecks);
    }
    const roomEvents=await this.convEvents(conv,finalChecks),roomMembers=await this.dmMembers({id:conv,kind:"group",root:g.root},roomEvents,finalChecks,packet),deliveryPacket={...packet,memberships:await this.roomMemberships({id:conv},roomEvents,roomMembers)};
    g.records=records.map(wire.groupCommitJSON);g.context=wire.groupContextJSON(packet);g.rosters=finalResolved.raw;
    const copies=[];
    for(const member of await wire.effectiveGroupMembers(packet.state,finalPins)) {
      const p=finalResolved.people.get(member.person);
      for(const device of p.devices) {
        if(device.address===this.address) continue;
        await this.groupRead(finalChecks,"pins",device.address);
        for(const page of this.groupProofPages(records)) {const last=page.at(-1);copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupProof,{v:1,seq:last.seq,hash:last.hash},wire.groupJournalJSON({records:page,more:false}),device));}
        copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupContext,{v:1,seq:packet.state.seq,hash:record.hash},wire.groupContextJSON(deliveryPacket),device));
        if(invitation && member.admission.seq===packet.state.seq) {
          for(const item of historyItems)copies.push(await this.groupDataCopy(packet,"history",wire.historyJSON(item),device));
        }
      }
    }
    for(const target of visitorTargets) {
      await this.convEvents(conv,finalChecks,target.pid);await this.groupRead(finalChecks,"pins",target.device.address);
      for(const page of this.groupProofPages(records)){const last=page.at(-1);copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupProof,{v:1,seq:last.seq,hash:last.hash},wire.groupJournalJSON({records:page,more:false}),target.device,{pid:target.pid}));}
      copies.push(await this.groupCarrierCopy(packet.root,wire.SubGroupContext,{v:1,seq:packet.state.seq,hash:record.hash},wire.groupContextJSON(packet),target.device,{pid:target.pid}));
    }
    const ops=[{s:"kv",k:key,v:g},{s:"kv",k:pendingKey,v:undefined},...copies.map(v=>({s:"outbox",k:v.id,v}))];
    if(invitation) {const k="group-invitation/out/"+invitation,row=await this.groupRead(finalChecks,"kv",k);if(!row || row.status!=="accepted") throw Error("Exact accepted invitation missing.");ops.push({s:"kv",k,v:{...row,status:"published"}});}
    await this.store.write(ops,finalChecks);this.changed();this.syncInvitations().catch(()=>{});
    if (this.connected) this.notifyState().then(st => st.enabled ? this.syncNotify() : undefined).catch(() => {});
    for(const rec of copies) await this.post(rec);
    return packet;
  }

  async publishGroupInvitation(id) {
    const row=await this.store.get("kv","group-invitation/out/"+id);
    if(!row) throw Error("No local group invitation with that ID.");
    if(row.status==="published") return {published:true};
    if(row.status!=="accepted" || !row.consent?.admission) throw Error("Exact invitation has no accepted consent.");
    const pending=await this.store.get("kv","group-publication/"+row.proposal.state.conv+"/"+row.proposal.seq);
    if(pending) {await this.publishGroupPacket(wire.parseGroupContext(pending.context),id);return {published:true};}
    const checks=[];await this.verifyGroupProposal(row.proposal,row.owner,row.inviter,row.fp,checks);
    const current=await this.groupCurrent(row.proposal.state.conv), me=await this.groupRead(checks,"kv","person");
    if(row.inviter!==this.address || row.fp!==this.fp) throw Error("This is not this device's invitation.");
    const state=structuredClone(current.state);state.seq=row.proposal.seq;state.prev=row.proposal.prev;
    state.members=[...await wire.effectiveGroupMembers(current.state,current.withdrawals || []),{person:row.proposal.target,roster:row.proposal.roster,admin:false,admission:row.consent.admission}].sort((a,b)=>a.person.localeCompare(b.person));
    if(new Set(state.members.map(m=>m.person)).size!==state.members.length) throw Error("Invited person is already a current member.");
    state.actor=me.person;state.actor_roster=me.hash;state.by=this.fp;
    await this.publishGroupPacket({root:current.root,proof:null,state:await wire.signGroupState(this.keys,state),withdrawals:current.withdrawals || null},id,checks);
    return {published:true};
  }

  async recoverGroupIntents() {
    const pending=(await this.store.all("kv")).filter(row=>row?.type==="group-publication" || row?.type==="group-invitation"&&row.direction==="out"&&row.status==="accepted");
    for(const saved of pending) {
      const key=saved.type==="group-publication"?"group-publication/"+saved.conv+"/"+saved.seq:"group-invitation/out/"+saved.id;
      const row=await this.store.get("kv",key);if(!row)continue;
      try {
        if(row.type==="group-publication") {
          if(row.invitation && (await this.store.get("kv","group-invitation/out/"+row.invitation))?.status==="published")continue;
          await this.publishGroupPacket(wire.parseGroupContext(row.context),row.invitation);
        } else if(row.status==="accepted")await this.publishGroupInvitation(row.id);
      } catch(e) { /* retained exact intent; common publisher alone retries conditional conflicts */ }
    }
    this.syncInvitations().catch(()=>{});
  }

  async groupTurnEvidence(conv, checks=[], captured=false) {
    const g=await this.groupRead(checks,"kv","group/"+conv), head=await this.groupRead(checks,"kv","group-head/"+conv);
    const packet=await (captured?this.groupCurrentState(conv):this.groupCurrent(conv));
    if(!g?.context || g.context!==wire.groupContextJSON({...packet,withdrawals:wire.parseGroupContext(g.context).withdrawals})) throw new StoreConflict();
    const members=await this.groupPeople(packet,checks), identity=await this.groupRead(checks,"kv","identity");
    if(!identity || identity.revoked || identity.address!==this.address || identity.fingerprint!==this.fp) throw Error("Local group identity changed.");
    if(head && (head.conflict || head.seq>packet.state.seq || head.seq===packet.state.seq && head.hash!==await wire.groupStateHash(packet.state))) throw new Hold("proof_pending","Group current state is behind the latest head.");
    return {packet,members,g};
  }

  async groupReply(conv, ref, required=true) { return this.logicalReply(conv, ref, required, "Group"); }

  // logicalReply names a reply's parent by its logical id: every device of a
  // multi-device audience keeps the LID, never the replier's own copy id.
  async logicalReply(conv, ref, required=true, what="Group") {
    if(!ref) return "";
    const rows=[...(await this.store.all("inbox")),...(await this.store.all("outbox"))].filter(m=>!m.control&&!m.aside&&(m.id===ref||m.lid===ref));
    const lids=new Set(rows.map(m=>m.lid));
    if(rows.some(m=>m.conv!==conv)||lids.size>1) throw Error(what+" reply names an ambiguous parent or another conversation.");
    if(!rows.length) {if(!required&&wire.validID(ref))return ref;throw Error("That "+what.toLowerCase()+" parent is not kept here.");}
    return rows[0].lid || rows[0].id;
  }

  async sendGroupTurn(c,{id:sendID="",queued=false,body,topic="",topic_event=null,quote="",reply_to="",files=[],receiver=null}) {
    checkFiles(files);
    const consentChecks=[], consentEvents=await this.convEvents(c.id,consentChecks), consentMembers=await this.dmMembers(c,consentEvents,consentChecks), human=await this.roomPlan(c,consentEvents,consentMembers);
    if(human)return this.sendHumanTurn(c,{id:sendID,queued,kind:"message",body,topic,topic_event,quote,reply_to,files,receiver},human);
    const checks=consentChecks, {packet,members}=await this.groupTurnEvidence(c.id,checks), me=await this.groupRead(checks,"kv","person"), own=wire.groupMember(packet.state,me.person), reply=await this.groupReply(c.id,reply_to);
    quote=await this.groupReply(c.id,quote);
    const plain=[];for(const file of files)plain.push({name:wire.safeName(file.name),bytes:file.bytes instanceof Uint8Array?file.bytes:new Uint8Array(await file.arrayBuffer())});
    const copies=[], lid=this.localSendID(sendID), firstID=wire.newID(), at=this.now(), stamp=await wire.groupAdmissionHash(own.admission);
    const replyKeys=[];
    if(receiver?.host) for(const member of members) if(member.person!==me.person) for(const d of member.devices || []) replyKeys.push({key:d.fingerprint,admission:await wire.groupAdmissionHash(wire.groupMember(packet.state,member.person).admission)});
    replyKeys.sort((a,b)=>a.key.localeCompare(b.key));
    const prepared=await this.prepareReceiverRequest(receiver,{id:firstID,lid,conv:c.id,root:wire.rootJSON(packet.root),ts:Math.floor(at/1000),kind:"message",body,quote,reply_to:reply,origin:"ui",group_admission:stamp,group_replies:replyKeys},plain,checks);
    for(const member of members)for(const device of member.devices || []) {
      if(device.address===this.address)continue;
      const pin=await this.pinned(device.address);await this.groupRead(checks,"pins",device.address);
      if(pin.pending||pin.fingerprint!==device.fingerprint)throw Error("Exact group recipient key changed.");
      const notify = await this.groupSupport(device.address,pin);
      if(prepared)await this.receiverSupport(device.address,pin);
      const publicKey=await this.pubOf(pin), sealed=[];for(const f of plain)sealed.push({...await wire.encryptFile(f.bytes,f.name,publicKey),uploaded:false});
      const id=copies.length?wire.newID():firstID, fan=[{person:me.person,roster:me.hash},...(member.person===me.person?[]:[{person:member.person,roster:(await this.store.get("persons",member.person)).hash}])], replica=member.person===me.person;
      const chan = notify && !replica && !topic_event ? await wire.notifyChannel(c.id, device.fingerprint) : "";
      const envelope=await wire.seal({v:wire.Version2,id,from:this.address,to:device.address,ts:Math.floor(at/1000),kind:"message",body,topic,topic_event,quote,reply_to:reply,conv:c.id,lid,root:wire.rootJSON(packet.root),origin:"ui",chan,replica,fan,attachments:sealed.map(f=>f.attachment),receiver_route:prepared?.route},this.keys,publicKey);
      copies.push({id,lid,conv:c.id,kind:"message",body,topic,topic_event,quote,reply_to:reply,origin:"ui",from:this.address,to:device.address,person:member.person,own:replica,replica,at,envelope,attachments:sealed.map(f=>f.attachment),files:sealed.length?sealed:undefined,state:"queued",required_cap:wire.CapGroup,recipient_fp:device.fingerprint,group_admission:stamp,...(prepared?{receiver_route:prepared.route}:{})});
    }
    if(!copies.length)throw Error("Group has no other current device to receive a copy.");
    await this.commitReceiverCopies(copies,prepared,checks,await this.roomStoredOps(c.id,checks,{lid},this.fp,null,null,packet),queued);this.changed();await this.keepSent(plain);
    if(queued)this.queueOutbox();else if(prepared)await this.post(prepared.delegation);else for(const rec of copies)await this.post(rec);
    const least=copyOrder(copies);return {id:copies[0].id,lid,state:least.state,detail:least.detail};
  }

  async admitGroupTurn(n,env,pin,base) {
    const room=!!n.human&&!n.sub&&!!n.pid&&n.pid===n.human.author_pid; // a person guest's turn (client roomGroupTurn): its PID is its author's
    if(n.pid&&!room) {
      if(n.human) {
        if(n.human.proof.some(e=>e.role==="human"))throw new Hold("invalid","Group human guest execution audience is not enabled.");
        const c=await this.groupRecord(n.conv);const sender=await this.personOf(env.from,pin);
        return this.admitHumanTurn(n,env,pin,base,wire.parseGroupRoot(n.root),sender,c);
      } // the group participation path does not read it (client admitConv; ROOM_V1 P3/P4)
      return this.admitGroupParticipation(n,env,pin,base);
    }
    const root=wire.parseGroupRoot(n.root), checks=[], {packet,members}=await this.groupTurnEvidence(n.conv,checks,!!n.human);
    const currentPin=await this.groupRead(checks,"pins",env.from);
    if(!currentPin || currentPin.pending || currentPin.fingerprint!==pin.fingerprint)throw new StoreConflict();
    if(wire.rootJSON(root)!==wire.rootJSON(packet.root))throw new Hold("invalid","Group root differs from current verified context.");
    if(n.sub==="history")return this.admitGroupHistory(n,env,pin,checks,packet,members);
    if(n.sub==="file")return this.admitGroupFile(n,env,pin,checks,packet,members);
    if(n.kind!=="message"||n.sub||n.target||n.pid&&!room||n.agent_id||n.status||n.ref||n.origin&&n.origin!=="ui")throw new Hold("invalid","This is not an ordinary human group turn.");
    let sender=members.find(m=>m.devices?.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint));
    if(n.human) { // a captured audience (ROOM_V1 §3): its author a current member device or the exact host of a following participation
      const evidence=await this.humanEvidence(await this.groupRecord(n.conv),n.human,checks);
      this.humanTurnAuthorization(n,evidence,null,env.from,pin.fingerprint,this.address,this.fp);
      if(!sender)sender=await this.personOf(env.from,pin);
    }
    if(!sender)throw new Hold("invalid","Sender is not a current group device.");
    const own=sender.person===this.me.person;
    if(n.replica!==own)throw new Hold("invalid","Group replica differs from sender's own person.");
    for(const fan of n.fan || []) {
      if(![sender.person,this.me.person].includes(fan.person))throw new Hold("invalid","Group fan names a different person.");
      const p=fan.person===this.me.person?this.me:await this.store.get("persons",fan.person);
      if(!p?.hashes.includes(fan.roster))throw new Hold("proof_pending","Group fan roster is not verified here.");
    }
    await this.groupReply(n.conv,n.reply_to,false);
    const key=pin.fingerprint+"/"+n.lid, hash=await wire.groupHistoryContentHash(n.conv,n), seen=await this.groupRead(checks,"lids",key);
    if(seen&&!seen.history) {if(seen.hash!==hash)throw new Hold("conflicting_duplicate","Conflicting group logical turn.");const ops=[];ops.checks=checks;return ops;}
    const ops=[];if(seen?.history)ops.push({s:"inbox",k:seen.id,v:undefined});
    const currentMember=wire.groupMember(packet.state,this.me.person),stamp=currentMember?await wire.groupAdmissionHash(currentMember.admission):"";
    ops.push({s:"inbox",k:env.id,v:{...base,v:wire.Version2,conv:n.conv,lid:n.lid,sub:"",origin:n.origin,fp:pin.fingerprint,own,replica:n.replica,state:"",group_admission:stamp,...(n.human?{pid:n.pid,human:n.human}:{})}},{s:"lids",k:key,v:{id:env.id,conv:n.conv,hash}});
    ops.push(...await this.roomStoredOps(n.conv,checks,n,pin.fingerprint,null,null,packet));
    ops.checks=checks;return ops;
  }

  async groupVisitorInvite(root,pid,checks=[],address=this.address,fp=this.fp) {
    const events=await this.convEvents(await wire.rootID(root),checks,pid), found=[];
    for(const x of events) {
      const e=x.e;if(e.type!=="invite"||e.group?.host_role!=="visitor"||e.host?.address!==address||e.host?.fingerprint!==fp)continue;
      const p=await this.groupRead(checks,e.author.person===this.me.person?"kv":"persons",e.author.person===this.me.person?"person":e.author.person);
      if(!p||p.state==="conflict"||!p.hashes.includes(e.author.roster))throw new Hold("proof_pending","Visitor inviter proof is missing.");
      const g=await this.groupRead(checks,"kv","group/"+e.conv);
      const resolved=await this.groupResolver(root,[],null,g||{rosters:{}},checks,[e.author]);
      const roster=await resolved.resolve(e.author.person,e.author.roster), device=roster.devices.find(d=>d.address===e.author.address);
      if(!device||await wire.fingerprint(device)!==e.author.fingerprint)throw new Hold("invalid","Visitor inviter original device differs.");
      try{await wire.verifyEvent(e,device.sign_key);}catch(err){throw new Hold("invalid",err.message);}
      found.push(x);
    }
    if(found.length!==1)throw new Hold(found.length?"invalid":"proof_pending","Visitor needs one exact signed invitation.");
    const h=found[0].e.host, host=await this.groupRead(checks,h.person===this.me.person?"kv":"persons",h.person===this.me.person?"person":h.person),hostPin=await this.groupRead(checks,"pins",address);
    if(!host?.devices.some(d=>d.address===address&&d.fingerprint===fp)||host.state==="conflict"||hostPin?.pending||address!==this.address&&hostPin?.fingerprint!==fp)throw new Hold("invalid","Visitor exact pinned host changed.");
    return found[0];
  }

  async admitGroupParticipation(n,env,pin,base) {
    const root=wire.parseGroupRoot(n.root), checks=[],c={id:n.conv,root:n.root,kind:"group"};
    const senderPin=await this.groupRead(checks,"pins",env.from);
    if(senderPin?.pending||senderPin?.fingerprint!==pin.fingerprint)throw new StoreConflict();
    const identity=await this.groupRead(checks,"kv","identity");
    if(!identity||identity.revoked||identity.address!==this.address||identity.fingerprint!==this.fp||root.realm!==this.realm)throw new Hold("invalid","Group PID exact identity/realm differs.");
    let record=null;
    if(n.sub==="event") {
      try{record=await this.eventRecord(n.body);const e=record.e;if(n.kind!=="message"||e.conv!==n.conv||e.pid!==n.pid)throw Error("Group event scope differs.");
        let authorPin=pin;
        if(e.author.address!==env.from||e.author.fingerprint!==pin.fingerprint) {
          if(!["scope","accept","dismiss"].includes(e.type))throw Error("Group event sender differs.");
          authorPin=await this.sendKey(e.author.address);
          const author=await this.personOf(e.author.address,authorPin);
          if(authorPin.fingerprint!==e.author.fingerprint||author.person!==e.author.person||!author.hashes.includes(e.author.roster))throw Error("Forwarded group end author proof differs.");
        }
        await wire.verifyEvent(e,(await this.pubOf(authorPin)).sign_key);}
      catch(e){throw new Hold("invalid",e.message);}
      if(record.e.host) {const h=record.e.host,hp=await this.sendKey(h.address);if(hp.fingerprint!==h.fingerprint||(await this.personOf(h.address,hp)).person!==h.person)throw new Hold("invalid","Group invitation host differs from pinned proof.");}
    }
    const g=await this.groupRead(checks,"kv","group/"+n.conv);
    if(g&&g.root!==n.root)throw new Hold("invalid","Group PID original root differs.");
    // Outside invitation is pending evidence only. Current original proof and
    // context must arrive before any request, excerpt, or acceptance counts.
    if(!g?.context && record?.e.type==="invite" && record.e.group?.host_role==="visitor" && record.e.host.person===this.me?.person && record.e.host.address===this.address && record.e.host.fingerprint===this.fp) {
      const author=await this.personOf(env.from,pin), e=record.e;
      if(author.person!==e.author.person||!author.hashes.includes(e.author.roster))throw new Hold("invalid","Visitor inviter person differs.");
      const next=g?structuredClone(g):{root:n.root,records:[],context:"",withdrawals:[],pending:[],rosters:{}};
      const resolved=await this.groupResolver(root,[],null,next,checks,[e.author]);
      await wire.verifyGroupProofPage(root,{records:[],more:false},resolved.resolve,this.realm,{previous:null});next.rosters=resolved.raw;
      await this.convEvents(n.conv,checks,n.pid);
      const key=pin.fingerprint+"/"+n.lid,hash=await wire.groupHistoryContentHash(n.conv,n),seen=await this.groupRead(checks,"lids",key);
      if(seen&&seen.hash!==hash)throw new Hold("conflicting_duplicate","Visitor invitation logical conflict.");
      const ops=seen?[]:[{s:"kv",k:"group/"+n.conv,v:next},{s:"inbox",k:env.id,v:{...base,v:2,conv:n.conv,lid:n.lid,pid:n.pid,sub:n.sub,replica:false,own:false,target:null,read:true}},{s:"lids",k:key,v:{id:env.id,conv:n.conv,hash}}];ops.checks=checks;ops.groupCarrier=true;return ops;
    }
    if(!g?.context)throw new Hold("proof_pending","Group PID current context is missing.");
    const events=await this.convEvents(n.conv,checks,n.pid);if(record)events.push(record);
    const members=await this.dmMembers(c,events,checks),info=this.resolveAgent(n.pid,events,members);
    if(!info.invite)throw new Hold("proof_pending","Group PID invitation does not count yet.");
    const recipientMember=members.has(this.me?.person);
    const disclosed=!!record&&!recipientMember&&["scope","accept","dismiss"].includes(record.e.type)&&info.host?.address!==this.address&&[...members.values()].some(p=>p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint));
    if(disclosed)await this.admitDisclosed(record,info,members,env.from,pin.fingerprint,c,checks);
    else {
      if(!recipientMember && (!info.external||info.host?.person!==this.me.person||info.host.address!==this.address||info.host.fingerprint!==this.fp))throw new Hold("invalid","Group visitor recipient differs from exact invited host.");
      this.externalRole(n,info,members,env.from,pin.fingerprint);
    }
    if(record?.e.type==="invite"&&record.hash!==info.invite)throw new Hold("invalid","Group invite differs from counted original.");
    await this.checkExternalReply(n,info,members,checks);
    const sender=members.get(this.me.person)?.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint), own=!!sender;
    for(const f of n.fan||[]) {const p=members.get(f.person)||members.hosts.get(f.person);if(!p?.hashes.includes(f.roster)||![this.me.person,info.host.person,...[...members.values()].filter(p=>p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint)).map(p=>p.person)].includes(f.person))throw new Hold("invalid","Group PID fan differs from exact sender/recipient.");}
    const hash=await wire.groupHistoryContentHash(n.conv,n),key=pin.fingerprint+"/"+n.lid,seen=await this.groupRead(checks,"lids",key);
    if(seen&&!seen.history){if(seen.hash!==hash)throw new Hold("conflicting_duplicate","Group PID logical content differs.");const ops=[];ops.checks=checks;return ops;}
    let attachments=n.attachments;
    if(n.sub==="excerpt") {const h=wire.parseGrantedExcerpt(n,info),used=new Set();attachments=h.attachments.map(a=>{const i=n.attachments.findIndex((f,i)=>!used.has(i)&&f.name===a.name&&f.size===a.size&&f.sha256===a.sha256);if(i>=0){used.add(i);return n.attachments[i];}return {...a,blob:null,availability:"unavailable",detail:"Selected bytes were not available at the forwarder."};});}
    const rec={...base,v:2,conv:n.conv,lid:n.lid,pid:n.pid,sub:n.sub,origin:n.origin,target:n.target||null,...(n.agent_id?{agent_id:n.agent_id}:{}),replica:n.replica,own,read:own||!!n.sub||base.read,attachments,group_admission:members.epochs.get(this.fp)||"",state:!own&&!n.sub&&["question","task"].includes(n.kind)&&n.target?.address===this.address?"conv_held":""};
    const ops=[...(seen?.history?[{s:"inbox",k:seen.id}]:[]),{s:"inbox",k:env.id,v:rec},{s:"lids",k:key,v:{id:env.id,conv:n.conv,hash}},...await this.roomStoredOps(n.conv,checks,n,pin.fingerprint,events,members)];ops.checks=checks;ops.groupCarrier=true;return ops;
  }

  async selectGroupHistory(conv, selection={}) {
    await this.groupCurrent(conv);
    if(Object.keys(selection).some(k=>!["last","since","refs"].includes(k)))throw Error("Unknown group history selection.");
    const modes=[!!selection.last,!!selection.since,selection.refs!=null].filter(Boolean).length;
    if(modes>1 || selection.last!=null&&(!Number.isInteger(selection.last)||selection.last<0||selection.last>64) || selection.since!=null&&(!Number.isSafeInteger(selection.since)||selection.since<0))throw Error("Choose one history mode, up to64 messages.");
    if(!modes)return [];
    const msgs=(await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox"))).filter(m=>m.kind==="message"&&!m.sub&&!m.pid&&!m.control&&!m.aside&&!m.deleted); // as the core's sources: no PID turn (a room guest's), D2
    const refs=[];
    for(const m of msgs)refs.push({lid:m.lid,author:m.claimed_key||m.fp||this.fp,hash:await wire.groupHistoryContentHash(conv,m)});
    if(selection.refs!=null) {
      if(!Array.isArray(selection.refs)||selection.refs.length>64)throw Error("Selected history exceeds64.");
      const identities=new Set();for(const r of selection.refs) {const key=r.author+"/"+r.lid;if(identities.has(key)||!refs.some(x=>x.lid===r.lid&&x.author===r.author&&x.hash===r.hash))throw Error("Selected history changed or is unavailable.");identities.add(key);}return selection.refs;
    }
    const chosen=selection.last?refs.slice(-selection.last):refs.filter((_,i)=>msgs[i].at>=selection.since);
    if(chosen.length>64)throw Error("Selected history exceeds64; choose fewer messages.");return chosen;
  }

  async groupSelectedItems(conv, refs, checks=[]) {
    const messages=(await this.convMessages(conv,await this.store.all("inbox"),await this.store.all("outbox"))), items=[];
    for(const ref of refs) {
      const matches=[];for(const m of messages)if(m.lid===ref.lid&&(m.claimed_key||m.fp||this.fp)===ref.author&&await wire.groupHistoryContentHash(conv,m)===ref.hash)matches.push(m);
      if(matches.length!==1)throw Error("Selected group history changed or is unavailable.");
      const m=matches[0];if(m.kind!=="message"||m.sub||m.deleted||m.target||m.pid||m.agent_id||m.status)throw Error("Selected history is not an ordinary human group turn.");
      const inbox=await this.groupRead(checks,"inbox",m.id);
      const source=inbox || await this.groupRead(checks,"outbox",m.id);
      if(!source || source.deleted || await wire.groupHistoryContentHash(conv,source)!==ref.hash || (source.claimed_key||source.fp||this.fp)!==ref.author)throw Error("Selected history source changed before publication.");
      items.push({v:1,from:m.from||this.address,from_key:ref.author,id:m.id,lid:m.lid,ts:Math.floor(m.at/1000),at:m.at,kind:m.kind,body:m.body,reply_to:m.reply_to||"",origin:m.origin||"",attachments:(m.attachments||[]).map(a=>({name:a.name,size:a.size,sha256:a.sha256}))});
    }
    return items;
  }

  async groupFileSource(conv, message, checks=[]) {
    const candidates=await this.authorityRows({conv,lid:message.lid},checks), rows=(await this.convMessages(conv,candidates.filter(r=>!r.to),candidates.filter(r=>r.to)));
    const matching=rows.filter(m=>m.lid===message.lid&&(m.claimed_key||m.fp||this.fp)===message.author);
    if(!matching.some(m=>m.kind==="message"&&!m.sub&&!m.pid&&!m.target&&!m.agent_id)) {
      const {packet}=await this.groupTurnEvidence(conv,checks),members=await this.dmMembers(await this.groupRecord(conv),null,checks),stamp=members.epochs.get(this.fp);
      if(!matching.length)throw Error("Original group PID file source is unavailable.");
      const verified=[];
      for(const source of matching) {
        if(!source.pid||source.sub||source.ref||!["question","task","answer","result"].includes(source.kind)||await wire.groupHistoryContentHash(conv,source)!==message.hash)throw Error("Conflicting exact group PID file source.");
        const stored=await this.groupRead(checks,source.to?"outbox":"inbox",source.id);
        let item={...this.itemOf(source,!!source.to),...(stored?.group_history?{group_history:stored.group_history}:{}),group_admission:source.to?stamp:source.group_admission};
        if(item.group_admission!==stamp)throw Error("Original group PID file admission changed.");
        if(source.to) {const env=wire.parseEnvelope(source.envelope),publicKey=await wire.publicEntry(this.keys,this.address);await wire.verifyEnvelope(env,publicKey.sign_key);if(env.id!==source.id||env.from!==this.address||env.kind!==source.kind)throw Error("Group PID file lacks its exact signed original source.");}
        item=await this.groupParticipationHistorySource(await this.groupRecord(conv),item,checks);
        const file=item.attachments[message.index];if(!file||file.name!==message.name||file.size!==message.size||file.sha256!==message.sha256)throw Error("Exact group PID file descriptor differs.");
        verified.push({...source,group_history:item.group_history});
      }
      const source=verified.sort((a,b)=>b.at-a.at||(a.id<b.id?1:-1))[0];return {...source,source_dir:source.to?"out":"in",group_admission:stamp};
    }
    const items=await this.groupSelectedItems(conv,[{lid:message.lid,author:message.author,hash:message.hash}],checks), item=items[0], file=item.attachments[message.index];
    if(!file || file.name!==message.name || file.size!==message.size || file.sha256!==message.sha256)throw Error("Selected group file descriptor differs.");
    const source=rows.find(m=>m.lid===message.lid&&(m.claimed_key||m.fp||this.fp)===message.author);return {...source,source_dir:source.to?"out":"in"};
  }

  async groupFileAuthorized(packet,members,address,fp,message,checks=[]) {
    const person=members.find(p=>p.devices?.some(d=>d.address===address&&d.fingerprint===fp));
    if(!person)throw Error("Group file requester is no longer a current device.");
    const member=wire.groupMember(packet.state,person.person), stamp=await wire.groupAdmissionHash(member.admission);
    if(!message.group_admission || message.group_admission!==stamp)throw Error("Group file requester admission changed.");
    const exactSource=await this.groupFileSource(packet.state.conv,message,checks);
    if(exactSource.pid) {if(person.person!==this.me.person||exactSource.group_admission!==stamp)throw Error("Group PID files require exact current own-linked history.");if(exactSource.group_history&&!await this.ownHistoryAuthority({address,fingerprint:fp},checks))throw Error("Historical witness file requires current own human devices and unchanged pins.");return;}
    if(!wire.groupAllowsHistory(member.admission,{lid:message.lid,author:message.author,hash:message.hash})) {
      if(person.person!==this.me.person)throw Error("Group file lacks its exact selected history grant.");
      const source=await this.groupFileSource(packet.state.conv,message,checks);
      if(source.group_admission!==message.group_admission)throw Error("Group file is not from this exact live admission.");
    }
  }

  async requestGroupFile(row,index) {
    const checks=[],{packet,members}=await this.groupTurnEvidence(row.conv,checks), item=await this.groupRead(checks,"inbox",row.id), attachment=item.attachments[index];
    if(!attachment)throw Error("No selected file at this index.");if(attachment.blob)return {note:"It is here already."};
    const member=wire.groupMember(packet.state,this.me.person), message={v:1,type:"request",lid:item.lid,sha256:attachment.sha256,author:item.claimed_key,hash:await wire.groupHistoryContentHash(item.conv,item),index,name:attachment.name,size:attachment.size,group_admission:await wire.groupAdmissionHash(member.admission)};
    await this.groupFileAuthorized(packet,members,this.address,this.fp,message,checks);await this.groupFileSource(item.conv,message,checks);
    const device=members.flatMap(m=>m.devices||[]).find(d=>d.address===item.synced_from);if(!device)throw Error("History forwarder is no longer a current group device.");
    const pendingKey="group-file-request/"+item.id+"/"+attachment.sha256, prior=await this.groupRead(checks,"kv",pendingKey);
    if(prior && wire.groupFileMsgJSON(prior.message)!==wire.groupFileMsgJSON(message))throw Error("Another exact selected file index is pending.");
    if(prior)return {note:"This exact file is already requested."};
    const rec=await this.groupDataCopy(packet,"file",wire.groupFileMsgJSON(message),device);
    const requested={...item,attachments:[...item.attachments]};
    requested.attachments[index]={...attachment,availability:"requested",detail:""};
    await this.store.write([{s:"inbox",k:item.id,v:requested},{s:"kv",k:pendingKey,v:{message,via:device.address}},{s:"outbox",k:rec.id,v:rec}],checks);this.changed();if(this.connected)await this.post(rec);return {note:"Requested from its group history forwarder; it must be online."};
  }

  async admitGroupFile(n,env,pin,checks,packet,members) {
    let message;try{message=wire.parseGroupFileMsg(n.body);}catch(e){throw new Hold("invalid",e.message);}
    if(!n.replica || !members.some(m=>m.devices?.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint)))throw new Hold("invalid","Group file sender is not a current device.");
    try{await this.groupFileAuthorized(packet,members,message.type==="request"?env.from:this.address,message.type==="request"?pin.fingerprint:this.fp,message,checks);}catch(e){throw new Hold("invalid",e.message);}
    const source=await this.groupFileSource(n.conv,message,checks), ops=[{s:"kv",k:"group-carrier/"+env.id,v:true}];
    if(message.type==="request") {
      if(n.attachments.length)throw new Hold("invalid","Group file request carries unexpected bytes.");
      const k="serve/"+env.id,old=await this.groupRead(checks,"kv",k);if(!old)ops.push({s:"kv",k,v:{serve:true,group:true,id:env.id,device:env.from,fp:pin.fingerprint,conv:n.conv,lid:message.lid,sha256:message.sha256,message,state:"pending",at:this.now()}});
    }else{
      const k="group-file-request/"+source.id+"/"+message.sha256, pending=await this.groupRead(checks,"kv",k);
      if(pending) {
        const exact={...message,type:"request",available:false,detail:""};
        if(pending.via!==env.from || wire.groupFileMsgJSON(exact)!==wire.groupFileMsgJSON(pending.message) || source.synced_from!==env.from)throw new Hold("invalid","Group file offer differs from its exact requested descriptor/forwarder.");
        const stored=await this.groupRead(checks,"inbox",source.id), row={...stored,attachments:[...stored.attachments]}, file=row.attachments[message.index];
        if(message.available) {
          const att=n.attachments[0];if(n.attachments.length!==1||att.name!==file.name||att.size!==file.size||att.sha256!==file.sha256)throw new Hold("invalid","Group file offer manifest differs.");row.attachments[message.index]={...file,blob:att.blob};
        }else{if(n.attachments.length)throw new Hold("invalid","Unavailable group file carries bytes.");row.attachments[message.index]={...file,availability:"unavailable",detail:message.detail};}
        ops.push({s:"inbox",k:row.id,v:row},{s:"kv",k,v:undefined});
      }
    }
    ops.checks=checks;ops.groupCarrier=true;return ops;
  }

  async serveGroupFile(job) {
    const {packet,members}=await this.groupTurnEvidence(job.conv);await this.groupFileAuthorized(packet,members,job.device,job.fp,job.message);
    const source=await this.groupFileSource(job.conv,job.message), attachment=source.attachments[job.message.index];
    const received=source.source_dir==="in"?await this.store.get("inbox",source.id):null;
    const exact=row=>row&&row.conv===job.conv&&row.lid===job.message.lid&&(row.claimed_key||row.fp||this.fp)===job.message.author;
    let bytes;
    if(received) {
      if(!exact(received)||await wire.groupHistoryContentHash(job.conv,received)!==job.message.hash)throw Error("Selected received group file source changed.");
      const file=received.attachments[job.message.index];if(!file?.blob)throw Error("This device holds no received copy of the selected file.");
      bytes=await wire.decryptFile(await this.cipherOf(file),file,this.keys);
    } else {
      const sent=await this.store.get("outbox",source.id);
      if(!exact(sent)||wire.parseEnvelope(sent.envelope).from!==this.address||job.message.author!==this.fp||await wire.groupHistoryContentHash(job.conv,sent)!==job.message.hash)throw Error("Selected sent group file source changed.");
      const kept=await this.store.get("files","kept/"+attachment.sha256);if(!kept)throw Error("This device kept no copy of the selected file.");bytes=await wire.decryptFile(kept.ct,kept.attachment,this.keys);
    }
    if(bytes.length!==job.message.size||wire.hex(await wire.sha256(bytes))!==job.message.sha256)throw Error("Kept group file differs from its exact requested descriptor.");
    const device=members.flatMap(m=>m.devices||[]).find(d=>d.address===job.device&&d.fingerprint===job.fp), pin=await this.pinned(device.address);
    if(pin.pending||pin.fingerprint!==job.fp)throw Error("Group file requester key changed.");
    await this.offerGroupFile(job,[await wire.encryptFile(bytes,job.message.name,await this.pubOf(pin))],"");
  }

  async offerGroupFile(job,files,detail) {
    const checks=[],{packet,members}=await this.groupTurnEvidence(job.conv,checks);await this.groupFileAuthorized(packet,members,job.device,job.fp,job.message,checks);await this.groupFileSource(job.conv,job.message,checks);
    const device=members.flatMap(m=>m.devices||[]).find(d=>d.address===job.device&&d.fingerprint===job.fp);if(!device)throw Error("Group file requester changed.");
    const message={...job.message,type:"offer",available:files.length>0,detail}, rec=await this.groupDataCopy(packet,"file",wire.groupFileMsgJSON(message),device,files);
    await this.store.write([{s:"outbox",k:rec.id,v:rec}],checks);this.changed();await this.post(rec);
  }

  async groupDataCopy(packet, sub, body, device, sealed=[]) {
    const pin=await this.pinned(device.address);if(pin.pending||pin.fingerprint!==device.fingerprint)throw Error("Group data recipient key changed.");
    await this.groupSupport(device.address,pin);
    const id=wire.newID(),lid=wire.newID(),key=await this.pubOf(pin);
    const envelope=await wire.seal({v:wire.Version2,id,lid,conv:packet.state.conv,root:wire.rootJSON(packet.root),from:this.address,to:device.address,ts:Math.floor(this.now()/1000),kind:"message",sub,body,replica:true,attachments:sealed.map(f=>f.attachment)},this.keys,key);
    const person=(await this.groupPeople(packet)).find(m=>m.devices?.some(d=>d.address===device.address&&d.fingerprint===device.fingerprint));
    if(!person)throw Error("Group data recipient is not current.");
    const recipient_admission=await wire.groupAdmissionHash(wire.groupMember(packet.state,person.person).admission);
    return {id,lid,conv:packet.state.conv,to:device.address,recipient_fp:device.fingerprint,recipient_admission,required_cap:wire.CapGroup,kind:"message",sub,body,aside:true,replica:true,at:this.now(),envelope,state:"queued",files:sealed.length?sealed.map(f=>({...f,uploaded:false})):undefined};
  }

  async groupHistoryScope(c,h,checks) {
    if(h.sub!==wire.SubStatus)return h;
    if(!h.ref)throw new Hold("invalid","Historical status lacks exact request.");
    const rows=(await this.authorityRows({conv:c.id,lid:h.ref.id},checks)).filter(r=>!r.sub&&!r.control&&!r.aside&&(r.fp||this.fp)===h.ref.fingerprint&&["question","task"].includes(r.kind));
    if(!rows.length)throw new Hold("proof_pending","Historical status request is missing.");
    const first=rows[0];if(!first.pid||rows.some(r=>r.pid!==first.pid||JSON.stringify(r.human)!==JSON.stringify(first.human)))throw new Hold("invalid","Historical status request scope conflicts.");
    return {...h,pid:first.pid,human:first.human};
  }

  // Inert, item-bound past authority. Only already verified public records and
  // pinned roster steps count; this never installs a group or person state.
  async groupHistoryWitness(c,h,checks=[]) {
    h=await this.groupHistoryScope(c,h,checks);
    const p=h.group_history,g=await this.groupRead(checks,"kv","group/"+c.id);
    if(!p||!h.pid||wire.rootJSON(p.root)!==c.root||p.proof?.length||!p.memberships?.length||p.memberships.length>8*(wire.MaxHumanAudience+1))throw new Hold("invalid","Malformed historical group witness.");
    wire.parseGroupContext(wire.groupContextJSON(p));
    const records=(g?.records||[]).map(wire.parseGroupCommit),authority=records[p.state.seq];
    if(!authority)throw new Hold("proof_pending","Original historical authority is missing.");
    const refs=[p.root.creator,{person:p.state.actor,roster:p.state.actor_roster},...p.state.members,...p.state.members.map(m=>m.admission),...(p.withdrawals||[])],rosters=new Map();
    for(const ref of refs){
      const person=await this.groupRead(checks,ref.person===this.me.person?"kv":"persons",ref.person===this.me.person?"person":ref.person),raw=g.rosters?.[ref.roster];
      if(!person||!["self","pinned"].includes(person.state)||!person.hashes.includes(ref.roster)||!raw)throw new Hold("proof_pending","Historical roster is not pinned here.");
      const roster=await wire.parseRoster(raw);if(roster.person!==ref.person||await wire.rosterHash(roster)!==ref.roster)throw new Hold("invalid","Historical roster hash differs.");rosters.set(ref.roster,roster);
    }
    const resolve=(person,hash)=>rosters.get(hash)?.person===person?rosters.get(hash):null;
    for(const w of p.withdrawals||[])await wire.verifyGroupWithdrawal(w,p.state,resolve);
    await wire.verifyGroupCurrent(p.state,p.root,authority,resolve,seq=>records[seq],p.withdrawals||[]);
    const pids=new Set([h.pid,...(h.human?.audience||[]).map(s=>s.pid)]);let bound=false;
    for(const e of p.memberships){
      if(e.conv!==c.id||!pids.has(e.pid)||e.type==="share")throw new Hold("invalid","Historical witness is outside the captured participation.");
      const person=await this.groupRead(checks,e.author.person===this.me.person?"kv":"persons",e.author.person===this.me.person?"person":e.author.person),pin=await this.groupRead(checks,"pins",e.author.address);
      if(!person||!["self","pinned"].includes(person.state)||!person.hashes.includes(e.author.roster)||!person.devices.some(d=>d.address===e.author.address&&d.fingerprint===e.author.fingerprint)||!pin||pin.pending||pin.fingerprint!==e.author.fingerprint)throw new Hold("invalid","Historical event exact author or roster differs.");
      if(!person.steps?.some(step=>step.hash===e.author.roster&&step.devices.includes(e.author.address+"|"+e.author.fingerprint)))throw new Hold("invalid","Historical event lacks original signing roster.");
      await wire.verifyEvent(e,(await this.pubOf(pin)).sign_key);
      if(["invite","scope"].includes(e.type)){const member=wire.groupMember(p.state,e.author.person);if(!member||await wire.groupAdmissionHash(member.admission)!==e.author.group_admission)throw new Hold("invalid","Historical inviter admission differs.");}
      if(e.pid===h.pid&&["invite","scope"].includes(e.type)&&e.group?.seq===p.state.seq&&e.group.hash===await wire.groupStateHash(p.state))bound=true;
    }
    const evidence=await Promise.all(p.memberships.map(async e=>({e,hash:await wire.eventHash(e)}))),members=await this.dmMembers(c,evidence,checks,p);
    for(const {e} of evidence)if(["invite","scope"].includes(e.type)){
      const scope=e.group,record=scope&&records[scope.seq],host=members.get(e.host?.person),author=wire.groupMember(p.state,e.author.person);
      if(!scope||!record||record.hash!==scope.hash||scope.seq>p.state.seq||members.epochs.get(e.author.fingerprint)!==e.author.group_admission||(scope.host_role==="member"?!host||members.epochs.get(e.host.fingerprint)!==scope.host_admission:scope.host_role!=="visitor"||host||scope.host_admission)|| (e.task_keys||[]).length!==(scope.task_admissions||[]).length||(e.task_keys||[]).some((fp,i)=>!members.epochs.get(fp)||members.epochs.get(fp)!==scope.task_admissions[i])||scope.host_role==="visitor"&&e.role!=="human"&&(!author?.admin||!record.admins.includes(e.author.person)))throw new Hold("invalid","Historical invite epochs differ from signed original state.");
    }
    if(!bound)throw new Hold("invalid","Historical state is not bound to this participation.");
    return p;
  }

  async makeGroupHistoryWitness(c,h,checks=[]) {
    h=await this.groupHistoryScope(c,h,checks);
    const all=[...await this.convEvents(c.id,checks),...await Promise.all((h.human?.proof||[]).map(async e=>({e,hash:await wire.eventHash(e)})))],unique=[...new Map(all.map(r=>[r.hash,r])).values()];
    const scopes=unique.filter(r=>r.e.pid===h.pid&&["invite","scope"].includes(r.e.type)&&r.e.group).map(r=>r.e.group),scope=scopes[0];
    if(!scope)throw new Hold("proof_pending","Original historical scope is missing.");
    if(scopes.some(s=>s.seq!==scope.seq||s.hash!==scope.hash))throw new Hold("invalid","Historical participation state is ambiguous.");
    const saved=await this.groupRead(checks,"kv","group/"+c.id),g=structuredClone(saved),record=g.records[scope.seq]&&wire.parseGroupCommit(g.records[scope.seq]);
    if(!record||record.hash!==scope.hash)throw new Hold("proof_pending","Original historical public record is missing.");
    const decoded=await this.groupOriginalContext(record,wire.parseGroupRoot(c.root),g,checks);
    if(!decoded)throw new Hold("proof_pending","Original historical ciphertext excludes this device.");
    const pids=new Set([h.pid,...(h.human?.audience||[]).map(s=>s.pid)]),p={...decoded.packet,proof:null,memberships:unique.filter(r=>pids.has(r.e.pid)&&r.e.type!=="share").map(r=>r.e)};
    return this.groupHistoryWitness(c,{...h,group_history:p},checks);
  }

  async groupParticipationHistoryCheck(conv,h,forwarder,checks=[]) {
    const c=await this.groupRecord(conv),{packet}=await this.groupTurnEvidence(conv,checks);
    let members=await this.dmMembers(c,null,checks);const own=members.get(this.me.person);
    if(!own?.devices.some(d=>d.address===forwarder.address&&d.fingerprint===forwarder.fingerprint)||!h.group_admission||h.group_admission!==members.epochs.get(this.fp))throw new Hold("invalid","Group PID history requires exact current own linked admission.");
    if(h.group_history&&!await this.ownHistoryAuthority(forwarder,checks))throw new Hold("invalid","Historical witness requires current own human devices.");
    if(h.v!==1||!wire.validID(h.id)||!wire.validID(h.lid)||!wire.validFingerprint(h.from_key)||h.ts<=0||!wire.validAddress(h.from)||h.reply_to&&!wire.validID(h.reply_to)||h.attachments.length>8||h.attachments.some(a=>!a.name||!Number.isSafeInteger(a.size)||a.size<0||a.size>(100<<20)||!wire.validHash(a.sha256)||a.blob))throw new Hold("invalid","Malformed historical group PID item.");
    if(h.sub===wire.SubStatus) {
      if(h.pid||h.attachments.length||!h.ref)throw new Hold("invalid","Historical group status scope malformed.");
      try{wire.parseControl(h.sub,h.body);}catch(e){throw new Hold("invalid",e.message);}
      const historyPacket=h.group_history?await this.groupHistoryWitness(c,h,checks):null;
      if(historyPacket&&(await this.dmMembers(c,[],checks,historyPacket)).epochs.get(this.fp)!==h.group_admission)throw new Hold("invalid","Historical own status admission differs.");
      return this.groupStatusScope(conv,h.ref,h.from,h.from_key,checks,true,historyPacket);
    }
    if(wire.historyAssistantReaction(h)) { // an assistant's own reaction: bound to its participation, its host need not be a member
      if(h.attachments.length)throw new Hold("invalid","Historical assistant reaction scope malformed.");
      try{wire.parseControl(h.sub,h.body);}catch(e){throw new Hold("invalid",e.message);}
      const events=await this.convEvents(conv,checks,h.pid),current=await this.dmMembers(c,events,checks),info=this.resolveAgent(h.pid,events,current);
      this.assistantBinding(info,h,h.from,h.from_key,false);
      await this.groupControlTarget(conv,h.ref,checks);
      await this.assistantRequest(conv,h.pid,h.ref,h.agent_id,info,current,checks);
      return info;
    }
    if([wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(h.sub)) {
      if(h.pid||h.attachments.length||!h.ref)throw new Hold("invalid","Historical group control scope malformed.");
      try{wire.parseControl(h.sub,h.body);}catch(e){throw new Hold("invalid",e.message);}
      const sender=[...members.values()].find(p=>p.devices.some(d=>d.address===h.from&&d.fingerprint===h.from_key));
      if(!sender)throw new Hold("invalid","Historical group control author is not current.");
      await this.groupControlFence(members,h.from_key,this.fp);await this.groupControlTarget(conv,h.ref,checks);
      const targetAuthor=[...members.values()].find(p=>p.devices.some(d=>d.fingerprint===h.ref.fingerprint));
      if(h.sub!==wire.SubReaction&&targetAuthor?.person!==sender.person)throw new Hold("invalid","Historical group control differs from exact author person.");
      return null;
    }
    if(!wire.validID(h.pid)||h.ref||!["","event","excerpt"].includes(h.sub))throw new Hold("invalid","Historical group participation scope malformed.");
    const historyPacket=h.group_history ? await this.groupHistoryWitness(c,h,checks) : null;
    const events=historyPacket ? (await Promise.all(historyPacket.memberships.map(async e=>({e,hash:await wire.eventHash(e)})))).filter(r=>r.e.pid===h.pid) : await this.convEvents(conv,checks,h.pid);let candidate=null;
    if(historyPacket){members=await this.dmMembers(c,events,checks,historyPacket);if(members.epochs.get(this.fp)!==h.group_admission)throw new Hold("invalid","Historical own admission differs.");}
    if(h.sub==="event") {
      candidate=await this.eventRecord(h.body);const e=candidate.e,author=e.author,p=[...members.values(),...members.hosts.values()].find(p=>p.devices.some(d=>d.address===author.address&&d.fingerprint===author.fingerprint));
      const forwarded=author.address!==h.from||author.fingerprint!==h.from_key;
      if(forwarded&&(e.type!=="dismiss"||![...members.values()].some(p=>p.devices.some(d=>d.address===h.from&&d.fingerprint===h.from_key))))throw new Hold("invalid","Only current members forward historical participation ends.");
      if(h.kind!=="message"||e.conv!==conv||e.pid!==h.pid||p?.person!==author.person||!p?.hashes.includes(author.roster))throw new Hold("invalid","Historical group event original author admission differs.");
      if(author.group_admission&&author.group_admission!==members.epochs.get(author.fingerprint))throw Object.assign(new Hold("invalid","Historical event original epoch changed."),{historicalEpoch:true});
      const pin=await this.groupRead(checks,"pins",author.address);if(!pin||pin.pending||pin.fingerprint!==author.fingerprint)throw new Hold("invalid","Historical event exact source key changed.");
      try{await wire.verifyEvent(e,(await this.pubOf(pin)).sign_key);}catch(err){throw new Hold("invalid",err.message);}
      if(e.host){const hp=await this.sendKey(e.host.address);if(hp.fingerprint!==e.host.fingerprint||(await this.personOf(e.host.address,hp)).person!==e.host.person)throw new Hold("invalid","Historical invitation host proof differs.");}
      events.push(candidate);
    }
    const current=await this.dmMembers(c,events,checks,historyPacket),info=this.resolveAgent(h.pid,events,current);
    if(historyPacket)current.historyEvents=await Promise.all(historyPacket.memberships.map(async e=>({e,hash:await wire.eventHash(e)})));
    if(!info.invite)throw new Hold("proof_pending","Historical PID original invitation is missing.");
    if(h.human) {
      const evidence=await this.groupHumanEvidence(c,h.human,checks,historyPacket);
      this.humanTurnAuthorization({...h,conv},evidence,info,h.from,h.from_key,this.address,this.fp,true);
    }
    // Retained own-member history is inert; only its lifecycle gate differs
    // from live delivery. Exact targets, author keys and requests still bind it.
    const retained=(h.sub==="excerpt"||!h.sub&&(["question","task","answer","result"].includes(h.kind)||progressOutput(h)))&&this.retainedAssistant(info,events);
    this.externalRole({...h,conv,replica:h.sub==="excerpt"},retained?{...info,state:"active"}:info,current,h.from,h.from_key);
    await this.checkExternalReply({...h,conv},info,current,checks,true);
    return info;
  }

  async admitGroupParticipationHistory(n,env,pin,h,checks,packet,members) {
    if(!n.replica||n.attachments.length||!members.some(p=>p.person===this.me.person&&p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint)))throw new Hold("invalid","Group PID history is only a current own linked replica.");
    for(const e of h.human?.proof.filter(e=>e.type==="scope")||[]) {
      const host=await this.sendKey(e.host.address);
      if(host.fingerprint!==e.host.fingerprint||(await this.personOf(e.host.address,host)).person!==e.host.person)throw new Hold("invalid","Historical captured host proof differs.");
    }
    await this.groupParticipationHistoryCheck(n.conv,h,{address:env.from,fingerprint:pin.fingerprint},checks);
    const key=h.from_key+"/"+h.lid,hash=h.ref?await this.groupControlHash(n.conv,h):await wire.groupHistoryContentHash(n.conv,h),seen=await this.groupRead(checks,"lids",key);
    if(seen&&(seen.conv!==n.conv||seen.hash!==hash))throw new Hold("conflicting_duplicate","Historical group PID logical content differs.");
    const ops=[{s:"kv",k:"group-carrier/"+env.id,v:true}];
    if(!seen)ops.push({s:"inbox",k:h.id,v:{...h,v:2,conv:n.conv,fp:h.from_key,claimed_key:h.from_key,person:await this.personOfFp(h.from_key),history:true,synced_from:env.from,read:true,replica:true,state:"",own:this.me.devices.some(d=>d.fingerprint===h.from_key),...(h.ref?{v:3,control:true}:{}),attachments:h.attachments.map(a=>({...a,availability:"requestable"}))}},{s:"lids",k:key,v:{id:h.id,conv:n.conv,hash,history:true}});
    ops.checks=checks;ops.groupCarrier=true;return ops;
  }

  async admitGroupHistory(n,env,pin,checks,packet,members) {
    const sender=members.find(m=>m.devices?.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint));
    if(!sender || !n.replica || n.attachments.length)throw new Hold("invalid","Group history is not from a current device or carries unexpected bytes.");
    let h;try{h=wire.parseHistory(n.body);}catch(e){throw new Hold("invalid",e.message);}
    if((JSON.parse(n.body).attachments||[]).some(a=>a.blob?.id||a.blob?.size||a.blob?.sha256))throw new Hold("invalid","Historical group file is not a manifest.");
    if(h.pid&&h.human?.proof.some(e=>e.role==="human"))throw new Hold("invalid","Group human guest execution audience is not enabled.");
    if(h.pid||h.ref)return this.admitGroupParticipationHistory(n,env,pin,h,checks,packet,members);
    if(h.kind!=="message"||h.sub||h.target||h.pid||h.agent_id||h.status||h.ref||h.origin&&h.origin!=="ui"||h.ts<=0||h.reply_to&&!wire.validID(h.reply_to))throw new Hold("invalid","Group history contains nonordinary input.");
    if(h.attachments.length>8 || (JSON.parse(n.body).attachments||[]).some(a=>!a.name || !Number.isSafeInteger(a.size) || a.size<0 || a.size>(100<<20) || !wire.validHash(a.sha256) || a.blob?.id || a.blob?.size || a.blob?.sha256))throw new Hold("invalid","Group history file is not an exact manifest.");
    const ref={lid:h.lid,author:h.from_key,hash:await wire.groupHistoryContentHash(n.conv,h)}, self=wire.groupMember(packet.state,this.me.person);
    const selected=wire.groupAllowsHistory(self.admission,ref);
    // Own linked history vouches for past attribution. An author's departure
    // cannot erase our old messages; the live sender/receiver gates stay above.
    if(!selected && (sender.person!==this.me.person || h.group_admission!==await wire.groupAdmissionHash(self.admission)))throw new Hold("invalid","Historical item lacks its exact selected grant/current own live admission.");
    const key=h.from_key+"/"+h.lid,seen=await this.groupRead(checks,"lids",key);
    if(seen && (seen.conv!==n.conv || seen.hash!==ref.hash))throw new Hold("conflicting_duplicate","Group history conflicts with an existing logical turn.");
    const ops=[{s:"kv",k:"group-carrier/"+env.id,v:true}];
    if(!seen)ops.push({s:"inbox",k:h.id,v:{...h,group_admission:selected?undefined:h.group_admission,v:wire.Version2,id:h.id,conv:n.conv,fp:"",claimed_key:h.from_key,history:true,synced_from:env.from,read:true,replica:true,state:"",attachments:h.attachments.map(a=>({...a,availability:"requestable"}))}},{s:"lids",k:key,v:{id:h.id,conv:n.conv,hash:ref.hash,history:true}});
    ops.checks=checks;ops.groupCarrier=true;return ops;
  }

  async admitGroupLifecycle(n,env,pin) {
    const checks=[], root=wire.parseGroupRoot(n.root), desc=wire.parseGroupCarrier(n.body);
    if(desc.to_key!==this.fp || root.realm!==this.realm) throw new Hold("invalid","Group lifecycle targets another key or workspace.");
    const currentPin=await this.groupRead(checks,"pins",env.from);
    if(!currentPin || currentPin.pending || currentPin.fingerprint!==pin.fingerprint)throw new StoreConflict();
    const body=new TextDecoder("utf-8",{fatal:true}).decode(await wire.decryptFile(await this.cipherOf(n.attachments[0],true),n.attachments[0],this.keys));
    const person=await this.personOf(env.from,pin), ops=[];
    try {
      if(n.sub===wire.SubGroupInvite) {
        const proposal=await wire.validateGroupInvitation(wire.parseGroupInvitation(body));
        if(wire.rootJSON(proposal.root)!==wire.rootJSON(root) || desc.seq!==proposal.state.seq || desc.hash!==await wire.groupStateHash(proposal.state)) throw Error("Group invitation descriptor differs.");
        await this.verifyGroupProposal(proposal,person.person,env.from,pin.fingerprint,checks);
        const me=await this.groupRead(checks,"kv","person");
        if(proposal.target!==me.person || proposal.roster!==me.hash || !me.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp)) throw Error("Invitation does not name this exact current person and device.");
        const id=await wire.groupInvitationID(proposal), k="group-invitation/in/"+id, old=await this.groupRead(checks,"kv",k);
        if(old && (old.inviter!==env.from || old.fp!==pin.fingerprint || old.owner!==person.person)) throw Error("Conflicting invitation sender.");
        const cancelled=await this.groupRead(checks,"kv","group-cancel/in/"+id+"/"+pin.fingerprint);
        const status=cancelled?.conv===n.conv&&cancelled.inviter===env.from&&cancelled.owner===person.person&&cancelled.seq===proposal.seq&&cancelled.hash===proposal.prev?"cancelled":"pending";
        if(!old) ops.push({s:"kv",k,v:{type:"group-invitation",id,direction:"in",status,proposal,inviter:env.from,owner:person.person,fp:pin.fingerprint}});
      } else if(n.sub===wire.SubGroupConsent) {
        const consent=wire.parseGroupConsent(body), k="group-invitation/out/"+consent.invitation, row=await this.groupRead(checks,"kv",k);
        if(consent.decision==="cancelled") {
          const incomingKey="group-invitation/in/"+consent.invitation,incoming=await this.groupRead(checks,"kv",incomingKey);
          if(incoming&&(incoming.inviter!==env.from||incoming.fp!==pin.fingerprint||incoming.owner!==person.person||wire.rootJSON(incoming.proposal.root)!==wire.rootJSON(root)||desc.seq!==incoming.proposal.seq||desc.hash!==incoming.proposal.prev))throw Error("Cancellation is not from the exact inviting device.");
          ops.push({s:"kv",k:"group-cancel/in/"+consent.invitation+"/"+pin.fingerprint,v:{conv:n.conv,inviter:env.from,owner:person.person,seq:desc.seq,hash:desc.hash}});
          if(incoming&&["pending","accepted","stale"].includes(incoming.status))ops.push({s:"kv",k:incomingKey,v:{...incoming,status:"cancelled"}});
        } else {
        if(!row) throw Error("Consent has no recorded local invitation.");
        const p=row.proposal;
        if(wire.rootJSON(root)!==wire.rootJSON(p.root) || person.person!==p.target || consent.decision!=="declined"&&person.hash!==p.roster || desc.seq!==p.seq || desc.hash!==p.prev) throw Error("Consent is not for this exact invitation and current person.");
        const resolve=consent.decision==="declined"?null:(await this.verifyGroupProposal(p,row.owner,row.inviter,row.fp,checks)).resolve;
        if(consent.admission) {
          const a=consent.admission;
          if(a.by!==pin.fingerprint || a.conv!==p.state.conv || a.realm!==root.realm || a.person!==p.target || a.roster!==p.roster || a.seq!==p.seq || a.prev!==p.prev || JSON.stringify(a.history)!==JSON.stringify(p.history)) throw Error("Consent admission differs from exact proposed transition.");
          await wire.verifyGroupAdmission(a,resolve);
        }
        if(row.status!=="pending" && row.status!==consent.decision && row.status!=="published" && !(consent.decision==="declined"&&row.status==="stale")) throw Error("Consent conflicts with recorded decision.");
        if(row.status==="pending"||consent.decision==="declined"&&row.status==="stale") ops.push({s:"kv",k,v:{...row,status:consent.decision,consent}});
        }
      } else {
        const w=wire.parseGroupWithdrawal(body), k="group/"+n.conv, g=await this.groupRead(checks,"kv",k);
        if(!g || g.root!==wire.rootJSON(root)) throw new Hold("proof_pending","Group withdrawal lacks original proof.");
        if(w.conv!==n.conv || w.realm!==root.realm || w.by!==pin.fingerprint || w.person!==person.person || w.roster!==person.hash || desc.hash!==wire.hex(await wire.sha256(wire.groupWithdrawalCanonical(w)))) throw Error("Withdrawal is not this exact current signer and admission.");
        const packet=g.context?wire.parseGroupContext(g.context):null;
        const resolved=await this.groupResolver(root,[],packet,g,checks);await wire.verifyGroupWithdrawalSignature(w,resolved.resolve);
        const next=structuredClone(g), member=packet&&wire.groupMember(packet.state,w.person), text=wire.groupWithdrawalJSON(w);
        if(member&&!member.admin&&await wire.groupAdmissionHash(member.admission)===w.admission) {await wire.verifyGroupWithdrawal(w,packet.state,resolved.resolve);if(!next.withdrawals.includes(text))next.withdrawals.push(text);}
        else if(!next.pending.includes(text))next.pending.push(text);
        ops.push({s:"kv",k,v:next});
      }
    } catch(e) {if(e instanceof Hold||e instanceof StoreConflict||retryable(e)) throw e;throw new Hold("invalid",e.message);}
    const identity=await this.groupRead(checks,"kv","identity");
    if(identity?.address!==this.address||identity.fingerprint!==this.fp||identity.revoked) throw new Hold("invalid","Local group identity changed.");
    const lidKey="group-lid/"+pin.fingerprint+"/"+n.lid, hash=wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify([n.conv,n.root,n.sub,n.body,n.attachments])))), prior=await this.groupRead(checks,"kv",lidKey);
    if(prior&&prior!==hash) throw new Hold("conflicting_duplicate","Group logical lifecycle conflict.");
    ops.push({s:"kv",k:lidKey,v:hash},{s:"kv",k:"group-carrier/"+env.id,v:true});ops.checks=checks;ops.groupCarrier=true;return ops;
  }
  // Existing kv namespace only: original signed opaque commits + verified current
  // context/withdrawals, never convs/inbox/history or ordinary attachment metadata.
  async groupRead(checks, s, k) {
    const v = await this.store.get(s, k);
    if (!checks.some((c) => c.s === s && c.k === k)) checks.push({ s, k, v });
    return v;
  }

  async groupResolver(root, records, packet, old, checks, extraRefs = []) {
    const refs = [{ person: root.creator.person, roster: root.creator.roster }, ...records.map((c) => ({ person: c.actor, roster: c.actor_roster })), ...extraRefs];
    if (packet) refs.push({ person: packet.state.actor, roster: packet.state.actor_roster }, ...packet.state.members,
      ...(packet.withdrawals || []), ...(old.pending || []).map(wire.parseGroupWithdrawal));
    const raw = { ...(old.rosters || {}) }, rosters = new Map(), people = new Map();
    for (const person of new Set(refs.map((r) => r.person))) {
      if (person === this.me?.person) this.me = (await this.store.get("kv", "person")) || this.me;
      const p = await this.pinChain(person); // existing pinned roster refresh, not a group journal fetch
      if (!p || p.state === "conflict") throw new Hold("identity_conflict", "group person is frozen");
      const stored = await this.groupRead(checks, p.state === "self" ? "kv" : "persons", p.state === "self" ? "person" : person);
      if (!stored || stored.hash !== p.hash || stored.state === "conflict") throw new StoreConflict();
      people.set(person, stored);
      const needed = refs.filter((r) => r.person === person).map((r) => r.roster);
      if (needed.some((hash) => !raw[hash])) {
        const steps = await this.chain(person, -1); // original rosters, verified against the already pinned head
        if (!steps.length) throw new Hold("proof_pending", "group roster is missing");
        await wire.verifyFirst(steps[0]);
        for (let i = 1; i < steps.length; i++) await wire.verifyNext(steps[i], steps[i - 1]);
        const hashes = await Promise.all(steps.map(wire.rosterHash));
        if (!hashes.includes(stored.hash)) throw new Hold("identity_conflict", "group roster disagrees with pinned head");
        for (let i = 0; i < steps.length; i++) if (stored.hashes.includes(hashes[i])) raw[hashes[i]] = wire.rosterJSON(steps[i]);
      }
      for (const hash of needed) {
        if (!stored.hashes.includes(hash) || !raw[hash]) throw new Hold("proof_pending", "group names an unverified roster step");
        const roster = await wire.parseRoster(raw[hash]);
        if (roster.person !== person || await wire.rosterHash(roster) !== hash) throw new Hold("invalid", "group roster binding differs");
        rosters.set(hash, roster);
      }
    }
    return { resolve: (person, hash) => rosters.get(hash)?.person === person ? rosters.get(hash) : null, raw, people };
  }

  async noteGroupHead(h) {
    if (!h || !wire.validHash(h.conv) || !wire.validFingerprint(h.bootstrap) || !wire.validHash(h.hash) || !Number.isSafeInteger(h.seq) || h.seq < 0) return;
    for (;;) {
      const checks = [], g = await this.groupRead(checks, "kv", "group/" + h.conv);
      if (!g || wire.parseGroupRoot(g.root).creator.fingerprint !== h.bootstrap) return;
      const k = "group-head/" + h.conv, before = await this.groupRead(checks, "kv", k);
      if (before && h.seq < before.seq) return;
      const next = before && h.seq === before.seq && (before.hash !== h.hash || before.conflict) ? { ...before, conflict: true } : h;
      try { await this.store.write([{ s: "kv", k, v: next }], checks); return; }
      catch (e) { if (!(e instanceof StoreConflict)) throw e; }
    }
  }

  async groupTarget(state, withdrawals, checks) {
    const me = await this.groupRead(checks, "kv", "person");
    if (!me || me.state !== "self" || !me.devices.some((d) => d.address === this.address && d.fingerprint === this.fp)) throw new Hold("invalid", "group target is not a current own device");
    const member = wire.groupMember(state, me.person);
    if (!member || await wire.groupWithdrawn(state, member, withdrawals)) throw new Hold("invalid", "group target admission is absent or withdrawn");
    return me;
  }

  async groupCurrentState(conv, checks = []) {
    const g = await this.groupRead(checks, "kv", "group/" + conv);
    if (!g?.context) throw new Hold("proof_pending", "group current context missing");
    if (wire.parseGroupRoot(g.root).realm !== this.realm) throw new Hold("invalid", "group belongs to another realm");
    const p = wire.parseGroupContext(g.context), head = await this.groupRead(checks, "kv", "group-head/" + conv);
    if (g.records.length - 1 !== p.state.seq || head && (head.conflict || head.seq > p.state.seq || head.seq === p.state.seq && head.hash !== await wire.groupStateHash(p.state))) throw new Hold("proof_pending", "group current context is behind latest head");
    const withdrawals = [...g.withdrawals, ...g.pending].map(wire.parseGroupWithdrawal);
    for (const m of p.state.members) if (m.admin && await wire.groupWithdrawn(p.state, m, withdrawals)) throw new Hold("invalid", "group pending withdrawal conflicts with admin admission");
    return { ...p, withdrawals };
  }


  async groupCurrent(conv) {
    const packet=await this.groupCurrentState(conv);
    await this.groupTarget(packet.state,packet.withdrawals,[]);
    return packet;
  }

  async groupContextWithdrawals(packet, g, resolve, people, checks) {
    const n = { conv: packet.state.conv }, root = packet.root, k = "group/" + n.conv;
    const pins = g.withdrawals.map(wire.parseGroupWithdrawal), pending = g.pending.map(wire.parseGroupWithdrawal);
    for (const w of packet.withdrawals || []) {
      const encoded = wire.groupWithdrawalJSON(w);
      if (g.withdrawals.includes(encoded)) continue;
      const person = people.get(w.person);
      try {
        if (w.conv !== n.conv || w.realm !== root.realm || !person || person.hash !== w.roster) throw Error("group withdrawal roster is not latest pinned roster");
        await wire.verifyGroupWithdrawalSignature(w, resolve);
      } catch (e) { throw new Hold("invalid", e.message); }
      try { await wire.verifyGroupWithdrawal(w, packet.state, resolve); }
      catch (e) {
        if (!g.pending.includes(encoded)) g.pending.push(encoded);
        await this.store.write([{ s: "kv", k, v: g }, { s: "kv", k: "group-realm", v: root.realm }], checks);
        const m = wire.groupMember(packet.state, w.person);
        throw new Hold(m?.admin && await wire.groupAdmissionHash(m.admission) === w.admission ? "invalid" : "proof_pending", "group withdrawal needs matching ordinary admission context");
      }
      pins.push(w);
    }
    for (const w of pending) {
      const m = wire.groupMember(packet.state, w.person);
      if (!m || await wire.groupAdmissionHash(m.admission) !== w.admission) continue;
      try {
        if (people.get(w.person)?.hash !== w.roster) throw Error("pending withdrawal roster is not current");
        await wire.verifyGroupWithdrawal(w, packet.state, resolve);
      } catch (e) { throw new Hold("invalid", e.message); }
      pins.push(w);
    }
    return pins;
  }

  async groupOriginalContext(record, root, g, checks) {
    let plain;
    try {
      const d = new Decrypter(); d.addIdentity(this.keys.box);
      plain = await d.decrypt(record.ciphertext);
    } catch (e) {
      // The pinned age implementation exposes this exact recipient-miss error.
      // Header/authentication/decoded errors never qualify as an unreadable gap.
      if (e.message === "no identity matched any of the file's recipients") return null;
      throw e;
    }
    if (plain.length > wire.MaxGroupState) throw Error("group original context exceeds bound");
    const packet = wire.parseGroupContext(new TextDecoder("utf-8", { fatal: true }).decode(plain));
    if (wire.rootJSON(packet.root) !== wire.rootJSON(root) || record.bootstrap !== root.creator.fingerprint || !await wire.groupCommitMatches(record, packet.state)) throw Error("group original ciphertext differs from signed header/root");
    const resolved = await this.groupResolver(root, [record], packet, g, checks);
    Object.assign(g.rosters, resolved.raw);
    const roster = resolved.resolve(record.actor, record.actor_roster);
    const writer = roster?.devices.find((d) => d.address === record.writer);
    if (!writer) throw new Hold("proof_pending", "group original writer roster missing");
    await wire.verifyGroupCommit(record, writer);
    if (packet.proof?.length) await wire.verifyGroupContext(packet, resolved.resolve);
    return { packet, ...resolved };
  }

  async recoverGroupGap(target, old, g, records, checks) {
    let prior = old;
    while (prior.state.seq + 1 < target.state.seq) {
      let decoded = await this.groupOriginalContext(records[prior.state.seq + 1], target.root, g, checks);
      let fresh = false;
      if (!decoded) {
        const me = await this.groupTarget(target.state, [...g.withdrawals, ...g.pending].map(wire.parseGroupWithdrawal), checks);
        const member = wire.groupMember(target.state, me.person), before = wire.groupMember(prior.state, me.person);
        if (member.admission.seq <= prior.state.seq || before && await wire.groupAdmissionHash(before.admission) === await wire.groupAdmissionHash(member.admission)) throw new Hold("proof_pending", "group unreadable gap requires fresh self consent");
        decoded = await this.groupOriginalContext(records[member.admission.seq], target.root, g, checks);
        if (!decoded) throw new Hold("proof_pending", "group fresh self join ciphertext unavailable");
        const joined = wire.groupMember(decoded.packet.state, me.person);
        if (!joined || decoded.packet.state.seq !== member.admission.seq || joined.roster !== member.roster || await wire.groupAdmissionHash(joined.admission) !== await wire.groupAdmissionHash(member.admission) || joined.admission.seq !== decoded.packet.state.seq || joined.admission.prev !== decoded.packet.state.prev) throw Error("group fresh self admission differs from exact original join slot");
        const joinedRoster = decoded.resolve(joined.person, joined.roster);
        let exact = false;
        for (const d of joinedRoster?.devices || []) if (d.address === this.address && await wire.fingerprint(d) === this.fp) exact = true;
        if (!exact) throw Error("group fresh join excludes this exact device");
        await this.groupTarget(decoded.packet.state, [...g.withdrawals, ...g.pending].map(wire.parseGroupWithdrawal), checks);
        fresh = true;
      }
      const { packet, resolve, people } = decoded;
      const pins = await this.groupContextWithdrawals(packet, g, resolve, people, checks);
      if (!fresh) await wire.verifyGroupState(packet.state, target.root, prior.state, resolve, pins);
      await wire.verifyGroupCurrent(packet.state, target.root, records[packet.state.seq], resolve, (seq) => records[seq], pins);
      for (const w of pins) for (const state of [prior.state, packet.state]) {
        const m = wire.groupMember(state, w.person);
        if (m?.admin && await wire.groupAdmissionHash(m.admission) === w.admission) throw Error("group withdrawal conflicts with promoted admin");
      }
      for (const w of pins) { const text = wire.groupWithdrawalJSON(w); if (!g.withdrawals.includes(text)) g.withdrawals.push(text); g.pending = g.pending.filter((x) => x !== text); }
      prior = packet;
    }
    return prior;
  }

  async admitGroupCarrier(n, env, pin) {
    const checks = [];
    const senderPin = await this.groupRead(checks, "pins", env.from);
    if (!senderPin || senderPin.pending || senderPin.fingerprint !== pin.fingerprint) throw new StoreConflict();
    const identity = await this.groupRead(checks, "kv", "identity");
    if (!identity || identity.revoked || identity.address !== this.address || identity.fingerprint !== this.fp) throw new Hold("invalid", "group local identity changed");
    const realmBefore = await this.groupRead(checks, "kv", "group-realm");
    const seenKey = "group-lid/" + pin.fingerprint + "/" + n.lid, seen = await this.groupRead(checks, "kv", seenKey);
    const hash = wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify([n.conv, n.root, n.sub, n.body, n.attachments,...(n.pid?[n.pid]:[])]))));
    if (seen && seen !== hash) throw new Hold("conflicting_duplicate", "group logical carrier conflict");
    let root, desc, body;
    try {
      root = wire.parseGroupRoot(n.root); desc = wire.parseGroupCarrier(n.body);
      if (desc.to_key !== this.fp || !wire.validID(this.realm || "") || root.realm !== this.realm || realmBefore && realmBefore !== root.realm) throw Error("group carrier exact key/realm differs");
      if (seen) {
        const ops = [{ s: "kv", k: "group-carrier/" + env.id, v: true }];
        ops.checks = checks; ops.groupCarrier = true; return ops;
      }
      body = new TextDecoder("utf-8", { fatal: true }).decode(await wire.decryptFile(await this.cipherOf(n.attachments[0], true), n.attachments[0], this.keys));
    } catch (e) { if (retryable(e) || e instanceof HubError) throw e; throw new Hold("invalid", e.message); }
    const k = "group/" + n.conv, before = await this.groupRead(checks, "kv", k);
    const g = before ? structuredClone(before) : { root: wire.rootJSON(root), records: [], context: "", withdrawals: [], pending: [], rosters: {} };
    if (g.root !== wire.rootJSON(root)) throw new Hold("invalid", "group original root differs");
    const prior=g.context?wire.parseGroupContext(g.context):null;
    const self=prior&&wire.groupMember(prior.state,this.me?.person), visitor=!!n.pid && (!self || await wire.groupWithdrawn(prior.state,self,[...g.withdrawals,...g.pending].map(wire.parseGroupWithdrawal)));
    let visitorInvite=null;
    if(visitor) {
      visitorInvite=await this.groupVisitorInvite(root,n.pid,checks);
      if(env.from!==visitorInvite.e.author.address||pin.fingerprint!==visitorInvite.e.author.fingerprint) {
        const p=await this.personOf(env.from,pin);await this.groupRead(checks,"persons",p.person);
        const oldMember=prior&&wire.groupMember(prior.state,p.person);
        if(!oldMember||await wire.groupWithdrawn(prior.state,oldMember,[...g.withdrawals,...g.pending].map(wire.parseGroupWithdrawal)))throw new Hold("invalid","Visitor carrier sender is not prior verified disclosure audience.");
      }
    }
    let page, packet;
    try {
      if (n.sub === wire.SubGroupProof) {
        page = wire.parseGroupJournal(body);
        const last = page.records?.at(-1);
        if (!last || last.seq !== desc.seq || last.hash !== desc.hash) throw Error("group proof descriptor differs");
      } else {
        packet = wire.parseGroupContext(body);
        if (wire.rootJSON(packet.root) !== g.root || packet.proof?.length || packet.state.seq !== desc.seq || await wire.groupStateHash(packet.state) !== desc.hash) throw Error("group context descriptor/root differs");
      }
    } catch (e) { throw new Hold("invalid", e.message); }
    const records = g.records.map(wire.parseGroupCommit), previous = records.at(-1) || null;
    let resolved;
    try { resolved = await this.groupResolver(root, page?.records || [], packet, g, checks); }
    catch (e) { if (e instanceof Hold || e instanceof StoreConflict || retryable(e)) throw e; throw new Hold("invalid", e.message); }
    const { resolve, raw, people } = resolved;
    g.rosters = raw;
    try { await wire.verifyGroupProofPage(root, { records: [], more: false }, resolve, this.realm, { previous }); }
    catch (e) { throw new Hold("invalid", e.message); }
    if (page) {
      if (page.records[0].seq > records.length) {
        await this.store.write([{ s: "kv", k, v: g }, { s: "kv", k: "group-realm", v: root.realm }], checks);
        throw new Hold("proof_pending", "group original prefix missing");
      }
      try { await wire.verifyGroupProofPage(root, page, resolve, this.realm, { previous, known: (seq) => records[seq] }); }
      catch (e) { throw new Hold("invalid", e.message); }
      for (const c of page.records) if (c.seq >= g.records.length) g.records.push(wire.groupCommitJSON(c));
    } else {
      const authority = records[packet.state.seq];
      if (!authority) throw new Hold("proof_pending", "group original authority missing");
      const hint = await this.groupRead(checks, "kv", "group-head/" + n.conv);
      if (records.length - 1 !== packet.state.seq || hint && (hint.conflict || hint.seq > packet.state.seq || hint.seq === packet.state.seq && hint.hash !== desc.hash)) throw new Hold("proof_pending", "group context behind latest head");
      let old = g.context ? wire.parseGroupContext(g.context) : null;
      let pins = await this.groupContextWithdrawals(packet, g, resolve, people, checks);
      try {
        if (!visitor && old && packet.state.seq > old.state.seq + 1) {
          old = await this.recoverGroupGap(packet, old, g, records, checks);
          pins = await this.groupContextWithdrawals(packet, g, resolve, people, checks);
        }
        if (old && packet.state.seq === old.state.seq && await wire.groupStateHash(old.state) !== desc.hash) throw Error("group conflicting current state");
        if (old && packet.state.seq === old.state.seq + 1) await wire.verifyGroupState(packet.state, root, old.state, resolve, pins);
        await wire.verifyGroupCurrent(packet.state, root, authority, resolve, (seq) => records[seq], pins);
        for (const w of pins) for (const state of [old?.state, packet.state].filter(Boolean)) {
          const m = wire.groupMember(state, w.person);
          if (m?.admin && await wire.groupAdmissionHash(m.admission) === w.admission) throw Error("group withdrawal conflicts with promoted admin");
        }
      } catch (e) { if (e instanceof Hold || e instanceof StoreConflict || retryable(e) || e instanceof HubError) throw e; throw new Hold("invalid", e.message); }
      if(visitor) {
        const original=records[visitorInvite.e.group.seq];
        if(!original||original.hash!==visitorInvite.e.group.hash)throw new Hold("invalid","Visitor invitation original slot differs.");
        const sender=[...people.values()].find(p=>p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint));
        const current=sender&&wire.groupMember(packet.state,sender.person);
        const member=current&&!await wire.groupWithdrawn(packet.state,current,pins);
        const actor=sender&&packet.state.actor===sender.person&&packet.state.by===pin.fingerprint&&packet.state.actor_roster===sender.hash;
        const departure=sender&&pins.some(w=>w.person===sender.person&&w.by===pin.fingerprint&&w.roster===sender.hash);
        if(!member&&!actor&&!departure)throw new Hold("invalid","Visitor context sender has no current or signed departure authority.");
      } else await this.groupTarget(packet.state, pins, checks);
      if(packet.memberships?.length) {
        const sender=[...people.values()].find(p=>p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint));
        const member=sender&&wire.groupMember(packet.state,sender.person);
        // Extra lifecycle claims need a current admin or own linked member.
        // Retain legitimate departing-publisher state without its memberships.
        if(visitor || !member || !member.admin&&sender.person!==this.me.person || await wire.groupWithdrawn(packet.state,member,pins))packet.memberships=undefined;
      }
      g.context = wire.groupContextJSON(packet);
      for (const w of pins) { const text = wire.groupWithdrawalJSON(w); if (!g.withdrawals.includes(text)) g.withdrawals.push(text); g.pending = g.pending.filter((x) => x !== text); }
    }
    // Logical quiet dedup survives receipt flushing. Original bytes are retained
    // in verified group state; no plaintext carrier is copied into held/files.
    const ops = [{ s: "kv", k, v: g }, { s: "kv", k: "group-realm", v: root.realm }, { s: "kv", k: seenKey, v: hash }, { s: "kv", k: "group-carrier/" + env.id, v: true }];
    if(packet?.memberships?.length)ops.push(...await this.roomMembershipOps(packet,checks));
    ops.checks = checks; ops.groupCarrier = true;
    return ops;
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
        const previous = await this.store.get("held", head.id);
        await this.store.write([{ s: "held", k: head.id, v: previous || { id: head.id, from: String(head.from || ""), reason: "invalid", envelope: data, at: this.now() } },
          { s: "receipts", k: head.id, v: { id: head.id, state: "quarantined" } }]);
        this.flushReceipts().catch(() => {});
      }
      return;
    }
    if ((await this.store.get("inbox", env.id)) || (await this.store.get("held", env.id)) || (await this.store.get("receipts", env.id)) || (await this.store.get("kv", "group-carrier/" + env.id))) {
      // Seen before: its receipt is sent again, nothing is stored twice.
      const state = (await this.store.get("inbox", env.id)) || (await this.store.get("kv", "group-carrier/" + env.id)) ? "delivered" : "quarantined";
      if (!(await this.store.get("receipts", env.id))) await put(this.store, "receipts", env.id, { id: env.id, state });
    } else {
      await this.admit(data, env); // stored (or held) before any receipt
    }
    this.flushReceipts().catch(() => {}); // sent alongside the next messages, not before them (MEL-546)
  }

  async archiveHeldNotice(id) {
    if (!wire.validID(id)) throw Error("Invalid held-message ID.");
    const record = await this.store.get("held", id);
    if (!record || !canArchiveHeld(record.reason)) throw Error("This held notice cannot be archived.");
    await this.store.write([{s:"held",k:id,v:{...record,notice_archived:true}}],[{s:"held",k:id,v:record}]);
    this.changed(true);
    return {note:"Notice archived locally. The retained message has not been accepted or run."};
  }

  async hold(env, data, reason, why = "") {
    const previous = await this.store.get("held", env.id);
    await this.store.write([{ s: "held", k: env.id, v: { id: env.id, from: env.from, reason, envelope: data, at: previous?.at || this.now(), detail_code:heldDiagnosticCode(why), notice_archived:canArchiveHeld(reason) && !!previous?.notice_archived } },
      { s: "receipts", k: env.id, v: { id: env.id, state: "quarantined" } }]);
    this.changed(true);
  }

  // admit verifies and stores one envelope as the Go client does
  // (verifyAndStore, admitConv). Failures to ask the server are thrown, so
  // the message stays unacknowledged and comes again.
  async admit(data, env, fromHeld = false, historyRecovery = false) {
    try {
      const recoveryChecks = historyRecovery ? await this.heldHistoryChecks(data, env) : [];
      if (!recoveryChecks) return;
      const ops = await this.admitInner(data, env);
      if (this.erased.size) this.eraseArrivals(ops); // a copy of a turn deleted here stays a skeleton
      const checks = ops.checks || [];
      checks.push(...recoveryChecks);
      for (const op of ops) if (op.s === "inbox" && op.v?.receiver_route?.op === "request" && !op.v.history) await this.receiverOriginAuthority(op.v, checks);
      await this.applyReadArrivals(ops,checks);
      ops.checks = checks;
      // A held message that now proves out was acknowledged as held already.
      ops.push(fromHeld ? { s: "held", k: env.id, v: undefined } : { s: "receipts", k: env.id, v: { id: env.id, state: "delivered" } });
      await this.store.write(ops, ops.checks);
      if (ops.some((o) => o.s === "erased")) await this.loadErased();
      this.changed(true);
      if (ops.some(o => o.s === "inbox" && o.v?.sub === "event")) { this.recoverHumanExcerpts().catch(() => {}); this.discloseHumanAudience(); this.retryHeld().catch(() => {}); }
      if (ops.groupCarrier) this.retryHeld().catch(() => {});
      if (ops.groupCarrier) this.recoverGroupIntents().catch(() => {});
      if (this.connected && ops.some(o => o.s === "convs" || o.s === "kv" && o.k.startsWith("group/") && o.v?.context || o.s === "inbox" && o.v?.sub === "event")) {
        this.notifyState().then(st => st.enabled ? this.syncNotify() : undefined).catch(() => {});
      }
      if(ops.readSync)this.syncReadMarks().catch(()=>{});
      if(ops.rootSync){this.syncRoots().catch(()=>{});this.syncReadMarks().catch(()=>{});}
      if(ops.groupCarrier || ops.some(o=>o.s==="inbox"&&historySource(o.v)))this.runHistory().catch(()=>{});
      if (this.connected && ops.some((o) => o.s === "outbox")) this.flushOutbox().catch(() => {}); // history forwarded to your other devices
      if (ops.some((o) => o.s === "kv" && o.v && o.v.serve)) this.runServes().catch(() => {});
      const atts = ops.flatMap((o) => (o.s === "inbox" && o.v && o.v.conv ? (o.v.attachments || []).filter((a) => a.blob) : []));
      if (atts.length) this.keepFiles(atts).catch(() => {});
    } catch (e) {
      if (e instanceof StoreConflict) return this.admit(data, env, fromHeld, historyRecovery); // reverify; nothing committed or acknowledged
      if (e instanceof Hold) {
        if (!fromHeld) await this.hold(env, data, e.reason, e.message);
        else {
          const h = await this.store.get("held", env.id);
          if (h && (e.reason !== "proof_pending" || h.reason !== e.reason)) {
            await this.store.write([{s:"held",k:env.id,v:{ ...h, reason: e.reason, notice_archived:canArchiveHeld(e.reason) && !!h.notice_archived, detail_code:heldDiagnosticCode(e.message), ...(historyRecovery && e.reason === "proof_pending" ? {history_recovery:true} : {}) }}],[{s:"held",k:env.id,v:h}]);
            this.changed(true);
          }
        }
        return;
      }
      throw e;
    }
  }

  // Older receivers rejected some already-authorized history after an
  // assistant ended. Only inert participation/control history from a current own human key
  // is eligible for the one-time startup recovery; normal admission still
  // verifies every history/proof constraint. Never replay a live request.
  async heldHistoryChecks(data, env) {
    const checks = [], own = await this.groupRead(checks, "kv", "person"), pin = await this.groupRead(checks, "pins", env.from);
    const held = await this.groupRead(checks, "held", env.id);
    const human = (address, fingerprint) => own?.devices.some(d => d.address === address && d.fingerprint === fingerprint) && own.human_keys?.includes(fingerprint);
    if (this.revoked || own?.state !== "self" || env.to !== this.address || env.v !== wire.Version2 || env.kind !== "message" ||
        !held || !(held.reason === "invalid" || held.reason === "proof_pending" && held.history_recovery) || held.envelope !== data || !pin || pin.pending ||
        !human(this.address, this.fp) || !human(env.from, pin.fingerprint)) return null;
    try {
      const n = await wire.open(data, this.keys, this.address, await this.pubOf(pin));
      if (n.sub !== "history" || !n.replica || n.attachments.length) return null;
      wire.parseGroupRoot(n.root);
      const item = wire.parseHistory(n.body);
      if (!item.pid && !item.ref) return null;
    } catch (_) { return null; }
    return checks;
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
    if (n.receiver_route && n.receiver_route.op !== "request") return this.admitReceiverSetup(n, env, pin);
    if (n.v === wire.Version3) return this.admitControl(n, env, pin);
    if(n.sub===wire.SubInvitationSync)return this.admitInvitationSync(n,env,pin);
    if(n.sub===wire.SubReadSync)return this.admitReadSync(n,env,pin);
    if(n.sub===wire.SubRootSync)return this.admitRootSync(n,env,pin);
    if (n.v === wire.Version2 && (n.sub === wire.SubGroupProof || n.sub === wire.SubGroupContext)) return this.admitGroupCarrier(n, env, pin);
    if (n.v === wire.Version2 && [wire.SubGroupInvite,wire.SubGroupConsent,wire.SubGroupWithdrawal].includes(n.sub)) return this.admitGroupLifecycle(n, env, pin);
    const base = { send_group:n.send_group||"", id: env.id, from: env.from, kind: n.kind, body: n.body, reply_to: n.reply_to, quote:n.quote||"",topic_done:!!n.topic_done,topic:n.topic||"",topic_event:n.topic_event||null, ts:n.ts, at: this.now(), fp: pin.fingerprint, read: false,
      ...(n.receiver_route ? { receiver_route: n.receiver_route } : {}),
      status: n.status || "", // a review notice (a report from another machine) is a message with status review_notice
      attachments: n.attachments.length ? n.attachments : undefined };
    if (n.v === 1) {
      await this.checkDeviceAgent(n, env.from, pin.fingerprint);
      const row = { ...base, ...(n.agent_id ? { agent_id: n.agent_id } : {}), ...(n.target ? { target: n.target } : {}), v: 1, state: n.kind === "question" || n.kind === "task" ? "held" : "" };
      if (isNotice(row)) return this.noticeOps(await this.store.all("inbox"), row); // one card per host (client.supersedeNotices)
      return [{ s: "inbox", k: env.id, v: row }];
    }
    if (n.v === wire.Version2 && JSON.parse(n.root).kind === "group") return this.mergeSendGroupOps(await this.admitGroupTurn(n, env, pin, base),n,n.sub==="history"?wire.parseHistory(n.body).from_key:pin.fingerprint);
    return this.mergeSendGroupOps(await this.admitConv(n, env, pin, base),n,n.sub==="history"?wire.parseHistory(n.body).from_key:pin.fingerprint);
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
    const senderOwn = this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint);
    const senderPerson = senderOwn ? this.me : await this.personOf(env.from, pin);
    if (n.human) return this.admitHumanTurn(n, env, pin, base, root, senderPerson);
    const external = await this.admitExternal(n, env, pin, base, root, senderPerson);
    if (external) return external;
    const mine = wire.rootMember(root, this.me.person);
    if (!mine) throw new Hold("invalid", "this device's person is not a member");
    if (!this.me.hashes.includes(mine)) {
      await this.refreshPerson(this.me);
      if (!this.me.hashes.includes(mine)) throw new Hold("proof_pending", "this device's person record here does not follow the one the conversation names");
    }
    // The sender: a current device of a member person, as pinned here (one
    // of yours, or the other person's).
    const own = this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint);
    const sp = senderPerson;
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
    if (n.sub === wire.SubDriveSpace) await this.checkDriveSpace(n, sp, root); // the space record follows the one kept here, under its owner (sp: the sender's person, verified above)
    if (n.sub === "history") {
      if (!own) throw new Hold("invalid", "history comes only from another device of your person");
      return this.admitHistory(n, env, root, ops);
    }
    if (n.sub === "file") {
      if (!own) throw new Hold("invalid", "file requests come only from another device of your person");
      return this.admitFile(n, env, ops);
    }
    if (n.sub === "event") { // a participation record: the sending device's own, for this very conversation, signed
      let e;
      try {
        e = wire.parseEvent(n.body);
        if (n.kind !== "message" || !n.pid || e.conv !== n.conv || e.pid !== n.pid || e.author.address !== env.from || e.author.fingerprint !== pin.fingerprint) {
          throw new Error("the record is not the sending device's own, for this conversation");
        }
        await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key);
        if (e.type === "scope") { // with its invite held here, only the exact projection is admitted
          const inv = (await this.convEvents(n.conv, null, n.pid)).find(x => x.hash === e.prev && x.e.type === "invite");
          if (inv && !await wire.projects(e, inv.e)) throw new Error("scope differs from its invitation");
        }
      } catch (err) {
        throw new Hold("invalid", "participation: " + err.message);
      }
    }
    await this.checkConversationAgent(n, env.from, pin.fingerprint, conv);
    const content = [n.kind, n.body, n.reply_to, n.conv, n.sub, n.origin, n.emotion, n.target, n.pid, n.status];
    if (n.agent_id) content.push(n.agent_id); // absent fields keep legacy duplicate hashes
    if (n.receiver_route) content.push(n.receiver_route);
    const hash = wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify(content))));
    const key = pin.fingerprint + "/" + n.lid;
    const seen = await this.store.get("lids", key);
    if (seen && seen.history) {
      ops.push({ s: "inbox", k: seen.id, v: undefined }); // a copy received directly replaces the history copy
    } else if (seen) {
      if (seen.hash !== hash) throw new Hold("conflicting_duplicate", "a message with the same key and logical id but other content is stored");
      return ops; // the same message again: acknowledged, not stored twice
    }
    // A copy from another device of yours is yours: shown as sent (from
    // there), never held or run here. A request to another device's agent
    // is history here (as the core keeps it); any other question or task is
    // held for the person. This browser never runs anything.
    const request = n.kind === "question" || n.kind === "task";
    const rec = { ...base, v: 2, conv: n.conv, lid: n.lid, sub: n.sub, pid: n.pid, origin: n.origin, emotion: n.emotion, replica: n.replica,
      target: n.target || null, ...(n.agent_id ? { agent_id: n.agent_id } : {}), own, read: own || base.read,
      state: !own && request && !(n.target && n.target.address !== this.address) ? "conv_held" : "" };
    // The attention hint is the sender's claim: recorded, checked against
    // this DM's channel here, never used to route anything.
    if (env.attn && !own) rec.attn = env.chan === (await wire.notifyChannel(n.conv, this.fp)) ? "ok" : "mismatch";
    if (n.sub === wire.SubDriveSpace) rec.read = true; // a quiet record: never unread, shown or alerting
    if (rec.kind === "message" && rec.body && (await this.tombstoned(rec))) rec.body = ""; // deleted before it got here: no plain text is stored
    const forwards = own ? [] : await this.forwardStale(n, env, pin, conv || { root: n.root });
    return [...ops, { s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash } }, ...forwards.map((r) => ({ s: "outbox", k: r.id, v: r }))];
  }

  async admitExternal(n, env, pin, base, root, senderPerson) {
    if (!n.pid) return null;
    const recipientMember = !!wire.rootMember(root, this.me.person), senderMember = !!wire.rootMember(root, senderPerson.person);
    let record = null, disclosed = false;
    if (n.sub === "event") {
      try {
        record = await this.eventRecord(n.body);
        const e = record.e, own = e.author.address === env.from && e.author.fingerprint === pin.fingerprint, here = (d) => d.address === this.address && d.fingerprint === this.fp;
        if (n.kind !== "message" || e.conv !== n.conv || e.pid !== n.pid) throw new Error("event is not this sending device's own");
        // client.disclosedHumanEvent: an original member device shares another
        // participation's public scope, acceptance or end with an accepted
        // guest (its own when it hosts that assistant; see below).
        disclosed = !recipientMember && senderMember && root.kind === "dm" && (e.type === "scope" && !!e.host && !here(e.host) || ["accept", "dismiss"].includes(e.type) && !here(e.author));
        if (!own && !disclosed) throw new Error("event is not this sending device's own");
        if (!disclosed) await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key);
      } catch (e) { throw new Hold("invalid", e.message); }
    }
    const c = await this.store.get("convs", n.conv) || { id: n.conv, root: n.root, peer: root.members.find((m) => m.person !== this.me.person)?.person, created: root.created, creator: root.creator.address };
    const events = [...await this.convEvents(n.conv), ...(record ? [record] : [])];
    let members = await this.dmMembers(c, events), info = this.resolveAgent(n.pid, events, members);
    if (disclosed && record.e.type !== "scope") {
      const e = record.e, own = e.author.address === env.from && e.author.fingerprint === pin.fingerprint, byHost = info.host && e.author.address === info.host.address && e.author.fingerprint === info.host.fingerprint;
      if (own && (info.role === "human" || e.type === "accept" && !byHost) || info.host && info.host.address === this.address && info.host.fingerprint === this.fp) disclosed = false; // a member's own human end, or this guest's own participation: its existing path
      if (!disclosed && !own) throw new Hold("invalid", "event is not this sending device's own");
      if (!disclosed) try { await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key); } catch (err) { throw new Hold("invalid", err.message); }
    }
    const hostOutside = record?.e.host && !wire.rootMember(root, record.e.host.person);
    if (recipientMember && senderMember && n.sub !== "excerpt" && !hostOutside && !info.external) return null;
    if (record?.e.host) {
      const h = record.e.host, hostPin = await this.sendKey(h.address);
      if (hostPin.fingerprint !== h.fingerprint) throw new Hold("invalid", "invite host key differs from its pinned device");
      const p = await this.personOf(h.address, hostPin);
      if (p.person !== h.person) throw new Hold("invalid", "invite host person differs from its pinned proof");
    }
    // Verify the unchanged human room's root and both pinned member chains.
    for (const mem of root.members) {
      const p = mem.person === this.me.person ? this.me : await this.pinChain(mem.person);
      if (p.state === "conflict") throw new Hold("identity_conflict", "DM member proof conflicts");
      if (!p.hashes.includes(mem.roster)) throw new Hold("proof_pending", "root member chain is not pinned");
    }
    const creator = root.creator.person === this.me.person ? this.me : await this.store.get("persons", root.creator.person);
    const dev = creator?.known.find((d) => d.address === root.creator.address && d.fingerprint === root.creator.fingerprint);
    if (!dev || !creator.hashes.includes(root.creator.roster)) throw new Hold("proof_pending", "root creator proof is missing");
    try { await wire.verifyRoot(root, (await wire.parsePublic(JSON.parse(dev.json))).sign_key); }
    catch (e) { throw new Hold("invalid", e.message); }
    const exists = await this.store.get("convs", n.conv);
    if (!exists && (record?.e.type !== "invite" || !senderMember)) throw new Hold("proof_pending", "outside host has no verified invitation root");
    const checks = this.humanEndEvent(n, info) || disclosed ? [] : null;
    if (checks) {
      const stored = await this.groupRead(checks, "convs", n.conv), currentPin = await this.groupRead(checks, "pins", env.from);
      if (!stored || stored.root !== c.root || !currentPin || currentPin.pending || currentPin.fingerprint !== pin.fingerprint) throw new StoreConflict();
      events.splice(0, events.length, ...await this.convEvents(n.conv, checks), ...(record ? [record] : []));
    }
    members = await this.dmMembers(c, events, checks); info = this.resolveAgent(n.pid, events, members);
    if (!info.invite) throw new Hold("proof_pending", "outside traffic has no unambiguous invitation proof yet");
    if (disclosed) await this.admitDisclosed(record, info, members, env.from, pin.fingerprint, c, checks);
    else if (!recipientMember && (info.host?.person !== this.me.person || info.host.address !== this.address || info.host.fingerprint !== this.fp) && !(this.humanEndEvent(n, info) && await this.humanEndReader(c, this.address, this.fp, checks))) throw new Hold("invalid", "outside recipient differs from the exact invited host");
    if (!disclosed) this.externalRole(n, info, members, env.from, pin.fingerprint);
    if (record?.e.type === "invite" && record.hash !== info.invite) throw new Hold("invalid", "invite differs from the counted invitation");
    if (record?.e.type === "scope" && (record.e.prev !== info.invite || !info.scope)) throw new Hold("invalid", "scope differs from its counted invitation");
    await this.checkExternalReply(n, info, members);
    const content = [n.kind, n.body, n.reply_to, n.conv, n.sub, n.origin, n.emotion, n.target, n.pid, n.status, n.agent_id || "", n.replica, n.attachments];
    if (n.receiver_route) content.push(n.receiver_route);
    const hash = wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify(content)))), key = pin.fingerprint + "/" + n.lid;
    const seen = await this.store.get("lids", key);
    if (seen && !seen.history) {
      if (seen.hash !== hash) throw new Hold("conflicting_duplicate", "external logical copy differs from stored content");
      const ops = []; if (checks) ops.checks = checks; return ops;
    }
    let attachments = n.attachments;
    if (n.sub === "excerpt") {
      const h = wire.parseGrantedExcerpt(n, info), used = new Set();
      attachments = h.attachments.map((a) => {
        const i = n.attachments.findIndex((f, i) => !used.has(i) && f.name === a.name && f.size === a.size && f.sha256 === a.sha256);
        if (i >= 0) { used.add(i); return n.attachments[i]; }
        return { ...a, blob: null, availability: "unavailable", detail: "Selected bytes were not available at the forwarder." };
      });
    }
    const own = senderPerson.person === this.me.person;
    const rec = { ...base, v: 2, conv: n.conv, lid: n.lid, sub: n.sub, pid: n.pid, origin: n.origin, emotion: n.emotion, target: n.target || null,
      ...(n.agent_id ? { agent_id: n.agent_id } : {}), replica: n.replica, own, read: own || !!base.read, attachments,
      state: !own && !n.sub && ["question", "task"].includes(n.kind) && n.target?.address === this.address ? "conv_held" : "" };
    const ops = [...(!exists ? [{ s: "convs", k: n.conv, v: c }] : []), ...(seen?.history ? [{ s: "inbox", k: seen.id, v: undefined }] : []),
      { s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash } }];
    if (checks) ops.checks = checks; return ops;
  }

  // itemOf is a stored message as a history item (manifests only).
  itemOf(m, sentHere) {
    return { v:1,from: sentHere ? this.address : m.from, from_key: sentHere ? this.fp : m.fp||m.claimed_key, id: m.id, lid: m.lid, ts: m.ts || (m.envelope?wire.parseEnvelope(m.envelope).ts:Math.floor(m.at / 1000)), at: m.at,
      kind: m.kind, body: m.body || "", reply_to: m.reply_to || "", quote:m.quote||"",send_group:m.send_group_conflict?"":m.send_group||"",topic:m.topic||"",topic_event:m.topic_event||null,topic_done:!!m.topic_done,status: m.status || "", sub: m.sub || "", origin: m.origin || "",
      emotion: m.emotion || "", target: m.target || null, pid: m.pid || "", ...(m.agent_id ? { agent_id: m.agent_id } : {}), ref: m.ref || null, ...(m.group_admission?{group_admission:m.group_admission}:{}), ...(m.group_history?{group_history:m.group_history}:{}), attachments: (m.attachments || []).map((a) => ({ name: a.name, size: a.size, sha256: a.sha256 })), ...(m.receiver_route ? { receiver_route: m.receiver_route } : {}), ...(m.human ? { human: m.human } : {}) };
  }

  // ownCopy seals a message of sub ("history", or "file": a file request
  // or offer) for your device dev (address, fingerprint, json) in
  // conversation c, with sealed files (attachment and ciphertext) if any:
  // kept in the outbox and sent as any copy is, never a message here (aside).
  async ownCopy(dev, c, sub, body, files = []) {
    const recipient = await wire.parsePublic(JSON.parse(dev.json));
    const id = wire.newID(), lid = wire.newID();
    const attachments = files.map((f) => f.attachment);
    const envelope = await wire.seal({ v: wire.Version2, id, from: this.address, to: dev.address, ts: Math.floor(this.now() / 1000), kind: "message",
      body, conv: c.id, lid, root: c.root, replica: true, sub, attachments }, this.keys, recipient);
    return { id, conv: c.id, lid, kind: "message", sub, body, at: this.now(), to: dev.address, own: true, aside: true, envelope,
      ...(wire.agentRequirement({ sub, body }) ? { required_cap: wire.agentRequirement({ sub, body }) } : {}),
      ...(wire.receiverRequirement({ sub, body }) ? { required_receiver_cap: true } : {}),
      attachments: attachments.length ? attachments : undefined, files: files.length ? files.map((f) => ({ ...f, uploaded: false })) : undefined, state: "queued", detail: "" };
  }

  async groupParticipationHistorySource(c,item,checks=[]) {
    try{await this.groupParticipationHistoryCheck(c.id,item,{address:this.address,fingerprint:this.fp},checks);}
    catch(e){if(item.group_history||e.reason!=="proof_pending"&&!e.historicalEpoch||!item.pid&&item.sub!==wire.SubStatus)throw e;item={...item,group_history:await this.makeGroupHistoryWitness(c,item,checks)};await this.groupParticipationHistoryCheck(c.id,item,{address:this.address,fingerprint:this.fp},checks);}
    return item;
  }

  async historyCopy(dev, c, item, checks=[]) {
    if(c.kind==="group") {
      const {packet}=await this.groupTurnEvidence(c.id,checks),members=await this.dmMembers(c,null,checks),stamp=members.epochs.get(this.fp);
      if(!members.get(this.me.person)?.devices.some(d=>d.address===dev.address&&d.fingerprint===dev.fingerprint))throw Error("Group history goes only to exact current own linked device.");
      const source=await this.groupRead(checks,item.from===this.address&&item.from_key===this.fp?"outbox":"inbox",item.id);
      if(!source||source.conv!==c.id)throw Error("Original group history source is unavailable.");
      const original=this.itemOf(source,!!source.to);
      if(wire.historyJSON({...original,group_admission:undefined,group_history:undefined,send_group:undefined})!==wire.historyJSON({...item,group_admission:undefined,group_history:undefined,send_group:undefined}))throw Error("Group history differs from its exact source row.");
      item={...item,send_group:original.send_group,group_admission:source.to?stamp:source.group_admission};
      if(item.group_admission!==stamp)throw Error("Historical group source admission changed.");
      if(item.pid||item.ref)item=await this.groupParticipationHistorySource(c,item,checks);
      if(item.group_history&&!await this.ownHistoryAuthority(dev,checks))throw Error("Historical witness requires current own human readers and unchanged pins.");
      item={...item,send_group:await this.sendGroupCopy(dev.address,await this.pinned(dev.address),item.send_group)};
      const rec=await this.groupDataCopy(packet,"history",wire.historyJSON(item),dev);rec.group_history=true;rec.send_group_wire=!!item.send_group;return rec;
    }
    if (!wire.rootMember(wire.parseRoot(c.root), this.me?.person)) throw new Error("An outside host cannot forward room history to sibling devices.");
    item={...item,send_group:await this.sendGroupCopy(dev.address,await this.pinned(dev.address),item.send_group)};
    const rec = await this.ownCopy(dev, c, "history", wire.historyJSON(item));
    rec.send_group_wire=!!item.send_group;
    if (item.pid) {
      const events = await this.convEvents(c.id);
      if (item.sub === "event") events.push(await this.eventRecord(item.body));
      const info = this.resolveAgent(item.pid, events, await this.dmMembers(c, events));
      if (info.role === "human") rec.required_cap = wire.CapHumanParticipation;
      else if (info.external) rec.required_cap = wire.CapExternalParticipation;
    }
    if (item.human) rec.required_cap = wire.CapHumanParticipation;
    return rec;
  }

  // forwardStale forwards, as history, a message the other person sent to
  // an older roster step of yours (fan) to your devices that step lacked:
  // copies its sender could not know to send (every such device forwards;
  // duplicates are stored once there).
  async forwardStale(n, env, pin, c) {
    const f = (n.fan || []).find((x) => x.person === this.me.person);
    if (!f || f.roster === this.me.hash) return [];
    const old = (this.me.steps || []).find((st) => st.hash === f.roster);
    if (!old) return []; // a step not in your chain: nothing to go by
    const item = this.itemOf({ ...n, id: env.id, from: env.from, fp: pin.fingerprint, at: this.now(), ts: env.ts || n.ts }, false);
    const out = [];
    for (const d of this.me.devices) {
      if (d.address === this.address || d.address === env.from || old.devices.includes(d.address + "|" + d.fingerprint)) continue;
      out.push(await this.historyCopy(d, { id: n.conv, root: c.root }, item));
    }
    return out;
  }

  // admitHistory takes a history item from another device of your person:
  // the message it forwards, sent under a key the forwarder names (its
  // word, not a signature here), from a device of a member person (one
  // that was or is theirs). It never runs, alerts or is held; a copy of
  // the same message received directly replaces it, and one received
  // before it stays.
  async admitHistory(n, env, root, ops) {
    const checks = ops.checks || [];
    let item;
    try {
      item = wire.parseHistory(n.body);
    } catch (e) {
      throw new Hold("invalid", "a malformed history item");
    }
    const hc = { id: n.conv, root: n.root, peer: root.members.find((m) => m.person !== this.me.person)?.person };
    const historyEvents = await this.convEvents(n.conv);
    if (item.sub === "event") {
      const r = await this.eventRecord(item.body);
      if (r.e.host && !wire.rootMember(root, r.e.host.person)) {
        const pin = await this.sendKey(r.e.host.address);
        if (pin.fingerprint !== r.e.host.fingerprint || (await this.personOf(r.e.host.address, pin)).person !== r.e.host.person) throw new Hold("invalid", "history invitation host differs from pinned proof");
      }
      historyEvents.push(r);
    }
    let humanEvidence = null;
    if (item.human) {
      for (const e of item.human.proof.filter(e => e.type === "scope")) {
        const hp = await this.sendKey(e.host.address);
        if (hp.fingerprint !== e.host.fingerprint || (await this.personOf(e.host.address, hp)).person !== e.host.person) throw new Hold("invalid", "Historical human host differs from pinned proof.");
      }
      humanEvidence = await this.humanEvidence(hc, item.human, checks);
      const scope = item.human.author_pid && humanEvidence.scopes.get(item.human.author_pid);
      const author = scope ? scope.host.address === item.from && scope.host.fingerprint === item.from_key : [...humanEvidence.members.values()].some(p => p.devices.some(d => d.address === item.from && d.fingerprint === item.from_key));
      if (!author) throw new Hold("invalid", "Historical human attribution differs from captured consent.");
      // Already-authorized own-member history is inert even after end; this
      // never admits guest sibling history or grants a fresh live audience.
    }
    const historyMembers = humanEvidence?.members || await this.dmMembers(hc, historyEvents);
    const externalInfo = item.pid && (humanEvidence?.scopes.get(item.pid) || this.resolveAgent(item.pid, historyEvents, historyMembers));
    let owner = null, dev = null;
    for (let pass = 0; pass < 2 && !owner; pass++) {
      for (const m of root.members) {
        let p = m.person === this.me.person ? this.me : await this.store.get("persons", m.person);
        if (pass === 1) p = p ? await this.refreshPerson(p) : await this.pinChain(m.person);
        const d = p && p.known.find((k) => k.address === item.from && k.fingerprint === item.from_key);
        if (d) { owner = p; dev = d; break; }
      }
    }
    if (!owner && externalInfo?.external) {
      owner = historyMembers.hosts.get(externalInfo.host.person);
      dev = owner?.known.find((k) => k.address === item.from && k.fingerprint === item.from_key);
      if (!dev) owner = null;
    }
    if (!owner && item.pid && (item.agent_id || item.sub === "event" || wire.historyAssistantReaction(item))) throw new Hold("proof_pending", "history outside host has no invitation proof yet");
    if (!owner) throw new Hold("invalid", "the history item's sender is no device of a member of this conversation");
    if (owner.state === "conflict") throw new Hold("identity_conflict", "this person's record conflicts with the one kept here; it is frozen");
    if (item.sub === "event") { // the signed event itself, verified under its author's key
      try {
        const e = wire.parseEvent(item.body);
        if (!item.pid || e.conv !== n.conv || e.pid !== item.pid || e.author.address !== item.from || e.author.fingerprint !== item.from_key) {
          throw new Error("the record is not its sender's own, for this conversation");
        }
        await wire.verifyEvent(e, (await wire.parsePublic(JSON.parse(dev.json))).sign_key);
      } catch (err) {
        throw new Hold("invalid", "participation: " + err.message);
      }
    }
    const assistant = wire.historyAssistantReaction(item);
    if (assistant) { // the assistant's own, bound as its direct copy is (client.assistantHistoryCheck)
      this.assistantBinding(externalInfo, item, item.from, item.from_key, false);
      if (item.human) await this.humanReactionRequest(n.conv, item, externalInfo); // to a captured audience: its stored request, never broader
      else await this.assistantRequest(n.conv, item.pid, item.ref, item.agent_id, externalInfo, historyMembers);
    } else if (externalInfo?.external && !item.human) {
      // Historical addressed turns are immutable audit/history, never new
      // delivery or execution after dismissal. Their exact author/target
      // and accepted original request remain checked independently.
      const historical = !item.sub && ["question", "task", "answer", "result"].includes(item.kind);
      this.externalRole({ ...item, conv: n.conv, replica: item.sub === "excerpt" }, historical ? { ...externalInfo, state: "active", held: 0 } : externalInfo, historyMembers, item.from, item.from_key);
      await this.checkExternalReply({ ...item, conv: n.conv }, externalInfo, historyMembers);
    }
    if (!assistant) await this.checkConversationAgent({ ...item, conv: n.conv }, item.from, item.from_key, { id: n.conv, root: n.root, peer: root.members.find((m) => m.person !== this.me.person)?.person }, true);
    const key = item.from_key + "/" + item.lid;
    if (item.from_key === this.fp || (await this.store.get("lids", key))) return ops; // sent here, or known: received directly, or as history before
    const mine = owner === this.me;
    if (item.sub === wire.SubDriveSpace) await this.checkDriveSpace({ conv: n.conv, body: item.body }, owner, root); // held until its predecessor is here
    if (wire.isControl(item.sub)) { // a control travels as history with its target reference; resolved when shown
      if (!item.ref) throw new Hold("invalid", "a control without its reference");
      try { wire.parseControl(item.sub, item.body); } catch (e) { throw new Hold("invalid", e.message); }
      const ctl = { id: item.id, v: 3, control: true, from: item.from, fp: item.from_key, kind: "message", sub: item.sub, body: item.body, ref: item.ref,
        at: item.at || item.ts * 1000, read: true, conv: n.conv, lid: item.lid, replica: true, own: mine && !assistant, person: assistant ? "" : owner.person, history: true, synced_from: env.from,
        ...(assistant ? { pid: item.pid, ...(item.agent_id ? { agent_id: item.agent_id } : {}), ...(item.human ? { human: item.human } : {}) } : {}) };
      if (item.sub === wire.SubRevision && (await this.refTombstoned(n.conv, item.ref))) ctl.body = ""; // a revision after the deletion keeps no text
      const extra = [];
      if (item.sub === wire.SubRetraction && owner.person === (await this.personOfFp(item.ref.fingerprint))) { // the author's own retraction, carried as history
        const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox");
        const t = inbox.find((m) => m.conv === n.conv && !m.control && !m.aside && m.lid === item.ref.id && m.fp === item.ref.fingerprint)
          || outbox.find((m) => m.conv === n.conv && !m.control && !m.aside && m.lid === item.ref.id && this.fp === item.ref.fingerprint);
        if (t) extra.push(...(await this.dropCached(t, ctl)), ...(await this.blankRetracted(t, t.fp ? "inbox" : "outbox", checks)));
      }
      const result = [...ops, { s: "inbox", k: item.id, v: ctl }, { s: "lids", k: key, v: { id: item.id, hash: "", history: true } }, ...extra];
      result.checks = checks; return result;
    }
    const rec = { id: item.id, from: item.from, kind: item.kind, body: item.body, quote:item.quote||"",topic:item.topic||"",topic_event:item.topic_event||null,topic_done:!!item.topic_done,reply_to: item.reply_to,ts:item.ts, at: item.at || item.ts * 1000, fp: item.from_key,
      read: true, v: 2, conv: n.conv, lid: item.lid, sub: item.sub, pid: item.pid, origin: item.origin, emotion: item.emotion, replica: true,
      target: item.target, ...(item.agent_id ? { agent_id: item.agent_id } : {}), ...(item.receiver_route ? { receiver_route: item.receiver_route } : {}), ...(item.human ? { human: item.human } : {}), own: mine, history: true, synced_from: env.from, state: "",
      attachments: item.attachments.length ? item.attachments.map((a) => ({ blob: null, name: a.name, size: a.size, sha256: a.sha256 })) : undefined };
    if (rec.kind === "message" && rec.body && (await this.tombstoned(rec))) rec.body = ""; // its author's retraction is already here
    return [...ops, { s: "inbox", k: item.id, v: rec }, { s: "lids", k: key, v: { id: item.id, hash: "", history: true } }];
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
      const own = await this.store.get("kv", "person");
      const recoverHistory = this.heldHistoryRecovery && own?.state === "self" && own.human_keys?.includes(this.fp) && own.devices.some(d => d.address === this.address && d.fingerprint === this.fp);
      const recovered = new Set(); // each invalid row at most once, even when admitted proof adds a pass
      let pos = "";
      for (;;) {
        const page = await this.store.after("held", pos, heldPage);
        for (const h of page) {
          // Missing context keeps the recovery guard through later ordinary proof retries.
          const historyRecovery = h.reason === "proof_pending" && h.history_recovery || recoverHistory && h.reason === "invalid" && !recovered.has(h.id);
          if (h.reason !== "proof_pending" && !historyRecovery) continue;
          if (historyRecovery) recovered.add(h.id);
          try {
            let env;
            try { env = wire.parseEnvelope(h.envelope); } catch (e) { if (historyRecovery) continue; throw e; }
            await this.admit(h.envelope, env, true, historyRecovery);
          } catch (e) {
            return;
          }
        }
        if (page.length === heldPage) pos = page[page.length - 1].id;
        else if (this.retryAgain) [this.retryAgain, pos] = [false, ""];
        else {
          if (recoverHistory) {
            try {
              await this.store.write([{s:"kv",k:"held-group-history-recovery-v1",v:true}],[{s:"kv",k:"person",v:own}]);
            } catch (e) { if (e instanceof StoreConflict) return; throw e; }
            this.heldHistoryRecovery = false;
          }
          return;
        }
      }
    } finally {
      this.retrying = null;
      this.discloseHumanAudience(); // group ends also follow fresh membership evidence
    }
  }

  // flushReceipts sends every stored receipt the server has not taken yet,
  // receiptWidth at a time: one round trip after another made a phone
  // opened after hours take minutes to catch up (MEL-546). One pass runs at
  // a time; a call during it makes another pass after it, so a receipt
  // stored meanwhile is sent. A receipt is removed only while it is still
  // the one sent.
  flushReceipts() {
    this.receiptsAgain = true;
    if (!this.receiptsRun) this.receiptsRun = this.receiptPasses().finally(() => { this.receiptsRun = null; });
    return this.receiptsRun;
  }

  async receiptPasses() {
    while (this.receiptsAgain) {
      this.receiptsAgain = false;
      const rows = await this.store.all("receipts"), sent = [];
      let next = 0, kept = false;
      const send = async () => {
        while (!kept && next < rows.length) {
          const r = rows[next++];
          try {
            await this.call("POST", "/v1/messages/" + r.id + "/ack", { state: r.state });
          } catch (e) {
            if (!(e instanceof HubError && e.status === 404)) { kept = true; return; } // kept; sent again later
          }
          sent.push(r);
        }
      };
      await Promise.all(Array.from({ length: Math.min(receiptWidth, rows.length) }, send));
      if (sent.length) {
        try {
          await this.store.write(sent.map((r) => ({ s: "receipts", k: r.id, v: undefined })), sent.map((r) => ({ s: "receipts", k: r.id, v: r })));
        } catch (e) {
          if (!(e instanceof StoreConflict)) throw e;
          for (const r of sent) { // one changed meanwhile: the others go, it is sent again
            try { await this.store.write([{ s: "receipts", k: r.id, v: undefined }], [{ s: "receipts", k: r.id, v: r }]); }
            catch (err) { if (!(err instanceof StoreConflict)) throw err; this.receiptsAgain = true; }
          }
        }
      }
      if (kept) return;
    }
  }

  // ---- live human typing: memory only, never the message/job paths
  typingScopeKey(s) { return (s.conv || "") + "\0" + (s.peer || "") + "\0" + (s.thread || ""); }

  async typingPreferences() {
    const saved = (await this.store.get("kv", "typing-preferences")) || {};
    const person = !!this.me; // the explicit, pinned self person; never labels/legacy traffic
    return { send: typeof saved.send === "boolean" ? saved.send : person, show: typeof saved.show === "boolean" ? saved.show : person };
  }
  async setTypingPreferences(value) {
    if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).some((k) => k !== "send" && k !== "show") ||
        [value.send, value.show].some((v) => v != null && typeof v !== "boolean")) throw new Error("invalid typing preferences");
    const p = { send: !!value.send, show: !!value.show };
    await put(this.store, "kv", "typing-preferences", p); // only preferences persist
    if (!p.show) for (const v of this.typing.seen.values()) v.active = false;
    await this.refreshTyping(false);
    this.changed();
    return p;
  }
  resetTyping(connected = false, supported = this.typing.supported) {
    const t = this.typing, visible = t.visible;
    t.abort.abort(); t.abort = new AbortController();
    t.generation++; t.connected = connected; t.supported = supported;
    t.seen.clear(); t.replay.clear(); t.sent.clear(); t.visible = "[]";
    if (t.timer !== null) clearTimeout(t.timer);
    t.timer = null;
    if (visible !== "[]") this.changed();
  }
  async typingKey(address) {
    if (!this.members.current || !this.typing.connected || this.revoked) return null;
    const matches = this.members.list.filter((m) => m.address === address);
    if (matches.length !== 1 || matches[0].presence === "offline") return null;
    const pin = await this.store.get("pins", address);
    if (!pin || pin.pending) return null; // no discovery, pinning, refresh or trust for a signal
    const pub = await this.pubOf(pin);
    if (pub.address !== address || await wire.fingerprint(pub) !== pin.fingerprint) return null;
    const people = [this.me, ...(await this.store.all("persons"))].filter(Boolean);
    const known = people.find((p) => (p.known || p.devices || []).some((d) => d.address === address));
    const person = people.find((p) => (p.devices || []).some((d) => d.address === address && d.fingerprint === pin.fingerprint));
    if (known && (!person || known.state === "conflict") || person && person.state === "conflict") return null;
    const ref = matches[0].person;
    if (ref && (!person || ref.id !== person.person || ref.hash !== person.hash)) return null;
    return { pub, pin, person };
  }
  async typingDM(scope) {
    if (!scope.conv || !this.me || this.me.state === "conflict" || !(this.me.devices || []).some((d) => d.address === this.address && d.fingerprint === this.fp)) return null;
    const record = await this.store.get("convs", scope.conv) || await this.groupRecord(scope.conv);
    if (!record) return null;
    if (record.kind === "group") {
      try { const packet=await this.groupCurrent(scope.conv);return {...packet.root,current:packet}; } catch { return null; }
    }
    let root;
    try { root = wire.parseRoot(record.root); } catch (e) { return null; } // v2 DM only; groups need effective-members resolver
    if (await wire.rootID(root) !== scope.conv) return null;
    if ((this.me.hashes || []).includes(wire.rootMember(root, this.me.person))) return { ...root, record };
    // client.typingGuest: this exact device is an accepted human guest here.
    return root.kind === "dm" && await this.humanEndReader(record, this.address, this.fp) ? { ...root, record, guest: true } : null;
  }
  async typingScopeAllows(scope, address, key) {
    const current = await this.typingKey(address);
    if (!key || !current || current.pin.fingerprint !== key.pin.fingerprint || (current.person?.person || "") !== (key.person?.person || "")) return false;
    if (!scope.conv) return scope.peer === address && (await this.threadSummaries()).some((t) => t.id === scope.thread && t.peer === address && !t.notice_only);
    const root = await this.typingDM(scope);
    if (root?.kind === "group") {
      const member=(await wire.effectiveGroupMembers(root.current.state,root.current.withdrawals)).find(m=>m.person===current.person?.person);
      return !!(member && current.person.hashes.includes(member.roster) && current.person.devices.some(d=>d.address===address&&d.fingerprint===current.pin.fingerprint));
    }
    if (!root) return false;
    if (current.person && (current.person.hashes || []).includes(wire.rootMember(root, current.person.person))) return true;
    return !!(root.record && await this.humanEndReader(root.record, address, current.pin.fingerprint)); // an exact accepted human guest key
  }
  async typingTargets(scope) {
    if (!scope.conv) {
      const key = await this.typingKey(scope.peer);
      if (await this.typingScopeAllows(scope, scope.peer, key)) return [key];
    } else {
      const root = await this.typingDM(scope);
      if (root?.kind === "group") {
        const out=[];
        for(const member of await wire.effectiveGroupMembers(root.current.state,root.current.withdrawals)) {
          if(member.person===this.me.person)continue;
          const person=await this.store.get("persons",member.person);
          for(const dev of person?.devices || []) {const key=await this.typingKey(dev.address);if(await this.typingScopeAllows(scope,dev.address,key))out.push(key);}
        }
        return out;
      }
      if (root) {
        // client.typingTargets: the member devices (a member's own person
        // excluded, as before) and each exact accepted human guest key.
        const out = [], seen = new Set([this.address]);
        const add = async (address) => { if (seen.has(address)) return; seen.add(address); const key = await this.typingKey(address); if (await this.typingScopeAllows(scope, address, key)) out.push(key); };
        let known = false;
        for (const m of root.members) {
          if (!root.guest && m.person === this.me.person) continue;
          const person = await this.store.get("persons", m.person);
          if (!person || person.state === "conflict" || !(person.hashes || []).includes(m.roster)) continue;
          known = true;
          for (const dev of person.devices) await add(dev.address);
        }
        for (const p of (await this.participationsOf(root.record)).filter(p => p.role === "human" && p.state === "active" && !p.held && p.host)) await add(p.host.address);
        if (known) return out;
      }
    }
    throw new Error("typing requires a known exact conversation or peer/thread with verified current membership");
  }
  pruneTyping() {
    const now = this.now();
    for (const [k, v] of this.typing.seen) if (v.expires <= now) this.typing.seen.delete(k);
    for (const [k, expires] of this.typing.replay) if (expires <= now) this.typing.replay.delete(k);
    for (const [k, v] of this.typing.sent) if (now - v.at > wire.SignalTTL) this.typing.sent.delete(k);
  }
  async typingRows(scope) {
    if (!this.connected || !this.typing.connected || !this.members.current || !(await this.typingPreferences()).show) return [];
    const out = new Map();
    for (const v of this.typing.seen.values()) {
      if (!v.active || v.expires <= this.now() || scope && this.typingScopeKey(scope) !== this.typingScopeKey(v.scope)) continue;
      const key = await this.typingKey(v.entry.address);
      if (!key || key.pin.fingerprint !== v.fp || !(await this.typingScopeAllows(v.scope, v.entry.address, key))) continue;
      const id = this.typingScopeKey(v.scope) + "\0" + (v.entry.person || v.entry.address), prev = out.get(id);
      if (!prev || prev.expires < v.expires) out.set(id, v);
    }
    return [...out.values()].sort((a, b) => a.entry.address.localeCompare(b.entry.address));
  }
  armTyping() {
    const t = this.typing;
    if (t.timer !== null) clearTimeout(t.timer);
    t.timer = null;
    if (!t.connected || !t.seen.size) return;
    const generation = t.generation, next = Math.min(...[...t.seen.values()].map((v) => v.expires));
    t.timer = setTimeout(() => {
      t.timer = null;
      if (generation === t.generation) return this.refreshTyping().catch(() => {});
    }, Math.max(0, next - this.now())); // one local expiry timer, no periodic request
  }
  async refreshTyping(notify = true) {
    const generation = this.typing.generation;
    this.pruneTyping();
    for (const v of this.typing.seen.values()) {
      if (!v.active) continue;
      const key = await this.typingKey(v.entry.address);
      if (!key || key.pin.fingerprint !== v.fp || !await this.typingScopeAllows(v.scope, v.entry.address, key)) v.active = false;
    }
    const rows = await this.typingRows();
    if (generation !== this.typing.generation) return;
    const visible = JSON.stringify(rows.map((v) => [this.typingScopeKey(v.scope), v.entry.person || "", v.entry.address, v.entry.label]));
    const changed = this.typing.visible !== visible;
    this.typing.visible = visible;
    this.armTyping();
    if (changed && notify) this.changed(); // renewal/read/tombstone expiry makes no change storm
  }
  async typingView(value) {
    const scope = wire.typingScope(value, true), preferences = await this.typingPreferences();
    const rows = Object.keys(scope).length ? await this.typingRows(scope) : [];
    return { scope, preferences, supported: this.typing.supported, current: this.connected && this.typing.connected && this.members.current,
      entries: rows.map((v) => ({ ...v.entry, expires: iso(v.expires) })) };
  }
  async onTyping(raw) {
    const t = this.typing, generation = t.generation, session = this.session, realm = this.realm;
    if (!this.connected || !t.connected || !t.supported || !(await this.typingPreferences()).show || !wire.validID(realm || "")) return;
    try {
      const signal = wire.parseSignal(raw);
      if (signal.session !== session) return;
      const key = await this.typingKey(signal.from);
      if (!key) return;
      const p = await wire.openTyping(raw, this.keys, this.address, key.pub, realm, this.now());
      const scope = wire.typingScope(p.conv ? { conv: p.conv } : { peer: p.from, thread: p.thread });
      if (!(await this.typingScopeAllows(scope, p.from, key)) || key.person && this.me && key.person.person === this.me.person) return;
      const current = await this.typingKey(p.from);
      if (generation !== t.generation || session !== this.session || realm !== this.realm || !t.connected || !current || current.pin.fingerprint !== key.pin.fingerprint || (current.person?.person || "") !== (key.person?.person || "") || p.ts <= this.now() - wire.SignalTTL || !(await this.typingScopeAllows(scope,p.from,current)) || !(await this.typingPreferences()).show) return;
      this.pruneTyping();
      const replay = p.from + "\0" + p.id;
      if (t.replay.has(replay) || t.replay.size >= 2048) return; // never evict live proof for room
      t.replay.set(replay, p.ts + wire.SignalTTL);
      const id = this.typingScopeKey(scope) + "\0" + key.pin.fingerprint, old = t.seen.get(id);
      if (old && (old.ts > p.ts || old.ts === p.ts && (!old.active || p.active)) || !old && t.seen.size >= 256) return;
      const expires = Math.min(p.ts + wire.SignalTTL, this.now() + wire.SignalTTL);
      t.seen.set(id, { ts: p.ts, active: p.active, expires, fp: key.pin.fingerprint, scope,
        entry: { ...(key.person ? { person: key.person.person } : {}), address: p.from, label: key.person ? key.person.label : p.from } });
      await this.refreshTyping();
    } catch (e) { /* live hints fail closed: never held, fetched, stored, acknowledged or executed */ }
  }
  async sendTyping(value, active) {
    const result = { submitted: 0, skipped: 0, throttled: false }, t = this.typing;
    if (!(await this.typingPreferences()).send || !this.connected || !t.connected || !t.supported) return result;
    const scope = wire.typingScope(value), realm = this.realm, generation = t.generation, session = this.session, signal = t.abort.signal;
    if (!wire.validID(realm || "")) throw new Error("typing workspace realm is unavailable");
    const targets = await this.typingTargets(scope);
    if (generation !== t.generation || !t.connected) return result;
    this.pruneTyping();
    const id = this.typingScopeKey(scope), prev = t.sent.get(id);
    if (active && prev && prev.active && this.now() - prev.at < wire.TypingThrottle) { result.throttled = true; return result; }
    if (!active && (!prev || !prev.active) || !prev && t.sent.size >= 256) return result;
    const ts = Math.max(this.now(), prev ? prev.ts + 1 : 0);
    t.sent.set(id, { at: this.now(), ts, active });
    for (const key of targets) {
      let profile;
      try { profile = await this.profile(key.pub.address); } catch (e) { result.skipped++; continue; }
      if (!profile || !profile.live || !await wire.profileSupports(profile, key.pub.address, key.pub.sign_key, wire.CapTyping)) { result.skipped++; continue; }
      for (const recipientSession of profile.sessions) {
        const current = await this.typingKey(key.pub.address);
        if (!wire.validID(recipientSession) || !current || current.pin.fingerprint !== key.pin.fingerprint || !await this.typingScopeAllows(scope, key.pub.address, current)) { result.skipped++; continue; }
        const raw = await wire.sealTyping({ v: 1, id: wire.newID(), from: this.address, to: key.pub.address, ts, session: recipientSession, realm,
          conv: scope.conv || "", thread: scope.thread || "", origin: "human", active }, this.keys, key.pub);
        const after = await this.typingKey(key.pub.address);
        if (generation !== t.generation || session !== this.session || realm !== this.realm || !t.connected || t.sent.get(id)?.ts !== ts || ts <= this.now() - wire.SignalTTL || !after || after.pin.fingerprint !== key.pin.fingerprint || !await this.typingScopeAllows(scope, key.pub.address, after) || !(await this.typingPreferences()).send) { result.skipped++; continue; }
        try { await this.call("POST", "/v1/signal", raw, { signal }); result.submitted++; } catch (e) { result.skipped++; }
      }
    }
    return result; // live fanout accepted only; no delivery, presence or work inference
  }

  // ---- the stream

  start() {
    if (this.running || this.revoked || !this.joined) return;
    this.running = true;
    this.loop();
  }

  // Managed workspace shutdown lets in-flight posts settle before its IDB
  // closes. A tab/process crash instead recovers the unchanged encrypted outbox.
  async close() {
    this.closing = true;
    this.stop();
    const settled = Promise.allSettled([this.outboxPass, this.sendCommit, ...this.posting?.values() || [], ...this.sendRequests || []].filter(Boolean));
    let timer;
    try {
      await Promise.race([settled, new Promise(resolve => { timer = setTimeout(() => { this.sendAbort.abort(); resolve(); }, 5000); })]);
      await settled;
    } finally { clearTimeout(timer); }
  }

  stop() {
    this.pictureGeneration++;
    this.pictureLoads.clear();
    for (const url of this.pictureURLs.values()) URL.revokeObjectURL(url);
    this.pictureURLs.clear();
    this.running = false;
    this.resetTyping();
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
    this.resetTyping();
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
    this.resetTyping(false, false);
    this.featureList = null;
    this.session = wire.newID();
    const path = "/v1/stream?ad=" + (await wire.sessionAd(this.keys, this.address, this.session)) + "&receipts=" + ((await this.store.get("kv","receipt-cursor")) || 0);
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
      if (j.code === "revoked" && !this.waitingLink) await this.setRevoked();
      if (this.waitingLink && (j.code === "link_refused" || j.code === "link_expired" || j.code === "revoked")) {
        await this.setLink(j.code === "link_refused" ? "refused" : "expired");
        this.stop();
      }
      return false;
    }
    if (this.waitingLink && r.headers.get("Agentnet-Members") === "1") {
      // A member's stream: approved before this stream connected.
      ctrl.abort();
      await this.finishLink();
      return true;
    }
    this.connected = true;
    this.resetTyping(!this.waitingLink, !this.waitingLink && r.headers.get("Agentnet-Signals") === "1");
    // A device waiting for approval is no member yet: its server lists it
    // nothing, which says nothing about the server.
    this.members = { ...this.members, listed: this.waitingLink ? "unknown" : r.headers.get("Agentnet-Members") === "1" ? "listed" : "not_listed", current: false };
    this.changed();
    let watchdog = setTimeout(() => ctrl.abort(), 3 * HEARTBEAT);
    const work = this.waitingLink ? Promise.resolve() : this.onConnect(); // a device waiting for approval only holds its stream
    const reader = r.body.getReader();
    const decoder = new TextDecoder();
    // Framing by eventsource-parser (the SSE grammar: LF/CR/CRLF, optional
    // space after the colon, multi-line data, comments, id/retry ignored).
    // This engine keeps the order: every event of a chunk is dispatched
    // (awaited) before the next chunk is fed, so the queue holds at most one
    // chunk's events; an incomplete frame at EOF is never dispatched. The
    // parser's buffer limit bounds an unterminated fragment (a line that
    // never ends), not a complete frame: a frame's own size is checked
    // where it is admitted (the envelope and record limits in wire.mjs).
    const queue = [];
    const parser = createParser({ onEvent: (m) => queue.push(m), onError: (err) => { if (err.type === "max-buffer-size-exceeded") queue.push({ overflow: true }); }, maxBufferSize: 8 << 20 });
    let healthy = true;
    try {
      // A linked event ends the approval stream between reads. Some readers
      // do not reject a later read after cancellation; reconnect immediately.
      while (!ctrl.signal.aborted) {
        const { value, done } = await reader.read();
        if (done) break;
        clearTimeout(watchdog);
        watchdog = setTimeout(() => ctrl.abort(), 3 * HEARTBEAT);
        parser.feed(decoder.decode(value, { stream: true }));
        while (queue.length) {
          const m = queue.shift();
          if (m.overflow) throw new Error("the stream sent a line too long to hold");
          if (m.event) await this.dispatch(m.event, m.data); // an event without a name is not one of the relay's
        }
      }
    } catch (e) {
      healthy = !(e instanceof HubError); // a message that could not be processed yet ends the connection
    } finally {
      clearTimeout(watchdog);
      this.connected = false;
      this.resetTyping();
      if (this.teamsSvc) this.teamsSvc.disconnected();
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
      const teams = this.teamsService();
      if (teams) teams.connected(f.includes("teams1"));
      // This device reads conversations and the attention hint (it never
      // alerts from the stream: its service worker shows the relay's pushes).
      if (f.includes("caps")) await this.call("PUT", "/v1/caps", wire.capsJSON(await wire.newCaps(this.keys, this.address, this.session, [wire.CapEnv2, "notify1", wire.CapPerson, wire.CapControl, wire.CapHeadless, wire.CapDrive, wire.CapAgentIdentity, wire.CapGroupHumanParticipation, wire.CapGroup, wire.CapSendGroup, wire.CapRootSync, wire.CapGroupInvitationControl, wire.CapReadSync, wire.CapOwnSyncV2, wire.CapRoom, ...(f.includes("signals1") ? [wire.CapTyping] : [])]))); // rcv1 already implied by rm1; explicit crs1 fits the 16-cap advertisement bound
      await this.publishPerson().catch(() => {});
      await this.flushReceipts();
      await this.retryHeld();
      await this.recoverGroupIntents();
      await this.flushOutbox();
      await this.retryApproved();
      await this.syncRoots();await this.syncReadMarks();await this.syncInvitations();
      await this.discloseHumanAudience(); // after a restart or reconnect: accepted guests learn each other
      this.runHistory().catch(() => {});
      this.runServes().catch(() => {});
      this.keepFiles().catch(() => {});
      await this.reconcileNotify().catch(() => {});
    } catch (e) { /* tried again on the next ping */ }
  }

  async dispatch(event, data) {
    if (this.waitingLink) {
      if (event === "linked") {
        await this.finishLink();
        this.abort.abort(); // reconnect as a member
      } else if (event === "ping") {
        let p = {};
        try { p = JSON.parse(data); } catch (e) { /* none */ }
        if (p.conn) this.call("POST", "/v1/stream/ack", { conn: p.conn }).catch(() => {});
      }
      return;
    }
    if (event === "device_admin") {
      await this.onDeviceAdminNotice(data);
    } else if (event === "receipt") {
      let r;try{r=JSON.parse(data);}catch{return;}
      if(!r||Object.keys(r).some(k=>!["id","state","seq"].includes(k))||!wire.validID(r.id)||!Number.isSafeInteger(r.seq)||r.seq<=0||!["delivered","quarantined","expired"].includes(r.state))return;
      const row=await this.store.get("outbox",r.id),cursor=await this.store.get("kv","receipt-cursor");
      const cancelledUnknown = row?.delivery_cancelled && row.handover_started!==false && !["delivered","expired","quarantined"].includes(row.state);
      const ops=[{s:"kv",k:"receipt-cursor",v:r.seq}];if(row&&(["queued","custody"].includes(row.state)||cancelledUnknown||row.state==="quarantined"&&r.state==="delivered"))ops.push({s:"outbox",k:r.id,v:{...row,state:r.state,detail:""}});
      try { await this.store.write(ops,[{s:"outbox",k:r.id,v:row},{s:"kv",k:"receipt-cursor",v:cursor}]); } catch(e) { if(e instanceof StoreConflict)return this.dispatch(event,data);throw e; }this.changed(true);
    } else if (event === "message") {
      await this.onMessage(data);
    } else if (event === "signal") {
      await this.onTyping(data);
    } else if (event === "link") {
      await this.onLinkEvent(data);
    } else if (event === "teams") { // the relay's signed team directory changed: invalidate, then verify and pin outside the reader
      const svc = this.teamsService();
      if (svc) {
        let d = null;
        try { d = JSON.parse(data); } catch (e) { d = null; }
        svc.onTeams(d);
        svc.sync().catch(() => {}).finally(() => this.changed());
      }
    } else if (event === "groups") {
      let heads;
      try { heads = JSON.parse(data); } catch (_) { return; }
      if (!Array.isArray(heads) || new TextEncoder().encode(data).length > (512 << 10)) return;
      for (const h of heads) await this.noteGroupHead(h); // independent batched hints, never a membership snapshot
      this.retryHeld().catch(() => {});
    } else if (event === "members") {
      let m;
      try { m = JSON.parse(data); } catch (e) { this.members = { ...this.members, current: false }; await this.refreshTyping(false); this.changed(); return; }
      this.members = { listed: "listed", current: true, at: this.now(), list: Array.isArray(m.members) ? m.members : [], truncated: !!m.truncated };
      await this.keepMemberFacts(m);
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
      await this.refreshTyping(false);
      this.changed();
      this.retryHeld().catch(() => {});
      // Updated signed capabilities arrive with members: retry existing
      // encrypted invitations now, through the normal pin/support checks.
      this.flushOutbox().catch(() => {});
    } else if (event === "ping") {
      let p = {};
      try { p = JSON.parse(data); } catch (e) { /* none */ }
      if (p.conn) this.call("POST", "/v1/stream/ack", { conn: p.conn }).catch(() => {});
      this.flushOutbox().catch(() => {});
      this.flushReceipts().catch(() => {});
    }
  }

  async onDeviceAdminNotice(data) {
    let n;
    try { n=JSON.parse(data); } catch { return; }
    if (!n || Object.keys(n).some(k=>!["seq","id","person","device","by","admin","at"].includes(k)) || !wire.validID(n.id) || !wire.validID(n.person) || !wire.validAddress(n.device) || !wire.validAddress(n.by) || typeof n.admin!=="boolean" || !Number.isSafeInteger(n.seq) || n.seq<=0 || !Number.isSafeInteger(n.at) || n.at<=0 || n.at>=2**40 || !this.me || this.me.person!==n.person) return;
    const key="device-admin/"+n.id, old=await this.store.get("kv",key);
    if (old) return; // dismissed notes stay as deduplication tombstones
    await this.store.write([{s:"kv",k:key,v:{...n,device_admin_notice:true,dismissed:false}}],[{s:"kv",k:key,v:old}]);
    this.changed();
  }

  async deviceAdminReview() {
    const notices=(await this.store.all("kv")).filter(n=>n?.device_admin_notice && !n.dismissed && n.person===this.me?.person);
    return notices.map(n=>({id:n.id,peer:n.by,kind:"message",why:"Your "+deviceWords(n.device)+(n.admin?" can now":" can no longer")+" change company settings — "+(n.admin?"granted":"withdrawn")+" from "+deviceWords(n.by)+" at "+new Date(n.at*1000).toLocaleTimeString([], {hour:"2-digit",minute:"2-digit",hour12:false})+".",excerpt:"",at:iso(n.at*1000),notice:true,reason:"device_admin"}));
  }

  async dismissDeviceAdminNotice(id) {
    const key="device-admin/"+id, n=await this.store.get("kv",key);
    if (!n?.device_admin_notice) return null;
    await this.store.write([{s:"kv",k:key,v:{...n,dismissed:true}}],[{s:"kv",k:key,v:n}]);
    this.changed();
    return {note:"Notice hidden on this device. Company settings access is unchanged."};
  }

  // keepMemberFacts keeps what the member list says that this device shows
  // offline too (client.keepMemberFacts): the workspace's own name, and the
  // other devices that say they run an agent. A name that is not a
  // workspace name is ignored (the list and the name known before stay).
  async keepMemberFacts(m) {
    const raw = typeof m.workspace === "string" ? m.workspace : "", name = wire.validWorkspaceName(raw);
    if ((name || !raw.trim()) && name !== this.workspaceName) {
      this.workspaceName = name;
      await put(this.store, "kv", "workspace", name || undefined);
    }
    const agents = this.members.list.filter((x) => x && x.agent === true && x.address !== this.address && wire.validAddress(x.address)).map((x) => x.address).sort();
    if (agents.join(" ") !== this.agentDevices.join(" ")) {
      this.agentDevices = agents;
      await put(this.store, "kv", "agent_devices", agents);
    }
  }

  // relayHost is the host name of this device's relay: what people see for
  // a workspace its admin has not named.
  relayHost() {
    try { return new URL(this.base).hostname; } catch (e) { return ""; }
  }

  // canAdmin reads this device's own role on its relay (the own profile's
  // self_role): one signal for every admin-only screen. Unknown is no.
  async canAdmin() {
    const [label, name] = String(this.address || "").split("/");
    if (!label || !name) return false;
    try { return (await this.call("GET", "/v1/agents/" + label + "/" + name + "/profile")).self_role === "admin"; } catch (e) { return false; }
  }

  // workspaceInfo answers GET /api/workspace (liveworkspacename.go).
  async workspaceInfo() {
    return { name: this.workspaceName, server: this.relayHost(), can_rename: await this.canAdmin() };
  }

  // renameWorkspace answers POST /api/workspace/name: the name for every
  // member, admin only; only an empty name clears it (spaces alone are no
  // name, refused as Go refuses them).
  async renameWorkspace(body) {
    if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some((k) => k !== "name") || typeof body.name !== "string") throw new Error("Enter a readable workspace name, up to 120 characters. Nothing changed.");
    const name = body.name === "" ? "" : wire.validWorkspaceName(body.name);
    if (body.name !== "" && !name) throw new Error("Enter a readable workspace name, up to 120 characters. Nothing changed.");
    let out;
    try { out = await this.call("PUT", "/v1/admin/workspace", { name }); } catch (e) {
      if (e && e.status === 403) throw new Error("Only an admin of this workspace can rename it for everyone.");
      if (e && e.status === 400) throw new Error("Enter a readable workspace name, up to 120 characters. Nothing changed.");
      throw new Error("Cannot rename the workspace now: " + (e && e.message || "the server did not answer"));
    }
    this.workspaceName = wire.validWorkspaceName(out && out.name);
    await put(this.store, "kv", "workspace", this.workspaceName || undefined);
    this.changed();
    return this.workspaceInfo();
  }

  // peerWordsFn names devices in the page's sentences (MEL-525), as
  // client.PeerWords does: "your Pixel" for another device of this person,
  // `another person, who calls themselves "Vitalii" (Desk)` for a device
  // a pinned person record names, with the
  // key's first group when that name is also this person's or another
  // pinned person's, and the device in words otherwise; never the address.
  async peerWordsFn() {
    const pinned = (await this.store.all("persons")).filter((p) => p && p.state === "pinned");
    const count = new Map(), key = (l) => String(l || "").toLowerCase();
    for (const p of [...(this.me ? [this.me] : []), ...pinned]) count.set(key(p.label), (count.get(key(p.label)) || 0) + 1);
    return (address) => {
      if (!wire.validAddress(address)) return address;
      if (address === this.address) return "this device";
      const device = deviceWords(address);
      if (this.me && (this.me.devices || []).some((d) => d.address === address)) return "your " + device;
      const p = pinned.find((x) => (x.devices || []).some((d) => d.address === address));
      if (!p || !p.label) return device;
      const d = p.devices.find((x) => x.address === address);
      return "another person, who calls themselves " + JSON.stringify(p.label) + " (" + device + (count.get(key(p.label)) > 1 && d && d.fingerprint ? " · " + String(d.fingerprint).split("-")[0] : "") + ")";
    };
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
          this.listed.set(ref.hash, { person: r.person, label: r.label, picture: r.picture || "",
            devices: await Promise.all(r.devices.map(async (d) => ({ address: d.address, fingerprint: await wire.fingerprint(d) }))) });
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
  async convMessages(convId, inbox, outbox) {
    const people=[this.me,...await this.store.all("persons")].filter(Boolean);
    const personOf = address => people.find(p=>p.devices?.some(d=>d.address===address))?.person || "";
    const groups = new Map();
    for (const r of outbox) {
      if (r.conv !== convId || r.aside || r.forwarded || r.sub === "excerpt" || r.sub === wire.SubDriveSpace || this.erasedRow(r)) continue; // sent excerpts and shared human records are carriers, not extra room turns; a deleted turn is not shown
      const g = groups.get(r.lid || r.id) || [];
      g.push(r);
      groups.set(r.lid || r.id, g);
    }
    const sent = [...groups.values()].map((g) => {
      const least = copyOrder(g);
      const copies=g.map(r=>({id:r.id,to:r.to,state:r.state,detail:r.detail,...this.cancellationView(r),own:!!r.own||!!this.me?.devices?.some(d=>d.address===r.to),person:r.person||personOf(r.to)})); return { ...g[0], send_group:g.some(r=>r.send_group_conflict)||new Set(g.map(r=>r.send_group).filter(Boolean)).size>1?"":g.find(r=>r.send_group)?.send_group||"", state: least.state, delivery:deliveryOf(copies),detail: least.detail, lagging: least.to, copies, delivery_cancelled:g.some(r=>r.delivery_cancelled)&&!g.some(r=>["custody","delivered"].includes(r.state)), handover_started:g.some(r=>r.delivery_cancelled&&r.state==="failed"&&r.handover_started!==false) ? true : least.handover_started };
    });
    const received = inbox.filter((m) => m.conv === convId && !m.control && m.sub !== wire.SubRootSync && m.sub !== wire.SubDriveSpace && !this.erasedRow(m)).map((m) => {
      if (m.sub !== "excerpt") return m;
      const h = wire.parseHistory(m.body);
      return { ...m, ...h, id: m.id, at: h.ts * 1000, sub: "", fp: "", excerpt_pid: m.pid, claimed_key: h.from_key,
        attachments: m.attachments, history: true, synced_from: m.from, replica: true, state: "" };
    });
    return [...received, ...sent].sort((a, b) => a.at - b.at);
  }

  // ---- the optional Google Drive space of a conversation (MEL-490)
  //
  // The native module (drivespace.mjs) does every Google side in the page,
  // with tokens in memory only. This engine keeps what is AgentNet's: the
  // conversation's space record (gdrive.Space, a quiet version 2 message
  // under drv1, admitted like the core does), this device's private state
  // (pending publications, agent grants, the admin's setup draft: sealed to
  // this device's own key at rest) and the workspace's public config.

  // checkDriveSpace admits a Drive space record the way the core does
  // (admitDriveControl): the first revision is owned by the person who
  // published it; every later one comes from that owner, follows the one
  // kept here (revision + 1, previous = its folder) and keeps the owner. A
  // second record at the same revision is a fork (refused); a revision
  // without its predecessor waits. Nothing in a record grants anything.
  async checkDriveSpace(n, sender, root) {
    let sp;
    try { sp = wire.parseDriveSpace(n.body); } catch (e) { throw new Hold("invalid", "malformed Drive space record"); }
    if (!sender || !sender.person) throw new Hold("invalid", "a Drive space record from no member person");
    if (!root.members.some((m) => m.person === sender.person)) throw new Hold("invalid", "a Drive space record from a person outside the conversation");
    const prior = await this.latestSpace(n.conv);
    if (!prior) {
      if (sp.revision !== 1 || sp.previous) throw new Hold("proof_pending", "the Drive space record's predecessor is not here (yet)");
      if (sp.owner !== sender.person) throw new Hold("invalid", "a Drive space is owned by the person who published it");
      return sp;
    }
    if (sp.revision <= prior.revision) {
      if (sp.revision === prior.revision && (sp.folder !== prior.folder || sp.owner !== prior.owner || sp.name !== prior.name || sp.disconnected !== prior.disconnected)) throw new Hold("invalid", "a second Drive space record at the same revision: a fork");
      return sp; // older or the same: nothing changes
    }
    if (sp.revision !== prior.revision + 1) throw new Hold("proof_pending", "the Drive space record's predecessor is not here (yet)");
    if (sp.owner !== prior.owner || sender.person !== prior.owner) throw new Hold("invalid", "only the space's original owner publishes its later revisions");
    if (sp.previous !== prior.folder) throw new Hold("invalid", "a Drive space record that does not follow the one kept here");
    return sp;
  }

  // latestSpace is the conversation's space as kept here: the highest
  // revision among admitted records (received, published here, or history).
  async latestSpace(conv) {
    let best = null;
    for (const m of [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))]) {
      if (m.conv !== conv || m.sub !== wire.SubDriveSpace) continue;
      let sp;
      try { sp = wire.parseDriveSpace(m.body); } catch (e) { continue; }
      if (!best || sp.revision > best.revision) best = sp;
    }
    return best;
  }

  // publishSpace sends the record to every device of the conversation that
  // reads it (drv1); copies for the others wait. The sender's own copies
  // make it the space kept here.
  async publishSpace(sp) {
    const c = await this.store.get("convs", sp.conv);
    if (!c) throw new Error("No such conversation here.");
    if (!this.me || sp.owner !== this.me.person) throw new Error("A Drive space is published by its owner only.");
    const prior = await this.latestSpace(sp.conv);
    if (prior && sp.revision !== prior.revision + 1) throw new Error("The space record here moved on; read it again.");
    return this.sendConv(c, { kind: "message", sub: wire.SubDriveSpace, body: wire.driveSpaceJSON(sp) });
  }

  // localGet/localSet keep a private record of this device sealed to its
  // own key in the store (never plain rows): Drive drafts, pending
  // publications, agent grants.
  async localGet(key) {
    const row = await this.store.get("kv", "local/" + key);
    if (!row || !row.sealed) return null;
    try { return await wire.openLocal(this.keys, row.sealed); } catch (e) { return null; }
  }
  async localSet(key, value) {
    if (value === null || value === undefined) { await this.store.write([{ s: "kv", k: "local/" + key, v: undefined }]); return; }
    await put(this.store, "kv", "local/" + key, { sealed: await wire.sealLocal(this.keys, value) });
  }

  // driveConfig is the workspace's public Drive configuration (no tokens,
  // secrets or accounts), as the relay publishes it; off when it has none.
  async driveConfig() {
    try { const st = await this.call("GET", "/v1/storage/drive"); return (st && st.config) || { enabled: false }; } catch (e) { if (e.status === 404) return { enabled: false }; throw e; }
  }

  // participationOf is what the Drive broker grant needs to know about an
  // agent in a DM: its conversation and state, whether it is hosted here.
  async participationOf(pid) {
    const row = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].find((m) => m.pid === pid && m.sub === "event" && m.conv);
    const c = row && (await this.store.get("convs", row.conv));
    const info = c && (await this.agentsOf(c)).find((x) => x.pid === pid);
    if (!info) throw new Error("No such agent participation here.");
    return { conv: c.id, state: info.state, host_here: !!info.host && info.host.address === this.address, held: info.held > 0 };
  }

  driveService() {
    if (!this.driveSvc) this.driveSvc = browserDriveProvider({
      getConfig: () => this.driveConfig(),
      getSpace: (conv) => this.latestSpace(conv),
      publishSpace: (sp) => this.publishSpace(sp),
      getOwner: async () => { if (!this.me) throw new Error("Set up your person first."); return this.me.person; },
      getGrant: (conv) => this.localGet("drive-grants/" + conv),
      setGrant: async (conv, pid, g) => { const all = (await this.localGet("drive-grants/" + conv)) || {}; if (g) all[pid] = g; else delete all[pid]; await this.localSet("drive-grants/" + conv, all); this.changed(); },
      participation: (pid) => this.participationOf(pid),
      getPending: (conv) => this.localGet("drive-pending/" + conv),
      setPending: async (conv, sp) => { await this.localSet("drive-pending/" + conv, sp); this.changed(); },
    });
    return this.driveSvc;
  }

  storageSetup() {
    if (!this.setupSvc) this.setupSvc = browserStorageSetupProvider({ call: (m, p, b) => this.call(m, p, b), readDraft: () => this.localGet("drive-setup-draft"), writeDraft: (d) => this.localSet("drive-setup-draft", d) });
    return this.setupSvc;
  }

  // ---- teams (R04): the native module verifies and pins the realm's signed
  // team chains over this engine; the realm is the one this authenticated
  // relay states (pinned per membership by the shell), never a page input.
  teamsService() {
    if (!this.teamsSvc && wire.validID(this.realm || "")) this.teamsSvc = browserTeams(this, this.realm);
    return this.teamsSvc || null;
  }

  // ---- storage: where this browser's files are and how much (client.StorageSummary)
  //
  // One explicit read: the file records this browser keeps (counted, never
  // decrypted) and the relay's own-usage report (GET /v1/storage). Counts
  // are ciphertext lengths, not what the browser allocates; the browser can
  // evict this site's data. Nothing is cleaned or changed by reading.
  async storageSummary() {
    const files = await this.store.all("files");
    const size = (v) => (v && v.ct ? v.ct.byteLength || v.ct.length || 0 : 0);
    const sum = (list) => ({ files: list.length, bytes: list.reduce((n, v) => n + size(v), 0) });
    const kept = sum(files.filter((v) => v && v.attachment)); // copies of files sent here, encrypted to this device
    const cache = sum(files.filter((v) => v && !v.attachment)); // ciphertext of received files, as fetched
    const queued = sum((await this.store.all("outbox")).flatMap((r) => (r.files || []).filter((f) => f.ct)));
    const areas = [
      { directory: "kept", label: "Retained sent files", kind: "ciphertext", status: "available", usage: kept,
        lifetime: "Kept for reopening here and for your other devices, with no automatic expiry. Deleting a message removes copies no other message still shows. The browser may evict this site's data." },
      { directory: "received", label: "Retained received files", kind: "ciphertext", status: "available", usage: cache,
        lifetime: "Fetched once and kept while it fits (up to " + (wire.BrowserMaxFile >> 20) + " MiB a file). A verified deletion of the message removes it; files you saved are yours. The browser may evict this site's data." },
      { directory: "outgoing", label: "Files waiting to be sent", kind: "ciphertext", status: "available", usage: queued,
        lifetime: "Released as soon as your server holds them; kept for retry until then." },
    ];
    const known = { files: kept.files + cache.files + queued.files, bytes: kept.bytes + cache.bytes + queued.bytes };
    let browser = "";
    try {
      const st = globalThis.navigator && navigator.storage;
      if (st) {
        const persisted = st.persisted ? await st.persisted() : null;
        const est = st.estimate ? await st.estimate() : null;
        browser = (persisted === true ? "This browser has agreed not to evict this site's data on its own." : persisted === false ? "This browser may clear this site's data when it needs space." : "")
          + (est && est.usage !== undefined ? " This site as a whole uses " + Math.round(est.usage / 1024) + " KB of the " + Math.round((est.quota || 0) / 1048576) + " MB the browser allows it (all of this origin, not only these files)." : "");
      }
    } catch (e) { /* not known */ }
    const local = { scope: "browser-device-records", location: "This browser's site storage for this address", areas, known, complete: true, browser: browser.trim(),
      exclusions: "Keys, messages, persons and files still in the composer are not counted. Counts are ciphertext lengths, not what the browser allocates." };
    const remote = { status: "unavailable" };
    try {
      const u = await this.call("GET", "/v1/storage");
      if (!u || u.scope !== "caller-owned-ciphertext" || u.quota_scope !== "hub-global" || !(u.quota_bytes > 0) || !(u.max_file_bytes > 0) || !(u.upload_idle_ttl_seconds > 0)) {
        remote.reason = "The server's storage summary was not understood; its usage and policy remain unknown.";
      } else { remote.status = "available"; remote.usage = u; }
    } catch (e) {
      if (e && e.status === 404) { remote.status = "unsupported"; remote.reason = "The workspace's server does not report storage usage."; }
      else remote.reason = "Storage usage could not be verified with the server.";
    }
    return { local, remote };
  }

  // ---- controls: reactions, revisions and retractions (client/controls.go)
  //
  // A version 3 message is about one earlier message: its ref names that
  // message's id (device thread) or logical id (conversation) and the key
  // that sent it. It is kept like any message (rows with v: 3 and ref) and
  // resolved when a message is shown, never when it arrives; it never
  // becomes a message, a decision, an alert or a job. A revision never
  // changes what was admitted; a retraction hides text and files here and
  // drops this device's cached ciphertext of the target's files.

  async groupControlHash(conv,n) {
    return wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify([await wire.groupHistoryContentHash(conv,n),n.ref]))));
  }

  async groupControlTarget(conv,ref,checks) {
    const rows=await this.authorityRows({conv,lid:ref.id},checks);
    const targets=rows.filter(r=>!r.control&&!r.aside);
    if(!targets.length)throw new Hold("proof_pending","Group control original has not arrived.");
    if(targets.some(r=>r.lid!==ref.id||(r.fp||this.fp)!==ref.fingerprint||r.excerpt_pid))throw new Hold("invalid","Group control original differs from exact logical ID and author.");
    return targets;
  }

  async groupControlFence(members,from,to) {
    const a=members.epochs.get(from),b=members.epochs.get(to);
    if(!a||!b)throw Error("Group control requires current sender and recipient.");
    const g=await this.store.get("kv","group/"+members.group.state.conv);
    for(const w of (g.pending||[]).map(wire.parseGroupWithdrawal))for(const m of members.group.state.members)if(m.person===w.person&&await wire.groupAdmissionHash(m.admission)===w.admission)throw new Hold("proof_pending","Group control pending withdrawal is unresolved.");
    return wire.hex(await wire.sha256(new TextEncoder().encode("agentnet-group-control-epochs-v1\n"+a+"\0"+b)));
  }

  retainedAssistant(info,events) {
    return info.role!=="human"&&!!info.invite&&info.state==="dismissed"&&!info.held&&!info.conflict&&events.some(r=>r.hash===info.decision&&r.e.type==="accept");
  }

  async groupStatusScope(conv,ref,host,hostFP,checks=[],historical=false,historyPacket=null) {
    const c=await this.groupRecord(conv);if(!c)throw new Hold("proof_pending","Group status context missing.");
    await this.groupTurnEvidence(conv,checks);
    const rows=(await this.authorityRows({conv,lid:ref.id},checks)).filter(r=>!r.control&&!r.aside&&(r.fp||this.fp)===ref.fingerprint);
    if(!rows.length)throw new Hold("proof_pending","Group status immutable request missing.");
    const first=rows[0];
    if(rows.some(r=>!r.pid||r.sub||!["question","task"].includes(r.kind)||r.pid!==first.pid||r.kind!==first.kind||JSON.stringify(r.target)!==JSON.stringify(first.target)))throw new Hold("invalid","Group status request is conflicting.");
    const events=historyPacket?await Promise.all(historyPacket.memberships.map(async e=>({e,hash:await wire.eventHash(e)}))):await this.convEvents(conv,checks,first.pid),members=await this.dmMembers(c,events,checks,historyPacket),info=this.resolveAgent(first.pid,events,members);
    if(historyPacket)members.historyEvents=events;
    if(historical&&!info.invite)throw new Hold("proof_pending","Historical status participation proof is missing.");
    const target=first.target,fp=first.fp||this.fp;
    if(info.state!=="active"&&!(historical&&this.retainedAssistant(info,events))||info.held||info.host?.address!==host||info.host.fingerprint!==hostFP||target?.address!==host||target.fingerprint!==hostFP||target.agent_id!==info.agent_id||!(first.human && wire.agentAuthor(first.human)) && target.group_admission!==members.epochs.get(fp))throw new Hold("invalid","Group status differs from exact PID/host/requester admission.");
    await this.checkExternalReply({conv,pid:first.pid,kind:replyKind(first.kind),reply_to:ref.id,agent_id:info.agent_id},info,members,checks,historical);
    return info;
  }

  async admitGroupControl(n,env,pin) {
    if(wire.assistantReaction(n))return this.admitGroupAssistantReaction(n,env,pin);
    const checks=[],c=await this.groupRecord(n.conv),{packet}=await this.groupTurnEvidence(n.conv,checks),senderPin=await this.groupRead(checks,"pins",env.from);
    if(senderPin?.pending||senderPin?.fingerprint!==pin.fingerprint)throw new StoreConflict();
    let pay;try{pay=wire.parseControl(n.sub,n.body);}catch(e){throw new Hold("invalid",e.message);}
    const members=await this.dmMembers(c,null,checks),sender=[...members.values()].find(p=>p.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint)),own=sender?.person===this.me.person;
    const rec={id:env.id,v:3,control:true,from:env.from,fp:pin.fingerprint,kind:"message",sub:n.sub,body:n.body,ref:n.ref,conv:n.conv,lid:n.lid,replica:!!n.replica,own:!!own,person:sender?.person||"",at:this.now(),read:true,group_admission:members.epochs.get(this.fp)};
    if(n.sub===wire.SubStatus) {if(pay.decision)throw new Hold("invalid","Group status is not an operator decision response.");await this.groupStatusScope(n.conv,n.ref,env.from,pin.fingerprint,checks);}
    else {
      if(![wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(n.sub)||!sender)throw new Hold("invalid","Group ordinary controls require a current member.");
      if(!!n.replica!==!!own)throw new Hold("invalid","Group control replica differs from own sender.");
      await this.groupControlFence(members,pin.fingerprint,this.fp);
      const targets=await this.groupControlTarget(n.conv,n.ref,checks),author=[...members.values()].find(p=>p.devices.some(d=>d.fingerprint===n.ref.fingerprint));
      if(n.sub!==wire.SubReaction&&author?.person!==sender.person)throw new Hold("invalid","Only the current sender person edits or retracts a group message.");
      if(n.sub===wire.SubRevision&&await this.refTombstoned(n.conv,n.ref))rec.body="";
      rec.targetRow=targets[0];
    }
    const key=pin.fingerprint+"/"+n.lid,hash=await this.groupControlHash(n.conv,n),seen=await this.groupRead(checks,"lids",key);
    if(seen){if(seen.hash!==hash)throw new Hold("conflicting_duplicate","Group control logical content differs.");const ops=[];ops.checks=checks;return ops;}
    const target=rec.targetRow;delete rec.targetRow;
    const ops=[{s:"inbox",k:env.id,v:rec},{s:"lids",k:key,v:{id:env.id,conv:n.conv,hash}}];
    if(n.sub===wire.SubRetraction)ops.push(...await this.dropCached(target,rec),...await this.blankRetracted(target,target.to?"outbox":"inbox",checks));
    ops.checks=checks;return ops;
  }

  async sendGroupControl(c,ref,sub,body) {
    if(![wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(sub))throw Error("Unsupported group message control.");
    const checks=[],{packet}=await this.groupTurnEvidence(c.id,checks),members=await this.dmMembers(c,null,checks),targets=await this.groupControlTarget(c.id,ref,checks);
    const author=[...members.values()].find(p=>p.devices.some(d=>d.fingerprint===ref.fingerprint));
    if(sub!==wire.SubReaction&&author?.person!==this.me.person)throw Error("Only the sender person edits or deletes this group message.");
    const recs=[],skipped=[],lid=wire.newID(),at=this.now();
    for(const person of members.values())for(const d of person.devices) {
      if(d.address===this.address)continue;
      const pin=await this.groupRead(checks,"pins",d.address);if(!pin||pin.pending||pin.fingerprint!==d.fingerprint)throw Error("Group control recipient key changed.");
      let ok=false,why="";try{await this.groupSupport(d.address,pin);[ok,why]=await this.ctlSupport(d.address,pin);}catch(e){why=e.message;}if(!ok&&sub!==wire.SubRetraction){skipped.push(d.address+": "+why);continue;}
      const own=person.person===this.me.person,fan=[{person:this.me.person,roster:this.me.hash},...(own?[]:[{person:person.person,roster:person.hash}])],id=wire.newID(),fence=await this.groupControlFence(members,this.fp,d.fingerprint);
      const envelope=await wire.seal({v:3,id,from:this.address,to:d.address,ts:Math.floor(at/1000),kind:"message",sub,body,ref,conv:c.id,lid,replica:own,fan},this.keys,await this.pubOf(pin));
      recs.push({v:3,control:true,id,lid,conv:c.id,to:d.address,fp:this.fp,own,person:this.me.person,kind:"message",sub,body,ref,at,aside:true,envelope,state:"queued",required_cap:wire.CapGroup,recipient_fp:d.fingerprint,group_admission:fence});
    }
    if(!recs.length) {
      if(members.size!==1||!members.has(this.me.person))throw Error("No current group device can read controls.");
      // A sole remaining person can still edit/delete their own messages.
      // Keep a signed local control in the same durable history store.
      const id=wire.newID(),fan=[{person:this.me.person,roster:this.me.hash}],fence=await this.groupControlFence(members,this.fp,this.fp);
      const envelope=await wire.seal({v:3,id,from:this.address,to:this.address,ts:Math.floor(at/1000),kind:"message",sub,body,ref,conv:c.id,lid,replica:true,fan},this.keys,await wire.publicEntry(this.keys,this.address));
      recs.push({v:3,control:true,id,lid,conv:c.id,to:this.address,fp:this.fp,own:true,person:this.me.person,kind:"message",sub,body,ref,at,aside:true,envelope,state:"delivered",required_cap:wire.CapGroup,recipient_fp:this.fp,group_admission:fence});
    }
    await this.commitControls(recs,checks);for(const r of recs)if(r.state!=="delivered")await this.post(r);return {id:recs[0].id,state:copyOrder(recs).state,skipped};
  }

  // An assistant's own reaction (client.admitAssistantReaction): it binds
  // to the exact request it reacts to and to the assistant that request
  // addressed, never to its host's person or device. In a device thread:
  // only a request this device sent that exact device key (named: to the
  // agent it named; default: naming none). Missing proof waits; a
  // mismatch is refused.
  async admitDeviceAssistantReaction(n, env, pin, rec) {
    const r = await this.store.get("outbox", n.ref.id);
    if (!r || r.v !== 1 || r.control || r.aside || !["question", "task"].includes(r.kind) || r.to !== env.from || n.ref.fingerprint !== this.fp) throw new Hold("invalid", "an assistant reacts only to a request this device sent it");
    if (!r.fp || r.fp !== pin.fingerprint) throw new Hold("invalid", "the request's exact recipient key is not the reacting key (or was not recorded)");
    if (n.agent_id ? r.target?.address !== env.from || r.target?.fingerprint !== pin.fingerprint || r.target?.agent_id !== n.agent_id : !!r.target) throw new Hold("invalid", n.agent_id ? "the reaction's agent is not the one the request named on that device" : "a default responder reacts only to a request with no named executor");
    return [{ s: "inbox", k: env.id, v: { ...rec, ...(n.agent_id ? { agent_id: n.agent_id } : {}), ...(n.origin ? { origin: n.origin } : {}) } }];
  }

  // assistantBinding: the participation's exact host key and agent (and,
  // live, that it may still give output). client.assistantHistoryCheck.
  assistantBinding(info, n, from, fp, live) {
    if (!info?.invite || info.state === "conflict") throw new Hold("proof_pending", "the assistant's participation is not settled here yet");
    if (info.role === "human" || info.host?.address !== from || info.host.fingerprint !== fp || (info.agent_id || "") !== (n.agent_id || "")) throw new Hold("invalid", "the reaction is not from the exact assistant of that participation");
    if (live && (info.state !== "active" || info.held)) throw new Hold("invalid", "the assistant's participation is not active");
  }

  // assistantRequest: ref is the exact request addressed to that
  // participation, under the very key that sent it (client:
  // externalOutputRequest and the request-key query).
  async assistantRequest(conv, pid, ref, agentID, info, members, checks) {
    await this.checkExternalReply({ conv, pid, kind: "message", status: wire.StatusProgress, reply_to: ref.id, agent_id: agentID || "" }, info, members, checks);
    const rows = checks ? await this.authorityRows({ conv, lid: ref.id }, checks) : [...await this.store.all("inbox"), ...await this.store.all("outbox")].filter((r) => r.conv === conv && (r.lid === ref.id || r.id === ref.id));
    if (!rows.some((r) => !r.control && !r.aside && !r.ref && (r.to ? this.fp : r.fp || r.claimed_key) === ref.fingerprint)) throw new Hold("invalid", "the reaction's request key differs");
  }

  // humanReactionRequest is client.humanReactionRequest: an assistant's
  // reaction to a captured audience names the exact stored request (conv,
  // lid = ref.id, its sender key, this participation, a question or task
  // addressed to its exact host and agent, with an audience), and its
  // audience is within that request's own: never broader than the request's.
  async humanReactionRequest(conv, n, info) {
    const rows = [...await this.store.all("inbox"), ...await this.store.all("outbox")].filter((r) => r.conv === conv && !r.control && !r.aside && r.lid === n.ref.id && r.pid === n.pid && r.human &&
      ["question", "task"].includes(r.kind) && (r.to ? this.fp : r.fp) === n.ref.fingerprint);
    if (!rows.length) throw new Hold("proof_pending", "the reaction's request is not here (yet)");
    const req = rows[0];
    if (req.target?.address !== info.host.address || req.target?.fingerprint !== info.host.fingerprint || (req.target?.agent_id || "") !== (info.agent_id || "")) throw new Hold("invalid", "the reaction's request is not addressed to this exact assistant");
    const captured = new Set(req.human.audience.map((s) => s.pid + "|" + s.invite + "|" + s.decision));
    if (!n.human.audience.every((s) => captured.has(s.pid + "|" + s.invite + "|" + s.decision))) throw new Hold("invalid", "the reaction's audience is broader than its request's");
  }

  async admitAssistantReaction(n, env, pin, c, root, rec) {
    if (!n.human && !wire.rootMember(root, this.me.person)) throw new Hold("invalid", "an assistant reaction goes only to the conversation's members");
    const events = await this.convEvents(n.conv), members = await this.dmMembers(c, events), info = this.resolveAgent(n.pid, events, members);
    this.assistantBinding(info, n, env.from, pin.fingerprint, true);
    if (n.human) { // to the request's captured audience: author the exact host, reader this exact device of it
      if (!info.invite) throw new Hold("proof_pending", "the assistant's participation is not here (yet)");
      const evidence = await this.humanEvidence(c, n.human);
      this.humanTurnAuthorization(n, evidence, info, env.from, pin.fingerprint, this.address, this.fp);
      await this.humanReactionRequest(n.conv, n, info);
    } else await this.assistantRequest(n.conv, n.pid, n.ref, n.agent_id, info, members);
    const key = pin.fingerprint + "/" + n.lid;
    if (await this.store.get("lids", key)) return []; // a copy already here
    Object.assign(rec, { conv: n.conv, lid: n.lid, replica: !!n.replica, own: false, person: "", pid: n.pid, ...(n.agent_id ? { agent_id: n.agent_id } : {}), ...(n.human ? { human: n.human } : {}), fan: n.fan || null });
    const forwards = await this.forwardStale(n, env, pin, c); // your devices the sender did not know get it as history
    return [{ s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash: "" } }, ...forwards.map((r) => ({ s: "outbox", k: r.id, v: r }))];
  }

  async admitGroupAssistantReaction(n,env,pin) {
    const checks=[],c=await this.groupRecord(n.conv);await this.groupTurnEvidence(n.conv,checks);
    const senderPin=await this.groupRead(checks,"pins",env.from);
    if(senderPin?.pending||senderPin?.fingerprint!==pin.fingerprint)throw new StoreConflict();
    try{wire.parseControl(n.sub,n.body);}catch(e){throw new Hold("invalid",e.message);}
    const events=await this.convEvents(n.conv,checks,n.pid),members=await this.dmMembers(c,events,checks),info=this.resolveAgent(n.pid,events,members),stamp=members.epochs.get(this.fp);
    if(!stamp||!members.get(this.me.person)?.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp))throw new Hold("invalid","An assistant reaction goes only to the group's current members.");
    if(!!n.replica!==this.me.devices.some(d=>d.address===env.from&&d.fingerprint===pin.fingerprint))throw new Hold("invalid","Group control replica differs from own sender.");
    this.assistantBinding(info,n,env.from,pin.fingerprint,true);
    await this.groupControlTarget(n.conv,n.ref,checks);
    await this.assistantRequest(n.conv,n.pid,n.ref,n.agent_id,info,members,checks);
    const key=pin.fingerprint+"/"+n.lid,hash=await this.groupControlHash(n.conv,n),seen=await this.groupRead(checks,"lids",key);
    if(seen){if(seen.hash!==hash)throw new Hold("conflicting_duplicate","Group control logical content differs.");const ops=[];ops.checks=checks;return ops;}
    // Stored only with this device's own exact live admission (as any group control).
    const rec={id:env.id,v:3,control:true,from:env.from,fp:pin.fingerprint,kind:"message",sub:n.sub,body:n.body,ref:n.ref,conv:n.conv,lid:n.lid,replica:!!n.replica,own:false,person:"",pid:n.pid,...(n.agent_id?{agent_id:n.agent_id}:{}),at:this.now(),read:true,group_admission:stamp};
    const ops=[{s:"inbox",k:env.id,v:rec},{s:"lids",k:key,v:{id:env.id,conv:n.conv,hash}}];ops.checks=checks;return ops;
  }

  // admitHumanEdit is client admitHumanEdit (ROOM_V1 §2.3): an edit
  // carrying its turn's captured audience counts only from that turn's
  // author, the very key that sent it, under the same author scope, to no
  // audience the turn did not have, where the turn's authority holds now.
  // It is kept under a member's person only (as the core's view), so a
  // guest's revision is not shown yet; a retraction blanks the turn here.
  async admitHumanEdit(n, env, pin) {
    const checks = [], c = await this.store.get("convs", n.conv) || await this.groupRecord(n.conv);
    if (!c) throw new Hold("proof_pending", "the conversation is not here (yet)");
    if (n.ref.fingerprint !== pin.fingerprint) throw new Hold("invalid", "an edit to a captured audience comes only from the key that sent its turn");
    const rows = (await this.authorityRows({ conv: n.conv, lid: n.ref.id }, checks)).filter((r) => !r.control && !r.aside && r.fp === n.ref.fingerprint); // received under that key
    const turn = rows.find((r) => r.human);
    if (!rows.length) throw new Hold("proof_pending", "the edited turn is not here (yet)");
    if (!turn) throw new Hold("invalid", "an edit carries a captured audience its turn did not");
    if ((turn.human.author_pid || "") !== (n.human.author_pid || "")) throw new Hold("invalid", "an edit comes only from its turn's author");
    const captured = new Set(turn.human.audience.map((s) => s.pid + "|" + s.invite + "|" + s.decision));
    if (!n.human.audience.every((s) => captured.has(s.pid + "|" + s.invite + "|" + s.decision))) throw new Hold("invalid", "an edit's audience is broader than its turn's");
    const evidence = await this.humanEvidence(c, n.human, checks);
    this.humanAuthorization(n.human, evidence, env.from, pin.fingerprint, this.address, this.fp);
    const key = pin.fingerprint + "/" + n.lid;
    if (await this.groupRead(checks, "lids", key)) { const ops = []; ops.checks = checks; return ops; }
    const own = this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint), sp = own ? this.me : await this.personOf(env.from, pin);
    const rec = { id: env.id, v: 3, control: true, from: env.from, fp: pin.fingerprint, kind: "message", sub: n.sub, body: n.body, ref: n.ref, at: this.now(), read: true,
      conv: n.conv, lid: n.lid, replica: !!n.replica, own, person: evidence.members.has(sp.person) ? sp.person : "", human: n.human, fan: n.fan || null };
    const ops = [{ s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash: "" } }];
    if (n.sub === wire.SubRetraction) ops.push(...(await this.dropCached(turn, rec)), ...(await this.blankRetracted(turn, turn.fp ? "inbox" : "outbox", checks)));
    ops.checks = checks; return ops;
  }

  async admitControl(n, env, pin) {
    const checks = [];
    if (n.sub === wire.SubClear) return this.admitClear(n, env, pin); // a person-local act: no membership asked
    if (wire.humanEdit(n)) return this.admitHumanEdit(n, env, pin); // to its turn's captured audience: decided as that turn was
    if(n.conv && await this.store.get("kv","group/"+n.conv))return this.admitGroupControl(n,env,pin);
    const ref = n.ref;
    const rec = { id: env.id, v: 3, control: true, from: env.from, fp: pin.fingerprint, kind: "message", sub: n.sub, body: n.body, ref, at: this.now(), read: true };
    // Headless (hdl1): this browser hosts nothing, so it never receives a
    // decision; a status is a host's word on a request THIS device sent,
    // accepted only from that request's executor (the device it went to).
    if (n.sub === wire.SubDecision) throw new Hold("invalid", "this browser hosts no requests: a decision has no place here");
    if (n.sub === wire.SubStatus) {
      let pay;
      try { pay = wire.parseControl(n.sub, n.body); } catch (e) { throw new Hold("invalid", "malformed status"); }
      if (pay.decision) {
        // A host's answer to an operator's decision (client.statusAllowed):
        // kept only for the very decision this device sent that host about
        // that request, naming the same report and attempt. It belongs to
        // that report's item, never to the request's execution state.
        if (n.conv) throw new Hold("invalid", "an answer to a decision is device-scoped");
        const d = await this.store.get("outbox", pay.decision);
        if (!d || !d.control || d.sub !== wire.SubDecision || d.to !== env.from || !d.ref || d.ref.id !== ref.id || d.ref.fingerprint !== ref.fingerprint) throw new Hold("invalid", "it answers a decision this device did not send " + env.from + " about that request");
        let made = null;
        try { made = JSON.parse(d.body); } catch (e) { made = null; }
        if (!made || made.report !== pay.report || made.attempt !== pay.attempt) throw new Hold("invalid", "it answers a decision this device did not make (report or attempt differ)");
        return [{ s: "inbox", k: env.id, v: rec }];
      }
      let target = null;
      if (!n.conv) {
        const r = await this.store.get("outbox", ref.id);
        if (r && r.v === 1 && !r.control && !r.aside && (r.kind === "question" || r.kind === "task") && r.to === env.from && ref.fingerprint === this.fp) target = r;
      } else {
        const rows = (await this.store.all("outbox")).filter((r) => r.conv === n.conv && !r.control && !r.aside && r.lid === ref.id && (r.kind === "question" || r.kind === "task"));
        const mine = rows.find((r) => r.target && r.target.address === env.from && r.target.fingerprint === pin.fingerprint);
        if (mine && ref.fingerprint === this.fp) target = mine;
      }
      if (!target) throw new Hold("proof_pending", "no request of this device that " + env.from + " executes is here (yet)");
      Object.assign(rec, n.conv ? { conv: n.conv, lid: n.lid, replica: !!n.replica } : {});
      return [{ s: "inbox", k: env.id, v: rec }];
    }
    if (!n.conv && wire.assistantReaction(n)) return this.admitDeviceAssistantReaction(n, env, pin, rec);
    if (!n.conv) {
      // The target is the row with exactly that id AND that sender key, in
      // this thread: a message from that device (inbox, verified under
      // ref's key) or one sent to it from here (outbox, ref = this key).
      // A received id is the sender's choice and can equal a sent id, so
      // each direction is checked on its own; neither shadows the other.
      const inRow = await this.store.get("inbox", ref.id), outRow = await this.store.get("outbox", ref.id);
      const inbound = inRow && inRow.v === 1 && !inRow.control && inRow.from === env.from && inRow.fp === ref.fingerprint ? inRow : null;
      const outbound = outRow && outRow.v === 1 && !outRow.control && !outRow.aside && outRow.to === env.from && this.fp === ref.fingerprint ? outRow : null;
      const target = inbound || outbound;
      if (!target) throw new Hold("proof_pending", "no message of this thread with that id and key is here (yet)");
      if (n.sub !== wire.SubReaction && pin.fingerprint !== ref.fingerprint) throw new Hold("invalid", "only the sender edits or deletes a message");
      if (n.sub === wire.SubRevision && (await this.refTombstoned("", ref))) rec.body = ""; // a revision after the deletion keeps no text
      const ops = [{ s: "inbox", k: env.id, v: rec }];
      if (n.sub === wire.SubRetraction) ops.push(...(await this.dropCached(target, rec)), ...(await this.blankRetracted(target, inbound ? "inbox" : "outbox", checks)));
      ops.checks = checks; return ops;
    }
    const conv = await this.store.get("convs", n.conv);
    if (!conv) throw new Hold("proof_pending", "the conversation is not here (yet)");
    if (!this.me) throw new Hold("invalid", "this device has no person");
    const root = wire.parseRoot(conv.root);
    if (wire.assistantReaction(n)) return this.admitAssistantReaction(n, env, pin, conv, root, rec);
    const own = this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint);
    const sp = own ? this.me : await this.personOf(env.from, pin);
    if (!wire.rootMember(root, sp.person)) throw new Hold("invalid", "the sender is not a member of this conversation");
    if (n.sub !== wire.SubReaction && sp.person !== (await this.personOfFp(ref.fingerprint))) throw new Hold("invalid", "only the sender's person edits or deletes a message");
    const key = pin.fingerprint + "/" + n.lid;
    if (await this.store.get("lids", key)) return []; // a copy already here
    Object.assign(rec, { conv: n.conv, lid: n.lid, replica: !!n.replica, own, person: sp.person, fan: n.fan || null });
    if (n.sub === wire.SubRevision && (await this.refTombstoned(n.conv, ref))) rec.body = ""; // a revision after the deletion keeps no text
    const ops = [{ s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, hash: "" } }];
    if (n.sub === wire.SubRetraction) {
      const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox");
      const t = inbox.find((m) => m.conv === n.conv && !m.control && !m.aside && m.lid === ref.id && m.fp === ref.fingerprint)
        || outbox.find((m) => m.conv === n.conv && !m.control && !m.aside && m.lid === ref.id && this.fp === ref.fingerprint);
      if (t) ops.push(...(await this.dropCached(t, rec)), ...(await this.blankRetracted(t, t.fp ? "inbox" : "outbox", checks)));
    }
    // A device that knows a newer roster than the sender's fan names
    // forwards the control, as history, to the devices the sender missed.
    const forwards = await this.forwardStale(n, env, pin, conv);
    const result = [...ops, ...forwards.map((r) => ({ s: "outbox", k: r.id, v: r }))];
    result.checks = checks; return result;
  }

  // blankRetracted is the retention rule for an authorized retraction: a
  // plain message's text and its revision texts are blanked in every row
  // of that exact scope (no copy is kept anywhere here); a question or
  // task keeps its body, the admitted execution input, disclosed under
  // Details. Files' cached ciphertext is dropCached's business.
  async blankRetracted(target, store, checks = []) {
    const key = target.conv ? target.lid : target.id, fp = store === "outbox" ? this.fp : target.fp || this.fp, conv = target.conv || "";
    if (["question","task"].includes(target.kind)) return this.cancelRequestCopies(conv,{id:key,fingerprint:fp},checks);
    if (target.kind !== "message") return [];
    const ops = [];
    const rows = [...(await this.store.all("inbox")).map((r) => ({ r, s: "inbox" })), ...(await this.store.all("outbox")).map((r) => ({ r, s: "outbox" }))];
    for (const { r, s } of rows) {
      const inScope = (r.conv || "") === conv;
      if (!r.control && !r.aside && inScope && (r.conv ? r.lid === key : r.id === key) && (s === "outbox" ? this.fp : r.fp || this.fp) === fp && r.body) {
        ops.push({ s, k: r.id, v: { ...r, body: "" } });
      } else if (r.control && r.sub === wire.SubRevision && r.ref && r.ref.id === key && r.ref.fingerprint === fp && inScope && r.body) {
        ops.push({ s, k: r.id, v: { ...r, body: "" } }); // its revision texts go with it
      }
    }
    // The private delegate wrapper is also a stored copy of this ordinary
    // message. Keep custody truthful; cancel only handover still local.
    for (const { r, s } of rows) {
      const request = r.receiver_setup?.request;
      if (!request || request.kind !== "message" || (request.conv || "") !== conv || (request.conv ? request.lid : request.id) !== key || request.from_key !== fp) continue;
      const wrapper = await this.groupRead(checks, s, r.id);
      if (!wrapper || wrapper.receiver_redacted) continue;
      const detail = "Original ordinary request was deleted before selected handover.";
      const pending = state => ["receiver_waiting", "queued", "waiting", "failed"].includes(state);
      ops.push({ s, k: r.id, v: { ...wrapper, body: "", attachments: undefined, files: undefined, receiver_redacted: true,
        receiver_setup: { ...wrapper.receiver_setup, redacted: true, request: { ...request, body: "", attachments: [] } },
        state: pending(wrapper.state) ? "failed" : wrapper.state, detail } });
      for (const descriptor of wrapper.receiver_setup.originals || []) {
        const original = await this.groupRead(checks, "outbox", descriptor.id);
        if (!original || original.receiver_route?.request_digest !== wrapper.receiver_route.request_digest || (original.conv || "") !== conv || (original.conv ? original.lid : original.id) !== key) continue;
        ops.push({ s: "outbox", k: original.id, v: { ...original, body: "", attachments: undefined, files: undefined,
          receiver_redacted: true, state: pending(original.state) ? "failed" : original.state, detail } });
      }
    }
    return ops;
  }

  // noticeOps is client.supersedeNotices for review notice row about to be
  // stored: a snapshot from its host stands for all the host's earlier
  // ones (one card per host). The host's older open notices are resolved;
  // the row itself is stored resolved when a newer one is here already,
  // open or dismissed (a late, older notice never brings a card back), or
  // when it says nothing waits any more (no items, no count: the settled
  // snapshot). A host's time is a version 2 report's at, else the
  // envelope's ts; of the same second, the later stored wins.
  noticeOps(inbox, row) {
    if (inbox.some((r) => r.id === row.id)) return []; // stored already: as it stands (dismissed stays dismissed)
    const at = noticeTime(row);
    let resolved = noticeSettled(row);
    const ops = [];
    for (const r of inbox) {
      if (r.id === row.id || r.from !== row.from || !isNotice(r)) continue;
      if (at < noticeTime(r)) resolved = true;
      else if (!r.resolved) ops.push({ s: "inbox", k: r.id, v: { ...r, resolved: true } });
    }
    ops.push({ s: "inbox", k: row.id, v: resolved ? { ...row, resolved: true } : row });
    return ops;
  }

  // settleLeftoverNotices is client.settleLeftoverNotices: once per store,
  // every open review notice but the newest per host (open or dismissed)
  // is resolved (notices stored before they superseded each other).
  async settleLeftoverNotices() {
    if (await this.store.get("kv", "notices_settled")) return;
    const newest = new Map(), ops = [];
    for (const r of (await this.store.all("inbox")).filter(isNotice).sort((x, y) => (x.at || 0) - (y.at || 0))) {
      const cur = newest.get(r.from);
      if (!cur) { newest.set(r.from, r); continue; }
      if (noticeTime(r) >= noticeTime(cur)) { if (!cur.resolved) ops.push({ s: "inbox", k: cur.id, v: { ...cur, resolved: true } }); newest.set(r.from, r); }
      else if (!r.resolved) ops.push({ s: "inbox", k: r.id, v: { ...r, resolved: true } });
    }
    await this.store.write([...ops, { s: "kv", k: "notices_settled", v: true }]);
  }

  // reportItems are the review notices this device received (kind message,
  // status review_notice), each a report from another machine: a version 2
  // body (client.Report) is parsed for what it says (request ids, the
  // requester keys, states as the host names them, blockers, attempts, the
  // snapshot time); an older one stays its count text. An item is
  // actionable and carries an excerpt only as the host sent it: the host
  // includes them for a device it granted as an operator (its exact key),
  // and nothing here can make one up. Each item carries the host's answer
  // to this device's decision on it, from this report at this attempt
  // (client.decisionResults).
  reportItems(inbox, outbox = [], words = (a) => a) {
    const out = [];
    for (const m of inbox) {
      if (m.v !== 1 || m.control || m.kind !== "message" || m.status !== "review_notice" || m.reply_to || (m.attachments || []).length || m.resolved) continue;
      const item = { id: m.id, peer: m.from, kind: "message", why: "A report from " + words(m.from), excerpt: firstLine(m.body), at: iso(m.at), notice: true };
      try {
        const v = JSON.parse(m.body);
        if (v && v.v === 2 && Array.isArray(v.items) && typeof v.host === "string" && (!v.host || v.host === m.from)) { // a machine reports only its own requests
          item.report = { host: m.from, at: iso((v.at || 0) * 1000), items: v.items.filter((it) => it && wire.validID(it.id || "")).map((it) => {
            const key = String(it.key || "");
            const o = { id: it.id, from: String(it.from || ""), key, kind: String(it.kind || ""), state: String(it.state || ""), blocker: String(it.blocker || ""), since: iso((it.since || 0) * 1000),
              attempt: Number.isSafeInteger(it.attempt) && it.attempt >= 0 ? it.attempt : 0, actionable: it.actionable === true && wire.validFingerprint(key) };
            if (it.conv === true) o.conv = true; // a DM or group request: decided, never answered by hand from here
            if ((o.actionable || o.state === "needs_human") && typeof it.excerpt === "string" && it.excerpt) o.excerpt = o.state === "needs_human" ? it.excerpt : firstLine(it.excerpt);
            // A task carrying out its agent's proposal (client.ProposalView, first lines): for the operator approving it.
            const p = it.proposal;
            if (o.actionable && p && typeof p === "object" && typeof p.proposal_id === "string" && p.proposal_id) {
              o.proposal = Object.fromEntries(["question_id", "question", "asker", "proposal_id", "proposal", "confirmed_by"].map((k) => [k, firstLine(typeof p[k] === "string" ? p[k] : "")]));
            }
            const res = this.decisionResult(inbox, outbox, m, o);
            if (res) o.result = res;
            return o;
          }) };
          // A device that may not decide: how many wait, and who decides
          // them from their own devices (client.Report Count, Deciders).
          if (Number.isSafeInteger(v.count) && v.count > 0) item.report.count = v.count;
          if (Array.isArray(v.deciders)) item.report.deciders = v.deciders.filter((d) => d && typeof d === "object").map((d) => ({
            ...(typeof d.person === "string" && d.person ? { person: d.person } : {}), ...(typeof d.label === "string" && d.label ? { label: d.label } : {}),
            ...(typeof d.address === "string" && d.address ? { address: d.address } : {}) })).filter((d) => d.person || d.address);
          const k = item.report.items.length || item.report.count || 0;
          item.excerpt = k + (k === 1 ? " request" : " requests") + " reported by " + item.report.host;
        }
      } catch (e) { /* an older count-only notice: its text stands */ }
      out.push(item);
    }
    return out;
  }

  // Private reports from a current own device supplement its public blocker.
  // A removed/pending key or another person's report never becomes our agent's turn.
  async ownNeedsYouReports(inbox, outbox) {
    const latest = new Map();
    if (this.me?.state === "conflict") return [];
    for (const m of inbox) {
      if (m.v !== 1 || m.control || m.kind !== "message" || m.status !== "review_notice" || m.reply_to || m.resolved) continue;
      if (!this.me?.devices.some(d => d.address === m.from && d.fingerprint === m.fp)) continue;
      const pin = await this.store.get("pins", m.from);
      if (!pin || pin.pending || pin.fingerprint !== m.fp) continue;
      const r = this.reportItems([m], outbox)[0]?.report;
      if (!r || r.host !== m.from) continue;
      if (!latest.has(r.host) || r.at >= latest.get(r.host).at) latest.set(r.host, {...r, fingerprint:m.fp});
    }
    return [...latest.values()];
  }

  needsYouText(m, reports) {
    if (!m.pid || !m.target || m.excerpt_pid || m.history) return "";
    const r = reports.find(r => r.host === m.target.address && r.fingerprint === m.target.fingerprint);
    const ids = [m.id, m.lid, ...(m.copies || []).map(c => c.id)];
    return r?.items.find(it => it.conv && it.state === "needs_human" && it.key === (m.fp || this.fp) && ids.includes(it.id))?.excerpt || "";
  }

  // noticeLine is a report's one-line text: what a version 2 report says,
  // or the older count text as sent.
  noticeLine(m) {
    try {
      const v = JSON.parse(m.body);
      if (v && v.v === 2 && Array.isArray(v.items) && (!v.host || v.host === m.from)) { const k = v.items.length || (Number.isSafeInteger(v.count) ? v.count : 0); return k + (k === 1 ? " request" : " requests") + " reported by " + m.from; }
    } catch (e) { /* count text */ }
    return firstLine(m.body);
  }

  // decisionResult is the host's latest answer to this device's decisions
  // about item it of a report from that host: statuses from that host
  // naming a decision this device sent it about that request (id, key) at
  // this attempt, echoed exactly, made from any report of that host (a card
  // a newer report replaced still shows its outcome). Answers to another
  // attempt stay with theirs (client.decisionResults).
  decisionResult(inbox, outbox, notice, it) {
    let best = null;
    for (const s of inbox) {
      if (!s.control || s.sub !== wire.SubStatus || s.from !== notice.from || s.conv || !s.ref || s.ref.id !== it.id || s.ref.fingerprint !== it.key) continue;
      let pay;
      try { pay = wire.parseControl(s.sub, s.body); } catch (e) { continue; }
      if (!pay.decision) continue;
      const d = outbox.find((r) => r.id === pay.decision && r.control && r.sub === wire.SubDecision && r.to === notice.from && r.ref && r.ref.id === it.id && r.ref.fingerprint === it.key);
      if (!d) continue;
      let made;
      try { made = JSON.parse(d.body); } catch (e) { continue; }
      if (made.attempt !== it.attempt || pay.report !== made.report || pay.attempt !== made.attempt) continue;
      if (!best || pay.at >= best.atRaw) best = { decision: pay.decision, state: pay.state, ...(pay.refused ? { refused: pay.refused } : {}), at: iso(pay.at * 1000), atRaw: pay.at };
    }
    if (best) delete best.atRaw;
    return best;
  }

  // answered: whether a host's answer to decision id has arrived.
  async answered(id) {
    for (const s of await this.store.all("inbox")) {
      if (!s.control || s.sub !== wire.SubStatus) continue;
      try { if (wire.parseControl(s.sub, s.body).decision === id) return true; } catch (e) { /* not an answer */ }
    }
    return false;
  }

  // decide is the page's POST /api/operator/decide (ui.OperatorDecisions)
  // from this browser as a granted operator: one signed decision to the
  // host that holds the request, bound to what the host reported (its id
  // there, the requester's key, the state and attempt seen, the report),
  // exactly as client.Decide sends it. The host applies it once, only if
  // the request is still as seen, and answers with a status naming this
  // decision: that answer, never delivery, is the outcome (shown as the
  // report item's result). Whether this device is an operator there is
  // the host's local grant: refused there, it says so in the answer. The
  // same decision asked again before the answer is not sent twice.
  async decide(x) {
    const host = String(x.host || ""), id = String(x.id || ""), key = String(x.key || ""), action = String(x.action || ""), expect = String(x.expect || ""), report = String(x.report || ""), text = String(x.text || "");
    const attempt = Number.isSafeInteger(x.attempt) ? x.attempt : Number.isSafeInteger(Number(x.attempt)) ? Number(x.attempt) : -1;
    if (!wire.validID(id) || !wire.validFingerprint(key)) throw new Error("A decision names the request's id and its sender's key, as the host reported them.");
    if (!wire.DecisionActions.includes(action)) throw new Error("No such decision.");
    if (!wire.validStateToken(expect) || attempt < 0) throw new Error("A decision names the state and attempt you saw in the report.");
    if ((action === "reply" || action === "decline" || action === "continue") && !text.trim()) throw new Error(action === "reply" ? "Write the answer first." : "Say why, in a few words.");
    if (new TextEncoder().encode(text).length > wire.MaxDecisionText) throw new Error("The text is too long (16 KB at most).");
    if (host === this.address) throw new Error("This browser holds no requests: there is nothing to decide here.");
    const notice = wire.validID(report) ? await this.store.get("inbox", report) : null;
    if (action!=="continue" && (!notice || notice.v !== 1 || notice.control || notice.kind !== "message" || notice.status !== "review_notice" || notice.from !== host)) throw new Error("That report is not one " + host + " sent this device.");
    if(action==="continue") {
      const own=this.me;
      if(expect!=="needs_human" || attempt<1 || !own || own.state!=="self" || !(own.human_keys||[]).includes(this.fp) || !own.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp) || !own.devices.some(d=>d.address===host))throw Error("Only a current own human device can answer this exact waiting request here.");
      if(!wire.validID(x.send_id||""))throw Error("Keep the answer's exact send ID when retrying.");
    }
    for (const r of await this.store.all("outbox")) { // asked again while unanswered: the one sent stands
      if (action === "continue" || !r.control || r.sub !== wire.SubDecision || r.to !== host || !r.ref || r.ref.id !== id || r.ref.fingerprint !== key) continue;
      let d;
      try { d = JSON.parse(r.body); } catch (e) { continue; }
      if (d.action === action && d.expect === expect && d.attempt === attempt && d.report === report && d.text === text && !(await this.answered(r.id))) {
        return { note: "Already sent to " + host + "; its answer shows here once it decides.", sent: r.id };
      }
    }
    const sent = await this.sendDeviceControl(host, { id, fingerprint: key }, wire.SubDecision, JSON.stringify({ action, expect, attempt, text, report }), action==="continue"?wire.CapContinuation:wire.CapHeadless, action==="continue"?x.send_id:"");
    this.changed();
    return { note: (sent.state === "queued" ? "Queued for " + host + ": it goes when your server is reachable." : "Sent to " + host + ".") + " Its answer shows here once it decides; delivery decides nothing.", sent: sent.id };
  }

  // Provider-derived exact continuation; this grants nothing on the host.
  continuationOf(m, key, exec) {
    if (!exec || exec.state !== "needs_human" || !Number.isSafeInteger(exec.attempt) || exec.attempt < 1 || !["question","task"].includes(m.kind) || !wire.validFingerprint(key)) return null;
    const own=this.me, host=own?.devices?.find(d=>d.address===exec.host);
    if(!own || own.state!=="self" || !(own.human_keys||[]).includes(this.fp) || !own.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp) || !host || (m.target && (m.target.address!==exec.host || m.target.fingerprint!==host.fingerprint)))return null;
    return {id:m.lid||m.id,key,host:exec.host,attempt:exec.attempt};
  }

  // execOn resolves a request's host-asserted state from status controls
  // by its executor: the highest counter wins (tie: later id). A terminal
  // answer or result in the thread supersedes any status. stale: the host
  // is not connected as the relay's current member list has it.
  execOn(statuses, host, answered) {
    if (answered || !statuses.length) return null;
    let best = null;
    for (const c of statuses) {
      let pay;
      try { pay = wire.parseControl(c.sub, c.body); } catch (e) { continue; }
      if (pay.decision) continue; // an answer to an operator's decision: it belongs to that report's item, never to exec
      const id = c.lid || c.id; // as controlsOn: the same on every device
      if (!best || pay.n > best.n || (pay.n === best.n && id > best.id)) best = { ...pay, id };
    }
    if (!best) return null;
    const m = this.members.current ? this.members.list.find((x) => x.address === host) : null;
    const stale = !m || m.presence !== "online";
    return { state: best.state, at: iso(best.at * 1000), detail: best.detail, host, stale, attempt: best.attempt || 0, ...(best.refused ? { refused: best.refused } : {}) };
  }

  // personOfFp is the person a device key belongs to, as pinned here: yours
  // (any device of your person) or one kept in persons; "" when unknown.
  async personOfFp(fp) {
    if (this.me && (fp === this.fp || (this.me.known || this.me.devices || []).some((d) => d.fingerprint === fp))) return this.me.person;
    for (const p of await this.store.all("persons")) if ((p.known || p.devices || []).some((d) => d.fingerprint === fp)) return p.person;
    return "";
  }

  // refTombstoned is tombstoned for a reference alone (conv "" or id, the
  // target's key and its logical/message id): a retraction by that key's
  // author is pinned here, whether or not the target row itself is.
  async refTombstoned(conv, ref) {
    const ctls = await this.retractions();
    if (!ctls.length) return false;
    const authorPerson = conv ? await this.personOfFp(ref.fingerprint) : "";
    return ctls.some((c) => c.ref && c.ref.id === ref.id && c.ref.fingerprint === ref.fingerprint && (c.conv || "") === (conv || "") &&
      (conv ? !!authorPerson && (c.person || "") === authorPerson : (c.fp || this.fp) === ref.fingerprint));
  }

  // retractions are the retraction rows held in inbox and outbox, kept in
  // memory until the store says a write may have changed them: admitting a
  // message read every message held here to look for one (MEL-546). A store
  // without retractionMark is read each time.
  async retractions() {
    const mark = this.store.retractionMark?.(), c = this.retractionCache;
    if (mark !== undefined && c && c.mark === mark) return c.rows;
    const rows = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].filter((r) => r.control && r.sub === wire.SubRetraction);
    if (mark !== undefined && this.store.retractionMark() === mark) this.retractionCache = { mark, rows };
    return rows;
  }

  // tombstoned says whether a valid retraction by its author is already
  // pinned for the message row r (any arrival order). What arrives after
  // that for the same exact ref (an original, a history copy, a direct
  // copy replacing a history one, a revision) is stored without its plain
  // text, so a deletion never comes back; a question or task keeps its
  // admitted text.
  async tombstoned(r) {
    const ctls = await this.retractions();
    if (!ctls.length) return false;
    return this.retractedBy(r, ctls, r.conv ? await this.personOfFp(r.fp || this.fp) : "");
  }

  // isRetracted says whether a message row's author retracted it, as the
  // control rows held here say (the same rule controlsOn applies).
  async isRetracted(r) {
    const ctls = await this.retractions();
    return ctls.length > 0 && this.retractedBy(r, ctls, await this.personOfFp(r.fp || this.fp));
  }

  // retractedBy says whether a retraction among ctls applies to row r: it
  // names r exactly (scope, key, author key) AND comes from r's author (the
  // very key in a device thread; that key's person in a conversation).
  // Anything else is somebody else's word and moves nothing here.
  retractedBy(r, ctls, authorPerson) {
    const key = r.conv ? r.lid : r.id, fp = r.fp || this.fp, conv = r.conv || "";
    return ctls.some((c) => c.sub === wire.SubRetraction && c.ref && c.ref.id === key && c.ref.fingerprint === fp && (c.conv || "") === conv &&
      (conv ? !!authorPerson && (c.person || "") === authorPerson : (c.fp || this.fp) === fp));
  }

  // dropCached is the store ops that forget this device's cached ciphertext
  // of a retracted message's files (ct/<blob>) and the copy kept of a file
  // this device sent (kept/<sha>). Bytes another message still shows (the
  // same file sent twice, a history copy, either direction, any
  // conversation) are kept: only the last live reference lets them go.
  // Files the person saved are theirs; nothing here touches them.
  // ---- deleting a conversation from this person's devices (client convclear.go) --------------
  //
  // This person's copy of one conversation is erased on every device of
  // theirs; others keep theirs, and membership, guests, approvals and running
  // work are unchanged. What is erased is named exactly: every turn (logical
  // id and sender key) of it held here. The names go as version 3 "clear"
  // parts to this person's other devices only, each copy waiting until that
  // device reads clr1; a device linked later gets every name (replayErased),
  // one the deleting device did not know of gets them from one that did
  // (forwardClear). The names stay here, so a late, retried or history copy
  // of an erased turn stays a skeleton nothing shows. Text that unfinished
  // work needs is hidden at once and erased by the local change that ends
  // that work (sweepErased, on this engine's change feed). A device thread
  // exists on this device only: deleting it tells no one.

  async loadErased() {
    this.erased = new Set((await this.store.all("erased")).map((r) => erasedKey(r.conv, r.key, r.lid)));
  }
  // A row's turn name: its conversation ("" for a device thread), sender key, and logical id (a device thread's message id).
  rowName(r) { return { conv: r.conv || "", key: r.to ? this.fp : r.fp || "", lid: r.conv ? r.lid || "" : r.id }; }
  erasable(r) { return !r.forwarded && (!r.aside || r.control) && erasableSubs.has(r.sub || ""); }
  erasedRow(r) {
    if (!this.erased.size || !this.erasable(r)) return false;
    const n = this.rowName(r);
    return !!n.key && !!n.lid && this.erased.has(erasedKey(n.conv, n.key, n.lid));
  }
  erasedConv(conv) { for (const k of this.erased) if (k.startsWith(conv + "|")) return true; return false; }

  // eraseOps blanks what erased turns of conv ("" for device threads) still
  // say here, except what unfinished work needs, and drops the file copies
  // only they held: received ciphertext, and a kept copy of a sent file no
  // un-erased sent turn still has. Ids, states and receipts stay.
  eraseOps(conv, inbox, outbox) {
    const ops = [], blanked = new Set();
    for (const r of [...inbox, ...outbox]) {
      if ((r.conv || "") !== conv || !r.body || !this.erasedRow(r) || (r.to ? retainedOut(r) : retainedIn(r))) continue;
      ops.push({ s: r.to ? "outbox" : "inbox", k: r.id, v: { ...r, body: "" } }); blanked.add(r);
    }
    for (const r of outbox) { // history copies of an erased turn queued or sent from here
      if (!conv || r.conv !== conv || r.sub !== "history" || !r.body || retainedOut(r)) continue;
      let h; try { h = wire.parseHistory(r.body); } catch (e) { continue; }
      if (this.erased.has(erasedKey(conv, h.from_key, h.lid))) ops.push({ s: "outbox", k: r.id, v: { ...r, body: "" } });
    }
    const live = [...inbox, ...outbox].filter((r) => (r.attachments || []).length && !blanked.has(r) && !this.erasedRow(r));
    const shown = (test) => live.some((r) => r.attachments.some(test));
    for (const r of blanked) for (const a of r.attachments || []) {
      if (a.blob?.id && !shown((x) => x.blob && x.blob.id === a.blob.id)) ops.push({ s: "files", k: "ct/" + a.blob.id, v: undefined });
      if (r.to && a.sha256 && !shown((x) => x.sha256 === a.sha256)) ops.push({ s: "files", k: "kept/" + a.sha256, v: undefined });
    }
    return ops;
  }
  // keptCount counts erased turns of conv whose text unfinished work keeps.
  keptCount(conv, inbox, outbox) {
    const lids = new Set();
    for (const r of [...inbox, ...outbox]) if ((r.conv || "") === conv && r.body && this.erasedRow(r) && (r.to ? retainedOut(r) : retainedIn(r))) lids.add(r.to ? "o" + this.rowName(r).lid : "i" + r.id);
    return lids.size;
  }

  // deleteConversation is POST /api/conversation/delete (ui livedelete.go):
  // a conversation by id (all of this person's devices), or a device thread
  // by its peer and earliest message (this device only).
  async deleteConversation(body) {
    if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some((k) => !["conv", "peer", "thread"].includes(k))) throw new Error("Name one conversation, or one thread and its peer.");
    if (body.peer || body.thread) {
      if (body.conv || !body.peer || !body.thread) throw new Error("Name one conversation, or one thread and its peer.");
      return this.deleteThread(String(body.peer), String(body.thread));
    }
    const conv = String(body.conv || "");
    if (!wire.validHash(conv) || !((await this.store.get("convs", conv)) || (await this.groupRecord(conv)))) throw new Error("No such conversation here.");
    if (!this.me) throw new Error("This device has no person.");
    const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox"), names = new Map();
    for (const r of [...inbox, ...outbox]) {
      if (r.conv !== conv || !this.erasable(r) || this.erasedRow(r)) continue;
      const n = this.rowName(r);
      if (n.key && n.lid) names.set(erasedKey(conv, n.key, n.lid), n);
    }
    if (!names.size) throw new Error("There is nothing here to delete.");
    const deletion = wire.newID(), ops = [];
    for (const [k, n] of names) { ops.push({ s: "erased", k, v: { conv, key: n.key, lid: n.lid, deletion, shared: false } }); this.erased.add(k); }
    let kept = 0;
    try {
      ops.push(...this.eraseOps(conv, inbox, outbox)); kept = this.keptCount(conv, inbox, outbox);
      await this.store.write(ops);
    } finally { await this.loadErased(); }
    this.changed();
    return { note: deletionNote(false, await this.shareErased(), kept) };
  }

  // deleteThread erases exactly the device thread with peer whose earliest
  // message is id, and the controls on its messages, here only.
  async deleteThread(peer, id) {
    const thread = (await this.v1Threads()).find((g) => g[0].id === id && g[0].peer === peer);
    if (!thread) throw new Error("No such conversation here.");
    const ids = new Set(thread.map((m) => m.id)), names = new Map(), deletion = wire.newID();
    const add = (key, lid) => names.set(erasedKey("", key, lid), { key, lid });
    for (const m of thread) add(m.dir === "in" ? m.fp : this.fp, m.id);
    for (const r of [...await this.store.all("inbox"), ...await this.store.all("outbox")]) {
      if (!r.conv && r.control && r.ref && ids.has(r.ref.id) && (r.to ? r.to === peer : r.from === peer)) add(r.to ? this.fp : r.fp, r.id); // its messages' controls go with them
    }
    const ops = [];
    for (const [k, n] of names) { ops.push({ s: "erased", k, v: { conv: "", key: n.key, lid: n.lid, deletion, shared: true } }); this.erased.add(k); }
    for (const m of thread) if (await this.store.get("kv", "topic/" + peer + "/" + m.id)) ops.push({ s: "kv", k: "topic/" + peer + "/" + m.id, v: undefined }); // its name and marks go with it
    let kept = 0;
    try {
      const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox");
      ops.push(...this.eraseOps("", inbox, outbox)); kept = this.keptCount("", inbox, outbox);
      await this.store.write(ops);
    } finally { await this.loadErased(); }
    this.changed();
    return { note: deletionNote(true, 0, kept) };
  }

  // clearCopies seals the parts of one deletion for devs (this person's other
  // devices), each waiting until that device reads clr1. The fan names this
  // person's roster: the copies go to its devices only.
  async clearCopies(devs, conv, deletion, turns) {
    const parts = [];
    for (let i = 0; i < turns.length; i += wire.MaxClearTurns + 1) parts.push(turns.slice(i, i + wire.MaxClearTurns + 1));
    const recs = [], at = this.now(), fan = [{ person: this.me.person, roster: this.me.hash }];
    for (const [i, chunk] of parts.entries()) {
      const lid = wire.newID(), ref = { id: chunk[0].id, fingerprint: chunk[0].fingerprint };
      const body = wire.clearJSON({ deletion, part: i + 1, parts: parts.length, turns: chunk.slice(1) });
      for (const dev of devs) {
        const pin = (await this.store.get("pins", dev.address)) || (await this.sendKey(dev.address).catch(() => null)); // a device just linked may not be pinned yet
        if (!pin || pin.pending || pin.fingerprint !== dev.fingerprint) continue; // a changed key is never used
        let ok = false, why = "";
        try { [ok, why] = await this.ctlSupport(dev.address, pin, wire.CapConvClear); } catch (e) { why = "server_unavailable: cannot reach your server"; }
        const id = wire.newID();
        const envelope = await wire.seal({ v: wire.Version3, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), kind: "message", sub: wire.SubClear, body, ref, conv, lid, replica: true, fan }, this.keys, await this.pubOf(pin));
        recs.push({ v: 3, control: true, id, conv, lid, to: dev.address, fp: this.fp, own: true, person: this.me.person, kind: "message", sub: wire.SubClear, body, ref, at, aside: true,
          required_cap: wire.CapConvClear, recipient_fp: dev.fingerprint, state: ok ? "queued" : "waiting", detail: ok ? "" : why, envelope });
      }
    }
    return recs;
  }

  // shareErased queues every deletion made here not yet told to this
  // person's other devices, marking it told in the same write (a second
  // sweep's write then conflicts and queues nothing); after a crash the next
  // sweep does it. It says how many devices it is queued for.
  async shareErased() {
    if (!this.me) return 0;
    const devs = this.me.devices.filter((d) => d.address !== this.address), byDeletion = new Map();
    for (const r of await this.store.all("erased")) if (!r.shared && r.conv) byDeletion.set(r.deletion, [...(byDeletion.get(r.deletion) || []), r]);
    for (const list of byDeletion.values()) {
      const turns = list.map((r) => ({ id: r.lid, fingerprint: r.key })).sort((a, b) => (a.fingerprint + a.id < b.fingerprint + b.id ? -1 : 1));
      const recs = await this.clearCopies(devs, list[0].conv, list[0].deletion, turns);
      const checks = list.map((r) => ({ s: "erased", k: erasedKey(r.conv, r.key, r.lid), v: r }));
      try {
        await this.store.write([...recs.map((r) => ({ s: "outbox", k: r.id, v: r })), ...list.map((r) => ({ s: "erased", k: erasedKey(r.conv, r.key, r.lid), v: { ...r, shared: true } }))], checks);
      } catch (e) { if (e instanceof StoreConflict) continue; throw e; } // queued by another sweep meanwhile
      this.changed();
      if (this.connected) for (const r of recs) if (r.state === "queued") await this.post(r);
    }
    return devs.length;
  }

  // replayErased tells dev, a device of this person just linked from here,
  // every turn erased here, conversation by conversation.
  async replayErased(dev) {
    if (!this.me) return;
    const byConv = new Map();
    for (const r of await this.store.all("erased")) if (r.conv) byConv.set(r.conv, [...(byConv.get(r.conv) || []), { id: r.lid, fingerprint: r.key }]);
    const recs = [];
    for (const [conv, turns] of byConv) recs.push(...await this.clearCopies([dev], conv, wire.newID(), turns));
    if (!recs.length) return;
    await this.store.write(recs.map((r) => ({ s: "outbox", k: r.id, v: r })));
    this.changed();
    if (this.connected) for (const r of recs) if (r.state === "queued") await this.post(r);
  }

  // admitClear applies a deletion from another current device of this
  // person, exactly the turns it names; from anyone else it is refused. It
  // is no conversation act: membership is not asked. Own devices the
  // deleting device did not know of are told the same names.
  async admitClear(n, env, pin) {
    let part;
    try { part = wire.parseControl(wire.SubClear, n.body); } catch (e) { throw new Hold("invalid", "a malformed conversation deletion"); }
    if (!this.me || env.from === this.address || !this.me.devices.some((d) => d.address === env.from && d.fingerprint === pin.fingerprint)) throw new Hold("invalid", "a conversation deletion comes only from another current device of this person");
    if (!(await this.store.get("convs", n.conv)) && !(await this.groupRecord(n.conv))) throw new Hold("proof_pending", "the conversation is not here (yet)");
    const turns = [n.ref, ...part.turns], ops = [{ s: "inbox", k: env.id, v: { id: env.id, v: 3, control: true, from: env.from, fp: pin.fingerprint, kind: "message", sub: wire.SubClear, body: n.body,
      ref: n.ref, conv: n.conv, lid: n.lid, replica: true, own: true, person: this.me.person, fan: n.fan || null, at: this.now(), read: true } }];
    const prior = this.erased, next = new Set(prior);
    for (const t of turns) {
      const k = erasedKey(n.conv, t.fingerprint, t.id);
      if (!next.has(k)) ops.push({ s: "erased", k, v: { conv: n.conv, key: t.fingerprint, lid: t.id, deletion: part.deletion, shared: true } });
      next.add(k);
    }
    this.erased = next;
    try { ops.push(...this.eraseOps(n.conv, await this.store.all("inbox"), await this.store.all("outbox"))); } finally { this.erased = prior; }
    // forwardClear: devices of this person the sender's fan roster did not have.
    const f = (n.fan || []).find((x) => x.person === this.me.person), old = f && f.roster !== this.me.hash && (this.me.steps || []).find((st) => st.hash === f.roster);
    if (old) {
      const devs = this.me.devices.filter((d) => d.address !== this.address && d.address !== env.from && !old.devices.includes(d.address + "|" + d.fingerprint));
      if (devs.length) ops.push(...(await this.clearCopies(devs, n.conv, wire.newID(), turns)).map((r) => ({ s: "outbox", k: r.id, v: r })));
    }
    return ops;
  }

  // eraseArrivals keeps only the skeleton of a turn arriving after its
  // deletion here (unless unfinished work needs its text), and forwards no
  // history copy of it.
  eraseArrivals(ops) {
    for (let i = ops.length - 1; i >= 0; i--) {
      const o = ops[i];
      if (o.s === "inbox" && o.v && o.v.body && this.erasedRow(o.v) && !retainedIn(o.v)) o.v = { ...o.v, body: "", attachments: o.v.attachments?.map(({ blob, ...a }) => a) };
      if (o.s === "outbox" && o.v?.sub === "history" && o.v.conv) {
        let h; try { h = wire.parseHistory(o.v.body); } catch (e) { continue; }
        if (this.erased.has(erasedKey(o.v.conv, h.from_key, h.lid))) ops.splice(i, 1);
      }
    }
  }

  // sweepErased, after every local change: erases what erased turns kept for
  // work that has since ended, and queues names still untold (after a
  // crash). No timer.
  scheduleErase() {
    if (this.erasing) { this.eraseAgain = true; return; }
    this.erasing = (async () => { do { this.eraseAgain = false; await this.sweepErased().catch(() => {}); } while (this.eraseAgain); })().finally(() => { this.erasing = null; });
  }
  async sweepErased() {
    const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox"), ops = [];
    for (const conv of new Set([...this.erased].map((k) => k.slice(0, k.indexOf("|"))))) ops.push(...this.eraseOps(conv, inbox, outbox));
    if (ops.length) { await this.store.write(ops); this.changed(); }
    if ((await this.store.all("erased")).some((r) => !r.shared && r.conv)) await this.shareErased();
  }

  async dropCached(m, retraction) {
    const ops = [];
    const atts = m.attachments || [];
    if (!atts.length) return ops;
    const rows = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))];
    const ctls = rows.filter((r) => r.control).concat(retraction ? [retraction] : []);
    const persons = new Map(); // author key -> person, looked up once
    const personOf = async (fp) => { if (!persons.has(fp)) persons.set(fp, await this.personOfFp(fp)); return persons.get(fp); };
    const retractedRows = new Set();
    for (const r of rows) if (!r.control && !r.aside && this.retractedBy(r, ctls, r.conv ? await personOf(r.fp || this.fp) : "")) retractedRows.add(r);
    const retracted = (r) => retractedRows.has(r); // a row its own author retracted (as controlsOn would see it)
    // The target itself is out (it is retracted by the control being applied,
    // or the very same row: scope, key, author key and direction); a row
    // that merely shares its id (the other direction) is another message.
    const sameRow = (r) => (r.conv || "") === (m.conv || "") && (r.conv ? r.lid === m.lid : r.id === m.id) && (r.fp || this.fp) === (m.fp || this.fp) && !!r.to === !!m.to;
    const live = rows.filter((r) => !r.control && !r.aside && !sameRow(r) && (r.attachments || []).length && !retracted(r));
    const stillShown = (test) => live.some((r) => (r.attachments || []).some(test));
    for (const a of atts) {
      if (a.blob && a.blob.id && !stillShown((x) => x.blob && x.blob.id === a.blob.id)) ops.push({ s: "files", k: "ct/" + a.blob.id, v: undefined });
      if (a.sha256 && !stillShown((x) => x.sha256 === a.sha256)) ops.push({ s: "files", k: "kept/" + a.sha256, v: undefined });
    }
    return ops;
  }

  // controlsOn resolves what ctls (this device's control rows) did to one
  // message: reactions (per author, per emoji, the highest counter wins;
  // equal: the later control id), the latest revision by its author, and
  // whether its author retracted it. A control's id is its logical id in a
  // conversation, the same in every device's copy (client controlRow), so
  // every device breaks a tie alike. who names an author for the page;
  // author(c) is a control's author identity (a key in a device thread, a
  // person in a conversation), targetAuthor the target's.
  controlsOn(ctls, targetAuthor, author, who, mineIs, idOf = (a) => a, hostLabel) {
    const byAuthorEmoji = new Map(), assistants = new Map();
    let rev = null, deleted = false;
    for (const c of ctls) {
      let pay;
      try { pay = wire.parseControl(c.sub, c.body); } catch (e) { continue; }
      const asst = assistantActor(c, hostLabel), a = asst ? asst.id : author(c); // an assistant reacts as itself, never as its host
      if (asst) assistants.set(a, asst);
      const id = c.lid || c.id;
      if (c.sub === wire.SubReaction) {
        const k = a + "\n" + pay.emoji;
        const cur = byAuthorEmoji.get(k);
        if (!cur || pay.n > cur.n || (pay.n === cur.n && id > cur.id)) byAuthorEmoji.set(k, { n: pay.n, id, op: pay.op, a, emoji: pay.emoji });
      } else if (a === targetAuthor) {
        if (c.sub === wire.SubRevision && (!rev || pay.rev > rev.rev || (pay.rev === rev.rev && id > rev.id))) rev = { rev: pay.rev, id, text: pay.text };
        if (c.sub === wire.SubRetraction) deleted = true;
      }
    }
    const reactions = new Map();
    for (const r of byAuthorEmoji.values()) {
      if (r.op !== "add") continue;
      const g = reactions.get(r.emoji) || { emoji: r.emoji, by: [], mine: false };
      const asst = assistants.get(r.a);
      g.by.push(asst || { id: idOf(r.a), label: who(r.a) }); // identity and label apart (client.ReactionView.By, client.Reactor)
      if (!asst && mineIs(r.a)) g.mine = true;
      reactions.set(r.emoji, g);
    }
    const out = {};
    if (reactions.size) out.reactions = [...reactions.values()];
    if (rev) { out.edited = true; out.revision = rev.rev; out.text = rev.text; }
    if (deleted) out.deleted = true;
    return out;
  }

  // reactedHere says whether this person (in a device thread, this device)
  // has emoji on the message ref names, as its view resolves it
  // (client Agent.controlsOf): only that can be taken off.
  async reactedHere(conv, ref, emoji) {
    const rows = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].filter((r) => r.control && r.sub === wire.SubReaction &&
      (r.conv || "") === conv && r.ref && r.ref.id === ref.id && r.ref.fingerprint === ref.fingerprint);
    const me = this.me && this.me.person;
    const view = conv ? this.controlsOn(rows, "", (x) => x.person || "", () => "", (p) => !!me && p === me)
      : this.controlsOn(rows, "", (x) => x.fp || this.fp, () => "", (fp) => fp === this.fp);
    return (view.reactions || []).some((r) => r.emoji === emoji && r.mine);
  }

  // nextCounter is this author's next counter for a control on ref (one
  // more than the highest held from any device of this person), so
  // devices seeing controls in any order agree.
  async nextCounter(conv, ref, sub, emoji) {
    let max = 0;
    for (const c of (await this.store.all("inbox")).concat(await this.store.all("outbox"))) {
      if (!c.control || c.sub !== sub || !c.ref || c.ref.id !== ref.id || c.ref.fingerprint !== ref.fingerprint || (c.conv || "") !== conv) continue;
      let pay;
      try { pay = wire.parseControl(c.sub, c.body); } catch (e) { continue; }
      if (sub === wire.SubReaction && pay.emoji === emoji && pay.n > max) max = pay.n;
      if (sub === wire.SubRevision && pay.rev > max) max = pay.rev;
    }
    return max + 1;
  }

  // ctlSupport says whether a control can go to address: the relay carries
  // version 3 and the device's signed capabilities include controls.
  async ctlSupport(address, pin, cap = wire.CapControl) {
    const what = cap === wire.CapContinuation ? "human clarification continuations" : cap === wire.CapHeadless ? "operator decisions" : cap === wire.CapDrive ? "Drive space records" : cap === wire.CapConvClear ? "conversation deletions" : "reactions, edits or deletions";
    const f = await this.features();
    if (!f.includes("env3") || !f.includes("caps")) return [false, "server_update: your server cannot carry " + what + " (it needs an update)"];
    let prof = null;
    try { prof = await this.profile(address); } catch (e) { if (retryable(e)) throw e; }
    const key = (await this.pubOf(pin)).sign_key;
    if (!(await wire.profileSupports(prof || {}, address, key, cap))) return [false, "peer_update: " + address + " cannot " + (cap === wire.CapContinuation ? "continue waiting requests (update the AgentNet app on that computer)" : cap === wire.CapHeadless ? "receive operator decisions" : cap === wire.CapDrive ? "read Drive space records" : cap === wire.CapConvClear ? "apply conversation deletions" : "read reactions, edits or deletions") + " yet (an older program, or it has not connected since updating)"];
    return [true, ""];
  }

  // messageControl is the page's POST /api/message/{react|edit|delete}
  // (ui.MessageControls): the message as shown (conv, id, dir) becomes the
  // exact reference; the keys decide who may do what; nothing is ever sent
  // as an older version instead.
  async messageControl(what, x) {
    if (!wire.validID(x.id || "") || (x.dir !== "in" && x.dir !== "out")) throw new Error("A control names a message by its id and direction.");
    const conv = x.conv || "";
    // The exact stored message: received (inbox), sent here (outbox), or, shown
    // as yours, one another device of your person sent, kept here as its own
    // replica (or history) under that device's key: never this browser's.
    let kept = x.dir === "in" ? "inbox" : "outbox", rec = await this.store.get(kept, x.id);
    if (!rec && x.dir === "out" && conv) {
      const replica = await this.store.get("inbox", x.id);
      if (replica && replica.own && replica.fp) [kept, rec] = ["inbox", replica];
    }
    if (!rec || rec.control || rec.aside || rec.excerpt_pid || rec.sub || (rec.conv || "") !== conv) throw new Error("No such controllable message here.");
    const targetFp = kept === "outbox" ? this.fp : rec.fp;
    if (!targetFp) throw new Error("That message's sender key is not recorded here: it cannot be referred to.");
    const ref = { id: conv ? rec.lid : rec.id, fingerprint: targetFp };
    const deleted = await this.isRetracted(rec); // it shows nothing more to react to, edit or delete (client Agent.deleted)
    let sub, payload;
    if (what === "react") {
      if (!(x.remove ? wire.validEmoji : wire.oneEmoji)(x.emoji || "")) throw new Error("A reaction is one emoji."); // my own added under the older rule can still be taken off
      if (deleted) throw new Error("That message was deleted: it takes no reactions.");
      if (x.remove && !(await this.reactedHere(conv, ref, x.emoji))) throw new Error("There is no " + x.emoji + " reaction of yours on that message to remove.");
      sub = wire.SubReaction;
      payload = { emoji: x.emoji, op: x.remove ? "remove" : "add", n: await this.nextCounter(conv, ref, sub, x.emoji) };
    } else if (what === "edit" || what === "delete") {
      const mine = conv ? (await this.personOfFp(targetFp)) === (this.me && this.me.person) : targetFp === this.fp;
      if (!mine) throw new Error(conv ? "Only the sender's person edits or deletes a message." : "Only the sender edits or deletes a message.");
      if (deleted) throw new Error(what === "edit" ? "That message was deleted; it cannot be edited." : "That message was deleted already.");
      if (what === "edit") {
        const text = String(x.text || "");
        if (wire.blank(text) || new TextEncoder().encode(text).length > wire.MaxRevisionBytes) throw new Error("An edit is 1 to " + wire.MaxRevisionBytes + " bytes of text.");
        sub = wire.SubRevision;
        payload = { rev: await this.nextCounter(conv, ref, sub, ""), text };
      } else { sub = wire.SubRetraction; payload = {}; }
    } else throw new Error("No such message control.");
    const body = JSON.stringify(payload);
    const sent = conv ? await this.sendConvControl(await this.store.get("convs", conv) || await this.groupRecord(conv), ref, sub, body)
      : await this.sendDeviceControl(kept === "outbox" ? rec.to : rec.from, ref, sub, body);
    if (sub === wire.SubRetraction) for (;;) {
      const checks = [], drop = [...(await this.dropCached(rec, { control: true, sub, ref, conv: conv || "" })), ...(await this.blankRetracted(rec, kept, checks))];
      try { if (drop.length) await this.store.write(drop, checks); break; }
      catch (e) { if (!(e instanceof StoreConflict)) throw e; } // refresh custody; never resend the control
    }
    this.changed();
    const done = what === "react" ? (x.remove ? "Reaction removed." : "Reacted.")
      : what === "edit" ? "Edited. A question or task already sent keeps running on what was sent; the edit is shown beside it."
        : "Deleted here and on devices that can read deletions. What was already read, saved or given to an agent stays with them; nothing running was stopped.";
    return { note: sent.skipped.length ? done + " Not sent to " + sent.skipped.length + " device(s) that cannot read it yet: " + sent.skipped.join("; ") : done,
      id: sent.id, state: sent.state, skipped: sent.skipped };
  }

  async sendDeviceControl(peer, ref, sub, body, cap = wire.CapControl, sendID = "") {
    const pin = await this.sendKey(peer);
    let ok, why;
    try { [ok,why] = await this.ctlSupport(peer, pin, cap); }
    catch (e) { if (sub !== wire.SubRetraction || !retryable(e)) throw e; ok=false;why=e.message; }
    if (!ok && sub !== wire.SubRetraction) throw new Error(why);
    const recipient = await this.pubOf(pin);
    const id = sendID || wire.newID(), at = this.now();
    if(sendID){
      if(!wire.validID(sendID))throw Error("Invalid decision send ID.");
      const prior=await this.store.get("outbox",sendID);
      if(prior){
        if(prior.to!==peer||prior.sub!==sub||prior.body!==body||prior.ref?.id!==ref.id||prior.ref?.fingerprint!==ref.fingerprint||prior.recipient_fp!==pin.fingerprint)throw Error("Send ID already names a different decision or reader key.");
        if(prior.state==="failed" && cap===wire.CapContinuation){
          const retry={...prior,state:"queued",detail:""};await this.store.write([{s:"outbox",k:id,v:retry}],[{s:"outbox",k:id,v:prior}]);await this.post(retry);return {id,state:(await this.store.get("outbox",id)).state,skipped:[]};
        }
        return {id,state:prior.state,skipped:[]};
      }
    }
    const envelope = await wire.seal({ v: wire.Version3, id, from: this.address, to: peer, ts: Math.floor(at / 1000), kind: "message", sub, body, ref }, this.keys, recipient);
    const rec = { v: 3, control: true, id, to: peer, fp: this.fp, recipient_fp:pin.fingerprint, required_cap:cap, kind: "message", sub, body, ref, at, aside: true, state: "queued", detail: "", envelope };
    try {await this.commitControls([rec],sendID?[{s:"outbox",k:id,v:undefined}]:[]);} catch(e){
      if(!sendID || !(e instanceof StoreConflict))throw e;
      const prior=await this.store.get("outbox",id);
      if(!prior || prior.to!==peer||prior.sub!==sub||prior.body!==body||prior.ref?.id!==ref.id||prior.ref?.fingerprint!==ref.fingerprint)throw Error("Send ID already names a different decision.");
      return {id,state:prior.state,skipped:[]};
    }
    await this.post(rec);
    const now = await this.store.get("outbox", id);
    return { id, state: now.state, skipped: [] };
  }

  // sendConvControl sends one control about a conversation turn: one copy
  // per device of both persons, under one logical id, each only if it can
  // read controls; the others are named, never sent something else.
  async sendConvControl(c, ref, sub, body) {
    if (!c) throw new Error("No such conversation here.");
    if(c.kind==="group")return this.sendGroupControl(c,ref,sub,body);
    const { why: stop, peer } = await this.gate(c);
    if (stop) throw new Error(stop);
    const me = this.me;
    const devices = [...peer.devices.map((d) => ({ ...d, own: false })), ...me.devices.filter((d) => d.address !== this.address).map((d) => ({ ...d, own: true }))];
    const lid = wire.newID(), at = this.now();
    const fan = [{ person: me.person, roster: me.hash }, { person: peer.person, roster: peer.hash }]; // the rosters the copies go to, as a turn names them
    const recs = [], skipped = [];
    for (const dev of devices) {
      const pin = await this.store.get("pins", dev.address);
      if (!pin || pin.fingerprint !== dev.fingerprint || pin.pending) continue; // a changed key is never used
      let ok = false, why = "";
      try { [ok, why] = await this.ctlSupport(dev.address, pin); } catch (e) { why = "server_unavailable: cannot reach your server"; }
      if (!ok && sub !== wire.SubRetraction) { skipped.push(dev.address + ": " + why); continue; }
      const recipient = await this.pubOf(pin);
      const id = wire.newID();
      const envelope = await wire.seal({ v: wire.Version3, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), kind: "message",
        sub, body, ref, conv: c.id, lid, replica: dev.own, fan }, this.keys, recipient);
      recs.push({ v: 3, control: true, id, conv: c.id, lid, to: dev.address, recipient_fp:pin.fingerprint, required_cap:wire.CapControl, fp: this.fp, own: dev.own, person: me.person, kind: "message", sub, body, ref, at, aside: true, state: "queued", detail: "", envelope });
    }
    if (!recs.length) throw new Error("No device of this conversation can read reactions, edits or deletions yet" + (skipped.length ? ": " + skipped.join("; ") : "."));
    await this.commitControls(recs);
    for (const r of recs) await this.post(r);
    const least = copyOrder(recs);
    return { id: recs[0].id, state: least.state, skipped };
  }

  // A locally authored retraction and stopping its still-local execution
  // copies are one write. An upload or stale queue snapshot cannot restore
  // those copies. Proven custody stays custody; an attempted POST is uncertain.
  async commitControls(recs, authorityChecks = []) {
    for (;;) {
      const checks = [...authorityChecks], ops = recs.map(v => ({s:"outbox",k:v.id,v}));
      const control = recs.find(r => r.sub === wire.SubRetraction);
      if (control) ops.push(...await this.cancelRequestCopies(control.conv || "",control.ref,checks));
      try { await this.store.write(ops, checks); return; }
      catch (e) { if (!(e instanceof StoreConflict) || authorityChecks.length) throw e; }
    }
  }

  async cancelRequestCopies(conv, ref, checks) {
    const ops=[];
    if (ref.fingerprint !== this.fp) return ops;
    for (const row of await this.store.all("outbox")) {
      const request = row.receiver_setup?.request;
      const original = !row.control && !row.aside && (row.conv || "") === conv && (conv ? row.lid : row.id) === ref.id && ["question","task"].includes(row.kind);
      const wrapper = request && request.from_key === ref.fingerprint && (request.conv || "") === conv && (conv ? request.lid : request.id) === ref.id && ["question","task"].includes(request.kind);
      if ((!original && !wrapper) || !["queued","waiting","receiver_waiting","failed"].includes(row.state)) continue;
      checks.push({s:"outbox",k:row.id,v:row});
      ops.push({s:"outbox",k:row.id,v:{...row,delivery_cancelled:true,state:"failed",
        detail:row.handover_started === false ? "Deleted before handover; not sent." : "Deleted; delivery may have been attempted and is unconfirmed. Cancellation cannot be confirmed."}});
    }
    return ops;
  }

  cancellationView(row) {
    return row.delivery_cancelled ? {send_stopped:true,...(row.state === "failed" && row.handover_started !== false ? {delivery_uncertain:true} : {})} : {};
  }

  outStateText(row, peer) {
    if (this.cancellationView(row).delivery_uncertain) return "Delivery unconfirmed; local retries stopped. " + (row.detail || "");
    return outText(row.state,peer,row.detail);
  }

  // ---- what the page reads (the daemon page API's shapes)

  async overview(listArchived = true) {
    const persons = await this.store.all("persons");
    const people = persons.map((p) => this.personView(p));
    const listedSeen = new Set();
    for (const m of this.members.list) {
      const l = m.person && this.listed && this.listed.get(m.person.hash);
      if (!l || listedSeen.has(l.person) || persons.some((p) => p.person === l.person)) continue;
      listedSeen.add(l.person);
      people.push({ label: l.label, ...(l.picture ? { picture: l.picture, picture_url: this.pictureURL(l.picture) } : {}), address: l.devices[0].address, state: "listed",
        devices: l.devices.map((d) => ({ address: d.address, name: d.address.split("/")[1], fingerprint: d.fingerprint })) });
    }
    const inbox = await this.store.all("inbox");
    const outbox = await this.store.all("outbox");
    const pinned = persons.filter((p) => p.state === "pinned"); // names for a record's author (liveagent.go dmPeople.known)
    const words = await this.peerWordsFn(); // sentences name a person and device, never the address
    const dms = [], links = new Map(); // person → the agents their device runs in DMs here
    const privateReports = await this.ownNeedsYouReports(inbox, outbox);
    const needsYou = [], heldTurns = []; // what waits for this person (client.PageReview), by conversation
    for (const c of await this.store.all("convs")) {
      const peer = persons.find((p) => p.person === c.peer);
      let msgs = (await this.convMessages(c.id, inbox, outbox));
      const participations = await this.participationsOf(c), member = !!wire.rootMember(wire.parseRoot(c.root), this.me?.person);
      const originals = member ? [] : [...(await this.dmMembers(c)).values()];
      if (!member) msgs = msgs.filter(m => m.sub !== "event" || participations.some(p => p.pid === m.pid && (p.role !== "human" || p.host?.address === this.address && p.host.fingerprint === this.fp || p.decision && ["active", "dismissed"].includes(p.state))));
      msgs = await this.oneRowPerRecord(msgs, new Map(participations.filter(p => p.role === "human").map(p => [p.pid, p])));
      this.needsYouOf(c.id, participations, msgs, inbox.filter((r) => r.control && r.conv === c.id), needsYou, heldTurns, words, privateReports);
      if (this.erasedConv(c.id) && !msgs.some((m) => !m.sub)) continue; // deleted here, and no later turn: not listed until one comes
      for (const info of participations.filter(p => p.role !== "human")) {
        if (!info.host) continue;
        const ls = links.get(info.host.person) || [];
        let l = ls.find((x) => x.address === info.host.address);
        if (!l) ls.push(l = { address: info.host.address, dms: [] });
        l.dms.push({ conv: c.id, pid: info.pid, state: info.state });
        links.set(info.host.person, ls);
      }
      // Use the same resolved revisions/retractions as the open conversation.
      const presented = new Map((await this.dm(c.id)).messages.map(m => [m.id, m]));
      const line = (m) => (presented.get(m.id)?.deleted ? "Message deleted" : presented.get(m.id)?.edited ? firstLine(presented.get(m.id).text || "") : m.sub === "event" ? this.eventText(m.body, peer, null, participations.find(p => p.pid === m.pid && p.role === "human") || false, { originals, member })
        : !m.body && (m.attachments || []).length ? "📎 " + m.attachments.map((a) => wire.safeName(a.name)).join(", ") // files only: their names
          : firstLine(m.body));
      const last = msgs.length ? this.lastEvent(msgs.at(-1), [peer, ...originals], pinned) : undefined;
      dms.push({ id: c.id, peer: this.personView(peer), ...(member ? {} : { members: originals.map(p => this.personView(p)) }), role: member ? "member" : participations.some(p => p.role === "human" && p.host?.address === this.address && p.host.fingerprint === this.fp) ? "human_guest" : "visitor", created: iso(c.created * 1000), mine: c.creator === this.address, count: msgs.length,
        title: msgs[0] ? line(msgs[0]) : "", last: msgs.length ? line(msgs[msgs.length - 1]) : "",
        last_at: iso(msgs.length ? msgs[msgs.length - 1].at : c.created * 1000),
        unread: msgs.filter((m) => m.fp && !m.own && !m.read).length, held: msgs.filter((m) => this.heldOpen(m, msgs)).length,
        waiting: msgs.filter((m) => m.state === "waiting").length,
        guests: participations.filter((p) => p.state === "active").length, decide: 0, // who is present to help; decisions counted below
        ...(last ? { last_event: last } : {}) });
    }
    dms.sort((a, b) => b.last_at.localeCompare(a.last_at));
    for(const g of (await this.store.all("kv")).filter(v=>v?.root&&v?.context&&Array.isArray(v.records))) {
      const packet=wire.parseGroupContext(g.context), conv=packet.state.conv, msgs=(await this.convMessages(conv,inbox,outbox));
      const view=await this.groupThread(conv), shown=new Set(view.messages.map(m=>m.id)),visible=msgs.filter(m=>shown.has(m.id));
      const parts=await this.participationsOf({id:conv,kind:"group",root:g.root}).catch(()=>[]),last=visible.length?this.lastEvent(visible.at(-1),view.members):undefined;
      this.needsYouOf(conv,parts.filter(p=>p.role!=="human"),visible,inbox.filter(r=>r.control&&r.conv===conv),needsYou,heldTurns,words,privateReports);
      if(this.erasedConv(conv)&&!visible.some(m=>!m.sub))continue; // deleted here, and no later turn
      const latestView = view.messages.at(-1);
      const latestText = !latestView ? "" : latestView.deleted ? "Message deleted" : firstLine(latestView.edited ? latestView.text || "" : latestView.event || latestView.body);
      dms.push({id:conv,kind:"group",title:packet.state.title,peer:{label:packet.state.title,address:"",state:""},members:view.members,role:view.role,frozen:view.frozen,created:iso(packet.root.created*1000),mine:packet.root.creator.address===this.address,count:visible.length,last:latestText,last_at:iso(visible.length?visible.at(-1).at:packet.root.created*1000),unread:visible.filter(m=>m.fp&&!m.own&&!m.read).length,held:0,waiting:visible.filter(m=>m.state==="waiting"||m.state==="queued").length,
        guests:parts.filter(p=>p.state==="active").length,decide:0,...(last?{last_event:last}:{})});
    }
    // decide: a conversation's requests this person decides here (live.go
    // countDecisions); a browser runs no agent, so its items carry none.
    for (const d of dms) d.decide = needsYou.filter((x) => x.conv === d.id && x.id && (x.actions || []).length).length;
    for (const p of people) if (links.has(p.person)) p.agents = links.get(p.person);
    const { threads, topics } = await this.topicOverview(listArchived); // ?topics=1: archived topics counted, not listed
    const held = (await this.store.all("held")).filter(h => !h.notice_archived);
    const now = Math.floor(this.now() / 1000);
    const link = this.link && !["linked", ""].includes(this.link.state) ? { state: this.link.state === "pending" && now >= this.link.expires ? "expired" : this.link.state, detail: this.link.detail || "" } : undefined;
    const history = Object.values(await this.historyBook()).map((j) => ({ device: j.device, name: j.device.split("/")[1], done: j.done, total: j.total, state: j.state }));
    const asks = (await this.linkRequests()).filter((r) => r.state === "pending")
      .map((r) => ({ id: r.id, address: r.address, name: r.address.split("/")[1], fingerprint: r.fingerprint, requested_at: iso(r.requested_at * 1000), expires: iso(r.expires * 1000), state: r.state }));
    return {
      demo: false, seq: this.seq, version: this.version, release: "",
      me: { address: this.address, fingerprint: this.fp, responder: "", responder_dir: "", browser: true, agent: false }, // a browser runs no agent
      workspace: { name: this.workspaceName, server: this.relayHost() }, agent_devices: [...this.agentDevices],
      device: { online: this.connected, revoked: this.revoked, persisted: this.storage ? this.storage.persisted : null },
      threads, topics, topic_list: true, review: [...this.reportItems(await this.store.all("inbox"), await this.store.all("outbox"), words), ...await this.deviceAdminReview()], needs_you: needsYou, held: heldTurns, quarantine: held.map(h => ({ ...quarantineItem(h), reason: holdText(h.reason, words(h.from)) })),
      directory: { status: this.members.listed, current: this.members.current, at: this.members.at ? iso(this.members.at) : undefined,
        truncated: this.members.truncated, members: this.members.list.filter((m) => m.address !== this.address)
          .map((m) => ({ address: m.address, presence: this.members.current ? m.presence : "", joined: iso((m.joined || 0) * 1000) })) },
      notify: await this.notifyView(),
      files: { max_file: wire.BrowserMaxFile, max_message: wire.BrowserMaxMessage, max_count: 8 },
      persons: true, agents: true, // agents on the other person's computer: invited, asked and dismissed here, never run here
      groups:true, group_invitations:await this.groupInvitations(),
      person: this.personView(this.me, this.me ? { published: !!this.me.published } : undefined) || undefined, people, dms,
      // A browser is always a person's device (never a service).
      role: this.me ? "person" : "unset", link, links: asks.length ? asks : undefined, history: history.length ? history : undefined,
    };
  }

  async dm(id) {
    if(await this.groupRecord(id))return this.groupThread(id);
    const c = await this.store.get("convs", id);
    if (!c) throw new Error("No conversation with that id.");
    const peer = await this.store.get("persons", c.peer);
    const inbox = await this.store.all("inbox"), outbox = await this.store.all("outbox");
    const privateReports = await this.ownNeedsYouReports(inbox, outbox);
    let msgs = (await this.convMessages(id, inbox, outbox));
    const ctls = [...inbox.filter((r) => r.control && r.conv === id), ...outbox.filter((r) => r.control && r.conv === id).map((r) => ({ ...r, dir: "out" }))];
    const myLabel = this.me && this.me.label ? this.me.label : "You";
    const personLabel = (pid) => (pid === (this.me && this.me.person) ? myLabel : peer && pid === peer.person ? peer.label : "someone");
    const controlsOf = (m, here) => {
      const targetFp = here ? this.fp : m.fp, mineAuthor = async () => (await this.personOfFp(targetFp)) === (this.me && this.me.person);
      return { targetFp, mine: mineAuthor };
    };
    const execView = (m, here) => {
      if (!m.target || (m.kind !== "question" && m.kind !== "task")) return {};
      const answered = msgs.some((r) => r.fp && shownReply(r.reply_to) === m.id && (r.kind === "answer" || r.kind === "result"));
      const e = this.execOn(ctls.filter((x) => x.sub === wire.SubStatus && x.ref && x.ref.id === m.lid && x.ref.fingerprint === (here ? this.fp : m.fp) && x.from === m.target.address), m.target.address, answered);
      const continuation=this.continuationOf(m,here?this.fp:m.fp,e);
      return e ? { exec: e, ...(continuation?{continuation,actions:["continue"]}:{}) } : {};
    };
    const hostLabels = new Map(rel0(ctls) ? (await this.participationsOf(c)).map((p) => [p.pid, p.host?.label || ""]) : []); // client: the participation host's label
    const ctlView = async (m, here) => {
      const { targetFp, mine } = controlsOf(m, here);
      const targetPerson = await this.personOfFp(targetFp);
      const rel = ctls.filter((x) => x.ref && x.ref.id === m.lid && x.ref.fingerprint === targetFp && x.sub !== wire.SubStatus && x.sub !== wire.SubDecision);
      const view = this.controlsOn(rel, targetPerson, (x) => x.person || "", personLabel, (p) => p === (this.me && this.me.person), undefined, (pid) => hostLabels.get(pid));
      view.can = view.deleted || !wire.rootMember(wire.parseRoot(c.root), this.me?.person) ? [] : ["react", ...((await mine()) ? ["edit", "delete"] : [])];
      return view;
    };
    const member = !!wire.rootMember(wire.parseRoot(c.root), this.me?.person);
    // A guest sees its own participation, and others only once accepted: a
    // shared invitation alone stays inert proof (livehuman.go guestViews).
    const guests = (await this.participationsOf(c)).filter(p => p.role === "human" && (member || p.host?.address === this.address && p.host.fingerprint === this.fp || p.decision && ["active", "dismissed"].includes(p.state))).map(p => this.guestView(p, member));
    const assistants = new Set((await this.agentsOf(c)).filter(p => p.decision).map(p => p.pid)); // an assistant a guest learned from its public scope and acceptance
    if (!member && guests.some(g => g.host_here)) msgs = msgs.filter(m => m.sub !== "event" || guests.some(g => g.pid === m.pid) || assistants.has(m.pid)); // shared records of an unaccepted participation stay out of a guest's timeline
    const guestActive = !member && guests.some(g => g.host_here && g.state === "active" && !g.held);
    const words = await this.peerWordsFn(); // sentences name a person and device, never the address
    // A guest or visitor sees both verified original people (client.Conversations Members).
    const originals = member ? [] : [...(await this.dmMembers(c)).values()];
    const parts = new Map((await this.participationsOf(c)).map(p => [p.pid, p]));
    const humans = new Map([...parts].filter(([, p]) => p.role === "human"));
    msgs = await this.oneRowPerRecord(msgs, humans);
    const shownReply = linkReplies(msgs, this.fp);
    const pinned = (await this.store.all("persons")).filter((p) => p.state === "pinned"); // names for a record's author (liveagent.go dmPeople.known)
    return { id, peer: this.personView(peer), ...(member ? {} : { members: originals.map(p => this.personView(p)) }), role: member ? "member" : guests.some(p => p.host_here) ? "human_guest" : "visitor", guests, audience_pending: guests.some(p => p.audience_pending), created: iso(c.created * 1000), mine: c.creator === this.address,
      frozen: peer && peer.state === "conflict" ? peer.address + " published a different person record than the one kept here, so this conversation is frozen: nothing more is sent in it." : "",
      agents: (await this.agentsOf(c)).map((info) => { const view = this.agentView(info, msgs, peer); if (!member) { view.can_ask = guestActive && view.can_ask; view.can_dismiss = false; if (view.can_ask) view.state_text = "In this conversation, on " + info.host.address + ". Ask it with @mention; its owner's permissions decide whether it runs."; } return view; }), // an accepted guest addresses active assistants under their owners' permissions
      messages: await Promise.all(msgs.map(async (m) => {
        const here = !m.fp && !m.excerpt_pid, out = here || !!m.own; // claimed excerpts have no verified original author key
        const state = m.state === "conv_held" && !this.heldOpen(m, msgs) ? "manual" : m.state; // answered here (client.turnClosesHeld)
        const event = m.sub === "event" ? this.eventText(m.body, peer, null, humans.get(m.pid) || false, { originals, member }) : "";
        const ev = event ? this.eventFields(m.body, [peer, ...originals], pinned) : null;
        return { id: m.id, lid: m.lid, dir: out ? "out" : "in", from: here ? this.address : m.from, kind: m.kind, status: m.status || "", actions: await this.proposalActions(m), body: event ? "" : m.body, reply_to: shownReply(m.reply_to),quote:shownReply(m.quote),sent_at:sentAt(m),delivery:m.delivery||"",
          ...(ev && ev.type ? { event_type: ev.type, event_by: ev.by } : {}),
          ...(m.agent_id ? { agent_id: m.agent_id } : {}), ...(m.target ? { target: m.target } : {}),
          send_group:m.send_group_conflict?"":m.send_group||"", send_group_author:here?this.fp:m.fp||m.claimed_key||"", origin: m.origin || "", verified_agent: verifiedAgent(m, parts.get(m.human&&wire.agentAuthor(m.human)?m.human.author_pid:m.pid), here ? this.address : m.from, here ? this.fp : m.fp),
          job_detail: this.needsYouText(m, privateReports),
          state, detail: m.detail || "", ...this.cancellationView(m), at: iso(m.at), unread: !out && !m.read, replica: !!m.replica,
          pid: m.pid || "", to: m.target ? m.target.address : "", event, via: m.own && !m.history ? m.from : "", copies: here ? await this.shownCopies(m.copies) : undefined,
          ...(m.excerpt_pid ? { excerpt_pid: m.excerpt_pid, claimed_key: m.claimed_key } : {}),
          synced_from: m.history ? m.synced_from : "",
          attachments: await Promise.all((m.attachments || []).map(async (a, i) => ({ index: i, name: wire.safeName(a.name), size: a.size, ...(here ? await this.sentState(a) : this.fileState(a)) }))),
          ...(event ? {} : m.excerpt_pid ? { can: [], reactions: [] } : await ctlView(m, here)), ...(event || m.excerpt_pid ? {} : execView(m, here)),
          state_text: event ? "" : here ? this.outStateText(m, words(m.lagging || (peer ? peer.address : ""))) : state === "conv_held" ? "Held for you: nothing runs it. Answer here if you want to." : state === "manual" ? "Replied by hand" : "" };
      })) };
  }

  // ---- agents in DMs: shown, invited, asked and dismissed here, as the
  // core resolves them; never hosted, accepted or run (this browser runs
  // nothing).

  // roomCopy is client roomCopy (ROOM_V1 §2.5): a copy that is a room
  // shape, which goes only to a reader of rm1 besides its own requirement:
  // an event of a room participation, a captured audience on a group, with
  // a room scope or an agent author, or on an edit; history by its item.
  async roomCopy(rec) {
    let { sub, body, human } = rec;
    if (sub === "history") { try { const item = wire.parseHistory(body); ({ sub, body } = item); human = item.human; } catch (e) { return false; } }
    if (human) return [wire.SubRevision, wire.SubRetraction].includes(sub) || wire.agentAuthor(human) || human.proof.some((e) => e.audience === "room") ||
      rec.required_cap === wire.CapGroup || !!(rec.conv && await this.store.get("kv", "group/" + rec.conv));
    if (sub !== "event") return false;
    let e;
    try { e = wire.parseEvent(body); } catch (err) { return false; }
    return e.audience === "room" || (await this.convEvents(rec.conv, null, e.pid)).some((x) => x.e.audience === "room");
  }
  async requireRoomSupport(address, pin) {
    const profile = await this.profile(address), key = await this.pubOf(pin);
    if (!await wire.profileSupports(profile || {}, address, key.sign_key, wire.CapRoom)) throw Object.assign(new Error(address + " cannot read room participation yet; update all its active AgentNet sessions."), { code: "room_unsupported" });
  }

  async requireGroupHumanSupport(address, pin) {
    const profile=await this.profile(address),key=await this.pubOf(pin);
    if(!await wire.profileSupports(profile||{},address,key.sign_key,wire.CapGroupHumanParticipation))throw Object.assign(Error(address+" needs an update for group human guests."),{code:"human_unsupported",address});
  }

  // Human support is distinct from agent hosting. Never fall back to apx1.
  async requireHumanSupport(address, pin) {
    const before = await this.store.get("pins", address);
    if (!pin || pin.pending || !before || before.pending || before.fingerprint !== pin.fingerprint) throw new Error("Human participant key is not currently pinned.");
    const pub = await this.pubOf(pin), profile = await this.profile(address), after = await this.store.get("pins", address);
    if (!after || after.pending || after.fingerprint !== pin.fingerprint) throw new Error("Human participant key changed.");
    if (!await wire.profileSupports(profile || {}, address, pub.sign_key, wire.CapHumanParticipation)) throw Object.assign(new Error(address + " cannot read human participation yet; update all active sessions."), { code: "human_unsupported", address });
  }

  async humanPlan(c, authorPID = "") {
    if (c.kind === "group") {
      const events=await this.convEvents(c.id),members=await this.dmMembers(c,events);
      if(!authorPID)return this.roomPlan(c,events,members);
      const guest=this.resolveAgent(authorPID,events,members);
      if(guest.role!=="human" || guest.state!=="active" || guest.held || guest.host?.address!==this.address || guest.host.fingerprint!==this.fp || members.has(this.me?.person))throw Error("Only the exact active guest device writes as that guest.");
      const h=await this.roomPlan(c,events,members);
      if(!h)throw Error("Human participation is not active.");
      h.author_pid=authorPID;await wire.validateHumanTurn(h,c.id);return h;
    }
    const root = wire.parseRoot(c.root), member = !!wire.rootMember(root, this.me?.person);
    if (member && authorPID || !member && !authorPID) throw new Error("Choose the exact accepted human participation for this conversation's author.");
    let events = await this.convEvents(c.id), members = await this.dmMembers(c, events);
    let infos = [...new Set(events.map(x => x.e.pid))].map(pid => this.resolveAgent(pid, events, members)).filter(p => p.role === "human");
    for (const p of infos) if (p.state === "active") {
      if (p.held) throw new Error("Human audience evidence is pending; review again.");
      await this.refreshPerson(p.host);
    }
    events = await this.convEvents(c.id); members = await this.dmMembers(c, events);
    infos = [...new Set(events.map(x => x.e.pid))].sort().map(pid => this.resolveAgent(pid, events, members)).filter(p => p.role === "human" && p.state === "active");
    const h = { ...(authorPID ? { author_pid: authorPID } : {}), audience: [], proof: [] };
    for (const p of infos) {
      if (p.held) throw new Error("Human audience changed during send; review again.");
      const accept = events.find(x => x.hash === p.decision)?.e;
      if (!accept) throw new Error("Human acceptance proof missing.");
      const scope = await this.participationScope(c, p, events).catch(() => { throw new Error("Human audience evidence is pending; review again."); }); // the public projection: never the invitation's note or grant
      h.audience.push({ pid: p.pid, invite: p.invite, decision: p.decision }); h.proof.push(scope, accept);
    }
    if (!h.audience.length) { if (authorPID) throw new Error("Human participation is not active."); return null; }
    await wire.validateHumanTurn(h, c.id);
    return h;
  }

  // TODO(integrate:P4): retain atomic consent ops when merging commitReceiverCopies proposal changes.
  // TODO(integrate:P5a): share/member/author fields must retain Go parity on the host contract.
  roomConsentOps(conv,events,info) {
    if(!info.member || info.held || !["active","dismissed"].includes(info.state))return [];
    const counted=new Set([info.invite,info.scope,info.decision,info.dismissal]);
    return events.filter(x=>counted.has(x.hash)||info.shares.includes(wire.eventJSON(x.e))).map(x=>({s:"kv",k:"room-event/"+conv+"/"+x.hash,v:{pid:info.pid}}));
  }

  roomReaderKey(conv,ref,person,admission) { return "room-reader/"+conv+"/"+ref.fingerprint+"/"+ref.lid+"/"+person+"/"+admission; }

  async roomStoredOps(conv,checks,n=null,fp=this.fp,events=null,members=null,packet=null) {
    events ||= await this.convEvents(conv,checks);
    members ||= await this.dmMembers(await this.groupRecord(conv),events,checks,packet);
    const ops=[];
    for(const pid of new Set(events.map(x=>x.e.pid)))ops.push(...this.roomConsentOps(conv,events,this.resolveAgent(pid,events,members)));
    if(n && !n.sub)for(const p of members.values()) {
      // A received copy proves its signed sender/receiver fan only. The
      // sender's commit fences the exact sealed packet and retains its roster.
      if(fp!==this.fp && !n.fan?.some(f=>f.person===p.person))continue;
      const admission=members.epochs.get(p.devices[0]?.fingerprint);
      if(admission)ops.push({s:"kv",k:this.roomReaderKey(conv,{lid:n.lid,fingerprint:fp},p.person,admission),v:true});
    }
    return ops;
  }

  async roomMemberships(c,events,members) {
    const selected=new Set();
    for(const pid of new Set(events.map(x=>x.e.pid))) {
      const p=this.resolveAgent(pid,events,members);
      if(!p.member || p.held || !(p.state==="active" || p.state==="dismissed" && p.decision && p.dismissal))continue;
      if(events.find(x=>x.hash===p.decision)?.e.type!=="accept")continue;
      if(p.state==="dismissed" && !await this.roomMembershipMayDismiss(members.group,(events.find(x=>x.hash===p.invite)||events.find(x=>x.hash===p.scope))?.e,events.find(x=>x.hash===p.dismissal)?.e))continue;
      for(const hash of [p.invite,p.scope,p.decision,p.dismissal])if(hash)selected.add(hash);
      for(const raw of p.shares)selected.add(await wire.eventHash(wire.parseEvent(raw)));
    }
    return events.filter(x=>selected.has(x.hash)).map(x=>x.e);
  }

  async roomMembershipMayDismiss(packet,inv,e) {
    if(!inv?.host || !inv.group || e?.type!=="dismiss")return false;
    const member=wire.groupMember(packet.state,e.author.person),host=e.author.person===inv.host.person&&e.author.address===inv.host.address&&e.author.fingerprint===inv.host.fingerprint;
    return host || e.author.person===inv.author.person || member && !await wire.groupWithdrawn(packet.state,member,packet.withdrawals||[]) && e.author.group_admission===await wire.groupAdmissionHash(member.admission) && (inv.group.host_role==="member" || member.admin);
  }

  async roomMembershipOps(packet,checks) {
    if(!packet.memberships?.length)return [];
    const conv=packet.state.conv, grouped=new Map(), ops=[], kept=await this.groupRead(checks,"kv","room-memberships/"+conv)||[];
    for(const e of packet.memberships) {
      if(e.conv!==conv || e.role || !["invite","scope","accept","share","dismiss"].includes(e.type))throw new Hold("invalid","Unrelated membership carrier record.");
      const p=await this.groupRead(checks,e.author.person===this.me.person?"kv":"persons",e.author.person===this.me.person?"person":e.author.person),pin=await this.sendKey(e.author.address),pub=await this.pubOf(pin);
      if(!p || p.state==="conflict" || !p.hashes.includes(e.author.roster) || !p.devices.some(d=>d.address===e.author.address&&d.fingerprint===e.author.fingerprint) || pin.fingerprint!==e.author.fingerprint)throw new Hold("proof_pending","Membership author exact proof missing.");
      try{await wire.verifyEvent(e,pub.sign_key);}catch(err){throw new Hold("invalid",err.message);}
      const group=await this.groupRead(checks,"kv","group/"+conv);
      if(e.group && (!group?.records[e.group.seq] || wire.parseGroupCommit(group.records[e.group.seq]).hash!==e.group.hash || e.group.seq>packet.state.seq))throw new Hold("invalid","Membership original binding differs.");
      grouped.set(e.pid,[...(grouped.get(e.pid)||[]),e]);
    }
    for(const [pid,evs] of grouped) {
      const indexed=await this.groupRead(checks,"kv","room-pid/"+pid);
      if(indexed && indexed!==conv)throw new Hold("invalid","Membership ID names another conversation.");
      ops.push({s:"kv",k:"room-pid/"+pid,v:conv});
      const invites=evs.filter(e=>e.type==="invite"&&e.group&&e.audience==="room"&&!e.until), inv=invites[0];
      if(!inv || invites.some(e=>wire.eventJSON(e)!==wire.eventJSON(inv)))throw new Hold("invalid","Membership needs one original invite.");
      const invite=await wire.eventHash(inv);
      if(inv.group.host_role==="visitor") {
        const group=await this.groupRead(checks,"kv","group/"+conv);
        const current=wire.groupMember(packet.state,inv.author.person);
        if(!current?.admin || !wire.parseGroupCommit(group.records[inv.group.seq]).admins.includes(inv.author.person))throw new Hold("invalid","Outside agent invitation needs a group administrator.");
      }
      const exact=e=>e.prev===invite&&e.author.person===inv.host.person&&e.author.address===inv.host.address&&e.author.fingerprint===inv.host.fingerprint;
      if(!evs.some(e=>e.type==="accept"&&exact(e)))throw new Hold("invalid","Membership needs exact host consent.");
      const accepts=await Promise.all(evs.filter(e=>e.type==="accept"&&exact(e)).map(wire.eventHash));
      for(const e of evs) {
        if(e.type==="dismiss") {
          if(!await this.roomMembershipMayDismiss(packet,inv,e) || e.prev!==invite&&!accepts.includes(e.prev))throw new Hold("invalid","Membership dismissal needs an authorized author and exact invitation or acceptance.");
        }
        if(e.type==="accept"&&!exact(e) || e.type==="scope"&&!wire.sameScope(e,await wire.scopeOf(inv,e.ts)) || e.type==="share"&&(e.prev!==invite||!["person","address","fingerprint","agent_id"].every(k=>(e.host?.[k]||"")===(inv.host[k]||""))))throw new Hold("invalid","Membership scope differs.");
        const hash=await wire.eventHash(e), raw=wire.eventJSON(e);
        if(!kept.includes(raw))kept.push(raw);
        if(e.type!=="share")ops.push({s:"kv",k:"room-event/"+conv+"/"+hash,v:{pid}});
      }
    }
    ops.push({s:"kv",k:"room-memberships/"+conv,v:kept});return ops;
  }

  async roomPlan(c,events,members) {
    const h={audience:[],proof:[]};
    for(const pid of new Set(events.map(x=>x.e.pid))) {
      const p=this.resolveAgent(pid,events,members);
      if(!follows(p)||p.state!=="active"||p.held)continue;
      const accept=events.find(x=>x.hash===p.decision)?.e;
      if(!accept)throw Error("Room acceptance proof missing.");
      h.audience.push({pid:p.pid,invite:p.invite,decision:p.decision});
      h.proof.push(await this.participationScope(c,p,events),accept);
    }
    if(!h.audience.length)return null;
    await wire.validateHumanTurn(h,c.id);return h;
  }

  // participationScope is client.participationScope: a participation's held
  // public scope, or, for this device's own DM invite, a new one signed and
  // sent; nobody else's invitation is ever shared in its place.
  async participationScope(c, info, events) {
    const held = info.scope && events.find(x => x.hash === info.scope);
    if (held) return held.e;
    const inv = events.find(x => x.hash === info.invite)?.e;
    if (inv?.type !== "invite" || inv.group && inv.audience!=="room" || inv.author.address !== this.address || inv.author.fingerprint !== this.fp) throw new Error("Participation's public scope is not held here yet.");
    const s = await wire.signEvent(this.keys, await wire.scopeOf(inv, Math.floor(this.now() / 1000)));
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(s), pid: s.pid });
    return s;
  }

  async humanEvidence(c, h, checks = []) {
    await wire.validateHumanTurn(h, c.id);
    if (c.kind === "group") return this.groupHumanEvidence(c, h, checks);
    const root = wire.parseRoot(c.root), people = new Map();
    const ids = new Set([...root.members.map(m => m.person), ...h.proof.map(e => e.author.person), ...h.proof.filter(e => e.type === "scope").map(e => e.host.person)]);
    for (const id of ids) {
      const p = await this.groupRead(checks, id === this.me?.person ? "kv" : "persons", id === this.me?.person ? "person" : id);
      if (!p || !["self", "pinned"].includes(p.state)) throw new Hold("proof_pending", "Human person proof is missing or frozen.");
      people.set(id, p);
    }
    const members = new Map(); members.hosts = new Map();
    for (const m of root.members) {
      const p = people.get(m.person);
      if (!p.hashes.includes(m.roster)) throw new Hold("proof_pending", "Original DM member chain is missing.");
      members.set(m.person, p);
    }
    const creator = people.get(root.creator.person), dev = creator?.known.find(d => d.address === root.creator.address && d.fingerprint === root.creator.fingerprint);
    if (!dev || !creator.hashes.includes(root.creator.roster)) throw new Hold("proof_pending", "Original DM creator proof is missing.");
    try { await wire.verifyRoot(root, (await wire.parsePublic(JSON.parse(dev.json))).sign_key); } catch (e) { throw new Hold("invalid", e.message); }
    const identity = await this.groupRead(checks, "kv", "identity");
    if (identity?.address !== this.address || identity.fingerprint !== this.fp || identity.revoked) throw new Hold("invalid", "Local human identity changed.");
    const invitations = new Map();
    for (const e of h.proof) {
      const p = people.get(e.author.person), pin = await this.groupRead(checks, "pins", e.author.address);
      if (!p.hashes.includes(e.author.roster) || !p.devices.some(d => d.address === e.author.address && d.fingerprint === e.author.fingerprint) || !pin || pin.pending || pin.fingerprint !== e.author.fingerprint) throw new Hold("proof_pending", "Human event author is not a current pinned device.");
      try { await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key); } catch (err) { throw new Hold("invalid", err.message); }
      if (e.type === "scope") { // the invitation's public projection: never its note, grant or task keys
        const host = people.get(e.host.person), hostPin = await this.groupRead(checks, "pins", e.host.address);
        if (!members.has(e.author.person) && !members.roomEvents?.has(await wire.eventHash(e)) || members.has(e.host.person) && !roomAgentScope(e) || !host.devices.some(d => d.address === e.host.address && d.fingerprint === e.host.fingerprint) || !hostPin || hostPin.pending || hostPin.fingerprint !== e.host.fingerprint) throw new Hold("invalid", "Human invitation does not bind original member and outside host.");
        if (!members.has(e.host.person)) members.hosts.set(host.person, host);
        invitations.set(e.pid, e);
      }
    }
    for (const e of h.proof.filter(e => e.type === "accept")) {
      const inv = invitations.get(e.pid);
      if (!inv || e.prev !== inv.prev || e.author.person !== inv.host.person || e.author.address !== inv.host.address || e.author.fingerprint !== inv.host.fingerprint) throw new Hold("invalid", "Human acceptance differs from its exact invitation host.");
    }
    const events = [...await this.convEvents(c.id, checks), ...await Promise.all(h.proof.map(async e => ({ e, hash: await wire.eventHash(e) })))];
    const unique = [...new Map(events.map(e => [e.hash, e])).values()], scopes = new Map();
    for (const s of h.audience) {
      const p = this.resolveAgent(s.pid, unique, members);
      if (!follows(p) || p.invite !== s.invite || p.decision !== s.decision) throw new Hold("proof_pending", "Captured human consent differs from local proof.");
      scopes.set(s.pid, p);
    }
    return { members, scopes };
  }

  // groupHumanEvidence is humanEvidence in a group (ROOM_V1 §3, client
  // verifyHumanProof and humanAuthority): the members are the verified
  // current context's (dmMembers), and a scope counts once its group
  // binding does (dmMembers' groupInvites, the proof's own scopes too).
  async groupHumanEvidence(c, h, checks, historyPacket=null) {
    const identity = await this.groupRead(checks, "kv", "identity");
    if (identity?.address !== this.address || identity.fingerprint !== this.fp || identity.revoked) throw new Hold("invalid", "Local human identity changed.");
    const records = [...(historyPacket ? await Promise.all(historyPacket.memberships.map(async e=>({e,hash:await wire.eventHash(e)}))) : await this.convEvents(c.id, checks)), ...await Promise.all(h.proof.map(async e => ({ e, hash: await wire.eventHash(e) })))];
    const unique = [...new Map(records.map(e => [e.hash, e])).values()], members = await this.dmMembers(c, unique, checks, historyPacket), invitations = new Map();
    for (const e of h.proof) {
      const p = await this.groupRead(checks, e.author.person === this.me?.person ? "kv" : "persons", e.author.person === this.me?.person ? "person" : e.author.person), pin = await this.groupRead(checks, "pins", e.author.address);
      if (!p || !["self", "pinned"].includes(p.state) || !p.hashes.includes(e.author.roster) || !p.devices.some(d => d.address === e.author.address && d.fingerprint === e.author.fingerprint) || !pin || pin.pending || pin.fingerprint !== e.author.fingerprint) throw new Hold("proof_pending", "Human event author is not a current pinned device.");
      try { await wire.verifyEvent(e, (await this.pubOf(pin)).sign_key); } catch (err) { throw new Hold("invalid", err.message); }
      if (e.type === "scope") {
        const host = members.get(e.host.person) || members.hosts.get(e.host.person), hostPin = await this.groupRead(checks, "pins", e.host.address);
        if (!members.has(e.author.person) && !members.roomEvents?.has(await wire.eventHash(e)) || members.has(e.host.person) && !roomAgentScope(e) || !host?.devices.some(d => d.address === e.host.address && d.fingerprint === e.host.fingerprint) || !hostPin || hostPin.pending || hostPin.fingerprint !== e.host.fingerprint) throw new Hold("invalid", "Human invitation does not bind a current member and its exact host.");
        invitations.set(e.pid, e);
      }
    }
    for (const e of h.proof.filter(e => e.type === "accept")) {
      const inv = invitations.get(e.pid);
      if (!inv || e.prev !== inv.prev || e.author.person !== inv.host.person || e.author.address !== inv.host.address || e.author.fingerprint !== inv.host.fingerprint) throw new Hold("invalid", "Human acceptance differs from its exact invitation host.");
    }
    const scopes = new Map();
    for (const s of h.audience) {
      const p = this.resolveAgent(s.pid, unique, members);
      if (!follows(p) || p.invite !== s.invite || p.decision !== s.decision) throw new Hold("proof_pending", "Captured human consent differs from local proof.");
      scopes.set(s.pid, p);
    }
    return { members, scopes };
  }

  // humanTurnAuthorization is client.humanTurnAuthorization: on a request
  // addressed to assistant participation x its exact host is also a reader,
  // and that host is its output's author. Scope grants no execution.
  humanTurnAuthorization(n, evidence, x, from, fromFP, to, toFP, historical = false) {
    const h = n.human;
    if (!n.pid || n.pid === (h.author_pid || "")) return this.humanAuthorization(h, evidence, from, fromFP, to, toFP, false, false, historical);
    if (!x || x.role === "human" || !x.invite || !x.host) throw new Hold("invalid", "Addressed human turn names no assistant participation.");
    const retained = historical && x.state === "dismissed" && !x.held && !x.conflict && !!x.decision;
    const live = x.state === "active" && !x.held || retained, exact = (a, f) => x.host.address === a && x.host.fingerprint === f;
    if (n.target) {
      if (n.target.address !== x.host.address || n.target.fingerprint !== x.host.fingerprint || (n.target.agent_id || "") !== (x.agent_id || "") || !live) throw new Hold("invalid", "Request does not name the exact active assistant.");
      return this.humanAuthorization(h, evidence, from, fromFP, to, toFP, false, exact(to, toFP), historical);
    }
    if (h.author_pid || !exact(from, fromFP) || (n.agent_id || "") !== (x.agent_id || "") || !live) throw new Hold("invalid", "Assistant output is not from its exact active host.");
    return this.humanAuthorization(h, evidence, from, fromFP, to, toFP, true, false, historical);
  }
  async assistantOf(c, n, checks) {
    if (!n.pid || n.pid === (n.human?.author_pid || "")) return null;
    const events = await this.convEvents(c.id, checks);
    return this.resolveAgent(n.pid, events, await this.dmMembers(c, events, checks));
  }

  humanAuthorization(h, evidence, from, fromFP, to, toFP, hostAuthor = false, hostReader = false, historical = false) {
    const member = (address, fp) => [...evidence.members.values()].some(p => p.devices.some(d => d.address === address && d.fingerprint === fp));
    const memberReader = member(to, toFP);
    if (historical && !memberReader) throw new Hold("invalid", "Human history remains with original members' own linked devices.");
    let sender = !h.author_pid && (member(from, fromFP) || hostAuthor), reader = memberReader || hostReader;
    for (const [pid, p] of evidence.scopes) {
      if (pid === h.author_pid && (p.role === "") !== wire.agentAuthor(h)) throw new Hold("invalid", "The author's role differs from its captured scope.");
      const author = pid === h.author_pid && p.host.address === from && p.host.fingerprint === fromFP;
      const recipient = p.host.address === to && p.host.fingerprint === toFP;
      const retained = historical && p.state === "dismissed" && !p.held && !p.conflict && (author || memberReader);
      if ((author || recipient) && (p.state !== "active" || p.held) && !retained) throw new Hold("proof_pending", "Human author or reader participation ended or is held.");
      sender ||= author; reader ||= recipient;
    }
    if (!sender || !reader) throw new Hold("invalid", "Human author or recipient is outside captured authority.");
  }

  async humanGate(c, rec, checks = []) {
    try {
      if (!c || !rec.human) throw new Error("Captured human conversation is missing.");
      const pin = await this.groupRead(checks, "pins", rec.to);
      if (!pin || pin.pending || pin.fingerprint !== rec.recipient_fp) throw new Error("Human recipient key changed.");
      const evidence = await this.humanEvidence(c, rec.human, checks);
      if(c.kind==="group") {
        const x=await this.assistantOf(c,rec,checks);
        if(x) { this.externalRole(rec,x,evidence.members,this.address,this.fp);await this.checkExternalReply(rec,x,evidence.members,checks); }
        if(rec.target && !wire.agentAuthor(rec.human) && evidence.members.epochs.get(this.fp)!==rec.target.group_admission)throw Error("Original group requester admission changed.");
      }
      this.humanTurnAuthorization(rec, evidence, await this.assistantOf(c, rec, checks), this.address, this.fp, rec.to, pin.fingerprint);
      return { why: "", pin };
    } catch (e) { return { why: e.message }; }
  }

  // sendHumanTurn sends an ordinary turn to the captured human audience, or,
  // with x, a request addressed to assistant participation x: its exact host
  // gets the executable copy (its ID is the request's LID) and alone decides
  // whether it runs; everyone else keeps the inert turn.
  async sendHumanTurn(c, n, h, x = null, attempt = 0) {
    const original = n;
    n = { ...n, reply_to: await this.logicalReply(c.id, n.reply_to, true, "Conversation"),quote:await this.logicalReply(c.id,n.quote,true,"Conversation") }; // client.humanReply: the LID every audience device keeps
    const request = !!x;
    if (request && (!["question", "task"].includes(n.kind) || !n.target || n.receiver)) throw new Error("An addressed request names its assistant only.");
    if(c.kind==="group" && request && h.proof.some(e=>e.role==="human"))throw Error("Group human guest execution audience is not enabled.");
    const checks = [], evidence = await this.humanEvidence(c, h, checks);
    const root = c.kind==="group" ? wire.parseGroupRoot(c.root) : wire.parseRoot(c.root), fan = c.kind==="group" ? [] : root.members.map(m => ({ person: m.person, roster: evidence.members.get(m.person).hash }));
    const groupAdmission=c.kind==="group"?evidence.members.epochs.get(this.fp):"";
    if(c.kind==="group"&&!groupAdmission) {
      const guest=h.author_pid && evidence.scopes.get(h.author_pid);
      if(!guest || guest.role!=="human" || guest.state!=="active" || guest.held || guest.host?.address!==this.address || guest.host.fingerprint!==this.fp)throw Error("Only a current member or exact accepted guest writes a group turn.");
    }
    const hosts = [...evidence.scopes.values()].map(p => p.host.devices.find(d => d.address === p.host.address && d.fingerprint === p.host.fingerprint));
    if (request && x.external) hosts.push({ address: x.host.address, fingerprint: x.host.fingerprint }); // the outside assistant host reads the addressed request only
    const devices = [...new Map([...evidence.members.values()].flatMap(p => p.devices).concat(hosts).filter(d => d.address !== this.address).map(d => [d.address, d])).values()];
    const lid = this.localSendID(n.id), firstID = wire.newID(), at = this.now(), plain = [], recs = [];
    for (const f of n.files || []) plain.push({ name: wire.safeName(f.name), bytes: f.bytes instanceof Uint8Array ? f.bytes : new Uint8Array(await f.arrayBuffer()) });
    checkFiles(plain.map(f => ({ name: f.name, size: f.bytes.length })));
    const prepared = request ? null : await this.prepareReceiverRequest(n.receiver, { id: firstID, lid, conv: c.id, root: c.root, ts: Math.floor(at / 1000), kind: "message", body: n.body, quote:n.quote||"",reply_to: n.reply_to || "", origin: "ui", pid: h.author_pid || "", human: h }, plain, checks);
    const turn = request ? { kind: n.kind, pid: n.pid, target: n.target } : { kind: "message", pid: h.author_pid || "" };
    for (const d of devices) {
      const pin = await this.groupRead(checks, "pins", d.address);
      if (!pin || pin.pending || pin.fingerprint !== d.fingerprint) throw new Error("Human audience device key changed.");
      const isHost = request && d.address === x.host.address && d.fingerprint === x.host.fingerprint;
      this.humanTurnAuthorization({ ...turn, human: h }, evidence, x, this.address, this.fp, d.address, d.fingerprint);
      if(c.kind!=="group") await this.requireHumanSupport(d.address, pin);
      // Group reader compatibility is checked per durable copy at handoff.
      if (isHost && (x.external || x.agent_id)) await this.requireAgentIdentity(d.address, pin, x.external ? wire.CapExternalParticipation : wire.CapAgentIdentity);
      if (prepared) await this.receiverSupport(d.address, pin);
      const pub = await this.pubOf(pin), sealed = [];
      for (const f of plain) sealed.push({ ...await wire.encryptFile(f.bytes, f.name, pub), uploaded: false });
      const id = isHost ? lid : recs.length || request ? wire.newID() : firstID, own = this.me.devices.some(x => x.address === d.address), inner = { v: 2, id, from: this.address, to: d.address, ts: Math.floor(at / 1000), kind: turn.kind, body: n.body,topic:n.topic||"",topic_event:n.topic_event||null, quote:n.quote||"",reply_to: n.reply_to || "", origin: "ui", conv: c.id, lid, root: c.root, fan, replica: own && !isHost, pid: turn.pid, ...(request ? { target: turn.target } : {}), human: h, attachments: sealed.map(f => f.attachment), receiver_route: prepared?.route };
      if(c.kind==="group") {
        inner.fan=[{person:this.me.person,roster:this.me.hash}];
        const reader=[...evidence.members.values()].find(p=>p.devices.some(x=>x.address===d.address&&x.fingerprint===d.fingerprint));
        if(reader&&reader.person!==this.me.person)inner.fan.push({person:reader.person,roster:reader.hash});
      }
      inner.send_group=await this.sendGroupCopy(d.address,pin,n.send_group);
      const envelope = await wire.seal(inner, this.keys, pub);
      recs.push({ ...inner, send_group:n.send_group||"", send_group_wire:!!inner.send_group, ...(prepared ? { receiver_route: prepared.route } : {}), at, own, envelope, ...(groupAdmission?{group_admission:groupAdmission}:{}), recipient_fp: d.fingerprint, required_cap: wire.CapHumanParticipation, files: sealed.length ? sealed : undefined, state: "queued", detail: "" });
    }
    if (!recs.length) throw new Error("No authorized human audience device.");
    for (const r of recs) { const gate = await this.humanGate(c, r, checks); if (gate.why) throw new Error(gate.why); }
    const consentEvents=c.kind==="group"?[...await this.convEvents(c.id,checks),...await Promise.all(h.proof.map(async e=>({e,hash:await wire.eventHash(e)})))]:[];
    try { await this.commitReceiverCopies(recs, prepared, checks, c.kind==="group"?await this.roomStoredOps(c.id,checks,recs[0],this.fp,consentEvents,evidence.members):[],n.queued); }
    catch (e) {
      if (e instanceof StoreConflict && attempt < 3) return this.sendHumanTurn(c, original, h, x, attempt + 1);
      throw e;
    }
    await this.keepSent(plain); this.changed();
    if (n.queued) this.queueOutbox(); else if (prepared) await this.post(prepared.delegation); else for (const r of recs) await this.post(r);
    const least = copyOrder(recs);
    return { id: recs[0].id, lid, state: least.state, detail: least.detail, copies: recs.map(r => ({ id: r.id, to: r.to, state: r.state, detail: r.detail })) };
  }

  async admitHumanTurn(n, env, pin, base, root, senderPerson, group = null) {
    const c = group || await this.store.get("convs", n.conv);
    if (!c) throw new Hold("proof_pending", "Human turn waits for its original invitation root.");
    // Resolve only already pinned original members / exact carried host proof.
    for (const m of group ? [] : root.members) if (m.person !== this.me.person) await this.pinChain(m.person);
    for (const e of n.human.proof.filter(e => e.type === "scope")) {
      const hp = await this.sendKey(e.host.address);
      if (hp.fingerprint !== e.host.fingerprint || (await this.personOf(e.host.address, hp)).person !== e.host.person) throw new Hold("invalid", "Human host differs from pinned person proof.");
    }
    if (n.target && n.target.address === this.address) throw new Hold("invalid", "This browser runs no agent.");
    const checks = [], stored = group ? await this.groupRecord(n.conv) : await this.groupRead(checks, "convs", n.conv), senderPin = await this.groupRead(checks, "pins", env.from);
    if (!stored || stored.root !== c.root || !senderPin || senderPin.pending || senderPin.fingerprint !== pin.fingerprint) throw new StoreConflict();
    const evidence = await this.humanEvidence(c, n.human, checks), x = await this.assistantOf(c, n, checks);
    this.humanTurnAuthorization(n, evidence, x, env.from, pin.fingerprint, this.address, this.fp);
    if(group) {
      const {packet}=await this.groupTurnEvidence(n.conv,checks,true);
      if(wire.rootJSON(root)!==wire.rootJSON(packet.root))throw new Hold("invalid","Group root differs from verified context.");
      await this.checkConversationAgent(n,env.from,pin.fingerprint,c);
      if(n.target && !wire.agentAuthor(n.human) && evidence.members.epochs.get(pin.fingerprint)!==n.target.group_admission)throw new Hold("invalid","Group requester admission changed.");
      if(x)await this.checkExternalReply(n,x,evidence.members,checks);
    }
    if (x && !n.target && n.reply_to) { // an output names one exact request of its assistant
      const req = await this.groupRead(checks, "inbox", n.reply_to) || await this.groupRead(checks, "outbox", n.reply_to) || (await this.authorityRows({ conv: n.conv, lid: n.reply_to }, checks))[0];
      if (req && (req.pid !== n.pid || !["question", "task"].includes(req.kind))) throw new Hold("invalid", "Assistant output does not reply to its exact request.");
    }
    if (n.reply_to) { const replied = await this.groupRead(checks, "inbox", n.reply_to) || await this.groupRead(checks, "outbox", n.reply_to); if (replied && replied.conv !== n.conv) throw new Hold("invalid", "A reply stays within its conversation."); }
    if (n.reply_to && await this.logicalReply(n.conv, n.reply_to, false, "Conversation").catch(e => { throw new Hold("invalid", e.message); }) !== n.reply_to) throw new Hold("invalid", "A human reply must name its exact logical parent.");
    const key = pin.fingerprint + "/" + n.lid, hash = await wire.groupHistoryContentHash(n.conv, n), seen = await this.groupRead(checks, "lids", key), ops = [];
    if (seen && !seen.history) { if (seen.hash !== hash) throw new Hold("conflicting_duplicate", "Human logical copy differs."); ops.checks = checks; return ops; }
    if (seen?.history) ops.push({ s: "inbox", k: seen.id, v: undefined });
    const own = senderPerson.person === this.me.person, rec = { ...base, v: 2, conv: n.conv, lid: n.lid, sub: "", pid: n.pid, human: n.human, origin: n.origin, emotion: n.emotion || "", target: n.target || null, ...(n.agent_id ? { agent_id: n.agent_id } : {}), replica: n.replica, own, read: own || base.read, state: "" };
    const forwards = !group && !own && wire.rootMember(root, this.me.person) ? await this.forwardStale(n, env, pin, c) : [];
    ops.push({ s: "inbox", k: env.id, v: rec }, { s: "lids", k: key, v: { id: env.id, conv: n.conv, hash } }, ...forwards.map(r => ({ s: "outbox", k: r.id, v: r })));
    if(group) {
      rec.group_admission=evidence.members.epochs.get(this.fp)||undefined; // member receive epoch; outside followers have none
      const events=[...await this.convEvents(n.conv,checks),...await Promise.all(n.human.proof.map(async e=>({e,hash:await wire.eventHash(e)})))];
      ops.push(...await this.roomStoredOps(n.conv,checks,n,pin.fingerprint,events,evidence.members));
    }
    ops.checks = checks;
    return ops; // ordinary human traffic: no claim, worker, task or root expansion
  }

  guestView(info, member) {
    const hostHere = info.host?.address === this.address && info.host.fingerprint === this.fp, active = info.state === "active" && !info.held;
    return { needs_update:info.needs_update||[],pid: info.pid, state: info.state, state_text: info.held ? "Waiting for verified participation evidence" : info.state === "dismissed" ? "Ended here. Other devices may not have applied this change yet. Previously shared copies remain." : info.state, host: this.personView(info.host), host_here: !!hostHere, inviter: this.personView(info.inviter), shared: info.grant.map(g => g.lid), missing: 0, held: info.held, can_decide: !!hostHere && info.state === "invited" && !info.held, can_leave: !!hostHere && active, can_end: !!member && ["invited", "active", "conflict"].includes(info.state), can_send: active && (!!member || !!hostHere), audience_pending: !!info.held || info.state === "dismissed" };
  }

  async changeHuman(action, body) {
    const allowed = action === "invite" ? ["conv", "host", "share", "note"] : action === "decide" ? ["pid", "accept"] : ["pid"];
    if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some(k => !allowed.includes(k))) throw new Error("Choose one exact human participation action.");
    if (action === "invite") {
      if (!wire.validHash(body.conv) || !wire.validAddress(body.host) || body.share != null && (!Array.isArray(body.share) || body.share.some(x => typeof x !== "string")) || body.note != null && typeof body.note !== "string") throw new Error("Invalid human invitation.");
      return this.inviteHuman(body);
    }
    if (!wire.validID(body.pid) || action === "decide" && typeof body.accept !== "boolean") throw new Error("Choose one exact human participation action.");
    const { c, info } = await this.agentConv(body.pid);
    const members=await this.dmMembers(c), member=members.has(this.me?.person);
    if (info.role !== "human") throw new Error("This action requires a human participation, not an agent.");
    const view = this.guestView(info, member);
    if (action !== "decide" && info.state === "dismissed") {
      if (!member && !(info.decision && info.host?.address === this.address && info.host.fingerprint === this.fp)) throw new Error("Only an original member or exact accepted human host ends participation.");
      // Retry already encrypted copies only; a later participant must not
      // become an audience member of this earlier end.
      for (const r of await this.store.all("outbox")) {
        if (r.conv !== c.id || r.pid !== info.pid || r.sub !== "event") continue;
        const e = wire.parseEvent(r.body);
        if (e.type === "dismiss" && e.author.address === this.address && e.author.fingerprint === this.fp) await this.post(r);
      }
      return view;
    }
    let type, prev;
    if (action === "decide") { if (!view.can_decide) throw new Error("Only the exact invited host decides an invitation once."); type = body.accept ? "accept" : "decline"; prev = info.invite; }
    else { if (!view.can_leave && !view.can_end) throw new Error("Only an original member or exact accepted human host ends participation."); type = "dismiss"; prev = info.state === "invited" ? info.invite : info.decision; }
    const e = await wire.signEvent(this.keys, { conv: c.id, pid: info.pid, type, prev, ts: Math.floor(this.now() / 1000), author: {...this.author(),...(members.group&&member?{group_admission:members.epochs.get(this.fp)}:{})} });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid: info.pid });
    return this.guestView((await this.agentConv(info.pid)).info, member);
  }

  async checkHuman(body) {
    if (!body || Object.keys(body).some(k => !["conv", "host"].includes(k)) || !wire.validHash(body.conv) || !wire.validAddress(body.host)) throw Error("Choose a person and conversation.");
    const c = await this.store.get("convs", body.conv) || await this.groupRecord(body.conv);
    if (!c) throw Error("No conversation here.");
    const members = await this.dmMembers(c);
    if(!members.has(this.me?.person))throw Error("Only a current member checks a human invitation.");
    const pin = await this.sendKey(body.host), person = await this.personOf(body.host, pin);
    if (!person.devices.some(d => d.address === body.host && d.fingerprint === pin.fingerprint) || !["self", "pinned"].includes(person.state)) throw Error("Host has no current pinned person/device proof.");
    members.set(person.person, person);
    const v = {ready: true, needs_update: [], offline: [], text: ""}, text = [];
    for (const p of [...members.values()].sort((a,b) => a.label.localeCompare(b.label))) {
      const role = p.person === this.me.person ? "me" : p.person === person.person ? "guest" : "member";
      let state = "ok", offline = false;
      for (const d of p.devices) {
        if (role === "guest" && d.address !== body.host) continue;
        const pin = await this.sendKey(d.address), before = pin;
        if (!pin || pin.pending || pin.fingerprint !== d.fingerprint) throw Error("Human participant key is not currently pinned.");
        const profile = await this.profile(d.address), pub = await this.pubOf(pin), after = await this.store.get("pins", d.address);
        if (!after || after.pending || after.fingerprint !== before.fingerprint) throw Error("Human participant key changed.");
        if (!profile.sessions?.length) state = "not set up";
        else if (state !== "not set up" && (!await wire.profileSupports(profile, d.address, pub.sign_key, wire.CapHumanParticipation) || members.group && (!await wire.profileSupports(profile,d.address,pub.sign_key,wire.CapGroup) || !await wire.profileSupports(profile,d.address,pub.sign_key,wire.CapRoom) || !await wire.profileSupports(profile,d.address,pub.sign_key,wire.CapGroupHumanParticipation)))) state = "update";
        offline = role === "guest" && d.address === body.host && !profile.live;
      }
      if (state !== "ok") {
        v.ready = false;
        v.needs_update.push({label: p.label, me: role === "me", role});
        const action = state === "not set up" ? "AgentNet set up" : "an AgentNet update";
        text.push(role === "me" ? "One of your other devices needs " + action + "." : role === "member" ? p.label + "'s app needs " + action + " to keep this chat working with a guest. That person's copy waits until then." : p.label + "'s app needs " + action + " before joining. The invitation waits until then.");
      } else if (offline) {
        v.offline.push(p.label);
        text.push(p.label + " is not connected now; the invitation reaches them when their app reconnects.");
      }
    }
    v.text = text.join(" ");
    return v;
  }

  async shownCopies(copies) { const people=[this.me,...await this.store.all("persons")].filter(Boolean);return (copies||[]).map(c=>({...c,person:c.own?"You":people.find(p=>p.person===c.person||p.devices.some(d=>d.address===c.to))?.label||"Someone"})); }

  async inviteHuman({ conv, host, share = [], note = "" }) {
    const c = await this.store.get("convs", conv) || await this.groupRecord(conv);
    if (!c) throw Error("No conversation here.");
    const members=await this.dmMembers(c);
    if(!members.has(this.me?.person))throw Error("Only a current member invites a human participant.");
    const pin = await this.sendKey(host), person = await this.personOf(host, pin);
    if (members.has(person.person)) throw new Error("That person already belongs to this DM.");
    for (const d of [...members.values()].flatMap(p => p.devices).concat([{ address: host }])) { if(members.group)await this.requireGroupHumanSupport(d.address,await this.sendKey(d.address));try{await this.requireHumanSupport(d.address,await this.sendKey(d.address));}catch(e){if(e.code!=="human_unsupported")throw e;} }
    const guests = (await this.participationsOf(c)).filter(p => p.role === "human" && ["active", "invited"].includes(p.state));
    if (guests.length >= wire.MaxHumanAudience || guests.some(p => p.host.address === host)) throw new Error("Human already invited/active, or audience limit reached; end it before inviting again.");
    const msgs = (await this.convMessages(conv, await this.store.all("inbox"), await this.store.all("outbox"))), grant = [];
    for (const id of share) {
      const m = msgs.find(m => (m.id === id || m.lid === id) && !m.sub && !m.excerpt_pid && !m.deleted);
      if (!m?.lid) throw new Error("Selected context is not an earlier shareable message in this DM.");
      if(members.group)await this.groupAgentSelection(conv,m,members);
      grant.push({ lid: m.lid, fingerprint: m.fp || this.fp });
    }
    const group=members.group?{seq:members.group.state.seq,hash:await wire.groupStateHash(members.group.state),host_role:"visitor"}:null;
    const e = await wire.signEvent(this.keys, { conv, pid: wire.newID(), type: "invite", role: "human", ts: Math.floor(this.now() / 1000), author: {...this.author(),...(group?{group_admission:members.epochs.get(this.fp)}:{})}, host: { person: person.person, address: host, fingerprint: pin.fingerprint }, grant: grant.length ? grant : null, audience: group?"room":"conversation", note: String(note).trim(),...(group?{group}:{}) });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid: e.pid });
    const scope = await wire.signEvent(this.keys, await wire.scopeOf(e, e.ts)); // what other guests may see of it
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(scope), pid: e.pid });
    return this.guestView((await this.agentConv(e.pid)).info, true); // excerpts wait for acceptance
  }

  recoverHumanExcerpts() {
    if (!this.recoveringHuman) this.recoveringHuman = this.recoverHumanExcerptPass().finally(() => { this.recoveringHuman = null; });
    return this.recoveringHuman;
  }
  async recoverHumanExcerptPass() {
    const conversations=await this.store.all("convs");
    for(const g of await this.store.all("kv"))if(g.root&&g.context&&Array.isArray(g.records)){const packet=wire.parseGroupContext(g.context);if(!conversations.some(c=>c.id===packet.state.conv))conversations.push({id:packet.state.conv,kind:"group",root:g.root});}
    for (const c of conversations) for (const p of await this.participationsOf(c)) {
      if (p.role !== "human" || p.state !== "active" || p.held || p.inviter?.address !== this.address || p.inviter.fingerprint !== this.fp) continue;
      const inv = (await this.convEvents(c.id, null, p.pid)).find(x => x.hash === p.invite)?.e;
      if (inv) await this.sendGrantedExcerpts(c, inv);
    }
  }

  // requireAgentReaction is client.requireParticipationCaps(agr1) plus what
  // the assistant's own output needs there (assistantReactionCaps): a named
  // agent's identity when no participation or group requirement covers it.
  async requireAgentReaction(address, pin, item, required) {
    const key = await this.pubOf(pin), profile = await this.profile(address);
    const after = await this.store.get("pins", address);
    if (!after || after.pending || after.fingerprint !== pin.fingerprint) throw new Error(address + "'s key changed.");
    if (!await wire.profileSupports(profile || {}, address, key.sign_key, wire.CapAgentReaction)) throw Object.assign(new Error(address + " cannot read assistants' reactions yet; update all its active AgentNet sessions."), { code: "agent_identity_unsupported", address });
    if (item?.agent_id && ![wire.CapGroup, wire.CapExternalParticipation].includes(required)) await this.requireAgentIdentity(address, pin);
  }

  async requireAgentIdentity(address, pin, required = wire.CapAgentIdentity) {
    const current = await this.store.get("pins", address);
    if (!pin || pin.pending || !current || current.pending || current.fingerprint !== pin.fingerprint) throw new Error("Named agent's host key is not currently pinned.");
    const key = await this.pubOf(pin), profile = await this.profile(address);
    const after = await this.store.get("pins", address);
    if (!after || after.pending || after.fingerprint !== pin.fingerprint) throw new Error("Named agent's host key changed.");
    if (!await wire.profileSupports(profile || {}, address, key.sign_key, wire.CapAgentIdentity) || required === wire.CapExternalParticipation && !await wire.profileSupports(profile || {}, address, key.sign_key, required)) throw Object.assign(new Error(address + " cannot read named agents" + (required === wire.CapExternalParticipation ? " with external participation" : "") + " yet; update all its active AgentNet sessions."), { code: "agent_identity_unsupported", address });
  }

  async agentCatalog(host) {
    host = String(host || "").trim();
    if (!host || host === this.address) throw new Error("This browser runs no local agents or responders.");
    if (!wire.validAddress(host)) throw new Error("That is not an AgentNet host address.");
    const pin = await this.sendKey(host);
    await this.requireAgentIdentity(host, pin);
    const key = await this.pubOf(pin), [label, device] = host.split("/");
    const raw = await this.call("GET", "/v1/agents/" + label + "/" + device + "/agent-catalog");
    if (!Array.isArray(raw) || raw.length > wire.MaxAgentCatalog) throw new Error("Agent catalog exceeds limit or has the wrong shape.");
    const ids = new Set(), agents = [];
    for (const value of raw) {
      const record = wire.parseAgentRecord(JSON.stringify(value));
      await wire.verifyAgent(record, key);
      if (ids.has(record.id)) throw new Error("Agent catalog repeats an identity.");
      ids.add(record.id); agents.push({ record: JSON.parse(wire.agentJSON(record)), enabled: true });
    }
    const after = await this.store.get("pins", host);
    if (!after || after.pending || after.fingerprint !== pin.fingerprint) throw new Error("Agent catalog's host key changed.");
    return { host, local: false, agents }; // exactly the public native DTO; no programs or permissions
  }

  async namedTarget(host, id) {
    if (!wire.validID(id)) throw new Error("That is not a stable agent ID.");
    const { agents } = await this.agentCatalog(host), found = agents.find((a) => a.record.id === id);
    if (!found) throw new Error("That agent is not in this host's verified public catalog.");
    return { address: found.record.host, fingerprint: found.record.host_key, agent_id: found.record.id };
  }

  async checkDeviceAgent(n, address, fingerprint) {
    if (n.target && (n.target.address !== this.address || n.target.fingerprint !== this.fp)) throw new Hold("invalid", "named request targets another device key");
    if (!n.agent_id) return;
    const request = await this.store.get("outbox", n.reply_to);
    if (!request || request.v !== 1 || request.to !== address || request.target?.address !== address || request.target?.fingerprint !== fingerprint || request.target?.agent_id !== n.agent_id) throw new Hold("invalid", "named answer does not match the requested agent and host key");
  }

  // Named identities are bound by the signed invitation, and so is every
  // agent's turn (agentTurn): from the participation's exact host address
  // and key, while it is active. History uses its original key and may
  // outlive the participation (client.checkConversationAgent).
  async checkConversationAgent(n, address, fingerprint, c, historical = false) {
    const agent = agentTurn(n);
    if (!n.agent_id && !n.target?.agent_id && !agent) return;
    if (!wire.validID(n.pid)) throw new Hold("invalid", "an agent's or named conversation turn has no participation");
    if (!c) throw new Hold("proof_pending", "named participation's conversation is not here yet");
    let info = this.resolveAgent(n.pid, await this.convEvents(n.conv), await this.dmMembers(c));
    if (!info.invite || !info.host || info.state === "conflict") throw new Hold("proof_pending", "named participation has no unambiguous verified invitation");
    if (n.target && (n.target.agent_id !== info.agent_id || n.target.address !== info.host.address || n.target.fingerprint !== info.host.fingerprint)) throw new Hold("invalid", "named request differs from its participation host");
    if(n.human && wire.agentAuthor(n.human)) info=this.resolveAgent(n.human.author_pid,await this.convEvents(n.conv),await this.dmMembers(c));
    if (agent && (info.role === "human" || address !== info.host.address || fingerprint !== info.host.fingerprint)) throw new Hold("invalid", "an agent's turn is not from its participation's exact host");
    // Ended is final, failing closed: an output still in flight at the end is
    // refused for good here if the end arrived first, while a device that
    // admitted it earlier keeps it (a known limit, ROOM_V1 §4.1).
    if (agent && !historical && (info.state === "declined" || info.state === "dismissed")) throw new Hold("invalid", "an agent's turn after its participation ended");
    if (agent && !historical && (info.state !== "active" || info.held)) throw new Hold("proof_pending", "an agent's turn waits for its participation to be active");
    if (n.agent_id && (n.agent_id !== info.agent_id || address !== info.host.address || fingerprint !== info.host.fingerprint || !["answer", "result"].includes(n.kind) && !progressOutput(n) || !n.reply_to || n.sub)) throw new Hold("invalid", "named answer differs from its participation host");
  }

  // Outside hosts remain separate pinned proof, never ordinary room members.
  async eventRecord(body) {
    const e = wire.parseEvent(body);
    return { e, hash: await wire.eventHash(e) };
  }

  externalRole(n, info, members, address, fp) {
    const member = [...members.values()].some((p) => p.devices.some((d) => d.address === address && d.fingerprint === fp));
    const host = info.host && address === info.host.address && fp === info.host.fingerprint;
    if (!info.invite || (!info.external || members.size !== 2) && !members.group || n.pid !== info.pid) throw new Hold("proof_pending", "participation has incomplete pinned evidence");
    if (n.sub === "event") {
      const ev = wire.parseEvent(n.body);
      if(members.group && member && (ev.author.address!==address || ev.author.fingerprint!==fp) && ev.type==="dismiss" && info.state==="dismissed" && !info.held && info.dismissalEvent===wire.eventJSON(ev))return;
      if (ev.author.address !== address || ev.author.fingerprint !== fp) throw new Hold("invalid", "event is not its sending device's own");
      const author = members.get(ev.author.person);
      const memberAuthor = member && author?.hashes.includes(ev.author.roster) && author.devices.some((d) => d.address === address && d.fingerprint === fp) && (!members.group || members.epochs.get(fp)===ev.author.group_admission);
      if(ev.type === "share") {
        if(!member||!info.shares?.includes(wire.eventJSON(ev)))throw new Hold("invalid","share does not name the exact group agent membership");
      } else if (ev.type === "invite") {
        if (!memberAuthor || ev.prev) throw new Hold("invalid", "invite is not this member's exact invitation");
      } else if (ev.type === "scope") {
        if (!memberAuthor || ev.prev !== info.invite) throw new Hold("invalid", "scope is not this member's projection of its invitation");
      } else if (["accept", "decline"].includes(ev.type)) {
        const p = members.hosts.get(ev.author.person) || members.get(ev.author.person);
        if (!host || ev.author.person !== info.host.person || !p?.hashes.includes(ev.author.roster) || ev.prev !== info.invite || members.group && (member ? members.epochs.get(fp)!==ev.author.group_admission : !!ev.author.group_admission)) throw new Hold("invalid", "decision is not from the exact invited host for its invite");
      } else if ((!memberAuthor && !((info.role === "human" || info.member && info.external) && host && info.dismissalEvent === wire.eventJSON(ev))) || ![info.invite, info.decision, info.dismissal].includes(ev.prev)) throw new Hold("invalid", "only a current member or exact accepted human host ends participation");
      return;
    }
    if (n.sub === "excerpt") {
      if (info.role === "human" && (info.state !== "active" || info.held)) throw new Hold("proof_pending", "human selected context waits for exact acceptance");
      if (!member || !(info.inviters||[info.inviter]).some(p=>p.address===address&&p.fingerprint===fp) || !n.replica || n.kind !== "message" || info.held || !["invited", "active"].includes(info.state)) throw new Hold("invalid", "excerpt is not from the inviter for a live granted participation");
      try { wire.parseGrantedExcerpt(n, info); } catch (e) { throw new Hold("invalid", e.message); }
      return;
    }
    if (n.sub) throw new Hold("invalid", "external host receives only selected excerpts and PID-addressed turns");
    if (info.role === "human") throw new Hold("invalid", "human participation confers no executor authority");
    if (info.state !== "active" || info.held) throw new Hold(info.state === "invited" ? "proof_pending" : "invalid", "external participation is not active");
    if (["question", "task"].includes(n.kind)) {
      if(n.human && wire.agentAuthor(n.human)) {
        if(!n.target || n.target.address!==info.host.address || n.target.fingerprint!==info.host.fingerprint || (n.target.agent_id||"")!==(info.agent_id||""))throw new Hold("invalid","Room ask targets a different agent.");
        return;
      }
      if (!member || !n.target || n.target.address !== info.host.address || n.target.fingerprint !== info.host.fingerprint || n.target.agent_id !== info.agent_id || (members.group ? members.epochs.get(fp)!==n.target.group_admission : !!n.target.group_admission)) throw new Hold("invalid", "request does not name the exact invited agent and requester admission");
    } else if (["answer", "result"].includes(n.kind) || progressOutput(n)) {
      if (!host || n.agent_id !== info.agent_id || !n.reply_to) throw new Hold("invalid", "output does not belong to the exact invited agent");
    } else throw new Hold("invalid", "outside traffic is not an addressed participation turn");
  }

  // The executable host copy's ID is the shared request LID. Existing wire
  // and IDs suffice to bind replies at every human audience device.
  async checkExternalReply(n, info, members, checks, historical = false) {
    if (!["answer", "result"].includes(n.kind) && !progressOutput(n) || n.sub) return;
    if (!info.decision) throw new Hold("proof_pending", "external output has no host acceptance proof yet");
    const decision = (historical&&members.historyEvents ? members.historyEvents : await this.convEvents(n.conv,checks,n.pid)).find((r) => r.e.pid===n.pid && r.hash === info.decision);
    if (!decision) throw new Hold("proof_pending", "external output has no host acceptance proof yet");
    if (decision.e.type !== "accept") throw new Hold("invalid", "external output participation was not accepted by its host");
    let candidates;
    if(checks) {
      candidates=await this.authorityRows({conv:n.conv,lid:n.reply_to},checks);
      for(const s of ["inbox","outbox"]) { const row=await this.groupRead(checks,s,n.reply_to);if(row&&!candidates.includes(row))candidates.push(row); }
    } else candidates=[...await this.store.all("inbox"), ...await this.store.all("outbox")].filter((r)=>r.id===n.reply_to||r.lid===n.reply_to);
    if (!candidates.length) throw new Hold("proof_pending", "external answer has no matching request yet");
    let original = null;
    for (const r of candidates) {
      const fp = r.to ? this.fp : r.fp, address = r.to ? this.address : r.from;
      const member = [...members.values()].some((p) => p.devices.some((d) => d.address === address && d.fingerprint === fp));
      const agent=!!r.human && wire.agentAuthor(r.human);
      if(agent) {
        const e=historical&&members.historyEvents?members.historyEvents:await this.convEvents(n.conv,checks,r.human.author_pid),asking=this.resolveAgent(r.human.author_pid,e,await this.dmMembers(await this.groupRecord(n.conv)||await this.store.get("convs",n.conv),e,checks,historical&&members.historyEvents?members.group:null));
        if(asking.state!=="active" && !(historical && this.retainedAssistant(asking,e)) || asking.held || asking.host?.address!==address || asking.host.fingerprint!==fp)throw new Hold("invalid","Asking agent membership ended or changed.");
      }
      if (!agent && !member || r.conv !== n.conv || r.pid !== n.pid || r.sub || r.excerpt_pid || !["question", "task"].includes(r.kind) || !progressOutput(n) && replyKind(r.kind) !== n.kind || !r.lid || original && (original.lid !== r.lid || original.fp !== fp) || r.target?.address !== info.host.address || r.target?.fingerprint !== info.host.fingerprint || r.target?.agent_id !== info.agent_id || (!agent && (members.group ? members.epochs.get(fp)!==r.target?.group_admission : !!r.target?.group_admission))) throw new Hold("invalid", "external answer does not match its exact participation request");
      original = { lid: r.lid, fp };
    }
  }

  humanEndEvent(n, info, conv = n.conv) {
    if (info.role !== "human" || n.sub !== "event") return false;
    const e = wire.parseEvent(n.body);
    return e.type === "dismiss" && e.pid === info.pid && e.conv === conv;
  }

  // admitDisclosed is client.disclosedHumanEvent's verification for a shared
  // human invite/accept/end: from a current original member device, about
  // another device's participation, signed by its own author (a member
  // device, or the exact invited host as its pinned chain shows it), counted
  // by its participation, to a device that is itself an accepted guest here.
  // It is proof only: no history, files, jobs or receiver/task authority.
  async admitDisclosed(record, info, members, from, fp, c, checks) {
    const e = record.e, exact = (d, a) => d.address === a.address && d.fingerprint === a.fingerprint;
    if ((!members.group && members.size !== 2) || !info.host || exact({ address: this.address, fingerprint: this.fp }, info.host)) throw new Hold("invalid", "shared participation proof is about another device's participation");
    if (![...members.values()].some(p => p.devices.some(d => d.address === from && d.fingerprint === fp))) throw new Hold("invalid", "shared human proof comes only from a current original member device");
    const member = e.type !== "accept" ? members.get(e.author.person) : null, host = !member && e.type !== "scope" && e.author.person === info.host.person && exact(e.author, info.host) ? members.hosts.get(e.author.person) || members.get(e.author.person) : null;
    if (!member && !host) throw new Hold("invalid", "shared human proof has no member or exact invited-host author");
    const by = member || host, dev = by.hashes.includes(e.author.roster) && by.devices.find(d => exact(d, e.author));
    if (!dev) throw new Hold("proof_pending", "shared human proof author is not pinned here yet");
    try { await wire.verifyEvent(e, (await wire.parsePublic(JSON.parse(dev.json))).sign_key); } catch (err) { throw new Hold("invalid", err.message); }
    const counted = e.type === "scope" ? info.scope : e.type === "accept" ? info.decision : info.dismissal;
    if (!counted || counted !== record.hash) throw new Hold(info.held ? "proof_pending" : "invalid", "shared human proof does not count for its participation");
    if (!await this.humanEndReader(c, this.address, this.fp, checks)) throw new Hold("invalid", "shared human proof goes only to another exact accepted guest");
  }

  // discloseHumanAudience is client.discloseHumanAudience on an original
  // member device: each accepted human guest gets the other accepted guests'
  // counted invite+accept (so guests know each other before anyone speaks),
  // and an ended guest's counted end goes to every guest this device may have
  // told of that acceptance (a stored copy in any state: delivery may be
  // unconfirmed). Pending or declined invitations are never shared. Runs after
  // participation records arrive and on connect; idempotent by stored copy.
  discloseHumanAudience() {
    this.disclosing = (this.disclosing || Promise.resolve()).then(() => this.discloseHumanPass()).catch(() => {});
    return this.disclosing;
  }
  // A counted room end follows new member admissions even when its original
  // fan-out preceded their join. Store one exact signed copy per key/admission.
  async discloseRoomConv(c) {
    const events=await this.convEvents(c.id), members=await this.dmMembers(c,events);
    if(!members.group || !members.epochs.get(this.fp))return;
    const ends=[...new Set(events.map(x=>x.e.pid))].map(pid=>this.resolveAgent(pid,events,members)).filter(p=>p.member&&!p.held&&p.state==="dismissed"&&p.decision&&p.dismissal);
    for(const end of ends)for(const person of members.values())for(const dev of person.devices) {
      if(dev.address===this.address)continue;
      const checks=[],currentEvents=await this.convEvents(c.id,checks),currentMembers=await this.dmMembers(c,currentEvents,checks),info=this.resolveAgent(end.pid,currentEvents,currentMembers);
      if(!currentMembers.epochs.get(this.fp) || !info.member || info.held || info.state!=="dismissed" || info.dismissal!==end.dismissal)continue;
      if(currentEvents.find(x=>x.hash===info.decision)?.e.type!=="accept")continue;
      if(!await this.roomMembershipMayDismiss(currentMembers.group,(currentEvents.find(x=>x.hash===info.invite)||currentEvents.find(x=>x.hash===info.scope))?.e,currentEvents.find(x=>x.hash===info.dismissal)?.e))continue;
      const admission=currentMembers.epochs.get(dev.fingerprint),pin=await this.groupRead(checks,"pins",dev.address);
      if(!admission || !pin || pin.pending || pin.fingerprint!==dev.fingerprint)continue;
      const body=wire.eventJSON(currentEvents.find(x=>x.hash===info.dismissal).e);
      const rows=await this.authorityRows({conv:c.id,pid:info.pid,sub:"event"},checks);
      if(rows.some(r=>r.to===dev.address&&r.recipient_fp===dev.fingerprint&&r.group_admission===admission&&r.body===body))continue;
      let ok=false,detail="";
      try { await this.groupSupport(dev.address,pin);[ok,detail]=await this.supports(dev.address,pin); }
      catch(e) { detail=retryable(e)?"server_unavailable: cannot reach your server":"peer_update: "+e.message; }
      const target=[...currentMembers.values()].find(p=>p.devices.some(d=>d.address===dev.address&&d.fingerprint===dev.fingerprint));
      if(!target)continue;
      const id=wire.newID(),lid=wire.newID(),at=this.now(),fan=[{person:this.me.person,roster:this.me.hash},...(target.person!==this.me.person?[{person:target.person,roster:target.hash}]:[])];
      const inner={v:2,id,from:this.address,to:dev.address,ts:Math.floor(at/1000),kind:"message",body,conv:c.id,root:c.root,lid,pid:info.pid,sub:"event",origin:"ui",replica:false,fan,attachments:[]};
      const rec={...inner,at,own:target.person===this.me.person,recipient_fp:dev.fingerprint,group_admission:admission,required_cap:wire.CapGroup,envelope:await wire.seal(inner,this.keys,await this.pubOf(pin)),state:ok?"queued":"waiting",detail};
      const gate=await this.externalGate(c,rec,checks);
      if(gate.why)continue;
      await this.store.write([{s:"outbox",k:id,v:rec}],checks);this.changed();
      if(rec.state==="queued")await this.post(rec);
    }
  }
  async discloseHumanPass() {
    if (!this.me) return;
    // Quiet group carriers live in kv, without adding visible conversation rows.
    for(const g of await this.store.all("kv"))if(g.root&&g.context&&Array.isArray(g.records)) {
      try { const packet=wire.parseGroupContext(g.context);await this.discloseRoomConv({id:packet.state.conv,kind:"group",root:g.root}); }
      catch(e) { /* incomplete/frozen group evidence stays held until the next retry */ }
    }
    const conversations=await this.store.all("convs");
    for(const g of await this.store.all("kv"))if(g.root&&g.context&&Array.isArray(g.records)){const p=wire.parseGroupContext(g.context);if(!conversations.some(c=>c.id===p.state.conv))conversations.push({id:p.state.conv,kind:"group",root:g.root});}
    for (const c of conversations) {
      let root; try { root = c.kind==="group"?wire.parseGroupRoot(c.root):wire.parseRoot(c.root); } catch (e) { continue; }
      if(c.kind!=="group" && (root.kind!=="dm" || !wire.rootMember(root,this.me.person)))continue;
      const events = await this.convEvents(c.id), members = await this.dmMembers(c, events);
      if ((!members.group && members.size !== 2) || ![...members.values()].some(p => p.devices.some(d => d.address === this.address && d.fingerprint === this.fp))) continue;
      const all = [...new Set(events.map(x => x.e.pid))].map(pid => this.resolveAgent(pid, events, members));
      if (!all.some(p => p.role === "human")) continue; // only a DM that has had human guests
      const subjects = all.filter(p => p.decision && !p.held && ["active", "dismissed"].includes(p.state) && p.host); // humans and assistants alike
      const deviceOf = p => ({ p, dev: p.host.devices.find(d => d.address === p.host.address && d.fingerprint === p.host.fingerprint) });
      const guests = subjects.filter(p => p.role === "human" && p.state === "active").map(deviceOf).filter(g => g.dev && g.dev.address !== this.address);
      const hosts = all.filter(p => p.role !== "human" && p.state === "active" && !p.held && p.external && p.host).map(deviceOf).filter(g => g.dev && g.dev.address !== this.address); // outside assistant hosts learn guest ends
      if (!guests.length && !hosts.length) continue;
      const raw = new Map();
      for (const r of await this.authorityRows({ conv: c.id })) if (r.sub === "event") try { raw.set((await this.eventRecord(r.body)).hash, r.body); } catch (e) { /* not a record */ }
      const sent = []; // every stored record copy from here, shared or this device's own (end-only fan-out)
      for (const r of (await this.store.all("outbox")).filter(r => r.conv === c.id && r.sub === "event")) try { sent.push({ r, hash: r.forwarded || (await this.eventRecord(r.body)).hash }); } catch (e) { /* not a record */ }
      const copied = (x, hash, dev) => sent.some(({ r, hash: h }) => r.pid === x.pid && h === hash && r.to === dev.address && r.recipient_fp === dev.fingerprint);
      for (const x of subjects) {
        let share = [];
        if (x.state === "active") {
          if (!guests.some(g => g.p.pid !== x.pid)) continue; // nobody to share it with
          let scope; try { scope = await this.participationScope(c, x, events); } catch (e) { continue; } // shared once its inviter's scope is held here
          const hash = await wire.eventHash(scope); raw.set(hash, raw.get(hash) || wire.eventJSON(scope));
          share = [hash, x.decision];
        } else share = [x.dismissal];
        for (const { p: g, dev } of guests) {
          if (g.pid === x.pid || g.host.address === x.host.address) continue;
          if (x.state === "dismissed" && !copied(x, x.decision, dev)) continue; // never told of that acceptance here
          for (const hash of share) if (hash && !copied(x, hash, dev)) await this.discloseHuman(c, x, hash, raw.get(hash) || wire.eventJSON(events.find(r => r.hash === hash).e), dev);
        }
        if (x.role === "human" && x.state === "dismissed") for (const { dev } of hosts) if (!copied(x, x.dismissal, dev)) await this.discloseHuman(c, x, x.dismissal, raw.get(x.dismissal) || wire.eventJSON(events.find(r => r.hash === x.dismissal).e), dev);
      }
    }
  }
  async discloseHuman(c, x, hash, body, dev) {
    const checks = [], pin = await this.groupRead(checks, "pins", dev.address);
    if (!pin || pin.pending || pin.fingerprint !== dev.fingerprint) return;
    let ok = false, detail = "";
    try { await this.requireHumanSupport(dev.address, pin);if(c.kind==="group")await this.requireGroupHumanSupport(dev.address,pin); [ok, detail] = await this.supports(dev.address, pin); }
    catch (e) { if (!retryable(e)) return; detail = "server_unavailable: cannot reach your server"; }
    const members = await this.dmMembers(c), fan = [...members.values()].filter(p=>!members.group || p.devices.some(d=>d.address===this.address&&d.fingerprint===this.fp)).map(p => ({ person: p.person, roster: p.hash }));
    const id = wire.newID(), lid = wire.newID(), at = this.now();
    const inner = { v: wire.Version2, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), kind: "message", body, conv: c.id, root: c.root, lid, sub: "event", pid: x.pid, replica: false, fan, attachments: [] };
    const envelope = await wire.seal(inner, this.keys, await this.pubOf(pin));
    const rec = { kind: "message", body, sub: "event", pid: x.pid, origin: "", id, conv: c.id, lid, at, to: dev.address, replica: false, own: false, fan, recipient_fp: dev.fingerprint, required_cap: wire.CapHumanParticipation, forwarded: hash, envelope, attachments: [], state: ok ? "queued" : "waiting", detail };
    const gate = await this.disclosureGate(c, rec, checks);
    if (gate.why) return;
    await this.store.write([{ s: "outbox", k: id, v: rec }], checks);
    this.changed();
    if (rec.state === "queued") await this.post(rec);
  }
  // disclosureGate fences a shared human record at enqueue and every retry:
  // the recipient is still an accepted guest with its captured key, and a
  // shared invite/accept is still the counted record of a still-active guest
  // (after an end here, a queued acceptance never goes).
  async disclosureGate(c, rec, checks) {
    try {
      if (!c) throw new Error("Only a current member shares human participation records.");
      const events = await this.convEvents(c.id, checks), members = await this.dmMembers(c, events, checks), x = this.resolveAgent(rec.pid, events, members);
      if(!members.has(this.me?.person) || members.group&&!members.epochs.get(this.fp))throw Error("Only a current member shares human participation records.");
      const e = wire.parseEvent(rec.body);
      if ((await this.eventRecord(rec.body)).hash !== rec.forwarded || x.held || x.host && rec.to === x.host.address) throw new Error("Shared human record does not match its participation.");
      if (e.type === "dismiss" ? x.state !== "dismissed" || x.dismissal !== rec.forwarded : x.state !== "active" || ![x.scope, x.decision].includes(rec.forwarded) || !x.scope) throw new Error("Shared human record is no longer current here.");
      const dev = await this.humanEndReader(c, rec.to, rec.recipient_fp, checks) || (e.type === "dismiss" && x.role === "human" ? await this.assistantHostReader(c, rec.to, rec.recipient_fp, checks) : null);
      const pin = checks ? await this.groupRead(checks, "pins", rec.to) : await this.store.get("pins", rec.to);
      if (!dev || !pin || pin.pending || pin.fingerprint !== rec.recipient_fp) throw new Error("Shared human record recipient is no longer an accepted guest device.");
      return { why: "", pin };
    } catch (e) { return { why: e.message }; }
  }

  // End evidence only: never excerpts, ordinary traffic, membership or jobs.
  async humanEndReader(c, address, fingerprint, checks) {
    const events = await this.convEvents(c.id, checks), members = await this.dmMembers(c, events, checks);
    if (!members.group && (wire.parseRoot(c.root).kind !== "dm" || members.size !== 2)) return null;
    for (const pid of new Set(events.map(x => x.e.pid))) {
      const p = this.resolveAgent(pid, events, members);
      if (p.role === "human" && p.state === "active" && !p.held && p.host?.address === address && p.host.fingerprint === fingerprint) return p.host.devices.find(d => d.address === address && d.fingerprint === fingerprint) || null;
    }
    return null;
  }

  // assistantHostReader is client.assistantHostReader: the exact outside host
  // of an active assistant participation may learn a guest's end.
  async assistantHostReader(c, address, fingerprint, checks) {
    const events = await this.convEvents(c.id, checks), members = await this.dmMembers(c, events, checks);
    if (wire.parseRoot(c.root).kind !== "dm" || members.size !== 2 || members.group) return null;
    for (const pid of new Set(events.map(x => x.e.pid))) {
      const p = this.resolveAgent(pid, events, members);
      if (p.role !== "human" && p.state === "active" && !p.held && p.external && p.host?.address === address && p.host.fingerprint === fingerprint) return p.host.devices.find(d => d.address === address && d.fingerprint === fingerprint) || null;
    }
    return null;
  }

  async externalGate(c, rec, checks) {
    if (rec.human) return this.humanGate(c,rec,checks);
    if (rec.forwarded) return this.disclosureGate(c, rec, checks);
    try {
      if (!c) throw new Error("External conversation is not kept here.");
      const events = await this.convEvents(c.id,checks,rec.pid);
      if (rec.sub === "event") events.push(await this.eventRecord(rec.body));
      const members = await this.dmMembers(c, events,checks), info = this.resolveAgent(rec.pid, events, members);
      this.externalRole(rec, info, members, this.address, this.fp);
      await this.checkExternalReply(rec, info, members,checks);
      let dev = [...members.values()].flatMap((p) => p.devices).find((d) => d.address === rec.to);
      if (rec.to === info.host.address) dev = info.host.devices.find((d) => d.address === info.host.address && d.fingerprint === info.host.fingerprint);
      if (!dev && this.humanEndEvent(rec, info) && rec.recipient_fp) dev = await this.humanEndReader(c, rec.to, rec.recipient_fp, checks);
      const pin = checks ? await this.groupRead(checks,"pins",rec.to) : await this.store.get("pins", rec.to);
      if (!dev || !pin || pin.pending || pin.fingerprint !== dev.fingerprint || info.role === "human" && rec.recipient_fp && pin.fingerprint !== rec.recipient_fp) throw new Error("External recipient is no longer a pinned participation audience device.");
      if(members.group && (rec.recipient_fp!==dev.fingerprint || (members.epochs.get(dev.fingerprint)||"")!==(rec.group_admission||"")))throw Error("Group participation recipient admission changed.");
      if (rec.sub === "excerpt" && rec.to !== info.host.address) throw new Error("Selected excerpts go only to their exact host.");
      return { why: "", pin };
    } catch (e) { return { why: e.message }; }
  }

  async sendExternal(c, n, info, excerptHostOnly = false, attempt = 0) {
    let events = await this.convEvents(c.id);
    if (n.sub === "event") events.push(await this.eventRecord(n.body));
    let members = await this.dmMembers(c, events);
    const ending = this.humanEndEvent(n, info, c.id);
    const others = ending ? [...new Set(events.map(x => x.e.pid))].map(pid => this.resolveAgent(pid, events, members)).filter(p => p.role === "human" && p.state === "active" && !p.held).map(p => p.host) : [];
    for (const p of [...members.values(), info.host, ...others]) { try { await this.refreshPerson(p); } catch (e) { if (!retryable(e)) throw e; } }
    const checks=[];
    events=await this.convEvents(c.id,checks,members.group || ending ? undefined : n.pid);if(n.sub==="event")events.push(await this.eventRecord(n.body));
    members = await this.dmMembers(c, events, checks); info = this.resolveAgent(n.pid, events, members);
    if(members.group && info.member && !n.sub && !n.human) n={...n,human:await this.roomPlan(c,events,members)};
    this.externalRole(n, info, members, this.address, this.fp);
    checkFiles(n.files || []);
    const host = info.host.devices.find((d) => d.address === info.host.address && d.fingerprint === info.host.fingerprint);
    const extra = ending ? [...new Set(events.map(x => x.e.pid))].map(pid => this.resolveAgent(pid, events, members)).filter(p => p.role === "human" && p.state === "active" && !p.held).map(p => p.host.devices.find(d => d.address === p.host.address && d.fingerprint === p.host.fingerprint)).filter(Boolean) : [];
    const roomHosts=[];
    if(n.human) {
      const evidence=await this.humanEvidence(c,n.human,checks);
      for(const p of evidence.scopes.values())roomHosts.push(p.host.devices.find(d=>d.address===p.host.address&&d.fingerprint===p.host.fingerprint));
    }
    const devices = [...new Map([host, ...extra, ...roomHosts, ...(excerptHostOnly ? [] : [...members.values()].flatMap((p) => p.devices))].filter((d) => d && d.address !== this.address).map(d=>[d.address,d])).values()];
    const firstID = wire.newID(), lid = n.id ? this.localSendID(n.id) : n.lid || (["question", "task"].includes(n.kind) && !n.sub ? firstID : wire.newID()), at = this.now();
    const plain = [];
    for (const f of n.files || []) plain.push({ name: wire.safeName(f.name), bytes: f.bytes instanceof Uint8Array ? f.bytes : new Uint8Array(await f.arrayBuffer()) });
    checkFiles(plain.map((f) => ({ name: f.name, size: f.bytes.length })));
    const replyKeys=members.group && n.receiver?.host ? (n.target ? [{key:n.target.fingerprint,admission:members.epochs.get(n.target.fingerprint)||""}] : devices.filter(d=>!this.me.devices.some(own=>own.fingerprint===d.fingerprint)).map(d=>({key:d.fingerprint,admission:members.epochs.get(d.fingerprint)||""}))).sort((a,b)=>a.key.localeCompare(b.key)) : [];
    if(members.group && n.target && n.human?.proof.some(e=>e.role==="human"))throw Error("Group human guest execution audience is not enabled.");
    const prepared=await this.prepareReceiverRequest(n.receiver,{id:n.target?lid:firstID,lid,conv:c.id,root:c.root,ts:Math.floor(at/1000),kind:n.kind,body:n.body,reply_to:n.reply_to||"",origin:n.origin||"",emotion:n.emotion||"",target:n.target,pid:n.pid,...(members.group?{group_admission:members.epochs.get(this.fp),group_replies:replyKeys}:{})},plain,checks);
    const recs = [], fan = [...members.values()].map((p) => ({ person: p.person, roster: p.hash }));
    for (const dev of devices) {
      const pin = await this.groupRead(checks,"pins",dev.address);
      if (!pin || pin.pending || pin.fingerprint !== dev.fingerprint) throw new Error("Participation recipient key is no longer pinned.");
      let ok = false, detail = "";
      try { if (info.role === "human") await this.requireHumanSupport(dev.address, pin); else await this.requireAgentIdentity(dev.address, pin, info.external?wire.CapExternalParticipation:wire.agentRequirement(n)); if(members.group)await this.groupSupport(dev.address,pin); [ok, detail] = await this.supports(dev.address, pin); }
      catch (e) { if(e.code==="human_unsupported"){ok=false;detail="peer_update: " + e.message;}else{if (!retryable(e)) throw e; detail = "server_unavailable: cannot reach your server";} }
      if(prepared)await this.receiverSupport(dev.address,pin);
      const recipient = await this.pubOf(pin), sealed = [];
      for (const f of plain) sealed.push({ ...await wire.encryptFile(f.bytes, f.name, recipient), uploaded: false });
      const id = prepared && n.target?.address===dev.address && n.target.fingerprint===dev.fingerprint ? lid : recs.length ? wire.newID() : firstID;
      const replica = members.group ? (n.target ? dev.address!==info.host.address : !!n.replica) : !!n.replica || this.me.devices.some((d) => d.address === dev.address) && n.target?.address !== dev.address;
      const recipientPerson=[...members.values()].find(p=>p.devices.some(d=>d.address===dev.address));
      const pairFan=members.group?[{person:this.me.person,roster:this.me.hash},...(recipientPerson&&recipientPerson.person!==this.me.person?[{person:recipientPerson.person,roster:recipientPerson.hash}]:[])]:fan;
      const inner = { ...n, v: wire.Version2, id, from: this.address, to: dev.address, ts: Math.floor(at / 1000), conv: c.id, root: c.root, lid, replica, fan:pairFan, attachments: sealed.map((f) => f.attachment),receiver_route:prepared?.route };
      inner.send_group=await this.sendGroupCopy(dev.address,pin,n.send_group);
      const envelope = await wire.seal(inner, this.keys, recipient);
      recs.push({ ...n,send_group_wire:!!inner.send_group,...(prepared?{receiver_route:prepared.route,recipient_fp:dev.fingerprint}:{}), ...(info.role === "human" ? {recipient_fp:dev.fingerprint} : {}), id, conv: c.id, lid, at, to: dev.address, replica, own: this.me.devices.some((d) => d.address === dev.address), required_cap: members.group?wire.CapGroup:info.role === "human" ? wire.CapHumanParticipation : wire.CapExternalParticipation, ...(members.group?{recipient_fp:dev.fingerprint,group_admission:members.epochs.get(dev.fingerprint)||""}:{}), envelope,
        attachments: inner.attachments, files: sealed.length ? sealed : undefined, state: ok ? "queued" : "waiting", detail });
    }
    if (!recs.length) throw new Error("No participation recipient.");
    for (const rec of recs) { const gate = await this.externalGate(c, rec, checks); if (gate.why) throw new Error(gate.why); }
    const carriers=[];
    if(members.group && n.sub==="event" && ["invite","share"].includes(wire.parseEvent(n.body).type)) {
      const selectedRows=(await this.convMessages(c.id,await this.store.all("inbox"),await this.store.all("outbox")));
      for (const ref of wire.parseEvent(n.body).grant || []) {
        const source=selectedRows.find(row=>row.lid===ref.lid&&(row.fp||this.fp)===ref.fingerprint);
        await this.groupAgentSelection(c.id,source,members,checks);
      }
      const g=await this.groupRead(checks,"kv","group/"+c.id), records=g.records.map(wire.parseGroupCommit),packet=wire.parseGroupContext(g.context);
      for(const dev of devices) {
        for(const page of this.groupProofPages(records)){const last=page.at(-1);carriers.push(await this.groupCarrierCopy(packet.root,wire.SubGroupProof,{v:1,seq:last.seq,hash:last.hash},wire.groupJournalJSON({records:page,more:false}),dev,{pid:n.pid}));}
        carriers.push(await this.groupCarrierCopy(packet.root,wire.SubGroupContext,{v:1,seq:packet.state.seq,hash:await wire.groupStateHash(packet.state)},wire.groupContextJSON(packet),dev,{pid:n.pid}));
      }
    }
    if(prepared && carriers.length)throw Error("Receiver setup does not belong on participation proof carriers.");
    try { await this.commitReceiverCopies([...recs,...carriers],prepared,checks,members.group?await this.roomStoredOps(c.id,checks,n,this.fp,events,members):[],n.queued); }
    catch (e) {
      // Receipt push can change an earlier outbox row in this captured scope.
      // Nothing is committed yet: recheck the same signed turn, never recreate
      // its invitation or retry a turn after any copy has been posted.
      if (e instanceof StoreConflict && attempt < 3) return this.sendExternal(c, n, info, excerptHostOnly, attempt + 1);
      throw e;
    }
    await this.keepSent(plain); this.changed();
    if(n.queued)this.queueOutbox();else if(prepared)await this.post(prepared.delegation);else for (const r of recs) if (r.state === "queued") await this.post(r);
    if (!n.queued) for (const r of carriers) await this.post(r);
    const least = copyOrder(recs);
    return { id: recs[0].id, lid, state: least.state, detail: least.detail, copies: recs.map((r) => ({ id: r.id, to: r.to, state: r.state, detail: r.detail })) };
  }

  async groupAgentSelection(conv, source, members, checks = []) {
    if (!source || !source.lid || source.kind !== "message" || source.sub || source.pid || source.target || source.agent_id || source.status || source.control || source.aside || source.excerpt_pid || source.history || await this.isRetracted(source)) throw Error("Selected group context is not an original ordinary human turn.");
    const author = source.fp || this.fp;
    if (!members.epochs.has(author) || source.group_admission !== members.epochs.get(this.fp)) throw Error("Selected group context admission is no longer current.");
    const hash = await wire.groupHistoryContentHash(conv, source);
    if (!source.to) {
      const pin = await this.groupRead(checks,"pins",source.from), seen = await this.groupRead(checks,"lids",author+"/"+source.lid);
      if (!pin || pin.pending || pin.fingerprint !== author || !seen || seen.history || seen.id !== source.id || seen.hash !== hash) throw Error("Selected group context lacks its exact verified original source.");
    }
    return (await this.groupSelectedItems(conv,[{lid:source.lid,author,hash}],checks))[0];
  }

  async sendGrantedExcerpts(c, invite) {
    const { info } = await this.agentConv(invite.pid);
    if (info.role === "human" && (info.state !== "active" || info.held)) return;
    const msgs = (await this.convMessages(c.id, await this.store.all("inbox"), await this.store.all("outbox")));
    for (const ref of invite.grant || []) {
      const lid = await wire.excerptLID(invite.pid, ref);
      if ((await this.store.all("outbox")).some((r) => r.conv === c.id && r.pid === invite.pid && r.sub === "excerpt" && r.lid === lid)) continue;
      const m = msgs.find((r) => r.lid === ref.lid && (r.fp || this.fp) === ref.fingerprint && !r.sub && !r.excerpt_pid && !r.deleted);
      if (!m || await this.isRetracted(m)) continue; // missing selections stay honestly missing
      if (c.kind === "group") await this.groupAgentSelection(c.id,m,await this.dmMembers(c));
      const item = this.itemOf(m, !m.fp);
      const shown = (await (c.kind==="group"?this.groupThread(c.id):this.dm(c.id))).messages.find((r) => r.id === m.id);
      if (!shown || shown.deleted) continue;
      item.body = shown.edited ? shown.text : shown.body; // freeze exactly the selected visible revision
      const files = [];
      for (let i = 0; i < (m.attachments || []).length; i++) {
        try { const f = await this.openFile(m.id, i, m.to ? "out" : "in"); files.push({ name: f.name, size: f.bytes.length, bytes: f.bytes }); }
        catch (e) { /* retain manifest without unavailable bytes */ }
      }
      await this.sendExternal(c, { kind: "message", sub: "excerpt", pid: invite.pid, lid, body: wire.historyJSON(item), replica: true, files }, info, true);
    }
  }

  // dmMembers are c's member persons as pinned here now: this device's
  // person and the other one unless frozen, each with the roster its root
  // names.
  async groupVisitorTargets(conv,packet,checks=[]) {
    const g=await this.groupRead(checks,"kv","group/"+conv), c={id:conv,kind:"group",root:g.root},events=await this.convEvents(conv,checks),members=await this.dmMembers(c,events,checks,packet),out=[];
    for(const pid of new Set(events.map(x=>x.e.pid))) {
      const info=this.resolveAgent(pid,events,members);
      if(!info.external||!info.invite||!["invited","active"].includes(info.state)||!info.host)continue;
      const device=info.host.devices.find(d=>d.address===info.host.address&&d.fingerprint===info.host.fingerprint);
      if(device&&device.address!==this.address)out.push({pid,device});
    }
    return out;
  }

  async dmMembers(c, events = null, checks = [], currentPacket=null) {
    const group = c.kind === "group" || JSON.parse(c.root).kind === "group";
    const packet = group ? currentPacket || await this.groupCurrentState(c.id, checks) : null;
    const root = group ? packet.root : wire.parseRoot(c.root);
    const out = new Map();
    if (group) { out.group=packet;out.epochs=new Map();out.groupInvites=new Set();out.roomEvents=new Set();out.roomAuthors=new Map(); }
    for (const member of group ? await wire.effectiveGroupMembers(packet.state,packet.withdrawals) : root.members) {
      const p = group ? await this.groupRead(checks, member.person === this.me?.person ? "kv" : "persons", member.person === this.me?.person ? "person" : member.person) : member.person === this.me?.person ? this.me : await this.store.get("persons", member.person);
      if (p && p.state !== "conflict" && p.hashes.includes(group ? member.roster : wire.rootMember(root, p.person))) {
        out.set(p.person,p);
        if (group) { const epoch=await wire.groupAdmissionHash(member.admission);for(const d of p.devices)out.epochs.set(d.fingerprint,epoch); }
      }
    }
    out.hosts = new Map();
    for (const { e } of events || await this.convEvents(c.id)) {
      const h = ["invite", "scope", "share"].includes(e.type) && e.host; // a scope names its invite's host
      if (!h || out.has(h.person)) continue;
      const p = group ? await this.groupRead(checks, h.person === this.me?.person ? "kv" : "persons", h.person === this.me?.person ? "person" : h.person) : h.person === this.me?.person ? this.me : await this.store.get("persons", h.person);
      if (p && ["self", "pinned"].includes(p.state) && p.devices.some((d) => d.address === h.address && d.fingerprint === h.fingerprint)) out.hosts.set(h.person, p);
    }
    if(group) for(const {e,hash} of events || await this.convEvents(c.id)) {
      const kept=await this.groupRead(checks,"kv","room-event/"+c.id+"/"+hash);
      if(!kept || kept.pid!==e.pid)continue;
      const p=await this.groupRead(checks,e.author.person===this.me?.person?"kv":"persons",e.author.person===this.me?.person?"person":e.author.person);
      if(p && ["self","pinned"].includes(p.state) && p.hashes.includes(e.author.roster) && p.devices.some(d=>d.address===e.author.address&&d.fingerprint===e.author.fingerprint)) {out.roomEvents.add(hash);out.roomAuthors.set(p.person,p);}
    }
    if (group) {
      const g=await this.groupRead(checks,"kv","group/"+c.id);
      for(const {e,hash} of events || await this.convEvents(c.id)) {
        const scope=e.group, author=out.get(e.author.person), record=scope && g.records[scope.seq] && wire.parseGroupCommit(g.records[scope.seq]);
        if(e.type!=="invite"&&e.type!=="scope"&&e.type!=="share" || !scope || !record || record.hash!==scope.hash || scope.seq>packet.state.seq || !out.roomEvents.has(hash) && (!author || out.epochs.get(e.author.fingerprint)!==e.author.group_admission))continue; // a room scope carries its invite's binding
        const host=out.get(e.host.person), isMember=!!host;
        if(scope.host_role==="member" ? !isMember || out.epochs.get(e.host.fingerprint)!==scope.host_admission : scope.host_role!=="visitor" || isMember || scope.host_admission)continue;
        if(!out.roomEvents.has(hash) && (e.task_keys || []).some((fp,i)=>!out.epochs.get(fp)||out.epochs.get(fp)!==scope.task_admissions?.[i]))continue;
        if(scope.host_role==="visitor" && e.role!=="human" && ["invite","scope"].includes(e.type) && (!wire.groupMember(packet.state,e.author.person)?.admin || !record.admins.includes(e.author.person)))continue;
        out.groupInvites.add(hash);
      }
    }
    out.shareGrants = new Map();
    if(group)for(const {e,hash} of events || await this.convEvents(c.id,checks)) {
      if(e.type!=="share")continue;
      const allowed=[];
      for(const ref of e.grant||[]) {
        let visible=!!await this.groupRead(checks,"kv",this.roomReaderKey(c.id,ref,e.author.person,e.author.group_admission));
        const member=wire.groupMember(packet.state,e.author.person);
        if(!visible && member && await wire.groupAdmissionHash(member.admission)===e.author.group_admission) {
          const h=member.admission.history?.find(h=>h.lid===ref.lid&&h.author===ref.fingerprint);
          if(h)for(const row of await this.authorityRows({conv:c.id},checks))if(row.lid===ref.lid && (row.fp||this.fp)===ref.fingerprint && await wire.groupHistoryContentHash(c.id,row)===h.hash)visible=true;
        }
        if(visible)allowed.push(ref);
      }
      out.shareGrants.set(hash,allowed);
    }
    return out;
  }

  // convEvents are the participation records of conv held here, received
  // and sent, oldest first: a set by record hash, as the core keeps them
  // (the same signed record in another message is one record).
  async authorityRows(scope, checks) {
    const rows = [];
    for (const s of ["inbox", "outbox"]) {
      const matching = (await this.store.all(s)).filter((r) => scopeRow(r, scope));
      if (checks) checks.push({ s, scope, rows: matching.map((v) => ({ k: v.id, v: authorityValue(s, v) })).sort((a, b) => a.k < b.k ? -1 : a.k > b.k ? 1 : 0) });
      rows.push(...matching);
    }
    return rows;
  }

  async convEvents(conv, checks, pid) {
    // Admitted HumanTurn proof is the same signed ledger metadata, not a
    // new membership table. Guard the whole captured set, including absence.
    const rows = (await this.authorityRows({ conv }, checks)).sort((a, b) => a.at - b.at);
    const out = [], seen = new Set();
    for(const raw of await this.groupRead(checks||[],"kv","room-memberships/"+conv)||[]) { const e=wire.parseEvent(raw), hash=await wire.eventHash(e);if((!pid||e.pid===pid)&&!seen.has(hash)){seen.add(hash);out.push({e,hash});} }
    for (const m of rows) {
      if(m.history&&m.group_history)continue; // inert past proof is never a live participation ledger
      try {
        const events = m.sub === "event" ? [wire.parseEvent(m.body)] : m.human ? wire.parseHumanTurn(m.human).proof : [];
        for (const e of events) {
          if (pid && e.pid !== pid) continue;
          const hash = await wire.eventHash(e);
          if (!seen.has(hash)) out.push({ e, hash });
          seen.add(hash);
        }
      } catch (err) { /* only successfully admitted records count */ }
    }
    return out;
  }

  // resolveAgent is the core's resolve (participation.go): a
  // participation's state from the set of its records, never their order,
  // with the members as pinned now. A record that does not count is held:
  // it has no effect until its evidence is here.
  resolveAgent(pid, evs, m) {
    const info = { pid, role: "", state: "pending", held: 0, host: null, inviter: null, grant: [], taskKeys: [], note: "", invite: "", scope: "", decision: "", dismissal: "", conflict: "", invited: 0, audience: "", until: 0 };
    // A current device of a member person (the person as seen from it);
    // an author also names a step of that person's chain.
    const at = (p, address, fp) => (p && p.devices.some((d) => d.address === address && d.fingerprint === fp) ? { ...p, address, fingerprint: fp } : null);
    const author = (a) => { const p = m.get(a.person); return p && p.hashes.includes(a.roster) && (m.group ? m.epochs.get(a.fingerprint)===a.group_admission : !a.group_admission) ? at(p, a.address, a.fingerprint) : null; };
    const host = (h) => (h ? at(m.get(h.person) || m.hosts?.get(h.person), h.address, h.fingerprint) : null);
    const memberKey = (fp) => [...m.values()].some((p) => p.devices.some((d) => d.fingerprint === fp));
    const priorAuthor=x=>m.roomEvents?.has(x.hash)?at(m.roomAuthors?.get(x.e.author.person),x.e.author.address,x.e.author.fingerprint):null;
    const invites = new Map(), decisions = [], dismisses = [], scopes = [], shares = [];
    for (const x of evs.filter((y) => y.e.pid === pid)) {
      const p = m.hosts?.get(x.e.author.person);
      const decisionAuthor = ["accept", "decline"].includes(x.e.type) && !x.e.author.group_admission && p?.hashes.includes(x.e.author.roster) && at(p, x.e.author.address, x.e.author.fingerprint);
      const humanLeaveAuthor = x.e.type === "dismiss" && !x.e.author.group_admission && p?.hashes.includes(x.e.author.roster) && at(p, x.e.author.address, x.e.author.fingerprint) && evs.some(y => y.e.pid === pid && ["invite", "scope"].includes(y.e.type) && (y.e.role === "human" || y.e.audience === "room" && y.e.group?.host_role === "visitor") && author(y.e.author) && host(y.e.host) && y.e.host.person === x.e.author.person && y.e.host.address === x.e.author.address && y.e.host.fingerprint === x.e.author.fingerprint);
      if (!author(x.e.author) && !priorAuthor(x) && !decisionAuthor && !humanLeaveAuthor) { info.held++; continue; }
      if (x.e.type === "invite") {
        if (!host(x.e.host) || !(x.e.task_keys || []).every(memberKey) && !m.roomEvents?.has(x.hash) || (m.group ? !m.groupInvites.has(x.hash) : !!x.e.group)) { info.held++; continue; }
        invites.set(x.hash, x);
      } else if (x.e.type === "scope") { if (!m.group || m.groupInvites.has(x.hash)) scopes.push(x); } // an invite's public projection, by its own author; in a group, a room scope whose binding verifies
      else if (x.e.type === "share") shares.push(x);
      else if (x.e.type === "dismiss") dismisses.push(x);
      else decisions.push(x);
    }
    // Without the invite itself (another guest, an outside assistant host), its
    // author's scope stands for it: host, agent and role, never grant, task
    // keys or note. Holding the invite, only its exact projection counts; an
    // invite held here that does not count is never stood in for.
    if (!invites.size && scopes.length) {
      const s = scopes.reduce((a, b) => (b.hash < a.hash ? b : a));
      const agree = scopes.every(x => wire.sameScope(x.e, s.e)); // protocol.SameScope: audience, end time and group binding too
      if (!agree) return Object.assign(info, { state: "conflict", conflict: "different invitation scopes share this participation id" });
      if (host(s.e.host) && !evs.some((y) => y.e.pid === pid && y.e.type === "invite" && y.hash === s.e.prev)) {
        invites.set(s.e.prev, { e: { v: 1, conv: s.e.conv, pid, type: "invite", prev: "", author: s.e.author, ts: s.e.ts, host: s.e.host, grant: null, audience: s.e.audience, task_keys: null, note: "", ...(s.e.role ? { role: s.e.role } : {}), ...(s.e.until ? { until: s.e.until } : {}), ...(s.e.group ? { group: s.e.group } : {}) }, hash: s.e.prev });
        info.scope = s.hash;
      } else info.held++;
    }
    const known = new Set(invites.keys());
    let inv = null;
    if (invites.size === 1) {
      [[info.invite, inv]] = [...invites];
      if (!info.scope) for (const s of scopes) if (wire.projectsHash(s.e, inv.e, info.invite) && (!info.scope || s.hash < info.scope)) info.scope = s.hash;
      Object.assign(info, { role: inv.e.role || "", host: host(inv.e.host), inviter: author(inv.e.author)||priorAuthor(inv), grant: [...(inv.e.grant || [])], taskKeys: m.group && m.roomEvents?.has(inv.hash) ? (inv.e.task_keys||[]).filter((fp,i)=>m.epochs.get(fp)===inv.e.group.task_admissions?.[i]) : inv.e.task_keys || [], audience: inv.e.audience || "", until: inv.e.until || 0,
        external: m.group ? inv.e.group.host_role === "visitor" : !m.has(inv.e.host.person),
        ...(m.group ? {group:inv.e.group} : {}),
        ...(inv.e.host.agent_id ? { agent_id: inv.e.host.agent_id } : {}), note: inv.e.note, invited: inv.e.ts, state: "invited" });
    } else if (invites.size > 1) {
      Object.assign(info, { state: "conflict", conflict: "different invites share this participation id" });
    }
    info.member = !!m.group && info.role === "" && info.audience === "room" && !info.until;
    info.inviters = info.inviter ? [info.inviter] : [];
    info.shares = [];
    for(const x of shares) {
      if(!inv || x.e.prev!==info.invite || !["person","address","fingerprint","agent_id"].every(k=>(x.e.host?.[k]||"")===(inv.e.host?.[k]||"")) || !m.groupInvites?.has(x.hash) || x.e.audience!==info.audience) {info.held++;continue;}
      info.shares.push(wire.eventJSON(x.e));const p=author(x.e.author)||priorAuthor(x);if(!info.inviters.some(i=>i.person===p.person))info.inviters.push(p);
      for(const g of m.shareGrants?.get(x.hash)||[])if(!info.grant.some(r=>r.lid===g.lid&&r.fingerprint===g.fingerprint))info.grant.push(g);
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
      const h = inv?.e.host;
      if (!author(x.e.author) && !priorAuthor(x) && !((info.role === "human" || info.member && info.external) && x.e.author.person === h.person && x.e.author.address === h.address && x.e.author.fingerprint === h.fingerprint)) { info.held++; continue; }
      if(m.group && info.member && info.external && !priorAuthor(x) && ![info.host.person,info.inviter.person].includes(x.e.author.person) && !wire.groupMember(m.group.state,x.e.author.person)?.admin) {info.held++;continue;}
      if (!known.has(x.e.prev)) { info.held++; continue; }
      if (info.state !== "dismissed" || x.hash < info.dismissal) { info.dismissal = x.hash; info.dismissalEvent = wire.eventJSON(x.e); }
      info.state = "dismissed";
    }
    // A room's end time, by this device's clock (ROOM_V1 §2.2): past it, it counts as ended; it orders nothing.
    if (info.until > 0 && ["invited", "active"].includes(info.state) && Math.floor(this.now() / 1000) > info.until) info.state = "dismissed";
    return info;
  }

  // agentsOf resolves every participation of conv c.
  async participationsOf(c) {
    const m = await this.dmMembers(c), evs = await this.convEvents(c.id);
    const waiting=(await this.store.all("outbox")).filter(r=>r.conv===c.id&&r.state==="waiting"&&r.required_cap===wire.CapHumanParticipation&&r.detail?.startsWith("peer_update: ")),persons=[this.me,...await this.store.all("persons")].filter(Boolean);
    return [...new Set(evs.map((x) => x.e.pid))].map(pid=>{const p=this.resolveAgent(pid,evs,m);p.needs_update=[...new Set(waiting.filter(r=>r.pid===pid).map(r=>persons.find(p=>p.devices.some(d=>d.address===r.to))?.label||"Someone"))];return p;});
  }
  async agentsOf(c) { return (await this.participationsOf(c)).filter(p => p.role !== "human"); }

  // heldOpen: whether turn m, held for the person (conv_held: nothing runs
  // it), still waits for them. The person's own turn in the same
  // conversation, from this browser or another device of theirs, answers
  // it if written once m had reached this browser, as client.turnClosesHeld
  // closes it there (state manual): one written earlier and delivered late
  // does not. A turn sent here was written when it was stored (at); one
  // from another device at the start of the second that device stamped
  // (ts). A request to an agent, an agent's output, a control or a record
  // answers nothing.
  heldOpen(m, msgs) {
    const wrote = (x) => x.fp && Number.isSafeInteger(x.ts) ? x.ts * 1000 : x.at;
    return m.state === "conv_held" && !msgs.some((x) => x !== m && (x.own || !x.fp && !x.excerpt_pid) && !x.sub && !x.target &&
      !String(x.origin || "").startsWith("agent:") && wrote(x) >= m.at);
  }

  // needsYouOf adds what waits for this person in conversation conv
  // (ui.ConvItem; reasons as client.Review*): an invitation for, or a
  // request to, an agent on another device of this person, as its
  // participation resolves here and its host last said (a status reaches
  // only a request this browser sent). This browser runs no agent, so each
  // is read-only: decided on decide_on, never here or by opening it. Person
  // turns held for the person go to held: they are answered here. Its
  // sentences name devices with words (peerWordsFn), never the address.
  needsYouOf(conv, infos, msgs, ctls, needsYou, held, words = (a) => a, reports = []) {
    const mine = (h) => !!h && !!this.me && h.person === this.me.person && h.address !== this.address;
    for (const info of infos) {
      if (info.role === "human" || info.state !== "invited" || !mine(info.host)) continue;
      // Listed when its first record reached this browser (msgs are oldest
      // first); the inviter's own time is only its claim.
      const first = msgs.find((m) => m.sub === "event" && m.pid === info.pid);
      needsYou.push({ reason: "agent_invite", conv, pid: info.pid, peer: info.inviter ? info.inviter.address : "", why: (info.inviter ? info.inviter.label : "Someone") + " invited your agent on " + words(info.host.address) + ". Decide there: this browser runs no agent.",
        excerpt: firstLine(info.note), at: first ? iso(first.at) : isoClaim(info.invited) || iso(this.now()), decide_on: info.host.address });
    }
    for (const m of msgs) {
      if (m.state === "conv_held") {
        if (this.heldOpen(m, msgs)) held.push({ reason: "person_turn", actions: ["resolve"], conv, ...(m.pid ? { pid: m.pid } : {}), id: m.id, peer: m.from, kind: m.kind, why: "Held for you: nothing runs it. Answer here if you want to.", excerpt: firstLine(m.body), at: iso(m.at), ...(m.read ? {} : { unread: true }) });
        continue;
      }
      const info = m.pid && m.target && ["question", "task"].includes(m.kind) ? infos.find((p) => p.pid === m.pid) : null;
      if (!info || !mine(info.host) || m.target.address !== info.host.address) continue;
      const here = !m.fp && !m.excerpt_pid, fp = here ? this.fp : m.fp;
      const answered = msgs.some((r) => r.fp && r.reply_to === m.id && (r.kind === "answer" || r.kind === "result"));
      const e = this.execOn(ctls.filter((x) => x.sub === wire.SubStatus && x.ref && x.ref.id === m.lid && x.ref.fingerprint === fp && x.from === m.target.address), m.target.address, answered);
      if (!e || !["awaiting", "needs_human", "interrupted", "running"].includes(e.state)) continue; // client.PageReview's states
      needsYou.push({ reason: { awaiting: "agent_awaiting", needs_human: "agent_needs_human", interrupted: "agent_interrupted", running: "agent_running" }[e.state], conv, pid: m.pid, id: m.id, peer: here ? this.address : m.from, kind: m.kind,
        why: (e.state === "needs_human" && this.needsYouText(m, reports)) || (e.state === "running" ? "Running on " + words(e.host) : "Decide on " + words(e.host)) + (e.detail ? ": " + e.detail : "") + ". This browser runs no agent.", excerpt: firstLine(m.body), at: iso(m.at), decide_on: e.host, ...(this.continuationOf(m,fp,e)?{continuation:this.continuationOf(m,fp,e),actions:["continue"]}:{}) });
    }
  }

  // agentView is a participation as the page shows it (ui.AgentView).
  agentView(info, msgs, peer, groupPeople=null, role="") {
    const hostHere = !!info.host && info.host.address === this.address;
    const label = (p) => (p === this.me ? this.me.label : p ? p.label : "");
    const shared = [], people = groupPeople || [this.me, peer].filter(Boolean);
    let missing = 0;
    for (const g of info.grant) {
      const m = msgs.find((x) => x.lid === g.lid && !x.sub && (x.excerpt_pid === info.pid && x.claimed_key === g.fingerprint || (!x.replica || groupPeople && !x.history && !x.pid && !x.target) && (x.fp || this.fp) === g.fingerprint));
      if (m) shared.push(m.id); else missing++;
    }
    const whose = hostHere ? "your" : label(info.host) + "'s";
    const v = { member:!!info.member || !!groupPeople && !info.external,pids:[info.pid],inviters:(info.inviters||[]).map(p=>this.personView(p)),pid: info.pid, state: info.state, host: this.personView(info.host), host_here: hostHere, inviter: this.personView(info.inviter),
      ...(info.external ? { external: true } : {}),
      ...(info.agent_id ? { agent_id: info.agent_id } : {}),
      note: info.note, shared, missing, tasks_from: info.taskKeys.map((fp) => {
        const p = people.find((x) => x.devices.some((d) => d.fingerprint === fp));
        return p && this.personView({ ...p, fingerprint: fp, address: p.devices.find((d) => d.fingerprint === fp).address });
      }).filter(Boolean),
      held: info.held, invited: isoClaim(info.invited), can_decide: false, can_dismiss: false, can_ask: false, state_text: "" };
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
    if (groupPeople) {
      v.state_text=info.member && info.state==="active" && !info.held && !hostHere ? "Member of this group. Can be asked by every member; its owner decides what runs. Stays until explicitly removed." : v.state_text.replaceAll("this DM","this group").replaceAll("either of you","current group members");
      if(info.member && info.external){v.can_dismiss=[info.host.person,info.inviter.person].includes(this.me.person)||!!groupPeople.find(p=>p.person===this.me.person)?.admin;v.state_text += " Runs on " + label(info.host) + "’s device, outside this group; that device receives every new message and file here until the agent is removed.";}
      if(role!=="member"){v.can_ask=false;v.can_dismiss=false;}
    } else if (info.external && !people.some((p) => p.person === this.me?.person && wire.validID(p.person) && p.person !== info.host.person)) { v.can_ask = false; v.can_dismiss = false; }
    return v;
  }

  // agentConv finds the conversation and participation of pid.
  async agentConv(pid) {
    const row = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].find((m) => m.pid === pid && m.sub === "event" && m.conv);
    const conv = row?.conv || await this.store.get("kv", "room-pid/"+pid);
    const c = conv && (await this.store.get("convs", conv) || await this.groupRecord(conv));
    if (!c) throw new Error("No such agent in a conversation here.");
    const info = this.resolveAgent(pid, await this.convEvents(c.id), await this.dmMembers(c));
    return { c, info };
  }

  author() {
    return { person: this.me.person, roster: this.me.hash, address: this.address, fingerprint: this.fp };
  }

  // inviteAgent invites the agent on the other member's device: never this
  // browser, which runs none. The earlier messages it may be shown and the
  // member keys it takes tasks from without asking are chosen by the person.
  async inviteAgent({ conv, host, share = [], tasks_from: tasks = [], note = "", agent_id = "" }) {
    const c = await this.store.get("convs", conv) || await this.groupRecord(conv);
    if (!c) throw new Error("No conversation " + conv + " here.");
    if (!this.me) throw new Error("Set up your person first.");
    if (host === this.address) throw new Error("This browser runs no agent: invite the agent on the other person's computer.");
    const m = await this.dmMembers(c);
    if (!m.has(this.me.person)) throw new Error("This device does not speak for a member of that conversation.");
    let hpp = [...m.values()].find((p) => p.devices.some((d) => d.address === host));
    const external = !hpp;
    if (external) {
      if(m.group && !wire.groupMember(m.group.state,this.me.person)?.admin)throw new Error("Only a group administrator can add an agent whose owner is outside this group.");
      if (!agent_id) throw new Error("An outside host needs an exact named agent.");
      const pin = await this.sendKey(host);
      hpp = await this.personOf(host, pin);
      await this.requireAgentIdentity(host, pin, wire.CapExternalParticipation);
    }
    const hp = { person: hpp.person, address: host, fingerprint: hpp.devices.find((d) => d.address === host).fingerprint };
    const selected = agent_id ? await this.namedTarget(host, agent_id) : null;
    if (selected && selected.fingerprint !== hp.fingerprint) throw new Error("Selected agent's host key differs from the member's verified device.");
    const msgs = [...(await this.store.all("inbox")), ...(await this.store.all("outbox"))].filter((x) => x.conv === conv);
    const grant = [];
    for (const id of share) {
      const x = msgs.find((y) => y.id === id && !y.sub && (!y.replica || m.group) && !y.excerpt_pid);
      if (!x || !x.lid) throw new Error("A message chosen to share is not an earlier message of this conversation that can be shared.");
      if (m.group) await this.groupAgentSelection(conv,x,m);
      grant.push({ lid: x.lid, fingerprint: x.fp || this.fp });
    }
    for (const fp of tasks) if (![...m.values()].some((p) => p.devices.some((d) => d.fingerprint === fp))) throw new Error(fp + " is not the key of a member of that conversation.");
    const group=m.group?{seq:m.group.state.seq,hash:await wire.groupStateHash(m.group.state),host_role:external?"visitor":"member",...(external?{}:{host_admission:m.epochs.get(hp.fingerprint)}),...(tasks.length?{task_admissions:tasks.map(fp=>m.epochs.get(fp))}:{})}:null;
    const existing = m.group && (await this.agentsOf(c)).find(p=>["invited","active"].includes(p.state)&&p.host?.address===hp.address&&p.host.fingerprint===hp.fingerprint&&(p.agent_id||"")===agent_id);
    const e = await wire.signEvent(this.keys, { conv, pid: existing ? existing.pid : wire.newID(), type: existing ? "share" : "invite",...(existing?{prev:existing.invite}:{}), ts: Math.floor(this.now() / 1000), author: {...this.author(),...(m.group?{group_admission:m.epochs.get(this.fp)}:{})},
      host: { person: hp.person, address: hp.address, fingerprint: hp.fingerprint, ...(selected ? { agent_id: selected.agent_id } : {}) }, grant: grant.length ? grant : null,
      audience: existing ? existing.audience : m.group ? "room" : "conversation", task_keys: existing ? null : tasks.length ? tasks : null, note: existing ? "" : String(note).trim(),...(group?{group:existing?{...group,task_admissions:undefined}:group}:{}) });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid: e.pid });
    if(!existing && m.group) { const scope=await wire.signEvent(this.keys,await wire.scopeOf(e,e.ts));await this.sendConv(c,{kind:"message",sub:"event",body:wire.eventJSON(scope),pid:e.pid}); }
    if (external) await this.sendGrantedExcerpts(c, e);
    return this.agentView((await this.agentConv(e.pid)).info,[],null,m.group?[...m.values()]:null,"member");
  }

  // dismissAgent ends a participation, following the record it ends.
  async dismissAgent(pid,duplicates=true) {
    const { c, info } = await this.agentConv(pid);
    if(duplicates && (c.kind==="group"||JSON.parse(c.root).kind==="group"))for(const other of await this.participationsOf(c))if(other.pid!==pid && !other.role && ["active","invited"].includes(other.state) && other.host?.address===info.host.address && other.host.fingerprint===info.host.fingerprint && (other.agent_id||"")===(info.agent_id||""))await this.dismissAgent(other.pid,false);
    if (info.role === "human") throw new Error("Use the human participation end action.");
    const members=await this.dmMembers(c);
    if (!this.me || !members.has(this.me.person) && !(info.member && info.external && info.host?.person===this.me.person)) throw new Error("This device does not speak for a member of that conversation.");
    if(members.group && info.member && info.external && ![info.host.person,info.inviter.person].includes(this.me.person) && !wire.groupMember(members.group.state,this.me.person)?.admin)throw new Error("Only a group administrator, the person who added this outside agent, or its owner can remove it.");
    let prev = info.decision;
    if (info.state === "invited") prev = info.invite;
    else if (!["active", "declined", "conflict"].includes(info.state) || !prev) throw new Error("The agent's participation is " + info.state + ".");
    const e = await wire.signEvent(this.keys, { conv: c.id, pid, type: "dismiss", prev, ts: Math.floor(this.now() / 1000), author: {...this.author(),...(members.group?{group_admission:members.epochs.get(this.fp)}:{})} });
    await this.sendConv(c, { kind: "message", sub: "event", body: wire.eventJSON(e), pid });
    return { pid, state: "dismissed" };
  }

  // askAgent sends a question or task to an active participation's agent,
  // on the other person's computer: its one target.
  async askAgent({ id: sendID = "", pid, kind = "question", body, topic="",send_group="",files = [], reply_receiver = null }) {
    if(send_group&&!wire.validID(send_group))throw Error("Invalid send group ID.");
    body = String(body || "").trim();
    if (wire.blank(body) && !files.length) throw new Error("Write what to ask or add a file first.");
    checkFiles(files);
    if (kind !== "question" && kind !== "task") throw new Error("An agent is asked a question or given a task.");
    const { c, info } = await this.agentConv(pid);
    if (info.role === "human") throw new Error("Human participants receive ordinary chat, never agent requests.");
    if (info.state !== "active" || info.held > 0) throw new Error("The agent's participation is " + info.state + ": not active.");
    if (info.host.address === this.address) throw new Error("This browser runs no agent.");
    topic=await this.outgoingTopic(c.id,topic,"");
    const replyTo=topic?await this.chatTopicHead(c.id,topic):"";
    const members=await this.dmMembers(c);
    if (!members.group) { // with guests present the room sees addressed work too; a guest asks only as her exact accepted scope
      const guest = members.has(this.me?.person) ? null : (await this.participationsOf(c)).find(p => p.role === "human" && p.state === "active" && !p.held && p.host?.address === this.address && p.host.fingerprint === this.fp);
      if (!members.has(this.me?.person) && !guest) throw new Error("Only a current conversation member or accepted guest asks its agent.");
      const human = await this.humanPlan(c, guest?.pid || "");
      if (human) return this.sendHumanTurn(c, { id: sendID, queued: true, kind, body, topic,send_group,reply_to:replyTo,files, pid, origin: "ui", receiver: reply_receiver, target: { address: info.host.address, fingerprint: info.host.fingerprint, ...(info.agent_id ? { agent_id: info.agent_id } : {}) } }, human, info);
    }
    if (!members.has(this.me?.person)) throw new Error("Only a current conversation member asks its agent.");
    return this.sendConv(c, { id: sendID, queued: true, kind, body, topic,send_group,reply_to:replyTo,files, pid, origin: "ui", receiver:reply_receiver, target: { address: info.host.address, fingerprint: info.host.fingerprint, ...(info.agent_id ? { agent_id: info.agent_id } : {}), ...(members.group?{group_admission:members.epochs.get(this.fp)}:{}) } });
  }

  // Do it confirms only this device's own question, exact answer bytes and
  // executor. The ordinary send paths retain the recipient's task policy.
  async proposalCandidate(id) {
    if (!this.me || this.me.state !== "self" || !this.me.devices?.some(d=>d.address===this.address && d.fingerprint===this.fp) || !(this.me.human_keys || []).includes(this.fp)) throw Error("Only an approved current human device confirms a proposal.");
    const p = await this.store.get("inbox", id);
    if (!p || p.kind !== "answer" || p.status !== wire.StatusProposal || p.control || p.sub || p.history || p.replica && !p.conv || !p.fp || wire.blank(p.body)) throw Error("That is not a proposal you can confirm here.");
    const ref = p.conv ? p.lid : p.id;
    if (await this.store.get("erased", erasedKey(p.conv || "", p.fp, ref))) throw Error("This proposal was deleted here.");
    const pin = await this.store.get("pins", p.from);
    if (!pin || pin.pending || pin.fingerprint !== p.fp) throw Error("The proposal's host key changed.");
    const rows = [...await this.store.all("inbox"), ...await this.store.all("outbox")];
    if (rows.some(r => r.control && (r.conv || "") === (p.conv || "") && r.ref?.id === ref && r.ref.fingerprint === p.fp && [wire.SubRevision, wire.SubRetraction].includes(r.sub))) throw Error("The proposal was edited or deleted after it was made.");
    const q = (await this.store.all("outbox")).find(r => !r.control && !r.aside && r.kind === "question" && r.id === p.reply_to && (r.conv || "") === (p.conv || ""));
    if (!q || (q.pid || "") !== (p.pid || "") || (q.topic || "") !== (p.topic || "")) throw Error("Only the device that asked the question confirms its proposal.");
    if (q.target ? q.target.address !== p.from || q.target.fingerprint !== p.fp || (q.target.agent_id || "") !== (p.agent_id || "") : p.agent_id || p.conv || q.to !== p.from || q.fp !== p.fp) throw Error("The proposal differs from the question's exact executor.");
    let c, info;
    if (p.conv) {
      ({ c, info } = await this.agentConv(p.pid));
      if (c.id !== p.conv || info.state !== "active" || info.held || info.host.address !== p.from || info.host.fingerprint !== p.fp || (info.agent_id || "") !== (q.target.agent_id || "")) throw Error("The proposal's exact agent is no longer active here.");
    }
    return { p, q, c, info };
  }

  async proposalActions(m) {
    if (m.kind !== "answer" || m.status !== wire.StatusProposal) return [];
    try {
      const {p} = await this.proposalCandidate(m.id);
      return await this.confirmedProposal(p) ? [] : ["do_it"];
    } catch { return []; }
  }

  async confirmedProposal(p) {
    const ref = p.conv ? p.lid : p.id;
    return (await this.store.all("outbox")).find(r => !r.control && !r.aside && r.kind === "task" && (r.conv || "") === (p.conv || "") && r.reply_to === ref && r.body === p.body && (r.pid || "") === (p.pid || "") && (!p.conv ? r.to === p.from : r.target?.address === p.from && r.target.fingerprint === p.fp && (r.target.agent_id || "") === (p.agent_id || "")));
  }

  async confirmProposal(id) {
    this.confirmingProposals ??= new Map();
    if (this.confirmingProposals.has(id)) return this.confirmingProposals.get(id);
    const run = this.confirmProposalOnce(id);
    this.confirmingProposals.set(id, run);
    try { return await run; } finally { this.confirmingProposals.delete(id); }
  }

  async confirmProposalOnce(id) {
    const {p,q,c,info} = await this.proposalCandidate(id);
    const kept = await this.confirmedProposal(p);
    if (kept) return {note:"This proposal was already sent as a task."};
    // The existing durable send correlation also deduplicates concurrent
    // engines/retries after reload. No new identity, grant or queue.
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(["confirm-proposal", this.fp, p.conv || "", p.lid || p.id, p.fp]))));
    const sendID = Array.from(digest.slice(0,16), b=>b.toString(16).padStart(2,"0")).join("");
    try {
      if (!c) await this.sendV1({id:sendID,queued:true,to:p.from,kind:"task",body:p.body,replyTo:p.id,files:[],status:"",target:q.target || null});
      else {
        const n = {id:sendID,queued:true,kind:"task",body:p.body,topic:p.topic || "",reply_to:p.lid,pid:p.pid,origin:"ui",target:q.target,files:[]};
        const members = await this.dmMembers(c);
        const guest = members.has(this.me?.person) ? null : (await this.participationsOf(c)).find(x=>x.role==="human" && x.state==="active" && !x.held && x.host?.address===this.address && x.host.fingerprint===this.fp);
        if (!members.has(this.me?.person) && !guest) throw Error("Only a current member or accepted guest confirms this proposal.");
        const human = !members.group && await this.humanPlan(c,guest?.pid || "");
        if (human) await this.sendHumanTurn(c,n,human,info);
        else await this.sendConv(c,n);
      }
    } catch(e) { if (!(await this.confirmedProposal(p))) throw e; }
    return {note:"Task saved; sending to the same agent."};
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
    const saved = await this.store.get("kv", "notify");
    const st = { enabled: false, allowed: [], mutes: [], rev: 0, synced: 0, chat_mutes: 1, ...(saved || {}) };
    if (saved && !saved.chat_mutes) {
      // Legacy state cannot tell a deny from a never-allowed person.
      // Preserve quiet known DMs; groups and future chats default unmuted.
      const convs = await this.store.all("convs");
      for (const c of convs) {
        if (c.kind === "group") continue;
        const p = await this.store.get("persons", c.peer);
        const allowed = p && p.state !== "conflict" && (st.allowed.includes(p.person) || convs.some(x => x.peer === p.person && x.creator === this.address));
        if (!allowed && !st.mutes.includes(c.id)) st.mutes.push(c.id);
      }
      st.chat_mutes = 1; st.rev++;
      await put(this.store, "kv", "notify", st);
    }
    return st;
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

  // Current verified chat members and active participant hosts may alert.
  // Unknown requests and pending invitations add no sender; mutes stay per chat.
  async notifySenders(st) {
    const eligible = new Set(), exact = new Map(), chats = [];
    for (const c of await this.store.all("convs")) {
      if (c.kind === "group") continue;
      const root = wire.parseRoot(c.root);
      if (!root.members.some(m => m.person === this.me?.person)) continue;
      const p = await this.store.get("persons", c.peer);
      if (p?.state === "pinned") { eligible.add(p.person); chats.push(c); }
    }
    for (const g of await this.store.all("kv")) if (g?.root && g.context && Array.isArray(g.records)) {
      try {
        const conv = wire.parseGroupContext(g.context).state.conv, packet = await this.groupCurrentState(conv);
        if (!wire.groupMember(packet.state, this.me?.person)) continue;
        for (const p of await this.groupPeople(packet)) if (p.person !== this.me.person) eligible.add(p.person);
        chats.push({id:conv,kind:"group",root:g.root});
      } catch (_) { /* unavailable/current membership is not a notification grant */ }
    }
    for (const p of await this.store.all("persons")) {
      if (!eligible.has(p.person) || p.state === "conflict") continue;
      for (const d of p.devices) {
        const pin = await this.store.get("pins", d.address);
        if (pin && !pin.pending && pin.fingerprint === d.fingerprint) exact.set(d.address, {address:d.address,fingerprint:d.fingerprint});
      }
    }
    for (const c of chats) {
      try {
        for (const info of await this.participationsOf(c)) {
          if (info.state !== "active" || info.held || !info.host?.address || info.host.address === this.address) continue;
          const pin = await this.store.get("pins", info.host.address);
          if (pin && !pin.pending && pin.fingerprint === info.host.fingerprint) exact.set(info.host.address, {address:info.host.address,fingerprint:pin.fingerprint});
        }
      } catch (_) { /* unavailable participation supplies no alert sender */ }
    }
    return [...exact.values()];
  }

  // syncNotify sends this device's preferences to the relay; what it sent
  // is confirmed (a change made meanwhile stays pending).
  async syncNotify() {
    const st = await this.notifyState();
    if (!(await this.notifyInfo())) return;
    const mutes = [];
    for (const conv of await this.effectiveNotifyMutes(st)) mutes.push(await wire.notifyChannel(conv, this.fp));
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

  async memberNotifyChats() {
    const chats = [];
    for (const c of await this.store.all("convs")) {
      if (c.kind === "group" || !c.root || !wire.parseRoot(c.root).members.some(m => m.person === this.me?.person)) continue;
      if ((await this.store.get("persons", c.peer))?.state === "pinned") chats.push(c);
    }
    return chats;
  }

  async effectiveNotifyMutes(st) {
    const chats = await this.memberNotifyChats(), quiet = new Set(chats.filter(c => st.mutes.includes(c.id)).map(c => c.peer));
    return [...new Set([...st.mutes, ...chats.filter(c => quiet.has(c.peer)).map(c => c.id)])];
  }

  async muteDM(conv, muted) {
    if (!(await this.store.get("convs", conv)) && !(await this.groupRecord(conv))) throw new Error("No chat " + conv + " here.");
    const chats = await this.memberNotifyChats(), selected = chats.find(c => c.id === conv);
    const ids = selected ? chats.filter(c => c.peer === selected.peer).map(c => c.id) : [conv];
    await this.changeNotify((st) => { st.mutes = st.mutes.filter(c => !ids.includes(c)).concat(muted ? ids : []); });
    return { note: muted ? "This chat is muted." : "This chat notifies you again." };
  }

  async allowSender(person, allowed) {
    if (!(await this.store.get("persons", person))) throw new Error("That person is not known here.");
    const convs = await this.store.all("convs");
    await this.changeNotify((st) => {
      st.allowed = st.allowed.filter(p => p !== person).concat(allowed ? [person] : []);
      if (!allowed) for (const c of convs) if (c.kind !== "group" && c.peer === person && !st.mutes.includes(c.id)) st.mutes.push(c.id);
    });
    return { note: allowed ? "Alerts from them are on." : "Alerts from them are off." };
  }

  // notifySeen reports messages this page presented (visible, focused,
  // that DM, newest in view): the relay then does not alert for them. It
  // is not a read receipt; nobody else learns it.
  async notifySeen(conv, ids) {
    const st = await this.notifyState();
    if (!st.enabled || !(await this.notifyInfo()) || !(await this.store.get("convs", conv)) && !(await this.groupRecord(conv))) return {};
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
    for (const g of await this.store.all("kv")) if (g?.root && g.context && Array.isArray(g.records)) {
      const conv = wire.parseGroupContext(g.context).state.conv;
      if ((await wire.notifyChannel(conv, this.fp)) === chan) return conv;
    }
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
      mutes: await this.effectiveNotifyMutes(st), allowed: (await this.notifySenders(st)).map((s) => s.address) };
  }

  // openFile fetches a received file's ciphertext, and returns it decrypted
  // only if both match the signed manifest: its safe name, the image type
  // its bytes show (never SVG or HTML), and the bytes. A file this device
  // sent opens from the copy it kept (openSent).
  async openFile(id, i, dir) {
    if (dir && dir !== "in" && dir !== "out") throw new Error("dir is in or out.");
    const m = dir === "out" ? null : await this.store.get("inbox", id);
    if (dir === "in" && !m) throw new Error("No received message with that id here.");
    if (dir === "out" || !m) return this.openSent(id, i);
    if (await this.isRetracted(m)) throw new Error("That message was deleted.");
    // No dir given: a received id is the sender's choice and can equal a
    // sent one here; then neither is opened.
    if (!dir && (await this.store.get("outbox", id))) throw new Error("That id names both a received and a sent message here: say which (dir in or out).");
    const att = (m.attachments || [])[i];
    if (!att) throw new Error("That message has no such file.");
    if (!att.blob) throw new Error("This file came with the conversation's history: get it from " + (m.synced_from || "your other device") + " first.");
    const plain = await wire.decryptFile(await this.cipherOf(att), att, this.keys);
    return { name: wire.safeName(att.name), size: att.size, image: wire.sniffImage(plain), bytes: plain };
  }

  // openSent is a file this device sent, from the copy it kept for itself
  // (keepSent): the same checks as a received file, against its own key.
  async openSent(id, i) {
    const m = await this.store.get("outbox", id);
    if (!m) throw new Error("No such message here.");
    if (await this.isRetracted(m)) throw new Error("That message was deleted.");
    const att = (m.attachments || [])[i];
    if (!att) throw new Error("That message has no such file.");
    const kept = att.sha256 && (await this.store.get("files", "kept/" + att.sha256));
    if (!kept) throw new Error("Not kept on this device: only the recipient has this file.");
    const plain = await wire.decryptFile(kept.ct, kept.attachment, this.keys);
    return { name: wire.safeName(att.name), size: att.size, image: wire.sniffImage(plain), bytes: plain };
  }

  // eventText says what an agent participation record in a DM does, as
  // the laptop's page does. This browser only shows it: it never invites,
  // hosts or runs an agent.
  // oneRowPerRecord shows a signed participation record once (liveagent.go
  // eventShown): each original's onward copy of it is no new timeline row,
  // and a held invitation stands for its own public scope.
  async oneRowPerRecord(msgs, humans) {
    const seen = new Set(), out = [], invited = new Set();
    for (const m of msgs) if (m.sub === "event") try { if (wire.parseEvent(m.body).type === "invite") invited.add(m.pid); } catch (e) { /* unreadable */ }
    for (const m of msgs) {
      if (m.sub === "event" && invited.has(m.pid)) try { if (wire.parseEvent(m.body).type === "scope") continue; } catch (e) { /* unreadable */ } // the invitation itself is shown
      if (m.sub === "event") { // one row per exact signed record (human or assistant)
        let hash = "";
        try { hash = await wire.eventHash(wire.parseEvent(m.body)); } catch (e) { /* shown as unreadable */ }
        if (hash && seen.has(hash)) continue;
        if (hash) seen.add(hash);
      }
      out.push(m);
    }
    return out;
  }

  // eventFields is liveagent.go eventFields: a participation record's type
  // and PID, and its author plainly: the author's person label as known
  // here (this person, people, or any person pinned here, in known), else
  // the signing device's address; empty for a record that cannot be read.
  eventFields(body, people, known = []) {
    let e;
    try { e = wire.parseEvent(body); } catch (err) { return { type: "", pid: "", by: "" }; }
    const p = [this.me, ...people, ...known].find((x) => x && x.person && x.person === e.author.person && x.label);
    return { type: e.type, pid: e.pid, by: p ? p.label : e.author.address };
  }

  // lastEvent is liveagent.go lastEvent: a conversation's latest row as a
  // chat list says it when it is a participation record (an invitation's
  // public scope is its invite), else undefined.
  lastEvent(m, people, known) {
    if (!m || m.sub !== "event") return undefined;
    const f = this.eventFields(m.body, people, known);
    return f.type ? { kind: f.type === "scope" ? "invite" : f.type, pid: f.pid, by: f.by } : undefined;
  }

  eventText(body, peer, people=null, human=false, view=null) {
    let e;
    try { e = wire.parseEvent(body); } catch (err) { return "A record about an agent that cannot be read here."; }
    const me = this.me && this.me.person;
    const known=id=>(people||[peer]).filter(Boolean).find(p=>p.person===id);
    const who = (id) => (id && id === me ? "You" : known(id)?.label || (people ? "An outside agent's owner" : "Someone not in this DM"));
    const whose = (id) => (id && id === me ? "your" : known(id)?.label ? known(id).label+"'s" : "an outside host's");
    const room=people?"this group":"this DM";
    if (human || e.role === "human") { // liveagent.go humanEventText: people are invited, join, decline, leave or are removed
      const info = typeof human === "object" ? human : null, originals = view?.originals || [];
      const author = e.author.person === me ? "You" : [peer, ...originals].filter(Boolean).find(p => p.person === e.author.person)?.label || (view && !view.member ? "A DM member" : "Someone not in this DM");
      const name = (subject) => info?.host?.person && info.host.person === me ? (subject ? "You" : "you") : info?.host ? info.host.label || info.host.address : e.host ? e.host.address : subject ? "A person" : "a person";
      switch (e.type) {
      case "invite": case "scope": return author + " invited " + name(false) + " into this DM.";
      case "accept": return name(true) + " joined this DM.";
      case "decline": return name(true) + " declined the invitation.";
      case "dismiss": return info?.host && e.author.person === info.host.person && e.author.address === info.host.address ? name(true) + " left this DM." : author + " removed " + name(false) + " from this DM.";
      }
      return "A participation record (" + e.type + ").";
    }
    switch (e.type) {
    case "invite": case "scope": return who(e.author.person) + " invited " + (e.host ? whose(e.host.person) + (people && e.group?.host_role==="visitor" ? " agent, whose owner is outside this group" : " agent") : "an agent") + " into " + room + "."; // a scope is the invitation's public part
    case "share": return who(e.author.person) + " shared more selected messages with the agent already in " + room + ".";
    case "accept": return who(e.author.person) + (people && !e.author.group_admission ? " accepted: this outside agent now receives every new message and file in this group until removed." : " accepted: the agent joins " + room + ".");
    case "decline": return who(e.author.person) + " declined the invitation for the agent.";
    case "dismiss": return who(e.author.person) + " dismissed the agent: it gets nothing more from " + room + ".";
    }
    return "A record about an agent (" + e.type + ").";
  }

  // thread is the device conversation holding message id.
  async thread(id) {
    const g = (await this.v1Threads()).find((x) => x.some((m) => m.id === id));
    if (!g) throw new Error("No message with that id.");
    const peer = g[0].peer;
    const pin = await this.store.get("pins", peer);
    const ctls = [...(await this.store.all("inbox")).filter((r) => r.control && !r.conv && r.from === peer).map((r) => ({ ...r, author: r.fp, addr: r.from })),
      ...(await this.store.all("outbox")).filter((r) => r.control && !r.conv && r.to === peer).map((r) => ({ ...r, author: this.fp, addr: this.address }))];
    const statuses = ctls.filter((x) => x.sub === wire.SubStatus && x.author !== this.fp);
    const execView = (m) => {
      if (m.dir !== "out" || (m.kind !== "question" && m.kind !== "task")) return {};
      const answered = g.some((r) => r.dir === "in" && r.reply_to === m.id && (r.kind === "answer" || r.kind === "result"));
      const e = this.execOn(statuses.filter((x) => x.ref && x.ref.id === m.id && x.ref.fingerprint === this.fp), peer, answered);
      const continuation=this.continuationOf(m,this.fp,e);
      return e ? { exec: e, ...(continuation?{continuation,actions:["continue"]}:{}) } : {};
    };
    const ctlView = (m) => {
      const targetFp = m.dir === "in" ? m.fp : this.fp;
      const rel = ctls.filter((x) => x.ref && x.ref.id === m.id && x.ref.fingerprint === targetFp && x.sub !== wire.SubStatus && x.sub !== wire.SubDecision);
      // Grouped by key; shown as the device's address (the core's By in a device thread), never a label.
      const addr = (fp) => (fp === this.fp ? this.address : peer);
      const view = this.controlsOn(rel, targetFp, (x) => x.author, addr, (fp) => fp === this.fp, addr);
      view.can = view.deleted ? [] : ["react", ...(targetFp === this.fp ? ["edit", "delete"] : [])];
      return view;
    };
    const heldText = "Held for you: nothing runs in this browser. Answer it here if you want to.";
    const peerWords = (await this.peerWordsFn())(peer); // the sentences name a person and device, never the address
    const topic = this.topicSummary(g, await this.topicLocals(), Math.floor(this.now() / 1000), pin);
    const permissionPerson=pin && !pin.pending ? [this.me,...await this.store.all("persons")].find(p=>p && ["self","pinned"].includes(p.state)&&p.devices.some(d=>d.address===peer&&d.fingerprint===pin.fingerprint)) : null;
    return { ...(permissionPerson?{permission_person:this.personView(permissionPerson)}:{}), id: g[0].id, peer, topic, key: { pinned: pin ? pin.fingerprint : "", pending: pin && pin.pending ? pin.pending.fingerprint : "" }, approved: false, task_grant: "",
      messages: await Promise.all(g.map(async (m) => {
        const inbound = m.dir === "in";
        return { id: m.id, dir: m.dir, from: inbound ? m.from : this.address, to: inbound ? this.address : m.to, kind: m.kind, body: m.body,
          ...(m.agent_id ? { agent_id: m.agent_id } : {}), ...(m.target ? { target: m.target } : {}),
          reply_to: m.reply_to || "",quote:m.quote||"",sent_at:sentAt(m), at: iso(m.at), state: m.state, status: m.status || "", detail: m.detail || "", ...this.cancellationView(m), unread: inbound && !m.read,
          files: await Promise.all((m.attachments || []).map(async (a) => ({ name: wire.safeName(a.name), size: a.size, ...(inbound ? this.fileState(a) : await this.sentState(a)) }))),
          ...ctlView(m), ...execView(m),
          actions: m.status === wire.StatusProposal ? await this.proposalActions(m) : inbound && m.state === "held" && !(pin && pin.pending) ? ["reply"] : [], // a held question or task: answered here by hand
          author: m.agent_id ? { label: "Agent " + m.agent_id, about: "Named executor asserted by host " + m.from + "; its host key and request bind this ID." }
            : inbound ? { label: m.from, about: "Signed with " + m.from + "'s key. Whether a person or one of their agents wrote it is not recorded." }
            : { label: "You", about: "Sent from this browser." },
          state_text: inbound ? (m.state === "held" ? heldText : m.state === "answered" ? "You answered it here." : "") : this.outStateText(m, peerWords) };
      })) };
  }

  async markRead(ids) {
    const ops=[],checks=[],own=await this.groupRead(checks,"kv","person");
    for(const id of ids||[]) {
      const m=await this.groupRead(checks,"inbox",id);if(!m)continue;
      if(!m.read)ops.push({s:"inbox",k:id,v:{...m,read:true}});
      const ref=this.readRef(m);
      if(own?.state==="self" && ref) {
        const k=this.readMarkKey(own.person,ref);
        if(!await this.groupRead(checks,"kv",k))ops.push({s:"kv",k,v:{owner:own.person,ref}});
      }
    }
    try{if(ops.length){await this.store.write(ops,checks);this.changed();}}catch(e){if(e instanceof StoreConflict)return this.markRead(ids);throw e;}
    this.syncReadMarks().catch(()=>{});
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

  // refreshThread asks once, when a device conversation is opened, about
  // its messages the server still holds.
  async refreshThread(id) {
    const g = (await this.v1Threads()).find((x) => x.some((m) => m.id === id)) || [];
    for (const m of g.filter((x) => x.dir === "out" && x.state === "custody").slice(-20)) {
      try {
        const r = await this.call("GET", "/v1/messages/" + m.id);
        if (r && r.state) await this.setOutState(m.id, r.state);
      } catch (e) { /* unknown stays unknown */ }
    }
    return { text: "" };
  }

  // Match native Resolve for an ordinary turn held for this person. This
  // changes only its local attention state; no reply, status or work is sent.
  async resolveHeldTurn(id) {
    const m = wire.validID(id || "") ? await this.store.get("inbox", id) : null;
    if (!m || m.id !== id || m.v !== 2 || !m.conv || m.state !== "conv_held" || m.own || m.control || m.sub ||
        !["question", "task"].includes(m.kind) || m.target && (m.target.address !== this.address || m.target.agent_id)) return null;
    try {
      await this.store.write([{s:"inbox",k:id,v:{...m,state:"resolved"}}],[{s:"inbox",k:id,v:m}]);
    } catch (e) { if (e instanceof StoreConflict) return this.resolveHeldTurn(id); throw e; }
    this.changed();
    return {note:"Marked as handled on this device. No reply was sent."};
  }

  // A received report is local history, never a job or a remote decision.
  // Dismiss only its exact inbox row; an outbox copy with that id is untouched.
  async dismissReport(id) {
    const m = wire.validID(id || "") ? await this.store.get("inbox", id) : null;
    if (!m || m.id !== id || m.v !== 1 || m.control || m.conv || m.sub || m.kind !== "message" ||
      m.status !== "review_notice" || m.reply_to || !Array.isArray(m.attachments || []) || (m.attachments || []).length) {
      throw new Error("Only a received report can be dismissed here.");
    }
    if (!m.resolved) {
      await this.store.write([{ s: "inbox", k: id, v: { ...m, resolved: true } }]);
      this.changed();
    }
    return { note: "Report dismissed on this device only. Nothing changed on " + m.from + "." };
  }

  // ---- invitations (MEL-533): ui/liveinvites.go, the same routes and words

  // invite makes one invitation link for a new person (an admin's device):
  // the server makes their label from the name and writes the names on the
  // invitation as its unsigned hints.
  async invite(r) {
    const cfg = await this.call("GET", "/v1/google/config", undefined, { signed: false });
    if (cfg.web_client_id || cfg.desktop_client_id) throw new Error("Invite by email in Settings → Workspaces. Invitation codes are an advanced admin CLI fallback.");
    const name = String(r.name || "").trim();
    if (!name) throw new Error("Write the name of the person you invite.");
    if (!wire.validInviteHint(name, wire.MaxInviteHint)) throw new Error("Use a shorter name (up to 64 characters) without line breaks.");
    if (!inviteDays.includes(r.days)) throw new Error("Choose how long the link works: " + inviteDays.slice(0, -1).join(", ") + " or " + inviteDays.at(-1) + " days.");
    const from = this.me && wire.validInviteHint(this.me.label, wire.MaxInviteHint) ? this.me.label : "";
    const workspace = wire.validInviteHint(this.workspaceName, wire.MaxWorkspaceHint) ? this.workspaceName : "";
    const ttl = r.days * 24 * 3600 * 1e9; // nanoseconds, as Go's time.Duration
    let out;
    try {
      out = await this.call("POST", "/v1/admin/invites", { name, from, workspace, ttl, admin: !!r.admin, browser: true });
    } catch (e) {
      if (e.status === 403) throw new Error("Only an admin of your server can invite people.");
      if (e.status === 409) throw new Error("Your server cannot make invitation links: it needs to serve its page over HTTPS that browsers trust.");
      throw e;
    }
    const inv = wire.decodeInvite(out.code);
    const link = inv.hub + "/#" + out.code;
    return { link, label: out.label || inv.label, expires: iso(this.now() + r.days * 24 * 3600 * 1000), message: inviteMessage(from, link) };
  }

  // invitesList says whether this device may invite and, for an admin's
  // device, the invitations still waiting to be used.
  async invitesList() {
    const cfg = await this.call("GET", "/v1/google/config", undefined, { signed: false });
    if (cfg.web_client_id || cfg.desktop_client_id) return { can_invite: false, invites: [] };
    const p = await this.call("GET", "/v1/admin/invites");
    return { can_invite: !!p.can_invite, invites: (p.invites || []).map((i) => ({ id: i.id, name: i.name || "", label: i.label, admin: !!i.admin, by: i.created_by,
      ...(i.created_at ? { created: iso(i.created_at * 1000) } : {}), expires: iso(i.expires * 1000) })) };
  }

  async revokeInvite(id) {
    if (!id) throw new Error("Choose an invitation.");
    try {
      await this.call("POST", "/v1/admin/invites/revoke", { id });
    } catch (e) {
      if (e.status === 404) throw new Error("That invitation was already used, withdrawn or expired.");
      if (e.status === 403) throw new Error("Only an admin of your server can invite people.");
      throw e;
    }
  }

  // getApp is where to get the AgentNet app (getapp.json, as Go reads it)
  // for this server's version, and the device this browser runs on.
  async getApp() {
    const table = this.getAppTable || (this.getAppTable = await (await this.fetch(this.base + "/assets/getapp.json", { cache: "no-store" })).json());
    if (!this.version) { try { await this.features(); } catch (e) { /* the latest release then */ } }
    const nav = globalThis.navigator || {};
    return { version: this.version || "", detected: getapp.detectPlatform(nav.userAgent, nav.userAgentData && nav.userAgentData.platform, nav.maxTouchPoints),
      platforms: getapp.downloads(table, this.version).map((d) => ({ id: d.id, label: d.label, url: d.url })) };
  }

  // api answers the page's requests as the daemon's page API does.
  async api(path, body) {
    const sending = ["/api/send", "/api/dm/send", "/api/dm/agent/ask"].includes(path) || path === "/api/act" && ["reply", "do_it"].includes(body?.do);
    if (sending && this.closing) throw Error("This workspace is closing. Your draft is kept.");
    const request = this.apiResult(path, body);
    if (sending) { this.sendRequests ??= new Set(); this.sendRequests.add(request); }
    try { return await request; }
    finally { if (sending) this.sendRequests.delete(request); }
  }

  async apiResult(path, body) {
    try { return await this.apiRequest(path,body); }
    catch(e) {
      if (["human_unsupported","agent_identity_unsupported"].includes(e.code)) {
        const p=[this.me,...await this.store.all("persons")].find(p=>p?.devices?.some(d=>d.address===e.address));
        throw Object.assign(Error((p?.label || "This person")+"’s app needs an update first."),{code:e.code});
      }
      throw e;
    }
  }

  async apiRequest(path, body) {
    if (this.revoked && body !== undefined && !path.startsWith("/api/act")) {
      throw new Error("This device was removed from its server: nothing more is sent or received here.");
    }
    const u = new URL(path, "http://page");
    if (["/api/send", "/api/dm/send", "/api/dm/agent/ask"].includes(u.pathname) && body?.reply_receiver != null) {
      const r = body.reply_receiver;
      if (!r.host && (typeof r !== "object" || Array.isArray(r) || r.kind !== "human" || Object.keys(r).some(k => !["kind", "agent_id", "instructions", "mode"].includes(k)) || r.agent_id || r.instructions || r.mode)) throw new Error("Unsupported reply receiver: this browser cannot run a local assistant or native session. Choose Me or select its exact linked host.");
      if (r.host) {
        wire.parseReceiverChoice(Object.fromEntries(Object.entries(r).filter(([k]) => k !== "host"))); await this.receiverHost(r.host);

      }
    }
    switch (u.pathname) {
    case "/api/groups/new": return {id:await this.createGroup(body.title)};
    case "/api/groups/invitations": return this.groupInvitations();
    case "/api/groups/invite": return this.inviteGroup(body);
    case "/api/groups/decide": return this.decideGroup(body);
    case "/api/groups/cancel": return this.cancelGroup(body);
    case "/api/groups/refresh": return this.refreshGroup(body);
    case "/api/groups/publish": return this.publishGroupInvitation(body.id);
    case "/api/groups/manage": return this.manageGroup(body);
    case "/api/reply-sessions": {
      if (body !== undefined) throw Error("Receiver catalog is read only.");
      const address = u.searchParams.get("host"), fingerprint = u.searchParams.get("host_key");
      if (!!address !== !!fingerprint) throw Error("Reply receiver catalog requires host and host_key together.");
      return this.receiverCatalog(address ? { address, fingerprint } : null);
    }
    case "/api/agents":
      if (body !== undefined) throw new Error("This browser runs no local agents or responders; configure agents on their host computer.");
      return this.agentCatalog(u.searchParams.get("host") || "");
    case "/api/typing": {
      if (body === undefined) return this.typingView({ conv: u.searchParams.get("conv") || "", peer: u.searchParams.get("peer") || "", thread: u.searchParams.get("thread") || "" });
      if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some((k) => k !== "scope" && k !== "active") || body.active != null && typeof body.active !== "boolean") throw new Error("invalid typing request");
      const scope = body.scope || {};
      if (typeof scope !== "object" || Array.isArray(scope) || Object.entries(scope).some(([k, v]) => !["conv", "peer", "thread"].includes(k) || v != null && typeof v !== "string")) throw new Error("invalid typing scope");
      return this.sendTyping(scope, !!body.active);
    }
    case "/api/typing/preferences": return this.setTypingPreferences(body);
    case "/api/overview": return this.overview(u.searchParams.get("topics") !== "1");
    case "/api/dm": return this.decorateChatTopics(await this.dm(u.searchParams.get("id")));
    case "/api/thread": return this.thread(u.searchParams.get("id"));
    case "/api/dm/new": return { id: await this.newDM(body.address) };
    case "/api/dm/send": return this.sendDM(body);
    case "/api/conversation/delete": return this.deleteConversation(body);
    case "/api/topics": return this.topicList(u.searchParams);
    case "/api/topic/create": case "/api/topic/archive": case "/api/topic/delete": case "/api/topic/rename": case "/api/topic/done": case "/api/topic/reopen": return this.changeTopic(u.pathname.split("/").pop(), body);
    case "/api/dm/guest/check": return this.checkHuman(body);
    case "/api/dm/guest/invite": return this.changeHuman("invite", body);
    case "/api/dm/guest/decide": return this.changeHuman("decide", body);
    case "/api/dm/guest/end": return this.changeHuman("end", body);
    case "/api/dm/agent/invite": return this.inviteAgent(body);
    case "/api/file": return this.openFile(u.searchParams.get("id"), Number(u.searchParams.get("i")), u.searchParams.get("dir") || "");
    case "/api/file/request": return this.requestFile(body.id, Number(body.index));
    case "/api/message/react": case "/api/message/edit": case "/api/message/delete": return this.messageControl(u.pathname.split("/").pop(), body || {});
    case "/api/storage": return this.storageSummary();
    case "/api/workspace": return this.workspaceInfo();
    case "/api/workspace/name": return this.renameWorkspace(body);
    case "/api/drive": return this.driveService().drive(body === undefined ? { conv: u.searchParams.get("conv") || "", action: "status" } : body);
    case "/api/drive/upload": return this.driveService().driveUpload(body.conv, body.file, !!body.confirm);
    case "/api/drive/service": return this.driveService(); // the page's panel needs the consent entry points bound to its clicks (user gesture)
    case "/api/drive/setup": return this.storageSetup().storageSetup(body === undefined ? { action: "status" } : body);
    case "/api/teams": { const t = this.teamsService(); return t ? t.teams() : { status: "unsupported", current: false, reason: "your server does not publish a workspace realm", at: iso(this.now()), truncated: false, teams: [] }; }
    case "/api/team": { const t = this.teamsService(); if (!t) throw new Error("Teams are not available on this server."); return t.team(body || {}); }
    case "/api/teams/snapshot": { const t = this.teamsService(); if (!t) throw new Error("Teams are not available on this server."); return t.teamsSnapshot((body || {}).teams || []); }
    case "/api/operator/decide": return this.decide(body || {});
    case "/api/approvals": // liveapprovals.go: a browser keeps no standing grants
      if (body !== undefined) throw new Error("The list of standing grants is read only.");
      return { questions: [], tasks: [], participations: [], read_only: true };
    case "/api/approvals/revoke": throw new Error("Nothing runs in this browser: approvals and grants are made and revoked on a computer with AgentNet.");
    case "/api/notify/enable": return this.enableNotify();
    case "/api/notify/disable": return this.disableNotify();
    case "/api/notify/mute": return this.muteDM(body.conv, !!body.muted);
    case "/api/notify/allow": return this.allowSender(body.person, !!body.allowed);
    case "/api/notify/seen": return this.notifySeen(body.conv, body.ids);
    case "/api/notify/resolve": return { conv: await this.resolveChannel(u.searchParams.get("chan") || "") };
    case "/api/dm/agent/dismiss": return this.dismissAgent(body.pid);
    case "/api/dm/agent/ask": return this.askAgent(body);
    case "/api/dm/agent/decide": throw new Error("This browser runs no agent: an agent is accepted on the computer that runs it.");
    case "/api/person/picture":
      if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some(k=>k!=="png") || typeof body.png !== "string") throw Error("Choose a picture.");
      return this.setPersonPicture(body.png);
    case "/api/person/label":
      if (!body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).some((k) => k !== "label") || typeof body.label !== "string") throw new Error("Choose a display label.");
      return this.renamePerson(body.label);
    case "/api/person": return this.createPerson(body.label);
    case "/api/device/link": return this.newDeviceLink();
    case "/api/device/decide": return this.decideLink(body.id, !!body.accept, !!body.agent_host);
    case "/api/device/remove": return this.removeDevice(body.address);
    case "/api/device/service": throw new Error("A browser is always a person's device: a service joins from a computer with AgentNet.");
    case "/api/refresh": return (await this.store.get("convs", body.id)) ? this.refreshDM(body.id) : this.refreshThread(body.id);
    case "/api/act":
      if (body.do === "archive_held") return this.archiveHeldNotice(body.id);
      if (body.do === "do_it") return this.confirmProposal(body.id);
      if (body.do === "read") { await this.markRead(body.ids); return { note: "" }; }
      if (body.do === "reply") return this.replyV1(body.id, body.body, body.send_id);
      if (body.do === "resolve") return await this.resolveHeldTurn(body.id) || await this.dismissDeviceAdminNotice(body.id) || this.dismissReport(body.id);
      throw new Error("Nothing runs in this browser: accept, approve and grants are made on a computer with AgentNet.");
    case "/api/send": return this.sendDirect(body);
    case "/api/simulate": throw new Error("Not available here.");
    case "/api/google/access": return body === undefined ? this.call("GET", "/v1/google/access") : this.call("PUT", "/v1/google/access", body);
    case "/api/invite": return this.invite(body || {});
    case "/api/invites": return this.invitesList();
    case "/api/invite/revoke": await this.revokeInvite((body || {}).id); return { revoked: true };
    case "/api/get-app": return this.getApp();
    case "/api/folders": throw new Error("Folders are chosen in the AgentNet app on a computer: this browser cannot see that computer's folders.");
    }
    throw new Error("Unknown request.");
  }
}
