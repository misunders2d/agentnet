// AgentNet messenger page. Text from anyone is inserted as text nodes only.
// Every change goes through the server's actions, which use the same client
// operations and rules as the command line; the page decides nothing itself.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { thread: null, data: null, seq: -1, answering: null, lastSeen: {}, presence: {}, lens: "classic",
  drafts: {}, draftKey: null, sending: false, expanded: null, query: "", singlesOpen: {}, directoryOpen: false,
  dm: null, dmData: null, personOpen: {}, dmReply: null, dmAgent: null, seenReported: {}, pendingOpen: null,
  version: "", updating: false, newVersion: "", dialogRestore: null, dialogBusy: false };
const lenses = ["classic", "comic", "zoom"];

// present flattens children and drops the ones a condition left out (false,
// null, undefined, ""), so they never reach the page as text.
const present = (list) => list.flat(Infinity).filter((k) => k !== undefined && k !== null && k !== false && k !== "");
const node = (k) => typeof k === "string" ? document.createTextNode(k) : k;

// fill replaces an element's children; use it instead of replaceChildren.
function fill(parent, ...kids) {
  parent.replaceChildren(...present(kids).map(node));
  return parent;
}

// el builds an element; string children become text nodes.
function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of present(kids)) e.append(node(k));
  return e;
}

async function api(path, body) {
  if (window.agentnetEngine) return window.agentnetEngine.api(path, body); // a browser device (the relay's page)
  const opts = body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  };
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.json();
}

const time = (s) => new Date(s).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
const when = (s) => {
  const d = new Date(s);
  return d.toDateString() === new Date().toDateString() ? time(s)
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
};
const size = (n) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : (n / (1 << 20)).toFixed(1) + " MB";
const firstLine = (s, n) => {
  const l = (s || "").split("\n")[0];
  return l.length > n ? l.slice(0, n - 1).trimEnd() + "…" : l;
};
const announce = (t) => { $("live").textContent = t; };
const kindTag = { question: "Question", task: "Task" };
const statusWord = { declined: "Declined", failed: "Failed", timeout: "Timed out", cancelled: "Cancelled", interrupted: "Interrupted",
  review_notice: "Report" };

// Addresses are person/agent: the person leads, the agent is secondary.
function who(addr) {
  const i = addr.indexOf("/");
  if (i < 0) return el("span", { class: "who" }, addr);
  return el("span", { class: "who" }, addr.slice(0, i), el("span", { class: "who-agent" }, addr.slice(i)));
}

function avatar(addr, cls) {
  let h = 0;
  for (const c of addr.split("/")[0]) h = (h * 41 + c.charCodeAt(0)) >>> 0;
  return el("span", { class: "avatar av" + (h % 6) + (cls ? " " + cls : ""), "aria-hidden": "true" },
    addr.charAt(0).toUpperCase());
}

// ---- overview ---------------------------------------------------------------

async function loadOverview() {
  const o = await api("/api/overview");
  state.overview = o;
  if (!state.version) state.version = o.version;
  else if (o.version && o.version !== state.version) updated(o.version);
  $("demo").hidden = !o.demo;
  $("me").textContent = o.me.address;
  $("me").title = "Key " + o.me.fingerprint;
  $("machine").textContent = o.device ? deviceLine(o.device)
    : o.me.responder ? "Your responder: " + o.me.responder + " in " + o.me.responder_dir
      : "No responder: questions and tasks wait for you";
  $("new-btn").hidden = !!o.device; // a browser device starts DMs with people, nothing else
  $("release").hidden = !o.release;
  $("release").textContent = o.release ? "Update recommended: " + o.release + " (see agentnet help update)" : "";
  renderNotify(o.notify);
  renderReview(o.review);
  renderThreads(o.threads);
  renderQuarantine(o.quarantine);
  if (state.pendingOpen) retryOpen();
  return o;
}

// ---- notifications (browser device) -----------------------------------------------------
//
// Off until the person turns them on, here. The service worker shows each
// alert ("AgentNet" / "New activity"); this page never shows one. It tells
// the relay which messages it actually presented, so those do not alert.

const notifyPermission = () => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

function renderNotify(n) {
  const line = $("notify-line");
  line.hidden = !n;
  if (!n) return;
  // Every change redraws the overview: an unchanged line keeps its button,
  // so a click is never lost to a redraw under the pointer.
  const key = JSON.stringify([n, notifyPermission()]);
  if (line.dataset.key === key) return;
  line.dataset.key = key;
  const iphone = !n.native && typeof navigator !== "undefined" && /iPhone|iPad/.test(navigator.userAgent) &&
    !(window.matchMedia && window.matchMedia("(display-mode: standalone)").matches);
  if (!n.available) {
    fill(line, "Notifications: " + (iphone ? "on iPhone, add AgentNet to your Home Screen first, then turn them on there." : n.reason));
  } else if (!n.native && notifyPermission() === "denied") {
    fill(line, "Notifications are blocked in this browser's settings. Everything else works without them.");
  } else if (n.enabled) {
    fill(line, "Notifications on, from " + plural(n.allowed.length, "contact", "contacts") + (n.pending ? " (your server is told when this page reconnects)" : "") + " · ",
      el("button", { type: "button", class: "text-btn", onclick: () => notifyAct("/api/notify/disable") }, "Turn off"));
  } else if (n.pending) {
    fill(line, "Notifications off here; your server is told when this page reconnects · ",
      el("button", { type: "button", class: "text-btn", onclick: () => notifyDialog() }, "Turn on…"));
  } else {
    fill(line, "Notifications off · ", el("button", { type: "button", class: "text-btn", onclick: () => notifyDialog() }, "Turn on…"));
  }
}

async function notifyAct(path, body) {
  try {
    const r = await api(path, body || {});
    if (r.note) announce(r.note);
    await loadOverview();
    if (state.dm) await loadDM();
  } catch (e) {
    announce(e.message);
  }
}

