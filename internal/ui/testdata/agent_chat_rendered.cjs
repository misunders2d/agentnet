// Inert public-host fixture; production skins, no Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{await document.fonts.ready;await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});};
const root=process.env.AGENTNET_STATIC_ROOT||path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const at='2026-10-06T10:00:00Z',local='sergey/laptop',remote='sergey/zenbook',localID='a'.repeat(32),remoteID='b'.repeat(32),defaultID='d'.repeat(32),disabledID='9'.repeat(32),legacyHistory='e'.repeat(32),retiredHistory='f'.repeat(32),panelChat='7'.repeat(32),panelMode=!!process.env.AGENTNET_PANEL_LAYOUT,resizeMode=!!process.env.AGENTNET_COMPOSER_RESIZE;
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
if(panelMode||resizeMode){overview.topic_list=true;overview.threads.push({id:panelChat,peer:local,agent_host:local,agent_key:'local-key',agent_id:localID,title:'Existing panel conversation',last:'Original panel message',last_at:at,count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:at});}
const boot=`
const seed=${JSON.stringify({overview,responder,local,remote,localID,remoteID,disabledID,legacyHistory,retiredHistory,panelChat,panelMode:panelMode||resizeMode,assignMode:!!process.env.AGENTNET_ASSIGN_MESSAGE,removedMode:!!process.env.AGENTNET_REMOVED_PERMISSIONS})};window.fixture={...seed,requests:[],threads:{},dms:{},staged:[],uploads:{},storedBytes:{},discarded:[],fileReads:0,disabled:false,badRemote:false};
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
if(p==='/api/operator/decide'&&fixture.reviewHistory)return {id:'queued-history-answer',state:'custody',note:'Answer queued. Waiting for the host.'};
if(p.startsWith('/api/agents')){const h=new URL(p,location.origin).searchParams.get('host'),remote=!!h;return {host:remote?(fixture.badRemote?'wrong/computer':h):seed.local,local:!remote,harnesses:seed.responder.harnesses,agents:remote?[{record:${JSON.stringify(record(remote,remoteID,'Zenbook agent'))},enabled:true}]:[{record:{...${JSON.stringify(record(local,defaultID,'Codex'))},label:fixture.defaultLabel||'Codex'},default:true,enabled:true,responder:fixture.responder},{record:${JSON.stringify(record(local,localID,'Pi','pi'))},enabled:!fixture.disabled,responder:{...fixture.responder,agent_id:seed.localID,harness:'pi'}},{record:${JSON.stringify(record(local,'c'.repeat(32),'Sleeping agent','omp'))},enabled:true,responder:{...fixture.responder,harness:'omp',ready:false,problem:'Not ready in fixture'}},{record:${JSON.stringify(record(local,disabledID,'Codex'))},enabled:false,responder:fixture.responder}]};}
if(seed.panelMode&&p==='/api/topic/transfer/location'){if(!body||Object.keys(body).sort().join(',')!=='peer,thread'||body.peer!==seed.local||body.thread!==seed.panelChat)throw Error('Unexpected panel location lookup scope');return null;}
if(seed.panelMode&&p==='/api/assistant-setup'){if(body!==undefined)throw Error('Panel navigation must not apply setup');return {local:true,harnesses:[['codex','Codex'],['pi','Pi'],['omp','OMP']].map(([id,label])=>({id,label,detected:true,configured:true,registered:true,supported:true,state:'connected',note:'Inert layout fixture native setup.'}))};}
if(p==='/api/upload/discard'){for(const id of body.ids){fixture.discarded.push(id);delete fixture.uploads[id];}return {};}
if(p==='/api/groups/new'){const id=String(Object.keys(fixture.dms).length+1).padStart(64,'c'),self={person:'sergey',label:'Sergey',address:seed.local,fingerprint:'local-key',state:'self'};fixture.overview.dms.push({id,kind:'group',title:body.title,members:[self],peer:self,created:'${at}',last_at:'${at}',count:0,unread:0});fixture.dms[id]={id,kind:'group',title:body.title,role:'admin',mine:true,members:[self],peer:self,created:'${at}',agents:[],topics:[],messages:[]};return {id};}
if(p.startsWith('/api/dm?'))return structuredClone(fixture.dms[new URL(p,location.origin).searchParams.get('id')]);
if(p==='/api/dm/agent/invite'){const d=fixture.dms[body.conv];if(!d)throw Error('Unknown room');const self={person:'sergey',label:'Sergey',address:seed.local};const v={pid:'pid-'+(d.agents.length+1),agent_id:body.agent_id,topic:body.topic,host:{...self,address:body.host,fingerprint:body.host===seed.local?'local-key':'remote-key'},host_here:body.host===seed.local,inviter:self,state:'active',state_text:'',shared:[],missing:0,tasks_from:[],held:0,invited:'${at}',can_decide:false,can_dismiss:true,can_ask:true,owner_invited:true};d.agents.push(v);return structuredClone(v);}
if(p==='/api/dm/agent/ask'){const d=Object.values(fixture.dms).find(x=>x.agents.some(a=>a.pid===body.pid));if(!d)throw Error('Unknown agent');const staged=(body.files||[]).map(id=>fixture.uploads[id]);for(const id of body.files||[])delete fixture.uploads[id];if(fixture.failNextSend){fixture.failNextSend=false;throw Error('Temporary send failure');}
 const kept=d.messages.find(m=>m.id===body.id);if(kept){if(kept.body!==body.body||kept.kind!==body.kind||kept.pid!==body.pid)throw Error('This send ID is kept with other words.');return {id:body.id,lid:body.id,state:'custody'};}
 for(const [index,file] of staged.entries())fixture.storedBytes[body.id+':'+index]=file.text;
 if(!d.topics.some(t=>t.id===body.topic))d.topics.push({id:body.topic,peer:seed.local,title:body.body.split('\\n')[0].slice(0,80),last:body.body,last_at:'${at}',count:1,review:0,unread:0,running:0,waiting:false,state:'active',quiet_since:'${at}'});
 d.messages.push({id:body.id,lid:body.id,dir:'out',from:seed.local,kind:body.kind,body:body.body,topic:body.topic,pid:body.pid,origin:'ui',at:'${at}',state:'custody',state_text:'',verified_agent:false,attachments:seed.assignMode&&body.body==='Default question'?[{index:0,name:'source.txt',size:20,openable:true},{index:1,name:'second.txt',size:20,openable:true},{index:2,name:'missing.txt',size:40,openable:false}]:staged.map((f,index)=>({index,name:f.name,size:f.text.length,openable:true}))});
 const summary=fixture.overview.dms.find(x=>x.id===d.id);summary.count=d.messages.length;summary.last=body.body;fixture.overview.seq++;
 if(fixture.loseNextSendSuccess){fixture.loseNextSendSuccess=false;fixture.lostTask=body.id;throw Error('Response lost after durable storage');}
 setTimeout(()=>fixture.change(),0);return {id:body.id,lid:body.id,state:'custody'};}
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
async function checkReviewHistory(page,width,nav,shots){
 await page.evaluate(()=>{
  const id=fixture.legacyHistory,host=fixture.remote;
  const summary={id,peer:host,agent_host:host,agent_key:'remote-key',agent_id:'',agent_legacy:true,title:'Retained waiting request',last:'Original retained question',last_at:'2026-10-06T10:00:00Z',count:1,review:1,unread:0,running:0,state:'needs_human',quiet_since:'2026-10-06T10:00:00Z'};
  const continuation={host,id,key:'original-request-key',attempt:7};
  const message={id,dir:'out',from:fixture.local,to:host,kind:'question',body:'Original retained question',at:'2026-10-06T10:00:00Z',state:'delivered',history:true,author:{label:'You'},detail:'Exact original clarification for the retained request.',exec:{host,state:'needs_human',attempt:7},continuation,actions:['continue']};
  fixture.threads[id]={id,peer:host,key:{pinned:'remote-key'},approved:true,messages:[message],topic:summary};
  fixture.overview.threads.push(summary);
  fixture.overview.review=[{id,peer:host,kind:'question',reason:'needs_human',why:message.detail,at:message.at}];
  fixture.reviewHistory=continuation;fixture.change();
 });
 await nav.getByRole('button',{name:/^Review/}).click();
 await page.getByRole('button',{name:/\. Review it\.$/}).click();
 const conversation=page.locator('[data-part="conversation"]');
 const recovery=conversation.getByRole('region',{name:'Agent history',exact:true});await recovery.waitFor();
 assert.equal(await recovery.locator('p').innerText(),'This is saved agent history. Its messages, draft and files stay here. For new work, choose a configured agent below.');
 await recovery.getByRole('button',{name:'Chat with Zenbook agent · Claude',exact:true}).waitFor();
 const row=conversation.locator('[data-mid="'+legacyHistory+'"]');
 const card=row.locator('section[aria-labelledby]').filter({has:page.locator('[data-agent-needs-you]')});
 await card.getByText('Exact original clarification for the retained request.',{exact:true}).waitFor();
 const guidance=card.locator('p').filter({hasText:'For a native permission or required authorization'});
 assert.match(await guidance.innerText(),/Use Reply/);
 assert.match(await guidance.innerText(),/Mark as handled/);
 assert.doesNotMatch(await guidance.innerText(),/Ask again|tag the same agent|attention clears/);
 await card.getByText('A reply supplies information; it does not grant new permissions.',{exact:true}).waitFor();
 assert.doesNotMatch(await card.innerText(),/Chat messages don’t approve or restart it/);
 await card.getByRole('button',{name:'Reply',exact:true}).waitFor();
 await card.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
 assert.equal(await card.getByRole('button',{name:'Ask again',exact:true}).count(),0);
 await settle(page);const combined=path.join(evidence,'comic-review-history-'+width+'.png');await page.screenshot({path:combined});shots.push(combined);
 await card.getByRole('button',{name:'Reply',exact:true}).click();
 const answer=page.getByRole('dialog',{name:'Answer your agent',exact:true});await answer.waitFor();
 await answer.getByRole('textbox',{name:'Your answer',exact:true}).fill('Use the recorded delivery date.');
 await settle(page);const answering=path.join(evidence,'comic-review-history-answer-'+width+'.png');await page.screenshot({path:answering});shots.push(answering);
 await answer.getByRole('button',{name:'Send answer',exact:true}).click();await answer.waitFor({state:'hidden'});
 await page.getByText('Answer queued. Waiting for the host.',{exact:true}).waitFor();
 const decisions=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'));
 assert.equal(decisions.length,1,'Reply uses only the exact request continuation route');
 const expected=await page.evaluate(()=>fixture.reviewHistory),body=decisions[0].body;
 assert.match(body.send_id,/^[a-f0-9]{32}$/);
 assert.deepEqual(body,{host:expected.host,id:expected.id,key:expected.key,attempt:expected.attempt,action:'continue',expect:'needs_human',text:'Use the recorded delivery date.',send_id:body.send_id,report:''});
 assert.equal(await page.evaluate(()=>fixture.threads[fixture.legacyHistory].messages[0].exec.state),'needs_human','queueing an answer does not claim the host completed it');
 // Guidance follows each actually offered action, with no new authorization.
 for(const variant of [{actions:['resolve']},{actions:['accept']},{actions:['continue'],continuation:{id:legacyHistory,key:'original-request-key',attempt:7}}]){
  await page.evaluate(variant=>{const m=fixture.threads[fixture.legacyHistory].messages[0];delete m.continuation;if(variant.continuation)m.continuation=variant.continuation;m.actions=variant.actions;fixture.change();},variant);
  await card.getByRole('button',{name:'Reply',exact:true}).waitFor({state:variant.continuation?'visible':'hidden'});
  if(variant.actions[0]!=='continue')await card.getByRole('button',{name:variant.actions[0]==='resolve'?'Mark as handled':'Ask again',exact:true}).waitFor();
  if(!variant.actions.includes('resolve'))await card.getByRole('button',{name:'Mark as handled',exact:true}).waitFor({state:'hidden'});
  if(!variant.actions.includes('accept'))await card.getByRole('button',{name:'Ask again',exact:true}).waitFor({state:'hidden'});
  const text=await guidance.innerText();assert.doesNotMatch(text,/tag the same agent/);
  assert.equal(text.includes('Use Reply'),!!variant.continuation);
  assert.equal(text.includes('Ask again'),variant.actions.includes('accept'));
  assert.equal(text.includes('Mark as handled'),variant.actions.includes('resolve'));
  await card.getByText(variant.continuation?'A reply supplies information; it does not grant new permissions.':'This request still needs attention. Native permissions stay unchanged.',{exact:true}).waitFor();
 }
 assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/send').length),0,'history and exact continuation never start a replacement conversation');
 assert.equal(await page.evaluate(()=>fixture.staged.length),0,'original history File is never staged for a continuation');
 assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'combined history request fits viewport');
 await page.evaluate(()=>fixture.unmount());
 assert.deepEqual(await page.evaluate(async()=>{const d=fixture.bag.comicDrafts.drafts[fixture.legacyHistory];return {text:d.text,sameFile:d.files[0].file===fixture.historyFile,bytes:await d.files[0].file.text()};}),{text:'Keep this original history draft',sameFile:true,bytes:'Original retained draft bytes'},'exact request Reply preserves the original history draft and File object');
}
async function checkComposerResize(page,width,shots){
 const field=page.locator('form[data-part="composer"] textarea').filter({visible:true});
 await field.waitFor();await field.fill('');await settle(page);
 const height=()=>field.evaluate(el=>el.getBoundingClientRect().height);
 const small=await height();
 await field.evaluate(el=>{
  window.composerResize={writes:[]};
  window.composerResize.observer=new MutationObserver(records=>window.composerResize.writes.push(...records.map(r=>({before:r.oldValue,height:el.style.height}))));
  window.composerResize.observer.observe(el,{attributes:true,attributeFilter:['style'],attributeOldValue:true});
 });
 await field.pressSequentially('short text');await settle(page);
 const writes=await page.evaluate(()=>composerResize.writes);
 assert(writes.length<=2,'single-line typing must not reset and rewrite live height for every keystroke: '+writes.length+' style writes');
 assert.equal(await field.inputValue(),'short text');
 await field.fill('one\ntwo\nthree\nfour');
 await field.evaluate(el=>new Promise((resolve,reject)=>{let n=0;const check=()=>{if(el.getBoundingClientRect().height>80)return resolve();if(++n>60)return reject(Error('multiline composer never grew'));requestAnimationFrame(check);};check();}));
 const grown=await height();
 await field.fill('one\ntwo\nthree\nfour\n');await settle(page);
 assert(await height()>grown+10,'trailing newline retains its own line');
 await field.fill('x');await settle(page);assert(await height()<=small+1,'deleting multiline content shrinks the field');
 await field.fill(Array.from({length:40},(_,i)=>'Wrapped line '+i+' keeps the composer capped.').join('\n'));await settle(page);
 assert(await height()<=(width>=1024?240:140)+1,'field respects wide/narrow cap');
 await field.press('Control+End');await settle(page);
 assert(await field.evaluate(el=>{
  const mirror=el.parentElement.querySelector('[aria-hidden="true"]');return el.scrollTop>0&&Math.abs(mirror.scrollTop-el.scrollTop)<=1;
 }),'capped input and visible mention mirror scroll together');
 await field.fill('composition');await field.dispatchEvent('compositionstart');await field.press('ArrowLeft');await field.dispatchEvent('compositionend');
 assert.equal(await field.inputValue(),'composition','IME/selection events retain input');
 await field.fill('one\ntwo\nthree\nfour\nfive\nsix\nseven');await settle(page);
 await page.setViewportSize({width,height:480});await settle(page);
 assert(await field.evaluate(el=>{const form=el.closest('form'),send=form.querySelector('button[type="submit"]');return send&&send.getBoundingClientRect().bottom<=innerHeight+1;}),'Send remains inside reduced available height');
 await page.setViewportSize({width,height:900});await settle(page);
 await page.evaluate(()=>{fixture.threads[fixture.panelChat].key.pending='changed-fixture-key';fixture.change();});
 await field.waitFor({state:'hidden'});
 await page.evaluate(()=>{delete fixture.threads[fixture.panelChat].key.pending;fixture.change();});
 await field.waitFor({state:'visible'});
 await field.fill('one\ntwo\nthree\nfour');await settle(page);
 assert(await height()>80,'same conversation reattaches sizing after read-only state');
 await field.evaluate(el=>{composerResize.observer.disconnect();composerResize.writes=[];composerResize.observer.observe(el,{attributes:true,attributeFilter:['style'],attributeOldValue:true});});
 await settle(page);await settle(page);
 assert.equal(await page.evaluate(()=>composerResize.writes.length),0,'settled geometry does not induce a height-write loop');
 const file=path.join(evidence,'comic-composer-resize-'+width+'.png');await page.screenshot({path:file});shots.push(file);
 assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/send').length),0,'sizing checks send no extra work');
 await field.fill('resize on departure\nsecond line');await page.evaluate(()=>fixture.unmount());
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
}

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
 assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined&&r.path!=='/api/refresh'&&!(r.path==='/api/act'&&r.body.do==='read')&&!(r.path==='/api/topic/transfer/location'&&Object.keys(r.body).sort().join(',')==='peer,thread'&&r.body.peer===fixture.local&&r.body.thread===fixture.panelChat)).length),0,'browsing panels sends no work, setup changes, permission grants or file stages');
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
   const nav=page.getByRole('navigation',{name:'Main'}),sent=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').map(r=>r.body));
   await nav.getByRole('button',{name:'Agents',exact:true}).click();
   if(process.env.AGENTNET_REVIEW_HISTORY){await checkReviewHistory(page,width,nav,shots);assert.deepEqual(errors,[]);await context.close();continue;}
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
   if(resizeMode){await page.evaluate(()=>fixture.openThread(fixture.panelChat));await checkComposerResize(page,width,shots);assert.deepEqual(errors,[]);await context.close();continue;}
   if(panelMode){
    await checkPanelLayout(page,width,nav,shots);
    assert.deepEqual(errors,[]);await page.evaluate(()=>fixture.unmount());
    assert.deepEqual(await page.evaluate(async()=>{const saved=fixture.bag.comicDrafts,d=saved.drafts[fixture.panelChat];return {open:saved.open?.id,agent:fixture.threads[fixture.panelChat].topic.agent_id,text:d.text,sameFile:d.files[0].file===fixture.panelFile,bytes:await d.files[0].file.text(),sends:fixture.requests.filter(r=>r.path==='/api/send').length,stages:fixture.staged.length};}),{open:panelChat,agent:localID,text:'Keep the exact panel draft',sameFile:true,bytes:'Exact original panel draft bytes',sends:0,stages:0},'panel navigation retains the exact conversation, identity, draft and original File');
    await context.close();continue;
   }
   // MEL-713: Chat with asks in a new topic of the person's My agents room.
   // The question shows at once, pending; the agent's check, invitation and
   // ask follow, and a failure keeps it there with Retry. A disabled agent
   // never becomes the default.
   const conv=page.locator('[data-part="conversation"]'),roomMsg=id=>page.locator('[data-mid="'+id+'"]');
   const invites=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/invite').map(r=>r.body));
   const rooms=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/new').length);
   const pidOf=topic=>page.evaluate(topic=>Object.values(fixture.dms)[0].agents.find(a=>a.topic===topic).pid,topic);
   await page.getByRole('button',{name:'Chat with Pi',exact:true}).click();
   let sheet=page.getByRole('dialog',{name:'Chat with Pi',exact:true});
   await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Keep my named question');
   await page.evaluate(()=>fixture.disabled=true);const clicked=Date.now();await sheet.getByRole('button',{name:'Send question',exact:true}).click();
   await conv.getByText('Keep my named question').first().waitFor();const shown=Date.now()-clicked;assert(shown<1000,'Chat with shows the question within one second: '+shown+'ms');
   await sheet.waitFor({state:'hidden'});
   await conv.getByText(/selected agent is unavailable/).first().waitFor();assert.deepEqual(await sent(),[],'disabled selected agent must not become default');
   assert.equal(await rooms(),1,'the My agents room is made once');
   await page.evaluate(()=>fixture.disabled=false);await conv.getByRole('button',{name:'Retry',exact:true}).first().click();
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/agent/ask'));
   const first=(await sent())[0];await roomMsg(first.id).waitFor();assert.match(await roomMsg(first.id).innerText(),/Keep my named question/);
   let invite=(await invites()).at(-1);assert.equal(invite.host,local);assert.equal(invite.agent_id,localID);assert.equal(invite.topic,first.topic);
   assert.deepEqual([...invite.tasks_from].sort(),['local-key','phone-key','remote-key'],'own device keys may task an own agent');
   assert.equal(first.kind,'question');assert.equal(first.pid,await pidOf(first.topic));
   if(process.env.AGENTNET_MODEL_REPORT){const runtime=page.locator('[data-part=agent-runtime]');await runtime.getByText(/Model and reasoning not reported/).waitFor();assert(!/gpt-6/.test(await runtime.innerText()),'active named agent never borrows default metadata');}
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:/^Chats/}).click();await page.getByRole('button',{name:'New',exact:true}).click();
   const newChat=page.getByRole('dialog',{name:'New chat',exact:true});await newChat.getByRole('button',{name:'Chat with Zenbook agent · Claude',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Zenbook agent · Claude',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Question for Zenbook');
   await page.evaluate(()=>fixture.badRemote=true);await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});await newChat.waitFor({state:'hidden'});
   await conv.getByText(/does not match/).first().waitFor();assert.equal((await sent()).length,1);
   await page.evaluate(()=>fixture.badRemote=false);await conv.getByRole('button',{name:'Retry',exact:true}).first().click();
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===2);
   const second=(await sent())[1];invite=(await invites()).at(-1);assert.equal(invite.host,remote);assert.equal(invite.agent_id,remoteID);
   assert.equal(second.topic,invite.topic);assert.notEqual(second.topic,first.topic,'each chat is its own topic');assert.equal(second.pid,await pidOf(second.topic));assert.equal(await rooms(),1,'later chats reuse the room');
   if(process.env.AGENTNET_MODEL_REPORT){const runtime=page.locator('[data-part=agent-runtime]');await runtime.getByText(/Last reported model: Claude Opus 4.6/).waitFor();assert.match(await runtime.innerText(),/Reasoning: not reported/,'absent effort stays explicit in active remote agent chat');}
   if(width<1024){await settle(page);await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
   await nav.getByRole('button',{name:'Agents',exact:true}).click();await page.getByRole('button',{name:'Chat with Codex',exact:true}).click();
   sheet=page.getByRole('dialog',{name:'Chat with Codex',exact:true});await sheet.getByRole('textbox',{name:'Your message',exact:true}).fill('Default question');await sheet.getByRole('button',{name:'Send question',exact:true}).click();await sheet.waitFor({state:'hidden'});
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===3);
   const third=(await sent())[2];invite=(await invites()).at(-1);assert.equal(invite.host,local);assert.equal(invite.agent_id,defaultID);await roomMsg(third.id).waitFor();
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
    const source=roomMsg(third.id);
    if(width>=1024){await source.locator('[data-message-bubble]').hover();await source.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Assign to your agent…',exact:true}).click();}
    else{await source.getByRole('button',{name:'Message actions',exact:true}).focus();await source.getByRole('button',{name:'Message actions',exact:true}).click();await page.getByRole('button',{name:'Assign to your agent…',exact:true}).click();}
    const assign=page.getByRole('dialog',{name:'Assign to your agent',exact:true});await assign.getByRole('combobox',{name:'Your agent',exact:true}).selectOption(remote+'#'+remoteID);
    await assign.getByRole('textbox',{name:'What should it do?',exact:true}).fill('Review the selected request');
    assert.equal((await sent()).length,3,'opening a reviewed assignment sends no task');assert(await assign.getByRole('checkbox',{name:'source.txt',exact:true}).isChecked());assert(await assign.getByRole('checkbox',{name:/missing.txt/}).isDisabled());
    await settle(page);const assignmentShot=path.join(evidence,'comic-assign-preview-'+width+'.png');await page.screenshot({path:assignmentShot});shots.push(assignmentShot);
    assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'assignment preview overflow');
    const setBody=text=>page.evaluate(({id,text})=>{Object.values(fixture.dms)[0].messages.find(m=>m.id===id).body=text;},{id:third.id,text});
    await setBody('Changed after selection');
    await assign.getByRole('button',{name:'Send task',exact:true}).click();await assign.getByRole('alert').waitFor();assert.match(await assign.getByRole('alert').innerText(),/message changed|no longer available/i);assert.equal((await sent()).length,3,'stale source is not silently delegated');
    await setBody('Default question');await page.evaluate(()=>{fixture.failStageAfterFirst=true;});
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
    const keptID=await page.evaluate(()=>fixture.lostTask);
    assert.equal(await page.evaluate(()=>Object.values(fixture.dms)[0].messages.filter(m=>m.kind==='task').length),1,'lost success really persisted one task');
    // The same send ID makes the retry the same task: the room keeps one.
    await assign.getByRole('button',{name:'Retry this task',exact:true}).click();await assign.waitFor({state:'hidden'});
    await roomMsg(keptID).waitFor();
    assert.equal(await page.evaluate(()=>Object.values(fixture.dms)[0].messages.filter(m=>m.kind==='task').length),1,'retry after a lost response adds no second task');
    const attempts=(await sent()).slice(3);assert.equal(attempts.length,3);for(const x of attempts)assert.deepEqual({...x,files:[]},{...attempts[0],files:[]},'retry retains exact request ID, agent, topic and body');
    for(const x of attempts)assert.equal(x.files.length,2);assert.notEqual(attempts[0].files[0],attempts[1].files[0],'consumed upload is restaged');
    assert.deepEqual(await page.evaluate(()=>Object.keys(fixture.uploads)),[],'successful and failed send stages are consumed or discarded');
    assert.equal(await page.evaluate(()=>fixture.fileReads),2,'source bytes are read once and retained');
    invite=(await invites()).at(-1);assert.equal(invite.host,remote);assert.equal(invite.agent_id,remoteID);
    const task=attempts.at(-1);assert.equal(task.id,keptID);assert.equal(task.pid,await pidOf(task.topic));assert.equal(task.topic,invite.topic);assert.equal(task.kind,'task');
    assert.match(task.body,/Review the selected request/);assert.match(task.body,/> Default question/);assert.match(task.body,/Selected message from You/);
    const sourceLink=page.getByRole('button',{name:'View original',exact:true});await sourceLink.waitFor();await sourceLink.click();await source.waitFor();assert.equal((await sent()).length,6,'recovery and source navigation never execute again');
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
   assert.equal(await chats.locator('[data-part="chat-row"][data-kind="agent"]').count(),0,'legacy and retired exchanges never return as active agent cards; Chat with makes no device chats');
   await chats.getByText('My agents',{exact:true}).first().waitFor();
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
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body!==undefined && r.path==='/api/dm/new')),'starter must not create human chats');
   assert.equal(await rooms(),1,'starter makes only the one My agents room');assert((await invites()).every(x=>[local,remote].includes(x.host)),'starter invites only own agents');
   await settle(page);const shot=path.join(evidence,'comic-agent-starter-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'document overflow');assert.deepEqual(errors,[]);
   await page.evaluate(()=>fixture.unmount());assert.deepEqual(await page.evaluate(async()=>{const d=fixture.bag.comicDrafts.drafts[fixture.legacyHistory];return {text:d.text,sameFile:d.files[0].file===fixture.historyFile,bytes:await d.files[0].file.text()};}),{text:'Keep this original history draft',sameFile:true,bytes:'Original retained draft bytes'},'opening current-agent recovery preserves the original draft and exact File object');await context.close();
  }
  if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'));fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  console.log(JSON.stringify({ok:true,checks:resizeMode?'Composer bounded height writes; multiline/trailing-newline growth and shrink, cap, mirror scroll, composition, reduced height and departure; desktop/narrow':process.env.AGENTNET_REVIEW_HISTORY?'Same saved-history request and current catalog; exact continuation Reply, original draft/File, capability-bound guidance; desktop/phone':panelMode?'Readable Agents/Review workspaces; setup Back; Settings details and phone Back focus; Chats search/New/empty; 64-character default name; exact conversation/draft/File retained without sends or staging; desktop/phone':process.env.AGENTNET_REMOVED_PERMISSIONS?'Removed phone has no active permission action before/after reload; replacement and Bob remain; history retained; no writes; desktop/phone':'Current-only agent discovery; retained disabled/unready catalog records, legacy/retired searchable history and exact recovery preserving draft/File; local default and exact local/remote named question; fresh readiness/catalog refusal keeps draft, no default substitution; no human DM/group; desktop/phone',shots}));
 }finally{if(browser)await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
