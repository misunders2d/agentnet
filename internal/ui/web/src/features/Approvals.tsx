// OKs: everything that waits for this person's decision, newest first,
// each naming who and what. Requests open their exact conversation, where
// the approval card decides; invitations and new devices are decided here.
// Reports from other computers sit apart, folded away.
import { useState, type ReactNode } from "react";
import { IconChevronRight, IconDeviceMobile, IconUsersGroup } from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp } from "../context";
import { agentName, firstLine, personName, plain, when } from "../model";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { ScreenTitle } from "./Approvals.title";
import { ConfirmSheet, Details, Row } from "./Approvals.sheets";
import { Reports } from "./Approvals.reports";
import { askerOf, capital, inSentence, myAgentName, nameOf, peerAgent, phaseOf, placeOf, whyWords } from "./Approvals.words";
import { useNeedsYouChats, type Chats } from "./Approvals.chats";

export { ApprovalCard } from "./Approvals.card";

/** Pending invitations of this device's own agent into a DM (its person decides). */
function agentJoins(o: T.Overview) {
  return (o.person?.agents || []).filter((a) => a.address === o.me.address)
    .flatMap((a) => (a.dms || []).filter((d) => d.state === "invited").map((d) => ({ ...d, agentId: a.agent_id })));
}
const groupInvites = (o: T.Overview) => (o.group_invitations || []).filter((i) => i.direction === "in" && i.status === "pending");
const deviceAsks = (o: T.Overview) => (o.links || []).filter((l) => l.state === "pending");

/** needsYouCount: the decisions waiting here that the overview alone proves
 *  (requests to your agent in chats are only in /api/dm: see useNeedsYou). */
export function needsYouCount(o: T.Overview | null): number {
  if (!o) return 0;
  return (o.review || []).filter((r) => !r.notice).length + (o.dms || []).reduce((n, d) => n + d.held, 0)
    + groupInvites(o).length + deviceAsks(o).length + agentJoins(o).length;
}

/** A request in a chat that waits for this person: one of the OKs list's rows
 *  (a running one only offers Stop, so it doesn't count). */
const waitsForYou = (m: T.DMMessage) => { const p = phaseOf(m); return !!p && p !== "running"; };

function chatRequests(o: T.Overview | null, chats: Chats): number {
  return (o?.dms || []).reduce((n, d) => n + (chats[d.id]?.messages || []).filter(waitsForYou).length, 0);
}

/** useNeedsYou: the one count of what waits for you, for the OKs badge, the
 *  home banner and the OKs header alike: needsYouCount(overview) plus the
 *  requests to your agent in chats (read once per change, shared). */
export function useNeedsYou(): number {
  const o = useStore(useApp(), (s) => s.overview);
  const chats = useNeedsYouChats();
  return needsYouCount(o) + chatRequests(o, chats);
}

interface Item { key: string; at: string; node: ReactNode }

export function OksView() {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const loadError = useStore(store, (s) => s.loadError);
  const chats = useNeedsYouChats();
  const names = useAgentNames();
  const count = needsYouCount(o) + chatRequests(o, chats);

  if (!o) return loadError ? <Failed text={loadError} retry={() => store.retryNow()} /> : <Loading />;

  const items: Item[] = [];
  for (const r of (o.review || []).filter((r) => !r.notice)) items.push({ key: "r:" + r.id, at: r.at, node: <ReviewRow r={r} o={o} names={names} /> });
  for (const d of o.dms || []) {
    const t = chats[d.id];
    const held = (t?.messages || []).filter((m) => m.dir === "in" && m.state === "conv_held");
    if (d.held > 0 && !held.length) items.push({ key: "h:" + d.id, at: d.last_at, node: <HeldCountRow d={d} /> });
    for (const m of held) items.push({ key: "h:" + m.id, at: m.at, node: <HeldRow m={m} t={t} o={o} /> });
    for (const m of (t?.messages || []).filter(waitsForYou)) items.push({ key: "q:" + m.id, at: m.at, node: <RequestRow m={m} t={t} o={o} names={names} /> });
  }
  for (const j of agentJoins(o)) {
    const a = (chats[j.conv]?.agents || []).find((x) => x.pid === j.pid);
    items.push({ key: "j:" + j.pid, at: a?.invited || "", node: <JoinRow conv={j.conv} pid={j.pid} a={a} t={chats[j.conv]} o={o} names={names} agentId={j.agentId} /> });
  }
  for (const g of groupInvites(o)) items.push({ key: "g:" + g.id, at: "", node: <GroupRow g={g} o={o} /> });
  for (const l of deviceAsks(o)) items.push({ key: "d:" + l.id, at: l.requested_at, node: <DeviceRow l={l} /> });
  // Newest first; what carries no time (an invitation) leads.
  items.sort((a, b) => (!a.at ? -1 : !b.at ? 1 : b.at.localeCompare(a.at)));
  const notices = (o.review || []).filter((r) => r.notice);

  return (
    <div className="pb-8">
      <header className="px-4 pt-5 pb-1">
        <ScreenTitle>OKs</ScreenTitle>
        {count > 0 && <p className="pt-2 text-[15px] font-semibold text-text-2 tnum">{count === 1 ? "1 thing needs you" : count + " things need you"}</p>}
      </header>
      {items.length ? (
        <div className="px-4 pt-3">
          <ul className="flex flex-col gap-3" aria-label="Waiting for you">
            {items.map((i) => <li key={i.key} className="fade-in">{i.node}</li>)}
          </ul>
        </div>
      ) : <AllClear />}
      {notices.length > 0 && <Reports notices={notices} o={o} />}
    </div>
  );
}