// notifyDialog turns notifications on, only on the person's click: the
// browser's own permission question comes from that click.
function notifyDialog() {
  const native = !!(state.overview.notify && state.overview.notify.native);
  if (native) return alertsDialog();
  dialog({
    title: "Get notifications?",
    body: [el("p", {}, "When someone you started a DM with writes to you, or an agent you invited answers, this device shows \u201cAgentNet: New activity\u201d. It never shows what was written."),
      el("p", {}, "You can mute any DM, and turn this off again here."),
      el("details", { class: "tech" }, el("summary", {}, "Details"),
        el("p", {}, "Your server keeps your notification settings and learns which of your messages belong to the same conversation (not which one), and when you read one here. Your browser's push service learns when a notification is sent to this device, not what it is about."),
        el("p", {}, "Your system may delay or hide notifications (Focus, battery saving, a closed browser on some systems). People who start a DM with you alert only after you allow them.")),
      el("p", { class: "hint" }, "Your browser asks next whether AgentNet may show notifications.")],
    ok: "Turn on",
    run: async () => {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") throw new Error("The browser did not allow notifications. Everything else works without them.");
      const r = await api("/api/notify/enable", {});
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// alertsDialog turns on this computer's own alerts (the daemon shows them;
// the browser is not asked for anything).
function alertsDialog() {
  dialog({
    title: "Get alerts on this computer?",
    body: [el("p", {}, "When someone you started a DM with writes to you, or an agent you invited answers, this computer shows \u201cAgentNet: New activity\u201d while AgentNet runs here, even with this page closed. It never shows what was written."),
      el("p", {}, "You can mute any DM, and turn this off again here."),
      el("details", { class: "tech" }, el("summary", {}, "Details"),
        el("p", {}, "Nothing leaves this computer for this: AgentNet decides from the messages it already holds. Your system may delay or hide alerts (do not disturb, focus modes). Clicking an alert opens its DM on Linux; on macOS and Windows it only shows. People who start a DM with you alert only after you allow them.")),
    ],
    ok: "Turn on",
    run: async () => {
      const r = await api("/api/notify/enable", {});
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// dmNotifyChips are a DM's mute, and whether its person may alert you.
function dmNotifyChips(t) {
  const n = state.overview && state.overview.notify;
  if (!n || !n.enabled || !t.peer.person) return [];
  const muted = n.mutes.includes(t.id);
  const allowed = n.allowed.includes(t.peer.address);
  return [el("button", { type: "button", class: "peer-chip" + (muted ? "" : " on"), onclick: () => notifyAct("/api/notify/mute", { conv: t.id, muted: !muted }),
    title: muted ? "This DM does not notify you" : "New activity in this DM notifies you" }, muted ? "Muted" : "Notifies you"),
  !allowed && el("button", { type: "button", class: "peer-chip", onclick: () => notifyAct("/api/notify/allow", { person: t.peer.person, allowed: true }),
    title: "You did not start a DM with " + t.peer.label + ", so their messages do not notify you" }, "Alerts from " + t.peer.label + " off · Allow")].filter(Boolean);
}

// reportSeen tells the relay which messages of the open DM the person has
// in front of them: the page visible and focused, that DM, its newest
// message in view (Classic). Only then; opening a DM is not enough.
function reportSeen() {
  const t = state.dmData, n = state.overview && state.overview.notify;
  if (!t || !n || !n.enabled || state.lens !== "classic" || document.visibilityState !== "visible" || !document.hasFocus()) return;
  const tl = $("timeline");
  if (tl.scrollHeight - tl.scrollTop - tl.clientHeight > 40) return;
  const ids = t.messages.filter((m) => m.dir === "in").slice(-32).map((m) => m.id);
  if (!ids.length || state.seenReported[t.id] === ids[ids.length - 1]) return;
  state.seenReported[t.id] = ids[ids.length - 1];
  api("/api/notify/seen", { conv: t.id, ids }).catch(() => { delete state.seenReported[t.id]; });
}

// showList shows the conversation list, in every lens (a summary alert,
// or one whose conversation is not here).
function showList(why) {
  if (state.lens === "zoom") Zoom.go(0, {});
  document.body.classList.remove("show-conv");
  if (why) announce(why);
}

// openClicked opens the DM a desktop alert named, if this computer holds
// it; otherwise the list, saying so. The id is only looked up, never used
// any other way.
async function openClicked(conv) {
  if (!(state.overview.dms || []).some((d) => d.id === conv)) {
    showList("The conversation of that alert is not on this computer.");
    return;
  }
  if (state.lens === "zoom") await Zoom.go(2, { dm: conv });
  else await openDM(conv);
}

// openNotified opens the conversation a notification names, as this
// device resolves its channel; if it is not here yet, it waits for the
// stream to catch up, then says so. A summary opens the conversation list.
function openNotified(chan) {
  if (!chan) {
    showList("New activity in more than one conversation.");
    return;
  }
  state.pendingOpen = { chan, until: Date.now() + 15000 };
  retryOpen();
  setTimeout(() => {
    if (state.pendingOpen && state.pendingOpen.chan === chan) {
      state.pendingOpen = null;
      showList("The conversation of that notification is not on this device.");
    }
  }, 15000);
}
window.agentnetOpen = openNotified;

async function retryOpen() {
  const p = state.pendingOpen;
  if (!p) return;
  const r = await api("/api/notify/resolve?chan=" + encodeURIComponent(p.chan)).catch(() => ({}));
  if (!r.conv || state.pendingOpen !== p) return;
  state.pendingOpen = null;
  if (state.lens === "zoom") await Zoom.go(2, { dm: r.conv });
  else await openDM(r.conv);
}

// deviceLine says what a browser device is doing, plainly.
function deviceLine(d) {
  if (d.revoked) return "This device was removed from its server: nothing more is sent or received here.";
  return (d.online ? "Connected to your server" : "Not connected to your server now: what you write waits here") +
    " · This browser runs nothing: questions and tasks wait for you" +
    (d.persisted === false ? " · This browser may clear this device's data" : "");
}

// ---- contacts ------------------------------------------------------------------
//
// One entry per exact address. A contact holds its conversations separately:
// a reply-linked chain is one conversation; a message linked to nothing is
// shown as a single message, never merged into a topic by guesswork. Review
// notices are reports from another machine, kept apart from both.

// contactsOf groups thread summaries by address, newest activity first.
function contactsOf(threads) {
  const by = new Map();
  for (const t of threads) {
    let c = by.get(t.peer);
    if (!c) {
      c = { peer: t.peer, conversations: [], singles: [], reports: [], review: 0, unread: 0, running: 0, notices: 0,
        waiting: false, keyChanged: false, lastAt: t.last_at, last: "" };
      by.set(t.peer, c);
    }
    c.keyChanged ||= t.key_changed;
    if (new Date(t.last_at) > new Date(c.lastAt)) c.lastAt = t.last_at;
    c.notices += t.notices; // open reports, also any that got a reply
    if (t.notice_only) { c.reports.push(t); continue; }
    c.review += t.review; c.unread += t.unread; c.running += t.running; c.waiting ||= t.waiting;
    const open = t.review || t.running || t.waiting;
    (t.count > 1 || open ? c.conversations : c.singles).push(t);
  }
  const newest = (a, b) => new Date(b.last_at) - new Date(a.last_at);
  const list = [...by.values()];
  for (const c of list) {
    c.conversations.sort(newest); c.singles.sort(newest); c.reports.sort(newest);
    const latest = [...c.conversations, ...c.singles].sort(newest)[0];
    c.last = latest ? latest.last : "";
  }
  return list.sort((a, b) => new Date(b.lastAt) - new Date(a.lastAt));
}

// searchKnown finds known agents (by address) and conversations or single
// messages (by their first and latest lines). It never invents people.
function searchKnown(q, threads, dir) {
  q = q.trim().toLowerCase();
  if (!q) return { people: [], dms: [], agents: [], conversations: [], listed: [] };
  const o = state.overview || {};
  // People by the name they give or their device; DMs by their lines or
  // the person's name. Nothing matched is merged or guessed.
  const has = (s) => (s || "").toLowerCase().includes(q);
  const people = (o.people || []).filter((p) => has(p.label) || has(p.address));
  const dms = (o.dms || []).filter((d) => has(d.title) || has(d.last) || has(d.peer.label));
  const agents = contactsOf(threads).filter((c) => c.peer.toLowerCase().includes(q));
  const known = new Set(threads.map((t) => t.peer));
  // Agents the server lists that there is no conversation with yet.
  const listed = ((dir && dir.members) || []).filter((m) => !known.has(m.address) && m.address.toLowerCase().includes(q));
  const conversations = threads.filter((t) => !t.notice_only &&
    (t.title.toLowerCase().includes(q) || t.last.toLowerCase().includes(q)))
    .sort((a, b) => new Date(b.last_at) - new Date(a.last_at));
  return { people, dms, agents, conversations, listed };
}

// ---- directory --------------------------------------------------------------------
//
// Who the server lists as enrolled, pushed by the daemon with presence: only
// for finding someone. Choosing one opens their contact or a new
// conversation to them; nothing is sent, trusted or approved by choosing.

const presenceWord = { connected: "online", reconnecting: "reconnecting", offline: "offline" };
const presenceLine = { connected: "Their computer is connected", reconnecting: "Their computer is reconnecting",
  offline: "Their computer is offline" };
const presenceTitle = "Whether the server sees their AgentNet running now; it does not mean a person is there.";

function directory() {
  return (state.overview && state.overview.directory) || { status: "unknown", members: [] };
}

// memberPresence is the server's word on an address, only while its view
// is current and lists them; otherwise null: nothing is said.
function memberPresence(addr) {
  const d = directory();
  if (!d.current) return null;
  const m = d.members.find((x) => x.address === addr);
  return m && presenceWord[m.presence] ? m.presence : null;
}

function presenceOf(addr) {
  const p = memberPresence(addr);
  return p && presenceWord[p];
}

// peerPresence is what a conversation's header says about the peer's
// computer: the server's pushed view while it is current and lists them;
// "not known now" once that view is not current. Only where the server
// lists no one (an older server) or does not list this peer does it show
// the one check made when the conversation was opened, with its time,
// never as live.
function peerPresence(peer) {
  const d = directory();
  if (d.status === "listed" && !d.current) return "Connection not known now";
  const p = memberPresence(peer);
  if (p) return presenceLine[p];
  const c = state.presence[peer];
  if (!c) return "";
  return c.at ? "Checked " + when(c.at) + ": " + c.text.charAt(0).toLowerCase() + c.text.slice(1) : c.text;
}

function presenceBadge(addr) {
  const p = presenceOf(addr);
  return p && el("span", { class: "presence-dot " + p, title: presenceTitle }, p);
}

// directoryNote says plainly what the list is and what it is not.
function directoryNote(d) {
  if (d.status === "not_listed") return "Your server does not list its members: it runs an older AgentNet, which its operator can update.";
  if (d.status !== "listed" || !d.at) return "Who is on your server is not known yet.";
  const notes = [];
  if (!d.current) notes.push("Your server's current list is not available: this one is as of " + when(d.at) + ", and who is online is not known.");
  if (d.truncated) notes.push("The server lists only the 1,000 most recently joined agents; others are not shown.");
  return notes.join(" ");
}

// chooseMember opens someone the server lists: their contact if there is
// one, otherwise a new conversation to them (nothing is sent until Send).
function chooseMember(addr) {
  clearSearch();
  if (state.overview.threads.some((t) => t.peer === addr)) {
    if (state.lens === "zoom") { Zoom.go(1, { peer: addr, person: null }); return; }
    state.expanded = addr;
    rerenderContacts();
    return;
  }
  newConversationDialog(addr);
}

function memberRow(m) {
  const recent = Date.now() - new Date(m.joined) < 7 * 24 * 3600e3;
  return el("li", {}, el("button", { type: "button", class: "result member", onclick: () => chooseMember(m.address) },
    el("span", { class: "result-kind" }, "Agent"),
    el("span", { class: "result-main" }, who(m.address),
      el("span", { class: "hint" }, recent ? "joined " + when(m.joined) : "no conversation yet")),
    presenceBadge(m.address)));
}

// directorySection lists the agents the server has that there is no
// conversation with yet, newest first, a few at a time.
function directorySection(threads) {
  const d = directory();
  const known = new Set(threads.map((t) => t.peer));
  const others = d.members.filter((m) => !known.has(m.address));
  const note = directoryNote(d);
  if (!others.length && !note) return [];
  const open = !!state.directoryOpen;
  const shown = open ? others : others.slice(0, 5);
  return [
    el("li", { class: "result-head" }, others.length ? "Also on your server (" + others.length + ")" : "On your server"),
    note && el("li", { class: "hint dir-note" }, note),
    ...shown.map(memberRow),
    others.length > shown.length && el("li", {}, el("button", { type: "button", class: "singles-toggle",
      onclick: () => { state.directoryOpen = true; rerenderContacts(); } }, "Show all " + others.length)),
  ];
}

const plural = (n, one, many) => n + " " + (n === 1 ? one : many);

// counts shows a contact's or conversation's numbers side by side; they are
// never added together.
function counts(x) {
  return [
    x.review > 0 && el("span", { class: "badge", title: "Decisions for you here" }, String(x.review),
      el("span", { class: "sr-only" }, x.review === 1 ? " needs your decision" : " need your decision")),
    x.unread > 0 && el("span", { class: "conv-flag unread" }, x.unread + " new"),
    x.notices > 0 && el("span", { class: "conv-flag report", title: "Reports that requests wait on that machine" },
      plural(x.notices, "report", "reports")),
  ];
}

function threadFlag(t) {
  if (t.review) return el("span", { class: "badge" }, String(t.review), el("span", { class: "sr-only" }, " needs your decision"));
  if (t.key_changed) return el("span", { class: "conv-flag danger" }, "Key changed");
  if (t.running) return el("span", { class: "conv-flag calm" }, "Responder working");
  if (t.unread) return el("span", { class: "conv-flag unread" }, t.unread + " new");
  if (t.waiting) return el("span", { class: "conv-flag calm" }, "Awaiting reply");
  return null;
}

// threadRow is one compact line for a conversation or single message.
function threadRow(t, open, single) {
  const current = !!(state.data && state.data.messages.some((m) => m.id === t.id));
  const b = el("button", { type: "button", class: "thread-row" + (single ? " single" : ""), "aria-current": current ? "true" : "false" },
    el("span", { class: "thread-title" }, t.title),
    t.count > 1 && el("span", { class: "thread-count", title: plural(t.count, "message", "messages") }, String(t.count)),
    threadFlag(t),
    el("span", { class: "conv-time" }, when(t.last_at)));
  b.addEventListener("click", () => open(t.id, b));
  return el("li", {}, b);
}

// reportLine groups one sender's review notices: the latest reported text
// and time, never presented as that machine's current queue.
function reportLine(c) {
  // Every report from this sender: open ones (also any that got a reply and
  // so sit in a conversation) and dismissed ones, newest first.
  const open = ((state.overview && state.overview.review) || []).filter((it) => it.notice && it.peer === c.peer)
    .map((it) => ({ id: it.id, at: it.at, text: it.excerpt, open: true }));
  const seen = new Set(open.map((r) => r.id));
  const all = [...open, ...c.reports.filter((t) => !seen.has(t.id)).map((t) => ({ id: t.id, at: t.last_at, text: t.title, open: false }))]
    .sort((a, b) => new Date(b.at) - new Date(a.at));
  if (!all.length) return null;
  const latest = all[0];
  const d = el("details", { class: "tech" }, el("summary", {}, "Details"),
    el("ul", { class: "report-items" }, all.map((r) => el("li", {},
      el("time", { datetime: r.at }, when(r.at)), " · ", r.open ? "not dismissed" : "dismissed", " · ",
      el("span", { class: "hint" }, r.text)))));
  return el("div", { class: "report-line" + (open.length ? "" : " seen") },
    el("p", {}, el("strong", {}, c.peer), " reported requests waiting for a person on that machine."),
    el("p", { class: "hint" }, "Latest report " + when(latest.at) + ": \u201c" + firstSentence(latest.text) + "\u201d. Decide there; nothing here can approve them."),
    el("div", { class: "report-actions" },
      open.length ? el("button", { type: "button", class: "chip", onclick: (e) => dismissReports(c.peer, e.currentTarget),
        title: "Clears these reports on this computer only; the requests still wait on " + c.peer }, "Dismiss " + plural(open.length, "report", "reports"))
        : el("span", { class: "hint" }, "Dismissed"),
      d));
}

const firstSentence = (s) => (s || "").split(/\.\s/)[0].replace(/\.$/, "");

function openReports(peer) {
  return ((state.overview && state.overview.review) || []).filter((it) => it.notice && it.peer === peer).map((it) => it.id);
}

// dismissReports clears one sender's open reports here, one existing
// resolve per report. Nothing is sent and nothing is approved.
async function dismissReports(peer, button) {
  if (button) { if (button.disabled) return; button.disabled = true; }
  const ids = openReports(peer);
  let failed = 0;
  for (const id of ids) {
    try { await act({ do: "resolve", id }); } catch (e) { failed++; }
  }
  announce(failed ? failed + " report(s) could not be dismissed." : "Reports from " + peer + " dismissed on this computer.");
}

// contactBody is what a contact opens into, in the sidebar and in Zoom:
// its reports, its conversations, and its single messages folded away.
function contactBody(c, open) {
  const singlesOpen = !!state.singlesOpen[c.peer];
  const unreadSingles = c.singles.reduce((n, t) => n + t.unread, 0);
  return [
    reportLine(c),
    c.conversations.length > 0 && el("ul", { class: "thread-list", "aria-label": "Conversations with " + c.peer },
      c.conversations.map((t) => threadRow(t, open, false))),
    c.singles.length > 0 && el("button", { type: "button", class: "singles-toggle", "aria-expanded": String(singlesOpen),
      onclick: () => { state.singlesOpen[c.peer] = !singlesOpen; rerenderContacts(); } },
      (singlesOpen ? "Hide " : "") + plural(c.singles.length, "single message", "single messages") +
      (unreadSingles && !singlesOpen ? " · " + unreadSingles + " new" : "") + (singlesOpen ? "" : " (not linked to a conversation)")),
    singlesOpen && el("ul", { class: "thread-list singles", "aria-label": "Single messages from " + c.peer }, c.singles.map((t) => threadRow(t, open, true))),
    !c.conversations.length && !c.singles.length && !c.reports.length && el("p", { class: "hint" }, "No messages yet."),
    el("button", { type: "button", class: "text-btn new-conv", onclick: () => newConversationDialog(c.peer) }, "New conversation with " + c.peer),
  ];
}

// ---- people and DMs ------------------------------------------------------------------
//
// A person is a human as their own AgentNet presents them, set up only when
// they choose; the name is their claim. Each DM is its own conversation, also
// with the same person, and stays apart from device history (the contacts
// below, one installation each). Nothing is merged by name or address.

const personStateText = {
  self: "your person",
  pinned: "checked against their computer's key",
  conflict: "frozen: they published a different record",
  listed: "not checked yet (checked when you start a DM)",
};

const personKey = (p) => p.person || "listed:" + p.address;

function peopleSection() {
  const o = state.overview;
  if (!o || !o.persons) return [];
  const head = el("li", { class: "result-head" }, "People");
  if (!o.person) {
    return [head, el("li", { class: "person-setup" },
      el("p", {}, "You have no person yet. A person is you, the human, as others see you in DMs."),
      el("button", { type: "button", class: "chip", onclick: () => personDialog() }, "Set up your person…"))];
  }
  const dms = o.dms || [];
  const people = [...(o.people || [])].sort((a, b) => {
    const la = dms.find((d) => d.peer.person && d.peer.person === a.person), lb = dms.find((d) => d.peer.person && d.peer.person === b.person);
    if (la || lb) return (lb ? new Date(lb.last_at) : 0) - (la ? new Date(la.last_at) : 0);
    return a.label.localeCompare(b.label);
  });
  return [head,
    el("li", { class: "hint person-me" }, "You: ", el("strong", {}, o.person.label), " · ",
      o.person.published ? "others can start a DM with you" : "not on your server yet"),
    ...people.map((p) => personRow(p, dms.filter((d) => d.peer.person && d.peer.person === p.person))),
    !people.length && el("li", { class: "hint empty-list" }, "No one else on your server has set up a person yet.")];
}

function personRow(p, dms) {
  const key = personKey(p);
  const open = !!state.personOpen[key];
  const unread = dms.reduce((n, d) => n + d.unread, 0);
  const held = dms.reduce((n, d) => n + d.held, 0);
  const last = dms[0];
  const head = el("button", { type: "button", class: "conv-item contact", "aria-expanded": String(open),
    onclick: () => { state.personOpen[key] = !open; rerenderContacts(); } },
    avatar(p.label || p.address),
    el("span", { class: "conv-main" },
      el("span", { class: "conv-top" }, el("span", { class: "conv-name person-name" }, p.label),
        el("span", { class: "kind-tag" }, "Person"), last && el("span", { class: "conv-time" }, when(last.last_at))),
      el("span", { class: "conv-bottom" },
        el("span", { class: "conv-last" }, last ? last.last || "No messages yet" : "No DM yet"),
        p.state === "conflict" && el("span", { class: "conv-flag danger" }, "Frozen"),
        held > 0 && el("span", { class: "conv-flag calm" }, held + " held"),
        unread > 0 && el("span", { class: "conv-flag unread" }, unread + " new")),
      el("span", { class: "conv-sub" }, "via " + p.address + " · " + (personStateText[p.state] || p.state) +
        (dms.length ? " · " + plural(dms.length, "DM", "DMs") : "")),
      (p.agents || []).map((a) => el("span", { class: "conv-sub agent-link" }, agentLinkText(a, p)))));
  return el("li", { class: "contact-item" + (open ? " open" : "") }, head,
    open && el("div", { class: "contact-body" },
      dms.length > 0 && el("ul", { class: "thread-list", "aria-label": "DMs with " + p.label }, dms.map((d) => dmRow(d, (id) => openDM(id)))),
      p.state === "conflict"
        ? el("p", { class: "hint" }, "Frozen: no new DM can start with this record.")
        : el("button", { type: "button", class: "text-btn new-conv", onclick: () => newDMDialog(p) }, "New DM with " + p.label)));
}

// dmFlags shows a DM's numbers side by side.
function dmFlags(d) {
  return [d.waiting > 0 && el("span", { class: "conv-flag calm" }, d.waiting + " kept"),
    d.held > 0 && el("span", { class: "conv-flag calm" }, d.held + " held"),
    d.unread > 0 && el("span", { class: "conv-flag unread" }, d.unread + " new")];
}

// dmRow is one DM in a person's list (sidebar and Zoom).
function dmRow(d, open) {
  const b = el("button", { type: "button", class: "thread-row", "aria-current": String(state.dm === d.id) },
    el("span", { class: "thread-title" }, d.title || "No messages yet"),
    d.count > 1 && el("span", { class: "thread-count", title: plural(d.count, "message", "messages") }, String(d.count)),
    dmFlags(d),
    el("span", { class: "conv-time", title: "Started " + new Date(d.created).toLocaleString() + (d.mine ? " by you" : " by them") }, when(d.last_at)));
  b.addEventListener("click", () => open(d.id, b));
  return el("li", {}, b);
}

// choosePerson opens a person found by search: their DMs, never a guess.
function choosePerson(p) {
  clearSearch();
  state.personOpen[personKey(p)] = true;
  if (state.lens === "zoom") { Zoom.go(1, { person: personKey(p), peer: null }); return; }
  rerenderContacts();
}

// dmAuthor names who wrote a DM message, as the sending AgentNet says.
function dmAuthor(m, d) {
  if (m.dir === "out") return "You";
  if ((m.origin || "").startsWith("agent:")) return "An agent on " + m.from + ", as their AgentNet says";
  return d.peer.label;
}

// personDialog sets up this installation's person, only when asked.
function personDialog() {
  const name = el("input", { id: "person-name", type: "text", autocomplete: "off", maxlength: "64" });
  dialog({
    title: "Set up your person",
    body: [el("p", {}, "A person is you, the human, as others see you in DMs. You set it up once, on this computer; nothing sets it up for you."),
      el("p", {}, "The name is what you call yourself: others see it as your claim, not a checked identity. If you already set up your person on another computer, do not set up a second one here: linking computers comes later."),
      el("label", { for: "person-name", class: "field-label" }, "Your name"), name],
    ok: "Set up",
    focus: name,
    run: async () => {
      const r = await api("/api/person", { label: name.value });
      announce(r.note);
      await loadOverview();
    },
  });
  state.dialogRestore = { type: "person" };
}

// newDMDialog starts a separate DM with a person; nothing is sent yet.
function newDMDialog(p) {
  dialog({
    title: "New DM with " + p.label,
    body: [el("p", {}, "A new conversation with ", el("strong", {}, p.label), " (the name they give) through " + p.address +
      ". It is separate from your other DMs with them."),
      p.state === "listed" && el("p", {}, "Their person record is checked against their computer's key now and kept here. If they later publish a different one, your DMs with them freeze."),
      el("p", { class: "hint" }, "Nothing is sent until you write.")],
    ok: "Start DM",
    run: async () => {
      const r = await api("/api/dm/new", { address: p.address });
      state.personOpen[p.person || personKey(p)] = true;
      await loadOverview();
      if (state.lens === "zoom") await Zoom.go(2, { dm: r.id });
      else await openDM(r.id);
    },
  });
}

// ---- a DM ---------------------------------------------------------------------------

function beginDM(id) {
  const changed = state.dm !== id;
  if (changed) {
    keepDraft();
    state.thread = null;
    state.data = null;
    state.dmData = null;
    state.draftKey = null;
    $("body").value = "";
    grow();
    setKind("message");
    setAnswering(null);
    setDMReply(null);
    setDMAgent(null);
  }
  state.dm = id;
  return changed;
}

async function openDM(id) {
  const changed = beginDM(id);
  document.body.classList.add("show-conv");
  await loadDM(true);
  if (state.dmData) state.personOpen[personKey(state.dmData.peer)] = true;
  await loadOverview();
  if (changed) api("/api/refresh", { id }).catch(() => {}); // once per open: receipts the server still holds
}

async function loadDM(scrollToEnd) {
  const id = state.dm;
  if (!id) return;
  let t;
  try {
    t = await api("/api/dm?id=" + encodeURIComponent(id));
  } catch (e) {
    announce(e.message);
    return;
  }
  if (state.dm !== id) return; // another conversation was opened meanwhile
  // A participation record is shown as the sentence it stands for, in every view.
  t.messages = t.messages.map((m) => (m.event ? Object.assign({}, m, { body: m.event }) : m));
  t.agents = t.agents || [];
  state.dmData = t;
  fill($("conv-name"), t.peer.label);
  $("conv-topic").textContent = "DM with a person · started " + when(t.created) + (t.mine ? " by you" : " by them");
  $("conv-avatar").replaceWith(Object.assign(avatar(t.peer.label || t.peer.address), { id: "conv-avatar" }));
  const online = presenceOf(t.peer.address); // the server's pushed view, only while current
  $("conv-presence").textContent = "The name they give · via " + t.peer.address + " · " + (personStateText[t.peer.state] || t.peer.state) +
    (online ? " · their computer is " + online : "");
  fill($("peer-chips"), ...dmNotifyChips(t));
  const n = $("notice");
  n.hidden = !t.frozen;
  fill(n, t.frozen && el("p", {}, t.frozen));
  $("composer").hidden = false;
  renderAgents(t);
  renderDMBody(scrollToEnd);
  const key = "dm:" + id;
  if (state.draftKey === null) restoreDraft(key, t); // just switched here (beginDM)
  else if (state.dmReply && !t.messages.some((m) => m.id === state.dmReply.id)) setDMReply(null);
  if (state.dmAgent && !(agentOf(state.dmAgent) || {}).can_ask) setDMAgent(null); // dismissed meanwhile
  state.draftKey = key;
  syncComposer();
  const unread = t.messages.filter((m) => m.unread).map((m) => m.id);
  if (unread.length) api("/api/act", { do: "read", ids: unread }).catch(() => {});
}

// renderDMBody draws the open DM, as a chat or as a comic; Zoom draws
// itself (lenses.js).
function renderDMBody(scrollToEnd) {
  const t = state.dmData;
  if (!t || state.lens === "zoom") return;
  if (state.lens === "comic") { Comic.render(comicDM(t)); return; }
  const tl = $("timeline");
  const atEnd = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 60;
  fill(tl, t.messages.length ? t.messages.map((m, i) => dmMsg(m, t, t.messages[i - 1]))
    : el("li", { class: "hint empty-list" }, "No messages yet. What you write here goes to " + t.peer.label + " only."));
  if (scrollToEnd || atEnd) tl.scrollTop = tl.scrollHeight;
  reportSeen();
}

// dmMsg is one DM message. Who wrote it is what the sending AgentNet says,
// shown as that: a person's name is their claim, an agent is marked.
function dmMsg(m, t, prev) {
  if (m.event) {
    return el("li", { id: "m-" + m.id, class: "event-line" }, el("span", {}, m.event), el("time", { datetime: m.at }, when(m.at)));
  }
  const mine = m.dir === "out";
  const agent = (m.origin || "").startsWith("agent:");
  const author = dmAuthor(m, t);
  const to = m.to && (agentOf(m.pid) ? agentName(agentOf(m.pid)) : "an agent on " + m.to);
  const sharedWith = t.agents.filter((a) => (a.state === "invited" || a.state === "active") && a.shared.includes(m.id));
  const cont = prev && !prev.event && prev.dir === m.dir && prev.origin === m.origin && !kindTag[m.kind] && !kindTag[prev.kind] &&
    !to && !prev.to && new Date(m.at) - new Date(prev.at) < 10 * 60e3;
  const held = m.state === "conv_held";
  const acts = m.actions || []; // a request to your agent: yours to decide on
  const meta = !cont && el("div", { class: "meta" }, el("span", { class: "who" }, author),
    agent && el("span", { class: "tag" }, "Agent"),
    to && el("span", { class: "tag" }, "To " + to.charAt(0).toLowerCase() + to.slice(1)),
    kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]),
    m.unread && el("span", { class: "tag unread" }, "New"),
    el("time", { datetime: m.at }, when(m.at)));
  const parent = m.reply_to && t.messages.find((x) => x.id === m.reply_to);
  return el("li", { id: "m-" + m.id, class: "msg " + m.dir + (cont ? " cont" : "") + (held || acts.length ? " needs" : "") },
    !mine && (cont ? el("span", { class: "avatar sm", "aria-hidden": "true" }) : avatar(t.peer.label || m.from, "sm")),
    el("div", { class: "col" }, meta,
      el("div", { class: "bubble", tabindex: "-1" },
        m.reply_to && (parent ? el("span", { class: "replyref" }, "Reply to: " + firstLine(parent.body, 90))
          : el("span", { class: "replyref" }, "Reply to a message not shown here")),
        el("p", { class: "body" }, m.body)),
      held && el("div", { class: "decide" }, el("p", { class: "decide-why" }, m.state_text)),
      acts.length > 0 && el("div", { class: "decide" }, el("p", { class: "decide-why" }, m.state_text),
        m.job_detail && el("p", { class: "hint" }, m.job_detail),
        el("div", { class: "acts" }, acts.map((a, i) => actionButton(a, m, t, i === 0)))),
      sharedWith.length > 0 && el("p", { class: "shared-note" }, "Shared with " + sharedWith.map((a) => agentName(a).replace(/^Your/, "your")).join(" and ")),
      el("div", { class: "foot" }, !held && !acts.length && m.state_text && el("span", {}, m.state_text),
        !t.frozen && el("button", { type: "button", class: "text-btn", onclick: () => { setDMReply(m); $("body").focus(); } }, "Reply"),
        dmDetails(m))));
}

function dmDetails(m) {
  const by = m.dir === "out" ? "This installation" : m.from;
  const origin = m.origin === "ui" ? by + " says a person wrote it. That is its claim, not proof."
    : (m.origin || "").startsWith("agent:") ? by + " says an agent (" + m.origin.slice(6) + ") wrote it. That is its claim."
      : by + " did not say who wrote it.";
  return el("details", { class: "tech" }, el("summary", {}, "Details"),
    el("dl", {},
      el("dt", {}, "Written by"), el("dd", {}, origin),
      el("dt", {}, "Sent"), el("dd", {}, new Date(m.at).toLocaleString()),
      el("dt", {}, "Device"), el("dd", {}, m.from),
      el("dt", {}, "Message id"), el("dd", { class: "mono" }, m.id),
      el("dt", {}, "Kind"), el("dd", {}, m.kind),
      m.state && [el("dt", {}, "Stored state"), el("dd", { class: "mono" }, m.state)],
      m.replica && [el("dt", {}, "Copy"), el("dd", {}, "A copy kept for history: nothing runs it")]));
}

// agentLinkText says which agent a person's device runs, and where it is
// in DMs here. The link is the invitation's host: that person and device.
function agentLinkText(a, p) {
  const active = a.dms.filter((d) => d.state === "active").length;
  return (isMe(p) ? "Your agent" : "Their agent") + " on " + a.address + " · " + plural(a.dms.length, "DM", "DMs") +
    (active ? " (" + active + " active)" : "");
}

// ---- agents in a DM ------------------------------------------------------------------

const agentOf = (pid) => state.dmData && (state.dmData.agents || []).find((a) => a.pid === pid);
// agentName names an agent by the person whose installation runs it.
const agentName = (a) => (a.host_here ? "Your agent" : a.host.label + "'s agent");
const isMe = (p) => !!p && p.state === "self";
// taskFree says whether your tasks run without its owner accepting each one
// (the invitation names your key). Anyone in the DM may still give it a
// task: the owner then accepts it, unless they already allow your key.
const taskFree = (a) => a.host_here || a.tasks_from.some(isMe);

// renderAgents shows the agents invited into the open DM: whose each is,
// what it may be shown, who may give it tasks, and what you can do now.
function renderAgents(t) {
  const box = $("agents");
  const canInvite = !!(state.overview && state.overview.agents && state.overview.person) && !t.frozen;
  box.hidden = !t.agents.length && !canInvite;
  const ended = (a) => a.state === "dismissed" || a.state === "declined";
  const past = t.agents.filter(ended);
  fill(box, t.agents.filter((a) => !ended(a)).map((a) => agentCard(a, t)),
    past.length > 0 && el("details", { class: "agents-past" }, el("summary", {}, plural(past.length, "earlier agent", "earlier agents")),
      past.map((a) => agentCard(a, t))),
    canInvite && el("button", { type: "button", class: "text-btn", onclick: () => inviteDialog(t) }, "Invite an agent…"));
}

function agentCard(a, t) {
  const shown = a.shared.length + a.missing;
  const facts = ["invited by " + (isMe(a.inviter) ? "you" : a.inviter.label),
    shown ? "shown " + plural(shown, "earlier message", "earlier messages") + (a.missing ? " (" + a.missing + " not here)" : "")
      : "shown no earlier messages",
    a.tasks_from.length ? "tasks without asking from " + a.tasks_from.map((p) => (isMe(p) ? "you" : p.label)).join(" and ")
      : "tasks wait for " + (a.host_here ? "you" : a.host.label) + " to accept them"];
  return el("div", { class: "agent-card " + a.state },
    el("div", { class: "agent-head" }, el("span", { class: "tag" }, "Agent"), el("strong", {}, agentName(a)),
      el("span", { class: "hint" }, "on " + a.host.address)),
    el("p", { class: "agent-state" }, a.state_text),
    el("p", { class: "hint" }, facts.join(" · ")),
    a.note && el("p", { class: "agent-note" }, "Note: " + a.note),
    (a.can_decide || a.can_ask || a.can_dismiss) && el("div", { class: "agent-actions" },
      a.can_decide && el("button", { type: "button", class: "btn primary", onclick: () => decideDialog(a, t, true) }, "Accept…"),
      a.can_decide && el("button", { type: "button", class: "btn", onclick: () => decideDialog(a, t, false) }, "Decline…"),
      a.can_ask && el("button", { type: "button", class: "btn", onclick: () => { setDMAgent(a); $("body").focus(); } }, "Ask"),
      a.can_dismiss && el("button", { type: "button", class: "text-btn", onclick: () => dismissDialog(a) }, "Dismiss…")));
}

// choice is one labelled radio or checkbox of a dialog.
function choice(type, name, value, label) {
  const input = el("input", { type, name, id: name + ":" + value });
  input.value = value;
  return { input, row: el("label", { class: "choice" }, input, el("span", {}, label)) };
}

// inviteDialog invites the agent on one member's computer. Nothing is
// chosen for the person: whose agent, what it may be shown and who may
// give it tasks are all picked here, and both people see them.
function inviteDialog(t) {
  const me = state.overview.person;
  // A browser device runs no agent: only the other person's can be invited from it.
  const browser = !!(state.overview.me && state.overview.me.browser);
  const hosts = [!browser && choice("radio", "agent-host", me.address, "Yours, on " + me.address),
    choice("radio", "agent-host", t.peer.address, t.peer.label + "'s, on " + t.peer.address)].filter(Boolean);
  const share = t.messages.filter((m) => !m.event).slice(-30)
    .map((m) => choice("checkbox", "agent-share", m.id, dmAuthor(m, t) + ": " + firstLine(m.body, 70)));
  const tasks = [[me, "You"], [t.peer, t.peer.label]].filter(([p]) => p.fingerprint)
    .map(([p, label]) => choice("checkbox", "agent-tasks", p.fingerprint, label));
  const note = el("input", { id: "agent-note", type: "text", maxlength: "200", autocomplete: "off" });
  const picked = (cs) => cs.filter((c) => c.input.checked).map((c) => c.input.value);
  dialog({
    title: "Invite an agent into this DM",
    body: [el("p", {}, "It joins only if its owner accepts, on their computer. You both see the invitation, what it may be shown and who may give it tasks."),
      el("fieldset", { class: "choices" }, el("legend", {}, "Whose agent"), hosts.map((c) => c.row)),
      el("fieldset", { class: "choices" }, el("legend", {}, "Earlier messages it may be shown"),
        share.length ? share.map((c) => c.row) : el("p", { class: "hint" }, "No messages yet."),
        el("p", { class: "hint" }, "None unless you choose. Nothing else earlier is given.")),
      el("fieldset", { class: "choices" }, el("legend", {}, "Tasks without asking its owner each time"), tasks.map((c) => c.row),
        el("p", { class: "hint" }, "Either of you can ask it questions or give it tasks. A task from someone not chosen here waits for its owner to accept it.")),
      el("label", { for: "agent-note", class: "field-label" }, "A note for its owner (optional)"), note],
    ok: "Invite",
    run: async () => {
      const host = picked(hosts)[0];
      if (!host) throw new Error("Choose whose agent to invite.");
      await api("/api/dm/agent/invite", { conv: t.id, host, share: picked(share), tasks_from: picked(tasks), note: note.value });
      announce("Invited. Its owner accepts or declines it on their computer.");
      await loadDM();
    },
  });
}

// decideDialog is the host's explicit answer, showing exactly what an
// accept agrees to.
function decideDialog(a, t, accept) {
  const shared = t.messages.filter((m) => a.shared.includes(m.id));
  const who = isMe(a.inviter) ? "You" : a.inviter.label;
  dialog({
    title: accept ? "Let your agent join this DM?" : "Decline the invitation?",
    body: accept ? [el("p", {}, who + " invited your agent. If you accept, it answers what either of you asks it here, on this computer with the responder you chose."),
      !(state.overview.me && state.overview.me.responder) && el("p", { class: "hint" }, "No responder is chosen on this computer yet: what is asked of it waits until you choose one."),
      el("p", {}, shared.length ? "It may be shown these earlier messages:" : "It is shown no earlier messages."),
      shared.length > 0 && el("ul", { class: "quote-list" }, shared.map((m) => el("li", {}, dmAuthor(m, t) + ": " + firstLine(m.body, 90)))),
      a.missing > 0 && el("p", { class: "hint" }, plural(a.missing, "chosen message is", "chosen messages are") + " not on this computer and cannot be shown."),
      el("p", {}, a.tasks_from.length ? "Tasks run without asking you when they come from: " + a.tasks_from.map((p) => (isMe(p) ? "you" : p.label)).join(" and ") + "."
        : "Every task waits for you to accept it."),
      a.note && el("p", {}, "Their note: " + a.note),
      el("p", { class: "hint" }, "You accept exactly this, all or nothing. Either of you can dismiss it later.")]
      : [el("p", {}, "Your agent does not join. If you change your mind, they can invite it again.")],
    ok: accept ? "Accept" : "Decline",
    run: async () => {
      await api("/api/dm/agent/decide", { pid: a.pid, accept });
      announce(accept ? "Your agent joined this DM." : "Declined.");
      await loadDM();
    },
  });
}

function dismissDialog(a) {
  dialog({
    title: "Dismiss " + agentName(a).replace(/^Your/, "your") + "?",
    body: [el("p", {}, "It gets nothing more from this DM and nothing more can be asked of it. What was already said stays. To have it again, invite it again."),
      el("p", { class: "hint" }, a.host_here ? "If it is running something now, that stops, and its reply is kept here."
        : "Something it already started on " + a.host.address + " may still finish, and its reply arrive, before the dismissal reaches that computer.")],
    ok: "Dismiss",
    run: async () => {
      await api("/api/dm/agent/dismiss", { pid: a.pid });
      if (state.dmAgent === a.pid) setDMAgent(null);
      announce("Dismissed.");
      await loadDM();
    },
  });
}

// setDMAgent makes the composer ask an agent of the open DM, or write to
// the person again (null). It is part of that DM's draft and never crosses
// to another.
function setDMAgent(a) {
  state.dmAgent = a ? a.pid : null;
  if (a) {
    state.dmReply = null;
    $("replying").hidden = false;
    $("replying-label").textContent = "Asking";
    $("replying-text").textContent = agentName(a) + " (on " + a.host.address + ")";
    setKind("question");
  } else if (!state.dmReply && !state.answering) {
    $("replying").hidden = true;
  }
  if (state.dm) syncComposer();
}

async function sendDM() {
  const t = state.dmData;
  if (state.sending || !t || t.frozen) return;
  const key = state.draftKey, text = $("body").value, reply = state.dmReply, agent = state.dmAgent;
  state.sending = true;
  syncComposer();
  $("compose-error").textContent = "";
  try {
    const r = agent ? await api("/api/dm/agent/ask", { pid: agent, kind: kindValue() === "task" ? "task" : "question", body: text })
      : await api("/api/dm/send", { conv: t.id, body: text, reply_to: reply ? reply.id : "" });
    announce(r.state === "waiting" ? "Kept here, not sent yet: " + (r.detail || "they cannot read conversations now.")
      : r.state === "queued" ? "Queued: it goes out when the server is reachable." : "Sent.");
    if (state.draftKey === key) {
      if ($("body").value === text) { $("body").value = ""; grow(); }
      if (state.dmReply === reply) setDMReply(null);
    } else if (state.drafts[key] && state.drafts[key].text === text) {
      delete state.drafts[key];
    }
  } catch (e) {
    if (state.draftKey === key) $("compose-error").textContent = e.message;
    else announce("Not sent to " + t.peer.label + ": " + e.message + " Your text is kept in that DM.");
  } finally {
    state.sending = false;
    syncComposer();
    if (state.newVersion) updated(state.newVersion);
  }
}

let rerenderContacts = () => {};
function rerender() {
  renderThreads(state.overview.threads);
  if (state.lens === "zoom") Zoom.refresh();
}

function renderReview(items) {
  const decisions = items.filter((it) => !it.notice);
  const reports = items.filter((it) => it.notice);
  const btn = $("review-btn");
  const n = decisions.length;
  $("review-count").textContent = n;
  $("review-word").textContent = n ? "Needs you" : "Nothing needs you";
  $("review-reports").hidden = !reports.length;
  $("review-reports").textContent = plural(reports.length, "report", "reports");
  btn.dataset.n = n;
  btn.setAttribute("aria-label", (n === 1 ? "1 item needs your decision" : n + " items need your decision") +
    (reports.length ? ", " + plural(reports.length, "report", "reports") + " from other machines" : ""));
  fill($("review-list"), ...(n ? decisions.map((it) => el("li", {},
    el("button", { type: "button", onclick: () => { toggleReview(false); openThread(it.id, it.id); } },
      el("span", {}, who(it.peer), " · ", kindTag[it.kind] || it.kind),
      el("span", { class: "review-why" }, it.why),
      el("span", { class: "review-text" }, it.excerpt)))) : [el("li", { class: "hint" }, "Nothing here waits for your decision.")]));
  const senders = [...new Set(reports.map((it) => it.peer))];
  $("reports").hidden = !senders.length;
  fill($("report-list"), ...senders.map((peer) => {
    const c = contactsOf(state.overview.threads).find((x) => x.peer === peer);
    return el("li", {}, c ? reportLine(c) : null);
  }));
}

function toggleReview(open) {
  const btn = $("review-btn");
  const show = open ?? btn.getAttribute("aria-expanded") !== "true";
  btn.setAttribute("aria-expanded", show);
  $("review").hidden = !show;
  if (show) $("review").querySelector("button")?.focus();
}

// renderThreads draws the sidebar: contacts, or search results while a
// search is typed.
function renderThreads(threads) {
  rerenderContacts = rerender;
  const list = $("conv-list");
  if (state.query.trim()) return renderSearch(threads);
  $("list-title").textContent = "Contacts";
  const people = peopleSection();
  const devices = people.length > 0 && el("li", { class: "result-head" }, "Devices: messages per installation");
  if (!threads.length) {
    fill(list, people, devices, el("li", { class: "hint empty-list" }, state.overview.device
      ? "No messages from devices here. Start a DM with a person above."
      : "No conversations yet. Start one with the + button, or with someone below."),
      directorySection(threads));
    return;
  }
  fill(list, people, devices, ...contactsOf(threads).map((c) => {
    const expanded = state.expanded === c.peer;
    const head = el("button", { type: "button", class: "conv-item contact", "aria-expanded": String(expanded),
      onclick: () => { state.expanded = expanded ? null : c.peer; rerenderContacts(); } },
      avatar(c.peer),
      el("span", { class: "conv-main" },
        el("span", { class: "conv-top" }, el("span", { class: "conv-name" }, who(c.peer)), presenceBadge(c.peer), el("span", { class: "conv-time" }, when(c.lastAt))),
        el("span", { class: "conv-bottom" },
          el("span", { class: "conv-last" }, c.last || (c.reports.length ? "Reports only" : "")),
          c.keyChanged && el("span", { class: "conv-flag danger" }, "Key changed"), counts(c)),
        el("span", { class: "conv-sub" }, [plural(c.conversations.length, "conversation", "conversations"),
          c.singles.length && plural(c.singles.length, "single message", "single messages")].filter(Boolean).join(" · "))));
    return el("li", { class: "contact-item" + (expanded ? " open" : "") }, head,
      expanded && el("div", { class: "contact-body" }, contactBody(c, (id) => openThread(id))));
  }), directorySection(threads));
}

// searchItems lists what a search finds; each result opens through the
// view's own handler (the sidebar, or Zoom), so it lands where it is.
function searchItems(q, threads, open) {
  const { people, dms, agents, conversations, listed } = searchKnown(q, threads, directory());
  const shown = conversations.slice(0, 30);
  const kind = (t) => t.count > 1 ? "Conversation" : "Message";
  if (!people.length && !dms.length && !agents.length && !conversations.length && !listed.length) {
    return [el("li", { class: "hint empty-list" }, "No person, agent or conversation matches."),
      directoryNote(directory()) && el("li", { class: "hint dir-note" }, directoryNote(directory()))];
  }
  return [
    people.length > 0 && el("li", { class: "result-head" }, plural(people.length, "person", "people")),
    ...people.map((p) => el("li", {}, el("button", { type: "button", class: "result", onclick: () => open.person(p) },
      el("span", { class: "result-kind" }, "Person"),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, p.label),
        el("span", { class: "hint" }, "via " + p.address + " · " + (personStateText[p.state] || p.state)))))),
    dms.length > 0 && el("li", { class: "result-head" }, plural(dms.length, "DM", "DMs")),
    ...dms.map((d) => el("li", {}, el("button", { type: "button", class: "result", onclick: () => open.dm(d) },
      el("span", { class: "result-kind" }, "DM"),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, d.title || "No messages yet"),
        el("span", { class: "hint" }, "with " + d.peer.label + " · " + when(d.last_at))), dmFlags(d)))),
    agents.length + listed.length > 0 && el("li", { class: "result-head" }, plural(agents.length + listed.length, "agent", "agents")),
    ...agents.map((c) => el("li", {}, el("button", { type: "button", class: "result", onclick: () => open.contact(c) },
      el("span", { class: "result-kind" }, "Agent"), el("span", { class: "result-main" }, who(c.peer),
        el("span", { class: "hint" }, " · " + plural(c.conversations.length, "conversation", "conversations"))), presenceBadge(c.peer), counts(c)))),
    ...listed.map(memberRow),
    conversations.length > 0 && el("li", { class: "result-head" }, plural(conversations.length, "conversation or message", "conversations or messages")),
    ...shown.map((t) => el("li", {}, el("button", { type: "button", class: "result", onclick: () => open.conversation(t) },
      el("span", { class: "result-kind" }, kind(t)),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, t.title),
        el("span", { class: "hint" }, "with ", t.peer, " · ", when(t.last_at))), threadFlag(t)))),
    conversations.length > shown.length && el("li", { class: "hint result-more" }, (conversations.length - shown.length) + " more: type more to narrow the search.")];
}

