// Actual Store + Composer guard over DTOs from signed temporary group requests.
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const root=pathToFileURL(process.env.AGENTNET_SOURCE+'/internal/ui/');
registerHooks({load(url,context,next){
 if(url.includes('/web/src/')&&url.endsWith('.ts'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;'))};
 return next(url,context);
},resolve(specifier,context,next){
 const parent=context.parentURL||'';
 if(parent.includes('/web/src/')){
  if(specifier==='react')return next('data:text/javascript,export const useSyncExternalStore=()=>{};',context);
  if(specifier==='./skin/brand')return next('data:text/javascript,export const skin={storage:"agentnet.messenger"};',context);
  if(specifier==='./optimistic.mjs')return next(new URL('static/optimistic.mjs',root).href,context);
  if(specifier==='./person-topics.mjs')return next(new URL('skins/shared/person-topics.mjs',root).href,context);
  if(specifier==='./linkText')return next(new URL('skins/shared/link-text.mjs',root).href,context);
  if(specifier.startsWith('.')&&!/\.(ts|tsx|mjs|js)$/.test(specifier))return next(new URL(specifier+'.ts',parent).href,context);
 }
 return next(specifier,context);
}});
const {Store}=await import(new URL('web/src/store.ts',root));
const {currentComposerFollowup}=await import(new URL('web/src/features/Composer.followup.ts',root));
const saved=new Map();globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>saved.set(k,v)};
const fixture=JSON.parse(await new Promise(resolve=>{let raw='';process.stdin.setEncoding('utf8');process.stdin.on('data',s=>raw+=s);process.stdin.on('end',()=>resolve(raw));}));
for(const c of fixture.cases){
 let sends=0;
 const host={platform:'daemon',workspace:{id:c.label},listen:()=>()=>{},api:async(path,body)=>{
  if(body&&path==='/api/request/followup')sends++;
  if(path.startsWith('/api/overview'))return c.overview;
  if(path.startsWith('/api/dm?'))return c.thread;
  if(path.startsWith('/api/typing'))return {entries:[]};
  if(path.startsWith('/api/agents'))return {agents:[]};
  return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'dm',id:c.thread.id});
 const draft='[@Your Pi](agentnet:agent/'+fixture.pid+') and what is weather tomorrow?';
 store.setDraft(c.thread.id,{text:draft});
 const dm=store.get().dm,ctx={conv:dm.id,dm,thread:null,overview:store.get().overview,names:{},canReply:true};
 const m=dm.messages.find(m=>m.lid===fixture.original);
 assert.equal(m.exec.state,c.state,c.label+' keeps exact current state');
 if(c.attempt){
  const bound=currentComposerFollowup(dm,fixture.pid,'',ctx);
  assert.ok(bound,c.label+' must steer exact running request');
  assert.equal(bound.attempt,c.attempt,c.label+' keeps actual local attempt');
  assert.equal(bound.ref.id,fixture.original);assert.equal(bound.ref.fingerprint,fixture.author);
  for(const changed of [{exec:{...m.exec,attempt:0}},{target:{...m.target,fingerprint:'0'.repeat(8)+'-'+('0'.repeat(8)+'-').repeat(2)+'0'.repeat(8)}},{exec:{...m.exec,host:'different/host'}},{topic:'1'.repeat(32)}]){
   const wrong={...dm,messages:dm.messages.map(x=>x===m?{...m,...changed}:x)},wrongCtx={...ctx,dm:wrong};
   if(changed.topic)assert.equal(currentComposerFollowup(wrong,fixture.pid,'',wrongCtx),undefined);
   else assert.throws(()=>currentComposerFollowup(wrong,fixture.pid,'',wrongCtx),/cannot be verified/);
  }
 }else if(c.state==='running')assert.throws(()=>currentComposerFollowup(dm,fixture.pid,'',ctx),/cannot be verified/,c.label+' must refuse unverifiable attempt');
 else assert.equal(currentComposerFollowup(dm,fixture.pid,'',ctx),undefined,c.label+' never re-labels old running status');
 assert.equal(store.draft(dm.id).text,draft);assert.equal(sends,0,'model never queues a fallback');store.stop();
 console.log(c.label+' exact attempt/state through Store and Composer PASS');
}

// Direct device threads have no participation or conversation hash.
const directModule=await import(new URL('web/src/features/Composer.followup.ts',root));
assert.equal(typeof directModule.currentDirectComposerFollowup,'function','direct composer selector exists for both send paths');
const directAuthor='aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa',directHostKey='bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb';
const directID='a'.repeat(32),directPeer='admin/host';
const directOverview={me:{address:'admin/phone',fingerprint:directAuthor},people:[],agents:[]};
const directMessage={id:directID,from:'admin/phone',from_key:directAuthor,to:directPeer,dir:'out',kind:'question',body:'Original request',author:{label:'You',about:''},exec:{state:'running',host:directPeer,attempt:1}};
const directThread={id:directID,peer:directPeer,key:{pinned:directHostKey},messages:[directMessage]};
const directContext=t=>({conv:t.id,dm:null,thread:t,overview:directOverview,names:{},canReply:true});
const selectDirect=t=>directModule.currentDirectComposerFollowup(t,directContext(t));
assert.equal(selectDirect(directThread).ref.id,directID);
for(const [patch,label] of [[{exec:{...directMessage.exec,attempt:0}},'attempt'],[{exec:{...directMessage.exec,stale:true}},'stale'],[{exec:{...directMessage.exec,host:'other/host'}},'host'],[{target:{address:directPeer,fingerprint:directAuthor}},'key']]){
 assert.throws(()=>selectDirect({...directThread,messages:[{...directMessage,...patch}]}),/cannot be verified/,label);
}
assert.equal(selectDirect({...directThread,messages:[{...directMessage,exec:{...directMessage.exec,state:'done'}}]}),undefined);
const secondDirect={...directMessage,id:'d'.repeat(32)};
const ambiguous={...directThread,messages:[directMessage,secondDirect]};
assert.throws(()=>selectDirect(ambiguous),/multiple running requests/);
assert.equal(directModule.currentDirectComposerFollowup(ambiguous,directContext(ambiguous),secondDirect.id).ref.id,secondDirect.id,'existing exact Reply disambiguates');
assert.throws(()=>selectDirect({...directThread,key:{pinned:directHostKey,pending:directAuthor}}),/cannot be verified/);
assert.throws(()=>selectDirect({...directThread,messages:[directMessage,{...directMessage,exec:{...directMessage.exec,attempt:2}}]}),/cannot be verified/);
const namedID='c'.repeat(32),otherNamed='e'.repeat(32);
assert.throws(()=>selectDirect({...directThread,messages:[{...directMessage,target:{address:directPeer,fingerprint:directHostKey,agent_id:namedID}},{...secondDirect,target:{address:directPeer,fingerprint:directHostKey,agent_id:otherNamed}}]}),/cannot be verified/,'same display names never bridge named identities');
console.log('direct selector default identity and refusal checks PASS');

// Execute the actual desktop and Android send functions with the same
// minimal host used by the Store checks above; no generated skin/browser.
const {runInNewContext}=await import('node:vm');
const journal=await import(new URL('web/src/send-journal.mjs',root));
const composerSource=readFileSync(new URL('web/src/features/Composer.tsx',root),'utf8');
const sendSource=process.env.AGENTNET_COMPOSER_SEND_SOURCE?readFileSync(process.env.AGENTNET_COMPOSER_SEND_SOURCE,'utf8'):composerSource;
const desktopSource=sendSource.match(/^  async function send\([\s\S]*?^  }/m)?.[0];
const androidSource=sendSource.match(/^  async function sendAndroid\([\s\S]*?^  }/m)?.[0];
const selectorSource=composerSource.match(/^  const selectedTargetFollowup = [\s\S]*?;$/m)?.[0];
assert.ok(desktopSource&&androidSource&&selectorSource,'actual send paths and shared destination selector');
async function directSend(platform,{named=false,fail=false,newTopic=false,ended=false,invalid=false,projection,refusal}={}) {
 const calls=[],previews=[],notices=[],savedRows=new Map();let cleared=0,serial=0,retry;
 const storage={getItem:k=>savedRows.get(k)||null,setItem:(k,v)=>savedRows.set(k,v)};
 const target={kind:'device',ask:true,name:'Selected agent',seed:directPeer};
 const namedTarget=named?{address:directPeer,fingerprint:directHostKey,agent_id:'c'.repeat(32)}:undefined;
 const original=projection?projection.thread.messages.find(m=>m.id===projection.original):directMessage;
 const message=projection?structuredClone(original):{...directMessage,target:namedTarget,exec:{...directMessage.exec,state:ended?'done':'running',attempt:invalid?0:1}};
 const thread=projection?structuredClone(projection.thread):{...directThread,messages:[message]};
 if(projection){thread.messages=thread.messages.map(m=>m.id===message.id?message:m);if(ended)message.exec={...message.exec,state:'done'};if(invalid)message.exec={...message.exec,attempt:0};}
 if(refusal==='pending')thread.key={...thread.key,pending:directAuthor};
 if(refusal==='key')message.target={...message.target,address:thread.peer,fingerprint:directAuthor};
 if(refusal==='stale')message.exec={...message.exec,stale:true};
 const overview=projection?.overview||directOverview;
 const draft={text:'Use English instead',newTopic,files:[{key:'terms',name:'terms.md',size:1,file:{}}]};
 const state={open:'selected-view',overview,drafts:{[thread.id]:draft}};
 const api=async(endpoint,request)=>{calls.push({endpoint,request:JSON.parse(JSON.stringify(request))});if(fail)throw Error('offline');return {id:request.id,state:'queued',note:'accepted for transport'};};
 const store={host:{platform,workspace:{id:platform},api},get:()=>state,isActive:()=>true,draft:()=>state.drafts[thread.id],setDraft:(_,d)=>state.drafts[thread.id]=d,
 api:{stage:async()=> 'staged-terms',send:request=>api('/api/send',request),discard:async()=>{}},toast:()=>{},refetch:async()=>{},open:async()=>{},
 sends:{begin:(_,m,r)=>{previews.push(m);retry=r;},ready:async()=>{},finish:()=>{},remove:()=>{},move:()=>{},has:()=>true,fail:()=>{}}};
 const env={store,thread,dm:undefined,conv:thread.id,ready:true,latest:()=>draft,target,overview,names:{},words:{},visible:{current:thread.id},EMPTY:{text:''},
 followupContext:{...directContext(thread),overview},currentDirectComposerFollowup:directModule.currentDirectComposerFollowup,selectedFollowup:()=>{throw Error('invited selector used for direct chat');},
 setNotice:n=>{if(n)notices.push(n.text);},setBusy:()=>{},clearSent:()=>{cleared++;state.drafts[thread.id]={text:''};},typing:{stop:()=>{}},
 sendID:()=> (++serial).toString(16).padStart(32,'0'),decode:text=>({text,spans:[]}),encode:text=>text,
 releaseFiles:()=>{},keepStaged:()=>{},forgetStaged:()=>{},announce:()=>{},errorText:e=>e.message,messageOf:()=>message,
 threadAgentID:t=>t.messages[0].target?.agent_id,androidPreparing:{current:new Set()},saveBatch:journal.saveBatch,acknowledge:journal.acknowledge,recordFailure:journal.recordFailure,localStorage:storage};
 runInNewContext(stripTypeScriptTypes(selectorSource)+'; globalThis.boundSelector = selectedTargetFollowup;',env);env.selectedTargetFollowup=env.boundSelector;
 const send=runInNewContext('('+stripTypeScriptTypes(desktopSource)+')',env);
 env.sendAndroid=runInNewContext('('+stripTypeScriptTypes(androidSource)+')',env);
 await send();
 if((invalid||refusal)&&!newTopic){assert.equal(calls.length,0);assert.equal(cleared,0);assert.match(notices.join(' '),/cannot be verified/);return;}
 assert.equal(calls.length,1,platform+' submits one request');
 assert.equal(calls[0].endpoint,newTopic||ended?'/api/send':'/api/request/followup',platform+' routes ordinary direct send');
 if(!newTopic&&!ended){assert.equal(calls[0].request.conv,'');assert.equal(calls[0].request.ref.id,projection?.original||directID);assert.equal(calls[0].request.ref.fingerprint,projection?.author||directAuthor);assert.equal(calls[0].request.attempt,original.exec.attempt);assert.deepEqual(calls[0].request.files,['staged-terms']);}
 if(fail&&platform==='android'){
  const entries=journal.readPending(storage,platform);assert.equal(entries.length,1);assert.deepEqual(entries[0].request,calls[0].request,'journal freezes exact intent and stages');
  assert.equal(entries[0].endpoint,'/api/request/followup');
 }else if(fail){
  assert.equal(typeof retry,'function');retry();await new Promise(r=>setTimeout(r,0));
  assert.equal(calls.length,2);assert.deepEqual(calls[1],calls[0],'desktop retry retains exact ID/ref/attempt/files');
 }else if(platform==='android') assert.equal(journal.readPending(storage,platform).length,0);
}
for(const platform of process.env.AGENTNET_COMPOSER_PLATFORM?[process.env.AGENTNET_COMPOSER_PLATFORM]:['daemon','android'])for(const opts of [{},{named:true},{fail:true},{newTopic:true},{ended:true},{invalid:true}])await directSend(platform,opts);
console.log('actual desktop/Android direct send, named/default, files, offline exact retry/journal, new-topic/no-run and invalid draft retention PASS');

// These original DTOs come unchanged from the linked human's actual Live.Thread.
const failures=[];
for(const projection of fixture.linked||[]){
 const m=projection.thread.messages.find(m=>m.id===projection.original);
 console.log(JSON.stringify({label:projection.label,id:m.id,dir:m.dir,from:m.from,from_key:m.from_key,history:m.history,exec:m.exec,key:projection.thread.key,target:m.target}));
 assert.equal(m.from_key,projection.author,'original author fingerprint survives native linked projection');
 assert.equal(projection.thread.key.pinned,projection.host_key,'B independently pins the current selected host');
 for(const platform of ['daemon','android']){
  try{
   for(const opts of [{},{fail:true},{refusal:'pending'},{refusal:'key'},{refusal:'stale'},{invalid:true},{ended:true}])await directSend(platform,{projection,...opts});
   console.log(projection.label+' '+platform+' actual linked DTO routing, exact intent and refusal/no-active controls PASS');
  }catch(e){failures.push(projection.label+' '+platform+': '+e.message);console.log('FAIL '+failures.at(-1));}
 }
}
assert.deepEqual(failures,[],'production-derived linked direct projection must steer in both current send paths');
