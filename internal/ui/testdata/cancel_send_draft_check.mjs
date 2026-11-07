// Existing source-check pattern: actual Store, Android Composer action, journal
// and pending projection. Synthetic host I/O only; no browser/native claims.
import assert from 'node:assert/strict';
import {registerHooks, stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
const root = new URL('../', import.meta.url);
registerHooks({load(url, context, next) {
  if (url.includes('/web/src/') && url.endsWith('.ts')) return {format:'module', shortCircuit:true,
    source:stripTypeScriptTypes(readFileSync(new URL(url), 'utf8').replace('constructor(readonly host: Host) {', 'constructor(host: Host) { this.host = host;'))};
  return next(url, context);
}, resolve(specifier, context, next) {
  const parent = context.parentURL || '';
  if (parent.includes('/web/src/')) {
    if (specifier === 'react') return next('data:text/javascript,export const useSyncExternalStore=()=>{};', context);
    if (specifier === './skin/brand') return next('data:text/javascript,export const skin={storage:"agentnet.messenger"};', context);
    if (specifier === './optimistic.mjs') return next(new URL('static/optimistic.mjs', root).href, context);
    if (specifier === './person-topics.mjs') return next(new URL('skins/shared/person-topics.mjs', root).href, context);
    if (specifier === './linkText') return next(new URL('skins/shared/link-text.mjs', root).href, context);
    if (specifier.startsWith('.') && !/\.(ts|tsx|mjs|js)$/.test(specifier)) return next(new URL(specifier + '.ts', parent).href, context);
  }
  return next(specifier, context);
}});
const {Store} = await import('../web/src/store.ts');
const {decode, encode, agentTargets, guestAuthor} = await import('../web/src/features/Composer.mentions.ts');
const journal = await import('../web/src/send-journal.mjs');
const storage = new Map();
globalThis.localStorage = {getItem:k=>storage.get(k) ?? null, setItem:(k,v)=>storage.set(k,v), removeItem:k=>storage.delete(k),get length(){return storage.size;},key:i=>[...storage.keys()][i] ?? null};
globalThis.document = {visibilityState:'visible', hasFocus:()=>true, addEventListener(){}, removeEventListener(){}};
const windowListeners=new Map();
globalThis.window = {addEventListener(type,fn){if(!windowListeners.has(type))windowListeners.set(type,new Set());windowListeners.get(type).add(fn);},removeEventListener(type,fn){windowListeners.get(type)?.delete(fn);}};
const storageEvent=key=>{for(const fn of windowListeners.get('storage')||[])fn({key});};
const source = readFileSync(new URL('web/src/features/Composer.tsx',root),'utf8');
const action = name => {
  const match = source.match(new RegExp('^  (?:async )?function '+name+'\\([\\s\\S]*?^  }','m'));
  assert(match, 'actual Composer '+name+' source');
  return stripTypeScriptTypes(match[0]);
};
const conv='a'.repeat(64), other='b'.repeat(64), topic='c'.repeat(32), reply='d'.repeat(32);
let serial=0, worlds=0;
const turn=()=>new Promise(resolve=>setImmediate(resolve));
const gate=()=>{let release;const promise=new Promise(r=>release=r);return {promise,release};};
async function world(draft={text:' Original E\n'},platform='android',holdAdmission=false,config={}) {
  config.failing=config.failPosts>0;
  let rows=[], cancelGate, holdGate, holdLate, mode='confirmed', heldResult;
  const admission=holdAdmission?gate():null,entered=gate();
  let connected=true,registration;
  const scope={}, posts=[], stages=[], cancels=[], holds=[], notices=[], me={address:'fixture/phone',fingerprint:'fixture-owner'};
  const view=id=>({id,kind:'group',title:'Synthetic chat',peer:{},messages:id===conv?rows:[],agents:[],guests:[],topics:[]});
  const host={platform, workspace:{id:'cancel-'+(++worlds),handle:'owned',state:'enrolled',endpoint:'https://synthetic.invalid',realm:'fixture-realm',address:'fixture/phone'}, workspaces:{state:()=>scope,has:()=>connected,list:()=>connected?[registration || host.workspace]:[]},
    android:{visible:()=>true,onVisibility:()=>()=>{}}, listen:()=>()=>{}, stage:async file=>{stages.push(file);return 'stage-'+stages.length;},
    api:async(path,body)=>{
      if(path.startsWith('/api/overview'))return {version:'fixture',me,groups:true,dms:[],threads:[],people:[]};
      if(path.startsWith('/api/dm?'))return view(new URL('https://fixture'+path).searchParams.get('id'));
      if(path.startsWith('/api/agents'))return {agents:[]};
      if(path==='/api/dm/send') {
        posts.push({...body});if(config.failPosts>0){config.failPosts--;throw Object.assign(Error('relay unavailable'),{status:503});}rows=[{id:body.id,lid:body.id,dir:'out',from:me.address,kind:'message',body:body.body,at:new Date().toISOString(),
          state:'queued',queued_send:{state:'pending',rev:1},topic:body.topic,reply_to:body.reply_to,
          attachments:(draft.files||[]).map(f=>({name:f.name,size:f.size}))}];
        entered.release();if(admission)await admission.promise;
        return {id:body.id,lid:body.id,state:'queued'};
      }
      if(path==='/api/schedule/hold_send') {
        holds.push(body.id);if(holdGate)await holdGate.promise;
        if(rows.every(m=>m.id!==body.id||!m.queued_send||m.queued_send.state!=='pending'&&m.queued_send.state!=='needs_review'))throw Object.assign(Error('this message was already sent'),{status:409});
        rows=rows.map(m=>m.id===body.id?{...m,state:'queued',detail:'Stopped. Its files are kept on this device: Retry sends it, Discard removes it.',queued_send:{state:'needs_review',rev:(m.queued_send?.rev||1)+1}}:m);
        if(holdLate)await holdLate.promise; // held on the host; its answer arrives late
        return {id:body.id,state:'needs_review',immediate:true,detail:'Stopped. Its files are kept on this device: Retry sends it, Discard removes it.'};
      }
      if(path==='/api/schedule/discard_send') {
        cancels.push(body.id);
        const selected=mode; if(cancelGate)await cancelGate.promise;
        if(selected==='failed')throw Object.assign(Error('already stored'),{status:409});
        if(selected==='uncertain')throw Error('cancel response lost');
        if(selected==='generic')return {note:'action accepted'};
        if(selected==='wrong-id')return {id:'f'.repeat(32),state:'cancelled'};
        if(selected==='stored')return {id:body.id,state:'sent'};
        rows=[];return heldResult || {id:body.id,state:'cancelled'};
      }
      return {};
    }};
  const store=new Store(host);await store.start();await store.open({kind:'dm',id:conv});store.setDraft(conv,draft);
  const env={store,conv,dm:view(conv),thread:undefined,ready:true,overview:store.get().overview,names:{},words:{},
    target:{kind:'conversation'},latest:()=>store.draft(conv),androidPreparing:{current:new Set()},EMPTY:{text:''},visible:{current:conv},
    decode,encode,agentTargets,guestAuthor,selectedTargetFollowup:()=>undefined,messageOf:()=>({id:reply}),
    sendID:()=> (++serial).toString(16).padStart(32,'0'),localStorage,savedStorage:()=>localStorage,URL,
    ...journal,setBusy:()=>{},setNotice:v=>notices.push(v),typing:{stop(){}},announce:()=>{},errorText:e=>e.message};
  for(const name of ['clearSent','keepStaged','forgetStaged'])env[name]=runInNewContext('('+action(name)+')',env);
  env.releaseFiles=files=>{for(const f of files)if(f.url)URL.revokeObjectURL(f.url);};
  const send=runInNewContext('('+action(platform==='android'?'sendAndroid':'send')+')',env);
  config.beforeSend?.({store,host});
  const sending=send();if(admission)await entered.promise;else await sending;await turn();
  const queued=()=>store.sends.merge(conv,rows).find(m=>m.queued_send);
  assert.equal(posts.length,config.expectedPosts ?? 1,'actual native submission count');if(posts.length&&!config.failing)assert(queued()?._discard,'host-kept inline Cancel');
  return {store,host,scope,posts,stages,cancels,holds,notices,draft,queued,me,rows:()=>rows,
    legacy(){
      const key='agentnet.messenger.drafts.'+host.workspace.id;
      for(const k of [...storage.keys()])if(k.startsWith(key+'.'))storage.delete(k);
      const raw=JSON.parse(storage.get(key)||'{}');for(const d of Object.values(raw))delete d.__an_draft_revision;storage.set(key,JSON.stringify(raw));
    },
    connected(value){connected=value;},
    replaceBinding(patch){registration={...host.workspace,...patch};},
    async restart(options={}){
      const {expectedText="",...binding}=options;
      store.stop();const fresh={},nextHost={...host,workspace:{...host.workspace,...binding},workspaces:{state:()=>fresh,has:()=>connected,list:()=>connected?[nextHost.workspace]:[]}};
      const next=new Store(nextHost);await next.start();await next.open({kind:'dm',id:conv});
      assert.equal(next.draft(conv).text,expectedText,'fresh Store reads only current durable draft, without automatic cancellation or replay');
      return {store:next,host:nextHost,scope:fresh,queued:()=>next.sends.merge(conv,rows).find(m=>m.queued_send),
        project(patch){rows=rows.map(m=>({...m,...patch}));},
        stored(){rows=rows.map(({queued_send,...m})=>({...m,state:'custody'}));next.sends.merge(conv,rows);},
        stop(){next.stop();}};
    },
    hold(){cancelGate=gate();return cancelGate;},holdHeld(){holdGate=gate();return holdGate;},holdAnswerLate(){holdLate=gate();return holdLate;},
    setRows(next){rows=next(rows);},mode(value){mode=value;},
    stored(){rows=rows.map(({queued_send,...m})=>({...m,state:'custody'}));store.sends.merge(conv,rows);},
    review(){rows=rows.map(m=>({...m,queued_send:{...m.queued_send,state:'needs_review'}}));},
    result(value){heldResult=value;},finishAdmission:async()=>{admission?.release();await sending;},
    async stop(){admission?.release();await sending;if(store.isActive())store.stop();await turn();}};
}

let passed=0;
async function check(name, fn) {if(process.env.CANCEL_ONLY&&!name.includes(process.env.CANCEL_ONLY))return;await fn();passed++;console.log('PASS '+name);}
await check('actual Android Cancel restores exact whitespace, with no new Send',async()=>{
  const w=await world();
  try {
    assert.equal(w.store.draft(conv).text,'','send cleared original composer');
    await w.queued()._discard();
    assert.equal(w.store.draft(conv).text,w.draft.text,'confirmed pre-outbox Cancel restores exact original draft');
    assert.equal(w.posts.length,1,'restoration never sends');
  } finally {await w.stop();}
});
await check('ordinary Composer uses the same confirmed-Cancel restoration',async()=>{
  const w=await world({text:'ordinary original'},'daemon');try{
    await w.queued()._discard();assert.equal(w.store.draft(conv).text,'ordinary original');assert.equal(w.posts.length,1);
  }finally{await w.stop();}
});

await check('restart before Cancel restores exact original encoded text',async()=>{
  const w=await world();let r;try{
    r=await w.restart();assert.equal(r.scope[Symbol.for('agentnet.ui.pendingSends')].drafts.size,1,'fresh Store rehydrates original recovery without replaying Send');
    await r.queued()._discard();assert.equal(r.store.draft(conv).text,w.draft.text,'restart-before-Cancel restores exact original text, not trimmed queued body');assert.equal(w.posts.length,1);
  }finally{r?.stop();await w.stop();}
});
for(const text of ['newer after restart',''])await check('restart Cancel preserves '+(text?'newer text':'a newer empty revision'),async()=>{
  const w=await world();let r;try{
    r=await w.restart();r.store.setDraft(conv,{text});await r.queued()._discard();assert.equal(r.store.draft(conv).text,text);
  }finally{r?.stop();await w.stop();}
});
for(const change of ['workspace','handle','identity'])await check('restart Cancel rejects changed '+change,async()=>{
  const w=await world();let r;try{
    r=await w.restart();const held=w.hold(),cancel=r.queued()._discard();
    if(change==='workspace')r.host.workspace.id+='-other';
    if(change==='handle')r.host.workspace.handle='new-handle';
    if(change==='identity'){w.me.fingerprint='other-person';await r.store.refetch();}
    held.release();await cancel;assert.equal(r.store.draft(conv).text,'');
  }finally{r?.stop();await w.stop();}
});
await check('restart Cancel cannot restore after storage wins',async()=>{
  const w=await world();let r;try{
    r=await w.restart();const held=w.hold(),cancel=r.queued()._discard();r.stored();held.release();await cancel;assert.equal(r.store.draft(conv).text,'');
  }finally{r?.stop();await w.stop();}
});
for(const patch of [{body:undefined},{attachments:[{name:'missing original file',size:1}]},{topic,reply_to:reply}])await check('legacy restart fallback refuses unavailable original context '+JSON.stringify(patch),async()=>{
  const w=await world();let r;try{
    w.legacy();r=await w.restart();r.project(patch);await r.queued()._discard();assert.equal(r.store.draft(conv).text,'');
  }finally{r?.stop();await w.stop();}
});

await check('restart recovers exact encoded text, mentions, Markdown and editor context',async()=>{
  const draft={text:'  **Original** [@Alice](agentnet:person/abc)\n',replyTo:reply,topic,plainReply:true,mediaMode:'original'};
  assert.equal(decode(draft.text).spans[0].id,'abc','actual mention encoding is exercised');
  const w=await world(draft);let r,next;try{
    r=await w.restart();await r.queued()._discard();
    for(const key of Object.keys(draft))assert.equal(r.store.draft(conv)[key],draft[key],key);
    r.stop();const host={...w.host,workspaces:{...w.host.workspaces,state:()=>({})}};
    next=new Store(host);await next.start();await next.open({kind:'dm',id:conv});
    for(const key of Object.keys(draft))assert.equal(next.draft(conv)[key],draft[key],'durable restored '+key);
    assert.equal(w.posts.length,1,'restoration and reopening never send');
  }finally{next?.stop();r?.stop();await w.stop();}
});
await check('restart retains original new-topic editor choice rather than submitted topic',async()=>{
  const draft={text:'topic original',newTopic:true,plainReply:true};const w=await world(draft);let r;try{
    r=await w.restart();await r.queued()._discard();for(const key of Object.keys(draft))assert.equal(r.store.draft(conv)[key],draft[key]);
  }finally{r?.stop();await w.stop();}
});
for(const text of ['newer before restart',' Original E\n',''])await check('newer '+JSON.stringify(text)+' survives restart before Cancel',async()=>{
  const w=await world();let r;try{
    w.store.setDraft(conv,{text});r=await w.restart({expectedText:text});await r.queued()._discard();assert.equal(r.store.draft(conv).text,text);
  }finally{r?.stop();await w.stop();}
});
await check('fresh enrolled handle can recover same verified membership after restart',async()=>{
  const w=await world();let r;try{r=await w.restart({handle:'new-restart-mount'});await r.queued()._discard();assert.equal(r.store.draft(conv).text,w.draft.text);}finally{r?.stop();await w.stop();}
});
await check('late Cancel cannot restore after membership disconnect',async()=>{
  const w=await world();try{const held=w.hold(),cancel=w.queued()._discard();w.connected(false);held.release();await cancel;assert.equal(w.store.draft(conv).text,'');}finally{await w.stop();}
});
await check('old in-flight callback cannot restore after registered handle replacement',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();w.replaceBinding({handle:'reconnected-current-mount'});
    assert.equal(w.host.workspace.handle,'owned','captured old host stayed immutable');
    held.release();await cancel;assert.equal(w.store.draft(conv).text,'');
  }finally{await w.stop();}
});
for(const field of ['endpoint','realm'])await check('restart original recovery refuses changed '+field,async()=>{
  const w=await world();let r;try{r=await w.restart({[field]:'another-workspace'});await assert.rejects(r.queued()._discard(),/another conversation or identity/);assert.equal(w.cancels.length,0);assert.equal(w.rows().length,1);}finally{r?.stop();await w.stop();}
});
await check('wrong conversation cannot use original recovery for a Cancel',async()=>{
  const w=await world();try{const wrong=w.store.sends.merge(other,w.rows()).find(m=>m.queued_send);await assert.rejects(wrong._discard,/another conversation/);assert.equal(w.rows().length,1);assert.equal(w.store.draft(conv).text,'');}finally{await w.stop();}
});
// B7: after a restart the File objects are gone. Cancel stops sending and keeps the
// message with its files on this device (held: Retry/Discard), never a cancel that
// strands them, and never a claim that the files came back to the draft.
await check('B7 restart with attachment recovery: Cancel holds the message, files kept, nothing restored',async()=>{
  const file=new File(['retained bytes'],'original.txt');const w=await world({text:'file text',files:[{key:'f',file,name:file.name,size:file.size,type:file.type}]});let r;try{
    r=await w.restart();const id=w.posts[0].id;
    await r.queued()._discard();
    assert.deepEqual(w.holds,[id],'one hold for the exact ID');assert.equal(w.cancels.length,0,'no cancel that clears the file pointers');
    assert.equal(w.rows().length,1,'the message stays in its chat');assert.equal(w.rows()[0].queued_send.state,'needs_review');
    assert.equal(r.store.draft(conv).text,'','nothing restored to the draft');assert.equal((r.store.draft(conv).files||[]).length,0);
    const shown=r.store.sends.merge(conv,w.rows()).find(m=>m.id===id);assert(shown?._failed,'shown stopped');assert(/files are kept on this device/.test(shown.state_text),shown?.state_text);
    assert(typeof shown._retry==='function'&&typeof shown._discard==='function','Retry and Discard offered');
    assert.notEqual(localStorage.getItem('agentnet.messenger.drafts.'+w.host.workspace.id+'.send-recovery.v1.'+id),null,'original text record kept');
    assert.equal(w.posts.length,1,'nothing sent again');
  }finally{r?.stop();await w.stop();}
});

