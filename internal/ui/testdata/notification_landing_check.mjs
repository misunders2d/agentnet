// Exercise notification readiness against the actual Comic Store source.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import { api, errorText } from '../web/src/api.ts';
import model from './comic_model.mjs';
import { pendingSends } from '../static/optimistic.mjs';
import { share } from '../web/src/structural.ts';
import { mainPreferences, personRoots } from '../skins/shared/person-topics.mjs';

const scheduleSource = stripTypeScriptTypes((await readFile(new URL('../web/src/schedule-saves.ts', import.meta.url), 'utf8'))
  .replace(/^import .*;$/gm, '')).replace(/^export /gm, '');
const source = stripTypeScriptTypes((await readFile(new URL('../web/src/store.ts', import.meta.url), 'utf8'))
  .replace(/^import .*;$/gm, '')
  .replace('constructor(readonly host: Host) {', 'constructor(host: Host) { this.host = host;'))
  .replace(/^export /gm, '');
const conv = 'a'.repeat(64), other = 'b'.repeat(64), mid = 'c'.repeat(32), next = 'd'.repeat(32), topic = 'e'.repeat(32);
const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
function fixture(workspace = 'default') {
  const listeners = new Set(), visibility = new Set(), calls = [], values = new Map();
  const env = { document: { visibilityState: 'visible', hasFocus: () => false, addEventListener() {}, removeEventListener() {} },
    window: { addEventListener() {}, removeEventListener() {} },
    localStorage: { getItem: k => values.get(k) ?? null, setItem: (k,v) => values.set(k,v) },
    setTimeout: (fn, ms) => { const timer = setTimeout(fn, ms); if (ms) timer.unref(); return timer; }, clearTimeout };
  const f = { calls, threads: new Map(), views: new Map([[conv, { id: conv, messages: [{ id: mid, dir: 'in', topic }], topics: [{ id: topic }] }]]),
    overview: { version: 'test', me: {}, dms: [{ id: conv }], threads: [] }, overviewGate: null, dmGates: new Map(), failOverview: false };
  const host = { platform: 'android', workspace: { id: workspace, handle: 'f'.repeat(32) },
    android: { visible: () => true, onVisibility: fn => { visibility.add(fn); return () => visibility.delete(fn); } },
    listen: fn => { listeners.add(fn); return () => listeners.delete(fn); },
    async api(path, body) {
      calls.push({path,body});
      if (path.startsWith('/api/overview')) { if (f.overviewGate) await f.overviewGate.promise; if (f.failOverview) throw Error('Offline fixture'); return f.overview; }
      if (path.startsWith('/api/agents')) return { agents: [] };
      if (path.startsWith('/api/dm?')) { const id = new URL(path,'https://fixture.test').searchParams.get('id'), view = f.views.get(id), gate = f.dmGates.get(id); if (gate) await gate.promise; if (!view) throw Error('Scope unavailable'); return view; }
      if (path.startsWith('/api/thread?')) { const id=new URL(path,'https://fixture.test').searchParams.get('id'); if(!f.threads.has(id))throw Error('Not a device thread');return f.threads.get(id); }
      if (path.startsWith('/api/typing') || path === '/api/refresh') return {};
      throw Error('Unexpected route: ' + path);
    } };
  const Store = runInNewContext(scheduleSource + '\n' + source + '\nStore', { ...env, api, errorText, pendingSends, share, messageTarget: model.messageTarget, currentCatalogAgents: model.currentCatalogAgents,
    mainPreferences, personRoots, brand: { storage: 'agentnet.messenger' } });
  f.store = new Store(host); f.host = host; f.env = env;
  f.change = async () => { for (const fn of listeners) fn({ type: 'change', seq: 1 }); await tick(); await tick(); };
  f.resume = async () => { for (const fn of visibility) fn(true); await tick(); await tick(); };
  return f;
}

