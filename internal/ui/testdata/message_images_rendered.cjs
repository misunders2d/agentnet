// MEL-856: Telegram's grouped media in Comic. Production loader + source-built
// (or, with AGENTNET_IMAGE_BUILT, the packaged) skin; fictional image, video
// and file bytes behind a fictional file API. CSS/bundle output stays in a
// temporary directory.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http'),os=require('node:os'),{execFileSync}=require('node:child_process');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const {createHash}=require('node:crypto');
const {stripTypeScriptTypes}=require('node:module');
const resource=!!process.env.AGENTNET_IMAGE_RESOURCE;
const assets=path.resolve(__dirname,'../static'),web=path.resolve(__dirname,'../web'),evidence=process.env.AGENTNET_SCREENSHOTS;
const comic=path.join(assets,'skins/comic');
const skin=path.resolve(process.env.AGENTNET_IMAGE_PACKAGE||comic);
const manifest=dir=>({...JSON.parse(fs.readFileSync(path.join(dir,'skin.json'))),digest:'0'.repeat(64)});
const selected=manifest(skin);
const skinRoute='/assets/skins/'+selected.id+'/';
const latency=!!process.env.AGENTNET_IMAGE_LATENCY;
if(evidence)fs.mkdirSync(evidence,{recursive:true});
const conv='a'.repeat(64),other='b'.repeat(64),message='c'.repeat(32),handle='d'.repeat(32),at='2026-11-27T12:00:00Z';
const me={person:'1'.repeat(32),label:'Tester',address:'tester/laptop',state:'self'},peer={person:'2'.repeat(32),label:'Casey',address:'casey/desktop',state:'pinned',devices:[]};
let bundle,styles,browser,fixture,groups;const images=new Map(),temporary=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-message-images-'));
// Natural sizes of the fixture pictures (the album lays out from these).
const shapes={portrait:[1080,2400],landscape:[2400,1200],'landscape-2':[2400,1200],'landscape-3':[2400,1200],square:[1200,1200],panorama:[3600,1000]};
const isPicture=kind=>kind in shapes,isVideo=kind=>kind==='video'||kind==='large-video';
const albumKinds=['portrait','landscape','landscape-2','portrait','square','landscape-3','portrait','landscape','panorama','portrait'];
const album=n=>albumKinds.slice(0,n).map((kind,i)=>['photo-'+(i+1)+'.png',kind]);
const cases={
 single:[['single-phone.png','portrait']],
 ...Object.fromEntries([2,3,4,5,6,7,8,9,10].map(n=>['album'+n,album(n)])),
 panorama:[['wide-view.png','panorama'],['phone.png','portrait']],
 video:[['before.png','portrait'],['clip.webm','video'],['large.webm','large-video'],['after.png','landscape']],
 docs:[['report.pdf','pdf'],['notes.txt','text'],['archive.zip','zip'],['data.csv','csv']],
 docs8:Array.from({length:8},(_,i)=>['file-'+(i+1)+(['.pdf','.txt','.zip','.csv'][i%4]),['pdf','text','zip','csv'][i%4]]),
 audio:[['song.mp3','mp3'],['voice.m4a','m4a'],['take.wav','wav']],
 over10:Array.from({length:12},(_,i)=>['many-'+(i+1)+'.png',i%2?'landscape':'portrait']),
 mixed:[['first.png','portrait'],['second.png','landscape'],['plan.pdf','pdf'],['notes.txt','text'],['song.mp3','mp3'],['third.png','portrait'],['fourth.png','landscape']],
 failed:[['first.png','portrait'],['failed.png','invalid'],['last.png','landscape']],
 pending:[],
};
// Expected shape of each case: groups in order as [kind, names].
function expectedGroups(mode){
 const list=cases[mode].map(([name,kind])=>({name,kind:kind==='invalid'?'file':isPicture(kind)||isVideo(kind)?'media':/^(mp3|m4a|wav)$/.test(kind)?'music':'file'}));
 return groups.groupRanges(list.map(x=>x.kind)).map(g=>({kind:g.kind,album:g.album,names:list.slice(g.from,g.till).map(x=>x.name),kinds:cases[mode].slice(g.from,g.till).map(([,k])=>k)}));
}
const sizeOf=kind=>kind==='large-video'?16*1024*1024+1:images.get(kind).length;
function reset(mode, history){
 const files=cases[mode].map(([name,kind],i)=>({index:2+i*3,name,size:sizeOf(kind),openable:true}));
 const dir=mode==='docs'||mode==='album4'?'out':'in';
 const caption=mode==='pending'?'':'Caption · '+mode;
 const row={id:message,lid:message,dir,from:dir==='out'?me.address:peer.address,kind:'message',body:caption,at,attachments:files,actions:[],reactions:[]};
 const dm=(id,messages)=>({id,created:at,kind:'dm',peer,mine:true,role:'member',members:[me,peer],agents:[],guests:[],frozen:'',messages,topics:[],can_send:true});
 const earlier=Array.from({length:Math.max(0,history-1)},(_,i)=>{
  const id=(i+1).toString(16).padStart(32,'0');
  return {...row,id,lid:id,body:'Retained fixture message '+(i+1),attachments:[]};
 });
 const messages=mode==='pending'?[{...row,body:'Before the album',attachments:[]}]:[...earlier,row];
 fixture={mode,dir,files,reads:[],posts:[],uploads:0,sends:[],events:new Set(),seq:1,views:{[conv]:dm(conv,messages),[other]:dm(other,[{...row,id:'e'.repeat(32),lid:'e'.repeat(32),body:'Other chat context',attachments:[]}])}};
 fixture.overview={version:'fictional',seq:1,topic_list:true,me:{address:me.address,fingerprint:'own'},person:me,persons:true,agents:true,files:{max_count:8,max_file:10485760,max_message:20971520},controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:[],dms:[conv,other].map(id=>({id,created:at,peer,kind:'dm',role:'member',count:id===conv?messages.length:1,title:id===conv?'Grouped media':'Other chat',last:id===conv?messages.at(-1).body:'Other chat context',last_at:at,unread:0,held:0,waiting:0,guests:0})),directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[],needs_you:[],held:[],agent_devices:[]};
}
const json=(res,value)=>{res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
const server=http.createServer(async(req,res)=>{
 const u=new URL(req.url,'http://127.0.0.1'),p=u.pathname;
 if(p==='/favicon.ico'||/^\/assets\/icon-(192|512)\.png$/.test(p)){res.writeHead(204).end();return;}
 if(p==='/'){res.setHeader('Content-Type','text/html');res.end(fs.readFileSync(path.join(assets,'index.html')));return;}
 if(p==='/assets/skins/index.json'){json(res,selected.id==='comic'?[selected]:[manifest(comic),selected]);return;}
 if(p===skinRoute+'entry.mjs'){res.setHeader('Content-Type','text/javascript');res.end(bundle);return;}
 if(p===skinRoute+'style.css'){res.setHeader('Content-Type','text/css');res.end(styles);return;}
 if(p.startsWith('/assets/')){const packageFile=p.startsWith(skinRoute);let base=packageFile?skin:assets,file=path.resolve(base,p.slice(packageFile?skinRoute.length:'/assets/'.length));if(packageFile&&!fs.existsSync(file)){base=comic;file=path.resolve(comic,p.slice(skinRoute.length));}/* Office omits files equal to Comic's; the host serves Comic's copy (static/skins.go builtinSkin). */if(file.startsWith(base+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',/\.(mjs|js)$/.test(file)?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 if(p==='/api/workspaces'){json(res,[{id:'default',handle,state:'enrolled',name:'Fictional media workspace',address:me.address,realm:'3'.repeat(32),endpoint:'https://fixture.invalid'}]);return;}
 if(p.endsWith('/events')){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache'});res.write(': ready\n\n');fixture.events.add(res);req.on('close',()=>fixture.events.delete(res));return;}
 const api=p.replace(/^\/workspaces\/default\/[a-f0-9]{32}/,'');
 if(api.startsWith('/api/')){
  const chunks=[];for await(const chunk of req)chunks.push(chunk);const raw=Buffer.concat(chunks),body=raw.length&&req.headers['content-type']==='application/json'?JSON.parse(raw):undefined;
  if(body!==undefined)fixture.posts.push({api,body});
  if(api==='/api/overview'){json(res,fixture.overview);return;}
  if(api==='/api/dm'){const id=u.searchParams.get('id');if(id===conv&&fixture.sync){fixture.sync.started.resolve();await fixture.sync.release.promise;}json(res,fixture.views[id]);return;}
  if(api==='/api/upload'){json(res,{id:'upload-'+(++fixture.uploads)});return;}
  if(api==='/api/dm/send'){const held=Promise.withResolvers();fixture.sends.push({body,release:held.resolve});await held.promise;json(res,{id:body?.id||'f'.repeat(32),state:'queued'});return;}
  const file=/^\/api\/files\/([^/]+)\/(\d+)$/.exec(api);
  if(file){const index=Number(file[2]),f=fixture.files.find(f=>f.index===index);fixture.reads.push({id:file[1],index,dir:u.searchParams.get('dir')});assert.equal(file[1],message);assert.equal(u.searchParams.get('dir'),fixture.dir);assert(f);if(fixture.hold?.index===index)await fixture.hold.promise;const kind=cases[fixture.mode][fixture.files.indexOf(f)][1];assert.notEqual(kind,'large-video','manifest gate: a video over 16 MiB is never read');res.writeHead(200,{'Content-Type':'application/octet-stream'});res.end(images.get(kind));return;}
  if(api==='/api/agents'){json(res,{agents:[],sessions:[],local:false});return;}
  if(api==='/api/groups/invitations'){json(res,[]);return;}
  if(api==='/api/typing/status'){json(res,{send:false,scopes:[]});return;}
  if(api==='/api/topics'){json(res,{topics:[],matched:0});return;}
  if(api==='/api/drive'&&!body){json(res,{enabled:true,configured:true,connected:true,space:{name:'Fixture space'}});return;}
  if(api==='/api/drive'&&body?.action==='save-attachment'){json(res,{note:'Saved fixture copy.'});return;}
  json(res,{});return;
 }
 res.writeHead(404).end('No media fixture route');
});
async function source(){
 groups=await import('data:text/javascript,'+encodeURIComponent(stripTypeScriptTypes(fs.readFileSync(path.join(web,'src/features/Message.groups.ts'),'utf8'))));
 if(process.env.AGENTNET_IMAGE_BUILT){bundle=fs.readFileSync(path.join(skin,'entry.mjs'),'utf8');styles=fs.readFileSync(path.join(skin,'style.css'));return;}
 const replacements={'optimistic.mjs':'../static/optimistic.mjs','pictures.mjs':'../static/pictures.mjs','person-topics.mjs':'../skins/shared/person-topics.mjs','reaction-preferences.mjs':'../skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'../skins/shared/collective-mentions.mjs','drive-space.mjs':'../skins/shared/drivespace.mjs','drive-setup.mjs':'../skins/shared/drivespace-setup.mjs'};
 const built=await require('../web/node_modules/esbuild').build({entryPoints:[path.join(web,'src/main.tsx')],bundle:true,write:false,format:'esm',platform:'browser',target:'es2022',jsx:'automatic',minify:true,define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'source-inputs',setup(build){build.onResolve({filter:/\.mjs$/},args=>{const name=path.basename(args.path);if(replacements[name]&&!fs.existsSync(path.resolve(args.resolveDir,args.path)))return{path:path.resolve(web,replacements[name])};});}}]});bundle=built.outputFiles[0].text;
 const css=path.join(temporary,'style.css');execFileSync(path.join(web,'node_modules/.bin/tailwindcss'),['-i','src/styles.css','-o',css,'--minify'],{cwd:web,stdio:'pipe'});styles=fs.readFileSync(css);
}
async function makeMedia(){const page=await browser.newPage();const data=await page.evaluate(async shapes=>{
 const result={};const colors=['#161b20','#224346','#51394e','#554631','#2d3d22','#3b2a4d'];let n=0;
 for(const [kind,[w,h]] of Object.entries(shapes)){const c=document.createElement('canvas');c.width=w;c.height=h;const x=c.getContext('2d');x.fillStyle=colors[n++%colors.length];x.fillRect(0,0,w,h);x.fillStyle='#edf2f4';x.font='bold 64px sans-serif';x.fillText('Harbor · '+kind,60,120);x.strokeStyle='#83c9b4';x.lineWidth=24;x.strokeRect(12,12,w-24,h-24);x.beginPath();x.arc(w/2,h/2,Math.min(w,h)/4,0,7);x.stroke();result[kind]=c.toDataURL('image/png').split(',')[1];}
 // A one-second VP8 clip recorded here: a real video for poster and play.
 const c=document.createElement('canvas');c.width=320;c.height=180;const x=c.getContext('2d');const stream=c.captureStream(30);const rec=new MediaRecorder(stream,{mimeType:'video/webm;codecs=vp8'});const parts=[];rec.ondataavailable=e=>parts.push(e.data);
 let t=0;const draw=()=>{x.fillStyle='#22313f';x.fillRect(0,0,320,180);x.fillStyle='#f2c14e';x.fillRect(20+t*4%260,60,40,60);t++;};const timer=setInterval(draw,33);draw();rec.start(100);await new Promise(r=>setTimeout(r,1200));rec.stop();await new Promise(r=>rec.onstop=r);clearInterval(timer);
 const bytes=new Uint8Array(await new Blob(parts,{type:'video/webm'}).arrayBuffer());let s='';for(const b of bytes)s+=String.fromCharCode(b);result.video=btoa(s);return result;
 },shapes);await page.close();for(const[k,v]of Object.entries(data))images.set(k,Buffer.from(v,'base64'));
 images.set('text',Buffer.from('Original text attachment\n'));images.set('pdf',Buffer.from('%PDF-1.4 fictional\n'));images.set('zip',Buffer.from('PK\x03\x04fictional'));images.set('csv',Buffer.from('a,b\n1,2\n'));
 images.set('mp3',Buffer.from('ID3fictional-song'));images.set('m4a',Buffer.from('fictional-m4a'));images.set('wav',Buffer.from('RIFFfictionalWAVE'));images.set('invalid',Buffer.from('This is not an image.'));images.set('large-video',Buffer.alloc(0));}
const tileButtons=(page,scope)=>(scope||page).getByRole('button',{name:/^Open (picture|video) /});
async function recordInput(control, type) {
 await control.evaluate((node,event)=>{
  window.imageInputAt=null;
  node.addEventListener(event,e=>{if(e.isTrusted)window.imageInputAt=performance.now();},{once:true,capture:true});
 },type);
}
async function recordDocumentInput(page, type) {
 await page.evaluate(event=>{window.imageInputAt=null;document.addEventListener(event,e=>{if(e.isTrusted)window.imageInputAt=performance.now();},{once:true,capture:true});},type);
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
 const refreshing=page.waitForRequest(req=>{const url=new URL(req.url());return url.pathname.endsWith('/api/dm')&&url.searchParams.get('id')===conv;},{timeout:5000});
 const sync={started:Promise.withResolvers(),release:Promise.withResolvers()};
 fixture.sync=sync;
 const view=fixture.views[conv],id='f'.repeat(32);
 view.messages.push({...view.messages.at(-1),id,lid:id,body:'Concurrent fixture arrival',attachments:[]});
 fixture.overview.seq=++fixture.seq;
 for(const stream of fixture.events)stream.write('event: change\ndata: '+fixture.seq+'\n\n');
 await refreshing;
 await sync.started.promise;
 return async()=>{sync.release.resolve();fixture.sync=null;await page.locator('p').filter({hasText:'Concurrent fixture arrival'}).waitFor({state:'attached'});};
}
// The album's tiles against Telegram's LayoutMediaGroup, scaled to the block.
async function checkAlbum(page, block, g, width) {
 await block.scrollIntoViewIfNeeded();
 await page.waitForFunction(el=>el.hasAttribute('data-album-ready'),await block.elementHandle());
 for(const [i,kind] of g.kinds.entries())if(isPicture(kind)){const im=block.locator('[data-tile="'+i+'"] img');await im.waitFor();await im.evaluate(im=>im.decode());}
 const box=await block.boundingBox();
 const rects=await block.locator('[data-tile]').evaluateAll(nodes=>nodes.map(n=>{const r=n.getBoundingClientRect(),b=n.querySelector('button');return{x:r.x,y:r.y,w:r.width,h:r.height,label:b.getAttribute('aria-label'),radius:getComputedStyle(n).borderTopLeftRadius};}));
 assert.deepEqual(rects.map(r=>r.label.replace(/^Open (picture|video) /,'')),g.names,'tiles keep attachment order');
 const ratios=g.kinds.map(k=>isPicture(k)?shapes[k][0]/shapes[k][1]:16/9);
 const tiles=groups.layoutMediaGroup(ratios.map(r=>({width:Math.round(r*1000),height:1000})));
 const height=Math.max(...tiles.map(t=>t.y+t.height)),factor=box.width/groups.ALBUM_WIDTH;
 assert(Math.abs(box.width-Math.min(groups.ALBUM_WIDTH,box.width))<1&&box.width<=groups.ALBUM_WIDTH+.5,'album is at most 430 px wide');
 assert(Math.abs(box.height-height*factor)<1.5,'album height follows Telegram: '+box.height+' vs '+height*factor);
 for(const [i,t] of tiles.entries()){const r=rects[i];for(const [got,want] of [[r.x-box.x,t.x],[r.y-box.y,t.y],[r.w,t.width],[r.h,t.height]])assert(Math.abs(got-want*factor)<1.5,'tile '+i+' geometry '+JSON.stringify({r,t,factor}));assert.equal(r.radius,'0px','inner tile corners stay square');assert(r.h>=Math.min(44,t.height*factor)-1);}
 assert.notEqual(await block.evaluate(n=>getComputedStyle(n).borderTopLeftRadius),'0px','outer corners are rounded');
 assert(box.x>=0&&box.x+box.width<=width+1,'album fits the screen');
 return {box,tiles:rects.length};
}
async function stepViewer(page, dialogName, delta, phone) {
 const dialog=page.getByRole('dialog').last();
 const stage=dialog.getByRole('region');
 if(phone){
  const b=await stage.boundingBox();const cdp=await page.context().newCDPSession(page);const y=b.y+Math.min(b.height/2,90);const at=x=>[{x:b.x+x,y}];
  await recordDocumentInput(page,'pointerup');
  const [from,to]=delta>0?[b.width*.75,b.width*.25]:[b.width*.25,b.width*.75];
  // A finger's quick swipe: one touch id, moves a frame apart. Chromium
  // starts a fling after it unless the viewer cancels its touchmoves; a tap
  // during that fling (about 400 ms) was no click.
  const pt=x=>[{...at(x)[0],id:1,radiusX:8,radiusY:8,force:1}];
  await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:pt(from)});
  for(let k=1;k<=6;k++){await page.waitForTimeout(16);await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:pt(from+(to-from)*k/6)});}
  await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});await cdp.detach();
 }else{
  const button=dialog.getByRole('button',{name:delta>0?'Next image':'Previous image',exact:true});
  await recordInput(button,'click');await button.click();
 }
 await page.getByRole('dialog',{name:dialogName,exact:true}).waitFor();
 const ms=await measureSinceInput(page,true);
 // The next tap comes soon, inside the fling a quick swipe would start.
 if(phone)await page.waitForTimeout(150);
 return ms;
}
async function openAndBrowse(page, block, g, phone, metric) {
 const first=tileButtons(page,block).first();
 await first.scrollIntoViewIfNeeded();
 if(!phone){
  await page.keyboard.press('Shift');await first.focus(); // a keyboard user's focus
  const ring=await first.evaluate(b=>{const c=getComputedStyle(b);return {visible:b.matches(':focus-visible'),style:c.outlineStyle,width:parseFloat(c.outlineWidth),offset:parseFloat(c.outlineOffset)};});
  assert(ring.visible&&ring.style!=='none'&&ring.width>=2&&ring.offset<=-ring.width,'album tile focus ring is drawn inside the tile, not clipped by the album: '+JSON.stringify(ring));
 }
 await recordInput(first,phone?'click':'keydown');
 if(phone)await first.tap();else await page.keyboard.press('Enter');
 const dialog=page.getByRole('dialog',{name:g.names[0],exact:true});
 await dialog.waitFor();
 const openMs=await measureSinceInput(page,true);
 assert(openMs<1100,'viewer opens within the local feedback bound');
 const steps=[];
 for(let i=0;i<g.names.length;i++){
  const d=page.getByRole('dialog',{name:g.names[i],exact:true});
  assert.equal((await d.locator('span[aria-live]').textContent()).replace(/\s/g,''),(i+1)+'/'+g.names.length,'viewer counts the album');
  const kind=g.kinds[i];
  if(isPicture(kind)){
   const im=d.getByRole('img',{name:g.names[i],exact:true});await im.evaluate(im=>im.decode());
   assert.deepEqual(await im.evaluate(i=>[i.naturalWidth,i.naturalHeight]),shapes[kind],'viewer shows the original');
   if(i===0){
    const button=d.getByRole('button',{name:'Download',exact:true});const downloaded=page.waitForEvent('download');await recordInput(button,'click');if(phone)await button.tap();else await button.click();const saved=await downloaded;
    const downloadMs=await measureSinceInput(page);assert(downloadMs<1100);assert.equal(saved.suggestedFilename(),g.names[0]);assert.deepEqual(fs.readFileSync(await saved.path()),images.get(kind),'original bytes');steps.push({downloadMs});
   }
  }else if(kind==='video'){
   const video=d.locator('video');await video.waitFor();
   await page.waitForFunction(v=>v.readyState>=2&&v.videoWidth===320,await video.elementHandle());
   if(await video.evaluate(v=>v.paused))await video.evaluate(v=>v.play());
   await page.waitForFunction(v=>!v.paused&&v.currentTime>.05,await video.elementHandle());
   assert.equal(await d.getByRole('button',{name:'Download '+g.names[i],exact:true}).count(),1,'video keeps Download');
   fixture.videoNode=await video.elementHandle();
  }else if(kind==='large-video'){
   await d.getByText('Videos up to 16 MiB can play here.',{exact:false}).waitFor();
   assert.equal(await d.locator('video').count(),0);
   if(fixture.videoNode)assert.deepEqual(await fixture.videoNode.evaluate(v=>[v.isConnected,v.paused,v.hasAttribute('src')]),[false,true,false],'leaving a video in the viewer stops and unloads it');
  }
  if(i<g.names.length-1)steps.push({stepMs:await stepViewer(page,g.names[i+1],1,phone)});
 }
 assert(await page.getByRole('dialog').last().getByRole('button',{name:'Next image',exact:true}).isDisabled(),'navigation stops at the album end');
 if(g.names.length>1){steps.push({backMs:await stepViewer(page,g.names.at(-2),-1,phone)});}
 const close=page.getByRole('dialog').last().getByRole('button',{name:'Close',exact:true});
 if(phone){await recordInput(close,'click');await close.tap();}
 else{await close.focus();await recordInput(close,'keydown');await page.keyboard.press('Escape');}
 await page.getByRole('dialog').waitFor({state:'hidden'});
 const closeMs=await measureSinceInput(page,true);
 assert(closeMs<1100,'viewer closes within the local feedback bound');
 assert(await first.evaluate(b=>b.getRootNode().activeElement===b),'focus returns to the opening tile');
 const all=steps.flatMap(s=>Object.values(s));assert(all.every(ms=>ms<1100),'every step under 1100 ms: '+JSON.stringify(steps));
 const result={openMs,maxStepMs:Math.max(0,...steps.map(s=>s.stepMs??s.backMs??0)),closeMs};
 if(metric)console.log('IMAGE_ACTION '+JSON.stringify({...metric,...result}));
 return result;
}
async function checkColumn(page, block, g, phone) {
 await block.scrollIntoViewIfNeeded();
 const names=await block.getByRole('button',{name:/^Download /}).evaluateAll(b=>b.map(n=>n.getAttribute('aria-label').slice(9)));
 assert.deepEqual(names,g.names,'column rows keep attachment order, each with Download');
 const rows=await block.evaluate(n=>[...n.children].map(c=>c.getBoundingClientRect().toJSON()));
 assert.equal(rows.length,g.names.length,'one row per file in one block');
 for(let i=1;i<rows.length;i++)assert(rows[i].top>=rows[i-1].bottom-1,'rows stack in one column');
 assert(rows.every(r=>Math.abs(r.width-rows[0].width)<1),'rows share the block width');
 assert.equal(await block.evaluate(n=>getComputedStyle(n).borderTopWidth!=='0px'),true,'one bordered block');
 const button=block.getByRole('button',{name:'Download '+g.names[0],exact:true});const download=page.waitForEvent('download');
 if(phone)await button.tap();else await button.press('Enter');
 const saved=await download;assert.equal(saved.suggestedFilename(),g.names[0]);assert.deepEqual(fs.readFileSync(await saved.path()),images.get(g.kinds[0]),'original bytes');
}
async function checkPending(page, phone) {
 const pics=[['pending-one.png','portrait'],['pending-two.png','landscape'],['pending-three.png','portrait']];
 await page.locator('form[data-part="composer"] input[type=file]').setInputFiles(pics.map(([name,kind])=>({name,mimeType:'image/png',buffer:images.get(kind)})));
 const field=page.locator('form[data-part="composer"]').getByRole('textbox').first();
 await field.click();await field.pressSequentially('Pending album');
 const send=page.locator('[data-part="send"]');
 await page.waitForFunction(b=>b.getAttribute('aria-disabled')!=='true',await send.elementHandle());
 await recordInput(send,'click');
 if(phone)await send.tap();else await send.click();
 const block=page.locator('[data-album="media"]').last();
 await block.waitFor();
 const feedbackMs=await measureSinceInput(page,true);
 assert(feedbackMs<1100,'a sent album shows at once: '+feedbackMs);
 assert.deepEqual(await tileButtons(page,block).evaluateAll(b=>b.map(n=>[n.getAttribute('aria-label'),n.getAttribute('aria-busy')])),pics.map(([n])=>['Open picture '+n,'true']),'pending album keeps order, marked busy');
 assert.equal(await page.getByText('sent, not kept here',{exact:false}).count(),0,'no file-row flash while sending');
 const caption=await page.locator('p').filter({hasText:'Pending album'}).last().boundingBox(),b=await block.boundingBox();
 assert(caption.y>=b.y+b.height-1,'caption under the pending album');
 return {feedbackMs};
}
(async()=>{try{
 await source();console.log('IMAGE_INPUT '+JSON.stringify({canonical:!!process.env.AGENTNET_IMAGE_BUILT,skin:selected.id,entrySHA256:createHash('sha256').update(bundle).digest('hex'),styleSHA256:createHash('sha256').update(styles).digest('hex')}));await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--autoplay-policy=no-user-gesture-required',...(resource?['--disable-gpu']:[])]});await makeMedia();
 const modes=process.env.AGENTNET_IMAGE_MODE?process.env.AGENTNET_IMAGE_MODE.split(','):latency?['album3']:resource?['album7']:Object.keys(cases);
 const widths=process.env.AGENTNET_IMAGE_WIDTH?process.env.AGENTNET_IMAGE_WIDTH.split(',').map(Number):[1440,360,320];
 for(const history of (latency?[10,100]:[1]))for(const width of widths)for(const mode of modes){
  reset(mode,history);const phone=width<=400;
  const context=await browser.newContext({viewport:{width,height:844},hasTouch:phone,acceptDownloads:true,reducedMotion:'reduce'}),page=await context.newPage(),errors=[];let stage='load';
  page.setDefaultTimeout(resource?10000:6000);
  const metrics=resource?await context.newCDPSession(page):null;
  if(metrics){await metrics.send('Emulation.setCPUThrottlingRate',{rate:6});await metrics.send('Performance.enable');}
  const memory=async()=>{const m=(await metrics.send('Performance.getMetrics')).metrics;return {jsHeapUsedBytes:m.find(x=>x.name==='JSHeapUsedSize').value};};
  page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(['error','warning'].includes(m.type()))errors.push(m.type()+': '+m.text());});
  await context.addInitScript(phone=>{
   if(phone){window.__agentnetPlatform='android';window.__agentnetAndroid={visible:()=>true,onVisibility:()=>()=>{},legacyDrafts:async()=>({drafts:[]}),acknowledgeLegacyDraft:async()=>({removed:true})};}
   window.imageResources={created:[],revoked:[]};
   // Failure evidence: the last trusted pointer events and their targets.
   window.imageEvents=[];for(const type of ['touchstart','pointerdown','pointerup','pointercancel','click','dblclick','touchend','touchcancel','scroll'])document.addEventListener(type,e=>{if(!e.isTrusted)return;const n=e.composedPath()[0],t=n instanceof Element?n.closest('button,[role=region],[role=dialog]'):null;window.imageEvents.push({type,at:Math.round(performance.now()),target:t?(t.getAttribute('aria-label')||t.textContent.trim().slice(0,24)||t.getAttribute('role')):String(n?.nodeName||n)});if(window.imageEvents.length>16)window.imageEvents.shift();},{capture:true});
   const originalCreate=URL.createObjectURL.bind(URL),originalRevoke=URL.revokeObjectURL.bind(URL);
   URL.createObjectURL=b=>{const url=originalCreate(b);window.imageResources.created.push({url,type:b.type});return url;};
   URL.revokeObjectURL=url=>{window.imageResources.revoked.push(url);return originalRevoke(url);};
   window.__agentnetOnBack=fn=>{window.imageBack=fn;return()=>{delete window.imageBack;};};},phone);
  await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  const row={mode,width,skin:selected.id};
  try{
   const held=mode==='album3'&&!latency;
   if(held)fixture.hold={index:fixture.files[1].index,...Promise.withResolvers()};
   await page.goto(origin+'/?skin='+selected.id+'#conv='+conv+'&workspace=default');
   if(!['comic','office'].includes(selected.id))await page.getByRole('button',{name:'Use this skin',exact:true}).click(); // built-in skins ask no trust
   if(mode==='pending'){
    await page.locator('p').filter({hasText:'Before the album'}).waitFor();
    stage='pending album';Object.assign(row,await checkPending(page,phone));
    if(evidence)await page.screenshot({path:path.join(evidence,'media-'+selected.id+'-'+width+'-'+mode+'.png')});
    for(const s of fixture.sends)s.release();
    assert.deepEqual(errors,[]);console.log(JSON.stringify({...row,result:'PASS'}));continue;
   }
   const caption=page.locator('p').filter({hasText:'Caption · '+mode});
   await caption.waitFor();
   const expected=expectedGroups(mode);
   if(held){
    stage='held original';
    const block=page.locator('[data-album="media"]');await block.waitFor();
    const tile=tileButtons(page,block).nth(1);await tile.waitFor();
    assert.equal(await block.getAttribute('data-album-ready'),null,'album waits for every picture size');
    const box=await block.boundingBox(),factor=box.width/groups.ALBUM_WIDTH;
    const placeholder=groups.layoutMediaGroup(fixture.files.map(()=>({width:1000,height:1000})));
    const now=await block.locator('[data-tile]').evaluateAll(n=>n.map(t=>{const r=t.getBoundingClientRect();return [r.width,r.height];}));
    assert(placeholder.every((t,i)=>Math.abs(now[i][0]-t.width*factor)<1.5&&Math.abs(now[i][1]-t.height*factor)<1.5),'unit-ratio layout until every size is known: '+JSON.stringify(now));
    if(phone)await tile.tap();else await tile.press('Enter');
    assert.equal(await page.getByRole('dialog').count(),0,'no viewer before its bytes arrive');
    fixture.hold.resolve();fixture.hold=null;
   }
   if(mode==='failed'){stage='failed re-split';await page.getByRole('button',{name:'Download failed.png',exact:true}).waitFor();}
   const blocks={media:page.locator('[data-album="media"]'),file:page.locator('[data-album="file"]'),music:page.locator('[data-album="music"]')};
   const seen={media:0,file:0,music:0};const order=[];
   for(const g of expected){
    if(!g.album){
     stage='single '+g.names[0];
     const kind=g.kinds[0];
     const control=g.kind==='media'&&isPicture(kind)?page.getByRole('button',{name:'Open picture '+g.names[0],exact:true}):page.getByRole('button',{name:'Download '+g.names[0],exact:true});
     await control.waitFor();order.push((await control.boundingBox()).y);
     if(g.kind==='media'&&isPicture(kind)){const im=control.locator('img');await im.waitFor();await im.evaluate(im=>im.decode());const r=await im.evaluate(i=>[i.clientWidth/i.clientHeight,i.naturalWidth/i.naturalHeight]);assert(Math.abs(r[0]-r[1])<.03,'a single picture keeps its whole shape');}
     continue;
    }
    const block=blocks[g.kind].nth(seen[g.kind]++);
    stage=g.kind+' group '+g.names[0];
    order.push((await block.boundingBox()).y);
    if(g.kind==='media'){const r=await checkAlbum(page,block,g,width);row.albums=[...(row.albums||[]),{tiles:r.tiles,width:Math.round(r.box.width),height:Math.round(r.box.height)}];}
    else await checkColumn(page,block,g,phone);
   }
   for(const k of Object.keys(blocks))assert.equal(await blocks[k].count(),seen[k],'exactly the expected '+k+' groups');
   for(let i=1;i<order.length;i++)assert(order[i]>order[i-1],'groups keep attachment order');
   const last=await page.locator('[data-mid="'+message+'"] [data-part="bubble"]').evaluate(b=>{const c=[...b.querySelectorAll('[data-album],[data-image-group],button[aria-label^="Download "],button[aria-label^="Open picture "]')].map(n=>n.getBoundingClientRect().bottom);return Math.max(...c);});
   assert((await caption.boundingBox()).y>=last-1,'the caption sits under the last group (Telegram send_files_box.cpp)');
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no horizontal scroll');
   if(evidence){await page.locator('[data-mid="'+message+'"]').scrollIntoViewIfNeeded();await page.screenshot({path:path.join(evidence,'media-'+selected.id+'-'+width+'-'+mode+'.png')});}
   const firstAlbum=expected.find(g=>g.album&&g.kind==='media');
   if(firstAlbum){
    stage='viewer';
    const release=latency?await holdNewArrival(page):null;
    const r=await openAndBrowse(page,blocks.media.first(),firstAlbum,phone,latency?{history,width}:null);
    if(release)await release();
    row.viewer=r;
   }
   if(mode==='failed'){
    stage='failed picture';
    assert.equal(await page.getByRole('button',{name:'Download failed.png',exact:true}).count(),1,'a failed picture becomes a file row');
    assert.equal(await blocks.media.count(),0,'the album re-forms around it (two singles)');
   }
   if(resource){const resources=await page.evaluate(async()=>{const module=await import('/assets/skins/comic/entry.mjs');const root=document.querySelector('#skin').shadowRoot.querySelector('.an-root');await module.unmount(root);const r=window.imageResources;return {created:r.created.filter(x=>x.type.startsWith('image/')).map(x=>x.url),revoked:r.revoked};});assert(resources.created.every(url=>resources.revoked.includes(url)),'unmount revokes every original image URL');console.log('IMAGE_RESOURCE '+JSON.stringify({width,cpuThrottle:6,reads:fixture.reads.length,...await memory().catch(()=>({})),created:resources.created.length}));}
   const wanted=new Set(fixture.files.filter((f,i)=>{const k=cases[mode][i][1];return isPicture(k)||k==='invalid'||k==='video';}).map(f=>f.index));
   assert(fixture.reads.every(r=>r.id===message&&r.dir===fixture.dir&&fixture.files.some(f=>f.index===r.index)),'exact sparse file index and direction');
   assert(fixture.reads.filter(r=>wanted.has(r.index)).length>=0);
   assert(!fixture.posts.some(r=>/\/api\/(send|dm\/send|request\/followup)$/.test(r.api)));
   assert.deepEqual(errors,[]);
   console.log(JSON.stringify({...row,groups:expected.map(g=>(g.album?g.kind:'single')+':'+g.names.length),result:'PASS'}));
  }catch(e){const input=await page.evaluate(()=>({recorded:window.imageInputAt!=null,events:window.imageEvents})).catch(()=>null);console.error('IMAGE_FAILURE '+JSON.stringify({mode,width,stage,input,errors,reads:fixture.reads,posts:fixture.posts.map(p=>p.api)}));if(evidence)await page.screenshot({path:path.join(evidence,'media-'+selected.id+'-'+width+'-'+mode+'-failure.png')}).catch(()=>{});throw e;
  }finally{fixture.hold?.resolve();fixture.sync?.release.resolve();for(const s of fixture.sends)s.release();await context.close();for(const res of fixture.events)res.end();}
 }
}finally{await browser?.close();server.closeAllConnections();await new Promise(r=>server.close(r));fs.rmSync(temporary,{recursive:true,force:true});}})().catch(e=>{console.error(e);process.exitCode=1;});