function renderSearch(threads) {
  $("list-title").textContent = "Search results";
  fill($("conv-list"), ...searchItems(state.query, threads, {
    person: (p) => choosePerson(p),
    dm: (d) => { clearSearch(); if (state.lens === "zoom") Zoom.go(2, { dm: d.id }); else openDM(d.id); },
    contact: (c) => { state.expanded = c.peer; clearSearch(); },
    conversation: (t) => { state.expanded = t.peer; clearSearch(); openThread(t.id); },
  }));
}

function clearSearch() {
  state.query = "";
  $("search").value = "";
  rerenderContacts();
}

function renderQuarantine(items) {
  const box = $("quarantine");
  box.hidden = !items.length;
  if (!items.length) return;
  $("quarantine-summary").textContent = items.length === 1 ? "1 message held back" : items.length + " messages held back";
  fill($("quarantine-list"), ...items.map((q) => el("li", {},
    el("span", {}, who(q.peer), " · ", when(q.at)), el("span", { class: "hint" }, q.reason))));
}

// ---- thread -----------------------------------------------------------------

// beginThread starts showing the thread holding message id, from any view.
// The composer's draft stays with the conversation it was written in, and
// nothing can be sent until the new one has loaded. It reports whether the
// conversation changed.
function beginThread(id) {
  const changed = !!state.dm || !state.data || !state.data.messages.some((m) => m.id === id);
  if (changed) {
    keepDraft();
    state.data = null;
    state.draftKey = null;
    $("body").value = "";
    grow();
    setKind("message");
    setAnswering(null);
    setDMReply(null);
    setDMAgent(null);
  }
  state.dm = null;
  state.dmData = null;
  state.thread = id;
  $("agents").hidden = true;
  return changed;
}

