// AgentNet messenger page. Text from anyone is inserted as text nodes only.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { current: null, seq: -1, replyTo: null, lastIds: {}, stream: null };

// el builds an element; string children become text nodes.
function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat()) {
    if (k === undefined || k === null || k === false) continue;
    e.append(typeof k === "string" ? document.createTextNode(k) : k);
  }
  return e;
}

async function api(path, body) {
  const opts = body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  };
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.json();
}

const time = (s) => new Date(s).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const when = (s) => {
  const d = new Date(s);
  return d.toDateString() === new Date().toDateString() ? time(s)
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
};
const size = (n) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : (n / (1 << 20)).toFixed(1) + " MB";
const kindWord = { question: "Question", task: "Task", answer: "Answer", result: "Result" };
const announce = (t) => { $("live").textContent = t; };

// ---- overview -------------------------------------------------------------

async function loadState() {
  const s = await api("/api/state");
  $("demo").hidden = !s.demo;
  $("me").textContent = s.me;
  $("machine").textContent = s.machine.hub + " · " + (s.machine.responder
    ? "Your responder: " + s.machine.responder + " in " + s.machine.responder_dir
    : "No responder: questions and tasks wait for you");
  renderReview(s.review);
  renderConvs(s.conversations);
  return s;
}

function renderReview(items) {
  const btn = $("review-btn");
  $("review-count").textContent = items.length;
  $("review-word").textContent = items.length === 1 ? "needs you" : "need you";
  btn.dataset.n = items.length;
  btn.setAttribute("aria-label", items.length + " items need your decision");
  const list = $("review-list");
  list.replaceChildren(...items.map((it) => el("li", {},
    el("button", { type: "button", onclick: () => { toggleReview(false); openConv(it.peer, it.id); } },
      el("span", {}, el("span", { class: "mono" }, it.peer), " · ", kindWord[it.kind] || it.kind),
      el("span", { class: "why" }, it.why),
      el("span", { class: "hint" }, it.excerpt)))));
  if (!items.length) list.replaceChildren(el("li", { class: "hint" }, "Nothing is waiting for you."));
}

function toggleReview(open) {
  const btn = $("review-btn");
  const show = open ?? btn.getAttribute("aria-expanded") !== "true";
  btn.setAttribute("aria-expanded", show);
  $("review").hidden = !show;
  if (show) $("review").querySelector("button")?.focus();
}

function renderConvs(convs) {
  $("conv-list").replaceChildren(...convs.map((c) => el("li", {},
    el("button", {
      type: "button", "aria-current": c.peer === state.current ? "true" : "false",
      onclick: () => openConv(c.peer),
    },
      el("span", { class: "conv-row1" }, el("span", { class: "mono" }, c.peer),
        el("span", { class: "conv-time" }, c.last_at && !c.last_at.startsWith("0001") ? when(c.last_at) : "")),
      c.next && el("span", { class: "conv-next" + (c.review ? " you" : "") },
        c.review ? c.next + " (" + c.review + ")" : c.next),
      c.paused && el("span", { class: "conv-next you" }, "Key changed: sending paused"),
      el("span", { class: "conv-last" }, c.last),
      el("span", { class: "conv-presence" }, c.presence)))));
}

// ---- conversation ---------------------------------------------------------

async function openConv(peer, focusId) {
  if (state.current !== peer) setReply(null);
  state.current = peer;
  document.body.classList.add("show-conv");
  await loadConv();
  await loadState();
  if (focusId) flash(focusId);
}

function flash(id) {
  const m = document.getElementById("m-" + id);
  if (!m) return;
  m.scrollIntoView({ block: "center" });
  m.classList.add("flash");
  (m.querySelector(".actions button") || m.querySelector(".bubble")).focus();
  setTimeout(() => m.classList.remove("flash"), 1600);
}

