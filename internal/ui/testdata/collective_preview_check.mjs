// Real Engine device-summary and Comic chat-list projections, no network/build.
// Native test passes its actual edited DM/group Overview on stdin.
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
import {collectivePlain} from '../skins/shared/collective-mentions.mjs';
import model from './comic_model.mjs';

const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch:async()=>{throw Error('no network');}});
e.address='self/phone';e.fp='11111111-22222222-33333333-44444444';
const cases=JSON.parse(readFileSync(new URL('../../client/testdata/body_preview_vectors.json',import.meta.url),'utf8'));
const clip=s=>[...s].length>120?[...s].slice(0,119).join('')+'…':s;
for(const count of [1000,4096,4097]){
  const refs=Array.from({length:count},(_,i)=>'p.'+i.toString(16).padStart(64,'0')).join(',');
  const body='[@Amazon_team](agentnet:collective/v1/'+refs+') Check inventory';
  cases.push({name:count+' refs',body,want:count<=4096?'@Amazon_team Check inventory':clip(body)});
}
const oversized='[@team](agentnet:collective/v1/'+'a'.repeat(280001)+')';
cases.push({name:'payload bound',body:oversized,want:clip(oversized)});
const summary=body=>e.topicSummary([{id:'a'.repeat(32),peer:'peer/desk',dir:'in',kind:'message',body,at:1000}],new Map(),2,null);
for(const c of cases){
  const before=c.body,t=summary(c.body);
  assert.equal(t.last,c.want,c.name+' Engine device preview');assert.equal(t.title,c.want,c.name+' automatic title');
  assert.equal(c.body,before,c.name+' signed bytes');
  // Existing grammar remains the independent source for valid full anchors.
  const intact=collectivePlain(c.body).replace(model.mentionRef,(_m,name)=>'@'+name).split('\n')[0];
  assert.equal(t.last,clip(intact),c.name+' shared presentation grammar parity');
  const o={dms:[{id:'c'.repeat(64),peer:{person:'person',label:'Peer'},count:1,last:t.last,last_at:'2026-10-16T00:00:00Z',unread:0,held:0}],threads:[],review:[],needs_you:[]};
  assert.equal(model.chatList(o,{})[0].last,model.firstLine(c.want),c.name+' actual Comic chat-list output');
}
const literal='[@literal](agentnet:person/name)',row={id:'b'.repeat(32),peer:'peer/desk',dir:'out',kind:'message',body:'plain',at:1000};
assert.equal(e.topicSummary([row],new Map([['peer/desk/'+row.id,{title:literal}]]),2,null).title,literal,'renamed topic stays literal');
assert.equal(model.firstLine(literal,120,true),literal,'literal clipping stays inert');
// Real direct thread/overview share the existing controls authority, including
// wrong author/key/ref/peer negatives; original signed text is never replaced.
const directID='5'.repeat(32),author='aaaaaaaa-bbbbbbbb-cccccccc-dddddddd',original='Original direct body';
const editedBody='[@Edited_team](agentnet:collective/v1/'+Array.from({length:1000},(_,i)=>'p.'+i.toString(16).padStart(32,'0')).join(',')+') Check inventory';
const control=(from,fp,ref,sub,payload)=>({id:wire.newID(),v:3,control:true,from,fp,kind:'message',sub,ref,body:JSON.stringify(payload),at:2});
const add=async c=>e.store.write([{s:'inbox',k:c.id,v:c}]);
await e.store.write([{s:'inbox',k:directID,v:{id:directID,v:1,from:'peer/desk',fp:author,kind:'message',body:original,at:1}}]);
await add(control('peer/desk',author,{id:directID,fingerprint:author},wire.SubRevision,{rev:1,text:editedBody}));
const exact=async()=>{
  const thread=await e.thread(directID),m=thread.messages.find(m=>m.id===directID);
  assert.equal(m.body,original);assert.equal(m.edited,true);assert.equal(m.text,editedBody);
  assert.equal(thread.topic.last,'@Edited_team Check inventory','actual resolved thread topic');
  const overview=await e.overview();assert.equal(overview.threads.find(t=>t.id===directID).last,thread.topic.last,'actual Engine overview shares resolved display');
  assert.equal(model.chatList(overview,{})[0].last,thread.topic.last,'actual direct Engine overview to Comic');
};
await exact();
for(const c of [
  control('peer/desk',e.fp,{id:directID,fingerprint:author},wire.SubRevision,{rev:99,text:'wrong author'}),
  control('peer/desk','99999999-99999999-99999999-99999999',{id:directID,fingerprint:author},wire.SubRevision,{rev:99,text:'changed key'}),
  control('other/desk',author,{id:directID,fingerprint:author},wire.SubRevision,{rev:99,text:'other peer'}),
  control('peer/desk',author,{id:wire.newID(),fingerprint:author},wire.SubRevision,{rev:99,text:'other ref'}),
]){await add(c);await exact();}
await add(control('peer/desk',author,{id:directID,fingerprint:author},wire.SubRetraction,{}));
assert.equal((await e.thread(directID)).topic.last,'Message deleted','retraction wins over any prior edited preview');
// Execute overview's real DM/group preview branches over an already presented
// synthetic read. These stubs supply admission outputs, not admission proofs.
const own='1'.repeat(32),peer='2'.repeat(32),roster='a'.repeat(64),conv='c'.repeat(64),group='d'.repeat(64),realm='3'.repeat(32);
e.me={person:own,label:'Owner',devices:[]};
const creator={person:own,roster,address:e.address,fingerprint:e.fp},members=[{person:own,roster},{person:peer,roster}];
const dmRoot={v:2,kind:'dm',creator,members,nonce:'4'.repeat(32),created:1};
const groupRoot={...dmRoot,v:wire.GroupRootVersion,kind:'group',realm,title:literal,admins:[own]};
const state={v:1,conv:group,realm,seq:0,prev:'',title:literal,actor:own,actor_roster:roster,by:e.fp,
  members:members.map(m=>({...m,admin:m.person===own,admission:{conv:group,realm,person:m.person,roster,seq:0,prev:'',by:e.fp}}))};
