// MessageView: one message of the open conversation. People's bubbles are
// round (yours butter, on the right), agents speak in caption boxes, and a
// participation record is a centred divider. Under a bubble: reactions,
// what a request asked of an agent and how far it got (only what its
// executor or an answer proves), and the approval card when the server
// lists actions for it. Delivery says only what receipts prove.
import { memo, useMemo, useState, type ReactNode } from "react";
import {
  IconArrowBackUp, IconBolt, IconMessageQuestion, IconCheck, IconChecks, IconClock, IconAlertTriangle, IconEye, IconCircleCheckFilled, IconCircle,
} from "@tabler/icons-react";
import type { T } from "../api";
import { useApp, useWide } from "../context";
import { deliveryWord, firstLine, owner, personName, plain, reminderOf, timeOf } from "../model";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Tag } from "../ui/Tag";
import { ApprovalCard } from "./Approvals";
import { deviceWords } from "./Approvals.words";
import { Markdown, type RenderMention } from "./Markdown";
import { MessageFiles } from "./Message.files";
import { EmojiDialog, Reactions } from "./Message.reactions";
import { ActionSheet, DeleteMessage, DetailsSheet, EditBox, Toolbar, inside, useTouchGestures, type Acts, type Can } from "./Message.actions";
import {
  agentLabel, agentOf, eventKind, ev, excerpt, guestOf, isReply, isRequest, isThreadMsg, joinedBefore, lower, problem, requestLabel, requestState, shownText, whoWrote,
  type AnyMsg, type Ctx, type Who,
} from "./Message.model";
import { RemindSheet, ReminderLine } from "./Reminders";
import { WhatTheySaw } from "./RoomPanel.cards";
import { room } from "./RoomPanel.model";

export interface MessageProps {
  m: AnyMsg;
  ctx: Ctx;
  all: AnyMsg[];
  first?: boolean;           // first of a run by one author: name line
  last?: boolean;            // last of the run: avatar and tail corner
  status?: boolean;          // say this message's delivery in words under it
  readOnly?: boolean;        // shown for reading only ("What Zen saw")
  onJump?: (id: string) => void;
  selecting?: boolean;
  selected?: boolean;
  onSelect?: (id: string) => void;   // toggles in selection mode; starts it otherwise
}

export const MessageView = memo(function MessageView(p: MessageProps) {
  if (ev(p.m)) return <EventDivider m={p.m as T.DMMessage} ctx={p.ctx} all={p.all} />;
  return <Bubble {...p} />;
});

