// Review exact selected originals before one shared display-only operation.
import { useEffect, useRef, useState } from "react";
import { errorText, type T, type TopicOrganizationReview } from "../api";
import { useApp } from "../context";
import { TOPICS } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { shownText } from "./Message.model";

export function OrganizeMessages({ dm, ids, merge, onClose, onDone }: {
  dm: T.DMThread; ids: string[]; merge?: string; onClose: () => void; onDone: () => void;
}) {
  const store = useApp(), [destination, setDestination] = useState("new"), [title, setTitle] = useState("");
  const [review, setReview] = useState<TopicOrganizationReview | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const fresh = useRef(sendID()), running = useRef(false), original = useRef(store.get().open), alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const count = ids.length, tooMany = count > 200;
  const current = () => alive.current && store.isActive() && store.get().open === original.current && !store.get().pending;
  const topics = (dm.topics || []).filter(t => t.state === "active" && t.id !== merge);
  const destinationName = destination === "new" ? title.trim() : topics.find(t => t.id === destination)?.title || "Selected topic";
  const prepare = async () => {
    if (running.current || !count || tooMany || destination === "new" && !title.trim() || !current()) return;
    running.current = true; setBusy(true); setError("");
    try {
      const value = await store.api.previewTopicOrganization({ conv: dm.id, ids, topic: destination === "new" ? fresh.current : destination,
        ...(merge ? { merge } : {}), ...(destination === "new" ? { new: true, title: title.trim() } : {}) });
      if (current()) setReview(value);
    } catch (e) { if (current()) setError(errorText(e)); }
    finally { running.current = false; if (alive.current) setBusy(false); }
  };
  const apply = async () => {
    if (running.current || !review || !current()) return;
    running.current = true; setBusy(true); setError("");
    try {
      await store.api.applyTopicOrganization(review);
      if (!current()) return;
      store.toast(count + (count === 1 ? " message moved." : " messages moved."), "ok");
      onDone(); void store.refetch();
    } catch (e) {
      if (current()) {
        const message = errorText(e);
        setError(message);
        if (message.trim().replace(/\.$/, "").toLowerCase() === "the selected messages, topic or audience changed; review the move again") setReview(null);
      }
    }
    finally { running.current = false; if (alive.current) setBusy(false); }
  };
  return <Sheet open onOpenChange={open => { if (!open && !busy) onClose(); }} title={merge ? "Merge this topic" : "Move selected messages"}
    description={count + (count === 1 ? " message selected" : " messages selected")} footer={<div className="flex flex-wrap justify-end gap-2">
      <Button variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
      {review ? <Button variant="act" disabled={busy} onClick={() => void apply()}>{busy ? "Moving…" : error ? "Retry this move" : "Move " + count + (count === 1 ? " message" : " messages")}</Button>
        : <Button variant="act" disabled={busy || !count || tooMany || destination === "new" && !title.trim()} onClick={() => void prepare()}>{busy ? "Checking…" : "Review move"}</Button>}
    </div>}>
    {tooMany && <p role="alert" className="mb-3 text-danger">Choose at most 200 messages per move. This selection has {count}.</p>}
    {review ? <p className="mb-4 font-semibold">Move {count} {count === 1 ? "message" : "messages"} to “{destinationName}”?</p>
      : <fieldset disabled={busy} className="mb-4 space-y-3">
        <label className="block font-semibold">Destination<select aria-label="Destination topic" value={destination} onChange={e => setDestination(e.target.value)} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke">
          <option value="new">A new topic</option>{topics.map(t => <option key={t.id} value={t.id}>{t.title || "Untitled topic"}</option>)}
        </select></label>
        {destination === "new" && <label className="block font-semibold">New topic name<input autoFocus value={title} maxLength={TOPICS.titleMax} onChange={e => setTitle(e.target.value)} className="mt-1 h-12 w-full rounded-xl bg-surface px-3 stroke" /></label>}
      </fieldset>}
    <details className="mb-3"><summary className="min-h-11 cursor-pointer font-semibold">Selected messages ({count})</summary>
      <ol className="max-h-52 overflow-auto space-y-2">{ids.map(id => {
        const m = dm.messages?.find(m => m.id === id || m.lid === id);
        return <li key={id} className="border-b border-hairline py-2 text-[13px] whitespace-pre-wrap">{m ? shownText(m) || "Message with files" : "Selected message no longer loaded"}</li>;
      })}</ol>
    </details>
    {merge && <p className="mb-3 text-[13px] text-text-2">Only these reviewed messages merge. Later posts remain in the source topic.</p>}
    {error && <p role="alert" className="text-danger">{error}</p>}
  </Sheet>;
}
