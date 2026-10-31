// The skin gallery: skins published in AgentNet's skin collection, read and
// checked by the host (host.gallery). Nothing is installed until the person
// chooses Install; using a skin afterwards still goes through the host's
// trust step, as for any skin stored in this browser. The host updates an
// installed skin on its own and keeps trust given to the version replaced.
import { IconChevronLeft, IconChevronRight } from "@tabler/icons-react";
import { useCallback, useEffect, useState } from "react";
import { errorText } from "../api";
import type { Gallery, GallerySkin } from "../host";
import { Button, IconButton } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { Card, Failed, GroupLabel, Hint, Skeleton } from "./Settings.parts";

export function SkinGallery({ gallery, onInstalled, onUse }: { gallery: Gallery; onInstalled: (id: string) => void; onUse: (id: string) => void }) {
  const [shown, setShown] = useState<{ loading: boolean; error?: string; skins?: GallerySkin[] }>({ loading: true });
  const [generation, setGeneration] = useState(0);
  const load = useCallback((fresh: boolean) => {
    let alive = true;
    setShown((s) => ({ ...s, loading: true, error: undefined }));
    gallery.list({ fresh }).then((r) => { if (alive) setShown({ loading: false, skins: r.skins }); },
      (e) => { if (alive) setShown({ loading: false, error: errorText(e) }); });
    return () => { alive = false; };
  }, [gallery]);
  useEffect(() => load(generation > 0), [load, generation]);
  const again = () => setGeneration((n) => n + 1);

  return (
    <section aria-labelledby="appearance-gallery" className="mt-6">
      <GroupLabel id="appearance-gallery">Skin gallery</GroupLabel>
      {shown.error ? <Failed text={shown.error} retry={again} />
        : !shown.skins ? <Skeleton lines={3} />
        : !shown.skins.length ? <Card className="p-4"><p className="text-[15px]">No skins are published yet.</p></Card>
        : <div className="grid gap-3 sm:grid-cols-2">
            {shown.skins.map((s) => <GalleryCard key={s.id} gallery={gallery} skin={s} onUse={onUse}
              onInstalled={(id) => { again(); onInstalled(id); }} />)}
          </div>}
      <Hint className="mt-2.5 px-1">Skins from AgentNet’s skin collection. AgentNet checks every file before storing a skin in this browser. A skin can read your chats and act as you, so you’re asked to trust it when you choose it.</Hint>
    </section>
  );
}

function GalleryCard({ gallery, skin, onInstalled, onUse }: { gallery: Gallery; skin: GallerySkin; onInstalled: (id: string) => void; onUse: (id: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const install = async () => {
    setBusy(true); setError("");
    try { onInstalled(await gallery.install(skin.id)); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  const action = skin.state === "incompatible"
    ? <Button size="sm" disabled>{skin.agentnet ? "Needs AgentNet " + skin.agentnet : "Needs a newer AgentNet"}</Button>
    : skin.state === "installed"
      ? <Button size="sm" onClick={() => onUse(skin.installed)} aria-label={"Use " + skin.name}>Use</Button>
      : <Button size="sm" variant="act" disabled={busy} onClick={() => void install()} aria-label={(skin.state === "update" ? "Update " : "Install ") + skin.name}>
          {busy ? (skin.state === "update" ? "Updating…" : "Installing…") : skin.state === "update" ? "Update" : "Install"}
        </Button>;
  return (
    <Card className="flex flex-col overflow-hidden">
      <Previews gallery={gallery} skin={skin} />
      <div className="flex flex-1 flex-col gap-2 p-4">
        <p className="flex flex-wrap items-center gap-2 font-semibold">{skin.name}<Tag>{skin.version}</Tag>
          {skin.state === "installed" && <Tag tone="ok">In this browser</Tag>}{skin.state === "update" && <Tag tone="act">Update available</Tag>}</p>
        {skin.description && <Hint>{skin.description}</Hint>}
        {skin.state === "incompatible" && <Hint>Update AgentNet to use this skin.</Hint>}
        {error && <p role="alert" className="text-[13px] font-semibold text-danger">{error}</p>}
        <div className="mt-auto flex justify-end pt-1">{action}</div>
      </div>
    </Card>
  );
}

// Previews are fetched one at a time and shown only after the host matched
// their hash; each object URL is revoked when the picture changes or leaves.
function Previews({ gallery, skin }: { gallery: Gallery; skin: GallerySkin }) {
  const [index, setIndex] = useState(0);
  const [url, setUrl] = useState("");
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (!skin.previews) return;
    let alive = true, made = "";
    setUrl(""); setFailed(false);
    gallery.preview(skin.id, index).then((blob) => {
      made = URL.createObjectURL(blob);
      if (alive) setUrl(made); else URL.revokeObjectURL(made);
    }, () => { if (alive) setFailed(true); });
    return () => { alive = false; if (made) URL.revokeObjectURL(made); };
  }, [gallery, skin.id, skin.previews, index]);
  if (!skin.previews) return null;
  const count = skin.previews;
  return (
    <div className="relative aspect-[16/10] overflow-hidden border-b-[1.5px] border-outline bg-sunken lg:border-b">
      {url ? <img src={url} alt={skin.alts[index] || skin.name + ", picture " + (index + 1)} className="block h-full w-full object-cover object-top" />
        : failed ? <p className="grid h-full place-items-center p-4 text-center text-[13px] text-muted">This picture could not be shown.</p>
        : <div aria-hidden="true" className="h-full w-full animate-pulse bg-sunken motion-reduce:animate-none" />}
      {count > 1 && (
        <div className="absolute inset-x-2 bottom-2 flex items-center justify-between">
          <IconButton label={"Previous picture of " + skin.name} className="bg-surface stroke" onClick={() => setIndex((index + count - 1) % count)}><IconChevronLeft size={20} aria-hidden="true" /></IconButton>
          <span className="rounded-full bg-surface px-2.5 py-1 text-[13px] font-semibold stroke tnum">{index + 1} of {count}</span>
          <IconButton label={"Next picture of " + skin.name} className="bg-surface stroke" onClick={() => setIndex((index + 1) % count)}><IconChevronRight size={20} aria-hidden="true" /></IconButton>
        </div>
      )}
    </div>
  );
}