for(const text of ['other-window draft',' Original E\n',''])await check('other-window durable edit '+JSON.stringify(text)+' wins stale Cancel',async()=>{
  const w=await world();let otherStore;try{
    const host={...w.host,workspaces:{...w.host.workspaces,state:()=>({})}};
    otherStore=new Store(host);await otherStore.start();await otherStore.open({kind:'dm',id:conv});otherStore.setDraft(conv,{text});
    await w.queued()._discard();assert.equal(otherStore.draft(conv).text,text);
    assert.equal(JSON.parse(localStorage.getItem('agentnet.messenger.drafts.'+w.host.workspace.id))[conv].text,text,'stale restore never writes base draft');
  }finally{otherStore?.stop();await w.stop();}
});
for(const raw of ['broken',JSON.stringify({version:999})])await check('malformed original metadata refuses Cancel without deleting send: '+raw,async()=>{
  const w=await world();try{
    const key='agentnet.messenger.drafts.'+w.host.workspace.id+'.send-recovery.v1.'+w.posts[0].id;
    localStorage.setItem(key,raw);await assert.rejects(w.queued()._discard(),/damaged|unsupported/);
    assert.equal(w.cancels.length,0);assert.equal(w.rows().length,1);assert.equal(localStorage.getItem(key),raw);
  }finally{await w.stop();}
});
for(const fault of ['original','clear','legacy'])await check(fault+' storage failure cannot clear draft or submit',async()=>{
  const original={text:'  kept before storage failure\n',replyTo:reply,topic,plainReply:true};const write=localStorage.setItem;let w;
  try{
    w=await world(original,'android',false,{expectedPosts:0,beforeSend({host}){
      const base='agentnet.messenger.drafts.'+host.workspace.id;
      if(fault==='legacy'){write(base,'broken');return;}
      localStorage.setItem=(key,value)=>{if(fault==='original'&&key.includes('.send-recovery.v1.') || fault==='clear'&&key===base&&JSON.parse(value)[conv]?.text==='')throw Error('synthetic quota');write(key,value);};
    }});
    for(const key of Object.keys(original))assert.equal(w.store.draft(conv)[key],original[key]);
    assert.equal(w.posts.length,0);assert(w.notices.some(n=>n?.text?.includes('not submitted')||n?.text?.includes('not saved')||n?.text?.includes('kept')));
    if(fault==='legacy')assert.equal(localStorage.getItem('agentnet.messenger.drafts.'+w.host.workspace.id),'broken');
  }finally{localStorage.setItem=write;await w?.stop();}
});

