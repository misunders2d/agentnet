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
 overview.demo=false;overview.review=[];overview.needs_you=[];overview.quarantine=[];overview.held=[];overview.threads=[];overview.topics=[];
 const peers=Array.from({length:1000},(_,i)=>({person:'fixture-person-'+i,label:'Person '+i,address:'fixture-'+i+'/laptop',state:'pinned',devices:[{address:'fixture-'+i+'/laptop',name:'Laptop',human:true}]}));
 overview.people=[...overview.people,...peers];overview.directory={status:'listed',members:peers.map(p=>({address:p.address,presence:'connected',joined:1}))};
 view.messages=Array.from({length:400},(_,i)=>({id:(i+1).toString(16).padStart(32,'0'),lid:(i+1).toString(16).padStart(32,'0'),dir:i%2?'out':'in',from:i%2?overview.me.address:view.peer.address,kind:'message',body:'Retained message '+(i+1),state:'delivered',delivery:'delivered',origin:'ui',at:new Date(Date.now()-(400-i)*1000).toISOString(),can:[],actions:[],reactions:[],unread:false}));
 let held,release,typing=false,seq=100;
 const counts={overview:0,dm:0,typing:0,catalogs:0},listeners=new Set();
 const emit=hint=>{seq++;for(const fn of listeners)fn({type:'change',seq,...(hint?{hint}:{})});};
 const host={version:1,platform:'daemon',workspace:{id:'latency',name:'Fixture',state:'enrolled'},skins:[],onOpen:()=>{},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},
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
 await until(()=>root.querySelectorAll('[data-part="message"]').length===400,'retained timeline');await pause(100);
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
 await until(()=>view.messages.length===401,'local storage');
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
