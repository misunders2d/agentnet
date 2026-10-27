// Actual invitation dialog: the destination is visible before catalog/history preparation.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-invite-open-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {InviteSheet} from './src/features/InviteSheet';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),mode=new URL(location).searchParams.get('mode');
const overview={seq:1,version:'fixture',role:'person',agents:true,groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture owner',state:'self',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[]};
const thread={id:'fixture-group',kind:'group',title:'Fixture group',role:'member',members:[{...overview.person,admin:true}],peer:{label:'Fixture group'},messages:[],agents:[],guests:[],topics:[]};
const f=window.fixture={reads:[],writes:[],opened:false};
const host={platform:'daemon',workspace:{id:'invite-open',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(f.opened&&(p.startsWith('/api/dm?')||p.startsWith('/api/agents')))return new Promise((resolve,reject)=>f.reads.push({p,resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};
 if(b){f.writes.push(p);throw Error('Unexpected fixture mutation '+p)}return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const r=f.reads[index];if(fail)r.reject(Error('Invented chat read refusal'));else r.resolve(r.p.startsWith('/api/dm?')?structuredClone(thread):{local:true,agents:[]})};
function App(){return <><textarea aria-label="Unrelated draft"/><button onClick={()=>{f.opened=true;store.openInvite(thread.id)}}>Open invitation</button><InviteSheet/></>}
await store.start();if(mode==='cached')store.set({dm:thread});createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(process.env.AGENTNET_INVITE_OPEN_SOURCE)fs.copyFileSync(process.env.AGENTNET_INVITE_OPEN_SOURCE,path.join(work,'src/features/InviteSheet.tsx'));
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Invitation opening fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const capture=async(page,name)=>{if(!process.env.AGENTNET_INVITE_OPEN_EVIDENCE)return;await page.evaluate(async()=>Promise.all(document.getAnimations().filter(a=>Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{}))));await page.screenshot({path:process.env.AGENTNET_INVITE_OPEN_EVIDENCE+'-'+name+'.png',fullPage:true})};
 for(const width of [1440,390])for(const mode of ['cached','fetch']){
  const page=await browser.newPage({viewport:{width,height:960}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port+'/?mode='+mode);await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep this draft');
  const open=page.getByRole('button',{name:'Open invitation',exact:true});
  await open.evaluate(b=>b.addEventListener('click',e=>{const start=performance.now();requestAnimationFrame(()=>{const d=document.querySelector('[role="dialog"]'),c=d?.querySelector('button[aria-label="Close"]'),r=c?.getBoundingClientRect();fixture.frame={trusted:e.isTrusted,feedbackMs:performance.now()-start,title:d?.textContent.includes('Bring someone in'),shell:d?.textContent.includes('Opening the invitation picker'),picker:!!d?.querySelector('input[type="search"]'),reads:fixture.reads.length,close:!!r&&r.top>=0&&r.bottom<=innerHeight&&r.left>=0&&r.right<=innerWidth,display:d&&getComputedStyle(d).display,opacity:d&&getComputedStyle(d).opacity}})},{once:true,capture:true}));
  await open.click();await page.waitForFunction(()=>fixture.frame);const frame=await page.evaluate(()=>fixture.frame);assert(frame.trusted&&frame.title&&frame.close&&frame.display!=='none'&&Number(frame.opacity)>0,JSON.stringify({width,mode,frame}));assert(frame.feedbackMs<500,JSON.stringify({width,mode,frame}));assert.equal(frame.reads,0,'catalog/history reads wait until after first frame');assert.equal(frame.picker,false,'full picker waits until after first frame');assert(frame.shell,'first frame must show invitation destination before preparing contents');
  const dialog=page.getByRole('dialog',{name:'Bring someone in',exact:true});await page.waitForFunction(()=>fixture.reads.length===1);await capture(page,mode+'-pending-'+width);
  if(mode==='fetch'){
   await page.evaluate(()=>fixture.finish(0,true));await dialog.getByRole('alert').waitFor();await capture(page,mode+'-error-'+width);await dialog.getByRole('button',{name:'Try again',exact:true}).click();await page.waitForFunction(()=>fixture.reads.length===2);
  }else await dialog.getByRole('searchbox',{name:'Search people and agents'}).fill('Draft search');
  await dialog.getByRole('button',{name:'Close',exact:true}).click();await dialog.waitFor({state:'hidden'});await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Still editable');
  const pending=mode==='fetch'?1:0;await open.click();await page.waitForFunction(n=>fixture.reads.length===n,pending+2);await page.evaluate(n=>fixture.finish(n),pending);await page.waitForTimeout(40);
  if(mode==='fetch')assert.equal(await dialog.getByRole('searchbox',{name:'Search people and agents'}).count(),0,'closed read cannot populate its replacement');else assert.equal(await dialog.getByText('Looking for agents…',{exact:true}).count(),1,'closed catalog cannot complete replacement');
  await page.evaluate(n=>fixture.finish(n),pending+1);
  if(mode==='fetch'){await page.waitForFunction(n=>fixture.reads.length===n,pending+3);await page.evaluate(n=>fixture.finish(n),pending+2)}
  await dialog.getByRole('searchbox',{name:'Search people and agents'}).waitFor();await dialog.getByText('Looking for agents…',{exact:true}).waitFor({state:'hidden'});assert.equal(await dialog.getByRole('searchbox',{name:'Search people and agents'}).inputValue(),'');await capture(page,mode+'-ready-'+width);
  await dialog.getByRole('button',{name:'Close',exact:true}).click();assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Still editable');assert.deepEqual(await page.evaluate(()=>fixture.writes),[]);assert.deepEqual(errors,[]);reports.push({width,mode,...frame});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'trusted opening of actual InviteSheet, first-frame destination before catalog/history preparation, held reads/failure/retry/Close/reopen/late results and preserved unrelated draft; synthetic native host'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
