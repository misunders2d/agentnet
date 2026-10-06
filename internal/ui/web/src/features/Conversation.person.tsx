// A person's chat contains separate signed conversations. Switching here
// keeps each one's audience, messages, topics and draft under its own ID.
import { useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { chatList, firstLine, personName } from "../model";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export function PersonConversations({ dm }: { dm: T.DMThread | null }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const chat = chatList(overview, {}).find((c) => c.conversations?.some((d) => d.id === dm?.id));
  const conversations = chat?.conversations || [];
  if (!dm || conversations.length < 2) return null;
  const current = conversations.find((d) => d.id === dm.id);
  const title = (d: T.DMSummary) => firstLine(d.title, 90) || "Empty conversation";
  const started = (d: T.DMSummary) => new Date(d.created).toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
  const shown = conversations.filter((d) => [d.title, d.last, started(d)].some((s) => s.toLowerCase().includes(query.trim().toLowerCase())));
  return <>
    <div className="flex min-w-0 items-center gap-3 border-b border-hairline bg-surface px-4 py-2">
      <span className="min-w-0 flex-1 truncate text-[13px] text-text-2">{current ? title(current) : "Conversation"}</span>
      <Button size="sm" variant="outline" onClick={() => { setQuery(""); setOpen(true); }}>Conversations ({conversations.length})</Button>
    </div>
    <Sheet open={open} onOpenChange={setOpen} title={"Conversations with " + personName(dm.peer)} description="Separate conversations, together in one chat.">
      <label className="mb-3 block">
        <span className="sr-only">Search conversations</span>
        <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search conversations" className="h-11 w-full rounded-xl bg-surface px-3 text-[16px] stroke" />
      </label>
      <ul aria-label="Conversations" className="flex flex-col gap-2">
        {shown.map((d) => <li key={d.id}>
          <button type="button" aria-current={d.id === dm.id ? "true" : undefined}
            onClick={() => { setOpen(false); void store.open({ kind: "dm", id: d.id }); }}
            className={"w-full min-w-0 rounded-xl p-3 text-left hover:bg-sunken " + (d.id === dm.id ? "bg-sunken stroke" : "bg-surface")}>
            <span className="block truncate font-bold">{title(d)}</span>
            <span className="block truncate text-[13px] text-text-2">Started {started(d)}{d.unread > 0 ? " · " + d.unread + " unread" : ""}</span>
            {!!d.last && <span className="mt-1 block truncate text-[14px] text-text-2">{firstLine(d.last, 120)}</span>}
          </button>
        </li>)}
      </ul>
      {!shown.length && <p className="py-6 text-center text-text-2">No conversations match.</p>}
    </Sheet>
  </>;
}