async function openThread(id, focusId) {
  const changed = beginThread(id);
  document.body.classList.add("show-conv");
  await loadThread(changed);
  if (state.data) { // the sidebar opens at this conversation's contact, with the item in view
    state.expanded = state.data.peer;
    const s = state.overview && state.overview.threads.find((x) => x.id === state.data.messages[0].id);
    if (s && s.count === 1 && !(s.review || s.running || s.waiting)) state.singlesOpen[s.peer] = true;
  }
  await loadOverview();
  if (focusId) flash(focusId);
  if (changed) refreshThread();
}

function flash(id) {
  if (state.lens === "comic") { Comic.focus(id); return; }
  const m = document.getElementById("m-" + id);
  if (!m) return;
  m.scrollIntoView({ block: "center" });
  m.classList.add("flash");
  (m.querySelector(".acts button") || m.querySelector(".bubble")).focus();
  setTimeout(() => m.classList.remove("flash"), 1600);
}

// refreshThread asks the network once, when a thread is opened: presence,
// and receipts for messages the server still holds. Not repeated.
async function refreshThread() {
  const id = state.thread;
  try {
    const p = await api("/api/refresh", { id });
    if (state.thread !== id || !state.data) return;
    state.presence[state.data.peer] = { text: p.text, at: p.at };
    $("conv-presence").textContent = peerPresence(state.data.peer);
  } catch (e) { /* presence stays unknown */ }
}

