// Actual production label, focused-region and catalog-name refresh code. No
// browser, native process, current-default lookup, or continuation is started.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const esbuild=require('../web/node_modules/esbuild');
const base=process.env.AGENTNET_LABEL_SOURCE||path.join(__dirname,'../web/src');
const source=file=>fs.readFileSync(path.join(base,file),'utf8');
const jsx=(tag,props,...children)=>({tag,props:{...props,children:children.flat(Infinity).filter(x=>x!==false&&x!=null)}});
const nodes=t=>t&&typeof t==='object'?[t,...(t.props?.children||[]).flatMap(nodes)]:[];
const text=t=>t&&typeof t==='object'?(t.props?.children||[]).map(text).join(''):String(t??'');
function load(file,globals,extra=''){
 const env={module:{exports:{}},jsx,Fragment:'Fragment',Date,Map,Set,console,...globals};env.exports=env.module.exports;
 vm.runInNewContext(esbuild.transformSync(source(file).replace(/^import[\s\S]*?from[^;]+;/gm,'')+extra,{loader:file.endsWith('tsx')?'tsx':'ts',jsxFactory:'jsx',jsxFragment:'Fragment',format:'cjs'}).code,env);
 return env.module.exports;
}
const model=load('model.ts',{collectivePlain:s=>s,previewText:s=>s}),message=load('features/Message.model.ts',model);
const view=load('features/Message.tsx',{memo:f=>f,historicalTargetLabel:message.historicalTargetLabel,Markdown:'Markdown',deviceWords:h=>h,requestResultText:s=>s});
const host='atlas/host',fingerprint='11111111-11111111-11111111-11111111',pi='a'.repeat(32),claude='b'.repeat(32),current='c'.repeat(32);
const names={[pi]:'Pi',[pi+'#host']:host,[pi+'#host_key']:fingerprint,[pi+'#harness']:'claude',[claude]:'Claude',[claude+'#host']:host,[claude+'#host_key']:fingerprint,[current]:'Current default'};
const ctx={conv:'d'.repeat(64),names,dm:{agents:[]},thread:null,overview:{me:{address:'atlas/laptop',responder:'codex'}}};
const target=id=>({address:host,fingerprint,agent_id:id});
const original={id:'e'.repeat(32),pid:'original-pid',dir:'out',kind:'question',target:target(pi),exec:{state:'needs_human',attempt:3,host},continuation:{host,id:'f'.repeat(32),key:fingerprint,attempt:3}};
const before=JSON.stringify(original),region=m=>view.AgentNeedsHumanTurn({m,ctx,detail:'Full original reason\nSecond paragraph'});
const piTree=region(original),claudeTree=region({...original,target:target(claude)});
assert.equal(piTree.props.tabIndex,-1,'the detached attention region remains focusable');
assert.equal(piTree.props['aria-label'],'Pi on '+host+' says');assert.equal(claudeTree.props['aria-label'],'Claude on '+host+' says');
assert.match(text(piTree),/^Pi on atlas\/host couldn’t finish/);assert.match(text(claudeTree),/^Claude on atlas\/host couldn’t finish/);
assert(!text(piTree).includes('Claude'),'historical named request does not borrow a changed current harness');
assert.equal(nodes(piTree).find(n=>n.tag==='Markdown').props.text,'Full original reason\nSecond paragraph');
assert.equal(JSON.stringify(original),before,'presentation never changes target or continuation tuple');
for(const changed of [{[pi+'#host_key']:'changed-key'},{[pi+'#host']:'another/host'},{[pi+'#host_key']:undefined}]){
 const label=message.historicalTargetLabel(target(pi),{...names,...changed});assert.equal(label,'Agent aaaaaaaa','a historical pin mismatch never borrows a catalog name');
}
assert.equal(message.historicalTargetLabel(target('9'.repeat(32)),names),'Agent 99999999');
assert.equal(message.historicalTargetLabel({address:host,fingerprint},names),'Earlier default · identity unknown');
assert.equal(message.historicalTargetLabel(undefined,names),'Earlier default · identity unknown');

