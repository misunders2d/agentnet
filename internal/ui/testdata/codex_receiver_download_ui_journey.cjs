// Reuse existing UI/relay guards and visible setup, not a parallel browser framework.
const fixtureSource=require('node:fs').readFileSync(require('node:path').join(__dirname,'receiver_ui_journey.cjs'),'utf8');
const helpers=fixtureSource.slice(0,fixtureSource.indexOf('// Reuse the qualified native1002t'));
const setup=fixtureSource.slice(fixtureSource.indexOf('(async()=>{'),fixtureSource.indexOf(' // Second membership'));
const journey=String.raw`
 cli('bob','unapprove','admin/laptop');
 const root=path.join(world,'native-codex'),fd=fs.openSync(path.join(world,'native-driver.log'),'w',0o600);
 const driver=spawn('/usr/bin/python3',[path.join(process.env.AGENTNET_NATIVE_SOURCE,'internal/client/testdata/codex_receiver_native.py')],{cwd:world,env:{PATH:'/usr/bin:/bin',AGENTNET_CODEX_RUNTIME:root,AGENTNET_CODEX_BINARY:binary,AGENTNET_CODEX_HOME:path.join(world,'alice'),AGENTNET_CODEX_DOWNLOAD:'1',SSL_CERT_FILE:process.env.SSL_CERT_FILE},stdio:['pipe',fd,fd]});fs.closeSync(fd);
 nativeDriver=driver;result.ownedDriverPID=driver.pid;
 const requestLog=()=>{try{return JSON.parse(fs.readFileSync(path.join(root,'evidence/requests.json'),'utf8'));}catch{return [];}};
 const sessions=()=>JSON.parse(cli('alice','receivers','--sessions','--json'))||[];
 const hooks=()=>{try{return fs.readFileSync(path.join(root,'evidence/hooks.jsonl'),'utf8').trim().split('\n').map(JSON.parse);}catch{return [];}};
 const tell=v=>driver.stdin.write(JSON.stringify(v)+'\n');
 stage='actual Codex registration';await until('first Codex native session registered and initial settled',()=>sessions().length===1&&requestLog().some(r=>!r.title_request)&&hooks().some(h=>h.hook_event_name==='Stop'));
 const selected=sessions().find(s=>s.harness==='codex'&&s.active),selectedHook=hooks().find(h=>h.hook_event_name==='SessionStart');assert(selected);
 const selectedSID=selectedHook.session_id;
 const requestsBefore=requestLog().length;tell({spawn:true});await until('other Codex native session settled',()=>sessions().filter(s=>s.harness==='codex'&&s.active).length===2&&requestLog().length>requestsBefore&&new Set(hooks().filter(h=>h.hook_event_name==='Stop').map(h=>h.session_id)).size===2);
 const other=sessions().find(s=>s.handle!==selected.handle),otherSID=hooks().find(h=>h.hook_event_name==='SessionStart'&&h.session_id!==selectedSID).session_id;
 assert(other&&otherSID);const baseline=requestLog().filter(r=>r.thread===otherSID&&!r.title_request).length;
 const config=fs.readFileSync(path.join(root,'codex/config.toml'),'utf8');assert(config.includes('sandbox_mode = "workspace-write"')&&config.includes('approval_policy = "on-request"')&&config.includes('exclude_slash_tmp = true')&&config.includes('exclude_tmpdir_env_var = true'));
 result.nativeConfig={version:execFileSync('/home/misunderstood/.local/share/mise/installs/codex/0.159.3/bin/codex',['--version'],{encoding:'utf8',env:{HOME:path.join(root,'home'),CODEX_HOME:path.join(root,'codex'),PATH:'/usr/bin:/bin'}}).trim(),mode:'workspace-write',approval:'on-request',network:'outer namespace has only lo; native network true for first Hub blob fetch',tmpExclusions:true,configHash:sha(Buffer.from(config))};
 for(const width of [390,1280]) {
  stage='Codex exact UI/file '+width;await alice.setViewportSize({width,height:width===390?844:900});await openRemote(alice);await alice.getByRole('radio',{name:'Question',exact:true}).check();await alice.getByRole('button',{name:'Refresh agents',exact:true}).click();await until('existing target catalog settled',()=>alice.evaluate(()=>state.targetCatalog&&!state.targetCatalog.loading));assert(await alice.evaluate(B=>!state.targetCatalog.error&&state.targetCatalog.agents.some(a=>a.id===B),B),'exact remote B in settled catalog');await alice.locator('#device-agent-target').selectOption(B);
  await disclose(alice);await alice.getByRole('button',{name:'Refresh local receivers',exact:true}).click();await until('existing receiver catalog settled',()=>alice.evaluate(()=>state.receiverCatalog&&!state.receiverCatalog.loading));assert(await alice.evaluate(handle=>!state.receiverCatalog.error&&state.receiverCatalog.sessions.some(s=>s.handle===handle),selected.handle),'exact native handle in settled catalog');await alice.locator('#receiver-picker').selectOption('session:'+selected.handle);
  assert.equal(await alice.locator('#receiver-picker').inputValue(),'session:'+selected.handle);assert.equal(await alice.locator('#receiver-instructions').count(),0);assert((await alice.locator('#reply-receiver').innerText()).includes('registration is not proof'));
  const body='LOCALLY AUTHORED UI GOAL '+width+': download only this exact correlated attachment using installed AgentNet into the private synthetic work directory under normal permissions.';
  const reply='Verified remote clarification1003y. SYNTHETIC FILE RETURN '+width,bytes=Buffer.from('EXACT CODEX UI FILE '+width+'\n'),filename='codex-'+width+'.txt';
  await alice.locator('#body').fill(body);await alice.locator('#file-input').setInputFiles({name:filename,mimeType:'text/plain',buffer:bytes});await shot(alice,'codex-selected',width);await alice.locator('#send').click();
  let request,binding;await until('original exact native binding created',async()=>{request=(await alice.evaluate(()=>state.data.messages)).find(m=>m.body===body);binding=(await api(alice,'/api/reply-receivers')).find(r=>r.request_ref===request?.id);return binding&&binding.receiver.session_handle===selected.handle;});
  assert.equal(request.target.agent_id,B);assert.equal(binding.receiver.kind,'live_session');assert(!binding.receiver.on_close);
  let copy;await until('Bob received original file',async()=>{const remote=await api(bob,'/api/thread?id='+request.id);copy=remote.messages.find(m=>m.id===request.id);return !!copy?.files?.length;});assert.equal(copy.body,body);assert.equal(copy.target.agent_id,B);assert.deepEqual(await bob.evaluate(async m=>Array.from((await window.agentnet.file(m.id,0,m.dir)).bytes),copy),[...bytes]);
  assert.equal(at('B').length,0,'manual Bob: no racing auto-answer');
  tell({gate:'download',thread:selectedSID,request_ref:request.id,body,reply_body:reply});await until('exact native download gate armed',()=>{try{return JSON.parse(fs.readFileSync(path.join(root,'evidence/gate-ready.json'),'utf8')).request_ref===request.id;}catch{return false;}});
  const returned=path.join(world,'returned-'+width);fs.mkdirSync(returned);const replyFile=path.join(returned,filename);fs.writeFileSync(replyFile,bytes,{mode:0o600});
  const inputID=cli('bob','reply','--wait','0','--file',replyFile,request.id,reply).trim().split(/\s+/)[0];assert.match(inputID,/^[0-9a-f]{32}$/);
  await until('selected native input accepted once',async()=>{binding=(await api(alice,'/api/reply-receivers')).find(r=>r.request_ref===request.id);return binding?.state==='accepted'&&binding.inputs?.length===1&&binding.inputs[0].id===inputID&&binding.inputs[0].state==='accepted';});
  const downloaded=path.join(root,'work/downloaded',filename);await until('native real AgentNet CLI downloaded exact bytes',()=>fs.existsSync(downloaded)&&sha(fs.readFileSync(downloaded))===sha(bytes));
  await until('native tool completion witnessed',()=>hooks().some(h=>h.hook_event_name==='PostToolUse'&&h.session_id===selectedSID)&&requestLog().filter(r=>r.thread===selectedSID&&!r.title_request&&r.selected_reply_present).length>=2*(width===390?1:2));
  const gate=JSON.parse(fs.readFileSync(path.join(root,'evidence/gate.json'),'utf8'));assert.equal(gate.input_id,inputID);assert(gate.original_present&&gate.reply_present&&gate.local_reference_matches);
  const transcript=hooks().find(h=>h.hook_event_name==='SessionStart'&&h.session_id===selectedSID).transcript_path;
  const stats=JSON.parse(execFileSync('/usr/bin/python3',['-c',receiptPython,path.join(world,'alice'),transcript,binding.id,inputID,body,reply],{encoding:'utf8'}));assert.equal(stats.physical,1);assert(stats.original&&stats.reply&&stats.reference&&stats.claim);
  assert.equal(requestLog().filter(r=>r.thread===otherSID&&!r.title_request).length,baseline,'other native session idle');assert.equal(at('A').length,0);assert.equal(at('B').length,0);assert.equal(at('C').length,0);
  await disclose(alice);await until('native receipt not execution claim',async()=>(await alice.locator('#receiver-status p[data-request="'+request.id+'"]').innerText()).includes('Accepted into native session; effects completion unconfirmed'));const status=await alice.locator('#receiver-status p[data-request="'+request.id+'"]').innerText();assert(!/handed over|completed through/i.test(status));await shot(alice,'codex-accepted-download',width);
  result.checks.push({width,receiver:selected.handle,target:B,physicalInputOnce:stats.physical,originalBody:true,verifiedReply:true,attachmentContext:true,requestFileHash:sha(bytes),downloadedHash:sha(fs.readFileSync(downloaded)),unselectedContinuation:0,defaultRuns:at('C').length,status});
 }
 assert.equal((await api(alice,'/api/responder')).dir,defaultBefore.dir);await Promise.all(reads);assert.deepEqual(result.errors,[]);assert.deepEqual(result.external,[]);result.pass=true;
})().catch(async e=>{result.failure={stage,error:e.stack||e.message};if(browser)for(const c of browser.contexts())for(const p of c.pages())try{const f=path.join(evidence,'first-failure-'+result.screenshots.length+'.png');await p.screenshot({path:f});result.screenshots.push(f);}catch{}process.exitCode=1;}).finally(async()=>{
 const failed=(label,e)=>{(result.cleanupErrors||=[]).push({label,error:e?.code||e?.name||'cleanup failure'});result.pass=false;process.exitCode=1;};
 try {if(nativeDriver){
  const root=path.join(world,'native-codex');result.ownedNativePIDs=[nativeDriver.pid,...children(nativeDriver.pid)];
  if(nativeDriver.exitCode===null){
   nativeDriver.stdin.on('error',e=>failed('native control pipe',e));
   nativeDriver.stdin.write(JSON.stringify({finish:true})+'\n');nativeDriver.stdin.end();
   await Promise.race([new Promise(r=>nativeDriver.once('exit',r)),idle(35000)]);
   if(nativeDriver.exitCode===null){failed('native driver timeout');nativeDriver.kill('SIGTERM');await idle(500);}
  }
  // Existing native driver owns [B,app-server,daemon,stop] under private env.
  // Never invoke a guessed second stop. No witness means no native launch.
  if(fs.existsSync(path.join(root,'evidence/pids.json'))){
   const stop=JSON.parse(fs.readFileSync(path.join(root,'evidence/daemon-stop.json'),'utf8'));result.nativeDaemonStopCode=stop.code;if(stop.code!==0)failed('private daemon stop');
  }
  for(let i=0;i<30&&result.ownedNativePIDs.some(p=>fs.existsSync('/proc/'+p));i++)await idle(100);result.remainingNativePIDs=result.ownedNativePIDs.filter(p=>fs.existsSync('/proc/'+p));if(result.remainingNativePIDs.length)failed('owned native survivor');
  const readSafe=(file,fallback)=>{try{return JSON.parse(fs.readFileSync(file,'utf8'));}catch{return fallback;}};
  let hooks=[];try{hooks=fs.readFileSync(path.join(root,'evidence/hooks.jsonl'),'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);}catch{}
  const safe={requests:readSafe(path.join(root,'evidence/requests.json'),[]),hookCounts:Object.fromEntries(['SessionStart','Stop','PostToolUse','SessionEnd'].map(n=>[n,hooks.filter(h=>h.hook_event_name===n).length]))};fs.writeFileSync(path.join(evidence,'native-safe.json'),JSON.stringify(safe,null,2),{mode:0o600});
 }}catch(e){failed('native cleanup',e);}finally{
  try{if(browser)await browser.close();}catch(e){failed('browser close',e);}
  for(let i=0;i<30&&(result.ownedBrowserPIDs||[]).some(p=>fs.existsSync('/proc/'+p));i++)await idle(100);result.remainingBrowserPIDs=(result.ownedBrowserPIDs||[]).filter(p=>fs.existsSync('/proc/'+p));if(result.remainingBrowserPIDs.length)failed('owned browser survivor');
  fs.writeFileSync(path.join(evidence,'result.json'),JSON.stringify(result,null,2),{mode:0o600});console.log(JSON.stringify({pass:result.pass,stage,checks:result.checks.length,screenshots:result.screenshots.length,errors:result.errors.length,external:result.external.length,remainingNative:result.remainingNativePIDs}));
 }
});`;
// Read receipt in place; persist only predicates/counts, never native text/claim token.
const receiptPython=String.raw`import json,sqlite3,sys
from pathlib import Path
home,file,binding,input_id,body,reply=sys.argv[1:]
db=sqlite3.connect('file:'+str(Path(home)/'agent.db')+'?mode=ro',uri=True)
claim=json.loads(db.execute('SELECT live_claim FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?',(binding,input_id)).fetchone()[0])
rows=[]
for line in Path(file).read_text().splitlines():
 v=json.loads(line);p=v.get('payload',{});item=p.get('item',{})
 if v.get('type')=='event_msg' and p.get('type')=='item_completed' and item.get('type')=='UserMessage' and claim['token'] in line:rows.append(line)
print(json.dumps({'physical':len(rows),'original':any(body in l for l in rows),'reply':any(reply in l for l in rows),'reference':any(input_id in l for l in rows),'claim':any(claim['id'] in l for l in rows)}))`;
let nativeDriver;
new Function('require','__dirname','nativeDriver','receiptPython',helpers+setup+journey)(require,__dirname,nativeDriver,receiptPython);
