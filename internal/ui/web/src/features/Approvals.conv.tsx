// OKs rows for conversation items, straight from the overview
// (needs_you, held, and self-consent notices in review). Each row opens
// its exact conversation at the item; its buttons are the item's own
// actions[], decided here only when no other device decides it.
import { useEffect, useState } from "react";
import { IconDeviceDesktop, IconDeviceLaptop, IconDeviceMobile } from "@tabler/icons-react";
import type { Api, T } from "../api";
import { useApp } from "../context";
import { agentName, deviceKind, firstLine, isWorkingItem } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { ConfirmSheet, DeclineSheet } from "./Approvals.sheets";
import { Body, OpenCard, kindTag, useLand, useOpen } from "./Approvals.parts";
import { Reason, capital, chatName, convTitle, deviceWords, inChat, personOf, senderOf, whyWords } from "./Approvals.words";

type Props = { c: T.ConvItem; o: T.Overview };

/** ConvRow: one item of needs_you or held, by why it waits. */
export function ConvRow({ c, o }: Props) {
  if (c.reason === Reason.invite) return <InviteRow c={c} o={o} />;
  if (c.reason === Reason.heldTurn) return <HeldTurnRow c={c} o={o} />;
  return <RequestRow c={c} o={o} />;
}

/** The face of whoever sent an item: a person, or you. */
function SenderFace({ c, o }: Props) {
  const p = personOf(c.peer, o);
  const who = senderOf(c, o);
  return <PersonAvatar name={who === "You" ? o.person?.label || "Me" : who} seed={p?.person || p?.address || c.peer} size={40} />;
}

/** DecideOn: an item another device of yours decides (this one runs no agent). */
function DecideOn({ address, o }: { address: string; o: T.Overview }) {
  const kind = deviceKind(address);
  const Icon = kind === "phone" ? IconDeviceMobile : kind === "laptop" ? IconDeviceLaptop : IconDeviceDesktop;
  return (
    <p className="mt-1 flex items-center gap-1.5 text-[14px] font-semibold text-text-2">
      <Icon size={18} aria-hidden="true" className="shrink-0" />Decide on {deviceWords(address, o)}
    </p>
  );
}

/** useAct runs one decision of a row; when it ends, focus lands on the
 *  next card (the clicked button was disabled while it ran). */
function useAct() {
  const store = useApp();
  const land = useLand();
  const [busy, setBusy] = useState("");
  const run = async (key: string, fn: (api: Api) => Promise<unknown>, ok: string) => {
    setBusy(key);
    await store.run(fn, ok);
    setBusy("");
    land();
  };
  return { busy, run };
}

