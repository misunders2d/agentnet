import fs from 'node:fs';
import assert from 'node:assert/strict';
// Run the real group card projection with already resolved participations.
// Admission, grants and request routing remain covered by the native journey.
const source=fs.readFileSync(process.env.AGENTNET_REJOIN_ENGINE || new URL('../static/engine.mjs',import.meta.url),'utf8');
const start=source.indexOf('    const agentCards=new Map();');
const end=source.indexOf('    for(const card of agentCards.values())',start);
assert(start>=0&&end>start);
const project=new Function('infos','messages','members','role',source.slice(start,end)+'return [...agentCards.values()];');
const host={address:'peer/desk',fingerprint:'keyA'};
const old={pid:'old',pids:['old'],state:'dismissed',host,agent_id:'codex',shared:['oldGrant'],tasks_from:[],inviter:{person:'me'},invited:'1'};
const current={...old,pid:'current',pids:['current'],state:'invited',shared:['newGrant'],invited:'2'};
for(const state of ['invited','active'])for(const reverse of [false,true]) {
 const next={...current,state};
 const cards=project.call({agentView:i=>({...i})},reverse?[next,old]:[old,next],[],[],'member');
 assert.equal(cards.length,2,'Past card must not replace current rejoin or disappear');
 assert.deepEqual(cards.find(c=>c.pid==='old').shared,['oldGrant'],'Old grants remain separate');
 assert.deepEqual(cards.find(c=>c.pid==='current').shared,['newGrant'],'New grants remain separate');
}
console.log('PASS rejoin projection: pending/active, both arrival orders, separate history grants');
// The legacy skins retain the ended PID's card and explain its current counterpart.
for(const skin of ['classic','zoom']) {
 const src=fs.readFileSync(new URL(`../skins/${skin}/src/entry.mjs`,import.meta.url),'utf8');
 const from=src.indexOf('function agentCard(a, t) {'),to=src.indexOf('\n// choice',from);
 const reportFrom=src.indexOf('function ownModelReport('),reportTo=src.indexOf('function ownModelReports(',reportFrom);
 const devices=src.match(/function devicesOf\(p\) \{[\s\S]*?\n\}/)?.[0];
 assert(from>=0&&to>from&&reportFrom>=0&&reportTo>reportFrom&&devices);
 const state={overview:{person:{devices:[{address:'me/desk'}]},model_reports:[]}};
 const card=new Function('el','isMe','agentName','plural','state',devices+'\n'+src.slice(reportFrom,reportTo)+src.slice(from,to)+'; return agentCard;')(
  (...parts)=>parts,()=>false,a=>a.agent_id,(n,s)=>`${n} ${s}`,state);
 const ended={...old,missing:0,inviter:{label:'You'},state_text:'Dismissed'};
 for(const state of ['invited','active']) {
  const rendered=JSON.stringify(card(ended,{agents:[ended,{...current,state}]}));
  assert(rendered.includes('Dismissed'),'Historical lifecycle disappeared');
  assert(rendered.includes(state==='active'?'Already in this chat':'Rejoin pending'),`${skin} hides current rejoin state`);
  assert(!rendered.includes('data-agent-model'),`${skin} reports a foreign agent as own`);
 }
 const other=JSON.stringify(card(ended,{agents:[ended,{...current,agent_id:'claude'}]}));
 assert(!other.includes('Rejoin pending'),'Different named agents coalesced');
 state.overview.person.devices.push({address:host.address});
 assert(JSON.stringify(card(ended,{agents:[ended,current]})).includes('Model unknown'),`${skin} omits own unknown model`);
 state.overview.model_reports.push({host:host.address,agent_id:ended.agent_id,model:'reported fixture model',at:1790000000});
 assert(JSON.stringify(card(ended,{agents:[ended,current]})).includes('Last reported model: reported fixture model'),`${skin} omits actual own model helper`);
 assert(!JSON.stringify(card({...ended,agent_id:'claude'},{agents:[]})).includes('reported fixture model'),`${skin} borrows another named agent model`);
}
console.log('PASS Classic/Zoom past cards: exact rejoin explanation, historical lifecycle retained');
