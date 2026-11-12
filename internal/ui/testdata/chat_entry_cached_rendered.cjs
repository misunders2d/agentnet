// MEL-775: reopening a chat shows its cached copy while the fresh copy loads.
// The cached copy must already sit at the first unread message (or the
// latest one), never at the top of the mounted history.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http');
const engineName=process.env.AGENTNET_ENGINE||'chromium',engine=require(process.env.AGENTNET_PLAYWRIGHT)[engineName],web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-chat-entry-'));
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const[a,b]of Object.entries({'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'}))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const entry=`import React from 'react';
import {createRoot} from 'react-dom/client';
import {Store} from './src/store';
import {StoreContext} from './src/context';
import {OwnedContext} from './src/owned';
import {useStore} from './src/store';
import {Timeline} from './src/features/Conversation.timeline';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),q=new URL(location).searchParams,count=240,unread=Number(q.get('unread')||0),tall=q.has('tall'),id=n=>String(n).padStart(32,'0'),conv='c'.repeat(64),fp='f'.repeat(64);
const messages=Array.from({length:count},(_,i)=>({id:id(i+1),lid:id(i+1),dir:'in',from:'peer/laptop',claimed_key:fp,kind:'message',body:tall&&i===count-unread?Array.from({length:60},(_,n)=>'Tall unread paragraph '+n).join('\\n\\n'):'Message '+(i+1),at:new Date(17e11+i*36e5).toISOString(),unread:i>=count-unread,...(i>=count-unread?{attention:{rev:1,writer:fp}}:{})}));
const overview={seq:1,version:'test',me:{address:'self/laptop'},person:{person:'self',label:'Me'},people:[],reminders:[],threads:[],dms:[{id:conv,unread}],unread_total:unread,alert_total:unread};
let calls=0,held=[];
window.fixture={count,unread,id,get store(){return store;},held:()=>held.length,release(){for(const r of held.splice(0))r();}};
const store=new Store({platform:'android',android:{visible:()=>true,onVisibility:()=>()=>{}},workspace:{id:'chat-entry',state:'enrolled'},listen(){return()=>{};},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')){const view={id:conv,seq:overview.seq,kind:'dm',peer:{person:'peer',label:'Peer'},messages:structuredClone(messages),agents:[],guests:[]};if(++calls>1)await new Promise(r=>held.push(r));return view;}
 if(p==='/api/act'&&b.do==='read')return new Promise(()=>{});
 if(p.startsWith('/api/agents'))return{agents:[]};
 return{};}});
function Conv(){const s=useStore(store,()=>store.get()),open=s.open,readVisit=useStore(store,()=>open&&store.readVisit(open,'',false));
 if(!open||!s.dm)return null;
 const ctx={conv,dm:s.dm,thread:null,overview:s.overview,names:{},canReply:true};
 return <Timeline key={open.id+':main'} ctx={ctx} messages={s.dm.messages} focus={open.focus} focusSeq={open.focusSeq} unread={open.unread} readVisit={readVisit} selected={null} onSelect={()=>{}} empty={null}/>;}
function App(){return <div className="flex h-full flex-col"><nav><button onClick={()=>store.open({kind:'dm',id:conv})}>Open</button><button onClick={()=>store.close()}>Close</button></nav><Conv/></div>;}
await store.start();
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text,css=fs.readFileSync(path.resolve(web,'../static/skins/comic/style.css'));
const server=http.createServer((q,r)=>{if(q.url==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(js);}if(q.url==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css);}r.setHeader('Content-Type','text/html');r.end('<!doctype html><title>Chat entry regression</title><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>');});
// Where the view sits: the first unread row's top, or the gap below the latest row.
const place=page=>page.evaluate(()=>{const box=document.querySelector('[data-part="timeline"]')?.parentElement;if(!box)return null;const top=box.getBoundingClientRect().top,bottom=box.getBoundingClientRect().bottom,first=fixture.store.get().dm?.messages.find(m=>m.unread&&fixture.store.canMarkUnread(m)),u=box.querySelector('[aria-label="Unread messages"]'),last=box.querySelector('[data-mid="'+fixture.id(fixture.count)+'"]');return{unread:!!first,scrollTop:box.scrollTop,gap:box.scrollHeight-box.scrollTop-box.clientHeight,unreadTop:u?u.getBoundingClientRect().top-top:null,lastVisible:!!last&&last.getBoundingClientRect().top<bottom&&last.getBoundingClientRect().bottom>top,earlier:[...box.querySelectorAll('button')].some(b=>b.textContent==='Earlier messages'&&b.getBoundingClientRect().bottom>top),height:box.clientHeight};});
const right=p=>!!p&&!p.earlier&&(p.unread?p.unreadTop!==null&&p.unreadTop>=-2&&(p.unreadTop<=60||p.gap<2):p.gap<2&&p.lastVisible);
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await engine.launch({headless:true,...(engineName==='chromium'&&process.env.AGENTNET_CHROMIUM?{executablePath:process.env.AGENTNET_CHROMIUM}:{})});
 const base='http://127.0.0.1:'+server.address().port;
 for(const width of [1440,390])for(const [unread,tall] of [[0,false],[5,false],[5,true],[40,false]]){
  const page=await browser.newPage({viewport:{width,height:900},reducedMotion:'reduce'}),errors=[];page.on('pageerror',e=>errors.push(String(e)));
  await page.goto(base+'/?unread='+unread+(tall?'&tall':''));
  await page.getByText('Open',{exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('[data-part="timeline"] [data-mid]'));
  await page.waitForTimeout(300);
  const fresh=await place(page);
  assert(right(fresh),'first entry misplaced '+JSON.stringify({engine:engineName,width,unread,fresh}));
  await page.getByText('Close',{exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('[data-part="timeline"]'));
  const input=Date.now();
  await page.getByText('Open',{exact:true}).click();
  await page.waitForFunction(()=>fixture.held()>0&&document.querySelector('[data-part="timeline"] [data-mid]'));
  await page.waitForTimeout(300);
  const cached=await place(page),cachedMs=Date.now()-input;
  assert(right(cached),'cached reopen misplaced while the fresh copy loads '+JSON.stringify({engine:engineName,width,unread,cached}));
  await page.evaluate(()=>fixture.release());
  await page.waitForTimeout(300);
  const after=await place(page);
  assert(right(after),'reopen misplaced after the fresh copy '+JSON.stringify({engine:engineName,width,unread,after}));
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({engine:engineName,width,unread,tall,cachedMs,checks:['first-entry','cached-reopen','after-fresh']}));
  await page.close();}
}finally{await browser?.close();server.close();fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exit(1);});
