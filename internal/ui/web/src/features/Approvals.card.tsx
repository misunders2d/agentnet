// The approval card: a system card (never a bubble) in the owner's own app
// for a request to their agent, driven only by the message's actions[].
// Its buttons call /api/act; nothing typed, mentioned or reacted approves.
// A request you sent that waits on someone else's OK gets a compact line.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { IconBolt, IconCheck, IconClock, IconFileText, IconLock, IconPlayerStop, IconRefresh, IconShieldCheck, IconUser, IconEye, IconSparkles, IconArrowBackUp } from "@tabler/icons-react";
import type { T } from "../api";
import { useAgentNames, useApp } from "../context";
import { deliveryWord, deviceKind, personName, timeOf } from "../model";
import { useOwned } from "../owned";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar, type Mood } from "../ui/Avatar";
import { Button } from "../ui/Button";
import {
  askerOf, capital, deviceWords, headlineOf, inSentence, isMine, isThreadMsg, kindWord, longer, myAgent, myAgentSeed, nameOf,
  participationOf, phaseOf, placeOf, requestText, waitsElsewhere, type Asker, type Phase, type Req,
} from "./Approvals.words";
import { ConfirmSheet, DeclineSheet } from "./Approvals.sheets";
import { focusComposer } from "./Composer.focus";
import { decidersWords } from "./Approvals.reports";
import { Command, Details } from "./Settings.parts";

export function ApprovalCard({ message, dm, thread }: { message: Req; dm?: T.DMThread | null; thread?: T.Thread | null }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const names = useAgentNames();
  const phase = phaseOf(message);
  if (phase) return <OwnerCard m={message} dm={dm} thread={thread} o={o} names={names} phase={phase} />;
  if (waitsElsewhere(message)) return <WaitingLine m={message} o={o} />;
  return null;
}

// ---- the requester's view ---------------------------------------------------

function WaitingLine({ m, o }: { m: Req; o: T.Overview | null }) {
  const e = m.exec!;
  const who = isMine(e.host, o) ? "your OK on " + deviceWords(e.host, o) : nameOf(e.host, o) + "’s OK";
  const delivered = m.dir === "out" ? deliveryWord(("delivery" in m ? m.delivery : undefined) ?? m.state ?? "") : "";
  const since = e.at ? "waiting since " + timeOf(new Date(e.at * 1000).toISOString()) : "";
  return (
    <div role="status" className="mx-auto flex w-full max-w-[560px] items-center gap-3 rounded-2xl border-[1.5px] border-dashed border-approval-ink/60 bg-surface px-3.5 py-2.5">
      <span className="grid size-9 shrink-0 place-items-center rounded-full bg-act/25 text-approval-ink" aria-hidden="true"><IconClock size={20} stroke={2} /></span>
      <div className="min-w-0">
        <p className="font-semibold leading-snug">Waiting for {who}</p>
        <p className="tnum text-[13px] text-muted">{[delivered, since].filter(Boolean).join(" · ")}</p>
      </div>
    </div>
  );
}

// ---- the owner's card -------------------------------------------------------

interface Props { m: Req; dm?: T.DMThread | null; thread?: T.Thread | null; o: T.Overview | null; names: Record<string, string>; phase: Phase }

type Sheet = null | "always" | "approve" | "decline" | "stop" | "close";