function Bubble({ m, ctx, all, first = true, last = true, status, readOnly, onJump, selecting, selected, onSelect }: MessageProps) {
  const store = useApp();
  const wide = useWide();
  const who = whoWrote(m, ctx);
  // Sheets and menus mount on first use: a long conversation keeps none of them around unopened.
  const [sheet, setSheet] = useState<boolean | null>(null);
  const [details, setDetails] = useState<boolean | null>(null);
  const [confirm, setConfirm] = useState<boolean | null>(null);
  const [emoji, setEmoji] = useState<boolean | null>(null);
  const [hot, setHot] = useState(false);
  const [editing, setEditing] = useState(false);
  const [remind, setRemind] = useState<boolean | null>(null);

  const live = !m._local && !readOnly && !m.deleted && !excerpt(m);
  // A reminder on a stored message, where this device keeps reminders (a computer).
  const reminder = live ? reminderOf(ctx.overview, m.id) : undefined;
  const remindable = { id: m.id, text: firstLine(shownText(m), 90) };
  const has = (what: string) => live && (m.can || []).includes(what);
  const can: Can = {
    react: has("react"),
    reply: live && ctx.canReply,
    edit: has("edit"),
    del: has("delete"),
    select: live && !!ctx.dm && ctx.canReply && !!onSelect && (ctx.dm.role || "member") === "member",
    remind: live && !!ctx.overview?.remind,
  };
  const acts: Acts = {
    topic:live&&ctx.dm&&ctx.canReply&&!isThreadMsg(m)&&!m.topic&&!m.topic_event?()=>{void store.run(a=>a.changeTopic("create",{conv:ctx.dm!.id,peer:"",id:m.lid||m.id})).then(r=>{if(r)store.setDraft(ctx.conv,{...store.draft(ctx.conv),topic:m.lid||m.id,newTopic:false});});}:undefined,
    reply: () => store.setDraft(ctx.conv, { ...store.draft(ctx.conv), replyTo: m.id }),
    copy: () => {
      navigator.clipboard?.writeText(plain(shownText(m))).then(() => store.toast("Copied", "ok"), () => store.toast("Couldn’t copy here", "error"));
    },
    edit: () => setEditing(true),
    del: () => setConfirm(true),
    details: () => setDetails(true),
    select: onSelect && (() => onSelect(m.id)),
    remind: () => setRemind(true),
    reminded: !!reminder,
  };
  const touch = useTouchGestures(() => (readOnly ? undefined : setSheet(true)), can.reply ? acts.reply : null);
  const mention = mentionFor(ctx);

  const text = useMemo(() => askChip(shownText(m), m, ctx), [m, ctx]);
  const time = <Meta m={m} who={who} />;
  const parent = referenceParent(all,m.quote);
  const quote=!!m.quote;
  const request=referenceParent(all,m.reply_to);
  const output=isReply(m)||(isThreadMsg(m)&&m.status==="progress")||(!isThreadMsg(m)&&m.verified_agent&&m.kind==="message");
  const link=output&&request&&!adjacent(all,request,m);

  const shape = who.agent ? "rounded-xl bg-agent text-ink"
    : who.mine ? "rounded-[20px] bg-mine text-mine-ink" + (last ? " rounded-br-md" : "")
      : "rounded-[20px] bg-theirs text-ink" + (last ? " rounded-bl-md" : "");

  const needsHumanDetail = isThreadMsg(m) ? "" : m.job_detail || (ctx.overview?.needs_you || []).find(c => c.conv === ctx.conv && c.id === m.id && c.reason === "agent_needs_human")?.why || "";

  const body = (
    <div tabIndex={wide && live && !selecting ? 0 : undefined} role={wide && live ? "group" : undefined} aria-label={wide && live ? who.name + ", " + timeOf(m.at) : undefined}
      className={"relative min-w-0 max-w-full stroke px-3.5 py-2 focus-visible:outline-offset-2 " + shape + (selected ? " ring-[3px] ring-agent-ink ring-offset-2 ring-offset-canvas" : "")}>
      {wide && hot && live && !selecting && !editing && <Toolbar m={m} ctx={ctx} can={can} acts={acts} mine={who.mine} />}
      {last && !who.mine && (
        <span className="absolute -left-10 bottom-0">
          {who.agent ? <AgentAvatar seed={who.seed} size={32} guest={who.guest} device={who.device} /> : <PersonAvatar name={who.name} seed={who.seed} size={32} guest={who.guest} />}
        </span>
      )}
      {link && <button type="button" className="mb-1 block max-w-full truncate text-left text-[13px] text-muted" onClick={()=>onJump?.(request!.id)}>↳ {m.kind==="message"?"update on":"answer to"} {plain(shownText(request!)).split("\n")[0]}</button>}
      {m._local && <p className="text-xs text-muted" role="status">{m.state_text}{m._failed && <button type="button" className="ml-2 underline" onClick={m._retry}>Retry</button>}</p>}
      {!m.deleted && !isThreadMsg(m) && isRequest(m) && m.pid && <p className="mb-1 text-[13px]" data-agent-recipient title={m.pid}>To {mention("agent", m.pid, agentOf(ctx, m.pid) ? agentLabel(agentOf(ctx, m.pid)!, ctx) : "agent")}</p>}
      {!m.deleted && !isThreadMsg(m) && m.proposal && <p className="mb-1 text-[13px] text-muted" data-proposal-provenance>{who.mine ? "You approved" : "Approved"} {agentOf(ctx, m.pid) ? agentLabel(agentOf(ctx, m.pid)!, ctx) + "’s" : "the agent’s"} suggested task</p>}
      {quote && <ReplyQuote parent={parent} ctx={ctx} onJump={onJump} />}
      {editing ? <EditBox m={m} ctx={ctx} onDone={() => setEditing(false)} />
        : m.deleted ? <p className="flow-root italic text-muted">Message deleted{time}</p>
          : text ? <Markdown text={text} mention={mention} tail={time} />
            : null}
      {!m.deleted && !editing && <MessageFiles m={m} />}
      {!text && !m.deleted && !editing && <div className="flow-root">{time}</div>}
    </div>
  );

  return (
    <div data-mid={m.id} className={"min-w-0 [overflow-wrap:anywhere] " + (first ? "mt-3.5" : "mt-1")}>
      <div className={"group/msg relative flex px-3 sm:px-4 [touch-action:pan-y] " + (who.mine ? "justify-end" : "justify-start") + (selecting ? " cursor-pointer" : "")}
        onClick={selecting && can.select ? (e) => { if (inside(e)) onSelect?.(m.id); } : undefined}
        onPointerEnter={wide ? () => setHot(true) : undefined} onFocus={wide ? () => setHot(true) : undefined}
        onPointerLeave={wide ? (e) => { if (!e.currentTarget.querySelector("[data-popup-open]")) setHot(false); } : undefined}
        {...(readOnly || selecting ? {} : touch.handlers)}>
        {selecting && (
          <span className={"mr-2 grid size-11 shrink-0 place-items-center self-center " + (can.select ? "text-agent-ink" : "text-hairline")} aria-hidden="true">
            {selected ? <IconCircleCheckFilled size={26} /> : <IconCircle size={26} stroke={1.6} />}
          </span>
        )}
        {touch.dx > 0 && (
          <span className="absolute left-4 top-1/2 grid size-9 -translate-y-1/2 place-items-center rounded-full bg-surface stroke" aria-hidden="true"
            style={{ opacity: Math.min(1, touch.dx / 64) }}><IconArrowBackUp size={18} /></span>
        )}
        <div className={"flex min-w-0 flex-col " + (who.mine ? "items-end " : "items-start ") + (who.mine ? "max-w-[85%] lg:max-w-[70%]" : "ml-10 max-w-[calc(100%-3rem)] sm:max-w-[80%] lg:max-w-[70%]")}
          style={touch.dx ? { transform: "translateX(" + touch.dx + "px)" } : undefined}>
          {first && !who.mine && <NameLine m={m} who={who} />}
          {first && who.mine && excerpt(m) && <span className="mb-1 text-[13px] text-muted">From before you joined</span>}
          {first && who.mine && !excerpt(m) && who.sub && <span className="mb-1 pr-1 text-[13px] text-muted">You · {who.sub}</span>}
          {body}
          <Reactions m={m} ctx={ctx} can={can.react} wide={wide} />
          {!m._local && <Under m={m} ctx={ctx} all={all} who={who} status={!!status} onDetails={acts.details} />}
          {reminder && !selecting && <ReminderLine r={reminder} m={remindable} />}
        </div>
        {!wide && live && !selecting && (
          <button type="button" onClick={() => setSheet(true)} className="sr-only focus:not-sr-only focus:absolute focus:right-2 focus:top-0 focus:inline-flex focus:min-h-11 focus:items-center focus:rounded-full focus:bg-surface focus:px-3 focus:stroke">
            Message actions
          </button>
        )}
        {sheet !== null && <ActionSheet open={sheet} onOpenChange={setSheet} m={m} ctx={ctx} can={can} acts={acts} who={who.name} onMoreEmoji={() => setEmoji(true)} />}
        {emoji !== null && <EmojiDialog open={emoji} onOpenChange={setEmoji} m={m} ctx={ctx} />}
        {details !== null && <DetailsSheet open={details} onOpenChange={setDetails} m={m} ctx={ctx} who={who.name} />}
        {confirm !== null && <DeleteMessage open={confirm} onOpenChange={setConfirm} m={m} ctx={ctx} />}
        {remind !== null && <RemindSheet open={remind} onOpenChange={setRemind} m={remindable} r={reminder} />}
      </div>
      {!readOnly && !isThreadMsg(m) && needsHumanDetail && m.exec?.state === "needs_human" && !(m.actions || []).length && <section data-agent-needs-you tabIndex={-1} aria-label="Your agent says" className="mx-3 mt-2 rounded-xl bg-agent px-3.5 py-2.5 text-agent-ink sm:mx-4"><p className="font-bold">Your agent couldn’t finish — it needs your answer</p><p className="pt-1 whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{needsHumanDetail}</p><p className="pt-2 text-[13px]">Open it on {deviceWords(m.target?.address || "", ctx.overview)}.</p></section>}
      {/* The approval card is a system card across the timeline, never part of the bubble. */}
      {!readOnly && (isRequest(m) || (m.actions || []).length > 0) && (
        <div className="mx-auto mt-2.5 w-full max-w-[600px] px-3 empty:hidden sm:px-4">
          <ApprovalCard message={m} dm={ctx.dm} thread={ctx.thread} />
        </div>
      )}
    </div>
  );
}

function adjacent(all: AnyMsg[], a: AnyMsg, b: AnyMsg) {
  const i = all.indexOf(b);
  for (let j = i - 1; j >= 0; j--) if (!ev(all[j])) return all[j] === a;
  return false;
}

function NameLine({ m, who }: { m: AnyMsg; who: Who }) {
  return (
    <div className="mb-1 flex max-w-full flex-wrap items-center gap-x-1.5 gap-y-0.5 pl-1 text-[13px] leading-tight">
      <span className={"font-bold " + (who.agent ? "text-agent-ink" : "text-ink")}>{who.name}</span>
      {who.guest && <Tag tone="guest">Guest</Tag>}
      {who.sub && <span className="text-muted">{who.sub}</span>}
      {excerpt(m) && <span className="text-muted">· from before you joined</span>}
    </div>
  );
}

/** Meta: the time (and for your messages a delivery tick) at the end of the text. */
function Meta({ m, who }: { m: AnyMsg; who: Who }) {
  const s = ("delivery" in m ? m.delivery : undefined) ?? m.state ?? "";
  const tick = !who.mine || who.agent || excerpt(m) ? null
    : s === "delivered" ? <IconChecks size={15} className="text-ok-ink" />
      : s === "custody" ? <IconCheck size={15} />
        : problem(s) && s !== "waiting" ? <IconAlertTriangle size={15} className="text-danger" />
          : s === "queued" || s === "waiting" ? <IconClock size={14} />
            : null;
  return (
    <span className="float-right ml-2.5 mt-[0.5em] inline-flex items-center gap-1 text-[12px] leading-none text-muted tnum">
      {m.edited && !m.deleted && <span>edited</span>}
      <time dateTime={m.sent_at || m.at}>{new Date(m.sent_at || m.at).toDateString()!==new Date(m.at).toDateString()?new Date(m.sent_at || m.at).toLocaleDateString([], {month:"short",day:"numeric"})+" · ":""}{timeOf(m.sent_at || m.at)}</time>
      {tick && <span aria-hidden="true" className="-mr-0.5">{tick}</span>}
      {tick && <span className="sr-only">{deliveryWord(s)}</span>}
    </span>
  );
}

function ReplyQuote({ parent, ctx, onJump }: { parent?: AnyMsg; ctx: Ctx; onJump?: (id: string) => void }) {
  if (!parent) return <p className="mb-1.5 rounded-lg border-l-[3px] border-outline/30 bg-ink/[.05] px-2 py-1 text-[13px] text-muted">Reply to a message not shown here</p>;
  const who = whoWrote(parent, ctx);
  const line = parent.deleted ? "Message deleted" : plain(shownText(parent)).split("\n")[0] || "Files";
  return (
    <button type="button" onClick={(e) => { e.stopPropagation(); onJump?.(parent.id); }}
      className="mb-1.5 block w-full min-w-0 rounded-lg border-l-[3px] border-agent-ink/60 bg-ink/[.05] px-2 py-1 text-left text-[13px] leading-snug hover:bg-ink/[.08]">
      <span className="block font-bold text-agent-ink">{who.name}</span>
      <span className="line-clamp-2 text-text-2 [overflow-wrap:anywhere]">{line}</span>
    </button>
  );
}

// Under a bubble: what a request asked and how far it got, or (for your
// latest message, or one with a problem) its delivery in words.
function Under({ m, ctx, all, who, status, onDetails }: { m: AnyMsg; ctx: Ctx; all: AnyMsg[]; who: Who; status: boolean; onDetails: () => void }) {
  const label = requestLabel(m, ctx);
  const detail = isThreadMsg(m) ? m.detail : m.job_detail;
  if (label) {
    const st = requestState(m, all);
    const tone = st.tone === "ok" ? "text-ok-ink" : st.tone === "work" ? "text-agent-ink" : st.tone === "wait" ? "text-approval-ink" : st.tone === "bad" ? "text-danger" : "text-muted";
    return (
      <div className={"mt-1 flex max-w-full flex-col px-1 text-[13px] " + (who.mine ? "items-end text-right" : "items-start")}>
        <button type="button" onClick={onDetails} className="relative inline-flex items-center gap-1.5 font-semibold text-text-2 before:absolute before:inset-x-0 before:top-1/2 before:h-11 before:-translate-y-1/2 before:content-['']">
          {m.kind === "task" ? <IconBolt size={14} className="text-agent-ink" /> : <IconMessageQuestion size={14} className="text-agent-ink" />}
          <span>{label}</span>
          {st.text && <><span aria-hidden="true" className="text-muted">·</span><span className={tone}>{st.text}</span></>}
        </button>
        {detail && !(m.actions || []).length && <span className="mt-0.5 text-muted [overflow-wrap:anywhere]">{detail}</span>}
      </div>
    );
  }
  if (!who.mine || who.agent || m.deleted) return null;
  const s = ("delivery" in m ? m.delivery : undefined) ?? m.state ?? "";
  if (!status && !problem(s)) return null;
  const word = deliveryWord(s);
  if (!word) return null;
  const bad = problem(s);
  return (
    <button type="button" onClick={onDetails}
      className={"relative mt-1 px-1 text-[12px] font-semibold before:absolute before:inset-x-0 before:top-1/2 before:h-11 before:-translate-y-1/2 before:content-[''] " + (bad ? "text-danger" : "text-muted")}>
      {word}{bad && m.state_text ? " · " + m.state_text : ""}
    </button>
  );
}

// ---- participation dividers ----------------------------------------------------------

// The words for joining and leaving are the room panel's: "Ledger joined to
// help · saw 10 messages · invited by you", "Ledger left · dismissed by you".
function EventDivider({ m, ctx, all }: { m: T.DMMessage; ctx: Ctx; all: AnyMsg[] }) {
  const [saw, setSaw] = useState<boolean | null>(null);
  const text = m.event || "";
  const k = eventKind(text);
  const a = agentOf(ctx, m.pid);
  const g = guestOf(ctx, m.pid);
  const me = ctx.overview?.person;
  const by = (w: string) => (w === "You" ? "you" : w);
  const visit = useVisit(ctx, k && (a || g) ? m.pid : undefined);
  let name = "", line: ReactNode = text, sub = "", divider = false;
  if (k && (a || g) && visit) {
    name = a ? agentLabel(a, ctx) : g!.host_here ? "You" : personName(g!.host);
    const inviter = a ? a.inviter : g!.inviter;
    const invitedBy = inviter && me && inviter.person === me.person ? "you" : personName(inviter);
    const n = visit.shared.length + visit.missing;
    if (k.kind === "joined") {
      divider = true;
      line = <><b>{name}</b> joined to help</>;
      sub = (n ? "saw " + n + (n === 1 ? " message" : " messages") : "saw nothing") + " · invited by " + invitedBy;
    } else if (k.kind === "invited") {
      line = <><b>{k.by ? cap(by(k.by)) : "Someone"}</b> invited {lower(name)}</>;
      sub = n ? n + " earlier " + (n === 1 ? "message" : "messages") + " to share" : "";
    } else if (k.kind === "declined") {
      line = <><b>{name}</b> won’t join</>;
      sub = k.by ? "declined by " + by(k.by) : "";
    } else if (joinedBefore(all, m.pid || "", all.indexOf(m)) === false) {
      // Ended before it joined: an invitation taken back, not a departure.
      line = <><b>{name}</b> won’t join</>;
      sub = "invite cancelled" + (k.by ? " by " + by(k.by) : "");
    } else {
      divider = true;
      line = <><b>{name}</b> left{k.by && " · dismissed by " + by(k.by)}</>;
    }
  }
  if (!divider) {
    // Invitations, refusals and records this page can't read are quiet lines; joining and leaving are the dividers.
    return (
      <p data-mid={m.id} role="note" className="mx-auto my-2 max-w-[min(92%,520px)] px-4 text-center text-[13px] leading-snug text-muted [overflow-wrap:anywhere]">
        {line}{sub && <span> · {sub}</span>}<span className="whitespace-nowrap"> · {timeOf(m.at)}</span>
      </p>
    );
  }
  const joined = k!.kind === "joined";
  const seen = visit!.shared.length + visit!.missing;
  return (
    <div data-mid={m.id} role="note" className="my-3 flex items-center gap-2 px-3 sm:px-4">
      <span className="h-0 flex-1 border-t-[1.5px] border-dashed border-outline/25" aria-hidden="true" />
      <div className={"flex max-w-[min(92%,460px)] items-center gap-2.5 rounded-2xl border-[1.5px] px-3 py-1.5 text-[13px] leading-snug "
        + (joined ? "border-outline/60 bg-guest-bg text-ink" : "border-hairline bg-surface text-text-2")}>
        {a && <AgentAvatar seed={a.agent_id || a.host.address} size={28} guest={joined} mood={joined ? "neutral" : "asleep"} />}
        {g && !a && <PersonAvatar name={personName(g.host)} seed={g.host.person || g.host.address} size={28} guest={joined} />}
        <span className="min-w-0">
          <span className="block text-[14px] [overflow-wrap:anywhere]">{line}<span className="text-muted"> · {timeOf(m.at)}</span></span>
          {sub && <span className="block text-muted">{sub}</span>}
          {joined && seen > 0 && (
            <button type="button" onClick={() => setSaw(true)}
              className="relative inline-flex items-center gap-1 font-semibold text-agent-ink underline decoration-agent-ink/40 underline-offset-2 before:absolute before:-inset-x-1 before:top-1/2 before:h-11 before:-translate-y-1/2 before:content-[''] hover:decoration-agent-ink">
              <IconEye size={15} />{name === "You" ? "See what you saw" : "See what " + lower(name) + " saw"}
            </button>
          )}
        </span>
      </div>
      <span className="h-0 flex-1 border-t-[1.5px] border-dashed border-outline/25" aria-hidden="true" />
      {saw !== null && ctx.dm && <WhatTheySaw t={ctx.dm} g={visit!} open={saw} onOpenChange={setSaw} />}
    </div>
  );
}

/** useVisit: one participation as the room panel reads it (what it was shown,
 *  whose it is), so the divider and "In this chat" count the same messages. */
function useVisit(ctx: Ctx, pid?: string) {
  const t = ctx.dm;
  return useMemo(() => {
    if (!t || !pid) return null;
    const one = { ...t, agents: (t.agents || []).filter((x) => x.pid === pid), guests: (t.guests || []).filter((x) => x.pid === pid) };
    const r = room(one, ctx.overview, ctx.names);
    return [...r.guests, ...r.invited, ...r.past][0] || null;
  }, [t, pid, ctx.overview, ctx.names]);
}

const cap = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);

