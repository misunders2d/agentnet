// A correction is an explicit new request to the same exact target. Owned native runs may accept it; transport success never says live steering worked.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText } from "../api";
import { useApp } from "../context";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { draftFiles, FilesTray, overLimit, releaseFiles } from "./Composer.files";
import type { StagedFile } from "../store";
import { agentLabel, agentOf, isRequest, isThreadMsg, shownText, threadAgentName, whoWrote, type AnyMsg, type Ctx } from "./Message.model";

export function followupRef(m: AnyMsg, ctx: Ctx) {
  if (m._local || m.deleted || m.dir !== "out" || !isRequest(m) || whoWrote(m, ctx).agent) return null;
  const fingerprint = isThreadMsg(m) ? m.from_key : m.send_group_author || m.claimed_key;
  const key = fingerprint || (m.from === ctx.overview?.me.address ? ctx.overview.me.fingerprint : "");
  const id = isThreadMsg(m) ? m.id : m.lid || m.id;
  return /^[a-f0-9]{32}$/.test(id) && /^[a-f0-9]{8}(?:-[a-f0-9]{8}){3}$/.test(key || "") ? { id, fingerprint: key! } : null;
}

export function FollowupMessage({ m, ctx, open, onOpenChange }: {m: AnyMsg; ctx: Ctx; open: boolean; onOpenChange: (open: boolean) => void}) {
  return <Sheet open={open} onOpenChange={onOpenChange} title="Follow up with the same agent">
    {open && <FollowupForm m={m} ctx={ctx} onDone={() => onOpenChange(false)} />}
  </Sheet>;
}

function FollowupForm({ m, ctx, onDone }: {m: AnyMsg; ctx: Ctx; onDone: () => void}) {
  const store = useApp(), [body, setBody] = useState(""), [files, setFiles] = useState<StagedFile[]>([]);
  const [busy, setBusy] = useState(false), [locked, setLocked] = useState(false), [error, setError] = useState("");
  const alive = useRef(true), sending = useRef(false), id = useRef(sendID()), originalOpen = useRef(store.get().open), selected = useRef(files);
  selected.current = files;
  useEffect(() => { alive.current = true; return () => { alive.current = false; releaseFiles(selected.current); }; }, []);
  const current = () => alive.current && store.isActive() && store.get().open === originalOpen.current && !store.get().pending;
  const ref = followupRef(m, ctx), agent = !isThreadMsg(m) ? agentOf(ctx, m.pid) : undefined;
  const target = agent ? agentLabel(agent, ctx) : threadAgentName(ctx), host = m.target?.address || m.to || ctx.thread?.peer || "";
  const problem = overLimit(files, ctx.overview?.files);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (sending.current || !ref || !body.trim() || problem || !current()) return;
    sending.current = true; setBusy(true); setError("");
    const staged: unknown[] = [];
    try {
      const view = ctx.dm ? await store.api.dm(ctx.dm.id) : await store.api.thread(ctx.thread!.id);
      if (!current()) return;
      const source = (view.messages || []).find(row => row.id === m.id);
      if (!source || source.deleted || JSON.stringify(followupRef(source, ctx)) !== JSON.stringify(ref))
        throw Error("This exact request is no longer available. Close this sheet and select it again.");
      setLocked(true);
      // Re-stage the same retained File objects on an uncertain-response retry;
      // the provider compares saved bytes under this same stable send ID.
      for (const f of files) { staged.push(await store.api.stage(f.file)); if (!current()) return; }
      const result = await store.host.api<{note:string}>("/api/request/followup", {id:id.current,conv:ctx.dm?.id || "",ref,body,files:staged});
      if (!current()) return;
      store.toast(result.note, "ok"); onDone(); void store.refetch();
    } catch (e) { if (current()) setError(errorText(e)); }
    finally {
      const ids = staged.filter((value): value is string => typeof value === "string");
      if (ids.length) await store.api.discard(ids).catch(() => {});
      sending.current = false; if (alive.current) setBusy(false);
    }
  };
  return <form onSubmit={submit} className="space-y-4">
    <p className="font-semibold">{target}<span className="block text-[13px] font-normal text-muted">{niceDevice(host)} · {m.kind}</span></p>
    <details className="rounded-xl bg-surface p-3 stroke"><summary className="cursor-pointer font-semibold">Original request from {whoWrote(m, ctx).name}</summary><p className="mt-2 max-h-40 overflow-auto whitespace-pre-wrap text-[13px]">{shownText(m)}</p></details>
    <p className="text-[13px] text-muted">Send a correction or next step to the same agent. It joins the current work when supported; otherwise it waits as a follow-up with the original context. The status shows what the agent accepted. Stopped or uncertain work waits for a decision.</p>
    <fieldset disabled={busy || locked} className="space-y-3">
      <label className="block font-semibold">What should change or happen next?<textarea rows={4} required value={body} onChange={e => setBody(e.target.value)} className="mt-1 block w-full rounded-xl bg-surface p-3 stroke" /></label>
      <label className="block text-[13px]">Attach files<input type="file" multiple className="mt-1 block w-full" onChange={e => { const picked = draftFiles([...e.target.files || []]); setFiles(old => [...old, ...picked]); e.target.value = ""; }} /></label>
      <FilesTray files={files} lim={ctx.overview?.files} busy={busy || locked} onRemove={key => setFiles(old => { releaseFiles(old.filter(f => f.key === key)); return old.filter(f => f.key !== key); })} />
    </fieldset>
    {(error || problem) && <p role="alert" className="text-danger">{error || problem}</p>}
    <Button type="submit" variant="act" disabled={busy || !ref || !body.trim() || !!problem}>{busy ? "Sending…" : locked ? "Retry this follow-up" : "Send follow-up"}</Button>
  </form>;
}
