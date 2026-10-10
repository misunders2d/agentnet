// The relay's page: this browser as an AgentNet device. It checks what the
// browser can do, allows one tab only, proves storage works, and starts the
// engine and the UI host (loader.js), which mounts the chosen skin over it.
// A device that has not joined gets "Get AgentNet" (landing.mjs): the app
// on a computer, this page on a phone's home screen, which joins only when
// the person taps Join, under an automatic device name.
import * as engineModule from "./engine.mjs";
import { Engine, openIDB, probeStore, sameOrigin } from "./engine.mjs";
import { addManifest, appBanner, installOffer, landing } from "./landing.mjs";
import { decodeInvite, decodeOffer, newID, support, validName } from "./wire.mjs";
import * as ws from "./workspaces.mjs";

const invitePrefix = "#agentnet-invite-v1:";
const linkPrefix = "#agentnet-link-v2:"; // a device link from another device of your person (its QR)
const openPrefix = "#agentnet-open:"; // a notification's click (sw.js)
const wsOpenPrefix = "#agentnet-workspace-open:"; // a click on another workspace's notification (workspaces-sw.js): <id>:<chan>

// push is the engine's Web Push adapter. The service worker is registered
// only when the person turns notifications on (or they were on already).
const b64url = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const subscriptionJSON = (s) => { const j = s.toJSON(); return { endpoint: j.endpoint, p256dh: j.keys.p256dh, auth: j.keys.auth }; };
const sameKey = (s, key) => { const k = s.options && s.options.applicationServerKey; return !k || b64url(new Uint8Array(k)) === key; };
const push = {
  supported: () => "serviceWorker" in navigator && "PushManager" in globalThis && "Notification" in globalThis,
  async subscribe(key) {
    const reg = await navigator.serviceWorker.register("/sw.js");
    await navigator.serviceWorker.ready;
    let s = await reg.pushManager.getSubscription();
    if (s && !sameKey(s, key)) { await s.unsubscribe(); s = null; }
    if (!s) s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    return subscriptionJSON(s);
  },
  // current is the subscription now, made again if the browser dropped it
  // (never asking for permission: only when it is still granted).
  async current(key) {
    if (Notification.permission !== "granted") return null;
    return this.subscribe(key);
  },
};

// A notification's click names a channel; the views open it once they are loaded.
let pendingOpen = null;
function openNotified(chan) {
  if (window.agentnetOpen) window.agentnetOpen(chan);
  else pendingOpen = chan;
}
function takeOpen() {
  if (location.hash.startsWith(wsOpenPrefix)) { // opened only once the workspace it names is registered here
    const [id, chan] = location.hash.slice(wsOpenPrefix.length).split(":");
    history.replaceState(null, "", location.pathname + location.search);
    pendingWorkspaceOpen = { id: /^[a-f0-9]{32}$/.test(id) ? id : "", chan: /^[A-Za-z0-9_-]{0,22}$/.test(chan || "") ? chan : "" };
    if (shell) openWorkspaceRoute();
    return;
  }
  if (!location.hash.startsWith(openPrefix)) return;
  const chan = location.hash.slice(openPrefix.length);
  history.replaceState(null, "", location.pathname + location.search);
  openNotified(/^[A-Za-z0-9_-]{0,22}$/.test(chan) ? chan : "");
}

