// A still from a video this device holds, for its card in the chat, as
// Telegram Desktop makes a video's "good thumbnail" from the downloaded file
// (5ff61571 data/data_document_media.cpp GenerateGoodThumbnail, 445–500:
// off the main path, once, failures remembered). The same bytes Play reads,
// in a muted element that is never played and never in the page: after its
// first frame is drawn, the element is unloaded and its URL revoked, so only
// a small JPEG stays. A capture that takes longer than POSTER_TIMEOUT, or any
// error, gives no still; the card then keeps its plain frame with Play.
export const POSTER_TIMEOUT = 4000;
const POSTER_EDGE = 720;

export interface PosterStill { blob: Blob; width: number; height: number }

export function capturePoster(video: Blob, timeout = POSTER_TIMEOUT): Promise<PosterStill | null> {
  return new Promise((resolve) => {
    let done = false;
    const url = URL.createObjectURL(video);
    const v = document.createElement("video");
    const finish = (still: PosterStill | null) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      v.onloadeddata = v.onseeked = v.onerror = null;
      try { v.pause(); } catch { /* never played */ }
      v.removeAttribute("src");
      try { v.load(); } catch { /* already unloaded */ }
      URL.revokeObjectURL(url);
      resolve(still);
    };
    const timer = setTimeout(() => finish(null), timeout);
    const draw = () => {
      if (done) return;
      try {
        const w = v.videoWidth, h = v.videoHeight;
        if (!w || !h) return finish(null); // sound only, or no picture decoded
        const k = Math.min(1, POSTER_EDGE / Math.max(w, h));
        const canvas = document.createElement("canvas");
        canvas.width = Math.max(1, Math.round(w * k));
        canvas.height = Math.max(1, Math.round(h * k));
        const ctx = canvas.getContext("2d");
        if (!ctx) return finish(null);
        ctx.drawImage(v, 0, 0, canvas.width, canvas.height);
        canvas.toBlob((blob) => finish(blob && blob.size ? { blob, width: w, height: h } : null), "image/jpeg", 0.85);
      } catch { finish(null); }
    };
    v.muted = true;
    v.defaultMuted = true;
    v.playsInline = true;
    v.preload = "auto";
    v.onerror = () => finish(null);
    // A frame a little in: the very first is often black. Short clips use 0.
    v.onloadeddata = () => {
      if (done) return;
      const at = Number.isFinite(v.duration) && v.duration > 0.5 ? 0.1 : 0;
      if (at === 0) return draw();
      v.onseeked = draw;
      try { v.currentTime = at; } catch { draw(); }
    };
    v.src = url;
    try { v.load(); } catch { finish(null); }
  });
}
