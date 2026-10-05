// The room's cards: a guest with exactly what they saw, an invitation that
// waits for someone, a request waiting for an owner's OK, a past guest, and
// the read-only sheet of what a guest was shown.
import { useState } from "react";
import { IconArrowBackUp, IconDoorExit, IconEye, IconHandStop } from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp } from "../context";
import { plain, timeOf, when } from "../model";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar, type Mood } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { callName, exposure, guestUpdateDraft, guestUpdatePeople, lineOf, plural, shortName, speaker, type Guest, type Waiting } from "./RoomPanel.model";

export type Act = (g: Guest, what: "accept" | "decline" | "dismiss" | "cancel" | "leave") => void;

function GuestAvatar({ g, size, mood, past }: { g: Guest; size: 32 | 40; mood?: Mood; past?: boolean }) {
  return g.kind === "agent"
    ? <AgentAvatar seed={g.seed} size={size} guest={!past && !g.member} device={g.device} mood={mood || (g.online === false ? "asleep" : g.working ? "working" : "neutral")} />
    : <PersonAvatar name={g.name} seed={g.seed} size={size} guest={!past} online={past ? undefined : g.online} />;
}

export function GuestCard({ g, t, busy, onAct }: { g: Guest; t: T.DMThread; busy: boolean; onAct: Act }) {
  const [seeing, setSeeing] = useState(false);
  const x = exposure(t, g, timeOf);
  const asleep = g.kind === "agent" && g.online === false;
  const line = [asleep ? g.line.replace(/ · ([^·]+)$/, " · asleep · $1 offline") : g.line, g.sinceAt && "since " + timeOf(g.sinceAt)].filter(Boolean).join(" · ");
  return (
    <article aria-label={g.name + (g.member ? ", member" : ", guest")} className="pop-in rounded-2xl stroke bg-guest-bg p-3 shadow-pop-sm">
      <div className="flex items-center gap-3">
        <GuestAvatar g={g} size={40} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5"><b className="truncate text-[15.5px] font-bold">{g.name}{g.kind === "person" && g.hostHere && " (you)"}</b>{!g.member && <Tag tone="guest">Guest</Tag>}</div>
          <div className={"truncate text-[13px] font-semibold " + (g.kind === "agent" ? "text-agent-ink" : "text-text-2")}>{line}</div>
        </div>
      </div>
      {(g.kind === "agent" || g.state === "conflict") && <p className="mt-2 text-[13px] font-semibold text-danger">{g.stateText}</p>}
      <div className="mt-2.5 rounded-xl border border-outline/20 bg-surface px-2.5 py-2">
        <div className="flex flex-wrap items-baseline justify-between gap-x-2 text-[13px] font-bold">{x.label}{!x.cells.length && x.range && <span className="whitespace-nowrap text-[12px] font-semibold text-muted tnum">{x.range}</span>}</div>
        {x.cells.length > 0 && (
          <>
            <div className="mt-1.5 flex gap-[3px]" aria-hidden="true">
              {x.cells.map((on, i) => <i key={i} className={"block h-3 max-w-5 flex-1 rounded-[3px] " + (on ? "bg-guest shadow-[inset_0_0_0_1.5px_var(--an-outline)]" : "bg-hairline")} />)}
            </div>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[13px] font-semibold text-muted">
              <span aria-hidden="true" className="flex items-center gap-1 whitespace-nowrap"><i className="block size-2.5 rounded-[2px] bg-guest" />Shared</span>
              <span aria-hidden="true" className="flex items-center gap-1 whitespace-nowrap"><i className="block size-2.5 rounded-[2px] bg-hairline" />Earlier: private</span>
              <span className="ml-auto whitespace-nowrap text-[12px] tnum">{x.range}</span>
            </div>
          </>
        )}
      </div>
      <div className="mt-2.5 flex flex-wrap gap-2">
        <Button size="sm" icon={<IconEye size={18} />} onClick={() => setSeeing(true)}>{g.hostHere && g.kind === "person" ? "What you saw" : "What " + shortName(g) + " saw"}</Button>
        {(g.can.dismiss || g.can.end) && <Button size="sm" disabled={busy} icon={<IconHandStop size={18} />} onClick={() => onAct(g, "dismiss")}>{g.member ? "Remove agent" : "Dismiss"}</Button>}
        {g.can.leave && <Button size="sm" disabled={busy} icon={<IconDoorExit size={18} />} onClick={() => onAct(g, "leave")}>Leave</Button>}
      </div>
      {(g.can.dismiss || g.can.end) && (
        <p className="mt-2 text-[13px] leading-snug text-text-2">
          {g.kind === "agent" ? "Can be asked while here. Removing it stops new requests; what was already shared stays with it." : "Stops new messages. What was already shared stays with them."}
        </p>
      )}
      {g.can.leave && <p className="mt-2 text-[13px] leading-snug text-text-2">You stop getting new messages. What you saw stays with you.</p>}
      <WhatTheySaw t={t} g={g} open={seeing} onOpenChange={setSeeing} />
    </article>
  );
}

