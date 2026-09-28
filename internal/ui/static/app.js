// AgentNet messenger page. Text from anyone is inserted as text nodes only.
// Every change goes through the server's actions, which use the same client
// operations and rules as the command line; the page decides nothing itself.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { thread: null, data: null, seq: -1, answering: null, lastSeen: {}, presence: {}, lens: "classic",
  drafts: {}, draftKey: null, sending: false };
const lenses = ["classic", "comic", "zoom"];

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
const firstLine = (s, n) => {
  const l = (s || "").split("\n")[0];
  return l.length > n ? l.slice(0, n - 1).trimEnd() + "…" : l;
};
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

// ---- overview ---------------------------------------------------------------

async function loadOverview() {
  const o = await api("/api/overview");
  state.overview = o;
  $("demo").hidden = !o.demo;
  $("me").textContent = o.me.address;
  $("me").title = "Key " + o.me.fingerprint;
  $("machine").textContent = o.me.responder
    ? "Your responder: " + o.me.responder + " in " + o.me.responder_dir
    : "No responder: questions and tasks wait for you";
  $("release").hidden = !o.release;
  $("release").textContent = o.release ? "Update recommended: " + o.release + " (see agentnet help update)" : "";
  renderReview(o.review);
  renderThreads(o.threads);
  renderQuarantine(o.quarantine);
  return o;
}