async function loadConv() {
  if (!state.current) return;
  const c = await api("/api/conversation?peer=" + encodeURIComponent(state.current));
  $("conv-peer").textContent = c.peer;
  $("conv-presence").textContent = c.presence;
  renderNotice(c);
  $("composer").hidden = false;
  const byId = Object.fromEntries(c.messages.map((m) => [m.id, m]));
  const tl = $("timeline");
  const atEnd = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 40;
  tl.replaceChildren(...c.messages.map((m) => renderMsg(m, byId)));
  if (!c.messages.length) tl.replaceChildren(el("li", { class: "empty" }, "No messages yet."));
  if (atEnd || state.lastPeer !== c.peer) tl.scrollTop = tl.scrollHeight;
  state.lastPeer = c.peer;
  const paused = !!c.notice;
  for (const id of ["body", "send", "kind", "files"]) $(id).disabled = paused;
  $("body").placeholder = paused ? "Sending is paused until you confirm the new key"
    : "Write to " + c.peer + " (Ctrl+Enter sends)";
}

function renderNotice(c) {
  const n = $("notice");
  if (!c.notice) { n.hidden = true; n.replaceChildren(); return; }
  n.hidden = false;
  n.replaceChildren(el("p", {}, c.notice.text),
    el("button", { type: "button", onclick: () => trustDialog(c) }, "Compare and confirm the new key…"));
}

function renderMsg(m, byId) {
  const needs = m.next === "Needs you";
  const parent = m.reply_to && byId[m.reply_to];
  const bubble = el("div", { class: "bubble", tabindex: "-1" },
    m.reply_to && el("div", { class: "replyref" }, parent
      ? el("button", { type: "button", class: "link", onclick: () => flash(parent.id) },
        "Reply to: " + (parent.body || "").split("\n")[0].slice(0, 80))
      : el("span", { class: "hint" }, "Reply to an earlier message not stored here")),
    m.body && el("p", { class: "body" }, m.body),
    m.files && m.files.length && el("div", { class: "files" }, m.files.map((f) =>
      el("span", { class: "file" }, el("span", {}, f.name), el("span", { class: "hint" }, size(f.size)),
        m.dir === "in" && el("button", { type: "button", class: "link", onclick: () => announce("Demo: nothing downloaded.") }, "Download")))),
    m.note && el("div", { class: "note" }, el("p", { class: "note-label" }, m.note.label), el("p", { class: "body" }, m.note.text)),
    m.detail && el("p", { class: "detail" }, m.detail),
    m.actions && m.actions.length && el("div", { class: "actions" }, m.actions.map((a) => actionButton(a, m))));
  return el("li", { id: "m-" + m.id, class: "msg " + m.dir + (needs ? " needs" : "") },
    el("div", { class: "meta" },
      el("span", { class: "author" }, m.author.label),
      m.author.future && el("span", { class: "tag future" }, "Future idea"),
      kindWord[m.kind] && el("span", { class: "tag" }, kindWord[m.kind]),
      m.status && m.status !== "done" && el("span", { class: "tag" }, m.status),
      el("time", { datetime: m.at }, when(m.at))),
    bubble,
    (m.state_text || m.next) && el("div", { class: "status" },
      m.state_text && el("span", {}, m.state_text),
      m.next && m.next.startsWith("Waiting on") && el("span", { class: "next" }, m.next)),
    el("details", { class: "tech" }, el("summary", {}, "Details"),
      el("dl", {},
        el("dt", {}, "Written by"), el("dd", {}, m.author.about),
        el("dt", {}, "Message id"), el("dd", { class: "mono" }, m.id),
        el("dt", {}, "Kind"), el("dd", {}, m.kind),
        m.state && [el("dt", {}, "Stored state"), el("dd", { class: "mono" }, m.state)],
        m.status && [el("dt", {}, "Outcome"), el("dd", { class: "mono" }, m.status)],
        m.path && [el("dt", {}, "Route"), el("dd", {}, m.path === "direct" ? "Direct to their computer" : "Through the Hub")])));
}

const actionLabel = {
  accept: "Accept and run…", decline: "Decline…", approve: "Approve sender…",
  resolve: "Close without replying…", reply: "Reply", cancel: "Stop your responder…",
};

function actionButton(a, m) {
  let label = actionLabel[a];
  if (a === "accept" && m.kind === "question") label = "Let your responder answer…";
  if (a === "accept" && m.state === "needs_human") label = "Run your responder again…";
  return el("button", { type: "button", onclick: () => decide(a, m) }, label);
}

