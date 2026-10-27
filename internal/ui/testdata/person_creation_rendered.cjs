// Actual person setup: immediate pending intent without inventing an identity.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-person-creation-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';import {ProfileSection} from './src/features/Settings.profile';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const overview={seq:1,version:'fixture',me:{address:'self/laptop',fingerprint:'fixture-key'},role:'unset',persons:true,people:[],dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],reads:[],holdReads:false,overview};
const host={platform:'daemon',workspace:{id:'person-creation',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/person')return new Promise((resolve,reject)=>f.writes.push({b:structuredClone(b),resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(i,fail=false,commit=!fail)=>{const w=f.writes[i],person={person:'created-self',label:w.b.label,state:'self',address:'self/laptop',devices:[],published:false};if(commit){overview.person=person;overview.role='person';overview.seq++}if(fail)w.reject(Error('Invented setup refusal'));else w.resolve({person,note:'Set up here; not on your server yet.'})};
f.newer=async(patch)=>{Object.assign(overview,patch);overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button>
 {s.tab==='settings'?<ProfileSection/>:<p>Another view</p>}<textarea aria-label="Unrelated draft"/></>}
await store.start();store.showTab('settings');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(process.env.AGENTNET_PERSON_CREATION_SOURCE)fs.copyFileSync(process.env.AGENTNET_PERSON_CREATION_SOURCE,path.join(work,'src/features/Settings.profile.tsx'));
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Person setup fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const setup=async(width)=>{const page=await browser.newPage({viewport:{width,height:900}});page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port);await page.getByLabel('Your name',{exact:true}).waitFor();return page};
 const pending=page=>page.getByRole('region',{name:'Person setup',exact:true});
 const save=async(page,label)=>{await page.getByLabel('Your name',{exact:true}).fill(label);const button=page.getByRole('button',{name:'Set up my person',exact:true});await button.evaluate(b=>b.addEventListener('click',e=>fixture.input={at:performance.now(),trusted:e.isTrusted},{once:true,capture:true}));await button.click();await pending(page).waitFor({timeout:500})};
 const capture=async(page,name)=>{if(process.env.AGENTNET_PERSON_CREATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_PERSON_CREATION_EVIDENCE+'-'+name+'.png',fullPage:true})};
 for(const width of [1440,390]){
  const page=await setup(width),errors=[];page.on('pageerror',e=>errors.push(String(e)));await save(page,'Requested name');
  const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return {...fixture.input,feedbackMs:performance.now()-fixture.input.at}});assert(timing.trusted&&timing.feedbackMs<500,JSON.stringify(timing));
  assert(await pending(page).evaluate(el=>{const r=el.getBoundingClientRect();return r.top>=0&&r.bottom<=innerHeight&&r.left>=0&&r.right<=innerWidth}));assert.equal(await page.getByLabel('Your name',{exact:true}).count(),0);assert.equal(await page.getByRole('button',{name:'It is a service or bot…',exact:true}).count(),0);assert.equal(await page.evaluate(()=>!!fixture.store.get().overview.person),false);assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  assert.equal(await page.evaluate(()=>{const a=fixture.store.api.createPerson('Requested name'),b=fixture.store.api.createPerson('Requested name');a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1);await capture(page,'pending-'+width);
  await page.getByRole('button',{name:'Toggle view',exact:true}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.getByLabel('Unrelated draft').fill('Keep unrelated input');await page.evaluate(()=>fixture.finish(0,true));await page.waitForFunction(()=>fixture.store.get().toasts.some(t=>t.text.includes('Person setup not confirmed')));await page.getByRole('button',{name:'Toggle view',exact:true}).click();await pending(page).getByRole('alert').waitFor();assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),false);await capture(page,'error-'+width);
  await pending(page).getByRole('button',{name:'Edit setup name',exact:true}).click();assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Requested name');await save(page,'Requested name');await page.evaluate(()=>fixture.finish(1,true));await pending(page).getByRole('alert').waitFor();await pending(page).getByRole('button',{name:'Retry person setup',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===3);assert.deepEqual(await page.evaluate(()=>fixture.writes.map(w=>w.b)),Array(3).fill({label:'Requested name'}));
  await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(2)});await page.getByText('Setup saved; checking current profile',{exact:true}).waitFor();await page.getByText('Set up here; not on your server yet.',{exact:true}).waitFor();await capture(page,'checking-'+width);await pending(page).getByRole('button',{name:'Check setup status',exact:true}).click();await page.getByRole('button',{name:'Checking setup…',exact:true}).waitFor();await page.evaluate(()=>fixture.store.api.createPerson('Requested name'));assert.equal(await page.evaluate(()=>fixture.writes.length),3,'saved status check and duplicate API call do not repeat a write');await page.evaluate(()=>fixture.release());await pending(page).waitFor({state:'hidden'});assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Requested name');await page.getByText('Not on your server yet:',{exact:false}).waitFor();assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.person),'created-self');assert.equal(await page.getByLabel('Unrelated draft').inputValue(),'Keep unrelated input');assert.deepEqual(errors,[]);reports.push({width,feedbackMs:timing.feedbackMs});await page.close();
 }
 for(const mode of ['stopped','before-admission','lost-reply','linked-person','changed-device','service-role','waiting-link']){
  const page=await setup(390);
  if(mode==='before-admission'){assert.equal(await page.evaluate(async()=>{const work=fixture.store.api.createPerson('Not admitted');fixture.store.stop();await work.catch(()=>{});return fixture.writes.length}),0)}
  else{await save(page,'Captured name');if(mode==='stopped'){assert.equal(await page.evaluate(async()=>{fixture.store.stop();const s=fixture.store.get();fixture.finish(0,false,false);await new Promise(r=>setTimeout(r,35));return fixture.store.get()===s}),true)}else if(mode==='lost-reply'){await page.evaluate(()=>fixture.finish(0,true,true));await pending(page).waitFor({state:'hidden'});assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Captured name');assert.equal(await page.evaluate(()=>fixture.writes.length),1)}else{
    const patch=mode==='linked-person'?{person:{person:'linked',label:'Linked person',state:'self',address:'self/laptop',devices:[]},role:'person'}:mode==='changed-device'?{me:{address:'other/device',fingerprint:'other-key'}}:mode==='service-role'?{role:'service'}:{link:{state:'pending',approver:'self/phone'}};
    await page.evaluate(patch=>fixture.newer(patch),patch);await pending(page).waitFor({state:'hidden'});await page.evaluate(()=>fixture.finish(0,false,false));await page.waitForTimeout(35);assert.equal(await page.evaluate(()=>fixture.store.get().personCreation),undefined);assert.equal(await page.evaluate(()=>fixture.store.get().toasts.length),0);if(mode==='linked-person')assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'Linked person');if(mode==='changed-device')assert.equal(await page.getByLabel('Your name',{exact:true}).inputValue(),'');
  }}await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual ProfileSection and Store; trusted input, held/refused setup, exact retry, saved readback, lost reply, navigation, linked identity, service/link/device changes and stopped workspace; no invented authority; synthetic host'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
