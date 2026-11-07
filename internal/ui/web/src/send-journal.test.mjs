import test from "node:test";
import assert from "node:assert/strict";
import { readPending, saveBatch, acknowledge, recordFailure, onJournalChange, savedSendDrafts } from "./send-journal.mjs";
const storage = () => { const data = new Map(); return { getItem:k=>data.get(k) ?? null,setItem:(k,v)=>data.set(k,v) }; };
const entry = (id="a".repeat(32), pid="agent-one") => ({id,conversation:"chat",endpoint:"/api/dm/agent/ask",request:{id,pid,body:"same text",kind:"question",topic:"topic",files:["stage-exact"]},createdAt:"2026-10-10T12:00:00Z"});
test("crash after gesture retains exact request and staged IDs without replay",()=>{const s=storage(),original=entry();saveBatch(s,"one",[original]);original.request.files[0]="changed";const recovered=readPending(s,"one");assert.equal(recovered[0].request.files[0],"stage-exact");assert.deepEqual(readPending(s,"two"),[]);recordFailure(s,"one",original.id,"Missing staged file");assert.equal(readPending(s,"one")[0].request.body,"same text");assert.equal(readPending(s,"one")[0].error,"Missing staged file");});
test("fanout success acknowledges only exact target; duplicate text stays independent",()=>{const s=storage(),a=entry(),b=entry("b".repeat(32),"agent-two");saveBatch(s,"one",[a,b]);acknowledge(s,"one",a.id);assert.deepEqual(readPending(s,"one").map(e=>e.id),[b.id]);saveBatch(s,"one",[b]);assert.equal(readPending(s,"one").length,1);});
test("same ID cannot replace frozen audience/body/kind/reply or attachment intent",()=>{for(const field of ["pid","body","kind","topic","files"]){const s=storage(),a=entry();saveBatch(s,"one",[a]);const b=structuredClone(a);b.request[field]=field==="files"?["different"]:"changed";assert.throws(()=>saveBatch(s,"one",[b]));assert.deepEqual(readPending(s,"one")[0].request,a.request);}});
test("storage failure is observable before caller clears draft or starts send",()=>{const s=storage();s.setItem=()=>{throw Error("quota")};let cleared=false,submitted=false;assert.throws(()=>{saveBatch(s,"one",[entry()]);cleared=true;submitted=true;});assert.equal(cleared,false);assert.equal(submitted,false);});
test("corrupt and action-authority entries fail closed",()=>{const s=storage();assert.throws(()=>saveBatch(s,"one",[{...entry(),endpoint:"/api/act",request:{do:"approve",send_id:"a".repeat(32)}}]));s.setItem("agentnet.android.send-journal.v1.one","broken");assert.throws(()=>readPending(s,"one"));});
test("change notices reach this package's listeners only, and a failing listener cannot undo a write",()=>{const s=storage();let seen=0;const off=onJournalChange(()=>{seen++;});const bad=onJournalChange(()=>{throw Error("listener");});saveBatch(s,"one",[entry()]);assert.equal(seen,1);assert.equal(readPending(s,"one").length,1,"written despite a failing listener");acknowledge(s,"one","a".repeat(32));assert.equal(seen,2);off();bad();saveBatch(s,"one",[entry()]);assert.equal(seen,2,"unsubscribed");});

test("bound follow-up journal preserves exact original and send identity after a crash",()=>{
 const s=storage(),a={...entry(),endpoint:"/api/request/followup",request:{id:"a".repeat(32),conv:"c".repeat(64),ref:{id:"b".repeat(32),fingerprint:"bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb"},body:"Use English",files:["saved-stage"]}};
 saveBatch(s,"one",[a]);a.request.ref.id="d".repeat(32);
 const recovered=readPending(s,"one")[0];assert.equal(recovered.request.ref.id,"b".repeat(32));
 assert.throws(()=>saveBatch(s,"one",[a]));assert.equal(readPending(s,"one")[0].request.files[0],"saved-stage");
});


