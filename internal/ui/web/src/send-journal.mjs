// Android's explicit send gestures survive process death. This records exact
// API requests, not delivery claims; only a successful native admission clears
// an entry. Reading never retries or grants authority.
const endpoints = new Set(["/api/send", "/api/dm/send", "/api/dm/agent/ask", "/api/act", "/api/request/followup"]);
const key = workspace => "agentnet.android.send-journal.v1." + workspace;
const clone = value => JSON.parse(JSON.stringify(value));
function valid(entry) {
  return entry && /^[0-9a-f]{32}$/.test(entry.id) && typeof entry.conversation === "string" &&
    endpoints.has(entry.endpoint) && entry.request && typeof entry.request === "object" && !Array.isArray(entry.request) &&
    (entry.endpoint === "/api/act" ? entry.request.do === "reply" && entry.request.send_id === entry.id : entry.request.id === entry.id);
}
export function readPending(storage, workspace) {
  const value = JSON.parse(storage.getItem(key(workspace)) || "[]");
  if (!Array.isArray(value) || value.some(e => !valid(e)) || new Set(value.map(e => e.id)).size !== value.length)
    throw new Error("The saved send journal is damaged. Nothing was retried.");
  return clone(value);
}
// Comic's own change notice: listeners in this package, not page events.
const changed = new Set();
export function onJournalChange(fn) { changed.add(fn); return () => { changed.delete(fn); }; }
function write(storage, workspace, entries) {
  storage.setItem(key(workspace), JSON.stringify(entries));
  // A listener's failure is its own: the journal is already written.
  for (const fn of [...changed]) { try { fn(); } catch (_) { /* the listener's own */ } }
}
export function saveBatch(storage, workspace, entries) {
  const pending = readPending(storage, workspace), added=[];
  for (const original of entries) {
    const entry = clone(original);
    if (!valid(entry)) throw new Error("Cannot save an incomplete send request.");
    const existing = pending.find(e => e.id === entry.id);
    if (existing) {
      if (existing.endpoint !== entry.endpoint || existing.conversation !== entry.conversation || JSON.stringify(existing.request) !== JSON.stringify(entry.request))
        throw new Error("This send ID already belongs to a different request.");
    } else {pending.push(entry);added.push(entry.id);}
  }
  // One synchronous write freezes every target before any target is submitted.
  write(storage, workspace, pending);
  return added; // only these new records belong to an aborted preparation
}
export function acknowledge(storage, workspace, id) {
  write(storage, workspace, readPending(storage, workspace).filter(e => e.id !== id));
}
export function recordFailure(storage, workspace, id, error) {
  write(storage, workspace, readPending(storage, workspace).map(e => e.id === id ? { ...e, error: String(error) } : e));
}

// Original editor context is not a Retry request. Keep it separately so native
// admission can acknowledge the request without losing a cancellable draft.
// Revision tombstones remain readable by older Comic versions (text is still
// text); older writes invalidate recovery rather than resurrecting old input.
const revisionField = "__an_draft_revision";
// Each record write gets its own token, never supplied by callers: a
// restoration binds to that exact write, so rewriting the same revision and
// content later never brings it back.
const writeField = "__an_draft_write";
const draftFields = ["text", "replyTo", "agent", "topic", "newTopic", "plainReply", "mediaMode"];
const scalarDraft = draft => Object.fromEntries(draftFields.filter(k => draft[k] !== undefined).map(k => [k, draft[k]]));
const validDraft = d => d && typeof d === "object" && !Array.isArray(d) && typeof d.text === "string" &&
  ["replyTo","agent","topic","mediaMode"].every(k => d[k] === undefined || typeof d[k] === "string") &&
  ["newTopic","plainReply"].every(k => d[k] === undefined || typeof d[k] === "boolean");
