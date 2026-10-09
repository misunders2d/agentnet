// Inert public-host fixture; production skins, no Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const at='2026-10-06T10:00:00Z',local='sergey/laptop',remote='sergey/zenbook',localID='a'.repeat(32),remoteID='b'.repeat(32);
const me={person:'sergey',label:'Sergey',address:local,state:'self',devices:[{address:local,fingerprint:'local-key'},{address:remote,fingerprint:'remote-key'},{address:'sergey/iphone',fingerprint:'phone-key'}]};
const overview={version:'fixture',seq:1,me:{address:local,fingerprint:'local-key',responder:'codex',responder_dir:'/fixture',agent:true},person:me,persons:true,agents:true,role:'person',people:[],review:[],links:[],reminders:[],threads:[],dms:[],agent_devices:[remote],directory:{current:true,members:[]},quarantine:[]};
const responder={chosen:true,manual:false,harness:'codex',dir:'/fixture',ready:true,harnesses:[{name:'codex',found:true}]};
const record=(host,id,label)=>({v:1,id,host,host_key:host===local?'local-key':'remote-key',label,ts:1});
const boot=`
const seed=${JSON.stringify({overview,responder,local,remote,localID,remoteID})};window.fixture={...seed,requests:[],threads:{},disabled:false,badRemote:false};
let opened,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'Agent starter fixture',endpoint:location.origin,address:seed.local,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){opened=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('No files');},file:async()=>{throw Error('No files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/responder')return structuredClone(fixture.responder);
if(p.startsWith('/api/agents')){const h=new URL(p,location.origin).searchParams.get('host'),remote=!!h;return {host:remote?(fixture.badRemote?'wrong/computer':h):seed.local,local:!remote,harnesses:seed.responder.harnesses,agents:remote?[{record:${JSON.stringify(record(remote,remoteID,'Zenbook agent'))},enabled:true}]:[{record:${JSON.stringify(record(local,localID,'Laptop agent'))},enabled:!fixture.disabled,responder:fixture.responder},{record:${JSON.stringify(record(local,'c'.repeat(32),'Sleeping agent'))},enabled:true,responder:{...fixture.responder,ready:false,problem:'Not ready in fixture'}}]};}
if(p==='/api/send'){const id='question-'+(Object.keys(fixture.threads).length+1),message={id,dir:'out',from:seed.local,to:body.to,kind:body.kind,body:body.body,at:'${at}',state:'queued',author:{label:'You',about:''},target:body.agent_id?{address:body.to,fingerprint:body.to===seed.local?'local-key':'remote-key',agent_id:body.agent_id}:undefined};const summary={id,peer:body.to,title:body.body,last:body.body,last_at:'${at}',count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:'${at}',agent_id:body.agent_id};fixture.threads[id]={id,peer:body.to,key:{pinned:'fixture-key'},approved:true,task_grant:'',messages:[message],topic:summary};fixture.overview.threads.push(summary);fixture.overview.seq++;return {id,state:'queued',path:'fixture'};}
if(p.startsWith('/api/thread?'))return structuredClone(fixture.threads[new URL(p,location.origin).searchParams.get('id')]);
if(p==='/api/groups/invitations')return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};if(p==='/api/permissions')return {questions:[],tasks:[]};return {};
}};
const base='/assets/skins/comic/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);window.ready=true;
`;
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
 let browser;const shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage(),errors=[];page.setDefaultTimeout(10000);
   await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());page.on('pageerror',e=>errors.push(e.stack));
   await page.goto(origin);await page.waitForFunction(()=>window.ready);
   const nav=page.getByRole('navigation',{name:'Main'}),sent=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/send').map(r=>r.body));
   await nav.getByRole('button',{name:'Agents',exact:true}).click();
   await page.getByRole('button',{name:'Chat with Laptop agent',exact:true}).waitFor();
   assert(await page.getByRole('button',{name:'Chat with Sleeping agent',exact:true}).isDisabled());
   assert(await page.getByRole('button',{name:'Chat with Zenbook agent',exact:true}).isVisible(),'own remote agent discoverable without threads');
   const yours=page.getByRole('region',{name:'Your agents',exact:true});
   await yours.getByText('Default agent',{exact:true}).waitFor({timeout:2000});
   await yours.getByText('Used when a request to this computer doesn’t name an agent.',{exact:true}).waitFor();
   const localCard=yours.locator('article').filter({has:page.getByRole('button',{name:'Chat with Laptop agent',exact:true})});
   await localCard.getByText(/^On /).waitFor();
   const localLast=await page.getByRole('button',{name:'Chat with Sleeping agent',exact:true}).boundingBox(),remoteFirst=await page.getByRole('button',{name:'Chat with Zenbook agent',exact:true}).boundingBox();
   assert(localLast && remoteFirst && localLast.y<remoteFirst.y,'local managed agents stay together before other-device agents');
   assert.deepEqual(await sent(),[],'discovery must send nothing');
   await settle(page);const agentsShot=path.join(evidence,'comic-agent-list-'+width+'.png');await page.screenshot({path:agentsShot});shots.push(agentsShot);
   await page.getByRole('button',{name:'Chat with Laptop agent',exact:true}).click();
   let sheet=page.getByRole('dialog',{name:'Chat with Laptop agent',exact:true});
   await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Keep my named question');
   await page.evaluate(()=>fixture.disabled=true);await sheet.getByRole('button',{name:'Send question',exact:true}).click();
   await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/selected agent is unavailable/);
   assert.equal(await sheet.getByRole('textbox',{name:'Your message',exact:true}).inputValue(),'Keep my named question');assert.deepEqual(await sent(),[],'disabled selected agent must not become default');
   await page.evaluate(()=>fixture.disabled=false);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});
   await page.locator('[data-mid="question-1"]').waitFor();
   assert.match(await page.locator('[data-mid="question-1"]').innerText(),/Keep my named question/);
   const first=(await sent())[0];assert.equal(first.to,local);assert.equal(first.agent_id,localID);assert.equal(first.kind,'question');
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:/^Chats/}).click();await page.getByRole('button',{name:'New',exact:true}).click();
   const newChat=page.getByRole('dialog',{name:'New chat',exact:true});await newChat.getByRole('button',{name:'Chat with Zenbook agent',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Zenbook agent',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Question for Zenbook');
   await page.evaluate(()=>fixture.badRemote=true);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/does not match/);assert.equal((await sent()).length,1);
   await page.evaluate(()=>fixture.badRemote=false);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});await newChat.waitFor({state:'hidden'});
   const second=(await sent())[1];assert.equal(second.to,remote);assert.equal(second.agent_id,remoteID);
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:'Agents',exact:true}).click();await page.getByRole('button',{name:'Chat with Your agent',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Your agent',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Default question');await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});
   const third=(await sent())[2];assert.equal(third.to,local);assert.equal(third.agent_id,undefined);
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body!==undefined && ['/api/dm/new','/api/groups/new','/api/dm/agent/invite'].includes(r.path))),'starter must not create human chats or invite');
   await settle(page);const shot=path.join(evidence,'comic-agent-starter-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'document overflow');assert.deepEqual(errors,[]);await context.close();
  }
  if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'));fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  console.log(JSON.stringify({ok:true,checks:'No-history agent discovery; local default and exact local/remote named question; fresh readiness/catalog refusal keeps draft, no default substitution; no human DM/group; desktop/phone',shots}));
 }finally{if(browser)await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
