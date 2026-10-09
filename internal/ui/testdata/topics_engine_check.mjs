// Browser parity for topics (client topics.go, ui livetopics.go): the
// engine's tunables are the Go client's, its derivation gives exactly the
// shared vectors' answers (the Go client is held to the same vectors), and
// its routes serve the overview, the paged All topics list and the person's
// changes as the daemon does. Real Engine over a memory store, no network.
// stdin: {vectors, constants} from topics_browser_test.go.
import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
import { Engine, memoryStore, deriveTopic, TOPICS, chatTopicAssignments, summarizeChatTopics } from '../static/engine.mjs';

let checks = 0;
const check = (v, why) => { assert.ok(v, why); checks++; };
const same = (a, b, why) => { assert.deepEqual(a, b, why); checks++; };
const refuses = async (fn, re, why) => { await assert.rejects(fn, re, why); checks++; };
const { vectors, constants } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));

// 1. The tunables are the Go client's.
same({ ...TOPICS }, constants, 'engine TOPICS equal the Go client constants');
check(vectors.archive_after === TOPICS.archiveAfter, 'the vectors assume the same archive time');

// 2. The shared derivation vectors.
for (const c of vectors.cases) {
  const facts = c.messages.map((m) => ({ id: m.id, reply_to: m.reply_to || '', at: m.at, in: m.dir === 'in', kind: m.kind, state: m.state || '',
    status: m.status || '', topic_done:!!m.topic_done, notice: !!m.notice, selected: !!m.selected }));
  const got = deriveTopic(facts, c.local || null, vectors.now);
  same(got, Object.fromEntries(Object.entries(c.want).filter(([, v]) => v !== '' && v !== undefined)), c.name);
}

// Optional DM/group topics use the same fixture data as Go.
for(const c of vectors.chat_cases){
 const msgs=c.messages.map(m=>({...m,ts:m.sent,to:m.dir==='out'?'bob/desk':undefined,at:m.sent*1000}));
 same(Object.fromEntries(chatTopicAssignments(msgs)),c.assigned,c.name+' assignment');
 const locals=new Map(Object.entries(c.local).map(([id,l])=>['chat/'+id,l]));
 const got=summarizeChatTopics('chat',msgs,locals,vectors.now).map((t,i)=>({ID:t.id,State:t.state,Count:t.count,DoneBy:t.done_by||'',Pending:t.pending,...(c.want[i]?.PendingIDs?{PendingIDs:t.pending_ids||[]}:{})}));
 same(got,c.want,c.name+' shared derivation');
}

// A new addressed request continues the logical topic head in both native
// room and browser send paths. Its agent output still replies to the request.
for (const room of [false,true]) {
  const e=new Engine({store:memoryStore(),base:'https://synthetic.invalid'});
  e.address='ann/phone';e.me={person:'ann'};
  const c={id:'chat'}, info={state:'active',held:0,host:{address:'bob/desk',fingerprint:'b'.repeat(64)}};
  e.agentConv=async()=>({c,info});e.outgoingTopic=async()=> 'flow';
  e.chatTopicHead=async(conv,topic)=>{same([conv,topic],['chat','flow'],'head belongs to the requested topic');return 'previous-answer';};
  e.dmMembers=async()=>new Map([['ann',{}]]);e.humanPlan=async()=>room?{}:null;
  let sent;
  e.sendConv=async(_,m)=>{sent=m;};e.sendHumanTurn=async(_,m)=>{sent=m;};
  await e.askAgent({pid:'participant',kind:'question',body:'Please finish',topic:'flow'});
  same(sent.reply_to,'previous-answer','agent request retains logical topic parent, room='+room);
  same(sent.target.address,'bob/desk','topic parent never changes the execution target');
}