async function loadThread(scrollToEnd) {
  const id = state.thread;
  if (!id) return;
  let t;
  try {
    t = await api("/api/thread?id=" + encodeURIComponent(id));
  } catch (e) {
    announce(e.message);
    return;
  }
  if (state.thread !== id) return; // another thread was opened meanwhile
  state.data = t;
  fill($("conv-name"), who(t.peer));
  $("conv-topic").textContent = firstLine(t.messages[0].body, 90);
  $("conv-avatar").replaceWith(Object.assign(avatar(t.peer), { id: "conv-avatar" }));
  $("conv-presence").textContent = peerPresence(t.peer);
  renderPeerChips(t);
  renderNotice(t);
  $("composer").hidden = false;
  renderBody(scrollToEnd);
  const key = t.messages[0].id; // a conversation's first message names its draft
  if (state.draftKey === null) restoreDraft(key, t); // just switched here (beginThread)
  state.draftKey = key;
  syncComposer();
  const unread = t.messages.filter((m) => m.unread).map((m) => m.id);
  if (unread.length) api("/api/act", { do: "read", ids: unread }).catch(() => {});
}

// renderBody draws the open thread in the chosen presentation. Zoom draws
// itself (lenses.js).
function renderBody(scrollToEnd) {
  if (state.dm) { renderDMBody(scrollToEnd); return; }
  const t = state.data;
  if (!t || state.lens === "zoom") return;
  if (state.lens === "comic") { Comic.render(t); return; }
  const byId = Object.fromEntries(t.messages.map((m) => [m.id, m]));
  const tl = $("timeline");
  const atEnd = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 60;
  fill(tl, ...t.messages.map((m, i) => renderMsg(m, byId, t.messages[i - 1], t)));
  if (scrollToEnd || atEnd) tl.scrollTop = tl.scrollHeight;
}

