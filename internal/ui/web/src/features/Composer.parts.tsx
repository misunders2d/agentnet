// The composer's pieces: the "+" menu, the reply chip, the Answer | Do it
// row, the send button, and the text field that shows mentions as chips.
// Colors and type sizes sit on inner elements, not on buttons themselves:
// the page's base sheet resets a button's font and color.
import { forwardRef, type CSSProperties, type ReactNode, type Ref, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent, type TextareaHTMLAttributes } from "react";
import { Menu } from "@base-ui/react/menu";
import { IconArrowUp, IconBolt, IconCornerUpLeft, IconMessageCircle, IconMoodSmile, IconPaperclip, IconPlus, IconUserPlus, IconX } from "@tabler/icons-react";
import { AgentAvatar } from "../ui/Avatar";
import type { Span } from "./Composer.mentions";

/** keep stops a tap from taking focus off the text field, so a phone's
 *  keyboard stays open; the click itself still happens. */
export const keep = (e: ReactMouseEvent) => e.preventDefault();

export interface MenuAction { label: string; sub?: string; icon: ReactNode; tone: string; disabled?: boolean; run: () => void }

export const menuIcons = {
  invite: <IconUserPlus size={20} stroke={2} />,
  file: <IconPaperclip size={20} stroke={2} />,
  emoji: <IconMoodSmile size={20} stroke={2} />,
};

