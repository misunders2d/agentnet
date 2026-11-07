// A positive result proves local admission of the original human request only.
// Missing projection evidence is uncertainty, never permission to acknowledge.
const idPattern = /^[0-9a-f]{32}$/;
const fields = {
  "/api/send": ["id", "to", "agent_id", "kind", "body", "reply_to", "quote", "files"],
  "/api/dm/send": ["id", "conv", "pid", "topic", "body", "reply_to", "quote", "files"],
  "/api/dm/agent/ask": ["id", "pid", "send_group", "kind", "body", "topic", "files"],
  "/api/act": ["do", "id", "send_id", "body"],
};
const empty = value => value === undefined || value === "";
const text = value => value ?? "";
const noFiles = value => value === undefined || Array.isArray(value) && value.length === 0;
// Go strings.TrimSpace uses Unicode White_Space (JS trim also removes BOM).
const trimBody = value => value.replace(/^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g, "");

function valid(entry) {
  const r = entry?.request, allowed = fields[entry?.endpoint];
  if (!allowed || !r || typeof r !== "object" || Array.isArray(r) ||
      !idPattern.test(entry.id) || typeof entry.conversation !== "string" ||
      typeof r.body !== "string" || Object.keys(r).some(k => !allowed.includes(k))) return false;
  if (Object.entries(r).some(([k, v]) => k !== "files" && v !== undefined && typeof v !== "string")) return false;
  // Stage IDs are ephemeral references, not evidence of the attachment bytes.
  if (!noFiles(r.files)) return false;
  return entry.endpoint === "/api/act" ? r.do === "reply" && r.send_id === entry.id && idPattern.test(r.id) : r.id === entry.id;
}

function human(message, me, dm) {
  return message.dir === "out" && message.from === me.address &&
    (dm ? message.send_group_author : message.from_key) === me.fingerprint &&
    empty(message.agent_id) && empty(message.agent_author_pid) &&
    !message.verified_agent && !message.history && !message.synced_from &&
    empty(message.claimed_key) && empty(message.excerpt_pid) &&
    empty(message.event) && !message.topic_event &&
    noFiles(message.files) && noFiles(message.attachments);
}

// DM projections resolve logical parents/quotes to the local physical copy.
// An ambiguous logical ID cannot establish which signed parent was selected.
function reference(messages, value) {
  if (empty(value)) return "";
  const exact = messages.filter(m => m.id === value);
  if (exact.length === 1) return exact[0].id;
  const logical = messages.filter(m => m.lid === value || m.copies?.some(c => c.id === value));
  return logical.length === 1 ? logical[0].id : undefined;
}

function dmMatches(entry, thread, me) {
  const r = entry.request, ask = entry.endpoint === "/api/dm/agent/ask";
  if (!ask && (r.conv !== entry.conversation || thread.id !== r.conv)) return false;
  const messages = thread.messages;
  if (!Array.isArray(messages)) return false;
  // topic is a display assignment, so topic organization removes original proof.
  if (messages.some(m => m.topic_event)) return false;
  const kind = ask ? r.kind || "question" : "message";
  if (ask && !["question", "task"].includes(kind)) return false;
  if (r.topic === "new") return false; // the core allocated an ID absent from the intent
  // With a parent, the displayed topic can be inherited rather than explicitly
  // signed. That projection cannot prove an explicit topic in the saved intent.
  if (!empty(r.topic) && !empty(r.reply_to)) return false;
  const reply = reference(messages, r.reply_to), quote = reference(messages, r.quote);
  if (reply === undefined || quote === undefined) return false;
  const agents = ask ? (thread.agents || []).filter(a => a.pid === r.pid || a.pids?.includes(r.pid)) : [];
  if (ask && (typeof r.pid !== "string" || !r.pid || agents.length !== 1)) return false;
  const matches = messages.filter(m => (m.lid || m.id) === entry.id && human(m, me, true) &&
    m.kind === kind && m.body === trimBody(r.body) && text(m.topic) === text(r.topic) &&
    text(m.reply_to) === reply && text(m.quote) === quote &&
    text(m.pid) === text(r.pid) && text(m.send_group) === text(r.send_group) &&
    (ask ? m.target && m.target.address === agents[0].host?.address &&
      m.target.fingerprint === agents[0].host?.fingerprint &&
      text(m.target.agent_id) === text(agents[0].agent_id) : !m.target));
  return matches.length === 1 ? matches[0] : undefined;
}

