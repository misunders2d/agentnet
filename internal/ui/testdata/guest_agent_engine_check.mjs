// Exact browser producer and real signed reader path; isolated peers, no live model.
import * as wire from '../static/wire.mjs';
import {world,check,refuses,now,checks} from './human_engine_check.mjs';

for(const group of [false,true]) {
 const w=await world(),{alice:a,bob:b,carol:g}=w;
 const host=await w.sibling(g,'carol/agent-host');
 const names=[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom,wire.CapGuestAgent,wire.CapTopicParticipation];
 for(const e of w.users){e.realm='f'.repeat(32);await w.caps(e,names);}
 let conv=w.conv;
 if(group) {
  conv=await a.createGroup('Guest own agent');
  const i=await a.inviteGroup({conv,person:b.me.person});await w.drain(b);await b.decideGroup({id:i.id,accept:true});await w.drain(a);
  if(a.recoveringGroups)await a.recoveringGroups;await a.recoverGroupIntents();await w.drain(b);await b.retryHeld();
 }
 const topic=wire.newID();
 const selected=await a.sendDM({conv,topic,body:'Selected before guest',files:[{name:'selected.txt',bytes:new TextEncoder().encode('EXACT_GUEST_SELECTED_BYTES')}]});
 await a.sendDM({conv,topic,body:'Unselected private history'});await w.drain(b);
 const parent=await a.changeHuman('invite',{conv,host:g.address,topic,share:[selected.id]});await w.drain(g);await w.drain(b);
 await g.changeHuman('decide',{pid:parent.pid,accept:true});await w.drain(a);await w.drain(b);
 await a.recoverHumanExcerpts();await w.drain(g);
 const excerpt=(await g.store.all('inbox')).find(r=>r.pid===parent.pid&&r.sub==='excerpt');check(!!excerpt,'guest selected excerpt source exists '+group);
 await refuses(()=>g.inviteAgent({conv,host:host.address,topic:''}),/exact accepted topic guest/);
 await refuses(()=>g.inviteAgent({conv,host:b.address,topic}),/own current agent host/);
 await refuses(()=>g.inviteAgent({conv,host:host.address,topic,share:[wire.newID()]}),/not admitted/);
 const child=await g.inviteAgent({conv,host:host.address,topic,share:[excerpt.id]});
 const originalInvite=(await g.convEvents(conv)).find(x=>x.e.pid===child.pid&&x.e.type==='invite');
 const retry=await g.inviteAgent({conv,host:host.address,topic,share:[excerpt.id]});
 check(retry.pid===child.pid&&(await g.convEvents(conv)).filter(x=>x.e.pid===child.pid&&x.e.type==='invite').every(x=>wire.eventJSON(x.e)===wire.eventJSON(originalInvite.e)),'producer retry preserves exact child PID and signed invite '+group);
 for(const e of [host,a,b]){await w.drain(e);await e.retryHeld();}
 for(const e of [a,b,host]) {
  check((await e.store.all('inbox')).some(r=>r.pid===child.pid),'child admitted '+e.address+' '+JSON.stringify((await e.store.all('held')).map(h=>({reason:h.reason,code:h.detail_code}))));
  const p=(await e.agentConv(child.pid)).info;
  check(p.parent===(await g.agentConv(parent.pid)).info.decision&&!p.member&&p.owner_invited,'browser guest creates exact transient own-host scope '+group+' '+e.address);
 }
 const childExcerpt=(await host.store.all('inbox')).find(r=>r.pid===child.pid&&r.sub==='excerpt');
 check(wire.parseHistory(childExcerpt.body).body==='Selected before guest'&&!(await host.store.all('inbox')).some(r=>r.body==='Unselected private history'),'only admitted selection reaches native host '+group);
 const selectedFile=await host.openFile(childExcerpt.id,0,'in');check(new TextDecoder().decode(selectedFile.bytes)==='EXACT_GUEST_SELECTED_BYTES','selected file bytes retained '+group);
 const c=await host.store.get('convs',conv)||await host.groupRecord(conv),info=(await host.agentConv(child.pid)).info;
 const accept=await wire.signEvent(host.keys,{conv,pid:child.pid,type:'accept',prev:info.invite,ts:Math.floor(now/1000),author:host.author()});
 await host.sendExternal(c,{kind:'message',sub:'event',pid:child.pid,body:wire.eventJSON(accept)},info);
 for(const e of [a,b,g])await w.drain(e);
 check((await a.agentConv(child.pid)).info.state==='active','browser member admits exact outside host consent '+group);
 const asked=await a.askAgent({pid:child.pid,topic,body:'Permitted topic request'});
 const copy=(await a.store.all('outbox')).find(r=>r.lid===asked.lid&&r.to===host.address),req=await wire.open(copy.envelope,host.keys,host.address,a.pub);
 check(req.target.address===host.address&&!req.replica,'browser member produces exact executable native-host copy '+group);
 // This Engine supplies the isolated native host's signer. Browsers never run
 // an agent; native execution is covered by TestTopicGuestInvitesOwnAgent.
 const evidence=await host.humanEvidence(c,req.human);
 await host.humanTurnAuthorization(req,evidence,(await host.agentConv(child.pid)).info,a.address,a.fp,host.address,host.fp);
 const {to,...received}=req;
 await host.store.write([{s:'inbox',k:req.id,v:{...received,fp:a.fp,at:now,state:'answered'}}]);
 const h={...req.human};delete h.author_pid;
 await host.sendExternal(c,{kind:'answer',pid:child.pid,reply_to:asked.lid,body:'Correlated guest agent answer',human:h},(await host.agentConv(child.pid)).info);
 await w.drain(a);
 check((await a.store.all('inbox')).some(r=>r.reply_to===asked.lid&&r.body==='Correlated guest agent answer'),'browser reader admits correlated output '+group);
 if(group) {
  const reply=(await a.store.all('inbox')).find(r=>r.reply_to===asked.lid&&r.kind==='answer'),item=a.itemOf(reply,false),chat=await a.groupRecord(conv);
  const witness=await a.makeGroupHistoryWitness(chat,item);
  check(witness.memberships.some(e=>e.pid===parent.pid&&e.type==='accept')&&!witness.memberships.some(e=>e.pid===parent.pid&&e.type==='invite'),'inert witness carries only exact public parent closure');
  await a.groupHistoryWitness(chat,{...item,group_history:witness});
 }
 await a.changeHuman('end',{pid:parent.pid});await a.discloseHumanPass();await b.discloseHumanPass();await w.drain(host);await host.retryHeld();
 check((await host.agentConv(child.pid)).info.state==='dismissed','browser outside host learns parent end '+group);
 await refuses(()=>a.askAgent({pid:child.pid,topic,body:'After parent ended'}),/not active/);
}
console.log('browser guest own-agent checks passed: '+checks);
