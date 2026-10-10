// The messenger's state: what the server says (overview and the open
// conversation), how the connection stands, and the person's unsent drafts.
// The server decides everything; this only loads, remembers and asks.
//
// One store serves one membership's host: the UI host mounts the interface
// again with a new host when the person switches workspace, so nothing here
// can act on another workspace by accident.
import { pendingSends } from "./optimistic.mjs";
import { useSyncExternalStore } from "react";
import { api, errorText, type Api, type T } from "./api";
import type { Host, HostEvent, OpenContext } from "./host";
import { messageTarget } from "./model";
import { mainPreferences, personRoots } from "./person-topics.mjs";

export type Conn = "loading" | "live" | "lost" | "updating" | "gone";
// A thread names its agent's device (peer) whenever that is known: an
// agent's topics are one conversation on screen, and what is on screen must
// not change identity when the messages arrive (store.open fills it in).
export type Open = null | { kind: "dm"; id: string; focus?: string; focusSeq?: number } | { kind: "thread"; id: string; focus?: string; focusSeq?: number; peer?: string };

export interface Draft {
  text: string;
  replyTo?: string;          // a message id in the open conversation
  agent?: string;            // legacy/single-button fallback; exact multi-target PIDs live in text mention references
  topic?: string;
  newTopic?: boolean;        // in an agent's conversation: start a separate one with the next send
  files?: StagedFile[];
}

export interface StagedFile {
  key: string;               // local identity in the composer
  name: string;
  size: number;
  type: string;
  url?: string;              // object URL for an image preview (revoked when dropped)
  staged?: unknown;          // what host.stage returned (an id on a daemon, a file handle in a browser)
  file: File;
}

export interface Toast { id: number; text: string; tone?: "ok" | "error" }

export type Tab = "chats" | "agents" | "oks" | "settings";

/** A conversation as last loaded: a DM or group, or a device thread (one topic). */
export type View = T.DMThread | T.Thread;
const VIEWS_KEPT = 24; // conversations kept for an instant reopen (loaded again when opened)
const OPEN_SLOW = 400; // ms a conversation may take to load before its placeholder shows

export interface State {
  tab: Tab;
  overview: T.Overview | null;
  open: Open;
  pending: Open;               // being opened: shown once its messages are here (what is shown stays until then)
  views: Record<string, View>; // the last loaded view of recently opened conversations, by id
  dm: T.DMThread | null;
  thread: T.Thread | null;
  typing: T.TypingView | null;
  invitations: T.GroupInvitationView[];
  conn: Conn;
  version: string;           // the program serving this page when it loaded
  newVersion: string;        // a newer program now serves it (reload to use)
  drafts: Record<string, Draft>;
  toasts: Toast[];
  loadError: string;
  invite: null | { conv: string; selected?: string[]; who?: string; label?: string; topic?: string };  // the "Bring someone in" sheet
  section: string;                                        // the open settings section, when chosen from elsewhere
  agentNames: Record<string, string>;                     // agent ids → the names their owners gave them
  topicBusy: Record<string, string>;
  panel: boolean;                                         // "In this chat" (desktop panel, phone sheet)
}

export const topicChangeKey = (c: Pick<T.TopicChange, "conv" | "peer" | "id" | "root">) => JSON.stringify([c.conv || "", c.peer || "", c.root ? "root" : c.id || ""]);

const draftsKey = (ws: string) => "agentnet.messenger.drafts." + ws;
const recovery = { update: [500, 1000, 2000, 4000, 8000, 15000, 30000], missed: [1000, 3000, 8000] };
const pause = (ms: number) => new Promise((r) => setTimeout(r, ms));