function threadMatches(entry, thread, me) {
  const r = entry.request, messages = thread.messages;
  if (!Array.isArray(messages)) return false;
  let to = r.to, kind = r.kind || "message", reply = text(r.reply_to), body = trimBody(r.body);
  if (entry.endpoint === "/api/act") {
    const parents = messages.filter(m => m.id === r.id && m.dir === "in");
    if (parents.length !== 1) return false;
    const parent = parents[0];
    to = parent.from; reply = parent.id;
    kind = parent.kind === "question" ? "answer" : parent.kind === "task" ? "result" : "message";
    // Reply keeps the submitted body; Send/SendDM trim it in their provider.
    body = r.body;
  } else if (!["message", "question", "task"].includes(kind)) return false;
  if (typeof to !== "string" || !to || thread.peer !== to) return false;
  const found = messages.filter(m => m.id === entry.id && human(m, me, false) &&
    m.to === to && m.kind === kind && m.body === body &&
    text(m.reply_to) === reply && text(m.quote) === text(r.quote) &&
    (empty(r.agent_id) ? !m.target : m.target?.address === to && m.target.agent_id === r.agent_id) &&
    (entry.endpoint !== "/api/act" || kind !== "result" || m.status === "done"));
  return found.length === 1 ? found[0] : undefined;
}

/** Read-only reconciliation. Errors and incomplete/changed views retain intent. */
export async function isSendPersisted(host, entry) {
  return !!(await persistedSend(host, entry));
}

// The stored copy of the saved request, or undefined (no proof).
async function persistedSend(host, entry) {
  if (!valid(entry)) return undefined;
  try {
    const overview = await host.api("/api/overview"), me = overview?.me;
    if (!me || typeof me.address !== "string" || !me.address || typeof me.fingerprint !== "string" || !me.fingerprint) return undefined;
    if (entry.endpoint === "/api/send" || entry.endpoint === "/api/act") {
      // Thread accepts any stored physical ID, including older/archived threads.
      const thread = await host.api("/api/thread?id=" + encodeURIComponent(entry.id));
      return threadMatches(entry, thread, me);
    }
    // A person's chat can aggregate roots; an exact agent PID binds its root.
    const ids = entry.endpoint === "/api/dm/agent/ask" ?
      [...new Set([entry.conversation, ...(overview.dms || []).map(d => d.id)])] : [entry.conversation];
    for (const id of ids) {
      let thread;
      try { thread = await host.api("/api/dm?id=" + encodeURIComponent(id)); } catch { continue; }
      const m = thread?.id === id ? dmMatches(entry, thread, me) : undefined;
      if (m) return m;
    }
  } catch { /* Lookup failure cannot acknowledge the saved request. */ }
  return undefined;
}

const stopped = m => m?.queued_send?.state === "needs_review";
const waiting = m => ["pending", "dispatching"].includes(m?.queued_send?.state);

/**
 * The person's explicit "Retry saved send" of one journal entry. A message this
 * device already holds as stopped is never sent from here: its own chat row
 * (Retry/Discard) decides. An unknown or failed lookup keeps the saved intent.
 * Returns what is proven: "held", "pending", "sent" or "discarded".
 */
export async function replaySavedSend(host, entry) {
  if (entry?.endpoint === "/api/dm/send") {
    // Its ID is this device's own; a stopped row under it is that message, whatever its files.
    let thread;
    try { thread = await host.api("/api/dm?id=" + encodeURIComponent(entry.conversation)); }
    catch { throw new Error("This message's chat could not be checked, so nothing was sent. It stays saved here; try again."); }
    if (thread?.id !== entry.conversation || !Array.isArray(thread.messages))
      throw new Error("This message's chat could not be checked, so nothing was sent. It stays saved here; try again.");
    if (thread.messages.some(m => m.dir === "out" && (m.lid || m.id) === entry.id && stopped(m))) return "held";
  }
  const state = m => stopped(m) ? "held" : waiting(m) ? "pending" : "sent";
  const found = await persistedSend(host, entry);
  if (found) return state(found);
  try { await host.api(entry.endpoint, entry.request); }
  catch (e) {
    // The host's own answer that the person discarded this exact message.
    if (e?.status >= 400 && e.status < 500 && /discarded this message/i.test(String(e.message))) return "discarded";
    const again = await persistedSend(host, entry);
    if (again) return state(again);
    throw e;
  }
  return "pending";
}

/** Words for what replaySavedSend proved; never more than that. */
export function savedSendReceipt(state) {
  return state === "held" ? "This message is stopped in its chat and was not sent. Use Retry or Discard there."
    : state === "discarded" ? "You discarded this message, so it was not sent."
    : state === "sent" ? "This message is already in its chat. It was not sent again."
    : "Saved send is in the outbox.";
}
