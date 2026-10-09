// Real signed/encrypted browser turns over isolated stores, no live service.
const assert=typeof window==='undefined'?(await import('node:assert/strict')).default:{
 ok:(value,message)=>{if(!value)throw Error(message||'Expected truthy');},
 deepEqual:(actual,expected,message)=>{if(JSON.stringify(actual)!==JSON.stringify(expected))throw Error(message||'Structures differ');},
 rejects:async(fn,re)=>{try{await fn();}catch(error){if(re.test(error.message))return;throw error;}throw Error('Expected rejection '+re);}
};
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, openIDB, chatTopicAssignments } from '../static/engine.mjs';
let checks=0;
const check=(v,why)=>{assert.ok(v,why);checks++;};
const same=(a,b,why)=>{assert.deepEqual(a,b,why);checks++;};
const refuses=async(fn,re)=>{await assert.rejects(fn,re);checks++;};
const users=[];
for(const name of ['alice','bob','outsider']){
 const storeName='history-contribution-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(storeName);
 const e=new Engine({store,base:'https://synthetic.invalid',now:()=>1790000000000,fetch:async()=>{throw Error('No live network in contribution check');}});
 e.fixtureStoreName=storeName;
 e.keys=await wire.newKeys();e.address=name+'/desk';e.pub=await wire.publicEntry(e.keys,e.address);e.fp=await wire.fingerprint(e.pub);
 e.roster=await wire.newRoster(e.keys,e.address,name);e.me=await e.personRecord([e.roster],'self',null);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address:e.address,fingerprint:e.fp}}]);
 e.refreshPerson=async p=>p;e.supports=async()=>[true,'',false];e.sendGroupCopy=async()=>'';e.queueOutbox=()=>{};e.changed=()=>{};e.syncTopicTitles=async()=>{};
 e.ctlSupport=async(address,pin,cap)=>{const other=users.find(u=>u.address===address);return [other?.caps?.includes(cap)??true,'peer_update: org1 required'];};
 e.caps=[wire.CapTopicOrganization];users.push(e);
}
for(const e of users)for(const other of users){
 const p=await e.personRecord([other.roster],other===e?'self':'pinned',null);
 await e.store.write([{s:'persons',k:p.person,v:p},{s:'pins',k:other.address,v:{address:other.address,json:wire.marshalPublic(other.pub),fingerprint:other.fp,pending:null}}]);
}
const [a,b,outsider]=users,root=await wire.newRoot(a.keys,{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},{person:b.me.person,roster:b.me.hash});
const conv=await wire.rootID(root),c={id:conv,root:wire.rootJSON(root),peer:b.me.person,created:root.created,creator:a.address};
await a.store.write([{s:'convs',k:conv,v:c}]);
await b.store.write([{s:'convs',k:conv,v:{...c,peer:a.me.person}}]);
await outsider.store.write([{s:'convs',k:conv,v:c}]);
const realm=wire.newID();for(const e of users){e.realm=realm;e.groupSupport=async()=>false;}
const members=users.map(e=>({person:e.me.person,roster:e.me.hash})).sort((x,y)=>x.person.localeCompare(y.person));
const gr=await wire.signGroupRoot(a.keys,{v:wire.GroupRootVersion,kind:'group',realm,title:'Destination group',creator:{person:a.me.person,roster:a.me.hash,address:a.address,fingerprint:a.fp},members,admins:[a.me.person],nonce:wire.newID(),created:1790000000}),destination=await wire.rootID(gr);
const groupMembers=[];for(const m of members){const e=users.find(e=>e.me.person===m.person),admission=await wire.signGroupAdmission(e.keys,{conv:destination,realm,person:m.person,roster:m.roster,seq:0,prev:'',history:null,by:e.fp});groupMembers.push({...m,admin:e===a,admission});}
const state=await wire.signGroupState(a.keys,{v:1,conv:destination,realm,seq:0,prev:'',title:'Destination group',members:groupMembers,actor:a.me.person,actor_roster:a.me.hash,by:a.fp});
await wire.verifyGroupState(state,gr,null,async person=>users.find(e=>e.me.person===person)?.roster,[]);
const packet={root:gr,state,withdrawals:null},context=wire.groupContextJSON(packet);
for(const e of users)await e.store.write([{s:'kv',k:'group/'+destination,v:{root:wire.rootJSON(gr),context,records:['signed bootstrap context verified above'],withdrawals:[],pending:[],rosters:{}}}]);
const deliver=async sent=>{for(const e of [b,outsider])for(const r of (await a.store.all('outbox')).filter(r=>r.lid===sent.lid&&r.to===e.address)){const env=wire.parseEnvelope(r.envelope);await wire.verifyEnvelope(env,a.pub.sign_key);const n=await wire.open(r.envelope,e.keys,e.address,a.pub),ops=await e.admitGroupTurn(n,env,await e.pinned(a.address),{...n,at:1790000000000,fp:a.fp,read:false});await e.store.write(ops,ops.checks||[]);}};
const parent=await a.sendDM({conv,body:'Unselected secret parent'});a.now=()=>1790000001000;const selected=await a.sendDM({conv,body:'Selected @agent task text',reply_to:parent.lid});a.now=()=>1790000000000;const child=await a.sendDM({conv,body:'Selected reply',reply_to:selected.lid});
const original=(await a.store.all('outbox')).map(r=>[r.id,r.envelope]);
let request={source:conv,ids:[child.id,selected.id],destination},review=await a.api('/api/history/contribution/preview',request);
same(review.items.map(m=>m.id),[selected.id,child.id],'selected parents precede children despite source clock skew and reverse click order');
check(review.body.includes('parent not shared')&&!review.body.includes('Unselected secret parent'),'omitted parent never disclosed');
check(review.body.includes('agentnet:message/'+selected.id+'?conv='+conv),'authorized original deep link retained');
check(review.items[1].selected_parent===1&&review.audience.length===3,'selected relations and full current audience reviewed');
const sent=await a.api('/api/history/contribution/apply',review);await deliver(sent);
for(const e of [b,outsider]){const m=(await e.convMessages(destination,await e.store.all('inbox'),await e.store.all('outbox'))).find(m=>m.lid===sent.lid);check(m?.kind==='message'&&m.body===review.body&&!m.pid&&!m.target&&!m.followup&&!m.reply_to,'signed ordinary snapshot inert at both group recipients');}
for(const [id,envelope] of original)same((await a.store.get('outbox',id)).envelope,envelope,'source originals remain byte-identical');
same((await a.applyHistoryContribution(review)).lid,sent.lid,'exact contribution retry deduplicated');
await refuses(()=>a.applyHistoryContribution({...review,body:review.body+' changed'}),/changed/);
if(typeof window!=='undefined'){a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:'https://synthetic.invalid',fetch:async()=>{throw Error('Restart is offline');}});await restarted.load();same((await restarted.applyHistoryContribution(review)).lid,sent.lid,'same operation retained after offline restart');
await refuses(()=>outsider.previewHistoryContribution(request),/member|current|membership/);
review=await a.previewHistoryContribution({source:conv,ids:[selected.id],destination});
const edit=wire.newID();await a.store.write([{s:'outbox',k:edit,v:{id:edit,lid:edit,conv,to:b.address,control:true,sub:wire.SubRevision,body:JSON.stringify({rev:1,text:'Edited selected'}),ref:{id:selected.lid,fingerprint:a.fp},person:a.me.person,state:'delivered',at:1790000000000}}]);
await refuses(()=>a.applyHistoryContribution(review),/changed/);
review=await a.previewHistoryContribution({source:conv,ids:[selected.id],destination});
const pin=await a.store.get('pins',outsider.address);await a.store.write([{s:'pins',k:outsider.address,v:{...pin,pending:pin.json}}]);await refuses(()=>a.applyHistoryContribution(review),/key|changed/);await a.store.write([{s:'pins',k:outsider.address,v:pin}]);
const topic=wire.newID();review=await a.previewHistoryContribution({source:conv,ids:[selected.id],destination,topic,new_topic:true,title:'History topic'});await a.applyHistoryContribution(review);check((await a.chatTopics(destination)).find(t=>t.id===topic)?.title==='History topic','named contribution topic committed with ordinary snapshot');
review=await a.previewHistoryContribution({source:conv,ids:[child.id],destination,topic});await a.changeChatTopic('archive',{conv:destination,id:topic,count:1});await refuses(()=>a.applyHistoryContribution(review),/active|changed|reopen/);
review=await a.previewHistoryContribution({source:conv,ids:[child.id],destination});
await a.changeChatMainTopic('archive',{conv:destination});await refuses(()=>a.applyHistoryContribution(review),/changed/);await refuses(()=>a.previewHistoryContribution({source:conv,ids:[child.id],destination}),/active|reopen/);await a.changeChatMainTopic('reopen',{conv:destination});
review=await a.previewHistoryContribution({source:conv,ids:[child.id],destination});
const commit=a.commitReceiverCopies; a.commitReceiverCopies=async function(...args){await a.store.write([{s:'pins',k:outsider.address,v:{...pin,pending:pin.json}}]);return commit.apply(this,args);};
try{await refuses(()=>a.applyHistoryContribution(review),/changed/);}finally{a.commitReceiverCopies=commit;await a.store.write([{s:'pins',k:outsider.address,v:pin}]);}
check(!(await a.store.all('outbox')).some(x=>x.lid===review.operation),'commit-time audience change leaves no encrypted outgoing copy');
const bytes=new TextEncoder().encode('Chosen historical file'),file=await a.sendDM({conv,body:'Selected file source',files:[{name:'chosen.txt',size:bytes.length,bytes}]});
review=await a.previewHistoryContribution({source:conv,ids:[file.id],destination,files:[{id:file.id,index:0}]});check(review.items[0].files[0].available&&review.items[0].files[0].selected,'chosen file exact availability reviewed');const withFile=await a.applyHistoryContribution(review),copy=(await a.store.all('outbox')).find(r=>r.lid===withFile.lid&&r.to===b.address),opened=await wire.open(copy.envelope,b.keys,b.address,a.pub);same([...await wire.decryptFile(copy.files[0].ct,opened.attachments[0],b.keys)],[...bytes],'chosen bytes durably encrypted to exact destination');
const keptReview=await a.previewHistoryContribution({source:conv,ids:[file.id],destination,files:[{id:file.id,index:0}]});
await a.sendGroupTurn(await a.groupRecord(destination),{id:keptReview.operation,queued:true,kind:'message',body:keptReview.body,topic:keptReview.topic||'',origin:'ui'});
await refuses(()=>a.applyHistoryContribution(keptReview),/changed/);
const sha=review.items[0].files[0].sha256;await a.store.write([{s:'files',k:'kept/'+sha}]);await refuses(()=>a.previewHistoryContribution({source:conv,ids:[file.id],destination,files:[{id:file.id,index:0}]}),/unavailable/);
await refuses(()=>a.previewHistoryContribution({source:conv,ids:Array.from({length:65},()=>wire.newID()),destination}),/64/);
console.log(JSON.stringify({checks}));
if(typeof window!=='undefined'){
 for(const e of [...users,restarted]){e.stop();await Promise.all([e.topicSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun].filter(Boolean));e.store.close();}
 for(const e of users)await new Promise((resolve,reject)=>{const request=indexedDB.deleteDatabase(e.fixtureStoreName);request.onsuccess=resolve;request.onerror=()=>reject(request.error);});
 window.historyContributionResult={ok:true,checks,storage:'IndexedDB'};
}