// Persisted, admitted executor states settle conversation Pending after reload.
{
 const st=memoryStore(),conv='a'.repeat(64),topic='b'.repeat(32),fp='11111111-22222222-33333333-44444444',hostFP='aaaaaaaa-bbbbbbbb-cccccccc-dddddddd';
 const fresh=()=>{const e=new Engine({store:st,base:'https://synthetic.invalid',fetch:async()=>{throw Error('unexpected network')},now:()=>10000});e.address='own/phone';e.fp=fp;return e;};
 let e=fresh(),seq=10;
 await st.write([{s:'pins',k:'peer/desk',v:{address:'peer/desk',fingerprint:hostFP,pending:null}}]);
 const ids=['1'.repeat(32),'2'.repeat(32),'3'.repeat(32)];
 await st.write(ids.map((id,i)=>({s:'outbox',k:id,v:{id,lid:id,conv,to:'peer/desk',state:'delivered',kind:'task',body:'retained request',topic,ts:i+1,at:(i+1)*1000,target:{address:'peer/desk',fingerprint:hostFP}}})));
 const status=async(id,state,n,key=fp,host='peer/desk')=>{
  const copy=(++seq).toString(16).padStart(32,'0');
  return e.admitControl({v:3,conv,lid:copy,kind:'message',sub:wire.SubStatus,body:JSON.stringify({state,n,at:5,attempt:1}),ref:{id,fingerprint:key}}, {id:copy,from:host,to:e.address},{fingerprint:hostFP});
 };
 const pending=async(want)=>{const topics=await e.chatTopics(conv);same(topics[0].pending_ids||[],want,'admitted status drives pending');};
 await e.loadErased();await pending(ids);
 await refuses(()=>status(ids[0],'resolved',2,fp,'other/desk'),/./,'wrong host cannot settle request');
 await refuses(()=>status(ids[0],'resolved',2,hostFP),/./,'wrong requester key cannot settle request');
 // A retained old reply names a host-only physical copy. It cannot settle a
 // different request by text/order; an authenticated logical status can.
 const oldReply={v:2,id:'a'.repeat(32),lid:'a'.repeat(32),conv,from:'peer/desk',fp:hostFP,kind:'result',body:'retained final answer',topic,reply_to:'f'.repeat(32),status:'done',ts:4,at:4000};
 await st.write([{s:'inbox',k:oldReply.id,v:oldReply}]);await pending(ids);
 await st.write(await status(ids[0],'answered',2));await st.write(await status(ids[0],'running',1));
 await st.write(await status(ids[1],'cancelled',3));await st.write(await status(ids[2],'interrupted',4));
 await pending([ids[2]]);
 e=fresh();await e.loadErased();await pending([ids[2]]);
 await st.write(await status(ids[2],'needs_human',5));await pending([ids[2]]);
 await st.write(await status(ids[2],'stopped',6));await pending([]);
 const unverified={...await st.get('outbox',ids[0]),id:'9'.repeat(32),lid:'9'.repeat(32),exec:{state:'answered'}};
 await st.write([{s:'outbox',k:unverified.id,v:unverified}]);await pending([unverified.id]);
}

