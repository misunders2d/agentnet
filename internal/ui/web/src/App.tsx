// The messenger's frame: a rail (desktop) or bottom tabs (phone), the chat
// list, the open conversation and, when it has guests or a pending OK, the
// "In this chat" panel. Phones show one of these at a time: a conversation
// is a card pushed over the tabs. How each change moves: ui/Motion.tsx.
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode, type RefObject } from "react";
import { IconMessageCircle, IconRobot, IconCircleCheck, IconSettings, IconInbox } from "@tabler/icons-react";
import type { T } from "./api";
import { useApp, useWide } from "./context";
import { useStore, type Open, type Store, type Tab } from "./store";
import { ChatList } from "./features/ChatList";
import { Conversation, NothingOpen, convKey } from "./features/Conversation";
import { Leaving, MOTION, Swap, usePresence, useSettled } from "./ui/Motion";
import { RoomPanel } from "./features/RoomPanel";
import { OksView, useNeedsYou } from "./features/Approvals";
import { AgentsView, warmAgentData } from "./features/AgentsView";
import { Settings, SettingsPane } from "./features/Settings";
import { WorkspaceCoin } from "./features/WorkspaceSwitcher";
import { ConnectionBanner } from "./features/Status";
import { PersonAvatar } from "./ui/Avatar";
import { NativeRecovery } from "./features/NativeRecovery";
import { Outbox } from "./features/Outbox";
import { outboxAttention } from "./outbox-attention";
import { skin } from "./skin/brand";

const tabs: { id: Tab; label: string; icon: typeof IconMessageCircle }[] = [
  { id: "chats", label: "Chats", icon: IconMessageCircle },
  { id: "agents", label: "Agents", icon: IconRobot },
  { id: "oks", label: "Review", icon: IconCircleCheck },
  { id: "outbox", label: "Outbox", icon: IconInbox },
];

export function App() {
  const store = useApp();
  const wide = useWide();
  const tab = useStore(store, (s) => s.tab);
  const open = useStore(store, (s) => s.open);
  const overview = useStore(store, (s) => s.overview);
  const oks = useNeedsYou();
  const views = useStore(store, (s) => s.views);

  // What the Agents tab shows is read once, as soon as there is an overview,
  // so even its first visit draws complete.
  const ready = !!overview;
  useEffect(() => { if (ready) warmAgentData(store); }, [ready]);

  const main = tab === "outbox" ? <Outbox /> : tab === "agents" ? <AgentsView /> : tab === "oks" ? <OksView /> : tab === "settings" ? <Settings /> : <ChatList />;
  // Phone: an open conversation is a card pushed over the tabs from the
  // right; Back pulls it away. The tabs stay drawn under it only while it
  // moves. Another topic of the same agent is the same card; another
  // conversation (a notification, a link) is a new card pushed over the
  // one that was open, which stays under it until it has arrived.
  const card = usePresence(wide || tab === "outbox" ? null : open, MOTION.pop);
  const cardKey = card.shown ? convKey(card.shown, views) : "";
  const below = useBelow(card.shown, cardKey, card.leaving);
  const arrived = useSettled(card.shown && !card.leaving ? cardKey : "", MOTION.push);
  const listScroll = useRef(0);

  if (!wide) {
    const under = !card.shown || card.leaving || (!arrived && !below);
    // Cards are keyed by conversation and the one below comes first, so the
    // card that was open keeps its elements (and scroll) as it goes under.
    return (
      <div className="relative h-full overflow-clip bg-canvas">
        {under && (
          <div inert={!!card.shown && !card.leaving} className={"flex h-full flex-col bg-canvas " + (card.leaving ? "an-under-out" : card.shown ? "an-under-in" : "")}>
            <PhoneTabs tab={tab} oks={oks} overview={overview} store={store} main={main} scroll={listScroll} />
          </div>
        )}
        {below && <Card key={below.key} o={below.o} leaving className="an-under-in" />}
        {card.shown && (
          <Card key={cardKey} o={card.shown} leaving={card.leaving}
            className={card.leaving ? "an-push-out an-card-edge" : arrived ? "" : "an-push-in an-card-edge"} />
        )}
      </div>
    );
  }

  return <Desktop tab={tab} oks={oks} overview={overview} store={store} main={main} />;
}

/** Card: a phone's open conversation, over the tabs. */
function Card({ o, leaving, className }: { o: NonNullable<Open>; leaving: boolean; className: string }) {
  return (
    <div inert={leaving} aria-hidden={leaving || undefined} data-card className={"absolute inset-0 flex flex-col bg-canvas " + className}>
      <Leaving.Provider value={leaving}><ConnectionBanner /><NativeRecovery /><Conversation o={o} /></Leaving.Provider>
    </div>
  );
}

