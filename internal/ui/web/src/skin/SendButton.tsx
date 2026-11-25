// The composer's send control (skin/README.md). It must stay a submit button
// named by `label` (Send, Send your reply…), refuse while not ready or busy
// (aria-disabled, so the field keeps focus), and keep focus in the field on
// a tap (onMouseDown={keep}). It sits at the right end of the composer's
// tools row, below the draft: 44px tall on a phone (an arrow), 40px from lg
// (the arrow and its words). An overlay skin may draw it however it likes.
import { IconArrowUp } from "@tabler/icons-react";
import type { MouseEvent as ReactMouseEvent } from "react";

/** keep stops a tap from taking focus off the text field. */
const keep = (e: ReactMouseEvent) => e.preventDefault();

export function SendButton({ ready, busy, label }: { ready: boolean; busy: boolean; label: string }) {
  const look = ready ? "bg-act stroke press hover:brightness-[1.03]" : "bg-sunken border-[1.5px] border-hairline lg:border";
  const ink = ready ? "text-act-ink" : "text-muted";
  const dots = <span aria-hidden="true" className={"flex gap-0.5 text-lg leading-none " + ink}><span className="working-dot">•</span><span className="working-dot">•</span><span className="working-dot">•</span></span>;
  return (
    <button type="submit" data-part="send" onMouseDown={keep} aria-disabled={!ready || busy} aria-label={label} title={label}
      className={"flex h-11 min-w-[52px] shrink-0 items-center justify-center gap-1.5 rounded-xl px-3 transition-colors duration-200 lg:h-10 " + look}>
      {busy ? dots : <>
        <IconArrowUp size={21} stroke={2.6} aria-hidden="true" className={"shrink-0 " + ink} />
        <span aria-hidden="true" className={"hidden truncate text-base font-semibold lg:block " + ink}>{label}</span>
      </>}
    </button>
  );
}
