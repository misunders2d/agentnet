// Topics in the chat list's search: an agent's topics by name or last line,
// archived ones too (GET /api/topics), so an older conversation is found
// without opening the agent first.
import { useEffect, useState } from "react";
import { errorText } from "../api";
import { useAgentNames, useApp } from "../context";
import { TOPICS, chatList, deviceKind, firstLine, topicOf, when, type Topic } from "../model";
import { useStore } from "../store";
import { AgentAvatar } from "../ui/Avatar";
import { TopicMark, topicLabel } from "./Conversation.topics";

export function TopicResults({ query, onCount }: { query: string; onCount: (n: number) => void }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const [hits, setHits] = useState<{ q: string; topics: Topic[]; matched: number } | null>(null);
  const [failed, setFailed] = useState("");
  const long = query.length >= TOPICS.searchMin;

  useEffect(() => {
    if (!long) { setHits(null); onCount(0); return; }
    let alive = true;
    const x = setTimeout(() => {
      store.api.topics({ q: query, limit: TOPICS.chatSearchMax }).then((p) => {
        if (!alive) return;
        const topics = (p.topics || []).map(topicOf);
        setHits({ q: query, topics, matched: p.matched }); setFailed(""); onCount(topics.length);
      }, (e) => { if (alive) { setFailed(errorText(e)); onCount(0); } });
    }, TOPICS.searchDelay);
    return () => { alive = false; clearTimeout(x); };
  }, [query, overview?.seq]);

  if (!long || (!hits?.topics.length && !failed)) return null;
  const agents = new Map(chatList(overview, names).filter((i) => i.kind === "agent").map((i) => [i.peer || "", i.title]));
  return (
    <div className="px-2 pt-4 lg:px-1">
      <h2 className="px-4 pb-1 text-[12px] font-extrabold uppercase tracking-[.08em] text-muted lg:px-3">
        Topics{hits && hits.matched > hits.topics.length ? " · " + hits.topics.length + " of " + hits.matched : ""}
      </h2>
      {failed && <p role="alert" className="px-4 text-[14px] text-danger lg:px-3">Topics couldn’t be searched: {failed}</p>}
      <ul aria-label="Matching topics" className="px-2">
        {(hits?.topics || []).map((t) => (
          <li key={t.peer + "/" + t.id}>
            <button type="button" onClick={() => void store.open({ kind: "thread", id: t.id })} aria-label={topicLabel(t) + ", with " + (agents.get(t.peer) || t.peer) + ", " + when(t.lastAt)}
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
    </div>
  );
}