function OwnerCard({ m, dm, thread, o, names, phase }: Props) {
  const store = useApp();
  const { root } = useOwned();
  const [busy, setBusy] = useState("");
  const [sheet, setSheet] = useState<Sheet>(null);
  const acts = m.actions || [];
  const can = (a: string) => acts.includes(a);
  const asker = askerOf(m, o, names, dm);
  const mine = myAgent(m, names, o, dm);
  const agent = inSentence(mine.name);
  const named = mine.named;
  const notice = isThreadMsg(m) && m.kind === "message" && m.status === "review_notice";
  const conv = dm?.id || thread?.id || "";
	const said = (isThreadMsg(m) ? m.detail : m.job_detail) || "";
	const proposal = isThreadMsg(m) ? m.proposal : null;

  const permissionPerson = thread?.permission_person;
  const permissionName = permissionPerson ? personName(permissionPerson) : asker.name;
  const permissionTarget = permissionPerson?.person || m.from;
  const act = async (a: T.Action, ok: string) => {
    setBusy(a.do);
    await store.run((api) => api.act(a), ok);
    setBusy("");
  };
  const allow = () => act({ do: "accept", id: m.id }, phase === "decide" ? "Allowed once." : "Running it again.");
  const reply = () => {
    store.setDraft(conv, { ...store.draft(conv), replyTo: m.id });
    focusComposer(root, conv); // this conversation's field, within the tap; it shows the reply it now carries
  };
  const replyHere = !!conv && (isThreadMsg(m) ? can("reply") : !dm?.frozen);

  if (phase === "closing") {
    return (
      <Shell tone="quiet" label={notice ? "Report" : "Needs a person"} at={m.at} labelledBy={"appr-" + m.id}>
        <div className="px-4 pt-3.5 pb-4">
          <h3 id={"appr-" + m.id} className="font-display text-[19px] font-bold leading-snug">
            {notice ? capital(deviceWords(m.from, o)) + " has requests waiting for a person there" : capital(agent) + "’s follow-up needs a person"}
          </h3>
          <p className="pt-1.5 text-text-2">{notice ? decidersWords(o?.review?.find((r) => r.id === m.id)?.report, m.from, o) : said || requestText(m)}</p>
          <Button className="mt-3.5" disabled={!!busy} onClick={() => setSheet("close")}>{notice ? "Dismiss report" : "Close it"}</Button>
        </div>
        <ConfirmSheet open={sheet === "close"} onOpenChange={(v) => setSheet(v ? "close" : null)}
          title={notice ? "Dismiss this report?" : "Close without replying?"}
          body={notice ? "It's cleared on this computer only. A newer report from " + deviceWords(m.from, o) + " shows again if requests still wait." : "Nothing is sent to " + asker.name + "."}
          confirm={notice ? "Dismiss" : "Close it"} onConfirm={() => act({ do: "resolve", id: m.id }, notice ? "Report dismissed." : "Closed. Nothing was sent.")} />
      </Shell>
    );
  }

  const kind = kindWord(m.kind);
  const whose = asker.you ? "your" : asker.person + "’s";
  const lead = phase === "running" ? capital(agent) + " is working on " + whose + " " + kind
    : asker.you ? (m.kind === "task" ? "You gave " + agent + " a task" : "You asked " + agent)
    : (m.kind === "task" ? asker.name + " gives " + agent + " a task" : asker.name + " asks " + agent);
  const part = participationOf(m, dm);
  const files = isThreadMsg(m) ? (m.files || []).length : (m.attachments || []).length;
  const stoppedWord = m.state === "interrupted" ? "Interrupted" : m.state === "cancelled" ? "Stopped" : "Didn’t finish";

  return (
    <section className="mx-auto flex w-full max-w-[560px] flex-col items-center gap-2" aria-labelledby={"appr-" + m.id}>
      {(phase === "decide" || phase === "needs_human") && (
        <p className="inline-flex max-w-full items-center gap-1.5 rounded-full border-[1.5px] border-ink/20 bg-surface px-3 py-1.5 text-[13px] font-semibold text-text-2">
          <IconBolt size={15} className="shrink-0 fill-act text-approval-ink" aria-hidden="true" />
          <span className="truncate">{named ? agent + " is your agent, so this waits for you" : "This is for your agent, so it waits for you"}</span>
        </p>
      )}
      <Shell tone={phase === "running" ? "work" : phase === "stopped" ? "quiet" : "act"} at={m.at}
        label={phase === "decide" ? "Needs your OK" : phase === "needs_human" ? "Needs you" : phase === "running" ? "Working" : stoppedWord}>
        <Duo asker={asker} agent={mine.name} sub={named ? mine.where : "On " + mine.where.replace(/^Your agent · /, "")} seed={myAgentSeed(m, o, dm)} device={deviceKind(mine.address)}
          mood={phase === "running" ? "working" : phase === "decide" || phase === "needs_human" ? "waiting" : "neutral"} live={phase === "decide"} />

        <div className="px-4 pt-3">
          <p className="text-[15px] font-semibold text-text-2">{lead}</p>
          <h3 id={"appr-" + m.id} className="mt-0.5 font-display text-[22px] font-bold leading-[1.18] tracking-[-0.01em] [overflow-wrap:anywhere]">
            {headlineOf(m) || (files ? "Files only" : "No text")}
          </h3>
        </div>

        {phase === "running" && said && <p role="status" className="mx-4 mt-3 whitespace-pre-wrap text-[14px] text-text-2 [overflow-wrap:anywhere]">{said}</p>}
        {proposal && <Details label="How this task was chosen" className="mx-4 mt-3">
          <p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{nameOf(proposal.asker, o)} asked: {proposal.question}</p>
          <p className="mt-2 whitespace-pre-wrap [overflow-wrap:anywhere]">Your agent suggested: {proposal.proposal}</p>
          <p className="mt-2">{nameOf(proposal.confirmed_by, o)} chose Do it. This uses only their usual task approval.</p>
        </Details>}

        {phase === "needs_human" || (phase === "stopped" && said) ? (
          <div className="mx-4 mt-3 rounded-xl bg-agent px-3.5 py-2.5 text-agent-ink">
            <p className="text-[13px] font-bold uppercase tracking-wide">{capital(agent)} says</p>
            <p className="pt-0.5 whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{said || "It needs a person to decide. It didn't say more."}</p>
          </div>
        ) : null}

        {(phase === "decide" || phase === "stopped") && (
          <dl className="mx-4 mt-3 border-t-2 border-dashed border-ink/15">
            {(longer(m) || files > 0) && <Fact icon={<IconFileText size={18} />} k="What"><FullText text={requestText(m)} long={longer(m)} files={files} /></Fact>}
            {dm?.kind === "group" && <Fact icon={<IconUser size={18} />} k="From">{asker.name}, {placeOf(dm, null, o)}</Fact>}
            {part && <Fact icon={<IconEye size={18} />} k="Sees">{seesWords(part)}</Fact>}
            <Fact icon={<IconSparkles size={18} />} k="Effect">{effectWords(m.kind, phase, agent)}</Fact>
          </dl>
        )}

        {phase === "decide" && (
          <div className="px-4 pt-3">
            <p className="flex items-start gap-2 rounded-xl bg-ok-bg px-3 py-2 text-[14px] font-semibold text-ok-ink">
              <IconShieldCheck size={18} className="mt-px shrink-0" aria-hidden="true" />
              {m.kind === "task" ? "Allowing once doesn’t allow later tasks." : "Allowing answers this one question only."}
            </p>
          </div>
        )}

        <div className="flex flex-col gap-2 px-4 pt-4 pb-3">
          {phase === "decide" && <>
            {can("accept") && <Button variant="act" size="lg" icon={<IconCheck size={20} stroke={2.5} />} disabled={!!busy} onClick={allow}>{busy === "accept" ? "Allowing…" : "Allow once"}</Button>}
            {can("decline") && <Button variant="outline" size="lg" className="min-h-12!" disabled={!!busy} onClick={() => setSheet("decline")}>Decline</Button>}
            {can("reply") && replyHere && <TextButton onClick={reply} icon={<IconArrowBackUp size={18} />}>Answer it yourself</TextButton>}
            {can("accept_always") && <TextButton onClick={() => setSheet("always")} disabled={!!busy}>{named ? "Always allow " + asker.name + " → " + agent + "…" : "Always allow tasks from " + asker.name + "…"}</TextButton>}
            {can("approve") && <TextButton onClick={() => setSheet("approve")} disabled={!!busy}>Approve {permissionName}…</TextButton>}
            {!can("decline") && <p className="px-1 pt-1 text-[13px] text-muted">If you don’t allow it, nothing runs.</p>}
          </>}
          {phase === "needs_human" && <>
            {replyHere && <Button variant="act" size="lg" icon={<IconArrowBackUp size={20} />} disabled={!!busy} onClick={reply}>Reply</Button>}
            {can("accept") && <Button variant="outline" size="lg" className="min-h-12!" icon={<IconRefresh size={19} />} disabled={!!busy} onClick={allow}>{busy === "accept" ? "Starting…" : "Run again"}</Button>}
            {can("resolve") && <TextButton onClick={() => setSheet("close")} disabled={!!busy}>Close without replying…</TextButton>}
          </>}
          {phase === "stopped" && <>
            {can("accept") && <Button variant="act" size="lg" icon={<IconRefresh size={20} />} disabled={!!busy} onClick={allow}>{busy === "accept" ? "Starting…" : "Run it again"}</Button>}
            {can("reply") && replyHere && <TextButton onClick={reply} icon={<IconArrowBackUp size={18} />}>Answer it yourself</TextButton>}
          </>}
          {phase === "running" && (
            <Button variant="danger" size="lg" className="min-h-12!" icon={<IconPlayerStop size={19} />} disabled={!!busy} onClick={() => setSheet("stop")}>{busy === "cancel" ? "Stopping…" : "Stop"}</Button>
          )}
        </div>

        {(phase === "decide" || phase === "needs_human") && (
          <p className="flex items-center gap-2 border-t-[1.5px] border-ink/10 px-4 py-2.5 text-[13px] font-medium text-text-2">
            <IconLock size={16} className="shrink-0 text-ink" aria-hidden="true" />Only you can approve this. Typing in chat can’t approve it.
          </p>
        )}
      </Shell>

      <ConfirmSheet open={sheet === "always"} onOpenChange={(v) => setSheet(v ? "always" : null)}
        title={named ? "Always allow " + asker.name + " → " + agent + "?" : "Always allow tasks from " + asker.name + "?"}
        body={permissionName + (permissionPerson ? " can give tasks from all current and future verified devices. Removing a device ends its access; key changes and person conflicts block it." : " can give tasks from this exact device key without asking you.") + " Your agent's normal permissions still apply. Turn it off in Settings → Permissions."}
        confirm="Always allow" onConfirm={() => act({ do: "accept_always", id: m.id }, "Allowed. Later tasks from " + asker.name + " run without asking.")}
        other="Just once" onOther={allow}>
        <TurnOff cmd={"agentnet unapprove --tasks " + permissionTarget} what="Turns it off. Their tasks wait for you again." />
      </ConfirmSheet>
      <ConfirmSheet open={sheet === "approve"} onOpenChange={(v) => setSheet(v ? "approve" : null)}
        title={"Approve " + permissionName + "?"}
        body={capital(agent) + " answers questions from " + permissionName + (permissionPerson ? " on all current and future verified devices." : " on this device.") + " Removing a device or a person conflict ends person permission; key changes block until trusted. Your question settings apply; tools you already allow keep their effects. Tasks still wait for you."}
        confirm={"Approve " + permissionName} onConfirm={() => act({ do: "approve", id: permissionTarget }, permissionName + "’s questions are answered automatically from now on.")}
        other="Just this one" onOther={allow}>
        <TurnOff cmd={"agentnet unapprove " + permissionTarget} what="Turns it off. Their questions wait for you again." />
      </ConfirmSheet>
      <DeclineSheet open={sheet === "decline"} onOpenChange={(v) => setSheet(v ? "decline" : null)} who={asker.person} kind={kind}
        onDecline={(reason) => act({ do: "decline", id: m.id, reason }, "Declined. " + asker.person + " is told.")} />
      <ConfirmSheet open={sheet === "stop"} onOpenChange={(v) => setSheet(v ? "stop" : null)}
        title={"Stop " + agent + "?"} body="It stops working on this now. Whatever it already did stays done."
        confirm="Stop" tone="danger" onConfirm={() => act({ do: "cancel", id: m.id }, "Stopping.")} />
      <ConfirmSheet open={sheet === "close"} onOpenChange={(v) => setSheet(v ? "close" : null)}
        title="Close without replying?" body={"Nothing is sent to " + asker.person + ". You can still write to them in the chat."}
        confirm="Close it" onConfirm={() => act({ do: "resolve", id: m.id }, "Closed. Nothing was sent.")} />
    </section>
  );
}

