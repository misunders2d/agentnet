// Real notification controls and native consent, trusted input, held/refused storage.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-notification-changes-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {NotificationsSection} from './src/features/Settings.notify';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const mode=new URL(location).searchParams.get('mode')||'daemon';
const overview={seq:1,version:'fixture',me:{address:'self/laptop'},people:[],dms:[],threads:[],needs_you:[],notify:{available:true,native:mode!=='browser',enabled:mode==='daemon',pending:false,preview:'',preview_default:'full'}};
const f=window.fixture={writes:[],reads:[],holdReads:false,timings:[],permissions:[],granted:false,connectionWrites:[],connectionReads:[],connectionTimings:[],connection:{enabled:false,badging:false},visibility:[],holdConnectionReads:false};
const status=()=>({granted:f.granted,app_granted:f.granted});
const host={platform:mode,workspace:{id:'notify-changes-'+mode,state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p.startsWith('/api/notify/'))return new Promise((resolve,reject)=>f.writes.push({p,b,resolve,reject}));
 if(p.startsWith('/api/typing'))return {preferences:{send:false,show:false},entries:[]};if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
if(mode==='android')host.android={visible:()=>true,notificationStatus:async()=>status(),requestNotifications:()=>new Promise(resolve=>f.permissions.push(granted=>{f.granted=granted;resolve(status())})),onVisibility:fn=>{f.visibility.push(fn);return()=>{f.visibility=f.visibility.filter(x=>x!==fn)}},connection:async()=>{const value={...f.connection};if(f.holdConnectionReads)await new Promise(resolve=>f.connectionReads.push(resolve));return value},setConnection:enabled=>new Promise((resolve,reject)=>f.connectionWrites.push({enabled,resolve,reject}))};
if(mode==='browser')Object.defineProperty(window,'Notification',{configurable:true,value:{permission:'default',requestPermission:()=>new Promise((resolve,reject)=>f.permissions.push(value=>{if(value==='error')reject(Error('Invented permission request failure'));else resolve(value?'granted':'denied')}))}});
const store=new Store(host);f.store=store;
f.finish=(index,fail=false,stale=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented notification refusal'));return;}
 if(!stale){overview.notify={...overview.notify,...(w.p.endsWith('preview')?{preview:w.b.preview}:{enabled:w.p.endsWith('enable')})};overview.seq++;}w.resolve({note:'Saved'});
};
f.newer=async(preview='sender')=>{overview.notify={...overview.notify,preview};overview.seq++;await store.refetch()};
f.finishConnection=(index,fail=false)=>{const w=f.connectionWrites[index];if(fail)w.reject(Error('Invented connection refusal'));else {f.connection={...f.connection,enabled:w.enabled};w.resolve({...f.connection})}};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button><output>{s.tab}</output><NotificationsSection/><textarea aria-label="Unrelated draft"/><div role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Notification recovery fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390])for(const mode of ['daemon','android','browser']){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?mode='+mode);const toggle=page.getByRole('switch',{name:'Notifications',exact:true});await toggle.waitFor();await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));
  if(mode!=='daemon'){
   await toggle.click();await page.getByText('Waiting for notification permission…',{exact:true}).waitFor();assert.equal(await toggle.getAttribute('aria-checked'),'false','no ungranted native permission shown as enabled');assert.equal(await page.evaluate(()=>fixture.writes.length),0);
   await page.evaluate(()=>fixture.permissions.shift()(false));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await page.evaluate(()=>fixture.writes.length),0,'denial never reaches enable API');
   if(mode==='browser'){await toggle.click();await page.evaluate(()=>fixture.permissions.shift()('error'));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await page.evaluate(()=>fixture.writes.length),0,'permission exception recovers without enabling');}
   await toggle.click();await page.evaluate(()=>fixture.permissions.shift()(true));await page.waitForFunction(()=>fixture.writes.length===1);await page.waitForFunction(()=>document.querySelector('[role="switch"][aria-label="Notifications"]').getAttribute('aria-checked')==='true');assert.equal(await toggle.getAttribute('aria-checked'),'true');await page.evaluate(()=>fixture.finish(0));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));
  }
  await page.evaluate(()=>document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[role="switch"][aria-label="Notifications"]').getAttribute('aria-checked')!=='false')return;observer.disconnect();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push(performance.now()-start)))});observer.observe(document.body,{subtree:true,attributes:true})},{once:true,capture:true}));
  const off=await page.evaluate(()=>fixture.writes.length);await toggle.click();assert.equal(await toggle.getAttribute('aria-checked'),'false','disable changes before blocked storage');await page.getByText('Saving notification preference…',{exact:true}).waitFor();
  await page.evaluate(()=>{const b=document.querySelector('[role="switch"][aria-label="Notifications"]');b.dispatchEvent(new MouseEvent('click',{bubbles:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true}))});assert.equal(await page.evaluate(()=>fixture.writes.length),off+1,'duplicate input cannot enqueue a second preference');
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retained while notification storage is blocked');await page.getByRole('button',{name:'Toggle view'}).click();await page.evaluate(i=>fixture.finish(i,true),off);await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await toggle.getAttribute('aria-checked'),'true','refusal restores current enabled value');
  if(mode!=='browser'){
   const select=page.getByLabel('Message previews',{exact:true}),index=await page.evaluate(()=>fixture.writes.length);await select.selectOption('hidden');assert.equal(await select.inputValue(),'hidden','preview selection stays selected before storage');assert.equal(await toggle.getAttribute('aria-checked'),'true','preview does not change notification consent');
   await page.evaluate(()=>fixture.newer());assert.equal(await select.inputValue(),'sender','newer linked preference wins');await page.evaluate(i=>fixture.finish(i,false,true),index);await page.waitForFunction(()=>!document.querySelector('#notification-preview').disabled);assert.equal(await select.inputValue(),'sender','older success cannot undo newer preference');
   const next=await page.evaluate(()=>fixture.writes.length);await select.selectOption('hidden');await page.evaluate(i=>fixture.finish(i,true),next);await page.waitForFunction(()=>!document.querySelector('#notification-preview').disabled);assert.equal(await select.inputValue(),'sender','failure reveals current preference');
  }
  const last=await page.evaluate(()=>fixture.writes.length);await toggle.click();await page.evaluate(i=>{fixture.holdReads=true;fixture.finish(i)},last);await page.getByText('Notification preference saved; checking current state…',{exact:true}).waitFor();assert.equal(await toggle.getAttribute('aria-checked'),'false');await page.getByRole('button',{name:'Check notification status',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),last+1,'status check never repeats the preference write');await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await toggle.getAttribute('aria-checked'),'false');
  if(mode==='android'){
   const connection=page.getByRole('switch',{name:'Background connection',exact:true});
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));
   await page.evaluate(()=>{fixture.holdConnectionReads=true;for(const fn of fixture.visibility)fn()});
   await page.waitForFunction(()=>fixture.connectionReads.length===1);
   await page.evaluate(()=>document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[aria-label="Background connection"]').getAttribute('aria-checked')!=='true')return;observer.disconnect();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.connectionTimings.push(performance.now()-start)))});observer.observe(document.body,{subtree:true,attributes:true})},{once:true,capture:true}));
   await connection.click();assert.equal(await connection.getAttribute('aria-checked'),'true','background connection updates before native reply');
   await page.getByText('Changing background connection…',{exact:true}).waitFor();
   if(process.env.AGENTNET_NOTIFICATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_NOTIFICATION_EVIDENCE+'-connection-pending-'+width+'.png',fullPage:true});
   await page.evaluate(()=>{const b=document.querySelector('[aria-label="Background connection"]');b.dispatchEvent(new MouseEvent('click',{bubbles:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true}))});
   assert.equal(await page.evaluate(()=>fixture.connectionWrites.length),1,'duplicate connection input cannot enqueue another native operation');
   await page.evaluate(()=>{fixture.holdConnectionReads=false;for(const resolve of fixture.connectionReads.splice(0))resolve()});
   assert.equal(await connection.getAttribute('aria-checked'),'true','stale pre-click read cannot erase pending choice');
   await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retained while notification storage is blocked');await page.getByRole('button',{name:'Toggle view'}).click();
   await page.evaluate(()=>fixture.finishConnection(0,true));await page.getByText('Invented connection refusal',{exact:true}).waitFor();
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));
   assert.equal(await connection.getAttribute('aria-checked'),'false','refused native change restores current setting');
   if(process.env.AGENTNET_NOTIFICATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_NOTIFICATION_EVIDENCE+'-connection-error-'+width+'.png',fullPage:true});
   await connection.click();assert.equal(await connection.getAttribute('aria-checked'),'true');await page.evaluate(()=>fixture.finishConnection(1));
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));assert.equal(await connection.getAttribute('aria-checked'),'true');
   // A newer native Stop is authoritative after the completed request.
   await page.evaluate(()=>{fixture.connection={enabled:false,badging:false};for(const fn of fixture.visibility)fn()});
   await page.waitForFunction(()=>document.querySelector('[aria-label="Background connection"]').getAttribute('aria-checked')==='false');
  }
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retained while notification storage is blocked');await page.waitForFunction(()=>fixture.timings.length===1);const times=await page.evaluate(()=>fixture.timings);assert(times.every(t=>t<500),JSON.stringify(times));assert.deepEqual(errors,[]);const connectionTimes=await page.evaluate(()=>fixture.connectionTimings);if(mode==='android')assert(connectionTimes.length===1&&connectionTimes.every(t=>t<500),JSON.stringify(connectionTimes));reports.push({width,mode,feedbackMs:times,connectionFeedbackMs:connectionTimes});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'trusted browser input and held synthetic storage; Android permission bridge simulated, no OS consent claimed'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
