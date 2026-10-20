// Production host hash routing and Comic, disposable fictional daemon data.
// Native OS notification callback/focus and Rust dispatch are separate checks.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const assets=path.resolve(__dirname,'../static'),web=path.resolve(__dirname,'../web');
const evidence=process.env.AGENTNET_SCREENSHOTS;
if(evidence)fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const conv='a'.repeat(64),other='b'.repeat(64),target='c'.repeat(32),missing='d'.repeat(32),topic='e'.repeat(32),handle='f'.repeat(32);
const at='2026-10-20T12:00:00Z',me={person:'1'.repeat(32),label:'Tester',address:'tester/laptop',state:'self'},peer={person:'2'.repeat(32),label:'Casey',address:'casey/desktop',state:'pinned',devices:[]};
let fixture,bundle,browser;
function reset(){
 const message=(id,body,extra={})=>({id,lid:id,dir:'in',from:peer.address,kind:'message',body,at,unread:true,actions:[],reactions:[],...extra});
 const messages=Array.from({length:120},(_,i)=>message(i.toString(16).padStart(32,'0'),'Context '+i+' '+('longer message context '.repeat(7)),{topic}));
 messages.splice(50,0,message(target,'Exact notified message',{topic}));
 const view=(id,messages)=>({id,created:at,kind:'dm',peer,mine:true,role:'member',members:[me,peer],agents:[],guests:[],frozen:'',messages,topics:id===conv?[{id:topic,conv,title:'Invoices',last:'Exact notified message',last_at:at,count:messages.length,unread:messages.length,state:'active',pending:false,review:0,running:0}]:[]});
 fixture={views:{[conv]:view(conv,messages),[other]:view(other,[message('9'.repeat(32),'Other chat context')])},requests:[],events:new Set(),held:[],holdOverview:false,holdDM:false,seq:1,uploads:0};
 fixture.overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:{max_count:8,max_file:1048576,max_message:2097152},controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[conv,other].map(id=>({id,created:at,peer,kind:'dm',role:'member',count:fixture.views[id].messages.length,title:id===conv?'Invoices':'Other chat',last:id===conv?'Exact notified message':'Other chat context',last_at:at,unread:1,held:0,waiting:0,guests:0})),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
}
const json=(res,value)=>{res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const release=()=>{if(!fixture)return;fixture.holdOverview=false;fixture.holdDM=false;for(const fn of fixture.held.splice(0))fn();};
const push=(kind='change')=>{for(const res of fixture.events)res.write('event: '+kind+'\ndata: '+(++fixture.seq)+'\n\n');};
const server=http.createServer(async(req,res)=>{
 const url=new URL(req.url,'http://127.0.0.1'),p=url.pathname;
 if(p==='/favicon.ico'||/^\/assets\/icon-(192|512)\.png$/.test(p)){res.writeHead(204);res.end();return;}
 if(p==='/'){res.writeHead(200,{'Content-Type':'text/html'});res.end(fs.readFileSync(path.join(assets,'index.html')));return;}
 if(p==='/assets/skins/index.json'){json(res,[{...JSON.parse(fs.readFileSync(path.join(assets,'skins/comic/skin.json'))),digest:'0'.repeat(64)}]);return;}
 if(p==='/assets/skins/comic/entry.mjs'&&bundle){res.writeHead(200,{'Content-Type':'text/javascript'});res.end(bundle);return;}
 if(p.startsWith('/assets/')){const file=path.resolve(assets,'.'+p.slice(7));if(file.startsWith(assets+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.writeHead(200,{'Content-Type':/\.(mjs|js)$/.test(file)?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream'});res.end(fs.readFileSync(file));return;}}
 if(p==='/api/workspaces'){json(res,[{id:'default',handle,state:'enrolled',name:'Fictional workspace',address:me.address,realm:'3'.repeat(32),endpoint:'https://fixture.invalid'}]);return;}
 if(p.endsWith('/events')){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache'});res.write(': ready\n\n');fixture.events.add(res);req.on('close',()=>fixture.events.delete(res));return;}
 const api=p.replace(/^\/workspaces\/default\/[a-f0-9]{32}/,'');
 if(api.startsWith('/api/')){
  const bytes=[];for await(const chunk of req)bytes.push(chunk);const raw=Buffer.concat(bytes);let body;
  if(raw.length&&req.headers['content-type']==='application/json')body=JSON.parse(raw);
  fixture.requests.push({path:p,api,body,at:Date.now()});
  if(api==='/api/overview'){if(fixture.holdOverview)await new Promise(resolve=>fixture.held.push(resolve));json(res,fixture.overview);return;}
  if(api==='/api/dm'){if(fixture.holdDM)await new Promise(resolve=>fixture.held.push(resolve));json(res,fixture.views[url.searchParams.get('id')]);return;}
  if(api==='/api/upload'){fixture.uploads++;json(res,{id:'fixture-stage-'+fixture.uploads});return;}
  if(api==='/api/act'&&body?.do==='read'){for(const view of Object.values(fixture.views))for(const m of view.messages)if(body.ids.includes(m.id))m.unread=false;json(res,{note:'Read.'});return;}
  if(api==='/api/agents'){json(res,{agents:[],sessions:[],local:false});return;}
  if(api==='/api/groups/invitations'){json(res,[]);return;}
  if(api==='/api/typing/status'){json(res,{send:false,scopes:[]});return;}
  if(api==='/api/topics'){json(res,{topics:fixture.views[url.searchParams.get('conv')]?.topics||[],matched:1});return;}
  json(res,{});return;
 }
 res.writeHead(404);res.end('No fictional route');
});
async function sourceBundle(){
 if(!process.env.AGENTNET_NOTIFICATION_SOURCE)return;
 const replacements={'optimistic.mjs':'../static/optimistic.mjs','pictures.mjs':'../static/pictures.mjs','person-topics.mjs':'../skins/shared/person-topics.mjs','reaction-preferences.mjs':'../skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'../skins/shared/collective-mentions.mjs','drive-space.mjs':'../skins/shared/drivespace.mjs','drive-setup.mjs':'../skins/shared/drivespace-setup.mjs'};
 const result=await require('../web/node_modules/esbuild').build({entryPoints:[path.join(web,'src/main.tsx')],bundle:true,write:false,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',minify:true,define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'build-inputs',setup(build){
  build.onResolve({filter:/\.mjs$/},args=>{const name=path.basename(args.path);if(replacements[name]&&!fs.existsSync(path.resolve(args.resolveDir,args.path)))return{path:path.resolve(web,replacements[name])};});
  if(process.env.AGENTNET_NOTIFICATION_SOURCE!=='1')build.onLoad({filter:/\/web\/src\/store\.ts$/},()=>({contents:fs.readFileSync(process.env.AGENTNET_NOTIFICATION_SOURCE,'utf8'),loader:'ts',resolveDir:path.join(web,'src')}));
 }}]});bundle=result.outputFiles[0].text;
}
const hash=id=>'msg='+id+'&conv='+conv+'&dir=in&workspace=default';
async function visibleTarget(page,id){
 await page.waitForFunction(id=>{const root=document.getElementById('skin')?.shadowRoot;for(const n of root?.querySelectorAll('[data-mid="'+id+'"]')||[]){const r=n.getBoundingClientRect(),box=n.closest('[role=log]')?.parentElement.getBoundingClientRect();if(!n.closest('[inert]')&&r.width&&box&&r.top<box.bottom&&r.bottom>box.top)return true;}return false;},id);
}
(async()=>{try{
 await sourceBundle();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 for(const width of [1280,390]){
  reset();const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.type()+': '+m.text());});
  await context.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort());
  // Cold hash entry uses the production host while its initial overview waits.
  fixture.holdOverview=true;await page.goto(origin+'/#'+hash(target));await visibleTarget(page,target);
  assert(fixture.holdOverview,'cold exact frame precedes unrelated overview completion');release();
  const field=()=>page.getByRole('textbox',{name:'Message Casey',exact:true});await field().fill('Draft survives notification');
  await page.locator('input[type=file]').setInputFiles({name:'unsent.txt',mimeType:'text/plain',buffer:Buffer.from('kept file')});
  await page.getByText('unsent.txt',{exact:true}).waitFor();
  await page.evaluate(other=>location.hash='conv='+other+'&workspace=default',other);await page.getByText('Other chat context',{exact:true}).last().waitFor();
  fixture.holdOverview=true;fixture.holdDM=true;push();const begin=Date.now();
  const comparison=process.env.AGENTNET_NOTIFICATION_COMPARE==='1';
  const releaseTimer=comparison?setTimeout(release,150):null;
  await page.evaluate(value=>{window.fixturePage=crypto.randomUUID();window.fixtureTap=performance.now();location.hash=value;},hash(target));await visibleTarget(page,target);
  if(releaseTimer)clearTimeout(releaseTimer);
  const cachedMs=Date.now()-begin,cachedBeforeRelease=fixture.holdOverview&&fixture.holdDM;
  if(!comparison)assert(cachedBeforeRelease,'cached target renders before either blocked response');
  assert.equal(await field().inputValue(),'Draft survives notification');await page.getByText('unsent.txt',{exact:true}).waitFor();
  // The exact row is visible; unseen earlier/later context remains unread.
  await page.waitForFunction(()=>document.hasFocus());
  const reads=fixture.requests.filter(r=>r.api==='/api/act'&&r.body?.do==='read').flatMap(r=>r.body.ids);
  assert(!reads.includes('0'.repeat(32))&&!reads.includes((119).toString(16).padStart(32,'0')),'offscreen rows are never acknowledged');
  release();await page.waitForTimeout(80);
  // Missing target keeps useful bound context and exact Retry/Dismiss state.
  await page.evaluate(value=>location.hash=value,hash(missing));await page.getByRole('button',{name:'Try again',exact:true}).waitFor();
  assert.equal(await field().inputValue(),'Draft survives notification');await page.getByText('unsent.txt',{exact:true}).waitFor();
  assert(!fixture.requests.filter(r=>r.api==='/api/act').some(r=>r.body?.ids?.includes(missing)),'missing message is never marked read');
  fixture.views[conv].messages.push({id:missing,lid:missing,dir:'in',from:peer.address,kind:'message',body:'Exact message arrived during catch-up',topic,at,unread:true,actions:[],reactions:[]});push('restart');
  await visibleTarget(page,missing);await page.getByRole('button',{name:'Try again',exact:true}).waitFor({state:'hidden'});
  const marker=await page.evaluate(()=>window.fixturePage);await page.evaluate(value=>location.hash=value,'msg='+target+'&conv='+conv+'&workspace='+'7'.repeat(32));
  await page.getByText('A notification is for a workspace that isn’t on this device.',{exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>window.fixturePage),marker,'warm routing retains the same page');assert.equal(await field().inputValue(),'Draft survives notification');
  assert(fixture.requests.filter(r=>r.api==='/api/dm').every(r=>r.path.startsWith('/workspaces/default/'+handle+'/')),'all history reads use the exact captured workspace/handle');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);assert.deepEqual(errors,[]);
  if(evidence)await page.screenshot({path:path.join(evidence,'notification-landing-'+width+'.png')});
  console.log(JSON.stringify({flow:'production hash -> Comic exact cold/cached -> missing -> restart catch-up',width,cachedFrameMs:cachedMs,cachedBeforeRelease,source:!!bundle,pageTitle:await page.title(),pageErrors:errors,result:'PASS'}));
  await context.close();for(const res of fixture.events)res.end();
 }
}finally{release();if(browser)await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}})().catch(error=>{console.error(error);process.exitCode=1;});