const originalDraft={text:"  **Original** [@Alice](agentnet:person/abc)\n",replyTo:"parent",topic:"topic",agent:"",newTopic:false,plainReply:true,mediaMode:"original"};
const owner={workspace:"one",handle:"mount-a",endpoint:"https://fixture.invalid",realm:"fixture",address:"fixture/phone",fingerprint:"synthetic-public-key"};
const draftKey="agentnet.messenger.drafts.one",send="c".repeat(32),conv="conversation";
function draftWorld(){
 const data=new Map();let fault,interleave,before;
 const storage={getItem:k=>data.get(k)??null,setItem(k,v){if(fault?.(k,v))throw Error("synthetic quota");before?.(k,v);data.set(k,v);interleave?.(k,v);},removeItem:k=>data.delete(k),get length(){return data.size;},key:i=>[...data.keys()][i]??null};
 const saved=savedSendDrafts(storage,draftKey);
 const revision=saved.write(conv,{text:""});saved.prepare(send,conv,owner,originalDraft,revision);
 return {data,storage,saved,revision,fault:f=>fault=f,interleave:f=>interleave=f,before:f=>before=f};
}
test("admission ACK removes Retry request, not original editor context",()=>{
 const w=draftWorld();const request={id:send,conversation:conv,endpoint:"/api/dm/send",request:{id:send,conv,body:"transformed"},createdAt:"fixture"};
 saveBatch(w.storage,"one",[request]);acknowledge(w.storage,"one",send);
 assert.deepEqual(readPending(w.storage,"one"),[]);assert.deepEqual(w.saved.pending(send,conv,owner).draft,originalDraft);
});
test("confirmed original restoration is durable conditional draft, never a send request",()=>{
 const w=draftWorld();const restored=w.saved.restore(send,conv,owner);assert.deepEqual(restored.draft,originalDraft);
 assert.deepEqual(savedSendDrafts(w.storage,draftKey).read(conv,owner).draft,originalDraft);
 assert.deepEqual(readPending(w.storage,"one"),[]);assert.equal(w.saved.restore(send,conv,owner),undefined,"same recovery cannot apply twice");
});
for(const text of ["newer",originalDraft.text,""])test("newer durable revision wins even with "+JSON.stringify(text),()=>{
 const w=draftWorld();w.saved.write(conv,{text});assert.equal(w.saved.restore(send,conv,owner),undefined);
 assert.equal(savedSendDrafts(w.storage,draftKey).read(conv,owner).draft.text,text);
});
for(const field of ["workspace","address","fingerprint","endpoint","realm"])test("original metadata refuses changed "+field,()=>{
 const w=draftWorld();assert.throws(()=>w.saved.restore(send,conv,{...owner,[field]:"other"}));
 assert(w.saved.pending(send,conv,owner),"original is retained");
});
test("conversation is bound independently from same send ID",()=>{const w=draftWorld();assert.throws(()=>w.saved.restore(send,"another",owner));assert(w.saved.pending(send,conv,owner));});
test("newer writer interleaved at restore cannot be overwritten",()=>{
 const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);let once=true;
 w.interleave(k=>{if(once&&k.includes(".restored.v1.")){once=false;other.write(conv,{text:"newer in other window"});}});
 assert.equal(w.saved.restore(send,conv,owner),undefined);assert.equal(other.read(conv,owner).draft.text,"newer in other window");
});
test("restoration leaves other conversations untouched",()=>{const w=draftWorld();w.saved.write("other",{text:"other draft"});w.saved.restore(send,conv,owner);assert.equal(w.saved.read("other",owner).draft.text,"other draft");});
test("older reader-compatible draft and older empty writes invalidate recovery",()=>{
 const w=draftWorld();assert.equal(JSON.parse(w.storage.getItem(draftKey))[conv].text,"");
 w.storage.setItem(draftKey,JSON.stringify({[conv]:{text:"old-client newer"}}));assert.equal(w.saved.restore(send,conv,owner),undefined);
 w.storage.setItem(draftKey,"{}");assert.equal(w.saved.restore(send,conv,owner),undefined);
 assert(w.saved.pending(send,conv,owner),"rollback never deletes unknown original metadata");
});
for(const raw of ["broken",JSON.stringify({version:999}),JSON.stringify({version:1,id:send})])test("malformed/unknown original recovery fails closed: "+raw,()=>{
 const w=draftWorld(),key=draftKey+".send-recovery.v1."+send;w.data.set(key,raw);
 assert.throws(()=>w.saved.restore(send,conv,owner));assert.equal(w.data.get(key),raw);
 assert.equal(w.saved.read(conv,owner).draft.text,"");
});
test("quota failure cannot clear original active draft or unresolved journal",()=>{
 const w=draftWorld();w.saved.write(conv,originalDraft);const request={id:"d".repeat(32),conversation:conv,endpoint:"/api/dm/send",request:{id:"d".repeat(32),conv,body:"wire"}};saveBatch(w.storage,"one",[request]);
 w.fault(k=>k.includes(".send-recovery.v1."));assert.throws(()=>w.saved.prepare(request.id,conv,owner,originalDraft,"future"));
 assert.deepEqual(w.saved.read(conv,owner).draft,originalDraft);assert.equal(readPending(w.storage,"one").length,1);
});
test("restore storage failure keeps original metadata and base draft",()=>{
 const w=draftWorld();w.fault(k=>k.includes(".restored.v1."));assert.throws(()=>w.saved.restore(send,conv,owner));
 assert.deepEqual(w.saved.pending(send,conv,owner).draft,originalDraft);assert.equal(w.saved.read(conv,owner).draft.text,"");
});
test("malformed legacy map is retained rather than overwritten",()=>{
 const w=draftWorld();w.storage.setItem(draftKey,"broken");assert.throws(()=>w.saved.write(conv,{text:"replacement"}));assert.equal(w.storage.getItem(draftKey),"broken");
});
test("same ID cannot replace original context or owner",()=>{
 const w=draftWorld();assert.throws(()=>w.saved.prepare(send,conv,owner,{...originalDraft,plainReply:false},w.revision));
 assert.throws(()=>w.saved.prepare(send,conv,{...owner,fingerprint:"other"},originalDraft,w.revision));
 assert.deepEqual(w.saved.pending(send,conv,owner).draft,originalDraft);
});

