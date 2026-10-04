// What the screens show, derived from the server's views. Nothing here
// decides anything: names are what people call themselves, states are
// what the server's records prove, and actions come from its can[] lists.
import type { T } from "./api";

// ---- people and agents ---------------------------------------------------

/** One chat in the list: a person (DM), a group, or an agent's own device thread. */
export interface ChatItem {
  key: string;                 // "dm:<id>" | "thread:<id>"
  open: { kind: "dm" | "thread"; id: string };
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
  topics?: Topic[];            // an agent's separate conversations, newest first
}

/** Topic is one of an agent's separate conversations (a device thread). */
export interface Topic { id: string; title: string; lastAt: string; unread: number }

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
  const byPeer = new Map<string, T.ThreadSummary[]>();
  for (const t of o.threads || []) {
    if (t.notice_only) continue; // another computer's reports: shown under OKs, not as a chat
    byPeer.set(t.peer, [...(byPeer.get(t.peer) || []), t]);
  }
  for (const [peer, ts] of byPeer) {
    ts.sort((a, b) => (b.last_at || "").localeCompare(a.last_at || ""));
    const latest = ts[0], person = deviceOwner(peer, o);
    items.push({
      key: "agent:" + peer,
      open: { kind: "thread", id: latest.id },
      kind: "agent",
      title: person ? agentName(undefined, agentNames, person, o.person) : niceDevice(peer),
      subtitle: person ? "on " + niceDevice(peer) : "Agent · no person linked",
      avatarSeed: peer,
      last: firstLine(latest.last),
      lastAt: latest.last_at,
      unread: ts.reduce((n, t) => n + t.unread, 0),
      needsYou: ts.reduce((n, t) => n + t.review, 0),
      held: 0,
      guests: 0,
      working: ts.some((t) => t.running > 0),
      topics: ts.map((t) => ({ id: t.id, title: firstLine(t.title, 60), lastAt: t.last_at, unread: t.unread })),
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
