// Execute the production Stop and Activity components with retained host DTOs.
// The tiny hook host supplies rendering only; it never reconciles execution.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const esbuild=require('../web/node_modules/esbuild');
const source=file=>fs.readFileSync(path.join(__dirname,'../web/src/',file),'utf8');
const jsx=(tag,props,...children)=>({tag,props:{...props,children:children.flat(Infinity).filter(x=>x!==false&&x!=null)}});
const nodes=t=>t&&typeof t==='object'?[t,...(t.props?.children||[]).flatMap(nodes)]:[];
const text=t=>t&&typeof t==='object'?(t.props?.children||[]).map(text).join(''):String(t??'');
function load(file,globals,extra=''){
 const env={module:{exports:{}},jsx,Fragment:'Fragment',Date,Map,Set,console,...globals};env.exports=env.module.exports;
 const code=source(file).replace(/^import[\s\S]*?from[^;]+;/gm,'')+extra;
 vm.runInNewContext(esbuild.transformSync(code,{loader:file.endsWith('tsx')?'tsx':'ts',jsxFactory:'jsx',jsxFragment:'Fragment',format:'cjs'}).code,env);
 return env.module.exports;
}
function hooks(){let cells=[],cursor=0,effects=[],fn,tree;
 const render=()=>{cursor=0;effects=[];tree=fn();for(const effect of effects)effect();return tree;};
 return {render,get:()=>tree,start:f=>{fn=f;return render();},react:{
  useState:init=>{const i=cursor++;cells[i]??={value:typeof init==='function'?init():init};return[cells[i].value,v=>{cells[i].value=typeof v==='function'?v(cells[i].value):v;}];},
  useRef:init=>{const i=cursor++;return cells[i]??(cells[i]={current:init});},
  useEffect:(effect,deps)=>{const i=cursor++,old=cells[i];if(!old||deps.some((d,n)=>d!==old[n])){cells[i]=deps;effects.push(effect);}},
 }};
}
const base={host:'fictional/host',id:'1'.repeat(32),key:'aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa',attempt:1};
const refused='the request is no longer as you saw it (now answered, attempt 1)';
const result=(state,reason)=>({decision:'2'.repeat(32),state,at:1792501200,...(reason?{refused:reason}:{})});
const model=load('model.ts',{collectivePlain:s=>s,previewText:s=>s}),messageModel=load('features/Message.model.ts',model);
const named='4'.repeat(32),sameName='8'.repeat(32),currentAgent='7'.repeat(32);
const labelContext={names:{[named]:'Historical Pi',[sameName]:'Historical Pi',[currentAgent]:'Current default'},thread:{peer:'fictional/host',topic:{agent_host:'fictional/host',agent_id:currentAgent}},overview:{me:{address:'fictional/phone'}}};
for(const dir of ['in','out']){
 const original={id:base.id,author:{label:'Original human'},dir,kind:'question'};
 assert.equal(messageModel.requestLabel({...original,target:{agent_id:named},responder:'claude'},labelContext),'Asked Historical Pi · 44444444','exact historical target wins and duplicate labels retain identity');
 assert.equal(messageModel.requestLabel({...original,target:{agent_id:'e'.repeat(32)}},labelContext),'Asked Agent eeeeeeee','unknown named target remains the original identity');
 assert.equal(messageModel.requestLabel({...original,kind:'task',responder:'pi'},labelContext),'Task for Pi','original responder snapshot labels legacy work');
 assert.equal(messageModel.requestLabel({...original,responder:'codex'},labelContext),'Asked Codex');
 assert.equal(messageModel.requestLabel({...original,responder:'manual'},labelContext),'Asked unknown agent','manual is not an executor identity');
 assert.equal(messageModel.requestLabel(original,labelContext),'Asked unknown agent','unknown legacy request never borrows current default or context');
}
const retained={id:base.id,author:{label:'You'},dir:'out',kind:'task',exec:{state:'running',attempt:1,stale:true},stop:{...base,result:result('answered',refused)}};
assert.match(messageModel.requestState(retained,[]).text,/Host reports finished/,'bubble caption must also distinguish a host terminal snapshot from stale working');
assert.equal(messageModel.working(retained,[]),false,'terminal snapshot never animates the selected old request as working');
const newer={...retained,exec:{state:'running',attempt:2,stale:false}};
assert.equal(messageModel.working(newer,[]),true,'old snapshot never suppresses newer genuine work');
assert.match(messageModel.requestState(newer,[]).text,/Working/,'newer current attempt keeps its actual execution caption');
const h=hooks(),calls=[];
const store={run:async action=>action({decide:async body=>{calls.push(structuredClone(body));return {note:'Queued only'};}}),toast:()=>{}};
let request={...base,result:result('answered',refused)};
const component=load('features/RequestResolution.tsx',{...h.react,terminalStopLabel:messageModel.terminalStopLabel,useApp:()=>store,Button:'Button',Sheet:'Sheet',sendID:()=> '3'.repeat(32),niceDevice:x=>x,errorText:e=>e.message});
const button=()=>nodes(h.get()).find(n=>n.tag==='Button');
h.start(()=>component.StopOwnRequest({request}));
assert.equal(button().props.disabled,true,'terminal answered refusal must not offer another Stop');
assert.match(text(h.get()),/finished|answered/i,'terminal reply is shown truthfully');
assert(!/Stopping|Stop queued/.test(text(h.get())),'terminal reply cannot remain stopping');
assert(text(h.get()).includes(refused),'host refusal is retained, not interpreted as permission to retry');
assert.equal(calls.length,0,'displaying a terminal result sends nothing');
for(const state of ['done','answered','failed','timeout','cancelled','stopped','declined','resolved','not_run']){
 for(const reason of ['',refused]){request={...base,result:result(state,reason)};h.render();assert(button().props.disabled,state+' terminal result is not actionable');assert(!/Stopping|Stop queued/.test(text(h.get())),state+' is no longer stopping');}
}
request={...base,pending:true};h.render();assert(button().props.disabled);assert.equal(text(button()),'Stop queued');
request={...base,result:result('running')};h.render();assert(button().props.disabled);assert.equal(text(button()),'Stopping…','accepted running cancellation remains in progress');
request={...base,result:result('running','running request changed')};h.render();assert(!button().props.disabled,'genuine running refusal keeps explicit retry');assert(text(h.get()).includes('running request changed'));
request={...base};h.render();assert(!button().props.disabled);button().props.onClick();

