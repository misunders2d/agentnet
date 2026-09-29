// The relay's page: this browser as an AgentNet device. It checks what the
// browser can do, allows one tab only, proves storage works, joins only
// when the person asks (with an invitation for this server), then starts
// the engine and the usual views (app.js, lenses.js) over it.
import { Engine, openIDB, probeStore } from "./engine.mjs";
import { support } from "./wire.mjs";

const invitePrefix = "#agentnet-invite-v1:";

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
function show(...kids) {
  panel.replaceChildren(el("h1", {}, "AgentNet"), ...kids.flat().filter(Boolean).map((k) => (typeof k === "string" ? el("p", {}, k) : k)));
}

// The code-trust and storage limits, said before anything is created.
const limits = () => [
  el("p", { class: "hint" }, "This page's code comes from this server. Whoever runs the server could change it, and code from here can use this device's keys and read its messages. Use a server you trust."),
  el("p", { class: "hint" }, "This device's keys stay in this browser and cannot be copied out or backed up. If the browser clears this site's data, the device is lost: join again and ask your server's admin to remove the old one."),
];

async function main() {
  // An invitation in the link is taken once and removed from the address
  // before anything else is done with it, so it is not kept in history or
  // shown again, even when it is damaged. It is not logged.
  let code = "", damaged = false;
  if (location.hash.startsWith(invitePrefix)) {
    const raw = location.hash.slice(1);
    history.replaceState(null, "", location.pathname + location.search);
    try { code = decodeURIComponent(raw); } catch (e) { damaged = true; }
  }
  document.querySelector(".app").hidden = true;
  panel = el("main", { id: "device-setup", class: "relay-page" });
  document.body.prepend(panel);
  show("Checking this browser…");
  const missing = globalThis.isSecureContext ? await support() : ["a secure (https) connection"];
  if (typeof indexedDB === "undefined") missing.push("IndexedDB storage");
  if (!navigator.locks) missing.push("Web Locks");
  if (missing.length) {
    show("This browser cannot hold an AgentNet device: it lacks " + missing.join(", ") + ".");
    return;
  }
  // One tab holds the device: a second one never opens a second stream or
  // writes anything.
  navigator.locks.request("agentnet-device", { ifAvailable: true }, async (lock) => {
    if (!lock) {
      show("AgentNet is already open in another tab of this browser. Use that tab: this one does nothing, so nothing is sent or received twice.");
      return;
    }
    await run(code, damaged);
    await new Promise(() => {}); // the lock is held while this tab is open
  });
}

async function run(code, damaged) {
  let store;
  try {
    store = await openIDB();
    await probeStore(store);
  } catch (e) {
    show("This browser does not keep data for this site (" + e.message + "), so it cannot hold a device. Private windows and blocked site data do this.");
    return;
  }
  const engine = new Engine({ store, base: location.origin });
  if (await engine.load()) start(engine);
  else joinScreen(engine, code, damaged);
}

function joinScreen(engine, code, damaged) {
  const invite = el("textarea", { id: "join-code", rows: "3", autocomplete: "off", spellcheck: "false", placeholder: "agentnet-invite-v1:…" });
  const name = el("input", { id: "join-name", type: "text", autocomplete: "off", spellcheck: "false", maxlength: "32", placeholder: "phone" });
  const error = el("p", { class: "error", role: "alert" });
  const button = el("button", { type: "button", class: "btn primary" }, "Join");
  button.addEventListener("click", async () => {
    if (button.disabled) return;
    button.disabled = true;
    error.textContent = "";
    try {
      // Asked for when the person acts; the answer is shown, not assumed.
      let persisted = null;
      try { persisted = navigator.storage && navigator.storage.persist ? await navigator.storage.persist() : null; } catch (e) { persisted = null; }
      await engine.join(code || invite.value.trim(), name.value.trim());
      engine.storage = { persisted };
      start(engine);
    } catch (e) {
      error.textContent = e.message;
      button.disabled = false;
    }
  });
  show(el("p", {}, "Join this server with this browser as one of your devices. It is for you, the person: nothing runs in it, and questions and tasks sent to it wait for you."),
    code ? el("p", {}, "An invitation came with the link.") : [damaged && el("p", { class: "error" }, "The invitation in the link is damaged. Paste the invitation instead."),
      el("label", { for: "join-code", class: "field-label" }, "Invitation"), invite],
    el("label", { for: "join-name", class: "field-label" }, "A name for this device (lowercase, such as phone)"), name,
    limits(), error, button);
  name.focus();
}

async function start(engine) {
  if (!engine.storage && navigator.storage && navigator.storage.persisted) {
    try { engine.storage = { persisted: await navigator.storage.persisted() }; } catch (e) { engine.storage = { persisted: null }; }
  }
  panel.hidden = true;
  document.querySelector(".app").hidden = false;
  window.agentnetEngine = { api: (path, body) => engine.api(path, body), listen: (fn) => engine.listen(fn) };
  engine.start();
  window.addEventListener("online", () => engine.online());
  window.addEventListener("offline", () => engine.offline());
  for (const src of ["/assets/lenses.js", "/assets/app.js"]) {
    await new Promise((res, rej) => { const s = el("script", { src }); s.onload = res; s.onerror = () => rej(new Error("could not load " + src)); document.head.append(s); });
  }
}

main().catch((e) => show("AgentNet could not start here: " + e.message));
