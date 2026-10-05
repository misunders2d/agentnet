// Standing permissions: who your agent answers or works for without asking
// you. Read from each device conversation's own record (approved,
// task_grant), so only devices your agent has talked with show; each can
// be turned off here. Shown in Agents and in Settings → Permissions.
import { useEffect, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore, type Store } from "../store";
import { Button } from "../ui/Button";
import { capital, deviceWords } from "./Approvals.words";
import { DeviceGrantConfirm, type GrantChange } from "./Trust";

export interface Grant { peer: string; thread: string; approved: boolean; tasks: string }

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
  return Promise.all(latestThreads(o).map((t) => store.api.thread(t.id).then((x) => ({ peer: t.peer, thread: t.id, approved: x.approved, tasks: x.task_grant }), () => null)))
    .then((rows) => rows.filter((r): r is Grant => !!r));
}

/** warmGrants reads the grants once ahead of the first screen that shows them. */
export function warmGrants(store: Store) {
  const o = store.get().overview;
  if (store.host.platform === "browser" || !o || lastGrants.has(store)) return;
  void readGrants(store, o).then((g) => { if (!lastGrants.has(store)) lastGrants.set(store, g); });
}

/** useGrants reads each peer's newest device conversation once per change (null while first reading). */
export function useGrants(): Grant[] | null {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [grants, setGrants] = useState<Grant[] | null>(() => lastGrants.get(store) ?? null);
  const browser = store.host.platform === "browser";
  useEffect(() => {
    if (browser || !o) return;
    let alive = true;
    void readGrants(store, o).then((g) => { lastGrants.set(store, g); if (alive) setGrants(g); });
    return () => { alive = false; };
  }, [o?.seq, browser]);
  return grants;
}

export function Permissions({ grants }: { grants: Grant[] | null }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  // Each change says what it means first (the device thread's menu asks the same way).
  const [change, setChange] = useState<{ what: GrantChange; g: Grant } | null>(null);
  if (!grants) return <p className="px-1 text-[14px] text-muted" aria-busy="true">Checking…</p>;
  const answers = grants.filter((g) => g.approved);
  const others = grants.filter((g) => !g.approved); // known devices whose questions wait: answered automatically once turned on, before they ask
  const tasks = grants.filter((g) => g.tasks);
  const name = (g: Grant) => capital(deviceWords(g.peer, o));
  return (
    <article className="rounded-2xl bg-surface p-4 stroke">
      <Grants title="Answers questions automatically from" empty="Nobody: every question waits for your OK." what="automatic answers"
        rows={answers.map((g) => ({ key: g.peer, name: name(g), note: "", on: true, run: () => setChange({ what: "unapprove", g }) }))} />
      {others.length > 0 && (
        <Grants title="Their questions wait for you" empty="" what="automatic answers"
          rows={others.map((g) => ({ key: g.peer, name: name(g), note: "", on: false, run: () => setChange({ what: "approve", g }) }))} />
      )}
      <div className="my-3 border-t-2 border-dashed border-ink/15" />
      <Grants title="Does tasks without asking from" empty="Nobody: every task waits for your OK." what="tasks without asking"
        rows={tasks.map((g) => ({ key: g.peer, name: name(g), note: g.tasks === "active" ? "" : "Paused: " + g.tasks, on: true, run: () => setChange({ what: "revoke_tasks", g }) }))} />
      <p className="pt-3 text-[13px] text-muted">In chats, people can ask an agent you let in. Its tasks wait for your OK unless you said otherwise when it joined.</p>
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
