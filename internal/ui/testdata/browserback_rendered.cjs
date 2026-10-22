const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{
  const origin=new URL(process.argv[2]).origin;
  for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];
   page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(e.message));
   await page.route(origin+'/outside',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Outside fixture</title>Earlier document'}));
   await page.goto(origin+'/outside');await page.goto(process.argv[2]);
   const loaded=page.url();
   await page.reload();await page.getByRole('button',{name:'New',exact:true}).waitFor();
   await page.getByRole('button',{name:'New',exact:true}).click();await page.getByRole('dialog',{name:'New chat',exact:true}).waitFor();
   await page.goBack();await page.getByRole('dialog',{name:'New chat',exact:true}).waitFor({state:'detached'});assert.equal(page.url(),loaded);
   await page.locator('[data-part="chat-row"]').first().click();await page.locator('[data-part="conversation"]').waitFor();
   await page.goBack();await page.locator('[data-part="conversation"]').waitFor({state:'detached'});assert.equal(page.url(),loaded);
   await page.goBack();await page.waitForURL(origin+'/outside');assert.deepEqual(errors,[]);
   console.log(JSON.stringify({width,sheet:'dismissed',chat:'closed',nextBack:'earlier document',errors}));
   await context.close();
  }
  // A direct tab has no earlier document: Back must not disable later handling.
  const context=await browser.newContext(),page=await context.newPage();
  await context.addCookies([{name:'agentnet_ui',value:new URL(process.argv[2]).searchParams.get('t'),url:origin,httpOnly:true,sameSite:'Strict'}]);
  await page.route(origin+'/back-direct',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Direct tab fixture</title>'}));
  await page.goto(origin+'/back-direct');
  const session=await context.newCDPSession(page);await session.send('Page.resetNavigationHistory');await session.detach();
  await page.evaluate(async()=>{const {browserBackNavigation}=await import('/assets/browser-back.mjs');window.layers=0;window.calls=0;window.off=browserBackNavigation()( ()=>{calls++;if(layers){layers--;return true;}return false;});});
  await page.goBack();await page.waitForFunction(()=>window.calls===1);await page.evaluate(()=>layers=1);await page.goBack();await page.waitForFunction(()=>calls===2&&layers===0);
  await page.evaluate(()=>{off();window.layers=1;});await page.goBack();assert.equal(await page.evaluate(()=>layers),1,'unmounted skin receives no Back action');await context.close();
  console.log('browser Back PASS');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
