// The messenger's frame: a rail (desktop) or bottom tabs (phone), the chat
// list, the open conversation and, when it has guests or a pending OK, the
// "In this chat" panel. Phones show one of these at a time.
import { IconMessageCircle, IconRobot, IconCircleCheck, IconSettings } from "@tabler/icons-react";
import { useEffect } from "react";
import { useApp, useWide } from "./context";
import { useStore, type Tab } from "./store";
import { ChatList } from "./features/ChatList";
import { Conversation } from "./features/Conversation";
import { RoomPanel } from "./features/RoomPanel";
import { OksView, useNeedsYou } from "./features/Approvals";
import { AgentsView } from "./features/AgentsView";
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

  useEffect(() => { document.documentElement.lang = navigator.language || "en"; }, []);

  const main = tab === "agents" ? <AgentsView /> : tab === "oks" ? <OksView /> : tab === "settings" ? <Settings /> : <ChatList />;

  if (!wide) {
    // Phone: one screen at a time. An open conversation covers the tabs.
    if (open) return (<div className="flex h-dvh flex-col bg-canvas"><ConnectionBanner /><Conversation /><Toasts /></div>);
    return (
      <div className="flex h-dvh flex-col bg-canvas">
        <ConnectionBanner />
        <div className="min-h-0 flex-1 overflow-y-auto">{main}</div>
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
        <Toasts />
      </div>
    );
  }

  return (
    <div className="grid h-dvh grid-cols-[76px_minmax(300px,360px)_1fr] bg-canvas">
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
      <aside className="min-h-0 overflow-y-auto border-r-[1.5px] border-outline bg-canvas">{main}</aside>
      <main className="flex min-h-0 min-w-0">
        <div className="flex min-w-0 flex-1 flex-col"><ConnectionBanner />{tab === "settings" ? <SettingsPane /> : <Conversation />}</div>
        {tab !== "settings" && <RoomPanel />}
      </main>
      <Toasts />
    </div>
  );
}
