// AgentNet messenger page. Text from anyone is inserted as text nodes only.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { current: null, seq: -1, replyTo: null, lastIds: {}, lastPeer: null };

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
    if (k === undefined || k === null || k === false || k === "") continue;
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

const time = (s) => new Date(s).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
const when = (s) => {
  const d = new Date(s);
  return d.toDateString() === new Date().toDateString() ? time(s)
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
};
const size = (n) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : (n / (1 << 20)).toFixed(1) + " MB";
const firstLine = (s, n) => (s || "").split("\n")[0].slice(0, n);
const announce = (t) => { $("live").textContent = t; };
const kindTag = { question: "Question", task: "Task" };
const statusWord = { declined: "Declined", failed: "Failed", timeout: "Timed out", cancelled: "Cancelled", interrupted: "Interrupted" };

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
  const n = items.length;
  $("review-count").textContent = n;
  $("review-word").textContent = n ? "Needs you" : "Nothing needs you";
  btn.dataset.n = n;
  btn.setAttribute("aria-label", n === 1 ? "1 item needs your decision" : n + " items need your decision");
  $("review-list").replaceChildren(...(n ? items.map((it) => el("li", {},
    el("button", { type: "button", onclick: () => { toggleReview(false); openConv(it.peer, it.id); } },
      el("span", {}, who(it.peer), " · ", kindTag[it.kind] || it.kind),
      el("span", { class: "review-why" }, it.why),
      el("span", { class: "review-text" }, it.excerpt)))) : [el("li", { class: "hint" }, "Nothing is waiting for you.")]));
}

function toggleReview(open) {
  const btn = $("review-btn");
  const show = open ?? btn.getAttribute("aria-expanded") !== "true";
  btn.setAttribute("aria-expanded", show);
  $("review").hidden = !show;
  if (show) $("review").querySelector("button")?.focus();
}

function renderConvs(convs) {
  $("conv-list").replaceChildren(...convs.map((c) => {
    let flag = null;
    if (c.review) flag = el("span", { class: "badge", title: "Needs your decision" }, String(c.review),
      el("span", { class: "sr-only" }, c.review === 1 ? " item needs your decision" : " items need your decision"));
    else if (c.paused) flag = el("span", { class: "conv-flag danger" }, "Key changed");
    else if (c.next.startsWith("Your responder")) flag = el("span", { class: "conv-flag calm" }, "Responder working");
    else if (c.next.startsWith("Waiting on")) flag = el("span", { class: "conv-flag calm" }, "Awaiting reply");
    return el("li", {},
      el("button", {
        type: "button", class: "conv-item",
        "aria-current": c.peer === state.current ? "true" : "false", onclick: () => openConv(c.peer),
      },
        avatar(c.peer),
        el("span", { class: "conv-main" },
          el("span", { class: "conv-top" }, el("span", { class: "conv-name" }, who(c.peer)),
            el("span", { class: "conv-time" }, c.last_at && !c.last_at.startsWith("0001") ? when(c.last_at) : "")),
          el("span", { class: "conv-bottom" }, el("span", { class: "conv-last", title: c.last }, c.last), flag))));
  }));
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
  (m.querySelector(".acts button") || m.querySelector(".bubble")).focus();
  setTimeout(() => m.classList.remove("flash"), 1600);
}

async function loadConv() {
  const peer = state.current;
  if (!peer) return;
  const c = await api("/api/conversation?peer=" + encodeURIComponent(peer));
  if (state.current !== peer) return; // another conversation was opened meanwhile
  $("conv-name").replaceChildren(who(c.peer));
  $("conv-avatar").replaceWith(Object.assign(avatar(c.peer), { id: "conv-avatar" }));
  $("conv-presence").textContent = c.presence;
  renderNotice(c);
  $("composer").hidden = false;
  const byId = Object.fromEntries(c.messages.map((m) => [m.id, m]));
  const tl = $("timeline");
  const atEnd = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 60;
  tl.replaceChildren(...c.messages.map((m, i) => renderMsg(m, byId, c.messages[i - 1])));
  if (!c.messages.length) tl.replaceChildren(el("li", { class: "empty" }, "No messages yet."));
  if (atEnd || state.lastPeer !== c.peer) tl.scrollTop = tl.scrollHeight;
  state.lastPeer = c.peer;
  const paused = !!c.notice;
  for (const id of ["body", "send", "files"]) $(id).disabled = paused;
  $("kind").disabled = paused || !!state.replyTo;
  $("body").placeholder = paused ? "Sending is paused until you confirm the new key" : "Write to " + c.peer;
}

