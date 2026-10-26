import { NativeActivity } from "./NativeActivity";
import { currentNativeActivity } from "./NativeActivity.model";
// The timeline: messages by day, runs of one author grouped, participation
// dividers, who is typing and which agent is working. It opens at the
// bottom (or at the message a notification or approval points to), stays
// pinned there while you are at the bottom, and otherwise keeps your place
// and counts what arrived below in a "new" pill.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { IconArrowDown, IconMessageCircle } from "@tabler/icons-react";
import { useApp } from "../context";
import { useStore, type ReadVisit } from "../store";
import { agentName, dayLabel, messageTarget, timeOf } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { groupedSends } from "../optimistic.mjs";
import { localRunningDetail } from "../runningDetail";
import type { T } from "../api";
import { StopOwnRequest } from "./RequestResolution";
import { useOwned } from "../owned";
import { focusComposer } from "./Composer.focus";
import { MessageView } from "./Message";
import { agentLabel, agentOf, ev, isRequest, isThreadMsg, threadAgentName, whoWrote, working, terminalStopLabel, type AnyMsg, type Ctx } from "./Message.model";

type Item =
  | { type: "day"; key: string; label: string }
  | { type: "new"; key: string }
  | { type: "msg"; key: string; m: AnyMsg; first: boolean; last: boolean; status: boolean; compact: boolean; targets?: AnyMsg[] };

const RUN = 5 * 60e3;
const HISTORY_PAGE = 80; // message groups, keeping every target of one send together
const INITIAL_PAGE = 24; // a few screens; explicit history reveals retain the 80-group batch
const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
type ReadingAnchor = { row: HTMLElement; offset: number; scrollTop: number };

