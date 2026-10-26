// Actual Timeline/MessageView: navigation reuses unchanged retained bubbles.
// Instrument only the real memoized component's invocation, not its output.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-timeline-navigation-'));
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
for(const [a,b]of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const message=path.join(work,'src/features/Message.tsx'),original=fs.readFileSync(message,'utf8'),needle='function MessageView(p: MessageProps) {';
assert(original.includes(needle));fs.writeFileSync(message,original.replace(needle,needle+' window.fixture.renders.push(p.m.id);'));
if(process.env.AGENTNET_TIMELINE_BASELINE)fs.copyFileSync(process.env.AGENTNET_TIMELINE_BASELINE,path.join(work,'src/features/Conversation.timeline.tsx'));
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';import {Timeline} from './src/features/Conversation.timeline';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals'),id=n=>String(n).padStart(32,'0');
window.fixture={renders:[]};const count=Number(new URL(location).searchParams.get('count'))||100;
const messages=Array.from({length:count},(_,i)=>({id:id(i+1),lid:id(i+1),dir:'out',from:'self/laptop',kind:'message',body:'Retained message '+(i+1),at:new Date(1793016000000+i*60000).toISOString(),can:['react'],reactions:[]}));
messages.at(-1).quote=id(count-12);
const overview={me:{address:'self/laptop'},person:{person:'self',label:'Me'},people:[],reminders:[]},dm={id:'chat',kind:'dm',peer:{person:'peer',label:'Peer'},messages,agents:[],guests:[]},ctx={conv:'chat',dm,thread:null,overview,names:{},canReply:true};
const store=new Store({platform:'daemon',workspace:{id:'navigation',state:'enrolled'},async api(){return{};}}),select=()=>{};
function App(){const [focus,setFocus]=useState(id(count)),[seq,setSeq]=useState(0),[visit,setVisit]=useState({scope:'first'});return <div className="flex h-full flex-col"><nav><button onClick={()=>{setFocus(id(count-8));setSeq(s=>s+1);}}>Notification</button><button onClick={()=>{setVisit({scope:'new'});setFocus(id(count));setSeq(s=>s+1);}}>New visit</button></nav><Timeline ctx={ctx} messages={messages} focus={focus} focusSeq={seq} readVisit={visit} selected={null} onSelect={select} empty={null}/><form data-conv="chat"><textarea aria-label="Draft"/></form></div>}
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
const css=fs.readFileSync(path.resolve(web,'../static/skins/comic/style.css'));
const server=http.createServer((q,r)=>{if(q.url==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(js)}if(q.url==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css)}r.setHeader('Content-Type','text/html');r.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}nav,form{padding:12px}nav button{min-height:44px;margin-right:20px}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390])for(const count of [100,1000]){
  const page=await browser.newPage({viewport:{width,height:900},reducedMotion:'reduce'}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?count='+count);
  const row=n=>page.locator('[data-mid="'+String(n).padStart(32,'0')+'"]');await row(count).waitFor();
  // Keep several pages mounted; navigation must not redraw any unchanged row.
  for(let i=0;i<3&&await page.getByRole('button',{name:'Earlier messages',exact:true}).count();i++)await page.getByRole('button',{name:'Earlier messages',exact:true}).click();
  const retained=await page.locator('[data-mid]').count();assert(retained>=Math.min(count,200));
  const visible=async n=>{await page.waitForFunction(id=>{const row=document.querySelector('[data-mid="'+id+'"]'),box=document.querySelector('[data-part="timeline"]').parentElement,r=row?.getBoundingClientRect(),b=box.getBoundingClientRect();return r&&r.bottom>b.top&&r.top<b.bottom;},String(n).padStart(32,'0'));};
  const measure=async(label,action,target,max)=>{await page.evaluate(()=>fixture.renders=[]);await action();await visible(target);const renders=await page.evaluate(()=>fixture.renders.length);assert(renders<=max,label+' redrew '+renders+' of '+retained+' retained bubbles');assert.equal(await row(target).locator('[data-message-bubble]').getAttribute('tabindex'),'0');assert.equal(await page.locator('[data-mid]').count(),retained);console.log(JSON.stringify({width,count,label,retained,renders}));};
  await measure('notification',()=>page.getByRole('button',{name:'Notification',exact:true}).click(),count-8,2);
  await measure('quote',()=>row(count).getByRole('button',{name:new RegExp('Retained message '+(count-12)+'$')}).click(),count-12,4);
  await measure('new-visit',()=>page.getByRole('button',{name:'New visit',exact:true}).click(),count,2);
  await measure('quote-after-new-visit',()=>row(count).getByRole('button',{name:new RegExp('Retained message '+(count-12)+'$')}).click(),count-12,4);
  await page.getByRole('textbox',{name:'Draft'}).fill('Keep typing');assert.equal(await page.getByRole('textbox',{name:'Draft'}).inputValue(),'Keep typing');assert.deepEqual(errors,[]);await page.close();
 }
}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1});