// Main-flow preferences never promote, reassign or erase native sibling topics.
{
 const st=memoryStore(),conv='d'.repeat(64),fp='11111111-22222222-33333333-44444444',peerFP='aaaaaaaa-bbbbbbbb-cccccccc-dddddddd';
 const e=new Engine({store:st,base:'https://synthetic.invalid',now:()=>100000,fetch:async()=>{throw Error('Main preferences must not request transport')}});
 e.address='own/phone';e.fp=fp;e.me={person:'1'.repeat(32),devices:[]};
 const id=n=>n.toString(16).padStart(32,'0');
 const row=(n,v={})=>({v:2,id:id(n),lid:id(n),conv,from:'peer/desk',fp:peerFP,kind:'message',body:'turn '+n,at:n*1000,ts:n,read:false,...v});
 const main=row(1),reply=row(2,{reply_to:main.id}),seed=row(3),child=row(4,{reply_to:seed.id}),promotion=row(5,{topic:seed.lid,topic_event:{action:'create',seen:[]}}),native=row(6,{topic:id(60)}),control=row(7,{sub:'event',control:true,body:'authority survives'}),history=row(8,{fp:'',claimed_key:peerFP,history:true,replica:true,read:true});
 await st.write([{s:'convs',k:conv,v:{id:conv,root:'retained root'}},...[main,reply,seed,child,promotion,native,control,history].map(v=>({s:'inbox',k:v.id,v}))]);await e.loadErased();
 const summary=async()=>e.chatTopicSummaries(conv);
 let v=await summary();same(v.main_topic.count,3,'Main contains only unassigned ordinary originals');same(v.topics.map(t=>t.count).sort(),[1,2],'full assignments retain promoted reply chain');
 const change=(what,body={})=>e.changeTopic(what,{conv,root:true,...body});
 for(const body of [{id:seed.id},{ids:[seed.id]},{peer:'peer/desk'}])await refuses(()=>change('delete',body),/invalid|Invalid/,'root mixed identity refused before writes');
 for(const body of [{ids:{}},{count:false},{root:'true'},{unknown:true}])await refuses(()=>change('delete',body),/Bad request/,'malformed Main change rejected');
 await refuses(()=>change('done'),/topic|Topic|action|Invalid/,'Main never creates shared done facts');
 await change('rename',{title:'Private Main'});v=await summary();check(v.main_topic.title==='Private Main'&&v.main_topic.renamed,'private Main title projected');
 await change('archive',{count:3});same((await summary()).main_topic.state,'archived','Main archive persisted');
 await change('reopen',{count:3});same((await summary()).main_topic.state,'active','private reopen without signed topic');
 await change('rename',{title:''});same((await summary()).main_topic.title,main.body,'reset automatic name');
 const pending=row(9,{kind:'task',state:'held'}),queued=row(10,{to:'peer/desk',from:undefined,fp:undefined,state:'queued'});
 await st.write([{s:'inbox',k:pending.id,v:pending},{s:'outbox',k:queued.id,v:queued}]);
 check((await summary()).main_topic.pending_ids.includes(pending.id),'Main pending names exact request');
 await change('archive');await change('delete');
 v=await summary();check(!v.main_topic,'deleted Main hidden');same(v.topics.map(t=>t.count).sort(),[1,2],'delete leaves native and promoted chains');
 same((await st.all('erased')).map(r=>r.lid).sort(),[main,reply,history,pending,queued].map(r=>r.lid).sort(),'delete ledger names only exact Main turns');
 for(const r of [seed,child,promotion,native,control])same((await st.get('inbox',r.id)).body,r.body,'sibling/authority preserved '+r.id);
 for(const r of [main,reply,history])same((await st.get('inbox',r.id)).body,'','Main plaintext erased '+r.id);
 same((await st.get('inbox',pending.id)).body,pending.body,'unfinished task source retained');same((await st.get('outbox',queued.id)).body,queued.body,'queued copy source retained');
 same((await st.get('convs',conv)).root,'retained root','root/audience unchanged');
 same(e.rowName({...history,history:false}).key,'','unadmitted claimed key cannot name deletion');
 const restarted=new Engine({store:st,base:e.base,now:()=>100000});restarted.address=e.address;restarted.fp=fp;restarted.me=e.me;await restarted.loadErased();check(!(await restarted.chatTopicSummaries(conv)).main_topic,'restart keeps Main erased');
 const fresh=row(11);await st.write([{s:'inbox',k:fresh.id,v:fresh}]);v=await restarted.chatTopicSummaries(conv);check(v.main_topic.state==='active'&&v.main_topic.unread===1,'new Main message remains active and unread after archived deletion');
}