// The message is already stored. Only the first overview is delayed.
{
  const f = fixture(); f.overviewGate = deferred();
  const starting = f.store.start(), landing = f.store.openMessage(mid, {dir:'in'});
  await tick(); f.overviewGate.resolve(); await Promise.all([starting, landing]); await tick();
  assert.equal(f.store.get().open?.id, conv, 'a cold legacy notification must resolve after the initial overview');
  assert.equal(f.store.get().open.focus, mid);
  assert.equal(f.store.draft(conv).topic, topic);
  f.store.stop();
}

// New native destinations bypass the overview and all unrelated DM reads.
{
  const f = fixture(); f.overviewGate = deferred();
  const starting = f.store.start();
  const file = {key:'staged', file:{name:'unsent.txt'}, staged:{id:'native-token'}};
  const draft = {text:'unsent', replyTo:next, agent:'chosen', topic:'old-topic', files:[file]};
  f.store.setDraft(conv,draft);
  await f.store.openMessage(mid,{conv,dir:'in'});
  assert.equal(f.store.get().open?.id,conv,'enriched tap opens without awaiting slow overview');
  assert.equal(f.calls.filter(c=>c.path.startsWith('/api/dm?')).length,1,'one exact local DM view, reused for landing');
  assert.equal(f.store.draft(conv).topic,topic);
  assert.equal(f.store.draft(conv).text,draft.text); assert.equal(f.store.draft(conv).replyTo,next);
  assert.equal(f.store.draft(conv).files[0],file); assert.equal(f.store.draft(conv).agent,'chosen');
  assert.equal(f.store.get().messageLanding,null);
  f.overviewGate.resolve(); await starting; f.store.stop();
}

// Retain a truthful unavailable target, then recover from an existing change.
{
  const f = fixture(); await f.store.start();
  f.views.set(conv,{id:conv,messages:[]});
  await f.store.openMessage(mid,{conv,dir:'in'});
  assert.equal(f.store.get().open?.id,conv,'missing exact target still shows its bound chat context');
  assert.equal(f.store.get().open.focus,undefined,'chat context never substitutes another message for the target');
  assert.equal(f.store.get().messageLanding.target.id,mid);
  assert.equal(f.store.get().messageLanding.busy,false);
  assert.match(f.store.get().messageLanding.error,/not available/);
  f.views.set(conv,{id:conv,messages:[{id:mid,dir:'in',topic}]});
  await f.change();
  assert.equal(f.store.get().open?.focus,mid,'existing local change retries the retained target');
  assert.equal(f.store.get().messageLanding,null); f.store.stop();
}

// Already loaded messages use the existing cache before unrelated overview
// work or another local-view GET. Both notification forms keep draft files.
for (const bound of [false,true]) {
  const f=fixture();await f.store.start();await f.store.prefetch({kind:'dm',id:conv});
  const file={key:'cached-file',file:{name:'draft.txt'},staged:{id:'kept-token'}};
  f.store.setDraft(conv,{text:'Cached draft',files:[file],replyTo:next,agent:'chosen',topic:'old-topic'});
  const overview=deferred(),target=deferred();f.overviewGate=overview;f.dmGates.set(conv,target);
  const refreshing=f.store.load(false);await tick();const before=f.calls.length;
  const landing=f.store.openMessage(mid,bound?{conv,dir:'in'}:{dir:'in'});await tick();
  try {
    assert.equal(f.store.get().open?.focus,mid,'cached '+(bound?'bound':'legacy')+' destination opens before either gate');
    assert.equal(f.calls.slice(before).filter(c=>/^\/api\/(?:overview|agents|dm|thread)(?:\?|$)/.test(c.path)).length,0,'cached landing adds no overview, catalog or view GET');
    assert.equal(f.store.draft(conv).topic,topic);assert.equal(f.store.draft(conv).text,'Cached draft');
    assert.equal(f.store.draft(conv).replyTo,next);assert.equal(f.store.draft(conv).agent,'chosen');
    assert.equal(f.store.draft(conv).files[0],file);assert.equal(f.store.get().messageLanding,null);
  } finally { overview.resolve();target.resolve();await Promise.all([refreshing,landing]);f.store.stop(); }
}