// ---- workspaces ------------------------------------------------------------------
//
// The device this page enrolled (in this site's own store, under its own
// lock) is the default workspace, adopted as it is. Other workspaces are
// enrollments of their own (workspaces.mjs): each with its own keys,
// store, lock, stream and push registration, at the server its invitation
// named. Nothing received can add a server: only the person's Join, with
// an invitation, and only to a server this relay's page may connect to.
let shell = null, memberships = null;
const pushAdapters = new Map(); // workspace id -> its push adapter (its own registration)
let pendingWorkspaceOpen = null;
const originsKey = "agentnet.workspaces.origins.v1"; // servers the person joined by invitation, explicitly
const consented = () => { try { const l = JSON.parse(localStorage.getItem(originsKey) || "[]"); return Array.isArray(l) ? l : []; } catch (e) { return []; } };
const consent = (base) => { const l = consented(); if (!l.includes(base)) { l.push(base); localStorage.setItem(originsKey, JSON.stringify(l)); } };
// pageConnects lists what this page's own policy lets it connect to (the
// relay's admin sets it: agentnet hub serve --browser-origin), or null when
// the response has no policy header. An unavailable probe defers secondary
// startup; the browser also enforces the document's actual policy.
async function pageConnects() {
  try {
    const r = await ws.workspaceProbe(fetch, location.pathname, { method: "HEAD", cache: "no-store" });
    if (!r.ok) throw new Error("Connection policy unavailable");
    const csp = r.headers.get("content-security-policy");
    if (!csp) return null;
    const m = /(?:^|;)\s*connect-src([^;]*)/.exec(csp);
    return m ? m[1].trim().split(/\s+/) : [];
  } catch (e) { throw new Error("This page's connection policy could not be checked: " + e.message); }
}
// allowOrigin: a workspace's server is reached only if the person joined
// it by invitation here and this page may connect to it. Never from traffic.
async function allowOrigin(base) {
  if (base === location.origin) return;
  const host = new URL(base).host;
  if (!consented().includes(base)) throw new Error("This browser has not joined a workspace at " + host + ".");
  const allowed = await pageConnects();
  if (allowed !== null && !allowed.includes(base)) throw new Error("This server's page may not connect to " + host + ". The admin of this server can allow it (agentnet hub serve --browser-origin).");
}
async function workspaces(engine) {
  shell = new ws.WorkspaceShell();
  const problems = [];
  try {
    memberships = new ws.BrowserMemberships({ shell, Engine, openIDB, locks: navigator.locks, storage: localStorage, fetch: (u, o) => fetch(u, o),
      decodeInvite, newID, allowOrigin, pushFor: (id) => { const a = ws.workspacePush(id); pushAdapters.set(id, a); return a; } });
    memberships.adoptDefault(engine, { name: "" }); // the device enrolled here, as it is: its store, lock and stream stay; no label of its own (the workspace's name shows)
  } catch (e) {
    problems.push("Joined workspaces could not be read here: " + e.message); memberships = null;
    // A broken secondary entry need not hide the default's local history.
    // Recover only its exact saved binding and realm; never start unguarded.
    const records = JSON.parse(localStorage.getItem("agentnet.workspaces.v1") || "[]");
    if (!Array.isArray(records)) throw e;
    const defaults = records.filter(r => r?.id === "default");
    if (defaults.length > 1) throw e;
    const record = defaults[0] || { id: "default", handle: newID(), name: "", endpoint: location.origin, address: engine.address, realm: "", state: "enrolled" };
    if (ws.workspaceEndpoint(record.endpoint) !== location.origin || !/^[a-f0-9]{32}$/.test(record.handle || "") || record.state !== "enrolled" || record.realm && !/^[a-f0-9]{32}$/.test(record.realm) || record.address && record.address !== engine.address) throw e;
    if (!defaults.length) records.unshift(record);
    engine.fetch = ws.workspaceRealmFetch(engine.fetch.bind(engine), location.origin, record, () => localStorage.setItem("agentnet.workspaces.v1", JSON.stringify(records)), error => {
      engine.stop(); const entry = shell.members.get("default"); if (entry) entry.blockedError = error;
    });
    if (!shell.members.has("default")) shell.register(record, engine);
  }
  window.agentnetWorkspaces = {
    shell,
    async join({ name, invite, agent }) {
      if (!memberships) throw new Error("Joined workspaces cannot be kept in this browser.");
      agent = agent || engine.address.split("/")[1]; // this device's own name: people never name devices
      if (!validName(agent || "")) throw new Error("Use lowercase letters, numbers and dashes for the device name, like phone or work-laptop.");
      let inv;
      try { inv = decodeInvite(invite); } catch (e) { throw new Error("That is not a complete invitation code. Copy all of it again, or ask the sender for a new link."); }
      if (inv.cert) throw new Error("This invitation is for the AgentNet program on a computer, not for a browser. Ask the sender for a browser invite link.");
      const base = ws.workspaceEndpoint(inv.hub);
      if (base === location.origin) throw new Error("That invitation is for this server, which this browser already holds as its own workspace.");
      consent(base); // the person's Join with an invitation naming that server
      return memberships.join({ name, invite, agent });
    },
    async disconnect(id) {
      if (!memberships) throw new Error("Unknown workspace");
      await memberships.disconnect(id);
      const still = memberships.records.filter((r) => r.state !== "disconnected").map((r) => r.endpoint);
      localStorage.setItem(originsKey, JSON.stringify(consented().filter((o) => still.includes(o))));
    },
  };
  return { problems, async restore() {
    if (!memberships) return;
    // Restore independently, after the default view mounts. A stalled server
    // does not hold the local view or another membership's startup.
    const records = memberships.records.filter(r => r.state === "enrolled" && !shell.members.has(r.id));
    let next = 0;
    await Promise.all(Array.from({ length: Math.min(2, records.length) }, async () => {
      while (next < records.length) {
        const r = records[next++];
        try { await memberships.start(r); engine.changed(); }
        catch (e) {
          let label = r.name;
          if (!label) { try { label = new URL(r.endpoint).host; } catch (_) { label = "Workspace"; } }
          problems.push(label + ": " + e.message);
        }
        if (pendingWorkspaceOpen?.id === r.id) openWorkspaceRoute();
      }
    }));
  } };
}
// openWorkspaceRoute opens what a workspace notification's click in a new
// window named, once that workspace is registered here; an unknown one
// opens nothing (never the workspace shown instead).
function openWorkspaceRoute() {
  const p = pendingWorkspaceOpen;
  if (!p || !shell) return;
  const e = p.id && shell.members.get(p.id);
  if (!e || !e.connected) return;
  pendingWorkspaceOpen = null;
  shell.openFromRegistration(p.id, p.chan, (chan) => openNotified(chan));
}

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat()) if (k !== undefined && k !== null && k !== false && k !== "") e.append(typeof k === "string" ? document.createTextNode(k) : k);
  return e;
}

