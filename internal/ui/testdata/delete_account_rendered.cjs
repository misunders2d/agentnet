// Actual Store and DeleteAccount: Delete account says "Deleting…" at once,
// cannot close or resubmit while the relay works, says why on failure and
// offers Try again, and shows the deleted state on success. Invented data
// only. Timings are click-to-DOM.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-delete-'));
const shots=process.env.AGENTNET_SCREENSHOTS;
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {DeleteAccount} from './src/features/DeleteAccount';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const me={person:'bea',label:'Bea',address:'bea/phone',state:'self',devices:[{address:'bea/phone'}]};
const f=window.fixture={calls:[],hold:false,fail:false,held:[]};
const overview=()=>({seq:1,version:'fixture',me:{address:'bea/phone',fingerprint:'bea-key'},person:me,people:[],dms:[],threads:[],needs_you:[]});
const host={platform:'daemon',workspace:{id:'delete',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return overview();
 if(p==='/api/person/delete'){f.calls.push(structuredClone(b));if(f.hold)await new Promise(r=>f.held.push(r));if(f.fail)throw Error('Invented relay refusal');return {note:'Your account was deleted.',deleted:['bea/phone']};}
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;f.release=()=>{f.hold=false;for(const r of f.held.splice(0))r();};
await store.start();
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><div className="p-4"><DeleteAccount person={me}/></div></StoreContext.Provider></OwnedContext.Provider>);
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
  await page.goto('http://127.0.0.1:'+server.address().port+'/');
  await timed(page,'sheet opens',()=>page.getByRole('button',{name:'Delete account…'}).click(),()=>page.getByRole('dialog',{name:'Delete your account?'}).waitFor());
  const sheet=page.getByRole('dialog',{name:'Delete your account?'}),submit=sheet.getByRole('button',{name:'Delete my account'});
  assert.equal(await sheet.getByRole('link',{name:'What is deleted and what is kept'}).getAttribute('href'),'https://github.com/misunders2d/agentnet/blob/main/ACCOUNT_DELETION.md');
  assert.ok(await submit.isDisabled(),'submit enabled before typing DELETE');
  await sheet.getByLabel('Type DELETE to confirm').fill('DELET');assert.ok(await submit.isDisabled(),'submit enabled for a partial word');
  await sheet.getByLabel('Type DELETE to confirm').fill('delete');assert.ok(!(await submit.isDisabled()),'submit disabled after typing delete');
  if(shots)await page.screenshot({path:path.join(shots,'delete-confirm-'+width+'.png')});
  // Held: "Deleting…" shows at once; the sheet stays and cannot resubmit.
  await page.evaluate(()=>{fixture.hold=true;fixture.fail=true;});
  await timed(page,'deleting shown',()=>submit.click(),()=>sheet.getByText('Deleting your account…',{exact:true}).waitFor());
  assert.ok(await submit.isDisabled(),'submit enabled while deleting');
  assert.ok(await sheet.getByLabel('Type DELETE to confirm').isDisabled(),'input enabled while deleting');
  await page.keyboard.press('Escape');await sheet.waitFor();
  await page.evaluate(()=>fixture.release());
  await timed(page,'failure shown',async()=>{},()=>sheet.getByText('Your account was not deleted: Invented relay refusal',{exact:true}).waitFor());
  // Try again succeeds and shows the deleted state.
  await page.evaluate(()=>{fixture.fail=false;});
  await timed(page,'deleted shown',()=>sheet.getByRole('button',{name:'Try again'}).click(),()=>page.getByRole('dialog',{name:'Your account was deleted'}).waitFor());
  await page.getByText('Messages and files on this device stay',{exact:false}).waitFor();
  if(shots)await page.screenshot({path:path.join(shots,'delete-done-'+width+'.png')});
  assert.deepEqual(await page.evaluate(()=>fixture.calls),[{person:'bea'},{person:'bea'}]);
  await page.keyboard.press('Escape');await page.getByRole('dialog').waitFor({state:'hidden'});
  assert.ok(await page.getByRole('button',{name:'Account deleted'}).isDisabled(),'Delete stays available after deletion');
  assert.deepEqual(errors,[]);await page.close();
 }
 console.log('DeleteAccount PASS '+timings.join(', '));
}finally{if(browser)await browser.close();server.close();fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exit(1);});
