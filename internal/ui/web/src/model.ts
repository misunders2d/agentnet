// What the screens show, derived from the server's views. Nothing here
// decides anything: names are what people call themselves, states are
// what the server's records prove, and actions come from its can[] lists.
import type { T } from "./api";

// Exact logical references survive a different local copy becoming canonical.
export function messageTarget<M extends { id: string; lid?: string }>(messages: M[], id?: string): M | undefined {
  if (!id) return;
  const exact = messages.find(m => m.id === id);
  if (exact) return exact;
  const logical = messages.filter(m => m.lid === id);
  return logical.length === 1 ? logical[0] : undefined;
}

// ---- people and agents ---------------------------------------------------

/** One chat in the list: a person (DM), a group, or an agent's own device thread. */
export interface ChatItem {
  key: string;                 // "dm:<id>" | "thread:<id>"
  open: { kind: "dm"; id: string; focus?: string } | { kind: "thread"; id: string; peer?: string };
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
  conversations?: T.DMSummary[]; // distinct signed conversations in one person's chat
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
  undoDelay:6000,     // confirmation can be undone before applying the action
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
  root?: boolean;
  conv?: string;
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
  pendingIDs?: string[];       // exact requests/notices keeping it open; navigation only
  state: TopicState;
  doneBy?: "agent" | "you" | "person";
  conclusion?: string;         // the final reply that made it done, first line: the agent's, or yours when you answered by hand here
  concludedBy?: string;        // the device that sent it
  count: number;               // its messages, as this view shows them (a Mark done covers no later one)
  quietSince: string;          // when it went quiet: its last message, or a later Mark done / Reopen here
}