test("aborted preparation owns only newly added Retry records",()=>{
 const s=storage(),a=entry(),b=entry("e".repeat(32));assert.deepEqual(saveBatch(s,"one",[a]),[a.id]);
 assert.deepEqual(saveBatch(s,"one",[a]),[]);const added=saveBatch(s,"one",[a,b]);assert.deepEqual(added,[b.id]);
 for(const id of added)acknowledge(s,"one",id);assert.deepEqual(readPending(s,"one").map(e=>e.id),[a.id]);
});

test("unfamiliar File metadata cannot become a recovered draft",()=>{
 const w=draftWorld(),key=draftKey+".send-recovery.v1."+send,r=w.saved.pending(send,conv,owner);
 r.draft.files=[{file:{},staged:"not-a-capability"}];const raw=JSON.stringify(r);w.data.set(key,raw);
 assert.throws(()=>w.saved.restore(send,conv,owner));assert.equal(w.data.get(key),raw);assert.equal(w.saved.read(conv,owner).draft.text,"");
});

test("command-shaped unknown metadata never becomes a Retry request",()=>{
 const w=draftWorld(),key=draftKey+".send-recovery.v1."+send,r=w.saved.pending(send,conv,owner);
 r.draft.do="send";const raw=JSON.stringify(r);w.data.set(key,raw);
 assert.throws(()=>w.saved.restore(send,conv,owner));assert.deepEqual(readPending(w.storage,"one"),[]);assert.equal(w.data.get(key),raw);
});

test("rollback reader retains the newly restored active draft",()=>{
 const w=draftWorld();assert.deepEqual(w.saved.restore(send,conv,owner).draft,originalDraft);
 // Published v0.10.9 loadDrafts reads the ordinary draft map's text field.
 // A cold rollback has no new renderer's membership memory or overlay reader.
 const older=JSON.parse(w.storage.getItem(draftKey));
 assert.equal(older[conv].text,originalDraft.text,"rollback must not hide the restored original active draft");
});