await check('storage event cannot replace unsaved newer input after quota failure',async()=>{
  const w=await world(),write=localStorage.setItem;
  try{
    const base='agentnet.messenger.drafts.'+w.host.workspace.id;
    localStorage.setItem=(key,value)=>{if(key===base)throw Error('synthetic draft quota');write(key,value);};
    w.store.setDraft(conv,{text:'newer input kept only in this window'});
    localStorage.setItem=write;storageEvent(base);
    assert.equal(w.store.draft(conv).text,'newer input kept only in this window','a storage event must not discard input whose persistence failed');
    await w.queued()._discard();assert.equal(w.store.draft(conv).text,'newer input kept only in this window');
  }finally{localStorage.setItem=write;await w.stop();}
});

await check('refused restore then restart: kept original is offered once its chat loads; put back sends nothing',async()=>{
  const w=await world(),write=localStorage.setItem,key='agentnet.messenger.drafts.'+w.host.workspace.id;let r;
  try{
    localStorage.setItem=function(k,v){if(k===key)throw Error('synthetic quota');return write.call(this,k,v);};
    await w.queued()._discard();localStorage.setItem=write;
    assert.equal(w.store.draft(conv).text,w.draft.text,'text shown here although storage refused it');
    r=await w.restart({expectedText:''});
    const kept=r.store.keptOriginals();assert.equal(kept.length,1,'one kept original');assert.equal(kept[0].state,'offer');
    assert.equal(kept[0].draft.text,w.draft.text,'exact original text');
    r.store.setDraft(conv,{text:'newer typed after restart'});
    assert.throws(()=>r.store.putBackKept(kept[0].id,conv),/already a draft/,'never replaces newer input');
    r.store.setDraft(conv,{text:''});r.store.putBackKept(kept[0].id,conv);
    assert.equal(r.store.draft(conv).text,w.draft.text);assert.equal(r.store.keptOriginals().length,0,'released once back');
    assert.equal(w.posts.length,1,'no new send');
  }finally{localStorage.setItem=write;r?.stop();await w.stop();}
});
await check('kept original whose message is stored or still queued is never offered',async()=>{
  const w=await world(),write=localStorage.setItem,key='agentnet.messenger.drafts.'+w.host.workspace.id;let r;
  try{
    r=await w.restart({expectedText:''});
    assert.equal(r.store.keptOriginals().length,0,'queued row holds it');
    r.stored();assert.equal(r.store.keptOriginals().length,0,'stored message holds or released it');
  }finally{localStorage.setItem=write;r?.stop();await w.stop();}
});
await check('legacy queued body still recovers without a stored draft entry',async()=>{
  const w=await world();let r;try{
    w.legacy();localStorage.setItem('agentnet.messenger.drafts.'+w.host.workspace.id,'{}');
    r=await w.restart();const body=r.queued().body;await r.queued()._discard();assert.equal(r.store.draft(conv).text,body,'legacy body recovery remains narrower than original');
  }finally{r?.stop();await w.stop();}
});
await check('failed new preparation cannot acknowledge an older unresolved send record',async()=>{
  const write=localStorage.setItem,id=(serial+1).toString(16).padStart(32,'0');let w;
  try{
    w=await world({text:'existing unresolved'},'android',false,{expectedPosts:0,beforeSend({host}){
      journal.saveBatch(localStorage,host.workspace.id,[{id,conversation:conv,endpoint:'/api/dm/send',request:{id,conv,body:'existing unresolved'},createdAt:'earlier'}]);
      localStorage.setItem=(key,value)=>{if(key.includes('.send-recovery.v1.'))throw Error('synthetic quota');write(key,value);};
    }});
    assert.equal(journal.readPending(localStorage,w.host.workspace.id).length,1);
    assert.equal(journal.readPending(localStorage,w.host.workspace.id)[0].id,id);
    assert.equal(w.store.draft(conv).text,'existing unresolved');assert.equal(w.posts.length,0);
  }finally{localStorage.setItem=write;await w?.stop();}
});

