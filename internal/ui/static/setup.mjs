// The AgentNet app's first-run page (internal/ui/setup.go): this computer
// has not joined yet. The invitation (or a device link from another device
// of yours) arrives in the address's fragment, when the app was opened from
// an invitation link, or is pasted here. The page shows the server it is for
// and what the invitation says (the inviter's words, not proof), and joins
// only when the person presses Join. The device is named automatically.
// A computer whose membership ended (its device link refused or not
// approved in time, or removed by its server) says so and starts again
// only when the person presses Start again.
const invitePrefix = "agentnet-invite-v1:";
const linkPrefix = "agentnet-link-v2:";

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

async function call(path, body) {
  const r = await fetch(path, body === undefined ? { cache: "no-store" } : {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), cache: "no-store" });
  const t = await r.text();
  if (!r.ok) throw new Error(t.trim() || "AgentNet could not do that.");
  return t ? JSON.parse(t) : null;
}

// codeFrom finds an invitation or device link in what was pasted or opened:
// the link itself (https://server/#…), an agentnet://open#… link, or the
// bare code. "" when there is none.
export function codeFrom(text) {
  let s = String(text || "").trim();
  const hash = s.indexOf("#");
  if (hash >= 0 && !s.startsWith(invitePrefix) && !s.startsWith(linkPrefix)) s = s.slice(hash + 1);
  try { s = decodeURIComponent(s); } catch (e) { return ""; }
  s = s.trim();
  return s.startsWith(invitePrefix) || s.startsWith(linkPrefix) || s.startsWith("google-signin=") ? s : "";
}

export function canScanInvitation(state) {
  return state === "none" || state === "incomplete";
}

// Scanning yields untrusted text only; the existing inspect and explicit Join remain the authority.
export async function scanInvitation(scanner) {
  const result = await scanner();
  if (result?.canceled === true) return "";
  if (typeof result?.text !== "string" || result.text.length > 16 * 1024 || new TextEncoder().encode(result.text).length > 16 * 1024)
    throw new Error("This QR code is not an AgentNet invitation. Paste the complete invitation link instead.");
  const code = codeFrom(result.text);
  if (!code) throw new Error("This QR code is not an AgentNet invitation. Paste the complete invitation link instead.");
  return code;
}

let panel, state = { state: "none", device: "", device_words: "" };
let held = ""; // an invitation that arrived while the computer's membership had ended
let setupBusy = false;

// endedWords is what an ended membership says: a title and why.
export const endedWords = {
  refused: ["Adding this computer was refused", "It was refused on your other device."],
  expired: ["This computer was not added", "Nobody approved it on your other device in time."],
  removed: ["This computer was removed", "Its AgentNet server removed it."],
};

function show(title, ...kids) {
  panel.replaceChildren(el("div", { class: "join-card" }, el("h1", {}, title),
    kids.flat().filter(Boolean).map((k) => (typeof k === "string" ? el("p", {}, k) : k))));
}

// welcome asks for the invitation when none came with the app's opening.
function welcome(problem) {
  const box = el("textarea", { id: "setup-code", rows: "3", autocomplete: "off", spellcheck: "false", placeholder: "Paste the invitation link here" });
  const error = el("p", { class: "error", role: "alert", id: "setup-error" }, problem || "");
  const go = el("button", { type: "submit", class: "btn primary join-go" }, "Continue");
  const form = el("form", { class: "join-form", novalidate: true }, el("label", { for: "setup-code" }, "Your invitation"), box, error, go);
  const nativeScan = window.__agentnetPlatform === "android" && typeof window.__agentnetAndroid?.scanQR === "function" && canScanInvitation(state.state);
  const scan = nativeScan && el("button", { type: "button", class: "btn primary join-go" }, "Scan QR code");
  if (scan) scan.addEventListener("click", async () => {
    if (scan.disabled || setupBusy || !canScanInvitation(state.state)) return;
    scan.disabled = true;
    try {
      const code = await scanInvitation(() => window.__agentnetAndroid.scanQR());
      if (!code || setupBusy || !box.isConnected || !canScanInvitation(state.state)) return;
      box.value = code; error.textContent = "";
      await inspect(code);
    } catch (failure) { if (box.isConnected) error.textContent = failure.message || "The camera scanner could not open. Paste the invitation link instead."; }
    finally { scan.disabled = false; }
  });
  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    if (setupBusy) return;
    const code = codeFrom(box.value);
    if (!code) { error.textContent = "That is not an AgentNet invitation. Copy the whole link you were sent, or ask for a new one."; return; }
    inspect(code);
  });
  show("Welcome to AgentNet",
    el("p", { class: "join-intro" }, "Chat with people and their agents."),
    state.state === "incomplete" && el("p", { class: "join-recovery" }, "Joining did not finish last time. Sign in with the same Google account to finish it, or reuse your invitation below."),
    "Open your workspace’s Get AgentNet link, or enter its address below.",
    scan, googleForm(), el("details", { open: nativeScan }, el("summary", {}, "Use an invitation or device link"), form));
  panel.querySelector("#google-hub").focus();
}

