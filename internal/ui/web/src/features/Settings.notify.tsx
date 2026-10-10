// Notifications: off until the person turns them on here. An alert says
// only "AgentNet: New activity", never what was written. Each chat can be
// muted from its own menu; chats notify by default once alerts are on.
import { useEffect, useRef, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Card, GroupLabel, Hint, PageHead, Skeleton, Toggle } from "./Settings.parts";

const permission = () => (typeof Notification === "undefined" ? "unsupported" : Notification.permission);

export function notifySummary(n: T.NotifyView | undefined): string {
  if (!n || !n.available) return "Unavailable";
  return n.enabled ? "On" : "Off";
}

export function NotificationsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const head = <PageHead title="Notifications" titleRef={titleRef} lead={<>When someone writes, or an agent you brought in answers, this {store.host.platform !== "daemon" ? "device" : "computer"} shows “AgentNet: New activity”. Never what was written.</>} />;
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
  const blocked = !n.native && permission() === "denied";

  // Turning on asks the browser first, from this click: its own question
  // needs one. A computer's own alerts ask nothing.
  const turn = async (on: boolean) => {
    setBusy(true);
    if (on && store.host.android) {
      try {
        if (!(await store.host.android.requestNotifications()).granted) {
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

  if (!n.available) return <Card className="p-4"><p>{iphone ? "On iPhone, add AgentNet to your Home Screen first, then turn them on there." : n.reason || "Notifications aren’t available here."}</p></Card>;

  return (
    <div className="space-y-6">
      <Card className="p-4">
        <div className="flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <p className="font-semibold">{n.native && !store.host.android ? "Alerts on this computer" : "Notifications on this device"}</p>
            <Hint>{store.host.android ? "Android shows alerts while AgentNet is connected." : n.native ? "Shown while AgentNet runs here, even with this page closed." : "Your browser shows them, even with this page closed."}</Hint>
          </div>
          <Toggle label="Notifications" checked={n.enabled} disabled={busy || (blocked && !n.enabled)} onChange={turn} />
        </div>
        {blocked && <Hint className="mt-3">Notifications are blocked in this browser’s settings. Everything else works without them.</Hint>}
        {n.pending && <Hint className="mt-3">Your server is told about this change when this page reconnects.</Hint>}
      </Card>
      <Hint className="px-1">Chats notify you unless muted. Use Mute in the chat’s menu to keep it quiet.</Hint>
    </div>
  );
}

/** Android connection lifetime is explicit; the notification carries a Stop action. */
function AndroidConnection() {
  const store = useApp(), android = store.host.android;
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  useEffect(() => {
    if (!android) return;
    let alive = true;
    android.connection().then(v => { if (alive) setEnabled(v.enabled); }, () => { if (alive) setError("Could not read Android connection settings."); });
    return () => { alive = false; };
  }, [android]);
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