// ---- the addressed agent ---------------------------------------------------------------

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** askChip: a question or task names its agent as "@Name" in plain text (the
 *  ask itself goes by participation id). Shown, that name becomes the agent's
 *  chip, by the id the request was sent to; the stored text stays as sent. */
function askChip(text: string, m: AnyMsg, ctx: Ctx): string {
  if (!text || isThreadMsg(m) || !isRequest(m) || !m.pid) return text;
  const a = agentOf(ctx, m.pid);
  if (!a) return text;
  // The names the sender may have used: the agent's own, or whose agent it is (older pages said "assistant").
  const whose = [owner(a.host, ctx.overview?.person), "Your", personName(a.host) + "’s"];
  const names = [agentLabel(a, ctx), ...whose.flatMap((w) => [w + " agent", w + " assistant"])]
    .filter((n, i, l) => n && l.indexOf(n) === i && /^[^\[\]\r\n]{1,80}$/.test(n))
    .sort((x, y) => y.length - x.length);
  const re = new RegExp("(^|\\s)@(" + names.map(escape).join("|") + ")(?=$|[\\s.,:;!?])");
  const hit = re.exec(text);
  if (!hit || (text.slice(0, hit.index).split("`").length - 1) % 2) return text; // not inside code
  const at = hit.index + hit[1].length;
  return text.slice(0, at) + "[@" + hit[2] + "](agentnet:agent/" + m.pid + ")" + text.slice(at + 1 + hit[2].length);
}