function renderNotice(c) {
  const n = $("notice");
  if (!c.notice) { n.hidden = true; n.replaceChildren(); return; }
  n.hidden = false;
  n.replaceChildren(el("p", {}, c.notice.text),
    el("button", { type: "button", class: "btn", onclick: () => trustDialog(c) }, "Compare keys…"));
}

// Messages from the same author within a few minutes form one group.
function continues(m, prev) {
  return prev && prev.dir === m.dir && m.dir !== "system" && !kindTag[m.kind] && !m.status && prev.author.label === m.author.label &&
    !(prev.actions && prev.actions.length) && !prev.note && new Date(m.at) - new Date(prev.at) < 10 * 60e3;
}

const decisions = ["accept", "decline", "approve", "resolve", "reply"];

function renderMsg(m, byId, prev) {
  const cont = continues(m, prev);
  const parent = m.reply_to && byId[m.reply_to];
  const needs = (m.actions || []).some((a) => decisions.includes(a));
  const working = (m.actions || []).includes("cancel");
  const refWord = m.kind === "answer" ? "Answer to: " : m.kind === "result" ? "Result for: " : "Reply to: ";

  const bubble = el("div", { class: "bubble", tabindex: "-1" },
    m.reply_to && (parent
      ? el("button", { type: "button", class: "replyref", onclick: () => flash(parent.id) }, refWord + firstLine(parent.body, 90))
      : el("span", { class: "replyref" }, "Reply to an earlier message not stored here")),
    m.body && el("p", { class: "body" }, m.body),
    m.files && m.files.length && el("div", { class: "files" }, m.files.map((f) => {
      const ext = (f.name.split(".").pop() || "").slice(0, 4).toUpperCase();
      return el("span", { class: "file" }, el("span", { class: "file-icon", "aria-hidden": "true" }, ext || "FILE"),
        el("span", { class: "file-text" }, el("span", { class: "file-name" }, f.name), el("span", { class: "file-size" }, size(f.size))),
        m.dir === "in" && el("button", { type: "button", class: "text-btn", onclick: () => announce("Demo: nothing was downloaded.") }, "Download"));
    })));

  const meta = !cont && m.dir !== "system" && el("div", { class: "meta" },
    m.dir === "in" && m.author.label === m.peer ? who(m.peer) : el("span", { class: "who" }, m.author.label),
    m.author.future && el("span", { class: "tag future" }, "Future idea"),
    kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]),
    m.status && m.status !== "done" && el("span", { class: "tag" }, statusWord[m.status] || m.status),
    el("time", { datetime: m.at }, when(m.at)));

  let panel = null;
  if (needs) {
    panel = el("div", { class: "decide" },
      el("p", { class: "decide-why" }, (m.state_text || "").replace(/^Needs you: /, "Needs you · ")),
      m.detail && el("p", { class: "decide-detail" }, m.detail),
      el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, i === 0))));
  } else if (working) {
    panel = el("div", { class: "working" }, el("span", {}, m.state_text), actionButton("cancel", m, false));
  }

  const footText = !needs && !working ? m.state_text : "";
  const waiting = m.next && m.next.startsWith("Waiting on") ? m.next : "";
  const parts = [waiting && el("span", { class: "waiting" }, waiting), footText && el("span", {}, footText), details(m)]
    .filter(Boolean).flatMap((p, i) => i ? [el("span", { class: "sep", "aria-hidden": "true" }, "·"), p] : [p]);
  const foot = el("div", { class: "foot" }, parts);

  const col = el("div", { class: "col" }, meta, bubble,
    m.note && el("div", { class: "note" }, el("p", { class: "note-label" }, m.note.label), el("p", { class: "body" }, m.note.text)),
    panel, foot);
  return el("li", { id: "m-" + m.id, class: "msg " + m.dir + (cont ? " cont" : "") + (needs ? " needs" : "") },
    m.dir === "in" && (cont ? el("span", { class: "avatar sm", "aria-hidden": "true" }) : avatar(m.peer, "sm")),
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
      m.path && [el("dt", {}, "Route"), el("dd", {}, m.path === "direct" ? "Direct to their computer" : "Through the server")]));
}

const actionLabel = {
  accept: "Accept and run…", decline: "Decline…", approve: "Answer them automatically from now on…",
  resolve: "Close without replying…", reply: "Reply", cancel: "Stop…",
};

function actionButton(a, m, primary) {
  let label = actionLabel[a];
  if (a === "accept" && m.kind === "question") label = "Let your responder answer…";
  if (a === "accept" && m.state === "needs_human") label = "Run your responder again…";
  return el("button", { type: "button", class: "act" + (primary ? " go" : ""), onclick: () => decide(a, m) }, label);
}

