// What a person can do to one message: react, reply, copy, select it to
// bring someone in, edit or delete their own (only when the server's can[]
// says so), and read its details. Desktop shows a hover toolbar; phones a
// sheet on long-press. Nothing destructive happens by gesture alone: delete
// always asks first.
import { useEffect, useRef, useState, type MouseEvent, type PointerEvent, type ReactNode } from "react";
import { AlertDialog } from "@base-ui/react/alert-dialog";
import { Menu } from "@base-ui/react/menu";
import {
  IconArrowBackUp, IconCopy, IconDots, IconPencil, IconTrash, IconInfoCircle, IconMoodSmile, IconSquareCheck,
  IconChecks, IconCheck, IconClock, IconAlertTriangle, IconChevronDown,
} from "@tabler/icons-react";
import { useApp } from "../context";
import { plain, timeOf } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button } from "../ui/Button";
import { Markdown } from "./Markdown";
import { ReactPopover, controlRef, useReact } from "./Message.reactions";
import { QuickReactions } from "./Emoji";
import { decode, encode, shift } from "./Composer.mentions";
import { copyText, deviceWords, isRequest, isThreadMsg, shownText, type AnyMsg, type Ctx } from "./Message.model";
import { usePortal } from "../owned";

export interface Can { react: boolean; reply: boolean; edit: boolean; del: boolean; select: boolean }

export interface Acts {
  reply: () => void;
  copy: () => void;
  edit: () => void;
  del: () => void;
  details: () => void;
  select?: () => void;
}

// ---- desktop: the hover toolbar ---------------------------------------------------

export function Toolbar({ m, ctx, can, acts, mine }: { m: AnyMsg; ctx: Ctx; can: Can; acts: Acts; mine: boolean }) {
  const portal = usePortal();
  const btn = "grid size-11 place-items-center rounded-full text-text-2 hover:bg-sunken hover:text-ink";
  return (
    <div className={"absolute top-0 z-10 flex items-center rounded-full bg-surface stroke opacity-0 shadow-pop-sm transition-opacity duration-150 group-hover/msg:opacity-100 group-focus-within/msg:opacity-100 has-[[data-popup-open]]:opacity-100 "
      + (mine ? "right-full mr-2" : "left-full ml-2")}>
      {can.react && (
        <ReactPopover m={m} ctx={ctx} wide
          trigger={<button type="button" aria-label="React" title="React" className={btn}><IconMoodSmile size={20} /></button>} />
      )}
      {can.reply && <button type="button" aria-label="Reply" title="Reply" onClick={acts.reply} className={btn}><IconArrowBackUp size={20} /></button>}
      <Menu.Root>
        <Menu.Trigger aria-label="More actions" title="More actions" className={btn + " data-[popup-open]:bg-sunken"}><IconDots size={20} /></Menu.Trigger>
        <Menu.Portal container={portal}>
          <Menu.Positioner side="bottom" align={mine ? "end" : "start"} sideOffset={6} collisionPadding={12} className="z-50">
            <Menu.Popup className="min-w-52 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
              <Items can={can} acts={acts} m={m} render={(icon, label, onClick, danger) => (
                <Menu.Item key={label} onClick={onClick}
                  className={"flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-3 text-[15px] font-medium outline-none data-[highlighted]:bg-sunken " + (danger ? "text-danger" : "")}>
                  {icon}{label}
                </Menu.Item>
              )} />
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
    </div>
  );
}

function Items({ can, acts, m, render }: { can: Can; acts: Acts; m: AnyMsg; render: (icon: ReactNode, label: string, onClick: () => void, danger?: boolean) => ReactNode }) {
  const text = !m.deleted && !!shownText(m);
  return (
    <>
      {text && render(<IconCopy size={20} />, "Copy text", acts.copy)}
      {can.select && acts.select && render(<IconSquareCheck size={20} />, "Select", acts.select)}
      {can.edit && render(<IconPencil size={20} />, "Edit", acts.edit)}
      {render(<IconInfoCircle size={20} />, "Details", acts.details)}
      {can.del && render(<IconTrash size={20} />, "Delete…", acts.del, true)}
    </>
  );
}

// ---- phones: the long-press sheet ----------------------------------------------------

