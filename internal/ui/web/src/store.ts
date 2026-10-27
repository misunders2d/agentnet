// The messenger's state: what the server says (overview and the open
// conversation), how the connection stands, and the person's unsent drafts.
// Local intent appears immediately; the server confirms it or supplies failure.
//
// One store serves one membership's host: the UI host mounts the interface
// again with a new host when the person switches workspace, so nothing here
// can act on another workspace by accident.
import { pendingSends } from "./optimistic.mjs";
import { useSyncExternalStore } from "react";
import { api, errorText, type Api, type ReminderResult, type T } from "./api";
import type { Host, HostEvent, OpenContext } from "./host";
import { currentCatalogAgents, directChatKey, messageTarget } from "./model";
import { mainPreferences, personRoots } from "./person-topics.mjs";
import { skin as brand } from "./skin/brand";
import { share } from "./structural";
import { scheduleSaves, type ScheduleSave } from "./schedule-saves";
import { clearScheduledDraft, type ScheduleAttempt } from "./features/Composer.schedule-model";
import { messageChangeKey, messageVersion, messageMatches, changeConfirmed, presentMessage, type MessageChange } from "./message-changes";
import { presentTopic, topicVersion, type TopicPreview } from "./topic-changes";
import { invitationResult, type InvitationSend, type InvitationTarget } from "./invitation-sends";

export type Conn = "loading" | "live" | "lost" | "updating" | "gone";
// A thread names its agent's device (peer) whenever that is known: an
// agent's topics are one conversation on screen, and what is on screen must
// not change identity when the messages arrive (store.open fills it in).
export type Open = null | { kind: "dm"; id: string; focus?: string; preview?: string; focusSeq?: number; unread?: boolean } | { kind: "thread"; id: string; focus?: string; focusSeq?: number; unread?: boolean; peer?: string };

// One ephemeral entry, never a second unread source. Its identity fences late
// viewport callbacks; the first unread is frozen before any acknowledgement.
export interface ReadVisit { readonly scope: string; readonly firstUnread?: string }

export interface Draft {
  text: string;
  replyTo?: string;          // a message id in the open conversation
  agent?: string;            // legacy/single-button fallback; exact multi-target PIDs live in text mention references
  topic?: string;
  newTopic?: boolean;        // in an agent's conversation: start a separate one with the next send
  plainReply?: boolean;      // this Reply stays an ordinary message instead of continuing the replied agent's request
  files?: StagedFile[];
  mediaMode?: "original" | "smaller";
}

export interface StagedFile {
  key: string;               // local identity in the composer
  name: string;
  size: number;
  type: string;
  url?: string;              // object URL for an image preview (revoked when dropped)
  staged?: unknown;          // what host.stage returned (an id on a daemon, a file handle in a browser)
  file: File;
  original?: File;           // untouched source for an explicit Original file choice
  mediaMode?: "original" | "smaller";
  preparing?: boolean;
  mediaProgress?: number;
  mediaNote?: string;
}

export interface Toast { id: number; text: string; tone?: "ok" | "error" }
export interface MessageLanding {
  target: { id: string; context?: OpenContext };
  busy: boolean;
  error: string;
  legacyMiss?: boolean; // a completed unbound search waits for explicit retry/resume/reconnect
}

export type Tab = "chats" | "agents" | "oks" | "outbox" | "settings";

/** A conversation as last loaded: a DM or group, or a device thread (one topic). */
export type View = T.DMThread | T.Thread;
type ConfirmedRead = { kind: "dm" | "thread"; id: string; seq: number; unread: number; group?: string; archived_unread?: number };
const VIEWS_KEPT = 24; // conversations kept for an instant reopen (loaded again when opened)

export interface PendingReaction {
  change: T.ControlAction;
  selected: NonNullable<Open>;
  self: T.Reactor;
  token: number;
  confirmed?: boolean; // durable desired remove/add; projection still pending
  priorRead?: Promise<View>; // latest read before the durable acknowledgement
}
export interface ReminderIntent {
  message: string;
  action: "set" | "done" | "cancel";
  due?: string;
  title?: string;
}
export interface ReminderChange {
  intent: ReminderIntent;
  original: string;
  seq: number;
  reminder: T.ReminderView | null;
  status: "saving" | "confirming" | "failed";
  error?: string;
}
const reminderVersion = (r?: T.ReminderView) => JSON.stringify(r ? [r.message, r.conv, r.from, r.due, r.revision, r.writer] : null);
export const reactionScope = (c: T.ControlAction) => JSON.stringify([c.conv || "", c.id, c.dir]);

interface PendingDismiss {
  token: number;
  seq: number;
  rows: string[];
  views: Record<string, { seq: number; message: string }>;
  saved?: boolean;
}

const dismissMessageKey = (m: T.DMMessage | T.Message) => JSON.stringify([
  m.id, m.dir, m.from, m.target, "pid" in m ? m.pid : undefined, m.state,
  m.detail, "job_detail" in m ? m.job_detail : undefined, m.exec,
  m.continuation, m.attention, m.actions, m.deleted,
]);

export interface State {
  tab: Tab;
  overview: T.Overview | null;
  open: Open;
  pending: Open;               // selected destination whose messages are still loading
  views: Record<string, View>; // the last loaded view of recently opened conversations, by id
  dm: T.DMThread | null;
  thread: T.Thread | null;
  typing: T.TypingView | null;
  invitations: T.GroupInvitationView[];
  conn: Conn;
  version: string;           // the program serving this page when it loaded
  newVersion: string;        // a newer program now serves it (reload to use)
  unsent: boolean;           // a send begun here is not yet stored, or failed with its Retry (sends.unsent)
  drafts: Record<string, Draft>;
  scheduleSaves: Record<string, ScheduleSave>;
  toasts: Toast[];
  messageLanding: MessageLanding | null; // one exact notification, only for this mounted workspace
  loadError: string;
  groupContextPending: string; // selected group keeps its last verified view while its new context arrives
  invite: null | { conv: string; selected?: string[]; who?: string; label?: string; topic?: string };  // the "Bring someone in" sheet
  section: string;                                        // the open settings section, when chosen from elsewhere
  agentNames: Record<string, string>;                     // agent ids → the names their owners gave them
  topicBusy: Record<string, string>;
  topicPreviews: Record<string, TopicPreview>;
  reactionPending: Record<string, Record<string, PendingReaction>>;
  dismissPending: Record<string, PendingDismiss>;
  messageChanges: Record<string, MessageChange>;
  reminderChanges: Record<string, ReminderChange>;
  pictureChange?: { person: T.PersonView; seq: number; png: string; saved?: T.PersonView };
  personRename?: { request: { person: string; original: string; label: string }; saved?: T.PersonView; error?: string };
  personCreation?: { request: { address: string; fingerprint: string; label: string }; saved?: { person: T.PersonView; note: string }; error?: string };
  workspaceRename?: { request: { address: string; fingerprint: string; person?: string; original: string; name: string }; saved?: T.WorkspaceInfoView; error?: string };
  deviceDecisions?: Record<string, { link: T.LinkRequest; person: string; accept: boolean; saved?: boolean }>;
  deviceRemovals?: Record<string, { device: T.DeviceView; person: string; saved?: boolean }>;
  groupDecisions?: Record<string, { invitation: T.GroupInvitationView; person: string; accept: boolean; saved?: boolean }>;
  groupChanges?: Record<string, PendingGroupChange>;
  groupCreation?: { request: { title: string; people: string[] }; person: string; remaining: string[]; id?: string; done?: boolean; error?: string };
  invitationSends?: Record<string, InvitationSend>;
  teamCreation?: { change: T.TeamChange; person: string; saved?: T.TeamState; error?: string };
  teamChanges?: Record<string, { change: T.TeamChange; person: string; original: T.TeamView; saved?: T.TeamState; error?: string }>;
  panel: boolean;                                         // "In this chat" (desktop panel, phone sheet)
  readVisit: ReadVisit | null;
  contentRevision: number; // successful content loads; typing/presence do not advance choice lists
}

export interface PendingGroupChange {
  change: T.GroupChange;
  person: string;
  original: T.DMThread;
  summary?: string;
  saved?: boolean;
  queued?: boolean;
  error?: string;
}

// Message arrival/reads do not replace a group mutation. A newer group name,
// membership, role or freeze does. These are comparison keys, never authority.
const groupVersion = (t: T.DMThread | T.DMSummary) => JSON.stringify([
  t.title, t.role, t.frozen, t.members?.map(m => [m.person, m.admin]).sort(),
]);

export const topicChangeKey = (c: Pick<T.TopicChange, "conv" | "peer" | "id" | "root">) => JSON.stringify([c.conv || "", c.peer || "", c.root ? "root" : c.id || ""]);

const draftsKey = (ws: string) => brand.storage + ".drafts." + ws;

// unsentIn: what a reload would lose: a draft's text or files, or a send
// not yet stored.
export const unsentIn = (s: Pick<State, "drafts" | "unsent"> & Partial<Pick<State,"scheduleSaves" | "messageChanges" | "reminderChanges" | "pictureChange" | "personRename" | "personCreation" | "workspaceRename" | "deviceDecisions" | "deviceRemovals" | "groupDecisions" | "groupChanges" | "groupCreation" | "invitationSends" | "teamCreation" | "teamChanges">>) => s.unsent || !!s.workspaceRename && !s.workspaceRename.saved && !s.workspaceRename.error || !!s.personCreation && !s.personCreation.saved && !s.personCreation.error || !!s.personRename && !s.personRename.saved && !s.personRename.error || Object.values(s.invitationSends || {}).some(p => !p.done && !p.error) || !!s.groupCreation && !s.groupCreation.done && !s.groupCreation.error || Object.values(s.teamChanges || {}).some(p => !p.saved && !p.error) || !!s.teamCreation && !s.teamCreation.saved && !s.teamCreation.error || Object.values(s.groupChanges || {}).some(p => !p.saved && !p.error) || Object.values(s.groupDecisions || {}).some(p => !p.saved) || Object.values(s.deviceDecisions || {}).some(p => !p.saved) || Object.values(s.deviceRemovals || {}).some(p => !p.saved) || !!s.pictureChange && !s.pictureChange.saved || Object.keys(s.reminderChanges || {}).length > 0 || Object.values(s.messageChanges || {}).some(p => p.status === "pending" || p.status === "failed" && p.action === "edit") || Object.values(s.scheduleSaves || {}).some(slot=>!!slot.attempt && (slot.status==="saving" || slot.status==="uncertain")) || Object.values(s.drafts).some((d) => d.text || (d.files && d.files.length));

const recovery = { update: [500, 1000, 2000, 4000, 8000, 15000, 30000], missed: [1000, 3000, 8000] };
const pause = (ms: number) => new Promise((r) => setTimeout(r, ms));

export class Store {
  unreadReviewSize = 50; // prefix size only; Review rows stay in its mounted list
  readonly api: Api;
  readonly sends: ReturnType<typeof pendingSends>;
  private readonly scheduled: ReturnType<typeof scheduleSaves>;
  private state: State;
  private subs = new Set<() => void>();
  private stopListen: (() => void) | null = null;
  private refresh: Promise<void> | null = null;
  private openRefresh: Promise<boolean> | null = null;
  private openAgain = false;
  private overviewRequest = 0;
  private confirmedReads = new Map<string, ConfirmedRead>();
  private confirmedReminders = new Map<string, { seq: number; reminder: T.ReminderView | null }>();
  private reminderWork = new Map<string, { intent: string; work: Promise<ReminderResult | undefined> }>();
  private reminderState?: { source: State; value: State };
  private pictureState?: { source: State; value: State };
  private pictureWork?: { png: string; work: Promise<T.PersonView> };
  private deviceDecisionWork = new Map<string, { accept: boolean; work: Promise<{ note: string }> }>();
  private deviceDecisionState?: { source: State; value: State };
  private deviceRemovalWork = new Map<string, Promise<{ note: string }>>();
  private groupDecisionWork = new Map<string, { accept: boolean; work: Promise<{ recorded: boolean }> }>();
  private groupDecisionState?: { source: State; value: State };
  private groupChangeWork = new Map<string, { change: T.GroupChange; work: Promise<T.GroupChangeResult> }>();
  private groupChangeState?: { source: State; value: State };
  private teamCreationWork?: { change: T.TeamChange; work: Promise<T.TeamState> };
  private teamChangeWork = new Map<string, { change: T.TeamChange; work: Promise<T.TeamState> }>();
  private dmStartWork = new Map<string, Promise<{ id: string }>>();
  private groupCreationWork?: Promise<string>;
  private invitationWork = new Map<string, Promise<InvitationSend>>();
  private personRenameWork?: Promise<T.PersonView>;
  private personCreationWork?: Promise<{ person: T.PersonView; note: string }>;
  private workspaceRenameWork?: Promise<T.WorkspaceInfoView>;
  private workspaceRenameState?: { source: State; value: State };
  private personRenameState?: { source: State; value: State };
  private reminderOverviewCache?: { source: T.Overview; changes: State["reminderChanges"]; value: T.Overview };
  private reactionSequence = 0;
  private reacting = new Map<string, { pending: PendingReaction; work: Promise<{ note: string } | undefined> }>();
  private dismissSequence = 0;
  private dismissing = new Map<string, { intent: string; token: number; work: Promise<{ note: string }> }>();
  private dismissOverview?: { source: T.Overview; pending: State["dismissPending"]; value: T.Overview };
  private dismissState?: { source: State; value: State };
  private messageState?: { source: State; value: State };
  private topicState?: { source: State; value: State };
  private messageViews?: { source: State["views"]; changes: State["messageChanges"]; value: State["views"] };
  private topicViews?: { source: State["views"]; overview: State["overview"]; previews: State["topicPreviews"]; views: State["views"]; shownOverview: State["overview"] };
  private messageWork = new Map<string, { action: MessageChange["action"]; text?: string; work: Promise<{ note: string }> }>();
  private topicChanges = new Map<string, Promise<{ note: string } | undefined>>();
  private again = false;
  private catalogsAgain = false;
  private recovering = false;
  private refreshed = new Set<string>(); // conversations already asked about this open
  private toastSeq = 0;
  private focusSeq = 0;
  private topicFocusSeq = 0;
  private messageRefresh: { target: MessageLanding["target"]; again: boolean; promise: Promise<void> } | null = null;
  private alive = true;
  private unreadReturn: Open = null;
  private unreadLanding = 0;
  private chooseMain: ReturnType<typeof mainPreferences>;
  private hiddenChange = false;
  private mobileHidden = () => this.host.platform === "android" &&
    (document.visibilityState === "hidden" || this.host.android?.visible() === false);
  private offNativeVisibility?: () => void;
  private mobileVisible = () => {
    if (!this.alive || this.mobileHidden() || !document.hasFocus()) return;
    // A load admitted before Android paused may already have finished. Retry
    // its unread acknowledgement even when no push arrived while hidden.
    if (!this.hiddenChange) { this.markOpenRead(); void this.retryMessageLanding(); return; }
    this.hiddenChange = false;
    if (this.state.conn === "lost" || this.state.conn === "updating") void this.recover(recovery.missed);
    else void this.refetch(true);
  };

