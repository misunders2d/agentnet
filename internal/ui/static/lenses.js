// Comic and Zoom: two more ways to look at the same threads and DMs. They
// read the same data as the classic view and every decision goes through the
// same dialogs and server actions (decide, actionButton in app.js). Faces are
// initials only. In device history whether a person or one of their agents
// wrote a message is not recorded; in a DM it is what the sending AgentNet
// says, shown as that. Nothing here links a person to an agent: that waits
// for real participation data.
"use strict";

const motion = () => !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
const stampWord = { question: "QUESTION", task: "TASK", answer: "ANSWER", result: "RESULT" };
const needsYou = (m) => !isReport(m) && (m.actions || []).some((a) => decisionActions.includes(a));
const working = (m) => (m.actions || []).includes("cancel");
const authorName = (m) => m.author || (m.dir === "in" ? m.from : "You (this computer)");
const faceOf = (m) => m.face || (m.dir === "out" && state.overview ? state.overview.me.address : m.from);

// comicDM is a DM as the comic draws it: the person's name as they give it,
// no decisions (nothing in a DM runs), held and kept states as captions.
function comicDM(d) {
  const me = state.overview && state.overview.person ? state.overview.person.label : "You";
  return { id: "dm:" + d.id, dm: true, peer: d.peer.label, via: d.peer.address,
    messages: d.messages.map((m) => Object.assign({}, m, { dm: true, actions: m.actions || [], author: dmAuthor(m, d),
      face: m.dir === "out" ? me : d.peer.label || m.from, to: d.peer.label })) };
}

// Full text and details of one message, in a dialog.
function readDialog(m) {
  const d = m.dm ? dmDetails(m) : details(m);
  d.open = true;
  dialog({ title: (kindTag[m.kind] || "Message") + " · " + authorName(m), ok: "Close", run: async () => {},
    body: [el("div", { class: "quote" }, m.body), m.summary && el("p", { class: "hint" }, "Your responder's summary: " + m.summary), d] });
}

// Writing without the composer (Zoom): an answer to m, or a new message in
// thread t linked to its latest message.
function writeDialog(t, m) {
  const body = el("textarea", { id: "write-body", rows: "4" });
  const kind = el("select", { id: "write-kind" }, ["message", "question", "task"].map((k) => el("option", { value: k }, k[0].toUpperCase() + k.slice(1))));
  const last = t.messages[t.messages.length - 1];
  dialog({
    title: m ? "Answer " + m.from : "Write to " + t.peer,
    body: [m && el("div", { class: "quote" }, m.body),
      el("label", { for: "write-body", class: "field-label" }, m ? "Your answer" : "Message"), body,
      !m && [el("label", { for: "write-kind", class: "field-label" }, "Send as"), kind],
      el("p", { class: "hint" }, m ? "Your answer takes this " + m.kind + " over from your responder." : "It joins this conversation.")],
    ok: "Send", focus: body,
    run: async () => {
      if (m) await act({ do: "reply", id: m.id, body: body.value });
      else await api("/api/send", { to: t.peer, kind: kind.value, body: body.value, reply_to: last ? last.id : "" });
    },
  });
  state.dialogRestore = { type: "write", msg: m ? m.id : null };
}

// dmWriteDialog writes a message in DM d without the composer (Zoom).
function dmWriteDialog(d) {
  const body = el("textarea", { id: "write-body", rows: "4" });
  dialog({
    title: "Write to " + d.peer.label,
    body: [el("label", { for: "write-body", class: "field-label" }, "Message"), body,
      el("p", { class: "hint" }, "It goes to this DM only. A DM is for the person; nothing runs it.")],
    ok: "Send", focus: body,
    run: async () => {
      const r = await api("/api/dm/send", { conv: d.id, body: body.value });
      announce(r.state === "waiting" ? "Kept here, not sent yet: " + (r.detail || "they cannot read conversations now.") : "Sent.");
    },
  });
  state.dialogRestore = { type: "dmwrite" };
}

// ---- Comic: a thread is an issue, its messages are panels ----------------------

