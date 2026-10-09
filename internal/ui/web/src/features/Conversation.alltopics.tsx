// All topics: every topic with one agent, a page at a time from the server
// (GET /api/topics): search by name or last line, Active / Done / Archived.
// A side panel on desktops, a full-screen sheet on phones. Archived topics
// are only listed here; opening one shows it as it was, nothing deleted.
import { useEffect, useRef, useState } from "react";
import { Dialog } from "@base-ui/react/dialog";
import { IconChevronLeft, IconSearch, IconX } from "@tabler/icons-react";
import { errorText } from "../api";
import { useApp, useWide } from "../context";
import { TOPICS, firstLine, topicOf, when, type Topic, type TopicState } from "../model";
import { useModal, usePortal } from "../owned";
import { useStore } from "../store";
import { Button, IconButton } from "../ui/Button";
import { TopicMark, topicLabel } from "./Conversation.topics";

const filters: { id: TopicState; label: string; none: string }[] = [
  { id: "active", label: "Active", none: "No active topics" },
  { id: "done", label: "Done", none: "No done topics" },
  { id: "archived", label: "Archived", none: "Nothing archived yet" },
];

export interface FlatTopics {
 topics: Topic[];
 loading: boolean;
 failed: string;
 canSelect: (id: string) => boolean;
 detail: (id: string) => string;
 choose: (id: string) => void;
 change: (what: "delete" | "done" | "archive", ids: string[], counts: Record<string,number>) => Promise<{note: string} | undefined>;
}

