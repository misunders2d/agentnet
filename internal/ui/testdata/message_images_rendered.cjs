// Production loader + source-built Comic; fictional image bytes and file API.
// CSS/bundle output stays in /tmp until root rebuilds the integrated package.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http'),os=require('node:os'),{execFileSync}=require('node:child_process');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const {createHash}=require('node:crypto');
const resource=!!process.env.AGENTNET_IMAGE_RESOURCE;
const assets=path.resolve(__dirname,'../static'),web=path.resolve(__dirname,'../web'),evidence=process.env.AGENTNET_SCREENSHOTS;
if(evidence)fs.mkdirSync(evidence,{recursive:true});
const conv='a'.repeat(64),other='b'.repeat(64),message='c'.repeat(32),handle='d'.repeat(32),at='2026-10-20T12:00:00Z';
const me={person:'1'.repeat(32),label:'Tester',address:'tester/laptop',state:'self'},peer={person:'2'.repeat(32),label:'Casey',address:'casey/desktop',state:'pinned',devices:[]};
let bundle,styles,browser,fixture;const images=new Map(),temporary=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-message-images-'));
const cases={portraits:[['phone-one.png','portrait'],['phone-two.png','portrait']],single:[['single-phone.png','portrait']],landscapes:[['wide-one.png','landscape'],['wide-two.png','landscape']],many:Array.from({length:7},(_,i)=>['ordered-'+(i+1)+'.png',i%2?'landscape':'portrait']),mixed:[['first.png','portrait'],['notes.txt','text'],['second.png','landscape'],['third.png','portrait']],failed:[['first.png','portrait'],['failed.png','invalid'],['last.png','landscape']]};
function reset(mode){
 const files=cases[mode].map(([name,kind],i)=>({index:2+i*3,name,size:images.get(kind).length,openable:true}));
 const row={id:message,lid:message,dir:'in',from:peer.address,kind:'message',body:mode==='portraits'?'Two original phone screenshots':'Original attachments · '+mode,at,attachments:files,actions:[],reactions:[]};
 const dm=(id,messages)=>({id,created:at,kind:'dm',peer,mine:true,role:'member',members:[me,peer],agents:[],guests:[],frozen:'',messages,topics:[]});
 fixture={mode,files,reads:[],posts:[],events:new Set(),seq:1,views:{[conv]:dm(conv,[row]),[other]:dm(other,[{...row,id:'e'.repeat(32),lid:'e'.repeat(32),body:'Other chat context',attachments:[]}])}};
 fixture.overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:{max_count:8,max_file:10485760,max_message:20971520},controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[conv,other].map(id=>({id,created:at,peer,kind:'dm',role:'member',count:1,title:id===conv?'Original images':'Other chat',last:id===conv?row.body:'Other chat context',last_at:at,unread:0,held:0,waiting:0,guests:0})),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
}
const json=(res,value)=>{res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const server=http.createServer(async(req,res)=>{
 const u=new URL(req.url,'http://127.0.0.1'),p=u.pathname;
 if(p==='/favicon.ico'||/^\/assets\/icon-(192|512)\.png$/.test(p)){res.writeHead(204).end();return;}
 if(p==='/'){res.setHeader('Content-Type','text/html');res.end(fs.readFileSync(path.join(assets,'index.html')));return;}
 if(p==='/assets/skins/index.json'){json(res,[{...JSON.parse(fs.readFileSync(path.join(assets,'skins/comic/skin.json'))),digest:'0'.repeat(64)}]);return;}
 if(p==='/assets/skins/comic/entry.mjs'){res.setHeader('Content-Type','text/javascript');res.end(bundle);return;}
 if(p==='/assets/skins/comic/style.css'){res.setHeader('Content-Type','text/css');res.end(styles);return;}
 if(p.startsWith('/assets/')){const file=path.resolve(assets,'.'+p.slice(7));if(file.startsWith(assets+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',/\.(mjs|js)$/.test(file)?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 if(p==='/api/workspaces'){json(res,[{id:'default',handle,state:'enrolled',name:'Fictional image workspace',address:me.address,realm:'3'.repeat(32),endpoint:'https://fixture.invalid'}]);return;}
 if(p.endsWith('/events')){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache'});res.write(': ready\n\n');fixture.events.add(res);req.on('close',()=>fixture.events.delete(res));return;}
 const api=p.replace(/^\/workspaces\/default\/[a-f0-9]{32}/,'');
 if(api.startsWith('/api/')){
  const chunks=[];for await(const chunk of req)chunks.push(chunk);const raw=Buffer.concat(chunks),body=raw.length&&req.headers['content-type']==='application/json'?JSON.parse(raw):undefined;
  if(body!==undefined)fixture.posts.push({api,body});
  if(api==='/api/overview'){json(res,fixture.overview);return;}
  if(api==='/api/dm'){json(res,fixture.views[u.searchParams.get('id')]);return;}
  const file=/^\/api\/files\/([^/]+)\/(\d+)$/.exec(api);
  if(file){const index=Number(file[2]),f=fixture.files.find(f=>f.index===index);fixture.reads.push({id:file[1],index,dir:u.searchParams.get('dir')});assert.equal(file[1],message);assert.equal(u.searchParams.get('dir'),'in');assert(f);const kind=cases[fixture.mode][fixture.files.indexOf(f)][1];res.writeHead(200,{'Content-Type':'application/octet-stream'});res.end(images.get(kind));return;}
  if(api==='/api/agents'){json(res,{agents:[],sessions:[],local:false});return;}
  if(api==='/api/groups/invitations'){json(res,[]);return;}
  if(api==='/api/typing/status'){json(res,{send:false,scopes:[]});return;}
  if(api==='/api/topics'){json(res,{topics:[],matched:0});return;}
  if(api==='/api/drive'&&body?.action==='save-attachment'){json(res,{note:'Saved fixture copy.'});return;}
  json(res,{});return;
 }
 res.writeHead(404).end('No image fixture route');
});
async function source(){
 if(process.env.AGENTNET_IMAGE_BUILT){assert(!process.env.AGENTNET_IMAGE_BASELINE,'built and baseline modes are exclusive');bundle=fs.readFileSync(path.join(assets,'skins/comic/entry.mjs'),'utf8');styles=fs.readFileSync(path.join(assets,'skins/comic/style.css'));return;}
 const replacements={'optimistic.mjs':'../static/optimistic.mjs','pictures.mjs':'../static/pictures.mjs','person-topics.mjs':'../skins/shared/person-topics.mjs','reaction-preferences.mjs':'../skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'../skins/shared/collective-mentions.mjs','drive-space.mjs':'../skins/shared/drivespace.mjs','drive-setup.mjs':'../skins/shared/drivespace-setup.mjs'};
 const built=await require('../web/node_modules/esbuild').build({entryPoints:[path.join(web,'src/main.tsx')],bundle:true,write:false,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',minify:true,define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'source-inputs',setup(build){build.onResolve({filter:/\.mjs$/},args=>{const name=path.basename(args.path);if(replacements[name]&&!fs.existsSync(path.resolve(args.resolveDir,args.path)))return{path:path.resolve(web,replacements[name])};});if(process.env.AGENTNET_IMAGE_BASELINE)build.onLoad({filter:/\/features\/Message\.files\.tsx$/},()=>({contents:fs.readFileSync(process.env.AGENTNET_IMAGE_BASELINE,'utf8'),loader:'tsx',resolveDir:path.join(web,'src/features')}));}}]});bundle=built.outputFiles[0].text;
 const css=path.join(temporary,'style.css');execFileSync(path.join(web,'node_modules/.bin/tailwindcss'),['-i','src/styles.css','-o',css,'--minify'],{cwd:web,stdio:'pipe'});styles=fs.readFileSync(css);
}
async function makeImages(){const page=await browser.newPage();const data=await page.evaluate(()=>{
 const result={};for(const [kind,w,h]of [['portrait',1080,2400],['landscape',2400,1200]]){const c=document.createElement('canvas');c.width=w;c.height=h;const x=c.getContext('2d');x.fillStyle='#161b20';x.fillRect(0,0,w,h);x.fillStyle='#edf2f4';x.font='bold 46px sans-serif';x.fillText('13:50 · Harbor phone',60,100);x.fillStyle='#d5dfdf';x.font='34px sans-serif';x.fillText('Original screenshot · '+kind,60,164);for(let n=0;n<7;n++){const y=220+n*285;if(y+230>h)break;x.fillStyle=n%2?'#29343c':'#222c33';x.beginPath();x.roundRect(35,y,w-70,240,28);x.fill();x.fillStyle='#83c9b4';x.beginPath();x.roundRect(65,y+35,62,62,14);x.fill();x.fillStyle='#eaf1f2';x.font='bold 34px sans-serif';x.fillText(['Notes','Calendar','Harbor','Tasks'][n%4]+' · '+(n+1),150,y+78);x.fillStyle='#c1cbcf';x.font='32px sans-serif';x.fillText('Casey · Invoice review',65,y+145);x.fillText('The original document is ready to read.',65,y+194);}result[kind]=c.toDataURL('image/png').split(',')[1];}return result;
 });await page.close();for(const[k,v]of Object.entries(data))images.set(k,Buffer.from(v,'base64'));images.set('text',Buffer.from('Original text attachment\n'));images.set('invalid',Buffer.from('This is not an image.'));}
function imageButtons(page){return page.getByRole('button',{name:/^Open picture /});}
(async()=>{try{
 await source();console.log('IMAGE_INPUT '+JSON.stringify({canonical:!!process.env.AGENTNET_IMAGE_BUILT,entrySHA256:createHash('sha256').update(bundle).digest('hex'),styleSHA256:createHash('sha256').update(styles).digest('hex')}));await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:resource?['--disable-gpu']:[]});await makeImages();
 const modes=process.env.AGENTNET_IMAGE_MODE?[process.env.AGENTNET_IMAGE_MODE]:process.env.AGENTNET_IMAGE_COMPARE?['portraits']:resource?['many']:Object.keys(cases);
 for(const width of (process.env.AGENTNET_IMAGE_WIDTH?[Number(process.env.AGENTNET_IMAGE_WIDTH)]:[1280,390]))for(const mode of modes){
  reset(mode);const context=await browser.newContext({viewport:{width,height:844},hasTouch:width===390,acceptDownloads:true,reducedMotion:'reduce'}),page=await context.newPage(),errors=[];let stage='load';
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
   await page.goto(origin+'/#conv='+conv+'&workspace=default');await page.locator('p').filter({hasText:mode==='portraits'?'Two original phone screenshots':'Original attachments · '+mode}).waitFor();
   if(resource){await page.waitForFunction(()=>[...document.querySelector('#skin').shadowRoot.querySelectorAll('img')].some(im=>im.complete&&im.naturalWidth));await page.evaluate(()=>new Promise(r=>setTimeout(r,250)));initialResource={elapsedMs:Math.round(performance.now()-began),reads:fixture.reads.length,...await memory()};}
   const wanted=cases[mode].filter(([,kind])=>['portrait','landscape'].includes(kind)).map(([name])=>name);
   for(const name of wanted){const b=page.getByRole('button',{name:'Open picture '+name,exact:true});await b.scrollIntoViewIfNeeded();await b.locator('img').waitFor();await b.locator('img').evaluate(im=>im.decode());}
   if(mode==='failed')await page.getByRole('button',{name:'Download failed.png',exact:true}).waitFor();
   const layout=await imageButtons(page).evaluateAll(buttons=>buttons.map(b=>{const r=b.getBoundingClientRect(),im=b.querySelector('img');return{name:b.getAttribute('aria-label').slice(13),x:r.x,y:r.y,w:r.width,h:r.height,image:im?{w:im.clientWidth,h:im.clientHeight,nw:im.naturalWidth,nh:im.naturalHeight}:null};}));
   assert.deepEqual(layout.map(x=>x.name),wanted,'original image order');for(const b of layout){assert(b.w>0&&b.h>=44&&b.x>=0&&b.x+b.w<=width+1,'image target fits');assert(b.image.w>0&&b.image.h>0);assert(Math.abs(b.image.w/b.image.h-b.image.nw/b.image.nh)<.02,'whole-image preview keeps natural ratio');}
   const group=page.locator('[data-image-group]');const grouped=await group.count();
   if(process.env.AGENTNET_IMAGE_COMPARE){const row={mode,width,grouped,sideBySide:Math.abs(layout[0].y-layout[1].y)<2,previewHeights:layout.map(x=>Math.round(x.h)),groupHeight:grouped?Math.round((await group.boundingBox()).height):null};console.log('IMAGE_COMPARE '+JSON.stringify(row));if(!process.env.AGENTNET_IMAGE_BASELINE)assert(row.sideBySide);if(evidence){await imageButtons(page).first().scrollIntoViewIfNeeded();await page.screenshot({path:path.join(evidence,'images-'+width+'.png')});}continue;}
   if(wanted.length>1){assert.equal(grouped,1);if(mode==='portraits'||mode==='landscapes')assert(Math.abs(layout[0].y-layout[1].y)<2,'two images share one row');assert(layout.every(b=>b.h<=240),'multi-image previews stay bounded');}
   else assert.equal(grouped,0,'single image retains its existing proportional layout');
   if(mode==='mixed'){const item=page.getByRole('button',{name:'Download notes.txt',exact:true});const r=await item.evaluate(n=>n.parentElement.getBoundingClientRect().width);assert(r>layout[0].w*1.5,'mixed document keeps a full row');}
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
   if(mode==='portraits'){
    stage='full-image actions';assert.equal(await page.getByRole('button',{name:'Save to project space',exact:true}).count(),0,'group has no repeated inline save links');
    for(const [n,name]of wanted.entries()){
     const thumb=page.getByRole('button',{name:'Open picture '+name,exact:true});await thumb.scrollIntoViewIfNeeded();await thumb.focus();await page.keyboard.press('Enter');const dialog=page.getByRole('dialog',{name,exact:true});await dialog.waitFor();const im=dialog.getByRole('img',{name,exact:true});await im.evaluate(i=>i.decode());assert.deepEqual(await im.evaluate(i=>[i.naturalWidth,i.naturalHeight]),[1080,2400]);assert.equal(await im.getAttribute('src'),await thumb.locator('img').getAttribute('src'));
     const region=dialog.getByRole('region');await region.focus();await region.press('+');const zoom=Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''));await region.press('+');assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''))>zoom);await dialog.getByRole('button',{name:'100%',exact:true}).click();const box=await region.boundingBox();await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.down();await page.mouse.move(box.x+box.width/2+40,box.y+box.height/2+35);await page.mouse.up();assert.notEqual(await im.evaluate(i=>i.style.transform),'translate(0px, 0px)');await dialog.getByRole('button',{name:'Fit',exact:true}).click();
     if(width===390){const cdp=await context.newCDPSession(page),touches=d=>[{x:box.x+box.width/2-d/2,y:box.y+box.height/2},{x:box.x+box.width/2+d/2,y:box.y+box.height/2}];const before=Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''));await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:touches(60)});await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:touches(120)});await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''))>before,'pinch keeps working');}
     const download=page.waitForEvent('download');await dialog.getByRole('button',{name:'Download',exact:true}).click();const saved=await download;assert.equal(saved.suggestedFilename(),name);assert.deepEqual(fs.readFileSync(await saved.path()),images.get('portrait'),'original bytes preserved');
     await dialog.getByRole('button',{name:'Save to project space',exact:true}).click();const confirm=page.getByRole('alertdialog',{name:'Save to Google Drive?',exact:true});await confirm.waitFor();await confirm.getByRole('button',{name:'Save to Google Drive',exact:true}).click();await confirm.waitFor({state:'hidden'});assert(await dialog.isVisible());assert.deepEqual(fixture.posts.filter(x=>x.api==='/api/drive').at(-1)?.body,{conv,action:'save-attachment',dir:'in',message,index:2+n*3,confirm_outside_e2ee:true});
     await dialog.getByRole('button',{name:'Close',exact:true}).focus();await page.keyboard.press('Tab');assert(await dialog.evaluate(d=>d.contains(d.getRootNode().activeElement)),'Tab remains in viewer');
     if(width===390)assert.equal(await page.evaluate(()=>window.imageBack()),true,'Back closes viewer');else await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});assert(await thumb.evaluate(b=>b.getRootNode().activeElement===b),'focus returns to exact image');
    }
   }
   if(mode==='failed'){const caption=group.locator('span.text-muted[title="failed.png"]');assert.equal(await caption.count(),1,'failed image retains readable filename caption');assert(await caption.isVisible());assert(await caption.evaluate(n=>n.scrollWidth<=n.clientWidth),'failed filename is readable at this viewport');assert.equal(await page.getByRole('button',{name:'Save to project space',exact:true}).count(),1,'failed image keeps file actions');}
   assert(fixture.reads.every(r=>r.id===message&&r.dir==='in'&&fixture.files.some(f=>f.index===r.index)),'exact sparse file index and direction');assert(!fixture.posts.some(r=>/\/api\/(send|dm\/send|request\/followup)$/.test(r.api)));assert.deepEqual(errors,[]);
   if(evidence){await imageButtons(page).first().scrollIntoViewIfNeeded();await page.screenshot({path:path.join(evidence,'images-'+width+'-'+mode+'.png')});}
   if(resource){const after=await memory();const totalReads=fixture.reads.length;const resources=await page.evaluate(async()=>{const module=await import('/assets/skins/comic/entry.mjs');const root=document.querySelector('#skin').shadowRoot.querySelector('.an-root');await module.unmount(root);const r=window.imageResources;return {created:r.created.filter(x=>x.type.startsWith('image/')).map(x=>x.url),revoked:r.revoked,observers:r.observed.size,remainingImages:root.querySelectorAll('img').length};});assert.equal(resources.observers,0,'unmount disconnects picture observers');assert.equal(resources.remainingImages,0);assert(resources.created.every(url=>resources.revoked.includes(url)),'unmount revokes all original image object URLs');assert.equal(totalReads,wanted.length,'each original fetched once after explicit scrolling');console.log('IMAGE_RESOURCE '+JSON.stringify({width,softwareRendering:true,cpuThrottle:6,originalPixels:[1080*2400,2400*1200],originalCount:wanted.length,initial:initialResource,afterAllOpened:after,totalReads,cleanup:{created:resources.created.length,revoked:resources.created.filter(url=>resources.revoked.includes(url)).length,observers:resources.observers}}));}
   console.log(JSON.stringify({mode,width,images:wanted.length,grouped,heights:layout.map(x=>Math.round(x.h)),wholeImage:true,order:true,result:'PASS'}));
  }catch(e){console.error('IMAGE_FAILURE '+JSON.stringify({mode,width,stage,errors,reads:fixture.reads,posts:fixture.posts,paragraphs:await page.locator('p').evaluateAll(nodes=>nodes.map(n=>({text:n.textContent,html:n.outerHTML,rect:n.getBoundingClientRect().toJSON(),visibility:getComputedStyle(n).visibility}))) }));if(evidence)await page.screenshot({path:path.join(evidence,'images-'+width+'-'+mode+'-failure.png')});throw e;
  }finally{await context.close();for(const res of fixture.events)res.end();}
 }
}finally{await browser?.close();server.closeAllConnections();await new Promise(r=>server.close(r));fs.rmSync(temporary,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exitCode=1;});
