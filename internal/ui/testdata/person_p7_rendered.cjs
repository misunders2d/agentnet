// Actual bundled skins against the public inert Go host fixture.
const { chromium }=require(process.env.AGENTNET_PLAYWRIGHT);
const assert=require('node:assert/strict'),path=require('node:path');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{for(const skin of ['comic','classic','zoom'])for(const width of [1280,390]){
  const origin=new URL(process.env.P7_URL).origin,context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage(),errors=[];
  await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  page.on('pageerror',e=>errors.push(String(e)));
  await context.request.post(origin+'/p7/reset');
  await page.goto(process.env.P7_URL);
  await page.goto(origin+'/?skin='+skin);
  if(skin==='comic'){
   await page.locator('section[aria-label="Chats"]').waitFor();
   await page.getByRole('button',{name:/P7 person permission question/}).first().click();
  }else{
   await page.locator('#review-btn').waitFor();
   await page.locator('#review-btn').click();
   await page.locator('#review').getByRole('button',{name:/P7 person permission question/}).first().click();
  }
  const open=page.getByRole('button',{name:/Approve Sergey/}).first();await open.waitFor();await open.click();
  const confirm=page.getByRole('button',{name:'Approve Sergey',exact:true});await confirm.waitFor();
  assert(await page.getByText(/all current and future verified devices/).count(),skin+width+': person scope explained');
  await page.evaluate(async()=>{const root=document.querySelector('#skin')?.shadowRoot;await Promise.all((root?.getAnimations({subtree:true})||[]).filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'p7-'+skin+'-'+width+'.png')});
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),skin+width+': horizontal overflow');
  await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/api/act' && r.request().method()==='POST'),confirm.click()]);
  const acts=await (await context.request.get(origin+'/p7/actions')).json();assert.equal(acts.at(-1).id,process.env.P7_PERSON,skin+width+': exact person ID');
  if(skin==='comic'){
   if(width<700)await page.getByRole('button',{name:/Back to chats/}).click();
   await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:width<700?'You':'Settings',exact:true}).click();
   await page.getByRole('button',{name:/^Permissions/}).click();
   const off=page.getByRole('button',{name:'Turn off automatic answers for Sergey',exact:true});await off.waitFor();
   await page.getByRole('button',{name:'Turn off tasks without asking for Sergey',exact:true}).waitFor();
   assert(await page.getByText('All current and future verified devices',{exact:true}).count()>=2,'canonical person grants shown');
   if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'p7-permissions-'+width+'.png')});
   assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'permissions horizontal overflow');
   await page.getByRole('button',{name:'In a terminal',exact:true}).click();
   await page.getByText('agentnet unapprove --tasks PERSON-or-ADDRESS',{exact:true}).scrollIntoViewIfNeeded();
   await page.evaluate(async()=>{await document.fonts.ready;const root=document.querySelector('#skin')?.shadowRoot;await Promise.all((root?.getAnimations({subtree:true})||[]).filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
   if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'p7-permissions-details-'+width+'.png')});
   await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/api/act'&&r.request().method()==='POST'),off.click()]);
   const revoked=await (await context.request.get(origin+'/p7/actions')).json();assert.equal(revoked.at(-1).id,process.env.P7_PERSON);assert.equal(revoked.at(-1).do,'unapprove');
  }
  assert.deepEqual(errors,[],skin+width+': page errors');await context.close();console.log(skin,width,'person decision PASS');
 }}finally{await browser.close();}
 console.log('P7 person approval rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
