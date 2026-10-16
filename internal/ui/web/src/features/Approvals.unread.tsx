import { useEffect } from "react";
import { useApp } from "../context";
import { useStore } from "../store";
import type { T } from "../api";
import { when } from "../model";
import { Button } from "../ui/Button";
import { usePagedChoices, useSteadyChoices } from "./Conversation.choices";

const key=(r:T.UnreadRow)=>r.key;
export function UnreadReview(){
 const store=useApp(),overview=useStore(store,s=>s.overview),revision=useStore(store,s=>s.contentRevision);
 const supported=typeof overview?.unread_total==="number";
 const page=usePagedChoices(supported,"review-unread",revision,50,key,async(after,limit)=>{
  const p=await store.api.unread({after,limit});
  if(p.seq<(overview?.seq||0))throw Error("Unread messages changed while loading. Try again.");
  return {...p,rows:p.rows||[]};
 },store.unreadReviewSize);
 useEffect(()=>{if(page.rows.length)store.unreadReviewSize=page.rows.length;},[page.rows.length]);
 const stable=useSteadyChoices(page.rows,key,"review-unread");
 const scope=(r:T.UnreadRow)=>{
  if(r.kind==="device")return (r.peer||"Agent chat")+(r.topic_title?" · "+r.topic_title:"");
  const chat=overview?.dms?.find(d=>d.id===r.conv);
  const title=chat?.kind==="group"?chat.title:chat?.peer.label;
  return (title||"Chat")+" · "+(r.topic?r.topic_title||"Topic":"Main flow");
 };
 return <section className="px-4 pt-6" aria-labelledby="review-unread-title">
  <h2 id="review-unread-title" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Unread{supported?" · "+(page.loading||page.error?overview!.unread_total:page.total):""}</h2>
  {!supported?<p className="pt-2 text-[14px] text-text-2">Unread messages are not available from this connection.</p>:<>
   <p className="pt-1 text-[14px] text-text-2">Open a message in its chat. Decisions waiting for you stay separate.</p>
   {page.error&&<div role="alert" className="pt-2 text-[14px] text-danger">Unread messages couldn’t load: {page.error} <Button size="sm" variant="outline" onClick={page.retry}>Try again</Button></div>}
   {page.loading&&<p role="status" className={page.rows.length?"sr-only":"pt-2 text-[14px] text-text-2"}>Loading unread messages…</p>}
   {!page.loading&&!page.error&&!page.rows.length&&<p role="status" className="pt-2 text-[14px] text-text-2">No unread messages.</p>}
   <ul {...stable.bind} aria-label="Unread messages" aria-busy={page.loading} className="mt-2 flex flex-col gap-1">
    {stable.choices.map(({row:r,present})=><li key={r.key} data-unread-key={r.key}>
     <button type="button" aria-disabled={!present} onClick={()=>{if(present){stable.release();void store.openUnreadRow(r);}}} aria-label={scope(r)+", "+r.label+", "+r.preview+(present?". Open message.":". No longer unread.")}
      className={"flex min-h-11 w-full flex-col gap-1 rounded-2xl px-3 py-3 text-left "+(present?"bg-surface hover:bg-sunken":"bg-sunken text-muted")}>
      <span className="flex min-w-0 items-center gap-2"><span className="min-w-0 flex-1 truncate text-[14px] font-bold">{scope(r)}</span><time dateTime={r.at} className="shrink-0 text-[12px] text-muted tnum">{when(r.at)}</time></span>
      <span className="text-[13px] font-semibold text-text-2">{r.dir==="out"?"You":r.label}</span>
      <span className="text-[14px] [overflow-wrap:anywhere]">{present?r.preview||"Attachment": "No longer unread"}</span>
     </button>
    </li>)}
   </ul>
   {page.next&&<div className="pt-2"><Button size="sm" variant="outline" disabled={page.loading} onClick={page.more}>Show more unread messages</Button></div>}
  </>}
 </section>;
}
