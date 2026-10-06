// OKs: everything that waits for this person's decision, newest first,
// each naming who and what, straight from the overview: requests to your
// agent and invitations for it decided here (needs_you), review decisions,
// group invitations and new devices: exactly what the header counts.
// Questions held for you, items another device of yours decides, and quiet
// notices follow; reports from other computers sit apart, folded away.
// Every row opens its exact conversation, where the approval card decides
// the same way. An invitation card also reads its chat once, to name what
// your agent would see there.
import { useLayoutEffect, useRef, useState } from "react";
import { IconDeviceMobile, IconUsersGroup } from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp } from "../context";
import { agentName, firstLine, isWorkingItem, when } from "../model";
import { focusedIn } from "../owned";
import { useStore } from "../store";
import { AgentAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { ScreenTitle } from "./Approvals.title";
import { ConfirmSheet, Details, Row } from "./Approvals.sheets";
import { Reports } from "./Approvals.reports";
import { Body, Card, OpenCard, Landing, kindTag, useLand, useOpen } from "./Approvals.parts";
import { ConvRow, SelfConsentRow } from "./Approvals.conv";
import { HeldBack } from "./Approvals.held";
import { capital, decidable, inSentence, isSelfConsent, nameOf, peerAgent, whyWords } from "./Approvals.words";

export { ApprovalCard } from "./Approvals.card";

const groupInvites = (o: T.Overview) => (o.group_invitations || []).filter((i) => i.direction === "in" && i.status === "pending");
const deviceAsks = (o: T.Overview) => (o.links || []).filter((l) => l.state === "pending");
const reviewAsks = (o: T.Overview) => (o.review || []).filter((r) => !r.notice);

/** needsYouCount: the decisions waiting for this device, as the overview
 *  lists them: requests to your agent and invitations for it that are
 *  decided here (not on another device), review decisions, group
 *  invitations to you and new devices. Notices and questions held for you
 *  are listed in OKs but are not decisions, so they are not counted. */
export function needsYouCount(o: T.Overview | null): number {
  if (!o) return 0;
  return decidable(o).length + reviewAsks(o).length + groupInvites(o).length + deviceAsks(o).length;
}

/** useNeedsYou: the one count of what waits for you, for the OKs badge, the
 *  home banner and the OKs header alike. */
export function useNeedsYou(): number {
  return needsYouCount(useStore(useApp(), (s) => s.overview));
}

interface Item { key: string; at: string; node: React.ReactNode }

// Newest first; what carries no time (an invitation) leads.
const newestFirst = (a: { at: string }, b: { at: string }) => (!a.at && !b.at ? 0 : !a.at ? -1 : !b.at ? 1 : b.at.localeCompare(a.at));

/** useLanding keeps focus on the screen when a card's decision ends: the
 *  clicked button was disabled while it ran, and the card itself goes once
 *  the overview drops it. Focus then moves to the card if it is still
 *  there, else the next one (or the one before), else the screen title.
 *  It never takes focus that something else on the page holds. */
function useLanding(cards: string) {
  const root = useRef<HTMLDivElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const want = useRef<{ key: string; order: string[]; until: number } | null>(null);
  const settle = () => {
    const w = want.current, box = root.current;
    if (!w || !box) return;
    if (Date.now() > w.until) { want.current = null; return; }
    const a = focusedIn(box) as HTMLElement | null; // focus inside the skin's own tree
    const lost = !a || a === document.body || (a instanceof HTMLButtonElement && a.disabled);
    const card = (k: string) => box.querySelector<HTMLElement>("[data-card=\"" + CSS.escape(k) + "\"]");
    const target = (li: HTMLElement) => li.querySelector<HTMLElement>("[data-open]") || li.querySelector<HTMLElement>("button:not([disabled])") || li;
    const here = card(w.key);
    if (here) { if (lost) target(here).focus(); return; } // still listed: wait until the overview drops it
    want.current = null;
    if (!lost) return;
    const i = w.order.indexOf(w.key);
    const next = w.order.slice(i + 1).map(card).find(Boolean) || w.order.slice(0, Math.max(i, 0)).reverse().map(card).find(Boolean);
    (next ? target(next) : title.current)?.focus();
  };
  useLayoutEffect(settle, [cards]);
  const land = (key: string) => {
    const order = [...(root.current?.querySelectorAll<HTMLElement>("[data-card]") || [])].map((e) => e.dataset.card || "");
    want.current = { key, order, until: Date.now() + 15000 };
    requestAnimationFrame(settle);
  };
  return { root, title, land };
}

/** Listed: one card in a list, with its key for landing focus. */
function Listed({ k, land, children }: { k: string; land: (key: string) => void; children: React.ReactNode }) {
  return <li data-card={k} className="fade-in"><Landing.Provider value={() => land(k)}>{children}</Landing.Provider></li>;
}

export function OksView() {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const loadError = useStore(store, (s) => s.loadError);
  const names = useAgentNames();
  const count = needsYouCount(o);

  const items: Item[] = [];
  const elsewhere: T.ConvItem[] = [];
  const working: T.ConvItem[] = [];
  if (o) {
    for (const r of reviewAsks(o)) items.push({ key: "r:" + r.id, at: r.at, node: <ReviewRow r={r} o={o} names={names} /> });
    for (const c of o.needs_you || []) {
      if (isWorkingItem(c)) working.push(c);
      else if (c.decide_on) elsewhere.push(c); // decided on another device: listed apart, not counted
      else items.push({ key: "n:" + c.conv + ":" + (c.id || c.pid), at: c.at, node: <ConvRow c={c} o={o} /> });
    }
    for (const g of groupInvites(o)) items.push({ key: "g:" + g.id, at: "", node: <GroupRow g={g} o={o} /> });
    for (const l of deviceAsks(o)) items.push({ key: "d:" + l.id, at: l.requested_at, node: <DeviceRow l={l} /> });
    items.sort(newestFirst);
    elsewhere.sort(newestFirst);
    working.sort(newestFirst);
  }
  const held = [...(o?.held || [])].sort(newestFirst);
  const joined = (o?.review || []).filter(isSelfConsent).sort(newestFirst);
  const security = (o?.review || []).filter((r) => r.reason === "device_admin").sort(newestFirst);
  const notices = (o?.review || []).filter((r) => r.notice && !isSelfConsent(r) && r.reason !== "device_admin");
  const keys = [...items.map((i) => i.key), ...working.map((c) => "w:" + c.conv + ":" + c.id), ...held.map((c) => "h:" + c.conv + ":" + c.id), ...elsewhere.map((c) => "e:" + c.conv + ":" + (c.id || c.pid)), ...joined.map((r) => "s:" + r.id), ...security.map((r) => "a:" + r.id)];
  const { root, title, land } = useLanding(keys.join("\n"));

  if (!o) return loadError ? <Failed text={loadError} retry={() => store.retryNow()} /> : <Loading />;

  return (
    <div ref={root} className="pb-8">
      <header className="px-4 pt-5 pb-1">
        <ScreenTitle ref={title}>OKs</ScreenTitle>
        {count > 0 && <p className="pt-2 text-[15px] font-semibold text-text-2 tnum">{count === 1 ? "1 thing needs you" : count + " things need you"}</p>}
      </header>
      {items.length ? (
        <div className="px-4 pt-3">
          <ul className="flex flex-col gap-3" aria-label="Waiting for you">
            {items.map((i) => <Listed key={i.key} k={i.key} land={land}>{i.node}</Listed>)}
          </ul>
        </div>
      ) : !held.length && !elsewhere.length && !working.length && <AllClear />}
      {working.length > 0 && (
        <section className="px-4 pt-6" aria-labelledby="oks-working">
          <h2 id="oks-working" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Working</h2>
          <p className="pt-1 text-[14px] text-text-2">These requests are already running. Stop them here if needed.</p>
          <ul className="flex flex-col gap-3 pt-3" aria-label="Working">
            {working.map((c) => { const k = "w:" + c.conv + ":" + c.id; return <Listed key={k} k={k} land={land}><ConvRow c={c} o={o} /></Listed>; })}
          </ul>
        </section>
      )}
      {held.length > 0 && (
        <section className="px-4 pt-6" aria-labelledby="oks-held">
          <h2 id="oks-held" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Asked of you</h2>
          <p className="pt-1 text-[14px] text-text-2">Answer them in the chat if you want to. Nothing runs them.</p>
          <ul className="flex flex-col gap-3 pt-3">
            {held.map((c) => { const k = "h:" + c.conv + ":" + c.id; return <Listed key={k} k={k} land={land}><ConvRow c={c} o={o} /></Listed>; })}
          </ul>
        </section>
      )}
      {elsewhere.length > 0 && (
        <section className="px-4 pt-6" aria-labelledby="oks-elsewhere">
          <h2 id="oks-elsewhere" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Decided on your other devices</h2>
          <p className="pt-1 text-[14px] text-text-2">Your agent runs on another of your devices, so you decide these there.</p>
          <ul className="flex flex-col gap-3 pt-3">
            {elsewhere.map((c) => { const k = "e:" + c.conv + ":" + (c.id || c.pid); return <Listed key={k} k={k} land={land}><ConvRow c={c} o={o} /></Listed>; })}
          </ul>
        </section>
      )}
      {(joined.length > 0 || security.length > 0) && (
        <section className="px-4 pt-6" aria-labelledby="oks-notices">
          <h2 id="oks-notices" className="text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Just so you know</h2>
          <ul className="flex flex-col gap-3 pt-3">
            {security.map((r) => <Listed key={"a:" + r.id} k={"a:" + r.id} land={land}><DeviceAdminRow r={r} /></Listed>)}
            {joined.map((r) => <Listed key={"s:" + r.id} k={"s:" + r.id} land={land}><SelfConsentRow r={r} o={o} names={names} /></Listed>)}
          </ul>
        </section>
      )}
      {notices.length > 0 && <Reports notices={notices} o={o} />}
      <HeldBack o={o} />
    </div>
  );
}

function DeviceAdminRow({ r }: { r: T.ReviewItem }) {
  const store = useApp(), land = useLand();
  const [busy, setBusy] = useState(false);
  return <article className="rounded-2xl border-[1.5px] border-ink/15 bg-sunken p-3.5">
    <Body at={r.at} title={r.why}>
      <Button variant="ghost" size="sm" disabled={busy} onClick={async () => {
        setBusy(true);
        await store.run(api => api.act({ do: "resolve", id: r.id }), "Notice hidden on this device. Company settings access is unchanged.");
        setBusy(false); land();
      }}>Hide notice</Button>
    </Body>
  </article>;
}

// ---- rows ---------------------------------------------------------------------

function ReviewRow({ r, o, names }: { r: T.ReviewItem; o: T.Overview; names: Record<string, string> }) {
  const { isOpen, go } = useOpen();
  const from = capital(peerAgent(r.peer, o, names).name);
  const mine = o.person ? agentName(undefined, names, o.person, o.person) : "Your agent";
  const title = r.kind === "task" ? from + " gave " + inSentence(mine) + " a task" : r.kind === "question" ? from + " asked " + inSentence(mine) + " something"
    : mine + " needs you about " + from + "’s message";
  return (
    <OpenCard onOpen={() => go("thread", r.id, r.id)} current={isOpen(r.id)} label={title + ". Review it."} face={<AgentAvatar seed={r.peer} size={40} mood="waiting" />} detail={<details><summary className="cursor-pointer font-semibold">Read the whole message</summary><p className="pt-2 whitespace-pre-wrap [overflow-wrap:anywhere]">{whyWords(r.why, r.peer, o)}</p></details>}>
      <Body tag={kindTag(r.kind)} at={r.at} title={title} quote={firstLine(r.excerpt, 160)} />
    </OpenCard>
  );
}

function GroupRow({ g, o }: { g: T.GroupInvitationView; o: T.Overview }) {
  const store = useApp();
  const land = useLand();
  const [busy, setBusy] = useState(false);
  const by = nameOf(g.inviter, o);
  const shared = (g.history || []).length;
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((api) => api.decideGroup(g.id, accept), accept ? "Accepted. You’re in once the group’s admin confirms." : "Declined.");
    setBusy(false);
    land();
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
  const land = useLand();
  const [sure, setSure] = useState(false);
  const [busy, setBusy] = useState(false);
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((api) => api.decideDevice(l.id, accept), accept ? "“" + l.name + "” is now one of your devices." : "Refused. It didn’t join as you.");
    setBusy(false);
    land();
  };
  return (
    <Card>
      <span className="grid size-10 shrink-0 place-items-center rounded-full bg-guest-bg text-guest-ink stroke" aria-hidden="true"><IconDeviceMobile size={20} /></span>
      <Body tag={<Tag tone="muted">New device</Tag>} at={l.requested_at} title={<>A new device, “{l.name}”, wants to join as you</>}
        meta={"Approve it only if you just signed in with Google or opened your device link on it yourself. The request ends " + when(l.expires) + "."}>
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
