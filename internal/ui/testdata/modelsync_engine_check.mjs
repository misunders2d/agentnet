const assert={ok:(x,m)=>{if(!x)throw Error(m)},rejects:async(fn,re)=>{try{await fn()}catch(e){if(re.test(e.message))return;throw e}throw Error('Expected rejection '+re)}};
import {Engine,memoryStore,openIDB} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks=0;const check=(x,m)=>{assert.ok(x,m);checks++;};
const blobs=new Map();
const pubs=new Map(),chains=new Map(),profiles=new Map(),posts=[],engines=[];
const json=(v,status=200)=>new Response(v==null?null:JSON.stringify(v),{status});
const fetch=async(url,o={})=>{
 const u=new URL(url),p=u.pathname;
 if(p==='/v1/blobs'){const b=JSON.parse(o.body);blobs.set(b.id,{...b,received:0,state:'uploading',bytes:new Uint8Array(b.size)});return json(blobs.get(b.id));}
 if(p.startsWith('/v1/blobs/')){const id=p.split('/')[3],b=blobs.get(id);if(p.endsWith('/data'))return new Response(b.bytes);if(o.method==='PUT'){const bytes=new Uint8Array(o.body);b.bytes.set(bytes,Number(u.searchParams.get('offset')));b.received+=bytes.length;}if(p.endsWith('/complete'))b.state='stored';return json(b);}
 if(p==='/v1/version')return json({features:['env2','env3','person2','caps','notify1']});
 if(p.endsWith('/profile'))return json(profiles.get(p.slice(11,-8)));
 if(p.startsWith('/v1/persons/')&&p.endsWith('/chain'))return json({records:chains.get(p.slice(12,-6)).filter(r=>r.seq>Number(u.searchParams.get('after'))),more:false});
 if(/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p))return json({public:JSON.parse(wire.marshalPublic(pubs.get(p.slice(11))))});
 if(p==='/v1/messages'){const e=wire.parseEnvelope(o.body);await wire.verifyEnvelope(e,pubs.get(e.from).sign_key);posts.push(o.body);return json({state:'custody'});}
 if(p.endsWith('/wait'))return json({state:'custody'});
 if(p.endsWith('/ack'))return json(null,204);
 throw Error('Unexpected synthetic route '+p);
};
async function caps(e,names=[wire.CapEnv2,wire.CapPerson,wire.CapRootSync,wire.CapReadSync,wire.CapModelSync,wire.CapRoom,wire.CapOwnSyncV2,wire.CapOwnSyncV3,wire.CapControl]){
 const session=wire.newID();profiles.set(e.address,{person:JSON.parse(wire.rosterJSON(e.roster)),sessions:[session],caps:[JSON.parse(wire.capsJSON(await wire.newCaps(e.keys,e.address,session,names)))],live:true});
}
async function device(address){
 const name='direct-history-'+wire.newID(),store=typeof window==='undefined'?memoryStore():await openIDB(name),e=new Engine({store,base:'https://synthetic.invalid',fetch});e.fixtureStoreName=name;engines.push(e);
 e.keys=await wire.newKeys();e.address=address;e.pub=await wire.publicEntry(e.keys,address);e.fp=await wire.fingerprint(e.pub);e.connected=false;pubs.set(address,e.pub);return e;
}
async function person(address){
 const e=await device(address);e.roster=await wire.newRoster(e.keys,address,address.split('/')[0]);e.me=await e.personRecord([e.roster],'self',null);
 chains.set(e.me.person,[JSON.parse(wire.rosterJSON(e.roster))]);
 await e.store.write([{s:'kv',k:'identity',v:{keys:e.keys,address,fingerprint:e.fp}},{s:'kv',k:'person',v:e.me}]);await e.pinDevices(e.me);await caps(e);return e;
}
async function sibling(owner,address,human=true){
 const e=await device(address),join=await wire.joinConsent(e.keys,address,owner.me.person,owner.me.seq+1,owner.me.hash);
 const humans=[...await wire.rosterHumans(owner.roster),...(human?[e.fp]:[])];
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


const a=await person('owner/host'),p=await sibling(a,'owner/phone'),foreign=await person('other/host');
const model={agent_id:'',model:'gpt-6.1-sol',harness:'codex',executor:'a'.repeat(64),at:1,revision:1},named={...model,agent_id:wire.newID(),model:'Claude Opus 4.6'};
const record=(reports=[model,named])=>({v:1,person:a.me.person,roster:a.me.hash,reports});
const carrier=async(sender=a,body=record(),target=p,extra={})=>wire.seal({v:2,id:wire.newID(),from:sender.address,to:target.address,ts:1,kind:'message',sub:wire.SubModelSync,replica:true,body:JSON.stringify(body),...extra},sender.keys,target.pub);
const id=await receive(await carrier(),p);
check((await p.modelReports()).length===2,'default and named own-host reports are privately projected');
check(!await p.store.get('inbox',id)&&await p.store.get('receipts',id),'model report is quiet custody, never inbox/job');
const previous=JSON.stringify(await p.modelReports());await receive(await carrier(),p);check(JSON.stringify(await p.modelReports())===previous,'same revision duplicate never churns time or report');
await receive(await carrier(a,record([{...model,revision:2,at:2,model:'new model'}])),p);await receive(await carrier(a,record([model])),p);
check((await p.modelReports()).find(r=>!r.agent_id).model==='new model','older report never replaces newer snapshot');
const conflict=await receive(await carrier(a,record([{...model,revision:2,at:2,model:'conflict'}])),p);check((await p.store.get('held',conflict))?.reason==='invalid','same revision conflict is held');
const reverse={revision:2,at:2,executor:model.executor,harness:model.harness,model:'new model',agent_id:''};const reversed=await receive(await carrier(a,record([reverse])),p);check(!await p.store.get('held',reversed),'JSON field order does not create a revision conflict');
const other=await receive(await carrier(foreign),p);check((await p.store.get('held',other))?.reason==='invalid','foreign sender cannot inject private model metadata');
for(const mode of ['agent-host-reader','removed','frozen','pending-pin','changed-pin']){
 const own=await p.store.get('kv','person'),pin=await p.store.get('pins',a.address),changed=mode==='agent-host-reader'?{...own,human_keys:[a.fp]}:mode==='removed'?{...own,devices:own.devices.filter(d=>d.address!==a.address)}:mode==='frozen'?{...own,state:'conflict'}:own;
 await p.store.write([{s:'kv',k:'person',v:changed},{s:'pins',k:a.address,v:mode==='pending-pin'?{...pin,pending:{fingerprint:foreign.fp}}:mode==='changed-pin'?{...pin,fingerprint:foreign.fp}:pin}]);
 const held=await receive(await carrier(a,record([{...model,revision:3}])),p);check(!!await p.store.get('held',held),'model admission fences '+mode);
 check(!(await p.modelReports()).length,'private overview hides stale host authority '+mode);
 await p.store.write([{s:'kv',k:'person',v:own},{s:'pins',k:a.address,v:pin}]);
}
const host=await sibling(a,'owner/agent-server',false);
// Refresh the phone through the same verified signed roster chain.
await p.refreshPerson(p.me);
const hostCarrier=await receive(await carrier(host,record([model])),p);
check(!await p.store.get('held',hostCarrier)&&(await p.modelReports()).some(r=>r.host_key===host.fp&&r.model===model.model),'current own agent host reports its own model to human reader');
check((await p.modelReports()).filter(r=>r.host_key===a.fp).length===2,'agent-host metadata cannot overwrite another host snapshot');
const deniedHostReader=await receive(await carrier(a,record([model]),host),host);
check((await host.store.get('held',deniedHostReader))?.reason==='invalid'&&(await host.modelReports()).length===0,'agent host has no private reader authority');
check((await p.store.all('inbox')).length===0&&(await p.store.all('convs')).length===0&&(await p.store.all('outbox')).length===0,'metadata creates no conversations, grants or jobs');
for(const bad of [{...model,model:''},{...model,model:'x\ncontrol'},{...model,model:'x'.repeat(121)},{...model,model:'\ud800'},{...model,revision:0},{...model,at:9007199254740992},{...model,effort:'high'}]){await assert.rejects(()=>carrier(a,record([bad])),/model report|agent model/);checks++;}
for(const extra of [{send_group:wire.newID()},{session:wire.newID()},{fallback:true},{chan:wire.newID()},{target:{address:p.address,fingerprint:p.fp}},{pid:wire.newID()},{origin:'ui'}]){await assert.rejects(()=>carrier(a,record(),p,extra),/quiet|attention|root sync|send group|execution target/);checks++;}
await assert.rejects(()=>carrier(a,record([model,model])),/duplicate/);checks++;
const raw=await carrier(a,record(),p),env=wire.parseEnvelope(raw),rec={id:env.id,to:p.address,recipient_fp:p.fp,required_cap:wire.CapModelSync,sub:wire.SubModelSync,body:JSON.stringify(record()),envelope:raw,at:1,state:'queued'};
await a.store.write([{s:'outbox',k:rec.id,v:rec}]);await caps(p,[wire.CapEnv2,wire.CapPerson,wire.CapRoom]);const beforePosts=posts.length;await a.post(rec);
check(posts.length===beforePosts&&(await a.store.get('outbox',rec.id)).state==='waiting','older reader holds exact durable ciphertext quietly');
const humanID=wire.newID(),humanRaw=await wire.seal({id:humanID,from:a.address,to:p.address,ts:1,kind:'message',body:'real turn'},a.keys,p.pub);
await a.store.write([{s:'outbox',k:humanID,v:{id:humanID,v:1,to:p.address,fp:p.fp,envelope:humanRaw,kind:'message',body:'real turn',at:2,state:'queued'}}]);a.connected=true;await a.flushOutboxOnce();a.connected=false;
check((await a.store.get('outbox',humanID)).state==='custody','waiting rootless model report never FIFO-blocks real turn even without legacy aside flag');
await caps(p);await a.post({...rec,state:'queued'});check((await a.store.get('outbox',rec.id)).state==='custody','updated exact reader sends retained carrier');
check((await a.store.get('outbox',rec.id)).envelope===raw,'capability retry preserves exact signature and ciphertext');
if(typeof window!=='undefined'){p.store.close();p.store=await openIDB(p.fixtureStoreName);}
const restarted=new Engine({store:p.store,base:p.base,fetch});Object.assign(restarted,{keys:p.keys,address:p.address,pub:p.pub,fp:p.fp,me:p.me,connected:false});check((await restarted.modelReports()).find(r=>!r.agent_id&&r.host_key===a.fp).model==='new model','private model snapshot survives restart');
check(wire.MaxAdvertisedCaps===23,'mdl1, org1, tss1 and ha1 fit native advertisement bound');
const result={ok:true,checks};if(typeof window!=='undefined')window.modelSyncResult=result;console.log(JSON.stringify(result));