export class Store {
  readonly api: Api;
  readonly sends: ReturnType<typeof pendingSends>;
  private state: State;
  private subs = new Set<() => void>();
  private stopListen: (() => void) | null = null;
  private refresh: Promise<void> | null = null;
  private topicChanges = new Map<string, Promise<{ note: string } | undefined>>();
  private again = false;
  private recovering = false;
  private refreshed = new Set<string>(); // conversations already asked about this open
  private toastSeq = 0;
  private focusSeq = 0;
  private topicFocusSeq = 0;
  private alive = true;
  private chooseMain: ReturnType<typeof mainPreferences>;
  private hiddenChange = false;
  private mobileHidden = () => this.host.platform === "android" &&
    (document.visibilityState === "hidden" || (window as Window & { __agentnetNativeVisible?: boolean }).__agentnetNativeVisible === false);
  private mobileVisible = () => {
    if (!this.alive || this.mobileHidden() || !document.hasFocus()) return;
    // A load admitted before Android paused may already have finished. Retry
    // its unread acknowledgement even when no push arrived while hidden.
    if (!this.hiddenChange) { this.markOpenRead(); return; }
    this.hiddenChange = false;
    if (this.state.conn === "lost" || this.state.conn === "updating") void this.recover(recovery.missed);
    else void this.refetch();
  };

  constructor(readonly host: Host) {
    this.chooseMain = mainPreferences(host.workspace.id, () => localStorage);
    this.api = api(host);
    this.sends = pendingSends(host, () => {
      const o = this.state?.open;
      if (o) this.set(this.viewPatch(o, this.state.views[o.id] ?? null, true));
    });
    this.state = {
      tab: "chats", overview: null, open: null, pending: null, views: {}, dm: null, thread: null, typing: null, invitations: [],
      conn: "loading", version: "", newVersion: "", drafts: this.loadDrafts(), toasts: [], loadError: "", invite: null, topicBusy: {}, panel: false, section: "", agentNames: {},
    };
  }

  // ---- subscription
  get = () => this.state;
  subscribe = (fn: () => void) => { this.subs.add(fn); return () => this.subs.delete(fn); };
  private set(patch: Partial<State>) {
    if (!this.alive) return;
    this.state = { ...this.state, ...patch };
    for (const fn of this.subs) fn();
  }

  // ---- lifecycle
  // start listens before the first load: a change told while that load
  // runs (an arrival, the relay connection coming up) causes one more load
  // right after it, as during any load (refetch), instead of staying unseen
  // until the next change. A large history makes that first load long.
  // A handle that cannot listen (stale, blocked) cannot load either: the
  // first load then says why.
  async start() {
    if (this.host.platform === "android") {
      document.addEventListener("visibilitychange", this.mobileVisible);
      document.addEventListener("agentnet-native-visibility", this.mobileVisible);
      window.addEventListener("focus", this.mobileVisible);
    }
    try { this.listen(); } catch { /* the first load fails and shows it */ }
    await this.load(true);
  }

  stop() {
    this.alive = false;
    if (this.host.platform === "android") {
      document.removeEventListener("visibilitychange", this.mobileVisible);
      document.removeEventListener("agentnet-native-visibility", this.mobileVisible);
      window.removeEventListener("focus", this.mobileVisible);
    }
    this.sends.dispose();
    this.stopListen?.();
    this.stopListen = null;
  }

  isActive() { return this.alive; }

  private listen() {
    this.stopListen?.();
    this.stopListen = this.host.listen((e: HostEvent) => {
      if (!this.alive) return;
      // The native connection may keep syncing with the screen off. Render one
      // authoritative snapshot on return, not a UI refresh for every carrier.
      if (this.mobileHidden()) {
        this.hiddenChange = true;
        if (e.type !== "change") this.set({ conn: e.type === "restart" ? "updating" : "lost" });
        return;
      }
      if (e.type === "change") void this.refetch();
      else if (e.type === "restart") { this.set({ conn: "updating" }); void this.recover(recovery.update); }
      else { this.set({ conn: "lost" }); void this.recover(recovery.missed); }
    });
  }

  // refetch loads what is shown once per burst of changes: changes that
  // arrive while it loads cause one more load, not one each.
  refetch(): Promise<void> { return this.load(false); }

  // load is that one load under way. The first one (start) also says why it
  // failed; a later failure waits for the next change, or the person.
  private load(first: boolean): Promise<void> {
    this.again = true;
    if (!this.refresh) this.refresh = (async () => {
      try {
        do {
          this.again = false;
          try { await this.reload(first); }
          catch (e) { if (!first) throw e; this.set({ loadError: errorText(e), conn: "lost" }); }
          first = false;
        } while (this.again && this.alive);
      } catch { /* the next change, or the person, tries again */ }
      finally { this.refresh = null; }
    })();
    return this.refresh;
  }

