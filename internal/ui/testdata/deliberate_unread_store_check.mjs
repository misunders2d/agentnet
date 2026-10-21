// Actual Comic Store; the fake provider only models local action/CAS results.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
import {api,errorText} from '../web/src/api.ts';
import model from './comic_model.mjs';
import {pendingSends} from '../static/optimistic.mjs';
import {share} from '../web/src/structural.ts';
import {mainPreferences,personRoots} from '../skins/shared/person-topics.mjs';
const scheduleSource=stripTypeScriptTypes((await readFile(new URL('../web/src/schedule-saves.ts',import.meta.url),'utf8'))
 .replace(/^import .*;$/gm,'')).replace(/^export /gm,'');
const source=stripTypeScriptTypes((await readFile(new URL('../web/src/store.ts',import.meta.url),'utf8'))
 .replace(/^import .*;$/gm,'').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;')).replace(/^export /gm,'');
const conv='a'.repeat(64),topic='b'.repeat(32),mid='c'.repeat(32),next='d'.repeat(32),sibling='e'.repeat(32);
const fp='11111111-11111111-11111111-11111111',otherFP='22222222-22222222-22222222-22222222';
const tick=()=>new Promise(r=>setImmediate(r));
const drain=async()=>{for(let i=0;i<8;i++)await tick();};
const deferred=()=>{let resolve,reject;const promise=new Promise((r,j)=>{resolve=r;reject=j;});return{promise,resolve,reject};};
function fixture(saved={}) {
 const rows=[{id:mid,kind:'message',dir:'in',body:'anchor',topic:'',unread:false},{id:next,kind:'message',dir:'out',body:'sent',topic:'',unread:false},{id:sibling,kind:'message',dir:'in',topic,body:'sibling',unread:false}];
 const calls=[],values=new Map(),listeners=new Set(),visibility=new Set(),revisions=new Map();
 const env={document:{visibilityState:'visible',hasFocus:()=>true,addEventListener(){},removeEventListener(){}},window:{addEventListener(){},removeEventListener(){}},
  localStorage:{getItem:k=>values.get(k)||null,setItem:(k,v)=>values.set(k,v)},setTimeout:(fn,ms)=>{const t=setTimeout(fn,ms);if(ms)t.unref();return t;},clearTimeout};
 const f={rows,calls,readGate:null,saved,dmLoads:[]};
 f.view=()=>({id:conv,messages:structuredClone(rows),topics:[{id:topic}],agents:[]});
 f.holdDM=()=>{const d=deferred();f.dmLoads.push(d.promise);return d;};
 const host={platform:'android',workspace:{id:'default',handle:'f'.repeat(32)},workspaces:{state:()=>saved},
  android:{visible:()=>true,onVisibility:fn=>{visibility.add(fn);return()=>visibility.delete(fn);}},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},
  async api(path,body){calls.push({path,body:body&&structuredClone(body)});
   if(path.startsWith('/api/overview'))return{version:'test',me:{},dms:[{id:conv,unread:rows.filter(m=>m.unread).length}],threads:[]};
   if(path.startsWith('/api/dm?'))return f.dmLoads.length?f.dmLoads.shift():f.view();
   if(path.startsWith('/api/thread?'))return{id:conv,peer:'peer',messages:structuredClone(rows)};
   if(path.startsWith('/api/agents'))return{agents:[]};
   if(path.startsWith('/api/typing')||path==='/api/refresh')return{};
   if(path==='/api/act'){
    if(body.do==='unread'){for(const m of rows)if(body.ids.includes(m.id)){const rev=Math.max(revisions.get(m.id)||0,m.attention?.rev||0)+1;revisions.set(m.id,rev);m.unread=true;m.attention={rev,writer:fp};}}
    else if(body.do==='read') {if(f.readGate)await f.readGate;for(const m of rows)if(body.ids.includes(m.id)&&(!m.attention||JSON.stringify(body.attention?.[m.id])===JSON.stringify(m.attention))){revisions.set(m.id,Math.max(revisions.get(m.id)||0,m.attention?.rev||0));m.unread=false;delete m.attention;}}
    else throw Error('Unexpected action');return{};
   }throw Error('Unexpected route '+path);
  }};
 const Store=runInNewContext(scheduleSource+'\n'+source+'\nStore',{...env,api,errorText,pendingSends,share,messageTarget:model.messageTarget,currentCatalogAgents:model.currentCatalogAgents,mainPreferences,personRoots,brand:{storage:'agentnet.messenger'}});
 f.store=new Store(host);f.host=host;
 // The original small fixture's rows all fit. Model presentation explicitly;
 // focused cases below restrict it without changing provider read semantics.
 f.visibleIDs=null;
 f.present=()=>{const o=f.store.get().open;if(!o||!f.store.readVisit)return;
  const d=f.store.draft(o.id),visit=f.store.readVisit(o,d.topic||'',!!d.newTopic);
  if(visit)f.store.reportVisible(visit,(f.store.get().views[o.id]?.messages||[]).filter(m=>f.visibleIDs===null||f.visibleIDs.includes(m.id)));
 };
 let scheduled=false;f.store.subscribe(()=>{if(!scheduled){scheduled=true;queueMicrotask(()=>{scheduled=false;f.present();});}});
 f.capture=()=>{const o=f.store.get().open,d=o&&f.store.draft(o.id),visit=o&&f.store.readVisit?.(o,d.topic||'',!!d.newTopic),rows=structuredClone(f.store.get().views[o?.id]?.messages||[]);return()=>visit&&f.store.reportVisible(visit,rows);};
 f.change=async()=>{for(const fn of listeners)fn({type:'change',seq:1});await drain();};
 f.resume=async()=>{for(const fn of visibility)fn(true);await drain();};
 f.reads=()=>calls.filter(c=>c.path==='/api/act'&&c.body.do==='read');
 return f;
}
{
 const f=fixture();await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();
 f.rows[0].unread=true;f.rows[0].attention={rev:1,writer:fp};await f.change();
 assert.equal(f.reads().length,0,'a deliberate remote mark learned during this visit must not trigger a read');
 await f.store.open({kind:'dm',id:conv});f.store.setDraft(conv,{text:''},true);await drain();
 assert.equal(f.reads().length,0,'reselecting the current chat or topic keeps the current visit');
 assert.equal(f.store.get().views[conv].messages[0].unread,true);f.store.stop();
}

