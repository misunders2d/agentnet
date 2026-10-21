// Actual Store with virtual ten-second I/O barriers. No polling or model calls.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
import {api,errorText} from '../web/src/api.ts';
import model from './comic_model.mjs';
import {pendingSends} from '../static/optimistic.mjs';
import {share} from '../web/src/structural.ts';
import {mainPreferences,personRoots} from '../skins/shared/person-topics.mjs';
const scheduleSource=stripTypeScriptTypes((await readFile(new URL('../web/src/schedule-saves.ts',import.meta.url),'utf8')).replace(/^import .*;$/gm,'')).replace(/^export /gm,'');
const source=stripTypeScriptTypes((await readFile(new URL('../web/src/store.ts',import.meta.url),'utf8')).replace(/^import .*;$/gm,'').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;')).replace(/^export /gm,'');
const tick=()=>new Promise(r=>setImmediate(r));
const drain=async()=>{for(let i=0;i<12;i++)await tick();};
const gate=()=>{let release;const promise=new Promise(r=>release=r);return {promise,release};};
const conv='a'.repeat(64),other='d'.repeat(64),topic='b'.repeat(32),sibling='c'.repeat(32),writer='1'.repeat(64),replacement='2'.repeat(64);
function fixture(kind='dm') {
 let now=0,nextTimer=0,seq=1,readHold,overviewHold,viewHold,reminderHold,reminderError=false,readError=false,notify,decorateOverview=o=>o;
 const timers=new Map(),calls=[],rows=[{id:'seen',topic,kind:'message',dir:'in',unread:true},{id:'sibling',topic:sibling,kind:'message',dir:'in',unread:true}];
 let reminders=[{message:'seen',conv,from:'peer/device',title:'Read this later',due:'2026-10-20T10:00:00Z',overdue:true}];
 const view=(id=conv)=>({id,seq,peer:kind==='dm'?{}:'peer/device',kind:'group',messages:id===conv?structuredClone(rows):[],agents:[],topics:[{id:topic,unread:rows.filter(m=>m.topic===topic&&m.unread).length},{id:sibling,unread:rows.filter(m=>m.topic===sibling&&m.unread).length}],main_topic:{id:'main',unread:0}});
 const summary=()=>({id:conv,peer:'peer/device',unread:rows.filter(m=>m.unread).length,state:'active'});
 const overview=()=>decorateOverview({version:'test',seq,me:{address:'self/device'},remind:true,reminders:structuredClone(reminders),dms:kind==='dm'?[summary(),{id:other,unread:0}]:[],threads:kind==='thread'?[summary()]:[],unread_total:summary().unread,alert_total:summary().unread});
 const env={document:{visibilityState:'visible',hasFocus:()=>true},localStorage:{getItem:()=>null,setItem:()=>{}},setTimeout:(fn,ms)=>{const id=++nextTimer;timers.set(id,{fn,at:now+ms});return id;},clearTimeout:id=>timers.delete(id)};
 const host={platform:'daemon',workspace:{id:Math.random()+''},listen:fn=>{notify=fn;return()=>{};},async api(path,body){
  calls.push({path,body:body&&structuredClone(body)});
  if(path.startsWith('/api/overview')){const result=overview(),hold=overviewHold;if(hold)await hold.promise;return result;}
  if(path.startsWith('/api/dm?')||path.startsWith('/api/thread?')){const id=new URL('http://test'+path).searchParams.get('id'),result=view(id),hold=viewHold;viewHold=null;if(hold)await hold.promise;return result;}
  if(path==='/api/act'){
   if(readHold)await readHold.promise;if(readError)throw Error('read refused');
   for(const row of rows)if(body.ids.includes(row.id)&&(!row.attention||JSON.stringify(row.attention)===JSON.stringify(body.attention?.[row.id]))){row.unread=false;delete row.attention;}seq++;return {note:''};
  }
  if(path.startsWith('/api/remind')){
   if(reminderError)throw Error('reminder refused');
   if(path==='/api/remind')reminders=reminders.map(r=>r.message===body.id?{...r,due:new Date(body.due*1000).toISOString(),overdue:false}:r);
   else reminders=reminders.filter(r=>r.message!==body.id);
   seq++;const result={note:'Reminder saved',seq,message:body.id,reminder:structuredClone(reminders.find(r=>r.message===body.id)||null)};
   if(reminderHold)await reminderHold.promise;return result;
  }
  if(path.startsWith('/api/agents'))return {agents:[]};return {};
 }};
 const Store=runInNewContext(scheduleSource+'\n'+source+'\nStore',{...env,api,errorText,pendingSends,share,messageTarget:model.messageTarget,currentCatalogAgents:model.currentCatalogAgents,mainPreferences,personRoots,brand:{storage:'test'}});
 const store=new Store(host);
 let queued=false;store.subscribe(()=>{if(!queued){queued=true;queueMicrotask(()=>{queued=false;const o=store.get().open;if(!o)return;const draft=store.draft(o.id),visit=store.readVisit(o,o.kind==='dm'?draft.topic||'':'',o.kind==='dm'&&!!draft.newTopic);if(visit)store.reportVisible(visit,(store.get().views[o.id]?.messages||[]).filter(m=>m.id==='seen'));});}});
 return {store,calls,rows,env,view,overview,now:()=>now,
  async start(){await store.start();store.setDraft(conv,{text:'kept draft',topic,replyTo:'reply',files:[{key:'file'}]});},
  async open(){await store.open({kind,id:conv,...(kind==='thread'?{peer:'peer/device'}:{})});},
  holdRead(){return readHold=gate();},holdOverview(){return overviewHold=gate();},holdView(){return viewHold=gate();},holdReminder(){return reminderHold=gate();},
  failRead(){readError=true;},failReminder(){reminderError=true;},
  decorateOverview(fn){decorateOverview=fn;},
  async advance(ms){now+=ms;for(const [id,t]of [...timers])if(t.at<=now){timers.delete(id);t.fn();}await drain();},
  releaseAt(g,ms=10000){env.setTimeout(g.release,ms);},
  bump(){seq++;},setReminder(value){reminders=[value];seq++;},push(){notify({type:'change'});},
  badge(){return (kind==='dm'?store.get().overview.dms:store.get().overview.threads).find(r=>r.id===conv).unread;},
 };
}
const failures=[];
async function check(label,fn){try{await fn();console.log(label+' PASS');}catch(e){failures.push(label+': '+e.message);console.log('FAIL '+failures.at(-1));}}
for(const kind of ['dm','thread'])for(const lane of ['overview','open-view'])await check(kind+' read prompt while '+lane+' held',async()=>{
 const f=fixture(kind),read=f.holdRead();await f.start();await f.open();await drain();
 const blocked=f.holdOverview(),oldView=lane==='open-view'?f.holdView():null;f.releaseAt(blocked);if(oldView)f.releaseAt(oldView);
 const pending=f.store.refetch();await drain();read.release();await drain();
 const prompt=f.badge(),timeline=f.store.get().views[conv].messages.find(m=>m.id==='seen').unread;
 await f.advance(9999);const beforeDeadline=f.badge(),lateBadges=[];f.store.subscribe(()=>lateBadges.push(f.badge()));await f.advance(1);await pending;await drain();
 assert.equal(f.badge(),1,'eventual state keeps unseen sibling');
 assert.equal(f.store.draft(conv).text,'kept draft');assert.equal(f.store.draft(conv).files[0].key,'file');
 f.store.stop();console.log(JSON.stringify({kind,lane,prompt,beforeDeadline,after10000ms:f.badge(),virtualDelayMs:10000}));
 assert.equal(timeline,false,'confirmed read timeline must not wait for an older open-view GET');
 assert.equal(prompt,1,'badge clears confirmed read before blocked overview');assert.equal(beforeDeadline,1);assert(lateBadges.every(n=>n===1),'late overview/view never restores confirmed attention');
});
for(const action of ['move','done','cancel'])await check('reminder '+action+' prompt',async()=>{
 const f=fixture();await f.start();const blocked=f.holdOverview();f.releaseAt(blocked);const pending=f.store.refetch();await drain();
 const run=f.store.changeReminder?.bind(f.store)||f.store.run.bind(f.store);
 await run(a=>action==='move'?a.remind('seen',new Date('2026-10-21T12:00:00Z')):action==='done'?a.remindDone('seen'):a.remindCancel('seen'));
 await drain();const prompt=f.store.get().overview.reminders.filter(r=>r.overdue).length;
 await f.advance(9999);const beforeDeadline=f.store.get().overview.reminders.filter(r=>r.overdue).length,lateBadges=[];f.store.subscribe(()=>lateBadges.push(f.store.get().overview.reminders.filter(r=>r.overdue).length));
 await f.advance(1);await pending;await drain();const after=f.store.get().overview.reminders.filter(r=>r.overdue).length;f.store.stop();
 console.log(JSON.stringify({action,prompt,beforeDeadline,after10000ms:after,virtualDelayMs:10000}));
 assert.equal(after,0);assert.equal(prompt,0,'confirmed reminder action clears due attention before slow overview');assert.equal(beforeDeadline,0);assert(lateBadges.every(n=>n===0),'late overview never restores moved/ended reminder attention');
});
for(const kind of ['dm','thread'])await check(kind+' newer read generation and failure guards',async()=>{
 const f=fixture(kind),read=f.holdRead();f.rows[0].attention={rev:7,writer};await f.start();await f.open();await drain();
 f.rows[0].attention={rev:7,writer:replacement};f.bump();await f.store.refetch();read.release();await drain();
 assert.equal(f.rows[0].unread,true);assert.equal(f.badge(),2,'refused stale CAS leaves replacement attention');
 assert.equal(f.calls.filter(c=>c.path==='/api/act').length,1);f.store.stop();
 const failed=fixture(kind);failed.failRead();await failed.start();await failed.open();await drain();assert.equal(failed.badge(),2);failed.store.stop();
});
for(const kind of ['dm','thread'])await check(kind+' old read-view response preserves newer overview and arrivals',async()=>{
 const f=fixture(kind),read=f.holdRead();await f.start();await f.open();await drain();
 const view=f.holdView();read.release();await drain();
 f.rows[0].unread=true;f.rows[0].attention={rev:7,writer:replacement};f.rows.push({id:'arrival',topic,kind:'message',dir:'in',unread:true});f.bump();
 await f.store.reload(false,false);assert.equal(f.badge(),3);
 view.release();await drain();assert.equal(f.badge(),3,'late older confirmed read view cannot decrement a newer overview');
 await f.store.refetch();await drain();assert.equal(f.badge(),3);assert.equal(f.store.get().views[conv].messages.find(m=>m.id==='seen').attention.writer,replacement);f.store.stop();
});
for(const kind of ['dm','thread'])await check(kind+' newer visible unread survives older blocked overview',async()=>{
 const f=fixture(kind),read=f.holdRead();await f.start();await f.open();await drain();const overview=f.holdOverview(),pending=f.store.refetch();await drain();
 read.release();await drain();assert.equal(f.badge(),1);
 f.rows[0].unread=true;f.rows[0].attention={rev:7,writer:replacement};f.bump();await f.store.refreshOpen();await drain();
 const current=f.badge();overview.release();await pending;await drain();const after=f.badge();f.store.stop();
 assert.equal(current,2,'authoritative newer unread updates the exact badge while overview is blocked');assert.equal(after,2,'old overview cannot overwrite newer view generation');
});
await check('newer reminder response, failure and stopped workspace guards',async()=>{
 const f=fixture();await f.start();const held=f.holdReminder();const run=f.store.changeReminder?.bind(f.store)||f.store.run.bind(f.store);
 const pending=run(a=>a.remind('seen',new Date('2026-10-21T12:00:00Z')));
 await drain();f.setReminder({...f.overview().reminders[0],due:'2026-10-20T12:00:00Z',overdue:true});await f.store.refetch();held.release();await pending;await drain();
 assert.equal(f.store.get().overview.reminders[0].overdue,true,'older move response cannot clear a newer reminder');f.store.stop();
 const failed=fixture();await failed.start();failed.failReminder();await (failed.store.changeReminder?.bind(failed.store)||failed.store.run.bind(failed.store))(a=>a.remindDone('seen'));assert.equal(failed.store.get().overview.reminders[0].overdue,true);failed.store.stop();
 const stopped=fixture();await stopped.start();const late=stopped.holdReminder(),work=(stopped.store.changeReminder?.bind(stopped.store)||stopped.store.run.bind(stopped.store))(a=>a.remindDone('seen'));await drain();stopped.store.stop();const state=stopped.store.get(),calls=stopped.calls.length;late.release();await work;await drain();assert.equal(stopped.store.get(),state);assert.equal(stopped.calls.length,calls,'late completion never refreshes a stopped workspace');
});
for(const mode of ['muted-dm','active-device-topics','archived-latest'])await check(mode+' totals retain unrelated attention',async()=>{
 const kind=mode==='muted-dm'?'dm':'thread',f=fixture(kind),read=f.holdRead();
 f.decorateOverview(o=>{
  if(kind==='dm')return {...o,dms:o.dms.map(d=>d.id===other?{...d,unread:4}:d),unread_total:o.unread_total+4,alert_total:4,notify:{mutes:[conv]}};
  const own=o.threads[0];
  if(mode==='archived-latest')return {...o,threads:[],topic_list:true,topics:[{peer:own.peer,total:2,archived:2,archived_unread:own.unread+5,latest:{...own,state:'archived'}}],unread_total:own.unread+5,alert_total:own.unread+5};
  return {...o,threads:[...o.threads,{...own,id:'other-device-topic',unread:5}],topics:[{peer:own.peer,total:2,archived:0,archived_unread:0,latest:own}],unread_total:own.unread+5,alert_total:own.unread+5};
 });
 await f.start();await f.open();await drain();const overview=f.holdOverview(),pending=f.store.refetch();await drain();read.release();await drain();
 const o=f.store.get().overview;
 assert.equal(o.unread_total,kind==='dm'?5:6);assert.equal(o.alert_total,kind==='dm'?4:6);
 if(kind==='dm'){assert.equal(o.dms.find(d=>d.id===other).unread,4);assert.equal(f.store.get().dm.topics.find(t=>t.id===topic).unread,0);assert.equal(f.store.get().dm.topics.find(t=>t.id===sibling).unread,1);}
 else{assert.equal(o.topics[0].latest.unread,1);assert.equal(mode==='archived-latest'?o.topics[0].archived_unread:o.threads[1].unread,mode==='archived-latest'?6:5);}
 overview.release();await pending;await drain();assert.equal(f.store.get().overview.unread_total,o.unread_total);f.store.stop();
});
for(const kind of ['dm','thread'])await check(kind+' pending same-ID focus promotes already-kept newer view',async()=>{
 const f=fixture(kind),read=f.holdRead();await f.start();const old=f.holdView();
 const opening=f.store.open({kind,id:conv,focus:'seen',...(kind==='thread'?{peer:'peer/device'}:{})});await drain();
 assert.equal(f.store.get().pending.id,conv);
 // Another completed same-scope load may keep a newer view before this
 // request returns. Its cache update does not own the pending navigation.
 f.rows[0].attention={rev:7,writer:replacement};f.bump();f.store.keep(conv,f.view());old.release();await opening;await drain();
 const state=f.store.get();f.store.stop();read.release();await drain();
 assert.equal(state.pending,null,'older GET must still finish the selected pending navigation');
 assert.equal(state.open?.id,conv);assert.equal(state.open?.focus,'seen');assert.equal(state.views[conv].messages[0].attention.writer,replacement);
 assert.equal(state.readVisit!==null,true,'fresh explicit entry arms its newer kept generation');
});
for(const kind of ['dm','thread'])await check(kind+' virtual viewport preserves full unseen-tail totals',async()=>{
 const f=fixture(kind),read=f.holdRead();for(let i=0;i<130;i++)f.rows.push({id:'unseen-tail-'+i,topic,kind:'message',dir:'in',unread:true});
 await f.start();await f.open();await drain();const overview=f.holdOverview(),pending=f.store.refetch();await drain();read.release();await drain();
 const state=f.store.get();assert.equal(f.badge(),131,'one visible read leaves every unseen tail/sibling unread');assert.equal(state.overview.unread_total,131);assert.equal(state.overview.alert_total,131);
 assert.deepEqual(f.calls.filter(c=>c.path==='/api/act').flatMap(c=>c.body.ids),['seen'],'only intersection-observed row is acknowledged');
 overview.release();await pending;await drain();f.store.stop();
});
for(const cached of [false,true])await check((cached?'cached':'fresh')+' exact Review open selects Chats and preserves draft/files',async()=>{
 const f=fixture(),read=f.holdRead();await f.start();if(cached)await f.open();f.store.showTab('oks');
 await f.store.open({kind:'dm',id:conv,focus:'seen'});await drain();const state=f.store.get(),draft=f.store.draft(conv);f.store.stop();read.release();await drain();
 assert.equal(state.tab,'chats','exact Review result displays the opened conversation');assert.equal(state.open.id,conv);assert.equal(state.open.focus,'seen');
 assert.equal(draft.text,'kept draft');assert.equal(draft.replyTo,'reply');assert.equal(draft.files[0].key,'file');
});
assert.deepEqual(failures,[],'all prompt attention and generation guards');