/** A request to your agent: waiting for a one-time OK, or for a person. */
function RequestRow({ c, o }: Props) {
  const { isOpen, go } = useOpen();
  const { busy, run } = useAct();
  const [sheet, setSheet] = useState<"" | "decline" | "close" | "stop">("");
  const acts = c.decide_on ? [] : c.actions || [];
  const can = (a: string) => acts.includes(a);
  const human = c.reason === Reason.needsHuman;
  const retry = human || c.reason === Reason.interrupted;
  const working = isWorkingItem(c);
  const title = convTitle(c, o);
  const who = senderOf(c, o);
  const kind = c.kind === "task" ? "task" : "question";
  const said = human && c.why ? whyWords(c.why, c.peer, o) : "";
  const actions = c.decide_on ? (working ? <p className="mt-1 text-[14px] text-text-2">Working on {deviceWords(c.decide_on, o)}.</p> : <DecideOn address={c.decide_on} o={o} />) : acts.length ? <>
    {can("cancel") && <Button variant="danger" size="sm" disabled={!!busy} onClick={() => setSheet("stop")}>Stop</Button>}
    {can("accept") && <Button variant={retry ? "outline" : "act"} size="sm" disabled={!!busy}
      onClick={() => run("accept", (api) => api.act({ do: "accept", id: c.id }), retry ? "Running it again." : "Allowed once.")}>
      {busy === "accept" ? (retry ? "Starting…" : "Allowing…") : human ? "Ask again" : retry ? "Run it again" : "Allow once"}
    </Button>}
    {can("decline") && <Button variant="outline" size="sm" disabled={!!busy} onClick={() => setSheet("decline")}>Decline</Button>}
    {can("resolve") && <Button variant="ghost" size="sm" disabled={!!busy} aria-label="Mark as handled" onClick={() => setSheet("close")}>Mark as handled</Button>}
  </> : undefined;
  return (
    <>
      <OpenCard onOpen={() => go("dm", c.conv, c.id)} current={isOpen(c.conv, c.id)} label={title + ". Open it in the chat."}
        face={human ? <AgentAvatar seed={c.pid || c.peer} size={40} /> : <SenderFace c={c} o={o} />} actions={actions}
        detail={said ? <details><summary className="cursor-pointer font-semibold">Read the agent’s whole message</summary><p className="pt-2 whitespace-pre-wrap [overflow-wrap:anywhere]">{said}</p></details> : undefined}>
        <Body tag={working ? <Tag tone="agent">Working</Tag> : human ? <Tag tone={c.decide_on ? "muted" : "act"}>Needs you</Tag> : kindTag(c.kind)} at={c.at} title={title} quote={firstLine(c.excerpt, 160)}
          meta={capital(inChat(c.conv, o)) + (working ? " · Already running" : retry || c.decide_on ? "" : c.kind === "task" ? " · Runs only if you allow it" : " · Answered only if you allow it")}>
          {said && <p className="pt-1 line-clamp-2 text-[14px] text-text-2 [overflow-wrap:anywhere]"><b className="font-bold text-agent-ink">Your agent says:</b> {said}</p>}
        </Body>
      </OpenCard>
      <ConfirmSheet open={sheet === "stop"} onOpenChange={(v) => setSheet(v ? "stop" : "")}
        title="Stop this request?" body="It stops working on this now. Whatever it already did stays done." confirm="Stop" tone="danger"
        onConfirm={() => run("cancel", (api) => api.act({ do: "cancel", id: c.id }), "Stopping.")} />
      <DeclineSheet open={sheet === "decline"} onOpenChange={(v) => setSheet(v ? "decline" : "")} who={who === "You" ? "Your other device" : who} kind={kind}
        onDecline={(reason) => run("decline", (api) => api.act({ do: "decline", id: c.id, reason }), "Declined. " + (who === "You" ? "Your other device" : who) + " is told.")} />
      <ConfirmSheet open={sheet === "close"} onOpenChange={(v) => setSheet(v ? "close" : "")}
        title="Mark as handled?" body={"Nothing is sent" + (who === "You" ? "." : " to " + who + ". You can still write to them in the chat.")}
        confirm="Mark as handled" onConfirm={() => run("resolve", (api) => api.act({ do: "resolve", id: c.id }), "Marked as handled. Nothing was sent.")} />
    </>
  );
}

/** What an invitation would let your agent see and who could give it tasks,
 *  read once from the chat when the row shows: the decision names them. */
function useInvitation(c: T.ConvItem, skip: boolean) {
  const store = useApp();
  const [view, setView] = useState<{ a?: T.AgentView; failed?: boolean }>({});
  useEffect(() => {
    if (skip || !c.pid) return;
    let live = true;
    store.api.dm(c.conv).then((t) => {
      if (!live) return;
      const a = (t.agents || []).find((x) => x.pid === c.pid);
      setView(a ? { a } : { failed: true });
    }).catch(() => { if (live) setView({ failed: true }); });
    return () => { live = false; };
  }, [store, c.conv, c.pid, skip]);
  return view;
}

