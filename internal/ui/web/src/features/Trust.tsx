// Trust a changed identity, and standing permissions for a device's agent
// (MEL-528). A device whose key changed is held: nothing is sent to it and
// what it sent waits, until the person compares the new key with its owner
// by another way (in person, a call) and trusts exactly that key
// (act trust {id, key}). A browser keeps no trust of its own: it says to do
// this in AgentNet on a computer. Answering a device's questions
// automatically, and running its tasks without asking, are this computer's
// own permissions (act approve, unapprove, grant_tasks, revoke_tasks); they change
// nothing already running.
import { useEffect, useState, type ReactNode } from "react";
import { IconCheck, IconShieldExclamation } from "@tabler/icons-react";
import type { Api, T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Confirm } from "./Message.actions";
import { capital, deviceWords } from "./Approvals.words";

/** TrustSheet: compare the new code with what its owner says, then trust exactly it. */
export function TrustSheet({ open, onOpenChange, thread }: { open: boolean; onOpenChange: (o: boolean) => void; thread: T.Thread }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [same, setSame] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (open) setSame(false); }, [open]);
  const expect = thread.key.pending || ""; // the code shown is the only one trusted
  const who = deviceWords(thread.peer, o);
  const trust = async () => {
    setBusy(true);
    const ok = await store.run((a) => a.act({ do: "trust", id: thread.peer, key: expect }), "Trusted. You can write again, and what it sent meanwhile is checked again.");
    setBusy(false);
    if (ok !== undefined) onOpenChange(false);
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={"Check " + who + "’s new identity"}
      description="Its AgentNet identity changed. That happens when AgentNet is set up again, or when someone pretends to be them."
      footer={
        <div className="flex flex-col gap-2">
          <Button variant="act" size="lg" disabled={!same || busy || !expect} onClick={() => void trust()}>Trust the new identity</Button>
          <Button variant="ghost" size="lg" onClick={() => onOpenChange(false)}>Not now</Button>
        </div>
      }>
      <p className="text-[15px] leading-relaxed text-text-2">
        Ask its owner for their code in person or on a call, not in a chat here, and compare it. They find it in AgentNet under Settings → Your devices → Details → Key.
      </p>
      <dl className="mt-3 grid gap-2">
        <Code label="Code you had" value={thread.key.pinned || "none kept"} muted />
        <Code label="New code" value={expect} />
      </dl>
      <label className="mt-4 flex min-h-12 cursor-pointer items-center gap-3 rounded-2xl stroke bg-surface px-3 py-2 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink">
        <input type="checkbox" className="sr-only" checked={same} onChange={(e) => setSame(e.target.checked)} />
        <span aria-hidden="true" className={"grid size-[22px] shrink-0 place-items-center rounded-md border-[1.5px] border-outline " + (same ? "bg-act text-act-ink" : "bg-surface")}>{same && <IconCheck size={15} stroke={3} />}</span>
        <span className="text-[15px] font-semibold">The new code matches what they told me</span>
      </label>
      <p className="mt-3 text-[13px] text-muted">Until you trust it, nothing is sent to {who} and what it sent waits here, unread and unrun.</p>
    </Sheet>
  );
}

/** Code shows a key's code exactly as Settings → Your devices → Details → Key
 *  writes it (its own dash-separated groups), so both sides compare the same text. */
function Code({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className={"rounded-2xl px-3.5 py-2.5 " + (muted ? "bg-sunken" : "bg-surface stroke")}>
      <dt className="text-[12px] font-extrabold uppercase tracking-[0.05em] text-muted">{label}</dt>
      <dd className={"font-mono text-[17px] tracking-wide [overflow-wrap:anywhere] " + (muted ? "text-text-2" : "font-bold text-ink")}>{value}</dd>
    </div>
  );
}

/** TrustNotice: what the composer says while sending to a device is paused
 *  for its changed identity, with the way out (a computer) or where to go (a browser). */