// 3. Routes, on a browser device holding many topics with one agent.
const day = 86400_000, now = 1790000000_000, me = 'ann/phone', bob = 'bob/desk', other = 'eve/box';
const store = memoryStore();
let network = 0, changes = 0, clock = now;
const fetch = async () => { network++; throw Error('topics never use the network'); };
const engine = new Engine({ store, base: 'https://synthetic.invalid', fetch, now: () => clock });
engine.address = me; engine.fp = 'f'.repeat(16); engine.listen(() => changes++);
let seq = 0;
const id = () => (++seq).toString(16).padStart(32, '0');
const ops = [];
const out = (v) => { const r = { v: 1, id: id(), to: bob, fp: 'b'.repeat(16), kind: 'message', body: '', state: 'delivered', status: '', reply_to: '', ...v }; ops.push({ s: 'outbox', k: r.id, v: r }); return r; };
const inn = (v) => { const r = { v: 1, id: id(), from: bob, fp: 'b'.repeat(16), kind: 'message', body: '', state: '', status: '', reply_to: '', read: true, ...v }; ops.push({ s: 'inbox', k: r.id, v: r }); return r; };
// 150 one-message topics a minute apart, the oldest 40 more than a week ago.
const plain = [];
for (let i = 0; i < 150; i++) plain.push(out({ body: (i % 3 ? 'Alpha note ' : 'Beta note ') + i + '\nsecond line', at: now - (i < 40 ? 9 * day : day) + i * 60_000 }));
// A task the agent finished: done by the agent, with its conclusion.
const task = out({ kind: 'task', body: 'Count the boxes\nin aisle 4', at: now - 3600_000 });
const result = inn({ kind: 'result', status: 'done',topic_done:true, body: '412 boxes\nall dry', reply_to: task.id, at: now - 3500_000 });
// A task that failed: not done. A question held for the person: pending.
const failed = out({ kind: 'task', body: 'Ship it', at: now - 3400_000 });
inn({ kind: 'result', status: 'failed', body: 'could not ship', reply_to: failed.id, at: now - 3300_000 });
const held = inn({ kind: 'question', state: 'held', body: 'May I order tape?', read: false, at: now - 20 * day });
// An old question nobody answered: still waits, never archived.
const waits = out({ kind: 'question', body: 'Is the truck here?', at: now - 30 * day });
// A task bob gave this browser, answered here by hand (replyV1 marks it
// answered): done by the person, the result is their words.
const given = inn({ kind: 'task', state: 'answered', body: 'Label the shelf', at: now - 3200_000 });
const mine = out({ kind: 'result', status: 'done', body: 'Labelled A to F\ndone', reply_to: given.id, at: now - 3100_000 });
// An old, unread message: archived, and its unread count kept.
inn({ body: 'old unread', read: false, at: now - 12 * day });
// Another agent's topic.
ops.push({ s: 'outbox', k: 'e'.repeat(32), v: { v: 1, id: 'e'.repeat(32), to: other, fp: 'e'.repeat(16), kind: 'message', body: 'Beta for eve', state: 'delivered', status: '', reply_to: '', at: now - 2 * day } });
await store.write(ops);
await engine.loadErased();

const { threads, topics } = await engine.topicOverview(false);
const tb = topics.find((t) => t.peer === bob);
check(tb.total === 156 && tb.archived === 41 && tb.archived_unread === 1, 'counts: 156 topics with bob, 41 archived (one unread message among them): ' + JSON.stringify({ ...tb, latest: undefined }));
check(threads.filter((t) => t.peer === bob).length === 115 && !threads.some((t) => t.state === 'archived'), 'the overview lists the 115 not archived');
// Without topics=1 (the previous interface, installed skins) every thread
// is listed, archived ones with their state; the counts are the same.
const full = await engine.topicOverview(true);
check(full.threads.filter((t) => t.peer === bob).length === 156 && full.threads.filter((t) => t.state === 'archived').length === 41, 'every thread without topics=1');
same(full.topics.map((c) => ({ ...c, latest: c.latest.id })), topics.map((c) => ({ ...c, latest: c.latest.id })), 'the same counts either way');
check(tb.latest.id === task.id || tb.latest.last_at >= threads[0].last_at, 'latest is the most recently active topic');
const topicOf = async (mid) => (await engine.api('/api/thread?id=' + mid)).topic;
let t = await topicOf(result.id);
check(t.id === task.id && t.state === 'done' && t.done_by === 'agent' && t.conclusion === '412 boxes' && t.concluded_by === bob && !t.pending && t.title === 'Count the boxes', 'agent-done topic: ' + JSON.stringify(t));
check(t.quiet_since === new Date(Math.floor(result.at / 1000) * 1000).toISOString(), 'quiet since its last message, in whole seconds: ' + t.quiet_since);
t = await topicOf(mine.id);
check(t.id === given.id && t.state === 'active' && !t.done_by && !t.conclusion, 'answered by hand here: done by you, your words: ' + JSON.stringify(t));
t = await topicOf(failed.id);
check(t.state === 'active' && !t.done_by && !t.pending, 'a failed result is not done');
t = await topicOf(held.id);
check(t.state === 'active' && t.pending && t.review === 1, 'a held question keeps an old topic pending');
same(t.pending_ids,[held.id],'held question links its exact existing actions');
t = await topicOf(waits.id);
check(t.state === 'active' && t.pending && t.waiting, 'an old unanswered question waits');
same(t.pending_ids,[waits.id],'old unanswered question is named exactly');
t = await topicOf(plain[0].id);
check(t.state === 'archived' && !t.pending, 'an old quiet topic is archived and still opens');

