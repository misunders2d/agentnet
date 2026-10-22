// Reuse the shared presentation widgets (skins/shared/drivespace*.mjs),
// bundled by build.sh. They get only
// this workspace's host provider, never a default transport or page global.
import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { useApp } from "../context";
import { useStore } from "../store";
import { mountDriveSpace, saveAttachmentToDrive } from "../drive-space.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Confirm } from "./Message.actions";
import type { AnyMsg } from "./Message.model";
import { RelayAttachmentStorage } from "./RelayAttachmentStorage";

const colors = { "--panel": "var(--an-surface)", "--text": "var(--an-ink)", "--border": "var(--an-outline)" } as CSSProperties;
const notice = "Google Drive files are outside AgentNet end-to-end encryption. Google permissions apply independently.";

export function DriveSettings() {
  const store = useApp();
  const isActive = useCallback(() => store.isActive(), [store]);
  return <RelayAttachmentStorage host={store.host} isActive={isActive} />;
}

export function ProjectSpaceSheet({ conv, open, onOpenChange }: { conv: string; open: boolean; onOpenChange: (v: boolean) => void }) {
  return <Sheet open={open} onOpenChange={onOpenChange} title="Project space" description={notice} wide>
    {open && <ProjectSpace conv={conv} onSettings={() => onOpenChange(false)} />}
  </Sheet>;
}

function ProjectSpace({ conv, onSettings }: { conv: string; onSettings: () => void }) {
  const store = useApp();
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const provider = store.host.drive;
    if (!root.current || !provider) return;
    const agents = (store.get().dm?.agents || []).filter((a) => a.state === "active" && a.host_here).map((a) => ({ pid: a.pid }));
    const widget = mountDriveSpace(root.current, { conv, provider, agents, openSettings: () => { onSettings(); store.showTab("settings", "storage"); } });
    const stop = store.host.listen((e) => { if (e.type === "change" && store.isActive()) widget.refresh(); });
    return () => { stop(); widget.destroy(); };
  }, [store, conv]);
  return <div style={colors} className="min-w-0">
    {!store.host.drive && <p role="status">Google Drive is unavailable on this device.</p>}
    <div ref={root} />
  </div>;
}

export function SaveToDrive({ m, index }: { m: AnyMsg; index: number }) {
  const store = useApp();
  const dm = useStore(store, (s) => s.dm);
  const [open, setOpen] = useState(false);
  if (!dm?.peer.person) return null;
  const conv = dm.id;
  return <>
    <button type="button" className="min-h-11 rounded-lg px-2 text-left text-[13px] text-text-2 underline hover:bg-sunken" onClick={() => setOpen(true)}>Save to project space</button>
    <Confirm open={open} onOpenChange={setOpen} title="Save to Google Drive?" ok="Save to Google Drive" danger={false}
      onOk={() => store.run(async () => {
        const provider = store.host.drive;
        if (!provider) throw new Error("Google Drive is unavailable on this device.");
        return saveAttachmentToDrive(provider, conv, m, index, true);
      }, "Saved to Google Drive.")}>
      <p>{notice} This saves a separate copy; the encrypted attachment stays in this chat.</p>
    </Confirm>
  </>;
}
