// Inert captured-host fixture: bundled skins and native DTO-shaped synthetic data.
// No static rebuild, Hub, credentials or harness.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const settle=async page=>{await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));await page.evaluate(async()=>{
 await document.fonts.ready;
 const animations=document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>{
  const target=a.effect.target,closed=target?.closest('details:not([open])');
  return a.effect.getComputedTiming().iterations!==Infinity&&(!closed||!!closed.querySelector(':scope > summary')?.contains(target));
 });
 let timer;try{await Promise.race([Promise.all(animations.map(a=>a.finished.catch(()=>{}))),new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('Visible animations did not settle')),4000);})]);}finally{clearTimeout(timer);}
});};
const root=process.env.AGENTNET_RENDERED_ASSETS?path.resolve(process.env.AGENTNET_RENDERED_ASSETS):path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
assert(evidence,'private screenshot directory required');fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(name,address,key)=>({person:({Aster:'a',Brin:'b',Cora:'c',Dune:'d'}[name]).repeat(32),label:name,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const outside=person('Dune','dune/office','dune-key');
const me=person('Aster','aster/laptop','aster-key'),brin=person('Brin','brin/desktop','brin-key');me.state='self';
const cora=person('Cora','cora/windows','cora-key');
const guest={pid:'guest-1',host:outside,inviter:me,shared:[],state:'active',state_text:'Guest while present',host_here:false,can_send:true,can_end:true,can_leave:false,can_decide:false};
const thread={id:conv,kind:'group',title:'orchard_dispatch',peer:{label:'orchard_dispatch',address:'',state:''},role:'member',members:[{...me,admin:true},brin],frozen:'',agents:[],guests:[guest],messages:[
 {id:'context',lid:'context',from:brin.address,dir:'in',kind:'message',body:'Selected warehouse context',at,group_ref:{lid:'context',author:brin.fingerprint,hash:'a'.repeat(64)},attachments:[{name:'manifest.txt',size:12,id:'file-1'}]},
 {id:'private',lid:'private',from:me.address,dir:'out',kind:'message',body:'Unselected earlier history',at,group_ref:{lid:'private',author:me.fingerprint,hash:'b'.repeat(64)}}]};
const overview={version:'fixture',seq:1,me:{address:me.address,fingerprint:me.fingerprint,responder:'fixture'},person:me,persons:true,agents:true,groups:true,files:{max_file:1048576,max_message:2097152,max_count:8},controls:true,role:'person',people:[outside,cora],review:[],links:[],reminders:[],threads:[],dms:[{id:conv,kind:'group',title:thread.title,peer:thread.peer,count:2,unread:0}],directory:{current:true,members:[me,brin,outside,cora].map(p=>({address:p.address,label:p.label,fingerprint:p.fingerprint,presence:'connected'}))},quarantine:[],group_invitations:[{id:'invite-1',conv,direction:'out',status:'pending',title:thread.title,inviter:me.person,target:cora.person,history:[],can_cancel:true,can_refresh:true}]};
const completion=process.env.AGENTNET_COMPLETION_PROJECTION?JSON.parse(fs.readFileSync(process.env.AGENTNET_COMPLETION_PROJECTION,'utf8')):null;
if(completion){Object.assign(overview,completion.overview);Object.assign(thread,completion.thread);}
const presentationResult="**Choose the destination.**\n\nKeep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. Keep the complete original result. \n\nFULL_RESULT_END\nreaction: \ud83d\udc4d\ntopic: done\nemotion: curious";
const boot=`
const seed=${JSON.stringify({overview,thread})};window.fixture={...seed,requests:[]};
const variant=new URL(location.href).searchParams.get('case')||'admin';
if(variant==='completion')fixture.overview.group_invitations=[];
if(variant==='member')fixture.thread.members[0].admin=false;
if(variant==='topic-invite'||variant==='topic-invite-dm'){
 fixture.overview.group_invitations=[];fixture.thread.guests=[{pid:'broad-human',host:fixture.overview.people.find(p=>p.label==='Cora'),inviter:fixture.overview.person,state:'active',invited:'2026-10-05T10:00:00Z',shared:[],can_end:true}];
 fixture.overview.agent_devices=['brin/desktop'];
 fixture.thread.topics=[{id:'1'.repeat(32),title:'Warehouse',state:'active',count:1,last_at:'2026-10-05T10:01:00Z'},{id:'2'.repeat(32),title:'Private plans',state:'active',count:1,last_at:'2026-10-05T10:02:00Z'}];
 fixture.thread.messages[0].attachments=[];
 fixture.thread.messages.push({id:'topic-message',lid:'topic-message',topic:'1'.repeat(32),from:'brin/desktop',dir:'in',kind:'message',body:'Warehouse topic context',at:'2026-10-05T10:01:00Z',group_ref:{lid:'topic-message',author:'brin-key',hash:'c'.repeat(64)}},{id:'other-topic-message',lid:'other-topic-message',topic:'2'.repeat(32),from:'brin/desktop',dir:'in',kind:'message',body:'Unrelated private plans',at:'2026-10-05T10:02:00Z',group_ref:{lid:'other-topic-message',author:'brin-key',hash:'d'.repeat(64)}});
 fixture.thread.agents=[{pid:'broad-agent',host:fixture.thread.members[1],agent_id:'a'.repeat(32),state:'active',state_text:'Active',invited:'2026-10-05T10:00:00Z',host_here:false,shared:[],can_ask:true,can_dismiss:true,inviter:fixture.overview.person}];
 fixture.thread.messages.at(-1).attachments=[{name:'other-topic-private.txt',id:'private-file',size:12}];
 if(variant==='topic-invite-dm'){
  fixture.thread.kind='dm';fixture.thread.peer=fixture.thread.members[1];fixture.thread.members=[];
  Object.assign(fixture.overview.dms[0],{kind:'dm',peer:fixture.thread.peer});
 }
}

if(variant==='presentation'){
 fixture.overview.group_invitations=[];fixture.thread.guests=[];
 fixture.result=${JSON.stringify(presentationResult)};
 fixture.thread.agents=[{pid:'presentation-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person}];
 for(const [id,body,detail] of [['presentation-short','Short exact request',${JSON.stringify('Short reason\nemotion: curious')}],['presentation-long','Long exact request',fixture.result]])fixture.thread.messages.push({id,lid:id,from:'brin/desktop',dir:'in',kind:'task',body,at:'2026-10-05T10:01:00Z',pid:'presentation-agent',target:{address:fixture.overview.me.address,agent_id:'a'.repeat(32)},state:'needs_human',job_state:'needs_human',job_detail:detail,actions:['resolve'],can:[]});
}

if(variant==='report-history'){
 const host='aster/other-laptop',id='3'.repeat(32),key='exact-request-key';
 fixture.overview.group_invitations=[];
 fixture.overview.person.devices=[{address:'aster/laptop',human:true},{address:host}];
 const original={id,lid:id,from:'aster/laptop',dir:'out',kind:'task',body:'Finish the README edits',at:'2026-10-05T10:00:00Z',continuation:{id,key,host,attempt:2},exec:{host,state:'needs_human',attempt:2},actions:['continue']};
 fixture.thread.messages.push(original,{id:'4'.repeat(32),lid:'4'.repeat(32),from:'aster/laptop',dir:'out',kind:'task',body:'Finish the README edits in this follow-up',reply_to:id,at:'2026-10-05T10:01:00Z',exec:{host,state:'done',attempt:1},actions:[]},{id:'5'.repeat(32),from:host,dir:'in',kind:'result',body:'README edits committed in the follow-up',reply_to:'4'.repeat(32),status:'done',at:'2026-10-05T10:02:00Z'});
 window.setReportHistory=mode=>{
  const item={id,key,from:'aster/laptop',kind:'task',state:'needs_human',attempt:2,since:100,excerpt:'OLD_BLOCKER_DETAIL: choose the repository',actionable:false};
  if(mode==='confirmed')item.result={decision:'decision',state:'resolved',at:101};
  if(mode==='refused')item.result={decision:'decision',state:'needs_human',refused:'Request changed',at:101};
  if(mode==='new-attempt')item.attempt=3;
  const report=(items,time,received)=>({id:'notice-'+time,peer:host,notice:true,kind:'message',at:received,report:{v:2,host,at:time,items}});
  fixture.overview.review=[report([item],100,'2026-10-05T10:02:00Z')];
  if(mode==='empty-iso'||mode==='empty-tie')fixture.overview.review.push(report([],mode==='empty-iso'?'1970-01-01T00:01:41Z':100,'2026-10-05T10:01:00Z'));
 };
 setReportHistory('active');
}

if(variant==='followup'||variant.startsWith('composer-followup')){
 const key='aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa',originalKey='eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee',id='1'.repeat(32);
 fixture.overview.me.fingerprint=key;fixture.overview.person.fingerprint=key;fixture.thread.members[0].fingerprint=key;fixture.overview.person.devices=[{address:'aster/laptop',fingerprint:key},{address:'aster/other-laptop',fingerprint:originalKey}];
 fixture.overview.group_invitations=[];fixture.thread.guests=[];fixture.followupOriginal='Prepare contribution-margin.md in Russian';
 fixture.thread.agents=[{pid:'followup-agent',host:fixture.thread.members[1],agent_id:'a'.repeat(32),state:'active',state_text:'Active',invited:'2026-10-05T10:00:00Z',host_here:false,shared:[],can_ask:true,inviter:fixture.overview.person}];
 fixture.thread.messages.push({id,lid:id,from:'aster/other-laptop',send_group_author:originalKey,via:'aster/other-laptop',dir:'out',kind:'question',body:fixture.followupOriginal,at:'2026-10-05T10:01:00Z',pid:'followup-agent',target:{address:'brin/desktop',fingerprint:'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb',agent_id:'a'.repeat(32)},state:'delivered',exec:{state:'running',host:'brin/desktop',attempt:1},actions:[]});
 fixture.followupID=id;fixture.followupStages=[];
 fixture.thread.members[1].fingerprint='bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb';
 fixture.thread.members[1].devices[0].fingerprint=fixture.thread.members[1].fingerprint;
 if(variant.startsWith('composer-followup')){
  fixture.thread.agents.push({...fixture.thread.agents[0],pid:'same-name-other-agent',agent_id:'b'.repeat(32)});
  const root=fixture.thread.messages.at(-1);
  fixture.thread.messages.push({...root,id:'2'.repeat(32),lid:'2'.repeat(32),body:'Prepare independent inventory report',exec:{...root.exec,state:'queued'}});
  for(const [i,state] of ['pending','accepted','part_waiting'].entries())fixture.thread.messages.push({...root,id:String(i+5).repeat(32),lid:String(i+5).repeat(32),body:'Non-running '+state+' sibling',exec:{...root.exec,state}});
  fixture.thread.topics=[{id:'9'.repeat(32),title:'Other topic',state:'active',count:1,last_at:root.at}];
  fixture.thread.messages.push({...root,id:'3'.repeat(32),lid:'3'.repeat(32),topic:'9'.repeat(32),body:'Other-topic work'},
   {...root,id:'4'.repeat(32),lid:'4'.repeat(32),origin:'agent:codex',body:'Peer-authored work'});
 }
}
if(variant==='browser-app'){
 fixture.overview.device={browser:true};fixture.overview.me.browser=true;
}
if(variant==='guest-active'||variant==='guest-invited'){
 fixture.thread.role='human_guest';fixture.overview.person={...fixture.thread.guests[0].host,state:'self'};fixture.overview.me.address=fixture.overview.person.address;
 Object.assign(fixture.thread.guests[0],{host_here:true,can_end:false,can_leave:variant==='guest-active',can_decide:variant==='guest-invited',can_send:variant==='guest-active',state:variant==='guest-active'?'active':'invited'});
 fixture.overview.group_invitations=[];
}
if(variant==='long-links'){
 fixture.url='https://example.test/folder/'+('long-segment/'.repeat(12))+'?q=a%2Fb&value=%E2%9C%93#section-2';
 fixture.linkBody='Read '+fixture.url+' then <'+fixture.url+'>.\\n[Useful guide]('+fixture.url+')\\n'+String.fromCharCode(96)+fixture.url+String.fromCharCode(96)+'\\n\\n~~~text\\n'+fixture.url+'\\n~~~';
 fixture.thread.messages.push({id:'long-links',lid:'long-links',from:'brin/desktop',dir:'in',kind:'message',body:fixture.linkBody,at:'2026-10-05T10:02:00Z'});
 fixture.overview.group_invitations=[];
}
if(variant==='reactions'){
 fixture.overview.group_invitations=[];fixture.thread.guests=[];
 const people=[{id:'brin',label:'Brin'},{id:'cora',label:'Cora'},{id:'dune',label:'Dune'}];
 const agent={id:'agent-mark',assistant:true,host:'brin/desktop',agent_id:'a'.repeat(32)};
 Object.assign(fixture.thread.messages[0],{can:['react'],reactions:[
  {emoji:'😂',by:people.slice(0,1)},{emoji:'👍',by:[...people,agent]},
  {emoji:'🎉',by:people.slice()},{emoji:'👀',by:[agent]}]});
 fixture.thread.messages=[fixture.thread.messages[0]];
}
if(variant==='oks'){
 fixture.thread.kind='dm';fixture.thread.peer=${JSON.stringify(brin)};fixture.thread.guests=[];fixture.thread.members=[];
 fixture.thread.topics=[{id:'work-topic',title:'Warehouse check',state:'active',count:1,last_at:'2026-10-05T10:00:00Z'},{id:'other-topic',title:'Carrier review',state:'active',count:1,last_at:'2026-10-05T10:00:00Z'}];
 const req=(id,kind,topic,actions,state,body)=>({id,lid:id,from:'brin/desktop',dir:'in',kind,topic,actions,state,body,at:'2026-10-05T10:00:00Z',pid:'own-agent',target:{address:'aster/laptop'},verified_agent:false});
 fixture.thread.agents=[{pid:'own-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',state_text:'Active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,invited:'2026-10-05T10:00:00Z',tasks_from:[]}];
 fixture.thread.messages=[req('working-request','task','work-topic',['cancel'],'running','Check warehouse inventory'),req('held-question','question','',['accept','decline','approve'],'held','Which carrier should we use?'),req('needs-person','task','other-topic',['resolve','accept','reply'],'needs_human','Review carrier handoff'),{id:'main-note',from:'brin/desktop',dir:'in',kind:'message',body:'Main flow conversation',at:'2026-10-05T10:00:00Z'}];
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.group_invitations=[];
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:fixture.thread.peer,count:4,unread:0,title:'',last:'Main flow conversation'}];
 fixture.why='Permission needed for carrier handoff.\\nReview the destination and confirm the warehouse owner has authorized this change. Nothing was sent to the carrier. Full detail remains here: FINAL_REASON_MARKER.';
 fixture.overview.needs_you=[{conv:fixture.thread.id,id:'working-request',reason:'agent_awaiting',peer:'brin/desktop',kind:'task',excerpt:'Check warehouse inventory',why:'Already working',actions:['cancel'],at:'2026-10-05T11:00:00Z'}, {conv:fixture.thread.id,id:'held-question',reason:'agent_awaiting',peer:'brin/desktop',kind:'question',excerpt:'Which carrier should we use?',why:'Not approved',actions:['accept','decline','approve'],at:'2026-10-05T10:00:00Z'}, {conv:fixture.thread.id,id:'needs-person',reason:'agent_needs_human',peer:'brin/desktop',kind:'task',excerpt:'Review carrier handoff',why:fixture.why,actions:['resolve','accept','reply'],at:'2026-10-05T09:00:00Z'}];
}
if(variant.startsWith('continuation-')){
 const mode=variant.slice('continuation-'.length),id='needs-answer-exact';
 fixture.thread.kind='dm';fixture.thread.peer=${JSON.stringify(brin)};fixture.thread.guests=[];fixture.thread.members=[];
 fixture.thread.agents=[{pid:'own-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',state_text:'Active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,tasks_from:[]}];
 const descriptor={id,key:'exact-request-key',attempt:7,...(['remote','resolve'].includes(mode)?{host:'aster/other-laptop'}:{})};
 const request={id,lid:id,from:'brin/desktop',dir:'in',kind:'task',body:'Review the destination',quote:'ordinary-quote',at:'2026-10-05T10:00:00Z',pid:'own-agent',target:{address:'aster/laptop'},state:'needs_human',job_state:'needs_human',job_detail:'Which warehouse should I use? EXACT_QUESTION',detail:'Which warehouse should I use? EXACT_QUESTION',actions:mode==='stale'?['resolve']:['continue','resolve'],...(mode==='stale'?{}:{continuation:descriptor})};
 fixture.thread.messages=[fixture.thread.messages[0],request,{id:'ordinary-quote',from:'brin/desktop',dir:'in',kind:'message',body:'Ordinary quoted note',at:'2026-10-05T09:00:00Z'}];
 fixture.overview.group_invitations=[];fixture.overview.people=[${JSON.stringify(brin)}];
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:fixture.thread.peer,count:3,unread:0,last:'Selected warehouse context'}];
 fixture.overview.needs_you=[{conv:fixture.thread.id,id,reason:'agent_needs_human',peer:'brin/desktop',kind:'task',excerpt:request.body,why:request.job_detail,actions:request.actions,continuation:request.continuation,at:request.at}];
 if(mode==='resolve'){request.actions=['continue'];fixture.overview.needs_you[0].decide_on=descriptor.host;}
}
if(variant.startsWith('decline-invite-')){
 fixture.overview.dms=[];fixture.overview.threads=[];
 fixture.overview.group_invitations=[{id:'inbound-exact',conv:'d'.repeat(64),direction:'in',status:variant.endsWith('stale')?'stale':'pending',title:'Orchard inbound invitation',inviter:${JSON.stringify(brin.address)},target:fixture.overview.person.person,history:[]}];
}
if(variant==='direct-files'){
 const id='6'.repeat(32),summary={id,peer:'brin/desktop',title:'Files on my other device',last:'Retained file',last_at:'2026-10-05T10:00:00Z',count:1,unread:0,state:'active'};
 fixture.overview.dms=[];fixture.overview.threads=[summary];fixture.overview.group_invitations=[];
 fixture.deviceThread={id,peer:summary.peer,key:{pinned:'brin-key'},approved:false,messages:[{id,from:'aster/phone',dir:'out',kind:'message',body:'Retained file',history:true,synced_from:'aster/laptop',at:'2026-10-05T10:00:00Z',author:{label:'Aster',about:'Synced fictional original'},actions:[],files:[{index:0,name:'retained.txt',size:5,openable:false,availability:'requestable'}]}],topic:summary};
 window.openSyncedFiles=()=>open(id,'message');
 window.fileArrives=()=>{Object.assign(fixture.deviceThread.messages[0].files[0],{openable:true,availability:''});reloadFixture();};
}
if(variant==='device-oks'){
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.dms=[];fixture.overview.group_invitations=[];fixture.overview.needs_you=[];
 const request='7'.repeat(32),other='8'.repeat(32),body='Check Orchard warehouse inventory';
 const summary=(id,title)=>({id,peer:'brin/desktop',title,last:title,last_at:'2026-10-05T10:00:00Z',count:1,review:0,running:0,unread:0,waiting:false,pending:true,state:'active',quiet_since:'2026-10-05T10:00:00Z'});
 fixture.overview.threads=[summary(request,body),summary(other,'Carrier notes')];
 const message={id:request,from:'brin/desktop',dir:'in',kind:'question',body,at:'2026-10-05T10:00:00Z',author:{label:'Brin',about:'Signed fictional fixture'},state:'held',actions:['reply','accept','decline','approve']};
 fixture.deviceThread={id:request,peer:'brin/desktop',key:{pinned:'brin-key'},approved:false,task_grant:'',messages:[message],topic:fixture.overview.threads[0]};
 fixture.otherDeviceThread={...fixture.deviceThread,id:other,topic:fixture.overview.threads[1],messages:[{...message,id:other,kind:'message',body:'Different device thread',state:'delivered',actions:[]}]};
 window.setDeviceState=state=>{
  message.state=state;message.actions=state==='held'?['reply','accept','decline','approve']:state==='running'?['cancel']:[];
  fixture.overview.threads[0].review=state==='held'?1:0;fixture.overview.threads[0].running=['running','cancel_requested'].includes(state)?1:0;
  fixture.overview.review=state==='done'?[]:[{id:request,peer:'brin/desktop',kind:'question',excerpt:body,why:state==='held'?'Not approved for automatic answers':state==='cancel_requested'?'Cancellation requested; stopping':'Already running',reason:['running','cancel_requested'].includes(state)?'agent_running':state==='held'?'held':'',at:'2026-10-05T10:00:00Z'}];
  changed?.({type:'change',seq:++fixture.overview.seq});
 };
 // The initial overview carries review[], never a needs_you substitute.
 fixture.overview.threads[0].review=1;fixture.overview.review=[{id:request,peer:'brin/desktop',kind:'question',excerpt:body,why:'Not approved for automatic answers',reason:'held',at:'2026-10-05T10:00:00Z'}];
 window.openOtherDevice=()=>open(other,'message');
}
if(variant==='notifications'){
 const p=${JSON.stringify(brin)};p.devices.push({address:'brin/phone',fingerprint:'phone-key'});fixture.thread.kind='dm';fixture.thread.peer=p;
 fixture.overview.dms=[{id:fixture.thread.id,kind:'dm',peer:p,title:'Warehouse',created:'2026-10-01T10:00:00Z'}, {id:'e'.repeat(64),kind:'dm',peer:{...p,address:'brin/phone'},title:'Shipping',created:'2026-10-02T10:00:00Z'}];
 fixture.overview.notify={available:true,native:true,enabled:true,allowed:['brin/phone'],mutes:['e'.repeat(64)]};fixture.overview.group_invitations=[];
}
if(variant==='accepted')fixture.overview.group_invitations[0].status='accepted';
if(variant.startsWith('delivery-')){
 fixture.overview.people=[${JSON.stringify(brin)}];fixture.overview.dms=[];fixture.overview.group_invitations=[];fixture.overview.needs_you=[];
 const scenario=variant.slice('delivery-'.length),id='9'.repeat(32);
 const uncertain=scenario.startsWith('uncertain'),never=scenario.startsWith('never'),error=scenario==='queued-error';
 const terminal=scenario.endsWith('native')?'not_delivered':'failed';
 const state=uncertain||never||error?terminal:scenario==='running'?'delivered':scenario==='answered'?'delivered':scenario;
 const detail=uncertain?'Delivery unconfirmed; local retries stopped. Cancellation cannot be confirmed.':never?'Deleted before handover; not sent.':error?'Queued request cannot be handed over: synthetic recipient capability missing.':'';
 const summary={id,peer:'brin/desktop',title:'Synthetic delivery request',last:'Synthetic delivery request',last_at:'2026-10-05T10:00:00Z',count:1,review:0,running:0,unread:0,waiting:false,pending:true,state:'active',quiet_since:'2026-10-05T10:00:00Z'};
 const message={id,from:fixture.overview.me.address,to:'brin/desktop',dir:'out',kind:'question',body:'Synthetic delivery request',at:'2026-10-05T10:00:00Z',author:{label:'Aster',about:'Signed fictional fixture'},state,delivery:state,actions:[],deleted:!error,detail,state_text:uncertain?detail:never?'Not sent; local sending stopped.':error?detail:state,...(!error?{send_stopped:true}:{}),...(uncertain?{delivery_uncertain:true}:{})};
 if(scenario==='running')message.exec={state:'running',host:'brin/desktop',at:1};
 if(scenario==='answered')message.state_text='Answered by their agent';
 fixture.overview.threads=[summary];fixture.overview.review=[];
 fixture.deviceThread={id,peer:'brin/desktop',key:{pinned:'brin-key'},approved:false,task_grant:'',messages:[message],topic:summary};
 if(scenario==='answered')fixture.deviceThread.messages.push({id:'answer-fixture',from:'brin/desktop',dir:'in',kind:'answer',reply_to:id,body:'Synthetic answer already received',at:'2026-10-05T10:01:00Z',author:{label:'Brin',about:'Signed fictional fixture'},state:'received',actions:[]});
 fixture.deliveryScenario=scenario;fixture.deliveryDetail=detail;
}
if(variant==='other-device')Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});
if(variant==='legacy-flags'){delete fixture.overview.group_invitations[0].can_cancel;delete fixture.overview.group_invitations[0].can_refresh;}
if(variant==='terminal-card'){
 fixture.thread.guests=[];fixture.overview.group_invitations=[];
 fixture.thread.agents=[{pid:'own-agent',host:fixture.overview.person,agent_id:'a'.repeat(32),state:'active',invited:'2026-10-05T10:00:00Z',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,tasks_from:[]}];
 fixture.thread.messages.push({id:'stopped-exact',lid:'stopped-exact',from:fixture.overview.me.address,dir:'out',kind:'task',body:'Translate contribution margin. '+('Keep the full original instructions. '.repeat(40)),at:'2026-10-05T10:01:00Z',pid:'own-agent',target:{address:fixture.overview.me.address,agent_id:'a'.repeat(32)},exec:{state:'cancelled',host:fixture.overview.me.address},job_state:'cancelled',job_detail:'Cancelled by you. Nothing else should run.',actions:['accept'],state:'delivered'});
}
if(variant==='guest-oks'||variant==='guest-inline'){
 fixture.thread.role='human_guest';fixture.thread.agents=[];fixture.overview.group_invitations=[];
 fixture.thread.guests=[{pid:'human-invite',host:fixture.overview.person,inviter:fixture.thread.members[1],state:'invited',host_here:true,can_decide:true,shared:[]}];
 fixture.overview.needs_you=[{conv:fixture.thread.id,pid:'human-invite',role:'human',peer:fixture.thread.members[1].address,reason:'agent_invite',excerpt:'Join this conversation',why:'Invited you to join as a guest',actions:['accept','decline'],at:'2026-10-05T10:00:00Z'}];
}
if(variant==='guest-inline'){
 window.setInlineInvitation=(state,decidable=true,pid='human-invite')=>{
  const g={pid,host:fixture.overview.person,inviter:fixture.thread.members[1],state,host_here:true,can_decide:state==='invited'&&decidable,can_send:state==='active',can_leave:state==='active',shared:[],held:decidable?0:1};
  fixture.thread.guests=state?[g]:[];
  fixture.overview.needs_you=g.can_decide?[{conv:fixture.thread.id,pid:g.pid,role:'human',peer:g.inviter.address,reason:'agent_invite',excerpt:'Join this conversation',why:'Invited you to join as a guest',actions:['accept','decline'],at:'2026-10-05T10:00:00Z'}]:[];
  window.reloadFixture?.();
 };
}

if(variant==='render-perf'){
 fixture.thread.guests=[];fixture.thread.agents=[];fixture.overview.group_invitations=[];
 fixture.thread.messages=Array.from({length:1200},(_,i)=>({id:'perf-'+i,lid:'perf-'+i,from:'brin/desktop',dir:'in',kind:'message',body:i===0?'Selected warehouse context':'Retained message '+i,at:new Date(Date.parse('2026-10-05T10:00:00Z')+i*1000).toISOString(),unread:false,actions:[]}));
 fixture.overview.dms[0].count=fixture.thread.messages.length;
 fixture.profile={formats:0,inputs:[],measuring:false};
 const format=Date.prototype.toLocaleTimeString;
 Date.prototype.toLocaleTimeString=function(...args){if(fixture.profile.measuring)fixture.profile.formats++;return Reflect.apply(format,this,args);};
 document.addEventListener('input',event=>{if(!fixture.profile.measuring||!event.composedPath().some(node=>node?.tagName==='TEXTAREA'))return;const start=performance.now();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.profile.inputs.push(performance.now()-start)));},true);
}
if(variant==='multi-agent'||variant==='team-tags'){
fixture.thread.guests=[];fixture.overview.group_invitations=[];fixture.stages=[];fixture.failedOnce=false;
fixture.thread.agents=['agent-a','agent-b'].map((pid,i)=>({pid,host:fixture.thread.members[1],agent_id:String(i+1).repeat(32),state:'active',state_text:'Active',host_here:false,shared:[],can_ask:true,inviter:fixture.overview.person,invited:'2026-10-05T10:00:00Z',tasks_from:[]}));
if(variant==='multi-agent'&&${JSON.stringify(process.env.AGENTNET_COLLECTIVE_TAGS==='1')}){
 // Real rendered composer candidates: 1,000 current humans, including self,
 // plus two existing agents. Only those two agents create request jobs.
 fixture.thread.members.push(...Array.from({length:998},(_,i)=>{
  const person=(10000+i).toString(16).padStart(32,'0'),address='stress'+i+'/desktop',fingerprint='stress-key-'+i;
  return {person,label:'Participant '+i,address,fingerprint,state:'pinned',devices:[{address,fingerprint}]};
 }));
 fixture.teamSnapshotEdited=localStorage.getItem('fixture-team-snapshot-edited')==='1';
}

}
if(variant==='phone-rejoin'){
 fixture.thread.guests=[];fixture.overview.group_invitations=[];
 fixture.overview.agent_devices=[fixture.thread.members[1].address];
 fixture.otherThread={...fixture.thread,id:'d'.repeat(64),title:'other_dispatch',agents:[],guests:[],messages:[]};
 fixture.overview.dms.push({id:fixture.otherThread.id,kind:'group',title:fixture.otherThread.title,peer:fixture.otherThread.peer,count:0,unread:0});
 window.openOtherGroup=()=>open(fixture.otherThread.id,'conversation');
 fixture.thread.agents=[{pid:'old-agent',host:fixture.thread.members[1],agent_id:'1'.repeat(32),state:'active',state_text:'Active',host_here:false,shared:['old-grant'],can_ask:true,can_dismiss:true,inviter:fixture.overview.person,invited:'2026-10-05T10:00:00Z',tasks_from:[]}];
 window.rejoinFixture=(state,notify=true,identity='same')=>{
  const old=fixture.thread.agents[0],host={...old.host};
  if(identity==='key')host.fingerprint='changed-key';
  if(identity==='host')host.address='brin/other-device';
  fixture.thread.agents=[old,{...old,pid:'new-agent',host,agent_id:identity==='name'?'2'.repeat(32):old.agent_id,state,state_text:state==='active'?'Active':'Invited',shared:['new-grant'],can_dismiss:false,can_ask:state==='active'}];
  if(notify)changed?.({type:'change',seq:++fixture.overview.seq});
 };
}
if(variant==='heldback'){
 fixture.overview.group_invitations=[];fixture.overview.review=[];fixture.overview.needs_you=[];
 fixture.overview.quarantine=[
 {id:'legacy-invalid',peer:'brin/desktop',code:'invalid',reason:'invalid',detail:'The original detailed reason was not recorded or is unavailable.',recovery:'The retained message stays blocked. You can archive this notice locally; this does not accept, resend, or run it.',at:'2026-10-05T10:00:00Z',can_archive:true},
 {id:'invalid-ended-invite',peer:'brin/desktop',code:'invalid',reason:'invalid',detail_code:'group_invitation_outdated',detail:'This invitation no longer matches the current group.',recovery:'If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice.',can_archive:true,at:'2026-10-05T10:01:00Z'},
 {id:'legacy-unknown',peer:'brin/desktop',code:'old_unknown_code',reason:'legacy unknown',at:'2026-10-05T10:02:00Z'},
 {id:'proof-waiting',peer:'brin/desktop',code:'proof_pending',reason:'proof_pending',can_archive:false,at:'2026-10-05T10:03:00Z'}];
 fixture.retainedHeld=structuredClone(fixture.overview.quarantine);
}
if(variant==='held-flood'){
 // t5-held: an older own device re-sends a few records under new IDs; its
 // copies opened under its own key. Plus copies waiting on an invitation and
 // unverified legacy rows with sizes only. 326 notices: over one batch. And
 // three different messages from a changed key, equally long: a trust
 // decision, never "re-sent".
 fixture.overview.group_invitations=[];fixture.overview.review=[];fixture.overview.needs_you=[];fixture.batches=[];
 fixture.overview.person.devices.push({address:'aster/bezos',fingerprint:'bezos-key'});
 // Cora's Windows has a device conversation here, so its changed key is a
 // decision this computer can make; without one it is only a diagnostic
 // (heldDecisionPeers, e2bebdb2).
 fixture.overview.threads=[{id:'9'.repeat(32),peer:'cora/windows',title:'Dock schedule',last:'Dock schedule',last_at:'2026-10-05T06:00:00Z',count:1,unread:0,state:'active'}];
 const hex=i=>(i+1).toString(16).padStart(32,'0'),record=k=>String(k).repeat(32);
 fixture.overview.quarantine=[
  ...Array.from({length:300},(_,i)=>({id:hex(i),peer:'aster/bezos',code:'invalid',detail_code:'participation_binding_mismatch',sender_verified:true,action:'update_sender',logical:record(i%4),size:2840+36*(i%4),detail:'An internal invitation or membership record does not match its sending device or conversation.',recovery:'It stays blocked and grants no access. Older AgentNet versions forward such records under the wrong device. Update AgentNet on the sending device to stop new copies.',at:new Date(Date.UTC(2026,9,5,10,0,i)).toISOString(),can_archive:true})),
  ...Array.from({length:6},(_,i)=>({id:hex(1000+i),peer:'aster/bezos',code:'proof_pending',detail_code:'participation_invite_unresolved',sender_verified:true,action:'wait_invitation',logical:record(5+i%2),size:2404,detail:'It belongs to an agent or guest participation whose invitation this device can’t resolve yet.',recovery:'It waits and runs nothing. Checks continue when invitations or membership change here.',at:'2026-10-05T09:00:00Z',can_archive:true})),
  ...Array.from({length:20},(_,i)=>({id:hex(2000+i),peer:'claimed/remote',code:'invalid',detail_code:'',size:2000+i%2,detail:'The original detailed reason was not recorded or is unavailable.',recovery:'The retained message stays blocked.',at:'2026-10-05T08:00:00Z',can_archive:true})),
  ...Array.from({length:3},(_,i)=>({id:hex(3000+i),peer:'cora/windows',code:'key_changed',reason:'key_changed',size:1200,detail:'',recovery:'',at:new Date(Date.UTC(2026,9,5,7,0,i)).toISOString(),can_archive:false}))];
 fixture.retainedHeld=structuredClone(fixture.overview.quarantine);
}
if(variant==='team-tags'){fixture.teams=[];fixture.overview.people=[...fixture.overview.people,fixture.thread.members[1]];}
if(variant==='team-legacy'){
 fixture.teams=[{id:'e'.repeat(32),name:'Retained legacy',members:[fixture.overview.person.person],managers:[fixture.overview.person.person],agents:[],manager:true,member:true,listed:true,conflict:false,archived:false}];
 fixture.overview.people=[...fixture.overview.people,fixture.thread.members[1]];
}
if(variant==='agent-child'){
 fixture.thread.guests=[];fixture.overview.group_invitations=[];
 fixture.thread.agents=['source','target'].map((pid,i)=>({pid,host:fixture.overview.person,agent_id:String(i+1).repeat(32),state:'active',host_here:true,shared:[],can_ask:true,inviter:fixture.overview.person,tasks_from:[]}));
 fixture.thread.messages.push({id:'agent-child',lid:'agent-child',from:fixture.overview.me.address,dir:'out',kind:'question',body:'Child request from a verified agent',at:'2026-10-05T10:01:00Z',pid:'target',agent_author_pid:'source',verified_agent:true,target:{address:fixture.overview.me.address,agent_id:'2'.repeat(32)},exec:{state:'interrupted',host:fixture.overview.me.address},job_detail:'Cannot run this request again: originating local run stopped. Mark it as handled if no more work is needed.',actions:['resolve'],state:'delivered'});
}


// Bounded v32 qualification uses the actual current-source Comic module.
if(variant==='v32-attention'||variant==='v32-landing'||variant==='v32-readonly'){
 fixture.thread.guests=[];fixture.overview.group_invitations=[];
 fixture.thread.topics=[{id:'9'.repeat(32),title:'Sibling topic',state:'active',count:1,last_at:seed.thread.messages[0].at}];
 fixture.thread.messages=Array.from({length:180},(_,i)=>({id:(i+1).toString(16).padStart(32,'0'),lid:(i+1).toString(16).padStart(32,'0'),from:i%2?'aster/laptop':'brin/desktop',dir:i%2?'out':'in',kind:'message',body:i===179?'Selected warehouse context':'History turn '+i,at:new Date(Date.parse('2026-10-05T10:00:00Z')+i*1000).toISOString(),unread:false}));
 Object.assign(fixture.thread.messages[177],{attachments:[{name:'retained.txt',size:12,id:'fixture-file'}]});
 Object.assign(fixture.thread.messages[178],{excerpt_pid:'stored-grant',claimed_key:'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb',lid:'8'.repeat(32),body:'Admitted stored excerpt'});
 fixture.thread.messages.push({id:'9'.repeat(32),lid:'9'.repeat(32),kind:'message',dir:'in',from:'brin/desktop',topic:'9'.repeat(32),body:'Sibling stays unread',at:'2026-10-05T10:03:00Z',unread:true,attention:{rev:8,writer:'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb'}});
 fixture.overview.dms[0].count=181;fixture.overview.dms[0].unread=1;
 fixture.attentionRevs={};
 if(variant==='v32-readonly')fixture.thread.guests=[{...${JSON.stringify(guest)},missing:0,shared:[fixture.thread.messages[177].lid]}];
}
if(variant.startsWith('v32-direct-')){
 const id='1'.repeat(32),peer='brin/desktop',key='bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb',author='eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee';
 fixture.overview.group_invitations=[];fixture.overview.dms=[];fixture.overview.agent_devices=[peer];
 fixture.overview.person.devices.push({address:'aster/other-laptop',fingerprint:author});
 const summary={id,peer,title:'Current direct work',last:'Direct running request',count:1,unread:0,state:'active'};
 const named=variant.includes('named'),target=named?{address:peer,fingerprint:key,agent_id:'a'.repeat(32)}:undefined;
 fixture.overview.threads=[summary];
 fixture.deviceThread={id,peer,key:{pinned:key},approved:true,topic:summary,messages:[{id,from:'aster/other-laptop',from_key:author,to:peer,dir:'out',kind:'question',body:'Direct running request',at:'2026-10-05T10:00:00Z',author:{label:'You',about:''},target,exec:{state:'running',host:peer,attempt:3},state:'delivered',actions:[]}]};
 fixture.followupStages=[];
}

let open;
const changeListeners=new Set(),changed=event=>{for(const fn of [...changeListeners])fn(event);};
const host={version:1,platform:(variant==='composer-followup-android'||variant.endsWith('-android'))?'android':'daemon',android:(variant==='composer-followup-android'||variant.endsWith('-android'))?{legacyDrafts:async()=>({drafts:[]}),visible:()=>true,onVisibility:()=>()=>{}}:undefined,workspace:{id:'default',name:'P6 fixture',endpoint:location.origin,address:seed.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changeListeners.add(fn);return()=>changeListeners.delete(fn);},stage:async f=>{if(variant==='followup'||variant.startsWith('composer-followup')){if(fixture.followupStageFailure&&f.name==='fail-second.md'){fixture.followupStageFailure=false;throw Error('Synthetic second stage failure');}const id='followup-staged-'+(fixture.followupStages.length+1);fixture.followupStages.push({id,name:f.name,size:f.size,text:await f.text()});if(fixture.followupStagePause){fixture.followupStagePause=false;await new Promise(resolve=>{window.finishFollowupStage=resolve;});}return id;}if(variant==='multi-agent'){const id='multi-stage-'+(fixture.stages.length+1);fixture.stages.push({id,name:f.name,size:f.size});return {id};}if(variant==='oks'){fixture.staged={name:f.name,size:f.size};return {id:'staged-1'};}throw Error('fixture accepts no files');},file:async(id,index,dir)=>{if(variant==='direct-files'){fixture.download={id,index,dir};return {bytes:new TextEncoder().encode('hello')};}throw Error('fixture contains no files');},api:async(p,body)=>{
fixture.requests.push({path:p,body});

if(variant.startsWith('v32-direct-')&&p.startsWith('/api/thread?'))return structuredClone(fixture.deviceThread);
if(variant.startsWith('v32-direct-')&&p==='/api/request/followup'){
 if(variant.endsWith('-android')&&!fixture.directFailed){fixture.directFailed=true;throw Error('Synthetic uncertain direct steering');}
 return {note:'Steering sent. Waiting for native acceptance.'};
}
if((variant==='v32-attention'||variant==='v32-landing')&&p.startsWith('/api/dm?')&&fixture.holdNextDM){
 fixture.holdNextDM=false;const snapshot=structuredClone(fixture.thread);return new Promise(resolve=>{window.finishV32DM=()=>resolve(snapshot);});
}
if((variant==='v32-attention'||variant==='v32-landing')&&p==='/api/act'){
 if(!['read','unread'].includes(body.do))throw Error('Unexpected attention action');
 for(const m of fixture.thread.messages)if(body.ids.includes(m.id)){
  if(body.do==='unread'){const rev=(fixture.attentionRevs[m.id]||0)+1;fixture.attentionRevs[m.id]=rev;m.unread=true;m.attention={rev,writer:'aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa'};}
  else if(!m.attention||JSON.stringify(body.attention?.[m.id])===JSON.stringify(m.attention)){m.unread=false;delete m.attention;}
 }
 fixture.overview.dms[0].unread=fixture.thread.messages.filter(m=>m.unread).length;
 return {};
}

if((variant==='followup'||variant.startsWith('composer-followup'))&&p==='/api/upload/discard')return {};
if((variant==='followup'||variant.startsWith('composer-followup'))&&p==='/api/request/followup'){
 if(fixture.rejectEndedSteering)throw Error('The selected current work ended; steering was not applied.');
 if(variant.startsWith('composer-followup') && body.attempt!==fixture.thread.messages.find(m=>(m.lid||m.id)===body.ref.id)?.exec?.attempt)throw Error('Steering must retain the exact host attempt');
 if(!fixture.followupFailed){fixture.followupFailed=true;throw Error('Synthetic uncertain response; retry the same follow-up');}
 fixture.followupSent=true;return {note:'Correction sent; pending native acceptance.'};
}

if(variant.startsWith('composer-followup')&&p==='/api/dm/agent/ask')return {id:body.id,lid:body.id,state:'custody'};
if(variant==='reactions'&&p==='/api/message/react'){
 if(body.conv!==fixture.thread.id||body.id!=='context'||body.dir!=='in')throw Error('Unexpected reaction target');
 if(fixture.failReaction){fixture.failReaction=false;throw Error('Synthetic reaction failed');}
 if(fixture.holdReaction)await new Promise(resolve=>{window.finishReaction=()=>{fixture.holdReaction=false;resolve();};});
 const m=fixture.thread.messages[0];let r=m.reactions.find(r=>r.emoji===body.emoji);
 if(!r){r={emoji:body.emoji,by:[]};m.reactions.push(r);}
 if(body.remove){r.by=r.by.filter(b=>b.id!=='me');r.mine=false;}
 else if(!r.mine){r.by.push({id:'me',label:'Aster'});r.mine=true;}
 return {note:'Reaction saved'};
}
if(variant.startsWith('continuation-')&&['/api/act','/api/operator/decide'].includes(p)){
 const c=fixture.thread.messages.find(m=>m.id==='needs-answer-exact').continuation;
 if(variant==='continuation-resolve'&&body.check){
  if(body.check!==true||body.action!=='resolve'||body.host!==c.host||body.id!==c.id||body.key!==c.key||body.attempt!==c.attempt||body.send_id)throw Error('Unexpected read-only resolution check');
  if(fixture.resolveUnsupported)throw Error('peer_update: Other laptop cannot handle own requests yet. Update AgentNet on that computer and reconnect all its open sessions.');
  return {note:'The host can receive this decision. Nothing has been sent.'};
 }
 if(!c||body.id!==c.id||body.key!==c.key||body.attempt!==c.attempt||!body.send_id)throw Error('Unexpected continuation identity');
 if(!fixture.continuationFailed){fixture.continuationFailed=true;throw Error('Synthetic lost transport response; retry this same answer');}
 if(variant==='continuation-resolve'){
  if(p!=='/api/operator/decide'||body.action!=='resolve'||body.expect!=='needs_human'||body.report!==''||body.text)throw Error('Unexpected resolution action');
  fixture.resolutionQueued=true;return {note:'Decision queued. Waiting for the host to confirm.'};
 }
 const m=fixture.thread.messages.find(m=>m.id===c.id);m.state='running';m.job_state='running';m.actions=['cancel'];delete m.continuation;
 fixture.overview.needs_you=[];changed?.({type:'change',seq:++fixture.overview.seq});return {note:'Answer submitted to the same request.'};
}
if(variant==='phone-rejoin'&&p==='/api/dm/agent/dismiss'){
 if(body.pid!=='old-agent')throw Error('Unexpected dismissal identity');
 Object.assign(fixture.thread.agents[0],{state:'dismissed',state_text:'Dismissed',can_dismiss:false,can_ask:false});
 return structuredClone(fixture.thread.agents[0]);
}
if(variant==='phone-rejoin'&&p.startsWith('/api/dm?')&&fixture.failDM)throw Error('Synthetic conversation unavailable');
if(variant==='phone-rejoin'&&p.startsWith('/api/dm?')){
 if(new URL(p,location.origin).searchParams.get('id')===fixture.otherThread.id){
  if(fixture.holdOtherDM)return new Promise(resolve=>{window.finishOtherRead=()=>resolve(structuredClone(fixture.otherThread));});
  return structuredClone(fixture.otherThread);
 }
 if(fixture.holdDM){
  const snapshot=structuredClone(fixture.thread);
  return new Promise((resolve,reject)=>{window.finishRejoinRead=()=>fixture.rejectDM?reject(Error('STALE_REJOIN_READ_ERROR')):resolve(snapshot);});
 }
}
if(variant.startsWith('decline-invite-')&&p==='/api/groups/decide'){
 if(body.id!=='inbound-exact'||body.accept!==false)throw Error('Unexpected invitation decision');
 fixture.overview.group_invitations=[];changed?.({type:'change',seq:++fixture.overview.seq});return {};
}
if(variant==='held-flood'&&p==='/api/act'){
 if(body.do!=='archive_held_batch'||!body.held.length||body.held.length>256)throw Error('Unexpected batch archive');
 if(!fixture.archiveFailed){fixture.archiveFailed=true;throw Error('Synthetic archive failure');}
 fixture.batches.push(body.held.map(r=>r.id));
 if(fixture.batches.length===1){
  // Between batches one chosen copy changes cause and a new copy arrives:
  // the later snapshot must not hide either.
  const late=fixture.overview.quarantine.find(q=>q.id===(1).toString(16).padStart(32,'0'));late.detail_code='admission_failed';
  fixture.overview.quarantine.push({id:'f'.repeat(32),peer:'aster/bezos',code:'invalid',detail_code:'participation_binding_mismatch',sender_verified:true,action:'update_sender',logical:'0'.repeat(32),size:2840,detail:'New arrival',at:'2026-10-05T11:00:00Z',can_archive:true});
 }
 const before=fixture.overview.quarantine.length;
 fixture.overview.quarantine=fixture.overview.quarantine.filter(q=>!body.held.some(r=>r.id===q.id&&r.reason===q.code&&r.detail_code===(q.detail_code||'')));
 return {note:'Archived '+(before-fixture.overview.quarantine.length)+' notices on this device. Changed or newer notices stay visible. Nothing was accepted or run.'};
}
if(variant==='heldback'&&p==='/api/act'){
 if(body.do==='archive_held_batch'){
  if(![1,2].includes(body.held.length)||body.held.some(r=>!['legacy-invalid','invalid-ended-invite'].includes(r.id)))throw Error('Unexpected archive snapshot');
  fixture.overview.quarantine=fixture.overview.quarantine.filter(q=>!body.held.some(r=>r.id===q.id&&r.reason===q.code&&r.detail_code===(q.detail_code||'')));
  return {note:'Archived 2 notices on this device. Nothing was accepted or run.'};
 }
 if(body.do!=='archive_held'||body.id!=='invalid-ended-invite')throw Error('Unexpected or unauthorized held action');
 fixture.overview.quarantine=fixture.overview.quarantine.filter(q=>q.id!==body.id);
 return {note:'Notice archived on this device. Its envelope remains blocked.'};
}if(variant==='multi-agent'&&p==='/api/dm/agent/ask'){if(body.pid==='agent-b'&&!fixture.failedOnce){fixture.failedOnce=true;throw Error('Synthetic exact participation failure');}return {id:body.id,lid:body.id,state:'custody',state_text:'Stored for delivery'};}if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
if(['team-tags','team-legacy'].includes(variant)&&p==='/api/teams')return {status:'available',current:true,tags:true,legacy_add:true,teams:structuredClone(fixture.teams)};
if(['team-tags','team-legacy'].includes(variant)&&p==='/api/team'){
 if(body.op==='create'){const t={id:'f'.repeat(32),version:body.v,name:body.name,members:[],agents:[],managers:[fixture.overview.person.person],manager:true,member:false,listed:true,conflict:false,archived:false};fixture.teams.push(t);return structuredClone(t);}
 const t=fixture.teams.find(t=>t.id===body.team);if(!t)throw Error('Unknown tag');
 if(body.op==='add'){t.members.push(body.target);if(t.version!==2)t.version=3;}else if(body.op==='agent-add')t.agents.push(body.agent);else if(body.op==='agent-remove')t.agents=t.agents.filter(a=>a.id!==body.agent.id);else if(body.op==='rename')t.name=body.name;else throw Error('Unexpected tag change');
 return structuredClone(t);
}
if(p==='/api/teams')return {status:'available',current:true,tags:true,teams:variant==='multi-agent'?[{id:'f'.repeat(32),name:fixture.teamSnapshotEdited?'Changed reviewers':'Reviewers',version:2,members:fixture.teamSnapshotEdited?[]:[fixture.thread.members[1].person],agents:fixture.teamSnapshotEdited?[]:fixture.thread.agents.map(a=>({id:a.agent_id,host:a.host.address,host_key:a.host.fingerprint})),listed:true,conflict:false,archived:false,managers:[fixture.overview.person.person]}]:[]};
if(p==='/api/get-app')return {version:'v0.8.5',detected:'linux',platforms:[{id:'linux',label:'Linux',url:'https://downloads.example/AgentNet.AppImage'},{id:'windows',label:'Windows',url:'https://downloads.example/AgentNet.exe'}]};
if(p==='/api/device/link')return {url:'https://workspace.example/#agentnet-link-v2:fixture',app_url:'agentnet://open#agentnet-link-v2:fixture',expires:'2026-10-08T23:59:00Z'};
if((variant==='direct-files'||variant==='device-oks'||variant.startsWith('delivery-'))&&p.startsWith('/api/thread?'))return structuredClone(new URL(p,location.origin).searchParams.get('id')==='8'.repeat(32)?fixture.otherDeviceThread:fixture.deviceThread);
if(variant==='direct-files'&&p==='/api/file/request'){if(body.id!==fixture.deviceThread.id||body.index!==0)throw Error('wrong original file');fixture.deviceThread.messages[0].files[0].availability='requested';return {};}
if(variant==='device-oks'&&p==='/api/act'){if(body.do==='cancel')setDeviceState('cancel_requested');return {note:'Fixture action recorded'};}
if(p.startsWith('/api/agents'))return {host:body?.host||new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address,local:!p.includes('host='),agents:fixture.thread.agents.filter(a=>a.agent_id&&a.host.address===(new URL(p,location.origin).searchParams.get('host')||seed.overview.me.address)).map(a=>({record:{id:a.agent_id,label:variant==='agent-child'?(a.pid==='source'?'Codex':'Claude'):a.host.address===seed.overview.me.address?'Prospect':'Analyst',host:a.host.address,host_key:a.host.fingerprint},enabled:true})),sessions:[]};
if(p==='/api/dm/agent/invite'){fixture.thread.agents[0].shared=body.share;changed?.({type:'change',seq:++fixture.overview.seq});return structuredClone(fixture.thread.agents[0]);}
if(p==='/api/notify/allow'){fixture.overview.notify.allowed=body.allowed?['brin/desktop','brin/phone']:[];return {};}
if(p==='/api/notify/mute'){fixture.overview.notify.mutes=body.muted?[...fixture.overview.notify.mutes,body.conv]:fixture.overview.notify.mutes.filter(id=>id!==body.conv);return {};}
if(p==='/api/dm/guest/check')return {ready:true,text:'All conversation devices support human guests.'};
if(p==='/api/dm/guest/invite')return {};if(p==='/api/dm/send')return {id:body.id};
if(variant==='guest-inline'&&p==='/api/dm/guest/decide'){
 if(!fixture.thread.guests.find(g=>g.pid===body.pid)?.can_decide)throw Error('Stale or wrong guest decision');
 return new Promise(resolve=>{window.finishInlineDecision=()=>{setInlineInvitation(body.accept?'active':'declined',true,body.pid);resolve({});};});
}
if(p==='/api/dm/guest/decide'){Object.assign(fixture.thread.guests[0],{state:body.accept?'active':'declined',can_send:body.accept,can_decide:false,can_leave:body.accept});return {};}
if(p==='/api/dm/guest/end'){Object.assign(fixture.thread.guests[0],{state:'dismissed',can_send:false,can_leave:false,can_end:false});return {};}
if(p==='/api/groups/cancel'){fixture.overview.group_invitations[0].status='cancelled';return {};}
if(p==='/api/groups/refresh'){fixture.overview.group_invitations[0]={...fixture.overview.group_invitations[0],id:'fresh-invite',status:'pending'};return fixture.overview.group_invitations[0];}
if(p.includes('/groups/invitations'))return variant.startsWith('decline-invite-')?structuredClone(fixture.overview.group_invitations):[];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};return {};
}};
if(variant==='browser-app')host.platform='browser';
const skin=new URL(location.href).searchParams.get('skin'),base='/assets/skins/'+skin+'/';
const manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync((await(await fetch(base+manifest.document)).text()).replaceAll('url(m/','url('+base+'m/'));document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);
window.openGroup=()=>open(seed.thread.id,'conversation');window.v32Open=(id,kind,context)=>open(id,kind,context);window.v32Close=()=>open('');window.openFixtureMessage=id=>open(id,'message');window.reloadFixture=()=>changed?.({type:'change',seq:++fixture.overview.seq});
window.disableInvite=()=>{Object.assign(fixture.overview.group_invitations[0],{can_cancel:false,can_refresh:false});changed?.({type:'change',seq:++fixture.overview.seq});};window.ready=true;
if(new URL(location.href).searchParams.get('native')==='1'){
 const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))),sleep=ms=>new Promise(r=>setTimeout(r,ms));
 setTimeout(async()=>{let result;try{
  await openGroup();const dom=()=>document.querySelector('#skin').shadowRoot;
  for(let n=0;n<200&&dom().querySelectorAll('[data-mid]').length!==1200;n++)await sleep(25);
  await document.fonts.ready;await frame();
  if(dom().querySelectorAll('[data-mid]').length!==1200)throw Error('Native timeline did not load');
  const input=dom().querySelector('form[aria-label="Write a message"] textarea');
  fixture.requests=[];fixture.profile.formats=0;fixture.profile.inputs=[];fixture.profile.measuring=true;
  const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set;
  for(const text of ['Draft','Draft grows','Draft stays unsent']){setter.call(input,text);input.dispatchEvent(new Event('input',{bubbles:true,composed:true}));await frame();}
  const typed={...fixture.profile,inputs:[...fixture.profile.inputs]},start=performance.now();
  fixture.thread.messages.push({id:'perf-new',lid:'perf-new',from:'brin/desktop',dir:'in',kind:'message',body:'Newest arrival remains responsive',at:'2026-10-05T11:00:00Z',unread:false,actions:[]});reloadFixture();
  for(let n=0;n<200&&!dom().querySelector('[data-mid="perf-new"]');n++)await sleep(25);await frame();
  result={kind:'native-comic-render-profile',width:innerWidth,typed,arrivalMs:performance.now()-start,rows:dom().querySelectorAll('[data-mid]').length,draft:input.value,sent:fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask','/api/act'].includes(r.path))};
 }catch(e){result={error:String(e),stack:e.stack};}await fetch('/native-result',{method:'POST',body:JSON.stringify(result)});},100);
}

`;
let nativeFinish;
const server=http.createServer((req,res)=>{if(req.url==='/native-result'&&req.method==='POST'){let body="";req.on("data",chunk=>body+=chunk);req.on("end",()=>{fs.writeFileSync(path.join(evidence,"native-result.json"),body);res.end("{}");nativeFinish?.();});return;}const u=new URL(req.url,'http://127.0.0.1');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}res.statusCode=404;res.end('fixture route missing');});
(async()=>{
 let browser;const errors=[],shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  if(process.env.AGENTNET_NATIVE_FIXTURE==='1'){
   fs.writeFileSync(path.join(evidence,'native-url'),origin+'/?skin=comic&case=render-perf&native=1');
   await new Promise(resolve=>{nativeFinish=resolve;});return;
  }
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});

  if(process.env.AGENTNET_V32_ASSET_PROBE==='1'){
   const context=await browser.newContext({viewport:{width:1280,height:900}}),page=await context.newPage(),requests=[],consoleErrors=[];
   page.on('response',r=>{if(r.status()>=400)requests.push({url:r.url(),status:r.status()});});page.on('requestfailed',r=>requests.push({url:r.url(),failure:r.failure()}));page.on('console',m=>{if(m.type()==='error')consoleErrors.push(m.text());});
   try{await page.goto(origin+'/?skin=comic&case=v32-attention');await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());await settle(page);fs.writeFileSync(path.join(evidence,'asset-probe.json'),JSON.stringify({requests,consoleErrors},null,2));console.log(JSON.stringify({requests,consoleErrors}));}finally{await context.close();}return;
  }

  for(const skin of (process.env.AGENTNET_TEST_SKINS||'comic').split(','))for(const width of (process.env.AGENTNET_GUEST_INLINE==='1'?[1024,1279,390]:[1280,390])){
   const openCase=async (variant,platform)=>{const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'});await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());if(platform)await context.addInitScript(p=>Object.defineProperty(navigator,'platform',{get:()=>p}),platform);const page=await context.newPage();page.setDefaultTimeout(8000);page.on('pageerror',e=>errors.push(skin+': '+e.stack));if(process.env.AGENTNET_V32_RENDERED==='1')page.on('console',m=>{if(m.type()==='error')errors.push(skin+': '+m.text());});if(process.env.AGENTNET_V32_RENDERED==='1'){page.on('response',r=>{if(r.status()>=400)errors.push(skin+': HTTP '+r.status()+' '+r.url());});page.on('requestfailed',r=>errors.push(skin+': '+r.url()+' '+r.failure()?.errorText));}await page.goto(origin+'/?skin='+skin+'&case='+variant);try{await page.waitForFunction(()=>window.ready);}catch(e){console.error(JSON.stringify({skin,width,variant,errors,text:await page.locator('body').innerText()}));throw e;}if(variant.startsWith('v32-direct-')||variant==='completion'||variant==='direct-files'||variant==='device-oks'||variant.startsWith('delivery-')||variant.startsWith('decline-invite-'))return {page,context};await page.evaluate(()=>openGroup());try{await page.getByText(variant==='oks'?'Main flow conversation':'Selected warehouse context',{exact:false}).first().waitFor();}catch(e){console.error(JSON.stringify({errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent)}));throw e;}return {page,context};};

if(process.env.AGENTNET_V32_RENDERED==='1'){
 const findings=[];const fonts=await browser.newContext().then(async context=>{try{const page=await context.newPage();const urls=[];page.on('response',r=>{if(r.url().endsWith('.woff2'))urls.push({url:r.url(),status:r.status()});});await page.goto(origin+'/?skin=comic&case=v32-attention');await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());await settle(page);const loaded=await page.evaluate(()=>[...document.fonts].filter(f=>f.status==='loaded').map(f=>f.family));assert(loaded.length>=2,'both unchanged font families load through the corrected fixture routes');return{urls,loaded};}finally{await context.close();}});fs.writeFileSync(path.join(evidence,'v32-fonts-'+width+'.json'),JSON.stringify(fonts,null,2));
 const shot=async(page,name)=>{await settle(page);const file=path.join(evidence,'v32-'+name+'-'+width+'.png');await page.screenshot({path:file});shots.push(file);};
 const check=async(name,variant,run)=>{
  let page,context;const result={name,width,variant};
  try{({page,context}=await openCase(variant));await run(page);await settle(page);assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'no horizontal page overflow');result.ok=true;}
  catch(e){result.ok=false;result.error=e.stack;console.error('V32 FINDING',name,width,e.message);}
  finally{if(page){result.requests=await page.evaluate(()=>fixture.requests);result.focus=await page.locator('#skin').evaluate(e=>{const a=e.shadowRoot.activeElement;return a?{tag:a.tagName,text:a.innerText,html:a.outerHTML}:null;});fs.writeFileSync(path.join(evidence,'v32-'+name+'-'+width+'.dom.txt'),await page.locator('#skin').evaluate(e=>e.shadowRoot.innerHTML));await shot(page,name);}if(context)await context.close();findings.push(result);if(!result.ok)process.exitCode=1;}
 };
 const actions=async(page,id,{touch=false,keyboard=false}={})=>{
  const row=page.locator('[data-mid="'+id+'"]');await row.scrollIntoViewIfNeeded();
  if(width===390){
   if(touch){const bubble=row.locator(':scope > div').first();await bubble.dispatchEvent('pointerdown',{pointerType:'touch',pointerId:7,clientX:80,clientY:200,bubbles:true});await page.getByRole('dialog').waitFor();await bubble.dispatchEvent('pointerup',{pointerType:'touch',pointerId:7,bubbles:true});}
   else{const b=row.getByRole('button',{name:'Message actions',exact:true});await b.focus();await b.press('Enter');}
   return page.getByRole('dialog');
  }
  await row.hover();const b=row.getByRole('button',{name:'More actions',exact:true});await b.waitFor();if(keyboard){await b.focus();await b.press('Enter');}else await b.click();return page.getByRole('menu');
 };
 const unread=page=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body.do==='unread').map(r=>r.body));
 await check('unread-actions','v32-attention',async page=>{
  assert.equal(await page.locator('[data-mid]').count(),80,'latest eighty groups initially mounted');
  for(const [i,opts] of [[177,{touch:width===390}],[179,{keyboard:true}]]){
   const id=(i+1).toString(16).padStart(32,'0'),menu=await actions(page,id,opts);
   const item=menu.getByRole(width===390?'button':'menuitem',{name:'Mark unread',exact:true});await item.focus();await item.press('Enter');await menu.waitFor({state:'hidden'});
   await page.waitForFunction(id=>fixture.requests.some(r=>r.body?.do==='unread'&&r.body.ids[0]===id),id);
   assert.deepEqual((await unread(page)).at(-1).ids,[id]);
  }
  await page.evaluate(()=>reloadFixture());await settle(page);
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body?.do==='read').length),0,'current visit refresh keeps deliberate marks');
  await page.evaluate(()=>v32Close());await page.evaluate(()=>openGroup());
  await page.waitForFunction(()=>fixture.requests.some(r=>r.body?.do==='read'&&r.body.attention));
  const ack=await page.evaluate(()=>fixture.requests.find(r=>r.body?.do==='read'&&r.body.attention).body);
  assert.deepEqual(Object.keys(ack.attention).sort(),[178,180].map(i=>i.toString(16).padStart(32,'0')).sort());
  for(const g of Object.values(ack.attention))assert.deepEqual(g,{rev:1,writer:'aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa'});
  assert.equal(await page.evaluate(()=>fixture.thread.messages.at(-1).attention.rev),8,'sibling scope survives reopening Main');
 });
 await check('unread-excerpt','v32-attention',async page=>{
  const id=(179).toString(16).padStart(32,'0'),menu=await actions(page,id,{touch:width===390});
  await menu.getByRole(width===390?'button':'menuitem',{name:'Mark unread',exact:true}).click();await menu.waitFor({state:'hidden'});
  await page.waitForFunction(id=>fixture.requests.some(r=>r.body?.do==='unread'&&r.body.ids[0]===id),id);
  assert.deepEqual((await unread(page)).at(-1).ids,[id],'stored excerpt uses carrier ID');
 });
 await check('unread-keyboard-focus','v32-attention',async page=>{
  const id=(180).toString(16).padStart(32,'0'),menu=await actions(page,id,{keyboard:true});
  const item=menu.getByRole(width===390?'button':'menuitem',{name:'Mark unread',exact:true});await item.focus();await item.press('Enter');await menu.waitFor({state:'hidden'});await settle(page);
  const focused=await page.locator('#skin').evaluate(e=>{const a=e.shadowRoot.activeElement;return a?{tag:a.tagName,text:a.textContent,label:a.getAttribute('aria-label')}:null;});
  assert(focused&&focused.tag==='BUTTON'&&(width===1280?focused.label==='More actions':focused.text==='Message actions'),'closing keyboard action returns to its exact trigger');
 });
 await check('history-reveal-focus','v32-attention',async page=>{
  const earlier=page.getByRole('button',{name:'Earlier messages',exact:true});await earlier.focus();await settle(page);
  // Focusing the offscreen button scrolls to it and invokes the existing automatic earlier().
  assert.equal(await page.locator('[data-mid]').count(),160);assert(await earlier.evaluate(e=>e===e.getRootNode().activeElement),'Earlier retains focus while more history remains');
  const retained=page.locator('[data-mid]').first(),retainedID=await retained.getAttribute('data-mid'),beforeY=(await retained.boundingBox()).y;
  await earlier.press('Enter');await settle(page);assert.equal(await page.locator('[data-mid]').count(),180);assert.equal(await earlier.count(),0);
  const focused=await page.locator('#skin').evaluate(e=>{const a=e.shadowRoot.activeElement;return a&&{tag:a.tagName,html:a.outerHTML};});
  assert(focused&&focused.tag==='DIV'&&focused.html.includes('data-mid="'+retainedID+'"'),'final Earlier activation focuses its retained visible row');
  assert(Math.abs((await page.locator('[data-mid="'+retainedID+'"]').boundingBox()).y-beforeY)<=2,'final reveal retains scroll position');
 });

 await check('excerpt-keyboard-authority','v32-attention',async page=>{
  const id=(179).toString(16).padStart(32,'0'),row=page.locator('[data-mid="'+id+'"]');await row.scrollIntoViewIfNeeded();
  if(width===1280){const bubble=row.locator('[data-message-bubble]');assert.equal(await bubble.getAttribute('tabindex'),'0');assert.equal(await bubble.getAttribute('role'),'group');assert.match(await bubble.getAttribute('aria-label'),/Brin/);await bubble.focus();}
  const menu=await actions(page,id,{keyboard:true});
  assert.equal(await menu.getByRole(width===390?'button':'menuitem',{name:/^(Edit|Delete…|Reply|Select)$/}).count(),0,'attention does not grant excerpt edit/react/reply/select authority');
  const item=menu.getByRole(width===390?'button':'menuitem',{name:'Mark unread',exact:true});await item.focus();await shot(page,'excerpt-keyboard-menu');await item.press('Enter');await menu.waitFor({state:'hidden'});
  await page.waitForFunction(id=>fixture.requests.some(r=>r.body?.do==='unread'&&r.body.ids[0]===id),id);
 });
 await check('readonly-view-inert','v32-readonly',async page=>{
  await page.getByRole('button',{name:'orchard_dispatch. Who’s in this chat',exact:true}).click();await page.getByRole('button',{name:'What Dune saw',exact:true}).click();
  const dialog=page.getByRole('dialog',{name:'What Dune saw',exact:true});await dialog.waitFor();await dialog.getByText('History turn 177',{exact:true}).waitFor();
  assert.equal(await dialog.getByRole('button',{name:/Mark unread|Message actions|More actions/}).count(),0);assert.equal(await dialog.locator('[data-message-bubble][tabindex]').count(),0);
  assert.equal((await unread(page)).length,0,'read-only shared history dispatches no attention action');
 });
 await check('automatic-history-focus-neutral','v32-attention',async page=>{
  const field=page.locator('form[aria-label="Write a message"] textarea');await field.focus();
  await page.getByRole('log',{name:'Messages',exact:true}).evaluate(e=>{e.parentElement.scrollTop=0;e.parentElement.dispatchEvent(new Event('scroll',{bubbles:true}));});
  await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelectorAll('[data-mid]').length>80);await settle(page);
  assert(await field.evaluate(e=>e===e.getRootNode().activeElement),'automatic earlier() remains focus-neutral');
 });

 await check('unread-from-here','v32-attention',async page=>{
  await page.evaluate(()=>v32Open('1'.padStart(32,'0'),'message',{conv:fixture.thread.id,dir:'in'}));
  const row=page.locator('[data-mid="'+('1'.padStart(32,'0'))+'"]');await row.waitFor();await settle(page);
  assert.equal(await page.locator('[data-mid]').count(),180,'exact old target reveals complete retained Main history');
  const menu=await actions(page,'1'.padStart(32,'0'),{keyboard:true});await menu.getByRole(width===390?'button':'menuitem',{name:'Mark unread from here',exact:true}).click();await menu.waitFor({state:'hidden'});
  await page.waitForFunction(()=>fixture.requests.some(r=>r.body?.do==='unread'&&r.body.ids.length===180));
  assert.deepEqual((await unread(page)).at(-1).ids,Array.from({length:180},(_,i)=>(i+1).toString(16).padStart(32,'0')));
  assert.equal(await page.evaluate(()=>fixture.thread.messages.at(-1).attention.rev),8,'from-here excludes sibling topic');
 });
 await check('landing-arrival','v32-landing',async page=>{
  const id='f'.repeat(32);await page.evaluate(id=>v32Open(id,'message',{conv:fixture.thread.id,dir:'in'}),id);
  const retry=page.getByRole('button',{name:'Try again',exact:true}),dismiss=page.getByRole('button',{name:'Dismiss',exact:true});await retry.waitFor();await dismiss.waitFor();
  await retry.focus();await retry.press('Enter');await retry.waitFor();await settle(page);await shot(page,'landing-waiting');
  assert.match(await retry.locator('..').innerText(),/message|available|ready/i);
  await page.evaluate(id=>{fixture.thread.messages.splice(3,0,{...fixture.thread.messages[0],id,lid:id,body:'EXACT_NOTIFICATION_TARGET'});reloadFixture();},id);
  const target=page.locator('[data-mid="'+id+'"]');await target.waitFor();await retry.waitFor({state:'hidden'});await settle(page);
  assert.match(await target.innerText(),/EXACT_NOTIFICATION_TARGET/);assert(await target.evaluate(e=>{const r=e.getBoundingClientRect();return r.top>=0&&r.bottom<=innerHeight;}),'arrival scrolls exact old target into view');
 });
 await check('landing-cancel-supersede','v32-landing',async page=>{
  const a='e'.repeat(32),b='d'.repeat(32);
  await page.evaluate(id=>v32Open(id,'message',{conv:fixture.thread.id,dir:'in'}),a);await page.getByRole('button',{name:'Dismiss',exact:true}).waitFor();
  await page.getByRole('button',{name:'Dismiss',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Try again',exact:true}).waitFor({state:'hidden'});
  await page.evaluate(id=>{fixture.thread.messages.unshift({...fixture.thread.messages[0],id,lid:id,body:'Cancelled old target'});reloadFixture();},a);await settle(page);
  assert.equal(await page.locator('[data-mid="'+a+'"]').count(),0,'dismissed late arrival does not reveal old history');
  await page.evaluate(({a,b})=>{fixture.holdNextDM=true;void v32Open(a,'message',{conv:fixture.thread.id,dir:'in'});void v32Open(b,'message',{conv:fixture.thread.id,dir:'in'});},{a:'f'.repeat(32),b});
  await page.waitForFunction(()=>!!window.finishV32DM);await page.evaluate(()=>finishV32DM());
  await page.getByRole('button',{name:'Try again',exact:true}).waitFor();
  await page.evaluate(id=>{fixture.thread.messages.unshift({...fixture.thread.messages[0],id,lid:id,body:'Superseded old target'});reloadFixture();},'f'.repeat(32));await settle(page);
  assert.equal(await page.locator('[data-mid="'+('f'.repeat(32))+'"]').count(),0,'superseded arrival cannot hijack newer missing target');
 });
 for(const kind of ['default','named','named-android'])await check('direct-'+kind,'v32-direct-'+kind,async page=>{
  await page.evaluate(()=>v32Open(fixture.deviceThread.id,'message'));const field=page.locator('form[aria-label="Write a message"] textarea');await field.waitFor();
  await field.fill('Adjust the currently running direct request');await page.locator('form[aria-label="Write a message"] [data-part="send"]').click();
  await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/request/followup'));
  const call=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/request/followup').body);
  assert.equal(call.conv,'');assert.equal(call.attempt,3);assert.deepEqual(call.ref,{id:'1'.repeat(32),fingerprint:'eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee'});
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/send','/api/dm/agent/ask'].includes(r.path)).length),0,'ordinary send steers without new execution');
  if(kind.endsWith('android')){
   await page.getByRole('button',{name:/Saved messages.*Review/}).first().waitFor();
   const journal=await page.evaluate(()=>JSON.parse(localStorage.getItem('agentnet.android.send-journal.v1.default')));assert.equal(journal.length,1);assert.equal(journal[0].endpoint,'/api/request/followup');assert.equal(journal[0].request.attempt,3);assert.deepEqual(journal[0].request.ref,call.ref);
   await page.getByRole('button',{name:/Saved messages.*Review/}).first().click();const dialog=page.getByRole('dialog',{name:'Saved messages',exact:true});await dialog.getByRole('button',{name:'Retry saved send',exact:true}).click();
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===2);
   const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').map(r=>r.body));assert.deepEqual(calls[0],calls[1]);
  }
 });
 await check('invited-steering-retained','composer-followup',async page=>{
  await page.evaluate(()=>fixture.followupFailed=true);const field=page.locator('form[aria-label="Write a message"] textarea');await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' Continue this exact invited request');
  await page.locator('form[aria-label="Write a message"] [data-part="send"]').click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/request/followup'));
  const call=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/request/followup').body);assert.equal(call.conv,'c'.repeat(64));assert.equal(call.attempt,1);assert.deepEqual(call.ref,{id:'1'.repeat(32),fingerprint:'eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee'});
 });
 fs.writeFileSync(path.join(evidence,'v32-results-'+width+'.json'),JSON.stringify({width,findings,errors},null,2));
 continue;
}

