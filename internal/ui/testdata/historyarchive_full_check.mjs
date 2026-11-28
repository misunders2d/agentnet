// A browser source packs full history parts and a browser receiver imports
// one, end to end over the real production path: direct-history pages stage
// archive children, the engine packs, seals and uploads a part, posts its
// descriptor, and the new device downloads, decrypts, inflates and admits
// every child. Synthetic relay only; all ciphertext and signatures are real.
// window.archiveFull may set {count, bytes} (originals, body bytes each).
const assert={ok:(x,m)=>{if(!x)throw Error(m)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const browser=typeof window!=='undefined',options={count:1100,bytes:200,...(browser?window.archiveFull:JSON.parse(process.env.ARCHIVE_FULL||'{}'))};
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const blobs=new Map(),pubs=new Map(),chains=new Map(),profiles=new Map(),engines=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 // Blob state replies carry no bytes, as the relay's do.
 const state=({bytes,...b})=>json(b);
 if(p==='/v1/blobs'){const b=JSON.parse(o.body);blobs.set(b.id,{...b,received:0,state:'uploading',bytes:new Uint8Array(b.size)});return state(blobs.get(b.id));}
 if(p.startsWith('/v1/blobs/')){const id=p.split('/')[3],b=blobs.get(id);if(p.endsWith('/data'))return new Response(b.bytes);if(o.method==='PUT'){const bytes=new Uint8Array(o.body);b.bytes.set(bytes,Number(u.searchParams.get('offset')));b.received+=bytes.length;}if(p.endsWith('/complete'))b.state='stored';return state(b);}
 if(p==='/v1/version')return json({features:['env2','env3','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages')return json({state:'custody'});
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack'))return json(null,204);
 throw Error('Unexpected synthetic route '+p);
};
const archiveCaps=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl,wire.CapHistoryArchive];
async function caps(e,names=archiveCaps){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='archive-full-'+wire.newID(),store=browser?await openIDB(name):memoryStore(),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
 e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);e.connected=false;pubs.set(address,e.pub);return e;
}
async function person(address){
 const e=await device(address);e.roster=await wire.newRoster(e.keys,address,address.split('/')[0]);e.me=await e.personRecord([e.roster],'self',null);
 chains.set(e.me.person,[JSON.parse(wire.rosterJSON(e.roster))]);
 await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);await caps(e);return e;
}
async function sibling(owner,address){
 const e=await device(address),join=await wire.joinConsent(e.keys,address,owner.me.person,owner.me.seq+1,owner.me.hash);
 const humans=[...await wire.rosterHumans(owner.roster),e.fp];
 const next=await wire.nextRoster(owner.keys,owner.address,owner.roster,[...owner.me.devices.map(d=>pubs.get(d.address)),e.pub],join,owner.roster.label,humans);
 const steps=[...await Promise.all(chains.get(owner.me.person).map(r=>wire.parseRoster(r))),next];
 owner.roster=next;owner.me=await owner.personRecord(steps,'self',owner.me);e.roster=next;e.me=await e.personRecord(steps,'self',null);
 chains.set(owner.me.person,steps.map(r=>JSON.parse(wire.rosterJSON(r))));
 await owner.store.write([{s:'kv',k:'person',v:owner.me}]);await owner.pinDevices(owner.me);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}}]);await e.pinDevices(e.me);await caps(e);await caps(owner);return e;
}
const heap=()=>browser&&performance.memory?performance.memory.usedJSHeapSize:process.memoryUsage().heapUsed+process.memoryUsage().arrayBuffers;
const mark=async name=>{if(browser)await window.fetch('/mark?name='+name);};

const desk=await person('archive/desk'),phone=await sibling(desk,'archive/phone'),peer=await person('archive-peer/desk');
for(const e of [desk,phone])await e.store.write([{s:'persons',k:peer.me.person,v:{...peer.me,state:'pinned'}},{s:'pins',k:peer.address,v:{address:peer.address,json:wire.marshalPublic(peer.pub),fingerprint:peer.fp,pending:null}}]);
// Incompressible bodies, so the measured part is as large as the count allows.
const text=n=>{const raw=new Uint8Array(Math.ceil(n*3/4));crypto.getRandomValues(raw);return wire.b64(raw).slice(0,n);};
const originals=[];
for(let i=0;i<options.count;i+=100){
 const ops=[];
 for(let j=i;j<Math.min(options.count,i+100);j++){const id=wire.newID(),m={id,from:desk.address,to:peer.address,ts:j+1,kind:'message',body:'archived '+j+' '+text(options.bytes),attachments:[]};originals.push(id);ops.push({s:'outbox',k:id,v:{...m,v:1,at:(j+1)*1000,fp:peer.fp,envelope:await wire.seal(m,desk.keys,peer.pub),state:'delivered'}});}
 await desk.store.write(ops);
}

