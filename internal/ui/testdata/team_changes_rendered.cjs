// Every list mutation presents its exact local intent before the signed write.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-team-changes-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';import {TeamsSection} from './src/features/Teams';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),op=new URL(location).searchParams.get('op');
const agent={id:'agent-one',host:'self/laptop',host_key:'fixture-key'},overview={seq:1,version:'fixture',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture owner',state:'self',address:'self/laptop',devices:[]},people:[{person:'peer',label:'Fixture peer',state:'pinned',address:'peer/laptop',devices:[]},{person:'third',label:'Fixture third',state:'pinned',address:'third/laptop',devices:[]}],dms:[],threads:[],needs_you:[]};
const base={version:['join','leave'].includes(op)?1:2,id:'one',realm_id:'fixture',name:'Original list',seq:1,hash:'one',members:op==='join'?['peer']:['self','peer'],managers:op==='manager-remove'?['self','peer']:['join','leave'].includes(op)?['peer']:['self'],agents:op==='agent-remove'?[agent]:[],manager:!['join','leave'].includes(op),member:op!=='join',archived:false,conflict:false,listed:true};
const teams={tags:true,legacy_add:true,current:true,status:'available',at:'2026-01-01T00:00:00Z',truncated:false,teams:[base,{...base,id:'two',name:'Other list'}]};
const listeners=new Set(),f=window.fixture={writes:[],reads:[],holdReads:false,teams,initial:JSON.stringify(base)};
const host={platform:'daemon',workspace:{id:'team-changes',state:'enrolled'},listen(fn){listeners.add(fn);return()=>listeners.delete(fn)},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/teams'){const snapshot=structuredClone(teams);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p==='/api/team')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/workspace'))return {can_rename:true};
 if(p.startsWith('/api/agents'))return {host:'self/laptop',agents:[{enabled:true,record:{...agent,label:'Fixture agent',harness:'codex'}}]};
 if(b)throw Error('Unexpected fixture mutation '+p);return {};
}};
const store=new Store(host);f.store=store;f.emit=()=>{overview.seq++;for(const fn of listeners)fn({type:'change',seq:overview.seq})};
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented list refusal'));return;}
 const t=teams.teams.find(t=>t.id===w.b.team),c=w.b;t.seq++;t.hash='confirmed-'+t.seq;
 if(c.op==='rename')t.name=c.name;if(c.op==='add')t.members.push(c.target);if(c.op==='remove')t.members=t.members.filter(p=>p!==c.target);
 if(c.op==='manager-add')t.managers.push(c.target);if(c.op==='manager-remove')t.managers=t.managers.filter(p=>p!==c.target);
 if(c.op==='agent-add')t.agents.push(c.agent);if(c.op==='agent-remove')t.agents=[];
 if(c.op==='join'){t.members.push('self');t.member=true}if(c.op==='leave'){t.members=t.members.filter(p=>p!=='self');t.member=false}
 if(c.op==='delete')teams.teams=teams.teams.filter(p=>p.id!==t.id);f.emit();w.resolve(structuredClone(t));
};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
f.newer=()=>{teams.teams[0]={...teams.teams[0],name:'Newer signed list',seq:10,hash:'newer'};f.emit()};
f.owner=async()=>{overview.person={...overview.person,person:'other-owner'};overview.seq++;await store.refetch()};
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
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Team changes fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const ops=['rename','add','remove','manager-add','manager-remove','agent-add','agent-remove','join','leave','delete'];
 const capture=async(page,name)=>{if(!process.env.AGENTNET_TEAM_CHANGE_EVIDENCE)return;await page.evaluate(async()=>{await Promise.all(document.getAnimations().filter(a=>Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{})))});await page.screenshot({path:process.env.AGENTNET_TEAM_CHANGE_EVIDENCE+'-'+name+'.png',fullPage:true})};
 const assertPresented=async locator=>{const bounds=await locator.evaluate(el=>{const r=el.getBoundingClientRect();let top=0,bottom=innerHeight,left=0,right=innerWidth;for(let p=el.parentElement;p;p=p.parentElement){const s=getComputedStyle(p),b=p.getBoundingClientRect();if(/auto|scroll|hidden|clip/.test(s.overflowY)){top=Math.max(top,b.top);bottom=Math.min(bottom,b.bottom)}if(/auto|scroll|hidden|clip/.test(s.overflowX)){left=Math.max(left,b.left);right=Math.min(right,b.right)}}return{shown:r.width>0&&r.height>0&&r.top>=top-1&&r.bottom<=bottom+1&&r.left>=left-1&&r.right<=right+1,rect:{top:r.top,bottom:r.bottom,left:r.left,right:r.right},clip:{top,bottom,left,right}}});assert(bounds.shown,'feedback must be inside the actual viewport and scrollport: '+JSON.stringify(bounds))};
 const open=async(page,name='Original list')=>{await page.getByRole('button',{name:new RegExp('^'+name)}).click();await page.getByRole('dialog',{name,exact:true}).waitFor()};
 const prepare=async(page,op)=>{
  await open(page);const sheet=page.getByRole('dialog',{name:'Original list',exact:true}),peer=sheet.getByRole('list',{name:'Members',exact:true}).locator('li').filter({hasText:'Fixture peer'});
  if(op==='rename'){await sheet.getByRole('textbox',{name:'New name',exact:true}).fill('Requested list');return sheet.getByRole('button',{name:'Rename list',exact:true})}
  if(op==='add'){await sheet.getByRole('combobox',{name:'Person',exact:true}).selectOption('third');return sheet.getByRole('button',{name:'Add person',exact:true})}
  if(op==='agent-add'){await page.waitForFunction(()=>!document.querySelector('select[aria-label="Agent"]').disabled);await sheet.getByRole('combobox',{name:'Agent',exact:true}).selectOption('agent-one');return sheet.getByRole('button',{name:'Add agent',exact:true})}
  if(op==='agent-remove')return sheet.getByRole('button',{name:'Remove agent',exact:true});
  if(op==='join'||op==='leave')return sheet.getByRole('button',{name:op==='join'?'Join list':'Leave list',exact:true});
  const button=op==='delete'?sheet.getByRole('button',{name:'Delete list',exact:true}):peer.getByRole('button',{name:op==='remove'?'Remove':op==='manager-add'?'Make manager':'Take manager role',exact:true});await button.click();return page.getByRole('button',{name:'Confirm change',exact:true});
 };
 for(const width of [1440,390])for(const op of ops){
  const page=await browser.newPage({viewport:{width,height:960}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?op='+op);const button=await prepare(page,op),pending=page.getByRole('region',{name:'Pending change to Original list',exact:true});await button.scrollIntoViewIfNeeded();await button.evaluate(b=>b.addEventListener('click',e=>fixture.input={at:performance.now(),trusted:e.isTrusted},{once:true,capture:true}));await button.click();await pending.waitFor({timeout:500});
  const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return{...fixture.input,feedback:performance.now()-fixture.input.at}});assert(timing.trusted);assert(timing.feedback<500,JSON.stringify({width,op,timing}));
  await assertPresented(pending);
  const sheet=page.getByRole('dialog',{name:op==='rename'?'Requested list':'Original list',exact:true});
  if(op==='rename')await sheet.waitFor();
  if(op==='add')await sheet.getByRole('list',{name:'Members',exact:true}).locator('li').filter({hasText:'Fixture third'}).getByText('Adding…',{exact:true}).waitFor();
  if(op==='remove')assert.equal(await sheet.getByRole('list',{name:'Members',exact:true}).locator('li').filter({hasText:'Fixture peer'}).count(),0);
  if(op==='manager-add'||op==='manager-remove')await sheet.getByText(op==='manager-add'?'Adding manager role…':'Removing manager role…',{exact:true}).waitFor();
  if(op==='agent-add')await sheet.getByRole('list',{name:'Agents in this list',exact:true}).getByText('Adding…',{exact:true}).waitFor();
  if(op==='agent-remove')assert.equal(await sheet.getByRole('list',{name:'Agents in this list',exact:true}).locator('li').count(),0);
  if(op==='join')await sheet.getByRole('list',{name:'Members',exact:true}).getByText('Joining…',{exact:true}).waitFor();
  if(op==='leave'||op==='delete')assert.equal(await page.getByRole('dialog').count(),0);if(op==='delete')assert.equal(await page.getByRole('button',{name:/^Original list/}).count(),0);
  assert.equal(await page.evaluate(()=>JSON.stringify(fixture.teams.teams[0])===fixture.initial),true,'local intent does not change canonical members, managers, agents or identity');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);
  assert.equal(await page.evaluate(()=>{const p=fixture.store.get().teamChanges.one,a=fixture.store.changePeopleList(p.change,p.original),b=fixture.store.changePeopleList(p.change,p.original);a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1);
  await capture(page,op+'-pending-'+width);
  await page.evaluate(()=>fixture.finish(0,true));await pending.getByRole('alert').waitFor();assert.match(await pending.getByRole('alert').textContent(),/Invented list refusal/);
  await assertPresented(pending);
  await capture(page,op+'-error-'+width);
  await pending.getByRole('button',{name:'Check list change',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),1);await pending.getByRole('button',{name:'Retry list change',exact:true}).click();await pending.getByRole('status').waitFor();assert.deepEqual(await page.evaluate(()=>fixture.writes[0].b),await page.evaluate(()=>fixture.writes[1].b));
  if(await page.getByRole('dialog').count())await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep while list changes');await page.getByRole('button',{name:'Toggle view'}).click();await page.getByText('Another view',{exact:true}).waitFor();await page.getByRole('button',{name:'Toggle view'}).click();await pending.waitFor();await open(page,'Other list');
  await page.evaluate(()=>{fixture.holdReads=true;fixture.finish(1)});await page.waitForFunction(()=>fixture.store.get().teamChanges?.one?.saved);assert.equal(await page.getByRole('dialog',{name:'Other list',exact:true}).count(),1,'old list reply cannot replace the selected list');await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();await pending.getByText('List change saved; checking current state…',{exact:true}).waitFor();await pending.getByRole('button',{name:'Check list change',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),2);await page.evaluate(()=>fixture.release());await pending.waitFor({state:'hidden'});assert.equal(await page.getByRole('dialog').count(),0);assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep while list changes');assert.deepEqual(errors,[]);reports.push({width,op,feedbackMs:timing.feedback,writes:2});await page.close();
 }
 for(const scope of ['newer','owner','stopped']){
  const page=await browser.newPage({viewport:{width:390,height:960}});await page.goto('http://127.0.0.1:'+server.address().port+'/?op=rename');await(await prepare(page,'rename')).click();await page.getByRole('region',{name:'Pending change to Original list',exact:true}).waitFor();
  if(scope==='newer'){await page.evaluate(()=>fixture.newer());await page.getByRole('dialog',{name:'Newer signed list',exact:true}).waitFor();}
  if(scope==='owner')await page.evaluate(()=>fixture.owner());
  if(scope==='stopped')assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(0);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true);
  else{await page.evaluate(()=>fixture.finish(0,true));await page.waitForTimeout(30);assert.equal(await page.getByRole('region',{name:'Pending change to Original list',exact:true}).count(),0);assert.equal(await page.evaluate(()=>fixture.store.get().toasts.some(t=>t.text.includes('Invented list refusal'))),false)}await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'ten actual list controls at desktop/phone widths, held writes and reads, failure/exact retry/duplicate input/navigation and separate newer-state/owner/teardown guards; no native list mutation'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
