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
const {Store}=await import(new URL('web/src/store.ts',root));
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
  assert.equal(before.busy,'done','B duplicate guard stays through B summary refresh');assert.equal(before.topic,'active');
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
