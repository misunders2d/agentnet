// Actual Comic reminder controls over disposable native installation.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 let desktopKeyboardSequences=0,touchPickerSequences=0;
 try {
  for(const [width,zone] of [[1280,'America/New_York'],[1100,'Europe/Kyiv'],[390,'America/New_York'],[390,'Europe/Kyiv']]){
   const context=await browser.newContext({viewport:{width,height:900},timezoneId:zone,colorScheme:width===390?'dark':'light',isMobile:width===390,hasTouch:width===390}),page=await context.newPage(),errors=[],requests=[];
   page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(String(e)));
   page.on('request',r=>{if(new URL(r.url()).pathname==='/api/remind')requests.push(r.postDataJSON());});
   const target=new URL(process.env.PARITY_URL);
   await page.goto(target.href);
   if(process.env.AGENTNET_REMINDER_SKIN){
    target.searchParams.delete('t');target.searchParams.set('skin',process.env.AGENTNET_REMINDER_SKIN);
    await page.goto(target.href); // Establish the session before selecting an installed package.
    try{await page.getByRole('button',{name:'Use this skin',exact:true}).click();}
    catch(e){console.error('skin startup',page.url(),await page.locator('body').innerText(),await (await page.request.get(new URL('/assets/skins/index.json',target).href)).text());throw e;}
   }
   await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/^Laptop\b/}).click();
   const msg=page.locator('[data-mid="'+process.env.PARITY_FIRST+'"]');await msg.waitFor();
   if(width===390){await msg.getByRole('button',{name:'Message actions'}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
   else {await msg.locator('[role="group"]').hover();await msg.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}
   let sheet=page.getByRole('dialog',{name:'Remind me later'});await sheet.waitFor();
   const year=new Date().getFullYear()+1;
   for(const [index,month,day,hour,minute] of [[0,3,1,9,45],[1,4,1,17,20]]){
    if(index){await msg.getByRole('button',{name:'Change…'}).click();sheet=page.getByRole('dialog',{name:'Move the reminder'});await sheet.waitFor();}
    const date=sheet.getByLabel('Date',{exact:true}),time=sheet.getByLabel('Time',{exact:true});
    await date.waitFor();await time.waitFor();assert.equal(await date.getAttribute('type'),'text');assert.equal(await time.getAttribute('type'),'time');
    const displayedZone=await page.evaluate(()=>Intl.DateTimeFormat().resolvedOptions().timeZone);
    assert((await sheet.innerText()).includes(displayedZone),'visible timezone unchanged');
    const before=requests.length,dateValue=`${year}-${String(month).padStart(2,'0')}-${String(day).padStart(2,'0')}`;
    await date.fill('0012-10-22');await time.fill('12:01');
    await sheet.getByRole('button',{name:index?'Move it':'Remind me',exact:true}).click();
    await sheet.getByRole('alert').waitFor();assert.match(await sheet.getByRole('alert').innerText(),/future/);assert.equal(requests.length,before);
    // The reported year must be replaceable with ordinary typing, without
    // navigating a browser's year/day segments or restoring an old default.
    await date.focus();await page.keyboard.press('ControlOrMeta+A');await page.keyboard.type(dateValue);
    assert.equal(await date.inputValue(),dateValue,'replace year0012 with complete four-digit date');
    await date.fill('202');
    await sheet.getByRole('button',{name:index?'Move it':'Remind me',exact:true}).click();
    assert.match(await sheet.getByRole('alert').innerText(),/YYYY-MM-DD/);assert.equal(requests.length,before);
    assert(await date.evaluate(e=>e===e.getRootNode().activeElement),'invalid date returns focus to its editable field');
    await date.fill(`${year}-02-30`);
    await sheet.getByRole('button',{name:index?'Move it':'Remind me',exact:true}).click();
    assert.match(await sheet.getByRole('alert').innerText(),/valid date/);assert.equal(requests.length,before);
    await date.fill(dateValue);await time.fill('10:30');
    if(index){
     // Browsers without showPicker retain a real native calendar control.
     const calendar=sheet.getByLabel('Calendar date',{exact:true});
     await calendar.evaluate(e=>Object.defineProperty(e,'showPicker',{value:undefined,configurable:true}));
     await sheet.getByRole('button',{name:'Choose from calendar',exact:true}).click();
     assert(await calendar.isVisible(),'native calendar fallback is usable');
     await calendar.fill(`${year}-05-02`);assert.equal(await date.inputValue(),`${year}-05-02`);
     await date.fill(dateValue);
    }
    // In Chromium's mobile-picker emulation the same keys reached InputTime
    // but produced no input events (also on the unchanged baseline). Its
    // hour/minute segments are not keyboard-editable; retain these assertions
    // for desktop segments and all fill/empty/date/DST checks for touch below.
    if(width>=1024){
     await time.focus();await page.keyboard.press('Home');await page.keyboard.press('Backspace');
     await page.keyboard.type('11');
     assert.equal(await time.inputValue(),'11:30','typing into incomplete hour retains minute segment');
     await page.keyboard.type('45');
     assert.equal(await time.inputValue(),'11:45','typed minutes persist');
     desktopKeyboardSequences++;
    }else touchPickerSequences++;
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
    assert.equal(await sheet.getByRole('alert').count(),0,'editing clears obsolete validation feedback');
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
   if(width>=1024){
    const bubble=msg.locator('[data-message-bubble]');await bubble.hover();
    const more=msg.getByRole('button',{name:'More actions',exact:true});await more.waitFor();
    const geometry=await more.evaluate(b=>({toolbar:b.parentElement.getBoundingClientRect().bottom,bubble:b.closest('[data-message-bubble]').getBoundingClientRect().bottom}));
    assert(geometry.toolbar<=geometry.bubble+1,'hover toolbar stays above the reminder action line');
    await more.focus();await more.press('Enter');await page.getByRole('menuitem',{name:'Details',exact:true}).waitFor();await page.keyboard.press('Escape');
   }
   await msg.getByRole('button',{name:'Cancel',exact:true}).click();await msg.getByRole('button',{name:'Change…'}).waitFor({state:'detached'});
   // Ordinary pointer actions, never forced: changing, cancelling and
   // finishing a reminder must coexist with hover/focus message tools.
   if(width===390){await msg.getByRole('button',{name:'Message actions'}).focus();await page.keyboard.press('Enter');await page.getByRole('button',{name:'Remind me…',exact:true}).click();}
   else {await msg.locator('[data-message-bubble]').hover();await msg.getByRole('button',{name:'More actions'}).click();await page.getByRole('menuitem',{name:'Remind me…'}).click();}
   const again=page.getByRole('dialog',{name:'Remind me later',exact:true});await again.waitFor();await again.getByRole('button',{name:'Remind me',exact:true}).click();await again.waitFor({state:'hidden'});
   await msg.getByRole('button',{name:'Change…',exact:true}).waitFor();
   await msg.getByRole('button',{name:'Done',exact:true}).click();await msg.getByRole('button',{name:'Change…'}).waitFor({state:'detached'});
   assert.deepEqual(errors,[]);await context.close();
  }
  assert.deepEqual({desktopKeyboardSequences,touchPickerSequences},{desktopKeyboardSequences:4,touchPickerSequences:4},'both desktop keyboard and native touch-picker coverage ran');
  console.log('reminder date/time PASS: editable year0012 recovery, calendar fallback, labelled desktop/touch controls, incomplete/invalid-date validation, no implicit send, NY/Kyiv DST timestamps; keyboard hour/minute desktop-only ('+desktopKeyboardSequences+' sequences), not executed on native touch pickers ('+touchPickerSequences+' sequences)');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
