// Share a reviewed, attributed snapshot through the ordinary encrypted message
// path. This never moves originals or turns old requests into agent work.
import { useEffect, useRef, useState } from "react";
import { errorText, type HistoryContributionReview, type T } from "../api";
import { useApp } from "../context";
import { agentName, niceDevice, TOPICS } from "../model";
import { sendID } from "../optimistic.mjs";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { overLimit } from "./Composer.files";
import { Markdown } from "./Markdown";
import { NewGroupForm } from "./NewChat";

export function ContributeHistory({ dm, ids, names, onClose, onDone }: {
  dm: T.DMThread; ids: string[]; names: Record<string, string>; onClose: () => void; onDone: () => void;
}) {
  const store = useApp(), overview = useStore(store, s => s.overview);
  const [destination, setDestination] = useState(""), [target, setTarget] = useState<T.DMThread | null>(null);
  const [topic, setTopic] = useState(""), [title, setTitle] = useState(""), [creating, setCreating] = useState(false);
  const [review, setReview] = useState<HistoryContributionReview | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [locked, setLocked] = useState(false);
  const files = (dm.messages || []).filter(m => ids.includes(m.id) || !!m.lid && ids.includes(m.lid))
    .flatMap(m => (m.attachments || []).map((f, i) => ({ ...f, id: m.id, index: f.index ?? i, key: m.id + ":" + (f.index ?? i) })));
  const [chosen, setChosen] = useState(() => new Set(files.filter(f => f.openable).map(f => f.key)));
  const alive = useRef(true), running = useRef(false), original = useRef(store.get().open), generation = useRef(0), newTopic = useRef(sendID());
  useEffect(() => { alive.current = true; return () => { alive.current = false; generation.current++; }; }, []);
  const current = () => alive.current && store.isActive() && store.get().open === original.current && !store.get().pending;
  const groups = (overview?.dms || []).filter(d => d.kind === "group" && d.id !== dm.id && !d.frozen && (!d.role || d.role === "member"));
  const problem = ids.length > 64 ? "Choose at most 64 messages to share together." : overLimit(files.filter(f => chosen.has(f.key)), overview?.files);
  const selectGroup = async (id: string) => {
    const request = ++generation.current;
    setDestination(id); setTarget(null); setTopic(""); setReview(null); setError("");
    if (!id) return;
    try {
      const value = await store.api.dm(id);
      if (current() && request === generation.current) setTarget(value);
    } catch (e) { if (current() && request === generation.current) setError(errorText(e)); }
  };
  const prepare = async () => {
    if (!target || !ids.length || problem || topic === "new" && !title.trim() || running.current || !current()) return;
    running.current = true; setBusy(true); setError("");
    try {
      const value = await store.api.previewHistoryContribution({ source: dm.id, ids, destination,
        topic: topic === "new" ? newTopic.current : topic,
        ...(topic === "new" ? {new_topic:true,title:title.trim()} : {}),
        files: files.filter(f => chosen.has(f.key)).map(f => ({id:f.id,index:f.index})) });
      if (current()) setReview(value);
    } catch (e) { if (current()) setError(errorText(e)); }
    finally { running.current = false; if (alive.current) setBusy(false); }
  };
  const apply = async () => {
    if (!review || running.current || !current()) return;
    running.current = true; setLocked(true); setBusy(true); setError("");
    try {
      await store.api.applyHistoryContribution(review);
      if (!current()) return;
      store.toast("Selected history queued for the group.", "ok");
      const draft = store.draft(review.destination);
      store.setDraft(review.destination, {...draft,topic:review.topic || undefined,newTopic:false,replyTo:undefined});
      onDone(); void store.refetch(); await store.open({kind:"dm",id:review.destination});
    } catch (e) {
      if (current()) {
        const message = errorText(e);
        setError(message);
        // This exact refusal proves that nothing was enqueued. An uncertain
        // transport response keeps the original review and operation for retry.
        if (message.trim().replace(/\.$/, "").toLowerCase() === "the selected history or destination audience changed; review the contribution again") {
          setReview(null); setLocked(false);
        }
      }
    }
    finally { running.current = false; if (alive.current) setBusy(false); }
  };
  const audience = [...new Map((review?.audience || []).map(a => [a.role + ":" + (a.role === "agent" ? a.address + ":" + a.fingerprint + ":" + (a.agent_id || a.pid || "") : a.person) + ":" + (a.topic || ""), a])).values()];
  const audienceLabel = (a: HistoryContributionReview["audience"][number]) => {
    const known = a.role === "agent" && target?.agents?.find(v => v.pid === a.pid && v.host.address === a.address && v.host.fingerprint === a.fingerprint);
    return known ? agentName(known.agent_id, names, known.host, overview?.person) + " · " + niceDevice(a.address) : a.label || a.address;
  };
  return <Sheet open onOpenChange={open => { if (!open && !busy) onClose(); }} title={creating ? "Create a destination group" : "Continue in a group"}
    description={ids.length + (ids.length === 1 ? " selected message" : " selected messages")}
    footer={!creating && <div className="flex flex-wrap justify-end gap-2">
      <Button variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
      {review ? <Button variant="act" disabled={busy} onClick={() => void apply()}>{busy ? "Sharing…" : locked ? "Retry this share" : "Share selected history"}</Button>
        : <Button variant="act" disabled={busy || !target || !ids.length || !!problem || topic === "new" && !title.trim()} onClick={() => void prepare()}>{busy ? "Checking…" : "Review history and audience"}</Button>}
    </div>}>
    {creating ? <NewGroupForm navigate={false} onBusy={setBusy} onBack={() => setCreating(false)} onCreated={id => { setCreating(false); void selectGroup(id); }} /> : <div className="space-y-4">
      <p className="text-[13px] text-muted">Share a copy attributed to its original speakers. The original chat stays intact. Old questions and tasks are shared as context and do not run.</p>
      {review ? <>
        <p className="font-semibold">Share with “{target?.title || "Selected group"}”</p>
        <section aria-label="Reviewed audience" className="rounded-xl bg-surface p-3 stroke">
          <p className="font-semibold">Who can receive this history</p>
          <ul className="mt-2 max-h-40 overflow-auto space-y-1 text-[13px]">{audience.map((a, i) => <li key={i}>{audienceLabel(a)} · {a.role}{a.topic ? " · selected topic" : ""}</li>)}</ul>
          <p className="mt-2 text-[13px] text-muted">Pending invitees are not included. Later access follows the group’s normal invitations.</p>
        </section>
        <details open className="rounded-xl bg-surface p-3 stroke"><summary className="cursor-pointer font-semibold">History to share ({review.items.length})</summary>
          <div className="mt-2 max-h-64 overflow-auto"><Markdown text={review.body} /></div>
        </details>
        {review.items.flatMap(item => (item.files || []).filter(f => f.selected).map(f => <p key={item.id + ":" + f.index} className="text-[13px]">{f.name} · {f.available ? "included" : "unavailable"}</p>))}
        {!locked && <Button variant="ghost" disabled={busy} onClick={() => setReview(null)}>Change selection or destination</Button>}
      </> : <fieldset disabled={busy} className="space-y-3">
        <label className="block font-semibold">Destination group<select value={destination} onChange={e => void selectGroup(e.target.value)} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke">
          <option value="">Choose a group</option>{groups.map(g => <option key={g.id} value={g.id}>{g.title || "Unnamed group"}</option>)}
          {target && !groups.some(g => g.id === target.id) && <option value={target.id}>{target.title || "New group"}</option>}
        </select></label>
        <Button variant="ghost" onClick={() => setCreating(true)}>Create a group</Button>
        {destination && !target && !error && <p role="status">Reading the group…</p>}
        {target && <label className="block font-semibold">Destination topic<select value={topic} onChange={e => setTopic(e.target.value)} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke">
          <option value="">Main flow</option><option value="new">A new topic</option>{(target.topics || []).filter(t => t.state === "active").map(t => <option key={t.id} value={t.id}>{t.title || "Untitled topic"}</option>)}
        </select></label>}
        {topic === "new" && <label className="block font-semibold">New topic name<input value={title} onChange={e => setTitle(e.target.value)} maxLength={TOPICS.titleMax} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke" /></label>}
        {files.map(f => <label key={f.key} className="flex min-h-11 items-center gap-2 text-[13px]"><input type="checkbox" disabled={!f.openable} checked={chosen.has(f.key)} onChange={e => {const checked = e.target.checked; setChosen(s => {const next = new Set(s); if (checked) next.add(f.key); else next.delete(f.key); return next;});}} />{f.name}{!f.openable && " · unavailable; not shared"}</label>)}
      </fieldset>}
      {(error || problem) && <p role="alert" className="text-danger">{error || problem}</p>}
    </div>}
  </Sheet>;
}
