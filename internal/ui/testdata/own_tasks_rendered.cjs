// The bundled Comic skin with the same inert host seam as agent_chat_rendered.
// No live profile, native executor, grant or session is changed.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const root=path.resolve(__dirname,'../static'),person='a'.repeat(32),other='b'.repeat(32);
const own={person,label:'Alex',address:'alex/laptop',state:'self',devices:[{address:'alex/laptop',name:'Laptop',human:true},{address:'alex/phone',name:'Phone',human:true},{address:'alex/desk',name:'Desk',human:true}]};
const overview={version:'fixture',seq:1,me:{address:own.address,fingerprint:'fixture',responder:'codex',responder_dir:'/fixture',agent:true},person:own,persons:true,agents:true,role:'person',people:[{person:other,label:'Alex',address:'other/desk',state:'pinned',devices:[{address:'other/desk'}]}],review:[],links:[],reminders:[],threads:[],dms:[],agent_devices:[],directory:{current:true,members:[]},quarantine:[]};
const boot=`
const seed=${JSON.stringify({overview,person,other})};
const browser=new URL(location.href).searchParams.has('browser');
window.fixture={overview:seed.overview,actions:[],tasks:sessionStorage.getItem('own-tasks')==='on',fail:true};
const host={version:1,platform:browser?'browser':'daemon',workspace:{id:'fixture',name:'Own task fixture',address:seed.overview.me.address},workspaces:null,skins:[],listen(){return()=>{};},onOpen(){},onSkinsChange(){return()=>{};},stage:async()=>{throw Error('No files');},file:async()=>{throw Error('No files');},api:async(p,body)=>{
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/approvals')return {questions:[],tasks:[{person:seed.other,label:'Alex',status:'active'},...(fixture.tasks?[{person:seed.person,label:'Alex',status:'active'}]:[])],native_tasks:[{address:'alex/desk',fingerprint:'native-exact-key',status:'active'}],participations:[]};
if(p==='/api/act'){
fixture.actions.push(structuredClone(body));
if(browser)throw Error('Browser has no native grant authority');
if(body.id!==seed.person)throw Error('Wrong person target');
if(body.do==='grant_tasks'&&fixture.fail){fixture.fail=false;throw Error('Fixture could not save this permission');}
if(body.do==='grant_tasks'||body.do==='revoke_tasks'){fixture.tasks=body.do==='grant_tasks';sessionStorage.setItem('own-tasks',fixture.tasks?'on':'off');fixture.overview.seq++;return 'Permission saved on Laptop.';}
throw Error('Unexpected action');
}
if(p==='/api/responder')return {chosen:true,manual:false,harness:'codex',dir:'/fixture',ready:true,harnesses:[{name:'codex',found:true}]};
if(p.startsWith('/api/agents'))return {host:seed.overview.me.address,local:true,agents:[],harnesses:[]};
if(p==='/api/groups/invitations')return [];if(p.includes('/topics'))return {topics:[],placements:[]};return {};
}};
const base='/assets/skins/comic/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const target=document.createElement('div');target.className='skin-root';shadow.append(target);await(await import(base+manifest.entry)).mount(target,host);window.ready=true;
`;
const server=http.createServer((req,res)=>{
 const u=new URL(req.url,'http://127.0.0.1');
 if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}
 if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}
 if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 res.statusCode=404;res.end('fixture route missing');
});
(async()=>{
 let browser;
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:1000}}),page=await context.newPage(),errors=[];page.setDefaultTimeout(8000);
   await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());page.on('pageerror',e=>errors.push(String(e)));
   const settings=async()=>{await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width<700?'You':'Settings',exact:true}).click();await page.getByRole('button',{name:/^Permissions/}).click();};
   await page.goto(origin);await page.waitForFunction(()=>window.ready);await settings();
   const on=()=>page.getByRole('button',{name:'Turn on tasks without asking for My devices',exact:true});
   await on().waitFor();assert(await page.getByText(/Permissions on your Laptop/).count(),'receiving host named');
   assert(await page.getByText(/All your verified devices, including phones/).count(),'verified scope');
   assert(await page.getByText(/Direct tasks allowed/).count(),'native consent shown while off');
   await on().click();await page.getByRole('button',{name:'Allow future tasks',exact:true}).click();
   await page.getByText(/Fixture could not save this permission/).waitFor();await on().waitFor();
   await on().click();assert(await page.getByText(/native approval requirements still apply/).count(),'native boundary explained');
   await page.getByRole('button',{name:'Allow future tasks',exact:true}).click();
   await page.getByRole('button',{name:'Turn off tasks without asking for My devices',exact:true}).waitFor();
   assert.deepEqual(await page.evaluate(()=>fixture.actions),[{do:'grant_tasks',id:person},{do:'grant_tasks',id:person}],'exact own person, failed grant not shown as applied');
   await page.reload();await page.waitForFunction(()=>window.ready);await settings();
   await page.getByRole('button',{name:'Turn off tasks without asking for My devices',exact:true}).click();
   assert(await page.getByText(/separate device permission or accepted agent invitation/).count(),'scope of removal explained');
   await page.getByRole('button',{name:'Remove permission',exact:true}).click();await on().waitFor();
   assert.deepEqual(await page.evaluate(()=>fixture.actions),[{do:'revoke_tasks',id:person}]);
   await page.getByRole('button',{name:'Turn off tasks without asking for Alex',exact:true}).waitFor();
   assert(await page.getByText(/Direct tasks allowed/).count(),'separate consent remains visible');
   assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'no horizontal overflow');
   await page.getByRole('alertdialog').waitFor({state:'hidden'});
   await page.evaluate(async()=>{await document.fonts.ready;const root=document.querySelector('#skin').shadowRoot;await Promise.all(root.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true,mode:0o700});await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'own-tasks-'+width+'.png')});}
   await page.goto(origin+'/?browser=1');await page.waitForFunction(()=>window.ready);await settings();
   await page.getByText('This browser runs nothing. Permissions belong to the computer that runs your agent.').waitFor();
   assert.equal(await page.getByRole('button',{name:/tasks without asking/}).count(),0,'no unsupported browser action');
   assert.deepEqual(errors,[]);await context.close();console.log('Own task permissions',width,'PASS');
  }
 }finally{if(browser)await browser.close();await new Promise(r=>server.close(r));}
 console.log('Own task permissions rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
