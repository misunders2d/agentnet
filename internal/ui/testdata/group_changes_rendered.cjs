// Actual group controls: local feedback never grants role or membership authority.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-group-changes-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore,unsentIn} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {RoomPanel,RoomSheet} from './src/features/RoomPanel';import {GroupChanges,groupRights} from './src/features/GroupAdmin';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const action=new URL(location.href).searchParams.get('action'),self={person:'self',label:'Fixture owner',address:'self/laptop',state:'self',admin:true},other={person:'peer',label:'Fixture peer',address:'peer/laptop',state:'pinned',admin:action==='demote'};
const group={seq:1,id:'group-one',kind:'group',title:'Fixture group',role:'member',members:[self,other],peer:other,created:'2026-01-01T00:00:00Z',messages:[],agents:[],guests:[]};
const second={...group,id:'group-two',title:'Other group',members:structuredClone(group.members)};
const overview={seq:1,version:'fixture',role:'person',groups:true,me:{address:self.address},person:{...self,devices:[]},people:[other],dms:[group,second],threads:[],needs_you:[]};
const f=window.fixture={writes:[],reads:0,group,overview};
const host={platform:'daemon',workspace:{id:'group-changes',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')){f.reads++;return structuredClone(p.includes('group-two')?second:group)}
 if(p==='/api/groups/manage')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {typists:[]};return {};
}};
const store=new Store(host);f.store=store;f.unsent=()=>unsentIn(store.get());f.rights=()=>groupRights(store.get().dm,store.get().overview);
f.finish=(index,fail=false,apply=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented group mutation refusal'));return;}
 if(apply){const c=w.b;if(c.action==='rename')group.title=c.title;else if(c.action==='promote'||c.action==='demote')group.members.find(m=>m.person===c.person).admin=c.action==='promote';else if(c.action==='remove')group.members=group.members.filter(m=>m.person!==c.person);else group.role='left';group.seq++;overview.seq++}w.resolve({queued:true});
};
f.newer=async()=>{group.title='Newer signed name';group.members[1].admin=true;group.seq++;overview.seq++;await store.refetch()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.setPanel(true)}>Open members</button><button onClick={()=>void store.openChat({kind:'dm',id:'group-two'})}>Other conversation</button><button onClick={()=>void store.openChat({kind:'dm',id:'group-one'})}>Original conversation</button><h1>{s.dm?.title||'Chats'}</h1><GroupChanges/><textarea aria-label="Unrelated draft"/><RoomPanel/><RoomSheet/></>}
await store.start();await store.openChat({kind:'dm',id:'group-one'});createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Group change fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
 for(const width of [1440,390])for(const action of ['rename','promote','demote','remove','leave']){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?action='+action);
  const panel=width===390?page.getByRole('dialog',{name:'In this chat',exact:true}):page.getByRole('complementary',{name:'In this chat'});
  const closePanel=async()=>{if(await panel.isVisible())await panel.getByRole('button',{name:width===390?'Close':'Close panel',exact:true}).click()};
  const open=async()=>{
   await page.getByRole('button',{name:'Open members',exact:true}).click();await panel.waitFor();
   if(action==='rename')await panel.getByRole('button',{name:'Rename group…',exact:true}).click();
   else if(action==='leave')await panel.getByRole('button',{name:'Leave group…',exact:true}).click();
   else{await panel.getByRole('button',{name:'Change Fixture peer in Fixture group',exact:true}).click();await page.getByRole('menuitem',{name:action==='promote'?'Make admin…':action==='demote'?'Remove admin role…':'Remove from group…',exact:true}).click()}
  };
  await open();const label={rename:'Rename',promote:'Make admin',demote:'Remove admin role',remove:'Remove',leave:'Leave group'}[action];
  const dialog=page.getByRole('dialog').filter({has:page.getByRole('button',{name:label,exact:true})}).last();
  if(action==='rename')await dialog.getByRole('textbox',{name:'Group name',exact:true}).fill('Requested group name');
  const button=dialog.getByRole('button',{name:label,exact:true});await button.waitFor();const box=await button.boundingBox();
  await page.evaluate(({width,action})=>{
   window.inputTiming={};
   document.addEventListener('click',e=>{
    const timing=window.inputTiming={at:performance.now(),trusted:e.isTrusted,target:e.target.textContent?.slice(0,120)};
    const text=action==='rename'?(width===390?'Name: “Requested group name”':'Requested group name'):action==='leave'?'Chats':action==='promote'?'Admin pending':action==='demote'?'Member role pending':'Fixture peer · Removal pending';
    const selector=action==='leave'||action==='rename'&&width!==390?'h1,h2,h3,[role=heading]':'p,span,strong';
    const frame=()=>{
     const visible=[...document.querySelectorAll(selector)].some(node=>node.textContent===text&&node.getClientRects().length);
     if(visible){timing.domAt=performance.now();requestAnimationFrame(()=>{timing.feedbackAt=performance.now()});}
     else if(performance.now()-timing.at<1000)requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
   },{capture:true,once:true});
  },{width,action});
  const start=Date.now();await page.mouse.click(box.x+box.width/2,box.y+box.height/2);
  const local=action==='rename'?(width===390?panel.getByText('Name: “Requested group name”',{exact:true}):page.getByRole('heading',{name:'Requested group name',exact:true})):action==='leave'?page.getByRole('heading',{name:'Chats',exact:true}):panel.getByText(action==='promote'?'Admin pending':action==='demote'?'Member role pending':'Fixture peer · Removal pending',{exact:true});
  await local.waitFor({timeout:500}).catch(async e=>{console.error(JSON.stringify({width,action,...await page.evaluate(()=>({input:window.inputTiming,writes:fixture.writes.length,changes:fixture.store.get().groupChanges,title:fixture.store.get().dm?.title}))}));await page.screenshot({path:'/tmp/agentnet-group-change-failure.png'});throw e});await page.waitForFunction(()=>Number.isFinite(window.inputTiming.feedbackAt),{},{timeout:500});const input=await page.evaluate(()=>window.inputTiming);const feedbackMs=Date.now()-start;assert(input.trusted);assert(input.feedbackAt-input.at<500,action+' event-to-feedback '+(input.feedbackAt-input.at));
  assert.equal(await page.evaluate(()=>fixture.writes.length),1);assert.equal(await page.evaluate(()=>fixture.unsent()),true);
  assert.equal(await page.evaluate(()=>fixture.store.get().views['group-one'].title),'Fixture group','local title is not authoritative cached group');
  assert.deepEqual(await page.evaluate(()=>fixture.store.get().views['group-one'].members.map(m=>[m.person,m.admin])),[['self',true],['peer',action==='demote']]);
  if(process.env.AGENTNET_GROUP_CHANGES_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_GROUP_CHANGES_EVIDENCE+'-'+action+'-pending-'+width+'.png',fullPage:true});
  await closePanel();await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep through group change');await page.getByRole('button',{name:'Other conversation',exact:true}).click();await page.getByRole('heading',{name:'Other group',exact:true}).waitFor();
  await page.evaluate(()=>fixture.finish(0,true));await page.getByRole('alert').filter({hasText:'Invented group mutation refusal'}).waitFor();assert.equal(await page.getByRole('heading').textContent(),'Other group','failure cannot navigate back');assert.equal(await page.evaluate(()=>fixture.unsent()),false);
  if(process.env.AGENTNET_GROUP_CHANGES_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_GROUP_CHANGES_EVIDENCE+'-'+action+'-error-'+width+'.png',fullPage:true});
  await page.getByRole('button',{name:'Try group change again',exact:true}).click();await page.getByRole('status').filter({hasText:'Saving this change'}).waitFor();await page.evaluate(()=>{void fixture.store.api.manageGroup(fixture.writes[1].b).catch(()=>{})});assert.equal(await page.evaluate(()=>fixture.writes.length),2,'duplicate pending input shares one write');
  await page.evaluate(()=>fixture.finish(1));await page.getByRole('button',{name:'Check group change',exact:true}).waitFor();assert.equal(await page.getByRole('heading').textContent(),'Other group','late leave acknowledgement cannot close another chat');
  const before=await page.evaluate(()=>fixture.reads);await page.getByRole('button',{name:'Check group change',exact:true}).click();await page.waitForFunction(n=>fixture.reads>n,before);assert.equal(await page.evaluate(()=>fixture.writes.length),2,'confirmation is read only');
  await page.evaluate(()=>{fixture.finish(1,false,true)});await page.getByRole('button',{name:'Check group change',exact:true}).click();await page.getByRole('region',{name:'Group changes',exact:true}).waitFor({state:'hidden'});
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep through group change');assert.deepEqual(errors,[]);reports.push({width,action,feedbackMs,eventToFeedbackMs:input.feedbackAt-input.at,trustedInput:input.trusted});await page.close();
 }
 // New signed state and workspace teardown defeat late mutation replies.
 for(const teardown of [false,true]){
  const page=await browser.newPage();await page.goto('http://127.0.0.1:'+server.address().port+'/?action=rename');await page.getByRole('heading',{name:'Fixture group',exact:true}).waitFor();
  await page.evaluate(()=>{void fixture.store.api.manageGroup({conv:'group-one',action:'rename',title:'Pending older name'}).catch(()=>{})});await page.getByRole('heading',{name:'Pending older name',exact:true}).waitFor();
  if(teardown){assert.equal(await page.evaluate(async()=>{fixture.store.stop();const before=fixture.store.get();fixture.finish(0);await new Promise(r=>setTimeout(r,30));return fixture.store.get()===before}),true)}
  else{await page.evaluate(()=>fixture.newer());await page.getByRole('heading',{name:'Newer signed name',exact:true}).waitFor();assert.match(await page.evaluate(async()=>{try{await fixture.store.api.manageGroup({conv:'group-one',action:'rename',title:'Another requested name'});return ''}catch(e){return e.message}}),/Wait for this group change/);assert.equal(await page.evaluate(()=>fixture.writes.length),1);await page.evaluate(()=>fixture.finish(0,true));assert.equal(await page.getByRole('alert').count(),0);assert.equal(await page.getByRole('heading').textContent(),'Newer signed name')}
  await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual group controls and Store, trusted input with held writes; no native membership or role changed'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