// ---- decisions (simulated in the demo) -------------------------------------

function decide(a, m) {
  if (a === "reply") { setReply(m); $("body").focus(); return; }
  const machine = $("machine").textContent;
  const quote = el("div", { class: "quote" }, m.body);
  const from = el("dl", {}, el("dt", {}, "From"), el("dd", {}, m.peer));
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
    const reason = el("textarea", { id: "decline-reason" });
    return dialog({
      title: "Decline this " + m.kind + "?", body: [from, quote,
        el("label", { for: "decline-reason" }, "Reason, sent to " + m.peer + " (optional)"), reason],
      ok: "Decline (simulated)", act: () => ({ id: m.id, do: "decline", reason: reason.value }),
    });
  }
  if (a === "approve") {
    return dialog({
      title: "Answer " + m.peer + "'s questions automatically?", body: [
        el("p", {}, "Future questions from " + m.peer + " are answered by your responder without asking you, in question mode (no tools). Tasks still wait for you."),
        el("p", {}, "This question stays waiting: answer it yourself or let your responder answer it."),
        el("p", { class: "hint" }, "In a terminal: agentnet approve " + m.peer)],
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
      el("p", {}, "Ask " + c.peer + " for their fingerprint through another channel, in person or on a call, and compare."),
      el("dl", {}, el("dt", {}, "Previous"), el("dd", { class: "mono" }, c.notice.old),
        el("dt", {}, "New"), el("dd", { class: "mono" }, c.notice.new)),
      el("label", { class: "check" }, check, el("span", {}, "The new fingerprint matches what they told me.")),
      el("p", { class: "hint" }, "In a terminal: agentnet trust " + c.peer)],
    ok: "Confirm key (simulated)", gate: check, act: { id: c.peer, do: "trust" },
  });
}

// dialog shows a confirmation. The consequential button is never the
// default: focus starts on Cancel and Enter does not submit.
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

const kindValue = () => document.querySelector('input[name="kind"]:checked').value;

function setReply(m) {
  state.replyTo = m;
  $("replying").hidden = !m;
  $("kind").disabled = !!m;
  if (m) $("replying-text").textContent = firstLine(m.body, 70);
}

function grow() {
  const t = $("body");
  t.style.height = "auto";
  t.style.height = Math.min(t.scrollHeight, window.innerHeight * 0.4) + "px";
}

async function send(ev) {
  ev.preventDefault();
  $("compose-error").textContent = "";
  const files = [...$("files").files].map((f) => ({ name: f.name, size: f.size }));
  try {
    await api("/api/send", {
      peer: state.current, kind: kindValue(), body: $("body").value,
      reply_to: state.replyTo ? state.replyTo.id : "", files,
    });
    $("body").value = "";
    $("files").value = "";
    $("file-names").textContent = "";
    grow();
    setReply(null);
  } catch (e) { $("compose-error").textContent = e.message; }
}

function kindHint() {
  $("compose-hint").textContent = {
    message: "A message never runs anything. Ctrl+Enter sends.",
    question: "Their responder may answer automatically if they approved you.",
    task: "A task runs only if they accept it.",
  }[kindValue()];
}

// ---- push ------------------------------------------------------------------

// One event stream; each event carries only a change counter. When it
// breaks, say so and wait for the person instead of retrying on a timer.
function listen() {
  const es = new EventSource("/events");
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
  $("body").addEventListener("input", grow);
  $("body").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $("composer").requestSubmit(); }
  });
  $("kind").addEventListener("change", kindHint);
  kindHint();
  $("files").addEventListener("change", () => {
    const names = [...$("files").files].map((f) => f.name).join(", ");
    $("file-names").textContent = names ? names + " (demo: names only, not read)" : "";
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
      try { await api("/api/simulate", { what }); } catch (e) { announce(e.message); demoNote(e.message); }
    });
  }
  $("reconnect").addEventListener("click", async () => {
    try {
      await loadState();
      if (state.current) await loadConv();
      $("lost").hidden = true;
      listen();
    } catch (e) { announce("Still not connected. If agentnet restarted, open the new address it printed."); }
  });
  loadState().then((s) => {
    if (!state.current && s.conversations.length && window.matchMedia("(min-width: 761px)").matches) openConv(s.conversations[0].peer);
  }).catch(() => { $("lost").hidden = false; });
  listen();
});

function demoNote(msg) {
  const d = $("demo");
  let n = d.querySelector(".hint");
  if (!n) { n = el("span", { class: "hint" }); d.append(n); }
  n.textContent = msg;
}
