// What one message in the open conversation says, in words: who wrote it,
// what it asked of an agent and how far that got, where its copies are.
// DM messages (T.DMMessage) and device-thread messages (T.Message) differ in
// shape; everything here reads both. Nothing decides: states are the
// server's, actions come from its can[] and actions[] lists.
import type { T } from "../api";
import { agentName, agentWhere, deliveryWord, deviceKind, deviceWho, jobWord, niceDevice, owner, personName, threadAuthor, threadRow, whoName, type DeviceKind } from "../model";

export type AnyMsg = (T.DMMessage | T.Message) & { _local?: boolean; _failed?: boolean; _retry?: () => void };

/** Everything a message needs to know about the conversation it is in. */
export interface Ctx {
  conv: string;                      // the open conversation's id (also its draft key)
  dm: T.DMThread | null;
  thread: T.Thread | null;
  overview: T.Overview | null;
  names: Record<string, string>;     // agent catalog names (useAgentNames)
  canReply: boolean;                 // the conversation takes new messages from this person
}

export const isThreadMsg = (m: AnyMsg): m is T.Message => "author" in m;
export const isRequest = (m: AnyMsg) => m.kind === "question" || m.kind === "task";
export const isReply = (m: AnyMsg) => m.kind === "answer" || m.kind === "result";
export const shownText = (m: AnyMsg) => (m.deleted ? "" : m.edited && typeof m.text === "string" ? m.text : m.body || "");
export const ev = (m: AnyMsg) => (isThreadMsg(m) ? "" : m.event || "");
export const excerpt = (m: AnyMsg) => !isThreadMsg(m) && !!m.excerpt_pid;

const isMe = (p: T.PersonView | null | undefined, o: T.Overview | null) => !!p && !!o?.person?.person && p.person === o.person.person;

// ---- agents -------------------------------------------------------------------

/** agentLabel: what an agent in this conversation is called (model.agentName):
 *  its own name, else "Your agent" / "Vitalii’s agent". */
export const agentLabel = (a: T.AgentView, ctx: Pick<Ctx, "names" | "overview">) =>
  agentName(a.agent_id, ctx.names, a.host, ctx.overview?.person);

/** hostOf: the person (you or someone in the directory) one of whose devices is this address. */
export function hostOf(address: string, o: T.Overview | null): T.PersonView | null {
  const holds = (p?: T.PersonView | null) => !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));
  if (o?.person && holds(o.person)) return o.person;
  return (o?.people || []).find(holds) || null;
}

/** lower: "Your agent" reads "your agent" inside a sentence; names stay as they are. */
export const lower = (s: string) => (/^(Your|Their) /.test(s) ? s.charAt(0).toLowerCase() + s.slice(1) : s);

/** roomTitle: a DM by the person in it; one you are a guest or visitor in, by its two people. */
export function roomTitle(t: T.DMThread) {
  const two = (t.members || []).map((p) => personName(p));
  return (t.role === "human_guest" || t.role === "visitor") && two.length ? two.join(" & ") : personName(t.peer);
}

export const agentOf = (ctx: Pick<Ctx, "dm">, pid?: string) => (pid ? (ctx.dm?.agents || []).find((a) => a.pid === pid) : undefined);
export const guestOf = (ctx: Pick<Ctx, "dm">, pid?: string) => (pid ? (ctx.dm?.guests || []).find((g) => g.pid === pid) : undefined);

/** The device thread's other end, as the chat list names it (model.threadRow):
 *  its agent where one runs ("Bohdan’s agent"; its device is said apart,
 *  "on Desk"), this computer's agent for your own device that runs none, or
 *  the person whose device it is ("Vitalii"). */
export function threadAgentName(ctx: Pick<Ctx, "thread" | "names" | "overview">) {
  return threadRow(ctx.thread?.peer || "", ctx.overview, ctx.names).title;
}

// ---- authors ------------------------------------------------------------------

export interface Who {
  key: string;            // identity for grouping consecutive messages
  name: string;
  sub?: string;           // "Your agent · Laptop", "on your Phone"
  agent: boolean;         // drawn as an agent (caption box, robot face)
  mine: boolean;          // this person wrote it (right side, butter bubble)
  guest: boolean;
  seed: string;
  device?: DeviceKind;    // an agent's device badge, when its name says what it is
}

