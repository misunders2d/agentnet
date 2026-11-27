// A message's files: pictures as thumbnails (fetched only when scrolled into
// view, and only when their bytes are a picture), everything else as a chip
// with name, size and Download. Files come checked and decrypted from
// host.file; a download is a link the browser saves where it saves things,
// so nothing here claims where the file went. HTML and SVG never render.
import { Fragment, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { IconDownload, IconFile, IconFileText, IconFileZip, IconPhoto, IconCloudDownload, IconMovie, IconPlayerPlayFilled, IconLoader2, IconMusic } from "@tabler/icons-react";
import { errorText, type Api, type FileProgress } from "../api";
import { useStore } from "../store";
import { useApp } from "../context";
import { usePortal } from "../owned";
import { niceDevice, size as bytes } from "../model";
import { Sheet } from "../ui/Sheet";
import { ImageViewer, type ImageNavigation } from "../ui/ImageViewer";
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
import { ALBUM_WIDTH, groupRanges, layoutMediaGroup, type GroupKind } from "./Message.groups";

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

/** hasFiles: the message shows attachments (its text then goes under them, as Telegram's caption). */
export const hasFiles = (m: AnyMsg) => filesOf(m).length > 0;

const audioName = /\.(mp3|m4a|aac|flac|wav|opus|oga|weba)$/i;
// Pictures whose bytes turned out not to be a picture (or failed to decode):
// they leave the album and show as files, as before.
const notPictures = new Set<string>();

// kindOf is Telegram's album type for one attachment (attach_prepare.cpp
// AlbumType): photos, videos and GIFs share the mosaic; audio and other
// files each form a column. A file not here (to ask for, gone, sent from
// elsewhere) is a file row. A message still sending keeps its pictures and
// videos in the album, so the album shows at once and stays when it is sent.
function kindOf(m: AnyMsg, f: FileItem): GroupKind {
  const st = standing(m, f);
  const shown = st === "open" || (!!m._local && st === "sent");
  if (shown && videoName.test(f.name)) return "media";
  if (shown && pictureName.test(f.name) && !notPictures.has(pictureKey(m, f))) return "media";
  if (audioName.test(f.name)) return "music";
  return "file";
}

type Viewer = { index: number; opener: HTMLButtonElement };

export function MessageFiles({ m, className = "mt-1.5" }: { m: AnyMsg; className?: string }) {
  const [viewer, setViewer] = useState<Viewer | null>(null);
  const [, rerender] = useState(0);
  // Each tile's width:height once known (a picture's decoded size, a
  // video's poster or 16:9). The album lays out once all its pictures are known.
  const [ratios, setRatios] = useState(() => new Map<string, number>());
  const files = filesOf(m);
  const kinds = files.map(f => kindOf(m, f));
  const groups = groupRanges(kinds).map(g => ({ ...g, files: files.slice(g.from, g.till) }));
  const album = viewer ? groups.find(g => g.album && g.kind === "media" && g.files.some(f => f.index === viewer.index)) : undefined;
  const single = viewer && !album ? groups.find(g => !g.album && g.kind === "media" && g.files[0].index === viewer.index && pictureName.test(g.files[0].name)) : undefined;
  const shown = album?.files || single?.files || [];
  const selected = viewer ? shown.findIndex(f => f.index === viewer.index) : -1;
  // A removed or no longer openable attachment closes its viewer.
  if (viewer && selected < 0) setViewer(null);
  if (!files.length) return null;
  const known = (key: string, ratio: number) => setRatios(current => current.get(key) === ratio ? current : new Map(current).set(key, ratio));
  const failed = (key: string) => { notPictures.add(key); rerender(n => n + 1); };
  return (
    <div className={"flex min-w-0 flex-col gap-1.5 " + className}>
      {groups.map(g => {
        const key = pictureKey(m, g.files[0]);
        if (g.album && g.kind === "media") return <MediaAlbum key={"album\n" + key} m={m} files={g.files} ratios={ratios} onKnown={known} onFailed={failed}
          onOpen={(f, opener) => setViewer({ index: f.index, opener })} />;
        if (g.album) return <FileColumn key={"column\n" + key} m={m} files={g.files} music={g.kind === "music"} />;
        const f = g.files[0];
        const open = standing(m, f) === "open";
        return <Fragment key={key}>
          {g.kind === "media" && pictureName.test(f.name) && open ? <Picture m={m} f={f} onFailed={() => failed(key)} onOpen={opener => setViewer({ index: f.index, opener })} /> :
            <div className="min-w-0 space-y-1">
              {g.kind === "media" && open && videoName.test(f.name) ? <VideoFile m={m} f={f} /> : <FileChip m={m} f={f} music={g.kind === "music"} />}
              {open && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
            </div>}
        </Fragment>;
      })}
      {viewer && selected >= 0 && <MediaGallery m={m} f={shown[selected]} inAlbum={!!album}
        opener={viewer.opener} onClose={() => setViewer(null)}
        navigation={album ? {
          index: selected, total: shown.length,
          step: delta => setViewer(current => {
            if (!current) return current;
            const position = shown.findIndex(item => item.index === current.index);
            const next = position < 0 ? undefined : shown[position + delta];
            return next ? { ...current, index: next.index } : current;
          }),
        } : undefined} />}
    </div>
  );
}

// MediaAlbum is Telegram's Mode::Grid: LayoutMediaGroup at 430 px, scaled
// with its spacing to the bubble (history_view_media_grouped.cpp
// countCurrentSize), so positions are percentages of the block. Only the
// block's outer corners are rounded. Until every picture's size is known the
// tiles are squares (Telegram Android's default ratio); the album then lays
// out once.
function MediaAlbum({ m, files, ratios, onKnown, onFailed, onOpen }: {
  m: AnyMsg; files: FileItem[]; ratios: Map<string, number>;
  onKnown: (key: string, ratio: number) => void; onFailed: (key: string) => void;
  onOpen: (f: FileItem, opener: HTMLButtonElement) => void;
}) {
  const keys = files.map(f => pictureKey(m, f));
  const ratioOf = (f: FileItem, key: string) => ratios.get(key) ?? (videoName.test(f.name) ? undefined : pictures.get(key)?.ratio);
  const ready = files.every((f, i) => videoName.test(f.name) || ratioOf(f, keys[i]) !== undefined);
  const sizes = files.map((f, i) => {
    const ratio = ready ? ratioOf(f, keys[i]) ?? 16 / 9 : 1;
    return { width: Math.round(ratio * 1000), height: 1000 };
  });
  const tiles = layoutMediaGroup(sizes);
  const height = Math.max(...tiles.map(t => t.y + t.height));
  const pct = (v: number, of: number) => (v / of * 100).toFixed(4) + "%";
  return (
    <div data-image-group="" data-album="media" data-album-ready={ready ? "" : undefined} role="group" aria-label={"Album, " + files.length + " items"}
      className="relative w-[430px] max-w-full overflow-hidden rounded-xl bg-sunken" style={{ aspectRatio: ALBUM_WIDTH + " / " + height }}>
      {files.map((f, i) => {
        const t = tiles[i];
        return <div key={keys[i]} data-tile={i} className="absolute overflow-hidden"
          style={{ left: pct(t.x, ALBUM_WIDTH), top: pct(t.y, height), width: pct(t.width, ALBUM_WIDTH), height: pct(t.height, height) }}>
          {videoName.test(f.name)
            ? <VideoTile m={m} f={f} onKnown={ratio => onKnown(keys[i], ratio)} onOpen={opener => onOpen(f, opener)} />
            : <PictureTile m={m} f={f} onKnown={ratio => onKnown(keys[i], ratio)} onFailed={() => onFailed(keys[i])} onOpen={opener => onOpen(f, opener)} />}
        </div>;
      })}
    </div>
  );
}

function useInUse(key: string) {
  useEffect(() => {
    inUse.set(key, (inUse.get(key) || 0) + 1);
    return () => { const n = (inUse.get(key) || 1) - 1; if (n) inUse.set(key, n); else inUse.delete(key); };
  }, [key]);
}

// The album's pictures load together: they are fetched once the block is
// near the screen, as single pictures are.
function useNearScreen<T extends HTMLElement>(enabled: boolean, deps: unknown[], onNear: () => void) {
  const ref = useRef<T>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el || !enabled) return;
    const io = new IntersectionObserver((seen) => {
      if (!seen.some((s) => s.isIntersecting)) return;
      io.disconnect();
      onNear();
    }, { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, deps);
  return ref;
}

function PictureTile({ m, f, onKnown, onFailed, onOpen }: {
  m: AnyMsg; f: FileItem; onKnown: (ratio: number) => void; onFailed: () => void; onOpen: (opener: HTMLButtonElement) => void;
}) {
  const store = useApp();
  const key = pictureKey(m, f);
  const known = pictures.get(key);
  const [url, setUrl] = useState(known?.picture ? known.url : "");
  const [shown, setShown] = useState(!!url && known?.ratio !== undefined);
  const local = !!m._local;
  useInUse(key);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);
  const box = useNearScreen<HTMLButtonElement>(!url && !local, [key, local], () => {
    fetchPicture(store.api, m, f).then((p) => {
      if (!alive.current) return;
      if (p.picture) setUrl(p.url); else onFailed();
    }).catch(() => { if (alive.current) onFailed(); });
  });
  useEffect(() => { if (known?.ratio !== undefined) onKnown(known.ratio); }, [key]);
  return (
    <button ref={box} type="button" onClick={e => { if (url) onOpen(e.currentTarget); }} aria-label={"Open picture " + f.name} title={f.name}
      aria-busy={local || undefined} className="relative block size-full overflow-hidden bg-sunken">
      {url && <img src={url} alt={f.name} onLoad={({ currentTarget: image }) => {
        const ratio = image.naturalWidth / image.naturalHeight;
        const cached = pictures.get(key);
        if (cached?.url === url) cached.ratio = ratio;
        onKnown(ratio);
        setShown(true);
      }} onError={onFailed} decoding="async"
        className={"absolute inset-0 size-full object-cover transition-opacity duration-200 motion-reduce:transition-none " + (shown ? "opacity-100" : "opacity-0")} />}
      {!shown && <span className="absolute inset-0 grid place-items-center text-muted"><IconPhoto size={28} stroke={1.6} className="animate-pulse motion-reduce:animate-none" /></span>}
    </button>
  );
}

// A video in the album: its poster (made here, as on its own card) with
// Telegram's play badge; a tap opens the viewer, which plays it.
function VideoTile({ m, f, onKnown, onOpen }: { m: AnyMsg; f: FileItem; onKnown: (ratio: number) => void; onOpen: (opener: HTMLButtonElement) => void }) {
  const store = useApp();
  const key = pictureKey(m, f);
  const download = useDownload(m, f);
  const here = download.phase === "here" && !m._local;
  const inline = Number.isSafeInteger(f.size) && f.size > 0 && f.size <= INLINE_VIDEO_LIMIT;
  const [poster, setPoster] = useState<Poster | null>(() => posters.get(key) || null);
  useInUse(key);
  useEffect(() => { onKnown(poster?.url ? poster.width / poster.height : 16 / 9); }, [key]);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);
  const box = useNearScreen<HTMLButtonElement>(here && inline && !poster, [key, here, inline, !!poster], () => {
    void fetchPoster(store.api, m, f).then((p) => { if (alive.current && p) setPoster(p); });
  });
  const still = poster?.url || "";
  return (
    <button ref={box} type="button" onClick={e => onOpen(e.currentTarget)} aria-label={"Open video " + f.name} title={f.name}
      aria-busy={m._local || undefined} data-poster={still ? "ready" : here && inline && !poster ? "loading" : here && inline ? "none" : "waiting"}
      className="group relative block size-full overflow-hidden bg-black text-white">
      {still && <img src={still} alt="" draggable={false} decoding="async" className="absolute inset-0 size-full object-cover" />}
      <span className="absolute inset-0 grid place-items-center">
        <span className="grid size-11 place-items-center rounded-full bg-black/60 shadow-lg transition-transform duration-150 group-hover:scale-105 motion-reduce:transition-none">
          {here && inline ? <IconPlayerPlayFilled size={22} className="translate-x-px" /> : download.phase !== "here" && !m._local ? <IconDownload size={20} /> : <IconMovie size={22} stroke={1.6} />}
        </span>
      </span>
      {(!here || !inline) && <span className="absolute bottom-1.5 left-1.5 rounded-full bg-black/60 px-2 py-0.5 text-[11px] tnum">{bytes(f.size)}</span>}
    </button>
  );
}