export function AllTopics({ open, onOpenChange, peer,conv, agent, current, flat }: {
  open: boolean; onOpenChange: (o: boolean) => void; peer: string;conv?:string; agent: string; current?: string; flat?: FlatTopics;
}) {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const seq = overview?.seq;
  const [filter, setFilter] = useState<TopicState>("active");
  const [query, setQuery] = useState("");
  const [q, setQ] = useState(""); // the query after the person stops typing
  const [loadedItems, setItems] = useState<Topic[]>([]);
  const [next, setNext] = useState("");
  const [matched, setMatched] = useState(0);
  const [ownLoading, setLoading] = useState(false);
  const [ownFailed, setFailed] = useState("");
  const items=flat?flat.topics.filter(t=>t.state===filter&&(t.title+" "+t.last).toLowerCase().includes(q.toLowerCase())):loadedItems;
  const loading=flat?.loading??ownLoading,failed=flat?.failed??ownFailed;
  const matchedCount=flat?items.length:matched;
  const [selected,setSelected]=useState<string[]>([]);
  const [counts,setCounts]=useState<Record<string,number>>({});
  const [action,setAction]=useState<"delete"|"done"|"archive"|null>(null);
  const [undo,setUndo]=useState(false);
  const [busy,setBusy]=useState(false);
  const timer=useRef<ReturnType<typeof setTimeout>|null>(null);
  const cancel=()=>{if(timer.current)clearTimeout(timer.current);timer.current=null;setUndo(false);setAction(null);};
  useEffect(()=>()=>{if(timer.current)clearTimeout(timer.current);},[]);
  useEffect(()=>{if(!open){cancel();setSelected([]);}},[open]);
  const commit=()=>{
    setUndo(true);const ids=[...selected],covered={...counts},what=action!;
    timer.current=setTimeout(()=>{
      timer.current=null;setUndo(false);setBusy(true);
      void (flat?flat.change(what,ids,covered):store.run(a=>a.changeTopic(what,{conv,peer,id:"",ids,counts:covered}))).then(r=>{if(r){if(what==="delete"&&conv&&ids.includes(store.draft(conv).topic||""))store.setDraft(conv,{...store.draft(conv),topic:undefined,newTopic:false,replyTo:undefined});setSelected([]);setAction(null);store.toast(r.note,"ok");}setBusy(false);});
    },TOPICS.undoDelay);
  };
  const toggle=(id:string)=>{setCounts(had=>({...had,[id]:items.find(t=>t.id===id)?.count||1}));setSelected(had=>had.includes(id)?had.filter(x=>x!==id):[...had,id]);};
  const asked = useRef(0);
  const search = useRef<HTMLInputElement>(null);
  const portal = usePortal();
  const modal = useModal(open);

  // Counts per filter, from the overview: it lists active and done topics and counts archived ones.
  const shown = flat?.topics || (conv ? ((store.get().views[conv] as import("../api").T.DMThread)?.topics||[]) : overview?.threads || []).filter((t) => (conv?t.conv===conv:t.peer===peer) && !t.notice_only).map(topicOf);
  const count = { active: shown.filter((t) => t.state === "active").length, done: shown.filter((t) => t.state === "done").length,
    archived:flat||conv?shown.filter(t=>t.state==="archived").length:(overview?.topics || []).find((c) => c.peer === peer)?.archived || 0 };

  useEffect(() => { const x = setTimeout(() => setQ(query.trim()), TOPICS.searchDelay); return () => clearTimeout(x); }, [query]);

  // The first page again when the filter or the search changes; when
  // anything else here changes, the same rows again (as many as were shown).
  const shownFor = useRef("");
  useEffect(() => {
    if (!open || flat) return;
    const ask = ++asked.current, key = filter + "\n" + q, same = shownFor.current === key;
    shownFor.current = key;
    if (!same) { setItems([]); setNext(""); } // never another filter's rows under this one
    setLoading(true);
    store.api.topics({ conv,peer, state: filter, q, limit: same ? Math.min(Math.max(items.length, TOPICS.pageSize), TOPICS.pageMax) : TOPICS.pageSize }).then((p) => {
      if (ask !== asked.current) return;
      setItems((p.topics || []).map(topicOf)); setNext(p.next || ""); setMatched(p.matched); setFailed("");
    }, (e) => { if (ask === asked.current) setFailed(errorText(e)); }).finally(() => { if (ask === asked.current) setLoading(false); });
  }, [open, conv,peer, filter, q, seq,!!flat]);
  // Emptied once it has slid away, not while it does (it would flash "No topics").
  const reset = () => { setItems([]); setNext(""); setQuery(""); setQ(""); shownFor.current = ""; };

  const more = () => {
    const ask = ++asked.current;
    setLoading(true);
    store.api.topics({ conv,peer, state: filter, q, before: next, limit: TOPICS.pageSize }).then((p) => {
      if (ask !== asked.current) return;
      setItems((had) => [...had, ...(p.topics || []).map(topicOf).filter((t) => !had.some((h) => h.id === t.id))]); setNext(p.next || ""); setMatched(p.matched);
    }, (e) => { if (ask === asked.current) setFailed(errorText(e)); }).finally(() => { if (ask === asked.current) setLoading(false); });
  };
  const go = (t: Topic) => { onOpenChange(false); if(flat)flat.choose(t.id);else if(conv)store.setDraft(conv,{...store.draft(conv),topic:t.id,newTopic:false,replyTo:undefined});else void store.open({ kind: "thread", id: t.id, peer: t.peer }); };
  const f = filters.find((x) => x.id === filter)!;

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange} onOpenChangeComplete={(o) => { if (!o && !open) reset(); }} modal="trap-focus">
      <Dialog.Portal container={portal}>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-[#1B1530]/30 transition-opacity duration-200 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-none" />
        {/* Focus starts in the search box, except on touch, where that would cover the list with the keyboard. */}
        <Dialog.Popup {...modal} initialFocus={(how) => (how === "touch" ? true : search.current)} className={"fixed z-50 flex flex-col bg-canvas text-ink outline-none transition-transform duration-[280ms] ease-out-soft motion-reduce:transition-none "
          + (wide ? "inset-y-0 right-0 w-[440px] border-l-[1.5px] border-outline shadow-pop data-[starting-style]:translate-x-full data-[ending-style]:translate-x-full"
            : "inset-0 data-[starting-style]:translate-y-full data-[ending-style]:translate-y-full")}>
          <div className={"flex shrink-0 items-center gap-2 border-b-[1.5px] border-outline bg-surface " + (wide ? "h-[76px] px-5" : "h-16 pl-1 pr-3")}>
            {!wide && <Dialog.Close aria-label="Back to the conversation" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken"><IconChevronLeft size={26} stroke={2.2} /></Dialog.Close>}
            <div className="min-w-0 flex-1">
              <Dialog.Title className="truncate font-display text-[22px] font-extrabold leading-tight">All topics</Dialog.Title>
              <Dialog.Description className="truncate text-[13px] text-text-2">With {agent} · each topic keeps its participants and history</Dialog.Description>
            </div>
            {wide && <Dialog.Close aria-label="Close" className="grid size-11 shrink-0 place-items-center rounded-full stroke bg-surface hover:bg-sunken"><IconX size={20} /></Dialog.Close>}
          </div>

          <div className="shrink-0 px-4 pt-3 lg:px-5">
            <label className="flex h-11 items-center gap-2 rounded-xl bg-surface pl-3 stroke has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-agent-ink">
              <IconSearch size={18} stroke={2.2} aria-hidden="true" className="shrink-0" />
              <span className="sr-only">Search topics</span>
              <input ref={search} type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search names and last messages" autoComplete="off" enterKeyHint="search"
                className="min-w-0 flex-1 bg-transparent text-[16px] outline-none placeholder:text-muted focus-visible:outline-none lg:text-[15px] [&::-webkit-search-cancel-button]:hidden" />
              {query && <IconButton label="Clear search" onClick={() => setQuery("")} className="shrink-0"><IconX size={18} /></IconButton>}
            </label>
            <div role="group" aria-label="Show" className="mt-3 flex gap-2">
              {filters.map((x) => (
                <button key={x.id} type="button" aria-pressed={filter === x.id} onClick={() => setFilter(x.id)}
                  className={"flex h-11 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-full px-2 text-[14px] font-extrabold stroke transition-colors duration-200 ease-out-soft "
                    + (filter === x.id ? "bg-ink text-canvas" : "bg-surface text-ink hover:bg-sunken")}>
                  <span className="truncate">{x.label}</span><span className={"tnum " + (filter === x.id ? "opacity-80" : "text-muted")}>{count[x.id]}</span>
                </button>
              ))}
            </div>
          </div>

          {selected.length>0 && <div className="shrink-0 border-b border-hairline px-4 py-2" aria-live="polite">
            <p className="text-[14px] font-semibold">{selected.length} selected</p>
            {!action ? <div className="mt-2 flex flex-wrap gap-2">{(["delete","done","archive"] as const).map(what=><Button key={what} size="sm" disabled={busy} onClick={()=>setAction(what)}>{what==="delete"?"Delete for me":what==="done"?"Mark done":"Archive"}</Button>)}<Button size="sm" onClick={()=>setSelected([])}>Cancel selection</Button></div>
              : <><p className="mt-1 text-[14px]">{action==="delete"?"Delete these topics for you? Others keep their copies. People chats: your devices; agent chats: this device.":action==="done"?conv?"Mark these topics done for everyone?":"Mark these topics done on this device?":"Archive these topics on this device? Nothing deleted."}</p>
                <div className="mt-2 flex gap-2">{undo?<><span className="text-[14px]">Will apply in six seconds.</span><Button size="sm" onClick={cancel}>Undo</Button></>:<><Button size="sm" disabled={busy} onClick={commit}>Confirm</Button><Button size="sm" disabled={busy} onClick={cancel}>Cancel</Button></>}</div></>}
          </div>}
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-3 pt-2 lg:px-3" aria-busy={loading}>
            <p role="status" className={q && !loading && !failed && items.length ? "px-3 pb-1 text-[13px] font-semibold text-text-2" : "sr-only"}>
              {q && !loading && !failed ? (matchedCount === 1 ? "1 " + f.label.toLowerCase() + " topic matches" : matchedCount + " " + f.label.toLowerCase() + " topics match") : ""}
            </p>
            {failed && (
              <div role="alert" className="mx-2 mt-2 rounded-2xl bg-danger-bg p-3 text-[14px] text-danger stroke">Topics didn’t load: {failed}</div>
            )}
            {!failed && items.length === 0 && !loading && (
              <div className="px-4 py-10 text-center">
                <p className="font-display text-[18px] font-bold">{q ? "Nothing here matches “" + q + "”" : f.none}</p>
                <p className="mt-1 text-[14px] text-text-2">{q ? "Try Done or Archived, or other words." : filter === "archived" ? "A topic is archived after a quiet week with nothing waiting." : ""}</p>
              </div>
            )}
            {items.length > 0 && (
              <ul aria-label={f.label + " topics"} className="flex flex-col gap-0.5">
                {items.map((t) => (
                  <li key={t.id} className="flex items-center">
                    <input type="checkbox" aria-label={"Select "+(t.title||"Untitled topic")} checked={selected.includes(t.id)} disabled={undo||busy||!!flat&&!flat.canSelect(t.id)} onChange={()=>toggle(t.id)} className="m-3 size-5 shrink-0"/>
                    <button type="button" onClick={() => go(t)} aria-current={t.id === current ? "true" : undefined} aria-label={topicLabel(t) + ", " + when(t.lastAt)}
                      className={"flex min-w-0 flex-1 flex-col gap-1 rounded-2xl border px-3 py-2.5 text-left transition-colors duration-200 "
                        + (t.id === current ? "border-outline bg-surface shadow-pop-sm" : "border-transparent hover:bg-sunken")}>
                      <span className="flex min-w-0 items-center gap-2">
                        <span className={"min-w-0 flex-1 truncate text-[15.5px] " + (t.unread ? "font-extrabold" : "font-bold")}>{firstLine(t.title, TOPICS.listTitle) || "Untitled"}</span>
                        <time dateTime={t.lastAt} className="shrink-0 text-[12px] font-semibold text-muted tnum">{when(t.lastAt)}</time>
                      </span>
                      <span className="flex min-w-0 items-center gap-2">
                        <TopicMark t={t} />{flat&&<span className="shrink-0 text-[12px] text-text-2">{flat.detail(t.id)}</span>}
                        <span className="min-w-0 flex-1 truncate text-[13.5px] text-text-2">
                          {t.conclusion ? <><b className="font-bold text-agent-ink">{t.doneBy === "you" ? "You:" : t.concludedBy && t.concludedBy === overview?.me.address ? "Your agent:" : "Agent:"}</b> {t.conclusion}</> : t.last}
                        </span>
                        {t.unread > 0 && <span aria-hidden="true" className="grid h-5 min-w-5 shrink-0 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{t.unread}</span>}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {next && (
              <div className="flex justify-center pt-2">
                <Button size="sm" variant="outline" disabled={loading} onClick={more}>Show more ({matched - items.length})</Button>
              </div>
            )}
            {loading && items.length === 0 && <p className="px-4 py-6 text-center text-[14px] text-text-2" role="status">Loading topics…</p>}
          </div>
          <p className="shrink-0 border-t border-hairline px-5 py-2.5 pb-[max(10px,env(safe-area-inset-bottom))] text-[12.5px] text-muted">
            {conv?"Names sync across your linked devices. Done and Reopen are shared with everyone. Archive stays on this device. Delete for me leaves others’ copies.":"Names sync across your linked devices. Done and Archive stay on this device. Delete for me leaves others’ copies."}
          </p>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
