// A message's files: pictures as thumbnails (fetched only when scrolled into
// view, and only when their bytes are a picture), everything else as a chip
// with name, size and Download. Files come checked and decrypted from
// host.file; a download is a link the browser saves where it saves things,
// so nothing here claims where the file went. HTML and SVG never render.
import { useEffect, useRef, useState } from "react";
import { IconDownload, IconFile, IconFileText, IconFileZip, IconPhoto, IconCloudDownload } from "@tabler/icons-react";
import { errorText, type Api } from "../api";
import { useApp } from "../context";
import { usePortal } from "../owned";
import { niceDevice, size as bytes } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button } from "../ui/Button";
import { isThreadMsg, type AnyMsg } from "./Message.model";
import { SaveToDrive } from "./Drive";

interface FileItem { index: number; name: string; size: number; openable: boolean; availability?: string; note?: string }

const pictureName = /\.(png|jpe?g|gif|webp)$/i;

function sniff(b: Uint8Array): string {
  const at = (i: number, s: string) => [...s].every((c, j) => b[i + j] === c.charCodeAt(0));
  if (b.length >= 8 && b[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (at(0, "GIF87a") || at(0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return "";
}

function filesOf(m: AnyMsg): FileItem[] {
  if (isThreadMsg(m)) return (m.files || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, note: f.note }));
  return (m.attachments || []).map((f, i) => ({ index: f.index ?? i, name: f.name, size: f.size, openable: f.openable, availability: f.availability, note: f.note }));
}

/** Where a file stands for this device: openable here, to ask for, asked, or not here. */
function standing(m: AnyMsg, f: FileItem): "open" | "request" | "requested" | "gone" | "sent" {
  const sentHere = m.dir === "out" && !(!isThreadMsg(m) && (m.via || m.synced_from));
  if (sentHere) return f.openable ? "open" : "sent";
  if (f.availability === "requestable") return "request";
  if (f.availability === "requested") return "requested";
  if (f.availability === "unavailable" || f.openable === false) return "gone";
  return "open";
}

export function MessageFiles({ m }: { m: AnyMsg }) {
  const files = filesOf(m);
  if (!files.length) return null;
  return (
    <div className="mt-1.5 flex flex-col gap-1.5">
      {files.map((f) => <div key={f.index} className="min-w-0 space-y-1">
        {standing(m, f) === "open" && pictureName.test(f.name) ? <Picture m={m} f={f} /> : <FileChip m={m} f={f} />}
        {standing(m, f) === "open" && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
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
    return { url, picture: !!type };
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

function Picture({ m, f }: { m: AnyMsg; f: FileItem }) {
  const store = useApp();
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
  if (failed) return <FileChip m={m} f={f} />;
  // The box keeps its size from the first frame (4:3, the picture cropped to
  // fill it): nothing below it moves when the picture arrives.
  return (
    <>
      <button ref={box} type="button" onClick={() => url && setBig(true)} aria-label={"Open picture " + f.name}
        className="relative block overflow-hidden rounded-xl border border-outline/30 bg-sunken" style={{ width: "min(240px, 100%)", aspectRatio: "4 / 3" }}>
        {url && <img src={url} alt={f.name} onLoad={() => setShown(true)} decoding="async"
          className={"absolute inset-0 block h-full w-full object-cover transition-opacity duration-200 motion-reduce:transition-none " + (shown ? "opacity-100" : "opacity-0")} />}
        {!shown && <span className="absolute inset-0 grid place-items-center text-muted"><IconPhoto size={28} stroke={1.6} className="animate-pulse motion-reduce:animate-none" /></span>}
      </button>
      <Sheet open={big} onOpenChange={setBig} title={f.name} description={bytes(f.size)} wide
        footer={<Button variant="act" size="lg" icon={<IconDownload size={20} />} onClick={() => save(url, f.name, portal)}>Download</Button>}>
        <img src={url} alt={f.name} className="mx-auto max-h-[60dvh] rounded-2xl object-contain" />
      </Sheet>
    </>
  );
}

function FileChip({ m, f }: { m: AnyMsg; f: FileItem }) {
  const store = useApp();
  const portal = usePortal();
  const load = useFileBytes();
  const [busy, setBusy] = useState(false);
  const st = standing(m, f);
  const from = !isThreadMsg(m) && m.synced_from ? "your " + niceDevice(m.synced_from) : "the device it came from";
  const Icon = /\.(zip|tar|gz|tgz|7z|rar)$/i.test(f.name) ? IconFileZip : /\.(txt|md|csv|json|log|pdf|docx?)$/i.test(f.name) ? IconFileText : IconFile;

  const open = async () => {
    setBusy(true);
    try { save((await load(m, f)).url, f.name, portal); }
    catch (e) { store.toast("Couldn’t open " + f.name + ": " + errorText(e), "error"); }
    setBusy(false);
  };
  const ask = async () => {
    setBusy(true);
    await store.run((a) => a.requestFile(m.id, f.index), "Asked " + from + " for it. It opens here once that device sends it.");
    setBusy(false);
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
        <button type="button" onClick={open} disabled={busy} aria-label={"Download " + f.name}
          className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken disabled:opacity-50"><IconDownload size={20} /></button>
      )}
      {st === "request" && (
        <button type="button" onClick={ask} disabled={busy} className="inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-full px-3 text-[13px] font-semibold hover:bg-sunken disabled:opacity-50">
          <IconCloudDownload size={18} />Get it
        </button>
      )}
    </div>
  );
}
