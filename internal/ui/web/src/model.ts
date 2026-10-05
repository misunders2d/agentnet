// What the screens show, derived from the server's views. Nothing here
// decides anything: names are what people call themselves, states are
// what the server's records prove, and actions come from its can[] lists.
import type { T } from "./api";

// ---- people and agents ---------------------------------------------------

/** One chat in the list: a person (DM), a group, or an agent's own device thread. */
export interface ChatItem {
  key: string;                 // "dm:<id>" | "thread:<id>"
  open: { kind: "dm"; id: string } | { kind: "thread"; id: string; peer?: string };
  kind: "person" | "group" | "agent";
  title: string;
  subtitle?: string;           // e.g. "Vitalii's agent · ZenBook"
  avatarSeed: string;          // stable seed for the avatar's colour
  last: string;
  lastAt: string;
  unread: number;
  needsYou: number;            // OKs this device gives here: requests or invitations for your agent (not ones decided on another device)
  held: number;                // questions or tasks held here for you to answer: not OKs, never counted as one
  guests: number;              // people or agents helping right now
  working: boolean;            // an agent is working on something here
  frozen?: string;
  members?: string[];          // group member names, for stacked avatars
  topics?: Topic[];            // an agent's separate conversations not archived, newest first
  peer?: string;               // an agent's device address
  topicTotal?: number;         // every topic with that agent, archived ones too
  keyChanged?: boolean;        // the agent's identity changed: sending is paused until it is checked and trusted
}

// ---- topics (docs/plans/TOPICS.md) ------------------------------------------

/** Topic tunables for the screens. Owner (2026-10-04): "this should all be
 *  easily changed, if needed — not via settings, but with code": change them
 *  here; there is no setting. How topics are derived (when one is archived,
 *  page limits) is the server's: client/topics.go and engine.mjs. */
export const TOPICS = {
  barMax: 6,          // chips in a conversation's topic bar, "All topics (N)" included
  chipMinWidth: 104,  // px a topic chip keeps before the bar shows one fewer (the rest are under All topics)
  chipMaxWidth: "16rem", // the widest a topic chip grows on a wide screen
  barTitle: 40,       // characters of a topic's name in a bar chip (the chip truncates further)
  listTitle: 90,      // characters of a topic's name in the All topics list
  pageSize: 30,       // topics fetched per page of the All topics list
  searchDelay: 250,   // ms after the last keystroke before a topic search is sent
  searchMin: 2,       // characters before the chat list also searches older topics
  chatSearchMax: 8,   // topics the chat list's search shows
  // The server's limits, pinned to client/topics.go by internal/ui topics_browser_test.go:
  titleMax: 120,      // characters in a name you give a topic (client.TopicTitleMax)
  pageMax: 200,       // topics one request may list (client.TopicPageMax)
} as const;

// Arrival notes use seconds; message ordering still uses arrival.
export const MESSAGES = Object.freeze({ ARRIVED_NOTE_AFTER: 60 });

/** Reminder tunables (MEL-528, "Remind me…"): the quick times offered and
 *  how the list at the top of Chats behaves. Change them here; there is no
 *  setting. How far ahead a reminder may be set is the server's
 *  (client/remind.go maxReminderAhead). */
export const REMIND = {
  soon: [30, 120] as readonly number[], // minutes ahead of the quick choices: "In 30 minutes", "In 2 hours"
  morning: 9,         // hour of "Tomorrow at 9:00" (this device's time)
  customAhead: 60,    // minutes ahead the "At a time I choose" field starts at
  listMax: 4,         // reminders shown at the top of Chats before "Show all"
  titleMax: 90,       // characters of a reminded message's first line in the list
} as const;

export type TopicState = "active" | "done" | "archived";

/** Topic is one of an agent's separate conversations (a device thread): each
 *  is its own reply chain, so its own session on the agent's side. */
export interface Topic {
  id: string;
  peer: string;
  title: string;
  autoTitle?: string;          // its first line, when you gave it a name of your own
  renamed: boolean;
  last: string;
  lastAt: string;
  unread: number;
  needsYou: number;            // decisions waiting for you in it
  waiting: boolean;            // a request in it waits for the agent, or the agent is working
  pending: boolean;            // anything in it is still open: never archived
  state: TopicState;
  doneBy?: "agent" | "you";
  conclusion?: string;         // the final reply that made it done, first line: the agent's, or yours when you answered by hand here
  concludedBy?: string;        // the device that sent it
  count: number;               // its messages, as this view shows them (a Mark done covers no later one)
  quietSince: string;          // when it went quiet: its last message, or a later Mark done / Reopen here
}

