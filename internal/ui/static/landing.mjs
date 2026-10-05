// "Get AgentNet": what the relay's page shows a device that has not joined
// (MEL-533, MEL-536). People use the AgentNet app: on a computer, an
// installer for its system, then the invitation opened in the app
// (agentnet://open#…); on a phone, this page added to the home screen.
// The server shown is the one the invitation leads to (from the code);
// who invited and the names are only what the invitation says. Nothing
// joins without the person's tap, and devices are named automatically.
import { appLink, detectPlatform, downloads, forPlatform, isPhone } from "./getapp.mjs";
import { mountGoogleSignIn } from "./google-signin.mjs";
import { decodeInvite, decodeOffer } from "./wire.mjs";

const phoneNames = { android: "android-phone", iphone: "iphone", ipad: "ipad" };
const systemWords = { windows: "Windows", macos: "macOS", linux: "Linux", android: "Android", iphone: "iPhone", ipad: "iPad" };

// platform is this device, from the browser's own report.
export function platform() {
  const nav = globalThis.navigator || {};
  return detectPlatform(nav.userAgent, nav.userAgentData && nav.userAgentData.platform, nav.maxTouchPoints);
}

// standalone: opened from the home screen (the installed page).
const standalone = () => !!(globalThis.matchMedia && matchMedia("(display-mode: standalone)").matches) || globalThis.navigator?.standalone === true;

// addManifest links the install manifest, on phones only: a computer gets
// the AgentNet app instead, and is never offered this page as an app.
export function addManifest() {
  if (!isPhone(platform()) || document.querySelector('link[rel="manifest"]')) return;
  const l = document.createElement("link");
  l.rel = "manifest";
  l.href = "/manifest.webmanifest";
  l.crossOrigin = "use-credentials";
  document.head.append(l);
}

// The install prompt Android's browser offers once (beforeinstallprompt),
// kept for the person's tap.
let installPrompt = null;
globalThis.addEventListener?.("beforeinstallprompt", (e) => { e.preventDefault(); installPrompt = e; });

let table = null, version = "";
async function getAppList() {
  if (!table) table = await (await fetch("/assets/getapp.json", { cache: "no-store" })).json();
  if (!version) { try { version = (await (await fetch("/v1/version", { cache: "no-store" })).json()).version || ""; } catch (e) { version = ""; } }
  return downloads(table, version);
}

// codeIn finds an invitation or device link in pasted text: the link, the
// app's own link or the bare code.
export function codeIn(text) {
  let s = String(text || "").trim();
  const hash = s.indexOf("#");
  if (hash >= 0 && !/^agentnet-(invite-v1|link-v2):/.test(s)) s = s.slice(hash + 1);
  try { s = decodeURIComponent(s).trim(); } catch (e) { return ""; }
  return /^agentnet-(invite-v1|link-v2):/.test(s) ? s : "";
}

// read says what a code is: an invitation or a device link, its server's
// host, the inviter's words, and why it cannot be used ("" when it can).
export function read(code, now = Date.now()) {
  if (code.startsWith("agentnet-link-v2:")) {
    let o, host = "";
    try { o = decodeOffer(code); host = new URL(decodeInvite(o.invite).hub).host; } catch (e) {
      return { problem: "That device link is not complete. Make a new one on your other device (Your devices, Add a device)." };
    }
    const expired = now / 1000 >= o.expires;
    return { kind: "link", host, expires: o.expires * 1000, problem: expired ? "That device link expired. Make a new one on your other device (Your devices, Add a device)." : "" };
  }
  let inv;
  try { inv = decodeInvite(code); } catch (e) { return { problem: "That is not a complete invitation. Copy the whole link you were sent, or ask for a new one." }; }
  return { kind: "invite", host: new URL(inv.hub).host, name: inv.name, from: inv.from, workspace: inv.workspace, cert: !!inv.cert, problem: "" };
}

