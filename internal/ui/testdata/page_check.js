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
  removeAttribute(k) { delete this.attrs[k]; }
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
let storageReply = null; // what /api/storage answers (an object, or 404)
let teamsReply = { realm_id: "r", status: "available", current: true, at: "2026-09-28T12:00:00Z", truncated: false, teams: [] }; // what /api/teams answers (an object, or 404)
let teamSnapshotPersons = [{ id: "p-me", seq: 0, hash: "x" }, { id: "p-alice", seq: 0, hash: "y" }];
const teamGroupCreates = [], teamGroupInvites = [];
let teamGroupRefusesPerson = "";

let teamRefuses = ""; // what /api/team refuses with, if set
let teamResultName = ""; // a returned name may be newer than the retained directory
const namedA = 'a'.repeat(32), namedB = 'b'.repeat(32), namedC = 'c'.repeat(32);
const namedRecord = (id, host = 'alice/desk') => ({v:1,id,host,host_key:'SHA256:fixture',label:'Builder',ts:1});
const agentCatalogs = {'alice/desk':[namedRecord(namedA),namedRecord(namedB)],'me/laptop':[]};
const catalogResponder = {chosen:true,harness:'codex',dir:'/tmp/fixture',ready:true,timeout:600,context:['/tmp/context']};
const localAgents = [];
const replySessions = [{handle:"exact-Pi_handle",harness:"pi",label:"Local Pi",active:false},{handle:"exact-OMP_handle",harness:"omp",label:"Local OMP",active:true}];
let catalogRefuses = '', catalogPublished = false;
const teamChanges = []; // changes sent to /api/team
let uploads = 0, uploadFails = false; // files handed to /api/upload
let uploadTries = 0, uploadFailAt = 0; // the one upload try (counted from 1) that fails
let dmSendRefuses = ""; // what /api/dm/send refuses with, if set
let agentAskRefuses = ""; // the exact agent request may fail without retargeting its files
let sendRefuses = ""; // what /api/send refuses with, if set
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
  calls.push({ path: u.pathname, body, dir: u.searchParams.get("dir"), host: u.searchParams.get("host") });
  let data = {};
  if (u.pathname === "/api/thread") { // the daemon answers with the thread holding that message
    const id = u.searchParams.get("id");
    data = threads[id] || Object.values(threads).find((t) => t.messages.some((m) => m.id === id));
  }
  else if (u.pathname === "/api/overview") data = Object.assign({ version: serving }, overview);
  else if (u.pathname === "/api/send") {
    if (sendRefuses) return Promise.resolve({ ok: false, status: 409, text: async () => sendRefuses, statusText: "" });
    data = { id: "new", state: "delivered" };
  }
  else if (u.pathname === "/api/refresh") data = refreshReply;
  else if (u.pathname === "/api/dm") data = dmThreads[u.searchParams.get("id")] || {};
  else if (u.pathname === "/api/dm/send") {
    if (dmSendRefuses) return Promise.resolve({ ok: false, status: 409, text: async () => dmSendRefuses, statusText: "" });
    data = { id: "sent-dm", state: "custody" };
  }
  else if (u.pathname === "/api/dm/new") data = { id: "d3" };
  else if (u.pathname.startsWith("/api/dm/agent/")) {
    if (u.pathname.endsWith("/ask") && agentAskRefuses) return Promise.resolve({ ok: false, status: 409, text: async () => agentAskRefuses });
    data = u.pathname.endsWith("/ask") ? { id: "asked", state: "custody" } : {};
  }
  else if (u.pathname === "/api/notify/resolve") data = { conv: resolvable[u.searchParams.get("chan")] || "" };
  else if (u.pathname === "/api/operator/decide") data = { note: "Sent to the host.", sent: "dec1" };
  else if (u.pathname === "/api/teams") {
    if (teamsReply === 404) return Promise.resolve({ ok: false, status: 404, text: async () => "404 page not found", statusText: "" });
    data = teamsReply;
  }
  else if (u.pathname === "/api/team") {
    if (teamRefuses) return Promise.resolve({ ok: false, status: 409, text: async () => teamRefuses, statusText: "" });
    teamChanges.push(body);
    data = { realm_id: "r", id: body.team || "t-new", name: body.name || teamResultName || "Platform", seq: 1, hash: "h", managers: ["p-me"], members: ["p-me"], archived: false };
  }
  else if (u.pathname === "/api/teams/snapshot") data = { realm_id: "r", sources: body.teams.map((id) => ({ id, seq: 1, hash: "h" })), persons: teamSnapshotPersons, at: 1700000000 };
  else if (u.pathname === "/api/groups/new") {
    teamGroupCreates.push(body);
    data = { id: "team-group" };
    dmThreads[data.id] = { id: data.id, kind: "group", title: body.title, members: [{ person: "p-me", label: "Sergey", address: "me/laptop", admin: true }], messages: [], agents: [], role: "member", frozen: "" };
  }
  else if (u.pathname === "/api/groups/invite") {
    teamGroupInvites.push(body);
    if (body.person === teamGroupRefusesPerson) return Promise.resolve({ ok: false, status: 409, text: async () => "Group changed; fresh invitation and consent required.", statusText: "" });
    data = { id: "invite-" + body.person, status: "pending" };
  }
  else if (u.pathname === "/api/drive/setup") data = { runtime: "daemon", settings: { config: { enabled: false }, revision: 0, can_admin: true } };
  else if (u.pathname === '/api/responder') data = {...catalogResponder,harnesses:[{name:'codex',found:true}]};
  else if (u.pathname === '/api/reply-sessions') data = {host:'me/laptop',local:true,sessions:replySessions};
  else if (u.pathname === '/api/agents') {
    if (catalogRefuses) return Promise.resolve({ok:false,status:409,text:async()=>catalogRefuses});
    const host = u.searchParams.get('host');
    if (!body) data = host ? {host,local:false,agents:(agentCatalogs[host]||[]).map(record=>({record,enabled:true}))} : {host:'me/laptop',local:true,agents:localAgents,harnesses:[{name:'codex',found:true}]};
    else {
      let agent = localAgents.find(a=>a.record.id===body.id);
      if(body.action==='create') {agent={record:namedRecord(namedC,'me/laptop'),enabled:true,responder:{...catalogResponder,harness:body.harness,dir:body.dir}};localAgents.push(agent);}
      if(body.action==='update') agent.responder={...agent.responder,harness:body.harness,dir:body.dir};
      if(body.action==='disable') {agent.enabled=false;delete agent.responder;}
      data={saved:true,published:catalogPublished,agent,note:catalogPublished?'Hub publication confirmed.':'Publication not confirmed.'};
    }
  }
  else if (u.pathname === "/api/storage") {
    if (storageReply === 404) return Promise.resolve({ ok: false, status: 404, text: async () => "404 page not found", statusText: "" });
    data = storageReply;
  }
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
  document, fetch, console, setTimeout, clearTimeout, URL, URLSearchParams, sessionStorage, Notification, Blob,
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

  // R2 (FAIL04): the composer says who gets it and how, and the Send button
  // names the target; the kind changes the verb, answering changes it too.
  await run('openThread("a1")');
  check($("to-name").textContent === "alice/desk" && $("to-how").textContent.includes("continues") && $("send-label").textContent === "Send to alice/desk",
    "a device conversation names its recipient and what it continues: " + $("to-how").textContent);
  run("setKind")("task");
  check($("send-label").textContent === "Give task to alice/desk" && $("to-how").textContent.includes("accept first"), "a task says so on Send: " + $("send-label").textContent);
  run("setKind")("question");
  check($("send-label").textContent === "Ask alice/desk", "a question says so on Send");
  run("setKind")("message");
  // Text started for one target is not sent to another without a look:
  // the first send refuses and names the To line; the next one goes.
  await run('openThread("c1")');
  $("body").value = "port 8443";
  run("noteTyping()");
  run("setAnswering")(run("state.data.messages[0]"));
  check($("send-label").textContent === "Send answer", "answering by hand says so on Send");
  calls.length = 0;
  await run("send")(ev);
  check(sends().length === 0 && $("compose-error").textContent.includes("Check the To line"), "a draft started for another target is refused once: " + $("compose-error").textContent);
  await run("send")(ev);
  check(sends().length === 1, "the next send, after the person looked, goes (" + sends().length + ")");
  run("setAnswering")(null);
  $("body").value = "";
  run("noteTyping()");

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
  const mixedThreads = [...zen, ...reports, ...laptop];
  const contacts = run("contactsOf")(mixedThreads);
  const zc = contacts.find((c) => c.peer === "admin/zenbook");
  check(contacts.length === 3 && contacts.filter((c) => c.peer.startsWith("admin/")).length === 2, "one contact per exact address (admin/laptop apart from admin/zenbook)");
  check(zc.conversations.map((t) => t.id).sort().join() === "chain,held" && zc.singles.length === 30 && zc.reports.length === 0,
    "chain and open item listed as conversations, 30 singles kept single");
  const seen = contacts.flatMap((c) => [...c.conversations, ...c.singles, ...c.reports]).map((t) => t.id);
  check(seen.length === mixedThreads.length && new Set(seen).size === mixedThreads.length, "every thread reachable exactly once");
  check(zc.review === 1 && zc.unread === 3 && zc.notices === 0, "zenbook counts: 1 decision, 3 unread");
  const oc = contacts.find((c) => c.peer === "hub/ops");
  check(oc.review === 0 && oc.unread === 0 && oc.notices === 2 && oc.reports.length === 2, "reports counted apart from decisions and unread");

  // N2: search finds known agents and conversations, says which is which,
  // opens the exact one, and never offers people or reports.
  let r = run("searchKnown")("zenbook", mixedThreads);
  check(r.agents.length === 1 && r.agents[0].peer === "admin/zenbook" && r.conversations.length === 0, "search by address finds the agent");
  r = run("searchKnown")("admin", mixedThreads);
  check(r.agents.map((c) => c.peer).sort().join() === "admin/laptop,admin/zenbook", "a shared label finds both exact addresses, not one person");
  r = run("searchKnown")("rollout", mixedThreads);
  check(r.conversations.length === 1 && r.conversations[0].id === "chain", "search finds the conversation by its line");
  check(run("searchKnown")("request", mixedThreads).conversations.length === 0, "reports are not search results");
  check(run("searchKnown")("   ", mixedThreads).agents.length === 0, "an empty search shows nothing");
  overview.threads = mixedThreads;
  overview.review = [{ id: "held", peer: "admin/zenbook", kind: "question", why: "", excerpt: "" },
    { id: "n1", peer: "hub/ops", kind: "message", why: "", excerpt: "", notice: true },
    { id: "n2", peer: "hub/ops", kind: "message", why: "", excerpt: "", notice: true },
    { id: "n3", peer: "carol/ci", kind: "message", why: "", excerpt: "", notice: true }];
  mixedThreads.push(Object.assign({}, mixedThreads.find((t) => t.id === "chain"), { id: "c1", peer: "carol/ci", title: "which port?" }));
  await run("loadOverview()");
  $("search").value = "which port";
  run("state").query = "which port";
  run("rerenderContacts")();
  calls.length = 0;
  const opened = run("searchKnown")("which port", mixedThreads).conversations.find((t) => t.peer === "carol/ci");
  await run("openThread")(opened.id);
  check(calls.some((c) => c.path === "/api/thread") && run("state.data.peer") === "carol/ci" && run("state.thread") === "c1",
    "a search result opens exactly that conversation");
  run("clearSearch")();

  // N3: decisions and reports are counted apart; dismissing touches only
  // that sender's reports and only resolves them.
  run("renderReview")(overview.review);
  check(String($("review-count").textContent) === "4" && $("review-reports").hidden,
    "one Activity badge counts decisions and reports: " + [$("review-count").textContent, $("review-reports").textContent, $("review-reports").hidden].join("/"));
  calls.length = 0;
  await run("dismissReports")("hub/ops");
  const acts = calls.filter((c) => c.path === "/api/act").map((c) => c.body);
  check(acts.length === 2 && acts.every((b) => b.do === "resolve") && acts.map((b) => b.id).sort().join() === "n1,n2",
    "dismiss resolves exactly that sender's reports: " + JSON.stringify(acts));
  check(!calls.some((c) => c.path === "/api/send"), "dismissing sends nothing");

  // Nothing a condition leaves out reaches the page as text: search with
  // only conversation matches, the Zoom write dialog (and its kind picker),
  // a read dialog without a summary, and the contact list.
  overview.threads = mixedThreads;
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
  overview.threads = mixedThreads; overview.review = [];
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

  // A real update must not serialize away files or exact targets. Any
  // unsent draft holds both automatic and explicit reload in this window.
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
  check(reloads === 0 && !$('updating').hidden && $('reload').disabled && $('body').value === 'unsent to alice' && byId['write-body'].value === 'zoom text', 'unsent text, inactive conversation and dialog hold automatic update reload');
  check(run('reloadUpdated()') === false && reloads === 0, 'explicit reload cannot silently discard unsent drafts');
  // Existing text-only persistence remains readable; update no longer
  // treats this partial representation as an exact draft handoff.
  check(run('keepForReload()') && store.has('agentnet-reload'), 'existing text snapshot remains supported');
  run("state").drafts = {}; run("state").thread = null; run("state").data = null; run("state").draftKey = null;
  run("state").version = ""; run("state").newVersion = ""; // a fresh page learns its version again
  $("body").value = ""; $("dialog").open = false;
  await run("restoreAfterReload()");
  check($("body").value === "unsent to alice" && run("state.drafts")["c1"].text === "carol draft" && run("state.drafts")["c1"].kind === "task",
    "drafts are back after the reload");
  check($("dialog").open && byId["write-body"].value === "zoom text" && byId["write-kind"].value === "question",
    "the open dialog is back with its text: " + byId["write-body"].value);
  check(!store.has("agentnet-reload"), "kept text is restored once");
  $('dialog').open = false; run('state').dialogRestore = null;
  $('body').value = ''; run('state').drafts = {}; run('state').answering = null; run('state').dmReply = null; run('state').dmAgent = null; run('state').deviceAgentID = ''; run('state').typedFor = null;
  const unsentUpgrade = { name: 'upgrade.txt', size: 3, staged: 'old-stage', file: { marker: 'exact File object' } };
  run('state').files = [unsentUpgrade]; run('updated')('v3');
  check(reloads === 0 && run('state').files[0] === unsentUpgrade && $('reload').disabled && !run('reloadUpdated()'), 'file-only exact object survives version change and explicit reload refuses');
  run('state').files = [];
  for (const held of [{ agent: 'pid-exact' }, { agent_id: 'named-exact' }, { typedFor: { to: 'exact-target' } }, { reply: { id: 'exact-reply' } }]) {
    run('state').drafts = { held }; run('updated')('v3');
    check(reloads === 0 && run('state').drafts.held === held && !run('reloadUpdated()'), 'target-only saved draft holds exact memory through update');
  }
  run('state').drafts = {}; run('updated')('v3');
  check(reloads === 1 && !$('reload').disabled, 'empty drafts permit ordinary automatic version reload');
  run("newConversationDialog")("admin/zenbook");
  byId["new-body"].value = "new conversation text";
  storageBroken = true;
  reloads = 0;
  run("updated")("v3");
  check(reloads === 0 && !$("reload").hidden && !$("updating").hidden, "unsent dialog with unavailable storage remains in memory");
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
  let side = JSON.stringify($("profile-card").children.map(textOf));
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
  check(side.includes("2 chats") && side.includes("1 device") && !side.includes("deploy"),
    "the sidebar has one compact row per person, not their DMs: " + side);
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
  $("agents").querySelector(".assistant-participant").click();
  let ag = JSON.stringify($("dialog-body").children.map(textOf));
  $("dialog").close();
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
  $("agents").querySelector(".assistant-participant").click();
  ag = JSON.stringify($("dialog-body").children.map(textOf));
  $("dialog").close();
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

  // Human invitation is independent from assistant availability.
  overview.agents = false;
  dmThreads.d4.agents = [];
  await run("loadOverview()");
  await run('openDM("d4")');
  check(textOf($("agents")).includes("Add participants"), "original people can invite humans independently of assistant availability");
  run("participantsDialog")(run("state.dmData"));
  check(textOf($("dialog-body")).includes("Invite a person") && !textOf($("dialog-body")).includes("Invite an assistant"), "human invitation available; unsupported assistant invitation not offered");
  $("dialog").close();
  delete overview.agents;

  // Notifications (browser device): off until the person turns them on; the
  // browser's permission is asked only from that click; the page reports
  // only what the person has in front of them; a click opens the DM this
  // device resolves, never a guess.
  check(!$("notify-line").hidden && textOf($("notify-line")).includes("not available"), "unavailable notifications explain why instead of an empty settings tab");
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
  // The browser device writes to devices too (version 1), from Zoom as
  // anywhere; a refused send keeps the dialog and its text.
  overview.device = { online: true, persisted: true, revoked: false };
  await run("loadOverview()");
  check($("new-btn").hidden !== true, "a browser device can start a conversation with a device");
  await run('Zoom.go(2, { thread: "a1" })');
  const chatText = JSON.stringify(textOf($("zoom").children[$("zoom").children.length - 1]));
  check(!chatText.includes("[object Object]") && chatText.includes("alice/desk"), "Zoom's chat names a device message's author: " + chatText.slice(0, 300));
  run("writeDialog(state.data, null)");
  byId["write-body"].value = "hey!";
  sendRefuses = "alice/desk's key changed: nothing is sent until the new key is trusted.";
  calls.length = 0;
  await $("dialog-ok").onclick();
  check($("dialog").open && byId["write-body"].value === "hey!" && $("dialog-error").textContent.includes("key changed"),
    "a refused send keeps the dialog, its text and says why: " + $("dialog-error").textContent);
  sendRefuses = "";
  await $("dialog-ok").onclick();
  const zb = calls.filter((c) => c.path === "/api/send").pop();
  check(!$("dialog").open && zb.body.to === "alice/desk" && (zb.body.kind || "message") === "message" && zb.body.body === "hey!", "then it is sent: " + JSON.stringify(zb && zb.body));
  delete overview.device;
  await run("loadOverview()");
  run('setLens("classic")');
  await run('openDM("d2")');
  // Agent asks reuse the same staging, retry and file-only path as DM sends.
  dmThreads.d2.agents = [agentV("pidF", { state: "active", host: alice, host_here: false, inviter: me, can_ask: true })];
  await run('openDM("d2")');
  run("setDMAgent")(dmThreads.d2.agents[0]);
  check($("attach").hidden === false, "an exact active agent can receive attached files");
  run("addFiles")([fileOf("agent-only.txt", 5, "text/plain")]);
  $("body").value = "";
  uploadFails = true; calls.length = 0;
  await run("send")(ev);
  check(run("state.files").length === 1 && !calls.some(c => c.path === "/api/dm/agent/ask") && $("compose-error").textContent.includes("still here"), "failed agent staging keeps file and sends nothing");
  uploadFails = false; agentAskRefuses = "External host unavailable"; calls.length = 0;
  await run("send")(ev);
  let fileAsk = calls.find(c => c.path === "/api/dm/agent/ask");
  check(fileAsk && fileAsk.body.pid === "pidF" && fileAsk.body.kind === "question" && fileAsk.body.body === "" && fileAsk.body.files.length === 1 &&
    run("state.files").length === 1 && !run("state.files")[0].staged && run("state.dmAgent") === "pidF" && !calls.some(c => c.path === "/api/dm/send"), "refused file-only ask retains exact agent and file, consumed staging cleared");
  agentAskRefuses = ""; run("setKind")("task"); calls.length = 0;
  await run("send")(ev);
  fileAsk = calls.find(c => c.path === "/api/dm/agent/ask");
  check(fileAsk && fileAsk.body.pid === "pidF" && fileAsk.body.kind === "task" && fileAsk.body.body === "" && fileAsk.body.files.length === 1 &&
    calls.some(c => c.path === "/api/upload") && run("state.files").length === 0, "file-only task restages and sends to exact agent");
  run("setDMAgent")(null);
  dmThreads.d2.agents = [];
  // Headless (R05/R06/R09): a host's word on a request is shown apart from
  // delivery and says when it is old; a version 2 report names requests and
  // offers actions only where core marked them actionable; a decision is
  // sent bound to the state and attempt seen; a notification's #msg= lands
  // on the exact message and does nothing else.
  {
    const lensBefore = run("state.lens");
    run('setLens("classic")');
    threads.h1 = { id: "h1", peer: "carol/ci", key: { pinned: "SHA256:c" }, messages: [msg("h1", "carol/ci", "ready when you are"),
      msg("h9", "me/laptop", "run the nightly build", { dir: "out", kind: "task", state: "delivered", state_text: "Delivered to carol/ci",
        exec: { state: "running", at: new Date(Date.now() - 4 * 60e3).toISOString(), host: "carol/ci", stale: false, attempt: 2, detail: "" } })] };
    overview.threads.push({ id: "h1", peer: "carol/ci", last_at: T, title: "ready when you are", count: 2, review: 0, unread: 0, running: 0 });
    await run('openThread("h1")');
    let tl = JSON.stringify($("timeline").children.map(textOf));
    check(tl.includes("Working on carol/ci since") && tl.includes("Delivered to carol/ci") && !tl.includes("not confirmed"), "a running request shows the host's word beside delivery: " + tl.slice(-300));
    threads.h1.messages.at(-1).exec = { state: "needs_human", at: new Date(Date.now() - 3 * 3600e3).toISOString(), host: "carol/ci", stale: true, detail: "awaiting approval of a shell command" };
    await run("loadThread(false)");
    tl = JSON.stringify($("timeline").children.map(textOf));
    check(tl.includes("Needs a person there on carol/ci: awaiting approval of a shell command") && tl.includes("last known 3 h ago, not confirmed now"), "a stale needs-human state says when it was last known: " + tl.slice(-300));
    threads.h1.messages.at(-1).exec = undefined;
    // a version 2 report: read-only for a non-operator, actionable for an operator
    overview.threads.push({ id: "n2", peer: "hub/bot", last_at: T, title: "2 requests reported by hub/bot", count: 1, notice_only: true, notices: 1, review: 0, unread: 0, running: 0 });
    overview.review = [{ id: "n2", peer: "hub/bot", kind: "message", notice: true, at: T, excerpt: "2 requests reported by hub/bot",
      report: { host: "hub/bot", at: T, items: [
        { id: "a".repeat(32), from: "alice/desk", kind: "task", state: "awaiting", blocker: "awaiting_acceptance", since: T, actionable: false },
        { id: "b".repeat(32), from: "me/laptop", kind: "question", state: "needs_human", blocker: "needs_human", since: T, key: "SHA256:me", attempt: 3, excerpt: "which city?", actionable: true }] } }];
    await run("loadOverview()");
    let rl = textOf($("report-list")).replace(/\s+/g, " ");
    check(rl.includes("reported at") && rl.includes("2 requests waiting there") && rl.includes("waits for acceptance") && rl.includes("Its text is not shared with this device") && rl.includes("which city?"),
      "a report names its requests, states and blockers; text only where shared: " + rl.slice(0, 400));
    check(!/Accept and run/.test(rl) && rl.includes("Answer it there") && rl.includes("Close it there"), "actions appear only on the actionable item, matching its state: " + rl.slice(0, 500));
    const walk = (n, out = []) => { if (n && n.children) { out.push(n); n.children.forEach((c) => walk(c, out)); } return out; };
    const answerBtn = walk($("report-list")).find((e) => e.tagName === "button" && textOf(e).startsWith("Answer it there"));
    answerBtn.click();
    check($("dialog").open && textOf($("dialog-body")).includes("which city?") && textOf($("dialog-body")).includes("only if the request is still in that state"), "the decision dialog quotes the request and the binding");
    byId["decide-text"].value = "Riga";
    calls.length = 0;
    await $("dialog-ok").onclick(); await tick(); await tick();
    const dec = calls.find((c) => c.path === "/api/operator/decide");
    check(dec && dec.body.host === "hub/bot" && dec.body.id === "b".repeat(32) && dec.body.key === "SHA256:me" && dec.body.action === "reply" && dec.body.expect === "needs_human" && dec.body.attempt === 3 && dec.body.text === "Riga" && dec.body.report === "n2",
      "a decision is bound to host, request, key, state seen, attempt and the report: " + JSON.stringify(dec && dec.body));
    overview.review[0].report.items[1].result = { state: "running", at: T };
    await run("loadOverview()");
    rl = textOf($("report-list")).replace(/\s+/g, " ");
    check(rl.includes("Now Running"), "the host's real outcome shows on the item: " + rl.slice(0, 500));
    overview.review[0].report.items[1].result = { refused: "stale expect: the request is now running", at: T };
    await run("loadOverview()");
    check(textOf($("report-list")).includes("Refused: stale expect"), "a refusal shows as a refusal, not a state");
    const oldReport = { id: "c".repeat(32), peer: "hub/bot", kind: "message", notice: true, at: T, excerpt: "older snapshot", report: { host: "hub/bot", at: T, items: [{ id: "d".repeat(32), from: "alice/desk", kind: "task", state: "awaiting", key: "SHA256:a", attempt: 1, excerpt: "Earlier settled task", actionable: true, result: { refused: "no longer as you saw it" } }] } };
    const newReport = { id: "e".repeat(32), peer: "hub/bot", kind: "message", notice: true, at: T, excerpt: "newer snapshot", report: { host: "hub/bot", at: T, items: [{ id: "f".repeat(32), from: "me/laptop", kind: "task", state: "awaiting", key: "SHA256:me", attempt: 2, excerpt: "Newest pending task", actionable: true }] } };
    for (const order of [[oldReport, newReport], [newReport, oldReport]]) {
      overview.review = order; await run("loadOverview()");
      const tied = textOf($("report-list"));
      check(tied.includes("2 snapshots at that time; their order is not known") && tied.includes("Earlier settled task") && tied.includes("Newest pending task") && tied.includes("Refused: no longer as you saw it"), "same-second native/browser order retains both exact snapshots and host refusal");
      const pending = walk($("report-list")).find(e => e.tagName === "li" && e.className.includes("report-request") && textOf(e).includes("Newest pending task"));
      walk(pending).find(e => e.tagName === "button" && textOf(e).startsWith("Accept and run there")).click();
      calls.length = 0; await $("dialog-ok").onclick();
      const exact = calls.find(c => c.path === "/api/operator/decide");
      check(exact && exact.body.report === newReport.id && exact.body.id === newReport.report.items[0].id && exact.body.attempt === 2 && exact.body.host === "hub/bot", "tied pending request binds its own report, host, id and attempt");
    }
    overview.review = [newReport]; await run("loadOverview()");
    check(!textOf($("report-list")).includes("snapshots at that time") && !textOf($("report-list")).includes("Earlier settled task"), "dismissed tied report stays out of open snapshots; single report unchanged");
    // a notification's #msg= lands on the message, sends nothing
    ctx.location.hash = "#msg=h1";
    check(run("takeClicked()") === null, "a malformed #msg is ignored");
    ctx.location.hash = "#msg=" + "0".repeat(30) + "a1";
    const clicked = run("takeClicked()");
    check(clicked && clicked.msg === "0".repeat(30) + "a1" && ctx.location.hash === "", "#msg=ID is taken once from the address");
    ctx.location.hash = "#msg=" + "0".repeat(30) + "a1&conv=" + "d".repeat(64) + "&dir=out";
    const clicked2 = run("takeClicked()");
    check(clicked2.conv === "d".repeat(64) && clicked2.dir === "out", "conv and dir ride along when given");
    calls.length = 0;
    await run('openThread("a1")');
    await run("openClicked")({ msg: "h9", conv: "", dir: "" }); // a device-thread message here
    check(run("state.data && state.data.peer") === "carol/ci" && run("state.data.messages.some((m) => m.id === 'h9')") && sends().length === 0, "landing opens the exact conversation and sends nothing");
    overview.threads = overview.threads.filter((t) => t.id !== "h1" && t.id !== "n2"); overview.review = []; delete threads.h1;
    await run("loadOverview()");
    run("setLens")(lensBefore);
  }

  // Storage (STORAGE1-8): one explicit read, shown as the API words it;
  // unknown is never zero; the server's quota is never an allowance.
  {
    const area = (d, label, usage, extra) => Object.assign({ directory: d, label, kind: "ciphertext", status: usage ? "available" : "unavailable", usage, lifetime: "Kept until " + d + " rule." }, extra || {});
    const hub = { scope: "caller-owned-ciphertext", quota_scope: "hub-global", own: { stored: { files: 2, reserved_bytes: 4096, recorded_received_bytes: 4096 }, incomplete: { files: 0, reserved_bytes: 0, recorded_received_bytes: 0 } },
      quota_bytes: 1073741824, max_file_bytes: 104857600, upload_idle_ttl_seconds: 86400, location: "the Hub's blobs folder",
      policy: { delivered_attachments: "kept until the operator cleans up", unattached_attachments: "kept until cleanup", undelivered_attachments: "never removed", message_envelopes: "kept", incomplete_uploads: "reclaimed after idle", manual_delivered_age_default_seconds: 2592000, manual_unattached_age_default_seconds: 86400 } };
    const normal = { local: { scope: "agent-home-managed-files", location: "This installation's AgentNet home", complete: true, known: { files: 3, bytes: 3300 }, exclusions: "Keys and databases are not counted.",
      areas: [area("staging", "Files waiting to be sent", { files: 0, bytes: 0 }), area("kept", "Retained sent files", { files: 3, bytes: 3300 })] }, remote: { status: "available", usage: hub } };
    const textOfStorage = () => textOf($("storage")).replace(/\s+/g, " ");
    storageReply = normal;
    await run('settingsTab("storage")'); await tick(); await tick();
    let t = textOfStorage();
    check(t.includes("3 files · 3.2 KB") && t.includes("held by AgentNet here") && t.includes("Retained sent files") && t.includes("0 files · 0 B"), "a normal read shows the known local total and each area: " + t.slice(0, 200));
    check(t.includes("2 files · 4.0 KB reserved") && t.includes("quota is 1.00 GB for everyone together; that is not your allowance") && !t.includes("%"), "the server's own usage and the global quota are labelled apart, no percentage: " + t.slice(0, 400));
    check(!t.includes("you are an admin"), "a member sees no aggregate");
    check(t.includes("delivered files older than 30 days") && t.includes("Nothing expires by itself"), "operator cleanup defaults are named as defaults, not schedules");
    // partial: one area could not be inspected: unknown, not zero, and the total is not complete
    storageReply = { ...normal, local: { ...normal.local, complete: false, known: { files: 3, bytes: 3300 }, areas: [normal.local.areas[1], area("downloads", "Retained received files", null, { reason: "The managed directory could not be inspected." })] } };
    await run('renderStorage()'); await tick(); await tick();
    t = textOfStorage();
    check(t.includes("could not be inspected") && /Retained received files encrypted unknown/.test(t) && t.includes("not complete") && !/Retained received files encrypted 0 files/.test(t), "an uninspected area says unknown, never zero, and the total is marked incomplete: " + t.slice(0, 300));
    // offline: the server did not answer; local stays
    storageReply = { ...normal, remote: { status: "unavailable", reason: "Storage usage could not be verified with the Hub." } };
    await run('renderStorage()'); await tick(); await tick();
    t = textOfStorage();
    check(t.includes("3 files · 3.2 KB") && t.includes("could not be verified") && !t.includes("quota"), "an unreachable server leaves local counts and says the remote is not known: " + t.slice(0, 300));
    // unsupported server
    storageReply = { ...normal, remote: { status: "unsupported", reason: "This Hub does not report storage usage." } };
    await run('renderStorage()'); await tick(); await tick();
    check(textOfStorage().includes("does not report storage usage"), "an older server is named as not reporting");
    // admin: the aggregate appears, labelled
    storageReply = { ...normal, remote: { status: "available", usage: { ...hub, global: { stored: { files: 40, reserved_bytes: 8388608, recorded_received_bytes: 8388608 }, incomplete: { files: 1, reserved_bytes: 100, recorded_received_bytes: 50 } } } } };
    await run('renderStorage()'); await tick(); await tick();
    t = textOfStorage();
    check(t.includes("you are an admin") && t.includes("40 files · 8.0 MB reserved") && t.includes("unfinished: 1 file"), "an admin sees everyone's usage, labelled: " + t.slice(0, 400));
    // provider without the route
    storageReply = 404;
    await run('renderStorage()'); await tick(); await tick();
    check(textOfStorage().includes("does not report storage yet"), "a provider without storage says so honestly");
    check(!/\/home\/|\/tmp\/|t=[0-9a-f]{8}|fingerprint|agentnet_ui/.test(textOfStorage()), "no paths, tokens or keys in the storage view");
    storageReply = null;
    calls.length = 0;
  }

  // Message controls (MEL-476/477): what the backend resolved is shown as
  // is; edits show the new text with the original under Details; a deleted
  // message is a tombstone without files; only allowed actions appear.
  {
    const m2c = dmThreads.d2.messages.find((x) => x.id === "m2"), m3c = dmThreads.d2.messages.find((x) => x.id === "m3");
    Object.assign(m2c, { edited: true, revision: 2, text: "budget (revised)", can: ["react", "edit", "delete"], reactions: [{ emoji: "👍", by: [{ id: "p-al", label: "Alice" }], mine: false }, { emoji: "🎉", by: [{ id: "p-me", label: "Sergey" }, { id: "p-al2", label: "Sergey" }], mine: true }] });
    Object.assign(m3c, { can: ["react"], attachments: [{ index: 0, name: "photo.png", size: 10 }] });
    await run('openDM("d2")');
    let tl = JSON.stringify($("timeline").children.map(textOf));
    check(tl.includes("budget (revised)") && tl.includes("· edited"), "an edited message shows its new text with an edited marker: " + tl.slice(0, 200));
    check(tl.includes("👍 1") && tl.includes("🎉 2"), "reactions show emoji and counts: " + tl.slice(0, 200));
    check(tl.includes("Original text") && tl.includes("budget"), "the original text stays under Details");
    const walk = (n, out = []) => { if (n && n.children) { out.push(n); n.children.forEach((c) => walk(c, out)); } return out; };
    const nodes = walk($("timeline"));
    const mine = nodes.find((e) => e.tagName === "button" && (e.className || "").includes("reaction") && (e.className || "").includes("mine"));
    calls.length = 0;
    mine.click(); await tick();
    let c = calls.find((x) => x.path === "/api/message/react");
    check(c && c.body.conv === "d2" && c.body.id === "m2" && c.body.dir === "out" && c.body.emoji === "🎉" && c.body.remove === true, "pressing your own reaction removes it, naming conv, id and dir: " + JSON.stringify(c && c.body));
    const theirs = nodes.find((e) => e.tagName === "button" && (e.className || "").includes("reaction") && !(e.className || "").includes("mine"));
    calls.length = 0;
    theirs.click(); await tick();
    c = calls.find((x) => x.path === "/api/message/react");
    check(c && c.body.emoji === "👍" && !c.body.remove, "pressing another's reaction adds yours");
    const editBtns = nodes.filter((e) => e.tagName === "button" && textOf(e) === "Edit");
    const delBtns = nodes.filter((e) => e.tagName === "button" && textOf(e) === "Delete…");
    check(editBtns.length === 1 && delBtns.length === 1, "edit and delete are offered only where allowed (" + editBtns.length + "," + delBtns.length + ")");
    // a deleted message: tombstone, no files, no actions, the sent text of a question stays on record
    Object.assign(m3c, { deleted: true, kind: "question" });
    await run("loadDM(false)");
    tl = JSON.stringify($("timeline").children.map(textOf));
    check(tl.includes("Message deleted") && !tl.includes("photo.png") && !tl.includes("can you check?".slice(0, 12) + "\"") , "a deleted message is a tombstone without its files: " + tl.slice(0, 200));
    check(tl.includes("What was sent to their agent") && tl.includes("can you check?"), "a deleted question keeps what was sent under Details");
    delete m2c.edited; delete m2c.revision; delete m2c.text; delete m2c.can; delete m2c.reactions;
    delete m3c.deleted; delete m3c.can; delete m3c.attachments; m3c.kind = "question";
  }

  // Received files: a picture shown in place, anything else saved; sent ones not reopened.
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10, 0, 0]);
  served["/api/files/m3/0"] = png;
  served["/api/files/m3/1"] = new TextEncoder().encode("<svg onload=alert(1)>");
  const m3f = dmThreads.d2.messages.find((x) => x.id === "m3");
  m3f.attachments = [{ index: 0, name: "photo.png", size: 10 }, { index: 1, name: "logo.svg", size: 21 }];
  dmThreads.d2.messages.find((x) => x.id === "m2").attachments = [{ index: 0, name: "mine.txt", size: 5 }];
  await run('openDM("d2")');
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("photo.png") && tl.includes("logo.svg") && tl.includes("mine.txt") && (tl.match(/Open/g) || []).length === 2 && tl.includes("mine.txt 5 B Sent"),
    "received files offer Open; a sent one without a kept copy says only Sent: " + tl);
  // A sent file opens only when this device says it kept a copy; one it
  // did not keep says so instead of offering a dead Open.
  dmThreads.d2.messages.find((x) => x.id === "m2").attachments = [{ index: 0, name: "mine.txt", size: 5, openable: true }];
  await run("loadDM(false)");
  tl = JSON.stringify($("timeline").children.map(textOf));
  check((tl.match(/Open/g) || []).length === 3, "a kept sent file offers Open: " + tl);
  dmThreads.d2.messages.find((x) => x.id === "m2").attachments = [{ index: 0, name: "mine.txt", size: 5, openable: false, note: "Not kept on this device." }];
  await run("loadDM(false)");
  tl = JSON.stringify($("timeline").children.map(textOf));
  check((tl.match(/Open/g) || []).length === 2 && tl.includes("Not kept on this device"), "a sent file without a kept copy says so: " + tl);
  dmThreads.d2.messages.find((x) => x.id === "m2").attachments = [{ index: 0, name: "mine.txt", size: 5 }];
  await run("loadDM(false)");
  const slot0 = document.createElement("span"), btn0 = document.createElement("button");
  calls.length = 0;
  await run("openFile")("m3", 0, "photo.png", btn0, slot0, "in");
  check(calls.some((c) => c.path === "/api/files/m3/0" && c.dir === "in"), "opening names the message's own direction: " + JSON.stringify(calls));
  check(slot0.children.some((c) => c.tagName === "img" && String(c.attrs.src).startsWith("blob:")), "a PNG, by its bytes, is shown in place from a blob: URL");
  const slot1 = document.createElement("span"), btn1 = document.createElement("button");
  await run("openFile")("m3", 1, "logo.svg", btn1, slot1);
  check(!slot1.children.some((c) => c.tagName === "img"), "an SVG is never shown as a picture: it is saved");
  const dl = slot1.children.find((c) => c.tagName === "a");
  check(dl && dl.attrs.download === "logo.svg" && textOf(dl) === "Download logo.svg" && btn1.hidden, "a non-picture becomes a Download link the person clicks, nothing is claimed saved");
  check(run("state.opened").length === 2, "opened files are tracked to be freed");
  await run('openDM("d1")');
  check(run("state.opened").length === 0, "leaving the conversation frees them");
  // History files (MEL-433): asked for from the device of yours they came
  // from; one asked for, or that no device has, says so; none opens yet.
  m3f.synced_from = "alice/laptop";
  m3f.attachments = [{ index: 0, name: "old.txt", size: 3, availability: "requestable" }, { index: 1, name: "asked.txt", size: 3, availability: "requested" },
    { index: 2, name: "gone.txt", size: 3, availability: "unavailable", note: "Your server no longer holds it, and this browser did not keep it." }];
  await run('openDM("d2")');
  tl = JSON.stringify($("timeline").children.map(textOf));
  check(tl.includes("Get it from laptop") && tl.includes("Asked laptop for it") && tl.includes("Not available") && tl.includes("did not keep it") &&
    !/old\.txt[^"]*Open/.test(tl) && (tl.match(/Open/g) || []).length === 0, "history files: asked for, asked, not available; none opens: " + tl);
  const getIt = (function tree(n, out = []) { if (n && n.children) { out.push(n); n.children.forEach((c) => tree(c, out)); } return out; })($("timeline"))
    .find((e) => e.tagName === "button" && textOf(e) === "Get it from laptop");
  calls.length = 0;
  getIt.click();
  await new Promise((r) => setTimeout(r, 0));
  check(calls.some((c) => c.path === "/api/file/request" && c.body.id === "m3" && c.body.index === 0), "Get it asks for that file: " + JSON.stringify(calls));
  delete m3f.synced_from;
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
  check((tl.includes("Reminder today") || tl.includes("Reminder tomorrow")) && tl.includes("Change…") && tl.includes("Done") && (tl.match(/Remind me…/g) || []).length === 0,
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
  side = JSON.stringify($("profile-card").children.map(textOf));
  check(side.includes("Who uses this computer?") && side.includes("I do: set up my person") && side.includes("It is a service or bot") &&
    side.includes("Add this device from there"), "setup asks person or service, and points to adding a device: " + side);
  calls.length = 0;
  run("serviceDialog()");
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/device/service"), "a service is chosen only by its button");
  overview.role = "service";
  await run("loadOverview()");
  side = JSON.stringify($("profile-card").children.map(textOf));
  check(side.includes("This computer is a service or bot") && !side.includes("set up my person"), "a service is never asked to set up a person: " + side);
  // A browser is always a person's device; one waiting for approval sets up nothing.
  Object.assign(overview, { role: "unset", device: { online: true, persisted: true, revoked: false } });
  await run("loadOverview()");
  side = JSON.stringify($("profile-card").children.map(textOf));
  check(side.includes("Set up your person") && !side.includes("service or bot") && !side.includes("Add this device from there"), "a browser is offered a person only: " + side);
  overview.link = { state: "pending" };
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(side.includes("Waiting for your other device to approve this one") && !side.includes("Set up your person"), "a browser waiting for approval sets up nothing: " + side);
  delete overview.link;
  delete overview.device;
  overview.role = "service";
  const bobPerson2 = { person: "p-bob", label: "Bob", address: "bob/desk", state: "pinned", devices: [
    { address: "bob/desk", name: "desk", fingerprint: "SHA256:b1" }, { address: "bob/phone", name: "phone", fingerprint: "SHA256:b2" }] };
  Object.assign(overview, { role: "person", person: Object.assign({}, keepPerson, { devices: [
      { address: "me/laptop", name: "laptop", fingerprint: "SHA256:me", this: true }, { address: "me/phone", name: "phone", fingerprint: "SHA256:m2" }] }),
    people: [alicePerson, bobPerson2], links: [{ id: "L1", address: "me/tablet", name: "tablet", fingerprint: "SHA256:t1", requested_at: T, expires: T, state: "pending" }],
    history: [{ device: "me/phone", name: "phone", done: 3, total: 12, state: "running" }],
    threads: [sum("b9", "bob/phone", { title: "phone hello" }), sum("h1", "hub/ops", { title: "service report" }), sum("m9", "me/phone", { title: "to my phone" })] });
  await run("loadOverview()");
  side = JSON.stringify($("conv-list").children.map(textOf));
  check(JSON.stringify(textOf($("profile-card"))).includes("2 devices") && side.includes("A new device,  tablet , asks to join as you"),
    "you, your devices (this one marked) and a new device's request: " + side);
  check(side.includes("2 devices") && side.replace(/\s+/g, "").includes("hub/ops") && !side.replace(/\s+/g, "").includes("bob/phone"),
    "a person's devices are named under them; their device is not a row of its own; a service is: " + side);
  // Your own devices' conversations are under you too, one click away.
  const meTree = (function tree(n, out = []) { if (n && n.children) { out.push(n); n.children.forEach((c) => tree(c, out)); } return out; })($("profile-devices"));
  const mine = meTree.find((e) => e.tagName === "details" && textOf(e).includes("You on 2 devices"));
  const toPhone = mine && (function tree(n, out = []) { if (n && n.children) { out.push(n); n.children.forEach((c) => tree(c, out)); } return out; })(mine)
    .find((e) => e.tagName === "button" && textOf(e) === "1 device conversation");
  check(!!toPhone && mine.attrs.open === undefined, "your devices, closed, each with its conversations: " + (mine ? JSON.stringify(textOf(mine)) : "none"));
  toPhone.click();
  check(run("state.hub.kind") === "device" && run("state.hub.key") === "me/phone", "one click opens your device's conversations");
  calls.length = 0;
  run("linkDialog")(overview.links[0]);
  const ask = JSON.stringify($("dialog-body").children.map(textOf));
  check(ask.includes("tablet") && ask.includes("Approve it only if") && ask.includes("Details") && !calls.some((c) => c.path === "/api/device/decide"),
    "the request says what approving means, keys under Details, and waits: " + ask);
  await $("dialog-ok").onclick();
  check(calls.some((c) => c.path === "/api/device/decide" && c.body.id === "L1" && c.body.accept === true), "approving sends that decision");
  // A message you sent from your other device says so; its copies, one per
  // device, only in Details.
  const viaMsg = { id: "v1", dir: "out", from: "me/phone", via: "me/phone", kind: "message", body: "from my phone", at: T, origin: "ui", state: "custody",
    copies: [{ to: "alice/desk", state: "delivered" }, { to: "me/laptop", state: "custody" }] };
  check(run("dmAuthor")(viaMsg, { peer: alicePerson }) === "You, on your phone", "sent from your phone: " + run("dmAuthor")(viaMsg, { peer: alicePerson }));
  const det = JSON.stringify(textOf(run("dmDetails")(viaMsg)));
  check(det.includes("Sent from") && det.includes("your phone (me/phone)") && det.includes("alice/desk: delivered") && det.includes("me/laptop: on your server"),
    "Details name the device it came from and each copy: " + det);
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
  // Your own device's conversations: yours, and back goes to the list (you
  // have no DM page of your own to go back to).
  run('openHub({ kind: "device", key: "me/phone" })');
  check($("hub-back").hidden && $("conv-topic").textContent.startsWith("Your device") && !JSON.stringify($("hub").children.map(textOf)).includes("not on this computer"),
    "your device's conversations say so and link back to nothing that is not there: " + $("conv-topic").textContent);
  run("backOneLevel()");
  check(!(run("state.hub") && run("state.hub.kind") === "person"), "back from your device goes to the list, not to a page of yours: " + JSON.stringify(run("state.hub")));
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
  run('state.contactView = "people"');
  run("Zoom.go(0, {})");
  let zoom = JSON.stringify($("zoom").children.map(textOf));
  check(zoom.includes("People") && zoom.includes("Alice") && zoom.includes("Vitalii"), "Zoom shows people apart from devices: " + zoom.slice(0, 300));
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

  // A realistic directory must not turn the everyday list into a wall.
  const scale = require("./scale_fixture.cjs")(overview);
  Object.assign(overview, scale.overview, { persons: true, person: { person: "self-scale", label: "You", address: "me/laptop", state: "self" } });
  Object.assign(dmThreads, scale.conversations);
  run('state.contactView = "recent"; state.contactLimit = 20; state.query = ""');
  await run("loadOverview()");
  const contactRows = () => $("conv-list").children.filter((n) => (n.className || "").includes("contact-item"));
  check(contactRows().length === 20 && textOf($("conv-list")).includes("60 remaining"), "80 contacts render 20 rows, with more reachable");
  check(textOf(contactRows()[0]).includes("service29"), "people and services sort together by newest activity");
  find($("conv-list"), (n) => n.tagName === "button" && textOf(n).startsWith("Show more")).click();
  check(contactRows().length === 40, "show more reveals the next contacts");
  find($("conv-list"), (n) => n.tagName === "button" && textOf(n) === "Unread").click();
  check(contactRows().length === 15 && run("state.contactLimit") === 20, "unread shows ten people and five services, reset to first page");
  check(run("sidebarEntries")(overview.threads).every((e) => e.unread > 0), "read conversations stay out of Unread");
  run('selectSection("people")');
  check(contactRows().length === 20 && textOf(contactRows()[0]).includes("Person 00"), "directory starts alphabetically and remains bounded");
  const scaleSearch = run("searchKnown")("person37/tablet", overview.threads, overview.directory);
  check(scaleSearch.people.length === 1 && scaleSearch.people[0].person === "person-37", "search any linked device finds its person");
  const oldChat = run("searchKnown")("Weekend plans 37", overview.threads, overview.directory);
  check(oldChat.dms.length === 1 && oldChat.dms[0].id === "scale-37-0", "search reaches an older chat beyond the visible page");
  run("choosePerson")(scaleSearch.people[0]);
  check(textOf($("hub")).includes("Weekend plans 37") && textOf($("hub")).includes("Person 37 on 3 devices"), "person page retains all chats and linked devices");
  const entries = run("sidebarEntries")(overview.threads);
  check(entries.length === 80 && entries.filter((e) => e.person).length === 50, "150 owned devices do not become duplicate top-level contacts");
  run('state.contactView = "recent"; state.contactLimit = 20');
  const zoomScale = run("Zoom.everyone()");
  check(textOf(zoomScale).includes("60 remaining") && !textOf(zoomScale).includes("Person 00"), "Zoom uses the same bounded activity model");
  run("renderReview")([{ notice: true, peer: "service00/bot" }]);
  check($("review-word").textContent === "Reports", "open report never sits beside Nothing needs you");
  run("renderReview")([]);
  check($("review-word").textContent === "Nothing needs you" && $("review-reports").hidden, "empty review clears report attention");
  // Dismissed reports are history: not a line of their own, but listed,
  // folded, under the current ones; a current one names its time and
  // what this page cannot do, and opens the conversation instead.
  const dismissed = run("reportLine")({ peer: "old/server", conversations: [], singles: [], reports: [{ id: "old", last_at: T, title: "Old report", notices: 0 }] });
  check(dismissed === null, "a dismissed-only sender has no current report line");
  overview.threads.push({ id: "old", peer: "old/server", last_at: T, title: "Old report", count: 1, notice_only: true, notices: 0, review: 0, unread: 0, running: 0 });
  await run("loadOverview()");
  check(!$("reports-earlier").hidden && textOf($("report-earlier-list")).includes("Old report") && textOf($("report-earlier-list")).includes("old/server"),
    "dismissed reports are listed under Earlier reports: " + textOf($("report-earlier-list")));
  overview.threads.push({ id: "cur", peer: "svc/bot", last_at: T, title: "3 requests wait", count: 1, notice_only: true, notices: 1, review: 0, unread: 0, running: 0 });
  overview.review = [{ id: "cur", peer: "svc/bot", kind: "message", notice: true, at: T, excerpt: "3 requests wait for a person on svc/bot." }];
  await run("loadOverview()");
  const reportText = textOf($("report-list"));
  check(reportText.includes("svc/bot") && reportText.includes("reported at") && reportText.includes("cannot see") && !reportText.includes("Decide there") && reportText.includes("Dismiss 1 report here"),
    "a current report says when, what this page cannot do, and offers only honest actions: " + reportText);
  overview.threads.splice(-2, 2); overview.review = [];

  // Workspaces: the host (loader.js) keeps one immutable transport per
  // membership and says which is shown; the page keeps one view per
  // membership. Here: two memberships A and B over stand-in hosts that
  // record which one carried each call.
  const wsCalls = { A: [], B: [] };
  const wsStates = { A: {}, B: {} };
  const wsListeners = new Set();
  let wsActive = "A", wsHooks = {};
  const wsHost = (id) => Object.freeze({ workspace: { id, name: "Workspace " + id }, platform: "daemon",
    api: async (path, body) => {
      wsCalls[id].push({ path, body });
      const r = await fetch(path, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!r.ok) throw new Error(await r.text());
      return r.json();
    },
    listen: () => () => {},
    stage: async (f) => { wsCalls[id].push({ path: "stage", name: f.name }); return "staged-" + id; },
    file: async () => ({ bytes: new Uint8Array([1]) }),
    workspaces: {
      list: () => [{ id: "A" }, { id: "B" }], active: () => wsActive, has: (w) => w === "A" || w === "B",
      state: (w) => { if (!wsStates[w]) throw new Error("Unknown workspace"); return wsStates[w]; },
      onChange: (f) => { wsListeners.add(f); return () => wsListeners.delete(f); },
      select: (w) => { // as loader.js does: capture first, then the shell's change event
        const previous = wsActive;
        if (wsHooks.capture) wsHooks.capture(previous, wsStates[previous]);
        wsActive = w;
        ctx.window.agentnet = wsHost(w);
        for (const f of wsListeners) f({ id: w, previous, host: ctx.window.agentnet, state: wsStates[w] });
      },
    } });
  ctx.window.agentnetWorkspace = { hook: (h) => { wsHooks = h; if (h.switched) wsListeners.add(h.switched); } };
  ctx.window.agentnet = wsHost("A");
  run('window.agentnetWorkspace.hook({ capture: workspaceCapture, switched: (e) => { state.switching = workspaceSwitched(e); } })');
  const select = async (w) => { run('window.agentnet.workspaces.select("' + w + '")'); await run("state.switching"); };
  overview.me.address = "me/A";
  await run('openThread("a1")');
  $("body").value = "for alice in A";
  $("timeline").scrollTop = 120;
  wsCalls.A.length = 0;
  overview.me.address = "me/B";
  await select("B");
  check(run("state.thread") === null && $("body").value === "" && $("me").textContent === "me/B", "B starts with its own empty view, loaded over B's host");
  check(wsStates.A.view && wsStates.A.view.drafts.a1.text === "for alice in A" && wsStates.A.view.thread === "a1" && wsStates.A.view.scroll === 120, "A's view (conversation, draft, scroll) is kept for A");
  check(wsCalls.A.length === 0 && wsCalls.B.some((c) => c.path === "/api/overview"), "nothing of B's load went through A's host");
  // A send started in B keeps B's host and B's draft, whatever is shown when it ends.
  await run('openThread("c1")');
  $("body").value = "for carol in B";
  hold["/api/send"] = true;
  calls.length = 0;
  const inB = run("send")(ev);
  await tick();
  check(wsCalls.B.some((c) => c.path === "/api/send" && c.body.to === "carol/ci" && c.body.body === "for carol in B") && !wsCalls.A.some((c) => c.path === "/api/send"), "the send goes through B's host");
  overview.me.address = "me/A";
  await select("A");
  check(run("state.thread") === "a1" && $("body").value === "for alice in A" && run("state.data.peer") === "alice/desk" && !$("send").disabled && $("timeline").scrollTop === 120,
    "back in A: its conversation, draft and scroll, and Send is free (B's send is B's)");
  hold["/api/send"] = false;
  release("/api/send");
  await inB;
  check($("body").value === "for alice in A" && run("state.thread") === "a1" && !run("state.sending"), "B's finished send changes nothing shown in A");
  check(!(wsStates.B.view.drafts.c1) && wsStates.B.view.sending === false, "B's kept view has its sent draft gone and no send under way");
  overview.me.address = "me/B";
  await select("B");
  check(run("state.thread") === "c1" && $("body").value === "" && !$("send").disabled, "B shows its conversation with the sent text gone");
  // An answer that arrives for a workspace no longer shown draws nothing.
  hold["/api/overview"] = true;
  overview.me.address = "me/B-late";
  const late = run("loadOverview()"); // B asks; answered after A is shown
  await tick();
  overview.me.address = "me/A";
  const switching = select("A"); // A's own load is held too
  await tick();
  release("/api/overview"); // B's late answer first
  await late;
  check($("me").textContent !== "me/B-late", "B's late answer is not drawn into A");
  release("/api/overview"); // A's own answer
  hold["/api/overview"] = false; // A's conversation loads its overview once more, unheld
  await switching;
  check($("me").textContent === "me/A" && run("state.thread") === "a1", "A's own answer is drawn");
  // B's pending/failed load may show no presentation belonging to A.
  $('body').value = 'private A draft'; run('keepDraft()');
  $('timeline').append('private A file plaintext'); $('comic').append('private A comic'); $('zoom').append('private A zoom');
  fillTestHeader();
  function fillTestHeader() { $('conv-name').textContent = 'private A header'; $('conv-avatar').textContent = 'A'; }
  const normalB = wsHost('B'); let failB;
  ctx.window.agentnet = { ...normalB, api: (p, body) => p === '/api/overview' ? new Promise((_resolve, reject) => { failB = reject; }) : normalB.api(p, body) };
  wsActive = 'B';
  const unavailableB = run('workspaceSwitched')({ id: 'B', previous: 'A', state: wsStates.B });
  await tick();
  check(textOf($('timeline')) === '' && textOf($('comic')) === '' && !textOf($('zoom')).includes('private A') && textOf($('conv-name')) === 'Choose a conversation' && textOf($('conv-avatar')) === '' && $('composer').hidden && $('agents').hidden && $('timeline').hidden && $('comic').hidden && $('conversation-details').disabled, 'pending B synchronously removes A plaintext/header/actions across lenses');
  failB(Error('synthetic B offline')); await unavailableB;
  check(!$('lost').hidden && !textOf($('hub')).includes('private A') && textOf($('hub')).includes('unavailable') && $('conversation-details').disabled && $('profile-btn').disabled && wsStates.A.view.drafts.a1.text === 'private A draft', 'failed B remains neutral; saved A draft retained');
  ctx.window.agentnet = normalB;
  await select('A');
  check(run('state.thread') === 'a1' && $('body').value === 'private A draft' && !$('conversation-details').disabled, 'returning to A restores its exact view and draft after B failure');
  // A notification names its workspace: unknown opens nothing (not the
  // current one); known switches there first.
  const dmBase = (overview.dms || []).find((d) => d.id !== clickedID) || (overview.dms || [])[0]; // the DM of that alert, in both memberships' overviews here
  if (dmBase && !overview.dms.some((d) => d.id === clickedID)) overview.dms.push(Object.assign({}, dmBase, { id: clickedID }));
  dmThreads[clickedID] = dmThreads[clickedID] || Object.assign({}, dmThreads[dmBase && dmBase.id] || dmThreads.d2, { id: clickedID });
  ctx.location.hash = "#conv=" + clickedID + "&workspace=Z";
  run("clickedLater()");
  await pause(20);
  check(run("state.dm") === null && run("state.thread") === "a1" && ctx.location.hash === "" && $("live").textContent.includes("nothing was opened"), "an unknown workspace in a notification opens nothing: " + $("live").textContent);
  ctx.location.hash = "#conv=" + clickedID + "&workspace=B";
  run("clickedLater()");
  await pause(40);
  check(wsActive === "B" && run("state.dm") === clickedID, "a known workspace is shown first, then its conversation: " + JSON.stringify({ wsActive, dm: run("state.dm"), thread: run("state.thread"), live: $("live").textContent, overview: !!run("state.overview") }));
  ctx.location.hash = "#msg=" + "0".repeat(30) + "a1&workspace=A";
  const wsClicked = run("takeClicked()");
  check(wsClicked && wsClicked.workspace === "A" && wsClicked.target.msg === "0".repeat(30) + "a1", "#msg= carries its workspace too");
  ctx.location.hash = "#review&workspace=Z";
  const unknownReview = run("takeClicked()");
  check(unknownReview && unknownReview.refused, "unknown review workspace refuses instead of showing current workspace");
  ctx.location.hash = "#review&workspace=A";
  const workspaceReview = run("takeClicked()");
  check(workspaceReview.workspace === "A" && workspaceReview.target.review, "review notification carries its workspace");
  calls.length = 0;
  await run("openClicked")(workspaceReview);
  check(wsActive === "A" && !$("review").hidden && sends().length === 0, "review opens after switching workspace without sending");
  run("toggleReview(false)");
  // A reload keeps the workspace with the drafts: they come back there.
  await select("A");
  await run('openThread("a1")');
  $("body").value = "kept over reload";
  check(run("keepForReload()") && JSON.parse(store.get("agentnet-reload")).workspace === "A", "what is kept names its workspace");
  await select("B");
  check(await run("restoreAfterReload()") && wsActive === "A" && run("state.thread") === "a1" && $("body").value === "kept over reload", "restored in the workspace it was kept for");
  $("body").value = "";
  const activeBeforeUpgrade = run('state.drafts'), inactiveBeforeUpgrade = wsStates.B.view.drafts, versionBeforeUpgrade = run('state.newVersion');
  run('state').drafts = {}; run('state').files = []; run('state').answering = null; run('state').dmReply = null; run('state').dmAgent = null; run('state').deviceAgentID = ''; run('state').typedFor = null;
  $('dialog').open = false; run('state').dialogRestore = null;
  const exactInactive = { text: 'private B draft', kind: 'task', agent_id: 'exact-B-agent', typedFor: { to: 'B target' }, files: [unsentUpgrade] };
  wsStates.B.view.drafts = { inactive: exactInactive }; reloads = 0; run('updated')('v-next');
  check(reloads === 0 && $('reload').disabled && !run('reloadUpdated()') && wsStates.B.view.drafts.inactive === exactInactive && exactInactive.files[0] === unsentUpgrade, 'inactive workspace exact target/file draft holds automatic and explicit update reload while current workspace is empty');
  run('state').drafts = activeBeforeUpgrade; wsStates.B.view.drafts = inactiveBeforeUpgrade; run('state').newVersion = versionBeforeUpgrade;
  delete ctx.window.agentnet; delete ctx.window.agentnetWorkspace;

  // Teams (R04): People lists the workspace's teams as verified; create,
  // join, leave and manage go to the server and its answer stands; a
  // refusal (the last manager) is shown as it came; unsupported and
  // unavailable servers are said so; a selection expands reviewed people
  // before separate explicit group invitations.
  const peopleBefore = overview.people, personBefore = overview.person; // the fixture's people stay as they are for later checks
  overview.person = { person: "p-me", label: "Sergey", address: "me/laptop", state: "self", published: true };
  overview.people = [{ person: "p-alice", label: "Alice", address: "alice/desk", state: "pinned" }];
  teamsReply.teams = [{ realm_id: "r", id: "t1", name: "Platform", seq: 2, hash: "h1", managers: ["p-alice"], members: ["p-alice", "p-me"], archived: false, member: true, manager: false, conflict: false, listed: true },
    { realm_id: "r", id: "t2", name: "Ops", seq: 0, hash: "h2", managers: ["p-me"], members: ["p-me", "p-alice"], archived: false, member: true, manager: true, conflict: false, listed: true }];
  run('selectSection("people")');
  await tick();
  await run("loadTeams()");
  await run("loadOverview()");
  const teamsText = () => { const i = textOf($("conv-list")).indexOf("Teams"); return i < 0 ? "(no Teams block)" : textOf($("conv-list")).slice(i); };
  let tList = teamsText();
  check(tList.includes("Platform") && tList.includes("2 members") && tList.includes("Ops") && tList.includes("You manage") && !tList.includes("not read"), "People lists the teams with their sizes and your role: " + tList.slice(0, 300));
  run('openHub({ kind: "team", key: "t2" })');
  await tick();
  let tHub = textOf($("hub"));
  check($("conv-name").children.map(textOf).join("") === "Ops" && tHub.includes("You") && tHub.includes("Manager") && tHub.includes("Rename") && tHub.includes("Archive") && tHub.includes("only manager"),
    "a team page shows its members and the manager's actions, and says the last manager cannot leave the role: " + tHub.slice(0, 300));
  const aliceTeamRow = () => $("hub").querySelector(".team-members").children.find((row) => textOf(row).includes("Alice"));
  aliceTeamRow().querySelector("button").click();
  check($("dialog-title").textContent === "Make Alice a manager of Ops?", "manager dialog names the selected person, not their object");
  await $("dialog-ok").onclick();
  check(teamChanges.at(-1).op === "manager-add" && teamChanges.at(-1).target === "p-alice", "clicked Make manager sends the selected member's exact ID");
  teamsReply.teams[1].managers.push("p-alice");
  await run("loadTeams()");
  aliceTeamRow().querySelector("button").click();
  check($("dialog-title").textContent === "Take the manager role from Alice?", "manager removal dialog names the selected person");
  await $("dialog-ok").onclick();
  check(teamChanges.at(-1).op === "manager-remove" && teamChanges.at(-1).target === "p-alice", "clicked Take role sends the selected member's exact ID");
  teamsReply.teams[1].managers = ["p-me"];
  await run("loadTeams()");
  aliceTeamRow().querySelector(".team-member-acts").children[1].click();
  check($("dialog-title").textContent === "Remove Alice from Ops?", "membership removal dialog names the selected person");
  await $("dialog-ok").onclick();
  check(teamChanges.at(-1).op === "remove" && teamChanges.at(-1).target === "p-alice", "clicked Remove sends the selected member's exact ID");
  run('openHub({ kind: "team", key: "t1" })');
  await tick();
  tHub = textOf($("hub"));
  check(tHub.includes("Alice") && tHub.includes("Leave") && !tHub.includes("Rename") && !tHub.includes("Make manager"), "a member sees Leave and no management: " + tHub.slice(0, 300));
  teamResultName = "Server-renamed team";
  await run('teamAct({ team: "t1", op: "join" })');
  check($("live").textContent === "You joined Server-renamed team.", "join announcement uses the accepted result, not a stale directory label");
  teamResultName = "";
  teamChanges.length = 0;
  await run('teamAct({ team: "t1", op: "leave" })');
  check(teamChanges.length === 1 && teamChanges[0].op === "leave" && teamChanges[0].team === "t1" && $("live").textContent.includes("left"), "leaving is one signed change to the server: " + JSON.stringify(teamChanges));
  run("teamDialog(null)");
  $("team-name").value = "Data";
  await $("dialog-ok").onclick();
  check(teamChanges.some((c) => c.op === "create" && c.name === "Data") && $("live").textContent.includes("created"), "creating a team names it");
  teamRefuses = "the last manager cannot give up the role: make someone else a manager first";
  run('openHub({ kind: "team", key: "t2" })');
  await tick();
  await run('teamAct({ team: "t2", op: "manager-remove", target: "p-me" })');
  check($("live").textContent.includes("last manager"), "the server's refusal is shown as it came: " + $("live").textContent);
  teamRefuses = "";
  // Current team snapshots are explicit group invitation drafts, never admission.
  const groupChipButton = label => { const walk = n => n instanceof Elem ? [n, ...n.children.flatMap(walk)] : []; return walk($("dialog-body")).find(n => n.tagName === "button" && n.attrs["aria-label"] === label); };
  overview.people.push({ person: "p-bob", label: "Bob", address: "bob/desk", state: "pinned" }); await run("loadOverview()");
  teamSnapshotPersons = [...teamSnapshotPersons, { id: "p-bob", seq: 0, hash: "z" }];
  await run('teamSelection([{ id: "t1", name: "Platform" }, { id: "t2", name: "Ops" }])');
  await pause(20);
  let tBody = textOf($("dialog-body"));
  check($("dialog").open && tBody.includes("Alice") && tBody.includes("Bob") && !tBody.includes("Remove You") && tBody.includes("no earlier messages") && $("dialog-ok").textContent === "Create group", "team snapshot opens reviewed current people in existing create dialog: " + tBody.slice(0,300));
  groupChipButton("Remove Bob").click();
  $("group-name").value = "Reviewed team group";
  await $("dialog-ok").onclick();
  check(teamGroupCreates.length === 1 && teamGroupInvites.length === 1 && teamGroupInvites[0].person === "p-alice" && teamGroupInvites[0].conv === "team-group" && Object.keys(teamGroupInvites[0].history).length === 0, "removing person excludes their invitation; new group shares nothing earlier");
  // Separate successes survive explicit retry without duplicating the created group.
  teamGroupCreates.length = 0; teamGroupInvites.length = 0; teamGroupRefusesPerson = "p-bob";
  await run('teamSelection([{ id: "t1", name: "Platform" }])'); await pause(20);
  $("group-name").value = "Partial team group";
  await $("dialog-ok").onclick();
  check($("dialog").open && $("dialog-error").textContent.includes("fresh invitation and consent") && !groupChipButton("Remove Alice") && !!groupChipButton("Remove Bob"), "head refusal stays honest with only remaining target retained");
  teamGroupRefusesPerson = ""; await $("dialog-ok").onclick();
  check(teamGroupCreates.length === 1 && teamGroupInvites.filter(i=>i.person==="p-alice").length === 1 && teamGroupInvites.filter(i=>i.person==="p-bob").length === 2, "explicit retry reuses created ID and never repeats successful invitation");
  // Existing single-person invitation path retains its history selection.
  teamGroupInvites.length = 0; await run('inviteGroupDialog(state.dmData)'); $("group-invite-person").value="p-alice"; $("group-history-mode").value="last"; $("group-history-last").value="3"; await $("dialog-ok").onclick();
  check(teamGroupInvites.length===1&&teamGroupInvites[0].person==="p-alice"&&teamGroupInvites[0].history.last===3,"single-person path retains chosen earlier history");
  await run('inviteGroupDialog(state.dmData)'); await pause(20); $("group-team").value="t1";
  const walkGroup = n => n instanceof Elem ? [n,...n.children.flatMap(walkGroup)] : [];
  await walkGroup($("dialog-body")).find(n=>n.tagName==="button"&&textOf(n)==="Add team people").listeners.click[0]();
  groupChipButton("Remove Bob").click(); $("group-history-mode").value="last";$("group-history-last").value="2"; await $("dialog-ok").onclick();
  check($("group-invite-person").disabled, "team recipients visibly disable conflicting single-person choice; removable chips are exact targets");
  check(teamGroupInvites.at(-1).person==="p-alice"&&teamGroupInvites.at(-1).history.last===2,"existing group team draft reviewed removal and chosen history precede invitation");
  // Exact persisted invitation states remain visible without repeated consent or re-signing.
  overview.group_invitations = [{id:"stale-a",conv:"team-group",target:"p-alice",direction:"out",status:"stale",title:"Team group"}, {id:"accepted-in",conv:"unjoined-group",target:"self-person",direction:"in",status:"accepted",title:"Waiting group"}]; await run('loadOverview()');
  check(run('groupInvitationNotices().length')===2 && run('groupInvitationNotice(state.overview.group_invitations[0])').includes("fresh consent"), "stale outgoing and accepted-but-unjoined status visible and truthful");
  await run('inviteGroupDialog(state.dmData,"p-alice")'); check($("group-invite-person").value==="p-alice", "fresh invitation deliberately prefills exact still-eligible person"); $("dialog").close();
  overview.group_invitations.push({id:"fresh-a",conv:"team-group",target:"p-alice",direction:"out",status:"pending",title:"Team group"}); await run('loadOverview()');check(!run('groupInvitationNotices().some(i=>i.id==="stale-a")'), "fresh pending proposal supersedes stale row without guessing chronological IDs");
  overview.dms.push({id:"unjoined-group",kind:"group",members:[{person:"self-person"}],title:"Waiting group",peer:{}});await run('loadOverview()');check(!run('groupInvitationNotices().some(i=>i.id==="accepted-in")'), "joined person no longer falsely appears waiting"); overview.dms.pop();overview.group_invitations=[];await run('loadOverview()');
  // Captured workspace cannot retarget a reviewed team draft.
  await run('teamSelection([{ id: "t1", name: "Platform" }])'); const beforeTeamSwitch=teamGroupCreates.length; run('state.gen++'); $("group-name").value="wrong workspace"; await $("dialog-ok").onclick();
  check(teamGroupCreates.length===beforeTeamSwitch&&$("dialog-error").textContent.includes("Workspace changed"),"late team group draft refuses after workspace generation changed"); $("dialog").close();
  teamSnapshotPersons = [{ id: "p-me", seq: 0, hash: "x" }, { id: "p-alice", seq: 0, hash: "y" }];
  // not current: the last verified state is shown as such; unsupported servers say so
  teamsReply = { ...teamsReply, status: "unavailable", current: false, reason: "cannot reach your server" };
  await run("loadTeams()");
  await run("loadOverview()");
  tList = teamsText();
  check(tList.includes("last verified state") && tList.includes("Platform"), "offline: the last verified teams stay, marked as such: " + tList.slice(0, 260));
  teamsReply = 404;
  await run("loadTeams()");
  await run("loadOverview()");
  tList = teamsText();
  check(tList.includes("does not have teams") && !tList.includes("New team"), "an older server: no teams, no create button: " + tList.slice(0, 200));
  teamsReply = { realm_id: "r", status: "available", current: true, at: T, truncated: false, teams: [] };
  overview.people = peopleBefore; overview.person = personBefore;
  run('selectSection("chats")');
  await run("loadOverview()");

  // The optional Drive (MEL-490): Settings > File storage options mounts the
  // program's setup module over the membership shown; a DM's Project space
  // panel mounts its module over a provider bound to that membership; a
  // file's copy to the space is confirmed and goes through the module with
  // the exact DM, message and index. Stand-ins record what they were given.
  const mounted = [];
  ctx.window.agentnetModules = {
    "drivespace-setup": { mountFileStorageOptions: async (box, { provider }) => { const r = await provider.storageSetup({ action: "status" }); mounted.push({ setup: r.settings.can_admin }); box.append(new Elem("p")); } },
    drivespace: { daemonDriveProvider: (fetchJSON, fetchRaw) => ({ kind: "daemon", fetchJSON, fetchRaw }),
      mountDriveSpace: (box, { conv, provider, agents, openSettings }) => { mounted.push({ panel: conv, provider: provider.kind, agents: agents.length, settings: typeof openSettings }); box.append(new Elem("section")); },
      saveAttachmentToDrive: async (provider, conv, m, index, confirm) => { mounted.push({ save: { conv, id: m.id, dir: m.dir, index, confirm, provider: provider.kind } }); return { file: { name: "note.txt" }, notice: "outside encryption" }; } },
  };
  storageReply = 404;
  run('settingsTab("storage")');
  await pause(20);
  check(mounted.some((x) => x.setup === true), "Settings > Storage mounts File storage options over the daemon's setup route: " + JSON.stringify(mounted));
  await run('openDM("d2")');
  const chip = find($("peer-chips"), (n) => n.tagName === "button" && textOf(n) === "Project space");
  check(!!chip && $("drive-panel").hidden, "a DM offers its Project space, closed");
  chip.click();
  await pause(30);
  check(!$("drive-panel").hidden && mounted.some((x) => x.panel === "d2" && x.provider === "daemon" && x.settings === "function"), "opening it mounts the module for that DM over a daemon provider with a way to Settings: " + JSON.stringify(mounted.slice(-1)));
  await run('openDM("d3")');
  check($("drive-panel").hidden && run("state.driveOpen") === false, "another DM starts with the panel closed");
  dmThreads.d2.messages.push(dmsg("mf", "in", "", { attachments: [{ index: 0, name: "note.txt", size: 5 }] }));
  await run('openDM("d2")');
  const fileMsg = run("state.dmData.messages.find((m) => m.attachments && m.attachments.length && m.dir === 'in')");
  if (fileMsg) {
    run("saveToDrive")({ id: fileMsg.id, dir: "in" }, 0, "note.txt");
    tBody = textOf($("dialog-body"));
    check($("dialog").open && tBody.includes("outside AgentNet's end-to-end encryption") && $("dialog-ok").textContent.includes("outside encryption"), "copying a file to the space asks first and says what it means: " + tBody.slice(0, 200));
    await $("dialog-ok").onclick();
    await pause(20);
    const saved = mounted.find((x) => x.save);
    check(saved && saved.save.conv === "d2" && saved.save.id === fileMsg.id && saved.save.dir === "in" && saved.save.index === 0 && saved.save.confirm === true && saved.save.provider === "daemon", "the copy names the DM, message, direction and index, confirmed: " + JSON.stringify(saved));
  } else check(false, "the fixture DM has no received file to copy");
  delete ctx.window.agentnetModules;

  // Named identities keep exact IDs through selection, refresh, drafts and workspace switches.
  wsStates.A.view=null;wsStates.B.view=null;wsActive='B';ctx.window.agentnet=wsHost('B');
  await select('A');
  overview.me.address='me/laptop'; delete overview.device;
  overview.person={person:'p-me',label:'Me',address:'me/laptop',state:'self',fingerprint:'fp-me'};
  await run('openThread("a1")'); run('setKind("question")');
  await run('loadTargetCatalog()'); run('chooseDeviceAgent')(namedA);
  $('body').value='exact A draft'; run('noteTyping()');
  run('chooseDeviceAgent')(namedB); run('chooseDeviceAgent')(namedA);
  check(run('targetId()').endsWith(':agent:'+namedA)&&run('boundElsewhere()')==='', 'A/B/A keeps exact target and original text binding');
  run('keepDraft()'); await run('openThread("c1")'); await run('openThread("a1")'); await tick();
  check(run('state.deviceAgentID')===namedA&&$('body').value==='exact A draft'&&run('state.drafts.a1.agent_id')===namedA,'named selection persists in conversation draft');
  await run('loadTargetCatalog()'); calls.length=0; await run('send')(ev);
  check(calls.some(c=>c.path==='/api/send'&&c.body.agent_id===namedA&&c.body.to==='alice/desk'),'named question posts exact ID/host');
  $('body').value='message remains device-only';run('setKind("message")');calls.length=0;await run('send')(ev);
  check(calls.some(c=>c.path==='/api/send'&&!('agent_id' in c.body)&&c.body.kind==='message'),'plain message omits agent_id');
  run('setKind("question")');$('body').value='keep removed target';run('noteTyping()');
  agentCatalogs['alice/desk']=[namedRecord(namedB)];calls.length=0;await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send')&&run('state.deviceAgentID')===namedA&&$('body').value==='keep removed target'&&$('compose-error').textContent.includes('no default'),'removed ID refuses without fallback and preserves draft');
  check($('send').disabled&&$('to-name').textContent.includes('name unavailable')&&!$('to-name').textContent.includes(namedA.slice(0,8))&&run('state.deviceAgentID')===namedA&&$('agent-target').children.some(n=>n.attrs.title===namedA),'missing selection is named plainly (no ID in its text) while retaining full ID/title: '+$('to-name').textContent);
  agentCatalogs['alice/desk']=[namedRecord(namedA),namedRecord(namedB)];await run('loadTargetCatalog()');
  run('chooseDeviceAgent')(namedB);calls.length=0;await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send')&&$('compose-error').textContent.includes('someone else'),'retargeted text requires explicit second send');
  run('chooseDeviceAgent')(namedA);run('noteTyping()');run('keepDraft()');
  await select('B');check(!run('state.deviceAgentID'),'new workspace does not inherit named selection');await select('A');await tick();
  check(run('state.deviceAgentID')===namedA&&$('body').value==='keep removed target','workspace A restores its exact selection/text');
  await run('loadTargetCatalog()');
  hold['/api/agents']=true;const lateCatalog=run('loadTargetCatalog()');await tick();await select('B');
  release('/api/agents');hold['/api/agents']=false;await lateCatalog;
  check(run('state.targetCatalog')===null,'late remote catalog cannot paint another workspace');
  await select('A');await tick();await run('loadTargetCatalog()');
  $('body').value='captured named send in A';run('state.typedFor=null');run('noteTyping()');
  wsCalls.A.length=0;wsCalls.B.length=0;hold['/api/agents']=true;
  const capturedNamedSend=run('send')(ev);await tick();await select('B');release('/api/agents');hold['/api/agents']=false;await capturedNamedSend;
  check(wsCalls.A.some(c=>c.path==='/api/send'&&c.body.agent_id===namedA)&&!wsCalls.B.some(c=>c.path==='/api/send')&&$('body').value==='','named send waiting for catalog stays on captured workspace and clears only its own draft');
  await select('A');await tick();catalogRefuses='Peer does not support named agents.';await run('loadTargetCatalog()');calls.length=0;await run('send')(ev);
  check(textOf($('agent-target')).includes(catalogRefuses)&&!calls.some(c=>c.path==='/api/send')&&run('state.deviceAgentID')===namedA,'older-peer refusal visible with exact target retained');catalogRefuses='';await run('loadTargetCatalog()');
  run('keepDraft()');const oldCalls=wsCalls.A.length;
  hold['/api/agents']=true;const lateSettings=run('renderResponder()');await tick();await select('B');
  // renderResponder's catalog read is held after its responder read.
  await tick(); release('/api/agents');hold['/api/agents']=false;await lateSettings;
  check(!textOf($('named-agents')).includes('Agents on this computer')&&wsCalls.A.length>oldCalls,'late local settings do not paint another workspace');
  await select('A');await tick();await run('renderResponder()');
  const namedButton = label => find($('named-agents'),n=>n.tagName==='button'&&textOf(n)===label);
  namedButton('Create agent…').click();byId['named-label'].value='Builder';byId['named-harness'].value='codex';byId['named-dir'].value='/tmp/named';
  calls.length=0;await $('dialog-ok').onclick();
  check(calls.some(c=>c.path==='/api/agents'&&c.body?.action==='create'&&c.body.label==='Builder'&&!('id' in c.body))&&textOf($('named-agents')).includes('Agent list not updated for others'),'local save and availability to others are distinct, stable ID created here');
  check(!calls.some(c=>c.path==='/api/responder'&&c.body),'named save leaves default responder untouched');
  namedButton('Configure…').click();byId['named-harness'].value='codex';byId['named-dir'].value='/tmp/changed';calls.length=0;await $('dialog-ok').onclick();
  const update=calls.find(c=>c.path==='/api/agents'&&c.body?.action==='update');
  check(update?.body.id===namedC&&!('context' in update.body)&&!('timeout' in update.body)&&localAgents[0].responder.timeout===600&&localAgents[0].responder.context[0]==='/tmp/context','update keeps ID and preserves timeout/context');
  catalogPublished=true;calls.length=0;namedButton('Update agent list').click();await tick();
  check(calls.some(c=>c.path==='/api/agents'&&JSON.stringify(c.body)==='{"action":"publish"}')&&textOf($('named-agents')).includes('Agent list updated for others'),'publication retries only through explicit button');
  namedButton('Disable…').click();calls.length=0;await $('dialog-ok').onclick();
  check(calls.some(c=>c.path==='/api/agents'&&c.body?.action==='disable'&&c.body.id===namedC)&&textOf($('named-agents')).includes('Disabled on this computer'),'disable is exact and retains identity');
  namedButton('Configure…').click();byId['named-harness'].value='codex';byId['named-dir'].value='/tmp/stale';await select('B');calls.length=0;await $('dialog-ok').onclick();
  check(!calls.some(c=>c.path==='/api/agents'&&c.body)&&$('dialog-error').textContent.includes('Workspace changed'),'stale config dialog cannot mutate another workspace');$('dialog').close();await select('A');await tick();
  ctx.window.agentnet={...wsHost('A'),platform:'browser'};calls.length=0;await run('renderResponder()');
  check(!calls.some(c=>c.path==='/api/agents'||c.path==='/api/responder')&&!textOf($('named-agents')).includes('Create'),'browser offers no local program configuration');ctx.window.agentnet=wsHost('A');
  dmThreads.d4.peer={...alicePerson,fingerprint:'fp-alice'};
  dmThreads.d4.agents=[agentV('named-pid-A',{agent_id:namedA,host:dmThreads.d4.peer,state:'active',can_ask:true}),agentV('named-pid-B',{agent_id:namedB,host:dmThreads.d4.peer,state:'active',can_ask:true})];
  await run('openDM("d4")');run('setDMAgent')(dmThreads.d4.agents[0]);run('setDMAgent')(dmThreads.d4.agents[1]);run('setDMAgent')(dmThreads.d4.agents[0]);
  check(run('state.dmAgent')==='named-pid-A'&&textOf($('agent-target')).includes('@Builder')&&!textOf($('agent-target')).includes(namedA.slice(0,8))&&$('agent-target').children.some(n=>n.attrs.title?.includes(namedA)),'accepted DM participations retain exact values/full titles; their text is the catalog name without IDs: '+textOf($('agent-target')));
  // @ UI does not parse authority from plain text; row selection owns exact PID.
  run('setDMAgent')(null); $('body').value='@'; run('showMentions()');
  check(run('state.dmAgent')===null && !$('mentions').hidden, 'typing @ only opens participants; it never addresses an assistant');
  run('mentionKey')({key:'ArrowDown',preventDefault(){}});
  run('mentionKey')({key:'Enter',preventDefault(){}});
  check(run('state.dmAgent')==='named-pid-B' && run('kindValue()')==='question' && $('body').value.startsWith('@'), 'keyboard mention selects exact second participation and explicit Question');
  run('showMentions(true)'); run('mentionKey')({key:'Escape',preventDefault(){},stopPropagation(){}});
  check($('mentions').hidden && run('state.dmAgent')==='named-pid-B', 'Escape closes mention list without changing target');
  run('showMentions(true)'); run('agentOf("named-pid-A").can_ask=false'); run('pickMention(0)');
  check(run('state.dmAgent')==='named-pid-B' && $('mentions').hidden, 'stale mention choice never retargets to unavailable assistant');
  run('agentOf("named-pid-A").can_ask=true'); run('setDMAgent')(dmThreads.d4.agents[0]);
  // @ also finds people by name among many (same names stay distinct rows, no
  // addresses in text); a person's name is text only; outsiders only as invitations.
  {
    const t = run('state.dmData'), guests = t.guests;
    t.guests = Array.from({length: 100}, (_, i) => ({ pid: 'guest-pid-' + i, state: 'active', host_here: false, host: { label: i % 2 ? 'Sam' : 'Guest ' + i, address: 'guest' + i + '/desk' }, inviter: alicePerson, shared: [] }));
    const rows = () => $('mentions').children.flatMap(n => n && n.tagName === 'button' ? [n] : ((n && n.children) || []).filter(c => c && c.tagName === 'button'));
    calls.length = 0;
    $('body').value = '@sam'; run('showMentions()');
    const sams = rows().filter(r => textOf(r).startsWith('@Sam'));
    check(sams.length === 50 && sams.every(r => !textOf(r).includes('/desk')) && new Set(sams.map(r => r.attrs.title)).size === 50, 'fifty same-named guests stay distinct rows by name, no addresses in text: ' + sams.length);
    check(sams.every(r => textOf(r).includes('invited by Alice') && /account guest\d+/.test(textOf(r))), 'same-named people say which, readably (inviter, account), on the row itself');
    run('mentionKey')({key:'ArrowDown',preventDefault(){}}); run('mentionKey')({key:'Enter',preventDefault(){}});
    check($('body').value === '@Sam ' && run('state.dmAgent') === 'named-pid-A' && !calls.some(c => /send|ask/.test(c.path)) && $('mentions').hidden, 'choosing a person writes their name: assistant target unchanged, nothing sent');
    check(run('state.mentions').length === 1 && run('state.mentions')[0].ref.kind === 'guest' && run('state.mentions')[0].ref.id === 'guest-pid-3' && textOf($('agent-target')).includes('@Sam'), 'the second Sam is kept by exact participation and shown as selected');
    // The exact reference: encode/decode round trip; only a known one renders as a chip.
    const enc = run('encodeMentions')($('body').value, run('state.mentions'));
    check(enc === '[@Sam](agentnet:guest/guest-pid-3) ', 'the second Sam travels as an exact guest reference: ' + enc);
    const dec = run('decodeMentions')(enc);
    check(dec.text === '@Sam ' && dec.spans.length === 1 && dec.spans[0].ref.id === 'guest-pid-3', 'decoding (history, edit) gives back @Sam and the same exact reference');
    const nodes = run('mentionNodes')(enc + 'x [@Ghost](agentnet:person/nobody) [@Evil](javascript:alert(1)) [@Bad](agentnet:person/bad id)', t);
    const chips = nodes.filter(n => n && n.tagName), flat = nodes.map(n => typeof n === 'string' ? n : textOf(n)).join('');
    check(chips.length === 1 && chips[0].className.includes('mention-chip') && textOf(chips[0]) === '@Sam' && !flat.includes('agentnet:guest') && flat.includes('@Ghost') && !flat.includes('agentnet:person/nobody') &&
      flat.includes('[@Evil](javascript:alert(1))') && flat.includes('[@Bad](agentnet:person/bad id)') && !nodes.some(n => n && n.tagName === 'a'), 'only an exact known mention is a chip; unknown reads @Name; malformed stays inert text, never a link: ' + flat);
    check(run('firstLine')(enc + 'hi', 40) === '@Sam hi', 'previews read @Name, never the reference');
    // Drafts keep it exactly; edits around it move it; an edit inside it drops it visibly.
    run('keepDraft()'); const key = run('state.draftKey');
    check(run('state.drafts')[key].mentions.length === 1, 'the draft keeps the exact mention');
    $('body').value = 'Hi @Sam '; run('trackMentions()');
    check(run('state.mentions').length === 1 && run('state.mentions')[0].start === 3, 'text typed before a mention moves it');
    $('body').value = 'Hi @Sxm '; run('trackMentions()');
    check(run('state.mentions').length === 0 && $('live').textContent.includes('no longer an exact mention'), 'an edit inside a mention drops it visibly, never re-matched by name');
    run('state.drafts')[key] = { ...run('state.drafts')[key], text: '@Sa ', mentions: [{ start: 0, name: 'Sam', ref: { kind: 'guest', id: 'guest-pid-3' } }] };
    run('restoreDraft')(key, t);
    check(run('state.mentions').length === 0 && $('live').textContent.includes('no longer exact'), 'a kept mention whose text no longer matches is dropped on restore, visibly');
    // Two people mentioned, no assistant chosen: an ordinary message with both exact references; nothing runs.
    run('setDMAgent')(null); $('body').value = ''; run('state.mentions=[]'); run('trackMentions()');
    $('body').value = '@sam'; run('trackMentions()'); run('showMentions()'); run('mentionKey')({key:'Enter',preventDefault(){}});
    $('body').value += '@gue'; run('trackMentions()'); run('showMentions()'); run('mentionKey')({key:'Enter',preventDefault(){}});
    calls.length = 0; await run('send')({ preventDefault() {} });
    const sent = calls.filter(c => c.path === '/api/dm/send');
    check(sent.length === 1 && sent[0].body.body === '[@Sam](agentnet:guest/guest-pid-1) [@Guest 0](agentnet:guest/guest-pid-0) ' && !calls.some(c => c.path === '/api/dm/agent/ask') && !run('state.mentions').length,
      'two exact person mentions go as an ordinary message; no assistant is asked: ' + JSON.stringify(sent.map(c => c.body.body)));
    run('setDMAgent')(dmThreads.d4.agents[0]);
    $('body').value = '@builder'; run('showMentions()');
    check(textOf(rows()[0]).startsWith('@Builder') && rows().some(r => textOf(r).startsWith('Invite a person')) && rows().some(r => textOf(r).startsWith('Invite an assistant')) === !!run('inviteRights(state.dmData).assistants'), 'assistants first; invitations offered to an original member as the participants list allows: ' + rows().map(textOf).join(' | '));
    $('body').value = '@zz-nobody'; run('showMentions()');
    check(textOf($('mentions')).includes('No one here matches') && rows().length > 0 && rows().every(r => textOf(r).startsWith('Invite')), 'no match: only actionable invitations');
    run('closeMentions()'); t.guests = guests; $('body').value = '';
  }
  $('body').value='';run('state.typedFor=null');
  run('setKind("task")');await run('loadDM()');
  check(!$('kind').disabled&&run('kindValue()')==='task','DM question/task controls enabled after device switch and same-agent refresh keeps task');
  run('inviteDialog')(run('state.dmData'));
  const hostChoice=byId['agent-host:alice/desk'];hostChoice.checked=true;
  for(const f of hostChoice.listeners.change||[]) f({currentTarget:hostChoice});await tick();
  byId['invite-agent'].value=namedB;calls.length=0;await $('dialog-ok').onclick();
  check(calls.some(c=>c.path==='/api/dm/agent/invite'&&c.body.agent_id===namedB&&c.body.host==='alice/desk'),'named invite carries exact ID and chosen host: '+$('dialog-error').textContent+' '+JSON.stringify(calls));
  run('inviteDialog')(run('state.dmData'));byId['agent-host:alice/desk'].checked=true;calls.length=0;await select('B');await $('dialog-ok').onclick();
  check(!calls.some(c=>c.path==='/api/dm/agent/invite')&&$('dialog-error').textContent.includes('another conversation or workspace'),'stale invitation refuses workspace retarget: '+$('dialog-error').textContent+' '+JSON.stringify(calls));$('dialog').close();
  await select('A');await run('openDM("d4")');run('decideDialog')(dmThreads.d4.agents[0],run('state.dmData'),true);await select('B');calls.length=0;await $('dialog-ok').onclick();
  check(!calls.some(c=>c.path==='/api/dm/agent/decide')&&$('dialog-error').textContent.includes('Workspace or DM changed'),'stale participation decision refuses workspace retarget');$('dialog').close();
  await select('A');await run('openDM("d4")');run('dismissDialog')(dmThreads.d4.agents[0]);await select('B');calls.length=0;await $('dialog-ok').onclick();
  check(!calls.some(c=>c.path==='/api/dm/agent/dismiss')&&$('dialog-error').textContent.includes('Workspace or DM changed'),'stale dismissal refuses workspace retarget');$('dialog').close();
  const namedMessage={...msg('named','alice/desk','answer'),kind:'answer',agent_id:namedB};
  run('state.targetCatalog=null');
  const unknownAuthor=run('dmAuthor')(namedMessage,dmThreads.d4);
  check(unknownAuthor.includes('alice/desk')&&unknownAuthor.endsWith('name unavailable')&&!unknownAuthor.includes(namedB.slice(0,8)),'unknown author: exact host, name unavailable, no ID in its text: '+unknownAuthor);
  const records=[namedRecord(namedA),namedRecord(namedB)];
  run('(records)=>{state.targetCatalog={host:"alice/desk",agents:records}}')(records);
  check(run('dmAuthor')(namedMessage,dmThreads.d4)==='Builder on alice/desk'&&run('namedAgentLabel')(namedA,'alice/desk')==='Builder'&&run('namedAgentLabel')(namedB,'alice/desk')==='Builder','identical catalog names read as the name, without IDs in chat text');
  check(run('catalogLabel')(records[0],records)==='Builder · '+namedA.slice(0,8)+' · alice/desk'&&run('catalogLabel')(records[1],records)==='Builder · '+namedB.slice(0,8)+' · alice/desk','pickers still tell two agents of one name apart');
  check(run('namedAgentLabel')(namedA,'other/host')==='Assistant on other/host · name unavailable','catalog labels never transfer to another execution host');
  check(run('agentName')({agent_id:namedA,host:{address:'dana/desk',label:'Dana'},host_here:false})==="Dana's assistant · name unavailable"&&run('agentName')({agent_id:namedA,host:{address:'me/desk',label:'Me'},host_here:true})==='Your assistant · name unavailable','participant without a catalog: whose assistant, name unavailable');
  const namedRendered=run('renderMsg')(namedMessage,{},null,threads.a1);
  const namedWho=find(namedRendered,n=>n.className==='who');
  check(textOf(namedWho)==='Builder on alice/desk'&&namedWho.attrs.title.includes(namedB)&&namedWho.attrs.title.includes('host assertion')&&textOf(run('dmDetails')(namedMessage)).includes(namedB)&&textOf(run('dmDetails')(namedMessage)).includes('host assertion'),'everyday author is readable; full ID and provenance remain in title and Details');
  const namedWhoA=find(run('renderMsg')({...namedMessage,id:'named-a-msg',agent_id:namedA},{},null,threads.a1),n=>n.className==='who');
  check(textOf(namedWhoA)===textOf(namedWho)&&namedWhoA.attrs.title.includes(namedA)&&!namedWhoA.attrs.title.includes(namedB),'two agents of one name stay two: same text, each exact identity in its title');
  const linkText=run('agentLinkText')({agent_id:namedA,address:'alice/desk',dms:[]},alicePerson);
  check(linkText.includes('Builder on alice/desk')&&!linkText.includes(namedA.slice(0,8)),'overview uses the catalog name and host, no ID: '+linkText);

  // External invitations use a current-workspace address and exact catalog ID.
  await select('A'); await run('openDM("d4")');
  overview.directory = { status: 'listed', current: true, members: [{ address: 'outside/host' }] };
  agentCatalogs['outside/host'] = [namedRecord(namedC, 'outside/host')];
  const externalShared = dmThreads.d4.messages.find(m => !m.event && !m.excerpt_pid);
  externalShared.attachments = [{ name: 'chosen-context.txt', size: 7 }];
  await run('loadOverview()'); await run('loadDM()');
  run('inviteDialog')(run('state.dmData'));
  const outside = byId['agent-host:outside/host'];
  check(outside && textOf($('dialog-body')).includes('not verified real-world owners') && textOf($('dialog-body')).includes('chosen-context.txt') && textOf($('dialog-body')).includes('Unselected history and files are excluded'), 'external picker discloses directory trust and selected file visibility');
  outside.checked = true;
  for (const f of outside.listeners.change || []) f({ currentTarget: outside }); await tick();
  calls.length = 0; await $('dialog-ok').onclick();
  check(!calls.some(c => c.path === '/api/dm/agent/invite') && $('dialog-error').textContent.includes('exact named agent'), 'external host refuses ambiguous default');
  byId['invite-agent'].value = namedC; byId['agent-share:' + externalShared.id].checked = true;
  calls.length = 0; await $('dialog-ok').onclick();
  check(!calls.some(c => c.path === '/api/dm/agent/invite') && $('dialog-error').textContent.includes('Confirm file access'), 'file history needs separate explicit acknowledgement');
  byId['agent-file-consent:yes'].checked = true;
  await $('dialog-ok').onclick();
  check(calls.some(c => c.path === '/api/dm/agent/invite' && c.body.host === 'outside/host' && c.body.agent_id === namedC && JSON.stringify(c.body.share) === JSON.stringify([externalShared.id])), 'external invite shares only exact selected context and named host: ' + $('dialog-error').textContent + ' ' + JSON.stringify(calls));
  overview.directory.current = false; await run('loadOverview()'); run('inviteDialog')(run('state.dmData'));
  check(!textOf($('dialog-body')).includes('External host · outside/host'), 'stale directory exposes no external selection'); $('dialog').close();

  // Visitor is an authority boundary even if a provider supplies no frozen text.
  const savedMessages = dmThreads.d4.messages, savedAgents = dmThreads.d4.agents;
  dmThreads.d4.role = 'visitor'; dmThreads.d4.frozen = '';
  dmThreads.d4.messages = [dmsg('snapshot', 'in', 'selected snapshot', { excerpt_pid: 'external-pid', claimed_key: 'claimed-key', synced_from: 'alice/desk', from: 'original/author', kind: 'task', actions: ['accept'], can: ['edit', 'delete', 'react'] }),
    dmsg('visitor-request', 'in', 'task addressed to your agent', { actions: ['accept'], state_text: 'Needs you: task acceptance', kind: 'task' })];
  dmThreads.d4.agents = [agentV('external-pid', { state: 'invited', external: true, can_decide: true })];
  await run('openDM("d4")'); run('setDMAgent')(null);
  check($('composer').hidden && $('body').disabled && $('send').disabled && $('attach').hidden, 'visitor ordinary composer disabled and hidden');
  $('agents').querySelector('.assistant-participant').click();
  const visitorAgents = textOf($('dialog-body')), visitorTimeline = textOf($('timeline'));
  $('dialog').close();
  check(visitorAgents.includes('External host') && visitorAgents.includes('Accept') && !textOf($('agents')).includes('Add participants'), 'visitor retains provider acceptance only and labels external context');
  for (const sender of ['alice/desk', 'bob/host']) check(run('dmAuthor')(dmsg('human-' + sender, 'in', 'addressed question', { from: sender, kind: 'question', origin: 'ui' }), { ...run('state.dmData'), peer: { label: 'Different room member' } }) === sender, 'visitor names each actual human sender address independently of peer label: ' + sender);
  check(visitorTimeline.includes('Claimed original/author') && visitorTimeline.includes('authorship is not verified') && visitorTimeline.includes('This snapshot never runs') && visitorTimeline.includes('claimed-key'), 'excerpt claims and grant are honest in author and Details');
  check(!run('canDo')(run('state.dmData').messages[0], 'react') && run('state.dmData').messages[0].actions.length === 0 && run('state.dmData').messages[1].actions[0] === 'accept', 'snapshot cannot mutate or execute; addressed request keeps provider action');
  calls.length = 0; $('body').value = 'cannot write room'; await run('send')(ev); run('inviteDialog')(run('state.dmData'));
  check(!calls.some(c => c.path === '/api/dm/send' || c.path === '/api/dm/agent/invite'), 'visitor sends and invites no ordinary member operation');
  delete dmThreads.d4.role; dmThreads.d4.messages = savedMessages; dmThreads.d4.agents = savedAgents; await run('openDM("d4")'); $('body').value = '';

  // Wrapped browser transport must stage through its captured workspace.
  const savedHost = ctx.window.agentnet, savedEngine = ctx.window.agentnetEngine;
  ctx.window.agentnetEngine = {};
  const fileBytes = new Uint8Array([0, 255, 7]), browserFile = { name: 'browser-current.bin', size: 3, arrayBuffer: async () => fileBytes.buffer };
  let browserStages = 0;
  const browserHost = { workspace: { id: 'browser-A' }, stage: async f => { browserStages++; return { workspace: 'browser-A', name: f.name, size: f.size, arrayBuffer: () => f.arrayBuffer() }; },
    api: async (_path, body) => { if (body.files.some(f => f.workspace !== 'browser-A')) throw Error('File belongs to another workspace'); return {}; } };
  ctx.window.agentnet = browserHost;
  const wrappedFiles = await run('preparedFiles')([{ file: browserFile, name: browserFile.name, size: 3 }], browserHost);
  check(browserStages === 1 && wrappedFiles[0].workspace === 'browser-A' && new Uint8Array(await wrappedFiles[0].arrayBuffer())[1] === 255, 'captured browser host stages exact bytes with workspace marker');
  await browserHost.api('/api/dm/agent/ask', { files: wrappedFiles });
  let wrongWorkspace = false;
  try { await browserHost.api('/api/dm/agent/ask', { files: [{ ...wrappedFiles[0], workspace: 'browser-B' }] }); } catch (e) { wrongWorkspace = e.message.includes('another workspace'); }
  check(wrongWorkspace, 'wrong workspace remains refused');
  ctx.window.agentnet = null;
  const directFiles = await run('preparedFiles')([{ file: browserFile, name: browserFile.name, size: 3 }]);
  check(!directFiles[0].workspace && new Uint8Array(await directFiles[0].arrayBuffer())[1] === 255 && browserStages === 1, 'direct engine fallback preserves exact bytes only without captured host');
  ctx.window.agentnet = savedHost; ctx.window.agentnetEngine = savedEngine;

  const audienceRequest = dmsg('audience-physical', 'out', '', { lid: 'host-physical', pid: 'exact-pid', kind: 'question', target: { address: 'outside/host', agent_id: namedC }, attachments: [{ name: 'file-only.txt', size: 3 }] });
  const audienceReport = dmsg('report-copy', 'in', 'report', { pid: 'exact-pid', kind: 'answer', agent_id: namedC, from: 'outside/host', reply_to: 'host-physical' });
  const audienceThread = { ...dmThreads.d4, messages: [audienceRequest, audienceReport] };
  check(run('dmReplyParent')(audienceReport, audienceThread) === audienceRequest && textOf(run('dmMsg')(audienceReport, audienceThread)).includes('Reply to: (files only: file-only.txt)'), 'linked audience resolves host-copy report to exact visible file-only request by shared logical identity');
  for (const wrong of [{ pid: 'other-pid' }, { target: { address: 'other/host', agent_id: namedC } }, { target: { address: 'outside/host', agent_id: namedB } }, { kind: 'message' }, { excerpt_pid: 'context-grant' }]) {
    check(!run('dmReplyParent')(audienceReport, { ...audienceThread, messages: [{ ...audienceRequest, ...wrong }] }), 'logical reply cannot cross participation/host/agent/kind/context bounds: ' + JSON.stringify(wrong));
  }
  check(!run('dmReplyParent')(audienceReport, { ...audienceThread, messages: [audienceRequest, { ...audienceRequest, id: 'another-copy' }] }), 'ambiguous logical identity never selects a parent');
  const savedDefaultResponder = overview.me.responder;
  overview.me.responder = ''; await run('loadOverview()');
  run('decide')('accept', { ...audienceRequest, kind: 'task', from: 'alice/phone' }, audienceThread);
  check(textOf($('dialog-body')).includes('outside/host') && textOf($('dialog-body')).includes(', using its local configuration') && !textOf($('dialog-body')).includes(namedC.slice(0, 8)) && !!find($('dialog-body'), n => (n.attrs?.title || '').includes(namedC)) && !textOf($('dialog-body')).includes('no responder is set') && $('dialog-ok').disabled, 'named task decision names its exact local executor (ID in the title) without default fallback and keeps acceptance gate: ' + textOf($('dialog-body')));
  $('dialog').close(); overview.me.responder = savedDefaultResponder; await run('loadOverview()');

  // Native receiver delegation stays distinct from addressed executor and workspace.
  await select('A'); await run('openThread("a1")');
  overview.reply_receivers = true;
  localAgents.push({record:namedRecord(namedA,'me/laptop'),enabled:true,responder:{...catalogResponder}});
  await run('loadOverview()'); await run('loadReceiverCatalog()');
  run('chooseReplyReceiver')(namedA);
  check(run('state.replyReceiver.mode') === '' && run('state.replyReceiver.instructions') === '', 'assistant choice never prechecks task mode or invents instructions');
  run('state.replyReceiver={...state.replyReceiver,instructions:"ORIGINAL LOCAL",mode:"question"}; keepDraft()');
  const delegated = run('state.replyReceiver');
  $('body').value = 'remote request'; run('keepDraft()');
  await run('openThread("b1")'); check(run('state.replyReceiver') === null, 'another conversation has no receiver from prior draft');
  await run('openThread("a1")'); check(run('state.replyReceiver') === delegated && $('body').value === 'remote request', 'exact receiver and original instructions restore with conversation');
  check(run('hasUnsentDrafts()'), 'selected receiver defers automatic/explicit update reload');
  localAgents.find(a=>a.record.id===namedA).enabled=false;
  calls.length=0; await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send') && $('body').value==='remote request' && run('state.replyReceiver')===delegated && $('compose-error').textContent.includes('no default'), 'removed receiver refuses before send and keeps exact draft');
  localAgents.find(a=>a.record.id===namedA).enabled=true;
  hold['/api/agents']=true; calls.length=0; wsCalls.A.length=0; wsCalls.B.length=0;
  const receiverPending=run('send')(ev); await tick();
  await select('B');
  delete hold['/api/agents'];
  while(held['/api/agents']?.length) release('/api/agents');
  await receiverPending; await tick();
  check(wsCalls.A.some(c=>c.path==='/api/send'&&c.body.reply_receiver.agent_id===namedA&&c.body.reply_receiver.instructions==='ORIGINAL LOCAL'&&c.body.reply_receiver.mode==='question') && !wsCalls.B.some(c=>c.path==='/api/send'), 'slow selected send uses captured native workspace and original delegation, never selected B');
  check(run('state.replyReceiver')===null, 'late A receiver catalog never installs A selection in B');
  await select('A'); await run('openThread("a1")');
  run('state.receiverBindings=[{request_ref:"a1",receiver:{kind:"human"},label:"Me (human)",host:"me/laptop",state:"pending"}]; renderReceiverStatus()');
  check(textOf($('receiver-status')).includes('Replies remain for you; no automatic continuation') && !textOf($('receiver-status')).includes('Waiting for correlated reply'), 'human bindings describe person handling even when native input is pending');
  run('state.receiverBindings=[{request_ref:"a1",receiver:{kind:"managed_agent"},label:"Selected assistant",host:"me/laptop",state:"pending"}]; renderReceiverStatus()');
  check(textOf($('receiver-status')).includes('Waiting for correlated reply'), 'managed pending copy still means correlated continuation');
  run('state.data.messages.push({id:"first-request",dir:"out",body:"FIRST DISTINCT ORIGINAL"},{id:"second-request",dir:"out",body:"SECOND DISTINCT ORIGINAL"});state.receiverBindings=[{request_ref:"first-request",receiver:{kind:"managed_agent"},label:"Same assistant",host:"me/laptop",state:"completed"},{request_ref:"second-request",receiver:{kind:"managed_agent"},label:"Same assistant",host:"me/laptop",state:"completed"}];renderReceiverStatus()');
  check(textOf($('receiver-status')).includes('“FIRST DISTINCT ORIGINAL”') && textOf($('receiver-status')).includes('“SECOND DISTINCT ORIGINAL”') && textOf($('receiver-status')).includes('first-re') && textOf($('receiver-status')).includes('second-r'), 'two completed bindings for same assistant quote their own exact outbound request and identity');
  // Safe registered-session metadata binds an opaque local handle, not a native path.
  overview.reply_sessions=true;overview.files={max_file:1000,max_count:2,max_message:2000}; await run('loadOverview()'); await run('loadReceiverCatalog()');
  run('chooseReplyReceiver')('session:exact-Pi_handle');
  const liveReceiver=run('state.replyReceiver');
  check(liveReceiver.kind==='live_session' && liveReceiver.session_handle==='exact-Pi_handle' && !('mode' in liveReceiver) && !('instructions' in liveReceiver), 'native session choice carries exact handle without managed instructions/mode');
  check(textOf($('reply-receiver')).includes('Inactive native adapter') && !textOf($('reply-receiver')).includes('online'), 'known inactive registration retained without liveness inference');
  check($('receiver-instructions').children.length===0 || !textOf($('reply-receiver')).includes('Original continuation instructions'), 'live native selection never presents managed continuation instructions');
  $('body').value='EXACT LIVE ORIGINAL';run('keepDraft()');await run('openThread("b1")');await run('openThread("a1")');
  check(run('state.replyReceiver')===liveReceiver && $('body').value==='EXACT LIVE ORIGINAL', 'native exact handle/draft retained across conversation switch');
  $('body').value='';run('noteTyping()');run('setKind("question")');await run('loadTargetCatalog()');run('chooseDeviceAgent')(namedB);$('body').value='EXACT LIVE ORIGINAL';run('noteTyping()');
  const nativeDraftFile=fileOf('native-exact.txt',7,'text/plain');run('pushFiles')([nativeDraftFile]);run('keepDraft()');const retainedNativeFile=run('state.files')[0];
  await select('B');check(run('state.replyReceiver')===null&&!run('state.deviceAgentID')&&!run('state.files').length,'B never inherits native handle/remote target/file draft');await select('A');await tick();
  check(run('state.replyReceiver')===liveReceiver&&run('state.deviceAgentID')===namedB&&$('body').value==='EXACT LIVE ORIGINAL'&&run('state.files')[0]===retainedNativeFile&&retainedNativeFile.file===nativeDraftFile,'A/B/A restores exact native handle, independent remote target, text and original File object');
  const preparedLive=await run('prepareReplyReceiverSelection')(liveReceiver,ctx.window.agentnet,'A',true,true);
  check(JSON.stringify(preparedLive)==='{"kind":"live_session","session_handle":"exact-Pi_handle"}', 'native send emits only exact allowlisted intent');
  const savedNative=replySessions.shift(); calls.length=0; await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send'||c.path==='/api/upload') && $('body').value==='EXACT LIVE ORIGINAL' && run('state.replyReceiver')===liveReceiver && run('state.files')[0]===retainedNativeFile && $('compose-error').textContent.includes('no default'), 'unknown native handle refuses before staging, preserving exact draft with no fallback');
  replySessions.unshift(savedNative); hold['/api/reply-sessions']=true;wsCalls.A.length=0;wsCalls.B.length=0;
  const nativePending=run('send')(ev);await tick();await select('B');delete hold['/api/reply-sessions'];while(held['/api/reply-sessions']?.length)release('/api/reply-sessions');await nativePending;await tick();
  check(wsCalls.A.some(c=>c.path==='/api/send'&&JSON.stringify(c.body.reply_receiver)==='{"kind":"live_session","session_handle":"exact-Pi_handle"}')&&!wsCalls.B.some(c=>c.path==='/api/send')&&run('state.replyReceiver')===null,'delayed native catalog send retains original A host/handle, never installs or sends it in B');
  await select('A');await run('openThread("a1")');
  run('state.receiverBindings=[{request_ref:"a1",receiver:{kind:"live_session",session_handle:"exact-Pi_handle"},label:"Local Pi (pi)",host:"me/laptop",state:"accepted"}];renderReceiverStatus()');
  check(textOf($('receiver-status')).includes('Accepted into native session; effects completion unconfirmed')&&!textOf($('receiver-status')).includes('Running')&&!textOf($('receiver-status')).includes('Completed ·'),'native ACK describes accepted input, not execution or completed effects');
  // Closed-session backup is deliberate intent, never inferred from the default.
  await run('loadReceiverCatalog()');run('chooseReplyReceiver')('session:exact-Pi_handle');
  check(!run('state.replyReceiver.on_close') && $('receiver-summary').textContent.includes('no backup'), 'native receiver has no automatic backup');
  run('chooseReplyBackup')(namedA);
  check(run('state.replyReceiver.on_close.mode')==='' && run('state.replyReceiver.on_close.instructions')==='', 'backup selection never invents instructions or task authority');
  run('state.replyReceiver={...state.replyReceiver,on_close:{...state.replyReceiver.on_close,instructions:"EXPLICIT BACKUP ORIGINAL",mode:"question"}};keepDraft();renderReplyReceiver()');
  const closedReceiver=run('state.replyReceiver');
  $('body').value='BEFORE NO-SWITCH CAPTURE';run('noteTyping()');run('pushFiles')([fileOf('backup-exact.txt',9,'text/plain')]);run('workspaceCapture')( 'A',wsStates.A);const closedFile=run('state.files')[0];
  $('body').value='EXACT BACKUP DRAFT';run('setKind("task")');run('workspaceCapture')('A',wsStates.A);
  check(wsStates.A.view.drafts.a1.text==='EXACT BACKUP DRAFT'&&wsStates.A.view.drafts.a1.kind==='task'&&wsStates.A.view.drafts.a1.reply_receiver===closedReceiver&&wsStates.A.view.drafts.a1.files[0]===closedFile,'repeated capture without switch refreshes actual current text/kind/exact backup/File');
  await run('openThread("b1")');await run('openThread("a1")');
  check(run('state.replyReceiver')===closedReceiver && $('body').value==='EXACT BACKUP DRAFT' && run('state.files')[0]===closedFile, 'conversation restores original backup and File exactly');
  await select('B');check(run('state.replyReceiver')===null,'other workspace never inherits backup');await select('A');await tick();
  check(run('state.replyReceiver')===closedReceiver && run('state.files')[0]===closedFile && $('body').value==='EXACT BACKUP DRAFT' && kind()==='task', 'inactive A backup draft restores text/kind/exact receiver/backup/File without reselecting IDs');
  const preparedClosed=await run('prepareReplyReceiverSelection')(closedReceiver,ctx.window.agentnet,'A',true,true);
  check(JSON.stringify(preparedClosed)==='{"kind":"live_session","session_handle":"exact-Pi_handle","on_close":{"agent_id":"'+namedA+'","instructions":"EXPLICIT BACKUP ORIGINAL","mode":"question"}}', 'backup wire is strict native intent only; no private catalog configuration');
  const localBackup=localAgents.find(a=>a.record.id===namedA),backupConfig={...localBackup.responder};
  localBackup.responder={...localBackup.responder,dir:'/changed-executor'};calls.length=0;await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send'||c.path==='/api/upload')&&run('state.replyReceiver')===closedReceiver&&run('state.files')[0]===closedFile&&$('compose-error').textContent.includes('configuration changed'),'changed backup refuses before staging with exact draft kept');
  localBackup.responder=backupConfig;localBackup.enabled=false;calls.length=0;await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/send'||c.path==='/api/upload')&&run('state.replyReceiver')===closedReceiver&&$('compose-error').textContent.includes('no default'),'disabled backup refuses before staging without fallback');localBackup.enabled=true;
  hold['/api/agents']=true;wsCalls.A.length=0;wsCalls.B.length=0;
  const closedPending=run('send')(ev);await tick();await select('B');delete hold['/api/agents'];while(held['/api/agents']?.length)release('/api/agents');await closedPending;await tick();
  check(wsCalls.A.some(c=>c.path==='/api/send'&&c.body.reply_receiver.on_close?.agent_id===namedA&&c.body.reply_receiver.on_close.instructions==='EXPLICIT BACKUP ORIGINAL')&&!wsCalls.B.some(c=>c.path==='/api/send')&&run('state.replyReceiver')===null, 'delayed backup catalog keeps original A delegation, no B retarget');
  await select('A');await run('openThread("a1")');
  for (const [handoff,words] of [['preauthorized','Backup preauthorized; not handed over'],['held','Backup held; no safe handoff'],['handed_over','Handed over to selected backup; effects completion unconfirmed']]) {
    run('state.receiverBindings') .splice(0,run('state.receiverBindings.length'),{request_ref:'a1',receiver:{kind:handoff==='handed_over'?'managed_agent':'live_session',on_close:{agent_id:namedA}},label:'Exact receiver',handoff_label:'Chosen A',host:'me/laptop',state:'pending',handoff_state:handoff,detail:handoff==='held'?'uncertain accepted claim retained':''});
    calls.length=0;run('renderReceiverStatus()');
    check(textOf($('receiver-status')).includes(words)&&textOf($('receiver-status')).includes('Chosen A ('+namedA.slice(0,8)+')')&&!calls.length,'status projects native '+handoff+' and exact target without action');
  }
  await run('loadReceiverCatalog()');run('chooseReplyReceiver')('session:exact-Pi_handle');run('chooseReplyBackup')(namedA);run('chooseReplyBackup')('');
  check(!run('state.replyReceiver.on_close'),'explicit backup off removes authority rather than hiding it');
  overview.reply_sessions=false;
  overview.reply_receivers=false;
  let unsupportedReceiver=false;
  try { await run('prepareReplyReceiverSelection')(delegated,ctx.window.agentnet,'A',false); } catch(e) { unsupportedReceiver=e.message.includes('unavailable'); }
  check(unsupportedReceiver,'relay browser refuses preserved managed receiver selection instead of silently dropping it');
  run('state.replyReceiver=null; state.receiverCatalog=null; state.receiverBindings=[]');
  // Human group discussion uses the same explicit local reply receiver choice.
  overview.groups=true;overview.reply_receivers=true;
  dmThreads.group0={...dmThreads.d2,id:"group0",kind:"group",title:"Exact human group",members:[overview.person],agents:[],messages:[],frozen:""};
  overview.dms.push({id:"group0",kind:"group",title:"Exact human group",peer:{label:"Exact human group"},members:[overview.person],count:0,unread:0,last_at:new Date().toISOString()});
  await run('loadOverview()');await run('openDM("group0")');
  check(textOf($('conv-list')).includes('New group…')&&textOf($('conv-list')).includes('Exact human group'),'active sidebar exposes distinct group conversations and create action');
  check(!$('reply-receiver').hidden&&textOf($('reply-receiver')).includes('Me (human)')&&$('compose-hint').textContent.includes('current group members'),'group composer exposes reply receiver and says exact audience');
  calls.length=0;$('body').value='ordinary group text';await run('send')(ev);
  const groupSend=calls.find(c=>c.path==='/api/dm/send');
  check(groupSend&&groupSend.body.conv==='group0'&&groupSend.body.reply_receiver?.kind==='human','ordinary native group Send preserves explicit human reply receiver');
  run('state.replyReceiver={kind:"managed_agent",agent_id:"unavailable"};state.files=[]');calls.length=0;$('body').value='keep group draft';await run('send')(ev);
  check(!calls.some(c=>c.path==='/api/dm/send'||c.path==='/api/upload')&&$('body').value==='keep group draft','stale assistant selection refuses group Send without fallback or staging');
  dmThreads.group0.messages=[{id:'selected-turn',dir:'out',body:'EXACT SELECTED GROUP TURN',group_ref:{lid:'a'.repeat(32),author:'a'.repeat(64),hash:'b'.repeat(64)},attachments:[{name:'selected.bin',size:3}]}];
  run('inviteGroupDialog')(dmThreads.group0);
  check(textOf($('dialog-body')).includes('EXACT SELECTED GROUP TURN')&&textOf($('dialog-body')).includes('Includes 1 file')&&!textOf($('dialog-body')).includes('[object Object]'),'selected history renders actual checkbox row and explicit file notice');$('dialog').close();
  const savedTypingUI=run('typingUI');
  run('globalThis.groupTypingScopes=[];typingUI={setScope(scope){groupTypingScopes.push(scope);return Promise.resolve();}}');
  run('setDMAgent')({pid:'exact-typing-pid',host:{label:'Bob',address:'bob/desk'},can_ask:true});await Promise.resolve();await Promise.resolve();
  run('setDMAgent')(null);await Promise.resolve();await Promise.resolve();
  check(run('groupTypingScopes').length===2&&run('groupTypingScopes')[0]===null&&run('groupTypingScopes')[1]?.conv==='group0','ordinary group → Ask → Cancel rearms current typing scope without a message/refresh');
  run('setDMAgent')(null);await Promise.resolve();await Promise.resolve();
  check(run('groupTypingScopes').length===2,'unchanged group target does not reset typing generation');
  run('typingUI=null');
  run('state.replyReceiver=null');dmThreads.group0.frozen='current group access unavailable';await run('loadDM()');run('kindHint()');
  check(textOf($('agents')).includes('last verified member')&&$('conv-presence').textContent.includes('Last verified audience')&&$('to-how').textContent.includes('Last verified audience')&&$('compose-hint').textContent.includes('Current group audience unavailable'),'frozen group labels last verified audience and refuses implied current sending');
  run('state.replyReceiver=null');run('groupInvitationDialog')({id:'exact-invite',title:'No history',inviter:'alice/desk',history:null});
  check(textOf($('dialog-body')).includes('No earlier messages or files are shared'),'native nil selected history renders without implying room access');$('dialog').close();
  // Display direction stays original; group files use their exact physical table.
  run('globalThis.originalGroupFileChips=fileChips;globalThis.physicalGroupFiles=[];fileChips=(m)=>{physicalGroupFiles.push(m);return null;}');
  const physicalThread={id:'physical-group',kind:'group',title:'Group',peer:{label:'Group'},members:[],agents:[],messages:[],role:'member'},physicalRow={id:'exact-history-file',dir:'out',from:'me/laptop',kind:'message',body:'',at:'2026-09-28T12:00:00Z',attachments:[{name:'z.txt',size:1}],can:[]};
  run('dmMsg')({...physicalRow,synced_from:'me/laptop'},physicalThread);
  run('dmMsg')({...physicalRow,via:'me/phone'},physicalThread);
  run('dmMsg')(physicalRow,physicalThread);
  run('dmMsg')({...physicalRow,synced_from:'me/laptop'},{...physicalThread,kind:'dm'});
  check(run('physicalGroupFiles').map(m=>m.dir).join(',')==='in,in,out,out'&&physicalRow.dir==='out'&&run('physicalGroupFiles')[0].id===physicalRow.id,'group forwarded files route exact inbox while original display, local outbox and DM remain unchanged');
  run('fileChips=originalGroupFileChips');
    if (failed) process.exit(1);
  finished = true;
  console.log("page logic ok");
})().catch((e) => { console.error(e); process.exit(1); });
// A wait that never ends would let node exit quietly with nothing checked
// after it: that is a failure, said so.
let finished = false;
process.on("beforeExit", () => { if (!finished) { console.error("FAIL: the checks ended early: something awaited never answered"); process.exit(1); } });
