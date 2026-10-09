import { reactionChoices, recordReaction, humanReactionOrder } from "./reaction-preferences.mjs";
import {collectiveOptions} from './collective-mentions.mjs';
import { rawLinkParts } from "./link-text.mjs";
function niceGoogleDevice(address) { const name = String(address || "").split("/").pop() || "device"; return name.charAt(0).toUpperCase() + name.slice(1); }
import { avatarPicture, openPictureEditor, pastePictures } from "./pictures.mjs";
import { topicControls } from './topics.mjs';
import {personRoots,personTopicEntries,mainPreferences} from './person-topics.mjs';
import { pendingSends, sendID, groupedSends } from "./optimistic.mjs";
import { markup } from './template.mjs';
import manifest from './manifest.mjs';
const mounted = new WeakMap();
// ---- people words (MEL-529, MEL-525) -------------------------------------
// Pure helpers over the host's overview: a device is an agent only when it
// says it runs one (overview.agent_devices; me.agent for this one); people
// see a person and a device, never an address (the address stays in the
// title, as a verified detail); a name shared with another person, or only
// listed by the server, shows its key's first group. The same rules as
// Comic's model.ts and the vectors in internal/ui/testdata/device_words.json.
const deviceSpecial = { iphone: "iPhone", ipad: "iPad", imac: "iMac", mac: "Mac", macbook: "MacBook" };
function deviceWords(address) {
  const s = String(address || ""), i = s.indexOf("/");
  return (i < 0 ? s : s.slice(i + 1)).split("-").filter(Boolean)
    .map((w, j) => deviceSpecial[w.toLowerCase()] || (j === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(" ");
}
function runsAgentIn(o, address) {
  if (!o || !address) return false;
  if (address === o.me.address) return !!o.me.agent;
  return (o.agent_devices || []).includes(address);
}
const shortKeyOf = (fp) => String(fp || "").replace(/^SHA256:/i, "").trim().split(/[-\s]/)[0] || "";
// whoParts: { name, device, key } for a device address ("You", "Pixel", "").
function whoParts(o, address) {
  const device = deviceWords(address);
  if (!o) return { name: address, device: "", key: "" };
  if (address === o.me.address) return { name: "You", device, key: "" };
  const holds = (p) => !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));
  const p = holds(o.person) ? o.person : (o.people || []).find(holds);
  if (!p) return { name: device || address, device: "", key: "" };
  if (o.person && (p === o.person || (p.person && p.person === o.person.person))) return { name: "You", device, key: "" };
  const label = String(p.label || "").trim().toLowerCase(), same = (x) => (x.person && x.person === p.person) || x.address === p.address;
  const clash = !!label && [o.person, ...(o.people || [])].some((x) => x && !same(x) && String(x.label || "").trim().toLowerCase() === label);
  const fp = ((p.devices || []).find((d) => d.address === address) || {}).fingerprint || (p.address === address ? p.fingerprint : "") || "";
  return { name: p.label || device, device, key: clash || p.state === "listed" ? shortKeyOf(fp) : "" };
}
// whoTextIn: "Vitalii · Phone", "You · Pixel", "Sergey · 19c77bce · Box".
function whoTextIn(o, address) {
  const w = whoParts(o, address);
  return [w.name, w.key, w.device].filter(Boolean).join(" · ");
}
// devicesText: a person's devices in words, "on Desk, Phone", never addresses.
function devicesText(p) {
  return "on " + (p.devices && p.devices.length ? p.devices : [{ address: p.address }]).map((d) => deviceWords(d.address)).join(", ");
}
// whoMatches: search finds a device by its address or by who it is in words
// ("sergey" finds admin/pixel, shown as "Sergey · Pixel"); q is lower case.
function whoMatches(o, address, q) {
  return String(address || "").toLowerCase().includes(q) || whoTextIn(o, address).toLowerCase().includes(q);
}
// ---- end of people words
export async function mount(root, host) {
  if (host.version !== 1) throw new Error('Zoom requires AgentNet host API v1');
  await unmount(root);
  const doc = new DOMParser().parseFromString(markup, 'text/html');
  root.replaceChildren(...doc.body.childNodes);
  root.classList.add('zoom-root');
  root.lang = navigator.language || 'en';
  mounted.set(root, createZoom(root, host));
}
export async function unmount(root) { const stop = mounted.get(root); if (stop) { mounted.delete(root); stop(); } }
function createZoom(root, host) {
let currentHost = host, alive = true;
const abort = new AbortController(), cleanups = [], timers = new Set();
const on = (target, kind, handler) => target.addEventListener(kind, handler, {signal: abort.signal});
const setTimeout = (fn, ms) => { const timer = globalThis.setTimeout(() => { timers.delete(timer); if (alive) fn(); }, ms); timers.add(timer); return timer; };
// AgentNet messenger page. Text from anyone is inserted as text nodes only.
// Every change goes through the server's actions, which use the same client
// operations and rules as the command line; the page decides nothing itself.
"use strict";

const $ = (id) => root.querySelector("#" + CSS.escape(id));
const topicSelections={},topicFresh={};
const choosePersonMain=mainPreferences(currentHost.workspace.id,()=>root.ownerDocument.defaultView.localStorage);
function flatTopicScope(t) {
 const roots=personRoots(state.overview,t),main=choosePersonMain(roots,t?.peer?.person);
 return main?{roots,main,view:t}:null;
}
async function choosePersonTopic(conv,topic) {
 state.messageOpening=null;
 topicSelections[conv]=topic||'';topicFresh[conv]=false;
 if(conv===state.dm){setDMReply(null);renderDMBody(false);markDMRead();}else await openDM(conv);
}
function openPersonMain(p,dms) {
 const main=choosePersonMain(dms,p.person);
 if(main){void choosePersonTopic(main,'');return true;}return false;
}
const sends = pendingSends(host, () => { if (alive) Zoom.refresh(); });
const state = { thread: null, data: null, seq: -1, answering: null, lastSeen: {}, presence: {},
  drafts: {}, draftKey: null, replyReceiver: null, replyReceiverHost: null, receiverCatalog: null, receiverCatalogSeq: 0, receiverBindings: [], sending: false, hub: null, hubUp: null, query: "", singlesOpen: {}, directoryOpen: false,
  dm: null, dmData: null, dmReply: null, dmAgent: null, seenReported: {}, pendingOpen: null, clickedAtStart: null,
  files: [], opened: [], contactView: "recent", contactLimit: 20,
  version: "", updating: false, newVersion: "", dialogRestore: null, dialogBusy: false, gen: 0, switching: null, capturedFor: null };


// A send may finish after this root was unmounted. Workspace renderer state
// holds its activity; completion updates only the currently mounted view.
const skinKey = (host.skin?.id || manifest.id).replace(/^local:/, "");
const pendingKey = skinKey + "Pending";
const ownSkin = (id) => currentHost.skin ? id === currentHost.skin.id : id === manifest.id || id === "local:" + manifest.id;
const workspaceState = currentHost.workspaces?.state?.(currentHost.workspace.id);
const pending = workspaceState ? (workspaceState[pendingKey] ??= {busy: false}) : {busy: false};
const pendingChanged = () => { if (alive) { syncComposer(); if (state.overview) Zoom.refresh(); } };
const pendingAccepted = ({key, text, files, reply, answering}) => {
  if (!alive || state.draftKey !== key) return;
  if ($('body').value === text) { $('body').value = ''; grow(); state.typedFor = null; state.mentions = []; state.mentionText = ''; }
  if (reply && state.dmReply?.id === reply.id) setDMReply(null);
  if (answering && state.answering?.id === answering.id) setAnswering(null);
  dropFiles(files); keepDraft();
};
pending.changed = pendingChanged; pending.accepted = pendingAccepted;
Object.defineProperty(state, 'sending', {get: () => pending.busy, set: value => {pending.busy = value; pending.changed?.();}});

const modules = { typing: () => import('./typing.mjs'), drivespace: () => import('./drivespace.mjs'),
  'drivespace-setup': () => import('./drivespace-setup.mjs'), 'assistant-setup': () => import('./assistant-setup.mjs') };
const moduleOf = (name) => modules[name]();

// present flattens children and drops the ones a condition left out (false,
// null, undefined, ""), so they never reach the page as text.
const present = (list) => list.flat(Infinity).filter((k) => k !== undefined && k !== null && k !== false && k !== "");
const node = (k) => typeof k === "string" ? document.createTextNode(k) : k;

// fill replaces an element's children; use it instead of replaceChildren.
function fill(parent, ...kids) {
  parent.replaceChildren(...present(kids).map(node));
  return parent;
}

// el builds an element; string children become text nodes.
function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of present(kids)) e.append(node(k));
  return e;
}

// Every operation keeps the host it started with.
async function api(path, body, host = currentHost) { return host.api(path, body); }
const topicUI=topicControls(root,{api,chooseRoot:choosePersonTopic,announce:text=>announce(text),choose:id=>{state.messageOpening=null;if(state.dm){topicSelections[state.dm]=id;topicFresh[state.dm]=false;setDMReply(null);renderDMBody(false);markDMRead();if(Zoom.level===3)void Zoom.go(2,{});}else if(id!==state.thread)void openThread(id);},fresh:()=>{state.messageOpening=null;const id=state.dm||state.thread;topicFresh[id]=true;if(state.dm){topicSelections[id]='';renderDMBody(false);markDMRead();}announce('New topic: your next message starts a separate flow.');if(state.dm)dmWriteDialog(state.dmData);else writeDialog(state.data,null);},changed:async()=>{await loadOverview();state.dm?await loadDM(false):await loadThread(false);}});
cleanups.push(()=>topicUI.stop());

// A time is an RFC 3339 string, or unix seconds (a host's report and its
// answers, client.Report, count in seconds).
const asDate = (s) => new Date(typeof s === "number" && s < 1e11 ? s * 1000 : s);
const time = (s) => asDate(s).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
const when = (s) => {
  const d = asDate(s);
  return d.toDateString() === new Date().toDateString() ? time(s)
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
};
const size = (n) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : (n / (1 << 20)).toFixed(1) + " MB";
// A person mention in text: [@Name](agentnet:person/ID) or agentnet:guest/PID
// (see mentionWho). mentionPlain reads each as @Name.
const mentionRef = /\[@([^\[\]\r\n]{1,80})\]\(agentnet:(person|guest|agent)\/([A-Za-z0-9_-]{1,64})\)/g;
const mentionName = (s) => String(s || "").replace(/[\[\]\r\n]/g, "").trim().slice(0, 80);
const mentionPlain = (s) => typeof s === "string" ? s.replace(mentionRef, (_, name) => "@" + name) : s;
const firstLine = (s, n) => {
  const l = mentionPlain(s || "").split("\n")[0]; // a person mention reads as @Name
  return l.length > n ? l.slice(0, n - 1).trimEnd() + "…" : l;
};
const announce = (t) => { if (alive) $("live").textContent = t; };
const kindTag = { question: "Question", task: "Task" };
const statusWord = { declined: "Declined", failed: "Failed", timeout: "Timed out", cancelled: "Cancelled", interrupted: "Interrupted",
  review_notice: "Report", proposal: "Proposed task (not run)" };

// Addresses are person/agent: the person leads, the agent is secondary.
// A device as people see it: the person (or "You"), then the device; the
// address is its title, as a verified detail.
const runsAgent = (addr) => runsAgentIn(state.overview, addr);
const whoText = (addr) => whoTextIn(state.overview, addr);
function who(addr) {
  const w = whoParts(state.overview, addr);
  return el("span", { class: "who", title: addr }, w.name, (w.key || w.device) && el("span", { class: "who-agent" }, " · " + [w.key, w.device].filter(Boolean).join(" · ")));
}

function avatar(addr, cls) {
  const picture = avatarPicture(state.overview, addr);

  let h = 0;
  for (const c of addr.split("/")[0]) h = (h * 41 + c.charCodeAt(0)) >>> 0;
  return el("span", { class: "avatar av" + (h % 6) + (cls ? " " + cls : "") + (picture ? " profile-picture" : ""), "aria-hidden": "true" },
    addr.charAt(0).toUpperCase(), picture && el("img", { src: picture, alt: "", onerror: e => e.currentTarget.remove() }));
}

// ---- overview ---------------------------------------------------------------

async function loadOverview() {
  if (!alive) throw new Error("Zoom was unmounted");
  const gen = state.gen;
  const o = await api("/api/overview");
  if (gen !== state.gen) return o; // answered for a workspace no longer shown: nothing of it is drawn
  state.overview = o;
  for (const id of ["profile-btn", "new-btn", "conversation-details"]) $(id).disabled = false;
  if (!state.version) state.version = o.version;
  else if (o.version && o.version !== state.version) updated(o.version);
  $("demo").hidden = !o.demo;
  $("me").textContent = o.person ? o.person.label : "This device";
  $("me").title = "Key " + o.me.fingerprint;
  const m = machineLines(o);
  $("machine").textContent = m.summary;
  fill($("machine-detail"), ...m.details.filter(Boolean).map((t) => el("p", {}, t)));
  $("new-btn").hidden = !!(o.link && o.link.state === "pending"); // a device waiting for approval sends nothing
  renderOffline(o);
  $("release").hidden = !o.release;
  fill($("release"), o.release && ["Update available · " + o.release + ". ", el("a", {href: "https://github.com/misunders2d/agentnet/releases", target: "_blank", rel: "noopener noreferrer"}, "Release notes")]);
  $("profile-btn").classList.toggle("update-available", !!o.release);
  $("profile-btn").title = o.release ? "Your profile and settings · Update available" : "Your profile and settings";
  renderNotify(o.notify);
  renderProfile(o);
  renderReview(o.review);
  renderThreads(o.threads);
  renderQuarantine(o.quarantine);
  if (state.dm || state.data) setHubBack();
  else if (state.hub && !$("hub").hidden) renderHub();
  if (state.pendingOpen) retryOpen();
  if (o.reply_receivers) loadReceiverBindings();
  if (state.dm || state.data) renderReplyReceiver();
  return o;
}

// ---- files (MEL-489) ----------------------------------------------------------------------------
//
// A person adds files by choosing them, pasting an image or dropping them on
// the composer. They wait in that conversation's draft, with their name and
// size (a preview for a raster image), until Send: nothing leaves before.
// They are encrypted to the recipient before they leave this device (the
// browser device) or this computer's AgentNet (the daemon's page); the
// server only ever holds ciphertext. A file received is fetched, checked
// against what its sender signed and decrypted only when opened; only
// PNG, JPEG, GIF and WebP, by their bytes, are shown as pictures.

const rasterTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const fileLimits = () => (state.overview && state.overview.files) || null; // {max_file, max_message, max_count}
const dmVisitor = (d = state.dmData) => !!d && d.role === "visitor";
const dmHumanGuest = (d = state.dmData) => d?.role === "human_guest";
const guestAuthor = (d = state.dmData) => { const active = (d?.guests || []).filter(g => g.host_here && g.can_send); return active.length === 1 ? active[0] : null; };
// A guest's audience: both verified original people (members; older views:
// peer and inviters) and every other accepted guest it knows.
const guestOriginals = (d = state.dmData) => d?.members?.length ? d.members : [d?.peer, ...(d?.guests || []).map(g => g.inviter)];
const guestAudience = (d = state.dmData) => [...new Map([...guestOriginals(d), ...(d?.guests || []).filter(g => g.state === "active" && !g.host_here).map(g => g.host)].filter(p => p && p.person !== state.overview?.person?.person).map(p => [p.person, p])).values()];
const guestAudienceNames = (d = state.dmData) => guestAudience(d).map(p => p.label || p.address);
const joinedNames = names => names.length > 1 ? names.slice(0, -1).join(", ") + " and " + names.at(-1) : names[0] || "conversation participants";
const humanGroup = (d = state.dmData) => d?.kind === "group";
let fileSeq = 0;

// filesAllowed: a DM open and sendable to its person or exact active agent,
// or a device conversation open, sendable, not answering (an
// answer goes without files).
const filesAllowed = () => !!(fileLimits() && (state.dm ? state.dmData && !state.dmData.frozen &&
  (!dmVisitor() || !!state.dmAgent) && (!dmHumanGuest() || !!guestAuthor()) && !askGone()
  : state.thread && state.data && !state.data.key.pending && !state.answering));
// noFilesWhy says why the open conversation takes no files now.
const noFilesWhy = () => (state.answering ? "An answer goes without files: send them in a message of their own."
  : dmVisitor() ? "This is invited agent context; you are not a member of this DM." : "Files cannot be sent here.");

const fileExt = (name) => ((name.includes(".") ? name.split(".").pop() : "") || "FILE").slice(0, 4).toUpperCase();
const stamp = () => new Date().toISOString().replace(/[-:]/g, "").replace("T", "-").slice(0, 15);

// sniffImage is wire.sniffImage: a raster image type from the bytes only.
function sniffImage(b) {
  const at = (i, s) => [...s].every((c, j) => b[i + j] === c.charCodeAt(0));
  if (b.length >= 8 && b[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (at(0, "GIF87a") || at(0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return "";
}

// overLimit says why these files cannot go in one message ("" if they can).
function overLimit(files) {
  if (files.some((f) => f.reattachRequired)) return "AgentNet restarted. Remove and reattach the marked files before sending; your draft stays here.";
  const lim = fileLimits();
  if (!lim) return "Files cannot be sent here.";
  if (files.length > lim.max_count) return "A message takes at most " + lim.max_count + " files.";
  const big = files.find((f) => f.size > lim.max_file);
  if (big) return big.name + " is larger than " + size(lim.max_file) + (state.overview.device ? ", this browser's limit: send it from a computer with AgentNet." : ".");
  if (lim.max_message && files.reduce((n, f) => n + f.size, 0) > lim.max_message) {
    return "Together these files are more than " + size(lim.max_message) + ", this browser's limit for one message: send fewer at a time.";
  }
  return "";
}

// pushFiles puts chosen, pasted or dropped files in the open DM's draft (a
// pasted picture gets a name of its own); addFiles does it from the composer.
function pushFiles(list, pasted) {
  for (const f of list) {
    if (!f.size) continue; // a folder or an empty file
    const name = pasted && (!f.name || /^image\.(png|jpe?g|gif|webp)$/i.test(f.name))
      ? "pasted-image-" + stamp() + "." + ((f.type.split("/")[1] || "png").replace("jpeg", "jpg")) : f.name || "file";
    state.files.push({ key: ++fileSeq, file: f, name, size: f.size, url: rasterTypes.includes(f.type) ? URL.createObjectURL(f) : "" });
  }
}

function addFiles(list, pasted) {
  if (!filesAllowed()) { $("compose-error").textContent = noFilesWhy(); return; }
  pushFiles(list, pasted);
  $("compose-error").textContent = overLimit(state.files);
  renderPending();
  syncComposer();
}

// Capture the open draft before the native clipboard read can yield.
function pastedFiles(e, add) {
  const field=e.target, key=state.draftKey, host=currentHost, gen=state.gen;
  const here=()=>alive && field.isConnected && key===state.draftKey && host===currentHost && gen===state.gen;
  void pastePictures(e,files=>{if(here())add(files);},{
    clipboardImage:host.clipboardImage?()=>host.clipboardImage():undefined,
    insertText:text=>{if(here()){field.setRangeText(text,field.selectionStart,field.selectionEnd,"end");field.dispatchEvent(new Event("input",{bubbles:true}));}},
    error:text=>{if(here())$("compose-error").textContent=text;}
  });
}

function removeFile(key) {
  const f = state.files.find((x) => x.key === key);
  if (f && f.url) URL.revokeObjectURL(f.url);
  if (f && f.staged) discardStaged([f]);
  state.files = state.files.filter((x) => x.key !== key);
  $("compose-error").textContent = state.files.length ? overLimit(state.files) : "";
  renderPending();
}

// dropFiles removes the files a send took (others added meanwhile stay).
function dropFiles(sent) {
  for (const f of sent) if (f.url) URL.revokeObjectURL(f.url);
  state.files = state.files.filter((x) => !sent.includes(x));
  renderPending();
}

// pendingChips shows the draft's files in ul, each with its remove button.
function pendingChips(ul, removed) {
  ul.hidden = !state.files.length;
  fill(ul, ...state.files.map((f) => el("li", { class: "attach-chip" },
    f.url ? el("img", { src: f.url, alt: "", class: "attach-thumb" }) : el("span", { class: "file-icon", "aria-hidden": "true" }, fileExt(f.name)),
    el("span", { class: "file-text" }, el("span", { class: "file-name" }, f.name), el("span", { class: "file-size" }, size(f.size) + (f.reattachRequired ? " · Reattach after restart" : ""))),
    el("button", { type: "button", class: "icon-btn remove-file", "aria-label": "Remove " + f.name, title: "Remove",
      onclick: () => { removeFile(f.key); if (removed) removed(); } }, "×"))));
}

function renderPending() { pendingChips($("attach-list")); }

// preparedFiles hands the files to whatever sends them: the browser
// device's engine encrypts them itself; this computer's page first gives
// the bytes to its own AgentNet (never to the server), one at a time. Each
// file keeps the id it was handed over under (f.staged) until a send names
// it or it is removed, so a retry after a failure hands over only the rest.
async function preparedFiles(files, host) {
  host = host || currentHost;
  if (!files.length) return [];
  if (files.some((f) => f.reattachRequired)) throw new Error(overLimit(files));
  for (const [i, f] of files.entries()) {
    if (f.staged) continue;
    if (alive) $("compose-hint").textContent = "Handing " + (i + 1) + " of " + files.length + " files to AgentNet on this computer…";
    f.staged = await host.stage(f.file);
  }
  return files.map((f) => f.staged);
}

// sentStaged: a send took the files it named, sent or refused; if it is
// tried again, they are handed over again.
function sentStaged(files) { for (const f of files) f.staged = ""; }

// discardStaged tells this computer's AgentNet to drop files handed over
// and no longer sent (removed from a draft). If that fails, it drops them
// itself within the hour, or when it starts again.
function discardStaged(files) {
  const ids = files.map((f) => f.staged).filter(Boolean);
  sentStaged(files);
  if (ids.length) api("/api/upload/discard", { ids }).catch(() => {});
}

// fileChips are a message's files: name and size, and Open for one that
// came to this device (received, from another device of yours, or as
// history once it is here). A history file not here yet is asked for from
// the device it came from.
function fileChips(m, files) {
  if (!files || !files.length) return null;
  const sentHere = m.dir === "out" && !m.via && !m.synced_from;
  const from = m.synced_from ? m.synced_from.split("/")[1] : "";
  return el("div", { class: "files" }, files.map((f, i) => {
    const idx = f.index === undefined ? i : f.index;
    const slot = el("span", { class: "file-open" });
    const open = el("button", { type: "button", class: "text-btn", "data-file-open": m.id + ":" + idx, onclick: (e) => openFile(m.id, idx, f.name, e.currentTarget, slot, m.dir) }, "Open");
    // A file sent from here opens only when the backend says a kept copy
    // is openable; without that word it is with the recipient only. A
    // received one opens unless the backend says it is not openable.
    const action = sentHere ? (f.openable === true ? open : el("span", { class: "file-state" }, f.openable === false ? "Not kept on this device" : "Sent"))
      : f.availability === "requestable" ? el("button", { type: "button", class: "text-btn", onclick: (e) => requestFile(m, idx, from, e.currentTarget) }, "Get it from " + from)
        : f.availability === "requested" ? el("span", { class: "file-state" }, "Asked " + from + " for it")
          : f.availability === "unavailable" || f.openable === false ? el("span", { class: "file-state" }, "Not available")
            : open;
    const toDrive = state.dm && state.dmData && state.dmData.peer.person && (sentHere ? f.openable === true : f.availability !== "unavailable" && f.openable !== false)
      && el("button", { type: "button", class: "text-btn file-drive", title: "Copy this file to the DM's Google Drive space (outside encryption)", onclick: () => saveToDrive(m, idx, f.name) }, "To project space…");
    return el("span", { class: "file" }, el("span", { class: "file-icon", "aria-hidden": "true" }, fileExt(f.name)),
      el("span", { class: "file-text" }, el("span", { class: "file-name" }, f.name),
        el("span", { class: "file-size" }, f.saved ? "Saved: " + f.saved : size(f.size)),
        f.note && el("span", { class: "file-note" }, f.note)),
      action, toDrive, slot);
  }));
}

// requestFile asks the device of yours a history message came from for
// one of its files; it opens once that device sends it.
async function requestFile(m, i, from, button) {
  button.disabled = true;
  const host = currentHost, gen = state.gen;
  try {
    await api("/api/file/request", { id: m.id, index: i }, host);
    announce("Asked " + from + " for it: it opens here once that device sends it (it has to be online).");
    if (gen !== state.gen) return;
    await loadDM();
  } catch (e) {
    button.disabled = false;
    announce(e.message);
  }
}

// fetchFile gets a file checked and decrypted: by the browser device
// itself, or by this computer's AgentNet. dir is the message's own
// direction: a received id is the sender's choice and can equal a sent
// one here, so only dir makes the reference exact.
async function fetchFile(id, i, dir, host = currentHost) {
  const f = await host.file(id, i, dir);
  return { bytes: f.bytes, image: f.image || sniffImage(f.bytes) };
}

// openFile shows a received picture in place, or saves any other file
// under its safe name; its object URL lives until the conversation changes.
async function openFile(id, i, name, button, slot, dir) {
  button.disabled = true;
  button.setAttribute("aria-busy", "true");
  announce("Opening " + name + "…");
  const host = currentHost, gen = state.gen; // the membership this file is in
  try {
    const { bytes, image } = await fetchFile(id, i, dir, host);
    if (gen !== state.gen) return; // that workspace is no longer shown
    const url = URL.createObjectURL(new Blob([bytes], { type: image || "application/octet-stream" }));
    state.opened.push(url);
    if (image) {
      const picture = el("img", { src: url, alt: name, class: "file-preview" });
      await picture.decode();
      if (!alive || gen !== state.gen) return;
      const viewer = el("dialog", { class: "file-viewer", "aria-label": name },
        picture,
        el("div", { class: "file-viewer-actions" },
          el("a", { href: url, download: name, class: "text-btn" }, "Save " + name),
          state.overview?.person && el("button", { type: "button", class: "btn", onclick: () => editProfilePicture("", url) }, "Use as my picture"),
          el("button", { type: "button", class: "btn", onclick: () => viewer.close() }, "Close preview")));
      root.append(viewer);
      viewer.addEventListener("close", () => {
        viewer.remove();
        const opener = button.isConnected ? button : [...root.querySelectorAll("[data-file-open]")].find(b => b.dataset.fileOpen === id + ":" + i);
        if (alive && opener) opener.focus({preventScroll: true});
        else if (alive) focusZoomLayer($("zoom"));
      }, {once: true});
      viewer.showModal();
    } else {
      // Not a picture: a link the person clicks to save it, where their
      // browser puts downloads. Nothing is claimed about where it went.
      const a = el("a", { href: url, download: name, class: "text-btn file-download" }, "Download " + name);
      fill(slot, a);
      button.hidden = true;
      a.focus();
    }
  } catch (e) {
    button.textContent = "Open";
    announce("Could not open " + name + ": " + e.message);
  } finally {
    button.removeAttribute("aria-busy");
    button.disabled = false;
  }
}

// releaseOpened frees the pictures and files opened in the conversation left.
function releaseOpened() {
  for (const viewer of root.querySelectorAll(".file-viewer")) { viewer.close(); viewer.remove(); }
  state.opened.forEach((u) => URL.revokeObjectURL(u));
  state.opened = [];
}

// ---- reminders ("remind me later") ---------------------------------------------------------
//
// Personal and local: a reminder only asks for the person's attention at a
// time they chose. It changes nothing about the message and nobody else
// sees it. A reply to that message ends it; so do Done and Cancel.

const canRemind = () => !!(state.overview && state.overview.remind);
const reminderOf = (id) => ((state.overview && state.overview.reminders) || []).find((r) => r.message === id);

function dueText(iso) {
  const d = new Date(iso), now = new Date();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (d.toDateString() === now.toDateString()) return "today " + time;
  if (d.toDateString() === new Date(+now + 864e5).toDateString()) return "tomorrow " + time;
  return d.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" }) + " " + time;
}

// localInput is d as a datetime-local input's value (this computer's time).
const localInput = (d) => new Date(+d - d.getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

// reminderLine is a stored message's reminder: set one, or the one it
// has, with its time, and change, done and cancel.
function reminderLine(m) {
  if (!canRemind() || m.event) return null;
  const r = reminderOf(m.id);
  if (!r) return el("button", { type: "button", class: "text-btn", onclick: () => remindDialog(m) }, "Remind me…");
  const act = (path) => () => remindAct(path, m.id).catch((e) => announce(e.message));
  return el("span", { class: "reminder" + (r.overdue ? " overdue" : "") },
    (r.overdue ? "Reminder due since " : "Reminder ") + dueText(r.due) + " · ",
    el("button", { type: "button", class: "text-btn", onclick: () => remindDialog(m, r) }, "Change…"), " · ",
    el("button", { type: "button", class: "text-btn", onclick: act("/api/remind/done") }, "Done"), " · ",
    el("button", { type: "button", class: "text-btn", onclick: act("/api/remind/cancel") }, "Cancel"));
}

async function remindAct(path, id, due) {
  const r = await api(path, due === undefined ? { id } : { id, due });
  if (r.note) announce(r.note);
  await loadOverview();
  if (state.dm) await loadDM();
  else if (state.thread) await loadThread();
}

// remindDialog sets or moves a reminder: a few times, or one chosen.
function remindDialog(m, r) {
  const now = new Date();
  const presets = [["In 30 minutes", new Date(+now + 30 * 60e3)], ["In 2 hours", new Date(+now + 2 * 3600e3)],
    ["Tomorrow at 9:00", new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 9, 0)]];
  const choices = presets.map(([label, d], i) => choice("radio", "remind-when", String(i), label + " (" + dueText(d.toISOString()) + ")"));
  const custom = choice("radio", "remind-when", "custom", "At a time I choose:");
  const at = el("input", { type: "datetime-local", id: "remind-at" });
  at.value = localInput(new Date(+now + 3600e3));
  at.addEventListener("input", () => { custom.input.checked = true; });
  dialog({
    title: r ? "Move the reminder" : "Remind me later",
    body: [el("p", {}, "About: " + firstLine(m.body, 90)),
      r && el("p", { class: "hint" }, "Now " + (r.overdue ? "due since " : "at ") + dueText(r.due) + "."),
      el("fieldset", { class: "choices" }, el("legend", {}, "When"), choices.map((c) => c.row), custom.row, at),
      el("p", { class: "hint" }, "It only reminds you, on this computer: nothing is sent, and the message stays as it is. It ends when you reply to this message, or mark it done.")],
    ok: r ? "Move" : "Remind me",
    run: async () => {
      const pick = [...choices, custom].find((c) => c.input.checked);
      if (!pick) throw new Error("Choose when.");
      const due = pick === custom ? new Date(at.value) : presets[Number(pick.input.value)][1];
      if (isNaN(+due) || +due <= Date.now()) throw new Error("Choose a time in the future.");
      await remindAct("/api/remind", m.id, Math.floor(+due / 1000));
    },
  });
}

// openReminder opens the message a reminder is about.
async function openReminder(r) {
  if (r.conv) {
    await openDM(r.conv);
    flash(r.message);
  } else {
    await openThread(r.message, r.message);
  }
}

// remindersSection lists the pending reminders at the top of the list,
// overdue ones first (they stay until they end).
function remindersSection() {
  const rs = canRemind() ? state.overview.reminders || [] : [];
  if (!rs.length) return [];
  const sorted = [...rs].sort((a, b) => (b.overdue - a.overdue) || a.due.localeCompare(b.due));
  const overdue = rs.filter((r) => r.overdue).length;
  return [el("li", { class: "result-head" }, "Reminders" + (overdue ? " · " + overdue + " due" : "")),
    ...sorted.map((r) => el("li", {}, el("button", { type: "button", class: "conv-item reminder-item" + (r.overdue ? " overdue" : ""), onclick: () => openReminder(r) },
      el("span", { class: "conv-main" },
        el("span", { class: "conv-top" }, el("span", { class: "conv-name" }, r.title || "A message"),
          el("span", { class: "conv-time" }, (r.overdue ? "due " : "") + dueText(r.due))),
        el("span", { class: "conv-sub" }, "From " + r.from)))))];
}

// ---- notifications (browser device) -----------------------------------------------------
//
// Off until the person turns them on, here. The service worker shows each
// alert ("AgentNet" / "New activity"); this page never shows one. It tells
// the relay which messages it actually presented, so those do not alert.

const notifyPermission = () => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

function renderNotify(n) {
  const line = $("notify-line");
  line.hidden = false;
  if (!n) { fill(line, "Notifications are not available in this view."); return; }
  // Every change redraws the overview: an unchanged line keeps its button,
  // so a click is never lost to a redraw under the pointer.
  const key = JSON.stringify([n, notifyPermission()]);
  if (line.dataset.key === key) return;
  line.dataset.key = key;
  const iphone = !n.native && typeof navigator !== "undefined" && /iPhone|iPad/.test(navigator.userAgent) &&
    !(window.matchMedia && window.matchMedia("(display-mode: standalone)").matches);
  if (!n.available) {
    fill(line, "Notifications: " + (iphone ? "on iPhone, add AgentNet to your Home Screen first, then turn them on there." : n.reason));
  } else if (!n.native && notifyPermission() === "denied") {
    fill(line, "Notifications are blocked in this browser's settings. Everything else works without them.");
  } else if (n.enabled) {
    fill(line, "Notifications on, from " + plural(n.allowed.length, "contact", "contacts") + (n.pending ? " (your server is told when this page reconnects)" : "") + " · ",
      el("button", { type: "button", class: "text-btn", onclick: () => notifyAct("/api/notify/disable") }, "Turn off"));
  } else if (n.pending) {
    fill(line, "Notifications off here; your server is told when this page reconnects · ",
      el("button", { type: "button", class: "text-btn", onclick: () => notifyDialog() }, "Turn on…"));
  } else {
    fill(line, "Notifications off · ", el("button", { type: "button", class: "text-btn", onclick: () => notifyDialog() }, "Turn on…"));
  }
}

async function notifyAct(path, body) {
  try {
    const r = await api(path, body || {});
    if (r.note) announce(r.note);
    await loadOverview();
    if (state.dm) await loadDM();
  } catch (e) {
    announce(e.message);
  }
}

// notifyDialog turns notifications on, only on the person's click: the
// browser's own permission question comes from that click.
function notifyDialog() {
  const native = !!(state.overview.notify && state.overview.notify.native);
  if (native) return alertsDialog();
  dialog({
    title: "Get notifications?",
    body: [el("p", {}, "When someone in a chat writes to you, or an agent you invited answers, this device shows \u201cAgentNet: New activity\u201d. It never shows what was written."),
      el("p", {}, "You can mute any chat, and turn this off again here."),
      el("details", { class: "tech" }, el("summary", {}, "Details"),
        el("p", {}, "Your server keeps your notification settings and learns which of your messages belong to the same conversation (not which one), and when you read one here. Your browser's push service learns when a notification is sent to this device, not what it is about."),
        el("p", {}, "Your system may delay or hide notifications (Focus, battery saving, a closed browser on some systems). Chats notify you unless muted.")),
      el("p", { class: "hint" }, "Your browser asks next whether AgentNet may show notifications.")],
    ok: "Turn on",
    run: async () => {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") throw new Error("The browser did not allow notifications. Everything else works without them.");
      const r = await api("/api/notify/enable", {});
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// alertsDialog turns on this computer's own alerts (the daemon shows them;
// the browser is not asked for anything).
function alertsDialog() {
  dialog({
    title: "Get alerts on this computer?",
    body: [el("p", {}, "When someone in a chat writes to you, or an agent you invited answers, this computer shows \u201cAgentNet: New activity\u201d while AgentNet runs here, even with this page closed. It never shows what was written."),
      el("p", {}, "You can mute any chat, and turn this off again here."),
      el("details", { class: "tech" }, el("summary", {}, "Details"),
        el("p", {}, "Nothing leaves this computer for this: AgentNet decides from the messages it already holds. Your system may delay or hide alerts (do not disturb, focus modes). Clicking an alert opens its DM on Linux; on macOS and Windows it only shows. Chats notify you unless muted.")),
    ],
    ok: "Turn on",
    run: async () => {
      const r = await api("/api/notify/enable", {});
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// dmNotifyChips are a DM's mute, and whether its person may alert you.
function dmNotifyChips(t) {
  const n = state.overview && state.overview.notify;
  if (!n || !n.available || !t.id) return [];
  const muted = (n.mutes || []).includes(t.id);
  return [el("button", { type: "button", class: "peer-chip" + (muted ? "" : " on"), onclick: () => notifyAct("/api/notify/mute", { conv: t.id, muted: !muted }),
    title: muted ? "This chat is muted" : "New activity in this chat notifies you when device alerts are on" }, muted ? "Unmute chat" : "Mute chat")];
}

// reportSeen tells the relay which messages of the open DM the person has
// in front of them: the page visible and focused, that DM, its newest
// message in view. Only then; opening a DM is not enough.
function reportSeen() {
  const t = state.dmData, n = state.overview && state.overview.notify;
  if (!t || !n || !n.enabled || document.visibilityState !== "visible" || !document.hasFocus()) return;
  const tl = $("timeline");
  if (tl.scrollHeight - tl.scrollTop - tl.clientHeight > 40) return;
  const ids = t.messages.filter((m) => m.dir === "in").slice(-32).map((m) => m.id);
  if (!ids.length || state.seenReported[t.id] === ids[ids.length - 1]) return;
  state.seenReported[t.id] = ids[ids.length - 1];
  api("/api/notify/seen", { conv: t.id, ids }).catch(() => { delete state.seenReported[t.id]; });
}

// showList shows the conversation list, in every lens (a summary alert,
// or one whose conversation is not here).
function showList(why) {
  state.messageOpening = null;
  closeMentions();
  clearTyping();
  root.classList.remove("show-conv");
  if (why) announce(why);
  if (state.overview) void Zoom.go(0, {});
}

// openClicked opens the DM a desktop alert named, if this computer holds
// it; otherwise the list, saying so. The id is only looked up, never used
// any other way.
async function openClicked(conv) {
  if (conv && typeof conv === "object" && conv.refused) { showList(conv.refused); return; }
  if (conv && typeof conv === "object" && conv.workspace) { // named by a notification: shown first, then its conversation
    try { await switchWorkspace(conv.workspace); } catch (e) { showList(e.message); return; }
    conv = conv.target;
  }
  if (conv && typeof conv === "object" && conv.review) { showList(""); toggleReview(true); return; }
  if (conv && typeof conv === "object" && conv.msg) { await openMessage({ id: conv.msg, conv: conv.conv, dir: conv.dir }); return; }
  if (!(state.overview.dms || []).some((d) => d.id === conv)) {
    showList("The conversation of that alert is not on this computer.");
    return;
  }
  await openDM(conv);
}

// openNotified opens the conversation a notification names, as this
// device resolves its channel; if it is not here yet, it waits for the
// stream to catch up, then says so. A summary opens the conversation list.
function openNotified(chan) {
  if (!chan) {
    showList("New activity in more than one conversation.");
    return;
  }
  state.pendingOpen = { chan, until: Date.now() + 15000 };
  retryOpen();
  setTimeout(() => {
    if (state.pendingOpen && state.pendingOpen.chan === chan) {
      state.pendingOpen = null;
      showList("The conversation of that notification is not on this device.");
    }
  }, 15000);
}
currentHost.onOpen((target, kind, context) => {
  const dest = kind === 'review' ? {review: true} : kind === 'message' ? {msg: target, ...context} : kind === 'conversation' ? target : null;
  if (dest !== null) { if (state.overview) void openClicked(dest); else state.clickedAtStart = dest; }
  else openNotified(target);
}, ['message', 'review']);

async function retryOpen() {
  if (state.switching) await state.switching; // a workspace being shown first
  const p = state.pendingOpen;
  if (!p) return;
  const r = await api("/api/notify/resolve?chan=" + encodeURIComponent(p.chan)).catch(() => ({}));
  if (!r.conv || state.pendingOpen !== p) return;
  state.pendingOpen = null;
  await openDM(r.conv);
}

// renderOffline shows, above the conversations, that THIS workspace is not
// connected to its server now (a browser device knows its own stream; a
// computer's page learns it from its device line). Other workspaces are
// their own connections and say so themselves.
function renderOffline(o) {
  const strip = $("offline");
  if (!strip) return;
  const d = o.device;
  const off = !!(d && d.online === false && !d.revoked);
  strip.hidden = !off;
  if (off) {
    const w = currentHost && currentHost.workspace;
    strip.textContent = (w && w.name ? w.name + ": not" : "Not") + " connected to your server now. What you write here waits and is sent when it is back" + (w && currentHost.workspaces ? "; other workspaces are unaffected." : ".");
  }
}

// machineLines says what this computer or browser is in one plain line;
// the technical details (where the responder runs, the address, the key,
// the version) wait one click away.
function machineLines(o) {
  const tech = ["Address: " + o.me.address, "Key: " + o.me.fingerprint, o.version && "AgentNet " + o.version];
  const d = o.device;
  if (d) {
    if (d.revoked) return { summary: "This device was removed from its server: nothing more is sent or received here.", details: tech };
    return {
      summary: (d.online ? "Connected to your server" : "Not connected to your server now: what you write waits here") +
        (d.persisted === false ? " · This browser may clear this device's data" : ""),
      details: ["This browser runs nothing: questions and tasks wait for you.", ...tech],
    };
  }
  if (o.me.responder) return { summary: "Your responder: " + o.me.responder, details: ["It runs in " + o.me.responder_dir, ...tech] };
  return { summary: "No responder: questions and tasks wait for you", details: ["To choose one, see agentnet help responder.", ...tech] };
}

// ---- contacts ------------------------------------------------------------------
//
// One entry per exact address. A contact holds its conversations separately:
// a reply-linked chain is one conversation; a message linked to nothing is
// shown as a single message, never merged into a topic by guesswork. Review
// notices are reports from another machine, kept apart from both.

// contactsOf groups thread summaries by address, newest activity first.
function contactsOf(threads) {
  const by = new Map();
  for (const t of threads) {
    let c = by.get(t.peer);
    if (!c) {
      c = { peer: t.peer, conversations: [], singles: [], reports: [], review: 0, unread: 0, running: 0, notices: 0,
        waiting: false, keyChanged: false, lastAt: t.last_at, last: "" };
      by.set(t.peer, c);
    }
    c.keyChanged ||= t.key_changed;
    if (new Date(t.last_at) > new Date(c.lastAt)) c.lastAt = t.last_at;
    c.notices += t.notices; // open reports, also any that got a reply
    if (t.notice_only) { c.reports.push(t); continue; }
    c.review += t.review; c.unread += t.unread; c.running += t.running; c.waiting ||= t.waiting;
    const open = t.review || t.running || t.waiting;
    (t.count > 1 || open ? c.conversations : c.singles).push(t);
  }
  const newest = (a, b) => new Date(b.last_at) - new Date(a.last_at);
  const list = [...by.values()];
  for (const c of list) {
    c.conversations.sort(newest); c.singles.sort(newest); c.reports.sort(newest);
    const latest = [...c.conversations, ...c.singles].sort(newest)[0];
    c.last = latest ? latest.last : "";
  }
  return list.sort((a, b) => new Date(b.lastAt) - new Date(a.lastAt));
}

// searchKnown finds known agents (by address) and conversations or single
// messages (by their first and latest lines). It never invents people.
function searchKnown(q, threads, dir) {
  q = q.trim().toLowerCase();
  if (!q) return { people: [], dms: [], agents: [], conversations: [], listed: [] };
  const o = state.overview || {};
  // People by the name they give or their device; DMs by their lines or
  // the person's name. Nothing matched is merged or guessed.
  const has = (s) => (s || "").toLowerCase().includes(q);
  const people = (o.people || []).filter((p) => has(p.label) || has(p.address) || devicesOf(p).some((d) => has(d.address) || has(d.name) || has(deviceWords(d.address))));
  const dms = (o.dms || []).filter((d) => has(d.title) || has(d.last) || has(d.peer.label));
  const agents = contactsOf(threads).filter((c) => whoMatches(o, c.peer, q));
  const known = new Set(threads.map((t) => t.peer));
  // Agents the server lists that there is no conversation with yet.
  const listed = ((dir && dir.members) || []).filter((m) => !known.has(m.address) && whoMatches(o, m.address, q));
  const conversations = threads.filter((t) => !t.notice_only &&
    (t.title.toLowerCase().includes(q) || t.last.toLowerCase().includes(q)))
    .sort((a, b) => new Date(b.last_at) - new Date(a.last_at));
  return { people, dms, agents, conversations, listed };
}

// ---- directory --------------------------------------------------------------------
//
// Who the server lists as enrolled, pushed by the daemon with presence: only
// for finding someone. Choosing one opens their contact or a new
// conversation to them; nothing is sent, trusted or approved by choosing.

const presenceWord = { connected: "online", reconnecting: "reconnecting", offline: "offline" };
const presenceLine = { connected: "Their computer is connected", reconnecting: "Their computer is reconnecting",
  offline: "Their computer is offline" };
const presenceTitle = "Whether the server sees their AgentNet running now; it does not mean a person is there.";

function directory() {
  return (state.overview && state.overview.directory) || { status: "unknown", members: [] };
}

// memberPresence is the server's word on an address, only while its view
// is current and lists them; otherwise null: nothing is said.
function memberPresence(addr) {
  const d = directory();
  if (!d.current) return null;
  const m = d.members.find((x) => x.address === addr);
  return m && presenceWord[m.presence] ? m.presence : null;
}

function presenceOf(addr) {
  const p = memberPresence(addr);
  return p && presenceWord[p];
}

// peerPresence is what a conversation's header says about the peer's
// computer: the server's pushed view while it is current and lists them;
// "not known now" once that view is not current. Only where the server
// lists no one (an older server) or does not list this peer does it show
// the one check made when the conversation was opened, with its time,
// never as live.
function peerPresence(peer) {
  const d = directory();
  if (d.status === "listed" && !d.current) return "Connection not known now";
  const p = memberPresence(peer);
  if (p) return presenceLine[p];
  const c = state.presence[peer];
  if (!c) return "";
  return c.at ? "Checked " + when(c.at) + ": " + c.text.charAt(0).toLowerCase() + c.text.slice(1) : c.text;
}

function presenceBadge(addr) {
  const p = presenceOf(addr);
  return p && el("span", { class: "presence-dot " + p, title: presenceTitle }, p);
}

// directoryNote says plainly what the list is and what it is not.
function directoryNote(d) {
  if (d.status === "not_listed") return "Your server does not list its members: it runs an older AgentNet, which its operator can update.";
  if (d.status !== "listed" || !d.at) return "Who is on your server is not known yet.";
  const notes = [];
  if (!d.current) notes.push("Your server's current list is not available: this one is as of " + when(d.at) + ", and who is online is not known.");
  if (d.truncated) notes.push("The server lists only the 1,000 most recently joined agents; others are not shown.");
  return notes.join(" ");
}

// chooseMember opens someone the server lists: their contact if there is
// one, otherwise a new conversation to them (nothing is sent until Send).
function chooseMember(addr) {
  clearSearch();
  if (state.overview.threads.some((t) => t.peer === addr)) {
    openHub({ kind: "device", key: addr });
    return;
  }
  newConversationDialog(addr);
}

function memberRow(m) {
  const recent = Date.now() - new Date(m.joined) < 7 * 24 * 3600e3;
  return el("li", {}, el("button", { type: "button", class: "result member", onclick: () => chooseMember(m.address) },
    el("span", { class: "result-kind" }, runsAgent(m.address) ? "Agent" : "Person"),
    el("span", { class: "result-main" }, who(m.address),
      el("span", { class: "hint" }, recent ? "joined " + when(m.joined) : "no conversation yet")),
    presenceBadge(m.address)));
}

// directorySection lists the agents the server has that there is no
// conversation with yet, newest first, a few at a time.
function directorySection(threads) {
  const d = directory();
  const known = new Set(threads.map((t) => t.peer));
  const others = d.members.filter((m) => !known.has(m.address) && !deviceOwner(m.address)); // a checked person's devices are under them
  const note = directoryNote(d);
  if (!others.length && !note) return [];
  const open = !!state.directoryOpen;
  const shown = open ? others : others.slice(0, 5);
  return [
    el("li", { class: "result-head" }, others.length ? "Also on your server (" + others.length + ")" : "On your server"),
    note && el("li", { class: "hint dir-note" }, note),
    ...shown.map(memberRow),
    others.length > shown.length && el("li", {}, el("button", { type: "button", class: "singles-toggle",
      onclick: () => { state.directoryOpen = true; rerenderContacts(); } }, "Show all " + others.length)),
  ];
}

const plural = (n, one, many) => n + " " + (n === 1 ? one : many);

// counts shows a contact's or conversation's numbers side by side; they are
// never added together.
function counts(x) {
  return [
    x.review > 0 && el("span", { class: "badge", title: "Decisions for you here" }, String(x.review),
      el("span", { class: "sr-only" }, x.review === 1 ? " needs your decision" : " need your decision")),
    x.unread > 0 && el("span", { class: "conv-flag unread" }, x.unread + " new"),
    x.notices > 0 && el("span", { class: "conv-flag report", title: "Reports that requests wait on that machine" },
      plural(x.notices, "report", "reports")),
  ];
}

function threadFlag(t) {
  if (t.review) return el("span", { class: "badge" }, String(t.review), el("span", { class: "sr-only" }, " needs your decision"));
  if (t.key_changed) return el("span", { class: "conv-flag danger" }, "Key changed");
  if (t.running) return el("span", { class: "conv-flag calm" }, "Responder working");
  if (t.unread) return el("span", { class: "conv-flag unread" }, t.unread + " new");
  if (t.waiting) return el("span", { class: "conv-flag calm" }, "Awaiting reply");
  return null;
}

// threadRow is one compact line for a conversation or single message.
function threadRow(t, open, single) {
  const current = !!(state.data && state.data.messages.some((m) => m.id === t.id));
  const b = el("button", { type: "button", class: "thread-row" + (single ? " single" : ""), "aria-current": current ? "true" : "false" },
    el("span", { class: "thread-title" }, t.title),
    t.count > 1 && el("span", { class: "thread-count", title: plural(t.count, "message", "messages") }, String(t.count)),
    threadFlag(t),
    el("span", { class: "conv-time" }, when(t.last_at)));
  b.addEventListener("click", () => open(t.id, b));
  return el("li", {}, b);
}

// reportLine groups one sender's review notices: the latest reported text
// and time, never presented as that machine's current queue.
function reportLine(c) {
  // Every report from this sender: open ones (also any that got a reply and
  // so sit in a conversation) and dismissed ones, newest first.
  const open = ((state.overview && state.overview.review) || []).filter((it) => it.notice && it.peer === c.peer)
    .map((it) => ({ id: it.id, at: it.at, text: it.excerpt, open: true }));
  const seen = new Set(open.map((r) => r.id));
  const all = [...open, ...c.reports.filter((t) => !seen.has(t.id)).map((t) => ({ id: t.id, at: t.last_at, text: t.title, open: false }))]
    .sort((a, b) => new Date(b.at) - new Date(a.at));
  if (!open.length) return null; // dismissed ones are history: listed under "Earlier reports"
  const latest = all.find((r) => r.open);
  const conv = c.conversations[0] || c.singles[0]; // the newest conversation with that machine, if any
  const d = el("details", { class: "tech" }, el("summary", {}, "Every report"),
    el("ul", { class: "report-items" }, all.map((r) => el("li", {},
      el("time", { datetime: r.at }, when(r.at)), " · ", r.open ? "not dismissed" : "dismissed", " · ",
      el("span", { class: "hint" }, r.text)))));
  const latestItem = ((state.overview && state.overview.review) || []).find((it) => it.notice && it.peer === c.peer && it.id === latest.id);
  const v2 = latestItem && latestItem.report;
  // A host's timestamp can name several snapshots in the same second.
  // Show each, bound to its own report; their order and a live queue are not known.
  const tied = all.filter((r) => r.open && +new Date(r.at) === +new Date(latest.at));
  const snapshots = tied.map((r) => ({ r, item: ((state.overview && state.overview.review) || []).find((it) => it.notice && it.peer === c.peer && it.id === r.id) }));
  const canAct = snapshots.some(({ item }) => item && item.report && item.report.items.some((x) => x.actionable));
  return el("div", { class: "report-line" },
    el("p", {}, el("strong", {}, c.peer), " reported at " + when(v2 ? v2.at : latest.at) + (tied.length > 1 ? ": " + tied.length + " snapshots at that time; their order is not known." : v2 ? ", from " + v2.host + ": " + plural(v2.items.length || v2.count || 0, "request", "requests") + " waiting there." : ": " + firstSentence(latest.text) + ".")),
    el("p", { class: "hint" }, canAct ? "As their operator you can decide these from here; the host applies a decision only if the request is still in the state you saw."
      : "That was true on that machine at that time. " + decidersSentence(v2, c.peer)),
    tied.length > 1 ? el("div", {}, snapshots.map(({ r, item }) => el("div", {},
      el("p", { class: "hint" }, "Snapshot from " + (item && item.report ? item.report.host : c.peer) + " at " + when(r.at) + ". This is what it reported then, not a live queue."),
      item && item.report ? reportItems(item) : el("p", { class: "hint" }, r.text)))) : v2 && reportItems(latestItem),
    el("div", { class: "report-actions" },
      conv && el("button", { type: "button", class: "chip", onclick: () => { toggleReview(false); openThread(conv.id); } }, "Open conversation"),
      el("button", { type: "button", class: "chip", onclick: (e) => dismissReports(c.peer, e.currentTarget),
        title: "Hides these reports on this computer only; nothing changes on " + c.peer }, "Dismiss " + plural(open.length, "report", "reports") + " here"),
      d));
}

const firstSentence = (s) => (s || "").split(/\.\s/)[0].replace(/\.$/, "");

// decidersSentence says who decides a host's requests, as its report says
// (client.Report): never "decide on that machine" (MEL-532), and nothing the
// report does not say. A report naming the requests went to a device that
// decides them; a count report names who does (nobody yet when it names no
// one); a count-text notice (no report) says neither.
function decidersSentence(report, host) {
  if (!report) return host + " did not say who decides these.";
  const items = report.items || [];
  if (items.length) return items.some((x) => x.actionable) ? "You decide these from this device." : host + " listed these but did not let this device decide them.";
  if (!report.count) return "Nothing waits there any more.";
  const list = report.deciders || [];
  const me = state.overview && state.overview.person && state.overview.person.person;
  if (me && list.some((d) => d.person === me)) return "You decide these; this device gets them by name in " + host + "'s next report.";
  if (!list.length) return "Nobody can decide these from their devices yet. Whoever installed " + host + " can name a steward on that machine.";
  const names = list.map((d) => (d.person ? d.label || "Someone" : d.address || "a device"));
  return (names.length === 1 ? names[0] : names.slice(0, -1).join(", ") + " and " + names[names.length - 1]) + (names.length === 1 ? " decides" : " decide") + " these from their devices.";
}

function openReports(peer) {
  return ((state.overview && state.overview.review) || []).filter((it) => it.notice && it.peer === peer).map((it) => it.id);
}

// dismissReports clears one sender's open reports here, one existing
// resolve per report. Nothing is sent and nothing is approved.
async function dismissReports(peer, button) {
  if (button) { if (button.disabled) return; button.disabled = true; }
  const ids = openReports(peer);
  let failed = 0;
  for (const id of ids) {
    try { await act({ do: "resolve", id }); } catch (e) { failed++; }
  }
  announce(failed ? failed + " report(s) could not be dismissed." : "Reports from " + peer + " dismissed on this computer.");
}

// contactBody is what a contact opens into in the sidebar:
// its reports, its conversations, and its single messages folded away.
function contactBody(c, open) {
  const singlesOpen = !!state.singlesOpen[c.peer];
  const unreadSingles = c.singles.reduce((n, t) => n + t.unread, 0);
  return [
    reportLine(c),
    c.conversations.length > 0 && el("ul", { class: "thread-list", "aria-label": "Conversations with " + c.peer },
      c.conversations.map((t) => threadRow(t, open, false))),
    c.singles.length > 0 && el("button", { type: "button", class: "singles-toggle", "aria-expanded": String(singlesOpen),
      onclick: () => {
        state.singlesOpen[c.peer] = !singlesOpen;
        rerenderContacts();
        if (!$("hub").hidden && sameHub(state.hub, { kind: "device", key: c.peer })) renderHub();
      } },
      (singlesOpen ? "Hide " : "") + plural(c.singles.length, "single message", "single messages") +
      (unreadSingles && !singlesOpen ? " · " + unreadSingles + " new" : "") + (singlesOpen ? "" : " (not linked to a conversation)")),
    singlesOpen && el("ul", { class: "thread-list singles", "aria-label": "Single messages from " + c.peer }, c.singles.map((t) => threadRow(t, open, true))),
    !c.conversations.length && !c.singles.length && !c.reports.length && el("p", { class: "hint" }, "No messages yet."),
    el("button", { type: "button", class: "text-btn new-conv", onclick: () => newConversationDialog(c.peer) }, "New conversation with " + c.peer),
  ];
}

// ---- people and DMs ------------------------------------------------------------------
//
// A person is a human as their own AgentNet presents them, set up only when
// they choose; the name is their claim. Each DM is its own conversation, also
// with the same person, and stays apart from device history (the contacts
// below, one installation each). Nothing is merged by name or address.

const personStateText = {
  self: "your person",
  pinned: "checked against their computer's key",
  conflict: "frozen: they published a different record",
  listed: "not checked yet (checked when you start a DM)",
};

const personKey = (p) => p.person || "listed:" + p.address;

function peopleSection() {
  const o = state.overview;
  if (!o || !o.persons) return [];
  const head = el("li", { class: "result-head" }, "People");
  if (!o.person) return [head, ...linkNotices().map((n) => el("li", {}, n)), el("li", { class: "person-setup" }, setupChoice())];
  const dms = o.dms || [];
  const people = [...(o.people || [])].sort((a, b) => {
    const la = dms.find((d) => d.peer.person && d.peer.person === a.person), lb = dms.find((d) => d.peer.person && d.peer.person === b.person);
    if (la || lb) return (lb ? new Date(lb.last_at) : 0) - (la ? new Date(la.last_at) : 0);
    return a.label.localeCompare(b.label);
  });
  return [head,
    ...linkNotices().map((n) => el("li", {}, n)),
    el("li", { class: "hint person-me" }, meLine()),
    ...people.map((p) => personRow(p, dms.filter((d) => d.peer.person && d.peer.person === p.person))),
    !people.length && el("li", { class: "hint empty-list" }, "No one else on your server has set up a person yet."),
    ...groupsSection(), ...teamsSection()];
}

// Invitation controls belong to the exact outgoing inviter, as reported by the provider.
function groupInvitationActions(i) {
  if (i.direction !== "out") return [];
  const host = currentHost, gen = state.gen, ws = wsNow();
  const current = () => host === currentHost && gen === state.gen && ws === wsNow();
  const action = what => {
    const refresh = what === "refresh", label = refresh ? "Refresh invitation" : "Retract invitation";
    dialog({title: label + " for " + personLabelOf(i.target) + "?", ok: label,
      body: [el("p", {}, (refresh ? "Retract this exact invitation and send a fresh proposal for " : "Retract this exact invitation to ") + personLabelOf(i.target) + " in “" + i.title + "”."),
        el("p", {class:"hint"}, refresh ? "The person must review the current group and selected earlier context, then Join again. Prior acceptance does not apply to the fresh invitation. Membership remains unchanged until publication." : "This withdraws the pending invitation, including an acceptance that has not been published. It does not remove an existing member or recall copies already received.")],
      run: async () => {
        if (!current()) throw Error("Conversation or workspace changed. Review this invitation again.");
        const latest = (state.overview?.group_invitations || []).find(n => n.id === i.id);
        if (latest?.direction !== "out" || latest["can_" + (refresh ? "refresh" : "cancel")] !== true) throw Error("This invitation can no longer be changed here. Refresh the conversation and review it again.");
        await api("/api/groups/" + (refresh ? "refresh" : "cancel"), {id:i.id}, host);
        if (!current()) return;
        announce(refresh ? "Fresh invitation sent; they must Join again. Membership is unchanged." : "Invitation retracted; membership is unchanged.");
        await loadOverview(); if (current() && state.dm === i.conv) await loadDM(false);
      }});
  };
  return [i.can_cancel === true && el("button", {type:"button",class:"text-btn",onclick:()=>action("cancel")}, "Retract invitation…"),
    i.can_refresh === true && el("button", {type:"button",class:"text-btn",onclick:()=>action("refresh")}, "Refresh invitation…")];
}

function groupInvitationNotices(t) {
  const o = state.overview, records = o?.group_invitations || [];
  return records.filter(i => {
    if (t && i.conv !== t.id) return false;
    const room = t && t.id === i.conv ? t : (o.dms || []).find(d => d.id === i.conv);
    if ((room?.members || []).some(m => m.person === i.target)) return false;
    const other = records.filter(n => n.id !== i.id && n.conv === i.conv && n.target === i.target && n.direction === i.direction);
    if (i.direction === "out" && i.status === "stale") return !other.some(n => n.status === "pending" || n.status === "accepted");
    return i.direction === "in" && i.status === "accepted" && !room && !other.some(n => n.status === "pending");
  });
}
function groupInvitationNotice(i) {
  return i.direction === "out" ? "Invitation to " + personLabelOf(i.target) + " is stale. Review current people and history, send a fresh invitation, and obtain fresh consent."
    : "Your consent to " + i.title + " is recorded; you have not joined yet. Membership publication is pending. If the group changed, ask the inviter for a fresh invitation and accept that new proposal.";
}
async function reviewFreshGroupInvitation(i, open = openDM) {
  const host = currentHost, gen = state.gen, ws = wsNow();
  await open(i.conv);
  if (currentHost !== host || state.gen !== gen || wsNow() !== ws || state.dmData?.id !== i.conv) return;
  inviteGroupDialog(state.dmData, i.target);
}

function groupsSection(open = openDM) {
  const o = state.overview;
  if (!o?.groups) return [];
  return [el("li", { class: "result-head" }, "Groups", el("button", { type: "button", class: "text-btn", onclick: newGroupDialog }, "New group…")),
    ...(o.group_invitations || []).filter(i => i.direction === "in" && ["pending", "stale"].includes(i.status)).map(i => el("li", {},
      el("button", { type: "button", class: "thread-row", onclick: () => groupInvitationDialog(i) }, "Invitation: " + i.title + " · from " + i.inviter))),
    ...groupInvitationNotices().map(i => el("li", { class: "hint" }, groupInvitationNotice(i), groupInvitationActions(i), i.direction === "out" && el("button", { type: "button", class: "text-btn", onclick: () => reviewFreshGroupInvitation(i, open) }, "Review fresh invitation for " + personLabelOf(i.target) + "…"))),
    ...(o.dms || []).filter(humanGroup).map(d => dmRow(d, open))];
}

// Team membership is only a reviewed person selection, never room authority.
function groupPeopleSelection(t, host, gen, ws, initial, onReview = () => {}) {
  const owner = state.overview?.person?.person, chosen = new Map();
  const excluded = id => id === owner || (t?.members || []).some(m => m.person === id);
  const current = () => gen === state.gen && wsNow() === ws && currentHost === host && state.overview?.person?.person === owner;
  const chips = el("div", { class: "person-chips", "aria-label": "Selected people" });
  const note = el("p", { class: "hint" });
  const team = el("select", { id: "group-team", "aria-label": "Team" }, el("option", { value: "" }, "Choose a people list"));
  let reviewed = !!initial, loading = false, seq = 0;
  const draw = () => fill(chips, ...[...chosen.keys()].map(id => el("span", { class: "chip person-chip" }, personLabelOf(id),
    el("button", { type: "button", class: "chip-x", "aria-label": "Remove " + personLabelOf(id), onclick: () => { if (!state.dialogBusy) { chosen.delete(id); draw(); } } }, "×"))),
    !chosen.size && el("span", { class: "hint" }, "Nobody selected."));
  const add = snap => {
    if (!Array.isArray(snap.persons)) throw Error("The team snapshot has no verified people list.");
    for (const p of snap.persons) if (p && typeof p.id === "string" && !excluded(p.id)) chosen.set(p.id, p);
    reviewed = true; onReview(); draw(); note.textContent = "Current team people as verified at " + when(snap.at) + ". Review and remove anyone you do not mean. Later team changes grant no group or history access.";
  };
  if (initial) add(initial); else draw();
  const addTeam = el("button", { type: "button", class: "text-btn", onclick: async () => {
    if (!current() || state.dialogBusy || loading || !team.value) return;
    loading = true; addTeam.disabled = true; const request = ++seq;
    try {
      const snap = await api("/api/teams/snapshot", { teams: [team.value] }, host);
      if (current() && request === seq) add(snap);
    } catch (e) { if (current()) note.textContent = e.message; }
    finally { loading = false; if (current()) addTeam.disabled = false; }
  } }, "Add list’s people");
  api("/api/teams", undefined, host).then(v => {
    if (!current()) return;
    fill(team, el("option", { value: "" }, "Choose a people list"),
      (v.current ? v.teams || [] : []).filter(t => t.listed && !t.archived && !t.conflict).map(t => el("option", { value: t.id }, t.name)));
    if (!v.current) note.textContent = "Current people lists are unavailable. No people were added.";
  }).catch(e => { if (current()) note.textContent = e.message; });
  return { nodes: [el("label", { for: "group-team", class: "field-label" }, "People list (optional)"), team, addTeam, chips, note],
    current, reviewed: () => reviewed, persons: () => { if (loading) throw Error("Wait for the current people list snapshot before inviting."); return [...chosen.keys()]; },
    forget: id => { chosen.delete(id); draw(); } };
}

function newGroupDialog(initial) {
  initial = Array.isArray(initial?.persons) ? initial : null;
  const host = currentHost, gen = state.gen, ws = wsNow();
  const name = el("input", { id: "group-name", maxlength: 64, autocomplete: "off" });
  const selection = groupPeopleSelection(null, host, gen, ws, initial);
  let created = null;
  dialog({ title: "New group", ok: "Create group", focus: name,
    body: [el("label", { for: "group-name", class: "field-label" }, "Group name"), name, ...selection.nodes,
      el("p", { class: "hint" }, "Earlier context: this new group has no earlier messages or files to share."),
      el("p", { class: "hint" }, "You become its first administrator. Others join only after accepting their invitation; ordinary messages never run an agent.")],
    run: async () => {
      if (!selection.current()) throw Error("Workspace changed; review your group again.");
      const people = selection.persons();
      if (!created) { created = await api("/api/groups/new", { title: name.value.trim() }, host); name.disabled = true; }
      for (const person of people) {
        if (!selection.current()) throw Error("Workspace changed; the created group and sent invitations remain there. Review the remaining people again.");
        try { await api("/api/groups/invite", { conv: created.id, person, history: {} }, host); }
        catch (e) { throw Error("Group created. Sent invitations remain separate from membership; review remaining people and retry explicitly. " + e.message); }
        selection.forget(person);
      }
      if (!selection.current()) return;
      if (people.length) announce("Invitations sent separately; each person still must accept. A changed group may require a fresh invitation and consent.");
      await loadOverview(); await openDM(created.id);
    } });
  $("dialog-ok").disabled = !name.value.trim();
  name.addEventListener("input", () => { if (!state.dialogBusy) $("dialog-ok").disabled = !created && !name.value.trim(); });
}

function groupInvitationDialog(i) {
  const host = currentHost, gen = state.gen, ws = wsNow(), stale = i.status === "stale";
  const decide = async accept => {
    if (gen !== state.gen || wsNow() !== ws) throw Error("Workspace changed; review the invitation again.");
    await api("/api/groups/decide", { id: i.id, accept }, host);
    if (gen !== state.gen || wsNow() !== ws) return;
    announce(accept ? "Acceptance recorded. Membership waits for the administrator's verified update." : "Invitation declined.");
    await loadOverview();
  };
  dialog({ title: (stale ? "Invitation to " : "Join ") + i.title + (stale ? "" : "?"), ok: stale ? "No thanks" : "Accept invitation",
    body: [el("p", {}, stale ? "Group details changed. Ask for a new invitation to join, or dismiss this one." : "Invitation from " + i.inviter + ". Accepting joins this group after the administrator publishes its signed update."),
      el("p", { class: "hint" }, (i.history || []).length ? "Shared earlier context: " + plural(i.history.length, "selected message", "selected messages") + " and only their attached files. Other earlier messages stay private." : "No earlier messages or files are shared."),
      !stale && el("button", { type: "button", class: "text-btn", onclick: async () => { try { await decide(false); $("dialog").close(); } catch(e) { announce(e.message); } } }, "No thanks")],
    run: () => decide(!stale) });
}

function inviteGroupDialog(t, initialPerson) {
  if (!inviteRights(t).people || !t.members.some(p=>p.person===state.overview?.person?.person && p.admin)) { announce("Only a current administrator can invite a permanent member."); return; }
  const host = currentHost, gen = state.gen, ws = wsNow();
  const people = (state.overview?.people || []).filter(p => p.person && p.state === "pinned" && !(t.members || []).some(m => m.person === p.person));
  const person = el("select", { id: "group-invite-person" }, people.map(p => el("option", { value: p.person }, p.label + (p.email ? " · " + p.email : ""))));
  if (initialPerson) { if (!people.some(p => p.person === initialPerson)) { announce("That person is no longer eligible. Review current group members and people."); return; } person.value = initialPerson; }
  const selection = groupPeopleSelection(t, host, gen, ws, null, () => { person.disabled = true; });
  const mode = el("select", { id: "group-history-mode" }, el("option", { value: "none" }, "Share nothing earlier"), el("option", { value: "last" }, "Last messages"), el("option", { value: "since" }, "Messages since a date"), el("option", {value:"selected"},"Selected messages"));
  const last = el("input", { id: "group-history-last", type: "number", min: 1, max: 64, value: "10" });
  const since = el("input", { id: "group-history-since", type: "datetime-local" });
  dialog({ title: "Invite to " + t.title, ok: "Send invitation", focus: person,
    body: [el("label", { for: "group-invite-person", class: "field-label" }, "Single person (people list selection below)"), person, ...selection.nodes,
      !people.length && el("p", { class: "hint" }, "No other pinned person available. Open a person's conversation to check their device first."),
      el("label", { for: "group-history-mode", class: "field-label" }, "Earlier context"), mode,
      el("label", { for: "group-history-last" }, "Last messages (up to 64)"), last,
      el("label", { for: "group-history-since" }, "Since"), since,
      el("fieldset",{class:"share-choices"},el("legend",{},"Selected messages (up to 64)"),(t.messages || []).filter(m=>m.group_ref).slice(-64).map(m=>choice("checkbox","group-history-selected",m.id,el("span",{},dmAuthor(m,t)+": "+(firstLine(m.body,70)||"Files only"),(m.attachments || []).length>0&&el("span",{class:"kind-tag"},"Includes "+plural(m.attachments.length,"file","files")))).row)),
      el("p", { class: "hint" }, "Selected messages include their attached files. An invitation grants no access until that person accepts; other history stays private.")],
    run: async () => {
      if (!selection.current() || state.dm !== t.id) throw Error("Conversation changed; review the invitation again.");
      const selected=new Set([...root.querySelectorAll('input[name="group-history-selected"]:checked')].map(n=>n.value));
      const history = mode.value === "last" ? { last: Number(last.value) } : mode.value === "since" ? { since: new Date(since.value).getTime() } : mode.value==="selected"?{refs:t.messages.filter(m=>selected.has(m.id)).map(m=>m.group_ref)}:{};
      if (mode.value === "since" && !Number.isFinite(history.since)) throw Error("Choose a valid date.");
      const targets = selection.reviewed() ? selection.persons() : [person.value];
      if (!targets.length || targets.some(p => !p)) throw Error("Choose at least one person to invite.");
      for (const target of targets) {
        if (!selection.current() || state.dm !== t.id) throw Error("Conversation changed; sent invitations stay separate. Review the remaining people again.");
        try { await api("/api/groups/invite", { conv: t.id, person: target, history }, host); }
        catch (e) { throw Error("Sent invitations remain separate from membership; review remaining people and retry explicitly. " + e.message); }
        selection.forget(target);
      }
      if (!selection.current()) return;
      announce("Invitations sent separately; membership waits for each person's explicit acceptance. A changed group may require a fresh invitation and consent."); await loadOverview();
    } });
}

function pendingGroupPeople(t) {
  const seen = new Set();
  return (state.overview?.group_invitations || []).filter(i => i.conv === t.id && i.direction === "out" &&
    ["pending", "accepted"].includes(i.status) && !(t.members || []).some(m => m.person === i.target)).filter(i => {
      if (seen.has(i.target)) return false; seen.add(i.target); return true;
    });
}
function groupMemberCount(t) {
  const invited = pendingGroupPeople(t).length;
  return plural(t.members.length, t.frozen ? "last verified member" : "current member", t.frozen ? "last verified members" : "current members") + (invited ? " · " + invited + " invited" : "");
}

function renderGroupMembers(t, box = $("agents")) {
  const me = state.overview?.person?.person;
  const admin = !t.frozen && !dmVisitor(t) && !dmHumanGuest(t) && t.members.some(m => m.person === me && m.admin);
  box.hidden = false;
  fill(box, el("details", {}, el("summary", {}, groupMemberCount(t)),
    el("ul", {}, t.members.map(m => el("li", {}, m.label + " · " + m.address + (m.admin ? " · Administrator" : " · Member"),
      admin && [el("button", { type:"button",class:"text-btn",onclick:()=>groupChangeDialog(t,m.admin?"demote":"promote",m) }, m.admin?"Remove administrator role":"Make administrator"), el("button",{type:"button",class:"text-btn",onclick:()=>groupChangeDialog(t,"remove",m)},"Remove member")])))),
    ...pendingGroupPeople(t).map(i=>el("p", {class:"hint"}, personLabelOf(i.target) + (i.status === "accepted" ? " · Accepted · waiting to join" : " · Invited · waiting for them to accept"), groupInvitationActions(i))),
    ...groupInvitationNotices(t).filter(i => i.direction === "out").map(i => el("p", { class: "hint" }, groupInvitationNotice(i), groupInvitationActions(i), admin && el("button", {type:"button",class:"text-btn",onclick:()=>inviteGroupDialog(t,i.target)},"Review fresh invitation for " + personLabelOf(i.target) + "…"))),
    admin && el("button", {type:"button",class:"text-btn",onclick:()=>groupChangeDialog(t,"rename")},"Rename group…"),
    inviteRights(t).people && el("button", {type:"button",class:"text-btn",onclick:()=>inviteHumanDialog(t)},"Bring in human…"),
    admin && el("button", { type: "button", class: "text-btn", onclick: () => inviteGroupDialog(t) }, "Invite permanent member…"),
    !t.frozen && t.role === "member" && el("button",{type:"button",class:"text-btn",onclick:()=>groupChangeDialog(t,"leave")},"Leave group…"));
}

// deleteConversationDialog: this person's copy of the open conversation from
// all of their devices (client convclear.go); a device thread is stored on
// this device only, and the dialog says so.
function deleteConversationDialog(t) {
  const host = currentHost, gen = state.gen, ws = wsNow(), thread = !state.dmData, threadID = state.thread, peer = thread ? t.peer : t.id;
  const body = thread
    ? [el("p", {}, "This thread is stored on this device only: it is deleted here and nowhere else. " + t.peer + " keeps its copy.")]
    : [el("p", {}, "This removes the messages held on this device, and their copies on your other linked devices. Each of your other devices removes them once it is connected and updated; until then it still shows them."),
      el("p", {}, "Others in this conversation keep their copies. Anything already running keeps running and is removed when it finishes. Messages this device never received are not removed."),
      el("p", { class: "hint" }, "Members, guests and assistants stay as they are. A new message brings the conversation back with only that message.")];
  dialog({ title: thread ? "Delete this thread from this device?" : "Delete the messages in this conversation from your devices?", body, ok: "Delete",
    run: async () => {
      if (gen !== state.gen || wsNow() !== ws || (thread ? state.thread !== threadID : state.dm !== peer)) throw Error("Conversation changed; review this action again.");
      const r = await api("/api/conversation/delete", thread ? { peer: t.peer, thread: threadID } : { conv: t.id }, host);
      if (gen !== state.gen || wsNow() !== ws) return;
      showList(r.note); await loadOverview();
    } });
}

function groupChangeDialog(t,action,person) {
  const host=currentHost,gen=state.gen,ws=wsNow();
  const words={rename:"Rename group",promote:"Make administrator",demote:"Remove administrator role",remove:"Remove member",leave:"Leave group"}, name=action==="rename"?el("input",{id:"group-rename",maxlength:64,value:t.title}):null;
  dialog({title:words[action]+(person?": "+person.label:""),ok:words[action],focus:name,
    body:[name&&[el("label",{for:"group-rename",class:"field-label"},"Group name"),name],
      el("p",{class:"hint"},action==="leave"?"Leaving stops future group messages once peers receive your departure. Saved copies remain. The last administrator must appoint a successor first.":action==="remove"?"This removes future group access after the signed change arrives. Saved copies cannot be recalled.":action==="demote"?"The last administrator must appoint a successor first.":action==="promote"?"This person can invite members and manage the group; it grants no task permissions.":"Current members receive the new name; the group's identity and history stay the same.")],
    run:async()=>{
      if(gen!==state.gen||wsNow()!==ws||state.dm!==t.id)throw Error("Conversation changed; review this action again.");
      const r=await api("/api/groups/manage",{conv:t.id,action,...(person?{person:person.person}:{}),...(name?{title:name.value.trim()}:{})},host);
      if(gen!==state.gen||wsNow()!==ws)return;
      announce(r.queued?"Departure saved here and queued; other members have not confirmed receipt.":"Group change confirmed.");await loadOverview();await loadDM();
    }});
}

// ---- teams (R04): signed person teams of this workspace -------------------------------------
//
// A team is a current list of persons, kept by the relay as a signed chain
// and verified by this device (or its AgentNet). Being in a team grants
// nothing: no conversation, history, job or permission follows from it.
// What is shown is the verified state kept here; whether it is current is
// said, never assumed.
const teamStatusText = { unknown: "People lists not read yet", unsupported: "This server does not have people lists", unavailable: "People lists could not be read now; this is the last verified state", conflict: "A people list record conflicts with the one kept here; it is frozen" };
async function loadTeams() {
  const host = currentHost, gen = state.gen;
  try {
    const [v, permissions] = await Promise.all([api("/api/teams", undefined, host), api("/api/workspace", undefined, host).catch(() => null)]);
    if (gen !== state.gen) return;
    state.teams = v; state.teamAdmin = !!permissions?.can_rename;
    if (mentionView) showMentions();
  } catch (e) {
    if (gen !== state.gen) return;
    state.teams = { status: /404/.test(e.message) ? "unsupported" : "unavailable", current: false, reason: e.message, teams: (state.teams && state.teams.teams) || [] };
  }
  if (state.contactView === "people" && !state.query.trim()) rerenderContacts();
  if (state.hub && state.hub.kind === "team" && !$("hub").hidden) renderHub();
}
const personLabelOf = (id) => {
  const o = state.overview || {};
  if (o.person && o.person.person === id) return "You";
  const p = (o.people || []).find((x) => x.person === id);
  return p ? p.label : "someone not on your server now (" + id.slice(0, 8) + "…)";
};
function teamsSection() {
  const v = state.teams;
  const o = state.overview;
  const head = el("li", { class: "result-head teams-head" }, "People lists",
    v && v.status !== "unsupported" && o.person && el("button", { type: "button", class: "text-btn", onclick: () => teamDialog(null) }, "New people or agent list…"));
  if (!v) return [head, el("li", { class: "hint empty-list" }, "Reading people lists…")]; // loaded when People is chosen and on each change; rendering never fetches
  const q = state.query.trim().toLowerCase();
  const teams = (v.teams || []).filter((t) => !q || (t.name || "").toLowerCase().includes(q)).sort((a, b) => (a.archived !== b.archived ? (a.archived ? 1 : -1) : (a.name || "").localeCompare(b.name || "")));
  const status = v.status === "available" && v.current ? null
    : el("li", { class: "hint team-status " + (v.status === "conflict" ? "danger" : "") }, (teamStatusText[v.status] || v.status) + (v.reason && v.status !== "unsupported" ? " (" + v.reason + ")" : "") + (v.at && !v.current && v.teams && v.teams.length ? " · as of " + when(v.at) : ""));
  return [head, status,
    ...teams.map(teamRow),
    !teams.length && v.status !== "unsupported" && el("li", { class: "hint empty-list" }, q ? "No people list matches." : "No people lists yet.")].filter(Boolean);
}
function teamRow(t) {
  const current = !!(state.hub && state.hub.kind === "team" && state.hub.key === t.id);
  return el("li", { class: "contact-item team-item" + (current ? " open" : "") },
    el("button", { type: "button", class: "conv-item contact", "aria-current": String(current), onclick: () => openHub({ kind: "team", key: t.id }) },
      avatar(t.name || "?"),
      el("span", { class: "conv-main" },
        el("span", { class: "conv-top" }, el("span", { class: "conv-name person-name" }, t.name), el("span", { class: "kind-tag" }, "Team")),
        el("span", { class: "conv-bottom" },
          el("span", { class: "conv-last" }, plural((t.members || []).length, "person", "people")+" · "+plural((t.agents||[]).length,"agent","agents") + (t.managers && t.managers.length ? " · " + plural(t.managers.length, "manager", "managers") : "")),
          t.conflict && el("span", { class: "conv-flag danger" }, "Frozen"),
          t.archived && el("span", { class: "conv-flag calm" }, "Archived"),
          t.manager && el("span", { class: "conv-flag calm" }, "You manage"),
          !t.manager && t.member && el("span", { class: "conv-flag calm" }, "Member")))));
}
// teamAct sends one signed change; the relay's answer (or refusal) is the
// truth, shown as it comes: nothing is changed here until it is verified.
async function teamAct(change, done) {
  const host = currentHost, gen = state.gen;
  try {
    const st = await api("/api/team", change, host);
    if (gen !== state.gen) return;
    announce(change.op === "create" ? "People list created." : change.op === "join" ? "You joined " + st.name + "." : change.op === "leave" ? "You left " + st.name + "." : "Done: " + change.op.replace("-", " ") + ".");
    await loadTeams();
    if (done) done(null, st);
  } catch (e) {
    announce(e.message);
    if (done) done(e);
  }
}
function teamDialog(t) {
  const name = el("input", { id: "team-name", type: "text", maxlength: "64", value: t ? t.name : "", placeholder: "e.g. Data platform", autocomplete: "off" });
  dialog({ title: t ? "Rename " + t.name : "New people list", ok: t ? "Rename" : "Create", focus: name,
    body: [el("label", { for: "team-name", class: "field-label" }, "Name"), name,
      el("p", { class: "hint" }, t ? "Managers rename a people list; members see the new name once the server has it." : state.teams?.tags ? "Choose the people and agents after creating it. You manage the list; everyone can use its @tag in a chat." : "This server supports people lists. Update it to create mixed people and agent tags.")],
    run: async () => { const n = name.value.trim(); if (!n) throw new Error("Give the people list a name."); await new Promise((res, rej) => teamAct(t ? { team: t.id, op: "rename", name: n } : { op: "create", v: state.teams?.tags?2:1, name: n }, (e) => (e ? rej(e) : res()))); } });
}
function teamTargetDialog(t, agent) {
  const host=currentHost,gen=state.gen,o=state.overview;
  const people=[...(o.person?[o.person]:[]),...(o.people||[])].filter(p=>p.person&&['self','pinned'].includes(p.state));
  const person=el('select',{'aria-label':'Person'},el('option',{value:''},'Choose a person'),people.filter(p=>!t.members.includes(p.person)).map(p=>el('option',{value:p.person},p.label)));
  const hosts=[...new Map(people.flatMap(p=>devicesOf(p).map(d=>[d.address,{address:d.address,name:p.label+' · '+(d.name||d.address)}]))).values()];
  const device=el('select',{'aria-label':'Agent device'},el('option',{value:''},'This device'),hosts.filter(h=>h.address!==o.me.address).map(h=>el('option',{value:h.address},h.name)));
  const choice=el('select',{'aria-label':'Agent'}),status=el('p',{class:'hint'});let records=[],request=0;
  const load=async()=>{const n=++request;records=[];fill(choice,el('option',{value:''},'Reading agents…'));try{const v=await api('/api/agents'+(device.value?'?host='+encodeURIComponent(device.value):''),undefined,host);if(n!==request||gen!==state.gen)return;records=(v.agents||[]).filter(a=>a.enabled).map(a=>a.record).filter(a=>!(t.agents||[]).some(x=>x.id===a.id&&x.host===a.host&&x.host_key===a.host_key));fill(choice,el('option',{value:''},'Choose an agent'),records.map(a=>el('option',{value:a.id},a.label)));status.textContent='';}catch(e){if(n===request)status.textContent=e.message;}};
  device.addEventListener('change',()=>void load());
  dialog({title:agent?'Add an agent to @'+t.name:'Add a person to @'+t.name,ok:'Add',body:[el('p',{},'Everyone can use this tag. Adding someone here does not invite them to chats.'),...(agent?[device,choice,status]:[person])],run:async()=>{
    if(gen!==state.gen)throw new Error('Workspace changed. Open the list again.');
    let c={team:t.id,op:'add',target:person.value};
    if(agent){const a=records.find(a=>a.id===choice.value);if(!a)throw new Error('Choose an agent.');c={team:t.id,op:'agent-add',agent:{id:a.id,host:a.host,host_key:a.host_key}};}else if(!person.value)throw new Error('Choose a person.');
    await new Promise((resolve,reject)=>teamAct(c,e=>e?reject(e):resolve()));
  }});if(agent)void load();
}
function teamPersonDialog(t, op, who) {
  const words = { remove: ["Remove " + who.label + " from " + t.name + "?", "Remove", t.version===2 ? "Removes this tag target. Their chat membership and permissions stay the same." : "They can join again themselves; nothing else changes for them."],
    "manager-add": ["Make " + who.label + " a manager of " + t.name + "?", "Make manager", "Managers rename, delete and manage members."],
    "manager-remove": ["Take the manager role from " + who.label + "?", "Take role", "The last manager cannot be removed: the server refuses that, so hand the role over first."] };
  const [title, ok, hint] = words[op];
  dialog({ title, ok, body: [el("p", { class: "hint" }, hint)], run: async () => { await new Promise((res, rej) => teamAct({ team: t.id, op, target: who.id }, (e) => (e ? rej(e) : res()))); } });
}
// Team members are a reviewed selection for independent group invitations.
function teamHub(t) {
  const o = state.overview, me = o.person && o.person.person;
  const v = state.teams || {};
  const members = (t.members || []).map((id) => ({ id, label: personLabelOf(id), manager: (t.managers || []).includes(id), you: id === me }));
  const canManage = t.manager && !t.archived && !t.conflict;
  const note = t.conflict ? "Frozen: a signed record conflicts with the one kept here. Nothing is changed until that is resolved on the server."
    : !v.current ? "Shown as last verified here" + (v.at ? " (" + when(v.at) + ")" : "") + "; the server could not be read now, so it may have changed."
      : "";
  const row = (m) => el("li", { class: "team-member" }, avatar(m.label), el("span", { class: "team-member-name" }, m.label), m.manager && el("span", { class: "kind-tag" }, "Manager"),
    canManage && (t.version===2 || !m.you) && el("span", { class: "team-member-acts" },
      el("button", { type: "button", class: "text-btn", onclick: () => teamPersonDialog(t, m.manager ? "manager-remove" : "manager-add", m) }, m.manager ? "Take manager role" : "Make manager"),
      el("button", { type: "button", class: "text-btn", onclick: () => teamPersonDialog(t, "remove", m) }, "Remove")));
  return [
    note && el("p", { class: "hint" }, note),
    t.version===2 && el("p",{class:"hint"},"Everyone can use @"+t.name+". Only selected people and agents already in the chat are addressed. Managed by "+(t.managers||[]).map(personLabelOf).join(", ")+"."),
    el("ul", { class: "team-members", "aria-label": "Members" }, members.map(row)),
    (t.agents||[]).map(a=>el("div",{class:"team-member"},el("span",{},(catalogRecords(a.host).find(r=>r.id===a.id&&r.host===a.host&&r.host_key===a.host_key)?.label||"Agent · name unavailable")+" · "+a.host),canManage&&el("button",{type:"button",class:"text-btn",onclick:()=>teamAct({team:t.id,op:"agent-remove",agent:a})},"Remove agent"))),
    !members.length && !(t.agents||[]).length && el("p", { class: "hint empty-list" }, "No people or agents selected."),
    el("div", { class: "detail-actions" },
      t.version!==2 && o.person && !t.archived && !t.conflict && (t.member
        ? el("button", { type: "button", class: "chip", onclick: () => teamAct({ team: t.id, op: "leave" }) }, "Leave")
        : el("button", { type: "button", class: "chip", onclick: () => teamAct({ team: t.id, op: "join" }) }, "Join")),
      t.version===2 && canManage && [el("button",{type:"button",class:"chip",onclick:()=>teamTargetDialog(t,false)},"Add person…"),el("button",{type:"button",class:"chip",onclick:()=>teamTargetDialog(t,true)},"Add agent…")],
      canManage && el("button", { type: "button", class: "chip", onclick: () => teamDialog(t) }, "Rename…"),
      (t.manager || state.teamAdmin) && !t.conflict && el("button", { type: "button", class: "chip", onclick: () => dialog({
        title: "Delete this people list?", ok: "Delete list", body: [el("p", {}, "Only the list is deleted. Chats, messages and group membership stay as they are.")],
        run: async () => new Promise((resolve, reject) => teamAct({ team: t.id, op: "delete" }, e => e ? reject(e) : resolve()))
      }) }, "Delete list"),
      !t.archived && !t.conflict && members.length > 0 && el("button", { type: "button", class: "chip", onclick: () => teamSelection([t]) }, "Select for a conversation…")),
    t.version!==2 && t.member && t.manager && (t.managers || []).length === 1 && el("p", { class: "hint" }, "You are the only manager: the server refuses your leaving or losing the role until another manager exists."),
  ];
}
// Capture the current workspace before expanding teams into a new-group draft.
async function teamSelection(teams) {
  const host = currentHost, gen = state.gen, ws = wsNow();
  let snap;
  try { snap = await api("/api/teams/snapshot", { teams: teams.map(t => t.id) }, host); } catch (e) { if (gen === state.gen && wsNow() === ws && currentHost === host) announce(e.message); return; }
  if (gen !== state.gen || wsNow() !== ws || currentHost !== host) return;
  newGroupDialog(snap);
}

// personRow is one person in the sidebar: their DMs open in the main pane
// (the one DM directly, when there is just one).
// Message/list presentation belongs to Zoom below.

// dmFlags shows a DM's numbers side by side.
function dmFlags(d) {
  return [d.waiting > 0 && el("span", { class: "conv-flag calm" }, d.waiting + " kept"),
    d.held > 0 && el("span", { class: "conv-flag calm" }, d.held + " held"),
    d.unread > 0 && el("span", { class: "conv-flag unread" }, d.unread + " new")];
}

// dmRow is one DM in a person's list.
function dmRow(d, open) {
  const b = el("button", { type: "button", class: "thread-row", "aria-current": String(state.dm === d.id) },
    el("span", { class: "thread-title" }, d.title || "No messages yet"),
    d.count > 1 && el("span", { class: "thread-count", title: plural(d.count, "message", "messages") }, String(d.count)),
    dmFlags(d),
    el("span", { class: "conv-time", title: "Started " + new Date(d.created).toLocaleString() + (d.mine ? " by you" : " by them") }, when(d.last_at)));
  b.addEventListener("click", () => d.last_id ? void openMessage({id:d.last_id,conv:d.id}) : open(d.id, b));
  return el("li", {}, b);
}

// choosePerson opens a person found by search: their DMs, never a guess.
function choosePerson(p) {
  clearSearch();
  openHub({ kind: "person", key: personKey(p) });
}

// ---- a person's or a device's conversations (MEL-494) --------------------------------
//
// The sidebar lists people and device contacts, one row each. A row shows
// their conversations in the main pane (the only one directly, when there
// is just one), and an open conversation links back to them. It only
// groups: each conversation keeps its own id and nothing is merged.

// ownerOf is the person or device contact the open conversation belongs to.
function ownerOf() {
  if (humanGroup()) return null;
  if (state.dm) return state.dmData ? { kind: "person", key: personKey(state.dmData.peer) } : null;
  return state.data ? { kind: "device", key: state.data.peer } : null;
}

const sameHub = (a, b) => !!(a && b && a.kind === b.kind && a.key === b.key);

// hubOf is what a person's or device's view shows now: the person and
// their DMs, or the device contact with its conversations.
function hubOf(h) {
  const o = state.overview;
  if (!o || !h) return null;
  if (h.kind === "team") {
    const t = ((state.teams && state.teams.teams) || []).find((x) => x.id === h.key);
    return t ? { label: t.name, team: t, count: (t.members || []).length } : null;
  }
  if (h.kind === "person") {
    const dms = (o.dms || []).filter((d) => personKey(d.peer) === h.key);
    const p = (o.people || []).find((x) => personKey(x) === h.key) || (dms[0] && dms[0].peer);
    return p ? { label: p.label, person: p, dms, count: dms.length } : null;
  }
  const c = contactsOf(o.threads || []).find((x) => x.peer === h.key);
  return c ? { label: c.peer, contact: c, count: c.conversations.length + c.singles.length } : null;
}

// openHub shows a person's or device's conversations, leaving the open
// one (its draft stays with it).
function openHub(h) { if (h.kind === 'team') void Zoom.go(1, {team:h.key,person:null,peer:null,dm:null}); else if (h.kind === 'person') void Zoom.go(1, {person: h.key, peer: null}); else if (h.kind === 'device') void Zoom.go(1, {peer: h.key, person: null}); }

// showPane shows a person's or device's conversations ("hub") or the open
// conversation ("conv") in the main pane.
function showPane(which) {
  const hub = which === "hub";
  $("hub").hidden = !hub;
  $("timeline").hidden = hub;
  if (hub) {
    clearTyping();
    $("composer").hidden = true;
    $("agents").hidden = true; $("agents").classList.remove("group-agents");
    $("notice").hidden = true;
    $("hub-back").hidden = true;
    fill($("peer-chips"));
  }
}

function renderHub() {
  const x = hubOf(state.hub);
  showPane("hub");
  if (!x) {
    fill($("conv-name"), "Choose a conversation");
    $("conv-topic").textContent = "";
    $("conv-presence").textContent = "";
    fill($("hub"), el("p", { class: "hint empty-list" }, "This is not on this computer any more."));
    return;
  }
  $("conv-avatar").replaceWith(Object.assign(avatar(x.label), { id: "conv-avatar" }));
  if (x.team) {
    const t = x.team;
    fill($("conv-name"), t.name);
    $("conv-topic").textContent = "Team · " + plural((t.members || []).length, "member", "members") + (t.archived ? " · archived" : "") + " · grants nothing by itself";
    $("conv-presence").textContent = (state.teams && state.teams.current ? "Verified now" : "Last verified state") + " · id " + t.id.slice(0, 8) + "…";
    state.hubUp = null;
    $("hub-back").hidden = true;
    fill($("hub"), ...teamHub(t));
    return;
  }
  if (x.person) {
    const p = x.person;
    fill($("conv-name"), p.label);
    $("conv-topic").textContent = "Person · " + plural(x.dms.length, "DM", "DMs") + " · each DM is a separate conversation";
    $("conv-presence").textContent = "The name they give · " + devicesText(p) +
      " · " + (personStateText[p.state] || p.state);
    fill($("hub"),
      p.email && el("p", { class: "hint" }, p.email + " · verified by this workspace"),
      deviceDisclosure(p),
      (p.agents || []).map((a) => el("p", { class: "hint agent-link" }, agentLinkText(a, p))),
      x.dms.length ? el("ul", { class: "thread-list hub-list", "aria-label": "DMs with " + p.label }, x.dms.map((d) => dmRow(d, (id) => openDM(id))))
        : el("p", { class: "hint empty-list" }, "No DMs with " + p.label + " yet."),
      p.state === "conflict" ? el("p", { class: "hint" }, "Frozen: no new DM can start with this record.")
        : el("button", { type: "button", class: "chip new-conv", onclick: () => newDMDialog(p) }, "New DM with " + p.label));
    return;
  }
  const c = x.contact;
  const owner = deviceOwner(c.peer);
  fill($("conv-name"), who(c.peer));
  // Back to the person whose device it is; yours has no page of its own
  // (you are no DM partner): back goes to the list.
  const up = owner && owner.state !== "self";
  state.hubUp = up ? { kind: "person", key: personKey(owner) } : null;
  $("hub-back").hidden = !up;
  if (up) $("hub-back").textContent = "\u2039 " + owner.label;
  $("conv-topic").textContent = (owner ? (owner.state === "self" ? "Your device" : owner.label + "'s device") : "Device") + (runsAgent(c.peer) ? " · runs an agent" : "") + " · " + [plural(c.conversations.length, "conversation", "conversations"),
    c.singles.length && plural(c.singles.length, "single message", "single messages")].filter(Boolean).join(" · ");
  $("conv-presence").textContent = peerPresence(c.peer) || "";
  fill($("hub"), contactBody(c, (id) => openThread(id)));
}

// backOneLevel goes from a conversation to its person or device, and from
// there to the list.
function backOneLevel() {
  if ((state.dm || state.data) && state.hub) openHub(state.hub);
  else if (!$("hub").hidden && !$("hub-back").hidden && state.hubUp) openHub(state.hubUp); // a person's device, back to them
  else showList();
}

// setHubBack links the open conversation back to its person or device.
function setHubBack() {
  state.hub = state.hubUp = ownerOf();
  const x = hubOf(state.hub);
  $("hub-back").hidden = !x;
  if (x) $("hub-back").textContent = "‹ " + x.label + (x.count > 1 ? " · " + plural(x.count, x.person ? "DM" : "conversation", x.person ? "DMs" : "conversations") : "");
}

// dmAuthor names who wrote a DM message, as the sending AgentNet says.
function dmAuthor(m, d) {
  if (humanGroup(d) && m.claimed_key && m.synced_from) return "Claimed " + m.from + " · forwarded history";
  if (m.excerpt_pid) return "Claimed " + (m.agent_id ? namedAuthor(m) : m.from || "author") + " · forwarded context";
  if(m.verified_agent && m.agent_author_pid) {const a=(d.agents||[]).find(a=>a.pid===m.agent_author_pid&&a.host.address===m.from);if(a)return agentName(a);}
  if (m.agent_id) return namedAuthor(m);
  if (m.dir === "out") return m.via ? "You, on your " + myDeviceName(m.via) : "You";
  if ((m.origin || "").startsWith("agent:")) { // its verified participant label; exact host stays in Details
    const a = m.pid && (d.agents || []).find(x => x.pid === m.pid && x.host?.address === m.from);
    return a ? agentName(a) : "An agent on " + m.from + ", as their AgentNet says";
  }
  const guest = (d.guests || []).find(g => g.host.address === m.from);
  if (guest) return guest.host.label + " · guest";
  if (dmHumanGuest(d)) return [...guestOriginals(d), ...(d.guests || []).map(g => g.host)].find(p => p && (p.address === m.from || (p.devices || []).some(x => x.address === m.from)))?.label || m.from || "Sender not supplied";
  if (dmVisitor(d)) return m.from || "Sender not supplied";
  if (humanGroup(d)) return (d.members || []).find(p => p.devices?.some(v => v.address === m.from) || p.address === m.from)?.label || m.from || "Sender not supplied";
  return d.peer.label;
}

// personDialog sets up this installation's person, only when asked.
function personDialog() {
  const name = el("input", { id: "person-name", type: "text", autocomplete: "off", maxlength: "64" });
  dialog({
    title: "Set up your person",
    body: [el("p", {}, "A person is you, the human, as others see you in DMs. You set it up once, on this computer; nothing sets it up for you."),
      el("p", {}, "The name is what you call yourself: others see it as your claim, not a checked identity. If you already use AgentNet on another device, use its Add a device link instead of creating a second person."),
      el("label", { for: "person-name", class: "field-label" }, "Your name"), name],
    ok: "Set up",
    focus: name,
    run: async () => {
      const r = await api("/api/person", { label: name.value });
      announce(r.note);
      await loadOverview();
    },
  });
  state.dialogRestore = { type: "person" };
}

// ---- one person, several devices (MEL-433) ---------------------------------------------
//
// A person's devices are listed under the person, never as other people.
// A new device joins with a one-use link made on a device the person
// already has, and becomes theirs only when approved there. A service or
// bot has no person. Plain words first; keys and addresses in Details.

const devicesHere = () => !!(state.overview && state.overview.role); // the provider knows roles and devices

// devicesOf is a person's devices as their record names them (one device
// until they have several).
function devicesOf(p) {
  if (p.devices && p.devices.length) return p.devices;
  return [{ address: p.address, name: p.address.split("/").pop(), fingerprint: p.fingerprint || "" }];
}

// deviceOwner is the checked person (you, or someone pinned here) whose
// record names the device at address, or null. Only a checked record
// groups a device under a person; a name never does.
function deviceOwner(address) {
  const o = state.overview;
  if (!o) return null;
  return [...(o.person ? [o.person] : []), ...(o.people || [])]
    .find((p) => (p.state === "pinned" || p.state === "self") && devicesOf(p).some((d) => d.address === address)) || null;
}

// deviceDisclosure is a person's devices behind one click ("Bob on 2
// devices"), closed until opened: the person stays one node.
function deviceDisclosure(p, open) {
  const n = devicesOf(p).length;
  const body = el("div", {class: "device-disclosure-body"}, identityDetails(p), deviceList(p, open));
  const details = el("details", { class: "person-devices", "data-person": personKey(p) },
    el("summary", {}, (p.state === "self" ? "You" : p.label) + " on " + plural(n, "device", "devices")), body);
  details.querySelector("summary").addEventListener("click", e => {
    e.preventDefault();
    const opening = !details.open;
    for (const animation of body.getAnimations()) animation.cancel();
    if (!motion()) { details.open = opening; return; }
    details.open = true;
    const height = body.scrollHeight + "px";
    const animation = body.animate(opening
      ? [{height: "0px", opacity: 0}, {height, opacity: 1}]
      : [{height, opacity: 1}, {height: "0px", opacity: 0}], {duration: 220, easing: "ease-out", fill: "both"});
    animation.finished.then(() => { details.open = opening; animation.cancel(); }).catch(() => {});
  });
  return details;
}

function identityDetails(p) {
  const status = p.state === "self" ? "Your signed person record."
    : p.state === "pinned" ? "This device has pinned this person's keys and checked the signed device record."
    : p.state === "conflict" ? "Conflicting identity records: this person is frozen here."
    : "First contact: keys come from your workspace directory and are not pinned here yet.";
  return el("div", { class: "identity-details" }, el("p", { class: "hint" }, status + " The display name is self-chosen, not proof of who owns the keys."),
    p.person && el("p", { class: "mono" }, "Person ID: " + p.person),
    el("p", { class: "hint" }, "Compare a device fingerprint with its owner through another channel before relying on their identity."),
    devicesOf(p).map((d) => el("p", { class: "mono" }, d.name + " · " + d.address + " · Key: " + (d.fingerprint || "not available"))));
}

function renamePersonDialog(p) {
  const host = currentHost, workspace = wsNow(), person = p.person;
  const input = el("input", { id: "person-label", type: "text", maxlength: "64", autocomplete: "off", value: p.label });
  dialog({ title: "Change your display name", ok: "Save name", focus: input,
    body: [el("p", {}, "Your person ID, devices, conversations and permissions stay the same. Routing addresses do not change."),
      el("label", { for: "person-label", class: "field-label" }, "Display name"), input,
      el("p", { class: "hint" }, "Names are self-chosen. Other people can use the same name; their identities stay separate.")],
    run: async () => {
      if (workspace !== wsNow() || state.overview?.person?.person !== person) throw new Error("Your workspace or identity changed. Reopen Profile before saving.");
      const label = input.value.trim();
      if (!label) throw new Error("Enter a display name.");
      await api("/api/person/label", { label }, host);
      if (workspace === wsNow()) { await loadOverview(); announce("Display name changed."); }
    },
  });
}

// deviceList shows a person's devices, each with its own device
// conversations one click away, or a first message to it (its agent, as
// that computer's owner allows); this device is marked.
function deviceList(p, open = (addr) => openHub({ kind: "device", key: addr })) {
  const contacts = contactsOf((state.overview && state.overview.threads) || []);
  const me = state.overview && state.overview.me.address;
  return el("ul", { class: "device-list" }, devicesOf(p).map((d) => {
    const c = contacts.find((x) => x.peer === d.address);
    const n = c ? c.conversations.length + c.singles.length : 0;
    return el("li", { class: "device-row" },
      el("span", {}, el("strong", {}, d.name), d.this ? " (this device)" : "", el("span", { class: "hint" }, " · " + d.address)),
      n > 0 ? el("button", { type: "button", class: "text-btn", onclick: (e) => open(d.address, e.currentTarget) }, plural(n, "device conversation", "device conversations"))
        : !d.this && d.address !== me && el("button", { type: "button", class: "text-btn", onclick: () => newConversationDialog(d.address) }, "Write to it…"));
  }));
}

// setupChoice asks who uses this computer, until a person or a service is chosen.
function setupChoice() {
  const o = state.overview;
  if (o.link && o.link.state === "pending") return []; // it joins as the person of the device that approves it
  if (o.role === "service") return [el("p", {}, "This computer is a service or bot: it has no person. DMs are between people; its device conversations are below.")];
  const choose = devicesHere() && !o.device; // a browser is always a person's device
  return [
    el("p", {}, choose ? "Who uses this computer?" : "You have no person yet. A person is you, the human, as others see you in DMs."),
    el("button", { type: "button", class: "chip", onclick: () => personDialog() }, choose ? "I do: set up my person…" : "Set up your person…"),
    choose && el("button", { type: "button", class: "chip", onclick: () => serviceDialog() }, "It is a service or bot…"),
    choose && el("p", {}, "Already use AgentNet as yourself on another device? Add this device from there instead (Your devices, Add a device), so you are one person everywhere."),
  ];
}

function serviceDialog() {
  dialog({
    title: "A service or bot",
    body: [el("p", {}, "This computer then has no person: nobody writes DMs as a human from here, and people are not asked to trust it as one."),
      el("p", { class: "hint" }, "It keeps its device conversations and its invitation. Choose this for a server, an automation or a bot, not for your own computer.")],
    ok: "It is a service",
    run: async () => {
      const r = await api("/api/device/service", {});
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// meLine is "You: Alice · on laptop and phone · Your devices…".
function meLine() {
  const p = state.overview.person;
  const devs = p.devices || [];
  return [
    "You: ", el("strong", {}, p.label), " · ", p.published ? "others can start a DM with you" : "not on your server yet",
    devs.length > 1 && " · on " + devs.map((d) => d.name + (d.this ? " (this one)" : "")).join(", "),
    devicesHere() && [" · ", el("button", { type: "button", class: "text-btn", onclick: () => devicesDialog() }, "Your devices…")],
    // Your other devices' conversations, as each person's are: behind one click.
    devs.length > 1 && deviceDisclosure(p),
  ];
}

// linkNotices are what needs the person about devices: a new device
// asking to join (approved or refused here), or this device's own request.
function linkNotices() {
  const o = state.overview;
  const out = (o.links || []).filter((l) => l.state === "pending").map((l) => el("div", { class: "link-ask", role: "status" },
    el("p", {}, "A new device, ", el("strong", {}, l.name), ", asks to join as you."),
    el("button", { type: "button", class: "chip", onclick: () => linkDialog(l) }, "Check it…")));
  const own = o.link && ownLinkText[o.link.state];
  if (own) out.push(el("div", { class: "link-ask", role: "status" }, el("p", {}, own), o.link.detail && el("p", { class: "hint" }, o.link.detail)));
  return out;
}

const ownLinkText = {
  pending: "Waiting for your other device to approve this one. Open AgentNet there and answer it.",
  refused: "Your other device refused this one: it did not join as you.",
  expired: "That link expired before this device was approved. On your other device, choose Add a device and use the new link.",
  stale: "That link is out of date (your devices changed meanwhile). On your other device, choose Add a device and use the new link.",
  failed: "Joining as you did not work.",
};

// linkDialog asks the person whether a new device is theirs.
function linkDialog(l) {
  const refuse = el("button", { type: "button", class: "chip danger", onclick: async () => {
    try { const r = await api("/api/device/decide", { id: l.id, accept: false }); announce(r.note || "Refused."); $("dialog").close(); await loadOverview(); }
    catch (e) { $("dialog-error").textContent = e.message; }
  } }, "No, refuse it");
  dialog({
    title: "Is “" + l.name + "” your device?",
    body: [el("p", {}, "A new device, ", el("strong", {}, l.name), ", used your link and asks to join as you. Approve it only if you just opened your link on it yourself."),
      el("p", {}, "Once approved it is you: it sends and receives your DMs, and your chats are copied to it."),
      el("details", { class: "tech" }, el("summary", {}, "Details"),
        el("p", {}, "Device: " + l.address), el("p", {}, "Key: " + l.fingerprint),
        el("p", {}, "Asked " + new Date(l.requested_at).toLocaleString() + " · the request ends " + new Date(l.expires).toLocaleTimeString())),
      refuse],
    ok: "Yes, it is mine",
    run: async () => {
      const r = await api("/api/device/decide", { id: l.id, accept: true });
      if (r.note) announce(r.note);
      await loadOverview();
    },
  });
}

// historyLine says how far a new device has your chats.
function historyLine(h) {
  if (h.state === "ended") return "Copying your chats to " + h.name + " stopped: it is no longer one of your devices.";
  const parts=[];
  if(h.deferred)parts.push(h.deferred+" history items waiting for context");
  else if(h.state!=="done")parts.push(h.total?"Preparing history · "+h.done+" of "+h.total+" chats":"Preparing history on this device");
  if(h.blocked)parts.push(h.blocked+" history copies need attention");
  if(h.queued)parts.push(h.queued+" waiting to send");
  if(h.custody)parts.push(h.custody+" on the server, waiting for the device");
  if(parts.length)return parts.join(" · ");
  if(!h.delivery_known)return "History prepared here · delivery confirmation unavailable";
  return h.delivered?h.delivered+" history copies delivered · nothing waiting to send here":"No message history waiting to send from this device";
}

// devicesDialog lists the person's devices, adds one, removes one.
function devicesDialog() {
  const o = state.overview, p = o.person;
  const rows = (p.devices || []).map((d) => el("li", { class: "device-row" },
    el("span", {}, el("strong", {}, d.name), d.this ? " (this device)" : ""),
    el("details", { class: "tech" }, el("summary", {}, "Details"), el("p", {}, "Device: " + d.address), el("p", {}, "Key: " + d.fingerprint)),
    !d.this && (p.devices || []).length > 1 && el("button", { type: "button", class: "text-btn", onclick: async (e) => {
      e.currentTarget.disabled = true;
      try { const r = await api("/api/device/remove", { address: d.address }); announce(r.note); $("dialog").close(); await loadOverview(); }
      catch (err) { $("dialog-error").textContent = err.message; e.currentTarget.disabled = false; }
    } }, "Remove")));
  dialog({
    title: "Your devices",
    body: [el("p", {}, "You are one person on each of these. A new one joins with a link from here and becomes yours only when you approve it."),
      el("ul", { class: "device-list" }, rows),
      (o.history || []).map((h) => el("p", { class: "hint" }, historyLine(h)))],
    ok: "Add a device…",
    run: async () => { addDeviceDialog(); }, // opens once this one has closed
  });
}

// qrCode draws text as a QR code with the vendored encoder, loaded only
// when one is shown: dark on light with a four-module quiet zone, so a
// phone's camera reads it in either theme. null if it cannot be drawn
// (the link is shown as text anyway).
async function qrCode(text) {
  try {
    const { encodeQR } = await import("./qr.mjs");
    const m = encodeQR(text, "raw", { ecc: "low", border: 4 });
    const n = m.length, ns = "http://www.w3.org/2000/svg";
    let d = "";
    m.forEach((row, y) => row.forEach((on, x) => { if (on) d += "M" + x + " " + y + "h1v1h-1z"; }));
    const svg = document.createElementNS(ns, "svg");
    for (const [k, v] of [["viewBox", "0 0 " + n + " " + n], ["class", "qr"], ["role", "img"], ["aria-label", "QR code of the link"], ["shape-rendering", "crispEdges"]]) svg.setAttribute(k, v);
    const bg = document.createElementNS(ns, "rect"), dark = document.createElementNS(ns, "path");
    for (const [k, v] of [["width", n], ["height", n], ["fill", "#fff"]]) bg.setAttribute(k, v);
    dark.setAttribute("d", d);
    dark.setAttribute("fill", "#000");
    svg.append(bg, dark);
    return svg;
  } catch (e) {
    return null;
  }
}

// addDeviceDialog shows a one-use link for a new device of this person:
// a QR code to scan with its camera, and the link as text to paste.
async function addDeviceDialog() {
  let l;
  try { l = await api("/api/device/link", {}); } catch (e) { $("dialog-error").textContent = e.message; return; }
  const qr = await qrCode(l.url);
  const code = el("textarea", { id: "link-code", rows: "3", readonly: true, spellcheck: "false" });
  code.value = l.url;
  const copy = el("button", { type: "button", class: "chip", onclick: async () => {
    try { await navigator.clipboard.writeText(l.url); announce("Link copied."); } catch (e) { code.select(); $("dialog-error").textContent = "Copy it by hand: it is selected."; }
  } }, "Copy the link");
  dialog({
    title: "Add a device",
    body: [el("p", {}, qr ? "On your new device, scan this code with its camera, or open the link below in its browser (or give it to AgentNet there when it joins). It joins as you once you approve it here."
      : "On your new device, open this link: paste it into its browser, or give it to AgentNet there when it joins. It joins as you once you approve it here."),
      qr, code, copy,
      el("p", { class: "hint" }, "It works once, until " + new Date(l.expires).toLocaleTimeString() + ". Anyone with it can ask to be you, so give it only to your own device; you still approve it here.")],
    ok: "Done",
    run: async () => {},
  });
}

// newDMDialog starts a separate DM with a person; nothing is sent yet.
function newDMDialog(p) {
  dialog({
    title: "New DM with " + p.label,
    body: [el("p", {}, "A new conversation with ", el("strong", {}, p.label), " (the name they give) through " + p.address +
      ". It is separate from your other DMs with them."),
      p.state === "listed" && el("p", {}, "Their person record is checked against their computer's key now and kept here. If they later publish a different one, your DMs with them freeze."),
      el("p", { class: "hint" }, "Nothing is sent until you write.")],
    ok: "Start DM",
    run: async () => {
      const r = await api("/api/dm/new", { address: p.address });
      await loadOverview();
      await openDM(r.id);
    },
  });
}

// ---- a DM ---------------------------------------------------------------------------

function beginDM(id) {
  if (state.messageOpening?.conv !== id) state.messageOpening = null;
  const changed = state.dm !== id;
  if (changed) {
    clearTyping();
    keepDraft();
    state.files = [];
    renderPending();
    releaseOpened();
    state.thread = null;
    state.data = null;
    state.dmData = null;
    state.dmNames = {};
    closeMentions();
    state.draftKey = null;
    state.replyReceiver = null;
    state.replyReceiverHost = null;
    state.receiverCatalog = null;
    $("body").value = "";
    grow();
    setKind("message");
    setAnswering(null);
    setDMReply(null);
    setDMAgent(null);
    closeDrivePanel();
  }
  state.dm = id;
  return changed;
}

// ---- the optional Google Drive space of a DM (MEL-490) ----------------------------------------
//
// One panel per open DM, mounted from the program's own module over a
// provider bound to the membership shown when it opened. Everything Google
// is explicit there; the panel says when the workspace has it off and
// points to Settings. Nothing here changes what AgentNet encrypts.
function driveProviderFor(host) {
  if (!host.drive) return Promise.reject(new Error('Project space unavailable on this host'));
  return Promise.resolve(host.drive);
}
function closeDrivePanel() {
  state.driveOpen = false;
  const panel = $("drive-panel");
  if (panel) { panel.hidden = true; fill(panel); }
}
function renderDrive(t) {
  const chips = $("peer-chips");
  if (!chips || !t || !t.peer.person) return;
  const on = !!state.driveOpen;
  chips.append(el("button", { type: "button", class: "peer-chip" + (on ? " on" : ""), title: "This DM's shared Google Drive folder, if the workspace has Drive on; files there are outside AgentNet's encryption",
    onclick: () => toggleDrivePanel(t) }, "Project space"));
  if (on) renderDrivePanel(t);
}
async function toggleDrivePanel(t) {
  state.driveOpen = !state.driveOpen;
  if (!state.driveOpen) { closeDrivePanel(); await loadDM(false); return; }
  await renderDrivePanel(t);
  await loadDM(false);
}
async function renderDrivePanel(t) {
  const panel = $("drive-panel");
  if (!panel || !state.driveOpen) return;
  if (panel.dataset.conv === t.id) return; // mounted already for this DM
  fill(panel, el("p", { class: "hint" }, "Opening the project space…"));
  panel.hidden = false;
  panel.dataset.conv = t.id;
  const host = currentHost, gen = state.gen;
  try {
    const [m, provider] = await Promise.all([moduleOf("drivespace"), driveProviderFor(host)]);
    if (gen !== state.gen || state.dm !== t.id) return;
    fill(panel);
    m.mountDriveSpace(panel, { conv: t.id, provider, agents: (t.agents || []).filter((a) => a.state === "active" && a.host_here),
      openSettings: () => { showSettings(); settingsTab("storage"); } });
  } catch (e) {
    fill(panel, el("p", { class: "hint" }, "The project space is not available on this AgentNet" + (e && e.message ? ": " + e.message : ".")));
  }
}
// saveToDrive copies one received or sent attachment of this DM into its
// Google Drive space, after the person confirms it leaves AgentNet's
// encryption. The backend checks the file against what its sender signed.
function saveToDrive(m, idx, name) {
  const conv = state.dm, host = currentHost;
  dialog({ title: "Copy " + name + " to the project space?", ok: "Copy outside encryption",
    body: [el("p", {}, "The file is decrypted here and uploaded to this DM's Google Drive folder. There it is outside AgentNet's end-to-end encryption: Google and everyone the folder is shared with can read it."),
      el("p", { class: "hint" }, "The copy in AgentNet stays as it is. This needs the workspace's Drive on and your Google account connected in the project space.")],
    run: async () => {
      const [mod, provider] = await Promise.all([moduleOf("drivespace"), driveProviderFor(host)]);
      const r = await mod.saveAttachmentToDrive(provider, conv, m, idx, true);
      announce("Copied to the project space" + (r && r.file && r.file.name ? ": " + r.file.name : "") + ". " + (r && r.notice ? r.notice : ""));
    } });
}

async function openDM(id) { await Zoom.go(2, {dm: id}); }

async function loadDM(scrollToEnd) {
  const id = state.dm, gen = state.gen, focusOpening = state.messageOpening;
  if (!id) return;
  let t;
  try {
    t = await api("/api/dm?id=" + encodeURIComponent(id));
  } catch (e) {
    announce(e.message);
    return;
  }
  if (!alive || state.dm !== id || gen !== state.gen || (focusOpening && state.messageOpening !== focusOpening)) return; // another conversation or workspace was opened meanwhile
  // A participation record is shown as the sentence it stands for, in every view.
  t.messages = t.messages.map((m) => (m.excerpt_pid ? Object.assign({}, m, { actions: [], can: [] })
    : m.event ? Object.assign({}, m, { body: m.event }) : m));
  t.agents = t.agents || [];
  t.guests = t.guests || [];
  state.dmData = t;
  const arrivedFocus = resumeMessageFocus(t);
  fill($("conv-name"), humanGroup(t) ? t.title : t.peer.label);
  $("conv-topic").textContent = humanGroup(t) ? dmVisitor(t) ? "Invited agent context · visitor to this group" : dmHumanGuest(t) ? "Human guest in this group · no membership rights" : "Group conversation · " + groupMemberCount(t) : dmVisitor(t) ? "Invited agent context · you are not a member of this DM"
    : dmHumanGuest(t) ? "Temporary human participation · same private conversation" : "DM with a person · started " + when(t.created) + (t.mine ? " by you" : " by them");
  $("conv-avatar").replaceWith(Object.assign(avatar(humanGroup(t) ? t.title : t.peer.label || t.peer.address), { id: "conv-avatar" }));
  const online = presenceOf(t.peer.address); // the server's pushed view, only while current
  $("conv-presence").textContent = humanGroup(t) ? (t.frozen ? "Last verified audience · access unavailable" : dmVisitor(t) ? "Selected assistant context only" : groupMemberCount(t))
    : (dmVisitor(t) ? "Selected assistant context only" : online ? "Computer " + online : "Conversation with " + t.peer.label);

  fill($("peer-chips"), ...dmNotifyChips(t));
  const n = $("notice");
  n.hidden = !t.frozen && !dmVisitor(t) && !dmHumanGuest(t) && !t.audience_pending;
  fill(n, t.frozen ? el("p", {}, t.frozen) : dmVisitor(t) ? el("p", {}, "Only selected context and requests addressed to this agent are supplied. You cannot send ordinary room messages or change membership.") : [
    dmHumanGuest(t) && el("p", {}, guestAuthor(t) ? "You joined this conversation." : t.guests.some(g => g.host_here && g.state === "invited") ? "You’re invited. Join from your participant details." : t.guests.some(g => g.host_here && !["dismissed", "declined"].includes(g.state)) ? "Sending is unavailable here." : "You’re no longer in this conversation. Messages already received remain."),
    t.audience_pending && el("p", {}, "Audience updated here. Other devices may still be updating.")]);
  n.classList.toggle("guest-notice", dmHumanGuest(t));
  n.classList.toggle("audience-notice", !t.frozen && !!t.audience_pending);
  showPane("conv");
  $("composer").hidden = dmVisitor(t) && !selectedAgentPIDs().some(pid => agentOf(pid)?.can_ask);
  setHubBack();
  renderAgents(t);
  loadDMNames(t);
  renderDrive(t);
  if(topicSelections[t.id]&&!(t.topics||[]).some(x=>x.id===topicSelections[t.id])&&!sends.merge("dm:"+t.id,t.messages).some(m=>m._local&&m.topic===topicSelections[t.id]))topicSelections[t.id]="";
  topicUI.update({conv:t.id},t.topics,topicSelections[t.id],flatTopicScope(t));
  renderDMBody(scrollToEnd);
  const key = "dm:" + id;
  if (state.draftKey === null) restoreDraft(key, t); // just switched here (beginDM)
  else if (state.dmReply && !t.messages.some((m) => m.id === state.dmReply.id || m.lid === state.dmReply.lid)) setDMReply(null);
  if (state.dmAgent && agentOf(state.dmAgent)) setDMAgent(agentOf(state.dmAgent)); // its state now; a dismissed one stays the target
  state.draftKey = key;
  syncComposer();
  if (arrivedFocus) flash(arrivedFocus);
  markDMRead();
  await refreshTyping();
}

// Only the topic displayed by this skin is read; other fetched topics stay unread.
const readingDM = new Set();
function markDMRead() {
  const t = state.dmData, id = state.dm, gen = state.gen, host = currentHost;
  if (!alive || !t || t.id !== id || state.messageOpening?.conv === id || Zoom.level < 2 || Zoom.dm !== id || !!Zoom.query.trim()) return;
  const ids = t.messages.filter(m => m.unread && (m.topic || "") === (topicSelections[id] || "") && !readingDM.has(gen + ":" + m.id)).map(m => m.id);
  if (!ids.length) return;
  const keys = ids.map(id => gen + ":" + id);
  keys.forEach(key => readingDM.add(key));
  void api("/api/act", { do: "read", ids }, host).then(() => {
    if (alive && host === currentHost && gen === state.gen && state.dm === id) void refetch(false);
  }).catch(() => {}).finally(() => keys.forEach(key => readingDM.delete(key)));
}

// Zoom owns its own conversation presentation.
function renderDMBody() { if(state.dmData){topicUI.update({conv:state.dm},state.dmData.topics,topicSelections[state.dm],flatTopicScope(state.dmData));Zoom.refresh();} }

// A named agent's report refers to its host's request copy. Other audience
// copies carry that same logical ID; resolve only in this conversation and
// exact participation/host/agent, never by a similar body or another grant.
function dmReplyParent(m, t, ref = m.quote) {
  if (!ref) return null;
  if (humanGroup(t)) { const found = t.messages.filter(x => x.id === ref || x.lid === ref); return found.length === 1 ? found[0] : null; }
  if (ref === m.quote || !(m.pid && m.agent_id && (m.kind === "answer" || m.kind === "result"))) {
    const exact = t.messages.find(x => x.id === ref); // a device's own copy id, as before
    if (exact) return exact;
    const logical = t.messages.filter(x => x.lid === ref); // a human-audience reply names the parent's LID
    return logical.length === 1 ? logical[0] : null;
  }
  const candidates = t.messages.filter(x => (x.id === ref || x.lid === ref) &&
    x.pid === m.pid && !x.event && !x.excerpt_pid && (x.kind === "question" || x.kind === "task") &&
    x.target && x.target.address === m.from && x.target.agent_id === m.agent_id);
  return candidates.length === 1 ? candidates[0] : null;
}


function sentWhen(m) {
  const sent = m.sent_at || m.at, day = new Date(sent);
  if (day.toDateString() === new Date(m.at).toDateString()) return when(sent);
  return day.toLocaleDateString([], {month: "short", day: "numeric"}) + " · " + day.toLocaleTimeString([], {hour: "numeric", minute: "2-digit"});
}
function messageReference(m,t) {
 const output=["answer","result"].includes(m.kind)||m.status==="progress"||m.verified_agent&&m.kind==="message",ref=m.quote||output&&m.reply_to;
 if(!ref)return null;const parent=dmReplyParent(m,t,ref),previous=(t.messages||[]).slice(0,(t.messages||[]).indexOf(m)).filter(x=>!x.event).at(-1);
 if(output&&!m.quote&&parent===previous)return null;
 const line=parent?parent.deleted?"Message deleted":firstLine(shownText(parent),90):"a message not shown here";
 return el("button",{type:"button",class:"replyref",onclick:()=>parent&&Zoom.go(3,{msg:parent.id})},(m.quote?"Reply to: ":m.kind==="message"?"↳ update on ":"↳ answer to ")+line);
}
function guestUpdateTargets(g,t) {
  const all = [g.host, t.peer, ...(t.members || []), state.overview.person].filter(Boolean);
  const people = [...new Map(all.map(p => [p.person || p.address, p])).values()];
  return people.filter(p => p.person !== state.overview.person?.person && p.state !== "self" && (g.needs_update || []).includes(p.label) && people.filter(x => x.label === p.label).length === 1);
}
function guestUpdateDraft(member) {
  return (member ? "Could you update AgentNet? Our chat needs it for a guest to join." : "Could you update AgentNet? I'd like to bring you into a chat.") + " Get AgentNet: https://github.com/misunders2d/agentnet/releases";
}
async function askGuestUpdate(p) {
  const member = state.dmData?.peer?.person === p.person || state.dmData?.peer?.address === p.address;
  const existing = (state.overview.dms || []).find(d => d.peer?.person === p.person || d.peer?.address === p.address);
  const id = existing?.id || (await api("/api/dm/new", {address: p.address})).id;
  $("dialog").close();
  await loadOverview(); await openDM(id);
  $("body").value = guestUpdateDraft(member);
  grow(); keepDraft(); $("body").focus();
}

// dmMsg is one DM message. Who wrote it is what the sending AgentNet says,
// shown as that: a person's name is their claim, an agent is marked.
// Message/list presentation belongs to Zoom below.

function dmDetails(m) {
  const by = m.dir === "out" ? "This installation" : m.from;
  const origin = m.excerpt_pid ? "Forwarded context: " + (m.from || "the original author") + " is claimed by the forwarding host; authorship is not verified here. This snapshot never runs."
    : m.origin === "ui" ? by + " says a person wrote it. That is its claim, not proof."
    : (m.origin || "").startsWith("agent:") ? by + " says an agent (" + m.origin.slice(6) + ") wrote it. That is its claim."
      : by + " did not say who wrote it.";
  return el("details", { class: "tech" }, el("summary", {}, "Details"),
    el("dl", {},
      el("dt", {}, "Written by"), el("dd", {}, origin),
      m.excerpt_pid && [el("dt", {}, "Context grant"), el("dd", { class: "mono" }, m.excerpt_pid)],
      m.claimed_key && [el("dt", {}, "Claimed author key"), el("dd", { class: "mono" }, m.claimed_key)],
      m.excerpt_pid && m.synced_from && [el("dt", {}, "Forwarded by"), el("dd", {}, m.synced_from)],
      namedDetails(m),
      el("dt", {}, "Sent"), el("dd", {}, new Date(m.sent_at || m.at).toLocaleString()),
      el("dt", {}, "Device"), el("dd", {}, m.from),
      el("dt", {}, "Message id"), el("dd", { class: "mono" }, m.id),
      el("dt", {}, "Kind"), el("dd", {}, m.kind),
      m.delivery && [el("dt", {}, "Delivery"),el("dd",{},deliveryText(m))],
      m.delivery === "waiting" && m.detail && [el("dt", {}, "Waiting for"), el("dd", { class: "mono" }, m.detail.replace(/^peer_update:\s*/, ""))],
      m.delivery !== "waiting" && m.detail && [el("dt", {}, "Send detail"), el("dd", { class: "mono" }, m.detail)],
      m.delivery_uncertain && [el("dt", {}, "Delivery"), el("dd", {}, "Unconfirmed; local retries stopped. Cancellation cannot be confirmed.")],
      m.sent_at && Date.parse(m.at)-Date.parse(m.sent_at)>=60000 && [el("dt",{},"Arrived here"),el("dd",{},new Date(m.at).toLocaleString())],
      m.state && [el("dt", {}, "Stored state"), el("dd", { class: "mono" }, m.state)],
      m.via && [el("dt", {}, "Sent from"), el("dd", {}, "your " + myDeviceName(m.via) + " (" + m.via + ")")],
      !m.excerpt_pid && m.synced_from && [el("dt", {}, "Copied here"), el("dd", {}, "from your " + myDeviceName(m.synced_from) + " when this device was added. Who wrote it is that device's word, not checked here; nothing runs it.")],
      (m.copies || []).length > 1 && [el("dt", {}, "Copies"), el("dd", {}, el("ul", { class: "copy-list" }, m.copies.map((c) =>
        el("li", {}, (c.own?"your ":(c.person||"Someone")+"’s ") + (c.to.split("/")[1]||"device") + ": " + (c.delivery_uncertain ? "delivery unconfirmed" : c.send_stopped && ["failed","not_delivered"].includes(c.state) ? "not sent; local sending stopped" : copyWord[c.state] || c.state)))))],
      m.replica && [el("dt", {}, "Copy"), el("dd", {}, "A copy kept for history: nothing runs it")],
      controlDetails(m)));
}

// myDeviceName is the name of one of your devices, by its address.
function myDeviceName(address) {
  const p = state.overview && state.overview.person;
  const d = p && (p.devices || []).find((x) => x.address === address);
  return d ? d.name : address;
}

function deliveryText(m) {
  if (m.delivery_uncertain) return "Delivery unconfirmed; local retries stopped";
  if (m.send_stopped && ["failed","not_delivered"].includes(m.state)) return "Not sent; local sending stopped";
  return ["waiting","quarantined","expired","failed"].includes(m.delivery) && m.state_text ? m.state_text : copyWord[m.delivery] || m.delivery;
}
const copyWord = { delivered: "delivered", custody: "on your server", queued: "Sending…", waiting: "kept here, not sent yet", failed: "not sent", quarantined: "they could not verify it", expired: "not delivered: that session ended first" };

// agentLinkText says which agent a person's device runs, and where it is
// in DMs here. The link is the invitation's host: that person and device.
function agentLinkText(a, p) {
  const active = a.dms.filter((d) => d.state === "active").length;
  return (a.agent_id ? namedAgentOn(a.agent_id, a.address, isMe(p) ? "Your" : p && p.label ? p.label + "'s" : "") : (isMe(p) ? "Your agent" : "Their agent") + " on " + a.address) + " · " + plural(a.dms.length, "DM", "DMs") +
    (active ? " (" + active + " active)" : "");
}

// ---- agents in a DM ------------------------------------------------------------------

const agentOf = (pid) => state.dmData && (state.dmData.agents || []).find((a) => a.pid === pid);
// askGone: the composer asks an agent that cannot be asked now (dismissed,
// or not active). Its draft keeps that target: nothing meant for the agent
// goes to the person unless they remove the target themselves.
const askGone = () => selectedAgentPIDs().length > 0 && !selectedAgentPIDs().some(pid => agentOf(pid)?.can_ask);
// agentName names an agent by the person whose installation runs it.
// Labels come only from records already read for this exact host and ID.
const catalogRecords = host => state.dm && state.dmNames?.[host] || (state.targetCatalog && state.targetCatalog.host === host ? state.targetCatalog.agents || [] : []);
// A named agent's own name is only its verified catalog record's, read here
// for that exact host and ID; without it, the text says whose assistant it
// is and that its name is unavailable. IDs stay in titles and details, never
// in the text people read; two agents with one name stay two (by their keys).
const catalogName = (id, host, records = catalogRecords(host)) => (records.find(a => a.host === host && a.id === id && a.label) || {}).label || "";
const unnamedAgent = (whose, host) => (whose ? whose + " assistant" : "Assistant on " + host) + " · name unavailable";
function namedAgentLabel(id, host, records = catalogRecords(host), whose = "") {
  const name = catalogName(id, host, records);
  return name ? (whose ? whose + " " : "") + name : unnamedAgent(whose, host);
}
// namedAgentOn is namedAgentLabel with its host device, said once.
const namedAgentOn = (id, host, whose = "") => {
  const name = catalogName(id, host);
  return name ? name + " on " + host : (whose ? whose + " assistant on " : "Assistant on ") + host + " · name unavailable";
};
// whoseAgent: a participation's host as the owner of its agent ("Your", "Bob's").
const whoseAgent = (a) => !a ? "" : (a.host_here || isMe(a.host)) ? "Your" : a.host && a.host.label ? a.host.label + "'s" : "";
const agentName = (a) => a.agent_id ? namedAgentLabel(a.agent_id, a.host.address, undefined, whoseAgent(a)) : (isMe(a.host) ? "Your agent" : a.host.label + "'s agent");
const namedAuthor = (m) => namedAgentOn(m.agent_id, m.from, whoseAgent(m.pid && agentOf(m.pid)));
// catalogLabel names a record in a picker: the same catalog name, plus a short
// ID only where one host offers two agents of one name (to choose between).
function catalogLabel(a, records) {
  const sameHost = records.filter(x => x.host === a.host);
  if (!a.label || !sameHost.some(x => x.id !== a.id && x.label === a.label)) return (a.label || unnamedAgent("", a.host)) + " · " + a.host;
  let length = 8;
  while (length < a.id.length && sameHost.some(x => x.id !== a.id && x.id.slice(0, length) === a.id.slice(0, length))) length++;
  return a.label + " · " + a.id.slice(0, length) + " · " + a.host;
}
const namedProvenance = (m) => "Agent " + m.agent_id + " on " + m.from + " (host assertion)";
function namedDetails(m) {
  return [m.agent_id && [el("dt", {}, "Agent author"), el("dd", {}, namedProvenance(m))],
    m.target && [el("dt", {}, "Target"), el("dd", {}, m.target.address + (m.target.agent_id ? " · agent " + m.target.agent_id : ""))]];
}

// Public records are verified by the captured provider; never consume remote executors.
async function readAgentCatalog(address, host) {
  const view = await api("/api/agents?host=" + encodeURIComponent(address), undefined, host);
  if (view.host !== address || !Array.isArray(view.agents) || view.agents.some(a => !a.record || a.record.host !== address || !/^[0-9a-f]{32}$/.test(a.record.id))) throw new Error("Agent list does not match the selected computer.");
  return view.agents.filter(a => a.enabled !== false).map(a => a.record);
}
const isMe = (p) => !!p && p.state === "self";
// taskFree says whether your tasks run without its owner accepting each one
// (the invitation names your key). Anyone in the DM may still give it a
// task: the owner then accepts it, unless they already allow your key.
const taskFree = (a) => a.host_here || a.tasks_from.some(isMe);

// renderAgents shows the agents invited into the open DM: whose each is,
// what it may be shown, who may give it tasks, and what you can do now.
// Catalog names are display only; mentions always retain the participation ID.
async function loadDMNames(t) {
  state.dmNames ||= {};
  const gen = state.gen, host = currentHost, conv = t.id;
  for (const address of new Set(t.agents.map(a => a.host.address))) {
    if (state.dmNames[address]) continue;
    state.dmNames[address] = [];
    readAgentCatalog(address, host).then(records => {
      if (gen !== state.gen || host !== currentHost || conv !== state.dm) return;
      state.dmNames[address] = records;
      renderAgents(state.dmData); renderTarget();
      rerender(); // Authors and requested targets use the verified catalog names.
    }).catch(() => {}); // exact ID remains visible when a catalog is unavailable
  }
}
// inviteRights: whether you may invite assistants and people into t (the
// participants list and @ both offer only these existing consent paths).
function inviteRights(t) {
  const open = !!t && !t.frozen && !dmVisitor(t) && !dmHumanGuest(t) && (!humanGroup(t) || t.role === "member" && t.members.some(p=>p.person===state.overview?.person?.person));
  return { assistants: open && !!(state.overview?.agents && state.overview?.person),
    people: open && (humanGroup(t) ? t.role === "member" && t.members.some(p => p.person === state.overview?.person?.person) : !!state.overview?.person) };
}
function renderAgents(t) {
  const box = $("agents");
  const { assistants: canInvite, people: canInvitePeople } = inviteRights(t);
  const people = humanGroup(t) ? t.members : [...new Map((dmHumanGuest(t) ? guestOriginals(t) : [state.overview?.person, t.peer]).filter(Boolean).map(p => [p.person, p])).values()];
  box.hidden = false;
  fill(box, el("div", { class: "participant-list" },
    humanGroup(t) && t.frozen && el("span", {class: "hint"}, groupMemberCount(t)),
    people.map(p => el("span", { class: "participant" }, avatar(p.label || p.address, "sm"), p.state === "self" ? "You" : p.label)),
    humanGroup(t) && pendingGroupPeople(t).map(i => el("span", { class: "participant invited-person", "aria-label": "Invited person" },
      avatar(personLabelOf(i.target), "sm"), personLabelOf(i.target), el("span", { class: "tag" }, i.status === "accepted" ? "Accepted · waiting to join" : "Invited · waiting for them to accept"), groupInvitationActions(i))),
    (t.guests || []).filter(g => !["dismissed", "declined"].includes(g.state)).map(g => el("button", { type: "button", class: "participant human-participant", title: g.host.address + " · participation " + g.pid, onclick: () => dialog({ title: g.host.label + " · human participation", body: [guestCard(g, t)], ok: "Close", run: async () => {} }) }, avatar(g.host.label || "Someone", "sm"), g.host_here ? "You" : g.host.label, el("span", { class: "tag" }, g.state === "active" ? "Guest" : g.state === "dismissed" ? "Ended here" : g.state))),
    t.agents.filter(a => !["dismissed", "declined"].includes(a.state)).map(a => el("button", {
      type: "button", class: "participant assistant-participant", title: (a.agent_id || a.pid) + " · " + a.host.address + " · participation " + a.pid,
      onclick: () => dialog({title: agentName(a), body: [agentCard(a, t)], ok: "Close", run: async () => {}})
    }, avatar(agentName(a), "sm"), agentName(a), el("span", { class: "tag" }, a.state === "active" ? "Assistant" : a.state_text || a.state))),
    (canInvite || canInvitePeople) && el("button", { type: "button", class: "text-btn invite-assistant", onclick: () => participantsDialog(t) }, "+ Add participants")));
}

// Human guests use signed consent in this exact conversation. Permanent
// group membership is separate. Neither path recalls previously shared copies.
function participantsDialog(t) {
  const gen = state.gen, ws = wsNow();
  const current = () => gen === state.gen && ws === wsNow() && state.dm === t.id;
  const admin = inviteRights(t).people && humanGroup(t) && t.members.some(p => p.person === state.overview?.person?.person && p.admin);
  dialog({title: "Add participants", body: [
    el("p", {}, "People and assistants join this conversation only through their existing invitation and consent rules."),
    el("button", {type: "button", class: "btn", disabled: !inviteRights(t).people, onclick: () => { if (current()) inviteHumanDialog(t); }}, "Bring in human…"),
    humanGroup(t) && admin && el("button", {type: "button", class: "btn", onclick: () => { if (current()) inviteGroupDialog(t); }}, "Invite permanent member…"),
    humanGroup(t) && el("p", {class: "hint"}, "Any current member can invite a human guest. Guests receive selected earlier context and new messages while present, without group membership or invitation rights. Only administrators can add permanent members. Shared copies remain."),
    state.overview?.agents && el("button", {type: "button", class: "btn", onclick: () => { if (current()) inviteDialog(t); }}, "Invite an assistant…"),
    el("p", {class: "hint"}, "An assistant gets only selected history; its owner accepts the invitation. Address it with @mention. Ordinary chat never runs it.")], ok: "Close", run: async () => {}});
}

function guestCard(g, t) {
  return el("div", { class: "agent-card " + g.state },
    el("div", { class: "agent-head" }, el("span", { class: "tag" }, "Guest"), el("strong", {}, g.host.label || "Someone")),
    g.needs_update?.length && el("p",{class:"hint"},"Waiting for "+g.needs_update.map(label=>label===state.overview.person?.label?"your other device":label).join(", ")+" to update AgentNet"),
    guestUpdateTargets(g,t).map(p => el("button", {type:"button",class:"btn",onclick:()=>askGuestUpdate(p).catch(e=>announce(e.message))}, "Ask "+p.label+" to update")),
    el("p", {}, g.state === "active" ? "Joined this conversation." : g.state === "dismissed" ? "No longer in the active audience here. Other devices may still be updating." : g.state === "invited" ? "Invitation waiting for a response." : g.state_text),
    el("p", { class: "hint" }, "Invited by " + g.inviter.label),
    humanGroup(t) && el("p", {class:"hint"}, "Guest participation only: no group membership or invitation rights. Current members: " + joinedNames((t.members || []).map(p=>p.label || p.address))),
    el("details", { class: "tech" }, el("summary", {}, "Details"), el("p", {}, "Device: " + niceGoogleDevice(g.host.address)), el("p", { class: "mono" }, "Participation: " + g.pid)),
    el("p", { class: "hint" }, g.shared.length ? plural(g.shared.length, "selected earlier message", "selected earlier messages") + ". Available attached files are included only after acceptance." : "No earlier messages or files selected."),
    el("details", {class:"tech"}, el("summary", {}, "Sharing and leaving"), el("p", {}, "Only selected earlier context and new conversation messages are shared while present. No assistant runs automatically. Leaving or removing ends future local access, not previously received copies. Other devices may still be updating. No automatic expiry.")),
    el("div", { class: "agent-actions" },
      g.can_decide && el("button", { type: "button", class: "btn primary", onclick: () => guestActionDialog(g, t, "accept") }, "Join conversation…"),
      g.can_decide && el("button", { type: "button", class: "btn", onclick: () => guestActionDialog(g, t, "decline") }, "Decline invitation…"),
      g.can_leave && el("button", { type: "button", class: "btn", onclick: () => guestActionDialog(g, t, "leave") }, "Leave conversation…"),
      g.can_end && el("button", { type: "button", class: "text-btn", onclick: () => guestActionDialog(g, t, "end") }, "Remove " + g.host.label + "…")));
}

function guestActionDialog(g, t, action) {
  const transport = currentHost, gen = state.gen, ws = wsNow(), accepting = action === "accept", deciding = accepting || action === "decline";
  dialog({ title: accepting ? "Join conversation?" : deciding ? "Decline invitation?" : action === "leave" ? "Leave conversation?" : "Remove " + g.host.label + "?",
    body: [el("p", {}, accepting ? "Join " + joinedNames(guestAudienceNames(t)) + " in this conversation. You’ll receive the selected earlier messages and files, plus new messages while you’re here." : deciding ? "The selected earlier messages and files won’t be shared with you." : action === "leave" ? "You’ll stop receiving new messages here. Messages and files already received remain." : g.host.label + " will no longer receive new messages here. Messages and files already received remain."),
      humanGroup(t) && el("p", {class:"hint"}, "Group guest participation grants no membership or invitation rights. Current members: " + joinedNames((t.members || []).map(p=>p.label || p.address))),
      !deciding && el("p", {class:"hint"}, "Other devices may still be updating."),
      accepting && el("p", { class: "hint" }, g.shared.length ? plural(g.shared.length, "earlier message", "earlier messages") + " selected, with available attached files." : "No earlier messages or files selected.")],
    ok: accepting ? "Join conversation" : deciding ? "Decline" : action === "leave" ? "Leave conversation" : "Remove " + g.host.label,
    run: async () => {
      if (gen !== state.gen || ws !== wsNow() || state.dm !== t.id) throw new Error("Conversation or workspace changed. Reopen this action there.");
      await api(deciding ? "/api/dm/guest/decide" : "/api/dm/guest/end", { pid: g.pid, ...(deciding ? { accept: accepting } : {}) }, transport);
      if (gen === state.gen && ws === wsNow() && state.dm === t.id) { announce(accepting ? "You joined this conversation." : deciding ? "Invitation declined." : action === "leave" ? "You left this conversation here. Received copies remain." : g.host.label + " removed here. Other devices may still be updating."); await loadDM(); }
    } });
}

function inviteHumanDialog(t, address = "") {
  if (!inviteRights(t).people) { announce("Only current conversation members can invite a human guest here."); return; }
  const transport = currentHost, gen = state.gen, ws = wsNow(), current = () => gen === state.gen && ws === wsNow() && state.dm === t.id;
  const members = humanGroup(t) ? t.members : [state.overview.person, t.peer], addresses = new Set(members.flatMap(p => [p.address, ...(p.devices || []).map(d => d.address)]));
  const host = el("input", { id: "guest-host", type: "text", placeholder: "person/device, e.g. carol/desk", autocomplete: "off", spellcheck: "false", maxlength: "65" });
  host.value = address; // chosen from @ (an exact directory entry), still editable here
  const candidates = directory().current ? directory().members.filter(p => !addresses.has(p.address)) : [];
  const updateNote=el("p",{class:"hint",role:"status"});let checkedHost="";
  const checkHost=async()=>{const chosen=host.value.trim();checkedHost="";updateNote.textContent="Checking their app…";try{const v=await api("/api/dm/guest/check",{conv:t.id,host:chosen},transport);if(current()&&host.value.trim()===chosen){checkedHost=chosen;updateNote.textContent=v.text||"";$("dialog-ok").textContent=v.needs_update?.some(p=>p.role==="guest")?"Invite · waits for update":"Invite person";}}catch(e){if(current()&&host.value.trim()===chosen)updateNote.textContent="Could not check this person’s app. Try again.";}};
  // Blur must not insert status text between a checkbox pointer-down and up.
  host.addEventListener("input",()=>{checkedHost="";updateNote.textContent="";});
  const picks = candidates.map(p => el("button", { type: "button", class: "text-btn", onclick: () => { host.value = p.address;void checkHost(); } }, el("strong", {}, p.label || "Someone"), " · ", el("small", {}, niceGoogleDevice(p.address))));
  const ref = m => m.lid || m.id;
  const share = t.messages.filter(m => !m.event && !m.excerpt_pid && !m.deleted).slice(-30).map(m => choice("checkbox", "guest-share", ref(m), el("span", {}, dmAuthor(m, t) + ": " + (firstLine(m.body, 70) || "(files only)"), (m.attachments || []).map(f => el("span", { class: "tag" }, f.name + " · " + size(f.size))))));
  const fileConsent = choice("checkbox", "guest-file-consent", "yes", "Share files attached to the earlier messages I select with this person after acceptance.");
  share.forEach(c => c.input.addEventListener("change", () => { fileConsent.input.checked = false; }));
  const note = el("input", { id: "guest-note", type: "text", maxlength: "200", autocomplete: "off" });
  fileConsent.input.addEventListener("change", () => { if (fileConsent.input.checked && $("dialog-error").textContent === "Selected context includes files. Confirm file sharing, or deselect those messages.") $("dialog-error").textContent = ""; });
  if(address)void checkHost();
  dialog({ title: "Bring in human", body: [updateNote,
    humanGroup(t) && el("p", {class:"hint"}, "Current group members shared with the guest: " + joinedNames(members.map(p => p.label || p.address)) + ". This is guest participation, not permanent membership. Guests cannot invite or manage members."),
    el("p", {}, "Selected earlier messages and files are shared after they join. They’ll also receive new messages while they’re here."),
    el("label", { for: "guest-host", class: "field-label" }, "Person’s address"), host, el("button",{type:"button",class:"text-btn",onclick:()=>void checkHost()},"Check app compatibility"), el("div", { class: "agent-actions" }, picks),
    el("fieldset", { class: "choices" }, el("legend", {}, "Earlier context to share after acceptance"), share.length ? share.map(c => c.row) : el("p", { class: "hint" }, "No earlier messages."), el("p", { class: "hint" }, "None unless selected. Unselected earlier text and files remain private.")), fileConsent.row,
    el("label", { for: "guest-note", class: "field-label" }, "Optional note"), note,
    el("p", {class:"hint"}, humanGroup(t) ? "Current members can remove this guest; the guest can leave. No group membership or task permission is granted. Already shared copies remain." : "Either original person can remove them; they can leave. Already shared copies remain.")], ok: "Invite person",
    run: async () => {
      if (!current()) throw new Error("Conversation or workspace changed. Reopen the invitation there.");
      const address = host.value.trim(), selected = share.filter(c => c.input.checked).map(c => c.input.value);
      if (!address || addresses.has(address)) throw new Error("Choose the exact address of someone outside this conversation.");
      if (t.messages.some(m => selected.includes(ref(m)) && (m.attachments || []).length) && !fileConsent.input.checked) throw new Error("Selected context includes files. Confirm file sharing, or deselect those messages.");
      if(checkedHost!==address){await checkHost();if(checkedHost!==address)throw Error("Could not check this person’s app. Try again.");}
      await api("/api/dm/guest/invite", { conv: t.id, host: address, share: selected, note: note.value }, transport);
      if (current()) { announce("Human invited. No earlier context or files leave before acceptance."); await loadDM(); }
    } });
}

// A person mention travels inside the signed text as a readable Markdown-style
// reference to that exact person (or guest participation):
// [@Name](agentnet:person/ID) or [@Name](agentnet:guest/PID). It is advisory
// attention only: never routing, a grant or membership. Text that does not
// parse, or names no one here, stays inert text; nothing is ever a link.
// (mentionRef, mentionName and mentionPlain are defined with firstLine.)
// mentionWho is the person a reference names in conversation t, as named here.
function mentionWho(kind, id, t = state.dmData) {
  if (!t) return null;
  const mine = state.overview?.person?.person;
  if (kind === "guest") {
    const g = (t.guests || []).find(g => g.pid === id);
    return g && g.host?.label ? { label: g.host.label, me: !!g.host_here, title: g.host.label + " · Guest" } : null;
  }
  const p = [state.overview?.person, t.peer, ...(t.members || []), ...(dmHumanGuest(t) ? guestOriginals(t) : [])].find(p => p && p.person === id && p.label);
  return p ? { label: p.label, me: p.state === "self" || (!!mine && p.person === mine), title: p.label + " · Person" } : null;
}
// mentionNodes renders text with each exact, known mention as a chip.
function mentionNodes(text, t = state.dmData) {
  if (typeof text !== "string" || !text.includes("](agentnet:")) return [text];
  const out = [];
  let last = 0;
  for (const m of text.matchAll(mentionRef)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const who = mentionWho(m[2], m[3], t);
    out.push(who ? el("span", { class: "mention-chip" + (who.me ? " mention-me" : ""), title: who.title }, "@" + who.label) : "@" + m[1]);
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
function messageNodes(text) {
  return rawLinkParts(text).flatMap(part => typeof part === "string" ? mentionNodes(part, state.dm ? state.dmData : null) : [el("a", {href:part.href, target:"_blank", rel:"noopener noreferrer", title:part.href, "aria-label":part.href}, part.label)]);
}
// Previews are buttons that open the full message, where links become live.
function messagePreview(text) {
  return rawLinkParts(text).map(part => typeof part === "string" ? part : part.label).join("");
}
// decodeMentions gives text as it is edited (@Name) and its exact mentions.
function decodeMentions(text) {
  const s = String(text || ""), spans = [];
  let plain = "", last = 0;
  for (const m of s.matchAll(mentionRef)) {
    plain += s.slice(last, m.index);
    spans.push({ start: plain.length, name: m[1], ref: { kind: m[2], id: m[3] } });
    plain += "@" + m[1];
    last = m.index + m[0].length;
  }
  return { text: plain + s.slice(last), spans };
}
const mentionAt = (text, s) => text.slice(s.start, s.start + s.name.length + 1) === "@" + s.name;
const validMentions = (spans, text) => (Array.isArray(spans) ? spans : []).filter(s => s && Number.isInteger(s.start) && typeof s.name === "string" && /^[^\[\]\r\n]{1,80}$/.test(s.name) &&
  ["person", "guest", "agent"].includes(s.ref?.kind) && /^[A-Za-z0-9_-]{1,64}$/.test(s.ref?.id || "") && mentionAt(text, s));
// encodeMentions writes each exact mention still intact in text as its reference.
function encodeMentions(text, spans, plainAgents = true) {
  let out = text;
  for (const s of validMentions(spans, text).filter(s => !plainAgents || s.ref.kind !== "agent").sort((a, b) => b.start - a.start)) {
    out = out.slice(0, s.start) + "[@" + s.name + "](agentnet:" + s.ref.kind + "/" + s.ref.id + ")" + out.slice(s.start + s.name.length + 1);
  }
  return out;
}
// Only intact selected agent references route; names and incoming text never do.
function selectedAgentPIDs(text = $("body").value, spans = state.mentions, fallback = state.dmAgent) {
  const selected = [...new Set(validMentions(spans, text).filter(s => s.ref.kind === "agent").sort((a,b) => a.start-b.start).map(s => s.ref.id))];
  return selected.length ? selected : fallback ? [fallback] : [];
}
// shiftMentions follows one edit from prev to cur: mentions before or after
// it move with the text; one the edit touched is dropped (it is plain text now).
function shiftMentions(spans, prev, cur) {
  const max = Math.min(prev.length, cur.length);
  let p = 0, s = 0;
  while (p < max && prev[p] === cur[p]) p++;
  while (s < max - p && prev[prev.length - 1 - s] === cur[cur.length - 1 - s]) s++;
  const oldEnd = prev.length - s, delta = cur.length - prev.length, kept = [], dropped = [];
  for (const m of spans || []) {
    if (m.start + m.name.length + 1 <= p) kept.push(m);
    else if (m.start >= oldEnd) kept.push({ ...m, start: m.start + delta });
    else dropped.push(m);
  }
  return { kept: kept.filter(m => mentionAt(cur, m) || !dropped.push(m)), dropped };
}
// trackMentions keeps the composer's exact mentions with its text; a mention
// the person edits becomes plain text, and the page says so.
function trackMentions() {
  const cur = mentionInput().value, prev = state.mentionText ?? cur;
  if (cur !== prev && (state.mentions || []).length) {
    const { kept, dropped } = shiftMentions(state.mentions, prev, cur);
    state.mentions = kept;
    if (dropped.some(s => s.ref.kind === "agent" && s.ref.id === state.dmAgent)) state.dmAgent = selectedAgentPIDs(cur, kept, null)[0] || null;
    if (dropped.length) { announce(dropped.map(m => "@" + m.name).join(", ") + (dropped.length > 1 ? " are" : " is") + " no longer an exact mention; the text stays."); renderAgentTarget(); }
  }
  state.mentionText = cur;
}

// Typing @ searches the conversation's people and its added, currently
// askable assistants by name. Plain typed names confer no authority:
// selecting an assistant row binds its exact PID and host; selecting a person
// writes their name with an exact reference to them (above), nothing more.
// Anyone outside is offered only as an invitation through the existing
// consent dialogs, which grant nothing until accepted.
let mentionView = null;
// Zoom's visible write dialog edits the same draft as the hidden composer.
const mentionInput = () => $("dialog").open && $("write-mentions") && $("write-body") || $("body");
const mentionBox = () => mentionInput().id === "write-body" ? $("write-mentions") : $("mentions");
function closeMentions() {
  mentionView = null;
  for (const box of [$("mentions"), $("write-mentions")]) if (box) { box.hidden = true; fill(box); }
  for (const input of [$("body"), $("write-body")]) if (input) {
    input.setAttribute("aria-expanded", "false");input.removeAttribute("aria-activedescendant");
  }
}
// mentionPeople are the people currently here besides you: original members
// (or group members) and active guests, each once by person or participation.
function mentionPeople(t) {
  const me = state.overview?.person?.person, seen = new Map();
  const originals = humanGroup(t) ? t.members : dmHumanGuest(t) ? guestOriginals(t) : [state.overview?.person, t.peer];
  const account = (address) => (address || "").split("/")[0];
  for (const p of (originals || []).filter(Boolean)) {
    if ((me && p.person === me) || !p.person || !mentionName(p.label)) continue;
    seen.set("person:" + p.person, { kind: "person", name: mentionName(p.label), ref: { kind: "person", id: p.person }, role: humanGroup(t) ? (p.admin ? "Group admin" : "Group member") : "In this conversation", more: "account " + account(p.address), title: niceGoogleDevice(p.address) || "" });
  }
  for (const g of t.guests || []) if (g.state === "active" && !g.host_here && mentionName(g.host?.label)) seen.set("guest:" + g.pid, { kind: "person", name: mentionName(g.host.label), ref: { kind: "guest", id: g.pid }, person: g.host.person, role: "Guest", more: (g.inviter?.label ? "invited by " + g.inviter.label + " · " : "") + "account " + account(g.host.address), title: g.host.address + " · participation " + g.pid });
  const people = [...seen.values()], named = {};
  for (const p of people) named[p.name] = (named[p.name] || 0) + 1;
  for (const p of people) if (named[p.name] > 1) p.role += " · " + p.more; // the same name twice: say which, readably
  return people;
}
// mentionInvites: invitation actions for anyone not here, by name, when you may invite.
function mentionInvites(t, query) {
  const rights = inviteRights(t), out = [];
  if (rights.people && query) {
    const inside = new Set([...(humanGroup(t) ? t.members : [state.overview?.person, t.peer]).filter(Boolean).flatMap(p => [p.person, p.address, ...(p.devices || []).map(d => d.address)]),
      ...(t.guests || []).filter(g => !["dismissed", "declined"].includes(g.state)).map(g => g.host?.address)]);
    const pool = humanGroup(t) ? (state.overview?.people || []).filter(p => p.person && p.state === "pinned" && !inside.has(p.person) && !inside.has(p.address)).map(p => ({ name: p.label, person: p.person, address: p.address, title: niceGoogleDevice(p.address) }))
      : directory().current ? directory().members.filter(p => !inside.has(p.address)).map(p => ({ name: p.label || "Someone", address: p.address, title: niceGoogleDevice(p.address) })) : [];
    for (const p of pool.filter(p => p.name && p.name.toLowerCase().includes(query)).slice(0, 5)) out.push({ kind: "invite-person", ...p });
  }
  if (rights.people) out.push({ kind: "invite-people" });
  if (rights.assistants) out.push({ kind: "invite-assistant" });
  return out;
}
function mentionRow(it, i) {
  const [label, sub] = it.kind === "assistant" ? ["@" + it.name, whoseAgent(it.a) ? whoseAgent(it.a) + " assistant" : "Assistant"]
    : it.kind === "collective" ? ["@" + it.name, it.sub]
    : it.kind === "person" ? ["@" + it.name, it.role]
    : it.kind === "invite-person" ? ["Invite " + it.name + "…", "Not in this conversation · joins only if they accept"]
    : it.kind === "invite-people" ? ["Bring in human…", "Guest participation · choose what earlier context they see"]
    : ["Invite an assistant…", "Its owner accepts; it runs only when asked"];
  const title = it.kind === "assistant" ? (it.a.agent_id || it.a.pid) + " · " + it.a.host.address + " · participation " + it.a.pid : it.title || "";
  return el("button", { id: "mention-" + i, type: "button", class: "mention-row" + (it.kind.startsWith("invite") ? " mention-invite" : "") + (!i ? " selected" : ""),
    title, "aria-label": (it.kind.startsWith("invite") ? label : "Mention " + it.name) + ", " + sub, onclick: () => pickMention(i) }, label, el("small", {}, sub));
}
function showMentions(force = false) {
  const input = mentionInput(), t = state.dmData;
  const before = input.value.slice(0, input.selectionStart ?? input.value.length);
  const match = before.match(/(?:^|\s)@([^@\s]*)$/);
  if (!t || !state.dm || state.sending || (!force && !match)) { closeMentions(); return; }
  if (!mentionView) void loadTeams();
  const query = force ? "" : match[1].toLowerCase();
  const agents = dmHumanGuest(t) && !guestAuthor(t) ? [] : (t.agents || []).filter(a => a.can_ask);
  const people = mentionPeople(t);
  const exact=[...agents.map(a=>({kind:'agent',id:a.pid,agentID:a.agent_id,host:a.host.address,hostKey:a.host.fingerprint,name:agentName(a),original:{kind:'assistant',a,name:agentName(a)}})),...people.map(p=>({kind:p.ref.kind,id:p.ref.id,person:p.person,name:p.name,original:p}))];
  const collective=collectiveOptions(exact,state.teams).map(c=>({...c,targets:c.targets.map(p=>p.original)}));
  const items = [...collective,...exact.map(p=>p.original)].filter(p=>p.name.toLowerCase().includes(query)), invites=mentionInvites(t,query);
  const gen = state.gen, conv = t.id;
  mentionView = { items: [...items, ...invites], index: 0, gen, conv, start: match ? before.lastIndexOf("@") : null, end: input.selectionStart ?? input.value.length };
  const box = mentionBox(); box.hidden = false; box.setAttribute("aria-label", "People and assistants");
  fill(box, el("p", { class: "hint" }, "People and assistants"),
    items.length ? items.map((it, i) => mentionRow(it, i)) : el("p", { class: "hint" }, query ? "No one here matches “" + match[1] + "”." : "No one else is here yet."),
    invites.length > 0 && el("div", { class: "mention-invites" }, invites.map((it, i) => mentionRow(it, items.length + i))));
  input.setAttribute("aria-expanded", "true");
}
function pickMention(i) {
  const v = mentionView, it = v?.items[i], t = state.dmData;
  if (!it || v.gen !== state.gen || v.conv !== state.dm || !t || it.kind === "assistant" && !agentOf(it.a.pid)?.can_ask) { closeMentions(); return; }
  if (it.kind === "collective") {
    trackMentions();
    const targets=it.targets.filter(p=>!state.mentions.some(m=>m.ref.kind===(p.kind==='assistant'?'agent':p.ref.kind)&&m.ref.id===(p.kind==='assistant'?p.a.pid:p.ref.id)));
    if (targets.some(p=>p.kind==='assistant'&&!agentOf(p.a.pid)?.can_ask)) {closeMentions();announce('The participants changed. Choose the tag again.');return;}
    const input=mentionInput(), at=v.start??v.end,token=targets.length?targets.map(p=>'@'+p.name).join(' ')+' ':'';
    trackMentions();input.value=input.value.slice(0,at)+token+input.value.slice(v.end);$("body").value=input.value;trackMentions();
    let start=at;
    for(const p of targets){state.mentions.push({start,name:p.name,ref:p.kind==='assistant'?{kind:'agent',id:p.a.pid}:p.ref,role:p.role});start+=p.name.length+2;}
    const agent=targets.find(p=>p.kind==='assistant');if(agent)setDMAgent(agentOf(agent.a.pid));
    input.setSelectionRange?.(at+token.length,at+token.length);grow();keepDraft();closeMentions();renderAgentTarget();input.focus();
    announce(targets.length?'@'+it.name+' expanded to '+targets.length+' recipients. Review their names before sending.':'Everyone from @'+it.name+' is already selected.');return;
  }
  if (it.kind.startsWith("invite")) { // the existing consent dialog decides; nothing is granted here
    closeMentions();
    if (it.kind === "invite-assistant") inviteDialog(t); else inviteHumanDialog(t, it.address || "");
    return;
  }
  if (v.start !== null || it.kind === "person" || it.kind === "assistant") {
    const input = mentionInput(), at = v.start ?? v.end, token = "@" + it.name + " ";
    trackMentions(); // earlier edits first, so positions are this text's
    input.value = input.value.slice(0, at) + token + input.value.slice(v.end);
    input.setSelectionRange?.(at + token.length, at + token.length);
    $("body").value = input.value;
    trackMentions();
    if (it.kind === "person" || it.kind === "assistant") state.mentions = [...(state.mentions || []), { start: at, name: it.name, ref: it.kind === "assistant" ? {kind:"agent",id:it.a.pid} : it.ref, role: it.role }];
    grow();
  }
  if (it.kind === "assistant") setDMAgent(agentOf(it.a.pid));
  keepDraft(); closeMentions(); renderAgentTarget(); mentionInput().focus();
}
function mentionKey(e) {
  if (!mentionView) return;
  const n = mentionView.items.length;
  if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeMentions(); return; }
  if (["ArrowDown", "ArrowUp"].includes(e.key) && n) {
    e.preventDefault();
    mentionView.index = (mentionView.index + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
    for (let i = 0; i < n; i++) $("mention-" + i).classList.toggle("selected", i === mentionView.index);
    $("mention-" + mentionView.index).scrollIntoView?.({ block: "nearest" });
    mentionInput().setAttribute("aria-activedescendant", "mention-" + mentionView.index);
  } else if (e.key === "Enter" && !e.ctrlKey && !e.metaKey && n) { e.preventDefault(); pickMention(mentionView.index); }
}

function ownModelReport(host,agentID="") {
  const o=state.overview;
  if(!o?.person || !devicesOf(o.person).some(d=>d.address===host))return null;
  const report=(o.model_reports||[]).find(r=>r.host===host&&r.agent_id===agentID);
  const date=report?new Date(report.at*1000):null,stamp=date&&Number.isFinite(date.getTime())?date.toLocaleString():"Timestamp unavailable";
  return el("p", {class:"hint report-line", "data-agent-model":host+"#"+agentID}, report ? "Last reported model: "+report.model+" · "+stamp : "Model unknown");
}
function ownModelReports() {
  const o=state.overview,targets=new Map();
  for(const host of o?.agent_devices||[])if(devicesOf(o.person||{}).some(d=>d.address===host))targets.set(host+"#",{host,id:"",label:"Default agent"});
  for(const report of o?.model_reports||[])targets.set(report.host+"#"+report.agent_id,{host:report.host,id:report.agent_id,label:report.agent_id?namedAgentLabel(report.agent_id,report.host):"Default agent"});
  return targets.size ? el("section",{"aria-label":"Your agents’ reported models"},el("h3",{},"Your agents’ reported models"),[...targets.values()].map(t=>el("div",{},el("strong",{},t.label+" · "+deviceWords(t.host)),ownModelReport(t.host,t.id)))) : null;
}
function agentCard(a, t) {
  const rejoined = ["dismissed", "declined"].includes(a.state) && a.host.fingerprint &&
    (t.agents || []).find(b => b.pid !== a.pid && b.host.address === a.host.address &&
      b.host.fingerprint === a.host.fingerprint && b.topic === a.topic && (b.agent_id || "") === (a.agent_id || "") &&
      ["active", "invited"].includes(b.state));
  const shown = a.shared.length + a.missing;
  const facts = ["invited by " + (a.inviters?.length ? a.inviters : [a.inviter]).map(p => isMe(p) ? "you" : p.label).join(" and "),
    shown ? "shown " + plural(shown, "earlier message", "earlier messages") + (a.missing ? " (" + a.missing + " not here)" : "")
      : "shown no earlier messages",
    a.tasks_from.length ? "tasks without asking from " + a.tasks_from.map((p) => (isMe(p) ? "you" : p.label)).join(" and ")
      : "tasks wait for " + (a.host_here ? "you" : a.host.label) + " to accept them"];
  return el("div", { class: "agent-card " + a.state },
    el("div", { class: "agent-head" }, el("span", { class: "tag" }, "Agent"), el("strong", { ...(a.agent_id ? { title: a.agent_id + " · " + a.host.address } : {}) }, agentName(a)),
      el("details", {}, el("summary", {}, "Runs on " + (a.host_here ? "your computer" : a.host.label + "’s computer")), el("span", { class: "hint" }, a.state_text))),
    ownModelReport(a.host.address,a.agent_id||""),
    el("p", { class: "agent-state" }, a.state_text),
    rejoined && el("p", { class: "hint" }, rejoined.state === "active" ? "Already in this chat" : "Rejoin pending"),
    el("p", { class: "hint" }, facts.join(" · ")),
    a.external && a.member && a.topic == null && el("p", { class: "hint" }, "Runs outside this group on " + a.host.label + "’s computer; that computer receives every new message and file here until the agent is removed."),
    a.external && !a.member && el("p", { class: "hint" }, "External host · " + a.host.address + ". It is not a room member; it receives only selected context and requests addressed to this agent."),
    a.note && el("p", { class: "agent-note" }, "Note: " + a.note),
    (a.can_decide || a.can_ask || a.can_dismiss) && el("div", { class: "agent-actions" },
      a.can_decide && el("button", { type: "button", class: "btn primary", onclick: () => decideDialog(a, t, true) }, "Accept…"),
      a.can_decide && el("button", { type: "button", class: "btn", onclick: () => decideDialog(a, t, false) }, "Decline…"),
      a.can_ask && el("button", { type: "button", class: "btn", onclick: () => { setDMAgent(a); $("body").focus(); } }, "Ask"),
      a.can_dismiss && el("button", { type: "button", class: "text-btn", onclick: () => dismissDialog(a) }, a.member ? "Remove agent…" : "Dismiss…")));
}

// choice is one labelled radio or checkbox of a dialog.
function choice(type, name, value, label) {
  const input = el("input", { type, name, id: name + ":" + value });
  input.value = value;
  return { input, row: el("label", { class: "choice" }, input, el("span", {}, label)) };
}

// inviteDialog invites an agent on a member's or explicitly listed external host. Nothing is
// chosen for the person: whose agent, what it may be shown and who may
// give it tasks are all picked here, and both people see them.
function inviteDialog(t) {
  if (dmVisitor(t) || dmHumanGuest(t)) { announce("Only original DM members can invite an agent."); return; }
  const me = state.overview.person;
  const transport = currentHost, gen = state.gen, ws = wsNow();
  let catalogHost = "", catalog = [], catalogError = "", catalogSeq = 0;
  const named = el("select", { id: "invite-agent", "aria-label": "Agent on selected host" }, el("option", { value: "" }, "Device default agent"));
  const catalogStatus = el("p", { class: "hint", role: "status" }, "Choose a computer to see its agents. You can also choose its default agent.");
  const current = () => gen === state.gen && wsNow() === ws && state.dm === t.id;
  const loadCatalog = async address => {
    const seq = ++catalogSeq;
    catalogHost = address; catalog = []; catalogError = ""; named.disabled = true;
    fill(named, el("option", { value: "" }, "Device default agent"));
    catalogStatus.textContent = "Loading agents on " + address + "…";
    try {
      const records = await readAgentCatalog(address, transport);
      if (!current() || seq !== catalogSeq) return;
      catalog = records;
      fill(named, el("option", { value: "" }, "Device default agent"), records.map(a => el("option", { value: a.id, title: a.id + " · " + a.host }, catalogLabel(a, records))));
      catalogStatus.textContent = "Agent names are supplied by this computer. Its owner still decides which tasks may run. Only the earlier messages you choose are shared.";
    } catch (e) { if (current() && seq === catalogSeq) { catalogError = e.message; catalogStatus.textContent = "Named agents unavailable: " + e.message + " Device default can still be chosen."; } }
    finally { if (current() && seq === catalogSeq) named.disabled = false; }
  };
  // A browser device runs no agent. External hosts come from this workspace's
  // current directory; listing does not verify a real-world owner or grant trust.
  const browser = transport?.platform === "browser" || !!state.overview.device;
  const members = humanGroup(t) ? t.members : [me, t.peer];
  const devices = humanGroup(t) ? members.flatMap(p => (p.devices?.length ? p.devices : [{address:p.address,fingerprint:p.fingerprint}]).map(d => ({...d,label:p.label,person:p.person}))) : [];
  const hosts = humanGroup(t) ? [...new Map(devices.filter(d=>d.address && !(browser && d.address===me.address)).map(d=>[d.address,choice("radio","agent-host",d.address,(d.person===me.person?"Yours":d.label+"'s")+", on "+d.address)])).values()] : [!browser && choice("radio", "agent-host", me.address, "Yours, on " + me.address),
    choice("radio", "agent-host", t.peer.address, t.peer.label + "'s, on their " + deviceWords(t.peer.address))].filter(Boolean);
  if (!humanGroup(t)) for (const d of me.devices || []) {
    if (d.address !== me.address && runsAgent(d.address) && !hosts.some(c => c.input.value === d.address))
      hosts.push(choice("radio", "agent-host", d.address, "Yours, on " + deviceWords(d.address)));
  }
  const memberAddresses = new Set(humanGroup(t) ? devices.map(d=>d.address) : [me.address, t.peer.address, ...(me.devices || []).map(d => d.address), ...(t.peer.devices || []).map(d => d.address)]);
  const external = new Set(directory().current ? directory().members.filter(m => !memberAddresses.has(m.address)).map(m => m.address) : []);
  for (const address of external) hosts.push(choice("radio", "agent-host", address, "External host · " + address));
  hosts.forEach(c => c.input.addEventListener("change", () => { if (c.input.checked) loadCatalog(c.input.value); }));
  const share = t.messages.filter((m) => !m.event && !m.excerpt_pid).slice(-30)
    .map((m) => choice("checkbox", "agent-share", m.id, el("span", {}, dmAuthor(m, t) + ": " + (firstLine(m.body, 70) || "(files only)"),
      (m.attachments || []).map(f => el("span", { class: "tag" }, f.name + " · " + size(f.size))))));
  const fileConsent = choice("checkbox", "agent-file-consent", "yes", "Let this assistant read files attached to the messages I select.");
  share.forEach(c => c.input.addEventListener("change", () => { fileConsent.input.checked = false; }));
  const tasks = humanGroup(t) ? [...new Map(devices.filter(d=>d.fingerprint).map(d=>[d.fingerprint,choice("checkbox","agent-tasks",d.fingerprint,(d.person===me.person?"You":d.label)+" on "+d.address)])).values()] : [[me, "You"], [t.peer, t.peer.label]].filter(([p]) => p.fingerprint)
    .map(([p, label]) => choice("checkbox", "agent-tasks", p.fingerprint, label));
  const note = el("input", { id: "agent-note", type: "text", maxlength: "200", autocomplete: "off" });
  const picked = (cs) => cs.filter((c) => c.input.checked).map((c) => c.input.value);
  dialog({
    title: "Add an assistant to " + (humanGroup(t) ? "this group" : "this DM"),
    body: [el("p", {}, humanGroup(t) ? "The agent stays as a member after its owner accepts. Every member can ask it; its owner decides what runs. Choosing an agent already here shares more messages with the same membership." : "It joins only if its owner accepts, on their computer. You both see the invitation, what it may be shown and who may give it tasks."),
      el("fieldset", { class: "choices" }, el("legend", {}, "Whose agent"), hosts.map((c) => c.row)),
      el("p", { class: "hint" }, "External hosts are listed by this workspace's server, not verified real-world owners. Choose an exact named agent; its host must prove support and its owner must accept."),
      el("label", { for: "invite-agent", class: "field-label" }, "Assistant on that host"), named, catalogStatus,
      el("fieldset", { class: "choices" }, el("legend", {}, "Earlier messages it may be shown"),
        share.length ? share.map((c) => c.row) : el("p", { class: "hint" }, "No messages yet."),
        el("p", { class: "hint" }, "None unless you choose. Only selected messages and their available attached files become visible to this agent. Unselected history and files are excluded; unavailable bytes are reported as missing. No private agent session is copied.")),
      fileConsent.row,
      el("fieldset", { class: "choices" }, el("legend", {}, "Tasks without asking its owner each time"), tasks.map((c) => c.row),
        el("p", { class: "hint" }, humanGroup(t) ? "Every current member can ask the same agent. Its owner approves questions and tasks. If the agent is already here, this only shares more selected messages; it grants no new task permission." : "Either of you can ask it questions or give it tasks. A task from someone not chosen here waits for its owner to accept it.")),
      el("label", { for: "agent-note", class: "field-label" }, "A note for its owner (optional)"), note],
    ok: humanGroup(t) ? "Add agent or share more" : "Invite",
    run: async () => {
      const host = picked(hosts)[0];
      if (!host) throw new Error("Choose whose agent to invite.");
      if (!current()) throw new Error("This invitation belongs to another conversation or workspace. Reopen it there.");
      const agentID = named.value;
      if (external.has(host)) {
        if (!directory().current || !directory().members.some(m => m.address === host)) throw new Error("This external host is no longer in the current workspace directory. Reopen the invitation.");
        if (!agentID) throw new Error("Choose an exact named agent on this external host.");
      }
      if (agentID) {
        if (host !== catalogHost || named.disabled || catalogError || !catalog.some(a => a.id === agentID)) throw new Error("Load and choose an agent on this computer first.");
        const fresh = await readAgentCatalog(host, transport);
        if (!current()) throw new Error("Workspace changed. Reopen this invitation in its workspace.");
        if (!fresh.some(a => a.id === agentID)) throw new Error("That agent is no longer available on this computer. Nothing was invited.");
      }
      if (t.messages.some(m => picked(share).includes(m.id) && (m.attachments || []).length) && !fileConsent.input.checked) throw new Error("Selected messages include files. Confirm file access, or deselect those messages.");
      await api("/api/dm/agent/invite", { conv: t.id, host, ...(agentID ? { agent_id: agentID } : {}), share: picked(share), tasks_from: picked(tasks), note: note.value }, transport);
      if (!current()) return;
      announce(humanGroup(t) && t.agents?.some(a=>["active","invited"].includes(a.state)&&a.host.address===host&&(a.agent_id||"")===agentID) ? "Shared more messages with the agent already here." : "Invited. Its owner accepts or declines it on their computer.");
      await loadDM();
    },
  });
}

// decideDialog is the host's explicit answer, showing exactly what an
// accept agrees to.
function decideDialog(a, t, accept) {
  const transport = currentHost, gen = state.gen, ws = wsNow();
  const current = () => gen === state.gen && wsNow() === ws && state.dm === t.id;
  const shared = t.messages.filter((m) => a.shared.includes(m.id));
  const who = isMe(a.inviter) ? "You" : a.inviter.label;
  dialog({
    title: accept ? "Let " + agentName(a).replace(/^Your/, "your") + " join " + (humanGroup(t) ? "this group?" : "this DM?") : "Decline the invitation?",
    body: accept ? [el("p", {}, who + " invited " + agentName(a).replace(/^Your/, "your") + ". If you accept, it answers what " + (humanGroup(t) ? "current group members ask" : "either of you asks") + " it here, on " + a.host.address + " with its local executor configuration."),
      !a.agent_id && !(state.overview.me && state.overview.me.responder) && el("p", { class: "hint" }, "No responder is chosen on this computer yet: what is asked of it waits until you choose one."),
      el("p", {}, shared.length ? "It may be shown these earlier messages:" : "It is shown no earlier messages."),
      shared.length > 0 && el("ul", { class: "quote-list" }, shared.map((m) => el("li", {}, dmAuthor(m, t) + ": " + (firstLine(m.body, 90) || "(files only)"),
        (m.attachments || []).map(f => el("span", { class: "tag" }, f.name + " · " + size(f.size)))))),
      shared.some(m => (m.attachments || []).length) && el("p", { class: "hint" }, "Files attached to selected messages may be read by this agent when available. Missing bytes remain unavailable; unselected room history and files are excluded."),
      a.missing > 0 && el("p", { class: "hint" }, plural(a.missing, "chosen message is", "chosen messages are") + " not on this computer and cannot be shown."),
      el("p", {}, a.tasks_from.length ? "Tasks run without asking you when they come from: " + a.tasks_from.map((p) => (isMe(p) ? "you" : p.label)).join(" and ") + "."
        : "Every task waits for you to accept it."),
      a.note && el("p", {}, "Their note: " + a.note),
      el("p", { class: "hint" }, "You accept exactly this, all or nothing. Chosen messages replay as context; no native session is copied. Either of you can dismiss it later.")]
      : [el("p", {}, "Your agent does not join. If you change your mind, they can invite it again.")],
    ok: accept ? "Accept" : "Decline",
    run: async () => {
      if (!current()) throw new Error("Workspace or DM changed. Reopen this agent decision there.");
      await api("/api/dm/agent/decide", { pid: a.pid, accept }, transport);
      if (!current()) return;
      announce(accept ? "Your agent joined " + (humanGroup(t) ? "this group." : "this DM.") : "Declined.");
      await loadDM();
    },
  });
}

function dismissDialog(a) {
  const transport = currentHost, gen = state.gen, ws = wsNow(), conv = state.dm;
  const current = () => gen === state.gen && wsNow() === ws && state.dm === conv;
  dialog({
    title: (a.member ? "Remove " : "Dismiss ") + agentName(a).replace(/^Your/, "your") + "?",
    body: [el("p", {}, "It gets nothing more from this DM and nothing more can be asked of it. What was already said stays. To have it again, invite it again."),
      el("p", { class: "hint" }, a.host_here ? "If it is running something now, that stops, and its reply is kept here."
        : "Something it already started on " + a.host.address + " may still finish, and its reply arrive, before the dismissal reaches that computer.")],
    ok: a.member ? "Remove agent" : "Dismiss",
    run: async () => {
      if (!current()) throw new Error("Workspace or DM changed. Reopen this dismissal there.");
      await api("/api/dm/agent/dismiss", { pid: a.pid }, transport);
      if (!current()) return;
      announce("Dismissed.");
      await loadDM();
    },
  });
}

// setDMAgent makes the composer ask an agent of the open DM, or write to
// the person again (null). It is part of that DM's draft and never crosses
// to another.
function setDMAgent(a) {
  const previous = state.dmAgent;
  state.dmAgent = a ? a.pid : null;
  if (!a) state.mentions = (state.mentions || []).filter(s => s.ref.kind !== "agent");
  if (a) {
    state.dmReply = null;
    $("replying").hidden = false;
    $("replying-label").textContent = "Asking";
    $("replying-text").textContent = a.host ? agentName(a) + " (on " + a.host.address + ")" + (a.can_ask ? "" : ", cannot be asked now") : "an agent no longer in this DM";
    if (previous !== a.pid || kindValue() === "message") setKind("question");
  } else if (!state.dmReply && !state.answering) {
    $("replying").hidden = true;
  }
  if (state.dm) syncComposer();
  if (humanGroup() && previous !== state.dmAgent) refreshTyping();
}

function startSend(key, text, files, reply, answering, t, kind, retry, id, topic = "", topicRoot = "", addressedPID = state.dmAgent, group = "") {
  sends.begin(key, { id, ...(state.dm ? { lid: id, origin: "ui", send_group: group, send_group_author: state.overview?.me?.fingerprint, pid: addressedPID || "" } : { author: {label: "You", about: ""}, to: t.peer }),
    topic, _topicRoot: topicRoot, dir: "out", from: state.overview?.me?.address || "", body: text, kind, at: new Date().toISOString(), reply_to: reply?.id || answering?.id || "", quote: reply?.id || "",
    attachments: files.map(f => ({name:f.name,size:f.size,openable:false})), files: files.map(f => ({name:f.name,size:f.size,openable:false})) }, retry);
  if (alive && state.draftKey === key) {
    if ($("body").value === text) { $("body").value = ""; grow(); state.typedFor = null; state.mentions = []; state.mentionText = ""; }
    if (reply && state.dmReply === reply) setDMReply(null);
    if (answering && state.answering === answering) setAnswering(null);
    state.files = state.files.filter(f => !files.includes(f)); renderPending(); keepDraft(); syncComposer();
  }
}
function failSend(id, key, ws, text, files, reply, answering, reason, mentions) {
  if (!sends.fail(id, reason)) return; // a pushed durable message already merged
  if (!alive) return; // the current renderer owns drafts; this failed turn keeps its Retry
  const here = alive && wsNow() === ws && state.draftKey === key;
  const d = here ? {text: $("body").value, files: state.files} : draftsOf(ws)[key] || {text:"",files:[]};
  if (!d.text && !(d.files || []).length) {
    if (here) {
      $("body").value = text; state.mentions = mentions || []; state.mentionText = text; grow();
      state.files = files; renderPending(); if (reply) setDMReply(reply); if (answering) setAnswering(answering); keepDraft(); syncComposer();
    } else draftsOf(ws)[key] = {...d, text, files, reply, answering, mentions};
    sends.remove(id);
  }
  if (here) $("compose-error").textContent = "Not sent: " + reason + (d.text || d.files?.length ? " Your newer draft is kept. Retry the failed message above." : " Your text and files are restored.");
  else announce("Not sent: " + reason + ". Your draft is kept in its conversation.");
  if (alive) Zoom.refresh();
}

async function sendDM(retry) {
  const t = retry?.t || state.dmData;
  if ((!retry && state.sending) || !t || t.frozen || (dmHumanGuest(t) && !guestAuthor(t)) || (!retry && dmVisitor(t) && !selectedAgentPIDs().some(pid => agentOf(pid)?.can_ask))) return;
  if (!retry) {
    trackMentions();
    const text=$("body").value, mentions=(state.mentions || []).slice(), agents=selectedAgentPIDs(text,mentions);
    if (agents.length > 1) {
      if (askGone()) { kindHint(); return; }
      const files=state.files.slice(), elsewhere=boundElsewhere();
      if (elsewhere || files.length && overLimit(files)) { $("compose-error").textContent=elsewhere || overLimit(files); return; }
      const key=state.draftKey, topic=topicFresh[t.id]?sendID():topicSelections[t.id] || "", kind=kindValue()==="task"?"task":"question";
      const receiverSelection=state.replyReceiver && {...state.replyReceiver}, nativeReceiver=!!state.overview?.reply_receivers, nativeSessions=!!state.overview?.reply_sessions, receiverContext=receiverCapture();
      const group=sendID();
      const plans=agents.map((agent,index)=>({id:sendID(),key,text,reply:null,agent,files:files.map(f=>({...f,staged:index===0?f.staged:"",url:null})),kind,t,topic,mentions,receiverSelection,nativeReceiver,nativeSessions,receiverContext,fanout:true,group}));
      if (topicFresh[t.id]) {topicSelections[t.id]=topic;topicFresh[t.id]=false;}
      $("body").value="";state.mentions=[];state.mentionText="";state.dmAgent=null;state.typedFor=null;
      state.files=state.files.filter(f=>!files.includes(f));grow();renderPending();keepDraft();syncComposer();
      await Promise.all(plans.map(plan=>sendDM(plan)));
      files.forEach(f=>f.url && URL.revokeObjectURL(f.url));
      return;
    }
  }
  const {key,text,reply,agent,files,kind} = retry || {key:state.draftKey,text:$("body").value,reply:state.dmReply,agent:state.dmAgent,files:state.files.slice(),kind:kindValue()==="task"?"task":"question"};
  const topic = retry ? retry.topic : topicFresh[t.id] ? sendID() : topicSelections[t.id] || "";
  const group = retry?.group || "";
  const id = retry?.id || sendID(), mentions = retry?.mentions || (state.mentions || []).slice();
  if (!retry) trackMentions();
  const signed = encodeMentions(text, mentions); // each exact person mention as its reference; the rest as typed
  if (!retry && askGone()) { kindHint(); return; } // never sent to the person instead
  const elsewhere = retry ? "" : boundElsewhere();
  if (elsewhere) { $("compose-error").textContent = elsewhere; return; }
  if (!retry && files.length && overLimit(files)) { $("compose-error").textContent = overLimit(files); return; }
  if (typingUI) typingUI.stop();
  const host = currentHost, ws = wsNow(), receiverSelection = retry ? retry.receiverSelection : state.replyReceiver && { ...state.replyReceiver }, nativeReceiver = retry ? retry.nativeReceiver : !!state.overview?.reply_receivers, nativeSessions = retry ? retry.nativeSessions : !!state.overview?.reply_sessions, receiverContext = retry ? retry.receiverContext : receiverCapture(); // captured local delegation
  if (!retry && topicFresh[t.id]) { topicSelections[t.id] = topic; topicFresh[t.id] = false; }
  startSend(key, text, files, reply, null, t, agent ? kind : "message", () => { sends.remove(id); void sendDM({id,key,text,reply,agent,files,kind,t,topic,mentions,receiverSelection,nativeReceiver,nativeSessions,receiverContext,fanout:retry?.fanout,group}); }, id, topic, "", agent, group);
  if (alive) { syncComposer(); $("compose-error").textContent = ""; }
  try {
    await sends.ready(id);
    let r;
    const humanAsk = !!agent && (dmHumanGuest(t) || (t.guests || []).some(g => g.state === "active")); // a request with guests present names its assistant only
    const receiver = humanAsk ? null : await prepareReplyReceiverSelection(receiverSelection, host, ws, nativeReceiver, nativeSessions, receiverContext);
    const ids = await preparedFiles(files, host); // a failure here keeps what was handed over, for the retry
    try {
      r = agent ? await api("/api/dm/agent/ask", { id, send_group: group, pid: agent, kind,topic, body: signed, files: ids, ...(receiver ? { reply_receiver: receiver } : {}) }, host)
        : await api("/api/dm/send", { id, conv: t.id, topic,...(dmHumanGuest(t) ? { pid: guestAuthor(t).pid } : {}), body: signed, reply_to: reply ? reply.id : "", quote:reply?.id||"", files: ids, ...(receiver ? { reply_receiver: receiver } : {}) }, host);
    } finally { sentStaged(files); }
    announce(r.state === "receiver_waiting" ? r.detail || "Waiting for the selected reply host to accept this exact request." : r.state === "waiting" ? "Kept here, not sent yet: " + (r.detail || "they cannot read conversations now.")
      : r.state === "queued" ? "Sending…" : "Sent.");
    sends.finish(id, r);
    files.forEach(f => f.url && URL.revokeObjectURL(f.url));
    if (alive && wsNow() === ws) void loadDM();
  } catch (e) {
    if (retry?.fanout) {
      if (sends.fail(id,e.message) && alive) announce("Not sent to " + (agentOf(agent) ? agentName(agentOf(agent)) : "selected assistant") + ": " + e.message + ". Retry only this request above.");
    } else failSend(id, key, ws, text, files, reply, null, e.message, mentions);
  } finally {
    if (alive && wsNow() === ws) { state.sending = false; kindHint(); syncComposer(); if (state.newVersion) updated(state.newVersion); }
    else { state.sending = false; sentElsewhere(ws); }
  }
}

let rerenderContacts = () => {};
function rerender() {
  renderThreads(state.overview.threads);
  Zoom.refresh();
}

function renderReview(items) {
  const decisions = items.filter((it) => !it.notice && it.reason !== "agent_running");
  const working = items.filter((it) => !it.notice && it.reason === "agent_running");
  const security = items.filter((it) => it.reason === "device_admin");
  const reports = items.filter((it) => it.notice && it.reason !== "device_admin");
  const btn = $("review-btn");
  const conversations = (state.overview.needs_you || []).filter(c => ["agent_needs_human", "agent_interrupted"].includes(c.reason));
  const n = decisions.length + conversations.length;
  const total = n + reports.length + security.length + (state.overview.links || []).filter((l) => l.state === "pending").length;
  $("review-count").textContent = total;
  $("review-count").hidden = !total;
  $("review-word").textContent = n ? "Needs you" : security.length ? "Notices" : reports.length ? "Reports" : "Nothing needs you";
  $("review-reports").hidden = true;
  $("review-reports").textContent = plural(reports.length, "report", "reports");
  btn.dataset.n = n;
  btn.setAttribute("aria-label", (n === 1 ? "1 item needs your decision" : n + " items need your decision") +
    (reports.length ? ", " + plural(reports.length, "report", "reports") + " from other machines" : "") + (security.length ? ", " + plural(security.length, "company settings notice", "company settings notices") : ""));
  fill($("review-list"), ...conversations.map(c => {
    const chat = (state.overview.dms || []).find(d => d.id === c.conv);
    return el("li", {}, el("button", {type:"button", onclick:()=>{ toggleReview(false); openMessage({id:c.id,conv:c.conv}); }},
      el("strong", {}, c.reason === "agent_interrupted" ? "Your agent was interrupted — run it again if needed" : "Your agent couldn’t finish — it needs your attention"),
      el("span", {class:"review-why"}, "In " + (chat?.title || (chat?.peer ? "your chat with " + (chat.peer.label || "this person") : "a chat")))),
      c.why && el("details", {}, el("summary", {}, "Read the agent’s whole message"), el("p", {class:"agent-detail"}, c.why)),
      c.decide_on && el("p", {class:"hint"}, "Open it on " + deviceWords(c.decide_on)));
  }), ...(decisions.length ? decisions.map((it) => el("li", {},
    el("button", { type: "button", onclick: () => { toggleReview(false); openThread(it.id, it.id); } },
      el("span", {}, who(it.peer), " · ", kindTag[it.kind] || it.kind),
      el("span", { class: "review-why" }, it.why),
      el("span", { class: "review-text" }, it.excerpt)))) : conversations.length ? [] : [el("li", { class: "hint" }, "Nothing here waits for your decision.")]));
  const held = (state.overview.held || []).map(c => el("li", {},
    el("button", {type:"button", onclick:()=>{ toggleReview(false); openMessage({id:c.id,conv:c.conv}); }},
      el("strong", {}, "Asked of you"), el("span", {class:"review-text"}, c.excerpt)),
    el("p", {class:"hint"}, !c.decide_on && (c.actions || []).includes("resolve") ? "Answer in the chat, or mark it as handled on this device. Nothing runs it." : "Answer in the chat if you want to. Nothing runs it."),
    !c.decide_on && (c.actions || []).includes("resolve") && el("button", {type:"button", class:"text-btn", onclick:()=>{
      const host = currentHost;
      dialog({title:"Mark as handled?",ok:"Mark as handled",body:[el("p", {}, "This clears this item on this device. The message stays in the chat. No reply is sent.")],run:async()=>{
        if(host!==currentHost)throw Error("Workspace changed. Open the request again.");
        await act({do:"resolve",id:c.id},host); if(host===currentHost)await loadOverview();
      }});
    }}, "Mark as handled")));
  fill($("activity-extra"), remindersSection(), ...held, ...working.map(it => el("li", {},
    el("button", {type:"button", onclick:()=>{ toggleReview(false); openThread(it.id, it.id); }},
      el("strong", {}, "Working · ", who(it.peer)),
      el("span", {class:"review-why"}, it.why),
      el("span", {class:"review-text"}, it.excerpt)))), ...security.map(it => el("li", {}, el("p", {}, it.why), el("time", {datetime:it.at}, when(it.at)), el("button", {type:"button", onclick:async()=>{ await api("/api/act",{do:"resolve",id:it.id}); await loadOverview(); }}, "Hide notice"))), ...linkNotices().map((n) => el("li", {}, n)));
  const senders = [...new Set(reports.map((it) => it.peer))];
  const contacts = contactsOf(state.overview.threads);
  fill($("report-list"), ...senders.map((peer) => {
    const c = contacts.find((x) => x.peer === peer);
    return el("li", {}, c ? reportLine(c) : null);
  }));
  // Dismissed reports are history: folded away, never in the way of what
  // is current.
  const earlier = contacts.flatMap((c) => c.reports.filter((t) => !t.notices).map((t) => ({ peer: c.peer, at: t.last_at, text: t.title })))
    .sort((a, b) => new Date(b.at) - new Date(a.at));
  $("reports").hidden = !senders.length && !earlier.length;
  $("reports-earlier").hidden = !earlier.length;
  fill($("report-earlier-list"), ...earlier.map((r) => el("li", {}, el("time", { datetime: r.at }, when(r.at)), " · ", r.peer, " · ", el("span", { class: "hint" }, r.text))));
}

function toggleReview(open) {
  const btn = $("review-btn");
  const show = open ?? btn.getAttribute("aria-expanded") !== "true";
  btn.setAttribute("aria-expanded", show);
  btn.classList.toggle("selected", show);
  btn.setAttribute("aria-current", show ? "page" : "false");
  for (const name of ["chats", "people"]) {
    const active = !show && (name === "people" ? state.contactView === "people" : state.contactView !== "people");
    $("nav-" + name).classList.toggle("selected", active);
    $("nav-" + name).setAttribute("aria-current", active ? "page" : "false");
  }
  $("review").hidden = !show;
  $("conv-list").hidden = show;
  $("search").hidden = show;
  $("section-title").textContent = show ? "Activity" : state.contactView === "people" ? "People" : "Chats";
  if (show) {
    root.classList.remove("show-conv");
  }
  $("draft-state").hidden = !show; $("zoom").hidden = show;
  if (show) $("review").querySelector("button")?.focus();
}

// The everyday list shows recent people, not every device and conversation.
// Directory browsing is explicit; search still covers everything held here.
function sidebarEntries(threads) {
  const o = state.overview || {}, dms = o.dms || [], contacts = contactsOf(threads);
  const entries = (o.people || []).map((p) => {
    const chats = dms.filter((d) => personKey(d.peer) === personKey(p)).sort((a, b) => new Date(b.last_at) - new Date(a.last_at));
    const devices = contacts.filter((c) => devicesOf(p).some((d) => d.address === c.peer));
    const times = [...chats.map((d) => d.last_at), ...devices.map((c) => c.lastAt)].filter(Boolean);
    return { person: p, chats, label: p.label, at: times.sort((a, b) => new Date(b) - new Date(a))[0],
      unread: chats.reduce((n, d) => n + (d.unread || 0), 0) + devices.reduce((n, c) => n + c.unread, 0) };
  });
  for (const c of contacts.filter((c) => !deviceOwner(c.peer) || (state.contactView !== "people" && deviceOwner(c.peer) === o.person))) {
    entries.push({ contact: c, label: c.peer, at: c.lastAt, unread: c.unread });
  }
  return entries.filter((e) => state.contactView === "people" || (state.contactView === "unread" ? e.unread > 0 : !!e.at))
    .sort(state.contactView === "people" ? (a, b) => a.label.localeCompare(b.label)
      : (a, b) => new Date(b.at) - new Date(a.at) || a.label.localeCompare(b.label));
}

// Message/list presentation belongs to Zoom below.

function contactControls() {
  return el("div", { role: "group", "aria-label": "Browse conversations", class: "contact-modes" },
    Object.entries(state.contactView === "people" ? {} : { recent: "Recent", unread: "Unread" }).map(([key, label]) =>
      el("button", { type: "button", "aria-pressed": String(state.contactView === key),
        onclick: () => { state.contactView = key; state.contactLimit = 20; rerenderContacts(); } }, label)));
}

function moreContacts(total) {
  return total > state.contactLimit && el("button", { type: "button", class: "chip more-contacts",
    onclick: () => { state.contactLimit += 20; rerenderContacts(); } }, "Show more · " + (total - state.contactLimit) + " remaining");
}

function renderThreads() { rerenderContacts = rerender; }

// searchItems lists what a search finds; each result opens through the
// sidebar handler, so it lands where it is.
function searchItems(q, threads, open) {
  const { people, dms, agents, conversations, listed } = searchKnown(q, threads, directory());
  const shown = conversations.slice(0, 30);
  const kind = (t) => t.count > 1 ? "Conversation" : "Message";
  if (!people.length && !dms.length && !agents.length && !conversations.length && !listed.length) {
    return [el("li", { class: "hint empty-list" }, "No person, agent or conversation matches."),
      directoryNote(directory()) && el("li", { class: "hint dir-note" }, directoryNote(directory()))];
  }
  return [
    people.length > 0 && el("li", { class: "result-head" }, plural(people.length, "person", "people")),
    ...people.map((p) => el("li", {}, el("button", { type: "button", class: "result", onclick: (e) => open.person(p, e.currentTarget) },
      el("span", { class: "result-kind" }, "Person"),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, p.label, p.person && el("span", { class: "hint", title: "Person ID: " + p.person }, " · @" + p.person.slice(0, 8))),
        el("span", { class: "hint" }, devicesText(p) + " · " + (personStateText[p.state] || p.state)))))),
    dms.length > 0 && el("li", { class: "result-head" }, dms.some(humanGroup) ? plural(dms.length, "conversation", "conversations") : plural(dms.length, "DM", "DMs")),
    ...dms.map((d) => el("li", {}, el("button", { type: "button", class: "result", onclick: (e) => open.dm(d, e.currentTarget) },
      el("span", { class: "result-kind" }, humanGroup(d) ? "Group" : "DM"),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, d.title || "No messages yet"),
        el("span", { class: "hint" }, "with " + d.peer.label + " · " + when(d.last_at))), dmFlags(d)))),
    agents.length + listed.length > 0 && el("li", { class: "result-head" }, plural(agents.length + listed.length, "device", "devices")),
    ...agents.map((c) => el("li", {}, el("button", { type: "button", class: "result", onclick: (e) => open.contact(c, e.currentTarget) },
      el("span", { class: "result-kind" }, runsAgent(c.peer) ? "Agent" : "Person"), el("span", { class: "result-main" }, who(c.peer),
        el("span", { class: "hint" }, " · " + plural(c.conversations.length, "conversation", "conversations"))), presenceBadge(c.peer), counts(c)))),
    ...listed.map(memberRow),
    conversations.length > 0 && el("li", { class: "result-head" }, plural(conversations.length, "conversation or message", "conversations or messages")),
    ...shown.map((t) => el("li", {}, el("button", { type: "button", class: "result", onclick: (e) => open.conversation(t, e.currentTarget) },
      el("span", { class: "result-kind" }, kind(t)),
      el("span", { class: "result-main" }, el("span", { class: "result-title" }, t.title),
        el("span", { class: "hint" }, "with ", t.peer, " · ", when(t.last_at))), threadFlag(t)))),
    conversations.length > shown.length && el("li", { class: "hint result-more" }, (conversations.length - shown.length) + " more: type more to narrow the search.")];
}

function renderSearch(threads) {
  $("list-title").textContent = "Search results";
  fill($("conv-list"), ...searchItems(state.query, threads, {
    person: (p) => choosePerson(p),
    dm: (d) => { clearSearch(); openDM(d.id); },
    contact: (c) => { clearSearch(); openHub({ kind: "device", key: c.peer }); },
    conversation: (t) => { clearSearch(); openThread(t.id); },
  }));
}

function clearSearch() {
  state.query = "";
  $("search").value = "";
  rerenderContacts();
}

// Why a received message is held back, worded from its code
// (QuarantineItem.code) with the sender from who(), never from reason, which
// names the address. One that didn't verify only claims who sent it.
const heldWords = {
  invalid: () => "This message failed a check and was kept out of the chat. Its contents stay hidden and it cannot start any work.",
  key_changed: (p) => [p, "'s key changed. It waits until you check and trust the new one."],
  proof_pending: () => "It names a conversation or person this device can't check yet. It waits here; nothing runs it.",
  identity_conflict: (p) => ["It disagrees with the person record kept here for ", p, ". It stays held; nothing runs it."],
  conflicting_duplicate: (p) => [p, " sent different content under a message already received. It stays held; nothing runs it."],
};

function renderQuarantine(items) {
  const box = $("quarantine");
  box.hidden = !items.length;
  if (!items.length) return;
  $("quarantine-summary").textContent = items.length === 1 ? "1 message held back" : items.length + " messages held back";
  fill($("quarantine-list"), ...items.map((q) => {
    const known = Object.hasOwn(heldWords, q.code || "");
    return el("li", {},
      el("span", {}, q.code === "invalid" ? "A message that couldn't be accepted" : known ? who(q.peer) : ["Unverified, says it's from ", who(q.peer)], " · ", when(q.at)),
      el("span", { class: "hint" }, q.detail || (known ? heldWords[q.code](who(q.peer)) : "It couldn't be verified, so it isn't shown.")),
      q.recovery && el("span", { class: "hint" }, q.recovery),
      q.can_archive && el("button", { type: "button", class: "text-btn", onclick: async e => {
        const button=e.currentTarget;button.disabled=true;
        try { const host=currentHost; await act({do:"archive_held",id:q.id},host); if(host===currentHost)await refetch(false); } catch(err) { announce(err.message); } finally { if(button.isConnected)button.disabled=false; }
      } }, "Archive notice"));
  }));
}

// ---- thread -----------------------------------------------------------------

// beginThread starts showing the thread holding message id, from any view.
// The composer's draft stays with the conversation it was written in, and
// nothing can be sent until the new one has loaded. It reports whether the
// conversation changed.
function beginThread(id) {
  state.messageOpening = null;
  closeMentions();
  const changed = !!state.dm || !state.data || !state.data.messages.some((m) => m.id === id);
  if (changed) {
    clearTyping();
    keepDraft();
    state.files = [];
    renderPending();
    releaseOpened();
    state.data = null;
    state.draftKey = null;
    state.deviceAgentID = "";
    state.replyReceiver = null;
    state.replyReceiverHost = null;
    state.receiverCatalog = null;
    state.targetCatalog = null;
    state.targetCatalogSeq = (state.targetCatalogSeq || 0) + 1;
    $("body").value = "";
    grow();
    setKind("message");
    setAnswering(null);
    setDMReply(null);
    setDMAgent(null);
  }
  state.dm = null;
  state.dmData = null;
  state.thread = id;
  $("agents").hidden = true; $("agents").classList.remove("group-agents");
  return changed;
}

async function openThread(id, focusId) { await Zoom.go(2, {thread: id}); if (focusId) await Zoom.go(3, {msg: focusId}); }

function flash(id) { void Zoom.go(3, {msg: id}); }

// refreshThread asks the network once, when a thread is opened: presence,
// and receipts for messages the server still holds. Not repeated.
async function refreshThread() {
  const id = state.thread, gen = state.gen;
  try {
    const p = await api("/api/refresh", { id });
    if (state.thread !== id || gen !== state.gen || !state.data) return;
    state.presence[state.data.peer] = { text: p.text, at: p.at };
    $("conv-presence").textContent = peerPresence(state.data.peer);
  } catch (e) { /* presence stays unknown */ }
}

async function loadThread(scrollToEnd) {
  const id = state.thread, gen = state.gen;
  if (!id) return;
  let t;
  try {
    t = await api("/api/thread?id=" + encodeURIComponent(id));
  } catch (e) {
    announce(e.message);
    return;
  }
  if (state.thread !== id || gen !== state.gen) return; // another thread or workspace was opened meanwhile
  state.data = t;
  fill($("conv-name"), who(t.peer));
  $("conv-topic").textContent = firstLine(t.messages[0].body, 90);
  $("conv-avatar").replaceWith(Object.assign(avatar(t.peer), { id: "conv-avatar" }));
  $("conv-presence").textContent = peerPresence(t.peer);
  renderPeerChips(t);
  renderNotice(t);
  showPane("conv");
  $("composer").hidden = false;
  setHubBack();
  const page=t.topic ? await api("/api/topics?peer="+encodeURIComponent(t.peer)+"&limit=200") : {topics:[]};
  if(state.thread!==id||gen!==state.gen)return;
  topicUI.update({peer:t.peer},page.topics,t.topic?.id||id);
  renderBody(scrollToEnd);
  const key = t.messages[0].id; // a conversation's first message names its draft
  if (state.draftKey === null) restoreDraft(key, t); // just switched here (beginThread)
  state.draftKey = key;
  if (state.deviceAgentID && !state.targetCatalog) loadTargetCatalog();
  syncComposer();
  const unread = t.messages.filter((m) => m.unread).map((m) => m.id);
  if (unread.length) api("/api/act", { do: "read", ids: unread }).catch(() => {});
  await refreshTyping();
}

// Zoom owns its own device-thread presentation.
function renderBody() { /* Zoom renders after the bound load completes. */ }

// Zoom owns one presentation; interface selection belongs to the host.
function showZoom() { $('draft-state').hidden = true; $('zoom').hidden = false; Zoom.refresh(); }

function renderPeerChips(t) {
  const chips = [];
  chips.push(el("button", { type: "button", class: "peer-chip" + (t.approved ? " on" : ""),
    title: t.approved ? "Their questions are answered automatically by your responder" : "Their questions wait for you",
    onclick: () => approvalDialog(t) }, t.approved ? "I answer their questions" : "Their questions wait for me"));
  if (t.task_grant) {
    chips.push(el("button", { type: "button", class: "peer-chip on", title: (t.task_target === t.permission_person?.person ? "Tasks from this person’s current verified devices run without asking: " : "Tasks from this exact key run without asking: ") + t.task_grant,
      onclick: () => revokeDialog(t) }, t.task_grant === "active" ? (t.task_target === t.permission_person?.person ? "Tasks: always (this person)" : "Tasks: always (this key)") : "Tasks: grant on hold"));
  }
  fill($("peer-chips"), ...chips);
}

function renderNotice(t) {
  const n = $("notice");
  if (!t.key.pending) { n.hidden = true; fill(n, ); return; }
  n.hidden = false;
  fill(n, el("p", {}, t.peer + "'s key changed. Their new messages are held and sending is blocked until you trust the new key."),
    el("button", { type: "button", class: "btn", onclick: () => trustDialog(t) }, "Compare keys…"));
}

// Messages from the same author within a few minutes form one group.
function continues(m, prev) {
  return prev && prev.dir === m.dir && prev.agent_id === m.agent_id && (prev.target && prev.target.agent_id) === (m.target && m.target.agent_id) && !kindTag[m.kind] && !m.status && prev.author.label === m.author.label &&
    !(prev.actions && prev.actions.length) && !prev.summary && new Date(m.at) - new Date(prev.at) < 10 * 60e3;
}

const decisionActions = ["accept", "accept_always", "decline", "approve", "resolve", "reply", "do_it"];

// ---- message controls: reactions, edits, deletion (MEL-476, MEL-477, R08) ----
//
// A message carries what controls did to it, as this device resolves them:
// reactions [{emoji, by, mine}], edited + text (the shown text; body stays
// what was sent or admitted), deleted, and can (react, edit, delete). The
// page shows exactly that: an edit changes the text on screen, never what
// an agent already got; a deletion hides text and files here and on
// devices that can read one, and undoes nothing else.

const shownText = (m) => (m.deleted ? "" : m.edited && typeof m.text === "string" ? m.text : m.body);
// A reactor is {id, label} (a person, or a device address): two people with
// one label stay two entries; the label alone is never an identity. An
// assistant's own reaction (assistant: true, with host, agent_id, pid) is
// named as its participant is (agentName), or in a device thread by its
// verified catalog name, else plainly (namedAgentLabel). Its exact identity is
// in the title, never in the text.
const assistantReactorLabel = (b) => {
  const a = b.pid && agentOf(b.pid);
  if (a) return agentName(a); // exactly as the participant is named here
  return b.agent_id ? namedAgentLabel(b.agent_id, b.host) : "Assistant on " + b.host;
};
const assistantReactorTitle = (b) => assistantReactorLabel(b) + " · " + (b.agent_id ? "agent " + b.agent_id : "default responder") + " on " + b.host + (b.pid ? " · participation " + b.pid : "");
const reactorLabel = (b) => (typeof b === "string" ? b : b && b.assistant ? assistantReactorLabel(b) : (b && b.label) || (b && b.id) || "someone");
const reactorNames = (r) => (r.by || []).map(reactorLabel).join(", ");
const quickEmoji = ["👍", "❤️", "😂", "🎉", "👀", "✅",
  "🙏", "👏", "🔥", "😊", "😍", "🤔",
  "😮", "😢", "😡", "👎", "💯", "🚀",
  "🙌", "😅", "🤝", "💪", "👌", "❌"];
const canDo = (m, what) => !m.excerpt_pid && !(state.dm && (dmVisitor() || dmHumanGuest())) && Array.isArray(m.can) && m.can.includes(what);
const controlRef = (m, conv) => (conv ? { conv, id: m.id, dir: m.dir } : { id: m.id, dir: m.dir });

async function control(what, body, done, countReaction = false) {
  const host = currentHost, gen = state.gen; // the membership the message is in
  try {
    const r = await api("/api/message/" + what, body, host);
    if (countReaction) recordReaction(body.emoji);
    announce(r.note || "Done.");
    if (gen !== state.gen) { if (done) done(null); return; }
    if (state.dm) await loadDM(false); else await loadThread(false);
    if (done) done(null);
  } catch (e) {
    announce(e.message);
    if (done) done(e);
  }
}

// reactionsRow: each emoji with its count and who; yours toggles off, the
// others' add yours. A "+" opens the quick picker.
function reactionsRow(m, conv) {
  if (m.deleted) return null;
  const can = canDo(m, "react");
  const chips = humanReactionOrder(m.reactions || []).flatMap((r) => {
    // People's marks toggle as one chip; each assistant's own mark is its
    // own chip, named, so it never reads as a person's (or its host's).
    const people = { ...r, by: (r.by || []).filter((b) => !b.assistant) }, agents = (r.by || []).filter((b) => b.assistant);
    const label = r.emoji + " " + people.by.length;
    const title = reactorNames(people);
    const own = !people.by.length ? [] : [can ? el("button", { type: "button", class: "reaction" + (r.mine ? " mine" : ""), title, "aria-label": r.emoji + " by " + title + (r.mine ? " (you); press to remove yours" : "; press to add yours"),
      onclick: () => control("react", { ...controlRef(m, conv), emoji: r.emoji, remove: !!r.mine }, undefined, !r.mine) }, label)
      : el("span", { class: "reaction" + (r.mine ? " mine" : ""), title }, label)];
    return own.concat(agents.map((b) => {
      const name = assistantReactorLabel(b);
      return el("span", { class: "reaction assistant-reaction", title: assistantReactorTitle(b), "aria-label": r.emoji + " by " + name + ", an assistant (" + assistantReactorTitle(b) + ")" }, r.emoji, el("span", { class: "reaction-agent" }, name));
    }));
  });
  if (!chips.length && !can) return null;
  return el("div", { class: "reactions" }, chips, can && reactPicker(m, conv));
}

// reactPicker: the quick reactions, a bottom sheet on a phone and a popup
// anchored to "+" on a wide screen: upward when only the timeline above has
// room, otherwise downward, scrolled fully into view. Close, Escape or a tap
// outside (phone) dismisses it.
function reactPicker(m, conv) {
  const d = el("details", { class: "react-pick" });
  const close = () => { d.open = false; d.querySelector("summary").focus(); };
  const send = (emoji) => {
    d.open = false;
    const alreadyMine = (m.reactions || []).some(r => r.emoji === emoji && r.mine);
    control("react", { ...controlRef(m, conv), emoji }, undefined, !alreadyMine);
  };
  d.addEventListener("keydown", (e) => { if (e.key === "Escape" && d.open) { e.preventDefault(); close(); } });
  d.addEventListener("toggle", () => {
    const menu = d.querySelector(".react-menu");
    if (!d.open || getComputedStyle(menu).position === "fixed") return;
    const box = (d.closest(".timeline") || root).getBoundingClientRect(), r = d.getBoundingClientRect(), h = menu.offsetHeight + 8;
    d.classList.toggle("up", box.bottom - r.bottom < h && r.top - box.top >= h);
    menu.scrollIntoView({ block: "nearest" });
  });
  d.append(el("summary", { "aria-label": "Add a reaction", title: "Add a reaction" }, "＋"),
    el("div", { class: "react-menu", role: "group", "aria-label": "Choose a reaction" },
      el("div", { class: "react-head" }, el("strong", {}, "Choose a reaction"), el("button", { type: "button", class: "react-close", "aria-label": "Close", onclick: close }, "✕")),
      el("div", { class: "react-grid" }, reactionChoices(quickEmoji).map((e) => el("button", { type: "button", "aria-label": "React " + e, onclick: () => send(e) }, e)))));
  return d;
}

// messageMenu: what you may do to a message, reachable by touch and
// keyboard (a details element, no hover): edit or delete your own, react.
function messageMenu(m, conv, bubble) {
  if (m.deleted) return null;
  const items = [];
  if (canDo(m, "edit")) items.push(el("button", { type: "button", onclick: (e) => { e.currentTarget.closest("details").open = false; editInPlace(m, conv, bubble); } }, "Edit"));
  if (canDo(m, "delete")) items.push(el("button", { type: "button", onclick: (e) => { e.currentTarget.closest("details").open = false; deleteDialog(m, conv); } }, "Delete…"));
  if(conv&&!m.topic&&!m.topic_event&&state.dmData&&!state.dmData.frozen&&!dmVisitor(state.dmData)&&!m.excerpt_pid)items.push(el("button",{type:"button",onclick:async()=>{try{await api("/api/topic/create",{conv,peer:"",id:m.lid||m.id});topicSelections[conv]=m.lid||m.id;await loadDM(false);await Zoom.go(2,{});}catch(e){announce(e.message);}}},"Make a topic"));
  if (!items.length) return null;
  return el("details", { class: "msg-menu" }, el("summary", { "aria-label": "Message actions", title: "Message actions" }, "⋯"), el("div", { class: "menu-items" }, items));
}

// editInPlace replaces the bubble's text with a box: Save sends a revision,
// Cancel keeps everything. A question or task says that its agent keeps
// what it already got.
function editInPlace(m, conv, bubble) {
  const p = bubble.querySelector(".body");
  if (!p || bubble.querySelector(".edit-box")) return;
  const box = el("div", { class: "edit-box" });
  const ta = el("textarea", { rows: "3", "aria-label": "New text" });
  const decoded = decodeMentions(shownText(m)); // edited as @Name; each untouched mention keeps its exact reference
  let spans = decoded.spans, edited = decoded.text;
  ta.value = decoded.text;
  const err = el("p", { class: "error", role: "alert" });
  ta.addEventListener("input", () => {
    const r = shiftMentions(spans, edited, ta.value);
    spans = r.kept; edited = ta.value;
    if (r.dropped.length) err.textContent = r.dropped.map(s => "@" + s.name).join(", ") + " is no longer an exact mention; the text stays.";
  });
  const cancel = () => { box.replaceWith(p); };
  box.append(ta,
    (m.kind === "question" || m.kind === "task") && el("p", { class: "hint" }, "Editing changes the text shown here and on their devices. What their agent already received stays as sent; nothing runs again."),
    el("div", { class: "edit-actions" },
      el("button", { type: "button", class: "btn primary", onclick: () => {
        if (!ta.value.trim()) { err.textContent = "Write the new text first."; return; }
        const text = encodeMentions(ta.value, spans).trim();
        control("edit", { ...controlRef(m, conv), text }, (e) => { if (e) err.textContent = e.message; });
      } }, "Save"),
      el("button", { type: "button", class: "btn", onclick: cancel }, "Cancel")), err);
  p.replaceWith(box);
  ta.focus();
}

function deleteDialog(m, conv) {
  dialog({ title: "Delete this message?", ok: "Delete",
    body: [el("div", { class: "quote" }, mentionPlain(shownText(m)) || "(files only)"),
      el("p", {}, "It is removed here and on devices that can read deletions. Anyone who already read it, saved its files or gave it to an agent keeps what they have; nothing that is running is stopped."),
      (m.kind === "question" || m.kind === "task") && el("p", { class: "hint" }, "What was sent to their agent stays on record under Details.")],
    run: async () => { await control("delete", controlRef(m, conv)); } });
}

// bodyOf is the message's text as shown: the edit, a deleted marker, or the body.
// reportText is what a report message says, in a line: the version 2
// report's count and host (deciding is in Activity), or the older count text.
function reportText(m) {
  try {
    const v = JSON.parse(m.body);
    if (v && v.v === 2 && Array.isArray(v.items)) return plural(v.items.length || v.count || 0, "request", "requests") + " waiting on " + (v.host || m.from) + " at " + when(v.at) + ". See Activity for what they are" + (v.items.some((x) => x.actionable) ? " and to decide them from here." : ".");
  } catch (e) { /* the older count text */ }
  return mentionPlain(shownText(m));
}
// lineOf is a message's first line as the page names it: a report by what it says.
const lineOf = (m, n) => firstLine(isReport(m) ? reportText(m) : m.body || "(files)", n);

function bodyOf(m) {
  if (m.deleted) return el("p", { class: "body tombstone" }, "Message deleted");
  if (isReport(m)) return el("p", { class: "body report-body" }, reportText(m)); // a report from another machine: what it says, never its raw record
  const target = (m.kind === "question" || m.kind === "task") && m.pid ? agentOf(m.pid) : null;
  const recipient = (m.kind === "question" || m.kind === "task") && m.pid && el("span", {class:"addressed-agent", "data-agent-recipient":"", title:m.pid}, "To @" + (target ? agentName(target) : "agent") + "\n");
  if (confirmedTask(m)) return approvalBody(m, recipient);
  return el("p", { class: "body" }, recipient, messageNodes(shownText(m)), m.edited && el("span", { class: "edited", title: "Revision " + (m.revision || "") }, " · edited"));
}

// Only the signed projection's bound confirmation is compact, never matching text.
function confirmedTask(m) { return m.kind === "task" && !!m.proposal?.proposal_id; }
function approvalBody(m, recipient = null) {
  const target = m.pid && agentOf(m.pid);
  const messages = (state.dm ? state.dmData : state.data)?.messages || [];
  const matches = messages.filter(x => x.id === m.proposal.proposal_id || x.lid === m.proposal.proposal_id);
  const original = matches.length === 1 && !matches[0].deleted ? matches[0] : null;
  return el("div", {class:"body compact-approval"}, recipient,
    el("p", {"data-proposal-provenance":""}, (m.proposal.edited ? (m.dir === "out" ? "You edited and sent this task" : "Edited and sent this task") : (m.dir === "out" ? "You approved this task" : "Approved this task")) + " · suggested by " + (target ? agentName(target) : "the agent")),
    original && el("button", {type:"button",class:"text-btn",onclick:()=>flash(original.id)}, "View proposal"),
    el("details", {"data-approved-task-text":""}, el("summary", {}, "Approved task text"),
      el("p", {}, mentionNodes(m.body, state.dm ? state.dmData : null)),
      m.edited && el("p", {}, "Edited message: ", mentionNodes(shownText(m), state.dm ? state.dmData : null))));
}

// controlDetails are the rows Details adds: the original text of an edited
// message, and what was sent of a deleted question or task (on record;
// never shown in the tombstone).
function controlDetails(m) {
  const rows = [];
  if (m.edited && !m.deleted) rows.push(el("dt", {}, "Original text"), el("dd", {}, mentionPlain(m.body)));
  if (m.deleted && (m.kind === "question" || m.kind === "task")) rows.push(el("dt", {}, "What was sent to their agent"), el("dd", {}, mentionPlain(m.body)));
  if (m.revision) rows.push(el("dt", {}, "Revision"), el("dd", { class: "mono" }, String(m.revision)));
  return rows;
}

// ---- headless: a host's word on a request, reports from other machines,
// decisions by a granted operator (R05/R06/R09, MEL-497/426/435) ----
//
// exec is what the executing host asserted about one request (signed by
// that host, resolved by core): never derived from delivery, presence or
// time. Delivery facts stay in the footer as before. An old status is
// said to be old.

const execWord = { steered: "Accepted into current run", queued: "Queued there", awaiting: "Waiting for their acceptance", running: "Running", needs_human: "Needs a person there", stopped: "Stopped",
  not_run: "Not run", declined: "Declined there", failed: "Failed there", cancelled: "Cancelled there", interrupted: "Interrupted there",
  // a host's own state names, as its report shows them (client.stateXxx)
  held: "Waiting for approval there", pending: "Queued there", accepted: "Accepted there", resolved: "Closed there", answered: "Answered there", cancel_requested: "Stopping there" };
function ago(iso) {
  const s = Math.max(0, (Date.now() - asDate(iso)) / 1000);
  return s < 90 ? "just now" : s < 5400 ? Math.round(s / 60) + " min ago" : s < 172800 ? Math.round(s / 3600) + " h ago" : Math.round(s / 86400) + " d ago";
}
function execLine(m, t) {
  const e = m.exec;
  if (!e || !e.state) return null;
  // The terminal answer or result in this thread supersedes the host's last
  // word on the request: once it is answered, nothing is "running" for it.
  if (t && (t.messages || []).some((x) => x.reply_to === m.id && (x.kind === "answer" || x.kind === "result"))) return null;
  const word = e.state === "running" ? "Working" : execWord[e.state] || e.state;
  const detail = e.detail && (blockerWord[e.detail] || e.detail); // a blocker category, or the host's bounded plain reason
  const text = word + " on " + e.host + (e.state === "running" && e.at ? " since " + when(e.at) : "") + (detail ? ": " + detail : "");
  return el("span", { class: "exec " + e.state + (e.stale ? " stale" : ""), title: "Asserted by " + e.host + (e.at ? " at " + new Date(e.at).toLocaleString() : "") },
    e.state === "running" && !e.stale && el("span", {class: "working-dots", "aria-hidden": "true"}, "···"), text, e.stale && el("span", { class: "exec-stale" }, " · last known " + (e.at ? ago(e.at) : "earlier") + ", not confirmed now"),
    e.refused && el("span", { class: "exec-refused" }, " · refused: " + e.refused));
}

// A version 2 report: what a machine said about its own requests at that
// time. Actions appear only for items core marked actionable (this device's
// key holds that host's operator grant); everything else is read-only.
const blockerWord = { awaiting_acceptance: "waits for acceptance", question_not_approved: "asks from a sender not approved for automatic answers", needs_human: "needs a person's decision", running: "running", seems_stuck: "seems stuck" };
function proposalCard(p) {
  if (!p || !p.proposal_id) return null;
  const name = (address) => deviceOwner(address)?.label || String(address || 'Someone').split('/')[0].replaceAll('-', ' ');
  return el("details", { class: "tech" }, el("summary", {}, "How this task was chosen"),
    el("p", { style: "white-space:pre-wrap;overflow-wrap:anywhere" }, name(p.asker) + " asked: " + p.question),
    el("p", { style: "white-space:pre-wrap;overflow-wrap:anywhere" }, "Your agent suggested: " + p.proposal),
    el("p", {}, name(p.confirmed_by) + (p.edited ? " edited and sent this task." : " chose Do it.") + " This uses only their usual task approval."));
}

function reportItems(it) {
  const r = it.report;
  if (!r || !Array.isArray(r.items)) return null;
  return el("ul", { class: "report-requests" }, r.items.map((x) => {
    // After a decision the host applied, the state this snapshot shows no
    // longer holds: no further decision is offered on it (the host's next
    // report names what waits now); a refusal leaves the choice open.
    const decided = x.result && !x.result.refused;
    const acts = x.actionable && !decided ? decisionButtons(it, x) : null;
    const res = x.result && el("p", { class: "hint" }, x.result.refused ? "Refused: " + x.result.refused
      : "Now " + (execWord[x.result.state] || x.result.state) + (x.result.at ? " (" + when(x.result.at) + ")" : "") + ", after your decision from here. The host's next report says what waits there now.");
    return el("li", { class: "report-request" + (x.actionable ? " actionable" : "") },
      el("div", { class: "report-head" }, el("strong", {}, kindTag[x.kind] || x.kind || "request"), " from ", who(x.from || "?"), " · ", (execWord[x.state] || x.state || "state unknown"),
        x.blocker && el("span", { class: "hint" }, " · " + (blockerWord[x.blocker] || x.blocker)), x.since && el("span", { class: "hint" }, " · since " + when(x.since))),
      x.excerpt ? (x.state === "needs_human" ? el("details", {}, el("summary", {}, "Read all available detail"), el("p", {class:"agent-detail"}, x.excerpt)) : el("p", { class: "report-excerpt" }, x.excerpt)) : el("p", { class: "hint" }, "Its text is not shared with this device."),
      proposalCard(x.proposal),
      el("p", { class: "hint mono" }, "Request " + x.id.slice(0, 8) + "… on " + r.host),
      res, acts);
  }));
}

const decisionWord = { accept: "Accept and run there…", decline: "Decline there…", reply: "Answer it there…", resolve: "Close it there…", cancel: "Stop it there…" };
// The decisions a host takes, per the state it reported (client.admitDecision):
// accept runs a task nobody accepted, answers a held question automatically,
// or runs again one that stopped; reply and decline take it over by hand;
// resolve closes what needs a person; cancel stops a run. The host still
// refuses anything not possible when it looks.
function decisionButtons(it, x) {
  const by = { awaiting: ["accept", "decline"], held: ["accept", "reply", "decline"], needs_human: ["reply", "resolve"], running: ["cancel"],
    interrupted: ["accept"], failed: ["accept"], cancelled: ["accept"] };
  const again = ["interrupted", "failed", "cancelled"].includes(x.state); // accept there means: run it again
  // A DM or group request (conv) is decided here, never answered by hand.
  const acts = (x.actions && x.actions.length ? x.actions : by[x.state] || []).filter((a) => !(x.conv && a === "reply"));
  if (!acts.length) return null;
  return el("div", { class: "acts" }, acts.map((a, i) => el("button", { type: "button", class: "act" + (i === 0 ? " go" : ""), onclick: () => operatorDialog(it, x, a) }, a === "accept" && again ? "Run it again there…" : decisionWord[a] || a)));
}

// operatorDialog sends one decision to the host, bound to the state and
// attempt the operator saw; the host answers with the real resulting
// state (or a refusal), shown on the item when it arrives.
function operatorDialog(it, x, action) {
  const host = it.report.host;
  const needText = action === "reply" || action === "decline";
  const ta = needText ? el("textarea", { id: "decide-text", rows: "3", "aria-label": action === "reply" ? "Your answer" : "Why" }) : null;
  dialog({ title: (decisionWord[action] || action).replace(/…$/, "") + " on " + host + "?", ok: decisionWord[action] ? decisionWord[action].replace(/ there…$/, "") : action,
    body: [el("p", {}, "The " + (kindTag[x.kind] || x.kind || "request").toLowerCase() + " from " + (x.from || "?") + ", as " + host + " reported it at " + when(it.report.at) + " (" + (execWord[x.state] || x.state) + ")."),
      x.excerpt && el("div", { class: "quote" }, x.excerpt),
      ta, el("p", { class: "hint" }, "The host applies this only if the request is still in that state; you see the real outcome here. Nothing runs on this computer.")],
    run: async () => {
      const body = { host, id: x.id, key: x.key, action, expect: x.state, attempt: x.attempt, text: ta ? ta.value.trim() : "", report: it.id };
      if (needText && !body.text) throw new Error(action === "reply" ? "Write the answer first." : "Say why, in a few words.");
      const r = await api("/api/operator/decide", body);
      announce(r.note || "Sent to " + host + ".");
      await loadOverview();
    } });
}

// openMessage lands on one exact message (a notification's click, #msg=ID
// with an optional conv and dir): its conversation opens and it is flashed.
// Nothing is sent, accepted or read for the person by landing.
// Resolve the exact displayed row before choosing its topic. A delayed load
// cannot pull the person back after another navigation or workspace switch.
function messageTarget(messages, ref) {
  const rows = messages.filter(m => !ref.dir || m.dir === ref.dir);
  const exact = rows.find(m => m.id === ref.id);
  if (exact) return exact;
  const logical = rows.filter(m => m.lid === ref.id);
  return logical.length === 1 ? logical[0] : undefined;
}
// Existing push-driven loads resume a missing exact target. No timer or fetch
// loop is added; explicit topic or conversation navigation cancels this intent.
function resumeMessageFocus(t) {
  const pending = state.messageOpening;
  if (!pending?.waiting || pending.conv !== t.id) return "";
  const message = messageTarget(t.messages, pending.ref);
  if (!message) return "";
  state.messageOpening = null;
  topicSelections[t.id] = message.topic || "";
  topicFresh[t.id] = false;
  return message.id;
}
async function openDMMessage(conv, ref) {
  const pending = {conv, ref, waiting:false}; state.messageOpening = pending;
  try {
    const opening = openDM(conv), gen = state.gen, host = currentHost;
    const selected = topicSelections[conv]; const generation = Zoom.generation;
    await opening;
    if (!alive || state.messageOpening !== pending || host !== currentHost || gen !== state.gen || state.dm !== conv || topicSelections[conv] !== selected || generation !== Zoom.generation) return;
    const message = messageTarget(state.dmData?.messages || [], ref);
    if (!message) { pending.waiting = true; announce("That message is not on this device yet. It will open when its history arrives."); return; }
    topicSelections[conv] = message.topic || "";
    topicFresh[conv] = false;
    renderDMBody(false);
    flash(message.id);
    if (state.messageOpening === pending) { state.messageOpening = null; void markDMRead(); }
  } finally {
    if (state.messageOpening === pending && !pending.waiting) state.messageOpening = null;
  }
}
async function openMessage(ref) {
  if (!ref || !ref.id) return;
  if (ref.conv && (state.overview.dms || []).some(d => d.id === ref.conv)) return openDMMessage(ref.conv, ref);
  if (!ref.conv) {
    try { await openThread(ref.id, ref.id); if (state.data && state.data.messages.some(m => m.id === ref.id)) return; } catch (e) { /* not a device thread here */ }
  }
  const gen = state.gen, host = currentHost;
  for (const d of state.overview.dms || []) {
    try {
      const v = await api("/api/dm?id=" + encodeURIComponent(d.id));
      if (!alive || gen !== state.gen || host !== currentHost) return;
      if (messageTarget(v.messages, ref)) return openDMMessage(d.id, ref);
    } catch (e) { /* next */ }
  }
  if (alive && gen === state.gen && host === currentHost) showList("That message is not on this device.");
}

// Message/list presentation belongs to Zoom below.

function details(m) {
  return el("details", { class: "tech" }, el("summary", {}, "Details"),
    el("dl", {},
      el("dt", {}, "Written by"), el("dd", {}, m.author.about),
      namedDetails(m),
      el("dt", {}, "Sent"), el("dd", {}, new Date(m.sent_at || m.at).toLocaleString()),
      el("dt", {}, "Message id"), el("dd", { class: "mono" }, m.id),
      el("dt", {}, "Kind"), el("dd", {}, m.kind),
      m.delivery && [el("dt", {}, "Delivery"),el("dd",{},deliveryText(m))],
      m.delivery === "waiting" && m.detail && [el("dt", {}, "Waiting for"), el("dd", { class: "mono" }, m.detail.replace(/^peer_update:\s*/, ""))],
      m.delivery !== "waiting" && m.detail && [el("dt", {}, "Send detail"), el("dd", { class: "mono" }, m.detail)],
      m.delivery_uncertain && [el("dt", {}, "Delivery"), el("dd", {}, "Unconfirmed; local retries stopped. Cancellation cannot be confirmed.")],
      m.sent_at && Date.parse(m.at)-Date.parse(m.sent_at)>=60000 && [el("dt",{},"Arrived here"),el("dd",{},new Date(m.at).toLocaleString())],
      m.state && [el("dt", {}, "Stored state"), el("dd", { class: "mono" }, m.state)],
      m.status && [el("dt", {}, "Outcome"), el("dd", { class: "mono" }, m.status)],
      m.responder && [el("dt", {}, "Handled by"), el("dd", {}, m.responder === "manual" ? "a reply by hand" : "your responder (" + m.responder + ")")],
      m.path && [el("dt", {}, "Route"), el("dd", {}, m.path === "direct" ? "Direct to their computer" : "Through the server")],
      controlDetails(m),
      m.dir === "in" && m.files && m.files.some((f) => !f.saved) &&
        [el("dt", {}, "Save files"), el("dd", { class: "mono" }, "agentnet download " + m.id)]));
}

const actionLabel = {
  do_it: "Do it", continue: "Reply",
  accept: "Accept and run…", accept_always: "Always accept from this key…", decline: "Decline…",
  approve: "Answer their questions automatically…", resolve: "Close without replying…", reply: "Reply", cancel: "Stop…",
};

// isReport: exactly the shape the client files as a review notice (a plain
// message with that status, no reply link, no files).
const isReport = (m) => m.dir === "in" && m.kind === "message" && m.status === "review_notice" && !m.reply_to && !(m.files && m.files.length);

function actionButton(a, m, t, primary) {
  let label = actionLabel[a];
  if (a === "resolve") label = isReport(m) ? "Dismiss report…" : "Mark as handled";
  if (a === "approve" && t.permission_person) label="Approve " + t.permission_person.label + "…";
  if (a === "accept" && m.kind === "question") label = "Let your responder answer…";
  if (a === "accept" && ["needs_human", "interrupted", "failed", "cancelled"].includes(m.exec?.state || m.state)) label = "Run your responder again…";
  if (a === "accept" && (m.actions || []).includes("resolve") && !["interrupted", "failed", "cancelled"].includes(m.exec?.state || m.state)) label = "Ask again";
  return el("button", { type: "button", class: "act" + (primary ? " go" : ""), onclick: () => decide(a, m, t) }, label);
}

function agentDetailFor(m, t) {
  return m.job_detail || (state.overview.needs_you || []).find(c => c.conv === t.id && c.id === m.id && c.reason === "agent_needs_human")?.why || "";
}

function agentNeedsYouTurn(m, t) {
  return el("section", {class:"agent-turn", "aria-label":"Your agent says"},
    el("strong", {}, (m.exec?.state || m.state) === "interrupted" ? "Your agent was interrupted — run it again if needed" : "Your agent couldn’t finish — it needs your attention"),
    el("p", {class:"agent-detail"}, agentDetailFor(m, t)),
    (m.actions || []).length ? el("div", {class:"acts"}, m.actions.map((a,i)=>actionButton(a,m,t,i===0)))
      : m.target && el("p", {class:"hint"}, "Open it on " + deviceWords(m.target.address)));
}

// ---- decisions ---------------------------------------------------------------

async function act(body, host) {
  const r = await api("/api/act", body, host);
  if (r.note) announce(r.note);
  return r;
}

function decide(a, m, t) {
  if (a === "continue") {
    if (!m.continuation) { announce("This request no longer waits for an answer. Refresh it and review its current state."); return; }
    const c = {...m.continuation}, id = sendID(), host = currentHost;
    const text = el("textarea", {id:"agent-answer",rows:"4",maxlength:"8000"});
    const send = async () => {
      if (host !== currentHost) throw Error("Workspace changed. Open the request again.");
      if (!text.value.trim()) throw Error("Write your answer first.");
      const result = c.host
        ? await api("/api/operator/decide",{host:c.host,id:c.id,key:c.key,action:"continue",expect:"needs_human",attempt:c.attempt,text:text.value.trim(),send_id:id,report:""},host)
        : await act({do:"continue",id:c.id,key:c.key,attempt:c.attempt,body:text.value.trim(),send_id:id},host);
      announce(result.note || "Answer submitted."); await refetch(false);
    };
    dialog({title:"Answer your agent",ok:"Send answer",focus:text,body:[
      el("p",{class:"agent-detail"},m.job_detail || m.detail || ""),
      el("p",{class:"hint"},"Reply supplies missing information for this request. If the agent reports a permission or environment problem, resolve it in the native agent on its host computer first. Reply and retry keep the same permissions."),
      el("label",{for:"agent-answer",class:"field-label"},"Your answer"),text],run:send});
    $("dialog-ok").disabled = true;
    text.addEventListener("input",()=>{if(!state.dialogBusy)$("dialog-ok").disabled=!text.value.trim();});
    return;
  }
  if (a === "do_it") return act({ do: a, id: m.id }).then(() => refetch(false)).catch(e => announce(e.message));
  if (a === "reply") return writeDialog(t, m);
  const quote = el("div", { class: "quote" }, mentionPlain(m.body));
  const from = el("dl", {}, el("dt", {}, "From"), el("dd", {}, m.from));
  const runs = m.target && m.target.agent_id
    ? namedAgentOn(m.target.agent_id, m.target.address) + ", using its local configuration on that computer."
    : state.overview && state.overview.me.responder
    ? "Your responder (" + state.overview.me.responder + ") in " + state.overview.me.responder_dir
    : "Nothing yet: no responder is set. It stays accepted until you choose one (agentnet responder set).";
  if (a === "accept" || a === "accept_always") {
    const check = el("input", { type: "checkbox", id: "gate" });
    const always = a === "accept_always";
    return dialog({
      title: m.kind === "task" ? "Run this task on your computer?" : "Let your responder answer this?",
      body: [from, quote,
        el("dl", {}, el("dt", {}, "Runs"), el("dd", m.target && m.target.agent_id ? { title: "agent " + m.target.agent_id + " · " + m.target.address } : {}, runs),
          el("dt", {}, "Permissions"), el("dd", {}, m.kind === "task"
            ? "Its normal permissions. It is not sandboxed and can change files."
            : "Questions use your native tools, skills and permissions unchanged. A native approval may need your attention."),
          always && [el("dt", {}, "From now on"), el("dd", {}, "Later tasks from " + (t.permission_person?.label || deviceWords(m.from)) + (t.permission_person ? "’s current and future verified devices" : "’s current key") + " also run without asking, until you revoke it.")]),
        el("label", { class: "check" }, check, el("span", {}, always ? (t.permission_person ? "I allow this person's current and future verified devices to give tasks." : "I want this and later tasks from this key to run.") : "I have read this and want it to run.")),
        el("p", { class: "hint" }, "In a terminal: agentnet accept " + (always ? "--always " : "") + m.id)],
      ok: always ? "Run and always accept" : "Run", gate: check, run: () => act({ do: a, id: m.id }),
    });
  }
  if (a === "decline") {
    const reason = el("textarea", { id: "reason", rows: "3", placeholder: "Optional, sent to " + m.from });
    dialog({
      title: "Decline this " + m.kind + "?", body: [from, quote, el("label", { for: "reason", class: "field-label" }, "Reason"), reason],
      ok: "Decline", run: () => act({ do: "decline", id: m.id, reason: reason.value }),
    });
    state.dialogRestore = { type: "decline", msg: m.id };
    return;
  }
  if (a === "approve") return approvalDialog(t);
  if (a === "resolve" && isReport(m)) {
    return dialog({ title: "Dismiss this report?", body: [quote,
      el("p", {}, "It is cleared on this computer only. The requests it reported still wait for a person on " + m.from + "'s machine; nothing here can approve them.")],
      ok: a.member ? "Remove agent" : "Dismiss", run: () => act({ do: "resolve", id: m.id }) });
  }
  if (a === "resolve") {
    return dialog({ title: "Mark as handled?", body: [quote, el("p", {}, "Nothing is sent to " + m.from + ".")],
      ok: "Mark as handled", run: () => act({ do: "resolve", id: m.id }) });
  }
  if (a === "cancel") {
    return dialog({ title: "Stop your responder?", body: [quote, el("p", {}, "Work already done on your computer is not undone.")],
      ok: "Stop", run: () => act({ do: "cancel", id: m.id }) });
  }
}

function approvalDialog(t) {
  const person=t.permission_person, who=person?.label || deviceWords(t.peer), target=person?.person || t.peer;
  if (t.approved) return dialog({ title:"Stop answering " + who + "’s questions automatically?",
    body:[el("p",{},"Their questions wait for you again. A run already started may finish unless stopped.")],
    ok:"Stop automatic answers",run:()=>act({do:"unapprove",id:t.question_target || target}) });
  return dialog({ title:"Approve " + who + "?",
    body:[el("p",{},"Your responder answers " + who + "’s questions " + (person ? "from all current and future verified devices." : "from this device only.") + " Removing a device ends person access; key changes and person conflicts block it. Your normal question settings apply; tools you already allow keep their effects. Tasks still wait for you."),
      el("p",{},"Questions already waiting stay waiting; allow one separately.")],
    ok:"Approve " + who,run:()=>act({do:"approve",id:target}) });
}

function revokeDialog(t) {
  const who=t.permission_person?.label || deviceWords(t.peer);
  dialog({title:"Stop running " + who + "’s tasks without asking?",
    body:[el("p",{},"Their tasks wait for you again. A task already running may finish unless stopped.")],
    ok:"Revoke",run:()=>act({do:"revoke_tasks",id:t.task_target || t.peer}) });
}

function trustDialog(t) {
  const expect = t.key.pending; // the key shown is the only key trusted
  const check = el("input", { type: "checkbox", id: "gate" });
  dialog({
    title: "Trust " + t.peer + "'s new key?", body: [
      el("p", {}, "Ask " + t.peer + " for their key fingerprint through another channel, in person or on a call, and compare."),
      el("dl", {}, el("dt", {}, "Pinned"), el("dd", { class: "mono" }, t.key.pinned || "none"),
        el("dt", {}, "New"), el("dd", { class: "mono" }, expect)),
      el("label", { class: "check" }, check, el("span", {}, "The new fingerprint matches what they told me."))],
    ok: "Trust new key", gate: check, run: () => act({ do: "trust", id: t.peer, key: expect }),
  });
}

// dialog shows a confirmation. The consequential button is never the
// default: focus starts on Cancel and Enter does not confirm. run does the
// work; its error is shown in the dialog, which then stays open.
function dialog({ title, body, ok, run, gate, focus }) {
  const d = $("dialog");
  if (d.open) d.close();
  state.dialogOpener = root.getRootNode().activeElement;
  state.dialogRestore = null; // text dialogs say how to reopen them after an update
  $("dialog-title").textContent = title;
  fill($("dialog-body"), ...body);
  $("dialog-error").textContent = "";
  const okBtn = $("dialog-ok");
  okBtn.textContent = ok;
  okBtn.disabled = !!gate;
  if (gate) gate.addEventListener("change", () => { okBtn.disabled = !gate.checked; });
  let busy = false; // a double click confirms once
  okBtn.onclick = async () => {
    if (busy) return;
    busy = true;
    state.dialogBusy = true;
    okBtn.disabled = true;
    try {
      await run();
      d.close();
    } catch (e) {
      $("dialog-error").textContent = e.message;
      okBtn.disabled = !!gate && !gate.checked;
    } finally {
      busy = false;
      state.dialogBusy = false;
      if (state.newVersion) updated(state.newVersion);
    }
  };
  d.showModal();
  (focus || $("dialog-cancel")).focus();
}

function newConversationDialog(prefill) {
  const to = el("input", { id: "new-to", type: "text", placeholder: "person/agent, e.g. bob/desk", autocomplete: "off", spellcheck: "false" });
  if (typeof prefill === "string") to.value = prefill;
  const kind = el("select", { id: "new-kind" }, ["message", "question", "task"].map((k) => el("option", { value: k }, k[0].toUpperCase() + k.slice(1))));
  const body = el("textarea", { id: "new-body", rows: "4", placeholder: "What do you want to say?" });
  dialog({
    title: "New conversation",
    body: [el("label", { for: "new-to", class: "field-label" }, "To"), to,
      el("label", { for: "new-kind", class: "field-label" }, "Send as"), kind,
      el("label", { for: "new-body", class: "field-label" }, "Message"), body,
      el("p", { class: "hint" }, "A message never runs anything. A question may be answered by their responder if they approved you. A task runs only if they accept it, once or by standing permission for your key.")],
    ok: "Send",
    focus: typeof prefill === "string" ? body : to,
    run: async () => {
      const r = await api("/api/send", { to: to.value.trim(), kind: kind.value, body: body.value });
      announce(r.state === "queued" ? "Sending…" : "Sent.");
      openThread(r.id);
    },
  });
  state.dialogRestore = { type: "new", prefill: typeof prefill === "string" ? prefill : undefined };
}

// ---- composer ------------------------------------------------------------------

// One shared typing presenter also serves Notebook. Its API stays captured
// to the workspace that opened it; switching never retargets a pending clear.
let typingUI = null, typingLoading = null;
function typingScope() {
  if ($("composer").hidden) return null;
  if (humanGroup() && (dmVisitor() || state.dmData.frozen || state.dmAgent)) return null;
  if (dmHumanGuest() && !guestAuthor()) return null; // only an accepted guest types here (client.typingGuest)
  if (state.dm && state.dmData && !state.dmData.frozen) return { conv: state.dm };
  if (state.thread && state.data && !state.data.key.pending) return { peer: state.data.peer, thread: state.data.messages[0].id };
  return null;
}
async function ensureTyping() {
  if (typingUI) return typingUI;
  if (typingLoading) return typingLoading;
  const gen = state.gen, host = currentHost;
  typingLoading = moduleOf("typing").then(m => {
    if (gen !== state.gen) return null;
    const transport = (path, body) => host.api(path, body);
    return typingUI = m.mountTyping({ api: transport, input: $("body"), line: $("typing-line"), settings: $("typing-settings") });
  }).catch(() => null).finally(() => { if (gen === state.gen) typingLoading = null; });
  return typingLoading;
}
async function refreshTyping() { const ui = await ensureTyping(); if (ui) await ui.setScope(typingScope()); }
function clearTyping() { if (typingUI) typingUI.setScope(null); }

const kindValue = () => root.querySelector('input[name="kind"]:checked').value;

function setKind(kind) {
  if (kind === "task") kind = "question";
  for (const r of root.querySelectorAll('input[name="kind"]')) r.checked = r.value === kind;
  kindHint();
  renderTarget();
}

// targetId names what the composer sends to now: the device, the person of
// a DM, an agent asked in it, or the message being answered by hand.
function targetId() {
  if (state.dm) return state.dmAgent ? "agent:" + state.dmAgent : "dm:" + state.dm;
  if (state.answering) return "answer:" + state.answering.id;
  return state.data ? "device:" + state.data.peer + (state.deviceAgentID && kindValue() !== "message" ? ":agent:" + state.deviceAgentID : "") : "";
}

const deviceAgentMissing = () => !!state.deviceAgentID && !state.answering && kindValue() !== "message" &&
  (!state.targetCatalog || state.targetCatalog.loading || state.targetCatalog.error || !(state.targetCatalog.agents || []).some(a => a.id === state.deviceAgentID));
function chooseDeviceAgent(id) {
  if (state.sending) return;
  state.deviceAgentID = id;
  keepDraft(); syncComposer(); kindHint();
}
async function loadTargetCatalog() {
  const t = state.data, gen = state.gen, key = state.draftKey, host = currentHost;
  if (!t || state.dm) return;
  const seq = state.targetCatalogSeq = (state.targetCatalogSeq || 0) + 1;
  const current = () => gen === state.gen && key === state.draftKey && state.data && state.data.peer === t.peer && seq === state.targetCatalogSeq;
  state.targetCatalog = { host: t.peer, loading: true, agents: [] }; syncComposer(); kindHint();
  try {
    const agents = await readAgentCatalog(t.peer, host);
    if (current()) state.targetCatalog = { host: t.peer, agents };
  } catch (e) { if (current()) state.targetCatalog = { host: t.peer, agents: [], error: e.message }; }
  if (current()) { syncComposer(); kindHint(); renderBody(false); }
}
function renderAgentTarget() {
  const box = $("dialog").open && $("write-agent-target") || $("agent-target");
  if (!box) return;
  const d = state.dmData, t = state.data;
  const mentioned = state.dm ? validMentions(state.mentions, $("body").value) : []; // the exact people this draft mentions
  box.hidden = state.dm ? !d || (dmHumanGuest(d) && !state.dmAgent || (!(d.agents || []).length && !state.dmAgent)) && !mentioned.length : !t || !!state.answering || kindValue() === "message";
  if (box.hidden) { fill(box); return; }
  if (state.dm) {
    const a = agentOf(state.dmAgent);
    fill(box, state.dmAgent && !mentioned.some(s => s.ref.kind === "agent") && el("span", { class: "addressed-assistant", title: (a?.agent_id || state.dmAgent) + " · " + (a?.host.address || "unavailable") + " · participation " + state.dmAgent },
      "@" + (a ? agentName(a) : "Selected assistant unavailable"),
      el("button", { type: "button", class: "text-btn", disabled: state.sending, "aria-label": "Remove addressed assistant", onclick: () => { setDMAgent(null); keepDraft(); } }, "×")),
      mentioned.map(s => el("span", { class: "addressed-assistant mentioned-person", title: "@" + s.name + (s.role ? " · " + s.role : "") },
        "@" + s.name,
        el("button", { type: "button", class: "text-btn", disabled: state.sending, "aria-label": "Make @" + s.name + " plain text", onclick: () => { state.mentions = (state.mentions || []).filter(x => x !== s);
          if (s.ref.kind === "agent") state.dmAgent = selectedAgentPIDs($("body").value,state.mentions,null)[0] || null;
          keepDraft(); renderAgentTarget(); syncComposer(); } }, "×"))),
      el("button", { type: "button", class: "text-btn", disabled: state.sending, onclick: () => { showMentions(true); mentionInput().focus(); } }, "@ Mention"));
    return;
  }
  const catalog = state.targetCatalog, selected = state.deviceAgentID || "", agents = catalog && catalog.host === t.peer ? catalog.agents || [] : [];
  fill(box, el("button", {type: "button", class: "text-btn", disabled: state.sending,
    title: selected || "Device default responder", onclick: () => {
      const gen = state.gen, key = state.draftKey;
      const choose = id => { if (gen !== state.gen || key !== state.draftKey) return; chooseDeviceAgent(id); $("dialog").close(); $("body").focus(); };
      dialog({title: "Address an assistant on " + t.peer, body: [
        el("button", {type: "button", class: "btn", onclick: () => choose("")}, "Device default responder"),
        agents.map(a => el("button", {type: "button", class: "btn", title: a.id + " · " + a.host, onclick: () => choose(a.id)}, catalogLabel(a, agents))),
        el("button", {type: "button", class: "text-btn", disabled: catalog?.loading, onclick: async () => { await loadTargetCatalog(); $("dialog").close(); }}, "Refresh agents"),
        catalog?.error && el("p", {class: "error"}, catalog.error)], ok: "Close", run: async () => {}});
    }}, selected ? "@" + namedAgentLabel(selected, t.peer) + (deviceAgentMissing() && catalogName(selected, t.peer) ? " · unavailable" : "") : "Address assistant…"), catalog?.error && el("p", {class: "error", role: "alert"}, catalog.error));

}

// Reply destination is explicitly selected, independent of the addressed executor.
async function localReceiverCatalog(host, expected) {
  const v = await api("/api/agents", undefined, host);
  if (!v.local || v.host !== expected || !Array.isArray(v.agents) || v.agents.some(a => !a.record || a.record.host !== expected || !/^[0-9a-f]{32}$/.test(a.record.id))) throw new Error("Local assistant list does not match this workspace's native host.");
  return v.agents.filter(a => a.enabled && a.responder?.ready);
}
async function localReplySessions(host, expected) {
  const v = await api("/api/reply-sessions", undefined, host);
  if (!v.local || v.host !== expected || !Array.isArray(v.sessions) || v.sessions.some(s => !s || !/^[A-Za-z0-9_-]{1,128}$/.test(s.handle) || !["pi", "omp", "codex", "claude"].includes(s.harness) || typeof s.label !== "string" || typeof s.active !== "boolean")) throw new Error("Native session list does not match this workspace's host.");
  return v.sessions;
}
function receiverOwnDevices(person) {
  if (person?.state !== "self" || !Array.isArray(person.devices)) return [];
  return person.devices.filter(d => typeof d.address === "string" && /^[0-9a-f]{8}(?:-[0-9a-f]{8}){3}$/.test(d.fingerprint || ""));
}
function receiverCapture() {
  const o = state.overview, person = o?.person;
  return { address: o?.me?.address, person: person && { person: person.person, state: person.state, devices: receiverOwnDevices(person).map(d => ({ address: d.address, fingerprint: d.fingerprint })) }, host: state.replyReceiverHost && { ...state.replyReceiverHost } };
}
function receiverHostProof(choice, context) {
  if (!choice || !context?.address || !receiverOwnDevices(context.person).some(d => d.address === context.address) || !receiverOwnDevices(context.person).some(d => d.address === choice.address && d.fingerprint === choice.fingerprint)) throw new Error("Selected reply host is not a current own device with that exact key. Your original choice stays; nothing sent.");
}
async function remoteReplySessions(host, choice) {
  const v = await api("/api/reply-sessions?host=" + encodeURIComponent(choice.address) + "&host_key=" + encodeURIComponent(choice.fingerprint), undefined, host);
  if (v.local !== false || v.host !== choice.address || v.host_key !== choice.fingerprint || !["pending", "ready", "unavailable"].includes(v.status) || !Array.isArray(v.sessions) || v.sessions.some(s => !s || !/^[A-Za-z0-9_-]{1,128}$/.test(s.handle) || !["pi", "omp", "codex", "claude"].includes(s.harness) || typeof s.label !== "string" || typeof s.active !== "boolean") || new Set(v.sessions.map(s => s.handle)).size !== v.sessions.length || v.status !== "ready" && v.sessions.length) throw new Error("Remote registration snapshot does not match the exact selected host key.");
  return { ...v, sessions: v.sessions.filter(s => s.harness !== "claude") }; // Claude product receipt gate remains separate
}
function chooseReplyHost(address) {
  if (state.sending) return;
  const d = receiverOwnDevices(state.overview?.person).find(d => d.address === address);
  if (address !== state.overview?.me?.address && !d) return;
  state.replyReceiverHost = address === state.overview.me.address ? null : { address: d.address, fingerprint: d.fingerprint };
  state.receiverCatalog = null; state.receiverCatalogSeq++;
  keepDraft(); renderReplyReceiver(); // retained receiver is never silently retargeted
}
async function loadReceiverCatalog() {
  const context = receiverCapture(), choice = context.host;
  if (!choice && !state.overview?.reply_receivers) return;
  const host = currentHost, ws = wsNow(), gen = state.gen, key = state.draftKey, address = choice?.address || state.overview.me.address, sessionsSupported = !!state.overview.reply_sessions, seq = ++state.receiverCatalogSeq;
  const current = () => gen === state.gen && ws === wsNow() && host === currentHost && key === state.draftKey && seq === state.receiverCatalogSeq && JSON.stringify(choice) === JSON.stringify(state.replyReceiverHost);
  state.receiverCatalog = { loading: true, host: address, remote: !!choice, fingerprint: choice?.fingerprint, agents: [], sessions: [] };
  renderReplyReceiver();
  try {
    if (choice) {
      receiverHostProof(choice, context);
      const [records, snapshot] = await Promise.all([readAgentCatalog(address, host), remoteReplySessions(host, choice)]);
      if (current()) state.receiverCatalog = { host: address, remote: true, fingerprint: choice.fingerprint, agents: records.map(record => ({ record })), sessions: snapshot.sessions, status: snapshot.status, detail: snapshot.detail, at: snapshot.at };
    } else {
      const [agents, sessions] = await Promise.all([localReceiverCatalog(host, address), sessionsSupported ? localReplySessions(host, address) : []]);
      if (current()) state.receiverCatalog = { host: address, agents, sessions };
    }
  } catch (e) { if (current()) state.receiverCatalog = { host: address, remote: !!choice, fingerprint: choice?.fingerprint, agents: [], sessions: [], error: e.message }; }
  if (current()) renderReplyReceiver();
}
async function loadReceiverBindings() {
  const host = currentHost, ws = wsNow(), gen = state.gen;
  try { const rows = await api("/api/reply-receivers", undefined, host); if (gen === state.gen && ws === wsNow() && host === currentHost && Array.isArray(rows)) { state.receiverBindings = rows; renderReceiverStatus(); } }
  catch (_) { if (gen === state.gen && ws === wsNow()) { state.receiverBindings = []; renderReceiverStatus(); } }
}
function chooseReplyReceiver(id) {
  if (state.sending) return;
  const a = state.receiverCatalog?.agents.find(a => a.record.id === id), session = state.receiverCatalog?.sessions?.find(s => "session:" + s.handle === id);
  if (id && !a && !session) return;
  state.replyReceiver = session ? { kind: "live_session", session_handle: session.handle, label: session.label || session.harness + " session", harness: session.harness, host: state.receiverCatalog.host, ...(state.receiverCatalog.remote ? { fingerprint: state.receiverCatalog.fingerprint } : {}), workspace: wsNow() } : a ? { kind: "managed_agent", agent_id: id, label: a.record.label, host: a.record.host, ...(state.receiverCatalog.remote ? { fingerprint: state.receiverCatalog.fingerprint } : {}), workspace: wsNow(), instructions: "", mode: "" } : null;
  keepDraft(); renderReplyReceiver();
}
function chooseReplyBackup(id) {
  if (state.sending || state.replyReceiver?.kind !== "live_session") return;
  if (id && !["pi", "omp", "codex"].includes(state.replyReceiver.harness)) return;
  const agent = state.receiverCatalog?.agents?.find(a => a.record.id === id);
  if (id && !agent) return;
  const { on_close, ...receiver } = state.replyReceiver;
  state.replyReceiver = id ? { ...receiver, on_close: { agent_id: id, label: agent.record.label, instructions: "", mode: "", configuration: JSON.stringify(state.receiverCatalog.remote ? agent.record : agent.responder) } } : receiver;
  if (!id && on_close && receiver.harness === "claude") {
    const error = $("compose-error"), obsolete = "Automatic backup after Claude closes is unavailable. Replies stay with this session. Remove the backup or choose another receiver before sending.";
    if (error.textContent === obsolete || error.textContent === obsolete + " Your files are still here.") error.textContent = "";
  }
  keepDraft(); renderReplyReceiver();
}
async function prepareReplyReceiverSelection(selected, host, workspace, supported, sessionsSupported, context = null) {
  if (selected?.on_close && selected.harness === "claude") throw new Error("Automatic backup after Claude closes is unavailable. Replies stay with this session. Remove the backup or choose another receiver before sending.");
  const remote = context?.host;
  if (remote) {
    receiverHostProof(remote, context);
    if (selected && (selected.workspace !== workspace || selected.host !== remote.address || selected.fingerprint !== remote.fingerprint)) throw new Error("Selected receiver belongs to another host or workspace. Choose it explicitly; your draft stays.");
    const backup = selected?.on_close && { ...selected.on_close };
    if (backup && selected.kind !== "live_session") throw new Error("Closed-session backup requires an exact native reply session.");
    let receiver = { kind: "human", host: { ...remote } };
    if (selected) {
      if (selected.kind === "live_session") {
        const snapshot = await remoteReplySessions(host, remote);
        if (snapshot.status !== "ready" || !snapshot.sessions.some(s => s.handle === selected.session_handle && s.harness === selected.harness)) throw new Error(snapshot.detail || "Selected remote session snapshot is pending or unavailable. Nothing sent; no default was chosen.");
        receiver = { kind: "live_session", session_handle: selected.session_handle, host: { ...remote } };
      } else if (selected.kind === "managed_agent") {
        if (!selected.instructions?.trim() || !["question", "task"].includes(selected.mode)) throw new Error("Write original continuation instructions and choose question or task mode.");
        const agents = await readAgentCatalog(remote.address, host);
        if (!agents.some(a => a.id === selected.agent_id)) throw new Error("Selected remote assistant is no longer published on this host. Nothing sent; no default was chosen.");
        receiver = { kind: "managed_agent", agent_id: selected.agent_id, instructions: selected.instructions, mode: selected.mode, host: { ...remote } };
      } else throw new Error("Unsupported selected reply receiver.");
      if (backup) {
        if (!backup.instructions?.trim() || !["question", "task"].includes(backup.mode)) throw new Error("Write original backup instructions and choose question or task mode.");
        const agents = await readAgentCatalog(remote.address, host), agent = agents.find(a => a.id === backup.agent_id);
        if (!agent || JSON.stringify(agent) !== backup.configuration) throw new Error("Selected backup public record changed or is unavailable. Choose it again explicitly; no default was chosen.");
        receiver.on_close = { agent_id: backup.agent_id, instructions: backup.instructions, mode: backup.mode };
      }
    }
    const fresh = await api("/api/overview", undefined, host);
    if (fresh.me?.address !== context.address || fresh.person?.person !== context.person.person) throw new Error("Reply receiver workspace identity changed. Nothing sent; original draft stays.");
    receiverHostProof(remote, { address: fresh.me.address, person: fresh.person });
    return receiver;
  }
  if (!selected) return supported ? { kind: "human" } : null;
  const backup = selected.on_close && { ...selected.on_close };
  if (backup && selected.kind !== "live_session") throw new Error("Closed-session backup requires an exact native reply session.");
  if (!supported || selected.workspace !== workspace || selected.fingerprint) throw new Error("Selected reply assistant is unavailable in this workspace. Your draft stays here.");
  if (selected.kind === "live_session") {
    if (!sessionsSupported) throw new Error("Native session continuation is unavailable in this workspace. Your draft stays here.");
    const sessions = await localReplySessions(host, selected.host);
    const session = sessions.find(s => s.handle === selected.session_handle);
    if (!session || session.harness !== selected.harness) throw new Error("Selected native reply session is not registered here with that harness. Nothing sent; no default was chosen.");
    const receiver = { kind: "live_session", session_handle: selected.session_handle };
    if (backup) {
      if (!backup.instructions?.trim() || !["question", "task"].includes(backup.mode)) throw new Error("Write original backup instructions and choose question or task mode.");
      const agents = await localReceiverCatalog(host, selected.host), agent = agents.find(a => a.record.id === backup.agent_id);
      if (!agent) throw new Error("Selected backup assistant is no longer enabled on this host. Nothing sent; no default was chosen.");
      if (JSON.stringify(agent.responder) !== backup.configuration) throw new Error("Selected backup assistant configuration changed. Choose it again explicitly. Nothing sent; no default was chosen.");
      receiver.on_close = { agent_id: backup.agent_id, instructions: backup.instructions, mode: backup.mode };
    }
    return receiver;
  }
  if (!selected.instructions.trim() || !["question", "task"].includes(selected.mode)) throw new Error("Write original local continuation instructions and choose question or task mode.");
  const agents = await localReceiverCatalog(host, selected.host);
  if (!agents.some(a => a.record.id === selected.agent_id)) throw new Error("Selected reply assistant is no longer enabled on this host. Nothing sent; no default was chosen.");
  return { kind: "managed_agent", agent_id: selected.agent_id, instructions: selected.instructions, mode: selected.mode };
}
function renderReplyReceiver() {
  const box = $("reply-receiver"); if (!box) return;
  box.hidden = !state.dmData && !state.data || !!state.answering || !!state.dmAgent && (dmHumanGuest() || (state.dmData?.guests || []).some(g => g.state === "active")); // a request with guests present names its assistant only
  $("receiver-options").hidden = box.hidden;
  if (box.hidden) { fill(box); renderReceiverStatus(); return; }
  const selected = state.replyReceiver, catalog = state.receiverCatalog, choice = state.replyReceiverHost, supported = !!state.overview?.reply_receivers || !!choice;
  const address = choice?.address || state.overview?.me?.address, devices = receiverOwnDevices(state.overview?.person);
  const catalogMatches = catalog?.host === address && (!choice || catalog.fingerprint === choice.fingerprint);
  const live = selected?.kind === "live_session", managed = selected?.kind === "managed_agent", backup = live && selected.on_close, claudeBackup = live && selected.harness === "claude";
  $("receiver-summary").textContent = "Reply receiver · " + (selected ? selected.label + (live ? " (" + selected.harness + " session · " + selected.session_handle.slice(0, 8) + ")" : "") + " on " + selected.host + (managed ? selected.mode ? " · " + selected.mode : " · choose mode" : "") : "Me (human)" + (choice ? " on " + choice.address : "")) + (live ? backup ? " · backup " + backup.label + " · " + (backup.mode || "choose mode") : " · no backup" : "");
  if (supported && (!catalog || !catalogMatches)) { if (state.draftKey !== null) loadReceiverCatalog(); return; }
  const selectedMatches = !selected || selected.host === address && (!choice ? !selected.fingerprint : selected.fingerprint === choice.fingerprint);
  const agents = supported && catalogMatches ? catalog?.agents || [] : [], sessions = supported && catalogMatches ? catalog?.sessions || [] : [], selectedSession = live && selectedMatches && sessions.find(s => s.handle === selected.session_handle);
  const picker = el("select", { id: "receiver-picker", "aria-label": "Reply receiver", disabled: state.sending,
    onchange: e => chooseReplyReceiver(e.currentTarget.value) },
    el("option", { value: "", selected: !selected }, "Me (human)"),
    agents.map(a => el("option", { value: a.record.id, selected: selectedMatches && selected?.agent_id === a.record.id, title: a.record.id }, a.record.label + " · " + a.record.host)),
    sessions.map(s => el("option", { value: "session:" + s.handle, selected: selectedMatches && live && selected.session_handle === s.handle, title: "Registration metadata; not an online check" }, (s.label || s.harness + " session") + " (" + s.harness + " · " + s.handle.slice(0, 8) + ") · " + catalog.host + (s.active ? " · registered" : " · inactive registration"))),
    selected && (!selectedMatches || (live ? !selectedSession : !agents.some(a => a.record.id === selected.agent_id))) && el("option", { value: live ? "session:" + selected.session_handle : selected.agent_id, selected: true }, selected.label + " · unavailable"));
  const focused = (root.getRootNode().activeElement || null), editing = focused?.id === "receiver-instructions" || focused?.id === "receiver-backup-instructions", start = editing && focused.selectionStart, end = editing && focused.selectionEnd;
  const instruction = managed && Object.assign(el("textarea", { id: "receiver-instructions", rows: 2, maxlength: 4096, disabled: state.sending, oninput: e => { state.replyReceiver = { ...state.replyReceiver, instructions: e.currentTarget.value }; keepDraft(); } }), { value: selected.instructions });
  const hostPicker = el("select", { id: "receiver-host", "aria-label": "Reply receiver device", disabled: state.sending,
    onchange: e => chooseReplyHost(e.currentTarget.value) },
    el("option", { value: state.overview?.me?.address, selected: !choice }, "This device · " + state.overview?.me?.address),
    devices.filter(d => d.address !== state.overview.me.address).map(d => el("option", { value: d.address, selected: choice?.address === d.address && choice.fingerprint === d.fingerprint, title: d.fingerprint }, d.address)),
    choice && !devices.some(d => d.address === choice.address && d.fingerprint === choice.fingerprint) && el("option", { value: choice.address, selected: true, title: choice.fingerprint }, choice.address + " · selected key unavailable"));
  fill(box, el("label", { for: "receiver-host" }, "Reply receiver device"), hostPicker,
    el("label", { for: "receiver-picker" }, "Reply receiver"), picker,
    supported && el("button", { type: "button", class: "text-btn", disabled: state.sending || catalog?.loading, onclick: loadReceiverCatalog }, choice ? "Refresh receivers" : "Refresh local receivers"),
    !supported && el("p", { class: "hint" }, "This browser runs no local assistants. Choose a current own device explicitly to receive replies there."),
    catalog?.loading && el("p", { class: "hint" }, choice ? "Loading selected host records…" : "Loading local receivers…"),
    catalog?.error && el("p", { class: "error", role: "alert" }, catalog.error),
    choice && el("p", { class: "hint" }, catalog?.status === "ready" ? "Authenticated registration snapshot; not an online check. Selected host decides approval and exact native configuration." : "Registration snapshot " + (catalog?.status || "pending") + ". Refresh explicitly after its verified reply; no receiver or default is selected automatically."),
    choice && catalog?.detail && el("p", { class: "hint" }, catalog.detail),
    managed && [el("label", { for: "receiver-instructions" }, choice ? "Original continuation instructions" : "Original local continuation instructions"), instruction,
      el("label", { for: "receiver-mode" }, choice ? "Continuation mode" : "Local continuation mode"),
      el("select", { id: "receiver-mode", "aria-label": choice ? "Continuation mode" : "Local continuation mode", disabled: state.sending, onchange: e => { state.replyReceiver = { ...state.replyReceiver, mode: e.currentTarget.value }; keepDraft(); renderReplyReceiver(); } },
        el("option", { value: "", selected: !selected.mode }, "Choose question or task"),
        el("option", { value: "question", selected: selected.mode === "question" }, "Question"),
        el("option", { value: "task", selected: selected.mode === "task" }, "Task")),
      el("p", { class: "hint" }, selected.label + " runs on " + selected.host + " when a verified correlated reply arrives. Send delegates these original instructions once; reply text grants no permissions. " + (selected.mode === "task" ? "Local task actions use this assistant's normal permissions." : "Local question mode uses this assistant's normal question permissions."))]);
  if (live) {
    box.append(el("p", { class: "hint" }, selectedSession ? (selectedSession.active ? "Registered native adapter; registration is not proof the session is online. " : "Inactive native adapter; registration alone does not prove clean closure. ") + "This receives correlated replies into the existing " + selected.harness + " session on " + selected.host + ", under that session's own task and permissions. Reply text grants no permissions." : "Selected native session is unavailable in this host catalog. Your exact draft stays; no default is chosen."),
      ...(claudeBackup ? [el("p", { class: "hint" }, "Automatic backup after Claude closes is unavailable. Replies stay with this session." + (backup ? " Remove the backup or choose another receiver before sending." : "")),
        ...(backup ? [el("p", { class: "error", role: "alert" }, "Retained unsupported backup · " + backup.label + ". Remove it or change receiver explicitly before sending; your original draft and files stay."),
          el("button", { type: "button", class: "text-btn", disabled: state.sending, onclick: () => chooseReplyBackup("") }, "Remove unsupported backup")] : [])] : [
        el("label", { for: "receiver-backup-picker" }, choice ? "If this session closes · optional host backup" : "If this session closes · optional local backup"),
        el("select", { id: "receiver-backup-picker", "aria-label": "Closed-session backup assistant", disabled: state.sending, onchange: e => chooseReplyBackup(e.currentTarget.value) },
          el("option", { value: "", selected: !backup }, "No backup · keep replies pending"),
          agents.map(a => el("option", { value: a.record.id, selected: backup?.agent_id === a.record.id, title: a.record.id }, a.record.label + " · " + a.record.host)),
          backup && !agents.some(a => a.record.id === backup.agent_id) && el("option", { value: backup.agent_id, selected: true }, backup.label + " · unavailable")),
        el("p", { class: "hint" }, "Only an observed clean native shutdown can hand over unclaimed replies. Unknown crash or uncertain intake stays held; viewing or reloading never starts work.")]));
    if (backup) {
      const update = (key, value) => { state.replyReceiver = { ...state.replyReceiver, on_close: { ...state.replyReceiver.on_close, [key]: value } }; keepDraft(); };
      const backupInstruction = Object.assign(el("textarea", { id: "receiver-backup-instructions", rows: 2, maxlength: 4096, disabled: state.sending || claudeBackup, oninput: e => update("instructions", e.currentTarget.value) }), { value: backup.instructions });
      box.append(el("label", { for: "receiver-backup-instructions" }, "Original backup continuation instructions"), backupInstruction,
        el("label", { for: "receiver-backup-mode" }, "Backup continuation mode"),
        el("select", { id: "receiver-backup-mode", "aria-label": "Backup continuation mode", disabled: state.sending || claudeBackup, onchange: e => { update("mode", e.currentTarget.value); renderReplyReceiver(); } },
          el("option", { value: "", selected: !backup.mode }, "Choose question or task"),
          el("option", { value: "question", selected: backup.mode === "question" }, "Question"),
          el("option", { value: "task", selected: backup.mode === "task" }, "Task")),
        el("p", { class: "hint" }, claudeBackup ? "These unsupported backup instructions are retained, not delegated. Remove the backup or change receiver explicitly." : "Send explicitly delegates these instructions to " + backup.label + " (" + backup.agent_id.slice(0, 8) + ") on " + selected.host + " after clean closure, under its normal " + (backup.mode || "chosen") + " permissions. " + (choice ? "The selected host verifies and accepts its actual configuration; this browser sees only the published record." : "The native host freezes this exact configuration; later changes refuse, never select a default.")));
      if (editing && focused.id === "receiver-backup-instructions" && !state.sending) { backupInstruction.focus(); backupInstruction.setSelectionRange(start, end); }
    }
  }
  if (editing && focused.id === "receiver-instructions" && managed && !state.sending) { instruction.focus(); instruction.setSelectionRange(start, end); }
  renderReceiverStatus();
}
function renderReceiverStatus() {
  const box = $("receiver-status"); if (!box) return;
  const messages = (state.dm ? state.dmData?.messages : state.data?.messages) || [];
  const ids = new Set(messages.flatMap(m => [m.id, m.lid]).filter(Boolean));
  const rows = (state.receiverBindings || []).filter(b => state.dm ? b.conv === state.dm : !b.conv && ids.has(b.request_ref)).slice(-3);
  box.hidden = !rows.length;
  const words = { pending: "Waiting for correlated reply", running: "Running locally", completed: "Local continuation completed", canceled: "Canceled", uncertain: "Uncertain · local review needed", refused: "Refused · original selection kept", accepted: "Accepted · completion unconfirmed" };
  const liveWords = { pending: "Pending native session intake; no unattended handoff proven", accepted: "Accepted into native session; effects completion unconfirmed", completed: "Native input processed; effects completion unconfirmed", canceled: "Canceled", refused: "Refused · exact session binding kept", uncertain: "Uncertain native intake · local review needed" };
  fill(box, rows.map(b => {
    const matches = messages.filter(m => m.dir === "out" && !m.excerpt_pid && (m.id === b.request_ref || m.lid === b.request_ref));
    const request = matches.length === 1 ? matches[0] : null;
    const caption = request && firstLine(request.body || "Files: " + (request.attachments || []).map(f => f.name).join(", "), 80);
    const remote = !!b.receiver?.host;
    const status = request?.state === "receiver_waiting" ? "Awaiting selected-host approval and Ready; original request not sent" : b.receiver?.kind === "human" ? "Replies remain for you; no automatic continuation" : b.receiver?.kind === "live_session" ? liveWords[b.state] || "Native session state " + b.state + "; effects completion unconfirmed" : (remote ? { ...words, running: "Running on selected host", completed: "Selected-host continuation completed", uncertain: "Uncertain · selected-host review needed" } : words)[b.state] || b.state;
    return el("p", { class: "hint", "data-request": b.request_ref },
      el("span", { class: "receiver-request", title: b.request_ref }, "Request " + (caption ? "“" + caption + "” · " : "") + b.request_ref.slice(0, 8)),
      b.label + " on " + b.host + " · " + status + (b.detail ? ": " + b.detail : "") + ". This is continuation state reported by this provider, separate from delivery.",
      b.receiver?.on_close && el("span", { class: "receiver-request" },
        ({ preauthorized: "Backup preauthorized; not handed over", held: "Backup held; no safe handoff", handed_over: "Handed over to selected backup; effects completion unconfirmed" }[b.handoff_state] || "Backup state unconfirmed; no handoff claimed") + " · " + (b.handoff_label || "Selected assistant") + " (" + b.receiver.on_close.agent_id.slice(0, 8) + ") on " + b.host));
  }));
}

// renderTarget says, above the text, exactly who gets what is typed and
// how, and names it on the Send button. It reads the same state a send
// reads, so what it shows is what goes out.
function renderTarget() {
  renderAgentTarget();
  renderReplyReceiver();
  const set = (to, how, verb) => { $("to-name").textContent = to; $("to-how").textContent = how; $("send-label").textContent = verb; };
  if (state.dm) {
    const d = state.dmData;
    if (!d) { set("", "", "Send"); return; }
    if (humanGroup(d) && !state.dmAgent) {
      const guestNames = (d.guests || []).filter(g => g.state === "active" && !g.host_here).map(g => g.host.label);
      set(d.title, (d.frozen ? "· Last verified audience: " : "· ") + (d.members || []).map(p => p.state === "self" ? "You" : p.label).join(", ") + (guestNames.length ? " · Guests: " + joinedNames(guestNames) : "") + (dmHumanGuest(d) ? " · You are a guest, not a member" : "") + (state.dmReply ? " · reply" : ""), "Send to " + d.title); return;
    }
    if (state.dmAgent) {
      const a = agentOf(state.dmAgent);
      const who = a ? agentName(a).replace(/^Your/, "your") : "an agent no longer in this DM";
      const task = kindValue() === "task";
      set(who + (a ? " (on " + a.host.address + ")" : ""),
        askGone() ? "· cannot be asked now" : task ? "· a task: it runs on " + a.host.address + " once accepted, or by standing permission" : "· a question: that agent answers it",
        (task ? "Give task to " : "Ask ") + who);
      return;
    }
    const guestNames = dmHumanGuest(d) ? (d.guests || []).filter(g => g.state === "active" && !g.host_here).map(g => g.host.label) : (d.guests || []).filter(g => g.can_send && !g.host_here).map(g => g.host.label);
    const audience = joinedNames(dmHumanGuest(d) ? guestAudienceNames(d) : [d.peer.label, ...guestNames]);
    set(audience, (state.dmReply ? "· replying to “" + firstLine(state.dmReply.body, 40) + "”" : "· same conversation") + (guestNames.length ? " · includes accepted guests" : dmHumanGuest(d) ? " · conversation message" : d.audience_pending ? " · private here; other devices may still be updating" : " · private between original people"), guestNames.length || dmHumanGuest(d) ? "Send" : "Send to " + d.peer.label);
    return;
  }
  const t = state.data;
  if (!t) { set("", "", "Send"); return; }
  if (state.answering) { set(whoText(t.peer), "· your answer to their " + (kindTag[state.answering.kind] || "message").toLowerCase(), "Send answer"); return; }
  const last = t.messages[t.messages.length - 1], k = kindValue();
  if (state.deviceAgentID && k !== "message") {
    const id = state.deviceAgentID, agent = state.targetCatalog && (state.targetCatalog.agents || []).find(a => a.id === id);
    const name = namedAgentOn(id, t.peer);
    set(name, deviceAgentMissing() ? "· unavailable; draft kept" : k === "task" ? "· task permission still required" : "· a question for this agent",
      (k === "task" ? "Give task to " : "Ask ") + (agent ? agent.label : "selected agent")); return;
  }
  const to = whoText(t.peer);
  set(to, (last ? "· continues “" + lineOf(last, 40) + "”" : "· a new conversation") +
    (k === "task" ? " · as a task they accept first" : k === "question" ? " · as a question their responder may answer" : ""),
    k === "task" ? "Give task to " + to : k === "question" ? "Ask " + to : "Send to " + to);
}

// A draft is bound to the conversation and target it was started for
// (noteTyping). A send checks that binding first: text started for one
// recipient is never sent to another without the person looking again.
state.typedFor = null;
function noteTyping() {
  if (!$("body").value) { state.typedFor = null; return; }
  if (!state.typedFor) state.typedFor = { key: state.draftKey, to: targetId() };
}
function boundElsewhere() {
  const b = state.typedFor;
  if (!b || (b.key === state.draftKey && b.to === targetId())) return "";
  state.typedFor = null; // looked at: the next send goes where the To line says
  return "You started this text for someone else. Check the To line, then send again.";
}

// setDMReply chooses the message of the open DM a new message replies to
// (or none). It is part of that DM's draft and never crosses to another.
function setDMReply(m) {
  if (m && (dmVisitor() || m.excerpt_pid)) return;
  state.dmReply = m ? { id: m.id, body: m.body } : null;
  if (m && state.dmAgent) setDMAgent(null);
  if (!m && (state.answering || state.dmAgent)) return;
  $("replying").hidden = !m;
  if (m) {
    $("replying-label").textContent = "Replying to";
    $("replying-text").textContent = firstLine(m.body, 70);
  }
  renderTarget();
}

function setAnswering(m) {
  state.answering = m;
  $("replying").hidden = !m;
  $("replying-label").textContent = "Answering";
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
  if (text || state.answering || state.dmReply || state.dmAgent || state.deviceAgentID || state.replyReceiver || state.replyReceiverHost || state.files.length) {
    state.drafts[state.draftKey] = { text, kind: kindValue(), answering: state.answering, reply: state.dmReply, agent: state.dmAgent, agent_id: state.dm ? "" : state.deviceAgentID || "", files: state.files, typedFor: state.typedFor, mentions: validMentions(state.mentions, text), reply_receiver: state.replyReceiver, ...(state.replyReceiverHost ? { reply_receiver_host: state.replyReceiverHost } : {}) };
  }
  else delete state.drafts[state.draftKey];
}

function restoreDraft(key, t) {
  const d = state.drafts[key];
  $("body").value = d ? d.text : "";
  state.mentions = validMentions(d && d.mentions, $("body").value); // exact as kept, or dropped visibly, never matched by name
  if (d && Array.isArray(d.mentions) && state.mentions.length < d.mentions.length) announce("Some mentions in this draft are no longer exact; their text stays.");
  state.mentionText = $("body").value;
  state.typedFor = (d && d.typedFor) || null;
  state.replyReceiver = d && d.reply_receiver || null;
  state.replyReceiverHost = d && d.reply_receiver_host || null;
  state.receiverCatalog = null;
  state.files = (d && d.files) || [];
  state.deviceAgentID = !state.dm && d && d.agent_id || "";
  renderPending();
  grow();
  setKind(d ? d.kind : "message");
  // Answer only what can still be answered by hand.
  const m = d && d.answering && t.messages.find((x) => x.id === d.answering.id && (x.actions || []).includes("reply"));
  setAnswering(m || null);
  // A DM's reply target comes back only if that message is in this DM.
  const r = state.dm && d && d.reply && t.messages.find((x) => x.id === d.reply.id);
  setDMReply(r || null);
  // An agent target comes back as it was, even when that agent cannot be
  // asked any more: the person, not a dismissal, moves the text elsewhere.
  const a = state.dm && d && d.agent && ((t.agents || []).find((x) => x.pid === d.agent) || { pid: d.agent });
  setDMAgent(a || null);
  if (a && d.kind === "task") setKind("task");
}

// syncComposer enables what the open conversation allows. A send on its way
// keeps Send disabled, whatever refreshes meanwhile.
function syncComposer() {
  $("mention-button").hidden = !state.dm || dmHumanGuest() && !guestAuthor(); // a guest mentions only while joined
  $("mention-button").disabled = state.sending;
  $("mention-button").setAttribute("aria-label", "Mention a person or assistant");
  $("mention-button").title = "Mention a person or assistant";
  if (state.dm) { // a DM: messages only, to the person
    const d = state.dmData;
    const revoked = !!(state.overview && state.overview.device && state.overview.device.revoked);
    const blocked = !d || !!d.frozen || revoked || (dmHumanGuest(d) && !guestAuthor(d)) || (dmVisitor(d) && !selectedAgentPIDs().some(pid => agentOf(pid)?.can_ask));
    $("composer").hidden = dmVisitor(d) && blocked;
    $("body").disabled = blocked;
    $("send").disabled = blocked || state.sending || askGone();
    const a = state.dmAgent && agentOf(state.dmAgent);
    // Asking an agent: a question, or a task when the invitation lets you give it tasks.
    $("kind").hidden = true;
    $("kind").disabled = blocked || state.sending || askGone();
    for (const r of root.querySelectorAll('input[name="kind"]')) {
      const l = r.value === "message" && r.closest && r.closest("label");
      if (l) l.hidden = !!state.dmAgent;
    }
    if (a && kindValue() === "message") setKind("question");
    $("attach").hidden = !filesAllowed();
    $("body").placeholder = !d ? "" : revoked ? "This device was removed from its server" : d.frozen ? "Nothing more can be sent in this conversation"
      : askGone() ? "This agent cannot be asked now" : a ? "Ask " + agentName(a).replace(/^Your/, "your") : dmHumanGuest(d) ? "Message " + joinedNames(guestAudienceNames(d)) : "Write to " + (humanGroup(d) ? d.title : d.peer.label);
    kindHint();
    renderTarget();
    return;
  }
  $("kind").hidden = true;
  $("attach").hidden = !filesAllowed();
  const t = state.data;
  // Question and Task only toward a device that runs an agent: a phone or a
  // browser is its person, who gets a message.
  const asks = !!t && runsAgent(t.peer);
  for (const r of root.querySelectorAll('input[name="kind"]')) { const l = r.closest && r.closest("label"); if (l) l.hidden = r.value !== "message" && !asks; }
  if (t && !asks && kindValue() !== "message" && !state.answering) setKind("message");
  const blocked = !t || !!t.key.pending;
  $("body").disabled = blocked;
  $("send").disabled = blocked || state.sending || deviceAgentMissing();
  $("kind").disabled = blocked || !!state.answering;
  $("body").placeholder = !t ? "" : t.key.pending ? "Sending is blocked until you trust the new key" : "Write to " + whoText(t.peer);
  renderTarget();
}

function grow() {
  const t = $("body");
  t.style.height = "auto";
  t.style.height = Math.min(t.scrollHeight, window.innerHeight * 0.4) + "px";
}

async function send(ev, retry) {
  ev?.preventDefault();
  if (!retry && state.dm) { await sendDM(); return; }
  const t = retry?.t || state.data;
  if (state.sending || !t || t.key.pending) return; // only a loaded conversation with its trusted device key
  // Everything this send needs is fixed now: switching conversation or a
  // refresh while it is on its way changes none of it.
  let key = retry?.key || state.draftKey, text = retry ? retry.text : $("body").value, answering = retry ? retry.answering : state.answering, files = retry ? retry.files : answering ? [] : state.files.slice();
  const group = retry?.group || "";
  const id = retry?.id || sendID(), mentions = retry?.mentions || (state.mentions || []).slice();
  const newTopic = retry ? retry.newTopic : !!topicFresh[t.id];
  const last = sends.merge(key, t.messages).at(-1);
  const topicRoot = retry ? retry.topicRoot : newTopic ? id : last?._topicRoot || "";
  const draft = retry?.draft ? {...retry.draft,id} : { id, to: t.peer, kind: kindValue(), body: text, reply_to: newTopic?"":last ? last.id : "" };
  const agentID = retry ? draft.agent_id || "" : !answering && draft.kind !== "message" ? state.deviceAgentID || "" : "";
  if (agentID) draft.agent_id = agentID;
  if (!retry && agentID && deviceAgentMissing()) { $("compose-error").textContent = "Selected agent unavailable. Refresh agents or choose the device default; your draft stays here."; return; }
  const elsewhere = retry ? "" : boundElsewhere();
  if (elsewhere) { $("compose-error").textContent = elsewhere; return; }
  if (!retry && answering && state.files.length) { $("compose-error").textContent = noFilesWhy(); return; }
  if (!retry && files.length && overLimit(files)) { $("compose-error").textContent = overLimit(files); return; }
  // The membership this send is for: its host carries it, and only its own
  // drafts are touched when it is done, wherever the person is by then.
  const host = currentHost, ws = wsNow(), receiverSelection = retry ? retry.receiverSelection : state.replyReceiver && { ...state.replyReceiver }, nativeReceiver = retry ? retry.nativeReceiver : !!state.overview?.reply_receivers, nativeSessions = retry ? retry.nativeSessions : !!state.overview?.reply_sessions, receiverContext = retry ? retry.receiverContext : receiverCapture();
  if (typingUI) typingUI.stop();
  if (!retry && newTopic) topicFresh[t.id] = false;
  startSend(key, text, files, null, answering, t, answering ? "answer" : draft.kind, () => { sends.remove(id); void send(null, {id,key,text,files,answering,t,newTopic,topicRoot,mentions,draft,receiverSelection,nativeReceiver,nativeSessions,receiverContext}); }, id, "", topicRoot);
  if (alive) { syncComposer(); $("compose-error").textContent = ""; }
  try {
    await sends.ready(id);
    let r;
    if (answering) {
      await act({ do: "reply", id: answering.id, send_id: id, body: text }, host);
    } else {
      if (agentID) {
        const agents = await readAgentCatalog(t.peer, host);
        if (!agents.some(a => a.id === agentID)) {
          if (alive && wsNow() === ws && state.draftKey === key) state.targetCatalog = { host: t.peer, agents };
          throw new Error("Selected agent is no longer available on this computer. Nothing sent; no default was chosen.");
        }
      }
      const receiver = await prepareReplyReceiverSelection(receiverSelection, host, ws, nativeReceiver, nativeSessions, receiverContext);
      if (receiver) draft.reply_receiver = receiver;
      if (files.length) draft.files = await preparedFiles(files, host); // a failure here keeps what was handed over, for the retry
      try { r = await api("/api/send", draft, host); } finally { sentStaged(files); }
      if (topicRoot) sends.move(id, topicRoot);
      if (newTopic && alive && wsNow() === ws && state.draftKey === key) void openThread(r.id);
    announce(r.state === "receiver_waiting" ? r.detail || "Waiting for the selected reply host to accept this exact request." : r.state === "queued" ? "Sending…" : "Sent.");
    }
    sends.finish(id, r);
    files.forEach(f => f.url && URL.revokeObjectURL(f.url));
    if (alive && wsNow() === ws) void loadThread();
  } catch (e) {
    failSend(id,key,ws,text,files,null,answering,e.message,mentions);
  } finally {
    if (alive && wsNow() === ws) { state.sending = false; syncComposer(); if (state.newVersion) updated(state.newVersion); }
    else { state.sending = false; sentElsewhere(ws); }
  }
}

function kindHint() {
  if (state.dm) {
    const a = state.dmAgent && agentOf(state.dmAgent);
    if (askGone()) {
      $("compose-hint").textContent = (a ? agentName(a) + " cannot be asked now" + (a.state_text ? " (" + a.state_text + ")" : "") : "That agent is no longer in this DM") +
        ". Your text stays here. To send it to " + (state.dmData ? state.dmData.peer.label : "the person") + " instead, remove the agent (×) first.";
      return;
    }
    if (!a && dmHumanGuest()) { $("compose-hint").textContent = guestAuthor() ? "Chat and files. Ctrl+Enter sends." : (state.dmData.guests || []).some(g => g.host_here && g.state === "invited") ? "Join before sending. Your draft stays here." : "Sending is unavailable here. Your draft stays here."; return; }
    $("compose-hint").textContent = humanGroup() && !a ? (state.dmData.frozen ? "Current group audience unavailable. Saved messages remain; nothing can be sent here now." : "Messages go to the current group members; nothing runs them. Ctrl+Enter sends.") : !a ? "A DM message is for the person; nothing runs it. Ctrl+Enter sends."
      : kindValue() === "task" ? (taskFree(a) ? "It runs on " + a.host.address + " without asking " + (a.host_here ? "you" : a.host.label) + " first."
        : a.host.label + " accepts it first, unless they already allow tasks from your key.") + " Ctrl+Enter sends."
        : (humanGroup() ? "Current group members see what you ask and what it answers. It runs on "
          : dmHumanGuest() ? "Everyone in this conversation sees what you ask and what it answers. " + a.host.label + "'s permissions decide whether it runs, on "
          : (state.dmData.guests || []).some(g => g.state === "active") ? "Everyone in this conversation sees what you ask and what it answers. It runs on "
          : "Both of you see what you ask and what it answers. It runs on ") + a.host.address + ". Ctrl+Enter sends.";
    return;
  }
  if (state.deviceAgentID && !state.answering && kindValue() !== "message") {
    $("compose-hint").textContent = deviceAgentMissing() ? "Selected agent unavailable. Your draft stays here; refresh agents or choose the device default."
      : kindValue() === "task" ? "Its owner accepts the task first, unless they already allow tasks from you." : "This agent may answer automatically if its owner approved you.";
    return;
  }
  $("compose-hint").textContent = state.answering
    ? "Your reply answers this " + state.answering.kind + " and takes it over from your responder. Ctrl+Enter sends."
    : {
      message: "A message never runs anything. Ctrl+Enter sends.",
      question: "Their responder may answer automatically if they approved you.",
      task: "A task runs only if they accept it, once or by standing permission for your key.",
    }[kindValue()];
}

// ---- workspaces ------------------------------------------------------------------------
//
// The host (loader.js) keeps one immutable transport per membership and
// says which one is shown. This page keeps one view per membership: the
// open conversation, drafts, what was seen, presence, a send under way.
// Switching puts the view of the workspace left into the host's state for
// it and takes the next one out; anything asynchronous started before the
// switch belongs to a generation that is over and draws nothing here.
const wsNow = () => currentHost.workspace.id;
const wsAPI = () => (currentHost && currentHost.workspaces) || null;
// savedOf is the view kept for a membership not shown now (null if none).
function savedOf(id) {
  const w = wsAPI();
  if (!w || id === wsNow() && alive) return null;
  try { return w.state(id)[skinKey] || null; } catch (e) { return null; } // disconnected: nothing kept
}
// draftsOf: a membership's drafts, shown or kept; {} for one no longer here.
const draftsOf = (id) => (alive && id === wsNow() ? state.drafts : (savedOf(id) || {}).drafts || {});
function sentElsewhere(id) { const s = savedOf(id); if (s) s.sending = false; }
const viewKeys = ["thread", "dm", "drafts", "lastSeen", "presence", "seenReported", "singlesOpen", "version", "newVersion", "contactView", "contactLimit"];
// workspaceCapture keeps the view shown now for the membership id, in the
// host's state for it (before the selection changes; once).
function workspaceCapture(id, st) {
  // Join/disconnect can capture without switching. Refresh that still-active
  // draft at the actual pre-switch hook; only suppress the switched fallback.
  if (!st || state.capturedFor === id && id !== wsNow()) return;
  keepDraft();
  const view = {};
  for (const k of viewKeys) view[k] = state[k];
  view.scroll = $("timeline").scrollTop;
  view.zoom = {level: Zoom.level, peer: Zoom.peer, person: Zoom.person, dm: Zoom.dm, msg: Zoom.msg, query: Zoom.query};
  st[skinKey] = view;
  state.capturedFor = id;
}
// workspaceSwitched shows the membership selected now: its kept view, or
// a fresh one, loaded over its own host.
// switchWorkspace shows another membership and waits for its view.
async function switchWorkspace(id) {
  const w = wsAPI();
  if (!w || !w.has(id)) throw new Error("That workspace is not on this device.");
  if (w.active() !== id) w.select(id);
  if (state.switching) await state.switching;
}

// ---- push ------------------------------------------------------------------------

// One event stream; each event carries only a change counter. When it
// breaks, say so and wait for the person instead of retrying on a timer.
let stopListen = null; // closes the stream listened to now
function listen() {
  stopListen?.();
  const host = currentHost;
  stopListen = host.listen((event) => {
    if (!alive || currentHost !== host) return;
    if (event.type === 'change') { const first = state.seq < 0; state.seq = event.seq; refetch(first); }
    else if (event.type === 'restart') { state.updating = true; $('updating').hidden = false; $('lost').hidden = true; recover(recovery.update); }
    else { typingUI?.disconnect(); $('lost').hidden = false; recover(recovery.missed); }
  });
}

// recovery: waits (ms) between reconnection attempts, bounded. They happen
// only after the stream breaks; otherwise the page never asks.
const recovery = { update: [500, 1000, 2000, 4000, 8000, 15000, 30000], missed: [1000, 3000] };
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

async function recover(schedule) {
  for (const wait of schedule) {
    await pause(wait);
    if (await reconnect()) return;
  }
  if (!alive) return;
  if (state.updating) {
    state.updating = false;
    $("updating").hidden = true;
    $("lost").hidden = false;
    announce("AgentNet did not come back at this address. Run agentnet ui for the address to open, or agentnet update --status.");
  }
}

// reconnect loads the page's data again and listens; it reports success.
async function reconnect() {
  const host = currentHost, gen = state.gen, workspace = wsNow();
  try {
    await loadOverview();
  } catch (e) {
    if (e.status !== 409 || e.message !== "stale or disconnected workspace" || !host.reconnect || state.sending || state.dialogBusy) return false;
    try {
      // Native staged IDs live in the old daemon. Keep text/file metadata,
      // but require explicit reattachment when the host mounts a new root.
      for (const files of [state.files, ...Object.values(state.drafts).map((d) => d.files || [])]) {
        for (const f of files) if (f.staged) { f.staged = ""; f.reattachRequired = true; }
      }
      await host.reconnect(); // identity-checked rebind + remount; no returned host
      if (!alive) return true; // replacement root owns reconnection now
      if (gen !== state.gen || currentHost !== host || wsNow() !== workspace) return false;
      await loadOverview();
      renderPending();
      if (state.files.some((f) => f.reattachRequired)) $("compose-error").textContent = overLimit(state.files);
    } catch (err) { if (alive) announce(err.message); return false; }
  }
  if (gen !== state.gen || wsNow() !== workspace) return false;
  try {
    if (state.thread) await loadThread(false);
    else if (state.dm) await loadDM(false);
  } catch (_) { return false; }
  state.updating = false;
  $("lost").hidden = true;
  if (!state.newVersion) $("updating").hidden = true;
  listen();
  return true;
}

// Reload persistence keeps only part of a draft. Keep every unsent draft
// in memory until the person sends or clears it, including other workspaces.
function hasUnsentDrafts() {
  const pending = (d) => d && (d.text || d.answering || d.reply || d.agent || d.agent_id || d.reply_receiver || d.reply_receiver_host || d.typedFor || (d.files && d.files.length));
  if ($("body").value || state.answering || state.dmReply || state.dmAgent || state.deviceAgentID || state.replyReceiver || state.replyReceiverHost || state.typedFor || state.files.length ||
    Object.values(state.drafts).some(pending) || (state.dialogRestore && $("dialog").open)) return true;
  const w = wsAPI();
  if (w) {
    try {
      for (const { id } of w.list()) {
        if (id === wsNow()) continue;
        const view = w.state(id)[skinKey];
        if (w.state(id)[pendingKey]?.busy || view && Object.values(view.drafts || {}).some(pending)) return true;
      }
    } catch (_) { return true; } // an uninspected saved view must not be discarded
  }
  return false;
}

// updated uses the new daemon now, but reloads its page only when no draft
// can be lost and no send or confirmation is on its way.
function updated(v) {
  state.newVersion = v;
  if (state.sending || state.dialogBusy) return; // retried when they finish
  if (hasUnsentDrafts()) {
    $("updating").hidden = false;
    $("updating-text").textContent = "AgentNet was updated to " + v + ". Your unsent drafts stay in this window. Send or clear drafts in every workspace before reloading.";
    $("reload").hidden = false;
    $("reload").disabled = true;
    return;
  }
  $("reload").disabled = false;
  if (keepForReload()) { location.reload(); return; }
  $("updating").hidden = false;
  $("updating-text").textContent = "AgentNet was updated to " + v + ". Reload to use it; this browser could not keep your unsent text for the reload, so copy it first.";
  $("reload").hidden = false;
}

function reloadUpdated() {
  if (state.sending || state.dialogBusy || hasUnsentDrafts()) {
    announce("Send or clear your unsent drafts in every workspace before reloading.");
    return false;
  }
  location.reload();
  return true;
}

const reloadKey = "agentnet." + skinKey + ".reload." + currentHost.workspace.id;

// keepForReload stores unsent text: every conversation's draft, the
// composer, and an open dialog's fields (not its consent boxes).
function keepForReload() {
  keepDraft();
  const drafts = {};
  for (const [k, d] of Object.entries(state.drafts)) {
    drafts[k] = { text: d.text, kind: d.kind, answering: d.answering ? d.answering.id : null, reply: d.reply ? d.reply.id : null, mentions: (d.mentions || []).map(({ start, name, ref }) => ({ start, name, ref })), reply_receiver: d.reply_receiver || null, ...(d.reply_receiver_host ? { reply_receiver_host: d.reply_receiver_host } : {}) };
  }
  const fields = {};
  if (state.dialogRestore && $("dialog").open) {
    for (const f of $("dialog-body").querySelectorAll("input[type=text], textarea, select")) if (f.id) fields[f.id] = f.value;
  }
  const keep = { workspace: wsNow(), drafts, thread: state.thread, dm: state.dm, dialog: state.dialogRestore && $("dialog").open ? state.dialogRestore : null, fields };
  try {
    const text = JSON.stringify(keep);
    sessionStorage.setItem(reloadKey, text);
    return sessionStorage.getItem(reloadKey) === text;
  } catch (e) {
    return false;
  }
}

// restoreAfterReload puts back what keepForReload stored, once.
async function restoreAfterReload() {
  let keep = null;
  try {
    keep = JSON.parse(sessionStorage.getItem(reloadKey) || "null");
    sessionStorage.removeItem(reloadKey);
  } catch (e) { /* nothing kept */ }
  if (!keep) return false;
  if (keep.workspace && keep.workspace !== wsNow()) { // back in the workspace it was kept for, if that is still here
    try { await switchWorkspace(keep.workspace); } catch (e) { return false; }
  }
  for (const [k, d] of Object.entries(keep.drafts || {})) {
    state.drafts[k] = { text: d.text, kind: d.kind, answering: d.answering ? { id: d.answering } : null, reply: d.reply ? { id: d.reply } : null, mentions: d.mentions || [], reply_receiver: d.reply_receiver || null };
  }
  if (keep.thread) await openThread(keep.thread);
  else if (keep.dm) await openDM(keep.dm);
  const r = keep.dialog;
  if (r) {
    const m = r.msg && state.data ? state.data.messages.find((x) => x.id === r.msg) : null;
    if (r.type === "write" && state.data) writeDialog(state.data, m);
    else if (r.type === "dmwrite" && state.dmData) dmWriteDialog(state.dmData);
    else if (r.type === "new") newConversationDialog(r.prefill);
    else if (r.type === "person") personDialog();
    else if (r.type === "decline" && m) decide("decline", m, state.data);
    for (const [id, v] of Object.entries(keep.fields || {})) { const f = root.querySelector("#" + CSS.escape(id)); if (f) f.value = v; }
  }
  return true;
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
      const gen = state.gen;
      const o = await loadOverview();
      if (gen !== state.gen) break; // the workspace changed: its own switch loads the next view
      if (mentionView || state.contactView === "people" || (state.hub && state.hub.kind === "team")) await loadTeams(); // the change stream said something changed; teams are read again, never polled
      if (state.thread) await loadThread(false);
      else if (state.dm) await loadDM(false);
      Zoom.refresh();
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
  for (const d of o.dms || []) {
    if (!first && state.lastSeen["dm:" + d.id] !== d.last_at) changed.push(d.peer.label);
    state.lastSeen["dm:" + d.id] = d.last_at;
  }
  if (changed.length) announce("New activity with " + [...new Set(changed)].join(", "));
}

// Main navigation belongs to this skin. Identity and every action still come
// from the same provider on both platforms.
function selectSection(section) {
  keepDraft();
  if (section === "people") loadTeams();
  state.contactView = section === "people" ? "people" : "recent";
  state.contactLimit = 20;
  state.query = "";
  $("search").value = "";
  $("section-title").textContent = section === "people" ? "People" : "Chats";
  $("search").placeholder = section === "people" ? "Find people or devices" : "Search conversations";
  for (const name of ["chats", "people"]) {
    $("nav-" + name).classList.toggle("selected", name === section);
    $("nav-" + name).setAttribute("aria-current", name === section ? "page" : "false");
  }
  toggleReview(false);
  root.classList.remove("show-conv");
  void Zoom.go(0, {});
  rerender();
}

async function editProfilePicture(current, src) {
  const host = currentHost, gen = state.gen;
  if (await openPictureEditor({ into: root, current, src, save: png => api("/api/person/picture", { png }, host) }) && gen === state.gen) { await loadOverview(); renderProfile(state.overview); }
}

function renderProfile(o) {
  const p = o.person;
  fill($("profile-initial"), p?.picture_url ? el("img", { src: p.picture_url, alt: "", class: "profile-picture-image" }) : (p ? p.label : o.me.address).charAt(0).toUpperCase());
  fill($("profile-card"), p ? el("div", { class: "profile-card" }, avatar(p.label),
    el("div", {}, el("h3", {}, p.label), p.email && el("p", { class: "hint" }, p.email + " · verified by this workspace"), el("p", { class: "hint" }, "One person, " + plural(devicesOf(p).length, "device", "devices")),
      p.person && el("p", { class: "hint", title: "Person ID: " + p.person }, "@" + p.person.slice(0, 8))))
    : setupChoice());
  fill($("profile-devices"), p ? deviceDisclosure(p, (addr) => { $("settings").close(); openHub({ kind: "device", key: addr }); }) : null,
    p && p.state === "self" && el("button", { type: "button", class: "btn", onclick: () => renamePersonDialog(p) }, "Change display name"),
    p && p.state === "self" && el("button", { type: "button", class: "btn", onclick: () => editProfilePicture(p.picture_url) }, "Choose picture"),
    p && p.state === "self" && p.picture && el("button", { type: "button", class: "btn", onclick: async () => { try { await api("/api/person/picture", { png: "" }); await loadOverview(); renderProfile(state.overview); } catch(e) { dialog({ title: "Picture not removed", body: [el("p", {}, e.message)], ok: "Close", run: async () => {} }); } } }, "Remove picture"),
    p && el("button", { type: "button", class: "btn", onclick: () => { $("settings").close(); devicesDialog(); } }, "Manage your devices"));
}

// renderResponder is the Agent settings: which harness answers here, or
// none, from GET /api/responder; a change goes through POST /api/responder
// and shows exactly what the daemon says back. A browser device has no
// responder and says so. "Installed" is found on this computer's PATH:
// never logged in or working.
async function renderResponder() {
  const box = $("responder");
  const host = currentHost, gen = state.gen, ws = wsNow();
  const currentView = () => gen === state.gen && wsNow() === ws;
  if ((host && host.platform === "browser") || (state.overview && state.overview.device)) {
    fill(box, el("p", { class: "hint" }, "This browser runs nothing: questions and tasks wait for you. Choose an agent on a computer with AgentNet."),ownModelReports());
    fill($("named-agents"));
    return;
  }
  let r;
  try { r = await api("/api/responder", undefined, host); } catch (e) {
    if (!currentView()) return;
    fill(box, el("p", { class: "hint" }, /404/.test(e.message) ? "This AgentNet does not offer agent settings on the page yet: run agentnet help responder on this computer." : e.message));
    await renderNamedAgents("", host, gen);
    return;
  }
  if (!currentView()) return;
  const found = (r.harnesses || []).filter((h) => h.found);
  const options = [{ value: "manual", label: "No automatic answers: questions and tasks wait for me" },
    ...(r.harnesses || []).map((h) => ({ value: h.name, label: h.name + (h.found ? " · installed" : " · not found on this computer"), disabled: !h.found }))];
  const current = !r.chosen ? "" : r.manual ? "manual" : r.harness;
  const dir = el("input", { type: "text", id: "responder-dir", value: r.dir || "", placeholder: "Folder it works in (absolute path)" });
  const err = el("p", { class: "error", role: "alert" });
  const save = async (value) => {
    const body = value === "manual" ? { manual: true } : { harness: value, dir: dir.value.trim() || undefined };
    err.textContent = "";
    try {
      if (!currentView()) throw new Error("Workspace changed. Reopen Agent settings there.");
      const res = await api("/api/responder", body, host);
      if (!currentView()) return;
      announce(res.note || "Saved.");
      await loadOverview();
      renderResponder();
    } catch (e) { if (currentView()) err.textContent = e.message; }
  };
  fill(box,
    el("h3", {}, "Answers for you"), ownModelReport(state.overview?.me.address),ownModelReports(),
    el("p", {}, !r.chosen ? "Nothing chosen yet" + (r.problem ? ": " + r.problem : ".") : r.manual ? "No automatic responder: everything waits for you."
      : r.harness + " answers approved questions and runs accepted tasks in " + r.dir + (r.ready ? "." : ". Not ready: " + (r.problem || "check the folder and the program."))),
    el("div", { class: "responder-choice", role: "radiogroup", "aria-label": "Your agent" }, options.map((o) =>
      el("label", { class: o.disabled ? "off" : "" }, el("input", { type: "radio", name: "responder", value: o.value, checked: o.value === current, disabled: o.disabled,
        onchange: () => { if (o.value === "manual") save("manual"); } }), " ", o.label))),
    el("label", { class: "field-label", for: "responder-dir" }, "Works in"), dir,
    el("p", { class: "hint" }, "Requests from other people start in this folder. Your own sessions stay as they are. Tasks usually change files here, within your normal permissions."),
    el("div", { class: "detail-actions" }, el("button", { type: "button", class: "btn primary", onclick: () => {
      const chosen = box.querySelector('input[name="responder"]:checked');
      if (!chosen) { err.textContent = "Choose an agent, or no automatic answers."; return; }
      save(chosen.value);
    } }, "Save")),
    !found.length && el("p", { class: "hint" }, "No supported agent program was found on this computer's PATH as AgentNet runs it."),
    err);
  await renderNamedAgents("", host, gen);
}

// The existing installer table and device link keep the installed computer
// under the same person. A link requests admission; it never approves it.
async function renderBrowserApp(box, host, currentView) {
  const section = el("section", {"aria-label":"Connect an agent on your computer"},
    el("p", {}, "Install AgentNet on the computer where your coding agent runs. Add it to your existing person, then choose your agent and working folder in Settings → Agent."));
  const error = el("p", {class:"error",role:"alert"});
  const linked = el("div", {});
  const make = el("button", {type:"button",class:"btn primary",onclick:async()=>{
    if (!currentView() || make.disabled) return;
    make.disabled=true;error.textContent="";
    try {
      const link=await api("/api/device/link",{},host);
      if (!currentView()) return;
      fill(linked, link.app_url && el("a",{href:link.app_url,class:"btn"},"Open this link in AgentNet"),
        el("p",{class:"hint"},"Return here to approve the new computer in Settings → Your devices. Your chats stay with the same person. This one-use link expires at "+new Date(link.expires).toLocaleTimeString()+"."),
        el("details",{},el("summary",{},"App opened without the link?"),el("p",{},"Copy this link and paste it into the installed app:"),el("code",{style:"display:block;overflow-wrap:anywhere;user-select:all"},link.url)));
      make.textContent="Make a new app link";
    } catch(e) { if(currentView())error.textContent=e.message; }
    finally {make.disabled=false;}
  }},"Link the installed app to me");
  section.append(make,linked,error);box.append(section);
  try {
    const app=await api("/api/get-app",undefined,host);if(!currentView())return;
    const mine=(app.platforms||[]).find(p=>p.id===app.detected),others=(app.platforms||[]).filter(p=>p!==mine);
    if(mine)section.insertBefore(el("a",{href:mine.url,class:"btn",rel:"noopener"},"Get AgentNet for "+mine.label),make);
    if(others.length)section.insertBefore(el("details",{},el("summary",{},mine?"Other systems":"Choose your computer"),el("ul",{},...others.map(p=>el("li",{},el("a",{href:p.url,rel:"noopener"},p.label))))),make);
  }catch(e){if(currentView())error.textContent=e.message;}
}

async function renderNamedAgents(note = "", host = currentHost, gen = state.gen, detail = "") {
  const box = $("named-agents"), ws = wsNow();
  const current = () => gen === state.gen && wsNow() === ws;
  let view;
  try { view = await api("/api/agents", undefined, host); }
  catch (e) { if (current()) fill(box, el("p", { class: "hint" }, "Named agent settings unavailable: " + e.message)); return; }
  if (!current()) return;
  if (!view.local) { fill(box, el("p", { class: "hint" }, "Local agent configuration belongs on a native AgentNet computer.")); return; }
  const change = async body => {
    if (!current()) throw new Error("Workspace changed. Reopen Agent settings there.");
    const result = await api("/api/agents", body, host);
    const outcome = result.saved ? result.published ? "Saved on this computer. Agent list updated for others." : "Saved on this computer. Agent list not updated for others. Choose Update agent list to try again." : "Save on this computer not confirmed.";
    if (current()) await renderNamedAgents(outcome, host, gen, result.note || "");
  };
  const edit = entry => {
    const r = entry && entry.responder;
    const harness = el("select", { id: "named-harness" }, el("option", { value: "" }, "Choose installed program"),
      (view.harnesses || []).map(h => el("option", { value: h.name, disabled: !h.found, selected: r && r.harness === h.name }, h.name + (h.found ? " · found" : " · not found"))));
    const dir = el("input", { type: "text", id: "named-dir", value: r && r.dir || "", placeholder: "Absolute working folder" });
    const label = !entry && el("input", { type: "text", id: "named-label", maxlength: "64", autocomplete: "off" });
    const records = (view.agents || []).map(a => a.record);
    dialog({ title: entry ? "Configure " + namedAgentLabel(entry.record.id, view.host, records) : "Create an agent on this computer", ok: "Save agent",
      body: [entry ? el("p", { title: entry.record.id + " · " + view.host }, catalogLabel(entry.record, records)) : [el("label", { for: "named-label", class: "field-label" }, "Agent name"), label],
        el("label", { for: "named-harness", class: "field-label" }, "Local program"), harness,
        el("label", { for: "named-dir", class: "field-label" }, "Works in"), dir,
        el("p", { class: "hint" }, "Choose the program and folder this agent works in. Saving also tries to make it available to others. Installed program and folder checks do not test sign-in or model availability. Saving does not run the agent."),
        entry && el("p", { class: "hint" }, "Its context files (and any time limit you set) are kept.")],
      run: async () => {
        if (!harness.value || !dir.value.trim() || (!entry && !label.value.trim())) throw new Error("Choose an installed program, an absolute working folder and a display label for a new agent.");
        await change({ action: entry ? "update" : "create", ...(entry ? { id: entry.record.id } : { label: label.value.trim() }), harness: harness.value, dir: dir.value.trim() });
      } });
  };
  const records = (view.agents || []).map(a => a.record);
  fill(box, el("h3", {}, "Agents on this computer"),
    el("p", { class: "hint" }, "Give each agent a name, program and working folder. Others can choose available agents; you still decide which tasks may run. Program and folder checks do not test sign-in or model availability."),
    note && el("p", { class: "named-agent-outcome", role: "status", title: detail }, note),
    (view.agents || []).map(entry => {
      const r = entry.responder;
      return el("section", { class: "named-agent-card", "aria-label": "Local agent " + namedAgentLabel(entry.record.id, view.host, records) },
        el("strong", { title: entry.record.id + " · " + view.host + " (host-signed agent)" }, namedAgentLabel(entry.record.id, view.host, records)), el("p", {}, "On " + view.host),
        ownModelReport(view.host,entry.record.id),
        el("p", { class: "hint" }, !entry.enabled ? "Disabled on this computer. Earlier messages remain." : r ? "Program: " + r.harness + " · " + r.dir + (r.ready ? " · program/folder checks pass" : " · not ready: " + (r.problem || "check program and folder")) : "Program settings unavailable."),
        r && el("p", { class: "hint" }, (r.timeout_seconds ? "Your time limit: " + r.timeout_seconds + " seconds · " : "") + "Context files: " + ((r.context || []).join(", ") || "none")),
        el("div", { class: "detail-actions" }, el("button", { type: "button", class: "btn", onclick: () => edit(entry) }, "Configure…"),
          entry.enabled && el("button", { type: "button", class: "text-btn", onclick: () => dialog({ title: "Disable " + namedAgentLabel(entry.record.id, view.host, records) + "?", ok: "Disable locally",
            body: [el("p", {}, "Disables this agent on this computer and tries to update the agent list for others. Earlier messages remain. Drafts for this agent keep their recipient.")], run: () => change({ action: "disable", id: entry.record.id }) }) }, "Disable…")));
    }),
    !(view.agents || []).length && el("p", { class: "hint" }, "No agents set up on this computer."),
    el("div", { class: "detail-actions" }, el("button", { type: "button", class: "btn", onclick: () => edit(null) }, "Create agent…"),
      el("button", { type: "button", class: "btn", onclick: async () => { try { await change({ action: "publish" }); } catch (e) { if (current()) fill(box, el("p", { class: "error", role: "alert" }, e.message), el("button", { type: "button", class: "btn", onclick: () => renderNamedAgents("", host, gen) }, "Refresh agents")); } } }, "Update agent list")));
}

// ---- Storage: where files live, how long, what remains (STORAGE1-8) ----
//
// One explicit read of GET /api/storage (the daemon: its five managed
// folders and the Hub's own-usage report; a browser device: its stored
// records and the same Hub report). Shown as the API words it: known
// counts only, "unknown" never zero, file lengths not disk blocks, the
// Hub's quota is the whole server's, never your allowance. Nothing here
// cleans, changes or promises retention.

const bytesText = (n) => (n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : n < 1073741824 ? (n / 1048576).toFixed(1) + " MB" : (n / 1073741824).toFixed(2) + " GB");
const amountText = (u) => (u ? plural(u.files, "file", "files") + " · " + bytesText(u.bytes) : "unknown");

function storageArea(a) {
  return el("li", { class: "storage-area " + (a.status === "available" ? "" : "off") },
    el("div", { class: "storage-line" }, el("strong", {}, a.label), " ", el("span", { class: "tag" }, a.kind === "plaintext" ? "readable files" : "encrypted"),
      el("span", { class: "storage-amount" }, a.usage ? amountText(a.usage) : "unknown")),
    a.reason && el("p", { class: "hint" }, a.reason),
    el("details", { class: "tech" }, el("summary", {}, "Where and how long"),
      el("dl", {}, el("dt", {}, "Folder"), el("dd", { class: "mono" }, a.directory), el("dt", {}, "Kept"), el("dd", {}, a.lifetime))));
}

function storageRemote(r) {
  if (!r || r.status !== "available" || !r.usage) {
    return el("div", { class: "storage-block" }, el("h4", {}, "On your server"),
      el("p", { class: "hint" }, r && r.status === "unsupported" ? (r.reason || "The workspace’s server does not report storage usage.")
        : (r && r.reason) || "Not known now: your server did not answer. What is shown above is this device's own view."));
  }
  const u = r.usage, own = u.own || {}, stored = own.stored || {}, inc = own.incomplete || {};
  const bucket = (b) => plural(b.files || 0, "file", "files") + " · " + bytesText(b.reserved_bytes || 0) + " reserved";
  const pol = u.policy || {};
  const days = (sec) => (sec ? plural(Math.round(sec / 86400), "day", "days") : "");
  return el("div", { class: "storage-block" }, el("h4", {}, "On your server"),
    el("p", {}, "Your encrypted files there: ", el("strong", {}, bucket(stored)), (inc.files ? "; unfinished uploads: " + bucket(inc) : ""), "."),
    el("p", { class: "hint" }, "Reserved bytes are what counts against the server's quota, not disk blocks. The server holds only ciphertext."),
    el("p", {}, "The whole server's quota is " + bytesText(u.quota_bytes) + " for everyone together; that is not your allowance, and what is free for you cannot be told from it. Largest file: " + bytesText(u.max_file_bytes) + "."),
    u.global && el("p", {}, "Everyone on this server (you are an admin): " + bucket(u.global.stored || {}) + (u.global.incomplete && u.global.incomplete.files ? "; unfinished: " + bucket(u.global.incomplete) : "") + "."),
    el("details", { class: "tech" }, el("summary", {}, "How long the server keeps things"),
      el("dl", {},
        el("dt", {}, "Delivered files"), el("dd", {}, pol.delivered_attachments || "unknown"),
        el("dt", {}, "Undelivered files"), el("dd", {}, pol.undelivered_attachments || "unknown"),
        el("dt", {}, "Files never attached"), el("dd", {}, pol.unattached_attachments || "unknown"),
        el("dt", {}, "Messages"), el("dd", {}, pol.message_envelopes || "unknown"),
        el("dt", {}, "Unfinished uploads"), el("dd", {}, (pol.incomplete_uploads || "unknown") + (u.upload_idle_ttl_seconds ? " (idle limit " + Math.round(u.upload_idle_ttl_seconds / 3600) + " h)" : "")),
        el("dt", {}, "Operator cleanup defaults"), el("dd", {}, "Only when the operator runs it: delivered files older than " + (days(pol.manual_delivered_age_default_seconds) || "?") + ", unattached older than " + (days(pol.manual_unattached_age_default_seconds) || "?") + ". Nothing expires by itself."),
        el("dt", {}, "Where"), el("dd", {}, u.location || "the server's data"))));
}

// renderFileStorage mounts the optional provider setup (Settings > File
// storage options): the workspace admin's checklist and public client ids,
// off by default; never a Google login, never a command run. It speaks
// only to the membership shown when the tab opened.
async function renderFileStorage() {
  const box = $("file-storage");
  if (!box) return;
  const host = currentHost;
  fill(box, el("p", { class: "hint" }, "Reading…"));
  try {
    const m = await moduleOf("drivespace-setup");
    if (typeof m.mountFileStorageOptions !== "function") throw new Error("no setup module");
    fill(box);
    const provider = { storageSetup: (r) => api("/api/drive/setup", !r || !r.action || r.action === "status" ? undefined : r, host) };
    await m.mountFileStorageOptions(box, { provider });
  } catch (e) {
    fill(box, el("p", { class: "hint" }, "File storage options are not available on this AgentNet" + (e && e.message ? " (" + e.message + ")" : "") + ". Encrypted attachments work without them."));
  }
}

async function renderStorage() {
  const box = $("storage");
  fill(box, el("p", { class: "hint" }, "Reading…"));
  let v;
  try { v = await api("/api/storage"); } catch (e) {
    fill(box, el("p", { class: "hint" }, /404/.test(e.message) ? "This AgentNet does not report storage yet." : "Storage could not be read now."),
      el("button", { type: "button", class: "btn", onclick: renderStorage }, "Read again"));
    return;
  }
  const local = v.local || {}, areas = local.areas || [];
  const drafts = state.files.length ? plural(state.files.length, "file", "files") + " waiting in the composer (in memory only)" : "";
  const known = local.known || { files: 0, bytes: 0 };
  fill(box,
    el("div", { class: "storage-block" }, el("h4", {}, "On this " + (local.scope === "browser-device-records" ? "browser" : "computer")),
      el("p", {}, el("strong", {}, amountText(known)), local.complete ? " held by AgentNet here." : " counted; some of it could not be inspected, so the total is not complete."),
      local.location && el("p", { class: "hint" }, local.location + "."),
      drafts && el("p", { class: "hint" }, drafts + "."),
      local.browser && el("p", { class: "hint" }, local.browser),
      el("ul", { class: "storage-areas" }, areas.map(storageArea)),
      local.exclusions && el("p", { class: "hint" }, local.exclusions)),
    storageRemote(v.remote),
    el("div", { class: "detail-actions" }, el("button", { type: "button", class: "btn", onclick: renderStorage }, "Read again")));
}

async function renderAssistantSetup() {
  const root = $("assistant-setup"), host = currentHost, gen = state.gen, ws = wsNow();
  if (!root) return;
  const current = () => gen === state.gen && ws === wsNow() && host === currentHost;
  if (host?.platform === "browser" || !!state.overview?.device) {
    fill(root, el("h3", {}, "Connect an agent on your computer"));
    await renderBrowserApp(root, host, current);
    return;
  }
  try {
    const m = await moduleOf("assistant-setup");
    if (!current()) return;
    await m.mountAssistantSetup({root, suggestedHarness: state.overview?.me?.responder || "", api: (path, body) => api(path, body, host), isCurrent: current, isBrowser: host?.platform === "browser" || !!state.overview?.me?.browser, onChanged: async () => {
      if (!current()) return;
      try { await loadOverview(); if (!current()) return; state.dmNames = {}; if (state.dm) await loadDM(); }
      catch (e) { if (current()) announce("Setup saved; the participant list could not be refreshed: " + e.message); }
    }});
  } catch (e) { if (current()) fill(root, el("p", {class: "hint"}, "Assistant setup unavailable: " + e.message)); }
}
let appControlsRender = 0;
async function renderAppControls() {
  const host = currentHost, gen = state.gen, render = ++appControlsRender;
  const command = $("app-command"), update = $("app-update");
  if (!command || !update) return;
  fill(command); fill(update);
  if (host.platform === "browser" || !host.appStatus) return;
  const current = () => alive && host === currentHost && gen === state.gen && render === appControlsRender && $("settings").open;
  let status;
  try { status = await host.appStatus(); }
  catch (e) {
    if (current() && !/404|not found/i.test(e.message)) fill(update, el("p", {class:"error", role:"alert"}, e.message));
    return;
  }
  if (!current()) return;
  let busy = false, message = "", problem = "", checking = false, checked = null, checkProblem = "";
  const check = async () => {
    if (checking || busy || !host.appCheckUpdate || !current()) return;
    checking = true; checked = null; checkProblem = ""; paint();
    try { const result = await host.appCheckUpdate(); if (current()) checked = result; }
    catch (e) { if (current()) checkProblem = "Couldn’t check for updates: " + e.message; }
    finally { if (current()) { checking = false; paint(); } }
  };
  const act = async (replace) => {
    if (busy || !current()) return;
    busy = true; problem = ""; paint();
    try {
      if (replace) { const v = await host.appReplaceCommand(); if (current()) status = { ...status, ...v }; }
      else { const v = await host.appUpdate(); if (current()) message = v.message; }
    } catch (e) { if (current()) problem = e.message; }
    finally { if (current()) { busy = false; paint(); } }
  };
  const paint = () => {
    if (!current()) return;
    fill(command, el("h3", {}, "AgentNet command for your tools"),
      el("p", {class:"hint", style:"overflow-wrap:anywhere"}, "Location: " + status.cli_path),
      status.cli_state === "installed" ? el("p", {class:"hint"}, "Your tools can use this copy of AgentNet.") :
      status.cli_state === "custom" ? [el("p", {}, "This command is your own build. Replace it with the app’s copy only if you choose."),
        el("button", {type:"button", class:"btn", disabled:busy || !host.appReplaceCommand, onclick:() => dialog({
          title:"Replace your AgentNet command?", ok:"Replace command",
          body:[el("p", {}, "Your tools will use the app’s version at " + status.cli_path + ". Your existing custom build at this location is replaced.")],
          run:() => act(true)
        })}, "Replace command…")] : el("p", {class:"error", role:"alert"}, status.cli_problem || "The AgentNet command could not be installed."));
    fill(update, el("h3", {}, "Update this computer"),
      el("p", {}, "The app, its AgentNet command and connected tools update together. The app restarts when ready."),
      el("p", {class:"hint"}, "Installed app: " + status.version + "."),
      el("div", {class:"detail-actions"},
        host.appCheckUpdate && el("button", {type:"button", class:"btn", disabled:busy || checking, onclick:check}, checking ? "Checking…" : "Check for updates"),
        el("button", {type:"button", class:"btn", disabled:busy || checking || !status.app_update_supported || !host.appUpdate || (!!checked && checked.state !== "available"), onclick:() => act(false)}, busy ? "Updating…" : "Update AgentNet")),
      checked && el("p", {role:"status"}, checked.state === "available" ? `Version ${checked.latest} is available.` : checked.state === "current" ? `This app matches the latest stable release (${checked.latest}).` : `This app (${checked.version}) is ahead of the latest stable release (${checked.latest}).`),
      checkProblem && el("p", {class:"error", role:"alert"}, checkProblem),
      !status.app_update_supported && el("p", {class:"hint"}, status.problem || "This installation is updated by its package manager."),
      (message || status.update_result) && el("p", {role:"status"}, message || status.update_result),
      problem && el("p", {class:"error", role:"alert"}, problem));
  };
  paint();
}

function settingsTab(name) {
  appControlsRender++;
  if (name === "profile" || name === "device") renderAppControls();
  if (name === "notifications") ensureTyping().then(ui => { if (ui) ui.showSettings(); });
  if (name === "device") { renderResponder(); renderAssistantSetup(); }
  if (name === "storage") { renderStorage(); renderFileStorage(); }
  for (const key of ["profile", "appearance", "notifications", "device", "storage"]) $("settings-" + key).hidden = key !== name;
  for (const b of root.querySelectorAll("[data-settings]")) {
    const on = b.dataset.settings === name;
    b.setAttribute("aria-selected", String(on)); b.setAttribute("aria-pressed", String(on)); b.tabIndex = on ? 0 : -1;
    if (on && b.scrollIntoView) b.scrollIntoView({ block: "nearest", inline: "nearest" }); // the tab row scrolls on a phone: the chosen tab is always in view
  }
}
function showSettings() {
  toggleReview(false);
  if (state.overview) renderProfile(state.overview);
  settingsTab("profile");
  $("settings").showModal();
}
function setTheme(theme) {
  if (!["system", "light", "dark"].includes(theme)) theme = "system";
  root.dataset.theme = theme;
  for (const b of root.querySelectorAll("[data-theme]")) b.setAttribute("aria-pressed", String(b.dataset.theme === theme));
  try { localStorage.setItem("agentnet-theme", theme); } catch (e) { /* local only */ }
}

function renderInstalledInterfaces() {
  // Installed interfaces sit in the same Interface group as Classic, Comic
  // and Zoom: one choice, whatever package it comes from.
  for (const b of $("interfaces").querySelectorAll("[data-skin]")) b.remove();
  if (currentHost && currentHost.skins.length > 1) {
    const switchTo = (id) => {
      keepDraft();
      if (state.files.length || Object.values(state.drafts).some((d) => d.files && d.files.length)) { announce("Send or remove draft attachments before switching interface."); return; }
      if (!keepForReload()) { announce("Could not save your draft. Finish it before switching interface."); return; }
      const change = () => currentHost.selectSkin(id);
      if (Object.values(state.drafts).some((d) => d.text)) {
        dialog({ title: "Switch interface?", body: [el("p", {}, "Your unsent text will be kept for when you return to this interface. The other interface has its own drafts.")], ok: "Switch", run: async () => change() });
      } else change();
    };
    for (const s of currentHost.skins) {
      $("interfaces").append(el("button", { type: "button", "data-skin": s.id, "aria-pressed": String(ownSkin(s.id)), onclick: () => !ownSkin(s.id) && switchTo(s.id) }, s.name));
    }
  }
}

// ---- wiring ------------------------------------------------------------------------

// start wires the page; the relay's page loads this file after the device
// is ready, when the document has loaded already.
function start() {
  $("conversation-details").addEventListener("click", () => {
    const t = state.dmData || state.data;
    if (!t) return;
    const peer = state.dmData ? humanGroup(t) ? t.title : t.peer.label : state.data.peer;
    const members = humanGroup(t) ? el("section", {class: "conversation-members"}) : null;
    if (members) renderGroupMembers(t, members);
    const past = state.dmData ? t.agents.filter(a => ["dismissed", "declined"].includes(a.state)).map(a => agentCard(a, t)) : [];
    const routing = el("button", {type: "button", class: "btn", onclick: () => { $("dialog").close(); $("routing-settings").showModal(); }}, "Advanced CLI reply routing…");
    const actions = state.data ? el("div", { class: "detail-actions" },
      el("button", { type: "button", class: "btn", onclick: () => approvalDialog(state.data) }, "Question permissions"),
      state.data.task_grant && el("button", { type: "button", class: "btn", onclick: () => revokeDialog(state.data) }, "Task permissions")) : null;
    const guests = state.dmData ? (t.guests || []).map(g => guestCard(g, t)) : [];
    const remove = el("button", { type: "button", class: "btn", onclick: () => { $("dialog").close(); deleteConversationDialog(t); } }, state.dmData ? "Delete conversation…" : "Delete this thread…");
    dialog({ title: peer, body: [state.dmData ? deviceDisclosure(t.peer) : el("p", { class: "mono" }, state.data.key?.pinned || "Key not checked yet"), members, guests, past, actions, routing, remove], ok: "Close", run: async () => {} });
  });
  $("nav-chats").addEventListener("click", () => selectSection("chats"));
  $("nav-people").addEventListener("click", () => selectSection("people"));
  $("profile-btn").addEventListener("click", showSettings);
  $("settings-tabs").addEventListener("keydown", (e) => {
    const tabs = [...root.querySelectorAll("[data-settings]")];
    const i = tabs.indexOf((root.getRootNode().activeElement || null));
    if (i < 0 || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault(); e.stopPropagation();
    const next = e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : (i + (e.key === "ArrowLeft" ? -1 : 1) + tabs.length) % tabs.length;
    settingsTab(tabs[next].dataset.settings); tabs[next].focus();
  });
  $("settings-close").addEventListener("click", () => $("settings").close());
  $("settings").addEventListener("close", () => { if (!$("settings").open) appControlsRender++; });
  for (const b of root.querySelectorAll("[data-settings]")) b.addEventListener("click", () => settingsTab(b.dataset.settings));
  for (const b of root.querySelectorAll("[data-theme]")) b.addEventListener("click", () => setTheme(b.dataset.theme));
  $("settings-notifications").append($("notify-line"));
  renderInstalledInterfaces();
  if (currentHost.onSkinsChange) cleanups.push(currentHost.onSkinsChange(renderInstalledInterfaces));
  if (currentHost.manageLocalSkins) cleanups.push(currentHost.manageLocalSkins($("local-interfaces")) || (() => {}));
  let theme = "system";
  try { theme = localStorage.getItem("agentnet-theme") || theme; } catch (e) { /* default */ }
  setTheme(theme);
  $("composer").addEventListener("submit", send);
  $("body").addEventListener("input", grow);
  $("body").addEventListener("input", noteTyping);
  $("body").addEventListener("input", () => { trackMentions(); showMentions(); });
  $("body").addEventListener("keydown", mentionKey);
  $("mention-button").addEventListener("click", () => { showMentions(true); $("body").focus(); });
  $("routing-close").addEventListener("click", () => $("routing-settings").close());
  on(root, "pointerdown", e => { if (mentionView && !mentionBox().contains(e.target) && e.target !== mentionInput() && e.target !== $("mention-button")) closeMentions(); });
  $("body").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $("composer").requestSubmit(); }
  });
  $("kind").addEventListener("change", () => { kindHint(); renderTarget(); });
  kindHint();
  $("replying-cancel").addEventListener("click", () => { setAnswering(null); setDMReply(null); setDMAgent(null); });
  // Files: chosen, pasted (an image in the clipboard) or dropped on the composer.
  $("attach").addEventListener("click", () => $("file-input").click());
  $("file-input").addEventListener("change", () => { addFiles([...$("file-input").files]); $("file-input").value = ""; });
  $("body").addEventListener("paste", (e) => {
    if (!filesAllowed()) return;
    pastedFiles(e, files => addFiles(files, true));
  });
  $("composer").addEventListener("dragover", (e) => { if (filesAllowed() && e.dataTransfer && [...e.dataTransfer.types].includes("Files")) e.preventDefault(); });
  $("composer").addEventListener("drop", (e) => {
    if (!filesAllowed() || !e.dataTransfer || !e.dataTransfer.files.length) return;
    e.preventDefault();
    addFiles([...e.dataTransfer.files]);
  });
  $("review-btn").addEventListener("click", () => toggleReview());
  $("new-btn").addEventListener("click", () => newConversationDialog());
  $("search").addEventListener("input", () => { state.query = $("search").value; rerenderContacts(); });
  $("search").addEventListener("keydown", (e) => {
    if (e.key === "Escape" && $("search").value) { e.preventDefault(); clearSearch(); }
    if (e.key === "Enter") { const first = $("conv-list").querySelector(".result"); if (first) first.click(); }
  });
  on(root, "keydown", (e) => {
    if (!$("dialog").open && !$("settings").open && !root.querySelector(".file-viewer[open]") && $("review").hidden && Zoom.onKey(e)) { e.preventDefault(); return; }
    if (e.key === "Escape" && !$("review").hidden) { toggleReview(false); $("review-btn").focus(); return; }
    if (e.key === "/" && !$("settings").open && !$("dialog").open && !$("routing-settings").open && !e.target.closest("input, textarea, select")) {
      e.preventDefault();
      $("search").focus();
      return;
    }
  });
  $("back").addEventListener("click", backOneLevel);
  $("hub-back").addEventListener("click", () => state.hubUp && openHub(state.hubUp));
  $("dialog-form").addEventListener("submit", (e) => { if (e.submitter !== $("dialog-cancel")) e.preventDefault(); });
  for (const [id, what] of [["sim-arrival", "arrival"], ["sim-finish", "finish"]]) {
    $(id).addEventListener("click", async () => {
      try { await api("/api/simulate", { what }); } catch (e) { announce(e.message); }
    });
  }
  $("reconnect").addEventListener("click", async () => {
    if (!(await reconnect())) announce("Still not connected. If the daemon restarted, run agentnet ui for the new address.");
  });
  $("reload").addEventListener("click", reloadUpdated);
  $("dialog").addEventListener("close", () => {
    state.dialogRestore = null;
    if (!alive) return;
    if (state.dialogOpener?.isConnected) state.dialogOpener.focus({preventScroll: true});
    else focusZoomLayer($("zoom"));
    state.dialogOpener = null;
  });
  on(window, "focus", () => { // a missed change is caught when the person comes back
    if (!$("lost").hidden) return;
    refetch(false);
  });
  // What the person has in front of them, reported when it changes.
  $("timeline").addEventListener("scroll", reportSeen);
  on(document, "visibilitychange", reportSeen);
  on(window, "focus", reportSeen);
  // A desktop alert's click opens this page on its conversation (#conv=ID,
  // from the daemon), or changes only the fragment of a tab already open
  // on it: taken once and removed from the address, either way.
  on(window, 'beforeunload', (e) => {
    keepForReload();
    if (state.sending || state.files.length || Object.values(state.drafts).some(d => d.files?.length)) {
      e.preventDefault(); e.returnValue = '';
    }
  });
  loadOverview().then(async (o) => {
    if (!alive) return;
    showZoom();
    const clicked = state.clickedAtStart;
    state.clickedAtStart = null;
    if (clicked) { await openClicked(clicked); return; }
    const saved = wsAPI()?.state(wsNow())[skinKey];
    if (saved) {
      for (const k of viewKeys) if (saved[k] !== undefined) state[k] = saved[k];
      if (saved.thread) await openThread(saved.thread); else if (saved.dm) await openDM(saved.dm);
      if (saved.zoom) { Object.assign(Zoom, saved.zoom); Zoom.refresh(); }
      return;
    }
    if (await restoreAfterReload()) return; // back after an update, with what was unsent
    Zoom.refresh();
  }).catch(() => { if (alive) $("lost").hidden = false; });
  listen();
}
function revealZoomContent(content) {
  for (const animation of content.getAnimations()) animation.cancel();
  if (motion()) content.animate([{opacity: 0, transform: "scale(.98)"}, {opacity: 1, transform: "none"}], {duration: 180, easing: "ease-out"});
}
const motion = () => !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
const needsYou = (m) => !isReport(m) && (m.actions || []).some((a) => decisionActions.includes(a));
const working = (m) => (m.actions || []).includes("cancel");
// A DM message's author is the signed sender metadata; a device message's is {label, about}.
const authorName = (m) => (m.author && typeof m.author === "object" ? m.author.label : m.author) || (m.dir === "in" ? m.from : "You (this computer)");
const faceOf = (m) => m.face || (m.dir === "out" && state.overview ? state.overview.me.address : m.from);

// dialogFiles is a write dialog's files: the open conversation's draft
// files, the same the composer shows, added by choosing or by pasting into
// body.
function dialogFiles(body) {
  const list = el("ul", { class: "attach-list", "aria-label": "Files to send" });
  const input = el("input", { type: "file", id: "write-files", multiple: true, hidden: true });
  const note = el("p", { class: "error", role: "alert" });
  const show = () => { pendingChips(list, show); note.textContent = state.files.length ? overLimit(state.files) : ""; renderPending(); };
  const add = (files, pasted) => { pushFiles(files, pasted); show(); };
  input.addEventListener("change", () => { add([...input.files]); input.value = ""; });
  body.addEventListener("paste", (e) => pastedFiles(e, files => add(files, true)));
  show();
  return [el("button", { type: "button", class: "btn write-attach", onclick: () => input.click() }, "Add files or pictures…"), input, list, note];
}

// Writing without the composer (Zoom): an answer to m, or a new message in
// thread t linked to its latest message (with the draft's files).
function writeDialog(t, m) {
  const host = currentHost, gen = state.gen;
  const body = el("textarea", { id: "write-body", rows: "4" });
  body.value = $("body").value;
  body.addEventListener("input", () => { $("body").value = body.value; keepDraft(); });
  const canFiles = !m && !!fileLimits() && !t.key.pending;
  const kind = el("select", { id: "write-kind" }, ["message", "question", "task"].map((k) => el("option", { value: k }, k[0].toUpperCase() + k.slice(1))));
  const last = t.messages[t.messages.length - 1];
  dialog({
    title: m ? "Answer " + m.from : "Write to " + t.peer,
    body: [m && el("div", { class: "quote" }, m.body),
      el("label", { for: "write-body", class: "field-label" }, m ? "Your answer" : "Message"), body,
      !m && [el("label", { for: "write-kind", class: "field-label" }, "Send as"), kind],
      canFiles && dialogFiles(body),
      el("p", { class: "hint" }, m ? "Your answer takes this " + m.kind + " over from your responder." : "It joins this conversation.")],
    ok: "Send", focus: body,
    run: async () => {
      if (!alive || gen !== state.gen || host !== currentHost || state.thread !== t.id) throw Error("Conversation or workspace changed; reopen this draft there.");
      if (dmHumanGuest(state.dmData) && !guestAuthor()) throw Error("Sending is unavailable here. Your draft stays here.");
      if (!body.value.trim() && !(canFiles && state.files.length)) throw Error("Write a message or add a file first.");
      $("body").value = body.value; body.value = "";
      setKind(kind.value); setAnswering(m || null);
      void send(); // the dialog closes now; the chat owns the captured send and failure

    },
  });
  state.dialogRestore = { type: "write", msg: m ? m.id : null };
}

// dmWriteDialog writes a message in DM d without the composer (Zoom). Its
// files are this DM's draft files, the same the composer shows.
function dmWriteDialog(d) {
  if (dmHumanGuest(d) && !guestAuthor(d)) { announce("Join before sending. Your draft stays here."); return; }
  const host = currentHost, gen = state.gen;
  const body = el("textarea", { id: "write-body", rows: "4" });
  body.value = $("body").value;
  body.addEventListener("input", () => {
    $("body").value = body.value;trackMentions();keepDraft();renderAgentTarget();showMentions();
  });
  body.addEventListener("keydown", mentionKey);
  const mentions = el("div", {id:"write-mentions",hidden:true});
  const targets = el("div", {id:"write-agent-target"});
  const canFiles = filesAllowed();
  dialog({
    title: "Write to " + (humanGroup(d) ? d.title : d.peer.label),
    body: [el("label", { for: "write-body", class: "field-label" }, "Message"), body, targets, mentions,
      canFiles && dialogFiles(body),
      el("p", { class: "hint" }, humanGroup(d) ? "Messages go to current members and accepted guests. Each selected assistant receives its own request, subject to its owner’s permissions." : "Messages stay in this DM. Each selected assistant receives its own request, subject to its owner’s permissions.")],
    ok: "Send", focus: body,
    run: async () => {
      if (!alive || gen !== state.gen || host !== currentHost || state.dm !== d.id) throw Error("Conversation or workspace changed; reopen this draft there.");
      if (dmHumanGuest(state.dmData) && !guestAuthor()) throw Error("Sending is unavailable here. Your draft stays here.");
      if (!body.value.trim() && !(canFiles && state.files.length)) throw Error("Write a message or add a file first.");
      $("body").value = body.value;trackMentions();closeMentions();
      const pending = sendDM();body.value = "";void pending; // further writing stays available while this captured turn is stored

    },
  });
  state.dialogRestore = { type: "dmwrite" };
  renderAgentTarget();
}

// zoomReminders are the pending reminders on the everyone level.
function zoomReminders() {
  const items = remindersSection();
  return items.length ? el("section", { class: "zoom-reminders" }, el("ul", { class: "thread-list" }, items)) : null;
}

// agentNodes are a person's agents, each joined to them by a line: the
// device that runs it and the DMs it was invited into here (open picks one).
function agentNodes(p, open) {
  const agents = p.agents || [];
  if (!agents.length) return null;
  const dmTitle = (conv) => { const d = ((state.overview && state.overview.dms) || []).find((x) => x.id === conv); return d ? firstLine(d.title || "DM", 40) : "a DM"; };
  return el("ul", { class: "agent-nodes", "aria-label": (isMe(p) ? "Your" : p.label + "'s") + " agents" }, agents.map((a) =>
    el("li", { class: "agent-node" },
      el("span", { class: "agent-node-head" }, el("span", { class: "tag" }, "Agent"), agentLinkText(a, p)),
      el("span", { class: "agent-node-dms" }, a.dms.map((d) =>
        el("button", { type: "button", class: "text-btn", onclick: () => open(d.conv) }, dmTitle(d.conv) + " · " + d.state))))));
}

// zoomDirectory is the directory list the sidebar shows, below the people.
function zoomDirectory(threads) {
  const rows = directorySection(threads);
  return rows.length ? el("ul", { class: "zoom-dir" }, rows) : null;
}

// ---- Zoom: everyone → one person → one thread → one message ---------------------

// Every Zoom redraw keeps keyboard focus in its own surface. Never take
// focus from settings, the host strip, or anything outside this hierarchy.
function focusZoomLayer(layer) {
  const target = [...layer.querySelectorAll('.zoom-content button, .zoom-content [tabindex]:not([tabindex="-1"])')]
    .find(element => !element.disabled && element.getClientRects().length);
  target?.focus({preventScroll: true});
  const focused = layer.getRootNode().activeElement;
  if (!focused || !layer.contains(focused)) {
    const scene = layer.querySelector(".zoom-content");
    if (scene) { scene.tabIndex = -1; scene.focus({preventScroll: true}); }
  }
}

const Zoom = {
  team: null,
  generation: 0, navigating: null, dirty: false, finish: null, revealEnd: false, level: 0, peer: null, person: null, dm: null, msg: null, origins: [], query: "",

  // Two kinds of path: a person (by their record) and their DMs, or a
  // device contact (by address) and its conversations. Never both at once.
  personOf() { return ((state.overview && state.overview.people) || []).find((p) => personKey(p) === this.person); },

  names() {
    if(this.team && this.level===1)return ['Everyone',state.teams?.teams?.find(t=>t.id===this.team)?.name||'List','Conversation','Message'];
    if (this.level === 0) {
      return ["Everyone", state.overview && state.overview.persons ? "Person or contact" : "Contact", "Conversation", "Message"];
    }
    if (this.dm === state.dm && humanGroup()) return ["Everyone", "Group", state.dmData.title, "Message"];
    if (this.person) {
      const p = this.personOf(), d = state.dmData;
      const scope=flatTopicScope(d),selected=scope&&personTopicEntries(scope.roots,{[d.id]:d},scope.main).find(t=>t.conv===d.id&&t.topic===(topicSelections[d.id]||''));
      return ["Everyone", p ? p.label : "Person", d && this.level >= 2 ? selected?.title || "Main" : "Chat", "Message"];
    }
    const t = state.data;
    return ["Everyone", this.level >= 1 ? this.peer : "Contact", t && this.level >= 2 ? firstLine(t.messages[0].body, 40) : "Conversation", "Message"];
  },

  // refresh redraws the current level after new data, without motion.
  refresh() {
    if (!alive || !state.overview) return;
    if (this.navigating !== null) { this.dirty = true; return; }
    const localMotion = $("zoom").getAnimations({subtree: true}).filter(a => a.playState === "running" && !(a instanceof CSSAnimation));
    if (localMotion.length) {
      if (!this.redrawAfterAnimation) {
        this.redrawAfterAnimation = true;
        Promise.allSettled(localMotion.map(a => a.finished)).then(() => { this.redrawAfterAnimation = false; this.refresh(); });
      }
      return;
    }
    if (this.person) {
      if (this.level >= 2 && (!state.dmData || state.dm !== this.dm)) this.level = 1;
      if (this.level === 3 && !state.dmData.messages.some((m) => m.id === this.msg)) this.level = 2;
    } else {
      if (this.level >= 2 && (!state.data || state.data.peer !== this.peer)) this.level = this.peer ? 1 : 0;
      if (this.level === 3 && !state.data.messages.some((m) => m.id === this.msg)) this.level = 2;
    }
    const surface = $("zoom"), focused = root.getRootNode().activeElement;
    const ownedFocus = !!focused && surface.contains(focused);
    const oldContent = surface.querySelector(".zoom-layer:last-child .zoom-content");
    const scroll = oldContent?.scrollTop || 0;
    const atEnd = oldContent && oldContent.scrollHeight - scroll - oldContent.clientHeight < 60;
    const expanded = [...surface.querySelectorAll(".zoom-layer:last-child details[open]")].map(d => d.dataset.person).filter(Boolean);
    const focusText = ownedFocus && focused.tagName === "BUTTON" ? focused.textContent : null;
    const typing = focused?.classList.contains("zoom-search");
    const selection = typing ? [focused.selectionStart, focused.selectionEnd] : null;
    fill(surface, this.layer());
    const content = surface.querySelector(".zoom-content");
    for (const d of surface.querySelectorAll("details[data-person]")) d.open = expanded.includes(d.dataset.person);
    content.scrollTop = this.level === 2 && (atEnd || this.revealEnd) ? content.scrollHeight : scroll;
    this.revealEnd = false;
    if (typing) {
      const search = surface.querySelector(".zoom-search");
      search.focus({preventScroll: true});
      search.setSelectionRange(...selection);
    } else if (ownedFocus) {
      const match = focusText && [...surface.querySelectorAll("button")].find(b => !b.disabled && b.textContent === focusText);
      if (match) match.focus({preventScroll: true}); else focusZoomLayer(surface);
    }
  },

  // results is what the Zoom search finds; choosing one zooms to it.
  results() {
    const pick = (level, patch, from) => { this.query = ""; this.go(level, patch, from); };
    return el("div", { class: "zoom-results" }, el("ul", { class: "conv-list" }, searchItems(this.query, state.overview.threads, {
      person: (p, from) => pick(1, { person: personKey(p), peer: null }, from),
      dm: (d, from) => pick(2, { dm: d.id }, from),
      contact: (c, from) => pick(1, { peer: c.peer, person: null }, from),
      conversation: (t, from) => pick(2, { thread: t.id, peer: t.peer, person: null }, from),
    })));
  },

  content(views) {
    if(this.team && this.level===1){const t=state.teams?.teams?.find(t=>t.id===this.team);return el('section',{class:'zoom-person'},el('h2',{},t?.name||'List'),...(t?teamHub(t):[el('p',{class:'hint'},'This list is no longer available.')]));}
    return this.query.trim() ? this.results() : views[this.level]();
  },

  layer() {
    const views = this.person ? [() => this.everyone(), () => this.personLevel(), () => this.dmLevel(), () => this.dmMessage()]
      : [() => this.everyone(), () => this.person_(), () => this.thread(), () => this.message()];
    topicUI.show(this.level>=2);
    const content = el("div", { class: "zoom-content" }, this.content(views));
    const search = el("input", { type: "search", class: "zoom-search", placeholder: "Search…",
      "aria-label": "Search people, agents and conversations", autocomplete: "off", spellcheck: "false", value: this.query,
      oninput: (e) => { this.query = e.target.value; fill(content, this.content(views)); revealZoomContent(content); },
      onkeydown: (e) => { if (e.key === "Escape" && this.query) { e.stopPropagation(); e.target.value = ""; this.query = ""; fill(content, this.content(views)); } } });
    return el("div", { class: "zoom-layer" },
      el("div", { class: "zoom-side" }, search, el("nav", { class: "ladder", "aria-label": "Zoom level" },
        this.names().map((n, i) => i===1&&this.person&&flatTopicScope(state.dmData)?null:el("button", {
          type: "button", class: "rung" + (i === this.level ? " here" : ""), disabled: i > this.level,
          "aria-current": i === this.level ? "step" : "false", onclick: () => i < this.level && this.go(i, {}),
        }, el("span", { class: "rung-dot" }), el("span", { class: "rung-name" }, n))),
        el("p", { class: "hint ladder-hint" }, "Esc zooms out"))),
      content);
  },

  async go(level, patch, from) {
    if (level < 2) state.messageOpening = null;
    if(level===1&&!patch.dm&&!patch.thread&&!patch.peer&&(patch.person||this.person)&&!(humanGroup()&&!patch.person)) {
     const key=patch.person||this.person,p=(state.overview?.people||[]).find(p=>personKey(p)===key);
     const roots=(state.overview?.dms||[]).filter(d=>d.peer?.person===p?.person);
     const main=p&&choosePersonMain(roots,p.person);
     if(main){if(patch.person){topicSelections[main]='';topicFresh[main]=false;patch={...patch,dm:main};level=2;}else level=0;}
    }
    this.finish?.();
    const generation = ++this.generation, host = currentHost;
    this.navigating = generation;
    toggleReview(false);
    const inward = level > this.level || (level === this.level && !!from);
    if (inward && from) this.origins[this.level] = from.getBoundingClientRect();
    const origin = from ? from.getBoundingClientRect() : inward ? null : this.origins[level];
    const { thread, dm, ...rest } = patch;
    Object.assign(this, {team:null}, rest, { level });
    if (thread) {
      const changed = beginThread(thread); // the same switch as the other views: drafts stay put
      this.person = null;
      await loadThread(true);
      this.peer = state.data?.peer || this.peer;
      if (changed) refreshThread();
    }
    if (dm) {
      const changed = beginDM(dm);
      this.dm = dm;
      this.peer = null;
      await loadDM(true);
      if (state.dmData) this.person = personKey(state.dmData.peer); // a listed person is known by its id once checked
      if (changed) api("/api/refresh", { id: dm }).catch(() => {});
    }
    if (!alive || generation !== this.generation || host !== currentHost) return;
    const root = $("zoom");
    for (const c of [...root.children].slice(0, -1)) c.remove(); // still leaving from a fast earlier zoom
    const old = root.lastElementChild, fresh = this.layer();
    const rr = root.getBoundingClientRect();
    const ox = origin ? origin.left + origin.width / 2 - rr.left : rr.width / 2;
    const oy = origin ? origin.top + origin.height / 2 - rr.top : rr.height / 2;
    fresh.style.transformOrigin = ox + "px " + oy + "px";
    root.append(fresh);
    if (old && motion()) {
      old.style.transformOrigin = fresh.style.transformOrigin;
      old.classList.add(inward ? "zoom-away-in" : "zoom-away-out");
      fresh.classList.add(inward ? "zoom-arrive-in" : "zoom-arrive-out");
      const finish = () => {
        old.remove(); fresh.classList.remove("zoom-arrive-in", "zoom-arrive-out");
        if (generation !== this.generation) return;
        this.finish = null; this.navigating = null;
        if (this.dirty) { this.dirty = false; this.refresh(); }
      };
      this.finish = finish;
      const arrivals = fresh.getAnimations();
      Promise.allSettled(arrivals.map(a => a.finished)).then(() => { if (this.finish === finish) finish(); });
    } else {
      old?.remove(); this.navigating = null; this.dirty = false;
    }
    focusZoomLayer(fresh);
  },

  out() { if (this.level > 0) this.go(this.level - 1, {}); },

  onKey(e) {
    if (e.key === "Escape" && this.level > 0) { this.out(); return true; }
    return false;
  },

  // people is the group of persons at the top of Level 0: each by the name
  // they give, with their device and DMs, and a line to each agent their
  // device runs in DMs here (the invitation's host, never a name).
  people(entries) {
    const o = state.overview;
    if (!o.persons) return null;
    const head = el("h3", { class: "zoom-group" }, "People");
    if (!o.person) return el("section", { class: "zoom-people-set" }, head, linkNotices(), el("div", { class: "person-setup" }, setupChoice()));
    const dms = o.dms || [], people = entries ? entries.filter((e) => e.person).map((e) => e.person) : o.people || [];
    // Each person is one node, you included (once); their devices open
    // under it on a click, from their checked record.
    const devices = (p) => (p.state === "pinned" || p.state === "self") && deviceDisclosure(p, (addr, from) => this.go(1, { peer: addr, person: null }, from));
    const me = o.person;
    const meFace = [avatar(me.label, "node-face"), el("span", { class: "node-name" }, me.label + " (you)"),
      el("span", { class: "node-status" }, me.published ? "others can start a DM with you" : "not on your server yet")];
    const self = el("li", { class: "person-cluster self" },
      devicesHere() ? el("button", { type: "button", class: "person-card self", "aria-label": "You, " + me.label + ": your devices", onclick: () => devicesDialog() }, meFace)
        : el("div", { class: "person-card self" }, meFace),
      devices(me), agentNodes(me, (conv) => { this.person = null; openDM(conv); }));
    return el("section", { class: "zoom-people-set" }, head, linkNotices(),
      el("p", { class: "hint" }, "Each name is what that person calls themself."),
      el("ul", { class: "person-cards" }, self, people.map((p) => {
        const theirs = dms.filter((d) => d.peer.person && d.peer.person === p.person);
        const held = theirs.reduce((n, d) => n + d.held, 0), unread = theirs.reduce((n, d) => n + d.unread, 0);
        const status = [devicesText(p), personStateText[p.state] || p.state, theirs.length && plural(theirs.length, "DM", "DMs"),
          held && held + " held", unread && unread + " new"].filter(Boolean).join(" · ");
        const b = el("button", { type: "button", class: "person-card" + (p.state === "conflict" ? " danger" : ""), "aria-label": p.label + ", " + status },
          avatar(p.label || p.address, "node-face"), el("span", { class: "node-name" }, p.label), el("span", { class: "node-status" }, status));
        b.addEventListener("click", () => theirs[0]?.last_id ? void openMessage({id:theirs[0].last_id,conv:theirs[0].id}) : this.go(1, { person: personKey(p), peer: null }, b));
        return el("li", { class: "person-cluster" }, b, devices(p),
          agentNodes(p, (conv) => this.go(2, { person: personKey(p), dm: conv, peer: null })));
      })),
      !people.length && el("p", { class: "hint" }, "No one else on your server has set up a person yet."));
  },

  // Level 0 uses this package's bounded people/service list.
  // Devices stay under their person; services do not need a crowded orbit.
  everyone() {
    const o = state.overview, entries = sidebarEntries(o.threads), shown = entries.slice(0, state.contactLimit);
    const list = shown.filter((e) => e.contact).map((e) => e.contact);
    return el("div", { class: "zoom-people" }, contactControls(), zoomReminders(), this.people(shown),
      list.length > 0 && el("h3", { class: "zoom-group" }, "Devices and services"),
      el("ul", { class: "thread-list" }, list.map((c) => el("li", {},
        el("button", { type: "button", class: "thread-row", onclick: () => this.go(1, { peer: c.peer, person: null }) },
          avatar(c.peer, "sm"), el("span", { class: "thread-title" }, who(c.peer)), counts(c))))),
      !entries.length && el("p", { class: "hint" }, state.contactView === "unread" ? "No unread conversations." : "No activity yet. People lists everyone."),
      moreContacts(entries.length), state.contactView === "people" && zoomDirectory(o.threads),
      el("ul", {class:"thread-list"}, groupsSection((id,from)=>this.go(2,{dm:id},from))),
      state.contactView==='people' && el('ul',{class:'thread-list'},teamsSection()));
  },

  // Level 1 for a person: their separate DMs (the same rows the sidebar
  // shows) and a new one.
  personLevel() {
    if (this.dm === state.dm && humanGroup()) {
      const d=state.dmData;
      return el("section", {class:"zoom-person"}, el("h2",{},d.title),
        el("p",{class:"hint"},groupMemberCount(d) + ". Ordinary messages do not run agents."),
        el("ul",{class:"thread-list"},dmRow((state.overview.dms || []).find(x => x.id === d.id) || {...d, last_at: d.messages.at(-1)?.at || d.created},(id,from)=>this.go(2,{dm:id},from))));
    }
    const p = this.personOf();
    if (!p) return el("p", { class: "hint" }, "That person is not listed here any more.");
    const dms = ((state.overview && state.overview.dms) || []).filter((d) => d.peer.person && d.peer.person === p.person);
    const online = presenceOf(p.address);
    return el("div", { class: "zoom-person" },
      el("header", { class: "zoom-head" }, avatar(p.label || p.address), el("div", {}, el("h2", {}, p.label),
        el("p", { class: "hint" }, "The name they give · " + devicesText(p) + " · " + (personStateText[p.state] || p.state) +
          (online ? " · their computer is " + online : "")))),
      deviceDisclosure(p, (addr, from) => this.go(1, { peer: addr, person: null }, from)),
      el("div", { class: "zoom-contact" },
        dms.length ? el("ul", { class: "thread-list", "aria-label": "DMs with " + p.label }, dms.map((d) => dmRow(d, (id, from) => this.go(2, { dm: id }, from))))
          : el("p", { class: "hint" }, "No DM with " + p.label + " yet."),
        p.state === "conflict" ? el("p", { class: "hint" }, "Frozen: no new DM can start with this record.")
          : el("button", { type: "button", class: "text-btn new-conv", onclick: () => newDMDialog(p) }, "New DM with " + p.label)),
      (p.agents || []).length > 0 && el("section", { class: "zoom-agents" }, el("h3", { class: "zoom-group" }, "Their agent"),
        agentNodes(p, (conv) => this.go(2, { dm: conv }))));
  },

  // Level 2 for a person: one DM as a short chat.
  dmLevel() {
    const base = state.dmData;
    const d = base && {...base, messages:sends.merge("dm:"+base.id, base.messages)};
    if (!d) return el("p", { class: "hint" }, "This DM could not be loaded.");
    const messages=d.messages.filter(m=>(m.topic||'')===(topicSelections[d.id]||''));
    const title=d.topics?.find(t=>t.id===topicSelections[d.id])?.title || (messages[0] ? firstLine(messages[0].body,80) : "No messages yet");
    const me = state.overview && state.overview.person ? state.overview.person.label : "You";
    return el("div", { class: "zoom-scene" },
      el("header", { class: "zoom-head" }, el("div", {},
        el("p", { class: "hint" }, humanGroup(d) ? (dmHumanGuest(d) ? "Guest in group: " : "Group: ") + d.title + " · " + groupMemberCount(d) : "DM with " + d.peer.label + " (the name they give) · " + devicesText(d.peer)),
        el("h2", {}, title))),
      d.frozen && el("p", { class: "notice" }, d.frozen),
      humanGroup(d) && pendingGroupPeople(d).length > 0 && el("section", {"aria-label":"Invited people", class:"participant-list"},
        pendingGroupPeople(d).map(i => el("span", {class:"participant invited-person"},
          avatar(personLabelOf(i.target), "sm"), personLabelOf(i.target),
          el("span", {class:"tag"}, i.status === "accepted" ? "Accepted · waiting to join" : "Invited · waiting for them to accept"), groupInvitationActions(i)))),
      humanGroup(d) && el("details", {class:"zoom-agents", "aria-label":"People in this group"},
        el("summary", {}, "Current members and human guests"),
        el("ul", {}, (d.members || []).map(p=>el("li", {}, p.label + (p.admin ? " · Administrator" : " · Member")))),
        dmHumanGuest(d) && el("p", {class:"hint"}, "You are a guest, not a group member. Guests cannot invite or manage members."),
        (d.guests || []).map(g=>guestCard(g,d)),
        inviteRights(d).people && el("button", {type:"button",class:"text-btn",onclick:()=>participantsDialog(d)}, "+ Add participants")),
      humanGroup(d) && el("details", {class:"zoom-agents", "aria-label":"Agents in this group"},
        el("summary", {}, d.agents.filter(a=>!["dismissed","declined"].includes(a.state)).length + " agents in this group"),
        d.agents.filter(a=>!["dismissed","declined"].includes(a.state)).map(a=>agentCard(a,d)),
        inviteRights(d).assistants && el("button",{type:"button",class:"text-btn",onclick:()=>inviteDialog(d)},"Add agent or share more…")),
      $("compose-error").textContent && el("p",{class:"error",role:"alert"},$("compose-error").textContent),
      el("ol", { class: "mini-chat" }, groupedSends(messages).map(({m, compact: groupPart}) => {
        if (m._local) return el("li", {class:"mc mine",id:"m-"+m.id}, el("div", {class:"mc-stack"}, el("div",{class:"mc-bubble"},m.pid && el("span",{class:"mc-who","data-agent-recipient":"",title:m.pid},"To @"+(agentOf(m.pid)?agentName(agentOf(m.pid)):"agent")),!groupPart && el("span",{class:"mc-text"},m.body), el("span",{class:"mc-files"},(m.attachments || m.files || []).map(f=>f.name).join(", "))),el("p",{class:"narr",role:"status"},m.state_text), m._failed && el("button",{type:"button",onclick:m._retry},"Retry")));
        if (m.event) return el("li", { class: "event-line" }, el("span", {}, m.event), el("time", { datetime: m.sent_at || m.at }, sentWhen(m)));
        const mine = m.dir === "out";
        const to = m.target && m.target.agent_id ? namedAgentLabel(m.target.agent_id, m.target.address, undefined, whoseAgent(m.pid && agentOf(m.pid))) : m.to && agentOf(m.pid) ? agentName(agentOf(m.pid)) : "";
        const execution = execLine(m, d);
        const compact = !m.deleted && confirmedTask(m);
        const bubble = el(compact ? "div" : "button", { ...(compact ? {} : {type:"button"}), class: compact ? "mc-bubble compact-approval-bubble" : "mc-bubble" },
          el("span", { class: "mc-who" }, dmAuthor(m, d) + (kindTag[m.kind] ? " · " + kindTag[m.kind] : "") + (to ? " · to " + to : "") + " · " + sentWhen(m)),
          !m.deleted && (m.kind === "question" || m.kind === "task") && m.pid && el("span", {class:"mc-who", "data-agent-recipient":"", title:m.pid}, "To @" + (agentOf(m.pid) ? agentName(agentOf(m.pid)) : "agent")),
          compact ? approvalBody(m) : !groupPart && el("span", { class: "mc-text" + (m.deleted ? " tombstone" : "") }, m.deleted ? "Message deleted" : messagePreview(shownText(m)), !m.deleted && m.edited ? " · edited" : ""),
          !m.deleted && (m.attachments || []).length > 0 && el("span", { class: "mc-files" }, "📎 " + m.attachments.map((f) => f.name).join(", ")),
          (m.reactions || []).length > 0 && el("span", { class: "mc-files" }, m.reactions.map((r) => r.emoji + " " + (r.by || []).length).join("  ")));
        if (compact) bubble.append(el("button", {type:"button",class:"text-btn",onclick:()=>this.go(3,{msg:m.id},bubble)}, "Message details"));
        else bubble.addEventListener("click", () => this.go(3, { msg: m.id }, bubble));
        return el("li", { class: "mc " + (mine ? "mine" : "theirs"), id:"m-"+m.id }, m.verified_agent ? el("span", {class:"avatar sm", "aria-hidden":"true"}, "🤖") : avatar(mine ? me : humanGroup(d) ? dmAuthor(m,d) : d.peer.label || m.from, "sm"), el("div",{class:"mc-stack"},!groupPart && messageReference(m,d),bubble),
          agentDetailFor(m, d) && ((m.actions || []).includes("resolve") || m.exec?.state === "needs_human") && agentNeedsYouTurn(m, d),
          proposalCard(m.proposal),
          (m.actions || []).includes("do_it") && el("div", {class:"acts"}, actionButton("do_it",m,d,true)),
          execution && el("p", {class:"narr", "data-agent-execution":""}, execution),
          m.exec?.state === "not_run" && m.job_detail && el("p", {class:"hint"}, m.job_detail),
          m.exec?.state !== "not_run" && (m.delivery || m.state_text) && el("p", { class: m.exec?.state ? "hint" : "narr" }, m.dir === "out" && m.delivery ? deliveryText(m) : m.state_text));
      })),
      el("div", { class: "zoom-write" }, el("button", { type: "button", class: "btn", disabled: !!d.frozen || state.sending || dmHumanGuest(d) && !guestAuthor(d), onclick: () => dmWriteDialog(d) },
        d.frozen ? "Nothing more can be sent in this conversation" : dmHumanGuest(d) && !guestAuthor(d) ? "Join before writing in this group" : humanGroup(d) ? "Write in this group…" : "Write in this DM…")));
  },

  // Level 3 for a person: one DM message with everything known about it.
  dmMessage() {
    const d = state.dmData, m = d.messages.find((x) => x.id === this.msg);
    const det = dmDetails(m);
    det.open = true;
    const me = state.overview && state.overview.person ? state.overview.person.label : "You";
    return el("article", { class: "zoom-message", tabindex: "-1" },
      el("div", { class: "meta" }, avatar(m.dir === "out" ? me : humanGroup(d) ? dmAuthor(m,d) : d.peer.label || m.from, "sm"), el("span", { class: "who" }, dmAuthor(m, d)),
        kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]), el("time", { datetime: m.sent_at || m.at }, sentWhen(m))),
      messageReference(m,state.dmData?.messages.includes(m)?state.dmData:state.data),
      agentDetailFor(m, d) && ((m.actions || []).includes("resolve") || m.exec?.state === "needs_human") && agentNeedsYouTurn(m, d),
      bodyOf(m),
      !m.deleted && reminderLine(m),
      !m.deleted && fileChips(m, m.attachments),
      reactionsRow(m, d.id), messageMenu(m, d.id, { querySelector: () => null }),
      m.state_text && el("p", { class: "hint" }, m.state_text),
      ["question", "task"].includes(m.kind) && (m.actions || []).some(a => a !== "do_it") &&
        !(agentDetailFor(m, d) && ((m.actions || []).includes("resolve") || m.exec?.state === "needs_human")) &&
        el("div", {class:"acts"}, m.actions.filter(a => a !== "do_it").map((a,i) => actionButton(a,m,d,i===0))),
      proposalCard(m.proposal),
      (m.actions || []).includes("do_it") && el("div", {class:"acts"}, actionButton("do_it",m,d,true)),
      det);
  },

  // Level 1 for a device contact: its reports and its separate
  // conversations, as a compact list (the same one the sidebar shows).
  person_() {
    const c = contactsOf(state.overview.threads).find((x) => x.peer === this.peer);
    if (!c) return el("p", { class: "hint" }, "No conversations with " + this.peer + ".");
    return el("div", { class: "zoom-person" },
      el("header", { class: "zoom-head" }, avatar(this.peer), el("div", {}, el("h2", {}, who(this.peer)),
        el("p", { class: "hint" }, peerPresence(this.peer) || plural(c.conversations.length, "conversation", "conversations")),
        el("div", { class: "zoom-counts" }, counts(c)))),
      el("div", { class: "zoom-contact" }, contactBody(c, (id, from) => this.go(2, { thread: id }, from))));
  },

  // Level 2: one conversation as a short chat.
  thread() {
    const base = state.data;
    const t = base && {...base,messages:sends.merge(state.draftKey || state.thread,base.messages)};
    return el("div", { class: "zoom-scene" },
      el("header", { class: "zoom-head" }, el("div", {}, el("p", { class: "hint" }, who(t.peer)), el("h2", {}, firstLine(t.messages[0].body, 80)))),
      $("compose-error").textContent && el("p",{class:"error",role:"alert"},$("compose-error").textContent),
      el("ol", { class: "mini-chat" }, t.messages.map((m) => {
        if (m._local) return el("li", {class:"mc mine",id:"m-"+m.id}, el("div", {class:"mc-stack"}, el("div",{class:"mc-bubble"},el("span",{class:"mc-text"},[m.body,...(m.attachments || m.files || []).map(f=>f.name)].filter(Boolean).join("\n"))),el("p",{class:"narr",role:"status"},m.state_text), m._failed && el("button",{type:"button",onclick:m._retry},"Retry")));
        const mine = m.dir === "out";
        const compact = !m.deleted && confirmedTask(m);
        const bubble = el(compact ? "div" : "button", { ...(compact ? {} : {type:"button"}), class: compact ? "mc-bubble compact-approval-bubble" : "mc-bubble" },
          el("span", { class: "mc-who" }, authorName(m) + (kindTag[m.kind] ? " · " + kindTag[m.kind] : "") + " · " + sentWhen(m)),
          compact ? approvalBody(m) : el("span", { class: "mc-text" + (m.deleted ? " tombstone" : "") }, m.deleted ? "Message deleted" : messagePreview(shownText(m)), !m.deleted && m.edited ? " · edited" : ""),
          (m.reactions || []).length > 0 && el("span", { class: "mc-files" }, m.reactions.map((r) => r.emoji + " " + (r.by || []).length).join("  ")));
        if (compact) bubble.append(el("button", {type:"button",class:"text-btn",onclick:()=>this.go(3,{msg:m.id},bubble)}, "Message details"));
        else bubble.addEventListener("click", () => this.go(3, { msg: m.id }, bubble));
        return el("li", { class: "mc " + (mine ? "mine" : "theirs") },
          avatar(mine ? state.overview.me.address : m.from, "sm"), el("div",{class:"mc-stack"},messageReference(m,t),bubble),
          m.summary && el("p", { class: "narr" }, "Your responder's summary: " + m.summary),
          execLine(m, t),
          m.state_text && !needsYou(m) && el("p", { class: "narr" + (working(m) ? " running" : "") }, m.state_text),
          proposalCard(m.proposal),
          needsYou(m) && el("div", { class: "decide" }, el("p", { class: "decide-why" }, (m.state_text || "").replace(/^Needs you: /, "Needs you · ")),
            el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, t, i === 0)))),
          working(m) && el("div", { class: "acts" }, actionButton("cancel", m, t, false)),
          isReport(m) && (m.actions || []).length > 0 && el("div", { class: "acts" }, m.actions.map((a) => actionButton(a, m, t, false))));
      })),
      el("div", { class: "zoom-write" }, el("button", { type: "button", class: "btn", disabled: !!t.key.pending || state.sending,
        onclick: () => writeDialog(t, null) }, t.key.pending ? "Sending is blocked until you trust the new key" : "Write in this conversation…")));
  },

  // Level 3: one message with everything known about it.
  message() {
    const t = state.data, m = t.messages.find((x) => x.id === this.msg);
    const d = details(m);
    d.open = true;
    return el("article", { class: "zoom-message", tabindex: "-1" },
      el("div", { class: "meta" }, avatar(m.dir === "out" ? state.overview.me.address : m.from, "sm"), el("span", { class: "who" }, authorName(m)),
        kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]), el("time", { datetime: m.sent_at || m.at }, sentWhen(m))),
      messageReference(m,state.dmData?.messages.includes(m)?state.dmData:state.data),
      bodyOf(m),
      !m.deleted && reminderLine(m),
      !m.deleted && m.files && m.files.length && el("p", { class: "hint" }, "Files: " + m.files.map((f) => f.name + " (" + size(f.size) + ")").join(", ")),
      reactionsRow(m, ""), messageMenu(m, "", { querySelector: () => null }),
      m.summary && el("div", { class: "note" }, el("p", { class: "note-label" }, "Summary written on this computer by your responder"), el("p", { class: "body" }, m.summary)),
      execLine(m, t),
      m.state_text && el("p", { class: "hint" }, m.state_text),
      (m.actions || []).length > 0 && el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, t, i === 0))),
      d);
  },
};

start();
return () => {
  keepDraft();
  if (wsAPI()?.state) workspaceCapture(wsNow(), wsAPI().state(wsNow()));
  alive = false; state.gen++; sends.dispose();
  if (pending.changed === pendingChanged) pending.changed = null;
  if (pending.accepted === pendingAccepted) pending.accepted = null;
  stopListen?.(); typingUI?.destroy(); abort.abort();
  for (const dispose of cleanups) dispose();
  for (const timer of timers) globalThis.clearTimeout(timer);
  releaseOpened(); closeMentions();
  for (const draft of [state, ...Object.values(state.drafts)]) for (const f of draft.files || []) if (f.url) URL.revokeObjectURL(f.url);
  root.replaceChildren();
};

}
