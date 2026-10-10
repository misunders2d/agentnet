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
  platform !== "daemon" || !!(o as { device?: unknown } | null | undefined)?.device;

/** The named agent a tool gets: an existing one (id set) or a new one, and
 *  the folder it works in. */
export interface AgentPick { id: string; label: string; dir: string }

/** The default agent as GET /api/responder reports it: the program and
 *  folder used when a request names no agent. Setup only reads its folder. */
export type DefaultAgent = Pick<T.ResponderView, "chosen" | "manual" | "harness" | "dir"> | null | undefined;

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
  needs_setup: "Its connection is out of date or still points to your previous AgentNet. Choose this program and reconnect it here.",
  not_detected: "Not found on this computer.",
  unsupported: "Can’t be connected on this computer yet. Nothing was changed.",
  error: "Its settings can’t be changed safely, so nothing was changed.",
};

export const tools = (v: T.AssistantSetupView | null | undefined) => v?.harnesses || [];

/** Native sessions may check in while this window is away. Read once on
 * returning; never poll or install anything from a focus event. */
export function watchSetupFocus(doc: Document, refresh: () => void): () => void {
  const visible = () => { if (doc.visibilityState !== "hidden") refresh(); };
  doc.defaultView?.addEventListener("focus", visible);
  doc.addEventListener("visibilitychange", visible);
  return () => {
    doc.defaultView?.removeEventListener("focus", visible);
    doc.removeEventListener("visibilitychange", visible);
  };
}

/** A tool can be chosen when the server found it and can set it up safely. */
export const nativeSelectable = (h: T.AssistantSetupHarness) => h.detected && h.supported;
export const selectable = (h: T.AssistantSetupHarness, c?: T.AgentCatalogView | null) => nativeSelectable(h) || canHaveAgent(c,h.id);

/** The tools ticked when the list opens: the ones already set up, so running
 *  setup again repairs them. */
export const startChosen = (v: T.AssistantSetupView, c?: T.AgentCatalogView | null) => new Set(tools(v).filter((h) => selectable(h,c) && (h.configured || agentsFor(c, h.id).length > 0)).map((h) => h.id));

/** The person's named agents that run this tool. */
export const agentsFor = (c: T.AgentCatalogView | null | undefined, harness: string) =>
  (c?.agents || []).filter((a) => a.enabled && a.responder?.harness === harness);

/** A tool gets a named agent when the existing responder registry can run it. */
export const canHaveAgent = (c: T.AgentCatalogView | null | undefined, harness: string) =>
  (c?.harnesses || []).some((h) => h.name === harness && h.found);

/** Chat readiness comes from the named-agent catalog, never hook registration. */
export function agentStatus(h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined) {
  const agents = agentsFor(c, h.id), ready = agents.filter(a => a.responder?.ready);
  if (ready.length) return { word: "Ready", tone: "ok" as const, sentence: andList(ready.map(a => a.record.label)) + (ready.length === 1 ? " is" : " are") + " set up for chats, questions and approved tasks." };
  if (agents.length) return { word: "Needs attention", tone: "danger" as const, sentence: agents.map(a => a.record.label + ": " + (a.responder?.problem || "It cannot start on this computer.")).join(" ") };
  if (canHaveAgent(c,h.id)) return { word: "Needs setup", tone: "act" as const, sentence: "Choose a name and working folder for its agent." };
  if (!h.detected) return { word: "Not found", tone: "muted" as const, sentence: "Not installed on this computer." };
  if (!h.supported || !canHaveAgent(c, h.id)) return { word: "Unavailable", tone: "muted" as const, sentence: "An AgentNet agent cannot run with this tool here yet." };
  return { word: "Needs setup", tone: "act" as const, sentence: "Choose a name and working folder for its agent." };
}

/** sameFolder: the same folder, ignoring a trailing separator. */
const sameFolder = (a: string | undefined, b: string | undefined) => {
  const t = (p: string | undefined) => (p || "").trim().replace(/(.)[\\/]+$/, "$1");
  return !!t(a) && t(a) === t(b);
};

/** byAge: agents oldest first (by their signed record's time; the catalog's
 *  own order, which is the order they were made in, breaks ties). */
const byAge = (agents: T.CatalogAgent[]) =>
  agents.map((a, i) => ({ a, i })).sort((x, y) => (x.a.record.ts - y.a.record.ts) || (x.i - y.i)).map((x) => x.a);

/** preferredAgent: of the agents that run one tool, the one setup uses
 *  unless the person switches: the one working in the default agent's
 *  folder, else the oldest. Choosing it changes nothing about the others. */
