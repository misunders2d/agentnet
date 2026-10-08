// Inert captured-host fixture: bundled skins and native DTO-shaped synthetic data.
// No static rebuild, Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(name,address,key)=>({person:name.toLowerCase().padEnd(32,'a'),label:name,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const outside=person('Dune','dune/office','dune-key');
const me=person('Aster','aster/laptop','aster-key'),brin=person('Brin','brin/desktop','brin-key');me.state='self';
const cora=person('Cora','cora/windows','cora-key');
const guest={pid:'guest-1',host:outside,inviter:me,shared:[],state:'active',state_text:'Guest while present',host_here:false,can_send:true,can_end:true,can_leave:false,can_decide:false};
const thread={id:conv,kind:'group',title:'orchard_dispatch',peer:{label:'orchard_dispatch',address:'',state:''},role:'member',members:[{...me,admin:true},brin],frozen:'',agents:[],guests:[guest],messages:[
 {id:'context',lid:'context',from:brin.address,dir:'in',kind:'message',body:'Selected warehouse context',at,group_ref:{lid:'context',author:brin.fingerprint,hash:'a'.repeat(64)},attachments:[{name:'manifest.txt',size:12,id:'file-1'}]},
 {id:'private',lid:'private',from:me.address,dir:'out',kind:'message',body:'Unselected earlier history',at,group_ref:{lid:'private',author:me.fingerprint,hash:'b'.repeat(64)}}]};
