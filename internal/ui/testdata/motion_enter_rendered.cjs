// Real Swap and shipping CSS: settle existing entrance animations without a
// computed-animation scan; keep later arrivals and continuous activity alive.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),web=path.resolve(__dirname,'../web');
const entry=`
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';import {Swap,useLeaving} from './src/ui/Motion';
const original=Element.prototype.getAnimations;window.animationScans=0;Element.prototype.getAnimations=function(...args){window.animationScans++;return original.apply(this,args)};
function Pane({id}){const leaving=useLeaving(),[late,setLate]=useState(false);return <section data-pane={id} data-leaving={String(leaving)}><h1 className={"fade-in "+(late?"font-bold":"font-normal")}>Pane {id}</h1><div className="pop-in">Ready content</div><div className="working-dot">Active work</div>{Array.from({length:1000},(_,i)=><p key={i}>Retained row {i}</p>)}<button onClick={()=>setLate(true)}>Add arrival</button>{late&&<div data-late className="pop-in">New arrival</div>}</section>}
const paused=new URLSearchParams(location.search).has('paused');if(paused){const set=window.setTimeout;window.setTimeout=(fn,ms,...args)=>ms===240?0:set(fn,ms,...args);}
function App(){const [id,setID]=useState('first');return <><button onClick={()=>setID(id==='first'?'second':'first')}>Switch pane</button><Swap id={id} enter={paused?"an-page-in-next":"an-pane-in"} leave={paused?"an-page-out-next":"an-pane-out"} ms={paused?240:220} label="fixture"><Pane id={id}/></Swap></>}
createRoot(document.getElementById('app')).render(<App/>);
`;
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:web,loader:'tsx'},bundle:true,format:'esm',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
const css=fs.readFileSync(path.resolve(web,'../static/skins/comic/style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>html,body{margin:0}#app{height:100vh}body.hold [data-swap] *{animation-play-state:paused!important}</style><script>if(location.search.includes("paused"))document.documentElement.className="paused"</script><div class="an-root"><div id="app" class="an-app"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390])for(const reducedMotion of ['no-preference','reduce']){
  const page=await browser.newPage({viewport:{width,height:900},reducedMotion});const errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port);await page.getByRole('button',{name:'Switch pane'}).click();const current=page.locator('[data-swap-side="current"] [data-pane="second"]');await current.waitFor();
  assert.equal(await page.evaluate(()=>window.animationScans),0,'navigation must not flush computed animations across the whole conversation');
  const entrances=await current.locator('.fade-in,.pop-in').evaluateAll(nodes=>nodes.map(n=>({opacity:getComputedStyle(n).opacity,duration:getComputedStyle(n).animationDuration})));assert(entrances.length>=2);assert(entrances.every(n=>n.opacity==='1'),'new pane content starts fully shown');
  assert.equal(await current.getAttribute('data-leaving'),'false');
  const old=page.locator('[data-swap-side="leaving"]');if(await old.count()){assert.equal(await old.getAttribute('inert'),'');assert.equal(await old.locator('[data-pane]').getAttribute('data-leaving'),'true');}
  await old.waitFor({state:'detached'});await current.getByRole('button',{name:'Add arrival'}).click();const late=current.locator('[data-late]');await late.waitFor();if(reducedMotion==='no-preference')assert.notEqual(await late.evaluate(n=>getComputedStyle(n).animationDuration),'0s');else assert.equal(await late.evaluate(n=>getComputedStyle(n).animationName),'none');assert.equal(await current.locator('h1').evaluate(n=>getComputedStyle(n).opacity),'1','ordinary class updates must not restart settled entrance');
  if(reducedMotion==='no-preference')assert.equal(await current.locator('.working-dot').evaluate(n=>getComputedStyle(n).animationIterationCount),'infinite','working activity remains active');
  assert.deepEqual(errors,[]);console.log(JSON.stringify({width,reducedMotion,existingEntranceSettled:true,laterEntrancePreserved:true,animationScans:await page.evaluate(()=>window.animationScans)}));
  await page.goto('http://127.0.0.1:'+server.address().port+'/?paused');await page.evaluate(()=>document.body.classList.add('hold'));
  const started=Date.now();await page.getByRole('button',{name:'Switch pane'}).click();
  await page.waitForFunction(()=>{const old=document.querySelector('[data-swap-side="leaving"]'),now=document.querySelector('[data-swap-side="current"] h1');if(!old||!now)return false;const rect=now.getBoundingClientRect(),style=getComputedStyle(old);return now.textContent==='Pane second'&&rect.left<innerWidth&&rect.right>0&&rect.top<innerHeight&&rect.bottom>0&&(style.visibility==='hidden'||style.display==='none'||style.opacity==='0');},null,{polling:10,timeout:500});
  assert(Date.now()-started<500,'new navigation must be visible while animation and cleanup wait');assert.equal(await page.locator('[data-swap-side="leaving"]').count(),1,'cleanup timer must still be held');
  console.log(JSON.stringify({width,reducedMotion,pausedNavigationVisible:true}));await page.close();
 }
}finally{await browser?.close();await new Promise(r=>server.close(r));}})().catch(e=>{console.error(e.stack);process.exitCode=1});
