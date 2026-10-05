import { daemonDriveProvider, boundDriveProvider } from "./drivespace.mjs";
// One shell; immutable per-membership transports. Separate stores prevent
// accidental routing, not access by fully trusted code executing this origin.
const ID = /^(default|[a-f0-9]{32})$/;
const HANDLE = /^[a-f0-9]{32}$/;
const route = (p) => { if (!p.startsWith("/api/") && p !== "/events") throw new Error("Invalid workspace API path"); return p; };
const json = async (fetcher, path, body) => {
 const r = await fetcher(path, body === undefined ? {} : {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
 if (!r.ok) {
  const text=(await r.text()).trim();let detail;
  try{detail=JSON.parse(text);}catch(_){}
  const error=new Error(detail?.error||text||r.statusText);error.status=r.status;
  if(detail?.retry_id)error.retryID=detail.retry_id;
  throw error;
 }
 return r.json();
};

export class WorkspaceShell {
 constructor({fetch:fetcher=globalThis.fetch.bind(globalThis),eventSource=(url)=>new EventSource(url)}={}) {
  this.fetch=fetcher; this.eventSource=eventSource;this.members=new Map();this.states=new Map();this.active=null;this.listeners=new Set();
 }
 register(binding, engine=null) {
  if (!ID.test(binding.id)||!HANDLE.test(binding.handle)||binding.state!=="enrolled") throw new Error("Invalid workspace binding");
  if(this.members.has(binding.id)) throw new Error("Workspace already registered");
  const entry={binding:Object.freeze({...binding}),engine,connected:true};
  this.members.set(binding.id,entry);this.states.set(binding.id,{});
  if(this.active===null)this.active=binding.id;
  return this.bind(binding.id);
 }
 async load() { const bindings=await json(this.fetch,"/api/workspaces");for(const b of bindings)if(!this.members.has(b.id))this.register(b);return this.list(); }
 // A daemon restart retires mount handles. Reacquire only this same pinned
 // membership; never update a transport held by an operation already started.
 async recoverNative(id, identity) {
  const old=this.members.get(id), b=old?.binding;
  if(this.active!==id||!old?.connected||old.engine||!identity?.fingerprint||!identity.address||identity.address!==b.address||!HANDLE.test(b.realm||""))throw new Error("Workspace identity unavailable; reconnect refused");
  const bindings=await json(this.fetch,"/api/workspaces");
  const matches=Array.isArray(bindings)?bindings.filter(x=>x.id===id):[];
  const next=matches.length===1?matches[0]:null;
  if(!next||next.state!=="enrolled"||!HANDLE.test(next.handle)||next.endpoint!==b.endpoint||next.realm!==b.realm||next.address!==b.address)throw new Error("Workspace identity changed or missing; reconnect refused");
  const overview=await json(this.fetch,"/workspaces/"+id+"/"+next.handle+"/api/overview");
  if(overview?.me?.address!==identity.address||overview?.me?.fingerprint!==identity.fingerprint)throw new Error("Workspace key changed or missing; reconnect refused");
  if(this.active!==id||this.members.get(id)!==old||!old.connected)throw new Error("Workspace changed during reconnect");
  if(next.handle===b.handle)return this.bind(id);
  const entry={binding:Object.freeze({...next}),engine:null,connected:true};
  old.connected=false;this.members.set(id,entry);
  return this.bind(id);
 }
 // list gives each binding with hub_name, the workspace's own name its admin
 // set (a browser workspace's engine keeps it; the daemon's list carries it).
 list(){return [...this.members.values()].map(e=>e.engine?Object.freeze({...e.binding,hub_name:e.engine.workspaceName||undefined}):e.binding);}
 state(id=this.active){if(!this.states.has(id))throw new Error("Unknown workspace");return this.states.get(id);}
 select(id) {const e=this.members.get(id);if(!e?.connected)throw new Error("Disconnected workspace");const previous=this.active;this.active=id;for(const f of this.listeners)f({id,previous,host:this.bind(id),state:this.state(id)});}
 onChange(fn){this.listeners.add(fn);return()=>this.listeners.delete(fn);}
 bind(id=this.active) {
  const entry=this.members.get(id);if(!entry?.connected)throw new Error("Disconnected workspace");
  const prefix="/workspaces/"+id+"/"+entry.binding.handle;
  const thisShell=this;
  const check=()=>{if(entry.blockedError)throw entry.blockedError;if(!entry.connected||this.members.get(id)!==entry)throw new Error("Stale workspace handle");};
  const call=async(p,body)=>{check();route(p);if(body?.files?.some(f=>f&&typeof f==="object"&&f.workspace!==id))throw new Error("File belongs to another workspace");return entry.engine?entry.engine.api(p,body):json(this.fetch,prefix+p,body);};
  const provider = entry.engine ? entry.engine.driveService?.() : daemonDriveProvider(call, (p, init) => {
   check(); route(p); return thisShell.fetch(prefix + p, init);
  });
  return Object.freeze({
   ...(provider ? { drive: boundDriveProvider(provider, check) } : {}),
   workspace:entry.binding, platform:entry.engine?"browser":"daemon",api:call,
   listen:(fn)=>{check();if(entry.engine)return entry.engine.listen(seq=>{if(entry.connected)fn({type:"change",seq,workspace:id});});
    const es=this.eventSource(prefix+"/events");es.addEventListener("change",e=>{if(entry.connected)fn({type:"change",seq:Number(e.data),workspace:id});});
    es.addEventListener("restart",()=>{es.close();fn({type:"restart",workspace:id});});
    es.onerror=()=>{es.close();fn({type:"disconnect",workspace:id});};return()=>es.close();
   },
   async stage(file){check();if(entry.engine)return Object.freeze({workspace:id,name:file.name,size:file.size,arrayBuffer:()=>file.arrayBuffer()});
    const r=await thisShell.fetch(prefix+"/api/upload?name="+encodeURIComponent(file.name),{method:"POST",headers:{"Content-Type":"application/octet-stream"},body:file});
    if(!r.ok)throw new Error((await r.text()).trim());return(await r.json()).id;
   },
   async file(message,index,dir){check();if(entry.engine)return call("/api/file?id="+encodeURIComponent(message)+"&i="+index+(dir?"&dir="+encodeURIComponent(dir):""));
    const r=await thisShell.fetch(prefix+"/api/files/"+encodeURIComponent(message)+"/"+index+(dir?"?dir="+encodeURIComponent(dir):""));
    if(!r.ok)throw new Error((await r.text()).trim());return {bytes:new Uint8Array(await r.arrayBuffer())};
   }
  });
  // All bound operations close over entry; selection can never retarget them.

 }
 async join(body){const b=await json(this.fetch,"/api/workspaces/join",body);return this.register(b);}
 async rename(id,name) {
  // An empty name clears this device's label: the workspace's own name shows.
  name=String(name).trim();if([...name].length>120||/[\u0000-\u001f\u007f-\u009f]/.test(name))throw new Error("Enter a readable name, up to 120 characters");
  const entry=this.members.get(id);if(!entry?.connected)throw new Error("Disconnected workspace");
  const old=entry.binding;
  const next=entry.engine?await this.renameBrowser(id,name):await json(this.fetch,"/api/workspaces/rename",{id,handle:old.handle,name});
  if(this.members.get(id)!==entry||!entry.connected||!next||next.id!==old.id||next.handle!==old.handle||next.endpoint!==old.endpoint||next.realm!==old.realm||next.address!==old.address||next.state!==old.state)throw new Error("Workspace identity changed; rename refused");
  // Keep the same entry/transport: a name change never retargets pending work,
  // reloads a conversation or discards its draft.
  entry.binding=Object.freeze({...old,name:next.name});return entry.binding;
 }
 async disconnect(id) {
  const e=this.members.get(id);if(!e?.connected)throw new Error("Disconnected workspace");
  if(e.engine)throw new Error("Browser disconnect requires its enrollment lifecycle adapter");
  await json(this.fetch,"/api/workspaces/disconnect",{id,handle:e.binding.handle});
  e.connected=false;this.members.delete(id);this.states.delete(id);
  if(this.active===id){this.active=null;const next=this.members.keys().next().value;if(next)this.select(next);}
 }
 // A native membership disconnected here keeps its keys and history; the
 // program lists it with its state and can route it again.
 async disconnected(){const all=await json(this.fetch,"/api/workspaces/all");return Array.isArray(all)?all.filter(w=>ID.test(w?.id)&&w.state==="disconnected"&&!this.members.get(w.id)?.connected):[];}
 // Reconnect binds that same membership again under a new handle: never a
 // second membership, and never one the program answers for another.
 async reconnect(id) {
  if(this.members.get(id)?.connected)throw new Error("Workspace already connected");
  const was=(await this.disconnected()).find(w=>w.id===id);if(!was)throw new Error("No disconnected workspace with that ID here");
  const next=await json(this.fetch,"/api/workspaces/reconnect",{id});
  if(next?.id!==id||next.endpoint!==was.endpoint||next.realm!==was.realm||next.address!==was.address)throw new Error("Workspace identity changed; reconnect refused");
  return this.register(next);
 }
 // Notification routes come from a locally bound registration, never payload.ws.
 openFromRegistration(boundID,conversation,open) {
  if(!this.members.get(boundID)?.connected)throw new Error("Unknown notification registration");
  this.select(boundID);open(conversation,this.bind(boundID));
 }
}

// Mount outside renderer, so desktop/mobile and installed skins share switcher.
// Renderer captures old state before selection, restores only selected state.
export function mountWorkspaceSwitcher(root,shell,{beforeSwitch=()=>{},afterSwitch=()=>{}}={}) {
 const bar=document.createElement("section");bar.className="workspace-bar";bar.setAttribute("aria-label","Workspace");
 const label=document.createElement("label"),labelText=document.createElement("span");labelText.className="workspace-label";labelText.textContent="Workspace ";label.append(labelText);
 const select=document.createElement("select");select.setAttribute("aria-label","Active workspace");
 const detail=document.createElement("span");detail.className="workspace-detail";
 const refresh=()=>{select.replaceChildren(...shell.list().map(b=>{const o=document.createElement("option");o.value=b.id;o.textContent=b.name||b.hub_name||new URL(b.endpoint).host;o.title=new URL(b.endpoint).host;return o;}));select.value=shell.active||"";const b=shell.members.get(shell.active)?.binding;detail.textContent=b?"Connected workspace":"No connected workspace";connectionText.textContent=b?new URL(b.endpoint).host+" · "+b.address:"No connection";};
 const connection=document.createElement("details"),summary=document.createElement("summary"),connectionText=document.createElement("span");summary.textContent="Connection details";connection.append(summary,connectionText);connection.className="workspace-detail";
 const rename=document.createElement("button");rename.type="button";rename.textContent="Rename";rename.className="workspace-rename";
 rename.onclick=()=>{
  const selectedID=shell.active,b=shell.members.get(selectedID)?.binding;if(!b)return;
  const dialog=document.createElement("dialog");dialog.className="workspace-name-dialog";const form=document.createElement("form"),title=document.createElement("h2"),input=document.createElement("input"),hint=document.createElement("p"),error=document.createElement("p"),actions=document.createElement("div"),save=document.createElement("button"),cancel=document.createElement("button");
  title.textContent="Workspace name";input.value=b.name||"";input.maxLength=120;input.setAttribute("aria-label","Workspace display name");hint.textContent="Your name for this workspace on this installation; leave it empty to use the workspace's own name. Identity, people, permissions and history stay unchanged.";error.setAttribute("role","alert");save.textContent="Save name";save.type="submit";cancel.textContent="Cancel";cancel.type="button";actions.className="workspace-name-actions";actions.append(cancel,save);form.append(title,hint,input,error,actions);dialog.append(form);root.append(dialog);
  const close=()=>{dialog.close();dialog.remove();rename.focus();};cancel.onclick=close;dialog.addEventListener("cancel",e=>{e.preventDefault();close();});
  form.onsubmit=async e=>{e.preventDefault();save.disabled=cancel.disabled=true;try{await shell.rename(selectedID,input.value);refresh();close();}catch(err){error.textContent=err.message;save.disabled=cancel.disabled=false;}};
  dialog.showModal();input.focus();
 };
 select.onchange=()=>{const previous=shell.active;beforeSwitch(previous,shell.state(previous));shell.select(select.value);};
 // On a phone the bar is one compact row: the workspace, and its other actions behind one toggle.
 const more=document.createElement("button");more.type="button";more.className="workspace-more-toggle";more.textContent="⋯";more.setAttribute("aria-label","Workspace options");more.setAttribute("aria-expanded","false");
 more.onclick=()=>{const open=!bar.classList.contains("open");bar.classList.toggle("open",open);more.setAttribute("aria-expanded",String(open));};
 const stop=shell.onChange(e=>{refresh();afterSwitch(e);});label.append(select);bar.append(label,more,rename,connection);root.prepend(bar);refresh();
 return {refresh,unmount(){stop();bar.remove();}};
}

// Independent store/lock names. Keep legacy default data losslessly in place.
export function browserWorkspaceNames(id) {
 if(!ID.test(id))throw new Error("Invalid workspace ID");
 return id==="default"?{database:"agentnet",lock:"agentnet-device"}:{database:"agentnet.workspace."+id,lock:"agentnet.workspace."+id};
}
// Explicit endpoint consent is required before calling this function; no
// endpoint from foreign traffic can expand the shell's allowed destinations.
export function workspaceEndpoint(endpoint,{testLoopback=false}={}) {
 const u=new URL(endpoint);
 const local=testLoopback&&u.protocol==="http:"&&["localhost","127.0.0.1","[::1]"].includes(u.hostname);
 if((u.protocol!=="https:"&&!local)||u.username||u.password||u.search||u.hash||u.pathname!=="/")throw new Error("Workspace endpoint requires an HTTPS origin");
 return u.origin;
}

// One realm-continuity gate shared by legacy and additional engines. Offline
// mounts are local; first outbound operation and every stream reconnect checks.
export function workspaceRealmFetch(fetcher,base,record,save,blocked){
 let realmChecked=false, realmFailure=null, checking=null;
    const optionsFor=o=>({...o,credentials:"omit",redirect:"error",mode:"cors"});
    const checkVersion=async response=>{
     if(!response.ok)throw new Error("Workspace identity check unavailable");
     const info=await response.clone().json();
     if(info.realm_id){
      if(!HANDLE.test(info.realm_id))realmFailure=new Error("Invalid workspace realm");
      else if(record.realm&&record.realm!==info.realm_id)realmFailure=new Error("Workspace realm changed; verify with owner");
      if(realmFailure){blocked(realmFailure);throw realmFailure;}
      record.realm=info.realm_id;save();
     }else if(record.realm){realmFailure=new Error("Workspace realm missing; verify with owner");blocked(realmFailure);throw realmFailure;}
     realmChecked=true;
 };
 return async(input,options={})=>{
     const u=new URL(input);if(u.origin!==base||!u.pathname.startsWith("/v1/"))throw new Error("Unapproved workspace request");
     if(realmFailure)throw realmFailure;
     if(u.pathname!=="/v1/version"&&(!realmChecked||u.pathname==="/v1/stream")){
      if(!checking)checking=(async()=>{const v=await fetcher(base+"/v1/version",optionsFor({method:"GET",cache:"no-store"}));await checkVersion(v);})().finally(()=>checking=null);
      await checking;
     }
     const response=await fetcher(input,optionsFor(options));
     if(u.pathname==="/v1/version"&&response.ok)await checkVersion(response);
     return response;
 };

}

// Browser enrollments use one engine/keyring/store/lock per local membership.
// Dependencies injected for deterministic fixtures; actual Engine retains all
// existing signatures, invite verification and permission behavior.
export class BrowserMemberships {
 constructor({shell,Engine,openIDB,locks,storage,fetch:fetcher,decodeInvite,newID,allowOrigin,pushFor=()=>null,testLoopback=false}) {
  Object.assign(this,{shell,Engine,openIDB,locks,storage,fetcher,decodeInvite,newID,allowOrigin,pushFor,testLoopback});this.runs=new Map();
  shell.renameBrowser=(id,name)=>this.rename(id,name);
  const raw=storage.getItem("agentnet.workspaces.v1");this.records=raw?JSON.parse(raw):[];
  if(!Array.isArray(this.records)||this.records.some(r=>!ID.test(r.id)||!HANDLE.test(r.handle)||!["enrolled","joining","disconnected"].includes(r.state)))throw new Error("Invalid browser workspace registry");
  if(new Set(this.records.map(r=>r.id)).size!==this.records.length)throw new Error("Duplicate browser workspace identity");
 }
 save(){this.storage.setItem("agentnet.workspaces.v1",JSON.stringify(this.records));}
 rename(id,name){const record=this.records.find(r=>r.id===id&&r.state==="enrolled"),entry=this.shell.members.get(id);if(!record||!entry?.engine)throw new Error("Unknown browser workspace");const old=record.name;record.name=name;try{this.save();}catch(e){record.name=old;throw e;}return {...entry.binding,name};}
 async start(record,{invite,agent}={}) {
  if(this.runs.has(record.id))throw new Error("Workspace already running");
  const base=workspaceEndpoint(record.endpoint,{testLoopback:this.testLoopback});
  await this.allowOrigin(base); // local explicit allowed-origin/CSP seam, never traffic-controlled
  const names=browserWorkspaceNames(record.id);
  let ready,failed,unlock;const started=new Promise((res,rej)=>{ready=res;failed=rej;});
  const released=new Promise(res=>unlock=res);
  const run={unlock,engine:null,finished:null};this.runs.set(record.id,run);
  run.finished=this.locks.request(names.lock,{ifAvailable:true},async lock=>{
   let store;
   try{
    if(!lock)throw new Error("Workspace already open in another window");
    store=await this.openIDB(names.database);
    const scopedFetch=workspaceRealmFetch(this.fetcher,base,record,()=>this.save(),error=>{
     run.engine?.stop();const entry=this.shell.members.get(record.id);if(entry)entry.blockedError=error;
    });
    const engine=new this.Engine({store,base,fetch:scopedFetch,push:this.pushFor(record.id)});run.engine=engine;
    const loaded=await engine.load();
    // Existing pinned local state mounts offline. Every subsequent network
    // operation is gated by endpoint version/realm verification above.
    if(!loaded){
     if(!invite)throw new Error("Workspace enrollment incomplete; retry its invitation");
     await engine.join(invite,agent);
    }
    record.state="enrolled";record.address=engine.address;this.save();
    const host=this.shell.register(record,engine);run.host=host;engine.start();ready(host);await released;
   }catch(e){failed(e);}
   finally{if(run.engine?.close)await run.engine.close();else run.engine?.stop();store?.close();this.runs.delete(record.id);}
  });run.finished.catch(failed);return started;
 }
 async join({name,invite,agent,id}) {
  const inv=this.decodeInvite(invite);
  if(inv.cert)throw new Error("Pinned certificate invitations require the native client");
  const endpoint=workspaceEndpoint(inv.hub,{testLoopback:this.testLoopback});
  let record=id?this.records.find(r=>r.id===id&&r.state==="joining"):null;
  if(id&&!record)throw new Error("Unknown joining workspace");
  if(record&&record.endpoint!==endpoint)throw new Error("Retry invite belongs to another endpoint");
  if(!record){record={id:this.newID(),handle:this.newID(),name,endpoint,state:"joining"};this.records.push(record);this.save();}
  return this.start(record,{invite,agent});
 }
 adoptDefault(engine,{name="",endpoint=engine.base,realm}={}) {
  endpoint=workspaceEndpoint(endpoint,{testLoopback:this.testLoopback});
  let record=this.records.find(r=>r.id==="default");
  if(record&&record.endpoint!==endpoint)throw new Error("Legacy workspace endpoint changed");
  if(record?.realm&&realm&&record.realm!==realm)throw new Error("Legacy workspace realm changed");
  if(!record){record={id:"default",handle:this.newID(),name,endpoint,state:"enrolled",address:engine.address,...(realm?{realm}:{})};this.records.unshift(record);this.save();}
  engine.fetch=workspaceRealmFetch(engine.fetch.bind(engine),endpoint,record,()=>this.save(),error=>{
   engine.stop();const entry=this.shell.members.get("default");if(entry)entry.blockedError=error;
  });
  if(this.shell.members.has("default"))return this.shell.bind("default");
  return this.shell.register(record,engine);
 }
 async restore(){for(const r of this.records)if(r.state==="enrolled"&&!this.shell.members.has(r.id))await this.start(r);}
 async disconnect(id) {
  const record=this.records.find(r=>r.id===id),run=this.runs.get(id);
  if(!record||!run)throw new Error("Unknown workspace");
  const entry=this.shell.members.get(id);if(entry)entry.connected=false;
  this.shell.members.delete(id);this.shell.states.delete(id);run.unlock();await run.finished;
  record.state="disconnected";this.save();
  if(this.shell.active===id){this.shell.active=null;const next=this.shell.members.keys().next().value;if(next)this.shell.select(next);}
 }
 async close(){const runs=[...this.runs.entries()];for(const [id,r] of runs){const e=this.shell.members.get(id);if(e)e.connected=false;this.shell.members.delete(id);this.shell.states.delete(id);r.unlock();}await Promise.all(runs.map(([,r])=>r.finished));if(!this.shell.members.has(this.shell.active))this.shell.active=this.shell.members.keys().next().value||null;}
}

// Every workspace registration has its own PushManager/VAPID subscription.
// Origin-wide notification permission remains shared by the browser platform.
export function workspacePush(id,{navigator:nav=globalThis.navigator,Notification:Notice=globalThis.Notification}={}) {
 if(!ID.test(id))throw new Error("Invalid push workspace");
 let registration=null;
 const keyText=bytes=>btoa(String.fromCharCode(...new Uint8Array(bytes))).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");
 const asJSON=s=>{const j=s.toJSON();return {endpoint:j.endpoint,p256dh:j.keys.p256dh,auth:j.keys.auth};};
 const adapter={
  supported:()=>!!nav?.serviceWorker&&"PushManager" in globalThis&&!!Notice,
  async subscribe(key) {
   const legacy=id==="default";
   registration=await nav.serviceWorker.register(legacy?"/sw.js":"/workspaces-sw.js?workspace="+id,legacy?{}:{scope:"/workspace-push/"+id+"/"});
   // serviceWorker.ready observes the document's controlling scope, which
   // would be the default registration. Await this exact registration.
   if(!registration.active)await new Promise((resolve,reject)=>{
    const worker=registration.installing||registration.waiting;
    if(!worker){reject(new Error("Workspace push worker unavailable"));return;}
    const check=()=>{if(worker.state==="activated")resolve();if(worker.state==="redundant")reject(new Error("Workspace push worker failed"));};
    worker.addEventListener("statechange",check);check();
   });
   let subscription=await registration.pushManager.getSubscription();
   const current=subscription?.options?.applicationServerKey;
   if(subscription&&(!current||keyText(current)!==key)){await subscription.unsubscribe();subscription=null;}
   if(!subscription)subscription=await registration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:key});
   return asJSON(subscription);
  },
  async current(key){if(Notice?.permission!=="granted")return null;return adapter.subscribe(key);},
  workspaceForEvent(event){
   // Never trust event.data.workspace or any foreign push field.
   if(!registration||event.source!==registration.active)return null;
   return id;
  }
 };
 return adapter;
}
