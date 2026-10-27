// Real profile and shared avatars, trusted input, held/refused storage.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-picture-changes-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {ProfileSection} from './src/features/Settings.profile';import {PersonAvatar} from './src/ui/Avatar';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const canvas=document.createElement('canvas');canvas.width=canvas.height=8;const ctx=canvas.getContext('2d');
const photo=color=>{ctx.fillStyle=color;ctx.fillRect(0,0,8,8);return canvas.toDataURL()};const red=photo('red'),blue=photo('blue');
const overview={seq:1,version:'fixture',me:{address:'self/laptop'},person:{person:'self',label:'Me',state:'self',address:'self/laptop',devices:[],picture:'red',picture_url:red},people:[],dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],reads:[],holdReads:false,timings:[],red,blue};
const host={platform:'daemon',workspace:{id:'picture-changes',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/person/picture')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false,stale=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented picture refusal'));return;}
 const person={...overview.person,picture:w.b.png?'blue':undefined,picture_url:w.b.png?'data:image/png;base64,'+w.b.png:undefined};
 if(!stale){overview.person=person;overview.seq++;}w.resolve(person);
};
f.newer=async()=>{overview.person={...overview.person,picture:'blue',picture_url:blue};overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button><output>{s.tab}</output>
 <div data-other-avatar><PersonAvatar name="Me" seed="self"/></div><ProfileSection/><textarea aria-label="Unrelated draft"/>
 <div role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Picture recovery fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);const remove=()=>page.getByRole('button',{name:'Remove picture',exact:true});await remove().waitFor();
  await page.evaluate(()=>document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[data-other-avatar] img'))return;observer.disconnect();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push(performance.now()-start)))});observer.observe(document.body,{subtree:true,childList:true})},{once:true,capture:true}));
  await remove().click();await page.locator('[data-other-avatar] img').waitFor({state:'hidden'});await page.getByText('Removing picture…',{exact:true}).waitFor();assert.equal(await page.locator('img').count(),0,'all own avatars change before storage replies');
  assert.deepEqual(await page.evaluate(()=>{const a=fixture.store.api.setPersonPicture(''),b=fixture.store.api.setPersonPicture('');return {same:a===b,writes:fixture.writes.length,unsent:fixture.store.hasUnsent()}}),{same:true,writes:1,unsent:true});
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retain while blocked');await page.getByRole('button',{name:'Toggle view'}).click();assert.equal(await page.locator('output').innerText(),'settings');
  await page.evaluate(()=>fixture.finish(0,true));await remove().waitFor();await page.getByRole('alert').filter({hasText:'Invented picture refusal'}).waitFor();assert.equal(await page.locator('[data-other-avatar] img').getAttribute('src'),await page.evaluate(()=>fixture.red));
  await remove().click();await page.evaluate(()=>fixture.newer());assert.equal(await page.locator('[data-other-avatar] img').getAttribute('src'),await page.evaluate(()=>fixture.blue));await page.evaluate(()=>fixture.finish(1,false,true));await page.getByRole('button',{name:'Choose picture',exact:true}).waitFor();
  await page.waitForFunction(()=>!fixture.store.get().pictureChange);assert.equal(await page.locator('[data-other-avatar] img').getAttribute('src'),await page.evaluate(()=>fixture.blue),'older completion cannot erase newer synced picture');
  await remove().click();await page.evaluate(()=>fixture.finish(2));await page.waitForFunction(()=>!fixture.store.get().pictureChange);assert.equal(await page.locator('img').count(),0);assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),false);
  await page.evaluate(()=>{fixture.result=fixture.store.api.setPersonPicture(fixture.red.split(',')[1]).catch(e=>{fixture.error=e.message})});await page.locator('[data-other-avatar] img').waitFor();assert.equal(await page.locator('[data-other-avatar] img').getAttribute('src'),await page.evaluate(()=>fixture.red),'new picture preview shared before save');
  await page.evaluate(()=>fixture.finish(3,true));await page.waitForFunction(()=>fixture.error==='Invented picture refusal');assert.equal(await page.locator('img').count(),0,'failed save restores current empty picture');
  await page.evaluate(()=>fixture.newer());await remove().click();await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(4)});await page.getByText('Picture change saved; checking current state…',{exact:true}).waitFor();assert.equal(await page.locator('img').count(),0);assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),false);await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>!fixture.store.get().pictureChange);
  await page.evaluate(()=>fixture.newer());await remove().click();await page.evaluate(()=>fixture.newer());await page.evaluate(()=>fixture.finish(5,true));await page.waitForFunction(()=>!fixture.store.get().pictureChange);assert.equal(await page.locator('[data-other-avatar] img').getAttribute('src'),await page.evaluate(()=>fixture.blue),'failure preserves newer synchronized state');
  await remove().click();assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(6);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true,'old workspace completion cannot update the stopped store');
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retain while blocked');await page.waitForFunction(()=>fixture.timings.length===1);const times=await page.evaluate(()=>fixture.timings);assert(times.every(t=>t<500),JSON.stringify(times));assert.deepEqual(errors,[]);reports.push({width,feedbackMs:times,writes:await page.evaluate(()=>fixture.writes.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'trusted input, actual shared UI, synthetic held backend; two frame opportunities are not physical paint proof'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
