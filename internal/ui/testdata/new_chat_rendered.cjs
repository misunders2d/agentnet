// Actual New picker and search result: immediate destination, guarded native creation.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-new-chat-'));
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {NewChatButton} from './src/features/NewChat';import {ChatList} from './src/features/ChatList';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),mode=new URL(location).searchParams.get('mode');
const people=['Bob','Carol'].map(n=>({person:n.toLowerCase(),label:'Fixture '+n,state:'pinned',address:n.toLowerCase()+'/laptop',devices:[]}));
const overview={seq:1,version:'fixture',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person:{person:'self',label:'Fixture owner',state:'self',address:'self/laptop',devices:[]},people,dms:[],threads:[],needs_you:[]};
const f=window.fixture={writes:[],threads:{}};
const host={platform:'daemon',workspace:{id:'new-chat',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/dm/new')return new Promise((resolve,reject)=>f.writes.push({b,resolve,reject}));
 if(p.startsWith('/api/dm?'))return structuredClone(f.threads[new URL(p,location.href).searchParams.get('id')]||{id:'elsewhere',kind:'dm',peer:people[0],messages:[],agents:[],guests:[],topics:[]});
 if(p==='/api/refresh'||p==='/api/topic/transfer/location')return {};
 if(p.includes('/groups/invitations'))return [];
 if(p.startsWith('/api/agents'))return {agents:[]};
 if(b)throw Error('Unexpected fixture mutation '+p);return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented create refusal'));return;}
 const peer=people.find(p=>p.address===w.b.address),id='chat-'+index;f.threads[id]={id,kind:'dm',role:'member',peer,messages:[],agents:[],guests:[],topics:[]};overview.dms.push({id,kind:'dm',peer,count:0,unread:0});overview.seq++;w.resolve({id});
};
f.owner=async()=>{overview.person={...overview.person,person:'other-owner'};overview.seq++;await store.refetch()};
function App(){const s=useStore(store,s=>s),[mounted,setMounted]=useState(true);f.mount=setMounted;return <><textarea aria-label="Unrelated draft"/>{mounted?(mode==='picker'?<NewChatButton/>:<ChatList/>):<p>Picker unmounted</p>}<p aria-label="Current conversation">{s.open?.id||'No selected conversation'}</p></>}
await store.start();store.showTab('chats');createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(process.env.AGENTNET_NEW_CHAT_SOURCE)for(const f of ['NewChat.tsx','ChatList.tsx'])fs.copyFileSync(path.join(process.env.AGENTNET_NEW_CHAT_SOURCE,f),path.join(work,'src/features',f));
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>New chat fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 const setup=async(page,mode)=>{await page.goto('http://127.0.0.1:'+server.address().port+'/?mode='+mode);await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep this draft');if(mode==='picker')await page.getByRole('button',{name:'New',exact:true}).click();else{if(await page.getByRole('button',{name:'Search chats',exact:true}).count())await page.getByRole('button',{name:'Search chats',exact:true}).click();await page.getByPlaceholder('Search people, agents, groups',{exact:true}).fill('Fixture')}};
 const choose=async(page,name='Bob')=>{const row=page.locator('[data-new-chat-choice]').filter({hasText:'Fixture '+name});await row.scrollIntoViewIfNeeded();await row.evaluate(b=>b.addEventListener('click',e=>fixture.input={at:performance.now(),trusted:e.isTrusted},{once:true,capture:true}));await row.click()};
 const region=(page,name='Bob')=>page.getByRole('region',{name:'Starting chat with Fixture '+name,exact:true});
 const presented=async locator=>assert(await locator.evaluate(el=>{const r=el.getBoundingClientRect();let top=0,bottom=innerHeight;for(let p=el.parentElement;p;p=p.parentElement)if(/auto|scroll|hidden|clip/.test(getComputedStyle(p).overflowY)){const b=p.getBoundingClientRect();top=Math.max(top,b.top);bottom=Math.min(bottom,b.bottom)}return r.top>=top-1&&r.bottom<=bottom+1&&r.left>=0&&r.right<=innerWidth}),'destination must be within the viewport and scrollport');
 const capture=async(page,name)=>{if(!process.env.AGENTNET_NEW_CHAT_EVIDENCE)return;await page.evaluate(async()=>Promise.all(document.getAnimations().filter(a=>Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{}))));await page.screenshot({path:process.env.AGENTNET_NEW_CHAT_EVIDENCE+'-'+name+'.png',fullPage:true})};
 for(const width of [1440,390])for(const mode of ['picker','search']){
  const page=await browser.newPage({viewport:{width,height:960}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);await setup(page,mode);await choose(page);const pending=region(page);await pending.waitFor({timeout:500});const timing=await page.evaluate(async()=>{await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));return{...fixture.input,feedback:performance.now()-fixture.input.at}});assert(timing.trusted);assert(timing.feedback<500,JSON.stringify({width,mode,timing}));await presented(pending);assert.equal(await page.locator('[data-new-chat-choice]').count(),0);assert.equal(await page.evaluate(()=>fixture.store.get().open),null,'no fabricated conversation ID');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),true);assert.equal(await page.evaluate(()=>{const a=fixture.store.api.newDM('bob/laptop'),b=fixture.store.api.newDM('bob/laptop');a.catch(()=>{});b.catch(()=>{});return a===b}),true);assert.equal(await page.evaluate(()=>fixture.writes.length),1);await capture(page,mode+'-pending-'+width);
  await pending.getByRole('button',{name:'Back to people',exact:true}).click();await choose(page);await pending.waitFor();assert.equal(await page.evaluate(()=>fixture.writes.length),1,'returning to the same pending target shares native work');await page.evaluate(()=>fixture.finish(0,true));await pending.getByRole('alert').waitFor();await presented(pending);await capture(page,mode+'-error-'+width);await pending.getByRole('button',{name:'Try starting chat again',exact:true}).click();await pending.getByRole('status').waitFor();assert.deepEqual(await page.evaluate(()=>fixture.writes.map(x=>x.b.address)),['bob/laptop','bob/laptop']);
  await pending.getByRole('button',{name:'Back to people',exact:true}).click();await choose(page,'Carol');const next=region(page,'Carol');await next.waitFor();await page.evaluate(()=>fixture.finish(1));await page.waitForTimeout(40);assert.equal(await next.count(),1,'older create result cannot replace new destination');assert.equal(await page.evaluate(()=>fixture.store.get().open),null);await page.evaluate(()=>fixture.finish(2));await page.waitForFunction(()=>fixture.store.get().open?.id==='chat-2');assert.equal(await next.count(),0);if(mode==='picker')assert.equal(await page.getByRole('dialog',{name:'New chat',exact:true}).count(),0);assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep this draft');assert.equal(await page.evaluate(()=>fixture.store.hasUnsent()),false);assert.deepEqual(errors,[]);reports.push({width,mode,feedbackMs:timing.feedback,writes:3});await page.close();
 }
 for(const mode of ['picker','search'])for(const scope of ['owner','navigation','close','unmount','stopped']){
  const page=await browser.newPage({viewport:{width:390,height:960}});page.setDefaultTimeout(5000);await setup(page,mode);await choose(page);await region(page).waitFor();
  if(scope==='owner')await page.evaluate(()=>fixture.owner());
  if(scope==='navigation')await page.evaluate(()=>fixture.store.open({kind:'dm',id:'elsewhere'}));
  if(scope==='close'){if(mode==='picker'){await page.getByRole('button',{name:'Close',exact:true}).click();await page.getByRole('dialog',{name:'New chat',exact:true}).waitFor({state:'hidden'})}else await page.getByPlaceholder('Search people, agents, groups',{exact:true}).fill('')}
  if(scope==='unmount'){await page.evaluate(()=>fixture.mount(false));await page.getByText('Picker unmounted',{exact:true}).waitFor()}
  if(scope==='stopped')await page.evaluate(()=>fixture.store.stop());await page.evaluate(()=>fixture.finish(0));await page.waitForTimeout(40);assert.equal(await page.evaluate(()=>fixture.store.get().open?.id||null),scope==='navigation'?'elsewhere':null,'late '+scope+' create cannot navigate');await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'actual New picker and chat-search result, held creation/failure/exact retry/Back/dedup, newer target, owner/navigation/close/unmount/stop; native transport mocked'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
