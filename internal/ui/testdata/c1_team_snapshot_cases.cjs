// First explicit subset. These cases do not cover all eighteen discovered
// scenarios, all owners, all skins or native platforms. They are not results.
const N='agentnet:internal/ui/web/src/features/NewChat.tsx#';
const T='agentnet:internal/ui/web/src/features/Teams.tsx#';
const assert=require('node:assert/strict'),path=require('node:path');
const {measure,finishMeasurement,capturePresented,checkBackend}=require('./c1_oracle.cjs');
const source={
 add:T+'current/Button/onClick#1',retry:T+'current/Button/onClick#2',cancel:T+'current/Button/onClick#3',
 name:N+'back/input/onChange#1',checkbox:N+'back/input/onChange#3',remove:N+'back/button/onClick#2',
};
function cases(){
 const pending=s=>s.snapshotPending&&s.snapshotPendingVisible&&!s.snapshotError&&s.createDisabled;
 const added=s=>!s.snapshotPending&&s.selected.includes('Person 01')&&s.selected.includes('Person 02')&&!s.createDisabled;
 return [
  {key:'add.held',action:'add',local:pending,confirmed:added},
  {key:'add.duplicate',action:'add',duplicate:true,local:pending,confirmed:added},
  {key:'add.failure',action:'add',completion:'reject',local:pending,confirmed:s=>s.snapshotPending&&!!s.snapshotError&&s.selected.length===0},
  {key:'add.concurrent-select',action:'add',other:'checkbox',local:pending,confirmed:s=>!s.snapshotPending&&s.selected.includes('Person 00')&&s.selected.includes('Person 01')&&s.selected.includes('Person 02')},
  {key:'add.concurrent-remove',action:'add',other:'remove',local:pending,confirmed:s=>!s.snapshotPending&&!s.selected.includes('Person 01')&&s.selected.includes('Person 02')},
  {key:'cancel.held',action:'cancel',local:s=>!s.snapshotPending&&!s.createDisabled,confirmed:s=>!s.snapshotPending&&!s.createDisabled,late_result:'must not add people'},
  {key:'retry.error-held',action:'retry',local:pending,confirmed:added},
  {key:'add.replacement-stale',action:'add',local:pending,confirmed:s=>!s.snapshotPending&&s.selected.includes('Person 02')&&!s.selected.includes('Person 01'),late_result:'old List A must not overwrite newer List B'},
 ].map(c=>({...c,target_definition_id:source[c.action],other_definition_id:source[c.other||'name'],
   owners:['direct NewChatButton fixture → picker → NewGroupForm → TeamPeople'],
   missing_owners:['ChatList wide/narrow/empty-search placements','history-transfer destination','people-list draft','InviteSheet'],
   confirmation_scope:'mock-backed presented selection; not HTTP/Hub/SQLite or remote custody'}));
}
function manifestCases(){
 const rows=[];
 for(const width of [1440,390])for(const d of cases()){
  const ordinal=rows.length,value='Concurrent '+ordinal;
  const other_expected=d.other==='checkbox'?{local:"s=>s.checkboxes.some(p=>p.name==='Person 00'&&p.checked)",confirmed:"s=>s.selected.includes('Person 00')"}
   :d.other==='remove'?{local:"s=>!s.selected.includes('Person 01')",confirmed:"s=>s.checkboxes.some(p=>p.name==='Person 01'&&!p.checked)"}
   :{local:'s=>s.groupName==='+JSON.stringify(value),confirmed:'s=>s.groupName==='+JSON.stringify(value)};
  const backend=d.completion==='reject'?'failing':'held';
  rows.push({...d,local:undefined,confirmed:undefined,case_id:d.key+'::'+width+'::'+backend,ordinal,target_id:d.action,
   expected:{local:d.local.toString(),confirmed:d.confirmed.toString()},other_expected,
   handlers:[source[d.action]+'#onClick'],other_handlers:[source[d.other||'name']+(d.other==='remove'?'#onClick':'#onChange')],
   dimensions:{width,backend,skin:'comic',platform:'Linux-Chromium',owner:'direct NewChatButton fixture',history:'not_exercised',people_fixture:41,input:'pointer',backend_completion:d.completion||'resolve'},
  });
 }
 return rows;
}
function checkAppWitness(w){
 assert(w,'C1 app side-effect witness missing');
 assert.equal(w.group_creates,0,'Repeated selection submitted group creation');
 assert.equal(w.group_invites,0,'Repeated selection sent group invitation');
 for(const key of ['unexpected_mutations','navigation','submit_events','parent_submit_handlers'])assert.deepEqual(w[key],[],'C1 unexpected app action: '+key);
 assert.equal(w.state.group_creation,false,'Unexpected group creation state');
 assert.equal(w.state.has_open,false,'Unexpected conversation navigation');
 assert.equal(w.state.message_landing,false,'Unexpected message navigation');
 assert.equal(w.dialog,'group','Selection unexpectedly closed/replaced picker dialog');
 assert.equal(typeof w.group_name,'string','Group details unexpectedly replaced');
}
async function executeCell({page,width,manifest,report,out,snapshot,negative}){
 const read=()=>page.evaluate(s=>new Function('return ('+s+')()')(),snapshot.toString());
 const witness=()=>page.evaluate(()=>fixture.snapshots.witness());
 const appWitness=async()=>{
  const state=await read();
  return page.evaluate(({dialog,group_name})=>({group_creates:fixture.group_creates,group_invites:fixture.group_invites,unexpected_mutations:fixture.unexpected_mutations,navigation:fixture.navigation,state:fixture.state(),submit_events:globalThis.__c1SubmitEvents,parent_submit_handlers:globalThis.__c1Fired.filter(e=>e.id==='agentnet:internal/ui/web/src/features/NewChat.tsx#back/form/onSubmit#1#onSubmit'),dialog,group_name}),{dialog:state.dialog,group_name:state.groupName});
 };
 const settle=(seq,kind,value)=>page.evaluate(({seq,kind,value})=>fixture.snapshots.settle(seq,kind,value),{seq,kind,value});
 const add=()=>page.getByRole('button',{name:'Add list’s people',exact:true});
 const choose=()=>page.getByRole('combobox',{name:'People list',exact:true});
 const cancel=()=>page.getByRole('button',{name:'Cancel list selection',exact:true});
 for(const c of manifest.cases.filter(c=>c.dimensions.width===width)){
  let phase='fixture';
  try{
   if(await page.getByRole('button',{name:'Close',exact:true}).isVisible().catch(()=>false))await page.getByRole('button',{name:'Close',exact:true}).click();
   await page.getByRole('button',{name:'New',exact:true}).click();await page.getByRole('button',{name:/^New group/}).click();
   await page.getByRole('textbox',{name:'Group name',exact:true}).fill('Group snapshot');
   await choose().getByRole('option',{name:'List A',exact:true}).waitFor({state:'attached'});
   const initial=await witness();assert.equal(initial.pending,0,'Previous case left a held fixture request');
   const base=initial.requests.length;
   const options=await choose().locator('option').allTextContents();
   assert.deepEqual(options,['Choose a people list','List A','List B'],'Excluded list fixture metadata not consumed');
   if(c.other==='remove')await page.getByRole('checkbox',{name:'Person 01',exact:true}).check();
   await choose().selectOption('list-a'); // Preparation only, not measured trusted-input coverage.
   if(c.action==='cancel'||c.action==='retry'||c.key==='add.replacement-stale'){
    await add().click();await cancel().waitFor();
    if(c.action==='retry'){
     await settle(base+1,'reject');await page.getByRole('alert').filter({hasText:'Synthetic snapshot refusal'}).waitFor();
    }else if(c.key==='add.replacement-stale'){
     await cancel().click();await choose().selectOption('list-b');
    }
   }
   const def=cases().find(d=>d.key===c.key);
   const target=c.action==='retry'?page.getByRole('button',{name:'Retry list selection',exact:true}):c.action==='cancel'?cancel():add();
   await page.evaluate(()=>{globalThis.__c1NegativeArmed=true;});
   phase='primary';
   const local=await measure(page,target,{snapshot,local:def.local,confirmed:def.confirmed,handlers:c.handlers,target_definition_id:c.target_definition_id},async t=>{
    if(c.input_variant==='Enter'||c.input_variant==='Space')return t.press(c.input_variant);
    if(c.input_variant==='touch'||c.input_variant==='mouse-independent'){
     const b=await t.boundingBox();assert(b,'Repeated input target missing');const x=b.x+b.width/2,y=b.y+b.height/2;
     if(c.input_variant==='touch'){await page.touchscreen.tap(x,y);await page.touchscreen.tap(x,y);}
     else{await page.mouse.click(x,y);await page.mouse.click(x,y);}
     return;
    }
    return c.duplicate?t.dblclick():t.click();
   });
   assert(def.local(await read()),'Repeated input lost intended pending/local state');
   const app_after_input=await appWitness();checkAppWitness(app_after_input);
   await capturePresented(page,path.join(out,`feedback-${report.results.length}.png`),snapshot,local);
   if(c.completion==='reject'){
    await settle(base+1,'reject');
    await page.getByRole('alert').filter({hasText:'Synthetic snapshot refusal'}).waitFor();
   }
   phase='independent';
   const otherTarget=c.other==='checkbox'?page.getByRole('checkbox',{name:'Person 00',exact:true}):c.other==='remove'?page.getByRole('button',{name:'Remove Person 01',exact:true}):page.getByRole('textbox',{name:'Group name',exact:true});
   const other=await measure(page,otherTarget,{snapshot,local:new Function('return ('+c.other_expected.local+')')(),confirmed:new Function('return ('+c.other_expected.confirmed+')')(),handlers:c.other_handlers,target_definition_id:c.other_definition_id},t=>c.other==='checkbox'?t.check():c.other==='remove'?t.click():t.fill('Concurrent '+c.ordinal));
   await capturePresented(page,path.join(out,`feedback-${report.results.length}-other.png`),snapshot,other);
   phase='backend-held';
   const raw=await witness(),scopeBase=c.action==='retry'?base+1:base,active=raw.requests.filter(r=>r.seq>scopeBase);
   const backend_witness={mode:c.dimensions.backend,phase:'at-independent-presented-feedback',requests:active.length,pending:active.filter(r=>r.state==='pending').length,failed:active.filter(r=>r.state==='rejected').length,raw,before_case_seq:base,scope_after_seq:scopeBase};
   checkBackend(backend_witness,c.dimensions.backend);
   assert.equal(raw.requests.length-base,c.action==='retry'||c.key==='add.replacement-stale'?2:1,'Rapid input dispatched unexpected snapshot count');
   phase='completion';
   const latest=raw.requests.at(-1).seq;
   if(c.key==='add.replacement-stale')await settle(latest,'resolve',{persons:[{id:'person-2'}],at:1700000002});
   else if(c.completion!=='reject')await settle(latest,'resolve');
   await finishMeasurement(page,local);await finishMeasurement(page,other);
   if(c.key==='add.replacement-stale')await settle(base+1,'resolve',{persons:[{id:'person-1'}],at:1700000001});
   await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
   const after=await read(),completion=await witness();
   if(c.action==='cancel')assert.deepEqual(after.selected,[],'Late cancelled snapshot resurrected people');
   if(c.key==='add.replacement-stale')assert.deepEqual(after.selected,['Person 02'],'Old snapshot overwrote newer selected people');
   if(c.other==='remove')assert.equal(after.selected.includes('Person 01'),false,'Snapshot resurrected manually removed person');
   assert.equal(completion.pending,0,'Case left a pending snapshot');
   const app_after_completion=await appWitness();checkAppWitness(app_after_completion);
   const interaction_trace=await page.evaluate(from=>(globalThis.__c1InteractionTrace||[]).filter(e=>e.at_ms>=from),local.driver_at_ms);
   report.results.push({case_id:c.case_id,target_id:c.target_id,dimensions:c.dimensions,status:'pass',local,other,backend_witness,backend_completion_witness:completion,after_state:after,late_guard_checked:!!c.late_result,interaction_trace,app_after_input,app_after_completion,confirmation_kind:'local-ui-result'});
   await page.evaluate(()=>{globalThis.__c1NegativeArmed=false;});
  }catch(e){
   e.message=phase+': '+e.message;
   let diagnostic;
   try{diagnostic=await page.evaluate(source=>{
    const o=globalThis.__c1Observation;
    const observation=o?Object.fromEntries(Object.entries(o).filter(([key])=>!['promise','feedbackPromise'].includes(key))):null;
    return{observation,state:new Function('return ('+source+')()')(),fired:globalThis.__c1Fired,backend:fixture.snapshots.witness(),interaction_trace:globalThis.__c1InteractionTrace};
   },snapshot.toString());}catch(error){diagnostic={error:String(error)};}
   report.results.push({case_id:c.case_id,status:'fail',reason:e.message,diagnostic});throw e;
  }
 }
}
function checkCompletions(manifest,report){
 for(const c of manifest.cases){
  const r=report.results.find(r=>r.case_id===c.case_id),w=r?.backend_completion_witness;
  checkAppWitness(r?.app_after_input);checkAppWitness(r?.app_after_completion);
  assert(w&&w.pending===0&&w.mode==='held','C1 snapshot completion evidence missing');
  const baseline=r.backend_witness.before_case_seq;
  assert(Number.isSafeInteger(baseline)&&baseline>=0,'C1 invalid case request baseline');
  const own=w.requests.filter(x=>x.seq>baseline),replacement=c.key==='add.replacement-stale',retry=c.action==='retry';
  assert.equal(own.length,replacement||retry?2:1,'C1 wrong terminal request count');
  assert.deepEqual(own.map(x=>x.seq),own.map((_,n)=>baseline+n+1),'C1 request identity sequence changed');
  assert.deepEqual(own.map(x=>x.teams),replacement?[['list-a'],['list-b']]:own.map(()=>['list-a']),'C1 exact list/retry binding changed');
  assert.deepEqual(own.map(x=>x.state),retry?['rejected','resolved']:[c.completion==='reject'?'rejected':'resolved',...(replacement?['resolved']:[])],'C1 snapshot result states missing');
  assert(own.every(x=>Number.isSafeInteger(x.settled_ordinal)&&x.settled_ordinal>0),'C1 actual settlement order missing');
  if(replacement)assert(own[1].settled_ordinal<own[0].settled_ordinal,'C1 old completion was not delivered after replacement');
  if(c.late_result)assert.equal(r.late_guard_checked,true,'C1 late-result outcome not checked');
  assert(new Function('return ('+c.expected.confirmed+')')()(r.after_state),'C1 intended result missing after completion');
  if(c.action==='cancel')assert.deepEqual(r.after_state.selected,[],'C1 cancelled people resurrected');
  if(replacement)assert.deepEqual(r.after_state.selected,['Person 02'],'C1 old completion overwrote replacement');
  const active=r.backend_witness.raw.requests.filter(x=>x.seq>r.backend_witness.scope_after_seq);
  assert.equal(r.backend_witness.requests,active.length,'C1 active request witness mismatch');
  assert.equal(r.backend_witness.pending,active.filter(x=>x.state==='pending').length,'C1 held request witness mismatch');
  assert.equal(r.backend_witness.failed,active.filter(x=>x.state==='rejected').length,'C1 failing request witness mismatch');
 }
}
module.exports={cases,source,manifestCases,executeCell,checkCompletions,checkAppWitness};