// ---- rows ---------------------------------------------------------------------

function Card({ children, onOpen, label, current }: { children: ReactNode; onOpen?: () => void; label?: string; current?: boolean }) {
  const cls = "relative flex w-full gap-3 rounded-2xl bg-surface p-3.5 text-left stroke " + (current ? "ring-2 ring-agent-ink ring-offset-2 ring-offset-canvas" : "");
  if (!onOpen) return <article className={cls}>{children}</article>;
  return (
    <button type="button" onClick={onOpen} aria-label={label} aria-current={current || undefined} className={cls + " press shadow-pop-sm hover:bg-sunken"}>
      {children}
      <span className="grid size-9 shrink-0 self-center place-items-center rounded-full bg-ink text-canvas" aria-hidden="true"><IconChevronRight size={20} stroke={2.5} /></span>
    </button>
  );
}

function Body({ tag, at, title, quote, meta, children }: { tag?: ReactNode; at?: string; title: ReactNode; quote?: string; meta?: string; children?: ReactNode }) {
  return (
    <div className="min-w-0 flex-1">
      {(tag || at) && (
        <div className="mb-1 flex items-center gap-2">
          {tag}
          {at && <time dateTime={at} className="tnum ml-auto text-[12px] font-semibold text-muted">{when(at)}</time>}
        </div>
      )}
      <p className="text-[16px] font-bold leading-snug [overflow-wrap:anywhere]">{title}</p>
      {quote && <p className="pt-1 line-clamp-2 text-[15px] leading-snug text-text-2 [overflow-wrap:anywhere]">“{quote}”</p>}
      {meta && <p className="pt-1.5 text-[13px] font-medium text-muted">{meta}</p>}
      {children}
    </div>
  );
}

const kindTag = (kind: string) => kind === "task" ? <Tag tone="agent">Task</Tag> : kind === "question" ? <Tag tone="agent">Question</Tag> : <Tag tone="muted">Follow-up</Tag>;

function useOpen() {
  const store = useApp();
  const open = useStore(store, (s) => s.open);
  return {
    isOpen: (id: string, focus?: string) => !!open && open.id === id && (!focus || open.focus === focus),
    go: (kind: "dm" | "thread", id: string, focus?: string) => void store.open({ kind, id, focus }),
  };
}

function ReviewRow({ r, o, names }: { r: T.ReviewItem; o: T.Overview; names: Record<string, string> }) {
  const { isOpen, go } = useOpen();
  const from = capital(peerAgent(r.peer, o, names).name);
  const mine = o.person ? agentName(undefined, names, o.person, o.person) : "Your agent";
  const title = r.kind === "task" ? from + " gave " + inSentence(mine) + " a task" : r.kind === "question" ? from + " asked " + inSentence(mine) + " something"
    : mine + " needs you about " + from + "’s message";
  return (
    <Card onOpen={() => go("thread", r.id, r.id)} current={isOpen(r.id)} label={title + ". Review it."}>
      <AgentAvatar seed={r.peer} size={40} mood="waiting" />
      <Body tag={kindTag(r.kind)} at={r.at} title={title} quote={firstLine(r.excerpt, 160)} meta={whyWords(r.why, r.peer, o)} />
    </Card>
  );
}

