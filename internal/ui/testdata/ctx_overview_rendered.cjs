// MEL-748 S5: a list reload that changes nothing messages read (here: another
// chat's row) must not render the open chat's messages again. The timeline's
// ctx takes its overview through useCtxOverview; ?raw passes the overview as
// it comes, as before, and must cost far more. The reload is the page's own
// for a change naming another chat (store.ts scopedRefetch): the list only.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http');
const engineName=process.env.AGENTNET_ENGINE||'chromium',engine=require(process.env.AGENTNET_PLAYWRIGHT)[engineName],web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-ctx-overview-'));
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const[a,b]of Object.entries({'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'}))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const entry=`import React,{Profiler} from 'react';
import {createRoot} from 'react-dom/client';
import {Store} from './src/store';
import {StoreContext} from './src/context';
import {OwnedContext} from './src/owned';
import {useStore} from './src/store';
import {Timeline} from './src/features/Conversation.timeline';
import {useCtxOverview} from './src/features/Conversation';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),raw=new URL(location).searchParams.has('raw'),count=200,id=n=>String(n).padStart(32,'0'),conv='c'.repeat(64),fp='f'.repeat(64);
const messages=Array.from({length:count},(_,i)=>({id:id(i+1),lid:id(i+1),dir:i%3?'in':'out',from:'peer/laptop',claimed_key:fp,kind:i%7?'message':'question',body:'Message '+(i+1)+' with **some** words',at:new Date(17e11+i*36e5).toISOString()}));
let rev=1;const noop=()=>{};
const overview=()=>({seq:rev,version:'test',me:{address:'self/laptop'},person:{person:'self',label:'Me'},people:[],reminders:[],threads:[],dms:[{id:conv},{id:'d'.repeat(64),last:'other '+rev}]});
const commits=[];
window.fixture={get store(){return store;},commits,async reload(){rev++;await store.scopedRefetch(['c:'+'d'.repeat(64)]);}};
const store=new Store({platform:'android',android:{visible:()=>true,onVisibility:()=>()=>{}},workspace:{id:'ctx-overview',state:'enrolled'},listen(){return()=>{};},async api(p){
 if(p.startsWith('/api/overview'))return overview();
 if(p.startsWith('/api/dm?'))return {id:conv,kind:'dm',scope:['c:'+conv],peer:{person:'peer',label:'Peer'},messages:structuredClone(messages),agents:[],guests:[]};
 if(p==='/api/act')return{};
 if(p.startsWith('/api/agents'))return{agents:[]};
 return{};}});
function Conv(){const s=useStore(store,()=>store.get()),open=s.open,readVisit=useStore(store,()=>open&&store.readVisit(open,'',false));
 const stable=useCtxOverview(s.overview),o=raw?s.overview:stable;
 const ctx=React.useMemo(()=>s.dm&&({conv,dm:s.dm,thread:null,overview:o,names:{},canReply:true}),[s.dm,o]);
 if(!open||!ctx)return null;
 return <Profiler id="timeline" onRender={(_,phase,ms,base)=>commits.push({phase,ms,base})}><Timeline key={open.id+':main'} ctx={ctx} messages={s.dm.messages} focus={open.focus} focusSeq={open.focusSeq} unread={open.unread} readVisit={readVisit} selected={null} onSelect={noop} empty={null}/></Profiler>;}
function App(){return <div className="flex h-full flex-col"><nav><button onClick={()=>store.open({kind:'dm',id:conv})}>Open</button></nav><Conv/></div>;}
await store.start();
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"development"'},write:false}).outputFiles[0].text,css=fs.readFileSync(path.resolve(web,'../static/skins/comic/style.css'));
const server=http.createServer((q,r)=>{if(q.url==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(js);}if(q.url==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css);}r.setHeader('Content-Type','text/html');r.end('<!doctype html><title>Timeline ctx regression</title><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>');});
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await engine.launch({headless:true,...(engineName==='chromium'&&process.env.AGENTNET_CHROMIUM?{executablePath:process.env.AGENTNET_CHROMIUM}:{})});
 const base='http://127.0.0.1:'+server.address().port,cost={};
 for(const mode of ['stable','raw']){
  const page=await browser.newPage({viewport:{width:1280,height:900},reducedMotion:'reduce'}),errors=[];page.on('pageerror',e=>errors.push(String(e)));
  await page.goto(base+'/'+(mode==='raw'?'?raw':''));
  await page.getByText('Open',{exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('[data-part="timeline"] [data-mid]'));
  await page.waitForTimeout(500);
  cost[mode]=await page.evaluate(async()=>{fixture.commits.length=0;for(let i=0;i<8;i++){await fixture.reload();await new Promise(r=>setTimeout(r,30));}const c=fixture.commits.filter(c=>c.phase!=='mount');return {n:c.length,ms:c.reduce((a,c)=>a+c.ms,0),base:c.reduce((a,c)=>a+c.base,0)};});
  assert.deepEqual(errors,[]);
  await page.close();
 }
 console.log(JSON.stringify({engine:engineName,timelineRenderMs:cost}));
 assert(cost.stable.n>=8&&cost.stable.ms*3<cost.raw.ms,'a reload that changes no field messages read rendered the timeline again: '+JSON.stringify(cost));
 console.log('a list reload that changes nothing messages read leaves them unrendered PASS');
}finally{await browser?.close();server.close();fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exit(1);});
