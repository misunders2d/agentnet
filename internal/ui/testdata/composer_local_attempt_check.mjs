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
const {currentComposerFollowup,composerReplyFollowup}=await import(new URL('web/src/features/Composer.followup.ts',root));
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

// Use the actual mention picker on a real public DTO: selecting an agent after
// choosing standard Reply must keep the exact ancestry, without supplied refs.
const clarify=fixture.cases.find(c=>c.state==='needs_human');
if(clarify){
 const source=readFileSync(new URL('web/src/features/Composer.tsx',root),'utf8');
 const pickSource=source.match(/^  function pick\([\s\S]*?^  }/m)?.[0];
 assert.ok(pickSource,'actual Composer mention picker');
 const original=clarify.thread.messages.find(m=>m.lid===fixture.original),dm=clarify.thread;
 let draft={text:'@Pi',replyTo:original.id,files:[{key:'kept',name:'answer.md',file:{}}]};
 const {runInNewContext:runTag}=await import('node:vm');
 const env={trig:{start:0},caret:3,mentionRange:null,text:draft.text,spans:[],dm,latest:()=>draft,
  followupContext:{conv:dm.id,dm,thread:null,overview:clarify.overview,names:{},canReply:true},composerReplyFollowup,
  shift:()=>({kept:[]}),agentTargets:(_text,spans,fallback)=>spans.filter(s=>s.kind==='agent').map(s=>s.id).concat(fallback||[]).slice(0,1),
  setMentionRange:()=>{},write:next=>draft=next,encode:text=>text,announce:()=>{},collectiveHref:()=>''};
 const pick=runTag('('+stripTypeScriptTypes(pickSource)+')',env);
 pick({kind:'agent',id:fixture.pid,name:'Your Pi'});
 assert.equal(draft.replyTo,original.id,'actual Reply then tag must retain needs_human ancestry');
 assert.equal(draft.agent,fixture.pid,'actual tag keeps selected PID');
 assert.equal(draft.files.length,1,'tag keeps the draft files');
 console.log('actual needs_human Reply then tag preserves exact ancestry PASS');
 // Execute the real Reply-change effect against an armed, tagged draft.
 // A selected message in a named topic restores that exact topic.
 const start=source.indexOf('  const lastReply ='),end=source.indexOf('  }, [conv, draft.replyTo]);',start)+ '  }, [conv, draft.replyTo]);'.length;
 assert.ok(start>=0&&end>start,'actual Reply effect');
 const named=structuredClone(dm),selected=named.messages.find(m=>m.id===original.id);selected.topic='7'.repeat(32);
 draft={...draft,newTopic:true,topic:'6'.repeat(32)};
 const replyEffect={conv:dm.id,draft,dm:named,thread:null,latest:()=>draft,focusField:()=>{},useRef:()=>({current:{conv:dm.id,replyTo:undefined}}),useEffect:f=>f(),store:{setDraft:(_id,d)=>draft=d}};
 runTag(stripTypeScriptTypes(source.slice(start,end)),replyEffect);
 assert.equal(draft.newTopic,false);assert.equal(draft.topic,selected.topic);assert.equal(draft.agent,fixture.pid);assert.equal(draft.files.length,1);
 console.log('actual tag then Reply cancels New topic, restores exact named topic and retains draft/files PASS');
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
assert.equal(selectDirect({...directThread,topic:{agent_host:directPeer,agent_key:directHostKey,agent_id:namedID},messages:[{...directMessage,target:{address:directPeer,fingerprint:directHostKey,agent_id:namedID}},{...secondDirect,target:{address:directPeer,fingerprint:directHostKey,agent_id:otherNamed},exec:{...secondDirect.exec,stale:true}}]}).ref.id,directID,'another exact agent never blocks selected work');
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
// The actual Composer destination block: a standard Reply with no tag derives
// its agent only from stored reply ancestry (MEL-692, oct23 T3).
const targetStart=composerSource.indexOf('  const followupContext: Ctx ='),targetEnd=composerSource.indexOf('  const latest = (): Draft =>',targetStart);
assert.ok(targetStart>=0&&targetEnd>targetStart,'actual Composer destination block');
const targetSource=stripTypeScriptTypes(composerSource.slice(targetStart,targetEnd))+'; globalThis.derived={target,replyAgent};';
const mentionsModule=await import(new URL('web/src/features/Composer.mentions.ts',root)),modelModule=await import(new URL('web/src/model.ts',root));
function composerTarget(dm,draft,overview){
 const env={conv:dm.id,dm,thread:undefined,overview,names:{},draft,text:draft.text,spans:[],answerable:undefined,useMemo:f=>f(),
  agentTargets:mentionsModule.agentTargets,replyFollowupAgent:directModule.replyFollowupAgent,participants:modelModule.participants,scopeMatches:modelModule.scopeMatches};
 runInNewContext(targetSource,env);return env.derived;
}
// Normal group Reply/tag -> actual desktop and Android send. No caller
// constructs a FollowupRef; the real selector derives it from the stored DTO.
async function ordinaryGroupSend(platform,c,{tagged=true,reply=true,newTopic=false,invalid,fail=false,round=1,plainReply=false,replyHuman=false}={}) {
 const dm=structuredClone(c.thread),original=dm.messages.find(m=>m.lid===(c.original||fixture.original)),topic=original.topic,calls=[],notices=[],rows=new Map();
 if(invalid)invalid(original,dm);
 const answer={id:'9'.repeat(32),lid:'9'.repeat(32),dir:'in',kind:'answer',body:'Which budget and folder?',reply_to:original.lid,pid:original.pid,topic:original.topic,from:original.target.address,verified_agent:true};
 const human={id:'8'.repeat(32),lid:'8'.repeat(32),dir:'in',kind:'message',body:'Sounds good',topic:original.topic,from:'admin/other'};
 dm.messages.push(answer,human);
 const draft={text:tagged?'@Your Pi Use the approved budget':'Use the approved budget',agent:tagged?fixture.pid:undefined,replyTo:reply?(replyHuman?human.id:answer.id):undefined,newTopic,topic,plainReply:plainReply||undefined,files:[{key:'answer',name:'answer.md',size:1,file:{}}]};
 const state={open:'selected-view',overview:c.overview,drafts:{[dm.id]:draft}};
 let serial=round*10,cleared=0,retry;
 const storage={getItem:k=>rows.get(k)||null,setItem:(k,v)=>rows.set(k,v)};
 const api=async(endpoint,request)=>{calls.push({endpoint,request:JSON.parse(JSON.stringify(request))});if(fail)throw Error('offline');return {id:request.id,state:'queued',note:'Reply queued in the original request context.'};};
 const ctx={conv:dm.id,dm,thread:null,overview:c.overview,names:{},canReply:true};
 const store={host:{platform,workspace:{id:'ordinary-'+platform},api},get:()=>state,isActive:()=>true,draft:()=>state.drafts[dm.id],setDraft:(_,d)=>state.drafts[dm.id]=d,
  api:{stage:async()=> 'staged-answer',askAgent:r=>api('/api/dm/agent/ask',r),sendDM:r=>api('/api/dm/send',r),discard:async()=>{}},toast:()=>{},refetch:async()=>{},
  sends:{begin:(_c,_m,r)=>{retry=r;},ready:async()=>{},finish:()=>{},remove:()=>{},has:()=>true,fail:()=>{},move:()=>{}}};
 const derived=composerTarget(dm,draft,c.overview);
 if(tagged)assert.equal(derived.replyAgent,undefined,'an explicit tag is never replaced by Reply ancestry');
 const env={store,dm,thread:undefined,conv:dm.id,ready:true,latest:()=>draft,target:tagged?{kind:'agent',pid:fixture.pid}:derived.target,overview:c.overview,names:{},words:{},visible:{current:dm.id},EMPTY:{text:''},
  followupContext:ctx,currentDirectComposerFollowup:directModule.currentDirectComposerFollowup,
  selectedFollowup:(pid,d)=>d.newTopic?undefined:currentComposerFollowup(dm,pid,d.topic||'',ctx,d.replyTo),
  setNotice:n=>{if(n)notices.push(n.text);},setBusy:()=>{},clearSent:()=>{cleared++;state.drafts[dm.id]={text:''};},typing:{stop:()=>{}},
  sendID:()=> (++serial).toString(16).padStart(32,'0'),decode:text=>({text,spans:[]}),encode:text=>text,agentTargets:(_t,_s,selected)=>selected?[selected]:[],
  releaseFiles:()=>{},keepStaged:()=>{},forgetStaged:()=>{},announce:()=>{},errorText:e=>e.message,
  messageOf:id=>dm.messages.find(m=>m.id===id||m.lid===id),guestAuthor:()=>null,
  androidPreparing:{current:new Set()},saveBatch:journal.saveBatch,acknowledge:journal.acknowledge,recordFailure:journal.recordFailure,localStorage:storage};
 runInNewContext(stripTypeScriptTypes(selectorSource)+'; globalThis.boundSelector=selectedTargetFollowup;',env);env.selectedTargetFollowup=env.boundSelector;
 const send=runInNewContext('('+stripTypeScriptTypes(desktopSource)+')',env);env.sendAndroid=runInNewContext('('+stripTypeScriptTypes(androidSource)+')',env);
 await send();
 if(invalid&&tagged){assert.equal(calls.length,0,'invalid exact ancestry has no send');assert.equal(cleared,0);assert.equal(state.drafts[dm.id].files.length,1);assert.ok(notices.length);return;}
 assert.equal(calls.length,1,platform+' one exact logical send');
 const continues=reply&&!newTopic&&(tagged||!invalid&&!plainReply&&!replyHuman);
 assert.equal(derived.replyAgent,!tagged&&continues?fixture.pid:undefined,platform+' Reply derives only the bound agent');
 const expected=continues?'/api/request/followup':tagged?'/api/dm/agent/ask':'/api/dm/send';
 assert.equal(calls[0].endpoint,expected,platform+' ordinary Reply routing for '+c.state);
 if(expected==='/api/request/followup'){
  assert.equal(calls[0].request.attempt,0,'ordinary answer queues normally, never obsolete steering');
  assert.equal(calls[0].request.ref.id,original.lid);assert.equal(calls[0].request.ref.fingerprint,c.author||fixture.author);assert.equal(calls[0].request.conv,dm.id);
  assert.deepEqual(calls[0].request.files,['staged-answer']);
 }else{assert.equal(calls[0].request.ref,undefined,'no tag/Reply/new-topic invents no continuation');if(expected==='/api/dm/send')assert.equal(calls[0].request.reply_to,draft.replyTo,'an ordinary Reply keeps its reply');}
 if(fail&&platform==='android')assert.deepEqual(journal.readPending(storage,'ordinary-'+platform)[0].request,calls[0].request,'Android journal retains exact ordinary continuation');
 else if(fail){assert.equal(typeof retry,'function');retry();await new Promise(r=>setTimeout(r,0));assert.deepEqual(calls[1],calls[0],'ordinary retry keeps logical ID/ref/Attempt0/files');}
 return calls[0];
}
const ordinaryCases=fixture.cases.filter(c=>['needs_human','answered','resolved'].includes(c.state));
for(const platform of ['daemon','android'])for(const c of ordinaryCases){
 await ordinaryGroupSend(platform,c);
 await ordinaryGroupSend(platform,c,{tagged:false});
 await ordinaryGroupSend(platform,c,{tagged:false,fail:true});
 await ordinaryGroupSend(platform,c,{tagged:false,plainReply:true});
 await ordinaryGroupSend(platform,c,{tagged:false,replyHuman:true});
 await ordinaryGroupSend(platform,c,{tagged:false,reply:false});
 for(const change of [m=>m.exec.state='failed',m=>m.pid='f'.repeat(32),m=>m.deleted=true])await ordinaryGroupSend(platform,c,{tagged:false,invalid:change});
 await ordinaryGroupSend(platform,c,{reply:false});
 await ordinaryGroupSend(platform,c,{newTopic:true});
 await ordinaryGroupSend(platform,c,{fail:true});
 for(const change of [m=>m.target.fingerprint='0'.repeat(8)+'-'+('0'.repeat(8)+'-').repeat(2)+'0'.repeat(8),m=>m.pid='f'.repeat(32),m=>m.topic='f'.repeat(32),m=>m.deleted=true,m=>m.origin='agent:pi',m=>m.exec.state='failed',m=>m.exec.state='interrupted',m=>m.history=true])await ordinaryGroupSend(platform,c,{invalid:change});
 // A second ordinary question/answer round has a new exact human request;
 // QueueRequestFollowup canonicalizes its verified root chain at the backend.
 const first=await ordinaryGroupSend(platform,c,{round:2}),next=structuredClone(c),request=next.thread.messages.find(m=>m.lid===fixture.original);
 request.body=first.request.body;request.reply_to=request.lid;request.id=request.lid=next.original=first.request.id;request.exec.state='answered';
 request.from=next.overview.me.address;request.send_group_author=next.author=next.overview.me.fingerprint;
 await ordinaryGroupSend(platform,next,{round:3});
}
if(ordinaryCases.length)console.log('actual desktop/Android ordinary tagged and plain Reply sends: two rounds, Attempt0, plain-Reply continuation, Stop/human/unbound stay ordinary, no-Reply/new-topic, exact retry/journal and native identity/failed/history refusals PASS');

async function directSend(platform,{named=false,fail=false,newTopic=false,ended=false,invalid=false,projection,refusal,refreshState,refreshFails=false,otherStale=false,ordinaryReply=false}={}) {
 const calls=[],previews=[],notices=[],savedRows=new Map();let cleared=0,serial=0,retry,refreshes=0,stages=0;
 const storage={getItem:k=>savedRows.get(k)||null,setItem:(k,v)=>savedRows.set(k,v)};
 const target={kind:'device',ask:true,name:'Selected agent',seed:directPeer};
 const namedTarget=named?{address:directPeer,fingerprint:directHostKey,agent_id:'c'.repeat(32)}:undefined;
 const original=projection?projection.thread.messages.find(m=>m.id===projection.original):directMessage;
 const message=projection?structuredClone(original):{...directMessage,target:namedTarget,exec:{...directMessage.exec,state:ended?'done':'running',attempt:invalid?0:1}};
 const thread=projection?structuredClone(projection.thread):{...directThread,messages:[message]};
 if(projection){thread.messages=thread.messages.map(m=>m.id===message.id?message:m);if(ended)message.exec={...message.exec,state:'done'};if(invalid)message.exec={...message.exec,attempt:0};}
 if(refusal==='pending')thread.key={...thread.key,pending:directAuthor};
 if(refusal==='key')message.target={...message.target,address:thread.peer,fingerprint:directAuthor};
 if(refusal==='stale'||refreshState||refreshFails)message.exec={...message.exec,stale:true};
 if(otherStale){thread.topic={agent_host:directPeer,agent_key:directHostKey,agent_id:namedTarget?.agent_id||''};thread.messages.push({...secondDirect,target:{address:directPeer,fingerprint:directHostKey,agent_id:'e'.repeat(32)},exec:{...secondDirect.exec,stale:true}});}
 const overview=projection?.overview||directOverview;
 const draft={text:'Use English instead',newTopic,replyTo:ordinaryReply?message.id:undefined,files:[{key:'terms',name:'terms.md',size:1,file:{}}]};
 const state={open:'selected-view',overview,drafts:{[thread.id]:draft}};
 const api=async(endpoint,request)=>{calls.push({endpoint,request:JSON.parse(JSON.stringify(request))});if(fail)throw Error('offline');return {id:request.id,state:'queued',note:'accepted for transport'};};
 const store={host:{platform,workspace:{id:platform},api},get:()=>state,isActive:()=>true,draft:()=>state.drafts[thread.id],setDraft:(_,d)=>state.drafts[thread.id]=d,
 api:{thread:async()=>{refreshes++;assert.equal(cleared,0,'refresh precedes draft clearing');if(refreshFails)throw Error('offline while refreshing exact work');return refreshState?{...thread,messages:[{...message,exec:{...message.exec,state:refreshState,stale:false,attempt:2}}]}:thread;},stage:async()=>{stages++;return 'staged-terms';},send:request=>api('/api/send',request),discard:async()=>{}},toast:()=>{},refetch:async()=>{},open:async()=>{},
 sends:{begin:(_,m,r)=>{previews.push(m);retry=r;},ready:async()=>{},finish:()=>{},remove:()=>{},move:()=>{},has:()=>true,fail:()=>{}}};
 const env={store,thread,dm:undefined,conv:thread.id,ready:true,latest:()=>draft,target,overview,names:{},words:{},visible:{current:thread.id},EMPTY:{text:''},
 followupContext:{...directContext(thread),overview},currentDirectComposerFollowup:directModule.currentDirectComposerFollowup,selectedFollowup:()=>{throw Error('invited selector used for direct chat');},
 setNotice:n=>{if(n)notices.push(n.text);},setBusy:()=>{},clearSent:()=>{cleared++;state.drafts[thread.id]={text:''};},typing:{stop:()=>{}},
 sendID:()=> (++serial).toString(16).padStart(32,'0'),decode:text=>({text,spans:[]}),encode:text=>text,
 releaseFiles:()=>{},keepStaged:()=>{},forgetStaged:()=>{},announce:()=>{},errorText:e=>e.message,messageOf:()=>message,
 threadAgentKey:t=>t.key,threadAgentHost:t=>(t.topic?.agent_id||t.topic?.agent_legacy)&&t.topic.agent_host||t.peer,threadAgentID:t=>t.messages[0].target?.agent_id,androidPreparing:{current:new Set()},saveBatch:journal.saveBatch,acknowledge:journal.acknowledge,recordFailure:journal.recordFailure,localStorage:storage};
 runInNewContext(stripTypeScriptTypes(selectorSource)+'; globalThis.boundSelector = selectedTargetFollowup;',env);env.selectedTargetFollowup=env.boundSelector;
 const send=runInNewContext('('+stripTypeScriptTypes(desktopSource)+')',env);
 env.sendAndroid=runInNewContext('('+stripTypeScriptTypes(androidSource)+')',env);
 await send();
 if(!message.target?.agent_id&&(newTopic||ended||refreshState==='done')){assert.equal(calls.length,0,'legacy terminal/new work has no send');assert.equal(cleared,0);assert.equal(stages,0,'legacy recovery stages no files');assert.equal(journal.readPending(storage,platform).length,0,'legacy recovery freezes no send journal');assert.match(notices.join(' '),/concrete agent binding/);return;}
 if(refreshFails){assert.equal(calls.length,0);assert.equal(cleared,0);assert.equal(refreshes,1);assert.match(notices.join(' '),/offline/);return;}
 if((invalid||refusal)&&!newTopic){assert.equal(calls.length,0);assert.equal(cleared,0);assert.match(notices.join(' '),/cannot be (verified|continued)/);return;}
 assert.equal(calls.length,1,platform+' submits one request');
 assert.equal(calls[0].endpoint,newTopic||ended||refreshState==='done'?'/api/send':'/api/request/followup',platform+' routes ordinary direct send');
 if(!newTopic&&!ended&&refreshState!=='done'){assert.equal(calls[0].request.conv,'');assert.equal(calls[0].request.ref.id,projection?.original||directID);assert.equal(calls[0].request.ref.fingerprint,projection?.author||directAuthor);assert.equal(calls[0].request.attempt,ordinaryReply&&message.exec.state!=='running'?0:refreshState?2:original.exec.attempt);assert.deepEqual(calls[0].request.files,['staged-terms']);}
 if(fail&&platform==='android'){
  const entries=journal.readPending(storage,platform);assert.equal(entries.length,1);assert.deepEqual(entries[0].request,calls[0].request,'journal freezes exact intent and stages');
  assert.equal(entries[0].endpoint,'/api/request/followup');
 }else if(fail){
  assert.equal(typeof retry,'function');retry();await new Promise(r=>setTimeout(r,0));
  assert.equal(calls.length,2);assert.deepEqual(calls[1],calls[0],'desktop retry retains exact ID/ref/attempt/files');
 }else if(platform==='android') assert.equal(journal.readPending(storage,platform).length,0);
}
for(const platform of process.env.AGENTNET_COMPOSER_PLATFORM?[process.env.AGENTNET_COMPOSER_PLATFORM]:['daemon','android'])for(const opts of [{},{named:true},{fail:true},{newTopic:true},{ended:true},{invalid:true},{named:true,otherStale:true},{refreshState:'done'},{named:true,refreshState:'done'},{named:true,refreshState:'running'},{named:true,refreshFails:true}])await directSend(platform,opts);
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
   if(m.exec.state!=='running'){await directSend(platform,{projection,ordinaryReply:true});await directSend(platform,{projection,ordinaryReply:true,fail:true});console.log(projection.label+' '+platform+' actual ordinary linked Reply Attempt0 PASS');continue;}
   for(const opts of [{},{fail:true},{refusal:'pending'},{refusal:'key'},{refusal:'stale'},{invalid:true},{ended:true}])await directSend(platform,{projection,...opts});
   console.log(projection.label+' '+platform+' actual linked DTO routing, exact intent and refusal/no-active controls PASS');
  }catch(e){failures.push(projection.label+' '+platform+': '+e.message);console.log('FAIL '+failures.at(-1));}
 }
}
assert.deepEqual(failures,[],'production-derived linked direct projection must steer in both current send paths');
