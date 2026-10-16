// Notifications: off until the person turns them on here. An alert says
// locally selected native text; closed-page browser push stays generic. Each chat can be
// muted from its own menu; chats notify by default once alerts are on.
import { useEffect, useRef, useState } from "react";
import type { T } from "../api";
import type { AndroidNotificationStatus } from "../host";
import { useApp } from "../context";
import { useStore } from "../store";
import { Card, GroupLabel, Hint, PageHead, Skeleton, Toggle } from "./Settings.parts";

const permission = () => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

export function notifySummary(n: T.NotifyView | undefined): string {
  if (!n || !n.available) return "Unavailable";
  return n.enabled ? "On" : "Off";
}

// Read Android's current grant and channel state on visibility changes; never
// change the OS permission while merely rendering settings.
function useNativeNotificationStatus() {
  const android = useApp().host.android;
  const [status, setStatus] = useState<AndroidNotificationStatus | null>(null);
  useEffect(() => {
    if (!android) return;
    let alive = true;
    const refresh = () => { void android.notificationStatus().then(v => { if (alive) setStatus(v); }, () => { if (alive) setStatus(null); }); };
    refresh();
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    const off = android.onVisibility(refresh);
    return () => { alive = false; window.removeEventListener("focus", refresh); document.removeEventListener("visibilitychange", refresh); off(); };
  }, [android]);
  return [android ? status : undefined, setStatus] as const;
}

export function NotificationSummary({ n }: { n: T.NotifyView | undefined }) {
  const [status] = useNativeNotificationStatus();
  if (n?.enabled && status !== undefined) return <>{status === null ? "Checking…" : status.granted ? "On" : "Blocked by Android"}</>;
  return <>{notifySummary(n)}</>;
}

export function NotificationsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const head = <PageHead title="Notifications" titleRef={titleRef} lead={<>When someone writes, or an agent you brought in answers, this device can notify you. Your operating system controls sound, vibration and lock-screen visibility.</>} />;
  if (!o) return <>{head}<Skeleton /></>;
  const n = o.notify;
  if (!n) return <>{head}<Card className="p-4"><p>Notifications aren’t available in this AgentNet.</p></Card><AndroidConnection /><Typing /></>;
  return <>{head}<NotifyBody n={n} /><AndroidConnection /><Typing /></>;
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

function NotifyBody({ n }: { n: T.NotifyView }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const iphone = !n.native && /iPhone|iPad/.test(navigator.userAgent) && !matchMedia("(display-mode: standalone)").matches;
  const android = store.host.android;
  const [nativeStatus, setNativeStatus] = useNativeNotificationStatus();
  const nativeGranted = nativeStatus?.granted ?? (android ? null : undefined);
  const blocked = android ? nativeGranted === false : !n.native && permission() === "denied";
  const channel = nativeStatus?.channel;
  const channelQuiet = channel && channel.importance > 0 && (channel.importance < 3 || !channel.sound || !channel.vibration);
  const openAndroidSettings = async () => {
    if (!android?.notificationSettings) return;
    try { await android.notificationSettings(); }
    catch { store.toast("Android notification settings could not open. Open AgentNet’s Chat activity channel in Android settings.", "error"); }
  };

  // Turning on asks the browser first, from this click: its own question
  // needs one. A computer's own alerts ask nothing.
  const turn = async (on: boolean) => {
    setBusy(true);
    if (on && store.host.android) {
      try {
        const status = await store.host.android.requestNotifications();
        setNativeStatus(status);
        const { granted } = status;
        if (!granted) {
          store.toast("Allow notifications in Android settings to receive alerts.", "error");
          setBusy(false); return;
        }
      } catch { store.toast("Android could not enable notifications. Try again.", "error"); setBusy(false); return; }
    }
    if (on && !n.native) {
      const p = typeof Notification === "undefined" ? "denied" : await Notification.requestPermission();
      if (p !== "granted") { store.toast("The browser didn’t allow notifications. Everything else works without them.", "error"); setBusy(false); return; }
    }
    await store.run((a) => a.notify(on ? "enable" : "disable"), on ? "Notifications are on." : "Notifications are off.");
    setBusy(false);
  };

  const preview = async (value: "" | "full" | "sender" | "hidden") => {
    setBusy(true);
    await store.run((a) => a.notify("preview", { preview: value }), "Message preview preference saved.");
    setBusy(false);
  };

  if (!n.available) return <Card className="p-4"><p>{iphone ? "On iPhone, add AgentNet to your Home Screen first, then turn them on there." : n.reason || "Notifications aren’t available here."}</p></Card>;

  return (
    <div className="space-y-6">
      <Card className="p-4">
        <div className="flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <p className="font-semibold">{n.native && !store.host.android ? "Alerts on this computer" : "Notifications on this device"}</p>
            <Hint>{store.host.android ? "Android shows alerts while AgentNet is connected. The first time you allow notifications, it also keeps AgentNet connected in the background. Your saved connection choice is respected." : n.native ? "Shown while AgentNet runs here, even with this page closed." : "Your browser shows them, even with this page closed."}</Hint>
          </div>
          <Toggle label="Notifications" checked={n.enabled && !blocked} disabled={busy || (!!android && nativeGranted === null) || (!android && blocked && !n.enabled)} onChange={turn} />
        </div>
        {blocked && <Hint className="mt-3">{android && nativeStatus?.app_granted && channel?.importance === 0 ? "Chat activity notifications are blocked in Android settings." : `Notifications are blocked in ${android ? "Android settings" : "this browser’s settings"}.`} Everything else works without them.</Hint>}
        {android && channelQuiet && <Hint className="mt-3">
          {channel.importance < 3 ? "Chat activity is set to silent in Android. " : !channel.sound ? "Chat activity sound is off in Android. " : ""}
          {!channel.vibration ? "Chat activity vibration is off in Android. " : ""}
          Keep this choice or change it in Android notification settings. Android’s sound mode and Do Not Disturb also control alerts.
        </Hint>}
        {android?.notificationSettings && <button type="button" className="mt-3 rounded-lg border border-hairline px-3 py-2 font-semibold" onClick={() => void openAndroidSettings()}>Android notification settings</button>}
        {n.pending && <Hint className="mt-3">Your server is told about this change when this page reconnects.</Hint>}
      </Card>
      <NotificationPreviewControl n={n} busy={busy} onChange={value => void preview(value)} />
      <Hint className="px-1">Chats notify you unless muted. Use Mute in the chat’s menu to keep it quiet.</Hint>
    </div>
  );
}

