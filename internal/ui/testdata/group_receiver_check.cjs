// Exact production group receiver functions; no authority or executor stand-in.
'use strict';
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync(path.join(__dirname, '../static/app.js'), 'utf8');
const region = (first, next) => {
  const a = source.indexOf(first), b = source.indexOf(next, a);
  assert(a >= 0 && b > a, first + ' production seam'); return source.slice(a, b);
};
const id = 'a'.repeat(32), address = 'synthetic/laptop';
const managed = () => ({kind:'managed_agent', agent_id:id, label:'Local Receiver', host:address, workspace:'A', instructions:'Original continuation', mode:'question'});
const live = () => ({kind:'live_session', session_handle:'exact-OMP', label:'Local OMP', harness:'omp', host:address, workspace:'A'});
const plain = v => JSON.parse(JSON.stringify(v));
function fixture() {
  const elements = {}, calls = [], staged = [], cleaned = [];
  const elem = () => ({hidden:false, textContent:'', value:'', setAttribute(){}, addEventListener(){}, append(){}, replaceChildren(){}});
  const state = {dm:'group', dmData:{id:'group',kind:'group',role:'member',peer:{label:'Group'},agents:[{pid:'exact-PID',can_ask:true}]},draftKey:'dm:group',files:[],drafts:{},overview:{reply_receivers:true,reply_sessions:true,me:{address}},replyReceiver:null,dmReply:null};
  const host = {stage:async f => {staged.push(f);return 'exact-file';}};
  let workspace = 'A', waitCatalog = null;
  const context = vm.createContext({state, window:{agentnet:host}, document:{activeElement:null,createElement:elem,createTextNode:v=>v}, URL:{revokeObjectURL(){}},
    $:n => elements[n] ||= elem(), typingUI:null,
    wsNow:()=>workspace, humanGroup:()=>true, dmVisitor:d=>d.role==='visitor', agentOf:pid=>state.dmData.agents.find(a=>a.pid===pid),
    askGone:()=>!!state.dmAgent&&!state.dmData.agents.find(a=>a.pid===state.dmAgent)?.can_ask,
    trackMentions(){}, encodeMentions:t=>t, validMentions:()=>[], boundElsewhere:()=>'', overLimit:()=>'', kindValue:()=> 'task', keepDraft(){}, syncComposer(){}, kindHint(){}, grow(){}, announce(){}, updated(){}, sentElsewhere:ws=>cleaned.push(ws), draftsOf:()=>state.drafts,
    setDMReply:r=>state.dmReply=r, dropFiles:files=>{state.files=state.files.filter(f=>!files.includes(f));}, renderReceiverStatus(){}, loadReceiverCatalog(){throw Error('unexpected catalog load');},
    api:async (url,body,transport) => {
      calls.push({url,body:plain(body ?? null),transport});
      if (url === '/api/agents') {if(waitCatalog)await waitCatalog; return {local:true,host:address,agents:[{enabled:true,record:{id,host:address},responder:{ready:true}}]};}
      if (url === '/api/reply-sessions') return {local:true,host:address,sessions:state.receiverCatalog?.sessions||[{handle:'exact-OMP',harness:'omp',label:'Local OMP',active:true}]};
      return {state:'queued'};
    }
  });
  vm.runInContext(region('const present =', '// host is') + region('const dmHumanGuest =', "// A guest's audience") + region('async function preparedFiles(', '// discardStaged') + region('async function localReceiverCatalog(', 'async function loadReceiverCatalog(') + region('function chooseReplyBackup(', 'function renderReceiverStatus(') + region('async function sendDM()', 'let rerenderContacts'), context);
  elements.body={...elem(),value:'Exact group text'};
  return {context,state,host,elements,calls,staged,cleaned,setWorkspace:v=>workspace=v,hold:promise=>waitCatalog=promise};
}
(async()=>{
  for (const receiver of [null,managed(),live()]) for(const pid of [null,'exact-PID']) {
    const f=fixture(); f.state.replyReceiver=receiver; f.state.dmAgent=pid;f.state.files=[{name:'group.txt',file:'exact bytes'}];f.state.dmReply={id:'exact-parent'};
    await f.context.sendDM(); const send=f.calls.find(c=>c.url.endsWith(pid?'agent/ask':'dm/send'));
    assert(send,'group ordinary/PID sends'); assert.equal(send.transport,f.host);assert.deepEqual(send.body.files,['exact-file']);assert.equal(send.body.body,'Exact group text');
    if(pid){assert.equal(send.body.pid,pid);assert.equal(send.body.kind,'task');}else{assert.equal(send.body.conv,'group');assert.equal(send.body.reply_to,'exact-parent');}
    assert.deepEqual(send.body.reply_receiver, receiver?.kind==='managed_agent'?{kind:receiver.kind,agent_id:id,instructions:receiver.instructions,mode:receiver.mode}:receiver?{kind:receiver.kind,session_handle:receiver.session_handle}:{kind:'human'});
    assert.equal(f.state.files.length,0);assert.equal(f.elements.body.value,'');
  }
  for(const receiver of [managed(),live()]) {
    const f=fixture();f.state.replyReceiver=receiver;f.state.overview.reply_receivers=false;f.state.files=[{name:'kept.txt',file:'kept bytes'}];
    await f.context.sendDM();assert.equal(f.calls.length,0);assert.equal(f.staged.length,0);assert.equal(f.elements.body.value,'Exact group text');assert.equal(f.state.files.length,1);assert.equal(f.state.replyReceiver,receiver);assert.match(f.elements['compose-error'].textContent,/unavailable in this workspace/);
  }
  // Claude still receives input; detached SessionEnd cannot authorize backup.
  for (const harness of ['claude','omp','pi','codex']) {
    const f=fixture(),receiver={...live(),session_handle:'exact-'+harness,harness,on_close:{agent_id:id,label:'Backup',instructions:'Keep original backup',mode:'question',configuration:JSON.stringify({ready:true})}};
    f.state.replyReceiver=receiver;f.state.receiverCatalog={host:address,sessions:[{handle:receiver.session_handle,harness,label:harness,active:true}],agents:[]};
    f.state.files=[{name:'kept.txt',file:'exact kept bytes'}];
    await f.context.sendDM();
    if(harness==='claude') {
      assert.equal(f.staged.length,0,'Claude backup refused before staging');assert(!f.calls.some(c=>c.url==='/api/dm/send'));
      assert.equal(f.state.replyReceiver,receiver);assert.equal(f.elements.body.value,'Exact group text');assert.equal(f.state.files.length,1);assert.match(f.elements['compose-error'].textContent,/Automatic backup after Claude closes is unavailable/g);
      const original=plain(receiver);f.context.chooseReplyBackup(id);assert.deepEqual(plain(f.state.replyReceiver),original,'no new Claude backup choice');f.context.chooseReplyBackup('');assert.equal(f.state.replyReceiver.on_close,undefined);assert.equal(f.state.replyReceiver.session_handle,receiver.session_handle);assert.equal(f.state.files.length,1);assert.equal(f.elements['compose-error'].textContent,'','explicit removal clears only obsolete backup refusal');await f.context.sendDM();assert.deepEqual(f.calls.find(c=>c.url==='/api/dm/send').body.reply_receiver,{kind:'live_session',session_handle:receiver.session_handle});
    } else assert.equal(f.calls.find(c=>c.url==='/api/dm/send').body.reply_receiver.on_close.agent_id,id,'supported backup unchanged');
  }
  const unrelated=fixture();unrelated.state.replyReceiver={...live(),harness:'claude',on_close:{agent_id:id}};unrelated.state.receiverCatalog={host:address,agents:[],sessions:[]};unrelated.elements['compose-error']={textContent:'Unrelated upload failed'};unrelated.context.chooseReplyBackup('');assert.equal(unrelated.elements['compose-error'].textContent,'Unrelated upload failed','deliberate removal preserves unrelated error');
  const spoof=fixture();spoof.state.replyReceiver={...live(),on_close:{agent_id:id,instructions:'Keep',mode:'question',configuration:JSON.stringify({ready:true})}};spoof.state.receiverCatalog={sessions:[{handle:'exact-OMP',harness:'claude',label:'Claude',active:true}]};spoof.state.files=[{name:'retained',file:'exact bytes'}];await spoof.context.sendDM();assert.equal(spoof.staged.length,0);assert.match(spoof.elements['compose-error'].textContent,/with that harness/);
  const remote=fixture();await assert.rejects(remote.context.prepareReplyReceiverSelection({...live(),harness:'claude',on_close:{agent_id:id}},remote.host,'A',false,false,{host:{address:'remote/exact'}}),/Automatic backup after Claude closes is unavailable/g);assert.equal(remote.calls.length,0,'remote retained Claude backup refuses before catalog/staging');
  for (const view of ['dm','ordinary-thread']) {
    const f=fixture();f.state.draftKey=null;f.state.replyReceiverHost=null;f.state.receiverCatalogSeq=0;
    if(view==='ordinary-thread'){f.state.dm=null;f.state.dmData=null;f.state.data={peer:'bob/desk'};}
    // Install the production loader instead of this fixture's no-load stub.
    vm.runInContext(region('async function loadReceiverCatalog(', 'function chooseReplyReceiver('),f.context);
    f.context.renderReplyReceiver();assert.equal(f.calls.length,0,'null draft cannot start catalog');assert.equal(f.state.receiverCatalog,undefined);
    f.state.draftKey=view==='dm'?'dm:group':'thread:exact';f.context.renderReplyReceiver();await new Promise(r=>setImmediate(r));
    assert.equal(f.state.receiverCatalog.loading,undefined,'final-key catalog settles');assert.equal(f.state.receiverCatalog.agents.length,1);assert.equal(f.calls.length,2,'one agents/sessions load');f.context.renderReplyReceiver();assert.equal(f.calls.length,2,'settled catalog not reloaded');
  }
  const browser=fixture();browser.state.overview.reply_receivers=false;await browser.context.sendDM();assert(!('reply_receiver' in browser.calls[0].body),'browser human has no invented native selection');
  for(const blocked of ['frozen','visitor','missing-target']) {
    const f=fixture();f.state.replyReceiver=managed();
    if(blocked==='frozen')f.state.dmData.frozen='Removed';else if(blocked==='visitor')f.state.dmData.role='visitor';else f.state.dmAgent='absent-PID';
    await f.context.sendDM();assert.equal(f.calls.length,0);assert.equal(f.staged.length,0);assert.equal(f.elements.body.value,'Exact group text');
  }
  const delayed=fixture();let release;delayed.hold(new Promise(r=>release=r));delayed.state.replyReceiver=managed();delayed.state.files=[{name:'A.txt',file:'A bytes'}];
  const pending=delayed.context.sendDM();await new Promise(r=>setImmediate(r));delayed.setWorkspace('B');delayed.context.window.agentnet={stage(){throw Error('retargeted');}};delayed.state.draftKey='dm:B';delayed.elements.body.value='B draft';release();await pending;
  const send=delayed.calls.find(c=>c.url==='/api/dm/send');assert.equal(send.transport,delayed.host);assert.equal(send.body.reply_receiver.agent_id,id);assert.equal(send.body.body,'Exact group text');assert.equal(delayed.elements.body.value,'B draft');assert.deepEqual(delayed.cleaned,['A']);
  for(const native of [true,false]) {
    const f=fixture();f.state.overview.reply_receivers=native;f.state.receiverCatalog={host:address,agents:[],sessions:[]};f.context.renderReplyReceiver();assert.equal(f.elements['reply-receiver'].hidden,false);assert.equal(f.elements['receiver-options'].hidden,false);assert.match(f.elements['receiver-summary'].textContent,/Me \(human\)/);
  }
  console.log('group receiver production functions ok');
})().catch(error=>{console.error(error);process.exitCode=1;});
