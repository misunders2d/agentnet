// "What can Bohdan see?": the recent messages (a stepper), exactly the
// selected ones, or everything since a date (MEL-528), and a preview of
// every line that will be shared. Nothing earlier than what is listed
// leaves this computer.
import { useRef, useState, type ReactNode } from "react";
import { IconCalendar, IconCheck, IconLock, IconMinus, IconPlus } from "@tabler/icons-react";
import type { T } from "../api";
import { localInput, timeOf, timeZone, sameDay, when } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { lineOf, plural, span, speaker } from "./RoomPanel.model";

export type Mode = "recent" | "selected" | "since";

const sinceParts = (since: string) => {
  const split = since.lastIndexOf("T");
  return split < 0 ? [since, ""] : [since.slice(0, split), since.slice(split + 1)];
};

/** sinceAt: the cutoff a "Share messages since" value (local YYYY-MM-DDTHH:MM)
 *  names, or why it names none. The local wall-clock parts must round-trip
 *  exactly, as for reminders and schedules: a mistyped day or a time that
 *  does not exist here (a DST gap) is refused, never moved to another time.
 *  A cutoff after now is refused too. */
export function sinceAt(since: string, now = Date.now()): { at: number; problem?: undefined } | { at?: undefined; problem: string } {
  const [day, time] = sinceParts(since);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return { problem: "Enter the date as YYYY-MM-DD." };
  if (!/^\d{2}:\d{2}$/.test(time)) return { problem: "Enter the time as hours and minutes." };
  const [y, mo, d] = day.split("-").map(Number), [h, mi] = time.split(":").map(Number);
  const at = new Date(y, mo - 1, d, h, mi);
  if (localInput(at) !== day + "T" + time) return { problem: "That date and time don’t exist here. Check the day and the time." };
  if (at.getTime() > now) return { problem: "That’s in the future. Choose a time up to now." };
  return { at: at.getTime() };
}

/** sinceShared: the messages at or after since, and the newest cap of them when
 *  there are more than one invitation carries (over says so). An invalid
 *  cutoff shares nothing and says why (problem): never another slice. */
export function sinceShared<M extends { at: string }>(list: M[], since: string, cap: number, now = Date.now()): { shared: M[]; total: number; over: boolean; problem?: string } {
  const cut = sinceAt(since, now);
  if (cut.problem !== undefined) return { shared: [], total: 0, over: false, problem: cut.problem };
  const all = list.filter((m) => Date.parse(m.at) >= cut.at);
  return { shared: all.slice(Math.max(0, all.length - cap)), total: all.length, over: all.length > cap };
}

/** sinceShortcuts: Last hour, Today and Yesterday, as local cutoffs from now. */
export function sinceShortcuts(now = new Date()): { label: string; value: string }[] {
  const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  return [
    { label: "Last hour", value: localInput(new Date(now.getTime() - 3600e3)) },
    { label: "Today", value: localInput(midnight) },
    { label: "Yesterday", value: localInput(yesterday) },
  ];
}

/** zoneLabel: this computer's time zone, named and as an offset from UTC. */
export function zoneLabel(at = new Date()): string {
  const off = -at.getTimezoneOffset(), abs = Math.abs(off);
  const utc = "UTC" + (off >= 0 ? "+" : "−") + String(Math.floor(abs / 60)).padStart(2, "0") + ":" + String(abs % 60).padStart(2, "0");
  const zone = timeZone();
  return zone ? zone + " (" + utc + ")" : utc;
}

const LADDER = [0, 1, 2, 3, 5, 10, 15, 20, 30, 50, 75, 100, 150, 200];

/** step moves the recent count along friendly sizes, ending exactly at everything there is. */
export function step(n: number, max: number, dir: 1 | -1): number {
  const rungs = [...LADDER.filter((x) => x < max), max];
  if (dir > 0) return rungs.find((x) => x > n) ?? max;
  return [...rungs].reverse().find((x) => x < n) ?? 0;
}

export function ContextChoice({ mode, onMode, recent, max, onRecent, picked, label, available, since, onSince, sinceCount, sinceProblem, cap }: {
  mode: Mode; onMode: (m: Mode) => void; recent: number; max: number; onRecent: (n: number) => void; picked: number; label: string; available: number;
  since: string; onSince: (v: string) => void; sinceCount: number; sinceProblem?: string; cap: number;
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
      <Option on={mode === "since"} onSelect={() => onMode("since")} label="Share messages since" wide>
        <SinceFields since={since} onSince={(v) => { onSince(v); onMode("since"); }} onFocus={() => onMode("since")} />
        <span className={"mt-1.5 block text-[13px] tnum text-text-2 " + (sinceProblem ? "font-extrabold" : "font-semibold")}>
          {sinceProblem ? sinceProblem + " Nothing is shared until it’s fixed." : sinceCount === 0 ? "Nothing since then" : sinceCount > cap ? "The newest " + cap + " of " + sinceCount : plural(sinceCount, "message")}
        </span>
      </Option>
    </fieldset>
  );
}