export function whoWrote(m: AnyMsg, ctx: Ctx): Who {
  const o = ctx.overview, me = o?.person;
  if (isThreadMsg(m)) {
    // An agent only where one runs: what is typed on your phone is yours (model.threadAuthor).
    const a = threadAuthor(m, o, ctx.names, ctx.thread?.peer || m.from, ctx.thread?.messages || []);
    return { ...a, guest: false, device: a.agent ? deviceKind(m.dir === "out" ? o?.me.address : m.from) : undefined };
  }
  const t = ctx.dm;
  // An agent's turn only when the server proved it came from that agent's
  // exact host (verified_agent); a claimed origin or agent id proves nothing.
  const agentMsg = m.verified_agent;
  if (agentMsg) {
    const a = agentOf(ctx, m.agent_author_pid || m.pid);
    if (a) {
      // A named agent says whose it is; "Your agent" already does, so it says where.
      const named = !!(a.agent_id && ctx.names[a.agent_id]);
      const sub = a.member ? (named ? owner(a.host, me) + " agent" : undefined) : named ? agentWhere(a.host, a.host.address, me) : "on " + niceDevice(a.host.address);
      return { key: "a:" + a.pid, name: agentLabel(a, ctx), sub, agent: true, mine: false, guest: !a.member, seed: a.agent_id || a.host.address, device: a.member ? undefined : deviceKind(a.host.address) };
    }
    const host = hostOf(m.from, o);
    return {
      key: "a:" + m.from, name: agentName(m.agent_id, ctx.names, host, me), sub: "on " + niceDevice(m.from),
      agent: true, mine: false, guest: false, seed: m.agent_id || m.from, device: deviceKind(m.from),
    };
  }
  if (m.dir === "out" && !m.excerpt_pid) {
    return { key: "me", name: "You", sub: m.via ? "on your " + niceDevice(m.via) : undefined, agent: false, mine: true, guest: false, seed: me?.person || me?.address || "me" };
  }
  const guest = (t?.guests || []).find((g) => g.host.address === m.from || (g.host.devices || []).some((d) => d.address === m.from));
  if (guest) return { key: "g:" + guest.pid, name: personName(guest.host), agent: false, mine: false, guest: true, seed: guest.host.person || guest.host.address };
  const p = [t?.peer, ...(t?.members || []), me, ...(o?.people || [])]
    .find((x) => x && (x.address === m.from || (x.devices || []).some((d) => d.address === m.from)));
  if (p) return { key: "p:" + (p.person || p.address), name: isMe(p, o) ? "You" : personName(p), agent: false, mine: false, guest: false, seed: p.person || p.address };
  return { key: "p:" + m.from, name: niceDevice(m.from) || "Someone", agent: false, mine: false, guest: false, seed: m.from };
}

/** deviceWords names a device for a sentence: "your Phone", "Vitalii’s Desk"
 *  (a look-alike name with its key: "Sergey · 19c77bce’s Desk"). */
export function deviceWords(address: string, o: T.Overview | null) {
  const w = deviceWho(address, o);
  if (w.relation === "own" || w.relation === "this") return "your " + w.device;
  return w.relation === "person" ? whoName(w) + "’s " + w.device : w.device;
}

// ---- requests to agents --------------------------------------------------------

/** requestLabel: "Asked Zen" / "Task for Zen" under a question or task sent to an agent. */
export function requestLabel(m: AnyMsg, ctx: Ctx): string {
  if (!isRequest(m)) return "";
  let to = "";
  if (isThreadMsg(m)) {
    const t = m.target;
    to = (t?.agent_id && ctx.names[t.agent_id]) || (m.dir === "out" ? threadAgentName(ctx) : "your agent");
  } else {
    const a = agentOf(ctx, m.pid);
    if (!a) return "";
    to = lower(agentLabel(a, ctx));
  }
  return (m.kind === "task" ? "Task for " : "Asked ") + to;
}

const localJob: Record<string, string> = {
  queued: "Queued", stopped: "Stopped", not_run: "Not run", resolved: "Closed", cancel_requested: "Stopping…",
  part_waiting: "Not started yet", not_delivered: "Reply kept here", manual: "Answered by hand", conv_held: "Held for you",
};
const word = (s?: string) => jobWord(s) || (s ? localJob[s] || "" : "");

/** answered: the reply that closes this request, if one is in the conversation. */
function answerTo(m: AnyMsg, all: AnyMsg[]) {
  return all.find((x) => isReply(x) && !!x.reply_to && (x.reply_to === m.id || (!isThreadMsg(m) && x.reply_to === m.lid)));
}

/** requestState: how far a question or task got, only as its executor or the
 *  conversation proves it: an answer here, the executor's word, the local job. */
