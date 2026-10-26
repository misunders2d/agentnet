// Real Store, Timeline and message controls. Writes are deliberately held;
// all identities/data are invented. No generated shipping assets are touched.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-local-changes-'));
const evidence=process.env.AGENTNET_LOCAL_CHANGES_EVIDENCE;
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {Timeline} from './src/features/Conversation.timeline';import {TopicBar,TopicEnd} from './src/features/Conversation.topics';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const conv='c'.repeat(64),topic='b'.repeat(32),count=Number(new URL(location).searchParams.get('count'))||100;
const overview={seq:1,version:'fixture',topic_list:true,me:{address:'self/laptop',fingerprint:'self-key'},person:{person:'self',label:'Me',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[],reminders:[]};
const view={seq:1,id:conv,kind:'dm',peer:{person:'peer',address:'peer/laptop',label:'Peer'},agents:[],guests:[],topics:[{id:topic,conv,peer:'',title:'Chosen topic',count,state:'active',last_at:'2026-10-26T12:00:00Z',unread:0,review:0}],
 messages:Array.from({length:count},(_,i)=>({id:String(i+1).padStart(32,'0'),lid:String(i+1).padStart(32,'0'),dir:'out',from:'self/laptop',kind:'message',body:'Original message '+(i+1),at:new Date(1793016000000+i*60000).toISOString(),revision:1,topic,can:['edit','delete','react'],reactions:[]}))};
const f=window.fixture={writes:[],listeners:new Set(),holdReads:false,reads:[],timings:[],ready:false};
const host={platform:'daemon',workspace:{id:'local-changes',state:'enrolled'},listen(fn){f.listeners.add(fn);return()=>f.listeners.delete(fn)},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')){const snapshot=structuredClone(view);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p.startsWith('/api/message/')||p.startsWith('/api/topic/'))return new Promise((resolve,reject)=>f.writes.push({p,b:structuredClone(b),resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented backend refusal'));return;}
 const m=view.messages.find(m=>m.id===w.b.id);if(w.p==='/api/message/edit'){m.edited=true;m.text=w.b.text;m.revision++;}
 if(w.p==='/api/message/delete'){m.deleted=true;m.revision++;}
 if(w.p==='/api/topic/rename')view.topics[0].title=w.b.title;
 else if(w.p.startsWith('/api/topic/'))view.topics[0].state=w.p.endsWith('/done')?'done':'active';
 view.seq++;w.resolve({note:'Stored'});
};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
f.newer=async()=>{view.messages.at(-1).text='Newer linked-device edit';view.messages.at(-1).edited=true;view.messages.at(-1).revision+=10;view.seq++;await store.refreshOpen()};
function App(){const dm=useStore(store,s=>s.dm),tab=useStore(store,s=>s.tab),o=useStore(store,s=>s.overview);if(!dm)return <p>Opening…</p>;
 const ctx={conv,dm,thread:null,overview:o,names:{},canReply:true};
 return <div className="flex h-full flex-col"><nav><button onClick={()=>store.showTab(tab==='settings'?'chats':'settings')}>Toggle settings</button><span data-current-tab>{tab}</span></nav>
 <TopicBar dm={dm}/><Timeline ctx={ctx} messages={dm.messages} selected={null} onSelect={()=>{}} empty={null} end={<TopicEnd ctx={ctx}/>}/>
 <form data-conv={conv}><textarea aria-label="Unrelated draft" onChange={e=>store.setDraft(conv,{...store.draft(conv),text:e.target.value})}/></form></div>;
}
await store.start();await store.open({kind:'dm',id:conv});store.setDraft(conv,{text:'',topic});
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
f.ready=true;
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(u.pathname==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Local changes fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}nav,form{padding:12px}button{min-height:44px}textarea{border:1px solid;width:95%}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser,lastPage;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});if(evidence)fs.mkdirSync(evidence,{recursive:true});
 for(const width of [1440,390])for(const count of [100,1000]){
  const page=await browser.newPage({viewport:{width,height:900},hasTouch:width<600}),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  lastPage=page;
  const measure=async(id,text)=>page.evaluate(({id,text})=>{
   document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{
    if(!document.body.textContent.includes(text))return;observer.disconnect();const domMs=performance.now()-start;
    requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push({id,domMs,presentedUpperMs:performance.now()-start})));
   });observer.observe(document.body,{subtree:true,childList:true,characterData:true});},{capture:true,once:true});
  },{id,text});
  await page.goto('http://127.0.0.1:'+server.address().port+'/?count='+count);const row=page.locator('[data-mid="'+String(count).padStart(32,'0')+'"]');await row.waitFor();await row.scrollIntoViewIfNeeded();
  const menu=async(name)=>{await page.getByRole('dialog').waitFor({state:'hidden'});await row.scrollIntoViewIfNeeded();if(width>600){await row.hover();await row.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name,exact:true}).click();}else{await row.locator('[data-message-bubble]').click({trial:true});const b=await row.locator('[data-message-bubble]').boundingBox(),cdp=await page.context().newCDPSession(page);await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:b.x+Math.min(30,b.width/2),y:b.y+b.height/2}]});await page.getByRole('dialog').waitFor();await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});await page.getByRole('dialog').getByRole('button',{name,exact:true}).click();await cdp.detach();}};
  await menu('Edit');await page.getByRole('textbox',{name:'New text'}).fill('Visible before backend');await measure('edit','Saving edit…');await page.getByRole('button',{name:'Save',exact:true}).click();
  await row.getByText('Visible before backend',{exact:false}).waitFor();assert.equal(await page.evaluate(()=>fixture.writes.length),1);assert.equal(await page.evaluate(()=>fixture.writes[0].b.text),'Visible before backend');await row.getByText('Saving edit…',{exact:true}).waitFor();
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep typing during write');await page.getByRole('button',{name:'Toggle settings'}).click();assert.equal(await page.locator('[data-current-tab]').innerText(),'settings');
  if(evidence)await page.screenshot({path:path.join(evidence,'edit-pending-'+width+'-'+count+'.png')});
  await page.evaluate(()=>fixture.finish(0,true));await row.getByText('Original message '+count,{exact:false}).waitFor();await row.getByRole('button',{name:'Edit again'}).click();assert.equal(await page.getByRole('textbox',{name:'New text'}).inputValue(),'Visible before backend');
  await page.getByRole('button',{name:'Save',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===2);await page.evaluate(()=>fixture.finish(1));await page.waitForFunction(()=>!document.querySelector('[data-message-change]'));
  await menu('Delete…');await measure('delete','Deleting message…');await page.getByRole('button',{name:'Delete',exact:true}).click();await row.getByText('Deleting message…',{exact:false}).waitFor();assert.equal(await row.getByText('Visible before backend',{exact:false}).count(),0);
  await page.getByRole('dialog').waitFor({state:'hidden'});
  if(evidence)await page.screenshot({path:path.join(evidence,'delete-pending-'+width+'-'+count+'.png')});
  await page.evaluate(()=>fixture.finish(2,true));await row.getByText('Visible before backend',{exact:false}).waitFor();
  await menu('Edit');await page.getByRole('textbox',{name:'New text'}).fill('Older local attempt');await page.getByRole('button',{name:'Save',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===4);await page.evaluate(()=>fixture.newer());await row.getByText('Newer linked-device edit',{exact:false}).waitFor();await page.evaluate(()=>fixture.finish(3,true));await row.getByText('Newer linked-device edit',{exact:false}).waitFor();
  await row.getByRole('button',{name:'Edit again'}).click();assert.equal(await page.getByRole('textbox',{name:'New text'}).inputValue(),'Older local attempt','newer linked content must not lose failed local text');
  await page.getByRole('button',{name:'Cancel',exact:true}).click();await row.getByText('Newer linked-device edit',{exact:false}).waitFor();assert.equal(await row.locator('[data-message-change]').count(),0);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep typing during write');assert.deepEqual(errors,[]);
  const topicMenu=async(name)=>{await page.locator('[data-topic]').click();await page.getByRole('menuitem',{name,exact:true}).click();};
  await topicMenu('Mark done');await page.getByRole('region',{name:'Topic state'}).getByText('Done',{exact:true}).waitFor();await page.getByText('Saving change…',{exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>fixture.writes.length),5);await page.evaluate(()=>fixture.finish(4,true));await page.waitForFunction(()=>!document.querySelector('[aria-label="Topic state"]'));
  await topicMenu('Rename…');await page.getByRole('textbox',{name:'Name',exact:true}).fill('Local topic title');await page.getByRole('button',{name:'Save',exact:true}).click();await page.locator('[data-topic]').getByText('Local topic title',{exact:true}).waitFor();
  await page.evaluate(()=>fixture.finish(5,true));await page.locator('[data-topic]').getByText('Chosen topic',{exact:true}).waitFor();await topicMenu('Rename…');assert.equal(await page.getByRole('textbox',{name:'Name',exact:true}).inputValue(),'Local topic title');
  await page.getByRole('button',{name:'Save',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===7);await page.evaluate(()=>fixture.finish(6));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  await topicMenu('Mark done');await page.waitForFunction(()=>fixture.writes.length===8);await page.evaluate(()=>fixture.finish(7));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  await page.getByRole('region',{name:'Topic state'}).getByRole('button',{name:'Reopen',exact:true}).click();await page.getByText('Saving change…',{exact:true}).waitFor();assert.equal(await page.getByRole('region',{name:'Topic state'}).count(),0);await page.evaluate(()=>fixture.finish(8));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  if(evidence)await page.screenshot({path:path.join(evidence,'recovery-'+width+'-'+count+'.png')});
  console.log(JSON.stringify({width,count,editImmediate:true,deleteImmediate:true,topicImmediate:true,failedDraftsRetained:true,newerSyncPreserved:true,unrelatedInput:true,timings:await page.evaluate(()=>fixture.timings),errors}));await page.close();
 }
}catch(error){if(lastPage&&!lastPage.isClosed()){console.error(await lastPage.evaluate(()=>({text:document.body.innerText.slice(-2500),message:window.fixture?.store.get().dm?.messages.at(-1),writes:window.fixture?.writes.map(w=>({p:w.p,b:w.b}))})));if(evidence)await lastPage.screenshot({path:path.join(evidence,'failure.png')});}throw error;}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}})().catch(e=>{console.error(e.stack);process.exitCode=1});