/** PendingCard: an invitation that waits for its host (or for the invited person). */
export function PendingCard({ g, t, busy, onAct }: { g: Guest; t: T.DMThread; busy: boolean; onAct: Act }) {
  const [seeing, setSeeing] = useState(false);
  const mine = g.can.decide;
  const store = useApp(), me = useStore(store, s => s.overview?.person?.label);
  const waiting=(t.guests||[]).find(x=>x.pid===g.pid)?.needs_update||[];
  const waitFor = waiting.length?"Waiting for "+waiting.map(label => label === me ? "your other device" : label).join(", ")+" to update AgentNet":g.kind === "agent" ? (g.hostHere ? "Waiting for your OK" : "Waiting for " + g.hostName + "’s OK") : g.hostHere ? "You’re invited to help" : "Waiting for " + g.name + " to join";
  return (
    <article aria-label={g.name + ", invited"} className={"pop-in rounded-2xl p-3 " + (mine ? "stroke bg-surface shadow-pop" : "border-2 border-dashed border-outline/40")}>
      <div className="flex items-center gap-3">
        <GuestAvatar g={g} size={32} mood="waiting" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5"><b className="truncate text-[15px] font-bold">{g.name}{g.kind === "person" && g.hostHere && " (you)"}</b><Tag tone={mine ? "act" : "muted"}>Invited</Tag></div>
          <div className={"text-[13px] font-semibold " + (mine ? "text-approval-ink" : "text-text-2")}>{waitFor}</div>
        </div>
      </div>
      {g.state === "pending" && <p className="mt-2 text-[13px] text-muted">{g.stateText}</p>}
      {mine && g.kind === "agent" && g.invitedBy !== "you" && (
        <p className="mt-2 text-[14px] leading-snug text-text-2">
          <b className="font-bold text-ink">{g.invitedBy}</b> invited it{g.note ? ":" : "."}
          {g.note && <span className="mt-1 block rounded-xl bg-sunken px-2.5 py-1.5 text-ink">“{g.note}”</span>}
        </p>
      )}
      {mine && (
        <p className="mt-2 text-[14px] leading-snug text-text-2">
          {g.kind === "agent"
            ? "It sees only " + (g.shared.length ? plural(g.shared.length, "earlier message") : "what someone asks it here") + (g.member ? ", then new group turns while it is a member" : "") + ". Only you can let it in."
            : g.line + ". You’d see " + (g.shared.length + g.missing ? plural(g.shared.length + g.missing, "earlier message") + ", then new ones" : "new messages") + " while you’re here."}
        </p>
      )}
      <div className="mt-2.5 flex flex-wrap gap-2">
        {mine && <Button size="sm" variant="act" disabled={busy} onClick={() => onAct(g, "accept")}>{g.kind === "agent" ? "Let it join" : "Join"}</Button>}
        {mine && <Button size="sm" disabled={busy} onClick={() => onAct(g, "decline")}>Decline</Button>}
        <Button size="sm" variant="ghost" icon={<IconEye size={18} />} onClick={() => setSeeing(true)}>{g.kind === "agent" ? "What it would see" : g.hostHere ? "What you’d see" : "What " + g.name + " would see"}</Button>
        {waiting.length>0 && <AskUpdate t={t} pid={g.pid} />}
        {!mine && (g.can.dismiss || g.can.end) && <Button size="sm" variant="ghost" disabled={busy} onClick={() => onAct(g, "cancel")}>Cancel invite</Button>}
      </div>
      <WhatTheySaw t={t} g={g} open={seeing} onOpenChange={setSeeing} pending />
    </article>
  );
}

export function WaitingRow({ w, onShow }: { w: Waiting; onShow: () => void }) {
  return (
    <div className="flex items-center gap-3 rounded-2xl border-2 border-dashed border-outline/40 py-2 pl-3 pr-1.5">
      <div className="min-w-0 flex-1">
        <b className="block truncate text-[14px] font-bold">{w.asker} → {w.agent}</b>
        <span className="block truncate text-[13px] text-text-2">{w.text}</span>
        <span className={"block text-[13px] font-semibold " + (w.mine ? "text-approval-ink" : "text-text-2")}>{w.mine ? "Waiting for your OK" : "Only " + w.decider + " can OK this"}</span>
      </div>
      <Button size="sm" variant={w.mine ? "act" : "ghost"} onClick={onShow}>{w.mine ? "Review" : "Show"}</Button>
    </div>
  );
}

