// Topics: an agent's separate conversations, as chips under the header, so
// one agent is one chat in the list and its threads never clutter it.
import { IconPlus } from "@tabler/icons-react";
import { useApp } from "../context";
import { chatList, when } from "../model";
import { useStore } from "../store";

export function TopicBar({ conv }: { conv: string }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const names = useStore(store, (s) => s.agentNames);
  const draft = useStore(store, (s) => s.drafts[conv]);
  const item = chatList(overview, names).find((i) => i.topics?.some((t) => t.id === conv));
  const topics = item?.topics || [];
  const fresh = !!draft?.newTopic;
  const chip = "inline-flex h-11 max-w-[16rem] shrink-0 items-center gap-2 rounded-full px-3.5 text-[14px] font-semibold ";
  return (
    <nav aria-label={"Topics with " + (item?.title || "this agent")} className="flex shrink-0 items-center gap-2 overflow-x-auto border-b border-hairline bg-canvas px-3 py-1.5 lg:px-5">
      {topics.map((t) => {
        const current = t.id === conv && !fresh;
        return (
          <button key={t.id} type="button" aria-current={current ? "true" : undefined} title={t.title + " · " + when(t.lastAt)}
            onClick={() => { if (fresh) store.setDraft(conv, { ...store.draft(conv), newTopic: false }); if (t.id !== conv) void store.open({ kind: "thread", id: t.id }); }}
            className={chip + (current ? "bg-ink text-canvas" : "bg-surface stroke text-ink hover:bg-sunken")}>
            <span className="truncate">{t.title || "Untitled"}</span>
            {t.unread > 0 && <span className="grid h-5 min-w-5 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{t.unread}</span>}
          </button>
        );
      })}
      <button type="button" aria-pressed={fresh} onClick={() => store.setDraft(conv, { ...store.draft(conv), newTopic: !fresh, replyTo: undefined })}
        className={chip + (fresh ? "bg-act text-act-ink stroke" : "text-agent-ink hover:bg-sunken")}>
        <IconPlus size={18} stroke={2.2} />New topic
      </button>
    </nav>
  );
}
