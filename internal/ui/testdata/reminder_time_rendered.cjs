// Actual Comic reminder controls over disposable native installation.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try {
  for(const [width,zone] of [[1280,'America/New_York'],[390,'America/New_York'],[390,'Europe/Kyiv']]){
   const context=await browser.newContext({viewport:{width,height:900},timezoneId:zone,isMobile:width===390,hasTouch:width===390}),page=await context.newPage(),errors=[],requests=[];
   page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(String(e)));
   page.on('request',r=>{if(new URL(r.url()).pathname==='/api/remind')requests.push(r.postDataJSON());});
   await page.goto(process.env.PARITY_URL);await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/^Laptop\b/}).click();
   const msg=page.locator('[data-mid="'+process.env.PARITY_FIRST+'"]');await msg.waitFor();
   if(width===390){await msg.getByRole('button',{name:'Message actions'}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
   else {await msg.locator('[role="group"]').hover();await msg.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}
   let sheet=page.getByRole('dialog',{name:'Remind me later'});await sheet.waitFor();
   const year=new Date().getFullYear()+1;
   for(const [index,month,day,hour,minute] of [[0,3,1,9,45],[1,4,1,17,20]]){
    if(index){await msg.getByRole('button',{name:'Change…'}).click();sheet=page.getByRole('dialog',{name:'Move the reminder'});await sheet.waitFor();}
    const date=sheet.getByLabel('Date',{exact:true}),time=sheet.getByLabel('Time',{exact:true});
    await date.waitFor();await time.waitFor();assert.equal(await date.getAttribute('type'),'date');assert.equal(await time.getAttribute('type'),'time');
    const displayedZone=await page.evaluate(()=>Intl.DateTimeFormat().resolvedOptions().timeZone);
    assert((await sheet.innerText()).includes(displayedZone),'visible timezone unchanged');
    const before=requests.length,dateValue=`${year}-${String(month).padStart(2,'0')}-${String(day).padStart(2,'0')}`;
    await date.fill(dateValue);await time.fill('10:30');
    await time.focus();await page.keyboard.press('Home');await page.keyboard.press('Backspace');
    await page.keyboard.type('11');
    assert.equal(await time.inputValue(),'11:30','typing into incomplete hour retains minute segment');
    await page.keyboard.type('45');
    assert.equal(await time.inputValue(),'11:45','typed minutes persist');
    // Invalid intermediate fields must remain editable instead of restoring default.
    await time.fill('');await page.waitForTimeout(20);assert.equal(await time.inputValue(),'');
    await sheet.getByRole('button',{name:index?'Move it':'Remind me',exact:true}).click();
    await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/Choose a date and time/);assert.equal(requests.length,before);
    await time.fill(`${String(hour).padStart(2,'0')}:${String(minute).padStart(2,'0')}`);
    assert.equal(await date.inputValue(),dateValue,'time edit preserves selected date');
    if(index){
     await sheet.getByRole('button',{name:'Close',exact:true}).click();await sheet.waitFor({state:'hidden'});
     await msg.getByRole('button',{name:'Change…'}).click();await sheet.waitFor();
     assert.equal(await date.inputValue(),dateValue,'dismiss/reopen preserves displayed selected date');
     assert.equal(await time.inputValue(),`${String(hour).padStart(2,'0')}:${String(minute).padStart(2,'0')}`,'dismiss/reopen preserves displayed selected time');
    }
    await time.focus();await page.keyboard.press('Tab');
    assert.equal(requests.length,before,'editing and blur never submit');
    assert(await sheet.getByLabel('At a time I choose').isChecked(),'editing selects custom time');
    assert.equal(await date.evaluate(e=>e.getBoundingClientRect().height)>=44,true);assert.equal(await time.evaluate(e=>e.getBoundingClientRect().height)>=44,true);
    await page.waitForTimeout(350);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true,mode:0o700});await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`reminder-${width}-${zone.replace('/','-')}-${index}.png`)});}
    await sheet.getByRole('button',{name:index?'Move it':'Remind me',exact:true}).click();await sheet.waitFor({state:'hidden'});
    const offset=zone==='America/New_York'?(index?4:5):(index?-3:-2);
    const expected=Math.floor(Date.UTC(year,month-1,day,hour+offset,minute)/1000);
    assert.deepEqual(requests.at(-1),{id:process.env.PARITY_FIRST,due:expected},'exact selected local date/time converts across DST');
    await msg.getByRole('button',{name:'Change…'}).waitFor();
   }
   await msg.getByRole('button',{name:'Cancel',exact:true}).click();await msg.getByRole('button',{name:'Change…'}).waitFor({state:'detached'});
   assert.deepEqual(errors,[]);await context.close();
  }
  console.log('reminder date/time PASS: labelled desktop/touch controls, keyboard hour/minute, incomplete validation, no implicit send, NY/Kyiv DST timestamps');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
