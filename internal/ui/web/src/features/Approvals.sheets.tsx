// Confirm steps for decisions: one sentence of what it means, the
// consequential button first and labelled with the action, and a way out.
import { useEffect, useState, type ReactNode } from "react";
import { Collapsible } from "@base-ui/react/collapsible";
import { IconChevronDown } from "@tabler/icons-react";
import { Sheet } from "../ui/Sheet";
import { Button } from "../ui/Button";

export function ConfirmSheet({ open, onOpenChange, title, body, confirm, onConfirm, other, onOther, tone = "act", children }: {
  open: boolean; onOpenChange: (open: boolean) => void; title: string; body: ReactNode; confirm: string; onConfirm: () => void | Promise<void>;
  other?: string; onOther?: () => void | Promise<void>; tone?: "act" | "danger"; children?: ReactNode;
}) {
  const done = (fn?: () => void | Promise<void>) => () => { onOpenChange(false); void fn?.(); };
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={title}
      footer={
        <div className="flex flex-col gap-2">
          <Button variant={tone} size="lg" onClick={done(onConfirm)}>{confirm}</Button>
          <Button variant="ghost" size="lg" onClick={other ? done(onOther) : () => onOpenChange(false)}>{other || "Cancel"}</Button>
        </div>
      }>
      <p className="text-[16px] leading-relaxed text-text-2">{body}</p>
      {children}
    </Sheet>
  );
}

export function DeclineSheet({ open, onOpenChange, who, kind, onDecline }: {
  open: boolean; onOpenChange: (open: boolean) => void; who: string; kind: string; onDecline: (reason: string) => void;
}) {
  const [reason, setReason] = useState("");
  useEffect(() => { if (open) setReason(""); }, [open]);
  return (
    <ConfirmSheet open={open} onOpenChange={onOpenChange} title={"Decline this " + kind + "?"}
      body={"Nothing runs. " + who + " is told it was declined."}
      confirm="Decline" tone="danger" onConfirm={() => onDecline(reason.trim())}>
      <label htmlFor="decline-reason" className="mt-4 block text-[14px] font-bold">Why? <span className="font-medium text-muted">Optional. {who} sees this.</span></label>
      <textarea id="decline-reason" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={2000}
        className="mt-1.5 w-full resize-none rounded-2xl bg-surface stroke px-3.5 py-2.5 text-[16px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
    </ConfirmSheet>
  );
}

/** Details: technical facts, folded away from the normal flow. */
export function Details({ children, label = "Details" }: { children: ReactNode; label?: string }) {
  return (
    <Collapsible.Root className="mt-2">
      <Collapsible.Trigger className="group -ml-2 inline-flex min-h-11 items-center gap-1 rounded-xl px-2 text-[14px] font-bold text-text-2 hover:bg-sunken">
        {label}<IconChevronDown size={16} className="transition-transform duration-200 group-data-[panel-open]:rotate-180" aria-hidden="true" />
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