let panel;
// show fills the page's one card: a title, then what this step says.
function show(title, ...kids) {
  panel.replaceChildren(el("div", { class: "join-card" }, el("h1", {}, title),
    kids.flat().filter(Boolean).map((k) => (typeof k === "string" ? el("p", {}, k) : k))));
}

// The storage note, and what it and the server's trust mean, in a few
// plain words up front and the rest one click away.
const storageNote = () => el("p", { class: "join-note" }, "Your chats are saved in this browser. Clearing its data removes them.");
const privacy = () => el("details", { class: "join-more" }, el("summary", {}, "Privacy and storage"),
  el("p", {}, "Messages are encrypted from device to device. Your server passes them on and cannot read them."),
  el("p", {}, "This page itself comes from your server each time you open it. Whoever runs the server could change it, and a changed page could read your chats here. Use a server you trust."),
  el("p", {}, "This browser's key is made here and cannot be copied out, so there is no backup. If the browser's data for this site is cleared, this browser cannot read its chats again: join again with a new invitation, and ask your server's admin to remove the old one."),
  el("p", {}, "Nothing runs in this browser. Questions and tasks sent here wait for you."));

// takeInvite takes an invitation from the link and removes it from the
// address, and so from this history entry, before anything else is done
// with it: it is not kept or shown again, even when it is damaged. It is
// not logged.
function takeInvite() {
  const link = location.hash.startsWith(linkPrefix);
  if (!link && !location.hash.startsWith(invitePrefix)) return null;
  const raw = location.hash.slice(1);
  history.replaceState(null, "", location.pathname + location.search);
  try { return { code: decodeURIComponent(raw), damaged: false, link }; } catch (e) { return { code: "", damaged: true, link }; }
}

// linkProblem says why a device link cannot be used here ("" when it can).
function linkProblem(code) {
  let o;
  try { o = decodeOffer(code); } catch (e) {
    return "That device link is not complete. Make a new one on your other device (Your devices, Add a device).";
  }
  if (Date.now() / 1000 >= o.expires) return "That device link expired. Make a new one on your other device (Your devices, Add a device).";
  return inviteProblem(o.invite);
}

