// Reports from other computers: another device of yours (or a server you
// look after) said requests wait for a person there. A report is that
// device's snapshot, never a live queue; its newest one replaces the older
// ones. A steward's devices get the requests by name, each actionable: the
// decision goes to that device, which applies it only if nothing changed.
// Any other device is told how many wait and who decides them from their
// own devices (MEL-532): never "go to that machine".
import { useEffect, useState } from "react";
import { Collapsible } from "@base-ui/react/collapsible";
import { IconChevronDown, IconDevices } from "@tabler/icons-react";
import type { T } from "../api";
import { useApp } from "../context";
import { when } from "../model";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { capital, deviceWords, kindWord } from "./Approvals.words";

type Item = T.ReportItem;
type Decision = "accept" | "decline" | "reply" | "resolve" | "cancel";

// What a device takes in each state it reports (client.admitDecision); it
// still refuses anything no longer possible when the decision arrives.
const byState: Record<string, Decision[]> = {
  awaiting: ["accept", "decline"], held: ["accept", "reply", "decline"], needs_human: ["reply", "resolve"], running: ["cancel"],
  interrupted: ["accept"], failed: ["accept"], cancelled: ["accept"],
};
const stateWords: Record<string, string> = {
  awaiting: "waits for an OK", held: "waits for an OK", needs_human: "needs a person", running: "is running", pending: "is queued",
  accepted: "was accepted", interrupted: "was interrupted", failed: "didn’t finish", cancelled: "was stopped", declined: "was declined",
  resolved: "was closed", answered: "was answered", cancel_requested: "is stopping", queued: "is queued", not_run: "didn’t run", stopped: "was stopped",
};
const label = (d: Decision, again: boolean) =>
  ({ accept: again ? "Run again there" : "Allow there", decline: "Decline there", reply: "Answer there", resolve: "Close there", cancel: "Stop there" })[d];

/** Who decides a host's requests, as its report names them: a sentence for
 * the card, never a command or "decide on that device". */
export function decidersWords(report: T.Report | undefined, host: string, o: T.Overview | null): string {
  const who = report?.deciders || [];
  const me = o?.person?.person;
  const where = deviceWords(host, o);
  if (me && who.some((d) => d.person === me)) return "You decide these; this device gets them by name in " + where + "’s next report.";
  if (who.length === 0) return "Nobody can decide these from their devices yet. Whoever installed " + where + " can name a steward on that machine.";
  const names = who.map((d) => (d.person ? d.label || "Someone" : capital(deviceWords(d.address || "", o))));
  const list = names.length === 1 ? names[0] : names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
  return list + (names.length === 1 ? " decides" : " decide") + " these from their devices.";
}

export function Reports({ notices, o }: { notices: T.ReviewItem[]; o: T.Overview }) {
  const hosts = [...new Set(notices.map((n) => n.peer))];
  return (
    <Collapsible.Root className="mx-4 mt-6 rounded-2xl border-[1.5px] border-ink/15 bg-sunken">
      <Collapsible.Trigger className="group flex min-h-14 w-full items-center gap-3 rounded-2xl px-4 text-left">
        <IconDevices size={20} className="shrink-0 text-text-2" aria-hidden="true" />
        <span className="flex-1 font-bold">Reports from other computers</span>
        <span className="tnum grid h-6 min-w-6 place-items-center rounded-full bg-surface px-1.5 text-[13px] font-bold text-text-2 stroke">{hosts.length}</span>
        <IconChevronDown size={20} className="shrink-0 transition-transform duration-200 group-data-[panel-open]:rotate-180" aria-hidden="true" />
      </Collapsible.Trigger>
      <Collapsible.Panel className="overflow-hidden px-4 pb-4">
        <p className="text-[14px] text-text-2">Another computer said requests wait for a person there. Each card says who decides them from their own devices.</p>
        <div className="pt-3">
          <ul className="flex flex-col gap-3">
            {hosts.map((h) => <HostReport key={h} host={h} notices={notices.filter((n) => n.peer === h)} o={o} />)}
          </ul>
        </div>
      </Collapsible.Panel>
    </Collapsible.Root>
  );
}

function HostReport({ host, notices, o }: { host: string; notices: T.ReviewItem[]; o: T.Overview }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  // The newest snapshot says what waits there now; older ones are history.
  const withReport = notices.filter((n) => n.report).sort((a, b) =>
    b.report!.at - a.report!.at || b.at.localeCompare(a.at) || (b.report!.items || []).length - (a.report!.items || []).length);
  const latest = withReport[0];
  const newest = [...notices].sort((a, b) => b.at.localeCompare(a.at))[0];
  const items = latest?.report?.items || [];
  const waiting = items.length || latest?.report?.count || 0;
  const where = deviceWords(host, o);
  const dismiss = async () => {
    setBusy(true);
    for (const n of notices) await store.run((api) => api.act({ do: "resolve", id: n.id }));
    store.toast("Dismissed here. A newer report from " + where + " shows again if requests still wait.", "ok");
    setBusy(false);
  };
  return (
    <li className="rounded-2xl bg-surface p-3.5 stroke">
      <div className="flex items-baseline gap-2">
        <p className="flex-1 font-bold leading-snug">{capital(where)}: {latest && waiting ? (waiting === 1 ? "1 request waits" : waiting + " requests wait") : "something waits for a person"}</p>
        <time dateTime={newest.at} className="tnum shrink-0 text-[13px] text-muted">{when(newest.at)}</time>
      </div>
      {latest && items.length > 0 ? (
        <div className="pt-2">
          <ul className="flex flex-col gap-2">
            {items.map((x) => <ReportRow key={x.id} x={x} host={host} report={latest} o={o} />)}
          </ul>
        </div>
      ) : <p className="pt-1 text-[14px] text-text-2">{decidersWords(latest?.report, host, o)}</p>}
      <button type="button" disabled={busy} onClick={dismiss}
        className="mt-2 -ml-2 inline-flex min-h-11 items-center rounded-xl px-2 text-[14px] font-bold text-text-2 underline decoration-ink/30 underline-offset-4 hover:bg-sunken disabled:opacity-50">
        {notices.length > 1 ? "Dismiss these reports" : "Dismiss this report"}
      </button>
    </li>
  );
}

