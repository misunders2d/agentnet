// "Connect your coding sessions": Comic's port of the old setup
// (static/assistant-setup.mjs, MEL-528 item 11). It lists the coding tools
// the server found on this computer (Claude Code, Codex, Pi, OMP), lets the
// person choose which to connect and the named agent each one gets (a name
// and a folder chosen in a folder browser), shows exactly what will change,
// and applies only that reviewed change. Settings → Your agent shows it;
// Agents' Connect an agent wizard embeds it (start, onDone).
//
// Setting this up approves nobody, shares no history, gives no permission
// to send tasks and leaves the default agent as it is. A browser installs
// nothing: there it only says where this is done.
import { IconPlugConnected } from "@tabler/icons-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import {
  agentStatus, agentsFor, applySetup, browserDevice, canHaveAgent, NAME_MAX, pickFor, pickOf, readSetup, selectable, startChosen, STATE_SENTENCE, STATE_WORDS, tools, notReady, nativeSelectable, watchSetupFocus,
  type AgentPick, type SavedAgent, type SetupCalls, type Stage,
} from "./AssistantSetup.model";
import { FolderField } from "./AssistantSetup.folders";
import { Card, Details, Fact, Hint, input } from "./Settings.parts";

const CARD_TITLE = "Set up your agents";
const them = (n: number) => (n === 1 ? "it" : "them");

const TITLE: Record<Stage, string> = {
  home: CARD_TITLE,
  choose: "Choose your agents",
  review: "Check the changes",
  saved: "Agent setup saved",
};

/** AssistantSetup: the whole flow in one card. start: open straight at the
 *  tool list (the wizard); onDone: where Done and Back lead instead of the
 *  card's first step. */
