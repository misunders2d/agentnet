// Joined deterministic native-process result -> real Host API -> production
// Comic. No route mocks, fabricated organization rows or live profiles/models.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const fixture=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{
  const context=await browser.newContext({viewport:{width:fixture.width,height:900}}),page=await context.newPage(),errors=[],posts=[];
  page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST')posts.push({path:new URL(r.url()).pathname,body:r.postData()});});
  const main=new URL(fixture.link);main.hash=new URLSearchParams({realm:fixture.realm,conv:fixture.conv,topic:'main'}).toString();
  const entry=fixture.url+'#'+main.hash.slice(1);
  await page.goto(entry);
  const call=path=>page.evaluate(async path=>{const response=await fetch(path);if(!response.ok)throw Error(await response.text());return response.json();},path);
  const view=()=>call('/api/dm?id='+fixture.conv);
  // Await actual observations in Node: never treat an async page predicate's
  // Promise as proof that an API result has arrived.
  const until=async(probe,label)=>{const deadline=Date.now()+15000;while(Date.now()<deadline){if(await probe())return;await page.waitForTimeout(50);}throw Error('Timed out waiting for '+label);};
  const row=id=>page.locator('[data-mid="'+id+'"]');
  const before=await view(),byLID=lid=>before.messages.find(m=>m.lid===lid||m.id===lid);
  const selected=fixture.selected.map(byLID),omitted=fixture.omitted.map(byLID);
  assert.equal(before.topics.find(t=>t.id===fixture.topic)?.title,'Fixture organization','requester receives the supplied new-topic name');
  assert.equal(before.messages.find(m=>m.id===fixture.move)?.body,'Moved selected messages to “Fixture organization”.','requester notice names the exact destination');
  assert(selected.every(Boolean)&&omitted.every(Boolean),'all exact originals remain available');
  assert(selected.every(m=>m.topic===fixture.topic),'native action moved exactly selected originals');
  assert(omitted.every(m=>!m.topic),'interleaved omitted parent/task stay in Main');
  assert.equal(selected[1].reply_to,omitted[0].id,'selected reply retains its omitted parent link');
  assert.equal(selected[0].attachments.length,1);assert.equal(selected[0].attachments[0].name,'original-report.txt');
  await row(fixture.report).waitFor();
  const destination=row(fixture.report).getByRole('link',{name:'Open organized topic',exact:true});
  assert.equal(await destination.getAttribute('href'),fixture.link,'native reported link is exact');
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'organization-native-report-'+fixture.width+'.png')});
  await destination.click();
  for(const m of selected)await row(m.id).waitFor();
  for(const m of omitted)assert.equal(await row(m.id).count(),0,'destination does not display an omitted original');
  // Exact reported href and selected/omitted original IDs prove navigation;
  // its contract does not require a particular topic-bar layout.
  const moved=row(fixture.move);await moved.waitFor();
  const undo=moved.getByRole('button',{name:'Undo topic move',exact:true});await undo.waitFor();
  const attachment=await context.request.get(new URL('/api/files/'+selected[0].id+'/0?dir=out',fixture.url).href);
  assert.equal(attachment.status(),200);assert.equal((await attachment.body()).toString(),'EXACT_ORGANIZATION_ATTACHMENT\n');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'destination fits viewport');
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'organization-native-destination-'+fixture.width+'.png')});
  await undo.click();
  await until(async()=>{const current=await view();return fixture.selected.every(lid=>current.messages.some(m=>(m.lid===lid||m.id===lid)&&!m.topic));},'real inverse organization');
  const undos=posts.filter(p=>p.path.endsWith('/api/topic/organization/undo'));
  assert.equal(undos.length,1);const request=JSON.parse(undos[0].body);
  assert.equal(request.conv,fixture.conv);assert.equal(request.lid,before.messages.find(m=>m.id===fixture.move).lid);assert(request.author&&request.id);
  await page.goto(entry); // Reload and reopen the original discussion normally.
  for(const m of [...selected,...omitted])await row(m.id).waitFor();
  const restored=await view();
  for(const original of [...selected,...omitted]){
   const current=restored.messages.filter(m=>m.id===original.id);
   assert.equal(current.length,1,'one exact original after Undo/reload');
   for(const key of ['id','lid','from','kind','body','at','reply_to','agent_id','target'])assert.deepEqual(current[0][key],original[key],key+' retained');
   assert(!current[0].topic,'original placement restored');
  }
  assert(!posts.some(p=>/\/api\/(?:send|dm\/send|upload|.*\/invite|.*\/decide)$/.test(p.path)),'navigation and Undo never resend originals or execute old work');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'restored view fits viewport');
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'organization-native-undo-'+fixture.width+'.png')});
  assert.deepEqual(errors,[]);await context.close();
  console.log('joined native organization Comic PASS '+fixture.width+': actual reported destination/link, original file/reply/IDs, real Undo and reload; no ordinary send; deterministic stand-in only');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
