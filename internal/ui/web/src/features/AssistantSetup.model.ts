// The rules of "Connect your coding sessions" (AssistantSetup.tsx): which
// tools can be chosen, the named agent each one may get, and the order in
// which an approved setup is saved. Kept apart from the screen so node checks
// them as they are (internal/ui/testdata/assistant_setup_model_check.mjs):
// this file has type imports only.
//
// The server decides everything that matters: which tools it found, what
// each change is, and whether the change it reviewed is still the change it
// applies (review_id). Nothing here grants anyone anything.
import type { FolderEntry, FoldersView, T } from "../api";

export type Stage = "home" | "choose" | "review" | "saved";

/** NAME_MAX is the longest name a new named agent may get (the signed
 *  agent record's label limit, as the old setup and the New agent sheet use). */
export const NAME_MAX = 64;

/** A browser device (the phone app, or a browser on a computer) runs and
 *  installs nothing; the computer that runs the tools does this setup. */
export const browserDevice = (platform: string, o: T.Overview | null | undefined) =>
  platform === "browser" || !!(o as { device?: unknown } | null | undefined)?.device;

/** The named agent a tool gets: an existing one (id set) or a new one, and
 *  the folder it works in. mustChoose: there are several, and the person
 *  has not said which one. */
export interface AgentPick { id: string; label: string; dir: string; mustChoose: boolean }

/** What each tool state the server reports means, in a few plain words. */
export const STATE_WORDS: Record<string, string> = {
  connected: "Connected",
  needs_activation: "Start a new session",
  detected: "Found",
  needs_setup: "Needs setup",
  not_detected: "Not found",
  unsupported: "Not here",
  error: "Left as it is",
};

/** One sentence per state, under the tool's name. The server's own note
 *  (paths and native terms) stays under Details. */
export const STATE_SENTENCE: Record<string, string> = {
  connected: "Set up, and one of its sessions has checked in. That doesn’t test its sign-in.",
  needs_activation: "Set up. Sessions you start from now on can use it.",
  detected: "Installed on this computer, not connected yet.",
  needs_setup: "Installed on this computer. Its connection needs setting up again.",
  not_detected: "Not found on this computer.",
  unsupported: "Can’t be connected on this computer yet. Nothing was changed.",
  error: "Its settings can’t be changed safely, so nothing was changed.",
};

export const tools = (v: T.AssistantSetupView | null | undefined) => v?.harnesses || [];

/** A tool can be chosen when the server found it and can set it up safely. */
export const selectable = (h: T.AssistantSetupHarness) => h.detected && h.supported;

/** The tools ticked when the list opens: the ones already set up, so running
 *  setup again repairs them. */
export const startChosen = (v: T.AssistantSetupView) => new Set(tools(v).filter((h) => selectable(h) && h.configured).map((h) => h.id));

/** The person's named agents that run this tool. */
export const agentsFor = (c: T.AgentCatalogView | null | undefined, harness: string) =>
  (c?.agents || []).filter((a) => a.enabled && a.responder?.harness === harness);

/** A tool gets a named agent only when this computer can run one with it
 *  (OMP, for example, is connected but never runs one here). */
export const canHaveAgent = (c: T.AgentCatalogView | null | undefined, harness: string) =>
  (c?.harnesses || []).some((h) => h.name === harness && h.found);

/** firstPick: the only existing agent for the tool, or a new one named after it. */
export function firstPick(h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined): AgentPick {
  const mine = agentsFor(c, h.id), only = mine.length === 1 ? mine[0] : undefined;
  return { id: only?.record.id || "", label: only?.record.label || h.label, dir: only?.responder?.dir || "", mustChoose: mine.length > 1 };
}

export const pickOf = (a: T.CatalogAgent): AgentPick => ({ id: a.record.id, label: a.record.label, dir: a.responder?.dir || "", mustChoose: false });

export const pickFor = (picks: ReadonlyMap<string, AgentPick>, h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined) =>
  picks.get(h.id) || firstPick(h, c);

/** notReady says what is still missing before the changes can be reviewed,
 *  or "" when every chosen tool that gets an agent has its name and folder. */
