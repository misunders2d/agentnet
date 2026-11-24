// A message's files: pictures as thumbnails (fetched only when scrolled into
// view, and only when their bytes are a picture), everything else as a chip
// with name, size and Download. Files come checked and decrypted from
// host.file; a download is a link the browser saves where it saves things,
// so nothing here claims where the file went. HTML and SVG never render.
import { Fragment, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { IconDownload, IconFile, IconFileText, IconFileZip, IconPhoto, IconCloudDownload, IconMovie, IconPlayerPlayFilled, IconLoader2 } from "@tabler/icons-react";
import { errorText, type Api, type FileProgress } from "../api";
import { useStore } from "../store";
import { useApp } from "../context";
import { usePortal } from "../owned";
import { niceDevice, size as bytes } from "../model";
import { Sheet } from "../ui/Sheet";
import { ImageViewer } from "../ui/ImageViewer";
import { VideoViewer, type VideoPlace } from "../ui/VideoViewer";
import { Button } from "../ui/Button";
import { useLeaving } from "../ui/Motion";
import { openPictureEditor, sniffVideo } from "../pictures.mjs";
import { isThreadMsg, type AnyMsg } from "./Message.model";
import { SaveToDrive } from "./Drive";
import { previewText, textPreviewName, TEXT_PREVIEW_LIMIT } from "./Message.files.text";
import { Markdown } from "./Markdown";
import { interleaveMp4 } from "./video-interleave";
import { capturePoster } from "./video-poster";

interface FileItem { index: number; name: string; size: number; openable: boolean; availability?: string; note?: string; fetch?: string }

const pictureName = /\.(png|jpe?g|gif|webp)$/i;
const videoName = /\.(mp4|m4v|mov|webm|ogv|ogg)$/i;
const INLINE_VIDEO_LIMIT = 16 * 1024 * 1024;
// The Linux app's webview (and GNOME Web): WebKitGTK, not Chromium or Android.
const webkitGTK = typeof navigator !== "undefined" && /\bLinux\b/.test(navigator.userAgent) && /AppleWebKit/.test(navigator.userAgent) && !/Chrome|Chromium|Android/.test(navigator.userAgent);

function sniff(b: Uint8Array): string {
  const at = (i: number, s: string) => [...s].every((c, j) => b[i + j] === c.charCodeAt(0));
  if (b.length >= 8 && b[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (at(0, "GIF87a") || at(0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return "";
}

function filesOf(m: AnyMsg): FileItem[] {
  if (isThreadMsg(m)) return (m.files || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, availability: f.availability, note: f.note, fetch: f.fetch }));
  return (m.attachments || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, availability: f.availability, note: f.note, fetch: f.fetch }));
}

/** Where a file stands for this device: openable here, to ask for, asked, or not here. */
function standing(m: AnyMsg, f: FileItem): "open" | "request" | "requested" | "gone" | "sent" {
  const imported = m.synced_from || (isThreadMsg(m) ? m.history : m.via);
  const sentHere = m.dir === "out" && !imported;
  if (sentHere) return f.openable ? "open" : "sent";
  if (f.availability === "requestable") return "request";
  if (f.availability === "requested") return "requested";
  if (f.availability === "unavailable" || f.openable === false) return "gone";
  return "open";
}

export function MessageFiles({ m }: { m: AnyMsg }) {
  const files = filesOf(m);
  if (!files.length) return null;
  const items = files.map(f => {
    const open = standing(m, f) === "open";
    return { f, open, picture: open && pictureName.test(f.name) };
  });
  const picturesInGroup = items.filter(item => item.picture).length;
  const grouped = picturesInGroup > 1;
  const pictureTrio = picturesInGroup === 3 && items.length === 3;
  const breaks = grouped ? new Set<number>() : undefined;
  if (breaks) {
    // Files keep their place and take a whole row. Split longer picture runs
    // into rows of two or three without leaving an orphan after three.
    let start = 0;
    for (let end = 0; end <= items.length; end++) {
      if (items[end]?.picture) continue;
      while (end - start > 3) {
        start += end - start === 4 ? 2 : 3;
        breaks.add(start);
      }
      start = end + 1;
    }
  }
  return (
    <div data-image-group={grouped ? "" : undefined} className={grouped ? "mt-1.5 flex w-[480px] max-w-full flex-wrap gap-2" : "mt-1.5 flex flex-col gap-1.5"}>
      {items.map(({ f, open, picture }, i) => {
        return <Fragment key={pictureKey(m, f)}>
          {/* Keep picture keys and viewer state on the same level between rows. */}
          {breaks?.has(i) && <div aria-hidden="true" className="-my-1 basis-full" />}
          {picture ? <Picture m={m} f={f} grouped={grouped} firstOfTrio={pictureTrio && i === 0} /> :
            <div className={"min-w-0 space-y-1 " + (grouped ? "basis-full" : "")}>
              {open && videoName.test(f.name) ? <VideoFile m={m} f={f} /> : <FileChip m={m} f={f} />}
              {open && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
            </div>}
        </Fragment>;
      })}
    </div>
  );
}

// useFileBytes fetches one file into an object URL, freed when the message goes.
function useFileBytes() {
  const store = useApp();
  const urls = useRef<string[]>([]);
  useEffect(() => () => { urls.current.forEach((u) => URL.revokeObjectURL(u)); }, []);
  return async (m: AnyMsg, f: FileItem) => {
    const { bytes: b } = await store.api.file(m.id, f.index, m.dir);
    const type = sniff(b);
    const url = URL.createObjectURL(new Blob([b as BlobPart], { type: type || "application/octet-stream" }));
    urls.current.push(url);
    return { url, picture: !!type, bytes: b };
  };
}

// save hands the file to the browser through a link clicked inside the
// skin's own tree (never the page's body).
function save(url: string, name: string, into: HTMLElement) {
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.rel = "noopener";
  a.hidden = true;
  into.append(a);
  a.click();
  a.remove();
}

// Pictures already fetched, by message, file and direction: a conversation
// opened again (or a topic switched back to) shows them at once instead of
// fetching and decrypting them again. The most recent PICTURES_KEPT stay,
// and any picture on screen: an object URL is freed only once no mounted
// Picture uses it. clearPictures frees them all (unmount). warmPictures
// fetches the newest pictures of a conversation about to open.
const PICTURES_KEPT = 48;
type Pic = { url: string; picture: boolean; ratio?: number };
const pictures = new Map<string, Pic>();
const fetchingPics = new Map<string, Promise<Pic>>();
const inUse = new Map<string, number>(); // mounted Pictures per key
const pictureKey = (m: AnyMsg, f: FileItem) => m.id + "\n" + f.index + "\n" + m.dir;

export function clearPictures() {
  for (const p of pictures.values()) URL.revokeObjectURL(p.url);
  pictures.clear();
  fetchingPics.clear();
  for (const p of posters.values()) if (p.url) URL.revokeObjectURL(p.url);
  posters.clear();
  fetchingPosters.clear();
}

function keep<T extends { url: string }>(map: Map<string, T>, key: string, p: T) {
  map.set(key, p);
  for (const [k, old] of map) {
    if (map.size <= PICTURES_KEPT) break;
    if (inUse.get(k)) continue; // on screen: kept until it is not
    map.delete(k);
    if (old.url) URL.revokeObjectURL(old.url);
  }
}
const rememberPicture = (key: string, p: Pic) => keep(pictures, key, p);

// Video posters (video-poster.ts), kept like pictures and under the same key:
// a still made here from the file once it is here, or url "" when a capture
// gave none (Play stays). Captures run one at a time, only for cards on
// screen, only for files Play could open (at most INLINE_VIDEO_LIMIT).
type Poster = { url: string; width: number; height: number };
const noPoster: Poster = { url: "", width: 0, height: 0 };
const posters = new Map<string, Poster>();
const fetchingPosters = new Map<string, Promise<Poster | null>>();
let posterTurn: Promise<unknown> = Promise.resolve();

function fetchPoster(api: Api, m: AnyMsg, f: FileItem): Promise<Poster | null> {
  const key = pictureKey(m, f);
  const have = posters.get(key);
  if (have) return Promise.resolve(have);
  let p = fetchingPosters.get(key);
  if (p) return p;
  const job = posterTurn.then(async (): Promise<Poster | null> => {
    if (!inUse.get(key)) return null; // left before its turn: asked again when shown
    let b = (await api.file(m.id, f.index, m.dir)).bytes;
    let type = b.byteLength === f.size && b.byteLength <= INLINE_VIDEO_LIMIT ? sniffVideo(b) : "";
    if (!type) return noPoster;
    // The same bytes Play gives WebKitGTK (video-interleave.ts).
    if (webkitGTK && (type === "video/mp4" || type === "video/quicktime")) {
      try { b = interleaveMp4(b); type = "video/mp4"; } catch { /* the bytes as sent */ }
    }
    const blob = new Blob([b as BlobPart], { type });
    b = new Uint8Array(0);
    const still = await capturePoster(blob);
    return still ? { url: URL.createObjectURL(still.blob), width: still.width, height: still.height } : noPoster;
  }).catch(() => noPoster);
  posterTurn = job;
  p = job.then((r) => {
    if (!r) return null;
    if (fetchingPosters.get(key) !== p) { if (r.url) URL.revokeObjectURL(r.url); return null; } // cleared meanwhile
    keep(posters, key, r);
    return r;
  }).finally(() => { if (fetchingPosters.get(key) === p) fetchingPosters.delete(key); });
  fetchingPosters.set(key, p);
  return p;
}

function fetchPicture(api: Api, m: AnyMsg, f: FileItem): Promise<Pic> {
  const key = pictureKey(m, f);
  const have = pictures.get(key);
  if (have) return Promise.resolve(have);
  let p = fetchingPics.get(key);
  if (!p) {
    p = api.file(m.id, f.index, m.dir).then(({ bytes: b }) => {
      const type = sniff(b);
      const pic = { url: URL.createObjectURL(new Blob([b as BlobPart], { type: type || "application/octet-stream" })), picture: !!type };
      if (fetchingPics.get(key) === p) rememberPicture(key, pic);
      return pic;
    }).finally(() => fetchingPics.delete(key));
    fetchingPics.set(key, p);
  }
  return p;
}

/** warmPictures fetches the newest few pictures of a conversation (the ones
 *  its first screen shows), so they are there when it opens. */
export function warmPictures(api: Api, messages: AnyMsg[] | null | undefined, n = 4) {
  const want: [AnyMsg, FileItem][] = [];
  for (const m of [...(messages || [])].reverse()) {
    for (const f of filesOf(m)) if (want.length < n && standing(m, f) === "open" && pictureName.test(f.name)) want.push([m, f]);
    if (want.length >= n) break;
  }
  for (const [m, f] of want) fetchPicture(api, m, f).catch(() => {});
}

function Picture({ m, f, grouped = false, firstOfTrio = false }: { m: AnyMsg; f: FileItem; grouped?: boolean; firstOfTrio?: boolean }) {
  const store = useApp();
  const hasPerson = useStore(store, s => s.overview?.person?.state === "self");
  const portal = usePortal();
  const box = useRef<HTMLButtonElement>(null);
  const key = pictureKey(m, f);
  const known = pictures.get(key);
  const [url, setUrl] = useState(known?.picture ? known.url : "");
  const [shown, setShown] = useState(!!url); // a picture seen before shows at once; a new one fades in
  const [ratio, setRatio] = useState(known?.ratio ?? 1);
  const [failed, setFailed] = useState(!!known && !known.picture);
  const [big, setBig] = useState(false);
  useEffect(() => {
    inUse.set(key, (inUse.get(key) || 0) + 1);
    return () => { const n = (inUse.get(key) || 1) - 1; if (n) inUse.set(key, n); else inUse.delete(key); };
  }, [key]);
  useEffect(() => {
    const el = box.current;
    if (!el || url || failed) return;
    let alive = true;
    const io = new IntersectionObserver((seen) => {
      if (!seen.some((s) => s.isIntersecting)) return;
      io.disconnect();
      fetchPicture(store.api, m, f).then((p) => {
        if (!alive) return;
        if (p.picture) setUrl(p.url); else setFailed(true);
      }).catch(() => { if (alive) setFailed(true); });
    }, { rootMargin: "200px" });
    io.observe(el);
    return () => { alive = false; io.disconnect(); };
  }, [key]);
  if (failed) return <div className={"min-w-0 space-y-1 " + (grouped ? "basis-full" : "")}>
    <FileChip m={m} f={f} wrapName={grouped} />
    {!isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
  </div>;
  // A wide lead in a three-picture group gets a row above the remaining two.
  const wideLead = firstOfTrio && ratio > 1.2 && ratio <= 2;
  // Keep a placeholder while loading; decoded images retain their whole
  // aspect ratio, bounded by the conversation and viewport.
  return (
    <div className="min-w-0 space-y-1" style={grouped ? { flex: wideLead ? "0 0 100%" : `${ratio} 1 0px`, minWidth: 44, maxWidth: wideLead ? "100%" : ratio * 200 } : undefined}>
      <button ref={box} type="button" onClick={() => url && setBig(true)} aria-label={"Open picture " + f.name} title={"Open full image: " + f.name}
        className={"relative flex max-w-full items-center justify-center overflow-hidden rounded-xl border border-outline/30 bg-sunken " + (grouped ? "min-h-11 w-full" : "")}
        style={grouped ? { aspectRatio: ratio, maxHeight: wideLead ? 240 : 200 } : url ? { width: "fit-content" } : { width: "min(360px, 100%)", aspectRatio: "4 / 3" }}>
        {url && <img src={url} alt={f.name} onLoad={({ currentTarget: image }) => {
          // The existing lazy image supplies dimensions. Keep them when a
          // single preview joins a group after another file becomes openable.
          // Bound extreme shapes while keeping the whole preview visible.
          const next = Math.min(3, Math.max(.4, image.naturalWidth / image.naturalHeight));
          const cached = pictures.get(key);
          if (cached?.url === url) cached.ratio = next;
          setRatio(next);
          setShown(true);
        }} onError={() => setFailed(true)} loading="lazy" decoding="async"
          className={"block h-auto w-auto max-w-full object-contain transition-opacity duration-200 motion-reduce:transition-none " + (grouped ? "max-h-full" : "max-h-[min(60dvh,480px)]") + " " + (shown ? "opacity-100" : "opacity-0")} />}
        {!shown && <span className="absolute inset-0 grid place-items-center text-muted"><IconPhoto size={28} stroke={1.6} className="animate-pulse motion-reduce:animate-none" /></span>}
      </button>
      {grouped && <span className="block truncate text-[12px] text-muted" title={f.name}>{f.name}</span>}
      {!grouped && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
      {big && <ImageViewer src={url} name={f.name} onClose={() => setBig(false)}
        openInApp={store.api.host.platform === "daemon" ? () => store.api.openImage(m.id, f.index, m.dir) : undefined}
        actions={<><Button icon={<IconDownload size={20} />} onClick={() => save(url, f.name, portal)}>Download</Button>
          {hasPerson && <Button onClick={async () => { if (await openPictureEditor({ into: portal, src: url, save: png => store.api.setPersonPicture(png) })) await store.refetch(); }}>Use as my picture</Button>}
          {grouped && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}</>} />}
    </div>
  );
}

// Big received files wait for a tap, as on Telegram (FileView.fetch: videos
// over 10 MiB, other files over 3 MiB, cleared copies). The download runs in
// this device's AgentNet whatever the page does; the page follows it one
// step at a time (each progress answer comes once it moved) and shares one
// state per file between the chip and the video around it.
type Download = { phase: "wait" | "fetching" | "failed" | "gone" | "here"; have: number; total: number; error?: string };
const downloads = new Map<string, Download>();
const downloadSubs = new Map<string, Set<() => void>>();
const following = new Set<string>();

function setDownload(key: string, d: Download) {
  downloads.set(key, d);
  downloadSubs.get(key)?.forEach((fn) => fn());
}

async function follow(api: Api, key: string, m: AnyMsg, f: FileItem, p: FileProgress) {
  if (following.has(key)) return;
  following.add(key);
  try {
    for (;;) {
      if (p.done) return setDownload(key, { phase: "here", have: p.total, total: p.total });
      if (p.gone) return setDownload(key, { phase: "gone", have: 0, total: p.total, error: p.error });
      if (p.error) return setDownload(key, { phase: "failed", have: p.have, total: p.total, error: p.error });
      if (!p.fetching) return setDownload(key, { phase: "wait", have: p.have, total: p.total }); // nothing runs now (a restart): offer it again
      setDownload(key, { phase: "fetching", have: p.have, total: p.total });
      p = await api.fileProgress(m.id, f.index, m.dir, p.have);
    }
  } catch (e) {
    setDownload(key, { phase: "failed", have: downloads.get(key)?.have || 0, total: downloads.get(key)?.total || f.size, error: errorText(e) });
  } finally { following.delete(key); }
}

// useDownload is where a waiting file stands here, and start() downloads it:
// the progress shows at once, before the request leaves.
function useDownload(m: AnyMsg, f: FileItem) {
  const store = useApp();
  const key = pictureKey(m, f);
  const subscribe = useCallback((fn: () => void) => {
    let subs = downloadSubs.get(key);
    if (!subs) downloadSubs.set(key, subs = new Set());
    subs.add(fn);
    return () => { subs!.delete(fn); if (!subs!.size) downloadSubs.delete(key); };
  }, [key]);
  const known = useSyncExternalStore(subscribe, () => downloads.get(key));
  const waiting = f.fetch === "wait" || f.fetch === "fetching";
  useEffect(() => { // a download started earlier, maybe elsewhere: follow it again
    if (f.fetch === "fetching" && !known && !following.has(key))
      void store.api.fileProgress(m.id, f.index, m.dir, -1).then((p) => follow(store.api, key, m, f, p)).catch(() => {});
  }, [key, f.fetch]);
  const d: Download = !waiting ? { phase: "here", have: f.size, total: f.size } : known || { phase: f.fetch === "fetching" ? "fetching" : "wait", have: 0, total: f.size };
  const start = () => {
    if (d.phase === "fetching" && following.has(key)) return;
    setDownload(key, { phase: "fetching", have: d.phase === "failed" ? d.have : 0, total: d.total || f.size });
    store.api.fetchFile(m.id, f.index, m.dir).then((p) => follow(store.api, key, m, f, p))
      .catch((e) => setDownload(key, { phase: "failed", have: 0, total: f.size, error: errorText(e) }));
  };
  return { ...d, start };
}

function DownloadState({ f, d }: { f: FileItem; d: ReturnType<typeof useDownload> }) {
  const pct = d.total > 0 ? Math.min(100, Math.floor((d.have / d.total) * 100)) : 0;
  if (d.phase === "fetching") return (
    <div className="w-full space-y-1" data-download="fetching">
      <div role="progressbar" aria-label={"Downloading " + f.name} aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}
        className="h-1.5 w-full overflow-hidden rounded-full bg-sunken">
        <div className="h-full rounded-full bg-accent transition-[width] duration-200 motion-reduce:transition-none" style={{ width: pct + "%" }} />
      </div>
      <p className="text-[12px] text-muted tnum">Downloading {pct}% · {bytes(Math.round(f.size * pct / 100))} of {bytes(f.size)}</p>
    </div>);
  if (d.phase === "failed") return (
    <div className="flex flex-wrap items-center gap-2" data-download="failed">
      <p role="status" className="text-[13px] text-text-2">Couldn’t download: {d.error}</p>
      <Button className="min-h-11" aria-label={"Retry download " + f.name} onClick={d.start}>Retry</Button>
    </div>);
  if (d.phase === "gone") return <p role="status" className="text-[13px] text-text-2" data-download="gone">This file is no longer on the relay.</p>;
  return (
    <Button className="min-h-11" icon={<IconDownload size={18} />} aria-label={"Download " + f.name + ", " + bytes(f.size)} data-download="wait" onClick={d.start}>
      Download · {bytes(f.size)}
    </Button>);
}

// Each open owns one Blob. Closing or leaving invalidates a pending read;
// host.file has no AbortSignal, so its late bytes are discarded, not cached.
function VideoFile({ m, f }: { m: AnyMsg; f: FileItem }) {
  const store = useApp();
  const leaving = useLeaving();
  const leavingRef = useRef(leaving);
  leavingRef.current = leaving;
  const media = useRef<HTMLVideoElement>(null);
  // Keep the element through React's ref detach so unmount cleanup can pause
  // and unload it before revoking the Blob, even after removal from the DOM.
  const bindMedia = useCallback((node: HTMLVideoElement | null) => { if (node) media.current = node; }, []);
  const owned = useRef("");
  const generation = useRef(0);
  const mounted = useRef(true);
  const pending = useRef(false);
  const startPaused = useRef(false);
  const [url, setURL] = useState("");
  const [phase, setPhase] = useState<"idle" | "loading" | "ready" | "failed">("idle");
  const [playAfterDownload, setPlayAfterDownload] = useState(false);
  const [note, setNote] = useState("");
  const [expanded, setExpanded] = useState<VideoPlace | null>(null);
  const download = useDownload(m, f);
  const inline = Number.isSafeInteger(f.size) && f.size > 0 && f.size <= INLINE_VIDEO_LIMIT;
  const key = pictureKey(m, f);
  const here = download.phase === "here";
  const opening = phase === "loading" || playAfterDownload;
  const frame = useRef<HTMLElement | null>(null);
  const [poster, setPoster] = useState<Poster | null>(() => posters.get(key) || null);
  useEffect(() => {
    inUse.set(key, (inUse.get(key) || 0) + 1);
    return () => { const n = (inUse.get(key) || 1) - 1; if (n) inUse.set(key, n); else inUse.delete(key); };
  }, [key]);
  // The poster is asked for once the card is near the screen and its file is
  // here; nothing else waits for it.
  useEffect(() => {
    const el = frame.current;
    if (!el || !here || !inline || poster || opening) return;
    let alive = true;
    const io = new IntersectionObserver((seen) => {
      if (!seen.some((s) => s.isIntersecting)) return;
      io.disconnect();
      void fetchPoster(store.api, m, f).then((p) => { if (alive && p) setPoster(p); });
    }, { rootMargin: "200px" });
    io.observe(el);
    return () => { alive = false; io.disconnect(); };
  }, [key, here, inline, !!poster, !!url, opening]);

  function release(reset = true) {
    generation.current++;
    pending.current = false;
    const video = media.current;
    if (video) { video.pause(); video.removeAttribute("src"); video.load(); }
    if (owned.current) { URL.revokeObjectURL(owned.current); owned.current = ""; }
    if (mounted.current) { setExpanded(null); setPlayAfterDownload(false); }
    if (reset && mounted.current) { setURL(""); setPhase("idle"); setNote(""); }
  }
  // Where native fullscreen is off (the Linux app), the window-size view.
  function expand() {
    const v = media.current;
    if (!v || !owned.current) return;
    const playing = !v.paused && !v.ended;
    v.pause();
    setExpanded({ time: v.currentTime, playing });
  }
  function collapse(to: VideoPlace) {
    setExpanded(null);
    const v = media.current;
    if (!v || !owned.current) return;
    v.currentTime = to.time;
    if (to.playing) void v.play().catch(() => {});
  }
  function failed(message: string) {
    release(false);
    if (mounted.current) { setURL(""); setPhase("failed"); setNote(message); }
  }
  useEffect(() => {
    // Keep the inert outgoing node mounted, but unload it immediately.
    // A cancelled transition can return this same card: offer Play again.
    if (leaving) release(false);
    else if (url && !owned.current) release();
  }, [leaving]);
  useEffect(() => {
    mounted.current = true;
    const hidden = () => { if (document.hidden) release(); };
    const leave = () => release();
    document.addEventListener("visibilitychange", hidden);
    window.addEventListener("pagehide", leave);
    const off = store.api.host.android?.onVisibility(visible => { if (!visible) release(); });
    return () => {
      mounted.current = false;
      document.removeEventListener("visibilitychange", hidden);
      window.removeEventListener("pagehide", leave);
      off?.(); release(false);
    };
  }, [store]);
  useEffect(() => {
    if (!url || !media.current || leavingRef.current || startPaused.current) return;
    const visit = generation.current;
    // This follows an explicit Play click; async decrypt may consume its
    // transient activation. Keep controls for a second native Play tap.
    void media.current.play().catch((e: unknown) => {
      if (!mounted.current || generation.current !== visit) return;
      if (e instanceof DOMException && e.name === "NotAllowedError") setNote("Tap Play in the video controls to start.");
      else failed("This video can’t be played here. Download to play it.");
    });
  }, [url]);
  async function open(paused = false) {
    if (!inline || leavingRef.current || pending.current || owned.current || document.hidden || store.api.host.android?.visible() === false) return;
    const visit = ++generation.current;
    pending.current = true; setPhase("loading"); setNote("Loading video…");
    try {
      let b = (await store.api.file(m.id, f.index, m.dir)).bytes;
      if (!mounted.current || leavingRef.current || generation.current !== visit || !store.isActive()) return;
      if (b.byteLength > INLINE_VIDEO_LIMIT || b.byteLength !== f.size) throw Error("Video size differs from its attachment details. Download to play it.");
      let type = sniffVideo(b);
      if (!type) throw Error("This video can’t be played here. Download to play it.");
      // WebKitGTK's player reads the wrong bytes in files whose sound and
      // picture lie far apart (video-interleave.ts): play the same packets in
      // time order. If that fails, the file as sent.
      if (webkitGTK && (type === "video/mp4" || type === "video/quicktime")) {
        try { b = interleaveMp4(b); type = "video/mp4"; } catch { /* the bytes as sent */ }
      }
      const blob = new Blob([b as BlobPart], { type });
      b = new Uint8Array(0);
      owned.current = URL.createObjectURL(blob);
      startPaused.current = paused;
      setURL(owned.current); setPhase("ready"); setNote(paused ? "Tap Play in the video controls to start." : "");
    } catch (e) {
      if (mounted.current && !leavingRef.current && generation.current === visit) failed(errorText(e));
    } finally { if (generation.current === visit) pending.current = false; }
  }
  // A Play tap on Android can request the same download as the file chip.
  // Keep the intent on this card only. A completed download can outlive it.
  const deferred = store.api.host.platform === "android" && inline && !here && download.phase !== "gone";
  function play() {
    if (here) { void open(); return; }
    if (!deferred || playAfterDownload || leavingRef.current || document.hidden || store.api.host.android?.visible() === false) return;
    setPlayAfterDownload(true);
    if (download.phase !== "fetching") download.start();
  }
  useEffect(() => {
    if (!playAfterDownload) return;
    if (here) { setPlayAfterDownload(false); void open(true); }
    else if (download.phase !== "fetching") setPlayAfterDownload(false);
  }, [playAfterDownload, download.phase]);

  // The card's frame: the poster (or a plain dark frame while it is made, or
  // when none could be), with Play over it when this device can open the
  // video, as Telegram draws Play/Download in the middle of its thumbnail
  // (5ff61571 history_view_gif.cpp 794–871).
  const still = poster?.url || "";
  const ratio = still && poster ? poster.width / poster.height : 16 / 9;
  const frameStyle = { aspectRatio: still && poster ? poster.width + " / " + poster.height : "16 / 9", width: still ? "min(100%, calc(min(60dvh, 480px) * " + ratio.toFixed(4) + "))" : "min(360px, 100%)" };
  const frameClass = "relative block max-w-full overflow-hidden rounded-xl border border-outline/30 bg-black text-white";
  const playable = (here && inline) || deferred;
  const preparing = here && inline && !poster;
  const face = <>
    {still && <img src={still} alt="" draggable={false} decoding="async" className="absolute inset-0 size-full object-contain" />}
    {preparing && <span className="absolute inset-0 animate-pulse bg-white/10 motion-reduce:animate-none" />}
    {!playable && <span className="absolute inset-0 grid place-items-center text-white/70"><IconMovie size={32} stroke={1.5} /></span>}
  </>;
  const posterState = still ? "ready" : preparing ? "loading" : here && inline ? "none" : "waiting";
  return (
    <div role="group" aria-label={"Video " + f.name} className="w-full min-w-0 max-w-[480px] space-y-1.5">
      {url ? null : playable
        ? <button ref={(el) => { frame.current = el; }} type="button" aria-label={"Play video " + f.name} title={"Play " + f.name} aria-busy={opening || undefined}
            data-poster={posterState} onClick={play} className={frameClass + " group"} style={frameStyle}>
            {face}
            <span className="absolute inset-0 grid place-items-center">
              <span className="grid size-14 place-items-center rounded-full bg-black/60 shadow-lg transition-transform duration-150 group-hover:scale-105 motion-reduce:transition-none">
                {opening ? <IconLoader2 size={26} className="animate-spin motion-reduce:animate-none" /> : <IconPlayerPlayFilled size={26} className="translate-x-px" />}
              </span>
            </span>
          </button>
        : <div ref={(el) => { frame.current = el; }} aria-hidden="true" data-poster={posterState} className={frameClass} style={frameStyle}>{face}</div>}
      {/* preload="auto": the bytes are in memory; "none" froze a few seconds in on the Linux app (October 29, 2026). */}
      {url && <video ref={bindMedia} src={url} controls playsInline preload="auto" aria-label={f.name}
        className="block max-h-[min(60dvh,480px)] w-full rounded-xl bg-black object-contain"
        onError={() => { if (owned.current) failed("This video can’t be played here. Download to play it."); }} />}
      <FileChip m={m} f={f} />
      {inline ? (opening || phase === "ready") && <div className="flex flex-wrap items-center gap-2">
        <Button className="min-h-11" aria-label={"Close video " + f.name} onClick={() => release()}>Close video</Button>
        {phase === "ready" && !document.fullscreenEnabled && <Button className="min-h-11" aria-label={"Expand video " + f.name} onClick={expand}>Expand</Button>}
      </div> : here && <p className="text-[12px] text-muted">{f.size > INLINE_VIDEO_LIMIT ? "Videos up to 16 MiB can play here." : "This video’s size is unknown."} Download to play this file.</p>}
      {note && <p role="status" className="text-[13px] text-text-2">{note}</p>}
      {url && expanded && <VideoViewer src={url} name={f.name} from={expanded} onClose={collapse} />}
    </div>
  );
}

function FileChip({ m, f, wrapName = false }: { m: AnyMsg; f: FileItem; wrapName?: boolean }) {
  const store = useApp();
  const hasPerson = useStore(store, s => s.overview?.person?.state === "self");
  const portal = usePortal();
  const load = useFileBytes();
  const [busy, setBusy] = useState(false);
  const [pictureURL, setPictureURL] = useState("");
  const [preview, setPreview] = useState<{ text: string; url: string } | null>(null);
  const st = standing(m, f);
  const download = useDownload(m, f);
  const waits = st === "open" && download.phase !== "here";
  const from = m.synced_from ? "your " + niceDevice(m.synced_from) : "the device it came from";
  const Icon = /\.(zip|tar|gz|tgz|7z|rar)$/i.test(f.name) ? IconFileZip : /\.(txt|md|csv|json|log|pdf|docx?)$/i.test(f.name) ? IconFileText : IconFile;

  const open = async () => {
    setBusy(true);
    try { const opened = await load(m, f); if (opened.picture) setPictureURL(opened.url); save(opened.url, f.name, portal); }
    catch (e) { store.toast("Couldn’t open " + f.name + ": " + errorText(e), "error"); }
    setBusy(false);
  };
  const ask = async () => {
    setBusy(true);
    await store.run((a) => a.requestFile(m.id, f.index), "Asked " + from + " for it. It opens here once that device sends it.");
    setBusy(false);
  };
  const showText = async () => {
    setBusy(true);
    try {
      const opened = await load(m, f);
      const text = previewText(f.name, opened.bytes);
      if (store.isActive()) setPreview({ text, url: opened.url });
    } catch (e) { if (store.isActive()) store.toast("Couldn’t preview " + f.name + ": " + errorText(e), "error"); }
    finally { if (store.isActive()) setBusy(false); }
  };

  const row = (
    <div className="flex min-w-0 items-center gap-2.5 rounded-xl border border-outline/25 bg-surface/80 py-1.5 pl-2.5 pr-1 text-ink">
      <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-sunken text-text-2" aria-hidden="true"><Icon size={20} stroke={1.7} /></span>
      <span className="min-w-0 flex-1">
        <span className={"block text-[14px] font-semibold " + (wrapName ? "[overflow-wrap:anywhere]" : "truncate")} title={f.name}>{f.name}</span>
        <span className="block text-[12px] text-muted tnum">
          {bytes(f.size)}
          {st === "requested" && " · asked " + from + " for it"}
          {st === "gone" && " · not available here"}
          {st === "sent" && " · sent, not kept here"}
          {f.note && " · " + f.note}
        </span>
      </span>
      {st === "open" && !waits && (
        <>
        {textPreviewName(f.name) && f.size <= TEXT_PREVIEW_LIMIT && <Button disabled={busy} aria-label={"Preview " + f.name} onClick={() => void showText()}>Preview</Button>}
        <button type="button" onClick={open} disabled={busy} aria-label={"Download " + f.name}
          className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken disabled:opacity-50"><IconDownload size={20} /></button>
        </>
      )}
      <Sheet open={preview !== null} onOpenChange={(open) => { if (!open) setPreview(null); }} title={f.name} description={bytes(f.size)} wide
        footer={<Button variant="act" disabled={!preview} onClick={() => preview && save(preview.url, f.name, portal)}>Download</Button>}>
        {/\.md$/i.test(f.name)
          ? <Markdown spoilers="literal" text={preview?.text || ""} className="text-[14px] leading-relaxed" />
          : <pre className="whitespace-pre-wrap break-words text-[14px] [overflow-wrap:anywhere]">{preview?.text}</pre>}
      </Sheet>
      {hasPerson && pictureURL && <Button onClick={async () => { if (await openPictureEditor({ into: portal, src: pictureURL, save: png => store.api.setPersonPicture(png) })) await store.refetch(); }}>Use as my picture</Button>}
      {st === "request" && (
        <button type="button" onClick={ask} disabled={busy} className="inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-full px-3 text-[13px] font-semibold hover:bg-sunken disabled:opacity-50">
          <IconCloudDownload size={18} />Get it
        </button>
      )}
    </div>
  );
  return waits ? <div className="min-w-0 space-y-1.5">{row}<DownloadState f={f} d={download} /></div> : row;
}
