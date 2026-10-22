const assert=require('node:assert/strict'),fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  for(const skin of ['comic']) for(const width of [1280,390]) {
   const ctx=await browser.newContext({reducedMotion:'reduce',viewport:{width,height:900}}),p=await ctx.newPage(),errors=[],posted=[];
   const archived=new Set(),trustPeer='fixture/changed-key',trustThread='9'.repeat(32),pendingKey='33333333-33333333-33333333-33333333';
   let handled=false,reportDone=false,failReport=true;p.setDefaultTimeout(6000);p.on('pageerror',e=>errors.push(String(e)));
   await p.route('**/api/overview*',async route=>{
    const response=await route.fetch(),o=await response.json();
    o.needs_you=[];o.held=[...(!handled?[{id:'held-one',excerpt:'Synthetic first human question'}]:[]),{id:'held-two',excerpt:'Synthetic second human question'}].map(x=>({...x,conv:'needs-chat',peer:'bob/desk',kind:'question',reason:'person_turn',at:new Date().toISOString(),why:'Held for you: nothing runs it.',actions:['resolve']}));
    o.held.push({...o.held.at(-1),id:"read-only",excerpt:"Synthetic older-host turn",actions:[]});
    o.quarantine=[{id:'invalid',peer:'bob/desk',code:'invalid',detail:'Invalid synthetic record',can_archive:true,at:new Date().toISOString()},{id:'pending',peer:'bob/desk',code:'proof_pending',detail:'Synthetic proof is still pending',can_archive:false,at:new Date().toISOString()}]
    o.quarantine.push({id:'changed-key-notice',peer:trustPeer,code:'key_changed',sender_verified:true,detail:'Exact synthetic changed key requires comparison',can_archive:true,at:new Date().toISOString()});
    o.quarantine=o.quarantine.filter(q=>!archived.has(q.id));
    o.threads=[...(o.threads||[]),{id:trustThread,peer:trustPeer,title:'Synthetic changed identity',state:'active',count:1,unread:0,last_at:new Date().toISOString()}];
    o.review=reportDone?[]:[{id:'report',peer:'alice/laptop',kind:'message',notice:true,at:new Date().toISOString(),report:{at:1791540000,host:'alice/laptop',count:1,items:[]}}];
    await route.fulfill({response,json:o});
   });
   await p.route('**/api/act',async route=>{
    const a=route.request().postDataJSON();posted.push(a);
    if(a.do==='resolve'&&a.id==='report'&&failReport){await route.fulfill({status:409,json:{error:'Synthetic dismissal failed'}});return;}
    if(a.do==='archive_held_batch'){for(const h of a.held||[])archived.add(h.id);await route.fulfill({json:{note:'Archived '+(a.held||[]).length+' notices on this device.'}});return;}
    assert.notEqual(a.do,'trust','this rendered coverage must never trust a synthetic key');
    if(a.do==='resolve'&&a.id==='held-one')handled=true;
    if(a.do==='resolve'&&a.id==='report')reportDone=true;
    await route.fulfill({json:{note:'Synthetic item handled on this device.'}});
   });
   await p.route('**/api/thread?*',async route=>{
    if(new URL(route.request().url()).searchParams.get('id')!==trustThread)return route.continue();
    await route.fulfill({json:{id:trustThread,peer:trustPeer,key:{pinned:'22222222-22222222-22222222-22222222',pending:pendingKey},messages:[]}});
   });
   if(process.env.AGENTNET_HELD_ASSETS){const path=require('node:path'),dir=process.env.AGENTNET_HELD_ASSETS;await p.route('**/assets/skins/comic/**',async route=>{const rel=new URL(route.request().url()).pathname.split('/assets/skins/comic/')[1];if(!rel||rel.includes('..'))throw Error('Invalid fixture asset path');const file=path.join(dir,rel);if(fs.existsSync(file))return route.fulfill({path:file,contentType:rel.endsWith('.mjs')?'text/javascript':rel.endsWith('.css')?'text/css':undefined});await route.continue();});}
   const open=async()=>{await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Review/}).click();};
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
    // Background records are diagnostics, never an approval or archive chore.
    assert.equal(await p.getByText('Chat sync needs attention',{exact:false}).count(),0,'background status is absent from OKs');
    assert.equal(await p.getByRole('button',{name:'Archive this notice',exact:true}).count(),0,'background notices are not tasks in OKs');
    await p.getByRole('button',{name:/Reports from other computers/}).click();
    const dismiss=p.getByRole('button',{name:'Dismiss this report',exact:true});await dismiss.click();
    await p.getByText('Synthetic dismissal failed',{exact:false}).filter({visible:true}).waitFor();
    assert.equal(await p.getByText(/Dismissed here\. A newer report/).count(),0,'no false success on failed dismiss');
    assert.equal(await dismiss.isDisabled(),false,'failure enables retry');
    failReport=false;await dismiss.click();
    await p.getByText(/Dismissed here\. A newer report/).filter({visible:true}).waitFor();
    // A real supported local key decision belongs in Review; merely opening
    // its comparison sheet never trusts the key or runs received content.
    const trustCard=p.locator('li').filter({hasText:'Exact synthetic changed key requires comparison'}).last();
    await trustCard.getByRole('button',{name:'Check and trust…',exact:true}).click();
    const trust=p.getByRole('dialog').filter({hasText:'New code'});await trust.waitFor();
    assert.equal(await trust.getByRole('button',{name:'Trust the new identity',exact:true}).isDisabled(),true);
    assert((await trust.innerText()).includes(pendingKey),'comparison shows the exact pending fingerprint');
    await trust.getByRole('button',{name:'Not now',exact:true}).click();await trust.waitFor({state:'hidden'});
    assert.equal(posted.some(a=>a.do==='trust'),false,'inspection/cancel grants nothing');
    const archivedResponse=p.waitForResponse(r=>r.url().endsWith('/api/act')&&r.request().postDataJSON()?.do==='archive_held_batch');
    await trustCard.getByRole('button',{name:'Archive notice',exact:true}).click();await archivedResponse;
    await trustCard.waitFor({state:'hidden'});
    assert.deepEqual(posted.filter(a=>a.do==='archive_held_batch').at(-1),{do:'archive_held_batch',held:[{id:'changed-key-notice',reason:'key_changed',detail_code:''}]});
    // Invalid/pending records live in Settings diagnostics, not approval cards.
    await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:width>=1024?'Settings':'You',exact:true}).click();
    await p.getByRole('button',{name:/^About/}).click();
    await p.getByText('Technical details',{exact:true}).click();
    await p.getByText(/^Retained sync records/).click();
    await p.getByText('Synthetic proof is still pending',{exact:true}).waitFor();
    const invalid=p.locator('li').filter({hasText:'Invalid synthetic record'}).filter({has:p.getByRole('button',{name:'Archive this notice',exact:true})}).last();
    await invalid.getByRole('button',{name:'Archive this notice',exact:true}).click();
    await invalid.waitFor({state:'hidden'});await p.getByText('Synthetic proof is still pending',{exact:true}).waitFor();
    assert.deepEqual(posted.filter(a=>a.do==='archive_held_batch').at(-1),{do:'archive_held_batch',held:[{id:'invalid',reason:'invalid',detail_code:''}]},'only exact archiveable diagnostic notice hidden');
    assert.equal(archived.has('pending'),false,'unarchiveable pending context remains retained');
    assert(posted.every(a=>a.do==='resolve'||a.do==='archive_held_batch'),'review/diagnostic inspection starts no execution or permission change');

   }
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:`${process.env.AGENTNET_SCREENSHOTS}/held-${skin}-${width}.png`});}
   assert.deepEqual(errors,[],'runtime errors');console.log(`${skin} ${width} PASS`);
   await p.unrouteAll({behavior:'wait'});await ctx.close();
  }
 }finally{await browser.close();}
 console.log('Held person turn rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