/** Pure presentation: privacy changes never request permission or enable alerts. */
export function NotificationPreviewControl({ n, busy, onChange }: {
  n: T.NotifyView; busy: boolean; onChange: (value: "" | "full" | "sender" | "hidden") => void;
}) {
  if (!n.native) return <p className="px-1 text-[13px] text-muted">Browser notifications while the page is closed always hide message content.</p>;
  const names: Record<string, string> = { full: "Full message preview", sender: "Sender only", hidden: "Hidden" };
  if (!n.preview_default || !names[n.preview_default]) return null;
  return <section className="rounded-xl border border-hairline p-4">
    <label htmlFor="notification-preview" className="block font-semibold">Message previews</label>
    <select id="notification-preview" className="mt-2 min-h-11 w-full rounded-lg border border-hairline bg-surface px-3" value={n.preview || ""} disabled={busy}
      onChange={e => onChange(e.currentTarget.value as "" | "full" | "sender" | "hidden")}>
      <option value="">Default ({names[n.preview_default]})</option>
      <option value="full">Full message preview</option>
      <option value="sender">Sender only</option>
      <option value="hidden">Hidden</option>
    </select>
    <p className="mt-2 text-[13px] text-muted">Full shows the sender and current message text. Sender hides the text. Hidden omits names and message text, while keeping fixed activity, reminder, decision or security notices. This choice stays on this device in this workspace and does not turn notifications on.</p>
    <p className="mt-2 text-[13px] text-muted">Previews may appear on a lock screen or remain in notification history. Your operating system controls that visibility. On macOS, clicking the current native banner does not open AgentNet.</p>
  </section>;
}

/** Android connection lifetime is explicit; the notification carries a Stop action. */
function AndroidConnection() {
  const store = useApp(), android = store.host.android;
  const notificationsEnabled = useStore(store, s => s.overview?.notify?.enabled);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  useEffect(() => {
    if (!android) return;
    let alive = true;
    const refresh = () => { void android.connection().then(v => { if (alive) { setEnabled(v.enabled); setError(""); } }, () => { if (alive) setError("Could not read Android connection settings."); }); };
    refresh();
    const off = android.onVisibility(refresh);
    return () => { alive = false; off(); };
  }, [android, notificationsEnabled]);
  if (!android) return null;
  const turn = async (on: boolean) => {
    setBusy(true); setError("");
    try { setEnabled((await android.setConnection(on)).enabled); }
    catch (e) { setError(e instanceof Error ? e.message : "Could not change the background connection."); }
    finally { setBusy(false); }
  };
  return <section className="mt-6"><GroupLabel>Connection</GroupLabel><Card className="p-4">
    <div className="flex items-center gap-3"><div className="min-w-0 flex-1">
      <p className="font-semibold">Stay connected in the background</p>
      <Hint>Keep receiving while you use other apps. Android shows a connection notification with a Stop button.</Hint>
    </div><Toggle label="Background connection" checked={!!enabled} disabled={busy || enabled === null} onChange={turn} /></div>
    {error && <p role="alert" className="mt-2 text-danger">{error}</p>}
  </Card></section>;
}