// inviteProblem says, in the person's words, why code cannot be used to
// join here ("" when it can). The engine checks the same again.
function inviteProblem(code) {
  if (!code) return "Paste the invitation code you were sent.";
  let inv;
  try { inv = decodeInvite(code); } catch (e) {
    return "That is not a complete invitation code. Copy all of it again, or ask the sender for a new link.";
  }
  if (inv.cert) return "This invitation is for the AgentNet program on a computer, not for a browser. Ask the sender for a browser invite link.";
  try { sameOrigin(inv.hub, location.origin); } catch (e) {
    return "This invitation is for another server (" + new URL(inv.hub).host + "). Open the invite link you were sent instead.";
  }
  return "";
}

let onInvite = null; // the join screen, while it is shown

async function main() {
  // A link opened in this tab while the page is open changes only the
  // fragment, without loading the page again: it is taken the same way.
  window.addEventListener("hashchange", () => {
    const inv = takeInvite();
    if (inv && onInvite) onInvite(inv);
    takeOpen();
  });
  takeOpen();
  const link = takeInvite();
  addManifest(); // phones only: a computer is never offered this page as an app
  document.head.append(el("link", { rel: "stylesheet", href: "/assets/landing.css" }));
  document.getElementById("skin").hidden = true;
  panel = el("main", { id: "device-setup", class: "join" });
  document.body.prepend(panel);
  show("AgentNet", "Opening…");
  if (!globalThis.isSecureContext) {
    show("AgentNet", "AgentNet needs a secure (https) address. Ask your server's admin for it.");
    return;
  }
  const missing = await support();
  if (typeof indexedDB === "undefined") missing.push("storage for sites");
  if (!navigator.locks) missing.push("tab locks");
  if (missing.length) {
    show("AgentNet", "This browser cannot run AgentNet (it lacks " + missing.join(", ") + "). Try an up-to-date browser.");
    return;
  }
  // One tab holds the device: a second one never opens a second stream or
  // writes anything.
  navigator.locks.request("agentnet-device", { ifAvailable: true }, async (lock) => {
    if (!lock) {
      show("AgentNet", "AgentNet is already open in another tab or window. Use that one: this one stays idle, so nothing is sent or received twice.");
      return;
    }
    await run(link);
    await new Promise(() => {}); // the lock is held while this tab is open
  });
}

async function run(link) {
  let store;
  try {
    store = await openIDB();
    await probeStore(store);
  } catch (e) {
    show("AgentNet", "This browser cannot save chats for this site, so AgentNet cannot run here. Private windows and blocked site data can do this: open the link in a regular window.",
      el("p", { class: "hint" }, "(" + e.message + ")"));
    return;
  }
  // The build this page was served as: the one its server wrote into the
  // engine (BUILD) when it does, else its first version answer. Once the
  // server serves another, the skin reloads the page as for any new
  // version, never over unsent drafts (engine.mjs, "this page outdated").
  const engine = new Engine({ store, base: location.origin, push, pageBuild: engineModule.BUILD || "" });
  if (await engine.load()) start(engine);
  else showLanding(engine, link);
}

// showLanding is "Get AgentNet" for a browser that has not joined
// (landing.mjs). Only a phone joins here, when the person taps Join: this
// browser's engine joins under an automatic name, then the person is
// created with the name they gave.
function showLanding(engine, link) {
  const persist = async () => {
    // Asked for when the person acts; the answer is shown, not assumed.
    try { return navigator.storage && navigator.storage.persist ? await navigator.storage.persist() : null; } catch (e) { return null; }
  };
  const ui = {
    el, show, onCode: null,
    googleConfig: () => engine.call("GET", "/v1/google/config", undefined, { signed: false }),
    googleNonce: () => engine.googleNonce(),
    async googleJoin(token, device) { const persisted = await persist(); await engine.joinGoogle(token, device); engine.storage = { persisted }; onInvite = null; start(engine, true); },
    notes: () => [storageNote(), privacy()],
    async join(code, device, name) {
      const problem = inviteProblem(code);
      if (problem) throw new Error(problem);
      const persisted = await persist();
      await engine.joinAuto(code, device);
      try { await engine.createPerson(name); } catch (e) { /* the messenger offers "Choose the name people see" again */ }
      engine.storage = { persisted };
      onInvite = null;
      start(engine, true);
    },
    async link(code, device) {
      const problem = linkProblem(code);
      if (problem) throw new Error(problem);
      const persisted = await persist();
      await engine.joinAndLinkAuto(code, device);
      engine.storage = { persisted };
      onInvite = null;
      start(engine, true);
    },
  };
  onInvite = (inv) => { if (!inv.damaged && ui.onCode) ui.onCode(inv.code); else landing(ui, inv); };
  landing(ui, link).catch((e) => show("AgentNet", "AgentNet could not start here: " + e.message));
}