// ---- mentions ---------------------------------------------------------------------------

function mentionFor(ctx: Ctx): RenderMention {
  return (kind, id, name) => {
    const me = ctx.overview?.person;
    let label = "", agent = false, guest = false, self = false;
    if (kind === "guest") {
      const g = (ctx.dm?.guests || []).find((x) => x.pid === id);
      if (g) { label = personName(g.host); guest = true; self = !!g.host_here; }
    } else if (kind === "agent") {
      const a = (ctx.dm?.agents || []).find((x) => x.pid === id || x.agent_id === id);
      if (a) { label = agentLabel(a, ctx); agent = true; }
    } else {
      const p = [me, ctx.dm?.peer, ...(ctx.dm?.members || [])].find((x) => x && x.person === id);
      if (p) { label = personName(p); self = !!me && p.person === me.person; }
    }
    if (!label) return "@" + name;
    const look = agent ? "bg-agent-fill/35 text-agent-ink" : guest ? "bg-guest-bg text-guest-ink" : self ? "bg-mine text-mine-ink ring-1 ring-outline/30" : "bg-ink/[.08] text-ink";
    return <span className={"rounded-md px-1 py-px font-semibold " + look} title={label + (agent ? " · agent" : guest ? " · guest" : "")}>@{label}</span>;
  };
}


function referenceParent(all: AnyMsg[], id?: string) {
  if (!id) return undefined;
  const exact = all.find(m => m.id === id);
  if (exact) return exact;
  const logical = all.filter(m => !isThreadMsg(m) && m.lid === id);
  return logical.length === 1 ? logical[0] : undefined;
}
