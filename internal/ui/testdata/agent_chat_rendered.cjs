// Inert public-host fixture; production skins, no Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=process.env.AGENTNET_STATIC_ROOT||path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const at='2026-10-06T10:00:00Z',local='sergey/laptop',remote='sergey/zenbook',localID='a'.repeat(32),remoteID='b'.repeat(32),defaultID='d'.repeat(32),disabledID='9'.repeat(32),legacyHistory='e'.repeat(32),retiredHistory='f'.repeat(32),panelChat='7'.repeat(32),panelMode=!!process.env.AGENTNET_PANEL_LAYOUT;
const me={person:'sergey',label:'Sergey',address:local,state:'self',devices:[{address:local,fingerprint:'local-key'},{address:remote,fingerprint:'remote-key'},{address:'sergey/iphone',fingerprint:'phone-key'}]};
const overview={version:'fixture',seq:1,me:{address:local,fingerprint:'local-key',responder:'codex',responder_dir:'/fixture',agent:true},person:me,persons:true,agents:true,role:'person',people:[],review:[],links:[],reminders:[],threads:[],dms:[],agent_devices:[remote],directory:{current:true,members:[]},quarantine:[],files:{max_count:8,max_file:100000,max_message:200000}};
if(process.env.AGENTNET_REMOVED_PERMISSIONS){
 me.devices.push({address:'sergey/android-phone-3',fingerprint:'replacement-key'});
 overview.people.push({person:'bob',label:'Bob',address:'bob/laptop',state:'pinned',devices:[{address:'bob/laptop',fingerprint:'bob-key'}]});
 overview.directory.members.push({address:'bob/laptop'},{address:'sergey/android-phone-2'},{address:'service/bot'});
 overview.agent_devices.push('service/bot');
 overview.threads.push(...['sergey/android-phone-2','bob/laptop','service/bot'].map((peer,i)=>({id:String(i+1).repeat(32),peer,title:'Retained history',last:'Historical message',last_at:at,count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:at})));
}
if(process.env.AGENTNET_MODEL_REPORT){overview.model_reports=[{host:local,host_key:"local-key",agent_id:defaultID,model:"gpt-6.1-sol",reasoning:"xhigh",harness:"codex",executor:"a".repeat(64),at:1791280800,revision:1},{host:remote,host_key:"remote-key",agent_id:remoteID,model:"Claude Opus 4.6",harness:"claude",executor:"b".repeat(64),at:1791280800,revision:1}];}
const responder={agent_id:defaultID,chosen:true,manual:false,harness:'codex',dir:'/fixture',ready:true,harnesses:[{name:'codex',found:true},{name:'pi',found:true},{name:'omp',found:true}]};
const record=(host,id,label,harness=host===local?'codex':'claude')=>({v:1,id,host,host_key:host===local?'local-key':'remote-key',label,ts:1,harness});
if(panelMode){overview.topic_list=true;overview.threads.push({id:panelChat,peer:local,agent_host:local,agent_key:'local-key',agent_id:localID,title:'Existing panel conversation',last:'Original panel message',last_at:at,count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:at});}
const boot=`
const seed=${JSON.stringify({overview,responder,local,remote,localID,remoteID,disabledID,legacyHistory,retiredHistory,panelChat,panelMode,assignMode:!!process.env.AGENTNET_ASSIGN_MESSAGE,removedMode:!!process.env.AGENTNET_REMOVED_PERMISSIONS})};window.fixture={...seed,requests:[],threads:{},staged:[],uploads:{},storedBytes:{},discarded:[],fileReads:0,disabled:false,badRemote:false};
const historyFile=new File(['Original retained draft bytes'],'history-draft.txt',{type:'text/plain'});
const panelFile=new File(['Exact original panel draft bytes'],'panel-original.txt',{type:'text/plain'});
const workspace={id:'default',name:'Agent starter fixture',endpoint:location.origin,address:seed.local,realm:'',handle:'agent-choice-workspace',state:'enrolled'};
const bag={comicDrafts:{handle:workspace.handle,drafts:{[seed.legacyHistory]:{text:'Keep this original history draft',files:[{key:'history-file',name:historyFile.name,size:historyFile.size,type:historyFile.type,file:historyFile}]}},open:null,dm:null,thread:null,tab:'chats'}};
fixture.bag=bag;fixture.historyFile=historyFile;
if(seed.panelMode){const summary=fixture.overview.threads.find(t=>t.id===seed.panelChat);fixture.threads[seed.panelChat]={id:seed.panelChat,peer:seed.local,key:{pinned:'local-key'},approved:true,task_grant:'',topic:summary,messages:[{id:seed.panelChat,dir:'out',from:seed.local,to:seed.local,kind:'question',body:'Original panel message',at:'${at}',state:'queued',author:{label:'You'},target:{address:seed.local,fingerprint:'local-key',agent_id:seed.localID}}]};bag.comicDrafts.drafts[seed.panelChat]={text:'Keep the exact panel draft',files:[{key:'panel-file',name:panelFile.name,size:panelFile.size,type:panelFile.type,file:panelFile}]};fixture.panelFile=panelFile;}
let opened,changed;
const host={version:1,platform:'daemon',workspace,workspaces:{list:()=>[workspace],active:()=>workspace.id,has:id=>id===workspace.id,select(){throw Error('Unexpected workspace switch');},onChange(){return()=>{};},state:id=>{if(id!==workspace.id)throw Error('Wrong workspace state');return bag;}},skins:[],onSkinsChange(){return()=>{};},onOpen(fn){opened=fn;},listen(fn){changed=fn;return()=>{};},stage:async file=>{if(fixture.failStageAfterFirst&&Object.keys(fixture.uploads).length){fixture.failStageAfterFirst=false;throw Error('Temporary upload failure');}const value={name:file.name,text:await file.text()};fixture.staged.push(value);const id='upload-'+fixture.staged.length;fixture.uploads[id]=value;return id;},file:async(id,index,dir)=>{fixture.fileReads++;const kept=fixture.storedBytes[id+':'+index];return {bytes:new TextEncoder().encode(kept===undefined?'Reviewed source file':fixture.corruptStoredBytes?'Different saved file':kept)};},api:async(p,body)=>{
fixture.requests.push({path:p,body});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/responder')return structuredClone(fixture.responder);
if(p.startsWith('/api/agents')){const h=new URL(p,location.origin).searchParams.get('host'),remote=!!h;return {host:remote?(fixture.badRemote?'wrong/computer':h):seed.local,local:!remote,harnesses:seed.responder.harnesses,agents:remote?[{record:${JSON.stringify(record(remote,remoteID,'Zenbook agent'))},enabled:true}]:[{record:{...${JSON.stringify(record(local,defaultID,'Codex'))},label:fixture.defaultLabel||'Codex'},default:true,enabled:true,responder:fixture.responder},{record:${JSON.stringify(record(local,localID,'Pi','pi'))},enabled:!fixture.disabled,responder:{...fixture.responder,agent_id:seed.localID,harness:'pi'}},{record:${JSON.stringify(record(local,'c'.repeat(32),'Sleeping agent','omp'))},enabled:true,responder:{...fixture.responder,harness:'omp',ready:false,problem:'Not ready in fixture'}},{record:${JSON.stringify(record(local,disabledID,'Codex'))},enabled:false,responder:fixture.responder}]};}
if(seed.panelMode&&p==='/api/assistant-setup'){if(body!==undefined)throw Error('Panel navigation must not apply setup');return {local:true,harnesses:[['codex','Codex'],['pi','Pi'],['omp','OMP']].map(([id,label])=>({id,label,detected:true,configured:true,registered:true,supported:true,state:'connected',note:'Inert layout fixture native setup.'}))};}
if(p==='/api/upload/discard'){for(const id of body.ids){fixture.discarded.push(id);delete fixture.uploads[id];}return {};}
if(p==='/api/send'){const staged=(body.files||[]).map(id=>fixture.uploads[id]);for(const id of body.files||[])delete fixture.uploads[id];if(fixture.failNextSend){fixture.failNextSend=false;throw Error('Temporary send failure');}const id=body.kind==='task'?body.id:(Object.keys(fixture.threads).length+1).toString(16).padStart(32,'0');if(fixture.threads[id])throw Error('This send is already kept here.');for(const [index,file] of staged.entries())fixture.storedBytes[id+':'+index]=file.text;const message={id,dir:'out',from:seed.local,to:body.to,kind:body.kind,body:body.body,at:'${at}',state:'queued',author:{label:'You',about:''},files:seed.assignMode&&body.body==='Default question'?[{name:'source.txt',size:20,openable:true},{name:'second.txt',size:20,openable:true},{name:'missing.txt',size:40,openable:false}]:staged.length?staged.map((f,index)=>({index,name:f.name,size:f.text.length,openable:true})):undefined,target:body.agent_id?{address:body.to,fingerprint:body.to===seed.local?'local-key':'remote-key',agent_id:body.agent_id}:undefined};const summary={id,peer:body.to,title:body.body,last:body.body,last_at:'${at}',count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:'${at}',agent_id:body.agent_id,agent_host:body.to,agent_key:body.to===seed.local?'local-key':'remote-key'};fixture.threads[id]={id,peer:body.to,key:{pinned:'fixture-key'},approved:true,task_grant:'',messages:[message],topic:summary};fixture.overview.threads.push(summary);fixture.overview.seq++;if(fixture.loseNextSendSuccess){fixture.loseNextSendSuccess=false;fixture.lostTask=id;throw Error('Response lost after durable storage');}return {id,state:'queued',path:'fixture'};}
if(p.startsWith('/api/thread?'))return structuredClone(fixture.threads[new URL(p,location.origin).searchParams.get('id')]);
if(p.startsWith('/api/topics?')){const q=new URL(p,location.origin).searchParams.get('q')||'',topics=fixture.overview.threads.filter(t=>(t.title+' '+t.last).toLowerCase().includes(q.toLowerCase()));return {topics,matched:topics.length,total:topics.length};}
if(p==='/api/groups/invitations')return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};if(p==='/api/approvals')return {questions:[],tasks:[],removed_devices:seed.removedMode?['sergey/android-phone-2']:[],own_devices:fixture.overview.person.devices.map(d=>({address:d.address,questions:'own',tasks:'own',this:d.address===seed.local}))};return {};
}};
fixture.change=()=>changed?.({type:'change',seq:++fixture.overview.seq});fixture.openThread=id=>opened(id,'thread');
const base='/assets/skins/comic/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);fixture.unmount=()=>module.unmount(skinRoot);window.ready=true;
`;
async function checkPanelLayout(page,width,nav,shots){
 const fit=async()=>{
  assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'panel document fits viewport');
  assert(!await page.locator('.skin-root').evaluate(el=>el.scrollWidth>el.clientWidth+1),'panel skin fits viewport');
 };
 const capture=async name=>{await settle(page);await fit();const file=path.join(evidence,'comic-panel-'+name+'-'+width+'.png');await page.screenshot({path:file});shots.push(file);};
 const workspace=async tab=>{
  await settle(page);await fit();
  if(width>=1024){
   const pane=page.locator('[data-workspace="'+tab+'"]');await pane.waitFor();
   const bounds=await pane.boundingBox();assert(bounds&&bounds.width>=700,tab+' has a readable desktop workspace '+JSON.stringify(bounds));
   assert.equal(await page.locator('aside').count(),0,tab+' has no narrow list column');
   assert.equal(await page.locator('[data-part="conversation"]').count(),0,tab+' hides the unrelated open conversation');
  }
 };
 const backToLists=async()=>{if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}};
 const composer=()=>page.locator('[data-part="composer"][data-conv="'+panelChat+'"]');
 const originalDraft=async()=>{
  await composer().waitFor();
  assert.equal(await composer().getByRole('textbox').inputValue(),'Keep the exact panel draft');
  await composer().getByRole('list',{name:'Files to send'}).getByText('panel-original.txt',{exact:true}).waitFor();
  await settle(page);await fit();
 };
 await workspace('agents');
 // Change only this synthetic catalog's label, then restore it before the
 // ordinary exact-agent starter cases can run in another mode/context.
 const longName='A'.repeat(64),displayName=longName+' · Codex';
 try{
  await page.evaluate(label=>{fixture.defaultLabel=label;fixture.change();},longName);
  const name=page.locator('p[title="'+displayName+'"]');await name.waitFor();
  const card=page.getByRole('region',{name:'Your agents',exact:true}).locator('article').filter({has:name});
  const badge=card.getByText('Default',{exact:true}),action=card.getByRole('button',{name:'Chat with '+displayName,exact:true});
  assert(await badge.isVisible(),'long default-agent name keeps its Default badge');
  assert(await name.evaluate(el=>getComputedStyle(el).textOverflow==='ellipsis'&&el.scrollWidth>el.clientWidth),'long concrete name truncates');
  const box=await card.boundingBox();assert(box&&box.x>=0&&box.x+box.width<=width+1,'long-name card fits');
  for(const control of [badge,action]){
   const bounds=await control.boundingBox();assert(bounds&&bounds.x>=box.x&&bounds.x+bounds.width<=box.x+box.width+1,'long-name badge/action stays inside its card');
   assert(await control.evaluate(el=>el.scrollWidth<=el.clientWidth+1),'long-name badge/action content fits');
  }
  await capture('long-default');
 }finally{await page.evaluate(()=>{delete fixture.defaultLabel;fixture.change();});}
 await page.getByRole('button',{name:'Chat with Codex',exact:true}).waitFor();
 await page.getByRole('button',{name:'Connect an agent',exact:true}).click();
 await page.getByRole('heading',{name:'Choose your agents',exact:true}).waitFor();
 const setup=page.getByRole('region',{name:'Set up your agents',exact:true});
 await setup.getByRole('checkbox',{name:'Set up Codex',exact:true}).waitFor();
 if(width>=1024)assert((await setup.boundingBox()).width>=600,'setup uses the desktop workspace width');
 await capture('setup');
 await setup.getByRole('button',{name:'Back',exact:true}).click();
 await page.getByRole('button',{name:'Connect an agent',exact:true}).waitFor();
 assert.equal(await page.getByRole('heading',{name:'Choose your agents',exact:true}).count(),0,'setup Back returns to the agent list');

 await page.evaluate(()=>fixture.openThread(fixture.panelChat));await originalDraft();
 await capture('original-draft');
 await backToLists();await nav.getByRole('button',{name:'Agents',exact:true}).click();await workspace('agents');
 await page.evaluate(()=>{fixture.overview.review=[{id:fixture.panelChat,peer:fixture.local,agent_id:fixture.localID,kind:'question',reason:'held',excerpt:'Original panel message',why:'Synthetic decision for the exact retained conversation',at:'2026-10-06T10:00:00Z',notice:false}];fixture.change();});
 await nav.getByRole('button',{name:/^Review/}).click();
 await page.getByRole('heading',{name:'Review',exact:true}).waitFor();await workspace('oks');
 const waiting=page.getByRole('list',{name:'Waiting for you',exact:true});await waiting.waitFor();
 await capture('review');
 await waiting.getByRole('button',{name:/\. Review it\.$/}).click();await originalDraft();

 await backToLists();await nav.getByRole('button',{name:width>=1024?'Settings':'You',exact:true}).click();
 const devices=page.getByRole('button',{name:/^Your devices/});await devices.click();
 await page.getByRole('heading',{name:'Your devices',exact:true}).waitFor();
 if(width>=1024){assert.equal(await page.locator('aside').count(),1,'Settings retains its compact index');assert.equal(await devices.getAttribute('aria-current'),'page');}
 await capture('settings-details');
 if(width<1024){
  await page.locator('[data-settings-back]').click();await devices.waitFor();
  assert(await devices.evaluate(el=>el.getRootNode().activeElement===el),'phone Settings Back restores the selected row focus');
 }
 await nav.getByRole('button',{name:'Chats',exact:true}).click();
 const chats=page.getByRole('region',{name:'Chats',exact:true});await chats.waitFor();
 if(width<1024)await chats.getByRole('button',{name:'Search chats',exact:true}).click();
 else await page.keyboard.press('Control+k');
 const search=chats.getByRole('searchbox',{name:/^Search chats/});await search.waitFor();
 assert(await search.evaluate(el=>el.getRootNode().activeElement===el),'chat search receives keyboard focus');
 await search.fill('Z'.repeat(80));await chats.getByText(/Nothing matches/).waitFor();
 await capture('search-empty');await search.press('Escape');assert.equal(await search.inputValue(),'');
 if(width<1024)await search.press('Escape');
 const add=chats.getByRole('button',{name:'New',exact:true});await add.click();
 const newChat=page.getByRole('dialog',{name:'New chat',exact:true});await newChat.waitFor();
 await newChat.getByRole('button',{name:'Chat with Pi',exact:true}).waitFor();
 await capture('new-chat');await page.keyboard.press('Escape');await newChat.waitFor({state:'hidden'});
 await settle(page);
 assert(await add.evaluate(el=>el.getRootNode().activeElement===el),'closing New returns keyboard focus');
 await page.evaluate(()=>{fixture.savedPanelThreads=fixture.overview.threads;fixture.overview.threads=[];fixture.overview.review=[];fixture.change();});
 try{await chats.getByRole('heading',{name:'No chats yet',exact:true}).waitFor();await capture('chats-empty');}
 finally{await page.evaluate(()=>{fixture.overview.threads=fixture.savedPanelThreads;delete fixture.savedPanelThreads;fixture.change();});}
 await page.evaluate(()=>fixture.openThread(fixture.panelChat));await originalDraft();await capture('restored-draft');
 assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined&&r.path!=='/api/refresh'&&!(r.path==='/api/act'&&r.body.do==='read')).length),0,'browsing panels sends no work, setup changes, permission grants or file stages');
}
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
   if(process.env.AGENTNET_REMOVED_PERMISSIONS){
    for(let attempt=0;attempt<2;attempt++){
     if(attempt){await page.reload();await page.waitForFunction(()=>window.ready);await page.getByRole('navigation',{name:'Main'}).getByRole('button',{name:'Agents',exact:true}).click();}
     const devices=page.getByRole('list',{name:'Your devices and current permissions'});
     await devices.getByText(/Android phone 3/i).waitFor();
     await page.getByRole('button',{name:/Turn on automatic answers for .*Bob/i}).waitFor();
     await page.getByRole('button',{name:/Turn on automatic answers for .*Bot/i}).waitFor();
     assert.equal(await page.getByRole('button',{name:/Turn on.*phone.?2/i}).count(),0,'removed device cannot return as an actionable historical fallback');
     assert(!/phone.?2/i.test(await devices.innerText()),'removed device absent from current roster controls');
     assert.equal(await page.evaluate(()=>fixture.overview.threads.filter(t=>t.peer==='sergey/android-phone-2').length),1,'historical thread retained');
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined).length),0,'reading permissions makes no grants or sends');
    }
    await settle(page);const shot=path.join(evidence,'comic-removed-permissions-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'permissions fit viewport');assert.deepEqual(errors,[]);await context.close();continue;
   }
   await page.getByRole('button',{name:'Chat with Pi',exact:true}).waitFor();
   assert.equal(await page.getByRole('button',{name:'Chat with Sleeping agent · OMP',exact:true}).count(),0,'unready agents are not chat choices');
   assert.equal(await page.getByRole('button',{name:'New agent',exact:true}).count(),0,'all installed programs already have enabled instances');
   assert.equal(await page.getByRole('button',{name:'Chat with Codex',exact:true}).count(),1,'the disabled earlier Codex cannot create a duplicate choice');
   assert(await page.getByRole('button',{name:'Chat with Zenbook agent · Claude',exact:true}).isVisible(),'own remote agent discoverable without threads');
   const yours=page.getByRole('region',{name:'Your agents',exact:true});
   await yours.getByText('Default for requests without an agent',{exact:true}).waitFor({timeout:2000});
   await yours.getByRole('button',{name:'Change default setting',exact:true}).waitFor();
   const localCard=yours.locator('article').filter({has:page.getByRole('button',{name:'Chat with Pi',exact:true})});
   await localCard.getByText(/^Pi · Ready · Laptop$/).waitFor();
   const localLast=await page.getByRole('button',{name:'Chat with Pi',exact:true}).boundingBox(),remoteFirst=await page.getByRole('button',{name:'Chat with Zenbook agent · Claude',exact:true}).boundingBox();
   assert(localLast && remoteFirst && localLast.y<remoteFirst.y,'local managed agents stay together before other-device agents');
   assert.equal(await page.getByRole('button',{name:'Chat with Your agent',exact:true}).count(),0,'no generic default chat is fabricated');assert.deepEqual(await sent(),[],'discovery must send nothing');
   if(process.env.AGENTNET_MODEL_REPORT){
    await settle(page);
    await yours.getByText(/^Last reported model: gpt-6.1-sol/).waitFor();await yours.getByText(/^Last reported model: Claude Opus 4.6/).waitFor();
    assert.equal(await localCard.getByText(/Model and reasoning not reported/).count(),1,'named identity never borrows default model');
    const modelRows=yours.locator('[data-agent-model]');assert.equal(await modelRows.count(),3,'metadata belongs only to the three currently usable identities');
    assert.equal(await modelRows.locator('time').count(),2,'reported values retain timestamps');
    for(const row of await modelRows.all()){const b=await row.boundingBox();if(!b||b.x<0||b.x+b.width>width+1)await page.screenshot({path:path.join(evidence,'model-overflow-'+width+'.png')});assert(b&&b.x>=0&&b.x+b.width<=width+1,'metadata fits desktop/mobile card '+JSON.stringify({b,width,text:await row.innerText()}));}
    assert.equal(await yours.getByRole('combobox',{name:/model/i}).count(),0,'read-only model visibility');
   }

   await settle(page);const agentsShot=path.join(evidence,'comic-agent-list-'+width+'.png');await page.screenshot({path:agentsShot});shots.push(agentsShot);
   if(panelMode){
    await checkPanelLayout(page,width,nav,shots);
    assert.deepEqual(errors,[]);await page.evaluate(()=>fixture.unmount());
    assert.deepEqual(await page.evaluate(async()=>{const saved=fixture.bag.comicDrafts,d=saved.drafts[fixture.panelChat];return {open:saved.open?.id,agent:fixture.threads[fixture.panelChat].topic.agent_id,text:d.text,sameFile:d.files[0].file===fixture.panelFile,bytes:await d.files[0].file.text(),sends:fixture.requests.filter(r=>r.path==='/api/send').length,stages:fixture.staged.length};}),{open:panelChat,agent:localID,text:'Keep the exact panel draft',sameFile:true,bytes:'Exact original panel draft bytes',sends:0,stages:0},'panel navigation retains the exact conversation, identity, draft and original File');
    await context.close();continue;
   }
   await page.getByRole('button',{name:'Chat with Pi',exact:true}).click();
   let sheet=page.getByRole('dialog',{name:'Chat with Pi',exact:true});
   await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Keep my named question');
   await page.evaluate(()=>fixture.disabled=true);await sheet.getByRole('button',{name:'Send question',exact:true}).click();
   await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/selected agent is unavailable/);
   assert.equal(await sheet.getByRole('textbox',{name:'Your message',exact:true}).inputValue(),'Keep my named question');assert.deepEqual(await sent(),[],'disabled selected agent must not become default');
   await page.evaluate(()=>fixture.disabled=false);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});
   await page.locator('[data-mid="00000000000000000000000000000001"]').waitFor();
   assert.match(await page.locator('[data-mid="00000000000000000000000000000001"]').innerText(),/Keep my named question/);
   const first=(await sent())[0];assert.equal(first.to,local);assert.equal(first.agent_id,localID);assert.equal(first.kind,'question');
   if(process.env.AGENTNET_MODEL_REPORT){const runtime=page.locator('[data-part=agent-runtime]');await runtime.getByText(/Model and reasoning not reported/).waitFor();assert(!/gpt-6/.test(await runtime.innerText()),'active named agent never borrows default metadata');}
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:/^Chats/}).click();await page.getByRole('button',{name:'New',exact:true}).click();
   const newChat=page.getByRole('dialog',{name:'New chat',exact:true});await newChat.getByRole('button',{name:'Chat with Zenbook agent · Claude',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Zenbook agent · Claude',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Question for Zenbook');
   await page.evaluate(()=>fixture.badRemote=true);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/does not match/);assert.equal((await sent()).length,1);
   await page.evaluate(()=>fixture.badRemote=false);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});await newChat.waitFor({state:'hidden'});
   const second=(await sent())[1];assert.equal(second.to,remote);assert.equal(second.agent_id,remoteID);
   if(process.env.AGENTNET_MODEL_REPORT){const runtime=page.locator('[data-part=agent-runtime]');await runtime.getByText(/Last reported model: Claude Opus 4.6/).waitFor();assert.match(await runtime.innerText(),/Reasoning: not reported/,'absent effort stays explicit in active remote agent chat');}
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:'Agents',exact:true}).click();await page.getByRole('button',{name:'Chat with Codex',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Codex',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Default question');await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});
   const third=(await sent())[2];assert.equal(third.to,local);assert.equal(third.agent_id,defaultID);
   if(process.env.AGENTNET_MODEL_REPORT){
    const runtime=page.locator('[data-part=agent-runtime]');await runtime.getByText(/Reasoning: xhigh/).waitFor();
    assert.match(await runtime.innerText(),/Settings may have changed/,'historical metadata never becomes a current-setting assertion');
    await settle(page);const activeShot=path.join(evidence,'comic-active-agent-model-'+width+'.png');await page.screenshot({path:activeShot});shots.push(activeShot);
    await page.evaluate(()=>{fixture.overview.model_reports[0].reasoning='low';fixture.change();});await runtime.getByText(/Reasoning: low/).waitFor();
    await page.evaluate(()=>{fixture.overview.model_reports[0].host_key='changed-key';fixture.change();});await runtime.getByText(/Model and reasoning not reported/).waitFor();
    assert(!/gpt-6/.test(await runtime.innerText()),'changed host key cannot retain metadata');
    await page.evaluate(()=>{fixture.overview.model_reports[0].host_key='local-key';fixture.overview.model_reports[0].reasoning='xhigh';fixture.change();});await runtime.getByText(/Reasoning: xhigh/).waitFor();
    await page.evaluate(()=>{fixture.ownDevices=fixture.overview.person.devices;fixture.overview.person.devices=fixture.ownDevices.filter(d=>d.address!==fixture.local);fixture.change();});await runtime.waitFor({state:'hidden'});
    await page.evaluate(()=>{fixture.overview.person.devices=fixture.ownDevices;fixture.change();});await runtime.getByText(/Reasoning: xhigh/).waitFor();

   }

   if(process.env.AGENTNET_ASSIGN_MESSAGE){
    const source=page.locator('[data-mid="00000000000000000000000000000003"]');
    if(width>=1024){await source.locator('[data-message-bubble]').hover();await source.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Assign to your agent…',exact:true}).click();}
    else{await source.getByRole('button',{name:'Message actions',exact:true}).focus();await source.getByRole('button',{name:'Message actions',exact:true}).click();await page.getByRole('button',{name:'Assign to your agent…',exact:true}).click();}
    const assign=page.getByRole('dialog',{name:'Assign to your agent',exact:true});await assign.getByRole('combobox',{name:'Your agent',exact:true}).selectOption(remote+'#'+remoteID);
    await assign.getByRole('textbox',{name:'What should it do?',exact:true}).fill('Review the selected request');
    assert.equal((await sent()).length,3,'opening a reviewed assignment sends no task');assert(await assign.getByRole('checkbox',{name:'source.txt',exact:true}).isChecked());assert(await assign.getByRole('checkbox',{name:/missing.txt/}).isDisabled());
    await settle(page);const assignmentShot=path.join(evidence,'comic-assign-preview-'+width+'.png');await page.screenshot({path:assignmentShot});shots.push(assignmentShot);
    assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'assignment preview overflow');
    await page.evaluate(()=>fixture.threads['00000000000000000000000000000003'].messages[0].body='Changed after selection');
    await assign.getByRole('button',{name:'Send task',exact:true}).click();await assign.getByRole('alert').waitFor();assert.match(await assign.getByRole('alert').innerText(),/message changed|no longer available/i);assert.equal((await sent()).length,3,'stale source is not silently delegated');
    await page.evaluate(()=>{fixture.threads['00000000000000000000000000000003'].messages[0].body='Default question';fixture.failStageAfterFirst=true;});
    await assign.getByRole('button',{name:'Send task',exact:true}).click();await assign.getByRole('button',{name:'Retry this task',exact:true}).waitFor();
    assert.match(await assign.getByRole('alert').innerText(),/upload failure/i);assert.equal((await sent()).length,3,'failed second upload sends no task');
    assert.deepEqual(await page.evaluate(()=>Object.keys(fixture.uploads)),[],'failed upload attempt discards earlier staged bytes');
    assert.equal(await page.evaluate(()=>fixture.discarded.length),1,'exact unused stage is discarded');
    await page.evaluate(()=>fixture.failNextSend=true);await assign.getByRole('button',{name:'Retry this task',exact:true}).click();await assign.getByRole('alert').waitFor();
    assert.match(await assign.getByRole('alert').innerText(),/send failure/i);assert.equal((await sent()).length,4,'pre-storage failed send remains one explicit attempt');
    assert(await assign.getByRole('combobox',{name:'Your agent',exact:true}).isDisabled(),'uncertain send keeps the exact target');
    assert(await assign.getByRole('textbox',{name:'What should it do?',exact:true}).isDisabled(),'uncertain send keeps reviewed bytes');
    await page.evaluate(()=>fixture.loseNextSendSuccess=true);await assign.getByRole('button',{name:'Retry this task',exact:true}).click();await assign.getByRole('alert').waitFor();
    assert.match(await assign.getByRole('alert').innerText(),/response lost/i);assert.equal((await sent()).length,5);
    const keptID=await page.evaluate(()=>fixture.lostTask),beforeStages=await page.evaluate(()=>fixture.staged.length);
    assert.equal(await page.evaluate(()=>Object.values(fixture.threads).filter(t=>t.messages[0]?.kind==='task').length),1,'lost success really persisted one task');
    await page.evaluate(()=>fixture.corruptStoredBytes=true);await assign.getByRole('button',{name:'Retry this task',exact:true}).click();await assign.getByRole('alert').waitFor();
    assert.match(await assign.getByRole('alert').innerText(),/file bytes differ/i);assert.equal((await sent()).length,5,'same metadata with differing saved bytes never resends');
    assert.equal(await page.evaluate(()=>fixture.staged.length),beforeStages,'recovery does not upload replacement bytes');
    await page.evaluate(()=>fixture.corruptStoredBytes=false);await assign.getByRole('button',{name:'Retry this task',exact:true}).click();await assign.waitFor({state:'hidden'});
    await page.locator('[data-mid="'+keptID+'"]').waitFor();
    const attempts=(await sent()).slice(3);assert.equal(attempts.length,2);assert.deepEqual({...attempts[0],files:[]},{...attempts[1],files:[]},'retry retains exact request ID, recipient and body');
    assert.equal(attempts[0].files.length,2);assert.equal(attempts[1].files.length,2);assert.notEqual(attempts[0].files[0],attempts[1].files[0],'consumed upload is restaged');
    assert.equal(await page.evaluate(()=>fixture.staged.length),5,'only failed uploads and two actual send attempts stage files');
    assert.deepEqual(await page.evaluate(()=>Object.keys(fixture.uploads)),[],'successful and failed send stages are consumed or discarded');
    assert.equal(await page.evaluate(()=>fixture.fileReads),5,'source bytes retained once; saved file mismatch and final exact files are read for recovery');
    assert.equal(attempts[1].id,keptID);assert.equal(attempts[1].to,remote);assert.equal(attempts[1].agent_id,remoteID);assert.equal(attempts[1].kind,'task');assert.match(attempts[1].body,/Review the selected request/);assert.match(attempts[1].body,/> Default question/);assert.match(attempts[1].body,/Selected message from You/);
    const sourceLink=page.getByRole('button',{name:'View original',exact:true});await sourceLink.waitFor();await sourceLink.click();await source.waitFor();assert.equal((await sent()).length,5,'recovery and source navigation never execute again');
   }
   const beforeHistory={sends:(await sent()).length,stages:await page.evaluate(()=>fixture.staged.length)};
   await page.evaluate(()=>{
    for(const [id,agent_id,title] of [[fixture.legacyHistory,'','Retained unbound history'],[fixture.retiredHistory,fixture.disabledID,'Retained retired Codex history']]){
     const summary={id,peer:fixture.local,agent_host:fixture.local,agent_key:'local-key',agent_id,agent_legacy:!agent_id,title,last:'Original retained question',last_at:'2026-10-06T10:00:00Z',count:1,review:0,unread:0,running:0,state:'answered',quiet_since:'2026-10-06T10:00:00Z'};
     const message={id,dir:'out',from:fixture.local,to:fixture.local,kind:'question',body:title,at:'2026-10-06T10:00:00Z',state:'answered',target:agent_id?{address:fixture.local,fingerprint:'local-key',agent_id}:undefined};
     fixture.threads[id]={id,peer:fixture.local,key:{pinned:'local-key'},approved:true,messages:[message],topic:summary};fixture.overview.threads.push(summary);
    }
    fixture.overview.topic_list=true;fixture.change();
   });
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:/^Chats/}).click();
   const chats=page.getByRole('region',{name:'Chats',exact:true});
   await chats.getByText('Saved agent history is available in Search chats.',{exact:true}).waitFor();
   assert.equal(await chats.locator('[data-part="chat-row"][data-kind="agent"]').count(),3,'legacy and retired exchanges never return as active agent cards');
   assert.equal(await chats.getByText('Retained unbound history',{exact:true}).count(),0);
   assert.equal(await chats.getByText('Retained retired Codex history',{exact:true}).count(),0);
   if(width<1024)await chats.getByRole('button',{name:'Search chats',exact:true}).click();
   const historySearch=chats.getByRole('searchbox',{name:/^Search chats/});
   await historySearch.fill('Retained');assert.equal(await historySearch.inputValue(),'Retained');
   const results=chats.getByRole('list',{name:'Matching topics',exact:true});await results.getByRole('button',{name:/^Retained unbound history,/}).waitFor();
   assert.equal(await results.getByRole('button').count(),2,'both old histories remain available through actual topic search');
   await results.getByRole('button',{name:/^Retained unbound history,/}).click();
   const recovery=page.locator('[data-part="conversation"]').getByRole('region',{name:'Agent history',exact:true});await recovery.waitFor();
   assert.equal(await recovery.getByRole('button',{name:'Chat with Pi',exact:true}).count(),1);
   assert.equal(await recovery.getByRole('button',{name:'Chat with Codex',exact:true}).count(),1);
   assert.equal(await recovery.getByRole('button').count(),2,'only exact current programs on the original host are explicit new-chat choices');
   await recovery.getByRole('button',{name:'Chat with Pi',exact:true}).click();
   const fresh=page.getByRole('dialog',{name:'Chat with Pi',exact:true});await fresh.waitFor();
   assert.equal(await fresh.getByRole('textbox',{name:'Your message',exact:true}).inputValue(),'','history draft is not transferred to another native session');
   await page.keyboard.press('Escape');await fresh.waitFor({state:'hidden'});
   await page.evaluate(()=>fixture.openThread(fixture.retiredHistory));await recovery.waitFor();
   assert.equal(await page.evaluate(()=>fixture.threads[fixture.retiredHistory].topic.agent_id),disabledID,'retired original binding remains exact');
   assert.equal((await sent()).length,beforeHistory.sends,'search and explicit recovery send no work');
   assert.equal(await page.evaluate(()=>fixture.staged.length),beforeHistory.stages,'history recovery stages no replacement files');
   assert.equal(await page.evaluate(()=>fixture.overview.threads.filter(t=>[fixture.legacyHistory,fixture.retiredHistory].includes(t.id)).length),2,'original histories are retained');
   await settle(page);const historyShot=path.join(evidence,'comic-agent-history-recovery-'+width+'.png');await page.screenshot({path:historyShot});shots.push(historyShot);
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body!==undefined && ['/api/dm/new','/api/groups/new','/api/dm/agent/invite'].includes(r.path))),'starter must not create human chats or invite');
   await settle(page);const shot=path.join(evidence,'comic-agent-starter-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'document overflow');assert.deepEqual(errors,[]);
   await page.evaluate(()=>fixture.unmount());assert.deepEqual(await page.evaluate(async()=>{const d=fixture.bag.comicDrafts.drafts[fixture.legacyHistory];return {text:d.text,sameFile:d.files[0].file===fixture.historyFile,bytes:await d.files[0].file.text()};}),{text:'Keep this original history draft',sameFile:true,bytes:'Original retained draft bytes'},'opening current-agent recovery preserves the original draft and exact File object');await context.close();
  }
  if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'));fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  console.log(JSON.stringify({ok:true,checks:panelMode?'Readable Agents/Review workspaces; setup Back; Settings details and phone Back focus; Chats search/New/empty; 64-character default name; exact conversation/draft/File retained without sends or staging; desktop/phone':process.env.AGENTNET_REMOVED_PERMISSIONS?'Removed phone has no active permission action before/after reload; replacement and Bob remain; history retained; no writes; desktop/phone':'Current-only agent discovery; retained disabled/unready catalog records, legacy/retired searchable history and exact recovery preserving draft/File; local default and exact local/remote named question; fresh readiness/catalog refusal keeps draft, no default substitution; no human DM/group; desktop/phone',shots}));
 }finally{if(browser)await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
