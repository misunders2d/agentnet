// Notifications: off until the person turns them on here. An alert says
// only "AgentNet: New activity", never what was written. Each chat can be
// muted, and a person who started a chat with you alerts you only once
// you allow them.
import { useEffect, useRef, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { personName } from "../model";
import { useStore } from "../store";
import { PersonAvatar } from "../ui/Avatar";
import { Card, GroupLabel, Hint, PageHead, Skeleton, Toggle } from "./Settings.parts";

const permission = () => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

export function notifySummary(n: T.NotifyView | undefined): string {
  if (!n || !n.available) return "Unavailable";
  return n.enabled ? "On" : "Off";
}

export function NotificationsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const head = <PageHead title="Notifications" titleRef={titleRef} lead={<>When someone writes, or an agent you brought in answers, this {store.host.platform === "browser" ? "device" : "computer"} shows “AgentNet: New activity”. Never what was written.</>} />;
  if (!o) return <>{head}<Skeleton /></>;
  const n = o.notify;
  if (!n) return <>{head}<Card className="p-4"><p>Notifications aren’t available in this AgentNet.</p></Card><Typing /></>;
  return <>{head}<NotifyBody n={n} dms={(o.dms || []).filter((d) => d.kind !== "group" && !!d.peer.person)} /><Typing /></>;
}

// Typing (MEL-528): whether this device tells people when you're typing,
// and shows when they are. Kept on this device only; it never carries
// what is typed, and an agent's work is shown on its request, apart.
function Typing() {
  const store = useApp();
  const seq = useStore(store, (s) => s.overview?.seq);
  const [prefs, setPrefs] = useState<T.TypingPreferences | null>(null);
  const [state, setState] = useState<"" | "busy" | "saved" | "failed" | "unavailable">("");
  const saving = useRef(false); // a read that lands while a change is saved doesn't undo it on screen
  useEffect(() => {
    let alive = true;
    store.api.typing({}).then((v) => { if (alive && !saving.current) setPrefs(v.preferences); }, () => { if (alive && !saving.current) setState("unavailable"); });
    return () => { alive = false; };
  }, [seq]);
  const set = async (next: T.TypingPreferences) => {
    const before = prefs;
    saving.current = true;
    setPrefs(next);
    setState("busy");
    try {
      await store.api.typingPreferences(next);
      setState("saved");
    } catch {
      setPrefs(before);
      setState("failed");
    }
    saving.current = false;
  };
  if (state === "unavailable") return null;
  return (
    <section aria-labelledby="notify-typing" className="mt-6">
      <GroupLabel id="notify-typing">Typing</GroupLabel>
      <Card className="divide-y divide-hairline">
        {([["send", "Share when I’m typing", "People in the chat see “typing…” while you write. Never what you write."],
          ["show", "Show when people are typing", "You see “typing…” when someone in the chat is writing."]] as const).map(([k, label, hint]) => (
          <div key={k} className="flex min-h-16 items-center gap-3 px-4 py-2.5">
            <div className="min-w-0 flex-1">
              <p className="font-semibold">{label}</p>
              <Hint>{hint}</Hint>
            </div>
            <Toggle label={label} checked={!!prefs?.[k]} disabled={!prefs || state === "busy"} onChange={(on) => prefs && void set({ ...prefs, [k]: on })} />
          </div>
        ))}
      </Card>
      <Hint className="mt-2 px-1" >
        <span role="status">{state === "saved" ? "Saved on this device. " : state === "failed" ? "Not saved. Try again. " : ""}</span>
        For this device in this workspace. Your agents’ work shows on each request, apart from this.
      </Hint>
    </section>
  );
}

