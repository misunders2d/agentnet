import { AgentModelReport } from "./AgentModelReport";
// Direct questions to an exact agent host. No person DM or participation is created.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type Api, type T } from "../api";
import { useAgentNames, useApp } from "../context";
import { agentHistoryThread, concreteAgentLabel, concreteAgentName, currentCatalogAgents, niceDevice, runsAgent, threadAgentHost } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export interface AgentChatTarget {
  host: string;
  agentId?: string;
  harness?:string;
  hostKey?:string;
  label: string;
  local: boolean;
  unavailable?: string;
}

function catalog(v: T.AgentCatalogView, host: string, key: string | undefined) {
  const agents = v.agents || [];
  if (!key || v.host !== host || agents.some(a => a.record?.host !== host || a.record.host_key!==key || !/^[0-9a-f]{32}$/.test(a.record?.id || "")))
    throw Error("The agent list does not match that computer.");
  return agents;
}

const ownKey=(o:T.Overview,host:string)=>host===o.me.address?o.me.fingerprint:o.person?.devices?.find(d=>d.address===host)?.fingerprint||(o.person?.address===host?o.person.fingerprint:undefined);

function currentChoices(agents:T.CatalogAgent[],local:boolean,host:string,problems:string[]) {
  const selected=currentCatalogAgents(agents,local);
  const enabled=agents.filter(a=>a.enabled&&a.record.harness);
  if(enabled.some(a=>{const same=enabled.filter(b=>b.record.harness===a.record.harness);return same.length>1&&same.filter(b=>b.default).length!==1;}))
    problems.push("Choose the current agent for each program in Agent setup on "+niceDevice(host)+".");
  return selected;
}

