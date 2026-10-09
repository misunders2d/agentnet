import { AgentModelReport } from "./AgentModelReport";
// Direct questions to an exact agent host. No person DM or participation is created.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type Api, type T } from "../api";
import { useApp } from "../context";
import { niceDevice, runsAgent } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export interface AgentChatTarget {
  host: string;
  agentId?: string;
  label: string;
  local: boolean;
  unavailable?: string;
}

function catalog(v: T.AgentCatalogView, host: string) {
  const agents = v.agents || [];
  if (v.host !== host || agents.some(a => a.record?.host !== host || !/^[0-9a-f]{32}$/.test(a.record?.id || "")))
    throw Error("The agent list does not match that computer.");
  return agents;
}

export async function ownAgentChatTargets(api: Api, o: T.Overview, includeLocal: boolean): Promise<{ targets: AgentChatTarget[]; problems: string[] }> {
  const targets: AgentChatTarget[] = [], problems: string[] = [];
  const devices = new Set((o.person?.devices || []).map(d => d.address));
  if (o.person?.address) devices.add(o.person.address);
  const hosts = [...devices].filter(h => h !== o.me.address && runsAgent(h, o));
  const work: Promise<AgentChatTarget[]>[] = hosts.map(async host => {
    const published = catalog(await api.agents(host), host).filter(a => a.enabled !== false);
    return published.length ? published.map(a => ({host, agentId:a.record.id, label:a.record.label, local:false}))
      : [{host, label:"Your agent", local:false}];
  });
  if (includeLocal) work.unshift((async () => {
    const [v, r] = await Promise.all([api.agents(), api.responder()]);
    if (!v.local) return [];
    const local: AgentChatTarget[] = catalog(v, o.me.address).map(a => ({host:o.me.address, agentId:a.record.id, label:a.record.label, local:true,
      unavailable:!a.enabled ? "Turned off" : !a.responder?.ready ? a.responder?.problem || "Not ready on this computer" : undefined}));
    if (o.me.agent) local.unshift({host:o.me.address, agentId:undefined, label:"Your agent", local:true,
      unavailable:r.chosen && !r.manual && r.ready ? undefined : r.problem || "Choose a ready program in Settings first"});
    return local;
  })());
  const results = await Promise.allSettled(work);
  for (const result of results) {
    if (result.status === "fulfilled") targets.push(...result.value);
    else problems.push(errorText(result.reason));
  }
  return {targets, problems};
}

/** Recheck the selected identity/capability before sending, never substitute a default. */
export async function checkAgentChatTarget(api: Api, target: AgentChatTarget, platform: string) {
  const fresh = await api.overview();
  if (target.local) {
    if (platform === "browser" || target.host !== fresh.me.address) throw Error("This computer's agent is unavailable in this workspace.");
  } else {
    const addresses = new Set((fresh.person?.devices || []).map(d => d.address));
    if (fresh.person?.address) addresses.add(fresh.person.address);
    if (!addresses.has(target.host) || !runsAgent(target.host, fresh) || fresh.person?.state === "conflict")
      throw Error("That agent is no longer on a current device of yours. Nothing sent.");
  }
  if (target.agentId) {
    const v = await api.agents(target.local ? undefined : target.host);
    const selected = catalog(v, target.host).find(a => a.record.id === target.agentId);
    if (!selected || selected.enabled === false || (target.local && (!v.local || !selected.responder?.ready)))
      throw Error("The selected agent is unavailable. Nothing sent; no default was chosen.");
  } else if (target.local) {
    const r = await api.responder();
    if (!fresh.me.agent || !r.chosen || r.manual || !r.ready) throw Error(r.problem || "Your agent is not ready on this computer. Nothing sent.");
  }
}

export function AgentChatButton({target, onStarted}: {target: AgentChatTarget; onStarted?: () => void}) {
  const [open, setOpen] = useState(false);
  return <>
    <Button size="sm" variant="act" disabled={!!target.unavailable} title={target.unavailable} onClick={() => setOpen(true)}>
      Chat with {target.label}
    </Button>
    <Sheet open={open} onOpenChange={setOpen} title={"Chat with " + target.label} description={"On " + niceDevice(target.host)}>
      {open && <AgentChatForm target={target} onStarted={() => {setOpen(false); onStarted?.();}} />}
    </Sheet>
  </>;
}

