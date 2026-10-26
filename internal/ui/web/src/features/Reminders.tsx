// Reminders ("Remind me later", MEL-528): a personal reminder on a received
// or sent message, shared with this person's verified devices. It only asks
// for their attention; the original message stays unchanged. Done, Cancel and
// a reply end it. The host's remind capability controls availability.
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { IconAlarm, IconAlarmSnooze, IconCalendar, IconCheck } from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp } from "../context";
import { REMIND, agentName, dueText, firstLine, isMine, localInput, nameOf, personOf, reminderTimes, timeZone } from "../model";
import { useStore, type Store } from "../store";
import { useRetainedText } from "../retained-text";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

/** What a reminder is about: a received message, as the sheet names it. */
export interface Remindable { id: string; text: string }

/** reminderFrom: who sent the reminded message, by name: a person in a chat,
 *  or an agent in its own conversation ("Vitalii’s agent"). Never an address. */
export function reminderFrom(r: T.ReminderView, o: T.Overview | null, names: Record<string, string>): string {
  if (isMine(r.from, o)) return "You";
  if (r.conv) return nameOf(r.from, o);
  const host = personOf(r.from, o);
  return host ? agentName(undefined, names, host, o?.person) : nameOf(r.from, o);
}

/** openReminder opens the message a reminder is about: in its chat, or in
 *  its agent's conversation (the thread that holds it, with its agent). */
export async function openReminder(store: Store, r: T.ReminderView) {
  if (r.conv) { await store.openMessage(r.message, { conv: r.conv }); return; }
  try {
    const t = await store.api.thread(r.message);
    store.showTab("chats");
    await store.open({ kind: "thread", id: t.id, focus: r.message, peer: t.peer });
  } catch {
    await store.openMessage(r.message); // says so when it is not on this device
  }
}

// ---- set or move -----------------------------------------------------------------------

