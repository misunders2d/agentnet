const assert = require('node:assert/strict');
const engines = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core'), engine = process.env.AGENTNET_INTERACTION_ENGINE || 'chromium';
(async()=>{
 const browser=await engines[engine].launch(engine==='chromium'?{headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'}:{headless:!process.env.AGENTNET_INTERACTION_HEADED});
 try{
  const origin=new URL(process.argv[2]).origin;
  for(const width of [1280,390]){
   const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];
   page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(e.message));
   await page.route(origin+'/outside',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Outside fixture</title>Earlier document'}));
   await page.goto(origin+'/outside');await page.goto(process.argv[2]);
   const loaded=page.url();
   await page.getByRole('button',{name:'New',exact:true}).waitFor(); // a reload during loading aborts its imports
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
  const context=await browser.newContext();let page=await context.newPage();
  await context.addCookies([{name:'agentnet_ui',value:new URL(process.argv[2]).searchParams.get('t'),url:origin,httpOnly:true,sameSite:'Strict'}]);
  await context.route(origin+'/back-direct',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Direct tab fixture</title>'}));
  if(engine==='webkit'){
   // As the Chromium reset below, for WebKit (no CDP): a tab the browser opens at its URL has no earlier entry.
   const opener=page;[page]=await Promise.all([context.waitForEvent('page'),opener.evaluate(u=>{window.open(u,'_blank','noopener');},origin+'/back-direct')]);
   await page.waitForURL(origin+'/back-direct');await opener.close();
  }else await page.goto(origin+'/back-direct');
  if(engine==='chromium'){const session=await context.newCDPSession(page);await session.send('Page.resetNavigationHistory');await session.detach();}
  // A person uses the page before its history entries are written, as one opening a layer does: WebKit's
  // Back skips entries a script added without user interaction (WebKit e7faecc4, wasCreatedByJSWithoutUserInteraction).
  const use=()=>page.mouse.click(1,1);
  await use();await page.evaluate(async()=>{const {browserBackNavigation}=await import('/assets/browser-back.mjs');window.layers=0;window.calls=0;window.off=browserBackNavigation()( ()=>{calls++;if(layers){layers--;return true;}return false;});});
  await use();await page.goBack();await page.waitForFunction(()=>window.calls===1);await page.evaluate(()=>layers=1);await use();await page.goBack();await page.waitForFunction(()=>calls===2&&layers===0);
  await page.evaluate(()=>{off();window.layers=1;});await use();await page.goBack();assert.equal(await page.evaluate(()=>layers),1,'unmounted skin receives no Back action');await context.close();
  {
   // A link or notification adds an entry; Back never does. Neither depends on history's length.
   const context=await browser.newContext(),page=await context.newPage();
   await context.addCookies([{name:'agentnet_ui',value:new URL(process.argv[2]).searchParams.get('t'),url:origin,httpOnly:true,sameSite:'Strict'}]);
   await page.route(origin+'/back-*',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Fragment fixture</title>'}));
   await page.goto(origin+'/back-earlier');await page.goto(origin+'/back-fragment');
   const use=()=>page.mouse.click(1,1); // as in the direct tab: a person has used the page before its entries are written
   await use();await page.evaluate(async()=>{const {browserBackNavigation}=await import('/assets/browser-back.mjs');window.layers=0;window.calls=0;browserBackNavigation()(()=>{calls++;if(layers){layers--;return true;}return false;});});
   const follow=async hash=>{await page.evaluate(hash=>new Promise(r=>{addEventListener('hashchange',r,{once:true});location.hash=hash;}),hash);};
   const clear=()=>page.evaluate(()=>history.replaceState(history.state,'',location.pathname+location.search)); // what the loader does on landing
   await page.evaluate(()=>{layers=2;});await follow('#one');await clear();assert.equal(await page.evaluate(()=>calls),0,'a followed link is not Back');
   await use();await page.goBack();await page.waitForFunction(()=>calls===1&&layers===1);
   await follow('#cut-forward');await clear();assert.equal(await page.evaluate(()=>calls),1,'a followed link that cuts Forward entries is not Back');
   await use();await page.goBack();await page.waitForFunction(()=>calls===2&&layers===0);
   for(let i=0;i<60;i++)await follow('#cap-'+i);await clear();assert.equal(await page.evaluate(()=>calls),2,'a followed link at the history cap is not Back');
   await page.evaluate(()=>{layers=1;});await use();await page.goBack();await page.waitForFunction(()=>calls===3&&layers===0);
   await context.close();
   console.log(JSON.stringify({fragment:'followed links leave the layer open; every Back reaches it'}));
  }
  {
   // A tab open across the update has an older version's entries, with no mark.
   const legacy=async(direct)=>{
    const context=await browser.newContext(),page=await context.newPage();
    await context.addCookies([{name:'agentnet_ui',value:new URL(process.argv[2]).searchParams.get('t'),url:origin,httpOnly:true,sameSite:'Strict'}]);
    await page.route(origin+'/back-*',r=>r.fulfill({contentType:'text/html',body:'<!doctype html><title>Legacy fixture</title>'}));
    if(!direct)await page.goto(origin+'/back-earlier');
    await page.goto(origin+'/back-legacy');
    if(direct){if(engine==='chromium'){const session=await context.newCDPSession(page);await session.send('Page.resetNavigationHistory');await session.detach();}}
    await page.mouse.click(1,1);await page.evaluate(()=>history.pushState({agentnetBack:'agentnet-back-legacy'},'',location.href));
    await page.reload();
    await page.mouse.click(1,1);await page.evaluate(async()=>{const {browserBackNavigation}=await import('/assets/browser-back.mjs');window.layers=1;window.calls=0;browserBackNavigation()(()=>{calls++;if(layers){layers--;return true;}return false;});});
    await page.mouse.click(1,1);await page.goBack();await page.waitForFunction(()=>calls===1&&layers===0);
    return {context,page};
   };
   const left=await legacy(false);
   await left.page.goBack({waitUntil:'commit'}).catch(()=>{});await left.page.waitForURL(origin+'/back-earlier');await left.context.close();
   const stayed=await legacy(true);
   await stayed.page.goBack();await stayed.page.waitForFunction(()=>calls===2);await stayed.page.waitForTimeout(1200);
   await stayed.page.mouse.click(5,5); // the guard returns with the next input
   await stayed.page.evaluate(()=>{layers=1;});await stayed.page.goBack();await stayed.page.waitForFunction(()=>calls===3&&layers===0);await stayed.context.close();
   console.log(JSON.stringify({legacy:'leaves past unmarked entries; a direct tab keeps handling Back'}));
  }
  console.log('browser Back PASS');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
