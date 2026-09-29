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
  readyState: "loading", // the page wires itself at DOMContentLoaded, which checks never fire
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
const resolvable = {}; // notification channels this device resolves, for /api/notify/resolve
let uploads = 0, uploadFails = false; // files handed to /api/upload
let uploadTries = 0, uploadFailAt = 0; // the one upload try (counted from 1) that fails
let dmSendRefuses = ""; // what /api/dm/send refuses with, if set
const served = {}; // received files' bytes, by /api/files path
const calls = [];
const hold = {};
const held = {};
function release(p) { const r = held[p].shift(); r(); }
function fetch(url, opts) {
  if (down) return Promise.reject(new TypeError("Failed to fetch"));
  const u = new URL(url, "http://127.0.0.1");
  const raw = opts && opts.headers && opts.headers["Content-Type"] === "application/octet-stream";
  const body = opts && opts.body ? (raw ? { bytes: opts.body.size } : JSON.parse(opts.body)) : undefined;
  calls.push({ path: u.pathname, body });
  let data = {};
  if (u.pathname === "/api/thread") data = threads[u.searchParams.get("id")];
  else if (u.pathname === "/api/overview") data = Object.assign({ version: serving }, overview);
  else if (u.pathname === "/api/send") data = { id: "new", state: "delivered" };
  else if (u.pathname === "/api/refresh") data = refreshReply;
  else if (u.pathname === "/api/dm") data = dmThreads[u.searchParams.get("id")] || {};
  else if (u.pathname === "/api/dm/send") {
    if (dmSendRefuses) return Promise.resolve({ ok: false, status: 409, text: async () => dmSendRefuses, statusText: "" });
    data = { id: "sent-dm", state: "custody" };
  }
  else if (u.pathname === "/api/dm/new") data = { id: "d3" };
  else if (u.pathname.startsWith("/api/dm/agent/")) data = u.pathname.endsWith("/ask") ? { id: "asked", state: "custody" } : {};
  else if (u.pathname === "/api/notify/resolve") data = { conv: resolvable[u.searchParams.get("chan")] || "" };
  else if (u.pathname === "/api/upload") {
    uploadTries++;
    if (uploadFails || uploadTries === uploadFailAt) return Promise.resolve({ ok: false, text: async () => "no space left for this file", statusText: "" });
    data = { id: "up-" + (++uploads) };
  } else if (u.pathname.startsWith("/api/files/")) {
    const bytes = served[u.pathname] || new Uint8Array([1, 2, 3]);
    return Promise.resolve({ ok: true, arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.length) });
  }
  else if (u.pathname.startsWith("/api/notify/")) data = { note: "" };
  else if (u.pathname === "/api/device/link") data = { url: "https://hub.example/#agentnet-link-v2:abc", expires: "2026-09-29T18:10:00Z" };
  else if (u.pathname.startsWith("/api/device/")) data = { note: "Done." };
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
// The browser's notification permission, as a check sets it.
const Notification = { permission: "default", asked: 0, answer: "granted",
  async requestPermission() { this.asked++; this.permission = this.answer; return this.answer; } };
