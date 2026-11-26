// View the original bytes at any size, inside the skin's own modal tree.
// Zoom uses the image's natural dimensions; no canvas or resized copy.
import { Dialog } from "@base-ui/react/dialog";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { IconChevronLeft, IconChevronRight, IconMinus, IconPlus, IconX } from "@tabler/icons-react";
import { useModal, usePortal } from "../owned";
import { errorText } from "../api";
import { Button, IconButton } from "./Button";

type Point = { x: number; y: number };
type View = Point & { zoom: number };
type OpenState = { pending: true } | { pending: false; text: string; error: boolean };
const initial: View = { zoom: 1, x: 0, y: 0 };

export type ImageNavigation = { index: number; total: number; step: (delta: -1 | 1) => void };

export function ImageViewer({ src, name, imageKey, loadFailed, opener, navigation, onClose, actions, openInApp }: {
  src?: string; name: string; imageKey: string; loadFailed: boolean; opener: HTMLButtonElement;
  navigation?: ImageNavigation; onClose: () => void; actions?: ReactNode; openInApp?: () => Promise<unknown>;
}) {
  const portal = usePortal(), modal = useModal(true);
  const [stage, setStage] = useState<HTMLDivElement | null>(null);
  const pointers = useRef(new Map<number, Point>());
  const swipe = useRef<(Point & { pointerId: number }) | null>(null);
  const [image, setImage] = useState({ width: 0, height: 0 });
  const [area, setArea] = useState({ width: 0, height: 0 });
  const [view, setView] = useState(initial);
  const [decodeFailed, setDecodeFailed] = useState(false);
  const failed = loadFailed || decodeFailed;
  const unavailable = !src || failed;
  const [openStates, setOpenStates] = useState(() => new Map<string, OpenState>());
  const openState = openStates.get(imageKey);
  const setOpenState = (state: OpenState) => {
    setOpenStates(current => new Map(current).set(imageKey, state));
  };
  useLayoutEffect(() => {
    swipe.current = null;
    pointers.current.clear();
    setImage({ width: 0, height: 0 });
    setView(initial);
    setDecodeFailed(false);
  }, [imageKey]);
  const fit = image.width && area.width && area.height ? Math.min(1, area.width / image.width, area.height / image.height) : 1;
  const maxZoom = Math.max(8, 1 / fit);
  const clampZoom = (zoom: number) => Math.max(1, Math.min(maxZoom, zoom));
  const constrain = (v: View): View => {
    const x = Math.max(0, (image.width * fit * v.zoom - area.width) / 2);
    const y = Math.max(0, (image.height * fit * v.zoom - area.height) / 2);
    return { zoom: v.zoom, x: Math.max(-x, Math.min(x, v.x)), y: Math.max(-y, Math.min(y, v.y)) };
  };
  const zoomBy = (factor: number, center: Point = { x: 0, y: 0 }) => setView(v => {
    const zoom = clampZoom(v.zoom * factor), ratio = zoom / v.zoom;
    return constrain({ zoom, x: center.x + (v.x - center.x) * ratio, y: center.y + (v.y - center.y) * ratio });
  });
  useLayoutEffect(() => {
    const el = stage;
    if (!el) return;
    const resize = () => {
      setArea({ width: el.clientWidth, height: el.clientHeight });
      setView(initial);
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(el);
    return () => observer.disconnect();
  }, [stage]);
  // Native listener: React's passive wheel listener cannot prevent page zoom.
  useEffect(() => {
    const el = stage;
    if (!el) return;
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const b = el.getBoundingClientRect();
      zoomBy(Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.03 : 0.002)), { x: e.clientX - b.left - b.width / 2, y: e.clientY - b.top - b.height / 2 });
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [stage, fit, maxZoom, image, area]);
  const gesture = () => {
    const [a, b] = [...pointers.current.values()];
    if (!a) return null;
    return b ? { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, distance: Math.hypot(b.x - a.x, b.y - a.y) } : { ...a, distance: 0 };
  };
  const step = (delta: -1 | 1) => {
    navigation?.step(delta);
  };
  return (
    <Dialog.Root open onOpenChange={open => { if (!open) onClose(); }} modal="trap-focus">
      <Dialog.Portal container={portal}>
        <Dialog.Popup {...modal} finalFocus={() => opener.isConnected ? opener : undefined}
          onKeyDown={e => {
            modal.onKeyDown(e);
            if (e.defaultPrevented || !navigation || view.zoom !== 1 || e.altKey || e.ctrlKey || e.metaKey) return;
            if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
            e.preventDefault();
            e.stopPropagation();
            step(e.key === "ArrowLeft" ? -1 : 1);
          }}
          className="fixed inset-0 z-50 flex flex-col bg-canvas text-ink outline-none">
          <div className="flex shrink-0 items-center gap-3 border-b border-hairline px-3 py-2 pt-[max(8px,env(safe-area-inset-top))]">
            <div className="min-w-0 flex-1">
              <Dialog.Title className="truncate font-semibold">{name}</Dialog.Title>
              <Dialog.Description className="text-sm text-muted">Drag to pan. Pinch or scroll to zoom.</Dialog.Description>
            </div>
            {navigation && <>
              <IconButton label="Previous image" disabled={navigation.index === 0} onClick={() => step(-1)}><IconChevronLeft size={24} /></IconButton>
              <span className="shrink-0 text-sm text-muted tnum" aria-live="polite">{navigation.index + 1} / {navigation.total}</span>
              <IconButton label="Next image" disabled={navigation.index === navigation.total - 1} onClick={() => step(1)}><IconChevronRight size={24} /></IconButton>
            </>}
            <Dialog.Close aria-label="Close" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken"><IconX size={20} /></Dialog.Close>
          </div>
          <div ref={setStage} tabIndex={0} role="region" aria-label="Image: drag to pan, pinch or scroll to zoom"
            className="relative flex min-h-0 flex-1 touch-none items-center justify-center overflow-hidden bg-sunken select-none"
            style={{ cursor: view.zoom > 1 ? "grab" : "default" }}
            onPointerDown={e => {
              if (e.pointerType === "mouse" && e.button !== 0) return;
              e.stopPropagation();
              e.currentTarget.focus();
              e.currentTarget.setPointerCapture(e.pointerId);
              pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
              swipe.current = navigation && view.zoom === 1 && e.pointerType === "touch" && pointers.current.size === 1
                ? { pointerId: e.pointerId, x: e.clientX, y: e.clientY } : null;
            }}
            onPointerMove={e => {
              if (!pointers.current.has(e.pointerId)) return;
              e.stopPropagation();
              const before = gesture()!;
              pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
              const after = gesture()!, b = e.currentTarget.getBoundingClientRect();
              setView(v => {
                const zoom = clampZoom(v.zoom * (before.distance > 0 && after.distance > 0 ? after.distance / before.distance : 1));
                const ratio = zoom / v.zoom;
                const x = before.x - b.left - b.width / 2, y = before.y - b.top - b.height / 2;
                return constrain({ zoom, x: x + (v.x - x) * ratio + after.x - before.x, y: y + (v.y - y) * ratio + after.y - before.y });
              });
            }}
            onLostPointerCapture={e => {
              pointers.current.delete(e.pointerId);
              if (swipe.current?.pointerId === e.pointerId) swipe.current = null;
            }}
            onPointerUp={e => {
              pointers.current.delete(e.pointerId);
              const start = swipe.current;
              if (start?.pointerId !== e.pointerId) return;
              swipe.current = null;
              const dx = e.clientX - start.x, dy = e.clientY - start.y;
              if (view.zoom === 1 && Math.abs(dx) >= 48 && Math.abs(dx) > Math.abs(dy) * 1.5) {
                e.stopPropagation();
                step(dx < 0 ? 1 : -1);
              }
            }}
            onPointerCancel={e => {
              pointers.current.delete(e.pointerId);
              swipe.current = null;
            }}
            onDoubleClick={() => setView(view.zoom === 1 ? { ...initial, zoom: Math.max(2, 1 / fit) } : initial)}
            onKeyDown={e => {
              if (navigation && view.zoom === 1 && (e.key === "ArrowLeft" || e.key === "ArrowRight")) return;
              if (e.key === "+" || e.key === "=") zoomBy(1.25);
              else if (e.key === "-") zoomBy(0.8);
              else if (e.key === "0") setView(initial);
              else if (e.key === "1") setView({ ...initial, zoom: 1 / fit });
              else if (["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key)) setView(v => constrain({ ...v, x: v.x + (e.key === "ArrowLeft" ? 40 : e.key === "ArrowRight" ? -40 : 0), y: v.y + (e.key === "ArrowUp" ? 40 : e.key === "ArrowDown" ? -40 : 0) }));
              else return;
              e.preventDefault();
              e.stopPropagation();
            }}>
            {failed ? <p role="alert">{src ? "Couldn’t display this image. Try Download." : "Couldn’t load this image."}</p>
              : !src ? <p role="status">Loading image…</p> : <img key={imageKey} src={src} alt={name} draggable={false}
              className="block max-w-none shrink-0"
              style={{ width: image.width * fit * view.zoom, height: image.height * fit * view.zoom, transform: `translate(${view.x}px, ${view.y}px)` }}
              onLoad={e => setImage({ width: e.currentTarget.naturalWidth, height: e.currentTarget.naturalHeight })}
              onError={() => setDecodeFailed(true)} />}
          </div>
          <div className="flex shrink-0 flex-wrap items-center justify-center gap-2 border-t border-hairline px-3 py-2 pb-[max(8px,env(safe-area-inset-bottom))]">
            <IconButton label="Zoom out" disabled={view.zoom <= 1 || unavailable} onClick={() => zoomBy(0.8)}><IconMinus size={20} /></IconButton>
            <output aria-label="Zoom level" className="min-w-12 text-center text-sm tnum">{Math.round(fit * view.zoom * 100)}%</output>
            <IconButton label="Zoom in" disabled={view.zoom >= maxZoom || unavailable} onClick={() => zoomBy(1.25)}><IconPlus size={20} /></IconButton>
            <Button disabled={unavailable} onClick={() => setView(initial)}>Fit</Button>
            <Button disabled={unavailable} onClick={() => setView({ ...initial, zoom: 1 / fit })}>100%</Button>
            {openInApp && <Button disabled={openState?.pending} onClick={async () => {
              setOpenState({ pending: true });
              try {
                await openInApp();
                setOpenState({ pending: false, text: "Asked your default image app to open it.", error: false });
              } catch (e) {
                setOpenState({ pending: false, text: errorText(e), error: true });
              }
            }}>{openState?.pending ? "Opening…" : "Open in default app"}</Button>}
            {actions}
            {openState && !openState.pending && <p role={openState.error ? "alert" : "status"} className="w-full text-center text-sm">{openState.text}</p>}
          </div>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