export function ActionSheet({ open, onOpenChange, m, ctx, can, acts, who, onMoreEmoji }: {
  open: boolean; onOpenChange: (o: boolean) => void; m: AnyMsg; ctx: Ctx; can: Can; acts: Acts; who: string; onMoreEmoji: () => void;
}) {
  const close = (fn: () => void) => () => { onOpenChange(false); fn(); };
  const react = useReact(m, ctx);
  const mine = (m.reactions || []).filter((r) => r.mine).map((r) => r.emoji);
  const snippet = plain(shownText(m)).split("\n")[0];
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={who === "You" ? "Your message" : who + "’s message"}
      description={snippet ? <span className="line-clamp-2">{snippet}</span> : undefined}>
      {can.react && (
        <div className="mb-3 flex justify-center">
          <QuickReactions chosen={mine} onPick={(e) => { onOpenChange(false); react(e, mine.includes(e)); }} onMore={close(onMoreEmoji)} />
        </div>
      )}
      <div className="flex flex-col">
        {can.reply && <SheetItem icon={<IconArrowBackUp size={22} />} label="Reply" onClick={close(acts.reply)} />}
        <Items can={can} acts={{ ...acts, copy: close(acts.copy), edit: close(acts.edit), del: close(acts.del), details: close(acts.details), select: acts.select && close(acts.select) }} m={m}
          render={(icon, label, onClick, danger) => <SheetItem key={label} icon={icon} label={label} onClick={onClick} danger={danger} />} />
      </div>
    </Sheet>
  );
}

function SheetItem({ icon, label, onClick, danger }: { icon: ReactNode; label: string; onClick: () => void; danger?: boolean }) {
  return (
    <button type="button" onClick={onClick}
      className={"flex min-h-13 items-center gap-4 rounded-2xl px-3 text-left text-[16px] font-semibold hover:bg-sunken active:bg-sunken " + (danger ? "text-danger" : "text-ink")}>
      <span className="text-text-2 [&>svg]:shrink-0" aria-hidden="true">{icon}</span>{label}
    </button>
  );
}

// ---- long-press and swipe (touch only) ---------------------------------------------

/** inside: the event happened in the row's own DOM. React also bubbles
 *  events from portals (the row's sheets and menus) to the row; those are not the row's. */
export const inside = (e: { currentTarget: Element; target: EventTarget }) => e.currentTarget.contains(e.target as Node);

/** useTouchGestures: long-press opens the sheet; swiping right past 64px replies.
 *  Vertical scrolling stays native (touch-action: pan-y on the row). */
export function useTouchGestures(onLongPress: () => void, onSwipe: (() => void) | null) {
  const [dx, setDx] = useState(0);
  const g = useRef<{ x: number; y: number; timer: number; moved: boolean; id: number } | null>(null);
  const end = () => {
    const s = g.current;
    if (!s) return;
    clearTimeout(s.timer);
    g.current = null;
    setDx((d) => { if (d > 64 && onSwipe) onSwipe(); return 0; });
  };
  return {
    dx,
    handlers: {
      onPointerDown: (e: PointerEvent) => {
        if (e.pointerType !== "touch" || !inside(e)) return;
        const timer = window.setTimeout(() => {
          if (!g.current || g.current.moved) return;
          g.current = null;
          navigator.vibrate?.(8);
          onLongPress();
        }, 450);
        g.current = { x: e.clientX, y: e.clientY, timer, moved: false, id: e.pointerId };
      },
      onPointerMove: (e: PointerEvent) => {
        const s = g.current;
        if (!s || e.pointerId !== s.id) return;
        const x = e.clientX - s.x, y = e.clientY - s.y;
        if (!s.moved && Math.hypot(x, y) > 8) { s.moved = true; clearTimeout(s.timer); }
        if (onSwipe && s.moved && x > 0 && Math.abs(y) < 24) setDx(Math.min(x, 88));
        else if (Math.abs(y) >= 24) setDx(0);
      },
      onPointerUp: end,
      onPointerCancel: () => { if (g.current) clearTimeout(g.current.timer); g.current = null; setDx(0); },
      onContextMenu: (e: MouseEvent) => { if ((e.nativeEvent as globalThis.PointerEvent).pointerType === "touch" || g.current) e.preventDefault(); },
    },
  };
}

