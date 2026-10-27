// Actual profile rename: immediate presentation, retained recovery and newer linked state.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-person-rename-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';import {ProfileSection} from './src/features/Settings.profile';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const overview={seq:1,version:'fixture',me:{address:'self/laptop'},person:{person:'self',label:'Original name',state:'self',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],reads:[],holdReads:false,overview};
const host={platform:'daemon',workspace:{id:'person-rename',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/person/label')return new Promise((resolve,reject)=>f.writes.push({b:structuredClone(b),resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(i,fail=false,commit=!fail)=>{const w=f.writes[i],person={...overview.person,label:w.b.label};if(commit){overview.person=person;overview.seq++}if(fail)w.reject(Error('Invented name refusal'));else w.resolve(person)};
f.newer=async(label,person='self')=>{overview.person={...overview.person,label,person};overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button><output aria-label="Shared profile name">{s.overview?.person?.label}</output>
 {s.tab==='settings'?<ProfileSection/>:<p>Another view</p>}<textarea aria-label="Unrelated draft"/></>}
await store.start();store.showTab('settings');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(process.env.AGENTNET_PERSON_RENAME_SOURCE)fs.copyFileSync(process.env.AGENTNET_PERSON_RENAME_SOURCE,path.join(work,'src/features/Settings.profile.tsx'));
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Profile rename fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const setup=async(width)=>{const page=await browser.newPage({viewport:{width,height:900}});page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port);await page.getByLabel('Your name',{exact:true}).waitFor();return page};
 const pending=page=>page.getByRole('region',{name:'Name change',exact:true});
 const shared=page=>page.getByRole('status',{name:'Shared profile name',exact:true});
 const save=async(page,label)=>{await page.getByLabel('Your name',{exact:true}).fill(label);const button=page.getByRole('button',{name:'Save',exact:true});await button.evaluate(b=>b.addEventListener('click',e=>fixture.input={at:performance.now(),trusted:e.isTrusted},{once:true,capture:true}));await button.click();await pending(page).waitFor({timeout:500})};
 const capture=async(page,name)=>{if(process.env.AGENTNET_PERSON_RENAME_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_PERSON_RENAME_EVIDENCE+'-'+name+'.png',fullPage:true})};
 for(const width of [1440,390]){
  const page=await setup(width),errors=[];page.on('pageerror',e=>errors.push(String(e)));await save(page,'Requested name');
  const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return {...fixture.input,feedbackMs:performance.now()-fixture.input.at}});assert(timing.trusted&&timing.feedbackMs<500,JSON.stringify(timing));
  assert(await pending(page).evaluate(el=>{const r=el.getBoundingClientRect();return r.top>=0&&r.bottom<=innerHeight&&r.left>=0&&r.right<=innerWidth}));assert.equal(await page.getByLabel('Your name',{exact:true}).count(),0);assert.equal(await shared(page).textContent(),'Requested name');assert.equal(await page.evaluate(()=>fixture.overview.person.label),'Original name');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  assert.equal(await page.evaluate(()=>{const a=fixture.store.api.renamePerson('Requested name'),b=fixture.store.api.renamePerson('Requested name');a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1);await capture(page,'pending-'+width);
  await page.getByRole('button',{name:'Toggle view',exact:true}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.getByLabel('Unrelated draft').fill('Keep unrelated input');await page.getByRole('button',{name:'Toggle view',exact:true}).click();await pending(page).waitFor();await page.evaluate(()=>fixture.finish(0,true));await pending(page).getByRole('alert').waitFor();assert.equal(await shared(page).textContent(),'Original name');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),false);await capture(page,'error-'+width);
  await pending(page).getByRole('button',{name:'Edit name again',exact:true}).click();assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Requested name');await save(page,'Requested name');await page.evaluate(()=>fixture.newer('Newer linked name'));assert.equal(await shared(page).textContent(),'Newer linked name');await page.evaluate(()=>fixture.finish(1,true));await pending(page).getByRole('alert').waitFor();assert.equal(await shared(page).textContent(),'Newer linked name');
  await pending(page).getByRole('button',{name:'Retry name change',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===3);assert.deepEqual(await page.evaluate(()=>fixture.writes.map(w=>w.b)),Array(3).fill({label:'Requested name'}));await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(2)});await page.getByText('Name saved; checking current profile',{exact:true}).waitFor();await capture(page,'checking-'+width);await pending(page).getByRole('button',{name:'Check name status',exact:true}).click();await page.evaluate(()=>fixture.store.api.renamePerson('Requested name'));assert.equal(await page.evaluate(()=>fixture.writes.length),3,'saved status check and duplicate API call do not repeat a write');await page.evaluate(()=>fixture.release());await pending(page).waitFor({state:'hidden'});assert.equal(await shared(page).textContent(),'Requested name');
  await page.getByLabel('Your name',{exact:true}).fill('Newer unsaved draft');await page.evaluate(()=>fixture.newer('Another linked name'));assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Newer unsaved draft');await save(page,'Owner change attempt');await page.evaluate(()=>fixture.newer('Other owner','other'));await pending(page).waitFor({state:'hidden'});await page.evaluate(()=>fixture.finish(3,false,false));await page.waitForTimeout(35);assert.equal(await shared(page).textContent(),'Other owner');assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Other owner');assert.equal(await page.getByLabel('Unrelated draft').inputValue(),'Keep unrelated input');assert.deepEqual(errors,[]);reports.push({width,feedbackMs:timing.feedbackMs});await page.close();
 }
 for(const mode of ['stopped','before-admission','lost-reply','closed-failure']){
  const page=await setup(390);
  if(mode==='before-admission'){assert.equal(await page.evaluate(async()=>{const work=fixture.store.api.renamePerson('Not admitted');fixture.store.stop();await work.catch(()=>{});return fixture.writes.length}),0)}
  else{await save(page,'Captured name');if(mode==='closed-failure'){await page.getByRole('button',{name:'Toggle view',exact:true}).click();await page.evaluate(()=>fixture.finish(0,true));await page.waitForFunction(()=>fixture.store.get().toasts.some(t=>t.text.includes('Name change not confirmed')));await page.getByRole('button',{name:'Toggle view',exact:true}).click();await pending(page).getByRole('alert').waitFor()}else if(mode==='stopped'){assert.equal(await page.evaluate(async()=>{fixture.store.stop();const s=fixture.store.get();fixture.finish(0,false,false);await new Promise(r=>setTimeout(r,35));return fixture.store.get()===s}),true)}else{await page.evaluate(()=>fixture.finish(0,true,true));await pending(page).waitFor({state:'hidden'});assert.equal(await shared(page).textContent(),'Captured name');assert.equal(await page.evaluate(()=>fixture.writes.length),1)}}
  await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual ProfileSection and Store; trusted input, held/refused writes, exact retry, saved readback, newer linked names/drafts, navigation, changed owner and stopped workspace; synthetic host'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
