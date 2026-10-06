// Who is in a conversation and what each guest was shown, derived only from
// the server's views: membership from the thread, guests and agents from
// their participations, permissions from their can_* flags. Shared by the
// "In this chat" panel and the "Bring someone in" sheet.
import type { T } from "../api";
import { agentName, agentWhere, deviceKind, firstLine, niceDevice, personName, type DeviceKind } from "../model";
import { eventKind } from "./Message.model";

/** online: true when the Hub lists the device as connected now, false when it
 *  lists it otherwise, null when nothing current is known. This computer is online. */
export function online(o: T.Overview | null, address: string): boolean | null {
  if (!o) return null;
  if (address === o.me.address) return true;
  if (!o.directory?.current) return null;
  const m = (o.directory.members || []).find((x) => x.address === address);
  return m ? m.presence === "connected" : null;
}

const holds = (p: T.PersonView | undefined | null, address: string) =>
  !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));

export const isMine = (o: T.Overview | null, address: string) => holds(o?.person, address) || address === o?.me.address;

/** callName is a name inside a sentence: "Bring your agent in". */
export const callName = (name: string) => name.replace(/^Your /, "your ");

/** shortName fits a button: an agent without a name of its own is "it". */
export const shortName = (g: { kind: string; name: string }) =>
  g.kind === "agent" && / agent$/.test(g.name) ? "it" : g.name;

/** them: the pronoun for a guest: "it" for an agent, "them" for a person. */
export const them = (kind: string) => (kind === "agent" ? "it" : "them");

export interface Speaker { name: string; seed: string; agent: boolean; me: boolean }

/** speaker says who wrote a message in this conversation, in words. */
export function speaker(m: T.DMMessage, t: T.DMThread, o: T.Overview | null, names: Record<string, string>): Speaker {
  const a = m.pid ? (t.agents || []).find((x) => x.pid === (m.agent_author_pid || m.pid)) : undefined;
  if (a && m.verified_agent && holds(a.host, m.from))
    return { name: agentName(a.agent_id, names, a.host, o?.person), seed: a.agent_id || a.host.address, agent: true, me: false };
  if (m.dir === "out" || isMine(o, m.from)) return { name: "You", seed: o?.person?.person || o?.me.address || "me", agent: false, me: true };
  const people = [t.peer, ...(t.members || []), ...(t.guests || []).map((g) => g.host)];
  const p = people.find((x) => holds(x, m.from));
  if (p) return { name: personName(p), seed: p.person || p.address, agent: false, me: false };
  return { name: niceDevice(m.from), seed: m.from, agent: false, me: false };
}