const ownerFields = ["workspace","handle","endpoint","realm","address","fingerprint"];
const validOwner = o => o && ownerFields.every(k => typeof o[k] === "string") && !!o.workspace && !!o.address && !!o.fingerprint;
// Native mount handles rotate on a core restart. A fresh renderer can read its
// same verified membership's saved text; in-flight callbacks still fence the
// exact current handle in Store. This never grants a transport capability.
const sameOwner = (a,b) => validOwner(a) && validOwner(b) && ownerFields.filter(k=>k!=="handle").every(k=>a[k]===b[k]);
const token = () => crypto.randomUUID().replaceAll("-", "");

export function savedSendDrafts(storage, draftsKey) {
  const pendingPrefix = draftsKey + ".send-recovery.v1.";
  // Each conversation's draft is its own record, so a write to one chat can never
  // replace another's. The ordinary map at draftsKey is what older versions read:
  // it is rebuilt from these records after every write here, and rebuilt again
  // if another window's stale whole-map write lands in between.
  const recordPrefix = draftsKey + ".draft.v1.";
  const recordKey = conv => recordPrefix + conv;
  const restoredKey = conv => draftsKey + ".restored.v1." + conv;
  const pendingKey = id => pendingPrefix + id;
  const keysWith = prefix => {
    const out=[];
    for(let i=0;i<storage.length;i++){const k=storage.key(i);if(k?.startsWith(prefix))out.push(k.slice(prefix.length));}
    return out;
  };
  const map = () => {
    const value = JSON.parse(storage.getItem(draftsKey) || "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) throw Error("Saved drafts are damaged. Your current draft was kept.");
    return value;
  };
  const legacyRevision = raw => "legacy:"+JSON.stringify(raw ?? null);
  // An older version rewrites the whole map from its own memory, without
  // revisions, and drops empty drafts. When it wrote last, the map speaks for
  // every conversation: its edits and its deletions win over records it never
  // knew. A map nobody has written yet says nothing.
  // An older version still open in another tab rewrites the map from what it
  // loaded long ago; while it is seen, this page's records stay authoritative
  // and none is removed (its edits to chats with records are not adopted).
  let olderLive = false;
  const olderWroteLast = values => !olderLive && storage.getItem(draftsKey)!==null && !Object.values(values).some(v => v && typeof v === "object" && typeof v[revisionField] === "string");
  const ownRecord = conv => {
    const raw=storage.getItem(recordKey(conv));
    if(raw===null)return;
    let r;try{r=JSON.parse(raw);}catch{throw Error("This saved draft is damaged. It was not replaced.");}
    if(!validDraft(r) || typeof r[revisionField]!=="string")throw Error("This saved draft is damaged. It was not replaced.");
    return {draft:scalarDraft(r),revision:r[revisionField],write:typeof r[writeField]==="string"?r[writeField]:undefined};
  };
  const recordSpeaks = (conv,values) => {
    const raw=values[conv];
    if(olderLive && storage.getItem(recordKey(conv))!==null)return true;
    return typeof raw?.[revisionField]==="string" || raw===undefined && !olderWroteLast(values);
  };
  const base = (conv, values = map()) => {
    const raw = values[conv];
    if (raw !== undefined && !validDraft(raw)) throw Error("This saved draft is damaged. It was not replaced.");
    if (recordSpeaks(conv,values)) {
      const r=ownRecord(conv);if(r)return r;
      // This version's map entry without its record is an undone write, never a draft.
      // Entries adopted from an older version carry a "legacy:" revision instead.
      if(raw!==undefined && !raw[revisionField].startsWith("legacy:"))return {draft:{text:""},revision:legacyRevision(undefined)};
    }
    return {draft:raw ? scalarDraft(raw) : {text:""}, revision:raw?.[revisionField] || legacyRevision(raw)};
  };
  // A restoration applies only while the record still holds the revision the
  // send cleared it to. Any newer edit or erase, in any window, wins.
  const restoredOver = (conv,current,owner) => {
    const raw=storage.getItem(restoredKey(conv));
    if(!raw)return;
    let r;try{r=JSON.parse(raw);}catch{return;}
    if(r?.version===1 && r.conversation===conv && (!owner || sameOwner(r.owner,owner)) && validDraft(r.draft) &&
      r.expected===current.revision && r.cleared===JSON.stringify(scalarDraft(current.draft)) && typeof r.write==="string" && r.write===current.write && typeof r.revision==="string") return {draft:scalarDraft(r.draft),revision:r.revision};
  };
  const effective = (conv,values,owner) => {const current=base(conv,values);return restoredOver(conv,current,owner) || current;};
  // Rebuild the older versions' map from the records. Each round reads storage
  // afresh, so a whole-map write from another window that landed meanwhile is
  // repaired on the next round instead of standing. The last step is always a
  // check, never a write: if the map still disagrees, this call fails.
  // `own` is the conversation this call just wrote: its record speaks for it.
  const rounds = 8;
  const project = (own,pending) => {
    for(let round=0;round<=rounds;round++){
      const values=map(),older=olderWroteLast(values),next={};
      for(const conv of new Set([...Object.keys(values),...keysWith(recordPrefix),...(pending?[own]:[])])){
        const raw=values[conv];
        if(raw!==undefined && !validDraft(raw))throw Error("This saved draft is damaged. It was not replaced.");
        let value;
        const mine=conv===own && (pending || ownRecord(conv));
        if(mine)value=restoredOver(conv,mine)||mine;
        else{
          if(!olderLive && !(typeof raw?.[revisionField]==="string" || raw===undefined && !older))storage.removeItem(recordKey(conv));
          if(raw===undefined && older)continue;
          // A damaged record stays as it is for repair; it never blocks other chats.
          try{value=effective(conv,values);}catch{if(raw!==undefined)next[conv]=raw;continue;}
        }
        next[conv]={...scalarDraft(value.draft),[revisionField]:value.revision};
      }
      const text=JSON.stringify(next);
      if(storage.getItem(draftsKey)===text)return;
      if(round===rounds)break;
      storage.setItem(draftsKey,text);
      if(pending)return; // one commit ahead of the record; the caller checks after it
    }
    throw Error("Drafts kept changing in another window. Older versions may show an earlier copy.");
  };
  // Nothing written here is ever undone. Each operation commits first the side
  // where text cannot be lost by a crash or refusal before the second commit:
  // a write commits its record first (the map, read by older versions, still
  // holds the earlier text; a send's original is kept separately); a
  // restoration commits the map first (the record still holds the cleared
  // revision and the original stays kept until released).
  const repair = own => {try{project(own);}catch{ /* reported by the caller's error */ }};
  // The map is only a mirror: rebuild it once more before reporting it unverified.
  const mirror = own => {try{project(own);}catch(e){try{project(own);}catch{throw Error("This draft is saved here, but older versions may still show an earlier copy. "+e.message);}}};
  const validRecord = r => r && r.version===1 && /^[a-f0-9]{32}$/.test(r.id) && typeof r.conversation==="string" &&
    validOwner(r.owner) && validDraft(r.draft) && Object.keys(r.draft).every(k=>draftFields.includes(k)) && typeof r.revision==="string" && typeof r.hadFiles==="boolean";
  const pending = id => {
    const raw=storage.getItem(pendingKey(id));
    if(raw===null)return;
    let r;try{r=JSON.parse(raw);}catch{throw Error("Original send recovery is damaged. The message was kept.");}
    if(!validRecord(r)||r.id!==id)throw Error("Original send recovery is unsupported or damaged. The message was kept.");
    return r;
  };
  const read = (conv,owner) => {const values=map(),current=base(conv,values);return (owner && restoredOver(conv,current,owner)) || current;};
  return {
    read,
    all(owner) {
      const values=map(),convs=new Set(Object.keys(values));
      if(!olderWroteLast(values))for(const conv of keysWith(recordPrefix))convs.add(conv);
      return Object.fromEntries([...convs].map(conv=>[conv,read(conv,owner)]));
    },
    write(conv,draft,revision=token()) {
      if(!validDraft(draft))throw Error("Cannot save an incomplete draft.");
      const values=map(); // a damaged map is kept, never replaced
      // An older version wrote the map last: adopt its edits and deletions into
      // the map first, so this record can never later look superseded by them.
      if(olderWroteLast(values))project();
      storage.setItem(recordKey(conv),JSON.stringify({...scalarDraft(draft),[revisionField]:revision,[writeField]:token()}));
      mirror(conv);
      // A restoration bound to an earlier revision stays inert; it is never removed here.
      return revision;
    },
    prepare(id,conv,owner,draft,revision) {
      if(!validOwner(owner))throw Error("Your current workspace identity is unavailable. The draft was kept.");
      const r={version:1,id,conversation:conv,owner:{...owner},draft:scalarDraft(draft),revision,hadFiles:!!draft.files?.length};
      if(!validRecord(r))throw Error("Cannot retain an incomplete original draft.");
      const old=pending(id);
      if(old){
        if(old.conversation!==conv || !sameOwner(old.owner,owner) || JSON.stringify(old.draft)!==JSON.stringify(r.draft))
          throw Error("This send ID already belongs to another original draft.");
        return old;
      }
      storage.setItem(pendingKey(id),JSON.stringify(r));return r;
    },
    pending(id,conv,owner) {
      const r=pending(id);
      if(!r)return;
      if(r.conversation!==conv || !sameOwner(r.owner,owner))throw Error("Original draft belongs to another conversation or identity. The message was kept.");
      return clone(r);
    },
    entries(owner) {
      const ids=[];
      for(let i=0;i<storage.length;i++){const k=storage.key(i);if(k?.startsWith(pendingPrefix))ids.push(k.slice(pendingPrefix.length));}
      return ids.flatMap(id=>{try{const r=pending(id);return sameOwner(r.owner,owner)?[clone(r)]:[];}catch{return [];}});
    },
    restore(id,conv,owner) {
      const r=this.pending(id,conv,owner);
      if(!r || !r.revision || read(conv,owner).revision!==r.revision)return;
      // The record is never overwritten here. A newer write in another window
      // wins even if it races this one: the restoration applies only while its
      // expected revision still matches. The map shows it to older versions.
      // Bound to the exact cleared revision AND its content, so a later write
      // reusing that revision with other content never brings this back.
      let now=read(conv,owner);
      // A base read from an older version's map has no write of its own: save
      // that same content as a record first, so the restoration binds to it.
      if(!now.write){this.write(conv,now.draft,now.revision);now=read(conv,owner);if(now.revision!==r.revision||!now.write)return;}
      const cleared=JSON.stringify(scalarDraft(now.draft));
      const overlay={version:1,conversation:conv,owner:{...owner},draft:r.draft,expected:r.revision,cleared,write:now.write,revision:token()};
      project(conv,{draft:scalarDraft(r.draft),revision:overlay.revision});
      try{storage.setItem(restoredKey(conv),JSON.stringify(overlay));}catch(e){repair(conv);throw e;}
      mirror(conv);
      const current=read(conv,owner);
      return current.revision===overlay.revision ? current : undefined;
    },
    release(id,conv,owner) {
      if(this.pending(id,conv,owner))storage.removeItem(pendingKey(id));
    },
    nextRevision:token,
    // Called for another tab's storage change: true once an older version's
    // whole-map write is seen while this page runs.
    olderWriterSeen() {
      if(!olderLive){try{const values=map();olderLive=storage.getItem(draftsKey)!==null && Object.keys(values).length>0 && !Object.values(values).some(v=>v&&typeof v==="object"&&typeof v[revisionField]==="string");}catch{ /* a damaged map is reported by reads */ }}
      return olderLive;
    },
    isStorageKey(key) {return key===draftsKey || key?.startsWith(pendingPrefix) || key?.startsWith(recordPrefix) || key?.startsWith(draftsKey+".restored.v1.");},
  };
}
