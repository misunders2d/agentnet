import {mount,unmount} from '/assets/skins/comic/entry.mjs';
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const frame=()=>new Promise(r=>requestAnimationFrame(r));
const assert=(v,m)=>{if(!v)throw Error(m);};
const until=async(f,label,ms=5000)=>{const end=performance.now()+ms;while(!f()){if(performance.now()>end)throw Error('Timed out: '+label);await pause(10);}};
const reports={actions:[],apiCalls:[],seenControls:[],pageErrors:[],inputDispatch:'synthetic DOM events (excludes OS input dispatch)',viewport:{width:innerWidth,height:innerHeight,dpr:devicePixelRatio},userAgent:navigator.userAgent,hardwareConcurrency:navigator.hardwareConcurrency,nativeScrollAnchoring:CSS.supports('overflow-anchor','auto')};window.latencyReport=null;
const pageError=e=>reports.pageErrors.push(String(e.message||e.reason||e).slice(0,1000));window.addEventListener('error',pageError);window.addEventListener('unhandledrejection',pageError);
try {
 const query=new URL(location.href).searchParams,token=query.get('token');
 const suite=window.__agentnetLatencySuite||query.get('suite')||'history';reports.suite=suite;assert(['history','navigation','settings','unread','actions'].includes(suite),'Unknown timing workload');
 if(token)await fetch('/?t='+encodeURIComponent(token));
 const read=async path=>{const r=await fetch(path);if(!r.ok)throw Error('Fixture read HTTP '+r.status);return r.json();};
 const seed=window.__agentnetLatencyInput||await read('/latency-input.json');
 const asset=await(await fetch('/assets/skins/comic/entry.mjs')).arrayBuffer();reports.entrySHA256=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',asset))).map(b=>b.toString(16).padStart(2,'0')).join('');
 const overview=structuredClone(seed.overview),view=structuredClone(seed.view);
 // Generated UI data uses a protocol-shaped conversation ID for exact
 // notification routing; the toy provider's dm0001 is not a valid target.
 overview.dms[0].id=view.id='c'.repeat(64);
 overview.demo=false;overview.review=[];overview.needs_you=[];overview.quarantine=[];overview.held=[];overview.threads=[];overview.topics=[];
 const peopleCount=Number(query.get('people')||1000);assert(Number.isInteger(peopleCount)&&peopleCount>=1&&peopleCount<=1000,'Invalid bounded fixture roster');
 const peers=Array.from({length:peopleCount},(_,i)=>({person:'fixture-person-'+i,label:'Person '+i,address:'fixture-'+i+'/laptop',state:'pinned',devices:[{address:'fixture-'+i+'/laptop',name:'Laptop',human:true}]}));
 overview.people=[...overview.people,...peers];overview.directory={status:'listed',members:peers.map(p=>({address:p.address,presence:'connected',joined:1}))};
 view.messages=Array.from({length:400},(_,i)=>({id:(i+1).toString(16).padStart(32,'0'),lid:(i+1).toString(16).padStart(32,'0'),dir:i%2?'out':'in',from:i%2?overview.me.address:view.peer.address,kind:'message',body:'Retained message '+(i+1),state:'delivered',delivery:'delivered',origin:'ui',at:new Date(Date.now()-(400-i)*1000).toISOString(),can:[],actions:[],reactions:[],unread:false}));
 // One human send at the initial window boundary has two distinct targets.
 for(const i of [375,376])Object.assign(view.messages[i],{kind:'question',dir:'out',from:overview.me.address,body:'Grouped boundary request',send_group:'a'.repeat(32),send_group_author:'b'.repeat(64),pid:(i+1).toString(16).padStart(32,'0')});
 view.messages[399].quote=view.messages[0].id;
 if(suite==='unread'){for(const i of [0,340,350]){view.messages[i].unread=true;view.messages[i].dir='in';view.messages[i].from=view.peer.address;}overview.dms[0].unread=3;view.main_topic={unread:3};}
 if(suite==='actions')view.messages.at(-1).can=['react','edit','delete'];
 overview.remind=true;overview.reminders=[];overview.schedules=true;
 let held,release,typing=false,seq=100,reactionFailure=false;
 let typingPreferences={send:false,show:true};
 const counts={overview:0,dm:0,typing:0,catalogs:0},listeners=new Set();
 const emit=hint=>{seq++;for(const fn of listeners)fn({type:'change',seq,...(hint?{hint}:{})});};
 let openTarget;
 const host={version:1,platform:window.__agentnetLatencyPlatform||query.get('platform')||'daemon',workspace:{id:'latency',name:'Fixture',state:'enrolled'},skins:[{id:'comic',name:'Comic',api:1}],onSkinsChange:()=>()=>{},onOpen:fn=>{openTarget=fn;},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},
  async api(path,body){
   if(path.startsWith('/api/overview')){counts.overview++;const result=structuredClone({...overview,seq});if(held)await held;return result;}
   if(path.startsWith('/api/dm?')){counts.dm++;await pause(15);return structuredClone(view);}
   if(path.startsWith('/api/typing/preferences')){await pause(25);typingPreferences={...body};return typingPreferences;}
   if(path.startsWith('/api/typing')){counts.typing++;return {scope:{conv:view.id},preferences:typingPreferences,supported:true,current:true,entries:typing?[{person:view.peer.person,address:view.peer.address,label:'Vitalii',expires:new Date(Date.now()+3000).toISOString()}]:[]};}
   if(path==='/api/dm/send'){
    await pause(25);view.messages.push({...body,lid:body.id,dir:'out',from:overview.me.address,kind:'message',state:'delivered',delivery:'delivered',origin:'ui',at:new Date().toISOString(),actions:[],can:[],reactions:[]});emit();return{id:body.id,lid:body.id,state:'queued'};
   }
   if(path==='/api/message/react'){await pause(150);if(reactionFailure)throw Error('Synthetic reaction unavailable. Try again.');const m=view.messages.find(m=>m.id===body.id);assert(m,'wrong reaction ID');m.reactions=body.remove?[]:[{emoji:body.emoji,mine:true,by:[{id:overview.person.person,label:'Alice'}]}];emit();return{note:'Stored'};}
   if(path==='/api/message/edit'){await pause(25);const m=view.messages.find(m=>m.id===body.id);assert(m,'wrong edit ID');m.body=body.text;emit();return{note:'Stored'};}
   if(path==='/api/message/delete'){await pause(25);const m=view.messages.find(m=>m.id===body.id);assert(m,'wrong delete ID');m.deleted=true;m.body='';emit();return{note:'Stored'};}
   if(path==='/api/storage/retry_send'){await pause(100);assert(body.conv===view.id&&body.id===view.messages.at(-1).id,'retry changed exact target');Object.assign(view.messages.at(-1),{state:'queued',delivery:'queued',state_text:'Queued',detail:''});emit();return{id:body.id,retried:1};}
   if(path==='/api/act'){
    if(body?.do==='read'){for(const m of view.messages)if(body.ids?.includes(m.id))m.unread=false;overview.dms[0].unread=view.messages.filter(m=>m.unread).length;view.main_topic={unread:overview.dms[0].unread};emit();}
    return {};
   }
   if(path==='/api/refresh')return {};
   if(path.startsWith('/api/agents')){counts.catalogs++;return{host:overview.me.address,local:true,agents:[],sessions:[]};}
   if(path==='/api/responder')return{manual:true,harness:'',dir:'',available:[],setup:{tools:[]}};
   if(path.startsWith('/api/grants'))return{grants:[]};
   if(path.startsWith('/api/teams'))return{teams:[],current:true,status:'available'};
   if(path.startsWith('/api/schedules'))return[];
   if(path.startsWith('/api/storage'))throw Error('Synthetic storage unavailable; Retry is safe.');
   if(path.includes('/drive')||path.includes('/google'))return{status:'unavailable',spaces:[]};
   if(body!==undefined)return {};
   return {};
  }};
 const originalAPI=host.api;
 host.api=async(path,body)=>{
  const call={path,method:body===undefined?'read':'write',start:performance.now()};reports.apiCalls.push(call);
  try{const result=await originalAPI(path,body);call.result=performance.now();call.ok=true;return result;}
  catch(e){call.result=performance.now();call.ok=false;throw e;}
 };
 const shadow=document.querySelector('#fixture').attachShadow({mode:'open'}),link=document.createElement('link');link.rel='stylesheet';link.href='/assets/skins/comic/style.css';shadow.append(link);
 const root=document.createElement('div');root.style.height='100%';shadow.append(root);await mount(root,host);
 window.__agentnetLatencyDispose=()=>{window.removeEventListener('error',pageError);window.removeEventListener('unhandledrejection',pageError);unmount(root);};
 const scope=()=>shadow;
 const shown=el=>{if(!el||el.closest('[inert]')||el.getAttribute('aria-hidden')==='true'||!el.getClientRects().length)return false;for(let p=el;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(s.visibility==='hidden'||s.display==='none'||s.opacity==='0')return false;}return true;};
 const onScreen=el=>{if(!shown(el))return false;const r=el.getBoundingClientRect();return r.bottom>0&&r.top<innerHeight&&r.right>0&&r.left<innerWidth;};
 const label=el=>(el.getAttribute('aria-label')||el.textContent||'').replace(/\s+/g,' ').trim();
 const buttons=()=>[...scope().querySelectorAll('button,[role="button"]')].filter(shown);
 const button=name=>buttons().find(b=>typeof name==='string'?label(b)===name:name.test(label(b)));
 const visibleText=text=>[...scope().querySelectorAll('h1,h2,h3,p,[role="heading"],[role="dialog"],[role="alert"],[role="status"]')].some(el=>onScreen(el)&&el.textContent.includes(text));
 const census=()=>{for(const el of [...scope().querySelectorAll('button,input,textarea,select,[role="switch"],[role="radio"],[role="menuitem"]')].filter(shown)){const key=el.tagName.toLowerCase()+':'+(el.getAttribute('type')||el.getAttribute('role')||'')+':'+label(el);if(!reports.seenControls.includes(key))reports.seenControls.push(key);}};
 const measure=async(id,action,expected,description,complete=expected)=>{
  window.latencyProgress={active:id,reports};census();assert(!expected(),id+' expected visible state already true before input');const record={id,expected:description,input:'synthetic DOM',start:performance.now(),apiStart:reports.apiCalls.length},gaps=[];performance.mark('latency:'+id+':input');let running=true,last=record.start;
  const watch=()=>{if(!running)return;const now=performance.now();gaps.push(now-last);last=now;requestAnimationFrame(watch);};requestAnimationFrame(watch);
  try{action();await until(expected,id+' visible feedback',3000);record.feedbackMs=performance.now()-record.start;performance.mark('latency:'+id+':feedback');
   await until(complete,id+' completed visible state',3000);record.visibleMs=performance.now()-record.start;await frame();await frame();assert(complete(),id+' state lost before paint opportunity');record.paintMs=performance.now()-record.start;
   record.api=reports.apiCalls.slice(record.apiStart).map(c=>({path:c.path,method:c.method,startMs:c.start-record.start,resultMs:c.result===undefined?null:c.result-record.start,ok:c.ok??null}));
   const writes=record.api.filter(c=>c.method==='write'&&c.resultMs!==null&&c.ok===true),failed=record.api.filter(c=>c.method==='write'&&c.resultMs!==null&&c.ok===false),projections=record.api.filter(c=>c.path.startsWith('/api/dm?')&&c.resultMs!==null&&c.ok===true);
   record.durableResultMs=writes.length?Math.max(...writes.map(c=>c.resultMs)):null;record.durableSource=writes.length?'synthetic host API result (not database persistence)':null;
   record.failedResultMs=failed.length?Math.max(...failed.map(c=>c.resultMs)):null;
   record.projectionResultMs=projections.length?Math.max(...projections.map(c=>c.resultMs)):null;
   record.frameMaxMs=Math.max(0,...gaps);record.frameP95Ms=gaps.sort((a,b)=>a-b)[Math.min(gaps.length-1,Math.floor(gaps.length*.95))]||0;
   assert(record.feedbackMs<2000,id+' feedback exceeds existing optimistic-feedback bound');record.ok=true;census();
  }finally{running=false;delete record.apiStart;reports.actions.push(record);}
 };
 const initialChat=()=>[...root.querySelectorAll('[data-part="chat-row"]')].find(b=>b.textContent.includes('Vitalii')&&onScreen(b));
 await until(initialChat,'visible initial chat row');await frame();await frame();assert(initialChat(),'initial chat control disappeared before input');reports.initialControl={visible:true,paintOpportunityFrames:2,readyAtMs:performance.now(),fonts:document.fonts.status};
 if(suite==='actions'){
  const message=view.messages.at(-1),row=()=>root.querySelector('[data-mid="'+message.id+'"]'),pending=()=>row()?.querySelector('[data-reaction-pending="true"]');
  await measure('navigation.actions-dm',()=>[...root.querySelectorAll('[data-part="chat-row"]')].find(b=>b.textContent.includes('Vitalii')).click(),()=>onScreen(root.querySelector('section[aria-busy="true"]'))||onScreen(row()),'opening feedback or exact action target visible',()=>onScreen(row()));
  const item=name=>[...scope().querySelectorAll('button,[role="menuitem"]')].find(b=>shown(b)&&!b.closest('[data-ending-style]')&&label(b)===name);
  const settled=()=>until(()=>!scope().querySelector('[data-ending-style]'),'previous popup retired');
  let menus=0;const menu=async()=>{await settled();row().querySelector('[data-message-bubble]').dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'}));await until(()=>row().querySelector('button[aria-label="More actions"],button[aria-label="Message actions"]'),'message action control');const more=row().querySelector('button[aria-label="More actions"],button[aria-label="Message actions"]');more.focus();await measure('message.menu-open-'+menus++,()=>more.click(),()=>!!item('Details'),'message action menu visible');};
  await menu();const pick=item('React 👍');if(!pick){const react=row().querySelector('button[aria-label="React"]');assert(react,'reaction control missing');const details=item('Details');details.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));await settled();await measure('reaction.picker-open',()=>react.click(),()=>!!item('React 👍'),'quick reaction choices visible');}
  await measure('reaction.add',()=>item('React 👍').click(),()=>!!pending(),'explicit pending reaction feedback',()=>message.reactions.some(r=>r.mine)&&!pending()&&!!row().querySelector('[aria-pressed="true"]'));await settled();
  await measure('reaction.remove',()=>row().querySelector('[aria-pressed="true"]').click(),()=>!!pending(),'explicit removal pending feedback',()=>!message.reactions.some(r=>r.mine)&&!pending()&&!row().querySelector('[aria-pressed="true"]'));
  reactionFailure=true;await menu();if(!item('React 👍')){item('Details').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));await settled();row().querySelector('button[aria-label="React"]').click();await until(()=>item('React 👍'),'quick reactions');}
  await measure('reaction.error',()=>item('React 👍').click(),()=>!!pending(),'pending feedback before actual failure',()=>visibleText('Synthetic reaction unavailable. Try again.')&&!pending()&&!row().querySelector('[aria-pressed="true"]'));reactionFailure=false;await settled();
  await menu();await measure('message.edit-open',()=>item('Edit').click(),()=>onScreen(row().querySelector('textarea[aria-label="New text"]')),'inline editor visible');await settled();const edit=row().querySelector('textarea');const set=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;
  await measure('message.edit-text',()=>{set.call(edit,'Edited exact action target');edit.dispatchEvent(new Event('input',{bubbles:true}));},()=>edit.value==='Edited exact action target','edited text visible');await measure('message.edit-save',()=>[...row().querySelectorAll('button')].find(b=>label(b)==='Save').click(),()=>!row().querySelector('textarea')&&row().textContent.includes('Edited exact action target'),'authoritative edited text replaces editor',()=>message.body==='Edited exact action target'&&!row().querySelector('textarea'));
  await menu();await measure('message.details-open',()=>item('Details').click(),()=>visibleText('Message details'),'exact message details visible');await settled();const detail=[...scope().querySelectorAll('[role="dialog"]')].find(d=>d.textContent.includes('Message details'));assert(detail.textContent.includes(message.id),'details wrong ID');await measure('message.details-close',()=>detail.querySelector('button[aria-label="Close"]').click(),()=>detail.hasAttribute('data-ending-style')||!detail.isConnected,'details closing feedback',()=>!detail.isConnected);
  for(const admin of [false,true]){overview.role=admin?'admin':'member';Object.assign(message,{state:'failed',delivery:'failed',detail:'send_error:relay_quota: Synthetic producer reason.',state_text:'Not sent'});emit();await until(()=>row().querySelector('[data-send-error="relay_quota"]'),'producer error projection');const error=row().querySelector('[data-send-error="relay_quota"]');assert(!![...error.querySelectorAll('button')].find(b=>label(b)==='Open Storage')===admin,'admin/member storage action mismatch');await measure('retry.exact-'+(admin?'admin':'member'),()=>[...error.querySelectorAll('button')].find(b=>label(b)==='Retry this message').click(),()=>!!error.querySelector('button:disabled'),'retry visibly pending',()=>!row().querySelector('[data-send-error]')&&message.state==='queued');}
  await menu();await measure('message.delete-confirm',()=>item('Delete…').click(),()=>visibleText('Delete this message?'),'delete requires concrete confirmation');await settled();const confirm=[...scope().querySelectorAll('[role="alertdialog"]')].find(shown);await measure('message.delete-cancel',()=>[...confirm.querySelectorAll('button')].find(b=>label(b)==='Keep it').click(),()=>confirm.hasAttribute('data-ending-style')||!confirm.isConnected,'cancel starts closing',()=>!confirm.isConnected);assert(!message.deleted,'cancel deleted message');await menu();item('Delete…').click();await until(()=>visibleText('Delete this message?'),'second explicit delete confirmation');const deleting=[...scope().querySelectorAll('[role="alertdialog"]')].find(shown);await measure('message.delete',()=>item('Delete').click(),()=>deleting.hasAttribute('data-ending-style')||!deleting.isConnected,'explicit delete starts closing confirmation',()=>!!message.deleted&&row().textContent.includes('Message deleted'));
 }
 if(suite==='history'){
 const messageReady=()=>[...root.querySelectorAll('[data-part="message"]')].some(onScreen);
 await measure('navigation.open-dm',()=>[...root.querySelectorAll('button')].find(b=>b.textContent.includes('Vitalii')).click(),()=>onScreen(root.querySelector('section[aria-busy="true"]'))||messageReady(),'opening progress or retained rows visible',messageReady);await pause(100);
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
 const initial=rows().length;await measure('history.scroll-prepend',()=>scroller.dispatchEvent(new Event('scroll')),()=>rows().length>initial,'older message rows appear');
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
 if(above&&!reports.nativeScrollAnchoring){
  const chosen=scroller.scrollTop+100;scroller.scrollTop=chosen;above.style.minHeight='280px';await frame();await frame();
  reports.userScrollRaceDrift=Math.abs(scroller.scrollTop-chosen);assert(reports.userScrollRaceDrift<2,'resize fallback fought a new user scroll');
  const row=rows().find(r=>r.getBoundingClientRect().bottom>scroller.getBoundingClientRect().top),top=row.getBoundingClientRect().top;
  above.style.minHeight='320px';await frame();await frame();reports.newReadingAnchorDrift=Math.abs(row.getBoundingClientRect().top-top);assert(reports.newReadingAnchorDrift<2,'resize fallback retained the old reading anchor');
 }
 // Explicit prepend fallback must also work with native anchoring disabled.
 scroller.style.overflowAnchor='none';
 const fallbackRow=rows().find(r=>r.getBoundingClientRect().bottom>scroller.getBoundingClientRect().top),fallbackTop=fallbackRow.getBoundingClientRect().top,n=rows().length;
 await measure('history.earlier-without-native-anchoring',()=>[...root.querySelectorAll('button')].find(b=>b.textContent==='Earlier messages').click(),()=>rows().length>n,'older rows appear without native scroll anchoring');
 reports.prependFallbackDrift=Math.abs(fallbackRow.getBoundingClientRect().top-fallbackTop);assert(reports.prependFallbackDrift<2,'prepend fallback moved anchor');scroller.style.removeProperty('overflow-anchor');
 let reveals=2;
 while(rows().length<view.messages.length){const n=rows().length;await measure('history.earlier-'+reveals,()=>[...root.querySelectorAll('button')].find(b=>b.textContent==='Earlier messages').click(),()=>rows().length>n,'additional retained rows appear');reveals++;}
 assert(new Set(rows().map(r=>r.dataset.mid)).size===view.messages.length,'history gap or duplicate');reports.olderReveals=reveals;
 // A normal scroll must not measure every already revealed row.
 scroller.scrollTop=Math.max(100,scroller.scrollHeight-scroller.clientHeight-200);await frame();await frame();
 const rectMethod=Element.prototype.getBoundingClientRect;let scrollGeometryReads=0;
 Element.prototype.getBoundingClientRect=function(...args){scrollGeometryReads++;return rectMethod.apply(this,args);};
 try{for(let i=0;i<10;i++)scroller.dispatchEvent(new Event('scroll'));}finally{Element.prototype.getBoundingClientRect=rectMethod;}
 reports.scrollGeometryReads=scrollGeometryReads;
 scroller.scrollTop=scroller.scrollHeight;scroller.dispatchEvent(new Event('scroll'));await frame();await frame();
 if(above)above.style.minHeight='400px';await frame();await frame();reports.bottomResizeGap=Math.abs(scroller.scrollHeight-scroller.clientHeight-scroller.scrollTop);assert(reports.bottomResizeGap<2,'late height change lost bottom pin');
 // Reopen resets the local window. An exact notification expands before
 // scrolling, and Reply still resolves an older unmounted parent.
 openTarget('','channel');
 // Swap deliberately retains this exact pane (and its window) if reopened
 // during exit. Wait for retirement before asserting a fresh mount.
 await until(()=>!root.querySelector('[data-part="timeline"]'),'departed pane retired');
 [...root.querySelectorAll('button')].find(b=>b.textContent.includes('Vitalii')).click();await until(()=>rows().length>0,'reopen history');
 assert(groupCount()<=80,'reopen mounted all history');
 await measure('history.notification-exact-target',()=>openTarget(view.messages[200].id,'message',{conv:view.id}),()=>!!findRow(view.messages[200].id),'exact previously unmounted notification target appears');
 const exact=findRow(view.messages[200].id),rect=exact.getBoundingClientRect(),bounds=scroll().getBoundingClientRect();
 assert(rect.bottom>bounds.top&&rect.top<bounds.bottom,'exact notification target not scrolled into view');
 assert(!findRow(view.messages[0].id),'notification unnecessarily mounted oldest history');
 const quote=[...findRow(view.messages[399].id).querySelectorAll('button')].find(b=>b.textContent.includes('Retained message 1'));
 assert(quote,'reply metadata lost its unmounted parent');await measure('history.quote-exact-parent',()=>quote.click(),()=>!!findRow(view.messages[0].id),'exact previously unmounted quoted parent appears');
 const parent=findRow(view.messages[0].id),parentRect=parent.getBoundingClientRect();assert(parentRect.bottom>scroll().getBoundingClientRect().top&&parentRect.top<scroll().getBoundingClientRect().bottom,'reply target not visible');
 reports.exactTargets=true;
 const expandedRows=rows().length;
 await openTarget(view.messages[350].id,'message',{conv:view.id});await frame();await frame();
 reports.monotonicTargets=!!findRow(view.messages[0].id)&&rows().length>=expandedRows;
 reports.targetRows={expanded:expandedRows,later:rows().length};
 assert(reports.scrollGeometryReads<=10&&reports.monotonicTargets,'ordinary scroll cost or later target collapsed revealed history');

 const before={...counts};typing=true;await measure('presence.typing-burst',()=>{for(let i=0;i<1000;i++)emit('typing');},()=>root.querySelector('[data-part="typing"]')?.textContent.includes('is typing'),'typing status appears after 1000 push hints');
 assert(counts.dm===before.dm,'typing reloaded history');assert(counts.overview===before.overview,'typing reloaded overview');assert(counts.typing-before.typing<=2,'typing requests failed to coalesce');
 reports.typingBurst={events:1000,reads:counts.typing-before.typing,historyReads:counts.dm-before.dm};
 const previous=counts.dm;for(let i=0;i<1000;i++)emit('presence');await pause(80);assert(counts.dm===previous,'unrelated presence reloaded history');
 const bubble=root.querySelector('[data-mid="'+view.messages[0].id+'"]');
 held=new Promise(r=>release=r);
 view.messages[0].body='Edited while overview blocked';view.messages[0].reactions=[{emoji:'👍',by:[{id:'fixture-alice',label:'Alice'}]}];
 let start=performance.now();await measure('sync.edit-reaction-overview-blocked',()=>emit(),()=>bubble.textContent.includes('Edited while overview blocked')&&bubble.textContent.includes('👍'),'exact edited body and reaction visible while overview is held');
 reports.activeEditMs=Math.round(performance.now()-start);assert(held,'overview was not held');
 assert(root.querySelector('[data-mid="'+view.messages[0].id+'"]')===bubble,'unchanged bubble node lost');
 const box=root.querySelector('textarea'),setValue=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;
 start=performance.now();await measure('composer.type',()=>{setValue.call(box,'Immediate local preview');box.dispatchEvent(new Event('input',{bubbles:true}));},()=>box.value==='Immediate local preview'&&[...root.querySelectorAll('button')].some(b=>/^Send/.test(b.getAttribute('aria-label')||'')&&!b.disabled),'draft text visible and React send control enabled');
 reports.typingPaintMs=Math.round(performance.now()-start);
 const send=[...root.querySelectorAll('button')].find(b=>/^Send/.test(b.getAttribute('aria-label')||''));assert(send,'send control missing');
 await measure('composer.send',()=>send.click(),()=>root.textContent.includes('Immediate local preview')&&box.value==='','draft cleared and exact local message visible',()=>view.messages.length===402&&[...root.querySelectorAll('[data-part="message"]')].some(m=>m.textContent.includes('Immediate local preview')&&m.textContent.includes('Delivered')));
 reports.optimisticMs=Math.round(reports.actions.at(-1).feedbackMs);reports.storedDisplayMs=Math.round(reports.actions.at(-1).visibleMs);
 held=null;release();await pause(100);
 // A changed reaction/body must survive object sharing; a later unchanged
 // refresh must keep the same element and never erase the user's draft.
 setValue.call(box,'Keep my draft');box.dispatchEvent(new Event('input',{bubbles:true}));emit();await pause(80);assert(box.value==='Keep my draft','refresh erased draft');assert(bubble.textContent.includes('Edited while overview blocked'),'edit lost');
 const gaps=[];let last=performance.now();for(let i=0;i<40;i++){emit('typing');await frame();const now=performance.now();gaps.push(now-last);last=now;}
 gaps.sort((a,b)=>a-b);reports.frameP95Ms=Math.round(gaps[Math.floor(gaps.length*.95)]*10)/10;
 }
 if(suite==='unread'){
  const first=view.messages[0],later=[view.messages[340],view.messages[350]];
  const target=()=>root.querySelector('[data-mid="'+first.id+'"]');
  await measure('unread.normal-row-open',()=>[...root.querySelectorAll('[data-part="chat-row"]')].find(b=>b.textContent.includes('Vitalii')).click(),()=>onScreen(root.querySelector('section[aria-busy="true"]'))||onScreen(target()),'opening progress or first unread visible',()=>onScreen(target()));
  const divider=root.querySelector('[role="separator"][aria-label="Unread messages"]');assert(onScreen(divider),'first unread divider not visible');
  await until(()=>first.unread===false,'exact visible unread acknowledgement');assert(later.every(m=>m.unread),'unseen tail was marked read');
  reports.renderContainment=getComputedStyle(target()).contain;reports.timeFormatParity=[...root.querySelectorAll('[data-part="timeline"] time[datetime]')].every(t=>t.textContent===new Date(t.dateTime).toLocaleTimeString([],{hour:'numeric',minute:'2-digit'}));assert(reports.timeFormatParity,'cached timestamp changed locale formatting');
  const timeline=()=>[...root.querySelectorAll('[data-part="timeline"]')].find(shown),rows=()=>[...timeline().querySelectorAll('[data-mid]')];const initial=rows().length;assert(initial<=80,'old unread entry mounted the entire newer history');assert(button('Later messages'),'newer retained history has no paging control');
  const scroller=timeline().parentElement;scroller.scrollTop=scroller.scrollHeight;scroller.dispatchEvent(new Event('scroll'));await frame();const anchor=rows().at(-1),offset=anchor.getBoundingClientRect().top,next=view.messages[initial];
  await measure('unread.later-page',()=>button('Later messages').click(),()=>onScreen(root.querySelector('[data-mid="'+next.id+'"]')),'next retained page begins visibly');assert(rows().length-initial<=80,'later reveal exceeds one page');const drift=Math.abs(anchor.getBoundingClientRect().top-offset);assert(drift<2,'later-page reveal moved retained reading row '+drift);assert(later.every(m=>m.unread),'later paging acknowledged unseen tail');
  const latest=button('Latest');assert(latest,'old-window Latest control missing');await measure('unread.latest',()=>latest.click(),()=>onScreen(root.querySelector('[data-mid="'+view.messages.at(-1).id+'"]')),'explicit Latest reaches newest retained message');const latestGroups=timeline().querySelectorAll('[data-part="message"]:not([data-send-target]),[data-send-group]').length;assert(latestGroups<=80,'Latest from bounded entry mounted '+latestGroups+' groups (rows '+rows().length+')');assert(later.every(m=>m.unread),'Latest acknowledged offscreen tail');
  reports.firstUnreadEntry={normalChatRow:true,first:first.id,visible:true,divider:true,firstCleared:true,unseenTailRetained:true,initialMounted:initial,laterPageDrift:drift,latestMounted:rows().length,readActions:reports.apiCalls.filter(c=>c.path==='/api/act').length};
 }
 // Every step below asserts its actual changed UI before recording frame/paint
 // opportunities. Empty/error states remain explicitly named, never counted
 // as timing proof for an unvisited mutation or permission dialog.
 const nav=async(name,expected)=>{const b=name==='You'?button(/You$/):button(name);assert(b,'Missing navigation '+name);await measure('navigation.'+name.toLowerCase(),()=>b.click(),expected,'selected '+name+' page visible');};
 if(suite==='navigation'){
 await nav('Agents',()=>visibleText('Agents'));
 await nav('Review',()=>visibleText('Review'));
 await nav('Outbox',()=>visibleText('Outbox'));
 for(const name of ['Drafts','Reminders','Scheduled'])await measure('outbox.'+name.toLowerCase(),()=>button(name).click(),()=>button(name)?.getAttribute('aria-current')==='page','selected '+name+' section is current');
 }
 if(suite==='settings'){
 await nav(innerWidth>=1024?'Settings':'You',()=>visibleText(innerWidth>=1024?'Settings':'You'));
 const sections=[['Your devices','Your devices'],['Coding agents','Coding agents'],['Permissions','Permissions'],['People lists','People & agent lists'],['Notifications','Notifications'],['Appearance','Appearance'],['Workspaces','Workspaces'],['Storage','Storage'],['About','About']];
 for(const [name,title]of sections){
  const b=button(new RegExp('^'+name));assert(b,'Missing settings row '+name);
  await measure('settings.open.'+name.toLowerCase().replaceAll(' ','-'),()=>b.click(),()=>visibleText(title),title+' heading visible');
  if(name==='Notifications'){
   for(const name of ['Share when I’m typing','Show when people are typing']){const toggle=scope().querySelector('[role="switch"][aria-label="'+name+'"]');if(toggle){await until(()=>!toggle.disabled,'typing setting loaded');const before=toggle.getAttribute('aria-checked');await measure('settings.typing.'+name,()=>toggle.click(),()=>toggle.getAttribute('aria-checked')!==before,'switch checked value changes',()=>visibleText('Saved on this device.'));}}
  }
  if(name==='Appearance'){
   for(const choice of ['dark','light','system']){const radio=scope().querySelector('[role="radio"][value="'+choice+'"],input[type="radio"][value="'+choice+'"]');if(radio)await measure('appearance.'+choice,()=>radio.click(),()=>radio.getAttribute('aria-checked')==='true'||radio.checked,'selected '+choice+' radio is checked');}
  }
  if(name==='Storage'){
   await until(()=>visibleText('Storage couldn’t be read just now.'),'safe storage error');
  }
  if(innerWidth<1024){const back=scope().querySelector('[data-settings-back]');assert(back,'Missing settings Back');await measure('settings.back.'+name,()=>back.click(),()=>!!button(new RegExp('^'+name))&&!visibleText(title),'settings list returns and opened page retires');}
 }
 }
 if(suite==='navigation'){
 await nav('Chats',()=>[...root.querySelectorAll('[data-part="list"]')].some(onScreen));
 const chatSearch=()=>[...scope().querySelectorAll('[data-part="list"] input[type="search"]')].find(onScreen);
 const searchButton=button('Search chats');if(searchButton)await measure('search.open',()=>searchButton.click(),()=>!!chatSearch(),'chat search field visible in the active list');
 const search=chatSearch();if(search){const set=Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set;await measure('search.no-results',()=>{set.call(search,'No such synthetic person');search.dispatchEvent(new Event('input',{bubbles:true}));},()=>visibleText('Nothing matches'),'visible empty search result');await measure('search.clear',()=>button('Clear search').click(),()=>search.value===''&&buttons().some(b=>label(b).includes('Vitalii')),'search cleared and original chat row visible');}
 const fresh=button('New')||button('New chat');assert(fresh,'New chat control missing');
 await measure('new-chat.open',()=>fresh.click(),()=>onScreen(scope().querySelector('[role="dialog"]')),'new chat dialog visible on screen');
 const dialog=()=>scope().querySelector('[role="dialog"]'),personRows=()=>[...dialog().querySelectorAll('li')].filter(el=>el.textContent.includes('Person '));
 if(peopleCount>40){assert(personRows().length<=40,'new-chat initial directory render is unbounded');const before=personRows().length;await measure('new-chat.reveal',()=>button(/^Show more people/).click(),()=>personRows().length>before,'next directory page rendered');assert(personRows().length-before<=40,'directory reveal exceeds one bounded page');}
 const q=dialog().querySelector('input[type="search"]');assert(q,'new chat search missing');
 const set=Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set,wanted=peers.at(-1).label;
 await measure('new-chat.search-last-person',()=>{set.call(q,wanted);q.dispatchEvent(new Event('input',{bubbles:true}));},()=>q.value===wanted&&personRows().some(el=>el.textContent.includes(wanted))&&personRows().length===1,'last roster person found beyond first page, unrelated rows removed');
 await measure('new-chat.clear-resets-page',()=>{[...dialog().querySelectorAll('button')].find(b=>label(b)==='Clear search').click();},()=>q.value===''&&personRows().length>1,'query cleared and initial directory page restored');assert(personRows().length<=40,'clearing directory search did not reset page bound');
 const close=button('Close');assert(close,'New chat close control missing');const closing=dialog();await measure('new-chat.close',()=>close.click(),()=>closing.hasAttribute('data-ending-style')||!closing.isConnected,'closing transition begins visibly',()=>!dialog());

 }
 reports.hostPlatform=host.platform;reports.coverageClaim='Only named actions and seen-control census; unvisited source/role/state cells remain open';
 reports.people=peers.length;reports.retainedMessages=400;reports.counts=counts;
 const percentile=(values,p)=>{const sorted=[...values].sort((a,b)=>a-b);return sorted.length?sorted[Math.min(sorted.length-1,Math.ceil(sorted.length*p)-1)]:null;};
 reports.families={};for(const family of new Set(reports.actions.map(a=>a.id.split('.')[0]))){const actions=reports.actions.filter(a=>a.id.split('.')[0]===family&&a.ok);const values=actions.map(a=>a.feedbackMs);reports.families[family]={samples:values.length,p50:percentile(values,.5),p95:percentile(values,.95),max:values.length?Math.max(...values):null};}
 reports.routineOver200ms=reports.actions.filter(a=>a.ok&&a.feedbackMs>200&&!a.id.startsWith('presence.')&&!a.id.startsWith('sync.')).map(({id,feedbackMs,visibleMs,paintMs,frameMaxMs})=>({id,feedbackMs,visibleMs,paintMs,frameMaxMs}));
 reports.smooth=reports.routineOver200ms.length===0;reports.timingMeaning='family percentiles pool different named controls in one pass; not repeated per-control statistical confidence. 2000ms is catastrophic bound only, routine feedback above200ms remains an open smoothness finding.';
 assert(reports.pageErrors.length===0,'Runtime errors: '+reports.pageErrors.join('; '));window.latencyReport={ok:true,...reports};
}catch(e){window.latencyReport={ok:false,error:String(e.message||e)+'\n'+String(e.stack||''),...reports};}
if(!window.__agentnetLatencyInput)fetch('/latency-report?run='+encodeURIComponent(new URL(location.href).searchParams.get('run')||''),{method:'POST',body:JSON.stringify(window.latencyReport)}).catch(()=>{});