for(const [name,edit,expected] of [
  ['newer different text',w=>w.store.setDraft(conv,{text:'newer draft'}),'newer draft'],
  ['newer same text and changed scope',w=>w.store.setDraft(conv,{text:w.draft.text,topic:'new-topic'}),' Original E\n'],
  ['same empty text is a newer revision',w=>w.store.setDraft(conv,{text:''}),''],
  ['type then erase is a newer revision',w=>{w.store.setDraft(conv,{text:'temporary'});w.store.setDraft(conv,{text:''});},''],
]) await check(name+' is not overwritten',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();edit(w);held.release();await cancel;
    assert.equal(w.store.draft(conv).text,expected);
    if(name.includes('scope'))assert.equal(w.store.draft(conv).topic,'new-topic');
  }finally{await w.stop();}
});

for(const mode of ['failed','uncertain','generic','wrong-id','stored'])await check(mode+' cancellation cannot restore',async()=>{
  const w=await world();try{
    w.mode(mode);
    const cancel=w.queued()._discard();
    if(['failed','uncertain'].includes(mode))await assert.rejects(cancel);else await cancel;
    assert.equal(w.store.draft(conv).text,'');assert.equal(w.posts.length,1);
  }finally{await w.stop();}
});

await check('stored/custody projection wins over a late cancelled reply',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();w.stored();held.release();await cancel;
    assert.equal(w.store.draft(conv).text,'');
  }finally{await w.stop();}
});
await check('custody admission result releases retained draft before late Cancel',async()=>{
  const w=await world();try{
    const row=w.queued(),held=w.hold(),cancel=row._discard();
    w.store.sends.finish(row.id,{id:row.id,state:'custody'});held.release();await cancel;
    assert.equal(w.store.draft(conv).text,'');
    assert.equal(w.scope[Symbol.for('agentnet.ui.pendingSends')].drafts.size,0,'accepted sends retain no draft/file contexts');
  }finally{await w.stop();}
});
await check('explicit failed-message Discard keeps its semantics and frees draft context',async()=>{
  const w=await world();try{
    w.review();await w.queued()._discard();assert.equal(w.store.draft(conv).text,'');
    assert.equal(w.scope[Symbol.for('agentnet.ui.pendingSends')].drafts.size,0);
  }finally{await w.stop();}
});
await check('failed Cancel retains original context for one confirmed explicit Cancel',async()=>{
  const w=await world();try{
    w.mode('failed');await assert.rejects(w.queued()._discard());w.mode('confirmed');await w.queued()._discard();
    assert.equal(w.store.draft(conv).text,w.draft.text);assert.equal(w.posts.length,1);
  }finally{await w.stop();}
});
await check('Stage2N newer draft already present before Cancel is preserved',async()=>{
  const w=await world();try{
    w.store.setDraft(conv,{text:'newer before Cancel',files:[]});await w.queued()._discard();
    assert.equal(w.store.draft(conv).text,'newer before Cancel');assert.equal(w.posts.length,1);
  }finally{await w.stop();}
});
await check('newer attachment without text is not replaced',async()=>{
  const w=await world();try{
    const file=new File(['new file'],'new.txt'),held=w.hold(),cancel=w.queued()._discard();
    w.store.setDraft(conv,{text:'',files:[{key:'new',file,name:file.name,size:file.size,type:file.type}]});
    held.release();await cancel;assert.equal(w.store.draft(conv).text,'');assert.equal(w.store.draft(conv).files[0].file,file);
  }finally{await w.stop();}
});