// The All topics list: pages, filters and search.
const list = (q) => engine.api('/api/topics?' + new URLSearchParams(q));
const seen = new Set();
let page, before = '', sizes = [];
do {
  page = await list({ peer: bob, state: 'archived', limit: '15', ...(before ? { before } : {}) });
  for (const x of page.topics) { check(!seen.has(x.id) && x.state === 'archived', 'archived page entry once'); seen.add(x.id); }
  sizes.push(page.topics.length);
  before = page.next || '';
} while (before);
same(sizes, [15, 15, 11], 'archived topics in pages of 15');
page = await list({ peer: bob, state: 'archived', limit: '15' });
check(/^\d{10}\|bob\/desk\|[0-9a-f]{32}$/.test(page.next), 'the cursor is the Go client\'s: seconds|peer|id: ' + page.next);
check(page.matched === 41, 'matched counts every page');
page = await list({ peer: bob });
check(page.topics.length === TOPICS.pageDefault && page.next && page.matched === 156, 'a default page');
check(page.topics.every((x, i) => i === 0 || Date.parse(x.last_at) <= Date.parse(page.topics[i - 1].last_at)), 'newest first');
page = await list({ q: 'BETA note' });
check(page.matched === 50 && page.topics.every((x) => x.peer === bob), 'search in any case over every peer: ' + page.matched);
page = await list({ q: 'beta' });
check(page.matched === 51 && page.topics.some((x) => x.peer === other), 'search finds the other agent too');
page = await list({ q: 'second line' });
check(page.matched === 0, 'only the first line is searched');
page = await list({ peer: bob, state: 'done' });
check(page.matched === 1 && page.topics.some((x) => x.id === task.id), 'done filter');
for (const q of [{ state: 'deleted' }, { limit: String(TOPICS.pageMax + 1) }, { limit: '-1' }, { limit: 'many' }, { before: 'yesterday' }]) {
  await refuses(() => list(q), /not valid/, 'refuses ' + JSON.stringify(q));
}