export function PlusMenu({ actions, disabled, onOpenChange, onClosed }: { actions: MenuAction[]; disabled?: boolean; onOpenChange?: (open: boolean) => void; onClosed?: () => void }) {
  return (
    <Menu.Root onOpenChange={(o) => onOpenChange?.(o)} onOpenChangeComplete={(o) => { if (!o) onClosed?.(); }}>
      <Menu.Trigger disabled={disabled} aria-label="Add to message" title="Add to message"
        className="group grid size-11 shrink-0 place-items-center self-end rounded-full bg-surface stroke press hover:bg-sunken disabled:opacity-40 data-[popup-open]:bg-ink lg:mb-0.5">
        <IconPlus size={22} stroke={2.4} className="text-ink transition-transform duration-200 ease-out-soft group-data-[popup-open]:rotate-45 group-data-[popup-open]:text-canvas motion-reduce:transition-none" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner side="top" align="start" sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Menu.Popup className="w-72 origin-[var(--transform-origin)] rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[transform,opacity] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 motion-reduce:transition-opacity">
            {actions.map((a) => (
              <Menu.Item key={a.label} disabled={a.disabled} onClick={a.run}
                className="flex min-h-14 cursor-pointer items-center gap-3 rounded-xl px-2.5 py-1.5 outline-none select-none data-[highlighted]:bg-sunken data-[disabled]:cursor-default data-[disabled]:opacity-50">
                <span aria-hidden="true" className={"grid size-10 shrink-0 place-items-center rounded-xl " + a.tone}>{a.icon}</span>
                <span className="min-w-0">
                  <span className="block font-semibold leading-tight">{a.label}</span>
                  {a.sub && <span className="mt-0.5 block text-[13px] leading-tight text-muted">{a.sub}</span>}
                </span>
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/** ReplyChip: what this message replies to; or, for a request answered by
 *  hand, what is answered and what that changes (`note`). */
export function ReplyChip({ title, text, note, cancel, onCancel }: { title: string; text: string; note?: string; cancel: string; onCancel: () => void }) {
  return (
    <div className="mb-2 flex items-center gap-2 rounded-2xl bg-canvas py-1 pl-3 stroke fade-in">
      <IconCornerUpLeft size={18} stroke={2.2} className="shrink-0 text-muted" aria-hidden="true" />
      <div className={"min-w-0 flex-1 border-l-[3px] pl-2.5 " + (note ? "border-act py-1" : "border-agent-fill")}>
        <p className="text-[13px] font-bold leading-snug">{title}</p>
        {text && <p className="truncate text-[13px] leading-snug text-text-2">{text}</p>}
        {note && <p className="pt-0.5 text-[13px] leading-snug text-text-2">{note}</p>}
      </div>
      <button type="button" onMouseDown={keep} onClick={onCancel} aria-label={cancel} title={cancel} className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken">
        <IconX size={18} stroke={2.4} className="text-text-2" />
      </button>
    </div>
  );
}

/** IntentRow: "Zen should [Answer | Do it]", shown once an agent is addressed. */
export function IntentRow({ name, seed, doIt, disabled, onChoose, onStop, note }: {
  name: string; seed: string; doIt: boolean; disabled?: boolean; onChoose: (doIt: boolean) => void; onStop?: () => void; note?: string;
}) {
  const keys = (e: ReactKeyboardEvent) => {
    if (!disabled && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key)) {
      e.preventDefault();
      onChoose(!doIt);
      (e.currentTarget.querySelector(doIt ? "[data-answer]" : "[data-doit]") as HTMLElement | null)?.focus();
    }
  };
  const seg = "flex h-9 items-center gap-1.5 px-3 text-sm font-bold transition-colors duration-200 ease-out-soft stroke";
  return (
    <div className="mb-1 fade-in">
      <div className="flex flex-wrap items-center gap-x-2">
        <span className="flex min-w-0 items-center gap-1.5 text-[13px] font-bold text-text-2">
          <AgentAvatar seed={seed} size={24} />
          <span className="truncate">{name} should</span>
        </span>
        <span className="flex items-center">
          <span role="radiogroup" aria-label={"What " + name + " should do"} aria-disabled={disabled || undefined} onKeyDown={keys}
            className={"flex " + (disabled ? "pointer-events-none opacity-45" : "")}>
            <button type="button" role="radio" aria-checked={!doIt} tabIndex={doIt ? -1 : 0} disabled={disabled} data-answer="" onMouseDown={keep} onClick={() => onChoose(false)} className="flex h-11 items-center">
              <span className={seg + " rounded-l-xl " + (!doIt ? "bg-agent-fill text-ink" : "bg-canvas text-text-2 hover:bg-sunken")}>
                <IconMessageCircle size={16} stroke={2.4} aria-hidden="true" />Answer
              </span>
            </button>
            <button type="button" role="radio" aria-checked={doIt} tabIndex={doIt ? 0 : -1} disabled={disabled} data-doit="" onMouseDown={keep} onClick={() => onChoose(true)} className="flex h-11 items-center">
              <span className={seg + " -ml-px rounded-r-xl " + (doIt ? "bg-act text-act-ink" : "bg-canvas text-text-2 hover:bg-sunken")}>
                <IconBolt size={16} stroke={2.4} aria-hidden="true" />Do it
              </span>
            </button>
          </span>
          {onStop && (
            <button type="button" onMouseDown={keep} onClick={onStop} aria-label={"Don’t ask " + name} title={"Don’t ask " + name} className="grid size-11 place-items-center rounded-full hover:bg-sunken">
              <IconX size={18} stroke={2.4} className="text-muted" />
            </button>
          )}
        </span>
        <span className="hidden text-[13px] text-muted lg:inline">Alt+D switches</span>
      </div>
      {note && <p className="pb-1.5 text-[13px] leading-snug text-text-2">{note}</p>}
    </div>
  );
}

export function SendButton({ doIt, ready, busy, label }: { doIt: boolean; ready: boolean; busy: boolean; label: string }) {
  const look = ready ? "bg-act stroke shadow-pop-sm press hover:brightness-[1.03]" : "bg-sunken border-[1.5px] border-hairline lg:border";
  const ink = ready ? "text-act-ink" : "text-muted";
  const dots = <span aria-hidden="true" className={"flex gap-0.5 text-lg leading-none " + ink}><span className="working-dot">•</span><span className="working-dot">•</span><span className="working-dot">•</span></span>;
  if (doIt) return (
    <button type="submit" onMouseDown={keep} aria-disabled={!ready || busy} aria-label={label} title={label}
      className={"flex h-[46px] shrink-0 items-center self-end rounded-full px-4 transition-colors duration-200 lg:h-12 " + look}>
      {busy ? dots : <span className={"flex items-center gap-1.5 text-base font-bold " + ink}><IconBolt size={20} stroke={2.4} aria-hidden="true" />Do it</span>}
    </button>
  );
  return (
    <button type="submit" onMouseDown={keep} aria-disabled={!ready || busy} aria-label={label} title={label}
      className={"grid size-[46px] shrink-0 place-items-center self-end rounded-full transition-colors duration-200 lg:size-12 " + look}>
      {busy ? dots : <IconArrowUp size={24} stroke={2.6} aria-hidden="true" className={ink} />}
    </button>
  );
}

/** Field: a textarea over a mirror that draws the same text, with exact
 *  mentions as chips. The textarea keeps the caret, selection, IME and
 *  spelling; its own glyphs are transparent, so what shows is the mirror.
 *  Both take their type metrics from one inline style, so no sheet can
 *  pull them apart. */
export const Field = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & {
  text: string; spans: Span[]; agent?: string; wide: boolean; mirror: Ref<HTMLDivElement>;
}>(function Field({ text, spans, agent, wide, mirror, className = "", ...rest }, ref) {
  const metrics: CSSProperties = { fontFamily: "var(--font-sans)", fontSize: wide ? 15 : 16, lineHeight: "22px", fontWeight: 400, letterSpacing: "normal" };
  const box = "px-4 py-[10.5px] whitespace-pre-wrap break-words lg:py-3";
  const parts: ReactNode[] = [];
  let last = 0;
  for (const s of [...spans].sort((a, b) => a.start - b.start)) {
    parts.push(text.slice(last, s.start));
    const look = s.kind === "agent" ? (s.id === agent ? "bg-agent text-agent-ink [box-shadow:inset_0_0_0_1px_var(--an-agent-fill)]" : "")
      : s.kind === "guest" ? "bg-guest-bg text-guest-ink [box-shadow:inset_0_0_0_1px_var(--an-guest)]"
      : "bg-surface text-ink [box-shadow:inset_0_0_0_1px_var(--an-outline)]";
    parts.push(<span key={s.start} className={"-mx-[3px] rounded-md px-[3px] " + look}>{"@" + s.name}</span>);
    last = s.start + s.name.length + 1;
  }
  parts.push(text.slice(last) + "​"); // a trailing newline still gets its line
  return (
    <div className={"relative min-w-0 flex-1 overflow-hidden rounded-[23px] bg-canvas stroke transition-shadow duration-200 focus-within:[box-shadow:0_0_0_3px_color-mix(in_srgb,var(--an-agent-ink)_30%,transparent)] lg:rounded-2xl " + className}>
      <div ref={mirror} aria-hidden="true" className={"pointer-events-none absolute inset-0 overflow-hidden text-ink " + box} style={metrics}>{parts}</div>
      <textarea ref={ref} rows={1} {...rest} style={{ ...metrics, color: "transparent", outline: "none" }}
        className={"relative block w-full resize-none bg-transparent caret-ink [scrollbar-width:none] placeholder:text-muted selection:bg-act selection:text-act-ink disabled:cursor-not-allowed " + box} />
    </div>
  );
});
