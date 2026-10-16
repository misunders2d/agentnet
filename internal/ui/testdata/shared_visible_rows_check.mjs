// Same deterministic membership/geometry contracts as visible_rows_check.mjs.
import assert from 'node:assert/strict';
import {watchVisibleRows} from '../skins/shared/visible-rows.mjs';

class Events {
 listeners=new Map();
 addEventListener(k,f){const set=this.listeners.get(k)||new Set();set.add(f);this.listeners.set(k,set);}
 removeEventListener(k,f){this.listeners.get(k)?.delete(f);}
 fire(k){for(const f of this.listeners.get(k)||[])f();}
}
const window=new Events(),document=new Events(),observers=[],reports=[],frames=new Map();
let focused=true,allowed=true,reserve=0,frameID=0,rects=0,rootRects=0,rootHeight=1000;
document.defaultView=window;document.visibilityState='visible';document.hasFocus=()=>focused;
window.requestAnimationFrame=function(fn){assert.equal(this,window);frames.set(++frameID,fn);return frameID;};
window.cancelAnimationFrame=function(id){assert.equal(this,window);frames.delete(id);};
const flush=()=>{const batch=[...frames.values()];frames.clear();batch.forEach(fn=>fn());};
window.IntersectionObserver=class {
 rows=new Set();
 constructor(callback,options){this.callback=callback;this.options=options;observers.push(this);}
 observe(row){this.rows.add(row);}
 unobserve(row){this.rows.delete(row);}
 disconnect(){this.disconnected=true;this.rows.clear();}
 emit(row,show=true){this.callback([{target:row,isIntersecting:show,intersectionRect:{width:show?50:0,height:show?1:0}}]);}
};
const rectangle=(top,height,left=0,width=100)=>({top,bottom:top+height,left,right:left+width,width,height});
const row=(id,top,height)=>({dataset:{mid:id},rect:rectangle(top,height),getBoundingClientRect(){rects++;return this.rect;}});
const ids=new Set(['short','tall',...Array.from({length:2000},(_,i)=>'off'+i)]);
const rows=[row('short',999,100),row('tall',999,5000),...Array.from(ids).slice(2).map(id=>row(id,10000,100))];
const descendants=new Set(rows),root={ownerDocument:document,isConnected:true,contains:node=>descendants.has(node),getBoundingClientRect(){rootRects++;return rectangle(0,rootHeight);}};
const ambient=['window','document','IntersectionObserver','requestAnimationFrame','cancelAnimationFrame'];
const saved=ambient.map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]);
for(const k of ambient)Object.defineProperty(globalThis,k,{configurable:true,get(){throw Error('ambient '+k+' used instead of supplied root scope');}});
try {
 const watch=watchVisibleRows(root,{onChange:set=>reports.push(set),reserve:()=>reserve,allowed:()=>allowed});
 const foreign=row('foreign',0,100),outside=row('short',0,100);descendants.add(foreign);
 watch.update([...rows,foreign,outside],ids);flush();
 const visible=()=>[...(reports.at(-1)||[])];
 assert.equal(observers.length,1);const observer=observers[0];assert.equal(observer.options.root,root);assert.equal(observer.options.threshold,0);
 assert.deepEqual([...observer.rows],rows,'only mounted canonical rows inside the supplied root observed');
 observer.emit(rows[0]);flush();assert.deepEqual(visible(),[],'one-pixel short sliver is not seen');
 rows[0].rect=rectangle(950,100);watch.evaluate();watch.evaluate();assert.equal(frames.size,1);flush();assert.deepEqual(visible(),['short'],'half short row is seen');
 const unchanged=reports.length;watch.evaluate();flush();assert.equal(reports.length,unchanged);
 const external=reports.at(-1);external.add('consumer-only');watch.evaluate();flush();assert.equal(reports.length,unchanged,'consumer cannot mutate the internal reported set');external.delete('consumer-only');
 observer.emit(rows[1]);flush();assert.deepEqual(visible(),['short'],'five-viewport row sliver is not seen');
 rows[1].rect=rectangle(500,5000);watch.evaluate();flush();assert.deepEqual(visible(),['short','tall'],'half viewport of tall row is seen without another IO callback');
 reserve=200;watch.evaluate();flush();assert.equal(observers.length,1,'footer update retains the membership observer');assert.deepEqual(visible(),[]);
 rows[0].rect=rectangle(750,100);watch.evaluate();flush();assert.deepEqual(visible(),['short']);
 rows[0].rect=rectangle(800,100);watch.evaluate();flush();assert.deepEqual(visible(),[],'footer covered row stays unseen');
 rows[0].rect=rectangle(760,200);watch.evaluate();flush();assert.deepEqual(visible(),[]);
 rootHeight=1100;watch.evaluate();flush();assert.deepEqual(visible(),['short'],'resize alone updates geometry');
 rootHeight=1000;watch.evaluate();flush();assert.deepEqual(visible(),[]);
 rows[0].rect=rectangle(760,60);watch.evaluate();flush();assert.deepEqual(visible(),['short']);
 rows[0].rect=rectangle(700,100,200,100);watch.evaluate();flush();assert.deepEqual(visible(),[],'horizontal overlap required');
 rows[0].rect=rectangle(700,0);watch.evaluate();flush();assert.deepEqual(visible(),[],'positive row height required');
 rects=0;rootRects=0;for(let i=0;i<100;i++)watch.evaluate();assert.equal(frames.size,1);flush();assert.equal(rootRects,1);assert.equal(rects,2,'2000 offscreen rows are never measured per frame');
 rows[0].rect=rectangle(700,100);watch.evaluate();const stale=[...frames.values()][0];focused=false;window.fire('blur');assert.equal(frames.size,0);const paused=reports.length;stale();observer.emit(rows[0]);flush();assert.equal(reports.length,paused,'focus invalidates pending and late work');
 focused=true;document.visibilityState='hidden';window.fire('focus');assert.equal(frames.size,0);
 document.visibilityState='visible';document.fire('visibilitychange');flush();assert.deepEqual(visible(),['short']);
 allowed=false;watch.evaluate();assert.deepEqual(visible(),[]);const blocked=reports.length;observer.emit(rows[1]);flush();assert.equal(reports.length,blocked,'current host/visit gate fences membership callbacks');
 allowed=true;watch.evaluate();flush();assert.deepEqual(visible(),['short']);
 const next=row('next',700,100);descendants.add(next);watch.update([next],['next']);observer.emit(rows[0]);flush();assert.deepEqual(visible(),[],'removed row callback cannot restore old canonical IDs');
 assert.deepEqual([...observer.rows],[next]);observer.emit(next);flush();assert.deepEqual(visible(),['next']);
 next.dataset.mid='replacement';watch.evaluate();flush();assert.deepEqual(visible(),[],'reused node with a different ID needs explicit canonical update');
 watch.update([next],['replacement']);observer.emit(next);flush();assert.deepEqual(visible(),['replacement']);
 root.isConnected=false;watch.evaluate();assert.deepEqual(visible(),[]);root.isConnected=true;watch.evaluate();flush();assert.deepEqual(visible(),['replacement']);
 watch.evaluate();const late=[...frames.values()][0];watch.destroy();assert.equal(frames.size,0);assert.equal(observer.disconnected,true);assert.deepEqual(visible(),[]);
 const done=reports.length;late();observer.emit(next);watch.update([next],['replacement']);watch.evaluate();window.fire('focus');document.fire('visibilitychange');flush();assert.equal(reports.length,done,'destroy fences all late callbacks and calls');
 watch.destroy();assert.equal(observers.length,1);assert.equal([...window.listeners.values(),...document.listeners.values()].reduce((n,s)=>n+s.size,0),0);

 // The suite above omits reserveTop: all original default-zero geometry remains covered.
 let topInset=100,topReads=0,bottomReads=0;const insetReports=[];
 rootHeight=1000;const short=row('inset-short',0,80),tall=row('inset-tall',900,5000);
 descendants.add(short);descendants.add(tall);
 const insetWatch=watchVisibleRows(root,{onChange:set=>insetReports.push(set),reserveTop:()=>{topReads++;return topInset;},reserve:()=>{bottomReads++;return 200;}});
 insetWatch.update([short,tall],['inset-short','inset-tall']);const insetObserver=observers[1];
 insetObserver.emit(short);insetObserver.emit(tall);flush();
 const insetVisible=()=>[...(insetReports.at(-1)||[])];
 assert.deepEqual(insetVisible(),[],'a row wholly covered by the sticky header stays unseen');
 short.rect=rectangle(60,80);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),['inset-short'],'half a short row below the header is seen');
 short.rect=rectangle(59,80);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),[],'less than half a short row below the header stays unseen');
 tall.rect=rectangle(450,5000);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),['inset-tall'],'tall row threshold uses half the effective 700px viewport');
 tall.rect=rectangle(451,5000);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),[],'less than half the effective viewport stays unseen');
 short.rect=rectangle(60,80);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),['inset-short']);
 topInset=200;insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),['inset-tall'],'live cached header height changes clipping and the effective tall-row threshold');
 topInset=1001;insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),[],'a header covering the viewport leaves no visible area');
 topInset=-5;tall.rect=rectangle(900,5000);short.rect=rectangle(0,80);insetWatch.evaluate();flush();assert.deepEqual(insetVisible(),['inset-short'],'negative top reserve clamps to the default zero inset');
 rects=0;rootRects=0;topReads=0;bottomReads=0;for(let i=0;i<100;i++)insetWatch.evaluate();assert.equal(frames.size,1);flush();
 assert.equal(rects,2);assert.equal(rootRects,1);assert.equal(topReads,1);assert.equal(bottomReads,1);assert.equal(observers.length,2,'live inset changes retain the same membership observer');
 insetWatch.destroy();assert.equal(insetObserver.disconnected,true);assert.equal([...window.listeners.values(),...document.listeners.values()].reduce((n,s)=>n+s.size,0),0);
} finally {
 for(const [k,descriptor] of saved)if(descriptor)Object.defineProperty(globalThis,k,descriptor);else delete globalThis[k];
}
console.log('PASS shared visible rows: default-zero and sticky-header clipping, short/tall half-effective-viewport thresholds, canonical membership, scoped IO, footer/resize/focus/host/visit/cleanup fences, coalescing and 2 member+1 root cost with 2000 offscreen rows');
