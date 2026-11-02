// Agent dismissals run side by side: each card waits on its own request, as
// tdesktop's kickParticipant; the room never locks. Synthetic data only.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-agent-dismissal-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore,unsentIn} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {RoomPanel,RoomSheet} from './src/features/RoomPanel';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const self={person:'self',label:'Fixture owner',address:'self/laptop',fingerprint:'self-key',state:'self',admin:true},other={person:'peer',label:'Fixture peer',address:'peer/desktop',fingerprint:'peer-key',state:'pinned'};
const agent=(pid,id)=>({pid,host:other,agent_id:id.repeat(32),state:'active',state_text:'Active',host_here:false,shared:[],can_ask:true,can_dismiss:true,inviter:self,invited:'2026-10-05T10:00:00Z',tasks_from:[]});
const group={seq:1,id:'group-one',kind:'group',title:'Fixture group',role:'member',members:[self,other],peer:other,created:'2026-01-01T00:00:00Z',messages:[],agents:[agent('agent-one','1'),agent('agent-two','2'),agent('agent-three','3')],guests:[]};
const overview={seq:1,version:'fixture',role:'person',groups:true,agents:true,me:{address:self.address,fingerprint:self.fingerprint},person:{...self,devices:[]},people:[other],dms:[group],threads:[],needs_you:[],agent_devices:[other.address]};
const f=window.fixture={writes:[],reads:0};
const host={platform:'daemon',workspace:{id:'agent-dismissal',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')){f.reads++;return structuredClone(group)}
 if(p==='/api/dm/agent/dismiss')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {typists:[]};return {};
}};
const store=new Store(host);f.store=store;f.unsent=()=>unsentIn(store.get());
f.end=pid=>{Object.assign(group.agents.find(a=>a.pid===pid),{state:'dismissed',state_text:'Dismissed',can_dismiss:false,can_ask:false});group.seq++;overview.seq++};
f.finish=(pid,fail=false)=>{const w=f.writes.find(w=>w.b.pid===pid&&!w.done);w.done=true;if(fail){w.reject(Error('Invented dismissal refusal'));return;}f.end(pid);w.resolve(structuredClone(group.agents.find(a=>a.pid===pid)))};
f.elsewhere=async pid=>{f.end(pid);await store.refreshOpen()};
function App(){const s=useStore(store,s=>s);return <><button onClick={()=>store.setPanel(true)}>Open room</button><textarea aria-label="Unrelated draft"/><div data-errors role="alert">{s.toasts.map(t=>t.text).join(' ')}</div><RoomPanel/><RoomSheet/></>}
await store.start();await store.openChat({kind:'dm',id:'group-one'});store.setPanel(true);createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Agent dismissal fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);
  const dismiss=()=>page.getByRole('button',{name:'Dismiss',exact:true}),waiting=()=>page.getByRole('button',{name:'Dismissing…',exact:true});
  const writes=()=>page.evaluate(()=>fixture.writes.map(w=>w.b.pid));
  await dismiss().first().waitFor();assert.equal(await dismiss().count(),3);
  // Three clicks in a row, each answered on screen at once, none waiting for the previous request.
  const timings=[];
  for(let i=0;i<3;i++){await dismiss().first().click({trial:true});const start=Date.now();await dismiss().first().click();await page.waitForFunction(n=>document.querySelectorAll('button').length&&[...document.querySelectorAll('button')].filter(b=>b.textContent==='Dismissing…').length===n,i+1,{timeout:500});timings.push(Date.now()-start);}
  assert(timings.every(ms=>ms<500),'each dismissal shows at once: '+timings);assert.deepEqual(await writes(),['agent-one','agent-two','agent-three'],'every dismissal is sent without waiting for the one before');
  assert.equal(await dismiss().count(),0);assert(await waiting().first().isDisabled(),'a dismissal in flight cannot be clicked again');
  assert.equal(await page.evaluate(()=>{const a=fixture.store.api.dismissAgent('agent-one'),b=fixture.store.api.dismissAgent('agent-one');a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal((await writes()).length,3,'duplicate input sends one dismissal');
  assert.equal(await page.evaluate(()=>fixture.store.get().dm.agents.filter(a=>a.state==='active').length),3,'pending dismissal never changes the reported participation');assert.equal(await page.evaluate(()=>fixture.unsent()),true);
  assert.equal(await page.getByRole('status').filter({hasText:'left · dismissed by you'}).count(),0,'no dismissal is claimed before it is confirmed');
  if(process.env.AGENTNET_AGENT_DISMISSAL_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_AGENT_DISMISSAL_EVIDENCE+'-pending-'+width+'.png',fullPage:true});
  const draft=width>=1024; // a phone's room sheet is modal: the draft behind it is inert
  if(draft)await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retain while dismissals run');
  // The second answers first; the others keep waiting.
  await page.evaluate(()=>fixture.finish('agent-two'));await page.getByRole('status').filter({hasText:'left · dismissed by you'}).waitFor();assert.equal(await waiting().count(),2,'the other dismissals still show their wait');
  // A refusal restores only its own control, with the reason.
  await page.evaluate(()=>fixture.finish('agent-three',true));await dismiss().waitFor();assert.equal(await dismiss().count(),1);assert.match(await page.locator('[data-errors]').textContent(),/Invented dismissal refusal/);assert.equal(await waiting().count(),1);
  // Ended elsewhere meanwhile: a late refusal cannot bring its control back.
  await page.evaluate(()=>fixture.elsewhere('agent-one'));await page.evaluate(()=>fixture.finish('agent-one',true));await page.waitForTimeout(30);assert.equal(await waiting().count(),0);assert.equal(await dismiss().count(),1,'only the refused agent offers Dismiss again');
  assert.equal(await page.evaluate(()=>fixture.unsent()),false,'nothing waits once every dismissal answered');
  if(process.env.AGENTNET_AGENT_DISMISSAL_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_AGENT_DISMISSAL_EVIDENCE+'-settled-'+width+'.png',fullPage:true});
  if(draft)assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retain while dismissals run');assert.deepEqual(errors,[]);
  reports.push({width,feedbackMs:timings,writes:(await writes()).length});
  await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual room cards and Store, trusted input with held synthetic dismissals; no participation changed'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
