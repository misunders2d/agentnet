// Building blocks the Settings sections share: sticker cards, grouped rows,
// a switch that says On/Off in words, a Details toggle for technical
// words, and a loader for the reads a section makes when it opens.
import { Collapsible } from "@base-ui/react/collapsible";
import { Switch } from "@base-ui/react/switch";
import { IconChevronDown, IconCopy } from "@tabler/icons-react";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { errorText } from "../api";
import { Button } from "../ui/Button";

export const input = "w-full min-h-12 rounded-xl stroke bg-surface px-3.5 text-ink placeholder:text-muted outline-none focus-visible:[outline:3px_solid_var(--an-agent-ink)] focus-visible:outline-offset-2";

/** Card: the flat sticker surface every section is made of. */
export function Card({ children, className = "", tone = "surface" }: { children: ReactNode; className?: string; tone?: "surface" | "agent" | "ok" | "danger" | "sunken" }) {
  const bg = { surface: "bg-surface", agent: "bg-agent", ok: "bg-ok-bg", danger: "bg-danger-bg", sunken: "bg-sunken" }[tone];
  return <div className={"rounded-2xl stroke " + bg + " " + className}>{children}</div>;
}

/** GroupLabel: the small capitals over a group ("MEMBERS" in the room panel). */
export function GroupLabel({ children, id }: { children: ReactNode; id?: string }) {
  return <h3 id={id} className="mb-2 px-1 text-[12px] font-bold uppercase tracking-[0.08em] text-muted">{children}</h3>;
}

/** Tile: a section's coloured sticker icon. Pastels with ink, in both themes (like avatars). */
export function Tile({ color, children, size = 36 }: { color: string; children: ReactNode; size?: 36 | 44 }) {
  return (
    <span aria-hidden="true" className="grid shrink-0 place-items-center rounded-xl stroke text-[#1B1530]" style={{ width: size, height: size, background: color }}>
      {children}
    </span>
  );
}

/** PageHead: a section's title and the one sentence that says what it is for. */
export function PageHead({ title, lead, titleRef }: { title: string; lead?: ReactNode; titleRef?: React.Ref<HTMLHeadingElement> }) {
  return (
    <header className="mb-5">
      <h2 ref={titleRef} tabIndex={-1} className="font-display text-[28px] font-extrabold leading-tight outline-none">{title}</h2>
      {lead && <p className="mt-1 max-w-[60ch] text-text-2">{lead}</p>}
    </header>
  );
}

/** Hint: secondary sentences, never smaller than 13px. */
export function Hint({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <p className={"text-[13px] leading-snug text-muted " + className}>{children}</p>;
}

/** Toggle: a switch that also says On or Off, so the state never rests on colour. */
export function Toggle({ checked, onChange, disabled, label }: { checked: boolean; onChange: (on: boolean) => void; disabled?: boolean; label: string }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-2">
      <span aria-hidden="true" className={"w-7 text-right text-[13px] font-bold " + (checked ? "text-ok-ink" : "text-muted")}>{checked ? "On" : "Off"}</span>
      <Switch.Root checked={checked} onCheckedChange={(on) => onChange(on)} disabled={disabled} aria-label={label}
        className="group relative inline-flex h-8 w-[52px] shrink-0 items-center rounded-full stroke bg-sunken p-[3px] transition-colors duration-200 ease-out-soft data-[checked]:bg-online disabled:opacity-50 before:absolute before:-inset-x-1 before:-inset-y-1.5 before:content-['']">
        <Switch.Thumb className="block size-6 rounded-full bg-muted transition-transform duration-200 ease-out-soft data-[checked]:translate-x-5 data-[checked]:bg-white data-[checked]:stroke motion-reduce:transition-none" />
      </Switch.Root>
    </span>
  );
}

/** Details: technical words (addresses, keys, paths, commands) behind one toggle. */
export function Details({ label = "Details", children, className = "", onOpen }: { label?: string; children: ReactNode; className?: string; onOpen?: () => void }) {
  return (
    <Collapsible.Root className={className} onOpenChange={(open) => { if (open) onOpen?.(); }}>
      <Collapsible.Trigger className="group -mx-2 inline-flex min-h-11 items-center gap-1 rounded-lg px-2 text-[13px] font-semibold text-muted hover:text-ink">
        {label}
        <IconChevronDown size={16} className="transition-transform duration-200 group-data-[panel-open]:rotate-180 motion-reduce:transition-none" />
      </Collapsible.Trigger>
      <Collapsible.Panel className="mt-1 space-y-1.5 text-[13px] text-text-2">{children}</Collapsible.Panel>
    </Collapsible.Root>
  );
}

/** Fact: one technical line inside Details, wrapped and selectable. */
export function Fact({ name, children }: { name: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(84px,auto)_1fr] gap-x-3">
      <span className="font-semibold text-muted">{name}</span>
      <span className="min-w-0 select-text break-all font-mono text-[12.5px] leading-5 text-ink">{children}</span>
    </div>
  );
}

/** copyText puts text on the clipboard; false when the browser refuses. */
export async function copyText(text: string): Promise<boolean> {
  try { await navigator.clipboard.writeText(text); return true; } catch { return false; }
}

/** Command: a terminal command with a Copy button (in Details only). */
export function Command({ cmd, what, onCopied }: { cmd: string; what: string; onCopied: (ok: boolean) => void }) {
  return (
    <div className="flex items-center gap-2 rounded-xl bg-sunken py-1 pl-3 pr-1">
      <div className="min-w-0 flex-1">
        <code className="block select-text break-all font-mono text-[13px] text-ink">{cmd}</code>
        <span className="text-[13px] text-muted">{what}</span>
      </div>
      <button type="button" aria-label={"Copy " + cmd} title="Copy" onClick={async () => onCopied(await copyText(cmd))}
        className="grid size-11 shrink-0 place-items-center rounded-full text-ink hover:bg-surface"><IconCopy size={18} /></button>
    </div>
  );
}

/** Skeleton: where something is still being read. */
export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div aria-busy="true" aria-label="Loading" className="space-y-3 rounded-2xl stroke bg-surface p-4">
      {Array.from({ length: lines }, (_, i) => <div key={i} className="h-4 animate-pulse rounded-full bg-sunken motion-reduce:animate-none" style={{ width: 92 - i * 18 + "%" }} />)}
    </div>
  );
}

/** Failed: a read that did not work, in words, with a way to try again. */
export function Failed({ text, retry }: { text: string; retry: () => void }) {
  return (
    <Card tone="danger" className="flex flex-wrap items-center gap-3 p-4">
      <p className="min-w-0 flex-1 text-[15px] font-semibold text-danger">{text}</p>
      <Button size="sm" onClick={retry}>Try again</Button>
    </Card>
  );
}

interface Loaded<T> { data?: T; error?: string; loading: boolean; reload: () => void }

/** useLoad reads once when shown and again when a dependency changes. A
 *  result that arrives after a newer read started is dropped. */
export function useLoad<T>(load: () => Promise<T>, deps: unknown[]): Loaded<T> {
  const [state, setState] = useState<{ data?: T; error?: string; loading: boolean }>({ loading: true });
  const gen = useRef(0);
  const run = useCallback(() => {
    const mine = ++gen.current;
    setState((s) => ({ ...s, loading: true }));
    load().then((data) => { if (gen.current === mine) setState({ data, loading: false }); },
      (e) => { if (gen.current === mine) setState({ error: errorText(e), loading: false }); });
  }, deps);
  useEffect(() => { run(); return () => { gen.current++; }; }, [run]);
  return { ...state, reload: run };
}