// setLens switches between the classic view, the comic and zoom. The
// choice is remembered in this browser only.
function setLens(name) {
  if (!lenses.includes(name)) name = "classic";
  state.lens = name;
  try { localStorage.setItem("agentnet-lens", name); } catch (e) { /* not remembered */ }
  for (const b of document.querySelectorAll("[data-lens]")) b.setAttribute("aria-pressed", String(b.dataset.lens === name));
  const zoom = name === "zoom";
  document.querySelector(".app").hidden = zoom;
  $("zoom").hidden = !zoom;
  $("timeline").hidden = name === "comic";
  $("comic").hidden = name !== "comic";
  if (zoom) {
    Object.assign(Zoom, state.dmData ? { level: 2, person: personKey(state.dmData.peer), dm: state.dm, peer: null }
      : state.data ? { level: 2, peer: state.data.peer, person: null } : { level: 0, peer: null, person: null });
    if (state.overview) Zoom.refresh();
  } else {
    renderBody(true);
  }
}

// lensSwitch is another copy of the switch, for views that hide the sidebar.
function lensSwitch() {
  return el("div", { class: "lens-switch", role: "group", "aria-label": "Show conversations as" }, lenses.map((l) =>
    el("button", { type: "button", "data-lens": l, "aria-pressed": String(l === state.lens), onclick: () => setLens(l) }, l[0].toUpperCase() + l.slice(1))));
}

function renderPeerChips(t) {
  const chips = [];
  chips.push(el("button", { type: "button", class: "peer-chip" + (t.approved ? " on" : ""),
    title: t.approved ? "Their questions are answered automatically by your responder" : "Their questions wait for you",
    onclick: () => approvalDialog(t) }, t.approved ? "Questions: automatic" : "Questions: ask me"));
  if (t.task_grant) {
    chips.push(el("button", { type: "button", class: "peer-chip on", title: "Tasks from this exact key run without asking: " + t.task_grant,
      onclick: () => revokeDialog(t) }, t.task_grant === "active" ? "Tasks: always (this key)" : "Tasks: grant on hold"));
  }
  fill($("peer-chips"), ...chips);
}

function renderNotice(t) {
  const n = $("notice");
  if (!t.key.pending) { n.hidden = true; fill(n, ); return; }
  n.hidden = false;
  fill(n, el("p", {}, t.peer + "'s key changed. Their new messages are held and sending is blocked until you trust the new key."),
    el("button", { type: "button", class: "btn", onclick: () => trustDialog(t) }, "Compare keys…"));
}

// Messages from the same author within a few minutes form one group.
function continues(m, prev) {
  return prev && prev.dir === m.dir && !kindTag[m.kind] && !m.status && prev.author.label === m.author.label &&
    !(prev.actions && prev.actions.length) && !prev.summary && new Date(m.at) - new Date(prev.at) < 10 * 60e3;
}

const decisionActions = ["accept", "accept_always", "decline", "approve", "resolve", "reply"];

function renderMsg(m, byId, prev, t) {
  const cont = continues(m, prev);
  const parent = m.reply_to && byId[m.reply_to];
  const actions = m.actions || [];
  const report = isReport(m); // a report from another machine, not a decision here
  const needs = !report && actions.some((a) => decisionActions.includes(a));
  const working = actions.includes("cancel");
  const refWord = m.kind === "answer" ? "Answer to: " : m.kind === "result" ? "Result for: " : "Reply to: ";

  const bubble = el("div", { class: "bubble", tabindex: "-1" },
    m.reply_to && (parent
      ? el("button", { type: "button", class: "replyref", onclick: () => flash(parent.id) }, refWord + firstLine(parent.body, 90))
      : el("span", { class: "replyref" }, "Reply to an earlier message not stored here")),
    el("p", { class: "body" }, m.body),
    m.files && m.files.length && el("div", { class: "files" }, m.files.map((f) => {
      const ext = (f.name.split(".").pop() || "").slice(0, 4).toUpperCase();
      return el("span", { class: "file" }, el("span", { class: "file-icon", "aria-hidden": "true" }, ext || "FILE"),
        el("span", { class: "file-text" }, el("span", { class: "file-name" }, f.name),
          el("span", { class: "file-size" }, f.saved ? "Saved: " + f.saved : size(f.size) + (m.dir === "in" ? " · not saved yet" : ""))));
    })));

  const meta = !cont && el("div", { class: "meta" },
    m.dir === "in" ? who(m.from) : el("span", { class: "who" }, m.author.label),
    kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]),
    m.status && m.status !== "done" && el("span", { class: "tag" }, statusWord[m.status] || m.status),
    m.unread && el("span", { class: "tag unread" }, "New"),
    el("time", { datetime: m.at }, when(m.at)));

  let panel = null;
  if (report) {
    panel = el("div", { class: "report-line" }, el("p", {}, m.state_text),
      actions.length > 0 && el("div", { class: "acts" }, actions.map((a) => actionButton(a, m, t, false))));
  } else if (needs) {
    panel = el("div", { class: "decide" },
      el("p", { class: "decide-why" }, (m.state_text || "").replace(/^Needs you: /, "Needs you · ")),
      m.detail && el("p", { class: "decide-detail" }, m.detail),
      el("div", { class: "acts" }, actions.map((a, i) => actionButton(a, m, t, i === 0))));
  } else if (working) {
    panel = el("div", { class: "working" }, el("span", {}, m.state_text), actionButton("cancel", m, t, false));
  }

  const footText = !needs && !working ? m.state_text : "";
  const waiting = m.next && m.next.startsWith("Waiting on") ? m.next : "";
  const parts = [waiting && el("span", { class: "waiting" }, waiting), footText && el("span", {}, footText), details(m)]
    .filter(Boolean).flatMap((p, i) => i ? [el("span", { class: "sep", "aria-hidden": "true" }, "·"), p] : [p]);

  const col = el("div", { class: "col" }, meta, bubble,
    m.summary && el("div", { class: "note" }, el("p", { class: "note-label" }, "Summary written on this computer by your responder"), el("p", { class: "body" }, m.summary)),
    panel, el("div", { class: "foot" }, parts));
  return el("li", { id: "m-" + m.id, class: "msg " + m.dir + (cont ? " cont" : "") + (needs ? " needs" : "") },
    m.dir === "in" && (cont ? el("span", { class: "avatar sm", "aria-hidden": "true" }) : avatar(m.from, "sm")),
    col);
}

function details(m) {
  return el("details", { class: "tech" }, el("summary", {}, "Details"),
    el("dl", {},
      el("dt", {}, "Written by"), el("dd", {}, m.author.about),
      el("dt", {}, "Sent"), el("dd", {}, new Date(m.at).toLocaleString()),
      el("dt", {}, "Message id"), el("dd", { class: "mono" }, m.id),
      el("dt", {}, "Kind"), el("dd", {}, m.kind),
      m.state && [el("dt", {}, "Stored state"), el("dd", { class: "mono" }, m.state)],
      m.status && [el("dt", {}, "Outcome"), el("dd", { class: "mono" }, m.status)],
      m.responder && [el("dt", {}, "Handled by"), el("dd", {}, m.responder === "manual" ? "a reply by hand" : "your responder (" + m.responder + ")")],
      m.path && [el("dt", {}, "Route"), el("dd", {}, m.path === "direct" ? "Direct to their computer" : "Through the server")],
      m.dir === "in" && m.files && m.files.some((f) => !f.saved) &&
        [el("dt", {}, "Save files"), el("dd", { class: "mono" }, "agentnet download " + m.id)]));
}

const actionLabel = {
  accept: "Accept and run…", accept_always: "Always accept from this key…", decline: "Decline…",
  approve: "Answer their questions automatically…", resolve: "Close without replying…", reply: "Reply", cancel: "Stop…",
};

// isReport: exactly the shape the client files as a review notice (a plain
// message with that status, no reply link, no files).
const isReport = (m) => m.dir === "in" && m.kind === "message" && m.status === "review_notice" && !m.reply_to && !(m.files && m.files.length);

function actionButton(a, m, t, primary) {
  let label = actionLabel[a];
  if (a === "resolve" && isReport(m)) label = "Dismiss report…";
  if (a === "accept" && m.kind === "question") label = "Let your responder answer…";
  if (a === "accept" && ["needs_human", "interrupted", "failed", "cancelled"].includes(m.state)) label = "Run your responder again…";
  return el("button", { type: "button", class: "act" + (primary ? " go" : ""), onclick: () => decide(a, m, t) }, label);
}

// ---- decisions ---------------------------------------------------------------

async function act(body) {
  const r = await api("/api/act", body);
  if (r.note) announce(r.note);
  return r;
}