// Preservation witnesses (falsification 2026-11-06T17:57Z): a second writer commits
// BEFORE this writer's own storage commit. Its newer text, its erase and another
// chat's edit must all survive, both for current readers and in the map an older
// version reads. Hooked at every commit point restore and ordinary writes make.
const hookPoints={
 "restore overlay commit":{key:draftKey+".restored.v1."+conv,act:w=>w.saved.restore(send,conv,owner)},
 "restore map commit":{key:draftKey,act:w=>w.saved.restore(send,conv,owner)},
 "ordinary write map commit":{key:draftKey,act:w=>{w.saved.write("third",{text:"third chat"});}},
};
for(const [point,{key,act}] of Object.entries(hookPoints))for(const kind of ["newer-same-conversation","intentional-erase","newer-other-conversation"])
 test(`${point}: ${kind} injected before the commit is preserved`,()=>{
  const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);
  other.write("other",{text:"prior other conversation"});
  const target=kind==="newer-other-conversation"?"other":conv,newer=kind==="intentional-erase"?"":"newer synthetic input";
  let injected=false;
  w.before(k=>{if(injected||k!==key)return;injected=true;other.write(target,{text:newer});});
  let restored,error;try{restored=act(w);}catch(e){error=String(e);}
  const active=savedSendDrafts(w.storage,draftKey).read(target,owner),legacy=JSON.parse(w.storage.getItem(draftKey));
  assert.equal(error,undefined);assert.equal(injected,true,"counterexample executed");
  assert.equal(active.draft.text,newer,"current reader keeps the newer input");
  assert.equal(legacy[target].text,newer,"older reader's map keeps the newer input");
  if(kind!=="newer-other-conversation")assert.equal(legacy.other.text,"prior other conversation");
  if(target===conv&&point.startsWith("restore"))assert.equal(restored,undefined,"superseded recovery does not report success");
  if(point.startsWith("ordinary"))assert.equal(legacy.third.text,"third chat");
  assert(w.saved.pending(send,conv,owner),"original recovery retained");
 });

