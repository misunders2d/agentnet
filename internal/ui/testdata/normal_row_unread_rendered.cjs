// Actual ChatList -> openChat -> Store -> Timeline, public Go DTO seed.
// Only the invented Host's stored rows change; no firstUnread is injected.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http'),{execFileSync}=require('node:child_process');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-normal-row-'));
const evidence=process.env.AGENTNET_NORMAL_ROW_EVIDENCE;if(evidence)fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const seed=fs.readFileSync(process.argv[2]);
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
for(const[a,b]of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
// Expose the real instance only when the ordinary row calls it. The wrapper
// forwards the untouched producer argument and result; no state is seeded.
const entry=`import {Store} from './src/store';export {mount} from './src/main';
const open=Store.prototype.openChat;Store.prototype.openChat=function(...args){window.fixture.store=this;return open.apply(this,args);};`;
const bundle=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false,logLevel:'silent'}).outputFiles[0].text;
execFileSync(path.join(web,'node_modules/.bin/tailwindcss'),['-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const boot=`
import {mount} from '/entry.mjs';
const query=new URL(location).searchParams,kind=query.get('kind'),count=kind==='group'?200:60;
const seed=await(await fetch('/seed.json')).json(),overview=structuredClone(seed.overview),view=structuredClone(seed.view),peer=view.peer,me=overview.person;
const id=kind==='thread'?'d'.repeat(32):'c'.repeat(64),binding={agent_host:peer.address,agent_key:'22222222-22222222-22222222-22222222',agent_id:'5'.repeat(32)};
view.id=id;view.kind=kind==='group'?'group':'dm';view.title=kind==='group'?'Normal unread group':'';view.role='member';view.agents=[];view.guests=[];view.topics=[];view.members=[me,peer];
view.messages=Array.from({length:count},(_,i)=>({id:(i+1).toString(16).padStart(32,'0'),lid:(i+1).toString(16).padStart(32,'0'),dir:'in',from:peer.address,to:overview.me.address,from_key:binding.agent_key,kind:'message',body:'Unread burst '+(i+1),at:new Date(Date.now()-(count-i)*60000).toISOString(),unread:true,state:'stored',actions:[],can:[],reactions:[]}));
view.main_topic={id:'',conv:id,count,unread:count,state:'active'};
overview.demo=false;overview.topic_list=true;overview.review=[];overview.needs_you=[];overview.held=[];overview.quarantine=[];overview.reminders=[];overview.threads=[];overview.topics=[];
overview.dms=[{...overview.dms[0],id,peer,kind:view.kind,title:view.title,last_id:view.messages.at(-1).lid,last:view.messages.at(-1).body,unread:count,count}];
if(kind==='thread'){
 peer.fingerprint=binding.agent_key;peer.devices=[{address:peer.address,fingerprint:peer.fingerprint,human:true}];view.peer=peer.address;overview.people=overview.people.map(p=>p.person===peer.person?peer:p);overview.agent_devices=[peer.address];overview.dms=[];
 view.messages[0].id=view.messages[0].lid=id;view.topic={id,peer:peer.address,...binding,title:'Normal device agent',last:'Unread burst '+count,last_at:view.messages.at(-1).at,count,unread:count,state:'active'};view.key={pinned:peer.fingerprint};
 overview.threads=[view.topic];overview.topics=[{peer:peer.address,...binding,total:1,archived:0,latest:view.topic}];
}
const calls=[],listeners=new Set();
const box=()=>document.querySelector('[data-part="timeline"]')?.parentElement;
const visible=id=>{const row=document.querySelector('[data-mid="'+id+'"]'),b=box()?.getBoundingClientRect(),r=row?.getBoundingClientRect();return!!b&&!!r&&!row.closest('[inert]')&&r.bottom>b.top&&r.top<b.bottom&&r.right>b.left&&r.left<b.right;};
const fixture=window.fixture={view,overview,calls,kind,visible,emit(){for(const fn of listeners)fn({type:'change',seq:++overview.seq});}};
const host={version:1,platform:'daemon',workspace:{id:'normal-row',name:'Invented unread',state:'enrolled'},listen(fn){listeners.add(fn);return()=>listeners.delete(fn);},onOpen(fn){fixture.navigate=fn;},onSkinsChange(){return()=>{};},skins:[],async api(p,b){
 const call={p,b:structuredClone(b)};if(p==='/api/act'&&b?.do==='read')call.visibleAtWrite=b.ids.filter(visible);calls.push(call);
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')||p.startsWith('/api/thread?'))return structuredClone(view);
 if(p==='/api/act'&&b?.do==='read'){for(const m of view.messages)if(b.ids.includes(m.id))m.unread=false;view.main_topic.unread=view.messages.filter(m=>m.unread).length;if(kind==='thread')view.topic.unread=view.main_topic.unread;else overview.dms[0].unread=view.main_topic.unread;fixture.emit();return{};}
 if(p.startsWith('/api/agents'))return{host:peer.address,local:false,agents:kind==='thread'?[{record:{id:binding.agent_id,host:peer.address,host_key:binding.agent_key,label:'Normal device agent',harness:'pi'},enabled:true}]:[]};
 if(p.startsWith('/api/typing'))return{entries:[],preferences:{send:false,show:false}};
 return{};
}};
await mount(document.getElementById('root'),host);window.ready=true;
`;
const server=http.createServer((q,r)=>{const u=new URL(q.url,'http://fixture');if(u.pathname==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(bundle);}if(u.pathname==='/boot.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(boot);}if(u.pathname==='/seed.json'){r.setHeader('Content-Type','application/json');return r.end(seed);}if(u.pathname==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css);}r.setHeader('Content-Type','text/html');r.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body,#root{height:100%;margin:0}</style><link rel="stylesheet" href="/style.css"><div id="root"></div><script type="module" src="/boot.mjs"></script>');});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 for(const width of [1440,390])for(const kind of ['dm','group','thread']){
  const context=await browser.newContext({viewport:{width,height:844}}),page=await context.newPage(),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  try{
   await page.goto('http://127.0.0.1:'+server.address().port+'/?kind='+kind);await page.waitForFunction(()=>window.ready&&!!document.querySelector('[data-part="chat-row"]'));
   assert.equal(await page.locator('[data-mid]').count(),0,'false before: row not opened');
   const row=page.locator('[data-part="chat-row"]').first();await row.hover();await page.waitForFunction(()=>fixture.calls.some(c=>/\/api\/(dm|thread)\?/.test(c.p)));
   assert.equal(await page.evaluate(()=>fixture.calls.filter(c=>c.b?.do==='read').length),0,'hover prefetch reads no rows');
   await row.click();await page.waitForFunction(()=>fixture.visible(fixture.view.messages[0].id)&&!!document.querySelector('[aria-label="Unread messages"]'));
   await page.waitForFunction(()=>fixture.view.messages[0].unread===false);
   const initial=await page.evaluate(()=>({mounted:document.querySelectorAll('[data-mid]').length,first:fixture.view.messages[0].id,visit:fixture.store.get().readVisit,open:fixture.store.get().open,tailUnread:fixture.view.messages.at(-1).unread,calls:fixture.calls}));
   assert.equal(initial.visit.firstUnread,initial.first);assert.equal(initial.open.focus,undefined);assert(initial.tailUnread,'unseen tail survives');assert(initial.mounted<=24,'initial group bound preserved');
   if(kind==='group'){
    const later=page.getByRole('button',{name:'Later messages',exact:true});await later.scrollIntoViewIfNeeded();
    initial.pagerGeometry=await later.evaluate(el=>{const r=el.getBoundingClientRect(),latest=[...el.closest('[data-part="conversation"]').querySelectorAll('button')].find(b=>b.textContent.trim()==='Latest'),s=latest.getBoundingClientRect(),hit=el.getRootNode().elementFromPoint(r.left+r.width/2,r.top+r.height/2);return{later:r.toJSON(),latest:s.toJSON(),overlap:Math.max(0,Math.min(r.right,s.right)-Math.max(r.left,s.left))*Math.max(0,Math.min(r.bottom,s.bottom)-Math.max(r.top,s.top)),centerHit:hit?.closest('button')===el};});
    assert.equal(initial.pagerGeometry.overlap,0,'inline Later and floating Latest do not overlap');assert(initial.pagerGeometry.centerHit,'actual pointer center reaches Later');
    await later.click();await page.waitForFunction(before=>document.querySelectorAll('[data-mid]').length>before,initial.mounted);
    const pointerPage=await page.locator('[data-mid]').count();assert.equal(pointerPage-initial.mounted,80,'pointer Later adds exactly one80-group page');
    assert(await page.evaluate(()=>fixture.view.messages.at(-1).unread),'pointer paging retains unseen tail');
    await later.focus();await page.keyboard.press('Enter');await page.waitForFunction(before=>document.querySelectorAll('[data-mid]').length>before,pointerPage);
    const keyboardPage=await page.locator('[data-mid]').count();assert.equal(keyboardPage-pointerPage,80,'keyboard Enter adds exactly one80-group page');
    assert(await page.evaluate(()=>fixture.view.messages.at(-1).unread),'keyboard paging retains unseen tail');initial.laterMounted=keyboardPage;initial.paging={pointerPage,keyboardPage};
   }
   if(kind!=='thread'){
    await page.evaluate(()=>fixture.navigate(fixture.view.messages.at(-1).id,'message',{conv:fixture.view.id,dir:'in'}));
    await page.waitForFunction(()=>fixture.visible(fixture.view.messages.at(-1).id));
    assert.equal(await page.evaluate(()=>fixture.store.get().open.focus),kind==='group'?(200).toString(16).padStart(32,'0'):(60).toString(16).padStart(32,'0'),'valid exact notification retains its target');
   }
   const reads=await page.evaluate(()=>fixture.calls.filter(c=>c.b?.do==='read'));
   for(const c of reads)assert.deepEqual(c.b.ids,c.visibleAtWrite,'only actual visible rows are acknowledged');
   assert.deepEqual(errors,[]);const result={kind,width,oldestVisible:true,divider:true,firstUnread:initial.visit.firstUnread,initialMounted:initial.mounted,laterMounted:initial.laterMounted,paging:initial.paging,pagerGeometry:initial.pagerGeometry,unseenTailRetained:true,reads,open:initial.open,pageErrors:errors};reports.push(result);console.log(JSON.stringify({ok:true,...result}));
   if(evidence)await page.screenshot({path:path.join(evidence,kind+'-'+width+'.png')});
  }catch(e){console.error(JSON.stringify({kind,width,pageErrors:errors,selected:await page.evaluate(()=>({open:fixture.store?.get().open,readVisit:fixture.store?.get().readVisit,calls:fixture.calls})).catch(()=>null)}));if(evidence){await page.screenshot({path:path.join(evidence,'failure-'+kind+'-'+width+'.png')}).catch(()=>{});fs.writeFileSync(path.join(evidence,'failure-'+kind+'-'+width+'.html'),await page.content());}throw e;}finally{await context.close();}
 }
 if(evidence)fs.writeFileSync(path.join(evidence,'results.json'),JSON.stringify(reports,null,2));
}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1;});
