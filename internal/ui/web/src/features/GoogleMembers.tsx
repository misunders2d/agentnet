// Workspace admission by Google email. The relay enforces every decision;
// a disabled/non-admin view never offers mutation controls.
import { useState, type FormEvent } from "react";
import { useApp } from "../context";
import type { T } from "../api";
import { Button } from "../ui/Button";
import { Card, Hint, useLoad } from "./Settings.parts";

export function GoogleMembers() {
  const store = useApp();
  const r = useLoad(() => store.api.googleAccess(), []);
  const [email, setEmail] = useState("");
  const [domain, setDomain] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  if (!r.data?.can_admin) return null;
  const change = async (c: T.GoogleAccessChange) => {
    if (busy) return;
    setBusy(true); setMessage("");
    try {
      await store.api.changeGoogleAccess(c);
      setMessage(c.remove ? (c.domain ? "New sign-ins from @" + c.domain + " are blocked. Remove individual emails below to end existing access." : "Access removed.") : r.data?.enabled ? "Access allowed. They can sign in with Google." : "Saved. They can sign in once Google sign-in is set up.");
      r.reload();
    } catch (e) { setMessage(e instanceof Error ? e.message : "Membership could not be changed."); }
    finally { setBusy(false); }
  };
  const submit = (kind: "email" | "domain") => (e: FormEvent) => {
    e.preventDefault();
    void change({ [kind]: kind === "email" ? email.trim() : domain.trim(), remove: false });
  };
  const input = "min-w-0 w-full rounded-xl border border-outline bg-surface px-3 py-2";
  return <Card className="mt-5 space-y-4 p-4">
    <h2 className="font-display text-xl font-bold">Invite people</h2>
    {!r.data.enabled && <Hint>Google sign-in is not set up for this workspace yet. Ask your workspace admin.</Hint>}
    <form onSubmit={submit("email")} className="space-y-2">
      <label htmlFor="invite-google-email" className="block font-semibold">Invite by email</label>
      <input id="invite-google-email" className={input} type="email" required value={email} autoComplete="email" onChange={e => setEmail(e.target.value)} />
      <Button type="submit" disabled={busy}>Invite by email</Button>
    </form>
    <form onSubmit={submit("domain")} className="space-y-2">
      <label htmlFor="invite-google-domain" className="block font-semibold">Allow everyone at @domain</label>
      <input id="invite-google-domain" className={input} required value={domain} placeholder="mellanni.com" onChange={e => setDomain(e.target.value)} />
      <Button type="submit" disabled={busy}>Allow domain</Button>
    </form>
    <Hint>Send people your workspace’s Get AgentNet page. No invitation code needed. Each later device still needs their OK.</Hint>
    <a className="block break-all underline" href={r.data.workspace_url + "/#google-signin"} target="_blank" rel="noopener">Get AgentNet</a>
    <div role="status" className="break-words text-sm">{message}</div>
    {(r.data.emails || []).map(e => <div key={e.email} className="flex flex-wrap items-center justify-between gap-2 border-t border-hairline pt-2">
      <span className="min-w-0 break-all">{e.email}{e.domain_member ? " · joined through a domain" : ""}{e.denied ? " · blocked" : e.admin ? " · admin" : ""}</span>
      <Button size="sm" variant="outline" disabled={busy} onClick={() => void change({ email: e.email, remove: !e.denied })}>{e.denied ? "Invite again" : "Remove access"}</Button>
    </div>)}
    <Hint>Removing a domain blocks new sign-ins. People already signed in stay until you remove their email.</Hint>
    {(r.data.domains || []).map(d => <div key={d} className="flex flex-wrap items-center justify-between gap-2 border-t border-hairline pt-2">
      <span className="break-all">@{d}</span><Button size="sm" variant="outline" disabled={busy} onClick={() => void change({ domain: d, remove: true })}>Remove domain</Button>
    </div>)}
  </Card>;
}