// A real topic transition arms that entry; same-topic selection stays a visit.
{
 const f=fixture();f.rows.push({id:'topic-later',kind:'message',dir:'out',topic,body:'later topic turn',unread:false});
 f.rows[2].unread=true;f.rows[2].attention={rev:1,writer:fp};
 await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();assert.equal(f.reads().length,0);
 f.store.setDraft(conv,{text:'',topic},true);await drain();assert.equal(f.reads().length,1,'real topic entry can clear its observed generation');
 await f.store.markUnread(sibling,true);await drain();
 const mark=f.calls.find(c=>c.body?.do==='unread');assert.deepEqual(mark.body.ids,[sibling,'topic-later']);
 f.store.setDraft(conv,{text:'',topic},true);await drain();assert.equal(f.reads().length,1,'same named-topic selection preserves its local mark');
 assert.equal(f.rows[0].unread,false);assert.equal(f.rows[1].unread,false);f.store.stop();
}

// Clearing a sent device-thread generation also acknowledges the legacy read
// state written by that same action, so the plain-ID projection cannot loop.
{
 const f=fixture();f.rows.splice(1);f.rows[0].dir='out';
 await f.store.start();await f.store.open({kind:'thread',id:conv,peer:'peer'});await drain();
 const before=f.reads().length;await f.store.markUnread(mid);await drain();assert.equal(f.reads().length,before);
 f.store.close();await f.store.open({kind:'thread',id:conv,peer:'peer'});await drain();
 assert.equal(f.reads().length,before+1,'clearing a sent deliberate mark must not add a redundant legacy read');
 assert.deepEqual(f.reads().at(-1).body.attention,{[mid]:{rev:1,writer:fp}});f.store.stop();
}

