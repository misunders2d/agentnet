import { useEffect, useState } from "react";
import { useApp } from "../context";
import { errorText } from "../api";
import { isSendPersisted } from "../send-recovery.mjs";
import { legacyDraft } from "../legacy-drafts.mjs";
import { readPending, acknowledge, recordFailure, type JournalEntry } from "../send-journal.mjs";
import { Sheet } from "../ui/Sheet";
import { Button } from "../ui/Button";
import { copyText } from "./Settings.parts";

/** Appears only for actual saved work. Nothing retries merely by opening it. */
export function NativeRecovery() {
  const store = useApp(), native = store.host.android, workspace = store.host.workspace.id;
  const [legacy, setLegacy] = useState<{key:string;value:string}[]>([]);
  const [recovered] = useState(() => {
    try { return new Set(native ? readPending(localStorage, workspace).map(e=>e.id) : []); } catch { return new Set<string>(); }
  });
  const [pending, setPending] = useState<JournalEntry[]>([]), [problem, setProblem] = useState("");
  const [open, setOpen] = useState(false), [busy, setBusy] = useState("");
  const [errors, setErrors] = useState<Record<string,string>>({});
  useEffect(() => {
    if (!native) return;
    let alive=true;
    if (workspace === "default") native.legacyDrafts().then(v=>{if(alive)setLegacy(v.drafts);},()=>{if(alive)setProblem("Could not read saved preview drafts. They remain on this device.");});
    const refresh=()=>{try {setPending(readPending(localStorage,workspace).filter(e=>recovered.has(e.id)||e.error));}catch{setProblem("Saved sends could not be read. They remain on this device.");}};
    refresh(); window.addEventListener("agentnet-send-journal",refresh);
    return()=>{alive=false;window.removeEventListener("agentnet-send-journal",refresh);};
  },[native,workspace,recovered]);
  if (!native || (!legacy.length && !pending.length && !problem)) return null;
  const run = async (key:string, work:()=>Promise<void>) => {
    setBusy(key);setErrors(v=>({...v,[key]:""}));
    try {await work();}catch(e){setErrors(v=>({...v,[key]:errorText(e)}));}finally{setBusy("");}
  };
  const submit = async (entry: Pick<JournalEntry,"id"|"conversation"|"endpoint"|"request">) => {
    if (await isSendPersisted(store.host,entry)) return;
    try { await store.host.api(entry.endpoint,entry.request); }
    catch (e) { if (!(await isSendPersisted(store.host,entry))) throw e; }
  };
  const restore = async (record:{key:string;value:string}) => {
    const d=legacyDraft(record);
    if(d.request && d.endpoint) {
      const entry={id:String(d.request.id),conversation:d.conversation,endpoint:d.endpoint as JournalEntry["endpoint"],request:d.request};
      await submit(entry);
      store.toast("Saved send is in the outbox.","ok");
    } else {
      const current=store.draft(d.conversation);
      if(current.text.trim() || current.files?.length) throw new Error("There is already a draft in this chat. Keep or send it first, then restore this one.");
      const key="agentnet.messenger.drafts."+workspace;
      const saved=JSON.parse(localStorage.getItem(key)||"{}");
      saved[d.conversation]={...current,text:d.body};
      // Durable write must succeed before the old preference can be acknowledged.
      localStorage.setItem(key,JSON.stringify(saved));
      store.setDraft(d.conversation,{...current,text:d.body});
      await store.open({kind:d.kind,id:d.conversation});
    }
    const ack=await native.acknowledgeLegacyDraft(record.key,record.value);
    if(!ack.removed) throw new Error("The saved draft changed while restoring it. Its latest version was kept.");
    setLegacy(v=>v.filter(x=>x.key!==record.key));
  };
  return <>
    <button className="w-full shrink-0 border-b border-hairline bg-surface px-4 py-2 text-left text-[13px] font-semibold" onClick={()=>setOpen(true)}>
      {problem ? "Saved messages need attention" : `${legacy.length+pending.length} saved ${legacy.length+pending.length===1?"message":"messages"} to recover`} · Review
    </button>
    <Sheet open={open} onOpenChange={setOpen} title="Saved messages" description="Drafts and sends kept on this phone before the app closed. Review them here; nothing sends automatically.">
      {problem && <p role="alert" className="mb-3 text-danger">{problem}</p>}
      {legacy.map(record=>{
        let d;try{d=legacyDraft(record);}catch{}
        return <section key={record.key} className="mb-3 rounded-2xl border border-hairline bg-surface p-4">
          <p className="font-semibold">{d?.request?"Unconfirmed send from the earlier preview":"Draft from the earlier preview"}</p>
          <p className="my-2 whitespace-pre-wrap break-words">{d?.body || "This saved record could not be read. The original remains on this phone."}</p>
          {d?.request && <p className="mb-2 text-[13px] text-muted">Retry uses the same message ID and recipient. A message already in the outbox is not sent again.</p>}
          {d && <Button size="sm" variant="outline" disabled={!!busy} onClick={()=>void run(record.key,()=>restore(record))}>{busy===record.key?"Restoring…":d.request?"Retry saved send":"Restore draft"}</Button>}
          {errors[record.key] && <p role="alert" className="mt-2 text-danger">{errors[record.key]}</p>}
        </section>;
      })}
      {pending.map(entry=><section key={entry.id} className="mb-3 rounded-2xl border border-hairline bg-surface p-4">
        <p className="font-semibold">Send not yet confirmed</p>
        <p className="my-2 whitespace-pre-wrap break-words">{String(entry.request.body || "Message with attachments")}</p>
        <p className="mb-2 text-[13px] text-muted">Retry keeps the same recipient and message ID. It does not repeat work already accepted.</p>
        <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={!!busy} onClick={()=>void run(entry.id,async()=>{
          try {await submit(entry);acknowledge(localStorage,workspace,entry.id);store.toast("Saved send is in the outbox.","ok");}
          catch(e){recordFailure(localStorage,workspace,entry.id,errorText(e));throw e;}
        })}>{busy===entry.id?"Retrying…":"Retry saved send"}</Button>
        <Button size="sm" variant="ghost" onClick={()=>void copyText(String(entry.request.body||"")).then(ok=>store.toast(ok?"Copied":"Couldn’t copy here",ok?"ok":"error"))}>Copy text</Button></div>
        {(errors[entry.id]||entry.error) && <p role="alert" className="mt-2 text-danger">{errors[entry.id]||entry.error}</p>}
      </section>)}
      {!legacy.length&&!pending.length&&!problem&&<p>All saved messages have been recovered.</p>}
    </Sheet>
  </>;
}