// FileColumn is Telegram's Mode::Column: documents (or audio) as rows of
// one block, each row keeping its own name, size and actions.
function FileColumn({ m, files, music }: { m: AnyMsg; files: FileItem[]; music: boolean }) {
  return (
    <div data-album={music ? "music" : "file"} role="group" aria-label={(music ? "Audio, " : "Files, ") + files.length + " items"}
      className="min-w-0 divide-y divide-outline/20 overflow-hidden rounded-xl border border-outline/25 bg-surface/80">
      {files.map(f => {
        const open = standing(m, f) === "open";
        return <div key={pictureKey(m, f)} className="min-w-0 space-y-1 py-0.5">
          <FileChip m={m} f={f} bare music={music} />
          {open && !isThreadMsg(m) && <div className="px-2.5 pb-1"><SaveToDrive m={m} index={f.index} /></div>}
        </div>;
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

// MediaGallery is the existing viewer for one picture, or for an album:
// Previous/Next, arrows and swipe step through the album's photos and
// videos in order. A video plays in the viewer with its own card (Play,
// download states and limits as on its own).
function MediaGallery({ m, f, inAlbum, opener, onClose, navigation }: {
  m: AnyMsg; f: FileItem; inAlbum: boolean; opener: HTMLButtonElement; onClose: () => void;
  navigation?: ImageNavigation;
}) {
  const store = useApp(), portal = usePortal();
  const hasPerson = useStore(store, state => state.overview?.person?.state === "self");
  const key = pictureKey(m, f);
  const video = videoName.test(f.name);
  const [loaded, setLoaded] = useState<{ key: string; picture: Pic | null } | null>(null);
  const cached = pictures.get(key);
  const result = loaded?.key === key ? loaded.picture : cached;
  useEffect(() => {
    if (video || pictures.has(key)) return;
    let current = true;
    fetchPicture(store.api, m, f).then(
      picture => { if (current) setLoaded({ key, picture }); },
      () => { if (current) setLoaded({ key, picture: null }); },
    );
    return () => { current = false; };
  }, [key, store]);

  const drive = inAlbum && !isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />;
  if (video) return <ImageViewer name={f.name} imageKey={key} loadFailed={false} onClose={onClose} opener={opener} navigation={navigation}
    content={<VideoFile key={key} m={m} f={f} inViewer />} actions={drive} />;
  const src = result?.picture ? result.url : undefined;
  return <ImageViewer src={src} name={f.name} imageKey={key} loadFailed={result !== undefined && !src}
    onClose={onClose} opener={opener} navigation={navigation}
    openInApp={src && store.api.host.platform === "daemon" ? () => store.api.openImage(m.id, f.index, m.dir) : undefined}
    actions={src && <><Button icon={<IconDownload size={20} />} onClick={() => save(src, f.name, portal)}>Download</Button>
      {hasPerson && <Button onClick={async () => {
        if (await openPictureEditor({ into: portal, src, save: png => store.api.setPersonPicture(png) })) await store.refetch();
      }}>Use as my picture</Button>}
      {drive}</>} />;
}

function Picture({ m, f, onFailed, onOpen }: { m: AnyMsg; f: FileItem; onFailed: () => void; onOpen: (opener: HTMLButtonElement) => void }) {
  const store = useApp();
  const box = useRef<HTMLButtonElement>(null);
  const key = pictureKey(m, f);
  const known = pictures.get(key);
  const [url, setUrl] = useState(known?.picture ? known.url : "");
  const [shown, setShown] = useState(!!url); // a picture seen before shows at once; a new one fades in
  useInUse(key);
  useEffect(() => {
    const el = box.current;
    if (!el || url) return;
    let alive = true;
    const io = new IntersectionObserver((seen) => {
      if (!seen.some((s) => s.isIntersecting)) return;
      io.disconnect();
      fetchPicture(store.api, m, f).then((p) => {
        if (!alive) return;
        if (p.picture) setUrl(p.url); else onFailed();
      }).catch(() => { if (alive) onFailed(); });
    }, { rootMargin: "200px" });
    io.observe(el);
    return () => { alive = false; io.disconnect(); };
  }, [key]);
  // Keep a placeholder while loading; a single picture keeps its whole
  // aspect ratio, bounded by the conversation and viewport.
  return (
    <div className="min-w-0 space-y-1">
      <button ref={box} type="button" onClick={e => { if (url) onOpen(e.currentTarget); }} aria-label={"Open picture " + f.name} title={"Open full image: " + f.name}
        className="relative flex max-w-full items-center justify-center overflow-hidden rounded-xl border border-outline/30 bg-sunken"
        style={url ? { width: "fit-content" } : { width: "min(360px, 100%)", aspectRatio: "4 / 3" }}>
        {url && <img src={url} alt={f.name} onLoad={({ currentTarget: image }) => {
          const cached = pictures.get(key);
          if (cached?.url === url) cached.ratio = image.naturalWidth / image.naturalHeight;
          setShown(true);
        }} onError={onFailed} loading="lazy" decoding="async"
          className={"block h-auto max-h-[min(60dvh,480px)] w-auto max-w-full object-contain transition-opacity duration-200 motion-reduce:transition-none " + (shown ? "opacity-100" : "opacity-0")} />}
        {!shown && <span className="absolute inset-0 grid place-items-center text-muted"><IconPhoto size={28} stroke={1.6} className="animate-pulse motion-reduce:animate-none" /></span>}
      </button>
      {!isThreadMsg(m) && <SaveToDrive m={m} index={f.index} />}
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
function VideoFile({ m, f, inViewer = false }: { m: AnyMsg; f: FileItem; inViewer?: boolean }) {
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
  useInUse(key);
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
  // Opened from an album tile: that tap is the Play tap, as in Telegram's viewer.
  useEffect(() => { if (inViewer && here) void open(); }, []);
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
    <div role="group" aria-label={"Video " + f.name} className={"w-full min-w-0 space-y-1.5 " + (inViewer ? "max-w-[min(100%,960px)]" : "max-w-[480px]")}>
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
        className={"block w-full rounded-xl bg-black object-contain " + (inViewer ? "max-h-[65dvh]" : "max-h-[min(60dvh,480px)]")}
        onError={() => { if (owned.current) failed("This video can’t be played here. Download to play it."); }} />}
      <FileChip m={m} f={f} />
      {inline ? (opening || phase === "ready") && <div className="flex flex-wrap items-center gap-2">
        <Button className="min-h-11" aria-label={"Close video " + f.name} onClick={() => release()}>Close video</Button>
        {phase === "ready" && !inViewer && !document.fullscreenEnabled && <Button className="min-h-11" aria-label={"Expand video " + f.name} onClick={expand}>Expand</Button>}
      </div> : here && <p className="text-[12px] text-muted">{f.size > INLINE_VIDEO_LIMIT ? "Videos up to 16 MiB can play here." : "This video’s size is unknown."} Download to play this file.</p>}
      {note && <p role="status" className="text-[13px] text-text-2">{note}</p>}
      {url && expanded && <VideoViewer src={url} name={f.name} from={expanded} onClose={collapse} />}
    </div>
  );
}

function FileChip({ m, f, wrapName = false, bare = false, music = false }: { m: AnyMsg; f: FileItem; wrapName?: boolean; bare?: boolean; music?: boolean }) {
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
  const Icon = music ? IconMusic : /\.(zip|tar|gz|tgz|7z|rar)$/i.test(f.name) ? IconFileZip : /\.(txt|md|csv|json|log|pdf|docx?)$/i.test(f.name) ? IconFileText : IconFile;

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
    <div className={"flex min-w-0 items-center gap-2.5 py-1.5 pl-2.5 pr-1 text-ink " + (bare ? "" : "rounded-xl border border-outline/25 bg-surface/80")}>
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
