// The actual Store under scoped change events (GET /events "scope"): another chat's
// change leaves the open chat as it is; its own, no chat's, or an unscoped one reads it.
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
// Store.start listens for other windows' draft writes (B6); this check has one window.
globalThis.window??={addEventListener(){},removeEventListener(){}};
const {Store,unsentIn}=await import(new URL('web/src/store.ts',root));
const saved=new Map();globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>saved.set(k,v)};
globalThis.document={visibilityState:'visible',hasFocus:()=>true,addEventListener(){},removeEventListener(){}};

const pause=ms=>new Promise(r=>setTimeout(r,ms));
const conv='a'.repeat(64),old='d'.repeat(64),other='c'.repeat(64);
const me={address:'fixture/laptop',fingerprint:'11111111-11111111-11111111-11111111',responder:'',responder_dir:'',agent:false};
async function world(scope){
 let fn,dm=0,overview=0;
 const host={platform:'daemon',workspace:{id:Math.random()+''},listen:f=>{fn=f;return ()=>{};},api:async path=>{
  if(path.startsWith('/api/overview')){overview++;return {version:'fixture',me,groups:true,dms:[],threads:[],people:[]};}
  if(path.startsWith('/api/dm?')){dm++;return {id:conv,kind:'dm',peer:{},messages:[],agents:[],guests:[],topics:[],...(scope?{scope}:{})};}
  if(path.startsWith('/api/agents'))return {agents:[]};if(path.startsWith('/api/typing'))return {entries:[]};return {};
 }};
 const store=new Store(host);await store.start();await store.open({kind:'dm',id:conv});await pause(50);
 let seq=10;
 const send=async keys=>{dm=0;overview=0;fn({type:'change',seq:++seq,...(keys?{keys}:{})});await pause(50);return {dm,overview};};
 return {store,send};
}
{
 const w=await world(['c:'+conv,'c:'+old]);
 assert.deepEqual(await w.send(['c:'+other]),{dm:0,overview:1},'another chat moved: the list only');
 assert.deepEqual(await w.send([]),{dm:1,overview:1},'no keys: everything');
 assert.deepEqual(await w.send(['c:'+conv]),{dm:1,overview:1},'the open chat moved');
 assert.deepEqual(await w.send(['c:'+other,'c:'+old]),{dm:1,overview:1},'its continuity family moved');
 assert.deepEqual(await w.send(['h','c:'+other]),{dm:0,overview:1},'history-copy progress is the list\'s');
 for(const k of ['*','p:peer'])assert.deepEqual(await w.send([k]),{dm:1,overview:1},k+' is no one chat');
 assert.deepEqual(await w.send(undefined),{dm:1,overview:1},'an unscoped change reads everything');
 w.store.stop();
}
{
 const w=await world(undefined);
 assert.deepEqual(await w.send(['c:'+other]),{dm:1,overview:1},'a view that does not say its scope reads again');
 w.store.stop();
}
console.log('scoped change events read again only the views they touch PASS');