export function AssistantSetup({ start = false, onDone }: { start?: boolean; onDone?: () => void }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const browser = browserDevice(store.host.platform, o);
  const calls: SetupCalls = useMemo(() => ({ setup: store.api.assistantSetup, agents: () => store.api.agents(), changeAgents: store.api.changeAgents }), [store]);
  const [stage, setStage] = useState<Stage>("home");
  const [view, setView] = useState<T.AssistantSetupView | null>(null);
  const [catalog, setCatalog] = useState<T.AgentCatalogView | null>(null);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [picks, setPicks] = useState<Map<string, AgentPick>>(new Map());
  const [review, setReview] = useState<T.AssistantSetupView | null>(null);
  const [done, setDone] = useState<{ agents: SavedAgent[]; shared: boolean | null } | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const alive = useRef(true);
  const heading = useRef<HTMLHeadingElement>(null);
  const moved = useRef(false);
  const statusRead = useRef(false);
  const statusGeneration = useRef(0);
  const screen = useRef({ stage, busy });
  screen.current = { stage, busy };
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    if (browser) return;
    return watchSetupFocus(document, () => {
      const ready = () => alive.current && store.isActive() && !screen.current.busy && ["choose", "saved"].includes(screen.current.stage);
      if (!ready() || statusRead.current) return;
      statusRead.current = true;
      const generation = statusGeneration.current;
      void readSetup(calls).then(({ view: v, catalog: c }) => { if (ready() && generation === statusGeneration.current) { setView(v); setCatalog(c); } }).catch((e) => {
        if (ready() && generation === statusGeneration.current) setError("Setup status could not be refreshed: " + errorText(e) + " Check again.");
      }).finally(() => { statusRead.current = false; });
    });
  }, [browser, calls, store]);
  // A new step moves focus to its title, as the old setup did (not on first show).
  useEffect(() => { if (moved.current) heading.current?.focus({ preventScroll: true }); moved.current = true; }, [stage]);

  const fail = (e: unknown) => { if (alive.current) { setBusy(""); setError(errorText(e)); } };

  const read = async () => {
    if (busy) return;
    statusGeneration.current++;
    setBusy("read"); setError("");
    try {
      const { view: v, catalog: c } = await readSetup(calls);
      if (!alive.current) return;
      setView(v); setBusy("");
      if (!v.local) return;   // this installation sets nothing up: its note is shown instead
      setCatalog(c); setPicks(new Map()); setChosen(startChosen(v, c)); setReview(null); setDone(null);
      setStage("choose");
    } catch (e) { fail(e); }
  };
  useEffect(() => { if (start && !browser) void read(); }, []);

  const list = tools(view);
  const picked = list.filter((h) => chosen.has(h.id));
  const missing = notReady(picked, catalog, picks);

  const toReview = async () => {
    if (busy) return;
    if (missing) { setError(missing); return; }
    statusGeneration.current++;
    setBusy("review"); setError("");
    try {
      const native = picked.filter(nativeSelectable);
      const r = native.length ? await calls.setup({ action: "review", harnesses: native.map((h) => h.id) }) : view;
      if (!r) return;
      if (!alive.current) return;
      setReview(r); setBusy(""); setStage("review");
    } catch (e) { fail(e); }
  };

  const apply = async () => {
    if (busy || !review) return;
    statusGeneration.current++;
    setBusy("apply"); setError("");
    try {
      const r = await applySetup(calls, picked, review.review_id || "", catalog, picks);
      if (!alive.current) return;
      setView(r.view); setCatalog(r.catalog); setPicks(r.picks); setDone({ agents: r.agents, shared: r.shared });
      setReview(null); setBusy(""); setStage("saved");
    } catch (e) {
      if (!alive.current) return;
      setReview(null); setStage("choose");
      // Part of it may be saved (a tool connected, its agent not): say so, never "nothing changed".
      if (alive.current) { setBusy(""); setError("Setup didn’t finish: " + errorText(e) + " Some of it may already be saved, so check the list again before you retry."); }
    }
  };

  const share = async () => {
    if (busy) return;
    setBusy("share"); setError("");
    try {
      const r = await calls.changeAgents({ action: "publish" });
      if (!alive.current) return;
      setDone((d) => d && { ...d, shared: !!r.published }); setBusy("");
    } catch (e) { fail(e); }
  };

  const leave = () => { setError(""); setReview(null); if (onDone) onDone(); else setStage("home"); };
  const setPick = (id: string, p: AgentPick) => setPicks((m) => new Map(m).set(id, p));
  const toggle = (id: string, on: boolean) => setChosen((s) => { const n = new Set(s); if (on) n.add(id); else n.delete(id); return n; });

  const head = (title: string) => (
    <div className="flex items-start gap-3">
      <span aria-hidden="true" className="grid size-10 shrink-0 place-items-center rounded-xl stroke bg-agent text-agent-ink"><IconPlugConnected size={22} /></span>
      <h3 ref={heading} tabIndex={-1} className="min-w-0 flex-1 pt-1 font-display text-[20px] font-bold leading-tight outline-none">{title}</h3>
    </div>
  );

  if (browser || (view && !view.local)) {
    return (
      <Card className="p-4">
        <section aria-label={CARD_TITLE} className="space-y-2">
          {head(CARD_TITLE)}
          <p className="text-[15px] text-text-2">
            {browser ? "A browser can’t look for programs or change them. Set up your Claude Code, Codex, Pi or OMP agents in the AgentNet app on the computer they run on." : view?.note || "This installation can’t look for programs or change them."}
          </p>
        </section>
      </Card>
    );
  }

  return (
    <Card className="p-4">
      <section aria-label={CARD_TITLE} aria-busy={!!busy} className="space-y-4">
        {head(TITLE[stage])}

        {stage === "home" && <>
          <p className="text-[15px] text-text-2">Set up a Claude Code, Codex, Pi or OMP agent for chats, questions and approved tasks. It uses your own settings, skills, plugins and sign-in.</p>
          {o?.me.responder && <p className="text-[15px] text-text-2">Choose {({ claude: "Claude Code", codex: "Codex", pi: "Pi" } as Record<string, string>)[o.me.responder] || o.me.responder} below to set up the program that answers for you.</p>}
          <Button variant="act" disabled={!!busy} onClick={read}>{busy === "read" ? "Looking…" : "Find them"}</Button>
        </>}

        {stage === "choose" && <>
          <p className="text-[15px] text-text-2">Their own settings, skills, plugins and sign-in stay as they are. Ready checks the installed program and folder; it does not test sign-in.</p>
          <ChooseAll catalog={catalog} list={list} chosen={chosen} disabled={!!busy} onChange={setChosen} />
          <ul className="space-y-2.5">
            {list.map((h) => (
              <li key={h.id} className="rounded-2xl stroke bg-surface">
                <label className={"flex min-h-14 items-start gap-3 p-3 " + (selectable(h, catalog) ? "cursor-pointer" : "cursor-not-allowed opacity-70")}>
                  <input type="checkbox" checked={chosen.has(h.id)} disabled={!!busy || !selectable(h, catalog)} onChange={(e) => toggle(h.id, e.target.checked)}
                    aria-label={"Set up " + h.label} className="mt-1 size-5 shrink-0 accent-[var(--an-agent-ink)]" />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-2"><span className="font-semibold">{h.label}</span><Tag tone={agentStatus(h, catalog).tone}>{agentStatus(h, catalog).word}</Tag></span>
                    <span className="block text-[14px] text-text-2">{agentStatus(h, catalog).sentence}</span>
                  </span>
                </label>
                <div className="px-3 pb-1"><NativeDetails h={h}/></div>
                {chosen.has(h.id) && <AgentChoice h={h} catalog={catalog} pick={pickFor(picks, h, catalog)} disabled={!!busy} onPick={(p) => setPick(h.id, p)} />}
              </li>
            ))}
          </ul>
          <Hint className="px-1">{picked.length} chosen. Setup gives nobody your history or permission to give tasks.</Hint>
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="ghost" disabled={!!busy} onClick={leave}>{onDone ? "Back" : "Cancel"}</Button>
            <Button variant="outline" disabled={!!busy} onClick={read}>{busy === "read" ? "Looking…" : "Check again"}</Button>
            <Button variant="act" disabled={!!busy || !picked.length} onClick={toReview}>{busy === "review" ? "Reading changes…" : "Review changes"}</Button>
          </div>
        </>}

        {stage === "review" && review && <>
          <ul className="space-y-2.5">
            {tools(review).filter((h) => chosen.has(h.id)).map((h) => (
              <li key={h.id} className="space-y-1 rounded-2xl stroke bg-surface p-3">
                <p className="font-semibold">{h.label}</p>

                <AgentLine h={h} catalog={catalog} pick={pickFor(picks, h, catalog)} />
                <NativeDetails h={h}/>

              </li>
            ))}
          </ul>
          <p className="text-[15px] text-text-2">Only these tools change. Your default agent, approvals, skills, MCP servers and history stay as they are.</p>
          {review.note && <Details><p>{review.note}</p></Details>}
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="ghost" disabled={!!busy} onClick={() => { setReview(null); setError(""); setStage("choose"); }}>Back</Button>
            <Button variant="act" disabled={!!busy} onClick={apply}>{busy === "apply" ? "Applying…" : "Apply changes"}</Button>
          </div>
        </>}

        {stage === "saved" && <>
          <ul className="space-y-2.5">
            {list.filter((h) => chosen.has(h.id)).map((h) => (
              <li key={h.id} className="space-y-1 rounded-2xl stroke bg-surface p-3">
                <p className="flex flex-wrap items-center gap-2"><span className="font-semibold">{h.label}</span><Tag tone={agentStatus(h, catalog).tone}>{agentStatus(h, catalog).word}</Tag></p>
                <p className="text-[15px] text-text-2">{agentStatus(h, catalog).sentence}</p>

                <NativeDetails h={h}/>
              </li>
            ))}
          </ul>
          {done && done.agents.length > 0 && <>
            <p className="text-[15px] text-text-2">Add {them(done.agents.length)} to a conversation to use {them(done.agents.length)} there; your approvals still apply.</p>
          </>}
          {done?.shared === false && (
            <p className="flex flex-wrap items-center gap-x-3 rounded-2xl bg-guest-bg px-3.5 py-2 text-[14px] font-semibold text-guest-ink">
              Saved here, but others can’t see {them(done.agents.length)} yet.
              <button type="button" disabled={!!busy} className="min-h-11 font-bold underline underline-offset-2" onClick={share}>{busy === "share" ? "Sharing…" : "Share again"}</button>
            </p>
          )}
          {view?.note && <Details><p>{view.note}</p></Details>}
          <div className="flex justify-end">
            <Button variant="act" onClick={leave}>Done</Button>
          </div>
        </>}

        {error && <p role="alert" className="rounded-2xl bg-danger-bg px-3.5 py-2.5 text-[15px] font-semibold text-danger">{error}</p>}
      </section>
    </Card>
  );
}

