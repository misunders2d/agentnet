// Real Sheet/Base UI with animation callbacks deliberately held after the
// trusted opening click. Visibility must not wait for transition bookkeeping.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-sheet-'));
fs.mkdirSync(path.join(work,'src/ui'),{recursive:true});fs.copyFileSync(process.env.AGENTNET_SHEET_BASELINE||path.join(web,'src/ui/Sheet.tsx'),path.join(work,'src/ui/Sheet.tsx'));fs.copyFileSync(path.join(web,'src/owned.tsx'),path.join(work,'src/owned.tsx'));fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const plusSource=fs.readFileSync(process.env.AGENTNET_PLUS_BASELINE||path.join(web,'src/features/Composer.parts.tsx'),'utf8');
const plus=plusSource.slice(plusSource.indexOf('export function PlusMenu('),plusSource.indexOf('/** ReplyChip:'));
assert(plus.startsWith('export function PlusMenu(')&&plus.includes('</Menu.Root>'),'real PlusMenu extraction');
fs.writeFileSync(path.join(work,'src/ui/PlusMenu.tsx'),`import React from 'react';import{Menu}from'@base-ui/react/menu';import{IconPlus}from'@tabler/icons-react';import{usePortal}from'../owned';${plus}`);
const entry=`
import React,{useState} from 'react';import{createRoot}from'react-dom/client';import{Sheet}from'./src/ui/Sheet';import{PlusMenu}from'./src/ui/PlusMenu';import{OwnedContext}from'./src/owned';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const raf=window.requestAnimationFrame,cancel=window.cancelAnimationFrame,pending=new Map();let seq=1000000,frozen=false;
const f=window.fixture={opened:0,visible:0,pending:()=>pending.size,freeze(){if(frozen)return;frozen=true;window.requestAnimationFrame=fn=>{const id=++seq;pending.set(id,fn);return id;};window.cancelAnimationFrame=id=>pending.delete(id);},resume(){window.requestAnimationFrame=raf;window.cancelAnimationFrame=cancel;frozen=false;for(const fn of pending.values())raf(fn);pending.clear();}};
new MutationObserver(()=>{const dialog=portals.querySelector('[role=dialog],[role=menu]');if(f.opened&&!f.visible&&dialog&&getComputedStyle(dialog).display!=='none'&&getComputedStyle(dialog).opacity==='1'&&dialog.getBoundingClientRect().width)f.visible=performance.now();}).observe(portals,{childList:true,subtree:true,attributes:true});
function App(){const[open,setOpen]=useState(false);return <><button onClick={()=>{f.opened=performance.now();f.visible=0;f.freeze();setOpen(true);}}>Open sheet</button><input aria-label="Unrelated draft"/><PlusMenu onOpenChange={open=>{if(open){f.opened=performance.now();f.visible=0;f.freeze();}}} actions={[{label:'Emoji',tone:'',icon:null,run:()=>{window.fixture.picked=true;}}]}/><Sheet open={open} onOpenChange={setOpen} title="Immediate actions" description="Actions stay visible while animation callbacks wait."><button>Details</button></Sheet></>}
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><App/></OwnedContext.Provider>);
`;
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text,css=fs.readFileSync(path.resolve(web,'../static/skins/comic/style.css'));
const server=http.createServer((q,r)=>{if(q.url==='/entry.mjs'){r.setHeader('Content-Type','text/javascript');return r.end(js)}if(q.url==='/style.css'){r.setHeader('Content-Type','text/css');return r.end(css)}r.setHeader('Content-Type','text/html');r.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}#app>button,#app>input{margin:16px;padding:12px}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
(async()=>{let browser;try{await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true});
 for(const width of [1440,390])for(const reducedMotion of ['no-preference','reduce']){
  const page=await browser.newPage({viewport:{width,height:900},reducedMotion}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);await page.goto('http://127.0.0.1:'+server.address().port);
  const press=async locator=>{const rect=await locator.boundingBox();assert(rect);await page.mouse.click(rect.x+rect.width/2,rect.y+rect.height/2);};
  await page.getByRole('button',{name:'Open sheet',exact:true}).waitFor();
  for(let attempt=0;attempt<2;attempt++){
   await press(page.getByRole('button',{name:'Open sheet',exact:true}));
   try{await page.waitForFunction(()=>fixture.visible>0,null,{polling:10,timeout:500});}catch(e){throw Error(JSON.stringify({width,reducedMotion,attempt,state:await page.evaluate(()=>({opened:fixture.opened,visible:fixture.visible,pending:fixture.pending(),inert:document.getElementById('app').inert,dialogs:[...document.querySelectorAll('[role=dialog]')].map(el=>({html:el.outerHTML.slice(0,700),display:getComputedStyle(el).display,opacity:getComputedStyle(el).opacity,rect:el.getBoundingClientRect().toJSON()}))})),errors}));}
   const result=await page.evaluate(()=>({domMs:fixture.visible-fixture.opened,heldCallbacks:fixture.pending(),modal:document.getElementById('app').inert}));assert(result.domMs<500);assert(result.heldCallbacks>0,'animation callbacks must actually remain held');assert(result.modal,'open sheet keeps background inert');
   const dialog=page.getByRole('dialog',{name:'Immediate actions'});await dialog.getByRole('button',{name:'Details',exact:true}).waitFor();
   if(process.env.AGENTNET_SHEET_EVIDENCE&&attempt===1){fs.mkdirSync(process.env.AGENTNET_SHEET_EVIDENCE,{recursive:true});await page.screenshot({path:path.join(process.env.AGENTNET_SHEET_EVIDENCE,width+'-'+reducedMotion+'.png')});}
   await press(dialog.getByRole('button',{name:'Close',exact:true}));await page.waitForFunction(()=>!document.getElementById('app').inert&&![...document.querySelectorAll('[role=dialog]')].some(el=>el.getBoundingClientRect().width>0),null,{polling:10,timeout:500});
   await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Usable after close '+attempt);assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Usable after close '+attempt);
   console.log(JSON.stringify({width,reducedMotion,attempt,...result,closedBeforeAnimationFrames:true}));
  }
  await page.evaluate(()=>fixture.resume());
  await press(page.getByRole('button',{name:'Add to message',exact:true}));
  await page.waitForFunction(()=>fixture.visible>0,null,{polling:10,timeout:500});
  const menuResult=await page.evaluate(()=>({domMs:fixture.visible-fixture.opened,heldCallbacks:fixture.pending()}));assert(menuResult.domMs<500);assert(menuResult.heldCallbacks>0);
  await press(page.getByRole('menuitem',{name:'Emoji',exact:true}));assert(await page.evaluate(()=>fixture.picked),'visible menu accepts intentional selection');
  console.log(JSON.stringify({width,reducedMotion,menu:'composer-plus',...menuResult}));
  await page.evaluate(()=>fixture.resume());assert.deepEqual(errors,[]);await page.close();
 }
}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true});}})().catch(e=>{console.error(e.stack);process.exitCode=1});
