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
  addEventListener() {}
  removeEventListener() {}
  focus() {}
  scrollIntoView() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
  querySelector() { return null; }
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
  else if (u.pathname === "/api/refresh") data = { text: "Connection unknown" };
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
  check(run("directoryNote")({ status: "not_listed", members: [] }).includes("older AgentNet"), "an older server is explained");
  check(run("directoryNote")({ status: "listed", current: true, truncated: true, members: [] }).includes("1,000"), "a truncated list is said");
  check(run("directoryNote")({ status: "unknown", members: [] }).includes("Not connected"), "unknown before the first connection");
  check(run("zoomDirectory")(overview.threads) !== null, "Zoom shows the same directory");
  delete overview.directory;

  if (failed) process.exit(1);
  console.log("page logic ok");
})().catch((e) => { console.error(e); process.exit(1); });
