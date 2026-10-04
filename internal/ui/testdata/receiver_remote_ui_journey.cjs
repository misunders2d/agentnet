// The bundled app (bundled_app.cjs), synthetic loopback provider only. No native harness claim.
'use strict';
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict'),os=require('node:os'),{createHash}=require('node:crypto');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),{bundledAppPage}=require('./bundled_app.cjs');
const assets=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_REMOTE_RECEIVER_EVIDENCE;
assert(evidence,'private evidence path required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const profile=fs.mkdtempSync(path.join(process.env.TMPDIR||os.tmpdir(),'remote-receiver-ui-'));
const local='a'.repeat(32),remote='b'.repeat(32),conv='c'.repeat(32),pid='d'.repeat(32),address='alice/phone',receiverAddress='alice/laptop',fingerprint='11111111-22222222-33333333-44444444';
const checks=[],requests=[],outside=[],errors=[],shots=[],served={};let browser,server,stage='setup',native=true,remoteStatus='ready';
const sha=b=>createHash('sha256').update(b).digest('hex');
const members=[{person:'1'.repeat(32),label:'Alice',address,state:'self'},{person:'2'.repeat(32),label:'Bob',address:'bob/desk',state:'pinned'}];
const agent={pid,agent_id:remote,host:{label:'Bob',address:'bob/desk',state:'pinned'},inviter:members[0],shared:[],missing:0,tasks_from:[],state:'active',state_text:'Accepted',can_ask:true};
const thread={id:conv,kind:'group',role:'member',title:'Synthetic group receiver',created:'2026-10-01T10:00:00Z',peer:{address:'bob/desk',label:'Bob'},members,agents:[agent],messages:[]};
const ownPerson={person:'1'.repeat(32),label:'Alice',state:'self',devices:[{address,fingerprint:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'},{address:receiverAddress,fingerprint}]};
const overview=()=>({person:ownPerson,demo:false,version:'synthetic-1006a',me:{address,fingerprint:'SHA256:synthetic'},threads:[],review:[],quarantine:[],dms:[{...thread,last_at:thread.created}],seq:1,reply_receivers:native,reply_sessions:native,files:{max_file:100000,max_message:200000,max_count:4}});
const catalog={local:true,host:address,agents:[{enabled:true,record:{id:local,host:receiverAddress,label:'Selected Receiver'},responder:{ready:true,harness:'codex'}}]};
const sessions={local:false,host:receiverAddress,host_key:fingerprint,status:'ready',sessions:[{handle:'exact-OMP',harness:'omp',label:'Selected OMP',active:true}]};
const json=(res,value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const mime=p=>p.endsWith('.js')||p.endsWith('.mjs')?'text/javascript':p.endsWith('.css')?'text/css':p.endsWith('.html')?'text/html':'image/png';
async function listen(){
 server=http.createServer(async(req,res)=>{
  const url=new URL(req.url,'http://127.0.0.1'),name=url.pathname;let raw='';for await(const chunk of req)raw+=chunk;
  const body=raw&&req.headers['content-type']?.includes('application/json')?JSON.parse(raw):null;
  if(name==='/events'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': fixture stream\n\n');return;}
  if(name==='/'){res.writeHead(200,{'Content-Type':'text/html'});res.end(bundledAppPage('Remote receiver fixture'));return;}
  if(name==='/assets/icon-192.png'&&process.env.AGENTNET_REMOTE_RECEIVER_ICON){const bytes=fs.readFileSync(process.env.AGENTNET_REMOTE_RECEIVER_ICON);served['icon-192.png']=sha(bytes);res.writeHead(200,{'Content-Type':'image/png'});res.end(bytes);return;}
  if(name.startsWith('/assets/')){
   const file=name.slice('/assets/'.length);if(file.includes('..'))return json(res,{},403);
   const target=path.join(assets,file);if(!fs.existsSync(target))return json(res,{},404);
   const bytes=fs.readFileSync(target);served[file]=sha(bytes);res.writeHead(200,{'Content-Type':mime(file)});res.end(bytes);return;
  }
  if(name==='/api/overview')return json(res,overview());
  if(name==='/api/dm')return json(res,thread);
  if(name==='/api/agents')return json(res,url.searchParams.has('host')?{host:receiverAddress,agents:catalog.agents}:{...catalog,agents:[]});
  if(name==='/api/reply-sessions')return json(res,url.searchParams.has('host')?{...sessions,status:remoteStatus,sessions:remoteStatus==='ready'?sessions.sessions:[]}: {local:true,host:address,sessions:[]});
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
 const box=await p.locator('.compose-box').boundingBox(),error=await p.locator('#compose-error').boundingBox();
 assert(box&&send.y+send.height<=box.y+box.height+1,name+' Send stays inside compose box');
 const timeline=await p.locator('#timeline').boundingBox(),options=await p.locator('.receiver-options-body').evaluate(e=>({client:e.clientHeight,scroll:e.scrollHeight}));
 const layout=await p.evaluate(()=>Object.fromEntries(['.conv','.conv-head','#agents','.composer','#receiver-options','.receiver-options-body','#replying'].map(s=>{const e=document.querySelector(s),r=e?.getBoundingClientRect();return [s,r&&{y:r.y,height:r.height,scroll:e.scrollHeight,client:e.clientHeight,shrink:getComputedStyle(e).flexShrink}];})));
 checks.push({name,width,send,to,box,error,timeline,options,layout});
 if(error){assert(error.y>=send.y+send.height-1,name+' refusal cannot overlap Send');assert(error.y+error.height<=view.height+1,name+' full refusal visible');}
 assert(timeline&&timeline.height>=80,name+' timeline remains at least80px');
 assert(options.client>0&&options.scroll>=options.client,name+' receiver controls remain scrollable');
}
async function chooseHost(p){await expand(p);await p.locator('#receiver-host').selectOption(receiverAddress);await p.waitForFunction(()=>state.receiverCatalog?.remote&&!state.receiverCatalog.loading);}
async function selectManaged(p){await chooseHost(p);await p.locator('#receiver-picker').selectOption(local);await p.locator('#receiver-instructions').fill('Original remote continuation');await p.locator('#receiver-mode').selectOption('question');}
(async()=>{
 const origin=await listen();browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,env:{...process.env,HOME:profile,XDG_CONFIG_HOME:profile,XDG_CACHE_HOME:profile},args:['--disable-background-networking','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1']});
 for(const width of [390,1280])for(const localSupport of [true,false]){
  native=localSupport;remoteStatus='ready';const context=await browser.newContext({viewport:{width,height:width===390?844:900}});
  await context.route('**/*',r=>{if(new URL(r.request().url()).origin===origin)return r.continue();outside.push(new URL(r.request().url()).origin);return r.abort();});
  const p=await context.newPage();p.on('pageerror',e=>errors.push(e.message));await p.goto(origin);await open(p);const label=localSupport?'native':'browser';
  stage=label+' remote ordinary '+width;await selectManaged(p);await p.locator('#body').fill('Ordinary remote '+width);await p.locator('#file-input').setInputFiles({name:'selected.txt',mimeType:'text/plain',buffer:Buffer.from('EXACT REMOTE FILE')});
  assert.equal(await p.locator('#to-name').innerText(),thread.title);await capture(p,label+'-remote-managed',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');
  let sent=requests.filter(r=>r.path==='/api/dm/send').at(-1);assert.deepEqual(sent.body.reply_receiver,{kind:'managed_agent',agent_id:local,instructions:'Original remote continuation',mode:'question',host:{address:receiverAddress,fingerprint}});assert.deepEqual(sent.body.files,['exact-staged-file']);
  stage=label+' remote PID '+width;await p.getByRole('button',{name:'Ask',exact:true}).click();await expand(p);await p.locator('#receiver-picker').selectOption('session:exact-OMP');await p.locator('#receiver-backup-picker').selectOption(local);await p.locator('#receiver-backup-instructions').fill('Original remote backup');await p.locator('#receiver-backup-mode').selectOption('task');await p.locator('#body').fill('Remote PID '+width);await capture(p,label+'-remote-session-PID',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');
  sent=requests.filter(r=>r.path==='/api/dm/agent/ask').at(-1);assert.equal(sent.body.pid,pid);assert.deepEqual(sent.body.reply_receiver,{kind:'live_session',session_handle:'exact-OMP',host:{address:receiverAddress,fingerprint},on_close:{agent_id:local,instructions:'Original remote backup',mode:'task'}});await p.locator('#receiver-backup-picker').selectOption('');
  stage=label+' pending snapshot '+width;remoteStatus='pending';await p.locator('#body').fill('Keep exact pending draft');await p.locator('#file-input').setInputFiles({name:'kept.txt',mimeType:'text/plain',buffer:Buffer.from('KEPT BYTES')});const before=requests.length;await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('compose-error').textContent.includes('pending'));assert.equal(requests.length,before);assert.equal(await p.locator('#body').inputValue(),'Keep exact pending draft');assert.equal(await p.evaluate(()=>state.files.length),1);await capture(p,label+'-remote-pending-refused',width);
  stage=label+' remote Me '+width;await p.locator('#receiver-picker').selectOption('');await capture(p,label+'-remote-Me',width);await p.locator('#send').click();await p.waitForFunction(()=>!state.sending&&document.getElementById('body').value==='');sent=requests.filter(r=>r.path==='/api/dm/agent/ask').at(-1);assert.deepEqual(sent.body.reply_receiver,{kind:'human',host:{address:receiverAddress,fingerprint}});
  stage=label+' changed host '+width;remoteStatus='ready';await p.locator('#receiver-host').selectOption(address);await p.waitForFunction(()=>!state.receiverCatalog?.loading);assert.equal(await p.evaluate(()=>state.replyReceiverHost),null);if(!localSupport)assert.deepEqual(await p.locator('#receiver-picker option').allTextContents(),['Me (human)']);
  await context.close();
 }
 assert.deepEqual(errors,[]);assert.deepEqual(outside,[]);console.log('remote receiver rendered UI ok');
})().catch(e=>{errors.push(stage+': '+e.stack);console.error(stage+': '+e.message);process.exitCode=1;}).finally(async()=>{
 if(browser)await browser.close();if(server){server.closeAllConnections();await new Promise(r=>server.close(r));}
 fs.writeFileSync(path.join(evidence,'result.json'),JSON.stringify({pass:!process.exitCode,stage,checks,requests,shots,served,errors,outside,profileRemoved:true},null,2),{mode:0o600});fs.rmSync(profile,{recursive:true,force:true});
});
