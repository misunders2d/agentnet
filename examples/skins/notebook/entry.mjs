// An independent visual flow: no default UI scripts, markup or globals.
// A notebook: one page per conversation, a table of contents on the left,
// files as clippings. What this interface does not do is said on the page,
// never hidden: the host's AgentNet button leads to the built-in one.
// mounted keeps, per root, what unmount stops: the host stream this page
// listens to. The host mounts the notebook again over another workspace's
// host when the person switches; the old root goes out with its stream.
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
  const button = (text, fn, cls) => { const b = el("button", text, cls); b.type = "button"; b.onclick = fn; return b; };
  const size = (n) => (n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(1) + " MB");
  const sniffImage = (b) => (b.length > 8 && b[0] === 0x89 && b[1] === 0x50 ? "image/png" : b.length > 3 && b[0] === 0xff && b[1] === 0xd8 ? "image/jpeg"
    : b.length > 6 && b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46 ? "image/gif" : b.length > 12 && b[8] === 0x57 && b[9] === 0x45 && b[10] === 0x42 && b[11] === 0x50 ? "image/webp" : "");

  const header = el("header"), status = el("p", "", "status"), toc = el("nav"), page = el("article"), form = el("form"), me = el("p", "", "me");
  toc.setAttribute("aria-label", "Conversations");
  const body = el("textarea"); body.setAttribute("aria-label", "Message"); body.placeholder = "Write here…";
  const typingLine = el("p", "", "typing"), typingSettings = el("div");
  const typing = mountTyping({ api: (path, value) => host.api(path, value), input: body, line: typingLine, settings: typingSettings });
  const to = el("p", "", "to"); to.setAttribute("aria-live", "polite");
  const send = el("button", "Send", "send"); send.type = "submit";
  const kind = el("select"); kind.setAttribute("aria-label", "Send as");
  for (const value of ["message", "question", "task"]) { const o = el("option", value.charAt(0).toUpperCase() + value.slice(1)); o.value = value; kind.append(o); }
  const attach = el("input"); attach.type = "file"; attach.multiple = true; attach.setAttribute("aria-label", "Files to send"); attach.hidden = true;
  const attachButton = button("Add a file…", () => attach.click());
  const clippings = el("ul", "", "clippings"); clippings.setAttribute("aria-label", "Files to send");
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
    li.append(el("span", f.name + " · " + size(f.size)));
    const slot = el("span", "", "slot");
    const sentHere = m.dir === "out" && !m.via && !m.synced_from;
    // Open only where the backend says so: a sent file needs an explicit
    // openable (a kept copy); a received one opens unless it says not.
    const canOpen = sentHere ? f.openable === true : f.openable !== false && !["requestable", "requested", "unavailable"].includes(f.availability);
    const stateText = sentHere ? (f.openable === true ? "" : f.openable === false ? "Not kept on this device" : "Sent")
      : f.availability === "requestable" ? "With your other device (ask for it in AgentNet)" : f.availability === "requested" ? "Asked your other device" : f.availability === "unavailable" || f.openable === false ? "Not available" : "";
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
      } catch (err) { b.disabled = false; b.textContent = "Open"; status.textContent = "Could not open " + f.name + ": " + err.message; }
    }));
    li.append(slot);
    return li;
  };

  const renderClippings = () => {
    const d = draft();
    clippings.replaceChildren(...(d ? d.files : []).map((f, i) => {
      const li = el("li", "", "clipping");
      li.append(el("span", f.file.name + " · " + size(f.file.size)), button("Remove", () => { d.files.splice(i, 1); renderClippings(); updateLeaveWarning(); }));
      return li;
    }));
    clippings.hidden = !d || !d.files.length;
  };
  attach.onchange = () => { const d = draft(); if (d) { for (const file of attach.files) d.files.push({ file }); renderClippings(); } attach.value = ""; updateLeaveWarning(); };
  form.ondragover = (e) => { if (selected && e.dataTransfer && [...e.dataTransfer.types].includes("Files")) e.preventDefault(); };
  form.ondrop = (e) => { const d = draft(); if (!d || !e.dataTransfer) return; e.preventDefault(); for (const file of e.dataTransfer.files) d.files.push({ file }); renderClippings(); updateLeaveWarning(); };

  const renderTo = () => {
    if (!selected || !thread) { to.textContent = ""; send.textContent = "Send"; return; }
    if (selected.type === "dm") { to.textContent = "To " + thread.peer.label + " · this DM"; send.textContent = "Send to " + thread.peer.label; return; }
    const last = (thread.messages || []).at(-1), k = kind.value;
    to.textContent = "To " + thread.peer + (last ? " · continues “" + (last.body || "(files)").split("\n")[0].slice(0, 40) + "”" : " · a new conversation") +
      (k === "task" ? " · as a task they accept first" : k === "question" ? " · as a question their responder may answer" : "");
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
    if (!selected) { typing.setScope(null); page.replaceChildren(el("p", "Choose a conversation from the contents.")); form.hidden = true; renderTo(); return; }
    const result = await host.api(selected.type === "dm" ? "/api/dm?id=" + encodeURIComponent(selected.id) : "/api/thread?id=" + encodeURIComponent(selected.id));
    if (!live || version !== loading) return;
    thread = result;
    typing.setScope(selected.type === "dm" ? { conv: selected.id } : { peer: thread.peer, thread: thread.messages[0].id });
    page.replaceChildren(el("h2", selected.type === "dm" ? thread.peer.label : thread.peer), ...(thread.messages || []).map((m) => {
      const row = el("section", "", "message " + m.dir);
      const who = m.dir === "out" ? "You" : (m.author && m.author.label) || m.from;
      row.append(el("small", who + " · " + (m.kind || "message") + " · " + (m.state_text || m.state || m.status || "")));
      const shown = m.deleted ? "" : m.edited && typeof m.text === "string" ? m.text : m.body;
      if (m.event) row.append(el("p", m.event, "event"));
      else if (m.deleted) row.append(el("p", "Message deleted", "gone"));
      else if (shown) { const p = el("p", shown); if (m.edited) p.append(el("small", " · edited")); row.append(p); }
      const files = m.deleted ? [] : m.files || m.attachments || [];
      if (files.length) { const ul = el("ul", "", "files"); files.forEach((f, i) => ul.append(fileClipping(m, f, i))); row.append(ul); }
      // reactions, edit and delete: what the backend says this device may do
      const conv = selected.type === "dm" ? selected.id : "";
      const ref = conv ? { conv, id: m.id, dir: m.dir } : { id: m.id, dir: m.dir };
      const can = (w) => Array.isArray(m.can) && m.can.includes(w);
      const act = async (what, body) => { try { const r = await host.api("/api/message/" + what, body); await refresh(); status.textContent = r.note || "Done."; } catch (e) { status.textContent = e.message; } };
      if (!m.deleted && ((m.reactions || []).length || can("react"))) {
        const line = el("div", "", "reactions");
        for (const r of m.reactions || []) { const b = button(r.emoji + " " + (r.by || []).length, can("react") ? () => act("react", { ...ref, emoji: r.emoji, remove: !!r.mine }) : null, "reaction" + (r.mine ? " mine" : "")); b.title = (r.by || []).map((x) => (typeof x === "string" ? x : x.label || x.id)).join(", "); line.append(b); }
        if (can("react")) for (const e of ["👍", "❤️", "🎉"]) line.append(button(e, () => act("react", { ...ref, emoji: e }), "pick"));
        row.append(line);
      }
      if (!m.deleted && (can("edit") || can("delete"))) {
        const tools = el("div", "", "tools");
        if (can("edit")) tools.append(button("Edit", () => {
          const ta = el("textarea"); ta.value = shown; const hint = (m.kind === "question" || m.kind === "task") ? el("small", "Editing changes the shown text only; what their agent already received stays as sent.") : "";
          const box = el("div", "", "editbox"); box.append(ta, hint, button("Save", () => act("edit", { ...ref, text: ta.value.trim() })), button("Cancel", () => box.remove()));
          row.append(box);
        }));
        if (can("delete")) tools.append(button("Delete…", () => {
          const box = el("div", "", "editbox"); box.append(el("small", "Removed here and on devices that can read deletions; what was already read, saved or given to an agent stays with them."),
            button("Delete", () => act("delete", ref)), button("Cancel", () => box.remove()));
          row.append(box);
        }));
        row.append(tools);
      }
      for (const a of m.actions || []) if (["accept", "decline", "resolve"].includes(a)) row.append(button(a.charAt(0).toUpperCase() + a.slice(1), async () => {
        try { await host.api("/api/act", { do: a, id: m.id }); await refresh(); } catch (e) { status.textContent = e.message; }
      }));
      return row;
    }));
    form.hidden = false; kind.hidden = selected.type === "dm";
    renderDraft();
  };

  const choose = async (item) => {
    if (!live) return;
    typing.setScope(null);
    const d = draft(); if (d) d.text = body.value;
    selected = view.selected = item;
    for (const b of toc.querySelectorAll("button")) b.setAttribute("aria-current", String(b.dataset.key === (item ? item.type + ":" + item.id : "")));
    try { await load(); status.textContent = ""; } catch (e) { status.textContent = e.message; }
  };

  const refresh = async () => {
    if (!live) return;
    try {
      overview = await host.api("/api/overview");
      if (!live) return;
      me.textContent = "You are " + overview.me.address + (overview.person ? " (" + overview.person.label + ")" : "");
      const people = (overview.dms || []).map((d) => ({ type: "dm", id: d.id, label: d.peer.label, line: d.title || "Chat", unread: d.unread || 0 }));
      const devices = (overview.threads || []).filter((t) => !t.notice_only).map((t) => ({ type: "device", id: t.id, label: t.peer, line: t.last || "Chat", unread: t.unread || 0 }));
      const section = (title, items) => {
        if (!items.length) return [];
        const ul = el("ul");
        for (const item of items) {
          const key = item.type + ":" + item.id;
          const b = button("", () => choose(item)); b.dataset.key = key;
          b.append(el("strong", item.label), el("span", " — " + item.line), item.unread ? el("em", " · " + item.unread + " new") : "");
          b.setAttribute("aria-current", String(selected && selected.type + ":" + selected.id === key));
          const li = el("li"); li.append(b); ul.append(li);
        }
        return [el("h3", title), ul];
      };
      toc.replaceChildren(...section("People", people), ...section("Devices and services", devices),
        !people.length && !devices.length ? el("p", "Nothing here yet.") : "");
      await load(); status.textContent = "";
    } catch (e) { status.textContent = e.message; }
  };

  header.append(el("h1", "Notebook"), el("p", "Your conversations, one page at a time."), me,
    el("p", "This notebook reads, replies, decides on what is held here, sends and opens files. Not in this notebook: finding new people, adding devices, other settings and notifications. Use the AgentNet button for those.", "limits"),
    button("Typing preferences", () => typing.showSettings()), typingSettings);
  form.append(typingLine, to, clippings, body, attach, attachButton, kind, send); form.hidden = true;
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
        status.textContent = ["queued", "waiting", "conv_waiting"].includes(result.state) ? "Saved here; waiting to send." : "Sent.";
      }
    } catch (e) { if (live) status.textContent = e.message + (files.length ? " Your files are still here." : ""); }
    finally { view.sending = false; if (view.render) view.render(); updateLeaveWarning(); }
  };
  root.classList.add("notebook");
  const columns = el("div", "", "columns"); columns.append(toc, page);
  root.append(header, status, columns, form);
  host.onOpen(async (chan, type) => {
    try {
      const route = type === "conversation" ? { conv: chan } : await host.api("/api/notify/resolve?chan=" + encodeURIComponent(chan));
      // Unknown channels go to the contents, without inventing a destination.
      await choose(route.conv ? { type: "dm", id: route.conv } : null);
      await refresh();
    } catch (e) { status.textContent = e.message; }
  });
  const stop = host.listen((event) => { if (event.type === "change") { const d = draft(); if (d) d.text = body.value; refresh(); } else { typing.disconnect(); status.textContent = "Connection interrupted. Reload to reconnect."; } });
  mounted.set(root, { stop() {
    const d = draft(); if (d) { d.text = body.value; d.kind = kind.value; }
    live = false; loading++; freeOpened(); typing.destroy(); stop();
    if (view.render === renderDraft) delete view.render;
    window.removeEventListener("beforeunload", beforeLeave);
    if (updateLeaveWarning === updateWarning) updateLeaveWarning = () => {};
  } });
  updateLeaveWarning();
  await refresh();
}
