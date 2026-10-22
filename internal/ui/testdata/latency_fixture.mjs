import {mount,unmount} from '/assets/skins/comic/entry.mjs';
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const frame=()=>new Promise(r=>requestAnimationFrame(r));
const assert=(v,m)=>{if(!v)throw Error(m);};
const until=async(f,label,ms=5000)=>{const end=performance.now()+ms;while(!f()){if(performance.now()>end)throw Error('Timed out: '+label);await pause(10);}};
const reports={actions:[],apiCalls:[],seenControls:[],pageErrors:[],feedbackFailures:[],inputDispatch:'synthetic DOM events (excludes OS input dispatch)',viewport:{width:innerWidth,height:innerHeight,dpr:devicePixelRatio},userAgent:navigator.userAgent,hardwareConcurrency:navigator.hardwareConcurrency,nativeScrollAnchoring:CSS.supports('overflow-anchor','auto')};window.latencyReport=null;
const pageError=e=>reports.pageErrors.push(String(e.message||e.reason||e).slice(0,1000));window.addEventListener('error',pageError);window.addEventListener('unhandledrejection',pageError);
try {
 const query=new URL(location.href).searchParams,token=query.get('token');
 const suite=window.__agentnetLatencySuite||query.get('suite')||'history';reports.suite=suite;const extraSuites=['topics','groups','compose','media','review','outbox','agents','teams','profile','storage','workspaces','permissions'];const extended=extraSuites.includes(suite);assert(['history','navigation','settings','unread','actions',...extraSuites].includes(suite),'Unknown timing workload');
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

 // These invented records follow api.gen.ts and the existing rendered journey
 // contracts. They exercise presentation only, never provider/native authority.
 const now=new Date().toISOString(),future=new Date(Date.now()+86400000).toISOString();
 const clone=v=>structuredClone(v),me=overview.person,peer=view.peer;
 if(extended){overview.agents=true;overview.me.fingerprint=me.fingerprint='11111111-22222222-33333333-44444444';for(const d of me.devices||[])if(d.address===overview.me.address)d.fingerprint=overview.me.fingerprint;}
 const harnesses=[{name:'codex',found:true},{name:'pi',found:true},{name:'claude',found:false}];
 const catalog={host:overview.me.address,local:true,harnesses,agents:[{record:{v:1,id:'1'.repeat(32),host:overview.me.address,host_key:overview.me.fingerprint,label:'Fixture Codex',harness:'codex',ts:1},enabled:true,default:true,responder:{chosen:true,manual:false,harness:'codex',dir:'/fixture',ready:true,harnesses}}]};
 let responder={chosen:true,manual:false,agent_id:'1'.repeat(32),harness:'codex',dir:'/fixture',ready:true,harnesses};
 const grants={questions:[],tasks:[],own_devices:[]};
 const schedules=[0,1].map(i=>({id:(90+i).toString(16).padStart(32,'0'),conv:view.id,topic:'',body:'Scheduled fixture '+i,due:future,timezone:'UTC',rev:1,state:'pending',audience:{persons:[me.person,peer.person],pids:[]},files:[]}));
 const historySchedule={...schedules[0],id:'8'.repeat(32),state:'sent',body:'Historical fixture schedule'};
 const teams={tags:true,legacy_add:true,current:true,status:'available',at:now,truncated:false,realm_id:'fixture',teams:[{version:2,realm_id:'fixture',id:'4'.repeat(32),name:'Fixture list',seq:1,hash:'f'.repeat(64),members:[me.person,peer.person],managers:[me.person],agents:[],manager:true,member:true,archived:false,conflict:false,listed:true}]};
 const cacheFiles=[{id:'1'.repeat(32),sha256:'2'.repeat(64),size:1024},{id:'3'.repeat(32),sha256:'4'.repeat(64),size:2048}];
 const bucket={files:2,reserved_bytes:3072,recorded_received_bytes:3072};
 const storage={local:{scope:'daemon',complete:true,known:{files:2,bytes:3072},areas:[],files:[]},remote:{status:'available',usage:{quota_bytes:1073741824,max_file_bytes:104857600,upload_idle_ttl_seconds:3600,location:'fixture relay',policy:{delivered_attachments:'retained',undelivered_attachments:'retained',unattached_attachments:'retained',message_envelopes:'retained',incomplete_uploads:'bounded',manual_delivered_age_default_seconds:86400,manual_unattached_age_default_seconds:86400},can_change_quota:true,own:{stored:bucket,incomplete:{...bucket,files:0,reserved_bytes:0}},global:{stored:bucket,incomplete:{...bucket,files:0,reserved_bytes:0}}}}};
 let reviewedSetup;
 let nativeFailure=false,notificationFailure=false,storageOffline=false,cacheOffline=false,cacheHold=false,releaseCache,workspaceInfo={name:'Fixture workspace',server:'Fixture server',can_rename:true};
 const relayStorage={can_admin:true,configured:true,connected:true,state:'connected',local:{files:2,bytes:3072},remote:{files:0,bytes:0},pending:{files:2,bytes:3072},remote_only_files:0,migration:{running:false,files:0,bytes:0,total_files:0,total_bytes:0}};
 const staged=[];reports.files=staged;
 const fileHash=async f=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',await Blob.prototype.arrayBuffer.call(f)))).map(b=>b.toString(16).padStart(2,'0')).join('');
 if(extended){
  view.messages=view.messages.slice(-4);delete view.messages.at(-1).quote;
  overview.person.state='self';overview.role='person';overview.groups=true;overview.persons=true;overview.topic_list=true;
  overview.directory.current=true;overview.files={max_count:8,max_file:100*1024*1024,max_message:100*1024*1024};
  view.members=[{...me,admin:true},peer];view.agents=[];view.guests=[];view.role='member';view.topics=[];
  overview.notify={available:true,native:true,enabled:true,preview:'',preview_default:'full'};
  if(suite==='topics'){
   view.kind=overview.dms[0].kind='group';view.title=overview.dms[0].title='Vitalii topics';
   view.topics=['active','done','archived'].map((state,i)=>({id:String(i+1).repeat(32),conv:view.id,title:'Fixture '+state,renamed:true,state,last:'Topic '+state,last_at:now,count:1,unread:0}));
   for(const [i,t] of view.topics.entries())view.messages.push({...view.messages[0],id:(20+i).toString(16).padStart(32,'0'),lid:(20+i).toString(16).padStart(32,'0'),topic:t.id,body:'Content '+t.title});
  }
  if(suite==='groups'){view.kind=overview.dms[0].kind='group';view.title=overview.dms[0].title='Vitalii launch';view.members=[{...me,admin:true},{...peer,admin:false}];}
  if(suite==='compose'){view.guests=[{pid:'6'.repeat(32),state:'dismissed',state_text:'Dismissed',host:peers.at(-1),host_here:false,inviter:me,shared:[],missing:0,held:0,can_decide:false,can_leave:false,can_end:true,can_send:false,audience_pending:false}];}
  if(suite==='groups'){for(const m of view.messages)m.group_ref={lid:m.lid,author:overview.me.fingerprint,hash:'8'.repeat(64)};overview.me.agent=true;overview.agent_devices=peers.slice(0,45).map(p=>p.address);}
  if(suite==='outbox')overview.reminders=[{message:view.messages[0].id,conv:view.id,from:peer.address,title:'Fixture overdue reminder',due:new Date(Date.now()-1000).toISOString(),overdue:true},{message:view.messages[1].id,conv:view.id,from:peer.address,title:'Fixture future reminder',due:future,overdue:false}];
  if(suite==='profile'){
   me.devices=[{address:overview.me.address,name:'Fixture laptop',fingerprint:overview.me.fingerprint,this:true},{address:'fixture/phone',name:'Fixture phone',fingerprint:'7'.repeat(64),this:false}];
   overview.links=[{id:'6'.repeat(32),name:'Fixture incoming phone',address:'fixture/new-phone',fingerprint:'8'.repeat(64),state:'pending',requested_at:now,expires:future}];
  }
  if(suite==='permissions'){grants.questions=[{person:peer.person,label:peer.label,address:peer.address,status:'active'}];overview.threads=[{id:'7'.repeat(32),peer:peer.address,title:'Fixture saved conversation',last:'Fixture context',last_at:now,count:1}];}
  if(suite==='review'){
   const native={token:'3'.repeat(32),harness:'codex',host:overview.me.address,request:'1'.repeat(32),key:overview.me.fingerprint,attempt:1,method:'item/commandExecution/requestApproval',thread:'fixture-thread',turn:'fixture-turn',item:'fixture-item',operation:'printf fixture',cwd:'/fixture',choices:['allow','deny','cancel'],expires:0};
   overview.review=[{id:'2'.repeat(32),peer:overview.me.address,kind:'message',notice:true,why:'Native input',excerpt:'1 request reported',at:now,report:{host:overview.me.address,at:now,items:[{id:native.request,key:native.key,kind:'question',state:'running',blocker:'needs_human',attempt:1,actionable:false,native}]}}];
   overview.group_invitations=[0,1].map(i=>({id:String(i+7).repeat(32),conv:view.id,direction:'in',status:'pending',title:'Fixture invitation '+i,inviter:peer.person,target:me.person,history:[]}));
   overview.links=[0,1].map(i=>({id:String(i+4).repeat(32),name:'Fixture review phone '+i,address:'fixture/review-'+i,fingerprint:'9'.repeat(64),state:'pending',requested_at:now,expires:future}));
   reports.nativeAuthority='presentation + invented response only; no native tool executed';
  }
 }
 let held,release,typing=false,seq=100,reactionFailure=false;
 let typingPreferences={send:false,show:true};
 const counts={overview:0,dm:0,typing:0,catalogs:0},listeners=new Set();
 const emit=hint=>{seq++;for(const fn of listeners)fn({type:'change',seq,...(hint?{hint}:{})});};
 let openTarget;
 const host={version:1,platform:window.__agentnetLatencyPlatform||query.get('platform')||'daemon',workspace:{id:'latency',name:'Fixture',state:'enrolled'},skins:[{id:'comic',name:'Comic',api:1}],onSkinsChange:()=>()=>{},onOpen:fn=>{openTarget=fn;},listen:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},
  async api(path,body){
   if(extended){
    const u=new URL(path,location.href),p=u.pathname;
    if(p==='/api/agents'){if(body){await pause(25);const a=catalog.agents.find(a=>a.record.id===body.id);if(body.action==='disable')a.enabled=false;else if(body.action==='update'){a.enabled=true;a.responder={...a.responder,harness:body.harness,dir:body.dir};}else if(body.action==='create')catalog.agents.push({record:{v:1,id:'5'.repeat(32),host:overview.me.address,host_key:overview.me.fingerprint,label:body.label,harness:body.harness,ts:2},enabled:true,responder:{...responder,harness:body.harness,dir:body.dir}});emit();return{saved:true,published:true,agent:a,note:'Fixture catalog saved'};}const remote=u.searchParams.get('host');return remote&&remote!==overview.me.address?{host:remote,local:false,agents:suite==='groups'&&overview.agent_devices.includes(remote)?[{record:{v:1,id:((overview.agent_devices.indexOf(remote)+100).toString(16).padStart(32,'0')),host:remote,host_key:'7'.repeat(64),label:'Fixture remote '+overview.agent_devices.indexOf(remote),harness:'codex',ts:1},enabled:true}]:[],harnesses:[]}:clone(catalog);}
    if(p==='/api/dm/guest/check')return{ready:true,needs_update:[],offline:[],text:''};
    if(p==='/api/responder'){if(body){await pause(25);const selected=body.agent_id&&catalog.agents.find(a=>a.record.id===body.agent_id);responder=selected?{...selected.responder,agent_id:selected.record.id,manual:false}:{...responder,...body};for(const a of catalog.agents)a.default=a.record.id===responder.agent_id;emit();}return clone(responder);}
    if(p==='/api/approvals')return clone(grants);
    if(p==='/api/folders')return{path:u.searchParams.get('path')||'/fixture',home:'/fixture',roots:['/'],dirs:[{name:'work',path:'/fixture/work'}],parent:'/',entries:[{name:'work',path:'/fixture/work',directory:true}],folders:[{name:'work',path:'/fixture/work'}]};
    if(p==='/api/assistant-setup'){if(body?.action==='review')reviewedSetup=clone(body);if(body?.action==='apply'){assert(reviewedSetup&&body.review_id==='fixture-review'&&JSON.stringify(body.harnesses)===JSON.stringify(reviewedSetup.harnesses),'setup Apply must match the exact reviewed token and harnesses');reports.setupApplyReview={review_id:body.review_id,harnesses:clone(body.harnesses),reviewMatched:true,nativeEffects:false};throw Error('Synthetic native setup refused; real harness configuration required');}return{local:true,review_id:body?.action==='review'?'fixture-review':'',harnesses:[{id:'codex',label:'Codex',detected:true,configured:false,registered:false,supported:true,state:'needs_setup',note:'Invented native setup presentation',change:'Update fixture hooks',target:'/fixture/settings'},{id:'pi',label:'Pi',detected:true,configured:true,registered:false,supported:true,state:'needs_activation',note:'Invented setup status'}]};}
    if(p==='/api/schedules')return body?{}:u.searchParams.has('id')?clone(schedules.find(s=>s.id===u.searchParams.get('id'))):clone(u.searchParams.get('all')==='1'?[...schedules,historySchedule]:schedules);
    if(p==='/api/schedule/audience')return{persons:[me.person,peer.person],pids:[]};
    if(p.startsWith('/api/schedule/')){await pause(25);let row=schedules.find(s=>s.id===body.id);if(p.endsWith('/create')){row={...body,rev:1,state:'pending',files:body.files||[]};schedules.push(row);}else{assert(row&&row.rev===body.rev,'schedule exact revision');if(p.endsWith('/edit'))Object.assign(row,body,{rev:row.rev+1});if(p.endsWith('/cancel'))row.state='cancelled';if(p.endsWith('/send_now'))row.state='sent';}emit();return clone(row);}
    if(p.startsWith('/api/remind')){await pause(25);const row=overview.reminders.find(r=>r.message===body.id);if(p==='/api/remind'){assert(row,'exact reminder');row.due=new Date(body.due*1000).toISOString();row.overdue=false;}else overview.reminders=overview.reminders.filter(r=>r.message!==body.id);emit();return{note:'Fixture reminder changed',reminder:row};}
    if(p.startsWith('/api/topic/')){await pause(25);const t=view.topics.find(t=>t.id===body.id);if(p.endsWith('/rename')){assert(t,'exact topic rename');t.title=body.title;t.renamed=!!body.title;}else if(p.endsWith('/done'))t.state='done';else if(p.endsWith('/archive'))t.state='archived';else if(p.endsWith('/reopen'))t.state='active';else if(p.endsWith('/delete'))view.topics=view.topics.filter(t=>t.id!==body.id);else throw Error('Unimplemented fixture topic route '+p);emit();return{note:'Fixture topic changed'};}
    if(p==='/api/topics')return{topics:clone(view.topics.filter(t=>(!u.searchParams.get('state')||t.state===u.searchParams.get('state'))&&(!u.searchParams.get('q')||t.title.toLowerCase().includes(u.searchParams.get('q').toLowerCase())))),matched:view.topics.length};
    if(p==='/api/groups/manage'){await pause(25);assert(body.conv===view.id,'exact group');const m=view.members.find(m=>m.person===body.person);if(body.action==='rename'){view.title=overview.dms[0].title=body.title;}else if(body.action==='promote')m.admin=true;else if(body.action==='demote')m.admin=false;else if(body.action==='remove')view.members=view.members.filter(m=>m.person!==body.person);else if(body.action==='leave'){view.role='left';overview.dms=[];}emit();return{queued:false};}
    if(p==='/api/request/followup'){await pause(25);assert(body.conv===view.id&&body.ref.id==='a'.repeat(32)&&body.ref.fingerprint===overview.me.fingerprint&&body.attempt===0,'tagged reply exact stored parent binding');view.messages.push({...body,lid:body.id,kind:'question',pid:'b'.repeat(32),from:overview.me.address,dir:'out',at:new Date().toISOString(),state:'queued',can:[],actions:[],reactions:[]});emit();return{note:'Reply queued in the original request context.'};}
    if(p==='/api/operator/decide'){await pause(25);if(nativeFailure){nativeFailure=false;throw Error('Synthetic native callback response unavailable');}return{note:'Fixture response saved; native result unknown'};}
    if(p==='/api/groups/decide'){await pause(25);overview.group_invitations=overview.group_invitations.filter(g=>g.id!==body.id);emit();return{recorded:true};}
    if(p==='/api/person/label'){await pause(25);me.label=body.label;emit();return clone(me);}
    if(p==='/api/device/link'){await pause(25);return{url:'https://fixture.invalid/one-use-only',expires:future};}
    if(p==='/api/device/decide'){await pause(25);overview.links=overview.links.filter(l=>l.id!==body.id);emit();return{note:'Fixture device decision stored'};}
    if(p==='/api/device/remove'){await pause(25);me.devices=me.devices.filter(d=>d.address!==body.address);emit();return{note:'Fixture device removed'};}
    if(p.startsWith('/api/notify/')){await pause(25);if(notificationFailure)throw Error('Synthetic notification preference unavailable');if(p.endsWith('/enable'))overview.notify.enabled=true;if(p.endsWith('/disable'))overview.notify.enabled=false;if(p.endsWith('/preview'))overview.notify.preview=body.preview;emit();return{note:'Fixture notification preference saved'};}
    if(p==='/api/workspace')return clone(workspaceInfo);
    if(p==='/api/workspace/name'){await pause(25);workspaceInfo.name=body.name;emit();return clone(workspaceInfo);}
    if(p==='/api/storage/attachments'){await pause(25);if(body){assert(body.action==='connect'||body.confirm===true,'Drive exact explicit confirmation');throw Error('Synthetic Drive '+body.action+' unavailable; real relay/Google authorization required');}return clone(relayStorage);}
    if(p==='/api/storage'){await pause(25);if(storageOffline)throw Error('Synthetic storage unavailable; Retry is safe.');return clone(storage);}
    if(p==='/api/storage/cache'){if(body){assert(body.selection?.length===1,'clear captured one-file selection');if(cacheHold){cacheHold=false;await new Promise(r=>releaseCache=r);}return{removed:{files:cacheOffline?0:1,bytes:cacheOffline?0:body.selection[0].size},retained:{files:cacheOffline?1:0,bytes:cacheOffline?body.selection[0].size:0}};}return{files:cacheFiles,usage:{files:2,bytes:3072}};}
    if(p==='/api/storage/quota'){await pause(25);assert(body.confirm===true,'quota explicit confirmation');storage.remote.usage.quota_bytes=body.quota_bytes;return clone(storage);}
    if(p==='/api/teams')return clone(teams);
    if(p==='/api/team'){await pause(25);let t=teams.teams.find(t=>t.id===body.team);if(body.op==='create'){t={...clone(teams.teams[0]),id:'9'.repeat(32),name:body.name,members:[me.person],agents:[]};teams.teams.push(t);}else if(body.op==='rename')t.name=body.name;else if(body.op==='delete')teams.teams=teams.teams.filter(x=>x.id!==t.id);else if(body.op==='add')t.members.push(body.target);else if(body.op==='remove')t.members=t.members.filter(id=>id!==body.target);else if(body.op==='manager-add')t.managers.push(body.target);else if(body.op==='manager-remove')t.managers=t.managers.filter(id=>id!==body.target);else if(body.op==='agent-add')t.agents.push(body.agent);else if(body.op==='agent-remove')t.agents=t.agents.filter(a=>a.id!==body.agent.id);else throw Error('Unimplemented fixture team '+body.op);emit();return clone(t);}
    if(p==='/api/teams/snapshot')return{realm_id:'fixture',sources:[],persons:[{id:peer.person}],at:1};
    if(p==='/api/act'&&body?.do!=='read'){
     await pause(25);if(body.do==='continue'){if(nativeFailure){nativeFailure=false;throw Error('Synthetic native callback response unavailable');}return{note:'Fixture callback response saved; native result unknown'};}if(body.do==='native_response')return{note:'Response stored for current fixture callback'};
     if(['approve','unapprove','grant_tasks','revoke_tasks'].includes(body.do)){const task=body.do.includes('tasks'),key=task?'tasks':'questions',remove=['unapprove','revoke_tasks'].includes(body.do);grants[key]=grants[key].filter(g=>(g.person||g.address)!==body.id);if(!remove)grants[key].push({person:body.id,label:body.id===me.person?me.label:peer.label,status:'active',address:peer.address});emit();return{};}
     if(body.do==='native_reply')return{note:'Response saved'};
    }
   }
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
 if(extended)host.stage=async file=>{const item={name:file.name,type:file.type,size:file.size,sha256:await fileHash(file)};staged.push(item);return 'fixture-staged-'+staged.length;};
 if(suite==='workspaces'){
  const memberships=[{...host.workspace,endpoint:location.origin,address:overview.me.address,handle:'fixture-active'},{id:'other',name:'Fixture other',state:'enrolled',endpoint:'https://fixture.invalid',address:'fixture/other',handle:'fixture-other'}],disconnected=[{id:'closed',name:'Fixture closed',state:'disconnected',endpoint:'https://fixture.invalid',address:'fixture/closed'}],subscribers=new Set(),bags={};let selectedWorkspace=host.workspace.id;
  const changed=()=>{for(const fn of subscribers)fn({id:selectedWorkspace});};reports.hostCalls=[];
  const owned=async(name,body,run)=>{const c={name,request:clone(body),start:performance.now()};reports.hostCalls.push(c);try{const v=await run();c.ok=true;return v;}catch(e){c.ok=false;throw e;}finally{c.result=performance.now();}};
  host.workspaces={list:()=>memberships,active:()=>selectedWorkspace,has:id=>memberships.some(w=>w.id===id),onChange:fn=>{subscribers.add(fn);return()=>subscribers.delete(fn);},state:id=>bags[id]||(bags[id]={}),select:id=>{assert(memberships.some(w=>w.id===id),'unknown fixture membership');selectedWorkspace=id;changed();},
   rename:(id,name)=>owned('workspace.rename',{id,name},async()=>{await pause(25);const w=memberships.find(w=>w.id===id);w.name=name;changed();return w;}),
   disconnect:id=>owned('workspace.disconnect',{id},async()=>{await pause(25);const w=memberships.find(w=>w.id===id);assert(id!==host.workspace.id,'cannot disconnect active fixture in this vector');memberships.splice(memberships.indexOf(w),1);disconnected.push({...w,state:'disconnected'});changed();}),
   disconnected:async()=>disconnected,join:body=>owned('workspace.join',body,async()=>{await pause(25);throw Error('Synthetic join refused; real server invitation verification required');}),reconnect:id=>owned('workspace.reconnect',{id},async()=>{await pause(25);throw Error('Synthetic reconnect unavailable; saved identity retained');})};
 }
 const originalAPI=host.api;
 host.api=async(path,body)=>{
  const call={path,method:body===undefined?'read':'write',start:performance.now()};if(extended&&body!==undefined)call.request=clone(body);reports.apiCalls.push(call);
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
  window.latencyProgress={active:id,reports};census();assert(!expected(),id+' expected visible state already true before input');const record={id,expected:description,input:'synthetic DOM',start:performance.now(),apiStart:reports.apiCalls.length,hostStart:reports.hostCalls?.length||0},gaps=[];performance.mark('latency:'+id+':input');let running=true,last=record.start;
  const watch=()=>{if(!running)return;const now=performance.now();gaps.push(now-last);last=now;requestAnimationFrame(watch);};requestAnimationFrame(watch);
  try{action();await until(expected,id+' visible feedback',3000);record.feedbackMs=performance.now()-record.start;performance.mark('latency:'+id+':feedback');
   await until(complete,id+' completed visible state',3000);record.visibleMs=performance.now()-record.start;await frame();await frame();assert(complete(),id+' state lost before paint opportunity');record.paintMs=performance.now()-record.start;
   if(reports.hostCalls)record.hostResults=reports.hostCalls.slice(record.hostStart).map(c=>({...c,startMs:c.start-record.start,resultMs:c.result===undefined?null:c.result-record.start}));
   record.api=reports.apiCalls.slice(record.apiStart).map(c=>({path:c.path,method:c.method,startMs:c.start-record.start,resultMs:c.result===undefined?null:c.result-record.start,ok:c.ok??null}));
   const writes=record.api.filter(c=>c.method==='write'&&c.resultMs!==null&&c.ok===true),failed=record.api.filter(c=>c.method==='write'&&c.resultMs!==null&&c.ok===false),projections=record.api.filter(c=>c.path.startsWith('/api/dm?')&&c.resultMs!==null&&c.ok===true);
   record.durableResultMs=writes.length?Math.max(...writes.map(c=>c.resultMs)):null;record.durableSource=writes.length?'synthetic host API result (not database persistence)':null;
   record.failedResultMs=failed.length?Math.max(...failed.map(c=>c.resultMs)):null;
   record.projectionResultMs=projections.length?Math.max(...projections.map(c=>c.resultMs)):null;
   record.frameMaxMs=Math.max(0,...gaps);record.frameP95Ms=gaps.sort((a,b)=>a-b)[Math.min(gaps.length-1,Math.floor(gaps.length*.95))]||0;
   record.stateVerified=true;record.feedbackBoundPassed=record.feedbackMs<2000;record.ok=record.feedbackBoundPassed;if(!record.feedbackBoundPassed)reports.feedbackFailures.push(id+' feedback exceeds existing optimistic-feedback bound');census();
  }finally{running=false;delete record.apiStart;delete record.hostStart;reports.actions.push(record);}
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
  await menu();await measure('message.copy-feedback',()=>item('Copy text').click(),()=>visibleText('Copied')||visibleText('Couldn’t copy here'),'clipboard outcome visible; OS clipboard persistence is separate');reports.clipboardOutcome=visibleText('Copied')?'browser clipboard accepted':'clipboard unavailable here';await settled();
  await menu();await measure('message.select',()=>item('Select').click(),()=>visibleText('1 message selected')&&!!item('Stop selecting'),'exact message selected with explicit exit');await settled();
  const move=item('Move to topic');if(move){await measure('message.organize-open',()=>move.click(),()=>visibleText('Move selected messages'),'organization review for selected original visible');await settled();const organize=[...scope().querySelectorAll('[role="dialog"]')].find(d=>d.textContent.includes('Move selected messages'));assert(organize.textContent.includes('Edited exact action target'),'organization lost exact original');await measure('message.organize-cancel',()=>[...organize.querySelectorAll('button')].find(b=>label(b)==='Cancel').click(),()=>organize.hasAttribute('data-ending-style')||!organize.isConnected,'organization cancelled without applying move',()=>!organize.isConnected);await settled();}
  await measure('message.selection-stop',()=>item('Stop selecting').click(),()=>!item('Stop selecting')&&!!root.querySelector('[data-part="composer"]'),'selection exits to original composer');
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
 if(extended){
  const active=(selector,box=scope())=>[...box.querySelectorAll(selector)].find(e=>shown(e)&&!e.closest('[data-ending-style]'));
  const control=(name,box=scope())=>[...box.querySelectorAll('button,[role="button"],[role="menuitem"]')].find(e=>shown(e)&&!e.closest('[data-ending-style]')&&(typeof name==='string'?label(e)===name:name.test(label(e))));
  const sheet=title=>[...scope().querySelectorAll('[role="dialog"],[role="alertdialog"]')].find(e=>shown(e)&&!e.closest('[data-ending-style]')&&[...e.querySelectorAll('h1,h2,h3')].some(h=>h.textContent===title));
  const tap=async(id,el,expected,description,complete=expected)=>{assert(el,'Missing control '+id);el.scrollIntoView({block:'nearest'});await frame();await measure(id,()=>el.click(),expected,description,complete);};
  const input=(el,value)=>{assert(el,'Missing input');Object.getOwnPropertyDescriptor(el instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:el instanceof HTMLSelectElement?HTMLSelectElement.prototype:HTMLInputElement.prototype,'value').set.call(el,value);el.dispatchEvent(new Event(el instanceof HTMLSelectElement?'change':'input',{bubbles:true}));};
  const fill=async(id,el,value,expected=()=>el.value===value,description='exact edited field value visible',complete=expected)=>{assert(el,'Missing input '+id);el.scrollIntoView({block:'nearest'});await frame();await measure(id,()=>input(el,value),expected,description,complete);};
  const retire=()=>until(()=>!scope().querySelector('[data-ending-style]'),'previous transient retires');
  const closeSheet=async(id,title)=>{const d=sheet(title);assert(d,'Missing sheet '+title);await tap(id,control('Close',d)||control('Cancel',d)||control('Done',d)||control('Back to the conversation',d),()=>d.hasAttribute('data-ending-style')||!d.isConnected,'exact sheet starts closing',()=>!d.isConnected);await retire();};
  const settings=async(name,title=name)=>{
   if(active('[data-settings-back]')&&!visibleText(title))await tap('settings.back-before-'+name,active('[data-settings-back]'),()=>!!control(/^Your devices/),'settings list visible before selecting another section');
   if(!control(/^Your devices/))await tap('settings.enter-'+suite,button(innerWidth>=1024?'Settings':/You$/),()=>!!control(/^Your devices/),'settings section list visible');
   if(!visibleText(title)){const b=name==='Profile'?buttons().find(b=>!b.closest('nav')&&[...b.querySelectorAll('span')].some(s=>s.textContent===me.label)):control(new RegExp('^'+name));await tap('settings.section-'+name.toLowerCase().replaceAll(' ','-'),b,()=>visibleText(title),'exact '+title+' heading visible');}
  };
  const chat=async()=>{await tap(suite+'.open-chat',initialChat(),()=>onScreen(root.querySelector('[data-part="timeline"]')),'exact conversation timeline visible');};
  const text=()=>active('[data-part="composer-field"] textarea');
  const more=()=>control('Add to message');
  const plus=async id=>{await retire();await tap(id,more(),()=>!!control(/^Photo or file/),'composer menu choices visible');};
  if(suite==='topics'){
   await chat();await tap('topics.all-open',control(/^All topics/),()=>!!sheet('All topics'),'all topics dialog visible');
   const d=()=>sheet('All topics');
   for(const filter of ['Done','Archived','Active'])await tap('topics.filter-'+filter.toLowerCase(),control(new RegExp('^'+filter),d()),()=>!!d().querySelector('ul[aria-label="'+filter+' topics"]'),'exact '+filter+' topic list visible');
   const q=d().querySelector('input[type="search"]');await fill('topics.search-all-states',q,'Fixture',()=>q.value==='Fixture'&&onScreen(q)&&onScreen(control('Clear search',d())),'controlled query and matching Clear search control visible',()=>!!d().querySelector('ul[aria-label="Matching topics"]')&&d().querySelectorAll('li').length===3);
   await tap('topics.search-clear',control('Clear search',d()),()=>q.value===''&&onScreen(q)&&!control('Clear search',d()),'controlled query cleared and Clear search control removed',()=>q.value===''&&!!d().querySelector('ul[aria-label="Active topics"]'));
   reports.topicSearchStages={feedback:'controlled query plus matching Clear search presence/absence',complete:'all-status matching list or restored Active list after background debounce',configuredSearchDelayMs:250,source:'model.ts TOPICS.searchDelay; Conversation.alltopics.tsx query->q timer',limit:'250ms is the inspected product debounce, not a new test bound; earlier completion-only observations remain unchanged in retained logs'};
   const checkbox=d().querySelector('input[type="checkbox"]');await tap('topics.select',checkbox,()=>checkbox.checked&&d().textContent.includes('1 selected'),'exact topic selection visible');
   await tap('topics.selection-archive-review',control('Archive',d()),()=>!!control('Confirm',d()),'explicit archive review appears');
   await tap('topics.selection-confirm',control('Confirm',d()),()=>!!control('Undo',d()),'pending change with Undo visible');
   await tap('topics.selection-undo',control('Undo',d()),()=>!control('Undo',d()),'pending topic archive cancelled');
   if(control('Cancel selection',d()))await tap('topics.selection-clear',control('Cancel selection',d()),()=>!d().textContent.includes('1 selected'),'selected state cleared');
   await closeSheet('topics.all-close','All topics');
   await tap('topics.new-intent',control('New topic'),()=>control('New topic')?.getAttribute('aria-pressed')==='true','new-topic intent selected');
   await tap('topics.new-cancel',control('New topic'),()=>control('New topic')?.getAttribute('aria-pressed')==='false','new-topic intent cancelled');
   await tap('topics.all-reopen',control(/^All topics/),()=>!!sheet('All topics'),'all topics reopened');
   await tap('topics.select-active',control(/^Fixture active,/,d()),()=>!sheet('All topics')&&!!root.querySelector('[data-topic="'+view.topics[0].id+'"][aria-current="true"]'),'exact selected topic chip current');await retire();
   const chip=()=>root.querySelector('[data-topic="'+view.topics[0].id+'"][aria-current="true"]');
   await tap('topics.menu',chip(),()=>!!control('Rename…'),'selected topic menu visible');
   await tap('topics.rename-open',control('Rename…'),()=>!!sheet('Rename topic'),'rename topic editor visible');await retire();
   await fill('topics.rename-text',sheet('Rename topic').querySelector('input'),'Fixture renamed');
   await tap('topics.rename-save',control('Save',sheet('Rename topic')),()=>!sheet('Rename topic')&&label(chip()).includes('Fixture renamed'),'exact title updated after API result');await retire();
   await tap('topics.menu-done',chip(),()=>!!control('Mark done'),'done action visible');
   await tap('topics.mark-done',control('Mark done'),()=>!!control('Reopen')&&view.topics[0].state==='done','topic done with concrete Reopen control');await retire();
   await tap('topics.reopen',control('Reopen'),()=>view.topics[0].state==='active'&&!control('Reopen'),'topic active after authoritative projection');
  }
  if(suite==='groups'){
   await chat();await tap('groups.members-open',control(/Who’s in this chat/),()=>!!sheet('In this chat')||!!active('aside[aria-label="In this chat"]'),'group member panel or sheet visible');
   await tap('groups.rename-open',control('Rename group…'),()=>!!sheet('Rename the group'),'rename group editor visible');
   await fill('groups.rename-text',sheet('Rename the group').querySelector('input'),'Fixture renamed group');
   await tap('groups.rename-save',control('Rename',sheet('Rename the group')),()=>view.title==='Fixture renamed group'&&!sheet('Rename the group'),'group name updated after exact group change');await retire();
   const memberMenu=()=>control(/^Change Vitalii in/);
   for(const [action,item,confirm]of [['promote','Make admin…','Make admin'],['demote','Remove admin role…','Remove admin role']]){
    await tap('groups.member-menu-'+action,memberMenu(),()=>!!control(item),'member role menu visible');
    await tap('groups.role-review-'+action,control(item),()=>!!sheet(action==='promote'?'Make Vitalii an admin?':'Remove Vitalii’s admin role?'),'specific role confirmation visible');await retire();
    await tap('groups.role-'+action,control(confirm),()=>view.members[1].admin===(action==='promote')&&!control(confirm),'specific role changed after API result');await retire();
   }
   await tap('groups.leave-review',control('Leave group…'),()=>!!control('Leave group'),'leave confirmation visible');
   await tap('groups.leave-cancel',control('Cancel',sheet('Leave “Fixture renamed group”?')),()=>!sheet('Leave “Fixture renamed group”?'),'leave cancelled and membership retained');assert(view.role==='member','leave cancel changed membership');await retire();
   if(sheet('In this chat'))await closeSheet('groups.members-close','In this chat');else await tap('groups.members-close',control('Close panel'),()=>!active('aside[aria-label="In this chat"]'),'group member panel closed');
   await tap('groups.invite-open',control('Bring in')||control('Bring someone in'),()=>!!sheet('Bring someone in'),'invitation picker visible');
   const invite=()=>sheet('Bring someone in'),options=kind=>[...invite().querySelectorAll('input[name="invite-who"]')].filter(e=>e.value.startsWith(kind+':'));
   await until(()=>options('a').length>0,'published agent choices loaded');reports.inviteInitial={people:options('p').length,agents:options('a').length};assert(options('p').length<=41&&options('a').length<=41,'invitation directory mounts unbounded choices');
   for(const [kind,labelText]of [['p','people'],['a','agents']]){const before=options(kind).length;await tap('groups.invite-reveal-'+labelText,control(new RegExp('^Show more '+labelText),invite()),()=>options(kind).length>before,'next bounded '+labelText+' page visible');assert(options(kind).length-before<=40,'invite reveal exceeded forty');}
   const search=invite().querySelector('input[type="search"]');await fill('groups.invite-search-last',search,peers.at(-1).label,()=>options('p').length===1&&invite().textContent.includes(peers.at(-1).label),'last roster person found beyond first page');
   await fill('groups.invite-clear',search,'',()=>options('p').length>1,'full query cleared');assert(options('p').length<=41&&options('a').length<=41,'query clear did not reset invite pages');
   const whole=[...invite().querySelectorAll('label')].find(l=>l.textContent.includes('Whole chat'));if(!whole.querySelector('input').checked)await tap('groups.invite-whole-chat',whole.querySelector('input'),()=>whole.querySelector('input').checked,'whole-chat scope explicitly selected');
   const member=invite().querySelector('input[name="human-invite-mode"][value="member"]');await tap('groups.invite-member-mode',member,()=>member.checked&&!!active('section[aria-label="Invite team people"]'),'admin permanent-member mode visible');assert(options('p').length<=41,'mode change did not reset people page');
   const guest=invite().querySelector('input[name="human-invite-mode"][value="guest"]');await tap('groups.invite-guest-mode',guest,()=>guest.checked&&!active('section[aria-label="Invite team people"]'),'guest mode restored without membership permission');
   const chosen=options('p')[0];await tap('groups.invite-person-choice',chosen,()=>chosen.checked&&!!control('Fewer messages',invite()),'exact person and history review visible');
   const beforeCount=invite().querySelector('output').textContent;await tap('groups.history-fewer',control('Fewer messages',invite()),()=>invite().querySelector('output').textContent!==beforeCount,'shared history count reduced');
   const selectedLabel=[...invite().querySelectorAll('label')].find(l=>l.textContent==='Selected messages');await tap('groups.history-selected',selectedLabel.querySelector('input'),()=>selectedLabel.querySelector('input').checked,'exact-message history mode checked');
   const sinceLabel=[...invite().querySelectorAll('label')].find(l=>l.textContent==='Since a date');await tap('groups.history-since',sinceLabel.querySelector('input'),()=>sinceLabel.querySelector('input').checked,'date-limited history mode checked');
   const agent=options('a')[0];await tap('groups.invite-agent-choice',agent,()=>agent.checked&&invite().textContent.includes('Who can give it tasks without asking'),'concrete agent and task-scope controls visible');
   const taskField=[...invite().querySelectorAll('fieldset')].find(f=>f.querySelector('legend')?.textContent==='Who can give it tasks without asking'),task=taskField?.querySelector('input[type="checkbox"]:not(:disabled)');assert(task,'verified own task principal missing');const beforeTask=task.checked;await tap('groups.invite-task-choice',task,()=>task.checked!==beforeTask,'exact task principal checkbox toggled without granting outside invitation');
   const scoped=[...invite().querySelectorAll('label')].find(l=>l.textContent.includes('Only this topic:'));await tap('groups.invite-topic-scope',scoped.querySelector('input'),()=>scoped.querySelector('input').checked&&invite().textContent.includes('Other topics stay outside this invitation.'),'selected topic scope remains explicit');
   assert(options('p').length<=41&&options('a').length<=41,'scope change did not reset bounded candidates');
   await closeSheet('groups.invite-cancel','Bring someone in');
   view.members.find(m=>m.person===me.person).admin=false;emit();await until(()=>!control('Rename group…'),'member role projection');
   await tap('groups.member-invite-open',control('Bring in')||control('Bring someone in'),()=>!!sheet('Bring someone in'),'member invitation picker visible');const memberWhole=[...sheet('Bring someone in').querySelectorAll('label')].find(l=>l.textContent.includes('Whole chat'));if(!memberWhole.querySelector('input').checked)await tap('groups.member-whole-chat',memberWhole.querySelector('input'),()=>memberWhole.querySelector('input').checked,'member whole-chat guest scope explicit');const memberOnly=sheet('Bring someone in').querySelector('input[name="human-invite-mode"][value="member"]');assert(memberOnly?.disabled,'member could add permanent group member');assert(sheet('Bring someone in').querySelector('input[name="human-invite-mode"][value="guest"]')?.checked,'member guest mode changed');
   await closeSheet('groups.member-invite-cancel','Bring someone in');

  }
  if(suite==='compose'){
   await chat();await fill('compose.draft-text',text(),'Fixture draft');
   await tap('compose.mention-open',control('Mention someone'),()=>!!active('[role="listbox"]'),'mention choices visible');
   const option=active('[role="option"]');if(option)await tap('compose.mention-pick',option,()=>text().value.includes('@'),'selected mention inserted in draft');
   await plus('compose.plus-open');await tap('compose.emoji-open',control(/^Emoji/),()=>!!control('Skin tone'),'emoji picker visible');await retire();
   await tap('compose.emoji-tone-open',control('Skin tone'),()=>!!active('[role="radiogroup"][aria-label="Skin tone"]'),'emoji tone choices visible');
   await tap('compose.emoji-tone',control('medium'),()=>!active('[role="radiogroup"][aria-label="Skin tone"]'),'tone chosen and tone selector closed');
   const emoji=active('[role="gridcell"]'),beforeEmoji=text().value;await tap('compose.emoji-pick',emoji,()=>!control('Skin tone')&&text().value!==beforeEmoji,'picked emoji visible in draft');await retire();
   await plus('compose.plus-schedule');await tap('compose.schedule-open',control(/^Schedule…/),()=>!!sheet('Schedule message'),'schedule review visible');await retire();
   const scheduleDialog=sheet('Schedule message'),due=scheduleDialog.querySelector('input[type="datetime-local"]');if(due)await fill('compose.schedule-time',due,new Date(Date.now()+2*86400000).toISOString().slice(0,16));
   await tap('compose.schedule-save',control('Save schedule',scheduleDialog),()=>schedules.length===3&&!sheet('Schedule message'),'same captured draft schedule stored and dialog closed');await retire();
   await fill('compose.new-draft',text(),'Retained outbox draft');
   if(innerWidth<1024)await tap('compose.back-to-chats',control('Back to chats'),()=>!!initialChat(),'chat list visible with draft retained');
   await tap('compose.open-outbox',button(/^Outbox\b/),()=>!!active('section[aria-label="Outbox"]'),'outbox visible');
   await tap('compose.drafts-tab',control(/^Drafts/),()=>!!active('section[aria-label="Drafts"]'),'draft section visible');
   const draftButton=control(/Retained outbox draft/);await tap('compose.draft-restore',draftButton,()=>onScreen(text())&&text().value==='Retained outbox draft','exact original conversation draft restored');
   await tap('compose.returning-guest-panel',control(/Who’s in this chat/),()=>!!sheet('In this chat')||!!active('aside[aria-label="In this chat"]'),'current chat participation visible');
   await tap('compose.returning-preselected',control('Invite again'),()=>!!sheet('Bring someone in'),'returning guest invitation visible');
   const returning=sheet('Bring someone in'),who=returning.querySelector('input[name="invite-who"]:checked');assert(who?.value==='p:'+peers.at(-1).person,'returning out-of-page person rebound');assert(returning.querySelectorAll('input[name="invite-who"][value^="p:"]').length<=41,'preselected appended beyond bounded people page');reports.preselectedOutOfPage={key:who.value,checked:who.checked,count:returning.querySelectorAll('input[name="invite-who"][value^="p:"]').length};
   await closeSheet('compose.returning-cancel','Bring someone in');
   // Normal Reply + actual mention choice derives the stored exact parent;
   // the invented host verifies the request shape, never harness authority.
   const pid='b'.repeat(32),original='a'.repeat(32),answer='d'.repeat(32);
   view.agents=[{pid,agent_id:catalog.agents[0].record.id,host:{...me,address:overview.me.address,fingerprint:overview.me.fingerprint},state:'active',state_text:'Active',can_ask:true,can_decide:false,can_dismiss:true,host_here:true,inviter:me,invited:now,tasks_from:[],shared:[],missing:0,held:0}];
   view.messages.push({id:original,lid:original,dir:'out',from:overview.me.address,kind:'question',body:'Fixture original budget request',pid,target:{address:overview.me.address,fingerprint:overview.me.fingerprint,agent_id:catalog.agents[0].record.id},exec:{state:'answered',host:overview.me.address,attempt:1},at:now,can:[],actions:[],reactions:[]},{id:answer,lid:answer,dir:'in',from:overview.me.address,kind:'answer',verified_agent:true,body:'Fixture which approved budget?',reply_to:original,pid,at:now,can:[],actions:[],reactions:[]});emit();
   const answerRow=()=>root.querySelector('[data-mid="'+answer+'"]');await until(answerRow,'exact ordinary clarification row');await fill('compose.clear-before-reply',text(),'');
   await measure('compose.clarification-actions',()=>answerRow().querySelector('[data-message-bubble]').dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true})),()=>!!control('Reply'),'exact clarification reply action visible');
   await tap('compose.clarification-reply',control('Reply'),()=>!!control('Cancel reply'),'reply chip binds exact clarification');await retire();
   await tap('compose.clarification-mention-open',control('Mention someone'),()=>!!active('[role="listbox"]'),'current participants picker visible');
   const agentChoice=[...scope().querySelectorAll('[role="option"]')].find(e=>shown(e)&&e.textContent.includes('Ask'));await tap('compose.clarification-agent',agentChoice,()=>!!control(/^Don’t ask /),'exact live agent mention selected');
   const tagged=text().value;await fill('compose.clarification-answer',text(),tagged+' Use the approved budget');
   const followups=reports.apiCalls.filter(c=>c.path==='/api/request/followup').length;
   await tap('compose.clarification-send',control(/^Ask /),()=>text().value===''&&!!root.querySelector('[data-part="timeline"]')?.textContent.includes('Use the approved budget'),'captured reply leaves composer and exact followup row appears',()=>reports.apiCalls.filter(c=>c.path==='/api/request/followup'&&c.ok===true).length===followups+1&&visibleText('Reply queued in the original request context.'));
   reports.taggedReply={parent:original,answer,pid,attempt:0,authority:'invented host shape assertion; signed claim/native execution requires provider journey'};

  }
  if(suite==='outbox'){
   await tap('outbox.open',button(/^Outbox\b/),()=>!!active('section[aria-label="Outbox"]'),'outbox page visible');
   await until(()=>control('Show history'),'schedules loaded');
   await tap('outbox.history-show',control('Show history'),()=>!!active('[aria-label="Scheduled message history"]'),'scheduled history visible');
   await tap('outbox.history-hide',control('Hide history'),()=>!active('[aria-label="Scheduled message history"]'),'scheduled history hidden');
   const search=active('input[placeholder="Chat, message or attachment"]');await fill('outbox.search',search,'absent fixture',()=>visibleText('No pending messages match your search.'),'explicit schedule no-results state');await fill('outbox.search-clear',search,'',()=>!!control('Edit schedule'),'original schedule rows restored');
   await tap('outbox.schedule-edit',control('Edit schedule'),()=>!!active('textarea[aria-label="Scheduled message text"]'),'one schedule editor visible');
   await fill('outbox.schedule-edit-text',active('textarea[aria-label="Scheduled message text"]'),'Edited scheduled fixture');
   await tap('outbox.audience-review',control('Review audience'),()=>!!control('Confirm reviewed audience'),'current audience review visible');
   await tap('outbox.audience-confirm',control('Confirm reviewed audience'),()=>!control('Confirm reviewed audience'),'reviewed audience explicitly selected');
   await tap('outbox.schedule-save',control('Save schedule changes'),()=>!active('textarea[aria-label="Scheduled message text"]')&&schedules[0].body==='Edited scheduled fixture','exact schedule revision saved');
   await tap('outbox.schedule-edit-again',control('Edit schedule'),()=>!!active('textarea[aria-label="Scheduled message text"]'),'same row edit visible');
   await tap('outbox.schedule-keep',control('Keep saved schedule'),()=>!active('textarea[aria-label="Scheduled message text"]'),'edit cancelled without mutation');
   await tap('outbox.schedule-reopen-for-cancel',control('Edit schedule'),()=>!!control('Cancel schedule'),'specific schedule expanded for cancellation');
   await tap('outbox.schedule-cancel',control('Cancel schedule'),()=>schedules[0].state==='cancelled'&&!root.textContent.includes('Edited scheduled fixture'),'exact schedule cancelled and active row removed');
   const remainingSchedule=schedules[1];await tap('outbox.schedule-sendnow-open',control('Edit schedule'),()=>!!control('Send now'),'other exact pending schedule details visible');
   const recipients=active('article details');await tap('outbox.schedule-recipients',recipients.querySelector('summary'),()=>recipients.open,'captured audience disclosed');
   await tap('outbox.schedule-sendnow',control('Send now'),()=>remainingSchedule.state==='sent'&&!control('Send now'),'exact revision dispatched, delivery separately confirmed');
   await tap('outbox.reminders-tab',control(/^Reminders/),()=>!!control(/^Reminder done:/),'due reminder list visible');
   const find=active('input[placeholder="Message or sender"]');await fill('outbox.reminder-search',find,'absent fixture',()=>visibleText('No reminders match your search.'),'reminder no-results state');await fill('outbox.reminder-search-clear',find,'',()=>!!control(/^Reminder done:/),'reminder rows restored');
   await tap('outbox.reminder-details',control('Details'),()=>!!sheet('Reminder'),'exact reminder details visible');
   await tap('outbox.reminder-move-open',control('Later…',sheet('Reminder'))||control('Change…',sheet('Reminder')),()=>!!sheet('Move the reminder'),'reminder reschedule sheet visible');
   await tap('outbox.reminder-preset',[...sheet('Move the reminder').querySelectorAll('label')].find(l=>l.textContent.includes('In 2 hours'))?.querySelector('input'),()=>[...sheet('Move the reminder').querySelectorAll('label')].find(l=>l.textContent.includes('In 2 hours'))?.querySelector('input')?.checked,'future reminder preset selected');
   await tap('outbox.reminder-move-save',control('Move it'),()=>!sheet('Move the reminder')&&!overview.reminders[0].overdue,'rescheduled reminder clears old attention');await retire();
   if(sheet('Reminder'))await closeSheet('outbox.reminder-detail-close','Reminder');
   const done=control(/^Reminder done:/),id=overview.reminders[0].message;await tap('outbox.reminder-done',done,()=>!overview.reminders.some(r=>r.message===id)&&!control('Reminder done: Fixture overdue reminder'),'exact reminder cleared after API result');
   await tap('outbox.remaining-reminder-details',control('Details'),()=>!!sheet('Reminder'),'remaining exact reminder details visible');
   const cancelID=overview.reminders[0].message;await tap('outbox.reminder-cancel',control('Cancel',sheet('Reminder')),()=>!overview.reminders.some(r=>r.message===cancelID)&&visibleText('No reminders pending on this device.'),'exact future reminder cancelled, no reminder remains');

  }
  if(suite==='profile'){
   await settings('Profile');const field=active('#settings-name');await fill('profile.name-edit',field,'Fixture Alice');
   await tap('profile.name-save',control('Save'),()=>me.label==='Fixture Alice'&&!control('Save'),'profile label saved and editor clean');
   if(innerWidth<1024)await tap('profile.back',active('[data-settings-back]'),()=>!!control(/^Your devices/),'settings list visible again');
   await settings('Your devices');
   const details=control('Details');await tap('devices.details',details,()=>details.getAttribute('aria-expanded')==='true','exact device details expanded');
   await tap('devices.link-create',control('Add a device'),()=>!!sheet('Add a device')&&!!active('code[aria-label="Link"]'),'one-use fixture link with QR visible');
   await closeSheet('devices.link-close','Add a device');
   await tap('devices.request-refuse',control('No, refuse it'),()=>!control('No, refuse it')&&overview.links.length===0,'exact pending device request removed after refusal');
   await tap('devices.remove-review',control('Remove Fixture phone'),()=>!!sheet('Remove Fixture phone?'),'specific other device removal confirmation');
   await tap('devices.remove-cancel',control('Keep it'),()=>!sheet('Remove Fixture phone?'),'device removal cancelled');assert(me.devices.length===2,'cancel removed device');await retire();
   await tap('devices.remove-review-again',control('Remove Fixture phone'),()=>!!sheet('Remove Fixture phone?'),'specific removal reviewed again');
   await tap('devices.remove',control('Remove Fixture phone'),()=>me.devices.length===1&&!sheet('Remove Fixture phone?'),'exact other device removed after API result');
  }
  if(suite==='permissions'){
   await settings('Permissions');
   for(const [id,target,confirm,done]of [['questions-off',/^Turn off automatic answers for Vitalii/,'Stop automatic answers',()=>grants.questions.length===0],['questions-on',/^Turn on automatic answers for Vitalii/,'Answer automatically',()=>grants.questions.length===1],['own-tasks-on',/^Turn on tasks without asking for My devices/,'Allow future tasks',()=>grants.tasks.length===1],['own-tasks-off',/^Turn off tasks without asking for My devices/,'Remove permission',()=>grants.tasks.length===0]]){
    await tap('permissions.review-'+id,control(target),()=>!!control(confirm),'explicit scoped grant confirmation visible');
    await tap('permissions.confirm-'+id,control(confirm),()=>done()&&!control(confirm),'exact grant change reflected after API result');await retire();
   }
  }
  if(suite==='agents'){
   await tap('agents.open',button(/^Agents\b/),()=>visibleText('Your agents'),'agents page visible');
   await until(()=>control('Change default setting'),'agent data loaded');
   await tap('agents.default-open',control('Change default setting'),()=>!!active('[role="radiogroup"]'),'default choices visible');
   const manual=active('[role="radiogroup"]')?.querySelector('[role="radio"]');await tap('agents.default-manual-choice',manual,()=>manual.getAttribute('aria-checked')==='true','manual default selected locally');
   await tap('agents.default-cancel',control('Cancel'),()=>!!control('Change default setting'),'default edit cancelled');assert(!responder.manual,'cancel changed saved responder');
   await tap('agents.default-reopen',control('Change default setting'),()=>!!active('[role="radiogroup"]'),'default choices reopened');
   const noAgent=active('[role="radiogroup"]').querySelector('[role="radio"]');await tap('agents.default-manual-rechoose',noAgent,()=>noAgent.getAttribute('aria-checked')==='true','explicit manual default selected');
   await tap('agents.default-manual-save',control('Save'),()=>responder.manual&&!!control('Change default setting'),'manual default saved, no harness execution');
   await tap('agents.default-program-open',control('Change default setting'),()=>!!active('[role="radiogroup"]'),'installed program default choices visible');
   const codex=[...active('[role="radiogroup"]').querySelectorAll('[role="radio"]')].find(e=>e.closest('label')?.querySelector('input[type=radio]')?.value==='codex');await tap('agents.default-program-choose',codex,()=>codex.getAttribute('aria-checked')==='true','same installed Codex program selected');
   const folder=control('Choose folder: Works in');if(folder){await tap('agents.default-folder-open',folder,()=>!!sheet('Choose a folder'),'default working-folder browser visible');await until(()=>control(/^Use “/),'default folder loaded');await tap('agents.default-folder-choose',control(/^Use “/),()=>!sheet('Choose a folder'),'default folder choice returned');await retire();}
   await tap('agents.default-program-save',control('Save'),()=>!responder.manual&&responder.harness==='codex'&&!!control('Change default setting'),'original installed program restored as default');
   await tap('agents.new-open',control('New agent'),()=>!!sheet('New agent'),'new named agent sheet visible');
   await fill('agents.new-name',active('#agent-name'),'Fixture Pi');
   await tap('agents.folder-open',control('Choose folder: Works in'),()=>!!sheet('Choose a folder'),'read-only folder browser visible');
   await until(()=>control(/^Use “/),'folder loaded');await tap('agents.folder-select',control(/^Use “/),()=>!sheet('Choose a folder')&&!!control('Change folder: Works in'),'selected folder returned to agent editor');await retire();
   await tap('agents.new-create',control('Create agent'),()=>catalog.agents.length===2&&!sheet('New agent'),'named agent saved without execution');await retire();
   const article=()=>[...root.querySelectorAll('article')].find(a=>a.textContent.includes('Fixture Pi'));
   const toggle=()=>article()?.querySelector('[role="switch"]');
   await tap('agents.disable-review',toggle(),()=>!!control('Turn off'),'disable confirmation visible');
   await tap('agents.disable',control('Turn off'),()=>!catalog.agents[1].enabled&&!article(),'specific agent disabled and removed from active list');await retire();
   await tap('agents.connect-open',control('Connect an agent'),()=>visibleText('Set up your agents')||visibleText('Choose your agents'),'native setup wizard or concrete unavailable state visible');
   await until(()=>control('Manage saved setups'),'retained setup controls loaded');const manage=control('Manage saved setups');await tap('agents.saved-setups-open',manage,()=>manage.getAttribute('aria-expanded')==='true','saved exact setups expanded');
   const saved=()=>root.querySelector('[data-agent-setup="'+catalog.agents[1].record.id+'"]');await tap('agents.repair-open',control('Repair or enable',saved()),()=>!!sheet('Turn on Fixture Pi'),'exact disabled setup repair editor visible');
   await tap('agents.repair-enable',control('Turn on',sheet('Turn on Fixture Pi')),()=>catalog.agents[1].enabled&&!sheet('Turn on Fixture Pi')&&saved()?.textContent.includes('Enabled · Ready'),'same retained agent ID enabled');await retire();
   await tap('agents.make-current',control('Use as current',saved()),()=>responder.agent_id===catalog.agents[1].record.id&&saved()?.textContent.includes('Current'),'exact retained setup becomes current');
   const checkbox=active('input[aria-label="Set up Codex"]');if(!checkbox.checked)await tap('agents.setup-choose',checkbox,()=>checkbox.checked,'installed program selected for native setup review');
   reports.setupReviewInput={dispatch:'synthetic DOM click; does not perform trusted pointer focus',activeTag:shadow.activeElement?.tagName,activeText:shadow.activeElement?.textContent,activeHeading:shadow.activeElement?.matches('h3')||false};
   const reviewButton=control('Review changes');reviewButton.scrollIntoView({block:'nearest'});await frame();
   await measure('agents.setup-review-focus',()=>reviewButton.focus({preventScroll:true}),()=>shadow.activeElement===reviewButton&&onScreen(reviewButton),'review control explicitly focused before keyboard-style activation; synthetic click alone does not move focus');
   await tap('agents.setup-review',control('Review changes'),()=>onScreen(control('Apply changes')),'reviewed native setup changes with explicit Apply control visible');
   const reviewHeading=[...root.querySelectorAll('h3')].find(h=>h.textContent==='Check the changes');assert(onScreen(reviewHeading)&&shadow.activeElement===reviewHeading,'reviewed step must bring and focus its exact heading on screen');reports.setupReviewScreen={heading:reviewHeading.getBoundingClientRect().toJSON(),apply:control('Apply changes').getBoundingClientRect().toJSON(),headingFocused:true,inputRoute:'explicit focus then synthetic activation; not physical pointer/touch qualification'};
   await tap('agents.setup-apply-error',control('Apply changes'),()=>visibleText('Synthetic native setup refused; real harness configuration required'),'native setup refusal shown, no configuration written');
   const namedBeforeBack=catalog.agents.filter(a=>a.enabled).map(a=>({id:a.record.id,label:a.record.label,harness:a.record.harness})),defaultBeforeBack=responder.agent_id;
   const wizard=()=>active('section[aria-label="Set up your agents"]'),restoredNamed=()=>namedBeforeBack.every(a=>[...root.querySelectorAll('article')].some(row=>row.querySelector('[role="switch"]')&&row.textContent.includes(a.label)));
   await tap('agents.setup-back',control('Back'),()=>!wizard()&&onScreen(control('Connect an agent'))&&shadow.activeElement===control('Connect an agent')&&restoredNamed(),'wizard exits to its exact focused visible Connect opener and original named-agent rows');
   const namedAfterBack=catalog.agents.filter(a=>a.enabled).map(a=>({id:a.record.id,label:a.record.label,harness:a.record.harness}));assert(JSON.stringify(namedAfterBack)===JSON.stringify(namedBeforeBack)&&responder.agent_id===defaultBeforeBack,'Back changed original named agent IDs, harnesses or exact default');
   const returnedHeading=[...root.querySelectorAll('h2')].find(h=>h.textContent==='Your agents');reports.setupReturnContext={namedBefore:namedBeforeBack,namedAfter:namedAfterBack,defaultAgent:responder.agent_id,openerFocused:shadow.activeElement===control('Connect an agent'),opener:control('Connect an agent').getBoundingClientRect().toJSON(),wizardAbsent:!wizard(),pageHeadingObservation:returnedHeading?.getBoundingClientRect().toJSON(),targetCorrection:'actual caller opener focus/visibility and exact restored IDs; page-top heading is an observation, not the Back target'};

  }
  if(suite==='teams'){
   await settings('People lists','People & agent lists');
   const fields=()=>[...root.querySelectorAll('input')].filter(shown);await fill('teams.create-name',fields()[0],'Fixture new list');
   await tap('teams.create',control('Create list'),()=>!!sheet('Fixture new list'),'new list opens after API result');
   const d=()=>sheet('Fixture new list')||sheet('Fixture renamed list');
   const person=d().querySelector('select[aria-label="Person"]');await fill('teams.add-person-choice',person,peer.person);
   await tap('teams.add-person',control('Add person',d()),()=>teams.teams[1].members.includes(peer.person)&&d().textContent.includes('Vitalii'),'selected verified person added');
   const member=()=>[...d().querySelectorAll('ul[aria-label="Members"] li')].find(e=>e.textContent.includes('Vitalii'));
   await tap('teams.manager-review',control('Make manager',member()),()=>!!control('Confirm change'),'exact person manager-role confirmation');
   await tap('teams.manager-add',control('Confirm change'),()=>teams.teams[1].managers.includes(peer.person)&&member().textContent.includes('Manager'),'manager role reflected after API result');await retire();
   await tap('teams.manager-remove-review',control('Take manager role',member()),()=>!!control('Confirm change'),'exact manager removal confirmation');
   await tap('teams.manager-remove',control('Confirm change'),()=>!teams.teams[1].managers.includes(peer.person)&&!!control('Make manager',member()),'other manager removed, own manager remains');await retire();
   await until(()=>!d().querySelector('select[aria-label="Agent"]').disabled,'current agent catalog for list');await fill('teams.agent-choice',d().querySelector('select[aria-label="Agent"]'),catalog.agents[0].record.id);
   await tap('teams.agent-add',control('Add agent',d()),()=>teams.teams[1].agents.length===1&&d().textContent.includes('Fixture Codex'),'exact published agent added without granting execution');
   await tap('teams.use-for-group',control('Select for a conversation…',d()),()=>!!sheet('New group'),'list snapshot opens selected people in new-group review');await retire();
   assert(sheet('New group').textContent.includes('Vitalii'),'list group draft lost exact selected person');await closeSheet('teams.group-cancel','New group');
   await tap('teams.list-reopen',control(/^Fixture new list/),()=>!!d(),'same managed list reopened');
   await tap('teams.person-remove-review',control('Remove',member()),()=>!!control('Confirm change'),'exact person removal confirmation');
   await tap('teams.person-remove',control('Confirm change'),()=>!teams.teams[1].members.includes(peer.person)&&!member(),'person removed from list only');await retire();
   reports.peopleListDOM={personOptions:d().querySelector('select[aria-label="Person"]').options.length,agentOptions:d().querySelector('select[aria-label="Agent"]').options.length,claim:'actual rendered native select rows; count is not isolated cost attribution'};
   const rename=[...d().querySelectorAll('input')].find(e=>e.type!=='checkbox');await fill('teams.rename-text',rename,'Fixture renamed list');
   await tap('teams.rename',control('Rename list',d()),()=>!!sheet('Fixture renamed list')&&teams.teams[1].name==='Fixture renamed list','same list renamed after API result');
   await tap('teams.delete-review',control('Delete list',d()),()=>!!control('Confirm change'),'delete list confirmation visible');
   await tap('teams.delete',control('Confirm change'),()=>teams.teams.length===1&&!sheet('Fixture renamed list'),'exact list removed without changing chats');await retire();
   const q=fields().at(-1);await fill('teams.search',q,'missing list',()=>!control(/^Fixture list/),'list search removes unmatched row');await fill('teams.search-clear',q,'',()=>!!control(/^Fixture list/),'original list restored');
  }
  if(suite==='media'){
   await chat();
   const canvas=document.createElement('canvas');canvas.width=768;canvas.height=512;const ctx=canvas.getContext('2d'),pixels=ctx.createImageData(canvas.width,canvas.height);let n=1;
   for(let i=0;i<pixels.data.length;i+=4){n=(n*1664525+1013904223)>>>0;pixels.data[i]=n>>>24;pixels.data[i+1]=n>>>16;pixels.data[i+2]=n>>>8;pixels.data[i+3]=255;}ctx.putImageData(pixels,0,0);
   const photo=new File([await new Promise(r=>canvas.toBlob(r,'image/png'))],'fixture-photo.png',{type:'image/png'});canvas.width=canvas.height=1;
   // 1.2s 64x48 H264 + AAC, generated by the existing bounded ffmpeg fixture
   // recipe; embedded bytes keep native/browser/Android exports identical.
   const video=new File([Uint8Array.from(atob('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'),c=>c.charCodeAt(0))],'fixture-video.mp4',{type:'video/mp4'});
   reports.mediaInputs=await Promise.all([photo,video].map(async f=>({name:f.name,size:f.size,type:f.type,sha256:await fileHash(f)})));
   const pick=file=>{const el=active('input[type="file"]')||root.querySelector('input[type="file"]');assert(el,'file picker missing');const transfer=new DataTransfer();transfer.items.add(file);Object.defineProperty(el,'files',{configurable:true,value:transfer.files});el.dispatchEvent(new Event('change',{bubbles:true}));};
   const tray=()=>active('ul[aria-label="Files to send"]'),row=()=>tray()?.querySelector('li'),busy=()=>!!control('Cancel preparation',tray()||root);
   await measure('media.photo-select',()=>pick(photo),()=>!!tray()&&!!row()?.querySelector('img'),'real selected photo preview visible');
   await tap('media.photo-smaller',control('Smaller photo',row()),()=>busy()||row()?.textContent.includes('JPEG'),'accurate preparation feedback or completed photo result',()=>!busy()&&row()?.textContent.includes('JPEG'));
   const changed=await(await fetch(row().querySelector('img').src)).blob();reports.photoPrepared={type:changed.type,size:changed.size,sha256:await fileHash(changed)};assert(changed.type==='image/jpeg'&&changed.size<photo.size*.9,'photo preparation did not produce bounded JPEG');
   await tap('media.photo-original',control('Original file',row()),()=>control('Original file',row())?.getAttribute('aria-pressed')==='true'&&row()?.textContent.includes('Original'),'explicit original choice restored');
   const restored=await(await fetch(row().querySelector('img').src)).blob();assert(await fileHash(restored)===reports.mediaInputs[0].sha256,'photo original bytes changed');
   await tap('media.photo-remove',control('Remove fixture-photo.png'),()=>!tray(),'selected photo removed');
   await measure('media.video-select',()=>pick(video),()=>!!row()?.querySelector('video'),'real selected video preview visible');
   const preview=row().querySelector('video');await until(()=>preview.readyState>=1,'actual video metadata');reports.videoOriginalMetadata={duration:preview.duration,width:preview.videoWidth,height:preview.videoHeight};assert(Math.abs(preview.duration-1.2)<.1&&preview.videoWidth===64&&preview.videoHeight===48,'video original metadata changed');
   await tap('media.video-prepare-start',control('Smaller video',row()),()=>busy(),'accurate video preparation progress visible');
   await tap('media.video-prepare-cancel',control('Cancel preparation',row()),()=>!busy()&&control('Original file',row())?.getAttribute('aria-pressed')==='true','cancel leaves original selected');
   const videoRestored=await(await fetch(row().querySelector('video').src)).blob();assert(await fileHash(videoRestored)===reports.mediaInputs[1].sha256,'cancelled video original bytes changed');
   await fill('media.original-send-text',text(),'Exact original video');
   await tap('media.original-send',control('Send'),()=>!tray(),'composer hands off exact original',()=>staged.length===1&&view.messages.some(m=>m.body==='Exact original video'));
   assert(staged[0].sha256===reports.mediaInputs[1].sha256&&staged[0].size===video.size&&staged[0].type==='video/mp4','sent video changed exact original');
   const broken=new File([new Uint8Array([1,2,3,4])],'fixture-unsupported.mp4',{type:'video/mp4'});await measure('media.unsupported-select',()=>pick(broken),()=>!!tray(),'unsupported real File retained in tray');
   const beforeUnsupported=row().querySelector('[role="status"]').textContent;await tap('media.unsupported-prepare',control('Smaller video',row()),()=>busy()||row()?.querySelector('[role="status"]')?.textContent!==beforeUnsupported,'unsupported processing progress or explicit fallback visible',()=>!busy()&&row()?.querySelector('[role="status"]')?.textContent!==beforeUnsupported&&row()?.textContent.toLowerCase().includes('original'));
   const unsupported=await(await fetch(row().querySelector('video').src)).blob();assert(await fileHash(unsupported)===await fileHash(broken),'unsupported fallback original changed');await tap('media.unsupported-remove',control('Remove fixture-unsupported.mp4'),()=>!tray(),'unsupported selection removed');
   reports.mediaProcessing='Photo conversion is actual CPU work and reported separately; video cancellation/unsupported choice measures UI. Actual codec output/audio/orientation/deadline qualification remains earlier media evidence.';
  }
  if(suite==='review'){
   await tap('review.open',button(/^Review/),()=>!!active('section[aria-label="Codex native request"]'),'native request presentation visible');
   const card=active('section[aria-label="Codex native request"]');assert(card.textContent.includes('printf fixture'),'native card exact operation missing');
   await tap('review.native-deny',control('Deny this action',card),()=>card.textContent.includes('Response saved.'),'invented callback response saved, native result explicitly pending');
   assert(!card.textContent.includes('completed successfully'),'invented callback claimed execution');
   const report=overview.review[0].report,renew=()=>{report.items[0].native.token='4'.repeat(32);emit();};renew();await until(()=>!!control('Allow this action')&&!control('Allow this action').disabled,'new exact native token controls');nativeFailure=true;
   await tap('review.native-allow-error',control('Allow this action'),()=>!!control('Retry response'),'lost callback response exposes same-ID retry');
   await tap('review.native-retry',control('Retry response'),()=>!control('Retry response')&&active('section[aria-label="Codex native request"]')?.textContent.includes('Response saved.'),'same callback response saved while native outcome unknown');
   const responses=reports.apiCalls.filter(c=>c.request?.do==='continue'||c.request?.action==='continue').slice(-2);assert(responses.length===2&&responses[0].request.send_id===responses[1].request.send_id&&(responses[0].request.body||responses[0].request.text)===(responses[1].request.body||responses[1].request.text),'native retry changed exact prepared response');
   const groupCard=title=>[...root.querySelectorAll('article')].find(a=>a.textContent.includes(title));
   await tap('review.group-join',control('Join',groupCard('Fixture invitation 0')),()=>!groupCard('Fixture invitation 0')&&!overview.group_invitations.some(g=>g.title==='Fixture invitation 0'),'group acceptance recorded, admission remains separately confirmed');
   await tap('review.group-refuse',control('No thanks',groupCard('Fixture invitation 1')),()=>!groupCard('Fixture invitation 1')&&overview.group_invitations.length===0,'exact group invitation refused');
   await tap('review.device-approve-review',control('Approve device'),()=>!!control('Yes, it’s mine'),'concrete device authorization confirmation');
   await tap('review.device-approve',control('Yes, it’s mine'),()=>overview.links.length===1&&!sheet('Is “Fixture review phone 0” your device?'),'exact device approval recorded in invented host');await retire();
   await tap('review.device-refuse',control('Refuse'),()=>overview.links.length===0&&!control('Refuse'),'other exact device request refused');
   const collapse=control(/^Requests from your agents/);await tap('review.report-collapse',collapse,()=>collapse.getAttribute('aria-expanded')==='false','native request reports collapsed');
   await tap('review.report-expand',collapse,()=>collapse.getAttribute('aria-expanded')==='true'&&!!active('section[aria-label="Codex native request"]'),'native request report restored');
   const details=active('details');if(details)await tap('review.report-details',details.querySelector('summary'),()=>details.open,'report details expanded');
  }
  if(suite==='storage'){
   await settings('Storage');await until(()=>control('Review received cache…'),'storage loaded');
   await until(()=>control('Move existing attachments…'),'typed relay attachment controls loaded');
   await tap('storage.drive-move-review',control('Move existing attachments…'),()=>!!sheet('Move existing relay attachments?'),'encrypted relay move requires concrete review');
   await closeSheet('storage.drive-move-cancel','Move existing relay attachments?');
   await tap('storage.drive-move-review-again',control('Move existing attachments…'),()=>!!sheet('Move existing relay attachments?'),'same relay move reviewed again');
   await tap('storage.drive-move-error',control('Move attachments'),()=>visibleText('Synthetic Drive move unavailable; real relay/Google authorization required'),'local trigger refusal visible; no ciphertext migration claimed');await retire();
   await tap('storage.drive-disconnect-review',control('Disconnect Drive…'),()=>!!sheet('Disconnect this relay’s Drive?'),'workspace-wide disconnect confirmation visible');
   await tap('storage.drive-disconnect-error',control('Disconnect Drive'),()=>visibleText('Synthetic Drive disconnect unavailable; real relay/Google authorization required'),'disconnect API failure shown; connection retained');await retire();

   const quota=[...scope().querySelectorAll('label')].find(l=>l.textContent.includes('Relay quota (GiB)'))?.querySelector('input');
   await fill('storage.invalid-quota',quota,'0',()=>quota.getAttribute('aria-invalid')==='true'&&control('Review limit…')?.disabled,'invalid quota visibly refused');
   await fill('storage.quota-edit',quota,'2',()=>!control('Review limit…')?.disabled,'valid reviewed quota enabled');
   await tap('storage.quota-review',control('Review limit…'),()=>!!sheet('Change everyone’s relay quota?'),'shared quota confirmation visible');
   await tap('storage.quota-save',control('Save relay quota'),()=>storage.remote.usage.quota_bytes===2147483648&&!sheet('Change everyone’s relay quota?')&&[...scope().querySelectorAll('label')].find(l=>l.textContent.includes('Relay quota (GiB)'))?.querySelector('input')?.value==='2','synthetic admin quota saved after explicit confirmation');await retire();
   await tap('storage.cache-review',control('Review received cache…'),()=>!!sheet('Clear this received cache?'),'captured received cache selection visible');
   await tap('storage.cache-cancel',control('Cancel',sheet('Clear this received cache?')),()=>!sheet('Clear this received cache?'),'cache review cancelled without removal');await retire();
   await tap('storage.cache-review-again',control('Review received cache…'),()=>!!sheet('Clear this received cache?'),'captured selection reviewed again');cacheHold=true;
   await tap('storage.cache-clear-start',control('Verify and clear cache'),()=>!!control('Stop after this file'),'per-file verification progress visible');
   await tap('storage.cache-stop',control('Stop after this file'),()=>!!control('Stopping after this file…'),'stop request acknowledged before next file');releaseCache();await until(()=>visibleText('1 file cleared. Stopped before the next file.'),'bounded clear stopped');
   cacheOffline=true;await tap('storage.cache-offline-review',control('Review received cache…'),()=>!!sheet('Clear this received cache?'),'offline candidates reviewed without claiming remote verification');
   await tap('storage.cache-offline-retain',control('Verify and clear cache'),()=>visibleText('0 files cleared. 2 files kept because another copy could not be verified.'),'unverified files explicitly retained');
   relayStorage.can_admin=false;storage.remote.usage.can_change_quota=false;
   await tap('storage.member-relay-refresh',control('Refresh storage status'),()=>!!control('Refreshing…'),'member relay status refresh feedback',()=>!control('Refreshing…')&&!control('Move existing attachments…')&&!control('Disconnect Drive…'));
   await tap('storage.member-quota-refresh',control('Read again'),()=>onScreen(control('Reading…')),'member storage refresh feedback',()=>onScreen(control('Read again'))&&!control('Reading…')&&!control('Review limit…'));
   assert(!control('Review limit…'),'member quota controls exposed');

  }
  if(suite==='workspaces'){
   await settings('Workspaces');await until(()=>control('Save for everyone'),'workspace information loaded');
   const field=[...scope().querySelectorAll('label')].find(l=>l.textContent.includes('Name for everyone'))?.querySelector('input');
   await fill('workspaces.shared-name-edit',field,'Fixture renamed workspace');
   await tap('workspaces.shared-name-save',control('Save for everyone'),()=>workspaceInfo.name==='Fixture renamed workspace'&&visibleText('Everyone now sees “Fixture renamed workspace”.'),'workspace admin name saved after API result');
   await tap('workspaces.shared-name-clear',control('Clear'),()=>workspaceInfo.name===''&&visibleText('The name is cleared:'),'workspace shared name cleared');
   const other=()=>host.workspaces.list().find(w=>w.id==='other');
   const membershipRow=name=>[...root.querySelectorAll('li')].find(li=>li.textContent.includes(name)&&li.querySelector('button'));
   await tap('workspaces.switch-selection',control('Switch'),()=>host.workspaces.active()==='other'&&membershipRow('Fixture other')?.textContent.includes('In use'),'captured membership selection visible; actual transport remount separate');
   await tap('workspaces.switch-selection-back',control('Switch'),()=>host.workspaces.active()==='latency'&&membershipRow('Fixture')?.textContent.includes('In use'),'original captured membership selected again');
   await tap('workspaces.local-name-open',control('Rename Fixture other'),()=>!!sheet('Rename Fixture other'),'local workspace-name editor visible');
   await fill('workspaces.local-name-text',sheet('Rename Fixture other').querySelector('input'),'Fixture local alias');
   await tap('workspaces.local-name-save',control('Save',sheet('Rename Fixture other')),()=>other()?.name==='Fixture local alias'&&!sheet('Rename Fixture other'),'device-local name saved through captured host contract');await retire();
   await tap('workspaces.leave-review',control('Leave Fixture local alias'),()=>!!sheet('Leave Fixture local alias?'),'specific membership departure confirmation');
   await tap('workspaces.leave-cancel',control('Stay'),()=>!sheet('Leave Fixture local alias?'),'departure cancelled and membership retained');assert(other(),'cancel disconnected membership');await retire();
   await tap('workspaces.join-open',control('Join a workspace'),()=>!!sheet('Join a workspace'),'workspace invitation form visible');
   const joining=sheet('Join a workspace');await fill('workspaces.join-invite',joining.querySelector('textarea'),'fixture-invitation');await fill('workspaces.join-device-name',joining.querySelector('input[placeholder="laptop"]'),'fixture-laptop');
   await tap('workspaces.join-error',control('Join',joining),()=>control('Join',joining)?.disabled||visibleText('Synthetic join refused'),'accurate joining progress',()=>visibleText('Synthetic join refused; real server invitation verification required'));await closeSheet('workspaces.join-cancel','Join a workspace');
   await tap('workspaces.reconnect-error',control('Reconnect Fixture closed'),()=>control('Reconnect Fixture closed')?.disabled||visibleText('Synthetic reconnect unavailable'),'accurate reconnect progress',()=>visibleText('Synthetic reconnect unavailable; saved identity retained'));
   if(innerWidth<1024)await tap('workspaces.back',active('[data-settings-back]'),()=>!!control(/^About/),'settings list visible');
   await settings('About');const details=control('Technical details');await tap('about.technical-details',details,()=>details.getAttribute('aria-expanded')==='true','technical details expanded');
   await settings('Notifications');const toggle=()=>active('[role="switch"][aria-label="Notifications"]');
   await tap('notifications.disable',toggle(),()=>toggle()?.getAttribute('aria-disabled')==='true'||toggle()?.getAttribute('aria-checked')==='false','notification preference saving or off feedback',()=>overview.notify.enabled===false&&toggle()?.getAttribute('aria-checked')==='false');
   notificationFailure=true;await tap('notifications.enable-error',toggle(),()=>toggle()?.getAttribute('aria-disabled')==='true','accurate pending before preference failure',()=>visibleText('Synthetic notification preference unavailable')&&toggle()?.getAttribute('aria-checked')==='false');notificationFailure=false;
   await tap('notifications.enable-retry',toggle(),()=>toggle()?.getAttribute('aria-disabled')==='true'||toggle()?.getAttribute('aria-checked')==='true','notification preference saving or on feedback',()=>overview.notify.enabled===true&&toggle()?.getAttribute('aria-checked')==='true');
   const preview=active('#notification-preview');for(const value of ['hidden','sender','full',''])await fill('notifications.preview-'+(value||'default'),preview,value,()=>preview.value===value&&overview.notify.preview===value,'preview choice saved without enabling or requesting native permission');

  }
 }
 if(extended)assert(reports.actions.length>0,'extended workload has no measured actions');
 reports.hostPlatform=host.platform;reports.coverageClaim='Only named actions and seen-control census; unvisited source/role/state cells remain open';
 reports.people=peers.length;reports.retainedMessages=extended?view.messages.length:400;reports.counts=counts;
 const percentile=(values,p)=>{const sorted=[...values].sort((a,b)=>a-b);return sorted.length?sorted[Math.min(sorted.length-1,Math.ceil(sorted.length*p)-1)]:null;};
 reports.families={};for(const family of new Set(reports.actions.map(a=>a.id.split('.')[0]))){const actions=reports.actions.filter(a=>a.id.split('.')[0]===family&&(a.ok||a.stateVerified));const values=actions.map(a=>a.feedbackMs);reports.families[family]={samples:values.length,p50:percentile(values,.5),p95:percentile(values,.95),max:values.length?Math.max(...values):null};}
 reports.routineOver200ms=reports.actions.filter(a=>(a.ok||a.stateVerified)&&a.feedbackMs>200&&!a.id.startsWith('presence.')&&!a.id.startsWith('sync.')).map(({id,feedbackMs,visibleMs,paintMs,frameMaxMs})=>({id,feedbackMs,visibleMs,paintMs,frameMaxMs}));
 reports.smooth=reports.routineOver200ms.length===0;reports.timingMeaning='family percentiles pool different named controls in one pass; not repeated per-control statistical confidence. 2000ms is catastrophic bound only, routine feedback above200ms remains an open smoothness finding.';
 assert(reports.feedbackFailures.length===0,reports.feedbackFailures.join('; '));
 assert(reports.pageErrors.length===0,'Runtime errors: '+reports.pageErrors.join('; '));window.latencyReport={ok:true,...reports};
}catch(e){window.latencyReport={ok:false,error:String(e.message||e)+'\n'+String(e.stack||''),...reports};}
if(!window.__agentnetLatencyInput)fetch('/latency-report?run='+encodeURIComponent(new URL(location.href).searchParams.get('run')||''),{method:'POST',body:JSON.stringify(window.latencyReport)}).catch(()=>{});