export function Timeline({ ctx, messages, focus, focusSeq, unread, readVisit, selected, onSelect, empty, footer, start, end }: {
  ctx: Ctx; messages: AnyMsg[]; focus?: string; focusSeq?: number; selected: string[] | null; onSelect: (id: string) => void; empty: ReactNode;
  unread?: boolean; readVisit: ReadVisit | null;
  footer?: ReactNode;      // a snackbar over the timeline's end (Bring back): the last lines stay clear of it
  start?: ReactNode;       // before the first message (the skin's topic intro, skin/TopicIntro.tsx)
  end?: ReactNode;         // after the last message (a topic that is done or archived says so)
}) {
  const { root } = useOwned();
  const box = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const foot = useRef<HTMLDivElement>(null);
  const initialFocus = useRef(focus);
  const [reserve, setReserve] = useState(0);
  const atBottom = useRef(!focus && !unread);
  const seen = useRef<Set<string> | null>(null);
  const [fresh, setFresh] = useState(0);
  const [far, setFar] = useState(false);
  const firstUnread = readVisit?.firstUnread;

  const items = useMemo(() => build(messages, ctx, firstUnread), [messages, ctx, firstUnread]);
  const groups = useMemo(() => items.filter((it): it is Extract<Item, { type: "msg" }> => it.type === "msg"), [items]);
  const [first, setFirst] = useState(() => groups[Math.max(0, groups.length - INITIAL_PAGE)]?.key);
  const [through, setThrough] = useState<string>();
  const retained = useRef<string[]>([]);
  const prependAnchor = useRef<{ row: HTMLElement; top: number; focus: boolean } | null>(null);
  const readingAnchor = useRef<ReadingAnchor | null>(null);
  const [requested, setRequested] = useState<{ id: string; focus?: string; seq?: number; visit: ReadVisit | null }>();
  const focused = messageTarget(messages, focus || firstUnread)?.id;
  const target = requested && requested.visit === readVisit && requested.focus === focus && requested.seq === focusSeq ? requested.id : focused;
  let from = groups.findIndex(it => it.key === first);
  if (from < 0) {
    // A deletion or a different canonical copy can remove the boundary row.
    // Keep the next surviving displayed group rather than dropping history.
    const keys = new Set(groups.map(it => it.key));
    const next = retained.current.find(key => keys.has(key));
    from = next ? groups.findIndex(it => it.key === next) : Math.max(0, groups.length - INITIAL_PAGE);
  }
  const wanted = groups.findIndex(it => it.m.id === target || it.targets?.some(m => m.id === target));
  if (wanted >= 0 && wanted < from) {
    // Revealing an exact old target is permanent for this mounted timeline.
    // A subsequent focus must not hide history the reader has already opened.
    from = wanted;
    setFirst(groups[wanted].key);
    // An old entry target must not mount every newer row just to reveal it.
    // Keep the existing page size, with explicit access to subsequent pages.
    if (!seen.current && !requested && focus === initialFocus.current && wanted + INITIAL_PAGE < groups.length) setThrough(groups[wanted + INITIAL_PAGE - 1].key);
  }
  let to = through ? groups.findIndex(it => it.key === through) + 1 : groups.length;
  if (through && !to) {
    const keys = new Set(groups.map(it => it.key));
    const last = retained.current.filter(key => keys.has(key)).at(-1);
    to = last ? groups.findIndex(it => it.key === last) + 1 : Math.min(groups.length, from + HISTORY_PAGE);
  }
  if (wanted >= to) { to = Math.min(groups.length, wanted + HISTORY_PAGE); setThrough(to < groups.length ? groups[to - 1].key : undefined); }
  const hasLater = to < groups.length;
  let begin = groups.length ? items.indexOf(groups[from]) : 0;
  while (begin > 0 && items[begin - 1].type !== "msg") begin--;
  let finish = hasLater ? items.indexOf(groups[to]) : items.length;
  while (hasLater && finish > begin && items[finish - 1].type !== "msg") finish--;
  const visible = items.slice(begin, finish);
  // Constructing a locale formatter per timestamp dominates large-page render
  // time. Reuse one for this immutable message projection, including redraws.
  const clock = useMemo(() => new Intl.DateTimeFormat([], { hour: "numeric", minute: "2-digit" }), [messages]);
  const formatTime = useCallback((at: string) => { const date = new Date(at); return Number.isNaN(date.getTime()) ? timeOf(at) : clock.format(date); }, [clock]);
  const [keyboardEntry, setKeyboardEntry] = useState<{ id: string; target?: string }>();
  const entryID = keyboardEntry?.target === target ? keyboardEntry?.id : target;
  const keyboardRow = visible.find(it => it.type === "msg" && (it.m.id === entryID || it.targets?.some(m => m.id === entryID)));
  const keyboardID = keyboardRow?.key || visible.filter(it => it.type === "msg" && !ev(it.m)).at(-1)?.key;
  const entryFocus = useCallback((id: string) => setKeyboardEntry(old => old?.id === id && old.target === target ? old : { id, target }), [target]);

  const keyboard = (e: KeyboardEvent<HTMLDivElement>) => {
    const log = inner.current, el = e.target as HTMLElement;
    // Portalled menus and editors keep their own keys and composition.
    if (!log?.contains(el) || e.nativeEvent.isComposing || e.altKey || e.ctrlKey || e.metaKey) return;
    const row = el.closest<HTMLElement>("[data-mid]");
    if (!row) return;
    if (el.hasAttribute("data-message-bubble") && ["ArrowUp", "ArrowDown", "Home", "End"].includes(e.key)) {
      const bubbles = [...log.querySelectorAll<HTMLElement>("[data-message-bubble][tabindex]")].filter(b => !b.closest("[hidden],[inert]") && b.getClientRects().length);
      const index = bubbles.indexOf(el);
      const next = e.key === "Home" ? 0 : e.key === "End" ? bubbles.length - 1 : Math.max(0, Math.min(bubbles.length - 1, index + (e.key === "ArrowUp" ? -1 : 1)));
      if (bubbles[next]) { e.preventDefault(); bubbles[next].focus(); }
      return;
    }
    if (e.key !== "Tab") return;
    const focusable = (b: HTMLElement) => b.tabIndex >= 0 && !b.matches(":disabled") && !b.closest("[hidden],[inert]") && !!b.getClientRects().length && getComputedStyle(b).visibility !== "hidden";
    const selector = "button,a[href],input,textarea,select,summary,[tabindex]";
    const following = e.shiftKey ? Node.DOCUMENT_POSITION_PRECEDING : Node.DOCUMENT_POSITION_FOLLOWING;
    // Tab may visit this message's actual controls and links. At its edge it
    // leaves the log, rather than walking controls on every retained row.
    if ([...row.querySelectorAll<HTMLElement>(selector)].some(b => b !== el && focusable(b) && !!(el.compareDocumentPosition(b) & following))) return;
    const composer = root.querySelector<HTMLTextAreaElement>('form[data-conv="' + CSS.escape(ctx.conv) + '"] textarea');
    if (!e.shiftKey && composer && !composer.disabled && !composer.closest("[inert]")) { e.preventDefault(); focusComposer(root, ctx.conv); return; }
    const outside = [...root.querySelectorAll<HTMLElement>(selector)].filter(b => !log.contains(b) && focusable(b) && !!(el.compareDocumentPosition(b) & following));
    const next = e.shiftKey ? outside.at(-1) : outside[0];
    if (next) { e.preventDefault(); next.focus(); }
  };

  const earlier = (event?: { detail: number }) => {
    if (!from || prependAnchor.current) return;
    // Capture one existing node only for this known prepend. Browser scroll
    // anchoring normally keeps it still; this delta is also the fallback on
    // engines without anchoring. Visibility separately measures only current
    // intersecting members, never the loaded history.
    const row = inner.current?.querySelector<HTMLElement>("[data-mid]");
    if (row) prependAnchor.current = { row, top: row.getBoundingClientRect().top,
      focus: event?.detail === 0 && from <= HISTORY_PAGE };
    setFirst(groups[Math.max(0, from - HISTORY_PAGE)].key);
  };
  const later = (event: { detail: number }) => {
    if (!hasLater || prependAnchor.current) return;
    const row = inner.current?.querySelector<HTMLElement>('[data-mid="' + CSS.escape(groups[to - 1].m.id) + '"]');
    if (row) prependAnchor.current = { row, top: row.getBoundingClientRect().top, focus: event.detail === 0 && to + HISTORY_PAGE >= groups.length };
    setThrough(to + HISTORY_PAGE < groups.length ? groups[to + HISTORY_PAGE - 1].key : undefined);
  };

  // Open at the bottom or at the focused message; afterwards follow new
  // messages only while at the bottom (or when they are your own).
  let placedOnEntry = false;
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    if (!focus && !readVisit) return; // cached display cannot choose the entry's anchor
    retained.current = groups.slice(from, to).map(it => it.key);
    const saved = prependAnchor.current;
    prependAnchor.current = null;
    if (saved?.row.isConnected && el.contains(saved.row)) {
      readingAnchor.current = null;
      el.scrollTop += saved.row.getBoundingClientRect().top - saved.top;
      if (saved.focus) {
        saved.row.tabIndex = -1;
        saved.row.focus({ preventScroll: true });
      }
    }
    if (!seen.current) {
      seen.current = new Set(messages.map((m) => m.id));
      if (target && flash(el, target, false)) { placedOnEntry = true; atBottom.current = false; return; }
      el.scrollTop = el.scrollHeight;
      atBottom.current = !hasLater;
      return;
    }
    const added = messages.filter((m) => !seen.current!.has(m.id));
    for (const m of added) seen.current.add(m.id);
    if (!added.length) return;
    if (atBottom.current || added.some((m) => whoWrote(m, ctx).mine)) {
      if (hasLater) { setFirst(groups[Math.max(0, groups.length - INITIAL_PAGE)].key); setThrough(undefined); setRequested({ id: messages.at(-1)!.id, focus, seq: focusSeq, visit: readVisit }); }
      atBottom.current = true;
      el.scrollTo({ top: el.scrollHeight, behavior: reduced() ? "auto" : "smooth" });
    }
    else setFresh((n) => n + added.filter((m) => !ev(m)).length);
  }, [messages, items, first, through, target, readVisit, unread, focus]);

  useLayoutEffect(() => {
    if (target && !placedOnEntry && seen.current && box.current) {
      readingAnchor.current = null;
      atBottom.current = false;
      flash(box.current, target, false);
    }
  }, [target, focusSeq, requested]);

  // Pictures, reactions and the typing line change the height, and a
  // phone's keyboard (or a growing message box) shrinks the view: the
  // latest message stays in sight while you are at the bottom.
  useEffect(() => {
    const el = box.current, content = inner.current;
    if (!el || !content) return;
    const needsAnchor = !CSS.supports("overflow-anchor", "auto");
    const ro = new ResizeObserver(() => {
      if (atBottom.current) {
        readingAnchor.current = null;
        el.scrollTop = el.scrollHeight;
      } else if (needsAnchor) {
        const saved = readingAnchor.current;
        // The visibility observer already measured this one reading row.
        // A changed scrollTop means a user scroll or explicit target won;
        // never pull it back while waiting for the next visibility frame.
        if (saved?.row.isConnected && el.contains(saved.row) && Math.abs(el.scrollTop - saved.scrollTop) < .5) {
          const offset = saved.row.getBoundingClientRect().top - el.getBoundingClientRect().top;
          if (Math.abs(offset - saved.offset) > .5) el.scrollTop += offset - saved.offset;
          saved.scrollTop = el.scrollTop;
        }
      }
      visibleRows.current();
    });
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
  const visibleRows = useVisibleRows(box, inner, ctx, messages, readVisit, first, through, reserve, readingAnchor, atBottom);

  const onScroll = () => {
    visibleRows.current();
    const el = box.current!;
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
    atBottom.current = !hasLater && gap < 80;
    setFar(gap > 600);
    if (el.scrollTop < 80 && !atBottom.current) earlier();
    if (atBottom.current) { if (fresh) setFresh(0); reportSeen(); }
  };
  const down = () => { if (hasLater) { setFirst(groups[Math.max(0, groups.length - INITIAL_PAGE)].key); setThrough(undefined); setRequested({ id: messages.at(-1)!.id, focus, seq: focusSeq, visit: readVisit }); } box.current?.scrollTo({ top: box.current.scrollHeight, behavior: reduced() ? "auto" : "smooth" }); setFresh(0); };
  const jump = useCallback((id: string) => {
    readingAnchor.current = null;
    atBottom.current = false;
    setRequested({ id, focus, seq: focusSeq, visit: readVisit });
  }, [focus, focusSeq, readVisit]);

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={box} onScroll={onScroll} className="dots h-full overflow-y-auto overscroll-contain">
        <div ref={inner} role="log" aria-live="polite" aria-relevant="additions" aria-label="Messages" data-part="timeline" onKeyDownCapture={keyboard} className="mx-auto flex min-h-full w-full max-w-[800px] flex-col justify-end pb-6 pt-3 [&>[data-mid]]:[contain:layout_style] [&>[data-send-group]]:[contain:layout_style]"
          style={reserve || hasLater ? { paddingBottom: reserve + (hasLater ? 80 : 24) } : undefined}>
          {items.length === 0 && empty}
          {from > 0 ? <button type="button" onClick={earlier} className="mx-auto min-h-11 px-4 text-sm font-semibold underline">Earlier messages</button> : items.length > 0 && start}
          {begin > 0 && visible[0]?.type !== "day" && groups[from] && <DayChip label={dayLabel(groups[from].m.at)} />}
          {visible.map((it) =>
            it.type === "day" ? <DayChip key={it.key} label={it.label} />
              : it.type === "new" ? <NewLine key={it.key} />
                : it.targets ? <div key={it.key} data-send-group={isThreadMsg(it.m) ? undefined : it.m.send_group} className="mt-3 rounded-2xl border border-outline/20 py-2">
                    <MessageView m={it.m} targets={it.targets} ctx={ctx} all={messages}
                      first={it.first} last={it.last} grouped onJump={jump} tabStop={it.key === keyboardID} onEntryFocus={entryFocus} formatTime={formatTime}
                      selecting={!!selected} selected={!!selected?.includes(it.m.id)} selectedIds={selected || undefined} onSelect={onSelect} />
                  </div>
                : <MessageView key={it.key} m={it.m} ctx={ctx} all={messages} first={it.first} last={it.last} status={it.status} compact={it.compact} onJump={jump} tabStop={it.key === keyboardID} onEntryFocus={entryFocus} formatTime={formatTime}
                    selecting={!!selected} selected={!!selected?.includes(it.m.id)} onSelect={onSelect} />)}
          {hasLater && <button type="button" onClick={later} className="mx-auto min-h-11 px-4 text-sm font-semibold underline">Later messages</button>}
          {items.length > 0 && !hasLater && end}
          <Activity ctx={ctx} messages={messages} />
        </div>
      </div>
      {(fresh > 0 || far || hasLater) && (
        <button type="button" onClick={down} style={reserve ? { bottom: reserve + 12 } : undefined}
          className="pop-in absolute bottom-3 left-1/2 z-10 flex h-11 -translate-x-1/2 items-center gap-1.5 rounded-full bg-ink px-4 text-[14px] font-bold text-canvas shadow-pop-sm">
          <IconArrowDown size={18} />{fresh > 0 ? fresh + " new" : "Latest"}
        </button>
      )}
      {footer && <div ref={foot} className="absolute inset-x-3 bottom-3 z-20 mx-auto max-w-md">{footer}</div>}
    </div>
  );
}

