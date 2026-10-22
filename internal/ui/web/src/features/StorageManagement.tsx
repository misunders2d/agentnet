import { useEffect, useRef, useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Hint } from "./Settings.parts";
import { storageBytes, storageFiles } from "./RelayAttachmentStorage.model";

export function DeviceCacheControls({ onChange }: { onChange: () => void }) {
  const store = useApp();
  const [selection, setSelection] = useState<T.StorageCacheSelection | null>(null);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [stopping, setStopping] = useState(false);
  const stop = useRef(false), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; stop.current = true; }; }, []);
  const active = () => mounted.current && store.isActive();
  const preview = async () => {
    setBusy("Checking received cache…"); setNotice(""); setError("");
    try { const v = await store.api.storageCache(); if (active()) setSelection(v); }
    catch (e) { if (active()) setError(errorText(e)); }
    if (active()) setBusy("");
  };
  const clear = async () => {
    if (!selection || busy) return;
    stop.current = false; setStopping(false); setError("");
    const files = selection.files || [];
    let removed = 0, retained = 0;
    try {
      for (const [index, file] of files.entries()) {
        if (stop.current || !active()) break;
        setBusy(`Checking and clearing ${index + 1} of ${files.length}…`);
        // One bounded file per action permits cancellation between files and
        // keeps the reviewed batch bound to its captured workspace provider.
        const v = await store.api.clearStorageCache([file]);
        removed += v.removed.files; retained += v.retained.files;
      }
      if (active()) setNotice(`${storageFiles(removed)} cleared.${retained ? ` ${storageFiles(retained)} kept because another copy could not be verified.` : ""}${stop.current ? " Stopped before the next file." : ""}`);
    } catch (e) { if (active()) setError(errorText(e)); }
    if (active()) { setBusy(""); setSelection(null); onChange(); }
  };
  const requestStop = () => { stop.current = true; setStopping(true); };
  return <div className="space-y-2 pt-3">
    <Button variant="outline" disabled={!!busy} onClick={() => void preview()}>{busy || "Review received cache…"}</Button>
    <Hint>Checks up to 200 complete received files. A file is cleared only after its full server copy is verified. Unsent files, failed-send copies and files held only here stay protected.</Hint>
    {notice && <p role="status" className="text-[14px] text-text-2">{notice}</p>}
    {error && <p role="alert" className="text-[14px] text-danger">{error}</p>}
    <Sheet open={selection !== null} onOpenChange={open => { if (!open) { if (busy) requestStop(); else setSelection(null); } }}
      title="Clear this received cache?" description="Only the reviewed files on this device are selected."
      footer={<div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" disabled={stopping} onClick={() => busy ? requestStop() : setSelection(null)}>{busy ? stopping ? "Stopping after this file…" : "Stop after this file" : "Cancel"}</Button>
        <Button disabled={!!busy || !selection?.files?.length} onClick={() => void clear()}>Verify and clear cache</Button>
      </div>}>
      <p><strong>{storageFiles(selection?.usage.files || 0)} · {storageBytes(selection?.usage.bytes || 0)}</strong> are candidates. Each local file and its authorized server copy are checked before removal; files without a matching server copy remain here.</p>
      <p className="mt-3">Chat history, unsent files and files saved to your folders remain. Opening an attachment later downloads it normally. A failed or offline check keeps the cached file.</p>
      {busy && <p role="status" className="mt-3">{busy}</p>}
    </Sheet>
  </div>;
}

export function RelayQuotaControls({ usage, onChange }: { usage: T.HubStorage; onChange: () => void }) {
  const store = useApp();
  const [value, setValue] = useState(String(usage.quota_bytes / (1 << 30)));
  const [confirm, setConfirm] = useState(false), [busy, setBusy] = useState(false);
  const [error, setError] = useState(""), [notice, setNotice] = useState("");
  const next = Number(value) * (1 << 30);
  const valid = /^\d+(?:\.\d+)?$/.test(value) && Number.isSafeInteger(next) && next > 0;
  const used = usage.global ? usage.global.stored.reserved_bytes + usage.global.incomplete.reserved_bytes : 0;
  const save = async () => {
    if (!valid || busy) return;
    setBusy(true); setError("");
    try { await store.api.changeStorageQuota(next); if (store.isActive()) { setConfirm(false); setNotice("Relay quota saved."); onChange(); } }
    catch (e) { if (store.isActive()) setError(errorText(e)); }
    if (store.isActive()) setBusy(false);
  };
  if (!usage.can_change_quota) return <Hint>Only a server admin can change the relay quota. Connecting Drive does not increase this limit.</Hint>;
  return <div className="space-y-2 border-t border-hairline pt-3">
    <form onSubmit={e => { e.preventDefault(); if (valid) setConfirm(true); }} className="flex flex-wrap items-end gap-3">
      <label className="block min-w-0 flex-1 text-[14px] font-semibold">Relay quota (GiB)
        <input aria-invalid={!valid} type="text" inputMode="decimal" value={value} onChange={e => { setValue(e.target.value); setError(""); }} className="mt-1 h-11 w-full rounded-xl stroke bg-surface px-3 text-[16px] text-ink" />
      </label>
      <Button type="submit" disabled={!valid || busy || next === usage.quota_bytes}>Review limit…</Button>
    </form>
    <Hint>This shared logical limit counts encrypted attachment reservations, including files in Drive. It is separate from free disk space and Google’s storage allowance.</Hint>
    {notice && <p role="status" className="text-[14px]">{notice}</p>}
    {error && <p role="alert" className="text-[14px] text-danger">{error}</p>}
    <Sheet open={confirm} onOpenChange={open => { if (!busy) setConfirm(open); }} title="Change everyone’s relay quota?" description="This is an administrator setting for the workspace."
      footer={<div className="flex justify-end gap-2"><Button variant="ghost" disabled={busy} onClick={() => setConfirm(false)}>Cancel</Button><Button disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save relay quota"}</Button></div>}>
      <p>Change the shared limit from <strong>{storageBytes(usage.quota_bytes)}</strong> to <strong>{storageBytes(next)}</strong>. The choice survives server restarts.</p>
      <p className="mt-3">Existing files are kept when the limit is lowered.</p>
      {next <= used && <p role="alert" className="mt-3 text-approval-ink">Current reservations use {storageBytes(used)}. New attachments cannot be sent until an administrator raises the limit or reclaims eligible storage.</p>}
      {error && <p role="alert" className="mt-3 text-danger">{error}</p>}
    </Sheet>
  </div>;
}
