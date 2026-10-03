import type { ReactNode } from "react";

type Tone = "guest" | "agent" | "ok" | "act" | "muted" | "danger";
const tones: Record<Tone, string> = {
  guest: "bg-guest-bg text-guest-ink border-guest",
  agent: "bg-agent text-agent-ink border-agent-ink/40",
  ok: "bg-ok-bg text-ok-ink border-ok-ink/40",
  act: "bg-act text-act-ink border-outline",
  muted: "bg-sunken text-muted border-hairline",
  danger: "bg-danger-bg text-danger border-danger/40",
};

/** Tag: a small word that carries meaning in text, never by colour alone. */
export function Tag({ tone = "muted", children, className = "" }: { tone?: Tone; children: ReactNode; className?: string }) {
  return <span className={"inline-flex items-center gap-1 h-5 px-1.5 rounded-md border text-[11px] font-bold uppercase tracking-wide leading-none " + tones[tone] + " " + className}>{children}</span>;
}