/** unmark drops Markdown emphasis and block markers from a one-line preview. */
const unmark = (s: string) => s.replace(/^(?:#{1,6}|>|[-*+]|\d+\.)\s+/, "").replace(/\*\*|__|~~|`/g, "").replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");

/** A message line for previews: its first line, or its files. */
export const lineOf = (m: T.DMMessage) => unmark(firstLine(m.text ?? m.body, 140)) || (m.attachments?.length ? "📎 " + m.attachments.map((f) => f.name).join(", ") : "");

/** The conversation's messages, without participation records. */
const timeline = (t: T.DMThread) => (t.messages || []).filter((m) => !m.event && !m.deleted && lineOf(m) !== "");

/** Messages a person could be shown: real messages here, not copies shared with this computer. */
export const shareable = (t: T.DMThread) => timeline(t).filter((m) => !m.excerpt_pid && !m.replica);

// ---- the room -----------------------------------------------------------------

export interface Member { email?: string; key: string; person?: string; name: string; seed: string; me: boolean; admin: boolean; online: boolean | null; note: string }

export interface Guest {
  key: string;
  pid: string;
  kind: "agent" | "person";
  member?: boolean;
  name: string;
  seed: string;
  who: string;                 // the invite sheet's key for this same person or agent ("Bring back")
  line: string;                // "Your agent · Laptop" / "Invited by you"
  device?: DeviceKind;
  online: boolean | null;
  where?: string;
  hostName: string;            // whose agent, or the person
  hostHere: boolean;           // this installation hosts it (it is me, or my agent)
  invitedBy: string;           // "you" or the inviter's name
  note?: string;               // the inviter's note ("Why?"), agents only
  state: string;
  stateText: string;
  shared: T.DMMessage[];       // exactly the earlier messages shared with it, as found here
  missing: number;             // shared, but not on this computer
  invitedAt?: string;
  sinceAt?: string;            // when it joined, from its participation records
  endedAt?: string;
  endedBy?: string;            // who ended it: "you", a name, or "" when it left on its own
  joined: boolean;             // it was in the room at some point (not only invited)
  working: boolean;
  can: { decide: boolean; dismiss: boolean; end: boolean; leave: boolean };
}

export interface Waiting { id: string; text: string; asker: string; agent: string; decider: string; mine: boolean }

export interface GroupInvite { id: string; target: string; name: string; state: string }

/** Outbound proposals are invitations, never members before publication. */
export function pendingGroupInvites(t: T.DMThread, o: T.Overview | null): GroupInvite[] {
  if (t.kind !== "group") return [];
  const members = new Set((t.members || []).map(m => m.person));
  const seen = new Set<string>();
  return (o?.group_invitations || []).filter(i => i.conv === t.id && i.direction === "out" &&
    (i.status === "pending" || i.status === "accepted") && !members.has(i.target)).flatMap(i => {
      if (seen.has(i.target)) return [];
      seen.add(i.target);
      return [{ id: i.id, target: i.target, state: i.status,
        name: o?.people?.find(p => p.person === i.target)?.label || "Person " + i.target.slice(0, 8) }];
    });
}

export interface Room {
  members: Member[];
  guests: Guest[];             // in the room now
  groupInvites: GroupInvite[]; // outbound member proposals, separate from guests
  invited: Guest[];            // not in yet
  waiting: Waiting[];          // requests here that wait for an owner's OK
  past: Guest[];               // dismissed, left or declined
}

const LIVE = new Set(["active", "conflict"]);
const PENDING = new Set(["invited", "pending"]);
const ENDED = new Set(["dismissed", "declined"]);

export function room(t: T.DMThread, o: T.Overview | null, names: Record<string, string>): Room {
  const me = o?.person;
  const msgs = t.messages || [];
  const events = (pid: string) => msgs.filter((m) => m.event && m.pid === pid).sort((a, b) => a.at.localeCompare(b.at));
  // A member's own records say when someone was invited, joined and left; a
  // guest's copies say only when they reached it, so a guest sees no "since".
  const member = !t.role || t.role === "member";
  // Its records say when it was invited, when it joined and who ended it
  // (the server's sentences, read by eventKind as the history dividers are).
  const times = (pid: string, state: string, invited?: string) => {
    const ev = events(pid);
    const last = ev.length > 1 ? ev[ev.length - 1].at : undefined;
    const kinds = ev.map((m) => eventKind(m.event || ""));
    const end = ENDED.has(state) ? kinds[kinds.length - 1] : null;
    return {
      invitedAt: ev[0]?.at || invited, sinceAt: member && LIVE.has(state) ? last : undefined, endedAt: ENDED.has(state) ? last : undefined,
      endedBy: end?.kind === "left" ? (/^you$/i.test(end.by) ? "you" : end.by) : undefined,
      joined: LIVE.has(state) || kinds.some((k) => k?.kind === "joined") || (state === "dismissed" && ev.length >= 3),
    };
  };
  const inviterWord = (p: T.PersonView) => (p.person && p.person === me?.person ? "you" : personName(p));
  const working = (pid: string) => msgs.some((m) => m.pid === pid && (m.exec?.state === "running" || m.state === "running"));

  const holders = t.members?.length ? t.members : [me, t.peer].filter((p): p is T.PersonView => !!p);
  const members: Member[] = holders.map((p) => {
    const self = !!me && !!p.person && p.person === me.person;
    const agents = (t.agents || []).filter((a) => LIVE.has(a.state) && a.host.person && a.host.person === p.person).flatMap((a) => (a.agent_id && names[a.agent_id] ? [names[a.agent_id]] : []));
    const admin = "admin" in p && !!(p as T.GroupMemberView).admin;
    return {
      key: p.person || p.address, person: p.person, email: p.email, name: personName(p), seed: p.person || p.address, me: self, admin,
      online: self ? null : online(o, p.address),
      note: agents.length ? agents.join(", ") + "’s owner" : "",
    };
  });

  const agents: Guest[] = (t.agents || []).map((a) => {
    const name = agentName(a.agent_id, names, a.host, me);
    return {
      key: "a:" + a.pid, pid: a.pid, kind: "agent", name, seed: a.agent_id || a.host.address,
      who: "a:" + a.host.address + "#" + (a.agent_id || ""),
      member: a.member,
      line: a.member ? "Invited by " + (a.inviters?.length ? a.inviters : [a.inviter]).map(inviterWord).join(" and ") : agentWhere(a.host, a.host.address, me), device: a.member ? undefined : deviceKind(a.host.address),
      where: a.host.person === me?.person ? "your computer" : niceDevice(a.host.address),
      online: online(o, a.host.address), hostName: a.host_here ? "you" : personName(a.host), hostHere: a.host_here,
      invitedBy: inviterWord(a.inviter), note: a.note?.trim() || undefined,
      state: a.state, stateText: a.state_text,
      shared: msgs.filter((m) => (a.shared || []).includes(m.id)), missing: a.missing,
      ...times(a.pid, a.state, a.invited && !a.invited.startsWith("0001") ? a.invited : undefined), working: working(a.pid),
      can: { decide: a.can_decide, dismiss: a.can_dismiss, end: false, leave: false },
    };
  });
  const people: Guest[] = (t.guests || []).map((g) => {
    const lids = new Set(g.shared || []);
    const shared = msgs.filter((m) => lids.has(m.lid || m.id));
    return {
      key: "g:" + g.pid, pid: g.pid, kind: "person", name: personName(g.host), seed: g.host.person || g.host.address,
      who: "p:" + (g.host.person || g.host.address),
      line: "Invited by " + inviterWord(g.inviter), invitedBy: inviterWord(g.inviter), device: undefined,
      online: g.host_here ? null : online(o, g.host.address), hostName: personName(g.host), hostHere: g.host_here,
      state: g.state, stateText: g.state_text, shared, missing: g.missing + (lids.size - shared.length),
      ...times(g.pid, g.state), working: false,
      can: { decide: g.can_decide, dismiss: false, end: g.can_end, leave: g.can_leave },
    };
  });
  const all = [...people, ...agents];
  const byTime = (a: Guest, b: Guest) => (b.endedAt || b.invitedAt || "").localeCompare(a.endedAt || a.invitedAt || "");

  const waiting: Waiting[] = [];
  for (const m of msgs) {
    if (!m.pid || (m.kind !== "task" && m.kind !== "question")) continue;
    const state = m.exec?.state || m.state;
    if (state !== "awaiting" && state !== "held") continue;
    const a = (t.agents || []).find((x) => x.pid === m.pid);
    if (!a) continue;
    waiting.push({
      id: m.id, text: lineOf(m), asker: speaker(m, t, o, names).name, agent: agentName(a.agent_id, names, a.host, me),
      decider: a.host_here ? "you" : personName(a.host), mine: a.host_here,
    });
  }

  return {
    members,
    guests: all.filter((g) => LIVE.has(g.state)),
    groupInvites: pendingGroupInvites(t, o),
    invited: all.filter((g) => PENDING.has(g.state)),
    waiting,
    // One row per person or agent: their latest visit.
    past: all.filter((g) => ENDED.has(g.state)).sort(byTime).filter((g, i, list) => list.findIndex((x) => x.who === g.who) === i),
  };
}

/** canBringIn: whether this person may invite anyone into t at all. */
export const canBringIn = (t: T.DMThread | null) => !!t && !t.frozen && (!t.role || t.role === "member");

// ---- what a guest was shown ---------------------------------------------------------

export interface Exposure { cells: boolean[]; label: string; range: string }

/** exposure draws what was shared against the conversation as it stood: one
 *  cell per message up to the last one shared (at most 14), filled when shared. */
export function exposure(t: T.DMThread, g: Guest, timeOf: (s: string) => string): Exposure {
  const n = g.shared.length + g.missing;
  // A guest's own copy of the chat may hold an earlier visit's messages, so
  // only a member's view can tell shared from private: a guest gets the count.
  if (t.role && t.role !== "member") {
    const s = g.shared;
    return { cells: [], label: n ? "Saw " + plural(n, "message") : "Saw nothing from before", range: s.length ? span(timeOf(s[0].at), timeOf(s[s.length - 1].at)) : "" };
  }
  const list = timeline(t);
  const ids = new Set(g.shared.map((m) => m.id));
  const idx = list.flatMap((m, i) => (ids.has(m.id) ? [i] : []));
  if (!idx.length) return { cells: [], label: g.missing ? "Saw " + plural(g.missing, "message") + " not on this computer" : "Saw nothing from before", range: "" };
  const first = idx[0], end = idx[idx.length - 1];
  const start = Math.max(0, end - 13);
  const cells = list.slice(start, end + 1).map((m) => ids.has(m.id));
  const before = g.invitedAt || "";
  const run = idx.length === end - first + 1 && !list.slice(end + 1).some((m) => before && m.at <= before);
  return {
    cells,
    label: run && !g.missing ? "Saw the last " + plural(n, "message") : "Saw " + plural(n, "message"),
    range: span(timeOf(list[first].at), timeOf(list[end].at)),
  };
}

export const plural = (n: number, word: string) => n + " " + word + (n === 1 ? "" : "s");

/** span joins two times, once when they read the same. */
export const span = (a: string, b: string) => (a === b ? a : a + "–" + b);

/** Labels describe a wait; only a unique known person can be its DM target. */
export function guestUpdatePeople(waiting: string[], candidates: (T.PersonView | null | undefined)[], self?: string) {
  const people = [...new Map(candidates.filter((p): p is T.PersonView => !!p).map(p => [p.person || p.address, p])).values()];
  return people.filter(p => p.person !== self && p.state !== "self" && waiting.includes(p.label) && people.filter(x => x.label === p.label).length === 1);
}

export function guestUpdateDraft(member: boolean) {
  return (member ? "Could you update AgentNet? Our chat needs it for a guest to join." : "Could you update AgentNet? I'd like to bring you into a chat.") + " Get AgentNet: https://github.com/misunders2d/agentnet/releases";
}