/** TurnOff: the other way to undo a standing permission, in a terminal on
 *  this computer, folded away (Settings → Permissions lists what it can see). */
function TurnOff({ cmd, what }: { cmd: string; what: string }) {
  const store = useApp();
  return (
    <Details label="Or turn it off in a terminal" className="mt-3">
      <Command cmd={cmd} what={what} onCopied={(ok) => store.toast(ok ? "Copied." : "Copy didn’t work here. Select the words and copy them by hand.", ok ? "ok" : "error")} />
    </Details>
  );
}

function seesWords(a: T.AgentView) {
  const n = (a.shared || []).length;
  if (!n) return "Only what’s asked of it here";
  return (n === 1 ? "1 shared message" : n + " shared messages") + " and what’s asked of it here";
}

function effectWords(kind: string, phase: Phase, agent: string) {
  if (phase === "stopped") return "Running it again starts fresh; it doesn’t pick up where it stopped.";
  if (kind === "task") return "Runs once here, with " + agent + "’s usual permissions. It can change files.";
  return capital(agent) + " answers with your setup, minus editing tools. Tools you already allow keep their effects.";
}

// ---- pieces -------------------------------------------------------------------

const bands = {
  act: "bg-act text-act-ink dots",
  work: "bg-agent text-agent-ink dots",
  quiet: "bg-sunken text-text-2",
};