export function PastRow({ g, onBringBack }: { g: Guest; onBringBack?: () => void }) {
  const what = g.state === "declined" ? ["declined"] : !g.joined ? ["invite cancelled" + (g.endedBy ? " by " + g.endedBy : "")] : [sawWords(g.shared.length + g.missing), endedWords(g)];
  const at = g.endedAt || g.invitedAt;
  return (
    <li className="flex min-h-12 items-center gap-3 py-1">
      <span className="grayscale-[.5]"><GuestAvatar g={g} size={32} mood="asleep" past /></span>
      <div className="min-w-0 flex-1">
        <b className="block truncate text-[14px] font-bold text-text-2">{g.name}</b>
        <span className="block text-[13px] leading-snug text-muted">{[at && when(at), ...what].filter(Boolean).join(" · ")}</span>
      </div>
      {onBringBack && <Button size="sm" variant="ghost" icon={<IconArrowBackUp size={18} />} onClick={onBringBack}>{g.joined ? "Bring back" : "Invite again"}</Button>}
    </li>
  );
}

/** "saw 3" / "saw nothing": how much earlier history a past guest was shown. */
const sawWords = (n: number) => (n ? "saw " + n : "saw nothing");

/** "dismissed by you" / "dismissed by Vitalii" / "left": who ended the visit, from its record. */
const endedWords = (g: Guest) => (g.endedBy ? "dismissed by " + g.endedBy : g.endedBy === "" ? "left" : "dismissed");

/** WhatTheySaw: a read-only sheet of exactly the messages shared with a guest. */
export function WhatTheySaw({ t, g, open, onOpenChange, pending }: { t: T.DMThread; g: Guest; open: boolean; onOpenChange: (v: boolean) => void; pending?: boolean }) {
  const who = g.hostHere && g.kind === "person" ? "you" : callName(g.name);
  const title = pending ? "What " + who + (who === "you" ? "’d" : " would") + " see" : "What " + who + " saw";
  const then = g.kind === "agent" ? "It also sees what someone asks it here." : "Plus new messages while " + (who === "you" ? "you’re" : "they’re") + " here.";
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={title.charAt(0).toUpperCase() + title.slice(1)}
      description={(g.shared.length + g.missing ? "Exactly " + plural(g.shared.length + g.missing, "message") + ", nothing earlier. " : "Nothing from before. ") + then}>
      {g.missing > 0 && (
        <p className="mb-3 rounded-xl bg-sunken px-3 py-2 text-[13px] font-semibold text-text-2">
          {pending && g.hostHere ? plural(g.missing, "message") + " arrive once you join." : plural(g.missing, "shared message") + (g.missing === 1 ? " isn’t" : " aren’t") + " on this computer, so " + (g.missing === 1 ? "it" : "they") + " can’t be shown."}
        </p>
      )}
      <SawList t={t} shared={g.shared} />
    </Sheet>
  );
}

// SawList: the shared messages, each with its author as this chat names them.
function SawList({ t, shared }: { t: T.DMThread; shared: T.DMMessage[] }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  return (
    <ol className="flex flex-col gap-3 pb-2">
      {shared.map((m) => {
        const s = speaker(m, t, o, names);
        return (
          <li key={m.id} className="flex gap-2.5">
            {s.agent ? <AgentAvatar seed={s.seed} size={28} /> : <PersonAvatar name={s.me ? o?.person?.label || "You" : s.name} seed={s.seed} size={28} />}
            <div className="min-w-0 flex-1 rounded-2xl stroke bg-surface px-3 py-2">
              <div className="flex items-baseline justify-between gap-2"><b className="text-[14px] font-bold">{s.name}</b><time className="text-[12px] text-muted tnum">{when(m.at)}</time></div>
              <p className="whitespace-pre-wrap break-words text-[15px] leading-snug">{plain(m.text ?? m.body) || lineOf(m)}</p>
            </div>
          </li>
        );
      })}
    </ol>
  );
}

function AskUpdate({ t, pid }: { t: T.DMThread; pid: string }) {
  const store = useApp(), o = useStore(store, s => s.overview);
  const guest = (t.guests || []).find(g => g.pid === pid);
  if (!guest) return null;
  const people = guestUpdatePeople(guest.needs_update || [], [guest.host, t.peer, ...(t.members || []), o?.person], o?.person?.person);
  return <>{people.map(p => <Button key={p.person || p.address} size="sm" onClick={() => void store.run(async api => {
    const existing = o?.dms?.find(d => d.peer.person === p.person || d.peer.address === p.address);
    const id = existing?.id || (await api.newDM(p.address)).id;
    await store.open({kind: "dm", id});
    store.setDraft(id, {...store.draft(id), text: guestUpdateDraft(t.peer.person === p.person || t.peer.address === p.address)});
  })}>Ask {p.label} to update</Button>)}</>;
}
