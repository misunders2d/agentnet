// One unresolved explicit Save per chat. This shares the existing workspace
// renderer state; it never schedules work, retries, fetches or activates a host.
import type { Host } from "./host";
import { skin as brand } from "./skin/brand";
import type { ScheduleAttempt } from "./features/Composer.schedule-model";
export interface ScheduleToken {id:string;conv:string;files:{name:string;size:number}[]}
export interface ScheduleSave extends ScheduleToken {
  owner?:string; attempt?:ScheduleAttempt; status:"saving"|"uncertain"|"refused"|"confirmed"; durable:boolean; note?:string;
}
type Shared = {handle?:string;slots:Map<string,ScheduleSave>;changed:(()=>void)|null};
const holders = new WeakMap<Host,Shared>(), key = Symbol.for("agentnet.ui.scheduleSaves");
const validID = (id:unknown) => typeof id === "string" && /^[a-f0-9]{32}$/.test(id);
const validConv = (id:unknown) => typeof id === "string" && /^[a-f0-9]{64}$/.test(id);

export function scheduleSaves(host:Host, changed:()=>void) {
  const owner=brand.storage;
  const workspace=host.workspaces?.state?.(host.workspace.id) as Record<PropertyKey,unknown> | undefined, storageKey="agentnet.messenger.schedule-save."+host.workspace.id;
  function tokens():Record<string,ScheduleToken> {
    try {
      const raw=JSON.parse(localStorage.getItem(storageKey) || "null"), out:Record<string,ScheduleToken>={};
      if(raw?.v!==1 || !raw.conv || typeof raw.conv!=="object" || Array.isArray(raw.conv))return out;
      for(const [conv,value] of Object.entries(raw.conv)) {
        const t=value as ScheduleToken;
        if(!validConv(conv)||!t||t.conv!==conv||!validID(t.id)||Object.keys(t).some(k=>!["id","conv","files"].includes(k))||!Array.isArray(t.files)||t.files.length>8||t.files.some(f=>!f||typeof f.name!=="string"||!Number.isSafeInteger(f.size)||f.size<0||Object.keys(f).some(k=>!["name","size"].includes(k))))continue;
        out[conv]={id:t.id,conv,files:t.files.map(f=>({name:f.name,size:f.size}))};
      }
      return out;
    }catch{return {};}
  }
  function persist(token:ScheduleToken, remove=false) {
    try {
      const all=tokens();
      if(remove){if(all[token.conv]?.id!==token.id)return true;delete all[token.conv];}
      else all[token.conv]={id:token.id,conv:token.conv,files:token.files.map(f=>({name:f.name,size:f.size}))};
      if(Object.keys(all).length)localStorage.setItem(storageKey,JSON.stringify({v:1,conv:all}));else localStorage.removeItem(storageKey);
      return true;
    }catch{return false;}
  }
  let shared = (workspace ? workspace[key] : holders.get(host)) as Shared | undefined;
  if(!shared || shared.handle!==host.workspace.handle) {
    shared={handle:host.workspace.handle,slots:new Map(Object.entries(tokens()).map(([conv,t])=>[conv,{...t,status:"uncertain",durable:true,note:"A schedule save before reload was not confirmed. The original save contents are no longer in memory; check saved state or explicitly forget this intent."}])),changed};
    if(workspace)workspace[key]=shared;else holders.set(host,shared);
  }
  const ledger=shared;
  ledger.changed=changed;
  const current=()=>host.workspace.handle===ledger.handle && (workspace ? workspace[key]===ledger : holders.get(host)===ledger);
  const notify=()=>{if(current())ledger.changed?.();};
  return {
    snapshot:()=>Object.fromEntries(ledger.slots),
    begin(conv:string,attempt:ScheduleAttempt) {
      if(!current())throw Error("This workspace handle changed. Your draft is kept.");
      const id=attempt.payload.id!;
      if(!validID(id)||!validConv(conv)||attempt.payload.conv!==conv)throw Error("Choose an exact chat and schedule ID.");
      const prior=ledger.slots.get(conv);
      if(prior && (prior.status==="saving" || prior.status==="confirmed" && prior.owner!==owner || prior.status==="uncertain" && (!prior.attempt || prior.id!==id || prior.attempt!==attempt)))throw Error("Resolve the original unconfirmed save before starting another schedule.");
      const token={id,conv,files:attempt.files.map(f=>({name:f.name,size:f.size}))};
      const durable=persist(token);ledger.slots.set(conv,{...token,owner:prior?.attempt===attempt?prior.owner:owner,attempt,status:"saving",durable});notify();
    },
    settle(conv:string,id:string,status:ScheduleSave["status"],note?:string) {
      const slot=ledger.slots.get(conv);if(!slot || slot.id!==id)return;
      if(current() && (status==="confirmed"||status==="refused"))persist(slot,true);
      ledger.slots.set(conv,{...slot,status,note});notify();
    },
    forget(conv:string,id:string) {const slot=ledger.slots.get(conv);if(!slot||slot.id!==id||!current())return;persist(slot,true);ledger.slots.delete(conv);notify();},
    takeConfirmed() {const out:ScheduleSave[]=[];for(const [conv,slot] of ledger.slots)if(slot.status==="confirmed"&&slot.attempt&&slot.owner===owner){out.push(slot);ledger.slots.delete(conv);}return out;},
    dispose(){if(ledger.changed===changed)ledger.changed=null;},
  };
}