// v0.10.9's own writer: rewrites the whole map from memory, scalar fields only,
// empty drafts dropped (git show v0.10.9:internal/ui/web/src/store.ts saveDrafts).
const oldWriter=(storage,drafts)=>{const keep={};for(const [k,d] of Object.entries(drafts))if(d.text||d.replyTo||d.agent||d.topic||d.newTopic||d.mediaMode)keep[k]={text:d.text,replyTo:d.replyTo,agent:d.agent,topic:d.topic,newTopic:d.newTopic,plainReply:d.plainReply,mediaMode:d.mediaMode};storage.setItem(draftKey,JSON.stringify(keep));};
const oldReader=storage=>JSON.parse(storage.getItem(draftKey)||"{}");
test("rollback then older writer: its edit and its deletion win after upgrade, nothing resurrects",()=>{
 const w=draftWorld();w.saved.write("kept",{text:"kept draft"});w.saved.write("gone",{text:"to be erased by old"});
 assert(w.saved.restore(send,conv,owner));
 const ram=oldReader(w.storage);assert.equal(ram[conv].text,originalDraft.text,"cold older reader sees the restored draft");
 ram.kept.text="edited in old";delete ram.gone;oldWriter(w.storage,ram);
 const up=savedSendDrafts(w.storage,draftKey);
 assert.equal(up.read("kept",owner).draft.text,"edited in old");
 assert.equal(up.read("gone",owner).draft.text,"","old deletion is not undone by the stale record");
 assert.equal(up.read(conv,owner).draft.text,originalDraft.text);
 up.write("new",{text:"after upgrade"});
 const map=oldReader(w.storage);assert.equal(map.gone,undefined,"deletion still holds after a new write");
 assert.equal(map.kept.text,"edited in old");assert.equal(map.new.text,"after upgrade");
 assert.equal(up.read("gone",owner).draft.text,"");assert.equal(Object.keys(up.all(owner)).includes("gone"),false);
});
test("rollback then older writer erasing everything: no record or restoration resurrects",()=>{
 const w=draftWorld();w.saved.write("other",{text:"other"});assert(w.saved.restore(send,conv,owner));
 oldWriter(w.storage,{});
 const up=savedSendDrafts(w.storage,draftKey);assert.equal(up.read(conv,owner).draft.text,"");assert.equal(up.read("other",owner).draft.text,"");
 up.write("fresh",{text:"fresh"});const map=oldReader(w.storage);
 assert.deepEqual(Object.keys(map),["fresh"]);assert.equal(up.read(conv,owner).draft.text,"");
});
test("first write into empty storage is kept",()=>{
 const data=new Map(),s={getItem:k=>data.get(k)??null,setItem:(k,v)=>data.set(k,v),removeItem:k=>data.delete(k),get length(){return data.size;},key:i=>[...data.keys()][i]??null};
 const d=savedSendDrafts(s,draftKey);d.write("a",{text:"first"});
 assert.equal(savedSendDrafts(s,draftKey).read("a",owner).draft.text,"first");assert.equal(oldReader(s).a.text,"first");
});
test("map quota during restore keeps original, base draft and older map unchanged",()=>{
 const w=draftWorld(),mapBefore=w.storage.getItem(draftKey);w.fault(k=>k===draftKey);
 assert.throws(()=>w.saved.restore(send,conv,owner));w.fault(undefined);
 assert.equal(w.storage.getItem(draftKey),mapBefore);assert.equal(w.storage.getItem(draftKey+".restored.v1."+conv),null);
 assert.equal(w.saved.read(conv,owner).revision,w.revision);assert(w.saved.pending(send,conv,owner));
 assert.deepEqual(w.saved.restore(send,conv,owner).draft,originalDraft,"retry after space frees");
});
test("map quota during ordinary write: record kept, older map keeps earlier text, failure names it",()=>{
 const w=draftWorld();w.saved.write("a",{text:"before"});w.fault(k=>k===draftKey);
 assert.throws(()=>w.saved.write("a",{text:"after"}),/older versions may still show an earlier copy/);w.fault(undefined);
 assert.equal(w.saved.read("a",owner).draft.text,"after","nothing written is reverted");assert.equal(oldReader(w.storage).a.text,"before","older reader keeps a real earlier version");
 w.saved.write("b",{text:"x"});assert.equal(oldReader(w.storage).a.text,"after","next save brings the mirror up to date");
});
test("map quota during a send clear: older map keeps the text, original kept",()=>{
 const w=draftWorld();w.saved.write(conv,{text:"typed"});const before=w.saved.read(conv,owner).revision,next=w.saved.nextRevision();
 w.saved.prepare("d".repeat(32),conv,owner,{text:"typed"},next);w.fault(k=>k===draftKey);
 assert.throws(()=>w.saved.write(conv,{text:""},next));w.fault(undefined);
 assert.equal(oldReader(w.storage)[conv].text,"typed","cold older reader never sees an unconfirmed clear");
 assert.notEqual(w.saved.read(conv,owner).revision,before);assert(w.saved.pending("d".repeat(32),conv,owner),"original kept for this version");
});
test("crash between restoration and map rebuild: current reader restores, next write repairs the map",()=>{
 const w=draftWorld();let crash=true;w.fault(k=>k===draftKey&&crash);
 assert.throws(()=>w.saved.restore(send,conv,owner));
 // simulate the crash landing after the overlay commit: put it back as a crashed process would leave it
 crash=false;const fresh=draftWorld();
 const overlay={version:1,conversation:conv,owner,draft:originalDraft,expected:fresh.revision,cleared:JSON.stringify({text:""}),revision:"f".repeat(32)};
 fresh.storage.setItem(draftKey+".restored.v1."+conv,JSON.stringify(overlay));
 const up=savedSendDrafts(fresh.storage,draftKey);assert.equal(up.read(conv,owner).draft.text,originalDraft.text);
 assert.equal(fresh.saved.restore(send,conv,owner),undefined,"never applied twice");
 up.write("other",{text:"x"});assert.equal(oldReader(fresh.storage)[conv].text,originalDraft.text,"map repaired");
});
test("damaged record of one chat never blocks saving another",()=>{
 const w=draftWorld();w.saved.write("bad",{text:"x"});w.storage.setItem(draftKey+".draft.v1.bad","{broken");
 w.saved.write("good",{text:"saved"});assert.equal(w.saved.read("good",owner).draft.text,"saved");
 assert.throws(()=>w.saved.read("bad",owner));assert.equal(w.storage.getItem(draftKey+".draft.v1.bad"),"{broken","kept for repair");
});