export const topicOf = (t: T.ThreadSummary): Topic => ({
  id: t.id, conv:t.conv,peer: t.peer, title: t.title, autoTitle: t.auto_title, renamed: !!t.renamed, last: t.last, lastAt: t.last_at, unread: t.unread,
  needsYou: t.review, waiting: t.waiting || t.running > 0, pending: t.pending, pendingIDs: t.pending_ids,
  state: t.state === "done" || t.state === "archived" ? t.state : "active",
  doneBy: t.done_by === "agent" || t.done_by === "you" || t.done_by === "person" ? t.done_by : undefined, conclusion: t.conclusion, concludedBy: t.concluded_by,
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

/** niceDevice is a device in words, as every screen shows it (client.DeviceWords;
 *  vectors in internal/ui/testdata/device_words.json): dashes as spaces, the
 *  first letter capital, iPhone, iPad, iMac, Mac and MacBook as written
 *  ("zenbook" → "Zenbook", "windows-laptop" → "Windows laptop", "iphone" → "iPhone"). */
const deviceSpecial: Record<string, string> = { iphone: "iPhone", ipad: "iPad", imac: "iMac", mac: "Mac", macbook: "MacBook" };
export const niceDevice = (address: string) =>
  deviceName(address || "").split("-").filter(Boolean)
    .map((w, i) => deviceSpecial[w.toLowerCase()] || (i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(" ");

export const personName = (p: T.PersonView | undefined | null) => (p && (p.label || "Someone")) || "Someone";

/** possessive: "Vitalii" → "Vitalii's"; "you" for this installation's person. */
export const owner = (p: T.PersonView | undefined | null, me?: T.PersonView | null) =>
  p && me && p.person && p.person === me.person ? "Your" : personName(p) + "’s";

export function chatList(o: T.Overview | null, agentNames: Record<string, string>): ChatItem[] {
  if (!o) return [];
  const items: ChatItem[] = [];
  // What waits for this device's decision in each chat (one decided on another device is not counted).
  const decide = new Map<string, number>();
  for (const c of decidable(o)) decide.set(c.conv, (decide.get(c.conv) || 0) + 1);
  const chats = new Map<string, T.DMSummary[]>();
  for (const d of o.dms || []) {
    // Group/guest rooms keep their own audience. Only verified person IDs
    // group human DMs: matching display names never merge identities.
    const person = d.kind !== "group" && (!d.role || d.role === "member") && d.peer.person;
    const key = person ? "person:" + person : "dm:" + d.id;
    chats.set(key, [...(chats.get(key) || []), d]);
  }
  for (const [key, conversations] of chats) {
    conversations.sort((a, b) => (b.last_at || "").localeCompare(a.last_at || "") || a.id.localeCompare(b.id));
    // Opening an empty conversation must not hide a person's existing chat.
    const d = conversations.find((c) => c.count > 0) || conversations[0];
    const group = d.kind === "group";
    items.push({
      key,
      open: { kind: "dm", id: d.id, ...(d.last_id ? { focus: d.last_id } : {}) },
      kind: group ? "group" : "person",
      title: group ? d.title || "Group" : personName(d.peer),
      subtitle: group ? (d.members || []).map((m) => m.label).filter(Boolean).join(", ") : undefined,
      avatarSeed: group ? d.id : d.peer.person || d.peer.address,
      last: firstLine(d.last),
      lastAt: d.last_at,
      unread: conversations.reduce((n, c) => n + c.unread, 0),
      needsYou: conversations.reduce((n, c) => n + (decide.get(c.id) || 0), 0),
      held: conversations.reduce((n, c) => n + c.held, 0),
      guests: 0,
      working: false,
      frozen: d.frozen,
      members: group ? (d.members || []).map((m) => m.label) : undefined,
      conversations: key.startsWith("person:") ? conversations : undefined,
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
    const row = threadRow(peer, o, agentNames); // an agent only where one runs (MEL-529)
    items.push({
      key: "agent:" + peer,
      open: { kind: "thread", id: latest.id, peer },
      kind: row.kind,
      title: row.title,
      subtitle: row.subtitle,
      avatarSeed: row.seed,
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

// ---- people and devices (MEL-529, MEL-525) ----------------------------------
// A device runs an agent only when it says so: overview.agent_devices for
// the others (kept offline), me.agent for this one. A phone or a browser
// never does, so its messages are its person's. Every screen asks these
// helpers; none guesses that a device thread's peer is an agent. The hint
// is for display and offers only: it decides nothing that runs.

/** runsAgent: the device at address runs an agent people may ask. */
export function runsAgent(address: string, o: T.Overview | null | undefined): boolean {
  if (!o || !address) return false;
  if (address === o.me.address) return !!o.me.agent;
  return (o.agent_devices || []).includes(address);
}

/** agentPeers: the other devices that run an agent (for the Agents lists). */
export const agentPeers = (o: T.Overview | null | undefined) => new Set<string>(o?.agent_devices || []);

/** shortKey: a key's first group ("19c77bce"), to tell look-alike names apart. */
export const shortKey = (fp?: string) => String(fp || "").replace(/^SHA256:/i, "").trim().split(/[-\s]/)[0] || "";

/** Who a device is, as people see it. */
export interface DeviceWho {
  relation: "this" | "own" | "person" | "unknown";
  agent: boolean;              // it runs an agent (runsAgent)
  person: T.PersonView | null; // its person, as their record names it
  device: string;              // the device in words: "Pixel"
  verified: boolean;           // that record is checked here, not only listed by the server
  short: string;               // the key's first group, when the name alone could pass for someone else
  name: string;                // "You", "Vitalii", or the device when no person is known
}

/** deviceWho says whose a device is: this one, another of yours, another
 *  person's, or unknown. A person's name gets the key's first group when it
 *  is also another person's (or yours), or when the server only lists them. */
export function deviceWho(address: string, o: T.Overview | null | undefined): DeviceWho {
  const device = niceDevice(address), agent = runsAgent(address, o);
  if (o && address === o.me.address) return { relation: "this", agent, person: o.person || null, device, verified: true, short: "", name: "You" };
  const p = deviceOwner(address, o || null);
  if (!p) return { relation: "unknown", agent, person: null, device, verified: false, short: "", name: device || "Someone" };
  if (o?.person && (p === o.person || (!!p.person && p.person === o.person.person))) return { relation: "own", agent, person: p, device, verified: true, short: "", name: "You" };
  const verified = p.state !== "listed";
  const label = (p.label || "").trim().toLowerCase();
  const same = (x: T.PersonView) => (!!x.person && x.person === p.person) || x.address === p.address;
  const clash = !!label && [o?.person, ...(o?.people || [])].some((x) => !!x && !same(x) && (x.label || "").trim().toLowerCase() === label);
  const fp = (p.devices || []).find((d) => d.address === address)?.fingerprint || (p.address === address ? p.fingerprint : "") || "";
  return { relation: "person", agent, person: p, device, verified, short: clash || !verified ? shortKey(fp) : "", name: personName(p) };
}

/** whoName: "Vitalii", or "Sergey · 19c77bce" when the name needs its key. */
export const whoName = (w: DeviceWho) => (w.short ? w.name + " · " + w.short : w.name);

/** One device thread's row: an agent where one runs, else the person. */
export interface ThreadRow { kind: "agent" | "person"; title: string; subtitle: string; seed: string; local: boolean }

/** threadRow names a device thread for the chat list and its header:
 *  - a device that runs an agent: that agent ("Your agent" · "on Zenbook"),
 *    or the device when its person is not known ("Bezos" · "Agent · not linked to a person");
 *  - another device of yours that runs none, while this one runs an agent:
 *    this computer's agent, asked from there ("Your agent" · "On this computer · asked from your Pixel");
 *  - another device of yours otherwise: "Your Pixel";
 *  - another person's device that runs none: that person ("Vitalii" · "from Phone"). */
export function threadRow(peer: string, o: T.Overview | null | undefined, names: Record<string, string>): ThreadRow {
  const w = deviceWho(peer, o), me = o?.person;
  if (w.agent) {
    if (!w.person) return { kind: "agent", title: w.device || "Agent", subtitle: "Agent · not linked to a person", seed: peer, local: false };
    return { kind: "agent", title: agentName(undefined, names, w.person, me) + (w.short ? " · " + w.short : ""), subtitle: "on " + w.device, seed: peer, local: false };
  }
  if (w.relation === "own" || w.relation === "this") {
    if (o?.me.agent) return { kind: "agent", title: "Your agent", subtitle: "On this computer · asked from your " + w.device, seed: o.me.address, local: true };
    return { kind: "person", title: "Your " + w.device, subtitle: "Your device", seed: peer, local: false };
  }
  if (w.relation === "person") return { kind: "person", title: whoName(w), subtitle: "from " + w.device, seed: w.person?.person || peer, local: false };
  return { kind: "person", title: w.device || "Someone", subtitle: "Not linked to a person", seed: peer, local: false };
}

/** Who wrote one message of a device thread. */
export interface ThreadAuthor { key: string; name: string; sub?: string; agent: boolean; mine: boolean; seed: string }

/** threadAuthor: who wrote m in the device thread with peer (all: the thread's messages).
 *  - from another device of yours that runs no agent: you ("You" · "from Pixel", on your side);
 *  - from another person's device that runs none: that person;
 *  - from a device that runs an agent: that agent;
 *  - sent here, as an answer or result to a device that runs none: this computer's
 *    agent, unless its request was answered by hand;
 *  - anything else sent here: you. */
export function threadAuthor(m: T.Message, o: T.Overview | null | undefined, names: Record<string, string>, peer: string, all: T.Message[] = []): ThreadAuthor {
  const me = o?.person;
  if (m.dir === "out") {
    if (o?.me.agent && (peer === o.me.address || !runsAgent(peer, o)) && (m.kind === "answer" || m.kind === "result")) {
      const req = all.find((x) => x.id === m.reply_to);
      if (!req || req.state !== "manual") return { key: "local:" + o.me.address, name: (m.agent_id && names[m.agent_id]) || "Your agent", sub: "On this computer", agent: true, mine: false, seed: m.agent_id || o.me.address };
    }
    return { key: "me", name: "You", agent: false, mine: true, seed: me?.person || "me" };
  }
  const w = deviceWho(m.from, o);
  if (w.agent) {
    const row = threadRow(m.from, o, names);
    return { key: "in:" + m.from + "#" + (m.agent_id || ""), name: (m.agent_id && names[m.agent_id]) || row.title, sub: "on " + w.device, agent: true, mine: false, seed: m.agent_id || m.from };
  }
  if (w.relation === "own" || w.relation === "this") return { key: "me:" + m.from, name: "You", sub: "from " + w.device, agent: false, mine: true, seed: me?.person || "me" };
  if (w.relation === "person") return { key: "p:" + (w.person?.person || m.from), name: whoName(w), sub: "from " + w.device, agent: false, mine: false, seed: w.person?.person || m.from };
  return { key: "p:" + m.from, name: w.device || "Someone", agent: false, mine: false, seed: m.from };
}

/** Keep follow-ups addressed to the named agent recorded in this thread's
 * verified messages. The send API resolves it against the current catalog. */
export function threadAgentID(thread: T.Thread): string | undefined {
  for (const m of [...(thread.messages || [])].reverse()) {
    if ((m as T.Message & { _local?: boolean })._local) continue; // pending preview carries no signed target
    if (m.dir === "out" && (m.kind === "question" || m.kind === "task"))
      return m.target?.address === thread.peer ? m.target.agent_id : undefined;
    // A paged view may contain the answer without its original question.
    if (m.from === thread.peer && (m.kind === "answer" || m.kind === "result") && m.agent_id)
      return m.agent_id;
  }
  return undefined;
}

/** What a device thread's composer may send to peer. */
export type DeviceTarget =
  | { kind: "agent" }                  // ask it, or give it a task
  | { kind: "own"; note: string }      // your own device that runs no agent: answer its requests by hand only
  | { kind: "person"; name: string };  // a person's device that runs no agent: a plain message only

/** deviceTarget: a device that runs no agent cannot be asked anything. */
export function deviceTarget(peer: string, o: T.Overview | null | undefined): DeviceTarget {
  const w = deviceWho(peer, o);
  if (w.agent) return { kind: "agent" };
  if (w.relation === "own" || w.relation === "this")
    return { kind: "own", note: "This is your own " + w.device + ". " + (o?.me.agent ? "Questions you ask there come to this computer’s agent." : "It runs no agent, so there is nothing to ask it.") };
  return { kind: "person", name: w.relation === "person" ? whoName(w) : w.device || "this device" };
}

/** askerWords: who asked, for a sentence ("Question from you · Pixel",
 *  "… from Vitalii · Phone", "… from Vitalii’s agent"). */
export function askerWords(address: string, o: T.Overview | null | undefined, names: Record<string, string>): { name: string; device: string; agent: boolean; you: boolean } {
  const w = deviceWho(address, o);
  if (w.agent) return { name: threadRow(address, o, names).title, device: w.device, agent: true, you: false };
  const you = w.relation === "own" || w.relation === "this";
  return { name: you ? "you" : whoName(w), device: w.device, agent: false, you };
}

/** bringInDevices: the devices of p whose default agent may be brought in:
 *  only devices that run an agent and publish no named agents (those are
 *  offered one by one). A phone or a browser is never offered. */
export function bringInDevices(p: T.PersonView, o: T.Overview | null | undefined, remote: Record<string, unknown[] | undefined>): string[] {
  const devices = (p.devices || []).length ? (p.devices || []).map((d) => d.address) : [p.address];
  return devices.filter((a) => runsAgent(a, o) && !(remote[a] || []).length);
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

export const scopeMatches = (p: { topic?: string }, topic: string) => p.topic === undefined || p.topic === topic;

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
    const name = agentName(a.agent_id, agentNames, a.host, me);
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

/** Name agents with their owner even in compact mentions. Unnamed own agents
 *  on other devices also retain the host name. */
export function agentName(agentId: string | undefined, names: Record<string, string>, host: T.PersonView | null | undefined, me: T.PersonView | null | undefined): string {
  const name = agentId && names[agentId];
  if (name) return (host ? owner(host, me) + " " : "") + name;
  const remoteOwn = host?.person && host.person === me?.person && host.address && host.address !== me.address;
  return owner(host, me) + " agent" + (remoteOwn ? " on " + niceDevice(host.address) : "");
}

/** agentWhere says whose agent it is and where it runs: "Your agent · Zenbook". */
export function agentWhere(host: T.PersonView | null | undefined, address: string, me: T.PersonView | null | undefined): string {
  return owner(host, me) + " agent · " + niceDevice(address);
}

// ---- conversation items (overview.needs_you and .held, ui.ConvItem) -------
// Shared by the chat list's banner and the OKs screen.

/** Why a conversation item waits (client.Review*). */
export const Reason = { awaiting: "agent_awaiting", needsHuman: "agent_needs_human", interrupted: "agent_interrupted", invite: "agent_invite", heldTurn: "person_turn" } as const;

/** Working is stoppable, but never an outstanding approval. */
export const isWorkingItem = (c: T.ConvItem) => (c.actions || []).includes("cancel");
/** Device requests use review, with a reason set from their local job state. */
export const isWorkingReview = (r: T.ReviewItem) => !r.notice && r.reason === "agent_running";

/** A current human device can explicitly resolve its own host's exact report
 * item. This only offers the decision; the host checks roster, key and attempt. */
export function ownReportResolution(x: T.ReportItem, host: string, o: T.Overview | null): T.ContinuationAction | null {
  const me = o?.person;
  if (!me || me.state !== "self" || x.state !== "needs_human" || x.attempt < 1 || !["question", "task"].includes(x.kind) || !x.key || host === o?.me.address) return null;
  if (!me.devices?.some(d => d.address === o?.me.address && d.human) || !me.devices.some(d => d.address === host)) return null;
  return {id:x.id,key:x.key,host,attempt:x.attempt};
}
/** Items this device decides; other-device and running items stay visible apart. */
export const decidable = (o: T.Overview | null) => (o?.needs_you || []).filter((c) => !c.decide_on && !isWorkingItem(c));

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
    case Reason.invite: return who + (c.role === "human" ? " invited you into " : " invited your agent into ") + chatName(c.conv, o);
    case Reason.needsHuman: return "Your agent couldn’t finish — it needs your answer";
    case Reason.interrupted: return "Your agent was interrupted — run it again if needed";
    case Reason.heldTurn: return who + (c.kind === "task" ? " gave you a task" : " asked you something");
    default: return who + (c.kind === "task" ? " gave your agent a task" : " asked your agent something");
  }
}

// ---- reminders ("Remind me later", on received messages; daemon only) ------

/** reminderOf: the pending reminder on message id, when this device keeps reminders. */
export const reminderOf = (o: T.Overview | null, id: string) =>
  (o?.remind && (o.reminders || []).find((r) => r.message === id)) || undefined;

/** clock says a time of day as this device writes it: "9:00 AM", "15:00". */
export const clock = (d: Date) => d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });

/** dueText says when, on this device's clock: "today 15:00", "tomorrow 9:00", "Tue 7 Oct 9:00". */
export function dueText(when: string | Date, now = new Date()): string {
  const d = new Date(when);
  const time = clock(d);
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
  return [...soon, { label: "Tomorrow at " + clock(morning), at: morning }];
}

/** localInput is d as a datetime-local field's value, in this device's time. */
export const localInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

// ---- held back (overview.quarantine) ----------------------------------------

/** holdVerified: whether a held message's sender is known (QuarantineItem.code).
 *  One that didn't verify ("unverified", or a code this page doesn't know)
 *  only claims who sent it, so it is never shown as that person. */
export const holdVerified = (code: string) =>
  ["key_changed", "proof_pending", "identity_conflict", "conflicting_duplicate"].includes(code);

/** holdSentence says why a received message is held back (QuarantineItem.code),
 *  naming its sender; its content is never shown. browser: this device can't trust keys. */
export function holdSentence(code: string, name: string, browser = false): string {
  switch (code) {
    case "invalid": return "This message failed a check and was kept out of the chat. Its contents stay hidden and it cannot start any work.";
    case "key_changed": return name + "’s identity changed. It waits until you check and trust the new one" + (browser ? " in AgentNet on your computer." : ".");
    case "proof_pending": return "It names a chat or a person this device can’t check yet. It waits here; nothing runs it.";
    case "identity_conflict": return "It disagrees with what this device knows about " + name + ". It stays held; nothing runs it.";
    case "conflicting_duplicate": return name + " sent different words under a message already received. It stays held; nothing runs it.";
    default: return "It says it’s from " + name + ", but that couldn’t be checked, so it isn’t shown.";
  }
}
