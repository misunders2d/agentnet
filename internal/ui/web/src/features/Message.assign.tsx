// Explicit delegation of a reviewed message to an exact own agent. The quoted
// source stays attributed; the selecting person sends a new ordinary task.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useRetainedText } from "../retained-text";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { checkAgentChatTarget, MY_AGENTS, myAgentsRoom, ownAgentChatTargets, ownAgentTopic, type AgentChatTarget } from "./AgentChat";
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

export function AssignMessage({ m, ctx, open, onOpenChange }: {m: AnyMsg; ctx: Ctx; open: boolean; onOpenChange: (open: boolean) => void}) {
  const [sending, setSending] = useState(false);
  const [targetKey, setTargetKey] = useState("");
  const host = targetKey.split("#")[0], o = ctx.overview;
  const key = host === o?.me.address ? o.me.fingerprint : o?.person?.devices?.find(d => d.address === host)?.fingerprint;
  const [instruction, setInstruction, retainedText] = useRetainedText(JSON.stringify(["assign", ctx.conv, m.dir, assignmentSnapshot(m), targetKey, key]), "Handle the request in this message.");
  return <Sheet open={open} onOpenChange={value => { if (!sending) { if (!value) retainedText.discard(); onOpenChange(value); } }} title="Assign to your agent">
    {open && <AssignmentForm m={m} ctx={ctx} targetKey={targetKey} setTargetKey={setTargetKey} instruction={instruction} setInstruction={setInstruction}
      captureText={retainedText.capture} onSending={setSending} onDone={() => onOpenChange(false)} />}
  </Sheet>;
}

function AssignmentForm({ m, ctx, onDone, onSending, targetKey, setTargetKey, instruction, setInstruction, captureText }: {
  m: AnyMsg; ctx: Ctx; onDone: () => void; onSending: (busy: boolean) => void;
  targetKey: string; setTargetKey: (value: string) => void; instruction: string; setInstruction: (value: string) => void; captureText: () => () => void;
}) {
  const store = useApp(), [targets, setTargets] = useState<AgentChatTarget[] | null>(null), [problems, setProblems] = useState<string[]>([]);
  const files = assignmentFiles(m), available = files.filter(f => f.openable);
  const [chosen, setChosen] = useState(() => new Set(available.map(f => f.index)));
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const locked = useRef(false), sending = useRef(false), id = useRef(sendID()), prepared = useRef<T.Draft | null>(null);
  const retained = useRef(new Map<number, File>()), alive = useRef(true), hostKey = useRef(""), sourceText = useRef("");
  const room = useRef(""), topic = useRef(sendID()); // a new topic of the My agents room (MEL-713)
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
    const confirmedText = captureText();
    sending.current = true; setBusy(true); onSending(true); setError("");
    const staged: unknown[] = [];
    const complete = async (result: T.Sent) => {
      confirmedText();
      if (!current()) return;
      store.toast("Task kept for " + target.label, "ok"); onDone();
      void store.refetch();
      await store.openUnread({ kind: "dm", id: room.current }, topic.current);
    };
    try {
      await checkAgentChatTarget(store.api, target, store.host.platform);
      if (!current()) return;
      room.current ||= await myAgentsRoom(store.api);
      if (!current()) return;
      const agent = await ownAgentTopic(store.api, target, room.current, topic.current);
      if (!current()) return;
      if (agent.state !== "active") throw Error(target.label + " is joining your " + MY_AGENTS + " room. Assign again once it has joined; nothing was sent.");
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
      // The same send ID makes a retry the same task, never another.
      const result = await store.api.askAgent({ id: id.current, pid: agent.pid, kind: "task", body: prepared.current.body, topic: topic.current, files: staged as string[] });
      await complete(result);
    } catch (err) { if (current()) setError(errorText(err)); }
    finally {
      const unused = staged.filter((value): value is string => typeof value === "string");
      if (unused.length) await store.api.discard(unused).catch(() => {});
      sending.current = false; onSending(false); if (alive.current) setBusy(false);
    }
  };
  return <form onSubmit={submit} className="space-y-4">
    <p className="text-[13px] text-muted">You send a new task in a new topic of your {MY_AGENTS} room. The selected agent replies there, using that computer’s usual permissions.</p>
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