// Contention up to and including the LAST allowed map commit: the call's final
// step is a check, so it either converged or reports failure; a cold older
// reader right afterwards (no later save) still sees every newer input.
for(const kind of ["newer-same-conversation","intentional-erase","newer-other-conversation"])
 for(const op of ["restore","write"])test(`${op}: competing writer before every allowed map commit, cold older read: ${kind}`,()=>{
  const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);other.write("other",{text:"prior other conversation"});
  const target=kind==="newer-other-conversation"?"other":conv;let n=0,busy=false,last;
  w.before(k=>{if(busy||k!==draftKey||n>=8)return;busy=true;n++;last=kind==="intentional-erase"&&n===8?"":"newer "+n;other.write(target,{text:last});busy=false;});
  let error,result;try{result=op==="restore"?w.saved.restore(send,conv,owner):w.saved.write("third",{text:"third"});}catch(e){error=String(e);}
  assert.equal(n,8,"interleaved before the last allowed commit");
  const cold=oldReader(w.storage),fresh=savedSendDrafts(w.storage,draftKey);
  assert.equal(fresh.read(target,owner).draft.text,last,"current reader keeps the newest input");
  if(last)assert.equal(cold[target]?.text,last,"cold older reader keeps the newest input");else assert.equal(cold[target]?.text||"","","cold older reader keeps the erase");
  if(kind!=="newer-other-conversation"){assert.equal(cold.other.text,"prior other conversation");if(op==="restore")assert.equal(result,undefined,"superseded restore is not success");}
  if(error)assert.match(error,/another window/);
  if(op==="write"&&!error)assert.equal(cold.third.text,"third");
  if(op==="write"){assert.equal(error,undefined,"mirror verified after one more rebuild");assert.equal(fresh.read("third",owner).draft.text,"third");assert.equal(cold.third.text,"third");}
 });
test("contention that never stops: the call reports failure, current readers keep the newest input",()=>{
 const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);let n=0,busy=false;
 w.before(k=>{if(busy||k!==draftKey)return;busy=true;n++;other.write("other",{text:"newer "+n});busy=false;});
 assert.throws(()=>w.saved.write("third",{text:"third"}),/another window/);w.before(undefined);
 assert.equal(savedSendDrafts(w.storage,draftKey).read("other",owner).draft.text,"newer "+n);
 assert(w.saved.pending(send,conv,owner));
});

// Same chat, same record key. A rollback after a failure undoes only this call's
// own record or restoration; a newer write or erase from another window stays.
for(const newer of ["newer in other window",""])for(const op of ["write","restore"])
 test(`${op} failing after a same-chat write ${JSON.stringify(newer)} does not roll it back`,()=>{
  // The other window commits between this call's own record/restoration commit and
  // its map commit, which then fails (quota).
  const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);let busy=false,done=false;
  w.fault(k=>{if(busy||done||k!==draftKey)return false;busy=true;other.write(conv,{text:newer});busy=false;done=true;return true;});
  // a write's map is only a mirror: one verified rebuild recovers it; a restoration's own commit failed
  if(op==="write")w.saved.write(conv,{text:"this window"});else assert.throws(()=>w.saved.restore(send,conv,owner),/synthetic quota/);
  assert.equal(done,true);
  const fresh=savedSendDrafts(w.storage,draftKey);assert.equal(fresh.read(conv,owner).draft.text,newer,"newer same-chat input kept");
  assert.equal(oldReader(w.storage)[conv].text,newer,"older reader's map agrees");
  assert(w.saved.pending(send,conv,owner),"original recovery retained");
 });
// Same-key commit order: whichever record commits last is the one both readers see.
for(const newer of ["newer in other window",""])for(const when of ["before","after"])
 test(`same-chat writer ${when} this record commit (${JSON.stringify(newer)}): readers agree, no third value`,()=>{
  const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey);let done=false;
  const hook=k=>{if(done||k!==draftKey+".draft.v1."+conv)return;done=true;other.write(conv,{text:newer});};
  if(when==="before")w.before(hook);else w.interleave(hook);
  w.saved.write(conv,{text:"this window"});assert.equal(done,true);
  const expected=when==="before"?"this window":newer,fresh=savedSendDrafts(w.storage,draftKey);
  assert.equal(fresh.read(conv,owner).draft.text,expected);assert.equal(oldReader(w.storage)[conv].text,expected);
  assert.equal(fresh.read(conv,owner).revision,oldReader(w.storage)[conv].__an_draft_revision);
 });
