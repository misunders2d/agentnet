// Synthetic fixture only; real built Comic + host API, three browser engines.
const assert=require('node:assert/strict'), fs=require('node:fs'), path=require('node:path');
const engines=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
for(const engine of ['chromium','firefox','webkit']) {
 const browser=await engines[engine].launch({headless:true});
 try { for(const width of [1440,390]) {
  const ctx=await browser.newContext({viewport:{width,height:900},acceptDownloads:true});
  const p=await ctx.newPage();p.setDefaultTimeout(20000);
  const errors=[];p.on('pageerror',e=>errors.push(String(e)));
  let release;const gate=new Promise(r=>release=r);
  await p.route('**/api/files/picture-message/8?dir=in',async route=>{await gate;await route.continue();});
  await p.goto(process.argv[2]);assert.equal(await p.title(),'AgentNet');
  await p.getByRole('button',{name:/^Bob/}).first().click();
  const card=p.getByRole('group',{name:'Video received.webm',exact:true});
  const play=card.getByRole('button',{name:'Play video received.webm',exact:true});
  await play.scrollIntoViewIfNeeded();
  await card.getByText('Preparing preview…').waitFor();
  const composer=p.getByRole('textbox').last();
  const start=Date.now();await composer.fill('Still responsive');assert(Date.now()-start<1100);
  assert.equal(await composer.inputValue(),'Still responsive');
  release();await play.scrollIntoViewIfNeeded();await play.locator('img').waitFor();
  await play.locator('img').evaluate(im=>im.decode());
  assert.equal(await card.locator('video').count(),0,'poster is inert image; no autoplay');
  assert((await play.locator('img').getAttribute('src')).startsWith('data:image/jpeg'));
  await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`poster-${engine}-${width}.png`)});
  const click=Date.now();await play.click();await card.getByRole('button',{name:'Close video received.webm'}).waitFor();
  assert(Date.now()-click<1100,'local playback loading feedback');
  await card.locator('video').waitFor();
  await card.getByRole('button',{name:'Close video received.webm'}).click();await play.waitFor();
  await p.unroute('**/api/files/picture-message/8?dir=in');
  const bad=p.getByRole('group',{name:'Video spoof.mp4',exact:true});
  await bad.getByRole('button',{name:'Play video spoof.mp4'}).scrollIntoViewIfNeeded();
  await bad.getByText('Preview unavailable. Play or download this video.').waitFor();
  await bad.getByRole('button',{name:'Play video spoof.mp4'}).click();
  await bad.getByRole('button',{name:'Play video spoof.mp4'}).waitFor();
  await bad.getByRole('button',{name:'Download spoof.mp4'}).waitFor();
  const sent=p.getByRole('group',{name:'Video sent.webm',exact:true});
  await sent.getByRole('button',{name:'Play video sent.webm'}).scrollIntoViewIfNeeded();
  await sent.locator('img').waitFor();
  assert.equal(await sent.locator('video').count(),0);
  const offline=p.getByRole('group',{name:'Video unavailable.webm',exact:true});
  await offline.getByRole('button',{name:'Play video unavailable.webm'}).scrollIntoViewIfNeeded();
  await offline.getByText('Preview unavailable. Play or download this video.').waitFor();
  await offline.getByRole('button',{name:'Download unavailable.webm'}).waitFor();
  assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({engine,width,poster:true,heldComposerMs:Date.now()-start,playFeedbackUnder1100:true,failureFallback:true}));
  await ctx.close();
 }} finally {await browser.close();}
}
})().catch(e=>{console.error(e);process.exit(1)});
