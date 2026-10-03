// Words for decisions: who asked, which agent of yours, what happens if
// you allow it. Everything is read from the server's views; the phase of a
// request comes from its actions[] (what may be done now) and stored state.
import type { T } from "../api";
import { agentName, agentWhere, firstLine, niceDevice, personName, plain } from "../model";

export type Req = T.DMMessage | T.Message;

export const isThreadMsg = (m: Req): m is T.Message => "author" in m;
const isRequest = (m: Req) => m.kind === "question" || m.kind === "task";

/** The person a device address belongs to, as the overview knows it. */
export function personOf(address: string, o: T.Overview | null): T.PersonView | null {
  if (!o || !address) return null;
  const has = (p: T.PersonView) => p.address === address || (p.devices || []).some((d) => d.address === address);
  if (o.person && has(o.person)) return o.person;
  return (o.people || []).find(has) || null;
}

export const isMine = (address: string, o: T.Overview | null) =>
  !!o && (address === o.me.address || (!!o.person && personOf(address, o) === o.person));

/** "Vitalii" for vitalii/desk; the device in words when no person is known. */
export const nameOf = (address: string, o: T.Overview | null) => {
  const p = personOf(address, o);
  return p ? personName(p) : niceDevice(address) || "Someone";
};

/** "your Phone", "Vitalii’s Desk": a device in a sentence (its name as shown everywhere). */
export function deviceWords(address: string, o: T.Overview | null) {
  if (isMine(address, o)) return "your " + niceDevice(address);
  const p = personOf(address, o);
  return p ? personName(p) + "’s " + niceDevice(address) : niceDevice(address);
}

export const capital = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

/** "Your agent" → "your agent", for the middle of a sentence; own names stay. */
export const inSentence = (name: string) => name.replace(/^Your /, "your ");

/** The participation a DM request went through. */
export const participationOf = (m: Req, dm?: T.DMThread | null) =>
  !isThreadMsg(m) && m.pid ? (dm?.agents || []).find((a) => a.pid === m.pid) : undefined;

/** Which of your agents a request is for, as model.agentName calls it
 *  everywhere: its own name, else "Your agent". named says which. */
export function myAgent(m: Req, names: Record<string, string>, o: T.Overview | null, dm?: T.DMThread | null) {
  const a = participationOf(m, dm);
  const id = a?.agent_id || m.target?.agent_id;
  const named = !!id && !!names[id];
  const me = o?.person;
  const name = me ? agentName(id, names, me, me) : (id && names[id]) || "Your agent";
  const address = a?.host.address || m.target?.address || o?.me.address || "";
  return { name, named, address, where: me ? agentWhere(me, address, me) : niceDevice(address) };
}

/** myAgentName: the same, in a sentence ("your agent", "Ledger"). */
export const myAgentName = (m: Req, names: Record<string, string>, o: T.Overview | null, dm?: T.DMThread | null) =>
  inSentence(myAgent(m, names, o, dm).name);

/** The seed for an agent's face: its id when named, else the device it runs on. */
export function myAgentSeed(m: Req, o: T.Overview | null, dm?: T.DMThread | null) {
  const a = participationOf(m, dm);
  return a?.agent_id || m.target?.agent_id || a?.host.address || m.target?.address || o?.me.address || "agent";
}

/** Who asked. name reads in a sentence ("Vitalii", "Vitalii’s desk"); title
 *  and sub label their face ("Desk", "Vitalii’s agent"). */
export interface Asker { name: string; title: string; sub: string; person: string; seed: string; agent: boolean; you: boolean }

