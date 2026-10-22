// Source-driven reaction/control regression. Synthetic local host, real Store,
// message components, CSS and Base UI; no generated production assets changed.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {createRequire}=require('node:module'),{execFileSync}=require('node:child_process');
const webDir=path.resolve(__dirname,'../web'),web=createRequire(path.join(webDir,'package.json'));
const engines=require(process.env.AGENTNET_PLAYWRIGHT),engine=process.env.AGENTNET_INTERACTION_ENGINE||'chromium';
const work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-interaction-'));
const entry=`
import React from 'react';import{createRoot}from'react-dom/client';
import{Store,useStore}from'./src/store';import{StoreContext}from'./src/context';import{OwnedContext}from'./src/owned';import{MessageView}from'./src/features/Message';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const conv='c'.repeat(64),overview={me:{address:'alice/laptop'},person:{person:'alice',address:'alice/laptop',label:'Alice',state:'self',devices:[]},people:[{person:'bob',address:'bob/laptop',label:'Bob'}],reminders:[],needs_you:[],seq:1};
const msg=(i)=>({id:String(i).padStart(32,'0'),lid:String(i).padStart(32,'0'),dir:'in',from:'bob/laptop',kind:'message',body:'Exact message '+i+' '+('A wrapping line of message text. '.repeat(i%2?3:18)),at:new Date().toISOString(),can:['react'],actions:[],reactions:[],state:'delivered'});
let view={id:conv,peer:overview.people[0],messages:Array.from({length:24},(_,i)=>msg(i+1)),can_send:true,agents:[],guests:[]};
const fixture=window.fixture={calls:[],timings:{},hold:false,offline:false,emit(){for(const fn of listeners)fn({type:'change',seq:2});}};
const listeners=new Set();const host={version:1,platform:'daemon',workspace:{id:'interaction',state:'enrolled'},listen(fn){listeners.add(fn);return()=>listeners.delete(fn)},async api(p,b){
 fixture.calls.push({p,b,at:performance.now()});
 if(p.startsWith('/api/dm?')){const result=structuredClone(view);fixture.timings.projectionStarted=performance.now();if(fixture.hold){fixture.hold=false;await new Promise(r=>fixture.release=r);}fixture.timings.projectionReturned=performance.now();return result;}
 if(p==='/api/storage/retry_send'){await new Promise(r=>setTimeout(r,30));view.messages.at(-1).detail='';view.messages.at(-1).state='queued';fixture.emit();return{id:b.id,retried:1};}
 if(p==='/api/message/react'){fixture.timings.actionStarted=performance.now();if(fixture.offline)throw Error('Reaction could not be stored. Try again when connected.');const m=view.messages.find(m=>m.id===b.id);if(!m)throw Error('Wrong target');m.reactions=b.remove?[]:[{emoji:b.emoji,mine:true,by:[{id:'alice',label:'Alice'}]}];fixture.timings.durable=performance.now();fixture.emit();return{note:'Stored'};}
 if(p.startsWith('/api/overview'))return structuredClone(overview);if(p.startsWith('/api/typing'))return{entries:[],preferences:{send:false,show:false}};if(p.startsWith('/api/agents'))return{agents:[]};return{};
}};
const store=new Store(host);store.alive=true;store.set({open:{kind:'dm',id:conv},dm:structuredClone(view),overview,conn:'live'});store.listen();fixture.store=store;
fixture.stale=()=>{fixture.hold=true;void store.refreshOpen();};
fixture.failure=async(code,admin)=>{overview.role=admin?'admin':'member';const m=view.messages.at(-1);Object.assign(m,{dir:'out',from:'alice/laptop',state:'failed',detail:'send_error:'+code+': Actual producer reason.',state_text:'Not sent'});await store.refreshOpen();};
fixture.many=()=>{view={...view,messages:Array.from({length:400},(_,i)=>msg(i+1))};void store.refreshOpen();};
function App(){const dm=useStore(store,s=>s.dm);const ctx={conv,dm,overview,canReply:true,names:{}};return <>{dm.messages.map(m=><MessageView key={m.id} m={m} ctx={ctx} all={dm.messages}/>)}<div aria-live="polite">{useStore(store,s=>s.toasts).map(t=><p key={t.id}>{t.text}</p>)}</div></>}
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(webDir,'src'),path.join(work,'src'),{recursive:true});
if(process.env.INTERACTION_BASELINE)for(const f of['store.ts','context.tsx','features/Message.tsx','features/Message.reactions.tsx','features/Message.actions.tsx'])fs.writeFileSync(path.join(work,'src',f),execFileSync('git',['show','HEAD:internal/ui/web/src/'+f],{cwd:webDir}));
for(const[k,v]of Object.entries(aliases))fs.copyFileSync(path.resolve(webDir,'../',v),path.join(work,'src',k));
fs.symlinkSync(path.join(webDir,'node_modules'),path.join(work,'node_modules'),'dir');
const result=web('esbuild').buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false,logLevel:'silent'});
const bundle=result.outputFiles[0].text;
const cssFile=path.join(work,'style.css');execFileSync(path.join(webDir,'node_modules/.bin/tailwindcss'),['-i',path.join(work,'src/styles.css'),'-o',cssFile,'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(cssFile,'utf8')+'\nbody{margin:0}.an-root{height:100vh}.an-app{height:100%;overflow:auto;padding:60px 24px}';
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://local');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');return res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentNet interaction fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root" data-theme="'+(u.searchParams.get('theme')||'light')+'"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/fixture.mjs"></script>');}if(u.pathname==='/fixture.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(bundle);}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css);}res.writeHead(404).end();});
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await (engine==='webkit'?engines.webkit.launch({headless:process.env.AGENTNET_INTERACTION_HEADED!=='1'}):engines.chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']}));
 const page=await browser.newPage({viewport:{width:1440,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.text());});await page.goto('http://127.0.0.1:'+server.address().port);await page.locator('[data-mid]').last().waitFor();
 const report={engine};
 if(process.env.AGENTNET_INTERACTION_HEADED==='1'){
  // The macOS runner exercises a real WebKit window and fullscreen state.
  // Its normal and half-width cases below keep the same target assertions.
  await page.locator('[data-mid]').last().scrollIntoViewIfNeeded();
  await page.mouse.click(5,5);
  await page.evaluate(()=>document.documentElement.requestFullscreen());
  await page.waitForFunction(()=>!!document.fullscreenElement);
  const fullRow=page.locator('[data-mid]').last();await fullRow.scrollIntoViewIfNeeded();await fullRow.hover();
  await fullRow.getByRole('button',{name:'More actions',exact:true}).click();
  await page.getByRole('menuitem',{name:'Copy text',exact:true}).waitFor();await page.keyboard.press('Escape');
  report.fullscreen=true;await page.evaluate(()=>document.fullscreenElement?document.exitFullscreen():undefined);
  await page.setViewportSize({width:1440,height:900});
 }
 let row=page.locator('[data-mid]').last();await row.scrollIntoViewIfNeeded();await row.hover();await row.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Copy text',exact:true}).waitFor();await page.keyboard.press('Escape');
 await row.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Details',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();await page.getByRole('dialog').waitFor({state:'detached'});const neighbour=page.locator('[data-mid]').nth(22);await neighbour.hover();await page.waitForTimeout(200);report.staleToolbarOpacity=await row.getByRole('button',{name:'More actions',exact:true}).evaluate(b=>getComputedStyle(b.parentElement).opacity);if(process.env.INTERACTION_GEOMETRY_ONLY){console.log(JSON.stringify(report));return;}if(!process.env.INTERACTION_BASELINE)assert.equal(Number(report.staleToolbarOpacity),0,'mouse-dismissed older toolbar persists above hovered message');await row.hover();await row.getByRole('button',{name:'More actions',exact:true}).focus();await page.keyboard.press('Enter');await page.getByRole('menuitem',{name:'Details',exact:true}).waitFor();await page.keyboard.press('Escape');
 report.toolbar=await row.evaluate(el=>{const b=el.querySelector('[data-message-bubble]').getBoundingClientRect(),t=el.querySelector('[aria-label="More actions"]').getBoundingClientRect();return{bubble:{top:b.top,bottom:b.bottom,left:b.left,right:b.right},control:{top:t.top,bottom:t.bottom,left:t.left,right:t.right}};});
 assert(report.toolbar.control.bottom<=report.toolbar.bubble.bottom+2&&report.toolbar.control.bottom>=report.toolbar.bubble.bottom-18,'toolbar is vertically attached to exact bubble');
 await page.setViewportSize({width:800,height:800});await row.hover();await page.waitForTimeout(80);report.halfWidthControls=await row.getByRole('button',{name:'More actions',exact:true}).count();
 if(process.env.INTERACTION_BASELINE){await page.setViewportSize({width:1440,height:900});await page.waitForTimeout(100);await row.hover();}
 if(!process.env.INTERACTION_BASELINE)assert.equal(report.halfWidthControls,1,'mouse controls remain available below desktop layout breakpoint');
 await row.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole(process.env.INTERACTION_BASELINE?'menuitem':'button',{name:'Copy text',exact:true}).waitFor();await page.keyboard.press('Escape');
 await page.setViewportSize({width:1440,height:900});await page.waitForTimeout(100);
 for(const count of[24,400]){if(count===400){await page.evaluate(()=>fixture.many());await page.waitForFunction(()=>fixture.store.get().dm.messages.length===400);}row=page.locator('[data-mid]').last();await row.scrollIntoViewIfNeeded();await page.mouse.move(2,2);await row.hover();
 await page.evaluate(baseline=>{fixture.stale();if(baseline)setTimeout(()=>fixture.release(),1200);},!!process.env.INTERACTION_BASELINE);await row.getByRole('button',{name:'React',exact:true}).click();const quick=page.getByRole('button',{name:'React 👍',exact:true});await quick.waitFor();await page.evaluate(()=>fixture.timings.selected=performance.now());await quick.click();
 await row.locator('[aria-pressed="true"]').waitFor({timeout:1500});const timing=await page.evaluate(()=>({...fixture.timings,paint:performance.now()}));report['reaction'+count]={api:timing.durable-timing.selected,paint:timing.paint-timing.selected};
 await page.evaluate(()=>fixture.release());await row.locator('[aria-pressed="true"]').click();await row.locator('[aria-pressed="true"]').waitFor({state:'detached'});
 }
 await page.evaluate(()=>fixture.offline=true);await row.hover();await row.getByRole('button',{name:'React',exact:true}).click();await page.getByRole('button',{name:'React 👍',exact:true}).click();await page.getByText('Reaction could not be stored. Try again when connected.',{exact:true}).waitFor();assert.equal(await row.locator('[aria-pressed="true"]').count(),0);
 await page.evaluate(()=>{fixture.offline=false;document.querySelector('#root').setAttribute('data-theme','dark')});await row.hover();
 for(const admin of[false,true]){await page.evaluate(admin=>fixture.failure('relay_quota',admin),admin);const error=row.locator('[data-send-error="relay_quota"]');await error.waitFor();assert.equal(await error.getByRole('button',{name:'Open Storage',exact:true}).count(),admin?1:0);assert(!await row.evaluate(el=>el.textContent.includes('send_error:')),'producer code shown raw');assert.equal((await error.innerText()).split('The workspace attachment quota is full.').length-1,1,'reason repeated');await error.getByRole('button',{name:'Retry this message',exact:true}).evaluate(b=>{b.click();b.click()});await error.waitFor({state:'detached'});}
 const retries=await page.evaluate(()=>fixture.calls.filter(c=>c.p==='/api/storage/retry_send'));assert.equal(retries.length,2,'one exact retry per action, no double click');for(const r of retries){assert.equal(r.b.conv,'c'.repeat(64));assert.equal(r.b.id,(400).toString().padStart(32,'0'));}
 for(const code of['file_too_large','relay_disk_full','local_disk_full','network_unavailable']){await page.evaluate(code=>fixture.failure(code,false),code);await row.locator('[data-send-error="'+code+'"]').waitFor();assert(!await row.evaluate(el=>el.textContent.includes('send_error:')));}
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(80);await row.scrollIntoViewIfNeeded();await row.click({button:'right'});await page.getByRole('dialog').waitFor();await page.keyboard.press('Escape');assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'narrow mouse layout overflow');
 await page.screenshot({path:process.env.INTERACTION_EVIDENCE||path.join(work,'interaction.png')});assert.deepEqual(errors,[]);console.log(JSON.stringify({ok:true,...report,pageErrors:errors}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1;});
