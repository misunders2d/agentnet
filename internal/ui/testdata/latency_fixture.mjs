import {mount,unmount} from '/assets/skins/comic/entry.mjs';
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const frame=()=>new Promise(r=>requestAnimationFrame(r));
const assert=(v,m)=>{if(!v)throw Error(m);};
const until=async(f,label,ms=5000)=>{const end=performance.now()+ms;while(!f()){if(performance.now()>end)throw Error('Timed out: '+label);await pause(10);}};
const reports={};window.latencyReport=null;
try {
 const query=new URL(location.href).searchParams,token=query.get('token');
 if(token)await fetch('/?t='+encodeURIComponent(token));
 const read=async path=>{const r=await fetch(path);if(!r.ok)throw Error('Fixture read HTTP '+r.status);return r.json();};
 const overview=await read('/api/overview?topics=1'),view=await read('/api/dm?id='+overview.dms[0].id);
 // Generated UI data uses a protocol-shaped conversation ID for exact
 // notification routing; the toy provider's dm0001 is not a valid target.
 overview.dms[0].id=view.id='c'.repeat(64);
 overview.demo=false;overview.review=[];overview.needs_you=[];overview.quarantine=[];overview.held=[];overview.threads=[];overview.topics=[];
 const peers=Array.from({length:1000},(_,i)=>({person:'fixture-person-'+i,label:'Person '+i,address:'fixture-'+i+'/laptop',state:'pinned',devices:[{address:'fixture-'+i+'/laptop',name:'Laptop',human:true}]}));
 overview.people=[...overview.people,...peers];overview.directory={status:'listed',members:peers.map(p=>({address:p.address,presence:'connected',joined:1}))};
 view.messages=Array.from({length:400},(_,i)=>({id:(i+1).toString(16).padStart(32,'0'),lid:(i+1).toString(16).padStart(32,'0'),dir:i%2?'out':'in',from:i%2?overview.me.address:view.peer.address,kind:'message',body:'Retained message '+(i+1),state:'delivered',delivery:'delivered',origin:'ui',at:new Date(Date.now()-(400-i)*1000).toISOString(),can:[],actions:[],reactions:[],unread:false}));
 // One human send at the initial window boundary has two distinct targets.
 for(const i of [319,320])Object.assign(view.messages[i],{kind:'question',dir:'out',from:overview.me.address,body:'Grouped boundary request',send_group:'a'.repeat(32),send_group_author:'b'.repeat(64),pid:(i+1).toString(16).padStart(32,'0')});
 view.messages[399].quote=view.messages[0].id;
 let held,release,typing=false,seq=100;
 const counts={overview:0,dm:0,typing:0,catalogs:0},listeners=new Set();
 const emit=hint=>{seq++;for(const fn of listeners)fn({type:'change',seq,...(hint?{hint}:{})});};
 let openTarget;
 const host={version:1,platform:'daemon',workspace:{id:'latency',name:'Fixture',state:'enrolled'},skins:[],onOpen:fn=>{openTarget=fn;},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},
  async api(path,body){
   if(path.startsWith('/api/overview')){counts.overview++;const result=structuredClone({...overview,seq});if(held)await held;return result;}
   if(path.startsWith('/api/dm?')){counts.dm++;await pause(15);return structuredClone(view);}
   if(path.startsWith('/api/typing')){counts.typing++;return {scope:{conv:view.id},preferences:{send:false,show:true},supported:true,current:true,entries:typing?[{person:view.peer.person,address:view.peer.address,label:'Vitalii',expires:new Date(Date.now()+3000).toISOString()}]:[]};}
   if(path==='/api/dm/send'){
    await pause(25);view.messages.push({...body,lid:body.id,dir:'out',from:overview.me.address,kind:'message',state:'delivered',delivery:'delivered',origin:'ui',at:new Date().toISOString(),actions:[],can:[],reactions:[]});emit();return{id:body.id,lid:body.id,state:'queued'};
   }
   if(path==='/api/act'||path==='/api/refresh')return {};
   if(path.startsWith('/api/agents')){counts.catalogs++;return{agents:[]};}
   if(body!==undefined)return {};
   return read(path);
  }};
 const shadow=document.querySelector('#fixture').attachShadow({mode:'open'}),link=document.createElement('link');link.rel='stylesheet';link.href='/assets/skins/comic/style.css';shadow.append(link);
 const root=document.createElement('div');root.style.height='100%';shadow.append(root);await mount(root,host);
 await until(()=>[...root.querySelectorAll('button')].some(b=>b.textContent.includes('Vitalii')),'chat row');
 [...root.querySelectorAll('button')].find(b=>b.textContent.includes('Vitalii')).click();
 await until(()=>root.querySelectorAll('[data-part="message"]').length>0,'retained timeline');await pause(100);
 // A departing pane stays mounted but inert during the existing animation.
 const timeline=()=>[...root.querySelectorAll('[data-part="timeline"]')].find(el=>!el.closest('[inert]')),scroll=()=>timeline().parentElement;
 const findRow=id=>timeline()?.querySelector('[data-mid="'+id+'"]');
 const rows=()=>[...(timeline()?.querySelectorAll('[data-mid]')||[])];
 const groupCount=()=>timeline().querySelectorAll('[data-part="message"]:not([data-send-target]),[data-send-group]').length;
 reports.initialMounted=rows().length;reports.initialGroups=groupCount();
 assert(groupCount()<=80,'initial history must mount at most 80 complete groups');
 assert(timeline().querySelectorAll('[data-send-group] [data-send-target]').length===2,'window split one explicit send');
 assert(!findRow(view.messages[0].id),'old history mounted eagerly');
 // Ordinary backward scrolling prepends history without counting own old
 // rows as arrivals or moving the visible anchor.
 const scroller=scroll();scroller.scrollTop=40;
 const anchorRow=rows().find(r=>r.getBoundingClientRect().bottom>scroller.getBoundingClientRect().top),anchorTop=anchorRow.getBoundingClientRect().top;
 const initial=rows().length;scroller.dispatchEvent(new Event('scroll'));await until(()=>rows().length>initial,'older history on backward scroll');await frame();await frame();
 reports.prependDrift=Math.abs(anchorRow.getBoundingClientRect().top-anchorTop);
 assert(reports.prependDrift<2,'older reveal moved reading anchor');
 assert(![...root.querySelectorAll('button')].some(b=>/\d+ new/.test(b.textContent)),'old rows counted as incoming');
 // A genuine incoming row while reading history retains the same anchor.
 view.messages.push({...view.messages[2],id:'f'.repeat(32),lid:'f'.repeat(32),body:'Concurrent incoming row',at:new Date().toISOString()});emit();
 await until(()=>root.textContent.includes('1 new'),'true incoming row counted');await frame();await frame();
 reports.arrivalDrift=Math.abs(anchorRow.getBoundingClientRect().top-anchorTop);
 assert(reports.arrivalDrift<2,'incoming row moved older reading anchor');
 // Chromium native scroll anchoring preserves height changes above the viewport.
 const above=rows().find(r=>r.getBoundingClientRect().bottom<scroller.getBoundingClientRect().top);
 if(above){above.style.minHeight='240px';await frame();await frame();reports.heightDrift=Math.abs(anchorRow.getBoundingClientRect().top-anchorTop);assert(reports.heightDrift<2,'late row height moved anchor');}
 // Explicit prepend fallback must also work with native anchoring disabled.
 scroller.style.overflowAnchor='none';
 const fallbackRow=rows().find(r=>r.getBoundingClientRect().bottom>scroller.getBoundingClientRect().top),fallbackTop=fallbackRow.getBoundingClientRect().top,n=rows().length;
 [...root.querySelectorAll('button')].find(b=>b.textContent==='Earlier messages').click();await until(()=>rows().length>n,'prepend fallback without native anchoring');await frame();await frame();
 reports.prependFallbackDrift=Math.abs(fallbackRow.getBoundingClientRect().top-fallbackTop);assert(reports.prependFallbackDrift<2,'prepend fallback moved anchor');scroller.style.removeProperty('overflow-anchor');
 let reveals=2;
 while(rows().length<view.messages.length){const n=rows().length;[...root.querySelectorAll('button')].find(b=>b.textContent==='Earlier messages').click();await until(()=>rows().length>n,'all earlier history retrievable');await frame();reveals++;}
 assert(new Set(rows().map(r=>r.dataset.mid)).size===view.messages.length,'history gap or duplicate');reports.olderReveals=reveals;
 // A normal scroll must not measure every already revealed row.
 scroller.scrollTop=Math.max(100,scroller.scrollHeight-scroller.clientHeight-200);await frame();await frame();
 const rectMethod=Element.prototype.getBoundingClientRect;let scrollGeometryReads=0;
 Element.prototype.getBoundingClientRect=function(...args){scrollGeometryReads++;return rectMethod.apply(this,args);};
 try{for(let i=0;i<10;i++)scroller.dispatchEvent(new Event('scroll'));}finally{Element.prototype.getBoundingClientRect=rectMethod;}
 reports.scrollGeometryReads=scrollGeometryReads;
 // Reopen resets the local window. An exact notification expands before
 // scrolling, and Reply still resolves an older unmounted parent.
 openTarget('','channel');
 // Swap deliberately retains this exact pane (and its window) if reopened
 // during exit. Wait for retirement before asserting a fresh mount.
 await until(()=>!root.querySelector('[data-part="timeline"]'),'departed pane retired');
 [...root.querySelectorAll('button')].find(b=>b.textContent.includes('Vitalii')).click();await until(()=>rows().length>0,'reopen history');
 assert(groupCount()<=80,'reopen mounted all history');
 await openTarget(view.messages[200].id,'message',{conv:view.id});await until(()=>findRow(view.messages[200].id),'hidden notification target');await frame();await frame();
 const exact=findRow(view.messages[200].id),rect=exact.getBoundingClientRect(),bounds=scroll().getBoundingClientRect();
 assert(rect.bottom>bounds.top&&rect.top<bounds.bottom,'exact notification target not scrolled into view');
 assert(!findRow(view.messages[0].id),'notification unnecessarily mounted oldest history');
 const quote=[...findRow(view.messages[399].id).querySelectorAll('button')].find(b=>b.textContent.includes('Retained message 1'));
 assert(quote,'reply metadata lost its unmounted parent');quote.click();await until(()=>findRow(view.messages[0].id),'reply expands exact old parent');await frame();await frame();
 const parent=findRow(view.messages[0].id),parentRect=parent.getBoundingClientRect();assert(parentRect.bottom>scroll().getBoundingClientRect().top&&parentRect.top<scroll().getBoundingClientRect().bottom,'reply target not visible');
 reports.exactTargets=true;
 const expandedRows=rows().length;
 await openTarget(view.messages[350].id,'message',{conv:view.id});await frame();await frame();
 reports.monotonicTargets=!!findRow(view.messages[0].id)&&rows().length>=expandedRows;
 reports.targetRows={expanded:expandedRows,later:rows().length};
 assert(reports.scrollGeometryReads<=10&&reports.monotonicTargets,'ordinary scroll cost or later target collapsed revealed history');

 const before={...counts};typing=true;for(let i=0;i<1000;i++)emit('typing');
 await until(()=>root.querySelector('[data-part="typing"]')?.textContent.includes('is typing'),'typing pulse');
 assert(counts.dm===before.dm,'typing reloaded history');assert(counts.overview===before.overview,'typing reloaded overview');assert(counts.typing-before.typing<=2,'typing requests failed to coalesce');
 reports.typingBurst={events:1000,reads:counts.typing-before.typing,historyReads:counts.dm-before.dm};
 const previous=counts.dm;for(let i=0;i<1000;i++)emit('presence');await pause(80);assert(counts.dm===previous,'unrelated presence reloaded history');
 const bubble=root.querySelector('[data-mid="'+view.messages[0].id+'"]');
 held=new Promise(r=>release=r);
 view.messages[0].body='Edited while overview blocked';view.messages[0].reactions=[{emoji:'👍',by:[{id:'fixture-alice',label:'Alice'}]}];
 let start=performance.now();emit();
 await until(()=>bubble.textContent.includes('Edited while overview blocked')&&bubble.textContent.includes('👍'),'edit/reaction before overview completed',3000);
 reports.activeEditMs=Math.round(performance.now()-start);assert(held,'overview was not held');
 assert(root.querySelector('[data-mid="'+view.messages[0].id+'"]')===bubble,'unchanged bubble node lost');
 const box=root.querySelector('textarea'),setValue=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;
 start=performance.now();setValue.call(box,'Immediate local preview');box.dispatchEvent(new Event('input',{bubbles:true}));await frame();
 reports.typingPaintMs=Math.round(performance.now()-start);
 const send=[...root.querySelectorAll('button')].find(b=>/^Send/.test(b.getAttribute('aria-label')||''));assert(send,'send control missing');
 start=performance.now();send.click();await until(()=>root.textContent.includes('Immediate local preview')&&box.value==='','optimistic feedback',2000);reports.optimisticMs=Math.round(performance.now()-start);
 await until(()=>view.messages.length===402,'local storage');
 await until(()=>[...root.querySelectorAll('[data-part="message"]')].some(m=>m.textContent.includes('Immediate local preview')&&m.textContent.includes('Delivered')),'durable delivery while overview blocked',3000);
 reports.storedDisplayMs=Math.round(performance.now()-start);
 held=null;release();await pause(100);
 // A changed reaction/body must survive object sharing; a later unchanged
 // refresh must keep the same element and never erase the user's draft.
 setValue.call(box,'Keep my draft');box.dispatchEvent(new Event('input',{bubbles:true}));emit();await pause(80);assert(box.value==='Keep my draft','refresh erased draft');assert(bubble.textContent.includes('Edited while overview blocked'),'edit lost');
 const gaps=[];let last=performance.now();for(let i=0;i<40;i++){emit('typing');await frame();const now=performance.now();gaps.push(now-last);last=now;}
 gaps.sort((a,b)=>a-b);reports.frameP95Ms=Math.round(gaps[Math.floor(gaps.length*.95)]*10)/10;
 reports.people=1000;reports.retainedMessages=400;reports.counts=counts;
 window.latencyReport={ok:true,...reports};
}catch(e){window.latencyReport={ok:false,error:String(e.stack||e),...reports};}
fetch('/latency-report?run='+encodeURIComponent(new URL(location.href).searchParams.get('run')||''),{method:'POST',body:JSON.stringify(window.latencyReport)}).catch(()=>{});
