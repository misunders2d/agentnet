// Notebook: an example skin, a standalone package on AgentNet's public skin
// contract (docs/UI_SKINS.md, Host API v1). It uses nothing of Comic and no
// page globals: only the host it is given, the root it owns and the shared
// typing presenter the host documents (/assets/typing.mjs).
//
// A notebook: a table of contents (people, then devices and services), one
// conversation page at a time, a composer with a To line and a Send button
// named after its target, files as clippings, and explicit decision
// buttons. What it does not do is said on its page: the host's bar above
// it switches to Comic for those.
//
// mounted keeps, per root, what unmount stops: the host stream this page
// listens to. The host mounts the notebook again over another workspace's
// host when the person switches; the old root goes out with its stream.
// Drafts (text, files, send kind) and the open conversation live in the
// host's per-workspace view state, so switching A/B/A restores them.
import { mountTyping } from "/assets/typing.mjs";

const mounted = new WeakMap();
let updateLeaveWarning = () => {};

export async function unmount(root) {
  const m = mounted.get(root);
  if (m) { mounted.delete(root); m.stop(); }
}

export async function mount(root, host) {
  const state = host.workspaces ? host.workspaces.state(host.workspace.id) : {};
  const view = state.notebook || (state.notebook = { drafts: new Map(), selected: null, sending: false });
  let live = true, warning = false;
  const dirty = () => {
    const views = host.workspaces ? host.workspaces.list().map((w) => host.workspaces.state(w.id).notebook).filter(Boolean) : [view];
    return views.some((v) => v.sending || [...v.drafts.values()].some((d) => d.text.length || d.files.length));
  };
  const beforeLeave = (event) => { if (dirty()) { event.preventDefault(); event.returnValue = true; } };
  const updateWarning = () => {
    const needed = live && dirty();
    if (needed && !warning) window.addEventListener("beforeunload", beforeLeave);
    if (!needed && warning) window.removeEventListener("beforeunload", beforeLeave);
    warning = needed;
  };
  updateLeaveWarning = updateWarning;
  const el = (tag, text, cls) => { const n = document.createElement(tag); if (text) n.textContent = text; if (cls) n.className = cls; return n; };
  const button = (text, fn, cls) => { const b = el("button", text, cls); b.type = "button"; if (fn) b.onclick = fn; return b; };
  const size = (n) => (n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(1) + " MB");
  const sniffImage = (b) => (b.length > 8 && b[0] === 0x89 && b[1] === 0x50 ? "image/png" : b.length > 3 && b[0] === 0xff && b[1] === 0xd8 ? "image/jpeg"
    : b.length > 6 && b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46 ? "image/gif" : b.length > 12 && b[8] === 0x57 && b[9] === 0x45 && b[10] === 0x42 && b[11] === 0x50 ? "image/webp" : "");
  const when = (iso) => { const d = iso ? new Date(iso) : null; return d && !isNaN(d) ? d.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : ""; };

  // ---- the frame: contents on the left, the open page on the right
  const contents = el("aside", "", "contents"), page = el("main", "", "page");
  const masthead = el("header", "", "masthead"), me = el("p", "", "me");
  masthead.append(el("h1", "Notebook"), el("p", "Your conversations, one page at a time.", "tagline"), me);
  const toc = el("nav"); toc.setAttribute("aria-label", "Conversations");
  const limits = el("p", "This notebook reads, replies, decides on what is held here, sends and opens files. Not in it: finding new people, adding devices, other settings and notifications. The bar above switches to Comic for those.", "limits");
  const typingSettings = el("div", "", "typing-settings");
  const typingToggle = button("Typing preferences", () => { typingSettings.hidden = !typingSettings.hidden; if (!typingSettings.hidden) typing.showSettings(); }, "quiet");
  typingSettings.hidden = true;
  contents.append(masthead, toc, limits, typingToggle, typingSettings);

  const status = el("p", "", "status"); status.setAttribute("role", "status");
  // say: one line of news. A loading error ("load") clears once loading works again.
  const say = (text, tone = "error", from = "") => { status.textContent = text; status.dataset.tone = tone; status.dataset.from = from; };
  const back = button("← Contents", () => choose(null), "back");
  const sheet = el("article", "", "sheet");
  const form = el("form", "", "composer"); form.setAttribute("aria-label", "Write on this page");
  const body = el("textarea"); body.setAttribute("aria-label", "Message"); body.placeholder = "Write here…"; body.rows = 3;
  const typingLine = el("p", "", "typing");
  const typing = mountTyping({ api: (path, value) => host.api(path, value), input: body, line: typingLine, settings: typingSettings });
  const to = el("p", "", "to"); to.setAttribute("aria-live", "polite");
  const send = el("button", "Send", "send"); send.type = "submit";
  const kind = el("select"); kind.setAttribute("aria-label", "Send as");
  for (const value of ["message", "question", "task"]) { const o = el("option", value.charAt(0).toUpperCase() + value.slice(1)); o.value = value; kind.append(o); }
  const attach = el("input"); attach.type = "file"; attach.multiple = true; attach.setAttribute("aria-label", "Files to send"); attach.hidden = true;
  const attachButton = button("Add a file…", () => attach.click(), "quiet");
  const clippings = el("ul", "", "clippings"); clippings.setAttribute("aria-label", "Files to send");
  const tools = el("div", "", "composer-tools");
  tools.append(attachButton, kind, send);
  form.append(typingLine, to, clippings, body, attach, tools);
  page.append(back, status, sheet, form);

  let selected = view.selected, overview, thread, loading = 0;
  const drafts = view.drafts; // host-owned workspace -> exact conversation -> { text, files, kind }
  const draft = () => { const k = selected && selected.type + ":" + selected.id; if (!k) return null; if (!drafts.has(k)) drafts.set(k, { text: "", files: [], kind: "message" }); return drafts.get(k); };
  body.addEventListener("input", () => { const d = draft(); if (d) d.text = body.value; updateLeaveWarning(); });

  const opened = [];
  const freeOpened = () => { opened.splice(0).forEach((u) => URL.revokeObjectURL(u)); };

  // A file of a message: its name and size, and Open only where the
  // backend says it is here (received, or a copy this device kept).
  const fileClipping = (m, f, i) => {
    const li = el("li", "", "clipping");
    li.append(el("span", f.name + " · " + size(f.size), "name"));
    const slot = el("span", "", "slot");
    const sentHere = m.dir === "out" && !m.via && !m.synced_from;
    const canOpen = sentHere ? f.openable === true : f.openable !== false && !["requestable", "requested", "unavailable"].includes(f.availability);
    const stateText = sentHere ? (f.openable === true ? "" : f.openable === false ? "Not kept on this device" : "Sent")
      : f.availability === "requestable" ? "With your other device (ask for it in Comic)" : f.availability === "requested" ? "Asked your other device" : f.availability === "unavailable" || f.openable === false ? "Not available" : "";
    if (f.note) li.append(el("small", f.note));
    if (stateText) li.append(el("small", stateText));
    if (canOpen) li.append(button("Open", async (e) => {
      const b = e.currentTarget; b.disabled = true; b.textContent = "Opening…";
      try {
        const r = await host.file(m.id, f.index === undefined ? i : f.index, m.dir);
        const bytes = r.bytes, image = r.image || sniffImage(bytes);
        const url = URL.createObjectURL(new Blob([bytes], { type: image || "application/octet-stream" }));
        opened.push(url);
        slot.replaceChildren();
        if (image) { const img = el("img"); img.src = url; img.alt = f.name; slot.append(img); }
        const a = el("a", image ? "Save" : "Download " + f.name); a.href = url; a.download = f.name; slot.append(a);
        b.hidden = true;
        a.focus();
      } catch (err) { b.disabled = false; b.textContent = "Open"; say("Could not open " + f.name + ": " + err.message); }
    }, "small"));
    li.append(slot);
    return li;
  };

  const renderClippings = () => {
    const d = draft();
    clippings.replaceChildren(...(d ? d.files : []).map((f, i) => {
      const li = el("li", "", "clipping");
      li.append(el("span", f.file.name + " · " + size(f.file.size), "name"), button("Remove", () => { d.files.splice(i, 1); renderClippings(); updateLeaveWarning(); }, "small"));
      return li;
    }));
    clippings.hidden = !d || !d.files.length;
  };
  attach.onchange = () => { const d = draft(); if (d) { for (const file of attach.files) d.files.push({ file }); renderClippings(); } attach.value = ""; updateLeaveWarning(); };
  form.ondragover = (e) => { if (selected && e.dataTransfer && [...e.dataTransfer.types].includes("Files")) e.preventDefault(); };
  form.ondrop = (e) => { const d = draft(); if (!d || !e.dataTransfer) return; e.preventDefault(); for (const file of e.dataTransfer.files) d.files.push({ file }); renderClippings(); updateLeaveWarning(); };

  const renderTo = () => {
    if (!selected || !thread) { to.textContent = ""; send.textContent = "Send"; return; }
    if (selected.type === "dm") { to.textContent = "To " + thread.peer.label + " · this conversation"; send.textContent = "Send to " + thread.peer.label; return; }
    const last = (thread.messages || []).at(-1), k = kind.value;
    to.textContent = "To " + thread.peer + (last ? " · continues “" + (last.body || "(files)").split("\n")[0].slice(0, 40) + "”" : " · a new conversation") +
      (k === "task" ? " · as a task they accept first" : k === "question" ? " · as a question their agent may answer" : "");
    send.textContent = (k === "task" ? "Give task to " : k === "question" ? "Ask " : "Send to ") + thread.peer;
  };
  kind.onchange = () => { const d = draft(); if (d) d.kind = kind.value; renderTo(); };
  const renderDraft = () => {
    if (!live) return;
    const d = draft();
    body.value = d ? d.text : ""; kind.value = d ? d.kind : "message";
    send.disabled = view.sending; renderClippings(); renderTo();
  };
  view.render = renderDraft;

  const load = async () => {
    if (!live) return;
    const version = ++loading;
    freeOpened();
    root.classList.toggle("reading", !!selected);
    if (!selected) {
      typing.setScope(null); thread = null;
      sheet.replaceChildren(el("p", "Choose a conversation from the contents.", "empty"));
      form.hidden = true; renderTo(); return;
    }
    const result = await host.api(selected.type === "dm" ? "/api/dm?id=" + encodeURIComponent(selected.id) : "/api/thread?id=" + encodeURIComponent(selected.id));
    if (!live || version !== loading) return;
    thread = result;
    const messages = thread.messages || [];
    typing.setScope(selected.type === "dm" ? { conv: selected.id } : messages.length ? { peer: thread.peer, thread: messages[0].id } : null);
    const title = selected.type === "dm" ? thread.peer.label : thread.peer;
    sheet.replaceChildren(el("h2", title), ...(messages.length ? [] : [el("p", "Nothing written here yet.", "empty")]), ...messages.map((m) => {
      const row = el("section", "", "message " + m.dir);
      const who = m.dir === "out" ? "You" : (m.author && m.author.label) || m.from;
      const meta = el("p", "", "meta");
      meta.append(el("strong", who), el("span", " · " + [m.kind && m.kind !== "message" ? m.kind : "", when(m.at || m.time), m.state_text || m.status || ""].filter(Boolean).join(" · ")));
      row.append(meta);
      const shown = m.deleted ? "" : m.edited && typeof m.text === "string" ? m.text : m.body;
      if (m.event) row.append(el("p", m.event, "event"));
      else if (m.deleted) row.append(el("p", "Message deleted", "gone"));
      else if (shown) { const p = el("p", shown, "text"); if (m.edited) p.append(el("small", " · edited")); row.append(p); }
      const files = m.deleted ? [] : m.files || m.attachments || [];
      if (files.length) { const ul = el("ul", "", "files"); files.forEach((f, i) => ul.append(fileClipping(m, f, i))); row.append(ul); }
      // reactions, edit and delete: what the backend says this device may do
      const conv = selected.type === "dm" ? selected.id : "";
      const ref = conv ? { conv, id: m.id, dir: m.dir } : { id: m.id, dir: m.dir };
      const can = (w) => Array.isArray(m.can) && m.can.includes(w);
      const act = async (what, body) => { try { const r = await host.api("/api/message/" + what, body); await refresh(); say(r.note || "Done.", "ok"); } catch (e) { say(e.message); } };
      // One row: the reactions people gave, then this device's own tools.
      const bar = el("div", "", "acts");
      if (!m.deleted) {
        for (const r of m.reactions || []) { const b = button(r.emoji + " " + (r.by || []).length, can("react") ? () => act("react", { ...ref, emoji: r.emoji, remove: !!r.mine }) : null, "reaction" + (r.mine ? " mine" : "")); b.title = (r.by || []).map((x) => (typeof x === "string" ? x : x.label || x.id)).join(", "); bar.append(b); }
        if (can("react")) for (const e of ["👍", "❤️", "🎉"]) { const b = button(e, () => act("react", { ...ref, emoji: e }), "pick"); b.setAttribute("aria-label", "React " + e); bar.append(b); }
      }
      if (!m.deleted && (can("edit") || can("delete"))) {
        if (can("edit")) bar.append(button("Edit", () => {
          const ta = el("textarea"); ta.value = shown; ta.setAttribute("aria-label", "Edited text");
          const hint = (m.kind === "question" || m.kind === "task") ? el("small", "Editing changes the shown text only; what their agent already received stays as sent.") : "";
          const box = el("div", "", "editbox"); box.append(ta, hint, button("Save", () => act("edit", { ...ref, text: ta.value.trim() })), button("Cancel", () => box.remove(), "quiet"));
          row.append(box); ta.focus();
        }, "small"));
        if (can("delete")) bar.append(button("Delete…", () => {
          const box = el("div", "", "editbox"); box.append(el("small", "Removed here and on devices that can read deletions; what was already read, saved or given to an agent stays with them."),
            button("Delete", () => act("delete", ref), "danger"), button("Cancel", () => box.remove(), "quiet"));
          row.append(box);
        }, "small"));
      }
      if (bar.childElementCount) row.append(bar);
      const decisions = (m.actions || []).filter((a) => ["accept", "decline", "resolve"].includes(a));
      if (decisions.length) {
        const bar = el("div", "", "decide");
        for (const a of decisions) bar.append(button(a.charAt(0).toUpperCase() + a.slice(1), async () => {
          try { await host.api("/api/act", { do: a, id: m.id }); await refresh(); } catch (e) { say(e.message); }
        }, a === "accept" ? "" : "quiet"));
        row.append(bar);
      }
      return row;
    }));
    form.hidden = false; kind.hidden = selected.type === "dm";
    renderDraft();
    page.scrollTop = page.scrollHeight;
  };

  const choose = async (item) => {
    if (!live) return;
    typing.setScope(null);
    const d = draft(); if (d) d.text = body.value;
    selected = view.selected = item;
    for (const b of toc.querySelectorAll("button")) b.setAttribute("aria-current", String(b.dataset.key === (item ? item.type + ":" + item.id : "")));
    try { await load(); say(""); } catch (e) { say(e.message, "error", "load"); }
    if (item) body.focus({ preventScroll: true });
  };

  const refresh = async () => {
    if (!live) return;
    try {
      overview = await host.api("/api/overview");
      if (!live) return;
      me.textContent = "You are " + (overview.person ? overview.person.label + " · " : "") + overview.me.address;
      const people = (overview.dms || []).map((d) => ({ type: "dm", id: d.id, label: d.kind === "group" ? d.title || "Group" : d.peer.label, line: d.last || (d.kind === "group" ? "Group" : "Chat"), unread: d.unread || 0 }));
      const devices = (overview.threads || []).filter((t) => !t.notice_only).map((t) => ({ type: "device", id: t.id, label: t.peer, line: t.last || t.title || "Chat", unread: t.unread || 0 }));
      const section = (title, items) => {
        if (!items.length) return [];
        const ul = el("ul");
        for (const item of items) {
          const key = item.type + ":" + item.id;
          const b = button("", () => choose(item)); b.dataset.key = key;
          b.append(el("strong", item.label), el("span", " — " + item.line.split("\n")[0]), item.unread ? el("em", " · " + item.unread + " new") : "");
          b.setAttribute("aria-current", String(!!selected && selected.type + ":" + selected.id === key));
          const li = el("li"); li.append(b); ul.append(li);
        }
        return [el("h3", title), ul];
      };
      toc.replaceChildren(...section("People", people), ...section("Devices and services", devices),
        !people.length && !devices.length ? el("p", "Nothing here yet.", "empty") : "");
      await load(); if (status.dataset.from === "load") say("");
    } catch (e) { say(e.message, "error", "load"); }
  };

  form.onsubmit = async (event) => {
    event.preventDefault();
    const d = draft();
    if (!selected || view.sending || !d || (!body.value.trim() && !d.files.length)) return;
    typing.stop();
    const text = body.value, target = { ...selected, peer: thread.peer, kind: kind.value, reply: (thread.messages || []).at(-1)?.id || "" }, files = d.files.slice();
    d.text = text; view.sending = true; send.disabled = true; updateLeaveWarning();
    try {
      const staged = [];
      for (const f of files) staged.push(await host.stage(f.file));
      const result = await host.api(target.type === "dm" ? "/api/dm/send" : "/api/send", target.type === "dm"
        ? { conv: target.id, body: text, files: staged }
        : { to: target.peer, body: text, kind: target.kind, reply_to: target.reply, files: staged });
      if (["failed", "not_delivered"].includes(result.state)) throw new Error(result.detail || "Could not send this message.");
      if (d.text === text) d.text = "";
      d.files = d.files.filter((f) => !files.includes(f));
      if (view.render) view.render();
      if (live) {
        await refresh();
        say(["queued", "waiting", "conv_waiting"].includes(result.state) ? "Saved here; waiting to send." : "Sent.", "ok");
      }
    } catch (e) { if (live) say(e.message + (files.length ? " Your files are still here." : "")); }
    finally { view.sending = false; if (view.render) view.render(); updateLeaveWarning(); }
  };
  body.addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); form.requestSubmit(); } });

  root.classList.add("notebook");
  root.append(contents, page);
  host.onOpen(async (chan, type) => {
    try {
      const route = type === "conversation" ? { conv: chan } : chan ? await host.api("/api/notify/resolve?chan=" + encodeURIComponent(chan)) : {};
      // Unknown channels go to the contents, without inventing a destination.
      await choose(route.conv ? { type: "dm", id: route.conv } : null);
      await refresh();
    } catch (e) { say(e.message); }
  });
  const stop = host.listen((event) => {
    if (event.type === "change") { const d = draft(); if (d) d.text = body.value; refresh(); }
    else { typing.disconnect(); say(event.type === "restart" ? "AgentNet is restarting. Reload in a moment to reconnect." : "Connection interrupted. Reload to reconnect.", "error", "load"); }
  });
  mounted.set(root, { stop() {
    const d = draft(); if (d) { d.text = body.value; d.kind = kind.value; }
    live = false; loading++; freeOpened(); typing.destroy(); stop();
    if (view.render === renderDraft) delete view.render;
    window.removeEventListener("beforeunload", beforeLeave);
    if (updateLeaveWarning === updateWarning) updateLeaveWarning = () => {};
    root.replaceChildren(); root.classList.remove("notebook", "reading");
  } });
  updateLeaveWarning();
  await refresh();
}
