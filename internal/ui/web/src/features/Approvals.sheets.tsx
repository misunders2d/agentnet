// Confirm steps for decisions: one sentence of what it means, the
// consequential button first and labelled with the action, and a way out.
import { useRef, useState, type ReactNode } from "react";
import { useRetainedText } from "../retained-text";
import { Collapsible } from "@base-ui/react/collapsible";
import { IconChevronDown } from "@tabler/icons-react";
import { Sheet } from "../ui/Sheet";
import { Button } from "../ui/Button";

export function ConfirmSheet({ open, onOpenChange, title, body, confirm, onConfirm, onCancel, other, onOther, tone = "act", children }: {
  open: boolean; onOpenChange: (open: boolean) => void; title: string; body: ReactNode; confirm: string; onConfirm: () => unknown;
  onCancel?: () => void; other?: string; onOther?: () => unknown; tone?: "act" | "danger"; children?: ReactNode;
}) {
  const done = (fn?: () => unknown) => () => { onOpenChange(false); void fn?.(); };
  const cancel = () => { onCancel?.(); onOpenChange(false); };
  return (
    <Sheet open={open} onOpenChange={value => { if (value) onOpenChange(true); else cancel(); }} title={title}
      footer={
        <div className="flex flex-col gap-2">
          <Button variant={tone} size="lg" onClick={done(onConfirm)}>{confirm}</Button>
          <Button variant="ghost" size="lg" onClick={other ? done(onOther) : cancel}>{other || "Cancel"}</Button>
        </div>
      }>
      <p className="text-[16px] leading-relaxed text-text-2">{body}</p>
      {children}
    </Sheet>
  );
}

export function DeclineSheet({ open, onOpenChange, who, kind, onDecline, draftKey }: {
  open: boolean; onOpenChange: (open: boolean) => void; who: string; kind: string; draftKey: string; onDecline: (reason: string) => Promise<boolean>;
}) {
  const [reason, setReason, retained] = useRetainedText(draftKey, "");
  return (
    <ConfirmSheet open={open} onOpenChange={onOpenChange} title={"Decline this " + kind + "?"}
      body={"Nothing runs. " + who + " is told it was declined."}
      confirm="Decline" tone="danger" onCancel={retained.discard} onConfirm={async () => {
        const confirmed = retained.capture();
        if (await onDecline(reason.trim())) confirmed();
      }}>
      <label htmlFor="decline-reason" className="mt-4 block text-[14px] font-bold">Why? <span className="font-medium text-muted">Optional. {who} sees this.</span></label>
      <textarea id="decline-reason" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={2000}
        className="mt-1.5 w-full resize-none rounded-2xl bg-surface stroke px-3.5 py-2.5 text-[16px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
    </ConfirmSheet>
  );
}

export function AnswerSheet({ question, onClose, onSend, draftKey }: {
  question: string; onClose: () => void; onSend: (text: string) => Promise<boolean>; draftKey: string;
}) {
  const [text, setText, retained] = useRetainedText(draftKey, "");
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const send = async () => {
    if (pending.current || !text.trim()) return;
    pending.current = true; setBusy(true);
    const confirmed = retained.capture();
    try { if (await onSend(text.trim())) { confirmed(); onClose(); } }
    finally { pending.current = false; setBusy(false); }
  };
  return <Sheet open onOpenChange={(open) => { if (!open) { retained.discard(); onClose(); } }} title="Answer your agent"
    footer={<Button size="lg" disabled={busy || !text.trim()} onClick={() => void send()}>{busy ? "Sending…" : "Send answer"}</Button>}>
    {question && <p className="whitespace-pre-wrap text-text-2 [overflow-wrap:anywhere]">{question}</p>}
    <p className="mt-3 text-[14px] text-text-2">Reply supplies missing information for this request. If the agent reports a permission or environment problem, resolve it in the native agent on its host computer first. Reply and retry keep the same permissions.</p>
    <label htmlFor="agent-answer" className="mt-4 block text-[14px] font-bold">Your answer</label>
    <textarea id="agent-answer" rows={4} value={text} onChange={(e) => setText(e.target.value)} maxLength={8000} disabled={busy}
      className="mt-1.5 w-full resize-none rounded-2xl bg-surface stroke px-3.5 py-2.5 text-[16px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
  </Sheet>;
}

/** Details: technical facts, folded away from the normal flow. */
export function Details({ children, label = "Details" }: { children: ReactNode; label?: string }) {
  return (
    <Collapsible.Root className="mt-2">
      <Collapsible.Trigger className="group -ml-2 inline-flex min-h-11 items-center gap-1 rounded-xl px-2 text-[14px] font-bold text-text-2 hover:bg-sunken">
        {label}<IconChevronDown size={16} className="transition-transform duration-200 motion-reduce:transition-none group-data-[panel-open]:rotate-180" aria-hidden="true" />
      </Collapsible.Trigger>
      <Collapsible.Panel><dl className="rounded-xl bg-sunken px-3.5 py-2.5">{children}</dl></Collapsible.Panel>
    </Collapsible.Root>
  );
}

export function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="py-1">
      <dt className="text-[12px] font-extrabold uppercase tracking-[0.05em] text-muted">{k}</dt>
      <dd className="text-[15px] [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}
