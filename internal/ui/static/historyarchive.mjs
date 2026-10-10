// Bounded own-human bootstrap over existing signed envelopes and age blobs.
// No archive child is a live turn; ordinary admission remains the authority.
import * as wire from './wire.mjs';
const utf8=new TextEncoder(),decode=new TextDecoder('utf-8',{
  fatal:true
});
const outgoing='history-archive/out/',incoming='history-archive/in/';
const inert=new Set(['history',wire.SubDeviceHistory,wire.SubRootSync,wire.SubGroupProof,wire.SubGroupContext]);
const yieldTurn=()=>new Promise(r=>setTimeout(r,0));
const exact=(value,keys)=>value&&typeof value==='object'&&!Array.isArray(value)&&Object.keys(value).every(k=>keys.includes(k));
export async function parseArchiveChunk(bytes,count){
  if(!(bytes instanceof Uint8Array)||bytes.length>wire.HistoryArchiveMaxBytes)throw Error('Archive exceeds bound.');
  const chunk=JSON.parse(decode.decode(bytes));
  if(!exact(chunk,['v','entries'])||chunk.v!==1||!Array.isArray(chunk.entries)||chunk.entries.length!==count||count<1||count>wire.HistoryArchiveMaxEntries)throw Error('Invalid archive chunk.');
  const children=new Set();
  for(const entry of chunk.entries){
    if(!exact(entry,['envelope','blobs'])||!entry.envelope||typeof entry.envelope!=='object'||!Array.isArray(entry.blobs)||entry.blobs.length>8)throw Error('Invalid archive entry.');
    const env=wire.parseEnvelope(entry.envelope);
    if(children.has(env.id))throw Error('Duplicate archive child.');
    children.add(env.id);
    if(env.v!==wire.Version2||env.kind!=='message'||env.ts<=0||!wire.validID(env.id)||!wire.validAddress(env.from)||!wire.validAddress(env.to)||!env.ct.length||env.ct.length>wire.MaxCiphertext||env.sig.length!==64||env.attn||env.chan||env.session||env.fallback)throw Error('Archive child is not inert.');
    const refs=new Set();
    for(const ref of env.blobs){
      if(!wire.validID(ref.id)||!wire.validHash(ref.sha256)||!Number.isSafeInteger(ref.size)||ref.size<1||refs.has(ref.id))throw Error('Invalid archive child blob reference.');
      refs.add(ref.id);
    }
    const ids=new Set();
    for(const blob of entry.blobs){
      if(!exact(blob,['id','ct'])||!wire.validID(blob.id)||typeof blob.ct!=='string'||ids.has(blob.id))throw Error('Invalid archive structural blob.');
      ids.add(blob.id);
      const ref=env.blobs.find(r=>r.id===blob.id),ct=wire.unb64(blob.ct,'archive structural ciphertext');
      if(!ref||ct.length!==ref.size||wire.hex(await wire.sha256(ct))!==ref.sha256)throw Error('Corrupt archive structural ciphertext.');
    }
  }
  return chunk;
}
export function historyArchive(e,Hold,StoreConflict){
  async function authority(manifest,from,key,checks=[]){
    return e.readSyncAuthority(manifest,from,key,e.address,e.fp,checks);
  }
  async function supported(dev){
    if(!await e.ownHistoryAuthority(dev))return false;
    const pin=await e.store.get('pins',dev.address);
    if(!pin||pin.pending||pin.fingerprint!==dev.fingerprint)return false;
    const [ok]=await e.ctlSupport(dev.address,pin,wire.CapHistoryArchive);
    return ok;
  }
  // The existing source cursor and exact sealed staging rows commit together.
  // Chunk encryption and blob I/O happen later, outside production and SSE.
  async function prepare(copies,ops,checks,dev){
    const live=copies.filter(r=>r.fresh_live).map(v=>({
      s:'outbox',k:v.id,v
    }));
    copies=copies.filter(r=>!r.fresh_live);
    if(!copies.length||!await supported(dev))return [...live,...copies.map(v=>({
      s:'outbox',k:v.id,v
    })),...ops];
    if(!await e.ownHistoryAuthority(dev,checks))throw Error('Archive recipient authority changed.');
    return [...live,...copies.map(row=>{
      if(!inert.has(row.sub))throw Error('Archive source is not inert.');
      return {
        s:'outbox',k:row.id,v:{
          ...row,recipient_fp:dev.fingerprint,state:'archive_staged',archive_chunk:''
        }
      };
    }),...ops];
  }
  async function pack(){
    const staged=(await e.store.outboxStates(['archive_staged'])).filter(r=>!r.archive_chunk).sort((a,b)=>(a.send_order??a.at)-(b.send_order??b.at)||a.id.localeCompare(b.id));
    const targets=new Map();
    for(const row of staged){
      const key=row.to+'\0'+row.recipient_fp;
      if(!targets.has(key))targets.set(key,[]);
      targets.get(key).push(row);
    }
    for(const rows of targets.values()){
      try{
        let outstanding=(await e.store.prefix('kv',outgoing)).filter(j=>j.to===rows[0].to&&j.recipient_fp===rows[0].recipient_fp&&['prepared','published'].includes(j.state)).length;
        while(rows.length&&outstanding<2){
          const checks=[],{
            own,pin
          }=await gate(rows[0],checks),entries=[],chosen=[];
          for(const row of rows){
            const current=await e.groupRead(checks,'outbox',row.id);
            if(current?.state!=='archive_staged'||current.archive_chunk)break;
            const envelope=JSON.parse(row.envelope),env=wire.parseEnvelope(envelope),blobs=[];
            for(const f of row.files||[]){
              const ref=env.blobs.find(b=>b.id===f.attachment.blob.id);
              if(!ref||!f.ct||f.ct.length!==ref.size||wire.hex(await wire.sha256(f.ct))!==ref.sha256)throw Error('Archive structural ciphertext unavailable.');
              blobs.push({
                id:ref.id,ct:wire.b64(f.ct)
              });
            }
            const entry={
              envelope,blobs
            },size=utf8.encode(JSON.stringify({
              v:1,entries:[...entries,entry]
            })).length;
            if(size>wire.HistoryArchiveMaxBytes){
              if(!entries.length)throw Error('One archive record exceeds 4 MiB.');
              break;
            }
            entries.push(entry);
            chosen.push(current);
            if(entries.length===wire.HistoryArchiveMaxEntries)break;
          }
          if(!entries.length)break;
          const sealed=await wire.encryptFile(utf8.encode(JSON.stringify({
            v:1,entries
          })),'agentnet-history-v1.json',await e.pubOf(pin)),id=wire.newID();
          const manifest={
            v:1,person:own.person,roster:own.hash,count:entries.length,format:'agentnet-history-v1'
          };
          await gate(rows[0],checks);
          await e.store.write([{
            s:'kv',k:outgoing+id,v:{
              id,to:rows[0].to,recipient_fp:rows[0].recipient_fp,manifest,...sealed,state:'prepared',children:chosen.map(r=>r.id)
            }
          },...chosen.map(row=>({
            s:'outbox',k:row.id,v:{
              ...row,archive_chunk:id
            }
          }))],checks);
          rows.splice(0,chosen.length);
          outstanding++;
        }
      }catch{
        await sourceFailure(rows[0]);
      }
    }
  }
  async function sourceFailure(row){
    if(!row)return;
    const current=await e.store.get('outbox',row.id);
    if(current?.state==='archive_staged')await e.store.write([{
      s:'outbox',k:row.id,v:{
        ...current,detail:'Archive catch-up remains incomplete; its recipient authority, structural ciphertext or size needs attention.'
      }
    }],[{
      s:'outbox',k:row.id,v:current
    }]);
  }
  async function gate(row,checks=[]){
    const own=await e.groupRead(checks,'kv','person'),dev=own?.devices.find(d=>d.address===row.to&&d.fingerprint===row.recipient_fp);
    if(!dev||!await e.ownHistoryAuthority(dev,checks))throw Error('Archive destination is no longer a current own human device.');
    const pin=await e.groupRead(checks,'pins',dev.address),[ok]=await e.ctlSupport(dev.address,pin,wire.CapHistoryArchive);
    if(!ok)throw Object.assign(Error('Archive reader capability unavailable.'),{code:'history_archive_unsupported'});
    return {
      own,dev,pin
    };
  }
  async function admit(n,env,pin){
    const manifest=wire.parseHistoryArchive(n.body),checks=[];
    await authority(manifest,env.from,pin.fingerprint,checks);
    const key=incoming+env.id,prior=await e.groupRead(checks,'kv',key);
    if(prior&&(JSON.stringify(prior.manifest)!==JSON.stringify(manifest)||JSON.stringify(prior.attachment)!==JSON.stringify(n.attachments[0])||prior.from_key!==pin.fingerprint))throw new Hold('invalid','Conflicting archive descriptor.');
    const ops=prior?[]:[{
      s:'kv',k:key,v:{
        id:env.id,from:env.from,from_key:pin.fingerprint,manifest,attachment:n.attachments[0],index:0,state:'pending'
      }
    }];
    ops.checks=checks;
    ops.archivePending=!prior?.retained;
    ops.archive=true;
    return ops;
  }
  async function uploadPass(){
    for(const row of await e.store.prefix('kv',outgoing)){
      if(e.closing||!e.connected)return;
      if(row.state!=='prepared')continue;
      try{
        const checks=[];
        await gate(row,checks);
        await e.uploadBlob(row.to,row.attachment.blob,row.ct);
        const {
          pin
        }=await gate(row,checks),at=e.now(),body=JSON.stringify(row.manifest);
        const envelope=await wire.seal({
          v:2,id:row.id,from:e.address,to:row.to,ts:Math.floor(at/1000),kind:'message',sub:wire.SubHistoryArchive,replica:true,body,attachments:[row.attachment]
        },e.keys,await e.pubOf(pin));
        const previous=await e.groupRead(checks,'kv',outgoing+row.id);
        if(previous?.state!=='prepared')continue;
        await e.store.write([{
          s:'outbox',k:row.id,v:{
            id:row.id,to:row.to,recipient_fp:row.recipient_fp,sub:wire.SubHistoryArchive,body,envelope,at,aside:true,state:'queued',required_cap:wire.CapHistoryArchive,attachments:[row.attachment],files:[{
              attachment:row.attachment,uploaded:true
            }]
          }
        },{
          s:'kv',k:outgoing+row.id,v:{
            ...row,state:'published',ct:null
          }
        }],checks);
        e.queueOutbox();
      }catch{
        const current=await e.store.get('kv',outgoing+row.id);
        if(current)await e.store.write([{
          s:'kv',k:outgoing+row.id,v:{
            ...current,error:'transfer_pending'
          }
        }]);
      }
    }
  }
  async function importPass(){
    for(let job of await e.store.prefix('kv',incoming)){
      if(e.closing)return;
      if(job.state==='done')continue;
      try{
        const checks=[];
        await authority(job.manifest,job.from,job.from_key,checks);
        let retained=await e.store.get('files','archive/'+job.id);
        if(!retained){
          if(!e.connected)return;
          const ct=await e.getBytes('/v1/blobs/'+job.attachment.blob.id+'/data',job.attachment.blob.size);
          // Both hashes/decryption must pass before the descriptor can be acked.
          await parseArchiveChunk(await wire.decryptFile(ct,job.attachment,e.keys),job.manifest.count);
          await authority(job.manifest,job.from,job.from_key,checks);
          const current=await e.groupRead(checks,'kv',incoming+job.id);
          job={
            ...current,retained:true,state:'importing'
          };
          await e.store.write([{
            s:'files',k:'archive/'+job.id,v:{
              ct
            }
          },{
            s:'kv',k:incoming+job.id,v:job
          },{
            s:'receipts',k:job.id,v:{
              id:job.id,state:'delivered'
            }
          }],checks);
          retained={
            ct
          };
          e.flushReceipts().catch(()=>{
          });
        }
        const bytes=await wire.decryptFile(retained.ct,job.attachment,e.keys),chunk=await parseArchiveChunk(bytes,job.manifest.count);
        while(job.index<chunk.entries.length){
          if(e.closing)return;
          await importEntry(job,chunk.entries[job.index]);
          job=await e.store.get('kv',incoming+job.id);
          await yieldTurn();
        }
        e.changed(true);
        e.retryHeld().catch(()=>{
        });
      }catch{
        const current=await e.store.get('kv',incoming+job.id);
        if(current)await e.store.write([{
          s:'kv',k:incoming+job.id,v:{
            ...current,error:'import_pending'
          }
        }]);
      }
    }
  }
  async function importEntry(job,entry){
    const data=JSON.stringify(entry.envelope),env=wire.parseEnvelope(data),checks=[];
    await authority(job.manifest,job.from,job.from_key,checks);
    if(env.from!==job.from||env.to!==e.address)throw Error('Archive child endpoint mismatch.');
    const pin=await e.groupRead(checks,'pins',job.from),n=await wire.open(data,e.keys,e.address,await e.pubOf(pin));
    if(!inert.has(n.sub)||n.kind!=='message'||n.target||n.pid&&![wire.SubGroupProof,wire.SubGroupContext].includes(n.sub)||n.sub==='history'&&!n.replica)throw Error('Archive child is not inert history.');
    const blobOps=[];
    for(const b of entry.blobs){
      const ref=env.blobs.find(r=>r.id===b.id);
      if(!ref||![wire.SubGroupProof,wire.SubGroupContext].includes(n.sub))throw Error('Unreferenced archive structural blob.');
      const ct=wire.unb64(b.ct,'archive ciphertext');
      if(ct.length!==ref.size||wire.hex(await wire.sha256(ct))!==ref.sha256)throw Error('Corrupt archive structural ciphertext.');
      blobOps.push({
        s:'files',k:'ct/'+b.id,v:{
          ct
        }
      });
    }
    if(blobOps.length)await e.store.write(blobOps,checks);
    await e.importArchiveChild(data,env,job,checks);
  }
  function worker(name,work){
    const running=name+'Run',again=name+'Again';
    e[again]=true;
    if(!e[running])e[running]=(async()=>{
      do{
        e[again]=false;
        await work();
      }
      while(e[again]&&!e.closing);
    })().finally(()=>{
      e[running]=null;
    });
    return e[running];
  }
  function wake(){
    const upload=worker('archiveUpload',async()=>{
      if(e.connected&&!e.closing){
        await pack();
        await uploadPass();
      }
    });
    const incoming=worker('archiveImport',importPass);
    const running=Promise.allSettled([upload,incoming]);
    e.archiveRun=running;
    running.finally(()=>{
      if(e.archiveRun===running)e.archiveRun=null;
    });
    return running;
  }
  async function receipt(row){
    const key=outgoing+row.id,job=await e.store.get('kv',key);
    if(!job)return;
    if(row.state!=='delivered')return;
    const ops=[],checks=[{s:'kv',k:key,v:job}];
    for(const id of job.children){
      const child=await e.store.get('outbox',id);
      checks.push({s:'outbox',k:id,v:child});
      if(child?.state==='archive_staged'&&child.archive_chunk===row.id)ops.push({
        s:'outbox',k:id,v:{
          ...child,state:'archive_accepted',...(child.files?{files:child.files.map(file=>({...file,ct:null}))}:{})
        }
      });
    }
    ops.push({
      s:'kv',k:key,v:{
        ...job,state:'accepted'
      }
    });
    try{await e.store.write(ops,checks);}
    catch(error){if(error instanceof StoreConflict)return receipt(row);throw error;}
    e.runHistory().catch(()=>{
    });
    wake().catch(()=>{
    });
  }
  return {
    prepare,admit,gate,wake,receipt
  };
}
