// Named pending people lists appear before the native write; only verified reads grant roles.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-team-creation-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {TeamsSection} from './src/features/Teams';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const overview={seq:1,version:'fixture',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture person',state:'self',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[]};
const base={version:2,id:'existing',name:'Existing list',seq:1,hash:'verified',members:['self'],managers:['self'],agents:[],manager:true,member:true,archived:false,conflict:false};
const teams={tags:true,legacy_add:true,current:true,status:'available',at:'2026-01-01T00:00:00Z',truncated:false,teams:[base]};
const f=window.fixture={writes:[],reads:[],holdReads:false};
const host={platform:'daemon',workspace:{id:'team-creation',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/teams'){const snapshot=structuredClone(teams);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/team')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/workspace'))return {can_rename:true};
 if(p.startsWith('/api/agents'))return {agents:[]};
 if(b)throw Error('Unexpected fixture mutation '+p);return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented list refusal'));return;}
 const saved={...base,id:'created-'+index,name:w.b.name};teams.teams.push(saved);w.resolve(structuredClone(saved));
};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
f.changeOwner=async()=>{overview.person={...overview.person,person:'replacement-owner'};overview.seq++;await store.refetch()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button>{s.tab==='settings'?<TeamsSection/>:<p>Another view</p>}<textarea aria-label="Unrelated draft"/></>}
await store.start();store.showTab('settings');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Team creation fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);
  const pending=page.getByRole('region',{name:'Pending people list'}),name=page.getByRole('textbox',{name:'New list name',exact:true});
  const create=async value=>{await name.fill(value);await page.getByRole('button',{name:'Create list',exact:true}).click();await pending.waitFor();};
  await name.fill('First pending list');const button=page.getByRole('button',{name:'Create list',exact:true});
  await button.scrollIntoViewIfNeeded();const point=await button.evaluate(button=>{button.addEventListener('click',e=>{fixture.input={at:performance.now(),trusted:e.isTrusted}},{once:true,capture:true});const r=button.getBoundingClientRect();return{x:r.left+r.width/2,y:r.top+r.height/2}});
  await page.mouse.click(point.x,point.y);await pending.waitFor({timeout:500});
  const input=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return{...fixture.input,feedbackAt:performance.now()}}),feedbackMs=input.feedbackAt-input.at;
  assert(input.trusted);assert(feedbackMs<500,JSON.stringify({width,feedbackMs}));await pending.getByText('First pending list',{exact:true}).waitFor();assert.equal(await name.inputValue(),'');assert.equal(await page.getByRole('dialog').count(),0,'pending list has no invented membership or manager controls');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  if(process.env.AGENTNET_TEAM_CREATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_TEAM_CREATION_EVIDENCE+'-pending-'+width+'.png',fullPage:true});
  assert.equal(await page.evaluate(()=>{const c={op:'create',v:2,name:'First pending list'},a=fixture.store.api.changeTeam(c),b=fixture.store.api.changeTeam(c);a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1);
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Preserve while creating');await page.getByRole('button',{name:'Toggle view'}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.getByRole('button',{name:'Toggle view'}).click();await pending.waitFor();
  await name.fill('Next list draft');await page.evaluate(()=>fixture.finish(0,true));await pending.getByRole('alert').waitFor();assert.match(await pending.getByRole('alert').textContent(),/Invented list refusal/);assert.equal(await name.inputValue(),'Next list draft');
  if(process.env.AGENTNET_TEAM_CREATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_TEAM_CREATION_EVIDENCE+'-error-'+width+'.png',fullPage:true});
  await pending.getByRole('button',{name:'Check people lists',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),1,'read-only check cannot create another list');
  await pending.getByRole('button',{name:'Try creating list again',exact:true}).click();await pending.getByRole('status').waitFor();assert.equal(await name.inputValue(),'Next list draft','retry preserves newer input');assert.deepEqual(await page.evaluate(()=>fixture.writes.map(w=>w.b)),[{op:'create',v:2,name:'First pending list'},{op:'create',v:2,name:'First pending list'}]);
  await page.getByRole('button',{name:/^Existing list/}).click();const existing=page.getByRole('dialog',{name:'Existing list',exact:true});await existing.waitFor();
  await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(1)});await page.waitForFunction(()=>fixture.store.get().teamCreation?.saved?.id==='created-1');assert.equal(await existing.count(),1,'late creation cannot replace another selected list');await existing.getByRole('button',{name:'Close',exact:true}).click();
  await pending.getByText('List saved; checking the current list…',{exact:true}).waitFor();await pending.getByRole('button',{name:'Check people lists',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),2);assert.equal(await page.getByRole('button',{name:/^First pending list/}).count(),0,'saved receipt is not a verified list view');
  await page.evaluate(()=>fixture.release());await pending.waitFor({state:'hidden'});await page.getByRole('button',{name:/^First pending list/}).waitFor();assert.equal(await page.getByRole('dialog').count(),0,'read confirmation cannot undo explicit navigation');
  await create('Automatic selection');await name.fill('Keep newer input');await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(2)});await pending.getByText('List saved; checking the current list…',{exact:true}).waitFor();assert.equal(await page.getByRole('dialog').count(),0);await page.evaluate(()=>fixture.release());const automatic=page.getByRole('dialog',{name:'Automatic selection',exact:true});await automatic.waitFor();assert.equal(await automatic.getByRole('textbox',{name:'New name',exact:true}).inputValue(),'Keep newer input');await automatic.getByRole('button',{name:'Close',exact:true}).click();
  await create('Unmounted completion');await page.getByRole('button',{name:'Toggle view'}).click();await page.evaluate(()=>fixture.finish(3));await page.waitForFunction(()=>fixture.store.get().teamCreation?.saved?.id==='created-3');await page.getByRole('button',{name:'Toggle view'}).click();await page.getByRole('button',{name:/^Unmounted completion/}).waitFor();await pending.waitFor({state:'hidden'});assert.equal(await page.getByRole('dialog').count(),0,'old component cannot reopen its completed list');
  await create('Old owner list');await page.evaluate(()=>fixture.changeOwner());await pending.waitFor({state:'hidden'});await page.evaluate(()=>fixture.finish(4,true));await page.waitForTimeout(30);assert.equal(await pending.count(),0,'old owner failure cannot reappear in a changed workspace');
  await create('Stopped workspace');assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(5);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Preserve while creating');assert.deepEqual(errors,[]);reports.push({width,feedbackMs,trustedInput:input.trusted,writes:await page.evaluate(()=>fixture.writes.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual TeamsSection and Store; held synthetic writes and reads; exact retry, duplicate input, navigation, failure, owner and teardown guards; no native list created'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
