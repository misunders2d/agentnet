const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const {MP4,fixtureMP4,mediaResponse,isWindowsMediaCandidate,classifyWindowsMedia}=require('./latency_media_probe.cjs');
const clone=o=>structuredClone(o),diag={readyState:0,networkState:3,errorCode:4,currentSrcScheme:'blob',duration:'NaN',width:0,height:0};
function sample(){
 const common={suite:'media',portableVector:'',ok:false,pageErrors:[],feedbackFailures:[],presentationFailures:[],videoMetadataFailure:clone(diag)};
 const report={...clone(common),error:'baseline: Timed out: actual video metadata\noriginal stack',variants:[{...clone(common),error:'Timed out: actual video metadata\noriginal stack'}],suiteVectors:['baseline'],mediaInputs:[{}, {sha256:MP4.sha256,size:MP4.size,type:MP4.type}],mediaSelectedSource:{container:'mp4'},actions:[{id:'media.photo-original',ok:true,stateVerified:true},{id:'media.video-select',ok:true,stateVerified:true}]};
 const verified={sourceSHA256:MP4.sha256,sourceSize:MP4.size,fetchSHA256:MP4.sha256,pageErrors:[]};
 const probe={source:{...MP4},transport:{ok:true},cleanup:{contextClosed:true,serverClosed:true},blob:{...clone(verified),...clone(diag),mode:'blob',metadataOk:false,playbackOk:false,metadataError:'Timed out: actual video metadata'},http:{...clone(verified),mode:'http',metadataOk:true,playbackOk:true,currentSrcScheme:'http',currentTime:.1,duration:1.2,width:320,height:180}};
 return{platform:'win32',engine:'webkit',suite:'media',report,driverErrors:[],probe};
}
let checks=0;
function rejects(label,mutate){const args=sample();mutate(args);assert.equal(classifyWindowsMedia(args),null,label);checks++;}
const original=sample(),before=JSON.stringify(original);
assert.equal(classifyWindowsMedia(original).execution,'not_executed');assert.equal(classifyWindowsMedia(original).causality,'source_scheme_supported');assert.equal(JSON.stringify(original),before,'raw original error/ok/actions/probe preserved');checks++;
const both=sample();Object.assign(both.probe.http,{metadataOk:false,playbackOk:false,...diag,currentSrcScheme:'http'});assert.equal(classifyWindowsMedia(both).causality,'broader_test_port_media_limitation');assert.match(classifyWindowsMedia(both).reason,/not blob-only proof/);checks++;
for(const platform of ['linux','darwin',''])rejects('other OS '+platform,a=>a.platform=platform);
for(const engine of ['chromium','firefox','WebView2',''])rejects('other engine '+engine,a=>a.engine=engine);
for(const suite of ['topics','compose','outbox','history',''])rejects('other interaction '+suite,a=>a.suite=suite);
rejects('report suite',a=>a.report.suite='actions');rejects('successful UI',a=>a.report.ok=true);
rejects('different UI failure',a=>a.report.error='baseline: Timed out: actual portable video playback');
rejects('contains metadata only in stack',a=>a.report.error='Runtime error\nTimed out: actual video metadata');
for(const [key,value]of Object.entries({readyState:1,networkState:2,errorCode:3,currentSrcScheme:'http'})){
 rejects('UI signature '+key,a=>a.report.videoMetadataFailure[key]=value);
 rejects('variant signature '+key,a=>a.report.variants[0].videoMetadataFailure[key]=value);
 rejects('independent signature '+key,a=>a.probe.blob[key]=value);
}
rejects('missing UI diagnostic',a=>delete a.report.videoMetadataFailure);
for(const key of ['pageErrors','feedbackFailures','presentationFailures']){
 rejects('unrelated original '+key,a=>a.report[key].push('unrelated'));
 rejects('unrelated variant '+key,a=>a.report.variants[0][key].push('unrelated'));
 rejects('missing original '+key,a=>delete a.report[key]);
}
rejects('driver runtime error',a=>a.driverErrors.push('unhandled rejection'));
rejects('original driver error',a=>a.report.driverError='transport');rejects('variant driver error',a=>a.report.variants[0].driverError='transport');
rejects('missing variant',a=>delete a.report.variants);rejects('extra failed variant',a=>a.report.variants.push(clone(a.report.variants[0])));
rejects('variant success',a=>a.report.variants[0].ok=true);rejects('wrong variant scope',a=>a.report.variants[0].portableVector='other');
rejects('wrong variant suite',a=>a.report.variants[0].suite='topics');rejects('wrong variant failure',a=>a.report.variants[0].error='Other error');
rejects('other suite vector',a=>a.report.suiteVectors=['baseline','other']);
for(const [key,value]of Object.entries({sha256:'wrong',size:MP4.size+1,type:'video/webm'}))rejects('original input '+key,a=>a.report.mediaInputs[1][key]=value);
rejects('other container',a=>a.report.mediaSelectedSource.container='webm');
rejects('earlier failed action',a=>a.report.actions[0].ok=false);rejects('unverified partial action',a=>a.report.actions[0].stateVerified=false);rejects('no actions',a=>a.report.actions=[]);
rejects('no independent probe',a=>delete a.probe);rejects('probe error',a=>a.probe.probeError='launch failure');
for(const [key,value]of Object.entries({sha256:'wrong',size:1,type:'video/webm',contentType:'video/mp4'}))rejects('independent source '+key,a=>a.probe.source[key]=value);
rejects('bad HTTP transport',a=>a.probe.transport.ok=false);rejects('context cleanup failed',a=>a.probe.cleanup.contextClosed=false);rejects('server cleanup failed',a=>a.probe.cleanup.serverClosed=false);
for(const mode of ['blob','http']){
 rejects(mode+' wrong mode',a=>a.probe[mode].mode='other');
 for(const [key,value]of Object.entries({sourceSHA256:'wrong',sourceSize:1,fetchSHA256:'wrong'}))rejects(mode+' bytes '+key,a=>a.probe[mode][key]=value);
 rejects(mode+' runtime error',a=>a.probe[mode].pageErrors.push('unrelated'));rejects(mode+' probe error',a=>a.probe[mode].probeError='fetch failure');
}
rejects('independent blob metadata succeeds',a=>a.probe.blob.metadataOk=true);rejects('independent blob playback succeeds',a=>a.probe.blob.playbackOk=true);rejects('no completed metadata guard',a=>delete a.probe.blob.metadataError);
for(const [key,value]of Object.entries({currentTime:0,currentSrcScheme:'blob',width:1,height:1,duration:8,metadataOk:false}))rejects('inconsistent HTTP success '+key,a=>a.probe.http[key]=value);
rejects('nonfinite HTTP metadata',a=>a.probe.http.duration=NaN);
const bytes=fixtureMP4();assert.equal(bytes.length,10580);
for(const method of ['GET','HEAD'])for(const [range,status,start,end]of [[undefined,200,0,10579],['bytes=0-31',206,0,31],['bytes=10570-',206,10570,10579],['bytes=-16',206,10564,10579],['bytes=0-999999',206,0,10579]]){
 const r=mediaResponse(bytes,method,range);assert.equal(r.status,status);assert.equal(r.headers['Content-Length'],String(end-start+1));assert.equal(r.headers['Accept-Ranges'],'bytes');assert.equal(r.headers['Content-Type'],'video/mp4');assert.deepEqual(r.body,method==='HEAD'?Buffer.alloc(0):bytes.subarray(start,end+1));if(status===206)assert.equal(r.headers['Content-Range'],`bytes ${start}-${end}/10580`);checks++;
}
for(const range of ['bytes=10580-','bytes=2-1','bytes=-0','bytes=-','bytes=0-1,5-8','nonsense','bytes=999999999999999999999-']){const r=mediaResponse(bytes,'GET',range);assert.equal(r.status,416);assert.equal(r.headers['Content-Range'],'bytes */10580');assert.equal(r.body.length,0);checks++;}
assert.equal(mediaResponse(bytes,'POST').status,405);checks++;