function Shell({ tone, label, at, children, labelledBy }: { tone: keyof typeof bands; label: string; at: string; children: ReactNode; labelledBy?: string }) {
  return (
    <article aria-labelledby={labelledBy} className={"fade-in w-full overflow-hidden rounded-2xl bg-surface stroke " + (tone === "quiet" ? "" : "shadow-pop")}>
      <header className={"flex items-center justify-between gap-3 border-b-[1.5px] border-outline px-3.5 py-2 " + bands[tone]}>
        <span className={"inline-flex h-6 -rotate-2 items-center gap-1.5 rounded-md stroke px-2 text-[11px] font-extrabold uppercase tracking-[0.07em] " + (tone === "act" ? "bg-surface text-ink" : "bg-surface")}>
          {tone === "work" && <span className="flex gap-0.5" aria-hidden="true"><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /></span>}
          {label}
        </span>
        <time dateTime={at} className="tnum text-[13px] font-bold">{timeOf(at)}</time>
      </header>
      {children}
    </article>
  );
}

function Face({ name, sub, agent, children }: { name: string; sub: string; agent?: boolean; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col items-center text-center">
      {children}
      <p className="pt-1.5 text-[15px] font-bold leading-tight text-balance [overflow-wrap:anywhere]">{name}</p>
      {sub && <p className={"text-[13px] font-semibold leading-snug " + (agent ? "text-agent-ink" : "text-muted")}>{sub}</p>}
    </div>
  );
}