const read={persons:[{person:peer,label:'Peer',devices:[]}],inbox:[],outbox:[],convs:[{id:conv,peer,root:JSON.stringify(dmRoot),created:1,creator:e.address}],groups:[{root:JSON.stringify(groupRoot),context:JSON.stringify({root:groupRoot,state}),records:[]}]};
let body=cases.find(c=>c.name==='1000 refs').body,edited=false,files=false;
const message=id=>({id,lid:id,kind:'message',body:files?'':body,at:1000,...(files?{attachments:[{name:literal}]}:{})});
Object.assign(e,{
  readView:async()=>read,personView:p=>p,peerWordsFn:async()=>a=>a,ownNeedsYouReports:async()=>new Map(),participationsOf:async()=>[],
  oneRowPerRecord:async m=>m,needsYouOf(){},erasedConv:()=>false,lastEvent:()=>undefined,
  convMessages:async id=>[message(id)],dm:async id=>({messages:[{...message(id),edited,...(edited?{text:body}:{})}]}),
  groupThread:async id=>({role:'member',members:[],messages:[{...message(id),edited,...(edited?{text:body}:{})}]}),
  topicOverview:async()=>({threads:[],topics:[]}),historyBook:async()=>({}),linkRequests:async()=>[],modelReports:async()=>[],receiveStatus:async()=>({}),
  activeReportItems:async()=>[],deviceAdminReview:async()=>[],notifyView:async()=>({}),groupInvitations:async()=>[],
});
for(edited of [false,true]){
  if(edited)body=body.replace('Amazon_team','Команда');
  const overview=await e.overview(),want=edited?'@Команда Check inventory':'@Amazon_team Check inventory';
  for(const d of overview.dms)assert.equal(d.last,want,'actual Engine overview DM/group '+(edited?'edited':'original'));
  for(const item of model.chatList(overview,{}))assert.equal(item.last,want,'actual Engine overview to Comic chat list');
  assert.equal(overview.dms.find(d=>d.kind==='group').title,literal,'group name is literal');
}
edited=false;files=true;
assert.equal((await e.overview()).dms.find(d=>d.id===conv).last,'📎 '+wire.safeName(literal),'file name branch keeps existing safe-name handling');
const raw=readFileSync(0,'utf8');
if(raw.trim()){
  const native=JSON.parse(raw),items=model.chatList(native.overview,{});
  for(const id of native.conversations){const item=items.find(i=>i.open.id===id);assert.ok(item,'native overview conversation reaches chat list');assert.equal(item.last,'@Команда Check inventory','actual edited native DM/group chat-list text');}
  if(native.direct)assert.equal(items.find(i=>i.open.id===native.direct)?.last,'@Direct_team Check inventory','actual edited native direct overview to Comic');
}
e.stop();
console.log('PASS shared Go/Engine vectors, 1000/4096 refs and bounds, Unicode/ordinary/malformed controls, literal title, actual Comic chat-list/native overview output');
