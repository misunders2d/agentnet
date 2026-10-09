// The open conversation: a DM, a group, or an agent's own device thread.
// Header and (phones) guest bar on top, the timeline, then the composer, or
// while messages are being chosen to share, the selection bar. Everything
// shown comes from the conversation's loaded view (store.views); actions
// go through the store.
import { useCallback, useEffect, useMemo, useState } from "react";
import { IconChevronLeft, IconMessages, IconX, IconUserPlus } from "@tabler/icons-react";
import { useAgentNames, useApp, useWide } from "../context";
import type { T } from "../api";
import { useStore, type Open, type View } from "../store";
import { MOTION, Swap, useLeaving } from "../ui/Motion";
import { Button } from "../ui/Button";
import { Composer } from "./Composer";
import { InviteSheet } from "./InviteSheet";
import { bringIn, PendingInvitations, RoomSheet } from "./RoomPanel";
import { agentRejoinState } from "./RoomPanel.model";
import { Header, GuestBar, helpers, type Helper } from "./Conversation.header";
import { TopicBar, TopicEnd } from "./Conversation.topics";
import { PersonTopics } from "./PersonTopics";
import { EmptyTimeline, Timeline } from "./Conversation.timeline";
import { roomTitle, threadAgentName, type AnyMsg, type Ctx } from "./Message.model";
import { chatList, deviceKind } from "../model";
import { AgentAvatar, GroupAvatar, PersonAvatar } from "../ui/Avatar";

/** convKey names what a conversation pane shows: a DM or group by its id,
 *  and an agent's device thread by the agent, so that its topics are one
 *  pane (switching topics swaps only the messages). It is the same before
 *  and after the messages arrive (store.open names the agent up front). */
export function convKey(o: NonNullable<Open>, views: Record<string, View>): string {
  if (o.kind === "dm") return "dm:" + o.id;
  const v = views[o.id] as T.Thread | undefined;
  return "thread:" + (o.peer || v?.peer || o.id);
}

/** Conversation shows conversation o from its loaded view (store.views):
 *  the open one, or, while it slides or fades away, the one just left.
 *  Until its messages are here (a slow load) it shows its header and a
 *  sketch of messages, which fades into the real thing in place. */
export function Conversation({ o }: { o: NonNullable<Open> }) {
  const views = useStore(useApp(), (s) => s.views);
  const key = convKey(o, views);
  const ready = !!views[o.id];
  return (
    <Swap id={ready ? key : key + ":opening"} className="flex min-h-0 min-w-0 flex-1 flex-col" side="flex min-h-0 min-w-0 flex-1 flex-col" enter="" leave="an-ready-out" ms={MOTION.ready} label="opening">
      {ready ? <OpenView open={o} /> : <Loading o={o} />}
    </Swap>
  );
}