// One threshold-0 membership observer. A frame measures only its current
// canonical members and root, so even arbitrarily tall rows can become seen.
function useVisibleRows(box: RefObject<HTMLDivElement | null>, inner: RefObject<HTMLDivElement | null>, ctx: Ctx, messages: AnyMsg[], visit: ReadVisit | null, first: string | undefined, through: string | undefined, reserve: number, anchor: RefObject<ReadingAnchor | null>, atBottom: RefObject<boolean>) {
  const store = useApp();
  const request = useRef<() => void>(() => {});
  const reserved = useRef(reserve);
  reserved.current = reserve;
  useEffect(() => {
    if (!visit || !box.current || !inner.current) return;
    const root = box.current;
    const needsAnchor = !CSS.supports("overflow-anchor", "auto");
    const byID = new Map(messages.map(m => [m.id, m]));
    const shown = new Map<Element, AnyMsg[]>();
    let observer: IntersectionObserver | undefined, epoch = 0, frame: number | undefined, alive = true;
    let reported = new Set<string>();
    const publish = (rows: AnyMsg[]) => {
      const ids = new Set(rows.map(m => m.id));
      if (ids.size === reported.size && [...ids].every(id => reported.has(id))) return;
      reported = ids;
      store.reportVisible(visit, rows);
    };
    const permitted = () => document.visibilityState === "visible" && document.hasFocus() && !(store.host.platform === "android" && store.host.android?.visible() === false);
    const cancel = () => { if (frame !== undefined) cancelAnimationFrame(frame); frame = undefined; };
    const schedule = () => {
      if (!alive || frame !== undefined) return;
      const current = epoch;
      frame = requestAnimationFrame(() => {
        if (!alive || current !== epoch) return;
        frame = undefined;
        if (!permitted() || !shown.size) { publish([]); return; }
        const viewport = root.getBoundingClientRect(), bottom = Math.max(viewport.top, viewport.bottom - reserved.current);
        const height = bottom - viewport.top, seen = new Map<string, AnyMsg>();
        let reading: ReadingAnchor | null = null;
        if (height > 0 && viewport.width > 0) for (const [row, represented] of shown) {
          const r = row.getBoundingClientRect();
          const width = Math.min(r.right, viewport.right) - Math.max(r.left, viewport.left);
          const visible = Math.min(r.bottom, bottom) - Math.max(r.top, viewport.top);
          if (needsAnchor && !atBottom.current && width > 0 && visible > 0 && (!reading || r.top - viewport.top < reading.offset)) reading = { row: row as HTMLElement, offset: r.top - viewport.top, scrollTop: root.scrollTop };
          if (r.height > 0 && width > 0 && visible > 0 && visible >= Math.min(r.height / 2, height / 2)) for (const m of represented) seen.set(m.id, m);
        }
        if (needsAnchor) {
          const saved = anchor.current;
          // A visibility frame can run after late row growth but before the
          // resize callback. Keep its pre-growth offset until resize reconciles
          // it; a changed scrollTop means the user's newer reading place wins.
          if (!saved?.row.isConnected || !root.contains(saved.row) || Math.abs(root.scrollTop - saved.scrollTop) >= .5) anchor.current = reading;
        }
        publish([...seen.values()]);
      });
    };
    request.current = schedule;
    const restart = () => {
      const current = ++epoch;
      cancel(); observer?.disconnect(); shown.clear(); publish([]);
      if (!permitted()) return;
      observer = new IntersectionObserver(entries => {
        if (!alive || current !== epoch) return;
        for (const e of entries) {
          const row = e.target as HTMLElement;
          const represented = (row.dataset.messageIds || row.dataset.mid || "").split(" ").map(id => byID.get(id)).filter((m): m is AnyMsg => !!m);
          if (represented.length && e.isIntersecting && e.intersectionRect.width > 0 && e.intersectionRect.height > 0) shown.set(e.target, represented);
          else shown.delete(e.target);
        }
        schedule();
      }, { root, threshold: 0 });
      for (const row of inner.current!.querySelectorAll<HTMLElement>("[data-mid]")) if (byID.has(row.dataset.mid || "")) observer.observe(row);
    };
    restart();
    window.addEventListener("focus", restart);
    window.addEventListener("blur", restart);
    document.addEventListener("visibilitychange", restart);
    const off = store.host.platform === "android" ? store.host.android?.onVisibility(restart) : undefined;
    return () => {
      alive = false; ++epoch; cancel(); observer?.disconnect(); shown.clear(); publish([]);
      if (request.current === schedule) request.current = () => {};
      window.removeEventListener("focus", restart);
      window.removeEventListener("blur", restart);
      document.removeEventListener("visibilitychange", restart);
      off?.();
    };
  }, [store, ctx.conv, messages, visit, first, through]);
  useEffect(() => { request.current(); }, [reserve]);
  return request;
}

