// Inert personal device-thread history. Uses the engine's verified own-person
// authority, transactional store, durable outbox and existing history wakes.
import * as wire from "./wire.mjs";

export const directHistoryRow=r=>!!r&&!r.conv&&!r.local&&Number.isFinite(r.at)&&[1,3].includes(r.v)&&(!r.aside||r.control)&&["",wire.SubStatus,wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(r.sub||"")&&(!r.receiver_route||r.receiver_route.op==="request");
const prefix="device-history/",page=50;
const fileEqual=(a,b)=>["lid","author","hash","index","name","size","sha256"].every(k=>a[k]===b[k]);

export function deviceHistory(e,Hold){
 const fail=(text,reason="invalid")=>{throw new Hold(reason,"Device history: "+text);};
 const proofs=new Map(),sweeps=new Map();
 const read=(checks,s,k)=>e.groupRead(checks,s,k);
 async function authority(w,env,pin,checks){try{return await e.readSyncAuthority(w,env.from,pin.fingerprint,e.address,e.fp,checks);}catch(x){fail(x.message);}}
 async function human(own,address,fp,admission=null){
  if(!fp||!own.known?.some(d=>d.address===address&&d.fingerprint===fp))return false;
  if(own.devices.some(d=>d.address===address&&d.fingerprint===fp)&&own.human_keys?.includes(fp))return true;
  if(own.steps.some(s=>s.devices.includes(address+"|"+fp)&&s.human_keys?.includes(fp)))return true;
  if(own.steps.every(s=>Array.isArray(s.human_keys)))return false;
  let steps=proofs.get(own.hash);
  if(!steps){
   try{steps=await e.chain(own.person,-1,admission);if(!steps.length)throw Error();await wire.verifyFirst(steps[0]);for(let i=1;i<steps.length;i++)await wire.verifyNext(steps[i],steps[i-1]);}
   catch{fail("original own-human roster proof unavailable.","proof_pending");}
   if(!steps[own.seq]||await wire.rosterHash(steps[own.seq])!==own.hash)fail("original roster conflicts with pinned head.");
   proofs.set(own.hash,steps.slice(0,own.seq+1));
  }
  for(const r of steps)if(await wire.rosterHas(r,address,fp)&&await wire.rosterHuman(r,fp))return true;
  return false;
 }
 async function validate(item,to){
  try{
  if((item.attachments||[]).some(a=>a.blob&&(a.blob.id||a.blob.size||a.blob.sha256)))fail("history carries ciphertext rather than manifests.");
  const h=wire.parseHistory(wire.historyJSON(item));
  if(h.id!==h.lid||h.group_history||h.group_admission||h.pid||h.human||h.topic||h.topic_event||h.send_group||!["",wire.SubStatus,wire.SubReaction,wire.SubRevision,wire.SubRetraction].includes(h.sub)||h.ts<=0||h.attachments.length>8||h.attachments.some(a=>!a.name||!Number.isSafeInteger(a.size)||a.size<0||a.size>100*1024*1024||!wire.validHash(a.sha256)))fail("malformed original.");
  await wire.checkVersion2({...h,v:h.sub?3:1,to,lid:"",conv:"",replica:false,root:"",session:"",fallback:false,attachments:h.attachments});
  return h;
  }catch(x){if(x instanceof Hold)throw x;fail(x.message);}
 }
 async function source(row,here){
  if(!directHistoryRow(row))return null;
  const meta=row.device_history,to=meta?meta.recipient:here?row.to:e.address,toKey=meta?meta.recipient_key||"":here?row.recipient_fp||row.fp||"":e.fp;
  const item=await validate({...e.itemOf(row,here),lid:row.id},to);
  return {row,here,item,recipient:to,recipient_key:toKey,hash:e.erasedRow(row)&&wire.validHash(meta?.hash)?meta.hash:await wire.deviceHistoryHash(item,to)};
 }
 async function original(id,checks,ops=[]){
  let result=null;
  for(const [s,here]of [["outbox",true],["inbox",false]]){
   const op=ops.findLast(o=>o.s===s&&o.k===id),row=op?op.v:await read(checks,s,id);if(!directHistoryRow(row))continue;
   const r=await source(row,here);if(result&&result.hash!==r.hash)fail("ambiguous original ID.");result=r;
  }return result;
 }
 async function project(r,own,checks,ops=[],admission=null){
  const h=r.item,fromOwn=await human(own,h.from,h.from_key,admission),toOwn=await human(own,r.recipient,r.recipient_key,admission);
  if(!fromOwn&&!toOwn)fail("neither endpoint belongs to this human.");
  let peer=fromOwn?r.recipient:h.from,direction=fromOwn?"out":"in";
  const ref=h.ref?.id||h.reply_to,parent=ref?await original(ref,checks,ops):null;
  if(ref&&!parent&&fromOwn&&toOwn)fail("own-device reply awaits its original request.","proof_pending");
  if(!parent&&(h.ref||h.agent_id))fail("exact original request unavailable.","proof_pending");
  if(parent){
   if(!h.sub&&(["answer","result"].includes(h.kind)||h.status===wire.StatusProgress)&&(h.from!==parent.recipient||r.recipient!==parent.item.from||parent.recipient_key&&h.from_key!==parent.recipient_key))fail("reply differs from original endpoints.");
   if(h.ref){
    if(h.ref.fingerprint!==parent.item.from_key)fail("control names another original key.");
    if(h.sub===wire.SubStatus&&(!["question","task"].includes(parent.item.kind)||h.from!==parent.recipient||!parent.recipient_key||h.from_key!==parent.recipient_key))fail("status host differs from original request.");
    if([wire.SubRevision,wire.SubRetraction].includes(h.sub)&&h.from_key!==parent.item.from_key)fail("control changed its original author.");
    if(h.sub===wire.SubReaction&&h.from_key!==parent.item.from_key&&(!parent.recipient_key||h.from_key!==parent.recipient_key))fail("reaction belongs to another key.");
   }
   if(h.agent_id){const t=parent.item.target;if(!t||t.address!==h.from||t.fingerprint!==h.from_key||t.agent_id!==h.agent_id)fail("named output differs from its requested agent.");}
   const meta=parent.row.device_history;
   if(meta){peer=meta.peer;if(["question","task"].includes(h.kind))direction=meta.direction;if(["answer","result"].includes(h.kind)||h.status===wire.StatusProgress)direction=h.from===peer?"in":"out";}
  }
  return {recipient:r.recipient,recipient_key:r.recipient_key,peer,direction,hash:r.hash};
 }
 async function carrier(dev,sub,body,files=[]){
  const id=wire.newID(),at=e.now(),attachments=files.map(f=>f.attachment);
  const envelope=await wire.seal({v:2,id,from:e.address,to:dev.address,ts:Math.floor(at/1000),kind:"message",sub,replica:true,body,attachments},e.keys,await wire.parsePublic(JSON.parse(dev.json)));
  return {id,to:dev.address,recipient_fp:dev.fingerprint,required_cap:wire.CapOwnSyncV3,sub,body,envelope,at,state:"queued",aside:true,...(files.length?{attachments,files:files.map(f=>({...f,uploaded:false}))}:{})};
 }
 async function admit(n,env,pin,admission=null){
  const w=wire.parseDeviceHistory(n.body),checks=[],ops=[];
  await e.refreshPerson(e.me,admission?{...admission,roster:w.roster}:null);const own=await authority(w,env,pin,checks);
  const item=await validate(w.item,w.recipient),r={item,recipient:w.recipient,recipient_key:w.recipient_key||"",hash:await wire.deviceHistoryHash(item,w.recipient)};
  // A previously admitted, locally erased original keeps its immutable hash.
  // A fresh carrier may be acknowledged without restoring text or needing an
  // erased parent's executable body to pass validation again.
  const erased=await read(checks,"inbox",item.id);
  if(erased?.device_history&&e.erasedRow(erased)){
   if(erased.fp!==item.from_key||erased.device_history.recipient!==w.recipient||erased.device_history.hash!==r.hash||erased.device_history.recipient_key&&r.recipient_key&&erased.device_history.recipient_key!==r.recipient_key)fail("copy conflicts with erased original.","conflicting_copy");
   ops.checks=checks;return ops;
  }
  const old=await original(item.id,checks);
  if(old){
   if(old.hash!==r.hash||old.item.from_key!==item.from_key||old.recipient_key&&r.recipient_key&&old.recipient_key!==r.recipient_key)fail("copy conflicts with stored original.","conflicting_copy");
   // A duplicate supplies no new proof of a legacy original recipient key.
   // Preserve both known and unknown identity before deriving any binding.
   r.recipient_key=old.recipient_key;
  }
  const meta=await project(r,own,checks,[],admission);
  if(old){ops.push({s:old.here?"outbox":"inbox",k:item.id,v:{...old.row,device_history:meta}});ops.checks=checks;return ops;}
  const fromOwn=await human(own,item.from,item.from_key,admission),row={...item,v:item.sub?3:1,lid:"",id:item.id,fp:item.from_key,history:true,synced_from:env.from,synced_key:pin.fingerprint,device_history:meta,replica:true,own:fromOwn,read:meta.direction==="out",state:"",at:Math.min(item.at||e.now(),e.now()),attachments:item.attachments.map(a=>({...a,availability:"requestable"})),...(item.sub?{control:true,aside:true}:{})};
  ops.push({s:"inbox",k:row.id,v:row});ops.checks=checks;ops.directHistory=true;return ops;
 }
 async function step(dev){
  const checks=[],ops=[],own=await read(checks,"kv","person");if(!await e.ownHistoryAuthority(dev,checks))return false;
  const key=prefix+"job/"+dev.fingerprint,saved=await read(checks,"kv",key),ceiling=await read(checks,"kv","device-history-arrival")||0;
  const job=structuredClone(saved||{older:ceiling+1,tail:ceiling});
  const incoming=await e.store.directRows({after:job.tail,ceiling,limit:page}),older=job.older?await e.store.directRows({ceiling:job.older-1,reverse:true,limit:page}):[];
  job.tail=incoming.at(-1)?.row.device_arrival||ceiling;if(older.length)job.older=older.at(-1).row.device_arrival;if(older.length<page)job.older=0;
  const sweepKey=dev.fingerprint+"/"+e.historyWake;let sweep=sweeps.get(sweepKey)||{after:"",done:false};if(incoming.length)sweep={after:"",done:false};
  const pendingPrefix=prefix+"pending/"+dev.fingerprint+"/",pending=[];
  if(!sweep.done){const keys=await e.store.keysAfter("kv",sweep.after||pendingPrefix,page);for(const k of keys){if(!k.startsWith(pendingPrefix)){sweep.done=true;break;}sweep.after=k;const p=await read(checks,"kv",k);if(p)pending.push(p);}if(keys.length<page)sweep.done=true;}
  const refs=[...incoming,...older,...pending],done=new Set(),visiting=new Set();let count=0;
  const queue=async ref=>{
   const s=ref.here?"outbox":"inbox",id=ref.row?.id||ref.id,k=s+"/"+id;if(done.has(k))return;
   if(visiting.has(k)||visiting.size>=64||count>=4*page)fail("dependency page remains.","proof_pending");visiting.add(k);
   try{
    const current=await read(checks,s,id);if(!directHistoryRow(current)||e.erasedRow(current)){done.add(k);return;}
    const r=await source(current,ref.here),parent=r.item.ref?.id||r.item.reply_to;
    if(parent&&parent!==id){const p=await original(parent,checks,ops);if(p)await queue({row:p.row,here:p.here});}
    const meta=await project(r,own,checks,ops);if(JSON.stringify(current.device_history)!==JSON.stringify(meta))ops.push({s,k:id,v:{...current,device_history:meta}});
    const ledger=prefix+"copy/"+dev.fingerprint+"/"+r.item.from_key+"/"+id,previous=await read(checks,"kv",ledger);
    if(previous){if(previous.hash!==r.hash)fail("copy ledger conflicts with original.","conflicting_copy");const copy=await read(checks,"outbox",previous.carrier);if(copy&&!["expired","not_delivered"].includes(copy.state)){done.add(k);return;}}
    // Neither its author nor the exact device that forwarded it here gets a copy back.
    if((r.item.from!==dev.address||r.item.from_key!==dev.fingerprint)&&!(current.history&&current.synced_from===dev.address&&current.synced_key===dev.fingerprint)){const body=JSON.stringify({v:1,person:own.person,roster:own.hash,recipient:r.recipient,...(r.recipient_key?{recipient_key:r.recipient_key}:{}),item:JSON.parse(wire.historyJSON(r.item))}),copy=await carrier(dev,wire.SubDeviceHistory,body);ops.push({s:"outbox",k:copy.id,v:copy},{s:"kv",k:ledger,v:{hash:r.hash,carrier:copy.id}});count++;}
    done.add(k);
   }finally{visiting.delete(k);}
  };
  for(const ref of refs){const id=ref.row?.id||ref.id,pk=pendingPrefix+(ref.here?"out/":"in/")+id;try{await queue(ref);ops.push({s:"kv",k:pk,v:undefined});}catch(x){if(!(x instanceof Hold)||x.reason==="conflicting_copy")throw x;ops.push({s:"kv",k:pk,v:{id,here:ref.here}});}}
  if(!await e.ownHistoryAuthority(dev,checks))return false;
  ops.push({s:"kv",k:key,v:job});await e.store.write(ops,checks);for(const k of sweeps.keys())if(k.startsWith(dev.fingerprint+"/"))sweeps.delete(k);sweeps.set(sweepKey,sweep);
  if(count&&e.connected)e.flushOutbox().catch(()=>{});return !!job.older||job.tail<ceiling||!sweep.done;
 }
 async function gate(rec){
  if(![wire.SubDeviceHistory,wire.SubDeviceFile].includes(rec.sub))return;
  await e.refreshPerson(e.me);const w=wire.parseDeviceHistory(rec.body,rec.sub===wire.SubDeviceFile),pin=await e.store.get("pins",rec.to);
  const authorized=async()=>{try{if(!pin||pin.pending||pin.fingerprint!==rec.recipient_fp)throw Error("Direct history recipient key changed.");await e.readSyncAuthority(w,e.address,e.fp,rec.to,pin.fingerprint);}catch(x){throw Object.assign(x,{code:"device_history_authority"});}};
  await authorized();
  const[ok,why]=await e.ctlSupport(rec.to,pin,wire.CapOwnSyncV3);if(!ok)throw Object.assign(Error(why),{code:"control_unsupported"});
  await authorized();
  if(rec.sub===wire.SubDeviceFile)await fileSource(w.item,[]);
 }
 async function fileSource(message,checks){
  const m=wire.parseGroupFileMsg(JSON.stringify(message));if(m.group_admission)fail("file has an unrelated admission.");
  const r=await original(m.lid,checks);if(!r||r.item.from_key!==m.author||r.hash!==m.hash||e.erasedRow(r.row))fail("file original unavailable or changed.");
  const f=r.item.attachments[m.index];if(!f||f.name!==m.name||f.size!==m.size||f.sha256!==m.sha256)fail("file manifest differs.");return r;
 }
 async function requestFile(row,index){
  const checks=[],current=await read(checks,"inbox",row.id),r=await source(current,false),a=current?.attachments?.[index];if(!a)throw Error("No such file.");if(a.blob)return{note:"It is here already."};
  await e.refreshPerson(e.me);const own=await read(checks,"kv","person"),dev=own?.devices.find(d=>d.address===current.synced_from);if(!dev)throw Error("The original forwarder is no longer your device.");
  const w={v:1,person:own.person,roster:own.hash,item:{v:1,type:"request",lid:row.id,author:r.item.from_key,hash:r.hash,index,name:a.name,size:a.size,sha256:a.sha256}};
  await e.readSyncAuthority(w,e.address,e.fp,dev.address,dev.fingerprint,checks);await fileSource(w.item,checks);
  const pk=prefix+"file/"+row.id+"/"+a.sha256,prior=await read(checks,"kv",pk);if(prior&&!fileEqual(prior.item,w.item))throw Error("Another exact file manifest is pending.");
  const rec=await carrier(dev,wire.SubDeviceFile,JSON.stringify(w)),next=structuredClone(current);next.attachments[index]={...a,availability:"requested"};
  await e.store.write([{s:"outbox",k:rec.id,v:rec},{s:"inbox",k:row.id,v:next},{s:"kv",k:pk,v:w}],checks);e.changed();if(e.connected)await e.post(rec);return{note:"Requested from your linked device; it must be online."};
 }
 async function admitFile(n,env,pin,admission=null){
  const w=wire.parseDeviceHistory(n.body,true),checks=[],ops=[];let m;try{m=wire.parseGroupFileMsg(JSON.stringify(w.item));}catch(x){fail(x.message);}
  if(m.type==="request"&&(m.available||n.attachments.length)||m.type==="offer"&&!!m.available!==(n.attachments.length===1))fail("malformed file carrier.");
  await e.refreshPerson(e.me,admission?{...admission,roster:w.roster}:null);await authority(w,env,pin,checks);const r=await fileSource(m,checks);
  if(m.type==="request"){const key="serve/"+env.id;if(!await read(checks,"kv",key))ops.push({s:"kv",k:key,v:{serve:true,direct:true,id:env.id,device:env.from,key:pin.fingerprint,message:m,state:"pending",at:e.now()}});}
  else {const pk=prefix+"file/"+m.lid+"/"+m.sha256,asked=await read(checks,"kv",pk);
   if(r.here)fail("file offer does not name a received history copy.");const row=structuredClone(r.row),a=row.attachments[m.index];
   if(m.available){const f=n.attachments[0];if(f.name!==m.name||f.size!==m.size||f.sha256!==m.sha256)fail("offered bytes differ.");}
   if(!asked){if(!a.blob)fail("no exact pending request or stored file.");ops.checks=checks;return ops;}if(!fileEqual(asked.item,m))fail("offer differs from requested manifest.");
   if(m.available){row.attachments[m.index]=n.attachments[0];ops.push({s:"kv",k:pk,v:undefined});}
   else row.attachments[m.index]={...a,availability:"unavailable",detail:m.detail||"Original file unavailable."};ops.push({s:"inbox",k:row.id,v:row});
  }ops.checks=checks;return ops;
 }
 async function serveFile(s){
  const own=await e.store.get("kv","person"),dev=own?.devices.find(d=>d.address===s.device&&d.fingerprint===s.key);if(!dev||!await e.ownHistoryAuthority(dev))throw Error("File requester authority changed.");
  const r=await fileSource(s.message,[]),a=r.row.attachments[s.message.index];let bytes;
  if(!r.here&&a.blob)bytes=await wire.decryptFile(await e.cipherOf(a),a,e.keys);
  else {const kept=await e.store.get("files","kept/"+a.sha256);if(!kept)throw Error("This device no longer holds the original file.");bytes=await wire.decryptFile(kept.ct,kept.attachment,e.keys);}
  const f=await wire.encryptFile(bytes,s.message.name,await wire.parsePublic(JSON.parse(dev.json)));if(f.attachment.sha256!==s.message.sha256||f.attachment.size!==s.message.size)throw Error("Kept bytes differ from original manifest.");await offerFile(s,[f],"");
 }
 async function offerFile(s,files,detail){
  const checks=[],own=await read(checks,"kv","person"),dev=own?.devices.find(d=>d.address===s.device&&d.fingerprint===s.key);if(!dev)return;
  const w={v:1,person:own.person,roster:own.hash,item:{...s.message,type:"offer",available:files.length>0,...(detail?{detail:"This device no longer holds the original file."}:{})}};
  await e.readSyncAuthority(w,e.address,e.fp,dev.address,dev.fingerprint,checks);await fileSource(w.item,checks);
  const rec=await carrier(dev,wire.SubDeviceFile,JSON.stringify(w),files);await e.store.write([{s:"outbox",k:rec.id,v:rec}],checks);e.changed();await e.post(rec);
 }
 return {source,original,project,human,admit,step,gate,requestFile,admitFile,serveFile,offerFile};
}