function OpenView({ open }: { open: NonNullable<Open> }) {
  const store = useApp();
  const wide = useWide();
  const leaving = useLeaving();
  const view = useStore(store, (s) => s.views[open.id]);
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const [selected, setSelected] = useState<string[] | null>(null);
  const [left, setLeft] = useState<Helper | null>(null);

  const t = open.kind === "dm" && view && view.id === open.id ? view as T.DMThread : null;
  const th = open.kind === "thread" && view ? view as T.Thread : null;
  const ctx: Ctx = useMemo(() => ({
    conv: t?.id ?? th?.id ?? open.id, dm: t, thread: th, overview, names, // the Composer's draft key
    canReply: t ? !t.frozen : th ? !th.key?.pending : false,
  }), [open.id, t, th, overview, names]);
  const hs = useMemo(() => helpers(ctx, (fn) => store.run(fn)), [ctx]);
  const canInvite = !!t && !t.frozen && (!t.role || t.role === "member");
  const topic = useStore(store, s => s.drafts[open.id]?.topic);
  const newTopic = useStore(store, s => s.drafts[open.id]?.newTopic);
  const personMain=t&&store.personMain(t);
  const messages: AnyMsg[] = useMemo(() => (t ? (t.messages||[]).filter(m => newTopic ? !m.topic : (m.topic||"") === (topic||"")) : th?.messages) || [], [t?.messages, th?.messages, topic, newTopic]);
  const toggle = useCallback((id: string) => setSelected(s => s ? (s.includes(id) ? s.filter(x => x !== id) : [...s, id]) : [id]), []);

  useEffect(() => {
    if (!selected) return;
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") setSelected(null); };
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  }, [!!selected]);
  useEffect(() => { if (!left) return; const x = setTimeout(() => setLeft(null), 9000); return () => clearTimeout(x); }, [left]);
  useEffect(() => setSelected(null), [open.id]); // another topic: nothing chosen in it yet

  if (!t && !th) return <Loading o={open} />;

  const title = t ? (t.kind === "group" ? t.title || "this group" : roomTitle(t)) : threadAgentName(ctx);

  return (
    <section aria-label={title} className="relative flex min-h-0 min-w-0 flex-1 flex-col bg-canvas">
      <Header ctx={ctx} wide={wide} helpers={hs} canInvite={canInvite} />
      {t && <PendingInvitations t={t} />}
      {!wide && <GuestBar helpers={hs} onDismissed={setLeft} />}
      {t&&personMain ? <PersonTopics dm={t} main={personMain} onMain={()=>{
        store.setDraft(personMain,{...store.draft(personMain),topic:undefined,newTopic:false,replyTo:undefined});
        void store.open({kind:"dm",id:personMain});
      }}/> : <TopicBar thread={ctx.thread||undefined} dm={ctx.dm||undefined} />}
      {/* Another topic swaps only the messages: header, topic bar and composer stay. */}
      <Swap id={open.id} className="flex min-h-0 flex-1 flex-col" side="flex min-h-0 flex-1 flex-col" enter="an-topic-in" leave="an-topic-out" ms={MOTION.topic} label="timeline">
      <Timeline ctx={ctx} messages={messages} focus={open.focus} focusSeq={open.focusSeq} selected={selected} onSelect={toggle} end={<TopicEnd ctx={ctx} />}
        footer={left && (
          // The room panel's words; the timeline keeps its last lines clear of it.
          <div role="status" className="pop-in flex items-center gap-3 rounded-2xl stroke bg-ink py-2 pl-4 pr-2 text-canvas shadow-pop">
            <p className="min-w-0 flex-1 text-[14px] font-semibold leading-snug">{left.name} left · dismissed by you</p>
            {t && !agentRejoinState(t, left.pid) && <Button size="sm" variant="act" onClick={() => {
              setLeft(null);
              void bringIn(store, t, wide, left.who, undefined, left.pid);
            }}>Bring back</Button>}
          </div>
        )}
        empty={<EmptyTimeline title={th ? "Nothing here yet" : "Say hello"}
          to={t ? "What you write here goes to " + (t.kind === "group" ? "the members of " + title : title) + " only." : "Messages with " + title + " show up here."} />} />
      </Swap>
      {selected
        ? <SelectBar count={selected.length} onCancel={() => setSelected(null)}
            onBringIn={() => { store.openInvite(ctx.conv, selected); setSelected(null); }} />
        : <Composer dm={t ?? undefined} thread={th ?? undefined} />}
      {!wide && !leaving && <RoomSheet />}
      {!leaving && <InviteSheet />}
    </section>
  );
}

function SelectBar({ count, onCancel, onBringIn }: { count: number; onCancel: () => void; onBringIn: () => void }) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-t-[1.5px] border-outline bg-surface px-3 py-2.5 pb-[max(10px,env(safe-area-inset-bottom))]">
      <button type="button" onClick={onCancel} aria-label="Stop selecting" className="grid size-11 place-items-center rounded-full hover:bg-sunken"><IconX size={22} /></button>
      <p className="min-w-0 flex-1 text-[15px] font-semibold" aria-live="polite">{count === 0 ? "Tap messages to share" : count === 1 ? "1 message selected" : count + " messages selected"}</p>
      <Button variant="act" icon={<IconUserPlus size={20} />} disabled={!count} onClick={onBringIn}>Bring someone in</Button>
    </div>
  );
}

