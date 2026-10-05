const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const gate=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
const evidence=process.env.P12_EVIDENCE || '/tmp/herdr-fixes-1005/p12-rendered';fs.mkdirSync(evidence,{recursive:true,mode:0o700});
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try {for(const skin of ['classic','zoom','comic']) {
  const context=await browser.newContext({viewport:{width:1440,height:960}}),p=await context.newPage();const errors=[];p.on('pageerror',e=>errors.push(String(e)));
  const url=new URL(process.env.P12_URL);await p.goto(url.href);await p.goto(url.origin+'/?skin='+skin);
  const root=p.locator('#skin').first();await root.getByRole('button').filter({hasText:'Vitalii'}).first().click();
  if(skin==='zoom') {
    await root.locator('.thread-list button').first().click();
  }
  const box=skin==='comic'?root.locator('textarea').last():root.locator('#body');
  if(skin!=='zoom') await box.waitFor({state:'visible'});
  const send=skin==='comic'?root.getByRole('button',{name:/^Send/}).last():root.locator('#send');
  let current, reject=false, saveFirst=false;
  await p.route('**/api/dm/send',async route=>{
    const item={request:route.request().postDataJSON(),before:gate(),after:gate(),saved:gate()};current=item;
    await item.before.promise;
    if(reject) {await route.fulfill({status:409,body:'Synthetic local save failed',headers:{'content-type':'text/plain'}});return;}
    const response=await route.fetch();item.saved.resolve();await item.after.promise;await route.fulfill({response});
  });
  const writeZoom=async text=>{await root.getByRole('button',{name:/Write in this DM/}).click();await root.locator('#write-body').fill(text);};
  const msg=text=>skin+': '+text;
  const start=async raw=>{const text=msg(raw);current=null;if(skin==='zoom'){await writeZoom(text);await root.getByRole('button',{name:'Send',exact:true}).click();}else{await box.fill(text);await send.click();}await p.waitForFunction(()=>true);for(let i=0;!current&&i<100;i++)await p.waitForTimeout(10);if(!current){await p.screenshot({path:path.join(evidence,skin+'-error.png')});throw Error(skin+' send missing: '+JSON.stringify(errors)+'; '+(await p.evaluate(()=>document.querySelector('#skin').shadowRoot.textContent)).slice(-2500));}return current;};
  const timeline=skin==='comic'?root.locator('[data-mid]'):skin==='zoom'?root.locator('.mini-chat'):root.locator('#timeline');
  const bodyCount=async text=>(skin==='comic'?timeline:timeline.locator('li')).filter({hasText:msg(text)}).count();
  const first=await start('P12 optimistic one');assert.equal(await box.inputValue(),'');assert.equal(await bodyCount('P12 optimistic one'),1);assert(await root.getByText('Sending…',{exact:true}).count());
  await p.screenshot({path:path.join(evidence,skin+'-sending.png')});
  first.before.resolve();await first.saved.promise;
  await p.waitForTimeout(100);assert.equal(await bodyCount('P12 optimistic one'),1,'push merges while HTTP response held');
  // A second send must be possible while the first response remains pending.
  const second=await start('P12 second while pending');assert.equal(await box.inputValue(),'');assert.equal(await bodyCount('P12 second while pending'),1);second.before.resolve();await second.saved.promise;second.after.resolve();first.after.resolve();
  await p.waitForTimeout(150);assert.equal(await bodyCount('P12 optimistic one'),1);assert.equal(await bodyCount('P12 second while pending'),1);
  await p.screenshot({path:path.join(evidence,skin+'-merged.png')});
  reject=true;const failed=await start('P12 restore this');assert.equal(await box.inputValue(),'');failed.before.resolve();await p.waitForFunction(text=>{const root=document.querySelector('#skin').shadowRoot;return [...root.querySelectorAll('textarea')].some(t=>t.value===text);},msg('P12 restore this'));
  assert.equal(await box.inputValue(),msg('P12 restore this'));assert(await root.getByText(/Not sent:/).count());if(skin==='zoom'){await writeZoom('');await root.getByRole('button',{name:'Cancel',exact:true}).click();}else await box.fill('');
  const newer=await start('P12 retry old');if(skin==='zoom'){await writeZoom('Newer draft stays');await root.getByRole('button',{name:'Cancel',exact:true}).click();}else await box.fill('Newer draft stays');newer.before.resolve();await root.getByRole('button',{name:'Retry',exact:true}).waitFor();assert.equal(await box.inputValue(),'Newer draft stays');
  await p.screenshot({path:path.join(evidence,skin+'-failed-new-draft.png')});
  reject=false;current=null;await root.getByRole('button',{name:'Retry',exact:true}).click();for(let i=0;!current&&i<100;i++)await p.waitForTimeout(10);assert(current,'Retry submitted captured message');assert.equal(current.request.body,msg('P12 retry old'));assert.equal(current.request.id,newer.request.id,'Retry retains correlation even after a lost save response');assert.equal(await box.inputValue(),'Newer draft stays','Retry preserves newer text');current.before.resolve();await current.saved.promise;current.after.resolve();await p.waitForTimeout(100);assert.equal(await bodyCount('P12 retry old'),1);
  assert.deepEqual(errors,[],skin+' page errors');console.log(skin+' immediate-clear, single-bubble, concurrent-send, restore, newer-draft, Retry PASS');await context.close();
 }} finally {await browser.close();}
 console.log('P12 rendered send PASS');
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
