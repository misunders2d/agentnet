// Topics: an agent's separate conversations (docs/plans/TOPICS.md). Each is
// its own reply chain, so its own session on the agent's side. The bar under
// the header shows at most TOPICS.barMax chips: the topics that need you,
// then unread ones, the open one and the most recent active ones, as many as
// fit, and last "All topics (N)", which lists every topic. The open topic's
// chip is its menu: rename, mark done, reopen. A done or archived topic says
// so at the end of its messages.
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Menu } from "@base-ui/react/menu";
import {
  IconAlertCircle, IconArchive, IconChevronDown, IconCircleCheck, IconClock, IconListSearch, IconPencil, IconPlus, IconRotateClockwise,
} from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp, useWide } from "../context";
import { TOPICS, chatList, dayLabel, firstLine, newestFirst, timeOf, topicMark, topicOf, type Topic } from "../model";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { AllTopics } from "./Conversation.alltopics";
import { threadAgentName, type Ctx } from "./Message.model";

/** barTopics picks the bar's chips: what needs you, then unread, then the
 *  open topic, then the most recent active ones; the open topic always gets
 *  a chip when there is room for one. */
export function barTopics(topics: Topic[], open: Topic | null, room: number): Topic[] {
  const pick: Topic[] = [];
  const add = (t: Topic) => { if (pick.length < room && !pick.some((p) => p.id === t.id)) pick.push(t); };
  const newest = [...topics].sort(newestFirst);
  newest.filter((t) => t.needsYou > 0).forEach(add);
  newest.filter((t) => t.unread > 0).forEach(add);
  if (open) add(open);
  newest.filter((t) => t.state === "active").forEach(add);
  if (open && room > 0 && !pick.some((p) => p.id === open.id)) pick[pick.length - 1] = open;
  return pick;
}

const markIcon = { needs: IconAlertCircle, archived: IconArchive, done: IconCircleCheck, waiting: IconClock } as const;
const markTone = { needs: "act", archived: "muted", done: "ok", waiting: "agent" } as const;

/** TopicMark shows a topic's state in words (compact: an icon, with the words for screen readers and on hover). */
export function TopicMark({ t, compact, onInk }: { t: Topic; compact?: boolean; onInk?: boolean }) {
  const m = topicMark(t);
  if (!m) return null;
  const Icon = markIcon[m.key];
  if (compact) {
    return (
      <span title={m.word} className={"inline-grid size-5 shrink-0 place-items-center rounded-full " + (m.key === "needs" ? "bg-act text-act-ink" : onInk ? "" : m.key === "done" ? "text-ok-ink" : m.key === "waiting" ? "text-agent-ink" : "text-muted")}>
        <Icon size={16} stroke={2.4} aria-hidden="true" /><span className="sr-only">{m.word}</span>
      </span>
    );
  }
  return <Tag tone={markTone[m.key]} className="shrink-0"><Icon size={12} stroke={2.6} aria-hidden="true" />{m.word}</Tag>;
}

/** topicLabel: what a screen reader hears for a topic chip or row. */
export const topicLabel = (t: Topic) =>
  [t.title || "Untitled topic", topicMark(t)?.word, t.unread ? t.unread + " unread" : ""].filter(Boolean).join(", ");