  // One exact topic action stays busy through its view refresh. Multiple
  // controls cannot enqueue duplicate lifecycle records against a stale view.
  changeTopic(what: Parameters<Api["changeTopic"]>[0], c: T.TopicChange) {
    const key = topicChangeKey(c);
    const pending = this.topicChanges.get(key);
    if (pending) return pending;
    this.set({ topicBusy: { ...this.state.topicBusy, [key]: what } });
    const work = (async () => {
      try {
        const result = await this.api.changeTopic(what, c);
        await this.refetch();
        return result;
      } catch (e) {
        if (errorText(e) !== "stale or disconnected workspace") this.toast(errorText(e), "error");
        return undefined;
      } finally {
        this.topicChanges.delete(key);
        const topicBusy = { ...this.state.topicBusy }; delete topicBusy[key];
        this.set({ topicBusy });
      }
    })();
    this.topicChanges.set(key, work);
    return work;
  }

  private async reload(first: boolean) {
    const o = await this.api.overview();
    const patch: Partial<State> = { overview: o, loadError: "", conn: "live" };
    if (first || !this.state.version) patch.version = o.version;
    else if (o.version && o.version !== this.state.version) patch.newVersion = o.version;
    this.set(patch);
    if (o.groups) this.api.groupInvitations().then((inv) => this.set({ invitations: inv || [] })).catch(() => {});
    void this.loadAgentNames();
    await this.loadOpen();
  }

  private async recover(schedule: number[]) {
    if (this.recovering) return;
    this.recovering = true;
    try {
      for (const wait of schedule) {
        await pause(wait);
        if (!this.alive) return;
        try {
          await this.reload(false);
          this.listen();
          return;
        } catch (e) {
          // The daemon was replaced under a workspace of this computer: the
          // host binds the same membership again (identity-checked) and
          // mounts Comic again over it. Text drafts are kept per
          // workspace; staged files and sends under way are not replayed.
          if (errorText(e) === "stale or disconnected workspace" && this.host.reconnect) {
            try { await this.host.reconnect(); } catch { /* next attempt */ }
            if (!this.alive) return; // mounted again over the new binding
          }
        }
      }
      this.set({ conn: this.state.conn === "updating" ? "gone" : "lost" });
    } finally {
      this.recovering = false;
    }
  }

  retryNow() { void this.recover([0]); }

  // ---- the open conversation
  // A UI preference selects an existing signed Main root, never a new scope.
  // Reloads and late history cannot silently change its audience or draft.
  personMain(dm: T.DMThread | T.DMSummary): string {
    const person = dm.peer?.person;
    if (!person || dm.kind === "group" || dm.role && dm.role !== "member") return "";
    const roots = personRoots(this.state.overview, dm as T.DMThread);
    return this.chooseMain(roots, person);
  }

  async openChat(o: Open) {
    if (o?.kind !== "dm" || o.focus) return this.open(o);
    const dm = this.state.overview?.dms?.find(d => d.id === o.id);
    const main = dm && this.personMain(dm);
    if (!main) return this.open(o);
    this.setDraft(main, { ...this.draft(main), topic: undefined, newTopic: false, replyTo: undefined });
    return this.open({ kind: "dm", id: main });
  }

  // open shows a conversation without a blank in between: one loaded
  // before shows at once (and is loaded again behind it); otherwise what is
  // on screen stays until the new one's messages are here, and only a slow
  // load (OPEN_SLOW) shows the opening placeholder. Switching topics with
  // one agent never shows it: the open topic stays until the next is ready.
  async open(o: Open) {
    if (!o) { this.close(); return; }
    if (o.focus) o = { ...o, focusSeq: ++this.focusSeq };
    if (o.kind === "thread" && !o.peer) {
      const id = o.id;
      const peer = (this.state.views[id] as T.Thread | undefined)?.peer || (this.state.overview?.threads || []).find((t) => t.id === id)?.peer;
      if (peer) o = { ...o, peer };
    }
    const cur = this.state.open;
    const cached = this.state.views[o.id];
    if ((cur && cur.kind === o.kind && cur.id === o.id) || cached) {
      this.set({ open: o, pending: null, ...this.viewPatch(o, cached ?? null, cur?.id === o.id) });
      await this.loadOpen(o);
    } else {
      this.set({ pending: o });
      const topicSwitch = cur?.kind === "thread" && o.kind === "thread";
      const slow = topicSwitch ? undefined : setTimeout(() => {
        if (this.state.pending === o) this.set({ open: o, pending: null, dm: null, thread: null, typing: null });
      }, OPEN_SLOW);
      await this.loadOpen(o);
      clearTimeout(slow);
    }
    if (this.state.open === o && !this.refreshed.has(o.id)) { // once per open: receipts the server still holds
      this.refreshed.add(o.id);
      this.api.refresh(o.id).catch(() => {});
    }
  }

