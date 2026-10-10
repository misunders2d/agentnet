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

  // ---- what the page waits for, and how long
  //
  // A stalled connection or proxy can hold one request without failing it.
  // The page never waits on such a request forever for something it can
  // open without: the list of skins (CATALOG_WAIT, from its request; Comic's
  // own manifest is then asked for from its fixed path, and whichever of the
  // two answers first opens the page) and a skin's fonts (DOCUMENT_WAIT; the
  // skin then shows in fallback fonts). Neither request is cancelled: what
  // arrives late is still used. A late answer is never a failure by itself.
  const CATALOG_WAIT = 1500, DOCUMENT_WAIT = 1500, LATE = Symbol("late");
  const within = (promise, ms) => {
    let timer;
    return Promise.race([promise, new Promise((resolve) => { timer = setTimeout(resolve, ms, LATE); })]).finally(() => clearTimeout(timer));
  };
  const fetchJSON = (path, what) => fetch(path).then(async (r) => {
    if (!r.ok) throw new Error("Could not load " + what);
    return r.json();
  });

  // ---- the page is what a phone's keyboard leaves visible
  //
  // Android shrinks the layout viewport with the keyboard (index.html:
  // interactive-widget=resizes-content), so the page and every skin follow
  // it by themselves. iOS ignores that and shrinks only the visual
  // viewport: there the host sizes the page from it (--an-viewport-h, read
  // by core.css) and says how much of the screen's bottom the keyboard
  // covers (--an-keyboard, for a skin's fixed bottom popups). Both are the
  // host's own names, set on <html> and inherited into the skin's shadow
  // tree, and absent while nothing covers the page or the person zooms in.
  // Events only, no polling.
  const fitViewport = (vv) => {
    const style = document.documentElement.style;
    const fit = () => {
      if (innerHeight - vv.height > 1 && Math.abs(vv.scale - 1) < 0.01) {
        style.setProperty("--an-viewport-h", vv.height + "px");
        style.setProperty("--an-keyboard", Math.max(0, innerHeight - vv.height - vv.offsetTop) + "px");
        if (scrollX || scrollY) scrollTo(0, 0); // iOS scrolled the page to show the field; it fits now, so back to its top
      } else {
        style.removeProperty("--an-viewport-h");
        style.removeProperty("--an-keyboard");
      }
    };
    vv.addEventListener("resize", fit);
    vv.addEventListener("scroll", fit);
    fit();
  };
  if (window.visualViewport) fitViewport(window.visualViewport);
  // The catalog is asked for here, alongside the host's own modules, and
  // after the viewport fitting: nothing that can fail comes before that.
  const catalogAsked = Date.now();
  const catalog = fetchJSON("/assets/skins/index.json", "the list of skins").then((list) => list.filter((s) => s && s.api === 1));
  catalog.catch(() => { /* seen where it is used */ });
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
  const nativeAppJSON = typeof window.__agentnetNativeAppJSON === "function" ? window.__agentnetNativeAppJSON : null;
  const appJSON = async (path, body) => {
    if (browser) throw new Error("Open the AgentNet app for this computer's settings.");
    if (nativeAppJSON) {
      const action = { "/api/app/status": "status", "/api/app/check": "check", "/api/app/update": "update", "/api/app/cli": "cli" }[path];
      if (!action) throw new Error("Unknown app action.");
      return nativeAppJSON(action, body);
    }
    const response = await fetch(path, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
    return response.json();
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
    ...(window.__agentnetPlatform === "android" ? { platform: "android", android: window.__agentnetAndroid, onBack: window.__agentnetOnBack } : {}),
    ...(typeof window.__agentnetNativeClipboardImage === "function" ? { clipboardImage: () => window.__agentnetNativeClipboardImage() } : {}),
    // Computer-wide app actions stay on this loopback origin; switching a
    // workspace never sends an updater request to a remote membership.
    appStatus: () => appJSON("/api/app/status"),
    appCheckUpdate: () => appJSON("/api/app/check"),
    appUpdate: () => appJSON("/api/app/update", {}),
    appReplaceCommand: () => appJSON("/api/app/cli", { replace: true }),
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
  // Rename sets this device's own label of a membership ("" clears it, and
  // the workspace's own name shows): wherever the shell can keep it.
  const canRename = !!shell && (!memberships || typeof shell.renameBrowser === "function");
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
    ...(canRename ? { rename: (id, name) => shell.rename(id, name) } : {}),
  }) : null;
  const switchTo = (id) => { if (id !== shell.active) shell.select(id); };

  // hostFor binds one membership: the transport is that membership's own
  // and never changes; what the host adds (skins, notification routing,
  // the workspace list) is the same for every one. The identity a
  // membership proved in its overview is what host.reconnect checks.
  const known = new Map(); // membership id -> { address, fingerprint } from its own overview
  // The skin bar's listeners: each overview a skin reads (it reads one on
  // its changes anyway) also tells the bar its workspace's name, so the bar
  // reads none per change of its own.
  const overviewSeen = new Set();
  const canRebind = !!shell && !memberships && !browser && typeof shell.recoverNative === "function";
  let remount = null, rebinding = null;
  const hostFor = (id) => {
    const bound = shell ? shell.bind(id) : single;
    const wid = bound.workspace.id;
    const api = async (path, body) => {
      const r = await bound.api(path, body);
      if (body === undefined && path === "/api/overview" && r) {
        if (r.me && r.me.fingerprint) known.set(wid, { address: r.me.address, fingerprint: r.me.fingerprint });
        for (const fn of [...overviewSeen]) { try { fn(wid, r); } catch (_) { /* the bar's own */ } }
      }
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

  let selected = null, surface = page, homeName = "Comic", fallback = false;
  try {
    const listed = await within(catalog, Math.max(0, catalogAsked + CATALOG_WAIT - Date.now())).catch(() => LATE);
    if (listed !== LATE) offered = listed;
    else {
      // No list of skins yet (late or failed): Comic's own manifest too,
      // from its fixed path, and the page opens with whichever answers
      // first (the list, when both have). Lateness alone never fails the
      // page: a slow link opens it later, as always. Comic opened from its
      // manifest is trusted by the host's list as always; the person's saved
      // choice is kept for the next load, and the list fills in when it comes.
      const own = fetchJSON("/assets/skins/" + HOME + "/skin.json", "Comic").then((m) => {
        if (!m || m.api !== 1 || m.id !== HOME) throw new Error("This program has no Comic skin");
        return [m];
      });
      own.catch(() => { /* seen by Promise.any, or the list answered */ });
      // Both failed: the list's own failure says why, as before.
      const first = await Promise.any([catalog.then((list) => ({ list })), own.then((list) => ({ list, own: true }))]).catch((e) => { throw e.errors[0]; });
      offered = first.list;
      if (first.own) {
        fallback = true;
        catalog.then((list) => { offered = list; return refreshSkins(); }).catch(() => { /* Comic only, until the next load */ });
      }
    }
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
        manage: common.manageLocalSkins || null, overviews: (fn) => { overviewSeen.add(fn); return () => overviewSeen.delete(fn); },
      });
      await bar.ready; // No unstyled host controls before the skin’s first paint.
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
    if (selected.document) {
      // A failure seen before the skin mounts stops it, as always; the rules
      // arriving after DOCUMENT_WAIT are adopted then, and a late failure
      // leaves the fallback fonts.
      const rules = adoptDocument(base + selected.document, base);
      rules.catch(() => { /* seen by within, or the fallback fonts stay */ });
      await within(rules, DOCUMENT_WAIT);
    }
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
    // An invitation or device link opened in the AgentNet app once this
    // computer has joined (agentnet://open#…): taken out of the address at
    // once (it is a secret), never acted on. The same server needs no
    // second person; another server is joined from the workspace menu.
    // TODO(integrate:P2): open P2's workspace join sheet with it, the
    // server's host shown and a click required.
    const invitation = () => {
      const hash = location.hash || "";
      if (browser || !/^#agentnet-(invite-v1|link-v2):/.test(hash)) return false;
      clear();
      let host = "";
      try {
        const code = decodeURIComponent(hash.slice(1));
        const raw = code.startsWith("agentnet-link-v2:") ? null : JSON.parse(atob(code.slice(code.indexOf(":") + 1).replace(/-/g, "+").replace(/_/g, "/")));
        host = raw && typeof raw.hub === "string" ? new URL(raw.hub).host : "";
      } catch (_) { host = ""; }
      notice(hash.startsWith("#agentnet-link-v2:") ? "A device link opens on a new device: this computer is already one of yours."
        : "This computer has joined AgentNet already. To join " + (host ? "the server " + host : "another server") + " with that invitation, use Join a workspace in the workspace menu.");
      return true;
    };
    const route = () => {
      const hash = location.hash || "";
      notice("");
      if (invitation()) return;
      const review = hash === "#review" || hash.startsWith("#review&"), msg = hash.startsWith("#msg="), conv = hash.startsWith("#conv=");
      const chats = /^#workspace=[^&]+$/.test(hash);
      if (!review && !msg && !conv && !chats) return;
      const q = new URLSearchParams(hash.slice(1)), wid = q.get("workspace");
      let target = "", kind = chats ? "channel" : "review", context;
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
    if (!fallback) try { localStorage.setItem("agentnet.skin", selected.id); localStorage.setItem("agentnet.skin.package", selected.id); } catch (_) {}
  } catch (e) {
    const box = text("div", "");
    box.className = "skin-root";
    surface.replaceChildren(...(surface === page ? [] : [...surface.querySelectorAll("link")]), box);
    const name = selected ? selected.name : homeName;
    card(box, { title: "Couldn’t open " + name, lines: [text("p", e.message || "Loading failed.")], alert: true,
      actions: !selected || selected.id === HOME ? [button("Reload", () => location.reload(), "primary")] : [button("Use " + homeName, () => go(HOME), "primary"), button("Reload", () => location.reload())] });
  }
})();
