// The timeline: messages by day, runs of one author grouped, participation
// dividers, who is typing and which agent is working. It opens at the
// bottom (or at the message a notification or approval points to), stays
// pinned there while you are at the bottom, and otherwise keeps your place
// and counts what arrived below in a "new" pill.
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { IconArrowDown, IconMessageCircle } from "@tabler/icons-react";
import { useApp } from "../context";
import { useStore } from "../store";
import { agentName, dayLabel, sameDay, timeOf } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { MessageView } from "./Message";
import { agentLabel, agentOf, ev, isRequest, isThreadMsg, threadAgentName, whoWrote, working, type AnyMsg, type Ctx } from "./Message.model";

type Item =
  | { type: "day"; key: string; label: string }
  | { type: "new"; key: string }
  | { type: "msg"; key: string; m: AnyMsg; first: boolean; last: boolean; status: boolean };

const RUN = 5 * 60e3;
const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

export function Timeline({ ctx, messages, focus, focusSeq, selected, onSelect, empty, footer, end }: {
  ctx: Ctx; messages: AnyMsg[]; focus?: string; focusSeq?: number; selected: string[] | null; onSelect: (id: string) => void; empty: ReactNode;
  footer?: ReactNode;      // a snackbar over the timeline's end (Bring back): the last lines stay clear of it
  end?: ReactNode;         // after the last message (a topic that is done or archived says so)
}) {
  const box = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const foot = useRef<HTMLDivElement>(null);
  const [reserve, setReserve] = useState(0);
  const atBottom = useRef(!focus);
  const seen = useRef<Set<string> | null>(null);
  const [fresh, setFresh] = useState(0);
  const [far, setFar] = useState(false);
  // The first unread message when the conversation opened: "New" goes above it.
  const [firstUnread] = useState(() => messages.find((m) => m.unread && m.dir === "in")?.id);

  const items = useMemo(() => build(messages, ctx, firstUnread), [messages, ctx, firstUnread]);

  // Open at the bottom or at the focused message; afterwards follow new
  // messages only while at the bottom (or when they are your own).
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    if (!seen.current) {
      seen.current = new Set(messages.map((m) => m.id));
      if (focus && flash(el, focus, false)) return;
      el.scrollTop = el.scrollHeight;
      return;
    }
    const added = messages.filter((m) => !seen.current!.has(m.id));
    for (const m of added) seen.current.add(m.id);
    if (!added.length) return;
    if (atBottom.current || added.some((m) => whoWrote(m, ctx).mine)) el.scrollTo({ top: el.scrollHeight, behavior: reduced() ? "auto" : "smooth" });
    else setFresh((n) => n + added.filter((m) => !ev(m)).length);
  }, [messages]);

  const focused = focus && messages.some(m => m.id === focus) ? focus : undefined;
  useEffect(() => { if (focused && seen.current && box.current) flash(box.current, focused, true); }, [focused, focusSeq]);

  // Pictures, reactions and the typing line change the height, and a
  // phone's keyboard (or a growing message box) shrinks the view: the
  // latest message stays in sight while you are at the bottom.
  useEffect(() => {
    const el = box.current, content = inner.current;
    if (!el || !content) return;
    const ro = new ResizeObserver(() => { if (atBottom.current) el.scrollTop = el.scrollHeight; });
    ro.observe(content);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // While a snackbar shows, the log ends above it, scrolled to its end (the divider it speaks of is there).
  useLayoutEffect(() => setReserve(footer && foot.current ? foot.current.offsetHeight + 12 : 0), [!!footer]);
  useLayoutEffect(() => {
    const el = box.current;
    if (!reserve || !el) return;
    atBottom.current = true;
    el.scrollTo({ top: el.scrollHeight, behavior: reduced() ? "auto" : "smooth" });
  }, [reserve]);

  const reportSeen = useSeen(ctx, messages, atBottom);

  const onScroll = () => {
    const el = box.current!;
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
    atBottom.current = gap < 80;
    setFar(gap > 600);
    if (atBottom.current) { if (fresh) setFresh(0); reportSeen(); }
  };
  const down = () => { box.current?.scrollTo({ top: box.current.scrollHeight, behavior: reduced() ? "auto" : "smooth" }); setFresh(0); };
  const jump = (id: string) => { if (box.current) flash(box.current, id, true); };

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={box} onScroll={onScroll} className="dots h-full overflow-y-auto overscroll-contain">
        <div ref={inner} role="log" aria-live="polite" aria-relevant="additions" aria-label="Messages" className="mx-auto flex min-h-full w-full max-w-[800px] flex-col justify-end pb-6 pt-3"
          style={reserve ? { paddingBottom: reserve + 24 } : undefined}>
          {items.length === 0 && empty}
          {items.map((it) =>
            it.type === "day" ? <DayChip key={it.key} label={it.label} />
              : it.type === "new" ? <NewLine key={it.key} />
                : <MessageView key={it.key} m={it.m} ctx={ctx} all={messages} first={it.first} last={it.last} status={it.status} onJump={jump}
                    selecting={!!selected} selected={!!selected?.includes(it.m.id)} onSelect={onSelect} />)}
          {items.length > 0 && end}
          <Activity ctx={ctx} messages={messages} />
        </div>
      </div>
      {(fresh > 0 || far) && (
        <button type="button" onClick={down} style={reserve ? { bottom: reserve + 12 } : undefined}
          className="pop-in absolute bottom-3 left-1/2 z-10 flex h-11 -translate-x-1/2 items-center gap-1.5 rounded-full bg-ink px-4 text-[14px] font-bold text-canvas shadow-pop-sm">
          <IconArrowDown size={18} />{fresh > 0 ? fresh + " new" : "Latest"}
        </button>
      )}
      {footer && <div ref={foot} className="absolute inset-x-3 bottom-3 z-20 mx-auto max-w-md">{footer}</div>}
    </div>
  );
}