export const topicOf = (t: T.ThreadSummary): Topic => ({
  id: t.id, peer: t.peer, title: t.title, autoTitle: t.auto_title, renamed: !!t.renamed, last: t.last, lastAt: t.last_at, unread: t.unread,
  needsYou: t.review, waiting: t.waiting || t.running > 0, pending: t.pending,
  state: t.state === "done" || t.state === "archived" ? t.state : "active",
  doneBy: t.done_by === "agent" || t.done_by === "you" ? t.done_by : undefined, conclusion: t.conclusion, concludedBy: t.concluded_by,
  count: t.count, quietSince: t.quiet_since || t.last_at,
});

/** newestFirst orders topics (or anything with lastAt and id) most recently active first. */
export const newestFirst = <X extends { lastAt: string; id: string }>(a: X, b: X) =>
  Date.parse(b.lastAt) - Date.parse(a.lastAt) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0);

/** topicMark is a topic's state in one word, or null for an ordinary active
 *  one: what needs you comes first, then archived, done and waiting. */
export function topicMark(t: Topic): null | { key: "needs" | "archived" | "done" | "waiting"; word: string } {
  if (t.needsYou > 0) return { key: "needs", word: "Needs you" };
  if (t.state === "archived") return { key: "archived", word: "Archived" };
  if (t.state === "done") return { key: "done", word: "Done" };
  if (t.waiting) return { key: "waiting", word: "Waiting" };
  return null;
}

/** plain removes mention markup: [@Name](agentnet:...) reads as @Name. */
export const mentionRef = /\[@([^\[\]\r\n]{1,80})\]\(agentnet:(person|guest|agent)\/([A-Za-z0-9_-]{1,64})\)/g;
export const plain = (s: string | undefined) => (s || "").replace(mentionRef, (_m, name) => "@" + name);
export const firstLine = (s: string | undefined, n = 120) => {
  const l = plain(s).split("\n")[0].trim();
  return l.length > n ? l.slice(0, n - 1).trimEnd() + "…" : l;
};

/** deviceName turns an address (person/agent) into words: "admin/zenbook" → "zenbook". */
export const deviceName = (address: string) => {
  const i = address.indexOf("/");
  return i < 0 ? address : address.slice(i + 1);
};

/** Capitalised device name for display ("zenbook" → "Zenbook"). */
export const niceDevice = (address: string) => {
  const d = deviceName(address);
  return d ? d.charAt(0).toUpperCase() + d.slice(1) : d;
};

export const personName = (p: T.PersonView | undefined | null) => (p && (p.label || niceDevice(p.address))) || "Someone";

/** possessive: "Vitalii" → "Vitalii's"; "you" for this installation's person. */
export const owner = (p: T.PersonView | undefined | null, me?: T.PersonView | null) =>
  p && me && p.person && p.person === me.person ? "Your" : personName(p) + "’s";

export function chatList(o: T.Overview | null, agentNames: Record<string, string>): ChatItem[] {
  if (!o) return [];
  const items: ChatItem[] = [];
  // What waits for this device's decision in each chat (one decided on another device is not counted).
  const decide = new Map<string, number>();
  for (const c of o.needs_you || []) if (!c.decide_on) decide.set(c.conv, (decide.get(c.conv) || 0) + 1);
  for (const d of o.dms || []) {
    const group = d.kind === "group";
    items.push({
      key: "dm:" + d.id,
      open: { kind: "dm", id: d.id },
      kind: group ? "group" : "person",
      title: group ? d.title || "Group" : personName(d.peer),
      subtitle: group ? (d.members || []).map((m) => m.label).filter(Boolean).join(", ") : undefined,
      avatarSeed: group ? d.id : d.peer.person || d.peer.address,
      last: firstLine(d.last),
      lastAt: d.last_at,
      unread: d.unread,
      needsYou: decide.get(d.id) || 0,
      held: d.held,
      guests: 0,
      working: false,
      frozen: d.frozen,
      members: group ? (d.members || []).map((m) => m.label) : undefined,
    });
  }
  // One row per agent: its separate conversations are topics inside it.
  // Archived topics are only counted (overview.topics): an agent whose
  // topics are all archived keeps its row, which opens its latest topic.
  const byPeer = new Map<string, T.ThreadSummary[]>();
  for (const t of o.threads || []) {
    if (t.notice_only) continue; // another computer's reports: shown under OKs, not as a chat
    byPeer.set(t.peer, [...(byPeer.get(t.peer) || []), t]);
  }
  const counts = new Map((o.topics || []).map((c) => [c.peer, c]));
  for (const peer of new Set([...byPeer.keys(), ...counts.keys()])) {
    const ts = (byPeer.get(peer) || []).map(topicOf).sort(newestFirst), c = counts.get(peer);
    const latest = c && (!ts[0] || newestFirst(topicOf(c.latest), ts[0]) < 0) ? topicOf(c.latest) : ts[0];
    if (!latest) continue;
    const person = deviceOwner(peer, o);
    items.push({
      key: "agent:" + peer,
      open: { kind: "thread", id: latest.id, peer },
      kind: "agent",
      title: person ? agentName(undefined, agentNames, person, o.person) : niceDevice(peer),
      subtitle: person ? "on " + niceDevice(peer) : "Agent · no person linked",
      avatarSeed: peer,
      last: firstLine(latest.last),
      lastAt: latest.lastAt,
      unread: ts.reduce((n, t) => n + t.unread, 0) + (c?.archived_unread || 0),
      needsYou: ts.reduce((n, t) => n + t.needsYou, 0),
      held: 0,
      guests: 0,
      working: (byPeer.get(peer) || []).some((t) => t.running > 0),
      topics: ts,
      peer,
      topicTotal: c ? c.total : ts.length,
      keyChanged: (byPeer.get(peer) || []).some((t) => t.key_changed) || !!c?.latest.key_changed,
    });
  }
  return items.sort((a, b) => (b.lastAt || "").localeCompare(a.lastAt || ""));
}

