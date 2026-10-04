// Standing permissions: who your agent answers or works for without asking
// you. Read from each device conversation's own record (approved,
// task_grant), so only devices your agent has talked with show; each can
// be turned off here. Shown in Agents and in Settings → Permissions.
import { useEffect, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { capital, deviceWords } from "./Approvals.words";

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

/** useGrants reads each peer's newest device conversation once per change (null while reading). */
export function useGrants(): Grant[] | null {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [grants, setGrants] = useState<Grant[] | null>(null);
  const browser = store.host.platform === "browser";
  const peers = latestThreads(o);
  useEffect(() => {
    if (browser || !o) return;
    let alive = true;
    Promise.all(peers.map((t) => store.api.thread(t.id).then((x) => ({ peer: t.peer, thread: t.id, approved: x.approved, tasks: x.task_grant }), () => null)))
      .then((rows) => { if (alive) setGrants(rows.filter((r): r is Grant => !!r)); });
    return () => { alive = false; };
  }, [o?.seq, browser]);
  return grants;
}

export function Permissions({ grants }: { grants: Grant[] | null }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  if (!grants) return <p className="px-1 text-[14px] text-muted" aria-busy="true">Checking…</p>;
  const answers = grants.filter((g) => g.approved);
  const tasks = grants.filter((g) => g.tasks);
  const turnOff = (g: Grant, what: "unapprove" | "revoke_tasks") =>
    store.run((api) => api.act({ do: what, id: g.peer }), what === "unapprove" ? "Their questions wait for you again." : "Their tasks wait for you again.");
  return (
    <article className="rounded-2xl bg-surface p-4 stroke">
      <Grants title="Answers questions automatically from" empty="Nobody: every question waits for your OK." what="automatic answers"
        rows={answers.map((g) => ({ key: g.peer, name: capital(deviceWords(g.peer, o)), note: "", off: () => turnOff(g, "unapprove") }))} />
      <div className="my-3 border-t-2 border-dashed border-ink/15" />
      <Grants title="Does tasks without asking from" empty="Nobody: every task waits for your OK." what="tasks without asking"
        rows={tasks.map((g) => ({ key: g.peer, name: capital(deviceWords(g.peer, o)), note: g.tasks === "active" ? "" : "Paused: " + g.tasks, off: () => turnOff(g, "revoke_tasks") }))} />
      <p className="pt-3 text-[13px] text-muted">In chats, people can ask an agent you let in. Its tasks wait for your OK unless you said otherwise when it joined.</p>
    </article>
  );
}

function Grants({ title, empty, what, rows }: { title: string; empty: string; what: string; rows: { key: string; name: string; note: string; off: () => void }[] }) {
  return (
    <div>
      <p className="font-bold">{title}</p>
      {!rows.length ? <p className="pt-1 text-[14px] text-text-2">{empty}</p> : (
        <ul>
          {rows.map((r) => (
            <li key={r.key} className="flex min-h-12 items-center gap-3">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-semibold">{r.name}</span>
                {r.note && <span className="block text-[13px] text-guest-ink">{r.note}</span>}
              </span>
              <Button size="sm" variant="ghost" className="underline decoration-ink/30 underline-offset-4" onClick={r.off} aria-label={"Turn off " + what + " for " + r.name}>Turn off</Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
