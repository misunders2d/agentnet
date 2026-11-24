// Production loader + source-built Comic; fictional image bytes and file API.
// CSS/bundle output stays in /tmp until root rebuilds the integrated package.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http'),os=require('node:os'),{execFileSync}=require('node:child_process');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const {createHash}=require('node:crypto');
const resource=!!process.env.AGENTNET_IMAGE_RESOURCE;
const assets=path.resolve(__dirname,'../static'),web=path.resolve(__dirname,'../web'),evidence=process.env.AGENTNET_SCREENSHOTS;
const comic=path.join(assets,'skins/comic');
const skin=path.resolve(process.env.AGENTNET_IMAGE_PACKAGE||comic);
const manifest=dir=>({...JSON.parse(fs.readFileSync(path.join(dir,'skin.json'))),digest:'0'.repeat(64)});
const selected=manifest(skin);
const skinRoute='/assets/skins/'+selected.id+'/';
const latency=!!process.env.AGENTNET_IMAGE_LATENCY;
if(evidence)fs.mkdirSync(evidence,{recursive:true});
const conv='a'.repeat(64),other='b'.repeat(64),message='c'.repeat(32),handle='d'.repeat(32),at='2026-10-20T12:00:00Z';
const me={person:'1'.repeat(32),label:'Tester',address:'tester/laptop',state:'self'},peer={person:'2'.repeat(32),label:'Casey',address:'casey/desktop',state:'pinned',devices:[]};
let bundle,styles,browser,fixture;const images=new Map(),temporary=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-message-images-'));
const cases={portraits:[['phone-one.png','portrait'],['phone-two.png','portrait']],single:[['single-phone.png','portrait']],landscapes:[['wide-one.png','landscape'],['wide-two.png','landscape-2'],['wide-three.png','landscape-3']],many:Array.from({length:7},(_,i)=>['ordered-'+(i+1)+'.png',i%2?'landscape':'portrait']),mixed:[['first.png','portrait'],['notes.txt','text'],['second.png','landscape'],['third.png','portrait']],failed:[['first.png','portrait'],['failed.png','invalid'],['last.png','landscape']]};
function reset(mode, history){
 const files=cases[mode].map(([name,kind],i)=>({index:2+i*3,name,size:images.get(kind).length,openable:true}));
 const dir=mode==='landscapes'?'out':'in';
 const row={id:message,lid:message,dir,from:dir==='out'?me.address:peer.address,kind:'message',body:mode==='portraits'?'Two original phone screenshots':'Original attachments · '+mode,at,attachments:files,actions:[],reactions:[]};
 const dm=(id,messages)=>({id,created:at,kind:'dm',peer,mine:true,role:'member',members:[me,peer],agents:[],guests:[],frozen:'',messages,topics:[]});
 const earlier=Array.from({length:history-1},(_,i)=>{
  const id=(i+1).toString(16).padStart(32,'0');
  return {...row,id,lid:id,body:'Retained fixture message '+(i+1),attachments:[]};
 });
 fixture={mode,dir,files,reads:[],posts:[],events:new Set(),seq:1,views:{[conv]:dm(conv,[...earlier,row]),[other]:dm(other,[{...row,id:'e'.repeat(32),lid:'e'.repeat(32),body:'Other chat context',attachments:[]}])}};
 fixture.overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:{max_count:8,max_file:10485760,max_message:20971520},controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[conv,other].map(id=>({id,created:at,peer,kind:'dm',role:'member',count:id===conv?history:1,title:id===conv?'Original images':'Other chat',last:id===conv?row.body:'Other chat context',last_at:at,unread:0,held:0,waiting:0,guests:0})),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
}
const json=(res,value)=>{res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const server=http.createServer(async(req,res)=>{
 const u=new URL(req.url,'http://127.0.0.1'),p=u.pathname;
 if(p==='/favicon.ico'||/^\/assets\/icon-(192|512)\.png$/.test(p)){res.writeHead(204).end();return;}
 if(p==='/'){res.setHeader('Content-Type','text/html');res.end(fs.readFileSync(path.join(assets,'index.html')));return;}
 if(p==='/assets/skins/index.json'){json(res,selected.id==='comic'?[selected]:[manifest(comic),selected]);return;}
 if(p===skinRoute+'entry.mjs'){res.setHeader('Content-Type','text/javascript');res.end(bundle);return;}
 if(p===skinRoute+'style.css'){res.setHeader('Content-Type','text/css');res.end(styles);return;}
 if(p.startsWith('/assets/')){const packageFile=p.startsWith(skinRoute);const base=packageFile?skin:assets;const file=path.resolve(base,p.slice(packageFile?skinRoute.length:'/assets/'.length));if(file.startsWith(base+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',/\.(mjs|js)$/.test(file)?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 if(p==='/api/workspaces'){json(res,[{id:'default',handle,state:'enrolled',name:'Fictional image workspace',address:me.address,realm:'3'.repeat(32),endpoint:'https://fixture.invalid'}]);return;}
 if(p.endsWith('/events')){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache'});res.write(': ready\n\n');fixture.events.add(res);req.on('close',()=>fixture.events.delete(res));return;}
 const api=p.replace(/^\/workspaces\/default\/[a-f0-9]{32}/,'');
 if(api.startsWith('/api/')){
  const chunks=[];for await(const chunk of req)chunks.push(chunk);const raw=Buffer.concat(chunks),body=raw.length&&req.headers['content-type']==='application/json'?JSON.parse(raw):undefined;
  if(body!==undefined)fixture.posts.push({api,body});
  if(api==='/api/overview'){json(res,fixture.overview);return;}
  if(api==='/api/dm'){const id=u.searchParams.get('id');if(id===conv&&fixture.sync){fixture.sync.started.resolve();await fixture.sync.release.promise;}json(res,fixture.views[id]);return;}
  const file=/^\/api\/files\/([^/]+)\/(\d+)$/.exec(api);
  if(file){const index=Number(file[2]),f=fixture.files.find(f=>f.index===index);fixture.reads.push({id:file[1],index,dir:u.searchParams.get('dir')});assert.equal(file[1],message);assert.equal(u.searchParams.get('dir'),fixture.dir);assert(f);if(index===8&&fixture.preview)await fixture.preview.promise;const kind=cases[fixture.mode][fixture.files.indexOf(f)][1];res.writeHead(200,{'Content-Type':'application/octet-stream'});res.end(images.get(kind));return;}
  if(api==='/api/agents'){json(res,{agents:[],sessions:[],local:false});return;}
  if(api==='/api/groups/invitations'){json(res,[]);return;}
  if(api==='/api/typing/status'){json(res,{send:false,scopes:[]});return;}
  if(api==='/api/topics'){json(res,{topics:[],matched:0});return;}
  if(api==='/api/drive'&&!body){json(res,{enabled:true,configured:true,connected:true,space:{name:'Fixture space'}});return;}
  if(api==='/api/drive'&&body?.action==='save-attachment'){json(res,{note:'Saved fixture copy.'});return;}
  json(res,{});return;
 }
 res.writeHead(404).end('No image fixture route');
});
async function source(){
 if(process.env.AGENTNET_IMAGE_BUILT){assert(!process.env.AGENTNET_IMAGE_BASELINE,'built and source baseline modes are exclusive');bundle=fs.readFileSync(path.join(skin,'entry.mjs'),'utf8');styles=fs.readFileSync(path.join(skin,'style.css'));return;}
 const replacements={'optimistic.mjs':'../static/optimistic.mjs','pictures.mjs':'../static/pictures.mjs','person-topics.mjs':'../skins/shared/person-topics.mjs','reaction-preferences.mjs':'../skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'../skins/shared/collective-mentions.mjs','drive-space.mjs':'../skins/shared/drivespace.mjs','drive-setup.mjs':'../skins/shared/drivespace-setup.mjs'};
 const built=await require('../web/node_modules/esbuild').build({entryPoints:[path.join(web,'src/main.tsx')],bundle:true,write:false,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',minify:true,define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'source-inputs',setup(build){build.onResolve({filter:/\.mjs$/},args=>{const name=path.basename(args.path);if(replacements[name]&&!fs.existsSync(path.resolve(args.resolveDir,args.path)))return{path:path.resolve(web,replacements[name])};});if(process.env.AGENTNET_IMAGE_BASELINE)build.onLoad({filter:/\/features\/Message\.files\.tsx$/},()=>({contents:fs.readFileSync(process.env.AGENTNET_IMAGE_BASELINE,'utf8'),loader:'tsx',resolveDir:path.join(web,'src/features')}));}}]});bundle=built.outputFiles[0].text;
 const css=path.join(temporary,'style.css');execFileSync(path.join(web,'node_modules/.bin/tailwindcss'),['-i','src/styles.css','-o',css,'--minify'],{cwd:web,stdio:'pipe'});styles=fs.readFileSync(css);
}
async function makeImages(){const page=await browser.newPage();const data=await page.evaluate(()=>{
 const result={};for(const [kind,w,h,bg]of [['portrait',1080,2400,'#161b20'],['landscape',2400,1200,'#224346'],['landscape-2',2400,1200,'#51394e'],['landscape-3',2400,1200,'#554631']]){const c=document.createElement('canvas');c.width=w;c.height=h;const x=c.getContext('2d');x.fillStyle=bg;x.fillRect(0,0,w,h);x.fillStyle='#edf2f4';x.font='bold 46px sans-serif';x.fillText('13:50 · Harbor phone',60,100);x.fillStyle='#d5dfdf';x.font='34px sans-serif';x.fillText('Original screenshot · '+kind,60,164);for(let n=0;n<7;n++){const y=220+n*285;if(y+230>h)break;x.fillStyle=n%2?'#29343c':'#222c33';x.beginPath();x.roundRect(35,y,w-70,240,28);x.fill();x.fillStyle='#83c9b4';x.beginPath();x.roundRect(65,y+35,62,62,14);x.fill();x.fillStyle='#eaf1f2';x.font='bold 34px sans-serif';x.fillText(['Notes','Calendar','Harbor','Tasks'][n%4]+' · '+(n+1),150,y+78);x.fillStyle='#c1cbcf';x.font='32px sans-serif';x.fillText('Casey · Invoice review',65,y+145);x.fillText('The original document is ready to read.',65,y+194);}result[kind]=c.toDataURL('image/png').split(',')[1];}return result;
 });await page.close();for(const[k,v]of Object.entries(data))images.set(k,Buffer.from(v,'base64'));images.set('text',Buffer.from('Original text attachment\n'));images.set('invalid',Buffer.from('This is not an image.'));}
function imageButtons(page){return page.getByRole('button',{name:/^Open picture /});}
async function recordInput(control, type) {
 await control.evaluate((node,event)=>{
  window.imageInputAt=null;
  node.addEventListener(event,e=>{if(e.isTrusted)window.imageInputAt=performance.now();},{once:true,capture:true});
 },type);
}
async function measureSinceInput(page, paint=false) {
 const ms=await page.evaluate(async frames=>{
  if(frames)await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  return window.imageInputAt==null?null:performance.now()-window.imageInputAt;
 },paint);
 assert.notEqual(ms,null,'trusted input timestamp');
 return Math.round(ms);
}
async function holdNewArrival(page) {
 assert(fixture.events.size,'push stream connected');
 const refreshing=page.waitForRequest(req=>{
  const url=new URL(req.url());
  return url.pathname.endsWith('/api/dm')&&url.searchParams.get('id')===conv;
 },{timeout:5000});
 const sync={started:Promise.withResolvers(),release:Promise.withResolvers()};
 fixture.sync=sync;
 const view=fixture.views[conv],id='f'.repeat(32);
 view.messages.push({...view.messages.at(-1),id,lid:id,body:'Concurrent fixture arrival',attachments:[]});
 fixture.overview.seq=++fixture.seq;
 for(const stream of fixture.events)stream.write('event: change\ndata: '+fixture.seq+'\n\n');
 await refreshing;
 await sync.started.promise;
 return async()=>{
  sync.release.resolve();
  fixture.sync=null;
  await page.locator('p').filter({hasText:'Concurrent fixture arrival'}).waitFor({state:'attached'});
 };
}
async function verifyOriginal(page, thumb, name, kind, phone, concurrent, metric) {
 await thumb.scrollIntoViewIfNeeded();
 if(!phone)await thumb.focus();
 await recordInput(thumb,phone?'click':'keydown');
 if(phone)await thumb.tap();else await page.keyboard.press('Enter');
 const dialog=page.getByRole('dialog',{name,exact:true});
 await dialog.waitFor();
 const openMs=await measureSinceInput(page,true);
 assert(openMs<1100,'viewer opens within the local feedback bound');
 const original=dialog.getByRole('img',{name,exact:true});
 assert.equal(await original.getAttribute('src'),await thumb.locator('img').getAttribute('src'));
 await original.evaluate(im=>im.decode());
 const fullImageMs=await measureSinceInput(page);
 const button=dialog.getByRole('button',{name:'Download',exact:true});
 await recordInput(button,'click');
 const downloaded=page.waitForEvent('download');
 if(phone)await button.tap();else await button.click();
 const saved=await downloaded;
 const downloadStartedMs=await measureSinceInput(page);
 assert(downloadStartedMs<1100,'browser starts the download within the local feedback bound');
 assert.equal(saved.suggestedFilename(),name);
 assert.deepEqual(fs.readFileSync(await saved.path()),images.get(kind),'download confirms the exact original bytes');
 const downloadSavedMs=await measureSinceInput(page);
 if(concurrent){await concurrent();assert(await dialog.isVisible(),'source and viewer survive arrival');}
 const close=dialog.getByRole('button',{name:'Close',exact:true});
 if(phone){await recordInput(close,'click');await close.tap();}
 else{await close.focus();await recordInput(close,'keydown');await page.keyboard.press('Escape');}
 await dialog.waitFor({state:'hidden'});
 const closeMs=await measureSinceInput(page,true);
 assert(closeMs<1100,'viewer closes within the local feedback bound');
 assert(await thumb.evaluate(b=>b.getRootNode().activeElement===b),'focus returns to the exact image');
 if(metric)console.log('IMAGE_ACTION '+JSON.stringify({...metric,name,openMs,fullImageMs,downloadStartedMs,downloadSavedMs,closeMs}));
}
(async()=>{try{
 await source();console.log('IMAGE_INPUT '+JSON.stringify({canonical:!!process.env.AGENTNET_IMAGE_BUILT,entrySHA256:createHash('sha256').update(bundle).digest('hex'),styleSHA256:createHash('sha256').update(styles).digest('hex')}));await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:resource?['--disable-gpu']:[]});await makeImages();
 const modes=process.env.AGENTNET_IMAGE_MODE?[process.env.AGENTNET_IMAGE_MODE]:latency?['landscapes']:process.env.AGENTNET_IMAGE_COMPARE?['portraits']:resource?['many']:Object.keys(cases);
 for(const history of (latency?[10,100]:[1]))for(const width of (process.env.AGENTNET_IMAGE_WIDTH?[Number(process.env.AGENTNET_IMAGE_WIDTH)]:[1280,390]))for(const mode of modes){
  reset(mode,history);const context=await browser.newContext({viewport:{width,height:844},hasTouch:width===390,acceptDownloads:true,reducedMotion:'reduce'}),page=await context.newPage(),errors=[];let stage='load';
  if(mode==='landscapes'&&!latency&&!process.env.AGENTNET_IMAGE_COMPARE)fixture.preview=Promise.withResolvers();
  page.setDefaultTimeout(resource?10000:5000);
  const metrics=resource?await context.newCDPSession(page):null;
  if(metrics){await metrics.send('Emulation.setCPUThrottlingRate',{rate:6});await metrics.send('Performance.enable');}
  const memory=async()=>{const m=(await metrics.send('Performance.getMetrics')).metrics;const c=await browser.newBrowserCDPSession();try{const {processInfo}=await c.send('SystemInfo.getProcessInfo');let rss=0;for(const p of processInfo){try{const text=fs.readFileSync('/proc/'+p.id+'/status','utf8');rss+=Number(/^VmRSS:\s+(\d+)/m.exec(text)?.[1]||0)*1024;}catch{}}return {jsHeapUsedBytes:m.find(x=>x.name==='JSHeapUsedSize').value,browserProcessesRSSBytes:rss};}finally{await c.detach();}};page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.type()+': '+m.text());});
  await context.addInitScript(phone=>{
   if(phone){window.__agentnetPlatform='android';window.__agentnetAndroid={visible:()=>true,onVisibility:()=>()=>{},legacyDrafts:async()=>({drafts:[]}),acknowledgeLegacyDraft:async()=>({removed:true})};}
   window.imageResources={created:[],revoked:[],observed:new Set()};
   const originalCreate=URL.createObjectURL.bind(URL),originalRevoke=URL.revokeObjectURL.bind(URL);
   URL.createObjectURL=b=>{const url=originalCreate(b);window.imageResources.created.push({url,type:b.type});return url;};
   URL.revokeObjectURL=url=>{window.imageResources.revoked.push(url);return originalRevoke(url);};
   const OriginalObserver=window.IntersectionObserver;window.IntersectionObserver=class extends OriginalObserver{observe(el){window.imageResources.observed.add(el);return super.observe(el);}unobserve(el){window.imageResources.observed.delete(el);return super.unobserve(el);}disconnect(){for(const el of window.imageResources.observed)if(this.__owned?.has(el))window.imageResources.observed.delete(el);return super.disconnect();}__owned=new Set();};
   const observe=window.IntersectionObserver.prototype.observe;window.IntersectionObserver.prototype.observe=function(el){this.__owned.add(el);return observe.call(this,el);};
   window.__agentnetOnBack=fn=>{window.imageBack=fn;return()=>{delete window.imageBack;};};},width===390);
  await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  try{
   const began=performance.now();let initialResource;
   await page.goto(origin+'/?skin='+selected.id+'#conv='+conv+'&workspace=default');
   if(selected.id!=='comic')await page.getByRole('button',{name:'Use this skin',exact:true}).click();
   await page.locator('p').filter({hasText:mode==='portraits'?'Two original phone screenshots':'Original attachments · '+mode}).waitFor();
   if(fixture.preview){
    stage='held original';
    const pending=page.getByRole('button',{name:'Open picture wide-three.png',exact:true});
    await pending.waitFor();
    assert.equal(await pending.locator('img').count(),0,'placeholder while original is held');
    if(width===390)await pending.tap();else await pending.press('Enter');
    assert.equal(await page.getByRole('dialog',{name:'wide-three.png',exact:true}).count(),0,'no viewer before bytes arrive');
    fixture.preview.resolve();
   }
   if(resource){await page.waitForFunction(()=>[...document.querySelector('#skin').shadowRoot.querySelectorAll('img')].some(im=>im.complete&&im.naturalWidth));await page.evaluate(()=>new Promise(r=>setTimeout(r,250)));initialResource={elapsedMs:Math.round(performance.now()-began),reads:fixture.reads.length,...await memory()};}
   const wanted=cases[mode].filter(([,kind])=>kind==='portrait'||kind.startsWith('landscape')).map(([name])=>name);
   for(const name of wanted){const b=page.getByRole('button',{name:'Open picture '+name,exact:true});await b.scrollIntoViewIfNeeded();await b.locator('img').waitFor();await b.locator('img').evaluate(im=>im.decode());}
   if(mode==='failed')await page.getByRole('button',{name:'Download failed.png',exact:true}).waitFor();
   const layout=await imageButtons(page).evaluateAll(buttons=>buttons.map(b=>{const r=b.getBoundingClientRect(),im=b.querySelector('img');return{name:b.getAttribute('aria-label').slice(13),x:r.x,y:r.y,w:r.width,h:r.height,image:im?{w:im.clientWidth,h:im.clientHeight,nw:im.naturalWidth,nh:im.naturalHeight}:null};}));
   assert.deepEqual(layout.map(x=>x.name),wanted,'original image order');for(const b of layout){assert(b.w>0&&b.h>=44&&b.x>=0&&b.x+b.w<=width+1,'image target fits');assert(b.image.w>0&&b.image.h>0);assert(Math.abs(b.image.w/b.image.h-b.image.nw/b.image.nh)<.02,'whole-image preview keeps natural ratio');}
   const group=page.locator('[data-image-group]');const grouped=await group.count();
   const row={mode,width,grouped,rows:new Set(layout.map(x=>Math.round(x.y))).size,previewHeights:layout.map(x=>Math.round(x.h)),groupHeight:grouped?Math.round((await group.boundingBox()).height):null};
   if(process.env.AGENTNET_IMAGE_COMPARE){console.log('IMAGE_COMPARE '+JSON.stringify(row));if(evidence){await imageButtons(page).first().scrollIntoViewIfNeeded();await page.screenshot({path:path.join(evidence,'images-'+width+'-'+mode+'.png')});}if(!latency)continue;}
   if(wanted.length>1){assert.equal(grouped,1);if(mode==='portraits')assert.equal(row.rows,1,'two portraits share one row');assert(layout.every(b=>b.h<=240),'multi-image previews stay bounded');}
   else assert.equal(grouped,0,'single image retains its existing proportional layout');
   if(mode==='landscapes'&&!process.env.AGENTNET_IMAGE_COMPARE){
    assert.equal(row.rows,1,'three landscapes fill one row');
    assert(layout.every(b=>b.h-b.image.h<5),'wide previews have no top and bottom letterboxing');
    const bounds=await group.boundingBox();
    assert(Math.abs(layout.at(-1).x+layout.at(-1).w-bounds.x-bounds.width)<2,'the row fills the attachment width');
    if(width!==390&&!latency){
     const buttons=imageButtons(page);
     await buttons.first().focus();
     for(let i=1;i<wanted.length;i++){
      await page.keyboard.press('Tab');
      assert(await buttons.nth(i).evaluate(b=>b.getRootNode().activeElement===b),'Tab follows attachment order');
     }
    }
   }
   if(mode==='mixed'){
    const item=page.getByRole('button',{name:'Download notes.txt',exact:true});
    const note=await item.evaluate(n=>n.parentElement.getBoundingClientRect().toJSON());
    assert(note.width>layout[0].w*1.5,'mixed document keeps a full row');
    assert(layout[0].y<note.y&&note.bottom<=layout[1].y,'document and images keep exact attachment order');
    assert(layout[1].w>layout[2].w*2&&Math.abs(layout[1].h-layout[2].h)<2,'mixed row follows the two shapes');
   }
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
   if(['landscapes','mixed','single'].includes(mode)){
    stage='exact original actions';
    const release=latency?await holdNewArrival(page):null;
    const syncStarted=release?performance.now():null;
    for(const [i,name] of wanted.entries()){
     const thumb=page.getByRole('button',{name:'Open picture '+name,exact:true});
     const kind=cases[mode].find(([candidate])=>candidate===name)[1];
     const concurrent=i===0&&release?async()=>{
      const delay=450;
      await new Promise(resolve=>setTimeout(resolve,Math.max(0,delay-(performance.now()-syncStarted))));
      await release();
      console.log('IMAGE_SYNC '+JSON.stringify({history,width,heldMs:Math.round(performance.now()-syncStarted)}));
     }:null;
     await verifyOriginal(page,thumb,name,kind,width===390,concurrent,latency?{history,width,phase:i===0?'first/during-sync':'warm/after-sync'}:null);
    }
    if(latency)await verifyOriginal(page,page.getByRole('button',{name:'Open picture wide-one.png',exact:true}),'wide-one.png','landscape',width===390,null,{history,width,phase:'reopen/after-sync'});
    assert.deepEqual(fixture.reads.map(r=>r.index).sort((a,b)=>a-b),fixture.files.filter(f=>wanted.includes(f.name)).map(f=>f.index),'each image fetched once');
    if(mode==='mixed'){
     const download=page.waitForEvent('download');
     const button=page.getByRole('button',{name:'Download notes.txt',exact:true});
     if(width===390)await button.tap();else await button.press('Enter');
     const saved=await download;
     assert.equal(saved.suggestedFilename(),'notes.txt');
     assert.deepEqual(fs.readFileSync(await saved.path()),images.get('text'));
    }
   }
   if(mode==='portraits'){
    stage='full-image actions';assert.equal(await page.getByRole('button',{name:'Save a readable copy to Google Drive',exact:true}).count(),0,'group has no repeated inline save links');
    for(const [n,name]of wanted.entries()){
     const thumb=page.getByRole('button',{name:'Open picture '+name,exact:true});await thumb.scrollIntoViewIfNeeded();await thumb.focus();await page.keyboard.press('Enter');const dialog=page.getByRole('dialog',{name,exact:true});await dialog.waitFor();const im=dialog.getByRole('img',{name,exact:true});await im.evaluate(i=>i.decode());assert.deepEqual(await im.evaluate(i=>[i.naturalWidth,i.naturalHeight]),[1080,2400]);assert.equal(await im.getAttribute('src'),await thumb.locator('img').getAttribute('src'));
     const region=dialog.getByRole('region');await region.focus();await region.press('+');const zoom=Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''));await region.press('+');assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''))>zoom);await dialog.getByRole('button',{name:'100%',exact:true}).click();const box=await region.boundingBox();await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.down();await page.mouse.move(box.x+box.width/2+40,box.y+box.height/2+35);await page.mouse.up();assert.notEqual(await im.evaluate(i=>i.style.transform),'translate(0px, 0px)');await dialog.getByRole('button',{name:'Fit',exact:true}).click();
     if(width===390){const cdp=await context.newCDPSession(page),touches=d=>[{x:box.x+box.width/2-d/2,y:box.y+box.height/2},{x:box.x+box.width/2+d/2,y:box.y+box.height/2}];const before=Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''));await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:touches(60)});await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:touches(120)});await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''))>before,'pinch keeps working');}
     const download=page.waitForEvent('download');await dialog.getByRole('button',{name:'Download',exact:true}).click();const saved=await download;assert.equal(saved.suggestedFilename(),name);assert.deepEqual(fs.readFileSync(await saved.path()),images.get('portrait'),'original bytes preserved');
     await dialog.getByRole('button',{name:'Save a readable copy to Google Drive',exact:true}).click();const confirm=page.getByRole('alertdialog',{name:'Save to Google Drive?',exact:true});await confirm.waitFor();await confirm.getByRole('button',{name:'Save to Google Drive',exact:true}).click();await confirm.waitFor({state:'hidden'});assert(await dialog.isVisible());assert.deepEqual(fixture.posts.filter(x=>x.api==='/api/drive').at(-1)?.body,{conv,action:'save-attachment',dir:'in',message,index:2+n*3,confirm_outside_e2ee:true});
     await dialog.getByRole('button',{name:'Close',exact:true}).focus();await page.keyboard.press('Tab');assert(await dialog.evaluate(d=>d.contains(d.getRootNode().activeElement)),'Tab remains in viewer');
     if(width===390)assert.equal(await page.evaluate(()=>window.imageBack()),true,'Back closes viewer');else await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});assert(await thumb.evaluate(b=>b.getRootNode().activeElement===b),'focus returns to exact image');
    }
   }
   if(mode==='failed'){const caption=group.locator('span.text-muted[title="failed.png"]');assert.equal(await caption.count(),1,'failed image retains readable filename caption');assert(await caption.isVisible());assert(await caption.evaluate(n=>n.scrollWidth<=n.clientWidth),'failed filename is readable at this viewport');assert.equal(await page.getByRole('button',{name:'Save a readable copy to Google Drive',exact:true}).count(),1,'failed image keeps file actions');}
   assert(fixture.reads.every(r=>r.id===message&&r.dir===fixture.dir&&fixture.files.some(f=>f.index===r.index)),'exact sparse file index and direction');assert(!fixture.posts.some(r=>/\/api\/(send|dm\/send|request\/followup)$/.test(r.api)));assert.deepEqual(errors,[]);
   if(evidence){await imageButtons(page).first().scrollIntoViewIfNeeded();await page.screenshot({path:path.join(evidence,'images-'+width+'-'+mode+'.png')});}
   if(resource){const after=await memory();const totalReads=fixture.reads.length;const resources=await page.evaluate(async()=>{const module=await import('/assets/skins/comic/entry.mjs');const root=document.querySelector('#skin').shadowRoot.querySelector('.an-root');await module.unmount(root);const r=window.imageResources;return {created:r.created.filter(x=>x.type.startsWith('image/')).map(x=>x.url),revoked:r.revoked,observers:r.observed.size,remainingImages:root.querySelectorAll('img').length};});assert.equal(resources.observers,0,'unmount disconnects picture observers');assert.equal(resources.remainingImages,0);assert(resources.created.every(url=>resources.revoked.includes(url)),'unmount revokes all original image object URLs');assert.equal(totalReads,wanted.length,'each original fetched once after explicit scrolling');console.log('IMAGE_RESOURCE '+JSON.stringify({width,softwareRendering:true,cpuThrottle:6,originalPixels:[1080*2400,2400*1200],originalCount:wanted.length,initial:initialResource,afterAllOpened:after,totalReads,cleanup:{created:resources.created.length,revoked:resources.created.filter(url=>resources.revoked.includes(url)).length,observers:resources.observers}}));}
   console.log(JSON.stringify({...row,skin:selected.id,images:wanted.length,wholeImage:true,order:true,result:'PASS'}));
  }catch(e){console.error('IMAGE_FAILURE '+JSON.stringify({mode,width,stage,errors,reads:fixture.reads,posts:fixture.posts,paragraphs:await page.locator('p').evaluateAll(nodes=>nodes.map(n=>({text:n.textContent,html:n.outerHTML,rect:n.getBoundingClientRect().toJSON(),visibility:getComputedStyle(n).visibility}))) }));if(evidence)await page.screenshot({path:path.join(evidence,'images-'+width+'-'+mode+'-failure.png')});throw e;
  }finally{fixture.preview?.resolve();fixture.sync?.release.resolve();await context.close();for(const res of fixture.events)res.end();}
 }
}finally{await browser?.close();server.closeAllConnections();await new Promise(r=>server.close(r));fs.rmSync(temporary,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exitCode=1;});