function decide(a, m, t) {
  if (a === "reply") {
    if (state.lens === "zoom") return writeDialog(t, m);
    setAnswering(m);
    $("body").focus();
    return;
  }
  const quote = el("div", { class: "quote" }, m.body);
  const from = el("dl", {}, el("dt", {}, "From"), el("dd", {}, m.from));
  const runs = state.overview && state.overview.me.responder
    ? "Your responder (" + state.overview.me.responder + ") in " + state.overview.me.responder_dir
    : "Nothing yet: no responder is set. It stays accepted until you choose one (agentnet responder set).";
  if (a === "accept" || a === "accept_always") {
    const check = el("input", { type: "checkbox", id: "gate" });
    const always = a === "accept_always";
    return dialog({
      title: m.kind === "task" ? "Run this task on your computer?" : "Let your responder answer this?",
      body: [from, quote,
        el("dl", {}, el("dt", {}, "Runs"), el("dd", {}, runs),
          el("dt", {}, "Permissions"), el("dd", {}, m.kind === "task"
            ? "Its normal permissions. It is not sandboxed and can change files."
            : "Question mode: your harness's own setup without editing tools or anything needing a new approval. Tools you already allow keep their effects."),
          always && [el("dt", {}, "From now on"), el("dd", {}, "Later tasks from " + m.from + "'s current key also run without asking, until you revoke it.")]),
        el("label", { class: "check" }, check, el("span", {}, always ? "I want this and later tasks from this key to run." : "I have read this and want it to run.")),
        el("p", { class: "hint" }, "In a terminal: agentnet accept " + (always ? "--always " : "") + m.id)],
      ok: always ? "Run and always accept" : "Run", gate: check, run: () => act({ do: a, id: m.id }),
    });
  }
  if (a === "decline") {
    const reason = el("textarea", { id: "reason", rows: "3", placeholder: "Optional, sent to " + m.from });
    dialog({
      title: "Decline this " + m.kind + "?", body: [from, quote, el("label", { for: "reason", class: "field-label" }, "Reason"), reason],
      ok: "Decline", run: () => act({ do: "decline", id: m.id, reason: reason.value }),
    });
    state.dialogRestore = { type: "decline", msg: m.id };
    return;
  }
  if (a === "approve") return approvalDialog(t);
  if (a === "resolve" && isReport(m)) {
    return dialog({ title: "Dismiss this report?", body: [quote,
      el("p", {}, "It is cleared on this computer only. The requests it reported still wait for a person on " + m.from + "'s machine; nothing here can approve them.")],
      ok: "Dismiss", run: () => act({ do: "resolve", id: m.id }) });
  }
  if (a === "resolve") {
    return dialog({ title: "Close without replying?", body: [quote, el("p", {}, "Nothing is sent to " + m.from + ".")],
      ok: "Close", run: () => act({ do: "resolve", id: m.id }) });
  }
  if (a === "cancel") {
    return dialog({ title: "Stop your responder?", body: [quote, el("p", {}, "Work already done on your computer is not undone.")],
      ok: "Stop", run: () => act({ do: "cancel", id: m.id }) });
  }
}

function approvalDialog(t) {
  if (t.approved) {
    return dialog({ title: "Stop answering " + t.peer + "'s questions automatically?",
      body: [el("p", {}, "Their questions will wait for you again. Ones already running may finish unless you stop them.")],
      ok: "Stop automatic answers", run: () => act({ do: "unapprove", id: t.peer }) });
  }
  return dialog({ title: "Answer " + t.peer + "'s questions automatically?",
    body: [el("p", {}, "From now on your responder answers questions from " + t.peer + " without asking you, with your harness's own setup minus editing tools and anything needing a new approval; tools you already allow keep their effects. " +
      (t.task_grant === "active" ? "Tasks are not affected: those from this key already run without asking, under the standing permission you gave." : "Tasks still wait for you.")),
      el("p", {}, "Questions already waiting stay waiting: answer them or let your responder answer each one."),
      el("p", { class: "hint" }, "In a terminal: agentnet approve " + t.peer)],
    ok: "Answer automatically", run: () => act({ do: "approve", id: t.peer }) });
}

function revokeDialog(t) {
  dialog({ title: "Stop running " + t.peer + "'s tasks without asking?",
    body: [el("p", {}, "Their tasks will wait for you again. A task already running is not stopped by this.")],
    ok: "Revoke", run: () => act({ do: "revoke_tasks", id: t.peer }) });
}

function trustDialog(t) {
  const expect = t.key.pending; // the key shown is the only key trusted
  const check = el("input", { type: "checkbox", id: "gate" });
  dialog({
    title: "Trust " + t.peer + "'s new key?", body: [
      el("p", {}, "Ask " + t.peer + " for their key fingerprint through another channel, in person or on a call, and compare."),
      el("dl", {}, el("dt", {}, "Pinned"), el("dd", { class: "mono" }, t.key.pinned || "none"),
        el("dt", {}, "New"), el("dd", { class: "mono" }, expect)),
      el("label", { class: "check" }, check, el("span", {}, "The new fingerprint matches what they told me.")),
      el("p", { class: "hint" }, "In a terminal: agentnet trust " + t.peer)],
    ok: "Trust new key", gate: check, run: () => act({ do: "trust", id: t.peer, key: expect }),
  });
}

// dialog shows a confirmation. The consequential button is never the
// default: focus starts on Cancel and Enter does not confirm. run does the
// work; its error is shown in the dialog, which then stays open.
function dialog({ title, body, ok, run, gate, focus }) {
  const d = $("dialog");
  if (d.open) d.close();
  state.dialogRestore = null; // text dialogs say how to reopen them after an update
  $("dialog-title").textContent = title;
  fill($("dialog-body"), ...body);
  $("dialog-error").textContent = "";
  const okBtn = $("dialog-ok");
  okBtn.textContent = ok;
  okBtn.disabled = !!gate;
  if (gate) gate.addEventListener("change", () => { okBtn.disabled = !gate.checked; });
  let busy = false; // a double click confirms once
  okBtn.onclick = async () => {
    if (busy) return;
    busy = true;
    state.dialogBusy = true;
    okBtn.disabled = true;
    try {
      await run();
      d.close();
    } catch (e) {
      $("dialog-error").textContent = e.message;
      okBtn.disabled = !!gate && !gate.checked;
    } finally {
      busy = false;
      state.dialogBusy = false;
      if (state.newVersion) updated(state.newVersion);
    }
  };
  d.showModal();
  (focus || $("dialog-cancel")).focus();
}

function newConversationDialog(prefill) {
  const to = el("input", { id: "new-to", type: "text", placeholder: "person/agent, e.g. bob/desk", autocomplete: "off", spellcheck: "false" });
  if (typeof prefill === "string") to.value = prefill;
  const kind = el("select", { id: "new-kind" }, ["message", "question", "task"].map((k) => el("option", { value: k }, k[0].toUpperCase() + k.slice(1))));
  const body = el("textarea", { id: "new-body", rows: "4", placeholder: "What do you want to say?" });
  dialog({
    title: "New conversation",
    body: [el("label", { for: "new-to", class: "field-label" }, "To"), to,
      el("label", { for: "new-kind", class: "field-label" }, "Send as"), kind,
      el("label", { for: "new-body", class: "field-label" }, "Message"), body,
      el("p", { class: "hint" }, "A message never runs anything. A question may be answered by their responder if they approved you. A task runs only if they accept it, once or by standing permission for your key.")],
    ok: "Send",
    focus: typeof prefill === "string" ? body : to,
    run: async () => {
      const r = await api("/api/send", { to: to.value.trim(), kind: kind.value, body: body.value });
      announce(r.state === "queued" ? "Queued: it goes out when the server is reachable." : "Sent.");
      openThread(r.id);
    },
  });
  state.dialogRestore = { type: "new", prefill: typeof prefill === "string" ? prefill : undefined };
}

// ---- composer ------------------------------------------------------------------

const kindValue = () => document.querySelector('input[name="kind"]:checked').value;

function setKind(kind) {
  for (const r of document.querySelectorAll('input[name="kind"]')) r.checked = r.value === kind;
  kindHint();
}

// setDMReply chooses the message of the open DM a new message replies to
// (or none). It is part of that DM's draft and never crosses to another.
function setDMReply(m) {
  state.dmReply = m ? { id: m.id, body: m.body } : null;
  if (m && state.dmAgent) setDMAgent(null);
  if (!m && (state.answering || state.dmAgent)) return;
  $("replying").hidden = !m;
  if (m) {
    $("replying-label").textContent = "Replying to";
    $("replying-text").textContent = firstLine(m.body, 70);
  }
}

function setAnswering(m) {
  state.answering = m;
  $("replying").hidden = !m;
  $("replying-label").textContent = "Answering";
  if (m) $("replying-text").textContent = (kindTag[m.kind] || "message").toLowerCase() + ": " + firstLine(m.body, 70);
  syncComposer();
  kindHint();
}

// keepDraft files the composer's text, kind and what it answers under the
// open conversation; restoreDraft puts a conversation's draft back, or an
// empty message.
function keepDraft() {
  if (state.draftKey === null) return;
  const text = $("body").value;
  if (text || state.answering || state.dmReply || state.dmAgent) {
    state.drafts[state.draftKey] = { text, kind: kindValue(), answering: state.answering, reply: state.dmReply, agent: state.dmAgent };
  }
  else delete state.drafts[state.draftKey];
}

function restoreDraft(key, t) {
  const d = state.drafts[key];
  $("body").value = d ? d.text : "";
  grow();
  setKind(d ? d.kind : "message");
  // Answer only what can still be answered by hand.
  const m = d && d.answering && t.messages.find((x) => x.id === d.answering.id && (x.actions || []).includes("reply"));
  setAnswering(m || null);
  // A DM's reply target comes back only if that message is in this DM.
  const r = state.dm && d && d.reply && t.messages.find((x) => x.id === d.reply.id);
  setDMReply(r || null);
  // An agent target comes back only while that agent of this DM can be asked.
  const a = state.dm && d && d.agent && (t.agents || []).find((x) => x.pid === d.agent && x.can_ask);
  setDMAgent(a || null);
  if (a && d.kind === "task") setKind("task");
}

// syncComposer enables what the open conversation allows. A send on its way
// keeps Send disabled, whatever refreshes meanwhile.
function syncComposer() {
  if (state.dm) { // a DM: messages only, to the person
    const d = state.dmData;
    const revoked = !!(state.overview && state.overview.device && state.overview.device.revoked);
    const blocked = !d || !!d.frozen || revoked;
    $("body").disabled = blocked;
    $("send").disabled = blocked || state.sending;
    const a = state.dmAgent && agentOf(state.dmAgent);
    // Asking an agent: a question, or a task when the invitation lets you give it tasks.
    $("kind").hidden = !a;
    for (const r of document.querySelectorAll('input[name="kind"]')) {
      const l = r.value === "message" && r.closest && r.closest("label");
      if (l) l.hidden = !!a;
    }
    if (a && kindValue() === "message") setKind("question");
    $("body").placeholder = !d ? "" : revoked ? "This device was removed from its server" : d.frozen ? "Nothing more can be sent in this conversation"
      : a ? "Ask " + agentName(a).replace(/^Your/, "your") : "Write to " + d.peer.label;
    kindHint();
    return;
  }
  $("kind").hidden = false;
  for (const r of document.querySelectorAll('input[name="kind"]')) { const l = r.closest && r.closest("label"); if (l) l.hidden = false; }
  const t = state.data;
  const blocked = !t || !!t.key.pending;
  $("body").disabled = blocked;
  $("send").disabled = blocked || state.sending;
  $("kind").disabled = blocked || !!state.answering;
  $("body").placeholder = !t ? "" : t.key.pending ? "Sending is blocked until you trust the new key" : "Write to " + t.peer;
}

function grow() {
  const t = $("body");
  t.style.height = "auto";
  t.style.height = Math.min(t.scrollHeight, window.innerHeight * 0.4) + "px";
}

async function send(ev) {
  ev.preventDefault();
  if (state.dm) { await sendDM(); return; }
  const t = state.data;
  if (state.sending || !t || t.key.pending) return; // one send at a time, to a loaded conversation
  // Everything this send needs is fixed now: switching conversation or a
  // refresh while it is on its way changes none of it.
  const key = state.draftKey, text = $("body").value, answering = state.answering;
  const last = t.messages[t.messages.length - 1];
  const draft = { to: t.peer, kind: kindValue(), body: text, reply_to: last ? last.id : "" };
  state.sending = true;
  syncComposer();
  $("compose-error").textContent = "";
  try {
    if (answering) {
      await act({ do: "reply", id: answering.id, body: text });
    } else {
      const r = await api("/api/send", draft);
      announce(r.state === "queued" ? "Queued: it goes out when the server is reachable." : "Sent.");
    }
    // Clear only what was sent: text typed meanwhile, or in another
    // conversation, stays.
    if (state.draftKey === key) {
      if ($("body").value === text) { $("body").value = ""; grow(); }
      if (state.answering === answering) setAnswering(null);
    } else if (state.drafts[key] && state.drafts[key].text === text) {
      delete state.drafts[key];
    }
  } catch (e) {
    if (state.draftKey === key) $("compose-error").textContent = e.message;
    else announce("Not sent to " + t.peer + ": " + e.message + " Your text is kept in that conversation.");
  } finally {
    state.sending = false;
    syncComposer();
    if (state.newVersion) updated(state.newVersion);
  }
}