export function TrustNotice({ thread }: { thread: T.Thread }) {
  const store = useApp();
  const [open, setOpen] = useState<boolean | null>(null);
  const browser = store.host.platform === "browser";
  return (
    <div className="flex items-start gap-3 border-t-[1.5px] border-outline bg-surface px-4 pt-3 pb-[max(12px,env(safe-area-inset-bottom))] lg:border-t lg:px-6 lg:py-4">
      <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-act text-act-ink stroke"><IconShieldExclamation size={20} aria-hidden="true" /></span>
      <div className="min-w-0 flex-1 py-0.5">
        <p className="font-semibold">Sending is paused</p>
        <p className="text-[13px] text-text-2">
          {browser ? "This agent’s identity changed. Trust it in AgentNet on your computer, after checking it with its owner."
            : "This agent’s identity changed. Check it with its owner before writing again."}
        </p>
        {!browser && <Button size="sm" variant="act" className="mt-2" onClick={() => setOpen(true)}>Check and trust…</Button>}
      </div>
      {open !== null && <TrustSheet open={open} onOpenChange={setOpen} thread={thread} />}
    </div>
  );
}

// ---- standing permissions for one device's agent ----------------------------------------------

export type GrantChange = "approve" | "unapprove" | "grant_tasks" | "revoke_tasks";

/** Each local permission change names the exact person ID or device address. */
const grantActs: Record<GrantChange, (peer: string) => (a: Api) => Promise<unknown>> = {
  approve: (peer) => (a) => a.act({ do: "approve", id: peer }),
  unapprove: (peer) => (a) => a.act({ do: "unapprove", id: peer }),
  grant_tasks: (peer) => (a) => a.act({ do: "grant_tasks", id: peer }),
  revoke_tasks: (peer) => (a) => a.act({ do: "revoke_tasks", id: peer }),
};

/** DeviceGrantConfirm: turn automatic answers on or off for a device, or stop
 *  running its tasks without asking, after saying what that means. */
export function DeviceGrantConfirm({ change, onClose, peer }: { change: GrantChange | null; onClose: () => void; peer: string; thread?: { task_grant?: string } }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const person = [o?.person, ...(o?.people || [])].find((p) => p?.person === peer);
  const own = !!person && person.person === o?.person?.person;
  const who = own ? "your devices" : person?.label || deviceWords(peer, o);
  const possessive = own ? "your devices’" : who + "’s";
  const host = o ? deviceWords(o.me.address, o) : "this computer";
  const [shown, setShown] = useState<GrantChange>(change || "approve");
  useEffect(() => { if (change) setShown(change); }, [change]);
  const text: Record<GrantChange, { title: string; ok: string; done: string; body: ReactNode; danger: boolean }> = {
    approve: {
      title: "Answer " + possessive + " questions automatically?", ok: "Answer automatically", danger: false,
      done: "Your agent now answers " + possessive + " questions without asking you.",
      body: <>
        <p>From now on your agent answers questions from {who} without asking you. It uses your native tools, skills and permissions unchanged. A native approval may still need your attention.</p>
        <p>Task permissions are unchanged. Questions already waiting stay waiting: answer them, or let your agent answer each one.</p>
      </>,
    },
    unapprove: {
      title: "Stop answering " + possessive + " questions automatically?", ok: "Stop automatic answers", danger: true,
      done: "This automatic question permission ended. Separate permissions are unchanged.",
      body: <p>This removes the automatic question permission on {host}. Questions need your OK unless a separate device permission or chat already allows them. An answer already being written may still finish unless you stop it.</p>,
    },
    revoke_tasks: {
      title: "Remove " + (own ? "your devices’" : who + "’s") + " task permission here?", ok: "Remove permission", danger: true,
      done: "This standing task permission ended. Other permissions and accepted tasks are unchanged.",
      body: <><p>This removes the permission for {who} on {host}. Future tasks need your OK unless a separate device permission or accepted agent invitation already allows them.</p><p>It does not stop running tasks or undo one-time approvals. Other receiving computers are unchanged.</p></>,
    },
    grant_tasks: {
      title: "Allow tasks from " + who + " without asking here?", ok: "Allow future tasks", danger: false,
      done: "Future tasks may run here with the agent’s normal permissions. Tasks already waiting stay waiting.",
      body: <><p>On {host}, your agent may run future tasks from {who} without another AgentNet approval. {person ? "This covers their current and future verified devices, including phones." : "This covers only this device’s current key."}</p><p>The agent keeps this computer’s normal tools and permissions; native approval requirements still apply. Removed devices, changed keys and frozen identities stay blocked. Tasks already waiting, failed or interrupted are not restarted. Other receiving computers are unchanged.</p></>,
    },
  };
  const w = text[shown];
  return (
    <Confirm open={!!change} onOpenChange={(v) => { if (!v) onClose(); }} title={capital(w.title)} ok={w.ok} danger={w.danger}
      onOk={() => void store.run(grantActs[shown](peer), w.done)}>
      {w.body}
    </Confirm>
  );
}
