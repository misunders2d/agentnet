// Reactions: sticker chips under a message, inside its column. People's
// marks share one chip per emoji (tap toggles yours); each agent's own mark
// is its own chip with its name, so it never reads as a person's.
import { useState, type ReactElement, type ReactNode } from "react";
import { Popover } from "@base-ui/react/popover";
import { Dialog } from "@base-ui/react/dialog";
import { IconMoodPlus } from "@tabler/icons-react";
import type { T } from "../api";
import { useApp } from "../context";
import { EmojiPicker, QuickReactions } from "./Emoji";
import { agentLabel, agentOf, isThreadMsg, type AnyMsg, type Ctx } from "./Message.model";
import { useModal, usePortal } from "../owned";

export const controlRef = (m: AnyMsg, ctx: Ctx): T.ControlAction =>
  isThreadMsg(m) ? { id: m.id, dir: m.dir } : { conv: ctx.conv, id: m.id, dir: m.dir };

export function useReact(m: AnyMsg, ctx: Ctx) {
  const store = useApp();
  return (emoji: string, remove = false) => void store.run((a) => a.react({ ...controlRef(m, ctx), emoji, remove }));
}

const reactor = (b: T.Reactor, ctx: Ctx) => {
  if (!b.assistant) return b.label || "Someone";
  const a = agentOf(ctx, b.pid);
  return a ? agentLabel(a, ctx) : (b.agent_id && ctx.names[b.agent_id]) || "An agent";
};

export function Reactions({ m, ctx, can, wide }: { m: AnyMsg; ctx: Ctx; can: boolean; wide: boolean }) {
  const react = useReact(m, ctx);
  const list = m.reactions || [];
  if (!list.length || m.deleted) return null;
  return (
    <div className={"flex flex-wrap items-center gap-x-1.5 " + (wide ? "-mb-1" : "-mb-1.5")}>
      {list.map((r) => {
        const people = (r.by || []).filter((b) => !b.assistant);
        const agents = (r.by || []).filter((b) => b.assistant);
        const names = people.map((b) => reactor(b, ctx)).join(", ");
        return [
          people.length > 0 && (
            <Chip key={r.emoji} mine={!!r.mine} disabled={!can}
              label={r.emoji + " from " + names + (can ? (r.mine ? ". Tap to take yours back" : ". Tap to add yours") : "")}
              onClick={() => react(r.emoji, !!r.mine)}>
              <span className="text-[17px] leading-none">{r.emoji}</span>
              <span className="tnum">{people.length}</span>
            </Chip>
          ),
          ...agents.map((b) => (
            <Chip key={r.emoji + (b.pid || b.id)} agent disabled label={r.emoji + " from " + reactor(b, ctx) + ", an agent"}>
              <span className="text-[17px] leading-none">{r.emoji}</span>
              <span className="max-w-28 truncate">{reactor(b, ctx)}</span>
            </Chip>
          )),
        ];
      })}
      {can && <AddReaction m={m} ctx={ctx} wide={wide} />}
    </div>
  );
}

// Chip: a 32px sticker inside a 44px-tall target.
function Chip({ children, mine, agent, disabled, label, onClick }: { children: ReactNode; mine?: boolean; agent?: boolean; disabled?: boolean; label: string; onClick?: () => void }) {
  const face = "inline-flex h-8 items-center gap-1 rounded-full border-[1.5px] px-2.5 text-[13px] font-bold transition-transform duration-200 ease-out-soft "
    + (mine ? "border-outline bg-act text-act-ink" : agent ? "border-agent-ink/40 bg-agent text-agent-ink" : "border-outline/70 bg-surface text-ink");
  if (disabled) return <span className="inline-flex h-11 items-center" title={label} aria-label={label} role="img"><span className={face}>{children}</span></span>;
  return (
    <button type="button" onClick={onClick} title={label} aria-label={label} aria-pressed={mine}
      className="group/chip inline-flex h-11 items-center">
      <span className={face + " group-hover/chip:-translate-y-px group-active/chip:scale-95 motion-reduce:transform-none"}>{children}</span>
    </button>
  );
}

function AddReaction({ m, ctx, wide }: { m: AnyMsg; ctx: Ctx; wide: boolean }) {
  return (
    <ReactPopover m={m} ctx={ctx} wide={wide}
      trigger={<button type="button" aria-label="Add a reaction" title="Add a reaction" className="group/add inline-grid size-11 place-items-center">
        <span className="grid size-8 place-items-center rounded-full border-[1.5px] border-dashed border-outline/50 text-text-2 group-hover/add:border-solid group-hover/add:bg-surface"><IconMoodPlus size={17} /></span>
      </button>} />
  );
}

/** ReactPopover: the quick reactions, then the whole picker, anchored to its trigger. */
export function ReactPopover({ m, ctx, trigger, wide, onDone }: { m: AnyMsg; ctx: Ctx; trigger: ReactElement; wide: boolean; onDone?: () => void }) {
  const portal = usePortal();
  const [open, setOpen] = useState(false);
  const [more, setMore] = useState(false);
  const react = useReact(m, ctx);
  const mine = (m.reactions || []).filter((r) => r.mine).map((r) => r.emoji);
  const pick = (e: string) => { react(e, mine.includes(e)); setOpen(false); onDone?.(); };
  return (
    <Popover.Root open={open} onOpenChange={(o) => { setOpen(o); if (!o) setMore(false); }}>
      <Popover.Trigger render={trigger} />
      <Popover.Portal container={portal}>
        <Popover.Positioner side="top" align={wide ? "center" : "start"} sideOffset={6} collisionPadding={12} className="z-50">
          <Popover.Popup className="rounded-full outline-none transition-[opacity,scale] duration-200 ease-out-soft data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
            <Popover.Title className="sr-only">React to this message</Popover.Title>
            {more ? <EmojiPicker onPick={pick} onClose={() => setOpen(false)} />
              : <span className="block rounded-full shadow-pop"><QuickReactions onPick={pick} onMore={() => setMore(true)} chosen={mine} /></span>}
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** EmojiDialog: the whole picker on its own, for phones (opened from a message's sheet).
 *  Like a sheet, it sits on the keyboard (--an-keyboard): its search opens one. */
export function EmojiDialog({ open, onOpenChange, m, ctx }: { open: boolean; onOpenChange: (o: boolean) => void; m: AnyMsg; ctx: Ctx }) {
  const portal = usePortal();
  const modal = useModal(open);
  const react = useReact(m, ctx);
  const mine = (m.reactions || []).filter((r) => r.mine).map((r) => r.emoji);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange} modal="trap-focus">
      <Dialog.Portal container={portal}>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-[#1B1530]/40 transition-opacity duration-200 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0" />
        <Dialog.Popup {...modal} className="fixed bottom-[calc(var(--an-keyboard,0px)_+_12px)] left-1/2 z-50 -translate-x-1/2 outline-none transition-[opacity,translate] duration-[280ms] ease-out-soft data-[starting-style]:translate-y-8 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
          <Dialog.Title className="sr-only">Choose a reaction</Dialog.Title>
          <EmojiPicker onPick={(e) => { react(e, mine.includes(e)); onOpenChange(false); }} onClose={() => onOpenChange(false)} />
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
