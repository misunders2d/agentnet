const assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{for(const width of [320,390,1280]){
  const context=await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(e.message));
  await page.goto(process.argv[2]);
  const first=page.locator('[data-part="chat-row"]').first();await first.waitFor();const title=await first.locator('[data-part="chat-title"]').innerText();
  await page.keyboard.press('Control+k');const search=page.getByPlaceholder('Search people, agents, groups');await search.fill(title);
  await search.press('ArrowDown');assert(await page.locator('[data-part="chat-row"]').first().evaluate(e=>e.getRootNode().activeElement===e),'Down enters matching rows');
  await page.keyboard.press('ArrowUp');assert(await search.evaluate(e=>e.getRootNode().activeElement===e),'Up returns to search');
  await search.press('Enter');await page.locator('[data-part="conversation"]').waitFor();
  await page.goBack();await page.locator('[data-part="conversation"]').waitFor({state:'detached'});
  await page.getByRole('button',{name:'New',exact:true}).click();const sheet=page.getByRole('dialog',{name:'New chat',exact:true});await sheet.waitFor();
  const input=sheet.getByRole('searchbox');await input.fill('Vitalii');const choice=sheet.locator('[data-new-chat-choice]:not(:disabled)').first();await choice.waitFor();
  await input.press('ArrowDown');assert(await choice.evaluate(e=>e.getRootNode().activeElement===e),'New chat Down targets exact choice');await page.keyboard.press('ArrowUp');assert(await input.evaluate(e=>e.getRootNode().activeElement===e));
  await input.press('Enter');await sheet.waitFor({state:'detached'});await page.locator('[data-part="conversation"]').waitFor();
  const more=page.getByRole('button',{name:'More',exact:true});await more.click();await page.getByRole('menuitem',{name:'In this chat',exact:true}).click();
  const panel=page.getByRole(width<1024?'dialog':'complementary',{name:'In this chat',exact:true});await panel.waitFor();await page.keyboard.press('Escape');await panel.waitFor({state:'detached'});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'viewport does not overflow');assert.deepEqual(errors,[]);
  if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'navigation-keys-'+width+'.png')});}
  console.log(JSON.stringify({width,searchEnter:true,searchArrows:true,newChatEnter:true,newChatArrows:true,panelEscape:true,errors}));await context.close();
 }}finally{await browser.close();}
 console.log('navigation keys PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
