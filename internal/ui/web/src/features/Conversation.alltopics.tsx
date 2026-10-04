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
import { useStore } from "../store";
import { Button, IconButton } from "../ui/Button";
import { TopicMark, topicLabel } from "./Conversation.topics";

const filters: { id: TopicState; label: string; none: string }[] = [
  { id: "active", label: "Active", none: "No active topics" },
  { id: "done", label: "Done", none: "No done topics" },
  { id: "archived", label: "Archived", none: "Nothing archived yet" },
];

export function AllTopics({ open, onOpenChange, peer, agent, current }: {
  open: boolean; onOpenChange: (o: boolean) => void; peer: string; agent: string; current?: string;
}) {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const seq = overview?.seq;
  const [filter, setFilter] = useState<TopicState>("active");
  const [query, setQuery] = useState("");
  const [q, setQ] = useState(""); // the query after the person stops typing
  const [items, setItems] = useState<Topic[]>([]);
  const [next, setNext] = useState("");
  const [matched, setMatched] = useState(0);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState("");
  const asked = useRef(0);

  // Counts per filter, from the overview: it lists active and done topics and counts archived ones.
  const shown = (overview?.threads || []).filter((t) => t.peer === peer && !t.notice_only).map(topicOf);
  const count = { active: shown.filter((t) => t.state === "active").length, done: shown.filter((t) => t.state === "done").length,
    archived: (overview?.topics || []).find((c) => c.peer === peer)?.archived || 0 };

  useEffect(() => { const x = setTimeout(() => setQ(query.trim()), TOPICS.searchDelay); return () => clearTimeout(x); }, [query]);

  // The first page again when the filter or the search changes; when
  // anything else here changes, the same rows again (as many as were shown).
  const shownFor = useRef("");
  useEffect(() => {
    if (!open) return;
    const ask = ++asked.current, key = filter + "\n" + q, same = shownFor.current === key;
    shownFor.current = key;
    if (!same) { setItems([]); setNext(""); } // never another filter's rows under this one
    setLoading(true);
    store.api.topics({ peer, state: filter, q, limit: same ? Math.min(Math.max(items.length, TOPICS.pageSize), 200) : TOPICS.pageSize }).then((p) => {
      if (ask !== asked.current) return;
      setItems((p.topics || []).map(topicOf)); setNext(p.next || ""); setMatched(p.matched); setFailed("");
    }, (e) => { if (ask === asked.current) setFailed(errorText(e)); }).finally(() => { if (ask === asked.current) setLoading(false); });
  }, [open, peer, filter, q, seq]);
  useEffect(() => { if (!open) { setItems([]); setNext(""); setQuery(""); setQ(""); shownFor.current = ""; } }, [open]);

  const more = () => {
    const ask = ++asked.current;
    setLoading(true);
    store.api.topics({ peer, state: filter, q, before: next, limit: TOPICS.pageSize }).then((p) => {
      if (ask !== asked.current) return;
      setItems((had) => [...had, ...(p.topics || []).map(topicOf).filter((t) => !had.some((h) => h.id === t.id))]); setNext(p.next || ""); setMatched(p.matched);
    }, (e) => { if (ask === asked.current) setFailed(errorText(e)); }).finally(() => { if (ask === asked.current) setLoading(false); });
  };
  const go = (t: Topic) => { onOpenChange(false); void store.open({ kind: "thread", id: t.id }); };
  const f = filters.find((x) => x.id === filter)!;

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-[#1B1530]/30 transition-opacity duration-200 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-none" />
        <Dialog.Popup className={"fixed z-50 flex flex-col bg-canvas text-ink outline-none transition-transform duration-[280ms] ease-out-soft motion-reduce:transition-none "
          + (wide ? "inset-y-0 right-0 w-[440px] border-l-[1.5px] border-outline shadow-pop data-[starting-style]:translate-x-full data-[ending-style]:translate-x-full"
            : "inset-0 data-[starting-style]:translate-y-full data-[ending-style]:translate-y-full")}>
          <div className={"flex shrink-0 items-center gap-2 border-b-[1.5px] border-outline bg-surface " + (wide ? "h-[76px] px-5" : "h-16 pl-1 pr-3")}>
            {!wide && <Dialog.Close aria-label="Back to the conversation" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken"><IconChevronLeft size={26} stroke={2.2} /></Dialog.Close>}
            <div className="min-w-0 flex-1">
              <Dialog.Title className="truncate font-display text-[22px] font-extrabold leading-tight">All topics</Dialog.Title>
              <Dialog.Description className="truncate text-[13px] text-text-2">With {agent} · each topic is a separate conversation</Dialog.Description>
            </div>
            {wide && <Dialog.Close aria-label="Close" className="grid size-11 shrink-0 place-items-center rounded-full stroke bg-surface hover:bg-sunken"><IconX size={20} /></Dialog.Close>}
          </div>

          <div className="shrink-0 px-4 pt-3 lg:px-5">
            <label className="flex h-11 items-center gap-2 rounded-xl bg-surface pl-3 stroke has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-agent-ink">
              <IconSearch size={18} stroke={2.2} aria-hidden="true" className="shrink-0" />
              <span className="sr-only">Search topics</span>
              <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search names and last messages" autoComplete="off" enterKeyHint="search"
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

          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-3 pt-2 lg:px-3" aria-busy={loading}>
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
                  <li key={t.id}>
                    <button type="button" onClick={() => go(t)} aria-current={t.id === current ? "true" : undefined} aria-label={topicLabel(t) + ", " + when(t.lastAt)}
                      className={"flex w-full flex-col gap-1 rounded-2xl border px-3 py-2.5 text-left transition-colors duration-200 "
                        + (t.id === current ? "border-outline bg-surface shadow-pop-sm" : "border-transparent hover:bg-sunken")}>
                      <span className="flex min-w-0 items-center gap-2">
                        <span className={"min-w-0 flex-1 truncate text-[15.5px] " + (t.unread ? "font-extrabold" : "font-bold")}>{firstLine(t.title, TOPICS.listTitle) || "Untitled"}</span>
                        <time dateTime={t.lastAt} className="shrink-0 text-[12px] font-semibold text-muted tnum">{when(t.lastAt)}</time>
                      </span>
                      <span className="flex min-w-0 items-center gap-2">
                        <TopicMark t={t} />
                        <span className="min-w-0 flex-1 truncate text-[13.5px] text-text-2">
                          {t.doneBy === "agent" && t.conclusion ? <><b className="font-bold text-agent-ink">{t.concludedBy && t.concludedBy === overview?.me.address ? "Your agent:" : "Agent:"}</b> {t.conclusion}</> : t.last}
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
            Names you give topics and Done marks are kept on this device only.
          </p>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
