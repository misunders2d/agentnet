// Actual choice hooks, TSX components and Store, deterministic native events.
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createRequire,stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
import {api,errorText} from '../web/src/api.ts';
import {pendingSends} from '../static/optimistic.mjs';
import {share} from '../web/src/structural.ts';
import {mainPreferences,personRoots} from '../skins/shared/person-topics.mjs';
import model from './comic_model.mjs';
const require=createRequire(import.meta.url),esbuild=require('../web/node_modules/esbuild');
const tick=()=>new Promise(r=>setImmediate(r)),drain=async()=>{for(let i=0;i<6;i++)await tick();};
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
const source=file=>readFileSync(new URL('../web/src/'+file,import.meta.url),'utf8');
const jsx=(tag,props,...children)=>({tag,props:{...props,children:children.flat(Infinity).filter(c=>c!==false&&c!=null)}});
const nodes=t=>t&&typeof t==='object'?[t,...(t.props?.children||[]).flatMap(nodes)]:[];
const code=file=>esbuild.transformSync(source(file).replace(/^import[\s\S]*?from[^;]+;/gm,'').replace(/^export \{[^}]+\} from[^;]+;/gm,''),{loader:file.endsWith('tsx')?'tsx':'ts',jsxFactory:'jsx',jsxFragment:'Fragment',format:'cjs'}).code;
function hooks(){
 let cells=[],cursor=0,pending=[],active=true,rendering=false,queued=false,renderFn,out;
 const render=()=>{if(!active)return out;rendering=true;cursor=0;pending=[];out=renderFn();rendering=false;for(const f of pending)f();return out;};
 const schedule=()=>{if(!queued){queued=true;queueMicrotask(()=>{queued=false;if(active&&!rendering)render();});}};
 const react={useState:init=>{const i=cursor++;if(!cells[i])cells[i]={value:typeof init==='function'?init():init};return[cells[i].value,v=>{const next=typeof v==='function'?v(cells[i].value):v;if(!Object.is(next,cells[i].value)){cells[i].value=next;schedule();}}];},useRef:init=>{const i=cursor++;return cells[i]??(cells[i]={current:init});},useEffect:(fn,deps)=>{const i=cursor++,old=cells[i];if(!old||deps.some((d,j)=>d!==old.deps[j]))pending.push(()=>{old?.cleanup?.();cells[i]={deps,cleanup:fn()};});}};
 return {react,start:fn=>{renderFn=fn;return render();},render,get:()=>out,stop(){active=false;for(const c of cells)c?.cleanup?.();}};
}
function load(file,globals){const context={module:{exports:{}},exports:{},jsx,Fragment:"Fragment",setTimeout,clearTimeout,...globals};context.exports=context.module.exports;runInNewContext(code(file),context);return context.module.exports;}
const row=(id,unread=1,at=1)=>({id,conv:'chat',peer:'peer/host',title:'needle '+id,last:'last',lastAt:new Date(at*1000).toISOString(),unread,state:'active',count:1,needsYou:0});
let h=hooks(),choice=load('features/Conversation.choices.ts',h.react);
assert.deepEqual([...choice.steadyChoices([row('a'),row('b')],[row('b',0),row('c')],r=>r.id,true)].map(c=>[c.row.id,c.present]),[['a',false],['b',true],['c',true]]);
let latest=[row('a'),row('b')],selection=false;
h.start(()=>choice.useSteadyChoices(latest,r=>r.id,'scope',selection));h.get().bind.onPointerEnter();await drain();latest=[row('b',0),row('c')];h.render();assert.deepEqual(Array.from(h.get().choices,c=>c.row.id),['a','b','c']);assert.equal(h.get().choices[0].present,false);assert.equal(h.get().choices[1].row.unread,0);
h.get().bind.onPointerLeave();await drain();assert.deepEqual(Array.from(h.get().choices,c=>c.row.id),['b','c']);h.get().bind.onFocusCapture();await drain();latest=[row('c'),row('b')];h.render();assert.equal(h.get().choices[0].row.id,'b');h.get().bind.onBlurCapture({currentTarget:{contains:()=>false},relatedTarget:null});await drain();assert.equal(h.get().choices[0].row.id,'c');h.stop();
// A synchronous Store render may precede React's continuous-event state commit.
// Keep state updates deferred here: the event must hold identity immediately.
{
 let cells=[],cursor=0,rows=[row('old'),row('stay')];
 const delayed={useState:()=>{cursor++;return[false,()=>{}];},useRef:init=>{const i=cursor++;return cells[i]??(cells[i]={current:init});},useEffect(){}};
 const actual=load('features/Conversation.choices.ts',delayed),render=()=>{cursor=0;return actual.useSteadyChoices(rows,r=>r.id,'held');};
 let view=render();view.bind.onPointerEnter();rows=[row('stay'),row('new')];view=render();
 assert.equal(view.choices[0].row.id,'old');assert.equal(view.choices[0].present,false);
}
// List growth may put a child under a pointer already inside the list without
// delivering enter to this binding. Actual bubbling over/move must start the
// existing ref hold before an external render; repeated movement stays cheap.
for(const event of ['onPointerOver','onPointerMove']){
 let cells=[],cursor=0,updates=0,rows=[row('old'),row('stay')];
 const delayed={useState:()=>{cursor++;return[0,()=>{updates++;}];},useRef:init=>{const i=cursor++;return cells[i]??(cells[i]={current:init});},useEffect(){}};
 const actual=load('features/Conversation.choices.ts',delayed),render=()=>{cursor=0;return actual.useSteadyChoices(rows,r=>r.id,'bubbling-held');};
 let view=render();rows=[...rows,row('grown')];view=render();
 assert.equal(typeof view.bind[event],'function',event+' must hold when enter was never delivered');
 view.bind[event]();assert.equal(updates,1);
 for(let i=0;i<100;i++){view.bind[event]();view.bind.onPointerOver?.();view.bind.onPointerMove?.();}
 assert.equal(updates,1,'already-held child moves do not schedule redundant renders');
 rows=[{...row('grown'),title:'Updated child'},row('stay',9),row('new')];view=render();
 assert.deepEqual(Array.from(view.choices,c=>[c.row.id,c.present]),[['old',false],['stay',true],['grown',true],['new',true]],'external update retains displayed identity and disables vanished row');
 assert.equal(view.choices[1].row.unread,9,'present scalar updates in place');
 assert.equal(view.choices[2].row.title,'Updated child','present child text updates in place');
 view.bind.onPointerLeave();assert.equal(updates,2);view=render();
 assert.deepEqual(Array.from(view.choices,c=>c.row.id),['grown','stay','new'],'leave releases normal ordering');
 view.bind.onPointerMove();view.bind.onFocusCapture();view.release();view=render();
 assert.deepEqual(Array.from(view.choices,c=>c.row.id),['grown','stay','new']);
}
// Prefix >200, sequential bounded requests, stable keys, no truncation on push.
h=hooks();choice=load('features/Conversation.choices.ts',h.react);
let revision=1,scope='one',enabled=true,data=Array.from({length:320},(_,i)=>row(String(i))),calls=[],concurrent=0,max=0,gate=null;
const fetch=async(after,limit)=>{concurrent++;max=Math.max(max,concurrent);calls.push({scope,revision,after,limit});if(gate){const g=gate;gate=null;await g.promise;}const from=after?Number(after):0,page=data.slice(from,from+limit);concurrent--;return {rows:page,total:data.length,next:from+page.length<data.length?String(from+page.length):'',content:revision,seq:revision};};
h.start(()=>choice.usePagedChoices(enabled,scope,revision,50,r=>r.id,fetch));await drain();assert.equal(h.get().rows.length,50);
for(let i=0;i<5;i++){h.get().more();await drain();}assert.equal(h.get().rows.length,300);
const before=calls.length;data=[row('new'),...data.slice(1)];revision++;h.render();await drain();assert.equal(h.get().rows.length,300);assert.deepEqual(calls.slice(before).map(c=>c.limit),[200,100]);assert.equal(new Set(h.get().rows.map(r=>r.id)).size,300);assert.equal(max,1);
// Coalesce pushes while one response is delayed, and discard the old pass.
gate=deferred();const old=gate;revision++;h.render();await tick();revision++;h.render();revision++;h.render();old.resolve();await drain();assert.equal(max,1);assert.equal(h.get().rows.length,300);
const currentCalls=calls.length;h.render();await drain();assert.equal(calls.length,currentCalls,'unchanged content does not refetch');
gate=deferred();const closing=gate;revision++;h.render();await tick();enabled=false;h.render();closing.resolve();await drain();assert.equal(h.get().rows.length,0,'closed list drops late replies');h.stop();
// A repeating cursor cannot create an infinite duplicate-only loop.
h=hooks();choice=load('features/Conversation.choices.ts',h.react);let repeatCalls=0;
h.start(()=>choice.usePagedChoices(true,'repeat',1,50,r=>r.id,async()=>{repeatCalls++;return {rows:[row('one')],total:500,next:'same'};}));await drain();assert.equal(repeatCalls,2);assert.match(h.get().error,/changed/);h.stop();
// Actual AllTopics source: all-status search, trim/clear/filter and held order.
function component(globals){
 const timers=new Map();let now=0,timerID=0;
 const clock={setTimeout:(fn,ms)=>{const id=++timerID;timers.set(id,{fn,at:now+ms});return id;},clearTimeout:id=>timers.delete(id)};
 const host=hooks();const helpers=load('features/Conversation.choices.ts',host.react);const store={get:()=>({views:{}}),api:{topics:async q=>({topics:[],matched:0})},draft:()=>({text:''}),openUnread(){},toast(){}};
 const shared={...host.react,...helpers,jsx,useApp:()=>store,useWide:()=>true,useStore:(_s,select)=>select({overview:{seq:1,threads:[]},contentRevision:1}),usePortal:()=>null,useModal:()=>({}),TOPICS:{searchDelay:1,pageSize:50,pageMax:200,undoDelay:1},topicOf:t=>t,firstLine:t=>t,when:()=>'',topicLabel:t=>t.title,errorText:e=>e.message,Button:'Button',IconButton:'IconButton',TopicMark:'TopicMark',IconSearch:'Icon',IconChevronLeft:'Icon',IconX:'Icon',Dialog:Object.fromEntries(['Root','Portal','Backdrop','Popup','Title','Description','Close'].map(k=>[k,k])),...globals};
 const exports=load('features/Conversation.alltopics.tsx',{...shared,...clock});
 // Commit the input render before advancing its debounce, independent of host load.
 const advance=async ms=>{await drain();now+=ms;for(const [id,timer]of [...timers])if(timer.at<=now){timers.delete(id);timer.fn();}await drain();};
 return {host,store,exports,advance};}
