// Actual production receiver UI functions, synthetic provider; no execution claim.
'use strict';
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync(path.join(__dirname,'../static/app.js'),'utf8');
const region=(a,b)=>{const i=source.indexOf(a),j=source.indexOf(b,i);assert(i>=0&&j>i,a);return source.slice(i,j);};
const plain=v=>JSON.parse(JSON.stringify(v));
const origin='alice/phone',address='alice/laptop',fingerprint='11111111-22222222-33333333-44444444',id='a'.repeat(32);
const choice={address,fingerprint},record={id,host:address,label:'Selected Receiver'};
const person=()=>({person:'b'.repeat(32),state:'self',devices:[{address:origin,fingerprint:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'},choice]});
const selected=()=>({kind:'managed_agent',agent_id:id,label:record.label,host:address,fingerprint,workspace:'A',instructions:'Original continuation',mode:'question'});
const live=()=>({kind:'live_session',session_handle:'exact-OMP',harness:'omp',label:'Selected OMP',host:address,fingerprint,workspace:'A'});
function fixture() {
 const elements={},calls=[],staged=[],announced=[],cleaned=[];
 const elem=()=>({hidden:false,textContent:'',value:'',children:[],setAttribute(){},addEventListener(){},append(...v){this.children.push(...v);},replaceChildren(...v){this.children=v;},focus(){},setSelectionRange(){}});
 const state={gen:1,receiverCatalogSeq:0,dm:'group',dmData:{id:'group',kind:'group',role:'member',peer:{label:'Bob'},agents:[{pid:'exact-PID',can_ask:true}],messages:[]},draftKey:'dm:group',drafts:{},files:[],replyReceiverHost:{...choice},replyReceiver:null,overview:{me:{address:origin},person:person(),reply_receivers:false,reply_sessions:false},dmReply:null};
 let workspace='A',snapshot={host:address,host_key:fingerprint,local:false,status:'ready',sessions:[{handle:'exact-OMP',harness:'omp',label:'Selected OMP',active:true},{handle:'held-Claude',harness:'claude',label:'Gated Claude',active:true}]},fresh={me:{address:origin},person:person()},publicRecord=record,hold=null;
 const host={stage:async file=>{staged.push(file);return 'exact-staged';}};
 const context=vm.createContext({state,window:{agentnet:host},document:{activeElement:null,createElement:elem,createTextNode:v=>v},URL:{revokeObjectURL(){}},typingUI:null,$:n=>elements[n]||=elem(),
 wsNow:()=>workspace,humanGroup:()=>state.dmData?.kind==='group',dmVisitor:d=>d.role==='visitor',agentOf:pid=>state.dmData.agents.find(a=>a.pid===pid),askGone:()=>!!state.dmAgent&&!state.dmData.agents.find(a=>a.pid===state.dmAgent)?.can_ask,
 trackMentions(){}, encodeMentions:t=>t, validMentions:()=>[], boundElsewhere:()=>'',overLimit:()=>'',kindValue:()=> 'task',syncComposer(){},kindHint(){},grow(){},announce:v=>announced.push(v),updated(){},sentElsewhere:ws=>cleaned.push(ws),draftsOf:()=>state.drafts,
 setDMReply:r=>state.dmReply=r,setDMAgent:a=>state.dmAgent=a?.pid||null,setAnswering:a=>state.answering=a,setKind(){},renderPending(){},deviceAgentMissing:()=>false,
 dropFiles:files=>state.files=state.files.filter(f=>!files.includes(f)),firstLine:v=>v,el:(tag,attrs,...children)=>({...elem(),tag,attrs,children}),fill:(e,...v)=>e.replaceChildren(...v),
 api:async(url,body,transport)=>{calls.push({url,body:plain(body??null),transport});
 if(url.startsWith('/api/agents?')){if(hold)await hold;return {host:address,agents:[{record:publicRecord,enabled:true}]};}
 if(url.startsWith('/api/reply-sessions?'))return snapshot;
 if(url==='/api/overview')return fresh;
 return {state:'receiver_waiting'};
 }});
 vm.runInContext(region('const present =','// host is')+region('const dmHumanGuest =',"// A guest's audience")+region('async function preparedFiles(','// discardStaged')+region('async function readAgentCatalog(','const isMe =')+region('async function localReceiverCatalog(','// renderTarget says')+region('function keepDraft()','// syncComposer enables')+region('async function sendDM()','let rerenderContacts')+region('async function send(ev)','function kindHint()'),context);
 elements.body={...elem(),value:'Exact captured text'};
 return {context,state,host,calls,staged,elements,announced,cleaned,snapshot:v=>snapshot=v,fresh:v=>fresh=v,record:v=>publicRecord=v,workspace:v=>workspace=v,hold:v=>hold=v};
}
(async()=>{
 // Browser local flags remain false; only explicit current-own remote selection enables delegation.
 for(const receiver of [null,selected(),live()])for(const shape of ['direct','dm','group','PID']){
  const f=fixture();f.state.replyReceiver=receiver;f.state.files=[{name:'exact.txt',file:'EXACT BYTES'}];f.state.dmReply={id:'parent'};
  if(shape==='direct'){f.state.dm=null;f.state.data={key:{},peer:'bob/desk',messages:[]};}else if(shape==='dm')f.state.dmData.kind='dm';else if(shape==='PID')f.state.dmAgent='exact-PID';
  if(shape==='direct')await f.context.send({preventDefault(){}});else await f.context.sendDM();
  const sent=f.calls.find(c=>['/api/send','/api/dm/send','/api/dm/agent/ask'].includes(c.url));assert(sent,shape+' sent');assert.equal(sent.transport,f.host);assert.equal(sent.body.body,'Exact captured text');assert.deepEqual(sent.body.files,['exact-staged']);
  const expected=receiver?.kind==='managed_agent'?{kind:receiver.kind,agent_id:id,instructions:receiver.instructions,mode:receiver.mode,host:choice}:receiver?{kind:receiver.kind,session_handle:receiver.session_handle,host:choice}:{kind:'human',host:choice};
  assert.deepEqual(sent.body.reply_receiver,expected);assert.equal(f.elements.body.value,'');assert.equal(f.state.files.length,0);assert.match(f.announced[0],/selected reply host.*accept/);
 }
 // Known changed/missing roster identity and unavailable session fail BEFORE any file staging.
 for(const bad of ['foreign-host','wrong-key','origin-removed','changed-person','fresh-removed','pending','unavailable','wrong-catalog-key','missing-session','retained-other-host']){
  const f=fixture();f.state.replyReceiver=['pending','unavailable','wrong-catalog-key','missing-session'].includes(bad)?live():selected();f.state.files=[{name:'kept.txt',file:'KEPT'}];
  if(bad==='foreign-host')f.state.replyReceiverHost={address:'mallory/desk',fingerprint};
  if(bad==='wrong-key')f.state.replyReceiverHost={address,fingerprint:'ffffffff-ffffffff-ffffffff-ffffffff'};
  if(bad==='origin-removed')f.state.overview.person.devices=[choice];
  if(bad==='changed-person')f.fresh({me:{address:origin},person:{...person(),person:'different'}});
  if(bad==='fresh-removed')f.fresh({me:{address:origin},person:{...person(),devices:[person().devices[0]]}});
  if(['pending','unavailable'].includes(bad))f.snapshot({host:address,host_key:fingerprint,local:false,status:bad,sessions:[]});
  if(bad==='wrong-catalog-key')f.snapshot({host:address,host_key:'wrong',local:false,status:'ready',sessions:[]});
  if(bad==='missing-session')f.snapshot({host:address,host_key:fingerprint,local:false,status:'ready',sessions:[]});
  if(bad==='retained-other-host')f.state.replyReceiver={...selected(),host:'alice/old'};
  await f.context.sendDM();assert.equal(f.staged.length,0,bad);assert(!f.calls.some(c=>c.url==='/api/dm/send'),bad);assert.equal(f.state.files.length,1);assert.equal(f.elements.body.value,'Exact captured text');assert(f.elements['compose-error'].textContent,bad);
 }
 const gated=fixture();const snapshot=await gated.context.remoteReplySessions(gated.host,choice);assert.deepEqual(plain(snapshot.sessions.map(s=>s.harness)),['omp'],'Claude not enabled');
 const backup=fixture();backup.state.replyReceiver={...live(),on_close:{agent_id:id,instructions:'Original backup',mode:'task',configuration:JSON.stringify(record)}};
 assert.deepEqual(plain((await backup.context.prepareReplyReceiverSelection(backup.state.replyReceiver,backup.host,'A',false,false,backup.context.receiverCapture())).on_close),{agent_id:id,instructions:'Original backup',mode:'task'});
 backup.record({...record,label:'Changed'});await assert.rejects(backup.context.prepareReplyReceiverSelection(backup.state.replyReceiver,backup.host,'A',false,false,backup.context.receiverCapture()),/public record changed/);
 // A late catalog cannot leak into another draft/workspace/provider.
 const late=fixture();let release;late.hold(new Promise(r=>release=r));const loading=late.context.loadReceiverCatalog();await new Promise(r=>setImmediate(r));late.workspace('B');late.state.gen++;late.state.draftKey='dm:B';late.context.window.agentnet={};late.state.receiverCatalog={host:'B',agents:[],sessions:[]};release();await loading;assert.equal(late.state.receiverCatalog.host,'B');
 // Already-started A send stays bound to captured A host, files, target and text.
 const send=fixture();let resume;send.hold(new Promise(r=>resume=r));send.state.replyReceiver=selected();send.state.files=[{name:'A.txt',file:'A BYTES'}];send.state.dmAgent='exact-PID';const sending=send.context.sendDM();await new Promise(r=>setImmediate(r));send.workspace('B');send.state.draftKey='dm:B';send.context.window.agentnet={stage(){throw Error('retargeted');}};send.elements.body.value='B draft';resume();await sending;const sent=send.calls.find(c=>c.url==='/api/dm/agent/ask');assert.equal(sent.transport,send.host);assert.equal(sent.body.pid,'exact-PID');assert.deepEqual(sent.body.reply_receiver.host,choice);assert.equal(send.elements.body.value,'B draft');assert.deepEqual(send.cleaned,['A']);
 // A late refused group send has no DM peer field and cannot throw while reporting A's kept draft.
 const refused=fixture();let unlock;refused.hold(new Promise(r=>unlock=r));refused.state.replyReceiver=selected();refused.state.dmData={...refused.state.dmData,title:'A group',peer:undefined};refused.record({...record,id:'c'.repeat(32)});const rejected=refused.context.sendDM();await new Promise(r=>setImmediate(r));refused.workspace('B');refused.state.draftKey='dm:B';refused.elements.body.value='B untouched';unlock();await rejected;assert.equal(refused.elements.body.value,'B untouched');assert.match(refused.announced[0],/Not sent to A group.*no default/);assert.equal(refused.staged.length,0);
 // Provider-reported waiting state is never described as already sent or local execution.
 const status=fixture();status.state.dmData.messages=[{id:'original',dir:'out',body:'Question',state:'receiver_waiting'}];status.state.receiverBindings=[{conv:'group',request_ref:'original',host:address,label:'Selected',receiver:{kind:'managed_agent',host:choice},state:'pending'}];status.context.renderReceiverStatus();assert.match(JSON.stringify(status.elements['receiver-status'].children),/Awaiting selected-host approval and Ready; original request not sent/);
 // Changing host preserves old selection; explicit Me on remote survives draft restoration.
 const draft=fixture();draft.state.replyReceiver=selected();draft.context.chooseReplyHost(origin);assert.equal(draft.state.replyReceiver.host,address);assert.equal(draft.state.replyReceiverHost,null);await assert.rejects(draft.context.prepareReplyReceiverSelection(draft.state.replyReceiver,draft.host,'A',false,false,draft.context.receiverCapture()),/unavailable/);
 draft.state.replyReceiver=null;draft.context.chooseReplyHost(address);draft.elements.body.value='';draft.context.keepDraft();assert.deepEqual(plain(draft.state.drafts['dm:group'].reply_receiver_host),choice);draft.state.replyReceiverHost=null;draft.context.restoreDraft('dm:group',{messages:[],agents:[]});assert.deepEqual(plain(draft.state.replyReceiverHost),choice);
 console.log('remote receiver production UI functions ok');
})().catch(e=>{console.error(e);process.exitCode=1;});
