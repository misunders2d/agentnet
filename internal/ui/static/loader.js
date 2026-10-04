// UI host v1 (docs/UI_SKINS.md). AgentNet is the core: the program and the
// documented skin contract this file serves. Every interface is a skin, a
// complete package loaded the same way: its manifest from the catalog
// (/assets/skins/index.json, plus the packages stored in this browser),
// its document rules (fonts) adopted by the page, its stylesheet and root
// inside a shadow tree of #skin, and its entry's mount/unmount. Built-in
// skins (Comic, the default) are packages too; only trust differs: a
// built-in one is trusted by this host's fixed list, any other one after
// the person accepts its exact digest. A shadow tree is styling isolation,
// not a sandbox: every skin runs with the page's full trust.
//
// Every skin but Comic gets the host's switcher above it (skinbar.mjs):
// one step back to Comic, the skin menu and the workspace control. Comic
// draws its own (Settings → Appearance → Skin, and its workspace menu).
//
// Workspaces: when the program serves /assets/workspaces.mjs, the host keeps
// one shell with one immutable transport per membership (a home of this
// computer's AgentNet, or a browser enrollment with its own engine). What a
// skin holds when an operation starts (a send, an upload, a file open) stays
// bound to that membership: choosing another workspace never retargets it;
// the skin is mounted again over a host bound to the new one.
(async () => {
  "use strict";
  const page = document.getElementById("skin");
  const browser = window.agentnetEngine;
  // Which skin opens and whether it needs consent: built-in skins are
  // trusted by the host's fixed list, never by a manifest (skin-choice.mjs).
  const { HOME, mark, choose, trusted: isTrusted, takenName } = await import("/assets/skin-choice.mjs");
  const json = async (path, body) => {
    if (!path.startsWith("/api/")) throw new Error("Expected an AgentNet API path");
    if (browser) return browser.api(path, body);
    const r = await fetch(path, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  };
  // single is the one-workspace transport of a program without the
  // workspace module.
  const single = {
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

  const { daemonDriveProvider, boundDriveProvider } = await import("/assets/drivespace.mjs");
  single.drive = boundDriveProvider(browser ? browser.driveService() : daemonDriveProvider(json, (path, init) => fetch(path, init)));

  // ---- what every host carries: notification routing, the catalog
  //
  // onOpen(fn, kinds): fn(target, kind, context). Every skin gets "channel"
  // (a browser notification's channel) and "conversation" (a DM id);
  // "message" ({conv, dir} in context) and "review" only when it lists
  // them. A destination the skin does not take is offered by the host's
  // switcher, with the way to open it in Comic.
  let openHandler = null, openKinds = new Set(), pendingChannel;
  const deliverChannel = (chan) => { if (openHandler) openHandler(chan, "channel"); else pendingChannel = chan; };
  window.agentnetOpen = deliverChannel; // device.mjs: a notification was clicked
  const skinsListeners = new Set();
  const go = (id, hash) => { const u = new URL(location.href); u.searchParams.set("skin", id); if (hash !== undefined) u.hash = hash; location.assign(u); };
  const common = {
    version: 1,
    onOpen(fn, kinds) {
      openHandler = typeof fn === "function" ? fn : null;
      openKinds = new Set(["channel", "conversation", ...(Array.isArray(kinds) ? kinds.filter((k) => k === "message" || k === "review") : [])]);
      if (openHandler && pendingChannel !== undefined) { const c = pendingChannel; pendingChannel = undefined; openHandler(c, "channel"); }
    },
    skins: [],
    onSkinsChange(fn) { skinsListeners.add(fn); return () => { skinsListeners.delete(fn); }; },
    selectSkin(id) {
      if (!common.skins.some((s) => s.id === id)) throw new Error("Unknown skin");
      go(id);
    },
  };
  // The browser-local package manager (local-skins.mjs): the same consent
  // and storage rules wherever a skin draws it.
  let localSkins = null;
  try { localSkins = await import("/assets/local-skins.mjs"); } catch (_) { /* a program without it: hosted packages still work */ }
  let offered = [];
  const refreshSkins = async () => {
    const local = localSkins ? await localSkins.catalog().catch(() => []) : [];
    // builtin is the host's word, set by mark from its own list, never a manifest's.
    // Every immutable workspace host keeps this SAME array reference.
    common.skins.splice(0, common.skins.length, ...offered.map(mark), ...local.filter((s) => s.local && !takenName(s.name)).map(mark));
    for (const fn of [...skinsListeners]) { try { fn(); } catch (_) { /* one listener's failure is its own */ } }
  };
  if (localSkins) common.manageLocalSkins = (root) => localSkins.manager(root, { changed: refreshSkins });

  // ---- workspaces: one shell, immutable bound transports
  //
  // The daemon's page lists its memberships from the program; a browser
  // device brings its own shell and enrollments (device.mjs). Without the
  // module there is one workspace on the single transport.
  let shell = null;
  const memberships = window.agentnetWorkspaces || null;
  try {
    const ws = await import("/assets/workspaces.mjs");
    if (memberships) shell = memberships.shell;
    else if (!browser) { shell = new ws.WorkspaceShell(); await shell.load(); }
  } catch (e) { shell = null; }
  if (shell && !shell.active) shell = null; // nothing enrolled: the single transport
  const entryOf = (id) => shell && shell.members.get(id);
  // Reconnect routes a membership disconnected here again: this computer's
  // program only (a browser enrollment has no program to ask).
  const canReconnect = !!shell && !memberships && typeof shell.reconnect === "function";
  const workspaces = shell ? Object.freeze({
    list: () => shell.list(),
    active: () => shell.active,
    has: (id) => !!(entryOf(id) && entryOf(id).connected), // known and connected here, by local registration only
    select: (id) => { if (!workspaces.has(id)) throw new Error("Unknown workspace"); switchTo(id); },
    state: (id) => shell.state(id), // this membership's own view state (drafts, open conversation), kept by the skin
    onChange: (fn) => shell.onChange(fn),
    join: (body) => (memberships ? memberships.join(body) : shell.join(body)),
    disconnect: (id) => (memberships ? memberships.disconnect(id) : shell.disconnect(id)),
    ...(canReconnect ? {
      disconnected: () => shell.disconnected(), // memberships disconnected here, with their state
      reconnect: (id) => shell.reconnect(id),
    } : {}),
  }) : null;
  const switchTo = (id) => { if (id !== shell.active) shell.select(id); };

  // hostFor binds one membership: the transport is that membership's own
  // and never changes; what the host adds (skins, notification routing,
  // the workspace list) is the same for every one. The identity a
  // membership proved in its overview is what host.reconnect checks.
  const known = new Map(); // membership id -> { address, fingerprint } from its own overview
  const canRebind = !!shell && !memberships && !browser && typeof shell.recoverNative === "function";
  let remount = null, rebinding = null;
  const hostFor = (id) => {
    const bound = shell ? shell.bind(id) : single;
    const wid = bound.workspace.id;
    const api = async (path, body) => {
      const r = await bound.api(path, body);
      if (body === undefined && path === "/api/overview" && r && r.me && r.me.fingerprint) known.set(wid, { address: r.me.address, fingerprint: r.me.fingerprint });
      return r;
    };
    const host = Object.freeze(Object.assign({}, bound, common, { api, workspaces }, canRebind && bound.platform === "daemon" ? { reconnect: () => rebind(host) } : {}));
    return host;
  };
  // rebind: this computer's program restarted and retired the membership's
  // handle. The host binds the same membership again, only when the
  // program still names it with the same endpoint, realm, address and key
  // it proved before, and mounts the skin again over the new binding.
  // Nothing under way is retargeted or replayed: the old host's staged
  // files and sends stay with the old binding.
  const rebind = (host) => {
    if (rebinding) return rebinding;
    rebinding = (async () => {
      const id = host.workspace.id;
      if (window.agentnet !== host || shell.active !== id) throw new Error("Workspace changed during reconnect");
      const before = entryOf(id);
      await shell.recoverNative(id, known.get(id));
      if (shell.active !== id) throw new Error("Workspace changed during reconnect");
      if (entryOf(id) === before) return; // the same binding still holds
      window.agentnet = hostFor(id);
      if (remount) await remount();
    })().finally(() => { rebinding = null; });
    return rebinding;
  };
  window.agentnet = hostFor(shell ? shell.active : "default"); // the host bound to the workspace shown now (the host's own pointer)
  if (shell) shell.onChange((e) => { window.agentnet = hostFor(e.id); }); // new operations take the new host; ones under way keep theirs

  // ---- the host's own pages and parts
  const text = (tag, value, cls) => { const e = document.createElement(tag); e.textContent = value; if (cls) e.className = cls; return e; };
  const button = (label, fn, cls) => { const b = text("button", label, "btn" + (cls ? " " + cls : "")); b.type = "button"; b.onclick = fn; return b; };
  const link = (href, into) => new Promise((resolve, reject) => {
    const l = document.createElement("link"); l.rel = "stylesheet"; l.href = href;
    l.onload = resolve; l.onerror = () => reject(new Error("Could not load " + href));
    into.append(l);
  });
  const card = (into, { title, lines, actions, mark, alert }) => {
    const gate = text("div", "", "skin-gate");
    const c = text("section", "", "skin-card");
    if (alert) c.setAttribute("role", "alert");
    const h = text("h1", title); h.id = "skin-gate-title"; c.setAttribute("aria-labelledby", h.id);
    if (mark) { const m = text("span", mark, "skin-card-mark"); m.setAttribute("aria-hidden", "true"); c.append(m); }
    c.append(h, ...lines);
    const row = text("div", "", "skin-card-actions"); row.append(...actions); c.append(row);
    gate.append(c);
    into.replaceChildren(gate);
    (actions[0] || h).focus();
  };
  // adoptDocument: a package's document rules (manifest "document"). The
  // page keeps only its @font-face and @property rules, with every URL
  // resolved inside the package; anything else in it is dropped. Browsers
  // ignore both kinds inside a shadow tree, so they apply at document level.
  const adoptDocument = async (href, base) => {
    const r = await fetch(href);
    if (!r.ok) throw new Error("Could not load the skin’s fonts");
    const parsed = new CSSStyleSheet();
    parsed.replaceSync(await r.text()); // never follows @import
    const kept = new CSSStyleSheet(), inside = new URL(base, location.href);
    for (const rule of parsed.cssRules) {
      if (typeof CSSPropertyRule !== "undefined" && rule instanceof CSSPropertyRule) kept.insertRule(rule.cssText, kept.cssRules.length);
      else if (rule instanceof CSSFontFaceRule) {
        let ok = true;
        const css = rule.cssText.replace(/url\(\s*(["']?)([^"')]*)\1\s*\)/g, (_, q, u) => {
          const abs = new URL(u, new URL(href, location.href));
          if (abs.origin !== inside.origin || !abs.pathname.startsWith(inside.pathname)) ok = false;
          return 'url("' + abs.href + '")';
        });
        if (ok) kept.insertRule(css, kept.cssRules.length);
      }
    }
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, kept];
  };

  let selected = null, surface = page, homeName = "Comic";
  try {
    const r = await fetch("/assets/skins/index.json");
    if (!r.ok) throw new Error("Could not load the list of skins");
    offered = (await r.json()).filter((s) => s && s.api === 1);
    await refreshSkins();
    // A choice saved before Comic was a package ("default", "classic")
    // opens Comic and is rewritten once; ?skin=default names Comic too.
    let saved = null, packageChoice = false;
    try { saved = localStorage.getItem("agentnet.skin"); packageChoice = localStorage.getItem("agentnet.skin.package") === saved; } catch (_) { /* local preference */ }
    const choice = choose(common.skins, { query: new URL(location.href).searchParams.get("skin"), saved, packageChoice });
    const home = choice.home;
    if (!home) throw new Error("This program has no Comic skin");
    homeName = home.name;
    if (choice.save) try { localStorage.setItem("agentnet.skin", choice.save); } catch (_) { /* local preference */ }
    selected = choice.selected;

    // The switcher over every skin but Comic: before trust is asked, so the
    // way back is there from the first moment.
    let bar = null;
    if (selected.id !== HOME) {
      const { mountSkinBar } = await import("/assets/skinbar.mjs");
      bar = mountSkinBar(document.body, {
        skins: () => common.skins, selected, home, choose: (id) => go(id), host: () => window.agentnet, workspaces,
        manage: common.manageLocalSkins || null,
      });
    }

    let trusted = isTrusted(selected, null);
    if (!trusted) try { trusted = isTrusted(selected, localStorage.getItem("agentnet.skin.trusted." + selected.id)); } catch (_) { /* ask */ }
    if (!trusted) await new Promise((resolve) => {
      const digest = text("p", "Fingerprint " + selected.digest.slice(0, 16) + "…" + selected.digest.slice(-8), "skin-card-digest");
      card(page, {
        title: "Use the skin “" + selected.name + "”?", mark: "✦",
        lines: [
          text("p", "This skin was made by someone else. It can read your chats and act as you, including sending messages and approving work. Use it only if you trust whoever made it."),
          text("p", (selected.local ? "It is stored in this browser" : "It is installed on this computer") + ". You are asked again whenever it changes."),
          digest,
        ],
        actions: [
          button("Use this skin", () => { try { localStorage.setItem("agentnet.skin.trusted." + selected.id, selected.digest); } catch (_) {} resolve(); }, "primary"),
          button("Use " + homeName, () => go(HOME)),
        ],
      });
    });
    if (selected.local) await localSkins.activate();
    const base = selected.local ? "/local-skins/" + selected.digest + "/" : "/assets/skins/" + selected.id + "/";
    if (selected.document) await adoptDocument(base + selected.document, base);
    page.replaceChildren();
    surface = page.attachShadow({ mode: "open" });
    // The host's base sheet (core.css, lowest layer), then the package's own.
    await link("/assets/skin-base.css", surface);
    if (selected.style) await link(base + selected.style, surface);
    const module = await import(base + selected.entry);
    if (typeof module.mount !== "function") throw new Error("This skin has no mount function");

    // mountSkin gives the skin a fresh root over the host of the workspace
    // shown now. On a workspace switch (or a rebind) it is mounted again:
    // unmount first, and what it held for the old membership goes out with
    // its root; operations it started keep the host they started with.
    let root = null, mounting = Promise.resolve();
    const mountSkin = async () => {
      openHandler = null; openKinds = new Set();
      if (root) { if (typeof module.unmount === "function") { try { await module.unmount(root); } catch (_) { /* replaced anyway */ } } root.remove(); }
      root = document.createElement("div");
      root.id = "skin";
      root.className = "skin-root";
      surface.append(root);
      await module.mount(root, window.agentnet);
      if (!openHandler) throw new Error("This skin must register notification handling with host.onOpen");
    };
    const failed = (e) => {
      const box = text("div", ""); box.className = "skin-root";
      if (root) root.replaceWith(box); else surface.append(box);
      root = box;
      card(box, { title: "Couldn’t open " + selected.name, lines: [text("p", e.message || "Loading failed.")], alert: true,
        actions: selected.id === HOME ? [button("Reload", () => location.reload(), "primary")] : [button("Use " + homeName, () => go(HOME), "primary"), button("Reload", () => location.reload())] });
    };
    remount = () => (mounting = mounting.then(mountSkin).catch(failed));
    await mountSkin();
    if (shell) shell.onChange(() => { remount(); });

    // A notification's destination: #conv=<64 hex>, #msg=<32 hex>[&conv=…][&dir=in|out]
    // or #review, each with an optional &workspace=<id>. The workspace must
    // be one registered here; an unknown one opens nothing (never the
    // current workspace instead).
    const notice = (words, action) => { if (bar) bar.notify(words, action ? { label: action, run: () => go(HOME, location.hash) } : null); };
    const clear = () => history.replaceState(null, "", location.pathname + location.search);
    const route = () => {
      const hash = location.hash || "";
      notice("");
      const review = hash === "#review" || hash.startsWith("#review&"), msg = hash.startsWith("#msg="), conv = hash.startsWith("#conv=");
      if (!review && !msg && !conv) return;
      const q = new URLSearchParams(hash.slice(1)), wid = q.get("workspace");
      let target = "", kind = "review", context;
      if (conv) {
        target = q.get("conv") || ""; kind = "conversation";
        if (!/^[0-9a-f]{64}$/.test(target)) { clear(); return; }
      } else if (msg) {
        target = q.get("msg") || ""; kind = "message";
        if (!/^[0-9a-f]{32}$/.test(target)) return;
        const c = q.get("conv"), d = q.get("dir");
        context = Object.freeze({ ...(c && /^[0-9a-f]{64}$/.test(c) ? { conv: c } : {}), ...(d === "in" || d === "out" ? { dir: d } : {}) });
      }
      if (wid !== null && (!workspaces || !workspaces.has(wid))) {
        if (conv) clear();
        else notice("A notification is for a workspace that isn’t on this device.");
        return;
      }
      if (!openKinds.has(kind)) { // never consumed until the person opens it
        notice(kind === "review" ? "Something is waiting for your decision." : "A notification is waiting for you.", "Open it in " + homeName);
        return;
      }
      clear();
      const deliver = () => { if (openHandler && openKinds.has(kind)) openHandler(target, kind, context); };
      if (wid !== null && wid !== shell.active) { const once = shell.onChange(() => { once(); mounting.then(deliver); }); switchTo(wid); return; }
      deliver();
    };
    window.addEventListener("hashchange", route);
    route();
    try { localStorage.setItem("agentnet.skin", selected.id); localStorage.setItem("agentnet.skin.package", selected.id); } catch (_) {}
  } catch (e) {
    const box = text("div", "");
    box.className = "skin-root";
    surface.replaceChildren(...(surface === page ? [] : [...surface.querySelectorAll("link")]), box);
    const name = selected ? selected.name : homeName;
    card(box, { title: "Couldn’t open " + name, lines: [text("p", e.message || "Loading failed.")], alert: true,
      actions: !selected || selected.id === HOME ? [button("Reload", () => location.reload(), "primary")] : [button("Use " + homeName, () => go(HOME), "primary"), button("Reload", () => location.reload())] });
  }
})();
