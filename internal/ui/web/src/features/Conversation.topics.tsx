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
import { topicChangeKey, useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { focusedIn, usePortal } from "../owned";
import { AllTopics } from "./Conversation.alltopics";
import { OrganizeMessages } from "./Conversation.organize";
import { deviceWords, requestState, shownText, threadAgentName, type Ctx } from "./Message.model";

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

/** steadyBar keeps the chips where they were across changes: barTopics says
 *  which topics belong in the bar, but a chip that may stay keeps its slot
 *  (so the bar never reorders under the pointer), and the topic just left
 *  keeps its chip (switching back is one click). A topic that must show
 *  (the open one, then ones that need you, the one just left, ones with
 *  news) takes the last slot of one that need not. prev: the bar's last chips; left: the topic just left. */
export function steadyBar(prev: string[], topics: Topic[], open: Topic | null, left: string, room: number): Topic[] {
  const ranked = barTopics(topics, open, room);
  if (!prev.length) return ranked;
  const by = new Map(topics.map((t) => [t.id, t]));
  if (open) by.set(open.id, open);
  const may = (id: string) => { const t = by.get(id); return !!t && (id === open?.id || id === left || t.needsYou > 0 || t.unread > 0 || t.state === "active"); };
  const must: string[] = [];
  const was = by.get(left);
  for (const t of [...(open ? [open] : []), ...ranked.filter((t) => t.needsYou > 0), ...(was ? [was] : []), ...ranked.filter((t) => t.unread > 0)]) {
    if (must.length < room && !must.includes(t.id)) must.push(t.id);
  }
  const slots = prev.filter(may);
  // The slot a must-show topic may take: the last one that need not show, sparing the topic just left.
  const free = () => {
    for (const spare of [true, false]) {
      for (let i = slots.length - 1; i >= 0; i--) if (!must.includes(slots[i]) && !(spare && slots[i] === left)) return i;
    }
    return -1;
  };
  while (slots.length > room) { const i = free(); slots.splice(i >= 0 ? i : slots.length - 1, 1); }
  for (const id of must) {
    if (slots.includes(id)) continue;
    if (slots.length < room) slots.push(id);
    else { const i = free(); if (i >= 0) slots[i] = id; }
  }
  for (const t of ranked) if (slots.length < room && !slots.includes(t.id)) slots.push(t.id);
  return slots.map((id) => by.get(id)!);
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

export function TopicBar({ thread,dm }: { thread?: T.Thread;dm?:T.DMThread }) {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const draft = useStore(store, (s) => s.drafts[dm?.id||thread!.id]);
  const [all, setAll] = useState(false);
  const item = chatList(overview, names).find((i) => i.open.kind === "thread" && i.peer === thread?.peer);
  const current=dm?.topics?.find(t=>t.id===draft?.topic);
  const open = dm ? current?topicOf(current):null : thread?.topic ? topicOf(thread.topic) : null;
  const topics = dm ? (dm.topics||[]).filter(t=>t.state!=="archived").map(topicOf) : item?.topics || [];
  const total = dm ? dm.topics?.length||0 : item?.topicTotal ?? topics.length;
  const listed = !!overview?.topic_list;
  const fresh = !!draft?.newTopic;
  const agent = dm ? dm.title||dm.peer.label : item?.title || "this agent";

  // As many chips as fit: the rest are under All topics, never in a scroll.
  const nav = useRef<HTMLElement>(null), allRef = useRef<HTMLButtonElement>(null), newRef = useRef<HTMLButtonElement>(null);
  const [room, setRoom] = useState<number>(TOPICS.barMax - 1);
  useLayoutEffect(() => {
    const el = nav.current;
    if (!el) return;
    const fit = () => {
      const cs = getComputedStyle(el), gap = parseFloat(cs.columnGap) || 8;
      // Every chip brings one gap: the next item is the spacer, which has one gap to
      // All topics (when listed) and one to New topic.
      const free = el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight) - (allRef.current?.offsetWidth || 0) - (newRef.current?.offsetWidth || 0) - (listed ? 2 : 1) * gap - (dm ? 85 : 0);
      setRoom(Math.max(dm?0:1, Math.min(TOPICS.barMax - (listed ? 1 : 0), Math.floor(free / (TOPICS.chipMinWidth + gap)))));
    };
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    if (allRef.current) ro.observe(allRef.current); // its count and unread mark change its width
    return () => ro.disconnect();
  }, [total, listed, wide]);

  // The topic being opened (its messages are on their way): its chip lights up at once.
  const pending = useStore(store, (s) => s.pending);
  const going = pending?.kind === "thread" && topics.some((t) => t.id === pending.id) ? pending.id : "";
  // The bar stays as it is across topics: the chosen chip keeps focus when
  // it becomes the open topic's menu, and is kept in view without moving the row.
  const refocus = useRef("");
  useLayoutEffect(() => {
    const el = nav.current, id = going || open?.id;
    if (!el || !id) return;
    const c = el.querySelector<HTMLElement>("[data-topic=\"" + CSS.escape(id) + "\"]");
    if (!c) return;
    if (refocus.current === id && !going && !c.contains(focusedIn(el))) { c.focus({ preventScroll: true }); refocus.current = ""; }
    const l = c.getBoundingClientRect().left - el.getBoundingClientRect().left + el.scrollLeft, r = l + c.offsetWidth;
    if (l < el.scrollLeft) el.scrollTo({ left: l - 12, behavior: "smooth" });
    else if (r > el.scrollLeft + el.clientWidth) el.scrollTo({ left: r - el.clientWidth + 12, behavior: "smooth" });
  }, [going, open?.id]);

  // The chips stay in their slots across a topic switch (steadyBar).
  const shown = useRef<{ ids: string[]; open: string; left: string }>({ ids: [], open: open?.id || "", left: "" });
  if ((open?.id || "") !== shown.current.open) shown.current = { ...shown.current, open: open?.id || "", left: shown.current.open };
  const pick = steadyBar(shown.current.ids, topics, open, shown.current.left, room);
  shown.current.ids = pick.map((t) => t.id);
  const hidden = topics.filter((t) => !pick.some((p) => p.id === t.id));
  const hiddenUnread = hidden.reduce((n, t) => n + t.unread, 0) + ((overview?.topics || []).find((c) => c.peer === thread?.peer)?.archived_unread || 0);
  const hiddenNeeds = hidden.filter((t) => t.needsYou > 0).length; // never archived: what needs you is pending
  const leaveNew = () => { if (fresh) store.setDraft(dm?.id||thread!.id, { ...store.draft(dm?.id||thread!.id), newTopic: false }); };
  const chip = "inline-flex h-11 min-w-0 items-center gap-1.5 rounded-full px-3.5 text-[14px] font-semibold ";

  return (
    <nav ref={nav} aria-label={"Topics with " + agent} className="flex shrink-0 items-center gap-2 overflow-hidden border-b border-hairline bg-canvas px-3 py-1.5 lg:px-5">
      {dm && <button type="button" aria-pressed={!draft?.topic&&!fresh} className={chip+"shrink-0 "+(!draft?.topic&&!fresh?"bg-ink text-canvas":"bg-surface stroke")} onClick={()=>store.setDraft(dm.id,{...store.draft(dm.id),topic:undefined,newTopic:false,replyTo:undefined},true)}>{wide?"Main flow":"Main"}</button>}
      {pick.map((t) => {
        const current = !!open && t.id === open.id && !fresh;
        const lit = going ? t.id === going : current;
        const body = (
          <>
            <TopicMark t={t} compact onInk={lit} />
            <span className="min-w-0 truncate">{firstLine(t.title, TOPICS.barTitle) || "Untitled"}</span>
            {t.unread > 0 && <span aria-hidden="true" className="grid h-5 min-w-5 shrink-0 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{t.unread}</span>}
          </>
        );
        const width = { minWidth: TOPICS.chipMinWidth, maxWidth: wide ? TOPICS.chipMaxWidth : undefined };
        if (current && listed) {
          return (
            <TopicMenu key={t.id} topic={t} onAll={() => setAll(true)}
              trigger={<Menu.Trigger data-topic={t.id} aria-current="true" aria-label={topicLabel(t) + ", open topic, menu"} title={t.title}
                style={width} className={chip + "flex-1 basis-0 " + (lit ? "bg-ink text-canvas" : "bg-surface stroke text-ink") + " data-[popup-open]:ring-2 data-[popup-open]:ring-act"}>
                {body}<IconChevronDown size={16} stroke={2.4} aria-hidden="true" className="shrink-0" />
              </Menu.Trigger>} />
          );
        }
        return (
          <button key={t.id} data-topic={t.id} type="button" aria-current={current ? "true" : undefined} aria-label={topicLabel(t)} title={t.title + " · " + dayLabel(t.lastAt) + " " + timeOf(t.lastAt)}
            onClick={() => { leaveNew(); refocus.current = t.id; if (!open || t.id !== open.id) dm?store.setDraft(dm.id,{...store.draft(dm.id),topic:t.id,newTopic:false,replyTo:undefined}):void store.open({ kind: "thread", id: t.id, peer: thread!.peer }); }}
            style={width} className={chip + "flex-1 basis-0 " + (lit ? "bg-ink text-canvas" : "bg-surface stroke text-ink hover:bg-sunken")}>
            {body}
          </button>
        );
      })}
      <span aria-hidden="true" className="ml-auto" />
      {listed && (
        <button ref={allRef} type="button" aria-haspopup="dialog" onClick={() => setAll(true)} title={"All topics (" + total + ")"}
          aria-label={"All topics (" + total + ")" + (hiddenNeeds ? ", " + hiddenNeeds + (hiddenNeeds === 1 ? " needs you" : " need you") : "")
            + (hiddenUnread ? ", " + hiddenUnread + " unread in other topics" : "")}
          className={chip + "shrink-0 bg-surface stroke text-ink hover:bg-sunken " + (wide ? "" : "gap-1 px-3")}>
          {hiddenNeeds > 0
            ? <span aria-hidden="true" className="grid h-5 min-w-5 shrink-0 place-items-center rounded-full bg-act px-1 text-[11px] font-bold text-act-ink tnum">
                {wide ? (hiddenNeeds > 99 ? "99+" : hiddenNeeds) : <IconAlertCircle size={14} stroke={2.6} />}
              </span>
            : <IconListSearch size={18} stroke={2.2} aria-hidden="true" className="shrink-0" />}
          {/* A phone keeps the words short: its one topic chip keeps room for its name. */}
          <span className="tnum">{wide ? "All topics (" + total + ")" : "All " + total}</span>
          {hiddenUnread > 0 && (wide
            ? <span aria-hidden="true" className="grid h-5 min-w-5 place-items-center rounded-full bg-danger px-1 text-[11px] font-bold text-white tnum">{hiddenUnread > 99 ? "99+" : hiddenUnread}</span>
            : <span aria-hidden="true" className="size-2.5 shrink-0 rounded-full bg-danger" />)}
        </button>
      )}
      <button ref={newRef} type="button" aria-pressed={fresh} aria-label={wide ? undefined : "New topic"} title="New topic: your next message starts a separate conversation"
        onClick={() => store.setDraft(dm?.id||thread!.id, { ...store.draft(dm?.id||thread!.id), newTopic: !fresh, replyTo: undefined })}
        className={chip + "shrink-0 " + (wide ? "" : "w-11 justify-center px-0 ") + (fresh ? "bg-act text-act-ink stroke" : "text-agent-ink hover:bg-sunken")}>
        <IconPlus size={18} stroke={2.2} aria-hidden="true" />{wide && "New topic"}
      </button>
      {listed && <AllTopics open={all} onOpenChange={setAll} peer={thread?.peer||""} conv={dm?.id} agent={agent} current={open?.id} />}
    </nav>
  );
}

/** TopicMenu: the open topic's menu (its chip): rename, mark done or reopen, all topics. */
export function TopicMenu({ topic, trigger, onAll }: { topic: Topic; trigger: ReactNode; onAll: () => void }) {
  const store = useApp();
  const portal = usePortal();
  const busy = useStore(store, s => s.topicBusy[topicChangeKey(topic)]);
  const [rename, setRename] = useState(false);
  const [pending, setPending] = useState(false);
  const [merge, setMerge] = useState<string[] | null>(null);
  const dm = useStore(store, s => topic.conv ? s.views[topic.conv] as T.DMThread | undefined : undefined);
  const item = "flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-3 text-[15px] font-medium outline-none data-[highlighted]:bg-sunken";
  return (
    <>
      <Menu.Root modal={false}>
        {trigger}
        <Menu.Portal container={portal}>
          <Menu.Positioner side="bottom" align="start" sideOffset={6} collisionPadding={12} className="z-50">
            <Menu.Popup className="min-w-60 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
              {!!topic.pendingIDs?.length && <Menu.Item className={item} onClick={() => setPending(true)}><IconClock size={20} aria-hidden="true" />Show pending requests ({topic.pendingIDs.length})</Menu.Item>}
              <Menu.Item disabled={!!busy} className={item} onClick={() => setRename(true)}><IconPencil size={20} aria-hidden="true" />Rename…</Menu.Item>
              {dm && !dm.frozen && (!dm.role || dm.role === "member") && <Menu.Item className={item} onClick={() => setMerge((dm.messages || []).filter(m => m.topic === topic.id && !m.topic_event && !m.event && !m.deleted && !m.excerpt_pid).map(m => m.id))}>Merge into another topic…</Menu.Item>}
              {topic.state === "active"
                ? <Menu.Item disabled={!!busy} className={item} onClick={() => void changeTopic(store, topic.root ? "archive" : "done", topic)}><IconCircleCheck size={20} aria-hidden="true" />{topic.root ? "Archive" : "Mark done"}</Menu.Item>
                : <Menu.Item disabled={!!busy} className={item} onClick={() => void changeTopic(store, "reopen", topic)}><IconRotateClockwise size={20} aria-hidden="true" />Reopen</Menu.Item>}
              <Menu.Separator className="mx-2 my-1 h-px bg-hairline" />
              <Menu.Item className={item} onClick={onAll}><IconListSearch size={20} aria-hidden="true" />All topics</Menu.Item>
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
      <RenameTopic open={rename} onOpenChange={setRename} topic={topic} />
      <PendingTopic open={pending} onOpenChange={setPending} topic={topic} />
      {merge && dm && <OrganizeMessages dm={dm} ids={merge} merge={topic.id} onClose={() => setMerge(null)} onDone={() => setMerge(null)} />}
    </>
  );
}

/** Navigation to the exact contributing requests, with their existing actions. */
function PendingTopic({ open, onOpenChange, topic }: { open: boolean; onOpenChange: (open: boolean) => void; topic: Topic }) {
  const store = useApp();
  const view = useStore(store, s => s.views[topic.conv || topic.id]);
  const overview = useStore(store, s => s.overview);
  return <Sheet open={open} onOpenChange={onOpenChange} title="Pending requests"
    description="These items keep this topic open. Open one to see its current status and available actions.">
    <div className="grid gap-2">
      {(topic.pendingIDs || []).map((id, i) => {
        const m = view?.messages?.find(m => m.id === id);
        return <Button key={id} variant="ghost" className="h-auto min-h-12 justify-start whitespace-normal text-left" onClick={() => {
          onOpenChange(false);
          void store.openMessage(id, topic.conv ? { conv: topic.conv } : undefined);
        }}>
          <span><span className="block text-[12px] text-text-2">Open pending {m?.kind === "task" ? "task" : m?.kind === "question" ? "question" : "item"} {i + 1}</span>
            <span>{m ? firstLine(shownText(m)) || "Message without text" : "Open request"}</span>
            {m && <span className="block pt-1 text-[12px] text-text-2">{m.exec || m.state === "running" || m.state === "needs_human" ? requestState(m,view?.messages || []).text : "No completion recorded here"}{(m.target?.address || m.exec?.host) ? " · on " + deviceWords(m.target?.address || m.exec?.host || "",overview) : ""}</span>}
          </span>
        </Button>;
      })}
      {!topic.pendingIDs?.length && <p className="text-text-2">No pending requests remain in this topic.</p>}
    </div>
  </Sheet>;
}

/** changeTopic sends one of your changes to a topic and says what it did. */
export function changeTopic(store: ReturnType<typeof useApp>, what: "rename" | "done" | "reopen" | "archive", t: Topic, title?: string) {
  // count: the messages this view showed, so a mark never covers one you have not seen.
  return store.changeTopic(what, { ...(t.root ? {root:true} : {}), peer: t.peer, conv:t.conv,id: t.id, ...(what === "rename" ? { title: title || "" } : { count: t.count }) }).then((r) => {
    if (r?.note) store.toast(r.note, "ok");
    return !!r;
  });
}

/** RenameTopic: a name of your own for a topic, synced across your linked devices. */
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
      description="The name syncs across your linked devices. Other people keep their own names."
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
 *  concluded (its own words, labelled as its), or your own final reply when
 *  you answered it by hand here, or that you marked it done, or that it is
 *  archived; Reopen makes it active again. */
export function TopicEnd({ ctx }: { ctx: Ctx }) {
  const store = useApp();
  const draft=useStore(store,s=>s.drafts[ctx.conv]);
  const current=ctx.dm?.topics?.find(t=>t.id===draft?.topic);
  const t = current?topicOf(current):ctx.thread?.topic ? topicOf(ctx.thread.topic) : null;
  const busy = useStore(store, s => t ? s.topicBusy[topicChangeKey(t)] : undefined);
  if (!t || t.state === "active" && !t.redirect || !ctx.overview?.topic_list) return null;
  if (t.redirect && ctx.dm) return <section aria-label="Merged topic" className="mx-3 mt-4 rounded-2xl bg-surface p-4 stroke lg:mx-5">
    <p className="text-[14px]">Selected messages moved to another topic. Later posts stay here.</p>
    <Button className="mt-2" onClick={() => store.setDraft(ctx.conv, { ...store.draft(ctx.conv), topic: t.redirect, newTopic: false, replyTo: undefined }, true)}>Open {ctx.dm.topics?.find(x => x.id === t.redirect)?.title || "destination topic"}</Button>
  </section>;
  const agent = t.concludedBy && t.concludedBy === ctx.overview?.me.address ? "Your agent" : threadAgentName(ctx);
  const archived = t.state === "archived";
  return (
    <section aria-label="Topic state" className="mx-3 mt-4 rounded-2xl bg-surface p-4 stroke lg:mx-5">
      <div className="flex flex-wrap items-center gap-2">
        {archived ? <Tag tone="muted"><IconArchive size={12} stroke={2.6} aria-hidden="true" />Archived</Tag> : <Tag tone="ok"><IconCircleCheck size={12} stroke={2.6} aria-hidden="true" />Done</Tag>}
        {t.doneBy === "you" && !t.conclusion && <span className="text-[13px] text-text-2">You marked it done on this device.</span>}
        {t.doneBy === "person" && <span className="text-[13px] text-text-2">{t.concludedBy===ctx.overview?.me.address?"You":ctx.overview?.people?.find(p=>p.address===t.concludedBy||p.devices?.some(d=>d.address===t.concludedBy))?.label||"A participant"} marked this done for everyone.</span>}
        {t.doneBy === "agent" && !archived && <span className="text-[13px] text-text-2">{agent} closed this topic.</span>}
      </div>
      {t.conclusion && (
        <p className="mt-2 text-[15px]"><span className="font-bold">{agent + "’s conclusion:"}</span> <span className="text-text-2">{t.conclusion}</span></p>
      )}
      {archived && (
        <p className="mt-2 text-[14px] text-text-2">
          Quiet since {dayLabel(t.quietSince).toLowerCase() === "today" ? "today" : new Date(t.quietSince).toLocaleDateString([], { month: "short", day: "numeric" })}, with nothing waiting. Nothing was deleted: a new message here makes it active again.
        </p>
      )}
      <div className="mt-3">
        <Button size="sm" variant="outline" disabled={!!busy} aria-busy={!!busy} icon={<IconRotateClockwise size={18} aria-hidden="true" />} onClick={() => void changeTopic(store, "reopen", t)}>{busy === "reopen" ? "Reopening…" : "Reopen"}</Button>
      </div>
    </section>
  );
}