const Comic = {
  pageOf: {}, // page shown per thread
  grid: false,

  pages(t) {
    const pages = [{ cover: true }];
    let page = [], slots = 0;
    for (const m of t.messages) {
      const w = needsYou(m) || m.body.length > 160 || m.summary ? 2 : 1;
      if (slots + w > 4 && page.length) { pages.push({ panels: page }); page = []; slots = 0; }
      page.push(m);
      slots += w;
    }
    if (page.length) pages.push({ panels: page });
    return pages;
  },

  // render draws thread t; a new message moves a reader on the last page on.
  render(t) {
    const root = $("comic");
    const pages = this.pages(t);
    const prev = this.shown;
    let idx = this.pageOf[t.id];
    if (idx === undefined) {
      const needs = pages.findIndex((p) => p.panels && p.panels.some(needsYou));
      idx = needs > 0 ? needs : pages.length - 1;
    } else if (prev && prev.id === t.id && idx === prev.count - 1) {
      idx = pages.length - 1;
    }
    idx = Math.min(idx, pages.length - 1);
    this.pageOf[t.id] = idx;
    this.shown = { id: t.id, count: pages.length };
    this.t = t;
    this.list = pages;
    const first = this.page(pages[idx], idx);
    first.classList.add("current");
    const book = el("div", { class: "book" }, first);
    this.book = book;
    fill(root,
      el("div", { class: "comic-bar" },
        el("button", { type: "button", class: "chip", onclick: () => this.toggleGrid() }, this.grid ? "Back to reading" : "All pages"),
        el("span", { class: "page-count", "aria-live": "polite" })),
      this.grid ? this.gridView() : el("div", { class: "reader" },
        el("button", { type: "button", class: "turn-btn prev", "aria-label": "Previous page", onclick: () => this.turn(-1) }, "‹"),
        book,
        el("button", { type: "button", class: "turn-btn next", "aria-label": "Next page", onclick: () => this.turn(1) }, "›")));
    this.counter();
    let sx = null;
    book.addEventListener("pointerdown", (e) => { sx = e.clientX; });
    book.addEventListener("pointerup", (e) => {
      if (sx === null) return;
      const dx = e.clientX - sx;
      sx = null;
      if (Math.abs(dx) > 60) this.turn(dx < 0 ? 1 : -1);
    });
  },

  counter() {
    const idx = this.pageOf[this.t.id], n = this.list.length - 1;
    $("comic").querySelector(".page-count").textContent = idx === 0 ? "Cover" : "Page " + idx + " of " + n;
    const prev = $("comic").querySelector(".prev"), next = $("comic").querySelector(".next");
    if (prev) prev.disabled = idx === 0;
    if (next) next.disabled = idx === n;
  },

  turn(dir, to) {
    const idx = this.pageOf[this.t.id];
    const target = to ?? idx + dir;
    if (target < 0 || target >= this.list.length || target === idx) return;
    this.pageOf[this.t.id] = target;
    const fresh = this.page(this.list[target], target);
    const old = this.book.querySelector(".page.current");
    fresh.classList.add("current");
    if (!old || !motion()) {
      if (old) old.remove();
      this.book.append(fresh);
    } else if (target > idx) {
      old.classList.remove("current");
      this.book.insertBefore(fresh, old);
      old.classList.add("leaf", "leaf-out");
      old.addEventListener("animationend", () => old.remove(), { once: true });
    } else {
      old.classList.remove("current");
      this.book.append(fresh);
      fresh.classList.add("leaf", "leaf-in");
      fresh.addEventListener("animationend", () => { fresh.classList.remove("leaf", "leaf-in"); old.remove(); }, { once: true });
    }
    this.counter();
  },

  // focus turns to the page holding message id and focuses its first action.
  focus(id) {
    const at = this.list.findIndex((p) => p.panels && p.panels.some((m) => m.id === id));
    if (at < 0) return;
    if (this.grid) { this.grid = false; this.pageOf[this.t.id] = at; this.render(this.t); }
    else this.turn(0, at);
    const panel = document.getElementById("p-" + id);
    if (panel) (panel.querySelector(".acts button") || panel.querySelector(".read-btn")).focus();
  },

  onKey(e) {
    if (e.key === "ArrowRight") { this.turn(1); return true; }
    if (e.key === "ArrowLeft") { this.turn(-1); return true; }
    return false;
  },

  toggleGrid() {
    this.grid = !this.grid;
    this.render(this.t);
    $("comic").querySelector(".comic-bar .chip").focus();
  },

  gridView() {
    return el("div", { class: "page-grid" }, this.list.map((p, i) => el("button", {
      type: "button", class: "thumb" + (i === this.pageOf[this.t.id] ? " current-thumb" : ""),
      "aria-label": i === 0 ? "Cover" : "Page " + i,
      onclick: () => { this.grid = false; this.pageOf[this.t.id] = i; this.render(this.t); },
    }, el("span", { class: "thumb-inner", "aria-hidden": "true", inert: true }, this.page(p, i)),
    el("span", { class: "thumb-label" }, i === 0 ? "Cover" : "Page " + i))));
  },

  page(p, idx) {
    return p.cover ? this.cover() : el("section", { class: "page" },
      el("header", { class: "page-head" }, el("span", { class: "scene-no" }, "Page " + idx),
        el("h2", {}, firstLine(this.t.messages[0].body, 60))),
      el("div", { class: "panels" }, this.widths(p.panels).map((wide, i) => this.panel(p.panels[i], wide))));
  },

  // widths makes a panel full width when it holds a lot or needs a decision,
  // and a half-width panel left alone in its row full width too.
  widths(panels) {
    const wide = panels.map((m) => needsYou(m) || m.body.length > 160 || !!m.summary);
    let run = 0;
    for (let i = 0; i <= wide.length; i++) {
      if (i < wide.length && !wide[i]) { run++; continue; }
      if (run % 2 === 1) wide[i - 1] = true;
      run = 0;
    }
    return wide;
  },

  cover() {
    const t = this.t, first = t.messages[0];
    const me = t.dm ? (state.overview && state.overview.person ? state.overview.person.label : "You")
      : state.overview ? state.overview.me.address : "";
    const open = t.messages.filter(needsYou).length;
    const held = t.messages.filter((m) => m.state === "conv_held").length;
    return el("section", { class: "page cover" },
      el("div", { class: "cover-top" }, el("span", {}, t.dm ? "DM · " + t.peer + " via " + t.via : "AgentNet · " + t.peer),
        first && el("span", {}, when(first.at))),
      el("h2", { class: "cover-title" }, first ? firstLine(first.body, 56) : "A DM with " + t.peer + ", no messages yet"),
      el("div", { class: "cast" },
        el("figure", {}, avatar(t.peer, "actor"), el("figcaption", {}, t.peer)),
        me && el("figure", {}, avatar(me, "actor"), el("figcaption", {}, "You"))),
      el("p", { class: "cover-sub" }, t.messages.length === 1 ? "1 message" : t.messages.length + " messages"),
      open ? el("div", { class: "burst" }, open === 1 ? "1 decision inside!" : open + " decisions inside!") : null,
      held ? el("p", { class: "cover-sub" }, held === 1 ? "1 held for you: nothing runs it" : held + " held for you: nothing runs them") : null,
      first && el("button", { type: "button", class: "btn comic-btn", onclick: () => this.turn(1) }, "Start reading"));
  },

  panel(m, wide) {
    if (m.event) { // a participation record: the narrator says it, nobody speaks it
      return el("article", { id: "p-" + m.id, class: "panel narration" + (wide ? " full" : "") },
        el("div", { class: "caption" }, m.event), el("footer", { class: "panel-foot" }, el("span", {}), el("time", { datetime: m.at }, when(m.at))));
    }
    const mine = m.dir === "out";
    const needs = needsYou(m);
    const caption = m.state_text && !needs && el("div", { class: "caption" + (working(m) ? " running" : "") }, m.state_text);
    const waiting = m.next && m.next.startsWith("Waiting on") && el("div", { class: "caption" }, m.next);
    return el("article", { id: "p-" + m.id, class: "panel" + (wide ? " full" : "") + (mine ? " mine" : " theirs") + (needs ? " splash" : "") },
      stampWord[m.kind] && el("span", { class: "stamp" }, stampWord[m.kind] + (m.status && m.status !== "done" ? " · " + (statusWord[m.status] || m.status).toUpperCase() : "")),
      el("button", { type: "button", class: "read-btn", "aria-label": "Read in full", title: "Read in full", onclick: () => readDialog(m) }, "⤢"),
      caption, waiting,
      el("div", { class: "art" }, avatar(faceOf(m), "actor"),
        el("div", { class: "balloons" },
          el("div", { class: "balloon speech " + (mine ? "mine" : "theirs") },
            el("p", { class: "balloon-text" }, m.body),
            m.files && m.files.length && el("div", { class: "props" }, m.files.map((f) => el("span", { class: "prop" }, "📎 " + f.name)))),
          m.summary && el("div", { class: "balloon thought mine" },
            el("p", { class: "thought-label" }, "Your responder's summary, only on this computer"),
            el("p", { class: "balloon-text" }, m.summary)))),
      el("footer", { class: "panel-foot" }, el("span", {}, authorName(m) + " → " + (mine ? m.to : "you")), el("time", { datetime: m.at }, when(m.at))),
      needs && el("div", { class: "splash-box" },
        el("div", { class: "splash-word" }, "NEEDS YOU!"),
        el("p", {}, (m.state_text || "").replace(/^Needs you: /, "")),
        m.detail && el("p", { class: "hint" }, m.detail),
        el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, this.t, i === 0)))),
      working(m) && el("div", { class: "acts" }, actionButton("cancel", m, this.t, false)),
      isReport(m) && (m.actions || []).length > 0 && el("div", { class: "acts" }, m.actions.map((a) => actionButton(a, m, this.t, false))));
  },
};

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

