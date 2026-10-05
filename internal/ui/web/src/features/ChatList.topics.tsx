// Topics in the chat list's search: an agent's topics by name or last line,
// archived ones too (GET /api/topics), so an older conversation is found
// without opening the agent first. The first TOPICS.chatSearchMax show at
// once; "Show more" pages through the rest.
import { useEffect, useState } from "react";
import { errorText } from "../api";
import { useAgentNames, useApp } from "../context";
import { TOPICS, chatList, deviceKind, firstLine, topicOf, when, type Topic } from "../model";
import { useStore } from "../store";
import { AgentAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { TopicMark, topicLabel } from "./Conversation.topics";

export function TopicResults({ query, onCount }: { query: string; onCount: (n: number) => void }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const [hits, setHits] = useState<{ q: string; topics: Topic[]; matched: number; next: string } | null>(null);
  const [failed, setFailed] = useState("");
  const [more, setMore] = useState(false); // a further page is being read
  const long = query.length >= TOPICS.searchMin;

  useEffect(() => {
    if (!long) { setHits(null); onCount(0); return; }
    let alive = true;
    const x = setTimeout(() => {
      // When anything changes, as many as were shown (one request).
      const shown = hits && hits.q === query ? hits.topics.length : 0;
      store.api.topics({ q: query, limit: Math.min(Math.max(shown, TOPICS.chatSearchMax), TOPICS.pageMax) }).then((p) => {
        if (!alive) return;
        const topics = (p.topics || []).map(topicOf);
        setHits({ q: query, topics, matched: p.matched, next: p.next || "" }); setFailed(""); onCount(topics.length);
      }, (e) => { if (alive) { setFailed(errorText(e)); onCount(0); } });
    }, TOPICS.searchDelay);
    return () => { alive = false; clearTimeout(x); };
  }, [query, overview?.seq]);

  const showMore = () => {
    if (!hits?.next) return;
    const was = hits;
    setMore(true);
    store.api.topics({ q: was.q, before: was.next, limit: TOPICS.pageSize }).then((p) => {
      setHits((h) => {
        if (!h || h.q !== was.q) return h; // the search changed meanwhile
        const topics = [...h.topics, ...(p.topics || []).map(topicOf).filter((t) => !h.topics.some((x) => x.id === t.id && x.peer === t.peer))];
        onCount(topics.length);
        return { ...h, topics, matched: p.matched, next: p.next || "" };
      });
    }, (e) => setFailed(errorText(e))).finally(() => setMore(false));
  };

  if (!long || (!hits?.topics.length && !failed)) return null;
  const agents = new Map(chatList(overview, names).filter((i) => i.open.kind === "thread").map((i) => [i.peer || "", i.title]));
  return (
    <div className="px-2 pt-4 lg:px-1">
      <h2 className="px-4 pb-1 text-[12px] font-extrabold uppercase tracking-[.08em] text-muted lg:px-3">
        Topics{hits && hits.matched > hits.topics.length ? " · " + hits.topics.length + " of " + hits.matched : ""}
      </h2>
      {failed && <p role="alert" className="px-4 text-[14px] text-danger lg:px-3">Topics couldn’t be searched: {failed}</p>}
      <ul aria-label="Matching topics" className="px-2">
        {(hits?.topics || []).map((t) => (
          <li key={t.peer + "/" + t.id}>
            <button type="button" onClick={() => void store.open({ kind: "thread", id: t.id, peer: t.peer })} aria-label={topicLabel(t) + ", with " + (agents.get(t.peer) || t.peer) + ", " + when(t.lastAt)}
              className="flex min-h-11 w-full items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken">
              <AgentAvatar seed={t.peer} size={36} device={deviceKind(t.peer)} />
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-[15px] font-bold">{firstLine(t.title, TOPICS.listTitle) || "Untitled"}</span>
                  <time dateTime={t.lastAt} className="shrink-0 text-[12px] font-semibold text-muted tnum">{when(t.lastAt)}</time>
                </span>
                <span className="mt-0.5 flex items-center gap-2">
                  <TopicMark t={t} />
                  <span className="min-w-0 truncate text-[13px] text-text-2">{agents.get(t.peer) || t.peer}</span>
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      {hits?.next && (
        <div className="flex justify-center px-4 pt-1 lg:px-3">
          <Button size="sm" variant="outline" disabled={more} onClick={showMore}>Show more topics ({hits.matched - hits.topics.length})</Button>
        </div>
      )}
    </div>
  );
}