await check('exact file/reply/topic survive, staging handles and previews do not',async()=>{
  const file=new File(['synthetic original bytes'],'note.txt',{type:'text/plain'}),url=URL.createObjectURL(file);
  const draft={text:'file reply',topic,replyTo:reply,plainReply:true,mediaMode:'original',
    files:[{key:'original-file',file,name:file.name,size:file.size,type:file.type,url}]};
  const w=await world(draft);try{
    await w.queued()._discard();const restored=w.store.draft(conv);
    for(const key of ['text','topic','replyTo','plainReply','mediaMode'])assert.equal(restored[key],draft[key],key);
    assert.equal(restored.files.length,1);assert.equal(restored.files[0].file,file);
    assert.equal(restored.files[0].key,'original-file');assert.equal(restored.files[0].staged,undefined);
    assert(restored.files[0].url && restored.files[0].url!==url,'fresh owned preview after send revoked the old one');
    assert.equal(w.stages.length,1);assert.equal(w.posts.length,1);
    URL.revokeObjectURL(restored.files[0].url);
  }finally{await w.stop();}
});
await check('late Android admission cleanup cannot clear restored text/files',async()=>{
  const file=new File(['held original bytes'],'held.txt',{type:'text/plain'});
  const w=await world({text:'held original',files:[{key:'held',file,name:file.name,size:file.size,type:file.type}]},'android',true);
  try{
    await w.queued()._discard();assert.equal(w.store.draft(conv).text,'held original');
    await w.finishAdmission();assert.equal(w.store.draft(conv).text,'held original');
    assert.equal(w.store.draft(conv).files[0].file,file);assert.equal(w.store.draft(conv).files[0].staged,undefined);
    assert.equal(w.posts.length,1);
  }finally{await w.stop();}
});