function build(messages: AnyMsg[], ctx: Ctx, firstUnread?: string): Item[] {
  const out: Item[] = [];
  const who = messages.map((m) => (ev(m) ? null : whoWrote(m, ctx)));
  const together = (i: number, j: number) => {
    const a = messages[i], b = messages[j], wa = who[i], wb = who[j];
    return !!wa && !!wb && wa.key === wb.key && sameDay(a.at, b.at) && Math.abs(+new Date(b.at) - +new Date(a.at)) < RUN && !(a.actions || []).length && b.id !== firstUnread;
  };
  let lastMine = -1;
  messages.forEach((m, i) => { if (who[i]?.mine && !isRequest(m) && !m.deleted) lastMine = i; });
  messages.forEach((m, i) => {
    if (i === 0 || !sameDay(messages[i - 1].at, m.at)) out.push({ type: "day", key: "d:" + m.at.slice(0, 10) + i, label: dayLabel(m.at) });
    if (m.id === firstUnread) out.push({ type: "new", key: "new" });
    out.push({ type: "msg", key: m.id, m, first: !(i > 0 && together(i - 1, i)), last: !(i + 1 < messages.length && together(i, i + 1)), status: i === lastMine });
  });
  return out;
}

function flash(box: HTMLElement, id: string, smooth: boolean) {
  const el = box.querySelector<HTMLElement>("[data-mid=\"" + CSS.escape(id) + "\"]");
  if (!el) return false;
  const target = el.querySelector<HTMLElement>("[data-agent-needs-you]") || el;
  target.scrollIntoView({ block: target === el ? "center" : "start", behavior: smooth && !reduced() ? "smooth" : "auto" });
  el.animate([{ backgroundColor: "color-mix(in srgb, var(--an-act) 40%, transparent)" }, { backgroundColor: "transparent" }], { duration: 1800, easing: "ease-out" });
  return true;
}

function DayChip({ label }: { label: string }) {
  return (
    <div className="my-3 flex justify-center" role="separator" aria-label={label}>
      <span className="rounded-full bg-surface px-3 py-1 text-[12px] font-bold text-text-2 stroke">{label}</span>
    </div>
  );
}

function NewLine() {
  return (
    <div className="my-2 flex items-center gap-2 px-4" role="separator" aria-label="New messages">
      <span className="h-px flex-1 bg-danger/50" />
      <span className="text-[12px] font-bold uppercase tracking-wide text-danger">New</span>
      <span className="h-px flex-1 bg-danger/50" />
    </div>
  );
}

// ---- typing and working ------------------------------------------------------------------