export async function ownAgentChatTargets(api: Api, o: T.Overview, includeLocal: boolean): Promise<{ targets: AgentChatTarget[]; problems: string[] }> {
  const targets: AgentChatTarget[] = [], problems: string[] = [];
  const devices = new Set((o.person?.devices || []).map(d => d.address));
  if (o.person?.address) devices.add(o.person.address);
  const hosts = [...devices].filter(h => h !== o.me.address && runsAgent(h, o));
  const work: Promise<AgentChatTarget[]>[] = hosts.map(async host => {
    const published = currentChoices(catalog(await api.agents(host), host,ownKey(o,host)),false,host,problems);
    if(!published.length||!published.some(a=>a.record.harness))throw Error("Update or configure the agent on " + niceDevice(host) + " to choose it.");
    return published.filter(a=>!!a.record.harness).map(a => ({host, agentId:a.record.id, harness:a.record.harness,hostKey:a.record.host_key,label:concreteAgentLabel(a.record,published), local:false}));
  });
  if (includeLocal) work.unshift((async () => {
    const v = await api.agents();
    if (!v.local) return [];
    const agents=currentChoices(catalog(v,o.me.address,o.me.fingerprint),true,o.me.address,problems);
    const local: AgentChatTarget[] = agents.map(a => ({host:o.me.address, agentId:a.record.id, harness:a.record.harness,hostKey:a.record.host_key,label:concreteAgentLabel(a.record,agents), local:true,
      unavailable:!a.enabled ? "Turned off" : !a.responder?.ready ? a.responder?.problem || "Not ready on this computer" : undefined}));
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
  if(!target.agentId)throw Error("Choose a configured agent on that computer. Your earlier conversation is kept.");
  const fresh = await api.overview();
  if (target.local) {
    if (platform !== "daemon" || target.host !== fresh.me.address) throw Error("This computer's agent is unavailable in this workspace.");
  } else {
    const addresses = new Set((fresh.person?.devices || []).map(d => d.address));
    if (fresh.person?.address) addresses.add(fresh.person.address);
    if (!addresses.has(target.host) || !runsAgent(target.host, fresh) || fresh.person?.state === "conflict")
      throw Error("That agent is no longer on a current device of yours. Nothing sent.");
  }
  if (target.agentId) {
    const v = await api.agents(target.local ? undefined : target.host);
    const selected = currentCatalogAgents(catalog(v, target.host,ownKey(fresh,target.host)),target.local).find(a => a.record.id === target.agentId);
    if (!selected || !selected.record.harness || target.harness&&selected.record.harness!==target.harness || target.hostKey&&selected.record.host_key!==target.hostKey || selected.enabled === false || (target.local && (!v.local || !selected.responder?.ready)))
      throw Error("The selected agent is unavailable. Nothing sent; no default was chosen.");
  } else if (target.local) {
    const r = await api.responder();
    if (!fresh.me.agent || !r.chosen || r.manual || !r.ready) throw Error(r.problem || "Your agent is not ready on this computer. Nothing sent.");
  }
}

export function AgentChatButton({target, onStarted}: {target: AgentChatTarget; onStarted?: () => void}) {
  const [open, setOpen] = useState(false);
  return <>
    <Button data-new-chat-choice size="sm" variant="act" disabled={!!target.unavailable} title={target.unavailable} onClick={() => setOpen(true)}>
      Chat with {target.label}
    </Button>
    <Sheet open={open} onOpenChange={setOpen} title={"Chat with " + target.label} description={"On " + niceDevice(target.host)}>
      {open && <AgentChatForm target={target} onStarted={() => {setOpen(false); onStarted?.();}} />}
    </Sheet>
  </>;
}

/** Explicitly start a new exact conversation; never bind saved history or
 * transfer its text/files to a different native session. Existing catalog
 * hydration supplies these choices, with no additional fetch or polling. */
export function AgentHistoryRecovery({thread,overview}:{thread:T.Thread;overview:T.Overview}) {
  const store=useApp(),names=useAgentNames();
  if(!agentHistoryThread(thread,names))return null;
  const host=threadAgentHost(thread),own=host===overview.me.address||overview.person?.devices?.some(d=>d.address===host);
  const targets:AgentChatTarget[]=own?Object.keys(names).filter(id=>/^[a-f0-9]{32}$/.test(id)&&names[id+"#current"]==="true"&&names[id+"#host"]===host&&names[id+"#host_key"]===ownKey(overview,host)).map(id=>({host,agentId:id,hostKey:names[id+"#host_key"],harness:names[id+"#harness"],label:concreteAgentName(id,names),local:host===overview.me.address})).filter(t=>!t.local||store.host.platform==="daemon"):[];
  return <section aria-label="Agent history" className="shrink-0 border-b border-outline bg-surface px-4 py-3">
    <p className="text-[13px] text-muted">This is saved agent history. Its original messages, draft and files stay here. Start a new conversation with a configured agent on {niceDevice(host)}.</p>
    <div className="mt-2 flex flex-wrap gap-2">{targets.length?targets.map(target=><AgentChatButton key={target.agentId} target={target}/>):<Button size="sm" variant="outline" onClick={()=>store.showTab("agents")}>Open Agents</Button>}</div>
  </section>;
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
    ownAgentChatTargets(store.api, o, includeLocal && store.host.platform === "daemon").then(v => {if (alive) setData(v);});
    return () => {alive = false;};
  }, [store, o.seq, includeLocal]);
  if (!data) return <p className="text-[13px] text-muted">Reading your agents…</p>;
  const targets = data.targets.filter(t => !query || (t.label + " " + niceDevice(t.host)).toLowerCase().includes(query.toLowerCase()));
  return <div className="space-y-2">
    {targets.map(t => <div key={t.host + "#" + (t.agentId || "")} className="flex flex-wrap items-center justify-between gap-2 rounded-xl bg-surface p-3 stroke">
      <div className="min-w-0 flex-1 basis-40"><p className="font-semibold">{t.label}</p><p className="text-[13px] text-muted">On {niceDevice(t.host)}{t.unavailable ? " · " + t.unavailable : ""}</p></div>
      <AgentChatButton target={t} onStarted={onStarted} />
      <div className="w-full"><AgentModelReport overview={o} host={t.host} agentId={t.agentId} /></div>
    </div>)}
    {data.problems.map((p,i) => <p key={i} className="text-[13px] text-muted">Agent list unavailable: {p}</p>)}
  </div>;
}