export function notReady(chosen: T.AssistantSetupHarness[], c: T.AgentCatalogView | null | undefined, picks: ReadonlyMap<string, AgentPick>): string {
  if (!chosen.length) return "Choose at least one tool.";
  for (const h of chosen) {
    if (!canHaveAgent(c, h.id)) continue;
    const p = pickFor(picks, h, c);
    if (p.mustChoose) return "Choose which agent " + h.label + " uses.";
    if (!p.label.trim()) return "Give the agent for " + h.label + " a name.";
    if (!p.dir.trim()) return "Choose the folder the agent for " + h.label + " works in.";
  }
  return "";
}

/** The calls applySetup makes, bound to one workspace's host (api.ts). */
export interface SetupCalls {
  setup: (r?: T.AssistantSetupRequest) => Promise<T.AssistantSetupView>;
  agents: () => Promise<T.AgentCatalogView>;
  changeAgents: (c: T.AgentCatalogChange) => Promise<T.AgentCatalogChangeResult>;
}

export interface Applied {
  view: T.AssistantSetupView;          // the tool list after the change
  catalog: T.AgentCatalogView;
  picks: Map<string, AgentPick>;       // each saved agent, by tool
  agents: string[];                    // the named agents saved or kept, by name
  shared: boolean | null;              // null: no agent to share; false: saved here, others can't see it yet
}

/** applySetup applies exactly the reviewed change, then gives each chosen
 *  tool its named agent. A rerun or a retry after a lost answer reuses the
 *  agent saved before (same tool, name and folder) instead of making a
 *  second one, and an agent that changed or was turned off since the list
 *  was read stops the run: nothing is guessed. */
export async function applySetup(calls: SetupCalls, chosen: T.AssistantSetupHarness[], reviewID: string, catalog: T.AgentCatalogView | null | undefined, picks: ReadonlyMap<string, AgentPick>): Promise<Applied> {
  const wanted = chosen.map((h) => ({ harness: h.id, pick: { ...pickFor(picks, h, catalog) }, agent: canHaveAgent(catalog, h.id) }));
  const view = await calls.setup({ action: "apply", harnesses: chosen.map((h) => h.id), review_id: reviewID });
  let now = await calls.agents();
  const saved = new Map(picks), names: string[] = [];
  for (const w of wanted.filter((x) => x.agent)) {
    const p = w.pick, label = p.label.trim(), dir = p.dir.trim();
    const exact = p.id ? (now.agents || []).find((a) => a.record.id === p.id) : undefined;
    if (p.id && (!exact || !exact.enabled || exact.responder?.harness !== w.harness)) throw new Error("The agent you picked changed or was turned off. Check the list again.");
    const existing = exact || agentsFor(now, w.harness).find((a) => a.record.label === label && a.responder?.dir === dir);
    let agent: T.CatalogAgent | undefined;
    if (existing && existing.responder?.harness === w.harness && existing.responder?.dir === dir) agent = existing;
    else {
      const r = await calls.changeAgents(existing ? { action: "update", id: existing.record.id, harness: w.harness, dir } : { action: "create", label, harness: w.harness, dir });
      if (!r.saved || !r.agent) throw new Error("Saving the agent wasn’t confirmed. Check the list again before you retry.");
      agent = r.agent;
    }
    saved.set(w.harness, pickOf(agent));
    names.push(agent.record.label);
    now = await calls.agents();
  }
  let shared: boolean | null = null;
  if (names.length) shared = !!(await calls.changeAgents({ action: "publish" })).published;
  return { view, catalog: now, picks: saved, agents: names, shared };
}

// ---- folders ------------------------------------------------------------------

// The folder browser reads GET /api/folders (api.ts FoldersView; P1's route).
export type { FolderEntry, FoldersView };

/** folderEntries: the subfolders of v, each with its full path. A bare name
 *  joins v.path with the separator v.path already uses. */
export function folderEntries(v: FoldersView): FolderEntry[] {
  const sep = v.path.includes("\\") && !v.path.includes("/") ? "\\" : "/";
  const base = v.path.endsWith(sep) ? v.path : v.path + sep;
  return (v.dirs || []).map((d) => (typeof d === "string" ? { name: d, path: base + d } : d));
}

/** folderName: the last part of a path, for a short label ("projects"). */
export function folderName(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : path;
}
