// Files in a draft: chosen, pasted or dropped; shown before sending (pictures
// as thumbnails, other files as chips with their size) and removable. They
// are handed to this computer's AgentNet only when the message is sent.
import { useEffect, useState } from "react";
import { IconFileUpload, IconX } from "@tabler/icons-react";
import type { T } from "../api";
import type { StagedFile } from "../store";
import { size } from "../model";

const pictures = ["image/png", "image/jpeg", "image/gif", "image/webp"];

/** bytes: a size in words, without a needless ".0" ("100 MB"). */
export const bytes = (n: number) => size(n).replace(".0 ", " ");
let seq = 0;

const stamp = () => new Date().toISOString().replace(/[-:]/g, "").replace("T", "-").slice(0, 15);

/** draftFiles turns picked, pasted or dropped files into draft files; a
 *  pasted picture gets a name of its own, and folders or empty files are skipped. */
export function draftFiles(list: File[], pasted = false): StagedFile[] {
  return list.filter((f) => f.size > 0).map((f) => {
    const name = pasted && (!f.name || /^image\.(png|jpe?g|gif|webp)$/i.test(f.name))
      ? "pasted-image-" + stamp() + "." + (f.type.split("/")[1] || "png").replace("jpeg", "jpg") : f.name || "file";
    return { key: "f" + ++seq, name, size: f.size, type: f.type, file: f, url: pictures.includes(f.type) ? URL.createObjectURL(f) : undefined };
  });
}

export const releaseFiles = (files: StagedFile[]) => { for (const f of files) if (f.url) URL.revokeObjectURL(f.url); };

/** overLimit says why these files cannot go in one message ("" when they can). */
export function overLimit(files: Pick<StagedFile, "name" | "size">[], lim: T.FileLimits | undefined): string {
  if (!files.length) return "";
  if (!lim) return "Files can’t be sent from here.";
  if (files.length > lim.max_count) return "One message takes at most " + lim.max_count + " files. Remove " + (files.length - lim.max_count) + " to send.";
  const big = files.find((f) => f.size > lim.max_file);
  if (big) return big.name + " is larger than " + bytes(lim.max_file) + ", the most one file can be.";
  if (lim.max_message && files.reduce((n, f) => n + f.size, 0) > lim.max_message) {
    return "Together these files are more than " + bytes(lim.max_message) + ". Send fewer at a time.";
  }
  return "";
}

const ext = (name: string) => ((name.includes(".") ? name.split(".").pop() : "") || "file").slice(0, 4).toUpperCase();

export function FilesTray({ files, lim, onRemove, busy }: { files: StagedFile[]; lim?: T.FileLimits; onRemove: (key: string) => void; busy: boolean }) {
  if (!files.length) return null;
  return (
    <ul aria-label="Files to send" className="-mx-1 mb-2 flex gap-3 overflow-x-auto px-1 pt-3 pb-1 [scrollbar-width:thin]">
      {files.map((f) => {
        const tooBig = !!lim && f.size > lim.max_file;
        const remove = (
          <button type="button" disabled={busy} onClick={() => onRemove(f.key)} aria-label={"Remove " + f.name}
            className="absolute -top-4 -right-4 grid size-11 place-items-center rounded-full disabled:opacity-40">
            <span className="grid size-6 place-items-center rounded-full bg-ink text-canvas stroke shadow-pop-sm"><IconX size={14} stroke={3} /></span>
          </button>
        );
        return f.url ? (
          <li key={f.key} className="relative shrink-0 pop-in" title={f.name + " · " + bytes(f.size)}>
            <img src={f.url} alt={f.name} className={"size-16 rounded-xl bg-white object-cover stroke " + (tooBig ? "outline-2 outline-danger" : "")} />
            {remove}
          </li>
        ) : (
          <li key={f.key} className={"relative flex h-16 max-w-56 shrink-0 items-center gap-2.5 rounded-xl bg-canvas pl-2 pr-4 stroke pop-in " + (tooBig ? "outline-2 outline-danger" : "")}>
            <span aria-hidden="true" className="grid size-10 shrink-0 place-items-center rounded-lg bg-agent text-[11px] font-bold tracking-wide text-agent-ink">{ext(f.name)}</span>
            <span className="min-w-0">
              <span className="block truncate text-sm font-semibold">{f.name}</span>
              <span className={"block text-[13px] tnum " + (tooBig ? "font-semibold text-danger" : "text-muted")}>{bytes(f.size)}{tooBig ? " · too big" : ""}</span>
            </span>
            {remove}
          </li>
        );
      })}
    </ul>
  );
}

/** useFileDrop accepts files dropped anywhere on zone (the conversation the
 *  composer sits in) and reports where to draw the drop target while a drag
 *  carrying files is over it. */
export function useFileDrop(zone: HTMLElement | null, enabled: boolean, onFiles: (files: File[]) => void): DOMRect | null {
  const [rect, setRect] = useState<DOMRect | null>(null);
  useEffect(() => {
    if (!zone || !enabled) { setRect(null); return; }
    let depth = 0;
    const carries = (e: DragEvent) => !!e.dataTransfer && [...e.dataTransfer.types].includes("Files");
    const enter = (e: DragEvent) => { if (!carries(e)) return; e.preventDefault(); depth++; setRect(zone.getBoundingClientRect()); };
    const over = (e: DragEvent) => { if (!carries(e)) return; e.preventDefault(); if (e.dataTransfer) e.dataTransfer.dropEffect = "copy"; };
    const leave = (e: DragEvent) => { if (!carries(e)) return; depth = Math.max(0, depth - 1); if (!depth) setRect(null); };
    const drop = (e: DragEvent) => {
      if (!carries(e)) return;
      e.preventDefault();
      depth = 0;
      setRect(null);
      const files = [...(e.dataTransfer?.files || [])];
      if (files.length) onFiles(files);
    };
    zone.addEventListener("dragenter", enter);
    zone.addEventListener("dragover", over);
    zone.addEventListener("dragleave", leave);
    zone.addEventListener("drop", drop);
    return () => {
      zone.removeEventListener("dragenter", enter);
      zone.removeEventListener("dragover", over);
      zone.removeEventListener("dragleave", leave);
      zone.removeEventListener("drop", drop);
    };
  }, [zone, enabled, onFiles]);
  return rect;
}

export function DropTarget({ rect, lim }: { rect: DOMRect; lim?: T.FileLimits }) {
  return (
    <div aria-hidden="true" className="pointer-events-none fixed z-30 grid place-items-center p-4 fade-in"
      style={{ left: rect.left, top: rect.top, width: rect.width, height: rect.height }}>
      <div className="grid size-full place-items-center rounded-3xl border-2 border-dashed border-outline bg-canvas/90 dots">
        <div className="flex flex-col items-center gap-3 text-center">
          <span className="grid size-16 place-items-center rounded-2xl bg-act text-act-ink stroke shadow-pop"><IconFileUpload size={30} stroke={2} /></span>
          <span className="font-display text-[22px] font-extrabold">Drop to attach</span>
          <span className="text-[13px] text-text-2">They go with your next message{lim ? " · up to " + lim.max_count + " files, " + bytes(lim.max_file) + " each" : ""}.</span>
        </div>
      </div>
    </div>
  );
}
