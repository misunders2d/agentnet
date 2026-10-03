// Setting up a named agent on this computer: its name (new agents only:
// a saved name is signed and does not change here), the program it runs
// and the folder it works in. Saving runs nothing.
import { useEffect, useState } from "react";
import type { T } from "../api";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export interface AgentSetup { label: string; harness: string; dir: string }

export function AgentSheet({ open, onOpenChange, harnesses, agent, onSave }: {
  open: boolean; onOpenChange: (v: boolean) => void; harnesses: T.HarnessView[]; agent?: T.CatalogAgent; onSave: (s: AgentSetup) => Promise<boolean>;
}) {
  const [label, setLabel] = useState("");
  const [harness, setHarness] = useState("");
  const [dir, setDir] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open) return;
    setLabel(""); setBusy(false);
    setHarness(agent?.responder?.harness || harnesses.find((h) => h.found)?.name || "");
    setDir(agent?.responder?.dir || "");
  }, [open]);
  const absolute = dir.trim().startsWith("/") || /^[A-Za-z]:\\/.test(dir.trim());
  const ready = (!!agent || !!label.trim()) && !!harness && absolute;
  const save = async () => {
    setBusy(true);
    if (await onSave({ label: label.trim(), harness, dir: dir.trim() })) onOpenChange(false);
    setBusy(false);
  };
  const found = harnesses.filter((h) => h.found);
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={agent ? "Turn on " + agent.record.label : "New agent"}
      description={agent ? "Choose what it runs and where it works." : "Give it a name, the program it runs and the folder it works in."}
      footer={
        <div className="flex flex-col gap-2">
          <Button variant="act" size="lg" disabled={!ready || busy} onClick={save}>{busy ? "Saving…" : agent ? "Turn on" : "Create agent"}</Button>
          <Button variant="ghost" size="lg" onClick={() => onOpenChange(false)}>Cancel</Button>
        </div>
      }>
      {!agent && <>
        <label htmlFor="agent-name" className="block text-[14px] font-bold">Name</label>
        <input id="agent-name" value={label} onChange={(e) => setLabel(e.target.value)} maxLength={64} autoComplete="off" placeholder="e.g. Stocky"
          className="mt-1.5 min-h-12 w-full rounded-2xl bg-surface stroke px-3.5 text-[16px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
      </>}
      <fieldset className={agent ? "" : "mt-5"}>
        <legend className="text-[14px] font-bold">Program</legend>
        {!found.length && <p className="pt-1 text-[14px] text-danger">No supported program was found on this computer.</p>}
        <div className="mt-1.5 flex flex-col gap-2">
          {harnesses.map((h) => (
            <label key={h.name} className={"flex min-h-12 items-center gap-3 rounded-2xl px-3.5 stroke " + (h.found ? "cursor-pointer bg-surface has-[:checked]:bg-agent" : "bg-sunken opacity-60")}>
              <input type="radio" name="agent-harness" value={h.name} checked={harness === h.name} disabled={!h.found} onChange={() => setHarness(h.name)} className="size-5 accent-[var(--an-agent-ink)]" />
              <span className="flex-1 font-semibold capitalize">{h.name}</span>
              <span className="text-[13px] text-muted">{h.found ? "Installed here" : "Not installed here"}</span>
            </label>
          ))}
        </div>
      </fieldset>
      <label htmlFor="agent-dir" className="mt-5 block text-[14px] font-bold">Works in</label>
      <input id="agent-dir" value={dir} onChange={(e) => setDir(e.target.value)} autoComplete="off" spellCheck={false} placeholder="/home/you/projects/warehouse"
        className="mt-1.5 min-h-12 w-full rounded-2xl bg-surface stroke px-3.5 font-mono text-[15px] outline-none focus-visible:ring-2 focus-visible:ring-agent-ink" />
      <p className="pt-2 text-[13px] text-muted">A folder on this computer, written in full. Saving runs nothing. Whether the program is signed in shows the first time it works.</p>
    </Sheet>
  );
}