function build(messages: AnyMsg[], ctx: Ctx, firstUnread?: string): Item[] {
  const out: Item[] = [];
  const rows = groupedSends(messages);
  messages = rows.map(row => row.m);
  const who = messages.map((m) => (ev(m) ? null : whoWrote(m, ctx)));
  // Compare adjacent runs using one local-day conversion per message. Parsing
  // the same timestamps in both neighbours and the day divider blocked input
  // on larger retained histories. Keep local timezone/DST semantics unchanged.
  const dates = messages.map(m => new Date(m.at));
  const days = dates.map(d => d.toDateString());
  const times = dates.map(d => d.getTime());
  const together = (i: number, j: number) => {
    const a = messages[i], b = messages[j], wa = who[i], wb = who[j];
    return !!wa && !!wb && wa.key === wb.key && days[i] === days[j] && Math.abs(times[j] - times[i]) < RUN && !(a.actions || []).length && b.id !== firstUnread;
  };
  let lastMine = -1;
  messages.forEach((m, i) => { if (who[i]?.mine && !isRequest(m) && !m.deleted) lastMine = i; });
  messages.forEach((m, i) => {
    if (rows[i].compact) return;
    const targets: AnyMsg[] = [m];
    for (let j = i + 1; j < rows.length && rows[j].compact; j++) targets.push(rows[j].m);
    if (i === 0 || days[i - 1] !== days[i]) out.push({ type: "day", key: "d:" + m.at.slice(0, 10) + i, label: dayLabel(m.at) });
    if (targets.some(target => target.id === firstUnread)) out.push({ type: "new", key: "new" });
    const end = i + targets.length - 1;
    out.push({ type: "msg", key: m.id, m, first: !(i > 0 && together(i - 1, i)), last: !(end + 1 < messages.length && together(end, end + 1)), status: i === lastMine, compact: false, ...(targets.length > 1 ? {targets} : {}) });
  });
  return out;
}

