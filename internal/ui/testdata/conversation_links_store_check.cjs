// Run against an esbuild bundle of the actual Comic Store, not a copied model.
const assert=require('node:assert/strict');
const {File}=require('node:buffer');
global.localStorage={getItem:()=>null,setItem:()=>{}};
global.document={visibilityState:'visible',hasFocus:()=>false};
const {Store}=require(process.argv[2]);
const conv='b'.repeat(64),topic='c'.repeat(32),root='d'.repeat(32),saved={};
let view={id:conv,role:'member',topics:[{id:topic}],messages:[]};
const calls=[],host={workspace:{id:'default',handle:'a'.repeat(32)},platform:'daemon',workspaces:{state:()=>saved},listen:()=>()=>{},api:async(path,body)=>{calls.push({path,body});return view;}};
(async()=>{
 const s=new Store(host),bytes=new File(['unsent attachment'],'draft.txt'),token={id:'owned-staged-token'},file={key:'f1',name:'draft.txt',file:bytes,staged:token},draft={text:'unsent',agent:'chosen-agent',replyTo:'e'.repeat(32),files:[file],topic};
 s.setDraft(conv,draft);
 await s.openLinkTarget(conv,'topic',{topic:'main'});
 assert.equal(s.get().open.id,conv);assert.deepEqual(s.draft(conv),{...draft,topic:'',newTopic:false});
 assert.equal(s.draft(conv).files[0],file);
 await s.openLinkTarget(conv,'topic',{topic});assert.equal(s.draft(conv).topic,topic);
 let before=s.get();view={...view,topics:[]};
 await assert.rejects(s.openLinkTarget(conv,'topic',{topic}),/not available/);
 assert.equal(s.get(),before,'failed landing leaves every state value intact');
 for(const role of ['visitor','human_guest']){
  view={id:conv,role,frozen:'',topics:[{id:topic}],messages:[]};
  await s.openLinkTarget(conv,'topic',{topic});
  before=s.get();const kept=before.drafts[conv];
  await assert.rejects(s.openLinkTarget(conv,'topic',{topic:'main'}),/not available/);
  assert.equal(s.get(),before,role+': Main refusal preserves exact state');
  assert.equal(s.get().open,before.open);assert.equal(s.get().views[conv],before.views[conv]);
  assert.equal(s.draft(conv),kept);assert.equal(kept.text,draft.text);assert.equal(kept.topic,topic);
  assert.equal(kept.agent,draft.agent);assert.equal(kept.replyTo,draft.replyTo);
  assert.equal(kept.files[0],file);assert.equal(kept.files[0].file,bytes);assert.equal(kept.files[0].staged,token);
  view={...view,main_topic:{id:'main',state:'active'}};
  await s.openLinkTarget(conv,'topic',{topic:'main'});
  assert.equal(s.draft(conv).topic,'');assert.equal(s.draft(conv).text,draft.text);
  assert.equal(s.draft(conv).agent,draft.agent);assert.equal(s.draft(conv).replyTo,draft.replyTo);
  assert.equal(s.draft(conv).files[0].file,bytes);assert.equal(s.draft(conv).files[0].staged,token);
 }
 before=s.get();
 view={id:root,peer:'bob/desk',topic:{id:root},messages:[{id:root,deleted:true}]};
 await assert.rejects(s.openLinkTarget(root,'thread'),/root/);assert.equal(s.get(),before);
 view={...view,messages:[{id:root}]};await s.openLinkTarget(root,'thread');assert.equal(s.get().open.id,root);
 assert(calls.every(c=>c.body===undefined&&/^\/api\/(dm|thread)\?id=/.test(c.path)),'no send, approval, or history refresh');
 const open=s.get().open;s.stop();const back=new Store(host);
 assert.equal(back.draft(conv).files[0],file,'same binding remount retains staged file');assert.equal(back.draft(conv).agent,draft.agent);assert.equal(back.get().open,open);
 const rebound=new Store({...host,workspace:{...host.workspace,handle:'f'.repeat(32)}});
 assert.equal(rebound.draft(conv).files,undefined,'retired binding never reuses staged files');assert.equal(rebound.get().open,null);
 back.stop();rebound.stop();

 // A viewed device topic acknowledges exact outgoing and incoming IDs once.
 const ids={topic:'1'.repeat(32),question:'2'.repeat(32),answer:'3'.repeat(32),sibling:'4'.repeat(32)};
 const reads=[];let failRead=false;
 const readHost={...host,workspaces:undefined,api:async(path,body)=>{
   assert.equal(path,'/api/act');assert.equal(body.do,'read');reads.push(body.ids);
   if(failRead)throw Error('synthetic read failure');return {};
 }};
 global.document.hasFocus=()=>true;
 const reading=new Store(readHost),tick=()=>new Promise(r=>setImmediate(r));
 reading.refetch=async()=>{reading.markOpenRead();};
 reading.state={...reading.state,overview:{me:{address:'owner/linux'}},open:{kind:'thread',id:ids.topic},views:{
  [ids.topic]:{messages:[{id:ids.question,kind:'question',dir:'out',from:'owner/linux',to:'owner/zenbook'},
    {id:ids.answer,kind:'answer',dir:'in',unread:true}]},
  [ids.sibling]:{messages:[{id:ids.sibling,kind:'question',dir:'in',unread:true}]}
 }};
 const present=()=>{const o=reading.state.open,v=reading.state.views[o.id];reading.state.readVisit=reading.prepareReadScope(o,v,{text:''});reading.reportVisible(reading.state.readVisit,v.messages);};
 present();await tick();await tick();
 assert.deepEqual(reads,[[ids.question,ids.answer]],'acknowledge exact viewed outgoing question and incoming answer');
 reading.markOpenRead();await tick();assert.equal(reads.length,1,'successful refetch never loops');
 reading.state.views[ids.topic].messages.push({id:ids.sibling,kind:'question',dir:'out'});
 failRead=true;present();await tick();failRead=false;present();await tick();
 assert.deepEqual(reads.slice(-2),[[ids.sibling],[ids.sibling]],'failed read acknowledgment retries only its exact ID');
 assert(!reads[0].includes(ids.sibling),'unviewed sibling topic omitted');
 const previous=reading.readAcknowledged.ids;
 reading.state={...reading.state,open:{kind:'thread',id:ids.sibling}};present();await tick();
 assert.notEqual(reading.readAcknowledged.ids,previous,'read acknowledgments retained only for the current topic');
 reading.state={...reading.state,open:{kind:'thread',id:ids.topic}};present();await tick();
 assert(reads.at(-1).includes(ids.question),'reopening a topic can acknowledge its exact displayed IDs again');
 reading.stop();
 console.log('conversation link store read check PASS: exact IDs, sibling isolation, dedup, retry, topic-scoped retention');
 console.log('conversation link store check PASS: exact local landing, failure identity, draft/agent/reply/files, remount binding, no action/history refresh');
})().catch(e=>{console.error(e);process.exitCode=1;});
