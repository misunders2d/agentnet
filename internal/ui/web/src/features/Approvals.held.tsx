// Held back (overview.quarantine): messages this device received but could
// not let in, by who sent them and why, in plain words. Their content is
// never shown, and they are not decisions: the OKs count leaves them out.
// One held for a changed identity can be checked and trusted from here on
// a computer, the same way as from the paused chat. One that didn't verify
// proves nothing about who sent it: it only says who it claims to be from.
// Background failures are one line per sending device and cause: how many
// records it sent how often (only where copies are known to repeat one),
// and who can act; every notice stays listed, with how many exact re-sent
// copies the provider folded into it (each still kept, blocked and found by
// its own ID). Several held trust decisions from one device are a plain
// count of messages, each listed in full.
import { useState } from "react";
import { IconShieldQuestion } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { heldAction, heldCopies, heldLast, heldStatus, holdSentence, holdVerified, nameOf, niceDevice, personOf, size, when } from "../model";
import type { Store } from "../store";
import { PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { latestThreads } from "./AgentsView.grants";
import { TrustSheet } from "./Trust";

const background = (code: string) => ["invalid", "proof_pending"].includes(code);
const plural = (n: number, one: string, many: string) => n + " " + (n === 1 ? one : many);

// archiveHeld hides exactly these notices here, every one of them: the
// provider takes at most 256 per request (client.MaxHeldNoticeBatch), so a
// larger group goes in several exact snapshots. A notice that changed or
// arrived since stays visible; nothing is accepted, resent or run.
async function archiveHeld(store: Store, rows: T.QuarantineItem[]) {
  const refs = rows.filter(q => q.can_archive && background(q.code)).map(q => ({ id: q.id, reason: q.code, detail_code: q.detail_code || "" }));
  let sent = 0, archived = 0;
  for (let i = 0; i < refs.length; i += 256) {
    const held = refs.slice(i, i + 256), result = await store.run(api => api.act({ do: "archive_held_batch", held }));
    if (!result) break; // run said why
    sent += held.length;
    archived += Number(/^Archived (\d+) notices/.exec(result.note || "")?.[1] ?? held.length);
  }
  if (sent === refs.length) store.toast("Archived " + plural(archived, "notice", "notices") + " on this device. Changed or newer notices stay visible. Nothing was accepted or run.");
  else if (sent) store.toast("Archived " + archived + " of " + plural(refs.length, "notice", "notices") + " before an error; the rest stay visible. Nothing was accepted or run.", "error");
}

export function HeldBack({ o }: { o: T.Overview }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const [visible, setVisible] = useState(12);
  const items = [...(o.quarantine || [])].sort((a, b) => b.at.localeCompare(a.at));
  const groups = new Map<string, T.QuarantineItem[]>();
  for (const q of items) {
    const key = JSON.stringify([q.code, q.detail_code || "", q.peer]);
    const group = groups.get(key);
    if (group) group.push(q); else groups.set(key, [q]);
  }
  const backgrounds = [...groups].filter(([, rows]) => background(rows[0].code));
  const decisions = [...groups].filter(([, rows]) => !background(rows[0].code));
  const archiveable = items.filter(q => q.can_archive && background(q.code));
  const archive = async (rows: T.QuarantineItem[]) => {
    if (busy) return;
    setBusy(true);
    try { await archiveHeld(store, rows); } finally { setBusy(false); }
  };
  if (!items.length) return null;
  const copies = backgrounds.reduce((n, [, rows]) => n + heldCopies(rows), 0);
  return (
    <section className="px-4 pt-6" aria-labelledby="oks-heldback">
      <h2 id="oks-heldback" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Held back</h2>
      {decisions.length > 0 && <ul className="flex flex-col gap-3 pt-3">{decisions.slice(0, visible).map(([key, rows]) => rows.length === 1 ? <HeldRow key={key} q={rows[0]} o={o} /> : <HeldGroup key={key} rows={rows} o={o} />)}</ul>}
      {backgrounds.length > 0 && <details className="mt-3 rounded-2xl bg-surface p-3.5 stroke">
        <summary className="cursor-pointer font-bold">Chat sync needs attention <span className="font-normal text-text-2">· {plural(backgrounds.length, "problem", "problems")}</span></summary>
        <p className="pt-1 text-[14px] text-text-2">{plural(copies, "held copy", "held copies")} this device couldn’t let in. Each is kept and stays blocked; nothing in them runs. Each line says what it waits for or who can fix it.</p>
        {archiveable.length > 0 && <><Button size="sm" variant="outline" className="mt-2" disabled={busy} onClick={() => void archive(archiveable)}>{busy ? "Archiving…" : archiveable.length === 1 ? "Archive 1 notice" : "Archive all " + archiveable.length + " notices"}</Button><p className="mt-1 text-[13px] text-muted">Hides these notices here; it repairs nothing. Checks continue for pending context; nothing is accepted or run. New or changed notices remain visible.</p></>}
        <ul className="flex flex-col gap-3 pt-3">{backgrounds.slice(0, visible).map(([key, rows]) => <HeldGroup key={key} rows={rows} o={o} busy={busy} onArchive={archive} />)}</ul>
      </details>}
      {Math.max(backgrounds.length, decisions.length) > visible && <Button className="mt-3" size="sm" variant="ghost" onClick={() => setVisible(n => n + 12)}>Show more problems</Button>}
    </section>
  );
}

// HeldGroup is one sending device and cause: a status line, what it means,
// and every notice behind "Show copies" (time, ID, size, record, and its
// folded re-sent copies), or every message behind "Show messages" for a
// group of trust decisions.
function HeldGroup({ rows, o, busy, onArchive }: { rows: T.QuarantineItem[]; o: T.Overview; busy?: boolean; onArchive?: (rows: T.QuarantineItem[]) => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [visible, setVisible] = useState(20);
  const first = rows[0], action = heldAction(first, o);
  const quiet = background(first.code);
  const archiveable = quiet ? rows.filter(q => q.can_archive) : [], noun = quiet ? "copies" : "messages";
  return <li className="rounded-2xl bg-surface p-3.5 stroke">
    <p className="font-bold">{heldStatus(rows, o)}<span className="font-normal text-text-2"> · last {when(heldLast(rows))}{action ? " · " + action : ""}</span></p>
    <p className="mt-1 text-[14px] text-text-2">{first.detail || holdSentence(first.code, nameOf(first.peer, o), true)}</p>
    {first.recovery && <p className="mt-1 text-[13px] text-muted">{first.recovery}</p>}
    <details className="mt-2" onToggle={e => setOpen(e.currentTarget.open)}>
      <summary className="cursor-pointer text-[13px] font-bold text-text-2">Show {noun} ({heldCopies(rows)})</summary>
      {open && <><ul className="mt-2 flex flex-col gap-2">{rows.slice(0, visible).map(q => quiet ? <li key={q.id} className="min-w-0 text-[13px] text-muted"><time dateTime={q.at}>{when(q.at)}</time>{q.size ? " · " + size(q.size) : ""}{q.logical ? " · record " + q.logical.slice(0, 8) : ""}{q.copies ? " · re-sent " + plural(q.copies, "more time", "more times") + (q.last_at ? ", last " + when(q.last_at) : "") : ""}<span className="block break-all font-mono">{q.id}</span></li> : <HeldRow key={q.id} q={q} o={o} />)}</ul>{rows.length > visible && <Button className="mt-2" size="sm" variant="ghost" onClick={() => setVisible(n => n + 20)}>Show more {noun} ({rows.length - visible})</Button>}</>}
    </details>
    {onArchive && archiveable.length > 0 && <Button size="sm" variant="ghost" className="mt-2" disabled={busy} onClick={() => void onArchive(archiveable)}>{archiveable.length === 1 ? "Archive this notice" : "Archive these " + archiveable.length + " notices"}</Button>}
  </li>;
}

function HeldRow({ q, o }: { q: T.QuarantineItem; o: T.Overview }) {
  const store = useApp();
  const browser = store.host.platform !== "daemon";
  const [thread, setThread] = useState<T.Thread | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const verified = holdVerified(q);
  const p = verified ? personOf(q.peer, o) : undefined;
  const name = nameOf(q.peer, o);
  // The device conversation with that device, where its changed key is kept.
  const conv = latestThreads(o).find((t) => t.peer === q.peer);
  const canTrust = q.code === "key_changed" && !browser && !!conv;
  const check = async () => {
    if (!conv) return;
    setBusy(true);
    try {
      const t = await store.api.thread(conv.id);
      if (!t.key.pending) store.toast("Its identity is already trusted here.", "ok");
      else { setThread(t); setOpen(true); }
    } catch (e) {
      store.toast(errorText(e), "error");
    }
    setBusy(false);
  };
  const archive = async () => {
    setBusy(true);
    try {
      const result = await store.api.act({ do: "archive_held", id: q.id });
      store.toast(result.note || "Notice archived on this device.");
      await store.refetch();
    } catch (e) { store.toast(errorText(e), "error"); }
    finally { setBusy(false); }
  };
  return (
    <li className="flex gap-3 rounded-2xl bg-surface p-3.5 stroke">
      {verified ? <PersonAvatar name={name} seed={p?.person || q.peer} size={40} />
        : <span className="grid size-10 shrink-0 place-items-center rounded-full bg-sunken text-text-2 stroke" aria-hidden="true"><IconShieldQuestion size={20} /></span>}
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-baseline gap-x-2">
          <b className="font-bold">{verified ? name : q.code === "invalid" ? "A message that couldn’t be accepted" : "A message that couldn’t be verified"}</b>
          <span className="text-[13px] text-muted">{p ? "on " + niceDevice(q.peer) + " · " : ""}<time dateTime={q.at}>{when(q.at)}</time></span>
        </p>
        <p className="mt-0.5 text-[14px] text-text-2">{q.detail || holdSentence(q.code || "", name, browser)}</p>
        {q.recovery && <p className="mt-1 text-[14px] text-text-2">{q.recovery}</p>}
        {q.can_archive && <Button size="sm" variant="outline" className="mt-2" disabled={busy} onClick={() => void archive()}>Archive notice</Button>}
        {canTrust && <Button size="sm" variant="act" className="mt-2" disabled={busy} onClick={() => void check()}>Check and trust…</Button>}
      </div>
      {thread && <TrustSheet open={open} onOpenChange={setOpen} thread={thread} />}
    </li>
  );
}
