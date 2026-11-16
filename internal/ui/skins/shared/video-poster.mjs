// One bounded, muted first-frame decode at a time. No playback, seeking or
// fullscreen: particularly important for WebKitGTK's fragile video surface.
let tail = Promise.resolve();
export function firstVideoFrame(blob, signal) {
  const run = tail.catch(() => {}).then(() => new Promise((resolve, reject) => {
    if (signal.aborted) { reject(Error("Cancelled")); return; }
    const video = document.createElement("video");
    const url = URL.createObjectURL(blob);
    let done = false;
    const finish = (image) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", cancel);
      video.onloadeddata = video.onerror = null;
      video.pause(); video.removeAttribute("src"); video.load();
      URL.revokeObjectURL(url);
      if (image) resolve(image); else reject(Error("Preview unavailable"));
    };
    const cancel = () => finish();
    const timer = setTimeout(cancel, 5000);
    signal.addEventListener("abort", cancel, { once: true });
    video.muted = true; video.playsInline = true; video.preload = "auto";
    video.onerror = cancel;
    video.onloadeddata = () => {
      try {
        if (!video.videoWidth || !video.videoHeight) return finish();
        const scale = Math.min(1, 360 / video.videoWidth, 240 / video.videoHeight);
        const canvas = document.createElement("canvas");
        canvas.width = Math.max(1, Math.round(video.videoWidth * scale));
        canvas.height = Math.max(1, Math.round(video.videoHeight * scale));
        canvas.getContext("2d").drawImage(video, 0, 0, canvas.width, canvas.height);
        finish(canvas.toDataURL("image/jpeg", .75));
      } catch { finish(); }
    };
    video.src = url;
  }));
  tail = run;
  return run;
}