/** Who asked: a person in a chat, or the agent on the other end of a device thread. */
export function askerOf(m: Req, o: T.Overview | null, names: Record<string, string>, dm?: T.DMThread | null): Asker {
  if (isThreadMsg(m)) {
    if (m.dir === "out") return { name: "You", title: "You", sub: "", person: "You", seed: o?.person?.person || o?.me.address || "me", agent: false, you: true };
    const a = peerAgent(m.from, o, names, m.agent_id);
    return { name: a.name, title: a.title, sub: a.sub, person: a.owner || a.name, seed: m.agent_id || m.from, agent: true, you: false };
  }
  const you = m.dir === "out" || isMine(m.from, o);
  const p = you ? o?.person : [dm?.peer, ...(dm?.members || []), ...(o?.people || [])].find((x) => x && (x.address === m.from || (x.devices || []).some((d) => d.address === m.from)));
  const name = you ? "You" : p ? personName(p) : niceDevice(m.from);
  return { name, title: name, sub: "", person: name, seed: p?.person || p?.address || m.from, agent: false, you };
}

/** The agent at the other end of a device conversation, in the shared
 *  wording: name for a sentence ("Bohdan’s agent", "your agent on Phone"),
 *  title and sub for its face ("Bohdan’s agent" / "On Desk"). */
export function peerAgent(address: string, o: T.Overview | null, names: Record<string, string>, agentId?: string) {
  const owner = personOf(address, o);
  const me = o?.person;
  const own = !!agentId && !!names[agentId];
  if (own) return { name: names[agentId!], title: names[agentId!], sub: owner ? agentWhere(owner, address, me) : "On " + niceDevice(address), owner: owner ? personName(owner) : "" };
  if (isMine(address, o)) return { name: "your agent on " + niceDevice(address), title: "Your agent", sub: "On " + niceDevice(address), owner: "You" };
  if (owner) {
    const name = agentName(undefined, names, owner, me);
    return { name, title: name, sub: "On " + niceDevice(address), owner: personName(owner) };
  }
  return { name: "the agent on " + niceDevice(address), title: niceDevice(address), sub: "An agent", owner: "" };
}

/** Where a request was asked, for a sentence: "in your chat with Vitalii". */
export function placeOf(dm?: T.DMThread | null, thread?: T.Thread | null, o?: T.Overview | null) {
  if (dm) return dm.kind === "group" ? "in " + (dm.title || "the group") : "in your chat with " + personName(dm.peer);
  if (thread) return "from " + deviceWords(thread.peer, o || null);
  return "";
}

/** The phase a request to your agent is in, from what may be done now. */
export type Phase = "decide" | "running" | "needs_human" | "stopped" | "closing";

export function phaseOf(m: Req): Phase | null {
  const acts = m.actions || [];
  if (!acts.length) return null;
  if (acts.includes("cancel")) return "running";
  if (!isRequest(m)) return acts.includes("resolve") ? "closing" : null; // a report, or a follow-up that needs a person
  const state = m.dir === "in" ? m.state || "" : m.exec?.state || "";
  if (acts.includes("resolve") || state === "needs_human") return "needs_human";
  if (["interrupted", "failed", "cancelled"].includes(state)) return "stopped";
  return "decide";
}

/** Requests that wait for someone else's OK, as the executing device said. */
export function waitsElsewhere(m: Req): boolean {
  return isRequest(m) && !(m.actions || []).length && !!m.exec && ["awaiting", "held"].includes(m.exec.state) && !m.exec.stale;
}

export const requestText = (m: Req) => plain(m.deleted ? "" : m.edited && typeof m.text === "string" ? m.text : m.body);
export const headlineOf = (m: Req) => firstLine(requestText(m), 140);
export const longer = (m: Req) => requestText(m).trim() !== headlineOf(m);

export const kindWord = (kind: string) => (kind === "task" ? "task" : kind === "question" ? "question" : "message");

/** Plain words for a review item's reason: names instead of addresses. */
export function whyWords(why: string, peer: string, o: T.Overview | null) {
  if (/^Tasks run only if you accept them/.test(why)) return "Runs only if you allow it";
  if (/is not approved for automatic answers$/.test(why)) return "Answered only if you allow it";
  return capital(why.split(peer).join(nameOf(peer, o)));
}