function AgentChatForm({target, onStarted}: {target: AgentChatTarget; onStarted: () => void}) {
  const store = useApp(), [text, setText] = useState(""), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const id = useRef(sendID());
  const submit = async (e: FormEvent) => {
    e.preventDefault(); if (busy || !text.trim()) return;
    setBusy(true); setError("");
    try {
      await checkAgentChatTarget(store.api, target, store.host.platform);
      const sent = await store.api.send({id:id.current, to:target.host, agent_id:target.agentId, kind:"question", body:text.trim()});
      if (!store.isActive()) return;
      onStarted(); store.showTab("chats");
      void store.refetch();
      await store.open({kind:"thread", id:sent.id, peer:target.host});
    } catch (e) { if (store.isActive()) setError(errorText(e)); }
    finally { if (store.isActive()) setBusy(false); }
  };
  return <form onSubmit={submit} className="space-y-3">
    <p className="text-[13px] text-muted">Ask this agent directly. It uses that computer’s normal setup and permissions.</p>
    <label className="block font-semibold">Your message
      <textarea rows={4} value={text} disabled={busy} onChange={e => setText(e.target.value)}
        className="mt-1 block w-full rounded-xl bg-surface p-3 stroke" placeholder={"Ask " + target.label} />
    </label>
    {error && <p role="alert" className="text-danger">{error}</p>}
    <Button variant="act" type="submit" disabled={busy || !text.trim()}>{busy ? "Sending…" : "Send question"}</Button>
  </form>;
}

/** Current own-device catalogs, independent of whether a conversation exists. */
export function OwnAgentChats({overview:o, includeLocal = true, query = "", onStarted}: {overview:T.Overview; includeLocal?:boolean; query?:string; onStarted?:()=>void}) {
  const store = useApp(), [data, setData] = useState<{targets:AgentChatTarget[]; problems:string[]} | null>(null);
  useEffect(() => {
    let alive = true;
    ownAgentChatTargets(store.api, o, includeLocal && store.host.platform !== "browser").then(v => {if (alive) setData(v);});
    return () => {alive = false;};
  }, [store, o.seq, includeLocal]);
  if (!data) return <p className="text-[13px] text-muted">Reading your agents…</p>;
  const targets = data.targets.filter(t => !query || (t.label + " " + niceDevice(t.host)).toLowerCase().includes(query.toLowerCase()));
  const reports = (o.model_reports || []).filter(r => (includeLocal || r.host !== o.me.address) && !data.targets.some(t => t.host === r.host && (t.agentId || "") === r.agent_id) && (!query || (r.model + " " + niceDevice(r.host)).toLowerCase().includes(query.toLowerCase())));
  return <div className="space-y-2">
    {targets.map(t => <div key={t.host + "#" + (t.agentId || "")} className="flex flex-wrap items-center justify-between gap-2 rounded-xl bg-surface p-3 stroke">
      <div className="min-w-0 flex-1 basis-40"><p className="font-semibold">{t.label}</p><p className="text-[13px] text-muted">On {niceDevice(t.host)}{t.unavailable ? " · " + t.unavailable : ""}</p></div>
      <AgentChatButton target={t} onStarted={onStarted} />
      <div className="w-full"><AgentModelReport overview={o} host={t.host} agentId={t.agentId} /></div>
    </div>)}
    {reports.map(r => <div key={r.host + "#" + r.agent_id} className="rounded-xl bg-surface p-3 stroke">
      <p className="font-semibold">{r.agent_id ? "Agent " + r.agent_id.slice(0, 8) : "Default agent"}</p>
      <p className="text-[13px] text-muted">On {niceDevice(r.host)}</p>
      <AgentModelReport overview={o} host={r.host} agentId={r.agent_id} />
    </div>)}
    {data.problems.map((p,i) => <p key={i} className="text-[13px] text-muted">Agent list unavailable: {p}</p>)}
  </div>;
}
