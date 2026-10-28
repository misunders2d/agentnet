// Whole-topic continuation: a finite reviewed quotation, with its source
// location private to this person. No helper is enrolled by this action.
import { useEffect, useRef, useState } from "react";
import { errorText, type T, type TopicTransferLocation, type TopicTransferPage, type TopicTransferReview, type TopicTransferScope } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Markdown } from "./Markdown";

export function canContinueTopic(dm?: T.DMThread, thread?: T.Thread, fresh = false): boolean {
  return !fresh && (dm ? !dm.frozen && (!dm.role || dm.role === "member") : !!thread && !thread.key?.pending);
}

export function TopicContinuation({ dm, thread, topic = "", fresh = false, open, onClose }: { dm?: T.DMThread; thread?: T.Thread; topic?: string; fresh?: boolean; open: boolean; onClose: () => void }) {
  const store = useApp();
  const seq = useStore(store, s => s.overview?.seq);
  const [location, setLocation] = useState<TopicTransferLocation | null>(null);
  const source = dm?.id || "", peer = thread?.peer || "", anchor = thread?.topic?.id || thread?.id || "";
  const scope: TopicTransferScope = source ? {source, topic} : {peer, thread: anchor};
  const eligible = canContinueTopic(dm, thread, fresh);
  useEffect(() => {
    let current = true; setLocation(null);
    if (eligible) store.api.topicTransferLocation(source ? {source, topic} : {peer, thread: anchor}).then(v => {if (current) setLocation(v?.operation ? v : null);}).catch(() => {});
    return () => {current = false;};
  }, [store, source, peer, anchor, topic, eligible, seq, open]);
  if (!eligible) return null;
  return <>
    {location && <div className="border-b border-hairline px-4 py-1 text-[13px]" role="status">This topic’s snapshot continued in another chat. {location.available
      ? <button className="font-semibold underline underline-offset-2" onClick={() => void store.openUnread({kind: "dm", id: location.destination}, location.destination_topic)}>Open destination</button>
      : <span className="text-text-2">Destination is unavailable here.</span>} <span className="text-text-2">Only you see this location.</span></div>}
    {open && <TransferTopic scope={scope} onClose={onClose} />}
  </>;
}

