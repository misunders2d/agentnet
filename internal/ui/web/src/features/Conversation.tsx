// The open conversation: a DM, a group, or an agent's own device thread.
// Header and (phones) guest bar on top, the timeline, then the composer, or
// while messages are being chosen to share, the selection bar. Everything
// shown comes from the conversation's loaded view (store.views); actions
// go through the store.
import { useEffect, useMemo, useState } from "react";
import { IconMessages, IconX, IconUserPlus } from "@tabler/icons-react";
import { useAgentNames, useApp, useWide } from "../context";
import type { T } from "../api";
import { useStore, type Open, type View } from "../store";
import { MOTION, Swap, useLeaving } from "../ui/Motion";
import { Button } from "../ui/Button";
import { Composer } from "./Composer";
import { InviteSheet } from "./InviteSheet";
import { RoomSheet } from "./RoomPanel";
import { Header, GuestBar, helpers, type Helper } from "./Conversation.header";
import { TopicBar, TopicEnd } from "./Conversation.topics";
import { EmptyTimeline, Timeline } from "./Conversation.timeline";
import { roomTitle, threadAgentName, type AnyMsg, type Ctx } from "./Message.model";

/** convKey names what a conversation pane shows: a DM or group by its id,
 *  and an agent's device thread by the agent, so that its topics are one
 *  pane (switching topics swaps only the messages). */
export function convKey(o: NonNullable<Open>, views: Record<string, View>): string {
  if (o.kind === "dm") return "dm:" + o.id;
  const v = views[o.id] as T.Thread | undefined;
  return "thread:" + (v?.peer || o.id);
}

/** Conversation shows conversation o from its loaded view (store.views):
 *  the open one, or, while it slides or fades away, the one just left. */
export function Conversation({ o }: { o: NonNullable<Open> }) {
  const views = useStore(useApp(), (s) => s.views);
  return <OpenView key={convKey(o, views)} open={o} />;
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
  const messages: AnyMsg[] = (t ? t.messages : th ? th.messages : null) || [];

  useEffect(() => {
    if (!selected) return;
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") setSelected(null); };
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  }, [!!selected]);
  useEffect(() => { if (!left) return; const x = setTimeout(() => setLeft(null), 9000); return () => clearTimeout(x); }, [left]);
  useEffect(() => setSelected(null), [open.id]); // another topic: nothing chosen in it yet

  if (!t && !th) return <Loading wide={wide} />;

  const title = t ? (t.kind === "group" ? t.title || "this group" : roomTitle(t)) : threadAgentName(ctx);
  const toggle = (id: string) => setSelected((s) => (s ? (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]) : [id]));

  return (
    <section aria-label={title} className="relative flex min-h-0 min-w-0 flex-1 flex-col bg-canvas">
      <Header ctx={ctx} wide={wide} helpers={hs} canInvite={canInvite} />
      {!wide && <GuestBar helpers={hs} onDismissed={setLeft} />}
      {ctx.thread && <TopicBar thread={ctx.thread} />}
      {/* Another topic swaps only the messages: header, topic bar and composer stay. */}
      <Swap id={open.id} className="flex min-h-0 flex-1 flex-col" side="flex min-h-0 flex-1 flex-col" enter="an-topic-in" leave="an-topic-out" ms={MOTION.topic} label="timeline">
      <Timeline ctx={ctx} messages={messages} focus={open.focus} selected={selected} onSelect={toggle} end={th ? <TopicEnd ctx={ctx} /> : undefined}
        footer={left && (
          // The room panel's words; the timeline keeps its last lines clear of it.
          <div role="status" className="pop-in flex items-center gap-3 rounded-2xl stroke bg-ink py-2 pl-4 pr-2 text-canvas shadow-pop">
            <p className="min-w-0 flex-1 text-[14px] font-semibold leading-snug">{left.name} left · dismissed by you</p>
            <Button size="sm" variant="act" onClick={() => {
              setLeft(null);
              store.openInvite(ctx.conv, undefined, left.who ? { who: left.who, label: "Since they left" } : undefined);
            }}>Bring back</Button>
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

function Loading({ wide }: { wide: boolean }) {
  const store = useApp();
  const [slow, setSlow] = useState(false);
  useEffect(() => { const x = setTimeout(() => setSlow(true), 6000); return () => clearTimeout(x); }, []);
  const rows = [["w-40", false], ["w-56", true], ["w-32", false], ["w-64", false], ["w-44", true]] as const;
  return (
    <section aria-busy="true" aria-label="Opening conversation" className="flex min-h-0 flex-1 flex-col bg-canvas">
      <div className={"flex shrink-0 items-center gap-3 border-b-[1.5px] border-outline bg-surface " + (wide ? "h-[76px] px-5" : "h-16 px-3")}>
        {!wide && <button type="button" onClick={() => store.close()} className="min-h-11 rounded-full px-2 text-[15px] font-semibold hover:bg-sunken">Back</button>}
        <span className="size-10 animate-pulse rounded-full bg-sunken motion-reduce:animate-none" />
        <span className="h-4 w-36 animate-pulse rounded-full bg-sunken motion-reduce:animate-none" />
      </div>
      <div className="dots flex flex-1 flex-col justify-end gap-3 px-4 pb-6">
        {rows.map(([w, mine], i) => (
          <span key={i} className={"h-10 animate-pulse rounded-[20px] bg-surface/80 motion-reduce:animate-none " + w + (mine ? " self-end bg-mine/60" : "")} />
        ))}
        {slow && (
          <p className="mt-2 self-center text-[14px] text-text-2">
            Still opening…{" "}
            <button type="button" onClick={() => { const o = store.get().open; if (o) void store.open({ ...o }); }} className="min-h-11 font-semibold text-agent-ink underline underline-offset-2">Try again</button>
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
