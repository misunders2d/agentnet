// The composer's pieces: the "+" menu (also the desktop Send menu), the
// reply chip, the addressed-agent row, the send button, the tools row's
// buttons, the formatting strip, and the text field that shows mentions as chips.
// Colors and type sizes sit on inner elements, not on buttons themselves:
// the page's base sheet resets a button's font and color.
import { forwardRef, type CSSProperties, type ReactNode, type Ref, type MouseEvent as ReactMouseEvent, type TextareaHTMLAttributes } from "react";
import { Menu } from "@base-ui/react/menu";
import { IconBold, IconClearFormatting, IconClock, IconCode, IconCornerUpLeft, IconEyeOff, IconItalic, IconLink, IconPaperclip, IconPlus, IconQuote, IconStrikethrough, IconUnderline, IconUserPlus, IconX } from "@tabler/icons-react";
import { formats, type Format } from "./TextFormatting.model";
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
};

/** Opening visibility follows menu state, without waiting for animation frames.
 *  PlusMenu: the composer's "+" menu. finalFocus says whether closing it
 *  returns focus to "+" (not when an action hands focus to what it opened). */
export function PlusMenu({ actions, disabled, onOpenChange, onClosed, finalFocus, trigger }: { actions: MenuAction[]; disabled?: boolean; onOpenChange?: (open: boolean) => void; onClosed?: () => void; finalFocus?: () => boolean;
  /** Another trigger for the same menu (the desktop Send menu's chevron). */
  trigger?: { label: string; icon: ReactNode; className: string } }) {
  const portal = usePortal();
  return (
    <Menu.Root modal={false} onOpenChange={(o) => onOpenChange?.(o)} onOpenChangeComplete={(o) => { if (!o) onClosed?.(); }}>
      {trigger
        ? <Menu.Trigger disabled={disabled} aria-label={trigger.label} title={trigger.label} data-part="send-menu" onMouseDown={e => e.preventDefault()} className={trigger.className}>{trigger.icon}</Menu.Trigger>
        : <Menu.Trigger disabled={disabled} aria-label="Add to message" title="Add to message"
        className={"group data-[popup-open]:bg-ink " + sideButton}>
        <IconPlus size={22} stroke={2.4} className="text-ink transition-transform duration-200 ease-out-soft group-data-[popup-open]:rotate-45 group-data-[popup-open]:text-canvas motion-reduce:transition-none" />
      </Menu.Trigger>}
      <Menu.Portal container={portal}>
        <Menu.Positioner side="top" align={trigger ? "end" : "start"} sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Menu.Popup finalFocus={finalFocus} className="w-64 origin-[var(--transform-origin)] rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[transform,opacity] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
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

/** sideButton: the round buttons beside the field ("+", "@"). Same size and
 *  border as the field and the send button at every width, so their edges
 *  and centres line up. */
export const sideButton = "grid size-[46px] shrink-0 place-items-center self-end rounded-full bg-surface stroke press hover:bg-sunken disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-agent-ink lg:size-12";

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

/** toolButton: the flat buttons in the composer's tools row (Aa, file, @,
 *  emoji, schedule, formatting). 44px square on a phone, 40px from lg; a
 *  refused one stays focusable (aria-disabled) and says why when pressed. */
export const toolButton = "grid size-11 shrink-0 place-items-center rounded-xl text-ink hover:bg-sunken aria-disabled:opacity-45 aria-expanded:bg-sunken data-[popup-open]:bg-sunken focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-agent-ink lg:size-10";

const formatIcons: Record<Format, ReactNode> = {
  bold: <IconBold size={20} stroke={2.2} />, italic: <IconItalic size={20} stroke={2.2} />, underline: <IconUnderline size={20} stroke={2.2} />,
  strike: <IconStrikethrough size={20} stroke={2.2} />, code: <IconCode size={20} stroke={2.2} />, quote: <IconQuote size={20} stroke={2.2} />,
  spoiler: <IconEyeOff size={20} stroke={2.2} />, link: <IconLink size={20} stroke={2.2} />, clear: <IconClearFormatting size={20} stroke={2.2} />,
};

/** FormatStrip: the formatting actions as buttons, in the same order and
 *  with the same names and shortcuts as the formatting menu. They keep the
 *  field's focus and selection (mousedown kept) and act on the live
 *  selection; without one they are dimmed and titled "Select text…". */
export function FormatStrip({ id, selected, shortcut, run, className }: { id: string; selected: boolean; shortcut: (f: typeof formats[number]) => string; run: (f: Format) => void; className: string }) {
  return (
    <div id={id} role="group" aria-label="Formatting options" data-part="composer-format" className={className}>
      {formats.map(f => (
        <button key={f.id} type="button" aria-label={f.label} title={selected ? f.label + " (" + shortcut(f) + ")" : "Select text to format: " + f.label}
          aria-disabled={!selected} onMouseDown={keep} onClick={() => run(f.id)} className={toolButton}>
          <span aria-hidden="true">{formatIcons[f.id]}</span>
        </button>
      ))}
    </div>
  );
}

export { SendButton } from "../skin/SendButton";

/** Field: the full-width draft inside the composer's frame: a textarea over a mirror that draws the same text, with exact
 *  mentions as chips. The textarea keeps the caret, selection, IME and
 *  spelling; its own glyphs are transparent, so what shows is the mirror.
 *  Selection tints the mirror without painting a second copy of the text.
 *  Both take their type metrics from one inline style, so no sheet can
 *  pull them apart. */
export const Field = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & {
  text: string; spans: Span[]; wide: boolean; mirror: Ref<HTMLDivElement>;
}>(function Field({ text, spans, wide, mirror, placeholder, className = "", ...rest }, ref) {
  const metrics: CSSProperties = { fontFamily: "var(--font-sans)", fontSize: wide ? 15 : 16, lineHeight: "22px", fontWeight: 400, letterSpacing: "normal" };
  const box = "pt-3 pb-1.5 px-3.5 whitespace-pre-wrap break-words";
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
    <div className={"relative min-w-0 overflow-hidden " + className}>
      <div ref={mirror} aria-hidden="true" className={"pointer-events-none absolute inset-0 overflow-hidden text-ink " + box} style={metrics}>
        <div data-composer-measure>{parts}</div>
        {/* The mirror draws the hint too: a textarea's own placeholder wraps or
            clips mid-letter on a narrow field, never ending in an ellipsis. */}
        {!text && placeholder && <div className="-mt-[22px] truncate text-muted">{placeholder}</div>}
      </div>
      <textarea ref={ref} rows={1} placeholder={placeholder} {...rest} style={{ ...metrics, color: "transparent", outline: "none" }}
        className={"relative block w-full resize-none bg-transparent caret-ink [scrollbar-width:none] placeholder:text-transparent selection:bg-act/40 selection:text-transparent disabled:cursor-not-allowed " + box} />
    </div>
  );
});
