// You / Settings. Phones: the You tab, a grouped list that opens one
// section at a time. Desktop: the list in the side column and the open
// section in the main pane (SettingsPane), side by side. Another screen
// opens a section by name (store.showTab("settings", "assistant")).
// Everything shown is what the server says; every change goes through the
// host's API.
import {
  IconBell, IconChevronLeft, IconChevronRight, IconDatabase, IconDevices, IconInfoCircle, IconPalette, IconRobot, IconShieldCheck, IconStack2,
} from "@tabler/icons-react";
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import type { T } from "../api";
import { useApp, useWide } from "../context";
import { niceDevice } from "../model";
import { useStore } from "../store";
import { PersonAvatar } from "../ui/Avatar";
import { AppearanceSection, applyTheme, savedTheme, themeWords, useTheme, type Theme } from "./Settings.appearance";
import { AssistantSection, harnessName, PermissionsSection } from "./Settings.assistant";
import { NotificationsSection, notifySummary } from "./Settings.notify";
import { GroupLabel, Tile } from "./Settings.parts";
import { DeviceGlyph, DevicesSection, devicesOf, pendingLinks, ProfileSection } from "./Settings.profile";
import { AboutSection, StorageSection, WorkspacesSection } from "./Settings.system";
import { workspaceLabel } from "./WorkspaceSwitcher";

/** applySavedTheme applies the theme remembered in this browser; call it once at startup. */
export function applySavedTheme() { applyTheme(savedTheme()); }

type Id = "profile" | RowId;
type RowId = "devices" | "assistant" | "permissions" | "notifications" | "appearance" | "workspaces" | "storage" | "about";

const VIEWS: Record<Id, (p: { titleRef?: React.Ref<HTMLHeadingElement> }) => ReactNode> = {
  profile: ProfileSection, devices: DevicesSection, assistant: AssistantSection, permissions: PermissionsSection, notifications: NotificationsSection,
  appearance: AppearanceSection, workspaces: WorkspacesSection, storage: StorageSection, about: AboutSection,
};

// Each row's sticker: pastel tiles with ink icons, in both themes (like avatars).
const ROWS: Record<RowId, { label: string; icon: ReactNode; color: string }> = {
  devices: { label: "Your devices", icon: <IconDevices size={20} />, color: "#A8D8FF" },
  assistant: { label: "Your agent", icon: <IconRobot size={20} />, color: "#D9C2FF" },
  permissions: { label: "Permissions", icon: <IconShieldCheck size={20} />, color: "#B5E3C4" },
  notifications: { label: "Notifications", icon: <IconBell size={20} />, color: "#FFC6E0" },
  appearance: { label: "Appearance", icon: <IconPalette size={20} />, color: "#F6E3A1" },
  workspaces: { label: "Workspaces", icon: <IconStack2 size={20} />, color: "#FFD6A5" },
  storage: { label: "Storage", icon: <IconDatabase size={20} />, color: "#C7F0E8" },
  about: { label: "About", icon: <IconInfoCircle size={20} />, color: "#DCD6EE" },
};

const GROUPS: { label: string; ids: RowId[] }[] = [
  { label: "Your setup", ids: ["devices", "assistant", "permissions"] },
  { label: "This app", ids: ["notifications", "appearance", "workspaces"] },
  { label: "Behind the scenes", ids: ["storage", "about"] },
];

// The section open on desktop is shared by the list (side column) and the
// pane (main column). panes counts mounted panes: with none, the list
// opens sections in place, as on a phone. asked counts sections opened from
// another screen: the pane then moves focus to the section's title.
const desk = { id: "profile" as Id, panes: 0, asked: 0, subs: new Set<() => void>() };
const deskSet = (patch: Partial<Pick<typeof desk, "id" | "panes" | "asked">>) => { Object.assign(desk, patch); for (const f of desk.subs) f(); };
const deskSub = (f: () => void) => { desk.subs.add(f); return () => { desk.subs.delete(f); }; };
const useDesk = () => useSyncExternalStore(deskSub, () => desk.id);
const usePanes = () => useSyncExternalStore(deskSub, () => desk.panes);
const useAsked = () => useSyncExternalStore(deskSub, () => desk.asked);