// Every source transaction is recorded: no pack or receipt may cover more
// than one engine part (HistoryArchivePackEntries) of outbox rows.
const write=desk.store.write.bind(desk.store),packs=[];let widest=0;
desk.store.write=async(ops,c)=>{
 const outbox=ops.filter(o=>o.s==='outbox').length,job=ops.find(o=>o.s==='kv'&&o.k.startsWith('history-archive/out/')&&o.v?.state==='prepared'&&!o.v.attachment?.uploaded);
 const started=performance.now(),out=await write(ops,c);
 if(job&&ops.some(o=>o.s==='files'))packs.push({children:job.v.children.length,commit_ms:performance.now()-started,marked:ops.find(o=>o.s==='files').v.history_part===true});
 if(ops.some(o=>o.s==='kv'&&o.k.startsWith('history-archive/out/')))widest=Math.max(widest,outbox);
 return out;
};
const phoneDev=desk.me.devices.find(d=>d.address===phone.address);
await desk.reconcileHistory();desk.connected=true;
const settle=async e=>{for(let i=0;i<64&&(e.archiveRun||e.historyProduction||e.syncOutboxPass);i++)await Promise.all([e.archiveRun,e.historyProduction,e.syncOutboxPass].filter(Boolean));};
const packStart=performance.now();
for(let i=0;i<options.count/50+8;i++){await desk.directHistory().step(phoneDev);await settle(desk);}
const packMs=performance.now()-packStart;
const jobs=(await desk.store.prefix('kv','history-archive/out/')).sort((a,b)=>(a.order??0)-(b.order??0));
check(jobs.length>=1&&jobs.length<=wire.HistoryArchiveWindow,'source keeps at most HistoryArchiveWindow parts outstanding');
check(jobs.every(j=>j.children.length<=wire.HistoryArchivePackEntries),'engine never packs a part above HistoryArchivePackEntries');
check(widest<=wire.HistoryArchivePackEntries+1,'no pack or upload transaction covers more than one engine part of outbox rows: '+widest);
const full=jobs.find(j=>j.children.length===wire.HistoryArchivePackEntries)||jobs.reduce((a,b)=>a.children.length>=b.children.length?a:b);
check(options.count<wire.HistoryArchivePackEntries+50||full.children.length===wire.HistoryArchivePackEntries,'a full engine part is packed');
check(jobs.every(j=>!('ct' in j)),'job rows scanned on every wake hold no part ciphertext');
const descriptors=(await desk.store.outboxSub(wire.SubHistoryArchive));
const descriptor=descriptors.find(r=>r.id===full.id);check(descriptor?.state==='custody','full part descriptor is uploaded and posted');
const partBytes=blobs.get(descriptor.attachments[0].blob.id).bytes.length;

// The new device imports the full part from the relay.
await mark('import_start');
let peakHeap=heap();const sampler=setInterval(()=>{peakHeap=Math.max(peakHeap,heap());},2);
const importChild=phone.importArchiveChild.bind(phone);phone.importArchiveChild=async(...a)=>{peakHeap=Math.max(peakHeap,heap());return importChild(...a);};
const importStart=performance.now();phone.connected=true;
await phone.admit(descriptor.envelope,wire.parseEnvelope(descriptor.envelope));
for(let i=0;i<8&&(await phone.store.get('kv','history-archive/in/'+full.id))?.state!=='done';i++){await phone.archives().wake();await settle(phone);}
const importMs=performance.now()-importStart;clearInterval(sampler);
await mark('import_end');
check((await phone.store.get('kv','history-archive/in/'+full.id))?.state==='done','receiver finishes importing the full part');
const children=new Set(full.children),imported=(await phone.store.all('inbox')).filter(r=>r.device_history&&originals.includes(r.id)).length;
check(imported===full.children.length,'every child of the full part is admitted: '+imported);
check(!await phone.store.get('files','archive/'+full.id),'completed import releases its retained part');

// The source's receipt for that part stays within one part too, and the
// window then lets the remaining children pack.
const receiptStart=performance.now();
await desk.dispatch('receipt',JSON.stringify({id:full.id,state:'delivered',seq:1}));if(desk.archiveReceiptRun)await desk.archiveReceiptRun;await settle(desk);
const receiptMs=performance.now()-receiptStart;
check((await desk.store.get('kv','history-archive/out/'+full.id)).state==='accepted','delivered receipt accepts the part');
check(widest<=wire.HistoryArchivePackEntries+1,'receipt transaction covers at most one engine part of outbox rows: '+widest);
check((await Promise.all([...children].map(id=>desk.store.get('outbox',id)))).every(r=>r.state==='archive_accepted'),'accepted children are retained, not delivered');