await check('navigation restores only originating conversation, without navigation',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();await w.store.open({kind:'dm',id:other});
    w.store.setDraft(other,{text:'other conversation draft'});held.release();await cancel;
    assert.equal(w.store.get().open.id,other);assert.equal(w.store.draft(other).text,'other conversation draft');
    assert.equal(w.store.draft(conv).text,w.draft.text);
  }finally{await w.stop();}
});
for(const change of ['workspace','handle','identity'])await check(change+' change rejects stale restoration',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();
    if(change==='workspace')w.host.workspace.id+='-other';
    if(change==='handle')w.host.workspace.handle='new-handle';
    if(change==='identity'){w.me.fingerprint='other-person';await w.store.refetch();}
    held.release();await cancel;assert.equal(w.store.draft(conv).text,'');
  }finally{await w.stop();}
});
await check('unmounted renderer writes only its unchanged retained draft',async()=>{
  const w=await world();try{
    const held=w.hold(),cancel=w.queued()._discard();w.store.stop();held.release();await cancel;
    assert.equal(w.store.isActive(),false);assert.equal(w.store.draft(conv).text,'','stopped live state stays stopped');
    assert.equal(w.scope.comicDrafts.drafts[conv].text,w.draft.text);
  }finally{await w.stop();}
});
for(const newer of [false,true])await check('replacement Store '+(newer?'preserves newer revision':'restores original draft'),async()=>{
  const w=await world();let replacement;try{
    const held=w.hold(),cancel=w.queued()._discard();w.store.stop();replacement=new Store(w.host);await replacement.start();
    if(newer)replacement.setDraft(conv,{text:'replacement draft'});
    held.release();await cancel;assert.equal(replacement.draft(conv).text,newer?'replacement draft':w.draft.text);
    assert.equal(w.store.isActive(),false);
  }finally{if(replacement)replacement.stop();await w.stop();}
});
// Review findings 2026-11-07 (F1, F2+F8, F4): reproduced before their repair.
await check('F1 Android: typing while files prepare keeps the new text, clears the sent file and reply',async()=>{
  const file=new File(['b'],'a.txt');
  const w=await world({text:'hi',replyTo:reply,files:[{key:'f',file,name:file.name,size:file.size,type:file.type}]},'android',false,{beforeSend({store,host}){
    const stage=host.stage;host.stage=async f=>{store.setDraft(conv,{...store.draft(conv),text:'hi!'});return stage(f);};}});
  try{const d=w.store.draft(conv);assert.equal(d.text,'hi!','newer typing kept');assert.equal(d.files?.length||0,0,'sent file left the composer');assert.equal(d.replyTo,undefined,'sent reply left the composer');}
  finally{await w.stop();}
});
await check('F2/F8 web Retry of a failed new-topic send submits the same ID, keeps the bubble, and Cancel still restores',async()=>{
  const w=await world({text:'new topic text',newTopic:true},'daemon',false,{failPosts:1});
  try{
    const failed=w.store.sends.merge(conv,[]).find(m=>m._failed);assert(failed,'failed bubble kept');
    failed._retry();for(let i=0;i<6;i++)await turn();
    assert(!w.notices.some(n=>n?.text?.includes('another original draft')),'Retry refused: '+JSON.stringify(w.notices));
    assert.equal(w.posts.length,2,'Retry submitted');assert.equal(w.posts[1].id,w.posts[0].id,'same send ID');
    await w.queued()._discard();assert.equal(w.store.draft(conv).text,'new topic text','Cancel after Retry restores the original');
  }finally{await w.stop();}
});
await check('F4 a Cancel restoration shows on remount with its files, and on a cold load before identity loads',async()=>{
  const file=new File(['b'],'keep.txt');
  const w=await world({text:'restored text',files:[{key:'f',file,name:file.name,size:file.size,type:file.type}]},'daemon');let warm;
  try{
    await w.queued()._discard();assert.equal(w.store.draft(conv).text,'restored text');assert.equal(w.store.draft(conv).files?.length,1);
    w.store.stop();
    warm=new Store(w.host);assert.equal(warm.draft(conv).text,'restored text','same-session remount');assert.equal(warm.draft(conv).files?.length,1,'remount keeps restored files');
    await warm.start();await warm.open({kind:'dm',id:conv});assert.equal(warm.draft(conv).files?.length,1,'files kept after refresh');warm.stop();warm=undefined;
    const fresh={},h={...w.host,workspaces:{...w.host.workspaces,state:()=>fresh}};
    const cold=new Store(h);assert.equal(cold.draft(conv).text,'restored text','cold load shows the restoration before identity loads');
  }finally{warm?.stop();await w.stop();}
});
await check('F7 only the older-version copy lags: a put-back save and a Cancel restoration show as saved',async()=>{
  const write=localStorage.setItem;const w=await world({text:'lag original'},'daemon');try{
    const base='agentnet.messenger.drafts.'+w.host.workspace.id;let overlaid=false;
    localStorage.setItem=(k,v)=>{if(k.includes('.restored.v1.'))overlaid=true;if(k===base&&overlaid)throw Error('synthetic quota');write(k,v);};
    await w.queued()._discard();
    assert.equal(w.store.draft(conv).text,'lag original','restored text shown');
    const toasts=w.store.get().toasts.map(t=>t.text).join(' | ');
    assert(!/could not be saved/.test(toasts),'restoration was saved, not refused: '+toasts);
    assert.equal(localStorage.getItem(base+'.send-recovery.v1.'+w.posts[0].id),null,'restored original released, not kept twice');
    w.store.setDraft(conv,{text:''});overlaid=true;
    w.store.saveDraftNow(conv,{text:'put back'});assert.equal(w.store.draft(conv).text,'put back','saved text shown');
  }finally{localStorage.setItem=write;await w.stop();}
});
await check('B7 before the exact held receipt nothing looks stopped; a late receipt never masks newer host state',async()=>{
  const file=new File(['retained bytes'],'original.txt');
  for(const later of ['none','sent','retried']){
    const w=await world({text:'file text',files:[{key:'f',file,name:file.name,size:file.size,type:file.type}]});let r;try{
      r=await w.restart();const id=w.posts[0].id,gateH=later==='retried'?w.holdAnswerLate():w.holdHeld();
      const pending=r.queued()._discard();await turn();
      let shown=r.store.sends.merge(conv,w.rows()).find(m=>m.id===id);
      if(later==='retried'){assert(shown._failed&&/kept on this device/.test(shown.state_text),'the host already held it: its own row shows that');}
      else{assert.equal(shown.state_text,'Stopping…','local change at once');assert(!shown._failed,'not shown stopped before the receipt');
        assert.equal(shown._discard,undefined,'no stopped-state controls before the receipt');}
      if(later==='sent')w.setRows(rows=>rows.map(({queued_send,...m})=>({...m,state:'custody'})));
      if(later==='retried')w.setRows(rows=>rows.map(m=>({...m,detail:undefined,queued_send:{state:'pending',rev:5}}))); // retried elsewhere after the host held it
      if(later!=='none')r.store.sends.merge(conv,w.rows());
      gateH.release();
      if(later==='sent')await assert.rejects(pending,/already sent/);else await pending;
      shown=r.store.sends.merge(conv,w.rows()).find(m=>m.id===id);
      if(later==='none'){assert(shown._failed&&/files are kept on this device/.test(shown.state_text),'confirmed held');}
      else if(later==='sent'){assert(!shown?.queued_send&&!shown?._failed,'stored message shown as sent, not held: '+JSON.stringify(shown?.state_text));}
      else {assert(!shown._failed&&shown.state_text!=='Stopping…'&&!/kept on this device/.test(shown.state_text),'newer host revision not masked: '+shown.state_text);}
    }finally{r?.stop();await w.stop();}
  }
});
await check('B6 a draft an older version replaced is offered in Saved messages; Put back only into an empty composer',async()=>{
  const w=await world({text:'sent'},'daemon');let next;try{
    const base='agentnet.messenger.drafts.'+w.host.workspace.id;
    w.store.setDraft(other,{text:'mine',replyTo:reply});w.store.stop();
    localStorage.setItem(base,'{}'); // an older version, this page closed, writes its map
    const fresh={},h={...w.host,workspaces:{...w.host.workspaces,state:()=>fresh}};
    next=new Store(h);await next.start();await next.open({kind:'dm',id:conv});next.setDraft(conv,{text:'typed after'});
    assert.equal(next.draft(other).text,'','the composer follows the older version');
    await next.open({kind:'dm',id:other});
    const kept=next.keptOriginals().filter(k=>k.replaced);assert.equal(kept.length,1);assert.deepEqual(kept[0].draft,{text:'mine',replyTo:reply});assert.equal(kept[0].state,'offer');
    next.setDraft(other,{text:'newer'});assert.throws(()=>next.putBackKept(kept[0].id,other),/already a draft/);assert.equal(next.draft(other).text,'newer','newer input never overwritten');
    next.setDraft(other,{text:''});next.putBackKept(kept[0].id,other);
    assert.equal(next.draft(other).text,'mine');assert.equal(next.draft(other).replyTo,reply);assert.equal(next.keptOriginals().filter(k=>k.replaced).length,0,'released once put back');
    assert.equal(w.posts.length,1,'nothing sent');
  }finally{next?.stop();await w.stop();}
});
// Delta review round 2 (R1-R5): reproduced before their repair.
await check('R1 every older-tab write is recorded, so a reload after its second write keeps this page\'s draft',async()=>{
  const w=await world({text:'sent'},'daemon');try{
    const base='agentnet.messenger.drafts.'+w.host.workspace.id;
    w.store.setDraft(other,{text:'mine'});
    localStorage.setItem(base,JSON.stringify({y:{text:'old 1'}}));storageEvent(base);
    localStorage.setItem(base,JSON.stringify({y:{text:'old 2'}}));storageEvent(base);
    w.store.stop();const fresh={},h={...w.host,workspaces:{...w.host.workspaces,state:()=>fresh}};
    const reloaded=new Store(h);await reloaded.start();assert.equal(reloaded.draft(other).text,'mine');reloaded.setDraft('z'.repeat(64),{text:'z'});
    assert.equal(new Store(h).draft(other).text,'mine','kept after a later write');reloaded.stop();
  }finally{await w.stop();}
});
await check('R3 Android: adding a file while files prepare still clears the sent text',async()=>{
  const a=new File(['a'],'a.jpg'),b=new File(['b'],'b.jpg');
  const w=await world({text:'hi',files:[{key:'a',file:a,name:a.name,size:a.size,type:a.type}]},'android',false,{beforeSend({store,host}){
    const stage=host.stage;host.stage=async f=>{const d=store.draft(conv);store.setDraft(conv,{...d,files:[...(d.files||[]),{key:'b',file:b,name:b.name,size:b.size,type:b.type}]});return stage(f);};}});
  try{const d=w.store.draft(conv);assert.equal(d.text,'','sent text left the composer');assert.deepEqual((d.files||[]).map(f=>f.key),['b'],'new file kept, sent file gone');}
  finally{await w.stop();}
});
await check('R4 Retry after a draft revision storage did not take still resubmits',async()=>{
  const write=localStorage.setItem;const w=await world({text:'retry me'},'daemon',false,{failPosts:1});try{
    const failed=w.store.sends.merge(conv,[]).find(m=>m._failed);assert(failed,'failed bubble kept');
    localStorage.setItem=()=>{throw Error('synthetic quota');};w.store.setDraft(conv,{text:'typed while full'});localStorage.setItem=write;
    failed._retry();for(let i=0;i<6;i++)await turn();
    assert.equal(w.posts.length,2,'Retry submitted: '+JSON.stringify(w.notices));
  }finally{localStorage.setItem=write;await w.stop();}
});
await check('R5 put-back save never reports success after its record was lost',async()=>{
  const write=localStorage.setItem;const w=await world({text:'sent'},'daemon');try{
    const base='agentnet.messenger.drafts.'+w.host.workspace.id,rec=base+'.draft.v1.'+other;let phase=0;
    localStorage.setItem=(k,v)=>{
      if(phase===0&&k===rec){write(k,v);phase=1;return;}
      if(phase===1&&k===base){phase=2;throw Error('synthetic quota');}
      if(phase===2&&k===base)throw Error('synthetic quota');
      if(phase===2&&k===rec){write(base,'{}');phase=3;throw Error('synthetic quota');}
      write(k,v);};
    let threw=false;try{w.store.saveDraftNow(other,{text:'put back'});}catch{threw=true;}
    localStorage.setItem=write;
    const stored=(()=>{try{return JSON.parse(localStorage.getItem(rec)||'null')?.text;}catch{return null;}})();
    assert(threw || stored==='put back','returned normally but storage lost the text (stored '+JSON.stringify(stored)+')');
  }finally{localStorage.setItem=write;await w.stop();}
});
console.log(JSON.stringify({required:passed,executed:passed,pass:passed,fail:0,skip:0,
  scope:'actual sendAndroid + journal + Store + pendingSends, synthetic host ACKs; not rendered/native qualification'}));
