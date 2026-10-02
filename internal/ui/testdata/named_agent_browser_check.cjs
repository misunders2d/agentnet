// Rendered UI only: production page/CSS/logic, synthetic captured workspace providers.
// No existing browser or real Hub/model. Use installed AGENTNET_PLAYWRIGHT.
const fs=require('fs'), http=require('http'), path=require('path'), assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const base=path.resolve(__dirname,'../static'), A='a'.repeat(32), B='b'.repeat(32), C='c'.repeat(32);
const person={person:'p-me',label:'Me',address:'me/laptop',state:'self',fingerprint:'SHA256:me'}, peer={person:'p-alice',label:'Alice',address:'alice/desk',state:'pinned',fingerprint:'SHA256:alice'};
const record=(id,host='alice/desk')=>({v:1,id,host,host_key:'SHA256:alice',label:id===A?'Builder A':id===B?'Builder B':'Local builder',ts:1});
const responder={chosen:true,harness:'codex',dir:'/tmp/synthetic-agent',timeout:77,context:['/tmp/synthetic-context'],ready:true};
const harnesses=[{name:'codex',found:true},{name:'pi',found:false}];
const message=(id,extra={})=>({id,dir:'in',from:'alice/desk',to:'me/laptop',kind:'message',body:'Synthetic conversation',at:'2026-09-30T12:00:00Z',author:{label:'alice/desk',about:'Synthetic host assertion'},actions:[],...extra});
const thread={id:'a1',peer:'alice/desk',key:{pinned:'SHA256:alice'},messages:[message('a1'),message('answer',{kind:'answer',agent_id:B,body:'Named reply',reply_to:'a1'})]};
const participation=(pid,id)=>({pid,agent_id:id,host:peer,host_here:false,inviter:person,state:'active',state_text:'Active',shared:[],missing:0,tasks_from:[],can_ask:true,can_dismiss:true});
const dm={id:'d1',peer,created:'2026-09-30T12:00:00Z',messages:[message('dm1',{origin:'ui'}),message('dm2',{agent_id:A,origin:'agent:codex',kind:'answer',body:'Named DM reply'})],agents:[participation('pid-A',A),participation('pid-B',B)]};
let agents=[], remote=[record(A),record(B)], published=false, requests=[], browserMode=false;
const bootstrap=`
const states={A:{},B:{}}, listeners=new Set();let active='A', hooks={};
const host=id=>Object.freeze({platform:${'__PLATFORM__'},skins:[],onOpen:()=>{},workspace:{id},api:async(path,body)=>{const r=await fetch('/fixture-api',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({workspace:id,path,body})});if(!r.ok)throw Error(await r.text());return r.json();},listen:()=>()=>{},workspaces:{active:()=>active,has:id=>id==='A'||id==='B',list:()=>[{id:'A'},{id:'B'}],state:id=>states[id],onChange:f=>{listeners.add(f);return()=>listeners.delete(f)},select:id=>{const previous=active;if(hooks.capture)hooks.capture(previous,states[previous]);active=id;window.agentnet=host(id);listeners.forEach(f=>f({id,previous,state:states[id]}));}}});
window.agentnet=host('A');window.agentnetWorkspace={hook:h=>{hooks=h;if(h.switched)listeners.add(h.switched)}};
window.agentnetModules={typing:{mountTyping:()=>({setScope:async()=>{},stop(){},disconnect(){},destroy(){},showSettings(){}})}};
window.fixture={select:id=>window.agentnet.workspaces.select(id)};`;
const server=http.createServer(async(req,res)=>{
 const url=new URL(req.url,'http://fixture');let content,type='text/javascript';
 if(url.pathname==='/fixture-api'){
  let raw='';for await(const chunk of req)raw+=chunk;const request=JSON.parse(raw);requests.push(request);
  const {body}=request,u=new URL(request.path,'http://fixture');let data={};
  if(u.pathname==='/api/overview') data={demo:false,me:{address:'me/laptop',fingerprint:'SHA256:me',browser:browserMode},...(browserMode?{device:{online:true,revoked:false}}:{}),person,people:[peer],agents:true,threads:[{id:'a1',peer:'alice/desk',title:'Synthetic conversation',last_at:'2026-09-30T12:00:00Z',count:2,unread:0,review:0}],dms:[{id:'d1',peer,title:'Synthetic DM',last_at:'2026-09-30T12:00:00Z',unread:0}],review:[],quarantine:[],seq:0};
  else if(u.pathname==='/api/thread') data=thread;
  else if(u.pathname==='/api/dm') data=dm;
  else if(u.pathname==='/api/responder') data={...responder,harnesses};
  else if(u.pathname==='/api/agents'){
   const address=u.searchParams.get('host');
   if(!body) data=address?{host:address,local:false,agents:remote.filter(a=>a.host===address).map(record=>({record,enabled:true}))}:{host:'me/laptop',local:true,agents,harnesses};
   else{let agent=agents.find(a=>a.record.id===body.id);if(body.action==='create'){agent={record:record(C,'me/laptop'),enabled:true,responder:{...responder,harness:body.harness,dir:body.dir}};agents.push(agent)}
    if(body.action==='update')agent.responder={...agent.responder,harness:body.harness,dir:body.dir};if(body.action==='disable'){agent.enabled=false;delete agent.responder}if(body.action==='publish')published=true;data={saved:true,published,agent,note:published?'Hub confirmation recorded.':'Hub confirmation unavailable.'};}
  }else if(u.pathname==='/api/send'||u.pathname.endsWith('/ask')||u.pathname.endsWith('/invite'))data={id:'a1',state:'custody'};
  else if(u.pathname==='/api/refresh')data={text:'Synthetic connection unknown'};
  res.setHeader('Content-Type','application/json');res.end(JSON.stringify(data));return;
 }
 if(req.method!=='GET'){res.writeHead(405).end();return;}
 if(url.pathname==='/'){type='text/html';content='<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentNet named agent UI fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/app.css">'+fs.readFileSync(path.join(base,'default.html'),'utf8')+'<script src="/fixture.js"></script><script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>';}
 else if(url.pathname==='/fixture.js')content=bootstrap.replace('__PLATFORM__',JSON.stringify(browserMode?'browser':'daemon'));
 else if(['/assets/app.css','/assets/app.js','/assets/lenses.js','/assets/icon-192.png'].includes(url.pathname)){type=url.pathname.endsWith('.css')?'text/css':url.pathname.endsWith('.png')?'image/png':'text/javascript';content=fs.readFileSync(path.join(base,url.pathname.endsWith('.png')?'ant.png':path.basename(url.pathname)));}
 else{res.writeHead(404).end();return;}
 res.setHeader('Content-Type',type+'; charset=utf-8');res.setHeader('Content-Security-Policy',"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'");res.end(content);
});
(async()=>{await new Promise(r=>server.listen(0,'127.0.0.1',r));let browser;const errors=[],external=[],checks=[],shots=[];
try{
 browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true});
 for(const width of [1280,390]){
  browserMode=false;agents=[];remote=[record(A),record(B)];published=false;requests=[];
  const context=await browser.newContext({viewport:{width,height:width===390?844:900}}),origin='http://127.0.0.1:'+server.address().port;
  await context.route('**/*',r=>{if(r.request().url().startsWith(origin+'/')||r.request().url().startsWith('data:'))return r.continue();external.push(r.request().url());return r.abort();});
  const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.type()+': '+m.text())});
  await page.goto(origin);assert.equal(await page.title(),'AgentNet named agent UI fixture');await page.locator('#nav-chats').waitFor();
  const capture=async name=>{const measurements=await page.evaluate(()=>({doc:document.documentElement.scrollWidth>innerWidth,dialogs:[...document.querySelectorAll('dialog[open]')].map(d=>({scroll:d.scrollWidth,client:d.clientWidth}))}));assert.equal(measurements.doc,false);assert(measurements.dialogs.every(d=>d.scroll<=d.client+1),name+': internal modal overflow');if(process.env.AGENTNET_SCREENSHOTS){const file=path.join(process.env.AGENTNET_SCREENSHOTS,`named-agent-${name}-${width}.png`);await page.screenshot({path:file});shots.push(file)}};
  await page.locator('#profile-btn').click();await page.getByRole('tab',{name:'Agent',exact:true}).click();await page.getByRole('button',{name:'Create agent…',exact:true}).click();
  await page.locator('#dialog').getByLabel('Agent name').fill('Local builder');await page.locator('#dialog').getByLabel('Local program').selectOption('codex');await page.locator('#named-dir').fill('/tmp/synthetic-named');assert(!/host-signed|catalog|publication/.test(await page.locator('#dialog').innerText()));await capture('create');await page.locator('#dialog-ok').click();
  const local=page.locator('#named-agents');await local.getByText(/Saved on this computer\. Agent list not updated for others/).waitFor();assert.equal(requests.filter(r=>r.path==='/api/agents'&&r.body?.action==='publish').length,0);await capture('saved-unpublished');
  await local.getByRole('button',{name:'Configure…',exact:true}).click();await page.locator('#named-dir').fill('/tmp/synthetic-updated');await page.locator('#dialog-ok').click();await local.getByText(/synthetic-updated/).waitFor();assert.equal(agents[0].responder.timeout,77);assert.deepEqual(agents[0].responder.context,['/tmp/synthetic-context']);
  await local.getByRole('button',{name:'Update agent list',exact:true}).click();await local.getByText(/Saved on this computer\. Agent list updated for others/).waitFor();await capture('published');
  await local.getByRole('button',{name:'Disable…',exact:true}).click();await page.locator('#dialog-ok').click();await local.getByText('Disabled on this computer. Earlier messages remain.',{exact:true}).waitFor();await capture('disabled');
  assert.equal(requests.filter(r=>r.path==='/api/responder'&&r.body).length,0);
  await page.locator('#settings-close').click();await page.evaluate(()=>openThread('a1'));await page.waitForFunction(()=>state.data?.id==='a1');
  await page.locator('input[name="kind"][value="question"]').check();await page.getByRole('button',{name:'Refresh agents',exact:true}).click();await page.locator('#device-agent-target option[value="'+A+'"]').waitFor({state:'attached'});
  await page.getByLabel('Question/task agent',{exact:true}).selectOption(A);await page.locator('#body').fill('Question for A');await page.getByLabel('Question/task agent',{exact:true}).selectOption(B);await page.getByLabel('Question/task agent',{exact:true}).selectOption(A);assert.equal(await page.locator('#to-name').innerText(),'Builder A on alice/desk');assert.equal(await page.locator('#device-agent-target option:checked').innerText(),'Builder A · alice/desk');assert((await page.locator('#device-agent-target option:checked').getAttribute('title')).includes(A));assert.equal(await page.locator('#m-answer .who').innerText(),'Builder B on alice/desk');assert((await page.locator('#m-answer .who').getAttribute('title')).includes(B));await capture('exact-A');
  await page.locator('#m-answer details.tech summary').click();assert((await page.locator('#m-answer details.tech').innerText()).includes(B));assert((await page.locator('#m-answer details.tech').innerText()).includes('host assertion'));await capture('author-details');await page.locator('#m-answer details.tech summary').click();
  await page.locator('#send').click();await page.waitForFunction(()=>!state.sending);assert(requests.some(r=>r.path==='/api/send'&&r.body?.agent_id===A));
  await page.locator('#body').fill('Keep exact missing target');remote=[record(B)];await page.getByRole('button',{name:'Refresh agents',exact:true}).click();await page.waitForFunction(()=>state.targetCatalog&&!state.targetCatalog.loading);assert.equal(await page.locator('#send').isDisabled(),true);assert.equal(await page.locator('#device-agent-target').inputValue(),A);assert.equal(await page.locator('#body').inputValue(),'Keep exact missing target');await capture('missing-ID');
  remote=[record(A),record(B)];await page.getByRole('button',{name:'Refresh agents',exact:true}).click();await page.waitForFunction(()=>!state.targetCatalog.loading);await page.getByLabel('Question/task agent',{exact:true}).selectOption(B);await page.locator('#send').click();await page.getByText('You started this text for someone else. Check the To line, then send again.',{exact:true}).waitFor();
  await page.evaluate(()=>fixture.select('B'));await page.waitForFunction(()=>state.overview&&window.agentnet.workspace.id==='B');assert.equal(await page.locator('#body').inputValue(),'');await page.evaluate(()=>fixture.select('A'));await page.waitForFunction(id=>state.data?.id==='a1'&&state.deviceAgentID===id,B);
  assert.equal(await page.locator('#body').inputValue(),'Keep exact missing target');
  await page.waitForFunction(()=>state.targetCatalog&&!state.targetCatalog.loading);await page.evaluate(()=>openDM('d1'));await page.waitForFunction(()=>state.dmData?.id==='d1');await page.getByLabel('DM recipient',{exact:true}).selectOption('pid-A');await page.getByLabel('DM recipient',{exact:true}).selectOption('pid-B');await page.locator('input[name="kind"][value="task"]').check();await page.locator('#body').fill('Task for accepted B');assert.equal(await page.locator('#m-dm2 .who').innerText(),'Builder A on alice/desk');assert((await page.locator('#to-name').innerText()).includes('Builder B'));await capture('DM-B');await page.locator('#send').click();await page.waitForFunction(()=>!state.sending);assert(requests.some(r=>r.path.endsWith('/ask')&&r.body?.pid==='pid-B'&&r.body.kind==='task'));
  await page.getByRole('button',{name:'Invite an agent…',exact:true}).click();await page.locator('input[name="agent-host"][value="alice/desk"]').check();await page.locator('#invite-agent option[value="'+B+'"]').waitFor({state:'attached'});await page.getByLabel('Agent on selected host').selectOption(B);await capture('invite-B');await page.locator('#dialog-ok').click();await page.locator('#dialog').waitFor({state:'hidden'});assert(requests.some(r=>r.path.endsWith('/invite')&&r.body?.agent_id===B&&r.body.host==='alice/desk'));
  checks.push({width,configCreateUpdateDisablePublish:true,preservedDefaultTimeoutContext:true,exactSelectionDraftAndWorkspace:true,removedIDRefuses:true,namedInvite:true,acceptedDMTask:true});
  await context.close();browserMode=true;requests=[];
  const browserContext=await browser.newContext({viewport:{width,height:width===390?844:900}});await browserContext.route('**/*',r=>{if(r.request().url().startsWith(origin+'/')||r.request().url().startsWith('data:'))return r.continue();external.push(r.request().url());return r.abort()});
  const browserPage=await browserContext.newPage();browserPage.on('pageerror',e=>errors.push(e.message));browserPage.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.type()+': '+m.text())});await browserPage.goto(origin);await browserPage.locator('#profile-btn').click();await browserPage.getByRole('tab',{name:'Agent',exact:true}).click();await browserPage.locator('#responder').getByText(/This browser runs nothing/).waitFor();assert.equal(await browserPage.getByRole('button',{name:'Create agent…'}).count(),0);assert(!requests.some(r=>r.path==='/api/agents'||r.path==='/api/responder'));
  if(process.env.AGENTNET_SCREENSHOTS){const file=path.join(process.env.AGENTNET_SCREENSHOTS,`named-agent-browser-no-local-${width}.png`);await browserPage.screenshot({path:file});shots.push(file)}await browserContext.close();
 }
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);console.log(JSON.stringify({pass:true,checks,shots,errors,externalRequests:external}));
}catch(e){const page=browser?.contexts().at(-1)?.pages().at(-1);console.error(JSON.stringify({errors,requests:requests.map(r=>({path:r.path,workspace:r.workspace})),text:page?await page.locator('body').innerText():''}));if(page&&process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'named-agent-failure.png')});throw e;}finally{if(browser)await browser.close();await new Promise(r=>server.close(r))}})().catch(e=>{console.error(e);process.exitCode=1});
