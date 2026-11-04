const assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const BUDGET=1000;
(async()=>{
 const width=Number(process.env.Q_WIDTH),url=new URL(process.env.Q_URL),chat=process.env.Q_CHAT;
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 const timings={};
 try{
  const context=await browser.newContext({viewport:{width,height:900},isMobile:width<500,hasTouch:width<500});
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(String(e)));
  await page.goto(url.href);await page.goto(url.origin+'/?skin=comic');
  const root=page.locator('#skin');
  const composer=root.locator('form[aria-label="Write a message"]');
  const openChat=async()=>{if(!await composer.isVisible().catch(()=>false))await root.getByRole('button').filter({hasText:chat}).first().click({timeout:15000});await composer.waitFor({state:'visible',timeout:15000});};
  await openChat();
  await root.getByText('first inbound',{exact:false}).first().waitFor({timeout:15000});
  const field=composer.getByRole('textbox');
  const send=composer.getByRole('button',{name:/^Send/});
  const control=async path=>{const r=await page.request.get(url.origin+path);assert.ok(r.ok(),path+': '+await r.text());};
  const timed=async(name,act,check)=>{const t0=Date.now();await act();await check(BUDGET);const took=Date.now()-t0;timings[name]=took;assert.ok(took<BUDGET,name+' took '+took+' ms');};
  const row=text=>root.locator('[data-mid]').filter({hasText:text});
  const value=()=>field.inputValue();
  let order=[];
  const read=async()=>{order=await root.locator('[data-mid]').allTextContents();};
  const at=s=>order.findIndex(t=>t.includes(s));
  const shownWords=()=>page.evaluate(()=>{const walk=n=>{let s='';for(const c of n.childNodes){if(c.nodeType===3)s+=c.textContent;else if(c.nodeType===1&&!(c.tagName==='DETAILS'&&!c.open)){s+=' '+walk(c);if(c.shadowRoot)s+=' '+walk(c.shadowRoot);}}return s;};const skin=document.querySelector('#skin');return walk(skin.shadowRoot||skin);});
  const noRequestWords=async()=>{const shown=await shownWords();for(const bad of ['context deadline','/v1/persons','chain?after','Your text and files are restored'])assert.ok(!shown.includes(bad),'primary text shows "'+bad+'"');};

  // The message stays, in the chat, as soon as it is sent; the composer stays clear.
  await field.fill('kept through a stall');
  await timed('send-visible',()=>send.click(),async b=>{await row('kept through a stall').first().waitFor({timeout:b});});
  assert.equal(await value(),'','composer cleared');
  await page.waitForTimeout(1500);
  assert.equal(await row('kept through a stall').count(),1,'one row while the request is held');
  assert.equal(await value(),'','no text handed back while waiting');
  await noRequestWords();

  // Other controls do not wait for it.
  await field.fill('cancel this one');
  await timed('second-send-visible',()=>send.click(),async b=>{await row('cancel this one').first().waitFor({timeout:b});});
  await field.fill('draft stays');

  // Cancel is the person's own act: gone at once, nothing else moved, a newer draft untouched.
  const discard=row('cancel this one').locator('[data-send-discard]');
  await discard.waitFor({timeout:10000});
  await timed('cancel-gone',()=>discard.click(),async b=>{await row('cancel this one').first().waitFor({state:'detached',timeout:b});});
  assert.equal(await row('kept through a stall').count(),1);
  assert.equal(await value(),'draft stays','a newer draft is not overwritten');
  await field.fill('');

  // A stopped message says so in words, offers its way out, keeps the cause behind Details.
  await field.fill('stopped then retried');
  await send.click();
  await row('stopped then retried').first().waitFor({timeout:BUDGET});
  await page.waitForTimeout(400);
  await control('/__test/stop?body='+encodeURIComponent('stopped then retried'));
  const stopped=row('stopped then retried');
  await stopped.locator('[data-local-send="stopped"]').first().waitFor({timeout:10000});
  assert.ok((await stopped.first().textContent()).includes('This person cannot receive messages now.'),'plain reason');
  await noRequestWords();
  const details=stopped.locator('[data-send-diagnostic]');
  await details.locator('summary').click();
  assert.ok((await details.textContent()).includes('/v1/persons'),'cause on request');
  await details.locator('summary').click();
  await noRequestWords();
  assert.equal(await stopped.locator('[data-send-discard]').count(),1,'can be discarded');
  const retry=stopped.locator('[data-send-retry]');
  await timed('retry-feedback',()=>retry.click(),async b=>{await stopped.locator('[data-local-send="stopped"]').first().waitFor({state:'detached',timeout:b});});
  assert.equal(await stopped.count(),1,'retry is the same message');

  // A newer message from them, sent during the stall. It waits for the same
  // rosters: nothing is shown on a roster that cannot be checked.
  await control('/__test/inbound');

  // The attempt runs out of its time, as in the incident: still waiting, still
  // in place, the cause only under Details. It is not cancelled and no text returns.
  const cause=row('kept through a stall').locator('[data-send-diagnostic]');
  await cause.waitFor({timeout:45000});
  assert.equal(await row('kept through a stall').locator('[data-local-send="stopped"]').count(),0,'a passing failure does not stop it');
  assert.equal(await value(),'','no text handed back after the attempt ran out');
  await noRequestWords();

  // Rosters answer, the sending itself stays stalled: as in the incident, their
  // later message comes in while mine still waits, and comes in below it.
  await control('/__test/open-rosters');
  await row('later inbound').first().waitFor({timeout:30000});
  await read();
  assert.ok(at('first inbound')<at('kept through a stall')&&at('kept through a stall')<at('stopped then retried')&&at('stopped then retried')<at('later inbound'),'order '+order.join(' | '));
  assert.equal(await row('kept through a stall').count(),1,'one row while the sending waits');
  assert.equal(await row('stopped then retried').count(),1,'one retried row while the sending waits');

  // A reload keeps it in view, in place.
  await page.reload();await openChat();
  await row('kept through a stall').first().waitFor({timeout:15000});
  await row('later inbound').first().waitFor({timeout:15000});
  await read();
  assert.ok(at('kept through a stall')<at('later inbound'),'place kept after reload');
  await noRequestWords();

  // The connection answers: the same rows settle, once each, in the same place.
  await control('/__test/open');
  await row('kept through a stall').locator('[data-local-send]').first().waitFor({state:'detached',timeout:60000});
  await row('stopped then retried').locator('[data-local-send]').first().waitFor({state:'detached',timeout:60000});
  assert.equal(await row('kept through a stall').count(),1,'one message after it settled');
  assert.equal(await row('stopped then retried').count(),1,'one retried message after it settled');
  await read();
  assert.ok(at('first inbound')<at('kept through a stall')&&at('kept through a stall')<at('later inbound'),'place kept after it settled');
  await noRequestWords();
  assert.deepEqual(errors,[]);
  console.log('queued send rendered PASS',width,JSON.stringify(timings));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
