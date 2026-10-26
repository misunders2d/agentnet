// Actual Store action lanes and RoomPanel hook, with deterministic I/O barriers.
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const root=pathToFileURL(process.env.AGENTNET_SOURCE+'/internal/ui/');
registerHooks({load(url,context,next){
 if(url.includes('/web/src/')&&url.endsWith('.ts'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(url.endsWith('/store.ts')&&process.env.AGENTNET_ACTION_STORE?process.env.AGENTNET_ACTION_STORE:new URL(url),'utf8').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;'))};
 return next(url,context);
},resolve(specifier,context,next){
 const parent=context.parentURL||'';
 if(parent.includes('/web/src/')){
  if(specifier==='react')return next('data:text/javascript,export const useSyncExternalStore=()=>{};',context);
  if(specifier==='./skin/brand')return next('data:text/javascript,export const skin={storage:"agentnet.messenger"};',context);
  if(specifier==='./optimistic.mjs')return next(new URL('static/optimistic.mjs',root).href,context);
  if(specifier==='./person-topics.mjs')return next(new URL('skins/shared/person-topics.mjs',root).href,context);
  if(specifier==='./linkText')return next(new URL('skins/shared/link-text.mjs',root).href,context);
  if(specifier.startsWith('.')&&!/\.(ts|tsx|mjs|js)$/.test(specifier))return next(new URL(specifier+'.ts',parent).href,context);
 }
 return next(specifier,context);
}});
const {Store,unsentIn}=await import(new URL('web/src/store.ts',root));
const saved=new Map();globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>saved.set(k,v)};
globalThis.document={visibilityState:'visible',hasFocus:()=>true,addEventListener(){},removeEventListener(){}};

const gate=()=>{let release;const promise=new Promise(r=>release=r);return {promise,release};};
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const conv='a'.repeat(64),other='c'.repeat(64),topic='b'.repeat(32);
const me={address:'fixture/laptop',fingerprint:'11111111-11111111-11111111-11111111',responder:'',responder_dir:'',agent:false};
const view=id=>({id,kind:'group',peer:{},messages:[],agents:[],guests:[],topics:[{id:topic,state:'active'}]});
async function world(){
 let viewA=view(conv),holdOverview,holdDM,holdAction,actionEntered,dmEntered,dmError=false,calls=0;
 const host={platform:'daemon',workspace:{id:Math.random()+''},listen:()=>()=>{},api:async(path,body)=>{
  if(path.startsWith('/api/overview')){if(holdOverview)await holdOverview.promise;return {version:'fixture',me,groups:true,dms:[],threads:[],people:[]};}
  if(path.startsWith('/api/topic/')){calls++;actionEntered?.release();if(holdAction)await holdAction.promise;viewA={...viewA,topics:[{id:topic,state:'archived'}]};return {note:'Archived'};}
  if(path.startsWith('/api/dm?')){const id=new URL('http://fixture'+path).searchParams.get('id');if(id===conv&&holdDM){dmEntered?.release();await holdDM.promise;}if(id===conv&&dmError)throw Error('selected view unavailable');return structuredClone(id===conv?viewA:view(id));}
  if(path.startsWith('/api/agents'))return {agents:[]};if(path.startsWith('/api/typing'))return {entries:[]};return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'dm',id:conv});
 return {store,calls:()=>calls,failDM(value){dmError=value;},holdOverview(){return holdOverview=gate();},holdDM(){dmEntered=gate();holdDM=gate();return {wait:dmEntered.promise,release:holdDM.release};},holdAction(){actionEntered=gate();holdAction=gate();return {wait:actionEntered.promise,release:holdAction.release};}};
}
{
 const w=await world(),overview=w.holdOverview(),dm=w.holdDM(),c={conv,peer:'',id:topic},key=JSON.stringify([conv,'',topic]);
 const work=w.store.changeTopic('archive',c);assert.equal(w.store.changeTopic('archive',c),work,'duplicate action shares exact in-flight promise');
 await dm.wait;assert.equal(w.calls(),1);assert.equal(w.store.get().topicBusy[key],'archive','guard stays while selected state is stale');
 dm.release();let complete=false;work.then(()=>complete=true);await pause(200);
 const beforeOverviewRelease=complete;overview.release();await work;
 assert.equal(beforeOverviewRelease,true,'unrelated blocked overview must not hold selected-view completion');
 assert.equal(w.store.get().dm.topics[0].state,'archived');assert.equal(w.store.get().topicBusy[key],undefined);w.store.stop();
 console.log('authoritative selected view completes while overview held; duplicate archive excluded PASS');
}
for(const target of ['other-chat','stopped-provider']){
 const w=await world(),action=w.holdAction(),work=w.store.changeTopic('archive',{conv,peer:'',id:topic});
 await action.wait;
 if(target==='other-chat')await w.store.open({kind:'dm',id:other});else w.store.stop();
 action.release();const result=await work;
 assert.equal(result,undefined,target+' never presents old-context completion');
 if(target==='other-chat'){assert.equal(w.store.get().dm.id,other);assert.equal(w.store.get().dm.topics[0].state,'active');w.store.stop();}
 console.log(target+' action binding PASS');
}
for(const target of ['current-thread','same-peer-other-thread']){
 const peer='fixture/desk',a='1'.repeat(32),b='2'.repeat(32),id=target==='current-thread'?a:b,overview=gate();
 const summary=(which,state='active')=>({id:which,peer,title:which===a?'A':'B',last:'',last_at:'2026-10-15T12:00:00Z',count:2,state,unread:0,review:0,running:0,pending:false});
 let hold=false,changed=false,calls=0;const threadCalls=[];
 const host={platform:'daemon',workspace:{id:target},listen:()=>()=>{},api:async(path,body)=>{
  if(path.startsWith('/api/overview')){if(hold)await overview.promise;return {version:'fixture',me,topic_list:true,dms:[],threads:[summary(a,changed&&id===a?'done':'active'),summary(b,changed&&id===b?'done':'active')],people:[]};}
  if(path.startsWith('/api/thread?')){const which=new URL('http://fixture'+path).searchParams.get('id');threadCalls.push(which);return {id:which,peer,messages:[],topic:summary(which,changed&&which===id?'done':'active')};}
  if(path.startsWith('/api/topic/')){assert.equal(body.id,id);calls++;changed=true;return {note:'Marked done'};}
  if(path.startsWith('/api/agents'))return {agents:[]};if(path.startsWith('/api/typing'))return {entries:[]};return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'thread',id:a,peer});hold=true;
 const change={peer,id,count:2},key=JSON.stringify(['',peer,id]),work=store.changeTopic('done',change);
 assert.equal(store.changeTopic('done',change),work,'in-flight duplicate uses same promise');
 let complete=false;work.then(()=>complete=true);await pause(25);
 const duplicate=target==='same-peer-other-thread'?store.changeTopic('done',change):work;
 const before={complete,duplicate:duplicate===work,busy:store.get().topicBusy[key],calls,topic:store.get().overview.threads.find(t=>t.id===id).state};
 // Always release pending work before asserting the fail-before observations.
 overview.release();const result=await work;await duplicate;await store.refetch();
 assert.equal(result.note,'Marked done');assert.equal(store.get().topicBusy[key],undefined);assert.equal(before.calls,1);
 assert.equal(store.get().overview.threads.find(t=>t.id===id).state,'done');
 if(target==='same-peer-other-thread'){
  assert.equal(before.complete,false,'A cannot complete B action before B overview state arrives');
  assert.equal(before.duplicate,true,'duplicate B action stays in the same flight until B state arrives');
  assert.equal(before.busy,'done','B duplicate guard stays through B summary refresh');assert.equal(before.topic,'done','local topic intent appears before the blocked overview');
  assert.deepEqual(threadCalls,[a,a],'only explicit final refetch reloads unrelated A');
 }else{
  assert.equal(before.complete,true,'current thread completes on its own authoritative refresh while overview is held');
  assert.equal(before.busy,undefined);assert.equal(store.get().thread.topic.state,'done');
 }
 store.stop();console.log(target+' exact topic refresh boundary PASS');
}
{
 const w=await world(),c={conv,peer:'',id:topic},key=JSON.stringify([conv,'',topic]),errors=[];
 w.store.toast=(text)=>errors.push(text);w.failDM(true);
 assert.equal(await w.store.changeTopic('archive',c),undefined);
 assert.equal(w.store.get().topicBusy[key],undefined,'failed selected refresh releases topic guard');
 assert.equal(w.store.get().dm.topics[0].state,'active','failed GET keeps prior view without claiming fresh state');
 assert.deepEqual(errors,['selected view unavailable']);
 w.failDM(false);assert.equal(await w.store.refreshOpen(),true);assert.equal(w.store.get().dm.topics[0].state,'archived','successful server action survives failed UI refresh');
 assert.equal(w.calls(),1,'refresh failure does not replay saved server action');
 assert.ok(await w.store.changeTopic('archive',c));assert.equal(w.calls(),2,'next explicit action can run after refresh failure');w.store.stop();
 console.log('real selected-view API failure suppresses success, preserves server action and releases guard PASS');
}
{
 // Execute the shipped hook body directly; JSX rendering is irrelevant to its
 // async guard. Stub React state/ref storage while keeping the exact source.
 const source=readFileSync(new URL('web/src/features/RoomPanel.tsx',root),'utf8');
 const hook=stripTypeScriptTypes(source.slice(source.indexOf('function useRoomAction('),source.indexOf('/** Decisions stay')));
 const selected={kind:'dm',id:conv},states=[],g={key:'agent',kind:'agent',pid:'p',name:'Pi'};
 let calls=0,snacks=0;
 const store={get:()=>({open:selected,pending:null}),isActive:()=>true,api:{dismissAgent:async()=>{calls++;return {}; }},run:fn=>fn(),refreshOpen:async()=>{throw Error('room refresh failed');}};
 const useRoomAction=Function('useApp','useState','useRef','callName','cap',hook+';return useRoomAction;')(()=>store,()=>['',s=>states.push(s)],v=>({current:v}),n=>n,n=>n);
 const action=useRoomAction(conv,()=>snacks++);
 await assert.rejects(action.act(g,'dismiss'),/room refresh failed/);
 assert.equal(states.at(-1),'','room refresh failure clears visible busy state');assert.equal(snacks,0);
 store.refreshOpen=async()=>false;await action.act(g,'dismiss');assert.equal(states.at(-1),'');assert.equal(snacks,0,'failed API refresh result never offers Undo');
 const dm=gate();store.refreshOpen=()=>dm.promise;
 const retry=action.act(g,'dismiss');await pause(0);await action.act(g,'dismiss');assert.equal(calls,3,'pending guard survives selected refresh and rejects duplicate');
 dm.release(true);await retry;assert.equal(states.at(-1),'');assert.equal(snacks,1,'retry succeeds after failure cleared guard');
 console.log('RoomPanel failure cleanup and duplicate guard PASS');
}

// The intended visible mutation precedes backend acceptance. A read containing
// a newer revision, failure, or navigation must not overwrite another target.
for (const action of ['edit','delete']) {
 const id='8'.repeat(32),control={conv,id,dir:'out'},write=gate(),read=gate();let fail=false,holdRead=false,calls=0;
 let m={id,dir:'out',from:'fixture/laptop',body:'Original',revision:1,can:['edit','delete']};
 const host={platform:'daemon',workspace:{id:'messages-'+action},listen:()=>()=>{},api:async(path,body)=>{
  if(path.startsWith('/api/overview'))return {version:'fixture',me,dms:[],threads:[],people:[]};
  if(path.startsWith('/api/dm?')){if(holdRead)await read.promise;return {id:conv,kind:'dm',peer:{},messages:[structuredClone(m)]};}
  if(path.startsWith('/api/message/')){calls++;await write.promise;if(fail)throw Error('Refused');m={...m,revision:2,...(action==='edit'?{edited:true,text:body.text}:{deleted:true})};return {note:'Stored'};}
  if(path.startsWith('/api/agents'))return {agents:[]};return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'dm',id:conv});holdRead=true;
 const run=()=>action==='edit'?store.api.edit({...control,text:'Changed'}):store.api.retract(control);
 const work=run();assert.equal(run(),work,'same exact pending action shares one write');
 const shown=store.get().dm.messages[0];assert.equal(action==='edit'?shown.text:shown.deleted,action==='edit'?'Changed':true);
 assert.equal(shown._change.status,'pending');assert.equal(m.body,'Original');assert.equal(m.revision,1,'local display is not persistence');
 const messages=store.get().dm.messages;
 store.setDraft(conv,{text:'Typing while pending'});
 assert.equal(store.get().dm.messages,messages,'draft input must not rebuild the pending timeline');
 write.release();await work;assert.equal(calls,1);assert.equal(store.get().dm.messages[0]._change.status,'saved');
 read.release();await store.refreshOpen();assert.equal(store.get().dm.messages[0]._change,undefined);assert.equal(store.get().dm.messages[0].revision,2);
 store.stop();console.log(action+' local feedback before blocked write/read; exact single persistence PASS');
}
for(const changed of [false,true]){
 const id='9'.repeat(32),write=gate();let m={id,dir:'out',from:'fixture/laptop',body:'Original',revision:1,can:['edit']};
 const host={platform:'daemon',workspace:{id:'failure-'+changed},listen:()=>()=>{},api:async(path)=>{
  if(path.startsWith('/api/overview'))return {version:'fixture',me,dms:[],threads:[],people:[]};
  if(path.startsWith('/api/dm?'))return {id:conv,kind:'dm',peer:{},messages:[structuredClone(m)]};
  if(path==='/api/message/edit'){await write.promise;throw Error('Refused');}
  if(path.startsWith('/api/agents'))return {agents:[]};return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'dm',id:conv});
 const work=store.api.edit({conv,id,dir:'out',text:'Attempted'});const failure=assert.rejects(work,/Refused/);
 if(changed){m={...m,body:'Newer linked edit',revision:3};await store.refreshOpen();assert.equal(store.get().dm.messages[0].body,m.body);
  await assert.rejects(store.api.edit({conv,id,dir:'out',text:'Another edit'}),/previous message change is still pending/,'newer sync must not turn a different pending edit into success');
 }
 write.release();await failure;await store.refreshOpen();
 assert.equal(store.get().dm.messages[0].body,changed?'Newer linked edit':'Original');
 {assert.equal(store.get().dm.messages[0]._change.status,'failed');assert.equal(store.get().dm.messages[0]._change.control.text,'Attempted','failed edit text survives newer canonical content');
  assert.equal(unsentIn(store.get()),true,'automatic reload must retain an unsuccessful edit');
  store.discardFailedEdit({conv,id,dir:'out'});assert.equal(unsentIn(store.get()),false,'explicit editor cancel releases the failed draft');
 }
 store.stop();console.log('message failure preserves '+(changed?'newer linked state':'original and attempted text')+' PASS');
}
{
 const read=gate();const host={platform:'daemon',workspace:{id:'navigation'},listen:()=>()=>{},api:async(path)=>{
  if(path.startsWith('/api/dm?')){await read.promise;return view(conv);}
  if(path.startsWith('/api/overview'))return {version:'fixture',me,dms:[],threads:[],people:[]};
  if(path.startsWith('/api/agents'))return {agents:[]};return {};
 }};
 const store=new Store(host);await store.start();const opening=store.open({kind:'dm',id:conv});
 assert.equal(store.get().open.id,conv,'first uncached destination selected synchronously');assert.equal(store.get().pending.id,conv);assert.equal(store.get().dm,null);
 store.showTab('settings');assert.equal(store.get().tab,'settings','unrelated controls remain available');
 read.release();await opening;assert.equal(store.get().tab,'settings','late load cannot navigate back');store.stop();console.log('uncached navigation immediate; late load preserves newer tab PASS');
}