function renderReview(items) {
  const btn = $("review-btn");
  const n = items.length;
  $("review-count").textContent = n;
  $("review-word").textContent = n ? "Needs you" : "Nothing needs you";
  btn.dataset.n = n;
  btn.setAttribute("aria-label", n === 1 ? "1 item needs your decision" : n + " items need your decision");
  $("review-list").replaceChildren(...(n ? items.map((it) => el("li", {},
    el("button", { type: "button", onclick: () => { toggleReview(false); openThread(it.id, it.id); } },
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

function renderThreads(threads) {
  if (!threads.length) {
    $("conv-list").replaceChildren(el("li", { class: "hint empty-list" }, "No conversations yet. Start one with the + button."));
    return;
  }
  $("conv-list").replaceChildren(...threads.map((t) => {
    let flag = null;
    if (t.review) flag = el("span", { class: "badge", title: "Needs your decision" }, String(t.review),
      el("span", { class: "sr-only" }, t.review === 1 ? " item needs your decision" : " items need your decision"));
    else if (t.key_changed) flag = el("span", { class: "conv-flag danger" }, "Key changed");
    else if (t.running) flag = el("span", { class: "conv-flag calm" }, "Responder working");
    else if (t.unread) flag = el("span", { class: "conv-flag unread" }, t.unread + " new");
    else if (t.waiting) flag = el("span", { class: "conv-flag calm" }, "Awaiting reply");
    const current = state.data && state.data.messages.some((m) => m.id === t.id);
    return el("li", {},
      el("button", { type: "button", class: "conv-item", "aria-current": current ? "true" : "false", onclick: () => openThread(t.id) },
        avatar(t.peer),
        el("span", { class: "conv-main" },
          el("span", { class: "conv-top" }, el("span", { class: "conv-name" }, who(t.peer)),
            el("span", { class: "conv-time" }, when(t.last_at))),
          el("span", { class: "conv-subject" }, t.title),
          el("span", { class: "conv-bottom" }, el("span", { class: "conv-last" }, t.count > 1 ? t.last : ""), flag))));
  }));
}

function renderQuarantine(items) {
  const box = $("quarantine");
  box.hidden = !items.length;
  if (!items.length) return;
  $("quarantine-summary").textContent = items.length === 1 ? "1 message held back" : items.length + " messages held back";
  $("quarantine-list").replaceChildren(...items.map((q) => el("li", {},
    el("span", {}, who(q.peer), " · ", when(q.at)), el("span", { class: "hint" }, q.reason))));
}

// ---- thread -----------------------------------------------------------------

// beginThread starts showing the thread holding message id, from any view.
// The composer's draft stays with the conversation it was written in, and
// nothing can be sent until the new one has loaded. It reports whether the
// conversation changed.
function beginThread(id) {
  const changed = !state.data || !state.data.messages.some((m) => m.id === id);
  if (changed) {
    keepDraft();
    state.data = null;
    state.draftKey = null;
    $("body").value = "";
    grow();
    setKind("message");
    setAnswering(null);
  }
  state.thread = id;
  return changed;
}

async function openThread(id, focusId) {
  const changed = beginThread(id);
  document.body.classList.add("show-conv");
  await loadThread(changed);
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
    state.presence[state.data.peer] = p.text;
    $("conv-presence").textContent = p.text;
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
  $("conv-name").replaceChildren(who(t.peer));
  $("conv-avatar").replaceWith(Object.assign(avatar(t.peer), { id: "conv-avatar" }));
  $("conv-presence").textContent = state.presence[t.peer] || "";
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
  const t = state.data;
  if (!t || state.lens === "zoom") return;
  if (state.lens === "comic") { Comic.render(t); return; }
  const byId = Object.fromEntries(t.messages.map((m) => [m.id, m]));
  const tl = $("timeline");
  const atEnd = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 60;
  tl.replaceChildren(...t.messages.map((m, i) => renderMsg(m, byId, t.messages[i - 1], t)));
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
    Object.assign(Zoom, state.data ? { level: 2, peer: state.data.peer } : { level: 0, peer: null });
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
  $("peer-chips").replaceChildren(...chips);
}

function renderNotice(t) {
  const n = $("notice");
  if (!t.key.pending) { n.hidden = true; n.replaceChildren(); return; }
  n.hidden = false;
  n.replaceChildren(el("p", {}, t.peer + "'s key changed. Their new messages are held and sending is blocked until you trust the new key."),
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
  const needs = actions.some((a) => decisionActions.includes(a));
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
  if (needs) {
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

function actionButton(a, m, t, primary) {
  let label = actionLabel[a];
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
    return dialog({
      title: "Decline this " + m.kind + "?", body: [from, quote, el("label", { for: "reason", class: "field-label" }, "Reason"), reason],
      ok: "Decline", run: () => act({ do: "decline", id: m.id, reason: reason.value }),
    });
  }
  if (a === "approve") return approvalDialog(t);
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
  $("dialog-title").textContent = title;
  $("dialog-body").replaceChildren(...body);
  $("dialog-error").textContent = "";
  const okBtn = $("dialog-ok");
  okBtn.textContent = ok;
  okBtn.disabled = !!gate;
  if (gate) gate.addEventListener("change", () => { okBtn.disabled = !gate.checked; });
  let busy = false; // a double click confirms once
  okBtn.onclick = async () => {
    if (busy) return;
    busy = true;
    okBtn.disabled = true;
    try {
      await run();
      d.close();
    } catch (e) {
      $("dialog-error").textContent = e.message;
      okBtn.disabled = !!gate && !gate.checked;
    } finally {
      busy = false;
    }
  };
  d.showModal();
  (focus || $("dialog-cancel")).focus();
}

function newConversationDialog() {
  const to = el("input", { id: "new-to", type: "text", placeholder: "person/agent, e.g. bob/desk", autocomplete: "off", spellcheck: "false" });
  const kind = el("select", { id: "new-kind" }, ["message", "question", "task"].map((k) => el("option", { value: k }, k[0].toUpperCase() + k.slice(1))));
  const body = el("textarea", { id: "new-body", rows: "4", placeholder: "What do you want to say?" });
  dialog({
    title: "New conversation",
    body: [el("label", { for: "new-to", class: "field-label" }, "To"), to,
      el("label", { for: "new-kind", class: "field-label" }, "Send as"), kind,
      el("label", { for: "new-body", class: "field-label" }, "Message"), body,
      el("p", { class: "hint" }, "A message never runs anything. A question may be answered by their responder if they approved you. A task runs only if they accept it, once or by standing permission for your key.")],
    ok: "Send",
    focus: to,
    run: async () => {
      const r = await api("/api/send", { to: to.value.trim(), kind: kind.value, body: body.value });
      announce(r.state === "queued" ? "Queued: it goes out when the server is reachable." : "Sent.");
      openThread(r.id);
    },
  });
}

// ---- composer ------------------------------------------------------------------

const kindValue = () => document.querySelector('input[name="kind"]:checked').value;

function setKind(kind) {
  for (const r of document.querySelectorAll('input[name="kind"]')) r.checked = r.value === kind;
  kindHint();
}

function setAnswering(m) {
  state.answering = m;
  $("replying").hidden = !m;
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
  if (text || state.answering) state.drafts[state.draftKey] = { text, kind: kindValue(), answering: state.answering };
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
}

// syncComposer enables what the open conversation allows. A send on its way
// keeps Send disabled, whatever refreshes meanwhile.
function syncComposer() {
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
  }
}

function kindHint() {
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
  const es = new EventSource("/events");
  es.addEventListener("change", (e) => {
    const seq = Number(e.data);
    if (seq === state.seq) return;
    const first = state.seq < 0;
    state.seq = seq;
    refetch(first);
  });
  es.onerror = () => {
    es.close();
    $("lost").hidden = false;
  };
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
  if (changed.length) announce("New activity with " + [...new Set(changed)].join(", "));
}

// ---- wiring ------------------------------------------------------------------------

document.addEventListener("DOMContentLoaded", () => {
  $("composer").addEventListener("submit", send);
  $("body").addEventListener("input", grow);
  $("body").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $("composer").requestSubmit(); }
  });
  $("kind").addEventListener("change", kindHint);
  kindHint();
  $("replying-cancel").addEventListener("click", () => setAnswering(null));
  $("review-btn").addEventListener("click", () => toggleReview());
  $("new-btn").addEventListener("click", newConversationDialog);
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("review").hidden) { toggleReview(false); $("review-btn").focus(); return; }
    // Page turning and zooming keys, unless typing or a dialog is open.
    if ($("dialog").open || e.target.closest("input, textarea, select") || e.ctrlKey || e.metaKey || e.altKey) return;
    const handled = state.lens === "zoom" ? Zoom.onKey(e)
      : state.lens === "comic" && state.data && !$("comic").hidden ? Comic.onKey(e) : false;
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
    try {
      await loadOverview();
      if (state.thread) await loadThread(false);
      $("lost").hidden = true;
      listen();
    } catch (e) { announce("Still not connected. If the daemon restarted, run agentnet ui for the new address."); }
  });
  window.addEventListener("focus", () => { // a missed change is caught when the person comes back
    if (!$("lost").hidden) return;
    refetch(false);
  });
  loadOverview().then((o) => {
    setLens(saved);
    if (state.lens !== "zoom" && !state.thread && o.threads.length && window.matchMedia("(min-width: 761px)").matches) openThread(o.threads[0].id);
  }).catch(() => { $("lost").hidden = false; });
  listen();
});
