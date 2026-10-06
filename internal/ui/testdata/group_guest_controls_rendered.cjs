// Inert captured-host fixture: current Classic/Zoom entry source and packaged supporting assets.
// No static rebuild, Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(name,address,key)=>({person:name.toLowerCase().padEnd(32,'a'),label:name,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const outside=person('Nora','nora/office','nora-key');
const me=person('Sergey','sergey/laptop','sergey-key'),vitalii=person('Vitalii','vitalii/desktop','vitalii-key');me.state='self';
const bohdan=person('Bohdan','bohdan/windows','bohdan-key');
const guest={pid:'guest-1',host:outside,inviter:me,shared:[],state:'active',state_text:'Guest while present',host_here:false,can_send:true,can_end:true,can_leave:false,can_decide:false};
const thread={id:conv,kind:'group',title:'amazon_team',peer:{label:'amazon_team',address:'',state:''},role:'member',members:[{...me,admin:true},vitalii],frozen:'',agents:[],guests:[guest],messages:[
 {id:'context',lid:'context',from:vitalii.address,dir:'in',kind:'message',body:'Selected warehouse context',at,attachments:[{name:'manifest.txt',size:12,id:'file-1'}]},
 {id:'private',lid:'private',from:me.address,dir:'out',kind:'message',body:'Unselected earlier history',at}]};