  // viewPatch: the state that shows o with view v (null: not loaded yet).
  private viewPatch(o: NonNullable<Open>, v: View | null, same: boolean): Partial<State> {
    if (same && !v) return {};
    if (v) v = { ...v, messages: this.sends.merge<T.DMMessage | T.Message>(o.id, v.messages || []) } as View;
    // A request arrow chooses its actual topic, including Main flow. Keep
    // unsent text, staged files and reply intact; only the displayed topic changes.
    const target = o.kind === "dm" && o.focus && o.focusSeq !== this.topicFocusSeq && v ? messageTarget((v as T.DMThread).messages || [], o.focus) : undefined;
    if (target) this.topicFocusSeq = o.focusSeq || 0;
    const draft = this.draft(o.id);
    const drafts = target ? { drafts: { ...this.state.drafts, [o.id]: { ...draft, topic: target.topic || "", newTopic: false } } } : {};
    const typing = same ? {} : { typing: null };
    const views = v ? { views: { ...this.state.views, [o.id]: v } } : {};
    return o.kind === "dm" ? { dm: (v as T.DMThread | null), thread: null, ...views, ...typing, ...drafts } : { thread: (v as T.Thread | null), dm: null, ...views, ...typing };
  }

  // prefetch loads a conversation the person is about to open (a press or a
  // pause over its row), so that opening it shows it at once; an open that
  // follows uses the same request. A read only: nothing is marked read by
  // it. It answers the view (loaded before, or now), or null.
  private fetching = new Map<string, Promise<View>>();
  prefetch(o: Open): Promise<View | null> {
    if (!o) return Promise.resolve(null);
    const have = this.state.views[o.id];
    if (have) return Promise.resolve(have);
    let f = this.fetching.get(o.id);
    if (!f) {
      f = (o.kind === "dm" ? this.api.dm(o.id) : this.api.thread(o.id)) as Promise<View>;
      this.fetching.set(o.id, f);
      f.then((v) => { if (!this.state.views[o.id]) this.keep(o.id, v); }, () => {}).finally(() => this.fetching.delete(o.id));
    }
    return f.catch(() => null);
  }

  // keep remembers a loaded view (the most recent VIEWS_KEPT, and the open one).
  private keep(id: string, v: View) {
    v = { ...v, messages: this.sends.merge<T.DMMessage | T.Message>(id, v.messages || []) } as View;
    const views: Record<string, View> = { ...this.state.views };
    delete views[id];
    views[id] = v;
    const ids = Object.keys(views);
    for (const old of ids.slice(0, Math.max(0, ids.length - VIEWS_KEPT))) if (old !== this.state.open?.id) delete views[old];
    this.set({ views });
  }

  showTab(tab: Tab, section = "") {
    const hidden = this.state.tab === "settings";
    this.set({ tab, section });
    if (hidden && tab !== "settings") this.markOpenRead();
  }
  openInvite(conv: string, selected?: string[], preset?: { who: string; label?: string; topic?: string }) { this.set({ invite: { conv, selected, topic: this.draft(conv).topic, ...preset } }); }
  closeInvite() { this.set({ invite: null }); }
  setPanel(panel: boolean) { this.set({ panel }); }

