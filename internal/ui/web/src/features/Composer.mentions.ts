// Mentions in the composer. A person mention travels inside the signed text
// as a readable reference to that exact person or guest participation:
// [@Name](agentnet:person/ID) or [@Name](agentnet:guest/PID) (the wire format
// app.js reads too). It is attention only, never routing or a grant. The
// addressed agent is the draft's `agent` (a pid); its "@Name" goes out as
// plain text, and the ask goes through askAgent.
//
// The draft keeps its text in this encoded form (agent mentions included, as
// agentnet:agent/PID), so exact mentions survive switching conversations and
// reloads; model.plain() reads it back as "@Name".
import type { T } from "../api";
import { deviceKind, mentionRef, participants, personName, type DeviceKind } from "../model";

export type RefKind = "person" | "guest" | "agent";

/** Span: one exact mention in the editable text, "@" + name at start. */
export interface Span { start: number; name: string; kind: RefKind; id: string }

export const mentionName = (s: string | undefined) => String(s || "").replace(/[\[\]\r\n]/g, "").trim().slice(0, 80);

const intact = (text: string, s: Span) =>
  /^[^\[\]\r\n]{1,80}$/.test(s.name) && /^[A-Za-z0-9_-]{1,64}$/.test(s.id) && text.slice(s.start, s.start + s.name.length + 1) === "@" + s.name;

/** decode turns a draft's text into what is edited ("@Name") and its exact mentions. */
export function decode(s: string): { text: string; spans: Span[] } {
  const spans: Span[] = [];
  let text = "", last = 0;
  for (const m of s.matchAll(mentionRef)) {
    text += s.slice(last, m.index);
    spans.push({ start: text.length, name: m[1], kind: m[2] as RefKind, id: m[3] });
    text += "@" + m[1];
    last = m.index + m[0].length;
  }
  return { text: text + s.slice(last), spans };
}

/** encode writes each intact mention as its reference; `plainAgents` writes
 *  agent mentions as the "@Name" they read as (what is sent). */
export function encode(text: string, spans: Span[], plainAgents = false): string {
  let out = text;
  for (const s of spans.filter((s) => intact(text, s)).sort((a, b) => b.start - a.start)) {
    if (plainAgents && s.kind === "agent") continue;
    out = out.slice(0, s.start) + "[@" + s.name + "](agentnet:" + s.kind + "/" + s.id + ")" + out.slice(s.start + s.name.length + 1);
  }
  return out;
}

/** shift follows one edit from prev to cur: mentions before or after it move
 *  with the text; one the edit touched is dropped (it reads as plain text now). */
export function shift(spans: Span[], prev: string, cur: string): { kept: Span[]; dropped: Span[] } {
  const max = Math.min(prev.length, cur.length);
  let p = 0, s = 0;
  while (p < max && prev[p] === cur[p]) p++;
  while (s < max - p && prev[prev.length - 1 - s] === cur[cur.length - 1 - s]) s++;
  const oldEnd = prev.length - s, delta = cur.length - prev.length;
  const kept: Span[] = [], dropped: Span[] = [];
  for (const m of spans) {
    const moved = m.start + m.name.length + 1 <= p ? m : m.start >= oldEnd ? { ...m, start: m.start + delta } : null;
    if (moved && intact(cur, moved)) kept.push(moved);
    else dropped.push(m);
  }
  return { kept, dropped };
}

/** trigger finds "@query" being typed right before the caret. */
export function trigger(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const m = before.match(/(?:^|\s)@([^@\s]{0,40})$/);
  return m ? { start: before.length - m[1].length - 1, query: m[1] } : null;
}

/** Candidate: someone the picker offers. */
export interface Candidate {
  key: string;
  kind: RefKind;
  id: string;            // person id, guest pid, or agent pid
  name: string;
  sub: string;           // who they are here, in words
  seed: string;
  device?: DeviceKind;   // an agent's device badge
}

/** guestAuthor is this installation's own joined guest participation in a
 *  conversation it is a temporary guest of (it sends with that pid). */
export const guestAuthor = (t: T.DMThread) =>
  t.role === "human_guest" ? (t.guests || []).find((g) => g.host_here && g.can_send) : undefined;

/** candidates are the agents that can be asked here and the people here
 *  besides you: members (or the DM's two people) and active guests. */
export function candidates(t: T.DMThread, o: T.Overview | null, agentNames: Record<string, string>): Candidate[] {
  const humanGuest = t.role === "human_guest";
  const out: Candidate[] = [];
  if (!humanGuest || guestAuthor(t)) {
    for (const p of participants(t, o, agentNames)) {
      if (p.kind !== "guest-agent" || p.state !== "active" || !p.can.ask || !p.pid) continue;
      const name = mentionName(p.name);
      if (name) out.push({ key: "a:" + p.pid, kind: "agent", id: p.pid, name, sub: p.subtitle, seed: p.seed, device: deviceKind(p.agent?.host.address) });
    }
  }
  const me = o?.person?.person;
  const group = t.kind === "group";
  const originals: (T.PersonView | T.GroupMemberView | undefined)[] = group ? t.members || []
    : humanGuest ? (t.members?.length ? t.members : [t.peer, ...(t.guests || []).map((g) => g.inviter)])
    : [t.peer];
  const people = new Map<string, Candidate>();
  for (const p of originals) {
    if (!p) continue;
    // A summary may arrive before its full peer view. Resolve only an exact
    // device from an already verified person; listed claims grant no identity.
    const known = !p.person && p.address ? o?.people?.find((x) => x.state === "pinned" && x.person &&
      (x.address === p.address || x.devices?.some((d) => d.address === p.address))) : undefined;
    const person = p.person || known?.person;
    if (!person || person === me) continue;
    const name = mentionName(p.label || known?.label);
    if (!name || people.has("p:" + person)) continue;
    const admin = "admin" in p && p.admin;
    people.set("p:" + person, { key: "p:" + person, kind: "person", id: person, name, sub: group ? (admin ? "Group admin" : "In this group") : "In this chat", seed: person });
  }
  for (const g of t.guests || []) {
    if (g.state !== "active" || g.host_here) continue;
    const name = mentionName(g.host.label);
    if (name) people.set("g:" + g.pid, { key: "g:" + g.pid, kind: "guest", id: g.pid, name, sub: "Guest · invited by " + personName(g.inviter), seed: g.host.person || g.host.address });
  }
  return [...out, ...people.values()];
}

/** matches ranks candidates for a query: name starts, then word starts, then
 *  anywhere; the closest (shortest) name first within each. */
export function matches(all: Candidate[], query: string): Candidate[] {
  const q = query.toLowerCase();
  if (!q) return all;
  const rank = (c: Candidate) => {
    const n = c.name.toLowerCase();
    return n.startsWith(q) ? 0 : n.split(/\s+/).some((w) => w.startsWith(q)) ? 1 : n.includes(q) ? 2 : -1;
  };
  return all.map((c) => [rank(c), c] as const).filter(([r]) => r >= 0)
    .sort((a, b) => a[0] - b[0] || a[1].name.length - b[1].name.length).map(([, c]) => c);
}
