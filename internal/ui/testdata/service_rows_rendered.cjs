// Participation records ("joined to help", "left · dismissed by …", "invited …")
// are one quiet centered line each, like Telegram service messages, never a card
// that fills the screen (owner, 2026-11-27). Synthetic data only.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-service-rows-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {Conversation} from './src/features/Conversation';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const self={person:'self',label:'Fixture owner',address:'self/laptop',fingerprint:'self-key',state:'self',admin:true},other={person:'peer',label:'Fixture peer',address:'peer/desktop',fingerprint:'peer-key',state:'pinned'};
const agent={pid:'agent-one',host:self,agent_id:'1'.repeat(32),state:'active',state_text:'Active',host_here:true,shared:['m0'],missing:16,can_ask:true,can_dismiss:true,inviter:self,invited:'2026-11-25T13:21:00Z',tasks_from:[],held:0,can_decide:false};
const at=i=>new Date(Date.parse('2026-11-25T13:00:00Z')+i*60000).toISOString();
const messages=[{id:'m0',lid:'m0',from:other.address,dir:'in',kind:'message',body:'Here is the price list for the overstock items.',at:at(0)}];
const kinds=[['invite','Fixture owner invited the agent into this group.'],['accept','The agent joined this group.'],['dismiss','Fixture owner dismissed the agent.']];
for(let i=0;i<9;i++){const [type,event]=kinds[i%3];messages.push({id:'e'+i,lid:'e'+i,from:self.address,dir:'out',kind:'event',event,event_type:type,event_by:'You',pid:'agent-one',at:at(i+1)});}
const group={seq:1,id:'group-one',kind:'group',title:'Fixture group',role:'member',members:[self,other],peer:other,created:'2026-01-01T00:00:00Z',messages,agents:[agent],guests:[]};
const overview={seq:1,version:'fixture',role:'person',groups:true,agents:true,me:{address:self.address,fingerprint:self.fingerprint},person:{...self,devices:[]},people:[other],dms:[group],threads:[],needs_you:[],agent_devices:[self.address]};
const host={platform:'daemon',workspace:{id:'service-rows',state:'enrolled'},listen(){return()=>{}},async api(p){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?'))return structuredClone(group);
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {typists:[]};return {};
}};
const store=new Store(host);window.fixture={store};
await store.start();await store.openChat({kind:'dm',id:'group-one'});
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><div style={{height:'100vh',display:'flex',flexDirection:'column'}}><Conversation o={{kind:'dm',id:'group-one'}}/></div></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
// Each built-in skin: Comic is the source; Office overlays its skin files (build.sh).
const bundles={};
for(const skin of ['comic','office']){
 const dir=path.join(work,skin);fs.mkdirSync(dir);
 fs.cpSync(path.join(web,'src'),path.join(dir,'src'),{recursive:true});
 if(skin==='office')for(const f of fs.readdirSync(path.resolve(web,'../skins/office/overlay')))fs.copyFileSync(path.resolve(web,'../skins/office/overlay',f),path.join(dir,'src/skin',f));
 for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(dir,'src',a));
 fs.symlinkSync(path.join(web,'node_modules'),path.join(dir,'node_modules'),'dir');
 const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:dir,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
 execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(dir,'src/styles.css'),'-o',path.join(dir,'style.css'),'--minify'],{cwd:dir,stdio:'pipe'});
 bundles[skin]={js,css:fs.readFileSync(path.join(dir,'style.css'))};
}
const server=http.createServer((req,res)=>{const [skin,file]=req.url.slice(1).split('/');const b=bundles[skin];if(b&&file==='entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(b.js)}if(b&&file==='style.css'){res.setHeader('Content-Type','text/css');return res.end(b.css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Service rows fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/'+(bundles[skin]?skin:'comic')+'/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/'+(bundles[skin]?skin:'comic')+'/entry.mjs"></script>')});
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
 for(const skin of ['comic','office'])for(const width of [1440,390]){
  const page=await browser.newPage({viewport:{width,height:900},deviceScaleFactor:2}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/'+skin+'/');
  const rows=page.locator('[data-mid^="e"]');await rows.first().waitFor();
  assert.equal(await rows.count(),9);
  if(process.env.AGENTNET_SERVICE_ROWS_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_SERVICE_ROWS_EVIDENCE+'-'+skin+'-'+width+'.png'});
  const boxes=[];for(let i=0;i<9;i++)boxes.push(await rows.nth(i).boundingBox());
  const tallest=Math.max(...boxes.map(b=>b.height)),total=boxes.reduce((s,b)=>s+b.height,0);
  // One line at desktop width; at most two at phone width. No card, no avatar.
  assert(tallest<=(width===390?44:24),skin+' '+width+': tallest service row '+tallest);
  for(let i=0;i<9;i++){const s=await rows.nth(i).evaluate(n=>{const c=getComputedStyle(n);return {tag:n.tagName,border:c.borderTopWidth,bg:c.backgroundColor,img:n.querySelectorAll('img,svg:not([aria-hidden])').length,size:c.fontSize}});
   assert.equal(s.tag,'P');assert.equal(s.border,'0px');assert.equal(s.bg,'rgba(0, 0, 0, 0)');assert.equal(s.size,'13px');}
  const joined=rows.nth(1),said=await joined.innerText();for(const part of ['joined to help','invited by you','saw 17 messages'])assert(said.includes(part),part+' in '+JSON.stringify(said));
  await joined.getByRole('button',{name:'See what it saw',exact:true}).click();await page.getByRole('dialog').waitFor();
  if(process.env.AGENTNET_SERVICE_ROWS_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_SERVICE_ROWS_EVIDENCE+'-'+skin+'-sheet-'+width+'.png'});
  await page.keyboard.press('Escape');await page.getByRole('dialog').waitFor({state:'hidden'});
  assert.deepEqual(errors,[]);reports.push({skin,width,tallest,total:Math.round(total)});await page.close();
 }
 console.log(JSON.stringify({ok:true,reports,scope:'participation records render as one quiet line each; See what they saw still opens'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
