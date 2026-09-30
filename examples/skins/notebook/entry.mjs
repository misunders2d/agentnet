// An independent visual flow: no default UI scripts, markup or globals.
// This deliberately small example shows how to build a complete replacement.
export async function mount(root, host) {
  const el = (tag, text, cls) => { const n = document.createElement(tag); if (text) n.textContent = text; if (cls) n.className = cls; return n; };
  const button = (text, fn) => { const b = el("button", text); b.type = "button"; b.onclick = fn; return b; };
  const header = el("header"), status = el("p", "", "status"), choices = el("select"), page = el("article"), form = el("form");
  choices.setAttribute("aria-label", "Conversation");
  const body = el("textarea"); body.setAttribute("aria-label", "Message"); body.placeholder = "Write a message…";
  const send = el("button", "Send"); send.type = "submit";
  const kind = el("select"); kind.setAttribute("aria-label", "Send as");
  for (const value of ["message", "question", "task"]) { const o = el("option", value); o.value = value; kind.append(o); }
  let selected, overview, thread, loading = 0; const drafts = new Map();
  const load = async () => {
    const version = ++loading;
    if (!selected) { page.replaceChildren(el("p", "Choose a conversation above.")); form.hidden = true; return; }
    const result = await host.api(selected.type === "dm" ? "/api/dm?id=" + encodeURIComponent(selected.id) : "/api/thread?id=" + encodeURIComponent(selected.id));
    if (version !== loading) return;
    thread = result;
    page.replaceChildren(...(thread.messages || []).map((m) => {
      const row = el("section", "", "message " + m.dir);
      row.append(el("small", (m.dir === "out" ? "You" : m.from) + " · " + (m.state || m.status || m.kind)), el("p", m.body));
      for (const a of m.actions || []) if (["accept", "decline", "resolve"].includes(a)) row.append(button(a, async () => {
        try { await host.api("/api/act", { do: a, id: m.id }); await refresh(); } catch (e) { status.textContent = e.message; }
      }));
      return row;
    }));
    form.hidden = false; kind.hidden = selected.type === "dm";
  };
  const refresh = async () => {
    try {
      overview = await host.api("/api/overview");
      const items = [...(overview.dms || []).map((d) => ({ type: "dm", id: d.id, label: d.peer.label + " · " + (d.title || "Chat") })),
        ...(overview.threads || []).filter((t) => !t.notice_only).map((t) => ({ type: "device", id: t.id, label: t.peer + " · " + (t.last || "Chat") }))];
      choices.replaceChildren(el("option", "Choose a conversation"));
      for (const item of items) { const o = el("option", item.label); o.value = item.type + ":" + item.id; choices.append(o); }
      if (selected) choices.value = selected.type + ":" + selected.id;
      choices.onchange = async () => {
        if (selected) drafts.set(selected.type + ":" + selected.id, body.value);
        selected = items.find((i) => i.type + ":" + i.id === choices.value);
        body.value = drafts.get(choices.value) || "";
        try { await load(); } catch (e) { status.textContent = e.message; }
      };
      await load(); status.textContent = "";
    } catch (e) { status.textContent = e.message; }
  };
  header.append(el("h1", "Notebook"), el("p", "Your conversations, one page at a time."), choices);
  form.append(body, kind, send); form.hidden = true;
  form.onsubmit = async (event) => {
    event.preventDefault(); if (!selected || !body.value.trim() || send.disabled) return;
    const text = body.value, target = { ...selected }; send.disabled = true;
    try {
      await host.api(target.type === "dm" ? "/api/dm/send" : "/api/send", target.type === "dm"
        ? { conv: target.id, body: text }
        : { to: thread.peer, body: text, kind: kind.value, reply_to: thread.messages.at(-1)?.id || "" });
      drafts.delete(target.type + ":" + target.id);
      if (selected?.id === target.id && body.value === text) body.value = "";
      await refresh();
    } catch (e) { status.textContent = e.message; } finally { send.disabled = false; }
  };
  root.classList.add("notebook"); root.append(header, status, page, form);
  host.onOpen(async (chan, type) => {
    try {
      const route = type === "conversation" ? { conv: chan } : await host.api("/api/notify/resolve?chan=" + encodeURIComponent(chan));
      // Unknown channels go to the conversation picker, without inventing a destination.
      selected = route.conv ? { type: "dm", id: route.conv } : null;
      await refresh();
    } catch (e) { status.textContent = e.message; }
  });
  host.listen((event) => { if (event.type === "change") refresh(); else status.textContent = "Connection interrupted. Reload to reconnect."; });
  await refresh();
}
