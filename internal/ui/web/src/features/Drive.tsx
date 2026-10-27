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

interface DriveAvailability { enabled?: boolean; configured?: boolean; connected?: boolean; requires_reconnect?: boolean; space?: unknown; pending?: unknown }
// Attachment rows share one in-flight status read per conversation. This is a
// capability check only; the provider still enforces membership and consent.
const driveChecks = new WeakMap<object, Map<string, Promise<DriveAvailability>>>();
function checkDrive(store: ReturnType<typeof useApp>, conv: string, revision: number): Promise<DriveAvailability> {
  let checks = driveChecks.get(store);
  if (!checks) driveChecks.set(store, checks = new Map());
  const key = JSON.stringify([conv, revision]);
  const prior = checks.get(key);
  if (prior) return prior;
  const work = store.host.drive!.drive<DriveAvailability>({conv, action: "status"});
  checks.set(key, work);
  void work.finally(() => { if (checks!.get(key) === work) checks!.delete(key); }).catch(() => {});
  return work;
}

export function SaveToDrive({ m, index }: { m: AnyMsg; index: number }) {
  const store = useApp();
  const dm = useStore(store, (s) => s.dm);
  const [open, setOpen] = useState(false);
  const revision = useStore(store, s => s.contentRevision);
  const conv = dm?.peer.person ? dm.id : "";
  const [availability, setAvailability] = useState<{conv:string; revision:number; value?:DriveAvailability; failed?:boolean}>();
  useEffect(() => {
    let active = true;
    if (conv && store.host.drive) void checkDrive(store, conv, revision).then(value => {
      if (active) setAvailability({conv, revision, value});
    }, () => { if (active) setAvailability({conv, revision, failed:true}); });
    return () => { active = false; };
  }, [store, conv, revision]);
  const status = availability?.conv === conv ? availability : undefined;
  const view = status?.value;
  const ready = !!(view?.enabled && view.configured && view.connected && !view.requires_reconnect && view.space && !view.pending);
  useEffect(() => { if (!ready) setOpen(false); }, [ready, conv]);
  if (!dm?.peer.person) return null;
  const unavailable = !store.host.drive ? "Google Drive unavailable" : status?.failed ? "Google Drive status unavailable" : !view ? "Checking Google Drive…" : !view.enabled ? "Google Drive off" : !ready ? "Set up Google Drive in Project space" : "";
  return <>
    <button type="button" className="min-h-11 rounded-lg px-2 text-left text-[13px] text-text-2 underline hover:bg-sunken" disabled={!ready} onClick={() => setOpen(true)}>{ready ? "Save a readable copy to Google Drive" : unavailable}</button>
    <Confirm open={open && ready} onOpenChange={setOpen} title="Save to Google Drive?" ok="Save to Google Drive" danger={false}
      onOk={() => store.run(async () => {
        const provider = store.host.drive;
        if (!provider) throw new Error("Google Drive is unavailable on this device.");
        return saveAttachmentToDrive(provider, conv, m, index, true);
      }, "Saved to Google Drive.")}>
      <p>{notice} This saves a separate copy; the encrypted attachment stays in this chat.</p>
    </Confirm>
  </>;
}