// landing shows the not-joined page. ui (device.mjs): el, show, and
// join(code, device, name) / link(code, device), which join this browser
// (phones only) and start the messenger.
export async function landing(ui, link) {
  const p = platform();
  const cfg = ui.googleConfig ? await ui.googleConfig().catch(() => ({})) : {};
  if (!link?.code && !link?.damaged && (cfg.web_client_id || cfg.desktop_client_id)) return googleLanding(ui, p, cfg);
  const code = link && !link.damaged ? link.code : "";
  const problem = link && link.damaged ? "This link could not be opened. Paste the invitation below, or ask the sender for a new link." : "";
  if (isPhone(p)) return phone(ui, p, code, problem);
  return computer(ui, p, code, problem);
}

function serverLine(ui, info) {
  return ui.el("p", { class: "join-recovery" }, "Server: ", ui.el("strong", {}, info.host),
    info.workspace ? " · the invitation calls it “" + info.workspace + "”" : "");
}

function pasteBox(ui, onCode, label) {
  const box = ui.el("textarea", { id: "landing-code", rows: "3", autocomplete: "off", spellcheck: "false", placeholder: "Paste the invitation link here" });
  const error = ui.el("p", { class: "error", role: "alert" });
  const form = ui.el("form", { class: "join-form", novalidate: true }, ui.el("label", { for: "landing-code" }, label), box, error,
    ui.el("button", { type: "submit", class: "btn primary join-go" }, "Continue"));
  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    const c = codeIn(box.value);
    if (!c) { error.textContent = "That is not an AgentNet invitation. Copy the whole link you were sent, or ask for a new one."; return; }
    onCode(c);
  });
  return form;
}

function copyButton(ui, code) {
  const status = ui.el("span", { class: "hint", role: "status" });
  const b = ui.el("button", { type: "button", class: "btn" }, "Copy invitation");
  b.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(location.origin + "/#" + code); status.textContent = " Copied."; } catch (e) { status.textContent = " Copying is blocked here."; }
  });
  return ui.el("span", { class: "landing-copy" }, b, status);
}

// ---- computers: the app's installer, then the invitation opened in it

async function downloadsBlock(ui, p) {
  let list;
  try { list = await getAppList(); } catch (e) {
    return ui.el("p", { class: "error" }, "The download list could not be loaded. Get AgentNet from github.com/misunders2d/agentnet/releases.");
  }
  const mine = forPlatform(list, p);
  const others = list.filter((d) => d !== mine);
  return ui.el("div", { class: "landing-downloads" },
    mine && ui.el("a", { class: "btn primary", href: mine.url, rel: "noopener" }, "Get AgentNet for " + (systemWords[p] || mine.label)),
    ui.el("details", { class: "join-more", open: !mine }, ui.el("summary", {}, mine ? "Other systems" : "Choose your system"),
      ui.el("ul", { class: "landing-list" }, others.map((d) => ui.el("li", {}, ui.el("a", { href: d.url, rel: "noopener" }, d.label))))));
}

function openButton(ui, code, words) {
  const b = ui.el("button", { type: "button", class: "btn primary" }, words);
  b.addEventListener("click", () => { location.href = appLink(code); }); // the code stays in memory, never in the page
  return b;
}

