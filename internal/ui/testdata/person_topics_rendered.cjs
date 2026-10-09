// Production skins, public Host API, fictional roots; no relay or real data.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const assets=path.resolve(process.env.AGENTNET_TOPIC_ASSETS||path.join(__dirname,'../static'));
const evidence=process.env.AGENTNET_SCREENSHOTS;
if(evidence)fs.mkdirSync(evidence,{recursive:true,mode:0o700});
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
if (${process.env.AGENTNET_TOPIC_UNREAD==='1'}) for(const id of [ids.main,ids.other])for(const m of views[id].messages)if(m.dir==='in')m.unread=true;
if (${process.env.AGENTNET_ADDRESSING==='1'}) {
 const pi='5'.repeat(32),codex='6'.repeat(32);
 views[ids.main].agents=[{pid:pi,agent_id:'7'.repeat(32),state:'active',host:me,inviter:me,invited_by:me,shared:[],host_here:true,member:true,can_ask:false},{pid:codex,agent_id:'8'.repeat(32),state:'active',host:me,inviter:me,invited_by:me,shared:[],host_here:true,member:true,can_ask:false}];
 views[ids.main].messages.push(msg('agent-ask','Are you the reviewer?',{kind:'question',from:me.address,origin:'agent:room',verified_agent:true,agent_author_pid:pi,agent_id:'7'.repeat(32),pid:codex,target:{address:me.address,agent_id:'8'.repeat(32)},state:'delivered',delivery:'delivered',exec:{state:'not_run',host:me.address},job_detail:'not run: originating local run stopped',state_text:'Not run: originating local run stopped'}),msg('approved-task','Review the fictional plan.',{dir:'out',from:me.address,kind:'task',origin:'ui',pid:pi,target:{address:me.address,agent_id:'7'.repeat(32)},proposal:{proposal_id:'fictional-proposal',confirmed_by:me.address,asker:me.address,proposal:'Review the fictional plan.'}}));
}
const summary=v=>({id:v.id,...(${process.env.AGENTNET_PREVIEW_ROUTE==='1'}&&v.messages.length?{last_id:v.messages.at(-1).lid||v.messages.at(-1).id}:{}),created:v.created,peer,kind:v.kind,role:v.role,count:v.messages.length,title:v.messages[0]?.deleted?'Message deleted':v.messages[0]?.body||'',last:v.messages.at(-1)?.deleted?'Message deleted':v.messages.at(-1)?.body||'',last_at:v.id===ids.other?'2026-10-07T12:00:00Z':at,unread:0,held:0,waiting:0,guests:v.guests.length});
const overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:false,controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[ids.main,ids.other,ids.deleted,ids.empty,ids.guest].map(id=>summary(views[id])),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
function unreadCounts(){for(const v of Object.values(views)){for(const t of v.topics)t.unread=v.messages.filter(m=>m.unread&&m.topic===t.id).length;const s=overview.dms.find(s=>s.id===v.id);if(s)s.unread=v.messages.filter(m=>m.unread).length;}}
if (${process.env.AGENTNET_TOPIC_UNREAD==='1'})unreadCounts();
if (${process.env.AGENTNET_PREVIEW_ROUTE==='1'}) overview.dms.sort((a,b)=>b.last_at.localeCompare(a.last_at));
overview.remind=${process.env.AGENTNET_OWN_REMIND==='1'};overview.reminders=JSON.parse(localStorage.getItem('fictional-reminders')||'[]');
if(localStorage.getItem('fictional-late'))overview.dms.push(summary(views[ids.late]));
let changed,open,release;
const requests=[];window.fixture={ids,views,overview,requests,native,delay:'',release:()=>release?.(),open:(...args)=>open(...args),addLate:()=>{localStorage.setItem('fictional-late','1');if(!overview.dms.some(d=>d.id===ids.late))overview.dms.push(summary(views[ids.late]));changed?.({type:'change',seq:++overview.seq});},tick:()=>changed?.({type:'change',seq:++overview.seq})};
fixture.unreadCounts=unreadCounts;
const host={version:1,platform:'daemon',workspace:{id:'r4-fictional',name:'Fictional workspace',address:me.address,realm:'fictional'},workspaces:null,skins:[],skin:{id:skin},skinURL:'/assets/skins/'+skin+'/',listen(fn){changed=fn;return()=>{};},onOpen(fn){open=fn;},onSkinsChange(){return()=>{};},selectSkin(){},file(){throw Error('No fixture files');},stage(){throw Error('No fixture files');},api:async(p,body)=>{
 requests.push({path:p,body:body||null});const url=new URL(p,location.origin);
 if(url.pathname==='/api/remind'){const m=views[ids.main].messages.find(m=>m.id===body.id);if(!m)throw Error('Exact stored message required');const r={message:m.id,conv:ids.main,from:m.from,title:m.body,due:new Date(body.due*1000).toISOString(),overdue:false};overview.reminders=overview.reminders.filter(x=>x.message!==m.id).concat(r);localStorage.setItem('fictional-reminders',JSON.stringify(overview.reminders));changed?.({type:'change',seq:++overview.seq});return{note:'Fictional reminder stored.'};}
 if(url.pathname==='/api/overview')return structuredClone(overview);
 if(${process.env.AGENTNET_TOPIC_UNREAD==='1'}&&url.pathname==='/api/act'&&body.do==='read'){if(fixture.failRead)throw Error('Fictional read acknowledgement unavailable');for(const v of Object.values(views))for(const m of v.messages)if(body.ids.includes(m.id))m.unread=false;unreadCounts();changed?.({type:'change',seq:++overview.seq});return{note:'Read.'};}
 if(url.pathname==='/api/dm'){const id=url.searchParams.get('id');if(id===fixture.delay){fixture.delay='';await new Promise(resolve=>release=resolve);}if(!views[id])throw Error('Unknown fictional root');return structuredClone(views[id]);}
 if(url.pathname==='/api/dm/send'){const view=views[body.conv];const m=msg(body.id,body.body,{dir:'out',from:me.address,topic:body.topic||'',reply_to:body.reply_to,at:new Date().toISOString()});view.messages.push(m);overview.dms=overview.dms.map(d=>d.id===view.id?summary(view):d);changed?.({type:'change',seq:++overview.seq});return{id:m.id,lid:m.id,state:'delivered'};}
 if(url.pathname==='/api/topics'){const rows=(views[url.searchParams.get('conv')]?.topics||[]).filter(t=>!url.searchParams.get('state')||t.state===url.searchParams.get('state'));return{topics:structuredClone(rows),matched:rows.length};}
 if(url.pathname.startsWith('/api/topic/')){const view=views[body.conv],ids=body.ids||[body.id];for(const id of ids){const t=view.topics.find(t=>t.id===id);if(t)t.state=url.pathname.endsWith('/done')?'done':url.pathname.endsWith('/archive')?'archived':'active';}changed?.({type:'change',seq:++overview.seq});return{note:'Fictional topic changed.'};}
 if(url.pathname==='/api/message/react')return{note:'Fictional reaction recorded.'};
 if(p.includes('/groups/invitations'))return[];
 if(p.startsWith('/api/typing/status'))return{send:false,scopes:[]};
 if(p.startsWith('/api/agents'))return{host:${process.env.AGENTNET_ADDRESSING==='1'}?me.address:peer.address,agents:${process.env.AGENTNET_ADDRESSING==='1'}?[{record:{id:'7'.repeat(32),label:'Pi',host:me.address}},{record:{id:'8'.repeat(32),label:'Codex',host:me.address}}]:[],sessions:[],local:${process.env.AGENTNET_ADDRESSING==='1'}};
 return{};
}};
const base='/assets/skins/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
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
 let browser;try{
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const skin of (process.env.PERSON_TOPIC_SKINS||'comic,classic,zoom').split(','))for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce',isMobile:width===390,hasTouch:width===390});
   await context.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort());
   const page=await context.newPage(),errors=[];page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.stack));
   const shot=async(name)=>{if(evidence){await page.waitForTimeout(350);await page.screenshot({path:path.join(evidence,'person-topics-'+skin+'-'+width+'-'+name+'.png')});}};
   const all=()=>page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/});
   const shown=text=>page.locator(skin==='comic'?'[role=log] [data-mid]':skin==='classic'?'.timeline .body':'.mini-chat .mc-text').filter({hasText:text}).first();
   const field=()=>skin==='comic'?page.getByRole('textbox',{name:'Message Casey',exact:true}):page.locator('#body');
   const list=()=>page.getByRole('dialog',{name:'All topics',exact:true});
   const edit=async text=>{if(skin==='zoom'){await page.getByRole('button',{name:'Write in this DM…',exact:true}).click();await page.locator('#write-body').fill(text);await page.locator('#dialog-cancel').click();}else await field().fill(text);};
   const choose=async(pattern)=>{await all().click();await list().getByRole('button',{name:pattern}).click();};
   await page.goto(origin+'/?skin='+skin);await page.waitForFunction(()=>window.ready);
   if(skin==='comic')await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Casey/}).first().click();
   else if(skin==='classic')await page.locator('.contact-item').getByRole('button',{name:/Casey/}).first().click();
   else await page.locator('.zoom-content').getByRole('button',{name:/Casey/}).first().click();
   if(process.env.AGENTNET_PREVIEW_ROUTE==='1'){
    const exact=(id,text)=>skin==='comic'?page.locator('[data-mid="'+id+'"]'):skin==='classic'?page.locator('#m-'+id):page.locator('.zoom-message').filter({hasText:text});
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
    if(skin==='zoom')await page.keyboard.press('Escape');
    assert.equal(await field().inputValue(),'Draft survives exact preview navigation','exact focus keeps unsent text');
    // A missing target does not invent a topic or trigger an action. When
    // its history later arrives, the existing change feed resolves it.
    await page.evaluate(()=>fixture.open('delayed-native','message',{conv:fixture.ids.main}));
    await page.waitForTimeout(150);
    await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.push({id:'delayed-native',lid:'delayed-native',topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'History arrived later',at:'2026-10-06T12:00:00Z',state:'stored',actions:[]});fixture.tick();});
    await exact('delayed-native','History arrived later').waitFor();
    if(skin==='zoom')await page.keyboard.press('Escape');
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
    if(skin==='zoom')await page.keyboard.press('Escape');
    await page.evaluate(()=>{fixture.delay=fixture.ids.other;fixture.open('other-native','message',{conv:fixture.ids.other});});
    await page.waitForFunction(()=>fixture.delay==='');
    await page.evaluate(()=>fixture.open('main-turn','message',{conv:fixture.ids.main}));
    await exact('main-turn','Main planning').waitFor();
    await page.evaluate(()=>fixture.release());await page.waitForTimeout(180);
    await exact('main-turn','Main planning').waitFor();
    if(skin==='zoom'){
     await page.evaluate(()=>fixture.open('left-native','message',{conv:fixture.ids.main}));
     await page.waitForTimeout(150);await page.keyboard.press('Escape');
     await page.locator('.zoom-content').getByRole('button',{name:/Casey/}).first().waitFor();
     await page.evaluate(()=>{fixture.views[fixture.ids.main].messages.push({id:'left-native',lid:'left-native',topic:fixture.native,dir:'in',from:'casey/desktop',kind:'message',body:'History after leaving chat',at:'2026-10-06T12:00:00Z',state:'stored',actions:[]});fixture.tick();});
     await page.waitForTimeout(180);
     await page.locator('.zoom-content').getByRole('button',{name:/Casey/}).first().waitFor();
     assert.equal(await page.locator('.zoom-message').count(),0,'late history does not reopen a chat left with Escape');
    }
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body&&['/api/dm/send','/api/operator/decide','/api/topic/done'].includes(r.path)).length),0,'preview navigation executes no message or permission action');
    await shot('preview-stale-load');assert.deepEqual(errors,[]);console.log('preview route '+skin+' '+width+' PASS: exact latest/archived/Main/copy alias/delayed/repeated/stale-load/draft');await context.close();continue;
   }
   try { await shown(process.env.AGENTNET_ADDRESSING==='1'?'Review the fictional plan.':'Main planning').waitFor(); } catch(e) { await shot('failed');console.error(errors);console.error((await page.evaluate(()=>document.querySelector('#skin').shadowRoot.textContent)).slice(-5000));throw e; }
   if(process.env.AGENTNET_TOPIC_UNREAD==='1'){
    const reads=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='read').flatMap(r=>r.body.ids));
    const readFlag=(conv,id)=>page.evaluate(([conv,id])=>fixture.views[fixture.ids[conv]].messages.find(m=>m.id===id).unread,[conv,id]);
    const closeList=()=>list().getByRole('button',{name:/^(Close|Close all topics|Back to the conversation)$/}).click();
    const row=title=>skin==='comic'?list().getByRole('button',{name:new RegExp('^'+title)}):list().locator('.topic-row').filter({has:page.getByRole('button',{name:new RegExp('^'+title)})});
    const rowRead=async(title,unread)=>{await row(title).waitFor();await page.waitForFunction(({skin,title,unread})=>{const shadow=document.querySelector('#skin').shadowRoot;const dialog=shadow.querySelector('[role=dialog]');if(!dialog)return false;const button=[...dialog.querySelectorAll('button')].find(b=>(b.getAttribute('aria-label')||b.textContent).startsWith(title));const text=skin==='comic'?button?.getAttribute('aria-label'):button?.closest('.topic-row')?.textContent;return !!text&&text.includes('unread')===unread;},{skin,title,unread});};
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
    const groupNav=page.getByRole('navigation',{name:skin==='comic'?/^Topics with /:'Topics',exact:skin!=='comic'});
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
    const own=skin==='comic'?page.locator('[data-mid="waiting-turn"]'):skin==='classic'?page.locator('#m-waiting-turn'):shown('Waiting recipient proof');
    if(skin==='comic'){
     if(width===390){await own.getByRole('button',{name:'Message actions',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
     else{await own.locator('[role="group"]').hover();await own.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}
    }else if(skin==='classic')await own.getByRole('button',{name:'Remind me…',exact:true}).click();
    else{await own.click();await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
    const sheet=page.getByRole('dialog',{name:'Remind me later',exact:true});await sheet.waitFor();await shot('own-reminder-open');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/remind').length),0,'opening own reminder does not submit');
    await sheet.getByText(/In 30 minutes/).first().click();await sheet.getByRole('button',{name:'Remind me',exact:true}).click();await sheet.waitFor({state:'hidden'});
    const posted=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/remind').map(r=>r.body));assert.equal(posted.length,1);assert.equal(posted[0].id,'waiting-turn');assert(posted[0].due>Math.floor(Date.now()/1000),'future reminder');
    await page.reload();await page.waitForFunction(()=>window.ready);
    if(skin==='comic')await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Casey/}).first().click();
    else if(skin==='classic'){const chat=page.locator('.contact-item').getByRole('button',{name:/Casey/}).first();if(await chat.isVisible())await chat.click();}
    else {await page.getByRole('button',{name:'Everyone',exact:true}).click();await page.locator('.zoom-content .reminder-item').filter({hasText:'Waiting recipient proof'}).click();}
    if(skin==='zoom')await page.locator('.zoom-message').getByText('Waiting recipient proof',{exact:true}).waitFor();else await shown('Main planning').waitFor();
    const ownAgain=skin==='comic'?page.locator('[data-mid="waiting-turn"]'):skin==='classic'?page.locator('#m-waiting-turn'):page.locator('.zoom-content');
    await ownAgain.getByRole('button',{name:'Change…',exact:true}).click();await page.getByRole('dialog',{name:'Move the reminder',exact:true}).waitFor();await shot('own-reminder-reopened');
    assert.equal(await page.evaluate(()=>fixture.overview.reminders.filter(r=>r.message==='waiting-turn').length),1,'own reminder remains after reload');
    await page.getByRole('dialog',{name:'Move the reminder',exact:true}).getByRole('button',{name:/^(Close|Cancel)$/,exact:true}).click();
    if(skin==='zoom')await page.keyboard.press('Escape');
    const incoming=skin==='comic'?page.locator('[data-mid="main-turn"]'):skin==='classic'?page.locator('#m-main-turn'):shown('Main planning');
    if(skin==='comic'){if(width===390){await incoming.getByRole('button',{name:'Message actions',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}else{await incoming.locator('[role="group"]').hover();await incoming.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}}
    else if(skin==='classic')await incoming.getByRole('button',{name:'Remind me…',exact:true}).click();else{await incoming.click();await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
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
   if(skin==='classic')assert.equal(await page.locator('#hub-back').isVisible(),false,'flat person chat has no intermediate DM hierarchy');
   assert.equal(await page.getByRole('button',{name:/Conversations \(/}).count(),0,'no competing Conversations navigation');
   const waitingBefore=await page.evaluate(()=>structuredClone(fixture.views[fixture.ids.main].messages.find(m=>m.id==='waiting-turn')));
   if(skin==='comic'){
    await page.locator('[data-mid="waiting-turn"]').getByRole('button',{name:/Waiting for Casey to update AgentNet/}).click();
    const details=page.getByRole('dialog',{name:'Message details',exact:true});await details.waitFor();
    await details.getByText('Technical details',{exact:true}).click();
    assert((await details.innerText()).includes('casey/old-phone needs group-chat-v1'),'waiting Details exposes exact persisted recipient and feature');await shot('waiting-details');
    await details.getByRole('button',{name:'Close',exact:true}).click();await details.waitFor({state:'hidden'});
   }else{
    if(skin==='zoom')await shown('Waiting recipient proof').click();
    const details=page.locator('details.tech').filter({hasText:'casey/old-phone needs group-chat-v1'}).first();
    if(skin!=='zoom')await details.locator('summary').click();assert((await details.innerText()).includes('casey/old-phone needs group-chat-v1'),'waiting Details exposes exact persisted recipient and feature');await shot('waiting-details');
    if(skin==='zoom')await page.keyboard.press('Escape');else await details.locator('summary').click();
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
   if(skin==='comic'){await page.getByRole('button',{name:/Invoice review.*open topic, menu/}).click();await page.getByRole('menuitem',{name:'Mark done',exact:true}).click();}
   else await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:width===390?'Done':'Mark done',exact:true}).click();
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/topic/done'));
   assert.deepEqual(await page.evaluate(()=>{const {conv,id}=fixture.requests.find(r=>r.path==='/api/topic/done').body;return{conv,id};}),{conv:'a'.repeat(64),id:'3'.repeat(32)},'native action keeps its original root');
   assert.equal(await field().inputValue(),'Main draft stays','root draft preserved when entering native topic');
   await edit('Exact native send');
   if(skin==='comic')await page.getByRole('button',{name:'Send',exact:true}).click();else if(skin==='zoom'){await page.getByRole('button',{name:'Write in this DM…',exact:true}).click();await page.locator('#dialog-ok').click();}else await page.locator('#send').click();
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
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