// ---- decisions (simulated in the demo) -------------------------------------

function decide(a, m) {
  if (a === "reply") { setReply(m); $("body").focus(); return; }
  const machine = $("machine").textContent;
  const quote = el("div", { class: "quote" }, m.body);
  const from = el("dl", {}, el("dt", {}, "From"), el("dd", { class: "mono" }, m.peer));
  if (a === "accept") {
    const check = el("input", { type: "checkbox", id: "confirm-read" });
    return dialog({
      title: m.kind === "task" ? "Run this task on your computer?" : "Let your responder answer this?",
      body: [from, quote,
        el("dl", {}, el("dt", {}, "Runs"), el("dd", {}, machine.split(" · ")[1] || "your responder"),
          el("dt", {}, "Permissions"), el("dd", {}, m.kind === "task"
            ? "Its normal permissions. It is not sandboxed and can change files."
            : "Question mode: no tools.")),
        el("label", { class: "check" }, check, el("span", {}, "I have read this and want it to run.")),
        el("p", { class: "hint" }, "In a terminal: agentnet accept " + m.id)],
      ok: "Run (simulated)", gate: check, act: { id: m.id, do: "accept" },
    });
  }
  if (a === "decline") {
    const reason = el("textarea", { id: "decline-reason", "aria-label": "Reason (optional, sent to " + m.peer + ")" });
    return dialog({
      title: "Decline this " + m.kind + "?", body: [from, quote,
        el("label", { for: "decline-reason" }, "Reason, sent to " + m.peer + " (optional)"), reason],
      ok: "Decline (simulated)", act: () => ({ id: m.id, do: "decline", reason: reason.value }),
    });
  }
  if (a === "approve") {
    return dialog({
      title: "Answer " + m.peer + "'s questions automatically?", body: [
        el("p", {}, "From now on your responder answers questions from " + m.peer + " without asking you, in question mode (no tools). Tasks still wait for you."),
        el("p", {}, "This question is answered now."), el("p", { class: "hint" }, "In a terminal: agentnet approve " + m.peer)],
      ok: "Approve (simulated)", act: { id: m.id, do: "approve" },
    });
  }
  if (a === "resolve") {
    return dialog({
      title: "Close without replying?", body: [quote, el("p", {}, "Nothing is sent to " + m.peer + ".")],
      ok: "Close (simulated)", act: { id: m.id, do: "resolve" },
    });
  }
  if (a === "cancel") {
    return dialog({
      title: "Stop your responder?", body: [quote, el("p", {}, "Work already done on your computer is not undone.")],
      ok: "Stop (simulated)", act: { id: m.id, do: "cancel" },
    });
  }
}

function trustDialog(c) {
  const check = el("input", { type: "checkbox", id: "confirm-fp" });
  dialog({
    title: "Confirm " + c.peer + "'s new key?", body: [
      el("p", {}, "Ask " + c.peer + " for their fingerprint through another channel (in person or a call) and compare."),
      el("dl", {}, el("dt", {}, "Previous"), el("dd", { class: "mono" }, c.notice.old),
        el("dt", {}, "New"), el("dd", { class: "mono" }, c.notice.new)),
      el("label", { class: "check" }, check, el("span", {}, "The new fingerprint matches what they told me.")),
      el("p", { class: "hint" }, "In a terminal: agentnet trust " + c.peer)],
    ok: "Confirm key (simulated)", gate: check, act: { id: c.peer, do: "trust" },
  });
}

// dialog shows a confirmation. The dangerous button is never the default:
// focus starts on Cancel and Enter does not submit.
function dialog({ title, body, ok, act, gate }) {
  const d = $("dialog");
  $("dialog-title").textContent = title;
  $("dialog-body").replaceChildren(...body);
  $("dialog-error").textContent = "";
  const okBtn = $("dialog-ok");
  okBtn.textContent = ok;
  okBtn.disabled = !!gate;
  if (gate) gate.addEventListener("change", () => { okBtn.disabled = !gate.checked; });
  okBtn.onclick = async () => {
    try {
      await api("/api/act", typeof act === "function" ? act() : act);
      d.close();
      announce("Done: " + ok.replace(" (simulated)", ""));
    } catch (e) { $("dialog-error").textContent = e.message; }
  };
  d.showModal();
  $("dialog-cancel").focus();
}