async function computer(ui, p, code, problem) {
  ui.onCode = (c) => computer(ui, p, c, "");
  if (!code) {
    ui.show("Get AgentNet",
      ui.el("p", { class: "join-intro" }, "Chat with people and their agents. AgentNet is an app for this computer: it also runs your coding agents."),
      problem && ui.el("p", { class: "join-recovery" }, problem),
      await downloadsBlock(ui, p),
      ui.el("h2", { class: "landing-h" }, "Have an invitation?"),
      ui.el("p", {}, "Open the link you were sent, or paste it here."),
      pasteBox(ui, (c) => computer(ui, p, c, ""), "Your invitation"));
    return;
  }
  const info = read(code);
  if (info.problem) { await computer(ui, p, "", info.problem); return; }
  if (info.kind === "link") {
    const until = new Date(info.expires).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    ui.show("Get AgentNet for this computer",
      serverLine(ui, info),
      ui.el("p", {}, "This link comes from one of your devices: this computer joins as one more device of yours, with your chats, once you approve it there."),
      ui.el("ol", { class: "landing-steps" },
        ui.el("li", {}, ui.el("strong", { class: "landing-step-title" }, "Install AgentNet. "), await downloadsBlock(ui, p)),
        ui.el("li", {}, ui.el("strong", { class: "landing-step-title" }, "Open this link in AgentNet. "), openButton(ui, code, "Open in AgentNet"))),
      ui.el("p", { class: "hint", id: "landing-expiry" }, "The link works until " + until + ". If it runs out while you install, make a new one on your other device."));
    return;
  }
  ui.show(info.from ? info.from + " invited you" : "You're invited to AgentNet",
    serverLine(ui, info),
    info.from && ui.el("p", { class: "hint" }, "Who invited you and the names are what the invitation says; the server is where it leads."),
    ui.el("ol", { class: "landing-steps" },
      ui.el("li", {}, ui.el("strong", { class: "landing-step-title" }, "Get the app. "), await downloadsBlock(ui, p)),
      ui.el("li", {}, ui.el("strong", { class: "landing-step-title" }, "Open your invitation in AgentNet. "), openButton(ui, code, "Open in AgentNet"),
        ui.el("p", { class: "hint" }, "If the app opens without it, copy the invitation and paste it there."), copyButton(ui, code))));
}

// ---- phones: this page on the home screen

function phone(ui, p, code, problem) {
  ui.onCode = (c) => phone(ui, p, c, "");
  const device = phoneNames[p];
  const ios = p === "iphone" || p === "ipad";
  if (!code) {
    const steps = ios && !standalone()
      ? ui.el("p", {}, "Tap Share, then Add to Home Screen. Then open AgentNet from your home screen and paste your invitation there.")
      : ui.el("p", {}, "Open the invitation link you were sent, or paste it here.");
    ui.show("Get AgentNet", ui.el("p", { class: "join-intro" }, "Chat with people and their agents."),
      problem && ui.el("p", { class: "join-recovery" }, problem), steps,
      (!ios || standalone()) && pasteBox(ui, (c) => phone(ui, p, c, ""), "Your invitation"));
    return;
  }
  const info = read(code);
  if (info.problem || info.cert) { phone(ui, p, "", info.problem || "This invitation is for the AgentNet program on a computer. Ask the sender for an invitation link."); return; }
  if (ios && !standalone()) { // an iPhone keeps the home-screen app's data apart from Safari's: join there
    ui.show(info.from ? info.from + " invited you" : "You're invited to AgentNet", serverLine(ui, info),
      ui.el("ol", { class: "landing-steps" },
        ui.el("li", {}, "Tap Share, then ", ui.el("strong", {}, "Add to Home Screen"), "."),
        ui.el("li", {}, "Copy your invitation: ", copyButton(ui, code)),
        ui.el("li", {}, "Open AgentNet from your home screen and paste it there.")));
    return;
  }
  if (info.kind === "link") {
    const error = ui.el("p", { class: "error", role: "alert" });
    const go = ui.el("button", { type: "button", class: "btn primary join-go" }, "Add this " + (systemWords[p] || "phone"));
    go.addEventListener("click", async () => {
      if (go.disabled) return;
      go.disabled = true; error.textContent = "";
      try { await ui.link(code, device); } catch (e) { error.textContent = e.message; go.disabled = false; }
    });
    ui.show("Add this phone to you", serverLine(ui, info),
      ui.el("p", {}, "This link comes from one of your devices. Approve this phone there with one tap: it becomes one more device of yours, with your chats."),
      error, go, ui.notes ? ui.notes() : []);
    return;
  }
  const name = ui.el("input", { id: "landing-name", type: "text", autocomplete: "name", maxlength: "64", value: info.name || "" });
  const error = ui.el("p", { class: "error", role: "alert" });
  const go = ui.el("button", { type: "submit", class: "btn primary join-go" }, "Join");
  const form = ui.el("form", { class: "join-form", novalidate: true },
    ui.el("label", { for: "landing-name" }, "Your name"), name, ui.el("p", { class: "hint" }, "The name people see. You can change it later."), error, go);
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (go.disabled) return;
    const chosen = name.value.trim();
    if (!chosen) { error.textContent = "Write the name people will see."; return; }
    go.disabled = true; error.textContent = "";
    try { await ui.join(code, device, chosen); } catch (e) { error.textContent = e.message; go.disabled = false; }
  });
  ui.show(info.from ? info.from + " invited you" : "You're invited to AgentNet", serverLine(ui, info),
    info.from && ui.el("p", { class: "hint" }, "Who invited you and the names are what the invitation says; the server is where it leads."),
    form, ui.notes ? ui.notes() : []);
  name.focus();
}