async function driverCase(options={}){
 const log=[],errors=[];let probeCalls=0,closed=0,browserClosed=0;
 const engine=options.engine||'webkit',suite=options.suite||'media',platform=options.platform||'win32',widths=({topics:4,compose:4,storage:3})[suite]||2;
 const args=sample();if(options.report)args.report=options.report;options.mutate?.(args);
 const browser={version:()=> 'synthetic',close:async()=>{browserClosed++;},newContext:async config=>({close:async()=>{closed++;},newPage:async()=>({on:(name,fn)=>{if(options.driverError&&name==='pageerror')fn(Error('unrelated page error'));},goto:async()=>{},waitForFunction:async()=>{},evaluate:async fn=>{
  const text=String(fn);if(text.includes('window.latencyReport'))return{...clone(args.report),viewport:config.viewport};if(text.includes('innerHTML'))return 'raw original failure DOM';if(text.includes('querySelectorAll'))return[];return{};
 }})})};
 const fakeProcess={platform,env:{AGENTNET_PLAYWRIGHT:'fake-engines',AGENTNET_LATENCY_ENGINES:engine,AGENTNET_LATENCY_SUITES:suite,AGENTNET_LATENCY_VIEWPORTS:'1440x960,390x844',LATENCY_URL:'http://127.0.0.1/latency'}};
 await vm.runInNewContext(fs.readFileSync(path.join(__dirname,'latency_rendered_check.cjs'),'utf8'),{URL,process:fakeProcess,require:name=>name==='fake-engines'?{[engine]:{launch:async()=>browser}}:name==='./latency_media_probe.cjs'?{isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe:async()=>{probeCalls++;return clone(args.probe);}}:require(name),console:{log:s=>log.push(JSON.parse(s)),error:s=>errors.push(s)}});
 assert.equal(closed,widths,'each original UI context closed');assert.equal(browserClosed,1,'original browser closed');
 const reports=log.filter(r=>r.suite),counts=log.find(r=>r.qualificationSummary).qualificationSummary;
 assert.equal(reports.length,widths);if(!options.report)for(const r of reports){assert.equal(r.ok,false,'never skip-as-pass');assert.equal(r.error,args.report.error,'raw error preserved');assert.deepEqual(r.actions,args.report.actions,'partial original actions preserved');}
 return{reports,counts,probeCalls,widths,exitCode:fakeProcess.exitCode,errors};
}
(async()=>{
 const allowed=await driverCase();assert.deepEqual(allowed.counts,{executed_pass:0,failed:0,not_executed:2,diagnostic:0});assert.equal(allowed.probeCalls,1,'same independent result reused across widths');assert.equal(allowed.exitCode,undefined);assert(allowed.reports.every(r=>r.execution==='not_executed'&&r.unsupportedTestPort.smoothnessQualified===false));checks++;
 for(const options of [{platform:'linux'},{platform:'darwin'},{engine:'chromium'},{engine:'firefox'},{suite:'topics'},{suite:'compose'},{suite:'outbox'},{driverError:true},{mutate:a=>a.report.pageErrors.push('fixture runtime error')},{mutate:a=>a.probe.blob.metadataOk=true},{mutate:a=>a.report.videoMetadataFailure.errorCode=3}]){
  const result=await driverCase(options);assert.equal(result.counts.failed,result.widths);assert.equal(result.counts.not_executed,0);assert.equal(result.exitCode,1,'all other UI failures remain fatal');checks++;
 }
 // Rendered verdicts: only a report whose every action carries a finite local
 // feedback inside the owner's bound passes; S19's held-write report (ok:true
 // with feedbackFailures) and missing measurements fail closed.
 const action=(id,ms)=>({id,feedbackMs:ms,feedbackBoundPassed:ms<1100,ok:ms<1100,stateVerified:true});
 const rendered=extra=>({ok:true,suite:'outbox',pageErrors:[],feedbackFailures:[],actions:[action('outbox.open',36),action('outbox.schedule-save',14)],...extra});
 const verdict=async report=>driverCase({platform:'linux',engine:'chromium',suite:'outbox',report});
 const good=await verdict(rendered());assert.deepEqual(good.counts,{executed_pass:2,failed:0,not_executed:0,diagnostic:0});assert.equal(good.exitCode,undefined);checks++;
 const s19=rendered({actions:[action('outbox.open',36),action('outbox.schedule-save',1147.8)],feedbackFailures:['outbox.schedule-save local feedback exceeds owner’s under-1100-ms bound']});
 for(const report of [s19,rendered({feedbackFailures:undefined}),rendered({actions:[]}),rendered({actions:undefined}),rendered({actions:[action('outbox.open',36),{id:'outbox.schedule-save',ok:true,stateVerified:true}]}),rendered({actions:[action('outbox.open',36),{...action('outbox.schedule-save',14),feedbackMs:NaN}]}),rendered({actions:[action('outbox.open',36),{...action('outbox.schedule-save',14),feedbackBoundPassed:undefined}]})]){
  const result=await verdict(report);assert.deepEqual(result.counts,{executed_pass:0,failed:2,not_executed:0,diagnostic:0});assert.equal(result.exitCode,1,'unmeasured or slow local feedback is fatal');checks++;
 }
 // The measured interval decides, not the flag: 0<=feedbackMs<1100.
 const flagged=ms=>rendered({actions:[action('outbox.open',36),{...action('outbox.schedule-save',14),feedbackMs:ms,feedbackBoundPassed:true,ok:true}]});
 for(const [ms,pass] of [[1099.9,true],[0,true],[1100,false],[1100.1,false],[-1,false],[Infinity,false],[-Infinity,false]]){const result=await verdict(flagged(ms));assert.deepEqual(result.counts,pass?{executed_pass:2,failed:0,not_executed:0,diagnostic:0}:{executed_pass:0,failed:2,not_executed:0,diagnostic:0},'feedbackMs '+ms);assert.equal(result.exitCode,pass?undefined:1);checks++;}
 // A saved rendered report can be replayed through the same runner.
 if(process.env.AGENTNET_LATENCY_REPLAY){for(const report of JSON.parse(fs.readFileSync(process.env.AGENTNET_LATENCY_REPLAY,'utf8')).filter(r=>r.actions)){const result=await verdict(report),bad=!!report.feedbackFailures?.length;assert.equal(result.counts.failed,bad?2:0,report.suite+' replay verdict');assert.equal(result.counts.executed_pass,bad?0:2);checks++;}}
 // Named-expectation qualification is decided by the reconciler from each row's own evidence.
 {const os=require('node:os'),cp=require('node:child_process'),dir=fs.mkdtempSync(path.join(os.tmpdir(),'named-'));
  try{const ids=['a','b','c','d','e','f','g','h','i'].map(x=>'agentnet:internal/ui/web/src/X.tsx#'+x+'/button/onClick#1');
   fs.writeFileSync(path.join(dir,'inv.json'),JSON.stringify({controls:ids.map((id,i)=>({definition_id:id,population:'comic-react',source_ref:{path:'internal/ui/web/src/X.tsx',line:i+1}}))}));
   const row=(id,ms,extra={})=>({id:'x',trusted:{assertion_kind:'named-expectation',control_id:id,is_trusted:true,target_match:true,dispatch_ok:true,mixed_synthetic:false,expected_at_input:false,true_before_input:false,handler_fired:true,handler_at:100,input_at:100,visible_at:100+ms,input_to_visible_ms:ms,...extra}});
   const report=(writeDelayMs,actions)=>JSON.stringify({engine:'chromium',suite:'actions',viewport:{width:1440,height:960},writeDelayMs,seenControls:[],firedControlIds:[],actions});
   const lines=[report(0,[row(ids[0],999.9),row(ids[1],1000),row(ids[2],50,{is_trusted:false}),row(ids[3],50,{target_match:false}),row(ids[4],50,{handler_fired:false}),row(ids[5],-1,{visible_at:99}),row(ids[6],50),{id:'y',trusted:{assertion_kind:'named-expectation',control_id:null,status:'unbound'}},row(ids[7],50,{assertion_kind:'generic'}),row(ids[8],50,{expected_at_input:true}),row(ids[8],50,{true_before_input:true}),row(ids[8],50,{dispatch_ok:false}),row(ids[8],50,{mixed_synthetic:true}),row(ids[8],50,{handler_at:151}),row(ids[8],50,{handler_at:99}),row(ids[8],50,{predicate_error:'boom'})]),report(1100,[row(ids[0],10),row(ids[6],1000.1)])];
   fs.writeFileSync(path.join(dir,'runner.log'),lines.join('\n'));
   cp.execFileSync(process.execPath,[path.join(__dirname,'interaction_inventory_reconcile.cjs'),path.join(dir,'inv.json'),path.join(dir,'runner.log'),path.join(dir,'out.json')],{stdio:'ignore'});
   const out=JSON.parse(fs.readFileSync(path.join(dir,'out.json'),'utf8')),t=out.comic_react.total,n=out.comic_react.named_expectations;
   // Only a: 999.9 ms passes in both observed cells. g passes once and fails held (1000.1): not certified.
   assert.equal(t.within_bound,1,'only every-row-qualified definitions count');assert.equal(t.named_attempted,9);assert.equal(t.named_failed,8,'generic-kind rows never qualify');assert.equal(out.comic_react.named_expectations.rows.filter(x=>x.control_id===ids[8]&&x.qualified).length,0,'pre-input truth, missing ack, mixed side effects, predicate errors and handler outside input..visible never qualify');assert.equal(out.comic_react.named_expectations.cells.length,2,'every observed cell is listed');
   assert.deepEqual(n.rows.filter(x=>x.qualified).map(x=>x.control_id),[ids[0],ids[6],ids[0]]);assert.equal(n.unbound,1);assert.equal(n.bound,17);checks++;
  }finally{fs.rmSync(dir,{recursive:true,force:true});}}
 // Library-primitive rows qualify only for source-attributed bindings, from the owned primitive's own transition.
 {const os=require('node:os'),cp=require('node:child_process'),dir=fs.mkdtempSync(path.join(os.tmpdir(),'prim-'));
  try{const A='agentnet:internal/ui/web/src/features/Message.actions.tsx#',M=A+'Toolbar/Menu.Trigger/#1',P=A+'Toolbar/button/#1',C=A+'Confirm/Button/#1',S='agentnet:internal/ui/web/src/ui/Sheet.tsx#Sheet/Dialog.Close/#1',X='agentnet:internal/ui/web/src/X.tsx#x/button/#1',Y='agentnet:internal/ui/web/src/X.tsx#y/button/onClick#1',ids=[M,P,C,S,X,Y];
   fs.writeFileSync(path.join(dir,'inv.json'),JSON.stringify({controls:ids.map((id,i)=>({definition_id:id,population:'comic-react',source_ref:{path:id.split('#')[0].replace('agentnet:',''),line:i+1}}))}));
   const prim=(id,primitive,kind,ms,extra={})=>({id:'p',trusted:{assertion_kind:'primitive-'+kind,control_id:id,primitive,primitive_kind:kind,primitive_binding_ok:true,is_trusted:true,target_match:true,dispatch_ok:true,mixed_synthetic:false,expected_at_input:false,own_at_input:false,true_before_input:false,intervening_inputs:0,owned_popup_preexisting:kind==='open'?false:null,unrelated_changes:0,handler_fired:false,handler_at:null,input_at:100,first_true_at:100+ms,visible_at:100+ms,input_to_visible_ms:ms,...extra}});
   const report=actions=>JSON.stringify({engine:'chromium',suite:'actions',viewport:{width:1440,height:960},writeDelayMs:0,seenControls:[],firedControlIds:[],actions,trusted_interactions:[{control_id:Y,status:'executed-pass'}]});
   const lines=[report([prim(M,'Menu.Trigger','open',66),prim(M,'Menu.Trigger','open',999.9),prim(P,'Popover.Trigger','open',40,{owned_popup_preexisting:true}),prim(S,'Dialog.Close','close',30),prim(S,'Dialog.Close','close',30,{unrelated_changes:1}),
    prim(C,'Dialog.Close','close',30,{first_true_at:null}),prim(C,'Dialog.Close','close',30,{intervening_inputs:1}),prim(C,'Dialog.Close','close',30,{own_at_input:true}),prim(C,'Menu.Trigger','close',30),prim(C,'Dialog.Close','open',30),prim(C,'Dialog.Close','close',30,{primitive_binding_ok:false}),prim(C,'Dialog.Close','close',30,{predicate_error:'own-state: boom'}),prim(C,'Dialog.Close','close',1000),prim(C,'Dialog.Close','close',30,{assertion_kind:'named-expectation'}),
    prim(X,'Menu.Trigger','open',20)])];
   fs.writeFileSync(path.join(dir,'runner.log'),lines.join('\n'));
   cp.execFileSync(process.execPath,[path.join(__dirname,'interaction_inventory_reconcile.cjs'),path.join(dir,'inv.json'),path.join(dir,'runner.log'),path.join(dir,'out.json')],{stdio:'ignore'});
   const out=JSON.parse(fs.readFileSync(path.join(dir,'out.json'),'utf8')),t=out.comic_react.total,n=out.comic_react.named_expectations;
   // Only M: both rows own-state proven <1000 ms. S has an unrelated-dialog row; C fails every variant; X has no source binding.
   assert.equal(t.within_bound,1,'only the fully proven primitive binding counts');assert.equal(t.named_attempted,5);assert.equal(t.named_failed,4);
   assert.deepEqual(n.rows.filter(x=>x.qualified).map(x=>x.control_id),[M,M,S]);assert.equal(n.primitive_rows,14,"the handler-kind-relabelled row is not a primitive row");assert.equal(n.primitive_qualified,3);
   assert.equal(n.rows.filter(x=>x.control_id===C&&x.qualified).length,0,'missing own transition, fallback time, intervening input, own state at input, wrong primitive/kind, unproven binding, predicate error, 1000 ms and handler-kind borrowing never qualify');
   assert.equal(n.rows.filter(x=>x.control_id===X&&x.qualified).length,0,'no primitive kind without a source-attributed binding');
   // Exclusive dispositions: a generic diagnostic pass (Y) neither qualifies nor leaves not_executed; totals reconcile per definition and per cell.
   assert.deepEqual([t.inventory,t.within_bound,t.failed,t.not_executed,t.generic_diagnostic_pass],[6,1,4,1,1]);assert.equal(t.within_bound+t.failed+t.not_executed,t.inventory);
   const d=n.cells[0].definitions;assert.deepEqual([d.within_bound,d.failed,d.not_executed],[1,4,1]);checks++;
  }finally{fs.rmSync(dir,{recursive:true,force:true});}}
 console.log('PASS '+checks+' media probe identity/transport/guard/driver and rendered-verdict, named-expectation and library-primitive checks; raw failures retained, one probe reused, no skip-as-pass');
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
