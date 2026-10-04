// Reuse the qualified native-page world, browser guards and real Pi fixture.
// Only this closed-session acceptance seam is new; no production mock status.
const source=require('node:fs').readFileSync(require('node:path').join(__dirname,'receiver_ui_journey.cjs'),'utf8');
let shared=source.slice(0,source.indexOf('async function liveJourney'));
shared=shared.replace("const root=path.join(world,'native-'+harness)","const root=path.join(world,'native-'+harness+'-'+(++nativeSerial))")
 .replace("PATH:nodeRoot+'/bin:/usr/bin:/bin'","PATH:path.join(world,'bin')+':'+nodeRoot+'/bin:/usr/bin:/bin'")
 .replace("fs.writeFileSync(extension,cli('alice','hooks','show',harness),{mode:0o600});","fs.writeFileSync(extension,cli('alice','hooks','show',harness).split(JSON.stringify(binary)).join(JSON.stringify(ackWrapper)),{mode:0o600});");
shared=shared.replace('/completed|human|accepted|pending|queued/', '/completed|human|accepted|pending|queued|preauthorized|handed-over|held/');
const boot=source.slice(source.indexOf('(async()=>{'),source.indexOf(' // Second membership'));
const cleanup=source.slice(source.indexOf('})().catch(async e=>'));
const body=String.raw`
 // Hold only synthetic remote executor output until the native clean-close.
 const stub=path.join(world,'bin/codex'),gate=path.join(world,'remote-release');
 let stubText=fs.readFileSync(stub,'utf8');
 stubText=stubText.replace("if '-o' in sys.argv:","if os.getcwd().endswith('work-B'):\n import time\n end=time.monotonic()+30\n while not os.path.exists(os.environ['AGENTNET_HUMAN_WORLD']+'/remote-release'):\n  assert time.monotonic()<end,'fixture release deadline'\n  time.sleep(.025)\nif '-o' in sys.argv:");
 fs.writeFileSync(stub,stubText,{mode:0o700});
 // ACK-loss wrapper is the existing native claim uncertainty fixture mechanism.
 ackWrapper=path.join(world,'ack-forward.py');
 fs.writeFileSync(ackWrapper,'#!/usr/bin/python3\nimport os,sys,subprocess,json\na=sys.argv[1:];data=sys.stdin.buffer.read() if "hook" in a else None\nif data and os.path.exists('+JSON.stringify(path.join(world,'drop-ack'))+') and json.loads(data).get("receiver_action")=="ack":sys.exit(0)\nsys.exit(subprocess.run(['+JSON.stringify(binary)+']+a,input=data).returncode)\n',{mode:0o700});
 async function compose(native,backup,label,width){
  await alice.setViewportSize({width,height:width===390?844:900});await openRemote(alice);
  await alice.getByRole('radio',{name:'Question',exact:true}).check();await alice.getByRole('button',{name:'Refresh agents',exact:true}).click();await alice.locator('#device-agent-target').selectOption(B);
  await disclose(alice);await alice.getByRole('button',{name:'Refresh local receivers',exact:true}).click();await alice.locator('#receiver-picker').selectOption('session:'+native.handle);
  assert.equal(await alice.locator('#receiver-backup-picker').inputValue(),'','backup off by default');
  if(backup){await alice.locator('#receiver-backup-picker').selectOption(backup);assert.equal(await alice.locator('#receiver-backup-mode').inputValue(),'');await alice.locator('#receiver-backup-instructions').fill('EXPLICIT UI BACKUP '+label);await alice.locator('#receiver-backup-mode').selectOption('question');}
  const bytes=Buffer.from('EXACT UI BACKUP FILE '+label+'\n');await alice.locator('#body').fill(label);await alice.locator('#file-input').setInputFiles({name:'backup.txt',mimeType:'text/plain',buffer:bytes});
  await alice.locator('#body').fill('BEFORE NO-SWITCH CAPTURE '+label);await alice.evaluate(()=>workspaceCapture(wsNow(),wsAPI().state(wsNow())));await alice.locator('#body').fill(label);
  return bytes;
 }
 const binding=async label=>{const id=await alice.evaluate(label=>state.data?.messages.find(m=>m.body===label)?.id,label);return (await api(alice,'/api/reply-receivers')).find(r=>r.request_ref===id);};
 const release=()=>fs.writeFileSync(gate,'release',{mode:0o600});
 const hold=()=>{try{fs.unlinkSync(gate);}catch(e){if(e.code!=='ENOENT')throw e;}};
 const nativeCount=n=>n.events().filter(e=>e.event==='stream').length;
 async function sendHeld(label){const count=at('B').length;await alice.locator('#send').click();await until('real remote held '+label,async()=>at('B').length===count+1&&!!await binding(label));}
 // Exact draft survives inactive workspace and delayed catalog with no retarget.
 stage='second workspace and backup draft';await alice.locator('#workspace-shell button').filter({hasText:'Join a workspace'}).click();await alice.locator('.workspace-join input[placeholder="e.g. Acme"]').fill('Isolated B workspace');await alice.locator('.workspace-join textarea').fill(fs.readFileSync(path.join(world,'invite-b'),'utf8').trim());await alice.locator('.workspace-join input[placeholder="laptop"]').fill('second');await alice.locator('.workspace-join .btn.primary').click();await alice.waitForFunction(()=>window.agentnet.workspaces.list().length===2);const wsB=await alice.evaluate(()=>window.agentnet.workspaces.list().find(w=>w.id!=='default').id);await selectWorkspace(alice,'default');
 let completed=0;
 for(const width of [390,1280]){
  stage='clean-close UI '+width;hold();const native=await launchNative('pi'),label='CLEAN CLOSE UI ORIGINAL '+width;const bytes=await compose(native,A,label,width);
  {
   const intent=await alice.evaluate(()=>JSON.stringify(state.replyReceiver)),file=await alice.evaluate(()=>{window.handoffFixtureFile=state.files[0].file;return state.files[0].name;});
   const draftEvidence=async where=>{(result.draftChecks||=[]).push({where,...await alice.evaluate(()=>({key:state.draftKey,text:document.getElementById('body').value,saved:state.drafts[state.draftKey]?.text,captured:state.capturedFor}))});};
   await draftEvidence('before switch');await selectWorkspace(alice,wsB);assert.equal(await alice.evaluate(()=>state.replyReceiver),null);await selectWorkspace(alice,'default');await draftEvidence('after restore');if(!(await alice.evaluate(()=>state.data?.peer==='bob/host')))await openRemote(alice);await until('exact inactive backup restored',async()=>await alice.evaluate(()=>JSON.stringify(state.replyReceiver))===intent);await draftEvidence('before assertion');
   assert.equal(await alice.evaluate(()=>state.files[0].name),file);assert.equal(await alice.locator('#body').inputValue(),label);assert.equal(await alice.evaluate(()=>kindValue()),'question');assert.equal(await alice.evaluate(()=>state.deviceAgentID),B);assert(await alice.evaluate(()=>state.files[0].file===window.handoffFixtureFile));result.checks.push('Inactive workspace '+width+' restores text/kind/exact native handle/backup/mode/instructions/remote B/original File; B inherits none');
  }
  await shot(alice,'backup-selected-'+width,width);
  await disclose(alice);
  for(const [control,selector] of [['instructions','#receiver-backup-instructions'],['mode','#receiver-backup-mode']]){
   await alice.locator(selector).scrollIntoViewIfNeeded();await recipientBounds(alice,'backup-'+control,width);const file=path.join(evidence,'backup-'+control+'-'+width+'.png');await alice.screenshot({path:file});result.screenshots.push(file);assert.equal(await alice.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  }
  await alice.locator('#receiver-summary').click();
  if(width===1280){
   let paused,started;const begun=new Promise(r=>started=r),pattern=new URL(ui('alice')).origin+'/workspaces/default/*/api/agents';
   await alice.context().route(pattern,r=>{paused=r;started();});const before=at('B').length;await alice.locator('#send').click();await begun;await selectWorkspace(alice,wsB);assert.equal(await alice.evaluate(()=>state.replyReceiver),null);await paused.continue();await alice.context().unroute(pattern);await until('captured backup sent',()=>at('B').length===before+1);await selectWorkspace(alice,'default');await openRemote(alice);await until('captured backup binding',async()=>!!await binding(label));result.checks.push('Delayed catalog send remains in captured workspace/remote B/backup A with original instructions');
  }else await sendHeld(label);
  let row=await binding(label);assert.equal(row.handoff_state,'preauthorized');assert.equal(row.receiver.on_close.agent_id,A);assert.equal(nativeCount(native),0);await shot(alice,'backup-preauthorized-'+width,width);
  await stopNative(native);await until('native clean close handover',async()=>{row=await binding(label);return row?.handoff_state==='handed_over'&&row.receiver.agent_id===A;});release();
  await until('selected frozen backup once',()=>at('A').length===completed+1);completed++;
  await until('completed handover',async()=>{row=await binding(label);return row?.state==='completed'&&row.inputs?.length===1&&row.inputs[0].state==='completed';});await idle(400);
  assert.equal(at('A').length,completed);assert.equal(nativeCount(native),0);assert.equal(at('C').length,0);const run=at('A').at(-1);assert(run.prompt.includes('EXPLICIT UI BACKUP '+label)&&run.prompt.includes(label)&&run.prompt.includes('SYNTHETIC REMOTE VERIFIED ANSWER'));assert.deepEqual(run.files.find(f=>f.name==='backup.txt').bytes,[...bytes]);
  const thread=await alice.evaluate(()=>state.data),request=thread.messages.find(m=>m.body===label);assert.equal(request.target.agent_id,B);const remote=await api(bob,'/api/thread?id='+request.id),copy=remote.messages.find(m=>m.id===request.id);assert.equal(copy.target.agent_id,B);assert.deepEqual(await bob.evaluate(async m=>Array.from((await window.agentnet.file(m.id,0,m.dir)).bytes),copy),[...bytes]);
  await disclose(alice);await until('visible native handover truth',async()=>(await alice.locator('#receiver-status p[data-request="'+row.request_ref+'"]').innerText()).includes('Handed over to selected backup; effects completion unconfirmed'));await shot(alice,'backup-handed-over-'+width,width);result.checks.push('Pi clean close '+width+': remote B/file bytes exact, frozen A one question run with original instructions and reply; native0/default0; truthful preauthorized/handed_over');
 }
 stage='no opt-in stays pending';hold();const noBackup=await launchNative('pi'),noLabel='NO BACKUP UI ORIGINAL';await compose(noBackup,null,noLabel,390);await sendHeld(noLabel);await stopNative(noBackup);release();await until('unselected return pending',async()=>{const row=await binding(noLabel);return row?.state==='pending'&&row.inputs?.length===1;});assert.equal(at('A').length,completed);assert.equal(nativeCount(noBackup),0);assert.equal(at('C').length,0);await shot(alice,'backup-off-pending',390);result.checks.push('No opt-in: genuine clean close + verified later reply remains pending; no A/default/native work');
 for(const action of ['disable','update']){
  stage='refused '+action+' backup';hold();await alice.setViewportSize({width:1280,height:900});const id=await create(alice,'Backup '+action,'work-A'),native=await launchNative('pi'),label='HELD BACKUP '+action;await compose(native,id,label,1280);await sendHeld(label);
  await api(alice,'/api/agents',{action,id,...(action==='update'?{harness:'codex',dir:path.join(world,'work-C')}:{})});await stopNative(native);release();await until('held native refusal '+action,async()=>{const r=await binding(label);return r?.handoff_state==='held'&&r.receiver.kind==='live_session'&&r.inputs?.length===1;});
  assert.equal(at('A').length,completed);assert.equal(at('C').length,0);assert.equal(nativeCount(native),0);await shot(alice,'backup-'+action+'-held',1280);result.checks.push('Native '+action+' config refusal: observed closure stays held on exact origin/backup; no fallback');
 }
 stage='uncertain native claim refused';hold();fs.writeFileSync(path.join(world,'drop-ack'),'drop',{mode:0o600});const uncertain=await launchNative('pi'),uncertainLabel='UNCERTAIN CLAIM UI ORIGINAL';await compose(uncertain,A,uncertainLabel,390);await sendHeld(uncertainLabel);release();await until('real native input once ACK lost',()=>nativeCount(uncertain)===1&&uncertain.events().some(e=>e.event==='end'&&e.inputs?.length));await stopNative(uncertain);await until('uncertain native claim held',async()=>{const row=await binding(uncertainLabel);return row?.handoff_state==='held'&&row.detail.includes('accepted, claimed or uncertain');});assert.equal(at('A').length,completed);assert.equal(at('C').length,0);assert.equal(nativeCount(uncertain),1);await shot(alice,'backup-uncertain-held',390);result.checks.push('Real native intake with dropped ACK then clean close: claim uncertainty held, no replay to backup/default');
 stage='reload is observational';await alice.reload();await ready(alice);await openRemote(alice);await idle(500);assert.equal(at('A').length,completed);assert.equal(at('C').length,0);assert.equal(nativeCount(uncertain),1);assert.equal((await api(alice,'/api/responder')).dir,defaultBefore.dir);result.checks.push('Read/reload causes no execution, no polling introduced, default unchanged');
 result.native=natives.map(n=>({handle:n.handle,streams:nativeCount(n),externalRequests:n.events().filter(e=>e.event==='NETWORK_PROHIBITED').length}));assert(natives.every(n=>!n.events().some(e=>e.event==='NETWORK_PROHIBITED')));await Promise.all(reads);assert.deepEqual(result.errors,[]);assert.deepEqual(result.external,[]);result.pass=true;
`;
// Inline evaluation retains the same helper lexical scope; no production eval.
eval(shared+'\nlet ackWrapper,nativeSerial=0;\n'+boot+body+cleanup);