// A warm legacy tap with an overview need not wait for a newer overview to
// finish. A later cached arrival also recovers a previously completed miss.
{
  const f=fixture();await f.store.start();const gate=deferred();f.overviewGate=gate;
  const refreshing=f.store.load(false);await tick();const landing=f.store.openMessage(mid,{dir:'in'});await tick();
  try { assert.equal(f.store.get().open?.focus,mid,'warm lookup starts its exact local read while overview is blocked'); }
  finally {gate.resolve();await Promise.all([refreshing,landing]);f.store.stop();}
  const late=fixture();late.views.set(conv,{id:conv,messages:[]});await late.store.start();await late.store.openMessage(mid,{dir:'in'});
  assert(late.store.get().messageLanding.legacyMiss);
  late.views.set(conv,{id:conv,messages:[{id:mid,dir:'in',topic}]});await late.store.prefetch({kind:'dm',id:conv});
  await late.change();assert.equal(late.store.get().open?.focus,mid,'existing change retries a target that has entered the existing cache');late.store.stop();
}

// Offline initial overview failure remains retryable. Native resume also retries.
{
  const f = fixture(); f.failOverview=true; await f.store.start();
  await f.store.openMessage(mid,{dir:'in'});
  assert.equal(f.store.get().open,null); assert.match(f.store.get().messageLanding.error,/not loaded/);
  f.failOverview=false; await f.store.retryMessageLanding();
  assert.equal(f.store.get().open?.focus,mid); f.store.stop();
  const resumed=fixture(); await resumed.store.start(); resumed.views.set(conv,{id:conv,messages:[]});
  await resumed.store.openMessage(mid,{conv});
  resumed.views.set(conv,{id:conv,messages:[{id:mid,dir:'in'}]});
  // Native visibility requires a focused page, just as the existing read gates do.
  resumed.env.document.hasFocus=()=>true;
  await resumed.resume(); assert.equal(resumed.store.get().open?.focus,mid); resumed.store.stop();
}

// A superseding tap need not wait for the old view; the old response cannot win.
{
  const f=fixture(); await f.store.start(); const old=deferred(); f.dmGates.set(conv,old);
  const first=f.store.openMessage(mid,{conv}); await tick();
  f.views.set(other,{id:other,messages:[{id:next,dir:'in'}]});
  await f.store.openMessage(next,{conv:other}); assert.equal(f.store.get().open.focus,next);
  old.resolve(); await first; assert.equal(f.store.get().open.focus,next); f.store.stop();
}

// Back, deliberate navigation, topic selection and unmount cancel stale work.
const main = await readFile(new URL('../web/src/main.tsx',import.meta.url),'utf8');
const backSource=stripTypeScriptTypes(main.slice(main.indexOf('const stopBack = host.onBack?.(() => {')+'const stopBack = host.onBack?.('.length,main.indexOf('\n  live++;')).trim().replace(/\);$/,''));
for (const cancel of ['back','chat','tab','topic','stop']) {
  const f=fixture(); await f.store.start(); const gate=deferred(); f.dmGates.set(conv,gate);
  const landing=f.store.openMessage(mid,{conv}); await tick();
  if(cancel==='back') {
    const back=runInNewContext(backSource,{store:f.store,root:{getRootNode:()=>({activeElement:null}),querySelectorAll:()=>[]},app:{querySelector:()=>null}});
    assert.equal(back(),true,'actual Android Back callback consumes the unresolved landing');
  } else if(cancel==='chat') { f.views.set(other,{id:other,messages:[]}); await f.store.open({kind:'dm',id:other}); }
  else if(cancel==='tab') f.store.showTab('settings');
  else if(cancel==='topic') f.store.setDraft(conv,{text:'retained',topic:'selected'},true);
  else f.store.stop();
  const before=f.calls.length; gate.resolve(); await landing;
  if(cancel==='stop')assert.equal(f.calls.length,before,'unmount performs no later lookups');
  assert.equal(f.store.get().messageLanding,null,cancel+': no retained destination');
  assert.notEqual(f.store.get().open?.id,conv,cancel+': old response never navigates');
  if(cancel==='tab') assert.equal(f.store.get().tab,'settings');
  f.store.stop();
}