  constructor(readonly host: Host) {
    this.chooseMain = mainPreferences(host.workspace.id, () => localStorage);
    const calls = api(host);
    this.api = { ...calls, act: action => action.do === "resolve" ? this.dismiss(action, calls) : calls.act(action),
      edit: c => this.changeMessage("edit", c, calls), retract: c => this.changeMessage("delete", c, calls),
      setPersonPicture: png => this.changePicture(png, calls),
      renamePerson: label => this.renamePerson(label, calls),
      createPerson: label => this.createPerson(label, calls),
      renameWorkspaceForEveryone: name => this.renameWorkspace(name, calls),
      decideDevice: (id, accept) => this.decideDevice(id, accept, calls),
      removeDevice: address => this.removeDevice(address, calls),
      decideGroup: (id, accept) => this.decideGroup(id, accept, calls),
      manageGroup: change => this.changeGroup(change, calls),
      newDM: address => this.startDM(address, calls),
      changeTeam: change => change.op === "create" ? this.createTeam(change, calls) : calls.changeTeam(change) };
    this.sends = pendingSends(host, () => {
      const o = this.state?.open;
      // A send clears its draft as it begins: until it is stored, a reload
      // waits for it (hasUnsent).
      this.set({ ...(o ? this.viewPatch(o, this.state.views[o.id] ?? null, true) : {}), unsent: this.sends.unsent() });
    });
    this.state = {
      tab: "chats", overview: null, open: null, pending: null, views: {}, dm: null, thread: null, typing: null, invitations: [], reactionPending: {}, dismissPending: {}, messageChanges: {}, reminderChanges: {},
      conn: "loading", version: "", newVersion: "", unsent: false, drafts: this.loadDrafts(), scheduleSaves:{}, toasts: [], messageLanding: null, loadError: "", groupContextPending: "", invite: null, topicBusy: {}, topicPreviews: {}, panel: false, section: "", agentNames: {}, readVisit: null, contentRevision: 0,
    };
    const saved=host.workspaces?.state?.(host.workspace.id)?.comicDrafts as {handle?:string;open:Open;dm:T.DMThread|null;thread:T.Thread|null;tab:State['tab']}|undefined;
    if(saved&&saved.handle===host.workspace.handle)Object.assign(this.state,{open:saved.open,dm:saved.dm,thread:saved.thread,tab:saved.tab});
    this.scheduled=scheduleSaves(host,()=>this.syncScheduleSaves());
    this.state.scheduleSaves=this.scheduled.snapshot();
  }

  // ---- subscription
  get = () => {
    const s = this.presentWorkspaceRename(this.presentPersonRename(this.presentGroupChanges(this.presentGroupDecisions(this.presentDeviceDecisions(this.presentPicture(this.presentReminders(this.presentMessages(this.presentTopics()))))))));
    if (!s.overview || !Object.keys(s.dismissPending).length) return s;
    if (this.dismissState?.source === s) return this.dismissState.value;
    if (this.dismissOverview?.source !== s.overview || this.dismissOverview.pending !== s.dismissPending) {
      this.dismissOverview = { source: s.overview, pending: s.dismissPending, value: this.dismissedOverview(s.overview) };
    }
    const value = this.dismissOverview.value === s.overview ? s : { ...s, overview: this.dismissOverview.value };
    this.dismissState = { source: s, value };
    return value;
  };

  private presentPicture(s: State): State {
    const p = s.pictureChange, o = s.overview;
    if (!p || !o?.person || o.seq !== p.seq || o.person.person !== p.person.person) return s;
    if (this.pictureState?.source === s) return this.pictureState.value;
    const person = { ...o.person, picture: p.png ? "pending" : undefined,
      picture_url: p.png ? "data:image/png;base64," + p.png : undefined };
    const value = { ...s, overview: { ...o, person } };
    this.pictureState = { source: s, value };
    return value;
  }

  private presentPersonRename(s: State): State {
    const p = s.personRename, o = s.overview;
    if (!p || p.error || !o?.person || o.person.person !== p.request.person || o.person.label !== p.request.original) return s;
    if (this.personRenameState?.source === s) return this.personRenameState.value;
    const value = { ...s, overview: { ...o, person: { ...o.person, label: p.request.label } } };
    this.personRenameState = { source: s, value };
    return value;
  }

  private renamePerson(label: string, calls: Api): Promise<T.PersonView> {
    const person = this.state.overview?.person;
    if (!this.alive || !person?.person) return Promise.reject(Error("This verified person is no longer available."));
    if (this.personRenameWork) return this.state.personRename?.request.label === label ? this.personRenameWork : Promise.reject(Error("Wait for the current name change to finish."));
    const old = this.state.personRename;
    if (old?.saved) return old.request.label === label ? Promise.resolve(old.saved) : Promise.reject(Error("Check the saved name before changing it again."));
    const request = { person: person.person, original: person.label, label };
    const current = () => this.alive && this.state.overview?.person?.person === request.person && this.state.personRename?.request === request;
    this.set({ personRename: { request } });
    const work = Promise.resolve().then(async () => {
      try {
        if (!current()) throw Error("This name change's workspace or person changed.");
        const saved = await calls.renamePerson(label);
        if (current()) this.set({ personRename: { request, saved } });
        return saved;
      } catch (error) {
        if (current()) {
          this.set({ personRename: { request, error: errorText(error) } });
          this.toast("Name change not confirmed. Open Profile to review or retry the exact name.", "error");
        }
        throw error;
      } finally {
        if (this.personRenameWork === work) this.personRenameWork = undefined;
        if (current()) void this.refetch();
      }
    });
    this.personRenameWork = work;
    return work;
  }

  clearPersonRename(request: NonNullable<State["personRename"]>["request"]) {
    if (!this.personRenameWork && this.state.personRename?.request === request && this.state.personRename.error) this.set({ personRename: undefined });
  }

  private createPerson(label: string, calls: Api): ReturnType<Api["createPerson"]> {
    const o = this.state.overview;
    if (!this.alive || !o?.persons || o.person || o.role !== "unset" || o.link?.state === "pending") return Promise.reject(Error("Person setup is no longer available on this device."));
    if (this.personCreationWork) return this.state.personCreation?.request.label === label ? this.personCreationWork : Promise.reject(Error("Wait for the current person setup to finish."));
    const old = this.state.personCreation;
    if (old?.saved) return old.request.label === label ? Promise.resolve(old.saved) : Promise.reject(Error("Check the saved person before trying setup again."));
    const request = { address: o.me.address, fingerprint: o.me.fingerprint, label };
    const current = () => this.alive && this.state.personCreation?.request === request;
    this.set({ personCreation: { request } });
    const work = Promise.resolve().then(async () => {
      try {
        if (!current()) throw Error("This person setup's workspace or device changed.");
        const saved = await calls.createPerson(label);
        if (current()) this.set({ personCreation: { request, saved } });
        return saved;
      } catch (error) {
        if (current()) {
          this.set({ personCreation: { request, error: errorText(error) } });
          this.toast("Person setup not confirmed. Open Profile to check its status or retry.", "error");
        }
        throw error;
      } finally {
        if (this.personCreationWork === work) this.personCreationWork = undefined;
        if (current()) void this.refetch();
      }
    });
    this.personCreationWork = work;
    return work;
  }

  clearPersonCreation(request: NonNullable<State["personCreation"]>["request"]) {
    if (!this.personCreationWork && this.state.personCreation?.request === request && this.state.personCreation.error) this.set({ personCreation: undefined });
  }

  private presentWorkspaceRename(s: State): State {
    const p = s.workspaceRename, o = s.overview;
    if (!p || p.error || !o?.workspace || o.workspace.name !== p.request.original) return s;
    if (this.workspaceRenameState?.source === s) return this.workspaceRenameState.value;
    const value = { ...s, overview: { ...o, workspace: { ...o.workspace, name: p.request.name } } };
    this.workspaceRenameState = { source: s, value };
    return value;
  }

  private renameWorkspace(name: string, calls: Api): Promise<T.WorkspaceInfoView> {
    const o = this.state.overview;
    if (!this.alive || !o) return Promise.reject(Error("This workspace is no longer available."));
    if (this.workspaceRenameWork) return this.state.workspaceRename?.request.name === name ? this.workspaceRenameWork : Promise.reject(Error("Wait for the current workspace name change to finish."));
    const old = this.state.workspaceRename;
    if (old?.saved) return old.request.name === name ? Promise.resolve(old.saved) : Promise.reject(Error("Check the saved workspace name before changing it again."));
    const request = { address: o.me.address, fingerprint: o.me.fingerprint, person: o.person?.person, original: o.workspace?.name || "", name };
    const current = () => this.alive && this.state.workspaceRename?.request === request;
    this.set({ workspaceRename: { request } });
    const work = Promise.resolve().then(async () => {
      try {
        if (!current()) throw Error("This name change's workspace or device changed.");
        const saved = await calls.renameWorkspaceForEveryone(name);
        if (current()) this.set({ workspaceRename: { request, saved } });
        return saved;
      } catch (error) {
        if (current()) {
          this.set({ workspaceRename: { request, error: errorText(error) } });
          this.toast("Workspace name change not confirmed. Open Workspaces to review or retry.", "error");
        }
        throw error;
      } finally {
        if (this.workspaceRenameWork === work) this.workspaceRenameWork = undefined;
        if (current()) void this.refetch();
      }
    });
    this.workspaceRenameWork = work;
    return work;
  }

  clearWorkspaceRename(request: NonNullable<State["workspaceRename"]>["request"]) {
    if (!this.workspaceRenameWork && this.state.workspaceRename?.request === request && this.state.workspaceRename.error) this.set({ workspaceRename: undefined });
  }