function googleForm(hub = "") {
  const address = el("input", { id: "google-hub", type: "url", value: hub, placeholder: "https://your-workspace.example", autocomplete: "url", required: true });
  const error = el("p", { class: "error", role: "alert" });
  const button = el("button", { type: "submit", class: "btn primary join-go" }, "Sign in with Google");
  const cancel = el("button", { type: "button", class: "btn", hidden: true }, "Cancel sign-in");
  cancel.addEventListener("click", async () => { try { await call("/api/setup/google/cancel", {}); setupBusy = false; } catch (e) { error.textContent = e.message; } });
  const form = el("form", { class: "join-form" }, el("label", { for: "google-hub" }, "Workspace address"), address,
    el("p", { class: "hint" }, "Use your invited Google email. Another device of yours must approve each later device."), error, button, cancel);
  form.addEventListener("submit", async ev => {
    ev.preventDefault(); if (button.disabled || setupBusy) return; button.disabled = true; setupBusy = true; error.textContent = "";
    try {
      await call("/api/setup/google", { hub: address.value.trim() });
      cancel.hidden = false;
      error.textContent = "Continue in your browser. Return here after Google sign-in.";
      const events = new EventSource("/api/setup/google/events");
      events.onmessage = event => { events.close(); cancel.hidden = true; const result = JSON.parse(event.data); if (result.state === "joined") location.replace("/"); else { setupBusy = false; error.textContent = result.problem; button.disabled = false; } };
      events.onerror = () => { events.close(); location.replace("/"); };
    } catch (e) { setupBusy = false; error.textContent = e.message; button.disabled = false; cancel.hidden = true;
      if (e.message.includes("not set up")) { const details = panel.querySelector("details"); if (details) { details.open = true; panel.querySelector("#setup-code")?.focus(); } else welcome(e.message); }
    }
  });
  return form;
}

// ended says how this computer's membership ended and starts again on the
// person's click: what it had is kept aside, never deleted.
function ended() {
  const [title, why] = endedWords[state.state];
  const error = el("p", { class: "error", role: "alert", id: "setup-error" });
  const go = el("button", { type: "button", class: "btn primary join-go" }, "Start again");
  go.addEventListener("click", async () => {
    if (go.disabled) return;
    go.disabled = true; error.textContent = "";
    try {
      state = await call("/api/setup/start-again", {});
    } catch (e) {
      error.textContent = e.message;
      go.disabled = false;
      return;
    }
    const code = held;
    held = "";
    if (code) inspect(code); else welcome("");
  });
  show(title, why,
    held ? "Start again to use the invitation you opened." : "Start again with a new invitation, or with a new link from your other device.",
    el("p", { class: "hint" }, "What this computer had is kept in a folder in AgentNet's data, not deleted."),
    error, go);
  go.focus();
}

async function inspect(code) {
  if (setupBusy) return;
  if (code.startsWith("google-signin=")) { const hub = code.slice("google-signin=".length); welcome(""); panel.querySelector("#google-hub").value = hub; return; }
  let inv;
  try { inv = await call("/api/setup/inspect", { code }); } catch (e) { welcome(e.message); return; }
  if (inv.problem) { welcome(inv.problem); return; }
  if (inv.kind === "link") linkCard(code, inv); else inviteCard(code, inv);
}

