// The same video at window size, inside the skin's own modal tree. The Linux
// app turns WebKit fullscreen off: entering it aborted the whole app (WebKitGTK
// 2.50.4, November 2, 2026). As in Telegram Desktop's media viewer, the video
// fills the app window instead; position and play state carry in and back.
import { Dialog } from "@base-ui/react/dialog";
import { useRef } from "react";
import { IconX } from "@tabler/icons-react";
import { useModal, usePortal } from "../owned";

export type VideoPlace = { time: number; playing: boolean };

export function VideoViewer({ src, name, from, onClose }: {
  src: string; name: string; from: VideoPlace; onClose: (to: VideoPlace) => void;
}) {
  const portal = usePortal(), modal = useModal(true);
  const video = useRef<HTMLVideoElement>(null);
  const close = () => { const v = video.current; onClose(v && v.readyState ? { time: v.currentTime, playing: !v.paused && !v.ended } : from); };
  return (
    <Dialog.Root open onOpenChange={open => { if (!open) close(); }} modal="trap-focus">
      <Dialog.Portal container={portal}>
        <Dialog.Popup {...modal} className="fixed inset-0 z-50 flex flex-col bg-black text-white outline-none">
          <div className="flex shrink-0 items-center gap-3 px-3 py-2 pt-[max(8px,env(safe-area-inset-top))]">
            <Dialog.Title className="min-w-0 flex-1 truncate font-semibold">{name}</Dialog.Title>
            <Dialog.Description className="sr-only">Video at window size. Esc returns to the chat.</Dialog.Description>
            <Dialog.Close aria-label="Close" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-white/10"><IconX size={20} /></Dialog.Close>
          </div>
          <video ref={video} src={src} controls playsInline preload="auto" aria-label={name} className="block min-h-0 w-full flex-1 object-contain"
            onLoadedMetadata={e => { const v = e.currentTarget; v.currentTime = from.time; if (from.playing) void v.play().catch(() => {}); }} />
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
