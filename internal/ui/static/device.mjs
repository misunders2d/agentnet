// The relay's page: this browser as an AgentNet device. It checks what the
// browser can do, allows one tab only, proves storage works, joins only
// when the person asks (with an invitation for this server), then starts
// the engine and the usual views (app.js, lenses.js) over it.
import { Engine, openIDB, probeStore, sameOrigin } from "./engine.mjs";
import { decodeInvite, decodeOffer, support, validName } from "./wire.mjs";

const invitePrefix = "#agentnet-invite-v1:";
const linkPrefix = "#agentnet-link-v2:"; // a device link from another device of your person (its QR)
const openPrefix = "#agentnet-open:"; // a notification's click (sw.js)

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
  if (!location.hash.startsWith(openPrefix)) return;
  const chan = location.hash.slice(openPrefix.length);
  history.replaceState(null, "", location.pathname + location.search);
  openNotified(/^[A-Za-z0-9_-]{0,22}$/.test(chan) ? chan : "");
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
  const engine = new Engine({ store, base: location.origin, push });
  if (await engine.load()) start(engine);
  else joinScreen(engine, link);
}

// joinScreen asks for a name for this browser, and for an invitation code
// only when the link brought none that can be used here. Nothing is
// enrolled or named until the person presses Join.
function joinScreen(engine, link) {
  onInvite = (inv) => joinScreen(engine, inv);
  if (link && link.link) { linkScreen(engine, link); return; }
  const fromLink = link && !link.damaged ? link.code : "";
  const linkProblem = link && (link.damaged ? "damaged" : inviteProblem(fromLink));
  const needCode = !fromLink || !!linkProblem;
  const invite = el("textarea", { id: "join-code", rows: "3", autocomplete: "off", spellcheck: "false", placeholder: "agentnet-invite-v1:…" });
  const name = el("input", { id: "join-name", type: "text", autocomplete: "off", autocapitalize: "none", spellcheck: "false", maxlength: "32", placeholder: "phone", "aria-describedby": "join-name-hint" });
  const error = el("p", { class: "error", role: "alert", id: "join-error" });
  const button = el("button", { type: "submit", class: "btn primary join-go" }, "Join");
  const form = el("form", { class: "join-form", novalidate: true },
    needCode && [el("label", { for: "join-code" }, "Invitation code"), invite],
    el("label", { for: "join-name" }, "Name this browser"), name,
    el("p", { class: "hint", id: "join-name-hint" }, "So you can tell your devices apart. Lowercase, like phone or work-laptop."),
    error, button);
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (button.disabled) return;
    const code = needCode ? invite.value.trim() : fromLink;
    const chosen = name.value.trim();
    const problem = inviteProblem(code) ||
      (!chosen ? "Choose a name for this browser." : !validName(chosen) ? "Use lowercase letters, numbers and dashes for the name, like phone or work-laptop." : "");
    error.textContent = problem; // each try replaces what the last one said
    if (problem) return;
    button.disabled = true;
    try {
      // Asked for when the person acts; the answer is shown, not assumed.
      let persisted = null;
      try { persisted = navigator.storage && navigator.storage.persist ? await navigator.storage.persist() : null; } catch (e) { persisted = null; }
      await engine.join(code, chosen);
      engine.storage = { persisted };
      onInvite = null;
      start(engine);
    } catch (e) {
      error.textContent = e.message;
      button.disabled = false;
    }
  });
  const recovery = linkProblem === "damaged" ? "This invite link could not be opened. Paste an invitation code below, or ask the sender for a new link."
    : linkProblem || (!link ? "Open the invite link you were sent, or paste the invitation code below." : "");
  show("Join AgentNet", el("p", { class: "join-intro" }, "Chat with people and their agents."),
    recovery && el("p", { class: "join-recovery" }, recovery),
    form, storageNote(), privacy());
  (needCode ? invite : name).focus();
}

// linkScreen joins this browser as a new device of the person whose
// other device made the link: a name for this browser, then that device's
// approval. Nothing is joined until the person presses Add.
function linkScreen(engine, link) {
  const problem = link.damaged ? "This device link could not be opened. Make a new one on your other device." : linkProblem(link.code);
  const name = el("input", { id: "join-name", type: "text", autocomplete: "off", autocapitalize: "none", spellcheck: "false", maxlength: "32", placeholder: "phone", "aria-describedby": "join-name-hint" });
  const error = el("p", { class: "error", role: "alert", id: "join-error" });
  const button = el("button", { type: "submit", class: "btn primary join-go", disabled: !!problem }, "Add this browser");
  const form = el("form", { class: "join-form", novalidate: true },
    el("label", { for: "join-name" }, "Name this browser"), name,
    el("p", { class: "hint", id: "join-name-hint" }, "So you can tell your devices apart. Lowercase, like phone or work-laptop."),
    error, button);
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (button.disabled) return;
    const chosen = name.value.trim();
    const why = !chosen ? "Choose a name for this browser." : !validName(chosen) ? "Use lowercase letters, numbers and dashes for the name, like phone or work-laptop." : "";
    error.textContent = why;
    if (why) return;
    button.disabled = true;
    try {
      let persisted = null;
      try { persisted = navigator.storage && navigator.storage.persist ? await navigator.storage.persist() : null; } catch (e) { persisted = null; }
      await engine.joinAndLink(link.code, chosen);
      engine.storage = { persisted };
      onInvite = null;
      start(engine);
    } catch (e) {
      error.textContent = e.message;
      button.disabled = false;
    }
  });
  show("Add this browser to you", el("p", { class: "join-intro" }, "This link comes from one of your devices. Name this browser, then approve it on that device: it becomes one more device of yours, with your chats."),
    problem && el("p", { class: "join-recovery" }, problem),
    form, storageNote(), privacy());
  name.focus();
}

async function start(engine) {
  if (!engine.storage && navigator.storage && navigator.storage.persisted) {
    try { engine.storage = { persisted: await navigator.storage.persisted() }; } catch (e) { engine.storage = { persisted: null }; }
  }
  panel.hidden = true;
  document.getElementById("skin").hidden = false;
  window.agentnetEngine = { api: (path, body) => engine.api(path, body), listen: (fn) => engine.listen(fn) };
  engine.start();
  window.addEventListener("online", () => engine.online());
  window.addEventListener("offline", () => engine.offline());
  if (navigator.serviceWorker) {
    navigator.serviceWorker.addEventListener("message", (e) => {
      if (e.data && e.data.type === "agentnet-open") openNotified(typeof e.data.chan === "string" ? e.data.chan : "");
    });
  }
  for (const src of ["/assets/loader.js"]) {
    await new Promise((res, rej) => { const s = el("script", { src }); s.onload = res; s.onerror = () => rej(new Error("could not load " + src)); document.head.append(s); });
  }
  if (pendingOpen !== null && window.agentnetOpen) { window.agentnetOpen(pendingOpen); pendingOpen = null; }
}

main().catch((e) => show("AgentNet", "AgentNet could not start here: " + e.message));