function kindHint() {
  if (state.dm) {
    const a = state.dmAgent && agentOf(state.dmAgent);
    $("compose-hint").textContent = !a ? "A DM message is for the person; nothing runs it. Ctrl+Enter sends."
      : kindValue() === "task" ? (taskFree(a) ? "It runs on " + a.host.address + " without asking " + (a.host_here ? "you" : a.host.label) + " first."
        : a.host.label + " accepts it first, unless they already allow tasks from your key.") + " Ctrl+Enter sends."
        : "Both of you see what you ask and what it answers. It runs on " + a.host.address + ". Ctrl+Enter sends.";
    return;
  }
  $("compose-hint").textContent = state.answering
    ? "Your reply answers this " + state.answering.kind + " and takes it over from your responder. Ctrl+Enter sends."
    : {
      message: "A message never runs anything. Ctrl+Enter sends.",
      question: "Their responder may answer automatically if they approved you.",
      task: "A task runs only if they accept it, once or by standing permission for your key.",
    }[kindValue()];
}

// ---- push ------------------------------------------------------------------------

// One event stream; each event carries only a change counter. When it
// breaks, say so and wait for the person instead of retrying on a timer.
function listen() {
  if (window.agentnetEngine) { // a browser device: its engine says when something changed
    window.agentnetEngine.listen((seq) => {
      const first = state.seq < 0;
      state.seq = seq;
      refetch(first);
    });
    return;
  }
  const es = new EventSource("/events");
  es.addEventListener("change", (e) => {
    const seq = Number(e.data);
    if (seq === state.seq) return;
    const first = state.seq < 0;
    state.seq = seq;
    refetch(first);
  });
  // The daemon is stopping to switch to its updated program and will serve
  // this page again at the same address.
  es.addEventListener("restart", () => {
    es.close();
    state.updating = true;
    $("updating").hidden = false;
    $("lost").hidden = true;
    recover(recovery.update);
  });
  es.onerror = () => {
    es.close();
    if (state.updating) return; // already reconnecting
    $("lost").hidden = false;
    recover(recovery.missed); // a switch whose notice was missed comes back quickly
  };
}

// recovery: waits (ms) between reconnection attempts, bounded. They happen
// only after the stream breaks; otherwise the page never asks.
const recovery = { update: [500, 1000, 2000, 4000, 8000, 15000, 30000], missed: [1000, 3000] };
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

async function recover(schedule) {
  for (const wait of schedule) {
    await pause(wait);
    if (await reconnect()) return;
  }
  if (state.updating) {
    state.updating = false;
    $("updating").hidden = true;
    $("lost").hidden = false;
    announce("AgentNet did not come back at this address. Run agentnet ui for the address to open, or agentnet update --status.");
  }
}

// reconnect loads the page's data again and listens; it reports success.
async function reconnect() {
  try {
    await loadOverview();
    if (state.thread) await loadThread(false);
    else if (state.dm) await loadDM(false);
  } catch (e) {
    return false;
  }
  state.updating = false;
  $("lost").hidden = true;
  if (!state.newVersion) $("updating").hidden = true;
  listen();
  return true;
}

// updated: the daemon now runs another version. The page reloads to use
// it, keeping every unsent text, but never while a send or a confirmation
// is on its way, and only if what it keeps is stored.
function updated(v) {
  state.newVersion = v;
  if (state.sending || state.dialogBusy) return; // retried when they finish
  if (keepForReload()) { location.reload(); return; }
  $("updating").hidden = false;
  $("updating-text").textContent = "AgentNet was updated to " + v + ". Reload to use it; this browser could not keep your unsent text for the reload, so copy it first.";
  $("reload").hidden = false;
}

const reloadKey = "agentnet-reload";

// keepForReload stores unsent text: every conversation's draft, the
// composer, and an open dialog's fields (not its consent boxes).
function keepForReload() {
  keepDraft();
  const drafts = {};
  for (const [k, d] of Object.entries(state.drafts)) {
    drafts[k] = { text: d.text, kind: d.kind, answering: d.answering ? d.answering.id : null, reply: d.reply ? d.reply.id : null };
  }
  const fields = {};
  if (state.dialogRestore && $("dialog").open) {
    for (const f of $("dialog-body").querySelectorAll("input[type=text], textarea, select")) if (f.id) fields[f.id] = f.value;
  }
  const keep = { drafts, thread: state.thread, dm: state.dm, lens: state.lens, dialog: state.dialogRestore && $("dialog").open ? state.dialogRestore : null, fields };
  try {
    const text = JSON.stringify(keep);
    sessionStorage.setItem(reloadKey, text);
    return sessionStorage.getItem(reloadKey) === text;
  } catch (e) {
    return false;
  }
}

// restoreAfterReload puts back what keepForReload stored, once.
async function restoreAfterReload() {
  let keep = null;
  try {
    keep = JSON.parse(sessionStorage.getItem(reloadKey) || "null");
    sessionStorage.removeItem(reloadKey);
  } catch (e) { /* nothing kept */ }
  if (!keep) return false;
  for (const [k, d] of Object.entries(keep.drafts || {})) {
    state.drafts[k] = { text: d.text, kind: d.kind, answering: d.answering ? { id: d.answering } : null, reply: d.reply ? { id: d.reply } : null };
  }
  if (keep.lens) setLens(keep.lens);
  if (keep.thread) await openThread(keep.thread);
  else if (keep.dm) await openDM(keep.dm);
  const r = keep.dialog;
  if (r) {
    const m = r.msg && state.data ? state.data.messages.find((x) => x.id === r.msg) : null;
    if (r.type === "new") newConversationDialog(r.prefill);
    else if (r.type === "person") personDialog();
    else if (r.type === "dmwrite" && state.dmData) dmWriteDialog(state.dmData);
    else if (r.type === "write" && state.data) writeDialog(state.data, m);
    else if (r.type === "decline" && m) decide("decline", m, state.data);
    for (const [id, v] of Object.entries(keep.fields || {})) { const f = document.getElementById(id); if (f) f.value = v; }
  }
  return true;
}

// refetch loads what the page shows once per burst of changes: changes
// that arrive while it loads cause one more load, not one each.
let loading = false, again = false;
async function refetch(first) {
  if (loading) { again = true; return; }
  loading = true;
  try {
    do {
      again = false;
      const o = await loadOverview();
      if (state.thread) await loadThread(false);
      else if (state.dm) await loadDM(false);
      if (state.lens === "zoom") Zoom.refresh();
      announceChanges(o, first);
      first = false;
    } while (again);
  } catch (e) { /* the next change or the person's return tries again */ }
  loading = false;
}

function announceChanges(o, first) {
  const changed = [];
  for (const t of o.threads) {
    if (!first && state.lastSeen[t.id] !== t.last_at) changed.push(t.peer);
    state.lastSeen[t.id] = t.last_at;
  }
  for (const d of o.dms || []) {
    if (!first && state.lastSeen["dm:" + d.id] !== d.last_at) changed.push(d.peer.label);
    state.lastSeen["dm:" + d.id] = d.last_at;
  }
  if (changed.length) announce("New activity with " + [...new Set(changed)].join(", "));
}

// ---- wiring ------------------------------------------------------------------------

// start wires the page; the relay's page loads this file after the device
// is ready, when the document has loaded already.
function start() {
  $("composer").addEventListener("submit", send);
  $("body").addEventListener("input", grow);
  $("body").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $("composer").requestSubmit(); }
  });
  $("kind").addEventListener("change", kindHint);
  kindHint();
  $("replying-cancel").addEventListener("click", () => { setAnswering(null); setDMReply(null); setDMAgent(null); });
  $("review-btn").addEventListener("click", () => toggleReview());
  $("new-btn").addEventListener("click", () => newConversationDialog());
  $("search").addEventListener("input", () => { state.query = $("search").value; rerenderContacts(); });
  $("search").addEventListener("keydown", (e) => {
    if (e.key === "Escape" && $("search").value) { e.preventDefault(); clearSearch(); }
    if (e.key === "Enter") { const first = $("conv-list").querySelector(".result"); if (first) first.click(); }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("review").hidden) { toggleReview(false); $("review-btn").focus(); return; }
    if (e.key === "/" && !$("dialog").open && !e.target.closest("input, textarea, select") && state.lens !== "zoom") {
      e.preventDefault();
      $("search").focus();
      return;
    }
    // Page turning and zooming keys, unless typing or a dialog is open.
    if ($("dialog").open || e.target.closest("input, textarea, select") || e.ctrlKey || e.metaKey || e.altKey) return;
    const handled = state.lens === "zoom" ? Zoom.onKey(e)
      : state.lens === "comic" && (state.data || state.dmData) && !$("comic").hidden ? Comic.onKey(e) : false;
    if (handled) e.preventDefault();
  });
  for (const b of document.querySelectorAll("#lens [data-lens]")) b.addEventListener("click", () => setLens(b.dataset.lens));
  let saved = null;
  try { saved = localStorage.getItem("agentnet-lens"); } catch (e) { /* default */ }
  $("back").addEventListener("click", () => document.body.classList.remove("show-conv"));
  $("dialog-form").addEventListener("submit", (e) => { if (e.submitter !== $("dialog-cancel")) e.preventDefault(); });
  for (const [id, what] of [["sim-arrival", "arrival"], ["sim-finish", "finish"]]) {
    $(id).addEventListener("click", async () => {
      try { await api("/api/simulate", { what }); } catch (e) { announce(e.message); }
    });
  }
  $("reconnect").addEventListener("click", async () => {
    if (!(await reconnect())) announce("Still not connected. If the daemon restarted, run agentnet ui for the new address.");
  });
  $("reload").addEventListener("click", () => location.reload());
  $("dialog").addEventListener("close", () => { state.dialogRestore = null; });
  window.addEventListener("focus", () => { // a missed change is caught when the person comes back
    if (!$("lost").hidden) return;
    refetch(false);
  });
  // What the person has in front of them, reported when it changes.
  $("timeline").addEventListener("scroll", reportSeen);
  document.addEventListener("visibilitychange", reportSeen);
  window.addEventListener("focus", reportSeen);
  // A desktop alert's click opens this page on its conversation (#conv=ID,
  // from the daemon): taken once and removed from the address.
  const clicked = /^#conv=([0-9a-f]{64})$/.exec(location.hash);
  if (location.hash.startsWith("#conv=")) history.replaceState(null, "", location.pathname + location.search);
  loadOverview().then(async (o) => {
    setLens(saved);
    if (clicked) { await openClicked(clicked[1]); return; }
    if (await restoreAfterReload()) return; // back after an update, with what was unsent
    // Open the latest conversation; reports are not conversations.
    const first = o.threads.find((t) => !t.notice_only && (t.count > 1 || t.review || t.running || t.waiting)) ||
      o.threads.find((t) => !t.notice_only);
    const dm = (o.dms || [])[0];
    const wide = state.lens !== "zoom" && !state.thread && !state.dm && window.matchMedia("(min-width: 761px)").matches;
    if (wide && dm && (!first || new Date(dm.last_at) > new Date(first.last_at))) openDM(dm.id);
    else if (wide && first) openThread(first.id);
  }).catch(() => { $("lost").hidden = false; });
  listen();
}
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
else start();
