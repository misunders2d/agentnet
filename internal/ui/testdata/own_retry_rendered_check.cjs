const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const fs = require('node:fs');
async function settle(p){await p.evaluate(async()=>{await document.fonts.ready;const root=document.querySelector('#skin')?.shadowRoot;await Promise.all((root?.getAnimations({subtree:true})||[]).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});}
(async () => {
  const browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
  try {
    for (const skin of ['comic','classic','zoom']) for (const state of (process.env.RETRY_STATES?process.env.RETRY_STATES.split(','):['cancelled','failed','interrupted','awaiting','running'])) {
      const stopped=['cancelled','failed','interrupted'].includes(state);
      const actions=state==='running'?['cancel']:state==='awaiting'?['accept','decline']:state==='interrupted'?['accept','resolve']:['accept'];
      const ctx=await browser.newContext({reducedMotion:'reduce',viewport:process.env.RETRY_MODE==='phone'?{width:390,height:844}:{width:1440,height:900}});
      const p=await ctx.newPage();p.setDefaultTimeout(5000);const errors=[];p.on('pageerror',e=>errors.push(String(e)));p.on('console',m=>{if(m.type()==='error'&&!/^Failed to load resource: the server responded with a status of 404/.test(m.text()))errors.push(m.text());});
      await p.route('**/api/overview*',async route=>{
        const response=await route.fetch();const o=await response.json();
        o.needs_you[0].reason=stopped?'agent_interrupted':state==='running'?'agent_running':'agent_awaiting';
        o.needs_you[0].actions=actions;o.needs_you[0].why='';o.people=[o.dms[0].peer];
        o.quarantine=[{id:'old-operation',peer:'bob/desk',code:'invalid',reason:'unused',at:new Date().toISOString()},{id:'unknown',peer:'bob/desk',code:'unverified',reason:'unused',at:new Date().toISOString()}];
        await route.fulfill({response,json:o});
      });
      await p.route('**/api/dm?*',async route=>{
        const response=await route.fetch();const d=await response.json();
        const m=d.messages[0];m.exec.state=state;m.actions=actions;m.job_detail=state==='interrupted'?'Interrupted run needs an explicit retry.':'';
        await route.fulfill({response,json:d});
      });
      await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);
      if(skin==='comic') await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();
      else {await p.locator('#profile-btn').click();await p.locator('#settings-tab-device').click();await p.locator('#quarantine-summary').click();}
      await p.getByText(/A message that couldn.t be accepted/).first().waitFor();
      assert.ok(await p.getByText(/failed a check and was kept out/).count(),skin+': legacy operation distinction');
      await settle(p);
      if(state==='cancelled'&&process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:process.env.AGENTNET_SCREENSHOTS+'/held-'+process.env.RETRY_MODE+'-'+skin+'.png'});}
      if(skin==='comic') {if(state==='interrupted'){await p.getByRole('button',{name:'Run it again',exact:true}).waitFor();assert.equal(await p.getByText(/Answered only if you allow it/).count(),0);}await p.getByRole('button',{name:/Open it in the chat/}).first().click();}
      else {await p.keyboard.press('Escape');if(state==='interrupted'){await p.locator('#review-btn').click();await p.locator('#review-list button').filter({hasText:'Your agent was interrupted'}).click();}else {await p.locator('#nav-chats').click();await p.getByRole('button',{name:/Bob/}).filter({visible:true}).first().click();}}
      if(skin==='zoom'&&state!=='interrupted'){await p.getByRole('button',{name:/Original request/}).click();}
      const row=skin==='comic'?p.locator('[data-mid="needs-request"]'):skin==='zoom'?p.locator('.zoom-message'):p.locator('#m-needs-request');
      await row.waitFor();
      const expected=state==='running'?(skin==='comic'?'Stop':'Stop…'):state==='awaiting'?(skin==='comic'?'Allow once':'Run…'):(skin==='comic'?'Run it again':'Run your responder again…');
      if(state==='awaiting' && skin!=='comic') await row.getByRole('button',{name:/run/i}).first().waitFor();
      else await row.getByRole('button',{name:expected,exact:true}).waitFor();
      if(state==='interrupted'){await row.getByText('Interrupted run needs an explicit retry.',{exact:true}).waitFor();await row.getByRole('button',{name:'Mark as handled',exact:true}).waitFor();}
      if(stopped) {
        assert.equal(await row.getByText('Needs your OK',{exact:true}).count(),0,skin+': retry called consent');
        assert.equal(await row.getByRole('button',{name:'Allow once',exact:true}).count(),0,skin+': retry called Allow once');
      }
      await settle(p);
      if(process.env.AGENTNET_SCREENSHOTS) {fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:process.env.AGENTNET_SCREENSHOTS+'/retry-'+process.env.RETRY_MODE+'-'+skin+'-'+state+'.png'});}
      assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1),false,skin+': overflow');
      assert.deepEqual(errors,[],skin+' '+state+': runtime errors');await ctx.close();console.log(skin+' '+state+' PASS');
    }
  } finally {await browser.close();}
  console.log('Own retry rendered PASS');
})().catch(e=>{console.error(e);process.exit(1)});