function RequestRow({ m, t, o, names }: { m: T.DMMessage; t: T.DMThread; o: T.Overview; names: Record<string, string> }) {
  const { isOpen, go } = useOpen();
  const asker = askerOf(m, o, names, t);
  const who = asker.name;
  const agent = myAgentName(m, names, o, t);
  const phase = phaseOf(m);
  const what = m.kind === "task" ? "task" : "question";
  const title = phase === "needs_human" ? capital(agent) + " needs you for " + (who === "You" ? "your " : who + "’s ") + what
    : phase === "stopped" ? capital(who === "You" ? "your " : who + "’s ") + what + " for " + agent + " didn’t finish"
    : m.kind === "task" ? who + " gave " + agent + " a task" : who + " asked " + agent + " something";
  return (
    <Card onOpen={() => go("dm", t.id, m.id)} current={isOpen(t.id, m.id)} label={title + ". Review it."}>
      {asker.agent ? <AgentAvatar seed={asker.seed} size={40} /> : <PersonAvatar name={asker.you ? "Me" : asker.name} seed={asker.seed} size={40} />}
      <Body tag={phase === "needs_human" ? <Tag tone="act">Needs you</Tag> : phase === "stopped" ? <Tag tone="muted">Didn’t finish</Tag> : kindTag(m.kind)}
        at={m.at} title={title} quote={firstLine(plain(m.body), 160)} meta={capital(placeOf(t, null, o))} />
    </Card>
  );
}

function HeldRow({ m, t, o }: { m: T.DMMessage; t: T.DMThread; o: T.Overview }) {
  const { isOpen, go } = useOpen();
  const who = nameOf(m.from, o);
  const title = m.kind === "task" ? who + " gave you a task" : who + " asked you something";
  return (
    <Card onOpen={() => go("dm", t.id, m.id)} current={isOpen(t.id, m.id)} label={title + ". Answer in the chat."}>
      <PersonAvatar name={who} seed={(t.kind === "group" ? "" : t.peer.person) || m.from} size={40} />
      <Body tag={kindTag(m.kind)} at={m.at} title={title} quote={firstLine(plain(m.body), 160)}
        meta={capital(placeOf(t, null, o)) + " · For you, not your agent"} />
    </Card>
  );
}

function HeldCountRow({ d }: { d: T.DMSummary }) {
  const { isOpen, go } = useOpen();
  const who = d.kind === "group" ? d.title || "A group" : personName(d.peer);
  const title = d.held === 1 ? who + " has a question for you" : who + " has " + d.held + " things for you";
  return (
    <Card onOpen={() => go("dm", d.id)} current={isOpen(d.id)} label={title + ". Open the chat."}>
      <PersonAvatar name={who} seed={d.kind === "group" ? d.id : d.peer.person || d.peer.address} size={40} />
      <Body at={d.last_at} title={title} meta="For you, not your agent. Answer in the chat." />
    </Card>
  );
}

function JoinRow({ conv, pid, a, t, o, names, agentId }: { conv: string; pid: string; a?: T.AgentView; t?: T.DMThread; o: T.Overview; names: Record<string, string>; agentId?: string }) {
  const store = useApp();
  const { go } = useOpen();
  const [busy, setBusy] = useState(false);
  const agent = inSentence(o.person ? agentName(agentId, names, o.person, o.person) : (agentId && names[agentId]) || "Your agent");
  const by = a ? (a.inviter.person && a.inviter.person === o.person?.person ? "You" : personName(a.inviter)) : "Someone";
  const where = t ? placeOf(t, null, o) : "into a chat";
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((api) => api.decideAgent(pid, accept), accept ? capital(agent) + " joined." : "Declined. " + capital(agent) + " stays out.");
    setBusy(false);
  };
  const seen = (a?.shared || []).length;
  const tasks = (a?.tasks_from || []).filter((p) => p.person !== o.person?.person).map(personName);
  return (
    <Card>
      <AgentAvatar seed={agentId || o.me.address} size={40} mood="waiting" />
      <Body tag={<Tag tone="act">Needs your OK</Tag>} at={a?.invited}
        title={by + " invited " + agent + " " + where.replace(/^in /, "into ")} quote={a?.note ? firstLine(a.note, 160) : undefined}
        meta={a ? (seen ? "It would see " + (seen === 1 ? "1 earlier message" : seen + " earlier messages") : "It would see nothing earlier") + " and what’s asked of it." : undefined}>
        {tasks.length > 0 && <p className="pt-1 text-[13px] font-semibold text-approval-ink">{tasks.join(" and ")} could give it tasks without asking you.</p>}
        <div className="mt-3 flex flex-wrap gap-2">
          <Button variant="act" size="sm" disabled={busy} onClick={() => decide(true)}>Let it join</Button>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => decide(false)}>Decline</Button>
          {t && <Button variant="ghost" size="sm" onClick={() => go("dm", conv)}>Open chat</Button>}
        </div>
      </Body>
    </Card>
  );
}