// Local single/from-here actions use one exact current-topic snapshot, including
// sent rows and attachments, while excluding controls/deleted/sibling rows.
{
 const f=fixture();f.rows.push({id:'event',kind:'message',dir:'in',event:'hidden control',topic:'',unread:false},
  {id:'deleted',kind:'message',dir:'in',deleted:true,topic:'',unread:false},
  {id:'excerpt',lid:'granted-original-lid',claimed_key:otherFP,kind:'message',dir:'in',body:'admitted granted turn',excerpt_pid:'guest',topic:'',unread:false});
 await f.store.start();await f.store.open({kind:'dm',id:conv});await f.store.markUnread(mid);await drain();
 assert.equal(f.store.canMarkUnread(f.store.get().views[conv].messages.at(-1)),true,'an admitted stored chat excerpt supports personal attention');
 let marks=f.calls.filter(c=>c.body?.do==='unread');assert.deepEqual(marks[0].body.ids,[mid]);
 assert.equal(f.reads().length,0,'local mark survives immediate view refresh');
 await f.store.markUnread(mid,true);await drain();marks=f.calls.filter(c=>c.body?.do==='unread');
 assert.deepEqual(marks[1].body.ids,[mid,next,'excerpt']);assert(!marks[1].body.ids.includes('granted-original-lid'),'the action uses the held carrier ID, not a forwarded original ID');assert.equal(f.rows[2].unread,false,'sibling topic unchanged');
 assert.equal(f.rows[1].unread,true,'sent row is deliberate personal attention');
 await f.store.refetch();await f.resume();await f.store.recover([0]);await drain();
 assert.equal(f.reads().length,0,'same-visit refresh/resume/reconnect never clear deliberate unread');
 f.store.close();await f.store.open({kind:'dm',id:conv});await drain();
 assert.equal(f.reads().length,1);assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:2,writer:fp},[next]:{rev:1,writer:fp},excerpt:{rev:1,writer:fp}});
 assert.equal(f.rows[0].unread,false);assert.equal(f.rows[1].unread,false);f.store.stop();
}

// Bootstrap and saved/restored views do not create deliberate read eligibility.
{
 const f=fixture();await f.store.start();await f.store.open({kind:'dm',id:conv});await f.store.markUnread(mid);await drain();f.store.stop();
 const restored=fixture(f.saved);restored.rows[0].unread=true;restored.rows[0].attention={rev:1,writer:fp};
 await restored.store.start();await drain();assert.equal(restored.reads().length,0,'remount/bootstrap preserves deliberate unread');
 await restored.resume();await drain();assert.equal(restored.reads().length,0);
 restored.store.close();await restored.store.open({kind:'dm',id:conv});await drain();
 assert.equal(restored.reads().length,1,'later explicit entry arms observed generation');restored.store.stop();
}

// A delayed acknowledgement of one full generation cannot clear or suppress
// an equal-revision replacement written by another device.
{
 const f=fixture();f.rows[0].unread=true;f.rows[0].attention={rev:7,writer:fp};
 let release;f.readGate=new Promise(r=>{release=r;});
 await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();
 assert.equal(f.reads().length,1);assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:7,writer:fp}});
 f.rows[0].attention={rev:7,writer:otherFP};await f.change();release();await drain();
 assert.equal(f.rows[0].unread,true);assert.equal(f.reads().length,1,'replacement is suppressed during the current visit');
 f.readGate=null;f.store.close();await f.store.open({kind:'dm',id:conv});await drain();
 assert.equal(f.reads().length,2,'old success does not suppress a replacement generation');
 assert.deepEqual(f.reads()[1].body.attention,{[mid]:{rev:7,writer:otherFP}});assert.equal(f.rows[0].unread,false);f.store.stop();
}

// Ordinary new unmarked arrivals still acknowledge once with the old request
// shape, without repeatedly fetching the same workspace/conversation.
{
 const f=fixture();await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();
 const before=f.calls.length;f.rows[0].unread=true;await f.change();await drain();
 assert.equal(f.reads().length,1);assert.equal('attention' in f.reads()[0].body,false);
 // The frozen pre-U1 Store makes eight requests in this same fixture.
 assert(f.calls.length-before<=8,'ordinary arrival/read retains the existing eight-request budget');
 await f.change();await drain();assert.equal(f.reads().length,1,'ordinary read does not loop');f.store.stop();
}