const overview={version:'fixture',seq:1,me:{address:me.address,fingerprint:me.fingerprint,responder:'fixture'},person:me,persons:true,agents:true,groups:true,files:{max_file:1048576,max_message:2097152,max_count:8},controls:true,role:'person',people:[outside,bohdan],review:[],links:[],reminders:[],threads:[],dms:[{id:conv,kind:'group',title:thread.title,peer:thread.peer,count:2,unread:0}],directory:{current:true,members:[me,vitalii,outside,bohdan].map(p=>({address:p.address,label:p.label,fingerprint:p.fingerprint,presence:'connected'}))},quarantine:[],group_invitations:[{id:'invite-1',conv,direction:'out',status:'pending',title:thread.title,inviter:me.person,target:bohdan.person,history:[],can_cancel:true,can_refresh:true}]};
const boot=`
const seed=${JSON.stringify({overview,thread})};window.fixture={...seed,requests:[]};
const variant=new URL(location.href).searchParams.get('case')||'admin';
if(variant==='member')fixture.thread.members[0].admin=false;
if(variant==='guest-active'||variant==='guest-invited'){
 fixture.thread.role='human_guest';fixture.overview.person={...fixture.thread.guests[0].host,state:'self'};fixture.overview.me.address=fixture.overview.person.address;
 Object.assign(fixture.thread.guests[0],{host_here:true,can_end:false,can_leave:variant==='guest-active',can_decide:variant==='guest-invited',can_send:variant==='guest-active',state:variant==='guest-active'?'active':'invited'});
 fixture.overview.group_invitations=[];
}
if(variant==='accepted')fixture.overview.group_invitations[0].status='accepted';
if(variant==='other-device')Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});
if(variant==='legacy-flags'){delete fixture.overview.group_invitations[0].can_cancel;delete fixture.overview.group_invitations[0].can_refresh;}
let open,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P6 fixture',endpoint:location.origin,address:seed.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('fixture accepts no files');},file:async()=>{throw Error('fixture contains no files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
if(p.startsWith('/api/agents'))return {host:body?.host||new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address,local:!p.includes('host='),agents:fixture.thread.agents.filter(a=>a.agent_id&&a.host.address===(new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address)).map(a=>({record:{id:a.agent_id,label:a.host.address===seed.overview.me.address?'Prospect':'Analyst',host:a.host.address},enabled:true})),sessions:[]};
if(p==='/api/dm/agent/invite'){fixture.thread.agents[0].shared=body.share;changed?.({type:'change',seq:++fixture.overview.seq});return structuredClone(fixture.thread.agents[0]);}
if(p==='/api/dm/guest/check')return {text:'All conversation devices support human guests.'};
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
window.openGroup=()=>open(seed.thread.id,'conversation');
window.disableInvite=()=>{Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});changed?.({type:'change',seq:++fixture.overview.seq});};window.ready=true;
`;
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}const source=u.pathname.match(/^\/assets\/skins\/(classic|zoom)\/entry\.mjs$/);if(source){res.setHeader('Content-Type','text/javascript');res.end(fs.readFileSync(path.resolve(__dirname,'../skins',source[1],'src/entry.mjs')));return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
 let browser;const errors=[],shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
  for(const skin of ['classic','zoom'])for(const width of [1280,390]){
   const openCase=async variant=>{const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'});await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());const page=await context.newPage();page.setDefaultTimeout(8000);page.on('pageerror',e=>errors.push(skin+': '+e.message));await page.goto(origin+'/?skin='+skin+'&case='+variant);await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());await page.getByText('Selected warehouse context',{exact:true}).first().waitFor();return {page,context};};
   const people=async page=>{if(skin==='zoom'){const d=page.getByLabel('People in this group',{exact:true});if(await d.getAttribute('open')===null)await d.locator(':scope > summary').click();}};
   const participants=async page=>{await people(page);await page.getByRole('button',{name:'+ Add participants',exact:true}).filter({visible:true}).first().click();};
   for(const variant of ['admin','member']){
    const {page,context}=await openCase(variant);await participants(page);assert.equal(await page.getByRole('button',{name:'Invite permanent member…',exact:true}).count(),variant==='admin'?1:0,'Only admin gets permanent member action');
    if(variant==='admin'){await page.getByRole('button',{name:'Invite permanent member…',exact:true}).click();await page.locator('#group-invite-person').waitFor();assert.equal(await page.locator('#guest-host').count(),0,'Explicit permanent member uses separate consent flow');await page.keyboard.press('Escape');await participants(page);}
    await page.getByRole('button',{name:'Bring in human…',exact:true}).click();await page.locator('#guest-host').fill(bohdan.address);
    assert.match(await page.locator('#dialog').innerText(),/Current group members shared with the guest:.*Sergey.*Vitalii/);assert.match(await page.locator('#dialog').innerText(),/not permanent membership/);
    if(variant==="admin")await page.locator('input[name="guest-share"][value="context"]').check();else{await page.getByRole("button",{name:"Check app compatibility",exact:true}).click();await page.getByText("All conversation devices support human guests.",{exact:true}).waitFor();await page.locator('input[name="guest-share"][value="context"]').focus();await page.keyboard.press("Space");}assert(await page.locator('input[name="guest-share"][value="context"]').isChecked(),"Pointer and keyboard select actual context checkbox");await page.locator('#dialog-ok').click();await page.getByText('Selected context includes files. Confirm file sharing, or deselect those messages.',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/invite').length),0);
    await settle(page);const consentShot=path.join(evidence,skin+'-'+variant+'-guest-history-'+width+'.png');await page.screenshot({path:consentShot});shots.push(consentShot);
    await page.locator('input[name="guest-file-consent"]').check();await page.locator('#dialog-ok').click();await page.locator('#dialog').waitFor({state:'hidden'});
    const sent=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/invite'));assert.equal(sent.length,1);assert.deepEqual(sent[0].body,{conv,host:bohdan.address,share:['context'],note:''});assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/invite').length),0,'Default human invite never grants membership');
    if(skin==='classic'){await page.locator('#body').fill('@Boh');await page.locator('#body').press('End');await page.locator('#body').dispatchEvent('input');await page.getByRole('button',{name:/Invite Bohdan/}).click();assert.equal(await page.locator('#guest-host').inputValue(),bohdan.address,'@ routes exact human address to guest dialog');await page.keyboard.press('Escape');}
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
  assert.deepEqual(errors,[]);console.log(JSON.stringify({ok:true,checks:'Classic+Zoom source: admin/member guest invite, selective file consent, guest join/leave/send/rights, exact retract/refresh and stale capability, desktop/mobile',shots}));
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
