// Inert public-host fixture; production skins, no Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const at='2026-10-06T10:00:00Z',local='sergey/laptop',remote='sergey/zenbook',localID='a'.repeat(32),remoteID='b'.repeat(32);
const me={person:'sergey',label:'Sergey',address:local,state:'self',devices:[{address:local,fingerprint:'local-key'},{address:remote,fingerprint:'remote-key'},{address:'sergey/iphone',fingerprint:'phone-key'}]};
const overview={version:'fixture',seq:1,me:{address:local,fingerprint:'local-key',responder:'codex',responder_dir:'/fixture',agent:true},person:me,persons:true,agents:true,role:'person',people:[],review:[],links:[],reminders:[],threads:[],dms:[],agent_devices:[remote],directory:{current:true,members:[]},quarantine:[],files:{max_count:8,max_file:100000,max_message:200000}};
if(true){overview.model_reports=[{host:local,host_key:"local-key",agent_id:"",model:"gpt-6.1-sol",harness:"codex",executor:"a".repeat(64),at:9007199254740991,revision:1},{host:remote,host_key:"remote-key",agent_id:remoteID,model:"Claude Opus 4.6 "+"x".repeat(90),harness:"claude",executor:"b".repeat(64),at:1791280800,revision:1}];}
const responder={chosen:true,manual:false,harness:'codex',dir:'/fixture',ready:true,harnesses:[{name:'codex',found:true}]};
const record=(host,id,label)=>({v:1,id,host,host_key:host===local?'local-key':'remote-key',label,ts:1});
const boot=`
const seed=${JSON.stringify({overview,responder,local,remote,localID,remoteID,assignMode:!!process.env.AGENTNET_ASSIGN_MESSAGE})};window.fixture={...seed,requests:[],threads:{},staged:[],fileReads:0,disabled:false,badRemote:false};
if(new URLSearchParams(location.search).has("browser")){fixture.overview.me={address:"sergey/iphone",fingerprint:"phone-key",browser:true,agent:false,responder:"",responder_dir:""};fixture.overview.agent_devices.push(seed.local);}
let opened,changed;
const host={version:1,platform:new URLSearchParams(location.search).has('browser')?'browser':'daemon',workspace:{id:'default',name:'Agent starter fixture',endpoint:location.origin,address:seed.local,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){opened=fn;},listen(fn){changed=fn;return()=>{};},stage:async file=>{fixture.staged.push({name:file.name,text:await file.text()});return 'upload-'+fixture.staged.length;},file:async()=>{fixture.fileReads++;return {bytes:new TextEncoder().encode('Reviewed source file')};},api:async(p,body)=>{
fixture.requests.push({path:p,body});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/responder')return structuredClone(fixture.responder);
if(p.startsWith('/api/agents')){const h=new URL(p,location.origin).searchParams.get('host'),remote=!!h;return {host:remote?(fixture.badRemote?'wrong/computer':h):seed.local,local:!remote,harnesses:seed.responder.harnesses,agents:remote?[{record:{v:1,id:h===seed.local?seed.localID:seed.remoteID,host:h,host_key:h===seed.local?"local-key":"remote-key",label:h===seed.local?"Laptop agent":"Zenbook agent",ts:1},enabled:true},{record:{v:1,id:"e".repeat(32),host:h,host_key:"remote-key",label:"Unknown remote agent",ts:1},enabled:true}]:[{record:${JSON.stringify(record(local,localID,'Laptop agent'))},enabled:!fixture.disabled,responder:fixture.responder},{record:${JSON.stringify(record(local,'c'.repeat(32),'Sleeping agent'))},enabled:true,responder:{...fixture.responder,ready:false,problem:'Not ready in fixture'}}]};}
if(p==='/api/send'){if(fixture.failNextSend){fixture.failNextSend=false;throw Error('Temporary send failure');}const id=(Object.keys(fixture.threads).length+1).toString(16).padStart(32,'0'),message={id,dir:'out',from:seed.local,to:body.to,kind:body.kind,body:body.body,at:'${at}',state:'queued',author:{label:'You',about:''},files:seed.assignMode&&body.body==='Default question'?[{name:'source.txt',size:20,openable:true},{name:'missing.txt',size:40,openable:false}]:undefined,target:body.agent_id?{address:body.to,fingerprint:body.to===seed.local?'local-key':'remote-key',agent_id:body.agent_id}:undefined};const summary={id,peer:body.to,title:body.body,last:body.body,last_at:'${at}',count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:'${at}',agent_id:body.agent_id};fixture.threads[id]={id,peer:body.to,key:{pinned:'fixture-key'},approved:true,task_grant:'',messages:[message],topic:summary};fixture.overview.threads.push(summary);fixture.overview.seq++;return {id,state:'queued',path:'fixture'};}
if(p.startsWith('/api/thread?'))return structuredClone(fixture.threads[new URL(p,location.origin).searchParams.get('id')]);
if(p==='/api/groups/invitations')return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};if(p==='/api/permissions')return {questions:[],tasks:[]};return {};
}};
const base='/assets/skins/'+(new URLSearchParams(location.search).get('skin')||'comic')+'/',manifest=await(await fetch(base+'skin.json')).json();
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
  for(const skin of process.env.AGENTNET_MODEL_SKINS?process.env.AGENTNET_MODEL_SKINS.split(','):['comic','classic','zoom'])for(const width of [1280,390])for(const mobileBrowser of [false,true]){
   const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage(),errors=[];page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.stack));
   await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
   await page.goto(origin+'/?skin='+skin+(mobileBrowser?'&browser=1':''));await page.waitForFunction(()=>window.ready);
   if(skin==='comic'){await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Agents',exact:true}).click();await page.getByText(/^Last reported model: Claude Opus 4.6/).waitFor();await page.getByText(/^Last reported model: gpt-6.1-sol/).waitFor();}
   else{await page.locator('#profile-btn').click();await page.locator('[data-settings="device"]').click();await page.locator('#responder').locator('..').locator('summary').click();await page.locator('#responder').getByText(/^Last reported model: Claude Opus 4.6/).waitFor();await page.locator('#responder').getByText(/^Last reported model: gpt-6.1-sol/).first().waitFor();}
   await settle(page);
   const visibleRows=page.locator('[data-agent-model]:visible');assert(await visibleRows.count()>=2,'private model rows are discoverable '+JSON.stringify({skin,width,mobileBrowser,count:await visibleRows.count()}));
   assert(await page.getByText('Model unknown',{exact:true}).count()>=1,'absent reports remain unknown');
   assert.match(await page.getByText(/^Last reported model: gpt-6.1-sol/).first().innerText(),/Timestamp unavailable/,'signed timestamp beyond Date range never crashes UI');
   assert.equal(await page.getByRole('combobox',{name:/model/i}).count(),0,'metadata is readonly');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body!==undefined)),'opening model status never calls native model/tools or changes settings');
   for(const row of await visibleRows.all()){const b=await row.boundingBox();assert(b&&b.x>=-1&&b.x+b.width<=width+1,'model rows fit viewport');}
   await settle(page);const shot=path.join(evidence,skin+'-model-status-'+width+(mobileBrowser?'-browser':'-native')+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'no model status horizontal overflow');assert.deepEqual(errors,[]);await context.close();
  }
  console.log(JSON.stringify({ok:true,checks:'Private readonly model report/unknown metadata; Comic/Classic/Zoom; desktop390/1280; native/browser',shots}));
 }finally{if(browser)await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