let flat=[row('read',0,9),row('unread',2,1),{...row('done'),state:'done'},{...row('archived'),state:'archived'}];
const c=component();let props={open:true,onOpenChange(){},peer:'',conv:'chat',agent:'Fixture',flat:{topics:flat,loading:false,failed:'',canSelect:()=>true,detail:()=>'',choose(){}}};
c.host.start(()=>c.exports.AllTopics(props));await drain();let tree=c.host.get();assert.equal(nodes(tree).find(n=>n.tag==='li').props.children[1].props.children[0].props.children[0].props.children[0],'needle unread');
const search=nodes(tree).find(n=>n.tag==='input'&&n.props.type==='search');search.props.onChange({target:{value:'  needle  '}});await c.advance(1);tree=c.host.get();assert.equal(nodes(tree).filter(n=>n.tag==='li').length,4,'search spans all statuses');
const clear=nodes(tree).find(n=>n.tag==='IconButton'&&n.props.label==='Clear search');clear.props.onClick();await c.advance(1);assert.equal(nodes(c.host.get()).filter(n=>n.tag==='li').length,2,'clear restores Active');
nodes(c.host.get()).find(n=>n.tag==='button'&&n.props.children?.[0]?.props?.children?.[0]==='Done').props.onClick();await drain();
nodes(c.host.get()).find(n=>n.tag==='input'&&n.props.type==='search').props.onChange({target:{value:'  needle  '}});await c.advance(1);assert.equal(nodes(c.host.get()).filter(n=>n.tag==='li').length,4);
nodes(c.host.get()).find(n=>n.tag==='IconButton'&&n.props.label==='Clear search').props.onClick();await c.advance(1);assert.equal(nodes(c.host.get()).filter(n=>n.tag==='li').length,1,'clear preserves selected Done');
nodes(c.host.get()).find(n=>n.tag==='input'&&n.props.type==='search').props.onChange({target:{value:'undefined'}});await c.advance(1);assert.equal(nodes(c.host.get()).filter(n=>n.tag==='li').length,0,'missing autoTitle cannot add a false search match');
c.host.stop();
// An explicit bulk confirmation dispatches immediately with its captured IDs.
// Panel lifetime and repeated input cannot cancel or repeat that request.
for(const what of ['archive','done','delete'])for(const leave of ['stay','close','scope','unmount','cancel']){
 const f=component(),calls=[],notices=[],gate=deferred();
 let p={...props,open:true,flat:{...props.flat,topics:[row('exact',2),{...row('second'),count:3}],change:(...args)=>{calls.push(args);return gate.promise;}}};
 f.store.toast=(...args)=>notices.push(args);
 f.host.start(()=>f.exports.AllTopics(p));await drain();
 const button=label=>nodes(f.host.get()).find(n=>n.tag==='Button'&&n.props.children[0]===label);
 for(const id of ['exact','second']){nodes(f.host.get()).find(n=>n.tag==='input'&&n.props['aria-label']==='Select needle '+id).props.onChange();await drain();}
 button(what==='delete'?'Delete for me':what==='done'?'Mark done':'Archive').props.onClick();await drain();
 assert.equal(calls.length,0,'review alone makes no change');
 if(what==='delete')assert(nodes(f.host.get()).some(n=>n.tag==='p'&&n.props.children.join('').includes("can't be undone")),'delete review states irreversibility');
 if(leave==='cancel'){
  button('Cancel').props.onClick();await drain();assert.equal(calls.length,0,'Cancel before confirmation makes no change');f.host.stop();continue;
 }
 const confirm=button('Confirm');confirm.props.onClick();confirm.props.onClick();await drain();
 assert.equal(calls.length,1,'Confirm must dispatch once immediately, before any timer or navigation');
 assert.equal(calls[0][0],what);assert.deepEqual(Array.from(calls[0][1]),['exact','second']);assert.deepEqual({...calls[0][2]},{exact:1,second:3});
 assert.equal(notices.length,0,'no success before the host result');
 const pending=nodes(f.host.get()).find(n=>n.tag==='Button'&&/^(Archiving|Marking|Deleting) 2/.test(String(n.props.children[0])));
 assert(pending?.props.disabled,'pending confirmation stays visible and disabled');
 assert(!button('Undo'),'no local timer is presented as a durable Undo');
 if(leave==='unmount')f.host.stop();else if(leave!=='stay'){p={...p,...(leave==='close'?{open:false}:{conv:'other-chat'})};f.host.render();}
 gate.resolve({note:'2 topics updated'});await drain();await f.advance(10000);
 assert.equal(calls.length,1,'departure/result/timers never repeat a captured operation');
 assert.deepEqual(notices,[['2 topics updated','ok']],'confirmed result survives panel departure');
 f.host.stop();
}
// Partial results use the host report; refused/unknown actions never claim success.
for(const result of [{note:'1 of 2 archived; 1 unchanged'},undefined,new Error('Permission changed')]){
 const f=component(),notices=[];f.store.toast=(...args)=>notices.push(args);
 f.host.start(()=>f.exports.AllTopics({...props,flat:{...props.flat,topics:[row('exact')],change:async()=>{if(result instanceof Error)throw result;return result;}}}));await drain();
 const button=label=>nodes(f.host.get()).find(n=>n.tag==='Button'&&n.props.children[0]===label);
 nodes(f.host.get()).find(n=>n.tag==='input'&&n.props.type==='checkbox').props.onChange();await drain();button('Archive').props.onClick();await drain();button('Confirm').props.onClick();await drain();
 assert.deepEqual(notices,result&&!(result instanceof Error)?[[result.note,'ok']]:[],'only actual host success is announced; current modal errors stay local');
 if(!result||result instanceof Error)assert(nodes(f.host.get()).some(n=>n.props.role==='alert'&&n.props.children.join('').includes(result instanceof Error?'Permission changed':"wasn't confirmed")),'failed confirmation is readable inside the active panel');
 if(!result||result instanceof Error)assert(button('Confirm')&&!button('Confirm').props.disabled,'failure retains review for explicit retry');
 f.host.stop();
}
// Keep existing non-flat query/filter/Show more scope assertions on actual TSX.
for(const filter of ['Active','Done','Archived']){
 const f=component(),calls=[],rows=Array.from({length:120},(_,i)=>row(String(i)));
 f.store.api.topics=async q=>{calls.push(q);const from=q.before?Number(q.before):0;return{topics:rows.slice(from,from+q.limit),matched:120,next:from+q.limit<120?String(from+q.limit):''};};
 f.host.start(()=>f.exports.AllTopics({...props,flat:undefined,peer:'peer/host'}));await drain();
 nodes(f.host.get()).find(n=>n.tag==='button'&&n.props.children?.[0]?.props?.children?.[0]===filter).props.onClick();await drain();
 nodes(f.host.get()).find(n=>n.tag==='input'&&n.props.type==='search').props.onChange({target:{value:'  needle  '}});await f.advance(1);
 assert.equal(calls.at(-1).state,'');assert.equal(calls.at(-1).q,'needle');assert.equal(calls.at(-1).conv,'chat');assert.equal(calls.at(-1).peer,'peer/host');
 nodes(f.host.get()).find(n=>n.tag==='Button'&&String(n.props.children[0]).startsWith('Show more')).props.onClick();await drain();
 assert.equal(calls.at(-1).state,'');assert.equal(calls.at(-1).before,'50');assert.equal(calls.at(-1).conv,'chat');assert.equal(calls.at(-1).peer,'peer/host');
 nodes(f.host.get()).find(n=>n.tag==='IconButton'&&n.props.label==='Clear search').props.onClick();await f.advance(1);assert.equal(calls.at(-1).state,filter.toLowerCase());f.host.stop();
}
// Preserve the existing zero-chip archived-unread regression on actual TopicBar.
{
 const bar=load('features/Conversation.topics.tsx',{
  useApp:()=>({}),useWide:()=>false,useAgentNames:()=>({}),useStore:(_store,select)=>select({overview:{topic_list:true},drafts:{chat:{topic:'closed'}},pending:null}),
  useState:init=>[typeof init==='number'?0:init,()=>{}],useRef:current=>({current}),useLayoutEffect(){},useSteadyChoices:rows=>({choices:rows.map(row=>({row,present:true})),bind:{},release(){}}),unreadTopicOrder:()=>0,
  chatList:()=>[],newestFirst:()=>0,topicOf:t=>({...t,needsYou:0,lastAt:''}),topicMark:()=>null,firstLine:t=>t,TOPICS:{barMax:4},AllTopics:'AllTopics',
  IconPlus:'Icon',IconListSearch:'Icon',IconAlertCircle:'Icon',IconArchive:'Icon',IconCircleCheck:'Icon',IconClock:'Icon',IconClockQuestion:'Icon'});
 const tree=bar.TopicBar({dm:{id:'chat',peer:{label:'Fixture'},main_topic:{unread:0},topics:[{id:'closed',title:'Closed',state:'archived',unread:3}]}});
 const all=nodes(tree).find(n=>n.tag==='button'&&n.props['aria-label']?.startsWith('All topics'));
 assert(all);assert.match(all.props['aria-label'],/3 unread in other topics/);
}
// Review's actual source keeps display/navigation apart from approval counts.
h=hooks();const st={unreadReviewSize:50,get:()=>({views:{}}),openUnreadRow:r=>opened.push(r),api:{}};let opened=[],page={rows:[{key:'stable',kind:'chat',conv:'chat',topic:'',id:'selected',lid:'logical',author:'author',dir:'out',at:'2026-10-16T00:00:00Z',label:'You',preview:'Review preview'}],total:1,next:'',loading:false,error:'',retry(){},more(){}};
const reviewHelpers=load('features/Conversation.choices.ts',h.react);const review=load('features/Approvals.unread.tsx',{...h.react,useApp:()=>st,useStore:(_s,f)=>f({overview:{unread_total:1,dms:[{id:'chat',peer:{label:'Casey'}}]},contentRevision:1}),...reviewHelpers,usePagedChoices:()=>page,when:()=>'',Button:'Button'});
h.start(()=>review.UnreadReview());await drain();tree=h.get();const button=nodes(tree).find(n=>n.tag==='button');assert.match(button.props['aria-label'],/Casey.*Main flow/);assert.equal(opened.length,0);button.props.onClick();assert.equal(opened[0],page.rows[0]);page={...page,loading:true};h.render();assert(nodes(h.get()).some(n=>n.props.role==='status'&&n.props.className==='sr-only'),'refresh status cannot move rows under the pointer');page={...page,rows:[],total:0,loading:false};h.render();assert(nodes(h.get()).some(n=>n.props.role==='status'&&n.props.children.includes('No unread messages.')));h.stop();
{ const state={overview:{unread_total:1,dms:[{id:'chat',peer:{label:'Casey'}}]},contentRevision:1};
 for(const r of [{...page.rows[0],key:'named',kind:'chat',conv:'chat',topic:'named',topic_title:'Never opened literal name',label:'Author'},{key:'device',kind:'device',peer:'agent/host',topic_title:'Device literal title',label:'Agent',preview:'Device preview'}]) {
  const x=hooks(),part=load('features/Approvals.unread.tsx',{...x.react,useApp:()=>st,useStore:(_s,f)=>f(state),...reviewHelpers,useSteadyChoices:rows=>({choices:rows.map(row=>({row,present:true})),bind:{},release(){}}),usePagedChoices:()=>({...page,rows:[r],total:1}),when:()=>'',Button:'Button'});
  x.start(()=>part.UnreadReview());const b=nodes(x.get()).find(n=>n.tag==='button');assert(b.props['aria-label'].includes(r.topic_title),'literal title without cached Store view');assert(b.props['aria-label'].includes(r.label),'author remains separate');x.stop();
 }
}
const approvals=load('features/Approvals.tsx',{...hooks().react,decidable:o=>o.needs_you||[],heldDecisionPeers:()=>new Set(),isWorkingReview:()=>false,incomingGroupInvites:load('features/RoomPanel.model.ts',{}).incomingGroupInvites});
assert.equal(approvals.needsYouCount({review:[],needs_you:[],unread_total:9}),0);assert.equal(approvals.needsYouCount({review:[],needs_you:[{}],unread_total:0}),1);
{const old={conv:'joined-room',inviter:'inviter/desk',direction:'in',status:'stale'};for(const status of ['accepted','declined']){const current={...old,status};assert.equal(approvals.needsYouCount({group_invitations:[old,current]}),0,status+' current invitation leaves no stale Review badge');assert.equal(approvals.needsYouCount({group_invitations:[old,{...current,inviter:'another/desk'}]}),1,'another inviter does not erase the stale decision');}}
// Actual Store bound navigation: exact one GET, current author/LID/scope, draft
// preservation, no action or history search, Back, and stale navigation fence.
const rawStore=source('store.ts').replace(/^import .*;$/gm,'').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host=host;').replace(/^export /gm,'');
const storeSource=stripTypeScriptTypes(rawStore);
const scheduleSource=stripTypeScriptTypes(source('schedule-saves.ts').replace(/^import .*;$/gm,'')).replace(/^export /gm,'');
const values=new Map(),listeners=new Set(),requests=[],conv='c'.repeat(64),topic='b'.repeat(32),mid='a'.repeat(32),lid='e'.repeat(32),fp='11111111-11111111-11111111-11111111';let loadGate=null;
const dm={id:conv,messages:[{id:mid,lid,dir:'out',topic,send_group_author:fp,kind:'message',body:'selected',unread:true,attention:{rev:4,writer:fp}}],topics:[{id:topic}],agents:[]};
const host={platform:'daemon',workspace:{id:'test',handle:'h'},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},async api(path,body){requests.push({path,body});if(path.startsWith('/api/overview'))return {seq:requests.length,version:'test',me:{fingerprint:fp},dms:[],threads:[]};if(path.startsWith('/api/agents'))return{agents:[]};if(path.startsWith('/api/dm?')){if(loadGate)await loadGate.promise;return structuredClone(dm);}if(path.startsWith('/api/thread?'))return{id:'thread',peer:'peer/host',key:{pinned:fp},messages:[{id:mid,dir:'in',kind:'message',body:'device',unread:true}]};if(path.startsWith('/api/typing'))return{};if(path==='/api/act'&&body.do==='read')return{note:'Recorded exact read'};throw Error('Unexpected '+path);}};
const {Store,topicChangeKey}=runInNewContext(scheduleSource+'\n'+storeSource+'\n({Store,topicChangeKey})',{api,errorText,pendingSends,share,messageTarget:model.messageTarget,currentCatalogAgents:model.currentCatalogAgents,directChatKey:model.directChatKey,mainPreferences,personRoots,brand:{storage:'test'},document:{visibilityState:'visible',hasFocus:()=>true},window:{},localStorage:{getItem:k=>values.get(k)||null,setItem:(k,v)=>values.set(k,v)},setTimeout:(f,ms)=>{const t=setTimeout(f,ms);t.unref();return t;},clearTimeout});
// A typed verification wait retains the exact last view and draft without
// claiming new membership or polling. Only a fresh successful view clears it.
{
 let mode='ready',held=null;
 const h={...host,api:async(path,body)=>{
  if(path.startsWith('/api/dm?')){
   if(held){const g=held;held=null;return g.promise;}
   if(mode==='pending')throw Object.assign(new Error('Updating verified group details.'),{code:'group_context_pending',status:503});
   if(mode==='denied')throw new Error('Permission denied');
   return {...structuredClone(dm),kind:'group',title:'Verified group'};
  }
  return host.api(path,body);
 }};
 const active=new Store(h);await active.start();await active.open({kind:'dm',id:conv});active.setDraft(conv,{text:'kept group draft'});
 const before=active.get().views[conv];mode='pending';assert.equal(await active.refreshOpen(),false);
 assert.equal(active.get().groupContextPending,conv,'typed pending group is visible as a verification wait');
 assert.equal(active.get().views[conv],before,'pending does not replace a verified DTO with partial context');
 assert.equal(active.draft(conv).text,'kept group draft');assert.equal(active.get().toasts.length,0,'verification wait is not a red error toast');
 mode='ready';assert.equal(await active.refreshOpen(),true);assert.equal(active.get().groupContextPending,'');
 const stale=deferred();held=stale;const old=active.loadOpen(active.get().open,true);await drain();
 await active.loadOpen(active.get().open,true);stale.resolve(Promise.reject(Object.assign(new Error('old pending'),{code:'group_context_pending'})));await old;
 assert.equal(active.get().groupContextPending,'','old wait cannot refreeze a newer successful projection');
 mode='denied';await active.refreshOpen();assert(active.get().toasts.some(t=>t.text==='Permission denied'&&t.tone==='error'),'actual refusal remains an error');
 active.close();assert.equal(active.get().groupContextPending,'');active.stop();
}
// F1: rows can already be reported while Review fences acknowledgement.
// Returning to Chats must use that unchanged visible set, with entry/CAS intact.
{
 const extra=(id,rev)=>({...dm.messages[0],id,lid:id,attention:{rev,writer:fp}});
 dm.messages.push(extra('f'.repeat(32),4),extra('d'.repeat(32),4));
 const active=new Store(host);await active.start();active.setDraft(conv,{text:'kept',topic});await active.open({kind:'dm',id:conv});active.showTab('oks');
 dm.messages[1].attention={rev:5,writer:fp};await active.refetch();
 const view=active.get().views[conv],visit=active.readVisit(active.get().open,topic),before=requests.filter(r=>r.path==='/api/act').length;
 active.reportVisible(visit,view.messages.slice(0,2));await drain();
 assert.equal(requests.filter(r=>r.path==='/api/act').length,before,'Review acknowledges zero visible rows');
 active.showTab('chats');await drain();
 const reads=requests.filter(r=>r.path==='/api/act').slice(before);
 assert.equal(reads.length,1,'Review -> Chats acknowledges already visible rows without a scroll');
 assert.deepEqual(reads[0].body.ids,[mid]);assert.deepEqual(JSON.parse(JSON.stringify(reads[0].body.attention)),{[mid]:{rev:4,writer:fp}},'unseen tail and newer attention generation stay untouched');
 active.showTab('chats');await drain();assert.equal(requests.filter(r=>r.path==='/api/act').length,before+1,'same tab cannot repeat acknowledgement');active.stop();dm.messages.splice(1);requests.length=0;
}
const store=new Store(host);await store.start();store.showTab('oks');const files=[{key:'staged'}];store.setDraft(conv,{text:'kept',replyTo:'reply',files,newTopic:true});
const bound={key:'chat',kind:'chat',conv,topic,id:mid,lid,author:fp,dir:'out',at:'',label:'You',preview:'selected'};
const publications=[];const off=store.subscribe(()=>{if(store.get().open)publications.push(store.returningToReview);});
const start=requests.length;assert.equal(await store.openUnreadRow(bound),true);assert.equal(publications.at(-1),true,'synchronous open subscriber sees Review return before publication');off();assert.equal(requests.length-start,1);assert.equal(store.get().open.focus,mid);assert.equal(store.draft(conv).topic,topic);assert.equal(store.draft(conv).text,'kept');assert.equal(store.draft(conv).files,files);assert.equal(store.draft(conv).replyTo,'reply');assert.equal(requests.some(r=>r.path==='/api/act'),false);store.close();assert.equal(store.get().tab,'oks');
assert.equal(await store.openUnreadRow({...bound,author:'wrong'}),false);assert.equal(store.get().open,null);
assert.equal(await store.openUnreadRow({kind:'device',thread:'thread',peer:'peer/host',id:mid,lid:mid,author:fp,dir:'in'}),true);assert.equal(store.get().open.id,'thread');store.close();
loadGate=deferred();const pendingOpen=store.openUnreadRow(bound);await tick();store.showTab('settings');loadGate.resolve();await pendingOpen;assert.equal(store.get().open,null);loadGate=null;
loadGate=deferred();const staleOpen=store.openUnreadRow(bound);await tick();const explicitLink=store.openLinkTarget(conv,'topic',{topic});loadGate.resolve();assert.equal(await staleOpen,false,'an explicit link cancels a pending Review landing');await explicitLink;assert.equal(store.get().open.focus,undefined);assert.equal(store.returningToReview,false,'another explicit open clears Review return');store.close();loadGate=null;
const revisionBefore=store.get().contentRevision;for(const fn of listeners)fn({type:'change',seq:900,hint:'presence'});await drain();assert.equal(store.get().contentRevision,revisionBefore);for(const fn of listeners)fn({type:'change',seq:901,hint:'typing'});await drain();assert.equal(store.get().contentRevision,revisionBefore);await store.refetch();assert.equal(store.get().contentRevision,revisionBefore+1);store.stop();
console.log('actual AllTopics source/model PASS');console.log('PASS Review/choices: >200 prefix, coalescing, stale/close/cursor, pointer/focus, all-status search, exact Store identity/drafts/Back, no actions or presence/typing refetch');

