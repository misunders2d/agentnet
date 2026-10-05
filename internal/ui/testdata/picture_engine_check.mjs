const png = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGNwS9nynxLMMGrAqAGjBgwXAwD2dV0fJCKNsQAAAABJRU5ErkJggg==";
import { WorkspaceShell } from "../static/workspaces.mjs";
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import { avatarPicture, validatePicture } from "../static/pictures.mjs";
const bytes=wire.unb64(png,"picture"),hash=await wire.pictureHash(bytes);
let checks=0;const check=(v,text)=>{assert.ok(v,text);checks++;};
let records=[],blobs=new Map(),gets=0,puts=0,mode="ok";
const fetch=async(url,o={})=>{
 const u=new URL(url);
 if(u.pathname.startsWith("/v1/pictures/")) {
  const id=u.pathname.split("/").pop();
  if(o.method==="PUT") {validatePicture(o.body);check(!!o.headers["X-Agentnet-Sig"],"authenticated upload");check(await wire.pictureHash(o.body)===id,"upload keyed by content");blobs.set(id,o.body);return new Response(null,{status:204});}
  gets++; if(mode==="offline")throw Error("offline");return new Response(mode==="forged"?bytes.map((v,i)=>i===45?v^1:v):blobs.get(id));
 }
 if(u.pathname.endsWith("/chain"))return new Response(JSON.stringify({records:records.filter(r=>r.seq>Number(u.searchParams.get("after"))).map(r=>JSON.parse(wire.rosterJSON(r))),more:false}));
 if(u.pathname==="/v1/person") {
  puts++;const next=await wire.parseRoster(o.body),prev=records.at(-1);
  if(mode==="concurrent") { mode="ok";records.push(await wire.nextRoster(keys,address,prev,prev.devices,null,"Concurrent name"));return new Response(JSON.stringify({code:"roster_stale",error:"stale"}),{status:409}); }
  await wire.verifyNext(next,prev);check(!next.picture||blobs.has(next.picture),"blob precedes signed reference");records.push(next);return new Response(null,{status:204});
 }
 throw Error("unexpected synthetic request "+url);
};
const keys=await wire.newKeys(),address="self/laptop",pub=await wire.publicEntry(keys,address);
const first=await wire.newRoster(keys,address,"Original");
const phoneKeys=await wire.newKeys(),phoneAddress="self/phone",phonePub=await wire.publicEntry(phoneKeys,phoneAddress);
const join=await wire.joinConsent(phoneKeys,phoneAddress,first.person,1,await wire.rosterHash(first));
const linked=await wire.nextRoster(keys,address,first,[pub,phonePub],join);records=[first,linked];
function engine(k,a){const e=new Engine({store:memoryStore(),base:"https://synthetic.invalid",fetch});e.keys=k;e.address=a;return e;}
const e=engine(keys,address);e.fp=await wire.fingerprint(pub);e.me={...await e.personRecord(records,"self",null),published:true};await e.store.write([{s:"kv",k:"person",v:e.me}]);
mode="concurrent";const info=await e.api("/api/person/picture",{png});
check(info.picture===hash && info.label==="Concurrent name","CAS retry retains newest name");
check(info.person===first.person && info.devices.length===2,"picture leaves identity/devices intact");
const signed=records.at(-1);const forged={...signed,picture:"f".repeat(64)};await assert.rejects(()=>wire.verifyNext(forged,records.at(-2)));checks++;
const phone=engine(phoneKeys,phoneAddress);phone.fp=await wire.fingerprint(phonePub);phone.me={...await phone.personRecord([first,linked],"self",null),published:true};await phone.personLabelHead(first.person);
check(phone.me.picture===hash,"other device follows signed picture");
const remote=await phone.personRecord(records,"pinned",null);check(remote.picture===hash,"members see same verified picture");
const url=await phone.loadPicture(hash);check(url.startsWith("blob:") && wire.b64(new Uint8Array(await (await globalThis.fetch(url)).arrayBuffer()))===png,"fetch validates bytes and produces CSP-safe URL");
const n=gets;mode="offline";check(await phone.loadPicture(hash)===url && gets===n,"cache survives offline without fetch");
phone.pictureURLs.delete(hash);await phone.store.write([{s:"kv",k:"picture:"+hash,v:"bad"}]);mode="forged";await assert.rejects(()=>phone.loadPicture(hash));checks++;
mode="ok";await e.renamePerson("Renamed");check(e.me.picture===hash,"rename preserves picture");
const removed=await phone.api("/api/person/picture",{png:""});check(!removed.picture && removed.label==="Renamed","remove preserves latest label");await e.personLabelHead(first.person);check(!e.me.picture,"removal reaches first device");
const head=records.at(-1);const next=await wire.nextRoster(keys,address,head,head.devices,null);check(!next.picture,"later roster preserves removal");
await assert.rejects(()=>wire.parseRoster({...JSON.parse(wire.rosterJSON(head)),picture:"invalid"}));checks++;
for(const bad of [new Uint8Array([1,2]),new Uint8Array(65537),bytes.slice(0,-1)]){assert.throws(()=>validatePicture(bad));checks++;}
const rectangle=bytes.slice();new DataView(rectangle.buffer).setUint32(20,8);assert.throws(()=>validatePicture(rectangle));checks++;
const good=phone.personView(remote);phone.pictureURLs.set(hash,url);
check(phone.personView(remote).picture_url===url,"verified host data supplies URL");
check(avatarPicture({people:[{person:"a",label:"Same",picture_url:"one"},{person:"b",label:"Same",picture_url:"two"}]},"Same")==="","duplicate labels never guess");
check(avatarPicture({people:[{person:"a",label:"Same",picture_url:url}]},"a")===url,"person ID selects picture");
check(!JSON.stringify(JSON.parse(wire.rosterJSON(first))).includes("picture"),"old canonical remains compatible");
const shell=new WorkspaceShell({fetch:async()=>({ok:true,json:async()=>({person:{picture_url:"/api/picture/"+hash},dms:[{peer:{picture_url:"/api/picture/"+hash}}]})})});
const binding={id:"default",name:"One",endpoint:"https://one.invalid",realm:"a".repeat(32),handle:"b".repeat(32),state:"enrolled"};
const ah=shell.register(binding);shell.register({...binding,id:"c".repeat(32),handle:"d".repeat(32)});shell.select("c".repeat(32));
const scoped=await ah.api("/api/overview");check(scoped.person.picture_url==="/workspaces/default/"+binding.handle+"/api/picture/"+hash,"picture URLs keep captured membership");check(scoped.dms[0].peer.picture_url===scoped.person.picture_url,"nested views keep captured membership");
console.log(JSON.stringify({checks,steps:records.map(r=>JSON.parse(wire.rosterJSON(r))),hash,png,puts}));