  // openMessage opens the conversation a notification's message belongs to:
  // the conversation the host names (context.conv); else the device
  // conversation that message starts; else the conversation that holds it,
  // found by looking (a DM's review notice names only the message and its
  // direction), never guessed. Nothing is sent, accepted or read by landing.
  async openMessage(id: string, context?: OpenContext) {
    this.set({ tab: "chats" });
    if (context?.conv && /^[0-9a-f]{64}$/.test(context.conv)) { await this.open({ kind: "dm", id: context.conv, focus: id }); return; }
    const o = this.state.overview, dms = o?.dms || [];
    if (dms.some((d) => d.id === id)) { await this.open({ kind: "dm", id }); return; }
    if ((o?.threads || []).some((t) => t.id === id)) { await this.open({ kind: "thread", id, focus: id }); return; }
    for (const d of dms) {
      try {
        const v = await this.api.dm(d.id);
        if (messageTarget((v.messages || []).filter(m => !context?.dir || m.dir === context.dir), id)) { await this.open({ kind: "dm", id: d.id, focus: id }); return; }
      } catch { /* the next one */ }
    }
    if (!context?.conv) { // a later message of a device conversation
      try {
        const t = await this.api.thread(id);
        if ((t.messages || []).some((m) => m.id === id)) { await this.open({ kind: "thread", id, focus: id }); return; }
      } catch { /* not a device conversation here */ }
    }
    this.toast("That message is not on this device.");
  }

  // openChannel opens the conversation a browser notification's channel
  // names; one this device does not have opens nothing in its place.
  async openChannel(chan: string) {
    this.set({ tab: "chats" });
    try {
      const r = await this.api.notifyResolve(chan);
      const conv = r?.conv || "";
      if (/^[0-9a-f]{64}$/.test(conv)) { await this.open({ kind: "dm", id: conv }); return; }
    } catch { /* said below */ }
    this.toast("The conversation of that notification is not on this device.");
  }

  // close leaves the conversation; its view stays remembered (a phone
  // slides it away, and opening it again is instant).
  close() {
    if (this.state.open) this.refreshed.delete(this.state.open.id);
    this.set({ open: null, pending: null, dm: null, thread: null, typing: null });
  }

  // loadOpen loads o (the open conversation, or the one being opened) and
  // shows it while it is still the one wanted; a load that lost the race is
  // only remembered.
  private async loadOpen(o: Open = this.state.open) {
    if (!o) return;
    const wanted = () => this.alive && (this.state.open === o || this.state.pending === o);
    try {
      // Being opened: a prefetch already under way for it is that load.
      const ahead = this.state.pending === o ? this.fetching.get(o.id) : undefined;
      const v: View = ahead ? await ahead : o.kind === "dm" ? await this.api.dm(o.id) : await this.api.thread(o.id);
      this.keep(o.id, v);
      if (!wanted()) return; // another conversation was opened meanwhile
      const first = this.state.pending === o;
      const newHost = o.kind === "dm" && ((v as T.DMThread).agents || []).some((a) => !(this.state.dm?.agents || []).some((b) => b.host.address === a.host.address));
      this.set({ ...(first ? { open: o, pending: null } : {}), ...this.viewPatch(o, v, this.state.open?.id === o.id) });
      if (newHost) void this.loadAgentNames();
      this.markOpenRead();
      if (o.kind === "dm") this.api.typing({ conv: o.id }).then((t) => { if (this.state.open === o) this.set({ typing: t }); }).catch(() => {});
    } catch (e) {
      if (!wanted()) return;
      // Its placeholder then says it is still opening, with Try again.
      if (this.state.pending === o) this.set({ open: o, pending: null, dm: null, thread: null, typing: null });
      this.toast(errorText(e), "error");
    }
  }

  // Reading one topic does not read the other topics returned by the same
  // conversation endpoint. Match Conversation's visible-message selection.
  private reading = new Set<string>();
  private markOpenRead() {
    const o = this.state.open;
    if (!this.alive || !o || this.state.tab === "settings" || o.kind === "dm" && o.focus && o.focusSeq !== this.topicFocusSeq) return;
    if (this.host.platform === "android" && (this.mobileHidden() || !document.hasFocus())) return;
    const v = this.state.views[o.id];
    if (!v) return;
    const draft = this.state.drafts[o.id];
    const messages = o.kind === "dm"
      ? ((v as T.DMThread).messages || []).filter(m => draft?.newTopic ? !m.topic : (m.topic || "") === (draft?.topic || ""))
      : v.messages;
    const ids = (messages || []).filter(m => m.unread && !this.reading.has(m.id)).map(m => m.id);
    if (!ids.length) return;
    ids.forEach(id => this.reading.add(id));
    void this.api.markRead(ids).then(() => {
      // Fetch the committed read state, including topic and chat counts.
      // A failed acknowledgement leaves the unread state intact for retry.
      if (this.alive) void this.refetch();
    }).catch(() => {}).finally(() => ids.forEach(id => this.reading.delete(id)));
  }

