// Execute the actual collapsed manager and callbacks; no browser or native
// process, catalog-name inference, job, session change or fixture-only UI.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const esbuild=require('../web/node_modules/esbuild'),base=path.join(__dirname,'../web/src');
const jsx=(tag,props,...children)=>({tag,props:{...props,children:children.flat(Infinity).filter(x=>x!==false&&x!=null)}});
const nodes=t=>t&&typeof t==='object'?[t,...(t.props?.children||[]).flatMap(nodes)]:[];
const words=t=>t&&typeof t==='object'?(t.props?.children||[]).map(words).join(''):String(t??'');
function load(file,globals){const env={module:{exports:{}},jsx,Fragment:'Fragment',Map,Set,Promise,...globals};env.exports=env.module.exports;vm.runInNewContext(esbuild.transformSync(fs.readFileSync(path.join(base,file),'utf8').replace(/^import[\s\S]*?from[^;]+;/gm,''),{loader:'tsx',jsxFactory:'jsx',jsxFragment:'Fragment',format:'cjs'}).code,env);return env.module.exports;}
const host='owner/laptop',key='host-key',a='a'.repeat(32),b='b'.repeat(32),pi='c'.repeat(32),unknown='d'.repeat(32);
const entry=(id,harness,dir,enabled=true)=>({record:{id,host,host_key:key,label:id===b?'Selected saved setup':harness||'Unidentified saved setup',harness},enabled,responder:enabled?{harness,dir,ready:true}:null});
let catalog={host,local:true,harnesses:[{name:'codex',found:true},{name:'pi',found:true},{name:'claude',found:false}],agents:[entry(a,'codex','/first'),entry(b,'codex','/selected'),{...entry(pi,'pi','/missing'),responder:{harness:'pi',dir:'/missing',ready:false,problem:'Folder unavailable'}},entry(unknown,undefined,undefined,false)]};
let values=[],slot=0,active=true,saved=0,refreshed=0,pointer='',calls=[];
const store={isActive:()=>active,get:()=>({overview:{me:{address:host,fingerprint:key}}}),refetch:async()=>{refreshed++;},api:{
 setResponder:async body=>{calls.push(['current',JSON.parse(JSON.stringify(body))]);const selected=catalog.agents.find(x=>x.record.id===body.agent_id&&x.enabled);assert(selected,'exact enabled ID required');pointer=selected.record.id;for(const x of catalog.agents){x.default=x===selected;if(x!==selected&&x.record.harness===selected.record.harness){x.enabled=false;x.responder=null;}}return {};},
 changeAgents:async body=>{calls.push(['change',JSON.parse(JSON.stringify(body))]);if(body.action==='publish')return {published:true};const x=catalog.agents.find(x=>x.record.id===body.id);assert(x,'exact existing ID required');if(body.action==='disable'){x.enabled=false;x.responder=null;x.default=false;}else{assert.equal(body.action,'update','manager never creates identities');assert(body.harness&&body.dir.startsWith('/'),'vanished configuration requires explicit fields');x.enabled=true;x.record.harness=body.harness;x.responder={harness:body.harness,dir:body.dir,ready:true};}return {published:true};},
}};
const hooks={useApp:()=>store,useStore:(_s,select)=>select(store.get()),useState:initial=>{const i=slot++;if(!(i in values))values[i]=initial;return [values[i],value=>{values[i]=typeof value==='function'?value(values[i]):value;}];}};
const manager=load('features/AgentSetupManager.tsx',{...hooks,errorText:e=>e.message||String(e),Button:'Button',Details:'Details',AgentSheet:'AgentSheet',harnessName:h=>h==='codex'?'Codex':h==='pi'?'Pi':h});
const render=()=>{slot=0;return manager.AgentSetupManager({catalog,onSaved:async()=>{saved++;}});};
const row=(tree,id)=>nodes(tree).find(n=>n.props['data-agent-setup']===id);
const button=(tree,label)=>nodes(tree).find(n=>n.tag==='Button'&&words(n)===label);
const flush=()=>new Promise(setImmediate);
(async()=>{
 let tree=render();assert.equal(tree.tag,'Details');assert.equal(tree.props.label,'Manage saved setups');assert.equal(tree.props.open,undefined,'global manager stays collapsed by the existing Details control');
 assert(!nodes(tree).some(n=>/Chat/.test(String(n.tag))),'management never offers chat recipients');
 assert(words(tree).includes('Folder unavailable'));assert(words(tree).includes('Program not recorded'));assert(words(tree).includes('Folder not available; choose it when enabling.'));
 button(row(tree,b),'Use as current').props.onClick();await flush();
 assert.equal(pointer,b,'the clicked exact record wins over the first same-program setup');
 assert.deepEqual(calls,[['current',{agent_id:b,manual:false,harness:'',dir:''}],['change',{action:'publish'}]]);
 assert.equal(saved,1);assert.equal(refreshed,1);assert.equal(catalog.agents.length,4,'duplicate resolution retains every record');
 assert.equal(catalog.agents.find(x=>x.record.id===a).enabled,false);
 tree=render();assert(words(row(tree,b)).includes('Current'));button(row(tree,b),'Disable').props.onClick();await flush();
 assert.deepEqual(calls.at(-1),['change',{action:'disable',id:b}]);assert.equal(catalog.agents.find(x=>x.record.id===b).responder,null);
 tree=render();button(row(tree,b),'Repair or enable').props.onClick();tree=render();
 let form=nodes(tree).find(n=>n.tag==='AgentSheet');assert.equal(form.props.agent.record.id,b);assert.equal(form.props.agent.responder,null,'missing native responder is never cloned');
 assert.deepEqual(Array.from(form.props.harnesses.map(h=>h.name)),['codex'],'other enabled and uninstalled programs are not offered');
 const before=calls.length;assert.equal(await form.props.onSave({harness:'codex',dir:'',label:''}),false);assert.equal(calls.length,before,'blank missing folder cannot re-enable a setup');
 assert.equal(await form.props.onSave({harness:'codex',dir:'/explicit-folder',label:''}),true);
 assert.deepEqual(calls.at(-1),['change',{action:'update',id:b,harness:'codex',dir:'/explicit-folder'}]);
 assert.equal(catalog.agents.length,4);assert.equal(catalog.agents.find(x=>x.record.id===b).responder.dir,'/explicit-folder');
 assert.equal(saved,3);assert.equal(refreshed,3);
 active=false;tree=render();const stopped=calls.length;button(row(tree,pi),'Use as current').props.onClick();await flush();
 assert.equal(calls.length,stopped,'inactive workspace never mutates even an otherwise enabled exact setup');
 catalog={...catalog,host:'wrong/host'};assert.equal(render(),null,'manager requires exact local host');
 catalog={...catalog,host,agents:catalog.agents.map(x=>({...x,record:{...x.record,host_key:'wrong-key'}}))};assert.equal(render(),null,'manager requires current local fingerprint');
 console.log('PASS actual saved-setup manager: collapsed grouped management, exact current ID/publish, duplicate resolution with retained records, disable and explicit same-ID repair, no missing responder clone, no chat targets, local-key/workspace fences');
})().catch(error=>{console.error(error);process.exitCode=1;});