function Activity({ ctx, messages }: { ctx: Ctx; messages: AnyMsg[] }) {
  const store = useApp();
  const typing = useStore(store, (s) => s.typing);
  const [now, setNow] = useState(Date.now);
  const entries = (typing?.entries || []).filter((e) => +new Date(e.expires) > now);
  // Each typing line goes when it expires (the server's own deadline).
  useEffect(() => {
    const next = Math.min(...entries.map((e) => +new Date(e.expires)));
    if (!isFinite(next)) return;
    const t = setTimeout(() => setNow(Date.now()), Math.max(50, next - Date.now() + 20));
    return () => clearTimeout(t);
  }, [typing, now]);

  const busy = new Map<string, { name: string; seed: string; since?: number; what: string }>();
  for (const m of messages) {
    if (!working(m, messages) || (m.actions || []).includes("cancel")) continue; // its approval card shows it, with Stop
    const what = (isThreadMsg(m) ? m.detail : m.job_detail) || "";
    if (isThreadMsg(m)) {
      // In a device thread the agent works on what you sent it; what it was sent works on your side.
      const name = m.dir === "out" ? threadAgentName(ctx) : agentName(undefined, ctx.names, ctx.overview?.person, ctx.overview?.person);
      busy.set(m.dir, { name, seed: m.dir === "out" ? ctx.thread?.peer || m.id : ctx.overview?.me.address || m.id, since: m.exec?.at, what });
      continue;
    }
    const a = agentOf(ctx, m.pid);
    if (a) busy.set(a.pid, { name: agentLabel(a, ctx), seed: a.agent_id || a.host.address, since: m.exec?.at, what });
  }
  if (!entries.length && !busy.size) return null;
  const names = entries.map((e) => e.label || "Someone");
  return (
    <div className="mt-3 flex flex-col gap-2 px-3 sm:px-4">
      {[...busy.entries()].map(([k, b]) => (
        <div key={k} role="status" className="fade-in ml-10 flex max-w-[min(100%,360px)] items-center gap-3 rounded-xl border-[1.5px] border-dashed border-agent-ink bg-surface px-3 py-2">
          <AgentAvatar seed={b.seed} size={32} mood="working" />
          <span className="min-w-0 leading-tight">
            <span className="flex items-center gap-2 text-[14px] font-bold text-agent-ink">{b.name} is working<Dots /></span>
            {(b.what || b.since) && <span className="block truncate text-[13px] text-text-2">{b.what || "since " + timeOf(new Date(b.since! < 1e11 ? b.since! * 1000 : b.since!).toISOString())}</span>}
          </span>
        </div>
      ))}
      {entries.length > 0 && (
        <div role="status" className="fade-in flex items-center gap-2 text-[13px] font-semibold text-text-2">
          <span className="ml-0.5 flex -space-x-1.5">{entries.slice(0, 3).map((e) => <PersonAvatar key={e.address} name={e.label || "?"} seed={e.person || e.address} size={24} />)}</span>
          {names.length === 1 ? names[0] + " is typing" : names.length === 2 ? names[0] + " and " + names[1] + " are typing" : names.length + " people are typing"}
          <Dots />
        </div>
      )}
    </div>
  );
}

function Dots() {
  return (
    <span className="inline-flex items-center gap-[3px]" aria-hidden="true">
      <span className="working-dot size-1.5 rounded-full bg-current" /><span className="working-dot size-1.5 rounded-full bg-current" /><span className="working-dot size-1.5 rounded-full bg-current" />
    </span>
  );
}

// ---- telling notifications what is in front of the person ------------------------------------

// useSeen reports the newest received messages of the open DM as seen (so
// other devices stop alerting) only while the page is visible and focused
// and the timeline is at its bottom; opening a conversation alone is not enough.
function useSeen(ctx: Ctx, messages: AnyMsg[], atBottom: RefObject<boolean>) {
  const store = useApp();
  const done = useRef("");
  const report = useRef(() => {});
  report.current = () => {
    const t = ctx.dm;
    if (!t || !ctx.overview?.notify?.enabled) return;
    if (document.visibilityState !== "visible" || !document.hasFocus() || !atBottom.current) return;
    const ids = messages.filter((m) => m.dir === "in" && !ev(m)).slice(-32).map((m) => m.id);
    if (!ids.length || done.current === ids[ids.length - 1]) return;
    done.current = ids[ids.length - 1];
    store.api.notify("seen", { conv: t.id, ids }).catch(() => { done.current = ""; });
  };
  useEffect(() => {
    const now = () => report.current();
    now();
    window.addEventListener("focus", now);
    document.addEventListener("visibilitychange", now);
    return () => { window.removeEventListener("focus", now); document.removeEventListener("visibilitychange", now); };
  }, [messages]);
  return () => report.current();
}

export function EmptyTimeline({ title, to }: { title: string; to: string }) {
  return (
    <div className="m-auto flex max-w-xs flex-col items-center px-6 py-10 text-center">
      <span className="mb-3 grid size-16 place-items-center rounded-2xl bg-mine text-mine-ink stroke"><IconMessageCircle size={30} /></span>
      <p className="font-display text-[20px] font-bold">{title}</p>
      <p className="mt-1 text-[15px] text-text-2">{to}</p>
    </div>
  );
}
