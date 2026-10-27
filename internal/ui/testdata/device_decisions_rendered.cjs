// Real notification controls and native consent, trusted input, held/refused storage.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-device-decisions-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {DevicesSection} from './src/features/Settings.profile';import {OksView,needsYouCount} from './src/features/Approvals';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const link={id:'link-one',name:'Fixture phone',address:'self/phone',fingerprint:'key-one',requested_at:'2026-01-01T00:00:00Z',expires:'2099-01-01T00:00:00Z',state:'pending'};
const overview={seq:1,version:'fixture',role:'person',me:{address:'self/laptop'},person:{person:'self',label:'Fixture person',state:'self',address:'self/laptop',devices:[{address:'self/laptop',name:'Fixture laptop',this:true,fingerprint:'one'}]},people:[],dms:[],threads:[],needs_you:[],links:[link]};
const f=window.fixture={writes:[],reads:[],holdReads:false,timings:[],link};
const host={platform:'daemon',workspace:{id:'device-decisions',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/device/decide')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject,fingerprint:overview.links.find(l=>l.id===b.id)?.fingerprint}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false,stale=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented device refusal'));return;}
 if(!stale){overview.links=[];overview.seq++;if(w.b.accept)overview.person.devices.push({address:link.address,name:link.name,fingerprint:w.fingerprint})}w.resolve({note:'Fixture decision stored'});
};
f.newer=async(state='pending',fingerprint='key-one')=>{overview.links=[{...link,state,fingerprint}];overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='oks'?'settings':'oks')}>Toggle view</button><output aria-label="Needs attention">{needsYouCount(s.overview)}</output>{s.tab==='oks'?<OksView/>:<DevicesSection/>}<textarea aria-label="Unrelated draft"/><div data-errors role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();store.showTab(new URL(location).searchParams.get('view')==='review'?'oks':'settings');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Device decision fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390])for(const view of ['settings','review']){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?view='+view);
  const refuse=()=>page.getByRole('button',{name:/^(No, refuse it|Refuse)$/});await refuse().waitFor();
  const started=Date.now();await refuse().click();await page.getByRole('region',{name:'Pending device decisions'}).waitFor({timeout:500});const feedbackMs=Date.now()-started;assert(feedbackMs<500);
  if(process.env.AGENTNET_DEVICE_DECISION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_DECISION_EVIDENCE+'-pending-'+view+'-'+width+'.png',fullPage:true});
  assert.equal(await refuse().count(),0,'request leaves active attention before the held decision reply');assert.equal(await page.getByLabel('Needs attention').textContent(),'0');
  assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),1,'local choice never grants device membership');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  assert.equal(await page.evaluate(()=>{const a=fixture.store.api.decideDevice('link-one',false),b=fixture.store.api.decideDevice('link-one',false);a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1,'duplicate input sends one decision');
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Preserve pending decision draft');await page.getByRole('button',{name:'Toggle view'}).click();await page.getByRole('region',{name:'Pending device decisions'}).waitFor();assert.equal(await refuse().count(),0,'pending choice follows navigation');
  await page.evaluate(()=>fixture.finish(0,true));await refuse().waitFor();assert.equal(await page.getByLabel('Needs attention').textContent(),'1');assert((await page.locator('[data-errors]').textContent()).includes('Invented device refusal'));
  if(process.env.AGENTNET_DEVICE_DECISION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_DECISION_EVIDENCE+'-error-'+view+'-'+width+'.png',fullPage:true});
  await refuse().click();await page.evaluate(()=>fixture.newer('refused'));assert.equal(await refuse().count(),0);await page.evaluate(()=>fixture.finish(1,true));await page.waitForTimeout(30);assert.equal(await page.getByLabel('Needs attention').textContent(),'0','old failure cannot resurrect a newer resolved request');
  await page.evaluate(()=>fixture.newer());await refuse().waitFor();await refuse().click();await page.evaluate(()=>fixture.newer('pending','key-two'));await refuse().waitFor();await page.evaluate(()=>fixture.finish(2,false,true));await page.waitForTimeout(30);assert.equal(await page.getByLabel('Needs attention').textContent(),'1','old success cannot hide a changed device key');
  // Approval remains a pending choice; the roster changes only on the real reply.
  const approve=page.getByRole('button',{name:/^(Yes, it’s mine|Approve device)$/});await approve.click();
  const confirm=page.getByRole('dialog');if(await confirm.count())await confirm.getByRole('button',{name:'Yes, it’s mine',exact:true}).click();
  await page.getByRole('region',{name:'Pending device decisions'}).waitFor();assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),1);
  await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(3)});await page.getByText('Device decision saved; checking current state…',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),1,'held readback cannot pretend membership has converged');
  await page.getByRole('button',{name:'Check device status',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),4,'checking pending confirmation is read-only');
  await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>fixture.store.get().overview.person.devices.length===2);assert.equal(await page.getByLabel('Needs attention').textContent(),'0');
  await page.evaluate(()=>fixture.newer());await refuse().waitFor();await refuse().click();assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(4);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true,'old completion cannot update a stopped workspace');
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Preserve pending decision draft');assert.deepEqual(errors,[]);reports.push({width,view,feedbackMs,writes:await page.evaluate(()=>fixture.writes.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual Settings/Review and shared badges, trusted input with held synthetic authority; no native device enrolled'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
