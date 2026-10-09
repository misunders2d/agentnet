// Standing permissions: who your agent answers or works for without asking
// you. Read saved person and advanced device grants directly, including
// people with no recent conversation and frozen grants that can be revoked.
// Shown in Agents and in Settings → Permissions.
import { useEffect, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore, type Store } from "../store";
import { Button } from "../ui/Button";
import { capital, deviceWords } from "./Approvals.words";
import { DeviceGrantConfirm, type GrantChange } from "./Trust";

export interface Grant { peer: string; approved: boolean; tasks: string; person?: string; label?: string; questionStatus?: string; nativeTasks?: string }

/** The newest device conversation with each other device. */
export function latestThreads(o: T.Overview | null) {
  const out = new Map<string, T.ThreadSummary>();
  for (const t of [...(o?.threads || []), ...(o?.topics || []).map((c) => c.latest)]) { // an agent whose topics are all archived: its latest
    const had = out.get(t.peer);
    if (!had || (t.last_at || "") > (had.last_at || "")) out.set(t.peer, t);
  }
  return [...out.values()];
}

// The last grants read per store: a screen showing them again draws them at
// once (nothing moves in) while they are read again.
const lastGrants = new WeakMap<object, Grant[]>();

function readGrants(store: Store, o: T.Overview): Promise<Grant[]> {
  return store.api.approvals().then(v => {
    const rows = new Map<string, Grant>();
    for (const q of v.questions || []) {
      const key = q.person || q.address;
      if (!key) continue;
      rows.set(key, { peer:key, approved:true, tasks:"", person:q.person, label:q.label, questionStatus:q.status });
    }
    for (const t of v.tasks || []) {
      const key=t.person || t.address, had=rows.get(key);
      rows.set(key, { ...had, peer:key, approved:had?.approved || false, tasks:t.status, person:t.person, label:t.label });
    }
    for (const t of v.native_tasks || []) {
      const had = rows.get(t.address);
      rows.set(t.address, { ...had, peer:t.address, approved:had?.approved || false, tasks:had?.tasks || "", nativeTasks:t.status });
    }
    // This exact verified person is the setting's target, even before any
    // device has sent a task. A display name or address prefix is never one.
    const own = o.person;
    if (own?.person && own.state === "self" && !rows.has(own.person)) rows.set(own.person, { peer:own.person, person:own.person, label:own.label, approved:false, tasks:"" });
    const personOf = new Map<string, string>();
    for (const p of [...(o.people || []), ...(own ? [own] : [])]) for (const d of p.devices || []) if (p.person) personOf.set(d.address, p.person);
    for (const t of latestThreads(o)) {
      const person = personOf.get(t.peer);
      if (!rows.has(t.peer) && (!own?.person || person !== own.person) && !(person && rows.get(person)?.approved)) rows.set(t.peer, { peer: t.peer, approved: false, tasks: "" });
    }
    return [...rows.values()];
  });
}

/** warmGrants reads the grants once ahead of the first screen that shows them. */
export function warmGrants(store: Store) {
  const o = store.get().overview;
  if (store.host.platform === "browser" || !o || lastGrants.has(store)) return;
  void readGrants(store, o).then((g) => { if (!lastGrants.has(store)) lastGrants.set(store, g); }).catch(() => {});
}

/** useGrants reads saved standing grants once per change (null while first reading). */
export function useGrants(): Grant[] | null {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [grants, setGrants] = useState<Grant[] | null>(() => lastGrants.get(store) ?? null);
  const browser = store.host.platform === "browser";
  useEffect(() => {
    if (browser || !o) return;
    let alive = true;
    void readGrants(store, o).then((g) => { if (alive) { lastGrants.set(store, g); setGrants(g); } }).catch(() => { if (alive) setGrants(null); });
    return () => { alive = false; };
  }, [store, o?.seq, browser]);
  return grants;
}