/** ChooseAll ticks every tool that can be connected (when there are two or more). */
function ChooseAll({ catalog, list, chosen, disabled, onChange }: { catalog: T.AgentCatalogView | null; list: T.AssistantSetupHarness[]; chosen: Set<string>; disabled: boolean; onChange: (s: Set<string>) => void }) {
  const ok = list.filter(h=>selectable(h,catalog));
  const box = useRef<HTMLInputElement>(null);
  const all = ok.length > 0 && ok.every((h) => chosen.has(h.id));
  const some = !all && ok.some((h) => chosen.has(h.id));
  useEffect(() => { if (box.current) box.current.indeterminate = some; }, [some]);
  if (ok.length < 2) return null;
  return (
    <label className="flex min-h-11 cursor-pointer items-center gap-3 px-3 font-semibold">
      <input ref={box} type="checkbox" checked={all} disabled={disabled} onChange={(e) => onChange(new Set(e.target.checked ? ok.map((h) => h.id) : []))}
        className="size-5 shrink-0 accent-[var(--an-agent-ink)]" />
      Choose all {ok.length} found
    </label>
  );
}

/** AgentChoice: the named agent a chosen tool gets: one of its existing
 *  agents, or a new one with a name; and the folder it works in. */
function AgentChoice({ h, catalog, pick, disabled, onPick }: {
  h: T.AssistantSetupHarness; catalog: T.AgentCatalogView | null; pick: AgentPick; disabled: boolean; onPick: (p: AgentPick) => void;
}) {
  if (!canHaveAgent(catalog, h.id)) {
    return <p className="border-t border-hairline px-3 py-2.5 text-[14px] text-text-2">An AgentNet agent cannot run with this tool here yet. Existing agents stay as they are.</p>;
  }
  const mine = agentsFor(catalog, h.id);
  const name = "setup-agent-" + h.id;
  return (
    <div className="space-y-3 border-t border-hairline p-3">
      {mine.length > 0 ? (
        <fieldset>
          <legend className="text-[14px] font-bold">Its agent</legend>
          <div className="mt-1.5 flex flex-col gap-2">
            {mine.map((a) => (
              <label key={a.record.id} className="flex min-h-12 cursor-pointer items-center gap-3 rounded-2xl stroke bg-surface px-3.5 has-[:checked]:bg-agent">
                <input type="radio" name={name} checked={pick.id === a.record.id} disabled={disabled} onChange={() => onPick(pickOf(a))} className="size-5 accent-[var(--an-agent-ink)]" />
                <span className="min-w-0 flex-1 font-semibold [overflow-wrap:anywhere]">{a.record.label}</span>
              </label>
            ))}
          </div>
          {pick.mustChoose && <Hint className="mt-1.5 px-1">You have several agents that run {h.label}. Choose the one to keep using.</Hint>}
        </fieldset>
      ) : (
        <div>
          <label htmlFor={name} className="mb-1.5 block text-[14px] font-bold">Its agent’s name</label>
          <input id={name} className={input} value={pick.label} maxLength={NAME_MAX} disabled={disabled} autoComplete="off"
            onChange={(e) => onPick({ ...pick, label: e.target.value })} />
        </div>
      )}
      <FolderField label={"Where " + (pick.label.trim() || h.label) + " works"} value={pick.dir} disabled={disabled || pick.mustChoose}
        onChange={(dir) => onPick({ ...pick, dir })}
        hint={pick.id ? "It keeps its name; only its folder changes if you choose another." : "Others can pick it in a conversation. It works there with your own setup and permissions."} />
    </div>
  );
}

/** AgentLine: what the review will do with the tool's agent, in words. */
function AgentLine({ h, catalog, pick }: { h: T.AssistantSetupHarness; catalog: T.AgentCatalogView | null; pick: AgentPick }) {
  if (!canHaveAgent(catalog, h.id)) return <p className="text-[14px] text-text-2">An AgentNet agent cannot run with this tool here yet.</p>;
  return (
    <>
      <p className="text-[14px]"><span className="font-semibold">{pick.id ? "Keeps its agent: " : "New agent: "}</span>{pick.label.trim()}</p>
      <p className="text-[14px]"><span className="font-semibold">Works in: </span><span className="font-mono text-[13px] [overflow-wrap:anywhere]">{pick.dir}</span></p>
    </>
  );
}

function NativeDetails({ h }: { h: T.AssistantSetupHarness }) {
  return <Details label="Native sessions"><p>{STATE_WORDS[h.state] || "Unknown"}: {STATE_SENTENCE[h.state] || h.note}</p><p>{h.note}</p>{h.change && <p>{h.change}</p>}{h.next && <p>{h.next}</p>}{h.target && <Fact name="Settings file">{h.target}</Fact>}</Details>;
}