function Duo({ asker, agent, sub, seed, device, mood, live }: { asker: Asker; agent: string; sub: string; seed: string; device?: "laptop" | "server" | "phone"; mood: Mood; live: boolean }) {
  const bolt = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    // The arrow nudges three times, then rests (never with reduced motion).
    const el = bolt.current;
    if (!live || !el || !el.animate || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const a = el.animate([{ transform: "translateX(0)" }, { transform: "translateX(4px)" }, { transform: "translateX(0)" }], { duration: 900, iterations: 3, easing: "ease-in-out", delay: 300 });
    return () => a.cancel();
  }, [live]);
  return (
    <div className="mx-auto grid w-full max-w-[440px] grid-cols-[minmax(0,1fr)_52px_minmax(0,1fr)] items-start gap-1 px-3 pt-4 sm:grid-cols-[minmax(0,1fr)_88px_minmax(0,1fr)] sm:px-4">
      <Face name={asker.title} sub={asker.sub}>
        {asker.agent ? <AgentAvatar seed={asker.seed} size={48} /> : <PersonAvatar name={asker.you ? "Me" : asker.title} seed={asker.seed} size={48} />}
      </Face>
      <span className="relative flex h-12 items-center" aria-hidden="true">
        <span className="absolute inset-x-1 top-1/2 border-t-2 border-dashed border-ink" />
        <span className="absolute right-0 top-1/2 -translate-y-1/2 border-y-[6px] border-l-[9px] border-y-transparent border-l-ink" />
        <span ref={bolt} className="relative mx-auto grid size-8 place-items-center rounded-full bg-act text-act-ink stroke"><IconBolt size={16} stroke={2.2} /></span>
      </span>
      <Face name={agent} sub={sub} agent>
        <AgentAvatar seed={seed} size={48} mood={mood} device={device} />
      </Face>
    </div>
  );
}

function Fact({ icon, k, children }: { icon: ReactNode; k: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[20px_64px_minmax(0,1fr)] items-start gap-x-2 border-b-2 border-dashed border-ink/15 py-2">
      <span className="mt-0.5 text-text-2" aria-hidden="true">{icon}</span>
      <dt className="pt-0.5 text-[12px] font-extrabold uppercase tracking-[0.05em] text-muted">{k}</dt>
      <dd className="text-[15px] font-medium leading-snug [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

function FullText({ text, long, files }: { text: string; long: boolean; files: number }) {
  const [all, setAll] = useState(false);
  const fileWords = files ? (files === 1 ? "1 file attached" : files + " files attached") : "";
  if (!long) return <>{fileWords}</>;
  return (
    <>
      <span className={"whitespace-pre-wrap " + (all ? "" : "line-clamp-4")}>{text}</span>
      {fileWords && <span className="mt-1 block text-[13px] text-muted">{fileWords}</span>}
      <button type="button" onClick={() => setAll(!all)} className="-ml-1 mt-0.5 min-h-11 rounded-lg px-1 text-[14px] font-bold text-agent-ink underline underline-offset-2">
        {all ? "Show less" : "Show all of it"}
      </button>
    </>
  );
}

function TextButton({ children, onClick, disabled, icon }: { children: ReactNode; onClick: () => void; disabled?: boolean; icon?: ReactNode }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled}
      className="inline-flex min-h-11 w-full items-center justify-center gap-1.5 rounded-xl px-3 text-[15px] font-bold text-ink underline decoration-ink/30 decoration-2 underline-offset-4 hover:bg-sunken disabled:opacity-50">
      {icon}{children}
    </button>
  );
}