// ---- confirm ----------------------------------------------------------------------------

/** Confirm: a plain question with the change it makes, and a clear way out. */
export function Confirm({ open, onOpenChange, title, children, ok, onOk, danger = true }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: string; children: ReactNode; ok: string; onOk: () => void; danger?: boolean;
}) {
  const portal = usePortal();
  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal container={portal}>
        <AlertDialog.Backdrop className="fixed inset-0 z-40 bg-[#1B1530]/40 transition-opacity duration-200 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0" />
        <AlertDialog.Popup className="fixed left-1/2 top-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-3xl bg-canvas p-5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-200 ease-out-soft data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
          <AlertDialog.Title className="font-display text-[22px] font-extrabold leading-tight">{title}</AlertDialog.Title>
          <AlertDialog.Description render={<div />} className="mt-2 space-y-2 text-[15px] text-text-2">{children}</AlertDialog.Description>
          <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <AlertDialog.Close render={<Button variant="outline">Keep it</Button>} />
            <Button variant={danger ? "danger" : "act"} onClick={() => { onOpenChange(false); onOk(); }}>{ok}</Button>
          </div>
        </AlertDialog.Popup>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}

export function DeleteMessage({ open, onOpenChange, m, ctx }: { open: boolean; onOpenChange: (o: boolean) => void; m: AnyMsg; ctx: Ctx }) {
  const store = useApp();
  return (
    <Confirm open={open} onOpenChange={onOpenChange} title="Delete this message?" ok="Delete"
      onOk={() => void store.run((a) => a.retract(controlRef(m, ctx)), "Message deleted")}>
      <div className="line-clamp-4 rounded-xl bg-surface px-3 py-2 text-ink stroke">{shownText(m) ? <Markdown text={shownText(m)} /> : "(files only)"}</div>
      <p>It’s removed here and on devices that can read deletions. Anyone who already read it, saved its files or gave it to an agent keeps what they have.</p>
      {isRequest(m) && <p>What was sent to the agent stays on record under Details.</p>}
    </Confirm>
  );
}

// ---- edit in place ------------------------------------------------------------------------

export function EditBox({ m, ctx, onDone }: { m: AnyMsg; ctx: Ctx; onDone: () => void }) {
  const store = useApp();
  // Shown as @Name; a mention left untouched keeps its exact reference when saved, an edited one becomes text.
  const start = useRef(decode(shownText(m)));
  const [text, setText] = useState(start.current.text);
  const spans = useRef(start.current.spans);
  const area = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
  }, []);
  useEffect(() => {
    const el = area.current;
    if (el) { el.style.height = "auto"; el.style.height = Math.min(el.scrollHeight, 240) + "px"; }
  }, [text]);
  const change = (v: string) => { spans.current = shift(spans.current, text, v).kept; setText(v); };
  const save = async () => {
    const body = encode(text, spans.current).trim();
    if (!body) return;
    if (body === shownText(m)) { onDone(); return; }
    const r = await store.run((a) => a.edit({ ...controlRef(m, ctx), text: body }), "Edited");
    if (r) onDone();
  };
  return (
    <div className="flex flex-col gap-2">
      <textarea ref={area} value={text} onChange={(e) => change(e.target.value)} rows={2} aria-label="New text"
        onKeyDown={(e) => {
          if (e.key === "Escape") { e.preventDefault(); onDone(); }
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); void save(); }
        }}
        className="w-full resize-none rounded-xl bg-surface px-3 py-2 text-ink outline-none stroke focus-visible:outline-none" />
      {isRequest(m) && <p className="text-[13px] text-text-2">The agent keeps what it already got; nothing runs again.</p>}
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onDone}>Cancel</Button>
        <Button size="sm" variant="act" onClick={() => void save()} disabled={!text.trim()}>Save</Button>
      </div>
    </div>
  );
}

// ---- details --------------------------------------------------------------------------------

const copyIcon = (state: string) =>
  state === "delivered" ? <IconChecks size={18} className="text-ok-ink" />
    : state === "custody" ? <IconCheck size={18} className="text-text-2" />
      : ["failed", "expired", "quarantined"].includes(state) ? <IconAlertTriangle size={18} className="text-danger" />
        : <IconClock size={18} className="text-muted" />;

