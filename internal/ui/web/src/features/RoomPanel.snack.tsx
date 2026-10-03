// "Ledger left · dismissed by you · Bring back": the snackbar after a
// dismissal in the room. It sits at the foot of the panel or phone sheet,
// never over the conversation, so the new "left" divider stays in view.
import { useEffect, useRef } from "react";
import { Button } from "../ui/Button";
import { them } from "./RoomPanel.model";

export interface Snack { name: string; kind: "agent" | "person"; who: string; id: number }

export function BringBackSnack({ snack, onBringBack, onClose }: { snack: Snack; onBringBack: () => void; onClose: () => void }) {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const id = setTimeout(() => close.current(), 9000);
    return () => clearTimeout(id);
  }, [snack.id]);
  return (
    <div role="status" className="pop-in sticky bottom-3 z-20 mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-2xl stroke bg-ink py-2 pl-4 pr-2 text-canvas shadow-pop-sm">
      <p className="min-w-48 flex-1 py-0.5 text-[14px] leading-snug">
        <b className="font-bold">{snack.name} left</b> · dismissed by you
        <span className="block text-[13px] opacity-80">What was already shared stays with {them(snack.kind)}.</span>
      </p>
      <Button size="sm" variant="act" onClick={onBringBack} className="ml-auto">Bring back</Button>
    </div>
  );
}
