import type { T } from "../api";
import { scopeMatches } from "../model";
import { isRequest, isThreadMsg, whoWrote, type AnyMsg, type Ctx } from "./Message.model";

function followupRef(m: AnyMsg, ctx: Ctx) {
  if (m._local || m.deleted || m.dir !== "out" || !isRequest(m) || !isThreadMsg(m) && m.origin?.startsWith("agent:") || whoWrote(m, ctx).agent) return null;
  const fingerprint = isThreadMsg(m) ? m.from_key : m.send_group_author || m.claimed_key;
  const key = fingerprint || (m.from === ctx.overview?.me.address ? ctx.overview.me.fingerprint : "");
  const id = isThreadMsg(m) ? m.id : m.lid || m.id;
  return /^[a-f0-9]{32}$/.test(id) && /^[a-f0-9]{8}(?:-[a-f0-9]{8}){3}$/.test(key || "") ? { id, fingerprint: key! } : null;
}

export interface ComposerFollowup {
  ref: { id: string; fingerprint: string };
  kind: string;
  attempt: number;
  binding: string;
}

// A display name or the last message never chooses work. The exact current
// PID, host key, selected topic and original human request must all match.
export function composerFollowup(m: T.DMMessage, pid: string, topic: string, ctx: Ctx): ComposerFollowup | null {
  const agent = ctx.dm?.agents?.find(a => a.pid === pid), ref = followupRef(m, ctx), attempt = m.exec?.attempt;
  if (!ref || !agent?.can_ask || !scopeMatches(agent, topic) || typeof attempt !== "number" || !Number.isSafeInteger(attempt) || attempt < 1 ||
      m.exec?.state !== "running" || m.exec.stale ||
      m.exec.host !== agent.host.address || m.pid !== pid || (m.topic || "") !== topic ||
      m.target?.address !== agent.host.address || m.target?.fingerprint !== agent.host.fingerprint ||
      (m.target?.agent_id || "") !== (agent.agent_id || "")) return null;
  return { ref, kind: m.kind, attempt, binding: JSON.stringify([pid,topic,agent.host.address,agent.host.fingerprint,agent.agent_id || "",m.exec.attempt]) };
}

// Queued or pending work never creates ambiguity for explicit live steering.
export function activeComposerFollowups(dm: T.DMThread | undefined, pids: string[], topic: string, ctx: Ctx) {
  const result = new Map<string, T.DMMessage[]>(pids.map(pid => [pid, []]));
  const seen = new Set<string>();
  for (const message of dm?.messages || []) {
    const rows = result.get(message.pid || "");
    const current = rows && composerFollowup(message, message.pid!, topic, ctx);
    if (!current) continue;
    const key = message.pid + JSON.stringify(current.ref);
    if (!seen.has(key)) { rows!.push(message); seen.add(key); }
  }
  return result;
}

// Resolve only stored reply ancestry within this chat/topic. A quote's text or
// an agent's display name never chooses between separate running requests.
export function composerReplyFollowup(dm: T.DMThread | undefined, pid: string, topic: string, ctx: Ctx, replyTo?: string): ComposerFollowup | undefined {
  const messages = dm?.messages || [], seen = new Set<string>();
  let next = replyTo;
  while (next && !seen.has(next)) {
    seen.add(next);
    const exact = messages.filter(m => m.id === next);
    const rows = exact.length ? exact : messages.filter(m => m.lid === next);
    if (!rows.length || rows.some(m => (m.topic || "") !== topic)) return;
    const current = rows.map(m => composerFollowup(m,pid,topic,ctx));
    if (current.every((m): m is ComposerFollowup => !!m) && current.every(m => JSON.stringify(m) === JSON.stringify(current[0]))) return current[0];
    const parents = new Set(rows.map(m => m.reply_to || ""));
    if (parents.size !== 1) return;
    next = rows[0].reply_to;
  }
}

export function currentComposerFollowup(dm: T.DMThread | undefined, pid: string, topic: string, ctx: Ctx, replyTo?: string): ComposerFollowup | undefined {
  const reported = (dm?.messages || []).filter(m => m.pid === pid && (m.topic || "") === topic && m.exec?.state === "running" && followupRef({...m,deleted:false},ctx));
  const attempts = new Map<string,number>();
  for (const message of reported) {
    const current = composerFollowup(message,pid,topic,ctx), key = current && JSON.stringify(current.ref);
    if (!current || attempts.has(key!) && attempts.get(key!) !== current.attempt) throw Error("Running work is reported, but its current attempt or identity cannot be verified. Nothing was sent; your draft is kept.");
    attempts.set(key!,current.attempt);
  }
  const rows = activeComposerFollowups(dm,[pid],topic,ctx).get(pid) || [];
  if (!rows.length) return;
  if (rows.length === 1) return composerFollowup(rows[0],pid,topic,ctx)!;
  const bound = composerReplyFollowup(dm,pid,topic,ctx,replyTo);
  if (bound) return bound;
  throw Error("This agent has " + rows.length + " distinct running requests in this chat/topic, and this message has no exact reply binding. Nothing was sent; your draft is kept.");
}
