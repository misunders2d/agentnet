// Real notification controls and native consent, trusted input, held/refused storage.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-notification-changes-'));
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {NotificationsSection} from './src/features/Settings.notify';
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const mode=new URL(location).searchParams.get('mode')||'daemon';
const history=Number(new URL(location).searchParams.get('history'));
const conv='a'.repeat(64);
const messages=Array.from({length:history},(_,i)=>({id:(i+1).toString(16).padStart(64,'0'),dir:'in',body:'Retained chat message '+i,unread:false}));
const overview={seq:1,version:'fixture',me:{address:'self/laptop'},people:[],dms:[],threads:[],needs_you:[],notify:{available:true,native:mode!=='browser',enabled:mode==='daemon'||mode==='windows',pending:false,preview:'',preview_default:'full',sound:mode==='windows'}};
const f=window.fixture={writes:[],reads:[],holdReads:false,timings:[],permissions:[],granted:false,connectionWrites:[],connectionReads:[],connectionTimings:[],connection:{enabled:false,badging:false},visibility:[],holdConnectionReads:false};
f.sound={source:'system',volume:100,revision:0,respect_dnd:false};f.soundWrites=[];
const status=()=>({granted:f.granted,app_granted:f.granted});
const host={platform:mode==='windows'?'daemon':mode,workspace:{id:'notify-changes-'+mode,state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview')){const snapshot=structuredClone(overview);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p.startsWith('/api/dm?'))return {id:conv,seq:1,messages};
 if(p==='/api/notify/sound'&&!b)return {...f.sound};
 if(p==='/api/notify/sound'||p==='/api/notify/sound-preview')return new Promise((resolve,reject)=>f.soundWrites.push({p,b,resolve,reject}));
 if(p.startsWith('/api/notify/'))return new Promise((resolve,reject)=>f.writes.push({p,b,resolve,reject}));
 if(p.startsWith('/api/typing'))return {preferences:{send:false,show:false},entries:[]};if(p.startsWith('/api/agents'))return {agents:[]};return {};
}};
if(mode==='android')host.android={visible:()=>true,notificationStatus:async()=>status(),requestNotifications:()=>new Promise(resolve=>f.permissions.push(granted=>{f.granted=granted;resolve(status())})),onVisibility:fn=>{f.visibility.push(fn);return()=>{f.visibility=f.visibility.filter(x=>x!==fn)}},connection:async()=>{const value={...f.connection};if(f.holdConnectionReads)await new Promise(resolve=>f.connectionReads.push(resolve));return value},setConnection:enabled=>new Promise((resolve,reject)=>f.connectionWrites.push({enabled,resolve,reject}))};
if(mode==='browser')Object.defineProperty(window,'Notification',{configurable:true,value:{permission:'default',requestPermission:()=>new Promise((resolve,reject)=>f.permissions.push(value=>{if(value==='error')reject(Error('Invented permission request failure'));else resolve(value?'granted':'denied')}))}});
const store=new Store(host);f.store=store;
f.retain=async()=>{await store.prefetch({kind:'dm',id:conv});return store.get().views[conv].messages.length};
f.sync=()=>{overview.seq++;f.holdReads=true;void store.refetch()};
f.finishSound=(index,failure)=>{
 const w=f.soundWrites[index];if(failure){w.reject(Error(failure));return;}
 if(w.p==='/api/notify/sound')f.sound={source:w.b.source,volume:w.b.volume,respect_dnd:w.b.respect_dnd,revision:f.sound.revision+1,...(w.b.file_name||f.sound.file_name?{file_name:w.b.file_name||f.sound.file_name}:{})};
 w.resolve({...f.sound});
};
f.finish=(index,fail=false,stale=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented notification refusal'));return;}
 if(!stale){overview.notify={...overview.notify,...(w.p.endsWith('preview')?{preview:w.b.preview}:{enabled:w.p.endsWith('enable')})};overview.seq++;}w.resolve({note:'Saved'});
};
f.newer=async(preview='sender')=>{overview.notify={...overview.notify,preview};overview.seq++;await store.refetch()};
f.finishConnection=(index,fail=false)=>{const w=f.connectionWrites[index];if(fail)w.reject(Error('Invented connection refusal'));else {f.connection={...f.connection,enabled:w.enabled};w.resolve({...f.connection})}};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
function App(){const s=useStore(store,s=>s);const [show,setShow]=React.useState(mode!=='windows');return <><button onClick={()=>store.showTab(s.tab==='settings'?'chats':'settings')}>Toggle view</button><output>{s.tab}</output>{mode==='windows'&&<button onClick={()=>setShow(!show)}>Toggle notifications panel</button>}{show&&<NotificationsSection/>}<textarea aria-label="Unrelated draft"/><div role="alert">{s.toasts.map(t=>t.text).join(' ')}</div></>}
await store.start();createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><App/></StoreContext.Provider></OwnedContext.Provider>);
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
const base=process.env.AGENTNET_NOTIFICATION_BASE;
if(base){
 const sources=execFileSync('git',['archive',base,'internal/ui/web/src'],{maxBuffer:16<<20});
 execFileSync('tar',['-x','-C',work,'--strip-components=3'],{input:sources});
}else fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{if(req.url==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Notification recovery fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});
const url=(search)=>'http://127.0.0.1:'+server.address().port+'/?'+search;
const frame=page=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve(performance.now()-fixture.inputStart)))));
async function soundTimings(browser) {
 const results=[];
 for(const width of [1440,390])for(const history of [640,6400]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[],feedback={};
  page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto(url(`mode=windows&history=${history}`));await page.waitForFunction(()=>window.fixture?.store);
  assert.equal(await page.evaluate(()=>fixture.retain()),history);
  const open=page.getByRole('button',{name:'Toggle notifications panel'}),heading=page.getByRole('heading',{name:'Notifications',exact:true});
  const timed=async(name,event,act,shown)=>{
   await page.evaluate(e=>document.addEventListener(e,()=>{fixture.inputStart=performance.now()},{once:true,capture:true}),event);
   await act();await shown();feedback[name]=await frame(page);
   assert(feedback[name]<1100,JSON.stringify({name,width,history,feedback}));
  };
  await page.evaluate(()=>fixture.sync());await page.waitForFunction(()=>fixture.reads.length===1);
  await timed('coldPanel','click',()=>open.click(),()=>heading.waitFor());
  const messagePreview=page.getByLabel('Message previews',{exact:true});
  const preview=page.getByRole('button',{name:'Play preview',exact:true}),save=page.getByRole('button',{name:'Save sound settings'});
  const sound=page.getByRole('combobox',{name:'Sound',exact:true});
  if(!base)assert.equal(await sound.inputValue(),'system');
  await timed('messagePrivacy','change',()=>messagePreview.selectOption('sender'),()=>page.getByText('Saving notification preference…',{exact:true}).waitFor());
  assert.equal(await messagePreview.inputValue(),'sender');await page.evaluate(()=>fixture.finish(0));
  await page.getByText('Notification preference saved; checking current state…',{exact:true}).waitFor();
  await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>!document.querySelector('#notification-preview').disabled);
  await open.click();await heading.waitFor({state:'hidden'});
  await timed('warmPanel','click',()=>open.click(),()=>heading.waitFor());
  if(base){assert.equal(await sound.count(),0);results.push({width,history,feedback});await page.close();continue;}
  const chooseFile=page.getByRole('button',{name:'Choose WAV file'}),volume=page.getByRole('slider',{name:'AgentNet volume'});
  const dnd=page.getByRole('switch',{name:'Respect Windows Do Not Disturb'});
  assert.equal(await dnd.getAttribute('aria-checked'),'false');
  await timed('builtIn','change',()=>sound.selectOption('chime'),()=>volume.waitFor());
  await timed('volumePointer','input',()=>volume.click({position:{x:50,y:16}}),()=>page.waitForFunction(()=>document.querySelector('[aria-label="AgentNet volume"]')?.value!=='100'));
  await volume.press('End');
  await timed('volume','input',()=>volume.press('ArrowLeft'),()=>page.getByText('99%',{exact:true}).waitFor());
  await timed('preview','click',()=>preview.click(),()=>page.getByText('Starting preview…',{exact:true}).waitFor());
  assert.equal(await page.evaluate(()=>fixture.soundWrites[0].p),'/api/notify/sound-preview');
  assert.equal(await page.evaluate(()=>fixture.soundWrites[0].b.volume),99);
  await page.evaluate(()=>fixture.finishSound(0));await page.getByText('Preview started.',{exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>fixture.sound.source),'system','preview must not save the draft');
  await timed('custom','change',()=>sound.selectOption('custom'),()=>chooseFile.waitFor());
  assert(await save.isDisabled());assert(await preview.isDisabled());
  await page.locator('input[type=file]').setInputFiles({name:'wrong.mp3',mimeType:'audio/mpeg',buffer:Buffer.from('ID3')});
  await page.getByText('Choose a WAV file up to 1 MiB.',{exact:true}).waitFor();
  const wav=Buffer.from('524946462600000057415645666d74201000000001000100401f0000803e00000200100064617461020000000001','hex');
  await timed('file','change',async()=>{
   const [picker]=await Promise.all([page.waitForEvent('filechooser'),chooseFile.click()]);
   await picker.setFiles({name:'Audio.wav',mimeType:'audio/wav',buffer:wav});
  },()=>page.getByText('Audio.wav',{exact:true}).waitFor());
  await timed('dndOn','click',()=>dnd.click(),()=>page.waitForFunction(()=>document.querySelector('[aria-label="Respect Windows Do Not Disturb"]')?.getAttribute('aria-checked')==='true'));
  const customSettings={source:'custom',volume:99,revision:0,respect_dnd:true,file_name:'Audio.wav',file:wav.toString('base64')};
  await timed('customPreview','click',()=>preview.click(),()=>page.getByText('Starting preview…',{exact:true}).waitFor());
  await page.waitForFunction(()=>fixture.soundWrites.length===2);
  assert.deepEqual(await page.evaluate(()=>fixture.soundWrites[1].b),customSettings);
  await page.evaluate(()=>fixture.finishSound(1));await page.getByText('Preview started.',{exact:true}).waitFor();
  await page.evaluate(()=>fixture.sync());await page.waitForFunction(()=>fixture.reads.length===1);
  await timed('save','click',()=>save.click(),()=>page.getByText('Saving sound settings…',{exact:true}).waitFor());
  await page.waitForFunction(()=>fixture.soundWrites.length===3);
  assert(await save.isDisabled());
  await save.dispatchEvent('click');assert.equal(await page.evaluate(()=>fixture.soundWrites.length),3,'double click must never write twice');
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Typing during held Windows sound save');
  await page.getByRole('button',{name:'Toggle view'}).click();
  await page.evaluate(()=>fixture.finishSound(2,'Invented Windows audio settings refusal.'));
  await page.getByText('Invented Windows audio settings refusal.',{exact:true}).waitFor();
  assert.equal(await sound.inputValue(),'custom');assert.equal(await volume.inputValue(),'99');assert(!await save.isDisabled());
  if(process.env.AGENTNET_NOTIFICATION_EVIDENCE)await page.screenshot({path:`${process.env.AGENTNET_NOTIFICATION_EVIDENCE}-sound-error-${width}-${history}.png`,fullPage:true});
  await timed('retry','click',()=>save.click(),()=>page.getByText('Saving sound settings…',{exact:true}).waitFor());
  await page.waitForFunction(()=>fixture.soundWrites.length===4);
  assert.deepEqual(await page.evaluate(()=>fixture.soundWrites[3].b),customSettings);
  await new Promise(resolve=>setTimeout(resolve,1150)); // deliberate slow save response
  await page.evaluate(()=>{fixture.release();fixture.finishSound(3)});
  await page.getByText('Sound settings saved on this device.',{exact:true}).waitFor();
  const confirmed=await frame(page);assert(confirmed>=1150);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Typing during held Windows sound save');
  await open.click();await heading.waitFor({state:'hidden'});await open.click();
  assert.equal(await sound.inputValue(),'custom');assert.equal(await volume.inputValue(),'99');assert(await save.isDisabled());
  assert.equal(await dnd.getAttribute('aria-checked'),'true');
  await timed('dndOff','click',()=>dnd.click(),()=>page.waitForFunction(()=>document.querySelector('[aria-label="Respect Windows Do Not Disturb"]')?.getAttribute('aria-checked')==='false'));
  await timed('system','change',()=>sound.selectOption('system'),()=>volume.waitFor({state:'hidden'}));
  await save.click();await page.waitForFunction(()=>fixture.soundWrites.length===5);await page.evaluate(()=>fixture.finishSound(4));
  await page.getByText('Sound settings saved on this device.',{exact:true}).waitFor();assert.equal(await preview.count(),0);
  assert.equal(await page.evaluate(()=>fixture.sound.respect_dnd),false);
  assert.deepEqual(errors,[]);results.push({width,history,feedback,confirmed});await page.close();
 }
 return results;
}
(async()=>{let browser;const reports=[];try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||undefined});
 if(!base&&!process.env.AGENTNET_NOTIFICATION_SOUND_ONLY)for(const width of [1440,390])for(const mode of ['daemon','android','browser']){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(5000);
  await page.goto('http://127.0.0.1:'+server.address().port+'/?mode='+mode);const toggle=page.getByRole('switch',{name:'Notifications',exact:true});await toggle.waitFor();await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));
  if(mode!=='daemon'){
   await toggle.click();await page.getByText('Waiting for notification permission…',{exact:true}).waitFor();assert.equal(await toggle.getAttribute('aria-checked'),'false','no ungranted native permission shown as enabled');assert.equal(await page.evaluate(()=>fixture.writes.length),0);
   await page.evaluate(()=>fixture.permissions.shift()(false));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await page.evaluate(()=>fixture.writes.length),0,'denial never reaches enable API');
   if(mode==='browser'){await toggle.click();await page.evaluate(()=>fixture.permissions.shift()('error'));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await page.evaluate(()=>fixture.writes.length),0,'permission exception recovers without enabling');}
   await toggle.click();await page.evaluate(()=>fixture.permissions.shift()(true));await page.waitForFunction(()=>fixture.writes.length===1);await page.waitForFunction(()=>document.querySelector('[role="switch"][aria-label="Notifications"]').getAttribute('aria-checked')==='true');assert.equal(await toggle.getAttribute('aria-checked'),'true');await page.evaluate(()=>fixture.finish(0));await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));
  }
  await page.evaluate(()=>document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[role="switch"][aria-label="Notifications"]').getAttribute('aria-checked')!=='false')return;observer.disconnect();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push(performance.now()-start)))});observer.observe(document.body,{subtree:true,attributes:true})},{once:true,capture:true}));
  const off=await page.evaluate(()=>fixture.writes.length);await toggle.click();assert.equal(await toggle.getAttribute('aria-checked'),'false','disable changes before blocked storage');await page.getByText('Saving notification preference…',{exact:true}).waitFor();
  await page.evaluate(()=>{const b=document.querySelector('[role="switch"][aria-label="Notifications"]');b.dispatchEvent(new MouseEvent('click',{bubbles:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true}))});assert.equal(await page.evaluate(()=>fixture.writes.length),off+1,'duplicate input cannot enqueue a second preference');
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retained while notification storage is blocked');await page.getByRole('button',{name:'Toggle view'}).click();await page.evaluate(i=>fixture.finish(i,true),off);await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await toggle.getAttribute('aria-checked'),'true','refusal restores current enabled value');
  if(mode!=='browser'){
   const select=page.getByLabel('Message previews',{exact:true}),index=await page.evaluate(()=>fixture.writes.length);await select.selectOption('hidden');assert.equal(await select.inputValue(),'hidden','preview selection stays selected before storage');assert.equal(await toggle.getAttribute('aria-checked'),'true','preview does not change notification consent');
   await page.evaluate(()=>fixture.newer());assert.equal(await select.inputValue(),'sender','newer linked preference wins');await page.evaluate(i=>fixture.finish(i,false,true),index);await page.waitForFunction(()=>!document.querySelector('#notification-preview').disabled);assert.equal(await select.inputValue(),'sender','older success cannot undo newer preference');
   const next=await page.evaluate(()=>fixture.writes.length);await select.selectOption('hidden');await page.evaluate(i=>fixture.finish(i,true),next);await page.waitForFunction(()=>!document.querySelector('#notification-preview').disabled);assert.equal(await select.inputValue(),'sender','failure reveals current preference');
  }
  const last=await page.evaluate(()=>fixture.writes.length);await toggle.click();await page.evaluate(i=>{fixture.holdReads=true;fixture.finish(i)},last);await page.getByText('Notification preference saved; checking current state…',{exact:true}).waitFor();assert.equal(await toggle.getAttribute('aria-checked'),'false');await page.getByRole('button',{name:'Check notification status',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.writes.length),last+1,'status check never repeats the preference write');await page.evaluate(()=>fixture.release());await page.waitForFunction(()=>!document.querySelector('[role="switch"][aria-label="Notifications"]').hasAttribute('data-disabled'));assert.equal(await toggle.getAttribute('aria-checked'),'false');
  if(mode==='android'){
   const connection=page.getByRole('switch',{name:'Background connection',exact:true});
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));
   await page.evaluate(()=>{fixture.holdConnectionReads=true;for(const fn of fixture.visibility)fn()});
   await page.waitForFunction(()=>fixture.connectionReads.length===1);
   await page.evaluate(()=>document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{if(document.querySelector('[aria-label="Background connection"]').getAttribute('aria-checked')!=='true')return;observer.disconnect();requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.connectionTimings.push(performance.now()-start)))});observer.observe(document.body,{subtree:true,attributes:true})},{once:true,capture:true}));
   await connection.click();assert.equal(await connection.getAttribute('aria-checked'),'true','background connection updates before native reply');
   await page.getByText('Changing background connection…',{exact:true}).waitFor();
   if(process.env.AGENTNET_NOTIFICATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_NOTIFICATION_EVIDENCE+'-connection-pending-'+width+'.png',fullPage:true});
   await page.evaluate(()=>{const b=document.querySelector('[aria-label="Background connection"]');b.dispatchEvent(new MouseEvent('click',{bubbles:true}));b.dispatchEvent(new MouseEvent('click',{bubbles:true}))});
   assert.equal(await page.evaluate(()=>fixture.connectionWrites.length),1,'duplicate connection input cannot enqueue another native operation');
   await page.evaluate(()=>{fixture.holdConnectionReads=false;for(const resolve of fixture.connectionReads.splice(0))resolve()});
   assert.equal(await connection.getAttribute('aria-checked'),'true','stale pre-click read cannot erase pending choice');
   await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Retained while notification storage is blocked');await page.getByRole('button',{name:'Toggle view'}).click();
   await page.evaluate(()=>fixture.finishConnection(0,true));await page.getByText('Invented connection refusal',{exact:true}).waitFor();
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));
   assert.equal(await connection.getAttribute('aria-checked'),'false','refused native change restores current setting');
   if(process.env.AGENTNET_NOTIFICATION_EVIDENCE)await page.screenshot({path:process.env.AGENTNET_NOTIFICATION_EVIDENCE+'-connection-error-'+width+'.png',fullPage:true});
   await connection.click();assert.equal(await connection.getAttribute('aria-checked'),'true');await page.evaluate(()=>fixture.finishConnection(1));
   await page.waitForFunction(()=>!document.querySelector('[aria-label="Background connection"]').hasAttribute('data-disabled'));assert.equal(await connection.getAttribute('aria-checked'),'true');
   // A newer native Stop is authoritative after the completed request.
   await page.evaluate(()=>{fixture.connection={enabled:false,badging:false};for(const fn of fixture.visibility)fn()});
   await page.waitForFunction(()=>document.querySelector('[aria-label="Background connection"]').getAttribute('aria-checked')==='false');
  }
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Retained while notification storage is blocked');await page.waitForFunction(()=>fixture.timings.length===1);const times=await page.evaluate(()=>fixture.timings);assert(times.every(t=>t<500),JSON.stringify(times));assert.deepEqual(errors,[]);const connectionTimes=await page.evaluate(()=>fixture.connectionTimings);if(mode==='android')assert(connectionTimes.length===1&&connectionTimes.every(t=>t<500),JSON.stringify(connectionTimes));reports.push({width,mode,feedbackMs:times,connectionFeedbackMs:connectionTimes});await page.close();
 }
 const sound=await soundTimings(browser);
 console.log(JSON.stringify({ok:true,reports,sound,base:base||null,scope:'trusted browser input and held synthetic storage; permission/audio bridge simulated'}));
 }finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
