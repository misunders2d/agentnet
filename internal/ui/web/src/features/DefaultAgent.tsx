// The default agent: the program, and the folder it works in, that this
// computer runs for a request that names no agent (the device responder;
// agentnet responder set). Settings → Your agent, the Default agent card in
// Agents and "Choose your agents" all change it here, with the same call
// (POST /api/responder). Choosing a program grants nobody anything:
// approvals stay where they are, and the person's own tool permissions stay
// the authority. Named agents are separate, and none is changed here.
import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { IconCheck } from "@tabler/icons-react";
import { useEffect, useId, useState, type ReactNode } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { FolderField } from "./AssistantSetup.folders";
import { defaultAgentState } from "./AssistantSetup.model";
import { GroupLabel, Hint } from "./Settings.parts";

export const MANUAL = "manual";

export const harnessName = (h: string) => ({ claude: "Claude", codex: "Codex", pi: "Pi" } as Record<string, string>)[h] || (h ? h.charAt(0).toUpperCase() + h.slice(1) : "");

/** DefaultAgentChoice: the default agent's program (or none: the person
 *  answers) and its folder, and Save. extra: what shows under the choice
 *  (Settings: what it may do). inline: Cancel and Save always sit under it
 *  (inside a card) instead of a bar kept in view; onCancel closes it. */
export function DefaultAgentChoice({ view, saved, extra, inline = false, onCancel }: {
  view: T.ResponderView; saved: () => void; extra?: (choice: string) => ReactNode; inline?: boolean; onCancel?: () => void;
}) {
  const store = useApp();
  const me = useStore(store, (s) => s.overview?.person);
  const id = useId();
  const current = !view.chosen ? "" : view.manual ? MANUAL : view.harness || "";
  const [choice, setChoice] = useState(current);
  const [dir, setDir] = useState(view.dir || "");
  const [busy, setBusy] = useState(false);
  useEffect(() => { setChoice(current); setDir(view.dir || ""); }, [current, view.dir]);
  const harnesses = view.harnesses || [];
  const dirty = choice !== current || (choice !== MANUAL && dir.trim() !== (view.dir || ""));
  const canSave = dirty && !!choice && (choice === MANUAL || !!dir.trim());
  const undo = () => { setChoice(current); setDir(view.dir || ""); };
  const ask = !canSave && choice !== MANUAL ? "Choose the folder it works in" : choice === MANUAL ? "Answer yourself?" : "Use " + harnessName(choice) + "?";

  const save = async () => {
    setBusy(true);
    const change: T.ResponderChange = choice === MANUAL ? { manual: true, harness: "", dir: "" } : { manual: false, harness: choice, dir: dir.trim() };
    const r = await store.run((a) => a.setResponder(change));
    setBusy(false);
    if (r) { store.toast(choice === MANUAL ? "Questions and tasks now wait for you." : harnessName(choice) + " answers for you from now on.", "ok"); saved(); }
  };

  return (
    <div className={inline ? "space-y-4" : "space-y-6"}>
      <section aria-labelledby={id}>
        <GroupLabel id={id}>Answers for you</GroupLabel>
        <RadioGroup value={choice} onValueChange={(v) => setChoice(String(v))} aria-labelledby={id} className="grid gap-2.5 @md:grid-cols-2">
          <Choice value={MANUAL} selected={choice === MANUAL} avatar={<PersonAvatar name={me?.label || "Me"} seed={me?.person || "me"} size={40} />} title="No agent" sub="I’ll answer myself" />
          {harnesses.map((h) => (
            <Choice key={h.name} value={h.name} selected={choice === h.name} disabled={!h.found}
              avatar={<AgentAvatar seed={h.name} size={40} mood={choice === h.name ? "done" : "neutral"} device="laptop" />}
              title={harnessName(h.name)} sub={h.found ? "Installed here" : "Not installed on this computer"} />
          ))}
        </RadioGroup>
        {!harnesses.some((h) => h.found) && <Hint className="mt-2 px-1">No supported program was found where AgentNet looks for it on this computer.</Hint>}
      </section>

      {choice && choice !== MANUAL && (
        <section className="fade-in">
          <FolderField label="Works in" value={dir} onChange={setDir} hint="Requests from other people start in this folder. Your own sessions stay as they are. Tasks usually change files here, within your normal permissions." />
        </section>
      )}

      {extra?.(choice)}

      {inline ? (
        <div className="flex flex-wrap items-center justify-end gap-2">
          {dirty && <p className="min-w-0 flex-1 text-[15px] font-semibold">{ask}</p>}
          <Button variant="ghost" disabled={busy} onClick={() => { undo(); onCancel?.(); }}>Cancel</Button>
          <Button variant="act" disabled={!canSave || busy} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
        </div>
      ) : dirty && (
        // Stays in view while the change is unsaved, wherever the page is scrolled.
        <div role="region" aria-label="Unsaved change" className="fade-in sticky bottom-3 z-10 flex items-center gap-2 rounded-2xl stroke bg-surface p-2 pl-4 shadow-pop">
          <p className="min-w-0 flex-1 text-[15px] font-semibold">{ask}</p>
          <Button variant="ghost" onClick={undo}>Undo</Button>
          <Button variant="act" disabled={!canSave || busy} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
        </div>
      )}
    </div>
  );
}