export function Permissions({ grants }: { grants: Grant[] | null }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  // Each change says what it means first (the device thread's menu asks the same way).
  const [change, setChange] = useState<{ what: GrantChange; g: Grant } | null>(null);
  if (store.host.platform === "browser") return <p className="px-1 text-[14px] text-muted">Set automatic task permissions in AgentNet on the computer that receives and runs the tasks.</p>;
  if (!grants) return <p className="px-1 text-[14px] text-muted" aria-busy="true">Checking…</p>;
  const own = grants.find((g) => g.person && g.person === o?.person?.person);
  const answers = grants.filter((g) => g.approved && g !== own);
  const others = grants.filter((g) => !g.approved && g !== own && !g.nativeTasks); // explicit device grants remain separate
  const tasks = grants.filter((g) => g.tasks);
  const name = (g: Grant) => g === own ? "My devices" : g.person ? g.label || "Verified person" : capital(deviceWords(g.peer, o));
  const native = grants.filter((g) => g.nativeTasks);
  const host = o ? deviceWords(o.me.address, o) : "this computer";
  return (
    <article className="rounded-2xl bg-surface p-4 stroke">
      <p className="pb-3 text-[13px] text-muted">Permissions on {host}. Set other receiving computers separately; this does not change them.</p>
      {own && <>
        <h3 className="font-bold">My devices</h3>
        <p className="pt-1 text-[13px] text-text-2">All your verified devices, including phones and devices you link later. Removed devices, changed keys and frozen identities stay blocked.</p>
        <Grants title="" empty="" what="automatic answers" rows={[{ key:own.peer, name:"My devices", note:own.approved ? "Automatic question permission: " + (own.questionStatus && own.questionStatus !== "active" ? own.questionStatus : "on") : "Automatic question permission: off", on:own.approved, run:() => setChange({what:own.approved ? "unapprove" : "approve", g:own}) }]} />
        <Grants title="" empty="" what="tasks without asking" rows={[{ key:own.peer, name:"My devices", note:own.tasks ? (own.tasks === "active" ? "Automatic tasks for all my devices: on" : "Automatic tasks for all my devices: " + own.tasks) : "Automatic tasks for all my devices: off", on:!!own.tasks, run:() => setChange({what:own.tasks ? "revoke_tasks" : "grant_tasks", g:own}) }]} />
        <p className="pt-1 text-[13px] text-muted">Uses this computer’s normal agent permissions. Separate device permissions and tasks already accepted remain if you turn this off.</p>
        <div className="my-3 border-t-2 border-dashed border-ink/15" />
      </>}
      <Grants title="Answers questions automatically from" empty="No other standing question permissions." what="automatic answers"
        rows={answers.map((g) => ({ key: g.peer, name: name(g), note: g.person ? (g.questionStatus === "active" ? "All current and future verified devices" : "Paused: " + g.questionStatus) : "This device only", on: true, run: () => setChange({ what: "unapprove", g }) }))} />
      {others.length > 0 && (
        <Grants title="Their questions wait for you" empty="" what="automatic answers"
          rows={others.map((g) => ({ key: g.peer, name: name(g), note: "", on: false, run: () => setChange({ what: "approve", g }) }))} />
      )}
      <div className="my-3 border-t-2 border-dashed border-ink/15" />
      <Grants title="Other standing task permissions" empty="No other person or device task grants." what="tasks without asking"
        rows={tasks.filter((g) => g !== own).map((g) => ({ key: g.peer, name: name(g), note: g.tasks === "active" ? (g.person ? "All current and future verified devices" : "This device only") : "Paused: " + g.tasks, on: true, run: () => setChange({ what: "revoke_tasks", g }) }))} />
      {native.length > 0 && <div className="pt-3"><p className="font-bold">Separate device permissions</p><ul>{native.map((g) => <li key={g.peer} className="pt-2 text-[14px]"><span className="font-semibold">{name(g)}</span><span className="block text-[13px] text-muted">{g.nativeTasks === "active" ? "Direct tasks allowed" : "Direct tasks paused: " + g.nativeTasks}. Managed through this computer’s device trust.</span></li>)}</ul></div>}
      <p className="pt-3 text-[13px] text-muted">In chats, an agent’s accepted invitation may also allow tasks from particular devices. Those permissions and one-time task approvals are separate.</p>
      <DeviceGrantConfirm change={change?.what || null} onClose={() => setChange(null)} peer={change?.g.peer || ""} thread={{ task_grant: change?.g.tasks }} />
    </article>
  );
}

function Grants({ title, empty, what, rows }: { title: string; empty: string; what: string; rows: { key: string; name: string; note: string; on: boolean; run: () => void }[] }) {
  return (
    <div className="[&+&]:mt-3">
      <p className="font-bold">{title}</p>
      {!rows.length ? <p className="pt-1 text-[14px] text-text-2">{empty}</p> : (
        <ul>
          {rows.map((r) => (
            <li key={r.key} className="flex min-h-12 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-semibold">{r.name}</span>
                {r.note && <span className="block text-[13px] text-guest-ink">{r.note}</span>}
              </span>
              <Button size="sm" variant="ghost" className="underline decoration-ink/30 underline-offset-4" onClick={r.run} aria-label={(r.on ? "Turn off " : "Turn on ") + what + " for " + r.name}>{r.on ? "Turn off" : "Turn on"}</Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