function GroupRow({ g, o }: { g: T.GroupInvitationView; o: T.Overview }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const by = nameOf(g.inviter, o);
  const shared = (g.history || []).length;
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((api) => api.decideGroup(g.id, accept), accept ? "Accepted. You’re in once the group’s admin confirms." : "Declined.");
    setBusy(false);
  };
  return (
    <Card>
      <span className="grid size-10 shrink-0 place-items-center rounded-full bg-agent text-agent-ink stroke" aria-hidden="true"><IconUsersGroup size={20} /></span>
      <Body tag={<Tag tone="muted">Group invite</Tag>} title={<>{by} invited you to <span className="font-display">“{g.title || "a group"}”</span></>}
        meta={shared ? "They share " + (shared === 1 ? "1 earlier message" : shared + " earlier messages") + " with you." : "Nothing earlier is shared with you."}>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button variant="act" size="sm" disabled={busy} onClick={() => decide(true)}>Join</Button>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => decide(false)}>No thanks</Button>
        </div>
      </Body>
    </Card>
  );
}

function DeviceRow({ l }: { l: T.LinkRequest }) {
  const store = useApp();
  const [sure, setSure] = useState(false);
  const [busy, setBusy] = useState(false);
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((api) => api.decideDevice(l.id, accept), accept ? "“" + l.name + "” is now one of your devices." : "Refused. It didn’t join as you.");
    setBusy(false);
  };
  return (
    <Card>
      <span className="grid size-10 shrink-0 place-items-center rounded-full bg-guest-bg text-guest-ink stroke" aria-hidden="true"><IconDeviceMobile size={20} /></span>
      <Body tag={<Tag tone="muted">New device</Tag>} at={l.requested_at} title={<>A new device, “{l.name}”, wants to join as you</>}
        meta={"Approve it only if you just opened your link on it yourself. The request ends " + when(l.expires) + "."}>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button variant="act" size="sm" disabled={busy} onClick={() => setSure(true)}>Approve device</Button>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => decide(false)}>Refuse</Button>
        </div>
      </Body>
      <ConfirmSheet open={sure} onOpenChange={setSure} title={"Is “" + l.name + "” your device?"}
        body="Once approved it is you: it sends and receives your messages, and your chats are copied to it."
        confirm="Yes, it’s mine" onConfirm={() => decide(true)}>
        <div className="pt-2">
          <Details>
            <Row k="Device">{l.address}</Row>
            <Row k="Key"><span className="tnum">{l.fingerprint}</span></Row>
            <Row k="Asked">{new Date(l.requested_at).toLocaleString()}</Row>
          </Details>
        </div>
      </ConfirmSheet>
    </Card>
  );
}

// ---- states -------------------------------------------------------------------

function AllClear() {
  return (
    <div className="flex flex-col items-center px-8 pt-12 pb-6 text-center">
      <div className="relative grid size-36 place-items-center rounded-full bg-ok-bg" aria-hidden="true">
        <span className="absolute inset-3 rounded-full border-2 border-dashed border-ok-ink/30" />
        <AgentAvatar seed="all-clear" size={72} mood="done" />
        <span className="absolute right-3 bottom-4 grid size-9 rotate-6 place-items-center rounded-full bg-surface text-ok-ink stroke shadow-pop-sm">
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth={3} strokeLinecap="round" strokeLinejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
        </span>
      </div>
      <h2 className="pt-6 font-display text-[22px] font-bold">Nothing needs you</h2>
      <p className="pt-2 max-w-[300px] text-[15px] text-text-2">When someone asks your agent for something only you can allow, or invites you somewhere, it waits here.</p>
    </div>
  );
}

function Loading() {
  return (
    <div className="px-4 pt-5" aria-busy="true" aria-label="Loading">
      <div className="h-7 w-24 rounded-lg bg-sunken" />
      <div className="mt-6 flex flex-col gap-3">
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex gap-3 rounded-2xl border-[1.5px] border-hairline p-3.5 motion-safe:animate-pulse">
            <div className="size-10 rounded-xl bg-sunken" />
            <div className="flex-1"><div className="h-4 w-3/4 rounded bg-sunken" /><div className="mt-2 h-3.5 w-1/2 rounded bg-sunken" /></div>
          </div>
        ))}
      </div>
    </div>
  );
}

function Failed({ text, retry }: { text: string; retry: () => void }) {
  return (
    <div className="px-6 pt-16 text-center" role="alert">
      <h2 className="font-display text-[22px] font-bold">Couldn’t load what needs you</h2>
      <p className="pt-2 text-text-2">{text}</p>
      <Button className="mt-4" onClick={retry}>Try again</Button>
    </div>
  );
}
