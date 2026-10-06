// Inert captured-host fixture: Classic/Zoom source, built Comic and native DTO-shaped synthetic data.
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
if(variant==='guest-active'||variant==='guest-invited'){
 fixture.thread.role='human_guest';fixture.overview.person={...fixture.thread.guests[0].host,state:'self'};fixture.overview.me.address=fixture.overview.person.address;
 Object.assign(fixture.thread.guests[0],{host_here:true,can_end:false,can_leave:variant==='guest-active',can_decide:variant==='guest-invited',can_send:variant==='guest-active',state:variant==='guest-active'?'active':'invited'});
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
if(variant==='notifications'){
 const p=${JSON.stringify(brin)};p.devices.push({address:'brin/phone',fingerprint:'phone-key'});fixture.thread.kind='dm';fixture.thread.peer=p;
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:p,title:'Warehouse',created:'2026-10-01T10:00:00Z'}, {id:'e'.repeat(64),kind:'dm',peer:{...p,address:'brin/phone'},title:'Shipping',created:'2026-10-02T10:00:00Z'}];
 fixture.overview.notify={available:true,native:true,enabled:true,allowed:['brin/phone'],mutes:['e'.repeat(64)]};fixture.overview.group_invitations=[];
}
if(variant==='accepted')fixture.overview.group_invitations[0].status='accepted';
if(variant==='other-device')Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});
if(variant==='legacy-flags'){delete fixture.overview.group_invitations[0].can_cancel;delete fixture.overview.group_invitations[0].can_refresh;}
let open,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P6 fixture',endpoint:location.origin,address:seed.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async f=>{if(variant==='oks'){fixture.staged={name:f.name,size:f.size};return {id:'staged-1'};}throw Error('fixture accepts no files');},file:async()=>{throw Error('fixture contains no files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
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
if(p.includes('/groups/invitations'))return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};return {};
}};
const skin=new URL(location.href).searchParams.get('skin'),base='/assets/skins/'+skin+'/';
const manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);
window.openGroup=()=>open(seed.thread.id,'conversation');window.reloadFixture=()=>changed?.({type:'change',seq:++fixture.overview.seq});
window.disableInvite=()=>{Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});changed?.({type:'change',seq:++fixture.overview.seq});};window.ready=true;
`;
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}const source=u.pathname.match(/^\/assets\/skins\/(classic|zoom)\/entry\.mjs$/);if(source){res.setHeader('Content-Type','text/javascript');res.end(fs.readFileSync(path.resolve(__dirname,'../skins',source[1],'src/entry.mjs')));return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
 let browser;const errors=[],shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
  for(const skin of (process.env.AGENTNET_TEST_SKINS||'classic,zoom').split(','))for(const width of [1280,390]){
   const openCase=async variant=>{const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'});await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());const page=await context.newPage();page.setDefaultTimeout(8000);page.on('pageerror',e=>errors.push(skin+': '+e.stack));await page.goto(origin+'/?skin='+skin+'&case='+variant);await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());try{await page.getByText(variant==='oks'?'Main flow conversation':'Selected warehouse context',{exact:skin!=='comic'}).first().waitFor();}catch(e){console.error(JSON.stringify({errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent)}));throw e;}return {page,context};};
   if(process.env.AGENTNET_NOTIFY_REGRESSION==='1'){
    const {page,context}=await openCase('notifications');if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
    await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width===1280?'Settings':'You',exact:true}).click();await page.getByRole('button',{name:/^Notifications/}).click();
    const allow=page.getByRole('switch',{name:'Allow alerts from Brin',exact:true});assert.equal(await allow.count(),1,'Duplicate DM roots share one exact-person toggle');assert.equal(await allow.getAttribute('aria-checked'),'true','Any current device grant enables person toggle');
    await allow.click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/notify/allow'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/notify/allow').body),{person:brin.person,allowed:false});assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64)],'Person change preserves mute');
    await page.getByText('Conversation overrides (2)',{exact:true}).click();const shipping=page.getByRole('switch',{name:'Mute conversation Shipping',exact:true});assert.equal(await shipping.getAttribute('aria-checked'),'true');await shipping.click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/notify/mute'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/notify/mute').body),{conv:'e'.repeat(64),muted:false});assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.allowed),[],'Conversation mute change preserves person grants');
    await settle(page);const shot=path.join(evidence,'comic-notification-person-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();continue;
   }
   if(process.env.AGENTNET_OKS_REGRESSION==='1'){
    const {page,context}=await openCase('oks');
    const composer=page.getByRole('textbox',{name:'Message Brin',exact:true});await composer.fill('Unsent warehouse draft');
    await page.locator('form[aria-label="Write a message"] input[type=file]').setInputFiles({name:'draft.txt',mimeType:'text/plain',buffer:Buffer.from('synthetic unsent file')});await page.getByRole('list',{name:'Files to send'}).waitFor();
    await page.getByRole('button',{name:'Carrier review',exact:true}).click();assert.equal(await page.locator('[data-mid="main-note"]').count(),0);
    const oks=async()=>{if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^OKs/}).click();await page.getByText('2 things need you',{exact:true}).waitFor();};
    if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('button',{name:/^Brin 2 need your OK/}).getByText('2 need your OK',{exact:true}).waitFor();if(width===390)await page.getByRole('button',{name:/Brin.*2 need your OK/}).click();await oks();const waiting=page.getByRole('list',{name:'Waiting for you',exact:true}),working=page.getByRole('list',{name:'Working',exact:true});
    assert.equal(await waiting.locator('[data-open]').count(),2,'Running request does not count as an OK');assert.equal(await working.locator('[data-open]').count(),1);assert(!/Runs only if|Answered only if/.test(await working.innerText()),'Running row has no approval wording');await working.getByRole('button',{name:'Stop',exact:true}).waitFor();
    await waiting.getByText('Read the agent’s whole message',{exact:true}).click();assert((await waiting.innerText()).includes('FINAL_REASON_MARKER'),'List expands full needs_human detail');
    const arrow=working.locator('[data-open]');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();assert.equal(await page.locator('[data-mid="needs-person"]').count(),0,'Arrow leaves wrong topic');assert.equal(await composer.inputValue(),'Unsent warehouse draft');await page.getByRole('list',{name:'Files to send'}).waitFor();
    if(width===1280){await page.getByRole('button',{name:'Carrier review',exact:true}).click();await page.evaluate(()=>reloadFixture());await page.waitForFunction(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length>2);await page.locator('[data-mid="needs-person"]').waitFor();assert.equal(await page.locator('[data-mid="working-request"]').count(),0,'Background refresh preserves manually selected topic after previous focus');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();await page.locator('[data-mid="working-request"]').evaluate(e=>e.getAnimations().forEach(a=>a.finish()));await arrow.click();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-mid="working-request"]').getAnimations().length>0);}
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
  assert.deepEqual(errors,[]);console.log(JSON.stringify({ok:true,checks:process.env.AGENTNET_OKS_REGRESSION==='1'?'Comic: working excluded from OK count, exact topic/repeat focus, unsent draft/files preserved, full reason, future approve separate from held accept, exact Stop':process.env.AGENTNET_NOTIFY_REGRESSION==='1'?'Comic: one person grant across duplicate roots and separate conversation mute':(process.env.AGENTNET_TEST_SKINS||'Classic+Zoom source')+': guest/member distinction, exact targets, retract/refresh, rights, desktop/mobile',shots}));
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
