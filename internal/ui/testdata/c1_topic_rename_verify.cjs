// One selected source-bound case. This never qualifies the complete family.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {bindings}=require('./c1_topic_rename.cjs');
const {checkMeasurement,digest}=require('./c1_oracle.cjs');
const faults=['primary-noop','independent-noop','wrong-target','observer-fault','slow-paint'];
function caseSpec(o){
 assert(o&&['topicbar','person'].includes(o.placement),'rename explicit placement required');
 assert(['reset','save-click','save-enter'].includes(o.action),'rename unsupported/unimplemented input');
 assert([390,1440].includes(o.width),'rename explicit width required');
 assert([10,100].includes(o.history),'rename explicit synthetic history required');
 assert(['resolve','reject'].includes(o.completion),'rename explicit completion required');
 assert(o.negative===null||faults.includes(o.negative),'rename unknown negative');
 const title=o.action==='reset'?'Automatic topic title':'Renamed case title';
 const pending=`s=>!s.rename_open&&s.topic_visible&&s.topic_label.startsWith(${JSON.stringify(title)})&&s.topic_text.includes(${JSON.stringify(title)})&&s.backend.pending===1&&s.backend.requests.length===1&&s.backend.confirmed_title==='Original renamed topic'`;
 const final=o.completion==='resolve'
  ?`s=>!s.rename_open&&s.topic_visible&&s.topic_label.startsWith(${JSON.stringify(title)})&&s.topic_text.includes(${JSON.stringify(title)})&&s.backend.pending===0&&s.backend.requests[0]?.state==='resolved'&&s.backend.confirmed_title===${JSON.stringify(title)}`
  :`s=>!s.rename_open&&s.topic_visible&&s.topic_text.includes('Original renamed topic')&&s.backend.pending===0&&s.backend.requests[0]?.state==='rejected'&&s.alerts.some(a=>a.includes('Synthetic rename refused'))`;
 const other=`s=>s.composer==='Independent rename text'&&s.composer_visible&&s.composer_mirror.includes('Independent rename text')&&s.backend.pending===1&&s.backend.requests.length===1&&s.backend.confirmed_title==='Original renamed topic'`;
 const target=o.action==='reset'?bindings.reset:o.action==='save-enter'?bindings.name:bindings.save;
 const handler=o.action==='reset'?bindings.reset+'#onClick':bindings.submit+'#onSubmit';
 return{schema:'agentnet-c1-rename-single/v1',case_id:[o.placement,o.action,o.width,o.history,o.completion].join('::'),placement:o.placement,action:o.action,width:o.width,history:o.history,completion:o.completion,negative:o.negative,
  title,request_title:o.action==='reset'?'':title,target_definition_id:target,handlers:[handler],
  menu_definition_id:bindings[o.placement+'_menu'],name_definition_id:bindings.name,submit_alias:bindings.submit,
  other_definition_id:bindings.composer,other_native_alias:bindings.composer_native_alias,other_handlers:[bindings.composer+'#onChange'],
  expected:{local:pending,confirmed:final},other_expected:{local:other,confirmed:other},
  dimensions:{skin:'Comic',platform:'Linux headless Chromium',input:o.action==='save-enter'?'keyboard Enter in name':'pointer',viewport_height:960,history:'synthetic source messages only',view:'fresh context; fonts loaded before input',real_db_sync:false},
  scope:'one synthetic component case; no complete control/state/skin/platform acceptance',missing:['name editing as separately measured action','personal Main/root','duplicates','stale/newer state','navigation/unmount','other input variants','real DB/sync normal/10x cold/warm','other skins/native platforms']};
}
function painted(m,expected,read){
 const p=m.presented;assert(p&&p.at_ms>=m.feedback.at_ms,'rename screenshot witness missing');
 assert(p.at_ms-m.input.at_ms<1000&&p.at_ms-m.driver_at_ms<1000,'rename presented feedback exceeded 1000ms');
 assert.equal(new Function('return ('+expected.local+')')()(p.state),true,'rename intended presented state missing');
 assert(/^feedback-0(?:-other)?\.png$/.test(p.path),'rename screenshot path invalid');
 const png=read(p.path);assert(png.subarray(0,8).equals(Buffer.from('89504e470d0a1a0a','hex'))&&digest(png)===p.sha256,'rename screenshot bytes/hash mismatch');
}
function verify(plan,report,read){
 assert(plan.schema==='agentnet-c1-rename-prepared/v1','rename prepared plan missing');
 const spec=caseSpec(plan.case);assert.deepEqual(spec,plan.case,'rename case differs from owning source definition');assert.equal(report.case_id,spec.case_id,'rename wrong case');
 assert.deepEqual(report.prepared_binding,plan.binding,'rename prepared binding mismatch');
 assert.equal(report.browser?.sha256,plan.binding.browser_sha256,'rename actual browser bytes mismatch');
 assert(report.browser?.pid>0&&report.browser?.version,'rename actual browser identity missing');
 assert(report.cleanup?.browser_closed===true&&report.cleanup?.server_closed===true,'rename explicit cleanup missing');
 assert(report.phase!=='bootstrap'&&report.bootstrap?.complete===true,'rename bootstrap incomplete');
 assert.equal(report.bootstrap.menu_definition_id,spec.menu_definition_id,'rename wrong source placement');
 assert.equal(report.bootstrap.actual.width,spec.width,'rename viewport metadata unconsumed');assert.equal(report.bootstrap.actual.height,960);
 assert.equal(report.bootstrap.actual.state.message_count,spec.history,'rename history metadata unconsumed');assert.equal(report.bootstrap.actual.state.view_kind,spec.placement==='person'?'dm':'group','rename placement metadata unconsumed');
 assert(report.bootstrap.unexpected.length===0,'rename undeclared fixture request');
 assert.deepEqual(report.browser_errors,[],'rename browser/console errors');
 if(spec.negative){
  assert(report.status==='failed'&&report.error,'rename negative did not fail');
  assert.equal(report.failure_phase,'observation','rename fixture/cleanup failure is not an accepted negative');
  assert(report.failure?.backend?.unexpected.length===0,'rename negative had fixture route failure');
  const measurement=spec.negative==='independent-noop'?report.failure.other:report.failure.primary;
  assert(measurement?.input?.trusted===true,'rename negative trusted input missing');
  if(spec.negative==='wrong-target'){
   assert.notEqual(measurement.target_definition_id,spec.target_definition_id,'rename wrong-target did not change actual source');
   assert(report.error.includes('C1 DOM/source identity mismatch'),'rename wrong-target diagnostic mismatch');
  }else{
   const handler=spec.negative==='independent-noop'?spec.other_handlers[0]:spec.handlers[0];
   const hit=report.failure.fault_hits.find(h=>h.kind===spec.negative&&h.handler===handler&&h.trusted===true);assert(hit,'rename actual fault hook not hit');
   if(spec.negative==='observer-fault')assert(report.error.includes('C1 observer exception: Injected rename observer fault'),'rename observer fault diagnostic mismatch');
   else if(spec.negative==='slow-paint'){assert(hit.blocked_ms>=1100,'rename slow paint not injected');assert(/C1 (local feedback exceeded 1000ms|missing feedback timing\/witness)/.test(report.error),'rename slow paint diagnostic mismatch');}
   else assert(report.error.includes('C1 missing feedback timing/witness'),'rename no-op diagnostic mismatch');
  }
  if(spec.negative==='primary-noop'||spec.negative==='wrong-target')assert.equal(report.failure.backend.requests.length,0,'rename suppressed/wrong control mutated backend');
  else{assert.equal(report.failure.backend.pending,1,'rename fault did not occur with backend held');assert.equal(report.failure.backend.requests.length,1);assert.equal(report.failure.backend.requests[0].title,spec.request_title);}
  if(spec.negative==='independent-noop'){checkMeasurement(report.primary,spec.handlers,spec.expected,spec.target_definition_id,true);painted(report.primary,spec.expected,read);}
  return{accepted_negative:true,product_pass:false,required:1,passed:0,subset_only:true,release_qualified:false};
 }
 assert.equal(report.status,'passed','rename workload did not pass');assert.equal(report.errors.length,0,'rename workload errors');
 checkMeasurement(report.primary,spec.handlers,spec.expected,spec.target_definition_id);
 checkMeasurement(report.other,spec.other_handlers,spec.other_expected,spec.other_definition_id);
 painted(report.primary,spec.expected,read);painted(report.other,spec.other_expected,read);
 assert.equal(report.held_backend.pending,1);assert.equal(report.held_backend.requests.length,1);assert.equal(report.held_backend.confirmed_title,'Original renamed topic');
 assert.equal(report.final_backend.requests.length,1,'rename duplicate backend operation');
 assert.equal(report.final_backend.requests[0].title,spec.request_title,'rename wrong intended operation');
 assert.equal(report.final_backend.requests[0].state,spec.completion==='resolve'?'resolved':'rejected','rename terminal backend result mismatch');
 assert.equal(report.final_backend.unexpected.length,0);
 return{required:1,passed:1,subset_only:true,release_qualified:false};
}
module.exports={caseSpec,verify,faults};
if(require.main===module){
 try{const [prepared,run]=process.argv.slice(2);assert(prepared&&run,'usage: verifier prepared-directory run-directory');const preparedPlan=JSON.parse(fs.readFileSync(path.join(prepared,'plan.json'))),report=JSON.parse(fs.readFileSync(path.join(run,'report.json'))),plan={...preparedPlan,case:caseSpec({...preparedPlan.case,negative:report.negative??null})};console.log(JSON.stringify(verify(plan,report,p=>fs.readFileSync(path.join(run,p)))));}
 catch(e){console.error(e.stack);process.exitCode=1;}
}