/** deviceOwner is the person whose device an address is, as their signed roster names it. */
export function deviceOwner(address: string, o: T.Overview | null): T.PersonView | null {
  if (!o) return null;
  const holds = (p?: T.PersonView | null) => !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));
  if (holds(o.person)) return o.person || null;
  return (o.people || []).find(holds) || null;
}

/** personOf: the person a device address belongs to, as the overview knows it. */
export const personOf = (address: string, o: T.Overview | null): T.PersonView | null => (address ? deviceOwner(address, o) : null);

/** isMine: the address is this device, or another device of this person. */
export const isMine = (address: string, o: T.Overview | null) =>
  !!o && (address === o.me.address || (!!o.person && personOf(address, o) === o.person));

/** nameOf: "Vitalii" for vitalii/desk; the device in words when no person is known. */
export const nameOf = (address: string, o: T.Overview | null) => {
  const p = personOf(address, o);
  return p ? personName(p) : niceDevice(address) || "Someone";
};

/** agentSubtitle says whose device an address is, in words. */
export function agentSubtitle(address: string, o: T.Overview | null): string {
  if (!o) return "";
  const person = o.person && (o.person.devices || []).some((d) => d.address === address) ? o.person : null;
  if (person) return "Your agent · " + niceDevice(address);
  for (const p of o.people || []) {
    if ((p.devices || []).some((d) => d.address === address) || p.address === address) return personName(p) + "’s agent · " + niceDevice(address);
  }
  return niceDevice(address);
}

// ---- who is in a conversation ---------------------------------------------

export interface Participant {
  key: string;
  kind: "member" | "guest-person" | "guest-agent";
  name: string;
  subtitle: string;
  seed: string;
  isMe: boolean;
  admin?: boolean;
  state: string;               // the server's state word, e.g. "active", "invited"
  stateText?: string;          // the server's sentence for it
  saw?: number;                // how many messages were shared with it
  pid?: string;
  can: { dismiss?: boolean; decide?: boolean; ask?: boolean; leave?: boolean; end?: boolean };
  agent?: T.AgentView;
  guest?: T.GuestView;
}

