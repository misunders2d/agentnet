import assert from "node:assert/strict";
import {WorkspaceShell,browserWorkspaceNames,workspaceEndpoint} from "../static/workspaces.mjs";
const a={id:"default",name:"Same",endpoint:"https://a.example",realm:"a".repeat(32),handle:"1".repeat(32),state:"enrolled"};
const b={...a,id:"b".repeat(32),endpoint:"https://b.example",handle:"2".repeat(32)};
const calls=[];let release;
const shell=new WorkspaceShell({fetch:async(url,opts)=>{calls.push({url,opts});if(url.includes("/api/upload"))await new Promise(r=>release=r);return {ok:true,json:async()=>({id:"staged-A"}),arrayBuffer:async()=>new ArrayBuffer(2)};}});
const ah=shell.register(a),bh=shell.register(b);
shell.state(a.id).draft={body:"Private A",reply:"same-id",files:["A"]};
const staged=ah.stage({name:"file",size:2});
shell.select(b.id);
assert.deepEqual(shell.state(b.id),{});
await bh.api("/api/send",{body:"B"});
release();assert.equal(await staged,"staged-A");
await ah.api("/api/send",{body:"A",files:["staged-A"]});
assert(calls[0].url.startsWith("/workspaces/default/"+a.handle));
assert(calls[1].url.startsWith("/workspaces/"+b.id+"/"+b.handle));
assert(calls[2].url.startsWith("/workspaces/default/"+a.handle));
shell.select(a.id);assert.equal(shell.state().draft.body,"Private A");
assert.notDeepEqual(browserWorkspaceNames(a.id),browserWorkspaceNames(b.id));
assert.equal(browserWorkspaceNames(a.id).database,"agentnet");
assert.throws(()=>workspaceEndpoint("http://remote.example"));
assert.throws(()=>workspaceEndpoint("https://user:pass@a.example"));
assert.equal(workspaceEndpoint("https://a.example"),"https://a.example");
assert.throws(()=>shell.openFromRegistration("foreign","same-id",()=>{}));
await shell.disconnect(b.id);
await assert.rejects(bh.api("/api/send",{}),/Stale/);
assert.equal(shell.active,a.id);
console.log("workspace transport, drafts, staged files, endpoints and stale handles PASS");
const {BrowserMemberships,workspacePush}=await import("../static/workspaces.mjs");
const storageMap=new Map(),storage={getItem:k=>storageMap.get(k),setItem:(k,v)=>storageMap.set(k,v)};
const dbs=new Map(),locked=new Set();
const locks={async request(name,options,fn){if(locked.has(name))return fn(null);locked.add(name);try{return await fn({name});}finally{locked.delete(name);}}};
const openIDB=async name=>{if(!dbs.has(name))dbs.set(name,{});return {data:dbs.get(name),close(){}};};
class FixtureEngine {
 constructor({store,base,fetch}){Object.assign(this,{store,base,fetch});}
 async load(){this.address=this.store.data.address;return !!this.address;}
 async call(method,path){const r=await this.fetch(this.base+path,{method});return r.json();}
 async join(invite,agent){await this.call("GET","/v1/version");this.address="same/"+agent;this.store.data.address=this.address;}
 start(){this.running=true;}stop(){this.running=false;}
 async api(p,b){return {endpoint:this.base,body:b};}
}
let nextID=3;
const browserShell=new WorkspaceShell();
const args={shell:browserShell,Engine:FixtureEngine,openIDB,locks,storage,
 fetch:async(url,options)=>{assert.equal(options.credentials,"omit");assert.equal(options.redirect,"error");assert.equal(options.mode,"cors");const info={realm_id:(url.includes("a.example")?"a":"b").repeat(32)};return{ok:true,json:async()=>info,clone:()=>({json:async()=>info})};},
 decodeInvite:code=>({hub:code}),newID:()=>String(nextID++).repeat(32),allowOrigin:async origin=>assert(origin.startsWith("https://"))};
const browser=new BrowserMemberships(args);
const browserA=await browser.join({name:"Same",invite:"https://a.example",agent:"laptop"});
const browserB=await browser.join({name:"Same",invite:"https://b.example",agent:"laptop"});
assert.equal(locked.size,2);assert.equal(dbs.size,2);
assert.notEqual(browserA.workspace.realm,browserB.workspace.realm);
browserShell.select(browserB.workspace.id);
assert.equal((await browserA.api("/api/send",{body:"A"})).endpoint,"https://a.example");
const foreignFile=await browserA.stage({name:"A",size:1,arrayBuffer:async()=>new ArrayBuffer(1)});
await assert.rejects(browserB.api("/api/dm/send",{files:[foreignFile]}),/another workspace/);
await browser.close();assert.equal(locked.size,0);
const restoredShell=new WorkspaceShell();
const restored=new BrowserMemberships({...args,shell:restoredShell});
await restored.restore();assert.equal(restoredShell.list().length,2);assert.equal(locked.size,2);
await restored.disconnect(browserA.workspace.id);
await assert.rejects(restoredShell.bind(browserB.workspace.id).api("/api/send",{files:[foreignFile]}),/another workspace/);
await assert.rejects(browserA.api("/api/send",{}),/Stale/);
await restored.close();
assert.equal(locked.size,0);

