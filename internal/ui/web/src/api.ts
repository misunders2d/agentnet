// Typed calls over the host's JSON API (docs/UI_SKINS.md "Routes"). Every
// call is bound to the host it was made with: an operation started in one
// workspace never moves to another when the person switches.
import type * as T from "./api.gen";
import type { Host } from "./host";

export type { T };

// TODO(integrate:P1): GET /api/folders?path= is P1's route (livefolders.go,
// MEL-534): an absolute folder (home by default), its parent, home, Windows
// drive roots and its subfolders, read-only, at most MaxFolders of them
// (truncated says some were left out). Replace these two types with the
// generated T.FoldersView once P1 lands. Subfolders may come as names or
// as {name, path}; features/AssistantSetup.model.ts folderEntries reads both.
export interface FoldersView {
  path: string;
  parent?: string;
  home?: string;
  roots?: string[];
  dirs: (string | FolderEntry)[] | null;
  truncated?: boolean;
}
export interface FolderEntry { name: string; path: string }

const q = (path: string, params: Record<string, string>) =>
  path + "?" + Object.entries(params).map(([k, v]) => k + "=" + encodeURIComponent(v)).join("&");

export function api(host: Host) {
  const get = <R>(path: string) => host.api<R>(path);
  const post = <R>(path: string, body: unknown) => host.api<R>(path, body);
  return {
    host,
    // topics=1: archived topics are counted, not listed (they are paged through topics below).
    overview: () => get<T.Overview>("/api/overview?topics=1"),
    dm: (id: string) => get<T.DMThread>(q("/api/dm", { id })),
    thread: (id: string) => get<T.Thread>(q("/api/thread", { id })),
    refresh: (id: string) => post<T.Presence>("/api/refresh", { id }),

    // Conversations between persons (DMs and groups)
    newDM: (address: string) => post<{ id: string }>("/api/dm/new", { address }),
    sendDM: (d: T.DMDraft) => post<T.Sent>("/api/dm/send", d),
    deleteConversation: (a: T.DeleteConversationAction) => post<{ note: string }>("/api/conversation/delete", a),

    // Assistants in a conversation
    inviteAgent: (a: T.AgentInvite) => post<T.AgentView>("/api/dm/agent/invite", a),
    decideAgent: (pid: string, accept: boolean) => post<T.AgentView>("/api/dm/agent/decide", { pid, accept }),
    dismissAgent: (pid: string) => post<T.AgentView>("/api/dm/agent/dismiss", { pid }),
    askAgent: (a: T.AgentAsk) => post<T.Sent>("/api/dm/agent/ask", a),
    agents: (hostAddress?: string) => get<T.AgentCatalogView>(hostAddress ? q("/api/agents", { host: hostAddress }) : "/api/agents"),
    changeAgents: (c: T.AgentCatalogChange) => post<T.AgentCatalogChangeResult>("/api/agents", c),

    // People invited to help in a DM
    checkGuest: (c: T.GuestCheckRequest) => post<T.GuestCheck>("/api/dm/guest/check", c),
    inviteGuest: (a: T.GuestAction) => post<T.GuestView>("/api/dm/guest/invite", a),
    decideGuest: (pid: string, accept: boolean) => post<T.GuestView>("/api/dm/guest/decide", { pid, accept }),
    endGuest: (pid: string) => post<T.GuestView>("/api/dm/guest/end", { pid }),

    // Groups
    groupInvitations: () => get<T.GroupInvitationView[] | null>("/api/groups/invitations"),
    newGroup: (title: string) => post<{ id: string }>("/api/groups/new", { title }),
    inviteToGroup: (d: T.GroupInviteDraft) => post<T.GroupInvitationView>("/api/groups/invite", d),
    decideGroup: (id: string, accept: boolean) => post<{ recorded: boolean }>("/api/groups/decide", { id, accept }),
    publishGroup: (id: string) => post<{ published: boolean }>("/api/groups/publish", { id }),
    manageGroup: (c: T.GroupChange) => post<T.GroupChangeResult>("/api/groups/manage", c),

    // Messages
    react: (c: T.ControlAction) => post<{ note: string }>("/api/message/react", c),
    edit: (c: T.ControlAction) => post<{ note: string }>("/api/message/edit", c),
    retract: (c: T.ControlAction) => post<{ note: string }>("/api/message/delete", c),

    // Device conversations (one agent's own threads) and decisions
    send: (d: T.Draft) => post<T.Sent>("/api/send", d),
    act: (a: T.Action) => post<{ note: string }>("/api/act", a),
    markRead: (ids: string[]) => post<{ note: string }>("/api/act", { do: "read", ids } satisfies T.Action),
    decide: (d: T.DecisionAction) => post<{ note: string }>("/api/operator/decide", d),

    // Topics (an agent's separate conversations): one page of the All topics
    // list, and the person's own changes, kept on this device only.
    topics: (p: { peer?: string; state?: string; q?: string; before?: string; limit?: number }) =>
      get<T.TopicPage>(q("/api/topics", Object.fromEntries(Object.entries(p).filter(([, v]) => v !== undefined && v !== "").map(([k, v]) => [k, String(v)])))),
    changeTopic: (what: "rename" | "done" | "reopen", c: T.TopicChange) => post<{ note: string }>("/api/topic/" + what, c),

    // Files
    stage: (file: File) => host.stage(file),
    discard: (ids: string[]) => post<unknown>("/api/upload/discard", { ids }),
    file: (id: string, index: number, dir?: string) => host.file(id, index, dir),
    requestFile: (id: string, index: number) => post<unknown>("/api/file/request", { id, index }),

    // Typing (ephemeral; never content)
    typing: (scope: T.TypingScope) => get<T.TypingView>(q("/api/typing", { conv: scope.conv || "", peer: scope.peer || "", thread: scope.thread || "" })),
    sendTyping: (scope: T.TypingScope, active: boolean) => post<T.TypingResult>("/api/typing", { scope, active }),
    typingPreferences: (p: T.TypingPreferences) => post<T.TypingPreferences>("/api/typing/preferences", p), // this device's own: share mine, show others

    // Me, my devices, my assistant
    createPerson: (label: string) => post<{ person: T.PersonView; note: string }>("/api/person", { label }),
    renamePerson: (label: string) => post<T.PersonView>("/api/person/label", { label }),
    deviceLink: () => post<T.DeviceLink>("/api/device/link", {}),
    decideDevice: (id: string, accept: boolean) => post<{ note: string }>("/api/device/decide", { id, accept }),
    removeDevice: (address: string) => post<{ note: string }>("/api/device/remove", { address }),
    responder: () => get<T.ResponderView>("/api/responder"),
    setResponder: (c: T.ResponderChange) => post<T.ResponderView & { note?: string }>("/api/responder", c),
    // Connecting coding sessions (hooks): read the tools, review a change, apply exactly that change.
    assistantSetup: (r?: T.AssistantSetupRequest) => (r ? post<T.AssistantSetupView>("/api/assistant-setup", r) : get<T.AssistantSetupView>("/api/assistant-setup")),
    // A folder on this computer and its subfolders, read-only: the folder browser (never a typed path).
    folders: (path?: string) => get<FoldersView>(path ? q("/api/folders", { path }) : "/api/folders"),

    // Reminders and notifications
    remind: (id: string, due: Date) => post<unknown>("/api/remind", { id, due: Math.floor(due.getTime() / 1000) }), // unix seconds
    remindDone: (id: string) => post<unknown>("/api/remind/done", { id }),
    remindCancel: (id: string) => post<unknown>("/api/remind/cancel", { id }),
    notifyResolve: (chan: string) => get<{ conv?: string }>(q("/api/notify/resolve", { chan })), // a browser notification's channel
    notify: (what:"enable" | "disable" | "mute" | "allow" | "seen", body: { conv?: string; person?: string; muted?: boolean; allowed?: boolean; ids?: string[] } = {}) => post<{ note: string }>("/api/notify/" + what, body),

    // Teams and storage
    teams: () => get<T.TeamsView>("/api/teams"),
    changeTeam: (c: T.TeamChange) => post<T.TeamState>("/api/team", c),
    teamSnapshot: (teams: string[]) => post<T.TeamSnapshot>("/api/teams/snapshot", { teams }),
    storage: () => get<T.StorageSummary>("/api/storage"),

    // The workspace's own name (its admin names it for everyone)
    workspace: () => get<T.WorkspaceInfoView>("/api/workspace"),
    renameWorkspaceForEveryone: (name: string) => post<T.WorkspaceInfoView>("/api/workspace/name", { name } satisfies T.WorkspaceNameChange),
  };
}

export type Api = ReturnType<typeof api>;

// errorText is what a failed call says, for the person.
export function errorText(e: unknown): string {
  if (e instanceof Error) return e.message || "Something went wrong.";
  return String(e || "Something went wrong.");
}