test("a write never removes a restoration; a superseded one stays inert",()=>{
 const w=draftWorld();assert(w.saved.restore(send,conv,owner));
 w.saved.write(conv,{text:"edited"});
 assert.notEqual(w.storage.getItem(draftKey+".restored.v1."+conv),null,"no compare-then-remove of a shared key");
 const fresh=savedSendDrafts(w.storage,draftKey);assert.equal(fresh.read(conv,owner).draft.text,"edited");
 assert.equal(JSON.parse(w.storage.getItem(draftKey))[conv].text,"edited");
});
// Bezos 15:06Z: no failure path compares a record or restoration and then
// mutates it back. A writer committing during this call's failure handling stays.
for(const prior of [null,"prior"])for(const newer of ["newer text",""])for(const target of ["record","restoration"])
 test(`failure handling never reverts a record or restoration: prior=${prior} newer=${JSON.stringify(newer)} ${target}`,()=>{
  const w=draftWorld(),other=savedSendDrafts(w.storage,draftKey),chat=target==="record"&&prior===null?"fresh":conv;
  if(prior!==null&&target==="record")w.saved.write(conv,{text:prior});
  // a write commits its record first, so its failure point is the map commit after it;
  // a restoration commits the map first, so its failure point is its own commit.
  // Another window writes during the following repair.
  const key=target==="record"?draftKey:draftKey+".restored.v1."+conv;let done=false;
  w.fault(k=>{if(k!==key||done)return false;done=true;return true;});
  w.before(k=>{if(done!==true||k!==draftKey)return;done="injected";other.write(chat,{text:newer});});
  let error;try{target==="record"?w.saved.write(chat,{text:"this call"}):w.saved.restore(send,conv,owner);}catch(e){error=String(e);}
  if(target==="restoration")assert.match(error,/synthetic quota/);
  assert.equal(done,"injected","writer committed during failure handling");
  const fresh=savedSendDrafts(w.storage,draftKey);
  assert.equal(fresh.read(chat,owner).draft.text,newer);assert.equal(JSON.parse(w.storage.getItem(draftKey))[chat]?.text??"",newer);
  assert(w.saved.pending(send,conv,owner));
 });

// Superseded restorations stay stored but can never come back.
test("superseded restoration never resurrects: repeated erase, reload, revision reuse",()=>{
 const w=draftWorld();assert(w.saved.restore(send,conv,owner));
 for(let i=0;i<5;i++){w.saved.write(conv,{text:""});w.saved.write(conv,{text:"typed "+i});}
 w.saved.write(conv,{text:""});
 const reload=savedSendDrafts(w.storage,draftKey);assert.equal(reload.read(conv,owner).draft.text,"");assert.equal(oldReader(w.storage)[conv].text,"");
 // the cleared revision reused with other content stays inert
 reload.write(conv,{text:"reused revision"},w.revision);
 assert.equal(savedSendDrafts(w.storage,draftKey).read(conv,owner).draft.text,"reused revision");assert.equal(oldReader(w.storage)[conv].text,"reused revision");
 // only the exact cleared state brings back the bound restoration (same state as when it applied)
 reload.write(conv,{text:""},w.revision);assert.equal(reload.read(conv,owner).draft.text,originalDraft.text);
});
test("restorations do not accumulate: one per conversation",()=>{
 const w=draftWorld();
 for(let i=0;i<20;i++){const id=(i+10).toString(16).padStart(32,"e"),rev=w.saved.write(conv,{text:""});w.saved.prepare(id,conv,owner,{text:"o"+i},rev);assert(w.saved.restore(id,conv,owner));w.saved.release(id,conv,owner);}
 assert.equal([...w.data.keys()].filter(k=>k.includes(".restored.v1.")).length,1);
 assert.equal(w.saved.read(conv,owner).draft.text,"o19");
});

