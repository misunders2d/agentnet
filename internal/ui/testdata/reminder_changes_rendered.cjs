// Actual Store and reminder controls: held writes, rejection and newer linked state.
// Invented data only. Trusted input; timings are event-to-DOM, not physical paint.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-reminder-changes-'));
const evidence=process.env.AGENTNET_REMINDER_CHANGES_EVIDENCE;
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {Reminders,RemindSheet} from './src/features/Reminders';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const conv='c'.repeat(64),count=Number(new URL(location).searchParams.get('count'))||100;
const original={message:'target',conv,from:'peer/laptop',title:'Target reminder',due:'2026-01-01T12:00:00Z',overdue:true,revision:1};
const overview={seq:1,version:'fixture',me:{address:'self/laptop',fingerprint:'self-key'},person:{person:'self',label:'Me',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[],remind:true,reminders:[original,{...original,message:'other',title:'Independent reminder'}]};
const view={seq:1,id:conv,kind:'dm',peer:{person:'peer',address:'peer/laptop',label:'Peer'},agents:[],guests:[],topics:[],messages:Array.from({length:count},(_,i)=>({id:String(i),dir:'in',from:'peer/laptop',kind:'message',body:'History '+i,at:'2026-01-01T10:00:00Z'}))};
const f=window.fixture={writes:[],reads:[],holdReads:false,failReads:false,legacy:false,timings:[]};
const host={platform:'daemon',workspace:{id:'reminder-changes',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));if(f.failReads)throw Error('Invented read failure');return snapshot;}
 if(p.startsWith('/api/dm?'))return structuredClone(view);
 if(p.startsWith('/api/remind'))return new Promise((resolve,reject)=>f.writes.push({p,b:structuredClone(b),resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented reminder refusal'));return;}
 const current=overview.reminders.find(r=>r.message===w.b.id),expected=w.b.expected;
 if(expected&&(expected.revision===0?!!current:current?.revision!==expected.revision)){w.reject(Error('That reminder changed; refresh and try again.'));return;}
 const r=overview.reminders.find(r=>r.message===w.b.id)||{...original,message:w.b.id,title:'New reminder'};
 overview.reminders=overview.reminders.filter(r=>r.message!==w.b.id);
 if(w.p==='/api/remind')overview.reminders.push({...r,due:new Date(w.b.due*1000).toISOString(),overdue:false,revision:r.revision+1});
 overview.seq++;w.resolve(f.legacy?{note:'Saved'}:{note:'Saved',seq:overview.seq,message:w.b.id,reminder:structuredClone(overview.reminders.find(r=>r.message===w.b.id)||null)});
};
f.newer=async()=>{overview.reminders=overview.reminders.filter(r=>r.message!=='target');overview.reminders.push({...original,revision:++original.revision});overview.seq++;await store.refetch()};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const [open,setOpen]=useState(false),o=useStore(store,s=>s.overview),tab=useStore(store,s=>s.tab);
 return <div className="p-4"><nav><button onClick={()=>store.showTab(tab==='settings'?'chats':'settings')}>Toggle settings</button><span data-current-tab>{tab}</span><span data-due>{o?.reminders?.filter(r=>r.overdue).length}</span></nav>
 <button onClick={()=>setOpen(true)}>New reminder</button><RemindSheet open={open} onOpenChange={setOpen} m={{id:'new',text:'New reminder'}}/>
 <Reminders/><textarea aria-label="Unrelated draft" onChange={e=>store.setDraft(conv,{text:e.target.value})}/></div>;
}
await store.start();await store.open({kind:'dm',id:conv});
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(path.join(web,'node_modules/.bin/tailwindcss'),['-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(u.pathname==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Reminder changes fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}nav,form{padding:12px}button{min-height:44px}textarea{border:1px solid;width:95%}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser,lastPage;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});if(evidence)fs.mkdirSync(evidence,{recursive:true});
 for(const width of [1440,390])for(const count of [100,1000]){
  const page=await browser.newPage({viewport:{width,height:900},hasTouch:width<600}),errors=[];lastPage=page;page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  await page.goto('http://127.0.0.1:'+server.address().port+'/?count='+count);
  const row=()=>page.getByRole('listitem').filter({has:page.getByText('Target reminder',{exact:true})});
  const pending=page.locator('[data-reminder-change="target"]');
  const measure=async(id,predicate)=>page.evaluate(({id,predicate})=>{
   document.addEventListener('click',()=>{const start=performance.now(),check=new Function('return '+predicate),observer=new MutationObserver(()=>{
    if(!check())return;observer.disconnect();const domMs=performance.now()-start;requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push({id,domMs,presentedUpperMs:performance.now()-start})));
   });observer.observe(document.body,{subtree:true,childList:true,characterData:true});},{capture:true,once:true});
  },{id,predicate});
  await row().waitFor();await measure('done',"!document.querySelector('[aria-label=\"Reminder done: Target reminder\"]')");
  await row().getByRole('button',{name:'Reminder done: Target reminder'}).click();await row().waitFor({state:'hidden'});assert.equal(await page.locator('[data-due]').innerText(),'1');await pending.getByText('Removing reminder…',{exact:true}).waitFor();
  assert.deepEqual(await page.evaluate(()=>{const one=fixture.store.changeReminder({action:'done',message:'target'}),two=fixture.store.changeReminder({action:'done',message:'target'});fixture.store.changeReminder({action:'cancel',message:'target'});return {same:one===two,writes:fixture.writes.length}}),{same:true,writes:1});
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Typed while storage blocked');await page.getByRole('button',{name:'Toggle settings'}).click();assert.equal(await page.locator('[data-current-tab]').innerText(),'settings');
  if(evidence)await page.screenshot({path:path.join(evidence,'done-pending-'+width+'-'+count+'.png')});
  await page.evaluate(()=>fixture.finish(0,true));await row().waitFor();assert.equal(await page.locator('[data-due]').innerText(),'2');await pending.getByRole('button',{name:'Retry reminder'}).click();await row().waitFor({state:'hidden'});await page.evaluate(()=>fixture.finish(1));await pending.waitFor({state:'hidden'});
  await page.evaluate(()=>fixture.newer());await row().getByRole('button',{name:'Details',exact:true}).click();await page.getByRole('dialog',{name:'Reminder',exact:true}).getByRole('button',{name:'Cancel',exact:true}).click();await row().waitFor({state:'hidden'});await page.getByRole('dialog').waitFor({state:'hidden'});
  await page.evaluate(()=>fixture.newer());await row().waitFor();assert.equal(await page.locator('[data-due]').innerText(),'2','new generation with same due time stays visible during pending cancel');await page.evaluate(()=>fixture.finish(2));await pending.getByText('Reminder change failed: That reminder changed; refresh and try again.',{exact:true}).waitFor();await row().waitFor();await pending.getByRole('button',{name:'Dismiss error'}).click();await pending.waitFor({state:'hidden'});
  await row().getByRole('button',{name:'Details',exact:true}).click();await page.getByRole('dialog',{name:'Reminder',exact:true}).getByRole('button',{name:'Later…',exact:true}).click();
  const move=page.getByRole('dialog',{name:'Move the reminder',exact:true});await move.getByLabel('Date',{exact:true}).fill('2035-01-02');await move.getByLabel('Time',{exact:true}).fill('09:15');
  await measure('move',"document.querySelector('[data-due]').textContent === '1'");await move.getByRole('button',{name:'Move it',exact:true}).click();await move.waitFor({state:'hidden'});assert.equal(await page.locator('[data-due]').innerText(),'1');assert.equal(await page.evaluate(()=>fixture.store.get().overview.reminders.find(r=>r.message==='target').overdue),false);
  const due=await page.evaluate(()=>fixture.writes[3].b.due);await page.evaluate(()=>fixture.finish(3,true));assert.equal(await page.locator('[data-due]').innerText(),'2');
  await page.getByRole('dialog',{name:'Reminder',exact:true}).getByRole('button',{name:'Close',exact:true}).click();await page.getByRole('dialog').waitFor({state:'hidden'});
  await pending.getByRole('button',{name:'Retry reminder'}).click();assert.equal(await page.evaluate(()=>fixture.writes[4].b.due),due);await page.evaluate(()=>fixture.finish(4));await pending.waitFor({state:'hidden'});
  await page.getByRole('button',{name:'New reminder',exact:true}).click();const create=page.getByRole('dialog',{name:'Remind me later',exact:true});await create.getByLabel('Date',{exact:true}).fill('2035-02-03');await create.getByLabel('Time',{exact:true}).fill('10:30');await measure('create',"!!document.querySelector('[aria-label=\"Reminder done: New reminder\"]')");await create.getByRole('button',{name:'Remind me',exact:true}).click();await create.waitFor({state:'hidden'});
  await page.getByRole('listitem').filter({has:page.getByText('New reminder',{exact:true})}).waitFor();await page.locator('[data-reminder-change="new"]').getByText('Saving reminder…',{exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Reminder done: New reminder',exact:true}).isDisabled(),true,'pending creation cannot accept conflicting Done');await page.evaluate(()=>fixture.finish(5));await page.locator('[data-reminder-change="new"]').waitFor({state:'hidden'});assert.equal(await page.getByRole('button',{name:'Reminder done: New reminder',exact:true}).isEnabled(),true);
  await page.evaluate(()=>{fixture.legacy=true;fixture.holdReads=true});await row().getByRole('button',{name:'Reminder done: Target reminder'}).click();await page.evaluate(()=>fixture.finish(6));await pending.getByText('Reminder change saved; checking current state…',{exact:true}).waitFor();
  await page.evaluate(()=>{fixture.failReads=true;fixture.release()});await pending.getByText('Invented read failure',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>fixture.writes.length),7);await page.evaluate(()=>fixture.failReads=false);await pending.getByRole('button',{name:'Check reminder'}).click();await pending.waitFor({state:'hidden'});assert.equal(await page.evaluate(()=>fixture.writes.length),7,'confirmation retries only the read');await row().waitFor({state:'hidden'});
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Typed while storage blocked');assert.deepEqual(errors,[]);
  if(evidence)await page.screenshot({path:path.join(evidence,'recovery-'+width+'-'+count+'.png')});
  console.log(JSON.stringify({width,count,done:true,cancel:true,move:true,create:true,failureRecovery:true,newerSameDue:true,duplicateGuard:true,legacyReadOnlyRecovery:true,timings:await page.evaluate(()=>fixture.timings),errors}));await page.close();
 }
}catch(error){if(lastPage&&!lastPage.isClosed()){console.error(await lastPage.evaluate(()=>({text:document.body.innerText,changes:fixture.store.get().reminderChanges,writes:fixture.writes.map(w=>({p:w.p,b:w.b}))})));if(evidence)await lastPage.screenshot({path:path.join(evidence,'failure.png')});}throw error;}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}})().catch(e=>{console.error(e.stack);process.exitCode=1});