// Independent registrations must never reuse/remove another VAPID subscription.
const registrations=new Map();
const nav={serviceWorker:{async register(script,options={}){
 const scope=options.scope||"/";
 if(!registrations.has(scope))registrations.set(scope,{active:{script},pushManager:{subscription:null,async getSubscription(){return this.subscription;},async subscribe(opts){
  return this.subscription={options:{applicationServerKey:Buffer.from(opts.applicationServerKey,"base64url")},toJSON:()=>({endpoint:"https://push.example/"+scope,keys:{p256dh:"pub",auth:"auth"}}),unsubscribe:async()=>{this.subscription=null;}};
 }}});
 return registrations.get(scope);
}}};
const pa=workspacePush("a".repeat(32),{navigator:nav,Notification:{permission:"granted"}});
const pb=workspacePush("b".repeat(32),{navigator:nav,Notification:{permission:"granted"}});
await pa.subscribe("AQ");await pb.subscribe("Ag");
assert.equal(registrations.size,2);
const ra=registrations.get("/workspace-push/"+"a".repeat(32)+"/"),rb=registrations.get("/workspace-push/"+"b".repeat(32)+"/");
assert.equal(pa.workspaceForEvent({source:ra.active,data:{workspace:"b".repeat(32)}}),"a".repeat(32));
assert.equal(pa.workspaceForEvent({source:rb.active}),null);
assert(ra.pushManager.subscription&&rb.pushManager.subscription);
console.log("browser independent stores/locks/restart, realm pins and VAPID registration binding PASS");

// Real worker source under a synthetic service-worker runtime: foreign ws
// payload cannot select another registration or produce a foreign URL.
const {readFile}=await import("node:fs/promises");
const vm=await import("node:vm");
const handlers={},notifications=[],posts=[],opened=[];
const ws="a".repeat(32);
const self={
 registration:{scope:"https://shell.example/workspace-push/"+ws+"/",showNotification:async(title,options)=>notifications.push(options)},
 location:{origin:"https://shell.example"},
 addEventListener:(name,fn)=>handlers[name]=fn,
 clients:{matchAll:async()=>[{url:"https://shell.example/",postMessage:msg=>posts.push(msg),focus:async()=>{}}],openWindow:async u=>opened.push(u)}
};
vm.runInNewContext(await readFile(new URL("../static/workspaces-sw.js",import.meta.url),"utf8"),{self,URL});
let waited;
handlers.push({data:{json:()=>({v:1,chan:"c".repeat(22),workspace:"b".repeat(32),url:"https://evil.example"})},waitUntil:p=>waited=p});await waited;
assert.equal(notifications[0].tag,ws+":"+"c".repeat(22));
handlers.notificationclick({notification:{data:{...notifications[0].data,workspace:"b".repeat(32)},close(){}},waitUntil:p=>waited=p});await waited;
assert.deepEqual(JSON.parse(JSON.stringify(posts)),[{type:"agentnet-workspace-open",chan:"c".repeat(22)}]);
assert.equal(opened.length,0);
console.log("real workspace worker source ignores foreign workspace/URL payload PASS");

// Reload mounts pinned local history without contacting an offline relay.
const offlineRecords=JSON.parse(storage.getItem("agentnet.workspaces.v1")).filter(r=>r.state==="enrolled");
const offlineStorage={getItem:()=>JSON.stringify(offlineRecords),setItem(){}};
let offlineCalls=0;
const offlineShell=new WorkspaceShell(),offline=new BrowserMemberships({...args,shell:offlineShell,storage:offlineStorage,fetch:async()=>{offlineCalls++;throw new TypeError("offline");}});
await offline.restore();
assert.equal(offlineShell.list().length,offlineRecords.length);
assert.equal(offlineCalls,0);
const offlineRun=offline.runs.values().next().value;
await assert.rejects(offlineRun.engine.call("GET","/v1/messages"),/offline/);
assert.equal(offlineShell.list().length,offlineRecords.length);
await offline.close();