// The person's changes, kept in this device's store only.
const writesBefore = changes;
const a = plain[121];
check((await engine.api('/api/topic/rename', { peer: bob, id: a.id, title: '  Zebra\n crossing ' })).note === 'Topic renamed. The name syncs across your linked devices.', 'renamed');
t = await topicOf(a.id);
check(t.title === 'Zebra crossing' && t.renamed && t.auto_title === 'Alpha note 121', 'a name of the person\'s own: ' + JSON.stringify(t));
page = await list({ q: 'zebra' });
check(page.matched === 1 && page.topics[0].id === a.id, 'search finds the name');
page = await list({ q: 'alpha note 121' });
check(page.matched === 1, 'and still the automatic name');
await refuses(() => engine.api('/api/topic/rename', { peer: bob, id: a.id, title: 'x'.repeat(TOPICS.titleMax + 1) }), /at most 120/, 'too long a name');
await engine.api('/api/topic/done', { peer: bob, id: a.id });
t = await topicOf(a.id);
check(t.state === 'done' && t.done_by === 'you' && t.title === 'Zebra crossing', 'Mark done keeps the name');
const reload = new Engine({ store, base: engine.base, fetch, now: () => clock });
reload.address = me; reload.fp = engine.fp; await reload.loadErased();
check((await reload.api('/api/thread?id=' + a.id)).topic.done_by === 'you', 'kept in IndexedDB: a fresh engine reads it');
same((await reload.api('/api/thread?id='+waits.id)).topic.pending_ids,[waits.id],'fresh engine derives the same pending request from retained history');
same((await reload.api('/api/thread?id='+held.id)).topic.pending_ids,[held.id],'fresh engine retains the exact held item without deciding it');
await engine.api('/api/topic/reopen', { peer: bob, id: task.id });
t = await topicOf(task.id);
check(t.state === 'active' && !t.done_by, 'Reopen after the agent finished');
await engine.api('/api/topic/reopen', { peer: bob, id: plain[0].id });
check((await topicOf(plain[0].id)).state === 'active', 'Reopen brings an archived topic back');
check((await engine.api('/api/topic/rename', { peer: bob, id: a.id, title: ' \n ' })).note === 'Topic named after its first message again.', 'a blank name says it gives back the automatic one');
await engine.api('/api/topic/rename', { peer: bob, id: a.id, title: '' });
t = await topicOf(a.id);
check(t.title === 'Alpha note 121' && !t.renamed && t.state === 'done', 'an empty name gives back the automatic one');
// A new message ends Mark done.
await store.write([{ s: 'inbox', k: 'c'.repeat(32), v: { v: 1, id: 'c'.repeat(32), from: bob, fp: 'b'.repeat(16), kind: 'message', body: 'one more', state: '', status: '', reply_to: a.id, read: false, at: now + 1000 } }]);
t = await topicOf(a.id);
check(t.state === 'active' && !t.done_by && t.unread === 1, 'a new message makes it active again');
// Mark done covers only what the page showed (count): with the new message unseen it does not hold.
check(/newer message/.test((await engine.api('/api/topic/done', { peer: bob, id: a.id, count: 1 })).note), 'a mark with a message unseen says so');
check((await topicOf(a.id)).state === 'active', 'and the topic stays active');
check(/Marked done/.test((await engine.api('/api/topic/done', { peer: bob, id: a.id, count: 2 })).note) && (await topicOf(a.id)).state === 'done', 'with every message seen it holds');
await engine.api('/api/topic/reopen', { peer: bob, id: a.id });
// A week later, untouched topics are archived; pending ones are not.
clock = now + 8 * day;
check((await topicOf(a.id)).state === 'archived' && (await topicOf(held.id)).state === 'active', 'eight days later');
clock = now;
for (const [what, body, re] of [['done', { peer: bob, id: 'f'.repeat(32) }, /No such topic/], ['done', { peer: bob, id: result.id }, /No such topic/],
  ['done', { peer: bob, id: a.id, extra: 1 }, /Bad request/], ['bogus', { peer: bob, id: a.id }, /Unknown request|No such topic change/]]) {
  await refuses(() => engine.api('/api/topic/' + what, body), re, 'refuses ' + what + ' ' + JSON.stringify(body));
}
check(changes > writesBefore && network === 0, 'changes are announced; nothing touched the network');
// MEL-562: outbound review reports are internal report threads, while user JSON stays visible.
{
 const s=memoryStore(),e=new Engine({store:s,now:()=>now});e.address='me/desk';
 const report='d'.repeat(32),ordinary='e'.repeat(32),peer='other/desk';
 await s.write([{s:'outbox',k:report,v:{v:1,id:report,to:peer,kind:'message',status:'review_notice',body:JSON.stringify({v:2,host:e.address,items:[]}),at:now,state:'custody',attachments:[]}},
 {s:'outbox',k:ordinary,v:{v:1,id:ordinary,to:peer,kind:'message',body:'{"user":"ordinary JSON remains visible"}',at:now+1,state:'custody',attachments:[]}}]);
 const threads=await e.threadSummaries();check(threads.find(t=>t.id===report).notice_only,'sent internal report excluded from topics');
 check(!threads.find(t=>t.id===report).title.includes('{'),'sent internal report has readable summary');
 check(!threads.find(t=>t.id===ordinary).notice_only,'ordinary user JSON still a topic');
 const page=await e.topicList(new URLSearchParams());check(!page.topics.some(t=>t.id===report)&&page.topics.some(t=>t.id===ordinary),'All topics preserves user JSON and omits internal reports');
}
console.log(JSON.stringify({ checks }));
