// Connection state and short confirmations. Losing the stream is said in
// words, with a way to try again; nothing retries on a timer beyond the
// store's bounded recovery.
import { useEffect, useState } from "react";
import { useApp } from "../context";
import { errorText, type T } from "../api";
import { unsentIn, useStore } from "../store";
import { Button } from "../ui/Button";

export function ConnectionBanner() {
  const store = useApp();
  const conn = useStore(store, (s) => s.conn);
  const newVersion = useStore(store, (s) => s.newVersion);
  const required = useStore(store, (s) => s.overview?.update_required);
  const app = useStore(store, (s) => !!s.overview?.app);
  const unsent = useStore(store, unsentIn);
  // A browser page whose server now serves a newer AgentNet reloads to get
  // it, by itself once nothing unsent would be lost (sends wait in this
  // browser meanwhile; drafts stay until sent or cleared).
  const browser = store.host.platform === "browser";
  useEffect(() => { if (browser && newVersion && !unsent) location.reload(); }, [browser, newVersion, unsent]);
  if (required) return <UpdateRequired required={required} app={app} reload={browser && !!newVersion} unsent={unsent} />;
  if (newVersion) return (
    <div role="status" className="flex flex-wrap items-center gap-3 border-b-[1.5px] border-outline bg-act px-4 py-2 text-act-ink">
      <span className="flex-1 text-sm font-semibold">AgentNet was updated to {newVersion}.{browser && unsent && " Send or clear your drafts here: the page then reloads with it."}</span>
      <Button size="sm" variant="outline" onClick={() => location.reload()} disabled={unsent} title={unsent ? "Send or clear your drafts first" : undefined}>Reload</Button>
    </div>
  );
  if (conn === "live" || conn === "loading") return null;
  const text = conn === "updating" ? "AgentNet is restarting with its update…" : conn === "gone" ? "AgentNet did not come back at this address." : "Connection lost. Messages you send wait here.";
  return (
    <div role="status" className="flex items-center gap-3 border-b-[1.5px] border-outline bg-guest-bg px-4 py-2 text-guest-ink">
      <span className="flex-1 text-sm font-semibold">{text}</span>
      {conn !== "updating" && <Button size="sm" variant="outline" onClick={() => store.retryNow()}>Try again</Button>}
    </div>
  );
}

// UpdateRequired: the server serves this device again only once it runs a
// newer AgentNet. It stays on top until then. The app offers its own
// update; a browser page reloads to get its server's version (reload) once
// nothing unsent would be lost.
function UpdateRequired({ required, app, reload, unsent }: { required: T.UpdateRequiredView; app: boolean; reload: boolean; unsent: boolean }) {
  const store = useApp(), host = store.host;
  const [busy, setBusy] = useState(false), [note, setNote] = useState("");
  const browser = host.platform === "browser";
  const canUpdate = app && !browser && !!host.appUpdate;
  const update = async () => {
    if (busy || !host.appUpdate) return;
    setBusy(true); setNote("");
    try { const r = await host.appUpdate(); if (store.isActive()) setNote(r.message); }
    catch (e) { if (store.isActive()) setNote(errorText(e)); }
    finally { if (store.isActive()) setBusy(false); }
  };
  const how = host.platform === "android" ? "Install the newer AgentNet APK over this app to keep your chats and identity. Do not uninstall it." : required.auto || (reload && unsent ? "Send or clear your drafts here: the page then reloads with your server's current version. What you send waits in this browser until then."
    : browser ? "This page reloads with your server's current version." : canUpdate ? "" : "Run agentnet update on this computer.");
  return (
    <div role="alert" className="flex flex-wrap items-center gap-3 border-b-[1.5px] border-outline bg-danger-bg px-4 py-3 text-danger">
      <div className="min-w-0 flex-1 space-y-0.5">
        <p className="font-display text-[18px] font-extrabold leading-tight">{required.latest ? `Update AgentNet to ${required.latest} to continue` : "Update AgentNet to continue"}</p>
        <p className="text-sm">Your server holds this device's messages until it runs that version.{how && " " + how}</p>
        {note && <p role="status" className="text-sm">{note}</p>}
      </div>
      {canUpdate && <Button size="sm" variant="act" disabled={busy} onClick={() => void update()}>{busy ? "Updating…" : "Update now"}</Button>}
      {reload && <Button size="sm" variant="outline" onClick={() => location.reload()} disabled={unsent} title={unsent ? "Send or clear your drafts first" : undefined}>Reload</Button>}
    </div>
  );
}

export function Toasts() {
  const store = useApp();
  const toasts = useStore(store, (s) => s.toasts);
  return (
    <div aria-live="polite" className="pointer-events-none fixed inset-x-0 bottom-20 z-50 flex flex-col items-center gap-2 px-4 lg:bottom-6">
      {toasts.map((t) => (
        <div key={t.id} role={t.tone === "error" ? "alert" : "status"}
          className={"pop-in pointer-events-auto max-w-md rounded-2xl stroke px-4 py-2.5 text-sm font-semibold shadow-pop " + (t.tone === "error" ? "bg-danger-bg text-danger" : t.tone === "ok" ? "bg-ok-bg text-ok-ink" : "bg-surface text-ink")}>
          {t.text}
        </div>
      ))}
    </div>
  );
}