export function preferredAgent(agents: T.CatalogAgent[], def?: DefaultAgent): T.CatalogAgent | undefined {
  const old = byAge(agents);
  const dir = def && def.chosen && !def.manual ? def.dir : "";
  return old.find((a) => sameFolder(a.responder?.dir, dir)) || old[0];
}

/** firstPick: the tool's existing agent (of several, preferredAgent), or a
 *  new one named after the tool. Nothing waits for a choice. */
export function firstPick(h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined, def?: DefaultAgent): AgentPick {
  const a = preferredAgent(agentsFor(c, h.id), def);
  return a ? pickOf(a) : { id: "", label: h.label, dir: "" };
}

export const pickOf = (a: T.CatalogAgent): AgentPick => ({ id: a.record.id, label: a.record.label, dir: a.responder?.dir || "" });

export const pickFor = (picks: ReadonlyMap<string, AgentPick>, h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined, def?: DefaultAgent) =>
  picks.get(h.id) || firstPick(h, c, def);

/** firstPicks: the pick of each tool that several agents run, fixed when the
 *  list is read, so changing the default agent afterwards does not switch
 *  it under the person. */
export function firstPicks(v: T.AssistantSetupView | null | undefined, c: T.AgentCatalogView | null | undefined, def?: DefaultAgent): Map<string, AgentPick> {
  const out = new Map<string, AgentPick>();
  for (const h of tools(v)) if (agentsFor(c, h.id).length > 1) out.set(h.id, firstPick(h, c, def));
  return out;
}

const COUNT: Record<number, string> = { 2: "Two", 3: "Three", 4: "Four", 5: "Five", 6: "Six", 7: "Seven", 8: "Eight", 9: "Nine" };
const quoted = (s: string) => "“" + s + "”";
const andList = (xs: string[]) => (xs.length < 2 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);

/** sameToolNote: when several agents run one tool, which one setup uses and
 *  that the others stay, in plain words; "" when there is only one. */
export function sameToolNote(h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined, pick: AgentPick): string {
  const mine = byAge(agentsFor(c, h.id)), using = mine.find((a) => a.record.id === pick.id);
  if (mine.length < 2 || !using) return "";
  const others = mine.filter((a) => a !== using).map((a) => quoted(a.record.label));
  return (COUNT[mine.length] || String(mine.length)) + " agents run " + h.label + " on this computer: " + andList(mine.map((a) => quoted(a.record.label))) + ". " +
    "Using " + quoted(using.record.label) + "; " + (others.length === 1 ? others[0] + " stays available with its history." : "the others stay available with their history.") + " You can switch below.";
}

/** othersKept: the tool's other agents, which setup leaves exactly as they are. */
export const othersKept = (h: T.AssistantSetupHarness, c: T.AgentCatalogView | null | undefined, pick: AgentPick) =>
  byAge(agentsFor(c, h.id)).filter((a) => a.record.id !== pick.id).map((a) => a.record.label);

/** defaultAgentState: the default agent in a few words for the top of the
 *  list. Ready is the server's own check (installed here, folder exists). */
export function defaultAgentState(v: T.ResponderView | null | undefined, program: (h: string) => string) {
  if (!v || !v.chosen) return { word: "Not set", tone: "act" as const, line: "None yet, so questions and tasks wait for you." };
  if (v.manual) return { word: "You answer", tone: "muted" as const, line: "None: you chose to answer questions and tasks yourself." };
  return { word: v.ready ? "Ready" : "Can’t start", tone: v.ready ? ("ok" as const) : ("danger" as const), line: program(v.harness || "") };
}

/** notReady says what is still missing before the changes can be reviewed,
 *  or "" when every chosen tool that gets an agent has its name and folder. */
