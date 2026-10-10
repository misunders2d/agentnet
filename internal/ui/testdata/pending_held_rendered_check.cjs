const assert=require('node:assert/strict'),fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const settle=async p=>p.evaluate(async()=>{await document.fonts.ready;const root=document.querySelector('#skin').shadowRoot.querySelector('.skin-root');await Promise.all(root.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  for(const skin of ['comic','classic','zoom']) for(const width of [1280,390]) {
   const ctx=await browser.newContext({reducedMotion:'reduce',viewport:{width,height:1000}}),p=await ctx.newPage(),errors=[],posted=[];
   // Comic keeps background notices under "Chat sync needs attention" and
   // archives exact snapshots (archive_held_batch); the others, one notice.
   const want=skin==='comic'?{do:'archive_held_batch',held:[{id:'pending',reason:'proof_pending',detail_code:''}]}:{do:'archive_held',id:'pending'};
   let archived=false,fail=true;p.setDefaultTimeout(6000);p.on('pageerror',e=>errors.push(String(e)));
   await p.route('**/api/overview*',async route=>{
    const response=await route.fetch(),o=await response.json();o.needs_you=[];o.held=[];o.review=[];
    o.quarantine=[...(!archived?[{id:'pending',code:'proof_pending',can_archive:true,detail:'Synthetic missing chat context',recovery:'You can archive this notice. Checks continue when connected; the message appears when verified.'}]:[]),{id:'key',code:'key_changed',detail:'Synthetic changed device key',can_archive:false}].map(x=>({...x,peer:'bob/desk',at:new Date().toISOString()}));
    await route.fulfill({response,json:o});
   });
   await p.route('**/api/act',async route=>{
    const a=route.request().postDataJSON();posted.push(a);
    if(fail){await route.fulfill({status:409,json:{error:'Synthetic archive failed'}});return;}
    assert.deepEqual(a,want);archived=true;await route.fulfill({json:{note:'Notice archived locally. The retained message has not been accepted or run.'}});
   });
   const open=async()=>{await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);if(skin==='comic'){await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();if(!archived)await p.locator('summary').filter({hasText:'Chat sync needs attention'}).click();}else {await p.locator('#profile-btn').click();await p.locator('[data-settings="device"]').click();await p.locator('#quarantine-summary').click();}};
   await open();
   const archive=p.getByRole('button',{name:skin==='comic'?'Archive this notice':'Archive notice',exact:true});await archive.waitFor();
   assert.equal(await archive.count(),1,'only pending-context notice can archive');assert.equal(posted.length,0,'reading never archives');
   await p.getByText('Checks continue when connected; the message appears when verified.',{exact:false}).filter({visible:true}).waitFor();
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await settle(p);await p.screenshot({path:`${process.env.AGENTNET_SCREENSHOTS}/pending-held-before-${skin}-${width}.png`});}
   await archive.click();await p.getByText('Synthetic archive failed',{exact:false}).filter({visible:true}).waitFor();
   assert.equal(await archive.isDisabled(),false,'failure permits explicit retry');assert.equal(archived,false);
   fail=false;await archive.click();await open();
   assert.equal(await p.getByText('Synthetic missing chat context',{exact:false}).filter({visible:true}).count(),0,'chosen notice stays hidden on reload');
   await p.getByText('Synthetic changed device key',{exact:false}).filter({visible:true}).waitFor();
   assert.deepEqual(posted,[want,want],'only chosen protected action was submitted');
   assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'no page overflow');
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await settle(p);await p.screenshot({path:`${process.env.AGENTNET_SCREENSHOTS}/pending-held-${skin}-${width}.png`});}
   assert.deepEqual(errors,[]);console.log(`${skin} ${width} PASS`);await p.unrouteAll({behavior:'wait'});await ctx.close();
  }
 }finally{await browser.close();}
 console.log('Pending held notice rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
