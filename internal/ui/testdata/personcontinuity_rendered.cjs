const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),assert=require('node:assert/strict'),path=require('node:path');
async function armOpen(page){await page.evaluate(()=>{
 const root=document.getElementById('skin').shadowRoot,t=window.mel736Open={input:0,feedback:null};
 root.addEventListener('click',()=>{if(!t.input)t.input=performance.now();requestAnimationFrame(check);},{capture:true,once:true});
 const check=()=>{if(!t.input||t.feedback!==null)return;for(const el of root.querySelectorAll('p,span,div')){
  if(!el.textContent.startsWith('Bohdan signed in again'))continue;const r=el.getBoundingClientRect();if(r.width&&r.height&&r.top<innerHeight&&r.bottom>0&&r.left<innerWidth&&r.right>0){t.feedback=performance.now()-t.input;observer.disconnect();break;}
 }};const observer=new MutationObserver(check);observer.observe(root,{subtree:true,childList:true,characterData:true,attributes:true});
 });}
async function opened(page){await page.waitForFunction(()=>window.mel736Open.feedback!==null);return page.evaluate(()=>window.mel736Open.feedback);}
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 let current;try{for(const width of [1280,390]){
  const page=await browser.newPage({viewport:{width,height:900}}),errors=[];current=page;page.on('pageerror',e=>errors.push(String(e)));
  await page.goto(process.env.MEL736_URL);await page.locator('section[aria-label="Chats"]').getByRole('button',{name:/Bohdan/}).first().waitFor();
  await armOpen(page);await page.locator('section[aria-label="Chats"]').getByRole('button',{name:/Bohdan/}).first().click();
  await page.getByText(/^Bohdan signed in again/).waitFor();const open=await opened(page);assert(open<1000,`open ${open}ms`);
  assert.equal(await page.getByText(/^Bohdan signed in again/).count(),1);await page.locator('[data-part="message"]').filter({hasText:'Current device message'}).first().waitFor();
  const back=page.getByRole('button',{name:'Back to chats',exact:true});
  if(await back.isVisible())await back.click();else{await page.getByRole('button',{name:'Outbox',exact:true}).click();await page.getByRole('button',{name:'Chats',exact:true}).first().click();}
  await armOpen(page);await page.locator('section[aria-label="Chats"]').getByRole('button',{name:/Bohdan/}).first().click();await page.getByText(/^Bohdan signed in again/).waitFor();const warm=await opened(page);assert(warm<1000,`warm open ${warm}ms`);
  await page.getByRole('textbox').last().fill('Continuity queued reply');
  // Measure from the actual input event, independently of Playwright's
  // action completion and DOM locator normalization of the message body.
  await page.evaluate(()=>{
   const root=document.getElementById('skin').shadowRoot,t=window.mel736Timing={input:0,feedback:null};
   root.addEventListener('click',()=>{if(!t.input)t.input=performance.now();},{capture:true,once:true});
   const observer=new MutationObserver(()=>{if(!t.input||t.feedback!==null)return;for(const el of root.querySelectorAll('[data-part="message"]')){
    const r=el.getBoundingClientRect();if(el.textContent.includes('Continuity queued reply')&&r.width&&r.height&&r.top<innerHeight&&r.bottom>0){t.feedback=performance.now()-t.input;observer.disconnect();break;}
   }});observer.observe(root,{subtree:true,childList:true,characterData:true});
  });
  await page.getByRole('button',{name:'Send',exact:true}).click();await page.waitForFunction(()=>window.mel736Timing.feedback!==null);
  const local=await page.evaluate(()=>window.mel736Timing.feedback);assert(local<1000,`pending feedback ${local}ms`);
  await page.getByText(/not confirmed|not submitted|unavailable|retry/i).first().waitFor();
  await page.locator('[data-part="message"]').filter({hasText:'Concurrent synchronized arrival'}).first().waitFor();
  const confirmed=await page.evaluate(()=>performance.now()-window.mel736Timing.input);
  await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`mel736-${process.env.MEL736_HISTORY}-${width}.png`)});
  assert.equal(await page.getByText(/^Bohdan signed in again/).count(),1);assert.deepEqual(errors,[]);
  console.log(JSON.stringify({width,history:Number(process.env.MEL736_HISTORY),open_ms:open,warm_open_ms:warm,pending_ms:local,failure_result_ms:confirmed,backend_delay_ms:1500,notice_count:1}));await page.close();
 }}catch(e){if(current){await current.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`mel736-failure-${process.env.MEL736_HISTORY}.png`)});console.log(await current.evaluate(()=>document.getElementById('skin')?.shadowRoot?.textContent?.slice(-4000)));}throw e;}finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
