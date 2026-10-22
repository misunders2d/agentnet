const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),{createRequire}=require('node:module');
const web=createRequire(path.resolve(__dirname,'../web/package.json'));
const load=(file)=>{const m={exports:{}};new Function('module','exports',web('esbuild').transformSync(fs.readFileSync(path.resolve(__dirname,'../web/src/features',file),'utf8'),{loader:'ts',format:'cjs',target:'es2022'}).code)(m,m.exports);return m.exports;};
const{videoBitrate}=load('Composer.media-budget.ts');
for(const[d,a,cap,rate]of[[1.5,true,33554432,2500000],[120,true,33554432,1885265],[300,true,33554432,677306],[300,false,33554432,805306],[301,true,33554432,null],[300,true,1048576,null],[0,false,33554432,null],[NaN,true,33554432,null]])assert.equal(videoBitrate(d,a,cap),rate);
const{sendFailure}=load('Message.send-error.ts');
for(const code of['relay_quota','file_too_large','relay_disk_full','local_disk_full','network_unavailable']){const m=sendFailure('send_error:'+code+': Exact producer reason.',false);assert.equal(m.code,code);assert(m.reason&&m.action&&!m.reason.includes('send_error:'));}
assert.equal(sendFailure('quota full',true),null);assert.equal(sendFailure('send_error:unknown: fail',true),null);
assert.equal(sendFailure('send_error:relay_quota: full',true).storage,true);assert.equal(sendFailure('send_error:relay_quota: full',false).storage,false);
assert.match(sendFailure('send_error:relay_disk_full: full',false).action,/admin/);
console.log('media budget: short/120s/300s, audio reservation, 650kbps floor and oversized duration PASS; coded send error classification PASS');

// Drive the real wrapper with a controlled codec module and clock: no
// two-minute sleep, and no permission/provider boundary is mocked away.
const wrapperSource=fs.readFileSync(path.resolve(__dirname,'../web/src/features/Composer.media.ts'),'utf8').replace('import("./Composer.video")','loadVideo()');
const wrapperCode=web('esbuild').transformSync(wrapperSource,{loader:'ts',format:'cjs',target:'es2022'}).code;
function wrapper(loadVideo){const m={exports:{}},timers=[];new Function('module','exports','loadVideo','setTimeout','clearTimeout',wrapperCode)(m,m.exports,loadVideo,(f,ms)=>{timers.push({f,ms,cleared:false});return timers.length-1;},i=>{timers[i].cleared=true;});return{...m.exports,timers};}
const tick=()=>new Promise(setImmediate),file=new File(['source'],'source.mp4',{type:'video/mp4'});
(async()=>{
 let native=0,entered=false;
 const browser=wrapper(async()=>({compressVideo:async(_f,signal)=>{entered=true;return new Promise((_r,j)=>signal.addEventListener('abort',()=>j(signal.reason),{once:true}));}}));
 const first=browser.prepareVideo(file,{prepareVideo:async()=>{native++;throw Error('unexpected native fallback');}},new AbortController().signal,()=>{});await tick();assert(entered);assert.equal(browser.timers[0].ms,120000);browser.timers[0].f();await assert.rejects(first,e=>e.name==='TimeoutError');assert.equal(native,0,'deadline must not start a fresh native work budget');assert(browser.timers[0].cleared);
 let nativeSignal;
 const fallback=wrapper(async()=>({compressVideo:async()=>{throw Error('unsupported codec');}}));
 const second=fallback.prepareVideo(file,{prepareVideo:async(_f,signal)=>{nativeSignal=signal;return new Promise((_r,j)=>signal.addEventListener('abort',()=>j(signal.reason),{once:true}));}},new AbortController().signal,()=>{});await tick();assert(nativeSignal);assert.equal(fallback.timers.length,1);fallback.timers[0].f();await assert.rejects(second,e=>e.name==='TimeoutError');assert(nativeSignal.aborted,'the original deadline cancels native fallback too');
 const lazy=wrapper(()=>new Promise(()=>{})),cancel=new AbortController();
 const waiting=lazy.prepareVideo(file,{},cancel.signal,()=>{});cancel.abort();await assert.rejects(waiting,e=>e.name==='AbortError');assert(lazy.timers[0].cleared,'cancelled lazy import releases queue/deadline');
 assert.equal(lazy.photoDimensions(new Uint8Array([255,216,255,255,255])),null,'truncated JPEG header remains an original file');
 console.log('media deadline: 120s total, no timeout-triggered fallback extension, active native abort and lazy-import cancellation PASS');
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
