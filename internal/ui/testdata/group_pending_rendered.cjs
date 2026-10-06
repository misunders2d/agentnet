// Inert public-host fixture; production skins, no Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(name,address,key)=>({person:name.toLowerCase().padEnd(32,'a'),label:name,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const outside=person('Nora','nora/office','nora-key');
const me=person('Sergey','sergey/laptop','sergey-key'),vitalii=person('Vitalii','vitalii/desktop','vitalii-key');me.state='self';
const member=(pid,host,inviters)=>({pid,host,member:true,invited:at,state:'active',state_text:'Member of this group. Can be asked by every member; its owner decides what runs. Stays until explicitly removed.',host_here:host.address===me.address,inviter:inviters[0],inviters,shared:[],missing:0,tasks_from:[],can_ask:true,can_dismiss:true});
const thread={id:conv,kind:'group',title:'amazon_team',peer:{label:'amazon_team',address:'',state:''},role:'member',members:[{...me,admin:true},vitalii],frozen:'',agents:[{...member('source-pid',me,[me,vitalii]),agent_id:'a'.repeat(32)},{...member('target-pid',vitalii,[me]),agent_id:'b'.repeat(32)},{...member('outside-pid',outside,[me]),external:true,state_text:'Member of this group. Runs on Nora’s device, outside this group; that device receives every new message and file here until the agent is removed.'}],guests:[],messages:[{id:'selected',lid:'selected',from:vitalii.address,dir:'in',body:'Selected group message',kind:'message',at,group_ref:{lid:'selected',author:'vitalii-key',hash:'e'.repeat(64)},verified_agent:false},{id:'agent-ask',lid:'agent-ask',from:me.address,dir:'out',body:'Verified request to the other group agent',kind:'question',at,pid:'target-pid',agent_author_pid:'source-pid',origin:'agent:fixture',verified_agent:true,to:vitalii.address,target:{address:vitalii.address,fingerprint:vitalii.fingerprint,agent_id:'b'.repeat(32)}},{id:'agent-answer',lid:'agent-answer',from:vitalii.address,dir:'in',body:'Verified group answer',kind:'answer',at,pid:'target-pid',origin:'agent:fixture',verified_agent:true,reply_to:'agent-ask'}]};
const overview={version:'fixture',seq:1,me:{address:me.address,fingerprint:me.fingerprint,responder:'fixture'},person:me,persons:true,agents:true,files:true,controls:true,role:'person',people:[vitalii,outside],review:[],links:[],reminders:[],threads:[],dms:[{id:conv,kind:'group',title:'amazon_team',peer:thread.peer,count:3,unread:0}],directory:{current:true,members:[{address:me.address,presence:'connected'},{address:vitalii.address,presence:'offline'},{address:outside.address,presence:'connected'}]},quarantine:[]};
const bohdan=person('Bohdan','bohdan/windows','bohdan-key');
thread.members=[{...me,admin:true}]; thread.agents=[]; thread.messages=[];
overview.people=[bohdan]; overview.groups=true;
overview.group_invitations=[{id:'invite-1',conv,direction:'out',status:'pending',title:'amazon_team',inviter:me.person,target:bohdan.person,history:[]}];
const boot=`
const seed=${JSON.stringify({overview,thread})};window.fixture={...seed,requests:[]};
const variant=new URL(location.href).searchParams.get('case');
if(variant){fixture.thread.kind='dm';fixture.thread.peer=fixture.overview.people[0];fixture.thread.members=[];fixture.thread.agents=[{...fixture.thread.agents[0],member:false,state:variant==='dm-conflict'?'conflict':'active',state_text:variant==='dm-conflict'?'Its signed records disagree.':'Active in this DM; its owner decides what runs.'}];fixture.thread.messages=[];seed.thread=fixture.thread;}
let open,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P6 fixture',endpoint:location.origin,address:seed.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('fixture accepts no files');},file:async()=>{throw Error('fixture contains no files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
if(p.startsWith('/api/agents'))return {host:body?.host||new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address,local:!p.includes('host='),agents:fixture.thread.agents.filter(a=>a.agent_id&&a.host.address===(new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address)).map(a=>({record:{id:a.agent_id,label:a.host.address===seed.overview.me.address?'Prospect':'Analyst',host:a.host.address},enabled:true})),sessions:[]};
if(p==='/api/dm/agent/invite'){fixture.thread.agents[0].shared=body.share;changed?.({type:'change',seq:++fixture.overview.seq});return structuredClone(fixture.thread.agents[0]);}
if(p.includes('/groups/invitations'))return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};return {};
}};
const skin=new URL(location.href).searchParams.get('skin'),base='/assets/skins/'+skin+'/';
const manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);
window.openGroup=()=>open(seed.thread.id,'conversation');
window.setInvite=(status,joined=false)=>{fixture.overview.group_invitations[0].status=status;fixture.thread.members=joined?[{...fixture.overview.person,admin:true},fixture.overview.people[0]]:[{...fixture.overview.person,admin:true}];changed?.({type:'change',seq:++fixture.overview.seq});};window.ready=true;
`;
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
let browser;
const errors=[],shots=[];
try {
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 for(const skin of ['comic','classic','zoom']) for(const width of [1280,390]) {
  console.log('Checking '+skin+' '+width);
  const context=await browser.newContext({viewport:{width,height:900}});
  await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  const page=await context.newPage(); page.setDefaultTimeout(10000);
  page.on('pageerror',e=>errors.push(skin+': '+e.stack));
  await page.goto(origin+'/?skin='+skin); await page.waitForFunction(()=>window.ready); await page.evaluate(()=>openGroup());
  await page.getByText(/1 (?:current )?member.*1 invited/).and(page.locator(':visible')).first().waitFor();
  if(skin==='comic') {
   if(!await page.getByRole('list',{name:'Invited people',exact:true}).isVisible()) await page.getByRole('button',{name:'amazon_team. Who’s in this chat',exact:true}).click();
  } else if(skin==='zoom') { const d=page.locator('.zoom-agents'); await d.waitFor(); if(await d.getAttribute('open')===null) await d.locator(':scope > summary').click(); }
  const invited=skin==='comic'?page.getByRole('list',{name:'Invited people',exact:true}):page.locator('.invited-person:visible');
  await invited.waitFor();
  assert.match(await invited.innerText(),/Bohdan/);
  assert.match(await invited.innerText(),/waiting for them to accept/);
  await settle(page); const shot=path.join(evidence,skin+'-pending-person-'+width+'.png'); await page.screenshot({path:shot}); shots.push(shot);
  await page.evaluate(()=>setInvite('accepted'));
  await invited.getByText('Accepted · waiting to join',{exact:true}).waitFor();
  assert(await page.getByText(/1 (?:current )?member.*1 invited/).and(page.locator(':visible')).first().isVisible(),'acceptance alone must not count as membership');
  await page.evaluate(()=>setInvite('accepted',true));
  await invited.waitFor({state:'hidden'});
  await page.getByText(/2 (?:current )?members/).and(page.locator(':visible')).first().waitFor();
  for(const status of ['declined','revoked']) {
   await page.evaluate(()=>setInvite('pending'));
   await page.getByText(/1 (?:current )?member.*1 invited/).and(page.locator(':visible')).first().waitFor();
   await page.evaluate(s=>setInvite(s),status);
   await invited.waitFor({state:'hidden'});
   await page.getByText(/1 (?:current )?member/).and(page.locator(':visible')).first().waitFor();
  }
  assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),skin+' document overflow');
  assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body!==undefined && r.path!=="/api/refresh")),skin+' rendering must send no decisions or invites: '+JSON.stringify(await page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined && r.path!=="/api/refresh").map(r=>r.path))));
  await context.close();
 }
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({ok:true,checks:'Pending person discoverable in room/header; accepted not joined; joined, declined, revoked transitions; no decisions or invites (refresh allowed); desktop/phone, all skins',shots}));
} finally { if(browser) await browser.close(); server.close(); }
})().catch(e=>{console.error(e);process.exitCode=1;});
