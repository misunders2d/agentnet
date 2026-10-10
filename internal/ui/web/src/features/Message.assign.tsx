// Explicit delegation of a reviewed message to an exact own agent. The quoted
// source stays attributed; the selecting person sends a new ordinary task.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type Api, type T } from "../api";
import { useApp } from "../context";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { checkAgentChatTarget, ownAgentChatTargets, type AgentChatTarget } from "./AgentChat";
import { Markdown } from "./Markdown";
import { overLimit } from "./Composer.files";
import { isThreadMsg, shownText, whoWrote, type AnyMsg, type Ctx } from "./Message.model";

export function assignmentFiles(m: AnyMsg) {
  return (isThreadMsg(m) ? m.files || [] : m.attachments || []).map((f, i) => ({ ...f, index: f.index ?? i }));
}

export function assignmentSnapshot(m: AnyMsg) {
  return JSON.stringify([m.id, m.from, m.kind, shownText(m), m.revision, !!m.deleted,
    assignmentFiles(m).map(f => [f.index, f.name, f.size])]);
}

export function assignmentBody(m: AnyMsg, ctx: Ctx, instruction: string) {
  const author = whoWrote(m, ctx).name.replace(/[\r\n]/g, " ");
  const link = "agentnet:message/" + (isThreadMsg(m) ? m.id : m.lid || m.id) + (ctx.dm ? "?conv=" + ctx.dm.id : "");
  return instruction.trim() + "\n\nSelected message from " + author + " · [View original](" + link + ")\n\n" +
    shownText(m).split("\n").map(line => "> " + line).join("\n");
}

// The public file view has no content hash. Recover only an exact kept
// request, including decrypted bytes, rather than mistaking metadata for proof.
async function keptAssignment(api: Api, draft: T.Draft, owner: string, hostKey: string, selected: File[], current: () => boolean): Promise<T.Sent | null> {
  let thread: T.Thread;
  try { thread = await api.thread(draft.id!); } catch { return null; }
  if (!current()) return null;
  const matches = thread?.messages?.filter(m => m.id === draft.id && m.dir === "out") || [];
  if (!matches.length) return null;
  const m = matches[0], target = m.target;
  if (matches.length !== 1 || m.dir !== "out" || m.from !== owner || m.to !== draft.to || m.kind !== draft.kind || m.body !== draft.body ||
      m.deleted || m.edited || m.reply_to || m.quote || (target?.agent_id || "") !== (draft.agent_id || "") ||
      (target && (target.address !== draft.to || target.fingerprint !== hostKey)) || (!target && thread.key?.pinned !== hostKey))
    throw Error("The retained task does not match this preview. Nothing else was sent.");
  const files = m.files || [];
  if (files.length !== selected.length) throw Error("The retained task’s files do not match this preview. Nothing else was sent.");
  for (let i = 0; i < files.length; i++) {
    const f = files[i], original = selected[i];
    if (!f.openable || f.name !== original.name || f.size !== original.size)
      throw Error("The retained task’s selected files cannot be verified here. Nothing else was sent.");
    let bytes: Uint8Array;
    try { ({ bytes } = await api.file(m.id, f.index ?? i, "out")); }
    catch (error) { throw Error("The retained task’s selected files could not be verified here. Nothing else was sent. " + errorText(error)); }
    if (!current()) return null;
    const expected = new Uint8Array(await original.arrayBuffer());
    if (bytes.length !== expected.length || bytes.some((value, index) => value !== expected[index]))
      throw Error("The retained task’s selected file bytes differ. Nothing else was sent.");
  }
  return {id:m.id,state:m.state || "queued"};
}

export function AssignMessage({ m, ctx, open, onOpenChange }: {m: AnyMsg; ctx: Ctx; open: boolean; onOpenChange: (open: boolean) => void}) {
  const [sending, setSending] = useState(false);
  return <Sheet open={open} onOpenChange={value => { if (!sending) onOpenChange(value); }} title="Assign to your agent">
    {open && <AssignmentForm m={m} ctx={ctx} onSending={setSending} onDone={() => onOpenChange(false)} />}
  </Sheet>;
}