// Proven realm mismatch is terminal for the bound transport, not offline.
const mismatchShell=new WorkspaceShell();
let targetCalls=0;
const mismatch=new BrowserMemberships({...args,shell:mismatchShell,storage:offlineStorage,fetch:async url=>{
 if(!url.endsWith("/v1/version"))targetCalls++;
 const info={realm_id:"f".repeat(32)};return{ok:true,json:async()=>info,clone:()=>({json:async()=>info})};
}});
await mismatch.restore();const mismatchRun=mismatch.runs.values().next().value;
await assert.rejects(mismatchRun.engine.call("POST","/v1/messages"),/realm changed/);
assert.equal(targetCalls,0);
assert.equal(mismatchRun.engine.running,false);
await assert.rejects(mismatchRun.host.api("/api/send",{}),/realm changed/);
await mismatch.close();
console.log("offline browser reload retained; proven realm mismatch blocks transport PASS");
// Legacy default adoption gets the same continuity gate before first stream.
const defaultShell=new WorkspaceShell(),defaultStorageMap=new Map();
const defaultStorage={getItem:k=>defaultStorageMap.get(k),setItem:(k,v)=>defaultStorageMap.set(k,v)};
let defaultOffered="a".repeat(32),defaultTargets=0;
const defaultFetch=async url=>{
 if(!url.endsWith("/v1/version"))defaultTargets++;
 const info={realm_id:defaultOffered};return{ok:true,json:async()=>info,clone:()=>({json:async()=>info})};
};
const legacyEngine=new FixtureEngine({store:{data:{address:"same/laptop"}},base:"https://a.example",fetch:defaultFetch});await legacyEngine.load();
const legacy=new BrowserMemberships({...args,shell:defaultShell,storage:defaultStorage,fetch:defaultFetch});
const legacyHost=legacy.adoptDefault(legacyEngine,{realm:"a".repeat(32)});
await legacyEngine.call("GET","/v1/stream");assert.equal(defaultTargets,1);
defaultOffered="b".repeat(32);
await assert.rejects(legacyEngine.call("GET","/v1/stream"),/realm changed/);
assert.equal(defaultTargets,1);
await assert.rejects(legacyHost.api("/api/send",{}),/realm changed/);
assert.equal(browserWorkspaceNames("default").database,"agentnet");
console.log("adopted default reconnect uses same realm gate with legacy DB intact PASS");

// A restarted native mount reacquires only the same authenticated identity.
const nativeBinding={...a,address:"alice/laptop"}, nativeKey={address:"alice/laptop",fingerprint:"pinned-key"};
const freshBinding={...nativeBinding,handle:"9".repeat(32)};
let offered=[freshBinding],offeredMe={...nativeKey},holdOverview=null,releaseOverview;
const nativeCalls=[];
const nativeShell=new WorkspaceShell({fetch:async(path)=>{
 nativeCalls.push(path);
 if(path==="/api/workspaces")return{ok:true,json:async()=>offered};
 if(path.endsWith("/api/overview")){if(holdOverview)await holdOverview;return{ok:true,json:async()=>({me:offeredMe})};}
 return{ok:false,status:409,text:async()=>"stale or disconnected workspace"};
}});
let nativeOld=nativeShell.register(nativeBinding);nativeShell.register({...b,address:"alice/laptop"});
const retained={text:"Private draft",agent:"exact-pid",agent_id:"exact-agent",kind:"task",reply:{id:"exact-reply"},typedFor:{to:"exact-pid"},files:[{name:"file",size:2,staged:"old-staged"}]};
nativeShell.state().draft=retained;
for(const bad of [{...freshBinding,realm:"f".repeat(32)},{...freshBinding,address:"other/laptop"},{...freshBinding,endpoint:"https://other.example"},null]){
 offered=bad?[bad]:[];await assert.rejects(nativeShell.recoverNative("default",nativeKey),/identity changed or missing/);assert.equal(nativeShell.bind().workspace.handle,nativeBinding.handle);
}
offered=[freshBinding,freshBinding];await assert.rejects(nativeShell.recoverNative("default",nativeKey),/identity changed or missing/);
for(const missing of [null,{}, {address:nativeKey.address}])await assert.rejects(nativeShell.recoverNative("default",missing),/identity unavailable/);
offered=[freshBinding];
for(const me of [{...nativeKey,fingerprint:"wrong"},{address:nativeKey.address},{}]){
 offeredMe=me;await assert.rejects(nativeShell.recoverNative("default",nativeKey),/key changed or missing/);assert.equal(nativeShell.bind().workspace.handle,nativeBinding.handle);
}
offeredMe={...nativeKey};
holdOverview=new Promise(r=>releaseOverview=r);
let late=nativeShell.recoverNative("default",nativeKey);await new Promise(r=>setTimeout(r,0));nativeShell.select(b.id);releaseOverview();await assert.rejects(late,/changed during reconnect/);assert.equal(nativeShell.active,b.id);assert.equal(nativeShell.members.get("default").binding.handle,nativeBinding.handle);
holdOverview=null;nativeShell.select("default");
const nativeFresh=await nativeShell.recoverNative("default",nativeKey);
assert.equal(nativeFresh.workspace.handle,freshBinding.handle);assert.equal(nativeShell.state().draft,retained);assert.equal(nativeShell.state().draft.files,retained.files);
const callsBeforeStale=nativeCalls.length;await assert.rejects(nativeOld.api("/api/send",{}),/Stale/);assert.equal(nativeCalls.length,callsBeforeStale);
// A late proof cannot overwrite an independently disconnected/rebound entry.
holdOverview=new Promise(r=>releaseOverview=r);offered=[{...freshBinding,handle:"8".repeat(32)}];late=nativeShell.recoverNative("default",nativeKey);await new Promise(r=>setTimeout(r,0));nativeShell.members.get("default").connected=false;nativeShell.members.delete("default");nativeShell.register({...freshBinding,handle:"7".repeat(32)});releaseOverview();await assert.rejects(late,/changed during reconnect/);assert.equal(nativeShell.members.get("default").binding.handle,"7".repeat(32));
console.log("native same-key generation recovery, preserved drafts/files, wrong identity and late switch/rebind refusal PASS");