  private changePicture(png: string, calls: Api): Promise<T.PersonView> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    if (this.pictureWork) return this.pictureWork.png === png ? this.pictureWork.work : Promise.reject(Error("Wait for the current picture change to finish."));
    const o = this.state.overview;
    if (!o?.person) return calls.setPersonPicture(png);
    const change = { person: o.person, seq: o.seq, png };
    const current = () => this.alive && this.state.pictureChange?.person === change.person;
    this.set({ pictureChange: change });
    const work = Promise.resolve().then(async () => {
      try {
        const saved = await calls.setPersonPicture(png);
        if (current()) this.set({ pictureChange: { ...change, saved } });
        return saved;
      } catch (error) {
        if (current()) {
          this.set({ pictureChange: undefined }); // reveal current state, never restore an old snapshot
          void this.refetch(); // a lost reply may follow a successful save
        }
        throw error;
      } finally {
        if (this.pictureWork?.work === work) this.pictureWork = undefined;
      }
    });
    this.pictureWork = { png, work };
    return work;
  }

  private startDM(address: string, calls: Api): Promise<{ id: string }> {
    const person = this.state.overview?.person?.person;
    if (!this.alive || !person) return Promise.reject(Error("Your verified person is not available in this workspace."));
    const key = person + ":" + address, prior = this.dmStartWork.get(key);
    if (prior) return prior;
    // Each native create starts a separate signed conversation. Share only
    // concurrent identical input; never replay a creation after a lost reply.
    const work = Promise.resolve().then(() => {
      if (!this.alive || this.state.overview?.person?.person !== person) throw Error("This workspace or person changed before the chat could start.");
      return calls.newDM(address);
    }).finally(() => {
      if (this.dmStartWork.get(key) === work) this.dmStartWork.delete(key);
    });
    this.dmStartWork.set(key, work);
    return work;
  }

  createGroup(title: string, people: string[]): Promise<string> {
    const person = this.state.overview?.person?.person;
    if (!this.alive || !person) return Promise.reject(Error("Your verified person is not available in this workspace."));
    const old = this.state.groupCreation;
    const same = old?.person === person && old.request.title === title && JSON.stringify(old.request.people) === JSON.stringify(people);
    if (old && !same) return Promise.reject(Error("Review the group already being created before starting another."));
    if (this.groupCreationWork) return this.groupCreationWork;
    if (old?.done && old.id) return Promise.resolve(old.id);
    const request = old?.request || { title, people: [...people] };
    let pending: NonNullable<State["groupCreation"]> = old ? { ...old, error: undefined } : { request, person, remaining: [...people] };
    const current = () => this.alive && this.state.overview?.person?.person === person && this.state.groupCreation?.request === request;
    this.set({ groupCreation: pending });
    const work = Promise.resolve().then(async () => {
      try {
        if (!current()) throw Error("This workspace or person changed before the group could start.");
        // Creation is not replayed automatically after an uncertain response.
        // Once its real ID is known, retry only the remaining invitations.
        if (!pending.id) {
          const { id } = await this.api.newGroup(title);
          if (!current()) throw Error("This workspace or person changed while the group was being created.");
          pending = { ...pending, id }; this.set({ groupCreation: pending });
        }
        const id = pending.id;
        if (!id) throw Error("The group response did not include its conversation ID.");
        for (const target of pending.remaining) {
          if (!current()) throw Error("This workspace or person changed before the next invitation.");
          await this.api.inviteToGroup({ conv: id, person: target, history: {} });
          if (!current()) throw Error("This workspace or person changed while inviting people.");
          pending = { ...pending, remaining: pending.remaining.filter(p => p !== target) };
          this.set({ groupCreation: pending });
        }
        pending = { ...pending, done: true }; this.set({ groupCreation: pending });
        return id;
      } catch (error) {
        if (current()) this.set({ groupCreation: { ...pending, error: errorText(error) } });
        throw error;
      } finally {
        if (this.groupCreationWork === work) this.groupCreationWork = undefined;
        if (current()) void this.refetch();
      }
    });
    this.groupCreationWork = work;
    return work;
  }

  clearGroupCreation(request: NonNullable<State["groupCreation"]>["request"]) {
    const pending = this.state.groupCreation;
    if (!this.groupCreationWork && pending?.request === request && (pending.done || pending.error)) this.set({ groupCreation: undefined });
  }

  sendInvitations(conv: string, targets: InvitationTarget[]): Promise<InvitationSend> {
    const person = this.state.overview?.person?.person, old = this.state.invitationSends?.[conv];
    if (!this.alive || !person || !targets.length || targets.some(t => t.input.conv !== conv)) return Promise.reject(Error("This invitation's verified conversation or person is unavailable."));
    if (old && JSON.stringify(old.targets) !== JSON.stringify(targets)) return Promise.reject(Error("Review this chat's pending invitations before sending another."));
    const running = this.invitationWork.get(conv);
    if (running) return running;
    if (old?.done) return Promise.resolve(old);
    const captured = old?.targets || structuredClone(targets);
    let pending: InvitationSend = old ? { ...old, error: undefined } : { targets: captured, person, next: 0, agents: {} };
    const current = () => this.alive && this.state.overview?.person?.person === person && this.state.invitationSends?.[conv]?.targets === captured;
    const update = () => this.set({ invitationSends: { ...this.state.invitationSends, [conv]: pending } });
    update();
    const work = Promise.resolve().then(async () => {
      try {
        for (let i = pending.next; i < captured.length; i++) {
          if (!current()) throw Error("This workspace or person changed before the next invitation.");
          const target = captured[i];
          if (target.route === "agent") {
            // Native and browser APIs both verify the named host catalog and
            // exact history/permission scope before admitting this invitation.
            let value = pending.agents[i] || await this.api.inviteAgent(target.input);
            if (!current()) throw Error("This invitation's workspace or person changed.");
            pending = { ...pending, agents: { ...pending.agents, [i]: value } }; update();
            if (target.joinOwn && value.state !== "active" && !value.owner_invited && value.can_decide) {
              value = await this.api.decideAgent(value.pid, true);
              if (!current()) throw Error("This invitation's workspace or person changed.");
              pending = { ...pending, agents: { ...pending.agents, [i]: value } };
            }
          } else if (target.route === "guest") await this.api.inviteGuest(target.input);
          else await this.api.inviteToGroup(target.input);
          if (!current()) throw Error("This invitation's workspace or person changed.");
          pending = { ...pending, next: i + 1 }; update();
        }
        pending = { ...pending, done: true }; update();
        this.toast(invitationResult(pending), "ok");
        return pending;
      } catch (error) {
        if (current()) { pending = { ...pending, error: errorText(error) }; update(); this.toast("Invitation not confirmed. Reopen Bring in to review or retry the exact selection.", "error"); }
        throw error;
      } finally {
        if (this.invitationWork.get(conv) === work) this.invitationWork.delete(conv);
        if (current()) void this.refetch();
      }
    });
    this.invitationWork.set(conv, work);
    return work;
  }

  clearInvitationSend(conv: string, targets: InvitationTarget[]) {
    const pending = this.state.invitationSends?.[conv];
    if (this.invitationWork.has(conv) || pending?.targets !== targets || !pending.done && !pending.error) return;
    const invitationSends = { ...this.state.invitationSends }; delete invitationSends[conv];
    this.set({ invitationSends });
  }

  private deviceDecisionMatches(p: NonNullable<State["deviceDecisions"]>[string], o: T.Overview): boolean {
    const l = o.links?.find(l => l.id === p.link.id);
    return o.person?.person === p.person && !!l && l.state === "pending" &&
      l.address === p.link.address && l.fingerprint === p.link.fingerprint &&
      l.expires === p.link.expires && l.requested_at === p.link.requested_at;
  }

  private presentDeviceDecisions(s: State): State {
    if (!s.overview || !Object.keys(s.deviceDecisions || {}).length) return s;
    if (this.deviceDecisionState?.source === s) return this.deviceDecisionState.value;
    const links = s.overview.links?.filter(l => {
      const p = s.deviceDecisions?.[l.id];
      return !p || !this.deviceDecisionMatches(p, s.overview!);
    });
    // This removes an already-made local choice from attention, never adds a
    // device or grants membership. Only the authoritative roster does that.
    const value = { ...s, overview: { ...s.overview, links } };
    this.deviceDecisionState = { source: s, value };
    return value;
  }

  private pruneDeviceDecisions() {
    const o = this.state.overview;
    if (!o || !this.state.deviceDecisions) return;
    const pending = Object.entries(this.state.deviceDecisions).filter(([, p]) => this.deviceDecisionMatches(p, o));
    if (pending.length !== Object.keys(this.state.deviceDecisions).length) this.set({ deviceDecisions: Object.fromEntries(pending) });
  }

  private decideDevice(id: string, accept: boolean, calls: Api): Promise<{ note: string }> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const prior = this.deviceDecisionWork.get(id);
    if (prior) return prior.accept === accept ? prior.work : Promise.reject(Error("Wait for this device decision to finish."));
    if (this.state.deviceDecisions?.[id]) return Promise.reject(Error("Check the pending device decision before trying again."));
    const o = this.state.overview, link = o?.links?.find(l => l.id === id && l.state === "pending");
    if (!link || !o?.person?.person) return calls.decideDevice(id, accept);
    const change = { link, person: o.person.person, accept };
    const current = () => this.alive && this.state.deviceDecisions?.[id]?.link === link;
    this.set({ deviceDecisions: { ...this.state.deviceDecisions, [id]: change } });
    const work = Promise.resolve().then(async () => {
      try {
        const result = await calls.decideDevice(id, accept);
        if (current()) {
          this.set({ deviceDecisions: { ...this.state.deviceDecisions, [id]: { ...change, saved: true } } });
          this.pruneDeviceDecisions();
          void this.refetch();
        }
        return result;
      } catch (error) {
        if (current()) {
          const pending = { ...this.state.deviceDecisions }; delete pending[id];
          this.set({ deviceDecisions: pending }); // reveal current state, never replay an old snapshot
          void this.refetch(); // a lost reply may follow a stored decision
        }
        throw error;
      } finally {
        if (this.deviceDecisionWork.get(id)?.work === work) this.deviceDecisionWork.delete(id);
      }
    });
    this.deviceDecisionWork.set(id, { accept, work });
    return work;
  }

  private pruneDeviceRemovals() {
    const o = this.state.overview, changes = this.state.deviceRemovals;
    if (!o || !changes) return;
    const pending = Object.entries(changes).filter(([, p]) => o.person?.person === p.person &&
      o.person.devices?.some(d => d.address === p.device.address && d.fingerprint === p.device.fingerprint));
    if (pending.length !== Object.keys(changes).length) this.set({ deviceRemovals: Object.fromEntries(pending) });
  }

  private removeDevice(address: string, calls: Api): Promise<{ note: string }> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const prior = this.deviceRemovalWork.get(address);
    if (prior) return prior;
    if (this.state.deviceRemovals?.[address]) return Promise.reject(Error("Check the pending device removal before trying again."));
    const person = this.state.overview?.person, device = person?.devices?.find(d => d.address === address);
    if (!device || !person?.person) return calls.removeDevice(address);
    const change = { device, person: person.person };
    const current = () => this.alive && this.state.deviceRemovals?.[address]?.device === device;
    // Presentation only. Never remove the device from the authoritative roster
    // used by permission, routing, and identity selectors before confirmation.
    this.set({ deviceRemovals: { ...this.state.deviceRemovals, [address]: change } });
    const work = Promise.resolve().then(async () => {
      try {
        const result = await calls.removeDevice(address);
        if (current()) {
          this.set({ deviceRemovals: { ...this.state.deviceRemovals, [address]: { ...change, saved: true } } });
          this.pruneDeviceRemovals();
          void this.refetch();
        }
        return result;
      } catch (error) {
        if (current()) {
          const pending = { ...this.state.deviceRemovals }; delete pending[address];
          this.set({ deviceRemovals: pending });
          void this.refetch();
        }
        throw error;
      } finally {
        if (this.deviceRemovalWork.get(address) === work) this.deviceRemovalWork.delete(address);
      }
    });
    this.deviceRemovalWork.set(address, work);
    return work;
  }

  private presentGroupChanges(s: State): State {
    const renames = Object.values(s.groupChanges || {}).filter(p => p.change.action === "rename" && !p.error);
    if (!renames.length) return s;
    if (this.groupChangeState?.source === s) return this.groupChangeState.value;
    // Only the displayed name changes. Membership and roles stay verified.
    const names = new Map(renames.map(p => [p.change.conv, p.change.title]));
    const show = <V extends T.DMThread | T.DMSummary>(t: V): V => names.has(t.id) ? { ...t, title: names.get(t.id) } : t;
    const value = { ...s, dm: s.dm ? show(s.dm) : null,
      overview: s.overview ? { ...s.overview, dms: s.overview.dms?.map(show) || null } : null };
    this.groupChangeState = { source: s, value };
    return value;
  }

  private createTeam(input: T.TeamChange, calls: Api): Promise<T.TeamState> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const prior = this.teamCreationWork;
    if (prior) return this.state.teamCreation && JSON.stringify(prior.change) === JSON.stringify(input)
      ? prior.work : Promise.reject(Error("Wait for this people list to finish saving."));
    if (this.state.teamCreation && !this.state.teamCreation.error) return Promise.reject(Error("Check the pending people list before creating another."));
    const person = this.state.overview?.person?.person;
    if (!person) return Promise.reject(Error("Your verified person is not available in this workspace."));
    const change = { ...input }, pending = { change, person };
    const current = () => this.alive && this.state.teamCreation?.change === change;
    this.set({ teamCreation: pending });
    const work = Promise.resolve().then(async () => {
      try {
        const saved = await calls.changeTeam(change);
        if (current()) this.set({ teamCreation: { ...pending, saved } });
        return saved;
      } catch (error) {
        if (current()) this.set({ teamCreation: { ...pending, error: errorText(error) } });
        throw error;
      } finally {
        if (this.teamCreationWork?.work === work) this.teamCreationWork = undefined;
      }
    });
    this.teamCreationWork = { change, work };
    return work;
  }

  confirmTeamCreation(view: T.TeamsView) {
    const pending = this.state.teamCreation;
    if (!pending?.saved || !view.current || view.status !== "available") return;
    const saved = pending.saved, current = view.teams?.find(t => t.id === saved.id);
    if (current && !current.conflict && current.seq >= saved.seq) this.set({ teamCreation: undefined });
  }

  changePeopleList(input: T.TeamChange, original: T.TeamView): Promise<T.TeamState> {
    if (!this.alive || input.team !== original.id || input.op === "create") return Promise.reject(Error("This people list is no longer selected."));
    const id = original.id, prior = this.teamChangeWork.get(id);
    if (prior) return this.state.teamChanges?.[id] && JSON.stringify(prior.change) === JSON.stringify(input) ? prior.work : Promise.reject(Error("Wait for this list's pending change to finish."));
    const old = this.state.teamChanges?.[id], person = this.state.overview?.person?.person;
    if (old && !old.error) return Promise.reject(Error("Check the pending list change before trying again."));
    if (!person) return Promise.reject(Error("Your verified person is not available in this workspace."));
    const change = { ...input, ...(input.agent ? { agent: { ...input.agent } } : {}) }, pending = { change, person, original };
    const current = () => this.alive && this.state.teamChanges?.[id]?.change === change;
    this.set({ teamChanges: { ...this.state.teamChanges, [id]: pending } });
    const work = Promise.resolve().then(async () => {
      try {
        const saved = await this.api.changeTeam(change);
        if (current()) this.set({ teamChanges: { ...this.state.teamChanges, [id]: { ...pending, saved } } });
        return saved;
      } catch (error) {
        if (current()) this.set({ teamChanges: { ...this.state.teamChanges, [id]: { ...pending, error: errorText(error) } } });
        throw error;
      } finally {
        if (this.teamChangeWork.get(id)?.work === work) this.teamChangeWork.delete(id);
      }
    });
    this.teamChangeWork.set(id, { change, work });
    return work;
  }

  confirmTeamChanges(view: T.TeamsView) {
    if (!view.current || view.status !== "available" || !this.state.teamChanges) return;
    const version = (t: T.TeamView) => JSON.stringify([t.realm_id, t.id, t.version, t.seq, t.hash, t.name, t.members, t.managers, t.agents, t.archived, t.conflict]);
    const entries = Object.entries(this.state.teamChanges), keep = entries.filter(([id, p]) => {
      const current = view.teams?.find(t => t.id === id);
      return current ? current.seq < p.original.seq || version(current) === version(p.original) : view.truncated;
    });
    if (keep.length !== entries.length) this.set({ teamChanges: Object.fromEntries(keep) });
  }

  private changeGroup(input: T.GroupChange, calls: Api): Promise<T.GroupChangeResult> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const prior = this.groupChangeWork.get(input.conv);
    if (prior) return this.state.groupChanges?.[input.conv] && JSON.stringify(prior.change) === JSON.stringify(input) ? prior.work : Promise.reject(Error("Wait for this group change to finish."));
    const old = this.state.groupChanges?.[input.conv];
    if (old && !old.error) return Promise.reject(Error("Check the pending group change before trying again."));
    const original = this.state.views[input.conv] as T.DMThread | undefined || old?.original, person = this.state.overview?.person?.person;
    if (!original || original.kind !== "group" || !person) return calls.manageGroup(input);
    const change = { ...input }, summary = this.state.overview?.dms?.find(d => d.id === input.conv);
    const pending: PendingGroupChange = { change, original, person, summary: summary && groupVersion(summary) };
    const current = () => this.alive && this.state.groupChanges?.[input.conv]?.change === change;
    this.set({ groupChanges: { ...this.state.groupChanges, [input.conv]: pending } });
    // Leaving navigates now. A later reply never closes another selected chat.
    if (input.action === "leave" && this.state.open?.kind === "dm" && this.state.open.id === input.conv) {
      this.setPanel(false); this.close();
    }
    const work = Promise.resolve().then(async () => {
      try {
        const result = await calls.manageGroup(change);
        if (current()) {
          this.set({ groupChanges: { ...this.state.groupChanges, [input.conv]: { ...pending, saved: true, queued: result.queued } } });
          void this.refetch();
        }
        return result;
      } catch (error) {
        if (current()) {
          this.set({ groupChanges: { ...this.state.groupChanges, [input.conv]: { ...pending, error: errorText(error) } } });
          void this.refetch();
        }
        throw error;
      } finally {
        if (this.groupChangeWork.get(input.conv)?.work === work) this.groupChangeWork.delete(input.conv);
      }
    });
    this.groupChangeWork.set(input.conv, { change: input, work });
    return work;
  }

  async checkGroupChange(id: string) {
    const pending = this.state.groupChanges?.[id], before = this.state.views[id];
    if (!pending || !this.alive) return;
    const view = await this.api.dm(id);
    if (!this.alive || this.state.groupChanges?.[id] !== pending || this.state.views[id] !== before) return;
    if (view.id !== id) throw Error("The exact group could not be confirmed.");
    this.keep(id, view);
    if (this.state.open?.kind === "dm" && this.state.open.id === id && this.state.dm === before) this.set({ dm: this.state.views[id] as T.DMThread });
    void this.refetch();
  }

  private groupDecisionMatches(p: NonNullable<State["groupDecisions"]>[string], o: T.Overview): boolean {
    const i = o.group_invitations?.find(i => i.id === p.invitation.id), original = p.invitation;
    return o.person?.person === p.person && !!i && i.direction === "in" &&
      i.status === original.status && i.conv === original.conv && i.inviter === original.inviter &&
      i.target === original.target && i.title === original.title && JSON.stringify(i.history) === JSON.stringify(original.history);
  }

  private presentGroupDecisions(s: State): State {
    if (!s.overview || !Object.keys(s.groupDecisions || {}).length) return s;
    if (this.groupDecisionState?.source === s) return this.groupDecisionState.value;
    const keep = (i: T.GroupInvitationView) => {
      const p = s.groupDecisions?.[i.id];
      return !p || !this.groupDecisionMatches(p, s.overview!);
    };
    // Remove only decided attention. Acceptance is never group membership or
    // a history grant; the signed current group remains the sole authority.
    const value = { ...s, invitations: s.invitations.filter(keep),
      overview: { ...s.overview, group_invitations: s.overview.group_invitations?.filter(keep) } };
    this.groupDecisionState = { source: s, value };
    return value;
  }

  private pruneGroupDecisions() {
    const o = this.state.overview, changes = this.state.groupDecisions;
    if (!o || !changes) return;
    const pending = Object.entries(changes).filter(([, p]) => this.groupDecisionMatches(p, o));
    if (pending.length !== Object.keys(changes).length) this.set({ groupDecisions: Object.fromEntries(pending) });
  }

  private decideGroup(id: string, accept: boolean, calls: Api): Promise<{ recorded: boolean }> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const prior = this.groupDecisionWork.get(id);
    if (prior) return prior.accept === accept ? prior.work : Promise.reject(Error("Wait for this group decision to finish."));
    if (this.state.groupDecisions?.[id]) return Promise.reject(Error("Check the pending group decision before trying again."));
    const o = this.state.overview, invitation = o?.group_invitations?.find(i => i.id === id && i.direction === "in" && ["pending", "stale"].includes(i.status));
    if (!invitation || !o?.person?.person) return calls.decideGroup(id, accept);
    const change = { invitation, person: o.person.person, accept };
    const current = () => this.alive && this.state.groupDecisions?.[id]?.invitation === invitation;
    this.set({ groupDecisions: { ...this.state.groupDecisions, [id]: change } });
    const work = Promise.resolve().then(async () => {
      try {
        const result = await calls.decideGroup(id, accept);
        if (current()) {
          this.set({ groupDecisions: { ...this.state.groupDecisions, [id]: { ...change, saved: true } } });
          this.pruneGroupDecisions();
          void this.refetch();
        }
        return result;
      } catch (error) {
        if (current()) {
          const pending = { ...this.state.groupDecisions }; delete pending[id];
          this.set({ groupDecisions: pending });
          void this.refetch();
        }
        throw error;
      } finally {
        if (this.groupDecisionWork.get(id)?.work === work) this.groupDecisionWork.delete(id);
      }
    });
    this.groupDecisionWork.set(id, { accept, work });
    return work;
  }

  private presentReminders(s: State): State {
    if (!s.overview || !Object.keys(s.reminderChanges).length) return s;
    if (this.reminderState?.source === s) return this.reminderState.value;
    const cached = this.reminderOverviewCache;
    let overview = s.overview;
    if (cached?.source === overview && cached.changes === s.reminderChanges) overview = cached.value;
    else {
      for (const p of Object.values(s.reminderChanges)) {
        const current = overview.reminders?.find(r => r.message === p.intent.message);
        // A newer generation wins even when another device chose the same time.
        // Hosts without generation metadata use the more conservative sequence.
        if (p.status === "failed" || reminderVersion(current) !== p.original ||
            (!current?.revision && overview.seq !== p.seq)) continue;
        overview = this.reminderOverview(overview, p.intent.message, p.reminder);
      }
      this.reminderOverviewCache = { source: s.overview, changes: s.reminderChanges, value: overview };
    }
    const value = overview === s.overview ? s : { ...s, overview };
    this.reminderState = { source: s, value };
    return value;
  }

  private presentTopics(): State {
    const s = this.state;
    if (!Object.keys(s.topicPreviews).length) return s;
    if (this.topicState?.source === s) return this.topicState.value;
    let views = s.views, overview = s.overview;
    const cached = this.topicViews;
    if (cached?.source === s.views && cached.overview === s.overview && cached.previews === s.topicPreviews) {
      views = cached.views; overview = cached.shownOverview;
    } else {
      for (const p of Object.values(s.topicPreviews)) {
        const c = p.change;
        if (c.conv) {
          const v = views[c.conv] as T.DMThread | undefined;
          if (v?.topics) views = { ...views, [c.conv]: { ...v, topics: v.topics.map(t => t.id === c.id ? presentTopic(t, p) : t) } };
        } else {
          const v = views[c.id] as T.Thread | undefined;
          if (v?.topic) views = { ...views, [c.id]: { ...v, topic: presentTopic(v.topic, p) } };
          if (overview) overview = { ...overview,
            threads: overview.threads?.map(t => t.id === c.id ? presentTopic(t, p) : t) || null,
            topics: overview.topics?.map(t => t.latest.id === c.id ? { ...t, latest: presentTopic(t.latest, p) } : t) };
        }
      }
      this.topicViews = { source: s.views, overview: s.overview, previews: s.topicPreviews, views, shownOverview: overview };
    }
    const value = { ...s, views, overview, dm: s.dm ? views[s.dm.id] as T.DMThread ?? s.dm : null, thread: s.thread ? views[s.thread.id] as T.Thread ?? s.thread : null };
    this.topicState = { source: s, value };
    return value;
  }

  private presentMessages(s: State): State {
    if (!Object.keys(s.messageChanges).length) return s;
    if (this.messageState?.source === s) return this.messageState.value;
    let views = s.views;
    const cached = this.messageViews;
    if (cached?.source === s.views && cached.changes === s.messageChanges) views = cached.value;
    else {
      for (const p of Object.values(s.messageChanges)) {
        const v = views[p.view];
        if (!v?.messages) continue;
        const at = v.messages.findIndex(m => messageMatches(m, p.control));
        if (at < 0) continue;
        const m = v.messages[at], shown = presentMessage(m, p);
        if (shown === m) continue;
        const messages = [...v.messages]; messages[at] = shown;
        views = { ...views, [v.id]: { ...v, messages } as View };
      }
      this.messageViews = { source: s.views, changes: s.messageChanges, value: views };
    }
    const value = views === s.views ? s : { ...s, views,
      dm: s.dm ? views[s.dm.id] as T.DMThread ?? s.dm : null,
      thread: s.thread ? views[s.thread.id] as T.Thread ?? s.thread : null };
    this.messageState = { source: s, value };
    return value;
  }

  private changeMessage(action: MessageChange["action"], control: T.ControlAction, calls: Api): Promise<{ note: string }> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const key = messageChangeKey(control), prior = this.messageWork.get(key);
    if (prior) {
      if (prior.action !== action || prior.text !== control.text) return Promise.reject(Error("The previous message change is still pending. Your edit is kept; try again after it finishes."));
      return prior.work;
    }
    const selected = this.state.open;
    const view = control.conv || (selected?.kind === "thread" ? selected.id : "");
    const original = this.state.views[view]?.messages?.find(m => messageMatches(m, control));
    const call = () => action === "edit" ? calls.edit(control) : calls.retract(control);
    // The server still validates every action. Never manufacture local authority.
    if (!original || !original.can?.includes(action)) return call();
    const p: MessageChange = { action, control: { ...control }, view, original: messageVersion(original), status: "pending" };
    this.set({ messageChanges: { ...this.state.messageChanges, [key]: p } });
    const work = Promise.resolve().then(async () => {
      try {
        const result = await call();
        if (this.alive) this.set({ messageChanges: { ...this.state.messageChanges, [key]: { ...p, status: "saved" } } });
        return result;
      } catch (error) {
        if (this.alive) this.set({ messageChanges: { ...this.state.messageChanges, [key]: { ...p, status: "failed", error: errorText(error) } } });
        throw error;
      } finally {
        this.messageWork.delete(key);
        // Read current truth even after a lost write response. Never replay work.
        if (this.alive) {
          void this.loadOpen(this.state.open?.id === view ? this.state.open : { kind: control.conv ? "dm" : "thread", id: view }, true);
          void this.load(false);
        }
      }
    });
    this.messageWork.set(key, { action, text: control.text, work });
    return work;
  }

  discardFailedEdit(control: T.ControlAction) {
    const key = messageChangeKey(control), p = this.state.messageChanges[key];
    if (p?.action !== "edit" || p.status !== "failed" || this.messageWork.has(key)) return;
    const messageChanges = { ...this.state.messageChanges }; delete messageChanges[key];
    this.set({ messageChanges });
  }
  get returningToReview() { return !!this.state.open && this.unreadReturn === this.state.open; }
  subscribe = (fn: () => void) => { this.subs.add(fn); return () => this.subs.delete(fn); };
  private set(patch: Partial<State>) {
    if (!this.alive) return;
    if (Object.keys(patch).every(key => Object.is(this.state[key as keyof State], patch[key as keyof State]))) return;
    this.state = { ...this.state, ...patch };
    if (this.state.groupCreation && patch.overview && patch.overview.person?.person !== this.state.groupCreation.person) {
      this.state = { ...this.state, groupCreation: undefined };
      this.groupCreationWork = undefined;
    }
    if (this.state.invitationSends && patch.overview) {
      const entries = Object.entries(this.state.invitationSends), keep = entries.filter(([, p]) => p.person === patch.overview?.person?.person);
      if (keep.length !== entries.length) {
        const kept = new Set(keep.map(([id]) => id));
        for (const [id] of entries) if (!kept.has(id)) this.invitationWork.delete(id);
        this.state = { ...this.state, invitationSends: Object.fromEntries(keep) };
      }
    }
    if (this.state.personRename && patch.overview && this.state.personRename.request.person !== patch.overview.person?.person) {
      this.state = { ...this.state, personRename: undefined }; this.personRenameWork = undefined;
    }
    if (this.state.personCreation && patch.overview) {
      const { request } = this.state.personCreation, o = patch.overview;
      if (o.person || !o.persons || o.role !== "unset" || o.link?.state === "pending" || o.me.address !== request.address || o.me.fingerprint !== request.fingerprint) {
        this.state = { ...this.state, personCreation: undefined }; this.personCreationWork = undefined;
      }
    }
    if (this.state.workspaceRename && patch.overview) {
      const { request } = this.state.workspaceRename, o = patch.overview;
      if (o.me.address !== request.address || o.me.fingerprint !== request.fingerprint || o.person?.person !== request.person) {
        this.state = { ...this.state, workspaceRename: undefined }; this.workspaceRenameWork = undefined;
      }
    }
    if (this.state.teamCreation && patch.overview && patch.overview.person?.person !== this.state.teamCreation.person) this.state = { ...this.state, teamCreation: undefined };
    if (this.state.teamChanges && patch.overview) {
      const entries = Object.entries(this.state.teamChanges), keep = entries.filter(([, p]) => p.person === patch.overview?.person?.person);
      if (keep.length !== entries.length) this.state = { ...this.state, teamChanges: Object.fromEntries(keep) };
    }
    if (this.state.groupChanges && (patch.views || patch.overview)) {
      const entries = Object.entries(this.state.groupChanges);
      const keep = entries.filter(([id, p]) => {
        const view = this.state.views[id] as T.DMThread | undefined;
        const summary = this.state.overview?.dms?.find(d => d.id === id);
        return this.state.overview?.person?.person === p.person &&
          (!view || groupVersion(view) === groupVersion(p.original)) &&
          (p.summary === undefined || !!summary && groupVersion(summary) === p.summary);
      });
      if (keep.length !== entries.length) this.state = { ...this.state, groupChanges: Object.fromEntries(keep) };
    }
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
    this.syncScheduleSaves();
    if (this.host.platform === "android") {
      document.addEventListener("visibilitychange", this.mobileVisible);
      this.offNativeVisibility = this.host.android?.onVisibility(this.mobileVisible);
      window.addEventListener("focus", this.mobileVisible);
    }
    try { this.listen(); } catch { /* the first load fails and shows it */ }
    await Promise.all([this.load(true), this.refreshOpen()]);
    await this.retryMessageLanding(true);
  }

  stop() {
    ++this.unreadLanding;this.unreadReturn=null;
    this.dismissMessageLanding();
    const saved=this.host.workspaces?.state?.(this.host.workspace.id);
    if(saved)saved.comicDrafts={handle:this.host.workspace.handle,drafts:this.state.drafts,open:this.state.open,dm:this.state.dm,thread:this.state.thread,tab:this.state.tab};
    this.alive = false;
    if (this.host.platform === "android") {
      document.removeEventListener("visibilitychange", this.mobileVisible);
      this.offNativeVisibility?.(); this.offNativeVisibility = undefined;
      window.removeEventListener("focus", this.mobileVisible);
    }
    this.reacting.clear();
    this.dismissing.clear();
    this.pictureWork = undefined;
    this.deviceDecisionWork.clear();
    this.deviceRemovalWork.clear();
    this.groupDecisionWork.clear();
    this.groupChangeWork.clear();
    this.teamCreationWork = undefined;
    this.teamChangeWork.clear();
    this.dmStartWork.clear();
    this.groupCreationWork = undefined;
    this.invitationWork.clear();
    this.personRenameWork = undefined;
    this.personCreationWork = undefined;
    this.workspaceRenameWork = undefined;
    this.sends.dispose();
    this.scheduled.dispose();
    this.stopListen?.();
    this.stopListen = null;
  }

  isActive() { return this.alive; }

  private syncScheduleSaves() {
    if(!this.alive)return;
    for(const slot of this.scheduled.takeConfirmed())this.setDraft(slot.conv,clearScheduledDraft(this.draft(slot.conv),slot.attempt!));
    this.set({scheduleSaves:this.scheduled.snapshot()});
  }
  beginScheduleSave(conv:string,attempt:ScheduleAttempt) {if(!this.alive)throw Error("This workspace is closed. Your draft is kept.");this.scheduled.begin(conv,attempt);}
  // A late response settles only the shared slot; its current renderer, or the
  // next start, owns guarded draft clearing. A stopped Store never reactivates.
  settleScheduleSave(conv:string,id:string,status:ScheduleSave["status"],note?:string) {this.scheduled.settle(conv,id,status,note);}
  forgetScheduleSave(conv:string,id:string) {if(this.alive)this.scheduled.forget(conv,id);}


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
      if (e.type === "change") {
        if (e.hint === "typing") void this.loadTyping();
        else if (e.hint === "presence") { void this.load(false, false); void this.loadTyping(); }
        else void this.refetch();
      }
      else if (e.type === "restart") { this.set({ conn: "updating" }); void this.recover(recovery.update); }
      else { this.set({ conn: "lost" }); void this.recover(recovery.missed); }
    });
  }

  // refetch loads what is shown once per burst of changes: changes that
  // arrive while it loads cause one more load, not one each.
  async refetch(retryLegacy = false): Promise<void> {
    await Promise.all([this.load(false), this.refreshOpen()]);
    await this.retryMessageLanding(!retryLegacy);
  }

  // A slow workspace overview never holds the open chat's receipt/arrival
  // behind it. Each lane coalesces independently, including continuous traffic.
  refreshOpen(): Promise<boolean> {
    this.openAgain = true;
    if (!this.openRefresh) this.openRefresh = (async () => {
      try {
        let loaded = false;
        do { this.openAgain = false; loaded = await this.loadOpen(); }
        while (this.openAgain && this.alive);
        return loaded;
      } finally { this.openRefresh = null; }
    })();
    return this.openRefresh;
  }

  // load is that one load under way. The first one (start) also says why it
  // failed; a later failure waits for the next change, or the person.
  private load(first: boolean, catalogs = true): Promise<void> {
    this.again = true;
    this.catalogsAgain ||= catalogs;
    if (!this.refresh) this.refresh = (async () => {
      try {
        do {
          this.again = false;
          const refreshCatalogs = this.catalogsAgain;
          this.catalogsAgain = false;
          try { await this.reload(first, refreshCatalogs); }
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
    const source = c.conv ? (this.state.views[c.conv] as T.DMThread | undefined)?.topics?.find(t => t.id === c.id)
      : (this.state.views[c.id] as T.Thread | undefined)?.topic || this.state.overview?.threads?.find(t => t.id === c.id);
    this.set({ topicBusy: { ...this.state.topicBusy, [key]: what },
      ...(source && !c.root ? { topicPreviews: { ...this.state.topicPreviews, [key]: { action: what, change: { ...c }, original: topicVersion(source) } } } : {}) });
    const selected = this.state.open;
    const selectedTarget = selected && (c.conv
      ? selected.kind === "dm" && selected.id === c.conv
      : selected.kind === "thread" && selected.id === c.id);
    const work = (async () => {
      try {
        const result = await this.api.changeTopic(what, c);
        if (!this.alive) return undefined;
        const overview = this.load(false);
        if (selectedTarget) {
          if (this.state.open !== selected || this.state.pending) return undefined;
          if (!await this.refreshOpen()) return undefined;
          if (!this.alive || this.state.open !== selected || this.state.pending) return undefined;
        } else {
          await overview;
          if (!this.alive || this.state.open !== selected || this.state.pending) return undefined;
        }
        return result;
      } catch (e) {
        if (errorText(e) !== "stale or disconnected workspace") this.toast(errorText(e), "error");
        return undefined;
      } finally {
        this.topicChanges.delete(key);
        const topicBusy = { ...this.state.topicBusy }; delete topicBusy[key];
        const topicPreviews = { ...this.state.topicPreviews }; delete topicPreviews[key];
        this.set({ topicBusy, topicPreviews });
      }
    })();
    this.topicChanges.set(key, work);
    return work;
  }

  private async reload(first: boolean, catalogs = true) {
    const request = ++this.overviewRequest;
    const o = await this.api.overview();
    if (!this.alive || request !== this.overviewRequest) return false;
    const patch: Partial<State> = { ...(catalogs ? {contentRevision:this.state.contentRevision+1}:{}), overview: share(this.state.overview, this.attentionOverview(o)), loadError: "", conn: "live" };
    if (first || !this.state.version) patch.version = o.version;
    else if (o.version && o.version !== this.state.version) patch.newVersion = o.version;
    this.set(patch);
    const picture = this.state.pictureChange;
    if (picture?.saved && (o.seq > picture.seq || o.person?.picture === picture.saved.picture)) this.set({ pictureChange: undefined });
    const rename = this.state.personRename;
    if (rename && !this.personRenameWork && o.person?.person === rename.request.person &&
      (o.person.label === rename.request.label || rename.saved && o.person.label !== rename.request.original)) this.set({ personRename: undefined });
    const workspaceRename = this.state.workspaceRename;
    if (workspaceRename && !this.workspaceRenameWork && o.workspace &&
      (o.workspace.name === workspaceRename.request.name || workspaceRename.saved && o.workspace.name !== workspaceRename.request.original)) this.set({ workspaceRename: undefined });
    this.pruneDeviceDecisions();
    this.pruneDeviceRemovals();
    this.pruneGroupDecisions();
    this.pruneDismissals();
    if (o.groups) this.set({ invitations: share(this.state.invitations, o.group_invitations || []) });
    if (catalogs) void this.loadAgentNames();
    return true;
  }

  // A small committed result can arrive before an older workspace build.
  // Preserve only its exact attention scope until the overview catches up.
  private attentionOverview(o: T.Overview): T.Overview {
    for (const [key, read] of this.confirmedReads) {
      if (o.seq >= read.seq) this.confirmedReads.delete(key);
      else o = this.readOverview(o, read);
    }
    for (const [message, result] of this.confirmedReminders) {
      if (o.seq >= result.seq) this.confirmedReminders.delete(message);
      else o = this.reminderOverview(o, message, result.reminder);
    }
    return o;
  }

  private readOverview(o: T.Overview, read: ConfirmedRead): T.Overview {
    if (read.group && read.archived_unread !== undefined) {
      const group = o.topics?.find(t => JSON.stringify([t.peer, directChatKey(t.latest)]) === read.group);
      if (!group) return o;
      const delta = read.archived_unread - group.archived_unread;
      return { ...o,
        ...(typeof o.unread_total === "number" ? { unread_total: Math.max(0, o.unread_total + delta) } : {}),
        ...(typeof o.alert_total === "number" ? { alert_total: Math.max(0, o.alert_total + delta) } : {}),
        topics: o.topics!.map(t => t === group ? { ...t, archived_unread: read.archived_unread!,
          ...(t.latest.id === read.id ? { latest: { ...t.latest, unread: read.unread } } : {}) } : t),
      };
    }
    const summary = read.kind === "dm" ? o.dms?.find(d => d.id === read.id)
      : o.threads?.find(t => t.id === read.id) || o.topics?.find(t => t.latest.id === read.id)?.latest;
    if (!summary || summary.unread === read.unread) return o;
    const delta = read.unread - summary.unread;
    const totals = {
      ...(typeof o.unread_total === "number" ? { unread_total: Math.max(0, o.unread_total + delta) } : {}),
      ...(typeof o.alert_total === "number" && !(read.kind === "dm" && o.notify?.mutes?.includes(read.id)) ? { alert_total: Math.max(0, o.alert_total + delta) } : {}),
    };
    if (read.kind === "dm") return { ...o, ...totals, dms: o.dms!.map(d => d.id === read.id ? { ...d, unread: read.unread } : d) };
    return { ...o, ...totals,
      threads: o.threads?.map(t => t.id === read.id ? { ...t, unread: read.unread } : t) || null,
      topics: o.topics?.map(t => t.latest.id === read.id ? { ...t, latest: { ...t.latest, unread: read.unread },
        ...(t.latest.state === "archived" ? { archived_unread: Math.max(0, t.archived_unread + delta) } : {}) } : t),
    };
  }

  private confirmReadOverview(o: NonNullable<Open>, view: View) {
    const overview = this.state.overview, seq = view.seq;
    if (!overview || typeof seq !== "number" || seq < overview.seq) return;
    const group = o.kind === "thread" && !overview.threads?.some(t => t.id === o.id) ? this.archivedRead(view) : undefined;
    const read = { kind: o.kind, id: o.id, seq, unread: (view.messages || []).filter(m => m.unread).length, ...group };
    const key = group ? "archived:" + group.group : JSON.stringify([o.kind, o.id]);
    if ((this.confirmedReads.get(key)?.seq || 0) > seq) return;
    this.confirmedReads.set(key, read);
    this.set({ overview: share(overview, this.readOverview(overview, read)) });
  }

  private archivedRead(view: View) {
    const t = view as T.Thread, group = t.peer_topics;
    if (t.topic?.state !== "archived" || t.topic.notice_only || !group || group.peer !== t.peer || t.topic.peer !== t.peer ||
        directChatKey(group.latest) !== directChatKey(t.topic) || !Number.isSafeInteger(group.archived_unread) || group.archived_unread < 0) return undefined;
    return { group: JSON.stringify([group.peer, directChatKey(group.latest)]), archived_unread: group.archived_unread };
  }

  private reminderOverview(o: T.Overview, message: string, reminder: T.ReminderView | null): T.Overview {
    const reminders = (o.reminders || []).filter(r => r.message !== message);
    if (reminder) reminders.push(reminder);
    reminders.sort((a, b) => a.due.localeCompare(b.due) || a.message.localeCompare(b.message));
    return { ...o, reminders };
  }

  changeReminder(intent: ReminderIntent, ok?: string): Promise<ReminderResult | undefined> {
    if (!this.alive) return Promise.resolve(undefined);
    const key = intent.message, identity = JSON.stringify([intent.action, intent.due]);
    const running = this.reminderWork.get(key);
    if (running) {
      if (running.intent === identity) return running.work;
      this.toast("Wait for this reminder change to finish.", "error");
      return Promise.resolve(undefined);
    }
    if (this.state.reminderChanges[key]?.status === "confirming") {
      this.toast("Check the saved reminder before changing it again.", "error");
      return Promise.resolve(undefined);
    }
    const o = this.state.overview, original = o?.reminders?.find(r => r.message === key);
    const expected = original?.revision !== undefined ? { revision: original.revision, writer: original.writer } : original ? undefined : { revision: 0 };
    const view = this.state.open && this.state.views[this.state.open.id];
    const message = view?.messages?.find(m => m.id === key);
    const reminder = intent.action === "set" ? { ...original, message: key, due: intent.due!, overdue: false,
      title: original?.title || intent.title || "A message", from: original?.from || message?.from || o?.me.address || "",
      conv: original?.conv || (message && this.state.open?.kind === "dm" ? this.state.open.id : undefined) } : null;
    this.set({ reminderChanges: { ...this.state.reminderChanges, [key]: {
      intent: { ...intent, title: intent.title || original?.title }, original: reminderVersion(original), seq: o?.seq || 0, reminder, status: "saving",
    } } });
    const work = (async () => {
      try {
        const result = await (intent.action === "set" ? this.api.remind(key, new Date(intent.due!), expected) :
          intent.action === "done" ? this.api.remindDone(key, expected) : this.api.remindCancel(key, expected));
        if (!this.alive) return undefined;
        const o = this.state.overview;
        if (typeof result.seq === "number" && result.message === key && result.reminder !== undefined) {
          const reminderChanges = { ...this.state.reminderChanges }; delete reminderChanges[key];
          let overview = o;
          if (o && result.seq >= o.seq && result.seq >= (this.confirmedReminders.get(key)?.seq || 0)) {
            this.confirmedReminders.set(key, { seq: result.seq, reminder: result.reminder });
            overview = share(o, this.reminderOverview(o, key, result.reminder));
          }
          this.set({ overview, reminderChanges });
          void this.refetch();
        } else {
          this.set({ reminderChanges: { ...this.state.reminderChanges, [key]: { ...this.state.reminderChanges[key], status: "confirming" } } });
          void this.confirmReminder(key);
        }
        if (ok) this.toast("Reminder change saved", "ok");
        return result;
      } catch (e) {
        if (this.alive) this.set({ reminderChanges: { ...this.state.reminderChanges, [key]: {
          ...this.state.reminderChanges[key], status: "failed", error: errorText(e),
        } } });
        if (this.alive) void this.refetch();
        return undefined;
      } finally { this.reminderWork.delete(key); }
    })();
    this.reminderWork.set(key, { intent: identity, work });
    return work;
  }

  async confirmReminder(message: string) {
    const change = this.state.reminderChanges[message];
    if (change?.status !== "confirming") return;
    try {
      const applied = await this.reload(false, false);
      if (applied && this.state.reminderChanges[message] === change) {
        const reminderChanges = { ...this.state.reminderChanges }; delete reminderChanges[message];
        this.set({ reminderChanges });
      }
    } catch (e) {
      if (this.state.reminderChanges[message] === change) this.set({ reminderChanges: {
        ...this.state.reminderChanges, [message]: { ...change, error: errorText(e) },
      } });
    }
  }

  discardReminderError(message: string) {
    if (this.state.reminderChanges[message]?.status !== "failed") return;
    const reminderChanges = { ...this.state.reminderChanges }; delete reminderChanges[message];
    this.set({ reminderChanges });
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
          await this.refreshOpen();
          this.listen();
          await this.retryMessageLanding();
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
    // A row's last_id identifies its actual root/topic, not an exact jump
    // past older unread. Quotes and notifications use open/openMessage.
    if (o?.kind === "dm" && o.focus) return this.open({ ...o, focus: undefined, preview: o.focus });
    if (o?.kind !== "dm" || o.focus) return this.open(o);
    const dm = this.state.overview?.dms?.find(d => d.id === o.id);
    const main = dm && this.personMain(dm);
    if (!main) return this.open(o);
    return this.open({ kind: "dm", id: main });
  }

  // A new destination appears on input; cached content is shown immediately,
  // otherwise its header/loading view appears while the exact request runs.
  async open(o: Open) {
    ++this.unreadLanding;this.unreadReturn=null;
    this.dismissMessageLanding();
    if (!o) { this.close(); return; }
    if (!o.focus) o = { ...o, unread: true };
    if (o.focus || o.kind === "dm" && o.preview) o = { ...o, focusSeq: ++this.focusSeq };
    if (o.kind === "thread" && !o.peer) {
      const id = o.id;
      const peer = (this.state.views[id] as T.Thread | undefined)?.peer || (this.state.overview?.threads || []).find((t) => t.id === id)?.peer;
      if (peer) o = { ...o, peer };
    }
    const cur = this.state.open;
    const cached = this.state.views[o.id];
    const differentPending = this.state.pending && (this.state.pending.kind !== o.kind || this.state.pending.id !== o.id);
    if (!cur || cur.kind !== o.kind || cur.id !== o.id || differentPending) this.beginReadVisit(undefined, o);
    else if (this.readAcknowledged.entering === cur) this.readAcknowledged.entering = o;
    if ((cur && cur.kind === o.kind && cur.id === o.id) || cached) {
      this.set({ tab: "chats", open: o, pending: null, ...this.viewPatch(o, cached ?? null, cur?.id === o.id) });
      await this.loadOpen(o);
    } else {
      this.set({ tab: "chats", open: o, pending: o, dm: null, thread: null, typing: null });
      await this.loadOpen(o);
    }
    if (this.state.open === o && !this.refreshed.has(o.id)) { // once per open: receipts the server still holds
      this.refreshed.add(o.id);
      this.api.refresh(o.id).catch(() => {});
    }
  }

  // Topic/count entry uses the loaded conversation, with no history lookup.
  // Explicit reply/search/notification focus is kept by their ordinary paths.
  async openUnread(o: NonNullable<Open>, topic = "") {
    ++this.unreadLanding;this.unreadReturn=null;
    if (o.kind === "dm") this.setDraft(o.id, { ...this.draft(o.id), topic: topic || undefined, newTopic: false }, true);
    const next = { ...o, unread: true, focusSeq: ++this.focusSeq };
    if (this.state.open?.id === o.id && this.state.open.kind === o.kind && this.state.views[o.id] && !this.state.pending && !this.readAcknowledged.entering) {
      this.dismissMessageLanding();
      this.set({ open: next, tab: "chats" });
    } else await this.open(next);
  }

  // Review already binds the exact local scope: one GET, no overview search,
  // remote refresh, or action. Only the existing viewport can acknowledge it.
  async openUnreadRow(row: T.UnreadRow): Promise<boolean> {
    const request=++this.unreadLanding,previous=this.state.open,pending=this.state.pending,tab=this.state.tab;
    const current=()=>this.alive&&request===this.unreadLanding&&this.state.open===previous&&this.state.pending===pending&&this.state.tab===tab;
    try{
      const chat=row.kind==="chat";
      if((!chat&&row.kind!=="device")||!row.id||!row.author||!row.lid||(chat?!row.conv:!row.peer||!row.thread))throw Error("That unread destination is no longer available.");
      const view:View=chat?await this.api.dm(row.conv!):await this.api.thread(row.thread!);
      if(!current())return false;
      const message=(view.messages||[]).find(m=>m.id===row.id&&m.dir===row.dir);
      const author=message&&(chat?(message as T.DMMessage).send_group_author||(message as T.DMMessage).claimed_key:(message as T.Message).from_key||(row.dir==="out"?this.state.overview?.me.fingerprint:undefined));
      if(view.id!==(chat?row.conv:row.thread)||(!chat&&(view as T.Thread).peer!==row.peer)||!message||message.deleted||(chat?!author||author!==row.author:!!author&&author!==row.author)||((message as T.DMMessage).lid||message.id)!==row.lid||(chat&&((message as T.DMMessage).topic||"")!==(row.topic||"")))throw Error("That unread message changed or is no longer available here. Refresh Review and choose it again.");
      this.dismissMessageLanding();
      const o:NonNullable<Open>=chat?{kind:"dm",id:row.conv!,focus:row.id,focusSeq:++this.focusSeq}:{kind:"thread",id:row.thread!,peer:row.peer,focus:row.id,focusSeq:++this.focusSeq};
      this.beginReadVisit(view);
      this.unreadReturn=o;
      this.set({...this.viewPatch(o,view,this.state.open?.id===o.id),open:o,pending:null,tab:"chats"});
      return true;
    }catch(error){if(current())this.toast(errorText(error),"error");return false;}
  }

  // viewPatch: the state that shows o with view v (null: not loaded yet).
  async openLinkTarget(id: string, kind: 'topic' | 'thread', context?: OpenContext) {
    ++this.unreadLanding;this.unreadReturn=null;
    this.dismissMessageLanding();
    const view=kind==='topic'?await this.api.dm(id):await this.api.thread(id);
    if(!this.alive||view.id!==id)throw new Error('That exact conversation is not available on this device.');
    if(kind==='topic'){
      const dm=view as T.DMThread, topic=context?.topic;
      if(dm.frozen||(topic!=='main'&&!dm.topics?.some(t=>t.id===topic))||(topic==='main'&&(dm.role==='visitor'||dm.role==='human_guest')&&!dm.main_topic))throw new Error('That exact topic is not available on this device.');
      this.setDraft(id,{...this.draft(id),topic:topic==='main'?'':topic,newTopic:false});
    }else{
      const thread=view as T.Thread;
      if(thread.topic?.id!==id||!thread.messages?.some(m=>m.id===id&&!m.deleted))throw new Error('That exact conversation root is not available on this device.');
    }
    const o: NonNullable<Open>=kind==='topic'?{kind:'dm',id}:{kind:'thread',id,peer:(view as T.Thread).peer};
    this.beginReadVisit(view);
    this.set({...this.viewPatch(o,view,this.state.open?.id===id),open:o,pending:null,tab:'chats'});
    // Link landing never invokes ordinary open's remote history refresh.
  }

  private viewPatch(o: NonNullable<Open>, v: View | null, same: boolean): Partial<State> {
    if (same && !v) return {};
    if (v) v = share(this.state.views[o.id], { ...v, messages: this.sends.merge<T.DMMessage | T.Message>(o.id, v.messages || []) } as View);
    // A request arrow chooses its actual topic, including Main flow. Keep
    // unsent text, staged files and reply intact; only the displayed topic changes.
    // Resolve an ordinary preview only on the fresh entry. A prefetched
    // copy may still name the message's previous topic.
    const focus = o.kind === "dm" ? o.focus || (!this.readAcknowledged.entering ? o.preview : undefined) : undefined;
    const target = o.kind === "dm" && focus && o.focusSeq !== this.topicFocusSeq && v ? messageTarget((v as T.DMThread).messages || [], focus) : undefined;
    if (target) this.topicFocusSeq = o.focusSeq || 0;
    const draft = this.draft(o.id);
    const drafts = target ? { drafts: { ...this.state.drafts, [o.id]: { ...draft, topic: target.topic || "", newTopic: false } } } : {};
    const typing = same ? {} : { typing: null };
    const views = v && this.state.views[o.id] !== v ? { views: { ...this.state.views, [o.id]: v } } : {};
    const readVisit = v ? this.prepareReadScope(o, v, drafts.drafts?.[o.id] || draft) : null;
    return o.kind === "dm" ? { dm: (v as T.DMThread | null), thread: null, ...views, ...typing, ...drafts, readVisit } : { thread: (v as T.Thread | null), dm: null, ...views, ...typing, readVisit };
  }

  // prefetch loads a conversation the person is about to open (a press or a
  // pause over its row), so that opening it shows it at once; an open that
  // follows uses the same request. A read only: nothing is marked read by
  // it. It answers the view (loaded before, or now), or null.
  private fetching = new Map<string, Promise<View>>();
  private latestFetch = new Map<string, Promise<View>>();
  prefetch(o: Open): Promise<View | null> {
    if (!o) return Promise.resolve(null);
    const have = this.state.views[o.id];
    if (have) return Promise.resolve(have);
    let f = this.fetching.get(o.id);
    if (!f) {
      f = (o.kind === "dm" ? this.api.dm(o.id) : this.api.thread(o.id)) as Promise<View>;
      this.fetching.set(o.id, f);
      this.latestFetch.set(o.id, f);
      f.then((v) => { if (this.latestFetch.get(o.id) === f && !this.state.views[o.id]) this.keep(o.id, v); }, () => {
        if (this.latestFetch.get(o.id) === f && !this.state.views[o.id]) this.latestFetch.delete(o.id);
      }).finally(() => { if (this.fetching.get(o.id) === f) this.fetching.delete(o.id); });
    }
    return f.catch(() => null);
  }

  // keep remembers a loaded view (the most recent VIEWS_KEPT, and the open one).
  private keep(id: string, v: View) {
    if (typeof v.seq === "number" && (this.state.views[id]?.seq || 0) > v.seq) return;
    v = share(this.state.views[id], { ...v, messages: this.sends.merge<T.DMMessage | T.Message>(id, v.messages || []) } as View);
    if (v === this.state.views[id]) return;
    const views: Record<string, View> = { ...this.state.views };
    delete views[id];
    views[id] = v;
    const ids = Object.keys(views);
    for (const old of ids.slice(0, Math.max(0, ids.length - VIEWS_KEPT))) if (old !== this.state.open?.id) {
      delete views[old];
      if (!this.fetching.has(old)) this.latestFetch.delete(old);
    }
    this.set({ views });
  }

  showTab(tab: Tab, section = "") {
    ++this.unreadLanding;this.unreadReturn=null;
    this.dismissMessageLanding();
    const previous = this.state.tab;
    this.set({ tab, section });
    if (tab === "chats" && previous !== "chats") this.markOpenRead();
  }
  openInvite(conv: string, selected?: string[], preset?: { who: string; label?: string }) {
    this.dismissMessageLanding();
    // Capture the visible scope once. Empty means Main flow, never all topics.
    this.set({ invite: { conv, selected, ...preset, topic: this.draft(conv).topic || "" } });
  }
  closeInvite() { this.set({ invite: null }); }
  setPanel(panel: boolean) { this.set({ panel }); }

  // Keep the exact destination until local views can prove it. Cached targets
  // open immediately; other enriched taps read just their bound DM.
  async openMessage(id: string, context?: OpenContext) {
    if (!/^[0-9a-f]{32}$/.test(id) || context?.conv !== undefined && !/^[0-9a-f]{64}$/.test(context.conv) ||
      context?.dir !== undefined && context.dir !== "in" && context.dir !== "out") {
      this.dismissMessageLanding();
      this.toast("This notification destination is invalid.", "error"); return;
    }
    const previous = this.state.messageLanding;
    if (previous?.busy && previous.target.id === id && previous.target.context?.conv === context?.conv && previous.target.context?.dir === context?.dir) {
      await (this.messageRefresh?.promise || this.retryMessageLanding()); return;
    }
    this.dismissMessageLanding();
    this.set({ tab: "chats", pending: null, messageLanding: { target: { id, context: context && { ...context } }, busy: true, error: "" } });
    await this.retryMessageLanding();
  }

  dismissMessageLanding() { if (this.state.messageLanding) this.set({ messageLanding: null }); }

  retryMessageLanding(automatic = false): Promise<void> {
    const landing = this.state.messageLanding;
    if (!landing || !this.alive) return Promise.resolve();
    if (automatic && landing.legacyMiss && !Object.values(this.state.views).some(view => {
      const message = messageTarget((view.messages || []).filter(m => !landing.target.context?.dir || m.dir === landing.target.context.dir), landing.target.id);
      return !(view as T.DMThread).frozen && !!message && !message.deleted;
    })) return Promise.resolve();
    if (this.messageRefresh?.target === landing.target) {
      if (!automatic || landing.target.context?.conv || !this.state.overview) this.messageRefresh.again = true;
      return this.messageRefresh.promise;
    }
    const run = { target: landing.target, again: false, promise: Promise.resolve() };
    const current = () => this.alive && this.state.messageLanding?.target === run.target;
    run.promise = Promise.resolve().then(async () => {
      do {
        run.again = false;
        if (!current()) return;
        this.set({ messageLanding: { ...this.state.messageLanding!, busy: true, error: "" } });
        try { await this.resolveMessageLanding(run.target, current, automatic); }
        catch (e) {
          if (current()) this.set({ messageLanding: { ...this.state.messageLanding!, busy: false, error: errorText(e) } });
        }
      } while (current() && run.again);
    }).finally(() => { if (this.messageRefresh === run) this.messageRefresh = null; });
    this.messageRefresh = run;
    return run.promise;
  }

  private async resolveMessageLanding(target: MessageLanding["target"], current: () => boolean, automatic = false) {
    const { id, context } = target;
    const contains = (view: View) => {
      const m = messageTarget((view.messages || []).filter(m => !context?.dir || m.dir === context.dir), id);
      return !!m && !m.deleted;
    };
    const knownThread = !context?.conv && (this.state.overview?.threads || []).some(t => t.id === id);
    const cachedThread = this.state.views[id];
    const cached = context?.conv ? this.state.views[context.conv]
      : (cachedThread?.id === id ? cachedThread : undefined) || (!knownThread ? Object.values(this.state.views).find(v => /^[0-9a-f]{64}$/.test(v.id) && !(v as T.DMThread).frozen && contains(v)) : undefined);
    let view = cached && (!context?.conv || cached.id === context.conv) && !(cached as T.DMThread).frozen && contains(cached) ? cached : undefined;
    let kind: "dm" | "thread" = view && /^[0-9a-f]{32}$/.test(view.id) ? "thread" : "dm";
    // A bound chat remains useful while catch-up has not supplied its exact
    // message. Keep the destination/retry notice and the person's draft topic;
    // never substitute another message or manufacture a read acknowledgement.
    const showContext = (v: View) => {
      if (!current() || v.id !== context?.conv || (v as T.DMThread).frozen || messageTarget<T.DMMessage | T.Message>(v.messages || [], id)) return;
      const o = { kind: "dm" as const, id: v.id };
      this.beginReadVisit(v);
      this.set({ ...this.viewPatch(o, v, this.state.open?.id === o.id), open: o, pending: null, tab: "chats" });
    };
    if (!view && context?.conv) {
      // Automatic retries follow refetch's open-view lane. A failed or
      // still-missing view needs the next push/manual retry, not a duplicate GET.
      const refreshedContext = automatic && cached && this.state.open?.kind === "dm" && this.state.open.id === context.conv;
      if (cached) showContext(cached);
      view = refreshedContext && cached ? cached : await this.api.dm(context.conv);
      if (!current()) return;
      if (view.id !== context.conv || (view as T.DMThread).frozen || !contains(view)) {
        showContext(view);
        throw new Error("The notified message is not available in this chat. Try again after reconnecting.");
      }
    } else if (!view) {
      if (!this.state.overview) await (this.refresh || this.load(true));
      if (!current()) return;
      if (!this.state.overview) throw new Error("Your chats have not loaded. Try again after reconnecting.");
      if ((this.state.overview.threads || []).some(t => t.id === id)) {
        try {
          const v = await this.api.thread(id);
          if (v.id === id && contains(v)) { view = v; kind = "thread"; }
        } catch { /* A known device thread never falls back to unrelated DMs. */ }
      } else {
        for (const d of this.state.overview.dms || []) {
          if (!current()) return;
          try {
            const v = await this.api.dm(d.id);
            if (v.id === d.id && !v.frozen && contains(v)) { view = v; break; }
          } catch { /* Legacy taps may belong to another admitted conversation. */ }
        }
        if (!view && current()) {
          try { const v = await this.api.thread(id); if (v.id === id && contains(v)) { view = v; kind = "thread"; } }
          catch { /* Not an available device conversation. */ }
        }
      }
      if (!view) {
        if (current()) this.set({ messageLanding: { ...this.state.messageLanding!, legacyMiss: true } });
        throw new Error("The notified message is not available on this device. Try again after reconnecting.");
      }
    }
    if (!current()) return;
    const o: NonNullable<Open> = kind === "dm" ? { kind, id: view.id, focus: id, focusSeq: ++this.focusSeq }
      : { kind, id: view.id, focus: id, focusSeq: ++this.focusSeq, peer: (view as T.Thread).peer };
    this.beginReadVisit(view);
    this.set({ ...this.viewPatch(o, view, this.state.open?.id === o.id), open: o, pending: null, tab: "chats", messageLanding: null });
    this.markOpenRead();
    if (kind === "dm") void this.loadTyping();
  }

  // openChannel opens the conversation a browser notification's channel
  // names; one this device does not have opens nothing in its place.
  async openChannel(chan: string) {
    this.dismissMessageLanding();
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
    ++this.unreadLanding;const review=this.returningToReview;this.unreadReturn=null;
    this.dismissMessageLanding();
    if (this.state.open) this.refreshed.delete(this.state.open.id);
    this.beginReadVisit();
    this.set({ open: null, pending: null, dm: null, thread: null, typing: null, readVisit: null, groupContextPending: "", ...(review?{tab:"oks" as const}:{}) });
  }

  // loadOpen loads o (the open conversation, or the one being opened) and
  // shows it while it is still the one wanted; a load that lost the race is
  // only remembered.
  private async loadOpen(o: Open = this.state.open, fresh = false): Promise<boolean> {
    if (!o) return false;
    const visit = this.readAcknowledged;
    const wanted = () => this.alive && (this.state.open === o || this.state.pending === o);
    let request: Promise<View> | undefined;
    try {
      // Being opened: a prefetch already under way for it is that load.
      let ahead = fresh ? undefined : this.fetching.get(o.id);
      if (!ahead) {
        ahead = (o.kind === "dm" ? this.api.dm(o.id) : this.api.thread(o.id)) as Promise<View>;
        this.fetching.set(o.id, ahead);
        this.latestFetch.set(o.id, ahead);
        const loading = ahead;
        void ahead.finally(() => { if (this.fetching.get(o.id) === loading) this.fetching.delete(o.id); }).catch(() => {});
      }
      request = ahead;
      const loaded: View = await ahead;
      if (!this.alive) return false;
      if (this.latestFetch.get(o.id) === ahead) this.keep(o.id, loaded);
      if (this.latestFetch.get(o.id) === ahead) {
        const changes = { ...this.state.messageChanges };
        for (const [key, p] of Object.entries(changes)) {
          if (p.view !== o.id) continue;
          const m = this.state.views[o.id]?.messages?.find(m => messageMatches(m, p.control));
          if (m && (changeConfirmed(m, p) || p.status === "saved" && messageVersion(m) !== p.original)) delete changes[key];
        }
        if (Object.keys(changes).length !== Object.keys(this.state.messageChanges).length) this.set({ messageChanges: changes });
      }
      // A newer same-ID load may already be kept. It still completes this
      // exact pending open; returning early would strand its focus/entry.
      const v = this.state.views[o.id] || loaded;
      if (!wanted()) return false; // another conversation was opened meanwhile
      const first = this.state.pending === o;
      const newHost = o.kind === "dm" && ((v as T.DMThread).agents || []).some((a) => !(this.state.dm?.agents || []).some((b) => b.host.address === a.host.address));
      // Only this entry's first committed fresh load arms deliberate reads.
      // Cached display and routine acknowledgement calls cannot consume it.
      if (this.readAcknowledged === visit && visit.entering === o && (first || this.state.open === o) && v.id === o.id) {
        visit.entry = new Map((v.messages || []).filter(m => m.attention).map(m => [m.id, this.readKey(m)]));
        visit.entering = null;
      }
      this.set({ ...(first ? { open: o, pending: null } : {}),
        ...(this.state.groupContextPending === o.id && this.latestFetch.get(o.id) === ahead ? {groupContextPending: ""} : {}),
        ...this.viewPatch(o, v, this.state.open?.id === o.id) });
      this.settleReactions(o, v, ahead);
      this.pruneDismissals();
      // A newer deliberate mark/arrival may follow a confirmed read while
      // its older overview is still blocked. Advance that exact snapshot too.
      const group = o.kind === "thread" ? this.archivedRead(v) : undefined;
      if (this.confirmedReads.has(group ? "archived:" + group.group : JSON.stringify([o.kind, o.id]))) this.confirmReadOverview(o, v);
      if (newHost) void this.loadAgentNames();
      this.markOpenRead();
      if (o.kind === "dm") void this.loadTyping();
      return true;
    } catch (e) {
      const groupPending = o.kind === "dm" && e !== null && typeof e === "object" && "code" in e && e.code === "group_context_pending";
      if (groupPending && this.latestFetch.get(o.id) !== request) return false;
      if (!this.fetching.has(o.id) && !this.state.views[o.id]) this.latestFetch.delete(o.id);
      if (!wanted()) return false;
      // Its placeholder then says it is still opening, with Try again.
      if (this.state.pending === o) this.set({ open: o, pending: null, dm: null, thread: null, typing: null });
      if (groupPending) { this.set({groupContextPending: o.id}); return false; }
      this.toast(errorText(e), "error");
      return false;
    }
  }

  // Reading one topic does not read the other topics returned by the same
  // conversation endpoint. Match Conversation's visible-message selection.
  private reading = new Set<string>();
  private readAcknowledged = { id: "", ids: new Set<string>(), entry: new Map<string, string>(), entering: null as Open,
    visit: null as ReadVisit | null, visible: new Map<string, string>() };
  private readKey(m: T.DMMessage | T.Message): string {
    return m.attention ? JSON.stringify([m.id, m.attention.rev, m.attention.writer]) : m.id;
  }
  private beginReadVisit(view?: View, entering: Open = null) {
    // A superseded entry's request must not become a later entry's baseline.
    if (this.readAcknowledged.entering) this.fetching.delete(this.readAcknowledged.entering.id);
    this.readAcknowledged = { id: "", ids: new Set<string>(), entering, visit: null, visible: new Map<string, string>(),
      entry: new Map((view?.messages || []).filter(m => m.attention).map(m => [m.id, this.readKey(m)])) };
  }

  private readScope(o: NonNullable<Open>, topic: string, fresh: boolean) { return JSON.stringify([o.kind, o.id, topic, fresh]); }
  private prepareReadScope(o: NonNullable<Open>, view: View, draft: Draft): ReadVisit | null {
    const scope = this.readScope(o, o.kind === "dm" ? draft.topic || "" : "", o.kind === "dm" && !!draft.newTopic);
    const visit = this.readAcknowledged;
    if (visit.id !== scope) {
      if (visit.id !== "") visit.entry = new Map<string, string>();
      visit.id = scope; visit.ids = new Set<string>(); visit.visible.clear(); visit.visit = null;
    }
    if (!visit.visit && !visit.entering) {
      const rows = o.kind === "dm" ? (view.messages || []).filter(m => ("topic" in m && m.topic || "") === (draft.newTopic ? "" : draft.topic || "")) : view.messages || [];
      visit.visit = { scope, firstUnread: rows.find(m => m.unread && this.canMarkUnread(m))?.id };
    }
    return visit.visit;
  }

  readVisit(o: NonNullable<Open>, topic = "", fresh = false): ReadVisit | null {
    const v = this.state.readVisit;
    return this.state.open?.kind === o.kind && this.state.open.id === o.id && v === this.readAcknowledged.visit &&
      v?.scope === this.readScope(o, topic, fresh) ? v : null;
  }

  reportVisible(visit: ReadVisit, rows: readonly (T.DMMessage | T.Message)[]) {
    if (visit !== this.state.readVisit || visit !== this.readAcknowledged.visit) return;
    this.readAcknowledged.visible = new Map(rows.map(m => [m.id, this.readKey(m)]));
    this.markOpenRead();
  }

  canMarkUnread(m: T.DMMessage | T.Message): boolean {
    const notice = this.state.open?.kind === "thread" && m.kind === "message" && m.status === "review_notice" &&
      !m.reply_to && !("files" in m && m.files?.length);
    return !("_local" in m && m._local) && !m.deleted && !("event" in m && m.event) &&
      !("topic_event" in m && m.topic_event) && !notice && ["message", "question", "task", "answer", "result"].includes(m.kind);
  }

  async markUnread(id: string, fromHere = false) {
    const o = this.state.open, view = o && this.state.views[o.id];
    if (!o || !view) return;
    const draft = this.draft(o.id);
    const messages = o.kind === "dm" ? ((view as T.DMThread).messages || []).filter(m => draft.newTopic ? !m.topic : (m.topic || "") === (draft.topic || "")) : view.messages || [];
    const anchor = messages.findIndex(m => m.id === id && this.canMarkUnread(m));
    if (anchor < 0) { this.toast("That message is no longer available here.", "error"); return; }
    const ids = [...new Set((fromHere ? messages.slice(anchor) : [messages[anchor]]).filter(m => this.canMarkUnread(m)).map(m => m.id))];
    await this.run(a => a.act({ do: "unread", id, ids }));
  }

  private markOpenRead() {
    const o = this.state.open;
    if (!this.alive || !o || this.state.tab !== "chats" || o.kind === "dm" && o.focus && o.focusSeq !== this.topicFocusSeq) return;
    if (this.state.pending && this.state.pending !== o) return;
    if (document.visibilityState !== "visible" || !document.hasFocus() || this.mobileHidden()) return;
    const v = this.state.views[o.id];
    if (!v) return;
    const draft = this.state.drafts[o.id];
    const messages = o.kind === "dm"
      ? ((v as T.DMThread).messages || []).filter(m => draft?.newTopic ? !m.topic : (m.topic || "") === (draft?.topic || ""))
      : v.messages;
    const scope = this.readScope(o, o.kind === "dm" ? draft?.topic || "" : "", o.kind === "dm" && !!draft?.newTopic);
    if (!this.state.readVisit || this.state.readVisit !== this.readAcknowledged.visit || this.state.readVisit.scope !== scope || this.readAcknowledged.entering) return;
    const acknowledged = this.readAcknowledged.ids;
    const visible = new Set((messages || []).flatMap(m => [m.id, this.readKey(m)]));
    for (const id of acknowledged) if (!visible.has(id)) acknowledged.delete(id);
    const pending = (messages || []).filter(m => this.canMarkUnread(m) && (m.unread || o.kind === "thread" && m.dir === "out") &&
      this.readAcknowledged.visible.get(m.id) === this.readKey(m) &&
      (!m.attention || this.readAcknowledged.entry.get(m.id) === this.readKey(m)) &&
      !this.reading.has(this.readKey(m)) && !acknowledged.has(this.readKey(m)));
    const ids = pending.map(m => m.id);
    if (!ids.length) return;
    const keys = pending.map(m => this.readKey(m));
    const attention = Object.fromEntries(pending.filter(m => m.attention).map(m => [m.id, m.attention!]));
    keys.forEach(key => this.reading.add(key));
    // The change stream brings the committed read state. Keep exact successful
    // acknowledgements so refreshing a sent device-thread row cannot loop.
    void this.api.markRead(ids, Object.keys(attention).length ? attention : undefined).then(async () => {
      keys.forEach(key => { acknowledged.add(key); this.reading.delete(key); });
      // The native action also writes legacy read state. A cleared sent row
      // returns to its plain ID; keep that acknowledgement within this visit.
      pending.filter(m => m.attention).forEach(m => acknowledged.add(m.id));
      if (!this.alive || !pending.some(m => m.unread)) return;
      // Reuse a completed committed push; an older in-flight view cannot
      // hold this read behind it. The exact fresh view proves a CAS result.
      const unread = new Set(pending.filter(m => m.unread).map(m => this.readKey(m)));
      const stale = () => this.state.views[o.id]?.messages?.some(m => unread.has(this.readKey(m)) && m.unread);
      if (this.state.open?.id === o.id && stale()) await this.loadOpen(this.state.open, true);
      if (!this.alive) return;
      const current = this.state.views[o.id];
      if (this.state.open?.id === o.id && current) this.confirmReadOverview(o, current);
      const summary = o.kind === "dm" ? this.state.overview?.dms?.find(d => d.id === o.id) : this.state.overview?.threads?.find(t => t.id === o.id);
      if (this.state.open?.id === o.id && current && !stale() && summary?.unread === (current.messages || []).filter(m => m.unread).length) return;
      void this.load(false, false);
    }).catch(() => {}).finally(() => keys.forEach(key => this.reading.delete(key)));
  }

  private typingLoading = false;
  private typingAgain = false;
  private async loadTyping() {
    this.typingAgain = true;
    if (this.typingLoading) return;
    this.typingLoading = true;
    try {
      do {
        this.typingAgain = false;
        const o = this.state.open;
        if (o?.kind !== "dm") return;
        try {
          const typing = await this.api.typing({ conv: o.id });
          if (this.alive && this.state.open === o) this.set({ typing: share(this.state.typing, typing) });
        } catch { /* a later push retries; the ordinary view remains usable */ }
      } while (this.typingAgain && this.alive);
    } finally { this.typingLoading = false; }
  }

  // loadAgentNames reads the agent catalogs once per change: this
  // installation's own, and the hosts of agents in the open conversation.
  private namesLoading = false;
  private async loadAgentNames() {
    if (this.namesLoading) return;
    this.namesLoading = true;
    try {
      const local=this.state.overview?.me.address;
      const hosts = new Set<string>([local||""]);
      for (const a of this.state.dm?.agents || []) hosts.add(a.host.address);
      const next: Record<string, string> = { ...this.state.agentNames };
      if(this.state.thread)hosts.add(this.state.thread.topic?.agent_host||this.state.thread.peer);
      for(const t of this.state.overview?.threads||[])if(t.agent_host)hosts.add(t.agent_host);
      for(const t of this.state.overview?.topics||[])if(t.latest.agent_host)hosts.add(t.latest.agent_host);
      for(const p of [this.state.overview?.person,...(this.state.overview?.people||[])])for(const a of p?.agents||[])if(a.dms?.some(d=>d.state==="active"))hosts.add(a.address);
      const addresses=[...hosts],catalogs=await Promise.allSettled(addresses.map(h=>this.api.agents(h===local?undefined:h||undefined)));
      for(const [index,result] of catalogs.entries())if(result.status==="fulfilled"){
        const view=result.value,agents=view.agents||[];
        const requested=addresses[index]||local||view.host;
        for(const key of Object.keys(next))if(key.endsWith("#host")&&next[key]===requested){const id=key.slice(0,-5);delete next[id+"#host"];delete next[id+"#host_key"];delete next[id+"#current"];}
        next[requested+"#catalog"]="current";
        if(view.host!==requested||agents.some(a=>a.record.host!==requested||!/^[0-9a-f]{32}$/.test(a.record.id)||!a.record.host_key))continue;
        for(const a of agents){next[a.record.id]=a.record.label;next[a.record.id+"#harness"]=a.record.harness||"";}
        const owner=[this.state.overview?.person,...(this.state.overview?.people||[])].find(p=>p?.address===requested||p?.devices?.some(d=>d.address===requested));
        const pinned=requested===local?this.state.overview?.me.fingerprint:owner?.devices?.find(d=>d.address===requested)?.fingerprint||(owner?.address===requested?owner.fingerprint:undefined);
        for(const a of currentCatalogAgents(agents,view.local)){
          if(pinned&&a.record.host_key!==pinned)continue;
          next[a.record.id+"#host"]=a.record.host;next[a.record.id+"#host_key"]=a.record.host_key;next[a.record.id+"#current"]="true";
        }
      }
      if ([...new Set([...Object.keys(next),...Object.keys(this.state.agentNames)])].some(id => this.state.agentNames[id] !== next[id])) this.set({ agentNames: next });
    } finally { this.namesLoading = false; }
  }

  // ---- drafts, kept per workspace and conversation; File objects stay in memory
  private loadDrafts(): Record<string, Draft> {
    const saved=this.host.workspaces?.state?.(this.host.workspace.id)?.comicDrafts as {handle?:string;drafts:Record<string,Draft>}|undefined;
    if(saved&&saved.handle===this.host.workspace.handle)return Object.fromEntries(Object.entries(saved.drafts).map(([id,d])=>[id,{...d,
      files:d.files?.map(f=>f.preparing?{...f,preparing:false,mediaProgress:undefined,mediaNote:"Preparation was interrupted. The original file is kept."}:f)}]));
    try {
      const raw = JSON.parse(localStorage.getItem(draftsKey(this.host.workspace.id)) || "{}");
      const out: Record<string, Draft> = {};
      for (const [k, v] of Object.entries(raw as Record<string, Draft>)) if (v && typeof v.text === "string") out[k] = {
        text: v.text, replyTo: typeof v.replyTo === "string" ? v.replyTo : undefined,
        agent: typeof v.agent === "string" ? v.agent : undefined,
        topic: typeof v.topic === "string" ? v.topic : undefined, newTopic: v.newTopic === true,
        plainReply: v.plainReply === true || undefined,
        mediaMode: v.mediaMode === "smaller" ? "smaller" : v.mediaMode === "original" ? "original" : undefined,
      };
      return out;
    } catch { return {}; }
  }

  private saveDrafts(drafts: Record<string, Draft>) {
    try {
      const keep: Record<string, Draft> = {};
      for (const [k, d] of Object.entries(drafts)) if (d.text || d.replyTo || d.agent || d.topic || d.newTopic || d.mediaMode) keep[k] = { text: d.text, replyTo: d.replyTo, agent: d.agent, topic: d.topic, newTopic: d.newTopic, plainReply: d.plainReply, mediaMode: d.mediaMode };
      localStorage.setItem(draftsKey(this.host.workspace.id), JSON.stringify(keep));
    } catch { /* a convenience only */ }
  }

  draft(conv: string): Draft { return this.state.drafts[conv] || { text: "" }; }

  setDraft(conv: string, d: Draft, topicSelected = false) {
    const prior = this.state.drafts[conv];
    if (topicSelected || (prior?.topic || "") !== (d.topic || "") || !!prior?.newTopic !== !!d.newTopic) this.dismissMessageLanding();
    const drafts = { ...this.state.drafts };
    if (!d.text && !d.replyTo && !d.agent && !d.newTopic && !d.topic && !d.mediaMode && !(d.files && d.files.length)) delete drafts[conv];
    else drafts[conv] = d;
    const changedScope = (prior?.topic || "") !== (d.topic || "") || !!prior?.newTopic !== !!d.newTopic;
    const selected = this.state.open?.id === conv && (topicSelected || changedScope);
    if (selected) {
      if ((prior?.topic || "") !== (d.topic || "") || !!prior?.newTopic !== !!d.newTopic) {
        const entering = this.readAcknowledged.entering;
        if (entering) {
          // The pending full-conversation load also contains the new topic.
          this.readAcknowledged.id = "";
          this.readAcknowledged.ids.clear();
          this.readAcknowledged.entry.clear();
          this.readAcknowledged.visit = null;
          this.readAcknowledged.visible.clear();
        } else this.beginReadVisit(this.state.views[conv]);
      }
      this.topicFocusSeq = this.state.open!.focusSeq || 0;
    }
    const o = this.state.open, view = this.state.views[conv];
    this.set({ drafts, ...(selected && o && view ? { readVisit: this.prepareReadScope(o, view, d) } : {}),
      ...(selected && topicSelected && o && !this.readAcknowledged.entering ? { open: { ...o, focus: undefined, ...(o.kind === "dm" ? { preview: undefined } : {}), focusSeq: undefined, unread: true } } : {}) });
    this.saveDrafts(drafts);
    if (selected) this.markOpenRead();
  }

  hasUnsent(): boolean { return !!this.groupCreationWork || this.dmStartWork.size > 0 || unsentIn(this.state); }

  // ---- feedback
  // Telegram Web A's local-change -> async request -> reconciliation pattern
  // (f67a408, actions/api/reactions.ts:234-272). Only presentation changes here;
  // the existing resolve operation and native authority remain unchanged.
  private captureDismiss(id: string): Omit<PendingDismiss, "token"> {
    const o = this.state.overview;
    const rows = [...(o?.needs_you || []), ...(o?.held || []), ...(o?.review || [])]
      .filter(r => r.id === id).map(r => JSON.stringify(r));
    const views: PendingDismiss["views"] = {};
    for (const [key, v] of Object.entries(this.state.views)) {
      const m = v.messages?.find(m => m.id === id && m.actions?.includes("resolve"));
      if (m) views[key] = { seq: v.seq || 0, message: dismissMessageKey(m) };
    }
    return { seq: o?.seq || 0, rows, views };
  }

  private dismissedOverview(o: T.Overview): T.Overview {
    const hidden = new Set(Object.values(this.state.dismissPending).filter(p => o.seq <= p.seq).flatMap(p => p.rows));
    const needs = (o.needs_you || []).filter(r => !hidden.has(JSON.stringify(r)));
    const held = (o.held || []).filter(r => !hidden.has(JSON.stringify(r)));
    const review = (o.review || []).filter(r => !hidden.has(JSON.stringify(r)));
    if (needs.length === (o.needs_you || []).length && held.length === (o.held || []).length && review.length === (o.review || []).length) return o;
    const closedNeeds = (o.needs_you || []).filter(r => hidden.has(JSON.stringify(r)));
    const closedHeld = (o.held || []).filter(r => hidden.has(JSON.stringify(r)));
    const closedReview = (o.review || []).filter(r => !r.notice && hidden.has(JSON.stringify(r)));
    const summary = (t: T.ThreadSummary): T.ThreadSummary => {
      const n = closedReview.filter(r => r.id === t.id || Object.values(this.state.dismissPending).some(p => p.views[t.id] && p.rows.includes(JSON.stringify(r)))).length;
      return n ? { ...t, review: Math.max(0, t.review - n) } : t;
    };
    return { ...o, needs_you: needs, held, review,
      // Resolving a decision never marks a chat read or completes reminders.
      dms: o.dms?.map(d => {
        const n = closedNeeds.filter(r => r.conv === d.id && !r.decide_on).length;
        const h = closedHeld.filter(r => r.conv === d.id && !r.decide_on).length;
        return n || h ? { ...d, decide: Math.max(0, d.decide - n), held: Math.max(0, d.held - h) } : d;
      }) || null,
      threads: o.threads?.map(summary) || null,
      topics: o.topics?.map(t => { const latest = summary(t.latest); return latest === t.latest ? t : { ...t, latest }; }),
    };
  }

  pendingDismiss(m: T.DMMessage | T.Message, view: View | null | undefined): PendingDismiss | undefined {
    const p = this.state.dismissPending[m.id], captured = view && p?.views[view.id];
    return captured && (view?.seq || 0) <= captured.seq && captured.message === dismissMessageKey(m) ? p : undefined;
  }

  private pruneDismissals() {
    const current = this.state.dismissPending, pending = { ...current };
    for (const [id, p] of Object.entries(current)) {
      if (!p.saved) continue;
      const now = this.captureDismiss(id);
      const oldOverview = now.seq <= p.seq && now.rows.some(r => p.rows.includes(r));
      const oldView = Object.entries(now.views).some(([key, v]) => p.views[key] && v.seq <= p.views[key].seq && v.message === p.views[key].message);
      if (!oldOverview && !oldView) delete pending[id];
    }
    if (Object.keys(pending).length !== Object.keys(current).length) this.set({ dismissPending: pending });
  }

  private dismiss(action: T.Action, calls: Api): Promise<{ note: string }> {
    if (!this.alive) return Promise.reject(Error("stale or disconnected workspace"));
    const id = action.id;
    if (!id) return calls.act(action);
    const captured = this.captureDismiss(id), intent = JSON.stringify(captured);
    const prior = this.dismissing.get(id);
    if (prior?.intent === intent) return prior.work;
    const p = { ...captured, token: ++this.dismissSequence };
    const current = () => this.alive && this.state.dismissPending[id]?.token === p.token;
    this.set({ dismissPending: { ...this.state.dismissPending, [id]: p } });
    const work = Promise.resolve().then(async () => {
      try {
        const result = await calls.act(action);
        if (current()) {
          this.set({ dismissPending: { ...this.state.dismissPending, [id]: { ...p, saved: true } } });
          // A pre-commit GET must not occupy the exact confirmation lane.
          if (this.state.open && p.views[this.state.open.id]) void this.loadOpen(this.state.open, true);
          // The caller's existing run/refetch owns the overview lane.
          this.pruneDismissals();
        }
        return result;
      } catch (error) {
        if (current()) {
          const pending = { ...this.state.dismissPending }; delete pending[id];
          this.set({ dismissPending: pending }); // reveal current DTO, never a captured stale copy
          // The response can be lost after a successful write. Fetch current
          // truth as well as displaying the error; never repeat the operation.
          if (this.state.open && p.views[this.state.open.id]) void this.loadOpen(this.state.open, true);
          void this.load(false);
        }
        throw errorText(error) === "stale or disconnected workspace" ? error : Error("Couldn’t confirm this item was marked as handled. " + errorText(error));
      } finally {
        if (this.dismissing.get(id)?.token === p.token) this.dismissing.delete(id);
      }
    });
    this.dismissing.set(id, { intent, token: p.token, work });
    return work;
  }

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
      if (this.alive && ok) this.toast(ok, "ok");
      if (this.alive) void this.refetch();
      return r;
    } catch (e) {
      // A workspace being left or replaced answers its old view this way:
      // the host remounts the interface, so there is nothing to tell.
      if (this.alive && errorText(e) !== "stale or disconnected workspace") this.toast(errorText(e), "error");
      return undefined;
    }
  }

  // Pending feedback is local to this exact view and verified human actor.
  // The authoritative DTO is never edited; peer and assistant marks stay intact.
  reactionIsCurrent(p: PendingReaction): boolean {
    const selected = this.state.open;
    const self = p.change.conv ? this.state.overview?.person?.person : this.state.overview?.me.address;
    const view = selected?.kind === "dm" ? this.state.dm : this.state.thread;
    return this.alive && this.state.tab === "chats" && selected === p.selected && !this.state.pending && self === p.self.id &&
      !!view?.messages?.some(m => m.id === p.change.id && m.dir === p.change.dir && !m.deleted);
  }

  // A previous visit may still be storing this exact human's add. A remove
  // chosen after returning must queue behind it even before the add is read.
  reactionInFlight(change: T.ControlAction): boolean {
    const p = this.reacting.get(JSON.stringify([reactionScope(change), change.emoji]))?.pending;
    const selected = this.state.open;
    const self = change.conv ? this.state.overview?.person?.person : this.state.overview?.me.address;
    return !!p && this.alive && p.self.id === self && p.selected.id === selected?.id && p.selected.kind === selected.kind;
  }

  private pendingReaction(p: PendingReaction, clear = false) {
    const scope = reactionScope(p.change), emoji = p.change.emoji!;
    const prior = this.state.reactionPending[scope] || {};
    if (clear && prior[emoji]?.token !== p.token) return;
    const slots = { ...prior };
    if (clear) delete slots[emoji]; else slots[emoji] = p;
    const all = { ...this.state.reactionPending };
    if (Object.keys(slots).length) all[scope] = slots; else delete all[scope];
    this.set({ reactionPending: all });
  }

  private settleReactions(o: NonNullable<Open>, view: View, request?: Promise<View>) {
    for (const slots of Object.values(this.state.reactionPending)) for (const p of Object.values(slots)) {
      if (p.selected !== o || p.confirmed === undefined) continue;
      const m = view.messages?.find(m => m.id === p.change.id && m.dir === p.change.dir);
      // A newer authoritative linked-device change may disagree with this
      // write. A post-ack current read wins; a preexisting stale read does not.
      const currentRead = !!request && request !== p.priorRead && this.latestFetch.get(o.id) === request;
      if (currentRead || !m || m.deleted || !!m.reactions?.some(r => r.emoji === p.change.emoji && r.mine) === !p.confirmed) this.pendingReaction(p, true);
    }
  }

  react(change: T.ControlAction): Promise<{ note: string } | undefined> {
    const selected = this.state.open;
    const self = change.conv ? this.state.overview?.person?.person : this.state.overview?.me.address;
    if (!selected || !self || !change.emoji || (change.conv ? selected.kind !== "dm" || selected.id !== change.conv : selected.kind !== "thread")) return Promise.resolve(undefined);
    const scope = reactionScope(change), key = JSON.stringify([scope, change.emoji]);
    const prior = this.reacting.get(key);
    if (prior && this.reactionIsCurrent(prior.pending)) {
      if (!!prior.pending.change.remove !== !!change.remove) {
        prior.pending = { ...prior.pending, change: { ...change, remove: !!change.remove }, confirmed: undefined };
        this.pendingReaction(prior.pending);
      }
      return prior.work;
    }
    const p: PendingReaction = { change: { ...change, remove: !!change.remove }, selected,
      self: { id: self, label: this.state.overview?.person?.label || "You" }, token: ++this.reactionSequence };
    if (!this.reactionIsCurrent(p)) return Promise.resolve(undefined);
    const slot = { pending: p, work: Promise.resolve(undefined) as Promise<{ note: string } | undefined> };
    // A revisit gets its own local feedback, but an older exact control
    // still in flight must finish before this newer intent is dispatched.
    const work = Promise.resolve(prior?.work).then(async () => {
      try {
        while (this.reactionIsCurrent(slot.pending)) {
          const sent = { ...slot.pending.change };
          const result = await this.api.react(sent);
          if (!this.reactionIsCurrent(slot.pending)) return undefined;
          if (!!slot.pending.change.remove === !!sent.remove) {
            slot.pending = { ...slot.pending, confirmed: !!sent.remove, priorRead: this.latestFetch.get(selected.id) };
            this.pendingReaction(slot.pending);
          }
          // R1: a pre-commit history request must not hold this fresh load.
          // Keep the existing latest-fetch fence and the pending overlay until
          // a current authoritative projection confirms the desired mark.
          const projected = await this.loadOpen(selected, true);
          if (!this.reactionIsCurrent(slot.pending)) return undefined;
          if (!!slot.pending.change.remove !== !!sent.remove) continue;
          if (slot.pending.confirmed !== !!sent.remove) {
            slot.pending = { ...slot.pending, confirmed: !!sent.remove };
            this.pendingReaction(slot.pending);
          }
          const view = selected.kind === "dm" ? this.state.dm : this.state.thread;
          if (projected) this.pendingReaction(slot.pending, true);
          else if (view) this.settleReactions(selected, view);
          void this.load(false);
          return result;
        }
      } catch (e) {
        if (this.reactionIsCurrent(slot.pending) && errorText(e) !== "stale or disconnected workspace") this.toast(errorText(e), "error");
        this.pendingReaction(slot.pending, true);
      } finally {
        if (this.reacting.get(key) === slot) this.reacting.delete(key);
        if (!this.reactionIsCurrent(slot.pending)) this.pendingReaction(slot.pending, true);
      }
      return undefined;
    });
    slot.work = work;
    this.reacting.set(key, slot);
    this.pendingReaction(p);
    return work;
  }

}

export function useStore<S>(store: Store, select: (s: State) => S): S {
  return useSyncExternalStore(store.subscribe, () => select(store.get()));
}
