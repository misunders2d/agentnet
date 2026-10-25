// Production skin packages over the public Host API, with inert local data.
// No relay, identity, model, private globals or external requests.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assets = path.resolve(process.env.AGENTNET_TOPIC_ASSETS || path.join(__dirname, '../static'));
const evidence = process.env.AGENTNET_SCREENSHOTS;
assert(evidence, 'AGENTNET_SCREENSHOTS must be outside the repository');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const boot = `
const kind=new URL(location).searchParams.get('kind'),skin=new URL(location).searchParams.get('skin');
const directMode=${!!process.env.DIRECT_AGENT_ORGANIZE_ONLY};
const renamedMode=${!!process.env.P28_RENAMED_ONLY};
const searchMode=${!!process.env.AGENTNET_ALL_STATUS_SEARCH};
const stopMode=${!!process.env.P11_STOP_ONLY},draftsMode=${!!process.env.P11_DRAFTS_ONLY},pendingMode=${!!process.env.P11_PENDING_ONLY},importMode=${!!process.env.P11_HISTORY_IMPORT_ONLY};
const stopResultMode=${!!process.env.P11_STOP_RESULT_ONLY};
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(label,address,key)=>({person:label.toLowerCase().padEnd(32,'a'),label,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const me=person(renamedMode||directMode?'Atlas':'Sergey',renamedMode||directMode?'atlas/laptop':'sergey/laptop',directMode?'11111111-11111111-11111111-11111111':'own-key'),peer=person(renamedMode||directMode?'Briar':'Vitalii',renamedMode||directMode?'briar/desktop':'vitalii/desktop',directMode?'22222222-22222222-22222222-22222222':'peer-key');me.state='self';
const piBinding={agent_host:peer.address,agent_key:peer.fingerprint,agent_id:'5'.repeat(32)},claudeBinding={...piBinding,agent_id:'6'.repeat(32)};
const topics=Array.from({length:4},(_,i)=>({id:String(i+1).repeat(32),...(kind==='agent'?{peer:peer.address}:{conv}),...(directMode?piBinding:{}),title:'Topic '+(i+1),last:'Topic '+(i+1)+' message',last_at:at,count:1,unread:0,review:0,running:0,waiting:false,pending:false,state:'active',quiet_since:at}));
if(directMode)topics.push({...topics[0],...claudeBinding,id:'9'.repeat(32),title:'Claude separate topic',last:'Separate concrete agent context'});
if(searchMode){for(const [i,state] of ['active','done','archived'].entries())Object.assign(topics[i],{title:'needle '+state,last:'Matching '+state+' topic',state});topics[3].title='Unrelated active topic';}
if(renamedMode){Object.assign(topics[0],{title:'**Literal name**',renamed:true});Object.assign(topics[1],{title:'**Automatic title**',renamed:false});Object.assign(topics[2],{title:String.fromCharCode(92)+'*literal'+String.fromCharCode(92)+'* _code_',renamed:true});}
const message=(i)=>({id:topics[i].id,lid:topics[i].id,from:peer.address,dir:'in',body:'Topic '+(i+1)+' message',kind:'message',at,state:'stored',actions:[],...(kind==='agent'?{to:me.address,author:{label:peer.label,about:'Fixture author'},...(directMode?{from_key:peer.fingerprint,agent_id:topics[i].agent_id}:{})}:{topic:topics[i].id})});
const main={id:'a'.repeat(32),lid:'a'.repeat(32),from:peer.address,dir:'in',body:'Quick main flow message',kind:'message',at,state:'stored',actions:[]};
let thread=kind==='agent'?{id:topics[0].id,peer:peer.address,key:{pinned:peer.fingerprint},approved:false,task_grant:'',topic:topics[0],messages:[message(0)]}:{id:conv,kind:kind==='group'?'group':'dm',title:kind==='group'?'Team launch':'',peer:kind==='group'?{label:'Team launch',address:'',state:''}:peer,role:'member',members:[{...me,admin:true},peer],frozen:'',agents:[],guests:[],topics,messages:[main,...topics.map((_,i)=>message(i))]};
// Pending: requests this person sent to the peer's agent. Neither has a current
// executor word (one none, one stale), so the topic is unconfirmed, not Waiting;
// the DM copy still waits for the peer's program, which the relay lists offline.
const asked=m=>({...m,dir:'out',from:me.address,...(kind==='agent'?{to:peer.address}:{})});
const update={to:peer.address,state:'waiting',detail:'peer_update: named agent capability unavailable: '+peer.address+' cannot synchronize direct-agent history or resolve own requests yet; update all its active AgentNet sessions'};
if(pendingMode){topics[0].pending=true;topics[0].waiting=false;topics[0].unconfirmed=2;topics[0].pending_ids=[topics[0].id,'b'.repeat(32)];thread.messages=[...(kind==='agent'?[]:[main]),asked({...message(0),kind:'task',body:'Earlier unresolved task',target:{address:peer.address,fingerprint:peer.fingerprint},...(kind==='agent'?{}:{copies:[update]})}),asked({...message(0),id:'b'.repeat(32),lid:'b'.repeat(32),kind:'question',body:'Second pending question',exec:{state:'needs_human',host:peer.address,attempt:1,stale:true},target:{address:peer.address,fingerprint:peer.fingerprint}}),{...message(0),id:'d'.repeat(32),lid:'d'.repeat(32),kind:'answer',body:'Unrelated final answer'}];}
const overview={version:'fixture',topic_list:true,seq:1,me:{address:me.address,fingerprint:me.fingerprint},person:me,persons:true,agents:true,files:false,controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:kind==='agent'?topics:[],topics:kind==='agent'?[{peer:peer.address,total:4,archived:searchMode?1:0,archived_unread:0,latest:topics[0]}]:[],dms:kind==='agent'?[]:[{id:conv,kind:thread.kind,title:thread.title,peer:thread.peer,count:5,unread:0}],directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[]};
if(directMode){overview.agent_devices=[peer.address];Object.assign(overview.topics[0],piBinding);overview.topics.unshift({peer:peer.address,...claudeBinding,total:1,archived:3,archived_unread:0,latest:topics[4]});}
if(pendingMode)overview.directory.members[1].presence='offline';
// A loaded direct topic must keep its binding when opening a child request,
// even while another admitted chat is unavailable.
if(pendingMode&&kind==='agent')overview.dms=[{id:conv,kind:'dm',peer,count:1,unread:0}];
if(stopMode){
 const pid='9'.repeat(32),m=asked({...message(0),kind:'question',body:'Exact remote running request',topic:'',pid,target:{address:peer.address,fingerprint:peer.fingerprint},exec:{host:peer.address,state:'running',attempt:2,at:Math.floor(Date.now()/1000)},stop:{host:peer.address,id:topics[0].id,key:me.fingerprint,attempt:2,pending:sessionStorage.getItem('stopQueued')==='1'}});
 thread.messages=[m];if(kind!=='agent')thread.agents=[{pid,host:{...me,address:peer.address},name:'Own agent',state:'active',state_text:'Active',host_here:false,inviter:me,shared:[],tasks_from:[],held:0,missing:0,invited:at,can_ask:true,can_decide:false,can_dismiss:false}];
 if(stopResultMode){const saved=JSON.parse(sessionStorage.getItem('stopResponse')||'null');if(saved){m.exec.stale=true;m.stop.pending=false;m.stop.result=saved;}if(sessionStorage.getItem('confirmedStop')){m.exec.state='cancelled';delete m.stop;}}
}

let destination;
if(importMode){
 main.kind='task';main.attachments=[{index:0,name:'terms.md',size:19,openable:true},{index:1,name:'unavailable.md',size:11,openable:false}];
 overview.files={max_file:1048576,max_message:2097152,max_count:8};
 destination={id:'d'.repeat(64),kind:'group',title:'Launch review',peer:{label:'Launch review',address:'',state:''},role:'member',members:[me,peer],frozen:'',agents:[],guests:[],topics:[{id:'5'.repeat(32),title:'Launch notes',state:'active',count:0}],messages:[]};
 overview.dms.push({id:destination.id,kind:'group',title:destination.title,peer:destination.peer,count:0,unread:0},{id:'e'.repeat(64),kind:'group',title:'Frozen archive',peer:{label:'Frozen archive',address:'',state:''},frozen:'frozen',count:0});
}
if(draftsMode)overview.files={max_file:1048576,max_message:2097152,max_count:8};
window.fixture={overview,thread,requests:[],topics,destination,previews:0,shares:{},directMessages:topics.map((_,i)=>({...message(i),placement:topics[i].id})),directOperations:{}};
const updateDirect=()=>{for(const t of topics)t.count=fixture.directMessages.filter(m=>m.placement===t.id).length;overview.threads=topics.filter(t=>t.count>0);for(const scope of overview.topics)scope.total=overview.threads.filter(t=>t.agent_host===scope.agent_host&&t.agent_key===scope.agent_key&&t.agent_id===scope.agent_id).length;};
fixture.topicCatalog=[...topics,{...topics[0],id:'f'.repeat(32),conv:'d'.repeat(64),peer:'outside/host',title:'needle unauthorized scope'}];let open,changed;
window.refreshFixture=()=>changed?.({type:'change',seq:++overview.seq});

window.confirmStop=()=>{thread.messages[0].exec.state="cancelled";delete thread.messages[0].stop;if(stopResultMode)sessionStorage.setItem('confirmedStop','1');changed?.({type:"change",seq:++overview.seq});};
window.replyStop=(state,refused='',execAttempt=2)=>{const m=thread.messages[0],result={decision:sessionStorage.getItem('stopDecision')||'8'.repeat(32),state,refused,at:1792501200};sessionStorage.setItem('stopResponse',JSON.stringify(result));m.exec={...m.exec,state:'running',attempt:execAttempt,stale:state!=='running'&&execAttempt===2};m.stop={host:peer.address,id:topics[0].id,key:me.fingerprint,attempt:2,pending:false,result};changed?.({type:'change',seq:++overview.seq});};
window.resolvePending=()=>{topics[0].pending_ids=[];topics[0].pending=topics[0].waiting=false;changed?.({type:'change',seq:++overview.seq});};
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P11 local fixture',endpoint:location.origin,address:me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('No fixture files');},file:async()=>{throw Error('No fixture files');},api:async(p,body)=>{
 fixture.requests.push({path:p,body});const url=new URL(p,location.origin);
 if(stopMode&&p==='/api/operator/decide'){sessionStorage.setItem('stopQueued','1');if(stopResultMode){sessionStorage.setItem('stopDecision',body.send_id);sessionStorage.setItem('stopDecisionCount',String(Number(sessionStorage.getItem('stopDecisionCount')||0)+1));}thread.messages[0].stop.pending=true;changed?.({type:'change',seq:++overview.seq});return {note:'Stop queued; host offline.'};}
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(draftsMode&&p.startsWith('/api/dm?')&&url.searchParams.get('id')==='d'.repeat(64))return {...thread,id:'d'.repeat(64),messages:[],topics:[]};
 if(p.startsWith('/api/dm?')){if(pendingMode&&kind==='agent'&&fixture.holdUnrelatedDM)await new Promise(resolve=>fixture.releaseUnrelatedDM=resolve);return structuredClone(importMode&&url.searchParams.get('id')===destination.id?destination:thread);}
 if(directMode&&p.startsWith('/api/thread?')){const id=url.searchParams.get('id'),m=fixture.directMessages.find(m=>m.id===id),topic=topics.find(t=>t.id===(m?.placement||id));return {...thread,id,topic:structuredClone(topic),messages:structuredClone(fixture.directMessages.filter(m=>m.placement===topic.id))};}
 if(directMode&&p.startsWith('/api/topics?')){const fields=['agent_host','agent_key','agent_id'],rows=topics.filter(t=>t.count>0&&t.peer===url.searchParams.get('peer')&&fields.every(key=>!url.searchParams.has(key)||(t[key]||'')===url.searchParams.get(key)));return {topics:structuredClone(rows),matched:rows.length};}
 if(p.startsWith('/api/thread?')){if(pendingMode||stopMode)return structuredClone(thread);const t=topics.find(t=>t.id===url.searchParams.get('id'))||topics[0];return {id:t.id,peer:peer.address,key:{pinned:'peer-key'},approved:false,task_grant:'',topic:structuredClone(t),messages:[t.id==='f'.repeat(32)?window.freshMessage:{...message(topics.indexOf(t))}]};}
 if(searchMode&&p.startsWith('/api/topics?')){const state=url.searchParams.get('state'),q=(url.searchParams.get('q')||'').toLowerCase(),scope=url.searchParams.get('conv'),who=url.searchParams.get('peer');const rows=fixture.topicCatalog.filter(t=>(scope?t.conv===scope:t.peer===who)&&(!state||t.state===state)&&(t.title+' '+t.last).toLowerCase().includes(q));const offset=url.searchParams.get('before')==='page2'?2:0,limit=q?2:Number(url.searchParams.get('limit')||40);return{topics:structuredClone(rows.slice(offset,offset+limit)),matched:rows.length,next:offset+limit<rows.length?'page2':''};}
 if(p.startsWith('/api/topics?')){const state=url.searchParams.get('state'),q=(url.searchParams.get('q')||'').toLowerCase();const rows=topics.filter(t=>(!state||t.state===state)&&t.title.toLowerCase().includes(q));return {topics:structuredClone(rows),matched:rows.length};}
 if(importMode&&p==='/api/history/contribution/preview'){
  const operation=String(++fixture.previews+5).repeat(32),selected=new Set((body.files||[]).map(f=>f.id+':'+f.index));
  const items=body.ids.map(id=>{const m=thread.messages.find(m=>m.id===id);return {id:m.id,lid:m.lid,author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64),from:m.from,sent_at:at,body:m.body,files:(m.attachments||[]).map((f,i)=>({index:i,name:f.name,size:f.size,sha256:'9'.repeat(64),available:!!f.openable,selected:selected.has(m.id+':'+i)}))};});
  return {...body,operation,token:'7'.repeat(64),imported_at:at,body:['History from '+(kind==='dm'?'Vitalii chat':'Team launch'),'Vitalii: Quick main flow message','Vitalii: Topic 1 message'].join(String.fromCharCode(10,10)),items,audience:[{person:me.person,address:me.address,fingerprint:me.fingerprint,label:'Sergey',role:'member'},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii',role:'member'},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii’s Analyst',role:'agent',agent_id:'1'.repeat(32),pid:'a'.repeat(32)},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii’s Reviewer',role:'agent',agent_id:'2'.repeat(32),pid:'b'.repeat(32)}]};
 }
 if(importMode&&p==='/api/history/contribution/apply'){
  if(fixture.rejectStale){fixture.rejectStale=false;throw Error('The selected history or destination audience changed; review the contribution again.');}
  if(!fixture.shares[body.operation]){fixture.shares[body.operation]=1;destination.messages.push({id:body.operation,lid:body.operation,dir:'out',from:me.address,kind:'message',body:body.body,topic:body.topic,at,state:'queued'});}
  if(fixture.loseSuccess){fixture.loseSuccess=false;throw Error('Synthetic lost success response; retry this exact share');}
  return {id:body.operation,lid:body.operation,state:'queued'};
 }
 if(directMode&&p==='/api/topic/organization/preview')return {...body,operation:body.new?body.topic:String(++fixture.previews+5).repeat(32),token:'7'.repeat(64),moves:body.ids.map(id=>({lid:id,author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64),topic:fixture.directMessages.find(m=>m.id===id).placement}))};
 if(directMode&&p==='/api/topic/organization/apply'){if(body.new&&!topics.some(t=>t.id===body.topic))topics.push({...topics[0],id:body.topic,title:body.title,count:0});fixture.directOperations[body.operation]=body;for(const id of body.ids)fixture.directMessages.find(m=>m.id===id).placement=body.topic;updateDirect();changed?.({type:'change',seq:++overview.seq});return {ID:body.operation,LID:body.operation,State:'stored'};}
 if(directMode&&p==='/api/topic/organization/undo'){for(const m of fixture.directOperations[body.lid].moves)fixture.directMessages.find(row=>row.id===m.lid).placement=m.topic;updateDirect();changed?.({type:'change',seq:++overview.seq});return [{ID:body.id,LID:body.id,State:'stored'}];}
 if(p==='/api/topic/organization/preview')return {...body,operation:String(++fixture.previews+5).repeat(32),token:'7'.repeat(64),moves:body.ids.map(id=>({lid:thread.messages.find(m=>m.id===id).lid,author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64),topic:thread.messages.find(m=>m.id===id).topic||''}))};
 if(p==='/api/topic/organization/apply'){if(fixture.rejectOrganizationStale){fixture.rejectOrganizationStale=false;throw Error('The selected messages, topic or audience changed; review the move again.');}for(const id of body.ids){const m=thread.messages.find(m=>m.id===id);m.topic=body.topic;}changed?.({type:'change',seq:++overview.seq});return {id:body.operation,lid:body.operation,state:'queued'};}
 if(p==='/api/topic/organization/undo'){if(fixture.loseUndo){fixture.loseUndo=false;throw Error('Synthetic lost response; retry the same undo.');}return [{id:body.id,lid:body.id,state:'queued'}];}

 if(p==='/api/dm/send'||p==='/api/send'){
  const id='f'.repeat(32),lid='e'.repeat(32),t={id, ...(kind==='agent'?{peer:peer.address}:{conv}),title:body.body,last:body.body,last_at:at,count:1,unread:0,review:0,running:0,waiting:false,pending:false,state:'active',quiet_since:at};
  topics.push(t);const m={...main,id:kind==='agent'?id:lid,lid,from:me.address,dir:'out',body:body.body,topic:kind==='agent'?'':id,...(kind==='agent'?{author:{label:'You',about:'Fixture sender'}}:{})};
  if(kind==='agent'){window.freshMessage=m;overview.threads=topics;}else{thread.messages.push(m);thread.topics=topics;}
  changed?.({type:'change',seq:++overview.seq});return {id:m.id,lid:m.lid,state:'delivered'};
 }
 if(p.startsWith('/api/topic/')){const what=url.pathname.split('/').at(-1),ids=body.ids||[body.id];for(const id of ids){const t=topics.find(t=>t.id===id);if(what==='create'){topics.push({id,conv,title:'Quick main flow message',last:'Quick main flow message',last_at:at,count:1,unread:0,state:'active',pending:false});for(const m of thread.messages)if(m.lid===id)m.topic=id;}else if(t){if(what==='delete'){topics.splice(topics.indexOf(t),1);if(kind!=='agent')thread.messages=thread.messages.filter(m=>m.topic!==id);}else t.state=what==='done'?'done':what==='archive'?'archived':'active';}}if(kind==='agent')overview.threads=topics;else thread.topics=topics;changed?.({type:'change',seq:++overview.seq});return {note:ids.length+' topics changed.'};}
 if(p.includes('/groups/invitations'))return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.startsWith('/api/agents'))return {host:peer.address,agents:directMode?[{record:{id:piBinding.agent_id,host:peer.address,host_key:peer.fingerprint,label:'Pi',harness:'pi'},enabled:true},{record:{id:claudeBinding.agent_id,host:peer.address,host_key:peer.fingerprint,label:'Claude',harness:'claude'},enabled:true}]:[],sessions:[],local:false};return {};
}};
const base='/assets/skins/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync((await(await fetch(base+manifest.document)).text()).replaceAll('url(m/','url('+base+'m/'));document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const root=document.createElement('div');root.className='skin-root';shadow.append(root);await(await import(base+manifest.entry)).mount(root,host);
window.openOther=()=>open("d".repeat(64),"conversation");
window.openChat=()=>open(kind==='agent'?topics[0].id:conv,kind==='agent'?'message':'conversation');window.ready=true;
`;
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://127.0.0.1');
  if (u.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>'); return; }
  if (u.pathname === '/boot.mjs') { res.setHeader('Content-Type', 'text/javascript'); res.end(boot); return; }
  if (u.pathname.startsWith('/assets/')) {
    const file = path.resolve(assets, '.' + u.pathname.slice(7));
    if (file.startsWith(assets + path.sep) && fs.existsSync(file) && fs.statSync(file).isFile()) {
      res.setHeader('Content-Type', file.endsWith('.mjs') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.json') ? 'application/json' : 'application/octet-stream');
      res.end(fs.readFileSync(file)); return;
    }
  }
  res.statusCode = 404; res.end('No fixture route');
});
(async () => {
  let browser;
  const errors = [];let combinations=0;
  try {
    await new Promise(r => server.listen(0, '127.0.0.1', r));
    const origin = 'http://127.0.0.1:' + server.address().port;
    browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
    for (const skin of (process.env.P11_SKINS||'comic').split(',')) for (const kind of (process.env.DIRECT_AGENT_ORGANIZE_ONLY?['agent']:(process.env.P11_ORGANIZE_ONLY||process.env.P11_HISTORY_IMPORT_ONLY||process.env.P11_DRAFTS_ONLY||process.env.P11_MENTIONS_ONLY)?['dm','group']:(process.env.P11_PENDING_ONLY||process.env.P11_STOP_ONLY)?['group','agent']:['dm', 'group', 'agent'])) for (const width of [1280, 390]) {
      const tag = skin + '-' + kind + '-' + width;
      const ctx = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: width < 500, reducedMotion: 'reduce' });
      await ctx.route('**/*', r => new URL(r.request().url()).origin === origin ? r.continue() : r.abort());
      const page = await ctx.newPage(); page.setDefaultTimeout(10000);
      page.on('pageerror', e => errors.push(tag + ': ' + e.stack));if(process.env.AGENTNET_ALL_STATUS_SEARCH==='1'){page.on('console',m=>{if(m.type()==='error')errors.push(tag+': '+m.text());});page.on('response',r=>{if(r.status()>=400)errors.push(tag+': HTTP '+r.status()+' '+r.url());});}
      const shot = async name => { await page.waitForTimeout(400); await page.screenshot({ path: path.join(evidence, tag + '-' + name + '.png') }); };
      const changed = () => page.evaluate(() => fixture.requests.filter(r => r.path.startsWith('/api/topic/')));
      const all = () => page.getByRole('button', { name: process.env.P11_HISTORY_IMPORT_ONLY ? /^All topics/ : /^All(?: topics| \d|$)/ }).first();
      try {
        await page.goto(origin + '/?skin=' + skin + '&kind=' + kind);
        await page.waitForFunction(() => window.ready); await page.evaluate(() => openChat());
        await all().waitFor();
        if (kind !== 'agent' && !process.env.P11_PENDING_ONLY && !process.env.P11_STOP_ONLY) {
          await page.getByText('Quick main flow message', { exact: false }).first().waitFor();
          assert.equal(await page.getByText('Topic 1 message', { exact: false }).isVisible(), false, tag + ': topic is separate from main');
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': page fits');
        await page.evaluate(() => document.fonts.ready); await shot('main');



        if(process.env.AGENTNET_ALL_STATUS_SEARCH==='1'){
          await all().click();const panel=page.getByRole('dialog',{name:'All topics',exact:true}),query=panel.getByRole('searchbox');
          await panel.getByRole('button',{name:/^Done/}).click();await panel.getByRole('list',{name:'Done topics',exact:true}).getByRole('listitem').filter({hasText:'needle done'}).waitFor();
          await query.fill('  needle  ');let results=panel.getByRole('list',{name:'Matching topics',exact:true});await results.waitFor();
          if(kind!=='dm'){await panel.getByRole('button',{name:/^Show more/}).click();await page.waitForFunction(()=>[...document.querySelector('#skin').shadowRoot.querySelectorAll('[role="dialog"] li')].length===3);}
          assert.equal(await results.getByRole('listitem').count(),3,tag+': every status matches');
          for(const state of ['active','done','archived'])await results.getByRole('button',{name:new RegExp('^needle '+state)}).waitFor();
          assert.match(await results.innerText(),/Done/i);assert.match(await results.innerText(),/Archived/i);assert.equal(await results.getByText(/unauthorized/).count(),0);
          await panel.getByText('3 topics match across all statuses',{exact:true}).waitFor();
          const before=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/topics?')).length);
          await panel.getByRole('button',{name:/^Archived/}).click();assert.equal(await results.getByRole('listitem').count(),3,'status tab does not filter search results');
          await query.focus();await query.press('Tab');await shot('all-status-search');
          assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/topics?')).length),before,'search tab selection adds no request');
          const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/topics?')).map(r=>r.path));
          for(const call of calls){const u=new URL(call,'http://fixture');if(u.searchParams.get('q')){assert.equal(u.searchParams.get('state')||'','');assert.equal(u.searchParams.get('q'),'needle');assert.equal(kind==='agent'?u.searchParams.get('peer'):u.searchParams.get('conv'),kind==='agent'?'vitalii/desktop':'c'.repeat(64));}}
          if(kind==='dm')assert.equal(calls.length,0,'flat person topics search never adds native count/query requests');
          else assert(calls.some(p=>new URL(p,'http://fixture').searchParams.get('before')==='page2'),'next page retains all-state scope');
          await query.fill('   ');await panel.getByRole('list',{name:'Archived topics',exact:true}).getByRole('listitem').filter({hasText:'needle archived'}).waitFor();assert.equal(await panel.getByRole('listitem').count(),1);
          await query.fill('needle');await panel.getByRole('list',{name:'Matching topics',exact:true}).waitFor();await panel.getByRole('button',{name:'Clear search',exact:true}).click();await panel.getByRole('list',{name:'Archived topics',exact:true}).waitFor();assert.equal(await panel.getByRole('listitem').count(),1,'clear restores selected Archived tab');
          fs.writeFileSync(path.join(evidence,tag+'-search-requests.json'),JSON.stringify(await page.evaluate(()=>fixture.requests),null,2));await shot('search-cleared');
          combinations++;continue;
        }

        if(process.env.P28_RENAMED_ONLY){
          await all().click();const list=page.getByRole('dialog',{name:'All topics',exact:true});
          const literal=list.getByRole('button',{name:/^\*\*Literal name\*\*/});await literal.waitFor();
          const title=row=>row.locator('span.truncate').first();
          const observed={list:await title(literal).innerText(),automatic:await title(list.getByRole('button',{name:/^\*\*Automatic title\*\*/})).innerText(),escaped:await title(list.getByRole('button',{name:/_code_/})).innerText()};
          await shot('renamed-list');await literal.click();
          const selected=page.locator('[data-part="topic-bar"]').getByRole('button',{name:/^\*\*Literal name\*\*/});await selected.waitFor();observed.chip=await selected.innerText();await shot('renamed-chip');
          fs.writeFileSync(path.join(evidence,tag+'-renamed.json'),JSON.stringify(observed,null,2));
          if(kind==='agent'){
          if(width<500)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
          if(width<500)await page.getByRole('button',{name:'Search chats',exact:true}).click();await page.getByPlaceholder('Search people, agents, groups',{exact:true}).fill('Literal');
          const results=page.getByRole('list',{name:'Matching topics',exact:true});await results.waitFor();
          observed.search=await title(results.getByRole('button',{name:/^\*\*Literal name\*\*/})).innerText();await shot('renamed-search');
          }
          fs.writeFileSync(path.join(evidence,tag+'-renamed.json'),JSON.stringify(observed,null,2));
          assert.equal(observed.list,'**Literal name**',tag+': renamed list title stays literal');
          assert.equal(observed.chip,'**Literal name**',tag+': renamed chip stays literal');
          if(kind==='agent')assert.equal(observed.search,'**Literal name**',tag+': renamed search title stays literal');
          assert.equal(observed.automatic,'Automatic title',tag+': automatic title keeps Markdown projection');
          assert.equal(observed.escaped,'\\*literal\\* _code_',tag+': literal escapes/code markers stay typed');
          console.log(tag+' renamed titles PASS');combinations++;continue;
        }

        if(process.env.P11_STOP_ONLY){
          const stop=page.getByRole('button',{name:'Stop',exact:true});await stop.waitFor();
          if(width<500)await stop.tap();else{await stop.focus();await stop.press('Enter');}
          await page.getByRole('button',{name:'Stop queued',exact:true}).waitFor();
          const decision=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'));
          assert.equal(decision.length,1);assert.equal(decision[0].body.action,'cancel');assert.equal(decision[0].body.expect,'running');assert.equal(decision[0].body.attempt,2);assert.equal(decision[0].body.id,'1'.repeat(32));assert.equal(decision[0].body.report,'');
          assert.equal(await page.evaluate(()=>fixture.thread.messages[0].exec.state),'running','queueing never fabricates stopped');
          await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openChat());
          await page.getByRole('button',{name:'Stop queued',exact:true}).waitFor();
          assert.equal(await page.getByRole('button',{name:'Stop queued',exact:true}).isDisabled(),true,'pending survives reload');
          if(process.env.P11_STOP_RESULT_ONLY){
            const refusal='the request is no longer as you saw it (now answered, attempt 2)';
            await page.evaluate(reason=>replyStop('answered',reason),refusal);
            const finished=()=>page.getByRole('status').filter({hasText:'Host reports finished: answered'}).first();
            await finished().waitFor();assert((await finished().innerText()).includes(refusal),'authoritative refusal reason stays visible');
            assert.equal(await finished().locator('.working-dot,.robot-scan').count(),0,'terminal host snapshot has no working animation');
            assert.equal(await page.getByRole('button',{name:'Stop',exact:true}).count(),0,'terminal snapshot offers no redundant Stop');
            assert.equal(await page.evaluate(()=>fixture.thread.messages[0].exec.state),'running','UI does not promote an ambiguous legacy decision into execution state');
            await shot('stop-host-finished');
            await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openChat());await finished().waitFor();
            assert.equal(await page.getByRole('button',{name:'Stop queued',exact:true}).count(),0,'terminal response survives reload without queued Stop');
            assert.equal(await page.evaluate(()=>sessionStorage.getItem('stopDecisionCount')),'1','reload and terminal display send no new decision');
            await page.evaluate(()=>replyStop('running','the running request changed'));
            await page.getByRole('button',{name:'Stop',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Stop',exact:true}).isDisabled(),false,'genuine running refusal retains explicit action');
            await page.getByText('the running request changed',{exact:true}).waitFor();
            await page.evaluate(()=>replyStop('running'));await page.getByRole('button',{name:'Stopping…',exact:true}).waitFor();
            assert.equal(await page.getByRole('button',{name:'Stopping…',exact:true}).isDisabled(),true,'acknowledged running cancellation is not yet stopped');
            await page.evaluate(()=>replyStop('cancelled'));
            const cancelled=page.getByRole('status').filter({hasText:'Host reports cancelled'}).first();await cancelled.waitFor();assert.equal(await cancelled.locator('.working-dot,.robot-scan').count(),0,'cancelled snapshot has no working animation');
            await shot('stop-host-cancelled');
            await page.evaluate(reason=>replyStop('answered',reason,3),refusal);
            const newer=page.getByRole('status').filter({hasText:'is working'}).first();await newer.waitFor();
            assert(await newer.locator('.working-dot').count()>0,'newer genuine attempt keeps working dots');
            assert.equal(await finished().count(),0,'old decision cannot replace newer current work');
            assert.equal(await page.getByRole('button',{name:'Stop',exact:true}).count(),0,'old-attempt Stop is not offered on newer work');
          }
          await page.evaluate(()=>confirmStop());await page.getByRole('button',{name:'Stop queued',exact:true}).waitFor({state:'detached'});
          if(process.env.P11_STOP_RESULT_ONLY){await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openChat());await all().waitFor();assert.equal(await page.locator('.working-dot,.robot-scan').count(),0,'normal current cancelled status remains settled after reload');assert.equal(await page.evaluate(()=>sessionStorage.getItem('stopDecisionCount')),'1');}
          assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/send','/api/dm/send','/api/agent/ask'].includes(r.path)).length),0,'Stop never sends natural-language work');
          await shot('remote-stop');combinations++;continue;
        }

        if(process.env.P11_MENTIONS_ONLY){
          const composer=page.getByRole('form',{name:'Write a message'}),field=composer.getByRole('textbox');
          const button=composer.getByRole('button',{name:'Mention someone',exact:true});
          await button.waitFor();
          const before=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'||r.path==='/api/agent/ask').length);
          await button.click();await page.getByRole('listbox').waitFor();
          assert.equal(await field.inputValue(),'','opening picker does not edit empty draft');
          await field.press('Escape');assert.equal(await page.getByRole('listbox').count(),0);
          assert.equal(await field.inputValue(),'','Escape keeps draft');
          const ru='Привет, обсудим сегодня';
          await field.fill(ru);await field.evaluate(e=>{e.setSelectionRange(8,8);});
          if(width<500)await button.tap();else await button.click();
          await page.getByRole('option').filter({hasText:'Vitalii'}).first().click();
          assert.equal(await field.inputValue(),'Привет, @Vitalii обсудим сегодня','Cyrillic caret and suffix preserved');
          assert.equal(await field.evaluate(e=>e.selectionStart),17);
          await field.fill('Спасибо всем');await field.evaluate(e=>e.setSelectionRange(8,12));
          await button.focus();await button.press('Enter');await page.getByRole('listbox').waitFor();
          await field.press('ArrowDown');await field.press('Escape');
          assert.equal(await field.inputValue(),'Спасибо всем','keyboard picker escape preserves selection text');
          assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'||r.path==='/api/agent/ask').length),before,'picker never implicitly sends');
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
          await shot('persistent-mention-button');combinations++;continue;
        }

        if(process.env.P11_DRAFTS_ONLY){
          const key='agentnet.messenger.drafts.default',conv='c'.repeat(64);
          const composer=()=>page.getByRole('form',{name:'Write a message'});
          const field=()=>composer().getByRole('textbox');
          const draft=()=>page.evaluate(({key,conv})=>JSON.parse(localStorage.getItem(key)||'{}')[conv],{key,conv});
          const reopen=async()=>{await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openChat());await composer().waitFor();};
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          await field().fill('Unsent existing-topic draft');
          await page.waitForFunction(({key,conv})=>JSON.parse(localStorage.getItem(key)||'{}')[conv]?.text==='Unsent existing-topic draft',{key,conv});
          assert.equal((await draft()).topic,'1'.repeat(32));
          await page.evaluate(()=>openOther());await page.locator('form[data-conv="'+ 'd'.repeat(64)+'"]').waitFor();
          await page.evaluate(()=>openChat());await field().waitFor();assert.equal(await field().inputValue(),'Unsent existing-topic draft');
          assert.equal((await draft()).topic,'1'.repeat(32),'navigation retains destination');
          await reopen();assert.equal(await field().inputValue(),'Unsent existing-topic draft');
          await page.locator('[role=log] [data-mid="'+ '1'.repeat(32)+'"]').waitFor();
          // Restore a real reply draft and verify the rendered quote and target.
          await page.evaluate(({key,conv})=>{const d=JSON.parse(localStorage.getItem(key));d[conv].replyTo='1'.repeat(32);localStorage.setItem(key,JSON.stringify(d));},{key,conv});
          await reopen();await composer().getByRole('button',{name:'Cancel reply',exact:true}).waitFor();
          assert.equal((await draft()).replyTo,'1'.repeat(32));await shot('reply-draft-restored');
          await composer().getByRole('button',{name:'Cancel reply',exact:true}).click();
          await page.getByRole('button',{name:'New topic',exact:true}).click();await field().fill('Unsent new-topic draft');
          await reopen();assert.equal(await field().inputValue(),'Unsent new-topic draft');assert.equal((await draft()).newTopic,true);
          await composer().getByRole('button',{name:'Keep this conversation',exact:true}).waitFor();
          // Missing destination never silently sends to Main or drops a reply.
          await page.evaluate(({key,conv})=>localStorage.setItem(key,JSON.stringify({[conv]:{text:'Keep this draft',topic:'e'.repeat(32),replyTo:'f'.repeat(32)}})),{key,conv});
          await reopen();await composer().getByText('Topic unavailable',{exact:true}).waitFor();await composer().getByText('Reply unavailable',{exact:true}).waitFor();
          assert(await composer().getByRole('button',{name:/^Send/}).isDisabled());await shot('missing-draft-target');
          await composer().getByRole('button',{name:'Cancel unavailable reply',exact:true}).click();await composer().getByRole('button',{name:'Move draft to Main',exact:true}).click();
          assert.equal(await field().inputValue(),'Keep this draft');
          // Files live in memory only, surviving navigation without fake reload persistence.
          await field().fill('');await composer().locator('input[type=file]').setInputFiles({name:'draft-only.txt',mimeType:'text/plain',buffer:Buffer.from('fixture')});
          await composer().getByText('draft-only.txt',{exact:true}).waitFor();
          await page.evaluate(()=>openOther());await page.locator('form[data-conv="'+ 'd'.repeat(64)+'"]').waitFor();await page.evaluate(()=>openChat());
          await composer().getByText('draft-only.txt',{exact:true}).waitFor();
          assert(!(JSON.stringify(await draft())||'').includes('draft-only.txt'),'no serialized File placeholders');
          assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/send'||r.path==='/api/dm/send').length),0,'draft checks send nothing');
          combinations++;console.log('PASS drafts '+tag);await ctx.close();continue;
        }

        if(process.env.P11_HISTORY_IMPORT_ONLY){
          const original=await page.evaluate(()=>JSON.stringify(fixture.thread.messages));
          const mainRow=page.locator('[data-mid="'+ 'a'.repeat(32)+'"]');
          if(width>600){await mainRow.hover();await mainRow.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=mainRow.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByText('1 message selected',{exact:true}).waitFor();
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          await page.locator('[data-mid="'+ '1'.repeat(32)+'"]').click();await page.getByText('2 messages selected',{exact:true}).waitFor();
          const launch=async()=>{await page.getByRole('button',{name:'Continue in a group',exact:true}).click();return page.getByRole('dialog',{name:'Continue in a group',exact:true});};
          const choose=async sheet=>{const select=sheet.getByRole('combobox',{name:'Destination group',exact:true});assert.equal(await select.getByRole('option',{name:'Frozen archive',exact:true}).count(),0);await select.selectOption('d'.repeat(64));await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('5'.repeat(32));assert(await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).isChecked());assert(await sheet.getByRole('checkbox',{name:/unavailable.md/}).isDisabled());};
          let sheet=await launch();await choose(sheet);await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await sheet.getByText('Share with “Launch review”',{exact:true}).waitFor();
          const audience=sheet.getByRole('region',{name:'Reviewed audience',exact:true});assert.match(await audience.innerText(),/Sergey.*member/s);assert.match(await audience.innerText(),/Vitalii’s Analyst.*agent/s);assert.match(await audience.innerText(),/Vitalii’s Reviewer.*agent/s);assert.equal(await audience.getByRole('listitem').count(),4,'Distinct same-host agents both remain in reviewed audience');
          assert.match(await sheet.innerText(),/terms.md.*included/s);await shot('history-review');
          await sheet.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/history/contribution/apply').length),0,tag+': cancel shares nothing');
          sheet=await launch();await choose(sheet);await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).uncheck();await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await page.evaluate(()=>fixture.rejectStale=true);await sheet.getByRole('button',{name:'Share selected history',exact:true}).click();await sheet.getByRole('alert').getByText(/review the contribution again/).waitFor();
          await sheet.getByRole('button',{name:'Review history and audience',exact:true}).waitFor();assert.equal(await sheet.getByRole('button',{name:'Retry this share',exact:true}).count(),0,'Canonical stale rejection permits a fresh review');
          await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).check();await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await page.evaluate(()=>fixture.loseSuccess=true);await sheet.getByRole('button',{name:'Share selected history',exact:true}).click();await sheet.getByRole('alert').getByText(/lost success response/).waitFor();
          assert.equal(await sheet.getByRole('button',{name:'Change selection or destination',exact:true}).count(),0,'Uncertain success retains immutable exact review');await shot('history-uncertain');
          await sheet.getByRole('button',{name:'Retry this share',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const sent=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/history/contribution/apply'));
          assert.equal(sent.length,3);assert.deepEqual(sent[1].body,sent[2].body,'Lost-success retry preserves exact review.operation/token/items/audience');assert.notEqual(sent[0].body.operation,sent[1].body.operation,'Stale rejection obtains a new reviewed operation');
          assert.deepEqual(sent[1].body.ids,['a'.repeat(32),'1'.repeat(32)]);assert.equal(sent[1].body.source,'c'.repeat(64));assert.equal(sent[1].body.destination,'d'.repeat(64));assert.equal(sent[1].body.topic,'5'.repeat(32));assert.deepEqual(sent[0].body.files,[]);assert.deepEqual(sent[1].body.files,[{id:'a'.repeat(32),index:0}]);
          assert.equal(await page.evaluate(()=>JSON.stringify(fixture.thread.messages)),original,'Original tasks/messages/files remain unchanged');assert.deepEqual(await page.evaluate(()=>Object.values(fixture.shares)),[1],'Lost response creates one attributed context message');
          assert.equal(await page.evaluate(()=>fixture.destination.messages[0].kind),'message','Old task contributes context, never executable work');assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'||r.path==='/api/send'||r.path==='/api/act'&&r.body?.do!=='read').length),0,'No old task is sent, accepted or rerun');
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,tag+': reviewed sharing fits narrow view');combinations++;console.log('HISTORY IMPORT PASS '+tag);await ctx.close();continue;
        }

        if(process.env.DIRECT_AGENT_ORGANIZE_ONLY){
          const header=page.locator('[data-part="conversation-head"]');
          await header.getByText('Pi',{exact:true}).waitFor();
          assert.match(await header.innerText(),/Desktop/i,tag+': concrete agent header retains its host device');
          await page.getByRole('textbox',{name:/^Ask .*Pi$/}).waitFor();
          assert.match(await all().innerText(),/4/,tag+': Pi topic count excludes same-host Claude');
          await all().click();
          const scoped=page.getByRole('dialog',{name:'All topics',exact:true});
          await scoped.getByRole('list',{name:'Active topics',exact:true}).getByRole('listitem').nth(3).waitFor();
          assert.equal(await scoped.getByRole('list',{name:'Active topics',exact:true}).getByRole('listitem').count(),4,tag+': scoped server topic page contains only Pi');
          assert.equal(await scoped.getByText('Claude separate topic',{exact:false}).count(),0,tag+': other concrete agent is absent');
          const badgeCounts={active:Number((await scoped.getByRole('button',{name:/^Active/}).innerText()).match(/\d+/)?.[0]||0),archived:Number((await scoped.getByRole('button',{name:/^Archived/}).innerText()).match(/\d+/)?.[0]||0)};
          console.log('DIRECT AGENT BADGE COUNTS '+tag+' '+JSON.stringify(badgeCounts));
          await shot('direct-agent-topic-counts');
          assert.deepEqual(badgeCounts,{active:4,archived:0},tag+': All topics badges belong to the selected Pi full tuple, independent of first same-host Claude summary');
          await scoped.getByRole('button',{name:width>600?'Close':'Back to the conversation',exact:true}).click();await scoped.waitFor({state:'hidden'});
          const row=page.locator('[data-mid="'+ '1'.repeat(32)+'"]');
          if(width>600){await row.hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=page.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();
          let sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          assert.equal(await sheet.getByRole('option',{name:'Claude separate topic',exact:true}).count(),0,tag+': move chooser excludes same-host other agent');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await shot('direct-move-review');
          await sheet.getByRole('button',{name:'Move 1 message',exact:true}).click();
          await sheet.getByRole('button',{name:'Undo move',exact:true}).waitFor();
          assert.equal(await page.evaluate(()=>fixture.overview.topics.find(t=>t.agent_id==='5'.repeat(32)).total),3,tag+': move actually reduces topic count');
          const applied=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply').at(-1));
          assert.equal(applied.body.peer,'briar/desktop');assert.deepEqual(applied.body.ids,['1'.repeat(32)]);assert.equal(applied.body.conv,'');
          assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/send').length),0,tag+': no originals resent');
          await sheet.getByRole('button',{name:'Undo move',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const undone=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/undo').at(-1));
          assert.equal(undone.body.lid,applied.body.operation,tag+': native capitalized result supplies exact undo operation');
          assert.equal(await page.evaluate(()=>fixture.overview.topics.find(t=>t.agent_id==='5'.repeat(32)).total),4,tag+': undo restores original topics');
          await page.getByRole('button',{name:/^Topic 1.*open topic, menu$/}).click();
          await page.getByRole('menuitem',{name:'Merge into another topic…',exact:true}).click();
          sheet=page.getByRole('dialog',{name:'Merge this topic',exact:true});await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await sheet.getByRole('button',{name:'Move 1 message',exact:true}).click();
          await sheet.getByRole('button',{name:'Done',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const merged=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply').at(-1));assert.equal(merged.body.merge,'1'.repeat(32));
          const originalRows=await page.evaluate(()=>fixture.directMessages.map(({placement,...m})=>m));
          const selectedRow=page.locator('[data-mid="'+ '1'.repeat(32)+'"]');
          if(width>600){await selectedRow.hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=selectedRow.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();
          sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('new');
          await sheet.getByRole('textbox',{name:'New topic name',exact:true}).fill('Build decisions');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();
          await sheet.getByText('Move 1 message to “Build decisions”?',{exact:true}).waitFor();
          await shot('direct-new-topic-review');
          await sheet.getByRole('button',{name:'Move 1 message',exact:true}).click();
          await sheet.getByRole('button',{name:'Undo move',exact:true}).waitFor();
          const fresh=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply').at(-1));
          assert.equal(fresh.body.new,true);assert.equal(fresh.body.topic,fresh.body.operation,tag+': new topic uses exact reviewed operation identity');
          assert.equal(fresh.body.title,'Build decisions');
          assert.deepEqual(await page.evaluate(()=>fixture.directMessages.map(({placement,...m})=>m)),originalRows,tag+': new topic preserves every original');
          await sheet.getByRole('button',{name:'Undo move',exact:true}).click();await sheet.waitFor({state:'hidden'});
          assert.equal(await page.evaluate(()=>fixture.overview.topics.find(t=>t.agent_id==='5'.repeat(32)).total),3,tag+': undo restores consolidated topics');
          assert.equal(await page.evaluate(()=>fixture.overview.topics.find(t=>t.agent_id==='6'.repeat(32)).total),1,tag+': Claude topic is unchanged by Pi organization');
          await header.getByText('Pi',{exact:true}).waitFor();await page.getByRole('textbox',{name:/^Ask .*Pi$/}).waitFor();
          const topicRequests=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/topics?')).map(r=>r.path));
          assert(topicRequests.length>=4,tag+': actual All topics and organization consumers queried scoped topics');
          for(const request of topicRequests){const query=new URL(request,'http://fixture').searchParams;assert.equal(query.get('peer'),'briar/desktop');assert.equal(query.get('agent_host'),'briar/desktop');assert.equal(query.get('agent_key'),'22222222-22222222-22222222-22222222');assert.equal(query.get('agent_id'),'5'.repeat(32));}
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,tag+': direct organization fits narrow view');
          await shot('direct-merged');combinations++;console.log('DIRECT ORGANIZE PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_ORGANIZE_ONLY){
          const mainRow=page.locator('[data-mid="'+ 'a'.repeat(32)+'"]');
          if(width>600){await mainRow.hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=page.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByText('1 message selected',{exact:true}).waitFor();
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          await page.locator('[data-mid="'+ '1'.repeat(32)+'"]').click();
          await page.getByText('2 messages selected',{exact:true}).waitFor();
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();
          let sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          assert.equal(await sheet.getByRole('option',{name:'Main flow',exact:true}).count(),1,'Main is an explicit move destination');
          await sheet.getByRole('textbox',{name:'New topic name',exact:true}).fill('Reviewed launch history');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();
          await sheet.getByText('Move 2 messages to “Reviewed launch history”?',{exact:true}).waitFor();
          await shot('organization-review');await sheet.getByRole('button',{name:'Cancel',exact:true}).click();
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply'))).length,0,tag+': cancel applies nothing');
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();
          await page.evaluate(()=>fixture.rejectOrganizationStale=true);await sheet.getByRole('button',{name:'Move 2 messages',exact:true}).click();
          await sheet.getByRole('alert').getByText(/review the move again/).waitFor();await sheet.getByRole('button',{name:'Review move',exact:true}).waitFor();
          assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='a'.repeat(32)).topic||''),'','Stale reviewed move leaves original main flow intact');
          assert.equal(await sheet.getByRole('button',{name:'Retry this move',exact:true}).count(),0,'Deterministic stale move requires fresh review');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await sheet.getByRole('button',{name:'Move 2 messages',exact:true}).click();
          await sheet.waitFor({state:'hidden'});
          const moved=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply'));assert.notEqual(moved[0].body.operation,moved[1].body.operation,'Fresh reviewed move captures a new operation');const sent=moved.at(-1);
          assert.deepEqual(sent.body.ids,['a'.repeat(32),'1'.repeat(32)],tag+': exact cross-topic selection');assert.equal(sent.body.topic,'2'.repeat(32));
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'))).length,0,tag+': originals never resent');
          assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='3'.repeat(32)).topic),'3'.repeat(32),tag+': unselected message unchanged');
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 3(?:\b)/}).click();
          await page.getByRole('button',{name:/^Topic 3.*open topic, menu$/}).click();
          await page.getByRole('menuitem',{name:'Merge into another topic…',exact:true}).click();
          sheet=page.getByRole('dialog',{name:'Merge this topic',exact:true});await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await sheet.getByRole('button',{name:'Move 1 message',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const merged=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply').at(-1));
          assert.deepEqual(merged.body.ids,['3'.repeat(32)]);assert.equal(merged.body.merge,'3'.repeat(32));
          // New Main choice is explicit and the current verified agent move
          // exposes a single human Undo with a stable retry identity.
          await page.evaluate(()=>{fixture.thread.messages.push({id:'9'.repeat(32),lid:'9'.repeat(32),from:'vitalii/desktop',send_group_author:'11111111-22222222-33333333-44444444',dir:'in',kind:'message',body:'Moved selected messages to this topic.',topic:'2'.repeat(32),at:'2026-10-05T10:00:01Z',state:'stored',verified_agent:true,pid:'b'.repeat(32),origin:'agent:organization',topic_event:{action:'move',moves:[{lid:'a'.repeat(32),author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64)}],request:{lid:'c'.repeat(32),author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64)}}});fixture.loseUndo=true;});
          await page.evaluate(()=>refreshFixture());await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 2(?:\b)/}).click();
          const undo=page.getByRole('button',{name:'Undo topic move',exact:true});await undo.click();await undo.waitFor({state:'visible'});await undo.click();await page.getByRole('button',{name:'Topic move undone',exact:true}).waitFor();
          const undos=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/undo'));assert.equal(undos.length,2);assert.equal(undos[0].body.id,undos[1].body.id);assert.equal(undos[1].body.lid,'9'.repeat(32));assert.equal(undos[1].body.author,'11111111-22222222-33333333-44444444');
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,tag+': organization fits narrow view');
          combinations++;console.log('ORGANIZE PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_PENDING_ONLY){
          if(kind!=='agent'){
            await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          }
          const current=()=>page.locator('[data-topic="'+ '1'.repeat(32)+'"][aria-current="true"]');
          const label=String(await current().getAttribute('aria-label'));
          assert(label.includes('No result')&&!label.includes('Waiting'),tag+': pending with no current executor word is not shown as Waiting: '+label);
          const composer=page.locator('[data-part="composer"]').getByRole('textbox');
          await composer.fill('Draft survives pending request navigation');
          await current().click();
          await page.getByRole('menuitem',{name:'Show pending requests (2)',exact:true}).click();
          const pending=page.getByRole('dialog',{name:'Pending requests',exact:true});
          await pending.waitFor();
          assert.equal(await pending.getByRole('button',{name:/Open pending/}).count(),2,tag+': every exact pending item listed');
          assert.equal(await pending.getByText('Unrelated final answer',{exact:true}).count(),0,tag+': unrelated reply is not a pending request');
          const text=await pending.innerText();
          assert.match(text,/No result recorded · on Vitalii’s \S+/,tag+': unknown is not invented running or done, and names its executor: '+text);
          assert.match(text,/last known/,tag+': reported stale status remains explicit');
          assert.match(text,kind==='agent'?/No result recorded · on [^\n]* · not connected now/:/No result recorded · on [^\n]* · needs an AgentNet update/,tag+': what is known of the executor: '+text);
          assert.match(text,/last known · on [^\n]* · not connected now/,tag+': the relay lists the executor offline');
          assert(!String(await pending.innerText()).includes('vitalii/desktop'),tag+': friendly host replaces raw address footer');
          await shot('pending-list');
          const before=await page.evaluate(()=>{fixture.holdUnrelatedDM=true;return fixture.requests.length;});
          await pending.getByRole('button',{name:/Open pending question 2 Second pending question/}).click();
          await pending.waitFor({state:'hidden'});
          if(kind==='agent'){
            await page.waitForFunction(before=>{const root=document.querySelector('#skin').shadowRoot;return root.querySelector('[data-mid="'+ 'b'.repeat(32)+'"]')?.getAnimations().length>0||fixture.requests.slice(before).some(r=>r.path.startsWith('/api/dm?'));},before);
            const requests=await page.evaluate(before=>fixture.requests.slice(before),before);
            assert(!requests.some(r=>r.path.startsWith('/api/dm?')),tag+': known direct request must not read an unrelated DM');
            const threads=requests.filter(r=>r.path.startsWith('/api/thread?'));
            assert.equal(threads.length,1,tag+': refresh only the bound direct topic once');
            assert.equal(new URL(threads[0].path,'http://fixture').searchParams.get('id'),'1'.repeat(32),tag+': child request never substitutes its ID for the direct topic root');
          }
          await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-mid="'+ 'b'.repeat(32)+'"]')?.getAnimations().length>0);
          assert.equal(await current().count(),1,tag+': original exact topic remains selected');
          assert.equal(await page.locator('[data-mid="'+ 'b'.repeat(32)+'"]').getAttribute('data-from'),'me',tag+': selected outgoing request keeps its author and direction');
          assert.equal(await composer.inputValue(),'Draft survives pending request navigation',tag+': draft remains in the bound conversation');
          await shot('pending-focused');
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.body&&r.path!='/api/refresh'&&!(r.path==='/api/act'&&r.body.do==='read')))).length,0,tag+': navigation sends no grants/retries/cancel/topic decisions');
          await page.evaluate(()=>resolvePending());
          await page.waitForTimeout(100);
          await current().click();
          assert.equal(await page.getByRole('menuitem',{name:/Show pending requests/}).count(),0,tag+': refreshed exact final state removes pending navigation');
          combinations++;console.log('PENDING PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_FLOW_ONLY){
          if(kind!=='agent'){
            await all().click();const list=page.getByRole('dialog',{name:'All topics',exact:true});
            await list.getByRole('button',{name:/^Topic 1(?:\b)/}).click();
            await page.getByText('Topic 1 message',{exact:false}).first().waitFor();
            assert.equal(await page.getByText('Quick main flow message',{exact:false}).first().isVisible(),false,tag+': selected flow is separate');
            await page.getByRole('button',{name:/^(Main|Main flow)$/}).click();
            await page.getByText('Quick main flow message',{exact:false}).first().waitFor();
            if(skin==='comic'){
              if(width>600){await page.getByText('Quick main flow message',{exact:false}).first().hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Make a topic',exact:true}).click();}
              else{const actions=page.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Make a topic',exact:true}).click();}
            }
            await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/topic/create'));
            await shot('promoted-flow');
            await page.getByRole('button',{name:/^(Main|Main flow)$/}).click();
            const mainRow=page.locator('[data-mid="'+ 'a'.repeat(32)+'"]').filter({hasText:'Quick main flow message'});
            await mainRow.first().waitFor({state:'hidden'});
            assert.equal(await mainRow.first().isVisible(),false,tag+': promoted message leaves main flow');
          }
          await page.getByRole('button',{name:'New topic',exact:true}).click();
          await page.locator('textarea:visible').last().fill('Brand new topic message');
          await page.getByRole('button',{name:/^(Send\b|Ask )/}).click();
          await page.locator('text=Brand new topic message >> visible=true').first().waitFor();
          const sent=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/send'||r.path==='/api/send'));
          assert(sent,tag+': send through Host API');
          if(kind!=='agent'){assert.equal(sent.body.topic,'new');assert.equal(await page.getByText('Quick main flow message',{exact:false}).first().isVisible(),false,tag+': new topic opens after sending');}else assert(!sent.body.reply_to,tag+': new agent topic has no parent');
          await shot('new-flow');combinations++;console.log('FLOW PASS '+tag);await ctx.close();continue;
        }
        await all().click();
        let dialog = page.getByRole('dialog', { name: 'All topics', exact: true });
        // Owner (v0.8.17): names and Archive (and in agent chats Done) follow the person's linked devices.
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).waitFor();
        assert.match(await dialog.innerText(), kind === 'agent' ? /Names, Done and Archive sync across your linked devices\./ : /Names and Archive sync across your linked devices\./, tag + ': All topics says which changes follow linked devices');
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 2', exact: true }).check();
        await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        assert.equal(await dialog.getByRole('button', { name: 'Confirm', exact: true }).count(), 1, tag + ': one confirmation');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await dialog.getByRole('button', { name: 'Undo', exact: true }).click();
        assert.equal((await changed()).length, 0, tag + ': Undo stops mutations');
        await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        // Owner (v0.8.17): the confirmation says where Done applies, like the footer.
        const doneAsk = await dialog.innerText();
        assert.match(doneAsk, kind === 'agent' ? /Mark these topics done on your linked devices\?|Mark done 2 topics\? On your linked devices\./ : /Mark these topics done for everyone\?|Mark done 2 topics\? Shared with everyone\./, tag + ': Mark done says where it applies');
        assert.doesNotMatch(doneAsk, /on this device/i, tag + ': Mark done is not called this device\'s');
        await shot('bulk-confirm');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/done'), null, { timeout: 12000 });
        let req = (await changed()).at(-1);
        assert.equal(req.body.ids.length, 2, tag + ': one batch');
        assert.deepEqual(req.body.counts, Object.fromEntries(req.body.ids.map(id=>[id,1])),tag+': displayed counts preserved');
        assert.equal(kind === 'agent' ? req.body.peer : req.body.conv, kind === 'agent' ? 'vitalii/desktop' : 'c'.repeat(64), tag + ': scope');
        await dialog.getByRole('button', { name: /^Done(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).waitFor();
        await shot('done');
        await dialog.getByRole('button', { name: /^(Close|Back to the conversation)$/ }).click();
        await all().click(); dialog = page.getByRole('dialog', { name: 'All topics', exact: true });
        await dialog.getByRole('button', { name: /^Active(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 4', exact: true }).check();
        await dialog.getByRole('button', { name: 'Archive', exact: true }).click();
        const archiveAsk = await dialog.innerText();
        assert.match(archiveAsk, /Archive these topics on your linked devices\? Nothing deleted\.|Archive 2 topics\? On your linked devices\./, tag + ': Archive says where it applies');
        assert.doesNotMatch(archiveAsk, /on this device/i, tag + ': Archive is not called this device\'s');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/archive'), null, { timeout: 12000 });
        await dialog.getByRole('button', { name: /^Archived(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).waitFor();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 4', exact: true }).check();
        await dialog.getByRole('button', { name: 'Delete for me', exact: true }).click();
        assert.match(await dialog.innerText(), /Others keep their copies|Other people keep their copies/);
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/delete'), null, { timeout: 12000 });
        assert.deepEqual(await page.evaluate(() => fixture.topics.map(t => t.title)), ['Topic 1', 'Topic 2'], tag + ': batch delete');
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': modal fits');
        await shot('deleted');
        await dialog.getByRole('button', { name: /^(Close|Back to the conversation)$/ }).click();
        await page.getByRole('button', { name: 'New topic', exact: true }).click();
        combinations++;console.log('PASS ' + tag);
      } catch (e) { await shot('failure'); console.error(JSON.stringify({ tag, errors, timeline:await page.locator("[role=log]").innerText().catch(()=>""), drafts:await page.evaluate(()=>localStorage.getItem("agentnet.messenger.drafts.default")), requests: await page.evaluate(() => fixture.requests) })); throw e; }
      await ctx.close();
    }
    assert.deepEqual(errors, []); console.log('chat topics rendered PASS: '+combinations+' desktop/phone chat/skin combinations');
  } finally { if (browser) await browser.close(); server.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