function dismiss(ui, close) {
  return ui.el("button", { type: "button", class: "icon-btn", "aria-label": "Dismiss", onclick: close }, "×");
}

// installOffer is Android's "Add to Home screen", offered once the phone
// joined (its browser's own prompt), or null.
export function installOffer(ui) {
  if (!installPrompt || standalone()) return null;
  const b = ui.el("button", { type: "button", class: "btn primary" }, "Add AgentNet to your home screen");
  const bar = ui.el("div", { class: "app-banner", role: "status" }, b, dismiss(ui, () => bar.remove()));
  b.addEventListener("click", async () => { const p = installPrompt; installPrompt = null; bar.remove(); await p.prompt(); });
  return bar;
}

// appBanner is the note an enrolled browser device on a computer shows
// above the messenger: on a computer, AgentNet is the app.
export async function appBanner(ui) {
  const p = platform();
  const key = "agentnet.app-banner.dismissed";
  if (isPhone(p)) return null;
  try { if (localStorage.getItem(key)) return null; } catch (e) { /* shown */ }
  let mine = null;
  try { mine = forPlatform(await getAppList(), p); } catch (e) { mine = null; }
  const bar = ui.el("div", { class: "app-banner", role: "note" },
    ui.el("span", { class: "app-banner-text" }, "AgentNet works best as the app on this computer: it also runs your coding agents."),
    ui.el("a", { class: "btn primary", href: mine ? mine.url : "https://github.com/misunders2d/agentnet/releases/latest", rel: "noopener" }, "Get the app"),
    ui.el("button", { type: "button", class: "btn", onclick: () => { location.href = appLink(""); } }, "Open the app"),
    dismiss(ui, () => { bar.remove(); try { localStorage.setItem(key, "1"); } catch (e) { /* this time only */ } }));
  return bar;
}

async function googleLanding(ui, p, cfg) {
  const root = ui.el("div", { class: "join-form" });
  const origin = location.origin;
  if (!isPhone(p)) {
    ui.show("Get AgentNet", ui.el("p", { class: "join-intro" }, "Your Google email is you. Get the app, then sign in."),
      await downloadsBlock(ui, p),
      ui.el("a", { class: "btn primary", href: "agentnet://open#google-signin=" + encodeURIComponent(origin) }, "Sign in with Google in AgentNet"));
    return;
  }
  if ((p === "iphone" || p === "ipad") && !standalone()) {
    ui.show("Get AgentNet", ui.el("p", {}, "Add AgentNet to your home screen first. Then open it and sign in with Google."),
      ui.el("p", {}, "Tap Share, then Add to Home Screen.")); return;
  }
  ui.show("Welcome to AgentNet", ui.el("p", { class: "join-intro" }, "Sign in with your invited Google email."),
    ui.el("p", { class: "hint" }, "Every later device also needs your OK on an existing device."), root, ...ui.notes());
  if (!cfg.web_client_id) { root.textContent = "Your workspace admin must enable Google browser sign-in."; return; }
  try { await mountGoogleSignIn(root, cfg.web_client_id, await ui.googleNonce(), token => ui.googleJoin(token, phoneNames[p] || "browser")); }
  catch (e) { root.replaceChildren(ui.el("p", { class: "error", role: "alert" }, e.message)); }
}
