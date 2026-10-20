import { useMemo, useState } from "react";
import { IconChevronRight, IconFileText, IconPaperclip, IconSearch } from "@tabler/icons-react";
import { useAgentNames, useApp } from "../context";
import type { T } from "../api";
import { firstLine, historicalAgentName, personName, threadRow } from "../model";
import { useStore, type Draft, type Open, type View } from "../store";

function draftDestination(id: string, draft: Draft, view: View | undefined, overview: T.Overview | null, names: Record<string, string>) {
  const dm = view && typeof view.peer !== "string" ? view as T.DMThread : undefined;
  const thread = view && typeof view.peer === "string" ? view as T.Thread : undefined;
  const summary = overview?.dms?.find(d => d.id === id);
  const topic = thread?.topic || overview?.threads?.find(t => t.id === id) || overview?.topics?.find(t => t.latest.id === id)?.latest;
  const isDM = !!dm || !!summary || !thread && !topic && /^[a-f0-9]{64}$/.test(id);
  const peer = thread?.peer || topic?.peer;
  const target: NonNullable<Open> = isDM ? { kind: "dm", id } : { kind: "thread", id, ...(peer ? { peer } : {}) };
  const room = dm || summary;
  const row = peer && topic ? threadRow(peer, overview, names, topic) : undefined;
  const title = room ? room.kind === "group" ? room.title || "Group" : personName(room.peer) : row?.title || "Saved conversation";
  const agent = dm?.agents?.find(a => a.pid === draft.agent);
  const scope = [
    row?.subtitle || (!isDM ? "Original agent selection" : undefined),
    draft.newTopic ? "New topic" : draft.topic ? dm?.topics?.find(t => t.id === draft.topic)?.title || "Saved topic" : isDM ? "Main" : topic?.title,
    draft.agent ? agent?.agent_id ? historicalAgentName(agent.agent_id, names) : "Saved agent selection" : undefined,
    draft.replyTo ? "Reply draft" : undefined,
  ].filter(Boolean).join(" · ");
  return { target, title, scope };
}

/** Drafts use their original conversation key. Opening never chooses another
 * topic, agent or person's main chat, and leaves the stored draft untouched. */
export function DraftsView() {
  const store = useApp();
  const drafts = useStore(store, s => s.drafts);
  const views = useStore(store, s => s.views);
  const dm = useStore(store, s => s.dm);
  const thread = useStore(store, s => s.thread);
  const overview = useStore(store, s => s.overview);
  const names = useAgentNames();
  const [query, setQuery] = useState("");
  const rows = useMemo(() => Object.entries(drafts)
    .filter(([, draft]) => !!draft.text || !!draft.files?.length)
    .map(([id, draft]) => {
      const view = views[id] || (dm?.id === id ? dm : thread?.id === id ? thread : undefined);
      const destination = draftDestination(id, draft, view, overview, names);
      const files = draft.files || [];
      return { id, draft, files, ...destination,
        search: [id, destination.title, destination.scope, draft.text, ...files.map(f => f.name)].join(" ").toLocaleLowerCase() };
    }), [drafts, views, dm, thread, overview, names]);
  const shown = useMemo(() => {
    const find = query.trim().toLocaleLowerCase();
    return find ? rows.filter(row => row.search.includes(find)) : rows;
  }, [rows, query]);

  return (
    <section aria-label="Drafts">
      <p className="mb-4 text-[14px] text-text-2">Continue writing in the original chat.</p>
      {rows.length > 0 && <label className="mb-4 flex min-h-11 items-center gap-2 rounded-2xl stroke bg-surface px-3 focus-within:outline-3 focus-within:outline-agent-ink">
        <IconSearch size={19} className="shrink-0 text-muted" aria-hidden="true" />
        <input type="search" aria-label="Search drafts" placeholder="Search drafts" value={query} onChange={e => setQuery(e.target.value)}
          className="min-w-0 flex-1 bg-transparent py-2 text-[16px] outline-none lg:text-[15px]" />
      </label>}
      {!rows.length ? <div className="rounded-2xl border border-dashed border-outline/40 px-5 py-8 text-center">
        <IconFileText size={28} className="mx-auto mb-2 text-muted" aria-hidden="true" />
        <p className="font-semibold">No drafts</p>
        <p className="mt-1 text-[14px] text-text-2">Messages you start writing appear here.</p>
      </div> : !shown.length ? <p role="status" className="py-6 text-center text-[14px] text-text-2">No drafts match your search.</p> : <ul className="divide-y divide-hairline">
        {shown.map(row => <li key={row.id}>
          <button type="button" onClick={() => {
            store.showTab("chats");
            void store.open(row.target).catch(error => store.toast(error instanceof Error ? error.message : "Could not open this draft's original chat.", "error"));
          }} className="flex w-full min-w-0 items-center gap-3 rounded-xl px-2 py-3 text-left hover:bg-sunken focus-visible:outline-3 focus-visible:outline-agent-ink">
            <span className="min-w-0 flex-1">
              <span className="block truncate font-semibold">{row.title}</span>
              {row.scope && <span className="mt-0.5 block truncate text-[12px] text-text-2">{row.scope}</span>}
              <span className="mt-1 block truncate text-[14px] text-text-2">{firstLine(row.draft.text, 160) || (row.files.length ? "Attachments only" : "Draft text")}</span>
              {!!row.files.length && <span className="mt-1 flex min-w-0 items-center gap-1 text-[12px] text-muted">
                <IconPaperclip size={14} className="shrink-0" aria-hidden="true" />
                <span className="truncate">{row.files.length} {row.files.length === 1 ? "file" : "files"} · {row.files.map(file => file.name).join(", ")}</span>
              </span>}
            </span>
            <IconChevronRight size={20} className="shrink-0 text-muted" aria-hidden="true" />
          </button>
        </li>)}
      </ul>}
    </section>
  );
}
