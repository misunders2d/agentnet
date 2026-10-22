const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM,headless:true,args:['--no-sandbox']});
 try {for(const width of [390,1280]){
  const context=await browser.newContext({reducedMotion:'reduce',viewport:{width,height:1000}}),p=await context.newPage(),errors=[],decisions=[];
  p.setDefaultTimeout(10000);
  p.on('pageerror',e=>errors.push(String(e)));p.on('console',m=>{if(m.type()==='error')console.error(m.text());});p.on('response',r=>{if(r.status()>=400)console.error(r.status(),r.url());});
  await p.route('**/api/operator/decide',async route=>{decisions.push(route.request().postDataJSON());await route.fulfill({json:{id:'saved-decision',state:'custody',note:'Decision queued.'}});});
  // A legacy request has no concrete current agent binding. Exercise its
  // public notification route without inventing an active device-agent row.
  await p.goto(process.argv[2]+'#msg='+'1'.repeat(32));
  const card=p.locator('section[aria-labelledby]').filter({has:p.locator('[data-agent-needs-you]')});
  await card.getByText('Professional memory unavailable; provide Bernard’s surname.',{exact:true}).waitFor();
  assert.ok((await card.innerText()).includes('On Bezos'),'remote executor retains its host');
  assert.ok(!(await card.innerText()).includes('On Zenbook'),'local device is not executor');
  await p.evaluate(async()=>{await document.fonts.ready;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));const root=document.querySelector('#skin').shadowRoot.querySelector('.skin-root');await Promise.all(root.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
  assert.equal(await card.evaluate(e=>e.scrollWidth>e.clientWidth+1),false,'card wraps at width '+width);
  const bounds=await card.boundingBox();assert.ok(bounds.x>=-1&&bounds.x+bounds.width<=width+1,'card fits viewport '+width);
  if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`remote-needs-human-${width}.png`)});}
  await card.getByRole('button',{name:'Reply',exact:true}).click();
  await p.getByRole('textbox',{name:'Your answer'}).fill('Bernard is our operations colleague.');
  await p.getByRole('button',{name:'Send answer',exact:true}).click();
  await p.getByRole('dialog').waitFor({state:'hidden'});
  await card.getByRole('button',{name:'Mark as handled',exact:true}).click();
  const dialog=p.getByRole('dialog');await dialog.getByRole('button',{name:'Mark as handled',exact:true}).click();
  await dialog.waitFor({state:'hidden'});
  assert.equal(decisions.length,3,'one continuation plus resolve check and submit');
  for(const d of decisions){assert.equal(d.host,'owner/bezos');assert.equal(d.id,'11111111111111111111111111111111');assert.equal(d.key,'verified-key');assert.equal(d.attempt,2);assert.equal(d.expect,'needs_human');}
  assert.equal(decisions[0].action,'continue');assert.equal(decisions[0].text,'Bernard is our operations colleague.');
  assert.equal(decisions[1].check,true);assert.equal(decisions[2].action,'resolve');
  assert.equal(errors.length,0,errors.join('\n'));await context.close();
 }}finally{await browser.close();}
 console.log('Remote needs-human card rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
