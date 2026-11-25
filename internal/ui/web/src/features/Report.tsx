// Report a message or a person to the workspace's admins (Google Play
// user-generated content policy; docs/DECISIONS.md, November 25, 2026). The
// report is the reporter's own ordinary message to each admin person: chats
// are end-to-end encrypted, so admins see only what the report quotes. They
// act with the existing controls (Remove). The relay's admin flag is a hint
// for addressing only and grants nothing.
import { useRef, useState, type FormEvent } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export interface ReportSubject {
  name: string;       // who is reported, as shown
  address: string;    // the device the reported message or person speaks through
  person?: string;    // their verified person, when known
  where?: string;     // the chat's title
  message?: string;   // the reported message's text; absent when reporting a person
}

export interface ReportAdmin { key: string; label: string; address: string; person?: string }

// reportAdmins lists the admin persons a report goes to: one row per person
// (or per device without a person), never the reporter or the reported.
export function reportAdmins(o: T.Overview | undefined, subject: { address: string; person?: string }): ReportAdmin[] {
  if (!o) return [];
  const admin = new Set((o.directory?.members || []).filter(m => m.admin).map(m => m.address));
  const me = o.person?.person, mine = new Set([o.me.address, ...(o.person?.devices || []).map(d => d.address)]);
  const out = new Map<string, ReportAdmin>();
  for (const address of admin) {
    if (mine.has(address) || address === subject.address) continue;
    const p = (o.people || []).find(p => p.address === address || (p.devices || []).some(d => d.address === address));
    if (p?.person && (p.person === me || p.person === subject.person)) continue;
    const key = p?.person || address;
    if (!out.has(key)) out.set(key, { key, label: p?.label || niceDevice(address), address: p?.address || address, person: p?.person });
  }
  return [...out.values()].sort((a, b) => a.label.localeCompare(b.label));
}

export function reportBody(s: ReportSubject, note: string, withMessage: boolean) {
  const line = (t: string) => t.replace(/[\r\n]+/g, " ").trim();
  const parts = ["**Report to workspace admins**", "Reported: " + line(s.name) + " (" + s.address + ")"];
  if (s.where) parts.push("Chat: " + line(s.where));
  if (withMessage && s.message) parts.push("Reported message:\n" + s.message.split("\n").map(l => "> " + l).join("\n"));
  parts.push("Reason: " + (note.trim() || "none given"));
  return parts.join("\n\n");
}

export function ReportSheet({ subject, open, onOpenChange }: { subject: ReportSubject; open: boolean; onOpenChange: (open: boolean) => void }) {
  const store = useApp();
  const [note, setNote] = useState(""), [withMessage, setWithMessage] = useState(true), [error, setError] = useState("");
  const sending = useRef(false);
  // The same send IDs and chats make a retry the same reports, never more.
  const ids = useRef(new Map<string, string>()), chats = useRef(new Map<string, string>());
  const admins = reportAdmins(store.get().overview || undefined, subject);
  const names = admins.map(a => a.label).join(", ");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (sending.current || !admins.length) return;
    sending.current = true; setError("");
    const body = reportBody(subject, note, withMessage);
    onOpenChange(false);
    store.toast("Sending report to " + names + "…");
    void (async () => {
      try {
        for (const a of admins) {
          let conv = chats.current.get(a.key);
          if (!conv) {
            const dms = store.get().overview?.dms || [];
            conv = dms.find(d => (a.person && d.peer.person === a.person) || d.peer.address === a.address)?.id || (await store.api.newDM(a.address)).id;
            chats.current.set(a.key, conv);
          }
          if (!ids.current.has(a.key)) ids.current.set(a.key, sendID());
          await store.api.sendDM({ id: ids.current.get(a.key), conv, body });
        }
        store.toast("Report sent to " + names, "ok");
        setNote(""); ids.current.clear();
        void store.refetch();
      } catch (err) {
        setError("The report was not sent: " + errorText(err) + ". Try again.");
        onOpenChange(true);
      } finally { sending.current = false; }
    })();
  };
  return <Sheet open={open} onOpenChange={value => { if (!value) setError(""); onOpenChange(value); }} title={"Report " + subject.name}>
    <form onSubmit={submit} className="space-y-4">
      {admins.length
        ? <p className="text-[13px] text-muted">Goes to this workspace’s admins: {names}. They see only what you send here, and can remove people who break the rules.</p>
        : <p role="alert" className="text-danger">This workspace has no other admin to send a report to. Contact the person who runs it.</p>}
      {subject.message !== undefined && <label className="flex min-h-11 items-start gap-2">
        <input type="checkbox" className="mt-1" checked={withMessage} onChange={e => setWithMessage(e.target.checked)} />
        <span><span className="font-semibold">Include the message</span><span className="mt-1 line-clamp-3 block text-[13px] text-muted">{subject.message}</span></span>
      </label>}
      <label className="block font-semibold">What’s wrong? (optional)
        <textarea rows={3} value={note} maxLength={2000} onChange={e => setNote(e.target.value)} className="mt-1 block w-full rounded-xl bg-surface p-3 font-normal stroke" />
      </label>
      {error && <p role="alert" className="text-danger">{error}</p>}
      <Button type="submit" variant="act" disabled={!admins.length}>{error ? "Try again" : "Send report"}</Button>
    </form>
  </Sheet>;
}
