// Actual Engine provider over admitted synthetic chat projections plus real device rows.
import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
const own='1'.repeat(32),peer='2'.repeat(32),roster='a'.repeat(64),conv='c'.repeat(64),group='d'.repeat(64),realm='3'.repeat(32),fp='11111111-22222222-33333333-44444444';
const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch:async()=>{throw Error('no network');}});e.address='self/phone';e.fp=fp;e.me={person:own,label:'Owner',devices:[]};
const creator={person:own,roster,address:e.address,fingerprint:fp},members=[{person:own,roster},{person:peer,roster}];
const root={v:2,kind:'dm',creator,members,nonce:'4'.repeat(32),created:1};
const groupRoot={...root,v:wire.GroupRootVersion,kind:'group',realm,title:'Muted',admins:[own]};
const state={v:1,conv:group,realm,seq:0,prev:'',title:'Muted',actor:own,actor_roster:roster,by:fp,members:members.map(m=>({...m,admin:m.person===own,admission:{conv:group,realm,person:m.person,roster,seq:0,prev:'',by:fp}}))};
const rows=new Map([[conv,[]],[group,[]]]);let serial=0,mutes=[group],reads=0,loads=0;
const add=(scope,topic='',fields={})=>{const id=(++serial).toString(16).padStart(32,'0'),m={id,lid:id,kind:'message',body:'[@Team](agentnet:person/team) Review',topic,at:1000,fp,from:'peer/desk',dir:'out',unread:true,...fields};rows.get(scope).push(m);return m;};
add(conv);add(conv);add(conv,'a'.repeat(32));for(let i=0;i<3;i++)add(conv,'b'.repeat(32));for(let i=0;i<4;i++)add(group);
// The guest/selected projection omits this raw row. It must never become Review.
add(group,'',{id:'f'.repeat(32),hidden:true});
const read=()=>({attention:new Map(),persons:[{person:peer,label:'Peer',devices:[]}],inbox:[],outbox:[],convs:[{id:conv,peer,root:JSON.stringify(root),created:1,creator:e.address}],groups:[{root:JSON.stringify(groupRoot),context:JSON.stringify({root:groupRoot,state}),records:[]}]});
const presented=scope=>rows.get(scope).filter(m=>!m.hidden).map(m=>({...m,at:new Date(m.at).toISOString()}));
Object.assign(e,{readView:async()=>{reads++;return read();},personView:p=>p,peerWordsFn:async()=>a=>a,ownNeedsYouReports:async()=>new Map(),participationsOf:async()=>[],oneRowPerRecord:async m=>m,needsYouOf(){},erasedConv:()=>false,lastEvent:()=>undefined,
 convMessages:async id=>{loads++;return rows.get(id);},dm:async id=>({messages:presented(id)}),groupThread:async id=>({role:'human_guest',members:[],messages:presented(id)}),historyBook:async()=>({}),linkRequests:async()=>[],modelReports:async()=>[],receiveStatus:async()=>({}),activeReportItems:async()=>[],deviceAdminReview:async()=>[],notifyView:async()=>({enabled:false,mutes}),groupInvitations:async()=>[]});