document.visibilityState = "visible";
document.hasFocus = () => focused;
let focused = true;
const ctx = vm.createContext({
  document, fetch, console, setTimeout, clearTimeout, URL, sessionStorage, Notification, Blob,
  window: { innerHeight: 800, matchMedia: () => ({ matches: false }), addEventListener() {} },
  localStorage: { getItem: () => null, setItem() {} },
  location: { reload() { reloads++; }, hash: "", pathname: "/", search: "" },
  history: { replaceState(a, b, url) { ctx.location.hash = ""; } },
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
  check(run("state.hub && state.hub.kind") === "device" && run("state.hub.key") === "bob/desk" && !$("hub").hidden && !$("dialog").open,
    "choosing a contact shows its conversations");
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
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("2 DMs") && side.includes("checked against their computer") && !side.includes("deploy"),
    "the sidebar has one row for a person, with how they are known, not their DMs: " + side);
  // A person's row shows their DMs in the main pane, apart; an open DM
  // links back to them; back goes one level up at a time (MEL-494).
  const rowOf = (name) => $("conv-list").children.map((li) => li.children && li.children.find((b) => b.tagName === "button" && textOf(b).includes(name))).find(Boolean);
  rowOf("Alice").click();
  let hub = JSON.stringify($("hub").children.map(textOf));
  check(!$("hub").hidden && $("timeline").hidden && $("composer").hidden && hub.includes("budget") && hub.includes("deploy") && hub.includes("New DM with Alice") &&
    run("state.dm") === null && textOf($("conv-name")) === "Alice", "a person with two DMs shows both, apart, in the main pane: " + hub);
  check(rowOf("Alice").attrs["aria-current"] === "true", "the sidebar marks the person shown");
  await run('openDM("d1")');
  check($("hub").hidden && !$("timeline").hidden && !$("hub-back").hidden && $("hub-back").textContent === "\u2039 Alice \u00b7 2 DMs",
    "an open DM links back to its person: " + $("hub-back").textContent);
  run("openHub(state.hub)"); // the link's click
  check(!$("hub").hidden && run("state.dm") === null, "the link goes back to the person's DMs");
  await run('openDM("d1")');
  run("backOneLevel()");
  check(!$("hub").hidden && run("state.dm") === null, "back from a DM goes to its person first (then to the list)");
  // With one DM, the person's row opens it directly; a device with one
  // conversation too; the back link still reaches their view.
  const twoDMs = overview.dms;
  overview.dms = twoDMs.filter((d) => d.id === "d1");
  await run("loadOverview()");
  rowOf("Alice").click();
  await pause(10);
  check(run("state.dm") === "d1" && $("hub").hidden && $("hub-back").textContent === "\u2039 Alice", "a person with one DM opens it directly: " + $("hub-back").textContent);
  overview.dms = twoDMs;
  await run("loadOverview()");
  const deviceRow = (addr) => $("conv-list").children.map((li) => li.children && li.children.find((b) => b.tagName === "button" &&
    textOf(b).replace(/\s+/g, "").includes(addr) && !textOf(b).includes("Person"))).find(Boolean);
  const keepThreads = overview.threads;
  overview.threads = [sum("c1", "carol/ci", { title: "which port?" }), sum("b1", "bob/desk", { title: "hi from bob", count: 2 }), sum("b2", "bob/desk", { title: "port?" })];
  await run("loadOverview()");
  deviceRow("carol/ci").click();
  await pause(10);
  check(run("state.data && state.data.peer") === "carol/ci" && $("hub").hidden, "a device with one conversation opens it directly");
  deviceRow("bob/desk").click();
  await pause(10);
  hub = JSON.stringify($("hub").children.map(textOf));
  check(!$("hub").hidden && run("state.data") === null && hub.includes("hi from bob") && hub.includes("New conversation with bob/desk"),
    "a device with more shows them in the main pane: " + hub);
  overview.threads = keepThreads;
  await run("loadOverview()");
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

  // Agents in a DM: invited by a person, accepted only by its host's person,
  // asked from the composer, dismissed by either; records read as sentences.
  const me = { person: "p-me", label: "Sergey", address: "me/laptop", fingerprint: "fp-me", state: "self" };
  const alice = Object.assign({ fingerprint: "fp-alice" }, alicePerson);
  const agentV = (pid, extra) => Object.assign({ pid, state: "invited", state_text: "", host: me, host_here: true, inviter: alice, note: "",
    shared: [], missing: 0, tasks_from: [], held: 0, invited: T, can_decide: false, can_dismiss: false, can_ask: false }, extra);
  overview.agents = true;
  overview.person = me;
  dmThreads.d4 = { id: "d4", peer: alice, created: T, mine: false, messages: [
    dmsg("m41", "in", "the deploy plan"),
    dmsg("m42", "in", "", { event: "Alice invited your agent (on me/laptop) into this DM.", pid: "pid1" }),
    dmsg("m43", "in", "which branch?", { kind: "question", pid: "pid1", to: "me/laptop", state: "part_waiting", state_text: "For your agent: it has not run yet." })],
    agents: [agentV("pid1", { state_text: "Alice invited your agent. Nothing runs unless you accept.", shared: ["m41"], note: "help with deploy",
      can_decide: true, can_dismiss: true })] };
  await run("loadOverview()");
  calls.length = 0;
  await run('openDM("d4")');
  let ag = JSON.stringify($("agents").children.map(textOf));
  check(!$("agents").hidden && ag.includes("Your agent") && ag.includes("Accept") && ag.includes("Decline") && ag.includes("invited by Alice") &&
    ag.includes("shown 1 earlier message") && ag.includes("tasks wait for you to accept them") && ag.includes("help with deploy"),
    "an invitation to your agent says who invited it, what it may see, and offers accept and decline: " + ag);
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("Alice invited your agent") && !tl.includes('"pid"') && tl.includes("Shared with your agent") && tl.includes("To your agent"),
    "a record reads as a sentence; the shared message and the request to the agent say so: " + tl);
  check($("timeline").children.flatMap((c) => strayText(c)).concat($("agents").children.flatMap((c) => strayText(c))).length === 0,
    "no stray text with agents");
  check(!tl.includes("Run") && !tl.includes("Stop"), "a request still waiting offers no decision");
  // A task to your agent that needs your accept: the usual decision, by its id.
  dmThreads.d4.messages.push(dmsg("m44", "in", "rotate the key", { kind: "task", pid: "pid1", to: "me/laptop", state: "awaiting",
    state_text: "Needs you: tasks run only if you accept them", actions: ["accept"] }));
  await run('openDM("d4")');
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("Needs you: tasks run only if you accept them") && tl.includes("Accept"), "a task to your agent offers accept: " + tl);
  calls.length = 0;
  run("decide")("accept", dmThreads.d4.messages[3], run("state.dmData"));
  check(!calls.some((c) => c.path === "/api/act"), "running it waits for the dialog");
  byId.gate.checked = true;
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/act" && c.body.do === "accept" && c.body.id === "m44"), "accept names that request");
  dmThreads.d4.messages.pop();
  run("decideDialog")(dmThreads.d4.agents[0], run("state.dmData"), true);
  const acc = JSON.stringify($("dialog-body").children.map(textOf));
  check(!calls.some((c) => c.path === "/api/dm/agent/decide") && acc.includes("the deploy plan") && acc.includes("Every task waits for you"),
    "accepting shows exactly what is agreed to, and waits for its button: " + acc);
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/dm/agent/decide" && c.body.pid === "pid1" && c.body.accept === true), "accept sends the host's decision");

  // Inviting: nothing is chosen for the person.
  calls.length = 0;
  run("inviteDialog")(run("state.dmData"));
  await $("dialog-ok").onclick();
  check(!calls.some((c) => c.path === "/api/dm/agent/invite") && $("dialog-error").textContent.includes("Choose whose agent"),
    "an invite needs whose agent, chosen by hand");
  byId["agent-host:alice/desk"].checked = true;
  byId["agent-share:m41"].checked = true;
  byId["agent-tasks:fp-me"].checked = true;
  byId["agent-note"].value = "please help";
  await $("dialog-ok").onclick();
  const invs = calls.filter((c) => c.path === "/api/dm/agent/invite");
  check(invs.length === 1 && invs[0].body.conv === "d4" && invs[0].body.host === "alice/desk" && JSON.stringify(invs[0].body.share) === '["m41"]' &&
    JSON.stringify(invs[0].body.tasks_from) === '["fp-me"]' && invs[0].body.note === "please help",
    "the invite carries exactly what was chosen: " + JSON.stringify(invs[0] && invs[0].body));

  // Asking an active agent from the composer; the target is part of that DM's draft.
  dmThreads.d4.agents = [agentV("pid2", { state: "active", host: alice, host_here: false, inviter: me, tasks_from: [me], can_ask: true, can_dismiss: true })];
  await run('openDM("d4")');
  ag = JSON.stringify($("agents").children.map(textOf));
  check(ag.includes("Alice's agent") && ag.includes("Ask") && ag.includes("Dismiss") && !ag.includes("Accept") && ag.includes("tasks without asking from you"),
    "someone else's agent offers ask and dismiss, never accept: " + ag);
  run("setDMAgent")(dmThreads.d4.agents[0]);
  check($("body").placeholder === "Ask Alice's agent" && $("kind").hidden === false && !$("replying").hidden,
    "asking an agent says whom, and offers tasks when the invitation allows yours: " + $("body").placeholder);
  $("body").value = "which branch?";
  calls.length = 0;
  await run("send")(ev);
  const asks = calls.filter((c) => c.path === "/api/dm/agent/ask");
  check(asks.length === 1 && asks[0].body.pid === "pid2" && asks[0].body.kind === "question" && asks[0].body.body === "which branch?" &&
    !calls.some((c) => c.path === "/api/dm/send"), "the question goes to the agent, not as a message to the person");
  check(run("state.dmAgent") === "pid2", "follow-ups go to the same agent until cancelled");
  dmThreads.d4.agents[0].tasks_from = [];
  await run('openDM("d4")');
  check($("kind").hidden === false, "a task can be given without standing permission: the owner accepts it");
  run("setKind")("task");
  run("kindHint")();
  check($("compose-hint").textContent.includes("Alice accepts it first"), "and the page says so: " + $("compose-hint").textContent);
  run("setKind")("question");
  dmThreads.d4.agents[0].tasks_from = [me];
  await run('openDM("d2")');
  check(run("state.dmAgent") === null && $("body").placeholder === "Write to Alice", "another DM does not ask that agent");
  await run('openDM("d4")');
  check(run("state.dmAgent") === "pid2", "coming back, the DM still asks its agent");
  run("setDMReply")(dmThreads.d4.messages[0]);
  check(run("state.dmAgent") === null && $("body").placeholder === "Write to Alice", "replying to a message writes to the person again");
  calls.length = 0;
  run("dismissDialog")(dmThreads.d4.agents[0]);
  check(!calls.some((c) => c.path === "/api/dm/agent/dismiss"), "dismissing waits for its button");
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/dm/agent/dismiss" && c.body.pid === "pid2"), "either person can dismiss");
  // A dismissal never turns a draft for the agent into a message to the
  // person: the target stays, Send waits, and only the person's own
  // choice (×) writes to the person.
  run("setDMAgent")(dmThreads.d4.agents[0]);
  $("body").value = "and the tests?";
  Object.assign(dmThreads.d4.agents[0], { state: "dismissed", state_text: "Dismissed by Alice", can_ask: false, can_dismiss: false });
  await run("loadDM()");
  check(run("state.dmAgent") === "pid2" && $("send").disabled && $("body").value === "and the tests?" && $("compose-hint").textContent.includes("remove the agent"),
    "a dismissal keeps the draft's agent target and blocks Send: " + $("compose-hint").textContent);
  calls.length = 0;
  await run("send")(ev);
  check(!calls.some((c) => c.path === "/api/dm/send" || c.path === "/api/dm/agent/ask"), "nothing goes to the person (or the agent) instead");
  await run('openDM("d2")');
  await run('openDM("d4")');
  check(run("state.dmAgent") === "pid2" && $("send").disabled && $("body").value === "and the tests?", "coming back, the draft still names the dismissed agent");
  run("setDMAgent")(null); // the person's ×
  check(!$("send").disabled && $("body").placeholder === "Write to Alice" && $("body").value === "and the tests?", "the person's own choice writes to the person, text kept");
  $("body").value = "";
  Object.assign(dmThreads.d4.agents[0], { state: "active", state_text: "", can_ask: true, can_dismiss: true });

  // A person is linked to the agents their device runs, in Classic and Zoom.
  overview.people = [Object.assign({}, alice, { agents: [{ address: "alice/desk", dms: [{ conv: "d4", pid: "pid2", state: "active" }] }] })];
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("Their agent on alice/desk · 1 DM (1 active)"), "the person row names their agent and its DMs: " + side);
  const zp = JSON.stringify(textOf(run("Zoom").people()));
  check(zp.includes("Agent") && zp.includes("Their agent on alice/desk") && zp.includes("active"), "Zoom joins the person to their agent: " + zp);
  overview.people = [alicePerson];

  // Where agents cannot be invited (the browser device), nothing offers it.
  overview.agents = false;
  dmThreads.d4.agents = [];
  await run("loadOverview()");
  await run('openDM("d4")');
  check($("agents").hidden, "no agent controls where agents cannot be invited");
  delete overview.agents;

  // Notifications (browser device): off until the person turns them on; the
  // browser's permission is asked only from that click; the page reports
  // only what the person has in front of them; a click opens the DM this
  // device resolves, never a guess.
  check($("notify-line").hidden, "no notification controls where the page has none (the daemon's page)");
  overview.notify = { available: true, enabled: false, reason: "", mutes: [], allowed: [] };
  calls.length = 0;
  await run("loadOverview()");
  const turnOn = $("notify-line").children[1];
  await run("loadOverview()");
  check($("notify-line").children[1] === turnOn, "an unchanged line keeps its button (a click is not lost to a redraw)");
  check(!$("notify-line").hidden && textOf($("notify-line")).includes("Notifications off") && Notification.asked === 0 &&
    !calls.some((c) => c.path.startsWith("/api/notify/")), "off, and nothing asked, when the page loads: " + textOf($("notify-line")));
  run("notifyDialog()");
  check(Notification.asked === 0 && !calls.some((c) => c.path === "/api/notify/enable"), "the dialog explains first; nothing is asked yet");
  Notification.answer = "denied";
  await $("dialog-ok").onclick();
  check(Notification.asked === 1 && $("dialog-error").textContent.includes("Everything else works") && !calls.some((c) => c.path === "/api/notify/enable"),
    "a refusal is said plainly and nothing is turned on");
  await run("loadOverview()");
  check(textOf($("notify-line")).includes("blocked in this browser's settings"), "blocked: said, and the messenger stays usable");
  Notification.permission = "default";
  Notification.answer = "granted";
  run("notifyDialog()");
  await $("dialog-ok").onclick();
  check(calls.filter((c) => c.path === "/api/notify/enable").length === 1, "allowed: turned on once");
  overview.notify = { available: true, enabled: true, reason: "", mutes: [], allowed: [] };
  await run("loadOverview()");
  await run('openDM("d2")');
  let chips = JSON.stringify($("peer-chips").children.map(textOf));
  check(chips.includes("Notifies you") && chips.includes("Alerts from Alice off · Allow"), "a DM says it notifies you, and that its person may not yet: " + chips);
  calls.length = 0;
  $("peer-chips").children[0].click();
  await tick();
  check(calls.some((c) => c.path === "/api/notify/mute" && c.body.conv === "d2" && c.body.muted === true), "one DM is muted, by its id");
  $("peer-chips").children[1].click();
  await tick();
  check(calls.some((c) => c.path === "/api/notify/allow" && c.body.person === "p-alice" && c.body.allowed === true), "allowing names the person");
  // Presented: visible, focused, that DM, newest in view (Classic).
  const seenCalls = () => calls.filter((c) => c.path === "/api/notify/seen");
  calls.length = 0;
  run("state.seenReported = {}");
  focused = false;
  run("reportSeen()");
  check(seenCalls().length === 0, "not focused: nothing is reported");
  focused = true;
  run("reportSeen()");
  check(seenCalls().length === 1 && seenCalls()[0].body.conv === "d2" && JSON.stringify(seenCalls()[0].body.ids) === '["m3"]',
    "in front of the person: the DM's messages from them are reported: " + JSON.stringify(seenCalls()[0] && seenCalls()[0].body));
  run("reportSeen()");
  check(seenCalls().length === 1, "and not again until something new is shown");
  run('state.seenReported = {}; state.lens = "comic"');
  run("reportSeen()");
  check(seenCalls().length === 1, "another view does not report (only Classic knows what is in view)");
  run('state.lens = "classic"');
  // A notification's click: the DM this device resolves.
  resolvable["AbCdEfGhIjKlMnOpQrStUv"] = "d1";
  await run("openThread")("a1");
  run('openNotified("AbCdEfGhIjKlMnOpQrStUv")');
  await pause(5);
  check(run("state.dm") === "d1", "a click opens the DM its channel resolves to here");
  run('openNotified("ZZZZZZZZZZZZZZZZZZZZZZ")');
  await pause(5);
  check(run("state.dm") === "d1" && run("state.pendingOpen") !== null, "an unknown channel opens nothing and waits for the stream");
  run("state.pendingOpen = null");

  // The footer: one plain line about this computer; where the responder
  // runs, the address, key and version one click away.
  overview.me.responder = "claude";
  overview.me.responder_dir = "/home/me/.agentnet/responder";
  await run("loadOverview()");
  check($("machine").textContent === "Your responder: claude", "the footer names the responder, not its folder: " + $("machine").textContent);
  const tech = JSON.stringify($("machine-detail").children.map(textOf));
  check(tech.includes("It runs in /home/me/.agentnet/responder") && tech.includes("Address: me/laptop") && tech.includes("Key: SHA256:me"),
    "the folder, address and key are in the details: " + tech);
  delete overview.me.responder;
  delete overview.me.responder_dir;
  await run("loadOverview()");
  check($("machine").textContent === "No responder: questions and tasks wait for you" &&
    JSON.stringify($("machine-detail").children.map(textOf)).includes("agentnet help responder"), "no responder: said plainly, how to choose one in the details");
  overview.device = { online: false, persisted: false, revoked: false };
  await run("loadOverview()");
  check($("machine").textContent === "Not connected to your server now: what you write waits here · This browser may clear this device's data" &&
    JSON.stringify($("machine-detail").children.map(textOf)).includes("This browser runs nothing"), "a browser device: its state plainly, the rest in the details: " + $("machine").textContent);
  delete overview.device;
  await run("loadOverview()");

  // Desktop alerts (the daemon's page): the daemon shows them; no browser
  // permission is asked. A click (#conv=ID) opens that DM, or the list.
  overview.notify = { available: true, native: true, enabled: false, reason: "", mutes: [], allowed: [] };
  await run("loadOverview()");
  const asked = Notification.asked;
  calls.length = 0;
  run("notifyDialog()");
  check(JSON.stringify($("dialog-body").children.map(textOf)).includes("this computer shows"), "the desktop dialog says this computer shows it");
  await $("dialog-ok").onclick();
  check(Notification.asked === asked && calls.some((c) => c.path === "/api/notify/enable"), "turned on without asking the browser anything");
  await run('openThread("a1")');
  await run('openClicked("d2")');
  check(run("state.dm") === "d2", "an alert's click opens its DM");
  await run('openClicked("' + "e".repeat(64) + '")');
  check(run("state.dm") === "d2", "a DM not here opens nothing else");
  // A click while this page is open changes only the fragment: it opens
  // that DM too, once, and leaves the address clean.
  const clickedID = "c".repeat(64);
  overview.dms = [...overview.dms, Object.assign({}, overview.dms.find((d) => d.id === "d2"), { id: clickedID })];
  dmThreads[clickedID] = Object.assign({}, dmThreads.d2, { id: clickedID });
  await run("loadOverview()");
  await run('openThread("a1")');
  ctx.location.hash = "#conv=" + clickedID;
  run("clickedLater()");
  await pause(20);
  check(run("state.dm") === clickedID && ctx.location.hash === "", "a same-tab click (#conv= fragment) opens its DM and is taken from the address");
  await run('openThread("a1")');
  ctx.location.hash = "#conv=not-an-id";
  run("clickedLater()");
  await pause(20);
  check(run("state.dm") === null && ctx.location.hash === "", "a malformed fragment is removed and opens nothing");
  check(/window\.addEventListener\("hashchange", clickedLater\)/.test(fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8")),
    "the page listens for a fragment change");
  overview.dms = overview.dms.filter((d) => d.id !== clickedID);
  delete dmThreads[clickedID];
  await run("loadOverview()");
  delete overview.notify;

  // Files (MEL-489): chosen, pasted or dropped into the open DM's draft;
  // nothing leaves before Send; limits said before sending; a failed send
  // keeps them; each DM keeps its own; received ones open only on request,
  // pictures (by their bytes) in place, anything else as a download.
  overview.files = { max_file: 1000, max_count: 2 };
  await run("loadOverview()");
  await run('openDM("d2")');
  check($("attach").hidden === false, "a DM offers to attach files");
  const fileOf = (name, size, type) => new File([new Uint8Array(size)], name, { type });
  calls.length = 0;
  run("addFiles")([fileOf("plan.pdf", 300, "application/pdf"), fileOf("image.png", 200, "image/png")], true);
  let pend = JSON.stringify($("attach-list").children.map(textOf));
  check(!$("attach-list").hidden && pend.includes("plan.pdf") && /pasted-image-\d{8}-\d{6}\.png/.test(pend) && pend.includes("300 B"),
    "files wait with their names and sizes; a pasted picture gets a name: " + pend);
  check(!calls.some((c) => c.path === "/api/upload" || c.path === "/api/dm/send"), "nothing leaves before Send");
  run("addFiles")([fileOf("third.txt", 10, "text/plain")]);
  check($("compose-error").textContent.includes("at most 2 files"), "too many files are said before sending: " + $("compose-error").textContent);
  run("removeFile")(run("state.files")[2].key);
  check(run("state.files").length === 2 && $("compose-error").textContent === "", "removing clears the complaint");
  check(run("overLimit")([fileOf("huge.bin", 5000, "")]).includes("larger than"), "a file over the limit is said");
  // Another DM starts empty, and this one keeps its files.
  await run('openDM("d1")');
  check(run("state.files").length === 0 && $("attach-list").hidden, "another DM has its own (empty) files");
  await run('openDM("d2")');
  check(run("state.files").length === 2, "coming back, the DM still has its files");
  // A failed hand-over keeps text and files; a good one sends them by id.
  uploadFails = true;
  $("body").value = "";
  calls.length = 0;
  await run("send")(ev);
  check(run("state.files").length === 2 && $("compose-error").textContent.includes("no space") && $("compose-error").textContent.includes("still here") &&
    !calls.some((c) => c.path === "/api/dm/send"), "a failed hand-over sends nothing and keeps the files: " + $("compose-error").textContent);
  uploadFails = false;
  calls.length = 0;
  await run("send")(ev);
  const withFiles = calls.find((c) => c.path === "/api/dm/send");
  check(withFiles && withFiles.body.body === "" && JSON.stringify(withFiles.body.files) === JSON.stringify(["up-" + (uploads - 1), "up-" + uploads]) &&
    calls.filter((c) => c.path === "/api/upload").length === 2, "a file-only message names the handed-over files: " + JSON.stringify(withFiles && withFiles.body));
  check(run("state.files").length === 0 && $("attach-list").hidden, "sent files leave the composer");
  // Staged files belong to the draft until a send names them: a failure
  // part way keeps what was handed over, and the retry hands over only the
  // rest; a refused send took what it named, so the next try hands them
  // over again; a removed file is discarded.
  run("addFiles")([fileOf("one.txt", 3, "text/plain"), fileOf("two.txt", 4, "text/plain")]);
  calls.length = 0;
  uploadTries = 0;
  uploadFailAt = 2;
  await run("send")(ev);
  const firstID = run("state.files")[0].staged;
  check(firstID && !run("state.files")[1].staged && calls.filter((c) => c.path === "/api/upload").length === 2 && !calls.some((c) => c.path === "/api/dm/send") &&
    $("compose-error").textContent.includes("still here"), "a failure part way keeps the first file's hand-over and sends nothing");
  uploadFailAt = 0;
  dmSendRefuses = "A file to send is no longer with AgentNet on this computer: send again.";
  calls.length = 0;
  await run("send")(ev);
  const refused = calls.find((c) => c.path === "/api/dm/send");
  check(calls.filter((c) => c.path === "/api/upload").length === 1 && refused && refused.body.files[0] === firstID && refused.body.files.length === 2,
    "the retry hands over only the rest and names both: " + JSON.stringify(refused && refused.body.files));
  check(run("state.files").length === 2 && run("state.files").every((f) => !f.staged) && $("compose-error").textContent.includes("send again"),
    "a refused send keeps the files, their hand-over spent: " + $("compose-error").textContent);
  dmSendRefuses = "";
  calls.length = 0;
  await run("send")(ev);
  check(calls.filter((c) => c.path === "/api/upload").length === 2 && run("state.files").length === 0, "the next try hands both over again and sends");
  run("addFiles")([fileOf("drop.txt", 3, "text/plain")]);
  await run("preparedFiles")(run("state.files"));
  const dropID = run("state.files")[0].staged;
  calls.length = 0;
  run("removeFile")(run("state.files")[0].key);
  await tick();
  const disc = calls.find((c) => c.path === "/api/upload/discard");
  check(dropID && disc && JSON.stringify(disc.body.ids) === JSON.stringify([dropID]) && run("state.files").length === 0, "a removed file is discarded by its id");
  // Device conversations (v1) take files too: each keeps its own; an answer
  // goes without them; a conversation blocked by a key change takes none.
  await run('openThread("a1")');
  check($("attach").hidden === false, "a device conversation offers to attach files");
  run("addFiles")([fileOf("v1.txt", 6, "text/plain")]);
  await run('openThread("c1")');
  check(run("state.files").length === 0, "another device conversation has its own (empty) files");
  run("addFiles")([fileOf("c1.txt", 2, "text/plain")]);
  run("setAnswering")(run("state.data").messages.find((m) => (m.actions || []).includes("reply")));
  check($("attach").hidden === true, "answering takes no files");
  calls.length = 0;
  $("body").value = "port 8080";
  await run("send")(ev);
  check(!calls.some((c) => c.path === "/api/act" || c.path === "/api/upload") && $("compose-error").textContent.includes("without files"),
    "an answer with files waiting is not sent, and says why: " + $("compose-error").textContent);
  run("setAnswering")(null);
  run("removeFile")(run("state.files")[0].key);
  $("body").value = "";
  await run('openThread("b1")');
  check($("attach").hidden === true, "a conversation blocked by a key change takes no files");
  await run('openThread("a1")');
  check(run("state.files").length === 1 && run("state.files")[0].name === "v1.txt", "coming back, the conversation still has its file");
  $("body").value = "";
  calls.length = 0;
  await run("send")(ev);
  const v1send = calls.find((c) => c.path === "/api/send");
  check(v1send && v1send.body.to === "alice/desk" && v1send.body.body === "" && v1send.body.files.length === 1 && v1send.body.files[0].startsWith("up-") &&
    run("state.files").length === 0, "a file-only message in a device conversation names the handed-over file: " + JSON.stringify(v1send && v1send.body));
  run('setLens("zoom")');
  await run('Zoom.go(2, { thread: "a1" })');
  run("addFiles")([fileOf("z.txt", 1, "text/plain")]);
  run("writeDialog(state.data, null)");
  check(JSON.stringify($("dialog-body").children.map(textOf)).includes("z.txt"), "Zoom's write dialog holds the conversation's file");
  byId["write-body"].value = "from zoom";
  calls.length = 0;
  await $("dialog-ok").onclick();
  const zv1 = calls.find((c) => c.path === "/api/send");
  check(zv1 && zv1.body.files.length === 1 && run("state.files").length === 0, "and sends it: " + JSON.stringify(zv1 && zv1.body));
  run('setLens("classic")');
  await run('openDM("d2")');
  // Asking an agent takes no files.
  dmThreads.d2.agents = [agentV("pidF", { state: "active", host: alice, host_here: false, inviter: me, can_ask: true })];
  await run('openDM("d2")');
  run("setDMAgent")(dmThreads.d2.agents[0]);
  check($("attach").hidden === true, "asking an agent takes no files");
  run("setDMAgent")(null);
  dmThreads.d2.agents = [];
  // Received files: a picture shown in place, anything else saved; sent ones not reopened.
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10, 0, 0]);
  served["/api/files/m3/0"] = png;
  served["/api/files/m3/1"] = new TextEncoder().encode("<svg onload=alert(1)>");
  const m3f = dmThreads.d2.messages.find((x) => x.id === "m3");
  m3f.attachments = [{ index: 0, name: "photo.png", size: 10 }, { index: 1, name: "logo.svg", size: 21 }];
  dmThreads.d2.messages.find((x) => x.id === "m2").attachments = [{ index: 0, name: "mine.txt", size: 5 }];
  await run('openDM("d2")');
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("photo.png") && tl.includes("logo.svg") && tl.includes("mine.txt") && (tl.match(/Open/g) || []).length === 2,
    "received files offer Open; sent ones only their names: " + tl);
  const slot0 = document.createElement("span"), btn0 = document.createElement("button");
  await run("openFile")("m3", 0, "photo.png", btn0, slot0);
  check(slot0.children.some((c) => c.tagName === "img" && String(c.attrs.src).startsWith("blob:")), "a PNG, by its bytes, is shown in place from a blob: URL");
  const slot1 = document.createElement("span"), btn1 = document.createElement("button");
  await run("openFile")("m3", 1, "logo.svg", btn1, slot1);
  check(!slot1.children.some((c) => c.tagName === "img"), "an SVG is never shown as a picture: it is saved");
  check(run("state.opened").length === 2, "opened files are tracked to be freed");
  await run('openDM("d1")');
  check(run("state.opened").length === 0, "leaving the conversation frees them");
  delete m3f.attachments;
  delete dmThreads.d2.messages.find((x) => x.id === "m2").attachments;
  delete overview.files;

  // Reminders ("remind me later", the daemon's page): on received messages
  // only; a few times or one chosen, only in the future; listed with the
  // overdue first; done and cancel by the message's id.
  const soon = new Date(Date.now() + 3600e3).toISOString(), past = new Date(Date.now() - 600e3).toISOString();
  Object.assign(overview, { remind: true, reminders: [
    { message: "m3", conv: "d2", from: "alice/desk", title: "can you check?", due: soon, overdue: false },
    { message: "a1", conv: "", from: "alice/desk", title: "hi from alice", due: past, overdue: true }] });
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.indexOf("Reminders · 1 due") >= 0 && side.indexOf("hi from alice") < side.indexOf("can you check?"),
    "reminders head the list, the overdue one first: " + side.slice(0, 200));
  await run('openDM("d2")');
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("Reminder today") && tl.includes("Change…") && tl.includes("Done") && (tl.match(/Remind me…/g) || []).length === 0,
    "the reminded message shows its reminder; sent messages offer none: " + tl);
  calls.length = 0;
  const m3 = dmThreads.d2.messages.find((x) => x.id === "m3");
  run("remindDialog")(m3);
  await $("dialog-ok").onclick();
  check(!calls.some((c) => c.path === "/api/remind") && $("dialog-error").textContent.includes("Choose when"), "a time must be chosen");
  byId["remind-when:custom"].checked = true;
  byId["remind-at"].value = "2020-01-01T09:00";
  await $("dialog-ok").onclick();
  check(!calls.some((c) => c.path === "/api/remind") && $("dialog-error").textContent.includes("future"), "only in the future");
  byId["remind-when:custom"].checked = false;
  byId["remind-when:0"].checked = true;
  await $("dialog-ok").onclick();
  const set = calls.find((c) => c.path === "/api/remind");
  check(set && set.body.id === "m3" && Math.abs(set.body.due - (Date.now() / 1000 + 1800)) < 120, "in 30 minutes, for that message: " + JSON.stringify(set && set.body));
  calls.length = 0;
  run("remindAct")("/api/remind/done", "m3");
  await tick();
  check(calls.some((c) => c.path === "/api/remind/done" && c.body.id === "m3"), "done names the message");
  await run('openThread("b1")');
  check(JSON.stringify($("timeline").children.map(textOf)).includes("Remind me…"), "a received device message offers a reminder");
  await run("openReminder")(overview.reminders[1]);
  check(run("state.thread") === "a1" && run("state.dm") === null, "a device-message reminder opens its conversation");
  overview.remind = false;
  delete overview.reminders;
  await run("loadOverview()");
  await run('openDM("d2")');
  check(!JSON.stringify($("timeline").children.map(textOf)).includes("Remind me"), "no reminders where the page has none (the browser device)");
  delete overview.remind;

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
  check(run("state.query") === "" && run("state.hub.key") === "listed:vitalii/laptop" && !$("hub").hidden && run("state.dm") === null,
    "choosing a person shows them, nothing else");

  // One person, several devices (MEL-433): setup asks person or service;
  // your devices are listed with this one marked; a new device's request
  // is decided here; a person's devices hang under them, never as rows of
  // their own; services stay apart.
  const keepPerson = overview.person, keepThreads2 = overview.threads;
  Object.assign(overview, { role: "unset", person: null });
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("Who uses this computer?") && side.includes("I do: set up my person") && side.includes("It is a service or bot") &&
    side.includes("Add this device from there"), "setup asks person or service, and points to adding a device: " + side);
  calls.length = 0;
  run("serviceDialog()");
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/device/service"), "a service is chosen only by its button");
  overview.role = "service";
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("This computer is a service or bot") && !side.includes("set up my person"), "a service is never asked to set up a person: " + side);
  const bobPerson2 = { person: "p-bob", label: "Bob", address: "bob/desk", state: "pinned", devices: [
    { address: "bob/desk", name: "desk", fingerprint: "SHA256:b1" }, { address: "bob/phone", name: "phone", fingerprint: "SHA256:b2" }] };
  Object.assign(overview, { role: "person", person: Object.assign({}, keepPerson, { devices: [
      { address: "me/laptop", name: "laptop", fingerprint: "SHA256:me", this: true }, { address: "me/phone", name: "phone", fingerprint: "SHA256:m2" }] }),
    people: [alicePerson, bobPerson2], links: [{ id: "L1", address: "me/tablet", name: "tablet", fingerprint: "SHA256:t1", requested_at: T, expires: T, state: "pending" }],
    history: [{ device: "me/phone", name: "phone", done: 3, total: 12, state: "running" }],
    threads: [sum("b9", "bob/phone", { title: "phone hello" }), sum("h1", "hub/ops", { title: "service report" })] });
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("on laptop (this one), phone") && side.includes("Your devices") && side.includes("A new device,  tablet , asks to join as you"),
    "you, your devices (this one marked) and a new device's request: " + side);
  check(side.includes("on desk, phone") && side.replace(/\s+/g, "").includes("hub/ops") && !side.replace(/\s+/g, "").includes("bob/phone"),
    "a person's devices are named under them; their device is not a row of its own; a service is: " + side);
  calls.length = 0;
  run("linkDialog")(overview.links[0]);
  const ask = JSON.stringify($("dialog-body").children.map(textOf));
  check(ask.includes("tablet") && ask.includes("Approve it only if") && ask.includes("Details") && !calls.some((c) => c.path === "/api/device/decide"),
    "the request says what approving means, keys under Details, and waits: " + ask);
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/device/decide" && c.body.id === "L1" && c.body.accept === true), "approving sends that decision");
  run("devicesDialog()");
  const devs = JSON.stringify($("dialog-body").children.map(textOf));
  check(/laptop\s+\(this device\)/.test(devs) && devs.includes("Remove") && devs.includes("Copying your chats to phone: 3 of 12"), "your devices, with the copy's progress: " + devs);
  $("dialog").close();
  run('openHub({ kind: "person", key: "p-bob" })');
  hub = JSON.stringify($("hub").children.map(textOf));
  check(hub.includes("Bob on 2 devices") && hub.includes("phone") && hub.includes("1 device conversation"), "a person's view lists their devices: " + hub);
  run('openHub({ kind: "device", key: "bob/phone" })');
  check(!$("hub-back").hidden && $("hub-back").textContent === "\u2039 Bob" && $("conv-topic").textContent.startsWith("Bob's device"),
    "their device's conversations say whose device it is and link back: " + $("conv-topic").textContent);
  run("backOneLevel()");
  check(run("state.hub.kind") === "person" && run("state.hub.key") === "p-bob", "back from their device goes to the person");
  // Zoom: one node for you (with a service present, no second "you" at the
  // devices' centre); each person's devices behind a closed disclosure.
  run('setLens("zoom")');
  run("Zoom.go(0, {})");
  const tree = (n, out = []) => { if (n && n.children) { out.push(n); n.children.forEach((c) => tree(c, out)); } return out; };
  const layer = $("zoom").children[$("zoom").children.length - 1]; // the level shown (the one before may still be leaving)
  const zoomEls = tree(layer);
  const zt = JSON.stringify(textOf(layer));
  check((zt.match(/\(you\)/g) || []).length === 1 && !zoomEls.some((e) => (e.className || "").split(/\s+/).includes("me")) &&
    zt.replace(/\s+/g, "").includes("hub/ops") && !zt.includes("This computer"), "Zoom shows you once, and the service apart: " + zt.slice(0, 400));
  const discl = zoomEls.filter((e) => e.tagName === "details" && (e.className || "").includes("person-devices"));
  const bobDevs = discl.find((e) => textOf(e).includes("Bob on 2 devices"));
  check(discl.length === 3 && discl.every((e) => e.attrs.open === undefined) && discl.some((e) => textOf(e).includes("You on 2 devices")) &&
    bobDevs && textOf(bobDevs).includes("desk") && textOf(bobDevs).includes("phone"),
    "each checked person's devices (Alice's one too) are behind a closed disclosure holding them: " + discl.map(textOf).join(" | "));
  run('setLens("classic")');
  overview.link = { state: "pending" };
  await run("loadOverview()");
  check(JSON.stringify($("conv-list").children.map(textOf)).includes("Waiting for your other device to approve this one"), "this device's own request is said plainly");
  Object.assign(overview, { person: keepPerson, threads: keepThreads2, people: [alicePerson, overview.people[1]] });
  delete overview.role; delete overview.links; delete overview.history; delete overview.link;
  overview.people = [alicePerson, { label: "Vitalii", address: "vitalii/laptop", state: "listed" }];
  await run("loadOverview()");

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
  // A DM's files in Comic: props on the panel, Open in the full read.
  overview.files = { max_file: 1000, max_count: 2 };
  await run("loadOverview()");
  const m3c = dmThreads.d2.messages.find((x) => x.id === "m3");
  m3c.attachments = [{ index: 0, name: "photo.png", size: 10 }];
  await run('openDM("d2")');
  run("Comic.turn(1)");
  comic = JSON.stringify($("comic").children.map(textOf));
  check(comic.includes("📎 photo.png"), "a comic panel shows the message's files: " + comic.slice(0, 300));
  const readBtn = $("comic").children.map((c) => c.querySelector && c.querySelector("button.read-btn")).find(Boolean);
  const panels = [];
  const collect = (n) => { if (n && n.children) { if (n.tagName === "article" && String(n.attrs.id || "").startsWith("p-")) panels.push(n); n.children.forEach(collect); } };
  $("comic").children.forEach(collect);
  const m3panel = panels.find((n) => n.attrs.id === "p-m3");
  check(!!readBtn && !!m3panel, "the comic page has a panel for the message with files");
  m3panel.querySelector("button.read-btn").click();
  const readBody = JSON.stringify($("dialog-body").children.map(textOf));
  check($("dialog").open && readBody.includes("photo.png") && readBody.includes("Open"), "the full read offers to open a received file: " + readBody.slice(0, 300));
  $("dialog").close();

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
  // Files in Zoom: named in the DM's chat, openable in the message, and the
  // write dialog takes this DM's draft files (the composer's own), no other's.
  zoom = JSON.stringify($("zoom").children.map(textOf));
  check(zoom.includes("📎 photo.png"), "Zoom's DM chat names a message's files: " + zoom.slice(0, 300));
  await run('Zoom.go(3, { msg: "m3" })');
  zoom = JSON.stringify($("zoom").children.map(textOf));
  check(zoom.includes("photo.png") && zoom.includes("Open"), "Zoom's message offers to open its file: " + zoom.slice(0, 300));
  await run('Zoom.go(2, { dm: "d3" })');
  run("addFiles")([fileOf("other.txt", 7, "text/plain")]);
  await run('Zoom.go(2, { dm: "d2" })');
  run("addFiles")([fileOf("notes.txt", 5, "text/plain")]);
  run("dmWriteDialog(state.dmData)");
  let wd = JSON.stringify($("dialog-body").children.map(textOf));
  check(wd.includes("notes.txt") && !wd.includes("other.txt") && wd.includes("Add files"), "the Zoom dialog holds this DM's files only: " + wd.slice(0, 300));
  run("pushFiles")([fileOf("b.txt", 5, "text/plain"), fileOf("c.txt", 5, "text/plain")]);
  run("dmWriteDialog(state.dmData)");
  byId["write-body"].value = "";
  calls.length = 0;
  await $("dialog-ok").onclick();
  check($("dialog").open && $("dialog-error").textContent.includes("at most 2 files") && !calls.some((c) => c.path === "/api/upload"),
    "too many files are refused in Zoom before anything leaves: " + $("dialog-error").textContent);
  run("removeFile")(run("state.files")[2].key);
  run("removeFile")(run("state.files")[1].key);
  calls.length = 0;
  await $("dialog-ok").onclick();
  const zf = calls.find((c) => c.path === "/api/dm/send");
  check(zf && zf.body.conv === "d2" && zf.body.files.length === 1 && calls.filter((c) => c.path === "/api/upload").length === 1 && run("state.files").length === 0,
    "a file-only message from Zoom sends this DM's file: " + JSON.stringify(zf && zf.body));
  await run('Zoom.go(2, { dm: "d3" })');
  check(run("state.files").length === 1 && run("state.files")[0].name === "other.txt", "the other DM still has its own file");
  run("removeFile")(run("state.files")[0].key);
  delete m3c.attachments;
  delete overview.files;
  await run("loadOverview()");
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