// An upload already started keeps its original generation, even after refresh.
let uploadRelease;const uploadCalls=[];
const uploadShell=new WorkspaceShell({fetch:async p=>{
 uploadCalls.push(p);
 if(p.includes("/api/upload")){await new Promise(r=>uploadRelease=r);return{ok:true,json:async()=>({id:"retired-upload"})};}
 return{ok:true,json:async()=>p==="/api/workspaces"?[freshBinding]:({me:nativeKey})};
}});
const uploadOld=uploadShell.register(nativeBinding),flight=uploadOld.stage({name:"unsent",size:2});
await uploadShell.recoverNative("default",nativeKey);uploadRelease();assert.equal(await flight,"retired-upload");assert(uploadCalls[0].includes(nativeBinding.handle));await assert.rejects(uploadOld.api("/api/send",{}),/Stale/);
console.log("in-flight native upload remains bound to retired generation PASS");

// BUG-19: a disconnected native workspace is listed with its state and
// reconnects under a new handle as the same membership. A connected one, one
// not listed as disconnected, or an answer naming another membership is
// refused; a handle from before the disconnect stays stale.
{
 const c={...b,id:"c".repeat(32),endpoint:"https://c.example",handle:"3".repeat(32),address:"alice/laptop"};
 const back={...c,handle:"4".repeat(32)};
 let cState="enrolled",answer=back;const posted=[];
 const reShell=new WorkspaceShell({fetch:async(path,opts)=>{
  if(path==="/api/workspaces/all")return{ok:true,json:async()=>[{...a},{...c,handle:cState==="enrolled"?c.handle:"",state:cState}]};
  if(path==="/api/workspaces/disconnect"){cState="disconnected";return{ok:true,json:async()=>({})};}
  if(path==="/api/workspaces/reconnect"){posted.push(JSON.parse(opts.body));if(answer===back)cState="enrolled";return{ok:true,json:async()=>answer};}
  return{ok:false,status:404,text:async()=>"not found"};
 }});
 reShell.register(a);const before=reShell.register(c);
 assert.deepEqual(await reShell.disconnected(),[]);
 await assert.rejects(reShell.reconnect(c.id),/already connected/);
 await reShell.disconnect(c.id);
 assert.deepEqual((await reShell.disconnected()).map(w=>[w.id,w.name,w.address]),[[c.id,c.name,c.address]]);
 await assert.rejects(reShell.reconnect("e".repeat(32)),/No disconnected workspace/);
 answer={...back,address:"mallory/laptop"};
 await assert.rejects(reShell.reconnect(c.id),/identity changed/);assert.equal(reShell.members.has(c.id),false);
 answer=back;
 const host=await reShell.reconnect(c.id);
 assert.equal(host.workspace.id,c.id);assert.equal(host.workspace.handle,back.handle);assert.deepEqual(posted.at(-1),{id:c.id});
 assert.deepEqual(reShell.list().map(w=>w.id),[a.id,c.id]);assert.equal(reShell.active,a.id);
 await assert.rejects(before.api("/api/send",{}),/Stale/);
 assert.deepEqual(await reShell.disconnected(),[]);
 console.log("native disconnect is undone by reconnect: same membership, new handle, stale old handle PASS");
}