// No product-size cap: the UI submits the complete exact snapshot in one action.
{
 const f=fixture();for(let i=0;i<130;i++)f.rows.push({id:'later-'+i,kind:'message',dir:i%2?'out':'in',topic:'',body:'',attachments:[{name:'file'}],unread:false});
 await f.store.start();await f.store.open({kind:'dm',id:conv});await f.store.markUnread(mid,true);await drain();
 const marks=f.calls.filter(c=>c.body?.do==='unread');assert.equal(marks.length,1);assert.equal(marks[0].body.ids.length,132);assert(!marks[0].body.ids.includes(sibling));
 assert.equal(f.reads().length,0);f.store.stop();
}
// Collect independent fresh-entry failures so before evidence retains every
// discriminator without repeatedly running the same fixture.
const freshFailures=[];
async function checkFresh(label,run){try{await run();console.log(label+' PASS');}catch(e){freshFailures.push(label+': '+e.message);console.log('FAIL '+freshFailures.at(-1));}}
await checkFresh('cached-no-mark -> first fresh G clears once; later G2 survives',async()=>{
 const f=fixture();try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});f.store.close();
  f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};
  await f.store.open({kind:'dm',id:conv});await drain();
  assert.equal(f.reads().length,1,'explicit cached reopen reads its first fresh generation');
  assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:4,writer:fp}});
  f.rows[0].unread=true;f.rows[0].attention={rev:5,writer:otherFP};await f.change();await f.resume();
  assert.equal(f.reads().length,1);assert.equal(f.rows[0].unread,true);
 }finally{f.store.stop();}
});
await checkFresh('routine resume cannot seed cached G while fresh entry is pending',async()=>{
 const f=fixture();let load;try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});
  f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};await f.change();f.store.close();
  f.rows[0].attention={rev:5,writer:otherFP};load=f.holdDM();const opening=f.store.open({kind:'dm',id:conv});
  await f.resume();assert.equal(f.reads().length,0,'cached marks are ineligible before fresh entry commit');
  load.resolve(f.view());await opening;await drain();assert.equal(f.reads().length,1);
  assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:5,writer:otherFP}});
 }finally{load?.resolve(f.view());f.store.stop();}
});
await checkFresh('close/reopen does not reuse a superseded entry load for its baseline',async()=>{
 const f=fixture();let old,newer;try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});f.store.close();
  f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};const oldView=f.view();old=f.holdDM();const first=f.store.open({kind:'dm',id:conv});
  f.store.close();f.rows[0].attention={rev:5,writer:otherFP};newer=f.holdDM();const second=f.store.open({kind:'dm',id:conv});
  old.resolve(oldView);await first;await drain();assert.equal(f.reads().length,0,'superseded result cannot seed the new entry');
  newer.resolve(f.view());await second;await drain();assert.equal(f.reads().length,1);
  assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:5,writer:otherFP}});
 }finally{old?.resolve(f.view());newer?.resolve(f.view());f.store.stop();}
});
await checkFresh('pending full-conversation load seeds selected topic without another GET',async()=>{
 const f=fixture();let load,release;try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});f.store.close();
  f.rows[2].unread=true;f.rows[2].attention={rev:4,writer:fp};f.readGate=new Promise(r=>{release=r;});const gets=f.calls.filter(c=>c.path.startsWith('/api/dm?')).length;load=f.holdDM();const opening=f.store.open({kind:'dm',id:conv});
  f.store.setDraft(conv,{text:'',topic},true);load.resolve(f.view());await opening;await drain();
  assert.equal(f.reads().length,1,'first fresh full-conversation result arms the now-selected topic');
  assert.equal(f.calls.filter(c=>c.path.startsWith('/api/dm?')).length,gets+1,'topic transition reuses the pending full-conversation GET');
  assert.deepEqual(f.reads()[0].body.attention,{[sibling]:{rev:4,writer:fp}});
  release();await drain(); // Existing post-ACK stale-projection refresh is independent of entry loading.
  f.rows[2].unread=true;f.rows[2].attention={rev:5,writer:otherFP};await f.change();await f.resume();
  assert.equal(f.reads().length,1,'later selected-topic G2 survives during this visit');assert.equal(f.rows[2].unread,true);
 }finally{release?.();load?.resolve(f.view());f.store.stop();}
});
await checkFresh('new-topic draft selects visible Main anchor and exact from-here rows',async()=>{
 const f=fixture();try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});f.store.setDraft(conv,{text:'draft',topic,newTopic:true});
  await f.store.markUnread(mid,true);await drain();const marks=f.calls.filter(c=>c.body?.do==='unread');
  assert.equal(marks.length,1);assert.deepEqual(marks[0].body.ids,[mid,next]);assert.equal(f.rows[2].unread,false);assert.equal(f.reads().length,0);
 }finally{f.store.stop();}
});
await checkFresh('failed first load and same-scope retry keep the unconsumed fresh entry',async()=>{
 const f=fixture();let failed,retry;try{
  await f.store.start();await f.store.open({kind:'dm',id:conv});f.store.close();
  f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};failed=f.holdDM();const first=f.store.open({kind:'dm',id:conv});
  failed.reject(Error('offline entry fixture'));await first;assert.equal(f.reads().length,0);
  retry=f.holdDM();const second=f.store.open({kind:'dm',id:conv});const same=f.store.open({kind:'dm',id:conv});
  retry.resolve(f.view());await Promise.all([second,same]);await drain();
  assert.equal(f.reads().length,1,'first successful exact entry load survives retry/reselection');
  assert.deepEqual(f.reads()[0].body.attention,{[mid]:{rev:4,writer:fp}});
 }finally{failed?.resolve(f.view());retry?.resolve(f.view());f.store.stop();}
});
await checkFresh('unseen tail and stale viewport/visit callbacks retain exact unread generations',async()=>{
 const f=fixture();try{
  f.visibleIDs=[mid];f.rows[0].unread=true;f.rows[1].unread=true;f.rows[1].dir='in';f.rows[2].unread=true;
  await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();
  assert.deepEqual(f.reads().flatMap(r=>r.body.ids),[mid],'opening reads only the intersecting row');
  assert(f.rows[1].unread,'unseen tail is retained');assert(f.rows[2].unread,'sibling retained');
  const stale=f.capture();f.store.close();f.visibleIDs=[];await f.store.open({kind:'dm',id:conv});await drain();
  stale();await drain();assert(f.rows[1].unread,'old observer cannot acknowledge a reopened visit');
  f.rows[0].unread=true;f.rows[0].attention={rev:8,writer:otherFP};await f.change();f.visibleIDs=[mid];f.present();await drain();
  assert(f.rows[0].unread,'new G2 learned during the visit survives presentation');
  f.store.setDraft(conv,{text:'',topic},true);const oldTopic=f.capture();f.store.setDraft(conv,{text:''},true);oldTopic();await drain();
  assert(f.rows[2].unread,'old topic observer cannot acknowledge a different scope');
 }finally{f.store.stop();}
});
await checkFresh('first fresh outgoing unread is frozen; exact topic entry reuses loaded view and drafts',async()=>{
 const f=fixture();try{
  f.visibleIDs=[];f.rows[0].dir='out';f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};
  const file={key:'kept-file'},draft={text:'unsent draft',replyTo:next,files:[file]};f.store.setDraft(conv,draft);
  await f.store.start();await f.store.open({kind:'dm',id:conv});await drain();
  let o=f.store.get().open,visit=f.store.readVisit(o);assert.equal(visit.firstUnread,mid,'effective outgoing unread can be the entry anchor');
  f.rows[0].unread=false;delete f.rows[0].attention;f.rows[1].unread=true;await f.change();
  assert.equal(f.store.readVisit(f.store.get().open).firstUnread,mid,'live read/arrival changes never move the divider');
  const gets=f.calls.filter(c=>c.path.startsWith('/api/dm?')).length;
  await f.store.openUnread({kind:'dm',id:conv},topic);await drain();
  assert.equal(f.calls.filter(c=>c.path.startsWith('/api/dm?')).length,gets,'loaded topic entry adds no GET');
  assert.equal(f.store.draft(conv).text,draft.text);assert.equal(f.store.draft(conv).replyTo,draft.replyTo);assert.equal(f.store.draft(conv).files[0],file);
  await f.store.open({kind:'dm',id:conv,focus:sibling});assert.equal(f.store.get().open.focus,sibling,'explicit message focus retains precedence');
 }finally{f.store.stop();}
});
await checkFresh('return to displayed topic cancels a different pending open and keeps a fresh entry',async()=>{
 const f=fixture();let held;try{
  f.visibleIDs=[];await f.store.start();await f.store.open({kind:'dm',id:conv});
  f.rows[0].unread=true;f.rows[0].attention={rev:4,writer:fp};await f.change();
  const other='f'.repeat(64);held=f.holdDM();const opening=f.store.open({kind:'dm',id:other});await tick();
  f.visibleIDs=[mid];await f.store.openUnread({kind:'dm',id:conv});await drain();
  assert.equal(f.reads().length,1,'returning from a pending different open arms exactly its fresh generation');
  held.resolve({id:other,messages:[],topics:[],agents:[]});await opening;await drain();
  assert.equal(f.store.get().open.id,conv,'late other load cannot replace the explicit return');
 }finally{held?.resolve({id:'f'.repeat(64),messages:[],topics:[],agents:[]});f.store.stop();}
});
assert.deepEqual(freshFailures,[],'all fresh-entry and visible-topic discriminators pass');
console.log('PASS deliberate unread Store: current visit, remote/local mark, exact single/from-here/siblings/sent/files, explicit entry, bootstrap/remount, full-generation delayed CAS, ordinary-read cost, uncapped atomic snapshot and fresh-entry/scope fencing');