  // loadAgentNames reads the agent catalogs once per change: this
  // installation's own, and the hosts of agents in the open conversation.
  private namesLoading = false;
  private async loadAgentNames() {
    if (this.namesLoading) return;
    this.namesLoading = true;
    try {
      const hosts = new Set<string>([""]);
      for (const a of this.state.dm?.agents || []) hosts.add(a.host.address);
      const next: Record<string, string> = { ...this.state.agentNames };
      for (const h of hosts) {
        try {
          const c = await this.api.agents(h || undefined);
          for (const a of c.agents || []) next[a.record.id] = a.record.label;
        } catch { /* names stay as words from addresses */ }
      }
      if (Object.keys(next).some(id => this.state.agentNames[id] !== next[id])) this.set({ agentNames: next });
    } finally { this.namesLoading = false; }
  }

  // ---- drafts, kept per workspace and conversation (text only survives a reload)
  private loadDrafts(): Record<string, Draft> {
    try {
      const raw = JSON.parse(localStorage.getItem(draftsKey(this.host.workspace.id)) || "{}");
      const out: Record<string, Draft> = {};
      for (const [k, v] of Object.entries(raw as Record<string, Draft>)) if (v && typeof v.text === "string") out[k] = { text: v.text, replyTo: v.replyTo, agent: v.agent };
      return out;
    } catch { return {}; }
  }

  private saveDrafts(drafts: Record<string, Draft>) {
    try {
      const keep: Record<string, Draft> = {};
      for (const [k, d] of Object.entries(drafts)) if (d.text || d.replyTo || d.agent) keep[k] = { text: d.text, replyTo: d.replyTo, agent: d.agent };
      localStorage.setItem(draftsKey(this.host.workspace.id), JSON.stringify(keep));
    } catch { /* a convenience only */ }
  }

  draft(conv: string): Draft { return this.state.drafts[conv] || { text: "" }; }

  setDraft(conv: string, d: Draft, topicSelected = false) {
    const prior = this.state.drafts[conv];
    const drafts = { ...this.state.drafts };
    if (!d.text && !d.replyTo && !d.agent && !d.newTopic && !d.topic && !(d.files && d.files.length)) delete drafts[conv];
    else drafts[conv] = d;
    this.set({ drafts });
    this.saveDrafts(drafts);
    if (this.state.open?.id === conv && (topicSelected || (prior?.topic || "") !== (d.topic || "") || !!prior?.newTopic !== !!d.newTopic)) {
      this.topicFocusSeq = this.state.open.focusSeq || 0;
      this.markOpenRead();
    }
  }

  hasUnsent(): boolean {
    return Object.values(this.state.drafts).some((d) => d.text || (d.files && d.files.length));
  }

  // ---- feedback
  toast(text: string, tone?: Toast["tone"]) {
    const id = ++this.toastSeq;
    this.set({ toasts: [...this.state.toasts, { id, text, tone }] });
    setTimeout(() => this.set({ toasts: this.state.toasts.filter((t) => t.id !== id) }), tone === "error" ? 7000 : 3500);
  }

  // run performs one action and reports its failure in words; the change
  // stream then brings what it changed.
  async run<R>(fn: (a: Api) => Promise<R>, ok?: string): Promise<R | undefined> {
    try {
      const r = await fn(this.api);
      if (ok) this.toast(ok, "ok");
      void this.refetch();
      return r;
    } catch (e) {
      // A workspace being left or replaced answers its old view this way:
      // the host remounts the interface, so there is nothing to tell.
      if (errorText(e) !== "stale or disconnected workspace") this.toast(errorText(e), "error");
      return undefined;
    }
  }
}

export function useStore<S>(store: Store, select: (s: State) => S): S {
  return useSyncExternalStore(store.subscribe, () => select(store.get()));
}
