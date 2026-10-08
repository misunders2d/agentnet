// Held back (overview.quarantine): messages this device received but could
// not let in, by who sent them and why, in plain words. Their content is
// never shown, and they are not decisions: the OKs count leaves them out.
// One held for a changed identity can be checked and trusted from here on
// a computer, the same way as from the paused chat. One that didn't verify
// proves nothing about who sent it: it only says who it claims to be from.
import { useState } from "react";
import { IconShieldQuestion } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { holdSentence, holdVerified, nameOf, niceDevice, personOf, when } from "../model";
import { PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { latestThreads } from "./AgentsView.grants";
import { TrustSheet } from "./Trust";

export function HeldBack({ o }: { o: T.Overview }) {
  const items = [...(o.quarantine || [])].sort((a, b) => b.at.localeCompare(a.at));
  if (!items.length) return null;
  return (
    <section className="px-4 pt-6" aria-labelledby="oks-heldback">
      <h2 id="oks-heldback" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Held back</h2>
      <p className="pt-1 text-[14px] text-text-2">Messages this device couldn’t let in. What they say isn’t shown, and nothing runs them.</p>
      <ul className="flex flex-col gap-3 pt-3">
        {items.map((q) => <HeldRow key={q.id} q={q} o={o} />)}
      </ul>
    </section>
  );
}

function HeldRow({ q, o }: { q: T.QuarantineItem; o: T.Overview }) {
  const store = useApp();
  const browser = store.host.platform === "browser";
  const [thread, setThread] = useState<T.Thread | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const verified = holdVerified(q.code || "");
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