/** DefaultAgentPanel: the default agent at the top of "Choose your agents":
 *  its program, its folder and whether it can start, with Change right
 *  there. view null: not read (yet, or error says why). */
export function DefaultAgentPanel({ view, error, disabled, onSaved }: { view: T.ResponderView | null; error?: string; disabled?: boolean; onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const s = defaultAgentState(view, harnessName);
  const set = !!view?.chosen && !view.manual;
  return (
    <section aria-label="Default agent" className="space-y-1.5 rounded-2xl stroke bg-surface p-3">
      <p className="flex flex-wrap items-center gap-2"><span className="font-semibold">Default agent</span>{view && <Tag tone={s.tone}>{s.word}</Tag>}</p>
      {view ? <>
        <p className="text-[15px]">{set ? <><span className="font-semibold">{s.line}</span><span className="text-text-2"> works in </span><span className="font-mono text-[13px] [overflow-wrap:anywhere]">{view.dir}</span></> : s.line}</p>
        {set && !view.ready && <p className="text-[14px] text-danger">{view.problem || "It can’t start on this computer."}</p>}
      </> : <p className="text-[14px] text-text-2">{error ? "It couldn’t be read: " + error : "Checking…"}</p>}
      <p className="text-[14px] text-text-2">Used when a request to this computer doesn’t name an agent. Setting up the agents below leaves it as it is.</p>
      {view && !open && (
        <Button variant={set && view.ready ? "outline" : "act"} size="sm" className="mt-1" disabled={disabled} onClick={() => setOpen(true)}>
          {set || view.manual ? "Change default agent" : "Choose a default agent"}
        </Button>
      )}
      {view && open && (
        <div className="mt-2 border-t border-hairline pt-3">
          <DefaultAgentChoice view={view} inline saved={() => { setOpen(false); onSaved(); }} onCancel={() => setOpen(false)} />
        </div>
      )}
    </section>
  );
}

function Choice({ value, selected, disabled, avatar, title, sub }: { value: string; selected: boolean; disabled?: boolean; avatar: ReactNode; title: string; sub: string }) {
  return (
    <label className={"press flex min-h-[72px] items-center gap-3 rounded-2xl stroke p-3 " + (disabled ? "cursor-not-allowed bg-sunken opacity-60" : selected ? "cursor-pointer bg-agent-fill text-ink shadow-pop-sm" : "cursor-pointer bg-surface hover:bg-sunken")}>
      {avatar}
      <span className="min-w-0 flex-1">
        <span className="block font-semibold">{title}</span>
        <span className={"block text-[13px] " + (selected ? "text-ink/80" : "text-muted")}>{sub}</span>
      </span>
      <Radio.Root value={value} disabled={disabled} className={"grid size-7 shrink-0 place-items-center rounded-full stroke " + (selected ? "bg-[#1B1530] text-act" : "bg-surface")}>
        <Radio.Indicator><IconCheck size={16} stroke={3} aria-hidden="true" /></Radio.Indicator>
      </Radio.Root>
    </label>
  );
}