function TransferTopic({ scope, onClose }: { scope: TopicTransferScope; onClose: () => void }) {
  const store = useApp();
  const overview = useStore(store, s => s.overview);
  const [review, setReview] = useState<TopicTransferReview | null>(null), [page, setPage] = useState<TopicTransferPage | null>(null);
  const [destination, setDestination] = useState(""), [target, setTarget] = useState<T.DMThread | null>(null), [topic, setTopic] = useState(""), [title, setTitle] = useState("");
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [error, setError] = useState(""), [confirmed, setConfirmed] = useState(false);
  const [audience, setAudience] = useState<TopicTransferReview["audience"]>([]), [audienceOffset, setAudienceOffset] = useState(0), [audienceNext, setAudienceNext] = useState(0);
  const running = useRef(false);
  const chats = (overview?.dms || []).filter(d => d.id !== scope.source && (!d.kind || d.kind === "dm" || d.kind === "group"));
  useEffect(() => {
    let active = true;
    store.api.topicTransferStatus(scope).then(v => {if (active && v?.id) {setReview(v);setDestination(v.destination);}}).catch(e => {if (active) setError(errorText(e));}).finally(() => {if (active) setLoading(false);});
    return () => {active = false;running.current = false;};
  }, [store]);
  useEffect(() => {
    let active = true;setTarget(null);
    if (destination) store.api.dm(destination).then(v => {if (active) setTarget(v);}).catch(e => {if (active) setError(errorText(e));});
    return () => {active = false;};
  }, [store, destination]);
  useEffect(() => {
    let active = true;setPage(null);
    setAudience(review?.audience || []);setAudienceOffset(0);setAudienceNext(review?.audience_next || 0);
    if (review?.id && review.total_batches) store.api.topicTransferPage({id: review.id, token: review.token, after: Math.min(review.cursor, review.total_batches - 1)}).then(v => {if (active) setPage(v);}).catch(e => {if (active) setError(errorText(e));});
    return () => {active = false;};
  }, [store, review?.id]);
  const close = () => {running.current = false;onClose();};
  async function preview() {
    setBusy(true);setError("");setConfirmed(false);
    try {setReview(await store.api.previewTopicTransfer({...scope, destination, ...(topic === "new" ? {new_topic: true, title} : topic ? {destination_topic: topic} : {})}));}
    catch(e) {setError(errorText(e));} finally {setBusy(false);}
  }
  async function show(after: number) {
    if (!review) return;setBusy(true);setError("");
    try {setPage(await store.api.topicTransferPage({id: review.id, token: review.token, after}));}catch(e) {setError(errorText(e));}finally {setBusy(false);}
  }
  async function showAudience(offset: number) {
    if (!review) return;setBusy(true);setError("");
    try {const p=await store.api.topicTransferPage({id:review.id,token:review.token,audience_after:offset});setAudience(p.audience||[]);setAudienceOffset(offset);setAudienceNext(p.audience_next||0);}catch(e) {setError(errorText(e));}finally {setBusy(false);}
  }
  async function apply() {
    if (!review) return;running.current = true;setBusy(true);setError("");
    let next = review;
    try {
      while (running.current && next.cursor < next.total_batches && next.state !== "canceled") {
        next = await store.api.applyTopicTransfer({id: next.id, token: next.token, cursor: next.cursor});setReview(next);
      }
    } catch(e) {setError(errorText(e));} finally {running.current = false;setBusy(false);}
  }
  async function cancel() {
    if (!review) return;running.current = false;setError("");
    try {setReview(await store.api.cancelTopicTransfer({id: review.id, token: review.token}));}catch(e) {setError(errorText(e));}
  }
  const done = review?.state === "queued", canceled = review?.state === "canceled";
  return <Sheet open onOpenChange={v => {if (!v) close();}} title="Continue whole topic" description="Review a finite snapshot and its audience before continuing in another chat." wide footer={<div className="flex flex-wrap justify-end gap-2">
    <Button variant="ghost" onClick={close}>{busy ? "Pause and close" : "Close"}</Button>
    {review && !done && !canceled && <Button variant="ghost" onClick={() => void cancel()}>Cancel remaining</Button>}
    {!review ? <Button variant="act" disabled={loading || busy || !target || topic === "new" && !title.trim()} onClick={() => void preview()}>Review whole topic</Button>
      : !done && !canceled && <Button variant="act" disabled={busy || !confirmed || !page} onClick={() => void apply()}>{busy ? "Queuing…" : review.cursor ? "Resume this snapshot" : "Continue with this snapshot"}</Button>}
    {done && <Button variant="act" onClick={() => {close();void store.openUnread({kind: "dm", id: review.destination}, review.destination_topic || "");}}>Open destination</Button>}
  </div>}>
    <div className="space-y-4">
      <p className="text-[14px] text-text-2">Original records stay in this chat. Later posts and running requests stay here. Quoted questions, tasks and mention links are context; they do not run. No helper is invited.</p>
      {loading ? <p role="status">Reading saved snapshot…</p> : !review ? <fieldset disabled={busy} className="space-y-3">
        <label className="block font-semibold">Destination chat or group<select value={destination} onChange={e => {setDestination(e.target.value);setTopic("");}} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke"><option value="">Choose a chat or group</option>{chats.map(d => <option key={d.id} value={d.id}>{d.title || d.peer?.label || d.peer?.address || "Unnamed chat"}</option>)}</select></label>
        {target && <label className="block font-semibold">Destination topic<select value={topic} onChange={e => setTopic(e.target.value)} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke"><option value="">Main flow</option><option value="new">A new topic</option>{(target.topics || []).filter(t => t.state === "active").map(t => <option key={t.id} value={t.id}>{t.title || "Untitled topic"}</option>)}</select></label>}
        {topic === "new" && <label className="block font-semibold">New topic name<input value={title} onChange={e => setTitle(e.target.value)} maxLength={120} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke" /></label>}
      </fieldset> : <>
        <p className="font-semibold">{review.total_items} source messages · {review.total_files} files · {review.total_batches} quotation parts</p>
        <section aria-label="Reviewed audience" className="rounded-xl bg-surface p-3 stroke"><p className="font-semibold">Who can receive this snapshot ({review.audience_total || review.audience.length})</p><ul className="mt-2 max-h-36 overflow-auto space-y-1 text-[13px]">{audience.map((a, i) => <li key={i} className="break-words">{a.label || a.address}{a.label && a.label !== a.address ? ` (${a.address})` : ""} · {a.role}</li>)}</ul>{(review.audience_total || review.audience.length)>16 && <div className="mt-2 flex justify-between gap-2"><Button variant="ghost" size="sm" disabled={busy||audienceOffset===0} onClick={()=>void showAudience(Math.max(0,audienceOffset-16))}>Previous recipients</Button><Button variant="ghost" size="sm" disabled={busy||!audienceNext} onClick={()=>void showAudience(audienceNext)}>Next recipients</Button></div>}<p className="mt-2 text-[13px] text-text-2">Current members and following guests/helpers shown. Pending invitees excluded; later access follows normal invitations.</p></section>
        <section aria-label="Snapshot preview" className="rounded-xl bg-surface p-3 stroke"><p className="font-semibold">Quotation preview {page ? page.after + 1 : ""} of {review.total_batches}</p>{page ? <><div className="mt-2 max-h-56 overflow-auto"><Markdown text={page.body} /></div>{page.items.flatMap(m => m.files).map((f, i) => <p key={i} className="mt-2 text-[13px]">{f.name} · {f.unsupported || (f.available ? "included" : "unavailable — acquire this exact source file before continuing")}</p>)}<div className="mt-2 flex justify-between gap-2"><Button variant="ghost" size="sm" disabled={busy || page.after === 0} onClick={() => void show(page.after - 1)}>Previous part</Button><Button variant="ghost" size="sm" disabled={busy || page.done} onClick={() => void show(page.next)}>Next part</Button></div></> : <p role="status">Reading exact source references…</p>}</section>
        <p role="status" className="font-semibold">{done ? "All quotation parts queued. Delivery follows each message’s status." : canceled ? `Canceled. ${review.cursor} already queued parts keep their delivery status.` : `${review.cursor} of ${review.total_batches} parts queued. Closing pauses the remaining parts; reopen here to resume.`}</p>
        {!done && !canceled && <label className="flex min-h-11 items-center gap-2 text-[14px]"><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} />I reviewed the audience and the whole-topic snapshot.</label>}
        {!busy && <Button variant="ghost" onClick={() => {setReview(null);setPage(null);setConfirmed(false);}}>Choose a fresh snapshot</Button>}
      </>}
      {error && <p role="alert" className="text-danger">{error}</p>}
    </div>
  </Sheet>;
}