/** RemindSheet sets a reminder on a received message, or moves the one it has. */
export function RemindSheet({ open, onOpenChange, m, r }: { open: boolean; onOpenChange: (o: boolean) => void; m: Remindable; r?: T.ReminderView }) {
  const store = useApp();
  // The quick times are counted from when the sheet opened.
  const times = useMemo(() => reminderTimes(), [open]);
  const [picked, setPicked, pickedDraft] = useRetainedText("reminder-choice:" + m.id, "0");
  const pick = picked === "custom" ? "custom" : Number(picked);
  const setPick = (value: number | "custom") => setPicked(String(value));
  const initialAt = useMemo(() => localInput(new Date(Date.now() + REMIND.customAhead * 60e3)), []);
  const [at, setAt, dateDraft] = useRetainedText("reminder-date:" + m.id, initialAt);
  const dateField = useRef<HTMLInputElement>(null);
  const dateHint = useId();
  const calendar = useRef<HTMLInputElement>(null);
  const [showCalendar, setShowCalendar] = useState(false);
  const [error, setError] = useState("");
  const change = useStore(store, s => s.reminderChanges[m.id]);
  const busy = !!change && change.status !== "failed";
  const zone = timeZone();

  const save = () => {
    if (pick === "custom" && !/^\d{4}-\d{2}-\d{2}$/.test(at.split("T")[0])) {
      setError("Enter the date as YYYY-MM-DD."); dateField.current?.focus(); return;
    }
    const due = pick === "custom" ? new Date(at) : times[pick].at;
    if (isNaN(due.getTime())) { setError("Choose a date and time."); return; }
    if (due.getTime() <= Date.now()) { setError("Choose a time in the future."); return; }
    if (pick === "custom" && localInput(due) !== at) { setError("Choose a valid date and time."); dateField.current?.focus(); return; }
    setError("");
    const clearChoice = pickedDraft.capture(), clearDate = dateDraft.capture();
    const work = store.changeReminder({ action: "set", message: m.id, due: due.toISOString(), title: m.text }, "Reminder change saved");
    onOpenChange(false);
    void work.then(result => { if (result !== undefined) { clearChoice(); clearDate(); } });
  };

  const choice = (key: number | "custom", label: string, sub?: string) => (
    <label key={String(key)} className={"flex min-h-12 cursor-pointer items-center gap-3 rounded-2xl border-[1.5px] px-3 py-2 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink "
      + (pick === key ? "border-outline bg-surface shadow-pop-sm" : "border-transparent hover:bg-sunken")}>
      <input type="radio" name="remind-when" checked={pick === key} onChange={() => { setPick(key); setError(""); }} className="sr-only" />
      <span aria-hidden="true" className={"grid size-[22px] shrink-0 place-items-center rounded-full border-[1.5px] border-outline " + (pick === key ? "bg-act text-act-ink" : "bg-surface")}>
        {pick === key && <IconCheck size={14} stroke={3} />}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block font-semibold">{label}</span>
        {sub && <span className="block text-[13px] text-text-2 tnum">{sub}</span>}
      </span>
    </label>
  );

  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={r ? "Move the reminder" : "Remind me later"}
      description={m.text ? <span className="line-clamp-2">About: {m.text}</span> : undefined}
      footer={
        <div className="flex flex-col gap-2">
          {error && <p role="alert" className="text-[14px] font-semibold text-danger">{error}</p>}
          <Button variant="act" size="lg" disabled={busy} onClick={() => void save()}>{r ? "Move it" : "Remind me"}</Button>
        </div>
      }>
      {r && <p className="mb-2 text-[14px] text-text-2">Now {r.overdue ? "due since " : "set for "}{dueText(r.due)}.</p>}
      <fieldset className="flex flex-col gap-1">
        <legend className="mb-1 text-[13px] font-extrabold uppercase tracking-wide text-muted">When</legend>
        {times.map((t, i) => choice(i, t.label, dueText(t.at)))}
        {choice("custom", "At a time I choose")}
        <div className="ml-9 mt-1 flex max-w-sm flex-col gap-3 sm:flex-row">
          <div className="min-w-0 flex-1">
            <label className="block">
            <span className="mb-1 block text-[13px] font-semibold text-text-2">Date</span>
            <input ref={dateField} aria-describedby={dateHint} type="text" value={at.split("T")[0]} placeholder="YYYY-MM-DD" autoComplete="off" spellCheck={false}
              onChange={(e) => { setAt(e.target.value + "T" + (at.split("T")[1] || "")); setPick("custom"); setError(""); }} onFocus={() => setPick("custom")}
              className="min-h-12 min-w-0 w-full rounded-xl stroke bg-surface px-3 text-[16px] text-ink outline-none focus-visible:outline-3 focus-visible:outline-agent-ink lg:text-[15px]" />
            </label>
            <span id={dateHint} className="mt-1 block text-[12px] text-text-2">Year-month-day · YYYY-MM-DD</span>
          </div>
          <label className="block min-w-0 flex-1">
            <span className="mb-1 block text-[13px] font-semibold text-text-2">Time</span>
            <input type="time" defaultValue={at.split("T")[1] || ""}
              onChange={(e) => { setAt((at.split("T")[0] || "") + "T" + e.target.value); setPick("custom"); setError(""); }} onFocus={() => setPick("custom")}
              className="min-h-12 min-w-0 w-full rounded-xl stroke bg-surface px-3 text-[16px] text-ink outline-none focus-visible:outline-3 focus-visible:outline-agent-ink lg:text-[15px]" />
          </label>
        </div>
        <div className="ml-9 mt-1">
          <button type="button" className="flex min-h-11 items-center gap-2 text-sm font-semibold underline underline-offset-2" onClick={() => {
            setPick("custom"); setShowCalendar(true); setError("");
            try { calendar.current?.showPicker?.(); } catch { /* The visible native control remains available. */ }
          }}><IconCalendar size={17} aria-hidden="true" />Choose from calendar</button>
          <input ref={calendar} aria-label="Calendar date" aria-hidden={!showCalendar} type="date" tabIndex={showCalendar ? 0 : -1}
            value={/^\d{4}-\d{2}-\d{2}$/.test(at.split("T")[0]) ? at.split("T")[0] : ""} min={localInput(new Date()).split("T")[0]}
            onChange={e => { setAt(e.target.value + "T" + (at.split("T")[1] || "")); setPick("custom"); setError(""); }}
            className={showCalendar ? "min-h-12 max-w-full rounded-xl stroke bg-surface px-3 text-[16px] text-ink" : "sr-only"} />
        </div>
      </fieldset>
      <p className="mt-3 text-[13px] leading-snug text-muted">
        Times are this computer’s{zone ? " (" + zone + ")" : ""}. When it’s due, this computer shows a notification, even with this window closed, while AgentNet runs here.
        Only you are reminded: nothing is sent and the message stays as it is. It ends when you reply to it or mark it done.
      </p>
    </Sheet>
  );
}