export function DetailsSheet({ open, onOpenChange, m, ctx, who }: { open: boolean; onOpenChange: (o: boolean) => void; m: AnyMsg; ctx: Ctx; who: string }) {
  const copies = isThreadMsg(m) ? [] : (m.copies || []);
  const at = new Date(m.at);
  const origin = (m as { origin?: string }).origin || "";
  const rows: [string, string][] = [
    ["Message", m.id],
    ["Kind", m.kind],
    ["Stored state", m.state || "—"],
    ["From device", m.from],
    ...(!isThreadMsg(m) && m.via ? [["Sent from", m.via] as [string, string]] : []),
    ...(!isThreadMsg(m) && m.synced_from ? [["Copied here from", m.synced_from] as [string, string]] : []),
    ...(!isThreadMsg(m) && m.excerpt_pid ? [["Shared under", m.excerpt_pid] as [string, string]] : []),
    ...(!isThreadMsg(m) && m.claimed_key ? [["Claimed author key", m.claimed_key] as [string, string]] : []),
    ...(origin ? [["Written by, as its device says", origin === "ui" ? "a person" : origin.replace(/^agent:/, "an agent (") + (origin.startsWith("agent:") ? ")" : "")] as [string, string]] : []),
    ...(m.exec ? [["Executor’s word", m.exec.state + " on " + m.exec.host + (m.exec.attempt ? " · attempt " + m.exec.attempt : "")] as [string, string]] : []),
    ...(m.revision ? [["Revision", String(m.revision)] as [string, string]] : []),
  ];
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title="Message details"
      description={(m.dir === "out" ? "Sent " : "Received ") + at.toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" }) + " at " + timeOf(m.at)}>
      <div className="flex flex-col gap-4 pb-2">
        {m.dir === "out" && copies.length > 0 && (
          <section>
            <h3 className="mb-1.5 text-[13px] font-bold uppercase tracking-wide text-muted">Where it is</h3>
            <ul className="divide-y divide-hairline rounded-2xl bg-surface stroke">
              {copies.map((c) => (
                <li key={c.to} className="flex items-center gap-3 px-3.5 py-2.5">
                  <span aria-hidden="true">{copyIcon(c.state)}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-semibold">{capital(deviceWords(c.to, ctx.overview))}</span>
                    <span className="block text-[13px] text-text-2">{copyText(c.state)}{c.detail ? " · " + c.detail : ""}</span>
                  </span>
                </li>
              ))}
            </ul>
            <p className="mt-1.5 text-[13px] text-muted">Delivered means their AgentNet stored it. It doesn’t mean anyone read it.</p>
          </section>
        )}
        {m.dir === "in" && <p className="text-[15px]">From {who}, as their AgentNet says.</p>}
        {m.edited && !m.deleted && (
          <section>
            <h3 className="mb-1.5 text-[13px] font-bold uppercase tracking-wide text-muted">Before it was edited</h3>
            <p className="whitespace-pre-wrap rounded-2xl bg-surface px-3.5 py-2.5 stroke [overflow-wrap:anywhere]">{plain(m.body)}</p>
          </section>
        )}
        {m.deleted && isRequest(m) && m.body && (
          <section>
            <h3 className="mb-1.5 text-[13px] font-bold uppercase tracking-wide text-muted">What was sent to the agent</h3>
            <p className="whitespace-pre-wrap rounded-2xl bg-surface px-3.5 py-2.5 stroke [overflow-wrap:anywhere]">{plain(m.body)}</p>
          </section>
        )}
        <details className="group rounded-2xl bg-sunken px-3.5 py-1">
          <summary className="flex min-h-11 cursor-pointer list-none items-center justify-between font-semibold [&::-webkit-details-marker]:hidden">
            Technical details <IconChevronDown size={18} className="transition-transform group-open:rotate-180" />
          </summary>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pb-2 text-[13px]">
            {rows.map(([k, v]) => [
              <dt key={k + "k"} className="text-muted">{k}</dt>,
              <dd key={k + "v"} className="min-w-0 break-all font-mono text-[12px] text-text-2">{v}</dd>,
            ])}
          </dl>
        </details>
      </div>
    </Sheet>
  );
}

const capital = (s: string) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);
