// Choosing a folder on this computer by browsing it, never by typing a path
// (owner, MEL-534): the folder an agent works in. Read-only: it lists the
// folders the server shows (GET /api/folders) and creates nothing. Used by
// AssistantSetup and by Settings → Your agent; Agents (the New agent sheet
// and the Connect an agent wizard) can use FolderField the same way.
import { IconArrowUp, IconChevronRight, IconDeviceDesktop, IconFolder, IconHome } from "@tabler/icons-react";
import { useEffect, useId, useRef, useState } from "react";
import { errorText, type FoldersView } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { folderEntries, folderName, folderWayOut, type FolderStep } from "./AssistantSetup.model";
import { Failed, Hint } from "./Settings.parts";

/** FolderField: the chosen folder in words, and a button that opens the
 *  folder browser. Empty until a folder is chosen. */
export function FolderField({ label, value, onChange, disabled, hint }: {
  label: string; value: string; onChange: (path: string) => void; disabled?: boolean; hint?: string;
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <div role="group" aria-labelledby={id}>
      <p id={id} className="mb-1.5 text-[14px] font-bold">{label}</p>
      <div className="flex flex-wrap items-center gap-3 rounded-2xl stroke bg-surface p-3">
        <span aria-hidden="true" className="grid size-9 shrink-0 place-items-center rounded-xl bg-sunken text-ink"><IconFolder size={20} /></span>
        <span className="min-w-0 flex-1">
          {value
            ? <><span className="block font-semibold [overflow-wrap:anywhere]">{folderName(value)}</span><span className="block font-mono text-[12.5px] text-muted [overflow-wrap:anywhere]">{value}</span></>
            : <span className="block text-[15px] text-muted">No folder chosen yet</span>}
        </span>
        <Button size="sm" variant={value ? "outline" : "act"} disabled={disabled} onClick={() => setOpen(true)} aria-label={(value ? "Change" : "Choose") + " folder: " + label}>
          {value ? "Change…" : "Choose folder…"}
        </Button>
      </div>
      {hint && <Hint className="mt-1.5 px-1">{hint}</Hint>}
      <FolderSheet open={open} onOpenChange={setOpen} start={value} title={label} onPick={(p) => { onChange(p); setOpen(false); }} />
    </div>
  );
}

/** FolderSheet browses folders from start (or home) and returns the one
 *  the person opens and picks. A folder that can't be read (deleted, or
 *  closed to this person) still leaves a way on: Up, Home, the drives. */
export function FolderSheet({ open, onOpenChange, start, title, onPick }: {
  open: boolean; onOpenChange: (open: boolean) => void; start?: string; title: string; onPick: (path: string) => void;
}) {
  const store = useApp();
  const [path, setPath] = useState<string | undefined>(start || undefined);
  const [v, setV] = useState<FoldersView | null>(null);
  const [last, setLast] = useState<FoldersView | null>(null);   // the last folder shown
  const [error, setError] = useState("");
  const [tries, setTries] = useState(0);
  const gen = useRef(0);
  // Each opening starts where the field is (or at home).
  useEffect(() => { if (open) { setPath(start || undefined); setV(null); setLast(null); setError(""); } }, [open]);
  useEffect(() => {
    if (!open) return;
    const mine = ++gen.current;
    setError("");
    store.api.folders(path).then(
      (r) => { if (gen.current === mine) { setV(r); setLast(r); } },
      (e) => { if (gen.current === mine) setError(errorText(e)); });
    return () => { gen.current++; };
  }, [open, path, tries]);
  // Opening another folder forgets the one shown, so "Use" never picks a folder no longer on screen.
  // No path: the server's own default folder (home).
  const go = (p?: string) => { setV(null); setPath(p); };
  const shown = error ? null : v;
  const dirs = shown ? folderEntries(shown) : [];
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title="Choose a folder" description={title}
      footer={
        <div className="flex flex-col gap-2">
          <Button variant="act" size="lg" disabled={!shown} onClick={() => shown && onPick(shown.path)}>
            {shown ? "Use “" + folderName(shown.path) + "”" : "Use this folder"}
          </Button>
          <Button variant="ghost" size="lg" onClick={() => onOpenChange(false)}>Cancel</Button>
        </div>
      }>
      {error ? (
        <div className="space-y-3">
          <Failed text={error} retry={() => setTries((n) => n + 1)} />
          <Steps steps={folderWayOut(path, last)} go={go} />
        </div>
      ) : !shown ? (
        <p aria-busy="true" className="py-3 text-[15px] text-muted">Reading folders…</p>
      ) : (
        <div className="space-y-3">
          <div className="rounded-2xl bg-sunken px-3.5 py-2.5">
            <p className="text-[13px] font-semibold text-muted">In</p>
            <p className="font-mono text-[14px] text-ink [overflow-wrap:anywhere]">{shown.path}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" icon={<IconArrowUp size={18} />} disabled={!shown.parent} onClick={() => shown.parent && go(shown.parent)}>Up</Button>
            {shown.home && <Button size="sm" icon={<IconHome size={18} />} disabled={shown.home === shown.path} onClick={() => shown.home && go(shown.home)}>Home</Button>}
            {(shown.roots || []).map((r) => (
              <Button key={r} size="sm" icon={<IconDeviceDesktop size={18} />} disabled={r === shown.path} onClick={() => go(r)}>{r}</Button>
            ))}
          </div>
          {dirs.length ? (
            <ul aria-label={"Folders in " + folderName(shown.path)} className="divide-y divide-hairline overflow-hidden rounded-2xl stroke bg-surface">
              {dirs.map((d) => (
                <li key={d.path}>
                  <button type="button" onClick={() => go(d.path)} className="flex min-h-12 w-full items-center gap-3 px-3.5 py-2 text-left hover:bg-sunken">
                    <IconFolder size={20} aria-hidden="true" className="shrink-0 text-muted" />
                    <span className="min-w-0 flex-1 font-semibold [overflow-wrap:anywhere]">{d.name}</span>
                    <IconChevronRight size={18} aria-hidden="true" className="shrink-0 text-muted" />
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="px-1 text-[15px] text-muted">No folders inside this one.</p>
          )}
          {shown.truncated && <Hint className="px-1">Some folders in here aren’t shown.</Hint>}
        </div>
      )}
    </Sheet>
  );
}

const STEP_ICON = { up: IconArrowUp, home: IconHome, root: IconDeviceDesktop };

/** Steps: the ways on from a folder that couldn't be read. */
function Steps({ steps, go }: { steps: FolderStep[]; go: (path?: string) => void }) {
  if (!steps.length) return null;
  return (
    <div className="flex flex-wrap gap-2">
      {steps.map((s) => { const Icon = STEP_ICON[s.kind]; return <Button key={s.kind + (s.path || "")} size="sm" icon={<Icon size={18} />} onClick={() => go(s.path)}>{s.label}</Button>; })}
    </div>
  );
}