function serverLine(inv) {
  return el("p", { class: "join-recovery" }, "Server: ", el("strong", {}, inv.host),
    inv.workspace ? " · the invitation calls it “" + inv.workspace + "”" : "");
}

function inviteCard(code, inv) {
  const name = el("input", { id: "setup-name", type: "text", autocomplete: "name", maxlength: "64", value: inv.name || "", "aria-describedby": "setup-name-hint" });
  const error = el("p", { class: "error", role: "alert", id: "setup-error" });
  const go = el("button", { type: "submit", class: "btn primary join-go" }, "Join");
  const form = el("form", { class: "join-form", novalidate: true },
    el("label", { for: "setup-name" }, "Your name"), name,
    el("p", { class: "hint", id: "setup-name-hint" }, "The name people see. You can change it later."),
    error, go);
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (go.disabled || setupBusy) return;
    const chosen = name.value.trim();
    if (!chosen) { error.textContent = "Write the name people will see."; return; }
    go.disabled = true; setupBusy = true; error.textContent = "";
    go.textContent = "Joining…";
    try {
      await call("/api/setup/join", { code, name: chosen });
      location.replace("/"); // the messenger now serves this address
    } catch (e) {
      error.textContent = e.message;
      setupBusy = false; go.disabled = false; go.textContent = "Join";
    }
  });
  show(inv.from ? inv.from + " invited you" : "You're invited",
    serverLine(inv),
    inv.from && el("p", { class: "hint" }, "Who invited you and the names are what the invitation says; the server is where it leads."),
    el("p", {}, "This computer joins as your " + (state.device_words || "computer") + "."),
    form,
    el("button", { type: "button", class: "text-btn", onclick: () => welcome("") }, "Use another invitation"));
  name.focus();
}

function linkCard(code, inv) {
  const error = el("p", { class: "error", role: "alert", id: "setup-error" });
  const go = el("button", { type: "button", class: "btn primary join-go" }, "Add this computer");
  const until = inv.expires ? new Date(inv.expires) : null;
  go.addEventListener("click", async () => {
    if (go.disabled || setupBusy) return;
    go.disabled = true; setupBusy = true; error.textContent = "";
    go.textContent = "Adding…";
    try {
      await call("/api/setup/join", { code, name: "" });
      location.replace("/"); // waits there for the approval on your other device
    } catch (e) {
      error.textContent = e.message;
      setupBusy = false; go.disabled = false; go.textContent = "Add this computer";
    }
  });
  show("Add this computer to you",
    serverLine(inv),
    el("p", {}, "This link comes from one of your devices. This computer becomes one more device of yours, with your chats, once you approve it there with one tap."),
    until && el("p", { class: "hint" }, "The link works until " + until.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + ". If it runs out, make a new one on your other device."),
    error, go,
    el("button", { type: "button", class: "text-btn", onclick: () => welcome("") }, "Use another invitation"));
  go.focus();
}

// take removes an invitation from the address before anything is done with
// it, so it is not kept in this window's history.
function take() {
  const h = location.hash.slice(1);
  if (!h) return "";
  history.replaceState(null, "", location.pathname + location.search);
  return codeFrom("#" + h);
}

async function main() {
  document.head.append(el("link", { rel: "stylesheet", href: "/assets/landing.css" }));
  document.getElementById("skin").hidden = true;
  panel = el("main", { id: "app-setup", class: "join" });
  document.body.prepend(panel);
  show("AgentNet", "Opening…");
  try { state = await call("/api/setup"); } catch (e) { show("AgentNet", "AgentNet could not start: " + e.message); return; }
  // The app opened again from another invitation link while this page is open.
  window.addEventListener("hashchange", () => {
    const c = take();
    if (!c) return;
    if (endedWords[state.state]) { held = c; ended(); } else inspect(c);
  });
  const code = take();
  if (endedWords[state.state]) { held = code; ended(); } else if (code) inspect(code); else welcome("");
}

if (typeof document !== "undefined") main();