(async()=>{
 await new Promise(resolve=>setImmediate(resolve));h.render();assert.equal(text(button()),'Stop queued');assert(button().props.disabled);
 assert.deepEqual(calls,[{...base,action:'cancel',expect:'running',report:'',send_id:'3'.repeat(32)}],'only one exact signed decision request, no work resend');
 request={...base,id:'4'.repeat(32),key:'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb',attempt:2};h.render();h.render();assert.equal(text(button()),'Stop','new exact request resets local pending state');
 let release;
 store.run=async action=>{await new Promise(resolve=>{release=resolve;});return action({decide:async body=>{calls.push(structuredClone(body));return {note:'Old request queued'};}});};
 button().props.onClick();h.render();assert.equal(text(button()),'Sending…');
 request={...base,host:'fictional/new-host',id:'5'.repeat(32),key:'cccccccc-cccccccc-cccccccc-cccccccc',attempt:3};h.render();h.render();
 assert.equal(text(button()),'Stop','changed host/key/id/attempt releases old local busy state');
 release();await new Promise(resolve=>setImmediate(resolve));h.render();assert.equal(text(button()),'Stop','late old response never queues Stop on the replacement request');
 assert.equal(calls[1].id,'4'.repeat(32));assert.equal(calls[1].attempt,2);assert.equal(calls[1].key,'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb');

 const ah=hooks(),activityModel=load('features/NativeActivity.model.ts',{});
 const activity=load('features/Conversation.timeline.tsx',{...ah.react,...component,...activityModel,terminalStopLabel:messageModel.terminalStopLabel,useApp:()=>store,useStore:()=>({entries:[]}),
  isThreadMsg:messageModel.isThreadMsg,working:messageModel.working,threadAgentName:()=> 'Pi',agentName:()=> 'Pi',agentOf:()=>({pid:'agent',host:{address:base.host}}),agentLabel:()=> 'Pi',
  localRunningDetail:(_detail,running)=>running?'Reading files':'',timeOf:()=> '12:00',AgentAvatar:'AgentAvatar',PersonAvatar:'PersonAvatar',NativeActivity:'NativeActivity',
 },'\nexport { Activity };');
 let message={id:base.id,author:{label:'You'},dir:'out',kind:'task',exec:{state:'running',attempt:1,stale:true,detail:'Reading files'},stop:{...base,result:result('answered',refused)}};
 const ctx={thread:{peer:base.host},names:{},overview:{me:{address:'fictional/phone'}}};
 ah.start(()=>activity.Activity({ctx,messages:[message]}));
 const terminal=ah.get();
 assert(!nodes(terminal).some(n=>n.tag==='AgentAvatar'&&n.props.mood==='working'),'terminal result has no working avatar');
 assert(!nodes(terminal).some(n=>typeof n.tag==='function'&&n.tag.name==='Dots'),'terminal result has no working dots');
 assert(!nodes(terminal).some(n=>n.tag==='NativeActivity'),'terminal result has no stale native activity');
 assert(!/is stopping|last reported working|reported a changed request/.test(text(terminal)),'terminal result is not presented as ongoing work');
 message={...message,exec:{...message.exec,stale:false},stop:{...base,pending:true}};ah.render();assert(text(ah.get()).includes('Stop queued'));assert(nodes(ah.get()).some(n=>typeof n.tag==='function'&&n.tag.name==='Dots'),'real pending running work keeps its indicator');
 message={...message,stop:{...base,result:result('running','changed current request')}};ah.render();assert(text(ah.get()).includes('reported a changed request'));
 message={...message,stop:{...base,result:result('cancelled')}};ah.render();assert(!nodes(ah.get()).some(n=>typeof n.tag==='function'&&n.tag.name==='Dots'),'confirmed cancellation has no working dots');
 message={...message,exec:{...message.exec,state:'running',stale:false,attempt:2},stop:{...base,result:result('answered',refused)}};ah.render();
 assert(text(ah.get()).includes('is working'),'a newer current running attempt remains visible');
 assert(!text(ah.get()).includes('Host reports finished'),'old stop snapshot never replaces the newer activity card');
 assert(nodes(ah.get()).some(n=>typeof n.tag==='function'&&n.tag.name==='Dots'),'newer genuine running work keeps dots');
 assert(!nodes(ah.get()).some(n=>n.tag===component.StopOwnRequest),'old attempt Stop action is not offered on newer current work');
 console.log('PASS actual StopOwnRequest/Activity: terminal refusal and success, queued/running/refusal controls, exact decision and identity reset, no work send, no terminal working indicator');
})().catch(error=>{console.error(error);process.exitCode=1;});
