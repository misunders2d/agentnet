// The messenger's frame: a rail (desktop) or bottom tabs (phone), the chat
// list, the open conversation and, when it has guests or a pending OK, the
// "In this chat" panel. Phones show one of these at a time: a conversation
// is a card pushed over the tabs. How each change moves: ui/Motion.tsx.
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { IconMessageCircle, IconRobot, IconCircleCheck, IconSettings } from "@tabler/icons-react";
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
import { Toasts, ConnectionBanner } from "./features/Status";
import { PersonAvatar } from "./ui/Avatar";

const tabs: { id: Tab; label: string; icon: typeof IconMessageCircle }[] = [
  { id: "chats", label: "Chats", icon: IconMessageCircle },
  { id: "agents", label: "Agents", icon: IconRobot },
  { id: "oks", label: "OKs", icon: IconCircleCheck },
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

  const main = tab === "agents" ? <AgentsView /> : tab === "oks" ? <OksView /> : tab === "settings" ? <Settings /> : <ChatList />;
  // Phone: an open conversation is a card pushed over the tabs from the
  // right; Back pulls it away. The tabs stay drawn under it only while it
  // moves. Another topic of the same agent is the same card; another
  // conversation (a notification, a link) is a new card pushed over the
  // one that was open, which stays under it until it has arrived.
  const card = usePresence(wide ? null : open, MOTION.pop);
  const cardKey = card.shown ? convKey(card.shown, views) : "";
  const below = useBelow(card.shown, cardKey, card.leaving);
  const arrived = useSettled(card.shown && !card.leaving ? cardKey : "", MOTION.push);
  const listScroll = useRef(0);

  if (!wide) {
    const under = !card.shown || card.leaving || (!arrived && !below);
    // Cards are keyed by conversation and the one below comes first, so the
    // card that was open keeps its elements (and scroll) as it goes under.
    return (
      <div className="relative h-full overflow-hidden bg-canvas">
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
        <Toasts />
      </div>
    );
  }

  return <Desktop tab={tab} oks={oks} overview={overview} store={store} main={main} />;
}

/** Card: a phone's open conversation, over the tabs. */
function Card({ o, leaving, className }: { o: NonNullable<Open>; leaving: boolean; className: string }) {
  return (
    <div inert={leaving} aria-hidden={leaving || undefined} data-card className={"absolute inset-0 flex flex-col bg-canvas " + className}>
      <Leaving.Provider value={leaving}><ConnectionBanner /><Conversation o={o} /></Leaving.Provider>
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
const order: Record<Tab, number> = { chats: 0, agents: 1, oks: 2, settings: 3 };
function usePages(tab: Tab): { enter: string; leave: string } {
  const [st, setSt] = useState({ tab, next: true });
  if (st.tab !== tab) setSt({ tab, next: order[tab] > order[st.tab] });
  return st.next ? { enter: "an-page-in-next", leave: "an-page-out-next" } : { enter: "an-page-in-prev", leave: "an-page-out-prev" };
}

function PhoneTabs({ tab, oks, overview, store, main, scroll }: { tab: Tab; oks: number; overview: T.Overview | null; store: Store; main: ReactNode; scroll: RefObject<number> }) {
  // The tab's own scroll position comes back with it (it was unmounted under the open conversation).
  const box = useRef<HTMLDivElement>(null);
  const pages = usePages(tab);
  useLayoutEffect(() => { if (box.current && tab === "chats") box.current.scrollTop = scroll.current; }, []);
  return (
      <>
        <ConnectionBanner />
        <Swap id={tab} className="min-h-0 flex-1 overflow-hidden" side="h-full" {...pages} ms={MOTION.page} label="tab">
          <div ref={box} className="h-full overflow-y-auto" onScroll={(e) => { if (tab === "chats") scroll.current = e.currentTarget.scrollTop; }}>{main}</div>
        </Swap>
        <nav aria-label="Main" className="grid grid-cols-4 border-t-[1.5px] border-outline bg-surface pb-[env(safe-area-inset-bottom)]">
          {tabs.map((t) => (
            <button key={t.id} type="button" onClick={() => store.showTab(t.id)} aria-current={tab === t.id ? "page" : undefined}
              className={"relative flex min-h-14 flex-col items-center justify-center gap-0.5 text-[12px] font-semibold " + (tab === t.id ? "text-ink" : "text-muted")}>
              <span className={"grid h-7 w-12 place-items-center rounded-full " + (tab === t.id ? "bg-act text-act-ink stroke" : "")}><t.icon size={20} stroke={2} /></span>
              {t.label}
              {t.id === "oks" && oks > 0 && <span className="absolute right-[22%] top-1 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{oks}</span>}
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

function Desktop({ tab, oks, overview, store, main }: { tab: Tab; oks: number; overview: T.Overview | null; store: Store; main: ReactNode }) {
  const open = useStore(store, (s) => s.open);
  const views = useStore(store, (s) => s.views);
  const settings = tab === "settings";
  const pages = usePages(tab);
  // The right pane moves only when what it shows changes: another
  // conversation, or Settings. Topics of one agent are one pane.
  const pane = settings ? "settings" : open ? convKey(open, views) : "none";
  return (
    <div className="grid h-full grid-cols-[76px_minmax(300px,360px)_1fr] bg-canvas">
      <nav aria-label="Main" className="flex flex-col items-center gap-2 border-r-[1.5px] border-outline bg-[#1B1530] py-3 text-white">
        <WorkspaceCoin />
        {tabs.map((t) => (
          <button key={t.id} type="button" onClick={() => store.showTab(t.id)} aria-current={tab === t.id ? "page" : undefined}
            className={"relative flex w-[60px] flex-col items-center gap-1 rounded-2xl py-2 text-[12px] font-semibold " + (tab === t.id ? "bg-act text-act-ink" : "text-white/80 hover:bg-white/10")}>
            <t.icon size={22} stroke={2} />{t.label}
            {t.id === "oks" && oks > 0 && <span className="absolute right-1 top-1 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{oks}</span>}
          </button>
        ))}
        <div className="flex-1" />
        <button type="button" onClick={() => store.showTab("settings")} aria-current={tab === "settings" ? "page" : undefined}
          className={"flex w-[60px] flex-col items-center gap-1 rounded-2xl py-2 text-[12px] font-semibold " + (tab === "settings" ? "bg-act text-act-ink" : "text-white/80 hover:bg-white/10")}>
          <IconSettings size={22} stroke={2} />Settings
        </button>
        <button type="button" aria-label="You: profile and devices" onClick={() => store.showTab("settings", "profile")} className="grid size-11 place-items-center rounded-full"><PersonAvatar name={overview?.person?.label || "Me"} seed={overview?.person?.person || "me"} size={40} /></button>
      </nav>
      <aside className="min-h-0 border-r-[1.5px] border-outline bg-canvas">
        <Swap id={tab} className="h-full overflow-hidden" side="h-full overflow-y-auto" {...pages} ms={MOTION.page} label="list">{main}</Swap>
      </aside>
      <main className="flex min-h-0 min-w-0">
        <div className="flex min-w-0 flex-1 flex-col">
          <ConnectionBanner />
          <Swap id={pane} className="flex min-h-0 flex-1 flex-col" side="flex min-h-0 flex-1 flex-col" enter="an-pane-in" leave="an-pane-out" ms={MOTION.pane} label="pane">
            {settings ? <SettingsPane /> : open ? <Conversation o={open} /> : <NothingOpen />}
          </Swap>
        </div>
        {!settings && <RoomPanel />}
      </main>
      <Toasts />
    </div>
  );
}
