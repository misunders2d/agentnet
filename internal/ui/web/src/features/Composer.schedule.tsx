import { useEffect, useRef, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useRetainedText } from "../retained-text";
import { useStore, type Draft } from "../store";
import type { ScheduleSave } from "../schedule-saves";
import { dueText } from "../model";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { sendID } from "../optimistic.mjs";
import { captureSchedule, localScheduleTime, scheduleDue, scheduleEdit, scheduleRefused, scheduleSummary, submitSchedule, type ScheduleAttempt } from "./Composer.schedule-model";

export type SchedulePane = "create" | "list" | null;
type Snapshot = {draft: Draft; body: string};
const input = "mt-1 block min-h-11 w-full min-w-0 rounded-xl border border-outline bg-surface p-3 text-[16px] outline-none focus-visible:outline-3 focus-visible:outline-agent-ink";
const states: Record<string,string> = {pending:"Pending",dispatching:"Preparing to send",sent:"Queued to send",cancelled:"Cancelled",needs_review:"Needs review"};
const mutable = (row: T.ScheduledSend) => row.state === "pending" || row.state === "needs_review";
const timezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export function ScheduleDialogs({pane,onPane,conv,dm,capture,reason,page=false,reviewOnly=false}: {
  pane: SchedulePane; onPane:(pane:SchedulePane)=>void; conv:string; dm?:T.DMThread;
  capture:()=>Snapshot; reason:string; page?:boolean; reviewOnly?:boolean;
}) {
  const store = useApp(), revision = useStore(store,s=>s.contentRevision), overview = useStore(store,s=>s.overview);
  const tab = useStore(store,s=>s.tab), showing = !page || tab === "outbox";
  const [snapshot,setSnapshot] = useState<Snapshot | null>(null), [due,setDue] = useState(localScheduleTime);
  const [audience,setAudience] = useState<T.ScheduledAudience | null>(null);
  const slot=useStore(store,s=>s.scheduleSaves[conv]);
  const blocked=!!slot && slot.status!=="refused";
  const [notice,setNotice] = useState(""), [found,setFound] = useState<T.ScheduledSend | null>(null), [forgetting,setForgetting] = useState(false);
  const [rows,setRows] = useState<T.ScheduledSend[]>([]), [selected,setSelected] = useState<T.ScheduledSend | null>(null);
  const [historyOpen,setHistoryOpen] = useState(false);
  const [editOrder,setEditOrder] = useState<string[]>([]);
  const [query,setQuery] = useState("");
  const [body,setBody,retainedBody] = useRetainedText(JSON.stringify(["schedule-edit", conv, selected?.conv, selected?.id, selected?.rev, selected?.body]), selected?.body || "");
  const [editTime,setEditTime] = useState("");
  const [removeFiles,setRemoveFiles] = useState(false), [review,setReview] = useState<T.ScheduledAudience | null>(null), [approved,setApproved] = useState<T.ScheduledAudience | undefined>();
  const [loading,setLoading] = useState(false), [busy,setBusy] = useState(false), busyRef = useRef(false);
  const epoch = useRef(0), mounted = useRef(true);
  useEffect(() => {mounted.current=true;return ()=>{mounted.current=false;epoch.current++;};},[]);

  useEffect(() => {
    if (pane !== "create" || reviewOnly) return;
    if(blocked){setSnapshot(null);setAudience(null);setLoading(false);return;}
    let current = true;
    const next = capture();setSnapshot(next);setDue(localScheduleTime());setAudience(null);setNotice("");setLoading(true);
    store.api.scheduleAudience(conv,next.draft.topic || "").then(a=>{if(current)setAudience(a);},()=>{if(current)setNotice("The current audience could not be verified. Your draft is kept. Close and reopen to review it again.");}).finally(()=>{if(current)setLoading(false);});
    return ()=>{current=false;};
  },[pane,conv,store,blocked,reviewOnly]);
  useEffect(() => {setHistoryOpen(false);},[pane,conv]);
  useEffect(() => {
    if (pane !== "list") { epoch.current++;setSelected(null);setRows([]);setReview(null);setApproved(undefined);return; }
    if (!showing) { epoch.current++;return; }
    let current = true;const request=++epoch.current;setLoading(true);
    store.api.schedules(conv,historyOpen).then(list=>{if(current && request===epoch.current)setRows(list || []);},()=>{if(current)setNotice("Scheduled messages could not be loaded. Reopen Scheduled to try again.");}).finally(()=>{if(current)setLoading(false);});
    return ()=>{current=false;};
  },[pane,conv,store,revision,historyOpen,showing]);

  const views = useStore(store,s=>s.views);
  const selectedDM = selected ? views?.[selected.conv] as T.DMThread | undefined : dm;
  const people = [overview?.person,...(overview?.people || []),...(selectedDM?.members || [])].filter((p):p is T.PersonView=>!!p && (p.state === "self" || p.state === "pinned"));
  function chatTitle(row:T.ScheduledSend) {
    const summary = overview?.dms?.find(d=>d.id===row.conv);
    const cached = views?.[row.conv] as T.DMThread | undefined;
    return (summary?.kind === "group" || cached?.kind === "group" ? summary?.title || cached?.title : summary?.peer?.label || cached?.peer?.label) || summary?.title || cached?.title || "Chat "+row.conv.slice(0,8);
  }
  function audienceText(a: T.ScheduledAudience) {
    const persons = (a.persons || []).map(id=>people.find(p=>p.person===id)?.label || "Verified participant ("+id.slice(0,8)+")");
    const pids = (a.pids || []).map(id=>{const guest=selectedDM?.guests?.find(g=>g.pid===id), agent=selectedDM?.agents?.find(g=>g.pid===id);return guest?.host.label || (agent ? (agent.host.label || "Verified host")+" · invited agent" : "Invited participant ("+id.slice(0,8)+")");});
    return [...persons,...pids].join(", ") || "No recipients";
  }
  async function refresh() { if(page&&store.get().tab!=="outbox")return;const request=++epoch.current;const list=await store.api.schedules(conv,historyOpen);if(mounted.current&&store.isActive()&&request===epoch.current)setRows(list || []); }
  async function save(attempt: ScheduleAttempt) {
    if (busyRef.current) return;
    try {store.beginScheduleSave(conv,attempt);}catch {setNotice("Resolve the original unconfirmed save before starting another schedule.");return;}
    busyRef.current=true;setBusy(true);setNotice("");
    try {
      await submitSchedule(store.api,attempt);
      store.settleScheduleSave(conv,attempt.payload.id!,"confirmed");
      if(mounted.current&&store.isActive()){onPane(null);store.toast("Scheduled for "+dueText(attempt.payload.due)+" · Outbox → Scheduled", "ok");}
    } catch (error) {
      const refused=scheduleRefused(error);
      store.settleScheduleSave(conv,attempt.payload.id!,refused?"refused":"uncertain",refused?"Save refused. Your draft is kept. Review the chat, audience, time and attachments before a changed save.":"Save not confirmed. Retry this original same-ID save; it may already be scheduled.");
    } finally { busyRef.current=false;setBusy(false); }
  }
  function start() {
    if (!snapshot || !audience || reason || blocked || busyRef.current) return;
    try { void save(captureSchedule(sendID(),conv,snapshot.body,snapshot.draft,scheduleDue(due),timezone(),audience)); }
    catch {setNotice("Choose a future time and include a message or file. Your draft is kept.");}
  }
  async function lookup(p: ScheduleSave) {
    if(busyRef.current)return;busyRef.current=true;setBusy(true);setFound(null);
    try {
      const row=await store.api.schedule(p.id);
      if(row.id!==p.id || row.conv!==conv)throw Error("Saved state did not match this chat.");
      store.settleScheduleSave(conv,p.id,"uncertain",p.attempt?"Saved state found; confirm with same-ID save. The lookup alone cannot verify attachment contents and does not clear your draft.":"Saved state found. Compare it with your draft before explicitly finishing this review. Your draft is kept.");
      if(mounted.current&&store.isActive())setFound(row);
    } catch {store.settleScheduleSave(conv,p.id,"uncertain","Saved state could not be confirmed. It may still be saved by an earlier request. This original intent is kept; no new ID was allocated.");}
    finally {busyRef.current=false;setBusy(false);}
  }
  function edit(row:T.ScheduledSend) {setEditOrder(activeRows.map(r=>r.id));setSelected(row);setEditTime(localScheduleTime(new Date(row.due)));setRemoveFiles(false);setReview(null);setApproved(undefined);setNotice("");}
  async function reviewAudience(row:T.ScheduledSend) {
    if(busyRef.current)return;busyRef.current=true;setBusy(true);setNotice("");
    try {const fresh=await store.api.scheduleAudience(row.conv,row.topic);if(mounted.current&&store.isActive())setReview(fresh);}
    catch {setNotice("The current audience could not be verified. The saved audience is unchanged.");}
    finally {busyRef.current=false;setBusy(false);}
  }
  async function action(run:()=>Promise<unknown>) {
    if(busyRef.current)return;busyRef.current=true;setBusy(true);setNotice("");
    try {await run();if(mounted.current&&store.isActive()){setSelected(null);setReview(null);setApproved(undefined);await refresh();}}
    catch {if(mounted.current&&store.isActive()){setSelected(null);setReview(null);setApproved(undefined);try {await refresh();}catch{}setNotice("The action was not confirmed or the revision changed. Review the refreshed list before trying again. No audience was changed automatically.");}}
    finally {busyRef.current=false;setBusy(false);}
  }
  const previous = slot?.attempt;
  const same = !!previous && !!snapshot && previous.payload.body===snapshot.body && previous.draft.text===snapshot.draft.text && previous.payload.topic===(snapshot.draft.topic || "") && previous.payload.reply_to===snapshot.draft.replyTo && localScheduleTime(new Date(previous.payload.due))===due && JSON.stringify(previous.payload.audience)===JSON.stringify(audience) && previous.files.length===(snapshot.draft.files || []).length && previous.files.every((f,i)=>f.file===snapshot.draft.files?.[i].file);
  const localTimezone = timezone();
  const matches = (row:T.ScheduledSend) => !query.trim() || (chatTitle(row)+" "+row.body+" "+(row.files || []).map(f=>f.name).join(" ")).toLowerCase().includes(query.trim().toLowerCase());
  // Moving a keyed DOM row can still drop native input focus. Keep its place
  // while an editor is open; new pending rows append until the edit closes.
  const positions = new Map(selected ? editOrder.map((id,i)=>[id,i]) : []);
  const activeRows = rows.filter(row=>row.state!=="sent"&&row.state!=="cancelled").sort((a,b)=>(positions.get(a.id) ?? Number.MAX_SAFE_INTEGER)-(positions.get(b.id) ?? Number.MAX_SAFE_INTEGER)||a.due.localeCompare(b.due));
  const historyRows = rows.filter(row=>row.state==="sent"||row.state==="cancelled");
  const runningCopy = store.host.platform === "browser"
    ? "Keep this page open and online to send."
    : store.host.platform === "android"
    ? "Sends while AgentNet is running and online. Background limits can delay sending."
    : "Sends from this device while AgentNet is running and online.";
  const close = () => {if(!busyRef.current){retainedBody.discard();onPane(null);setNotice("");setSelected(null);setReview(null);setApproved(undefined);}};

  const renderRow = (current:T.ScheduledSend) => {
    const expanded = selected?.id===current.id;
    // A push may replace the list row; an open edit keeps its captured revision.
    const row = expanded ? selected! : current;
    return <article key={row.id} className="border-b border-hairline py-3" aria-label={"Scheduled message: "+(states[row.state] || "Unavailable")}>
    <div className="flex min-w-0 items-start gap-3">
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-semibold">{chatTitle(row)}</p>
        <p className="mt-1 truncate text-sm">{row.body.slice(0,180) || (row.files?.length ? row.files.length+" attachment"+(row.files.length===1?"":"s") : "Message")}</p>
        <p className="mt-1 flex flex-wrap gap-x-2 text-[13px] text-text-2"><time className="whitespace-nowrap" dateTime={row.due}>{new Date(row.due).toLocaleString([], {dateStyle:"medium",timeStyle:"short"})}</time><span className="whitespace-nowrap">{states[row.state] || "Unavailable"}</span>{!!row.files?.length&&<span className="whitespace-nowrap">{row.files.length+" file"+(row.files.length===1?"":"s")}</span>}</p>
      </div>
      <Button size="sm" variant="ghost" disabled={busy} aria-expanded={expanded} onClick={()=>{if(expanded){retainedBody.discard();setSelected(null);}else edit(current);}}>{expanded?"Close details":mutable(row)?"Edit schedule":"View schedule"}</Button>
    </div>
    {expanded&&<div className="mt-3 rounded-xl bg-surface p-3 stroke">
    <p className="font-semibold"><time dateTime={row.due}>{new Date(row.due).toLocaleString([], {dateStyle:"medium",timeStyle:"short"})}</time></p>
    <p className="mt-1 text-sm text-text-2">{states[row.state] || "Unavailable"}{row.topic && " · "+(selectedDM?.topics?.find(t=>t.id===row.topic)?.title || "Topic")}</p>
    <p className="mt-2 whitespace-pre-wrap break-words text-sm">{row.body}</p>
    {row.files?.map((f,i)=><p key={i} className="mt-1 break-all text-sm text-text-2">{f.name}</p>)}
    {row.state==="needs_review"&&<p className="mt-2 text-sm text-text-2">The chat, audience or attachments need review before sending.</p>}
    {row.state==="dispatching"&&<p className="mt-2 text-sm text-text-2">Preparing to send. Delivery is confirmed separately.</p>}
    {mutable(row)&&<div className="mt-3 flex flex-wrap gap-2"><Button size="sm" disabled={busy} onClick={()=>void action(()=>store.api.sendScheduleNow(row.id,row.rev))}>Send now</Button><Button size="sm" variant="ghost" disabled={busy} onClick={()=>void action(()=>store.api.cancelSchedule(row.id,row.rev))}>Cancel schedule</Button></div>}
    <details className="mt-2 text-sm text-text-2">
      <summary className="min-h-11 cursor-pointer py-3 font-semibold">Recipients</summary>
      <p className="break-words">{audienceText(row.audience)}</p>
      {row.timezone!==localTimezone&&<p className="mt-1">Saved timezone: {row.timezone}</p>}
    </details>
    {mutable(row)&&selected?.id===row.id&&<div className="mt-3 space-y-3 border-t border-hairline pt-3">
      <label className="block text-sm font-semibold">Message<textarea aria-label="Scheduled message text" className={input} value={body} onChange={e=>setBody(e.target.value)} disabled={busy}/></label>
      <fieldset className="min-w-0"><legend className="text-sm font-semibold">Change send time</legend>
        <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="min-w-0 text-sm">Reschedule date<input aria-label="Reschedule date" type="date" className={input} value={editTime.split("T")[0]} onChange={e=>setEditTime(e.target.value+"T"+(editTime.split("T")[1] || ""))} disabled={busy}/></label>
          <label className="min-w-0 text-sm">Reschedule time<input aria-label="Reschedule time" type="time" className={input} value={editTime.split("T")[1] || ""} onChange={e=>setEditTime(editTime.split("T")[0]+"T"+e.target.value)} disabled={busy}/></label>
        </div>
        <p className="mt-2 text-sm text-text-2">{scheduleSummary(editTime)}</p>
      </fieldset>
      {!!row.files?.length&&<label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={removeFiles} onChange={e=>setRemoveFiles(e.target.checked)} disabled={busy}/>Remove all attachments</label>}
      <p className="text-sm text-text-2">{approved?"New audience explicitly confirmed: "+audienceText(approved):"Editing text or time keeps the saved audience ceiling and attachments unless explicitly removed."}</p>
      <Button size="sm" disabled={busy} onClick={()=>void reviewAudience(row)}>Review audience</Button>
      {review&&<div className="rounded-xl bg-sunken p-3 text-sm"><p>{audienceText(review)}</p><Button size="sm" disabled={busy} onClick={()=>{setApproved(review);setReview(null);}}>Confirm reviewed audience</Button></div>}
      <div className="flex flex-wrap gap-2"><Button variant="act" size="sm" disabled={busy} onClick={()=>{try {const change=scheduleEdit(selected,body,scheduleDue(editTime),timezone(),removeFiles,approved),confirmed=retainedBody.capture();void action(async()=>{await store.api.editSchedule(change);confirmed();});}catch{setNotice("Choose a date and time in the future. No change was saved.");}}}>Save schedule changes</Button><Button variant="ghost" size="sm" disabled={busy} onClick={()=>{retainedBody.discard();setSelected(null);setReview(null);setApproved(undefined);}}>Keep saved schedule</Button></div>
    </div>}
    </div>}
  </article>;
  };

  const content = <>
    {!page&&<nav aria-label="Scheduling" className="flex flex-wrap gap-2 pt-2">
      {!reviewOnly&&<Button size="sm" variant={pane==="create"?"outline":"ghost"} aria-pressed={pane==="create"} disabled={!!reason||busy} onClick={()=>{if(!reason&&!busyRef.current&&pane!=="create")onPane("create");}}>Schedule message</Button>}
      <Button size="sm" variant={pane==="list"?"outline":"ghost"} aria-pressed={pane==="list"} disabled={busy} onClick={()=>{if(!busyRef.current&&pane!=="list")onPane("list");}}>Scheduled messages</Button>
    </nav>}
    {notice && <p role="status" className="my-3 rounded-xl bg-sunken p-3 text-sm">{notice}</p>}
    {pane==="create" && !blocked && !reviewOnly && <div className="space-y-4 pt-2">
      {reason && <p role="alert" className="text-sm text-danger">{reason}</p>}
      <div className="rounded-xl bg-surface p-3 stroke"><p className="whitespace-pre-wrap break-words text-sm">{snapshot?.body || "Attachment message"}</p>{snapshot?.draft.files?.map(f=><p key={f.key} className="mt-1 break-all text-sm text-text-2">{f.name}</p>)}</div>
      <fieldset className="min-w-0"><legend className="text-sm font-semibold">When to send</legend>
        <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="min-w-0 text-sm">Send date<input aria-label="Send date" type="date" value={due.split("T")[0]} onChange={e=>setDue(e.target.value+"T"+(due.split("T")[1] || ""))} className={input} disabled={busy}/></label>
          <label className="min-w-0 text-sm">Send time<input aria-label="Send time" type="time" value={due.split("T")[1] || ""} onChange={e=>setDue(due.split("T")[0]+"T"+e.target.value)} className={input} disabled={busy}/></label>
        </div>
        <p className="mt-2 text-sm text-text-2">Local timezone: {timezone()}<span className="block">{scheduleSummary(due)}</span></p>
      </fieldset>
      <div className="text-sm"><p className="font-semibold">Approved audience</p><p>{loading?"Verifying current audience…":audience?audienceText(audience):"Audience unavailable"}</p><p className="mt-1 text-text-2">This exact preview is saved as the audience ceiling. Membership and keys are checked again before sending.</p></div>
      <Button variant="act" disabled={busy||loading||!audience||!!reason||same||blocked} onClick={start}>{slot?"Save changed schedule":"Save schedule"}</Button>
      {slot && <p className="text-sm text-text-2">A changed save starts a separate schedule after the prior refusal.</p>}
    </div>}
    {slot && <div aria-label="Unconfirmed schedule save" className="mt-4 rounded-xl bg-sunken p-3 text-sm">
      <p className="font-semibold">{slot.status==="saving"?"Saving schedule…":slot.status==="refused"?"Save refused":"Unconfirmed save"}</p>
      {slot.attempt && <><p className="whitespace-pre-wrap break-words">{slot.attempt.payload.body}</p><p>{new Date(slot.attempt.payload.due).toLocaleString()} · {slot.attempt.payload.timezone}</p></>}
      {slot.files.map((f,i)=><p key={i} className="break-all">{f.name} · {f.size} bytes</p>)}
      <p>{slot.note}</p>
      {!slot.durable&&<p role="status">This unconfirmed save won’t be remembered if the page reloads. Keep AgentNet open until you resolve it.</p>}
      <div className="mt-2 flex flex-wrap gap-2">
        {slot.attempt&&<Button size="sm" disabled={busy||slot.status==="saving"} onClick={()=>void save(slot.attempt!)}>Retry same save</Button>}
        <Button size="sm" variant="ghost" disabled={busy||slot.status==="saving"} onClick={()=>void lookup(slot)}>Check saved state</Button>
        <Button size="sm" variant="ghost" disabled={busy||slot.status==="saving"} onClick={()=>setForgetting(true)}>Forget…</Button>
      </div>
      {found?.id===slot.id&&!slot.attempt&&<div className="mt-3 rounded-xl bg-surface p-3"><p className="whitespace-pre-wrap break-words">{found.body}</p><p>{new Date(found.due).toLocaleString()} · {found.timezone} · {states[found.state] || "Unavailable"}</p>{found.files?.map((f,i)=><p key={i}>{f.name} · {f.size} bytes</p>)}<p>Compare this saved state with your draft. Attachment names and sizes do not prove identical contents.</p><Button size="sm" disabled={busy} onClick={()=>{store.forgetScheduleSave(conv,slot.id);setFound(null);onPane("list");setNotice("Saved-state review finished. Your draft is kept; compare it before saving another schedule.");}}>Keep draft and finish review</Button></div>}
      {forgetting&&<div className="mt-3"><p>It may already be scheduled. Check Scheduled messages before saving again.</p><Button size="sm" variant="danger" disabled={busy||slot.status==="saving"} onClick={()=>{store.forgetScheduleSave(conv,slot.id);setForgetting(false);setFound(null);}}>Forget unconfirmed save</Button></div>}
    </div>}
    {pane==="list" && <div className="space-y-3 pt-3">
      {page&&<><p className="text-sm text-text-2">{runningCopy}</p><label className="block text-sm font-semibold">Find scheduled messages<input type="search" className={input} value={query} onChange={e=>setQuery(e.target.value)} placeholder="Chat, message or attachment"/></label></>}
      {loading && <p role="status">Loading scheduled messages…</p>}
      <p className="text-sm text-text-2">This device · Local timezone: {localTimezone}</p>
      {!loading&&!activeRows.length&&<p>{page?"No scheduled messages pending on this device.":"No scheduled messages pending in this chat on this device."}</p>}
      {!!query.trim()&&!activeRows.some(matches)&&<p>No pending messages match your search.</p>}
      <section aria-label="Active scheduled messages">
      {activeRows.filter(matches).map(renderRow)}
      </section>
      {<div className="space-y-3">
        <Button size="sm" variant="ghost" aria-label={historyOpen?"Hide history":"Show history"} aria-expanded={historyOpen} onClick={()=>setHistoryOpen(open=>!open)}>{historyOpen?"Hide history":"Show history"}{historyOpen&&!loading&&" ("+historyRows.length+")"}</Button>
        {historyOpen&&<section aria-label="Scheduled message history">{historyRows.filter(matches).map(renderRow)}</section>}
        {historyOpen&&!loading&&!historyRows.length&&<p className="text-sm text-text-2">No scheduled message history on this device.</p>}
      </div>}
    </div>}
  </>;
  return page ? <section aria-label="Scheduled messages">{content}</section> : <Sheet open={!!pane} onOpenChange={open=>{if(!open)close();}} title={pane==="create"?"Schedule message":"Scheduled messages"} description={runningCopy}>{content}</Sheet>;
}
