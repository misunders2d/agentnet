// Device removal changes presentation immediately; native membership stays authoritative.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-device-removal-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {DevicesSection} from './src/features/Settings.profile';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const laptop={address:'self/laptop',name:'Fixture laptop',this:true,fingerprint:'laptop-key'};
const phone={address:'self/phone',name:'Fixture phone',fingerprint:'phone-key'};
const overview={seq:1,version:'fixture',role:'person',me:{address:laptop.address},person:{person:'self',label:'Fixture person',state:'self',address:laptop.address,devices:[laptop,phone]},people:[],dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],reads:[],holdReads:false};
const host={platform:'daemon',workspace:{id:'device-removal',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/device/remove')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false,stale=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented removal refusal'));return;}
 if(!stale){overview.person.devices=[laptop];overview.seq++}w.resolve({note:'Fixture removal stored'});
};
f.newer=async(fingerprint='phone-key',name='Fixture phone')=>{overview.person.devices=fingerprint?[laptop,{...phone,fingerprint,name}]:[laptop];overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button>{s.tab==='settings'?<DevicesSection/>:<p>Another view</p>}<textarea aria-label="Unrelated draft"/><div data-errors role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();store.showTab('settings');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Device removal fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);
  const button=()=>page.getByRole('button',{name:'Remove Fixture phone',exact:true});
  const dialog=page.getByRole('dialog',{name:'Remove Fixture phone?',exact:true});
  const pending=page.getByRole('status',{name:'Removing Fixture phone',exact:true});
  const remove=async()=>{await button().click();await dialog.waitFor();await dialog.getByRole('button',{name:'Remove Fixture phone',exact:true}).click()};
  await button().click();await dialog.getByRole('button',{name:'Keep it',exact:true}).click();await dialog.waitFor({state:'hidden'});assert.equal(await page.evaluate(()=>fixture.writes.length),0,'cancel never submits removal');
  await button().click();const start=Date.now();await dialog.getByRole('button',{name:'Remove Fixture phone',exact:true}).click();await pending.waitFor({timeout:500});await dialog.waitFor({state:'hidden',timeout:500});const feedbackMs=Date.now()-start;assert(feedbackMs<500);
  assert.equal(await button().count(),0,'the current device card becomes a pending removal immediately');assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),2,'pending removal cannot change the authoritative identity roster');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  assert.equal(await page.evaluate(()=>{const a=fixture.store.api.removeDevice('self/phone'),b=fixture.store.api.removeDevice('self/phone');a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1,'duplicate input sends one removal');
  if(process.env.AGENTNET_DEVICE_REMOVAL_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_REMOVAL_EVIDENCE+'-pending-'+width+'.png',fullPage:true});
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retain while removal is pending');await page.getByRole('button',{name:'Toggle view'}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.getByRole('button',{name:'Toggle view'}).click();await pending.waitFor();
  await page.evaluate(()=>fixture.finish(0,true));await button().waitFor();assert.match(await page.locator('[data-errors]').textContent(),/Invented removal refusal/);assert.equal(await pending.count(),0);
  if(process.env.AGENTNET_DEVICE_REMOVAL_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_REMOVAL_EVIDENCE+'-error-'+width+'.png',fullPage:true});
  await remove();await page.evaluate(()=>fixture.newer('',''));await page.evaluate(()=>fixture.finish(1,true));await page.waitForTimeout(30);assert.equal(await button().count(),0,'old failure cannot resurrect a newer removed device');assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),1);
  await page.evaluate(()=>fixture.newer());await remove();await page.evaluate(()=>fixture.newer('changed-key','Renamed replacement'));await page.getByRole('button',{name:'Remove Renamed replacement',exact:true}).waitFor();await page.evaluate(()=>fixture.finish(2,false,true));await page.waitForTimeout(30);assert.equal(await pending.count(),0,'old success cannot hide a replacement device key');await page.getByRole('button',{name:'Remove Renamed replacement',exact:true}).waitFor();
  await page.evaluate(()=>fixture.newer());await remove();await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(3)});await page.getByText('Removal saved; checking current device state…',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),2,'held readback cannot claim confirmed membership change');
  await page.getByRole('button',{name:'Check removal status',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),4,'status check is read-only');await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>fixture.store.get().overview.person.devices.length===1);assert.equal(await pending.count(),0);
  await page.evaluate(()=>fixture.newer());await remove();assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(4);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true,'late completion cannot update a stopped workspace');
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retain while removal is pending');assert.deepEqual(errors,[]);reports.push({width,feedbackMs,writes:await page.evaluate(()=>fixture.writes.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual device removal controls and Store, trusted input with held synthetic authority; no real device removed'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