{
 const useRetainedText=runInNewContext(code('retained-text.ts')+'\nuseRetainedText',{module:{exports:{}},exports:{},useApp:()=>store,useState:v=>[v,()=>{}]});
 const RenameTopic=runInNewContext(code('features/Conversation.topics.tsx')+'\nRenameTopic',{module:{exports:{}},exports:{},jsx,useRetainedText,topicChangeKey,useApp:()=>store,useState:v=>[v,()=>{}],useLayoutEffect(){},Sheet:'Sheet',Button:'Button',TOPICS:{titleMax:160},IconAlertCircle:'Icon',IconArchive:'Icon',IconCircleCheck:'Icon',IconClock:'Icon',IconClockQuestion:'Icon'});
 for(const [scope,shared] of [[{conv:'chat',id:'named'},true],[{conv:'chat',id:'',root:true},false],[{peer:'agent/host',id:'device'},false]]) {
  const t=RenameTopic({open:true,onOpenChange(){},topic:{...scope,title:'Current',autoTitle:'Automatic',renamed:true}}),text=[...nodes(t),...nodes(t.props.footer)].flatMap(n=>n.props.children||[]).filter(x=>typeof x==='string').join(' ');
  assert.equal(t.props.description.includes('for everyone with access'),shared);assert.equal(t.props.description.includes('automatic title for everyone'),shared);assert.equal(text.includes('Use automatic title for everyone'),shared);if(!shared)assert(t.props.description.includes('Other people keep their own names'));
 }
}