const isId = (s: string): s is Id => s in VIEWS;

/** Settings is the list of sections (phone: with each section opening over it). */
export function Settings() {
  const store = useApp();
  const wide = useWide();
  const panes = usePanes();
  const selected = useDesk();
  const asked = useStore(store, (s) => s.section);
  const [open, setOpen] = useState<Id | null>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const rows = useRef<Partial<Record<Id, HTMLButtonElement | null>>>({});
  const back = useRef<Id | null>(null);
  const split = wide && panes > 0;

  // A section named from elsewhere opens once; the request is then cleared,
  // so asking for the same section again opens it again.
  useEffect(() => {
    if (!asked) return;
    if (isId(asked)) { deskSet({ id: asked, asked: desk.asked + 1 }); setOpen(asked); }
    store.showTab("settings");
  }, [asked]);

  // Opening a section moves focus to its title; going back returns it to its row.
  useEffect(() => {
    if (open) title.current?.focus();
    else if (back.current) { rows.current[back.current]?.focus(); back.current = null; }
  }, [open]);

  if (split) return <List selected={selected} onPick={(id) => deskSet({ id })} rows={rows.current} title="Settings" desktop />;
  if (open) {
    const View = VIEWS[open];
    return (
      <div className="fade-in min-h-full">
        <div className="sticky top-0 z-10 border-b-[1.5px] border-outline bg-canvas px-2 py-1.5 lg:border-b">
          <button type="button" onClick={() => { back.current = open; setOpen(null); }} className="inline-flex min-h-11 items-center gap-0.5 rounded-full pl-1 pr-3 font-semibold hover:bg-sunken">
            <IconChevronLeft size={22} aria-hidden="true" />{wide ? "Settings" : "You"}
          </button>
        </div>
        <div className="@container mx-auto max-w-xl px-4 pb-10 pt-4"><View titleRef={title} /></div>
      </div>
    );
  }
  return <List onPick={setOpen} rows={rows.current} title={wide ? "Settings" : "You"} />;
}

/** SettingsPane is the open section, for the desktop's main column. */
export function SettingsPane() {
  const id = useDesk();
  const asked = useAsked();
  const title = useRef<HTMLHeadingElement>(null);
  useEffect(() => { deskSet({ panes: desk.panes + 1 }); return () => deskSet({ panes: desk.panes - 1 }); }, []);
  useEffect(() => { if (asked) title.current?.focus(); }, [asked]);
  const View = VIEWS[id];
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div key={id} className="@container fade-in mx-auto w-full max-w-[720px] px-8 pb-16 pt-8"><View titleRef={title} /></div>
    </div>
  );
}

