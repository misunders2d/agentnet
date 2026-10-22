import type { T } from "../api";
import { scopeMatches, threadAgentHost, threadAgentKey, threadAgentID } from "../model";
import { isRequest, isThreadMsg, whoWrote, type AnyMsg, type Ctx } from "./Message.model";

function followupRef(m: AnyMsg, ctx: Ctx) {
  if (m._local || m.deleted || m.dir !== "out" || !isRequest(m) || !isThreadMsg(m) && m.origin?.startsWith("agent:") || whoWrote(m, ctx).agent) return null;
  if ("history" in m && m.history && (!isThreadMsg(m) || !m.from_key) || !isThreadMsg(m) && m.excerpt_pid) return null;
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

export class ComposerRecovery extends Error {
  constructor(request:string,host:string,attempt:number|undefined){
    super("Request "+request+" on "+host+(attempt?" (attempt "+attempt+")":"")+" still reports running, but its current attempt or identity cannot be verified. Refresh to check that work. Nothing was sent; your text and files are kept.");
    this.name="ComposerRecovery";
  }
}

// A display name or the last message never chooses work. The exact current
// PID, host key, selected topic and original human request must all match.
export function composerFollowup(m: T.DMMessage, pid: string, topic: string, ctx: Ctx, ordinaryReply = false): ComposerFollowup | null {
  const agent = ctx.dm?.agents?.find(a => a.pid === pid), ref = followupRef(m, ctx), running = m.exec?.state === "running";
  const attempt = running ? m.exec?.attempt : ordinaryReply && ["answered", "needs_human", "resolved", "done"].includes(m.exec?.state || "") ? 0 : undefined;
  if (!ref || !agent?.can_ask || !scopeMatches(agent, topic) || typeof attempt !== "number" || !Number.isSafeInteger(attempt) || attempt < (running ? 1 : 0) ||
      m.exec?.stale ||
      m.exec?.host !== agent.host.address || m.pid !== pid || (m.topic || "") !== topic ||
      m.target?.address !== agent.host.address || m.target?.fingerprint !== agent.host.fingerprint ||
      (m.target?.agent_id || "") !== (agent.agent_id || "")) return null;
  return { ref, kind: m.kind, attempt, binding: JSON.stringify([pid,topic,agent.host.address,agent.host.fingerprint,agent.agent_id || "",attempt]) };
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
export function composerReplyFollowup(dm: T.DMThread | undefined, pid: string, topic: string, ctx: Ctx, replyTo?: string, strict = false): ComposerFollowup | undefined {
  const messages = dm?.messages || [], seen = new Set<string>();
  let next = replyTo;
  while (next && !seen.has(next)) {
    seen.add(next);
    const exact = messages.filter(m => m.id === next);
    const rows = exact.length ? exact : messages.filter(m => m.lid === next);
    if (!rows.length || rows.some(m => m.deleted || ("_local" in m && m._local) || m.excerpt_pid || ("history" in m && m.history) || (m.topic || "") !== topic)) {
      if (strict) throw Error("The selected reply or its topic is unavailable. Nothing was sent; your draft is kept.");
      return;
    }
    if (rows.some(m => isRequest(m))) {
      const current = rows.map(m => composerFollowup(m,pid,topic,ctx,true));
      if (current.every((m): m is ComposerFollowup => !!m) && current.every(m => JSON.stringify(m) === JSON.stringify(current[0]))) return current[0];
      if (strict) throw Error("The selected request or its exact agent cannot be continued. Nothing was sent; your draft is kept.");
      return;
    }
    const parents = new Set(rows.map(m => m.reply_to || ""));
    if (parents.size !== 1) {
      if (strict) throw Error("The selected reply has ambiguous ancestry. Nothing was sent; your draft is kept.");
      return;
    }
    next = rows[0].reply_to;
  }
  if (next && strict) throw Error("The selected reply has cyclic ancestry. Nothing was sent; your draft is kept.");
}

export function currentComposerFollowup(dm: T.DMThread | undefined, pid: string, topic: string, ctx: Ctx, replyTo?: string): ComposerFollowup | undefined {
  if (replyTo) {
    const bound = composerReplyFollowup(dm,pid,topic,ctx,replyTo,true);
    if (bound) return bound;
  }
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

// Direct threads already contain only this selected topic. Their immutable
// request IDs replace conversation/PID bindings; the current pin still owns
// the destination, including the default agent's absent Target.
export function currentDirectComposerFollowup(thread: T.Thread | undefined, ctx: Ctx, replyTo?: string): ComposerFollowup | undefined {
  if (!thread) return;
  const selected = threadAgentID(thread) || "", host=threadAgentHost(thread),key=threadAgentKey(thread), rows = new Map<string, ComposerFollowup>();
  const candidate = (message: T.Message, ordinaryReply = false): ComposerFollowup | undefined => {
    const running = message.exec?.state === "running";
    if ((!running && (!ordinaryReply || !["answered", "needs_human", "resolved", "done"].includes(message.exec?.state || ""))) || message.dir !== "out" || !isRequest(message)) return;
    // Another exact executor's work is outside this selected destination.
    if((message.target?.agent_id||"")!==selected)return;
    const ref = followupRef({...message, deleted:false}, ctx), attempt = running ? message.exec!.attempt : 0;
    if (!ref) return;
    if (message.deleted || key.pending || !key.pinned || message.to !== host ||
        message.exec!.host !== host || message.exec!.stale || !Number.isSafeInteger(attempt) || attempt == null || attempt < (running ? 1 : 0) ||
        (message.target?.agent_id || "") !== selected ||
        message.target && (message.target.address !== host || message.target.fingerprint !== key.pinned)) {
      if (running) throw new ComposerRecovery(ref.id,host,attempt);
      throw Error("The selected request or its exact agent cannot be continued. Nothing was sent; your draft is kept.");
    }
    const bound = {ref, kind:message.kind, attempt, binding:JSON.stringify([thread.id,host,key.pinned,selected,attempt])};
    return bound;
  };
  if (replyTo) {
    const seen = new Set<string>();
    for (let next: string | undefined = replyTo; next;) {
      if (seen.has(next)) throw Error("The selected reply has cyclic ancestry. Nothing was sent; your draft is kept.");
      seen.add(next);
      const exact = (thread.messages || []).filter(m => m.id === next);
      if (!exact.length || exact.some(m => m.deleted || ("_local" in m && m._local) || m.history && !m.from_key)) throw Error("The selected reply is unavailable. Nothing was sent; your draft is kept.");
      if (exact.some(m => isRequest(m))) {
        const current = exact.map(m => candidate(m,true));
        if (current.every((m): m is ComposerFollowup => !!m) && current.every(m => JSON.stringify(m) === JSON.stringify(current[0]))) return current[0];
        throw Error("The selected request or its exact agent cannot be continued. Nothing was sent; your draft is kept.");
      }
      const parents = new Set(exact.map(m => m.reply_to || ""));
      if (parents.size !== 1) throw Error("The selected reply has ambiguous ancestry. Nothing was sent; your draft is kept.");
      next = exact[0].reply_to;
    }
  }
  for (const message of thread.messages || []) {
    const bound = candidate(message);
    if (!bound) continue;
    const {ref} = bound;
    if (rows.has(ref.id) && JSON.stringify(rows.get(ref.id)) !== JSON.stringify(bound))
      throw new ComposerRecovery(ref.id,host,message.exec?.attempt);
    rows.set(ref.id,bound);
  }
  if (rows.size < 2) return rows.values().next().value;
  const seen = new Set<string>();
  for (let next = replyTo; next && !seen.has(next);) {
    seen.add(next);
    if (rows.has(next)) return rows.get(next);
    next = thread.messages?.find(m => m.id === next)?.reply_to;
  }
  throw Error("This agent has multiple running requests in this topic, and this message has no exact reply binding. Nothing was sent; your draft is kept.");
}
