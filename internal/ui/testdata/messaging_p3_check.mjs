import assert from "node:assert/strict";
import {runInNewContext} from "node:vm";
import {stripTypeScriptTypes} from "node:module";
import {readFile} from "node:fs/promises";
import {Engine,memoryStore,deliveryOf,outText} from "../static/engine.mjs";
const cases=JSON.parse(await readFile(new URL("delivery_vectors.json",import.meta.url)));
for(const v of cases)assert.equal(deliveryOf(v.copies),v.want,v.name);
const store=memoryStore(),e=new Engine({store,base:"https://synthetic.invalid",fetch:()=>{throw Error("No network");}}),id="a".repeat(32);
await store.write([{s:"outbox",k:id,v:{id,state:"custody",body:"hello"}}]);
await e.dispatch("receipt",JSON.stringify({id,state:"delivered",seq:1}));
assert.equal((await store.get("outbox",id)).state,"delivered");
await e.receiverOutboxProgress({id,state:"custody",body:"hello"},true);
assert.equal((await store.get("outbox",id)).state,"delivered","late POST response cannot undo pushed delivery");
for(const state of ["waiting","failed","not_delivered","delivered"]){
 await store.write([{s:"outbox",k:id,v:{id,state}}]);
 await e.dispatch("receipt",JSON.stringify({id,state:"expired",seq:2}));
 assert.equal((await store.get("outbox",id)).state,state);
}
await store.write([{s:"outbox",k:id,v:{id,state:"quarantined"}}]);
await e.dispatch("receipt",JSON.stringify({id,state:"delivered",seq:3}));
assert.equal((await store.get("outbox",id)).state,"delivered");
await e.dispatch("receipt",JSON.stringify({id:"b".repeat(32),state:"delivered",seq:9}));
assert.equal(await store.get("kv","receipt-cursor"),9);
for(const bad of ["no JSON",JSON.stringify({id,state:"done",seq:10}),JSON.stringify({id,state:"expired",seq:0}),JSON.stringify({id,state:"expired",seq:10,extra:true})]) await e.dispatch("receipt",bad);
assert.equal(await store.get("kv","receipt-cursor"),9,"invalid events do not advance the cursor");
let flushed = 0;
e.refreshTyping = async () => {}; e.fillListed = async () => {}; e.retryHeld = async () => {};
e.flushOutbox = async () => { flushed++; };
await e.dispatch("members", JSON.stringify({members:[]}));
assert.equal(flushed,1,"updated signed caps/member event retries waiting copies before any ping");
// Execute each shipped selector: a waiting original member is never confused
// with the guest host, and an ambiguous display label grants no DM target.
const me={person:"me",label:"Self",state:"self",address:"self/desk"},bob={person:"bob",label:"Bob",address:"bob/desk"},carol={person:"carol",label:"Carol",address:"carol/phone"};
for(const skin of ["classic","zoom","comic"]) {
  let select;
  if(skin==="comic") {
    const source=await readFile(new URL("../web/src/features/RoomPanel.model.ts",import.meta.url),"utf8");
    const fn=source.match(/export function guestUpdatePeople[\s\S]*?\n}/)[0].replace("export ","");
    select=runInNewContext("("+stripTypeScriptTypes(fn)+")");
  } else {
    const source=await readFile(new URL("../skins/"+skin+"/src/entry.mjs",import.meta.url),"utf8");
    const fn=source.match(/function guestUpdateTargets\(g,t\) {[\s\S]*?\n}/)[0];
    const get=runInNewContext("("+fn+")",{state:{overview:{person:me}}});
    select=(waiting,people)=>get({host:people[0],needs_update:waiting},{peer:people[1],members:people.slice(2)});
  }
  const result=select(["Bob"],[carol,bob,me],"me");assert.equal(result.length,1,skin);assert.equal(result[0].address,"bob/desk",skin);
  assert.equal(select(["Self"],[carol,bob,me],"me").length,0,skin+" never opens a DM to self");
  assert.equal(select(["Bob"],[carol,bob,{...bob,person:"other",address:"other/phone"}],"me").length,0,skin+" ambiguous name");
}
// Restore replay lowers the cursor, so subsequent disconnected receipts are replayed.
await e.dispatch("receipt",JSON.stringify({id:"b".repeat(32),state:"delivered",seq:1}));
assert.equal(await store.get("kv","receipt-cursor"),1);
for(const terminal of ["delivered","expired","quarantined"]) {
  await store.write([{s:"outbox",k:id,v:{id,state:terminal}}]);
  for(const stale of ["queued","custody","waiting","failed","not_delivered","quarantined","expired"]) {
    await e.receiverOutboxProgress({id,state:stale});
    assert.equal((await store.get("outbox",id)).state,terminal,terminal+" cannot become "+stale);
  }
}
const waits=JSON.parse(await readFile(new URL("waiting_vectors.json",import.meta.url)));
for(const v of waits)assert.equal(outText("waiting",v.peer,v.detail),v.want,v.name);
// Scope checks keep signed content checks while excluding transport progress.
await store.write([{s:"outbox",k:id,v:{id,conv:"scope",state:"custody",detail:"",body:"SIGNED"}}]);
const scope=[];await e.authorityRows({conv:"scope"},scope);
await e.dispatch("receipt",JSON.stringify({id,state:"delivered",seq:2}));
await store.write([{s:"kv",k:"test-result",v:true}],scope);
await store.write([{s:"outbox",k:id,v:{...await store.get("outbox",id),body:"ALTERED"}}]);
await assert.rejects(()=>store.write([{s:"kv",k:"test-result",v:false}],scope),/storage changed/);
for(const skin of ["classic","zoom"]) {
 const source=await readFile(new URL("../skins/"+skin+"/src/entry.mjs",import.meta.url),"utf8");
 const fn=source.match(/function deliveryText\(m\) {[\s\S]*?\n}/)[0];
 const words=source.match(/const copyWord = [^\n]+/)[0];
 const text=runInNewContext(words+";("+fn+")");
 assert.equal(text({delivery:"expired"}),"not delivered: that session ended first",skin);
 assert.equal(text({delivery:"quarantined"}),"they could not verify it",skin);
 assert.equal(text({delivery:"waiting",state_text:waits[0].want}),waits[0].want,skin);
}
for(const skin of ["classic","zoom","comic"]) {
 const source=await readFile(new URL(skin==="comic"?"../web/src/features/RoomPanel.model.ts":"../skins/"+skin+"/src/entry.mjs",import.meta.url),"utf8");
 let fn=source.match(/(?:export )?function guestUpdateDraft\(member[^)]*\) {[\s\S]*?\n}/)[0].replace("export ","");
 if(skin==="comic")fn=stripTypeScriptTypes(fn);
 const draft=runInNewContext("("+fn+")");
 assert.match(draft(false),/bring you into a chat/);assert.match(draft(true),/Our chat needs it for a guest/);
 for(const member of [false,true])assert.match(draft(member),/https:\/\/github.com\/misunders2d\/agentnet\/releases/,skin+" always includes update link");
}
console.log("PASS delivery and receipt checks");

// Snapshot completion proves local queueing, never receipt by the other device.
for(const skin of ["classic","zoom","comic"]) {
 const source=await readFile(new URL(skin==="comic"?"../web/src/features/Settings.profile.tsx":"../skins/"+skin+"/src/entry.mjs",import.meta.url),"utf8");
 const raw=source.match(skin==="comic"?/function copyWords[\s\S]*?\n}/:/function historyLine[\s\S]*?\n}/)[0];
 const words=runInNewContext("("+(skin==="comic"?stripTypeScriptTypes(raw):raw)+")",{state:{overview:{device:{}}}});
 assert.equal(words({state:"done",name:"phone",done:4,total:4}),"History queued · keep AgentNet running here",skin+" queued is not delivered");
 assert(words({state:"running",name:"phone",done:0,total:0}).startsWith("Getting your chats…"),skin+" unknown deferred progress stays unfinished");
 assert(/stopped/.test(words({state:"ended",name:"phone",done:4,total:4})),skin+" ended stays stopped");
}
