// Checks the page's own logic (drafts, sending, dialogs) with a minimal
// stand-in DOM. Run by page_test.go when node is installed.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

class Elem {
  constructor(tag, id) {
    Object.assign(this, { tagName: tag, id: id || "", value: "", textContent: "", hidden: false, disabled: false, open: false,
      checked: false, placeholder: "", className: "", scrollHeight: 0, scrollTop: 0, clientHeight: 0,
      style: {}, dataset: {}, children: [], attrs: {} });
    this.classList = { add() {}, remove() {}, contains: () => false, toggle() {} };
  }
  // Like the browser: anything that is not a node becomes text (false,
  // null and arrays included), so stray values show up as text here too.
  append(...k) { this.children.push(...k.map(asNode)); }
  replaceChildren(...k) { this.children = k.map(asNode); }
  replaceWith() {}
  setAttribute(k, v) {
    this.attrs[k] = String(v);
    if (k === "id") { this.id = String(v); byId[this.id] = this; }
  }
  getAttribute(k) { return this.attrs[k] ?? null; }
  // Listeners are kept, so a check can click an element as a person would.
  addEventListener(ev, f) { ((this.listeners ||= {})[ev] ||= []).push(f); }
  removeEventListener() {}
  click() { for (const f of (this.listeners && this.listeners.click) || []) f({ currentTarget: this, target: this, preventDefault() {}, stopPropagation() {} }); }
  focus() {}
  scrollIntoView() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
  // Simple selectors only: tag and classes, the last part of a descendant
  // selector, alternatives separated by commas.
  querySelector(sel) {
    for (const alt of sel.split(",")) {
      const m = alt.trim().split(/\s+/).pop().match(/^([a-z]*)((?:\.[\w-]+)*)$/i);
      if (!m || (!m[1] && !m[2])) continue;
      const classes = m[2].split(".").filter(Boolean);
      const hit = (n) => n instanceof Elem && (!m[1] || n.tagName === m[1]) && classes.every((c) => n.className.split(/\s+/).includes(c));
      const find = (n) => { for (const c of n.children) { if (hit(c)) return c; const f = c instanceof Elem && find(c); if (f) return f; } return null; };
      const f = find(this);
      if (f) return f;
    }
    return null;
  }
  // Only what the page asks for: a dialog's text fields.
  querySelectorAll(sel) {
    if (sel !== "input[type=text], textarea, select") return [];
    const out = [];
    const walk = (n) => {
      if (!(n instanceof Elem)) return;
      if ((n.tagName === "input" && n.attrs.type === "text") || n.tagName === "textarea" || n.tagName === "select") out.push(n);
      n.children.forEach(walk);
    };
    this.children.forEach(walk);
    return out;
  }
  closest() { return null; }
  getBoundingClientRect() { return { left: 0, top: 0, width: 0, height: 0 }; }
  get lastElementChild() { return this.children[this.children.length - 1] || null; }
  remove() {}
  contains() { return false; }
  requestSubmit() {}
}

const asNode = (k) => (k instanceof Elem || (k && typeof k === "object" && "text" in k)) ? k : { text: String(k) };
// strayText lists text the page should never show: a value a condition left
// out, or an element list turned into text.
function strayText(n, out = []) {
  if (!(n instanceof Elem)) {
    if (/^(false|null|undefined|true)$/.test(n.text) || String(n.text).includes("[object")) out.push(n.text);
    return out;
  }
  for (const c of n.children) strayText(c, out);
  return out;
}
const hasTag = (n, tag) => n instanceof Elem && (n.tagName === tag || n.children.some((c) => hasTag(c, tag)));
const byId = {};
let appElem;
appElem = new Elem("div");
const radios = ["message", "question", "task"].map((v) => Object.assign(new Elem("input"), { value: v, checked: v === "message" }));
const document = {
  body: new Elem("body"),
  getElementById: (id) => (byId[id] ||= new Elem("div", id)),
  createElement: (tag) => new Elem(tag),
  createElementNS: (ns, tag) => new Elem(tag),
  createTextNode: (text) => ({ text }),
  querySelector: (sel) => (sel === 'input[name="kind"]:checked' ? radios.find((r) => r.checked) : sel === ".app" ? appElem : null),
  querySelectorAll: (sel) => (sel === 'input[name="kind"]' ? radios : []),
  addEventListener() {},
};

// Threads: alice's a1 and bob's b1 (b2 is a question bob asked, still open).
const msg = (id, from, body, extra) => Object.assign({ id, dir: "in", from, to: "me/laptop", kind: "message", body,
  at: "2026-09-28T12:00:00Z", author: { label: from, about: "" }, actions: null }, extra);
const threads = {
  a1: { id: "a1", peer: "alice/desk", key: { pinned: "SHA256:a" }, messages: [msg("a1", "alice/desk", "hi from alice")] },
  b1: { id: "b1", peer: "bob/desk", key: { pinned: "SHA256:b", pending: "SHA256:new-bob" }, messages: [msg("b1", "bob/desk", "hi from bob")] },
  c1: { id: "c1", peer: "carol/ci", key: { pinned: "SHA256:c" },
    messages: [msg("c1", "carol/ci", "which port?", { kind: "question", state: "held", actions: ["reply", "accept", "approve", "decline"] })] },
};
const overview = { demo: false, me: { address: "me/laptop", fingerprint: "SHA256:me" }, threads: [], review: [], quarantine: [], seq: 0 };