/** useBelow: when one open conversation's card replaces another's, the
 *  card that was open, kept under the new one while it pushes in. */
function useBelow(o: Open, key: string, leaving: boolean): { key: string; o: NonNullable<Open> } | null {
  const [st, setSt] = useState<{ key: string; o: Open; below: { key: string; o: NonNullable<Open> } | null }>({ key, o, below: null });
  if (st.key !== key) setSt({ key, o, below: st.key && key && st.o && !leaving ? { key: st.key, o: st.o } : null });
  else if (st.o !== o) setSt({ ...st, o });
  useEffect(() => {
    const b = st.below;
    if (!b) return;
    const t = setTimeout(() => setSt((s) => (s.below === b ? { ...s, below: null } : s)), MOTION.push);
    return () => clearTimeout(t);
  }, [st.below]);
  return st.key === key ? st.below : null;
}

// usePages: the tabs are pages in their order (Chats, Agents, OKs,
// Settings): a later tab slides in from the right, an earlier one from the left.
const order: Record<Tab, number> = { chats: 0, agents: 1, oks: 2, outbox: 3, settings: 4 };
function usePages(tab: Tab): { enter: string; leave: string } {
  const [st, setSt] = useState({ tab, next: true });
  if (st.tab !== tab) setSt({ tab, next: order[tab] > order[st.tab] });
  return st.next ? { enter: "an-page-in-next", leave: "an-page-out-next" } : { enter: "an-page-in-prev", leave: "an-page-out-prev" };
}

