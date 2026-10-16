// Exercise the real Store: exact topic reads must converge without relying on
// a push arriving after the read HTTP response, including older in-flight GETs.
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
const Store=runInNewContext(scheduleSource+'\n'+source+'\nStore',{api,errorText,pendingSends,share,messageTarget:model.messageTarget,mainPreferences,personRoots,brand:{storage:'test'},document:{visibilityState:'visible',hasFocus:()=>true},localStorage:{getItem:()=>null,setItem:()=>{}},setTimeout,clearTimeout});
const tick=()=>new Promise(r=>setImmediate(r)),gate=()=>{let release;const promise=new Promise(r=>release=r);return{promise,release};};
// These small fixtures present every row of the selected topic. The actual
// timeline supplies this viewport input through its one observer.
const present=s=>{const o=s.get().open;if(!o)return;const d=s.draft(o.id),v=s.readVisit(o,d.topic||'',!!d.newTopic);if(v)s.reportVisible(v,s.get().views[o.id]?.messages||[]);};
const mount=s=>{let queued=false;s.subscribe(()=>{if(!queued){queued=true;queueMicrotask(()=>{queued=false;present(s);});}});};
const conv='a'.repeat(64),topic='b'.repeat(32),sibling='c'.repeat(32);
for(const mode of ['quiet','stale','push-before-ack','stream']){
 const stale=mode==='stale',pushed=mode==='push-before-ack'||mode==='stream';let push;
 const messages=[...['member','guest','agent','imported'].map(id=>({id,topic,unread:true,dir:'in',kind:'message'})),
  {id:'sibling-one',topic:sibling,unread:true,dir:'in',kind:'message'}, {id:'sibling-two',topic:sibling,unread:true,dir:'in',kind:'message'},
  {id:'main',unread:true,dir:'in',kind:'message'}];
 const readGate=gate(),oldOverview=gate(),oldView=gate(),reads=[];let hold=false,overviewCalls=0,dmCalls=0;
 const count=id=>messages.filter(m=>m.unread&&m.topic===id).length;
 const view=()=>({id:conv,peer:{},kind:'group',messages:structuredClone(messages),agents:[],topics:[{id:topic,unread:count(topic)},{id:sibling,unread:count(sibling)}],main_topic:{id:'main',unread:count(undefined)}});
 const overview=()=>({version:'test',dms:[{id:conv,count:7,unread:messages.filter(m=>m.unread).length}],threads:[]});
 const host={platform:'daemon',workspace:{id:'test-'+mode},listen:fn=>{push=fn;return()=>{};},async api(path,body){
  if(path.startsWith('/api/overview')){overviewCalls++;const result=overview();if(hold){hold=false;await oldOverview.promise;}return result;}
  if(path.startsWith('/api/dm?')){dmCalls++;const result=view();if(stale&&dmCalls===2)await oldView.promise;return result;}
  if(path==='/api/act'){reads.push(Array.from(body.ids));await readGate.promise;for(const m of messages)if(body.ids.includes(m.id))m.unread=false;if(pushed){push({type:'change'});await tick();await tick();}return{};}
  if(path.startsWith('/api/agents'))return{agents:[]};return{};
 }};
 const s=new Store(host);mount(s);await s.start();s.setDraft(conv,{text:'',topic});await s.open({kind:'dm',id:conv});
 assert.deepEqual(reads,[['member','guest','agent','imported']],'only exact viewed rows acknowledged');
 let older;
 if(stale){hold=true;older=s.refetch();await tick();}
 readGate.release();await tick();await tick();
 if(stale){oldOverview.release();oldView.release();await older;}
 for(let i=0;i<20;i++)await tick();
 assert.equal(s.get().overview.dms[0].unread,3,'chat badge retains Main and unseen siblings only');
 assert.equal(s.get().dm.topics.find(t=>t.id===topic).unread,0,'viewed topic badge converges');
 assert.equal(s.get().dm.topics.find(t=>t.id===sibling).unread,2,'unseen sibling badge retained');
 assert.equal(s.get().dm.main_topic.unread,1,'unseen Main retained');
 assert(s.get().dm.messages.filter(m=>m.topic===topic).every(m=>!m.unread),'viewed timeline converges');
 assert.equal(reads.length,1,'no acknowledgement loop');
 if(pushed){assert.equal(overviewCalls,2,'push-before-ack reuses its committed overview');assert.equal(dmCalls,2,'push-before-ack reuses its committed view');}
 if(mode==='stream'){
  for(let i=0;i<20;i++){messages.push({id:'arrival-'+i,topic,unread:true,dir:'in',kind:'message'});push({type:'change'});for(let j=0;j<20;j++)await tick();}
  assert.equal(reads.length,21,'one read per separated arrival');assert.equal(overviewCalls,42,'arrival and committed-read push each load once; no third overview per acknowledgement');assert.equal(dmCalls,42,'no extra view read after committed push');
 }
 const scope=s.readAcknowledged.ids;s.setDraft(conv,{text:'',topic:sibling});await tick();
 assert.notEqual(s.readAcknowledged.ids,scope,'dedup retention is scoped to the visible topic');
 assert.deepEqual(reads.at(-1),['sibling-one','sibling-two']);
 s.close();assert.equal(s.readAcknowledged.ids.size,0,'closing releases acknowledgement IDs');s.stop();
 console.log(JSON.stringify({ok:true,mode,staleAsyncReplies:stale,overviewCalls,dmCalls,readBatches:reads.length,unseenMain:1,unseenSibling:2}));
}
for(const stop of [false,true]){
 const other='d'.repeat(64),readGate=gate(),reads=[];let overviewCalls=0,dmCalls=0,committed=false;
 const host={platform:'daemon',workspace:{id:'navigate-'+stop},listen:()=>()=>{},async api(path,body){
  if(path.startsWith('/api/overview')){overviewCalls++;return{version:'test',dms:[{id:conv,unread:committed?1:2},{id:other,unread:0}],threads:[]};}
  if(path.startsWith('/api/dm?')){dmCalls++;const id=new URL('http://fixture'+path).searchParams.get('id');return{id,peer:{},agents:[],messages:id===other?[]:[{id:'viewed',kind:'message',topic,dir:'in',unread:!committed},{id:'unseen',kind:'message',topic:sibling,dir:'in',unread:true}]};}
  if(path==='/api/act'){reads.push(Array.from(body.ids));await readGate.promise;committed=true;return{};}
  if(path.startsWith('/api/agents'))return{agents:[]};return{};
 }};
 const s=new Store(host);mount(s);await s.start();s.setDraft(conv,{text:'',topic});await s.open({kind:'dm',id:conv});
 if(stop)s.stop();else await s.open({kind:'dm',id:other});
 readGate.release();for(let i=0;i<20;i++)await tick();
 assert.deepEqual(reads,[['viewed']],'navigation never reads the unviewed sibling');
 if(stop){assert.equal(overviewCalls,1,'stopped provider does not refresh');assert.equal(dmCalls,1);}
 else{assert.equal(s.get().open.id,other);assert.equal(s.get().dm.id,other);assert.equal(s.get().overview.dms.find(d=>d.id===conv).unread,1);assert.equal(overviewCalls,2,'one committed overview after navigation');assert.equal(dmCalls,2,'no reload of the departed conversation');s.stop();}
 console.log(JSON.stringify({ok:true,mode:stop?'stopped':'navigation',overviewCalls,dmCalls,readBatches:reads.length}));
}