async function start(engine, joinedNow) {
  if (!engine.storage && navigator.storage && navigator.storage.persisted) {
    try { engine.storage = { persisted: await navigator.storage.persisted() }; } catch (e) { engine.storage = { persisted: null }; }
  }
  // The shell first: adopting this device installs the realm guard on its
  // transport, and the first stream must not outrun it.
  let configuration;
  try { configuration = await workspaces(engine); }
  catch (error) { show("AgentNet", "AgentNet could not start here: " + error.message); return; }
  const { problems, restore } = configuration;
  panel.hidden = true;
  document.getElementById("skin").hidden = false;
  // loader.js binds the host's Drive provider to this engine's (driveService).
  window.agentnetEngine = { api: (path, body) => engine.api(path, body), listen: (fn) => engine.listen(fn), driveService: () => engine.driveService() };
  engine.start();
  window.addEventListener("online", () => engine.online());
  window.addEventListener("offline", () => engine.offline());
  // A phone suspends this page in the background; shown again, every
  // workspace's stream that is down or silent reconnects now (Engine.resume).
  const resume = () => { for (const e of new Set([engine, ...[...(shell?.members.values() || [])].map((m) => m.engine)])) e?.resume?.(); };
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") resume(); });
  window.addEventListener("pageshow", (e) => { if (e.persisted) resume(); }); // restored from the back-forward cache
  if (navigator.serviceWorker) {
    navigator.serviceWorker.addEventListener("message", (e) => {
      if (!e.data) return;
      const chan = typeof e.data.chan === "string" ? e.data.chan : "";
      if (e.data.type === "agentnet-open") { openNotified(chan); return; }
      if (e.data.type !== "agentnet-workspace-open" || !shell) return;
      // Routed by which registration the worker belongs to, never by
      // anything the message says; an unknown source opens nothing.
      for (const [id, a] of pushAdapters) {
        if (a.workspaceForEvent(e) !== id) continue;
        try { shell.openFromRegistration(id, chan, (c) => openNotified(c)); } catch (err) { /* not registered here */ }
        return;
      }
    });
  }
  for (const src of ["/assets/loader.js"]) {
    await new Promise((res, rej) => { const s = el("script", { src }); s.onload = res; s.onerror = () => rej(new Error("could not load " + src)); document.head.append(s); });
  }
  if (pendingOpen !== null && window.agentnetOpen) { window.agentnetOpen(pendingOpen); pendingOpen = null; }
  openWorkspaceRoute();
  const showProblems = () => {
    if (!problems.length) return;
    const note = el("p", { class: "workspace-problems", role: "status" }, "Not connected now: " + problems.join(" · "));
    document.body.prepend(note);
  };
  // Restore without selecting a workspace or remounting its view. Registration
  // notifies the existing engine listeners; operations keep their own binding.
  void restore().then(showProblems, error => { problems.push("Joined workspaces: " + error.message); showProblems(); });
  // On a computer the app is the way to use AgentNet (MEL-536); a phone that
  // just joined is offered its home screen.
  const note = joinedNow ? installOffer({ el }) : await appBanner({ el });
  if (note) document.getElementById("skin").before(note);
}

main().catch((e) => show("AgentNet", "AgentNet could not start here: " + e.message));