// fetch answers at once, except the paths listed in hold: those wait for
// release(path) so a test can act while a request is on its way. While
// down is set, every request fails as if the daemon were gone.
let down = false;
let refreshReply = { text: "Connection unknown" }; // the one check made when a thread opens
const dmThreads = {}; // DMs by id, for /api/dm
const calls = [];
const hold = {};
const held = {};
function release(p) { const r = held[p].shift(); r(); }
function fetch(url, opts) {
  if (down) return Promise.reject(new TypeError("Failed to fetch"));
  const u = new URL(url, "http://127.0.0.1");
  const body = opts && opts.body ? JSON.parse(opts.body) : undefined;
  calls.push({ path: u.pathname, body });
  let data = {};
  if (u.pathname === "/api/thread") data = threads[u.searchParams.get("id")];
  else if (u.pathname === "/api/overview") data = Object.assign({ version: serving }, overview);
  else if (u.pathname === "/api/send") data = { id: "new", state: "delivered" };
  else if (u.pathname === "/api/refresh") data = refreshReply;
  else if (u.pathname === "/api/dm") data = dmThreads[u.searchParams.get("id")] || {};
  else if (u.pathname === "/api/dm/send") data = { id: "sent-dm", state: "custody" };
  else if (u.pathname === "/api/dm/new") data = { id: "d3" };
  else if (u.pathname === "/api/person") data = { person: { person: "p-me", label: body.label, address: "me/laptop", state: "self" }, note: "Your person is set up." };
  const resp = { ok: true, json: async () => JSON.parse(JSON.stringify(data)), text: async () => "" };
  if (hold[u.pathname]) return new Promise((res) => { (held[u.pathname] ||= []).push(() => res(resp)); });
  return Promise.resolve(resp);
}

