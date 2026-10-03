// UI host v1. The engine and authenticated transport outlive any visual flow.
// Installed skins are full-trust local code, selected explicitly by the user.
// A skin renders inside its own shadow tree, so its stylesheet cannot reach
// the host's own controls; that is styling isolation only, not a sandbox.
//
// Workspaces: when the program serves /assets/workspaces.mjs, the host keeps
// one shell with one immutable transport per membership (a home of this
// computer's AgentNet, or a browser enrollment with its own engine). What a
// view holds when an operation starts (a send, an upload, a file open) stays
// bound to that membership: choosing another workspace never retargets it.
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
  // legacy is the one-workspace transport of an older program (no shell).
  const legacy = {
    platform: browser ? "browser" : "daemon", api: json,
    workspace: { id: "default", name: "This computer", endpoint: location.origin, address: "", realm: "", handle: "", state: "enrolled" },
    listen(fn) {
      if (browser) return browser.listen((seq) => fn({ type: "change", seq }));
      const es = new EventSource("/events");
      es.addEventListener("change", (e) => fn({ type: "change", seq: Number(e.data) }));
      es.addEventListener("restart", () => { es.close(); fn({ type: "restart" }); });
      es.onerror = () => { es.close(); fn({ type: "disconnect" }); };
      return () => es.close();
    },
    async file(id, index, dir) { // dir: the message's own dir ("in" or "out"); it makes the reference exact
      const q = dir ? "&dir=" + encodeURIComponent(dir) : "";
      if (browser) return json("/api/file?id=" + encodeURIComponent(id) + "&i=" + index + q);
      const r = await fetch("/api/files/" + encodeURIComponent(id) + "/" + index + (dir ? "?dir=" + encodeURIComponent(dir) : ""));
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      return { bytes: new Uint8Array(await r.arrayBuffer()) };
    },
    async stage(file) {
      if (browser) return { name: file.name, size: file.size, arrayBuffer: () => file.arrayBuffer() };
      const r = await fetch("/api/upload?name=" + encodeURIComponent(file.name), { method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: file });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      return (await r.json()).id;
    },
  };
  const common = {
    version: 1,
    onOpen(fn) { openHandler = fn; if (pending !== undefined) { fn(pending); pending = undefined; } },
    skins: [],
    selectSkin(id) {
      if (!common.skins.some((s) => s.id === id)) throw new Error("Unknown skin");
      const url = new URL(location.href); url.searchParams.set("skin", id); location.assign(url);
    },
  };

  // ---- workspaces: one shell, immutable bound transports
  //
  // The daemon's page lists its memberships from the program; a browser
  // device brings its own shell and enrollments (device.mjs). Without the
  // module (an older program) there is one workspace on the legacy transport.
  let shell = null, ws = null;
  const memberships = window.agentnetWorkspaces || null;
  try {
    ws = await import("/assets/workspaces.mjs");
    if (memberships) shell = memberships.shell;
    else if (!browser) { shell = new ws.WorkspaceShell(); await shell.load(); }
  } catch (e) { shell = null; ws = null; }
  if (shell && !shell.active) shell = null; // nothing enrolled: the legacy transport, as before
  const entryOf = (id) => shell && shell.members.get(id);
  const workspaces = shell ? Object.freeze({
    list: () => shell.list(),
    active: () => shell.active,
    has: (id) => !!(entryOf(id) && entryOf(id).connected), // known and connected here, by local registration only
    select: (id) => { if (!workspaces.has(id)) throw new Error("Unknown workspace"); switchTo(id); },
    state: (id) => shell.state(id), // this membership's own view state (drafts, open conversation), kept by the renderer
    onChange: (fn) => shell.onChange(fn),
    join: (body) => (memberships ? memberships.join(body) : shell.join(body)),
    disconnect: (id) => (memberships ? memberships.disconnect(id) : shell.disconnect(id)),
  }) : null;
  // hostFor binds one membership: the transport is that membership's own
  // and never changes; what the host adds (skins, notification routing,
  // the workspace list) is the same for every one.
  const hostFor = (id) => {
    const bound = shell ? shell.bind(id) : legacy;
    return Object.freeze(Object.assign({}, bound, common, { workspaces }));
  };
  let switching = null; // the renderer's capture hook, set once it is mounted
  const switchTo = (id) => {
    const previous = shell.active;
    if (id === previous) return;
    if (switching) switching(previous, shell.state(previous));
    shell.select(id);
  };
  window.agentnet = hostFor(shell ? shell.active : "default");
  if (shell) shell.onChange((e) => { window.agentnet = hostFor(e.id); }); // new operations take the new host; ones under way keep theirs
  // The renderer registers how it keeps and restores a membership's view:
  // capture(previous, state) before the selection changes, switched(event)
  // after. app.js sets both; a skin gets remounted with the new host instead.
  window.agentnetWorkspace = shell ? {
    hook(h) { switching = h.capture || null; if (h.switched) shell.onChange(h.switched); },
    async recoverNative(host, identity) {
      const id = host.workspace.id;
      if (browser || memberships || host.platform !== "daemon" || window.agentnet !== host || shell.active !== id) throw new Error("Workspace changed during reconnect");
      await shell.recoverNative(id, identity);
      if (window.agentnet !== host || shell.active !== id) throw new Error("Workspace changed during reconnect");
      window.agentnet = hostFor(id);
      return window.agentnet;
    },
  } : null;

  const script = (src) => new Promise((resolve, reject) => {
    const s = document.createElement("script"); s.src = src; s.onload = resolve; s.onerror = reject; document.head.append(s);
  });
  const style = (href, into) => { const l = document.createElement("link"); l.rel = "stylesheet"; l.href = href; (into || document.head).append(l); };
  const text = (tag, value) => { const e = document.createElement(tag); e.textContent = value; return e; };
  const button = (label, fn) => { const b = text("button", label); b.type = "button"; b.className = "btn"; b.onclick = fn; return b; };

  // hostBar is the host's own strip over any installed skin: which
  // interface this is, who you are here, and the way back to the built-in
  // one. It lives in its own shadow tree with its own styles, above the
  // skin, so no skin stylesheet can hide, move or restyle it.
  let notificationHint = () => {};
  const hostBar = (selected) => {
    const bar = document.createElement("div");
    bar.id = "host-bar";
    const sh = bar.attachShadow({ mode: "open" });
    // Styles come from core.css, linked into this shadow tree: the page's
    // CSP allows no inline styles, and no skin stylesheet reaches in here.
    const css = document.createElement("link"); css.rel = "stylesheet"; css.href = "/assets/core.css";
    const notice = document.createElement("div"); notice.className = "host-notification"; notice.hidden = true; notice.setAttribute("role", "status");
    notificationHint = (label, actionable = false) => {
      notice.replaceChildren(); notice.hidden = !label;
      if (!label) return;
      const item = actionable ? button(label, () => common.selectSkin("default")) : text("span", label);
      item.className = "host-pill";
      notice.append(item);
    };
    const pill = text("button", "AgentNet ▾"); pill.className = "host-pill"; pill.setAttribute("aria-haspopup", "menu"); pill.setAttribute("aria-expanded", "false");
    pill.title = "Interface: " + selected.name + ". Switch, or see who you are here.";
    const menu = document.createElement("div"); menu.className = "host-menu"; menu.setAttribute("role", "menu"); menu.hidden = true;
    const who = text("p", "Interface: " + selected.name + " · " + window.agentnet.platform);
    menu.append(who);
    for (const s of common.skins) {
      if (s.id === selected.id) continue;
      const b = text("button", (s.id === "default" ? "Back to AgentNet" : "Switch to " + s.name)); b.setAttribute("role", "menuitem");
      b.onclick = () => common.selectSkin(s.id);
      menu.append(b);
    }
    const toggle = async (open) => {
      menu.hidden = !open;
      pill.setAttribute("aria-expanded", String(open));
      if (open) {
        const h = window.agentnet; // the membership shown now
        try { const o = await h.api("/api/overview"); who.textContent = "Interface: " + selected.name + " · you are " + o.me.address + (shell ? " in " + h.workspace.name : ""); } catch (_) { /* the skin may be offline */ }
        (menu.querySelector("button") || pill).focus();
      }
    };
    pill.onclick = () => toggle(menu.hidden);
    sh.addEventListener("keydown", (e) => { if (e.key === "Escape" && !menu.hidden) { toggle(false); pill.focus(); } });
    sh.append(css, notice, pill, menu);
    return bar;
  };

  // workspaceBar is the persistent switcher, owned by the host outside
  // every interface: the module's selector, Join, Disconnect and Reconnect.
  // Joining needs the invitation you were given (it names the server);
  // leaving keeps this device's keys and history for that workspace and
  // revokes nothing there, and Reconnect routes that same membership again
  // (this computer's program only: a browser enrollment has no program to
  // ask).
  const workspaceBar = () => {
    if (!shell || !ws) return;
    const root = document.createElement("div");
    root.id = "workspace-shell";
    document.body.prepend(root);
    style("/assets/workspaces.css");
    const status = text("span", ""); status.className = "workspace-note"; status.setAttribute("role", "status");
    const note = (s) => { status.textContent = s; };
    const bar = ws.mountWorkspaceSwitcher(root, shell, { beforeSwitch: (previous, st) => { if (switching) switching(previous, st); }, afterSwitch: () => note("") });
    const tools = document.createElement("span"); tools.className = "workspace-tools";
    const joinBtn = button("Join a workspace…", () => joinForm());
    const canReconnect = !memberships && typeof shell.reconnect === "function";
    const leaveBtn = button("Disconnect…", async () => {
      const id = shell.active, b = entryOf(id) && entryOf(id).binding;
      if (!b) return;
      if (id === "default") { note("This computer's own workspace stays; only joined ones can be disconnected."); return; }
      if (!window.confirm("Disconnect from " + b.name + " (" + new URL(b.endpoint).host + ")? Your keys and history for it stay on this device; nothing is revoked at that server.")) return;
      try {
        if (switching) switching(id, shell.state(id));
        await workspaces.disconnect(id);
        bar.refresh();
        note("Disconnected from " + b.name + ". Its keys and history stay here" + (canReconnect ? "; Reconnect… connects it again." : "; join again with a new invitation to reconnect."));
      } catch (e) { note(e.message); }
    });
    tools.append(joinBtn, leaveBtn);
    if (canReconnect) tools.append(button("Reconnect…", () => reconnectForm()));
    tools.append(status);
    const reconnectForm = async () => {
      if (root.querySelector(".workspace-reconnect")) return;
      note("");
      let gone;
      try { gone = await shell.disconnected(); } catch (e) { note(e.message); return; }
      if (!gone.length) { note("No disconnected workspace here."); return; }
      if (root.querySelector(".workspace-reconnect")) return; // opened again while listing
      const f = document.createElement("form"); f.className = "workspace-join workspace-reconnect";
      const pickL = text("label", "Disconnected workspace"); const pick = document.createElement("select"); pick.setAttribute("aria-label", "Disconnected workspace");
      for (const w of gone) { const o = text("option", (w.name || "Unnamed workspace") + " · " + new URL(w.endpoint).host + " · " + w.address); o.value = w.id; pick.append(o); }
      pickL.append(pick);
      const err = text("p", ""); err.className = "workspace-error"; err.setAttribute("role", "alert");
      const go = button("Reconnect", async () => {
        const w = gone.find((x) => x.id === pick.value);
        if (!w) return;
        go.disabled = true; err.textContent = "";
        try {
          await shell.reconnect(w.id);
          bar.refresh(); f.remove();
          note("Reconnected " + (w.name || "the workspace") + " as " + w.address + ", with its keys and history. Choose it above to work there.");
        } catch (e) { err.textContent = e.message; go.disabled = false; }
      });
      go.className = "btn primary";
      const cancel = button("Cancel", () => f.remove());
      f.addEventListener("submit", (e) => { e.preventDefault(); go.click(); });
      f.append(pickL, err, go, cancel);
      root.append(f);
      pick.focus();
    };
    root.querySelector(".workspace-bar").append(tools);
    const joinForm = () => {
      if (root.querySelector(".workspace-join:not(.workspace-reconnect)")) return; // the reconnect form shares only its look
      note("");
      const f = document.createElement("form"); f.className = "workspace-join";
      const field = (tag, label, attrs) => { const l = text("label", label); const i = document.createElement(tag); Object.assign(i, attrs); l.append(i); return [l, i]; };
      const [nameL, name] = field("input", "Name it, for you", { maxLength: 48, required: true, placeholder: "e.g. Acme" });
      const [invL, invite] = field("textarea", "Invitation you were given", { rows: 2, required: true, placeholder: "agentnet-invite-v1:…", spellcheck: false });
      const [agentL, agent] = field("input", "This device's name there", { maxLength: 32, required: true, placeholder: "laptop", autocapitalize: "none" });
      const err = text("p", ""); err.className = "workspace-error"; err.setAttribute("role", "alert");
      let retryID = null; // a join that failed keeps its allocated id: the next try continues it, never a second membership
      const go = button("Join", async () => {
        const body = { name: name.value.trim(), invite: invite.value.trim(), agent: agent.value.trim(), ...(retryID ? { id: retryID } : {}) };
        if (!body.name || !body.invite || !body.agent) { err.textContent = "A name, the invitation and a device name are all needed."; return; }
        go.disabled = true; err.textContent = "";
        try {
          const h = await workspaces.join(body);
          invite.value = ""; // the invitation is single-use and is not kept on the page
          bar.refresh(); f.remove();
          note("Joined " + body.name + " as " + (h.workspace.address || body.agent) + ". Choose it above to work there.");
        } catch (e) {
          if (e.retryID) { retryID = e.retryID; f.dataset.retry = retryID; }
          err.textContent = e.message + (retryID ? " Paste a new invitation and press Join again: the same joining workspace is retried." : "");
          go.disabled = false;
        }
      });
      go.className = "btn primary";
      const cancel = button("Cancel", () => f.remove());
      f.addEventListener("submit", (e) => { e.preventDefault(); go.click(); });
      f.append(nameL, invL, agentL, err, go, cancel);
      root.append(f);
      name.focus();
    };
  };

  try {
    const r = await fetch("/assets/skins/index.json");
    if (!r.ok) throw new Error("Could not load the UI catalog");
    const offered = await r.json();
    let localSkins = null;
    try { localSkins = await import("/assets/local-skins.mjs"); } catch (_) { /* older program: hosted packages still work */ }
    const refreshSkins = async () => {
      const local = localSkins ? await localSkins.catalog().catch(() => []) : [];
      // Every immutable workspace host keeps this SAME array reference.
      common.skins.splice(0, common.skins.length, ...offered, ...local);
      window.dispatchEvent(new Event("agentnet-skins-change"));
    };
    await refreshSkins();
    let saved = "default";
    try { saved = localStorage.getItem("agentnet.skin") || saved; } catch (_) { /* local preference */ }
    const requested = new URL(location.href).searchParams.get("skin") || saved;
    const selected = common.skins.find((s) => s.id === requested && s.api === 1) || common.skins[0];
    workspaceBar();
    if (selected.id === "default") {
      const r = await fetch("/assets/default.html");
      if (!r.ok) throw new Error("Could not load the default interface");
      // Trusted bundled markup, never message text or a remotely supplied template.
      const doc = new DOMParser().parseFromString(await r.text(), "text/html");
      mount.replaceChildren(...doc.body.childNodes);
      style("/assets/app.css");
      await script("/assets/lenses.js"); await script("/assets/app.js");
      const manager = document.getElementById("local-interfaces");
      if (manager && localSkins) localSkins.manager(manager, { changed: refreshSkins });
    } else {
      let consent = false;
      try { consent = localStorage.getItem("agentnet.skin.trusted." + selected.id) === selected.digest; } catch (_) { /* ask */ }
      if (!consent) await new Promise((resolve) => {
        const card = text("section", ""); card.className = "join-card";
        card.append(text("h1", "Use “" + selected.name + "”?"), text("p", "This installed UI can read your chats and act as you, including sending messages and approving work. Use it only if you trust its author."), text("p", "Digest: " + selected.digest),
          button("Use this UI", () => { try { localStorage.setItem("agentnet.skin.trusted." + selected.id, selected.digest); } catch (_) {} resolve(); }),
          button("Use AgentNet", () => common.selectSkin("default")));
        mount.replaceChildren(card);
      });
      if (selected.local) await localSkins.activate();
      const packagePath = selected.local ? "/local-skins/" + selected.digest + "/" : "/assets/skins/" + selected.id + "/";
      mount.replaceChildren();
      // The skin gets a root of its own inside a shadow tree: its stylesheet
      // applies there and nowhere else. Colors such as --surface still
      // inherit from the page.
      const shadow = mount.attachShadow({ mode: "open" });
      style("/assets/core.css", shadow); // semantic colors and the root's own box; the skin's sheet comes after it
      if (selected.style) style(packagePath + selected.style, shadow);
      const module = await import(packagePath + selected.entry);
      if (typeof module.mount !== "function") throw new Error("The skin has no mount function");
      // mountSkin gives the skin a fresh root over one membership's host.
      // On a workspace switch the skin is mounted again over the new one:
      // what it held for the old membership goes out with its root, and
      // operations it started keep the host they started with.
      let root = null, mounting = Promise.resolve();
      const mountSkin = async () => {
        openHandler = undefined;
        if (root) { if (typeof module.unmount === "function") { try { await module.unmount(root); } catch (_) { /* replaced anyway */ } } root.remove(); }
        root = document.createElement("div");
        root.id = "skin";
        root.className = "skin-root";
        shadow.append(root);
        await module.mount(root, window.agentnet);
        if (!openHandler) throw new Error("This UI must register notification handling with host.onOpen");
      };
      await mountSkin();
      if (window.agentnetWorkspace) window.agentnetWorkspace.hook({ switched: () => { mounting = mountSkin().catch((e) => { root.replaceChildren(text("p", e.message)); }); } });
      // Notification fallback belongs to the host, outside the skin. Its
      // explicit action preserves the exact fragment and workspace on reload.
      document.body.append(hostBar(selected));
      // A notification's #conv=<hash>[&workspace=<id>]: the workspace named
      // must be one registered here; an unknown one opens nothing (never the
      // current workspace instead).
      const openConversation = () => {
        const hash = location.hash || "";
        notificationHint("");
        const review = hash === "#review" || hash.startsWith("#review&");
        if (review || hash.startsWith("#msg=")) {
          const q = new URLSearchParams(hash.slice(1)), wid = q.get("workspace");
          if (!review && !/^[0-9a-f]{32}$/.test(q.get("msg") || "")) return;
          if (wid !== null && (!workspaces || !workspaces.has(wid))) {
            notificationHint("Notification workspace unavailable here");
            return;
          }
          notificationHint(review ? "Open review in AgentNet" : "Open message in AgentNet", true);
          return; // never consume this destination until the person opens it
        }
        if (!hash.startsWith("#conv=")) return;
        history.replaceState(null, "", location.pathname + location.search);
        const q = new URLSearchParams(hash.slice(1));
        const conv = q.get("conv") || "", wid = q.get("workspace");
        if (!/^[0-9a-f]{64}$/.test(conv)) return;
        if (wid !== null) {
          if (!workspaces || !workspaces.has(wid)) return;
          if (wid !== shell.active) { const once = shell.onChange(() => { once(); mounting.then(() => openHandler && openHandler(conv, "conversation")); }); switchTo(wid); return; }
        }
        openHandler(conv, "conversation");
      };
      window.addEventListener("hashchange", openConversation);
      openConversation();
    }
    try { localStorage.setItem("agentnet.skin", selected.id); } catch (_) {}
  } catch (e) {
    mount.replaceChildren(text("h1", "Could not open this UI"), text("p", e.message || "UI loading failed"), button("Use AgentNet", () => { const u = new URL(location.href); u.searchParams.set("skin", "default"); location.assign(u); }));
  }
})();
