const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const text='Review the synthetic launch checklist.\n\nKeep account permissions unchanged. Report concrete findings and remaining checks.\n'+('LongReferenceWithoutSpaces'.repeat(18));
async function settle(p){await p.evaluate(async()=>{await document.fonts.ready;await new Promise(requestAnimationFrame);});}
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  for(const skin of ['comic']) for(const width of [1440,390]) for(const scenario of ['linked','missing','deleted','edited','incoming','revised','revised-incoming']) {
   const ctx=await browser.newContext({reducedMotion:'reduce',viewport:{width,height:900}}),p=await ctx.newPage(),errors=[];p.setDefaultTimeout(7000);p.on('pageerror',e=>errors.push(String(e)));const posted=[];
   await p.route('**/api/act',async route=>{posted.push(route.request().postDataJSON());await route.fulfill({json:{result:'Synthetic action recorded'}});});
    const revised=scenario.startsWith('revised'),incoming=scenario==='incoming'||scenario==='revised-incoming';
    const state={linked:'queued',missing:'running',deleted:'interrupted',edited:'needs_human',incoming:'awaiting',revised:'awaiting','revised-incoming':'awaiting'}[scenario];
    const actions={linked:[],missing:['cancel'],deleted:['accept','resolve'],edited:['accept','resolve'],incoming:['accept','decline'],revised:['accept','decline'],'revised-incoming':['accept','decline']}[scenario];
    await p.route('**/api/overview*',async route=>{const response=await route.fetch(),o=await response.json();o.needs_you=[];o.people=[o.dms[0].peer];o.dms[0].count=4;o.dms[0].last='Compact task approval';await route.fulfill({response,json:o});});
    await p.route('**/api/dm?*',async route=>{
     const response=await route.fetch(),d=await response.json(),m=d.messages[0];
     const proposal={...m,id:'proposal',lid:'proposal',dir:'in',kind:'answer',status:'proposal',body:text,agent_id:'builder',verified_agent:true,actions:[],exec:undefined,job_detail:''};
     if(scenario==='deleted')proposal.deleted=true;
     if(scenario==='edited'){proposal.edited=true;proposal.text='Later proposal edit must not replace approved bytes';}
     const task={...m,id:'approved',lid:'approved',dir:incoming?'in':'out',body:revised?'Edited task instructions.\n'+text:text,actions,exec:{state,host:'alice/laptop'},job_detail:'',proposal:{proposal_id:'proposal',proposal:text,question_id:'question',question:'Review launch?',asker:'alice/laptop',confirmed_by:incoming?'bob/desk':'alice/laptop',...(revised?{edited:true}:{})},can:['react','delete']};
     if(scenario==='edited'){task.edited=true;task.text='Later task edit';}
     d.messages=[...(scenario==='missing'?[]:[proposal]),task,{...m,id:'human-task',lid:'human-task',kind:'task',body:'Ordinary human task keeps its full instructions.',pid:'',actions:[],exec:undefined,job_detail:''},{...m,id:'human-message',lid:'human-message',kind:'message',body:'Ordinary human message keeps its full text.',pid:'',actions:[],exec:undefined,job_detail:''}];
     await route.fulfill({response,json:d});
    });
    await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);
    const root=p.locator('#skin');
    await root.getByRole('button').filter({hasText:/Bob/}).filter({visible:true}).first().click();
    // Terminal request cards deliberately start folded. Open the actual
    // user-facing card before asserting its independent approved-text control.
    const disclosure=root.locator('[data-approved-task-text]');await disclosure.waitFor({state:'attached'});
    const request=disclosure.locator('xpath=ancestor::details[@data-request-disclosure]');
    if(await request.count()){assert.equal(await request.getAttribute('open'),null,'terminal request folded at rest');await request.locator('summary').first().click();}
    await disclosure.waitFor();await settle(p);
    assert.equal(await disclosure.getAttribute('open'),null,'closed at rest');
    assert.equal(await disclosure.locator('p').first().isVisible(),false,'instructions hidden at rest');
    assert.match(await root.locator('[data-proposal-provenance]').innerText(),revised?(incoming?/^Edited and sent this task/:/^You edited and sent this task/):(incoming?/^Approved this task/:/^You approved this task/));
    assert.ok(await root.locator('[data-agent-recipient]').filter({visible:true}).count(),'target visible');
    const statusWord={queued:/Queued/,running:/Working|Running/,interrupted:/Interrupted/,needs_human:/Needs a person|Needs your answer|needs your answer|needs your/,awaiting:/Waiting|waits/}[state];
    assert.ok(await root.getByText(statusWord).filter({visible:true}).count(),'exact execution state remains visible: '+state);
    await root.getByText('Ordinary human task keeps its full instructions.',{exact:false}).filter({visible:true}).waitFor();
    await root.getByText('Ordinary human message keeps its full text.',{exact:false}).filter({visible:true}).waitFor();
    const link=root.getByRole('button',{name:'View proposal',exact:true});
    assert.equal(await link.count(),['missing','deleted'].includes(scenario)?0:1,'only bound available proposal linked');
    assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,'page overflow');
    const overflow=await disclosure.evaluate(e=>{const r=e.getBoundingClientRect();return r.left<0||r.right>innerWidth+1;});assert.equal(overflow,false,'approval fits width');
    await disclosure.evaluate(e=>e.closest('[role=group]')?.scrollIntoView({block:'center'}));
    if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`approval-${skin}-${width}-${scenario}-closed.png`)});}
    const summary=disclosure.locator('summary');
    for(const control of [summary,...(await link.count()?[link]:[])]) {
     const contrast=await control.evaluate(e=>{const lum=s=>{const v=s.match(/[\d.]+/g).slice(0,3).map(Number).map(n=>{n/=255;return n<=.04045?n/12.92:((n+.055)/1.055)**2.4;});return v[0]*.2126+v[1]*.7152+v[2]*.0722;};let bg=e;while(bg&&getComputedStyle(bg).backgroundColor==='rgba(0, 0, 0, 0)')bg=bg.parentElement;const a=lum(getComputedStyle(e).color),b=lum(getComputedStyle(bg).backgroundColor);return(Math.max(a,b)+.05)/(Math.min(a,b)+.05);});
     assert.ok(contrast>=4.5,'approval control contrast: '+contrast);
    }
    await summary.focus();await p.keyboard.press('Enter');assert.equal(await disclosure.getAttribute('open'),'','keyboard opens exact text');
    const exact=await disclosure.innerText();assert.ok(exact.includes('Review the synthetic launch checklist.'),'approved text retained');
    assert.ok((await disclosure.innerText()).includes('LongReferenceWithoutSpaces'.repeat(18)),'full text retained');
    if(scenario==='edited')assert.ok((await disclosure.innerText()).includes('Later task edit'),'edit discoverable alongside original');
    if(revised)assert.ok(exact.includes('Edited task instructions.'),'exact edited task retained');
    assert.equal(await disclosure.evaluate(e=>e.scrollWidth>e.clientWidth+1),false,'expanded text wraps');
    await summary.scrollIntoViewIfNeeded();
    if(process.env.AGENTNET_SCREENSHOTS)await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`approval-${skin}-${width}-${scenario}-open.png`)});
    await summary.focus();await p.keyboard.press('Enter');
    if(state!=='queued') {
     const row=root.locator('[data-mid="approved"]');
     const expected=state==='running'?'Stop':state==='interrupted'?'Run it again':state==='awaiting'?'Allow once':null;
     if(expected)await row.getByRole('button',{name:expected,exact:true}).waitFor();
     if(state==='interrupted')await row.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();
     if(state==='needs_human')assert.equal(await row.getByRole('button',{name:/^(Mark as handled|Ask again)$/}).count(),0,'needs_human result offers no close or rerun control');
     if(state==='running'){
      await row.getByRole('button',{name:expected,exact:true}).click();
      const response=p.waitForResponse(r=>r.url().endsWith('/api/act')&&r.request().postDataJSON()?.do==='cancel');
      await root.getByRole('dialog').getByRole('button',{name:'Stop',exact:true}).click();await response;
      assert.deepEqual(posted.map(x=>[x.do,x.id]),[['cancel','approved']],'cancel targets confirmed request, never proposal');
     }
    }
    if(scenario==='linked'){await link.focus();await p.keyboard.press('Enter');await settle(p);assert.ok(await root.getByText('Review the synthetic launch checklist.',{exact:false}).filter({visible:true}).count(),'proposal navigation works');}
    assert.deepEqual(errors,[],'runtime errors');console.log(`${skin} ${width} ${scenario} PASS`);
   await p.unrouteAll({behavior:"wait"});await ctx.close();
  }
 }finally{await browser.close();}
 console.log('Compact approval rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
