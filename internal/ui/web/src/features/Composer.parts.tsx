// The composer's pieces: the "+" menu, the reply chip, the addressed-agent
// row, the send button, and the text field that shows mentions as chips.
// Colors and type sizes sit on inner elements, not on buttons themselves:
// the page's base sheet resets a button's font and color.
import { forwardRef, type CSSProperties, type ReactNode, type Ref, type MouseEvent as ReactMouseEvent, type TextareaHTMLAttributes } from "react";
import { Menu } from "@base-ui/react/menu";
import { IconClock, IconCornerUpLeft, IconMoodSmile, IconPaperclip, IconPlus, IconUserPlus, IconX } from "@tabler/icons-react";
import { AgentAvatar } from "../ui/Avatar";
import type { Span } from "./Composer.mentions";
import { usePortal } from "../owned";

/** keep stops a tap from taking focus off the text field, so a phone's
 *  keyboard stays open; the click itself still happens. */
export const keep = (e: ReactMouseEvent) => e.preventDefault();

export interface MenuAction { label: string; sub?: string; icon: ReactNode; tone: string; disabled?: boolean; run: () => void }

export const menuIcons = {
  clock: <IconClock size={20} stroke={2} />,
  invite: <IconUserPlus size={20} stroke={2} />,
  file: <IconPaperclip size={20} stroke={2} />,
  emoji: <IconMoodSmile size={20} stroke={2} />,
};

/** PlusMenu: the composer's "+" menu. finalFocus says whether closing it
 *  returns focus to "+" (not when an action hands focus to what it opened). */
export function PlusMenu({ actions, disabled, onOpenChange, onClosed, finalFocus }: { actions: MenuAction[]; disabled?: boolean; onOpenChange?: (open: boolean) => void; onClosed?: () => void; finalFocus?: () => boolean }) {
  const portal = usePortal();
  return (
    <Menu.Root modal={false} onOpenChange={(o) => onOpenChange?.(o)} onOpenChangeComplete={(o) => { if (!o) onClosed?.(); }}>
      <Menu.Trigger disabled={disabled} aria-label="Add to message" title="Add to message"
        className="group grid size-11 shrink-0 place-items-center self-end rounded-full bg-surface stroke press hover:bg-sunken disabled:opacity-40 data-[popup-open]:bg-ink lg:mb-0.5">
        <IconPlus size={22} stroke={2.4} className="text-ink transition-transform duration-200 ease-out-soft group-data-[popup-open]:rotate-45 group-data-[popup-open]:text-canvas motion-reduce:transition-none" />
      </Menu.Trigger>
      <Menu.Portal container={portal}>
        <Menu.Positioner side="top" align="start" sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Menu.Popup finalFocus={finalFocus} className="w-64 origin-[var(--transform-origin)] rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[transform,opacity] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 motion-reduce:transition-opacity">
            {actions.map((a) => (
              <Menu.Item key={a.label} disabled={a.disabled} onClick={a.run}
                className="flex min-h-12 cursor-pointer items-center gap-3 rounded-xl px-2.5 py-1.5 outline-none select-none data-[highlighted]:bg-sunken data-[disabled]:cursor-default data-[disabled]:opacity-50">
                <span aria-hidden="true" className={"grid size-9 shrink-0 place-items-center rounded-xl " + a.tone}>{a.icon}</span>
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

/** The addressed agent, with a way to return to ordinary chat. */
export function IntentRow({ name, seed, onStop, note }: {
 name: string; seed: string; onStop?: () => void; note?: string;
}) {
 return <div className="mb-1 fade-in">
  <div className="flex items-center gap-1.5 text-[13px] font-bold text-text-2">
   <AgentAvatar seed={seed} size={24}/><span className="truncate">Asking {name}</span>
   {onStop && <button type="button" onMouseDown={keep} onClick={onStop} aria-label={"Don’t ask " + name} className="grid size-11 place-items-center rounded-full hover:bg-sunken"><IconX size={18}/></button>}
  </div>
  {note && <p className="pb-1.5 text-[13px] leading-snug text-text-2">{note}</p>}
 </div>;
}

export { SendButton } from "../skin/SendButton";

/** Field: a textarea over a mirror that draws the same text, with exact
 *  mentions as chips. The textarea keeps the caret, selection, IME and
 *  spelling; its own glyphs are transparent, so what shows is the mirror.
 *  Selection tints the mirror without painting a second copy of the text.
 *  Both take their type metrics from one inline style, so no sheet can
 *  pull them apart. */
export const Field = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & {
  text: string; spans: Span[]; wide: boolean; mirror: Ref<HTMLDivElement>;
}>(function Field({ text, spans, wide, mirror, className = "", ...rest }, ref) {
  const metrics: CSSProperties = { fontFamily: "var(--font-sans)", fontSize: wide ? 15 : 16, lineHeight: "22px", fontWeight: 400, letterSpacing: "normal" };
  const box = "px-4 py-[10.5px] whitespace-pre-wrap break-words lg:py-3";
  const parts: ReactNode[] = [];
  let last = 0;
  for (const s of [...spans].sort((a, b) => a.start - b.start)) {
    parts.push(text.slice(last, s.start));
    const look = s.kind === "agent" ? "bg-agent text-agent-ink [box-shadow:inset_0_0_0_1px_var(--an-agent-fill)]"
      : s.kind === "guest" ? "bg-guest-bg text-guest-ink [box-shadow:inset_0_0_0_1px_var(--an-guest)]"
      : "bg-surface text-ink [box-shadow:inset_0_0_0_1px_var(--an-outline)]";
    parts.push(<span key={s.start} className={"-mx-[3px] rounded-md px-[3px] " + look}>{"@" + s.name}</span>);
    last = s.start + s.name.length + 1;
  }
  parts.push(text.slice(last) + "​"); // a trailing newline still gets its line
  return (
    <div data-part="composer-field" className={"relative min-w-0 flex-1 overflow-hidden rounded-[23px] bg-canvas stroke transition-shadow duration-200 focus-within:[box-shadow:0_0_0_3px_color-mix(in_srgb,var(--an-agent-ink)_30%,transparent)] lg:rounded-2xl " + className}>
      <div ref={mirror} aria-hidden="true" className={"pointer-events-none absolute inset-0 overflow-hidden text-ink " + box} style={metrics}>
        <div data-composer-measure>{parts}</div>
      </div>
      <textarea ref={ref} rows={1} {...rest} style={{ ...metrics, color: "transparent", outline: "none" }}
        className={"relative block w-full resize-none bg-transparent caret-ink [scrollbar-width:none] placeholder:text-muted selection:bg-act/40 selection:text-transparent disabled:cursor-not-allowed " + box} />
    </div>
  );
});