function PhoneTabs({ tab, oks, overview, store, main, scroll }: { tab: Tab; oks: number; overview: T.Overview | null; store: Store; main: ReactNode; scroll: RefObject<number> }) {
  const due = outboxAttention(overview).total;
  // The tab's own scroll position comes back with it (it was unmounted under the open conversation).
  const box = useRef<HTMLDivElement>(null);
  const pages = usePages(tab);
  useLayoutEffect(() => { if (box.current && tab === "chats") box.current.scrollTop = scroll.current; }, []);
  return (
      <>
        <ConnectionBanner />
        <NativeRecovery />
        <Swap id={tab} className="min-h-0 flex-1 overflow-clip" side="h-full" {...pages} ms={MOTION.page} label="tab">
          <div ref={box} className="h-full overflow-y-auto" onScroll={(e) => { if (tab === "chats") scroll.current = e.currentTarget.scrollTop; }}>{main}</div>
        </Swap>
        <nav aria-label="Main" data-part="tabs" className="grid grid-cols-5 border-t-[1.5px] border-outline bg-surface pb-[env(safe-area-inset-bottom)]">
          {tabs.map((t) => (
            <button key={t.id} type="button" onClick={() => store.showTab(t.id)} aria-current={tab === t.id ? "page" : undefined}
              aria-label={t.id === "outbox" && due ? "Outbox, "+due+" "+(due === 1 ? "reminder" : "reminders")+" due" : undefined}
              className={"relative flex min-h-14 flex-col items-center justify-center gap-0.5 text-[12px] font-semibold " + (tab === t.id ? "text-ink" : "text-muted")}>
              <span className={"grid h-7 w-12 place-items-center rounded-full " + (tab === t.id ? "bg-act text-act-ink stroke" : "")}><t.icon size={20} stroke={2} /></span>
              {t.label}
              {t.id === "oks" && oks > 0 && <span className="absolute right-[22%] top-1 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{oks}</span>}
              {t.id === "outbox" && due > 0 && <span aria-hidden="true" className="absolute right-[22%] top-1 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{due}</span>}
            </button>
          ))}
          <button type="button" onClick={() => store.showTab("settings")} aria-current={tab === "settings" ? "page" : undefined}
            className={"flex min-h-14 flex-col items-center justify-center gap-0.5 text-[12px] font-semibold " + (tab === "settings" ? "text-ink" : "text-muted")}>
            <PersonAvatar name={overview?.person?.label || "Me"} seed={overview?.person?.person || "me"} size={28} />
            You
          </button>
        </nav>
      </>
  );
}

// The desktop list column (MEL-822/824): its width is the person's, kept in
// this browser under the skin's own storage prefix, between LIST.min and
// LIST.max and never so wide that the conversation gets less than
// LIST.conversation. Clicking the active Chats or Settings rail item hides it
// and shows it again; the column stays mounted while hidden, so its scroll,
// search and the open conversation beside it are kept.
export const LIST = { min: 240, max: 480, initial: 320, conversation: 440, rail: 68 };
const listKey = skin.storage + ".list";
const clampList = (w: number) => Math.round(Math.min(LIST.max, Math.max(LIST.min, Number.isFinite(w) ? w : LIST.initial)));
/** listRoom: the widest the column may be in a window this wide. */
export const listRoom = (windowWidth: number) => Math.max(LIST.min, Math.min(LIST.max, windowWidth - LIST.rail - LIST.conversation));
function savedList(): { width: number; hidden: boolean } {
  try { const v = JSON.parse(localStorage.getItem(listKey) || "{}"); return { width: clampList(Number(v.width) || LIST.initial), hidden: v.hidden === true }; }
  catch { return { width: LIST.initial, hidden: false }; }
}
function saveList(v: { width: number; hidden: boolean }) { try { localStorage.setItem(listKey, JSON.stringify(v)); } catch { /* this browser only */ } }
const listTabs: Tab[] = ["chats", "settings"];

function Desktop({ tab, oks, overview, store, main }: { tab: Tab; oks: number; overview: T.Overview | null; store: Store; main: ReactNode }) {
  const due = outboxAttention(overview).total;
  const open = useStore(store, (s) => s.open);
  const views = useStore(store, (s) => s.views);
  const settings = tab === "settings";
  // Lists use the side column. Setup and review are tasks in the workspace,
  // with room to read and act; the current chat stays remembered in the store.
  const workspace = tab === "agents" || tab === "oks" || tab === "outbox";
  const pages = usePages(tab);
  const [list, setListState] = useState(savedList);
  const setList = (next: { width: number; hidden: boolean }) => { setListState(next); saveList(next); };
  const grid = useRef<HTMLDivElement>(null);
  const hidden = !workspace && list.hidden;
  // Ctrl K (⌘K) finds a chat: a hidden chat list comes back for it.
  useEffect(() => {
    if (!hidden || tab !== "chats") return;
    const on = (e: globalThis.KeyboardEvent) => { if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") setList({ ...list, hidden: false }); };
    window.addEventListener("keydown", on, true);
    return () => window.removeEventListener("keydown", on, true);
  }, [hidden, tab, list]);
  // A rail item opens its section; the active Chats or Settings item hides or shows its list.
  const choose = (id: Tab) => {
    if (id === tab && listTabs.includes(id)) setList({ ...list, hidden: !list.hidden });
    else { if (list.hidden && listTabs.includes(id)) setList({ ...list, hidden: false }); store.showTab(id); }
  };
  const toggleProps = (id: Tab) => tab === id && listTabs.includes(id)
    ? { "aria-expanded": !list.hidden, "aria-controls": "an-list-column", title: (list.hidden ? "Show the " : "Hide the ") + (id === "settings" ? "settings list" : "chat list") } : {};
  // The main pane moves when its workspace or conversation changes.
  // Topics of one agent are one pane.
  const pane = workspace ? tab : settings ? "settings" : open ? convKey(open, views) : "none";
  const columns = workspace ? LIST.rail + "px minmax(0,1fr)"
    : LIST.rail + "px " + (hidden ? "0px" : "clamp(" + LIST.min + "px, var(--an-list-width), max(" + LIST.min + "px, min(" + LIST.max + "px, 100vw - " + (LIST.rail + LIST.conversation) + "px)))") + " minmax(0,1fr)";
  return (
    <div ref={grid} className="grid h-full bg-canvas" style={{ gridTemplateColumns: columns, ["--an-list-width" as string]: list.width + "px" }}>
      <nav aria-label="Main" data-part="rail" className="flex flex-col items-center gap-1.5 border-r-[1.5px] border-outline bg-rail py-3 text-rail-ink">
        <WorkspaceCoin />
        {tabs.map((t) => (
          <button key={t.id} type="button" onClick={() => choose(t.id)} aria-current={tab === t.id ? "page" : undefined} {...toggleProps(t.id)}
            aria-label={t.id === "outbox" && due ? "Outbox, "+due+" "+(due === 1 ? "reminder" : "reminders")+" due" : undefined}
            className={"relative flex w-[58px] flex-col items-center gap-0.5 rounded-2xl py-1.5 text-[12px] font-semibold " + (tab === t.id ? "bg-act text-act-ink" : "text-white/80 hover:bg-white/10")}>
            <t.icon size={22} stroke={2} />{t.label}
            {t.id === "oks" && oks > 0 && <span className="absolute right-0.5 top-0.5 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{oks}</span>}
            {t.id === "outbox" && due > 0 && <span aria-hidden="true" className="absolute right-0.5 top-0.5 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{due}</span>}
          </button>
        ))}
        <div className="flex-1" />
        <button type="button" onClick={() => choose("settings")} aria-current={tab === "settings" ? "page" : undefined} {...toggleProps("settings")}
          className={"flex w-[58px] flex-col items-center gap-0.5 rounded-2xl py-1.5 text-[12px] font-semibold " + (tab === "settings" ? "bg-act text-act-ink" : "text-white/80 hover:bg-white/10")}>
          <IconSettings size={22} stroke={2} />Settings
        </button>
        <button type="button" aria-label="You: profile and devices" onClick={() => { if (list.hidden) setList({ ...list, hidden: false }); store.showTab("settings", "profile"); }} className="grid size-11 place-items-center rounded-full"><PersonAvatar name={overview?.person?.label || "Me"} seed={overview?.person?.person || "me"} size={40} /></button>
      </nav>
      {!workspace && <aside id="an-list-column" data-part="list-panel" data-hidden={hidden ? "" : undefined} inert={hidden} aria-hidden={hidden || undefined}
        className={"relative min-h-0 min-w-0 overflow-hidden bg-[var(--an-sidebar,var(--an-canvas))] " + (hidden ? "" : "border-r-[1.5px] border-outline")}>
        {/* The list keeps its own width while the column is hidden, so nothing inside reflows. */}
        <div className="@container h-full" style={{ width: hidden ? list.width : undefined }}>
          <Swap id={tab} className="h-full overflow-clip" side="h-full overflow-y-auto" {...pages} ms={MOTION.page} label="list">{main}</Swap>
        </div>
        {!hidden && <ListResizer width={list.width} grid={grid} onWidth={(width) => setList({ ...list, width })} />}
      </aside>}
      <main className="flex min-h-0 min-w-0">
        <div className="flex min-w-0 flex-1 flex-col">
          <ConnectionBanner />
          <NativeRecovery />
          <Swap id={pane} className="flex min-h-0 flex-1 flex-col" side="flex min-h-0 flex-1 flex-col" enter="an-pane-in" leave="an-pane-out" ms={MOTION.pane} label="pane">
            {workspace ? (
              <div data-workspace={tab} className="min-h-0 flex-1 overflow-y-auto">
                <div className={tab === "outbox" ? "" : "@container mx-auto w-full max-w-[832px] lg:px-4 lg:pt-3"}>{main}</div>
              </div>
            ) : settings ? <SettingsPane /> : open ? <Conversation o={open} /> : <NothingOpen />}
          </Swap>
        </div>
        {!settings && !workspace && <RoomPanel />}
      </main>
    </div>
  );
}

/** ListResizer: the list column's right edge, Slack-like. Dragging moves only
 *  a CSS variable on the frame (no render per move); the width is kept on
 *  release. Arrow keys move it 16px (Shift: 48px), Home and End go to the
 *  bounds, a double click goes back to the initial width. */
function ListResizer({ width, grid, onWidth }: { width: number; grid: RefObject<HTMLDivElement | null>; onWidth: (w: number) => void }) {
  const drag = useRef<{ x: number; w: number; now: number } | null>(null);
  const room = () => listRoom(typeof window === "undefined" ? 1440 : window.innerWidth);
  const shown = Math.min(width, room());
  const set = (w: number) => onWidth(clampList(Math.min(w, room())));
  const live = (w: number) => grid.current?.style.setProperty("--an-list-width", w + "px");
  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = { x: e.clientX, w: shown, now: shown };
  };
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d) return;
    d.now = Math.min(room(), clampList(d.w + e.clientX - d.x));
    live(d.now);
  };
  const up = () => { const d = drag.current; drag.current = null; if (d && d.now !== width) set(d.now); };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 48 : 16;
    const next = e.key === "ArrowLeft" ? shown - step : e.key === "ArrowRight" ? shown + step : e.key === "Home" ? LIST.min : e.key === "End" ? room() : null;
    if (next === null) return;
    e.preventDefault();
    set(next);
  };
  return (
    <div role="separator" aria-orientation="vertical" aria-label="Resize the list" aria-controls="an-list-column" tabIndex={0} data-part="list-resize"
      aria-valuenow={shown} aria-valuemin={LIST.min} aria-valuemax={room()} title="Drag or use the arrow keys to resize"
      onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up} onLostPointerCapture={up} onKeyDown={key} onDoubleClick={() => set(LIST.initial)}
      className="absolute inset-y-0 right-0 z-10 w-2 cursor-col-resize touch-none outline-none after:absolute after:inset-y-0 after:left-1/2 after:w-0.5 after:-translate-x-1/2 after:bg-transparent after:transition-colors hover:after:bg-agent-ink/60 focus-visible:after:bg-agent-ink" />
  );
}