const overview={version:'fixture',seq:1,me:{address:me.address,fingerprint:me.fingerprint,responder:'fixture'},person:me,persons:true,agents:true,groups:true,files:{max_file:1048576,max_message:2097152,max_count:8},controls:true,role:'person',people:[outside,cora],review:[],links:[],reminders:[],threads:[],dms:[{id:conv,kind:'group',title:thread.title,peer:thread.peer,count:2,unread:0}],directory:{current:true,members:[me,brin,outside,cora].map(p=>({address:p.address,label:p.label,fingerprint:p.fingerprint,presence:'connected'}))},quarantine:[],group_invitations:[{id:'invite-1',conv,direction:'out',status:'pending',title:thread.title,inviter:me.person,target:cora.person,history:[],can_cancel:true,can_refresh:true}]};
const boot=`
const seed=${JSON.stringify({overview,thread})};window.fixture={...seed,requests:[]};
const variant=new URL(location.href).searchParams.get('case')||'admin';
if(variant==='member')fixture.thread.members[0].admin=false;
if(variant==='browser-app'){
 fixture.overview.device={browser:true};fixture.overview.me.browser=true;
}
if(variant==='guest-active'||variant==='guest-invited'){
 fixture.thread.role='human_guest';fixture.overview.person={...fixture.thread.guests[0].host,state:'self'};fixture.overview.me.address=fixture.overview.person.address;
 Object.assign(fixture.thread.guests[0],{host_here:true,can_end:false,can_leave:variant==='guest-active',can_decide:variant==='guest-invited',can_send:variant==='guest-active',state:variant==='guest-active'?'active':'invited'});
 fixture.overview.group_invitations=[];
}
if(variant==='long-links'){
 fixture.url='https://example.test/folder/'+('long-segment/'.repeat(12))+'?q=a%2Fb&value=%E2%9C%93#section-2';
 fixture.linkBody='Read '+fixture.url+' then <'+fixture.url+'>.\\n[Useful guide]('+fixture.url+')\\n'+String.fromCharCode(96)+fixture.url+String.fromCharCode(96)+'\\n\\n~~~text\\n'+fixture.url+'\\n~~~';
 fixture.thread.messages.push({id:'long-links',lid:'long-links',from:'brin/desktop',dir:'in',kind:'message',body:fixture.linkBody,at:'2026-10-05T10:02:00Z'});
 fixture.overview.group_invitations=[];
}
if(variant==='oks'){
 fixture.thread.kind='dm';fixture.thread.peer=${JSON.stringify(brin)};fixture.thread.guests=[];fixture.thread.members=[];
 fixture.thread.topics=[{id:'work-topic',title:'Warehouse check',state:'active',count:1,last_at:'2026-10-05T10:00:00Z'},{id:'other-topic',title:'Carrier review',state:'active',count:1,last_at:'2026-10-05T10:00:00Z'}];
 const req=(id,kind,topic,actions,state,body)=>({id,lid:id,from:'brin/desktop',dir:'in',kind,topic,actions,state,body,at:'2026-10-05T10:00:00Z',pid:'own-agent',target:{address:'aster/laptop'},verified_agent:false});
 fixture.thread.agents=[{pid:'own-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',state_text:'Active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,invited:'2026-10-05T10:00:00Z',tasks_from:[]}];
 fixture.thread.messages=[req('working-request','task','work-topic',['cancel'],'running','Check warehouse inventory'),req('held-question','question','',['accept','decline','approve'],'held','Which carrier should we use?'),req('needs-person','task','other-topic',['resolve','accept','reply'],'needs_human','Review carrier handoff'),{id:'main-note',from:'brin/desktop',dir:'in',kind:'message',body:'Main flow conversation',at:'2026-10-05T10:00:00Z'}];
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.group_invitations=[];
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:fixture.thread.peer,count:4,unread:0,title:'',last:'Main flow conversation'}];
 fixture.why='Permission needed for carrier handoff.\\nReview the destination and confirm the warehouse owner has authorized this change. Nothing was sent to the carrier. Full detail remains here: FINAL_REASON_MARKER.';
 fixture.overview.needs_you=[{conv:fixture.thread.id,id:'working-request',reason:'agent_awaiting',peer:'brin/desktop',kind:'task',excerpt:'Check warehouse inventory',why:'Already working',actions:['cancel'],at:'2026-10-05T11:00:00Z'}, {conv:fixture.thread.id,id:'held-question',reason:'agent_awaiting',peer:'brin/desktop',kind:'question',excerpt:'Which carrier should we use?',why:'Not approved',actions:['accept','decline','approve'],at:'2026-10-05T10:00:00Z'}, {conv:fixture.thread.id,id:'needs-person',reason:'agent_needs_human',peer:'brin/desktop',kind:'task',excerpt:'Review carrier handoff',why:fixture.why,actions:['resolve','accept','reply'],at:'2026-10-05T09:00:00Z'}];
}
if(variant.startsWith('continuation-')){
 const mode=variant.slice('continuation-'.length),id='needs-answer-exact';
 fixture.thread.kind='dm';fixture.thread.peer=${JSON.stringify(brin)};fixture.thread.guests=[];fixture.thread.members=[];
 fixture.thread.agents=[{pid:'own-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',state_text:'Active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,tasks_from:[]}];
 const descriptor={id,key:'exact-request-key',attempt:7,...(mode==='remote'?{host:'aster/other-laptop'}:{})};
 const request={id,lid:id,from:'brin/desktop',dir:'in',kind:'task',body:'Review the destination',quote:'ordinary-quote',at:'2026-10-05T10:00:00Z',pid:'own-agent',target:{address:'aster/laptop'},state:'needs_human',job_state:'needs_human',job_detail:'Which warehouse should I use? EXACT_QUESTION',detail:'Which warehouse should I use? EXACT_QUESTION',actions:mode==='stale'?['resolve']:['continue','resolve'],...(mode==='stale'?{}:{continuation:descriptor})};
 fixture.thread.messages=[fixture.thread.messages[0],request,{id:'ordinary-quote',from:'brin/desktop',dir:'in',kind:'message',body:'Ordinary quoted note',at:'2026-10-05T09:00:00Z'}];
 fixture.overview.group_invitations=[];fixture.overview.people=[${JSON.stringify(brin)}];
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:fixture.thread.peer,count:3,unread:0,last:'Selected warehouse context'}];
 fixture.overview.needs_you=[{conv:fixture.thread.id,id,reason:'agent_needs_human',peer:'brin/desktop',kind:'task',excerpt:request.body,why:request.job_detail,actions:request.actions,continuation:request.continuation,at:request.at}];
}
if(variant.startsWith('decline-invite-')){
 fixture.overview.dms=[];fixture.overview.threads=[];
 fixture.overview.group_invitations=[{id:'inbound-exact',conv:'d'.repeat(64),direction:'in',status:variant.endsWith('stale')?'stale':'pending',title:'Orchard inbound invitation',inviter:${JSON.stringify(brin.address)},target:fixture.overview.person.person,history:[]}];
}
if(variant==='device-oks'){
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.dms=[];fixture.overview.group_invitations=[];fixture.overview.needs_you=[];
 const request='7'.repeat(32),other='8'.repeat(32),body='Check Orchard warehouse inventory';
 const summary=(id,title)=>({id,peer:'brin/desktop',title,last:title,last_at:'2026-10-05T10:00:00Z',count:1,review:0,running:0,unread:0,waiting:false,pending:true,state:'active',quiet_since:'2026-10-05T10:00:00Z'});
 fixture.overview.threads=[summary(request,body),summary(other,'Carrier notes')];
 const message={id:request,from:'brin/desktop',dir:'in',kind:'question',body,at:'2026-10-05T10:00:00Z',author:{label:'Brin',about:'Signed fictional fixture'},state:'held',actions:['reply','accept','decline','approve']};
 fixture.deviceThread={id:request,peer:'brin/desktop',key:{pinned:'brin-key'},approved:false,task_grant:'',messages:[message],topic:fixture.overview.threads[0]};
 fixture.otherDeviceThread={...fixture.deviceThread,id:other,topic:fixture.overview.threads[1],messages:[{...message,id:other,kind:'message',body:'Different device thread',state:'delivered',actions:[]}]};
 window.setDeviceState=state=>{
  message.state=state;message.actions=state==='held'?['reply','accept','decline','approve']:state==='running'?['cancel']:[];
  fixture.overview.threads[0].review=state==='held'?1:0;fixture.overview.threads[0].running=['running','cancel_requested'].includes(state)?1:0;
  fixture.overview.review=state==='done'?[]:[{id:request,peer:'brin/desktop',kind:'question',excerpt:body,why:state==='held'?'Not approved for automatic answers':state==='cancel_requested'?'Cancellation requested; stopping':'Already running',reason:['running','cancel_requested'].includes(state)?'agent_running':'',at:'2026-10-05T10:00:00Z'}];
  changed?.({type:'change',seq:++fixture.overview.seq});
 };
 // The initial overview carries review[], never a needs_you substitute.
 fixture.overview.threads[0].review=1;fixture.overview.review=[{id:request,peer:'brin/desktop',kind:'question',excerpt:body,why:'Not approved for automatic answers',at:'2026-10-05T10:00:00Z'}];
 window.openOtherDevice=()=>open(other,'message');
}
if(variant==='notifications'){
 const p=${JSON.stringify(brin)};p.devices.push({address:'brin/phone',fingerprint:'phone-key'});fixture.thread.kind='dm';fixture.thread.peer=p;
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:p,title:'Warehouse',created:'2026-10-01T10:00:00Z'}, {id:'e'.repeat(64),kind:'dm',peer:{...p,address:'brin/phone'},title:'Shipping',created:'2026-10-02T10:00:00Z'}];
 fixture.overview.notify={available:true,native:true,enabled:true,allowed:['brin/phone'],mutes:['e'.repeat(64)]};fixture.overview.group_invitations=[];
}
if(variant==='accepted')fixture.overview.group_invitations[0].status='accepted';
if(variant.startsWith('delivery-')){
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.dms=[];fixture.overview.group_invitations=[];fixture.overview.needs_you=[];
 const scenario=variant.slice('delivery-'.length),id='9'.repeat(32);
 const uncertain=scenario.startsWith('uncertain'),never=scenario.startsWith('never'),error=scenario==='queued-error';
 const terminal=scenario.endsWith('native')?'not_delivered':'failed';
 const state=uncertain||never||error?terminal:scenario==='running'?'delivered':scenario==='answered'?'delivered':scenario;
 const detail=uncertain?'Delivery unconfirmed; local retries stopped. Cancellation cannot be confirmed.':never?'Deleted before handover; not sent.':error?'Queued request cannot be handed over: synthetic recipient capability missing.':'';
 const summary={id,peer:'brin/desktop',title:'Synthetic delivery request',last:'Synthetic delivery request',last_at:'2026-10-05T10:00:00Z',count:1,review:0,running:0,unread:0,waiting:false,pending:true,state:'active',quiet_since:'2026-10-05T10:00:00Z'};
 const message={id,from:fixture.overview.me.address,to:'brin/desktop',dir:'out',kind:'question',body:'Synthetic delivery request',at:'2026-10-05T10:00:00Z',author:{label:'Aster',about:'Signed fictional fixture'},state,delivery:state,actions:[],deleted:!error,detail,state_text:uncertain?detail:never?'Not sent; local sending stopped.':error?detail:state,...(!error?{send_stopped:true}:{}),...(uncertain?{delivery_uncertain:true}:{})};
 if(scenario==='running')message.exec={state:'running',host:'brin/desktop',at:1};
 if(scenario==='answered')message.state_text='Answered by their agent';
 fixture.overview.threads=[summary];fixture.overview.review=[];
 fixture.deviceThread={id,peer:'brin/desktop',key:{pinned:'brin-key'},approved:false,task_grant:'',messages:[message],topic:summary};
 if(scenario==='answered')fixture.deviceThread.messages.push({id:'answer-fixture',from:'brin/desktop',dir:'in',kind:'answer',reply_to:id,body:'Synthetic answer already received',at:'2026-10-05T10:01:00Z',author:{label:'Brin',about:'Signed fictional fixture'},state:'received',actions:[]});
 fixture.deliveryScenario=scenario;fixture.deliveryDetail=detail;
}
if(variant==='other-device')Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});
if(variant==='legacy-flags'){delete fixture.overview.group_invitations[0].can_cancel;delete fixture.overview.group_invitations[0].can_refresh;}
if(variant==='multi-agent'){
fixture.thread.guests=[];fixture.overview.group_invitations=[];fixture.stages=[];fixture.failedOnce=false;
fixture.thread.agents=['agent-a','agent-b'].map((pid,i)=>({pid,host:fixture.thread.members[1],agent_id:String(i+1).repeat(32),state:'active',state_text:'Active',host_here:false,shared:[],can_ask:true,inviter:fixture.overview.person,invited:'2026-10-05T10:00:00Z',tasks_from:[]}));
}
if(variant==='heldback'){
 fixture.overview.group_invitations=[];fixture.overview.review=[];fixture.overview.needs_you=[];
 fixture.overview.quarantine=[
 {id:'legacy-invalid',peer:'brin/desktop',code:'invalid',reason:'invalid',detail:'The original detailed reason was not recorded or is unavailable.',recovery:'The retained message stays blocked. You can archive this notice locally; this does not accept, resend, or run it.',at:'2026-10-05T10:00:00Z',can_archive:true},
 {id:'invalid-ended-invite',peer:'brin/desktop',code:'invalid',reason:'invalid',detail_code:'group_invitation_outdated',detail:'This invitation no longer matches the current group.',recovery:'If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice.',can_archive:true,at:'2026-10-05T10:01:00Z'},
 {id:'legacy-unknown',peer:'brin/desktop',code:'old_unknown_code',reason:'legacy unknown',at:'2026-10-05T10:02:00Z'},
 {id:'proof-waiting',peer:'brin/desktop',code:'proof_pending',reason:'proof_pending',can_archive:false,at:'2026-10-05T10:03:00Z'}];
 fixture.retainedHeld=structuredClone(fixture.overview.quarantine);
}
let open,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P6 fixture',endpoint:location.origin,address:seed.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async f=>{if(variant==='multi-agent'){const id='multi-stage-'+(fixture.stages.length+1);fixture.stages.push({id,name:f.name,size:f.size});return {id};}if(variant==='oks'){fixture.staged={name:f.name,size:f.size};return {id:'staged-1'};}throw Error('fixture accepts no files');},file:async()=>{throw Error('fixture contains no files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});if(variant.startsWith('continuation-')&&['/api/act','/api/operator/decide'].includes(p)){
 const c=fixture.thread.messages.find(m=>m.id==='needs-answer-exact').continuation;
 if(!c||body.id!==c.id||body.key!==c.key||body.attempt!==c.attempt||!body.send_id)throw Error('Unexpected continuation identity');
 if(!fixture.continuationFailed){fixture.continuationFailed=true;throw Error('Synthetic lost transport response; retry this same answer');}
 const m=fixture.thread.messages.find(m=>m.id===c.id);m.state='running';m.job_state='running';m.actions=['cancel'];delete m.continuation;
 fixture.overview.needs_you=[];changed?.({type:'change',seq:++fixture.overview.seq});return {note:'Answer submitted to the same request.'};
}
if(variant.startsWith('decline-invite-')&&p==='/api/groups/decide'){
 if(body.id!=='inbound-exact'||body.accept!==false)throw Error('Unexpected invitation decision');
 fixture.overview.group_invitations=[];changed?.({type:'change',seq:++fixture.overview.seq});return {};
}
if(variant==='heldback'&&p==='/api/act'){
 if(body.do!=='archive_held'||body.id!=='invalid-ended-invite')throw Error('Unexpected or unauthorized held action');
 fixture.overview.quarantine=fixture.overview.quarantine.filter(q=>q.id!==body.id);
 return {note:'Notice archived on this device. Its envelope remains blocked.'};
}if(variant==='multi-agent'&&p==='/api/dm/agent/ask'){if(body.pid==='agent-b'&&!fixture.failedOnce){fixture.failedOnce=true;throw Error('Synthetic exact participation failure');}return {id:body.id,lid:body.id,state:'custody',state_text:'Stored for delivery'};}if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
if(p==='/api/get-app')return {version:'v0.8.5',detected:'linux',platforms:[{id:'linux',label:'Linux',url:'https://downloads.example/AgentNet.AppImage'},{id:'windows',label:'Windows',url:'https://downloads.example/AgentNet.exe'}]};
if(p==='/api/device/link')return {url:'https://workspace.example/#agentnet-link-v2:fixture',app_url:'agentnet://open#agentnet-link-v2:fixture',expires:'2026-10-08T23:59:00Z'};
if((variant==='device-oks'||variant.startsWith('delivery-'))&&p.startsWith('/api/thread?'))return structuredClone(new URL(p,location.origin).searchParams.get('id')==='8'.repeat(32)?fixture.otherDeviceThread:fixture.deviceThread);
if(variant==='device-oks'&&p==='/api/act'){if(body.do==='cancel')setDeviceState('cancel_requested');return {note:'Fixture action recorded'};}
if(p.startsWith('/api/agents'))return {host:body?.host||new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address,local:!p.includes('host='),agents:fixture.thread.agents.filter(a=>a.agent_id&&a.host.address===(new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address)).map(a=>({record:{id:a.agent_id,label:a.host.address===seed.overview.me.address?'Prospect':'Analyst',host:a.host.address},enabled:true})),sessions:[]};
if(p==='/api/dm/agent/invite'){fixture.thread.agents[0].shared=body.share;changed?.({type:'change',seq:++fixture.overview.seq});return structuredClone(fixture.thread.agents[0]);}
if(p==='/api/notify/allow'){fixture.overview.notify.allowed=body.allowed?['brin/desktop','brin/phone']:[];return {};}
if(p==='/api/notify/mute'){fixture.overview.notify.mutes=body.muted?[...fixture.overview.notify.mutes,body.conv]:fixture.overview.notify.mutes.filter(id=>id!==body.conv);return {};}
if(p==='/api/dm/guest/check')return {ready:true,text:'All conversation devices support human guests.'};
if(p==='/api/dm/guest/invite')return {};if(p==='/api/dm/send')return {id:body.id};
if(p==='/api/dm/guest/decide'){Object.assign(fixture.thread.guests[0],{state:body.accept?'active':'declined',can_send:body.accept,can_decide:false,can_leave:body.accept});return {};}
if(p==='/api/dm/guest/end'){Object.assign(fixture.thread.guests[0],{state:'dismissed',can_send:false,can_leave:false,can_end:false});return {};}
if(p==='/api/groups/cancel'){fixture.overview.group_invitations[0].status='cancelled';return {};}
if(p==='/api/groups/refresh'){fixture.overview.group_invitations[0]={...fixture.overview.group_invitations[0],id:'fresh-invite',status:'pending'};return fixture.overview.group_invitations[0];}
if(p.includes('/groups/invitations'))return variant.startsWith('decline-invite-')?structuredClone(fixture.overview.group_invitations):[];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};return {};
}};
if(variant==='browser-app')host.platform='browser';
const skin=new URL(location.href).searchParams.get('skin'),base='/assets/skins/'+skin+'/';
const manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);
window.openGroup=()=>open(seed.thread.id,'conversation');window.openFixtureMessage=id=>open(id,'message');window.reloadFixture=()=>changed?.({type:'change',seq:++fixture.overview.seq});
window.disableInvite=()=>{Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});changed?.({type:'change',seq:++fixture.overview.seq});};window.ready=true;
`;
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
 let browser;const errors=[],shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
  for(const skin of (process.env.AGENTNET_TEST_SKINS||'classic,zoom').split(','))for(const width of [1280,390]){
   const openCase=async variant=>{const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'});await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());const page=await context.newPage();page.setDefaultTimeout(8000);page.on('pageerror',e=>errors.push(skin+': '+e.stack));await page.goto(origin+'/?skin='+skin+'&case='+variant);try{await page.waitForFunction(()=>window.ready);}catch(e){console.error(JSON.stringify({skin,width,variant,errors,text:await page.locator('body').innerText()}));throw e;}if(variant==='device-oks'||variant.startsWith('delivery-')||variant.startsWith('decline-invite-'))return {page,context};await page.evaluate(()=>openGroup());try{await page.getByText(variant==='oks'?'Main flow conversation':'Selected warehouse context',{exact:skin!=='comic'}).first().waitFor();}catch(e){console.error(JSON.stringify({errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent)}));throw e;}return {page,context};};
// Insert before AGENTNET_DELIVERY_STOP_REGRESSION branch in existing rendered harness.
if(process.env.AGENTNET_LONG_LINK_REGRESSION==='1'){
 const {page,context}=await openCase('long-links');
 try {
  let message=page.locator(skin==='comic'?'[data-mid="long-links"]':'#m-long-links');await message.waitFor();
  const url=await page.evaluate(()=>fixture.url),body=await page.evaluate(()=>fixture.linkBody);
  if(skin==='zoom'){
   assert.equal(await message.getByRole('link').count(),0,'Zoom preview stays one message-opening button');
   assert((await message.innerText()).includes('example.test/…'),'Zoom preview shortens raw URL text');
   assert(await message.evaluate(e=>e.scrollWidth<=e.clientWidth+1),'Zoom preview stays within viewport');
   await settle(page);const preview=path.join(evidence,'zoom-long-links-preview-'+width+'.png');await page.screenshot({path:preview});shots.push(preview);
   await message.locator('.mc-bubble').click();
   message=page.locator('.zoom-message');await message.waitFor();
  }
  const links=message.getByRole('link',{name:url,exact:true});assert.equal(await links.count(),2);
  for(const link of await links.all()){
   assert.equal(await link.innerText(),'example.test/…');assert.equal(await link.getAttribute('href'),url);assert.equal(await link.getAttribute('title'),url);
   assert.equal(await link.evaluate(e=>e.href),url,'Native open/copy-link uses exact target');
   if(width===390)assert(await link.evaluate(async e=>{e.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,pointerType:'touch',pointerId:9}));await new Promise(r=>setTimeout(r,500));const native=e.dispatchEvent(new PointerEvent('contextmenu',{bubbles:true,cancelable:true,pointerType:'touch',pointerId:9}));e.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerType:'touch',pointerId:9}));return native;}),'Long press keeps native full-target/copy-link menu');
  }
  assert((await message.innerText()).includes('Useful guide'),'Descriptive label unchanged');
  assert((await message.innerText()).includes(url),'Code retains full URL');
  assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='long-links').body),body,'Stored/copy/edit source unchanged');
  if(skin==='comic'){
   await page.evaluate(()=>{window.copiedText=null;Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copiedText=text;}}});});
   if(width===390){const actions=message.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('dialog').getByRole('button',{name:'Copy text',exact:true}).click();}
   else{await message.hover();await message.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Copy text',exact:true}).click();}
   assert.equal(await page.evaluate(()=>window.copiedText),body,'Copy message retains the entire original URL');
  }
  await settle(page);const shot=path.join(evidence,skin+'-long-links-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  assert(await message.evaluate(e=>e.scrollWidth<=e.clientWidth+1),'Message stays within viewport');
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_CONTINUATION_REGRESSION==='1'){
 for(const mode of ['local','remote','stale']){
  const {page,context}=await openCase('continuation-'+mode);
  try{
   const request=page.locator(skin==='comic'?'[data-mid="needs-answer-exact"]':'#m-needs-answer-exact');await request.waitFor();
   const continuationReply=(skin==='comic'?request:request.locator(skin==='classic'?'.decide .acts':'.agent-turn .acts')).getByRole('button',{name:'Reply',exact:true});
   const note=page.locator(skin==='comic'?'[data-mid="ordinary-quote"]':'#m-ordinary-quote');await note.waitFor();
   if(skin==='comic'&&width===390){const actions=note.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('dialog').getByRole('button',{name:'Reply',exact:true}).click();}else if(skin!=='zoom'){await note.hover();await note.getByRole('button',{name:'Reply',exact:true}).click();}
   assert.equal(await page.getByRole('dialog',{name:'Answer your agent',exact:true}).count(),0,'Ordinary Reply only quotes a message');
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>(r.path==='/api/operator/decide'||r.path==='/api/act'&&r.body?.do!=='read')).length),0,'Ordinary initial quote never submits a continuation');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask','/api/send'].includes(r.path))),'Clarification and quote never create another request/manual answer');
   if(mode==='stale'){
    assert.equal(await continuationReply.count(),0,'Unverified/stale host has no continuation Reply');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'||r.path==='/api/act'&&r.body?.do!=='read').length),0,'Stale request does not act on load');
   }else{
    await continuationReply.click();
    const dialog=page.getByRole('dialog',{name:'Answer your agent',exact:true});await dialog.waitFor();
    assert((await dialog.innerText()).includes('EXACT_QUESTION'),'Agent clarification shown in full');
    assert.match(await dialog.innerText(),/permission or environment problem/);assert.match(await dialog.innerText(),/Reply and retry keep the same permissions/);
    const send=dialog.getByRole('button',{name:'Send answer',exact:true});assert(await send.isDisabled(),'Empty answer cannot be submitted');
    await dialog.locator('#agent-answer').fill('Use the east warehouse');await send.click();
    await page.waitForFunction(()=>fixture.continuationFailed);await send.waitFor();
    await settle(page);await dialog.evaluate(async e=>{await Promise.all(e.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
    const sheetStyle=await dialog.evaluate(e=>{const c=getComputedStyle(e);return {background:c.backgroundColor,opacity:c.opacity,transform:c.transform,filter:c.filter,backdropFilter:c.backdropFilter,classes:e.className};});
    const styleFile=path.join(evidence,skin+'-continuation-answer-'+mode+'-'+width+'.json');fs.writeFileSync(styleFile,JSON.stringify(sheetStyle,null,2),{mode:0o600});shots.push(styleFile);
    const answerShot=path.join(evidence,skin+'-continuation-answer-'+mode+'-'+width+'.png');await page.screenshot({path:answerShot});shots.push(answerShot);
    await send.click();await page.waitForFunction(()=>fixture.thread.messages.find(m=>m.id==='needs-answer-exact').job_state==='running');
    await page.evaluate(()=>reloadFixture());await settle(page);
    const calls=await page.evaluate(()=>fixture.requests.filter(r=>['/api/act','/api/operator/decide'].includes(r.path)));
    assert.equal(calls.length,2,'One explicit retry after synthetic transport failure');assert.deepEqual(calls[0],calls[1],'Retry preserves exact request, attempt and send identity');
    const expected={id:'needs-answer-exact',key:'exact-request-key',attempt:7,send_id:calls[0].body.send_id};assert.match(expected.send_id,/^[0-9a-f]{32}$/);
    if(mode==='local')assert.deepEqual(calls[0],{path:'/api/act',body:{do:'continue',...expected,body:'Use the east warehouse'}});
    else assert.deepEqual(calls[0],{path:'/api/operator/decide',body:{host:'aster/other-laptop',...expected,action:'continue',expect:'needs_human',text:'Use the east warehouse',report:''}});
    assert.equal(await continuationReply.count(),0,'Consumed continuation descriptor removed on refresh');
   }
   await settle(page);const shot=path.join(evidence,skin+'-continuation-'+mode+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_DECLINE_INVITATION_REGRESSION==='1'){
 for(const status of ['pending','stale']){
  const {page,context}=await openCase('decline-invite-'+status);
  try{
   if(skin==='comic'){
    await page.getByText('Orchard inbound invitation',{exact:true}).waitFor();
    if(status==='stale')assert.equal(await page.getByRole('button',{name:'Join',exact:true}).count(),0,'Stale invitation cannot Join');
    const before=path.join(evidence,skin+'-decline-'+status+'-before-'+width+'.png');await page.screenshot({path:before});shots.push(before);
    await page.getByRole('button',{name:'No thanks',exact:true}).click();
   }else{
    await page.getByRole('button',{name:/Invitation: Orchard inbound invitation/}).click();
    const dialog=page.getByRole('dialog');await dialog.waitFor();
    if(status==='stale')assert.equal(await dialog.getByRole('button',{name:'Accept invitation',exact:true}).count(),0,'Stale invitation cannot accept');
    const before=path.join(evidence,skin+'-decline-'+status+'-before-'+width+'.png');await page.screenshot({path:before});shots.push(before);
    await dialog.getByRole('button',{name:'No thanks',exact:true}).click();
   }
   await page.waitForFunction(()=>fixture.overview.group_invitations.length===0);await page.evaluate(()=>reloadFixture());await settle(page);
   assert.equal(await page.getByText('Orchard inbound invitation',{exact:true}).count(),0,'Declined card stays absent after refresh');
   assert.equal(await page.getByRole('button',{name:/Invitation: Orchard inbound invitation/}).count(),0,'Legacy invitation row stays absent after refresh');
   assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/decide').map(r=>r.body)),[{id:'inbound-exact',accept:false}]);
   const shot=path.join(evidence,skin+'-decline-'+status+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_MULTI_AGENT_REGRESSION==='1'){
 const {page,context}=await openCase('multi-agent');
 try{
  if(skin==='zoom')await page.getByRole('button',{name:'Write in this group…',exact:true}).click();
  const field=skin==='comic'?page.locator('form[aria-label="Write a message"] textarea'):skin==='zoom'?page.locator('#write-body'):page.locator('#body');
  await field.waitFor({state:'visible'});
  const pick=async index=>{
   await field.press('End');await field.pressSequentially('@');
   // Two same-label agents on one host, distinct participation IDs, fixed DTO order.
   const options=skin==='comic'?page.getByRole('option').filter({hasText:'Analyst'}):page.locator('.mention-row').filter({hasText:'Analyst'});
   assert.equal(await options.count(),2,'both same-label agents must be offered');
   await options.nth(index).click();
  };
  await pick(0);await pick(1);
  // Edit the first mention away, preserving the second, then select it again.
  await field.press('Home');for(let i=0;i<'@Analyst '.length;i++)await field.press('Delete');
  await pick(0);await field.pressSequentially('Compare warehouse inventory');
  const file=skin==='comic'?page.locator('form[aria-label="Write a message"] input[type=file]'):skin==='zoom'?page.locator('#write-files'):page.locator('#file-input');
  await file.setInputFiles({name:'inventory.txt',mimeType:'text/plain',buffer:Buffer.from('synthetic inventory')});
  if(skin==='comic')await page.locator('form[aria-label="Write a message"] button[type=submit]').click();
  else await page.locator(skin==='zoom'?'#dialog-ok':'#send').click();
  if(skin==='classic'&&await page.getByText('You started this text for someone else. Check the To line, then send again.',{exact:true}).isVisible())await page.locator('#send').click();
  await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===2);
  const asks=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').map(r=>r.body));
  const first=await asks();assert.deepEqual(first.map(a=>a.pid).sort(),['agent-a','agent-b']);
  assert.notEqual(first[0].id,first[1].id,'one id per exact participation');
  assert.match(first[0].send_group,/^[a-f0-9]{32}$/);assert.equal(first[0].send_group,first[1].send_group,'one durable composer group');
  await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.innerHTML.includes('Retry'));
  const visibleCopies=()=>page.locator('#skin').evaluate(e=>(e.shadowRoot.innerText||e.shadowRoot.textContent).split('Compare warehouse inventory').length-1);
  assert.equal(await visibleCopies(),1,'one human body with two independently retryable targets');
  assert(first.every(a=>a.kind==='question'&&a.body.includes('Compare warehouse inventory')));
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send').length),0,'no ordinary-message fallback');
  if(skin==='zoom')for(const pid of ['agent-a','agent-b'])await page.locator('.mini-chat [data-agent-recipient][title="'+pid+'"]').getByText('To @Analyst',{exact:true}).waitFor();
  assert.equal(first[0].files.length,1);assert.equal(first[1].files.length,1);
  assert.notDeepEqual(first[0].files[0],first[1].files[0],'each request owns separately staged upload');
  assert.equal(await page.evaluate(()=>fixture.stages.length),2);
  await page.getByRole('button',{name:'Retry',exact:true}).click();
  await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===3);
  const after=await asks(),failed=first.find(a=>a.pid==='agent-b');
  assert.equal(after[2].pid,'agent-b');assert.equal(after[2].id,failed.id);
  assert.equal(after[2].send_group,failed.send_group,'retry retains the same durable group');
  assert.equal(after[2].files.length,1);assert.notDeepEqual(after[2].files,first.find(a=>a.pid==='agent-a').files,'failed retry never uses successful request upload');
  assert.equal(after.filter(a=>a.pid==='agent-a').length,1,'successful participation never resent');
  assert((await page.evaluate(()=>fixture.stages.length))<=3,'at most the failed request file restaged');
  await page.evaluate(()=>{
   const asks=fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').slice(0,2).map(r=>r.body);
   for(const [i,a] of asks.entries())fixture.thread.messages.push({id:a.id,lid:a.id,from:fixture.overview.me.address,dir:'out',origin:'ui',kind:a.kind,body:a.body,topic:a.topic,pid:a.pid,send_group:a.send_group,send_group_author:fixture.overview.me.fingerprint,at:'2026-10-05T10:02:00Z',target:{address:'brin/desktop',agent_id:String(i+1).repeat(32)},attachments:[{name:'inventory.txt',size:19,openable:false}],delivery:'delivered',exec:{state:i===0?'running':'done',host:'brin/desktop',attempt:1,at:1791194520}});
   reloadFixture();
  });
  await settle(page);assert.equal(await visibleCopies(),1,'durable projection replaces previews without duplicating body');
  for(const a of first){const child=page.locator(skin==='comic'?'[data-mid="'+a.id+'"]':'#m-'+a.id);assert((await child.innerText()).includes('inventory.txt'),'Each exact recipient retains its own attachment name despite compact human body');}
  await page.evaluate(()=>reloadFixture());await settle(page);assert.equal(await visibleCopies(),1,'reloading server projection preserves grouping');
  await page.evaluate(()=>{fixture.thread.messages.push({id:'incoming-ref',lid:'incoming-ref',from:'brin/desktop',dir:'in',kind:'message',body:'[@Analyst](agentnet:agent/agent-a) [@Analyst](agentnet:agent/agent-b) INERT_INCOMING_MARKER',at:'2026-10-05T10:03:00Z'});reloadFixture();});
  await page.getByText(/INERT_INCOMING_MARKER/).first().waitFor();await settle(page);
  assert.equal((await asks()).length,3,'incoming text never originates fanout');
  const shot=path.join(evidence,skin+'-multi-agent-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }catch(e){const shot=path.join(evidence,skin+'-multi-agent-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,shot,errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
 finally{await context.close();}
 continue;
}

   // Insert before delivery regression branch, within existing skin/width loop.
if(process.env.AGENTNET_HELDBACK_REGRESSION==='1'){
 const {page,context}=await openCase('heldback');
 try{
  if(skin==='comic'){
   if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
   await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).click();
  }else{
   if(width===390){if(skin==='zoom')await page.getByRole('navigation',{name:'Zoom level',exact:true}).getByRole('button',{name:'Everyone',exact:true}).click();else await page.getByRole('button',{name:'Back to conversations',exact:true}).click();}
   await page.locator('#profile-btn').click();await page.locator('[data-settings="device"]').click();
   await page.locator('#quarantine-summary').click();
  }
  const held=skin==='comic'?page.getByRole('region',{name:'Held back',exact:true}):page.locator('#quarantine');
  await held.waitFor({state:'visible'});
  assert.equal(await held.locator('li').count(),4);
  const specific=held.locator('li').filter({hasText:'This invitation no longer matches the current group.'});
  await specific.getByText('If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice.',{exact:true}).waitFor();
  assert.equal(await held.getByRole('button',{name:'Archive notice',exact:true}).count(),2);
  assert.equal(await held.getByRole('button',{name:/Accept|Approve|Run task|Trust/}).count(),0,'held notices cannot authorize execution or identity');
  assert(!/SECRET_HELD_BODY|SECRET_HELD_ATTACHMENT/.test(await held.innerText()),'encrypted contents never shown');
  // Old invalid DTO has no detail/recovery: safe generic fallback remains.
  await held.getByText('The original detailed reason was not recorded or is unavailable.',{exact:true}).waitFor();
  const unknown=held.locator('li').filter({hasText:skin==='comic'?"couldn’t be checked":"couldn't be verified"});
  assert.equal(await unknown.count(),1,'unknown old classification stays unverified');
  assert.equal(await unknown.getByRole('button',{name:'Archive notice'}).count(),0);
  await specific.getByRole('button',{name:'Archive notice',exact:true}).click();
  await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='archive_held'));
  await page.waitForFunction(()=>fixture.overview.quarantine.length===3);
  await page.waitForFunction(()=>!document.querySelector('#skin').shadowRoot.textContent.includes('This invitation no longer matches the current group.'));
  assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act').map(r=>r.body)),[{do:'archive_held',id:'invalid-ended-invite'}]);
  assert.deepEqual(await page.evaluate(()=>fixture.overview.quarantine.map(q=>q.id).sort()),['legacy-invalid','legacy-unknown','proof-waiting']);
  assert.equal(await page.evaluate(()=>fixture.retainedHeld.length),4,'local notice hide retains blocked envelope metadata');
  assert.equal(await held.getByRole('button',{name:'Archive notice',exact:true}).count(),1,'other invalid notice remains');
  await settle(page);const shot=path.join(evidence,skin+'-heldback-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }catch(e){const shot=path.join(evidence,skin+'-heldback-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,shot,errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
 finally{await context.close();}
 continue;
}

if(process.env.AGENTNET_DELIVERY_STOP_REGRESSION==='1'){
    for(const scenario of ['never','never-native','uncertain','uncertain-native','custody','delivered','running','answered','queued-error']){
     const {page,context}=await openCase('delivery-'+scenario),request='9'.repeat(32);
     await page.evaluate(id=>openFixtureMessage(id),request);
     const row=skin==='comic'?page.locator('[data-mid="'+request+'"]'):skin==='zoom'?page.locator('.zoom-message'):page.locator('#m-'+request);
     try{await row.waitFor();}catch(e){const shot=path.join(evidence,skin+'-'+scenario+'-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,scenario,errors,shot,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}const visible=await row.innerText();
     assert(!/Cancelled|Canceled/.test(visible),skin+' '+scenario+': local stop never claims recipient cancellation');
     if(scenario.startsWith('uncertain'))assert.match(visible,/Delivery unconfirmed|delivery unconfirmed/);
     if(scenario.startsWith('never'))assert.match(visible,/Not sent|not sent/);
     if(scenario==='running')assert.match(visible,/Working|working|Running|running/);
     if(scenario==='answered')assert.match(visible,/Answered|answered/);
     if(scenario==='delivered')assert.match(visible,/Delivered|delivered|on their/);
     if(scenario==='custody')assert.match(visible,/server|custody|Sent/);
     if(skin==='comic')await row.getByRole('button',{name:/^Asked /}).click();
     else {const details=row.locator('details.tech');if(!await details.evaluate(e=>e.open))await details.locator('summary').click();}
     const detail=await page.evaluate(()=>fixture.deliveryDetail);
     if(detail)await page.getByText(detail,{exact:true}).filter({visible:true}).first().waitFor();
     if(scenario.startsWith('uncertain'))assert(await page.getByText(/Cancellation cannot be confirmed/).filter({visible:true}).count()>0);
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/act','/api/send','/api/dm/send'].includes(r.path)).length),0,'Opening status/details never retries work');
     assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'Delivery detail fits viewport');
     await settle(page);const shot=path.join(evidence,skin+'-'+scenario+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();
    }
    continue;
   }
   if(process.env.AGENTNET_FULL_REASON_REGRESSION==='1'){
    const {page,context}=await openCase('oks');
    if(skin==='comic'){
      if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
      await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).click();
    }else {
      if(width===390){if(skin==='zoom')await page.getByRole('navigation',{name:'Zoom level',exact:true}).getByRole('button',{name:'Everyone',exact:true}).click();else await page.getByRole('button',{name:'Back to conversations',exact:true}).click();}
      await page.locator('#review-btn').click();
    }
    await page.getByText('Read the agent’s whole message',{exact:true}).click();
    await page.getByText(/FINAL_REASON_MARKER/).filter({visible:true}).first().waitFor();
    if(skin==='comic')await page.locator('[data-open]').filter({hasText:'Review carrier handoff'}).click();
    else await page.locator('#review-list button').filter({hasText:'Your agent couldn’t finish'}).click();
    const region=page.getByRole('region',{name:'Your agent says',exact:true});
    try{await region.waitFor();}catch(e){const shot=path.join(evidence,skin+'-full-reason-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,errors,shot,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
    assert((await region.innerText()).includes(await page.evaluate(()=>fixture.why)),'Chat retains the complete overview explanation when message detail is absent');
    await settle(page);const shot=path.join(evidence,skin+'-full-reason-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();continue;
   }
   if(process.env.AGENTNET_BROWSER_APP_REGRESSION==='1'){
    const {page,context}=await openCase('browser-app');
    if(skin==='comic'){
     if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
     await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Agents',exact:true}).click();
     await page.getByRole('button',{name:'Connect an agent on your computer',exact:true}).click();
    }else{
     if(width===390){if(skin==='zoom')await page.getByRole('navigation',{name:'Zoom level',exact:true}).getByRole('button',{name:'Everyone',exact:true}).click();else await page.getByRole('button',{name:'Back to conversations',exact:true}).click();}
     await page.locator('#profile-btn').click();await page.locator('[data-settings="device"]').click();
    }
    const setup=page.getByRole('region',{name:'Connect an agent on your computer',exact:true});
    await setup.getByRole('link',{name:'Get AgentNet for Linux',exact:true}).waitFor();
    assert.equal(await setup.getByRole('link',{name:'Get AgentNet for Linux',exact:true}).getAttribute('href'),'https://downloads.example/AgentNet.AppImage');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/device/link').length),0,'Looking does not create a device link');
    await setup.getByRole('button',{name:'Link the installed app to me',exact:true}).click();
    const open=setup.getByRole('link',{name:'Open this link in AgentNet',exact:true});await open.waitFor();
    assert.equal(await open.getAttribute('href'),'agentnet://open#agentnet-link-v2:fixture','Exact existing app link retained');
    assert.match(await setup.innerText(),/approve the new computer/);
    await setup.getByText('App opened without the link?',{exact:true}).click();
    await setup.getByText('https://workspace.example/#agentnet-link-v2:fixture',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/device/link').length),1);
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/assistant-setup','/api/device/decide'].includes(r.path)).length),0,'Browser setup neither changes tools nor approves a new device');
    await settle(page);const shot=path.join(evidence,skin+'-browser-app-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();continue;
   }
   if(process.env.AGENTNET_DIRECT_REVIEW_REGRESSION==='1'){
    const {page,context}=await openCase('device-oks');const request='7'.repeat(32),body='Check Orchard warehouse inventory';
    const activity=async()=>{if(skin==='comic'){const back=page.getByRole('button',{name:'Back to chats',exact:true});if(width===390&&await back.isVisible())await back.click();await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).click();}else{const back=page.getByRole('button',{name:'Back to conversations',exact:true});if(width===390&&await back.isVisible())await back.click();if(await page.locator('#review-btn').getAttribute('aria-expanded')!=='true')await page.locator('#review-btn').click();}};
    const waiting=skin==='comic'?page.getByRole('list',{name:'Waiting for you',exact:true}):page.locator('#review-list');
    const working=skin==='comic'?page.getByRole('list',{name:'Working',exact:true}):page.locator('#activity-extra');
    await activity();await waiting.getByRole('button').filter({hasText:body}).waitFor();
    if(skin==='comic'){await page.getByText('1 thing needs you',{exact:true}).waitFor();assert.equal(await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).getByText('1',{exact:true}).count(),1,'Direct held request counts in OK badge');}
    else{assert.equal(await page.locator('#review-count').innerText(),'1');assert(await page.locator('#review-count').isVisible());}
    await page.evaluate(()=>setDeviceState('running'));await working.getByRole('button').filter({hasText:body}).waitFor();assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0,'Running review[] request leaves decision list');
    if(skin==='comic'){assert.equal(await page.getByText('1 thing needs you',{exact:true}).count(),0);assert.equal(await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).getByText('1',{exact:true}).count(),0,'Working review[] request clears OK badge');}
    else{assert.equal(await page.locator('#review-count').innerText(),'0');assert(!await page.locator('#review-count').isVisible(),'Working review[] request clears activity badge');}
    assert(!/needs your (OK|decision)|Waiting for approval/.test(await working.innerText()),'Working row uses no approval claim');assert(!/\[object HTML/.test(await working.innerText()),'Working row renders peer name rather than DOM string');assert(!/Already running\s*·\s*Already running/.test(await working.innerText()),'Working status is not duplicated');assert.match(await working.innerText(),/Working|Already running/);await settle(page);const listShot=path.join(evidence,skin+'-direct-review-working-'+width+'.png');await page.screenshot({path:listShot});shots.push(listShot);
    await page.evaluate(()=>openOtherDevice());const unrelated=skin==='comic'?page.locator('[data-mid="'+'8'.repeat(32)+'"]'):skin==='zoom'?page.locator('.zoom-message').filter({hasText:'Different device thread'}):page.locator('#m-'+'8'.repeat(32));await unrelated.waitFor();assert((await unrelated.innerText()).includes('Different device thread'));await activity();
    await working.locator('button').filter({hasText:body}).first().click();const focused=skin==='comic'?page.locator('[data-mid="'+request+'"]'):skin==='zoom'?page.locator('.zoom-message').filter({hasText:body}):page.locator('#m-'+request);
    await focused.waitFor();assert((await focused.innerText()).includes(body),'Working row opens exact direct request');await unrelated.waitFor({state:'hidden'});assert(!await unrelated.isVisible(),'Working row leaves unrelated thread');
    await settle(page);const shot=path.join(evidence,skin+'-direct-review-running-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    await focused.getByRole('button',{name:skin==='comic'?'Stop':'Stop…',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Stop',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='cancel'));
    assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='cancel').body.id),request,'Stop binds exact running request');await activity();await working.getByRole('button').filter({hasText:body}).waitFor();assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0,'Stopping request remains outside decisions');
    await page.evaluate(()=>setDeviceState('done'));await page.waitForFunction(()=>!document.querySelector('#skin').shadowRoot.textContent.includes('Already running'));assert.equal(await working.getByRole('button').filter({hasText:body}).count(),0,'Completed request leaves Working');assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0);
    assert.equal(await page.evaluate(()=>fixture.overview.needs_you.length),0,'Regression exercises direct review[] throughout');await context.close();continue;
   }
   if(process.env.AGENTNET_NOTIFY_REGRESSION==='1'){
    const {page,context}=await openCase('notifications');if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
    await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width===1280?'Settings':'You',exact:true}).click();await page.getByRole('button',{name:/^Notifications/}).click();
    assert.equal(await page.getByRole('switch',{name:/Allow alerts from/}).count(),0,'No global people allow list');
    assert.equal(await page.getByText(/Conversation overrides/).count(),0,'No global conversation override list');
    await page.getByText('Chats notify you unless muted.',{exact:false}).waitFor();
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64)],'Opening settings preserves explicit mute');
    await settle(page);const settingsShot=path.join(evidence,'comic-notification-settings-'+width+'.png');await page.screenshot({path:settingsShot});shots.push(settingsShot);
    await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Chats',exact:true}).click();
    await page.evaluate(()=>openGroup());await page.getByRole('button',{name:'More',exact:true}).click();
    await page.getByRole('menuitem',{name:'Mute notifications',exact:true}).click();
    await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/notify/mute'));
    assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/notify/mute').body),{conv,muted:true});
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64),conv],'Chat mute preserves other explicit mute');
    await page.getByRole('button',{name:'More',exact:true}).click();await page.getByRole('menuitem',{name:'Turn notifications on',exact:true}).waitFor();
    await settle(page);const shot=path.join(evidence,'comic-notification-chat-mute-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    await page.getByRole('menuitem',{name:'Turn notifications on',exact:true}).click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/notify/mute').length===2);
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64)],'Unmuting exact chat preserves other mute');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/notify/allow').length),0,'Mute never changes sender grants');await context.close();continue;
   }
   if(process.env.AGENTNET_OKS_REGRESSION==='1'){
    const {page,context}=await openCase('oks');
    const composer=page.getByRole('textbox',{name:'Message Brin',exact:true});await composer.fill('Unsent warehouse draft');
    await page.locator('form[aria-label="Write a message"] input[type=file]').setInputFiles({name:'draft.txt',mimeType:'text/plain',buffer:Buffer.from('synthetic unsent file')});await page.getByRole('list',{name:'Files to send'}).waitFor();
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/}).click();
    await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/Carrier review/}).click();assert.equal(await page.locator('[data-mid="main-note"]').count(),0);
    const oks=async()=>{if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).click();await page.getByText('2 things need you',{exact:true}).waitFor();};
    if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('button',{name:/^Brin 2 need your OK/}).getByText('2 need your OK',{exact:true}).waitFor();if(width===390)await page.getByRole('button',{name:/Brin.*2 need your OK/}).click();await oks();const waiting=page.getByRole('list',{name:'Waiting for you',exact:true}),working=page.getByRole('list',{name:'Working',exact:true});
    assert.equal(await waiting.locator('[data-open]').count(),2,'Running request does not count as an OK');assert.equal(await working.locator('[data-open]').count(),1);assert(!/Runs only if|Answered only if/.test(await working.innerText()),'Running row has no approval wording');await working.getByRole('button',{name:'Stop',exact:true}).waitFor();
    await waiting.getByText('Read the agent’s whole message',{exact:true}).click();assert((await waiting.innerText()).includes('FINAL_REASON_MARKER'),'List expands full needs_human detail');
    const arrow=working.locator('[data-open]');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();assert.equal(await page.locator('[data-mid="needs-person"]').count(),0,'Arrow leaves wrong topic');assert.equal(await composer.inputValue(),'Unsent warehouse draft');await page.getByRole('list',{name:'Files to send'}).waitFor();
    if(width===1280){await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/}).click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/Carrier review/}).click();await page.evaluate(()=>reloadFixture());await page.waitForFunction(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length>2);await page.locator('[data-mid="needs-person"]').waitFor();assert.equal(await page.locator('[data-mid="working-request"]').count(),0,'Background refresh preserves manually selected topic after previous focus');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();await page.locator('[data-mid="working-request"]').evaluate(e=>e.getAnimations().forEach(a=>a.finish()));await arrow.click();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-mid="working-request"]').getAnimations().length>0);}
    const rect=await page.locator('[data-mid="working-request"]').boundingBox();assert(rect&&rect.y>=0&&rect.y<900,'Exact requested message visibly focused');await settle(page);const focusedShot=path.join(evidence,'comic-exact-topic-'+width+'.png');await page.screenshot({path:focusedShot});shots.push(focusedShot);
    await oks();await waiting.locator('[data-open]').filter({hasText:'Review carrier handoff'}).click();await page.locator('[data-mid="needs-person"]').waitFor();const region=page.getByRole('region',{name:'Your agent says',exact:true});assert((await region.innerText()).includes(await page.evaluate(()=>fixture.why)),'Chat displays full overview reason when job_detail absent');
    await oks();await waiting.locator('[data-open]').filter({hasText:'Which carrier should we use?'}).click();await page.locator('[data-mid="held-question"]').waitFor();assert.equal(await page.locator('[data-mid="needs-person"]').count(),0,'Main-flow request clears other topic');
    await page.locator('[data-mid="held-question"]').getByRole('button',{name:'Approve Brin…',exact:true}).click();const confirm=page.getByRole('dialog');assert.match(await confirm.innerText(),/This held question still waits: choose Allow once/);await confirm.getByRole('button',{name:'Approve Brin',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='approve'));assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='accept')),'Standing approval does not accept held item');
    await page.locator('[data-mid="held-question"]').getByRole('button',{name:'Allow once',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='accept'));assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='accept').body.id),'held-question');
    await oks();await working.getByRole('button',{name:'Stop',exact:true}).click();const stop=page.getByRole('dialog');await stop.getByRole('button',{name:'Stop',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='cancel'));assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='cancel').body.id),'working-request');
    await settle(page);const shot=path.join(evidence,'comic-oks-working-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/dm/send')),'Navigation preserves unsent draft');await context.close();continue;
   }
   if(skin==='comic'){
    const bring=page=>page.getByRole('button',{name:width===1280?'Bring in':'Bring someone in',exact:true}).first().click();
    const room=async page=>{if(!await page.getByRole('list',{name:'Invited people',exact:true}).isVisible())await page.getByRole('button',{name:'orchard_dispatch. Who’s in this chat',exact:true}).click();};
    for(const variant of ['admin','member']){
     const {page,context}=await openCase(variant);await bring(page);
     const guestMode=page.locator('input[name=human-invite-mode][value=guest]'),memberMode=page.locator('input[name=human-invite-mode][value=member]');
     try{assert(await guestMode.isChecked(),'Default human invitation is a guest');}catch(e){console.error(await page.getByRole('dialog').innerText());throw e;};assert.equal(await memberMode.isDisabled(),variant==='member','Permanent membership is admin-only');
     if(variant==='admin'){await memberMode.locator('..').click();assert(await memberMode.isChecked());await guestMode.locator('..').click();}
     await page.getByRole('radio',{name:/Cora/}).first().locator('..').click();
     const fewer=page.getByRole('button',{name:'Fewer messages',exact:true});while(!await fewer.isDisabled())await fewer.click();
     await settle(page);const inviteShot=path.join(evidence,'comic-'+variant+'-guest-invite-'+width+'.png');await page.screenshot({path:inviteShot});shots.push(inviteShot);await page.getByRole('button',{name:'Invite Cora as a guest',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/guest/invite'));
     assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/invite').body),{conv,host:cora.address,share:[],note:''});
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/invite').length),0,'Guest invite never grants membership');
     assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/check').body.host),cora.address,'Compatibility checks exact selected host');
     await settle(page);const shot=path.join(evidence,'comic-'+variant+'-guest-controls-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();
    }
    for(const variant of ['guest-active','guest-invited']){
     const {page,context}=await openCase(variant);assert.equal(await page.getByRole('button',{name:width===1280?'Bring in':'Bring someone in',exact:true}).count(),0,'Human guest cannot invite');
     await page.getByRole('button',{name:'orchard_dispatch. Who’s in this chat',exact:true}).click();assert.equal(await page.getByRole('button',{name:'Leave group',exact:true}).count(),0,'Guest has no membership leave action');await context.close();
    }
    for(const variant of ['admin','accepted','other-device','legacy-flags']){
     const {page,context}=await openCase(variant);await room(page);
     const list=page.getByRole('list',{name:'Invited people',exact:true}),retract=list.getByRole('button',{name:'Retract invitation',exact:true}),refresh=list.getByRole('button',{name:'Refresh invitation',exact:true});
     if(variant==='other-device'||variant==='legacy-flags'){assert.equal(await retract.count(),0);assert.equal(await refresh.count(),0);await context.close();continue;}
     await retract.click();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/cancel').length),0);await page.keyboard.press('Escape');
     await refresh.click();await page.getByRole('button',{name:'Send fresh invitation',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/groups/refresh'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/refresh').body),{id:'invite-1'});
     await retract.click();await page.getByRole('dialog',{name:'Retract invitation for Cora?',exact:true}).getByRole('button',{name:'Retract invitation',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/groups/cancel'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/cancel').body),{id:'fresh-invite'});await context.close();
    }
    continue;
   }
   const people=async page=>{if(skin==='zoom'){const d=page.getByLabel('People in this group',{exact:true});if(await d.getAttribute('open')===null)await d.locator(':scope > summary').click();}};
   const participants=async page=>{await people(page);await page.getByRole('button',{name:'+ Add participants',exact:true}).filter({visible:true}).first().click();};
   for(const variant of ['admin','member']){
    const {page,context}=await openCase(variant);await participants(page);assert.equal(await page.getByRole('button',{name:'Invite permanent member…',exact:true}).count(),variant==='admin'?1:0,'Only admin gets permanent member action');
    if(variant==='admin'){await page.getByRole('button',{name:'Invite permanent member…',exact:true}).click();await page.locator('#group-invite-person').waitFor();assert.equal(await page.locator('#guest-host').count(),0,'Explicit permanent member uses separate consent flow');await page.keyboard.press('Escape');await participants(page);}
    await page.getByRole('button',{name:'Bring in human…',exact:true}).click();await page.locator('#guest-host').fill(cora.address);
    assert.match(await page.locator('#dialog').innerText(),/Current group members shared with the guest:.*Aster.*Brin/);assert.match(await page.locator('#dialog').innerText(),/not permanent membership/);
    if(variant==="admin")await page.locator('input[name="guest-share"][value="context"]').check();else{await page.getByRole("button",{name:"Check app compatibility",exact:true}).click();await page.getByText("All conversation devices support human guests.",{exact:true}).waitFor();await page.locator('input[name="guest-share"][value="context"]').focus();await page.keyboard.press("Space");}assert(await page.locator('input[name="guest-share"][value="context"]').isChecked(),"Pointer and keyboard select actual context checkbox");await page.locator('#dialog-ok').click();await page.getByText('Selected context includes files. Confirm file sharing, or deselect those messages.',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/invite').length),0);
    await settle(page);const consentShot=path.join(evidence,skin+'-'+variant+'-guest-history-'+width+'.png');await page.screenshot({path:consentShot});shots.push(consentShot);
    await page.locator('input[name="guest-file-consent"]').check();await page.locator('#dialog-ok').click();await page.locator('#dialog').waitFor({state:'hidden'});
    const sent=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/invite'));assert.equal(sent.length,1);assert.deepEqual(sent[0].body,{conv,host:cora.address,share:['context'],note:''});assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/invite').length),0,'Default human invite never grants membership');
    if(skin==='classic'){await page.locator('#body').fill('@Cor');await page.locator('#body').press('End');await page.locator('#body').dispatchEvent('input');await page.getByRole('button',{name:/Invite Cora/}).click();assert.equal(await page.locator('#guest-host').inputValue(),cora.address,'@ routes exact human address to guest dialog');await page.keyboard.press('Escape');}
    assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'No mobile action overflow');await settle(page);const shot=path.join(evidence,skin+'-'+variant+'-guest-controls-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();
   }
   for(const variant of ['guest-active','guest-invited']){
    const {page,context}=await openCase(variant);await people(page);
    assert.equal(await page.getByRole('button',{name:'+ Add participants',exact:true}).filter({visible:true}).count(),0,'Guest cannot invite');assert.equal(await page.getByRole('button',{name:'Leave group…',exact:true}).filter({visible:true}).count(),0,'Guest cannot perform membership leave');
    if(skin==='classic')await page.locator('#agents .human-participant').filter({visible:true}).click();
    const join=page.getByRole('button',{name:'Join conversation…',exact:true}),leave=page.getByRole('button',{name:'Leave conversation…',exact:true});
    if(variant==='guest-invited'){
     assert(skin==='classic'?await page.locator('#send').isDisabled():await page.getByRole('button',{name:'Join before writing in this group',exact:true}).isDisabled(),'Invited guest cannot send');
     await join.click();assert.match(await page.locator('#dialog').innerText(),/no membership or invitation rights/);await page.locator('#dialog-ok').click();
     assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/decide').body.accept),true);await people(page);if(skin==='classic')await page.locator('#agents .human-participant').filter({visible:true}).click();
    }
    assert.match(await leave.locator('..').locator('..').innerText(),/no group membership or invitation rights/);
    if(skin==='classic')await page.keyboard.press('Escape');
    if(skin==='zoom'){await page.getByRole('button',{name:'Write in this group…',exact:true}).click();await page.locator('#write-body').fill('Guest dispatch test');await page.locator('#dialog-ok').click();}
    else{assert(!await page.locator('#send').isDisabled());await page.locator('#body').fill('Guest dispatch test');await page.locator('#send').click();}
    await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/send'));const request=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/send').body);assert.equal(request.pid,'guest-1');assert.equal(request.conv,conv);assert.equal(request.body,'Guest dispatch test');assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/groups/change')),'Guest sends no membership operations');
    await people(page);if(skin==='classic')await page.locator('#agents .human-participant').filter({visible:true}).click();
    await leave.click();await page.locator('#dialog-ok').click();assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/end').body),{pid:'guest-1'});await context.close();
   }
   for(const variant of ['admin','accepted','other-device','legacy-flags']){
    const {page,context}=await openCase(variant);const retract=page.getByRole('button',{name:'Retract invitation…',exact:true}).filter({visible:true}).first(),refresh=page.getByRole('button',{name:'Refresh invitation…',exact:true}).filter({visible:true}).first();
    if(variant==='other-device'||variant==='legacy-flags'){assert.equal(await retract.count(),0);assert.equal(await refresh.count(),0);await context.close();continue;}
    await retract.click();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/cancel').length),0,'Opening control grants nothing');await page.keyboard.press('Escape');assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/cancel').length),0,'Cancelling confirmation sends nothing');
    await refresh.click();assert.match(await page.locator('#dialog').innerText(),/Join again/);await page.locator('#dialog-ok').click();assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/refresh').body),{id:'invite-1'});
    await retract.click();await page.locator('#dialog-ok').click();assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/cancel').body),{id:'fresh-invite'});await context.close();
   }
   const {page,context}=await openCase('admin');await page.getByRole('button',{name:'Retract invitation…',exact:true}).filter({visible:true}).first().click();await page.evaluate(()=>disableInvite());await page.waitForTimeout(40);await page.locator('#dialog-ok').click();await page.getByText('This invitation can no longer be changed here. Refresh the conversation and review it again.',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/cancel').length),0,'Stale capability blocks action');await context.close();
  }
  if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'),'retained synthetic evidence must stay in /tmp');fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  assert.deepEqual(errors,[]);console.log(JSON.stringify({ok:true,checks:process.env.AGENTNET_DIRECT_REVIEW_REGRESSION==='1'?'Direct review[]: held/running/stopping/complete; decision badge/list vs Working; exact request/Stop; desktop/mobile':process.env.AGENTNET_OKS_REGRESSION==='1'?'Comic: working excluded from OK count, exact topic/repeat focus, unsent draft/files preserved, full reason, future approve separate from held accept, exact Stop':process.env.AGENTNET_NOTIFY_REGRESSION==='1'?'Comic: one person grant across duplicate roots and separate conversation mute':(process.env.AGENTNET_TEST_SKINS||'Classic+Zoom source')+': guest/member distinction, exact targets, retract/refresh, rights, desktop/mobile',shots}));
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
