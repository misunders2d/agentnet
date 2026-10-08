// "In this chat": who is here (members), who is helping (guests: people and
// agents, each with exactly what they were shown), who is invited, which
// requests wait for an owner's OK, and who has left. Every button comes from
// the server's can_* flags; dismissing stops what is new, never what was shared.
import { useEffect, useState } from "react";
import { IconLock, IconUserPlus, IconX } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useAgentNames, useApp, useWide } from "../context";
import { useStore, type Store } from "../store";
import { PersonAvatar } from "../ui/Avatar";
import { Button, IconButton } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { GuestCard, PastRow, PendingCard, WaitingRow, type Act } from "./RoomPanel.cards";
import { agentRejoinState, callName, canBringIn, room, shareable, type Guest, type GroupInvite } from "./RoomPanel.model";
import { BringBackSnack, type Snack } from "./RoomPanel.snack";
import { GroupFooter, MemberMenu, groupRights, useGroupChange } from "./GroupAdmin";

/** RoomPanel is the desktop side panel: open on request, and on its own while
 *  someone is helping or waiting to be let in (until closed for that state). */
export function RoomPanel() {
  const store = useApp();
  const wide = useWide();
  const roomy = useMedia("(min-width: 1280px)");
  const open = useStore(store, (s) => s.open);
  const t = useStore(store, (s) => s.dm);
  const o = useStore(store, (s) => s.overview);
  const panel = useStore(store, (s) => s.panel);
  const [closed, setClosed] = useState("");
  const [snack, setSnack] = useState<Snack | null>(null);
  const current = wide && open?.kind === "dm" && t?.id === open.id ? t : null;
  const r = current ? room(current, o, {}) : null; // who is here, for opening; names come with the body
  const busy = r ? [...r.guests, ...r.invited].map((g) => g.key + g.state).join(",") + "|" + r.waiting.map((w) => w.id).join(",") + "|" + r.groupInvites.map(i => i.id + i.state).join(",") : "";
  const sig = current ? current.id + "|" + busy : "";
  const attention = !!r && r.guests.length + r.invited.length + r.waiting.length + r.groupInvites.length > 0;
  const shown = !!current && !!r && (panel || (roomy && attention && closed !== sig));
  const close = () => { store.setPanel(false); setClosed(sig); };
  useEffect(() => setSnack(null), [current?.id]); // a snackbar belongs to its conversation

  useEffect(() => {
    if (!shown || roomy) return;
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") close(); };
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  });

  if (!shown || !current || !r) return null;

  return (
    <>
      {!roomy && <div aria-hidden="true" onClick={close} className="fade-in fixed inset-0 z-30 bg-[#1B1530]/30" />}
      <aside aria-label="In this chat"
        className={"fade-in flex w-[360px] shrink-0 flex-col border-l-[1.5px] border-outline bg-surface " + (roomy ? "h-full" : "fixed inset-y-0 right-0 z-40 shadow-pop")}>
        <header className="flex h-16 shrink-0 items-center justify-between gap-3 border-b-[1.5px] border-outline px-5">
          <h2 className="font-display text-[20px] font-extrabold tracking-tight">In this chat</h2>
          <IconButton label="Close panel" onClick={close} className="stroke bg-surface"><IconX size={18} /></IconButton>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5">
          <RoomBody t={current} onDismissed={(s) => { store.setPanel(true); setSnack(s); }} />
          {snack && <BringBackSnack key={snack.id} snack={snack} onClose={() => setSnack(null)}
            onBringBack={agentRejoinState(current, snack.pid) ? undefined : () => { setSnack(null); bringIn(store, current, true, snack.who, undefined, snack.pid); }} />}
        </div>
      </aside>
    </>
  );
}

/** RoomSheet is the same room on phones, opened from the conversation's "In this chat". */
export function RoomSheet() {
  const store = useApp();
  const wide = useWide();
  const open = useStore(store, (s) => s.open);
  const t = useStore(store, (s) => s.dm);
  const o = useStore(store, (s) => s.overview);
  const panel = useStore(store, (s) => s.panel);
  const [snack, setSnack] = useState<Snack | null>(null);
  const current = !wide && open?.kind === "dm" && t?.id === open.id ? t : null;
  // After a dismissal the sheet stays open with the snackbar at its foot, so
  // nothing covers the conversation and its new "left" divider.
  return (
    <Sheet open={panel && !!current} onOpenChange={(v) => { if (!v) { store.setPanel(false); setSnack(null); } }} title="In this chat">
      {current && <RoomBody t={current} onDismissed={setSnack} />}
      {snack && current && <BringBackSnack key={snack.id} snack={snack} onClose={() => setSnack(null)}
        onBringBack={agentRejoinState(current, snack.pid) ? undefined : () => { setSnack(null); bringIn(store, current, false, snack.who, undefined, snack.pid); }} />}
    </Sheet>
  );
}

/** bringIn opens "Bring someone in" for t; for "Bring back", with that person
 *  or agent chosen and what was said since they left already selected. */
