const assert={ok:(x,m)=>{if(!x)throw Error(m)},rejects:async(fn,re)=>{try{await fn()}catch(e){if(re.test(e.message))return;throw e}throw Error('Expected rejection '+re)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[],engines=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p==='/v1/version')return json({features:['env2','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);posts.push(o.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack'))return json(null,204);
 throw Error('Unexpected synthetic route '+p);
};
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapRoom,wire.CapOwnSyncV2]){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='topic-sync-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
 e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);e.connected=false;pubs.set(address,e.pub);return e;
}
async function person(address){
 const e=await device(address);e.roster=await wire.newRoster(e.keys,address,address.split('/')[0]);e.me=await e.personRecord([e.roster],'self',null);
 chains.set(e.me.person,[JSON.parse(wire.rosterJSON(e.roster))]);
 await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);await caps(e);return e;
}
async function sibling(owner,address){
 const e=await device(address),join=await wire.joinConsent(e.keys,address,owner.me.person,owner.me.seq+1,owner.me.hash);
 const humans=[...await wire.rosterHumans(owner.roster),e.fp];
 const next=await wire.nextRoster(owner.keys,owner.address,owner.roster,[...owner.me.devices.map(d=>pubs.get(d.address)),e.pub],join,owner.roster.label,humans);
 await wire.verifyNext(next,owner.roster);
 const steps=[...await Promise.all(chains.get(owner.me.person).map(r=>wire.parseRoster(r))),next];
 owner.roster=next;owner.me=await owner.personRecord(steps,'self',owner.me);e.roster=next;e.me=await e.personRecord(steps,'self',null);
 chains.set(owner.me.person,steps.map(r=>JSON.parse(wire.rosterJSON(r))));
 await owner.store.write([{s:'kv',k:'person',v:owner.me}]);await owner.pinDevices(owner.me);
 await e.store.write([{s:'kv',k:'person',v:e.me},{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}}]);await e.pinDevices(e.me);await caps(e);await caps(owner);return e;
}
async function receive(raw,e){const env=wire.parseEnvelope(raw);await e.admit(raw,env);if(e.topicSyncRun)await e.topicSyncRun;return env.id;}
async function rootCopies(e,conv){return(await e.store.all('outbox')).filter(r=>r.sub===wire.SubRootSync&&(!conv||r.conv===conv));}

