import { useRef, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

/** An explicit exact-request decision. Only the host's returned status closes
 * the request; sending this control never means the job has been resolved. */
export function ResolveOwnRequest({ request, disabled }: { request: T.ContinuationAction; disabled?: boolean }) {
  const store = useApp();
  const [choice, setChoice] = useState<{ request: T.ContinuationAction; send: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  if (!request.host) return null;
  const submit = async () => {
    if (!choice || sending.current) return;
    sending.current = true; setBusy(true);
    try {
      const c = choice.request;
      const result = await store.run(api => api.decide({host:c.host!,id:c.id,key:c.key,attempt:c.attempt,action:"resolve",expect:"needs_human",report:"",send_id:choice.send}));
      if (result) { store.toast(result.note || "Decision queued. Waiting for the host to confirm."); setChoice(null); }
    } finally { sending.current = false; setBusy(false); }
  };
  return <>
    <Button size="sm" variant="ghost" disabled={disabled || busy} onClick={() => setChoice({request:{...request},send:sendID()})}>Mark as handled</Button>
    <Sheet open={!!choice} onOpenChange={open => { if (!open && !busy) setChoice(null); }} title={"Mark as handled on " + niceDevice(choice?.request.host || request.host) + "?"}
      footer={<Button size="lg" disabled={busy} onClick={() => void submit()}>{busy ? "Sending…" : "Mark as handled"}</Button>}>
      <p>Closes this exact waiting request on its host. It keeps the messages and does not run the task again. The request stays pending here until the host confirms.</p>
    </Sheet>
  </>;
}