function Loading({ o }: { o: NonNullable<Open> }) {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const [slow, setSlow] = useState(false);
  useEffect(() => { const x = setTimeout(() => setSlow(true), 6000); return () => clearTimeout(x); }, []);
  // Its header is the chat list's: who it is with, where the real header puts it.
  const item = useMemo(() => chatList(overview, names).find((i) => (o.kind === "dm" ? i.open.kind === "dm" && i.open.id === o.id : i.open.kind === "thread" && i.peer === o.peer)), [overview, names, o]);
  const size = wide ? 48 : 40;
  const face = !item ? <span className="shrink-0 rounded-full bg-hairline" style={{ width: size, height: size }} />
    : item.kind === "agent" ? <AgentAvatar seed={item.avatarSeed} size={size} device={deviceKind(item.avatarSeed)} />
    : item.kind === "group" ? <GroupAvatar names={item.members?.length ? item.members : [item.title]} seed={item.avatarSeed} size={size} />
    : <PersonAvatar name={item.title} seed={item.avatarSeed} size={size} />;
  // A sketch of a conversation: outlined bubbles, theirs and yours, with real contrast in both themes.
  const rows = [["w-40", false], ["w-56", true], ["w-32", false], ["w-64", false], ["w-44", true]] as const;
  return (
    <section aria-busy="true" aria-label={item ? "Opening " + item.title : "Opening conversation"} className="flex min-h-0 flex-1 flex-col bg-canvas">
      <header className={"flex shrink-0 items-center gap-2 border-b-[1.5px] border-outline bg-surface " + (wide ? "h-[76px] px-5" : "h-16 pl-1 pr-1.5")}>
        {!wide && (
          <button type="button" onClick={() => store.close()} aria-label="Back to chats" className="flex h-11 shrink-0 items-center rounded-full pl-1 pr-1.5 hover:bg-sunken">
            <IconChevronLeft size={26} stroke={2.2} />
          </button>
        )}
        <span className="flex min-w-0 flex-1 items-center gap-3 py-1 pr-2">
          {face}
          <span className="min-w-0">
            {item ? <span className={"block truncate font-display font-bold leading-tight " + (wide ? "text-[22px]" : "text-[18px]")}>{item.title}</span>
              : <span data-skeleton className="block h-4 w-36 rounded-full bg-hairline" />}
            {/* The real header's second line (presence, members, device): its words, or a sketch of them in their place. */}
            {item?.subtitle ? <span className="block truncate text-[13px] text-text-2">{item.subtitle}</span>
              : <span className="flex h-[19px] items-center"><span className="block h-2.5 w-24 rounded-full bg-hairline" /></span>}
          </span>
        </span>
      </header>
      <div className="dots flex flex-1 flex-col justify-end gap-3 px-4 pb-6 lg:px-6">
        {rows.map(([w, mine], i) => (
          <span key={i} data-skeleton style={{ animationDelay: i * 90 + "ms" }}
            className={"an-sketch h-10 rounded-[20px] border-[1.5px] border-outline/30 shadow-[2px_2px_0_0_rgb(27_21_48/.08)] " + w + (mine ? " self-end rounded-br-md bg-mine" : " rounded-bl-md bg-surface")} />
        ))}
        {slow && (
          <p className="mt-2 self-center text-[14px] text-text-2">
            Still opening…{" "}
            <button type="button" onClick={() => { const c = store.get().open; if (c) void store.open({ ...c }); }} className="min-h-11 font-semibold text-agent-ink underline underline-offset-2">Try again</button>
          </p>
        )}
      </div>
    </section>
  );
}

export function NothingOpen() {
  return (
    <section className="dots flex min-h-0 flex-1 flex-col items-center justify-center gap-3 bg-canvas p-8 text-center">
      <span className="grid size-20 place-items-center rounded-3xl bg-mine text-mine-ink stroke"><IconMessages size={40} /></span>
      <p className="font-display text-[26px] font-extrabold">Pick a chat</p>
      <p className="max-w-sm text-[15px] text-text-2">Your conversations with people and their agents open here.</p>
    </section>
  );
}
