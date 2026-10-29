// A message's files: pictures as thumbnails (fetched only when scrolled into
// view, and only when their bytes are a picture), everything else as a chip
// with name, size and Download. Files come checked and decrypted from
// host.file; a download is a link the browser saves where it saves things,
// so nothing here claims where the file went. HTML and SVG never render.
import { useCallback, useEffect, useRef, useState } from "react";
import { IconDownload, IconFile, IconFileText, IconFileZip, IconPhoto, IconCloudDownload } from "@tabler/icons-react";
import { errorText, type Api } from "../api";
import { useStore } from "../store";
import { useApp } from "../context";
import { usePortal } from "../owned";
import { niceDevice, size as bytes } from "../model";
import { Sheet } from "../ui/Sheet";
import { ImageViewer } from "../ui/ImageViewer";
import { Button } from "../ui/Button";
import { useLeaving } from "../ui/Motion";
import { openPictureEditor, sniffVideo } from "../pictures.mjs";
import { isThreadMsg, type AnyMsg } from "./Message.model";
import { SaveToDrive } from "./Drive";
import { previewText, textPreviewName, TEXT_PREVIEW_LIMIT } from "./Message.files.text";
import { Markdown } from "./Markdown";

interface FileItem { index: number; name: string; size: number; openable: boolean; availability?: string; note?: string }

const pictureName = /\.(png|jpe?g|gif|webp)$/i;
const videoName = /\.(mp4|m4v|mov|webm|ogv|ogg)$/i;
const INLINE_VIDEO_LIMIT = 16 * 1024 * 1024;

