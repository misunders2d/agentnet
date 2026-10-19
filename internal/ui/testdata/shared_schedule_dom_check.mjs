import assert from 'node:assert/strict';
import {mountSchedule} from '../skins/shared/schedule.mjs';
import {scheduleSaves} from '../skins/shared/schedule-saves.mjs';
import {captureSchedule} from '../skins/shared/schedule-model.mjs';

// Same supplied-ownerDocument harness convention as shared_video_file_check.
class Events {
 listeners=new Map();
 addEventListener(k,f){const set=this.listeners.get(k)||new Set();set.add(f);this.listeners.set(k,set);}
 removeEventListener(k,f){this.listeners.get(k)?.delete(f);}
 fire(k,extra={}){const e={type:k,target:this,currentTarget:this,preventDefault(){this.defaultPrevented=true;},...extra};this['on'+k]?.(e);for(const f of [...(this.listeners.get(k)||[])])f(e);return e;}
}
class Element extends Events {
 constructor(tag,doc){super();this.tagName=tag.toUpperCase();this.ownerDocument=doc;this.children=[];this.attributes=new Map();this.style={};this.textContent='';this.disabled=false;this.value='';this.open=false;}
 get isConnected(){return this.connected??this.parentNode?.isConnected??false;}
 append(...nodes){for(const n of nodes){n.remove();n.parentNode=this;this.children.push(n);}}
 replaceChildren(...nodes){for(const n of [...this.children])n.remove();this.append(...nodes);}
 remove(){if(this.parentNode){this.parentNode.children=this.parentNode.children.filter(n=>n!==this);this.parentNode=null;}}
 setAttribute(k,v){this.attributes.set(k,String(v));}
 getAttribute(k){return this.attributes.get(k)??null;}
 click(){if(!this.disabled)this.fire('click');}
 showModal(){assert(this.isConnected);this.open=true;this.modalCalls=(this.modalCalls||0)+1;}
 close(){this.open=false;this.fire('close');}
}
const all=n=>[n,...n.children.flatMap(all)],words=n=>[n.textContent,...n.children.map(words)].join(' ');
const tick=async()=>{for(let i=0;i<10;i++)await new Promise(r=>setImmediate(r));};
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return{promise,resolve,reject};};
const saved=new Map();let denied=false;
globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>{if(denied)throw Error('denied');saved.set(k,v);},removeItem:k=>{if(denied)throw Error('denied');saved.delete(k);}};
const conv='a'.repeat(64),other='b'.repeat(64);let serial=0,checks=0;
function fixture(options={}){
 const doc=new Events(),win=new Events();win.crypto=globalThis.crypto;doc.defaultView=win;doc.createElement=tag=>new Element(tag,doc);
 const root=doc.createElement('section');root.connected=true;const sentinel=doc.createElement('p');sentinel.textContent='Entry-owned';root.append(sentinel);
 const file=options.file||new File(['original actual bytes'],'draft.txt');
 const original={text:'[@Casey](agentnet:person/abc) captured encoded body',topic:'topic',replyTo:'reply',files:options.noFiles?[]:[{key:'one',name:file.name,size:file.size,file}]};
 const drafts=options.drafts||new Map([[conv,original],[other,{text:'other chat kept',files:[]}]]),state=options.state||{};
 const id=options.id||'dom-'+(++serial),calls=[],stages=[],discards=[],clears=[],blocks=[],pushes=new Set(),handles=new Map(),rows=options.rows||new Map(),canonical=new Map();let captures=0,off=0,current=true,lose=options.lose,lookup404=false,conflict=false,audience={persons:['approved-person'],pids:[],author_pid:'author'};
 const ctx={supported:true,conv,ordinary:true,...options.context};
 const host=options.host||{platform:options.platform||'daemon',workspace:{id,handle:'h'},workspaces:{state:()=>state}};
 host.stage=async actual=>{
  stages.push(actual);if(options.stage)return options.stage(actual,stages.length);
  const h=options.opaque?{workspace:id,name:actual.name,size:actual.size,arrayBuffer:()=>actual.arrayBuffer()}:'stage-'+stages.length;handles.set(h,actual);return h;
 };
 host.api=async(path,body)=>{
  calls.push({path,body});const url=new URL(path,'https://owned.invalid');
  if(url.pathname==='/api/schedule/audience')return options.audience?options.audience():structuredClone(audience);
  if(url.pathname==='/api/upload/discard'){discards.push(body.ids);for(const h of body.ids)handles.delete(h);return{};}
  if(url.pathname==='/api/schedules'){
   if(url.searchParams.has('id')){if(lookup404)throw Error('404 PRIVATE');const row=rows.get(url.searchParams.get('id'));if(!row)throw Error('404');return structuredClone(row);}
   return options.list?options.list():structuredClone([...rows.values()]);
  }
  if(url.pathname==='/api/schedule/create'){
   if(options.create)return options.create(body);
   const files=[];for(const h of body.files||[]){const actual=handles.get(h);assert(actual,'single-use actual staged handle');handles.delete(h);files.push({name:actual.name,size:actual.size,bytes:[...new Uint8Array(await actual.arrayBuffer())]});}
   const signature=JSON.stringify({...body,files});if(canonical.has(body.id))assert.equal(signature,canonical.get(body.id),'same-ID payload/file bytes and audience stay immutable');
   else{canonical.set(body.id,signature);rows.set(body.id,{...body,rev:1,state:'pending',files:files.map(f=>({name:f.name,size:f.size,sha256:'descriptor-not-proof'}))});}
   if(lose){lose=false;throw Error('lost PRIVATE response');}return structuredClone(rows.get(body.id));
  }
  if(['/api/schedule/edit','/api/schedule/cancel','/api/schedule/send_now'].includes(url.pathname)){
   const row=rows.get(body.id);assert(row);assert.equal(body.rev,row.rev);
   if(conflict){conflict=false;row.rev++;throw Error('PRIVATE remote revision');}
   if(url.pathname.endsWith('/edit'))Object.assign(row,body);
   else row.state=url.pathname.endsWith('/cancel')?'cancelled':'sent';row.rev++;return structuredClone(row);
  }
  throw Error('UNAUTHORIZED ROUTE '+path);
 };
 const controller=mountSchedule({root,host,owner:options.owner||'skin.a',context:()=>ctx,capture:c=>{assert.equal(c,ctx.conv);captures++;const d=drafts.get(c)||{text:'',files:[]};return {body:d.text,draft:d};},getDraft:c=>drafts.get(c)||{text:'',files:[]},setDraft:(c,d)=>{clears.push({conv:c,draft:d});drafts.set(c,d);options.onSetDraft?.();},audienceText:a=>[...(a.persons||[]),...(a.pids||[])].join(', ')||'No recipients',subscribe:f=>{pushes.add(f);return()=>{off++;pushes.delete(f);};},isCurrent:()=>current,onBlockingChange:b=>blocks.push(b)});
 const button=(name,within=root)=>{const b=all(within).find(n=>n.tagName==='BUTTON'&&(n.textContent===name||n.getAttribute('aria-label')===name));assert(b,'missing button '+name+' in '+words(within));return b;};
 const click=async name=>{const b=button(name);assert(!b.disabled,name+' disabled');b.click();await tick();};
 const input=(label,value)=>{const n=all(root).find(n=>['INPUT','TEXTAREA'].includes(n.tagName)&&n.getAttribute('aria-label')===label);assert(n,'missing field '+label);if(value!==undefined){n.value=value;n.fire('input');}return n;};
 const stop=()=>{controller.destroy();controller.destroy();assert.deepEqual(root.children,[sentinel]);assert.equal(off,1);assert.equal(pushes.size,0);assert.equal([...doc.listeners.values(),...win.listeners.values()].reduce((n,s)=>n+s.size,0),0);};
 return{root,doc,win,file,original,drafts,state,host,controller,calls,stages,discards,clears,blocks,rows,ctx,button,click,input,stop,push:()=>{for(const f of pushes)f();},setAudience:a=>{audience=a;},set404:v=>{lookup404=v;},setConflict:()=>{conflict=true;},setCurrent:v=>{current=v;},get captures(){return captures;}};
}
const count=(f,path)=>f.calls.filter(c=>c.path.startsWith(path)).length;
const row=(state='pending',id='1'.repeat(32),rev=1)=>({id,conv,topic:'topic',body:'scheduled text',reply_to:'reply',quote:'reply',due:new Date(Date.now()+3600000).toISOString(),timezone:'UTC',rev,state,audience:{persons:['approved-person'],pids:[]},files:[{name:'kept.txt',size:10,sha256:'not-proof'}],detail:'PRIVATE BACKEND LABEL /local/path'});
const ambient=['window','document','fetch','IntersectionObserver','MutationObserver'];const descriptors=ambient.map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]);
for(const key of ambient)Object.defineProperty(globalThis,key,{configurable:true,get(){throw Error('ambient '+key+' used');}});
try{
 // Distinct actions; native modal, explicit local/UTC time and verified ceiling.
 {
  const f=fixture();assert.equal(f.calls.length,0);assert.equal(f.captures,0);assert.deepEqual(f.blocks,[false]);f.push();await tick();assert.equal(f.calls.length,0);
  await f.click('Schedule message…');const dialog=all(f.root).find(n=>n.tagName==='DIALOG');assert(dialog.open);assert.equal(dialog.modalCalls,1);assert.equal(f.input('Send date').type,'date');assert.equal(f.input('Send time').type,'time');assert.match(words(f.root),/UTC[+−]\d\d:\d\d/);assert(words(f.root).includes('approved-person'));assert.equal(count(f,'/api/schedule/create'),0);
  f.input('Send date','2000-01-01');f.input('Send time','00:00');await f.click('Save schedule');assert.equal(count(f,'/api/schedule/create'),0);assert(words(f.root).includes('future'));assert.equal(f.drafts.get(conv),f.original);
  f.controller.close();assert(!dialog.isConnected);assert(!dialog.open);f.push();await tick();assert.equal(count(f,'/api/schedules?'),0);f.stop();checks++;
 }
 // The actual lost-response path re-stages the SAME File, ID, encoded text,
 // reply/quote and approved audience, preserving newer typing and attachments.
 for(const opaque of [false,true]){
  const f=fixture({lose:true,opaque});await f.click('Schedule message…');await f.click('Save schedule');assert.equal(f.rows.size,1);assert.equal(f.clears.length,0);assert(f.controller.blocking);assert(words(f.root).includes('Unconfirmed save'));assert(!words(f.root).includes('PRIVATE'));assert.equal(count(f,'/api/schedules?'),0,'no automatic GET');
  const first=f.calls.find(c=>c.path==='/api/schedule/create').body,extra=new File(['new'],'new.txt');
  f.drafts.set(conv,{...f.original,text:'new typing stays',replyTo:'new reply',files:[...f.original.files,{key:'two',name:extra.name,size:extra.size,file:extra}]});f.setAudience({persons:['newly-added'],pids:[]});
  await f.click('Check saved state');assert.equal(f.clears.length,0);assert(f.controller.blocking);assert(words(f.root).includes('lookup alone cannot verify'));
  await f.click('Retry same save');const posts=f.calls.filter(c=>c.path==='/api/schedule/create');assert.equal(posts.length,2);assert.equal(posts[0].body.id,posts[1].body.id);assert.notEqual(posts[0].body.files[0],posts[1].body.files[0]);assert.deepEqual(f.stages,[f.file,f.file]);assert.deepEqual(posts[1].body.audience.persons,['approved-person']);assert.equal(posts[1].body.reply_to,'reply');assert.equal(posts[1].body.quote,'reply');assert.equal(posts[1].body.topic,'topic');assert.equal(posts[1].body.body,first.body);assert.equal(f.rows.size,1);assert.equal(f.clears.length,1);assert.equal(f.drafts.get(conv).text,'new typing stays');assert.equal(f.drafts.get(conv).replyTo,'new reply');assert.deepEqual(f.drafts.get(conv).files.map(x=>x.file),[extra]);assert.equal(f.drafts.get(other).text,'other chat kept');assert.equal(f.controller.blocking,false);assert.deepEqual(f.blocks,[false,true,false]);assert(![...saved.values()].some(v=>v.includes(first.id)));f.stop();checks++;
 }
 // Same-name/size replacement Files and a changed topic never get cleared.
 for(const patch of ['file','topic']){
  const held=deferred(),f=fixture({create:()=>held.promise});await f.click('Schedule message…');f.button('Save schedule').click();await tick();const body=f.calls.find(c=>c.path==='/api/schedule/create').body;
  const replacement=new File(['replacement same size'],'draft.txt');const changed=patch==='file'?{...f.original,files:[{...f.original.files[0],file:replacement}]}:{...f.original,topic:'another topic'};f.drafts.set(conv,changed);held.resolve({...row(),id:body.id});await tick();assert.equal(f.clears.length,1);if(patch==='file')assert.equal(f.drafts.get(conv).files[0].file,replacement);else assert.equal(f.drafts.get(conv),changed);f.stop();checks++;
 }
 // Every ineligible target has a readable disabled action and zero API calls.
 for(const context of [{supported:false},{ordinary:false},{followup:true},{frozen:true},{visitor:true},{newTopic:true},{missing:true},{conv:''}]){
  const f=fixture({context});assert(f.button('Schedule message…').disabled);assert(f.button('Schedule message…').title);f.button('Schedule message…').click();await f.controller.openCreate();assert.equal(f.calls.length,0);f.stop();checks++;
 }
 {
  const f=fixture({platform:'android'});assert(f.button('Schedule message…').disabled);assert(words(f.root).includes('Android'));assert.equal(f.calls.length,0);f.stop();checks++;
  const text=fixture({platform:'android',noFiles:true});await text.click('Schedule message…');assert(words(text.root).includes('Mobile background'));text.stop();checks++;
  const browser=fixture({platform:'browser',noFiles:true});await browser.click('Schedule message…');assert(words(browser.root).includes('browser page must stay open'));browser.stop();checks++;
 }
 // A foreign in-memory attachment intent is also unavailable on Android even
 // when this skin's current draft is text-only (the original Files are retained).
 {
  const state={},host={platform:'android',workspace:{id:'android-original',handle:'h'},workspaces:{state:()=>state}},file=new File(['original'],'original.txt');
  const attempt=captureSchedule('d'.repeat(32),conv,'old file',{text:'old file',files:[{key:'original',name:file.name,size:file.size,file}]},new Date(Date.now()+3600000).toISOString(),'UTC',{persons:[],pids:[]});
  const ledger=scheduleSaves(host,()=>{},'foreign.skin');ledger.begin(conv,attempt);ledger.settle(conv,attempt.payload.id,'uncertain');ledger.dispose();
  const f=fixture({host,state,noFiles:true});await f.click('Schedule message…');assert(f.button('Retry same save').disabled);assert(words(f.root).includes('file bridge is qualified'));assert.equal(f.stages.length,0);assert.equal(count(f,'/api/schedule/create'),0);assert.equal(f.controller.blocking,true);f.stop();checks++;
 }
 // Existing cold token makes no request, permits self-reload, but blocks new ID.
 for(const found of [false,true]){
  const ws='cold-dom-'+(++serial),r=row(),key='agentnet.messenger.schedule-save.'+ws;const raw=JSON.stringify({v:1,conv:{[conv]:{id:r.id,conv,files:[{name:'kept.txt',size:10}]}}});saved.set(key,raw);
  const f=fixture({id:ws,rows:found?new Map([[r.id,r]]):undefined});assert.equal(f.calls.length,0);assert.equal(f.controller.blocking,false);await f.click('Schedule message…');assert.equal(f.captures,0);assert.equal(count(f,'/api/schedule/audience?'),0);assert(!all(f.root).some(n=>n.textContent==='Save schedule'));
  await f.click('Check saved state');assert.equal(f.clears.length,0);assert.equal(saved.get(key),raw);assert.equal(count(f,'/api/schedule/create'),0);
  if(found){assert(words(f.root).includes('names and sizes do not prove'));await f.click('Keep draft and finish review');assert.equal(f.drafts.get(conv),f.original);assert(!saved.has(key));}
  else{assert(words(f.root).includes('may still be saved'));await f.click('Forget…');assert(words(f.root).includes('It may already be scheduled'));await f.click('Forget unconfirmed save');assert(!saved.has(key));assert.equal(f.clears.length,0);assert.equal(count(f,'/api/schedule/create'),0);}
  f.stop();checks++;
 }
 // GET 404 after an in-memory ambiguous response retains intent, token and ID.
 {
  const f=fixture({lose:true});await f.click('Schedule message…');await f.click('Save schedule');const before=saved.get('agentnet.messenger.schedule-save.'+f.host.workspace.id);f.set404(true);await f.click('Check saved state');assert.equal(saved.get('agentnet.messenger.schedule-save.'+f.host.workspace.id),before);assert(f.controller.blocking);assert.equal(f.clears.length,0);assert.equal(count(f,'/api/schedule/create'),1);f.stop();checks++;
 }
 // Denied localStorage keeps originals in memory and exposes the reload guard.
 {
  denied=true;const f=fixture({lose:true});await f.click('Schedule message…');await f.click('Save schedule');assert(f.controller.blocking);assert(words(f.root).includes('won’t be remembered'));await f.click('Retry same save');assert.equal(f.clears.length,1);assert.equal(f.controller.blocking,false);f.stop();denied=false;checks++;
 }
 // Revision actions retain scope; only a separately confirmed audience changes it.
 {
  const r=row(),f=fixture({rows:new Map([[r.id,r]])});await f.click('Scheduled messages');assert.equal(count(f,'/api/schedule/audience?'),0);await f.click('Edit schedule');f.input('Scheduled message text','edited text');await f.click('Save schedule changes');let edit=f.calls.filter(c=>c.path==='/api/schedule/edit').at(-1).body;assert.equal(edit.rev,1);assert(!('files'in edit));assert(!('audience'in edit));assert.equal(edit.topic,'topic');
  await f.click('Edit schedule');f.setAudience({persons:['approved-person','new-person'],pids:[]});await f.click('Review audience');assert(words(f.root).includes('Confirm reviewed audience'));assert(!words(f.root).includes('New audience explicitly confirmed'));await f.click('Confirm reviewed audience');assert(words(f.root).includes('New audience explicitly confirmed'));await f.click('Save schedule changes');edit=f.calls.filter(c=>c.path==='/api/schedule/edit').at(-1).body;assert.deepEqual(edit.audience.persons,['approved-person','new-person']);assert.equal(edit.rev,2);
  f.setConflict();await f.click('Cancel schedule');assert(words(f.root).includes('revision changed'));assert.equal(f.rows.get(r.id).state,'pending');assert.equal(count(f,'/api/schedule/create'),0);await f.click('Send now');const now=f.calls.find(c=>c.path==='/api/schedule/send_now').body;assert.deepEqual(now,{id:r.id,rev:4});assert(words(f.root).includes('Queued to send'));assert(!words(f.root).includes('Delivered'));assert(!words(f.root).includes('PRIVATE'));f.stop();checks++;
 }
 // Ordinary host pushes must not discard a time edit or replace its CAS base.
 {
  const r=row(),f=fixture({rows:new Map([[r.id,r]])});await f.click('Scheduled messages');await f.click('Edit schedule');
  const changed='2030-10-20T14:45';f.input('Reschedule date','2030-10-20');const time=f.input('Reschedule time','14:45');f.input('Scheduled message text','typing during a push');
  f.push();await tick();assert.equal(f.input('Reschedule date').value,'2030-10-20');assert.equal(f.input('Reschedule time'),time,'a push must not replace the focused native input');assert.equal(time.value,'14:45');assert.equal(f.input('Scheduled message text').value,'typing during a push');
  await f.click('Save schedule changes');const edit=f.calls.filter(c=>c.path==='/api/schedule/edit').at(-1).body;
  assert.equal(edit.due,new Date(changed).toISOString());assert.equal(edit.rev,1);assert.equal(edit.body,'typing during a push');assert(!('files'in edit));assert(!('audience'in edit));f.stop();checks++;
 }
 // A genuinely newer record must not silently promote the editor's revision.
 {
  const r=row(),f=fixture({rows:new Map([[r.id,r]])});await f.click('Scheduled messages');await f.click('Edit schedule');f.input('Reschedule date','2030-10-20');f.input('Reschedule time','18:30');
  f.rows.get(r.id).rev=9;f.rows.get(r.id).body='saved elsewhere';f.push();await tick();await f.click('Save schedule changes');
  assert.equal(f.calls.filter(c=>c.path==='/api/schedule/edit').at(-1).body.rev,1);assert.equal(f.rows.get(r.id).body,'saved elsewhere');assert(all(f.root).some(n=>n.getAttribute('role')==='status'));
  await f.click('Edit schedule');await f.click('Keep saved schedule');assert(!all(f.root).some(n=>n.getAttribute('aria-label')==='Reschedule time'));assert(words(f.root).includes('saved elsewhere'));f.stop();checks++;
 }
 // Terminal/dispatching states are read-only; only an open list follows pushes.
 {
  const rs=new Map(['sent','cancelled','dispatching','needs_review','pending'].map((s,i)=>{const r=row(s,(i+1).toString().repeat(32));return[r.id,r];})),f=fixture({rows:rs});await f.click('Scheduled messages');
  const articles=all(f.root).filter(n=>n.tagName==='ARTICLE');assert.equal(articles.length,5);for(const a of articles.slice(0,3))assert.equal(all(a).filter(n=>n.tagName==='BUTTON').length,0);assert.equal(all(f.root).filter(n=>n.textContent==='Edit schedule').length,2);assert(!words(f.root).includes('PRIVATE'));
  const before=count(f,'/api/schedules?');f.push();await tick();assert.equal(count(f,'/api/schedules?'),before+1);f.controller.close();f.push();await tick();assert.equal(count(f,'/api/schedules?'),before+1);assert.equal(all(f.root).filter(n=>n.tagName==='ARTICLE').length,0);f.stop();checks++;
 }
 // Late result after destroy touches only the holder. Next same-owner mount
 // consumes it once, with no GET, navigation, normal send or draft clearing in old UI.
 {
  const held=deferred(),f=fixture({create:()=>held.promise});await f.click('Schedule message…');f.button('Save schedule').click();await tick();const payload=f.calls.find(c=>c.path==='/api/schedule/create').body;f.stop();const calls=f.calls.length;held.resolve({id:payload.id});await tick();assert.equal(f.clears.length,0);assert.equal(f.calls.length,calls);assert.equal(f.drafts.get(conv),f.original);
  const next=fixture({host:{...f.host},state:f.state,drafts:f.drafts});assert.equal(next.clears.length,1);assert.equal(next.drafts.get(conv).text,'');assert.equal(next.calls.length,0);next.controller.update();assert.equal(next.clears.length,1);next.stop();checks++;
 }
 // Late A result while B is mounted cannot clear B; owner survives recovery.
 {
  const held=deferred(),f=fixture({create:()=>held.promise,owner:'skin.a'});await f.click('Schedule message…');f.button('Save schedule').click();await tick();const payload=f.calls.find(c=>c.path==='/api/schedule/create').body;f.stop();
  const foreign=fixture({host:{...f.host},state:f.state,owner:'skin.b'});held.resolve({id:payload.id});await tick();assert.equal(foreign.clears.length,0);assert.equal(foreign.drafts.get(conv),foreign.original);await foreign.click('Schedule message…');assert(words(foreign.root).includes('creating skin'));assert.equal(count(foreign,'/api/schedule/audience?'),0);foreign.stop();
  const own=fixture({host:{...f.host},state:f.state,owner:'skin.a',drafts:f.drafts});assert.equal(own.clears.length,1);own.stop();checks++;
 }
 // Entries may replace their view synchronously after a guarded draft clear.
 // That stopped renderer must not emit a later status callback or fetch a list.
 {
  let stop;const f=fixture({onSetDraft:()=>stop()});stop=()=>f.controller.destroy();await f.click('Schedule message…');await f.click('Save schedule');assert.equal(f.clears.length,1);assert.deepEqual(f.blocks,[false,true]);assert.equal(count(f,'/api/schedules?'),0);f.stop();checks++;
 }
 // Partial stage failure and a view change during staging never POST a schedule.
 {
  const f=fixture({stage:async(_file,n)=>{if(n===2)throw Error('PRIVATE stage');return 'partial';}}),two=new File(['two'],'two.txt');f.drafts.set(conv,{...f.original,files:[...f.original.files,{key:'two',name:two.name,size:two.size,file:two}]});await f.click('Schedule message…');await f.click('Save schedule');assert.deepEqual(f.discards,[['partial']]);assert.equal(count(f,'/api/schedule/create'),0);assert.equal(f.clears.length,0);assert(!words(f.root).includes('PRIVATE'));f.stop();checks++;
  const held=deferred(),late=fixture({stage:()=>held.promise});await late.click('Schedule message…');late.button('Save schedule').click();await tick();late.stop();held.resolve('late-stage');await tick();assert.equal(count(late,'/api/schedule/create'),0);assert.deepEqual(late.discards,[['late-stage']]);assert.equal(late.clears.length,0);checks++;
 }
 // Handle and view fences discard late previews and prevent commands in another chat.
 {
  const held=deferred(),f=fixture({audience:()=>held.promise});f.button('Schedule message…').click();await tick();f.ctx.conv=other;f.controller.update();held.resolve({persons:['PRIVATE-LATE'],pids:[]});await tick();assert.equal(all(f.root).filter(n=>n.tagName==='DIALOG').length,0);assert(!words(f.root).includes('PRIVATE-LATE'));assert.equal(count(f,'/api/schedule/create'),0);f.stop();checks++;
  const hidden=fixture();hidden.host.workspace.handle='changed';await hidden.controller.openList();assert.equal(hidden.calls.length,0);hidden.stop();checks++;
 }
 // Close/Escape teardown is native and isolated to this supplied entry root.
 {
  const f=fixture();await f.click('Schedule message…');const dialog=all(f.root).find(n=>n.tagName==='DIALOG');const e=dialog.fire('cancel');assert(e.defaultPrevented);assert(!dialog.open);assert(!dialog.isConnected);assert.equal(f.root.children.length,2);f.stop();checks++;
 }
 console.log('PASS shared schedule DOM: '+checks+' groups; immutable actual Files/opaque handles, lost response same-ID, current draft/owner fences, cold/404/denied recovery, explicit audience/revision actions, read-only states, closed push and late teardown; zero ordinary Send');
}finally{for(const [key,descriptor]of descriptors){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key];}}