export function requestState(m: AnyMsg, all: AnyMsg[]): { text: string; tone: "ok" | "work" | "wait" | "bad" | "muted" } {
  const answer = answerTo(m, all);
  // A proposal (MEL-521) answers with a task the agent may not run itself:
  // nothing was done. Confirmation comes from the host's actions[] list.
  if (answer && "status" in answer && answer.status === "proposal") return { text: "Suggested a task · not run", tone: "wait" };
  if (answer) return { text: m.kind === "task" ? "Done" : "Answered", tone: "ok" };
  const e = m.exec;
  if (e && e.state) {
    const w = word(e.state);
    if (w) return { text: w + (e.stale ? " · last known" : ""), tone: tone(e.state) };
  }
  if (m.dir === "in" && (isThreadMsg(m) || m.pid)) {
    const w = word(m.state);
    if (w) return { text: w, tone: tone(m.state || "") };
  }
  if ((m.actions || []).includes("cancel")) return { text: "Working…", tone: "work" };
  if (m.delivery_uncertain) return { text: "Delivery unconfirmed", tone: "wait" };
  if (m.send_stopped && ["failed","not_delivered"].includes(m.state || "")) return {text:"Not sent",tone:"muted"};
  const d = deliveryWord(("delivery" in m ? m.delivery : undefined) ?? m.state ?? "");
  return { text: d, tone: ["failed", "expired", "quarantined"].includes(m.state || "") ? "bad" : "muted" };
}

function tone(state: string): "ok" | "work" | "wait" | "bad" | "muted" {
  if (state === "running") return "work";
  if (["answered", "done", "manual", "resolved"].includes(state)) return "ok";
  if (["awaiting", "held", "needs_human", "part_waiting", "conv_held"].includes(state)) return "wait";
  if (["failed", "declined", "interrupted", "expired", "not_run", "not_delivered"].includes(state)) return "bad";
  return "muted";
}

/** working: a request its executor says is running now (and not answered yet). */
export function working(m: AnyMsg, all: AnyMsg[]) {
  if (!isRequest(m) || answerTo(m, all)) return false;
  if (m.exec?.state === "running") return !m.exec.stale;
  return m.state === "running" || (m.actions || []).includes("cancel");
}

// ---- delivery ----------------------------------------------------------------

export const problem = (state?: string) => ["failed", "expired", "quarantined", "waiting"].includes(state || "");

const copyWord: Record<string, string> = {
  delivered: "Delivered", custody: "On the server, until it connects", queued: "Waiting to send from here",
  waiting: "Kept here, not sent yet", failed: "Not sent", expired: "Not delivered", quarantined: "Couldn’t be verified there",
};
export const copyText = (state: string) => copyWord[state] || deliveryWord(state) || state;

// ---- participation dividers ------------------------------------------------------

type EventKind = "invited" | "joined" | "declined" | "left";

/** eventKind reads which participation record a divider stands for (the
 *  server's sentence for it, liveagent.go eventText/humanEventText). Ending
 *  someone's part reads one way for people and agents: they left, dismissed
 *  by whoever ended it ("by" is empty when they left on their own). */
export function eventKind(text: string): { kind: EventKind; by: string } | null {
  const by = (re: RegExp) => (re.exec(text)?.[1] || "").trim();
  if (/ invited .+ into this (DM|group)\.$/.test(text)) return { kind: "invited", by: by(/^(.+?) invited /) };
  if (/accepted: the agent (joins|participates)|accepted participation| joined this DM\.$/.test(text)) return { kind: "joined", by: by(/^(.+?) accepted/) };
  if (/declined the invitation/.test(text)) return { kind: "declined", by: by(/^(.+?) declined/) };
  if (/ dismissed the agent/.test(text)) return { kind: "left", by: by(/^(.+?) dismissed /) };
  if (/ removed .+ from this DM\.$/.test(text)) return { kind: "left", by: by(/^(.+?) removed /) };
  if (/ left this DM\.$/.test(text)) return { kind: "left", by: "" };
  return null;
}

/** joinedBefore: whether a participation's records here show it joined before
 *  the record at index i (an invitation ended before that was cancelled, not left). */
export function joinedBefore(all: AnyMsg[], pid: string, i: number): boolean | null {
  let invited = false;
  for (let j = 0; j < i; j++) {
    const m = all[j];
    if (isThreadMsg(m) || m.pid !== pid || !m.event) continue;
    const k = eventKind(m.event)?.kind;
    if (k === "joined") return true;
    if (k === "invited") invited = true;
  }
  return invited ? false : null; // null: its invitation isn't on record here
}
