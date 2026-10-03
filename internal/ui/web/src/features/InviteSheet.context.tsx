// "What can Bohdan see?": the recent messages (a stepper) or exactly the
// selected ones, and a preview of every line that will be shared. Nothing
// earlier than what is listed leaves this computer.
import { useState, type ReactNode } from "react";
import { IconCheck, IconLock, IconMinus, IconPlus } from "@tabler/icons-react";
import type { T } from "../api";
import { timeOf, sameDay, when } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { lineOf, plural, span, speaker } from "./RoomPanel.model";

export type Mode = "recent" | "selected";

const LADDER = [0, 1, 2, 3, 5, 10, 15, 20, 30, 50, 75, 100, 150, 200];

/** step moves the recent count along friendly sizes, ending exactly at everything there is. */
export function step(n: number, max: number, dir: 1 | -1): number {
  const rungs = [...LADDER.filter((x) => x < max), max];
  if (dir > 0) return rungs.find((x) => x > n) ?? max;
  return [...rungs].reverse().find((x) => x < n) ?? 0;
}

export function ContextChoice({ mode, onMode, recent, max, onRecent, picked, label, available }: {
  mode: Mode; onMode: (m: Mode) => void; recent: number; max: number; onRecent: (n: number) => void; picked: number; label: string; available: number;
}) {
  return (
    <fieldset className="grid grid-cols-2 gap-2.5">
      <legend className="sr-only">What they can see</legend>
      <Option on={mode === "recent"} onSelect={() => onMode("recent")} label="Recent messages">
        <div className="mt-1.5 flex items-center gap-1.5">
          <StepButton label="Fewer messages" disabled={recent <= 0} onClick={() => { onMode("recent"); onRecent(step(recent, max, -1)); }}><IconMinus size={18} stroke={2.5} /></StepButton>
          <output aria-live="polite" className="min-w-9 text-center font-display text-[22px] font-extrabold leading-none tnum">{recent}</output>
          <StepButton label="More messages" disabled={recent >= max} onClick={() => { onMode("recent"); onRecent(step(recent, max, 1)); }}><IconPlus size={18} stroke={2.5} /></StepButton>
        </div>
        <span className="mt-1 block text-[13px] font-semibold text-text-2">{available === 0 ? "Nothing here yet" : recent >= available ? "All of them" : "of " + available}</span>
      </Option>
      <Option on={mode === "selected"} onSelect={() => onMode("selected")} label="Selected messages">
        <span className="mt-1.5 block text-[13px] font-semibold leading-snug text-text-2">{picked ? (label ? label + " · " + plural(picked, "message") : plural(picked, "message") + " picked") : "Pick exactly which"}</span>
      </Option>
    </fieldset>
  );
}

function Option({ on, onSelect, label, children }: { on: boolean; onSelect: () => void; label: string; children: ReactNode }) {
  return (
    // The whole card selects its mode; the radio inside keeps keyboard and screen reader semantics.
    <div onClick={onSelect}
      className={"relative min-w-0 cursor-pointer rounded-2xl stroke px-3 py-2.5 transition-[background-color,box-shadow,transform] duration-200 ease-out-soft has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink "
        + (on ? "bg-act text-act-ink shadow-pop" : "bg-surface hover:bg-sunken")}>
      <label className="flex cursor-pointer items-center gap-1.5 text-[14px] font-extrabold leading-tight">
        <input type="radio" name="invite-context" checked={on} onChange={onSelect} className="sr-only" />
        {label}
      </label>
      <div className={on ? "[&_.text-text-2]:text-act-ink" : ""}>{children}</div>
    </div>
  );
}

function StepButton({ label, disabled, onClick, children }: { label: string; disabled: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-label={label} title={label} disabled={disabled} onClick={(e) => { e.stopPropagation(); onClick(); }}
      className="grid size-11 shrink-0 place-items-center rounded-xl stroke bg-surface text-ink press hover:bg-sunken disabled:opacity-35">
      {children}
    </button>
  );
}