export function TopicBar({ thread }: { thread: T.Thread }) {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const draft = useStore(store, (s) => s.drafts[thread.id]);
  const [all, setAll] = useState(false);
  const item = chatList(overview, names).find((i) => i.kind === "agent" && i.peer === thread.peer);
  const open = thread.topic ? topicOf(thread.topic) : null;
  const topics = item?.topics || [];
  const total = item?.topicTotal ?? topics.length;
  const listed = !!overview?.topic_list;
  const fresh = !!draft?.newTopic;
  const agent = item?.title || "this agent";

  // As many chips as fit: the rest are under All topics, never in a scroll.
  const nav = useRef<HTMLElement>(null), allRef = useRef<HTMLButtonElement>(null), newRef = useRef<HTMLButtonElement>(null);
  const [room, setRoom] = useState<number>(TOPICS.barMax - 1);
  useLayoutEffect(() => {
    const el = nav.current;
    if (!el) return;
    const fit = () => {
      const cs = getComputedStyle(el), gap = parseFloat(cs.columnGap) || 8;
      const free = el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight) - (allRef.current?.offsetWidth || 0) - (newRef.current?.offsetWidth || 0) - 2 * gap;
      setRoom(Math.max(1, Math.min(TOPICS.barMax - (listed ? 1 : 0), Math.floor((free + gap) / (TOPICS.chipMinWidth + gap)))));
    };
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    return () => ro.disconnect();
  }, [total, listed, wide]);

  const pick = barTopics(topics, open, room);
  const hiddenUnread = topics.filter((t) => !pick.some((p) => p.id === t.id)).reduce((n, t) => n + t.unread, 0)
    + ((overview?.topics || []).find((c) => c.peer === thread.peer)?.archived_unread || 0);
  const leaveNew = () => { if (fresh) store.setDraft(thread.id, { ...store.draft(thread.id), newTopic: false }); };
  const chip = "inline-flex h-11 min-w-0 items-center gap-1.5 rounded-full px-3.5 text-[14px] font-semibold ";

  return (
    <nav ref={nav} aria-label={"Topics with " + agent} className="flex shrink-0 items-center gap-2 overflow-hidden border-b border-hairline bg-canvas px-3 py-1.5 lg:px-5">
      {pick.map((t) => {
        const current = !!open && t.id === open.id && !fresh;
        const body = (
          <>
            <TopicMark t={t} compact onInk={!!open && t.id === open.id && !fresh} />
            <span className="min-w-0 truncate">{firstLine(t.title, TOPICS.barTitle) || "Untitled"}</span>
            {t.unread > 0 && <span aria-hidden="true" className="grid h-5 min-w-5 shrink-0 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{t.unread}</span>}
          </>
        );
        const width = { minWidth: TOPICS.chipMinWidth, maxWidth: wide ? "16rem" : undefined };
        if (current && listed) {
          return (
            <TopicMenu key={t.id} topic={t} onAll={() => setAll(true)}
              trigger={<Menu.Trigger aria-current="true" aria-label={topicLabel(t) + ", open topic, menu"} title={t.title}
                style={width} className={chip + "flex-1 basis-0 bg-ink text-canvas data-[popup-open]:ring-2 data-[popup-open]:ring-act"}>
                {body}<IconChevronDown size={16} stroke={2.4} aria-hidden="true" className="shrink-0" />
              </Menu.Trigger>} />
          );
        }
        return (
          <button key={t.id} type="button" aria-current={current ? "true" : undefined} aria-label={topicLabel(t)} title={t.title + " · " + dayLabel(t.lastAt) + " " + timeOf(t.lastAt)}
            onClick={() => { leaveNew(); if (!open || t.id !== open.id) void store.open({ kind: "thread", id: t.id }); }}
            style={width} className={chip + "flex-1 basis-0 " + (current ? "bg-ink text-canvas" : "bg-surface stroke text-ink hover:bg-sunken")}>
            {body}
          </button>
        );
      })}
      <span aria-hidden="true" className="ml-auto" />
      {listed && (
        <button ref={allRef} type="button" aria-haspopup="dialog" onClick={() => setAll(true)}
          aria-label={"All topics (" + total + ")" + (hiddenUnread ? ", " + hiddenUnread + " unread in other topics" : "")}
          className={chip + "shrink-0 bg-surface stroke text-ink hover:bg-sunken"}>
          {wide && <IconListSearch size={18} stroke={2.2} aria-hidden="true" />}
          <span className="tnum">All topics ({total})</span>
          {hiddenUnread > 0 && (wide
            ? <span aria-hidden="true" className="grid h-5 min-w-5 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{hiddenUnread > 99 ? "99+" : hiddenUnread}</span>
            : <span aria-hidden="true" className="size-2.5 shrink-0 rounded-full bg-danger" />)}
        </button>
      )}
      <button ref={newRef} type="button" aria-pressed={fresh} aria-label={wide ? undefined : "New topic"} title="New topic: your next message starts a separate conversation"
        onClick={() => store.setDraft(thread.id, { ...store.draft(thread.id), newTopic: !fresh, replyTo: undefined })}
        className={chip + "shrink-0 " + (wide ? "" : "w-11 justify-center px-0 ") + (fresh ? "bg-act text-act-ink stroke" : "text-agent-ink hover:bg-sunken")}>
        <IconPlus size={18} stroke={2.2} aria-hidden="true" />{wide && "New topic"}
      </button>
      {listed && <AllTopics open={all} onOpenChange={setAll} peer={thread.peer} agent={agent} current={open?.id} />}
    </nav>
  );
}

/** TopicMenu: the open topic's menu (its chip): rename, mark done or reopen, all topics. */
function TopicMenu({ topic, trigger, onAll }: { topic: Topic; trigger: ReactNode; onAll: () => void }) {
  const store = useApp();
  const [rename, setRename] = useState(false);
  const item = "flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-3 text-[15px] font-medium outline-none data-[highlighted]:bg-sunken";
  return (
    <>
      <Menu.Root>
        {trigger}
        <Menu.Portal>
          <Menu.Positioner side="bottom" align="start" sideOffset={6} collisionPadding={12} className="z-50">
            <Menu.Popup className="min-w-60 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
              <Menu.Item className={item} onClick={() => setRename(true)}><IconPencil size={20} aria-hidden="true" />Rename…</Menu.Item>
              {topic.state === "active"
                ? <Menu.Item className={item} onClick={() => void changeTopic(store, "done", topic)}><IconCircleCheck size={20} aria-hidden="true" />Mark done</Menu.Item>
                : <Menu.Item className={item} onClick={() => void changeTopic(store, "reopen", topic)}><IconRotateClockwise size={20} aria-hidden="true" />Reopen</Menu.Item>}
              <Menu.Separator className="mx-2 my-1 h-px bg-hairline" />
              <Menu.Item className={item} onClick={onAll}><IconListSearch size={20} aria-hidden="true" />All topics</Menu.Item>
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
      <RenameTopic open={rename} onOpenChange={setRename} topic={topic} />
    </>
  );
}

