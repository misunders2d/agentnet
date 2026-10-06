const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const skin=process.env.P23_SKIN,mode=process.env.P23_MODE || 'device',width=Number(process.env.P23_WIDTH),shots=process.env.AGENTNET_SCREENSHOTS;
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try {
  const context=await browser.newContext({viewport:{width,height:900},isMobile:width<500,hasTouch:width<500});
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(String(e)));
  const url=new URL(process.env.P23_URL);await page.goto(url.href);await page.goto(url.origin+'/?skin='+skin);
  const root=page.locator('#skin');
  // Comic and narrow Classic/Zoom start on their list; wide native skins
  // can already have opened the one available conversation.
  const doit=root.getByRole('button',{name:'Do it',exact:true});
  if(!await doit.isVisible().catch(()=>false)) {
   const chat=root.getByRole('button').filter({hasText:new RegExp(process.env.P23_PEER.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')+'|CHANGELOG|Not linked to a person|Laptop')}).first();
   try { await chat.click({timeout:5000}); } catch(e) {
    if(shots){fs.mkdirSync(shots,{recursive:true,mode:0o700});await page.screenshot({path:path.join(shots,'p23-'+mode+'-'+skin+'-'+width+'-error.png')});}
    console.error((await page.evaluate(()=>document.querySelector('#skin').shadowRoot.textContent)).slice(-10000));throw e;
   }
   if(skin==='zoom' && !await doit.isVisible().catch(()=>false)) await root.locator('.thread-list button').first().click();
  }
  await doit.waitFor({state:'visible',timeout:15000});
  const composer=skin==='comic'?root.locator('form[aria-label="Write a message"]'):root.locator('#composer');
  assert.equal(await composer.getByRole('radio').count(),0,'no composer intent toggle');
  assert.equal(await composer.getByRole('button',{name:/Do it/}).count(),0,'no up-front execution');
  assert.ok(await root.getByText('Update CHANGELOG.md.',{exact:false}).count(),'proposal shown');
  await page.waitForTimeout(450);
  // Live Zoom redraws can detach a Playwright element between selection and
  // measurement. Select and measure together, after navigation motion settles.
  const measured=await page.waitForFunction(()=>{
   const shadow=document.querySelector('#skin')?.shadowRoot;
   if(!shadow)return false;
   if(shadow.querySelector('#zoom')?.getAnimations({subtree:true}).some(a=>a.playState==='running'))return false;
   const buttons=[...shadow.querySelectorAll('button')].filter(b=>b.textContent.trim()==='Do it' && b.getClientRects().length && getComputedStyle(b).visibility==='visible');
   if(buttons.length!==1)return false;
   const r=buttons[0].getBoundingClientRect();
   return r.width>0 && r.height>0 ? {x:r.x,y:r.y,width:r.width,height:r.height} : false;
  },undefined,{timeout:5000});
  const box=await measured.jsonValue();await measured.dispose();
  assert.ok(box && box.x>=0 && box.x+box.width<=width && box.y>=0 && box.y+box.height<=900,'Do it fits the viewport: '+JSON.stringify(box));
  if(shots){fs.mkdirSync(shots,{recursive:true,mode:0o700});await page.screenshot({path:path.join(shots,'p23-'+mode+'-'+skin+'-'+width+'-proposal.png')});}
  // Dispatch both clicks in the same turn; native persistence must dedup.
  await doit.evaluate(b=>{b.click();b.click()});
  await doit.waitFor({state:'hidden',timeout:15000});
  await page.reload();
  await page.waitForTimeout(500);
  assert.equal(await root.getByRole('button',{name:'Do it',exact:true}).count(),0,'confirmed task stays confirmed after reload');
  assert.deepEqual(errors,[],'no page errors');
  if(shots)await page.screenshot({path:path.join(shots,'p23-'+mode+'-'+skin+'-'+width+'-confirmed.png')});
  console.log('P23 rendered proposal PASS '+mode+' '+skin+' '+width);
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
