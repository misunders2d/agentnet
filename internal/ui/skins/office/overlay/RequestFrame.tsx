// Office's frame around a request that needs someone (Comic kit slot,
// skin/README.md): a flat card with a coloured edge by state. act (your
// decision) is amber, work (running) is blue, quiet (stopped or handed over)
// is grey. The card's content (who asks, what, every action) is rendered as
// given, always visible, never reworded.
import type { ReactNode } from "react";
import { timeOf } from "../model";

export function RequestFrame({ tone, label, at, children, labelledBy }: { tone: "act" | "work" | "quiet"; label: string; at: string; children: ReactNode; labelledBy?: string }) {
  return (
    <article aria-labelledby={labelledBy} data-part="request" data-tone={tone} className={"fade-in office-request office-request-" + tone + " w-full overflow-hidden bg-surface"}>
      <header className="office-request-head flex items-center justify-between gap-3">
        <span className="office-request-label inline-flex items-center gap-1.5">
          {tone === "work" && <span className="flex gap-0.5" aria-hidden="true"><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /></span>}
          {label}
        </span>
        <time dateTime={at} className="tnum office-request-time">{timeOf(at)}</time>
      </header>
      {children}
    </article>
  );
}
