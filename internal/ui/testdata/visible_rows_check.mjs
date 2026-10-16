// Real hook/scroll callbacks with deterministic IO membership and rectangles.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
const source=await readFile(new URL('../web/src/features/Conversation.timeline.tsx',import.meta.url),'utf8');
const hook=stripTypeScriptTypes(source.slice(source.indexOf('function useVisibleRows('),source.indexOf('function build(')));
class Events {
 listeners=new Map();
 addEventListener(k,f){const set=this.listeners.get(k)||new Set();set.add(f);this.listeners.set(k,set);}
 removeEventListener(k,f){this.listeners.get(k)?.delete(f);}
 fire(k){for(const f of this.listeners.get(k)||[])f();}
}
const window=new Events(),document=new Events(),observers=[],reports=[],native=new Set(),frames=new Map();
document.visibilityState='visible';let focused=true,nativeVisible=true,frameID=0,cursor=0,pending=[],cells=[],rects=0,rootRects=0,rootHeight=1000;
document.hasFocus=()=>focused;
const raf=fn=>{frames.set(++frameID,fn);return frameID;},flush=()=>{const batch=[...frames.values()];frames.clear();batch.forEach(fn=>fn());};
class Observer {
 constructor(callback,options){this.callback=callback;this.options=options;this.rows=[];observers.push(this);}
 observe(row){this.rows.push(row);}
 disconnect(){this.disconnected=true;}
 emit(row,show=true){this.callback([{target:row,isIntersecting:show,intersectionRect:{width:show?50:0,height:show?1:0}}]);}
}
const rectangle=(top,height,left=0,width=100)=>({top,bottom:top+height,left,right:left+width,width,height});
const row=(id,top,height)=>({dataset:{mid:id},rect:rectangle(top,height),getBoundingClientRect(){rects++;return this.rect;}});
const messages=[{id:'short',unread:true},{id:'tall',unread:true},...Array.from({length:2000},(_,i)=>({id:'off'+i,unread:true}))];
const rows=[row('short',999,100),row('tall',999,5000),...messages.slice(2).map(m=>row(m.id,10000,100)),row('foreign',0,100)];
const box={current:{getBoundingClientRect(){rootRects++;return rectangle(0,rootHeight);}}},inner={current:{querySelectorAll:()=>rows}};
let visit={scope:'exact-entry'},reserve=0;
const store={host:{platform:'android',android:{visible:()=>nativeVisible,onVisibility:f=>{native.add(f);return()=>native.delete(f);}}},reportVisible:(v,ms)=>reports.push({visit:v,ids:Array.from(ms,m=>m.id)})};
const useVisibleRows=runInNewContext(hook+'\nuseVisibleRows',{window,document,IntersectionObserver:Observer,requestAnimationFrame:raf,cancelAnimationFrame:id=>frames.delete(id),useApp:()=>store,
 useRef:init=>{const i=cursor++;return cells[i]||(cells[i]={current:init});},
 useEffect:(fn,deps)=>{const i=cursor++,had=cells[i];if(!had||deps.some((d,j)=>d!==had.deps[j]))pending.push(()=>{had?.cleanup?.();cells[i]={deps,cleanup:fn()};});}});