/** changeTopic sends one of your changes to a topic and says what it did. */
export function changeTopic(store: ReturnType<typeof useApp>, what: "rename" | "done" | "reopen", t: Topic, title?: string) {
  return store.run((a) => a.changeTopic(what, { peer: t.peer, id: t.id, ...(what === "rename" ? { title: title || "" } : {}) })).then((r) => {
    if (r?.note) store.toast(r.note, "ok");
    return !!r;
  });
}

/** RenameTopic: a name of your own for a topic, kept on this device. */
function RenameTopic({ open, onOpenChange, topic }: { open: boolean; onOpenChange: (o: boolean) => void; topic: Topic }) {
  const store = useApp();
  const [title, setTitle] = useState(topic.title);
  const [busy, setBusy] = useState(false);
  useLayoutEffect(() => { if (open) setTitle(topic.title); }, [open]);
  const save = async (name: string) => {
    setBusy(true);
    const ok = await changeTopic(store, "rename", topic, name);
    setBusy(false);
    if (ok) onOpenChange(false);
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title="Rename topic"
      description="The new name is kept on this device only. Your agent and your other devices still see its first message."
      footer={
        <div className="flex flex-wrap items-center justify-end gap-2">
          {topic.renamed && <Button variant="ghost" disabled={busy} onClick={() => void save("")}>Use its first message</Button>}
          <Button variant="act" type="submit" form="rename-topic" disabled={busy || !title.trim() || title.trim() === topic.title}>Save</Button>
        </div>
      }>
      <form id="rename-topic" onSubmit={(e) => { e.preventDefault(); if (title.trim()) void save(title); }}>
        <label className="block text-[14px] font-semibold text-text-2" htmlFor="rename-topic-name">Name</label>
        <input id="rename-topic-name" value={title} maxLength={TOPICS.titleMax} autoFocus onChange={(e) => setTitle(e.target.value)} autoComplete="off"
          className="mt-1.5 h-12 w-full rounded-xl bg-surface px-3 text-[16px] stroke outline-none focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-agent-ink" />
        {topic.renamed && topic.autoTitle && <p className="mt-2 text-[13px] text-text-2">Its first message: “{topic.autoTitle}”</p>}
      </form>
    </Sheet>
  );
}

/** TopicEnd closes a done or archived topic's messages: what the agent
 *  concluded (its own words, labelled as its), or that you marked it done,
 *  or that it is archived; Reopen makes it active again. */
export function TopicEnd({ ctx }: { ctx: Ctx }) {
  const store = useApp();
  const t = ctx.thread?.topic ? topicOf(ctx.thread.topic) : null;
  if (!t || t.state === "active" || !ctx.overview?.topic_list) return null;
  const agent = t.concludedBy && t.concludedBy === ctx.overview?.me.address ? "Your agent" : threadAgentName(ctx);
  const archived = t.state === "archived";
  return (
    <section aria-label="Topic state" className="mx-3 mt-4 rounded-2xl bg-surface p-4 stroke lg:mx-5">
      <div className="flex flex-wrap items-center gap-2">
        {archived ? <Tag tone="muted"><IconArchive size={12} stroke={2.6} aria-hidden="true" />Archived</Tag> : <Tag tone="ok"><IconCircleCheck size={12} stroke={2.6} aria-hidden="true" />Done</Tag>}
        {t.doneBy === "you" && <span className="text-[13px] text-text-2">You marked it done on this device.</span>}
        {t.doneBy === "agent" && !archived && <span className="text-[13px] text-text-2">{agent} finished it.</span>}
      </div>
      {t.doneBy === "agent" && t.conclusion && (
        <p className="mt-2 text-[15px]"><span className="font-bold">{agent}’s conclusion:</span> <span className="text-text-2">{t.conclusion}</span></p>
      )}
      {archived && (
        <p className="mt-2 text-[14px] text-text-2">
          Quiet since {dayLabel(t.lastAt).toLowerCase() === "today" ? "today" : new Date(t.lastAt).toLocaleDateString([], { month: "short", day: "numeric" })}, with nothing waiting. Nothing was deleted: a new message here makes it active again.
        </p>
      )}
      <div className="mt-3">
        <Button size="sm" variant="outline" icon={<IconRotateClockwise size={18} aria-hidden="true" />} onClick={() => void changeTopic(store, "reopen", t)}>Reopen</Button>
      </div>
    </section>
  );
}