// Duplicate in-flight taps share the same request. Invalid and revoked scope
// destinations cannot fall back to a different conversation or read it.
{
  const f=fixture(); await f.store.start(); const gate=deferred(); f.dmGates.set(conv,gate);
  const first=f.store.openMessage(mid,{conv,dir:'in'}); await tick();
  const duplicate=f.store.openMessage(mid,{conv,dir:'in'}); gate.resolve(); await Promise.all([first,duplicate]);
  assert.equal(f.calls.filter(c=>c.path.startsWith('/api/dm?')).length,1);
  for(const context of [{conv:'invalid'},{dir:'unknown'}]) {
    const before=f.calls.length; await f.store.openMessage(mid,context); assert.equal(f.calls.length,before);
  }
  const before=f.calls.length; await f.store.openMessage('invalid',{conv});assert.equal(f.calls.length,before);
  for(const view of [undefined,{id:other,messages:[{id:mid}]},{id:conv,frozen:'revoked',messages:[{id:mid}]},{id:conv,messages:[{id:mid,deleted:true}]},{id:conv,messages:[{id:mid,dir:'out'}]}]) {
    f.store.close(); f.store.set({views:view?{[conv]:view}:{}}); if(view)f.views.set(conv,view);else f.views.delete(conv);
    await f.store.openMessage(mid,{conv,dir:'in'});
    assert.equal(f.store.get().open,null); assert(f.store.get().messageLanding.error);
  }
  f.store.dismissMessageLanding(); f.store.stop();
  assert(f.calls.every(c=>!c.body),'landing with an unfocused page performs only local reads');
}

// Each mounted workspace resolves only through its captured host, including
// a logical message ID whose canonical local copy is different.
{
  const a=fixture(), b=fixture('1'.repeat(32)); await Promise.all([a.store.start(),b.store.start()]);
  a.views.set(conv,{id:conv,messages:[]});
  b.views.set(conv,{id:conv,messages:[{id:next,lid:mid,dir:'in',topic}]});
  await b.store.openMessage(mid,{conv,dir:'in'});
  assert.equal(b.store.get().open.focus,mid); assert.equal(b.store.draft(conv).topic,topic);
  assert.equal(a.calls.filter(c=>c.path.startsWith('/api/dm?')).length,0,'other workspace is never searched');
  await a.store.openMessage(mid,{conv}); assert.equal(a.store.get().open?.id,conv);
  assert.equal(a.store.get().open.focus,undefined);assert.equal(a.store.get().messageLanding.target.id,mid);
  assert.equal(b.store.get().open.id,conv); a.store.stop();b.store.stop();
}
{
  const f=fixture();await f.store.start();f.overview.dms=[];
  f.threads.set(mid,{id:mid,peer:'owner/agent',messages:[{id:mid,dir:'out'}]});
  await f.store.openMessage(mid,{dir:'out'});assert.equal(f.store.get().open.kind,'thread');
  assert.equal(f.store.get().open.focus,mid);assert.equal(f.store.get().open.peer,'owner/agent');f.store.stop();
}
console.log('PASS notification landing: deferred/cold and direct/warm, exact topic/drafts/files, change/resume/offline retry, supersede/Back/navigation/unmount, duplicate/malformed/deleted/revoked scope, linked workspace/logical copy/legacy device thread');

// Fetch-cost regressions collect every outcome against the same Store revision.
const failures = [];
const gets = f => ({ overview:f.calls.filter(c=>c.path.startsWith('/api/overview')).length,
  catalogs:f.calls.filter(c=>c.path.startsWith('/api/agents')).length,
  dm:f.calls.filter(c=>c.path.startsWith('/api/dm?')).length,
  thread:f.calls.filter(c=>c.path.startsWith('/api/thread?')).length });
