// Actual storage controls and shared modal over a synthetic captured host.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {createRequire}=require('node:module'),{execFileSync}=require('node:child_process');
const webDir=path.resolve(__dirname,'../web'),web=createRequire(path.join(webDir,'package.json'));
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-storage-management-'));
const entry=`
import React from 'react';import{createRoot}from'react-dom/client';import{StoreContext}from'./src/context';import{OwnedContext}from'./src/owned';
import{DeviceCacheControls,RelayQuotaControls}from'./src/features/StorageManagement';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),react=createRoot(app);
const fixture=window.fixture={calls:[],active:true,changes:0,admin:true,offline:false,hold:false};
const files=[{id:'a'.repeat(32),size:1024,sha256:'b'.repeat(64)},{id:'c'.repeat(32),size:2048,sha256:'d'.repeat(64)}];
const store={isActive:()=>fixture.active,api:{async storageCache(){fixture.calls.push('preview');return{files,usage:{files:2,bytes:3072}};},
async clearStorageCache(selection){fixture.calls.push({selection});if(fixture.hold){fixture.hold=false;await new Promise(resolve=>fixture.release=resolve);}return fixture.offline?{removed:{files:0,bytes:0},retained:{files:1,bytes:selection[0].size}}:{removed:{files:1,bytes:selection[0].size},retained:{files:0,bytes:0}};},
async changeStorageQuota(quota_bytes){fixture.calls.push({quota_bytes});if(fixture.deny)throw Error('Only a server admin can change this limit.');return{};}}};
const mount=()=>react.render(<StoreContext.Provider value={store}><OwnedContext.Provider value={{root,app,portals}}><DeviceCacheControls onChange={()=>fixture.changes++}/><RelayQuotaControls usage={{quota_bytes:1073741824,global:{stored:{reserved_bytes:1000000000},incomplete:{reserved_bytes:0}},can_change_quota:fixture.admin}} onChange={()=>fixture.changes++}/></OwnedContext.Provider></StoreContext.Provider>);
fixture.member=()=>{fixture.admin=false;mount();};mount();
`;
fs.cpSync(path.join(webDir,'src'),path.join(work,'src'),{recursive:true});
fs.symlinkSync(path.join(webDir,'node_modules'),path.join(work,'node_modules'),'dir');
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
for(const[k,v]of Object.entries(aliases))fs.copyFileSync(path.resolve(webDir,'../',v),path.join(work,'src',k));
const bundle=web('esbuild').buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false,logLevel:'silent'}).outputFiles[0].text;
const cssFile=path.join(work,'style.css');execFileSync(path.join(webDir,'node_modules/.bin/tailwindcss'),['-i',path.join(webDir,'src/styles.css'),'-o',cssFile,'--minify'],{cwd:webDir,stdio:'pipe'});
const css=fs.readFileSync(cssFile,'utf8')+'\nbody{margin:0}.fixture{max-width:680px;margin:auto;padding:16px}.an-root{height:auto;min-height:100vh}.an-app{height:auto;display:block}';
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(req.method!=='GET'){res.writeHead(405).end();return;}if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root" data-theme="'+(u.searchParams.get('theme')||'light')+'"><div class="fixture"><div id="app" class="an-app"></div></div><div id="portals"></div></div><script type="module" src="/fixture.mjs"></script>');return;}if(u.pathname==='/fixture.mjs'){res.setHeader('Content-Type','text/javascript');res.end(bundle);return;}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');res.end(css);return;}res.writeHead(404).end();});
(async()=>{let browser;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 for(const width of[320,390,1280])for(const theme of['light','dark']){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));
  await page.goto('http://127.0.0.1:'+server.address().port+'/?theme='+theme);
  const quota=page.getByRole('textbox',{name:'Relay quota (GiB)'});await quota.waitFor();await quota.fill('0');assert.equal(await page.getByRole('button',{name:'Review limit…',exact:true}).isDisabled(),true);
  await quota.fill('2');await page.getByRole('button',{name:'Review limit…',exact:true}).click();await page.getByRole('dialog',{name:'Change everyone’s relay quota?'}).waitFor();assert.deepEqual(await page.evaluate(()=>fixture.calls),[],'review alone must not mutate quota');
  await page.getByRole('button',{name:'Save relay quota',exact:true}).click();await page.getByText('Relay quota saved.',{exact:true}).waitFor();assert.deepEqual(await page.evaluate(()=>fixture.calls),[{quota_bytes:2147483648}]);
  await page.getByRole('button',{name:'Review received cache…',exact:true}).click();await page.getByRole('dialog',{name:'Clear this received cache?'}).waitFor();await page.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal((await page.evaluate(()=>fixture.calls)).length,2,'cancel must not clear');
  await page.getByRole('button',{name:'Review received cache…',exact:true}).click();await page.getByRole('dialog',{name:'Clear this received cache?'}).waitFor();await page.evaluate(()=>fixture.hold=true);await page.getByRole('button',{name:'Verify and clear cache',exact:true}).click();await page.getByRole('button',{name:'Stop after this file',exact:true}).click();await page.evaluate(()=>fixture.release());await page.getByText('1 file cleared. Stopped before the next file.',{exact:true}).waitFor();
  const clears=await page.evaluate(()=>fixture.calls.filter(x=>x.selection));assert.equal(clears.length,1);assert.equal(clears[0].selection.length,1);
  await page.evaluate(()=>fixture.offline=true);await page.getByRole('button',{name:'Review received cache…',exact:true}).click();await page.getByRole('button',{name:'Verify and clear cache',exact:true}).click();await page.getByText('0 files cleared. 2 files kept because another copy could not be verified.',{exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'storage controls overflow at '+width+'/'+theme);
  await page.evaluate(()=>fixture.member());await quota.waitFor({state:'detached'});await page.getByText('Only a server admin can change the relay quota. Connecting Drive does not increase this limit.',{exact:true}).waitFor();assert.deepEqual(errors,[]);
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'storage-'+width+'-'+theme+'.png')});
  await page.close();
 }
 console.log('PASS rendered storage: 320/390/1280 light/dark, valid/invalid quota, explicit confirmation, cancel/stop, offline retention, member scope, no overflow/runtime errors');
}finally{if(browser)await browser.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exitCode=1;});
