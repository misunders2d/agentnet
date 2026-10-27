// Actual list selection and group form: pending snapshots never replace later choices.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-team-people-'));
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {TeamPeople} from './src/features/Teams';import {NewGroupForm} from './src/features/NewChat';import {Sheet} from './src/ui/Sheet';
import {InviteSheet} from './src/features/InviteSheet';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),mode=new URL(location).searchParams.get('mode');
const people=['Bob','Carol','Dave'].map(n=>({person:n.toLowerCase(),label:'Fixture '+n,state:'pinned',address:n.toLowerCase()+'/laptop',devices:[]}));
const overview={seq:1,version:'fixture',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture owner',state:'self',address:'self/laptop',devices:[]},people,dms:[],threads:[],needs_you:[]};
const thread={id:'fixture-conversation',kind:'group',title:'Fixture group',role:'member',members:[{...overview.person,admin:true}],peer:{label:'Fixture group'},messages:[],agents:[],guests:[],topics:[]};
const teams={tags:true,legacy_add:true,current:true,status:'available',at:'2026-01-01T00:00:00Z',truncated:false,teams:['A','B'].map((name,i)=>({version:2,id:'list-'+i,name:'Fixture list '+name,members:i?['dave']:['self','bob','carol'],managers:['self'],agents:[],manager:true,member:true,listed:true,archived:false,conflict:false}))};
const f=window.fixture={snapshots:[],writes:[],changes:[]};
const host={platform:'daemon',workspace:{id:'team-people',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?'))return structuredClone(thread);
 if(p==='/api/dm/guest/check')return {allowed:true};
 if(p==='/api/teams')return structuredClone(teams);
 if(p==='/api/teams/snapshot')return new Promise((resolve,reject)=>f.snapshots.push({b,resolve,reject}));
 if(p.startsWith('/api/workspace'))return {can_rename:true};if(p.startsWith('/api/agents'))return {agents:[]};
 if(b){f.writes.push({p,b});throw Error('Unexpected fixture mutation '+p)}return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.snapshots[index];if(fail){w.reject(Error('Invented list snapshot refusal'));return;}
 const members=teams.teams.find(t=>t.id===w.b.teams[0]).members;w.resolve({persons:members.map(id=>({id})),teams:w.b.teams,at:1790000000});
};
f.owner=async()=>{overview.person={...overview.person,person:'other-owner'};overview.seq++;await store.refetch()};
function App(){const [selected,setSelected]=useState([{id:'bob'}]),[excluded,setExcluded]=useState([]),[disabled,setDisabled]=useState(false),[mounted,setMounted]=useState(true),[pending,setPending]=useState(false);
 f.selected=selected;f.pending=pending;f.excluded=excluded;f.exclude=setExcluded;f.disable=setDisabled;f.mount=setMounted;
 if(mode==='invite')return <InviteSheet/>;
 return <Sheet open title="Choose group people" onOpenChange={()=>setMounted(false)}>{mounted?(mode==='group'?<NewGroupForm initialPeople={[{id:'bob'}]} onBack={()=>setMounted(false)} onCreated={()=>{throw Error('Unexpected group')}}/>:<><TeamPeople selected={selected} onChange={p=>{f.changes.push(p);setSelected(p)}} excluded={excluded} disabled={disabled} onPendingChange={setPending}/><button onClick={()=>setSelected(p=>p.some(x=>x.id==='dave')?p:[...p,{id:'dave'}])}>Select Dave separately</button><button disabled={pending}>Invite reviewed people</button></>):<p>Selection closed</p>}<textarea aria-label="Unrelated draft"/></Sheet>
}
await store.start();if(mode==='invite')store.set({invite:{conv:thread.id}});createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(process.env.AGENTNET_TEAM_PEOPLE_SOURCE)fs.copyFileSync(process.env.AGENTNET_TEAM_PEOPLE_SOURCE,path.join(work,'src/features/Teams.tsx'));
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Team people fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const choose=async(page,list='list-0')=>{await page.getByRole('combobox',{name:'People list',exact:true}).selectOption(list);const button=page.getByRole('button',{name:'Add list’s people',exact:true});await button.scrollIntoViewIfNeeded();await button.evaluate(b=>b.addEventListener('click',e=>fixture.input={at:performance.now(),trusted:e.isTrusted},{once:true,capture:true}));await button.click()};
 const shown=async locator=>{const v=await locator.evaluate(el=>{const r=el.getBoundingClientRect();let top=0,bottom=innerHeight;for(let p=el.parentElement;p;p=p.parentElement)if(/auto|scroll|hidden|clip/.test(getComputedStyle(p).overflowY)){const b=p.getBoundingClientRect();top=Math.max(top,b.top);bottom=Math.min(bottom,b.bottom)}return r.top>=top-1&&r.bottom<=bottom+1&&r.left>=0&&r.right<=innerWidth});assert(v,'selection must be inside viewport and scrollport')};
 const capture=async(page,name)=>{if(!process.env.AGENTNET_TEAM_PEOPLE_EVIDENCE)return;await page.evaluate(async()=>Promise.all(document.getAnimations().filter(a=>Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{}))));await page.screenshot({path:process.env.AGENTNET_TEAM_PEOPLE_EVIDENCE+'-'+name+'.png',fullPage:true})};
 for(const width of [1440,390])for(const mode of ['list','group']){
  const page=await browser.newPage({viewport:{width,height:960}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port+'/?mode='+mode);
  if(mode==='group')await page.getByRole('textbox',{name:'Group name',exact:true}).fill('Keep this group title');
  const submit=page.getByRole('button',{name:mode==='group'?'Create group':'Invite reviewed people',exact:true});assert.equal(await submit.isEnabled(),true);
  await choose(page);const pending=page.getByRole('region',{name:'Pending selection from Fixture list A',exact:true});await pending.waitFor({timeout:500});const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return{...fixture.input,feedback:performance.now()-fixture.input.at}});assert(timing.trusted);assert(timing.feedback<500,JSON.stringify({width,mode,timing}));await shown(pending);assert.equal(await submit.isDisabled(),true);assert.equal(await page.getByRole('combobox',{name:'People list',exact:true}).inputValue(),'');assert.equal(await page.evaluate(()=>fixture.snapshots.length),1);assert.equal(await page.getByRole('button',{name:'Add list’s people',exact:true}).isDisabled(),true);
  await capture(page,mode+'-pending-'+width);
  // Independent recipient changes remain usable and cannot be overwritten by the snapshot.
  await page.getByRole('button',{name:'Remove Fixture Bob',exact:true}).click();
  if(mode==='group')await page.getByRole('checkbox',{name:'Fixture Dave',exact:true}).check();else await page.getByRole('button',{name:'Select Dave separately',exact:true}).click();
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep this draft');await page.evaluate(()=>fixture.finish(0,true));await pending.getByRole('alert').waitFor();await shown(pending);assert.equal(await submit.isDisabled(),true);await capture(page,mode+'-error-'+width);
  await pending.getByRole('button',{name:'Retry list selection',exact:true}).click();assert.deepEqual(await page.evaluate(()=>fixture.snapshots.map(x=>x.b.teams)),[['list-0'],['list-0']]);
  if(mode==='list'){await page.evaluate(()=>fixture.exclude(['carol']));await page.waitForFunction(()=>fixture.excluded.includes('carol'))}await page.evaluate(()=>fixture.finish(1));await pending.waitFor({state:'hidden'});assert.equal(await submit.isEnabled(),true);assert.equal(await page.getByRole('button',{name:'Remove Fixture Bob',exact:true}).count(),0,'later removal must not be resurrected');assert.equal(await page.getByRole('button',{name:'Remove Fixture Dave',exact:true}).count(),1,'later choice must survive');assert.equal(await page.getByRole('button',{name:'Remove Fixture Carol',exact:true}).count(),mode==='group'?1:0,'current exclusions apply');
  await choose(page);await pending.waitFor();await pending.getByRole('button',{name:'Cancel list selection',exact:true}).click();await pending.waitFor({state:'hidden'});await choose(page,'list-1');const next=page.getByRole('region',{name:'Pending selection from Fixture list B',exact:true});await next.waitFor();await page.evaluate(()=>fixture.finish(2));await page.waitForTimeout(30);assert.equal(await next.count(),1,'cancelled response cannot replace the next selection');assert.equal(await page.getByRole('button',{name:'Remove Fixture Bob',exact:true}).count(),0);await next.getByRole('button',{name:'Cancel list selection',exact:true}).click();await page.evaluate(()=>fixture.finish(3));await page.waitForTimeout(30);assert.equal(await next.count(),0);assert.equal(await submit.isEnabled(),true);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep this draft');if(mode==='group')assert.equal(await page.getByRole('textbox',{name:'Group name',exact:true}).inputValue(),'Keep this group title');assert.equal(await page.evaluate(()=>fixture.writes.length),0);assert.deepEqual(errors,[]);reports.push({width,mode,feedbackMs:timing.feedback});await page.close();
 }
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:960}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port+'/?mode=invite');await page.getByRole('radio',{name:/Add as a permanent member/}).check();
  await choose(page);await page.evaluate(()=>fixture.finish(0));const submit=page.getByRole('button',{name:'Invite selected team people',exact:true});await submit.waitFor();assert.equal(await submit.isEnabled(),true);
  await choose(page,'list-1');const pending=page.getByRole('region',{name:'Pending selection from Fixture list B',exact:true});await pending.waitFor({timeout:500});const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return{...fixture.input,feedback:performance.now()-fixture.input.at}});assert(timing.trusted);assert(timing.feedback<500);await shown(pending);assert.equal(await submit.isDisabled(),true);await capture(page,'invite-pending-'+width);
  await page.getByRole('button',{name:'Remove Fixture Bob',exact:true}).click();await page.evaluate(()=>fixture.finish(1,true));await pending.getByRole('alert').waitFor();await shown(pending);assert.equal(await submit.isDisabled(),true);await capture(page,'invite-error-'+width);await pending.getByRole('button',{name:'Cancel list selection',exact:true}).click();assert.equal(await submit.isEnabled(),true);
  await choose(page,'list-1');await pending.waitFor();await page.getByRole('button',{name:'Close',exact:true}).click();await page.getByRole('dialog',{name:'Bring someone in',exact:true}).waitFor({state:'hidden'});await page.evaluate(()=>fixture.finish(2));await page.evaluate(()=>fixture.store.set({invite:{conv:'fixture-conversation'}}));await page.getByRole('radio',{name:/Add as a permanent member/}).check();assert.equal(await page.getByRole('button',{name:'Remove Fixture Dave',exact:true}).count(),0);assert.equal(await submit.count(),0,'closed flow cannot supply selections to its replacement');assert.equal(await page.evaluate(()=>fixture.writes.length),0);assert.deepEqual(errors,[]);reports.push({width,mode:'invite',feedbackMs:timing.feedback});await page.close();
 }
 for(const scope of ['owner','disabled','unmount','stopped']){
  const page=await browser.newPage({viewport:{width:390,height:960}});await page.goto('http://127.0.0.1:'+server.address().port+'/?mode=list');await choose(page);await page.getByRole('region',{name:'Pending selection from Fixture list A',exact:true}).waitFor();
  if(scope==='owner')await page.evaluate(()=>fixture.owner());if(scope==='disabled'){await page.evaluate(()=>fixture.disable(true));await page.getByRole('region',{name:'Pending selection from Fixture list A',exact:true}).waitFor({state:'hidden'})}if(scope==='unmount'){await page.getByRole('button',{name:'Close',exact:true}).click();await page.getByText('Selection closed',{exact:true}).waitFor()}if(scope==='stopped')await page.evaluate(()=>fixture.store.stop());await page.evaluate(()=>fixture.finish(0));await page.waitForTimeout(40);assert.equal(await page.evaluate(()=>fixture.changes.length),0,'late '+scope+' snapshot cannot change selected people');await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual TeamPeople, NewGroupForm and InviteSheet at desktop/phone widths: held read, failure, exact retry, cancellation, current choices/exclusions, owner/unmount/stop guards; no invitation or group write'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
