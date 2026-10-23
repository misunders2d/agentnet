// Independent decoder/URL discriminator, never an AgentNet playback substitute.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),crypto=require('node:crypto');
const MP4=Object.freeze({size:10580,sha256:'5034aee15c42574db5b6900cd7a2767d6d43db6658c92737fbeea06008870309',type:'video/mp4',contentType:'video/mp4; codecs="avc1.42c00c,mp4a.40.2"'});
const hash=bytes=>crypto.createHash('sha256').update(bytes).digest('hex');
function fixtureMP4(){
 const text=fs.readFileSync(path.join(__dirname,'latency_fixture.mjs'),'utf8');
 const encoded=text.match(/const mp4=new File\(\[Uint8Array.from\(atob\('([A-Za-z0-9+/=]+)'/);
 if(!encoded)throw Error('Independent media probe: exact embedded MP4 missing');
 const bytes=Buffer.from(encoded[1],'base64');
 if(bytes.length!==MP4.size||hash(bytes)!==MP4.sha256)throw Error('Independent media probe: exact MP4 identity changed');
 return bytes;
}
function mediaResponse(bytes,method,range){
 const headers={'Content-Type':MP4.type,'Accept-Ranges':'bytes','Content-Length':String(bytes.length)};
 if(!['GET','HEAD'].includes(method))return{status:405,headers:{Allow:'GET, HEAD','Content-Length':'0'},body:Buffer.alloc(0)};
 let status=200,body=bytes;
 if(range!==undefined){
  const m=/^bytes=(\d*)-(\d*)$/.exec(range);let start=0,end=bytes.length-1;
  if(m&&(m[1]||m[2])){if(m[1]){start=Number(m[1]);if(m[2])end=Math.min(end,Number(m[2]));}else start=Math.max(0,bytes.length-Number(m[2]));}
  if(!m||!(m[1]||m[2])||!Number.isSafeInteger(start)||!Number.isSafeInteger(end)||start<0||start>end||start>=bytes.length){
   return{status:416,headers:{...headers,'Content-Range':'bytes */'+bytes.length,'Content-Length':'0'},body:Buffer.alloc(0)};
  }
  status=206;body=bytes.subarray(start,end+1);headers['Content-Range']=`bytes ${start}-${end}/${bytes.length}`;headers['Content-Length']=String(body.length);
 }
 return{status,headers,body:method==='HEAD'?Buffer.alloc(0):body};
}
const signature=d=>d?.currentSrcScheme==='blob'&&d.errorCode===4&&d.readyState===0&&d.networkState===3;
const empty=a=>Array.isArray(a)&&a.length===0;
const clean=r=>empty(r.pageErrors)&&empty(r.feedbackFailures)&&empty(r.presentationFailures)&&!r.driverError;
function isWindowsMediaCandidate({platform,engine,suite,report,driverErrors}){
 const v=report?.variants?.[0],input=report?.mediaInputs?.[1];
 return platform==='win32'&&engine==='webkit'&&suite==='media'&&report?.suite==='media'&&report.ok===false&&
  report.error?.split('\n')[0]==='baseline: Timed out: actual video metadata'&&signature(report.videoMetadataFailure)&&clean(report)&&empty(driverErrors)&&
  report.variants?.length===1&&v?.ok===false&&v.suite==='media'&&v.portableVector===''&&v.error?.split('\n')[0]==='Timed out: actual video metadata'&&signature(v.videoMetadataFailure)&&clean(v)&&
  report.suiteVectors?.length===1&&report.suiteVectors[0]==='baseline'&&
  input?.sha256===MP4.sha256&&input.size===MP4.size&&input.type===MP4.type&&report.mediaSelectedSource?.container==='mp4'&&
  Array.isArray(report.actions)&&report.actions.length>0&&report.actions.every(a=>a.ok===true&&a.stateVerified===true);
}
function classifyWindowsMedia(args){
 if(!isWindowsMediaCandidate(args))return null;
 const p=args.probe,b=p?.blob,h=p?.http;
 const verified=c=>c?.sourceSHA256===MP4.sha256&&c.sourceSize===MP4.size&&c.fetchSHA256===MP4.sha256&&empty(c.pageErrors)&&!c.probeError;
 if(p?.probeError||p?.source?.sha256!==MP4.sha256||p.source.size!==MP4.size||p.source.type!==MP4.type||p.source.contentType!==MP4.contentType||p.transport?.ok!==true||p.cleanup?.contextClosed!==true||p.cleanup.serverClosed!==true||
  b?.mode!=='blob'||h?.mode!=='http'||!verified(b)||!verified(h)||!signature(b)||b.metadataOk!==false||b.playbackOk!==false||b.metadataError!=='Timed out: actual video metadata')return null;
 if(typeof h.metadataOk!=='boolean'||typeof h.playbackOk!=='boolean'||h.playbackOk&&(!h.metadataOk||h.currentSrcScheme!=='http'||!(h.currentTime>0)||!Number.isFinite(h.duration)||Math.abs(h.duration-1.2)>=.1||h.width!==320||h.height!==180))return null;
 const schemeSupported=h.metadataOk===true&&h.playbackOk===true&&h.currentSrcScheme==='http'&&h.currentTime>0;
 return{execution:'not_executed',reason:'Nonshipping Windows Playwright WebKit media test port: the independent identical-byte blob control reproduced readyState0/networkState3/errorCode4. '+
  (schemeSupported?'HTTP metadata and playback succeeded; source-scheme causality is supported.':'HTTP did not complete metadata/playback either; broader test-port media limitation, not blob-only proof.'),
  causality:schemeSupported?'source_scheme_supported':'broader_test_port_media_limitation',
  fullMediaSurfaceExecuted:false,smoothnessQualified:false,partialActionCount:args.report.actions.length,
  partialObservation:'Only recorded actions before the metadata failure ran; later video/original/file/assignment assertions were not executed. Original ok:false/error/actions are retained.'};
}
async function runMediaProbe(browser){
 const bytes=fixtureMP4(),requests=[];let context;
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://probe');let response;
  if(url.pathname==='/source.mp4')response=mediaResponse(bytes,req.method,req.headers.range);
  else if(url.pathname==='/')response={status:200,headers:{'Content-Type':'text/html'},body:'<!doctype html><meta charset="utf-8"><title>Independent media URL probe</title><video muted playsinline preload="metadata"></video>'};
  else response={status:404,headers:{'Content-Length':'0'},body:''};
  requests.push({method:req.method,path:url.pathname,range:req.headers.range??null,status:response.status,contentLength:response.headers['Content-Length']??null,contentRange:response.headers['Content-Range']??null});
  res.writeHead(response.status,response.headers);res.end(response.body);
 });
 const result={source:{...MP4},requests,cleanup:{contextClosed:false,serverClosed:false}};
 try{
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  const origin='http://127.0.0.1:'+server.address().port;
  const head=await fetch(origin+'/source.mp4',{method:'HEAD'}),range=await fetch(origin+'/source.mp4',{headers:{Range:'bytes=0-31'}}),part=Buffer.from(await range.arrayBuffer());
  result.transport={headStatus:head.status,headLength:head.headers.get('content-length'),rangeStatus:range.status,contentRange:range.headers.get('content-range'),rangeExact:part.equals(bytes.subarray(0,32))};
  result.transport.ok=head.status===200&&result.transport.headLength===String(MP4.size)&&range.status===206&&result.transport.contentRange===`bytes 0-31/${MP4.size}`&&result.transport.rangeExact;
  if(!result.transport.ok)throw Error('Independent media probe: HEAD/Range transport invalid');
  context=await browser.newContext();
  for(const mode of ['blob','http']){
   const page=await context.newPage(),pageErrors=[];page.on('pageerror',e=>pageErrors.push(String(e).slice(0,500)));
   try{
    await page.goto(origin,{timeout:5000});
    result[mode]=await page.evaluate(async({mode,source})=>{
     const v=document.querySelector('video'),events=[],started=performance.now();let ownedURL;
     const digest=async bytes=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes))).map(b=>b.toString(16).padStart(2,'0')).join('');
     const state=()=>({readyState:v.readyState,networkState:v.networkState,errorCode:v.error?.code??null,errorMessage:(v.error?.message||'').slice(0,500),currentSrcScheme:v.currentSrc.match(/^([a-z][a-z0-9+.-]*):/i)?.[1]||'',duration:Number.isFinite(v.duration)?v.duration:String(v.duration),width:v.videoWidth,height:v.videoHeight,currentTime:v.currentTime,elapsedMs:performance.now()-started});
     const r={mode,events,canPlayType:v.canPlayType(source.contentType),metadataOk:false,playbackOk:false};
     for(const type of ['loadstart','loadedmetadata','loadeddata','canplay','playing','error','stalled','abort'])v.addEventListener(type,()=>events.push({type,atMs:performance.now()-started,...state()}));
     const until=async(f,label)=>{const end=performance.now()+5000;while(!f()){if(performance.now()>end)throw Error('Timed out: '+label);await new Promise(resolve=>setTimeout(resolve,10));}};
     try{
      const response=await fetch('/source.mp4');if(!response.ok)throw Error('Source fetch HTTP '+response.status);
      const bytes=await response.arrayBuffer();r.sourceSize=bytes.byteLength;r.sourceSHA256=await digest(bytes);
      if(r.sourceSize!==source.size||r.sourceSHA256!==source.sha256)throw Error('Source byte identity mismatch');
      const input=mode==='blob'?(ownedURL=URL.createObjectURL(new Blob([bytes],{type:source.type}))):new URL('/source.mp4',location.href).href;
      const fetched=await fetch(input);if(!fetched.ok)throw Error('Control fetch HTTP '+fetched.status);r.fetchSHA256=await digest(await fetched.arrayBuffer());
      if(r.fetchSHA256!==source.sha256)throw Error('Control byte identity mismatch');
      v.muted=true;v.src=input;
      try{await until(()=>v.readyState>=1,'actual video metadata');}catch(e){r.metadataError=String(e.message||e);return Object.assign(r,state());}
      if(!Number.isFinite(v.duration)||Math.abs(v.duration-1.2)>=.1||v.videoWidth!==320||v.videoHeight!==180)throw Error('Exact source metadata mismatch');
      r.metadataOk=true;let playError;void v.play().catch(e=>{playError=String(e.message||e).slice(0,500);});
      try{await until(()=>v.currentTime>0||playError,'actual portable video playback');if(playError)throw Error(playError);r.playbackOk=true;}catch(e){r.playbackError=String(e.message||e);}
      return Object.assign(r,state());
     }catch(e){r.probeError=String(e.message||e);return Object.assign(r,state());}
     finally{v.pause();if(ownedURL)URL.revokeObjectURL(ownedURL);}
    },{mode,source:MP4});
    result[mode].pageErrors=pageErrors;
   }finally{await page.close();}
  }
  return result;
 }finally{
  try{await context?.close();result.cleanup.contextClosed=true;}finally{server.closeAllConnections();await new Promise(resolve=>server.close(resolve));result.cleanup.serverClosed=!server.listening;}
 }
}
module.exports={MP4,fixtureMP4,mediaResponse,isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe};