function AssignmentForm({ m, ctx, onDone, onSending }: {m: AnyMsg; ctx: Ctx; onDone: () => void; onSending: (busy: boolean) => void}) {
  const store = useApp(), [targets, setTargets] = useState<AgentChatTarget[] | null>(null), [problems, setProblems] = useState<string[]>([]);
  const [targetKey, setTargetKey] = useState(""), [instruction, setInstruction] = useState("Handle the request in this message.");
  const files = assignmentFiles(m), available = files.filter(f => f.openable);
  const [chosen, setChosen] = useState(() => new Set(available.map(f => f.index)));
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const locked = useRef(false), sending = useRef(false), id = useRef(sendID()), prepared = useRef<T.Draft | null>(null);
  const retained = useRef(new Map<number, File>()), alive = useRef(true), attempted = useRef(false), hostKey = useRef(""), sourceText = useRef("");
  const originalOpen = useRef(store.get().open);
  const current = () => alive.current && store.isActive() && store.get().open === originalOpen.current && !store.get().pending;
  const problem = overLimit(files.filter(f => chosen.has(f.index)), ctx.overview?.files);
  const targetID = (t: AgentChatTarget) => t.host + "#" + (t.agentId || "");
  useEffect(() => {
    alive.current = true;
    const overview = ctx.overview;
    if (overview) ownAgentChatTargets(store.api, overview, store.host.platform === "daemon").then(result => {
      if (alive.current) { setTargets(result.targets); setProblems(result.problems); }
    });
    return () => { alive.current = false; };
  }, [store]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const target = targets?.find(t => targetID(t) === targetKey);
    if (sending.current || !target || target.unavailable || !instruction.trim() || problem || !current()) return;
    sending.current = true; setBusy(true); onSending(true); setError("");
    const staged: unknown[] = [];
    const complete = async (result: T.Sent) => {
      if (!current()) return;
      store.toast("Task kept for " + target.label, "ok"); onDone();
      void store.refetch();
      await store.open({kind:"thread", id:result.id, peer:target.host});
    };
    try {
      if (prepared.current && attempted.current) {
        const kept = await keptAssignment(store.api, prepared.current, ctx.overview!.me.address, hostKey.current, [...chosen].map(i => retained.current.get(i)!), current);
        if (!current()) return;
        if (kept) { await complete(kept); return; }
      }
      await checkAgentChatTarget(store.api, target, store.host.platform);
      if (!current()) return;
      if (!prepared.current) {
        const overview = await store.api.overview();
        if (!current()) return;
        hostKey.current = target.local ? overview.me.fingerprint : overview.person?.devices?.find(d => d.address === target.host)?.fingerprint || "";
        if (!hostKey.current) throw Error("The selected agent’s device key is unavailable. Nothing sent.");
      }
      if (!prepared.current) {
        const fresh = ctx.dm ? await store.api.dm(ctx.dm.id) : await store.api.thread(ctx.thread!.id);
        if (!current()) return;
        const source = fresh.messages?.find(x => x.id === m.id);
        if (!source || source.deleted || assignmentSnapshot(source) !== assignmentSnapshot(m))
          throw Error("This message changed or is no longer available. Close this preview and select it again.");
        for (const f of files.filter(f => chosen.has(f.index))) {
          if (retained.current.has(f.index)) continue;
          const { bytes } = await store.api.file(m.id, f.index, m.dir);
          if (!current()) return;
          retained.current.set(f.index, new File([bytes as BlobPart], f.name));
        }
        if (!current()) return;
        prepared.current = {id:id.current, to:target.host, agent_id:target.agentId, kind:"task", body:assignmentBody(m, ctx, instruction)};
        sourceText.current = shownText(m);
        locked.current = true;
      }
      // Native staged uploads are consumed by the provider even when its
      // response is lost. Retry the same retained bytes under the same ID.
      for (const i of chosen) {
        staged.push(await store.api.stage(retained.current.get(i)!));
        if (!current()) return;
      }
      attempted.current = true;
      const result = await store.api.send({...prepared.current, files:staged as string[]});
      await complete(result);
    } catch (err) { if (current()) setError(errorText(err)); }
    finally {
      const unused = staged.filter((value): value is string => typeof value === "string");
      if (unused.length) await store.api.discard(unused).catch(() => {});
      sending.current = false; onSending(false); if (alive.current) setBusy(false);
    }
  };
  return <form onSubmit={submit} className="space-y-4">
    <p className="text-[13px] text-muted">You send a new task. Its reply appears in your private chat with the selected agent, using that computer’s usual permissions.</p>
    <details className="rounded-xl bg-surface p-3 stroke" open>
      <summary className="cursor-pointer font-semibold">Message from {whoWrote(m, ctx).name}</summary>
      <div className="mt-2 max-h-48 overflow-auto"><Markdown text={locked.current ? sourceText.current : shownText(m)} /></div>
    </details>
    <fieldset disabled={busy || locked.current} className="space-y-3">
      <label className="block font-semibold">Your agent
        <select required value={targetKey} onChange={e => setTargetKey(e.target.value)} className="mt-1 block min-h-11 w-full rounded-xl bg-surface px-3 stroke">
          <option value="">{targets === null ? "Reading your agents…" : "Choose an agent"}</option>
          {targets?.map(t => <option key={targetID(t)} value={targetID(t)} disabled={!!t.unavailable}>{t.label} · {niceDevice(t.host)}{t.unavailable ? " · " + t.unavailable : ""}</option>)}
        </select>
      </label>
      {problems.map((p, i) => <p key={i} className="text-[13px] text-muted">Agent list unavailable: {p}</p>)}
      <label className="block font-semibold">What should it do?
        <textarea rows={3} required value={instruction} onChange={e => setInstruction(e.target.value)} className="mt-1 block w-full rounded-xl bg-surface p-3 stroke" />
      </label>
      {files.map(f => <label key={f.index} className="flex min-h-11 items-center gap-2 text-[13px]">
        <input type="checkbox" disabled={!f.openable} checked={chosen.has(f.index)} onChange={e => setChosen(s => {const n = new Set(s); if (e.target.checked) n.add(f.index); else n.delete(f.index); return n;})} />
        {f.name}{!f.openable && " · unavailable on this device"}
      </label>)}
    </fieldset>
    {(error || problem) && <p role="alert" className="text-danger">{error || problem}</p>}
    <Button type="submit" variant="act" disabled={busy || !targetKey || !instruction.trim() || !!problem}>{busy ? "Sending task…" : prepared.current ? "Retry this task" : "Send task"}</Button>
  </form>;
}