/** An invitation for your agent into a chat: it joins only if you let it. */
function InviteRow({ c, o }: Props) {
  const { isOpen, go } = useOpen();
  const { busy, run } = useAct();
  const acts = c.decide_on ? [] : c.actions || [];
  const { a, failed } = useInvitation(c, !!c.decide_on);
  const title = convTitle(c, o);
  const seen = (a?.shared || []).length;
  const tasks = (a?.tasks_from || []).filter((p) => p.person !== o.person?.person).map((p) => p.label || p.address);
  const meta = c.decide_on ? "Nothing runs unless you let it join." : failed ? "Couldn’t check what it would see here."
    : !a ? "Checking what it would see…" : (seen ? "It would see " + (seen === 1 ? "1 earlier message" : seen + " earlier messages") : "It would see nothing earlier") + " and what’s asked of it.";
  const decide = (accept: boolean) => run(accept ? "accept" : "decline", (api) => api.decideAgent(c.pid!, accept), accept ? "Your agent joined." : "Declined. Your agent stays out.");
  const actions = c.decide_on ? <DecideOn address={c.decide_on} o={o} /> : acts.length ? <>
    {/* Letting it join waits for what it would see: the decision must name it.
        When that can't be read here, the chat's own card decides instead. */}
    {acts.includes("accept") && (failed
      ? <Button variant="act" size="sm" onClick={() => go("dm", c.conv)}>Decide in chat</Button>
      : <Button variant="act" size="sm" disabled={!!busy || !a} onClick={() => decide(true)}>{busy === "accept" ? "Letting it join…" : "Let it join"}</Button>)}
    {acts.includes("decline") && <Button variant="outline" size="sm" disabled={!!busy} onClick={() => decide(false)}>{busy === "decline" ? "Declining…" : "Decline"}</Button>}
  </> : undefined;
  return (
    <OpenCard onOpen={() => go("dm", c.conv)} current={isOpen(c.conv)} label={title + ". Open the chat."}
      face={<AgentAvatar seed={o.me.address} size={40} mood="waiting" />} actions={actions}>
      <Body tag={<Tag tone={c.decide_on ? "muted" : "act"}>Needs your OK</Tag>} at={c.at} title={title} quote={c.excerpt ? firstLine(c.excerpt, 160) : undefined} meta={meta}>
        {tasks.length > 0 && <p className="pt-1 text-[13px] font-semibold text-approval-ink">{tasks.join(" and ")} could give it tasks without asking you.</p>}
      </Body>
    </OpenCard>
  );
}

/** A question or task for you, held in its chat: answered there, never run. */
function HeldTurnRow({ c, o }: Props) {
  const { isOpen, go } = useOpen();
  const { busy, run } = useAct();
  const [close, setClose] = useState(false);
  const title = convTitle(c, o);
  return <>
    <OpenCard onOpen={() => go("dm", c.conv, c.id)} current={isOpen(c.conv, c.id)} label={title + ". Answer in the chat."} face={<SenderFace c={c} o={o} />}
      actions={!c.decide_on && c.actions?.includes("resolve") && <Button variant="ghost" size="sm" disabled={!!busy} onClick={() => setClose(true)}>Mark as handled</Button>}>
      <Body tag={kindTag(c.kind, "muted")} at={c.at} title={title} quote={firstLine(c.excerpt, 160)} meta={capital(inChat(c.conv, o)) + " · For you, not your agent"} />
    </OpenCard>
    <ConfirmSheet open={close} onOpenChange={setClose} title="Mark as handled?"
      body="This clears this item on this device. The message stays in the chat. No reply is sent."
      confirm="Mark as handled" onConfirm={() => run("resolve", api => api.act({ do: "resolve", id: c.id }), "Marked as handled on this device. No reply was sent.")} />
  </>;
}

/** A notice that your own agent joined a chat without your accept here,
 *  because you invited it from a device of yours: quiet, never counted as
 *  a decision. Hiding it removes only the notice; the agent stays. */
export function SelfConsentRow({ r, o, names }: { r: T.ReviewItem; o: T.Overview; names: Record<string, string> }) {
  const { go } = useOpen();
  const { busy, run } = useAct();
  const conv = r.conv || "";
  const agent = o.person ? agentName(r.agent_id || undefined, names, o.person, o.person) : (r.agent_id && names[r.agent_id]) || "Your agent";
  return (
    <article className="flex gap-3 rounded-2xl border-[1.5px] border-ink/15 bg-sunken p-3.5">
      <AgentAvatar seed={r.agent_id || o.me.address} size={40} mood="neutral" />
      <Body at={r.at} title={agent + " joined " + chatName(conv, o)}
        meta={"You invited it from " + deviceWords(r.peer, o) + ", so it didn’t need your OK here. Hiding this note doesn’t remove it from the chat."}>
        <div className="mt-2.5 flex flex-wrap gap-2">
          {conv && <Button variant="outline" size="sm" onClick={() => go("dm", conv)}>Open chat</Button>}
          <Button variant="ghost" size="sm" disabled={!!busy} onClick={() => run("resolve", (api) => api.act({ do: "resolve", id: r.id }), "Notice hidden. " + agent + " stays in the chat.")}>{busy ? "Hiding…" : "Hide notice"}</Button>
        </div>
      </Body>
    </article>
  );
}
