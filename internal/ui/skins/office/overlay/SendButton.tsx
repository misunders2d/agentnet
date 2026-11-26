// Office's send control (Comic kit slot, skin/README.md): a plain square
// button, green when the message can go, grey otherwise. It stays a submit
// button named by `label`, refuses while not ready or busy (aria-disabled, so
// the field keeps focus) and keeps focus in the field on a tap.
import { IconSend } from "@tabler/icons-react";
import type { MouseEvent as ReactMouseEvent } from "react";

/** keep stops a tap from taking focus off the text field. */
const keep = (e: ReactMouseEvent) => e.preventDefault();

export function SendButton({ ready, busy, label }: { ready: boolean; busy: boolean; label: string }) {
  const look = ready ? "office-send-on" : "office-send-off";
  const dots = <span aria-hidden="true" className="flex gap-0.5 text-lg leading-none"><span className="working-dot">•</span><span className="working-dot">•</span><span className="working-dot">•</span></span>;
  return (
    <button type="submit" data-part="send" onMouseDown={keep} aria-disabled={!ready || busy} aria-label={label} title={label}
      className={"office-send grid h-11 min-w-[52px] shrink-0 grid-flow-col place-items-center gap-1.5 px-3 transition-colors duration-150 lg:h-10 " + look}>
      {busy ? dots : <>
        <IconSend size={20} stroke={2} aria-hidden="true" />
        <span aria-hidden="true" className="hidden text-base font-semibold lg:block">{label}</span>
      </>}
    </button>
  );
}