// The independently focused Review card must name its target even when two
// requests share the same group, rather than relying on a separate To caption.
let reviewNames=names;const opened=[];
const review=load('features/Approvals.conv.tsx',{...model,historicalTargetLabel:message.historicalTargetLabel,useAgentNames:()=>reviewNames,
 useApp:()=>({run:()=>assert.fail('rendering never acts')}),useLand:()=>()=>{},useState:initial=>[initial,()=>{}],
 useOpen:()=>({isOpen:()=>false,go:(...args)=>opened.push(args)}),OpenCard:'OpenCard',Body:'Body',AgentAvatar:'AgentAvatar',PersonAvatar:'PersonAvatar',
 Button:'Button',Tag:'Tag',ConfirmSheet:'ConfirmSheet',DeclineSheet:'DeclineSheet',ResolveOwnRequest:'ResolveOwnRequest',
 deviceWords:h=>h,whyWords:s=>s,inChat:()=> 'in One shared group',capital:s=>s,kindTag:()=> 'Question',
},'\nexport { RequestRow };');
const o={...ctx.overview,needs_you:[],dms:[{id:ctx.conv,title:'One shared group'}]},item=id=>({conv:ctx.conv,id:id==='a'.repeat(32)?'pi-request':'claude-request',target:target(id),reason:model.Reason.needsHuman,peer:host,kind:'question',why:'Full original explanation',excerpt:'Same question',at:'2026-10-20T09:00:00Z',actions:[]});
o.needs_you=[item(pi),item(claude)];
const reviewRow=c=>review.RequestRow({c,o}),openCard=t=>nodes(t).find(n=>n.tag==='OpenCard');
for(const [id,label] of [[pi,'Pi'],[claude,'Claude']]){
 const c=item(id),tree=reviewRow(c),card=openCard(tree);assert.equal(card.props.label,label+' on '+host+' couldn’t finish — it needs your attention. Open it in the chat.');
 assert(text(tree).includes(label+' on '+host+' says:'),'said-prefix is exact even outside its caption');card.props.onOpen();
}
assert.deepEqual(opened,[['dm',ctx.conv,'pi-request'],['dm',ctx.conv,'claude-request']]);
assert.match(openCard(reviewRow({...item(pi),reason:model.Reason.interrupted})).props.label,/^Pi on atlas\/host was interrupted/);
reviewNames={...names,[pi+'#host_key']:'replacement-key'};assert.match(openCard(reviewRow(item(pi))).props.label,/^Agent aaaaaaaa on /,'Review rejects a replacement catalog key');
reviewNames={...names,[claude]:'Pi'};
for(const [id,short] of [[pi,'aaaaaaaa'],[claude,'bbbbbbbb']])assert(openCard(reviewRow(item(id))).props.label.includes('Pi · '+short),'same-name instances remain distinguishable');
reviewNames=names;assert.match(openCard(reviewRow({...item(pi),target:{address:host,fingerprint}})).props.label,/^Earlier default · identity unknown on /);

// Execute the existing Store method so refresh/removal/rekey assertions cover
// the metadata producer as well as the display consumer.
const storeSource=source('store.ts'),start=storeSource.indexOf('  private async loadAgentNames()'),end=storeSource.indexOf('\n  // ---- drafts',start);
assert(start>=0&&end>start,'actual name hydration method exists');
const harness=`export class Harness { namesLoading=false; state; api; changes=0; constructor(state,api){this.state=state;this.api=api;} set(patch){this.changes++;Object.assign(this.state,patch);} ${storeSource.slice(start,end)} }`;
const env={module:{exports:{}},Promise,Map,Set,currentCatalogAgents:model.currentCatalogAgents};env.exports=env.module.exports;
vm.runInNewContext(esbuild.transformSync(harness,{loader:'ts',format:'cjs'}).code,env);
(async()=>{
 const state={agentNames:{...names},dm:{agents:[{host:{address:host}}]},overview:{me:{address:'atlas/laptop'},threads:[]}};
 let records=[{record:{id:pi,host,host_key:fingerprint,label:'Pi',harness:'claude'},enabled:true}],reads=[];
 const h=new env.module.exports.Harness(state,{agents:async address=>{reads.push(address);return {host:address||'atlas/laptop',agents:address?records:[]};}});
 await h.loadAgentNames();assert.equal(message.historicalTargetLabel(target(pi),state.agentNames),'Pi');assert.deepEqual(reads,[undefined,host],'metadata shares the existing catalog fetch');
 records=[{record:{id:pi,host,host_key:'replacement-key',label:'Replacement named agent',harness:'codex'},enabled:true}];await h.loadAgentNames();
 assert.equal(message.historicalTargetLabel(target(pi),state.agentNames),'Agent aaaaaaaa','a replaced host key cannot relabel a historical request');
 const changes=h.changes;records=[];await h.loadAgentNames();assert(h.changes>changes,'deleting stale key metadata publishes the new name map');
 assert.equal(state.agentNames[pi+'#host_key'],undefined);assert.equal(message.historicalTargetLabel({...target(pi),fingerprint:'replacement-key'},state.agentNames),'Agent aaaaaaaa','removed record cannot retain its old proof');
 console.log('PASS actual focused attention labels: same-group Pi/Claude, exact original ID/host pin, changed harness/key, unknown legacy, full detail/continuation, existing catalog refresh only');
})().catch(error=>{console.error(error);process.exitCode=1;});
