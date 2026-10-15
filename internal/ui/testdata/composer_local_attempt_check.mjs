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