function NotifyBody({ n, dms }: { n: T.NotifyView; dms: T.DMSummary[] }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const iphone = !n.native && /iPhone|iPad/.test(navigator.userAgent) && !matchMedia("(display-mode: standalone)").matches;
  const blocked = !n.native && permission() === "denied";

  // Turning on asks the browser first, from this click: its own question
  // needs one. A computer's own alerts ask nothing.
  const turn = async (on: boolean) => {
    setBusy(true);
    if (on && !n.native) {
      const p = typeof Notification === "undefined" ? "denied" : await Notification.requestPermission();
      if (p !== "granted") { store.toast("The browser didn’t allow notifications. Everything else works without them.", "error"); setBusy(false); return; }
    }
    await store.run((a) => a.notify(on ? "enable" : "disable"), on ? "Notifications are on." : "Notifications are off.");
    setBusy(false);
  };

  if (!n.available) return <Card className="p-4"><p>{iphone ? "On iPhone, add AgentNet to your Home Screen first, then turn them on there." : n.reason || "Notifications aren’t available here."}</p></Card>;

  return (
    <div className="space-y-6">
      <Card className="p-4">
        <div className="flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <p className="font-semibold">{n.native ? "Alerts on this computer" : "Notifications on this device"}</p>
            <Hint>{n.native ? "Shown while AgentNet runs here, even with this page closed." : "Your browser shows them, even with this page closed."}</Hint>
          </div>
          <Toggle label="Notifications" checked={n.enabled} disabled={busy || (blocked && !n.enabled)} onChange={turn} />
        </div>
        {blocked && <Hint className="mt-3">Notifications are blocked in this browser’s settings. Everything else works without them.</Hint>}
        {n.pending && <Hint className="mt-3">Your server is told about this change when this page reconnects.</Hint>}
      </Card>
      {n.enabled && <Chats n={n} dms={dms} />}
    </div>
  );
}

// Person permission and per-conversation mutes are separate choices. Opening
// this page preserves both; changing a person grant never clears a mute.
export function notificationPeople(dms: T.DMSummary[], allowed: string[]) {
  const people = new Map<string, { peer: T.PersonView; conversations: T.DMSummary[]; allowed: boolean }>();
  for (const d of dms) {
    if (d.kind === "group" || !d.peer.person) continue;
    let p = people.get(d.peer.person);
    if (!p) { p = { peer: d.peer, conversations: [], allowed: false }; people.set(d.peer.person, p); }
    p.conversations.push(d);
    p.allowed ||= [d.peer.address, ...(d.peer.devices || []).map(x => x.address)].some(a => allowed.includes(a));
  }
  return [...people.values()];
}

function Chats({ n, dms }: { n: T.NotifyView; dms: T.DMSummary[] }) {
  const store = useApp();
  const [busy, setBusy] = useState("");
  const mutes = n.mutes || [];
  const people = notificationPeople(dms, n.allowed || []);
  const change = async (key: string, body: { person: string; allowed: boolean } | { conv: string; muted: boolean }) => {
    setBusy(key);
    await store.run(a => a.notify("person" in body ? "allow" : "mute", body));
    setBusy("");
  };
  if (!people.length) return <Hint className="px-1">People you chat with will appear here.</Hint>;
  return (
    <section aria-labelledby="notify-chats">
      <GroupLabel id="notify-chats">People who can alert you</GroupLabel>
      <Card className="divide-y divide-hairline">
        {people.map(p => {
          const name = personName(p.peer), id = p.peer.person!;
          const mutedCount = p.conversations.filter(d => mutes.includes(d.id)).length;
          return <div key={id} className="px-4 py-3">
            <div className="flex min-h-12 items-center gap-3">
              <PersonAvatar name={name} seed={id} size={40} />
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">{name}</p>
                <p className="text-[13px] text-muted">{p.allowed ? "Allowed to alert you" : "Alerts from this person are off"}{mutedCount ? ` · ${mutedCount} muted` : ""}</p>
              </div>
              <Toggle label={"Allow alerts from " + name} checked={p.allowed} disabled={!!busy || p.peer.state !== "pinned"} onChange={allowed => void change(id, { person: id, allowed })} />
            </div>
            <details className="mt-2">
              <summary className="cursor-pointer py-2 text-sm font-semibold text-text-2">Conversation overrides ({p.conversations.length})</summary>
              <p className="pb-2 text-sm text-muted">A muted conversation stays quiet even when this person is allowed.</p>
              {p.conversations.map(d => <div key={d.id} className="flex min-h-12 items-center gap-3 border-t border-hairline py-2">
                <div className="min-w-0 flex-1"><p className="truncate text-sm font-semibold">{d.title || "Main conversation"}</p><p className="text-xs text-muted">Started {new Date(d.created).toLocaleString()}</p></div>
                <Toggle label={"Mute conversation " + (d.title || "started " + d.created)} checked={mutes.includes(d.id)} disabled={!!busy} onChange={muted => void change(d.id, { conv: d.id, muted })} />
              </div>)}
            </details>
          </div>;
        })}
      </Card>
      <Hint className="mt-2 px-1">These choices apply on this device. Turning a person on or off preserves your conversation overrides.</Hint>
    </section>
  );
}