// ---- under a message ------------------------------------------------------------------

/** ReminderLine: the reminder a received message has, with Change (Later
 *  once it is due), Done and Cancel. */
export function ReminderLine({ r, m, right }: { r: T.ReminderView; m: Remindable; right?: boolean }) {
  const store = useApp();
  const busy = useStore(store, s => !!s.reminderChanges[m.id] && s.reminderChanges[m.id].status !== "failed");
  const [move, setMove] = useState<boolean | null>(null);
  const link = "relative font-bold underline decoration-current/30 underline-offset-2 before:absolute before:-inset-x-1 before:top-1/2 before:h-11 before:-translate-y-1/2 before:content-[''] hover:decoration-current";
  return (
    <div className={"mt-1 flex max-w-full flex-wrap items-center gap-x-1.5 gap-y-1 px-1 text-[13px] " + (right ? "justify-end " : "") + (r.overdue ? "font-semibold text-approval-ink" : "text-text-2")}>
      {r.overdue ? <IconAlarmSnooze size={15} aria-hidden="true" /> : <IconAlarm size={15} aria-hidden="true" />}
      <span>{r.overdue ? "Reminder due since " : "Reminder "}{dueText(r.due)}</span>
      <span aria-hidden="true" className="text-muted">·</span>
      <button type="button" className={link} disabled={busy} onClick={() => setMove(true)}>{r.overdue ? "Later…" : "Change…"}</button>
      <span aria-hidden="true" className="text-muted">·</span>
      <button type="button" className={link} disabled={busy} onClick={() => void store.changeReminder({ action: "done", message: m.id }, "Reminder change saved")}>Done</button>
      <span aria-hidden="true" className="text-muted">·</span>
      <button type="button" className={link} disabled={busy} onClick={() => void store.changeReminder({ action: "cancel", message: m.id }, "Reminder change saved")}>Cancel</button>
      {move !== null && <RemindSheet open={move} onOpenChange={setMove} m={m} r={r} />}
    </div>
  );
}

// ---- the dedicated Outbox view -----------------------------------------------------------

/** Reminders lists the pending reminders, the ones due first, each opening
 *  its message. Shown only where reminders are kept (a computer). */
export function Reminders() {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const changes = useStore(store, s => s.reminderChanges);
  const names = useAgentNames();
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<T.ReminderView | null>(null);
  const rs = o?.remind ? o.reminders || [] : [];
  useEffect(() => {
    if (selected && !rs.some(r => r.message === selected.message)) setSelected(null);
  }, [rs, selected]);
  if (!o?.remind) return <p className="py-5 text-text-2">Personal reminders are unavailable on this host.</p>;
  const sorted = [...rs].sort((a, b) => Number(b.overdue) - Number(a.overdue) || a.due.localeCompare(b.due));
  const due = rs.filter((r) => r.overdue).length;
  const shown = sorted.filter(r => !query.trim() || (r.title+" "+reminderFrom(r,o,names)).toLowerCase().includes(query.trim().toLowerCase()));
  return (
    <section aria-labelledby="outbox-reminders">
      <h2 id="outbox-reminders" className="flex items-center gap-1.5 pb-2 text-sm font-bold">
        <IconAlarm size={15} aria-hidden="true" />Reminders
        {due > 0 && <span className="rounded-full bg-act px-2 py-px text-[11px] tracking-normal text-act-ink stroke tnum">{due} due</span>}
      </h2>
      <p className="mb-3 text-sm text-text-2">Only you are reminded. Nothing is sent. Open a reminder to see its original message.</p>
      {Object.keys(changes).map(message => <ReminderChangeStatus key={message} message={message} />)}
      {!!rs.length&&<label className="block text-sm font-semibold">Find reminders<input type="search" value={query} onChange={e=>setQuery(e.target.value)} placeholder="Message or sender" className="my-2 block min-h-11 w-full rounded-xl border border-outline bg-surface px-3 text-[16px] outline-none focus-visible:outline-3 focus-visible:outline-agent-ink"/></label>}
      {!rs.length&&<p className="py-5">No reminders pending on this device.</p>}
      {!!rs.length&&!shown.length&&<p className="py-5">No reminders match your search.</p>}
      <ul className="divide-y divide-hairline">
        {shown.map((r) => <ReminderRow key={r.message} r={r} o={o} names={names} onChange={()=>setSelected(r)} />)}
      </ul>
      {selected&&rs.some(r=>r.message===selected.message)&&<Sheet open onOpenChange={open=>{if(!open)setSelected(null);}} title="Reminder" description={firstLine(selected.title,REMIND.titleMax)}>
        <ReminderLine r={rs.find(r=>r.message===selected.message) || selected} m={{id:selected.message,text:selected.title}} />
        <Button className="mt-4" onClick={()=>{void openReminder(store,selected);setSelected(null);}}>Open original message</Button>
      </Sheet>}
    </section>
  );
}

