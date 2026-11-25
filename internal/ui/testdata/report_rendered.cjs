// Actual Store, ReportSheet and Toasts: a report reaches each admin person as
// the reporter's own message; held, failed and retried sends stay truthful.
// Invented data only. Timings are click-to-DOM.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-report-'));
const shots=process.env.AGENTNET_SCREENSHOTS;
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {ReportSheet,reportAdmins,reportBody} from './src/features/Report';import {Toasts} from './src/features/Status';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const noAdmins=new URL(location).searchParams.get('admins')==='none';
const person=(id,label,address,devices=[])=>({person:id,label,address,state:'pinned',devices});
const overview={seq:1,version:'fixture',me:{address:'bea/phone',fingerprint:'bea-key'},
 person:{person:'bea',label:'Bea',address:'bea/phone',state:'self',devices:[{address:'bea/phone'},{address:'bea/laptop'}]},
 people:[person('ann','Ann','ann/laptop',[{address:'ann/laptop'},{address:'ann/phone'}]),person('carl','Carl','carl/laptop'),person('dan','Dan','dan/laptop'),person('eve','Eve','eve/laptop')],
 dms:[{id:'d'.repeat(64),peer:person('ann','Ann','ann/laptop')}],threads:[],needs_you:[],
 directory:{status:'listed',current:true,truncated:false,members:noAdmins?[{address:'dan/laptop'}]:[
  {address:'ann/laptop',admin:true},{address:'ann/phone',admin:true},{address:'carl/laptop',admin:true},{address:'dan/laptop',admin:true},
  {address:'bea/laptop',admin:true},{address:'eve/laptop'},{address:'mallory/phone',admin:true}]}};
const f=window.fixture={sends:[],news:[],hold:false,fail:false,held:[]};
const host={platform:'daemon',workspace:{id:'report',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/dm/new'){f.news.push(b.address);return {id:'n'.repeat(63)+f.news.length};}
 if(p==='/api/dm/send'){f.sends.push(structuredClone(b));if(f.hold)await new Promise(r=>f.held.push(r));if(f.fail)throw Error('Invented relay refusal');return {id:b.id,state:'queued'};}
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;f.release=()=>{f.hold=false;for(const r of f.held.splice(0))r();};
f.admins=()=>reportAdmins(store.get().overview,{address:'dan/laptop',person:'dan'}).map(a=>a.label);
f.body=reportBody({name:'Dan',address:'dan/laptop',where:'Ops',message:'line one\\nline two'},'  spam  ',true);
function App(){const [open,setOpen]=useState(false),[n,setN]=useState(0);
 return <div className="p-4"><button onClick={()=>setOpen(true)}>Open report</button><button onClick={()=>setN(n+1)}>Unrelated {n}</button>
 <ReportSheet open={open} onOpenChange={setOpen} subject={{name:'Dan',address:'dan/laptop',person:'dan',where:'Ops',message:'You are all fired, click this link'}}/><Toasts/></div>;}
await store.start();
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(u.pathname==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><div id="root"><div id="app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
const timings=[];
async function timed(page,label,act,done){const t0=Date.now();await act();await done();const ms=Date.now()-t0;timings.push(label+' '+ms+'ms');assert.ok(ms<1100,label+' took '+ms+'ms');}
(async()=>{let browser;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 for(const width of [1280,390]){
  const page=await browser.newPage({viewport:{width,height:860},hasTouch:width<600,colorScheme:width<600?'dark':'light'}),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  const base='http://127.0.0.1:'+server.address().port+'/';
  await page.goto(base);await page.getByRole('button',{name:'Open report'}).waitFor();
  // Admin persons: one row per verified person, never the reporter, the reported or an unknown device.
  assert.deepEqual(await page.evaluate(()=>fixture.admins()),['Ann','Carl']);
  const body=await page.evaluate(()=>fixture.body);
  assert.match(body,/Reported: Dan \(dan\/laptop\)/);assert.match(body,/> line one\n> line two/);assert.match(body,/Reason: spam$/);
  await page.getByRole('button',{name:'Open report'}).click();
  const sheet=page.getByRole('dialog',{name:'Report Dan'});await sheet.waitFor();
  await sheet.getByText('Goes to this workspace’s admins: Ann, Carl.',{exact:false}).waitFor();
  if(shots)await page.screenshot({path:path.join(shots,'report-sheet-'+width+'.png')});
  await sheet.getByLabel('What’s wrong? (optional)').fill('Phishing link');
  // Held sends: the sheet closes and a pending notice shows at once; other controls respond.
  await page.evaluate(()=>{fixture.hold=true;});
  await timed(page,'send pending',()=>sheet.getByRole('button',{name:'Send report'}).click(),()=>page.getByText('Sending report to Ann, Carl…',{exact:true}).waitFor());
  await sheet.waitFor({state:'hidden'});
  await timed(page,'unrelated while held',()=>page.getByRole('button',{name:/^Unrelated/}).click(),()=>page.getByRole('button',{name:'Unrelated 1'}).waitFor());
  await page.evaluate(()=>fixture.release());
  await page.getByText('Report sent to Ann, Carl',{exact:true}).waitFor();
  let sends=await page.evaluate(()=>fixture.sends);
  assert.equal(sends.length,2);assert.equal(sends[0].conv,'d'.repeat(64),'reuses the existing chat with Ann');
  assert.match(sends[0].body,/Report to workspace admins/);assert.match(sends[0].body,/> You are all fired, click this link/);assert.match(sends[0].body,/Reason: Phishing link/);
  assert.deepEqual(await page.evaluate(()=>fixture.news),['carl/laptop'],'a new chat only for an admin without one');
  // Failure: the sheet comes back with the reason and Try again; the retry reuses the same send IDs.
  await page.getByRole('button',{name:'Open report'}).click();await sheet.waitFor();
  await sheet.getByRole('checkbox',{name:/Include the message/}).uncheck();
  await page.evaluate(()=>{fixture.fail=true;fixture.sends=[];});
  await timed(page,'failure shown',()=>sheet.getByRole('button',{name:'Send report'}).click(),()=>sheet.getByText('The report was not sent: Invented relay refusal. Try again.',{exact:true}).waitFor());
  const failedID=(await page.evaluate(()=>fixture.sends))[0].id;
  assert.doesNotMatch((await page.evaluate(()=>fixture.sends))[0].body,/You are all fired/,'unticked message is left out');
  await page.evaluate(()=>{fixture.fail=false;fixture.sends=[];});
  await sheet.getByRole('button',{name:'Try again'}).click();
  await page.getByText('Report sent to Ann, Carl',{exact:true}).last().waitFor();
  sends=await page.evaluate(()=>fixture.sends);assert.equal(sends[0].id,failedID,'retry is the same report');assert.equal(sends.length,2);
  // No other admin: an explicit message, nothing to send.
  await page.goto(base+'?admins=none');await page.getByRole('button',{name:'Open report'}).click();
  await sheet.getByText('This workspace has no other admin to send a report to. Contact the person who runs it.',{exact:true}).waitFor();
  assert.equal(await sheet.getByRole('button',{name:'Send report'}).isDisabled(),true);
  if(shots)await page.screenshot({path:path.join(shots,'report-none-'+width+'.png')});
  assert.deepEqual(errors,[]);await page.close();
 }
 console.log('Report PASS '+timings.join(', '));
}finally{if(browser)await browser.close();server.close();fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exit(1);});
