// The skin switcher: the host's own bar above every skin but Comic (which
// has its own Settings → Appearance → Skin). It is one step back to Comic,
// the one skin menu (Comic, then installed skins and those stored in this
// browser, with import and removal), and the workspace control (switch,
// join, leave, reconnect). It sits in the page's flow above the skin (never
// over it), in a shadow tree of its own (skinbar.css), so no skin
// stylesheet can hide or restyle it. Text from anywhere is inserted as text
// nodes only.
//
// mountSkinBar(parent, options) draws it and returns { notify, element }:
//   skins()          the catalog (host.skins), read each time the menu opens
//   selected         the skin shown now ({ id, name, local?, builtin? })
//   home             the default skin ({ id, name }): Comic
//   choose(id, hash) reloads into that skin (hash: a destination to keep)
//   host()           the host bound to the workspace shown now
//   workspaces       the host's workspace list, or null without one
//   manage(root)     mounts the browser-local skin manager; returns its teardown (or null)
const svg = "http://www.w3.org/2000/svg";
const paths = {
  back: ["M19 12H5", "M11 18l-6-6 6-6"],
  chevron: ["M6 9l6 6 6-6"],
  check: ["M5 12l5 5L20 7"],
  plus: ["M12 5v14", "M5 12h14"],
  leave: ["M14 8V6a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h7a2 2 0 0 0 2-2v-2", "M9 12h12", "M18 9l3 3-3 3"],
  plug: ["M7 12l5 5", "M17 12l-5-5", "M7 12l-2 2a3 3 0 0 0 4 4l2-2", "M17 12l2-2a3 3 0 0 0-4-4l-2 2", "M3 21l2.5-2.5", "M18.5 5.5L21 3"],
  brush: ["M3 21v-4a4 4 0 1 1 4 4h-4", "M21 3a16 16 0 0 0-12.8 10.2", "M21 3a16 16 0 0 1-10.2 12.8", "M10.6 9a9 9 0 0 1 4.4 4.4"],
  box: ["M12 3l8 4.5v9L12 21l-8-4.5v-9z", "M12 12l8-4.5", "M12 12v9", "M12 12L4 7.5"],
};
const icon = (name, size = 18) => {
  const s = document.createElementNS(svg, "svg");
  for (const [k, v] of [["viewBox", "0 0 24 24"], ["width", size], ["height", size], ["aria-hidden", "true"], ["class", "icon"]]) s.setAttribute(k, v);
  for (const d of paths[name]) { const p = document.createElementNS(svg, "path"); p.setAttribute("d", d); s.append(p); }
  return s;
};

const generic = new Set(["", "current workspace", "this computer"]);
const serverName = (endpoint) => {
  try { const h = new URL(endpoint).hostname.replace(/^www\./, ""); return !h || h === "localhost" || h.startsWith("[") || /^[\d.]+$/.test(h) ? "" : h; } catch (_) { return ""; }
};
// The same words as Comic's workspace menu (WorkspaceSwitcher.tsx).
export const workspaceLabel = (w) => { const n = (w.name || "").trim(); return generic.has(n.toLowerCase()) ? serverName(w.endpoint) || "AgentNet" : n; };
export const coinLetters = (label) => {
  const base = label.includes(".") && !label.includes(" ") ? label.split(".").slice(-2, -1)[0] || label : label;
  const words = base.replace(/(\p{Ll})(\p{Lu})/gu, "$1 $2").split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  return words.length ? (words[0][0] + (words[1] ? words[1][0] : "")).toUpperCase() : "A";
};

