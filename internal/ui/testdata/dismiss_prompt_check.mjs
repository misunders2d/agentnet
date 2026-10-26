// Actual Comic Store; controlled I/O tests behavior, not performance qualification.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
import {api,errorText} from '../web/src/api.ts';
import model from './comic_model.mjs';
import {pendingSends} from '../static/optimistic.mjs';
import {share} from '../web/src/structural.ts';
import {mainPreferences,personRoots} from '../skins/shared/person-topics.mjs';

const schedule=stripTypeScriptTypes((await readFile(new URL('../web/src/schedule-saves.ts',import.meta.url),'utf8')).replace(/^import .*;$/gm,'')).replace(/^export /gm,'');
const source=stripTypeScriptTypes((await readFile(new URL('../web/src/store.ts',import.meta.url),'utf8')).replace(/^import .*;$/gm,'').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;')).replace(/^export /gm,'');
const barrier=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
const drain=async()=>{for(let i=0;i<20;i++)await new Promise(r=>setImmediate(r));};
const conv='a'.repeat(64),id='b'.repeat(32),other='c'.repeat(32);

function fixture(kind='dm') {
 let seq=1,notify,actHold=barrier(),overviewHold,viewHold,actCount=0,readCount=0,loseResponse=false;
 const timers=new Map();let timer=0;
 let message={id,dir:'in',from:'peer/device',kind:'task',body:'Exact task',state:'needs_human',detail:'Needs attention',job_detail:'Needs attention',unread:true,attention:{rev:1,writer:'d'.repeat(64)},actions:['resolve'],target:{address:'self/device',agent_id:'e'.repeat(32)},exec:{host:'self/device',attempt:1,state:'needs_human'}};
 if(kind==='thread')message={...message,author:{label:'Owner'}};
 let item={id,conv,peer:'peer/device',kind:'task',reason:'agent_needs_human',why:'Needs attention',excerpt:'Exact task',at:'2026-10-26T10:00:00Z',actions:['resolve']};
 const unrelated={...item,id:other,conv:'f'.repeat(64),excerpt:'Unrelated task'};
 const view=()=>({id:conv,seq,peer:kind==='thread'?'peer/device':{},kind:'group',messages:[structuredClone(message)],agents:[],topics:[],main_topic:{id:'main'}});
 const overview=()=>({seq,version:'test',me:{address:'self/device'},person:{person:'1'.repeat(32)},needs_you:kind==='dm'&&message.state==='needs_human'?[structuredClone(item),unrelated]:[unrelated],held:[],review:kind==='thread'&&message.state==='needs_human'?[{id,peer:'peer/device',kind:'task',why:'Needs attention',excerpt:'Exact task',at:item.at}]:[],dms:[{id:conv,unread:1,decide:message.state==='needs_human'?1:0,held:0}],threads:kind==='thread'?[{id:conv,peer:'peer/device',review:message.state==='needs_human'?1:0}]:[],reminders:[{message:id,overdue:true}],unread_total:1,alert_total:1});
 const host={platform:'daemon',workspace:{id:'owned'},listen:fn=>{notify=fn;return()=>{};},async api(path,body){
  if(path.startsWith('/api/overview')){readCount++;const result=overview(),hold=overviewHold;overviewHold=null;if(hold)await hold.promise;return result;}
  if(path.startsWith('/api/dm?')||path.startsWith('/api/thread?')){readCount++;const result=view(),hold=viewHold;viewHold=null;if(hold)await hold.promise;return result;}
  if(path==='/api/act'){assert.equal(body.do,'resolve');assert.equal(body.id,id);actCount++;await actHold.promise;message={...message,state:'resolved',actions:[]};seq++;if(loseResponse)throw Error('Response lost after commit');return {note:'Closed. No reply was sent.'};}
  if(path==='/api/typing')return {entries:[]};if(path.startsWith('/api/agents'))return {agents:[]};return {};
 }};
 const Store=runInNewContext(schedule+'\n'+source+'\nStore',{api,errorText,pendingSends,share,messageTarget:model.messageTarget,currentCatalogAgents:model.currentCatalogAgents,directChatKey:model.directChatKey,mainPreferences,personRoots,brand:{storage:'test'},document:{visibilityState:'visible',hasFocus:()=>false},localStorage:{getItem:()=>null,setItem:()=>{}},setTimeout:(fn,ms)=>{const id=++timer;timers.set(id,{fn,ms});return id;},clearTimeout:id=>timers.delete(id)});
 const store=new Store(host);
 return {store,start:async()=>{await store.start();await store.open({kind,id:conv});await drain();},act:()=>store.run(a=>a.act({do:'resolve',id})),release:()=>actHold.resolve(),fail:()=>actHold.reject(Error('Owned dismissal failure')),count:()=>kind==='dm'?store.get().overview.needs_you.filter(x=>x.id===id).length:store.get().overview.review.filter(x=>x.id===id).length,calls:()=>actCount,reads:()=>readCount,
  holdOverview:()=>overviewHold=barrier(),holdView:()=>viewHold=barrier(),
  newer:(identical=false)=>{seq++;message={...message,exec:{...message.exec,attempt:2},attention:{...message.attention,rev:2},detail:'New attempt',job_detail:'New attempt'};if(!identical)item={...item,why:'New attempt'};},push:()=>notify({type:'change'}),lose:()=>loseResponse=true,
 };
}

for(const kind of ['dm','thread']){
 const f=fixture(kind);await f.start();
 const original=f.store.get().views[conv],overview=f.store.get().overview;
 f.store.setDraft(conv,{text:'Keep my draft',files:[{key:'owned-file',file:{name:'exact.txt'}}]});
 const work=f.act();
 assert.equal(f.count(),0,kind+': decision attention leaves immediately while backend is blocked');
 assert.equal(f.store.get().overview.unread_total,1,'dismissal never marks messages read');
 assert.equal(f.store.get().overview.alert_total,1);assert.equal(f.store.get().overview.reminders.length,1);
 assert.equal(original.messages[0].state,'needs_human','pending presentation never mutates authoritative history');
 assert.equal(overview.needs_you.length,kind==='dm'?2:1,'original overview remains immutable');
 const duplicate=f.act();await drain();assert.equal(f.calls(),1,'duplicate intent sends one resolve');
 const oldOverview=f.holdOverview(),oldView=f.holdView(),oldRead=f.store.refetch();await drain();
 f.release();await work;await duplicate;await drain();
 assert.equal(f.count(),0,'success remains clear while an old overview is blocked');
 oldView.resolve();oldOverview.resolve();await oldRead;await drain();
 assert.equal(f.count(),0,'pre-commit snapshots cannot resurrect cleared attention');
 assert.equal(f.store.get().views[conv].messages[0].state,'resolved');
 assert.equal(f.store.draft(conv).text,'Keep my draft');assert.equal(f.store.draft(conv).files[0].key,'owned-file');
 f.store.stop();console.log(kind+' immediate dismissal, duplicate input, stale reads and preserved history/draft PASS');
}

for(const newer of [false,'changed','identical']){
 const f=fixture();await f.start();const work=f.act();assert.equal(f.count(),0);
 if(newer){f.newer(newer==='identical');f.push();await drain();assert.equal(f.count(),1,'newer authoritative attention wins even if overview row is byte-identical');}
 f.fail();assert.equal(await work,undefined);await drain();
 assert.equal(f.count(),1,'failure restores the current request');
 assert.equal(f.store.get().overview.needs_you.find(x=>x.id===id).why,newer==='changed'?'New attempt':'Needs attention','rollback never restores captured stale data');
 if(newer)assert.equal(f.store.get().views[conv].messages[0].exec.attempt,2,'newer attempt survives rollback');
 assert(f.store.get().toasts.some(x=>x.tone==='error'&&x.text.includes('Owned dismissal failure')),'failure names the failed operation');
 f.store.stop();console.log('dismiss failure with newer state='+newer+' PASS');
}
{
 const f=fixture();await f.start();f.lose();const work=f.act();f.release();assert.equal(await work,undefined);await drain();
 assert.equal(f.calls(),1,'lost response never repeats the mutation');
 assert.equal(f.count(),0,'fresh truth preserves the committed dismissal after response loss');
 assert.equal(f.store.get().views[conv].messages[0].state,'resolved');
 assert(f.store.get().toasts.some(t=>t.tone==='error'&&t.text.includes('Couldn’t confirm')),'lost acknowledgement is reported truthfully');
 f.store.stop();console.log('dismiss commit followed by lost response PASS');
}

{
 const f=fixture();await f.start();const work=f.act();f.store.stop();const stopped=f.store.get();f.fail();await work;assert.equal(f.store.get(),stopped,'late failure cannot reactivate a departed workspace');
 console.log('dismiss departed workspace PASS');
}
{
 const f=fixture();await f.start();const work=f.act();await drain();f.store.stop();const stopped=f.store.get(),reads=f.reads();f.release();await work;await drain();
 assert.equal(f.store.get(),stopped,'late success cannot reactivate a departed workspace');
 assert.equal(f.reads(),reads,'late success never starts workspace reads');
 console.log('dismiss late success after workspace departure PASS');
}
