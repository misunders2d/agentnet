// Production Comic and Host API over two disposable real agents and relay.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:960},timezoneId:'UTC'}),page=await context.newPage(),errors=[],posts=[];let scheduleReads=0,globalHistoryReads=0;
  page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(String(e)));
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname.startsWith('/api/'))posts.push({path:new URL(r.url()).pathname,body:r.postData()});});
  page.on('response',r=>{const u=new URL(r.url());if(r.request().method()==='GET'&&u.pathname==='/api/schedules'&&r.ok()){scheduleReads++;if(!u.searchParams.get('conv')&&!u.searchParams.has('id')&&u.searchParams.get('all')==='1')globalHistoryReads++;}});
  const call=(path,body)=>page.evaluate(async({path,body})=>{const r=await fetch(path,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw Error(await r.text());return r.json();},{path,body});
  const command=doWhat=>call('/__schedule_fixture',{Do:doWhat});
  const rows=()=>call('/api/schedules?conv='+process.env.SCHEDULE_CONV+'&all=0');
  // This Playwright version treats an async waitForFunction predicate's
  // Promise as truthy. Await each actual API observation in the test runner.
  const until=async(probe,deadline,label)=>{while(Date.now()<deadline){if(await probe())return;await page.waitForTimeout(100);}throw Error('Timed out waiting for '+label);};
  const field=()=>page.locator('[data-part="composer"] textarea');
  const list=()=>page.getByRole('region',{name:'Outbox',exact:true}).getByRole('region',{name:'Scheduled messages',exact:true});
  const chats=async()=>{await page.getByRole('navigation',{name:'Main',exact:true}).getByRole('button',{name:'Chats',exact:true}).click();await field().waitFor({state:'visible'});await list().waitFor({state:'hidden'});};
  const openChat=async()=>{await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button',{name:/Bob/}).first().click();await field().waitFor();};
  const openSchedule=async()=>{await page.getByRole('button',{name:'Add to message',exact:true}).click();await page.getByRole('menuitem',{name:/^Schedule…/}).click();};
  await page.goto(process.env.SCHEDULE_URL);await openChat();
  await field().fill('Real backend scheduled message');
  await page.locator('input[type=file]').setInputFiles({name:'kept.txt',mimeType:'text/plain',buffer:Buffer.from('exact retained schedule bytes')});
  await openSchedule();const create=page.getByRole('dialog',{name:'Schedule message',exact:true});
  await create.getByRole('button',{name:'Save schedule',exact:true}).waitFor();
  // Leave at least 45 seconds for saving/editing before the original deadline.
  const oldDue=Math.ceil((Date.now()+45000)/60000)*60000,newDue=oldDue+60000;
  const fillTime=async(dialog,prefix,instant)=>{const iso=new Date(instant).toISOString();await dialog.getByLabel(prefix+' date',{exact:true}).fill(iso.slice(0,10));await dialog.getByLabel(prefix+' time',{exact:true}).fill(iso.slice(11,16));};
  await fillTime(create,'Send',oldDue);await create.getByRole('button',{name:'Save schedule',exact:true}).click();await list().waitFor();
  const [saved]=await rows();assert.equal(saved.due,new Date(oldDue).toISOString().replace('.000',''));assert.equal(saved.rev,1);assert.equal(saved.files.length,1);
  await list().getByRole('button',{name:'Edit schedule',exact:true}).click();await fillTime(list(),'Reschedule',newDue);
  const time=list().getByLabel('Reschedule time',{exact:true});await time.focus();
  const beforePush=scheduleReads,pushed=await command('push');assert(pushed.ID&&pushed.LID,'push returns its exact stored identity');
  await until(async()=>{const dm=await call('/api/dm?id='+process.env.SCHEDULE_CONV);return scheduleReads>beforePush&&(dm.messages||[]).some(m=>m.id===pushed.ID||m.lid===pushed.LID);},Date.now()+15000,'exact pushed message and Outbox refresh');
  assert(await time.evaluate(e=>e===e.getRootNode().activeElement),'push preserves time-field focus');
  assert.equal(await time.inputValue(),new Date(newDue).toISOString().slice(11,16));
  await list().getByRole('button',{name:'Save schedule changes',exact:true}).click();await list().getByRole('button',{name:'Edit schedule',exact:true}).waitFor();
  const [edited]=await rows();assert.equal(edited.id,saved.id);assert.equal(edited.rev,2);assert.deepEqual(edited.files,saved.files);assert.deepEqual(edited.audience,saved.audience);assert.equal(Date.parse(edited.due),newDue);
  await chats();await field().fill('Draft survives a real push');
  await command('push');await page.waitForFunction(()=>[...document.querySelector('#skin').shadowRoot.querySelectorAll('[data-mid]')].filter(e=>e.textContent.includes('Schedule edit push signal')).length>=2);
  assert.equal(await field().inputValue(),'Draft survives a real push');
  await command('restart');await page.reload();await openChat();
  assert.deepEqual(await rows(),[edited],'durable schedule survives store/daemon reopen');
  // Clear only this test draft so the single entry opens the list.
  await field().fill('');await openSchedule();await list().getByText('Real backend scheduled message',{exact:true}).waitFor();
  await list().getByRole('button',{name:'Edit schedule',exact:true}).click();assert.equal(await list().getByLabel('Reschedule time',{exact:true}).inputValue(),new Date(newDue).toISOString().slice(11,16));await list().getByRole('button',{name:'Close details',exact:true}).click();
  const remaining=oldDue-Date.now()+1000;assert(remaining>0,'test edited and restarted before original deadline');await page.waitForTimeout(remaining);
  const originals=await command('received');assert.equal(originals.filter(m=>m.lid===saved.id).length,0,'old deadline sends zero originals');assert.equal((await rows())[0].state,'pending');
  await until(async()=>(await call('/api/schedules?id='+saved.id)).state==='sent',newDue+15000,'schedule dispatch at new deadline');
  await until(async()=>(await command('received')).filter(m=>m.lid===saved.id).length>=1,Date.now()+15000,'exact original at recipient');
  const received=(await command('received')).filter(m=>m.lid===saved.id);assert.equal(received.length,1);assert.equal(received[0].body,saved.body);assert.equal(received[0].attachments.length,1);assert.equal(received[0].attachments[0].sha256,saved.files[0].sha256);assert.equal(received[0].attachments[0].name,'kept.txt');
  assert.equal(globalHistoryReads,0,'Outbox never fetches closed history while only active work is shown');
  await list().getByRole('button',{name:'Show history',exact:true}).click();await list().getByRole('article',{name:'Scheduled message: Queued to send',exact:true}).waitFor();
  assert(globalHistoryReads>0,'explicit Show history fetches terminal schedules');assert.match(await list().getByRole('button',{name:'Hide history',exact:true}).textContent(),/\(1\)/);
  assert.equal(posts.filter(p=>['/api/dm/send','/api/send'].includes(p.path)).length,0,'scheduling controls never use ordinary Send');assert.deepEqual(errors,[]);
  console.log('live schedule PASS: rendered create/edit/reopen; real push keeps focus/draft; exact ID/revision/files/audience after daemon/store restart; old deadline zero, new deadline exactly one');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
