// Production loader/app/styles, synthetic loopback provider only. No native harness claim.
'use strict';
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict'),os=require('node:os'),{createHash}=require('node:crypto');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const assets=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_GROUP_RECEIVER_EVIDENCE;
assert(evidence,'private evidence path required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const profile=fs.mkdtempSync(path.join(process.env.TMPDIR||os.tmpdir(),'group-receiver-ui-'));
const local='a'.repeat(32),remote='b'.repeat(32),conv='c'.repeat(32),pid='d'.repeat(32),address='alice/laptop';
const copyOnly=process.env.AGENTNET_CLAUDE_BACKUP_COPY_ONLY==='1',affectedOnly=copyOnly||process.env.AGENTNET_CLAUDE_BACKUP_ONLY==='1';
const checks=[],requests=[],outside=[],errors=[],shots=[],served={};let browser,server,lastPage,stage='setup',native=true;
const sha=b=>createHash('sha256').update(b).digest('hex');
const members=[{person:'1'.repeat(32),label:'Alice',address,state:'self'},{person:'2'.repeat(32),label:'Bob',address:'bob/desk',state:'pinned'}];
const agent={pid,agent_id:remote,host:{label:'Bob',address:'bob/desk',state:'pinned'},inviter:members[0],shared:[],missing:0,tasks_from:[],state:'active',state_text:'Accepted',can_ask:true};
const thread={id:conv,kind:'group',role:'member',title:'Synthetic group receiver',created:'2026-10-01T10:00:00Z',peer:{address:'bob/desk',label:'Bob'},members,agents:[agent],messages:[]};
const overview=()=>({demo:false,version:'synthetic-1005b',me:{address,fingerprint:'SHA256:synthetic'},threads:[],review:[],quarantine:[],dms:[{...thread,last_at:thread.created}],seq:1,reply_receivers:native,reply_sessions:native,files:{max_file:100000,max_message:200000,max_count:4}});
const catalog={local:true,host:address,agents:[{enabled:true,record:{id:local,host:address,label:'Local Receiver'},responder:{ready:true,harness:'codex'}}]};
const sessions={local:true,host:address,sessions:[{handle:'exact-OMP',harness:'omp',label:'Selected OMP',active:true},{handle:'exact-Claude',harness:'claude',label:'Selected Claude',active:true}]};
const json=(res,value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const mime=p=>p.endsWith('.js')||p.endsWith('.mjs')?'text/javascript':p.endsWith('.css')?'text/css':p.endsWith('.html')?'text/html':'image/png';
async function listen(){
 server=http.createServer(async(req,res)=>{
  const url=new URL(req.url,'http://127.0.0.1'),name=url.pathname;let raw='';for await(const chunk of req)raw+=chunk;
  const body=raw&&req.headers['content-type']?.includes('application/json')?JSON.parse(raw):null;
  if(name==='/events'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': fixture stream\n\n');return;}
  if(name==='/assets/skins/index.json')return json(res,[{id:'default',api:1,name:'AgentNet'}]);
  if(name==='/assets/icon-192.png'&&process.env.AGENTNET_GROUP_RECEIVER_ICON){const bytes=fs.readFileSync(process.env.AGENTNET_GROUP_RECEIVER_ICON);served['icon-192.png']=sha(bytes);res.writeHead(200,{'Content-Type':'image/png'});res.end(bytes);return;}
  if(name==='/'||name.startsWith('/assets/')){
   const file=name==='/'?'index.html':name.slice('/assets/'.length);if(file.includes('..'))return json(res,{},403);
   const target=path.join(assets,file);if(!fs.existsSync(target))return json(res,{},404);
   const bytes=fs.readFileSync(target);served[file]=sha(bytes);res.writeHead(200,{'Content-Type':mime(file)});res.end(bytes);return;
  }
  if(name==='/api/overview')return json(res,overview());
  if(name==='/api/dm')return json(res,thread);
  if(name==='/api/agents')return json(res,catalog);
  if(name==='/api/reply-sessions')return json(res,sessions);
  if(name==='/api/reply-receivers')return json(res,[]);
  if(name==='/api/typing/settings')return json(res,{enabled:false});
  if(name==='/api/typing')return json(res,[]);
  if(name==='/api/upload'){requests.push({path:name,size:Buffer.byteLength(raw),name:url.searchParams.get('name')});return json(res,{id:'exact-staged-file'});}
  if(name==='/api/dm/send'||name==='/api/dm/agent/ask'){requests.push({path:name,body});return json(res,{id:'e'.repeat(32),state:'queued'});}
  if(name==='/api/refresh'||name==='/api/act')return json(res,{});
  return json(res,{},404);
 });await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});return 'http://127.0.0.1:'+server.address().port;
}
async function open(p){await p.waitForFunction(()=>typeof state!=='undefined'&&state.overview);await p.evaluate(id=>openDM(id),conv);await p.waitForFunction(id=>state.dmData?.id===id,conv);}
async function expand(p){if(!await p.locator('#receiver-options').evaluate(d=>d.open))await p.locator('#receiver-summary').click();await p.locator('#receiver-picker').waitFor();}
async function capture(p,name,width){
 const file=path.join(evidence,name+'-'+width+'.png');await p.screenshot({path:file});shots.push(file);
 assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,name+' document overflow');
 const view=p.viewportSize(),send=await p.locator('#send').boundingBox(),to=await p.locator('#to-line').boundingBox();
 assert(send&&send.y>=0&&send.y+send.height<=view.height+1,name+' visible Send');assert(to&&to.y>=0&&to.y+to.height<=view.height+1,name+' visible target');
 checks.push({name,width,send,to});
}
async function selectManaged(p){await expand(p);await p.locator('#receiver-picker').selectOption(local);await p.locator('#receiver-instructions').fill('Original group continuation');await p.locator('#receiver-mode').selectOption('question');}
(async()=>{
 const origin=await listen();browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,env:{...process.env,HOME:profile,XDG_CONFIG_HOME:profile,XDG_CACHE_HOME:profile},args:['--disable-background-networking','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1']});
 for(const width of [390,1280]){
  native=true;const context=await browser.newContext({viewport:{width,height:width===390?844:900}});
  await context.route('**/*',r=>{if(new URL(r.request().url()).origin===origin)return r.continue();outside.push(new URL(r.request().url()).origin);return r.abort();});
  const p=lastPage=await context.newPage();p.on('pageerror',e=>errors.push(e.message));await p.goto(origin);await open(p);
  let sent;
  if(!affectedOnly){
  stage='native ordinary '+width;await selectManaged(p);await p.locator('#body').fill('Ordinary group '+width);await p.locator('#file-input').setInputFiles({name:'selected.txt',mimeType:'text/plain',buffer:Buffer.from('EXACT SYNTHETIC FILE')});
  assert.equal(await p.locator('#to-name').innerText(),thread.title);await p.locator('#receiver-mode').scrollIntoViewIfNeeded();await capture(p,'native-ordinary-selected',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');
  sent=requests.filter(r=>r.path==='/api/dm/send').at(-1);assert.equal(sent.body.conv,conv);assert.deepEqual(sent.body.reply_receiver,{kind:'managed_agent',agent_id:local,instructions:'Original group continuation',mode:'question'});assert.deepEqual(sent.body.files,['exact-staged-file']);
  stage='native PID '+width;await p.getByRole('button',{name:'Ask',exact:true}).click();await expand(p);await p.locator('#receiver-picker').selectOption('session:exact-OMP');await p.locator('#receiver-backup-picker').selectOption(local);await p.locator('#receiver-backup-instructions').fill('Supported OMP backup');await p.locator('#receiver-backup-mode').selectOption('question');assert.equal(await p.locator('#receiver-backup-picker').inputValue(),local);await p.locator('#receiver-backup-picker').selectOption('');await p.locator('#body').fill('Group PID '+width);assert((await p.locator('#to-name').innerText()).includes('bob/desk'));await capture(p,'native-PID-selected',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');
  sent=requests.filter(r=>r.path==='/api/dm/agent/ask').at(-1);assert.equal(sent.body.pid,pid);assert.deepEqual(sent.body.reply_receiver,{kind:'live_session',session_handle:'exact-OMP'});
  } else await p.getByRole('button',{name:'Ask',exact:true}).click();
  stage='Claude backup unavailable '+width;await expand(p);await p.locator('#receiver-picker').selectOption('session:exact-Claude');assert.equal(await p.locator('#receiver-backup-picker').count(),0);assert((await p.locator('#reply-receiver').innerText()).includes('Replies stay with this session'));
  if(copyOnly){
   await p.locator('#body').fill('Retained Claude request '+width);await p.locator('#file-input').setInputFiles({name:'claude-kept.txt',mimeType:'text/plain',buffer:Buffer.from('CLAUDE EXACT KEPT FILE')});
   await p.evaluate(id=>{state.replyReceiver={...state.replyReceiver,on_close:{agent_id:id,label:'Retained Backup',instructions:'Original unsupported backup instructions',mode:'question',configuration:JSON.stringify({ready:true,harness:'codex'})}};keepDraft();renderReplyReceiver();},local);
   assert((await p.locator('#reply-receiver').innerText()).includes('Remove the backup or choose another receiver before sending.'));
   await p.getByRole('button',{name:'Remove unsupported backup',exact:true}).click();assert(!(await p.locator('#reply-receiver').innerText()).includes('Remove the backup or choose another receiver before sending.'));assert((await p.locator('#reply-receiver').innerText()).includes('Automatic backup after Claude closes is unavailable. Replies stay with this session.'));
   assert.equal(await p.evaluate(()=>state.replyReceiver.on_close),undefined);assert.equal(await p.locator('#receiver-picker').inputValue(),'session:exact-Claude');assert.equal(await p.evaluate(()=>state.files.length),1);assert.equal(await p.locator('#body').inputValue(),'Retained Claude request '+width);assert.equal(requests.length,0);await capture(p,'Claude-backup-explicitly-removed',width);await context.close();continue;
  }
  await p.locator('#body').fill('Retained Claude request '+width);await p.locator('#file-input').setInputFiles({name:'claude-kept.txt',mimeType:'text/plain',buffer:Buffer.from('CLAUDE EXACT KEPT FILE')});
  await p.evaluate(id=>{state.replyReceiver={...state.replyReceiver,on_close:{agent_id:id,label:'Retained Backup',instructions:'Original unsupported backup instructions',mode:'question',configuration:JSON.stringify({ready:true,harness:'codex'})}};keepDraft();renderReplyReceiver();},local);
  const retained=await p.evaluate(()=>JSON.stringify(state.replyReceiver)),beforeClaude=requests.length;await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('compose-error').textContent.includes('Automatic backup after Claude closes is unavailable'));
  assert.equal(requests.length,beforeClaude,'unsupported draft never uploads or sends');assert.equal(await p.evaluate(()=>JSON.stringify(state.replyReceiver)),retained);assert.equal(await p.evaluate(()=>state.files.length),1);assert.equal(await p.locator('#body').inputValue(),'Retained Claude request '+width);
  assert.equal(await p.locator('#receiver-backup-instructions').inputValue(),'Original unsupported backup instructions');assert(await p.locator('#receiver-backup-instructions').isDisabled());assert.equal(await p.locator('#receiver-backup-mode').inputValue(),'question');assert(await p.locator('#receiver-backup-mode').isDisabled());await p.getByRole('button',{name:'Remove unsupported backup',exact:true}).scrollIntoViewIfNeeded();await capture(p,'Claude-retained-backup-refusal',width);
  await p.getByRole('button',{name:'Remove unsupported backup',exact:true}).click();assert.equal(await p.evaluate(()=>state.replyReceiver.on_close),undefined);assert.equal(await p.locator('#receiver-picker').inputValue(),'session:exact-Claude');assert.equal(await p.evaluate(()=>state.files.length),1);assert.equal(await p.locator('#compose-error').innerText(),'');assert(!/SessionEnd|detached/.test(await p.locator('#reply-receiver').innerText()));assert(!(await p.locator('#reply-receiver').innerText()).includes('Remove the backup or choose another receiver before sending.'));await capture(p,'Claude-backup-explicitly-removed',width);
  await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');sent=requests.filter(r=>r.path==='/api/dm/agent/ask').at(-1);assert.deepEqual(sent.body.reply_receiver,{kind:'live_session',session_handle:'exact-Claude'});assert.deepEqual(sent.body.files,['exact-staged-file']);
  checks.push({name:'Claude '+width+' no new backup; old draft/text/file retained; no upload/send until deliberate removal; exact supported Claude intake payload',width});
  if(!affectedOnly){
  stage='browser retained refusal '+width;native=false;await p.evaluate(()=>{window.agentnet=Object.freeze({...window.agentnet,platform:'browser'});state.overview.reply_receivers=false;state.overview.reply_sessions=false;setDMAgent(null);state.replyReceiver={kind:'managed_agent',agent_id:'a'.repeat(32),label:'Retained Receiver',host:'alice/laptop',workspace:'default',instructions:'Keep original',mode:'question'};syncComposer();});
  await expand(p);await p.locator('#body').fill('Browser retained draft');await p.locator('#file-input').setInputFiles({name:'kept.txt',mimeType:'text/plain',buffer:Buffer.from('KEPT')});const before=requests.length;await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('compose-error').textContent.includes('unavailable in this workspace'));assert.equal(requests.length,before);assert.equal(await p.locator('#body').inputValue(),'Browser retained draft');assert.equal(await p.evaluate(()=>state.files.length),1);await capture(p,'browser-retained-refusal',width);
  stage='browser Me '+width;await p.locator('#receiver-picker').selectOption('');assert.deepEqual(await p.locator('#receiver-picker option').allTextContents(),['Me (human)']);await capture(p,'browser-Me-only',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');sent=requests.filter(r=>r.path==='/api/dm/send').at(-1);assert(!sent.body.reply_receiver);
  stage='frozen '+width;await p.evaluate(()=>{state.dmData.frozen='Current access unavailable';syncComposer();});assert(await p.locator('#send').isDisabled());assert(await p.locator('#body').isDisabled());}
  await context.close();
 }
 assert.deepEqual(errors,[]);assert.deepEqual(outside,[]);console.log('group receiver rendered UI ok');
})().catch(async e=>{if(lastPage){try{fs.writeFileSync(path.join(evidence,'failure-state.json'),JSON.stringify(await lastPage.evaluate(()=>({receiver:state.replyReceiver,catalog:state.receiverCatalog,overview:state.overview?.me,picker:document.getElementById('receiver-picker')?.textContent})),null,2),{mode:0o600});await lastPage.screenshot({path:path.join(evidence,'failure.png')});}catch{}}errors.push(stage+': '+e.stack);console.error(stage+': '+e.message);process.exitCode=1;}).finally(async()=>{
 if(browser)await browser.close();if(server){server.closeAllConnections();await new Promise(r=>server.close(r));}
 fs.writeFileSync(path.join(evidence,'result.json'),JSON.stringify({pass:!process.exitCode,affectedOnly,copyOnly,stage,checks,requests,shots,served,errors,outside,profileRemoved:true},null,2),{mode:0o600});fs.rmSync(profile,{recursive:true,force:true});
});
