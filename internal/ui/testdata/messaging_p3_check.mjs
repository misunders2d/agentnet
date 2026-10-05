import assert from "node:assert/strict";
import {runInNewContext} from "node:vm";
import {stripTypeScriptTypes} from "node:module";
import {readFile} from "node:fs/promises";
import {Engine,memoryStore,deliveryOf} from "../static/engine.mjs";
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
console.log("PASS delivery and receipt checks");