export function participants(t: T.DMThread | null, o: T.Overview | null, agentNames: Record<string, string>): Participant[] {
  if (!t) return [];
  const me = o?.person;
  const out: Participant[] = [];
  if (t.kind === "group") {
    for (const m of t.members || []) {
      const isMe = !!me && m.person === me.person;
      out.push({ key: "m:" + (m.person || m.address), kind: "member", name: isMe ? "You" : m.label, subtitle: m.admin ? "Admin" : "Member", seed: m.person || m.address, isMe, admin: m.admin, state: "member", can: {} });
    }
  } else {
    if (me) out.push({ key: "me", kind: "member", name: "You", subtitle: "", seed: me.person || me.address, isMe: true, state: "member", can: {} });
    out.push({ key: "peer", kind: "member", name: personName(t.peer), subtitle: niceDevice(t.peer.address), seed: t.peer.person || t.peer.address, isMe: false, state: "member", can: {} });
  }
  for (const a of t.agents || []) {
    if (a.state === "dismissed" || a.state === "declined") continue;
    const name = (a.agent_id && agentNames[a.agent_id]) || owner(a.host, me) + " agent";
    out.push({
      key: "a:" + a.pid, kind: "guest-agent", name,
      subtitle: owner(a.host, me) + " agent · " + niceDevice(a.host.address),
      seed: a.agent_id || a.host.address, isMe: false, state: a.state, stateText: a.state_text, saw: (a.shared || []).length,
      pid: a.pid, can: { dismiss: a.can_dismiss, decide: a.can_decide, ask: a.can_ask }, agent: a,
    });
  }
  for (const g of t.guests || []) {
    if (g.state === "dismissed" || g.state === "declined") continue;
    out.push({
      key: "g:" + g.pid, kind: "guest-person", name: personName(g.host),
      subtitle: "Invited by " + personName(g.inviter), seed: g.host.person || g.host.address, isMe: !!g.host_here, state: g.state, stateText: g.state_text,
      saw: (g.shared || []).length, pid: g.pid, can: { decide: g.can_decide, leave: g.can_leave, end: g.can_end }, guest: g,
    });
  }
  return out;
}

// ---- messages ---------------------------------------------------------------

/** How a message's delivery reads for its sender: only what receipts prove. */
export function deliveryWord(state: string): string {
  switch (state) {
    case "queued": return "Sending";
    case "waiting": return "Waiting to send";
    case "custody": return "Sent";
    case "delivered": return "Delivered";
    case "quarantined": return "Couldn’t be verified";
    case "expired": return "Not delivered";
    case "failed": return "Not sent";
    default: return "";
  }
}

/** A request's progress, as its executing device reports it. */
export function jobWord(state: string | undefined): string {
  switch (state) {
    case "awaiting": return "Waiting for an OK";
    case "held": return "Waiting for an OK";
    case "pending": case "accepted": return "Accepted";
    case "running": return "Working…";
    case "answered": case "done": return "Done";
    case "needs_human": return "Needs a person";
    case "declined": return "Declined";
    case "failed": return "Didn’t finish";
    case "cancelled": return "Stopped";
    case "interrupted": return "Interrupted";
    case "expired": return "Expired";
    default: return "";
  }
}

export const sameDay = (a: string, b: string) => new Date(a).toDateString() === new Date(b).toDateString();

export const timeOf = (s: string) => new Date(s).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });

export const when = (s: string) => {
  if (!s) return "";
  const d = new Date(s), now = new Date();
  if (d.toDateString() === now.toDateString()) return timeOf(s);
  const days = (now.getTime() - d.getTime()) / 86400000;
  if (days < 6) return d.toLocaleDateString([], { weekday: "short" });
  return d.toLocaleDateString([], { month: "short", day: "numeric" });
};