// Crash at every commit: a snapshot after each underlying storage commit is what
// a killed process leaves (no catch, no repair runs). Oracles per prefix:
// a send clear never shows the cleared state to a cold older reader before the
// clear completes, and the original stays kept for this version; a restoration
// shows the original to the cold reader from its first commit on, and this
// version keeps the original until it is released.
const crashWorld=()=>{const w=draftWorld(),snaps=[];w.interleave(()=>snaps.push(new Map(w.data)));return {w,snaps};};
const readers=snap=>{const s={getItem:k=>snap.get(k)??null,setItem(){throw Error("read-only")},removeItem(){throw Error("read-only")},get length(){return snap.size;},key:i=>[...snap.keys()][i]??null};
 return {cold:JSON.parse(snap.get(draftKey)||"{}")[conv]?.text??"",current:savedSendDrafts(s,draftKey).read(conv,owner).draft.text,original:savedSendDrafts(s,draftKey).pending(send,conv,owner)};};
test("crash at every commit of a send clear",()=>{
 const data=new Map(),snaps=[],storage={getItem:k=>data.get(k)??null,setItem(k,v){data.set(k,v);snaps.push(new Map(data));},removeItem(k){data.delete(k);snaps.push(new Map(data));},get length(){return data.size;},key:i=>[...data.keys()][i]??null};
 const d=savedSendDrafts(storage,draftKey);d.write(conv,{text:"typed"});const start=snaps.length;
 const next=d.nextRevision();d.prepare(send,conv,owner,{text:"typed"},next);d.write(conv,{text:""},next);
 const prefixes=snaps.slice(start);assert(prefixes.length>=3,"prepare, record and map commits observed");
 prefixes.forEach((snap,i)=>{const r=readers(snap),last=i===prefixes.length-1;
  if(!last)assert.equal(r.cold,"typed",`prefix ${i}: cold older reader keeps the text`);
  assert(r.current==="typed"||r.original,`prefix ${i}: this version shows the text or keeps the original`);});
});
test("crash at every commit of a restoration",()=>{
 const {w,snaps}=crashWorld();const start=snaps.length;assert(w.saved.restore(send,conv,owner));
 const prefixes=snaps.slice(start);assert(prefixes.length>=2,"map and restoration commits observed");
 prefixes.forEach((snap,i)=>{const r=readers(snap);
  assert.equal(r.cold,originalDraft.text,`prefix ${i}: cold older reader shows the original`);
  assert(r.current===originalDraft.text||r.original,`prefix ${i}: this version shows it or keeps the original`);
  assert(r.original,`prefix ${i}: original kept until released`);});
});

// An older v0.10.9 tab left open: it loaded the map once and rewrites all of it
// from that memory on each change. The new page sees it through a storage event.
test("older tab still open: this page's drafts are never removed or replaced by its stale map",()=>{
 const w=draftWorld();w.saved.write("known",{text:"known before"});
 const oldRam=oldReader(w.storage); // the older tab's load-time memory
 w.saved.write("known",{text:"typed here later"});w.saved.write("fresh",{text:"new chat here"});
 oldRam.mine={text:"typed in old tab"};oldWriter(w.storage,oldRam); // its next keystroke
 assert.equal(w.saved.olderWriterSeen(),true);
 w.saved.write("third",{text:"x"});oldWriter(w.storage,oldRam);w.saved.write("third",{text:"y"});
 const cur=savedSendDrafts(w.storage,draftKey);cur.olderWriterSeen();
 assert.equal(w.saved.read("known",owner).draft.text,"typed here later","stale older copy does not replace newer input");
 assert.equal(w.saved.read("fresh",owner).draft.text,"new chat here","chat unknown to the older tab is kept");
 assert.equal(w.saved.read("mine",owner).draft.text,"typed in old tab","older tab's own new chat is adopted");
 assert.equal(oldReader(w.storage).fresh.text,"new chat here","mirror re-shows this page's drafts");
});
test("cold re-upgrade (no older tab running) still adopts the older version's deletions",()=>{
 const w=draftWorld();w.saved.write("gone",{text:"x"});const ram=oldReader(w.storage);delete ram.gone;oldWriter(w.storage,ram);
 const up=savedSendDrafts(w.storage,draftKey);up.write("other",{text:"y"});assert.equal(up.read("gone",owner).draft.text,"");
});
