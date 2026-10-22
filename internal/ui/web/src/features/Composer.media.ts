// Sender-only File -> File preparation. The result enters the ordinary stage,
// encryption and retry path; the original File is never modified.
import type { Host } from "../host";

export const mediaKind = (file: File): "photo" | "video" | null =>
  /^image\/(jpeg|png|webp|gif|heic|heif|avif)$/.test(file.type) ? "photo"
    : /^video\//.test(file.type) ? "video" : null;
export interface PreparedMedia { file: File; note: string }
export const aborted = (signal: AbortSignal) => signal.throwIfAborted();

const abortable = <T>(promise: Promise<T>, signal: AbortSignal): Promise<T> => new Promise((resolve, reject) => {
  const stop = () => { signal.removeEventListener("abort", stop); reject(signal.reason); };
  if (signal.aborted) { stop(); return; }
  signal.addEventListener("abort", stop, { once: true });
  promise.then(v => { signal.removeEventListener("abort", stop); resolve(v); }, e => { signal.removeEventListener("abort", stop); reject(e); });
});

// A single queue serves all mounted composers. It bounds codec/bitmap work
// across files and chats; cancelled queued entries never start a decoder.
let queue: Promise<unknown> = Promise.resolve();
export function prepareMedia(file: File, host: Host, signal: AbortSignal, progress: (n: number) => void): Promise<PreparedMedia> {
  const work = queue.then(async () => {
    aborted(signal);
    return mediaKind(file) === "photo" ? preparePhoto(file, signal) : prepareVideo(file, host, signal, progress);
  });
  queue = work.catch(() => {});
  return work;
}

// Header checks happen before decoding. Animated/unknown images keep their
// original bytes; a corrupt header cannot request an unbounded bitmap.
export function photoDimensions(b: Uint8Array): [number, number] | null {
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const word = (at: number, text: string) => text.split("").every((c, i) => b[at + i] === c.charCodeAt(0));
  if (b.length >= 24 && b[0] === 137 && word(1, "PNG\r\n\x1a\n")) {
    for (let p = 8; p + 8 <= b.length;) {
      const n = v.getUint32(p);
      if (word(p + 4, "acTL")) return null;
      if (word(p + 4, "IDAT")) return [v.getUint32(16), v.getUint32(20)];
      if (n > b.length - p - 12) break;
      p += n + 12;
    }
    return null;
  }
  if (b.length >= 4 && b[0] === 255 && b[1] === 216) {
    for (let p = 2; p + 4 <= b.length;) {
      if (b[p++] !== 255) return null;
      while (b[p] === 255) p++;
      const marker = b[p++];
      if (marker === 217 || marker === 218) return null;
      if (p + 2 > b.length) return null;
      const n = v.getUint16(p);
      if (n < 2 || p + n > b.length) return null;
      if ([192, 193, 194].includes(marker) && n >= 7) return [v.getUint16(p + 5), v.getUint16(p + 3)];
      p += n;
    }
  }
  if (b.length >= 30 && word(0, "RIFF") && word(8, "WEBP")) {
    if (word(12, "VP8X")) {
      if (b[20] & 2) return null; // animation flag
      const u24 = (p: number) => b[p] | b[p + 1] << 8 | b[p + 2] << 16;
      return [u24(24) + 1, u24(27) + 1];
    }
    if (word(12, "VP8 ") && b[23] === 157 && b[24] === 1 && b[25] === 42) return [v.getUint16(26, true) & 16383, v.getUint16(28, true) & 16383];
  }
  return null;
}

export async function preparePhoto(file: File, signal: AbortSignal): Promise<PreparedMedia> {
  aborted(signal);
  if (file.size > 40 * 1024 * 1024) return { file, note: "This photo exceeds the 40 MiB preparation limit. Using the original file." };
  const dimensions = photoDimensions(new Uint8Array(await file.slice(0, 256 * 1024).arrayBuffer()));
  if (!dimensions || dimensions[0] <= 0 || dimensions[1] <= 0 || dimensions[0] * dimensions[1] > 24_000_000) {
    return { file, note: "Animated, unsupported or oversized photo. Using the original file." };
  }
  aborted(signal);
  const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  try {
    aborted(signal);
    const scale = Math.min(1, 2560 / Math.max(bitmap.width, bitmap.height));
    const width = Math.max(1, Math.round(bitmap.width * scale)), height = Math.max(1, Math.round(bitmap.height * scale));
    let blob: Blob | null;
    if (typeof OffscreenCanvas !== "undefined") {
      const canvas = new OffscreenCanvas(width, height), c = canvas.getContext("2d");
      if (!c) throw Error("Photo preparation is unavailable.");
      c.fillStyle = "white"; c.fillRect(0, 0, width, height); c.drawImage(bitmap, 0, 0, width, height);
      blob = await canvas.convertToBlob({ type: "image/jpeg", quality: .82 });
      canvas.width = canvas.height = 1;
    } else {
      const canvas = document.createElement("canvas"); canvas.width = width; canvas.height = height;
      const c = canvas.getContext("2d"); if (!c) throw Error("Photo preparation is unavailable.");
      c.fillStyle = "white"; c.fillRect(0, 0, width, height); c.drawImage(bitmap, 0, 0, width, height);
      blob = await new Promise<Blob | null>(r => canvas.toBlob(r, "image/jpeg", .82));
      canvas.width = canvas.height = 1;
    }
    aborted(signal);
    if (!blob || blob.type !== "image/jpeg") throw Error("This photo could not be prepared.");
    if (blob.size >= file.size * .9) return { file, note: "Less than 10% smaller. Using the original file." };
    return { file: new File([blob], file.name.replace(/\.[^.]*$/, "") + ".jpg", { type: "image/jpeg", lastModified: file.lastModified }), note: `${width} × ${height} JPEG · quality 82% · location metadata removed` };
  } finally { bitmap.close(); }
}

export async function prepareVideo(file: File, host: Host, signal: AbortSignal, progress: (n: number) => void): Promise<PreparedMedia> {
  aborted(signal);
  if (file.size > 1024 ** 3) return { file, note: "This video exceeds the 1 GiB preparation limit. Using the original file." };
  const combined = new AbortController(), cancel = () => combined.abort(signal.reason);
  signal.addEventListener("abort", cancel, { once: true });
  const deadline = setTimeout(() => combined.abort(new DOMException("Video preparation reached its two-minute limit.", "TimeoutError")), 120_000);
  let result: File;
  try {
    try {
      // This import is its own chunk. Reading ordinary chats loads no codecs.
      const { compressVideo } = await abortable(import("./Composer.video"), combined.signal);
      aborted(combined.signal);
      result = await compressVideo(file, combined.signal, progress);
    } catch (e) {
      aborted(combined.signal);
      if (!host.prepareVideo) throw e;
      result = await host.prepareVideo(file, combined.signal);
    }
  } finally {
    clearTimeout(deadline); signal.removeEventListener("abort", cancel);
  }
  aborted(signal);
  if (result.size >= file.size * .8) return { file, note: "Less than 20% smaller. Using the original file." };
  return { file: result, note: (result.type === "video/webm" ? "WebM · VP9 + Opus" : "MP4 · H.264 + AAC") + " · up to 1280 px / 30 fps · up to 2.5 Mbps video (lower for long clips) / 128 kbps audio" };
}
