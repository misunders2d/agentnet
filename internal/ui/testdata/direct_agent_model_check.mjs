import assert from 'node:assert/strict';
import model from './comic_model.mjs';
import { pendingSends } from '../static/optimistic.mjs';
import { deviceHistoryDirection } from '../static/devicehistory.mjs';
import { readFile } from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import {collectiveRef,parseCollective} from '../skins/shared/collective-mentions.mjs';
const {threadAgentID, threadAuthor, agentName, participants} = model;

const host = 'owner/laptop', agent = 'chosen-agent';
const request = {id:'request',dir:'out',from:host,to:host,kind:'question',target:{address:host,agent_id:agent}};
const answer = {id:'answer',dir:'out',from:host,to:host,kind:'answer',agent_id:agent,reply_to:request.id};
const o = {me:{address:host,agent:true},person:{person:'owner',address:host}};
assert.equal(threadAgentID({peer:host,messages:[request,answer]}), agent);
assert.equal(threadAgentID({peer:host,messages:[answer]}), agent, 'paged answer retains named executor');
assert.equal(threadAgentID({peer:'other/device',messages:[request,answer]}), undefined, 'another host cannot supply a target');
assert.equal(threadAgentID({peer:host,messages:[{...request,target:undefined}]}), undefined);
assert.equal(threadAgentID({peer:host,messages:[request,answer,{...request,id:'default',target:undefined}]}), undefined, 'latest default request does not switch back to an older named executor');
assert.equal(threadAgentID({peer:host,messages:[request,answer,{...request,id:'pending',target:undefined,_local:true}]}), agent, 'pending preview cannot change the verified executor');
const who = threadAuthor(answer,o,{[agent]:'Selected agent'},host,[request,answer]);
assert.equal(who.name,'Selected agent');
assert.equal(who.agent,true);
assert.equal(who.mine,false,'own agent answer appears opposite the human request');
assert.equal(threadAuthor(request,o,{},host,[request,answer]).mine,true);
// A retained self-question must reconcile its outgoing preview before an
// answer arrives, preserving both human authorship and the next agent target.
const pending = pendingSends({});
const files = [{name:'pasted-picture.png',size:391379,openable:true}];
pending.begin('self-thread',{...request,files});
pending.finish(request.id,{id:request.id,state:'queued'});
const retained = {...request,files,history:true,device_history:{peer:host,recipient:host,direction:'in'}};
const actual = {...retained,dir:deviceHistoryDirection(retained,'in'),state:'delivered'};
const merged = pending.merge('self-thread',[actual]);
assert.equal(merged.length,1,'stored self-question clears the Sending preview');
assert.equal(pending.has(request.id),false);
assert.equal(merged[0].files[0].openable,true,'actual attachment remains accessible');
assert.equal(threadAuthor(merged[0],o,{},host,merged).mine,true,'self-question stays a human message');
assert.equal(threadAgentID({peer:host,messages:merged}),agent,'follow-up before first answer keeps selected Codex');
pending.begin('self-thread',{...request,id:'another'});
assert.equal(pending.merge('self-thread',[{...request,id:'another',dir:'in'}]).length,2,'an incoming row alone cannot acknowledge a send');
assert.equal(threadAuthor(answer,o,{},host,[{...request,state:'manual'},answer]).mine,true,'manual answer remains human');
const remote = {...o.person,address:'owner/bezos',fingerprint:'owner-key',label:'Sergey'};
const roomThread = {kind:'group',messages:[],members:[o.person],agents:[{pid:'remote',host:remote,inviter:o.person,state:'active',member:true,host_here:false},{pid:'local',host:o.person,inviter:o.person,state:'active',member:true,host_here:true}]};
assert.equal(agentName(undefined,{},remote,o.person),'Your earlier default on Bezos','default remote agent keeps its exact host visible');
assert.equal(agentName(agent,{[agent]:'Codex'},remote,o.person),'Your Codex','named agent retains its executor and verified owner');
assert.equal(agentName(agent,{[agent]:'Codex'},{...remote,person:'another-owner',label:'Dana'},o.person),'Dana’s Codex','another person’s same-named executor stays distinguishable');
assert.equal(agentName(agent,{[agent]:'Codex'},undefined,o.person),'Codex','missing host cannot invent an owner');
assert.equal(participants(roomThread,o,{}).find(p=>p.pid==='remote').name,'Your earlier default on Bezos','mention/header identity agrees');
const roomSource=stripTypeScriptTypes((await readFile(new URL('../web/src/features/RoomPanel.model.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const room=runInNewContext(roomSource+'\nroom',{...model,eventKind:()=>null});
const views=room(roomThread,o,{}).guests;
assert.equal(views.find(g=>g.pid==='remote').where,'Bezos','same human is not the same host');
assert.equal(views.find(g=>g.pid==='local').where,'this computer');
const oldDefault=threadAuthor({...answer,agent_id:undefined}, {...o,me:{...o.me,responder:'pi'}}, {},host,[{...request,target:undefined,responder:'claude'},answer]);assert.equal(oldDefault.name,'Claude','old admitted default output retains captured harness after default changes');
const twinA='a'.repeat(32),twinB='b'.repeat(32),twinNames={[twinA]:'Twin',[twinA+'#harness']:'pi',[twinB]:'Twin',[twinB+'#harness']:'pi'};
const topic=(id,agent)=>({id,peer:host,agent_id:agent,agent_host:host,agent_key:'aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa',title:'Retained topic',last:'Done',last_at:'2026-10-20T10:00:00Z',count:1,review:0,unread:0,running:0,state:'archived',pending:false});
const ta=topic('1'.repeat(32),twinA),tb=topic('2'.repeat(32),twinB),rows=model.chatList({...o,dms:[],threads:[],topics:[{peer:host,total:2,archived:2,archived_unread:0,latest:ta},{peer:host,total:3,archived:3,archived_unread:0,latest:tb}]},twinNames);
assert.equal(rows.length,2,'same host and same name retain distinct agent rows even archived-only');assert.deepEqual(rows.map(r=>r.topicTotal).sort(),[2,3]);assert.notEqual(rows[0].title,rows[1].title,'same-name instances show stable disambiguators');
assert.equal(threadAgentID({peer:host,topic:ta,messages:[request,{...request,target:{address:host,agent_id:twinB}}]}),twinA,'immutable original binding wins over latest moved/display row');
const historical=threadAuthor({...answer,agent_id:twinA},o,{...twinNames,[twinA+'#harness']:'claude'},host,[request,answer]);assert.match(historical.name,/Twin/);assert(!historical.name.includes('Claude'),'current harness never relabels historical output');
console.log('Direct agent follow-up identity and self answer authorship PASS');

const inviteRights=runInNewContext(roomSource+'\n({canBringIn,shareable,isMine,online,speaker})',{...model,eventKind:()=>null});
const topicGuest={...roomThread,role:'human_guest',guests:[{pid:'guest',topic:'warehouse',state:'active',can_send:true,host_here:true,held:0,host:o.person}],messages:[{id:'selected',lid:'original',topic:'warehouse',kind:'message',body:'Already selected',excerpt_pid:'guest',replica:true},{id:'other',topic:'other',kind:'message',body:'Outside topic'}]};
assert.equal(inviteRights.canBringIn(topicGuest,'warehouse'),true,'accepted exact topic guest can bring own agent');
assert.equal(inviteRights.canBringIn(topicGuest,''),false,'guest cannot broaden invite to Main');
assert.equal(inviteRights.canBringIn({...topicGuest,frozen:'conflict'},'warehouse'),false,'frozen guest has no invite action');
assert.equal(inviteRights.canBringIn({...topicGuest,guests:[{...topicGuest.guests[0],state:'dismissed',can_send:false}]},'warehouse'),false,'ended guest has no invite action');
assert.deepEqual(Array.from(inviteRights.shareable(topicGuest).map(m=>m.id)),['selected'],'guest may choose its admitted selected excerpt only within its accepted topic');
const inviteSource=stripTypeScriptTypes((await readFile(new URL('../web/src/features/InviteSheet.candidates.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const candidateModel=runInNewContext(inviteSource+'\n({catalogHosts,pool})',{...model,...inviteRights,collectiveRef,parseCollective});
const guestOverview={...o,agents:true,person:{...o.person,devices:[{address:host},{address:'owner/second-laptop'}]},people:[{person:'outside',address:'outside/laptop',state:'pinned',devices:[{address:'outside/laptop'}]}]};
const hostList=candidateModel.catalogHosts(topicGuest,guestOverview);
assert(!hostList.includes('outside/laptop'),'guest catalog lookup stays within own devices');
const candidates=candidateModel.pool(topicGuest,guestOverview,{loading:false,local:[{record:{id:agent,host,label:"Connected Pi",harness:"pi"},enabled:true,responder:{ready:true}}],remote:{}},{},[],'daemon','guest','warehouse').candidates;
assert(candidates.length>0&&candidates.every(c=>c.kind==='agent'&&c.mine),'guest invite offers only its own agents');
assert.equal(candidateModel.pool(topicGuest,guestOverview,{loading:false,local:[],remote:{}},{},[],'daemon','guest','warehouse').candidates.length,0,'unconfigured default never fabricates a candidate');
console.log('Guest invite rights, admitted context and own-agent candidate sources PASS');

const mentionsSource=stripTypeScriptTypes((await readFile(new URL('../web/src/features/Composer.mentions.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const mentionView=runInNewContext(mentionsSource+'\n({candidates,guestAuthor})',{...model});
const scoped=(pid,topic)=>({pid,topic,host:remote,agent_id:agent,inviter:o.person,state:'active',can_ask:true});
const scopedRoom={...roomThread,agents:[scoped('broad',undefined),scoped('main',''),scoped('warehouse','warehouse'),scoped('other','other')]};
const rejoin=runInNewContext(roomSource+'\nagentRejoinState',{...model,eventKind:()=>null});
const oldScope={...scoped('ended','warehouse'),state:'dismissed'};
assert.equal(rejoin({...scopedRoom,agents:[oldScope,scoped('other','other'),scoped('broad',undefined)]},'ended'),'','Another topic or broad invitation cannot replace the old exact scope');
assert.equal(rejoin({...scopedRoom,agents:[oldScope,scoped('replacement','warehouse')]},'ended'),'Already in this chat');
assert.equal(rejoin({...scopedRoom,agents:[oldScope,{...scoped('replacement','warehouse'),state:'invited'}]},'ended'),'Rejoin pending');
for (const state of ['active','invited']) {
 const current={...scoped('current','warehouse'),state,pids:['current','retained-alias']};
 assert.equal(rejoin({...scopedRoom,agents:[current]},'retained-alias'),state==='active'?'Already in this chat':'Rejoin pending','A current aggregated agent card owns its retained PID aliases');
}
for(const [topic,want] of [['warehouse','warehouse'],['','main'],['new-topic','broad']]){
 assert.deepEqual(Array.from(mentionView.candidates(scopedRoom,o,{[agent]:'Codex'},topic).filter(c=>c.kind==='agent').map(c=>c.id)),[want],'one exact eligible executor, narrow topic before whole-chat');
}
assert.equal(mentionView.guestAuthor({role:'human_guest',guests:[{pid:'wrong',host_here:true,can_send:true,topic:'other'},{pid:'right',host_here:true,can_send:true,topic:'warehouse'}]},'warehouse').pid,'right');
console.log('Topic-bound composer targets and guest author PASS');
