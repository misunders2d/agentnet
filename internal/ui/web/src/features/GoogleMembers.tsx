// Workspace admission by Google email. The relay enforces every decision;
// a disabled/non-admin view never offers mutation controls.
import { useLayoutEffect, useRef, type FormEvent } from "react";
import { useApp } from "../context";
import type { T } from "../api";
import { useStore } from "../store";
import { useRetainedText } from "../retained-text";
import { Button } from "../ui/Button";
import { Card, Hint, useLoad } from "./Settings.parts";

export function GoogleMembers() {
  const store = useApp();
  const pending = useStore(store, s => s.googleAccessChange);
  const person = useStore(store, s => s.overview?.person?.person || "");
  const offline = useStore(store, s => s.conn !== "live");
  const r = useLoad(async () => {
    const p = store.get().googleAccessChange;
    const request = p?.saved ? p.request : undefined;
    const value = await store.api.googleAccess();
    if (request) store.confirmGoogleAccess(request, value);
    return value;
  }, [store, person, pending?.saved, pending?.error]);
  const [email, setEmail, retainedEmail] = useRetainedText("google-access-email/" + person, "");
  const [domain, setDomain, retainedDomain] = useRetainedText("google-access-domain/" + person, "");
  const progress = useRef<HTMLElement>(null);
  useLayoutEffect(() => { if (pending) progress.current?.scrollIntoView({ block: "nearest", behavior: "instant" }); }, [pending?.request, pending?.error]);
  const change = (c: T.GoogleAccessChange) => {
    if (pending || offline || !r.data?.can_admin) return;
    const clear = (c.domain ? retainedDomain : retainedEmail).capture();
    void store.api.changeGoogleAccess(c).then(() => { if (!c.remove) clear(); }).catch(() => {});
  };
  const submit = (kind: "email" | "domain") => (e: FormEvent) => {
    e.preventDefault();
    void change({ [kind]: kind === "email" ? email.trim() : domain.trim(), remove: false });
  };
  if (pending) {
    const c = pending.request.change, target = c.domain ? "@" + c.domain : c.email;
    const action = c.remove ? c.domain ? "Block new sign-ins from" : "Remove access for" : c.domain ? "Allow new sign-ins from" : "Invite";
    return <Card className="mt-5 p-4"><section ref={progress} aria-label="Workspace access change" className="space-y-3">
      <h2 className="font-display text-xl font-bold">{pending.error ? "Access change needs review" : pending.saved ? "Access change saved; checking current access" : "Changing workspace access"}</h2>
      <p className="break-words font-semibold">{action} {target}</p>
      <p role={pending.error ? "alert" : "status"}>{pending.error ? "Could not confirm the change. " + pending.error : pending.saved ? "The server accepted this change. Checking the latest access state." : "You can leave this page. Access changes only after the server confirms it."}</p>
      {c.domain && c.remove && <Hint>Existing members keep access. Remove individual emails to end their access.</Hint>}
      {pending.error && <Hint>A response can be lost after saving. Check current access before retrying the exact change.</Hint>}
      {r.error && <p role="alert">Could not check current access: {r.error}</p>}
      {r.data && !r.data.can_admin && <Hint>You no longer have permission to change access here.</Hint>}
      {r.data?.can_admin && (pending.error || pending.saved) && <div className="break-words text-sm" aria-label="Current access">
        {c.domain ? <>Last checked allowed domains: {(r.data.domains || []).join(", ") || "none"}.</> : (() => { const e = r.data.emails?.find(e => e.email === c.email?.trim().toLowerCase()); return "Last checked email status: " + (e ? e.denied ? "blocked" : "allowed" : "no individual access entry") + "."; })()}
      </div>}
      <div className="flex flex-wrap gap-2">
        {(pending.error || pending.saved) && <><Button variant="outline" disabled={r.loading} onClick={r.reload}>{r.loading ? "Checking access…" : "Check current access"}</Button><Button variant="ghost" onClick={() => store.clearGoogleAccess(pending.request)}>Dismiss status</Button></>}
        {pending.error && <Button variant="act" disabled={offline || r.loading || !r.data?.can_admin} onClick={() => { void store.api.changeGoogleAccess(c).catch(() => {}); }}>Retry access change</Button>}
      </div>
    </section></Card>;
  }
  if (!r.data?.can_admin) return null;
  const busy = offline;
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
