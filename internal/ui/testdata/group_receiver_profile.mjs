// Actual sealed history through Engine.onMessage and the selected storage.
import * as wire from '../static/wire.mjs';
export async function receiverProfile({world,check,realIDB,c,root,conv,keys,address,roster,pub}) {
 const w=await world(),engine=w.e,store=w.st,pending=new Set(),failures=[],saved=new Map();
 let measuring=false;
 const counters={kvScans:0,kvRows:0,disclosures:0,recoveries:0};
 const disclosure=typeof engine.discloseHumanPass==='function'?'discloseHumanPass':'discloseHumanAudience';
 for(const name of ['discloseHumanAudience','discloseHumanPass','recoverGroupIntents','runHistory','retryHeld','flushReceipts']) {
  const original=engine[name];if(typeof original!=='function')continue;saved.set(name,original);
  engine[name]=function(...args){
   if(measuring&&name===disclosure)counters.disclosures++;
   if(measuring&&name==='recoverGroupIntents')counters.recoveries++;
   const result=original.apply(this,args);
   if(result&&typeof result.then==='function'){pending.add(result);result.then(()=>pending.delete(result),e=>{pending.delete(result);failures.push(String(e));});}
   return result;
  };
 }
 const originalAll=store.all;
 store.all=async function(name){const rows=await originalAll.call(this,name);if(measuring&&name==='kv'){counters.kvScans++;counters.kvRows+=rows.length;}return rows;};
 const drain=async()=>{for(let n=0;pending.size&&n<64;n++)await Promise.allSettled([...pending]);check(!pending.size,'receiver background reaches idle');};
 const restore=()=>{measuring=false;store.all=originalAll;for(const [name,original]of saved)engine[name]=original;};
 try {
  await w.receive(c.proof);await w.receive(c.context);await drain();
  const linkedAddress='browser/receiver-source',linkedKeys=await wire.newKeys(),linkedPub=await wire.publicEntry(linkedKeys,linkedAddress),linkedFP=await wire.fingerprint(linkedPub);
  const linkedRoster=await wire.nextRoster(keys,address,roster,[...roster.devices,linkedPub],await wire.joinConsent(linkedKeys,linkedAddress,roster.person,1,await wire.rosterHash(roster)),roster.label,[...await wire.rosterHumans(roster),linkedFP]);
  await wire.verifyNext(linkedRoster,roster);
  w.extraChains.set(roster.person,[roster,linkedRoster].map(r=>JSON.parse(wire.rosterJSON(r))));
  const person=await engine.personRecord([roster,linkedRoster],'self',null);engine.me=person;
  await store.write([{s:'kv',k:'person',v:person}]);await engine.pinDevices(person);
  const session=wire.newID(),caps=await wire.newCaps(linkedKeys,linkedAddress,session,[wire.CapEnv2,wire.CapPerson,wire.CapGroup,wire.CapOwnSyncV2]);
  w.profiles.set(linkedAddress,{live:true,sessions:[session],caps:[JSON.parse(wire.capsJSON(caps))],person:JSON.parse(wire.rosterJSON(linkedRoster))});
  const packet=await engine.groupCurrent(conv),stamp=await wire.groupAdmissionHash(wire.groupMember(packet.state,roster.person).admission);
  const seal=body=>wire.seal({v:2,id:wire.newID(),lid:wire.newID(),from:linkedAddress,to:address,ts:1700000101,kind:'message',conv,root:wire.rootJSON(root),sub:'history',replica:true,body},linkedKeys,pub);
  const make=async i=>{const h={v:1,id:wire.newID(),lid:wire.newID(),from:linkedAddress,from_key:linkedFP,ts:1700000000+i,at:(1700000000+i)*1000,kind:'message',body:'Synthetic receiver history '+i,group_admission:stamp,attachments:[]};return {h,envelope:await seal(wire.historyJSON(h))};};
  const retained=[],recent=[];
  for(let i=0;i<120;i++)retained.push(await make(i));
  for(let i=0;i<57;i++)recent.push(await make(176-i));
  for(const row of retained)await w.receive(row);await drain();
  check((await engine.groupThread(conv)).messages.length===120,'receiver retains admitted originals');
  check(!failures.length,'receiver setup errors: '+failures.join('; '));
  measuring=true;const start=performance.now();
  for(const row of recent.slice(0,50))await w.receive(row);
  const firstPageMS=performance.now()-start,viewStart=performance.now(),first=await engine.groupThread(conv),firstViewMS=performance.now()-viewStart;
  check(first.messages.length===170&&recent.slice(0,50).every(({h})=>first.messages.some(m=>m.id===h.id&&m.lid===h.lid)),'recent page visible before older catch-up');
  check(recent.slice(50).every(({h})=>!first.messages.some(m=>m.lid===h.lid)),'undelivered older items not invented');
  for(const row of recent.slice(50))await w.receive(row);await drain();
  const profile={storage:realIDB?'real IndexedDB':'memory unit only',retained:120,batch:57,firstPage:50,firstPageMS,firstViewMS,convergenceMS:performance.now()-start,disclosure,...counters};measuring=false;
  const expected=[...retained,...recent],verify=async()=>{
   const rows=(await w.st.all('inbox')).filter(r=>r.conv===conv);
   check(rows.length===177&&expected.every(({h})=>rows.filter(r=>r.id===h.id&&r.lid===h.lid&&r.claimed_key===linkedFP&&r.history&&r.replica&&r.read&&r.state===''&&r.body===h.body).length===1),'exact inert originals converge once');
  };
  await verify();check(!(await store.all('held')).length,'valid receiver batch leaves no held cards');check(!failures.length,'receiver catch-up errors: '+failures.join('; '));
  for(const row of [retained[0],recent[0]]){await w.receive(row);await w.receive({envelope:await seal(wire.historyJSON(row.h))});}await drain();await verify();
  const wrong={...(await make(999)).h,group_admission:'f'.repeat(64)},bad=await seal(wire.historyJSON(wrong));await w.receive({envelope:bad});
  check(!await store.get('inbox',wrong.id)&&(await store.get('held',wire.parseEnvelope(bad).id))?.reason==='invalid','wrong group admission rejected');
  const blocked=await make(1000),pin=await store.get('pins',linkedAddress);
  try{await store.write([{s:'pins',k:linkedAddress,v:{...pin,pending:{...pin}}}]);await w.receive(blocked);check(!await store.get('inbox',blocked.h.id)&&!!await store.get('held',wire.parseEnvelope(blocked.envelope).id),'pending sender key change refused');}
  finally{await store.write([{s:'pins',k:linkedAddress,v:pin}]);}
  await drain();restore();await w.reload();await verify();return profile;
 } finally {try{await drain();}finally{restore();await w.close();}}
}
