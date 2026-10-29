// Real editor components below the same keyed desktop/phone remount boundary.
// The Host is invented; writes can only resolve/refuse/hold fixture promises.
import { createRoot } from "react-dom/client";
import { useState } from "react";
import { Store } from "./src/store";
import { StoreContext, useWide } from "./src/context";
import { OwnedContext } from "./src/owned";
import { EditBox } from "./src/features/Message.actions";
import { AssignMessage } from "./src/features/Message.assign";
import { AnswerSheet, DeclineSheet } from "./src/features/Approvals.sheets";
import { Reports } from "./src/features/Approvals.reports";
import { ScheduleDialogs } from "./src/features/Composer.schedule";
import { DefaultAgentChoice } from "./src/features/DefaultAgent";
import { useRetainedText } from "./src/retained-text";

export function mountEditorHarness(seed: any, form: string) {
  const root = document.querySelector<HTMLElement>("#root")!;
  root.className = "an-root";
  const app = root.appendChild(document.createElement("div")), portals = root.appendChild(document.createElement("div"));
  app.className = "an-app overflow-auto";
  const o = structuredClone(seed.overview), dm = structuredClone(seed.view);
  o.me.agent = true; o.person.devices = [{ address: o.me.address, fingerprint: o.me.fingerprint }];
  dm.messages = [{id:"a".repeat(32),lid:"a".repeat(32),from:o.me.address,to:dm.peer.address,dir:"out",kind:"message",body:"Original [@Alice](agentnet:person/alice)",revision:1,at:new Date().toISOString(),can:["edit"],reactions:[]}];
  const f = (window as any).editorFixture = {
    form, o, dm, calls: [] as any[], mode: "success", pending: [] as (() => void)[], target: "a", attempt: 1, workspace: "one", mounts: 0,
    resolve() { for (const finish of this.pending.splice(0)) finish(); },
  };
  const responder = {agent_id:"1".repeat(32),chosen:true,manual:false,harness:"codex",dir:"/invented/original",ready:true,harnesses:[{name:"codex",found:true},{name:"claude",found:true}]};
  const schedule = {id:"5".repeat(32),conv:dm.id,topic:"",body:"Original scheduled body",due:new Date(Date.now()+86400000).toISOString(),rev:1,state:"pending",audience:{persons:[o.person.person],pids:[]},files:[]};
  const confirm = async (p: string, b: any) => {
    f.calls.push({p,b:structuredClone(b)});
    if (f.mode === "failure") throw Error("Invented refused response");
    if (f.mode === "hold") await new Promise<void>(r => f.pending.push(r));
    // Assign brings the own agent into a topic of the My agents room; an own agent joins at once.
    if (p === "/api/dm/agent/invite") return {pid:"8".repeat(32),conv:b.conv,topic:b.topic,host:{address:b.host},agent_id:b.agent_id,state:"active"};
    return {id:b?.id || "9".repeat(32),state:"stored",note:"Invented confirmed response"};
  };
  const stores = new Map<string, Store>();
  function getStore(workspace: string) {
    if (stores.has(workspace)) return stores.get(workspace)!;
    const host: any = {version:1,platform:"daemon",workspace:{id:workspace,name:"Invented "+workspace,state:"enrolled"},workspaces:null,
      listen:()=>()=>{},onOpen(){},onSkinsChange:()=>()=>{},skins:[],async api(p:string,b:any) {
        if (b !== undefined) return confirm(p,b);
        if (p.startsWith("/api/overview")) return structuredClone(o);
        if (p.startsWith("/api/dm?")) return structuredClone(dm);
        if (p.startsWith("/api/thread?")) return {id:"9".repeat(32),peer:o.me.address,messages:[]};
        if (p.startsWith("/api/agents")) return {host:o.me.address,local:true,agents:["1","2"].map((id,i)=>({record:{id:id.repeat(32),host:o.me.address,host_key:o.me.fingerprint,harness:i?"claude":"codex",label:i?"Claude":"Codex"},enabled:true,responder:{ready:true}}))};
        if (p.startsWith("/api/schedules")) return [{...schedule,id:f.target==="a"?schedule.id:"6".repeat(32),rev:f.attempt}];
        if (p.startsWith("/api/folders")) return {path:"/invented/selected",home:"/invented",parent:"/invented",roots:["/"],dirs:[]};
        if (p.startsWith("/api/responder")) return structuredClone(responder);
        return {};
      }};
    const store = new Store(host);
    (store as any).set({overview:o,dm,views:{[dm.id]:dm},open:{kind:"dm",id:dm.id},tab:form==="schedule"?"outbox":"chats"});
    // Refetch cannot perform extra fixture work; the actual form call and its
    // exact payload still travel through Store.run and the typed Host API.
    store.refetch = async () => {};
    stores.set(workspace,store);return store;
  }
  const ctx: any = {conv:dm.id,dm,overview:o,names:{}};
  const respond = async (text: string) => {
    try { await confirm("fixture/answer",{id:f.target,attempt:f.attempt,text}); return true; }
    catch { return false; }
  };
  let rerender: () => void;
  function Editor() {
    const [open,setOpen] = useState(false);
    // Render the same invented authoritative report that the real Store uses
    // for exact target/version validation, rather than a props-only report.
    if (form === "report") o.review = [{id:"7".repeat(32),kind:"message",peer:o.me.address,why:"",excerpt:"",at:new Date().toISOString(),report:{v:1,at:1,host:o.me.address,items:[{id:f.target.repeat(32),key:"exact-key",attempt:f.attempt,from:dm.peer.address,kind:"question",state:"needs_human",blocker:"",since:1,excerpt:"Invented missing information",actionable:true}]}}];
    const store = getStore(f.workspace);
    const m = {...dm.messages[0],id:f.target.repeat(32),revision:f.attempt};
    const key = JSON.stringify([form,f.target,f.attempt]);
    f.mounts++;
    return <StoreContext.Provider value={store}>
      <button onClick={()=>setOpen(true)}>Open editor</button>
      {form==="edit" && open && <EditBox m={m} ctx={ctx} onDone={()=>setOpen(false)}/>}
      {form==="answer" && open && <AnswerSheet draftKey={key} question="Invented exact request" onClose={()=>setOpen(false)} onSend={respond}/>}
      {form==="decline" && <DeclineSheet draftKey={key} open={open} onOpenChange={setOpen} who="Alice" kind="task" onDecline={respond}/>}
      {form==="report" && <Reports o={o} notices={o.review}/>}
      {form==="schedule" && <ScheduleDialogs pane="list" onPane={()=>{}} conv={dm.id} dm={dm} capture={()=>({body:"",draft:{text:""}})} reason="" page/>}
      {form==="assign" && <AssignMessage m={m} ctx={ctx} open={open} onOpenChange={setOpen}/>}
      {form==="default" && open && <DefaultAgentChoice view={{...responder,agent_id:f.target.repeat(32),context:[String(f.attempt)]}} inline saved={()=>setOpen(false)} onCancel={()=>setOpen(false)}/>}
      {form==="primitive" && open && <Primitive scope={key}/>}
    </StoreContext.Provider>;
  }
  function Primitive({scope}:{scope:string}) {
    const [value,setValue,retained] = useRetainedText(scope, "Original primitive");
    return <><textarea aria-label="Primitive text" value={value} onChange={e=>setValue(e.target.value)}/><button onClick={retained.discard}>Discard</button><button onClick={()=>f.pending.push(retained.capture())}>Capture confirmation</button></>;
  }
  function Boundary() {
    const wide = useWide(), [,update] = useState(0);
    rerender=()=>update(n=>n+1);
    // Different parent types, matching the App's responsive remount cause.
    return <OwnedContext.Provider value={{root,app,portals}}>{wide?<section><Editor key={f.workspace+f.target+f.attempt}/></section>:<aside><Editor key={f.workspace+f.target+f.attempt}/></aside>}</OwnedContext.Provider>;
  }
  f.change = (field: string, value: any) => { f[field]=value; rerender(); };
  createRoot(app).render(<Boundary/>);
  (window as any).ready=true;
}