// ---- composer --------------------------------------------------------------

function setReply(m) {
  state.replyTo = m;
  $("replying").hidden = !m;
  $("kind").disabled = !!m;
  if (m) $("replying-text").textContent = (kindWord[m.kind] || "message").toLowerCase() + ": " + (m.body || "").split("\n")[0].slice(0, 60);
}

async function send(ev) {
  ev.preventDefault();
  $("compose-error").textContent = "";
  const files = [...$("files").files].map((f) => ({ name: f.name, size: f.size }));
  try {
    await api("/api/send", {
      peer: state.current, kind: $("kind").value, body: $("body").value,
      reply_to: state.replyTo ? state.replyTo.id : "", files,
    });
    $("body").value = "";
    $("files").value = "";
    $("file-names").textContent = "";
    setReply(null);
  } catch (e) { $("compose-error").textContent = e.message; }
}

// ---- push ------------------------------------------------------------------

// One event stream; each event carries only a change counter. When it
// breaks, say so and wait for the person instead of retrying on a timer.
function listen() {
  const es = new EventSource("/events");
  state.stream = es;
  es.addEventListener("change", async (e) => {
    const seq = Number(e.data);
    if (seq === state.seq) return;
    const first = state.seq < 0;
    state.seq = seq;
    const s = await loadState();
    if (state.current) await loadConv();
    if (!first) announceChanges(s);
    else snapshotIds(s);
  });
  es.onerror = () => {
    es.close();
    $("lost").hidden = false;
  };
}

function snapshotIds(s) {
  for (const c of s.conversations) state.lastIds[c.peer] = c.last_at;
}

function announceChanges(s) {
  const changed = s.conversations.filter((c) => state.lastIds[c.peer] !== c.last_at).map((c) => c.peer);
  snapshotIds(s);
  if (changed.length) announce("New activity with " + changed.join(", "));
}

// ---- wiring ----------------------------------------------------------------

document.addEventListener("DOMContentLoaded", () => {
  $("composer").addEventListener("submit", send);
  $("body").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $("composer").requestSubmit(); }
  });
  $("kind").addEventListener("change", () => {
    $("compose-hint").textContent = {
      message: "A message never runs anything.",
      question: "Their responder may answer automatically if they approved you.",
      task: "A task runs only if they accept it.",
    }[$("kind").value];
  });
  $("kind").dispatchEvent(new Event("change"));
  $("files").addEventListener("change", () => {
    $("file-names").textContent = [...$("files").files].map((f) => f.name).join(", ") + " (demo: names only, not read)";
  });
  $("replying-cancel").addEventListener("click", () => setReply(null));
  $("review-btn").addEventListener("click", () => toggleReview());
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("review").hidden) { toggleReview(false); $("review-btn").focus(); }
  });
  $("back").addEventListener("click", () => document.body.classList.remove("show-conv"));
  $("dialog-form").addEventListener("submit", (e) => { if (e.submitter !== $("dialog-cancel")) e.preventDefault(); });
  for (const [id, what] of [["sim-arrival", "arrival"], ["sim-finish", "finish"]]) {
    $(id).addEventListener("click", async () => {
      try { await api("/api/simulate", { what }); } catch (e) { announce(e.message); alertOnce(e.message); }
    });
  }
  $("reconnect").addEventListener("click", async () => {
    try {
      await loadState();
      if (state.current) await loadConv();
      $("lost").hidden = true;
      listen();
    } catch (e) { announce("Still not connected."); }
  });
  loadState().then((s) => {
    if (s.conversations.length && window.matchMedia("(min-width: 761px)").matches) openConv(s.conversations[0].peer);
  }).catch(() => { $("lost").hidden = false; });
  listen();
});

function alertOnce(msg) {
  const d = $("demo");
  let n = d.querySelector(".hint");
  if (!n) { n = el("span", { class: "hint" }); d.append(n); }
  n.textContent = msg;
}