function sniff(b: Uint8Array): string {
  const at = (i: number, s: string) => [...s].every((c, j) => b[i + j] === c.charCodeAt(0));
  if (b.length >= 8 && b[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (at(0, "GIF87a") || at(0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return "";
}

function filesOf(m: AnyMsg): FileItem[] {
  if (isThreadMsg(m)) return (m.files || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, availability: f.availability, note: f.note }));
  return (m.attachments || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, availability: f.availability, note: f.note }));
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
  const isPicture = (f: FileItem) => standing(m, f) === "open" && pictureName.test(f.name);
  // A shared preview row keeps tall screenshots compact. Each original keeps
  // its own viewer, filename and actions, in the attachment's existing order.
  const grouped = files.filter(isPicture).length > 1;
  return (
    <div data-image-group={grouped ? "" : undefined} className={grouped ? "mt-1.5 grid w-[480px] max-w-full grid-cols-2 gap-2" : "mt-1.5 flex flex-col gap-1.5"}>
      {files.map((f) => <div key={pictureKey(m, f)} className={"min-w-0 space-y-1 " + (grouped && !isPicture(f) ? "col-span-2" : "")}>
        {isPicture(f) ? <Picture m={m} f={f} grouped={grouped} />
          : standing(m, f) === "open" && videoName.test(f.name) ? <VideoFile m={m} f={f} /> : <FileChip m={m} f={f} />}
        {standing(m, f) === "open" && !isThreadMsg(m) && !(grouped && isPicture(f)) && <SaveToDrive m={m} index={f.index} />}
      </div>)}
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
type Pic = { url: string; picture: boolean };
const pictures = new Map<string, Pic>();
const fetchingPics = new Map<string, Promise<Pic>>();
const inUse = new Map<string, number>(); // mounted Pictures per key
const pictureKey = (m: AnyMsg, f: FileItem) => m.id + "\n" + f.index + "\n" + m.dir;

export function clearPictures() {
  for (const p of pictures.values()) URL.revokeObjectURL(p.url);
  pictures.clear();
  fetchingPics.clear();
}

function rememberPicture(key: string, p: Pic) {
  pictures.set(key, p);
  for (const [k, old] of pictures) {
    if (pictures.size <= PICTURES_KEPT) break;
    if (inUse.get(k)) continue; // on screen: kept until it is not
    pictures.delete(k);
    URL.revokeObjectURL(old.url);
  }
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

function Picture({ m, f, grouped = false }: { m: AnyMsg; f: FileItem; grouped?: boolean }) {
  const store = useApp();
  const hasPerson = useStore(store, s => s.overview?.person?.state === "self");
  const portal = usePortal();
  const box = useRef<HTMLButtonElement>(null);
  const key = pictureKey(m, f);
  const known = pictures.get(key);
  const [url, setUrl] = useState(known?.picture ? known.url : "");
  const [shown, setShown] = useState(!!url); // a picture seen before shows at once; a new one fades in
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
  if (failed) return <><FileChip m={m} f={f} />
    {grouped && <span className="block truncate text-[12px] text-muted" title={f.name}>{f.name}</span>}
    {grouped && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}</>;
  // Keep a placeholder while loading; decoded images retain their whole
  // aspect ratio, bounded by the conversation and viewport.
  return (
    <>
      <button ref={box} type="button" onClick={() => url && setBig(true)} aria-label={"Open picture " + f.name} title={"Open full image: " + f.name}
        className={"relative flex max-w-full items-center justify-center overflow-hidden rounded-xl border border-outline/30 bg-sunken " + (grouped ? "aspect-square w-full" : "")}
        style={grouped ? undefined : url ? { width: "fit-content" } : { width: "min(360px, 100%)", aspectRatio: "4 / 3" }}>
        {url && <img src={url} alt={f.name} onLoad={() => setShown(true)} onError={() => setFailed(true)} loading="lazy" decoding="async"
          className={"block h-auto w-auto max-w-full object-contain transition-opacity duration-200 motion-reduce:transition-none " + (grouped ? "max-h-full" : "max-h-[min(60dvh,480px)]") + " " + (shown ? "opacity-100" : "opacity-0")} />}
        {!shown && <span className="absolute inset-0 grid place-items-center text-muted"><IconPhoto size={28} stroke={1.6} className="animate-pulse motion-reduce:animate-none" /></span>}
      </button>
      {grouped && <span className="block truncate text-[12px] text-muted" title={f.name}>{f.name}</span>}
      {big && <ImageViewer src={url} name={f.name} onClose={() => setBig(false)}
        openInApp={store.api.host.platform === "daemon" ? () => store.api.openImage(m.id, f.index, m.dir) : undefined}
        actions={<><Button icon={<IconDownload size={20} />} onClick={() => save(url, f.name, portal)}>Download</Button>
          {hasPerson && <Button onClick={async () => { if (await openPictureEditor({ into: portal, src: url, save: png => store.api.setPersonPicture(png) })) await store.refetch(); }}>Use as my picture</Button>}
          {grouped && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}</>} />}
    </>
  );
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
  const [url, setURL] = useState("");
  const [phase, setPhase] = useState<"idle" | "loading" | "ready" | "failed">("idle");
  const [note, setNote] = useState("");
  const inline = Number.isSafeInteger(f.size) && f.size > 0 && f.size <= INLINE_VIDEO_LIMIT;

  function release(reset = true) {
    generation.current++;
    pending.current = false;
    const video = media.current;
    if (video) { video.pause(); video.removeAttribute("src"); video.load(); }
    if (owned.current) { URL.revokeObjectURL(owned.current); owned.current = ""; }
    if (reset && mounted.current) { setURL(""); setPhase("idle"); setNote(""); }
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
    if (!url || !media.current || leavingRef.current) return;
    const visit = generation.current;
    // This follows an explicit Play click; async decrypt may consume its
    // transient activation. Keep controls for a second native Play tap.
    void media.current.play().catch((e: unknown) => {
      if (!mounted.current || generation.current !== visit) return;
      if (e instanceof DOMException && e.name === "NotAllowedError") setNote("Tap Play in the video controls to start.");
      else failed("This video can’t be played here. Download to play it.");
    });
  }, [url]);
  async function open() {
    if (!inline || leavingRef.current || pending.current || owned.current || document.hidden || store.api.host.android?.visible() === false) return;
    const visit = ++generation.current;
    pending.current = true; setPhase("loading"); setNote("Loading video…");
    try {
      let b = (await store.api.file(m.id, f.index, m.dir)).bytes;
      if (!mounted.current || leavingRef.current || generation.current !== visit || !store.isActive()) return;
      if (b.byteLength > INLINE_VIDEO_LIMIT || b.byteLength !== f.size) throw Error("Video size differs from its attachment details. Download to play it.");
      const type = sniffVideo(b);
      if (!type) throw Error("This video can’t be played here. Download to play it.");
      const blob = new Blob([b as BlobPart], { type });
      b = new Uint8Array(0);
      owned.current = URL.createObjectURL(blob);
      setURL(owned.current); setPhase("ready"); setNote("");
    } catch (e) {
      if (mounted.current && !leavingRef.current && generation.current === visit) failed(errorText(e));
    } finally { if (generation.current === visit) pending.current = false; }
  }
  return (
    <div role="group" aria-label={"Video " + f.name} className="w-full min-w-0 max-w-[480px] space-y-1.5">
      <FileChip m={m} f={f} />
      {inline ? <div className="flex flex-wrap items-center gap-2">
        {phase === "idle" && <Button className="min-h-11" aria-label={"Play video " + f.name} onClick={() => void open()}>Play</Button>}
        {(phase === "loading" || phase === "ready") && <Button className="min-h-11" aria-label={"Close video " + f.name} onClick={() => release()}>Close video</Button>}
      </div> : <p className="text-[12px] text-muted">{f.size > INLINE_VIDEO_LIMIT ? "Videos up to 16 MiB can play here." : "This video’s size is unknown."} Download to play this file.</p>}
      {note && <p role="status" className="text-[13px] text-text-2">{note}</p>}
      {/* preload="auto": the bytes are in memory; "none" froze a few seconds in on the Linux app (October 29, 2026). */}
      {url && <video ref={bindMedia} src={url} controls playsInline preload="auto" aria-label={f.name}
        className="block max-h-[min(60dvh,480px)] w-full rounded-xl bg-black object-contain"
        onError={() => { if (owned.current) failed("This video can’t be played here. Download to play it."); }} />}
    </div>
  );
}

function FileChip({ m, f }: { m: AnyMsg; f: FileItem }) {
  const store = useApp();
  const hasPerson = useStore(store, s => s.overview?.person?.state === "self");
  const portal = usePortal();
  const load = useFileBytes();
  const [busy, setBusy] = useState(false);
  const [pictureURL, setPictureURL] = useState("");
  const [preview, setPreview] = useState<{ text: string; url: string } | null>(null);
  const st = standing(m, f);
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

  return (
    <div className="flex min-w-0 items-center gap-2.5 rounded-xl border border-outline/25 bg-surface/80 py-1.5 pl-2.5 pr-1 text-ink">
      <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-sunken text-text-2" aria-hidden="true"><Icon size={20} stroke={1.7} /></span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[14px] font-semibold" title={f.name}>{f.name}</span>
        <span className="block text-[12px] text-muted tnum">
          {bytes(f.size)}
          {st === "requested" && " · asked " + from + " for it"}
          {st === "gone" && " · not available here"}
          {st === "sent" && " · sent, not kept here"}
          {f.note && " · " + f.note}
        </span>
      </span>
      {st === "open" && (
        <>
        {textPreviewName(f.name) && f.size <= TEXT_PREVIEW_LIMIT && <Button disabled={busy} aria-label={"Preview " + f.name} onClick={() => void showText()}>Preview</Button>}
        <button type="button" onClick={open} disabled={busy} aria-label={"Download " + f.name}
          className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken disabled:opacity-50"><IconDownload size={20} /></button>
        </>
      )}
      <Sheet open={preview !== null} onOpenChange={(open) => { if (!open) setPreview(null); }} title={f.name} description={bytes(f.size)} wide
        footer={<Button variant="act" disabled={!preview} onClick={() => preview && save(preview.url, f.name, portal)}>Download</Button>}>
        {/\.md$/i.test(f.name)
          ? <Markdown text={preview?.text || ""} className="text-[14px] leading-relaxed" />
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
}