// Upgrade mid-copy: parts packed before ha2 (manifest without a format) are
// version 1, which upgraded receivers refuse. On the first wake after the
// upgrade their children are staged again and repacked as version 2, an
// unsent descriptor stops holding the window, and nothing is lost.
desk.store.write=write;
const waitingV1=jobs.find(j=>j.id!==full.id&&j.children.length===wire.HistoryArchivePackEntries)||jobs.find(j=>j.id!==full.id);
const v1=job=>{const {format,...manifest}=job.manifest;return manifest;};
const acceptedJob=await desk.store.get('kv','history-archive/out/'+full.id),waitingJob=await desk.store.get('kv','history-archive/out/'+waitingV1.id);
await desk.store.write([
 {s:'kv',k:'history-archive/out/'+full.id,v:{...acceptedJob,manifest:v1(acceptedJob),children:full.children,required_cap:wire.CapTopicContext,mention_cap:true}},
 // One accepted copy was erased here since: only its old part knows its requirements.
 {s:'outbox',k:full.children[0],v:{...await desk.store.get('outbox',full.children[0]),body:'',mention_cap:false}},
 {s:'kv',k:'history-archive/out/'+waitingV1.id,v:{...waitingJob,manifest:v1(waitingJob)}},
 {s:'outbox',k:waitingV1.id,v:{...await desk.store.get('outbox',waitingV1.id),state:'waiting',required_cap:'ha1',error:'peer_update: ha1 required'}}]);
const upgraded=new Engine({store:desk.store,base:desk.base,fetch});Object.assign(upgraded,{keys:desk.keys,address:desk.address,pub:desk.pub,fp:desk.fp,me:desk.me,connected:true});
for(let i=0;i<8;i++){await upgraded.archives().wake();await settle(upgraded);}
check((await desk.store.get('outbox',waitingV1.id)).state==='not_delivered','a version 1 descriptor waiting across the upgrade is replaced, not left holding the window');
const swept=[full.id,waitingV1.id];
check((await Promise.all(swept.map(id=>desk.store.get('kv','history-archive/out/'+id)))).every(j=>j.swept&&!j.children.length&&!j.ct),'version 1 parts are swept once and keep no children or ciphertext');
const after=(await desk.store.prefix('kv','history-archive/out/')).filter(j=>!swept.includes(j.id));
const repacked=new Set(after.filter(j=>j.manifest.format===wire.HistoryArchiveFormat&&['prepared','published'].includes(j.state)).flatMap(j=>j.children));
const again=[...full.children,...waitingV1.children];
const rows=await Promise.all(again.map(id=>desk.store.get('outbox',id)));
check(rows.every(r=>r.state==='archive_staged'&&(repacked.has(r.id)?r.archive_chunk&&!swept.includes(r.archive_chunk):!r.archive_chunk)),'every child of a version 1 part, accepted or waiting, is staged again for a version 2 part');
check(again.some(id=>repacked.has(id)),'the swept children repack as version 2 parts');
check(after.every(j=>j.children.length<=wire.HistoryArchivePackEntries),'repacked parts stay within the engine part cap');
const erasedCopy=await desk.store.get('outbox',full.children[0]);
check(erasedCopy.body===''&&erasedCopy.required_cap===wire.CapTopicContext&&erasedCopy.mention_cap===true,'an erased copy staged again keeps its version 1 part requirements');
// A receiver's unfinished version 1 import can never be read again: its
// source sends every copy again, so the job closes and its part is freed.
const staleID=wire.newID(),staleManifest=v1(acceptedJob);
await phone.store.write([{s:'kv',k:'history-archive/in/'+staleID,v:{id:staleID,from:desk.address,from_key:desk.fp,manifest:staleManifest,attachment:full.attachment,index:3,order:1,retained:true,state:'importing'}},{s:'files',k:'archive/'+staleID,v:{ct:new Uint8Array(64)}}]);
const upgradedPhone=new Engine({store:phone.store,base:phone.base,fetch});Object.assign(upgradedPhone,{keys:phone.keys,address:phone.address,pub:phone.pub,fp:phone.fp,me:phone.me,connected:false});
await upgradedPhone.archives().wake();await settle(upgradedPhone);
check((await phone.store.get('kv','history-archive/in/'+staleID)).state==='done'&&!await phone.store.get('files','archive/'+staleID),'an unfinished version 1 import closes and releases its retained part');
check((await phone.store.get('kv','history-archive/in/'+full.id)).state==='done','a finished version 2 import is untouched');
// A sealed part waiting to upload is not counted as a received file.
check(packs.length&&packs.every(p=>p.marked),'every packed part file row is marked as a history part');
await desk.store.write([{s:'files',k:'archive-out/'+wire.newID(),v:{ct:new Uint8Array(32),history_part:true}}]);
const received=(await upgraded.storageSummary()).local.areas.find(a=>a.directory==='received');
check(received.usage.files===0,'storage summary does not count a history part waiting to upload as a received file: '+JSON.stringify(received.usage));

const result={ok:true,checks,storage:browser?'IndexedDB':'memory',count:options.count,body_bytes:options.bytes,parts:jobs.map(j=>j.children.length),part_entries:full.children.length,part_bytes:partBytes,pack_commits:packs.map(({marked,...p})=>p),pack_phase_ms:Math.round(packMs),import_ms:Math.round(importMs),receipt_ms:Math.round(receiptMs),peak_heap_mb:+(peakHeap/1048576).toFixed(1)};
console.log('history archive full part',JSON.stringify(result));
if(browser){for(const e of engines){await settle(e);e.closing=true;e.store.close();await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(e.fixtureStoreName);r.onsuccess=resolve;r.onerror=()=>reject(r.error);r.onblocked=resolve;});}window.archiveFullResult=result;}
