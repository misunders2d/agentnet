// Notifications: off until the person turns them on here. An alert says
// only "AgentNet: New activity", never what was written. Each chat can be
// muted, and a person who started a chat with you alerts you only once
// you allow them.
import { IconBellOff } from "@tabler/icons-react";
import { useState } from "react";
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
  if (!n) return <>{head}<Card className="p-4"><p>Notifications aren’t available in this AgentNet.</p></Card></>;
  return <>{head}<NotifyBody n={n} dms={(o.dms || []).filter((d) => d.kind !== "group" && !!d.peer.person)} /></>;
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

// Chats is one switch per person chat: on when that chat may alert you
// (not muted, and its person allowed). Turning one on allows the person
// too; turning it off mutes just that chat.
function Chats({ n, dms }: { n: T.NotifyView; dms: T.DMSummary[] }) {
  const store = useApp();
  const [busy, setBusy] = useState("");
  const mutes = n.mutes || [], allowed = n.allowed || [];
  const set = async (d: T.DMSummary, on: boolean) => {
    setBusy(d.id);
    const name = personName(d.peer);
    if (!on) await store.run((a) => a.notify("mute", { conv: d.id, muted: true }), "Chat with " + name + " is muted.");
    else {
      if (mutes.includes(d.id)) await store.run((a) => a.notify("mute", { conv: d.id, muted: false }));
      if (!allowed.includes(d.peer.address)) await store.run((a) => a.notify("allow", { person: d.peer.person, allowed: true }));
      store.toast("Chat with " + name + " can alert you.", "ok");
    }
    setBusy("");
  };
  if (!dms.length) return <Hint className="px-1">Once you chat with someone, you can mute that chat here.</Hint>;
  return (
    <section aria-labelledby="notify-chats">
      <GroupLabel id="notify-chats">Chats that can alert you</GroupLabel>
      <Card className="divide-y divide-hairline">
        {dms.map((d) => {
          const muted = mutes.includes(d.id), ok = allowed.includes(d.peer.address);
          const name = personName(d.peer);
          return (
            <div key={d.id} className="flex min-h-16 items-center gap-3 px-4 py-2.5">
              <PersonAvatar name={name} seed={d.peer.person || d.peer.address} size={40} />
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">{name}</p>
                <p className="flex items-center gap-1 text-[13px] text-muted">
                  {muted ? <><IconBellOff size={14} aria-hidden="true" />Muted</> : ok ? "Alerts you" : "Off until you allow " + name}
                </p>
              </div>
              <Toggle label={"Alerts from the chat with " + name} checked={!muted && ok} disabled={busy === d.id} onChange={(on) => set(d, on)} />
            </div>
          );
        })}
      </Card>
      <Hint className="mt-2 px-1">People you started a chat with can alert you. Someone who started one with you can alert you only after you turn it on here.</Hint>
    </section>
  );
}
