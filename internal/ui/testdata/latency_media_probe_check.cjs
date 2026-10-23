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
 const engine=options.engine||'webkit',suite=options.suite||'media',platform=options.platform||'win32';
 const args=sample();options.mutate?.(args);
 const browser={version:()=> 'synthetic',close:async()=>{browserClosed++;},newContext:async config=>({close:async()=>{closed++;},newPage:async()=>({on:(name,fn)=>{if(options.driverError&&name==='pageerror')fn(Error('unrelated page error'));},goto:async()=>{},waitForFunction:async()=>{},evaluate:async fn=>{
  const text=String(fn);if(text.includes('window.latencyReport'))return{...clone(args.report),viewport:config.viewport};if(text.includes('innerHTML'))return 'raw original failure DOM';if(text.includes('querySelectorAll'))return[];return{};
 }})})};
 const fakeProcess={platform,env:{AGENTNET_PLAYWRIGHT:'fake-engines',AGENTNET_LATENCY_ENGINES:engine,AGENTNET_LATENCY_SUITES:suite,AGENTNET_LATENCY_VIEWPORTS:'1440x960,390x844',LATENCY_URL:'http://127.0.0.1/latency'}};
 await vm.runInNewContext(fs.readFileSync(path.join(__dirname,'latency_rendered_check.cjs'),'utf8'),{URL,process:fakeProcess,require:name=>name==='fake-engines'?{[engine]:{launch:async()=>browser}}:name==='./latency_media_probe.cjs'?{isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe:async()=>{probeCalls++;return clone(args.probe);}}:require(name),console:{log:s=>log.push(JSON.parse(s)),error:s=>errors.push(s)}});
 assert.equal(closed,2,'each original UI context closed');assert.equal(browserClosed,1,'original browser closed');
 const reports=log.filter(r=>r.suite),counts=log.find(r=>r.qualificationSummary).qualificationSummary;
 assert.equal(reports.length,2);for(const r of reports){assert.equal(r.ok,false,'never skip-as-pass');assert.equal(r.error,args.report.error,'raw error preserved');assert.deepEqual(r.actions,args.report.actions,'partial original actions preserved');}
 return{reports,counts,probeCalls,exitCode:fakeProcess.exitCode,errors};
}
(async()=>{
 const allowed=await driverCase();assert.deepEqual(allowed.counts,{executed_pass:0,failed:0,not_executed:2});assert.equal(allowed.probeCalls,1,'same independent result reused across widths');assert.equal(allowed.exitCode,undefined);assert(allowed.reports.every(r=>r.execution==='not_executed'&&r.unsupportedTestPort.smoothnessQualified===false));checks++;
 for(const options of [{platform:'linux'},{platform:'darwin'},{engine:'chromium'},{engine:'firefox'},{suite:'topics'},{suite:'compose'},{suite:'outbox'},{driverError:true},{mutate:a=>a.report.pageErrors.push('fixture runtime error')},{mutate:a=>a.probe.blob.metadataOk=true},{mutate:a=>a.report.videoMetadataFailure.errorCode=3}]){
  const result=await driverCase(options);assert.equal(result.counts.failed,2);assert.equal(result.counts.not_executed,0);assert.equal(result.exitCode,1,'all other UI failures remain fatal');checks++;
 }
 console.log('PASS '+checks+' media probe identity/transport/guard/driver checks; raw failures retained, one probe reused, no skip-as-pass');
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