export const dayLabel = (s: string) => {
  const d = new Date(s), now = new Date();
  if (d.toDateString() === now.toDateString()) return "Today";
  const y = new Date(now); y.setDate(now.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" });
};

export const size = (n: number) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : (n / (1 << 20)).toFixed(1) + " MB";

/** hue picks one of eight fixed hues for a seed (agents, avatars). */
export function hue(seed: string): number {
  let h = 0;
  for (const c of seed) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return h % 8;
}

export const initials = (name: string) => {
  const parts = name.replace(/[^\p{L}\p{N} ]/gu, " ").trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return "?";
  return (parts[0][0] + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase();
};

// ---- one wording for agents and devices (every screen uses these) ---------

export type DeviceKind = "laptop" | "server" | "phone";

/** deviceKind reads a device's kind from the name its owner gave it; no badge when the name says nothing. */
export function deviceKind(address?: string): DeviceKind | undefined {
  const n = deviceName(address || "").toLowerCase();
  if (/phone|pixel|android|mobile|iphone/.test(n)) return "phone";
  if (/laptop|book|mac|notebook/.test(n)) return "laptop";
  if (/server|srv|vps|cloud|nas|box|hub/.test(n)) return "server";
  return undefined;
}

/** agentName is what an agent is called everywhere: its own name when its
 *  owner gave it one, otherwise "Your agent" / "Vitalii's agent". */
export function agentName(agentId: string | undefined, names: Record<string, string>, host: T.PersonView | null | undefined, me: T.PersonView | null | undefined): string {
  return (agentId && names[agentId]) || owner(host, me) + " agent";
}

/** agentWhere says whose agent it is and where it runs: "Your agent · Zenbook". */
export function agentWhere(host: T.PersonView | null | undefined, address: string, me: T.PersonView | null | undefined): string {
  return owner(host, me) + " agent · " + niceDevice(address);
}

// ---- conversation items (overview.needs_you and .held, ui.ConvItem) -------
// Shared by the chat list's banner and the OKs screen.

/** Why a conversation item waits (client.Review*). */
export const Reason = { awaiting: "agent_awaiting", needsHuman: "agent_needs_human", invite: "agent_invite", heldTurn: "person_turn" } as const;

/** decidable: the items this device decides. One with decide_on is decided
 *  on that device (a browser runs no agent), so it is shown but never counted. */
export const decidable = (o: T.Overview | null) => (o?.needs_you || []).filter((c) => !c.decide_on);

export const chatOf = (conv: string, o: T.Overview | null) => (o?.dms || []).find((d) => d.id === conv) || null;

/** chatName: "your chat with Vitalii", "“Savannah rush order”": a chat by its name. */
export function chatName(conv: string, o: T.Overview | null) {
  const d = chatOf(conv, o);
  return !d ? "a chat" : d.kind === "group" ? (d.title ? "“" + d.title + "”" : "a group") : "your chat with " + personName(d.peer);
}

/** senderOf: who sent a conversation item, for a sentence ("You" for your own devices). */
export const senderOf = (c: T.ConvItem, o: T.Overview | null) => (isMine(c.peer, o) ? "You" : nameOf(c.peer, o));

/** convTitle: what a conversation item is, as one sentence that names who and what. */
export function convTitle(c: T.ConvItem, o: T.Overview | null): string {
  const who = senderOf(c, o);
  switch (c.reason) {
    case Reason.invite: return who + " invited your agent into " + chatName(c.conv, o);
    case Reason.needsHuman: return "Your agent needs you for " + (who === "You" ? "your " : who + "’s ") + (c.kind === "task" ? "task" : "question");
    case Reason.heldTurn: return who + (c.kind === "task" ? " gave you a task" : " asked you something");
    default: return who + (c.kind === "task" ? " gave your agent a task" : " asked your agent something");
  }
}

// ---- reminders ("Remind me later", on received messages; daemon only) ------

/** reminderOf: the pending reminder on message id, when this device keeps reminders. */
export const reminderOf = (o: T.Overview | null, id: string) =>
  (o?.remind && (o.reminders || []).find((r) => r.message === id)) || undefined;

/** dueText says when, on this device's clock: "today 15:00", "tomorrow 09:00", "Tue 7 Oct 09:00". */
export function dueText(when: string | Date, now = new Date()): string {
  const d = new Date(when);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (d.toDateString() === now.toDateString()) return "today " + time;
  const next = new Date(now);
  next.setDate(now.getDate() + 1);
  if (d.toDateString() === next.toDateString()) return "tomorrow " + time;
  const year = d.getFullYear() === now.getFullYear() ? {} : { year: "numeric" as const };
  return d.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short", ...year }) + " " + time;
}

/** timeZone names this device's time zone ("Europe/Kyiv"), or "" when the browser does not say. */
export function timeZone(): string {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { return ""; }
}

/** reminderTimes: the quick choices of "Remind me…", from now. */
export function reminderTimes(now = new Date()): { label: string; at: Date }[] {
  const soon = REMIND.soon.map((m) => ({ label: m < 60 ? "In " + m + " minutes" : "In " + m / 60 + (m === 60 ? " hour" : " hours"), at: new Date(now.getTime() + m * 60e3) }));
  const morning = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, REMIND.morning, 0);
  return [...soon, { label: "Tomorrow at " + REMIND.morning + ":00", at: morning }];
}

/** localInput is d as a datetime-local field's value, in this device's time. */
export const localInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

// ---- held back (overview.quarantine) ----------------------------------------

/** holdSentence says why a received message is held back (QuarantineItem.code),
 *  naming its sender; its content is never shown. browser: this device can't trust keys. */
export function holdSentence(code: string, name: string, browser = false): string {
  switch (code) {
    case "key_changed": return name + "’s identity changed. It waits until you check and trust the new one" + (browser ? " in AgentNet on your computer." : ".");
    case "proof_pending": return "It names a chat or a person this device can’t check yet. It waits here; nothing runs it.";
    case "identity_conflict": return "It disagrees with what this device knows about " + name + ". It stays held; nothing runs it.";
    case "conflicting_duplicate": return name + " sent different words under a message already received. It stays held; nothing runs it.";
    default: return "It couldn’t be verified, so it isn’t shown.";
  }
}
