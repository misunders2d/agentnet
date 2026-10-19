// Production skins, public Host API, fictional roots; no relay or real data.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const assets=path.resolve(process.env.AGENTNET_TOPIC_ASSETS||path.join(__dirname,'../static'));
const evidence=process.env.AGENTNET_SCREENSHOTS;
if(evidence)fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const adoptDocument = async (href, base) => {
  const r = await fetch(href);
  if (!r.ok) throw new Error("Could not load the skin’s fonts");
  const parsed = new CSSStyleSheet();
  parsed.replaceSync(await r.text()); // never follows @import
  const kept = new CSSStyleSheet(), inside = new URL(base, location.href);
  for (const rule of parsed.cssRules) {
    if (typeof CSSPropertyRule !== "undefined" && rule instanceof CSSPropertyRule) kept.insertRule(rule.cssText, kept.cssRules.length);
    else if (rule instanceof CSSFontFaceRule) {
      let ok = true;
      const css = rule.cssText.replace(/url\(\s*(["']?)([^"')]*)\1\s*\)/g, (_, q, u) => {
        const abs = new URL(u, new URL(href, location.href));
        if (abs.origin !== inside.origin || !abs.pathname.startsWith(inside.pathname)) ok = false;
        return 'url("' + abs.href + '")';
      });
      if (ok) kept.insertRule(css, kept.cssRules.length);
    }
  }
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, kept];
};
const boot=`
const skin=new URL(location).searchParams.get('skin');
const me={person:'1'.repeat(32),label:'Tester',address:'tester/laptop',state:'self'},peer={person:'2'.repeat(32),label:'Casey',address:'casey/desktop',state:'pinned',devices:[]};
const ids={main:'a'.repeat(64),other:'b'.repeat(64),deleted:'c'.repeat(64),empty:'d'.repeat(64),late:'e'.repeat(64),guest:'f'.repeat(64)},native='3'.repeat(32),otherNative='4'.repeat(32);
const at='2026-10-06T12:00:00Z';
const msg=(id,body,extra={})=>({id,lid:id,dir:'in',from:peer.address,kind:'message',body,at,state:'stored',actions:[],reactions:[],...extra});
const topic=(id,title,conv,state='active')=>({id,conv,title,last:title+' message',last_at:at,count:1,unread:1,state,pending:false,review:0,running:0});
const guests=name=>[{pid:name.toLowerCase().padEnd(32,'a'),state:'active',host:{person:name.toLowerCase().padEnd(32,'a'),label:name,address:name.toLowerCase()+'/phone',state:'pinned'},inviter:me,shared:[],can_send:true}];
const views={
 [ids.main]:{id:ids.main,created:'2026-01-01T00:00:00Z',messages:[msg('main-turn','Main planning'),msg('waiting-turn','Waiting recipient proof',{dir:'out',from:me.address,delivery:'waiting',state:'waiting',state_text:'Waiting for Casey to update AgentNet',detail:'peer_update: casey/old-phone needs group-chat-v1',copies:[{to:'casey/old-phone',person:'Casey',state:'waiting',detail:'casey/old-phone needs group-chat-v1'}]}),msg('main-native','Invoice message',{topic:native})],topics:[topic(native,'Invoice review',ids.main)],guests:guests('Morgan')},
 [ids.other]:{id:ids.other,created:'2026-02-01T00:00:00Z',messages:[msg('other-turn','Other historical flow'),msg('other-native','Archived detail',{topic:otherNative})],topics:[topic(otherNative,'Archived details',ids.other,'archived')],guests:guests('Taylor')},
 [ids.deleted]:{id:ids.deleted,created:'2026-03-01T00:00:00Z',messages:[msg('removed','PRIVATE REMOVED ORIGINAL',{deleted:true})],topics:[],guests:[]},
 [ids.empty]:{id:ids.empty,created:'2026-04-01T00:00:00Z',messages:[],topics:[],guests:[]},
 [ids.late]:{id:ids.late,created:'2025-01-01T00:00:00Z',messages:[msg('late-turn','Recovered older discussion')],topics:[],guests:[]},
 [ids.guest]:{id:ids.guest,created:'2024-01-01T00:00:00Z',role:'human_guest',messages:[msg('guest-turn','Separate guest audience')],topics:[],guests:[]}
};
for(const view of Object.values(views))Object.assign(view,{peer,kind:'dm',mine:true,role:view.role||'member',members:[me,peer],agents:[],frozen:''});
if (${process.env.AGENTNET_UNREAD_FIRST==='1'}) {
 const v=views[ids.main],anchor='6'.repeat(32),done='7'.repeat(32);
 v.messages=[msg('main-turn','Main planning',{unread:true}),msg('main-second','Second Main unread',{unread:true}),
  ...Array.from({length:40},(_,i)=>msg((100+i).toString(16).padStart(32,'0'),'Earlier invoice context '+i,{topic:native})),
  msg(anchor,'First unread outgoing invoice',{topic:native,dir:'out',from:me.address,unread:true,attention:{rev:4,writer:'11111111-11111111-11111111-11111111'}}),
  ...Array.from({length:120},(_,i)=>msg((200+i).toString(16).padStart(32,'0'),'Later invoice context '+i+' '+('body '.repeat(12)),{topic:native,unread:i===80||i===119})),
  ...Array.from({length:3},(_,i)=>msg((400+i).toString(16).padStart(32,'0'),'Done invoice item '+i,{topic:done,unread:true}))];
 v.main_topic={id:'',conv:ids.main,title:'Main',count:2,unread:2,state:'active'};
 v.topics=[{...topic(native,'Invoice review',ids.main),count:161,unread:3},{...topic(done,'Done invoices',ids.main,'done'),count:3,unread:3}];
}
if (${process.env.AGENTNET_TOPIC_UNREAD==='1'}) for(const id of [ids.main,ids.other])for(const m of views[id].messages)if(m.dir==='in')m.unread=true;
if (${process.env.AGENTNET_ADDRESSING==='1'}) {
 const pi='5'.repeat(32),codex='6'.repeat(32);
 views[ids.main].agents=[{pid:pi,agent_id:'7'.repeat(32),state:'active',host:me,inviter:me,invited_by:me,shared:[],host_here:true,member:true,can_ask:false},{pid:codex,agent_id:'8'.repeat(32),state:'active',host:me,inviter:me,invited_by:me,shared:[],host_here:true,member:true,can_ask:false}];
 views[ids.main].messages.push(msg('agent-ask','Are you the reviewer?',{kind:'question',from:me.address,origin:'agent:room',verified_agent:true,agent_author_pid:pi,agent_id:'7'.repeat(32),pid:codex,target:{address:me.address,agent_id:'8'.repeat(32)},state:'delivered',delivery:'delivered',exec:{state:'not_run',host:me.address},job_detail:'not run: originating local run stopped',state_text:'Not run: originating local run stopped'}),msg('approved-task','Review the fictional plan.',{dir:'out',from:me.address,kind:'task',origin:'ui',pid:pi,target:{address:me.address,agent_id:'7'.repeat(32)},proposal:{proposal_id:'fictional-proposal',confirmed_by:me.address,asker:me.address,proposal:'Review the fictional plan.'}}));
}
const summary=v=>({id:v.id,...(${process.env.AGENTNET_PREVIEW_ROUTE==='1'}&&v.messages.length?{last_id:v.messages.at(-1).lid||v.messages.at(-1).id}:{}),created:v.created,peer,kind:v.kind,role:v.role,count:v.messages.length,title:v.messages[0]?.deleted?'Message deleted':v.messages[0]?.body||'',last:v.messages.at(-1)?.deleted?'Message deleted':v.messages.at(-1)?.body||'',last_at:v.id===ids.other?'2026-10-07T12:00:00Z':at,unread:0,held:0,waiting:0,guests:v.guests.length});
const overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:false,controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[ids.main,ids.other,ids.deleted,ids.empty,ids.guest].map(id=>summary(views[id])),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
if (${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'}) {overview.schedules=true;overview.files={max_count:8,max_file:1048576,max_message:2097152};}
function unreadCounts(){for(const v of Object.values(views)){if(v.main_topic)v.main_topic.unread=v.messages.filter(m=>m.unread&&!m.topic).length;for(const t of v.topics)t.unread=v.messages.filter(m=>m.unread&&m.topic===t.id).length;const s=overview.dms.find(s=>s.id===v.id);if(s)s.unread=v.messages.filter(m=>m.unread).length;}}
if (${process.env.AGENTNET_TOPIC_UNREAD==='1'}||${process.env.AGENTNET_UNREAD_FIRST==='1'})unreadCounts();
if (${process.env.AGENTNET_UNREAD_FIRST==='1'})overview.dms.find(d=>d.id===ids.main).last_at='2026-12-01T12:00:00Z';
if (${process.env.AGENTNET_PREVIEW_ROUTE==='1'}) overview.dms.sort((a,b)=>b.last_at.localeCompare(a.last_at));
if (${process.env.AGENTNET_UNREAD_REVIEW==='1'}) {
 const v=views[ids.main],closed='7'.repeat(32),fp='aaaaaaaa-bbbbbbbb-cccccccc-dddddddd';
 v.kind='group';v.title='Fictional review group';
 v.messages=Array.from({length:263},(_,i)=>msg((1000+i).toString(16).padStart(32,'0'),'Review item '+i,{topic:i<2?'':i>=260?closed:native,unread:true,send_group_author:fp,attention:{rev:1,writer:fp}}));
 v.main_topic={id:'',conv:v.id,title:'Main',count:2,unread:2,state:'active'};
 v.topics=[{...topic(native,'Review topic',v.id),count:258,unread:258,renamed:true,auto_title:'Automatic review title'},{...topic(closed,'Archived review',v.id,'archived'),count:3,unread:3},...Array.from({length:258},(_,i)=>({...topic((500+i).toString(16).padStart(32,'0'),'Extra choice '+i,v.id),unread:0}))];
 // Provider-shaped projection from the already tested long-mention contract:
 // no parser is added here; actual native/Engine parsing is separate evidence.
 const raw='[@Amazon_team](agentnet:collective/v1/'+Array.from({length:1000},(_,i)=>'p.'+i.toString(16).padStart(32,'0')).join(',')+') Check inventory';
 overview.dms=[{...summary(v),kind:'group',title:v.title,last:'@Amazon_team Check inventory'}];
 window.longMentionInput={raw,projected:overview.dms[0].last};
 unreadCounts();overview.unread_total=263;overview.alert_total=263;
}
overview.remind=${process.env.AGENTNET_OWN_REMIND==='1'};overview.reminders=JSON.parse(localStorage.getItem('fictional-reminders')||'[]');
if(localStorage.getItem('fictional-late'))overview.dms.push(summary(views[ids.late]));
let changed,open,release;
const requests=[];window.fixture={ids,views,overview,requests,native,delay:'',release:()=>release?.(),open:(...args)=>open(...args),addLate:()=>{localStorage.setItem('fictional-late','1');if(!overview.dms.some(d=>d.id===ids.late))overview.dms.push(summary(views[ids.late]));changed?.({type:'change',seq:++overview.seq});},tick:()=>changed?.({type:'change',seq:++overview.seq})};
fixture.unreadCounts=()=>{unreadCounts();if(${process.env.AGENTNET_UNREAD_REVIEW==='1'})overview.unread_total=overview.dms.reduce((n,d)=>n+d.unread,0);};
fixture.pauseRead=${process.env.AGENTNET_UNREAD_REVIEW==='1'};
fixture.scheduleRows=[];fixture.scheduleStages=new Map();fixture.scheduleStageCount=0;fixture.scheduleAudience={persons:[me.person,peer.person],pids:[views[ids.main].guests[0].pid]};fixture.loseScheduleResponse=true;
fixture.scheduleStage=async file=>{const bytes=new Uint8Array(await file.arrayBuffer()),sha256=[...new Uint8Array(await crypto.subtle.digest('SHA-256',bytes))].map(x=>x.toString(16).padStart(2,'0')).join('');const id='stage-'+(++fixture.scheduleStageCount);fixture.scheduleStages.set(id,{name:file.name,size:file.size,sha256});return id;};
fixture.unreadRows=()=>Object.values(views).flatMap(v=>v.messages.filter(m=>m.unread&&!m.deleted).map(m=>({key:['chat',v.id,m.send_group_author||'author',m.lid||m.id].join(':'),kind:'chat',conv:v.id,topic:m.topic||'',topic_title:m.topic?(v.topics.find(t=>t.id===m.topic)?.title||''):'',id:m.id,lid:m.lid||m.id,author:m.send_group_author||'author',dir:m.dir,at:m.at,label:m.dir==='out'?'You':'Casey',preview:m.body}))).sort((a,b)=>b.at.localeCompare(a.at)||a.key.localeCompare(b.key));
const host={version:1,platform:'daemon',workspace:{id:'r4-fictional',name:'Fictional workspace',address:me.address,realm:'fictional'},workspaces:null,skins:[],skin:{id:skin},skinURL:'/assets/skins/'+skin+'/',listen(fn){changed=fn;return()=>{};},onOpen(fn){open=fn;},onSkinsChange(){return()=>{};},selectSkin(){},file(){throw Error('No fixture files');},stage(file){if(${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'})return fixture.scheduleStage(file);throw Error('No fixture files');},api:async(p,body)=>{
 requests.push({path:p,body:body||null});const url=new URL(p,location.origin);
 if(${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'} && url.pathname==='/api/schedule/audience')return structuredClone(fixture.scheduleAudience);
 if(${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'} && url.pathname==='/api/schedules'){if(url.searchParams.has('id')){const row=fixture.scheduleRows.find(r=>r.id===url.searchParams.get('id'));if(!row)throw Error('No exact fixture schedule');return structuredClone(row);}return structuredClone(fixture.scheduleRows.filter(r=>r.conv===url.searchParams.get('conv')));}
 if(${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'} && url.pathname==='/api/schedule/create'){
  const files=(body.files||[]).map(id=>{const f=fixture.scheduleStages.get(id);fixture.scheduleStages.delete(id);if(!f)throw Error('Consumed fixture stage');return f;});
  const canonical={...body,files},old=fixture.scheduleRows.find(r=>r.id===body.id);
  if(old){if(JSON.stringify(old.canonical)!==JSON.stringify(canonical))throw Object.assign(Error('different payload'),{code:'scheduled_id_conflict'});return structuredClone(old);}
  if(Date.parse(body.due)<=Date.now())throw Error('Future fixture time required');
  const row={...canonical,rev:1,state:'pending',canonical};fixture.scheduleRows.push(row);
  if(fixture.loseScheduleResponse){fixture.loseScheduleResponse=false;throw Error('Fictional response lost after save');}return structuredClone(row);
 }
 if(${process.env.AGENTNET_COMPOSER_SCHEDULE==='1'} && ['/api/schedule/edit','/api/schedule/cancel','/api/schedule/send_now'].includes(url.pathname)){
  const row=fixture.scheduleRows.find(r=>r.id===body.id);if(!row||row.rev!==body.rev)throw Object.assign(Error('Revision changed'),{code:'scheduled_stale'});
  if(fixture.scheduleConflict){fixture.scheduleConflict=false;row.rev++;throw Object.assign(Error('Revision changed'),{code:'scheduled_stale'});}
  if(url.pathname.endsWith('/cancel'))row.state='cancelled';else if(url.pathname.endsWith('/send_now'))row.state='sent';else Object.assign(row,body);row.rev++;return structuredClone(row);
 }
 if(url.pathname==='/api/remind'){const m=views[ids.main].messages.find(m=>m.id===body.id);if(!m)throw Error('Exact stored message required');const r={message:m.id,conv:ids.main,from:m.from,title:m.body,due:new Date(body.due*1000).toISOString(),overdue:false};overview.reminders=overview.reminders.filter(x=>x.message!==m.id).concat(r);localStorage.setItem('fictional-reminders',JSON.stringify(overview.reminders));changed?.({type:'change',seq:++overview.seq});return{note:'Fictional reminder stored.'};}
 if(${process.env.AGENTNET_UNREAD_REVIEW==='1'}&&url.pathname==='/api/unread'){const all=fixture.unreadRows(),from=Number(url.searchParams.get('after')||0),limit=Number(url.searchParams.get('limit')||50),rows=all.slice(from,from+limit);return{rows:structuredClone(rows),total:all.length,seq:overview.seq,content:overview.seq,...(from+rows.length<all.length?{next:String(from+rows.length)}:{})};}
 if(url.pathname==='/api/overview')return structuredClone(overview);
 if((${process.env.AGENTNET_UNREAD_REVIEW==='1'}||${process.env.AGENTNET_TOPIC_UNREAD==='1'}||${process.env.AGENTNET_UNREAD_FIRST==='1'})&&url.pathname==='/api/act'&&body.do==='read'){if(fixture.pauseRead)return{note:'Paused fictional read'};if(fixture.failRead)throw Error('Fictional read acknowledgement unavailable');for(const v of Object.values(views))for(const m of v.messages)if(body.ids.includes(m.id)&&(!m.attention||(body.attention?.[m.id]?.rev===m.attention.rev&&body.attention?.[m.id]?.writer===m.attention.writer))){m.unread=false;delete m.attention;}fixture.unreadCounts();changed?.({type:'change',seq:++overview.seq});return{note:'Read.'};}
 if(url.pathname==='/api/dm'){const id=url.searchParams.get('id');if(id===ids.other&&fixture.holdReopenRead){fixture.reopenReadWaiting=true;await new Promise(resolve=>fixture.finishReopenRead=()=>{fixture.holdReopenRead=false;resolve();});}if(id===fixture.delay){fixture.delay='';await new Promise(resolve=>release=resolve);}if(!views[id])throw Error('Unknown fictional root');return structuredClone(views[id]);}
 if(url.pathname==='/api/dm/send'){const view=views[body.conv];const m=msg(body.id,body.body,{dir:'out',from:me.address,topic:body.topic||'',reply_to:body.reply_to,at:new Date().toISOString()});view.messages.push(m);overview.dms=overview.dms.map(d=>d.id===view.id?summary(view):d);changed?.({type:'change',seq:++overview.seq});return{id:m.id,lid:m.id,state:'delivered'};}
 if(${process.env.AGENTNET_UNREAD_REVIEW==='1'}&&url.pathname==='/api/topics'){
  const q=(url.searchParams.get('q')||'').toLowerCase().trim(),state=url.searchParams.get('state'),from=Number(url.searchParams.get('before')||0),limit=Number(url.searchParams.get('limit')||50);
  const all=(views[url.searchParams.get('conv')]?.topics||[]).filter(t=>(!state||t.state===state)&&q.split(/\\s+/).filter(Boolean).every(w=>(t.title+' '+t.last).toLowerCase().includes(w))).sort((a,b)=>Number(b.unread>0)-Number(a.unread>0)||b.last_at.localeCompare(a.last_at)||b.id.localeCompare(a.id)),topics=all.slice(from,from+limit);
  return {topics:structuredClone(topics),matched:all.length,...(from+topics.length<all.length?{next:String(from+topics.length)}:{})};
 }
 if(url.pathname==='/api/topics'){const rows=(views[url.searchParams.get('conv')]?.topics||[]).filter(t=>!url.searchParams.get('state')||t.state===url.searchParams.get('state'));return{topics:structuredClone(rows),matched:rows.length};}
 if(${process.env.AGENTNET_TOPIC_REOPEN==='1'}&&url.pathname==='/api/topic/reopen'){if(body.conv!==ids.other||body.id!==otherNative||body.count!==1)throw Error('Wrong exact reopen target');await new Promise(resolve=>fixture.finishReopen=resolve);views[ids.other].topics[0].state='active';fixture.holdReopenRead=true;return{note:'Reopened'};}
 if(${process.env.AGENTNET_UNREAD_REVIEW==='1'}&&url.pathname==='/api/topic/rename'){const t=views[body.conv]?.topics.find(t=>t.id===body.id);if(!t)throw Error('Wrong exact rename target');await new Promise(resolve=>fixture.finishRename=resolve);t.title=body.title||t.auto_title;t.renamed=!!body.title;changed?.({type:'change',seq:++overview.seq});return{note:'Shared name changed after acknowledgement.'};}
 if(url.pathname.startsWith('/api/topic/')){const view=views[body.conv],ids=body.ids||[body.id];for(const id of ids){const t=view.topics.find(t=>t.id===id);if(t)t.state=url.pathname.endsWith('/done')?'done':url.pathname.endsWith('/archive')?'archived':'active';}changed?.({type:'change',seq:++overview.seq});return{note:'Fictional topic changed.'};}
 if(url.pathname==='/api/message/react')return{note:'Fictional reaction recorded.'};
 if(p.includes('/groups/invitations'))return[];
 if(p.startsWith('/api/typing/status'))return{send:false,scopes:[]};
 if(p.startsWith('/api/agents'))return{host:${process.env.AGENTNET_ADDRESSING==='1'}?me.address:peer.address,agents:${process.env.AGENTNET_ADDRESSING==='1'}?[{record:{id:'7'.repeat(32),label:'Pi',host:me.address}},{record:{id:'8'.repeat(32),label:'Codex',host:me.address}}]:[],sessions:[],local:${process.env.AGENTNET_ADDRESSING==='1'}};
 return{};
}};
const base='/assets/skins/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
const adoptDocument=${adoptDocument.toString()};
if(manifest.document)await adoptDocument(base+manifest.document,base);
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const root=document.createElement('div');root.className='skin-root';shadow.append(root);await(await import(base+manifest.entry)).mount(root,host);window.ready=true;
`;
const server=http.createServer((req,res)=>{
 const url=new URL(req.url,'http://127.0.0.1');
 if(url.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}
 if(url.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}
 if(url.pathname.startsWith('/assets/')){const file=path.resolve(assets,'.'+url.pathname.slice(7));if(file.startsWith(assets+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 res.statusCode=404;res.end('No fictional route');
});
(async()=>{
 let browser,activePage,activeWidth,activeEvidence;try{
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const skin of (process.env.PERSON_TOPIC_SKINS||'comic').split(','))for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce',isMobile:width===390,hasTouch:width===390});
   await context.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort());
   const page=await context.newPage(),errors=[],consoleIssues=[],consoleLocations=[],failedResponses=[],failedRequests=[],fontResponses=[];
   const network={pageerrors:errors,consoleIssues,consoleLocations,failedResponses,failedRequests,fontResponses};
   page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.stack));page.on('console',m=>{if(['error','warning'].includes(m.type())){consoleIssues.push(m.type()+': '+m.text());consoleLocations.push({type:m.type(),text:m.text(),location:m.location()});}});
   page.on('response',r=>{const request=r.request(),details={url:r.url(),status:r.status(),method:request.method(),resourceType:request.resourceType(),referer:request.headers().referer};if(r.status()>=400)failedResponses.push(details);if(request.resourceType()==='font')fontResponses.push(details);});
   page.on('requestfailed',r=>failedRequests.push({url:r.url(),method:r.method(),resourceType:r.resourceType(),failure:r.failure(),referer:r.headers().referer}));
   page.on('close',()=>{if(evidence)fs.writeFileSync(path.join(evidence,'network-'+skin+'-'+width+'.json'),JSON.stringify(network,null,2));});
   activeEvidence=network;
   activePage=page;activeWidth=width;
   const shot=async(name)=>{if(evidence){await page.waitForTimeout(350);await page.screenshot({path:path.join(evidence,'person-topics-'+skin+'-'+width+'-'+name+'.png')});}};
   const all=()=>page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/});
   const shown=text=>page.locator('[role=log] [data-mid]').filter({hasText:text}).first();
   const field=()=>page.getByRole('textbox',{name:'Message Casey',exact:true});
   const list=()=>page.getByRole('dialog',{name:'All topics',exact:true});
   const edit=async text=>{await field().fill(text);};
   const choose=async(pattern)=>{await all().click();await list().getByRole('button',{name:pattern}).click();};
   await page.goto(origin+'/?skin='+skin);await page.waitForFunction(()=>window.ready);
   if(process.env.AGENTNET_COMPOSER_SCHEDULE==='1'){
    const composer=page.locator('[data-part="composer"][data-conv="'+'a'.repeat(64)+'"]');
    const draftValue=()=>composer.locator('textarea').inputValue();
    await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Casey/}).first().click();
    await composer.waitFor({state:'visible'});
    await field().waitFor();await field().fill('Captured scheduled draft');
    await page.locator('input[type=file]').setInputFiles({name:'captured.txt',mimeType:'text/plain',buffer:Buffer.from('captured exact file bytes')});
    const menu=async name=>{await page.getByRole('button',{name:'Add to message',exact:true}).click();await page.getByRole('menuitem',{name:new RegExp('^'+name)}).click();};
    const create=()=>page.getByRole('dialog',{name:'Schedule message',exact:true}),schedules=()=>page.getByRole('dialog',{name:'Scheduled messages',exact:true});
    await menu('Schedule message');await create().getByText('Approved audience',{exact:true}).waitFor();await create().getByRole('button',{name:'Save schedule',exact:true}).waitFor({state:'visible'});
    await create().getByLabel('Send date',{exact:true}).fill('2030-10-17');await create().getByLabel('Send time',{exact:true}).fill('12:30');await shot('schedule-preview');
    await create().getByRole('button',{name:'Save schedule',exact:true}).click();await create().getByText('Unconfirmed save',{exact:true}).waitFor();
    assert.equal(await draftValue(),'Captured scheduled draft');assert.equal(await page.evaluate(()=>fixture.scheduleRows.length),1);
    await create().getByRole('button',{name:'Check saved state',exact:true}).click();await create().getByText(/Saved state found; confirm with same-ID save/).waitFor();assert.equal(await draftValue(),'Captured scheduled draft','descriptor lookup alone keeps draft');
    await create().getByRole('button',{name:'Close',exact:true}).click();await create().waitFor({state:'hidden'});
    await field().fill('New typing after unconfirmed save');await page.locator('input[type=file]').setInputFiles({name:'new.txt',mimeType:'text/plain',buffer:Buffer.from('new file stays')});
    await menu('Schedule message');await create().getByRole('button',{name:'Retry same save',exact:true}).click();await schedules().waitFor();await schedules().getByText('Captured scheduled draft',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>fixture.scheduleRows.length),1,'lost response retry creates exactly one row');assert.equal(await page.evaluate(()=>fixture.scheduleStageCount),2,'original actual file restaged');assert.equal(await draftValue(),'New typing after unconfirmed save');
    assert.equal(await composer.locator('[aria-label="Files to send"]').getByText('new.txt',{exact:true}).count(),1,'new file retained');assert.equal(await composer.locator('[aria-label="Files to send"]').getByText('captured.txt',{exact:true}).count(),0,'only confirmed captured file cleared');
    const creates=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/schedule/create').map(r=>r.body));assert.equal(creates.length,2);assert.equal(creates[0].id,creates[1].id);assert.notDeepEqual(creates[0].files,creates[1].files);assert.deepEqual({...creates[0],files:undefined},{...creates[1],files:undefined});
    await schedules().getByRole('button',{name:'Edit schedule',exact:true}).click();await schedules().getByLabel('Scheduled message text',{exact:true}).fill('Edited scheduled draft');
    await schedules().getByLabel('Reschedule date',{exact:true}).fill('2030-10-20');await schedules().getByLabel('Reschedule time',{exact:true}).fill('14:45');await page.evaluate(()=>fixture.tick());
    assert.equal(await schedules().getByLabel('Reschedule date',{exact:true}).inputValue(),'2030-10-20');assert.equal(await schedules().getByLabel('Reschedule time',{exact:true}).inputValue(),'14:45');
    await schedules().getByRole('button',{name:'Save schedule changes',exact:true}).click();await schedules().getByText('Edited scheduled draft',{exact:true}).waitFor();
    let editRequest=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/schedule/edit').at(-1).body);assert.equal(editRequest.due,await page.evaluate(()=>new Date('2030-10-20T14:45').toISOString()));assert(!('audience'in editRequest));assert(!('files'in editRequest));assert.equal(editRequest.rev,1);
    await page.evaluate(()=>{fixture.scheduleAudience.persons.push('9'.repeat(32));fixture.tick();});await schedules().getByRole('button',{name:'Edit schedule',exact:true}).click();await schedules().getByRole('button',{name:'Review audience',exact:true}).click();await schedules().getByRole('button',{name:'Confirm reviewed audience',exact:true}).click();await schedules().getByText(/New audience explicitly confirmed/).waitFor();await schedules().getByRole('button',{name:'Save schedule changes',exact:true}).click();
    await schedules().getByRole('button',{name:'Send now',exact:true}).click();await schedules().getByText(/^Queued to send/).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/schedule/send_now').at(-1).body.rev),3);assert.equal(await schedules().getByRole('button',{name:'Edit schedule',exact:true}).count(),0,'queued schedule read only');
    await page.evaluate(()=>{const row=structuredClone(fixture.scheduleRows[0]);Object.assign(row,{id:'7'.repeat(32),state:'pending',rev:5,body:'Cancel this schedule'});fixture.scheduleRows.push(row);fixture.tick();});await schedules().getByText('Cancel this schedule',{exact:true}).waitFor();await page.evaluate(()=>fixture.scheduleConflict=true);await schedules().getByRole('button',{name:'Cancel schedule',exact:true}).click();await schedules().getByText(/The action was not confirmed or the revision changed/).waitFor();await schedules().getByRole('button',{name:'Cancel schedule',exact:true}).click();await schedules().getByText(/^Cancelled/).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/schedule/cancel').at(-1).body.rev),6);
    await page.evaluate(()=>{for(const state of ['dispatching','needs_review'])fixture.scheduleRows.push({...structuredClone(fixture.scheduleRows[0]),id:state.padEnd(32,'0'),state,body:'State '+state,detail:'PRIVATE REMOTE DETAIL'});fixture.tick();});await schedules().getByText(/^Needs review/).waitFor();await schedules().getByText(/^Preparing to send/).waitFor();assert(!(await schedules().innerText()).includes('PRIVATE REMOTE DETAIL'));await shot('schedule-states');
    await schedules().getByRole('button',{name:'Close',exact:true}).click();await schedules().waitFor({state:'hidden'});const listGets=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).length);await page.evaluate(()=>fixture.tick());await page.waitForTimeout(120);assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).length),listGets,'closed list does not refetch');
    await menu('Schedule message');await create().getByLabel('Send date',{exact:true}).fill('2020-01-01');await create().getByLabel('Send time',{exact:true}).fill('12:00');await create().getByRole('button',{name:'Save schedule',exact:true}).click();await create().getByText(/Choose a future time/).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/schedule/create').length),2,'past date refused before POST');await create().getByRole('button',{name:'Close',exact:true}).click();await create().waitFor({state:'hidden'});
    await choose(/Invoice review/);await field().fill('Exact existing topic schedule');await menu('Schedule message');await create().getByLabel('Send date',{exact:true}).fill('2030-10-18');await create().getByLabel('Send time',{exact:true}).fill('14:30');await create().getByRole('button',{name:'Save schedule',exact:true}).click();await schedules().waitFor();await schedules().getByText('Exact existing topic schedule',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.scheduleRows.at(-1).topic),'3'.repeat(32));assert.equal(await draftValue(),'');
    assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/dm/send','/api/send','/api/request/followup','/api/dm/agent/ask'].includes(r.path))),false,'scheduling never calls normal send');assert.equal(await page.evaluate(()=>fixture.scheduleStages.size),0,'all staging handles consumed');assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);assert.deepEqual(errors,[]);assert.deepEqual(consoleIssues,[]);
    fs.writeFileSync(path.join(evidence,'schedule-'+width+'.json'),JSON.stringify(await page.evaluate(()=>({requests:fixture.requests,rows:fixture.scheduleRows,stages:fixture.scheduleStageCount})),null,2));await shot('schedule-existing-topic');console.log('schedule comic '+width+' PASS: exact audience/time/file; response-loss same-ID; lookup keeps draft; changed draft/files; revision edit/cancel/send-now; explicit audience; five states; closed push fence; existing topic; no normal send');await context.close();continue;
   }
   if(process.env.AGENTNET_UNREAD_REVIEW==='1'){
    const mid=i=>(1000+i).toString(16).padStart(32,'0');
    // A returned literal title must render before any destination DM is opened.
    await page.getByRole('button',{name:'Review',exact:true}).click();
    await page.getByRole('list',{name:'Unread messages',exact:true}).getByRole('button',{name:/Fictional review group · Review topic, Casey/}).first().waitFor();
    assert.equal(await page.evaluate(()=>fixture.requests.some(r=>r.path.startsWith('/api/dm?'))),false,'never-opened topic label adds no destination GET');
    await shot('never-opened-topic');await page.getByRole('button',{name:'Chats',exact:true}).click();
    const sidebarRow=page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Fictional review group/}).first();
    await sidebarRow.getByText('@Amazon_team Check inventory',{exact:true}).waitFor();assert(!(await sidebarRow.innerText()).includes('agentnet:collective'),'sidebar displays the compact projected long mention');
    assert((await page.evaluate(()=>window.longMentionInput.raw.length))>10000,'long signed anchor remains distinct from the clipped projection');await shot('compact-long-mention');
    await sidebarRow.click();
    const compose=page.getByRole('textbox',{name:'Message Fictional review group',exact:true});await compose.fill('Kept Review draft');
    if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
    await page.getByRole('button',{name:'Review',exact:true}).click();
    const unread=page.getByRole('list',{name:'Unread messages',exact:true});await unread.locator('li').first().waitFor();
    if(width===1280){
     const id='8'.repeat(32),before=await page.evaluate(()=>fixture.requests.length);
     await page.evaluate(id=>{const v=fixture.views[fixture.ids.main];fixture.pauseRead=false;v.messages.push({id,lid:id,dir:'in',from:'casey/desktop',kind:'message',body:'Visible Review-to-Chats signal',at:'2026-12-03T00:00:00Z',unread:true,send_group_author:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'});fixture.unreadCounts();fixture.tick();},id);
     await page.locator('[data-mid="'+id+'"]').waitFor();await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));
     assert.equal(await page.evaluate(before=>fixture.requests.slice(before).filter(r=>r.path==='/api/act').length,before),0,'Review fences already visible chat rows');
     await page.getByRole('button',{name:'Chats',exact:true}).click();
     await page.waitForFunction(id=>fixture.views[fixture.ids.main].messages.find(m=>m.id===id).unread===false,id);
     const reads=await page.evaluate(before=>fixture.requests.slice(before).filter(r=>r.path==='/api/act'),before);
     assert.equal(reads.length,1,'switching to Chats retries the unchanged visible set once');assert.deepEqual(reads[0].body.ids,[id]);
     assert.equal(await page.evaluate(()=>fixture.views[fixture.ids.main].messages.find(m=>m.body==='Review item 259').unread),true,'unseen topic tail remains unread');
     fs.writeFileSync(path.join(evidence,'review-to-chats.json'),JSON.stringify({before,reads},null,2));
     await page.getByRole('button',{name:'Review',exact:true}).click();
     await page.evaluate(id=>{const v=fixture.views[fixture.ids.main];v.messages=v.messages.filter(m=>m.id!==id);fixture.pauseRead=true;fixture.unreadCounts();fixture.tick();},id);
     await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]').length===50);
    }
    const start=await page.evaluate(()=>fixture.requests.length);
    await shot('unread-review');
    for(let i=0;i<5;i++){const count=await unread.locator('li').count();await page.getByRole('button',{name:'Show more unread messages',exact:true}).click();await page.waitForFunction(count=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]').length>count,count);}
    assert.equal(await unread.locator('li').count(),263,'Review loads beyond200');
    const first=unread.locator('li').first(),firstKey=await first.getAttribute('data-unread-key');await first.getByRole('button').hover();
    await page.evaluate(()=>{const v=fixture.views[fixture.ids.main];v.messages[0].unread=false;v.messages[1].body='Updated linked preview';fixture.unreadCounts();fixture.tick();});
    await first.getByRole('button').getAttribute('aria-disabled').then(async()=>{await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-unread-key] button')?.getAttribute('aria-disabled')==='true');});
    assert.equal(await unread.locator('li').first().getAttribute('data-unread-key'),firstKey,'vanished row stays under pointer');
    await page.getByRole('heading',{name:/^Unread/}).hover();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]').length===262);
    assert.equal(await unread.getByText('Updated linked preview',{exact:true}).count(),1,'text updates in place');
    const focused=unread.locator('li').first();await focused.getByRole('button').focus();const focusKey=await focused.getAttribute('data-unread-key');
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages[2].at='2026-12-01T00:00:00Z';fixture.tick();});
    await unread.locator('time[datetime="2026-12-01T00:00:00Z"]').waitFor();
    assert.equal(await unread.locator('li').first().getAttribute('data-unread-key'),focusKey,'keyboard choice keeps slot');
    await page.getByRole('heading',{name:/^Unread/}).evaluate(n=>{n.tabIndex=-1;n.focus();});
    await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-unread-key]')?.textContent.includes('Review item 2'));
    assert.equal(await page.evaluate(start=>fixture.requests.slice(start).some(r=>r.path==='/api/act'),start),false,'Review/list interaction does not read or accept');
    // The first unread target exercises the actual divider and keyboard entry.
    await unread.getByRole('button',{name:/, Review item 2\. Open message\.$/}).press('Enter');
    await page.locator('[data-mid="'+mid(2)+'"]').waitFor();await page.getByRole('separator',{name:'Unread messages',exact:true}).waitFor();await shot('unread-divider');
    if(width===390)await page.getByRole('button',{name:'Back to Review',exact:true}).click();else await page.getByRole('button',{name:'Review',exact:true}).click();
    await unread.locator('li').first().waitFor();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]').length>200);
    const landingStart=await page.evaluate(()=>fixture.requests.length);
    const target=unread.locator('li').filter({hasText:'Review item 200'}).getByRole('button');await page.evaluate(()=>fixture.pauseRead=false);await target.click();
    const selected=page.locator('[data-mid="'+mid(200)+'"]');await selected.waitFor();
    assert(await selected.evaluate(n=>{const r=n.getBoundingClientRect(),b=n.closest('[role=log]').parentElement.getBoundingClientRect();return r.top<b.bottom&&r.bottom>b.top;}),'explicit Review target is visible');
    assert.equal(await compose.inputValue(),'Kept Review draft');
    await selected.evaluate(e=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));
    const readEvidence=await page.evaluate(({start,target})=>{const log=document.querySelector('#skin').shadowRoot.querySelector('[role=log]'),root=log.parentElement,v=root.getBoundingClientRect(),reserve=parseFloat(log.style.paddingBottom||'0')?parseFloat(log.style.paddingBottom)-24:0,bottom=v.bottom-reserve,height=bottom-v.top;const visible=[...log.querySelectorAll('[data-mid]')].filter(e=>{const r=e.getBoundingClientRect(),overlap=Math.min(r.bottom,bottom)-Math.max(r.top,v.top);return overlap>0&&overlap>=Math.min(r.height/2,height/2)&&Math.min(r.right,v.right)>Math.max(r.left,v.left);}).map(e=>e.getAttribute('data-mid'));const reads=fixture.requests.slice(start).filter(r=>r.path==='/api/act'&&r.body.do==='read');return{target,visible,reads,remaining:fixture.views[fixture.ids.main].messages.filter(m=>m.unread).map(m=>m.id)};},{start:landingStart,target:mid(200)});
    assert(readEvidence.reads.length>0,'visible exact landing produces a read acknowledgement');assert(readEvidence.reads.every(r=>r.body.ids.every(id=>readEvidence.visible.includes(id))),'no offscreen row acknowledgement');assert(readEvidence.remaining.includes(mid(259)),'unseen tail survives exact landing');
    fs.writeFileSync(path.join(evidence,'review-visible-'+width+'.json'),JSON.stringify(readEvidence,null,2));
    if(width===390){await page.getByRole('button',{name:'Back to Review',exact:true}).click();await unread.locator('li').first().waitFor();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]').length>200);await unread.locator('li').filter({hasText:'Review item 259'}).getByRole('button').press('Enter');await compose.waitFor();}
    const group=page.getByRole('navigation',{name:/^Topics with /});await group.getByRole('button',{name:/^All/}).click();
    const panel=list(),topicRows=panel.getByRole('list',{name:'Active topics',exact:true});
    for(let i=0;i<8&&await topicRows.locator('li').count()<250;i++){const count=await topicRows.locator('li').count();await panel.getByRole('button',{name:/^Show more/}).click();await page.waitForFunction(count=>document.querySelector('#skin').shadowRoot.querySelector('ul[aria-label="Active topics"]').children.length>count,count);}
    assert((await topicRows.locator('li').count())>200,'All topics loads beyond200');
    const prior=(await topicRows.locator('li').first().textContent());await topicRows.locator('li').first().getByRole('button').hover();await topicRows.locator('li').first().getByRole('checkbox').check();
    await page.evaluate(()=>{const v=fixture.views[fixture.ids.main],t=v.topics.at(-1);v.messages.filter(m=>m.topic===fixture.native).forEach(m=>m.unread=false);v.messages.push({id:'9'.repeat(32),lid:'9'.repeat(32),dir:'in',from:'casey/desktop',kind:'message',body:'New topic arrival',topic:t.id,at:'2026-12-02T00:00:00Z',unread:true,send_group_author:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'});fixture.unreadCounts();fixture.tick();});
    await panel.getByRole('button',{name:/Extra choice 257.*1 unread/}).waitFor();
    assert((await topicRows.locator('li').count())>200,'push retains loaded topic prefix');assert((await topicRows.locator('li').first().textContent()).includes('Review topic'),'selected ID stays in its slot');
    await panel.getByRole('button',{name:'Cancel selection',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('ul[aria-label="Active topics"] li')?.textContent.includes('Extra choice 257'));
    await shot('unread-topics');
    await panel.getByRole('button',{name:/^Archived/}).click();await panel.getByRole('button',{name:/Archived review/}).waitFor();
    await page.evaluate(()=>{fixture.views[fixture.ids.main].topics.find(t=>t.id===fixture.native).state='done';fixture.tick();});
    await panel.getByRole('button',{name:/^Done/}).click();await panel.getByRole('button',{name:/^Review topic,/}).waitFor();
    await panel.getByRole('button',{name:/^Review topic,/}).click();await panel.waitFor({state:'hidden'});
    await page.getByRole('button',{name:/^Review topic,.*open topic, menu$/}).click();await page.getByRole('menuitem',{name:/^Rename/}).click();
    const rename=page.getByRole('dialog',{name:'Rename topic',exact:true});await rename.getByText('This name changes for everyone with access to this topic. Resetting restores the automatic title for everyone.',{exact:true}).waitFor();
    await rename.getByRole('button',{name:'Use automatic title for everyone',exact:true}).click();await page.waitForFunction(()=>!!fixture.finishRename);
    assert.equal(await page.getByRole('button',{name:/^Automatic review title,.*open topic, menu$/}).count(),0,'shared reset is not optimistic before acknowledgement');
    await page.evaluate(()=>fixture.finishRename());await rename.waitFor({state:'hidden'});await page.getByRole('button',{name:/^Automatic review title,.*open topic, menu$/}).waitFor();
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/rename').at(-1).body.title),'','empty reset uses the shared automatic title');await shot('shared-rename-reset');
    if(width===390){
     await page.setViewportSize({width:320,height:900});
     const compactMenu=page.getByRole('button',{name:/^Automatic review title,.*open topic, menu$/});
     await compactMenu.click();await page.getByRole('menuitem',{name:/^Rename/}).waitFor();
     if(evidence)await page.screenshot({path:path.join(evidence,'topic-menu-320-open.png')});
     await page.keyboard.press('Escape');await page.getByRole('menuitem',{name:/^Rename/}).waitFor({state:'hidden'});
     const geometry=await page.getByRole('navigation',{name:/^Topics with /}).evaluate(nav=>{
      const root=nav.getRootNode(),buttons=[...nav.querySelectorAll('button')],rect=nav.getBoundingClientRect();
      const targets=[['current',b=>b.hasAttribute('data-topic')&&b.getAttribute('aria-haspopup')==='menu'],['Main',b=>/^Main(?:,|$)/.test(b.getAttribute('aria-label')||'')],['All',b=>/^All topics/.test(b.getAttribute('aria-label')||'')],['New',b=>b.getAttribute('aria-label')==='New topic']].map(([name,find])=>{
       const b=buttons.find(find);if(!b)return{name,present:false};const r=b.getBoundingClientRect(),x=r.left+r.width/2,y=r.top+r.height/2,hit=root.elementFromPoint(x,y);
       return{name,present:true,label:b.getAttribute('aria-label'),title:b.title,width:r.width,height:r.height,left:r.left,right:r.right,top:r.top,bottom:r.bottom,enabled:!b.disabled&&b.getAttribute('aria-disabled')!=='true',inside:r.left>=rect.left&&r.right<=rect.right&&r.top>=rect.top&&r.bottom<=rect.bottom,centerHit:!!hit&&(hit===b||b.contains(hit))};
      });return{viewport:innerWidth,pageWidth:document.documentElement.scrollWidth,navWidth:nav.clientWidth,navScrollWidth:nav.scrollWidth,targets};
     });
     if(evidence)fs.writeFileSync(path.join(evidence,'topic-menu-320-geometry.json'),JSON.stringify(geometry,null,2));
     assert.equal(geometry.viewport,320);assert(geometry.pageWidth<=320,'320 page has no overflow: '+JSON.stringify(geometry));assert(geometry.navScrollWidth<=geometry.navWidth,'320 topic nav has no overflow: '+JSON.stringify(geometry));
     for(const t of geometry.targets)assert(t.present&&t.label&&t.width>=44&&t.height>=44&&t.enabled&&t.inside&&t.centerHit,'320 '+t.name+' remains accessible and hit-testable: '+JSON.stringify(geometry));
     await page.setViewportSize({width:390,height:900});
    }
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no page overflow');assert.deepEqual(errors,[]);assert.deepEqual(consoleIssues,[]);
    console.log(JSON.stringify({flow:'Review unread -> exact target -> Back -> stable All topics',skin,width,title:await page.title(),pageerrors:errors,consoleIssues,result:'PASS'}));await context.close();continue;
   }
   await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Casey/}).first().click();
   if(process.env.AGENTNET_UNREAD_FIRST==='1'){
    const anchor='6'.repeat(32),done='7'.repeat(32),tail=(319).toString(16).padStart(32,'0');
    const row=id=>page.locator('[data-mid="'+id+'"]');
    const flag=id=>page.evaluate(id=>fixture.views[fixture.ids.main].messages.find(m=>m.id===id).unread,id);
    await shown('Main planning').waitFor();await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages[0].unread);
    await edit('Preserved invoice draft');const gets=await page.evaluate(()=>fixture.requests.filter(r=>r.path.includes('/api/dm?id='+fixture.ids.main)).length);
    await choose(/Invoice review/);await row(anchor).waitFor();
    await page.waitForFunction(id=>!fixture.views[fixture.ids.main].messages.find(m=>m.id===id).unread,anchor);
    assert.equal(await flag(tail),true,'unseen tail beyond the viewport stays unread');
    assert.equal(await flag((280).toString(16).padStart(32,'0')),true,'second unseen tail stays unread');
    assert.equal(await field().inputValue(),'Preserved invoice draft');
    // Opening All topics can load other roots; selecting the already loaded
    // topic itself must not add another selected-conversation history GET.
    const selectedGets=await page.evaluate(()=>fixture.requests.filter(r=>r.path.includes('/api/dm?id='+fixture.ids.main)).length);
    assert.equal(selectedGets,gets+1,'only the read acknowledgement refreshes the selected view');
    const geometry=await row(anchor).evaluate(n=>{const box=n.closest('[role=log]').parentElement,r=n.getBoundingClientRect(),b=box.getBoundingClientRect();return{top:r.top,bottom:r.bottom,boxTop:b.top,boxBottom:b.bottom,mounted:n.closest('[role=log]').querySelectorAll('[data-mid]').length};});
    assert(geometry.top<geometry.boxBottom&&geometry.bottom>geometry.boxTop,'first unread old target is actually in the viewport');
    assert(geometry.mounted>80,'old target reveals the earlier mounted groups');
    await page.getByRole('separator',{name:'Unread messages',exact:true}).waitFor();
    await page.evaluate(()=>document.fonts.ready);await shot('first-unread');
    await page.evaluate(id=>{const v=fixture.views[fixture.ids.main],m=v.messages.find(m=>m.id===id);m.unread=true;m.attention={rev:5,writer:'22222222-22222222-22222222-22222222'};v.messages.filter(m=>!m.topic).forEach(m=>m.unread=true);v.messages.push({id:'8'.repeat(32),lid:'8'.repeat(32),topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'New tail while reading earlier context',at:'2026-10-07T12:00:00Z',unread:true,actions:[]});fixture.unreadCounts();fixture.tick();},anchor);
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main, 2 unread',exact:true}).waitFor();
    await page.waitForTimeout(160);assert.equal(await flag(anchor),true,'new deliberate generation survives current viewport');
    assert.equal(await flag('8'.repeat(32)),true,'new offscreen arrival stays unread');
    assert.equal(await page.getByRole('separator',{name:'Unread messages',exact:true}).count(),1,'divider remains frozen');
    const after=await row(anchor).evaluate(n=>({top:n.getBoundingClientRect().top,bottom:n.getBoundingClientRect().bottom}));assert(after.top<geometry.boxBottom&&after.bottom>geometry.boxTop,'arrival preserves the earlier viewport');
    await page.evaluate(done=>{const v=fixture.views[fixture.ids.main];v.kind='group';v.title='Fictional group';v.topics.find(t=>t.id===done).state='archived';Object.assign(fixture.overview.dms.find(d=>d.id===v.id),{kind:'group',title:v.title});fixture.tick();},done);
    const group=page.getByRole('navigation',{name:/^Topics with /});
    await group.getByRole('button',{name:'Main'+(width>1000?' flow':'')+', 2 unread',exact:true}).waitFor();
    await group.getByRole('button',{name:/All topics.*3 unread in other topics/}).waitFor();
    await shot('main-and-archived-counts');
    await group.getByRole('button',{name:/^All/}).click();await list().getByRole('button',{name:/^Archived/}).click();
    await list().getByRole('button',{name:/Done invoices.*3 unread/}).click();
    await shown('Done invoice item 0').waitFor();
    await page.waitForFunction(()=>fixture.views[fixture.ids.main].topics.find(t=>t.id==='7'.repeat(32)).unread===0);
    assert.equal(await flag('main-turn'),true,'reading archived topic leaves unseen Main unread');
    assert.equal(await flag(anchor),true,'reading archived topic leaves A deliberate unread');
    await page.evaluate(id=>fixture.open(id,'message',{conv:fixture.ids.main}),tail);await row(tail).waitFor();
    const exact=await row(tail).evaluate(n=>{const r=n.getBoundingClientRect(),b=n.closest('[role=log]').parentElement.getBoundingClientRect();return r.top<b.bottom&&r.bottom>b.top;});assert(exact,'exact notification target wins over first unread');
    assert.equal(await page.getByRole('textbox',{name:'Message Fictional group',exact:true}).inputValue(),'Preserved invoice draft');
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no page overflow');
    assert.equal(await page.evaluate(()=>fixture.requests.some(r=>r.body&&r.path==='/api/act'&&r.body.do!=='read')),false,'navigation never accepts work');
    assert.deepEqual(errors,[]);console.log('v33 first unread '+skin+' '+width+' PASS: old outgoing target, divider, visible-only read, fresh generation, Main/archived counts, exact target/draft');await context.close();continue;
   }
   if(process.env.AGENTNET_TOPIC_REOPEN==='1'){
    await all().click();await list().getByRole('button',{name:/^Archived(?:\s|$)/}).click();await list().getByRole('button',{name:/Archived details/}).click();
    const reopen=page.getByRole('button',{name:'Reopen',exact:true});await reopen.waitFor();
    await reopen.evaluate(button=>{button.click();button.click();});
    await page.waitForFunction(()=>typeof fixture.finishReopen==='function');
    const busy=page.getByRole('button',{name:'Reopening…',exact:true});assert.equal(await busy.isDisabled(),true);
    const calls=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/reopen').length);
    assert.equal(await calls(),1);await page.evaluate(()=>fixture.tick());assert.equal(await busy.isDisabled(),true);
    await page.evaluate(()=>fixture.finishReopen());await page.waitForFunction(()=>fixture.reopenReadWaiting);
    assert.equal(await busy.isDisabled(),true);await busy.evaluate(button=>button.click());assert.equal(await calls(),1);
    await page.evaluate(()=>fixture.finishReopenRead());await busy.waitFor({state:'hidden'});
    assert.equal(await calls(),1);assert.equal(await page.evaluate(()=>fixture.views[fixture.ids.other].topics[0].state),'active');
    assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/act','/api/dm/send'].includes(r.path))),false);
    assert.deepEqual(errors,[]);await context.close();continue;
   }
   if(process.env.AGENTNET_PREVIEW_ROUTE==='1'){
    const exact=(id,text)=>page.locator('[data-mid="'+id+'"]');
    await exact('other-native','Archived detail').waitFor();
    if(process.env.AGENTNET_TOPIC_UNREAD==='1'){
     await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.ids?.includes('other-native')));
     assert.deepEqual(await page.evaluate(()=>[...new Set(fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='read').flatMap(r=>r.body.ids))]),['other-native'],'preview reads only its selected topic; unseen Main/other topics stay unread');
    }
    assert.equal(await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).getAttribute('aria-pressed'),'false','preview opens its archived topic, not Main');
    await shot('preview-target');
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).click();
    await shown('Main planning').waitFor();
    await edit('Draft survives exact preview navigation');
    await page.evaluate(()=>{const m=fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-native');m.id='canonical-native';m.lid='main-native';fixture.open('main-native','message',{conv:fixture.ids.main});});
    await exact('canonical-native','Invoice message').waitFor();
    assert.equal(await field().inputValue(),'Draft survives exact preview navigation','exact focus keeps unsent text');
    // A missing target does not invent a topic or trigger an action. When
    // its history later arrives, the existing change feed resolves it.
    await page.evaluate(()=>fixture.open('delayed-native','message',{conv:fixture.ids.main}));
    await page.waitForTimeout(150);
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.push({id:'delayed-native',lid:'delayed-native',topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'History arrived later',at:'2026-10-06T12:00:00Z',state:'stored',actions:[]});fixture.tick();});
    await exact('delayed-native','History arrived later').waitFor();
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).click();
    await shown('Main planning').waitFor();
    await page.evaluate(()=>fixture.open('cancelled-native','message',{conv:fixture.ids.main}));
    await page.waitForTimeout(150);
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).click();
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.push({id:'cancelled-native',lid:'cancelled-native',topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'Cancelled pending focus',at:'2026-10-06T12:00:00Z',state:'stored',actions:[]});fixture.tick();});
    await page.waitForTimeout(180);await shown('Main planning').waitFor();
    assert.equal(await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).getAttribute('aria-pressed'),'true','explicit same-Main navigation cancels pending exact target');
    await page.evaluate(()=>{fixture.delay=fixture.ids.main;fixture.open('main-native','message',{conv:fixture.ids.main});});
    await page.waitForFunction(()=>fixture.delay==='');
    await page.evaluate(()=>fixture.open('main-turn','message',{conv:fixture.ids.main}));
    await exact('main-turn','Main planning').waitFor();
    await page.evaluate(()=>fixture.release());await page.waitForTimeout(150);
    await exact('main-turn','Main planning').waitFor();
    await page.evaluate(()=>{fixture.delay=fixture.ids.other;fixture.open('other-native','message',{conv:fixture.ids.other});});
    await page.waitForFunction(()=>fixture.delay==='');
    await page.evaluate(()=>fixture.open('main-turn','message',{conv:fixture.ids.main}));
    await exact('main-turn','Main planning').waitFor();
    await page.evaluate(()=>fixture.release());await page.waitForTimeout(180);
    await exact('main-turn','Main planning').waitFor();
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body&&['/api/dm/send','/api/operator/decide','/api/topic/done'].includes(r.path)).length),0,'preview navigation executes no message or permission action');
    await shot('preview-stale-load');assert.deepEqual(errors,[]);console.log('preview route '+skin+' '+width+' PASS: exact latest/archived/Main/copy alias/delayed/repeated/stale-load/draft');await context.close();continue;
   }
   try { await shown(process.env.AGENTNET_ADDRESSING==='1'?'Review the fictional plan.':'Main planning').waitFor(); } catch(e) { await shot('failed');console.error(errors);console.error((await page.evaluate(()=>document.querySelector('#skin').shadowRoot.textContent)).slice(-5000));throw e; }
   if(process.env.AGENTNET_TOPIC_UNREAD==='1'){
    const reads=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='read').flatMap(r=>r.body.ids));
    const readFlag=(conv,id)=>page.evaluate(([conv,id])=>fixture.views[fixture.ids[conv]].messages.find(m=>m.id===id).unread,[conv,id]);
    const closeList=()=>list().getByRole('button',{name:/^(Close|Close all topics|Back to the conversation)$/}).click();
    const row=title=>list().getByRole('button',{name:new RegExp('^'+title)});
    const rowRead=async(title,unread)=>{await row(title).waitFor();await page.waitForFunction(({title,unread})=>{const shadow=document.querySelector('#skin').shadowRoot;const dialog=shadow.querySelector('[role=dialog]');if(!dialog)return false;const button=[...dialog.querySelectorAll('button')].find(b=>(b.getAttribute('aria-label')||b.textContent).startsWith(title));const text=button?.getAttribute('aria-label');return !!text&&text.includes('unread')===unread;},{title,unread});};
    await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='read'));
    assert.deepEqual(await reads(),['main-turn'],'opening Main reads only displayed messages');
    assert.equal(await readFlag('main','main-native'),true,'unseen topic remains unread');
    assert.equal(await readFlag('other','other-turn'),true,'unopened root remains unread');
    await all().click();await rowRead('Invoice review',true);
    await rowRead('Other historical flow',true);
    const beforeSibling=await page.evaluate(()=>fixture.requests.filter(r=>r.path.includes('/api/dm?id='+fixture.ids.other)).length);
    await page.evaluate(()=>{fixture.views[fixture.ids.other].messages.find(m=>m.id==='other-turn').unread=false;fixture.unreadCounts();fixture.tick();});
    await page.waitForFunction(before=>fixture.requests.filter(r=>r.path.includes('/api/dm?id='+fixture.ids.other)).length>before,beforeSibling);
    await rowRead('Other historical flow',false);
    await list().getByRole('button',{name:/Invoice review/}).click();await shown('Invoice message').waitFor();
    await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-native').unread);
    await all().click();await rowRead('Invoice review',false);await closeList();
    const attempts=(await reads()).length;
    await page.evaluate(()=>{fixture.failRead=true;fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-native').unread=true;fixture.unreadCounts();fixture.tick();});
    await page.waitForFunction(before=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='read').flatMap(r=>r.body.ids).length>before,attempts);
    assert.equal(await readFlag('main','main-native'),true,'failed acknowledgement preserves unread');
    await all().click();await rowRead('Invoice review',true);await closeList();
    await page.evaluate(()=>{fixture.failRead=false;fixture.tick();});
    await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-native').unread);
    // A message arriving in Main while its sibling is selected is unseen.
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-turn').unread=true;fixture.unreadCounts();fixture.tick();});
    await all().click();await rowRead('Invoice review',false);await closeList();
    assert.equal(await readFlag('main','main-turn'),true,'hidden Main remains unread');
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).click();await shown('Main planning').waitFor();
    await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-turn').unread);
    // A delayed response from a view already left must not acknowledge it.
    await page.evaluate(()=>{fixture.views[fixture.ids.other].messages.find(m=>m.id==='other-turn').unread=true;fixture.unreadCounts();fixture.delay=fixture.ids.other;fixture.open(fixture.ids.other,'conversation');});
    await page.waitForFunction(()=>fixture.delay==='');
    await page.evaluate(()=>fixture.open(fixture.ids.main,'conversation'));await shown('Main planning').waitFor();
    await page.evaluate(()=>fixture.release());await page.waitForTimeout(180);
    assert.equal(await readFlag('other','other-turn'),true,'stale view response remains unread');
    // Group topic chips change the displayed draft topic without reopening the DM.
    await page.evaluate(()=>{const v=fixture.views[fixture.ids.main];v.kind='group';v.title='Fictional group';v.messages.find(m=>m.id==='main-native').unread=true;Object.assign(fixture.overview.dms.find(d=>d.id===v.id),{kind:'group',title:v.title});fixture.unreadCounts();fixture.tick();});
    const groupNav=page.getByRole('navigation',{name:/^Topics with /,exact:false});
    await groupNav.getByRole('button',{name:/^All/}).waitFor();
    assert.equal(await readFlag('main','main-native'),true,'group sibling stays unread before selection');
    await groupNav.getByRole('button',{name:/^All/}).click();await list().getByRole('button',{name:/Invoice review/}).click();
    await shown('Invoice message').waitFor();await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-native').unread);
    await groupNav.getByRole('button',{name:/^Main(?: flow)?$/,exact:true}).click();await shown('Main planning').waitFor();
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-turn').unread=true;fixture.unreadCounts();fixture.open('missing-group-target','message',{conv:fixture.ids.main});});
    await page.waitForTimeout(120);
    assert.equal(await readFlag('main','main-turn'),true,'absent exact target does not read fallback Main');
    await groupNav.getByRole('button',{name:/^Main(?: flow)?$/,exact:true}).click();
    await page.waitForFunction(()=>!fixture.views[fixture.ids.main].messages.find(m=>m.id==='main-turn').unread);
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.push({id:'missing-group-target',lid:'missing-group-target',topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'Arrived after manual Main choice',at:'2026-10-06T12:00:00Z',state:'stored',unread:true,actions:[]});fixture.unreadCounts();fixture.tick();});
    await page.waitForTimeout(120);await shown('Main planning').waitFor();
    assert.equal(await readFlag('main','missing-group-target'),true,'explicit same Main cancels stale focus and leaves later sibling unseen');
    await shot('read-only-visible-topic');assert.deepEqual(errors,[]);console.log('topic unread '+skin+' '+width+' PASS: visible-only, sibling read refresh, selection, failed retry, late hidden arrival, stale view');await context.close();continue;
   }
   if(process.env.AGENTNET_OWN_REMIND==='1'){
    const own=page.locator('[data-mid="waiting-turn"]');
    {
     if(width===390){await own.getByRole('button',{name:'Message actions',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
     else{await own.locator('[role="group"]').hover();await own.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}
    }
    const sheet=page.getByRole('dialog',{name:'Remind me later',exact:true});await sheet.waitFor();await shot('own-reminder-open');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/remind').length),0,'opening own reminder does not submit');
    await sheet.getByText(/In 30 minutes/).first().click();await sheet.getByRole('button',{name:'Remind me',exact:true}).click();await sheet.waitFor({state:'hidden'});
    const posted=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/remind').map(r=>r.body));assert.equal(posted.length,1);assert.equal(posted[0].id,'waiting-turn');assert(posted[0].due>Math.floor(Date.now()/1000),'future reminder');
    await page.reload();await page.waitForFunction(()=>window.ready);
    await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Casey/}).first().click();
    await shown('Main planning').waitFor();
    const ownAgain=page.locator('[data-mid="waiting-turn"]');
    await ownAgain.getByRole('button',{name:'Change…',exact:true}).click();await page.getByRole('dialog',{name:'Move the reminder',exact:true}).waitFor();await shot('own-reminder-reopened');
    assert.equal(await page.evaluate(()=>fixture.overview.reminders.filter(r=>r.message==='waiting-turn').length),1,'own reminder remains after reload');
    await page.getByRole('dialog',{name:'Move the reminder',exact:true}).getByRole('button',{name:/^(Close|Cancel)$/,exact:true}).click();
    const incoming=page.locator('[data-mid="main-turn"]');
    {if(width===390){await incoming.getByRole('button',{name:'Message actions',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}else{await incoming.locator('[role="group"]').hover();await incoming.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}}
    await page.getByRole('dialog',{name:'Remind me later',exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/remind').length),0,'incoming open unchanged; no accidental second submit');
    assert.deepEqual(errors,[]);console.log('Own/incoming reminder menu PASS '+skin+' '+width);await context.close();continue;
   }
   if(process.env.AGENTNET_ADDRESSING==='1'){
    await page.getByText('To @Codex',{exact:true}).waitFor();
    await page.getByText('You approved Pi’s suggested task',{exact:true}).waitFor();
    await page.getByText(/Not run/).first().waitFor();await page.getByText('not run: originating local run stopped',{exact:true}).first().waitFor();
    const recipient=page.locator('[data-agent-recipient]').filter({hasText:'Codex'});
    assert.equal(await recipient.getAttribute('title'),'6'.repeat(32),'recipient label uses exact request PID');
    const row=await recipient.evaluate(e=>e.closest('.msg,.mc,[data-mid]').innerText);assert(!/Delivered|reply\s*pending/i.test(row),'terminal execution row has no contradictory pending/delivery claim');
    assert.equal(await page.evaluate(()=>fixture.views[fixture.ids.main].messages.find(m=>m.id==='agent-ask').body),'Are you the reviewer?','recipient projection leaves signed body unchanged');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body&&['/api/dm/send','/api/action'].includes(r.path)).length),0,'rendering grants no action');
    await shot('agent-addressing');assert.deepEqual(errors,[]);console.log('Agent addressing/proposal PASS '+skin+' '+width);await context.close();continue;
   }
   assert.equal(await page.getByRole('button',{name:/Conversations \(/}).count(),0,'no competing Conversations navigation');
   const waitingBefore=await page.evaluate(()=>structuredClone(fixture.views[fixture.ids.main].messages.find(m=>m.id==='waiting-turn')));
   {
    await page.locator('[data-mid="waiting-turn"]').getByRole('button',{name:/Waiting for Casey to update AgentNet/}).click();
    const details=page.getByRole('dialog',{name:'Message details',exact:true});await details.waitFor();
    await details.getByText('Technical details',{exact:true}).click();
    assert((await details.innerText()).includes('casey/old-phone needs group-chat-v1'),'waiting Details exposes exact persisted recipient and feature');await shot('waiting-details');
    await details.getByRole('button',{name:'Close',exact:true}).click();await details.waitFor({state:'hidden'});
   }
   assert.deepEqual(await page.evaluate(()=>fixture.views[fixture.ids.main].messages.find(m=>m.id==='waiting-turn')),waitingBefore,'Details does not mutate persisted message or copies');
   await edit('Main draft stays');
   // Closed navigation must not fetch every root after unrelated updates.
   const before=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length);
   await page.evaluate(()=>fixture.tick());await page.waitForTimeout(180);
   const fetched=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).map(r=>new URL(r.path,location.origin).searchParams.get('id')));
   assert(fetched.slice(before).every(id=>id==='a'.repeat(64)),'closed bar has no inactive-root fanout');
   await all().click();await list().getByRole('button',{name:/Other historical flow/}).waitFor();
   assert(!await list().innerText().then(text=>text.includes('PRIVATE REMOVED ORIGINAL')),'deleted original never disclosed');
   await list().getByRole('button',{name:/Empty topic/}).waitFor();await shot('flat-list');
   // Both roots' native topics are siblings, including archived entries.
   await list().getByRole('button',{name:/^Archived(?:\s|$)/}).click();
   await list().getByRole('button',{name:/Archived details/}).waitFor();
   await list().getByRole('button',{name:/^Active(?:\s|$)/}).click();
   await list().getByRole('button',{name:/Other historical flow/}).click();await shown('Other historical flow').waitFor();
   await edit('Other root draft stays');await choose(/Invoice review/);
   await shown('Invoice message').waitFor();
   {await page.getByRole('button',{name:/Invoice review.*open topic, menu/}).click();await page.getByRole('menuitem',{name:'Mark done',exact:true}).click();}
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/topic/done'));
   assert.deepEqual(await page.evaluate(()=>{const {conv,id}=fixture.requests.find(r=>r.path==='/api/topic/done').body;return{conv,id};}),{conv:'a'.repeat(64),id:'3'.repeat(32)},'native action keeps its original root');
   assert.equal(await field().inputValue(),'Main draft stays','root draft preserved when entering native topic');
   await edit('Exact native send');
   await page.getByRole('button',{name:'Send',exact:true}).click();
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/send'));
   assert.deepEqual(await page.evaluate(()=>{const {conv,topic,body}=fixture.requests.find(r=>r.path==='/api/dm/send').body;return{conv,topic,body};}),{conv:'a'.repeat(64),topic:'3'.repeat(32),body:'Exact native send'});
   await choose(/Other historical flow/);assert.equal(await field().inputValue(),'Other root draft stays');
   await page.evaluate(()=>fixture.addLate());await page.waitForTimeout(200);assert.equal(await field().inputValue(),'Other root draft stays','late older history does not replace open draft');
   await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).click();
   await shown('Main planning').waitFor();await choose(/Recovered older discussion/);await shown('Recovered older discussion').waitFor();
   await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>fixture.open('a'.repeat(64),'conversation'));
   await shown('Main planning').waitFor();
   assert.equal(await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).getAttribute('aria-pressed'),'true','persisted Main survives earlier-root recovery and reload');
   await shot('main-reloaded');
   await page.evaluate(()=>fixture.delay=fixture.ids.deleted);await all().click();
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path.includes('/api/dm?id='+fixture.ids.deleted)));
   await page.evaluate(()=>fixture.open(fixture.ids.other,'conversation'));
   await shown('Other historical flow').waitFor();
   await list().waitFor({state:'hidden'});
   await edit('New selection survives older fetch');
   assert.equal(await field().inputValue(),'New selection survives older fetch','new selected root accepts typing while old fetch remains pending');
   await page.evaluate(()=>fixture.release());await page.waitForTimeout(250);
   assert.equal(await field().inputValue(),'New selection survives older fetch','older metadata fetch cannot replace current topic draft');
   assert.equal(await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:'Main',exact:true}).getAttribute('aria-pressed'),'false','older fetch cannot switch exact selected root');
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no page overflow');assert.deepEqual(errors,[]);
   console.log('person topics '+skin+' '+width+' PASS: flat roots/native/archived/empty/deleted; exact send/drafts; late sync/reload; no hidden audience merge');
   await context.close();
  }
 }catch(error){
  if(evidence&&activePage&&!activePage.isClosed()){
   await activePage.screenshot({path:path.join(evidence,'failure-'+activeWidth+'.png')}).catch(()=>{});
   const state=await activePage.evaluate(()=>({requests:fixture.requests,unread:fixture.unreadRows().length,rows:[...document.querySelector('#skin').shadowRoot.querySelectorAll('[data-unread-key]')].map(n=>({key:n.getAttribute('data-unread-key'),disabled:n.querySelector('button')?.getAttribute('aria-disabled')}))})).catch(e=>({captureError:String(e)}));
   fs.writeFileSync(path.join(evidence,'failure-'+activeWidth+'.json'),JSON.stringify({error:String(error),stack:error.stack,actual:error.actual,expected:error.expected,operator:error.operator,network:activeEvidence,state},null,2));
   fs.writeFileSync(path.join(evidence,'failure-'+activeWidth+'.dom.txt'),await activePage.locator('#skin').evaluate(e=>e.shadowRoot.innerHTML).catch(String));
  }throw error;
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
