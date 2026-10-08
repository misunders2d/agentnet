// Install on a computer, then use the existing one-use device link to keep
// the same person. Creating a link never approves the new device.
import { useEffect, useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";

export function BrowserAppSetup() {
  const store = useApp();
  const [app, setApp] = useState<T.GetAppView | null>(null);
  const [link, setLink] = useState<T.DeviceLink | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let alive = true;
    store.api.getApp().then(v => { if (alive && store.isActive()) setApp(v); }, e => { if (alive) setError(errorText(e)); });
    return () => { alive = false; };
  }, [store]);
  const make = async () => {
    setBusy(true); setError("");
    try { const v = await store.api.deviceLink(); if (store.isActive()) setLink(v); }
    catch (e) { if (store.isActive()) setError(errorText(e)); }
    finally { setBusy(false); }
  };
  const mine = app?.platforms?.find(p => p.id === app.detected);
  const others = app?.platforms?.filter(p => p !== mine) || [];
  const style = "inline-flex min-h-11 items-center rounded-full stroke bg-surface px-4 font-semibold underline underline-offset-2";
  return <section aria-label="Connect an agent on your computer" className="mt-3 space-y-3">
    <p className="text-[15px] text-text-2">Install AgentNet on the computer where your coding agent runs. Add it to your existing person, then choose your agent and working folder in Settings → Your agent.</p>
    {mine && <a className={style} href={mine.url} rel="noopener">Get AgentNet for {mine.label}</a>}
    {others.length > 0 && <details><summary className="cursor-pointer font-semibold">{mine ? "Other systems" : "Choose your computer"}</summary><ul>{others.map(p => <li key={p.id}><a className={style} href={p.url} rel="noopener">{p.label}</a></li>)}</ul></details>}
    <Button variant="act" disabled={busy} onClick={make}>{busy ? "Making a link…" : link ? "Make a new app link" : "Link the installed app to me"}</Button>
    {link && <div className="space-y-2">
      {link.app_url && <a className={style} href={link.app_url}>Open this link in AgentNet</a>}
      <p className="text-[14px] text-text-2">Return here to approve the new computer in Settings → Your devices. Your chats stay with the same person. This one-use link expires at {new Date(link.expires).toLocaleTimeString()}.</p>
      <details><summary className="cursor-pointer font-semibold">App opened without the link?</summary><p>Copy this link and paste it into the installed app:</p><code className="block select-all break-all text-[13px]">{link.url}</code></details>
    </div>}
    {error && <p role="alert" className="text-danger">{error} Try again by reopening Your agent.</p>}
  </section>;
}