function List({ selected, onPick, rows, title, desktop }: { selected?: Id; onPick: (id: Id) => void; rows: Partial<Record<Id, HTMLButtonElement | null>>; title: string; desktop?: boolean }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const requests = pendingLinks(o);
  return (
    <div className={desktop ? "px-3 pb-8 pt-5" : "mx-auto max-w-xl px-4 pb-10 pt-5"}>
      <h1 className="mb-5 px-1 font-display text-[28px] font-extrabold leading-tight">{title}</h1>
      {requests.length > 0 && (
        <button type="button" onClick={() => onPick("devices")}
          className="press mb-4 flex w-full items-center gap-3 rounded-2xl stroke bg-act p-3 text-left text-act-ink shadow-pop">
          <Tile color="#FFFFFF" size={44}><DeviceGlyph name={requests[0].name} size={24} /></Tile>
          <span className="min-w-0 flex-1">
            <span className="block text-[12px] font-bold uppercase tracking-[0.08em]">Needs your OK</span>
            <span className="line-clamp-2 block font-semibold leading-snug">{requests.length === 1 ? "“" + requests[0].name + "” asks to join as you" : requests.length + " devices ask to join as you"}</span>
          </span>
          <span className="grid size-10 shrink-0 place-items-center rounded-full bg-[#1B1530] text-white"><IconChevronRight size={20} aria-hidden="true" /></span>
        </button>
      )}
      <ProfileRow o={o} selected={selected === "profile"} onPick={() => onPick("profile")} rowRef={(el) => { rows.profile = el; }} desktop={desktop} />
      {GROUPS.map((g) => (
        <section key={g.label} aria-label={g.label} className="mt-5">
          <GroupLabel>{g.label}</GroupLabel>
          <div className={desktop ? "space-y-1" : "overflow-hidden rounded-2xl stroke bg-surface"}>
            {g.ids.map((id, i) => (
              <Row key={id} id={id} o={o} selected={selected === id} desktop={desktop} first={i === 0} onPick={() => onPick(id)} rowRef={(el) => { rows[id] = el; }} />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function ProfileRow({ o, selected, onPick, rowRef, desktop }: { o: T.Overview | null; selected: boolean; onPick: () => void; rowRef: (el: HTMLButtonElement | null) => void; desktop?: boolean }) {
  const p = o?.person;
  const devices = p ? devicesOf(p) : [];
  const sub = !o ? "" : p ? (devices.length === 1 ? "On 1 device" : "On " + devices.length + " devices · " + devices.map((d) => niceDevice(d.name)).join(", "))
    : o.link?.state === "pending" ? "Waiting for your other device" : o.role === "service" ? "A service, with no person" : "Set up your person";
  return (
    <button ref={rowRef} type="button" onClick={onPick} aria-current={selected ? "page" : undefined}
      className={"flex w-full items-center gap-4 rounded-2xl stroke p-4 text-left " + (selected ? "bg-surface shadow-pop-sm" : desktop ? "bg-surface/60 hover:bg-surface" : "bg-surface active:bg-sunken")}>
      <PersonAvatar name={p?.label || "You"} seed={p?.person || o?.me.address || "me"} size={56} />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-display text-[22px] font-bold leading-tight">{p ? p.label : "You"}</span>
        <span className={"block truncate text-[13px] " + (p || !o ? "text-muted" : "font-semibold text-agent-ink")}>{sub}</span>
      </span>
      <IconChevronRight size={20} className="shrink-0 text-muted" aria-hidden="true" />
    </button>
  );
}

function summary(id: RowId, o: T.Overview | null, workspace: string, theme: Theme): ReactNode {
  if (!o) return null;
  switch (id) {
    case "devices": {
      const n = o.person ? devicesOf(o.person).length : 0;
      const asks = pendingLinks(o).length;
      if (asks) return <span className="rounded-full bg-act px-2 py-0.5 text-[12px] font-bold text-act-ink stroke tnum">{asks} new</span>;
      return n ? String(n) : null;
    }
    case "assistant": return o.me.responder ? harnessName(o.me.responder) : "You answer";
    case "notifications": return notifySummary(o.notify);
    case "appearance": return themeWords[theme];
    case "workspaces": return workspace;
    case "about": return o.release ? <span className="rounded-full bg-agent px-2 py-0.5 text-[12px] font-bold text-agent-ink">Update</span> : o.version;
    default: return null;
  }
}

function Row({ id, o, selected, desktop, first, onPick, rowRef }: { id: RowId; o: T.Overview | null; selected: boolean; desktop?: boolean; first: boolean; onPick: () => void; rowRef: (el: HTMLButtonElement | null) => void }) {
  const store = useApp();
  const theme = useTheme();
  const s = ROWS[id];
  const value = summary(id, o, workspaceLabel(store.host.workspace), theme);
  const look = desktop
    ? "rounded-xl border-[1px] " + (selected ? "border-outline bg-surface" : "border-transparent hover:bg-sunken")
    : (first ? "" : "border-t border-hairline ") + "active:bg-sunken";
  return (
    <button ref={rowRef} type="button" onClick={onPick} aria-current={selected ? "page" : undefined}
      className={"flex min-h-14 w-full items-center gap-3 px-3 py-2 text-left " + look}>
      <Tile color={s.color}>{s.icon}</Tile>
      <span className="min-w-0 flex-1 truncate font-semibold">{s.label}</span>
      {value != null && <span className="max-w-[45%] shrink-0 truncate text-[13px] text-muted tnum">{value}</span>}
      <IconChevronRight size={18} className="shrink-0 text-muted" aria-hidden="true" />
    </button>
  );
}