const a=await person('alice/desk'),b=await person('bob/desk'),p=await sibling(a,'alice/phone');
for(const e of [a,p])await e.store.write([{s:'persons',k:b.me.person,v:{...b.me,state:'pinned'}},{s:'pins',k:b.address,v:{address:b.address,json:wire.marshalPublic(b.pub),fingerprint:b.fp,pending:null}}]);
const conv='c'.repeat(64),topic=wire.newID(),original={v:2,id:topic,lid:topic,conv,topic,from:b.address,fp:b.fp,kind:'message',body:'Automatic title',read:false,at:1,ts:1};
await a.store.write([{s:'inbox',k:topic,v:original}]);
await a.changeChatTopic('rename',{conv,id:topic,title:'Shared private title'});
if(a.topicSyncRun)await a.topicSyncRun;
const copies=async e=>(await e.store.all('outbox')).filter(o=>o.sub==='topic-sync');
let rows=await copies(a);check(rows.length===1,'own-human rename queues one quiet encrypted title fact');
const first=rows[0];check(!wire.parseEnvelope(first.envelope).attn&&first.aside&&first.required_cap===wire.CapOwnSyncV2,'title carrier is encrypted quiet own2 metadata');
await a.post(first);await receive(first.envelope,p);
check((await p.store.get('kv','topic/'+conv+'/'+topic)).title==='Shared private title'&&(await p.store.all('inbox')).length===0,'title arrives before history without creating a message');
await p.store.write([{s:'inbox',k:topic,v:original}]);check((await p.chatTopics(conv))[0].title==='Shared private title'&&!(await p.store.get('inbox',topic)).read,'later original projects title without marking unread');
check(!(await copies(p)).length,'sender already possessing exact fact gets no echo');
await a.changeChatTopic('rename',{conv,id:topic,title:''});if(a.topicSyncRun)await a.topicSyncRun;
rows=await copies(a);const reset=rows.find(r=>wire.parseTopicSync(r.body).titles.some(t=>t.topic===topic&&t.rev===3));await receive(reset.envelope,p);await receive(first.envelope,p);
check((await p.store.get('kv','topic/'+conv+'/'+topic)).title===''&&(await p.chatTopics(conv))[0].title==='Automatic title','explicit reset survives delayed older name');
const seal=async(sender,record,target=p)=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:target.address,ts:1,kind:'message',sub:wire.SubTopicSync,replica:true,body:JSON.stringify(record)},sender.keys,target.pub);
const record=wire.parseTopicSync(reset.body),fact=record.titles[0],highest=[a.fp,p.fp].sort().at(-1),lowest=[a.fp,p.fp].sort()[0];
const tie={...record,titles:[{...fact,rev:4,writer:highest,title:'Deterministic winner'}]};await receive(await seal(a,tie),p);await receive(await seal(a,{...tie,titles:[{...tie.titles[0],writer:lowest,title:'Other concurrent rename'}]}),p);
check((await p.store.get('kv','topic/'+conv+'/'+topic)).title==='Deterministic winner','same revision uses deterministic writer fingerprint ordering');
await receive(await seal(p,tie,a),a);await a.changeChatTopic('rename',{conv,id:topic,title:'After observed winner'});if(a.topicSyncRun)await a.topicSyncRun;
const newest=(await copies(a)).find(r=>wire.parseTopicSync(r.body).titles.some(t=>t.topic===topic&&t.rev===5));check(wire.parseTopicSync(newest.body).titles[0].rev===5,'local rename advances highest observed revision');
const conflict=await receive(await seal(a,{...tie,titles:[{...tie.titles[0],title:'Conflicting same fact'}]}),p);check((await p.store.get('held',conflict)).reason==='conflicting_duplicate','same writer and revision with another title fails closed');
const unknown=wire.newID(),mixed={...tie,titles:[{...fact,topic:unknown,rev:1,title:'Must roll back'}, {...tie.titles[0],title:'Conflicting same fact'}]};await receive(await seal(a,mixed),p);check(!await p.store.get('kv','topic/'+conv+'/'+unknown),'conflicting batch stores no partial earlier title');
const foreign=await receive(await seal(b,record),p);check((await p.store.get('held',foreign)).reason==='invalid','another person cannot rename our topic');
const legacy=wire.newID();await a.store.write([{s:'inbox',k:legacy,v:{v:1,id:legacy,from:b.address,fp:b.fp,kind:'message',body:'Legacy automatic',at:2000,read:false}}]);await a.changeTopic('rename',{peer:b.address,id:legacy,title:'Legacy private title'});if(a.topicSyncRun)await a.topicSyncRun;
const legacyCopy=(await copies(a)).find(r=>wire.parseTopicSync(r.body).titles.some(t=>t.scope===b.address));await receive(legacyCopy.envelope,p);check((await p.store.get('kv','topic/'+b.address+'/'+legacy)).title==='Legacy private title','legacy exact peer scope follows the same private sync');
const q=await sibling(a,'alice/other');await a.syncTopicTitles();const qCopies=(await copies(a)).filter(r=>r.to===q.address);check(qCopies.length===1&&wire.parseTopicSync(qCopies[0].body).titles.length===2,'new own human receives latest facts only');await receive(qCopies[0].envelope,q);
check((await q.store.get('kv','topic/'+conv+'/'+topic)).title==='After observed winner','offline third device converges without public topic event');
const oldCaps=[wire.CapEnv2,wire.CapPerson,wire.CapRoom];await caps(p,oldCaps);const count=posts.length;await a.post(newest);check((await a.store.get('outbox',newest.id)).state==='waiting'&&posts.length===count,'older own2 reader waits without receiving unknown subtype');await caps(p);
const originalFetch=a.fetch;a.fetch=async()=>{throw new TypeError('Failed to fetch');};await a.post(await a.store.get('outbox',newest.id));check(posts.length===count,'offline retry sends nothing and retains carrier');a.fetch=originalFetch;
await a.post(await a.store.get('outbox',newest.id));check(posts.length===count+1&&(await a.store.get('outbox',newest.id)).envelope===newest.envelope,'restored capability retries identical encrypted bytes');
if(typeof window!=='undefined'){a.store.close();a.store=await openIDB(a.fixtureStoreName);}
const restarted=new Engine({store:a.store,base:a.base,fetch});Object.assign(restarted,{keys:a.keys,address:a.address,pub:a.pub,fp:a.fp,me:a.me,connected:false});const beforeRestart=(await copies(a)).length;await restarted.syncTopicTitles();check((await copies(a)).length===beforeRestart,'restart retains latest per-recipient carrier evidence');
const resetTopic=wire.newID();await a.store.write([{s:'inbox',k:resetTopic,v:{...original,id:resetTopic,lid:resetTopic,topic:resetTopic}}]);await a.changeChatTopic('rename',{conv,id:resetTopic,title:''});if(a.topicSyncRun)await a.topicSyncRun;
const resetFact=await a.store.get('kv',a.topicTitleKey(a.me.person,{scope:conv,topic:resetTopic}));check(resetFact.rev===2&&resetFact.title==='','first explicit reset reserves revision1 for old saved names');
await receive(await seal(p,{...record,roster:p.me.hash,titles:[{...fact,topic:resetTopic,title:'Late migrated old name',rev:1,writer:'ffffffff-ffffffff-ffffffff-ffffffff'}]},a),a);
check((await a.store.get('kv','topic/'+conv+'/'+resetTopic)).title==='','later migration cannot resurrect a name after first explicit reset');
const migration=[],migrationScope='0'.repeat(64);for(let i=0;i<65;i++){const id=(i+1).toString(16).padStart(32,'0');migration.push({s:'kv',k:'topic/'+migrationScope+'/'+id,v:{type:'topic-state',peer:migrationScope,topic:id,title:'Saved title '+i,mark:'archived'}});}migration.push({s:'kv',k:'topic-title-recovered/'+a.me.person});await a.store.write(migration);const beforeMigrationRows=(await copies(a)).filter(r=>r.to===p.address),beforeMigration=beforeMigrationRows.length;await restarted.syncTopicTitles();const migrated=(await copies(a)).filter(r=>r.to===p.address&&!beforeMigrationRows.some(old=>old.id===r.id));check(migrated.length===2&&migrated.every(r=>wire.parseTopicSync(r.body).titles.length<=64),'65 saved local names migrate once through bounded carriers');await restarted.syncTopicTitles();check((await copies(a)).filter(r=>r.to===p.address).length===beforeMigration+2,'completed migration never reissues old names');
const markKey='topic/'+conv+'/'+topic,local=await p.store.get('kv',markKey);await p.store.write([{s:'kv',k:markKey,v:{...local,mark:'archived',mark_count:7}}]);await receive(newest.envelope,p);check((await p.store.get('kv',markKey)).mark_count===7,'title merge retains local topic marks');
const heldBatch=migrated.find(r=>wire.parseTopicSync(r.body).titles.length===64);await a.store.write([{s:'outbox',k:heldBatch.id,v:{...heldBatch,state:'quarantined'}}]);const beforeHeld=(await copies(a)).length;
for(let wake=0;wake<3;wake++)await restarted.syncTopicTitles();
check((await copies(a)).length===beforeHeld,'quarantined full title batch survives repeated recovery wakes without replacement');
check((await a.store.get('outbox',heldBatch.id)).envelope===heldBatch.envelope,'quarantined title carrier retains exact ciphertext');
const heldTitle=wire.parseTopicSync(heldBatch.body).titles[0],titleChecks=[],titleOps=await restarted.topicTitleOps(heldTitle.scope,heldTitle.topic,'',titleChecks);await a.store.write(titleOps,titleChecks);await restarted.syncTopicTitles();
const afterHeld=(await copies(a)).filter(r=>r.to===p.address&&!beforeMigrationRows.some(old=>old.id===r.id)&&!migrated.some(old=>old.id===r.id));
check(afterHeld.length===1&&wire.parseTopicSync(afterHeld[0].body).titles.length===1&&wire.parseTopicSync(afterHeld[0].body).titles[0].title==='','new explicit reset remains sendable beside held title batch');
const own=await p.store.get('kv','person'),pin=await p.store.get('pins',a.address);
for(const mode of ['agent-host','removed','frozen','pending-pin','changed-pin']){
 const changed=mode==='agent-host'?{...own,human_keys:own.human_keys.filter(fp=>fp!==a.fp)}:mode==='removed'?{...own,devices:own.devices.filter(d=>d.fingerprint!==a.fp)}:mode==='frozen'?{...own,state:'conflict'}:own;
 await p.store.write([{s:'kv',k:'person',v:changed},{s:'pins',k:a.address,v:mode==='pending-pin'?{...pin,pending:{fingerprint:b.fp}}:mode==='changed-pin'?{...pin,fingerprint:b.fp}:pin}]);
 const denied=await receive(await seal(a,{...record,titles:[{...fact,topic:wire.newID(),rev:5}]}),p);check((await p.store.get('held',denied)).reason===(mode==='pending-pin'?'key_changed':'invalid'),'topic ingest rejects '+mode);
 await p.store.write([{s:'kv',k:'person',v:own},{s:'pins',k:a.address,v:pin}]);
}
let changedProfile=false;const currentOwn=await a.store.get('kv','person');a.fetch=async(url,o)=>{const r=await fetch(url,o);if(!changedProfile&&new URL(url).pathname.endsWith('/profile')){changedProfile=true;await a.store.write([{s:'kv',k:'person',v:{...currentOwn,human_keys:currentOwn.human_keys.filter(fp=>fp!==p.fp)}}]);}return r;};await assert.rejects(()=>a.topicSyncGate(newest),/current own-human/);checks++;a.fetch=fetch;await a.store.write([{s:'kv',k:'person',v:currentOwn}]);
const raceTopic=wire.newID(),raceRecord={...record,titles:[{...fact,topic:raceTopic,rev:9}]},raw=await seal(a,raceRecord),env=wire.parseEnvelope(raw),n=await wire.open(raw,p.keys,p.address,a.pub),prepared=await p.admitTopicSync(n,env,pin);
const other=typeof window==='undefined'?p.store:await openIDB(p.fixtureStoreName);await other.write([{s:'kv',k:'person',v:{...own,human_keys:[p.fp]}}]);await assert.rejects(()=>p.store.write(prepared,prepared.checks),/storage changed/);checks++;if(other!==p.store)other.close();await p.store.write([{s:'kv',k:'person',v:own}]);check(!await p.store.get('kv','topic/'+conv+'/'+raceTopic),'competing authority change rolls back title projection and fact');
for(const mutate of [r=>delete r.titles[0].title,r=>r.titles[0].title=null,r=>r.titles[0].title=' noncanonical ',r=>r.titles[0].title='x'.repeat(121),r=>r.titles[0].rev=Number.MAX_SAFE_INTEGER+1,r=>r.titles[0].scope='wrong',r=>r.titles.push({...r.titles[0]}),r=>r.unexpected=true]){const r=structuredClone(record);mutate(r);await assert.rejects(async()=>wire.parseTopicSync(JSON.stringify(r)),/topic|unknown|title|field/);checks++;}
check(wire.topicTitle(' A\u0085B\u00a0 C ')==='A B C'&&wire.topicTitle('\ufeffname')==='\ufeffname','canonical title matches Go Unicode whitespace, preserving non-whitespace BOM');
await assert.rejects(()=>wire.seal({v:2,id:wire.newID(),from:a.address,to:p.address,ts:1,kind:'task',sub:wire.SubTopicSync,replica:true,body:JSON.stringify(record)},a.keys,p.pub),/quiet rootless/);checks++;
console.log('topic sync browser checks',checks);
if(typeof window!=='undefined'){for(const e of engines){await Promise.all([e.topicSyncRun,e.historyRun,e.readSyncRun,e.rootSyncRun,e.invitationSyncRun].filter(Boolean));e.store.close();await new Promise((resolve,reject)=>{const r=indexedDB.deleteDatabase(e.fixtureStoreName);r.onsuccess=resolve;r.onerror=()=>reject(r.error);});}window.topicSyncResult={ok:true,checks,storage:'IndexedDB'};}
