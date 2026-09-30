// UI host v1. The engine and authenticated transport outlive any visual flow.
// Installed skins are full-trust local code, selected explicitly by the user.
(async () => {
  "use strict";
  const mount = document.getElementById("skin");
  const browser = window.agentnetEngine;
  const json = async (path, body) => {
    if (!path.startsWith("/api/")) throw new Error("Expected an AgentNet API path");
    if (browser) return browser.api(path, body);
    const r = await fetch(path, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  };
  let openHandler, pending;
  window.agentnetOpen = (chan) => { if (openHandler) openHandler(chan); else pending = chan; };
  const host = {
    version: 1, platform: browser ? "browser" : "daemon", api: json,
    listen(fn) {
      if (browser) return browser.listen((seq) => fn({ type: "change", seq }));
      const es = new EventSource("/events");
      es.addEventListener("change", (e) => fn({ type: "change", seq: Number(e.data) }));
      es.addEventListener("restart", () => { es.close(); fn({ type: "restart" }); });
      es.onerror = () => { es.close(); fn({ type: "disconnect" }); };
      return () => es.close();
    },
    onOpen(fn) { openHandler = fn; if (pending !== undefined) { fn(pending); pending = undefined; } },
    async file(id, index) {
      if (browser) return json("/api/file?id=" + encodeURIComponent(id) + "&i=" + index);
      const r = await fetch("/api/files/" + encodeURIComponent(id) + "/" + index);
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      return { bytes: new Uint8Array(await r.arrayBuffer()) };
    },
    async stage(file) {
      if (browser) return { name: file.name, size: file.size, arrayBuffer: () => file.arrayBuffer() };
      const r = await fetch("/api/upload?name=" + encodeURIComponent(file.name), { method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: file });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      return (await r.json()).id;
    },
    skins: [],
    selectSkin(id) {
      if (!host.skins.some((s) => s.id === id)) throw new Error("Unknown skin");
      const url = new URL(location.href); url.searchParams.set("skin", id); location.assign(url);
    },
  };
  window.agentnet = host;
  const script = (src) => new Promise((resolve, reject) => {
    const s = document.createElement("script"); s.src = src; s.onload = resolve; s.onerror = reject; document.head.append(s);
  });
  const style = (href) => { const l = document.createElement("link"); l.rel = "stylesheet"; l.href = href; document.head.append(l); };
  const text = (tag, value) => { const e = document.createElement(tag); e.textContent = value; return e; };
  const button = (label, fn) => { const b = text("button", label); b.type = "button"; b.className = "btn"; b.onclick = fn; return b; };
  try {
    const r = await fetch("/assets/skins/index.json");
    if (!r.ok) throw new Error("Could not load the UI catalog");
    host.skins = await r.json();
    let saved = "default";
    try { saved = localStorage.getItem("agentnet.skin") || saved; } catch (_) { /* local preference */ }
    const requested = new URL(location.href).searchParams.get("skin") || saved;
    const selected = host.skins.find((s) => s.id === requested && s.api === 1) || host.skins[0];
    if (selected.id === "default") {
      const r = await fetch("/assets/default.html");
      if (!r.ok) throw new Error("Could not load the default interface");
      // Trusted bundled markup, never message text or a remotely supplied template.
      const doc = new DOMParser().parseFromString(await r.text(), "text/html");
      mount.replaceChildren(...doc.body.childNodes);
      style("/assets/app.css");
      await script("/assets/lenses.js"); await script("/assets/app.js");
    } else {
      let consent = false;
      try { consent = localStorage.getItem("agentnet.skin.trusted." + selected.id) === selected.digest; } catch (_) { /* ask */ }
      if (!consent) await new Promise((resolve) => {
        const card = text("section", ""); card.className = "join-card";
        card.append(text("h1", "Use “" + selected.name + "”?"), text("p", "This installed UI can read your chats and act as you, including sending messages and approving work. Use it only if you trust its author."),
          button("Use this UI", () => { try { localStorage.setItem("agentnet.skin.trusted." + selected.id, selected.digest); } catch (_) {} resolve(); }),
          button("Use AgentNet", () => host.selectSkin("default")));
        mount.replaceChildren(card);
      });
      mount.replaceChildren();
      if (selected.style) style("/assets/skins/" + selected.id + "/" + selected.style);
      const module = await import("/assets/skins/" + selected.id + "/" + selected.entry);
      if (typeof module.mount !== "function") throw new Error("The skin has no mount function");
      await module.mount(mount, host);
      if (!openHandler) throw new Error("This UI must register notification handling with host.onOpen");
      const openConversation = () => {
        const match = /^#conv=([0-9a-f]{64})$/.exec(location.hash);
        if (!match) return;
        history.replaceState(null, "", location.pathname + location.search);
        openHandler(match[1], "conversation");
      };
      window.addEventListener("hashchange", openConversation);
      openConversation();
      // A persistent escape hatch is owned by the host, not the skin.
      const back = button("Switch UI", () => host.selectSkin("default")); back.className = "skin-switch"; document.body.append(back);
    }
    try { localStorage.setItem("agentnet.skin", selected.id); } catch (_) {}
  } catch (e) {
    mount.replaceChildren(text("h1", "Could not open this UI"), text("p", e.message || "UI loading failed"), button("Use AgentNet", () => { const u = new URL(location.href); u.searchParams.set("skin", "default"); location.assign(u); }));
  }
})();
