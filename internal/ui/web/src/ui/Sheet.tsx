// Sheet: a bottom sheet on phones (at most 85% of the height, with a
// handle, so part of the chat stays visible) and a centred card on wide
// screens. Base UI's Dialog gives Escape, labelling and focus return;
// useModal keeps focus inside it and the app behind it inert.
import { Dialog } from "@base-ui/react/dialog";
import type { ReactNode } from "react";
import { IconX } from "@tabler/icons-react";
import { useModal, usePortal } from "../owned";

export function Sheet({ open, onOpenChange, title, description, children, footer, wide }: {
  open: boolean; onOpenChange: (open: boolean) => void; title: ReactNode; description?: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean;
}) {
  const portal = usePortal();
  const modal = useModal(open);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange} modal="trap-focus">
      <Dialog.Portal container={portal}>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-[#1B1530]/40 transition-opacity duration-200 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0" />
        <Dialog.Popup {...modal} className={"fixed z-50 flex flex-col bg-canvas text-ink outline-none stroke shadow-pop transition-all duration-[280ms] ease-out-soft "
          + "inset-x-0 bottom-0 max-h-[85dvh] rounded-t-3xl data-[starting-style]:translate-y-full data-[ending-style]:translate-y-full "
          + "lg:inset-auto lg:left-1/2 lg:top-1/2 lg:-translate-x-1/2 lg:-translate-y-1/2 lg:rounded-3xl lg:max-h-[86dvh] lg:data-[starting-style]:translate-y-[-46%] lg:data-[starting-style]:opacity-0 lg:data-[ending-style]:opacity-0 "
          + (wide ? "lg:w-[640px]" : "lg:w-[520px]")}>
          <div className="mx-auto mt-2 h-1.5 w-11 rounded-full bg-hairline lg:hidden" aria-hidden="true" />
          <div className="flex items-start gap-3 px-5 pt-3 pb-2 lg:pt-5">
            <div className="min-w-0 flex-1">
              <Dialog.Title className="font-display font-extrabold text-[26px] leading-tight">{title}</Dialog.Title>
              {description && <Dialog.Description className="mt-1 text-text-2">{description}</Dialog.Description>}
            </div>
            <Dialog.Close aria-label="Close" className="grid size-11 shrink-0 place-items-center rounded-full stroke bg-surface hover:bg-sunken"><IconX size={20} /></Dialog.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-4">{children}</div>
          {footer && <div className="border-t border-hairline px-5 py-3 pb-[max(12px,env(safe-area-inset-bottom))]">{footer}</div>}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
