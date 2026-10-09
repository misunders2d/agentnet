import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';
import * as ws from '../static/workspaces.mjs';
const source=(await readFile(new URL('../static/device.mjs',import.meta.url),'utf8')).replace(/^import .*;$/gm,'').replace(/main\(\)\.catch\([\s\S]*$/,'');
const home='https://home.example',other='https://other.example',realm='a'.repeat(32),id='b'.repeat(32);
const pause=ms=>new Promise(r=>setTimeout(r,ms));
let releaseHEAD,headStarted,streamCalls=0,changes=0,loaded=false,registered=false,opened=[],texts=[],fetchCalls=0;
const entered=new Promise(r=>headStarted=r),headGate=new Promise(r=>releaseHEAD=r);
const records=[{id:'default',handle:'1'.repeat(32),endpoint:home,state:'enrolled',realm,address:'me/phone'},
 {id,handle:'2'.repeat(32),endpoint:other,state:'enrolled',address:'me/phone'}];
const values=new Map([['agentnet.workspaces.v1',JSON.stringify(records)],['agentnet.workspaces.origins.v1',JSON.stringify([other])]]);
const localStorage={getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v)};
const element=()=>({hidden:false,replaceChildren(){},append(){},prepend(){},before(){},setAttribute(){},addEventListener(){}});
const skin=element(),body=element();
const document={body,getElementById:()=>skin,createElement:()=>element(),createTextNode:t=>(texts.push(t),t),head:{append(s){loaded=true;s.onload();}}};
const fetch=async(url,options)=>{
 fetchCalls++;
 if(url==='/device'){headStarted();await headGate;return new Response('',{headers:{'content-security-policy':"connect-src 'self' "+other}});}
 if(url===home+'/v1/version')return new Response(JSON.stringify({realm_id:realm}));
 if(url===home+'/v1/stream'){streamCalls++;return new Response('');}
 throw new Error('Unexpected remote request '+url);
};
class Engine {
 constructor(options){Object.assign(this,options);this.address='me/phone';}
 async load(){return true;}
 start(){this.running=true;registered=true;}stop(){}async close(){}api(){return Promise.resolve({cached:'chat'});}listen(){return()=>{};}driveService(){}
}
const window={addEventListener(){},agentnetOpen:chan=>opened.push(chan)};
const context={ws,Engine,openIDB:async()=>({close(){}}),decodeInvite:()=>{},newID:()=> '3'.repeat(32),validName:()=>true,
 localStorage,fetch,document,window,navigator:{locks:{request:async(n,o,fn)=>fn({})}},location:{origin:home,pathname:'/device'},
 appBanner:async()=>null,installOffer:()=>null,URL,AbortController,DOMException,setTimeout,clearTimeout,console};
vm.runInNewContext(source+'\npanel=document.body; globalThis.bootstrap=start; globalThis.setup=workspaces; globalThis.allow=allowOrigin; globalThis.pending=p=>pendingWorkspaceOpen=p; globalThis.route=openWorkspaceRoute;',context);
const engine=new Engine({base:home,fetch});
engine.start=function(){this.running=true;this.stream=this.fetch(home+'/v1/stream').catch(e=>{this.error=e;});};
engine.changed=()=>changes++;
const starting=context.bootstrap(engine);
await Promise.race([starting,pause(150).then(()=>{throw Error('cached default view waited for secondary CSP HEAD');})]);
assert(loaded&&engine.running,'default engine and loader are available');
assert.equal(window.agentnetWorkspaces.shell.active,'default');
await engine.stream;assert.equal(streamCalls,1,'realm guard validated before default stream');
await entered;assert(!registered,'secondary must wait for CSP');
assert.equal((await window.agentnetEngine.api('/api/overview')).cached,'chat');
releaseHEAD();for(let i=0;i<50&&!registered;i++)await pause(5);
assert(registered,'cached secondary restores after CSP');assert.equal(changes,1,'one existing engine notification refreshes membership view');
assert.equal(window.agentnetWorkspaces.shell.active,'default','restoration does not select or remount');
await window.agentnetWorkspaces.disconnect(id);
// A malformed unrelated membership does not disable the pinned default.
values.set('agentnet.workspaces.v1',JSON.stringify([records[0],{id:'broken-secondary'}]));
const recovered=new Engine({base:home,fetch});
const recovery=await context.setup(recovered);assert.equal(recovery.problems.length,1);
await recovered.fetch(home+'/v1/stream');assert.equal(streamCalls,2);
assert.equal(window.agentnetWorkspaces.shell.bind().workspace.realm,realm);
// A legacy default with no saved address still binds its unchanged engine.
const legacy={...records[0]};delete legacy.address;
values.set('agentnet.workspaces.v1',JSON.stringify([legacy,{id:'broken-secondary'}]));
await context.setup(new Engine({base:home,fetch}));assert.equal(window.agentnetWorkspaces.shell.active,'default');
// The same recovery must not waive a changed/default-invalid binding.
for(const bad of [{...records[0],endpoint:other},{...records[0],realm:'broken'},{...records[0],address:'someone/else'},records[0]]){
 const invalid=bad===records[0]?[records[0],records[0]]:[bad,{id:'broken-secondary'}];
 values.set('agentnet.workspaces.v1',JSON.stringify(invalid));
 await assert.rejects(context.setup(new Engine({base:home,fetch})),/Invalid|Duplicate|changed/);
}
// Fatal setup refusal shows the existing startup card, without a stream or loader.
values.set('agentnet.workspaces.v1',JSON.stringify([{...records[0],endpoint:other}]));
const refused=new Engine({base:home,fetch});loaded=false;body.hidden=false;const beforeRefusal=fetchCalls;
await context.bootstrap(refused);assert(!refused.running);assert(!loaded);assert(!body.hidden);assert.equal(fetchCalls,beforeRefusal);assert(texts.some(t=>t.includes("AgentNet could not start here")),"visible existing startup error card");
// A pending notification stays bound to its exact secondary, never default.
values.set('agentnet.workspaces.v1',JSON.stringify([records[0]]));
await context.setup(new Engine({base:home,fetch}));
context.pending({id,chan:'secondary-chat'});context.route();assert.equal(opened.length,0);
window.agentnetWorkspaces.shell.register(records[1],new Engine({base:other,fetch}));context.route();
assert.deepEqual(opened,['secondary-chat']);assert.equal(window.agentnetWorkspaces.shell.active,id);
// Explicit consent precedes even the page's CSP probe.
let probes=0,cspSignal;
context.fetch=async(u,o)=>{probes++;cspSignal=o.signal;await new Promise(()=>{});};
values.set('agentnet.workspaces.origins.v1','[]');
await assert.rejects(context.allow(other),/has not joined/);assert.equal(probes,0);
values.set('agentnet.workspaces.origins.v1',JSON.stringify([other]));
context.ws={...ws,workspaceProbe:(f,u,o)=>ws.workspaceProbe(f,u,o,undefined,{timeout:15})};
await assert.rejects(context.allow(other),/policy could not be checked.*timed out/);assert(cspSignal.aborted);
context.fetch=async()=>new Response('',{headers:{'content-security-policy':"connect-src 'self'"}});
await assert.rejects(context.allow(other),/may not connect/);
console.log('device cached default before offline-secondary HEAD; guarded stream/CSP/consent, corrupt-secondary recovery, exact notification routing, no restoration selection/remount PASS');