async function cost(name, run) {
  const f=fixture();
  try { await run(f); console.log('PASS fetch cost '+name); }
  catch(error) { failures.push(error);console.log('FAIL fetch cost '+name+': '+error.message); }
  finally { f.store.stop(); }
}
await cost('F1-cold',async f=>{
  f.views.set(conv,{id:conv,messages:[]});f.overviewGate=deferred();
  const starting=f.store.start(),landing=f.store.openMessage(mid,{dir:'in'});
  await tick();f.overviewGate.resolve();await Promise.all([starting,landing]);
  const counts=gets(f);console.log(JSON.stringify({case:'F1-cold',...counts}));
  assert.equal(counts.overview,1,'cold lookup reuses the initial overview');
  assert.equal(counts.catalogs,1,'cold lookup adds no catalog load');
  assert.equal(counts.dm,1,'cold miss scans only once');
});
await cost('F1-loaded',async f=>{
  await f.store.start();const before=gets(f);
  await f.store.openMessage(mid,{dir:'in'});const after=gets(f);
  console.log(JSON.stringify({case:'F1-loaded',before,after}));
  assert.equal(after.overview,before.overview,'loaded legacy lookup adds zero overview GETs');
  assert.equal(after.catalogs,before.catalogs,'loaded legacy lookup adds zero catalog GETs');
  assert.equal(after.dm,before.dm+1);assert.equal(f.store.get().open.focus,mid);
});
await cost('F2-legacy-miss',async f=>{
  f.overview.dms.push({id:other});f.views.set(conv,{id:conv,messages:[]});f.views.set(other,{id:other,messages:[]});
  await f.store.start();await f.store.openMessage(mid,{dir:'in'});const before=gets(f);
  for(let i=0;i<3;i++)await f.change();const after=gets(f);
  console.log(JSON.stringify({case:'F2-legacy-miss',before,after}));
  assert.equal(after.dm,before.dm,'a definitive legacy miss never rescans DMs on routine changes');
  assert.equal(after.thread,before.thread,'routine changes do not repeat the fallback thread GET');
  assert.equal(after.overview,before.overview+3,'only the three normal change overviews run');
  assert(f.store.get().messageLanding.error,'unresolved destination is retained truthfully');
  await f.store.retryMessageLanding();assert.equal(gets(f).dm,after.dm+2,'explicit retry may search again');
  f.env.document.hasFocus=()=>true;await f.resume();assert.equal(gets(f).dm,after.dm+4,'resume may search again');
  await f.store.recover([0]);assert.equal(gets(f).dm,after.dm+6,'reconnect may search again');
});
await cost('F3-known-thread',async f=>{
  f.views.set(conv,{id:conv,messages:[]});f.overview.threads=[{id:mid}];
  f.threads.set(mid,{id:mid,peer:'owner/agent',messages:[{id:mid,dir:'out'}]});
  await f.store.start();await f.store.openMessage(mid,{dir:'out'});const counts=gets(f);
  console.log(JSON.stringify({case:'F3-known-thread',...counts}));
  assert.equal(counts.dm,0,'a known device thread never reads any DM');
  assert.equal(counts.thread,1);assert.equal(f.store.get().open.kind,'thread');assert.equal(f.store.get().open.focus,mid);
});
await cost('bound-conv-late',async f=>{
  f.views.set(conv,{id:conv,messages:[]});await f.store.start();await f.store.openMessage(mid,{conv,dir:'in'});
  const before=gets(f);
  for(let i=0;i<3;i++) { if(i===2)f.views.set(conv,{id:conv,messages:[{id:mid,dir:'in',topic}]});await f.change(); }
  const after=gets(f);console.log(JSON.stringify({case:'bound-conv-late',before,after}));
  assert.equal(after.dm,before.dm+3,'each coalesced change reads only the exact bound DM');
  assert.equal(after.overview,before.overview+3);assert.equal(after.thread,0);
  assert(f.calls.filter(c=>c.path.startsWith('/api/dm?')).every(c=>new URL(c.path,'https://fixture.test').searchParams.get('id')===conv));
  assert.equal(f.store.get().open.focus,mid);assert.equal(f.store.get().messageLanding,null);
});
if(failures.length)throw new AggregateError(failures,'notification landing fetch costs failed');
