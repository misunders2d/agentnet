// Fabricated records exercise the C1 verifier. They never qualify app performance.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const path = require('node:path');
const { checkReport, digest } = require('./c1_oracle.cjs');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC0lEQVR42mP8/x8AAwMCAO+jZ4kAAAAASUVORK5CYII=', 'base64');
const expected = { local:'s=>s.value===\'after\'', confirmed:'s=>s.value===\'after\'' };
function fixture() {
  const measurement = () => ({ baseline_local:false,baseline_confirmed:false,before:{value:'before'},driver_at_ms:0,target_definition_id:'exact-definition',
    input:{at_ms:1,trusted:true,local_before:false,confirmed_before:false,before:{value:'before'}},
    feedback:{at_ms:20,frames:2,state:{value:'after'}},confirmed:{at_ms:30,frames:2,state:{value:'after'}},
    presented:{at_ms:40,state:{value:'after'},path:'feedback-0.png',sha256:digest(png)},fired:['exact-handler'] });
  const binding = { revision:'a'.repeat(40),source_digest:'b'.repeat(64),bundle_sha256:'c'.repeat(64),runner_sha256:'d'.repeat(64),browser_sha256:'e'.repeat(64) };
  const c = {case_id:'unit::1',target_id:'exact-person',target_definition_id:'exact-definition',other_definition_id:'exact-definition',dimensions:{backend:'held'},handlers:['exact-handler'],other_handlers:['exact-handler'],expected,other_expected:expected};
  return { manifest:{binding,cases:[c],required_case_ids:[c.case_id]},report:{binding:structuredClone(binding),results:[{case_id:c.case_id,target_id:c.target_id,dimensions:c.dimensions,status:'pass',local:measurement(),other:measurement(),backend_witness:{mode:'held',requests:1,pending:1,failed:0},screenshot:{path:'feedback-0.png',sha256:digest(png)},confirmation_kind:'local-ui-result'}]} };
}
let checks = 0;
assert.equal(checkReport(fixture().manifest,fixture().report,()=>png).release_qualified,false);checks++;
const negatives = [
  ['open',f=>f.report.results[0].status='open'],
  ['missing case',f=>f.report.results=[]],
  ['extra case',f=>f.report.results.push(structuredClone(f.report.results[0]))],
  ['wrong case',f=>f.report.results[0].case_id='not-requested'],
  ['empty denominator',f=>f.manifest.required_case_ids=[]],
  ['missing definition',f=>f.manifest.cases=[]],
  ['already true before arm',f=>f.report.results[0].local.before={value:'after'}],
  ['already true at input',f=>f.report.results[0].local.input.before={value:'after'}],
  ['untrusted input',f=>f.report.results[0].local.input.trusted=false],
  ['generic change not intended',f=>f.report.results[0].local.feedback.state={value:'something else'}],
  ['missing independent control',f=>delete f.report.results[0].other],
  ['unbounded independent control',f=>f.report.results[0].other.feedback.at_ms=1001],
  ['missing confirmed timestamp',f=>delete f.report.results[0].local.confirmed.at_ms],
  ['missing handler despite empty result declaration',f=>{f.report.results[0].local.fired=[];f.report.results[0].handlers=[]}],
  ['wrong target',f=>f.report.results[0].target_id='other-person'],
  ['wrong DOM/source binding',f=>f.report.results[0].local.target_definition_id='other-definition'],
  ['wrong backend',f=>f.report.results[0].backend_witness.mode='normal'],
  ['backend not held',f=>f.report.results[0].backend_witness.pending=0],
  ['backend never requested',f=>f.report.results[0].backend_witness.requests=0],
  ['wrong candidate binding',f=>f.report.binding.revision='f'.repeat(40)],
  ['missing screenshot',f=>delete f.report.results[0].local.presented],
  ['wrong screenshot hash',f=>f.report.results[0].local.presented.sha256='f'.repeat(64)],
  ['screenshot traversal',f=>f.report.results[0].local.presented.path='../private.png'],
  ['late presented screenshot',f=>f.report.results[0].other.presented.at_ms=1001],
  ['result changed before capture',f=>f.report.results[0].local.presented.state={value:'before'}],
  ['local result pretending remote delivery',f=>f.report.results[0].confirmation_kind='delivered'],
];
for (const [name,mutate] of negatives) { const f=fixture();mutate(f);assert.throws(()=>checkReport(f.manifest,f.report,()=>png),name);checks++; }
for (const args of [[],['--family','unimplemented']]) {
  const child=spawnSync(process.execPath,[path.join(__dirname,'c1_controls_rendered.cjs'),...args],{encoding:'utf8'});
  assert.equal(child.status,1,'incomplete C1 CLI must exit1');assert.match(child.stderr,/C1 incomplete/);checks++;
}
console.log(JSON.stringify({checks,negative_records:negatives.length,incomplete_cli_exit1:2,scope:'verifier unit fixtures only; zero app performance observations'}));
