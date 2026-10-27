// Open the chosen conversation form before its verified people snapshot arrives.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-team-selection-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {TeamsSection} from './src/features/Teams';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const people=[{person:'bob',label:'Fixture Bob',state:'pinned',address:'bob/laptop'},{person:'carol',label:'Fixture Carol',state:'pinned',address:'carol/laptop'}];
const overview={seq:1,version:'fixture',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture person',state:'self',address:'self/laptop',devices:[]},people,dms:[],threads:[],needs_you:[]};
const teams={tags:true,legacy_add:true,current:true,status:'available',at:'2026-01-01T00:00:00Z',truncated:false,teams:people.map((p,i)=>({version:2,id:'list-'+i,name:'Fixture list '+(i?'B':'A'),members:['self',p.person],managers:['self'],agents:[],manager:true,member:true,archived:false,conflict:false}))};
const f=window.fixture={snapshots:[],writes:[]};
const host={platform:'daemon',workspace:{id:'team-selection',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/teams')return structuredClone(teams);
 if(p==='/api/teams/snapshot')return new Promise((resolve,reject)=>f.snapshots.push({b,resolve,reject}));
 if(p.startsWith('/api/workspace'))return {can_rename:true};
 if(p.startsWith('/api/agents'))return {agents:[]};
 if(b){f.writes.push({p,b});throw Error('Unexpected fixture mutation '+p)}return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.snapshots[index];if(fail){w.reject(Error('Invented verified-list refusal'));return;}
 const person=w.b.teams[0]==='list-0'?'bob':'carol';w.resolve({persons:[{id:'self'},{id:person}],teams:w.b.teams});
};
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
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Team selection fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);
  const open=async name=>{await page.getByRole('button',{name:new RegExp('^Fixture list '+name)}).click();await page.getByRole('dialog',{name:'Fixture list '+name,exact:true}).waitFor()};
  const draft=page.getByRole('dialog',{name:'New group',exact:true});
  await open('A');const start=Date.now();await page.getByRole('button',{name:'Select for a conversation…',exact:true}).click();await draft.waitFor({timeout:500});const feedbackMs=Date.now()-start;assert(feedbackMs<500);await draft.getByRole('status').waitFor();assert.equal(await draft.getByRole('textbox',{name:'Group name',exact:true}).count(),0,'unverified snapshot cannot populate recipient selection');assert.equal(await page.evaluate(()=>fixture.snapshots.length),1);
  if(process.env.AGENTNET_TEAM_SELECTION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_TEAM_SELECTION_EVIDENCE+'-pending-'+width+'.png',fullPage:true});
  await draft.getByRole('button',{name:'Close',exact:true}).click();await draft.waitFor({state:'hidden'});await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep while snapshot is pending');
  await open('B');await page.getByRole('button',{name:'Select for a conversation…',exact:true}).click();await draft.waitFor();await page.evaluate(()=>fixture.finish(0));await page.waitForTimeout(30);assert.equal(await draft.getByRole('textbox',{name:'Group name',exact:true}).count(),0,'old list cannot populate the newly chosen list');
  await page.evaluate(()=>fixture.finish(1,true));await draft.getByRole('alert').waitFor();assert.match(await draft.getByRole('alert').textContent(),/Invented verified-list refusal/);
  if(process.env.AGENTNET_TEAM_SELECTION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_TEAM_SELECTION_EVIDENCE+'-error-'+width+'.png',fullPage:true});
  await draft.getByRole('button',{name:'Try again',exact:true}).click();await draft.getByRole('status').waitFor();assert.deepEqual(await page.evaluate(()=>fixture.snapshots.map(s=>s.b.teams)),[['list-0'],['list-1'],['list-1']]);await page.evaluate(()=>fixture.finish(2));await draft.getByRole('textbox',{name:'Group name',exact:true}).waitFor();
  assert.equal(await draft.getByRole('button',{name:'Remove Fixture Carol',exact:true}).count(),1,'verified requested list supplies its exact person');assert.equal(await draft.getByRole('button',{name:'Remove Fixture Bob',exact:true}).count(),0,'old requested list does not contaminate selection');
  await draft.getByRole('textbox',{name:'Group name',exact:true}).fill('Unsent fixture group');assert.equal(await page.evaluate(()=>fixture.writes.length),0,'opening or retrying selection never creates or invites');await draft.getByRole('button',{name:'Back to people',exact:true}).click();await draft.waitFor({state:'hidden'});
  await open('A');await page.getByRole('button',{name:'Select for a conversation…',exact:true}).click();await draft.waitFor();await draft.getByRole('button',{name:'Close',exact:true}).click();await draft.waitFor({state:'hidden'});await page.getByRole('button',{name:'Toggle view'}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.evaluate(()=>fixture.finish(3));await page.waitForTimeout(30);assert.equal(await draft.count(),0,'dismissed/unmounted selection cannot reopen on a late reply');await page.getByRole('button',{name:'Toggle view'}).click();assert.equal(await draft.count(),0);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep while snapshot is pending');assert.deepEqual(errors,[]);reports.push({width,feedbackMs,snapshots:await page.evaluate(()=>fixture.snapshots.length)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual people-list selection and group form, trusted input with held snapshot replies; no native group or invitation created'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
