import assert from 'node:assert/strict';
import * as model from '../web/src/model.ts';
import { readFile } from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
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
assert.equal(threadAuthor(answer,o,{},host,[{...request,state:'manual'},answer]).mine,true,'manual answer remains human');
const remote = {...o.person,address:'owner/bezos',fingerprint:'owner-key',label:'Sergey'};
const roomThread = {kind:'group',messages:[],members:[o.person],agents:[{pid:'remote',host:remote,inviter:o.person,state:'active',member:true,host_here:false},{pid:'local',host:o.person,inviter:o.person,state:'active',member:true,host_here:true}]};
assert.equal(agentName(undefined,{},remote,o.person),'Your agent on Bezos','default remote agent keeps its exact host visible');
assert.equal(agentName(agent,{[agent]:'Codex'},remote,o.person),'Codex','known agent name stays its actual name');
assert.equal(participants(roomThread,o,{}).find(p=>p.pid==='remote').name,'Your agent on Bezos','mention/header identity agrees');
const roomSource=stripTypeScriptTypes((await readFile(new URL('../web/src/features/RoomPanel.model.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const room=runInNewContext(roomSource+'\nroom',{...model,eventKind:()=>null});
const views=room(roomThread,o,{}).guests;
assert.equal(views.find(g=>g.pid==='remote').where,'Bezos','same human is not the same host');
assert.equal(views.find(g=>g.pid==='local').where,'this computer');
console.log('Direct agent follow-up identity and self answer authorship PASS');

const mentionsSource=stripTypeScriptTypes((await readFile(new URL('../web/src/features/Composer.mentions.ts',import.meta.url),'utf8')).replace(/^import .*;\n/gm,'')).replace(/^export /gm,'');
const mentionView=runInNewContext(mentionsSource+'\n({candidates,guestAuthor})',{...model});
const scoped=(pid,topic)=>({pid,topic,host:remote,agent_id:agent,inviter:o.person,state:'active',can_ask:true});
const scopedRoom={...roomThread,agents:[scoped('broad',undefined),scoped('main',''),scoped('warehouse','warehouse'),scoped('other','other')]};
const rejoin=runInNewContext(roomSource+'\nagentRejoinState',{...model,eventKind:()=>null});
const oldScope={...scoped('ended','warehouse'),state:'dismissed'};
assert.equal(rejoin({...scopedRoom,agents:[oldScope,scoped('other','other'),scoped('broad',undefined)]},'ended'),'','Another topic or broad invitation cannot replace the old exact scope');
assert.equal(rejoin({...scopedRoom,agents:[oldScope,scoped('replacement','warehouse')]},'ended'),'Already in this chat');
assert.equal(rejoin({...scopedRoom,agents:[oldScope,{...scoped('replacement','warehouse'),state:'invited'}]},'ended'),'Rejoin pending');
for(const [topic,want] of [['warehouse','warehouse'],['','main'],['new-topic','broad']]){
 assert.deepEqual(Array.from(mentionView.candidates(scopedRoom,o,{[agent]:'Codex'},topic).filter(c=>c.kind==='agent').map(c=>c.id)),[want],'one exact eligible executor, narrow topic before whole-chat');
}
assert.equal(mentionView.guestAuthor({role:'human_guest',guests:[{pid:'wrong',host_here:true,can_send:true,topic:'other'},{pid:'right',host_here:true,can_send:true,topic:'warehouse'}]},'warehouse').pid,'right');
console.log('Topic-bound composer targets and guest author PASS');