export function notReady(chosen: T.AssistantSetupHarness[], c: T.AgentCatalogView | null | undefined, picks: ReadonlyMap<string, AgentPick>, def?: DefaultAgent): string {
  if (!chosen.length) return "Choose at least one tool.";
  for (const h of chosen) {
    if (!canHaveAgent(c, h.id)) continue;
    const p = pickFor(picks, h, c, def);
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
  responder?: () => Promise<T.ResponderView>;
}

/** A named agent the setup saved or kept. ready is the server's own check
 *  (its program is installed here and its folder exists); problem is the
 *  server's reason when it is not. */
export interface SavedAgent { label: string; ready: boolean; problem: string }

export interface Applied {
  view: T.AssistantSetupView;          // the tool list after the change
  catalog: T.AgentCatalogView;
  picks: Map<string, AgentPick>;       // each saved agent, by tool
  agents: SavedAgent[];                // the named agents saved or kept
  shared: boolean | null;              // null: no agent to share; false: saved here, others can't see it yet
}

/** savedLine: what the result says about one saved agent. "Ready" only
 *  when the server checked that it can start here; otherwise only "saved". */
export const savedLine = (a: SavedAgent) =>
  a.ready ? "Your agent " + a.label + " is ready." : "Your agent " + a.label + " is saved, but it can’t start on this computer yet.";

/** readSetup reads the tool list first, then the agent list and the default
 *  agent only where this installation can set something up (local).
 *  Elsewhere the server's own note is what the person sees, so no other
 *  call is made. A default agent that can't be read is said
 *  (responderError); it does not stop the setup. */
export async function readSetup(calls: SetupCalls): Promise<{ view: T.AssistantSetupView; catalog: T.AgentCatalogView | null; responder?: T.ResponderView | null; responderError?: string }> {
  const view = await calls.setup();
  if (!view.local) return { view, catalog: null };
  const catalog = await calls.agents();
  if (!calls.responder) return { view, catalog };
  try { return { view, catalog, responder: await calls.responder() }; }
  catch (e) { return { view, catalog, responder: null, responderError: e instanceof Error ? e.message : String(e) }; }
}

/** applySetup applies exactly the reviewed change, then gives each chosen
 *  tool its named agent. A rerun or a retry after a lost answer reuses the
 *  agent saved before (same tool, name and folder) instead of making a
 *  second one, and an agent that changed or was turned off since the list
 *  was read stops the run: nothing is guessed. The tool's other agents are
 *  never changed, turned off or merged: only the person does that, in the
 *  Agents list. */
export async function applySetup(calls: SetupCalls, chosen: T.AssistantSetupHarness[], reviewID: string, catalog: T.AgentCatalogView | null | undefined, picks: ReadonlyMap<string, AgentPick>, def?: DefaultAgent): Promise<Applied> {
  const wanted = chosen.map((h) => ({ harness: h.id, pick: { ...pickFor(picks, h, catalog, def) }, agent: canHaveAgent(catalog, h.id) }));
  const native = chosen.filter(nativeSelectable);
  const view = native.length ? await calls.setup({ action: "apply", harnesses: native.map((h) => h.id), review_id: reviewID }) : await calls.setup();
  let now = await calls.agents();
  const saved = new Map(picks), names: SavedAgent[] = [];
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
    now = await calls.agents();
    // Ready as the server sees it now, not because the save went through.
    const id = agent.record.id, seen = (now.agents || []).find((a) => a.record.id === id) || agent;
    names.push({ label: agent.record.label, ready: !!seen.responder?.ready, problem: seen.responder?.problem || "" });
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

/** parentFolder: the folder that holds path, worked out from the path alone
 *  ("" at a root). Used only when the server can't read path, so that
 *  folder still has a way up; the server checks wherever it leads. */
export function parentFolder(path: string): string {
  const sep = path.includes("\\") && !path.includes("/") ? "\\" : "/";
  let p = path;
  while (p.length > 1 && p.endsWith(sep)) p = p.slice(0, -1);
  const i = p.lastIndexOf(sep);
  if (i < 0) return "";
  const head = p.slice(0, i);
  if (/^[A-Za-z]:$/.test(head)) return head + sep;   // C:\Users -> C:\
  if (/[^\\/]/.test(head)) return head;             // an ordinary folder
  return sep === "/" && i === 0 && p.length > 1 ? "/" : "";   // /home -> /; a root has none
}

/** A place the folder browser can still go to. No path: the server's own
 *  default folder (home). */
export interface FolderStep { kind: "up" | "home" | "root"; label: string; path?: string }

/** folderWayOut: where browsing can go when a folder can't be read (it was
 *  deleted, or it is closed to this person): up from it, home, and the
 *  drives last shown. Retrying the same folder is never the only way on. */
export function folderWayOut(failed: string | undefined, last: FoldersView | null | undefined): FolderStep[] {
  const out: FolderStep[] = [];
  const up = failed ? parentFolder(failed) : "";
  if (up) out.push({ kind: "up", label: "Up", path: up });
  if (failed && failed !== last?.home) out.push({ kind: "home", label: "Home" });
  for (const r of last?.roots || []) if (r !== failed && r !== up) out.push({ kind: "root", label: r, path: r });
  return out;
}

/** folderName: the last part of a path, for a short label ("projects"). */
export function folderName(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : path;
}
