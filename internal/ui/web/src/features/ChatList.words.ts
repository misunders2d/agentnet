// The chat list's words: rows named by who is in them, told apart when two
// read the same, and participation records said plainly. Everything here is
// read from the overview; nothing is fetched.
import type { T } from "../api";
import { agentName, chatList, firstLine, personName, when, type ChatItem } from "../model";
import { eventKind } from "./Message.model";

/** A chat list row: the shared item, plus what tells it apart from a row
 *  with the same name and whether its last line is a record of who came or left. */
export interface ListItem extends ChatItem {
  note?: string;       // "Started 10:34 PM", or a device thread's first line
  event?: boolean;     // the last line is a participation record, in words
}

/** personAt is the person a device address belongs to (you, or someone known here). */
export function personAt(address: string, o: T.Overview | null): T.PersonView | null {
  const holds = (p?: T.PersonView | null) => !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));
  if (holds(o?.person)) return o!.person!;
  return (o?.people || []).find(holds) || null;
}

/** chatItems is the chat list as this screen shows it: device threads named
 *  after their agent ("Vitalii’s agent", "on Desk"), records of who came
 *  or left in plain words, and rows that would read the same told apart. */
export function chatItems(o: T.Overview | null, names: Record<string, string>): ListItem[] {
  const dms = new Map((o?.dms || []).map((d) => [d.id, d]));
  const threads = new Map((o?.threads || []).map((t) => [t.id, t]));
  const items: ListItem[] = chatList(o, names).map((i) => {
    if (i.open.kind === "thread") return i; // named in model.chatList: one row per agent, its threads as topics
    const line = eventLine(dms.get(i.open.id)?.last || "", i.open.id, o, names);
    const note = (i.conversations?.length || 0) > 1 ? i.conversations!.length + " conversations" : undefined;
    return line ? { ...i, note, last: line, event: true } : { ...i, note };
  });

  // Rows with the same name: DMs say when they started, device threads what they began with.
  const same = new Map<string, ListItem[]>();
  for (const i of items) same.set(i.kind + "\n" + i.title, [...(same.get(i.kind + "\n" + i.title) || []), i]);
  for (const group of same.values()) {
    if (group.length < 2) continue;
    const started = (i: ListItem, exact: boolean) => {
      const at = dms.get(i.open.id)?.created || "";
      return at ? "Started " + (exact ? new Date(at).toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) : when(at)) : "";
    };
    const coarse = group.map((i) => started(i, false));
    const exact = new Set(coarse).size < coarse.length;
    for (const i of group) {
      if (i.open.kind === "dm") i.note = started(i, exact);
      else {
        const first = firstLine(threads.get(i.open.id)?.title, 80);
        if (first && first !== i.last) i.note = "“" + first + "”";
      }
    }
  }
  return items;
}

const address = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/;

/** nameFor turns a name the server wrote into the one people see here:
 *  "You" stays you, an address becomes its person's name, an unknown one "someone". */
function nameFor(word: string, o: T.Overview | null): string {
  const w = word.trim();
  if (!w || /^(Someone not in this DM|A DM member|a person|A person)$/.test(w)) return "someone";
  if (/^you$/i.test(w)) return "you";
  if (address.test(w)) {
    const p = personAt(w, o);
    return !p ? "someone" : p === o?.person ? "you" : personName(p);
  }
  return w;
}

/** agentWords: what the server called an invited agent, without its device's address. */
function agentWords(s: string): string {
  const w = s.replace(/\s*\(on [^)]*\)$/, "").trim();
  if (/^an outside host’?'?s agent$/i.test(w) || /^an unknown person’?'?s agent$/i.test(w)) return "an agent";
  return w;
}

/** agentIn names the one agent in conv now in this state, when exactly one is. */
function agentIn(conv: string, state: string, o: T.Overview | null, names: Record<string, string>): string {
  const hits: { p: T.PersonView; a: T.AgentLink }[] = [];
  for (const p of [o?.person, ...(o?.people || [])]) {
    if (!p) continue;
    for (const a of p.agents || []) if ((a.dms || []).some((d) => d.conv === conv && d.state === state)) hits.push({ p, a });
  }
  return hits.length === 1 ? agentName(hits[0].a.agent_id, names, hits[0].p, o?.person) : "";
}

const cap = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

// The server's sentences for participation records, whole (internal/ui/liveagent.go
// eventText, humanEventText); a message that only resembles one stays as written.
const records = [
  /^.+ invited .+ into this (DM|group)\.$/,
  /^.+ accepted: the agent (joins this DM|participates in this group)\.$/,
  /^Outside host \S+ accepted participation in this group\.$/,
  /^.+ declined the invitation( for the agent)?\.$/,
  /^.+ dismissed the agent: it gets nothing more from this (DM|group)\.$/,
  /^.+ joined this DM\.$/,
  /^.+ left this DM\.$/,
  /^.+ removed .+ from this DM\.$/,
];

/** eventLine says a participation record in plain words ("An agent left ·
 *  dismissed by you"), or null when the line is not one. */
export function eventLine(text: string, conv: string, o: T.Overview | null, names: Record<string, string>): string | null {
  if (/^A (participation record|record about an agent)( not shared here yet| that cannot be read here| \([a-z_]+\))\.$/.test(text)) return "Who’s in this chat changed";
  if (!records.some((re) => re.test(text))) return null;
  const k = eventKind(text);
  if (!k) return null;
  const by = nameFor(k.by, o);
  const agent = / the agent|agent \(on |^Outside host /.test(text);
  switch (k.kind) {
    case "invited": {
      const who = /^.+? invited (.+) into this (DM|group)\.$/.exec(text)?.[1] || "";
      const whom = agent ? (agentIn(conv, "invited", o, names) || agentWords(who)).replace(/^Your /, "your ") : nameFor(who, o);
      return cap(by) + " invited " + whom;
    }
    case "joined":
      if (agent) return cap(agentIn(conv, "active", o, names) || "an agent") + " joined to help";
      return cap(nameFor(/^(.+) joined this DM\.$/.exec(text)?.[1] || "", o)) + " joined";
    case "declined":
      if (agent) { // only an agent's owner declines for it: a named agent says whose already
        const named = agentIn(conv, "declined", o, names);
        return named ? cap(named) + " won’t join" : "The agent won’t join · declined by " + by;
      }
      return cap(by) + " won’t join";
    case "left": {
      if (/ dismissed the agent/.test(text)) return cap(agentIn(conv, "dismissed", o, names) || "an agent") + " left · dismissed by " + by;
      const removed = /^.+? removed (.+) from this DM\.$/.exec(text)?.[1];
      if (removed) return cap(nameFor(removed, o)) + " left · dismissed by " + by;
      return cap(nameFor(/^(.+) left this DM\.$/.exec(text)?.[1] || "", o)) + " left";
    }
  }
  return null;
}