export function mountSkinBar(parent, { skins, selected, home, choose, host, workspaces, manage }) {
  const holder = document.createElement("div");
  holder.id = "skin-bar";
  const root = holder.attachShadow({ mode: "open" });
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; };
  const css = el("link"); css.rel = "stylesheet"; css.href = "/assets/skinbar.css";

  // Menus and dialogs follow the device's light or dark setting.
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const scheme = () => { holder.dataset.scheme = media.matches ? "dark" : "light"; };
  scheme();
  media.addEventListener("change", scheme);

  const bar = el("nav", "bar"); bar.setAttribute("aria-label", "Skin and workspace");
  const homeButton = el("button", "home"); homeButton.type = "button";
  homeButton.setAttribute("aria-label", "Switch to " + home.name);
  homeButton.title = "Switch to " + home.name + ", AgentNet’s own skin";
  homeButton.append(icon("back", 18), el("span", "", home.name));
  homeButton.onclick = () => pickSkin(home.id);

  // ---- notices: a notification waiting, or what an action did
  const notice = el("div", "notice"); notice.hidden = true; notice.setAttribute("role", "status");
  const say = (text, action) => {
    notice.replaceChildren(); notice.hidden = !text;
    if (!text) return;
    notice.append(el("span", "text", text));
    if (action) { const b = el("button", "", action.label); b.type = "button"; b.onclick = action.run; notice.append(b); }
    const close = el("button", "close", "×"); close.type = "button"; close.setAttribute("aria-label", "Dismiss"); close.onclick = () => say("");
    notice.append(close);
  };

  // ---- menus: one open at a time; arrows move, Escape closes
  let openMenu = null;
  const menus = [];
  const makeMenu = (button, label, fill) => {
    const panel = el("div", "menu"); panel.hidden = true; panel.setAttribute("role", "menu"); panel.setAttribute("aria-label", label);
    panel.id = "menu-" + menus.length;
    button.setAttribute("aria-haspopup", "menu"); button.setAttribute("aria-expanded", "false"); button.setAttribute("aria-controls", panel.id);
    const items = () => [...panel.querySelectorAll("[role^=menuitem]:not(:disabled)")];
    const m = {
      button, panel,
      open() {
        if (openMenu && openMenu !== m) openMenu.close(false);
        openMenu = m; panel.hidden = false; button.setAttribute("aria-expanded", "true");
        fill(panel);
        const list = items();
        (list.find((i) => i.getAttribute("aria-checked") === "true") || list[0] || panel).focus();
      },
      close(focus = true) {
        if (panel.hidden) return;
        panel.hidden = true; button.setAttribute("aria-expanded", "false");
        if (openMenu === m) openMenu = null;
        if (focus) button.focus();
      },
    };
    panel.tabIndex = -1;
    button.onclick = () => (panel.hidden ? m.open() : m.close());
    button.addEventListener("keydown", (e) => { if ((e.key === "ArrowDown" || e.key === "ArrowUp") && panel.hidden) { e.preventDefault(); m.open(); } });
    panel.addEventListener("keydown", (e) => {
      const list = items(), i = list.indexOf(root.activeElement);
      const go = (n) => { e.preventDefault(); if (list.length) list[(n + list.length) % list.length].focus(); };
      if (e.key === "ArrowDown") go(i + 1);
      else if (e.key === "ArrowUp") go(i < 0 ? -1 : i - 1);
      else if (e.key === "Home") go(0);
      else if (e.key === "End") go(-1);
      else if (e.key === "Escape") { e.preventDefault(); m.close(); }
      else if (e.key === "Tab") m.close(false);
    });
    menus.push(m);
    return m;
  };
  document.addEventListener("pointerdown", (e) => {
    if (!openMenu) return;
    const path = e.composedPath();
    if (!path.includes(openMenu.panel) && !path.includes(openMenu.button)) openMenu.close(false);
  });
  const item = (role, glyph, name, line, run, extra = {}) => {
    const b = el("button", "item" + (extra.cls ? " " + extra.cls : "")); b.type = "button"; b.setAttribute("role", role); b.tabIndex = -1;
    if (role === "menuitemradio") b.setAttribute("aria-checked", String(!!extra.checked));
    const words = el("span", "words"); words.append(el("span", "name", name)); if (line) words.append(el("span", "line", line));
    b.append(glyph, words);
    if (extra.checked) { const t = icon("check", 20); t.classList.add("tick"); b.append(t); }
    if (extra.disabled) b.disabled = true;
    b.onclick = run;
    return b;
  };
  const glyph = (child, cls = "") => { const g = el("span", "glyph" + (cls ? " " + cls : "")); g.setAttribute("aria-hidden", "true"); if (typeof child === "string") g.textContent = child; else g.append(child); return g; };
  const group = (label, children) => { const g = el("div"); g.setAttribute("role", "group"); g.setAttribute("aria-label", label); g.append(...children); return [el("p", "group", label), g]; };

  // ---- dialogs (modal, in this tree)
  const dialog = el("dialog");
  let teardown = null;
  // A dialog that follows another (Join, then "You joined") reopens before
  // the first one's close event arrives: that event then changes nothing.
  dialog.addEventListener("close", () => {
    if (dialog.open) return;
    if (teardown) { teardown(); teardown = null; }
    dialog.replaceChildren(); if (dialog.returnTo) dialog.returnTo.focus(); dialog.returnTo = null;
  });
  const showDialog = (title, build, returnTo) => {
    if (dialog.open) dialog.close();
    const form = el("form"); form.method = "dialog";
    const h = el("h2", "", title); h.id = "dialog-title"; dialog.setAttribute("aria-labelledby", h.id);
    form.append(h);
    build(form);
    dialog.replaceChildren(form);
    dialog.returnTo = returnTo || null;
    dialog.showModal();
    const first = form.querySelector("input, textarea, .btn.act, .btn");
    if (first) first.focus();
    return form;
  };
  const btn = (text, cls, run) => { const b = el("button", "btn" + (cls ? " " + cls : ""), text); b.type = "button"; if (run) b.onclick = run; return b; };
  const actions = (...buttons) => { const a = el("div", "actions"); a.append(...buttons); return a; };

  // ---- skins
  const pickSkin = (id) => { if (openMenu) openMenu.close(false); if (id !== selected.id) choose(id); };
  const skinButton = el("button", "pick skin"); skinButton.type = "button";
  skinButton.setAttribute("aria-label", "Skin: " + selected.name + ". Choose another skin");
  const swatch = el("span", "swatch"); swatch.setAttribute("aria-hidden", "true"); swatch.append(icon("brush", 16));
  skinButton.append(swatch, el("span", "key", "Skin"), el("span", "value", selected.name), (() => { const c = icon("chevron", 16); c.classList.add("chev"); return c; })());
  const lineOf = (s) => (s.id === selected.id ? "In use" : s.id === home.id ? "AgentNet’s own skin" : s.builtin ? "Built into AgentNet" : s.local ? "Stored in this browser" : "Installed on this computer");
  const skinMenu = makeMenu(skinButton, "Skins", (panel) => {
    const all = skins();
    const radio = (s, g) => item("menuitemradio", g, s.name, lineOf(s), () => pickSkin(s.id), { checked: s.id === selected.id });
    const own = all.filter((s) => s.id === home.id || s.builtin), others = all.filter((s) => s.id !== home.id && !s.builtin);
    panel.replaceChildren(el("h2", "", "Skins"), el("p", "lead", "The same chats and settings, a different look."),
      ...group("Built in", own.map((s) => radio(s, glyph(s.id === home.id ? "Co" : icon("box", 20), s.id === home.id ? "comic" : "")))),
      ...(others.length ? group("From other people", others.map((s) => radio(s, glyph(icon("brush", 20))))) : []),
      ...(manage ? [el("hr", "sep"), item("menuitem", glyph(icon("plus", 20), "dashed"), "Import or remove skins…", "Skins stored in this browser", () => { skinMenu.close(false); managerDialog(); })] : []));
  });
  const managerDialog = () => {
    showDialog("Import or remove skins", (f) => {
      const box = el("div", "manager");
      teardown = manage(box);
      f.append(box, actions(btn("Done", "act", () => dialog.close())));
    }, skinButton);
  };
  const skinAnchor = el("div", "anchor"); skinAnchor.append(skinButton, skinMenu.panel);
  bar.append(homeButton, el("span", "divider"), skinAnchor);

  // ---- workspaces
  if (workspaces) {
    const wsButton = el("button", "pick ws"); wsButton.type = "button";
    const coin = el("span", "ws-coin"); coin.setAttribute("aria-hidden", "true");
    const wsValue = el("span", "value");
    const chev = icon("chevron", 16); chev.classList.add("chev");
    wsButton.append(coin, el("span", "key", "Workspace"), wsValue, chev);
    const current = () => workspaces.list().find((w) => w.id === workspaces.active()) || host().workspace;
    const label = () => {
      const name = workspaceLabel(current());
      coin.textContent = coinLetters(name); wsValue.textContent = name;
      wsButton.setAttribute("aria-label", "Workspace: " + name + ". Switch or join another");
    };
    label();
    workspaces.onChange(() => { label(); if (openMenu === wsMenu) wsMenu.close(false); });
    const deviceName = () => { const a = (host().workspace && host().workspace.address) || ""; return a.includes("/") ? a.split("/").pop() : ""; };
    const wsMenu = makeMenu(wsButton, "Workspaces", (panel) => {
      const active = workspaces.active();
      const who = el("p", "lead", "Each one is a separate server with its own people and chats.");
      const rows = workspaces.list().map((w) => {
        const here = w.id === active, connected = workspaces.has(w.id), name = workspaceLabel(w), server = serverName(w.endpoint);
        const line = here ? "You’re here" : w.state === "joining" ? "Still joining" : !connected || w.state === "disconnected" ? "Not connected" : server && server !== name ? server : "Switch to it";
        return item("menuitemradio", glyph(coinLetters(name), "coin"), name, line, () => {
          wsMenu.close(); if (here) return;
          try { workspaces.select(w.id); } catch (e) { say(e.message); }
        }, { checked: here, disabled: !here && !connected });
      });
      const gone = el("div");
      const tail = [];
      if (workspaces.join) tail.push(item("menuitem", glyph(icon("plus", 20), "dashed"), "Join a workspace…", "With an invitation from someone there", () => { wsMenu.close(false); joinDialog(); }));
      const cur = workspaces.list().find((w) => w.id === active);
      if (workspaces.disconnect && cur && cur.id !== "default") tail.push(item("menuitem", glyph(icon("leave", 20)), "Leave " + workspaceLabel(cur) + "…", "Stop getting its messages on this device", () => { wsMenu.close(false); leaveDialog(cur); }, { cls: "danger" }));
      panel.replaceChildren(el("h2", "", "Workspaces"), who, ...group("Yours", rows), gone, ...(tail.length ? [el("hr", "sep"), ...tail] : []));
      // Who you are here, and the workspaces this computer left: read now, shown when they come.
      host().api("/api/overview").then((o) => { if (!panel.hidden && o && o.me) who.textContent = "You are " + o.me.address + " in " + workspaceLabel(current()) + "."; }).catch(() => {});
      if (workspaces.disconnected) workspaces.disconnected().then((list) => {
        if (panel.hidden || !list || !list.length) return;
        gone.replaceChildren(...group("Not connected", list.map((w) => {
          const name = workspaceLabel(w);
          return item("menuitem", glyph(icon("plug", 20), "dashed"), "Reconnect " + name, (serverName(w.endpoint) || "Its chats and keys are still here"), async (e) => {
            const b = e.currentTarget; b.disabled = true; b.querySelector(".line").textContent = "Reconnecting…";
            try { await workspaces.reconnect(w.id); wsMenu.close(); say("Reconnected " + name + ". Its chats and keys are back; choose it in Workspaces to work there."); }
            catch (err) { b.disabled = false; b.querySelector(".line").textContent = "Couldn’t reconnect: " + err.message; }
          });
        })));
      }).catch((e) => { if (!panel.hidden) gone.replaceChildren(el("p", "error", "Couldn’t list the workspaces you left: " + e.message)); });
    });
    const joinDialog = () => {
      let retryID = "";
      showDialog("Join a workspace", (f) => {
        f.append(el("p", "muted", "Use the invitation someone on that server gave you."));
        const field = (tag, text, attrs, hint) => { const l = el("label", "", text); const i = el(tag); Object.assign(i, attrs); l.append(i); if (hint) l.append(el("small", "", hint)); return [l, i]; };
        const [invL, invite] = field("textarea", "Invitation", { rows: 3, required: true, spellcheck: false, autocomplete: "off", placeholder: "Paste the invitation or its link" });
        const [nameL, name] = field("input", "What you call it", { maxLength: 48, required: true, placeholder: "For example, Linen HQ" });
        const [devL, dev] = field("input", "This device’s name there", { maxLength: 32, required: true, autocomplete: "off", spellcheck: false, value: deviceName(), placeholder: "laptop" }, "Others there see it next to your name.");
        dev.setAttribute("autocapitalize", "none"); invite.setAttribute("autocapitalize", "none");
        const err = el("p", "error"); err.setAttribute("role", "alert");
        const go = btn("Join", "act"); go.type = "submit";
        f.onsubmit = async (e) => {
          e.preventDefault();
          const token = (invite.value.match(/agentnet-invite-v1:[^\s#&]+/) || [invite.value.trim()])[0];
          const body = { name: name.value.trim(), invite: token, agent: dev.value.trim(), ...(retryID ? { id: retryID } : {}) };
          if (!body.invite || !body.name || !body.agent) { err.textContent = "Paste the invitation, give the workspace a name, and name this device."; return; }
          go.disabled = true; go.textContent = "Joining…"; err.textContent = "";
          try {
            const h = await workspaces.join(body);
            invite.value = ""; // an invitation works once; it is not kept on the page
            const joined = h.workspace, label = workspaceLabel(joined);
            showDialog("You joined " + label, (g) => {
              const done = el("p", "done"); done.append(icon("check", 20), el("span", "", label + " has its own people and chats. Switch between workspaces any time from this menu."));
              g.append(done, actions(btn("Go to " + label, "act", () => { dialog.close(); try { workspaces.select(joined.id); } catch (x) { say(x.message); } }), btn("Stay here", "", () => dialog.close())));
            }, wsButton);
          } catch (x) {
            if (x.retryID) retryID = x.retryID;
            err.textContent = x.message + (retryID ? " Paste a new invitation and press Join again: it continues this same join." : "");
            go.disabled = false; go.textContent = "Join";
          }
        };
        f.append(invL, nameL, devL, err, actions(go, btn("Cancel", "", () => dialog.close())));
      }, wsButton);
    };
    const leaveDialog = (w) => {
      const name = workspaceLabel(w);
      showDialog("Leave " + name + "?", (f) => {
        const err = el("p", "error"); err.setAttribute("role", "alert");
        const go = btn("Leave " + name, "danger");
        go.onclick = async () => {
          go.disabled = true; go.textContent = "Leaving…";
          try { await workspaces.disconnect(w.id); dialog.close(); say("You left " + name + ". Its chats and keys stay on this device."); }
          catch (e) { go.disabled = false; go.textContent = "Leave " + name; err.textContent = e.message; }
        };
        f.append(el("p", "", "This device stops getting messages from " + name + "."), el("p", "", "Your chats and keys for it stay on this device, and nothing is removed on that server."),
          el("p", "muted", workspaces.reconnect ? "To come back, reconnect it from this menu." : "To come back later, you need a new invitation."), err, actions(go, btn("Stay", "", () => dialog.close())));
      }, wsButton);
    };
    const wsAnchor = el("div", "anchor end"); wsAnchor.append(wsButton, wsMenu.panel);
    bar.append(wsAnchor);
  }

  root.append(css, bar, notice, dialog);
  parent.prepend(holder);
  return {
    // notify shows a notification the skin cannot open, with the way to
    // open it (action: { label, run }), or clears it ("").
    notify(text, action) { say(text, action); },
    element: holder,
  };
}
