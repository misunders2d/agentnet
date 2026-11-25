// Actual Store, BlockConfirm, BlockedBar and Toasts: blocking shows at once,
// a held or failed change stays truthful, and a stale overview cannot undo a
// pending block. Invented data only. Timings are click-to-DOM.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-block-'));
const shots=process.env.AGENTNET_SCREENSHOTS;
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {BlockConfirm,BlockedBar,useBlocked} from './src/features/Block';import {Toasts} from './src/features/Status';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const person=(id,label,address)=>({person:id,label,address,state:'pinned',devices:[{address}]});
const f=window.fixture={calls:[],blocked:[],hold:false,fail:false,held:[],stale:false};
const overview=()=>({seq:1,version:'fixture',me:{address:'bea/phone',fingerprint:'bea-key'},block:true,blocked:f.stale?[]:[...f.blocked],
 person:{person:'bea',label:'Bea',address:'bea/phone',state:'self',devices:[{address:'bea/phone'}]},
 people:[person('dan','Dan','dan/laptop')],dms:[{id:'d'.repeat(64),peer:person('dan','Dan','dan/laptop')}],threads:[],needs_you:[]});
const host={platform:'daemon',workspace:{id:'block',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return overview();
 if(p==='/api/person/block'){f.calls.push(structuredClone(b));if(f.hold)await new Promise(r=>f.held.push(r));if(f.fail)throw Error('Invented relay refusal');
  f.blocked=b.blocked?[...new Set([...f.blocked,b.person])]:f.blocked.filter(x=>x!==b.person);return {note:b.blocked?'Blocked.':'Unblocked.',person:b.person,blocked:b.blocked};}
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;f.release=()=>{f.hold=false;for(const r of f.held.splice(0))r();};
function App(){const [open,setOpen]=useState(false),[n,setN]=useState(0),blocked=useBlocked('dan');
 return <div className="p-4"><button onClick={()=>setOpen(true)}>Block Dan…</button><button onClick={()=>setN(n+1)}>Unrelated {n}</button>
 <BlockConfirm person="dan" name="Dan" open={open} onOpenChange={setOpen}/>
 {blocked?<BlockedBar person="dan" name="Dan"/>:<textarea aria-label="Message Dan"/>}<Toasts/></div>;}
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
  await page.goto('http://127.0.0.1:'+server.address().port+'/');await page.getByLabel('Message Dan').waitFor();
  await page.getByRole('button',{name:'Block Dan…'}).click();
  const dialog=page.getByRole('alertdialog',{name:'Block Dan?'});await dialog.waitFor();
  await dialog.getByText('Groups you share don’t change.',{exact:false}).waitFor();
  if(shots)await page.screenshot({path:path.join(shots,'block-confirm-'+width+'.png')});
  // Held: the chat shows the block at once; a stale overview cannot undo it; other controls respond.
  await page.evaluate(()=>{fixture.hold=true;});
  await timed(page,'block shown',()=>dialog.getByRole('button',{name:'Block'}).click(),()=>page.getByText('You blocked Dan. Their direct messages are hidden.',{exact:true}).waitFor());
  await dialog.waitFor({state:'hidden'});
  await page.evaluate(()=>{fixture.stale=true;return fixture.store.refetch();});
  await page.getByText('You blocked Dan.',{exact:false}).waitFor();
  await timed(page,'unrelated while held',()=>page.getByRole('button',{name:/^Unrelated/}).click(),()=>page.getByRole('button',{name:'Unrelated 1'}).waitFor());
  await page.evaluate(()=>{fixture.stale=false;fixture.release();});
  await page.getByText('Blocked Dan',{exact:true}).waitFor();
  assert.deepEqual(await page.evaluate(()=>fixture.calls),[{person:'dan',blocked:true}]);
  await page.getByText('You blocked Dan.',{exact:false}).waitFor();
  if(shots)await page.screenshot({path:path.join(shots,'block-bar-'+width+'.png')});
  // Failure: the composer returns at once, then the block comes back with the reason.
  await page.evaluate(()=>{fixture.fail=true;fixture.hold=true;});
  await timed(page,'unblock shown',()=>page.getByRole('button',{name:'Unblock'}).click(),()=>page.getByLabel('Message Dan').waitFor());
  await page.evaluate(()=>fixture.release());
  await timed(page,'failure restored',async()=>{},()=>page.getByText('Could not unblock Dan: Invented relay refusal',{exact:true}).waitFor());
  await page.getByText('You blocked Dan.',{exact:false}).waitFor();
  // Success: the composer comes back and stays.
  await page.evaluate(()=>{fixture.fail=false;});
  await page.getByRole('button',{name:'Unblock'}).click();
  await page.getByText('Unblocked Dan',{exact:true}).waitFor();await page.getByLabel('Message Dan').waitFor();
  assert.deepEqual(await page.evaluate(()=>fixture.blocked),[]);
  assert.deepEqual(errors,[]);await page.close();
 }
 console.log('Block PASS '+timings.join(', '));
}finally{if(browser)await browser.close();server.close();fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exit(1);});