let schedule;
const render=()=>{cursor=0;pending=[];schedule=useVisibleRows(box,inner,{conv:'chat'},messages,visit,'short',reserve);pending.forEach(fn=>fn());};
render();
const ids=()=>reports.at(-1)?.ids||[];
assert.equal(observers.length,1,'one timeline observer');assert.equal(observers[0].options.root,box.current);
assert.deepEqual(observers[0].rows,rows.slice(0,-1),'only mounted canonical rows observed');
observers[0].emit(rows[0]);flush();assert.deepEqual(ids(),[],'one pixel of a short row is not seen');
rows[0].rect=rectangle(950,100);schedule.current();schedule.current();assert.equal(frames.size,1,'scroll coalesces once per frame');flush();assert.deepEqual(ids(),['short'],'half a short row is seen');
const stable=reports.length;schedule.current();flush();assert.equal(reports.length,stable,'unchanged visible set is not reported again');
observers[0].emit(rows[1]);flush();assert.deepEqual(ids(),['short'],'one pixel of a five-viewport row is not seen');
rows[1].rect=rectangle(500,5000);schedule.current();flush();assert.deepEqual(ids(),['short','tall'],'scroll sees half viewport without another IO callback');
reserve=200;render();flush();assert.equal(observers.length,1,'reserve does not recreate membership observer');assert.deepEqual(ids(),[],'footer reserve excludes short row and insufficient tall slice');
rows[0].rect=rectangle(750,100);schedule.current();flush();assert.deepEqual(ids(),['short'],'half short row above reserved footer is seen');
rows[0].rect=rectangle(800,100);schedule.current();flush();assert.deepEqual(ids(),[],'fully under footer remains unread');
rows[0].rect=rectangle(760,200);schedule.current();flush();assert.deepEqual(ids(),[],'40 pixels of a 200px row insufficient');
rootHeight=1100;schedule.current();flush();assert.deepEqual(ids(),['short'],'resize-only viewport growth sees half a row without IO');
rootHeight=1000;schedule.current();flush();assert.deepEqual(ids(),[],'resize shrink removes the insufficiently visible row');
rows[0].rect=rectangle(760,60);schedule.current();flush();assert.deepEqual(ids(),['short'],'resize-only geometry change sees half the row');
rows[0].rect=rectangle(700,100,200,100);schedule.current();flush();assert.deepEqual(ids(),[],'zero horizontal intersection is not seen');
rows[0].rect=rectangle(700,0);schedule.current();flush();assert.deepEqual(ids(),[],'zero-height row is not seen');
rects=0;rootRects=0;for(let i=0;i<100;i++)schedule.current();assert.equal(frames.size,1);flush();assert.equal(rootRects,1,'one root rectangle per frame');assert.equal(rects,2,'only the two intersecting members measured, not 2000 offscreen rows');
rows[0].rect=rectangle(700,100);schedule.current();const staleFrame=[...frames.values()][0];focused=false;window.fire('blur');assert.equal(frames.size,0,'blur cancels pending geometry');const stopped=reports.length;staleFrame();observers[0].emit(rows[0]);assert.equal(reports.length,stopped,'stale frame and disconnected IO cannot report');
focused=true;document.visibilityState='hidden';window.fire('focus');assert.equal(observers.length,1,'hidden page cannot observe');
document.visibilityState='visible';document.fire('visibilitychange');assert.equal(observers.length,2);observers[1].emit(rows[0]);flush();assert.deepEqual(ids(),['short']);
nativeVisible=false;for(const f of native)f(false);assert.deepEqual(ids(),[]);const paused=reports.length;observers[1].emit(rows[1]);flush();assert.equal(reports.length,paused,'native pause fences old membership');
nativeVisible=true;for(const f of native)f(true);assert.equal(observers.length,3);observers[2].emit(rows[0]);schedule.current();const oldFrame=[...frames.values()][0],oldSchedule=schedule.current;visit={scope:'next-entry'};render();oldFrame();oldSchedule();flush();assert.equal(ids().length,0,'old visit frame/scheduler cannot report');
const current=observers.at(-1);current.emit(rows[0]);flush();assert.equal(reports.at(-1).visit,visit);assert.deepEqual(ids(),['short']);schedule.current();const unmounted=[...frames.values()][0];for(const c of cells)c?.cleanup?.();assert.equal(frames.size,0);const done=reports.length;unmounted();current.emit(rows[0]);assert.equal(reports.length,done,'unmounted callbacks cannot report');
assert.equal(native.size,0);assert.equal([...window.listeners.values()].reduce((n,s)=>n+s.size,0),0);assert.equal([...document.listeners.values()].reduce((n,s)=>n+s.size,0),0);

// Execute the existing no-target entry, scroll and resize callbacks themselves.
const entryStart=source.indexOf('  useLayoutEffect(() => {'),entryEnd=source.indexOf('\n  useLayoutEffect(() => {',entryStart+1);
const el={scrollHeight:50,scrollTop:0,clientHeight:100,scrollTo({top}){this.scrollTop=top;}};
const bottom={current:false},seen={current:null};let scheduled=0,resize;
const globals={box:{current:el},inner:{current:{}},unread:true,focus:undefined,readVisit:{},retained:{current:[]},groups:[],from:0,prependAnchor:{current:null},seen,messages:[{id:'read'}],target:undefined,atBottom:bottom,items:[],first:undefined,ctx:{},flash:()=>false,whoWrote:()=>({mine:false}),ev:()=>false,setFresh(){},reduced:()=>true,useLayoutEffect:fn=>fn(),visibleRows:{current:()=>scheduled++}};
runInNewContext(stripTypeScriptTypes(source.slice(entryStart,entryEnd)),globals);assert.equal(bottom.current,true,'no-target unread entry follows bottom even before a scroll event');
const scrollStart=source.indexOf('  const onScroll ='),scrollEnd=source.indexOf('  const down =',scrollStart);
const onScroll=runInNewContext(stripTypeScriptTypes(source.slice(scrollStart,scrollEnd))+'\nonScroll',{...globals,setFar(){},fresh:0,earlier(){},reportSeen(){}});onScroll();assert.equal(scheduled,1,'existing scroll schedules current member evaluation');
const resizeStart=source.indexOf('  useEffect(() => {\n    const el = box.current, content = inner.current;'),resizeEnd=source.indexOf('\n  // While a snackbar',resizeStart);
runInNewContext(stripTypeScriptTypes(source.slice(resizeStart,resizeEnd)),{...globals,useEffect:fn=>fn(),ResizeObserver:class{constructor(fn){resize=fn;}observe(){}disconnect(){}}});el.scrollHeight=80;resize();assert.equal(el.scrollTop,80);assert.equal(scheduled,2,'existing content/box resize schedules evaluation');
console.log('PASS visible rows: sliver/half/tall/footer/resize, epoch/visit/unmount fences, unchanged-set coalescing, 2 member+1 root rect per frame with 2000 offscreen rows; actual entry/scroll/resize callbacks');
