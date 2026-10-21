// Saved local configurations are management/history, never chat recipients.
// Every mutation uses the exact record selected by the person.
import { useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { AgentSheet, type AgentSetup } from "./AgentsView.forms";
import { harnessName } from "./DefaultAgent";
import { Details } from "./Settings.parts";

export function AgentSetupManager({catalog,onSaved}:{catalog:T.AgentCatalogView|null;onSaved:()=>void|Promise<void>}) {
  const store=useApp(),me=useStore(store,s=>s.overview?.me);
  const [editing,setEditing]=useState<T.CatalogAgent|null>(null),[busy,setBusy]=useState(""),[error,setError]=useState("");
  if(!me||!catalog?.local||catalog.host!==me.address)return null;
  const entries=(catalog.agents||[]).filter(a=>a.record.host===me.address&&a.record.host_key===me.fingerprint);
  if(!entries.length)return null;
  const groups=new Map<string,T.CatalogAgent[]>();
  for(const a of entries){const h=a.record.harness||"";groups.set(h,[...(groups.get(h)||[]),a]);}
  const perform=async(id:string,action:()=>Promise<T.AgentCatalogChangeResult>)=>{
    if(busy||!store.isActive())return false;
    setBusy(id);setError("");
    try{
      const result=await action();
      if(!store.isActive())return false;
      if(!result.published)setError("Saved on this computer, but sharing has not been confirmed. Check again before retrying.");
      await onSaved();
      void store.refetch();
      return true;
    }catch(e){if(store.isActive())setError(errorText(e));return false;}
    finally{if(store.isActive())setBusy("");}
  };
  const current=(a:T.CatalogAgent)=>perform(a.record.id,async()=>{
    await store.api.setResponder({agent_id:a.record.id,manual:false,harness:"",dir:""});
    return store.api.changeAgents({action:"publish"});
  });
  const selected=entries.find(a=>a.record.id===editing?.record.id);
  const harnesses=(catalog.harnesses||[]).filter(h=>h.found&&(!entries.some(a=>a.enabled&&a.record.id!==selected?.record.id&&a.record.harness===h.name)||h.name===selected?.record.harness))
    .sort((a,b)=>Number(b.name===selected?.record.harness)-Number(a.name===selected?.record.harness));
  const save=(s:AgentSetup)=>{
    if(!selected){setError("That exact saved setup is no longer available. Check again.");return Promise.resolve(false);}
    if(!harnesses.some(h=>h.name===s.harness)||!(s.dir.startsWith("/")||/^[A-Za-z]:\\/.test(s.dir))){setError("Choose an installed program and its working folder.");return Promise.resolve(false);}
    if(entries.some(a=>a.enabled&&a.record.id!==selected.record.id&&a.record.harness===s.harness)){setError("Turn off the other enabled setup for this program first, or choose its exact current setup above.");return Promise.resolve(false);}
    return perform(selected.record.id,()=>store.api.changeAgents({action:"update",id:selected.record.id,harness:s.harness,dir:s.dir}));
  };
  return <Details label="Manage saved setups">
    <p>These are retained configurations on this computer. They are not additional agent chats. Choosing an exact current setup resolves older duplicates without changing their history.</p>
    {[...groups].sort(([a],[b])=>a?b?a.localeCompare(b):-1:1).map(([h,agents])=><section key={h||"unknown"} aria-label={(h?harnessName(h):"Program not recorded")+" saved setups"} className="space-y-2 pt-2">
      <h4 className="font-semibold">{h?harnessName(h):"Program not recorded"}</h4>
      {agents.map(a=><div key={a.record.id} data-agent-setup={a.record.id} className="rounded-xl border border-outline p-3 [overflow-wrap:anywhere]">
        <p className="font-semibold">{a.record.label}{a.enabled&&a.default?" · Current":""}</p>
        <p>{a.responder?.dir||"Folder not available; choose it when enabling."}</p>
        <p>{a.enabled?a.responder?.ready?"Enabled · Ready":"Enabled · Needs attention":"Disabled"}</p>
        {a.responder?.problem&&<p className="text-danger">{a.responder.problem}</p>}
        <div className="mt-2 flex flex-wrap gap-2">
          {a.enabled&&<>
            <Button size="sm" variant="outline" disabled={!!busy||a.default&&agents.filter(x=>x.enabled).length===1} onClick={()=>void current(a)}>Use as current</Button>
            <Button size="sm" variant="ghost" disabled={!!busy} onClick={()=>void perform(a.record.id,()=>store.api.changeAgents({action:"disable",id:a.record.id}))}>Disable</Button>
          </>}
          {(!a.enabled||!a.responder?.ready)&&<Button size="sm" variant="outline" disabled={!!busy} onClick={()=>{setError("");setEditing(a);}}>Repair or enable</Button>}
        </div>
      </div>)}
    </section>)}
    {error&&<p role="alert" className="text-danger">{error}</p>}
    <AgentSheet open={!!editing} onOpenChange={open=>!open&&setEditing(null)} harnesses={harnesses} agent={selected||editing||undefined} onSave={save}/>
  </Details>;
}