function flash(box: HTMLElement, id: string, smooth: boolean) {
  const el = box.querySelector<HTMLElement>("[data-message-ids~=\"" + CSS.escape(id) + "\"]") || box.querySelector<HTMLElement>("[data-mid=\"" + CSS.escape(id) + "\"]");
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
    <div className="my-2 flex items-center gap-2 px-4" role="separator" aria-label="Unread messages">
      <span className="h-px flex-1 bg-danger/50" />
      <span className="text-[12px] font-bold uppercase tracking-wide text-danger">Unread</span>
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

  const busy = new Map<string, { name: string; seed: string; since?: number; what: string; activity?: string; stop?: T.StopAction; stale?: boolean; terminal?: string }>();
  for (const m of messages) {
    const running = working(m, messages, ctx);
    // An old decision snapshot must not hide a newer current execution.
    const stop = m.stop && m.exec?.attempt && m.exec.attempt > m.stop.attempt ? undefined : m.stop;
    const terminal = terminalStopLabel(stop);
    if ((!running && !stop) || (m.actions || []).includes("cancel")) continue; // its approval card shows it, with Stop
    const what = terminal ? "" : localRunningDetail((isThreadMsg(m) ? m.detail : m.job_detail) || "", running);
    const nativeActivity = currentNativeActivity(isThreadMsg(m) ? m.detail : m.job_detail, m.exec, running && !stop?.pending && !terminal);
    if (isThreadMsg(m)) {
      // In a device thread the agent works on what you sent it; what it was sent works on your side.
      const name = m.dir === "out" ? threadAgentName(ctx) : agentName(undefined, ctx.names, ctx.overview?.person, ctx.overview?.person);
      busy.set(stop ? m.id : m.dir, { activity:nativeActivity, stop, terminal, stale:m.exec?.stale, name, seed: m.dir === "out" ? ctx.thread?.peer || m.id : ctx.overview?.me.address || m.id, since: terminal ? undefined : m.exec?.at, what });
      continue;
    }
    const a = agentOf(ctx, m.pid);
    if (a) busy.set(stop ? m.id : a.pid, { activity:nativeActivity, stop, terminal, stale:m.exec?.stale, name: agentLabel(a, ctx), seed: a.agent_id || a.host.address, since: terminal ? undefined : m.exec?.at, what });
  }
  if (!entries.length && !busy.size) return null;
  const names = entries.map((e) => e.label || "Someone");
  return (
    <div className="mt-3 flex flex-col gap-2 px-3 sm:px-4">
      {[...busy.entries()].map(([k, b]) => (
        <div key={k} role="status" className="fade-in ml-10 flex max-w-[min(100%,360px)] items-center gap-3 rounded-xl border-[1.5px] border-dashed border-agent-ink bg-surface px-3 py-2">
          <AgentAvatar seed={b.seed} size={32} mood={b.terminal ? "neutral" : "working"} />
          <div className="min-w-0 leading-tight">
            <span className="flex items-center gap-2 text-[14px] font-bold text-agent-ink">{b.name} {b.terminal ? "— " + b.terminal : b.stop?.result?.refused ? "reported a changed request" : b.stop?.pending ? "— Stop queued" : b.stop?.result ? "is stopping" : b.stale ? "last reported working" : "is working"}{!b.terminal && <Dots />}</span>
            {b.terminal && b.stop?.result?.refused && <p className="text-[12px] text-text-2 [overflow-wrap:anywhere]">{b.stop.result.refused}</p>}
            {b.activity ? <NativeActivity detail={b.activity} className="text-[13px] text-text-2 [overflow-wrap:anywhere]" /> : (b.what || b.since) ? <span className="block truncate text-[13px] text-text-2">{b.what || "since " + timeOf(new Date(b.since! < 1e11 ? b.since! * 1000 : b.since!).toISOString())}</span> : null}
          </div>
          {b.stop && !b.terminal && <StopOwnRequest request={b.stop} />}
        </div>
      ))}
      {entries.length > 0 && (
        <div role="status" data-part="typing" className="fade-in flex items-center gap-2 text-[13px] font-semibold text-text-2">
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
    const t = ctx.dm || ctx.thread;
    if (!t || !ctx.overview?.notify?.enabled) return;
    if (document.visibilityState !== "visible" || !document.hasFocus() || !atBottom.current) return;
    if (store.host.platform === "android" && store.host.android?.visible() === false) return;
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
    const off = store.host.platform === "android" ? store.host.android?.onVisibility(now) : undefined;
    return () => {
      window.removeEventListener("focus", now);
      document.removeEventListener("visibilitychange", now);
      off?.();
    };
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
