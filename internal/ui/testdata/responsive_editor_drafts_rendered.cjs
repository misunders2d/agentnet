// Actual ChatList -> openChat -> Store -> Timeline, public Go DTO seed.
// Only the invented Host's stored rows change; no firstUnread is injected.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http'),{execFileSync}=require('node:child_process');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-normal-row-'));
const evidence=process.env.AGENTNET_NORMAL_ROW_EVIDENCE;if(evidence)fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const seed=fs.readFileSync(process.argv[2]);
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
for(const[a,b]of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');fs.copyFileSync(path.join(__dirname,'responsive_editor_drafts.tsx'),path.join(work,'harness.tsx'));
// Expose the real instance only when the ordinary row calls it. The wrapper
// forwards the untouched producer argument and result; no state is seeded.
const entry=`import {Store} from './src/store';export {mount} from './src/main';export {mountEditorHarness} from './harness';
const open=Store.prototype.openChat;Store.prototype.openChat=function(...args){window.fixture.store=this;return open.apply(this,args);};`;
const bundle=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false,logLevel:'silent'}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const boot=`
import {mount,mountEditorHarness} from '/entry.mjs';
const form=new URL(location).searchParams.get('form');if(form){mountEditorHarness(await(await fetch('/seed.json')).json(),form);}else{
const query=new URL(location).searchParams,kind=query.get('kind'),count=kind==='group'?200:60;
const seed=await(await fetch('/seed.json')).json(),overview=structuredClone(seed.overview),view=structuredClone(seed.view),peer=view.peer,me=overview.person;
const id=kind==='thread'?'d'.repeat(32):'c'.repeat(64),binding={agent_host:peer.address,agent_key:'22222222-22222222-22222222-22222222',agent_id:'5'.repeat(32)};
view.id=id;view.kind=kind==='group'?'group':'dm';view.title=kind==='group'?'Normal unread group':'';view.role='member';view.agents=[];view.guests=[];view.topics=[];view.members=[me,peer];
view.messages=[{id:'a'.repeat(32),lid:'a'.repeat(32),dir:'out',from:overview.me.address,to:peer.address,kind:'message',body:'Original editable message',at:new Date().toISOString(),unread:false,state:'stored',can:['edit'],actions:[],reactions:[]}];
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
await mount(document.getElementById('root'),host);window.ready=true;}
`;
const server=http.createServer((q,r)=>{const u=new URL(q.url,'http://fixture');if(u.pathname==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(bundle);}if(u.pathname==='/boot.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(boot);}if(u.pathname==='/seed.json'){r.setHeader('Content-Type','application/json');return r.end(seed);}if(u.pathname==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css);}r.setHeader('Content-Type','text/html');r.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body,#root{height:100%;margin:0}</style><link rel="stylesheet" href="/style.css"><div id="root"></div><script type="module" src="/boot.mjs"></script>');});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:960}});page.setDefaultTimeout(5000);const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 await page.goto('http://127.0.0.1:'+server.address().port+'/?kind=dm');await page.waitForFunction(()=>window.ready&&!!document.querySelector('[data-part="chat-row"]'));
 await page.locator('[data-part="chat-row"]').first().click();await page.locator('[data-mid]').waitFor();
 const edit=async()=>{const row=page.locator('[data-mid]').first();await row.hover();await row.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Edit',exact:true}).or(page.getByRole('button',{name:'Edit',exact:true})).click();await page.getByRole('textbox',{name:'New text',exact:true}).waitFor();};
 await edit();await page.getByRole('textbox',{name:'New text',exact:true}).fill('Retain this exact typed draft');
 const values=[{width:1440,value:await page.getByRole('textbox',{name:'New text',exact:true}).inputValue()}];
 for(const width of [390,1440]){await page.setViewportSize({width,height:960});await page.waitForFunction(()=>!document.querySelector('[aria-label="New text"]'));await edit();values.push({width,value:await page.getByRole('textbox',{name:'New text',exact:true}).inputValue()});}
 const result={values,errors,apiWrites:await page.evaluate(()=>fixture.calls.filter(c=>['/api/edit','/api/send'].includes(c.p)))};console.log(JSON.stringify(result));if(evidence)fs.writeFileSync(path.join(evidence,'causal-green.json'),JSON.stringify(result,null,2));
 reports.push({form:'full-app-edit',...result});
 assert.equal(values[1].value,'Retain this exact typed draft','restored crossing narrow');assert.equal(values[2].value,'Retain this exact typed draft','restored returning wide');assert.deepEqual(errors,[]);
 await page.close();
 for(const form of ['edit','answer','decline','report','schedule','assign','default','primitive']){
  const page=await browser.newPage({viewport:{width:1440,height:960}}),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  const start=async()=>{await page.goto('http://127.0.0.1:'+server.address().port+'/?form='+form);await page.waitForFunction(()=>window.ready&&!!window.editorFixture);};
  const open=async()=>{
   await page.getByRole('button',{name:'Open editor',exact:true}).click();
   if(form==='report'){const title=page.getByRole('button',{name:/Reports from other computers/});if(await title.getAttribute('aria-expanded')!=='true')await title.click();await page.getByRole('button',{name:'Answer there',exact:true}).click();}
   if(form==='schedule')await page.getByRole('button',{name:'Edit schedule',exact:true}).click();
   if(form==='assign'){await page.waitForFunction(()=>document.querySelector('select')?.options.length>1);await page.getByRole('combobox',{name:'Your agent',exact:true}).selectOption({index:1});}
  };
  const field=()=>form==='default'?page.getByRole('radio',{name:/Claude/}):form==='decline'?page.locator('#decline-reason'):form==='answer'?page.locator('#agent-answer'):form==='report'?page.locator('#operator-text'):page.getByRole('textbox',{name:{edit:'New text',schedule:'Scheduled message text',assign:'What should it do?',primitive:'Primitive text'}[form],exact:true});
  const original={edit:'Original @Alice',answer:'',decline:'',report:'',schedule:'Original scheduled body',assign:'Handle the request in this message.',primitive:'Original primitive'}[form];
  const draft='Typed retained '+form+(form==='edit'?' @Alice':'');
  const write=async(text=draft)=>{if(form==='default'){await field().check();await page.getByRole('button',{name:'Change folder: Works in',exact:true}).click();await page.getByRole('button',{name:'Use “selected”',exact:true}).click();}else await field().fill(text);};
  const verify=async(expected=draft)=>{if(form==='default'){assert(await field().isChecked(),'default choice retained');assert.match(await page.getByRole('group',{name:'Works in',exact:true}).innerText(),/\/invented\/selected/);}else assert.equal(await field().inputValue(),expected);};
  const verifyOriginal=async()=>{if(form==='default'){assert(await page.getByRole('radio',{name:/Codex/}).isChecked());assert.match(await page.getByRole('group',{name:'Works in',exact:true}).innerText(),/\/invented\/original/);}else assert.equal(await field().inputValue(),original);};
  const cancel=async()=>{if(['answer','assign'].includes(form))await page.getByRole('button',{name:'Close',exact:true}).click();else await page.getByRole('button',{name:form==='schedule'?'Keep saved schedule':form==='primitive'?'Discard':'Cancel',exact:true}).click();};
  const resize=async width=>{await page.setViewportSize({width,height:960});await page.locator(width<1024?'#root .an-app > aside':'#root .an-app > section').waitFor();await open();};
  try{
   await start();await open();const parentRenders=await page.evaluate(()=>editorFixture.mounts);await write();assert.equal(await page.evaluate(()=>editorFixture.mounts),parentRenders,'typing does not rerender unrelated parent');await resize(390);await verify();await resize(1440);await verify();
   await cancel();if(form!=='primitive')await open();await verifyOriginal();
   assert.deepEqual(await page.evaluate(()=>editorFixture.calls),[],'reopen/resize/cancel never submits');
   await write();
   // Same Store, another exact target/revision; then another captured Store.
   for(const [fieldName,value]of [['target','b'],['attempt',2],['workspace','two']]){
    await page.evaluate(([k,v])=>editorFixture.change(k,v),[fieldName,value]);await open();await verifyOriginal();await write('Other '+fieldName);
   }
   await page.evaluate(()=>{editorFixture.workspace='one';editorFixture.target='a';editorFixture.change('attempt',1);});await open();await verify();
   // Do not execute a real task: fixture sends are refused or held/confirmed.
   if(form==='primitive'){
    await page.getByRole('button',{name:'Capture confirmation',exact:true}).click();await resize(390);await verify();await write(draft+' newer');await write(draft);
    await page.evaluate(()=>editorFixture.resolve());await verify();await resize(1440);await verify();
    await page.getByRole('button',{name:'Capture confirmation',exact:true}).click();await page.evaluate(()=>editorFixture.resolve());await page.waitForFunction(()=>document.querySelector('[aria-label="Primitive text"]')?.value==='Original primitive');await verifyOriginal();
    await write();await page.getByRole('button',{name:'Capture confirmation',exact:true}).click();await page.evaluate(()=>editorFixture.change('target','c'));await open();await write('New target draft');await page.evaluate(()=>editorFixture.resolve());await verify('New target draft');
   }else{
    const submit=()=>page.getByRole('button',{name:{edit:'Save',answer:'Send answer',decline:'Decline',report:'Answer there',schedule:'Save schedule changes',assign:'Send task',default:'Save'}[form],exact:true});
    await page.evaluate(()=>editorFixture.mode='failure');await submit().click();await page.waitForFunction(()=>editorFixture.calls.length>0);
    if(['answer','edit','assign','default'].includes(form)){await page.waitForFunction(()=>!document.querySelector('button:disabled')||!Array.from(document.querySelectorAll('button')).some(b=>/Sending|Saving/.test(b.textContent)));await resize(390);}else{await resize(390);}
    await verify();await page.evaluate(()=>editorFixture.mode='success');await submit().click();
    if(form==='schedule')await page.getByRole('button',{name:'Edit schedule',exact:true}).waitFor();
    else if(['edit','answer','assign','default'].includes(form))await field().waitFor({state:'hidden'});
    else await page.getByRole('dialog').waitFor({state:'hidden'});
    await resize(1440);await verifyOriginal();
   }
   if(form==='edit')assert((await page.evaluate(()=>editorFixture.calls)).every(c=>c.b.text.endsWith('[@Alice](agentnet:person/alice)')),'unchanged mention retains exact reference');
   assert.deepEqual(errors,[]);const report={form,restoredBothDirections:true,cancelClears:true,targetAttemptWorkspaceIsolated:true,failedRetained:form!=='primitive',confirmedClears:true,lateSameStringGuard:form==='primitive',pageErrors:errors,calls:await page.evaluate(()=>editorFixture.calls)};reports.push(report);console.log(JSON.stringify({ok:true,form}));
  }catch(error){if(evidence){await page.screenshot({path:path.join(evidence,'failure-'+form+'.png')});fs.writeFileSync(path.join(evidence,'failure-'+form+'.html'),await page.content());}throw error;}finally{await page.close();}
 }
 if(evidence)fs.writeFileSync(path.join(evidence,'results.json'),JSON.stringify(reports,null,2));
}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1;});