async function bringIn(store: Store, t: T.DMThread, wide: boolean, who?: string, since?: string, pid?: string) {
  if (pid) {
    try {
      const latest = await store.api.dm(t.id);
      const state = agentRejoinState(latest, pid);
      if (state) { store.toast(state, "ok"); await store.refetch(); return; }
      t = latest;
    } catch (e) { store.toast(errorText(e), "error"); return; }
  }
  const later = since ? shareable(t).filter((m) => m.at > since).map((m) => m.id) : [];
  if (!wide) store.setPanel(false);
  store.openInvite(t.id, later.length ? later : undefined, who ? { who, label: "Since they left" } : undefined);
}

function GroupInviteCard({ invite: i }: { invite: GroupInvite }) {
  const store = useApp();
  const [action, setAction] = useState<"cancel" | "refresh" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async () => {
    if (!action || busy) return;
    setBusy(true); setError("");
    try {
      if (action === "cancel") await store.api.cancelGroup(i.id);
      else await store.api.refreshGroup(i.id);
      store.toast(action === "cancel" ? "Invitation retracted. Their device updates when it connects." : "Fresh invitation sent. They need to accept it again.", "ok");
      setAction(null);
      await store.refetch();
    } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  return <li className="min-w-0 rounded-2xl stroke bg-sunken p-3">
    <div className="flex items-center gap-3"><PersonAvatar name={i.name} seed={i.target} size={36} />
      <div className="min-w-0 flex-1"><p className="break-words font-bold">{i.name}</p>
        <p className="text-[13px] text-text-2">{i.state === "accepted" ? "Accepted · waiting to join" : i.state === "pending" ? "Invited as a member · waiting for them to accept" : "Invitation needs refreshing"}</p>
      </div>
    </div>
    {(i.canCancel || i.canRefresh) && <div className="mt-2 flex flex-wrap gap-2">
      {i.canCancel && <Button size="sm" variant="outline" onClick={() => { setAction("cancel"); setError(""); }}>Retract invitation</Button>}
      {i.canRefresh && <Button size="sm" variant="ghost" onClick={() => { setAction("refresh"); setError(""); }}>Refresh invitation</Button>}
    </div>}
    <Sheet open={!!action} onOpenChange={v => { if (!v && !busy) setAction(null); }}
      title={(action === "cancel" ? "Retract invitation for " : "Send a fresh invitation to ") + i.name + "?"}
      footer={<Button variant="act" disabled={busy} onClick={() => void run()}>{busy ? "Saving…" : action === "cancel" ? "Retract invitation" : "Send fresh invitation"}</Button>}>
      <p>{action === "cancel" ? "This invitation will no longer let them join. Their device learns about the cancellation when it connects. Existing members stay in the group." : "The old invitation is replaced with the group’s current details and the same selected history. They must review and accept the fresh invitation."}</p>
      {error && <p role="alert" className="mt-3 text-danger">{error}</p>}
    </Sheet>
  </li>;
}

function RoomBody({ t, onDismissed }: { t: T.DMThread; onDismissed: (s: Snack) => void }) {
  const store = useApp();
  const wide = useWide();
  const o = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const r = room(t, o, names);
  const [busy, setBusy] = useState("");

  const invitable = canBringIn(t);
  const rights = groupRights(t, o);
  const change = useGroupChange(t);
  const memberOf = (person?: string) => (person ? (t.members || []).find((x) => x.person === person) : undefined);

  // act runs one decision or dismissal; the change stream then redraws the room.
  const act: Act = async (g, what) => {
    if (busy) return;
    setBusy(g.key);
    const api = store.api;
    const name = callName(g.name);
    const ok = await store.run<unknown>(
      () => what === "accept" ? (g.kind === "agent" ? api.decideAgent(g.pid, true) : api.decideGuest(g.pid, true))
        : what === "decline" ? (g.kind === "agent" ? api.decideAgent(g.pid, false) : api.decideGuest(g.pid, false))
        : g.kind === "agent" ? api.dismissAgent(g.pid) : api.endGuest(g.pid),
      what === "accept" ? (g.kind === "agent" ? cap(name) + " joined — it sees only what was shared with it" : "You joined — you’ll see new messages while you’re here")
        : what === "decline" ? "Declined. Nothing was shared." : what === "cancel" ? "Invitation cancelled" : what === "leave" ? "You left. What you saw stays with you." : undefined,
    );
    setBusy("");
    if (ok && what === "dismiss") onDismissed({ name: g.name, kind: g.kind, who: g.who, pid: g.kind === "agent" ? g.pid : undefined, id: Date.now() });
  };

  return (
    <div className="relative flex flex-col pb-2">
      <Label n={r.members.length}>Members</Label>
      <ul className="flex flex-col">
        {r.members.map((m) => (
          <li key={m.key} className="flex min-h-12 items-center gap-3 py-1">
            <PersonAvatar name={m.name} seed={m.seed} size={36} online={m.online} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5"><b className="truncate text-[15px] font-bold">{m.name}{m.me && " (you)"}</b>{m.admin && <Tag>Admin</Tag>}</div>
              {m.email && <div className="truncate text-sm text-muted">{m.email} · verified by this workspace</div>}
              {(m.note || m.online != null) && <div className="truncate text-[13px] font-semibold text-text-2">{m.note || (m.online ? "Online" : "Offline")}</div>}
            </div>
            {rights.admin && memberOf(m.person) && <MemberMenu t={t} member={memberOf(m.person)!} me={m.me} onPick={change.pick} />}
          </li>
        ))}
      </ul>
      {t.kind === "group" && t.frozen && <p className="mt-1 text-[13px] text-muted">The last members this computer could confirm.</p>}
      {rights.member && <GroupFooter admin={rights.admin} onPick={(a) => change.pick(a)} />}
      {change.sheet}

      {r.guests.some(g => g.member) && <><Label n={r.guests.filter(g => g.member).length}>Agents</Label><div className="flex flex-col gap-2.5">{r.guests.filter(g => g.member).map(g => <GuestCard key={g.key} g={g} t={t} busy={busy === g.key} onAct={act} />)}</div></>}
      <Label n={r.guests.filter(g => !g.member).length}>Guests</Label>
      {r.guests.length > 0 ? (
        <div className="flex flex-col gap-2.5">{r.guests.filter(g => !g.member).map((g) => <GuestCard key={g.key} g={g} t={t} busy={busy === g.key} onAct={act} />)}</div>
      ) : (
        <div className="rounded-2xl border-2 border-dashed border-hairline px-4 py-3.5 text-[14px] text-text-2">
          No one is helping here right now.{invitable && " Bring in a person or an agent — they see only what you share."}
        </div>
      )}
      {invitable && (
        <Button size="sm" variant="outline" className="mt-2.5 self-start" icon={<IconUserPlus size={18} />} onClick={() => bringIn(store, t, wide)}>Bring someone in</Button>
      )}

      {r.groupInvites.length > 0 && <>
        <Label n={r.groupInvites.length}>Invited people</Label>
        <ul aria-label="Invited people" className="space-y-2">{r.groupInvites.map(i => <GroupInviteCard key={i.id} invite={i} />)}</ul>
      </>}

      {r.invited.length > 0 && <>
        <Label n={r.invited.length}>Invited</Label>
        <div className="flex flex-col gap-2">{r.invited.map((g) => <PendingCard key={g.key} g={g} t={t} busy={busy === g.key} onAct={act} />)}</div>
      </>}

      {r.incomplete.length > 0 && <details className="mt-4 rounded-xl bg-sunken px-3.5 py-2 text-[13px] text-text-2">
        <summary className="cursor-pointer font-semibold">Incomplete context</summary>
        <ul className="mt-2 space-y-2">{r.incomplete.map(g => <li key={g.pid} className="whitespace-pre-wrap [overflow-wrap:anywhere]">{g.stateText || "Participation records are incomplete; no invitation has been verified."}</li>)}</ul>
      </details>}

      {r.waiting.length > 0 && <>
        <Label n={r.waiting.length}>Waiting on an OK</Label>
        <div className="flex flex-col gap-2">{r.waiting.map((w) => <WaitingRow key={w.id} w={w} onShow={() => { void store.open({ kind: "dm", id: t.id, focus: w.id }); if (!wide) store.setPanel(false); }} />)}</div>
      </>}

      {r.past.length > 0 && <>
        <Label>Past guests</Label>
        <ul className="flex flex-col">{r.past.map((g) => <PastRow key={g.key} g={g} returnState={g.kind === "agent" ? agentRejoinState(t, g.pid) : ""} onBringBack={invitable && canReturn(g, t, o) ? () => bringIn(store, t, wide, g.who, g.joined ? g.endedAt : undefined, g.kind === "agent" ? g.pid : undefined) : undefined} />)}</ul>
      </>}

      <p className="mt-5 flex gap-2 border-t-2 border-dashed border-hairline pt-3 text-[13px] font-semibold leading-snug text-text-2">
        <IconLock size={16} className="mt-0.5 shrink-0" aria-hidden="true" />
        Guests see only what was shared with them. Once dismissed, the chat carries on privately.
      </p>
    </div>
  );
}

/** canReturn: a past guest can be invited again the same way they first came. */
const canReturn = (g: Guest, t: T.DMThread, o: T.Overview | null) =>
  g.kind === "agent" ? !!o?.agents && !agentRejoinState(t, g.pid) : t.kind !== "group" && !g.hostHere;

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

function Label({ n, children }: { n?: number; children: string }) {
  return (
    <h3 className="mb-2 mt-5 flex items-center gap-2 text-[13px] font-extrabold uppercase tracking-wide text-text-2">
      {children}
      {n != null && <span className="grid h-5 min-w-5 place-items-center rounded-full bg-sunken px-1.5 text-[12px] tracking-normal text-ink tnum">{n}</span>}
    </h3>
  );
}

function useMedia(q: string): boolean {
  const [on, setOn] = useState(() => matchMedia(q).matches);
  useEffect(() => {
    const m = matchMedia(q);
    const f = () => setOn(m.matches);
    m.addEventListener("change", f);
    return () => m.removeEventListener("change", f);
  }, [q]);
  return on;
}