let serving = "v1"; // the version the daemon reports
const streams = [];  // every event stream the page opened
class FakeEventSource {
  constructor() { this.handlers = {}; this.closed = false; streams.push(this); }
  addEventListener(ev, f) { this.handlers[ev] = f; }
  close() { this.closed = true; }
  fire(ev) { if (ev === "error") this.onerror(); else this.handlers[ev]({ data: "" }); }
}
const store = new Map();
let storageBroken = false;
const sessionStorage = {
  getItem: (k) => { if (storageBroken) throw new Error("denied"); return store.has(k) ? store.get(k) : null; },
  setItem: (k, v) => { if (storageBroken) throw new Error("denied"); store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
};
let reloads = 0;
const ctx = vm.createContext({
  document, fetch, console, setTimeout, clearTimeout, URL, sessionStorage,
  window: { innerHeight: 800, matchMedia: () => ({ matches: false }), addEventListener() {} },
  localStorage: { getItem: () => null, setItem() {} },
  location: { reload() { reloads++; } },
  EventSource: FakeEventSource,
});
for (const f of ["lenses.js", "app.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), ctx, { filename: f });

const run = (code) => vm.runInContext(code, ctx);
const $ = (id) => document.getElementById(id);
const tick = () => new Promise((r) => setTimeout(r, 0));
const pause = (ms) => new Promise((r) => setTimeout(r, ms));
const sends = () => calls.filter((c) => c.path === "/api/send" || (c.path === "/api/act" && c.body.do !== "read"));
let failed = 0;
function check(ok, what) {
  if (!ok) { failed++; console.error("FAIL: " + what); }
}
const ev = { preventDefault() {} };

(async () => {
  // R1: a draft stays with its conversation.
  await run('openThread("a1")');
  $("body").value = "for alice";
  await run('openThread("b1")');
  check($("body").value === "" && run("state.data.peer") === "bob/desk", "switching conversation keeps alice's draft out of bob's");
  await run('openThread("a1")');
  check($("body").value === "for alice", "alice's draft comes back");

  // R1: while another conversation loads, nothing can be sent anywhere.
  hold["/api/thread"] = true;
  const opening = run('openThread("c1")');
  $("body").value = "typed during the switch";
  calls.length = 0;
  await run("send")(ev);
  check(sends().length === 0 && $("send").disabled, "no send while the next conversation loads");
  hold["/api/thread"] = false;
  release("/api/thread");
  await opening;
  check(run("state.data.peer") === "carol/ci", "carol's conversation loaded");

  // R2: one send, however often it is asked for; a refresh keeps Send off.
  $("body").value = "once";
  hold["/api/send"] = true;
  calls.length = 0;
  const first = run("send")(ev);
  run("send")(ev);
  await tick();
  check(sends().length === 1, "two submits send once (" + sends().length + ")");
  await run("loadThread(false)"); // a pushed change while the send is on its way
  check($("send").disabled, "Send stays disabled across a refresh");
  // R1: text typed in another conversation meanwhile survives the late reply.
  await run('openThread("a1")');
  $("body").value = "newer text for alice";
  hold["/api/send"] = false;
  release("/api/send");
  await first;
  const sent = sends()[0].body;
  check(sent.to === "carol/ci" && sent.body === "once", "the send went where and what it was written: " + JSON.stringify(sent));
  check($("body").value === "newer text for alice" && !$("send").disabled, "a late send clears nothing in another conversation");
  await run('openThread("c1")');
  check($("body").value === "", "the sent draft is gone from its own conversation");

  // R1: answering mode stays with its conversation too.
  const q = run("state.data.messages[0]");
  run("setAnswering")(q);
  $("body").value = "port 8443";
  await run('openThread("a1")');
  check(run("state.answering") === null, "answering does not follow into another conversation");
  await run('openThread("c1")');
  check(run("state.answering && state.answering.id") === "c1" && $("body").value === "port 8443", "answering comes back with its draft");

  // A draft keeps its kind; another conversation starts as a plain message.
  const choose = (k) => radios.forEach((r) => { r.checked = r.value === k; });
  const kind = () => radios.find((r) => r.checked).value;
  await run('openThread("a1")');
  $("body").value = "just a note";
  choose("message");
  await run('openThread("c1")');
  $("body").value = "please run the tests";
  choose("task");
  await run('openThread("a1")');
  check(kind() === "message" && $("body").value === "just a note", "alice's draft comes back as a message, not bob's task");
  await run('openThread("c1")');
  check(kind() === "task" && $("body").value === "please run the tests", "carol's task draft comes back as a task");
  run("setAnswering")(null);
  $("body").value = "";
  choose("message");

  // Zoom switches conversation through the same guard.
  await run('openThread("a1")');
  $("body").value = "alice again";
  hold["/api/thread"] = true;
  const zooming = run('Zoom.go(2, { peer: "carol/ci", thread: "c1" })');
  calls.length = 0;
  await run("send")(ev);
  check(sends().length === 0 && run("state.data") === null, "no send to alice while Zoom opens carol");
  hold["/api/thread"] = false;
  release("/api/thread");
  await zooming;
  check(run("state.data.peer") === "carol/ci" && $("body").value === "", "Zoom opened carol with her own (empty) draft");
  await run('openThread("a1")');
  check($("body").value === "alice again", "alice's draft survived the Zoom switch");

  // Dialogs confirm once.
  let runs = 0;
  run("dialog")({ title: "t", body: [], ok: "OK", run: async () => { runs++; await tick(); } });
  $("dialog-ok").onclick();
  $("dialog-ok").onclick();
  await tick(); await tick();
  check(runs === 1, "a double click confirms once (" + runs + ")");

  // R3: trusting names the key the dialog showed.
  await run('openThread("b1")');
  run("trustDialog")(run("state.data"));
  calls.length = 0;
  await $("dialog-ok").onclick();
  const trust = calls.find((c) => c.path === "/api/act");
  check(trust && trust.body.do === "trust" && trust.body.key === "SHA256:new-bob", "trust carries the compared key: " + JSON.stringify(trust && trust.body));

  // N1: 30 unlinked messages and one reply chain from one address are one
  // contact; the chain stays its own conversation, the singles stay single,
  // reports stay apart, and each thread appears exactly once.
  const sum = (id, peer, extra) => Object.assign({ id, peer, title: "t " + id, last: "l " + id, last_at: "2026-09-28T10:00:00Z",
    count: 1, review: 0, unread: 0, running: 0, waiting: false, key_changed: false, notices: 0, notice_only: false }, extra);
  const zen = [];
  for (let i = 0; i < 30; i++) zen.push(sum("z" + i, "admin/zenbook", { title: "announcement " + i, unread: i < 3 ? 1 : 0, last_at: "2026-09-28T09:" + String(i).padStart(2, "0") + ":00Z" }));
  zen.push(sum("chain", "admin/zenbook", { title: "AgentNet rollout", count: 3, last_at: "2026-09-28T11:00:00Z" }));
  zen.push(sum("held", "admin/zenbook", { title: "which port?", review: 1 }));
  const reports = [sum("n1", "hub/ops", { title: "2 request(s) wait for a person's decision on hub/ops. Review there: agentnet inbox --review", notices: 1, notice_only: true }),
    sum("n2", "hub/ops", { title: "1 request(s) wait", notices: 1, notice_only: true, last_at: "2026-09-28T08:00:00Z" })];
  const laptop = [sum("l1", "admin/laptop", { title: "same person label, other agent" })];
  const threads = [...zen, ...reports, ...laptop];
  const contacts = run("contactsOf")(threads);
  const zc = contacts.find((c) => c.peer === "admin/zenbook");
  check(contacts.length === 3 && contacts.filter((c) => c.peer.startsWith("admin/")).length === 2, "one contact per exact address (admin/laptop apart from admin/zenbook)");
  check(zc.conversations.map((t) => t.id).sort().join() === "chain,held" && zc.singles.length === 30 && zc.reports.length === 0,
    "chain and open item listed as conversations, 30 singles kept single");
  const seen = contacts.flatMap((c) => [...c.conversations, ...c.singles, ...c.reports]).map((t) => t.id);
  check(seen.length === threads.length && new Set(seen).size === threads.length, "every thread reachable exactly once");
  check(zc.review === 1 && zc.unread === 3 && zc.notices === 0, "zenbook counts: 1 decision, 3 unread");
  const oc = contacts.find((c) => c.peer === "hub/ops");
  check(oc.review === 0 && oc.unread === 0 && oc.notices === 2 && oc.reports.length === 2, "reports counted apart from decisions and unread");

  // N2: search finds known agents and conversations, says which is which,
  // opens the exact one, and never offers people or reports.
  let r = run("searchKnown")("zenbook", threads);
  check(r.agents.length === 1 && r.agents[0].peer === "admin/zenbook" && r.conversations.length === 0, "search by address finds the agent");
  r = run("searchKnown")("admin", threads);
  check(r.agents.map((c) => c.peer).sort().join() === "admin/laptop,admin/zenbook", "a shared label finds both exact addresses, not one person");
  r = run("searchKnown")("rollout", threads);
  check(r.conversations.length === 1 && r.conversations[0].id === "chain", "search finds the conversation by its line");
  check(run("searchKnown")("request", threads).conversations.length === 0, "reports are not search results");
  check(run("searchKnown")("   ", threads).agents.length === 0, "an empty search shows nothing");
  overview.threads = threads;
  overview.review = [{ id: "held", peer: "admin/zenbook", kind: "question", why: "", excerpt: "" },
    { id: "n1", peer: "hub/ops", kind: "message", why: "", excerpt: "", notice: true },
    { id: "n2", peer: "hub/ops", kind: "message", why: "", excerpt: "", notice: true },
    { id: "n3", peer: "carol/ci", kind: "message", why: "", excerpt: "", notice: true }];
  threads.push(Object.assign({}, threads.find((t) => t.id === "chain"), { id: "c1", peer: "carol/ci", title: "which port?" }));
  await run("loadOverview()");
  $("search").value = "which port";
  run("state").query = "which port";
  run("rerenderContacts")();
  calls.length = 0;
  const opened = run("searchKnown")("which port", threads).conversations.find((t) => t.peer === "carol/ci");
  await run("openThread")(opened.id);
  check(calls.some((c) => c.path === "/api/thread") && run("state.data.peer") === "carol/ci" && run("state.thread") === "c1",
    "a search result opens exactly that conversation");
  run("clearSearch")();

  // N3: decisions and reports are counted apart; dismissing touches only
  // that sender's reports and only resolves them.
  run("renderReview")(overview.review);
  check(String($("review-count").textContent) === "1" && $("review-reports").textContent === "3 reports" && !$("review-reports").hidden,
    "amber counts decisions only; reports shown apart: " + [$("review-count").textContent, $("review-reports").textContent, $("review-reports").hidden].join("/"));
  calls.length = 0;
  await run("dismissReports")("hub/ops");
  const acts = calls.filter((c) => c.path === "/api/act").map((c) => c.body);
  check(acts.length === 2 && acts.every((b) => b.do === "resolve") && acts.map((b) => b.id).sort().join() === "n1,n2",
    "dismiss resolves exactly that sender's reports: " + JSON.stringify(acts));
  check(!calls.some((c) => c.path === "/api/send"), "dismissing sends nothing");

  // Nothing a condition leaves out reaches the page as text: search with
  // only conversation matches, the Zoom write dialog (and its kind picker),
  // a read dialog without a summary, and the contact list.
  overview.threads = threads;
  await run("loadOverview()");
  run("state").query = "rollout";
  run("rerenderContacts")();
  check(strayText($("conv-list")).length === 0, "search results show no stray text: " + strayText($("conv-list")));
  run("clearSearch")();
  check(strayText($("conv-list")).length === 0, "contact list shows no stray text: " + strayText($("conv-list")));
  await run('openThread("a1")');
  run("writeDialog")(run("state.data"), null);
  check(strayText($("dialog-body")).length === 0 && hasTag($("dialog-body"), "select"),
    "the write dialog shows its kind picker and no stray text: " + strayText($("dialog-body")));
  run("writeDialog")(run("state.data"), run("state.data.messages[0]"));
  check(strayText($("dialog-body")).length === 0, "the answer dialog shows no stray text: " + strayText($("dialog-body")));
  run("readDialog")(run("state.data.messages[0]"));
  check(strayText($("dialog-body")).length === 0, "the read dialog shows no stray text: " + strayText($("dialog-body")));

  // Only the exact stored shape is a report; a reply or files make it a
  // normal message.
  const rep = { dir: "in", kind: "message", status: "review_notice", reply_to: "", files: null };
  check(run("isReport")(rep) && !run("isReport")(Object.assign({}, rep, { reply_to: "x" })) &&
    !run("isReport")(Object.assign({}, rep, { files: [{ name: "f" }] })) && !run("isReport")(Object.assign({}, rep, { kind: "answer" })),
    "report shape is exact");

  // A report that got a reply sits in a conversation and still counts and
  // can be dismissed.
  const mixed = [sum("mix", "hub/ops", { title: "1 request(s) wait", count: 2, notices: 1 }),
    sum("n9", "hub/ops", { title: "old report", notice_only: true, last_at: "2026-09-28T07:00:00Z" })];
  overview.threads = mixed;
  overview.review = [{ id: "mix", peer: "hub/ops", kind: "message", why: "", excerpt: "1 request(s) wait", at: "2026-09-28T10:00:00Z", notice: true }];
  await run("loadOverview()");
  const mc = run("contactsOf")(mixed)[0];
  check(mc.notices === 1 && mc.conversations.some((t) => t.id === "mix") && mc.reports.length === 1,
    "a report with a reply is a conversation and still counted");
  const line = run("reportLine")(mc);
  check(line && strayText(line).length === 0 && JSON.stringify(line).includes("Dismiss 1 report"), "the report line counts it");
  calls.length = 0;
  await run("dismissReports")("hub/ops");
  check(calls.filter((c) => c.path === "/api/act").map((c) => c.body.id).join() === "mix", "dismiss reaches the report in a conversation");

  // Update switch: after the restart notice the page reconnects on its own
  // for a bounded time; a missed notice gets a couple of quick tries; if the
  // daemon never returns, the page says so and stops trying.
  overview.threads = threads; overview.review = [];
  run("recovery").update = [0, 0, 0]; run("recovery").missed = [0, 0];
  run("listen")();
  let es = streams[streams.length - 1];
  down = true;
  calls.length = 0;
  es.fire("restart");
  await tick(); await tick();
  check(!$("updating").hidden && es.closed, "the restart notice shows that AgentNet is switching");
  down = false;
  await pause(20);
  check(streams.length >= 2 && !streams[streams.length - 1].closed && $("updating").hidden && $("lost").hidden,
    "the page reconnected to the restarted daemon");
  es = streams[streams.length - 1];
  down = true;
  es.fire("error");
  down = false;
  await pause(20);
  check(!streams[streams.length - 1].closed && $("lost").hidden && streams[streams.length - 1] !== es, "a missed restart notice still reconnects");
  es = streams[streams.length - 1];
  down = true;
  calls.length = 0;
  const before = streams.length;
  es.fire("restart");
  await pause(40);
  check(streams.length === before && !$("lost").hidden && $("updating").hidden, "a daemon that does not return: the page says so and stops");
  down = false;
  await run("reconnect")();

  // A new version: every unsent text is kept across the reload, including
  // an open dialog's fields but never its consent boxes; nothing reloads
  // while a send is on its way; without storage the page asks instead.
  await run('openThread("a1")');
  $("body").value = "unsent to alice";
  run("state").drafts["c1"] = { text: "carol draft", kind: "task", answering: null };
  run("writeDialog")(run("state.data"), null);
  byId["write-body"].value = "zoom text";
  byId["write-kind"].value = "question";
  run("state").sending = true;
  serving = "v2";
  reloads = 0;
  await run("loadOverview()");
  check(reloads === 0, "no reload while a send is on its way");
  run("state").sending = false;
  run("updated")(run("state.newVersion"));
  check(reloads === 1 && store.has("agentnet-reload"), "reloads once the send is done, with unsent text kept");
  // What the reloaded page gets back.
  run("state").drafts = {}; run("state").thread = null; run("state").data = null; run("state").draftKey = null;
  run("state").version = ""; run("state").newVersion = ""; // a fresh page learns its version again
  $("body").value = ""; $("dialog").open = false;
  await run("restoreAfterReload()");
  check($("body").value === "unsent to alice" && run("state.drafts")["c1"].text === "carol draft" && run("state.drafts")["c1"].kind === "task",
    "drafts are back after the reload");
  check($("dialog").open && byId["write-body"].value === "zoom text" && byId["write-kind"].value === "question",
    "the open dialog is back with its text: " + byId["write-body"].value);
  check(!store.has("agentnet-reload"), "kept text is restored once");
  run("newConversationDialog")("admin/zenbook");
  byId["new-body"].value = "new conversation text";
  storageBroken = true;
  reloads = 0;
  run("updated")("v3");
  check(reloads === 0 && !$("reload").hidden && !$("updating").hidden, "without storage the page asks instead of reloading");
  storageBroken = false;

  // Directory: who the server lists, found without any history; presence
  // only while the server's view is current; choosing opens, never sends.
  const textOf = (n) => n instanceof Object && n.children ? n.children.map(textOf).join(" ") : String((n && n.text) || "");
  overview.threads = [sum("t1", "bob/desk", { title: "port?", count: 2 })];
  overview.review = [];
  overview.directory = { status: "listed", current: true, at: "2026-09-28T12:00:00Z", truncated: false, members: [
    { address: "vitalii/laptop", presence: "connected", joined: "2026-09-28T11:59:00Z" },
    { address: "bob/desk", presence: "reconnecting", joined: "2026-09-01T00:00:00Z" },
  ] };
  await run("loadOverview()");
  let found = run("searchKnown")("vitalii", overview.threads, run("directory")());
  check(found.listed.length === 1 && found.listed[0].address === "vitalii/laptop" && found.agents.length === 0,
    "search finds someone the server lists, with no conversation yet");
  found = run("searchKnown")("bob", overview.threads, run("directory")());
  check(found.agents.length === 1 && found.listed.length === 0, "a contact is not listed twice");
  check(run("presenceOf")("vitalii/laptop") === "online" && run("presenceOf")("bob/desk") === "reconnecting", "presence while current");
  const section = run("directorySection")(overview.threads);
  check(section.length > 0 && JSON.stringify(section).includes("vitalii") && !JSON.stringify(section).includes('"bob/desk"'),
    "the directory lists only agents without a conversation");
  calls.length = 0;
  run("chooseMember")("vitalii/laptop");
  check($("dialog").open && byId["new-to"] && byId["new-to"].value === "vitalii/laptop" && !calls.some((c) => c.path === "/api/send" || c.path === "/api/act"),
    "choosing a new agent opens a new conversation to them and sends nothing");
  $("dialog").open = false;
  run("chooseMember")("bob/desk");
  check(run("state.expanded") === "bob/desk" && !$("dialog").open, "choosing a contact opens the contact");
  // Not current: presence is not said at all.
  overview.directory = Object.assign({}, overview.directory, { current: false,
    members: overview.directory.members.map((m) => Object.assign({}, m, { presence: "" })) });
  await run("loadOverview()");
  check(run("presenceOf")("vitalii/laptop") === null, "no presence without the server");
  const note = run("directoryNote")(run("directory")());
  check(note.includes("not known") && !/\bonline\b(?! is not known)/.test(note.replace("who is online is not known", "")), "not current is said plainly: " + note);
  check(!/connected/i.test(note), "not current does not claim a disconnection (the stream may be open): " + note);
  check(run("directoryNote")({ status: "not_listed", members: [] }).includes("older AgentNet"), "an older server is explained");
  check(run("directoryNote")({ status: "listed", current: true, at: "2026-09-28T12:00:00Z", truncated: true, members: [] }).includes("1,000"), "a truncated list is said");
  check(run("directoryNote")({ status: "unknown", members: [] }).includes("not known yet"), "unknown before the first list");
  check(run("directoryNote")({ status: "listed", current: false, members: [] }).includes("not known yet"), "listed but no list yet: no time is made up");
  check(run("zoomDirectory")(overview.threads) !== null, "Zoom shows the same directory");

  // The open conversation's header says what the list says: the check made
  // on opening (connected) never outlives a pushed change or a list that is
  // no longer current.
  const listed = (presence, current) => ({ status: "listed", current, at: "2026-09-28T12:00:00Z", truncated: false,
    members: [{ address: "bob/desk", presence: current ? presence : "", joined: "2026-09-01T00:00:00Z" }] });
  overview.threads = [sum("b1", "bob/desk")];
  overview.directory = listed("connected", true);
  refreshReply = { text: "Their computer is connected", at: "2026-09-28T12:00:00Z" };
  await run("loadOverview()");
  await run('openThread("b1")');
  await new Promise((r) => setTimeout(r, 0));
  check($("conv-presence").textContent === "Their computer is connected" && run("presenceOf")("bob/desk") === "online",
    "connected in header and list: " + $("conv-presence").textContent);
  overview.directory = listed("offline", true);
  await run("refetch()");
  check($("conv-presence").textContent === "Their computer is offline" && run("presenceOf")("bob/desk") === "offline",
    "a pushed offline reaches header and list: " + $("conv-presence").textContent);
  overview.directory = listed("", false);
  await run("refetch()");
  check($("conv-presence").textContent === "Connection not known now" && run("presenceOf")("bob/desk") === null,
    "a list that is not current: header and list say nothing live: " + $("conv-presence").textContent);
  // An older server lists no one: only the check, with its time.
  overview.directory = { status: "not_listed", current: false, members: [] };
  await run("refetch()");
  const checked = $("conv-presence").textContent;
  check(checked.startsWith("Checked ") && checked.endsWith(": their computer is connected"), "an older server: the check, with its time: " + checked);
  refreshReply = { text: "Connection unknown" };
  run("state.presence = {}");
  await run('openThread("a1")');
  await run('openThread("b1")');
  await new Promise((r) => setTimeout(r, 0));
  check($("conv-presence").textContent === "Connection unknown", "no answer: unknown, no time: " + $("conv-presence").textContent);
  delete overview.directory;

  // Human DMs: a person exists only when set up by hand; each DM with the
  // same person is separate; a DM sends only DM messages; a held question
  // offers nothing to run; a frozen DM sends nothing; device history stays
  // apart.
  const T = "2026-09-28T12:00:00Z";
  const alicePerson = { person: "p-alice", label: "Alice", address: "alice/desk", state: "pinned" };
  const dmsg = (id, dir, body, extra) => Object.assign({ id, dir, from: dir === "in" ? "alice/desk" : "me/laptop", kind: "message", body,
    origin: "ui", state: "", state_text: "", at: T }, extra);
  Object.assign(overview, { threads: [], review: [], persons: true, person: null, people: [alicePerson], dms: [] });
  calls.length = 0;
  await run("loadOverview()");
  let side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("Set up your person") && !side.includes("Alice"), "without a person the page offers setup and no DMs: " + side);
  check(!calls.some((c) => c.path === "/api/person"), "nothing sets up a person by itself");
  run("personDialog()");
  byId["person-name"].value = "Sergey";
  check(!calls.some((c) => c.path === "/api/person"), "the setup dialog waits for its button");
  await $("dialog-ok").onclick();
  const made = calls.filter((c) => c.path === "/api/person");
  check(made.length === 1 && made[0].body.label === "Sergey", "one person is set up, with the name typed");
  overview.person = { person: "p-me", label: "Sergey", address: "me/laptop", state: "self", published: true };
  overview.dms = [
    { id: "d2", peer: alicePerson, created: T, mine: true, count: 2, title: "budget", last: "can you check?", last_at: T, unread: 1, held: 1, waiting: 1 },
    { id: "d1", peer: alicePerson, created: T, mine: true, count: 1, title: "deploy", last: "deploy", last_at: T, unread: 0, held: 0, waiting: 0 },
  ];
  dmThreads.d1 = { id: "d1", peer: alicePerson, created: T, mine: true, messages: [dmsg("m1", "out", "deploy", { state: "delivered", state_text: "Delivered to alice/desk" })] };
  dmThreads.d2 = { id: "d2", peer: alicePerson, created: T, mine: true, messages: [
    dmsg("m2", "out", "budget", { state: "waiting", state_text: "Kept here, not sent yet: alice/desk cannot read conversations now" }),
    dmsg("m3", "in", "can you check?", { kind: "question", state: "conv_held", state_text: "Held for you: nothing runs it. Answer here if you want to.", unread: true })] };
  dmThreads.d3 = { id: "d3", peer: { person: "p-vit", label: "Vitalii", address: "vitalii/laptop", state: "pinned" }, created: T, mine: true, messages: [] };
  run('state.personOpen["p-alice"] = true');
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("2 DMs") && side.includes("budget") && side.includes("deploy") && side.includes("checked against their computer"),
    "two DMs with one person are listed apart, with how the person is known: " + side);
  calls.length = 0;
  await run('openDM("d2")');
  check(calls.some((c) => c.path === "/api/dm") && !calls.some((c) => c.path === "/api/thread"), "a DM opens through the DM API");
  check($("kind").hidden === true && $("body").placeholder === "Write to Alice", "a DM's composer writes messages to the person");
  let tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("Held for you") && tl.includes("Kept here, not sent yet") && !/Accept|responder answer|Decline/.test(tl),
    "a held DM question offers nothing to run, a kept message says so: " + tl);
  check(calls.some((c) => c.path === "/api/act" && c.body.do === "read" && c.body.ids.includes("m3")), "opening a DM marks its new message read");
  check($("timeline").children.flatMap((c) => strayText(c)).length === 0, "no stray text in a DM");
  $("body").value = "on budget";
  calls.length = 0;
  await run("send")(ev);
  const dmSends = calls.filter((c) => c.path === "/api/dm/send");
  check(dmSends.length === 1 && dmSends[0].body.conv === "d2" && dmSends[0].body.body === "on budget" && sends().length === 0,
    "a DM message goes to its DM only, never as an older message");
  $("body").value = "for d2 later";
  await run('openDM("d1")');
  check($("body").value === "", "another DM with the same person starts with its own empty draft");
  await run('openDM("d2")');
  check($("body").value === "for d2 later", "each DM keeps its own draft");
  dmThreads.d1.frozen = "Frozen: a different person record was published.";
  await run('openDM("d1")');
  check($("send").disabled && $("body").disabled && !$("notice").hidden, "a frozen DM sends nothing");
  calls.length = 0;
  run('newDMDialog({ label: "Vitalii", address: "vitalii/laptop", state: "listed" })');
  check(!calls.some((c) => c.path === "/api/dm/new") && JSON.stringify($("dialog-body").children.map(textOf)).includes("checked against their computer"),
    "starting a DM with a listed person says what is checked, and waits");
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/dm/new" && c.body.address === "vitalii/laptop") && run("state.dm") === "d3",
    "confirming starts a new DM with that device's person and opens it");
  await run('openThread("a1")');
  check(run("state.dm") === null && $("kind").hidden === false && run("state.data.peer") === "alice/desk",
    "device history opens apart from DMs, with its own composer");

  // Search finds people by the name they give or their device, and DMs by
  // their lines or the person; each result says its kind and opens exactly it.
  overview.people = [alicePerson, { label: "Vitalii", address: "vitalii/laptop", state: "listed" }];
  delete dmThreads.d1.frozen;
  await run("loadOverview()");
  const search = (q) => run("searchKnown")(q, overview.threads, run("directory")());
  found = search("vitalii");
  check(found.people.length === 1 && found.people[0].state === "listed" && found.dms.length === 0, "search finds a listed person by name");
  found = search("alice/desk");
  check(found.people.length === 1 && found.people[0].person === "p-alice", "search finds a person by their device");
  found = search("budget");
  check(found.dms.length === 1 && found.dms[0].id === "d2" && found.people.length === 0, "search finds a DM by its lines");
  found = search("alice");
  check(found.people.length === 1 && found.dms.length === 2, "the person and each of their DMs are separate results");
  run('state.query = "vit"');
  run("renderSearch")(overview.threads);
  const res = JSON.stringify($("conv-list").children.map(textOf));
  check(res.includes("Person") && res.includes("Vitalii") && res.includes("not checked yet"), "a person result says its kind and how it is known: " + res);
  run("choosePerson")(overview.people[1]);
  check(run("state.query") === "" && run('state.personOpen["listed:vitalii/laptop"]') === true, "choosing a person opens their row, nothing else");

  // Comic draws a DM as an issue: the person's name, captions for held and
  // kept messages, no decisions.
  run('setLens("comic")');
  await run('openDM("d2")');
  let comic = JSON.stringify($("comic").children.map(textOf));
  check($("timeline").hidden && !$("comic").hidden && comic.includes("You → Alice") && comic.includes("can you check?") &&
    comic.includes("Held for you") && comic.includes("Kept here") && !/Accept|Decline|NEEDS YOU/.test(comic),
    "a DM is a comic page with the person's name, held and kept captions, nothing to run: " + comic.slice(0, 300));
  run("Comic.turn(-1)");
  comic = JSON.stringify($("comic").children.map(textOf));
  check(comic.includes("DM · Alice via alice/desk") && comic.includes("1 held for you"), "the DM's cover names the person and the device: " + comic.slice(0, 300));
  await run('openDM("d3")');
  check(JSON.stringify($("comic").children.map(textOf)).includes("no messages yet"), "an empty DM has a cover, not an error");

  // Zoom: people apart from devices, then a person's DMs, then one DM.
  run('setLens("zoom")');
  run("Zoom.go(0, {})");
  let zoom = JSON.stringify($("zoom").children.map(textOf));
  check(zoom.includes("People") && zoom.includes("Alice") && zoom.includes("Vitalii") && zoom.includes("Devices"), "Zoom shows people apart from devices: " + zoom.slice(0, 300));
  await run('Zoom.go(1, { person: "p-alice", peer: null })');
  zoom = JSON.stringify($("zoom").children.map(textOf));
  check(zoom.includes("budget") && zoom.includes("deploy") && zoom.includes("New DM with Alice"), "a person in Zoom holds their separate DMs");
  await run('Zoom.go(2, { dm: "d2" })');
  zoom = JSON.stringify($("zoom").children.map(textOf));
  check(run("state.dm") === "d2" && zoom.includes("Held for you") && zoom.includes("Write in this DM"), "a DM in Zoom, with its held question and nothing to run");
  calls.length = 0;
  run("dmWriteDialog(state.dmData)");
  byId["write-body"].value = "from zoom";
  await $("dialog-ok").onclick();
  const zs = calls.filter((c) => c.path === "/api/dm/send");
  check(zs.length === 1 && zs[0].body.conv === "d2" && zs[0].body.body === "from zoom" && sends().length === 0, "writing in Zoom goes to that DM only");
  dmThreads.d1.frozen = "Frozen.";
  await run('Zoom.go(2, { dm: "d1" })');
  const zoomWrite = (n) => n instanceof Object && n.children ? (n.tagName === "button" && /Nothing more can be sent/.test(textOf(n)) ? n : n.children.map(zoomWrite).find(Boolean)) : null;
  const frozenBtn = $("zoom").children.map(zoomWrite).find(Boolean);
  check(frozenBtn && frozenBtn.attrs.disabled !== undefined, "a frozen DM in Zoom offers no writing");
  await run('Zoom.go(2, { thread: "a1" })');
  check(run("Zoom.person") === null && run("state.dm") === null, "a device conversation in Zoom leaves the person path");

  // Search opens a DM where it is: from Zoom, Zoom goes to that DM, also
  // another person's.
  const find = (root, pred) => {
    for (const c of root.children || []) {
      if (c instanceof Object && c.tagName && pred(c)) return c;
      const f = c instanceof Object && c.children ? find(c, pred) : null;
      if (f) return f;
    }
    return null;
  };
  const bobPerson = { person: "p-bob", label: "Bob", address: "bob/desk", state: "pinned" };
  overview.people = [alicePerson, bobPerson];
  overview.dms = [...overview.dms, { id: "d4", peer: bobPerson, created: T, mine: false, count: 1, title: "lunch plans", last: "lunch plans", last_at: T, unread: 0, held: 0, waiting: 0 }];
  dmThreads.d4 = { id: "d4", peer: bobPerson, created: T, mine: false, messages: [dmsg("m9", "in", "lunch plans", { from: "bob/desk" })] };
  delete dmThreads.d1.frozen;
  await run("loadOverview()");
  run('setLens("zoom")');
  await run('Zoom.go(2, { dm: "d2" })');
  run('Zoom.query = "lunch"; Zoom.refresh()');
  let hit = find($("zoom"), (n) => n.className === "result" && textOf(n).includes("lunch plans"));
  check(hit && textOf(hit).includes("DM"), "Zoom's search lists the DM with its kind");
  hit.click();
  await pause(20);
  check(run("state.dm") === "d4" && run("Zoom.level") === 2 && run("Zoom.person") === "p-bob" && run("Zoom.query") === "",
    "choosing a DM in Zoom's search zooms to that DM of another person: " + [run("state.dm"), run("Zoom.level"), run("Zoom.person")]);
  run('state.query = "budget"');
  run("renderSearch")(overview.threads);
  hit = find($("conv-list"), (n) => n.className === "result" && textOf(n).includes("budget"));
  hit.click();
  await pause(20);
  check(run("state.dm") === "d2" && run("Zoom.level") === 2 && run("Zoom.person") === "p-alice", "a DM result in Zoom's lens zooms to it (sidebar search)");
  run('Zoom.query = "alice"; Zoom.refresh()');
  find($("zoom"), (n) => n.className === "result" && textOf(n).includes("Person")).click();
  await pause(20);
  check(run("Zoom.level") === 1 && run("Zoom.person") === "p-alice", "a person result in Zoom zooms to that person");
  run('setLens("classic")');

  // Replying to one message of a DM: a visible target that can be
  // cancelled, kept with that DM's draft, sent as that DM's reply only.
  await run('openDM("d2")');
  const replyBtn = find($("timeline"), (n) => n.tagName === "button" && textOf(n) === "Reply" && true);
  const m3Item = find($("timeline"), (n) => n.id === "m-m3");
  find(m3Item, (n) => n.tagName === "button" && textOf(n) === "Reply").click();
  check(!$("replying").hidden && $("replying-label").textContent === "Replying to" && $("replying-text").textContent === "can you check?",
    "choosing Reply shows which message the reply is to");
  $("body").value = "yes, on it";
  await run('openDM("d1")');
  check($("replying").hidden && run("state.dmReply") === null, "another DM does not inherit the reply target");
  calls.length = 0;
  $("body").value = "unrelated";
  await run("send")(ev);
  check(calls.find((c) => c.path === "/api/dm/send").body.reply_to === "", "a message in another DM replies to nothing");
  await run('openDM("d2")');
  check(!$("replying").hidden && run("state.dmReply.id") === "m3" && $("body").value === "yes, on it", "the reply target comes back with that DM's draft");
  calls.length = 0;
  await run("send")(ev);
  const sentReply = calls.find((c) => c.path === "/api/dm/send");
  check(sentReply && sentReply.body.reply_to === "m3" && sentReply.body.conv === "d2" && $("replying").hidden, "the reply goes to that DM, to that message, and the target clears");
  find(find($("timeline"), (n) => n.id === "m-m2"), (n) => n.tagName === "button" && textOf(n) === "Reply").click();
  run("setDMReply(null)");
  check($("replying").hidden && run("state.dmReply") === null, "a reply target can be cancelled");
  check(replyBtn !== null, "DM messages offer Reply");

  run('setLens("classic")');
  Object.assign(overview, { persons: false, person: null, people: [], dms: [] });

  if (failed) process.exit(1);
  console.log("page logic ok");
})().catch((e) => { console.error(e); process.exit(1); });