if(process.env.AGENTNET_PRESENTATION_REGRESSION==='1'){
 const {page,context}=await openCase('presentation');
 try{
  for(const id of ['presentation-short','presentation-long']){
   const row=page.locator('[data-mid="'+id+'"]');await row.waitFor();
   assert(!/emotion:|reaction:|topic: done/.test(await row.innerText()),'Internal trailers are hidden before closure');
   const original=await page.evaluate(id=>fixture.thread.messages.find(m=>m.id===id).job_detail,id);
   await page.evaluate(id=>{const m=fixture.thread.messages.find(m=>m.id===id);m.state=m.job_state='resolved';m.actions=[];reloadFixture();},id);
   const card=row.locator('[data-request-result]');await card.waitFor();
   assert.equal(await card.getAttribute('open'),null,'Closed result starts compact');
   assert((await row.boundingBox()).height<220,'Long closed result stays compact');
   await card.locator('summary').click();
   await card.locator('summary').filter({hasText:/Prospect.*Request closed/}).waitFor();
   assert((await card.innerText()).includes(id==='presentation-short'?'Short exact request':'Long exact request'),'Exact request association retained');
   assert((await card.innerText()).includes(id==='presentation-short'?'Short reason':'FULL_RESULT_END'),'Complete human result remains accessible');
   assert(!/emotion:|reaction:|topic: done/.test(await card.innerText()),'Internal trailers are hidden after closure');
   assert.equal(await page.evaluate(id=>fixture.thread.messages.find(m=>m.id===id).job_detail,id),original,'Stored original bytes stay unchanged');
   assert.equal(await row.getByRole('button',{name:/^(Run it again|Ask again|Mark as handled)$/}).count(),0,'Closed result grants no execution controls');
  }
  assert(!await page.evaluate(()=>fixture.requests.some(r=>['/api/act','/api/send','/api/dm/send','/api/dm/agent/ask'].includes(r.path))),'Displaying and closing fixture state sends nothing');
 }finally{await context.close();}
 continue;
}
if(completion){
 for(const mode of ['grouped','single','unverified','excerpt','quote','wrong-target','unrelated']){
  const {page,context}=await openCase('completion');
  try{
   await page.evaluate(({mode,first,second,answer})=>{
    if(mode!=='grouped')for(const m of fixture.thread.messages)delete m.send_group;
    const exact=fixture.thread.messages.find(m=>m.id===answer),other=fixture.thread.messages.find(m=>m.id===second);
    if(!['grouped','single'].includes(mode)){
     const changed={...exact,id:'ineligible-completion',reply_to:other.id,pid:other.pid,from:other.target.address};
     if(mode==='unverified')changed.verified_agent=false;
     if(mode==='excerpt')Object.assign(changed,{excerpt_pid:other.pid,history:true});
     if(mode==='quote')Object.assign(changed,{kind:'message',quote:other.id,reply_to:''});
     if(mode==='wrong-target')changed.from=exact.from;
     if(mode==='unrelated')changed.reply_to='unrelated-exact-reference';
     fixture.thread.messages.push(changed);
    }
    openGroup();
   },{mode,first:completion.first,second:completion.second,answer:completion.answer});
   const first=page.locator('[data-mid="'+completion.first+'"]'),second=page.locator('[data-mid="'+completion.second+'"]');
   await first.waitFor();await second.waitFor();await settle(page);
   assert.match(await first.innerText(),/Answered/,'verified exact answer completes only its request');
   assert.match(await second.innerText(),/Sending/,'different target/inert/quoted/unrelated reply cannot complete second request');
   assert.equal(await first.getByText('Sending',{exact:true}).count(),0,'answered request has no contradictory sending tick');
   assert.equal(await second.getByText('Answered',{exact:true}).count(),0,'other exact target is still unanswered');
   if(mode==='single'){
    assert.match(await first.innerText(),/Grouped exact-agent question/,'original request body unchanged');
    if(width===390){const actions=first.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');}
    else{await first.hover();await first.getByRole('button',{name:'More actions',exact:true}).click();}
    await page.getByRole(width===390?'button':'menuitem',{name:'Details',exact:true}).click();
    const details=page.getByRole('dialog',{name:'Message details',exact:true});await details.waitFor();
    assert.equal(await details.getByText('Waiting to send from here',{exact:true}).count(),3,'three secondary transport copies remain queued');
    await details.getByText('Technical details',{exact:true}).click();
    assert.match(await details.innerText(),/Stored state\s+queued/,'no fabricated delivery/receipt state');await settle(page);
    const detailShot=path.join(evidence,'comic-request-completion-details-'+width+'.png');await page.screenshot({path:detailShot});shots.push(detailShot);
    await page.keyboard.press('Escape');await details.waitFor({state:'hidden'});await settle(page);
   }
   await page.locator('#skin').evaluate(e=>e.shadowRoot.activeElement?.blur());await page.mouse.move(0,0);await settle(page);
   const shot=path.join(evidence,'comic-request-completion-'+mode+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert.deepEqual(await page.evaluate(()=>fixture.thread.messages.filter(m=>m.kind==='question').map(m=>({body:m.body,delivery:m.delivery,copies:m.copies.map(c=>c.state)}))),completion.thread.messages.filter(m=>m.kind==='question').map(m=>({body:m.body,delivery:m.delivery,copies:m.copies.map(c=>c.state)})),'rendering preserves request body and all copy states');
   assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask','/api/act'].includes(r.path))),false,'viewing completion never sends/reruns work');
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_MENTION_SHORTCUT==='1'){
 for(const variant of ['oks','multi-agent','topic-invite']){
  const mac=variant==='oks',platform=mac?'MacIntel':'Linux x86_64';
  const {page,context}=await openCase(variant,platform);
  try{
   console.log('mention shortcut',variant,width,platform);
   if(variant==='topic-invite'){await page.getByRole('button',{name:'Warehouse',exact:true}).click();await page.locator('[data-mid="topic-message"]').waitFor();}
   const field=page.locator('textarea'),button=page.getByRole('button',{name:'Mention someone',exact:true}),picker=page.getByRole('listbox',{name:'Mention someone here',exact:true});
   const text='До SELECT после',choose=async()=>{await field.focus();await field.evaluate(e=>e.setSelectionRange(3,9));};
   const preserved=async()=>{assert.equal(await field.inputValue(),text,'opening picker preserves every draft byte');assert.deepEqual(await field.evaluate(e=>[e.selectionStart,e.selectionEnd]),[3,9],'selection/caret is unchanged');assert(await field.evaluate(e=>e===e.getRootNode().activeElement),'picker leaves focus in the field');};
   await field.fill(text);await choose();await button.click();await picker.waitFor();const permitted=await picker.getByRole('option').allTextContents();assert(permitted.length,'existing permitted candidates');await page.keyboard.press('Escape');await preserved();
   for(const shortcut of ['Control+Digit2','Meta+Digit2']){
    await choose();await page.keyboard.press(shortcut);await picker.waitFor();await preserved();assert.deepEqual(await picker.getByRole('option').allTextContents(),permitted,'shortcut reuses exact current permitted picker');await page.keyboard.press('Escape');
   }
   assert.equal(await button.getAttribute('title'),mac?'Mention someone (⌘2)':'Mention someone (Ctrl+2)','platform shortcut hint');
   assert.equal(await button.getAttribute('aria-keyshortcuts'),mac?'Meta+2':'Control+2');
   await choose();const russian=await field.evaluate(e=>{const k=new KeyboardEvent('keydown',{key:'ц',code:'Digit2',ctrlKey:true,bubbles:true,cancelable:true});e.dispatchEvent(k);return k.defaultPrevented;});assert(russian,'physical shortcut ignores layout character');await picker.waitFor();await preserved();await page.keyboard.press('Escape');
   for(const patch of [{isComposing:true,key:'Process'},{keyCode:229},{altKey:true},{shiftKey:true},{code:'Numpad2'}]){
    await choose();const prevented=await field.evaluate((e,patch)=>{const k=new KeyboardEvent('keydown',{key:'2',code:'Digit2',ctrlKey:true,bubbles:true,cancelable:true,...patch});e.dispatchEvent(k);return k.defaultPrevented;},patch);assert.equal(prevented,false,'IME/AltGr/Shift/nonphysical2 not intercepted');assert.equal(await picker.count(),0);await preserved();
   }
   await field.fill('base');await field.evaluate(e=>e.setSelectionRange(2,2));await field.press('Digit2');assert.equal(await field.inputValue(),'ba2se','ordinary2 types normally');assert.equal(await picker.count(),0);
   await field.fill(text);await choose();await field.press('Control+Digit2');await picker.waitFor();
   const brin=picker.getByRole('option').filter({has:page.getByText('Brin',{exact:true})}).first();await brin.click();assert.equal(await field.inputValue(),'До @Brin  после','selection is replaced at its original position only after choosing');
   assert.deepEqual(await field.evaluate(e=>[e.selectionStart,e.selectionEnd]),[9,9],'caret follows selected mention');
   if(variant==='topic-invite')assert.equal(await page.locator('[data-topic="'+('1'.repeat(32))+'"]').getAttribute('aria-current'),'true','current topic remains selected');
   assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/act','/api/dm/send','/api/dm/agent/ask','/api/send','/api/operator/decide'].includes(r.path))),false,'shortcut and selection never send or run work');
   await settle(page);const shot=path.join(evidence,'comic-mention-shortcut-'+variant+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_ATTENTION_LABELS==='1'){
 const {page,context}=await openCase('oks');
 try{
  const nav=page.getByRole('navigation',{name:'Main',exact:true});
  if(width<1024)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  const banner=page.getByRole('button',{name:/^2 need attention/});await banner.waitFor();
  assert.doesNotMatch(await banner.innerText(),/your OK|your answer/i,'mixed attention banner promises no approval or answer');
  assert.match(await page.locator('[data-part="chat-row"]').first().innerText(),/2 need attention/,'sidebar retains both unresolved items');
  await nav.getByRole('button',{name:/^Review/}).click();
  await page.getByRole('heading',{name:'Review',exact:true}).waitFor();await settle(page);
  assert.equal(await page.getByRole('heading',{name:'Review',exact:true}).locator('..').getByText('2 need attention',{exact:true}).count(),1,'same aggregate count in existing destination');
  await page.getByRole('button',{name:/^Your agent couldn’t finish/}).click();
  const blocked=page.locator('[data-mid="needs-person"]');await blocked.waitFor();
  await blocked.getByText('Needs attention',{exact:true}).waitFor();
  assert.doesNotMatch(await blocked.innerText(),/Needs your OK|needs your answer|Runs only if you allow/i,'runtime blocker is never a consent request');
  await blocked.getByRole('button',{name:'Ask again',exact:true}).waitFor();
  await blocked.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
  if(width<1024)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await nav.getByRole('button',{name:/^Review/}).click();
  await page.locator('[data-open]').filter({hasText:'Which carrier should we use?'}).click();
  const consent=page.locator('[data-mid="held-question"]');await consent.waitFor();
  await consent.getByText('Needs your OK',{exact:true}).waitFor();
  await consent.getByRole('button',{name:'Allow once',exact:true}).waitFor();
  await page.evaluate(()=>{fixture.overview.needs_you=fixture.overview.needs_you.filter(c=>c.reason==='agent_needs_human');reloadFixture();});
  if(width<1024)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await nav.getByRole('button',{name:/^Chats/}).click();
  await page.getByRole('button',{name:/^1 needs attention/}).waitFor();
  assert.match(await page.locator('[data-part="chat-row"]').first().innerText(),/Needs attention/,'a runtime-only badge remains visible');
  await page.evaluate(()=>{fixture.overview.needs_you[0].why='';fixture.thread.messages.find(m=>m.id==='needs-person').job_detail='';reloadFixture();});
  await page.getByRole('button',{name:/^1 needs attention/}).click();
  await blocked.getByText('The reason is not available on this device. Review the agent on its host computer.',{exact:true}).waitFor();
  assert.doesNotMatch(await blocked.innerText(),/needs a person to decide|needs your answer|This run has stopped/,'missing detail invents neither a decision nor a terminated native operation');
  await settle(page);const shot=path.join(evidence,'comic-attention-labels-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/act','/api/dm/send','/api/dm/agent/ask','/api/operator/decide'].includes(r.path))),false,'viewing wording never resolves, approves, retries or sends');
  assert.deepEqual(await page.evaluate(()=>fixture.thread.messages.filter(m=>['held-question','needs-person'].includes(m.id)).map(m=>({id:m.id,state:m.state,actions:m.actions}))),[{id:'held-question',state:'held',actions:['accept','decline','approve']},{id:'needs-person',state:'needs_human',actions:['resolve','accept','reply']}],'request states and actual actions stay exact');
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_GUEST_INLINE==='1'){
 const {page,context}=await openCase('guest-inline');
 try{
  const inline=page.getByRole('region',{name:'Invitations waiting for you',exact:true});
  const decisions=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/decide').map(r=>r.body));
  await settle(page);
  assert.equal(await inline.getByRole('button',{name:'Join',exact:true}).count(),1,'Pending invitation has inline Join without opening the optional sidebar');
  assert.equal(await page.getByRole('complementary',{name:'In this chat',exact:true}).count(),0,'Sidebar starts hidden below1280');
  assert.equal(await page.getByRole('dialog',{name:'In this chat',exact:true}).count(),0,'Mobile room sheet remains closed');
  const shot=path.join(evidence,'comic-guest-inline-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  if(width<1024)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  const banner=page.getByRole('button',{name:/^1 needs attention/});await banner.waitFor();
  assert.match(await banner.innerText(),/invited you into.*orchard_dispatch/s,'Human invitation names the person, not their agent');
  assert.match(await banner.innerText(),/Join when ready/);assert.doesNotMatch(await banner.innerText(),/your agent|It joins/);
  assert.equal(await banner.locator('[data-size="40"]').count(),1,'Human invitation uses person avatar');
  await page.evaluate(()=>{fixture.overview.needs_you[0].role='';reloadFixture();});
  await banner.getByText('It joins only when you say so',{exact:true}).waitFor();assert.match(await banner.innerText(),/invited your agent into/,'Agent invitation retains its original wording');
  assert.equal(await banner.locator('[data-size="40"]').count(),0,'Agent invitation retains robot avatar');
  await page.evaluate(()=>{fixture.overview.needs_you[0].role='human';reloadFixture();});await banner.getByText('Join when ready',{exact:true}).waitFor();
  await page.getByRole('button',{name:/^Review/}).click();
  await page.getByRole('button',{name:'Join conversation',exact:true}).waitFor({state:'visible'});
  assert.equal(await page.getByRole('button',{name:'Join conversation',exact:true}).count(),1,'One authoritative invitation in OKs');
  await page.evaluate(()=>openGroup());await inline.waitFor();
  await page.evaluate(()=>setInlineInvitation('dismissed'));await inline.waitFor({state:'hidden'});assert.deepEqual(await decisions(),[],'Withdrawal never sends a decision');
  await page.evaluate(()=>setInlineInvitation('invited',false));await settle(page);assert.equal(await inline.count(),0,'Unverified/nondecidable invitation has no inline action');
  await page.evaluate(()=>setInlineInvitation('active'));await settle(page);assert.equal(await inline.count(),0,'Already accepted invite has no second approval');
  await page.evaluate(()=>setInlineInvitation('invited'));await inline.waitFor();
  const join=inline.getByRole('button',{name:'Join',exact:true});await join.evaluate(b=>{b.click();b.click();});
  await page.waitForFunction(()=>!!window.finishInlineDecision);assert(await join.isDisabled(),'Decision stays busy while pending');
  assert.deepEqual(await decisions(),[{pid:'human-invite',accept:true}],'Double click sends one exact guest decision');
  await page.evaluate(()=>finishInlineDecision());await inline.waitFor({state:'hidden'});
  if(width<1024)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await page.getByRole('button',{name:/^Review/}).click();assert.equal(await page.getByRole('button',{name:'Join conversation',exact:true}).count(),0,'Resolution removes the same OKs item');
  await page.evaluate(()=>openGroup());await page.evaluate(()=>setInlineInvitation('invited',true,'fresh-human-invite'));await inline.waitFor();
  const decline=inline.getByRole('button',{name:'Decline',exact:true});await decline.focus();await page.keyboard.press('Enter');await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/decide').length===2);await page.evaluate(()=>finishInlineDecision());await inline.waitFor({state:'hidden'});
  assert.deepEqual(await decisions(),[{pid:'human-invite',accept:true},{pid:'fresh-human-invite',accept:false}],'Fresh invitation has exact keyboard Decline');
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/dm/agent/ask','/api/dm/agent/decide','/api/groups/decide'].includes(r.path)).length),0,'Guest decision performs no agent work or membership decision');
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_DIRECT_FILES_REGRESSION==='1'){
 const {page,context}=await openCase('direct-files');
 try{
  await page.evaluate(()=>openSyncedFiles());await page.getByText('retained.txt',{exact:true}).waitFor();
  const get=page.getByRole('button',{name:'Get it',exact:true});await get.click();
  await page.getByText(/asked your Laptop for it/).waitFor();assert.equal(await get.count(),0);
  const requests=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/file/request'));
  assert.deepEqual(requests.map(r=>r.body),[{id:'6'.repeat(32),index:0}]);
  await page.evaluate(()=>fileArrives());await page.getByRole('button',{name:'Download retained.txt',exact:true}).click();
  await page.waitForFunction(()=>fixture.download);assert.deepEqual(await page.evaluate(()=>fixture.download),{id:'6'.repeat(32),index:0,dir:'out'});
  assert(!await page.evaluate(()=>fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask'].includes(r.path))));
  const shot=path.join(evidence,'comic-direct-file-'+width+'.png');await settle(page);await page.screenshot({path:shot});shots.push(shot);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_REPORT_HISTORY_REGRESSION==='1'){
 assert.equal(skin,'comic');
 const {page,context}=await openCase('report-history');
 try{
  const original=page.locator('[data-mid="'+'3'.repeat(32)+'"]');
  await original.getByText(/A separate follow-up does not close it/).waitFor();
  await original.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='3'.repeat(32)).exec.state),'needs_human','same-outcome follow-up success cannot invent an exact resolution');
  if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).click();
  const reports=page.locator('div').filter({has:page.getByRole('button',{name:/^Reports from other computers/})}).last();
  await page.getByRole('button',{name:/^Reports from other computers/}).click();
  for(const mode of ['active','confirmed','refused','new-attempt','empty-iso','empty-tie']){
   await page.evaluate(mode=>{setReportHistory(mode);reloadFixture();},mode);
   if(mode.startsWith('empty')){
    await reports.getByText(/nothing waits any more/).waitFor();
    assert.doesNotMatch(await reports.innerText(),/OLD_BLOCKER_DETAIL|something waits for a person/,'Newest empty snapshot never revives old blockers');
    assert.equal(await reports.getByRole('button',{name:'Mark as handled',exact:true}).count(),0);
   }else{
    const row=reports.locator('li li').filter({hasText:'OLD_BLOCKER_DETAIL'});
    await row.waitFor();
    if(mode==='confirmed'){
     await row.getByText(/was closed/).first().waitFor();
     assert.doesNotMatch(await row.innerText(),/needs a person|Open it on/,'Confirmed resolution is history, with no live blocker claim');
     assert.equal(await row.getByRole('button',{name:'Mark as handled',exact:true}).count(),0);
     await row.getByText('Earlier blocker',{exact:true}).click();
     assert.match(await row.innerText(),/OLD_BLOCKER_DETAIL/,'Original evidence stays readable');
    }else{
     await row.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
     assert.match(await row.innerText(),/needs a person/,'Current or refused/new-attempt blocker keeps its explicit action');
    }
   }
   await settle(page);const shot=path.join(evidence,'comic-report-history-'+mode+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }
  assert.equal(await page.evaluate(()=>fixture.requests.some(r=>['/api/act','/api/operator/decide','/api/dm/send','/api/dm/agent/ask','/api/topic'].includes(r.path))),false,'Presentation never resolves, reruns or closes a topic');
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_TOPIC_INVITE_REGRESSION==='1'){
 assert.equal(skin,'comic');
 for(const room of ['group','dm'])for(const kind of ['main','person','agent'])for(const mode of ['recent','selected','since']){
  const {page,context}=await openCase(room==='dm'?'topic-invite-dm':'topic-invite');
  try{
   if(kind!=='main'){
    if(room==='dm'){
     await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/}).click();
     await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/Warehouse/}).click();
    }else await page.getByRole('button',{name:'Warehouse',exact:true}).click();
    await page.locator('[data-mid="topic-message"]').waitFor();
   }
   await page.getByRole('button',{name:width===1280?'Bring in':'Bring someone in',exact:true}).first().click();
   const dialog=page.getByRole('dialog',{name:'Bring someone in',exact:true});
   const scope=dialog.getByRole('radio',{name:kind==='main'?'Only this topic: Main flow Chosen history and future messages stay within this topic, even if it is renamed.':/Only this topic: Warehouse/});
   assert(await scope.isChecked(),'The visible topic, including Main, is captured before opening the invite');
   assert.equal(await dialog.locator('input[name=human-invite-mode]').count(),0,'Topic scope does not offer permanent full group membership');
   await dialog.getByRole('radio',{name:kind==='agent'?/Analyst/:/Cora/}).first().locator('..').click();
   await dialog.getByText(/already has a whole-chat invitation/).waitFor();
   const action=dialog.getByRole('button',{name:kind==='agent'?'Bring Analyst in':'Invite Cora as a guest',exact:true});
   await action.waitFor();
   if(mode==='selected'){
    await dialog.getByText('Selected messages',{exact:true}).click();
    await dialog.getByRole('checkbox',{name:kind==='main'?/Selected warehouse context/:/Warehouse topic context/}).locator('..').click();
   }else if(mode==='since'){
    await dialog.getByLabel('Share messages since',{exact:true}).fill('2026-10-05T00:00');
   }
   const expected=kind==='main'?mode==='selected'?['context']:['context','private']:['topic-message'];
   const preview=dialog.getByRole('region',{name:/ will see$/});
   assert.equal(await preview.locator('li').count(),expected.length,'Preview names exactly the messages sent');
   assert.doesNotMatch(await dialog.innerText(),/Unrelated private plans/,'Other-topic text never enters the chooser or preview');
   assert.equal(await dialog.getByRole('checkbox',{name:/Also share/}).count(),0,'Other-topic attachments never enter the shared file count');
   if(kind!=='main')assert.doesNotMatch(await dialog.innerText(),/Selected warehouse context|Unselected earlier history/,'Main history stays out of a named-topic invitation');
   if(kind==='person'&&mode==='since'){
    await page.evaluate(()=>{fixture.thread.topics[0].title='Renamed warehouse';fixture.thread.topics[0].state='archived';fixture.thread.messages.push({...fixture.thread.messages.at(-1),id:'new-other',lid:'new-other',body:'NEW_OTHER_TOPIC_PRIVATE'});reloadFixture();});
    await dialog.getByRole('radio',{name:/Only this topic: Renamed warehouse/}).waitFor();
    assert.equal(await preview.locator('li').count(),expected.length,'Renaming/archiving and concurrent other-topic arrival preserve the captured scope');
    assert.doesNotMatch(await dialog.innerText(),/NEW_OTHER_TOPIC_PRIVATE/);
   }
   const shot=path.join(evidence,'comic-topic-invite-'+room+'-'+kind+'-'+mode+'-'+width+'.png');await settle(page);await page.screenshot({path:shot});shots.push(shot);
   await action.click();
   const endpoint=kind==='agent'?'/api/dm/agent/invite':'/api/dm/guest/invite';
   await page.waitForFunction(p=>fixture.requests.some(r=>r.path===p),endpoint);
   const sent=await page.evaluate(p=>fixture.requests.find(r=>r.path===p).body,endpoint);
   assert.equal(sent.topic,kind==='main'?'':'1'.repeat(32));
   assert.deepEqual(sent.share,expected,'Recent, selected and since share only reviewed visible-topic history');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/groups/invite'||r.path==='/api/dm/agent/ask')),'Invite changes no group membership and runs no agent');
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'Topic choices fit phone width');
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_REACTION_REGRESSION==='1'){
 const {page,context}=await openCase('reactions');
 try{
  page.on('console',m=>{if(m.type()==='error')errors.push(skin+': '+m.text());});
  page.on('response',r=>{if(r.status()>=400)errors.push(skin+': HTTP '+r.status()+' '+r.url());});
  page.on('requestfailed',r=>errors.push(skin+': '+r.url()+' '+r.failure()?.errorText));
  const key='agentnet.messenger.reaction-usage.v1';
  const message=()=>page.locator('[data-mid="context"]');
  const enter=async()=>{await message().waitFor();};
  const chip=emoji=>message().getByRole('button',{name:new RegExp('^'+emoji+' (from|by) ')});
  const stored=()=>page.evaluate(key=>localStorage.getItem(key),key);
  const counts=async()=>JSON.parse(await stored()||'[]');
  const openPicker=async()=>{await message().getByLabel('Add a reaction',{exact:true}).click();await page.getByRole('button',{name:'React 👍',exact:true}).waitFor();};
  const choices=()=>page.getByRole('button',{name:/^React /}).evaluateAll(es=>es.map(e=>e.getAttribute('aria-label').slice(6)));
  const closePicker=async()=>{await page.keyboard.press('Escape');await settle(page);};
  await enter();
  const humanLabels=await message().getByRole('button',{name:/ (from|by) /}).allTextContents();
  assert.deepEqual(humanLabels.map(s=>s.trim().split(/\s|(?=[0-9])/)[0]),['👍','🎉','😂'],'Human chips sort by count with stable ties');
  assert.equal(await message().locator('[role="img"]').count(),2,'Agent marks remain separate, noninteractive chips');
  assert.equal(await stored(),null,'Receiving/rendering reactions never counts them');
  await openPicker();assert.deepEqual((await choices()).slice(0,6),['👍','❤️','😂','🎉','🙏','👀']);
  await page.locator('#skin').evaluate(el=>{const style=document.createElement('style');style.textContent='.an-root button.rounded-full{border-radius:6px}';el.shadowRoot.append(style);});
  const fits=()=>page.getByRole('group',{name:'React',exact:true}).evaluate(bar=>{
   const b=bar.getBoundingClientRect(),radius=Math.min(parseFloat(getComputedStyle(bar).borderTopLeftRadius),b.width/2,b.height/2);
   const inside=(x,y)=>{const cx=Math.max(b.left+radius,Math.min(x,b.right-radius)),cy=Math.max(b.top+radius,Math.min(y,b.bottom-radius));return Math.hypot(x-cx,y-cy)<=radius+.5;};
   return [...bar.querySelectorAll('button')].every(button=>{const r=button.getBoundingClientRect(),corner=Math.min(parseFloat(getComputedStyle(button).borderTopLeftRadius),r.width/2,r.height/2)*(1-1/Math.sqrt(2));return r.width>=44&&r.height>=44&&[[r.left+corner,r.top+corner],[r.right-corner,r.top+corner],[r.left+corner,r.bottom-corner],[r.right-corner,r.bottom-corner]].every(([x,y])=>inside(x,y));});
  });
  assert(await fits(),'Reaction paint fits rounded toolbar under custom button radii');
  const focus=page.getByRole('button',{name:'React 👍',exact:true});await focus.hover();await settle(page);assert(await fits(),'Hover paint stays inside rounded toolbar');await page.keyboard.press('Tab');await focus.focus();assert(await focus.evaluate(e=>getComputedStyle(e).outlineStyle!=='none'),'Visible keyboard focus remains');assert(await fits(),'Focus preserves backgrounds and 44px targets');
  await closePicker();
  await chip('😂').click();await page.waitForFunction(key=>JSON.parse(localStorage.getItem(key)||'[]').some(([e,n])=>e==='😂'&&n===1),key);
  await chip('😂').click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/message/react').at(-1)?.body.remove===true);await settle(page);
  assert.deepEqual(await counts(),[['😂',1]],'Removal does not increment usage');
  await page.evaluate(()=>{fixture.failReaction=true;});await chip('👍').click();await page.getByText('Synthetic reaction failed',{exact:true}).waitFor();assert.deepEqual(await counts(),[['😂',1]],'Failure does not increment usage');
  await page.evaluate(()=>{fixture.holdReaction=true;});await chip('👍').click();await page.waitForFunction(()=>!!window.finishReaction);assert.deepEqual(await counts(),[['😂',1]],'Pending calls do not count');await page.evaluate(()=>finishReaction());
  await page.waitForFunction(key=>JSON.parse(localStorage.getItem(key)||'[]').some(([e,n])=>e==='👍'&&n===1),key);await settle(page);
  await openPicker();assert.deepEqual((await choices()).slice(0,2),['😂','👍'],'Usage ties retain first-use order');
  // Comic toggles in its picker.
  await page.getByRole('button',{name:'React 👍',exact:true}).click();await settle(page);
  assert.deepEqual(await counts(),[['😂',1],['👍',1]],'An owned choice cannot count another addition');
  await chip('👍').click();await page.waitForFunction(key=>JSON.parse(localStorage.getItem(key)||'[]').some(([e,n])=>e==='👍'&&n===2),key);await settle(page);
  await openPicker();assert.deepEqual((await choices()).slice(0,2),['👍','😂']);
  assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'Picker does not overflow phone');
  await settle(page);let shot=path.join(evidence,skin+'-reactions-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  if(skin==='comic'){
   await page.getByRole('button',{name:'More reactions',exact:true}).click();
   const frequent=page.getByRole('group',{name:'Frequently used emoji',exact:true});await frequent.waitFor();
   await page.locator('[frimousse-emoji]').filter({visible:true}).first().waitFor();await settle(page);
   assert.deepEqual(await frequent.getByRole('button').allTextContents(),['👍','😂'],'Full picker puts frequently used exact emoji first');
   assert(await frequent.evaluate(e=>e.scrollWidth<=e.clientWidth),'Frequent row fits phone');
   shot=path.join(evidence,skin+'-full-reactions-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   await closePicker();
   const many=['👍','😂','🎉','❤️','👀','🙏','😀','😎','👩🏽‍💻','🙌','🥳','🤔','💯','🔥','✨','🚀','✅','🌈','🍀','🌻'];
   const savedMany=JSON.stringify(many.map((e,i)=>[e,30-i]));
   await page.evaluate(([key,value])=>localStorage.setItem(key,value),[key,savedMany]);
   const priorPicker=page.locator('[frimousse-root]'),reaction=page.getByRole('dialog',{name:'React to this message',exact:true});
   const picker=reaction.locator('[frimousse-root]'),recent=picker.getByRole('group',{name:'Frequently used emoji',exact:true});
   const full=async()=>{
    const quick=reaction.getByRole('button',{name:'React 👍',exact:true});
    await priorPicker.waitFor({state:'detached'});
    await message().getByLabel('Add a reaction',{exact:true}).click();
    await quick.or(picker).filter({visible:true}).first().waitFor();
    if(await quick.isVisible())await reaction.getByRole('button',{name:'More reactions',exact:true}).click();
    await picker.waitFor({state:'visible'});await picker.locator('[frimousse-emoji]').filter({visible:true}).first().waitFor();await settle(page);
   };
   await full();
   const loadedFonts=await page.evaluate(async()=>{await document.fonts.ready;return [...document.fonts].filter(f=>f.status==='loaded').map(f=>f.family);});
   assert(loadedFonts.length>=2,'Both unchanged font families load in the rendered picker');
   fs.writeFileSync(path.join(evidence,'emoji-fonts-'+width+'.json'),JSON.stringify(loadedFonts,null,2));
   const layout=await recent.evaluate(group=>{const g=group.getBoundingClientRect(),buttons=[...group.querySelectorAll('button')].map(e=>{const r=e.getBoundingClientRect();return {text:e.textContent,top:r.top,bottom:r.bottom,left:r.left,right:r.right,width:r.width,height:r.height};});return {width:g.width,scrollWidth:group.scrollWidth,visibleRows:[...new Set(buttons.filter(r=>r.top>=g.top&&r.bottom<=g.bottom+.5).map(r=>r.top))],buttons};});
   shot=path.join(evidence,skin+'-multiple-recent-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   fs.writeFileSync(path.join(evidence,'emoji-layout-'+width+'.json'),JSON.stringify(layout,null,2));
   assert(layout.visibleRows.length>=2,'Enough saved emoji show at least two complete visible rows');
   assert.deepEqual(await recent.getByRole('button').allTextContents(),many.slice(0,(width<440?7:8)*2),'Multiple rows retain existing frequency order and a bounded choice count');
   assert(layout.scrollWidth<=layout.width+.5&&layout.buttons.every(r=>r.width>=44&&r.height>=44),'Recent rows fit with 44px touch targets');
   assert.equal(await stored(),savedMany,'Opening the full picker never changes usage');
   const search=picker.getByRole('searchbox',{name:'Search emoji',exact:true});
   await search.fill('smile');await recent.waitFor({state:'hidden'});await picker.locator('[frimousse-emoji]').filter({visible:true}).first().waitFor();
   await search.fill('');await recent.waitFor();
   const categories=picker.getByRole('tablist',{name:'Emoji categories',exact:true});await categories.getByRole('tab').nth(1).click();assert.equal(await categories.getByRole('tab').nth(1).getAttribute('aria-selected'),'true');
   const exact=recent.getByRole('button',{name:'Choose 👩🏽‍💻',exact:true});await page.keyboard.press('Tab');await exact.focus();
   const keyboardFocus=await exact.evaluate(e=>({active:e.getRootNode().activeElement===e,outline:getComputedStyle(e).outlineStyle,focusVisible:e.matches(':focus-visible')}));
   fs.writeFileSync(path.join(evidence,'emoji-focus-'+width+'.json'),JSON.stringify(keyboardFocus,null,2));
   assert(keyboardFocus.active&&keyboardFocus.outline!=='none'&&keyboardFocus.focusVisible,'Second-row choice retains visible keyboard focus');
   await page.keyboard.press('Enter');await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/message/react').at(-1)?.body.emoji==='👩🏽‍💻');
   await full();assert.equal(await recent.getByRole('button',{name:'Choose 👩🏽‍💻',exact:true}).count(),1,'Exact toned/joined choice remains on reopen');await picker.getByRole('button',{name:'Close emoji',exact:true}).click();
   // The same component also inserts into the composer without sending.
   await priorPicker.waitFor({state:'detached'});
   await page.getByRole('button',{name:'Add to message',exact:true}).click();await page.getByRole('menuitem',{name:'Emoji',exact:true}).click();
   const composerPicker=page.getByRole('dialog',{name:'Emoji',exact:true}).locator('[frimousse-root]');await composerPicker.waitFor({state:'visible'});
   await composerPicker.getByRole('group',{name:'Frequently used emoji',exact:true}).getByRole('button',{name:'Choose 👩🏽‍💻',exact:true}).click();assert.equal(await page.getByRole('form',{name:'Write a message',exact:true}).getByRole('textbox').inputValue(),'👩🏽‍💻');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/dm/send')),'Choosing a recent emoji inserts without sending');
   if(width===390){
    await page.setViewportSize({width,height:400});await full();
    const accessible=await picker.evaluate(picker=>{const p=picker.getBoundingClientRect(),vp=picker.querySelector('[frimousse-viewport]').getBoundingClientRect();return {top:p.top,bottom:p.bottom,viewport:vp.height,inside:[...picker.querySelectorAll('input,[aria-label="Close emoji"],[role="tab"]')].every(e=>{const r=e.getBoundingClientRect();return r.top>=p.top&&r.bottom<=p.bottom+.5;}),overflow:document.documentElement.scrollWidth>innerWidth};});
    const geometry=await picker.evaluate(root=>{const box=e=>({tag:e.tagName,label:e.getAttribute('aria-label'),role:e.getAttribute('role'),rect:e.getBoundingClientRect().toJSON(),height:getComputedStyle(e).height,minHeight:getComputedStyle(e).minHeight,maxHeight:getComputedStyle(e).maxHeight,flexShrink:getComputedStyle(e).flexShrink,overflowY:getComputedStyle(e).overflowY,clientHeight:e.clientHeight,scrollHeight:e.scrollHeight});return {dialog:root.closest('[role="dialog"]')?.getAttribute('aria-label'),root:box(root),children:[...root.children].map(box),controls:[...root.querySelectorAll('input,[aria-label="Close emoji"],[role="tab"]')].map(box),html:root.outerHTML};});
    fs.writeFileSync(path.join(evidence,'emoji-short-geometry-'+width+'.json'),JSON.stringify({accessible,geometry},null,2));
    shot=path.join(evidence,skin+'-recent-short-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    assert(accessible.top>=0&&accessible.bottom<=400&&accessible.viewport>=44&&accessible.inside&&!accessible.overflow,'Short phone viewport keeps search, categories, dismissal and a grid row accessible');
    // A 44px viewport can initially contain a category heading. Scroll the
    // MAIN grid and prove an entire actual emoji target is reachable.
    const completeMain=async(button,label)=>{
     await button.evaluate(e=>{const vp=e.closest('[frimousse-viewport]');const r=e.getBoundingClientRect(),v=vp.getBoundingClientRect();vp.scrollTop+=r.top-v.top-Math.max(0,(v.height-r.height)/2);});
     let failure;
     try{await page.waitForFunction(e=>{const r=e.getBoundingClientRect(),v=e.closest('[frimousse-viewport]')?.getBoundingClientRect();const hit=e.getRootNode().elementFromPoint(r.left+r.width/2,r.top+r.height/2);return e.isConnected&&v&&r.height>=44&&r.top>=v.top-.5&&r.bottom<=v.bottom+.5&&(hit===e||e.contains(hit));},await button.elementHandle());}catch(e){failure=e;}
     const measured=await button.evaluate(e=>({emoji:e.textContent.trim(),connected:e.isConnected,button:e.getBoundingClientRect().toJSON(),viewport:e.closest('[frimousse-viewport]')?.getBoundingClientRect().toJSON(),active:e.hasAttribute('data-active'),centerHit:(()=>{const r=e.getBoundingClientRect(),hit=e.getRootNode().elementFromPoint(r.left+r.width/2,r.top+r.height/2);return {tag:hit?.tagName,label:hit?.getAttribute('aria-label'),inside:hit===e||e.contains(hit)};})()})).catch(e=>({error:String(e)}));
     fs.writeFileSync(path.join(evidence,'emoji-short-main-'+label+'.json'),JSON.stringify(measured,null,2));
     await page.screenshot({path:path.join(evidence,'emoji-short-main-'+label+'.png')});
     assert(!failure,'Complete44px main emoji target: '+JSON.stringify(measured));return measured.emoji;
    };
    const mainFirst=picker.locator('[frimousse-viewport] [frimousse-emoji][aria-label]:not([aria-label=""])').filter({visible:true}).first();
    const pointerLabel=await mainFirst.getAttribute('aria-label');
    const pointerButton=picker.getByRole('gridcell',{name:pointerLabel,exact:true});
    const pointerEmoji=await completeMain(pointerButton,'pointer');await pointerButton.click();
    await page.waitForFunction(emoji=>{const r=fixture.requests.filter(r=>r.path==='/api/message/react').at(-1);return r?.body.emoji===emoji&&!r.body.remove;},pointerEmoji);
    await full();
    await picker.getByRole('searchbox',{name:'Search emoji',exact:true}).focus();
    await page.keyboard.press('ArrowRight');await page.keyboard.press('ArrowRight');
    const activeMain=picker.locator('[frimousse-viewport] [frimousse-emoji][data-active]');await activeMain.waitFor();
    const keyboardEmoji=await completeMain(activeMain,'keyboard');
    await page.keyboard.press('Enter');
    await page.waitForFunction(emoji=>{const r=fixture.requests.filter(r=>r.path==='/api/message/react').at(-1);return r?.body.emoji===emoji&&!r.body.remove;},keyboardEmoji);
    assert.equal(await page.getByRole('form',{name:'Write a message',exact:true}).getByRole('textbox').inputValue(),'👩🏽‍💻','Short-grid reactions retain the composer draft');
    assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/dm/send')),'Short-grid pointer/arrow/Enter selection never sends the composer');
    fs.writeFileSync(path.join(evidence,'emoji-short-selection.json'),JSON.stringify({pointerEmoji,keyboardEmoji},null,2));
    await page.keyboard.press('Escape');await page.setViewportSize({width,height:900});
   }
   await page.evaluate(key=>localStorage.removeItem(key),key);await full();assert.equal(await recent.count(),0,'Empty history does not invent recent entries');await closePicker();
   await page.evaluate(key=>localStorage.setItem(key,JSON.stringify([['👍',2],['😂',1]])),key);
  }
  const usage=await stored();await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());await enter();await openPicker();
  assert.deepEqual((await choices()).slice(0,2),['👍','😂'],'Usage survives reload');assert.equal(await stored(),usage,'Reload/render does not increment');
  await closePicker();await page.evaluate(key=>localStorage.setItem(key,'{malformed'),key);await openPicker();assert.equal((await choices())[0],'👍','Malformed preference falls back');await closePicker();
  await page.evaluate(()=>{Storage.prototype.getItem=()=>{throw Error('Denied');};Storage.prototype.setItem=()=>{throw Error('Denied');};});await openPicker();assert.equal((await choices())[0],'👍','Denied storage falls back');await closePicker();
 }catch(error){
  await page.screenshot({path:path.join(evidence,'emoji-failure-'+width+'.png')}).catch(()=>{});
  const state=await page.evaluate(()=>({requests:fixture.requests,html:document.querySelector('#skin').shadowRoot.innerHTML})).catch(e=>({captureError:String(e)}));
  fs.writeFileSync(path.join(evidence,'emoji-failure-'+width+'.json'),JSON.stringify({error:String(error),state},null,2));throw error;
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_PHONE_REJOIN_REGRESSION==='1'){
 assert.equal(skin,'comic');if(width!==390)continue;
 for(const mode of ['navigate-loaded','navigate-pending','navigate-error','update-active','update-invited','click-active','click-invited','ended','key','name','host','error']){
  const {page,context}=await openCase('phone-rejoin');
  try{
   await page.getByRole('button',{name:'Dismiss',exact:true}).click();
   const notice=page.getByRole('status').filter({hasText:'left · dismissed by you'});
   const back=notice.getByRole('button',{name:'Bring back',exact:true});
   await back.waitFor();await settle(page);
   assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/dismiss').map(r=>r.body)),[{pid:'old-agent'}]);
   const before=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length);
   if(mode.startsWith('navigate-')){
    await page.evaluate(mode=>{fixture.holdDM=true;fixture.holdOtherDM=mode==='navigate-pending';fixture.rejectDM=mode==='navigate-error';},mode);
    await back.click();await page.waitForFunction(()=>!!window.finishRejoinRead);
    await page.evaluate(()=>openOtherGroup());
    if(mode==='navigate-pending')await page.waitForFunction(()=>!!window.finishOtherRead);
    else await page.locator('section[aria-label="other_dispatch"]').waitFor();
    await page.evaluate(()=>finishRejoinRead());await settle(page);
    assert.equal(await page.getByRole('dialog',{name:'Bring someone in',exact:true}).count(),0,'A delayed old-chat read cannot open an invitation after navigation');
    assert.equal(await page.getByText('STALE_REJOIN_READ_ERROR',{exact:true}).count(),0,'A failed old-chat read cannot toast over the new conversation');
    if(mode==='navigate-pending')await page.evaluate(()=>finishOtherRead());
   }else if(mode.startsWith('update-')){
    const state=mode.slice(7);await page.evaluate(state=>rejoinFixture(state),state);
    // Wait for the actual change-driven render, never for the 9-second snackbar expiry.
    await page.getByText(state==='active'?/^since /:'Waiting for Brin’s OK',{exact:true}).waitFor();
    await settle(page);assert.equal(await notice.count(),1,'Historical dismissal notice remains');
    assert.equal(await back.count(),0,'Already active/pending exact agent must not offer Bring back');
   }else{
    if(mode.startsWith('click-'))await page.evaluate(state=>rejoinFixture(state,false),mode.slice(6));
    else if(['key','name','host'].includes(mode))await page.evaluate(identity=>rejoinFixture('active',true,identity),mode);
    else if(mode==='error')await page.evaluate(()=>{fixture.failDM=true;});
    await back.click();await settle(page);
    const dialog=page.getByRole('dialog',{name:'Bring someone in',exact:true});
    if(mode.startsWith('click-')){
     assert.equal(await dialog.count(),0,'Stale render must recheck the latest exact agent before opening the invitation');
     await page.getByText(mode==='click-active'?'Already in this chat':'Rejoin pending',{exact:true}).waitFor();
    }else if(mode==='error'){
     assert.equal(await dialog.count(),0,'Failed fresh read cannot open the invitation');
     await page.getByText('Synthetic conversation unavailable',{exact:true}).waitFor();
    }else {
     await dialog.waitFor();
     if(mode==='ended')await dialog.getByRole('button',{name:'Bring Analyst in',exact:true}).waitFor();
    }
    assert(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length)>before,'Bring back refreshes the exact conversation before acting');
   }
   const shot=path.join(evidence,'comic-phone-rejoin-'+mode+'.png');await page.screenshot({path:shot});shots.push(shot);
   assert.deepEqual(await page.evaluate(()=>fixture.thread.agents.map(a=>[a.pid,a.shared])),mode.startsWith('navigate-')||mode==='ended'||mode==='error'?[['old-agent',['old-grant']]]:[['old-agent',['old-grant']],['new-agent',['new-grant']]],'Past/current participation and grants stay separate');
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/dm/agent/invite','/api/dm/guest/invite','/api/groups/invite','/api/dm/agent/ask','/api/dm/send'].includes(r.path)).length),0,'Snackbar never sends an invitation or executes work');
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_LONG_LINK_REGRESSION==='1'){
 const {page,context}=await openCase('long-links');
 try {
  const message=page.locator('[data-mid="long-links"]');await message.waitFor();
  const url=await page.evaluate(()=>fixture.url),body=await page.evaluate(()=>fixture.linkBody);
  const links=message.getByRole('link',{name:url,exact:true});assert.equal(await links.count(),2);
  for(const link of await links.all()){
   assert.equal(await link.innerText(),'example.test/…');assert.equal(await link.getAttribute('href'),url);assert.equal(await link.getAttribute('title'),url);
   assert.equal(await link.evaluate(e=>e.href),url,'Native open/copy-link uses exact target');
   if(width===390)assert(await link.evaluate(async e=>{e.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,pointerType:'touch',pointerId:9}));await new Promise(r=>setTimeout(r,500));const native=e.dispatchEvent(new PointerEvent('contextmenu',{bubbles:true,cancelable:true,pointerType:'touch',pointerId:9}));e.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerType:'touch',pointerId:9}));return native;}),'Long press keeps native full-target/copy-link menu');
  }
  assert((await message.innerText()).includes('Useful guide'),'Descriptive label unchanged');
  assert((await message.innerText()).includes(url),'Code retains full URL');
  assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='long-links').body),body,'Stored/copy/edit source unchanged');
  if(skin==='comic'){
   await page.evaluate(()=>{window.copiedText=null;Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copiedText=text;}}});});
   if(width===390){const actions=message.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('dialog').getByRole('button',{name:'Copy text',exact:true}).click();}
   else{await message.hover();await message.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Copy text',exact:true}).click();}
   assert.equal(await page.evaluate(()=>window.copiedText),body,'Copy message retains the entire original URL');
  }
  await settle(page);const shot=path.join(evidence,skin+'-long-links-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  assert(await message.evaluate(e=>e.scrollWidth<=e.clientWidth+1),'Message stays within viewport');
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_RESOLUTION_REGRESSION==='1'){
 assert.equal(skin,'comic');
 const {page,context}=await openCase('continuation-resolve');
 try{
  const request=page.locator('[data-mid="needs-answer-exact"]');await request.waitFor();
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide').length),0);
  await page.evaluate(()=>{fixture.resolveUnsupported=true;});
  await request.getByRole('button',{name:'Mark as handled',exact:true}).click();
  await request.getByRole('button',{name:'Check host again',exact:true}).waitFor();
  assert.match(await request.innerText(),/Update AgentNet on that computer/);
  assert.equal(await page.getByRole('dialog',{name:'Mark as handled on Other laptop?',exact:true}).count(),0,'Unsupported host is explained before confirmation');
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'&&!r.body.check).length),0,'Capability refusal queues no decision');
  await page.evaluate(()=>{fixture.resolveUnsupported=false;});
  await request.getByRole('button',{name:'Check host again',exact:true}).click();
  const dialog=page.getByRole('dialog',{name:'Mark as handled on Other laptop?',exact:true});await dialog.waitFor();
  assert.match(await dialog.innerText(),/does not run the task again/);
  await settle(page);{const shot=path.join(evidence,'comic-resolution-confirm-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);}
  const send=dialog.getByRole('button',{name:'Mark as handled',exact:true});await send.click();
  await page.waitForFunction(()=>fixture.continuationFailed);await send.waitFor();await send.click();
  await page.waitForFunction(()=>fixture.resolutionQueued);
  assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='needs-answer-exact').job_state),'needs_human','queueing does not close host work');
  const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'&&!r.body.check));
  assert.equal(calls.length,2);assert.deepEqual(calls[0],calls[1],'explicit retry retains exact target and send ID');
  assert.deepEqual(calls[0].body,{host:'aster/other-laptop',id:'needs-answer-exact',key:'exact-request-key',attempt:7,action:'resolve',expect:'needs_human',report:'',send_id:calls[0].body.send_id});
  await page.evaluate(()=>{const m=fixture.thread.messages.find(m=>m.id==='needs-answer-exact');m.job_state=m.state='resolved';m.actions=[];delete m.continuation;fixture.overview.needs_you=[];reloadFixture();});
  await page.waitForFunction(()=>!document.querySelector('#skin').shadowRoot.textContent.includes('Mark as handled'));
  assert(!await page.evaluate(()=>fixture.requests.some(r=>['/api/send','/api/dm/send','/api/dm/agent/ask'].includes(r.path))));
  const shot=path.join(evidence,'comic-resolution-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_HELD_FLOOD_REGRESSION==='1'){
 assert.equal(skin,'comic');
 const {page,context}=await openCase('held-flood');
 try{
  if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).click();
  const decisions=page.getByRole('region',{name:'Held back',exact:true});await decisions.waitFor();
  // Different messages from a changed key are never "one record re-sent".
  const decision=decisions.locator('li').filter({hasText:'3 held messages · Cora’s Windows'});
  assert.match(await decision.locator('p').first().innerText(),/^3 held messages · Cora’s Windows · last /);
  assert.doesNotMatch(await decision.innerText(),/re-sent|record|cop(y|ies)/i,'a trust decision is a neutral count of messages');
  await decision.locator('summary').filter({hasText:'Show messages (3)'}).click();await decision.locator('li').first().waitFor();
  assert.equal(await decision.locator('li').count(),3,'each held message stays listed');
  await decision.locator('summary').filter({hasText:'Show messages (3)'}).click();
  assert.equal(await decisions.getByText(/Your device Bezos re-sent/).count(),0,'passive sync records stay out of OKs');
  const nav=page.getByRole('navigation',{name:'Main',exact:true});
  await nav.getByRole('button',{name:width<700?'You':'Settings',exact:true}).click();
  await page.getByRole('button',{name:/^About/}).click();
  await page.getByRole('button',{name:'Technical details',exact:true}).click();
  const held=page.getByRole('region',{name:'Sync diagnostics',exact:true});await held.waitFor();
  await held.locator('summary').filter({hasText:'Retained sync records'}).click();
  assert.equal(await held.locator('li').count(),3,'326 passive copies render three device/cause groups; trust messages stay in OKs');
  // Own verified device: named, records not envelopes, and who acts.
  const own=held.locator('li').filter({hasText:'Your device Bezos re-sent 4 records 300 times'});
  assert.match(await own.locator('p').first().innerText(),/^Your device Bezos re-sent 4 records 300 times · last .+ · update AgentNet on Bezos$/);
  await held.locator('li').filter({hasText:'Your device Bezos re-sent 2 records 6 times'}).getByText(/· waiting for its invitation$/).waitFor();
  // Copies that never opened carry no record: their sizes are only sizes.
  const claimed=held.locator('li').filter({hasText:'A device claiming to be claimed/remote · 20 held copies, 2 envelope sizes'});
  assert.equal(await claimed.count(),1,'unverified copies are counted, not called records');
  assert.doesNotMatch(await claimed.locator('p').first().innerText(),/update AgentNet/,'an unverified claim is asked nothing');
  assert(!/unverified sender/.test(await held.innerText()),'own verified device not called unverified');
  await settle(page);{const shot=path.join(evidence,'comic-held-flood-group-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);}
  // Every copy stays inspectable, a bounded page at a time.
  await own.locator('summary').filter({hasText:'Show copies (300)'}).click();await own.locator('li').first().waitFor();assert.equal(await own.locator('li').count(),20,'expanded copies are bounded');
  assert.match(await own.locator('li').first().innerText(),/record 33333333/);
  await own.getByRole('button',{name:'Show more copies (280)',exact:true}).waitFor();
  // Each shown copy is one plain line whose only control dismisses that exact
  // notice (e2bebdb2: every hold class archives individually or as a group);
  // none becomes a decision card.
  const copies=own.locator('li');
  for(let i=0;i<20;i++)assert.deepEqual(await copies.nth(i).getByRole('button').allInnerTexts(),['Archive notice'],'background failures do not become individual decision cards');
  assert.equal(await held.getByRole('button',{name:/Trust|Accept|Approve|Run/}).count(),0,'no decision is offered for background copies');
  assert.equal(await own.locator('[data-size]').count(),0,'background copies are not drawn as a person');
  await own.locator('summary').filter({hasText:'Show copies (300)'}).click();
  await held.getByRole('button',{name:'Archive all 326 notices',exact:true}).waitFor(); // the whole count, not a silent 256
  const archive=own.getByRole('button',{name:'Archive these 300 notices',exact:true});await archive.click();
  await page.waitForFunction(()=>fixture.archiveFailed);
  assert.equal(await page.evaluate(()=>fixture.overview.quarantine.length),329,'a failed batch hides nothing');
  await archive.click();
  await page.waitForFunction(()=>fixture.batches.length===2&&fixture.overview.quarantine.length===31);
  assert.deepEqual(await page.evaluate(()=>fixture.batches.map(b=>b.length)),[256,44],'the group action covers the whole group in bounded exact snapshots');
  await page.locator('#skin').evaluate(e=>new Promise((resolve,reject)=>{const t=Date.now(),tick=()=>/Archived 299 notices on this device/.test(e.shadowRoot.textContent)?resolve():Date.now()-t>4000?reject(Error('no archive count toast')):setTimeout(tick,50);tick();}));
  assert.equal(await page.evaluate(()=>fixture.retainedHeld.length),329,'archive retains original ciphertext metadata');
  assert(await page.evaluate(()=>fixture.overview.quarantine.some(q=>q.detail_code==='admission_failed')&&fixture.overview.quarantine.some(q=>q.id==='f'.repeat(32))),'new and changed records survive stale snapshot');
  assert(!await page.evaluate(()=>fixture.requests.some(r=>r.body?.do&&r.body.do!=='archive_held_batch')),'bulk archive never admits/trusts/runs');
  const shot=path.join(evidence,'comic-held-flood-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_COMPOSER_FOLLOWUP_REGRESSION==='1'){
 // The next tagged message naturally goes to exact current work, without a
 // selector, steering mode, action, or second composer.
 {
  const {page,context}=await openCase('composer-followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   const choose=async text=>{await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(text);};
   await choose(' Use English instead');
   assert.notEqual(await form.locator('[data-part="send"]').getAttribute('aria-disabled'),'true','queued siblings never block the next tagged message');
   assert.equal(await form.getByRole('combobox').count(),0,'no request selector');assert.equal(await form.getByRole('button',{name:'Cancel steering',exact:true}).count(),0);
   await form.getByRole('button',{name:'Add to message',exact:true}).click();assert.equal(await page.getByRole('menuitem',{name:/Steer current work/}).count(),0,'no extra steering action');await form.getByRole('button',{name:'Add to message',exact:true}).click();await page.getByRole('menu').waitFor({state:'hidden'});await settle(page);
   await form.locator('input[type=file]').setInputFiles({name:'canonical.md',mimeType:'text/markdown',buffer:Buffer.from('canonical original bytes')});
   await form.locator('[data-part="send"]').click();await page.getByText(/Synthetic uncertain response/).first().waitFor();
   const retry=page.getByRole('button',{name:/Retry/}).first();await retry.waitFor();
   await choose(' A newer draft stays here');const newer=await field.inputValue();
   await retry.click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===2);
   const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup'));
   assert.deepEqual({...calls[0].body,files:[]},{...calls[1].body,files:[]},'retry captures original ID/ref/attempt/body');
   assert.equal(calls[0].body.attempt,1);assert.deepEqual(calls[0].body.ref,{id:'1'.repeat(32),fingerprint:'eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee'});
   const staged=await page.evaluate(()=>fixture.followupStages);for(const call of calls){const file=staged.find(f=>f.id===call.body.files[0]);assert.equal(file.text,'canonical original bytes');assert.equal(file.name,'canonical.md');}
   assert.equal(await field.inputValue(),newer);await page.getByText(/Steering sent. Waiting for native acceptance/).first().waitFor();
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length),0,'tagged current work did not create another run');
   await page.evaluate(()=>{fixture.thread.messages.find(m=>m.id==='1'.repeat(32)).exec.state='done';reloadFixture();});
   await form.locator('[data-part="send"]').click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===1);
   const fresh=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/agent/ask').body);
   assert.equal(fresh.pid,'followup-agent');assert.match(fresh.body,/A newer draft stays here$/);assert(!fresh.ref&&!fresh.attempt,'no running work keeps ordinary new-request wire');
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth));
   const shot=path.join(evidence,'comic-natural-steering-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 {
  const {page,context}=await openCase('composer-followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await page.evaluate(()=>{fixture.thread.messages.find(m=>m.id==='2'.repeat(32)).exec.state='running';fixture.followupFailed=true;reloadFixture();});
   await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' Bound by the existing Reply action');const draft=await field.inputValue();
   await form.locator('[data-part="send"]').click();await form.getByText(/2 distinct running requests/).waitFor();
   assert.equal(await field.inputValue(),draft);assert.equal(await form.getByRole('combobox').count(),0,'genuine ambiguity adds no chooser');
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/request/followup','/api/dm/agent/ask'].includes(r.path)).length),0,'ambiguous work never guesses or starts another run');
   const request=page.locator('[data-mid="'+('1'.repeat(32))+'"]');await request.waitFor();
   if(width===390){const actions=request.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('dialog').getByRole('button',{name:'Reply',exact:true}).click();}
   else{await request.hover();await request.getByRole('button',{name:'Reply',exact:true}).click();}
   assert.match(await field.inputValue(),/@.*Analyst/,'exact reply keeps the existing agent tag');
   await form.locator('[data-part="send"]').click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===1);
   const call=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/request/followup').body);assert.equal(call.ref.id,'1'.repeat(32));assert.equal(call.attempt,1);
  }finally{await context.close();}
 }
 {
  const {page,context}=await openCase('composer-followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' Captured before completion');
   await form.locator('input[type=file]').setInputFiles({name:'race.md',mimeType:'text/markdown',buffer:Buffer.from('retained race bytes')});
   await page.evaluate(()=>fixture.followupStagePause=true);await form.locator('[data-part="send"]').click();await page.waitForFunction(()=>!!window.finishFollowupStage);
   await field.fill('Newer draft survives completion race');
   await page.evaluate(()=>{fixture.rejectEndedSteering=true;fixture.thread.messages.find(m=>m.id==='1'.repeat(32)).exec.state='done';reloadFixture();finishFollowupStage();});
   await page.getByText(/current work ended; steering was not applied/).first().waitFor();
   await page.getByRole('button',{name:/Retry/}).first().click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===2);
   const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').map(r=>r.body));assert.deepEqual({...calls[0],files:[]},{...calls[1],files:[]});assert.equal(calls[0].attempt,1);
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length),0,'completion race never starts new execution');assert.equal(await field.inputValue(),'Newer draft survives completion race');
  }finally{await context.close();}
 }
 {
  const {page,context}=await openCase('composer-followup-android');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' Android natural current-run steering');
   await form.locator('input[type=file]').setInputFiles({name:'android.md',mimeType:'text/markdown',buffer:Buffer.from('Android retained stage bytes')});
   await form.locator('[data-part="send"]').click();await page.getByRole('button',{name:/Saved messages.*Review/}).first().waitFor();
   const journal=await page.evaluate(()=>JSON.parse(localStorage.getItem('agentnet.android.send-journal.v1.default')));assert.equal(journal.length,1);assert.equal(journal[0].endpoint,'/api/request/followup');assert.equal(journal[0].request.attempt,1);
   assert.deepEqual(journal[0].request.ref,{id:'1'.repeat(32),fingerprint:'eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee'});
   await field.fill('New Android draft remains');await page.getByRole('button',{name:/Saved messages.*Review/}).first().click();
   const dialog=page.getByRole('dialog',{name:'Saved messages',exact:true});await dialog.waitFor();await dialog.getByRole('button',{name:'Retry saved send',exact:true}).click();
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===2);
   const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup'));assert.deepEqual(calls[0].body,calls[1].body,'Android journal retains exact ID/ref/attempt/stageIDs');assert.equal(await page.evaluate(()=>fixture.followupStages.length),1);
   await page.waitForFunction(()=>JSON.parse(localStorage.getItem('agentnet.android.send-journal.v1.default')).length===0);await dialog.waitFor({state:'hidden'});assert.equal(await field.inputValue(),'New Android draft remains');
  }finally{await context.close();}
 }
 // Each selected agent captures its own current run. The same next send
 // can steer one current run and make ordinary new work for another agent.
 {
  const {page,context}=await openCase('composer-followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await page.evaluate(()=>{const source=fixture.thread.messages.find(m=>m.id==='1'.repeat(32));fixture.thread.messages.push({...source,id:'b'.repeat(32),lid:'b'.repeat(32),pid:'same-name-other-agent',body:'Second exact agent work',target:{...source.target,agent_id:'b'.repeat(32)},exec:{...source.exec,attempt:2}});fixture.followupFailed=true;reloadFixture();});
   const both=async suffix=>{await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' @');await page.getByRole('option').filter({hasText:'Analyst'}).nth(1).click();await field.pressSequentially(suffix);};
   await both(' Both currently running targets');await form.locator('[data-part="send"]').click();
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===2);
   const calls=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').map(r=>r.body));assert.deepEqual(calls.map(c=>[c.ref.id,c.attempt]).sort(),[['1'.repeat(32),1],['b'.repeat(32),2]]);assert.notEqual(calls[0].id,calls[1].id);assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length),0);
   await page.evaluate(()=>{fixture.thread.messages.find(m=>m.id==='b'.repeat(32)).exec.state='done';reloadFixture();});
   await both(' Mixed current and new work');await form.locator('[data-part="send"]').click();
   await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===3&&fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===1);
   const fresh=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/agent/ask').body);assert.equal(fresh.pid,'same-name-other-agent');assert(!fresh.attempt&&!fresh.ref);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_FOLLOWUP_REGRESSION==='1'){
 // Preserve the former sheet's meaningful safety checks through the one composer.
 {
  const {page,context}=await openCase('followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   const choose=async text=>{await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(text);};
   const calls=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup'));
   const request=page.locator('[data-mid="'+('1'.repeat(32))+'"]');await request.waitFor();
   if(width===390){const actions=request.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');}
   else{await request.hover();await request.getByRole('button',{name:'More actions',exact:true}).click();}
   assert.equal(await page.getByRole(width===390?'button':'menuitem',{name:'Follow up with this agent…',exact:true}).count(),0,'redundant message action is removed');
   await page.keyboard.press('Escape');assert.equal(await page.getByRole('dialog',{name:'Follow up with the same agent',exact:true}).count(),0);
   await choose(' A staged correction');assert.deepEqual(await calls(),[],'typing the next tag submits nothing');
   await form.locator('input[type=file]').setInputFiles([{name:'terms.md',mimeType:'text/markdown',buffer:Buffer.from('English terminology')},{name:'fail-second.md',mimeType:'text/markdown',buffer:Buffer.from('later file')}]);
   await page.evaluate(()=>{fixture.followupStageFailure=true;fixture.followupFailed=true;});
   await form.locator('[data-part="send"]').click();await page.getByText(/Synthetic second stage failure/).first().waitFor();
   assert.deepEqual(await calls(),[],'second stage failure sends no steering');assert.match(await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),/terms.md/,'original files stay with the exact retry');
   const firstStage=await page.evaluate(()=>fixture.followupStages[0].id);
   await field.fill('A newer draft during the staged-file retry');
   await page.getByRole('button',{name:/Retry/}).first().click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===1);
   const stagedCall=(await calls())[0];assert.equal(stagedCall.body.files[0],firstStage,'prepared first file is retained across the second-file failure');
   assert.equal(await page.evaluate(()=>fixture.followupStages.length),2);assert.equal(await field.inputValue(),'A newer draft during the staged-file retry');
   await choose(' Use English instead');
   await form.locator('input[type=file]').setInputFiles({name:'terms.md',mimeType:'text/markdown',buffer:Buffer.from('English terminology')});
   await page.evaluate(()=>fixture.followupFailed=false);
   await form.locator('[data-part="send"]').click();await page.getByText(/Synthetic uncertain response/).first().waitFor();
   await field.fill('Newer draft after uncertain handover');
   await page.getByRole('button',{name:/Retry/}).first().click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length===3);
   const sent=(await calls()).slice(1);assert.deepEqual({...sent[0].body,files:[]},{...sent[1].body,files:[]},'uncertain retry retains exact ID/ref/attempt/body');
   const stages=await page.evaluate(()=>fixture.followupStages);
   for(const call of sent){assert.equal(call.body.attempt,1);const file=stages.find(s=>s.id===call.body.files[0]);assert.deepEqual({name:file.name,size:file.size,text:file.text},{name:'terms.md',size:19,text:'English terminology'});}
   assert.deepEqual(sent[0].body.ref,{id:'1'.repeat(32),fingerprint:'eeeeeeee-eeeeeeee-eeeeeeee-eeeeeeee'});
   assert.equal(await field.inputValue(),'Newer draft after uncertain handover');assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id===fixture.followupID).body),'Prepare contribution-margin.md in Russian','original request remains exact');
  }finally{await context.close();}
 }
 {
  const {page,context}=await openCase('followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' A correction abandoned before handover');
   await form.locator('input[type=file]').setInputFiles({name:'terms.md',mimeType:'text/markdown',buffer:Buffer.from('English terminology')});
   await page.evaluate(()=>fixture.followupStagePause=true);await form.locator('[data-part="send"]').click();await page.waitForFunction(()=>!!window.finishFollowupStage);
   await field.fill('Navigation-side newer draft');await page.evaluate(()=>openGroup());await page.evaluate(()=>finishFollowupStage());
   await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/upload/discard'&&r.body.ids.includes(fixture.followupStages[0].id)));
   await page.getByText(/chat changed before steering was submitted/).first().waitFor();
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length),0,'view changed during staging sends no correction');
   assert.equal(await field.inputValue(),'Navigation-side newer draft','navigation never overwrites the newer draft');
   const shot=path.join(evidence,'comic-steering-navigation-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 {
  const {page,context}=await openCase('followup');
  try{
   const form=page.locator('form[aria-label="Write a message"]'),field=form.locator('textarea');
   await field.fill('@');await page.getByRole('option').filter({hasText:'Analyst'}).first().click();await field.pressSequentially(' Later clarification');
   const before=await field.inputValue();
   await page.evaluate(()=>{fixture.thread.messages.find(m=>m.id===fixture.followupID).deleted=true;reloadFixture();});
   await form.locator('[data-part="send"]').click();await form.getByText(/current attempt or identity cannot be verified/).waitFor();
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/request/followup').length),0,'deleted exact request refuses before handover');assert.equal(await field.inputValue(),before);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_CONTINUATION_REGRESSION==='1'){
 for(const mode of ['local','remote','stale']){
  const {page,context}=await openCase('continuation-'+mode);
  try{
   const request=page.locator('[data-mid="needs-answer-exact"]');await request.waitFor();
   const continuationReply=request.getByRole('button',{name:'Reply',exact:true});
   const note=page.locator('[data-mid="ordinary-quote"]');await note.waitFor();
   if(width===390){const actions=note.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('dialog').getByRole('button',{name:'Reply',exact:true}).click();}else{await note.hover();await note.getByRole('button',{name:'Reply',exact:true}).click();}
   assert.equal(await page.getByRole('dialog',{name:'Answer your agent',exact:true}).count(),0,'Ordinary Reply only quotes a message');
   assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>(r.path==='/api/operator/decide'||r.path==='/api/act'&&r.body?.do!=='read')).length),0,'Ordinary initial quote never submits a continuation');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask','/api/send'].includes(r.path))),'Clarification and quote never create another request/manual answer');
   if(mode==='stale'){
    assert.equal(await continuationReply.count(),0,'Unverified/stale host has no continuation Reply');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/operator/decide'||r.path==='/api/act'&&r.body?.do!=='read').length),0,'Stale request does not act on load');
   }else{
    await continuationReply.click();
    const dialog=page.getByRole('dialog',{name:'Answer your agent',exact:true});await dialog.waitFor();
    assert((await dialog.innerText()).includes('EXACT_QUESTION'),'Agent clarification shown in full');
    assert.match(await dialog.innerText(),/permission or environment problem/);assert.match(await dialog.innerText(),/Reply and retry keep the same permissions/);
    const send=dialog.getByRole('button',{name:'Send answer',exact:true});assert(await send.isDisabled(),'Empty answer cannot be submitted');
    await dialog.locator('#agent-answer').fill('Use the east warehouse');await send.click();
    await page.waitForFunction(()=>fixture.continuationFailed);await send.waitFor();
    await settle(page);await dialog.evaluate(async e=>{await Promise.all(e.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
    const sheetStyle=await dialog.evaluate(e=>{const c=getComputedStyle(e);return {background:c.backgroundColor,opacity:c.opacity,transform:c.transform,filter:c.filter,backdropFilter:c.backdropFilter,classes:e.className};});
    const styleFile=path.join(evidence,skin+'-continuation-answer-'+mode+'-'+width+'.json');fs.writeFileSync(styleFile,JSON.stringify(sheetStyle,null,2),{mode:0o600});shots.push(styleFile);
    const answerShot=path.join(evidence,skin+'-continuation-answer-'+mode+'-'+width+'.png');await page.screenshot({path:answerShot});shots.push(answerShot);
    await send.click();await page.waitForFunction(()=>fixture.thread.messages.find(m=>m.id==='needs-answer-exact').job_state==='running');
    await page.evaluate(()=>reloadFixture());await settle(page);
    const calls=await page.evaluate(()=>fixture.requests.filter(r=>['/api/act','/api/operator/decide'].includes(r.path)));
    assert.equal(calls.length,2,'One explicit retry after synthetic transport failure');assert.deepEqual(calls[0],calls[1],'Retry preserves exact request, attempt and send identity');
    const expected={id:'needs-answer-exact',key:'exact-request-key',attempt:7,send_id:calls[0].body.send_id};assert.match(expected.send_id,/^[0-9a-f]{32}$/);
    if(mode==='local')assert.deepEqual(calls[0],{path:'/api/act',body:{do:'continue',...expected,body:'Use the east warehouse'}});
    else assert.deepEqual(calls[0],{path:'/api/operator/decide',body:{host:'aster/other-laptop',...expected,action:'continue',expect:'needs_human',text:'Use the east warehouse',report:''}});
    assert.equal(await continuationReply.count(),0,'Consumed continuation descriptor removed on refresh');
   }
   await settle(page);const shot=path.join(evidence,skin+'-continuation-'+mode+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_DECLINE_INVITATION_REGRESSION==='1'){
 for(const status of ['pending','stale']){
  const {page,context}=await openCase('decline-invite-'+status);
  try{
   if(skin==='comic'){
    await page.getByText('Orchard inbound invitation',{exact:true}).waitFor();
    if(status==='stale')assert.equal(await page.getByRole('button',{name:'Join',exact:true}).count(),0,'Stale invitation cannot Join');
    const before=path.join(evidence,skin+'-decline-'+status+'-before-'+width+'.png');await page.screenshot({path:before});shots.push(before);
    await page.getByRole('button',{name:'No thanks',exact:true}).click();
   }
   await page.waitForFunction(()=>fixture.overview.group_invitations.length===0);await page.evaluate(()=>reloadFixture());await settle(page);
   assert.equal(await page.getByText('Orchard inbound invitation',{exact:true}).count(),0,'Declined card stays absent after refresh');
   assert.equal(await page.getByRole('button',{name:/Invitation: Orchard inbound invitation/}).count(),0,'Legacy invitation row stays absent after refresh');
   assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/decide').map(r=>r.body)),[{id:'inbound-exact',accept:false}]);
   const shot=path.join(evidence,skin+'-decline-'+status+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  }finally{await context.close();}
 }
 continue;
}
if(process.env.AGENTNET_LEGACY_LIST_REGRESSION==='1'){
 const {page,context}=await openCase('team-legacy');
 try{
  if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
  await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width===1280?'Settings':'You',exact:true}).click();
  await page.getByRole('button',{name:'People lists',exact:true}).click();
  const reads=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/agents')).length);
  await page.getByRole('button',{name:/Retained legacy/}).click();
  const dialog=page.getByRole('dialog');await dialog.getByText(/Adding a person keeps this list/).waitFor();
  assert.equal(await dialog.getByRole('combobox',{name:'Agent',exact:true}).count(),0,'legacy list never offers agent targets');
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/agents')).length),reads,'opening the legacy editor loads no agent catalog');
  await dialog.getByRole('combobox',{name:'Person',exact:true}).selectOption(brin.person);
  await dialog.getByRole('button',{name:'Add person',exact:true}).click();
  await dialog.getByRole('list',{name:'Members',exact:true}).getByText('Brin',{exact:true}).waitFor();
  const list=await page.evaluate(()=>fixture.teams[0]);assert.equal(list.id,'e'.repeat(32));assert.equal(list.version,3);assert.deepEqual(list.managers,[me.person]);assert.deepEqual(list.members,[me.person,brin.person]);
  assert.equal(await dialog.getByRole('button',{name:'Leave list',exact:true}).count(),1);
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/dm/agent/ask','/api/dm/agent/invite','/api/groups/invite','/api/dm/send'].includes(r.path)).length),0);
  await settle(page);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false);
  const shot=path.join(evidence,'comic-legacy-add-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  await page.evaluate(()=>{fixture.teams[0].manager=false;reloadFixture();});await settle(page);
  assert.equal(await dialog.getByRole('button',{name:'Add person',exact:true}).count(),0);await dialog.getByText('Only a manager can add people to this list.',{exact:true}).waitFor();
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_AGENT_CHILD_REGRESSION==='1'){
 const {page,context}=await openCase('agent-child');
 try{
  const row=page.locator('[data-mid="agent-child"]'),card=row.locator('[data-terminal-request]');
  await card.waitFor();await card.locator('summary').click();
  await card.getByText(/originating local run stopped/).waitFor();
  assert.equal(await card.getByRole('button',{name:'Run it again',exact:true}).count(),0);
  await card.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
  assert.doesNotMatch(await card.innerText(),/You asked|Running it again starts fresh/);assert.match(await card.innerText(),/Your Codex asks your Claude/);
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do!=='read').length),0,'expanding never replays or resolves');
  await settle(page);
  const shot=path.join(evidence,'comic-agent-child-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_TEAM_EDIT_REGRESSION==='1'){
 const {page,context}=await openCase('team-tags');
 try{
  if(skin==='comic'){
   if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
   await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width===1280?'Settings':'You',exact:true}).click();
   await page.getByRole('button',{name:'People lists',exact:true}).click();
   await page.getByLabel('New list name',{exact:true}).fill('Warehouse');
   await page.getByRole('button',{name:'Create list',exact:true}).click();
   await page.getByRole('dialog').getByText(/Everyone can use @Warehouse/).waitFor();
   await page.getByRole('combobox',{name:/^Person/}).selectOption(brin.person);
   await page.getByRole('button',{name:'Add person',exact:true}).click();
   await page.getByRole('list',{name:'Members',exact:true}).getByText('Brin',{exact:true}).waitFor();
   await page.getByRole('combobox',{name:/^Agent’s device/}).selectOption(brin.address);
   await page.getByRole('combobox',{name:'Agent',exact:true}).selectOption('1'.repeat(32));
   await page.getByRole('button',{name:'Add agent',exact:true}).click();
  }
  await page.waitForFunction(()=>fixture.teams[0]?.agents.length===1);
  const t=await page.evaluate(()=>fixture.teams[0]);assert.equal(t.version,2);assert.deepEqual(t.members,[brin.person]);assert.deepEqual(t.agents,[{id:'1'.repeat(32),host:brin.address,host_key:brin.fingerprint}]);
  assert.equal(await page.getByRole('button',{name:/^Join( list)?$/}).count(),0,'Explicit tag targets do not expose self-join');
  await page.getByRole('button',{name:'Remove agent',exact:true}).waitFor();await settle(page);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'Tag editor fits viewport');
  const shot=path.join(evidence,skin+'-team-edit-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
  await page.getByRole('button',{name:'Remove agent',exact:true}).click();await page.waitForFunction(()=>fixture.teams[0].agents.length===0);
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/dm/agent/ask','/api/dm/agent/invite','/api/groups/invite','/api/dm/send'].includes(r.path)).length),0,'Editing a tag never invites or dispatches');
 }catch(e){const shot=path.join(evidence,skin+'-team-edit-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,shot,errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent)}));throw e;}
 finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_TERMINAL_CARD==='1'){
 const {page,context}=await openCase('terminal-card');
 try{
  const row=page.locator('[data-mid="stopped-exact"]'),card=row.locator('[data-terminal-request]');
  await card.waitFor();console.log("terminal card loaded",width);await settle(page);console.log("terminal settled",width);
  assert.equal(await card.getAttribute('open'),null,'Historical stopped request starts collapsed');
  assert((await row.boundingBox()).height<120,'Whole stopped request stays compact');
  assert.match(await card.locator('summary').innerText(),/Stopped/);
  assert.match(await card.locator('summary').innerText(),/Translate contribution margin/);
  const retry=card.getByRole('button',{name:'Run it again',exact:true});assert.equal(await retry.isVisible(),false);
  const actions=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do!=='read'));
  await card.locator('summary').focus();await page.keyboard.press('Enter');
  await retry.waitFor();console.log('terminal expanded',width);assert.match(await card.innerText(),/Cancelled by you/);assert.deepEqual(await actions(),[],'Expanding never retries');
  await card.locator('summary').focus();await page.keyboard.press('Enter');assert.equal(await retry.isVisible(),false);
  await page.evaluate(()=>{fixture.thread.messages.find(m=>m.id==='stopped-exact').actions=[];reloadFixture();});console.log('terminal refreshing',width);await settle(page);console.log('terminal refreshed',width);assert.equal(await card.getAttribute('open'),null);assert((await row.boundingBox()).height<120,'History without actions stays compact');
  const shot=path.join(evidence,'comic-terminal-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_GUEST_OKS==='1'){
 const {page,context}=await openCase('guest-oks');
 try{
  await page.getByRole('button',{name:/^Review/}).click();
  await page.getByRole('button',{name:'Join conversation',exact:true}).waitFor();
  const decline=page.getByRole('button',{name:'No thanks',exact:true});await decline.click();
  await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/guest/decide'));
  assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/guest/decide').map(r=>r.body)),[{pid:'human-invite',accept:false}]);
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/decide'||r.path==='/api/dm/agent/ask').length),0);
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_RENDER_PERF==='1'){
 const {page,context}=await openCase('render-perf');
 try{
  await settle(page);
  const input=page.getByRole('form',{name:'Write a message'}).locator('textarea');
  await page.evaluate(()=>{fixture.requests=[];fixture.profile.formats=0;fixture.profile.inputs=[];fixture.profile.measuring=true;});
  for(const text of ['Draft','Draft grows','Draft stays unsent']){await input.fill(text);await settle(page);}
  const typed=await page.evaluate(()=>({...fixture.profile,rows:document.querySelector('#skin').shadowRoot.querySelectorAll('[data-mid]').length,sent:fixture.requests.some(r=>['/api/dm/send','/api/dm/agent/ask','/api/act'].includes(r.path))}));
  assert.equal(typed.rows,1200);assert.equal(typed.sent,false);
  if(process.env.AGENTNET_PERF_BASELINE!=='1')assert(typed.formats<8,'Typing must not reformat retained conversation: '+typed.formats);
  assert.equal(await input.inputValue(),'Draft stays unsent');
  const arrivalStart=await page.evaluate(()=>performance.now());
  await page.evaluate(()=>{fixture.profile.formats=0;fixture.thread.messages.push({id:'perf-new',lid:'perf-new',from:'brin/desktop',dir:'in',kind:'message',body:'Newest arrival remains responsive',at:'2026-10-05T11:00:00Z',unread:false,actions:[]});reloadFixture();});
  await page.locator('[data-mid="perf-new"]').waitFor();await settle(page);
  assert.equal(await input.inputValue(),'Draft stays unsent');
  console.log(JSON.stringify({kind:'comic-render-profile',width,typed,arrivalMs:await page.evaluate(start=>performance.now()-start,arrivalStart),arrivalFormats:await page.evaluate(()=>fixture.profile.formats)}));
 }finally{await context.close();}
 continue;
}
if(process.env.AGENTNET_MULTI_AGENT_REGRESSION==='1'){
 const {page,context}=await openCase('multi-agent');
 try{
  const field=page.locator('form[aria-label="Write a message"] textarea');
  await field.waitFor({state:'visible'});
  const checkComicMirror=async (labels,selected=false)=>{
   if(skin!=='comic')return;
   await settle(page);
   const mirror=await field.evaluate(el=>({text:el.previousElementSibling.textContent,labels:[...el.previousElementSibling.querySelectorAll('span')].map(s=>s.textContent),color:getComputedStyle(el).color}));
   assert.equal(mirror.text,(await field.inputValue())+'\u200b','mirror contains exactly one copy of the editable text');
   assert.deepEqual(mirror.labels,labels,'collective chips remain compact, without recipient metadata or names');
   assert.equal(mirror.color,'rgba(0, 0, 0, 0)','textarea glyphs stay transparent');
   if(!selected)return;
   await field.press('Control+A');
   const selection=await field.evaluate(el=>({color:getComputedStyle(el,'::selection').color,background:getComputedStyle(el,'::selection').backgroundColor,from:el.selectionStart,to:el.selectionEnd}));
   assert.equal(selection.color,'rgba(0, 0, 0, 0)','selection must not paint a second, independently wrapped copy behind the mirror');
   const alpha=/[\/,]\s*([\d.]+)\)$/.exec(selection.background)?.[1];
   assert(Number(alpha)>0&&Number(alpha)<1,'selection highlight leaves the mirror text readable');
   assert.equal(selection.from,0);assert.equal(selection.to,(await field.inputValue()).length);
   const shot=path.join(evidence,'comic-collective-selection-'+labels.length+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
   await field.press('Control+End');
  };
  let firstAgentToken='';
  const pick=async index=>{
   await field.press('Control+End');
   if((await field.inputValue())&&!/\s$/.test(await field.inputValue()))await field.pressSequentially(' ');
   const before=await field.inputValue();
   await field.pressSequentially('@');
   // Two same-label agents on one host, distinct participation IDs, fixed DTO order.
   const options=page.getByRole('option').filter({hasText:'Analyst'});
   assert.equal(await options.count(),2,'both same-label agents must be offered');
   await options.nth(index).click();
   return (await field.inputValue()).slice(before.length);
  };
  if(process.env.AGENTNET_COLLECTIVE_TAGS==='1'){
   // Removing the only collective must clear the legacy single-agent fallback.
   await field.pressSequentially('@everyone');
   await checkComicMirror([]);
   const firstTag=page.getByRole('option').filter({hasText:'everyone'});
   await firstTag.click();assert.equal((await field.inputValue()).trimEnd(),'@everyone');
   await checkComicMirror(['@everyone'],true);
   if(skin==='comic'){
    await page.getByRole('button',{name:'Don’t ask 2 agents',exact:true}).click();
    assert.equal(await page.getByRole('button',{name:/^Don’t ask /}).count(),0,'stopping a collective removes every exact agent target');
    assert.equal(await field.inputValue(),'@everyone ','human attention tag remains compact when stopping agents');
   }
   await field.fill('');
   if(skin==='comic')assert.equal(await page.getByRole('button',{name:/^Don’t ask /}).count(),0,'deleted collective leaves no fallback agent');
   for(const name of ['everyone','Reviewers']){
    await field.press('Control+End');
    if((await field.inputValue())&&!/\s$/.test(await field.inputValue()))await field.pressSequentially(' ');
    await field.pressSequentially('@'+name);
    const option=page.getByRole('option').filter({hasText:name});
    await option.waitFor({state:'visible'});if(name==='everyone'){const shot=path.join(evidence,skin+'-tag-picker-'+width+'.png');await settle(page);await page.screenshot({path:shot});shots.push(shot);}await option.click();
   }
   assert.equal((await field.inputValue()).trimEnd(),'@everyone @Reviewers','collectives remain two compact tags');
   assert(!/@Brin|@Analyst|Participant/.test(await field.inputValue()),'the 1000-member composer never expands tags into participant names');
   assert.equal(await page.evaluate(()=>fixture.thread.members.length),1000,'actual rendered group has 1000 human members');
   // An edited shared list, then a complete skin reload, must not rewrite
   // either the tag label or its already selected exact recipient snapshot.
   await page.evaluate(()=>{fixture.teamSnapshotEdited=true;localStorage.setItem('fixture-team-snapshot-edited','1');reloadFixture();});
   await settle(page);assert.equal((await field.inputValue()).trimEnd(),'@everyone @Reviewers');
   await page.reload();await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openGroup());
   await page.getByText('Selected warehouse context',{exact:false}).first().waitFor();
   await field.waitFor({state:'visible'});
   assert.equal((await field.inputValue()).trimEnd(),'@everyone @Reviewers','full reload restores both compact frozen tags after list edits');
   await checkComicMirror(['@everyone','@Reviewers'],true);
  }else{firstAgentToken=await pick(0);await pick(1);}

  // Edit the first mention away, preserving the second, then select it again.
  if(process.env.AGENTNET_COLLECTIVE_TAGS!=='1'){
   const remove=firstAgentToken,firstAgent=(await field.inputValue()).indexOf(remove);
   assert(remove.startsWith('@')&&firstAgent>=0,'the exact first selected mention must remain before editing it');
   await field.evaluate((el,range)=>{el.focus();el.setSelectionRange(range.start,range.end);},{start:firstAgent,end:firstAgent+remove.length});await field.press('Delete');
  }
  await pick(0);await field.pressSequentially('Compare warehouse inventory');
  const file=page.locator('form[aria-label="Write a message"] input[type=file]');
  await file.setInputFiles({name:'inventory.txt',mimeType:'text/plain',buffer:Buffer.from('synthetic inventory')});
  if(skin==='comic')await page.locator('form[aria-label="Write a message"] button[type=submit]').click();
  await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===2);
  const asks=()=>page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').map(r=>r.body));
  const first=await asks();assert.deepEqual(first.map(a=>a.pid).sort(),['agent-a','agent-b']);
  assert.notEqual(first[0].id,first[1].id,'one id per exact participation');
  assert.match(first[0].send_group,/^[a-f0-9]{32}$/);assert.equal(first[0].send_group,first[1].send_group,'one durable composer group');
  await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.innerHTML.includes('Retry'));
  const visibleCopies=()=>page.locator('#skin').evaluate(e=>(e.shadowRoot.innerText||e.shadowRoot.textContent).split('Compare warehouse inventory').length-1);
  assert.equal(await visibleCopies(),1,'one human body with two independently retryable targets');
  assert(first.every(a=>a.kind==='question'&&a.body.includes('Compare warehouse inventory')));
  if(process.env.AGENTNET_COLLECTIVE_TAGS==='1'){
   assert(first.every(a=>a.body.includes('[@Reviewers](agentnet:collective/v1/')),'signed requests retain compact frozen team snapshot');
   assert(first.every(a=>!a.body.includes('[@Brin]')),'no hidden expanded human names');
   for(const a of first){
    const everyone=/\[@everyone\]\(agentnet:collective\/v1\/([^)]*)\)/.exec(a.body);
    assert(everyone,'sent request retains compact everyone snapshot');
    assert.equal(everyone[1].split(',').length,1001,'everyone captures 999 other humans and two existing agents');
    const reviewers=/\[@Reviewers\]\(agentnet:collective\/v1\/([^)]*)\)/.exec(a.body);
    assert.equal(reviewers[1].split(',').length,3,'renaming and emptying the shared team cannot rewrite its captured three references');
    assert(reviewers[1].includes('a.agent-a')&&reviewers[1].includes('a.agent-b'),'team edits preserve both exact agent participation IDs');
   }
  }
  assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send').length),0,'no ordinary-message fallback');
  assert.equal(first[0].files.length,1);assert.equal(first[1].files.length,1);
  assert.notDeepEqual(first[0].files[0],first[1].files[0],'each request owns separately staged upload');
  assert.equal(await page.evaluate(()=>fixture.stages.length),2);
  await page.getByRole('button',{name:'Retry',exact:true}).click();
  await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').length===3);
  const after=await asks(),failed=first.find(a=>a.pid==='agent-b');
  assert.equal(after[2].pid,'agent-b');assert.equal(after[2].id,failed.id);
  assert.equal(after[2].send_group,failed.send_group,'retry retains the same durable group');
  assert.equal(after[2].body,failed.body,'retry preserves exact collective recipient snapshot');
  assert.equal(after[2].files.length,1);assert.notDeepEqual(after[2].files,first.find(a=>a.pid==='agent-a').files,'failed retry never uses successful request upload');
  assert.equal(after.filter(a=>a.pid==='agent-a').length,1,'successful participation never resent');
  assert((await page.evaluate(()=>fixture.stages.length))<=3,'at most the failed request file restaged');
  await page.evaluate(()=>{
   const asks=fixture.requests.filter(r=>r.path==='/api/dm/agent/ask').slice(0,2).map(r=>r.body);
   for(const [i,a] of asks.entries())fixture.thread.messages.push({id:a.id,lid:a.id,from:fixture.overview.me.address,dir:'out',origin:'ui',kind:a.kind,body:a.body,topic:a.topic,pid:a.pid,send_group:a.send_group,send_group_author:fixture.overview.me.fingerprint,at:'2026-10-05T10:02:00Z',target:{address:'brin/desktop',agent_id:String(i+1).repeat(32)},attachments:[{name:'inventory.txt',size:19,openable:false}],delivery:'delivered',exec:{state:i===0?'running':'done',host:'brin/desktop',attempt:1,at:1791194520}});
   reloadFixture();
  });
  await settle(page);assert.equal(await visibleCopies(),1,'durable projection replaces previews without duplicating body');
  if(process.env.AGENTNET_COLLECTIVE_TAGS==='1'){
   const rendered=await page.locator('[data-send-group]').innerText();
   assert(rendered.includes('@everyone')&&rendered.includes('@Reviewers'),'1000-member sent/history projection shows compact original labels');
   assert(!rendered.includes('Participant 0')&&!rendered.includes('agentnet:collective/'),'history never expands names or exposes frozen recipient metadata');
  }
  if(skin==='comic'){
   const group=page.locator('[data-send-group]');assert.equal(await group.count(),1);
   assert.equal(await group.locator('[data-message-bubble]').count(),1,'No empty second bubble');
   assert.equal(await group.locator('[data-send-target]').count(),2,'Each exact target retains its own state');
   assert.equal((await group.innerText()).split('inventory.txt').length-1,1,'Shared attachment shown once');
   assert.match(await group.innerText(),/Working/);assert.match(await group.innerText(),/Done/);
  }
  await page.evaluate(()=>reloadFixture());await settle(page);assert.equal(await visibleCopies(),1,'reloading server projection preserves grouping');
  await page.evaluate(()=>{fixture.thread.messages.push({id:'incoming-ref',lid:'incoming-ref',from:'brin/desktop',dir:'in',kind:'message',body:'[@Analyst](agentnet:agent/agent-a) [@Analyst](agentnet:agent/agent-b) INERT_INCOMING_MARKER',at:'2026-10-05T10:03:00Z'});reloadFixture();});
  await page.getByText(/INERT_INCOMING_MARKER/).first().waitFor();await settle(page);
  assert.equal((await asks()).length,3,'incoming text never originates fanout');
  const shot=path.join(evidence,skin+'-multi-agent-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }catch(e){const shot=path.join(evidence,skin+'-multi-agent-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,shot,errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
 finally{await context.close();}
 continue;
}

   // Insert before delivery regression branch, within existing skin/width loop.
if(process.env.AGENTNET_HELDBACK_REGRESSION==='1'){
 const {page,context}=await openCase('heldback');
 try{
  if(skin==='comic'){
   if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
   const nav=page.getByRole('navigation',{name:'Main',exact:true});
   await nav.getByRole('button',{name:/^Review/}).click();
   assert.equal(await page.getByRole('region',{name:'Held back',exact:true}).count(),0,'unknown classification offers no supported decision and stays in diagnostics');
   await nav.getByRole('button',{name:width<700?'You':'Settings',exact:true}).click();
   await page.getByRole('button',{name:/^About/}).click();
   await page.getByRole('button',{name:'Technical details',exact:true}).click();
  }
  const held=page.getByRole('region',{name:'Sync diagnostics',exact:true});
  await held.waitFor({state:'visible'});
  if(skin==='comic')await held.locator('summary').filter({hasText:'Retained sync records'}).click();
  assert.equal(await held.locator('li').count(),4);
  const specific=held.locator('li').filter({hasText:'This invitation no longer matches the current group.'});
  await specific.getByText('If you still need to join, use the newer invitation or ask the inviter for a fresh one. You can archive this old notice.',{exact:true}).waitFor();
  assert.equal(await held.getByRole('button',{name:'Archive notice',exact:true}).count(),0);
  assert.equal(await held.getByRole('button',{name:/Accept|Approve|Run task|Trust/}).count(),0,'held notices cannot authorize execution or identity');
  assert(!/SECRET_HELD_BODY|SECRET_HELD_ATTACHMENT/.test(await held.innerText()),'encrypted contents never shown');
  // Old invalid DTO has no detail/recovery: safe generic fallback remains.
  await held.getByText('The original detailed reason was not recorded or is unavailable.',{exact:true}).waitFor();
  if(skin==='comic'){
   await held.getByRole('button',{name:'Archive all 2 notices',exact:true}).click();
   await page.waitForFunction(()=>fixture.overview.quarantine.length===2);
   assert.deepEqual(await page.evaluate(()=>fixture.overview.quarantine.map(q=>q.id).sort()),['legacy-unknown','proof-waiting']);
   assert.equal(await page.evaluate(()=>fixture.retainedHeld.length),4,'archiving diagnostics retains blocked messages');
   assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body.do!=='archive_held_batch')),'archive changes no admission or task state');
  }
  await settle(page);const shot=path.join(evidence,skin+'-heldback-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
 }catch(e){const shot=path.join(evidence,skin+'-heldback-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,shot,errors,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
 finally{await context.close();}
 continue;
}

if(process.env.AGENTNET_DELIVERY_STOP_REGRESSION==='1'){
    for(const scenario of ['never','never-native','uncertain','uncertain-native','custody','delivered','running','answered','queued-error']){
     const {page,context}=await openCase('delivery-'+scenario),request='9'.repeat(32);
     await page.evaluate(id=>openFixtureMessage(id),request);
     const row=page.locator('[data-mid="'+request+'"]');
     try{await row.waitFor();}catch(e){const shot=path.join(evidence,skin+'-'+scenario+'-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,scenario,errors,shot,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}const visible=await row.innerText();
     assert(!/Cancelled|Canceled/.test(visible),skin+' '+scenario+': local stop never claims recipient cancellation');
     if(scenario.startsWith('uncertain'))assert.match(visible,/Delivery unconfirmed|delivery unconfirmed/);
     if(scenario.startsWith('never'))assert.match(visible,/Not sent|not sent/);
     if(scenario==='running')assert.match(visible,/Working|working|Running|running/);
     if(scenario==='answered')assert.match(visible,/Answered|answered/);
     if(scenario==='delivered')assert.match(visible,/Delivered|delivered|on their/);
     if(scenario==='custody')assert.match(visible,/server|custody|Sent/);
     if(skin==='comic')await row.getByRole('button',{name:/^Asked /}).click();
     const detail=await page.evaluate(()=>fixture.deliveryDetail);
     if(detail)await page.getByText(detail,{exact:true}).filter({visible:true}).first().waitFor();
     if(scenario.startsWith('uncertain'))assert(await page.getByText(/Cancellation cannot be confirmed/).filter({visible:true}).count()>0);
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/act','/api/send','/api/dm/send'].includes(r.path)).length),0,'Opening status/details never retries work');
     assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'Delivery detail fits viewport');
     await settle(page);const shot=path.join(evidence,skin+'-'+scenario+'-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();
    }
    continue;
   }
   if(process.env.AGENTNET_FULL_REASON_REGRESSION==='1'){
    const {page,context}=await openCase('oks');
    if(skin==='comic'){
      if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
      await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).click();
    }
    await page.getByText('Read the agent’s whole message',{exact:true}).click();
    await page.getByText(/FINAL_REASON_MARKER/).filter({visible:true}).first().waitFor();
    if(skin==='comic')await page.locator('[data-open]').filter({hasText:'Review carrier handoff'}).click();
    const region=page.getByRole('region',{name:'Your agent says',exact:true});
    try{await region.waitFor();}catch(e){const shot=path.join(evidence,skin+'-full-reason-failure-'+width+'.png');await page.screenshot({path:shot});console.error(JSON.stringify({skin,width,errors,shot,text:await page.locator('#skin').evaluate(e=>e.shadowRoot.innerText||e.shadowRoot.textContent),requests:await page.evaluate(()=>fixture.requests)}));throw e;}
    assert((await region.innerText()).includes(await page.evaluate(()=>fixture.why)),'Chat retains the complete overview explanation when message detail is absent');
    await settle(page);const shot=path.join(evidence,skin+'-full-reason-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();continue;
   }
   if(process.env.AGENTNET_BROWSER_APP_REGRESSION==='1'){
    const {page,context}=await openCase('browser-app');
    if(skin==='comic'){
     if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
     await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Agents',exact:true}).click();
     await page.getByRole('button',{name:'Connect an agent on your computer',exact:true}).click();
    }
    const setup=page.getByRole('region',{name:'Connect an agent on your computer',exact:true});
    await setup.getByRole('link',{name:'Get AgentNet for Linux',exact:true}).waitFor();
    assert.equal(await setup.getByRole('link',{name:'Get AgentNet for Linux',exact:true}).getAttribute('href'),'https://downloads.example/AgentNet.AppImage');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/device/link').length),0,'Looking does not create a device link');
    await setup.getByRole('button',{name:'Link the installed app to me',exact:true}).click();
    const open=setup.getByRole('link',{name:'Open this link in AgentNet',exact:true});await open.waitFor();
    assert.equal(await open.getAttribute('href'),'agentnet://open#agentnet-link-v2:fixture','Exact existing app link retained');
    assert.match(await setup.innerText(),/approve the new computer/);
    await setup.getByText('App opened without the link?',{exact:true}).click();
    await setup.getByText('https://workspace.example/#agentnet-link-v2:fixture',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/device/link').length),1);
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>['/api/assistant-setup','/api/device/decide'].includes(r.path)).length),0,'Browser setup neither changes tools nor approves a new device');
    await settle(page);const shot=path.join(evidence,skin+'-browser-app-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();continue;
   }
   if(process.env.AGENTNET_DIRECT_REVIEW_REGRESSION==='1'){
    const {page,context}=await openCase('device-oks');const request='7'.repeat(32),body='Check Orchard warehouse inventory';
    const activity=async()=>{const back=page.getByRole('button',{name:'Back to chats',exact:true});if(width===390&&await back.isVisible())await back.click();await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).click();};
    const waiting=page.getByRole('list',{name:'Waiting for you',exact:true});
    const working=page.getByRole('list',{name:'Working',exact:true});
    await activity();await waiting.getByRole('button').filter({hasText:body}).waitFor();
    if(skin==='comic'){await page.locator('header').filter({has:page.getByRole('heading',{name:'Review',exact:true})}).getByText('1 needs attention',{exact:true}).waitFor();assert.equal(await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).getByText('1',{exact:true}).count(),1,'Direct held request counts in OK badge');}
    if(skin==='comic'){
     await waiting.getByRole('button',{name:'Brin asked your agent something. Review it.',exact:true}).waitFor();
     await waiting.getByRole('button',{name:'Allow once',exact:true}).waitFor();
     for(const state of ['needs_human','interrupted']){
      await page.evaluate(state=>setDeviceState(state),state);
      await waiting.getByRole('button',{name:'Allow once',exact:true}).waitFor({state:'hidden'});
      assert.equal(await waiting.locator('[data-open]').count(),1,'Nonrunning attention still opens the exact request');
     }
     await page.evaluate(()=>setDeviceState('held'));
     await waiting.getByRole('button',{name:'Allow once',exact:true}).waitFor();
     // Same actual human request, now from a verified own phone. It is a person,
     // even if an old server still holds it; no agent is advertised there.
     await page.evaluate(()=>{const phone={address:'aster/android-phone-3',fingerprint:'phone-key'};fixture.overview.person.devices.push(phone);fixture.deviceThread.peer=phone.address;fixture.deviceThread.messages[0].from=phone.address;setDeviceState('held');fixture.overview.review[0].peer=phone.address;reloadFixture();});
     await waiting.getByRole('button',{name:'You asked your agent something. Review it.',exact:true}).waitFor();
     assert.equal(await waiting.getByText(/Your agent on Android phone 3/).count(),0,'Human own phone is not relabeled an agent');
     assert.equal(await waiting.locator('[data-me]').count(),1,'Own human uses person avatar');
     // A stale list can never approve a now-answered request.
     await page.evaluate(()=>{fixture.deviceThread.messages[0].state='answered';fixture.deviceThread.messages[0].actions=[];});
     await waiting.getByRole('button',{name:'Allow once',exact:true}).click();
     await page.locator('[data-mid="'+request+'"]').waitFor();
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='accept').length),0,'Stale review does not accept an answered request');
     await page.evaluate(()=>{fixture.deviceThread.peer='brin/desktop';fixture.deviceThread.messages[0].from='brin/desktop';setDeviceState('held');});await activity();
     await waiting.getByRole('button',{name:'Allow once',exact:true}).click();
     await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='accept'));
     assert.deepEqual(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/act'&&r.body?.do==='accept').map(r=>r.body)),[{do:'accept',id:request}],'List approval binds the exact currently held request');
    }
    await page.evaluate(()=>setDeviceState('running'));await working.getByRole('button').filter({hasText:body}).waitFor();assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0,'Running review[] request leaves decision list');
    if(skin==='comic')assert.equal(await working.getByRole('button',{name:'Allow once',exact:true}).count(),0,'Running requests offer no approval');
    if(skin==='comic'){assert.equal(await page.locator('header').filter({has:page.getByRole('heading',{name:'Review',exact:true})}).getByText('1 needs attention',{exact:true}).count(),0);assert.equal(await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).getByText('1',{exact:true}).count(),0,'Working review[] request clears OK badge');}
    assert(!/needs your (OK|decision)|Waiting for approval/.test(await working.innerText()),'Working row uses no approval claim');assert(!/\[object HTML/.test(await working.innerText()),'Working row renders peer name rather than DOM string');assert(!/Already running\s*·\s*Already running/.test(await working.innerText()),'Working status is not duplicated');assert.match(await working.innerText(),/Working|Already running/);await settle(page);const listShot=path.join(evidence,skin+'-direct-review-working-'+width+'.png');await page.screenshot({path:listShot});shots.push(listShot);
    await page.evaluate(()=>openOtherDevice());const unrelated=page.locator('[data-mid="'+'8'.repeat(32)+'"]');await unrelated.waitFor();assert((await unrelated.innerText()).includes('Different device thread'));await activity();
    await working.locator('button').filter({hasText:body}).first().click();const focused=page.locator('[data-mid="'+request+'"]');
    await focused.waitFor();assert((await focused.innerText()).includes(body),'Working row opens exact direct request');await unrelated.waitFor({state:'hidden'});assert(!await unrelated.isVisible(),'Working row leaves unrelated thread');
    await settle(page);const shot=path.join(evidence,skin+'-direct-review-running-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    await focused.getByRole('button',{name:'Stop',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Stop',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='cancel'));
    assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='cancel').body.id),request,'Stop binds exact running request');await activity();await working.getByRole('button').filter({hasText:body}).waitFor();assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0,'Stopping request remains outside decisions');
    await page.evaluate(()=>setDeviceState('done'));await page.waitForFunction(()=>!document.querySelector('#skin').shadowRoot.textContent.includes('Already running'));assert.equal(await working.getByRole('button').filter({hasText:body}).count(),0,'Completed request leaves Working');assert.equal(await waiting.getByRole('button').filter({hasText:body}).count(),0);
    assert.equal(await page.evaluate(()=>fixture.overview.needs_you.length),0,'Regression exercises direct review[] throughout');await context.close();continue;
   }
   if(process.env.AGENTNET_NOTIFY_REGRESSION==='1'){
    const {page,context}=await openCase('notifications');if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();
    await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width===1280?'Settings':'You',exact:true}).click();await page.getByRole('button',{name:/^Notifications/}).click();
    assert.equal(await page.getByRole('switch',{name:/Allow alerts from/}).count(),0,'No global people allow list');
    assert.equal(await page.getByText(/Conversation overrides/).count(),0,'No global conversation override list');
    await page.getByText('Chats notify you unless muted.',{exact:false}).waitFor();
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64)],'Opening settings preserves explicit mute');
    await settle(page);const settingsShot=path.join(evidence,'comic-notification-settings-'+width+'.png');await page.screenshot({path:settingsShot});shots.push(settingsShot);
    await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Chats',exact:true}).click();
    await page.evaluate(()=>openGroup());await page.getByRole('button',{name:'More',exact:true}).click();
    await page.getByRole('menuitem',{name:'Mute notifications',exact:true}).click();
    await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/notify/mute'));
    assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/notify/mute').body),{conv,muted:true});
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64),conv],'Chat mute preserves other explicit mute');
    await page.getByRole('button',{name:'More',exact:true}).click();await page.getByRole('menuitem',{name:'Turn notifications on',exact:true}).waitFor();
    await settle(page);const shot=path.join(evidence,'comic-notification-chat-mute-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);
    await page.getByRole('menuitem',{name:'Turn notifications on',exact:true}).click();await page.waitForFunction(()=>fixture.requests.filter(r=>r.path==='/api/notify/mute').length===2);
    assert.deepEqual(await page.evaluate(()=>fixture.overview.notify.mutes),['e'.repeat(64)],'Unmuting exact chat preserves other mute');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/notify/allow').length),0,'Mute never changes sender grants');await context.close();continue;
   }
   if(process.env.AGENTNET_OKS_REGRESSION==='1'){
    const {page,context}=await openCase('oks');
    const composer=page.getByRole('textbox',{name:'Message Brin',exact:true});await composer.fill('Unsent warehouse draft');
    await page.locator('form[aria-label="Write a message"] input[type=file]').setInputFiles({name:'draft.txt',mimeType:'text/plain',buffer:Buffer.from('synthetic unsent file')});await page.getByRole('list',{name:'Files to send'}).waitFor();
    await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/}).click();
    await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/Carrier review/}).click();assert.equal(await page.locator('[data-mid="main-note"]').count(),0);
    const oks=async()=>{if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:/^Review/}).click();await page.getByText('2 things need you',{exact:true}).waitFor();};
    if(width===390)await page.getByRole('button',{name:'Back to chats',exact:true}).click();await page.getByRole('button',{name:/^Brin 2 need your OK/}).getByText('2 need your OK',{exact:true}).waitFor();if(width===390)await page.getByRole('button',{name:/Brin.*2 need your OK/}).click();await oks();const waiting=page.getByRole('list',{name:'Waiting for you',exact:true}),working=page.getByRole('list',{name:'Working',exact:true});
    assert.equal(await waiting.locator('[data-open]').count(),2,'Running request does not count as an OK');assert.equal(await working.locator('[data-open]').count(),1);assert(!/Runs only if|Answered only if/.test(await working.innerText()),'Running row has no approval wording');await working.getByRole('button',{name:'Stop',exact:true}).waitFor();
    await waiting.getByText('Read the agent’s whole message',{exact:true}).click();assert((await waiting.innerText()).includes('FINAL_REASON_MARKER'),'List expands full needs_human detail');
    const arrow=working.locator('[data-open]');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();assert.equal(await page.locator('[data-mid="needs-person"]').count(),0,'Arrow leaves wrong topic');assert.equal(await composer.inputValue(),'Unsent warehouse draft');await page.getByRole('list',{name:'Files to send'}).waitFor();
    if(width===1280){await page.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^All/}).click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/Carrier review/}).click();await page.evaluate(()=>reloadFixture());await page.waitForFunction(()=>fixture.requests.filter(r=>r.path.startsWith('/api/dm?')).length>2);await page.locator('[data-mid="needs-person"]').waitFor();assert.equal(await page.locator('[data-mid="working-request"]').count(),0,'Background refresh preserves manually selected topic after previous focus');await arrow.click();await page.locator('[data-mid="working-request"]').waitFor();await page.locator('[data-mid="working-request"]').evaluate(e=>e.getAnimations().forEach(a=>a.finish()));await arrow.click();await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-mid="working-request"]').getAnimations().length>0);}
    const rect=await page.locator('[data-mid="working-request"]').boundingBox();assert(rect&&rect.y>=0&&rect.y<900,'Exact requested message visibly focused');await settle(page);const focusedShot=path.join(evidence,'comic-exact-topic-'+width+'.png');await page.screenshot({path:focusedShot});shots.push(focusedShot);
    await oks();await waiting.locator('[data-open]').filter({hasText:'Review carrier handoff'}).click();await page.locator('[data-mid="needs-person"]').waitFor();const region=page.getByRole('region',{name:'Your agent says',exact:true});assert((await region.innerText()).includes(await page.evaluate(()=>fixture.why)),'Chat displays full overview reason when job_detail absent');
    await oks();await waiting.locator('[data-open]').filter({hasText:'Which carrier should we use?'}).click();await page.locator('[data-mid="held-question"]').waitFor();assert.equal(await page.locator('[data-mid="needs-person"]').count(),0,'Main-flow request clears other topic');
    await page.locator('[data-mid="held-question"]').getByRole('button',{name:'Approve Brin…',exact:true}).click();const confirm=page.getByRole('dialog');assert.match(await confirm.innerText(),/This held question still waits: choose Allow once/);await confirm.getByRole('button',{name:'Approve Brin',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='approve'));assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='accept')),'Standing approval does not accept held item');
    await page.locator('[data-mid="held-question"]').getByRole('button',{name:'Allow once',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='accept'));assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='accept').body.id),'held-question');
    await oks();await working.getByRole('button',{name:'Stop',exact:true}).click();const stop=page.getByRole('dialog');await stop.getByRole('button',{name:'Stop',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/act'&&r.body?.do==='cancel'));assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/act'&&r.body?.do==='cancel').body.id),'working-request');
    await settle(page);const shot=path.join(evidence,'comic-oks-working-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);assert(!await page.evaluate(()=>fixture.requests.some(r=>r.path==='/api/dm/send')),'Navigation preserves unsent draft');await context.close();continue;
   }
   if(skin==='comic'){
    const bring=async page=>{
     await page.getByRole('button',{name:width===1280?'Bring in':'Bring someone in',exact:true}).first().click();
     const dialog=page.getByRole('dialog',{name:'Bring someone in',exact:true});
     assert(await dialog.getByRole('radio',{name:/Only this topic: Main flow/}).isChecked(),'Main is the initial scope');
     await dialog.getByRole('radio',{name:/^Whole chat/}).locator('..').click();
    };
    const room=async page=>{if(!await page.getByRole('list',{name:'Invited people',exact:true}).isVisible())await page.getByRole('button',{name:'orchard_dispatch. Who’s in this chat',exact:true}).click();};
    for(const variant of ['admin','member']){
     const {page,context}=await openCase(variant);await bring(page);
     const guestMode=page.locator('input[name=human-invite-mode][value=guest]'),memberMode=page.locator('input[name=human-invite-mode][value=member]');
     try{assert(await guestMode.isChecked(),'Default human invitation is a guest');}catch(e){console.error(await page.getByRole('dialog').innerText());throw e;};assert.equal(await memberMode.isDisabled(),variant==='member','Permanent membership is admin-only');
     if(variant==='admin'){await memberMode.locator('..').click();assert(await memberMode.isChecked());await guestMode.locator('..').click();}
     await page.getByRole('radio',{name:/Cora/}).first().locator('..').click();
     const fewer=page.getByRole('button',{name:'Fewer messages',exact:true});while(!await fewer.isDisabled())await fewer.click();
     await settle(page);const inviteShot=path.join(evidence,'comic-'+variant+'-guest-invite-'+width+'.png');await page.screenshot({path:inviteShot});shots.push(inviteShot);await page.getByRole('button',{name:'Invite Cora as a guest',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/dm/guest/invite'));
     assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/invite').body),{conv,host:cora.address,share:[],note:''});
     assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/invite').length),0,'Guest invite never grants membership');
     assert.equal(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/guest/check').body.host),cora.address,'Compatibility checks exact selected host');
     await settle(page);const shot=path.join(evidence,'comic-'+variant+'-guest-controls-'+width+'.png');await page.screenshot({path:shot});shots.push(shot);await context.close();
    }
    for(const variant of ['guest-active','guest-invited']){
     const {page,context}=await openCase(variant);assert.equal(await page.getByRole('button',{name:width===1280?'Bring in':'Bring someone in',exact:true}).count(),0,'Human guest cannot invite');
     await page.getByRole('button',{name:'orchard_dispatch. Who’s in this chat',exact:true}).click();assert.equal(await page.getByRole('button',{name:'Leave group',exact:true}).count(),0,'Guest has no membership leave action');await context.close();
    }
    for(const variant of ['admin','accepted','other-device','legacy-flags']){
     const {page,context}=await openCase(variant);await room(page);
     const list=page.getByRole('list',{name:'Invited people',exact:true}),retract=list.getByRole('button',{name:'Retract invitation',exact:true}),refresh=list.getByRole('button',{name:'Refresh invitation',exact:true});
     if(variant==='other-device'||variant==='legacy-flags'){assert.equal(await retract.count(),0);assert.equal(await refresh.count(),0);await context.close();continue;}
     await retract.click();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/groups/cancel').length),0);await page.keyboard.press('Escape');
     await refresh.click();await page.getByRole('button',{name:'Send fresh invitation',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/groups/refresh'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/refresh').body),{id:'invite-1'});
     await retract.click();await page.getByRole('dialog',{name:'Retract invitation for Cora?',exact:true}).getByRole('button',{name:'Retract invitation',exact:true}).click();await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/groups/cancel'));assert.deepEqual(await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/groups/cancel').body),{id:'fresh-invite'});await context.close();
    }
    continue;
   }
  }
  if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'),'retained synthetic evidence must stay in /tmp');fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  assert.deepEqual(errors,[]);console.log(JSON.stringify({ok:true,checks:process.env.AGENTNET_V32_RENDERED==='1'?'Bounded v32 rendered journey results are in v32-results-WIDTH.json; inspect each finding':process.env.AGENTNET_MENTION_SHORTCUT==='1'?'Physical Ctrl/Cmd+Digit2 reuses permitted DM/group/topic picker; layout/IME guards; caret/draft/hints/no-send; desktop/phone':process.env.AGENTNET_ATTENTION_LABELS==='1'?'Neutral aggregate attention wording; real consent retains OK/actions; unchanged counts, requests and authority; desktop/phone':process.env.AGENTNET_PRESENTATION_REGRESSION==='1'?'Comic short/long needs-human closure: exact agent/request, compact status, full projected result, immutable original, no action dispatch; desktop/mobile':process.env.AGENTNET_COMPOSER_FOLLOWUP_REGRESSION==='1'?'Comic natural tagged steering: no extra UI; running-only exact attempt; ordinary new request without a run; concrete ambiguity and existing Reply binding; strict completion race/retry; Android journal; desktop1280/mobile390':process.env.AGENTNET_LEGACY_LIST_REGRESSION==='1'?'Comic retained legacy list: exact identity/manager, add person, preserved leave, no agent catalog/control/grants; desktop/mobile':process.env.AGENTNET_AGENT_CHILD_REGRESSION==='1'?'Comic verified Codex author to Claude recipient, exact stopped-parent reason, no retry or replay, retained handling; desktop/mobile':process.env.AGENTNET_TOPIC_INVITE_REGRESSION==="1"?"Comic DM/group Main/named topic: Recent/Selected/Since, private files excluded, stable scope after rename/archive/arrival, broad grant disclosure; desktop/mobile":process.env.AGENTNET_REPORT_HISTORY_REGRESSION==="1"?"Comic exact blocker history: arbitrary follow-up success stays separate; confirmed/refused/new attempt and newest empty snapshots; desktop/mobile":completion?'Comic signed exact completion, queued secondary copies/details unchanged; inert/different-target/quote/unrelated negatives desktop1280/mobile390':process.env.AGENTNET_FOLLOWUP_REGRESSION==='1'?'Comic natural tagged composer replaces follow-up sheet/action: stage failure retains files, exact uncertain retry/newer draft, navigation abandons unused stages, deleted source refuses; desktop1280/mobile390':process.env.AGENTNET_REACTION_REGRESSION==='1'?'Comic: first-use/persisted frequency, success/failure/removal, human-count ordering and separate agent chips, desktop/mobile':process.env.AGENTNET_PHONE_REJOIN_REGRESSION==='1'?'Comic phone: active/pending rejoin, stale click/navigation, genuine return, exact key/agent/host identity and fresh-read error':process.env.AGENTNET_DIRECT_REVIEW_REGRESSION==='1'?'Direct review[]: held/running/stopping/complete; decision badge/list vs Working; exact request/Stop; desktop/mobile':process.env.AGENTNET_OKS_REGRESSION==='1'?'Comic: working excluded from OK count, exact topic/repeat focus, unsent draft/files preserved, full reason, future approve separate from held accept, exact Stop':process.env.AGENTNET_NOTIFY_REGRESSION==='1'?'Comic: one person grant across duplicate roots and separate conversation mute':(process.env.AGENTNET_TEST_SKINS||'comic')+': guest/member distinction, exact targets, retract/refresh, rights, desktop/mobile',shots}));
 }finally{await browser?.close();server.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