/** SinceFields: the cutoff as separate Date and Time fields (the reminder
 *  pattern: a typed YYYY-MM-DD date, a calendar only on request, a time
 *  field), the local time zone, and Last hour / Today / Yesterday. */
function SinceFields({ since, onSince, onFocus }: { since: string; onSince: (v: string) => void; onFocus: () => void }) {
  const [day, time] = sinceParts(since);
  const calendar = useRef<HTMLInputElement>(null);
  const [showCalendar, setShowCalendar] = useState(false);
  const field = "min-h-11 w-full min-w-0 rounded-xl stroke bg-surface px-2.5 text-[16px] text-ink outline-none focus-visible:outline-3 focus-visible:outline-agent-ink lg:text-[15px]";
  return (
    // Inside the card: typing here never toggles the card itself.
    <div className="mt-1.5" onClick={(e) => e.stopPropagation()}>
      <div className="grid grid-cols-2 gap-2">
        <label className="block min-w-0">
          <span className="mb-1 block text-[13px] font-semibold">Date</span>
          <input type="text" inputMode="numeric" value={day} placeholder="YYYY-MM-DD" autoComplete="off" spellCheck={false} aria-describedby="invite-since-zone"
            onChange={(e) => onSince(e.target.value + "T" + time)} onFocus={onFocus} className={field} />
        </label>
        <label className="block min-w-0">
          <span className="mb-1 block text-[13px] font-semibold">Time</span>
          <input type="time" value={time} aria-describedby="invite-since-zone"
            onChange={(e) => onSince(day + "T" + e.target.value)} onFocus={onFocus} className={field} />
        </label>
      </div>
      <p id="invite-since-zone" className="mt-1 text-[13px] text-text-2">Your time zone: {zoneLabel()}</p>
      <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
        {sinceShortcuts().map((q, i) => (
          <button key={q.label} type="button" aria-pressed={since === q.value} onClick={() => { setShowCalendar(false); onSince(sinceShortcuts()[i].value); }}
            className={"min-h-11 rounded-full stroke px-3 text-[14px] font-bold press " + (since === q.value ? "bg-ink text-canvas" : "bg-surface text-ink hover:bg-sunken")}>{q.label}</button>
        ))}
        <button type="button" className="flex min-h-11 items-center gap-1.5 px-1.5 text-[14px] font-semibold underline underline-offset-2" onClick={() => {
          setShowCalendar(true); onFocus();
          try { calendar.current?.showPicker?.(); } catch { /* the visible date field remains */ }
        }}><IconCalendar size={17} aria-hidden="true" />Calendar</button>
        <input ref={calendar} aria-label="Calendar date" aria-hidden={!showCalendar} type="date" tabIndex={showCalendar ? 0 : -1}
          value={/^\d{4}-\d{2}-\d{2}$/.test(day) ? day : ""} max={localInput(new Date()).split("T")[0]}
          onChange={(e) => onSince(e.target.value + "T" + time)}
          className={showCalendar ? "min-h-11 max-w-full rounded-xl stroke bg-surface px-2.5 text-[16px] text-ink" : "sr-only"} />
      </div>
    </div>
  );
}

function Option({ on, onSelect, label, children, wide }: { on: boolean; onSelect: () => void; label: string; children: ReactNode; wide?: boolean }) {
  return (
    // The whole card selects its mode; the radio inside keeps keyboard and screen reader semantics.
    <div onClick={onSelect}
      className={"relative min-w-0 cursor-pointer rounded-2xl stroke px-3 py-2.5 transition-[background-color,box-shadow,transform] duration-200 ease-out-soft has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink "
        + (wide ? "col-span-2 " : "") + (on ? "bg-act text-act-ink shadow-pop" : "bg-surface hover:bg-sunken")}>
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
export function Preview({ who, shared, after, t, o, names, gapless, limit }: {
  who: string; shared: T.DMMessage[]; after: string; t: T.DMThread; o: T.Overview | null; names: Record<string, string>; gapless: boolean; limit?: string;
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
      {limit && <p className="mb-1.5 text-[13px] font-semibold text-approval-ink">{limit}</p>}
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