for(const archived of [true,false]){const o=await e.overview(archived);assert.equal(o.unread_total,10,'all scopes exactly once');assert.equal(o.alert_total,6,'only local conversation mutes affect alerts');assert(!('unread_rows' in o),'overview remains scalar');}
const originalKey=e.unreadIndex.rows.find(r=>r.id===rows.get(conv)[0].id).key;rows.get(conv)[0].id='e'.repeat(32);e.changed();await e.overview(false);assert.equal(e.unreadIndex.rows.find(r=>r.id==='e'.repeat(32)).key,originalKey,'selected carrier changes retain logical identity');
const shared=e.unreadIndex;await e.overview(true);await e.overview(false);assert.equal(e.unreadIndex,shared,'both overview modes reuse unchanged compact index');
let p=await e.api('/api/unread?limit=2');assert.equal(p.total,10);assert.equal(p.rows.length,2);assert(p.next);assert(p.rows.every(r=>r.preview==='@Team Review'&&r.dir==='out'));assert(!p.rows.some(r=>r.id==='f'.repeat(32)));
const index=e.unreadIndex,counts=[reads,loads];await e.api('/api/unread?limit=2');assert.equal(e.unreadIndex,index);assert.deepEqual([reads,loads],counts,'Review reuses compact index without history scan');
p.rows[0].key='mutated';assert.notEqual((await e.api('/api/unread?limit=2')).rows[0].key,'mutated','page copy cannot corrupt index');
mutes=[];e.changed();assert.equal((await e.overview(false)).alert_total,10,'mute revision invalidates');
// >200 author-qualified references, including a logical-ID collision.
for(let i=0;i<205;i++)add(conv);const sameLID=rows.get(conv)[0].lid;add(conv,'',{lid:sameLID,fp:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd',dir:'in'});e.changed();
const first=await e.api('/api/unread?limit=200');assert.equal(first.total,216);assert.equal(first.rows.length,200);assert.equal(new Set(first.rows.map(r=>r.key)).size,200);
const second=(await e.api('/api/unread?after='+encodeURIComponent(first.next)+'&limit=200'));const expected=second.rows.map(r=>r.key);assert.equal(expected.length,16);
const collisions=[...first.rows,...second.rows].filter(r=>r.lid===sameLID);assert.equal(collisions.length,2);assert.equal(new Set(collisions.map(r=>r.author)).size,2);assert.equal(new Set(collisions.map(r=>r.key)).size,2,'same LID retains distinct original-author keys');
for(const r of first.rows)rows.get(r.conv).find(m=>m.id===r.id).unread=false;e.changed();
const remaining=await e.api('/api/unread?after='+encodeURIComponent(first.next)+'&limit=200');assert.deepEqual(remaining.rows.map(r=>r.key),expected,'cursor survives first-page reads');assert.equal(remaining.total,16);
for(const query of ['limit=-1','limit=201','limit=no','limit=%2B1','after=bogus','after='+btoa(JSON.stringify({v:1,key:'row'})).replace(/=+$/,'')])await assert.rejects(()=>e.api('/api/unread?'+query));
// Actual provider order and cursor retain milliseconds inside a same-second burst.
{
 const stamp=1792700000000;
 e.unreadIndex={seq:e.seq,sorted:false,rows:[['a',10],['b',90],['c',50]].map(([key,ms])=>({key,at:new Date(stamp+ms).toISOString()}))};
 const first=await e.api('/api/unread?limit=2');assert.deepEqual(first.rows.map(r=>r.key),['b','c'],'same-second arrivals must keep their real subsecond order');
 const next=await e.api('/api/unread?limit=2&after='+encodeURIComponent(first.next));assert.deepEqual(next.rows.map(r=>r.key),['a']);
 e.changed();
}
// Overlapping first builds deduplicate without retaining a full overview.
e.changed();const before=reads;await Promise.all([e.api('/api/unread'),e.api('/api/unread')]);assert.equal(reads,before+1);assert.equal(e.unreadBuild,null);assert(!('readView' in e.unreadIndex));
// A write during construction must remain labelled with the captured older seq.
e.changed();const captured=e.seq,oldRead=e.readView;e.readView=async()=>{const v=await oldRead();e.changed();return v;};const concurrent=await e.overview(false);assert.equal(concurrent.seq,captured);assert.equal(e.unreadIndex.seq,captured);e.readView=oldRead;
// Real device projection counts archived rows once in either overview mode.
const d=new Engine({store:memoryStore(),base:'https://synthetic.invalid',fetch:async()=>{throw Error('no network');}});d.address=e.address;d.fp=fp;
const id='9'.repeat(32);await d.store.write([{s:'inbox',k:id,v:{id,v:1,from:'peer/desk',fp,kind:'message',body:'device body',at:1}}]);
for(const archived of [true,false]){const o=await d.overview(archived);assert.equal(o.unread_total,1);assert.equal(o.alert_total,1);const p=await d.api('/api/unread');assert.equal(p.rows[0].kind,'device');assert.equal(p.rows[0].id,id);assert.equal(p.rows[0].preview,'device body');assert.equal(p.rows[0].topic_title,'device body');}
await d.changeTopic('rename',{peer:'peer/desk',id,title:'Private device @literal'});assert.equal((await d.api('/api/unread')).rows[0].topic_title,'Private device @literal','archived device title follows existing summary');
// Paging is over the complete filtered set, with bucket/time/id/peer ties.
const topic=(id,peer,at,unread,state='active')=>({id,peer,conv,last_at:new Date(at*1000).toISOString(),unread,state,title:'searchable',last:'last'});
let topics=[topic('z','a',300,0),topic('b','b',100,1),topic('a','a',100,1),topic('b','a',100,1),topic('done','a',400,0,'done')];
e.chatTopics=async()=>topics;
const tp=await e.api('/api/topics?conv='+conv+'&limit=2&q=%20searchable%20');assert.equal(tp.matched,5);assert.deepEqual(tp.topics.map(t=>[t.id,t.peer]),[['b','a'],['b','b']]);
topics[1].unread=0;topics[1].last_at=new Date(1000000).toISOString();
let later=await e.api('/api/topics?conv='+conv+'&before='+encodeURIComponent(tp.next));assert.equal(later.topics[0].id,'a');assert.equal(later.topics.length,4);
topics=topics.filter(t=>!(t.id==='b'&&t.peer==='b'));
later=await e.api('/api/topics?conv='+conv+'&before='+encodeURIComponent(tp.next));assert.equal(later.topics[0].id,'a');assert.equal(later.topics.length,3,'deleted cursor still compares');
assert.equal((await e.api('/api/topics?conv='+conv+'&state=done')).matched,1);
for(const query of ['conv=foreign&before='+encodeURIComponent(tp.next),'conv='+conv+'&limit=wat','conv='+conv+'&limit=-1','conv='+conv+'&before=20261016000000%7C'+conv+'%7Cb'])await assert.rejects(()=>e.api('/api/topics?'+query));
// Same comparator/cursor through the device route; use admission summaries here.
e.threadSummaries=async()=>topics.map(({conv,...t})=>t);
const dp=await e.api('/api/topics?limit=1');assert.equal(dp.topics[0].unread,1);const dc=dp.next;
topics=topics.filter(t=>t.id!==dp.topics[0].id);
assert((await e.api('/api/topics?before='+encodeURIComponent(dc))).matched>=1);
await assert.rejects(()=>e.api('/api/topics?peer=foreign/device&before='+encodeURIComponent(dc)));
e.stop();d.stop();console.log('PASS unread provider totals, mutes, >200, authors, guest projection, cursors, invalidation, reuse and captured seq');