const Zoom = {
  level: 0, peer: null, person: null, dm: null, msg: null, origins: [], query: "",

  // Two kinds of path: a person (by their record) and their DMs, or a
  // device contact (by address) and its conversations. Never both at once.
  personOf() { return ((state.overview && state.overview.people) || []).find((p) => personKey(p) === this.person); },

  names() {
    if (this.level === 0) {
      return ["Everyone", state.overview && state.overview.persons ? "Person or contact" : "Contact", "Conversation", "Message"];
    }
    if (this.person) {
      const p = this.personOf(), d = state.dmData;
      return ["Everyone", p ? p.label : "Person", d && this.level >= 2 && d.messages[0] ? firstLine(d.messages[0].body, 40) : "DM", "Message"];
    }
    const t = state.data;
    return ["Everyone", this.level >= 1 ? this.peer : "Contact", t && this.level >= 2 ? firstLine(t.messages[0].body, 40) : "Conversation", "Message"];
  },

  // refresh redraws the current level after new data, without motion.
  refresh() {
    if (this.person) {
      if (this.level >= 2 && (!state.dmData || state.dm !== this.dm)) this.level = 1;
      if (this.level === 3 && !state.dmData.messages.some((m) => m.id === this.msg)) this.level = 2;
    } else {
      if (this.level >= 2 && (!state.data || state.data.peer !== this.peer)) this.level = this.peer ? 1 : 0;
      if (this.level === 3 && !state.data.messages.some((m) => m.id === this.msg)) this.level = 2;
    }
    const typing = document.activeElement && document.activeElement.classList && document.activeElement.classList.contains("zoom-search");
    fill($("zoom"), this.layer());
    if (typing) { const f = $("zoom").querySelector(".zoom-search"); if (f) f.focus(); }
  },

  // results is what the Zoom search finds; choosing one zooms to it.
  results() {
    const pick = (level, patch) => { this.query = ""; this.go(level, patch); };
    return el("div", { class: "zoom-results" }, el("ul", { class: "conv-list" }, searchItems(this.query, state.overview.threads, {
      person: (p) => pick(1, { person: personKey(p), peer: null }),
      dm: (d) => pick(2, { dm: d.id }),
      contact: (c) => pick(1, { peer: c.peer, person: null }),
      conversation: (t) => pick(2, { thread: t.id, peer: t.peer, person: null }),
    })));
  },

  content(views) { return this.query.trim() ? this.results() : views[this.level](); },

  layer() {
    const views = this.person ? [() => this.everyone(), () => this.personLevel(), () => this.dmLevel(), () => this.dmMessage()]
      : [() => this.everyone(), () => this.person_(), () => this.thread(), () => this.message()];
    const content = el("div", { class: "zoom-content" }, this.content(views));
    const search = el("input", { type: "search", class: "zoom-search", placeholder: "Search people, agents and conversations",
      "aria-label": "Search people, agents and conversations", autocomplete: "off", spellcheck: "false", value: this.query,
      oninput: (e) => { this.query = e.target.value; fill(content, this.content(views)); },
      onkeydown: (e) => { if (e.key === "Escape" && this.query) { e.stopPropagation(); e.target.value = ""; this.query = ""; fill(content, this.content(views)); } } });
    return el("div", { class: "zoom-layer" },
      el("div", { class: "zoom-side" }, lensSwitch(), search, el("nav", { class: "ladder", "aria-label": "Zoom level" },
        this.names().map((n, i) => el("button", {
          type: "button", class: "rung" + (i === this.level ? " here" : ""), disabled: i > this.level,
          "aria-current": i === this.level ? "step" : "false", onclick: () => i < this.level && this.go(i, {}),
        }, el("span", { class: "rung-dot" }), el("span", { class: "rung-name" }, n))),
        el("p", { class: "hint ladder-hint" }, "Esc zooms out"))),
      content);
  },

  async go(level, patch, from) {
    const inward = level > this.level;
    if (inward && from) this.origins[this.level] = from.getBoundingClientRect();
    const origin = inward ? (from ? from.getBoundingClientRect() : null) : this.origins[level];
    const { thread, dm, ...rest } = patch;
    Object.assign(this, rest, { level });
    if (thread) {
      const changed = beginThread(thread); // the same switch as the other views: drafts stay put
      this.person = null;
      await loadThread(true);
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
      old.addEventListener("animationend", () => old.remove(), { once: true });
      fresh.addEventListener("animationend", () => fresh.classList.remove("zoom-arrive-in", "zoom-arrive-out"), { once: true });
    } else if (old) old.remove();
    const target = fresh.querySelector(".zoom-content button, .zoom-content [tabindex]");
    if (target) target.focus({ preventScroll: true });
  },

  out() { if (this.level > 0) this.go(this.level - 1, {}); },

  onKey(e) {
    if (e.key === "Escape" && this.level > 0) { this.out(); return true; }
    return false;
  },

  // people is the group of persons at the top of Level 0: each by the name
  // they give, with their device and DMs, and a line to each agent their
  // device runs in DMs here (the invitation's host, never a name).
  people() {
    const o = state.overview;
    if (!o.persons) return null;
    const head = el("h3", { class: "zoom-group" }, "People");
    if (!o.person) {
      return el("section", { class: "zoom-people-set" }, head,
        el("p", { class: "hint" }, "You have no person yet. A person is you, the human, as others see you in DMs."),
        el("button", { type: "button", class: "chip", onclick: () => personDialog() }, "Set up your person…"));
    }
    const dms = o.dms || [], people = o.people || [];
    return el("section", { class: "zoom-people-set" }, head,
      el("p", { class: "hint" }, "You: " + o.person.label + ". Each name is what that person calls themself."),
      people.length ? el("ul", { class: "person-cards" }, people.map((p) => {
        const theirs = dms.filter((d) => d.peer.person && d.peer.person === p.person);
        const held = theirs.reduce((n, d) => n + d.held, 0), unread = theirs.reduce((n, d) => n + d.unread, 0);
        const status = ["via " + p.address, personStateText[p.state] || p.state, theirs.length && plural(theirs.length, "DM", "DMs"),
          held && held + " held", unread && unread + " new"].filter(Boolean).join(" · ");
        const b = el("button", { type: "button", class: "person-card" + (p.state === "conflict" ? " danger" : ""), "aria-label": p.label + ", " + status },
          avatar(p.label || p.address, "node-face"), el("span", { class: "node-name" }, p.label), el("span", { class: "node-status" }, status));
        b.addEventListener("click", () => this.go(1, { person: personKey(p), peer: null }, b));
        return el("li", { class: "person-cluster" }, b, agentNodes(p, (conv) => this.go(2, { person: personKey(p), dm: conv, peer: null })));
      })) : el("p", { class: "hint" }, "No one else on your server has set up a person yet."),
      o.person.agents && o.person.agents.length > 0 && el("div", { class: "person-cluster mine" },
        el("p", { class: "hint" }, "You"), agentNodes(o.person, (conv) => { this.person = null; openDM(conv); })));
  },

  // Level 0: the people you have DMs with, and each device you talk to,
  // around this computer.
  everyone() {
    const o = state.overview;
    const list = contactsOf(o.threads);
    const people = this.people();
    const devices = people && el("h3", { class: "zoom-group" }, "Devices: messages per installation");
    if (!list.length) {
      return el("div", { class: "zoom-people" }, zoomReminders(), people, devices,
        el("p", { class: "hint" }, "No conversations yet: start one with someone on your server."), zoomDirectory(o.threads));
    }
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 100 100");
    svg.setAttribute("preserveAspectRatio", "none");
    svg.setAttribute("class", "net-lines");
    svg.setAttribute("aria-hidden", "true");
    const pos = list.map((_, i) => {
      const a = -Math.PI / 2 + Math.PI / list.length + (2 * Math.PI * i) / list.length;
      return [50 + 38 * Math.cos(a), 50 + 34 * Math.sin(a)];
    });
    pos.forEach(([x, y], i) => {
      const l = document.createElementNS("http://www.w3.org/2000/svg", "line");
      for (const [k, v] of [["x1", 50], ["y1", 50], ["x2", x], ["y2", y]]) l.setAttribute(k, v);
      l.setAttribute("class", "net-line" + (list[i].review ? " hot" : ""));
      l.setAttribute("vector-effect", "non-scaling-stroke");
      svg.append(l);
    });
    const nodes = list.map((p, i) => {
      const status = [presenceOf(p.peer), plural(p.conversations.length, "conversation", "conversations"),
        p.review && p.review + " need" + (p.review === 1 ? "s" : "") + " you", p.keyChanged && "key changed",
        p.unread && p.unread + " new", p.notices && plural(p.notices, "report", "reports")].filter(Boolean).join(" · ");
      const node = el("button", { type: "button", class: "node" + (p.review ? " glow" : "") + (p.keyChanged ? " danger" : ""),
        "aria-label": p.peer + ", " + status },
        avatar(p.peer, "node-face"), el("span", { class: "node-name" }, who(p.peer)), el("span", { class: "node-status" }, status),
        p.review ? el("span", { class: "badge" }, String(p.review)) : null);
      node.style.left = pos[i][0] + "%";
      node.style.top = pos[i][1] + "%";
      node.addEventListener("click", () => this.go(1, { peer: p.peer, person: null }, node));
      return node;
    });
    const me = el("div", { class: "node me" }, avatar(o.me.address, "node-face"), el("span", { class: "node-name" }, "You"),
      el("span", { class: "node-status" }, o.me.address));
    me.style.left = "50%";
    me.style.top = "50%";
    return el("div", { class: "zoom-people" }, zoomReminders(), people, devices,
      el("div", { class: "network" }, svg, me, nodes,
        el("p", { class: "zoom-hint" }, "Glowing contacts have something waiting for your decision.")),
      zoomDirectory(o.threads));
  },

  // Level 1 for a person: their separate DMs (the same rows the sidebar
  // shows) and a new one.
  personLevel() {
    const p = this.personOf();
    if (!p) return el("p", { class: "hint" }, "That person is not listed here any more.");
    const dms = ((state.overview && state.overview.dms) || []).filter((d) => d.peer.person && d.peer.person === p.person);
    const online = presenceOf(p.address);
    return el("div", { class: "zoom-person" },
      el("header", { class: "zoom-head" }, avatar(p.label || p.address), el("div", {}, el("h2", {}, p.label),
        el("p", { class: "hint" }, "The name they give · via " + p.address + " · " + (personStateText[p.state] || p.state) +
          (online ? " · their computer is " + online : "")))),
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
    const d = state.dmData;
    if (!d) return el("p", { class: "hint" }, "This DM could not be loaded.");
    const me = state.overview && state.overview.person ? state.overview.person.label : "You";
    return el("div", { class: "zoom-scene" },
      el("header", { class: "zoom-head" }, el("div", {},
        el("p", { class: "hint" }, "DM with " + d.peer.label + " (the name they give) · via " + d.peer.address),
        el("h2", {}, d.messages[0] ? firstLine(d.messages[0].body, 80) : "No messages yet"))),
      d.frozen && el("p", { class: "notice" }, d.frozen),
      el("ol", { class: "mini-chat" }, d.messages.map((m) => {
        if (m.event) return el("li", { class: "event-line" }, el("span", {}, m.event), el("time", { datetime: m.at }, when(m.at)));
        const mine = m.dir === "out";
        const bubble = el("button", { type: "button", class: "mc-bubble" },
          el("span", { class: "mc-who" }, dmAuthor(m, d) + (kindTag[m.kind] ? " · " + kindTag[m.kind] : "") + " · " + when(m.at)),
          el("span", { class: "mc-text" }, m.body));
        bubble.addEventListener("click", () => this.go(3, { msg: m.id }, bubble));
        return el("li", { class: "mc " + (mine ? "mine" : "theirs") }, avatar(mine ? me : d.peer.label || m.from, "sm"), bubble,
          m.state_text && el("p", { class: "narr" }, m.state_text));
      })),
      el("div", { class: "zoom-write" }, el("button", { type: "button", class: "btn", disabled: !!d.frozen, onclick: () => dmWriteDialog(d) },
        d.frozen ? "Nothing more can be sent in this conversation" : "Write in this DM…")));
  },

  // Level 3 for a person: one DM message with everything known about it.
  dmMessage() {
    const d = state.dmData, m = d.messages.find((x) => x.id === this.msg);
    const det = dmDetails(m);
    det.open = true;
    const me = state.overview && state.overview.person ? state.overview.person.label : "You";
    return el("article", { class: "zoom-message", tabindex: "-1" },
      el("div", { class: "meta" }, avatar(m.dir === "out" ? me : d.peer.label || m.from, "sm"), el("span", { class: "who" }, dmAuthor(m, d)),
        kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]), el("time", { datetime: m.at }, when(m.at))),
      el("p", { class: "body" }, m.body),
      m.state_text && el("p", { class: "hint" }, m.state_text),
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
    const t = state.data;
    return el("div", { class: "zoom-scene" },
      el("header", { class: "zoom-head" }, el("div", {}, el("p", { class: "hint" }, who(t.peer)), el("h2", {}, firstLine(t.messages[0].body, 80)))),
      el("ol", { class: "mini-chat" }, t.messages.map((m) => {
        const mine = m.dir === "out";
        const bubble = el("button", { type: "button", class: "mc-bubble" },
          el("span", { class: "mc-who" }, authorName(m) + (kindTag[m.kind] ? " · " + kindTag[m.kind] : "") + " · " + when(m.at)),
          el("span", { class: "mc-text" }, m.body));
        bubble.addEventListener("click", () => this.go(3, { msg: m.id }, bubble));
        return el("li", { class: "mc " + (mine ? "mine" : "theirs") },
          avatar(mine ? state.overview.me.address : m.from, "sm"), bubble,
          m.summary && el("p", { class: "narr" }, "Your responder's summary: " + m.summary),
          m.state_text && !needsYou(m) && el("p", { class: "narr" + (working(m) ? " running" : "") }, m.state_text),
          needsYou(m) && el("div", { class: "decide" }, el("p", { class: "decide-why" }, (m.state_text || "").replace(/^Needs you: /, "Needs you · ")),
            el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, t, i === 0)))),
          working(m) && el("div", { class: "acts" }, actionButton("cancel", m, t, false)),
          isReport(m) && (m.actions || []).length > 0 && el("div", { class: "acts" }, m.actions.map((a) => actionButton(a, m, t, false))));
      })),
      el("div", { class: "zoom-write" }, el("button", { type: "button", class: "btn", disabled: !!t.key.pending,
        onclick: () => writeDialog(t, null) }, t.key.pending ? "Sending is blocked until you trust the new key" : "Write in this conversation…")));
  },

  // Level 3: one message with everything known about it.
  message() {
    const t = state.data, m = t.messages.find((x) => x.id === this.msg);
    const d = details(m);
    d.open = true;
    return el("article", { class: "zoom-message", tabindex: "-1" },
      el("div", { class: "meta" }, avatar(m.dir === "out" ? state.overview.me.address : m.from, "sm"), el("span", { class: "who" }, authorName(m)),
        kindTag[m.kind] && el("span", { class: "tag" }, kindTag[m.kind]), el("time", { datetime: m.at }, when(m.at))),
      el("p", { class: "body" }, m.body),
      m.files && m.files.length && el("p", { class: "hint" }, "Files: " + m.files.map((f) => f.name + " (" + size(f.size) + ")").join(", ")),
      m.summary && el("div", { class: "note" }, el("p", { class: "note-label" }, "Summary written on this computer by your responder"), el("p", { class: "body" }, m.summary)),
      m.state_text && el("p", { class: "hint" }, m.state_text),
      (m.actions || []).length > 0 && el("div", { class: "acts" }, m.actions.map((a, i) => actionButton(a, m, t, i === 0))),
      d);
  },
};
