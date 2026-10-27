// Real device-link controls, trusted input, held/refused link and clipboard work.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-device-link-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {DevicesSection} from './src/features/Settings.profile';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const overview={seq:1,version:'fixture',role:'person',me:{address:'self/laptop'},person:{person:'self',label:'Fixture person',state:'self',address:'self/laptop',devices:[{address:'self/laptop',name:'Fixture laptop',this:true,fingerprint:'one'}]},people:[],dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],timings:[],copies:[],clipboardFail:true};
const host={platform:'daemon',workspace:{id:'device-link',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/device/link')return new Promise((resolve,reject)=>f.writes.push({resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{f.copies.push(text);if(f.clipboardHold)await new Promise(resolve=>{f.clipboardRelease=resolve});if(f.clipboardFail)throw Error('Invented clipboard refusal')}}});
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail)w.reject(Error('Invented link refusal'));else w.resolve({url:'https://fixture.invalid/one-use-'+index,expires:'2099-01-01T00:00:00Z'})};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button><output>{s.tab}</output><DevicesSection/><textarea aria-label="Unrelated draft"/><div role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Device link fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);const add=page.getByRole('button',{name:'Add a device',exact:true});await add.waitFor();
  const started=Date.now();await add.click();const dialog=page.getByRole('dialog',{name:'Add a device',exact:true});
  await dialog.waitFor({timeout:500});const openMs=Date.now()-started;assert(openMs<500,'dialog must open before the held native link reply');
  await dialog.getByText('Making a one-use link…',{exact:true}).waitFor();assert.equal(await dialog.locator('code').count(),0,'pending state never invents a usable link');
  assert.equal(await page.evaluate(()=>fixture.writes.length),1);
  if(process.env.AGENTNET_DEVICE_LINK_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_LINK_EVIDENCE+'-pending-'+width+'.png',fullPage:true});
  await dialog.getByRole('button',{name:'Done',exact:true}).click();await dialog.waitFor({state:'hidden'});
  await add.click();await dialog.waitFor();assert.equal(await page.evaluate(()=>fixture.writes.length),1,'reopening pending work cannot create another link');
  await dialog.getByRole('button',{name:'Close',exact:true}).click();await dialog.waitFor({state:'hidden'});
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retain while link generation is held');await page.getByRole('button',{name:'Toggle view'}).click();
  await page.evaluate(()=>fixture.finish(0));await page.waitForTimeout(50);assert.equal(await dialog.count(),0,'late completion cannot reopen a dismissed dialog');
  await add.click();await dialog.locator('code[aria-label="Link"]').waitFor();assert.equal(await page.evaluate(()=>fixture.writes.length),1,'completed pending link remains available on deliberate reopening');
  assert.equal(await dialog.locator('code').textContent(),'https://fixture.invalid/one-use-0');
  await page.evaluate(()=>{fixture.clipboardHold=true});const copyStarted=Date.now();
  await dialog.getByRole('button',{name:'Copy link',exact:true}).click();await dialog.getByRole('button',{name:'Copying…',exact:true}).waitFor({timeout:500});
  const copyMs=Date.now()-copyStarted;assert(copyMs<500,'held clipboard work needs immediate visible feedback');
  assert.equal(await dialog.getByRole('button',{name:'Copied',exact:true}).count(),0,'pending clipboard work cannot claim copied');
  await dialog.getByRole('button',{name:'Copying…',exact:true}).evaluate(b=>b.click());assert.equal(await page.evaluate(()=>fixture.copies.length),1,'duplicate input cannot repeat pending copy');
  await page.evaluate(()=>{fixture.clipboardHold=false;fixture.clipboardRelease()});
  const copyError=dialog.getByRole('alert');await copyError.waitFor();assert.match(await copyError.textContent(),/Copy didn’t work here/);assert.equal(await copyError.evaluate(e=>!!e.closest('[inert]')),false,'clipboard refusal must remain accessible inside the active dialog');
  if(process.env.AGENTNET_DEVICE_LINK_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_LINK_EVIDENCE+'-copy-error-'+width+'.png',fullPage:true});
  await page.evaluate(()=>{fixture.clipboardFail=false});await dialog.getByRole('button',{name:'Copy link',exact:true}).click();await dialog.getByRole('button',{name:'Copied',exact:true}).waitFor();assert.equal(await dialog.getByRole('alert').count(),0,'retry clears the old refusal');assert.deepEqual(await page.evaluate(()=>fixture.copies),['https://fixture.invalid/one-use-0','https://fixture.invalid/one-use-0']);await dialog.getByRole('button',{name:'Done',exact:true}).click();await dialog.waitFor({state:'hidden'});
  await add.click();await dialog.waitFor();await page.evaluate(()=>fixture.finish(1,true));await dialog.getByText('Could not make a link. Try again.',{exact:true}).waitFor();
  if(process.env.AGENTNET_DEVICE_LINK_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_DEVICE_LINK_EVIDENCE+'-error-'+width+'.png',fullPage:true});
  await dialog.getByRole('button',{name:'Try again',exact:true}).click();await dialog.getByText('Making a one-use link…',{exact:true}).waitFor();await page.evaluate(()=>fixture.finish(2));await dialog.locator('code[aria-label="Link"]').waitFor();assert.equal(await dialog.locator('code').textContent(),'https://fixture.invalid/one-use-2');
  await page.evaluate(()=>{fixture.clipboardHold=true});await dialog.getByRole('button',{name:'Copy link',exact:true}).click();await dialog.getByRole('button',{name:'Copying…',exact:true}).waitFor();
  await dialog.getByRole('button',{name:'Done',exact:true}).click();await dialog.waitFor({state:'hidden'});
  await add.click();await dialog.waitFor();await page.evaluate(()=>fixture.finish(3));await dialog.locator('code[aria-label="Link"]').waitFor();
  await page.evaluate(()=>{fixture.clipboardHold=false;fixture.clipboardRelease()});await page.waitForTimeout(50);
  assert.equal(await dialog.locator('code').textContent(),'https://fixture.invalid/one-use-3');assert.equal(await dialog.getByRole('button',{name:'Copied',exact:true}).count(),0,'old clipboard success cannot claim the new link was copied');
  await dialog.getByRole('button',{name:'Done',exact:true}).click();await dialog.waitFor({state:'hidden'});
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retain while link generation is held');assert.equal(await page.evaluate(()=>fixture.store.get().overview.person.devices.length),1,'creating links grants no device membership');assert.deepEqual(errors,[]);reports.push({width,openMs,copyMs,writes:await page.evaluate(()=>fixture.writes.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual device dialog, trusted input and held synthetic link generation; no native device enrolled'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
