// The actual shared Composer and codecs over a synthetic captured host. This
// exercises daemon/browser/Android host contracts, not physical devices.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http');
const{execFileSync}=require('node:child_process'),{createRequire}=require('node:module');
const webDir=path.resolve(__dirname,'../web'),web=createRequire(path.join(webDir,'package.json'));
const{chromium}=require(process.env.AGENTNET_PLAYWRIGHT),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-android-send-'));
fs.cpSync(path.join(webDir,'src'),path.join(work,'src'),{recursive:true});fs.symlinkSync(path.join(webDir,'node_modules'),path.join(work,'node_modules'),'dir');
for(const name of['pictures.mjs','optimistic.mjs'])fs.copyFileSync(path.resolve(webDir,'../static',name),path.join(work,'src',name));
for(const name of['person-topics.mjs','reaction-preferences.mjs','collective-mentions.mjs'])fs.copyFileSync(path.resolve(webDir,'../skins/shared',name),path.join(work,'src',name));
for(const[src,dst]of[['drivespace.mjs','drive-space.mjs'],['drivespace-setup.mjs','drive-setup.mjs']])fs.copyFileSync(path.resolve(webDir,'../skins/shared',src),path.join(work,'src',dst));
const entry=`
import React from'react';import{createRoot}from'react-dom/client';import{Store,useStore}from'./src/store';import{StoreContext}from'./src/context';import{OwnedContext}from'./src/owned';import{Composer}from'./src/features/Composer';import{MessageView}from'./src/features/Message';import{NativeRecovery}from'./src/features/NativeRecovery';import{Toasts}from'./src/features/Status';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),conv='c'.repeat(64),other='d'.repeat(64),mode=new URL(location).searchParams.get('case');
const me={address:'alice/phone',fingerprint:'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd'},peer={person:'bob',address:'bob/laptop',label:'Bob'},dm={id:conv,peer,messages:[],agents:[],guests:[],topics:[],can_send:true},overview={seq:1,version:'fixture',me,person:{person:'alice',label:'Alice',state:'self',address:me.address,devices:[]},people:[peer],dms:[{id:conv,peer}],threads:[],files:{max_count:8,max_file:1024,max_message:1024}};
const f=window.fixture={calls:[],stages:[],journalWrites:0,accepted:0};let push;
const originalSet=Storage.prototype.setItem;Storage.prototype.setItem=function(k,v){if(k.startsWith('agentnet.android.send-journal.v1.')){f.journalWrites++;if(mode==='quota')throw Error('Fictional storage full');}return originalSet.call(this,k,v);};
const host={version:1,platform:'android',workspace:{id:'android-local-'+mode,state:'enrolled'},android:{legacyDrafts:async()=>({drafts:[]}),visible:()=>true},listen(fn){push=fn;return()=>{}},stage:async file=>{f.stages.push(file.name);return 'stage-exact';},async api(path,body){
 if(path==='/api/dm/send')return new Promise((resolve,reject)=>f.calls.push({body:structuredClone(body),resolve,reject}));
 if(path.startsWith('/api/dm?'))return structuredClone(new URL(path,location).searchParams.get('id')===other?{...dm,id:other,messages:[]}:dm);if(path.startsWith('/api/overview'))return structuredClone(overview);if(path.startsWith('/api/agents'))return{agents:[]};if(path.startsWith('/api/typing'))return{preferences:{send:false,show:false},entries:[]};return{};
}};
const store=f.store=new Store(host);store.set({overview,open:{kind:'dm',id:conv},dm,views:{[conv]:dm},conn:'live'});
f.publish=async newer=>{if(newer&&dm.messages.length)dm.messages[0].body='Newer synchronized text';await store.refreshOpen();};
f.finish=(index,kind='ok')=>{const call=f.calls[index];if(kind!=='reject'&&!dm.messages.some(m=>m.id===call.body.id)){f.accepted++;dm.messages.push({...call.body,id:call.body.id,lid:call.body.id,from:me.address,send_group_author:me.fingerprint,origin:'ui',dir:'out',kind:'message',delivery:'delivered',state:'delivered',can:[],reactions:[],at:new Date().toISOString()});}if(kind!=='ok')call.reject(Error('Fictional response unconfirmed'));else call.resolve({id:call.body.id,lid:call.body.id,state:'delivered'});};
f.pending=()=>store.sends.merge(conv,store.get().views[conv]?.messages||[]).filter(m=>m._local).map(m=>({id:m.id,state:m.state,text:m.state_text}));
f.journal=()=>JSON.parse(localStorage.getItem('agentnet.android.send-journal.v1.'+host.workspace.id)||'[]');
f.navigate=()=>{const v={...dm,id:other,messages:[]};store.set({open:{kind:'dm',id:other},dm:v,views:{...store.get().views,[other]:v}});};
function App(){const state=useStore(store,s=>s),view=state.dm,rows=store.sends.merge(view.id,view.messages),ctx={conv:view.id,dm:view,overview:state.overview,canReply:true,names:{}};return <><NativeRecovery/><section data-testid="messages">{rows.map(m=><MessageView key={m.id} m={m} ctx={ctx} all={rows} status/>)}</section><Composer dm={view}/><Toasts/></>}
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
web('esbuild').buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,splitting:true,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',entryNames:'fixture',outdir:path.join(work,'bundle'),define:{'process.env.NODE_ENV':'"production"'},metafile:true,write:true,logLevel:'silent'});
execFileSync(process.execPath,[path.join(webDir,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(webDir,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:webDir,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'),'utf8')+'\nbody{margin:0}.an-root{min-height:100vh;padding:24px}.an-app{max-width:800px;margin:0 auto}';
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(u.pathname==='/'){res.setHeader('Content-Type','text/html');return res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root" data-theme="light"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/fixture.js"></script>');}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css);}const name=u.pathname.slice(1);if(/^[\w.-]+\.js$/.test(name)&&fs.existsSync(path.join(work,'bundle',name))){res.setHeader('Content-Type','text/javascript');return res.end(fs.readFileSync(path.join(work,'bundle',name)));}res.writeHead(404).end();});
(async()=>{let browser;const reports=[];try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM,headless:true,args:['--no-sandbox']});
for(const width of [1280,390])for(const mode of ['held','rejected-files','lost-response','quota','navigate','synced','stopped']){
 console.log(JSON.stringify({caseStarted:mode,width}));const page=await browser.newPage({viewport:{width,height:850}}),errors=[];page.on('pageerror',e=>{errors.push(String(e));console.error('Fixture page error:',String(e))});await page.goto('http://127.0.0.1:'+server.address().port+'/?case='+mode);const field=page.locator('textarea');await field.fill('Exact journaled local pending');
 if(mode==='rejected-files')await page.locator('input[type=file]').first().setInputFiles({name:'fixture.txt',mimeType:'text/plain',buffer:Buffer.from('fixture')});
 const start=Date.now();await page.getByRole('button',{name:/^Send/}).click();
 if(mode==='quota'){await page.getByText(/Send was not submitted.*draft is kept/).waitFor();assert.equal(await field.inputValue(),'Exact journaled local pending');assert.equal(await page.evaluate(()=>fixture.calls.length),0);assert.equal(await page.evaluate(()=>fixture.pending().length),0);assert.equal(await page.evaluate(()=>fixture.journal().length),0);}
 else{
  await page.waitForFunction(()=>fixture.calls.length===1);await page.locator('[data-testid=messages]').locator('[data-part=message]').filter({hasText:'Exact journaled local pending'}).waitFor({timeout:1000});await page.locator('[data-testid=messages]').getByText('Sending…',{exact:true}).waitFor();const feedbackMs=Date.now()-start;assert(feedbackMs<1000,'exact local pending must precede held native response below1000ms');assert.equal(await field.inputValue(),'');assert.equal(await page.evaluate(()=>fixture.journal().length),1);assert.equal(await page.evaluate(()=>fixture.accepted),0);assert.equal(await page.locator('[data-testid=messages]').getByRole('button',{name:/Delivered|Queued|On your server/}).count(),0,'local preview has no admission receipt');
  const original=await page.evaluate(()=>fixture.calls[0].body);await field.fill('New draft survives result');
  if(mode==='navigate'){await page.evaluate(()=>fixture.navigate());await page.locator('form[data-conv="'+('d'.repeat(64))+'"]').waitFor();await field.fill('Other chat draft');}
  if(mode==='stopped')await page.evaluate(()=>fixture.store.stop());
  if(mode==='rejected-files'||mode==='lost-response'){
   await page.evaluate(mode=>fixture.finish(0,mode==='lost-response'?'lost':'reject'),mode);await page.getByRole('button',{name:/Saved messages.*Review/}).waitFor();await page.waitForFunction(()=>fixture.pending().length===0);assert.equal(await page.evaluate(()=>fixture.journal()[0].id),original.id);await page.getByRole('button',{name:/Saved messages.*Review/}).click();await page.getByRole('dialog',{name:'Saved messages',exact:true}).getByRole('button',{name:'Retry saved send',exact:true}).click();
   if(mode==='rejected-files'){await page.waitForFunction(()=>fixture.calls.length===2);assert.deepEqual(await page.evaluate(()=>fixture.calls[1].body),original,'retry freezes exact ID/stages/request');await page.evaluate(()=>fixture.finish(1));assert.equal(await page.evaluate(()=>fixture.stages.length),1);}
   await page.waitForFunction(()=>fixture.journal().length===0);assert.equal(await page.evaluate(()=>fixture.accepted),1);if(mode==='lost-response')assert.equal(await page.evaluate(()=>fixture.calls.length),1,'readback detects stored exact send without replay');
  }else{
   if(mode==='synced'){await page.evaluate(()=>{fixture.finish(0);return fixture.publish(true)});await page.locator('[data-testid=messages]').locator('[data-part=message]').filter({hasText:'Newer synchronized text'}).waitFor();assert.equal(await page.evaluate(()=>fixture.pending().length),0,'native result cannot reintroduce an old preview');}
   else await page.evaluate(()=>fixture.finish(0));await page.waitForFunction(()=>fixture.journal().length===0);
  }
  assert.equal(await field.inputValue(),mode==='navigate'?'Other chat draft':'New draft survives result','current draft is not cleared by late result');if(mode==='navigate')assert.equal(await page.locator('[data-testid=messages]').locator('[data-part=message]').filter({hasText:'Exact journaled local pending'}).count(),0);
  reports.push({width,mode,feedbackMs});
 }
 assert.deepEqual(errors,[]);await page.close();
}
console.log(JSON.stringify({ok:true,reports,scope:'actual Composer, MessageView, NativeRecovery and Store; held synthetic native admission, not physical Android timing'}));
}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1;});
