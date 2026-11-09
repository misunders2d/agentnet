// Source/configuration/verifier unit checks only, no browser launch.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {spawnSync}=require('node:child_process');
const {caseSpec,verify}=require('./c1_topic_rename_verify.cjs');
let checks=0,skipped=0;const check=(name,f)=>{f();checks++;console.log('PASS '+name);};
const input={placement:'person',action:'reset',width:390,history:10,completion:'resolve',negative:null};
for(const [name,value] of [['placement','bad'],['action','name-edit'],['width',400],['history',0],['completion','maybe'],['negative','retry']])check('reject unsupported '+name,()=>assert.throws(()=>caseSpec({...input,[name]:value})));
const spec=caseSpec(input),predicate=s=>new Function('return ('+s+')')();
check('distinct physical and callback identities',()=>{const enter=caseSpec({...input,action:'save-enter'}),click=caseSpec({...input,action:'save-click'});assert.notEqual(enter.target_definition_id,click.target_definition_id);assert.deepEqual(enter.handlers,click.handlers);assert.notEqual(spec.other_definition_id,spec.other_native_alias);});
check('source placement metadata changes actual expected menu identity',()=>assert.notEqual(spec.menu_definition_id,caseSpec({...input,placement:'topicbar'}).menu_definition_id));
const pending={rename_open:false,topic_visible:true,topic_label:'Automatic topic title, open topic, menu',topic_text:'Automatic topic title',backend:{pending:1,requests:[{state:'pending'}],confirmed_title:'Original renamed topic'}};
check('intended caption and held result both required',()=>{const p=predicate(spec.expected.local);assert(p(pending));assert(!p({...pending,topic_text:'Original renamed topic'}));assert(!p({...pending,backend:{...pending.backend,pending:0}}));assert(!p({...pending,rename_open:true}));});
check('accessible label alone cannot prove visible change',()=>assert(!predicate(spec.expected.local)({...pending,topic_visible:false})));
check('independent text requires actual visible mirror',()=>{const p=predicate(spec.other_expected.local),s={composer:'Independent rename text',composer_visible:true,composer_mirror:'Independent rename text',backend:pending.backend};assert(p(s));assert(!p({...s,composer_mirror:''}));assert(!p({...s,composer_visible:false}));});
check('empty report fails closed',()=>assert.throws(()=>verify({schema:'agentnet-c1-rename-prepared/v1',case:spec,binding:{}},{},()=>Buffer.alloc(0))));
const dir=fs.mkdtempSync(path.join(os.tmpdir(),'agr-rename-verifier-'));
try{
 const prepared=path.join(dir,'prepared'),run=path.join(dir,'run');fs.mkdirSync(prepared);fs.mkdirSync(run);
 fs.writeFileSync(path.join(prepared,'plan.json'),JSON.stringify({schema:'agentnet-c1-rename-prepared/v1',case:spec,binding:{}}));fs.writeFileSync(path.join(run,'report.json'),'{}');
 check('malformed verifier CLI cannot exit0',()=>{const r=spawnSync(process.execPath,[path.join(__dirname,'c1_topic_rename_verify.cjs'),prepared,run],{encoding:'utf8',timeout:3000});assert.equal(r.status,1);assert(r.stderr.includes('rename wrong case'));});
 if(process.platform==='linux')check('unbounded run refuses before browser/server startup',()=>{const r=spawnSync(process.execPath,[path.join(__dirname,'c1_topic_rename_driver.cjs'),'--run',prepared,'--negative','none'],{env:{...process.env,C1_BOUND_UNIT:'',C1_OUT:run},encoding:'utf8',timeout:3000});assert.equal(r.status,1);assert(r.stderr.includes('refuse browser outside exact task-owned unit'));assert(!fs.existsSync(path.join(run,'processes.json')));});
 else skipped++;
}finally{fs.rmSync(dir,{recursive:true,force:true});}
console.log(JSON.stringify({source_checks_passed:checks,skipped,rendered_cases:0,live_negative_proofs:0,release_qualified:false}));