function ReportRow({ x, host, report, o }: { x: Item; host: string; report: T.ReviewItem; o: T.Overview }) {
  const store = useApp();
  const [pick, setPick] = useState<Decision | null>(null);
  const decided = x.result && !x.result.refused;
  // A DM or group request is decided here, never answered by hand: its
  // answer belongs in that conversation.
  const acts = x.actionable && !decided ? (byState[x.state] || []).filter((d) => !(x.conv && d === "reply")) : [];
  const again = ["interrupted", "failed", "cancelled"].includes(x.state);
  const from = x.from ? capital(deviceWords(x.from, o)) : "Someone";
  return (
    <li className="rounded-xl bg-sunken px-3 py-2.5">
      <p className="text-[15px] leading-snug"><b>{from}</b> {x.kind === "task" ? "gave it a task" : x.kind === "question" ? "asked it something" : "sent a " + kindWord(x.kind)} · {stateWords[x.state] || x.state}</p>
      <p className="pt-0.5 line-clamp-2 text-[14px] text-text-2 [overflow-wrap:anywhere]">{x.excerpt ? "“" + x.excerpt + "”" : "Its text isn’t shared with this device."}</p>
      {x.result && (
        <p className={"mt-1 text-[13px] font-semibold " + (x.result.refused ? "text-danger" : "text-ok-ink")}>
          {x.result.refused ? "Not applied: " + x.result.refused : "Sent from here. Now it " + (stateWords[x.result.state] || "is " + x.result.state) + " there."}
        </p>
      )}
      {acts.length > 0 ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {acts.map((d, i) => <Button key={d} size="sm" variant={i === 0 ? "act" : "outline"} onClick={() => setPick(d)}>{label(d, again)}</Button>)}
        </div>
      ) : !decided && <p className="pt-1 text-[13px] text-muted">{decidersWords(report.report, host, o)}</p>}
      <OperatorSheet open={!!pick} decision={pick} x={x} where={deviceWords(host, o)} again={again}
        onOpenChange={(v) => { if (!v) setPick(null); }}
        onSend={(text) => store.run((api) => api.decide({ host, id: x.id, key: x.key, action: pick!, expect: x.state, attempt: x.attempt, text, report: report.id }), "Sent to " + deviceWords(host, o) + ".")} />
    </li>
  );
}

function OperatorSheet({ open, decision, x, where, again, onOpenChange, onSend }: {
  open: boolean; decision: Decision | null; x: Item; where: string; again: boolean; onOpenChange: (v: boolean) => void; onSend: (text: string) => void;
}) {
  const [text, setText] = useState("");
  useEffect(() => { if (open) setText(""); }, [open]);
  if (!decision) return null;
  const needText = decision === "reply" || decision === "decline";
  const what = { accept: again ? "It starts fresh there, with that device’s usual permissions." : "It runs there, with that device’s usual permissions.",
    decline: "Nothing runs. The sender is told why.", reply: "Your answer goes to the sender from that device.",
    resolve: "It’s closed there. Nothing is sent.", cancel: "It stops there. Whatever it already did stays done." }[decision];
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={label(decision, again) + "?"}
      description={"On " + where + ". It applies this only if the request still " + (stateWords[x.state] || "is as reported") + "."}
      footer={
        <div className="flex flex-col gap-2">
          <Button variant={decision === "decline" || decision === "cancel" ? "danger" : "act"} size="lg" disabled={needText && !text.trim()}
            onClick={() => { onOpenChange(false); onSend(text.trim()); }}>{label(decision, again)}</Button>
          <Button variant="ghost" size="lg" onClick={() => onOpenChange(false)}>Cancel</Button>
        </div>
      }>
      {x.excerpt && <p className="rounded-xl bg-sunken px-3.5 py-2.5 text-text-2 [overflow-wrap:anywhere]">“{x.excerpt}”</p>}
      <p className="pt-3 text-text-2">{what}</p>
      {needText && <>
        <label htmlFor="operator-text" className="mt-4 block text-[14px] font-bold">{decision === "reply" ? "Your answer" : "Why, in a few words"}</label>
        <textarea id="operator-text" rows={3} value={text} onChange={(e) => setText(e.target.value)} maxLength={4000}
          className="mt-1.5 w-full resize-none rounded-2xl bg-surface stroke px-3.5 py-2.5 text-[16px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
      </>}
    </Sheet>
  );
}
