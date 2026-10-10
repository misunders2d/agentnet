const assert=require('node:assert/strict'),fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  for(const skin of ['comic','classic','zoom']) for(const width of [1280,390]) {
   const ctx=await browser.newContext({reducedMotion:'reduce',viewport:{width,height:900}}),p=await ctx.newPage(),errors=[],posted=[];
   let handled=false,reportDone=false,failReport=true;p.setDefaultTimeout(6000);p.on('pageerror',e=>errors.push(String(e)));
   await p.route('**/api/overview*',async route=>{
    const response=await route.fetch(),o=await response.json();
    o.needs_you=[];o.held=[...(!handled?[{id:'held-one',excerpt:'Synthetic first human question'}]:[]),{id:'held-two',excerpt:'Synthetic second human question'}].map(x=>({...x,conv:'needs-chat',peer:'bob/desk',kind:'question',reason:'person_turn',at:new Date().toISOString(),why:'Held for you: nothing runs it.',actions:['resolve']}));
    o.held.push({...o.held.at(-1),id:"read-only",excerpt:"Synthetic older-host turn",actions:[]});
    o.quarantine=[{id:'invalid',peer:'bob/desk',code:'invalid',detail:'Invalid synthetic record',can_archive:true,at:new Date().toISOString()},{id:'pending',peer:'bob/desk',code:'proof_pending',detail:'Synthetic proof is still pending',can_archive:false,at:new Date().toISOString()}];
    o.review=reportDone?[]:[{id:'report',peer:'alice/laptop',kind:'message',notice:true,at:new Date().toISOString(),report:{at:1791540000,host:'alice/laptop',count:1,items:[]}}];
    await route.fulfill({response,json:o});
   });
   await p.route('**/api/act',async route=>{
    const a=route.request().postDataJSON();posted.push(a);
    if(a.do==='resolve'&&a.id==='report'&&failReport){await route.fulfill({status:409,json:{error:'Synthetic dismissal failed'}});return;}
    if(a.do==='resolve'&&a.id==='held-one')handled=true;
    if(a.do==='resolve'&&a.id==='report')reportDone=true;
    await route.fulfill({json:{note:'Synthetic item handled on this device.'}});
   });
   const open=async()=>{await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);if(skin==='comic')await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();else await p.locator('#review-btn').click();};
   await open();
   const card=p.locator('li').filter({hasText:'Synthetic first human question'}).filter({has:p.getByRole('button',{name:'Mark as handled',exact:true})}).last();
   await card.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
   assert.equal(posted.filter(x=>x.do==='resolve').length,0,'opening/reading does not resolve');
   assert.equal(await p.getByRole('button',{name:'Mark as handled',exact:true}).count(),2,'only advertised local resolve actions shown');
   await card.getByRole('button',{name:'Mark as handled',exact:true}).click();
   const dialog=p.getByRole('dialog');await dialog.getByText('The message stays in the chat.',{exact:false}).waitFor();
   await dialog.getByRole('button',{name:'Cancel',exact:true}).click();
   assert.equal(posted.filter(x=>x.do==='resolve').length,0,'cancel leaves turn waiting');
   await card.getByRole('button',{name:'Mark as handled',exact:true}).click();
   const response=p.waitForResponse(r=>r.url().endsWith('/api/act')&&r.request().postDataJSON()?.do==='resolve');
   await p.getByRole('dialog').getByRole('button',{name:'Mark as handled',exact:true}).click();await response;
   await open();
   assert.equal(await p.getByText('Synthetic first human question',{exact:false}).filter({visible:true}).count(),0,'handled card stays removed after reload');
   await p.getByText('Synthetic second human question',{exact:false}).filter({visible:true}).waitFor();
   assert.deepEqual(posted.filter(x=>x.do==='resolve'),[{do:'resolve',id:'held-one'}],'only exact chosen turn resolved');
   assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'no page overflow');
   if(skin==='comic') {
    // Background notices sit under "Chat sync needs attention", one archive per archivable group.
    await p.locator('summary').filter({hasText:'Chat sync needs attention'}).click();
    assert.equal(await p.getByRole('button',{name:'Archive this notice',exact:true}).count(),1,'security hold gains no archive');
    await p.getByRole('button',{name:/Reports from other computers/}).click();
    const dismiss=p.getByRole('button',{name:'Dismiss this report',exact:true});await dismiss.click();
    await p.getByText('Synthetic dismissal failed',{exact:false}).filter({visible:true}).waitFor();
    assert.equal(await p.getByText(/Dismissed here\. A newer report/).count(),0,'no false success on failed dismiss');
    assert.equal(await dismiss.isDisabled(),false,'failure enables retry');
    failReport=false;await dismiss.click();
    await p.getByText(/Dismissed here\. A newer report/).filter({visible:true}).waitFor();
   }
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:`${process.env.AGENTNET_SCREENSHOTS}/held-${skin}-${width}.png`});}
   assert.deepEqual(errors,[],'runtime errors');console.log(`${skin} ${width} PASS`);
   await p.unrouteAll({behavior:'wait'});await ctx.close();
  }
 }finally{await browser.close();}
 console.log('Held person turn rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