function ReminderRow({ r, o, names, onChange }: { r: T.ReminderView; o: T.Overview | null; names: Record<string, string>; onChange:()=>void }) {
  const store = useApp();
  const busy = useStore(store, s => !!s.reminderChanges[r.message] && s.reminderChanges[r.message].status !== "failed");
  const from = reminderFrom(r, o, names);
  const p = personOf(r.from, o);
  const face = r.conv ? <PersonAvatar name={from} seed={p?.person || r.from} size={36} /> : <AgentAvatar seed={r.from} size={36} mood={r.overdue ? "waiting" : "neutral"} />;
  return (
    <li className="flex items-center gap-1 py-2">
      <button type="button" onClick={() => void openReminder(store, r)}
        className={"flex min-h-14 min-w-0 flex-1 items-center gap-3 rounded-xl px-2 py-2 text-left press " + (r.overdue ? "bg-act/15 text-ink" : "hover:bg-sunken")}>
        {face}
        <span className="min-w-0 flex-1">
          <span className="block truncate font-bold">{firstLine(r.title, REMIND.titleMax) || "A message"}</span>
          {/* When first: it is what the list is for; who sent it may be cut short. */}
          <span className={"flex min-w-0 gap-1 text-[13px] " + (r.overdue ? "font-semibold" : "text-text-2")}>
            <span className="shrink-0 tnum">{r.overdue ? "Due " + dueText(r.due) : cap(dueText(r.due))}</span>
            <span className="min-w-0 truncate">· From {from}</span>
          </span>
        </span>
      </button>
      <Button size="sm" variant="ghost" onClick={onChange}>Details</Button>
      <button type="button" disabled={busy} aria-label={"Reminder done: " + (firstLine(r.title, 40) || "a message")} title="Done"
        onClick={() => void store.changeReminder({ action: "done", message: r.message }, "Reminder change saved")}
        className="grid size-11 shrink-0 place-items-center rounded-full text-text-2 hover:bg-sunken hover:text-ink"><IconCheck size={20} /></button>
    </li>
  );
}

/** Pending presentation is separate from both saved reminders and due attention. */
export function ReminderChangeStatus({ message }: { message: string }) {
  const store = useApp();
  const p = useStore(store, s => s.reminderChanges[message]);
  if (!p) return null;
  return <div data-reminder-change={message} role={p.status === "failed" ? "alert" : "status"} className="my-2 flex flex-wrap items-center gap-2 text-sm text-text-2">
    {p.intent.title && <span className="max-w-full truncate font-semibold">{firstLine(p.intent.title, REMIND.titleMax)}</span>}
    <span>{p.status === "failed" ? "Reminder change failed: " + p.error : p.status === "confirming" ? "Reminder change saved; checking current state…" : p.intent.action === "set" ? "Saving reminder…" : "Removing reminder…"}</span>
    {p.status === "failed" && <><Button size="sm" onClick={() => void store.changeReminder(p.intent, "Reminder change saved")}>Retry reminder</Button><Button size="sm" variant="ghost" onClick={() => store.discardReminderError(message)}>Dismiss error</Button></>}
    {p.status === "confirming" && <><span>{p.error}</span><Button size="sm" onClick={() => void store.confirmReminder(message)}>Check reminder</Button></>}
  </div>;
}

/** latestReceived: the newest message in a conversation that can take a
 *  reminder (received, not a record of who came or left, not deleted). */
export function latestReceived(msgs: (T.DMMessage | T.Message)[]): Remindable | null {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.dir !== "in" || m.deleted || ("event" in m && m.event) || ("excerpt_pid" in m && m.excerpt_pid)) continue;
    return { id: m.id, text: firstLine((m.edited && m.text) || m.body, REMIND.titleMax) };
  }
  return null;
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
