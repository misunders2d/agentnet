// Real Comic package and guarded file API: each thumbnail opens its original.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);

async function checkVideos(p, ctx, phone, base, errors, reads, probes) {
  const card = name => p.getByRole('group', { name: 'Video ' + name, exact: true });
  const file = name => card(name).getByRole('button', { name: 'Play video ' + name, exact: true });
  const close = name => card(name).getByRole('button', { name: 'Close video ' + name, exact: true });
  await file('received.webm').waitFor(); // baseline fails: video was only Download
  assert.equal(await p.locator('video').count(), 0, 'no media element/decode before explicit Play');
  assert.deepEqual(await p.evaluate(() => window.videoResources.created.filter(x => x.type.startsWith('video/'))), [], 'no video Blob before Play');
  const policy = (await ctx.request.get(base + '/')).headers()['content-security-policy'];
  assert.equal(policy.split(';').map(x => x.trim()).find(x => x.startsWith('media-src ')), 'media-src blob:', 'actual workspace host permits only owned Blob media');
  assert.equal(await file('large.webm').count(), 0);
  assert.equal(await file('unknown.mov').count(), 0);
  await card('large.webm').getByText('Videos up to 16 MiB can play here.', { exact: false }).waitFor();
  await card('unknown.mov').getByText('This video’s size is unknown.', { exact: false }).waitFor();
  assert.deepEqual(reads, [], 'manifest gate and initial cards do not read videos');
  const playBox = await file('received.webm').boundingBox();
  assert(playBox.height >= 44, 'Play has a 44px target');
  await file('received.webm').click();
  let video = card('received.webm').locator('video');
  await video.waitFor();
  await p.waitForFunction(v => v.readyState >= 2 && v.videoWidth === 160 && v.videoHeight === 120, await video.elementHandle());
  assert.deepEqual(reads, [[8,'in']], 'one exact sparse index/id/direction read per open');
  const src = await video.getAttribute('src');
  assert(src.startsWith('blob:'));
  assert.deepEqual(await video.evaluate(v => [v.controls,v.playsInline,v.autoplay,v.muted]), [true,true,false,false]);
  await p.waitForFunction(v => !v.paused && v.currentTime > .1, await video.elementHandle());
  await video.evaluate(v => v.pause());
  assert.equal(await video.evaluate(v => v.paused), true);
  await video.evaluate(v => { v.currentTime = 1.5; v.volume = .4; });
  await p.waitForFunction(v => !v.seeking && Math.abs(v.currentTime-1.5) < .15, await video.elementHandle());
  assert.equal(await video.evaluate(v => v.volume), .4, 'native audio control property works');
  const decodedAudio = await video.evaluate(v => v.webkitAudioDecodedByteCount);
  assert(decodedAudio > 0, 'local Vorbis audio actually decoded');
  const fullscreenEnabled = await p.evaluate(() => document.fullscreenEnabled);
  fs.writeFileSync(path.join(process.env.AGENTNET_SCREENSHOTS,`video-${phone?'390':'1440'}-support.json`),JSON.stringify({fullscreenEnabled,decodedAudio},null,2));
  if (fullscreenEnabled) {
    await video.evaluate(v => v.requestFullscreen());
    assert.equal(await video.evaluate(v => v.ownerDocument.fullscreenElement === v || v.getRootNode().fullscreenElement === v), true);
    await p.evaluate(() => document.exitFullscreen());
  }
  await video.scrollIntoViewIfNeeded();
  const geometry = await video.boundingBox();
  assert(geometry.width <= (phone ? 390 : 1440) && geometry.height <= (phone ? 844 : 900)*.6+1);
  assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  if (process.env.AGENTNET_SCREENSHOTS) await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`video-${phone?'390':'1440'}.png`)});
  const original = await ctx.request.get(base + '/api/files/picture-message/8?dir=in');
  const download = p.waitForEvent('download');
  await card('received.webm').getByRole('button',{name:'Download received.webm',exact:true}).click();
  const saved = await download;
  assert.equal(saved.suggestedFilename(),'received.webm');
  assert.deepEqual(fs.readFileSync(await saved.path()),await original.body(),'original video download remains byte exact');
  const handle = await video.elementHandle();
  await close('received.webm').click();
  await video.waitFor({state:'detached'});
  assert.deepEqual(await handle.evaluate(v => [v.paused,v.hasAttribute('src')]),[true,false]);
  assert(await p.evaluate(src => window.videoResources.revoked.includes(src),src),'Close revokes owned media URL');

  // Delayed async activation rejection keeps native controls and allows the
  // user's next Play; it is not misreported as unsupported codec.
  await p.evaluate(() => { window.videoRejectNextPlay = true; });
  await file('received.webm').click();
  await card('received.webm').getByRole('status').filter({hasText:'Tap Play in the video controls to start.'}).waitFor();
  video = card('received.webm').locator('video');
  await video.evaluate(v => v.play());
  await p.waitForFunction(v => !v.paused && v.currentTime > .1,await video.elementHandle());
  await close('received.webm').click();

  for(const name of ['spoof.mp4','broken.webm','oversized-return.webm']) {
    await file(name).click();
    await card(name).getByRole('status').filter({hasText:/can’t be played here|size differs/}).waitFor();
    assert.equal(await card(name).locator('video').count(),0,'bad bytes/decode/returned-size limit retain Download only');
    assert.equal(await card(name).getByRole('button',{name:'Download '+name,exact:true}).count(),1);
  }
  await file('unavailable.webm').click();await card('unavailable.webm').getByRole('status').waitFor();
  assert.equal(await card('unavailable.webm').locator('video').count(),0,'host verification/read failure does not mount media');
  assert.equal(await card('unavailable.webm').getByRole('button',{name:'Download unavailable.webm',exact:true}).count(),1);
  assert(!reads.some(([i]) => i===11 || i===12),'large and unknown manifests never fetch');

  let resume;
  const gate = new Promise(resolve => { resume=resolve; });
  await p.route('**/api/files/picture-message/14?dir=in',async route => { await gate; await route.continue(); });
  await file('late.webm').click();
  await card('late.webm').getByRole('status').filter({hasText:'Loading video…'}).waitFor();
  const before = await p.evaluate(() => window.videoResources.created.length);
  await close('late.webm').click();
  const finished = p.waitForResponse(r => r.url().endsWith('/api/files/picture-message/14?dir=in'));
  resume(); await (await finished).finished();
  await p.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await p.evaluate(() => window.videoResources.created.length),before,'late read after Close creates no Blob');
  assert.equal(await card('late.webm').locator('video').count(),0);
  await p.unroute('**/api/files/picture-message/14?dir=in');

  for(const cause of ['pagehide','hidden',...(phone?['android']:[])]) {
    await file('received.webm').click();
    video=card('received.webm').locator('video');await video.waitFor();
    const url=await video.getAttribute('src'), old=await video.elementHandle();
    await p.evaluate(cause => {
      if(cause==='hidden') {Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));delete document.hidden;}
      else if(cause==='android')window.videoAndroidVisibility(false);
      else window.dispatchEvent(new Event('pagehide'));
    },cause);
    await video.waitFor({state:'detached'});
    assert.deepEqual(await old.evaluate(v => [v.paused,v.hasAttribute('src')]),[true,false],cause+' unloads native media');
    assert(await p.evaluate(url => window.videoResources.revoked.includes(url),url),cause+' revokes URL');
    if(cause==='android')await p.evaluate(() => window.videoAndroidVisibility(true));
  }
  const navigate=async()=>{if(phone)await p.getByRole('button',{name:'Back to chats',exact:true}).first().click();else await p.getByRole('button',{name:/^Carol/}).first().click();};
  const back=async()=>{await p.getByRole('button',{name:/^Bob/}).first().click();await file('received.webm').waitFor();};
  const measure=async(node,start,label)=>{
    const state=await node.evaluate((v,start)=>{
      const levels=[];
      for(let e=v;e;e=e.parentElement){
        if(e.matches('[data-swap-side],[data-card]')){
          const style=getComputedStyle(e);
          levels.push({kind:e.hasAttribute('data-card')?'card':'swap',level:e.parentElement?.getAttribute('data-swap')||null,side:e.getAttribute('data-swap-side')||(e.getAttribute('aria-hidden')==='true'?'leaving':'current'),duration:style.animationDuration,animation:style.animationName});
        }
      }
      const leaving=levels.find(e=>e.side==='leaving'),media=v.tagName==='VIDEO'?v:v.querySelector('video');
      const durationMs=leaving?Math.max(...leaving.duration.split(',').map(s=>parseFloat(s)*(s.trim().endsWith('ms')?1:1000))):null;
      return {captured:{tag:v.tagName,role:v.getAttribute('role'),label:v.getAttribute('aria-label')},connected:v.isConnected,phase:!v.isConnected?'unmounted':leaving?leaving.kind+'-leaving':'current',paused:media?media.paused:null,src:media?.getAttribute('src')??null,mediaCount:v.tagName==='VIDEO'?1:v.querySelectorAll('video').length,side:leaving?.side||null,level:leaving?.level||null,duration:leaving?.duration||null,durationMs,levels,elapsed:performance.now()-start};
    },start);
    fs.writeFileSync(path.join(process.env.AGENTNET_SCREENSHOTS,`video-${phone?'390':'1440'}-${label}.json`),JSON.stringify(state,null,2));return state;
  };
  const phaseGate=async(node,start,label)=>{
    let failure;
    try{await p.waitForFunction(v=>!v.isConnected||!!v.closest('[data-swap-side="leaving"],[data-card][aria-hidden="true"]'),node);}catch(e){failure=String(e);}
    const phase=await measure(node,start,label+'-phase');
    assert(!failure,'navigation reached leaving or detached: '+JSON.stringify({failure,phase}));
    assert(phase.phase==='unmounted'||phase.connected&&phase.side==='leaving',JSON.stringify(phase));
    return phase;
  };
  const cleanupAtPhase=async(node,start,label,phase)=>{
    let failure;
    if(phase.phase!=='unmounted'){
      assert(phase.durationMs>0&&phase.durationMs<=240,'existing leave duration: '+JSON.stringify(phase));
      try{await p.waitForFunction(v=>!v.isConnected||(v.paused&&!v.hasAttribute('src')),node,{timeout:Math.ceil(phase.durationMs)});}catch(e){failure=String(e);}
    }
    const cleanup=await measure(node,start,label+'-cleanup');
    assert(!failure,'cleanup within existing leave duration: '+JSON.stringify({failure,phase,cleanup}));
    if(phase.phase!=='unmounted')assert(cleanup.connected&&cleanup.side==='leaving','cleanup before unmount: '+JSON.stringify({phase,cleanup}));
    else assert.equal(cleanup.connected,false,'direct unmount remains separately classified');
    assert.equal(cleanup.paused,true,JSON.stringify(cleanup));assert.equal(cleanup.src,null,JSON.stringify(cleanup));
    return {phase,cleanup,proof:phase.phase==='unmounted'?'unmount-only':phase.phase};
  };
  const releasedURL=async url=>{
    const revoked=await p.evaluate(url=>window.videoResources.revoked.includes(url),url);assert(revoked,'owned video URL revoked');
    probes.urls.add(url);
    const denied=await p.evaluate(async url=>{try{await fetch(url);return false;}catch{return true;}},url);assert(denied,'old video Blob can no longer fetch');
    return {revoked,fetchDenied:denied};
  };
  await file('received.webm').click();
  video=card('received.webm').locator('video');await video.waitFor();
  const navigationURL=await video.getAttribute('src'), detached=await video.elementHandle();
  await p.waitForFunction(v=>!v.paused&&v.currentTime>0,detached);
  const navStart=await p.evaluate(()=>performance.now());
  await navigate();
  const navigation=await cleanupAtPhase(detached,navStart,'chat',await phaseGate(detached,navStart,'chat'));
  navigation.url=await releasedURL(navigationURL);
  await p.waitForFunction(v=>!v.isConnected,detached);await back();

  // Finish a held real file response during the connected leaving interval.
  let releaseLate,arrive;
  const arrived=new Promise(resolve=>{arrive=resolve;});
  await p.route('**/api/files/picture-message/14?dir=in',async route=>{
    const response=await route.fetch(),body=await response.body();
    await new Promise(resolve=>{releaseLate=async()=>{await route.fulfill({response,body});resolve();};arrive();});
  });
  await file('late.webm').click();await arrived;
  const lateNode=await card('late.webm').elementHandle(),lateBefore=await p.evaluate(()=>window.videoResources.created.length),lateStart=await p.evaluate(()=>performance.now());
  await navigate();
  const latePhase=await phaseGate(lateNode,lateStart,'late');
  const finishing=p.waitForResponse(r=>r.url().endsWith('/api/files/picture-message/14?dir=in'));
  await releaseLate();await(await finishing).finished();
  await p.evaluate(()=>new Promise(resolve=>requestAnimationFrame(resolve)));
  const late={phase:latePhase,after:await measure(lateNode,lateStart,'late-after-response'),created:await p.evaluate(()=>window.videoResources.created.length)};
  fs.writeFileSync(path.join(process.env.AGENTNET_SCREENSHOTS,`video-${phone?'390':'1440'}-late-leaving.json`),JSON.stringify(late,null,2));
  if(latePhase.phase!=='unmounted')assert(late.after.connected&&late.after.side==='leaving','late completion is inside the actual leave window: '+JSON.stringify(late));
  else assert.equal(late.after.connected,false,'late completion after direct unmount is separately classified');
  assert.equal(late.after.mediaCount,0,'late bytes never mount media');assert.equal(late.after.src,null,'late bytes never set a source');
  assert.equal(late.created,lateBefore,'late completion while leaving creates no URL');
  await p.unroute('**/api/files/picture-message/14?dir=in');await p.waitForFunction(node=>!node.isConnected,lateNode);await back();

  // A topic selection replaces the keyed Timeline immediately, without a
  // leave window; exercise its existing unmount cleanup rather than invent one.
  await file('received.webm').click();video=card('received.webm').locator('video');await video.waitFor();
  const topicNode=await video.elementHandle(),topicURL=await video.getAttribute('src'),topicStart=await p.evaluate(()=>performance.now());
  await p.waitForFunction(v=>!v.paused&&v.currentTime>0,topicNode);
  const topicNavigation=p.getByRole('navigation',{name:'Topics',exact:true});
  await topicNavigation.getByRole('button',{name:/^All topics(?: \(|$)/}).click();
  const topicDialog=p.getByRole('dialog',{name:'All topics',exact:true});
  await topicDialog.getByRole('button',{name:/^Video topic(?:,|$)/}).click();
  await topicDialog.waitFor({state:'hidden'});
  const topic=await cleanupAtPhase(topicNode,topicStart,'topic',await phaseGate(topicNode,topicStart,'topic'));
  topic.url=await releasedURL(topicURL);await p.waitForFunction(v=>!v.isConnected,topicNode);
  await p.getByRole('navigation',{name:'Topics',exact:true}).getByRole('button',{name:/^Main(?:,|$)/}).click();await file('received.webm').waitFor();

  // Exercise real CSP denials, without making the forbidden media requests.
  const denyStart=errors.length;
  const denied=await p.evaluate(async () => {
    const got=[];const listener=e=>{if(e.effectiveDirective==='media-src')got.push(e.blockedURI);};
    document.addEventListener('securitypolicyviolation',listener);
    const urls=['/api/files/picture-message/8?dir=in','https://media.invalid/forbidden.webm'];
    const nodes=urls.map(src=>{const v=document.createElement('video');v.src=src;v.preload='auto';document.body.append(v);v.load();return v;});
    await new Promise((resolve,reject)=>{const end=Date.now()+3000;const check=()=>{if(got.length>=2)resolve();else if(Date.now()>end)reject(Error('missing media CSP violations'));else setTimeout(check,10);};check();});
    for(const v of nodes){v.removeAttribute('src');v.load();v.remove();}document.removeEventListener('securitypolicyviolation',listener);return got;
  });
  assert(denied.some(x=>x.includes('/api/files/')) && denied.some(x=>x.includes('media.invalid')),'direct route and remote media both denied');
  assert(errors.slice(denyStart).every(e => /Content Security Policy|violates.*directive/i.test(e.text||e)),'only expected CSP console errors');
  assert.deepEqual(errors.slice(0,denyStart),[],'no video runtime/console errors before deliberate CSP denials');
  // WorkspaceShell supplies two real bound fixture providers. Selecting the
  // second workspace unmounts the first skin, which must unload its video.
  await file('received.webm').click();video=card('received.webm').locator('video');await video.waitFor();
  let workspace={executed:false,reason:'phone workspace control is behind the chat card; Back already releases its video'};
  if(!phone){
    const workspaceNode=await video.elementHandle(),workspaceURL=await video.getAttribute('src');
    await p.waitForFunction(v=>!v.paused&&v.currentTime>0,workspaceNode);
    const workspaceStart=await p.evaluate(()=>performance.now());
    await p.getByRole('button',{name:/^Workspace: .*Switch or join another/}).click();
    await p.getByRole('button',{name:/Second fixture.*Switch to it/}).click();
    workspace=await cleanupAtPhase(workspaceNode,workspaceStart,'workspace',await phaseGate(workspaceNode,workspaceStart,'workspace'));
    workspace.url=await releasedURL(workspaceURL);await p.waitForFunction(v=>!v.isConnected,workspaceNode);
  }else await close('received.webm').click();
  console.log(JSON.stringify({video:phone?'390':'1440',policy,reads,decodedAudio,fullscreenEnabled,denied,geometry,navigation,late,topic,workspace,expectedResourceErrors:probes.errors}));
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  let activePage,activeWidth;
  try {
    for (const phone of [false, true]) {
      const ctx = await browser.newContext({ viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 }, hasTouch: phone, acceptDownloads: true });
      await ctx.addInitScript(() => {
        window.videoResources={created:[],revoked:[]};
        const create=URL.createObjectURL.bind(URL),revoke=URL.revokeObjectURL.bind(URL),play=HTMLMediaElement.prototype.play;
        URL.createObjectURL=b=>{const url=create(b);window.videoResources.created.push({url,type:b.type,size:b.size});return url;};
        URL.revokeObjectURL=url=>{window.videoResources.revoked.push(url);revoke(url);};
        HTMLMediaElement.prototype.play=function(){if(window.videoRejectNextPlay&&this.tagName==='VIDEO'){window.videoRejectNextPlay=false;return Promise.reject(new DOMException('Gesture required','NotAllowedError'));}return play.call(this);};
      });
      if (phone) await ctx.addInitScript(() => {
        window.__agentnetPlatform = 'android';
        window.__agentnetOnBack = fn => { window.imageViewerBack = fn; return () => { delete window.imageViewerBack; }; };
        let visible=true;const listeners=new Set();
        window.__agentnetAndroid={visible:()=>visible,onVisibility:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},legacyDrafts:async()=>({drafts:[]}),acknowledgeLegacyDraft:async()=>({removed:true})};
        window.videoAndroidVisibility=value=>{visible=value;for(const fn of listeners)fn(value);};
      });
      const p = await ctx.newPage(), errors = [], reads = [], probes={urls:new Set(),errors:[]};
      activePage=p;activeWidth=phone?390:1440;
      const assetBodies=[];
      p.on('response',r=>{
        const name=new URL(r.url()).pathname.match(/^\/assets\/skins\/comic\/(entry\.mjs|style\.css|document\.css)$/)?.[1];
        if(name)assetBodies.push(r.body().then(b=>({name,url:r.url(),sha256:createHash('sha256').update(b).digest('hex')})));
      });
      p.on('request', r => { const m = r.url().match(/\/api\/files\/picture-message\/(\d+)\?dir=(in|out)$/); if (m && +m[1] >= 8) {reads.push([+m[1],m[2]]);if(+m[1]===15)probes.urls.add(r.url());} });
      p.setDefaultTimeout(10000);
      p.on('pageerror', e => errors.push(String(e)));
      p.on('console', m => { if (m.type() === 'error') {const e={text:m.text(),url:m.location().url};if(probes.urls.has(e.url)&&/ERR_FILE_NOT_FOUND|Failed to load resource|Fetch API cannot load/.test(e.text))probes.errors.push(e);else errors.push(e);} });
      await p.goto(process.argv[2]);
      assert.equal(await p.title(), 'AgentNet');
      assert.equal(new URL(p.url()).origin, new URL(process.argv[2]).origin);
      await p.getByRole('button', { name: /^Bob/ }).first().click();
      const executedAssets=await Promise.all(assetBodies);
      fs.writeFileSync(path.join(process.env.AGENTNET_SCREENSHOTS,`executed-assets-${phone?'390':'1440'}.json`),JSON.stringify(executedAssets,null,2));
      if(process.env.AGENTNET_VIDEO_COMIC){
        assert(executedAssets.some(a=>a.name==='entry.mjs'),'record actual loaded Comic entry');
        for(const a of executedAssets)assert.equal(a.sha256,createHash('sha256').update(fs.readFileSync(path.join(process.env.AGENTNET_VIDEO_COMIC,a.name))).digest('hex'),'actual loaded immutable '+a.name);
      }
      for (const [name, index, width, height] of [['wide.png', 2, 2400, 1600], ['tall.png', 5, 1000, 2000]]) {
        const thumb = p.getByRole('button', { name: 'Open picture ' + name, exact: true });
        // Extra attachment cards put the old lazy thumbnails above the
        // initial tail viewport; reveal each before waiting for its image.
        await thumb.scrollIntoViewIfNeeded();
        await thumb.locator('img').waitFor();
        await thumb.locator('img').evaluate(im => im.decode());
        const preview = await thumb.locator('img').evaluate(im => ({ width: im.clientWidth, height: im.clientHeight, natural: im.naturalWidth / im.naturalHeight }));
        assert(preview.width > 0 && preview.height > 0, 'preview remains visible');
        assert(Math.abs(preview.width / preview.height - preview.natural) < .02, 'portrait and landscape previews retain whole original ratio');
        assert(preview.height <= (phone ? 844 : 900) * .6 + 1, 'media preview remains bounded by viewport');
        await thumb.click();
        const dialog = p.getByRole('dialog', { name, exact: true }), image = dialog.getByRole('img', { name, exact: true });
        await dialog.waitFor();
        await image.evaluate(im => im.decode());
        assert.deepEqual(await dialog.evaluate(d => {
          const b = d.getBoundingClientRect();
          return [b.x, b.y, b.width, b.height];
        }), [0, 0, phone ? 390 : 1440, phone ? 844 : 900], 'viewer fills the viewport');
        assert.deepEqual(await image.evaluate(im => [im.naturalWidth, im.naturalHeight]), [width, height], 'selected original resolution');
        assert.equal(await image.getAttribute('src'), await thumb.locator('img').getAttribute('src'), 'viewer uses this thumbnail’s original');
        const original = await ctx.request.get(new URL(process.argv[2]).origin + `/api/files/picture-message/${index}?dir=in`);
        assert(original.ok(), 'guarded original attachment read succeeds');
        const originalBytes = await original.body();
        const stage = dialog.getByRole('region');
        await p.waitForFunction(im => im.clientWidth > 0 && im.clientWidth <= im.parentElement.clientWidth + 1 && im.clientHeight <= im.parentElement.clientHeight + 1, await image.elementHandle(), { timeout: 5000 }).catch(async e => {
          console.error('Fit geometry:', await image.evaluate(im => ({ image: [im.clientWidth, im.clientHeight], stage: [im.parentElement.clientWidth, im.parentElement.clientHeight], style: im.getAttribute('style') })));
          throw e;
        });
        const fitted = await image.boundingBox();
        const box = await stage.boundingBox();
        assert(fitted.width <= box.width + 1 && fitted.height <= box.height + 1, 'whole image fits initially');
        await dialog.getByRole('button', { name: '100%', exact: true }).click();
        assert.equal(await dialog.getByLabel('Zoom level').textContent(), '100%');
        assert.deepEqual(await image.evaluate(im => [im.getBoundingClientRect().width, im.getBoundingClientRect().height]), [width, height], '100% displays original pixels');
        await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await p.mouse.down();
        await p.mouse.move(box.x + box.width / 2 + 60, box.y + box.height / 2 + 40);
        await p.mouse.up();
        assert.notEqual(await image.evaluate(im => im.style.transform), 'translate(0px, 0px)', 'drag pans the enlarged image');
        await stage.press('ArrowRight');
        await dialog.getByRole('button', { name: 'Fit', exact: true }).click();
        assert.equal(await image.evaluate(im => im.style.transform), 'translate(0px, 0px)');
        const before = Number((await dialog.getByLabel('Zoom level').textContent()).replace('%', ''));
        await dialog.getByRole('button', { name: 'Zoom in', exact: true }).click();
        assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%', '')) > before);
        await dialog.getByRole('button', { name: 'Zoom out', exact: true }).click();
        await stage.hover();
        await p.mouse.wheel(0, -120);
        await p.waitForTimeout(100);
        assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%', '')) > before, 'wheel zooms');
        await dialog.getByRole('button', { name: 'Fit', exact: true }).click();
        if (phone) {
          const cdp = await ctx.newCDPSession(p);
          const touches = distance => [{ x: box.x + box.width / 2 - distance / 2, y: box.y + box.height / 2 }, { x: box.x + box.width / 2 + distance / 2, y: box.y + box.height / 2 }];
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: touches(60) });
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: touches(120) });
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
          assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%', '')) > before, 'real touch pinch zooms');
          assert.equal(await dialog.getByRole('button', { name: 'Open in default app', exact: true }).count(), 0, 'Android has no desktop action');
        } else {
          await dialog.getByRole('button', { name: 'Open in default app', exact: true }).click();
          await p.getByRole('status').filter({ hasText: 'Asked your default image app to open it.' }).waitFor();
        }
        const download = p.waitForEvent('download');
        await dialog.getByRole('button', { name: 'Download', exact: true }).click();
        const d = await download;
        assert.equal(d.suggestedFilename(), name);
        assert.deepEqual(fs.readFileSync(await d.path()), originalBytes, 'Download keeps exact original');
        assert.equal(await dialog.getByRole('button', { name: 'Use as my picture', exact: true }).count(), 1);
        await dialog.getByRole('button', { name: 'Close', exact: true }).focus();
        await p.keyboard.press('Tab');
        assert(await dialog.evaluate(d => d.contains(d.getRootNode().activeElement)), 'Tab stays inside the viewer');
        assert.equal(await p.locator('.an-app').evaluate(el => el.inert), true);
        assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        if (process.env.AGENTNET_SCREENSHOTS) {
          fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS, { recursive: true, mode: 0o700 });
          await dialog.getByRole('button', { name: 'Fit', exact: true }).click();
          await p.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `image-viewer-${phone ? '390' : '1440'}-${name}.png`) });
        }
        if (phone) assert.equal(await p.evaluate(() => window.imageViewerBack()), true, 'Android Back closes the viewer');
        else await p.keyboard.press('Escape');
        await dialog.waitFor({ state: 'hidden' });
        assert.equal(await p.locator('.an-app').evaluate(el => el.inert), false);
        assert(await thumb.evaluate(el => el.getRootNode().activeElement === el), 'focus returns to the exact thumbnail');
      }
      assert.deepEqual(errors, [], 'no runtime or console errors');
      await checkVideos(p,ctx,phone,new URL(process.argv[2]).origin,errors,reads,probes);
      await ctx.close();
    }
    console.log('image viewer + video rendered PASS: original image checks, lazy exact video read, metadata/audio/play/pause/seek, fallback/limits, lifecycle and Blob-only CSP');
  } catch(error) {
    if(activePage&&!activePage.isClosed()){
      await activePage.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'video-failure-'+activeWidth+'.png')}).catch(()=>{});
      const state=await activePage.evaluate(()=>({url:location.href,resources:window.videoResources,html:document.querySelector('#skin')?.shadowRoot?.innerHTML||document.body.innerHTML})).catch(e=>({captureError:String(e)}));
      fs.writeFileSync(path.join(process.env.AGENTNET_SCREENSHOTS,'video-failure-'+activeWidth+'.json'),JSON.stringify({error:String(error),state},null,2));
    }throw error;
  } finally {
    await browser.close();
  }
})().catch(e => { console.error(e); process.exit(1); });