/** Checklist: pick exactly which messages to share, newest last. */
export function Checklist({ list, t, o, names, selected, onToggle, onClear }: {
  list: T.DMMessage[]; t: T.DMThread; o: T.Overview | null; names: Record<string, string>; selected: Set<string>; onToggle: (id: string) => void; onClear: () => void;
}) {
  const [shown, setShown] = useState(20);
  const rows = list.slice(Math.max(0, list.length - shown));
  if (!list.length) return <p className="mt-3 text-[13px] text-muted">There are no messages here to share yet.</p>;
  return (
    <div className="fade-in mt-3 rounded-2xl border-[1.5px] border-hairline bg-surface p-1.5">
      <div className="flex items-center justify-between px-2 pb-1 pt-0.5">
        <span className="text-[13px] font-extrabold uppercase tracking-wide text-muted">Pick messages</span>
        {selected.size > 0 && <button type="button" onClick={onClear} className="min-h-11 rounded-full px-3 text-[13px] font-bold text-text-2 hover:bg-sunken">Clear</button>}
      </div>
      {list.length > rows.length && (
        <button type="button" onClick={() => setShown((n) => n + 20)} className="mb-1 min-h-11 w-full rounded-xl text-[13px] font-bold text-text-2 hover:bg-sunken">
          Show {Math.min(20, list.length - rows.length)} earlier
        </button>
      )}
      <ul className="flex flex-col">
        {rows.map((m) => {
          const who = speaker(m, t, o, names);
          const on = selected.has(m.id);
          return (
            <li key={m.id}>
              <label className={"flex min-h-11 cursor-pointer items-center gap-2.5 rounded-xl px-2 py-1.5 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink " + (on ? "bg-sunken" : "hover:bg-sunken")}>
                <input type="checkbox" checked={on} onChange={() => onToggle(m.id)} className="peer sr-only" />
                <span aria-hidden="true" className={"grid size-[22px] shrink-0 place-items-center rounded-md border-[1.5px] border-outline " + (on ? "bg-act text-act-ink" : "bg-surface")}>
                  {on && <IconCheck size={15} stroke={3} />}
                </span>
                <span className="min-w-0 flex-1 truncate text-[14px]"><b className="font-bold">{who.name}</b> <span className="text-text-2">{lineOf(m)}</span></span>
                <time className="shrink-0 text-[12px] text-muted tnum">{when(m.at)}</time>
              </label>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** Preview lists exactly what will be shared, author and first line, newest last. */
export function Preview({ who, shared, after, t, o, names, gapless }: {
  who: string; shared: T.DMMessage[]; after: string; t: T.DMThread; o: T.Overview | null; names: Record<string, string>; gapless: boolean;
}) {
  const [all, setAll] = useState(false);
  const rows = all || shared.length <= 4 ? shared : shared.slice(-4);
  const first = shared[0], last = shared[shared.length - 1];
  const range = first ? (sameDay(first.at, last.at) ? span(timeOf(first.at), timeOf(last.at)) : span(when(first.at), when(last.at))) : "";
  return (
    <section aria-label={who + " will see"} className="mt-3 rounded-2xl border-2 border-dashed border-outline/35 bg-surface/70 px-3 py-2.5">
      <div className="mb-1.5 flex items-baseline justify-between gap-3">
        <h4 className="truncate text-[13px] font-extrabold uppercase tracking-wide text-text-2">{who} will see</h4>
        {range && <span className="shrink-0 text-[12px] font-bold text-muted tnum">{range}</span>}
      </div>
      {shared.length > rows.length && (
        <button type="button" onClick={() => setAll(true)} className="-ml-1.5 flex min-h-11 items-center px-1.5 text-[13px] font-bold text-text-2 hover:text-ink">
          <span className="rounded-full border-[1.5px] border-hairline bg-surface px-2.5 py-0.5">+{shared.length - rows.length} more</span>
        </button>
      )}
      <ul className="flex flex-col gap-1">
        {rows.map((m) => {
          const s = speaker(m, t, o, names);
          return (
            <li key={m.id} className="fade-in flex min-w-0 items-center gap-2 text-[14px] leading-snug">
              {s.agent ? <AgentAvatar seed={s.seed} size={20} /> : <PersonAvatar name={s.name === "You" ? o?.person?.label || "You" : s.name} seed={s.seed} size={20} />}
              <b className="shrink-0 font-bold">{s.name}</b>
              <span className="min-w-0 truncate text-text-2">{lineOf(m)}</span>
            </li>
          );
        })}
      </ul>
      <p className="mt-2 flex items-start gap-1.5 border-t border-dashed border-hairline pt-2 text-[13px] font-semibold text-muted">
        <IconLock size={15} className="mt-0.5 shrink-0" />
        <span>{shared.length ? (gapless ? "Nothing earlier." : "Nothing else from before.") : "Nothing from before they join."} {after}</span>
      </p>
    </section>
  );
}
