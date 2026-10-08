// Real loader and bundled skins; every app operation is intercepted locally.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const settle = page => page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
async function releaseChecks(browser) {
  for (const skin of (process.env.AGENTNET_TEST_SKINS || 'comic,classic,zoom').split(',')) for (const width of [1280,390]) {
    const context = await browser.newContext({viewport:{width,height:900},reducedMotion:'reduce'});
    const page = await context.newPage();page.setDefaultTimeout(8000);await page.clock.install();
    const errors = [], actions = [], checks = [], pending = [];
    let supported = true, next = {state:'available',version:'v0.8.9',latest:'v0.8.10'};
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => { if (r.method() === 'POST' && new URL(r.url()).pathname.startsWith('/api/app/')) actions.push(r.url()); });
    const origin = new URL(process.argv[2]).origin, other = 'b'.repeat(32);
    const bindings = ['default',other].map((id,i) => ({id,handle:String(i+1).repeat(32),endpoint:origin,address:'alice/laptop',realm:'a'.repeat(32),name:i?'Other fixture':'First fixture',state:'enrolled'}));
    await page.route('**/api/workspaces', r => r.fulfill({json:bindings}));
    await page.route('**/api/workspaces/all', r => r.fulfill({json:bindings}));
    await page.route('**/workspaces/*/*/**', r => {
      const url = new URL(r.request().url());url.pathname = url.pathname.replace(/^\/workspaces\/[^/]+\/[^/]+/,'');
      return r.continue({url:url.href});
    });
    await page.route('**/api/app/status', r => r.fulfill({json:{version:'v0.8.9',cli_path:'/synthetic/agentnet',cli_state:'installed',app_update_supported:supported,problem:supported?'':'Managed by the fixture package manager.'}}));
    const reply = (r,v) => v.error ? r.fulfill({status:503,contentType:'text/plain',body:v.error}) : r.fulfill({json:v});
    await page.route('**/api/app/check', r => {
      assert.equal(r.request().method(),'GET');checks.push(r.request().url());
      const value = {...next};
      if(value.hold){pending.push({route:r,value});return;}
      return reply(r,value);
    });
    const nav = () => page.getByRole('navigation',{name:'Main',exact:true});
    const about = async () => {
      if(skin==='comic'){
        await nav().getByRole('button',{name:width===390?'You':'Settings',exact:true}).click();
        await page.getByRole('button',{name:/^About/}).click();
      }else await page.getByRole('button',{name:'Your profile and settings',exact:true}).click();
    };
    const close = async () => {
      if(skin==='comic'){
        await nav().getByRole('button',{name:'Chats',exact:true}).click();
        await page.getByRole('button',{name:'Update AgentNet',exact:true,includeHidden:true}).waitFor({state:'detached'});
      }
      else await page.getByRole('button',{name:'Close settings',exact:true}).click();
      await settle(page);
    };
    const check = () => page.getByRole('button',{name:'Check for updates',exact:true});
    const update = () => page.getByRole('button',{name:'Update AgentNet',exact:true});
    const resultText = v => v.error ? 'Couldn’t check for updates: '+v.error : v.state==='available' ? `Version ${v.latest} is available.` : v.state==='current' ? `This app matches the latest stable release (${v.latest}).` : `This app (${v.version}) is ahead of the latest stable release (${v.latest}).`;
    const run = async value => {
      next = value;const before = checks.length;await check().click();
      await page.getByText(resultText(value),{exact:true}).waitFor();
      assert.equal(checks.length,before+1,'one explicit click makes one check');
    };
    try {
      await page.goto(process.argv[2]); // consume the demo token before selecting a skin
      await page.goto(origin+'/?skin='+skin);await about();
      await check().waitFor();assert.equal(checks.length,0,'opening the settings does not check releases');
      assert((await page.locator('#skin').first().evaluate(e=>e.shadowRoot.textContent)).includes('v0.8.9'),'installed app version is shown');
      // Exercise an in-flight result without timers or a live release lookup.
      next={state:'available',version:'v0.8.9',latest:'v0.8.10',hold:true};await check().click();
      await page.getByRole('button',{name:'Checking…',exact:true}).waitFor();
      assert(await update().isDisabled(),'checking disables installation');
      assert.equal(pending.length,1);await reply(pending[0].route,pending[0].value);pending.length=0;
      await page.getByText('Version v0.8.10 is available.',{exact:true}).waitFor();assert(!(await update().isDisabled()));
      for(const value of [{state:'current',version:'v0.8.9',latest:'v0.8.9'},{state:'ahead',version:'v0.8.9',latest:'v0.8.8'}]){
        await run(value);assert(await update().isDisabled(),'current/ahead cannot install the latest stable');
      }
      await run({error:'Synthetic offline release service'});assert(!(await update().isDisabled()),'failure clears the previous successful comparison');
      assert.equal(await page.getByText('This app (v0.8.9) is ahead of the latest stable release (v0.8.8).',{exact:true}).count(),0);
      // An unsupported installation may still perform the read-only check.
      await close();supported=false;await about();await check().waitFor();
      await run({state:'available',version:'v0.8.9',latest:'v0.8.10'});assert(await update().isDisabled());
      await close();supported=true;await about();await check().waitFor();
      for(const leave of ['close','tab','workspace']){
        const old = leave==='tab'?{error:'STALE_CHECK_ERROR',hold:true}:{state:'available',version:'v0.8.9',latest:'v9.9.9',hold:true};
        next=old;await check().click();await page.getByRole('button',{name:'Checking…',exact:true}).waitFor();
        assert.equal(pending.length,1);
        if(leave==='workspace'){
          await page.evaluate(id=>window.agentnet.workspaces.select(id),other);
          await page.waitForFunction(id=>window.agentnet.workspace.id===id,other);
          await about();
        }else if(leave==='tab'){
          if(skin==='comic'){
            if(width===390)await page.getByRole('button',{name:'You',exact:true}).first().click();
            await page.getByRole('button',{name:/^Appearance/}).click();
            await page.getByRole('heading',{name:'Appearance',exact:true}).waitFor();
            if(width===390)await page.getByRole('button',{name:'You',exact:true}).first().click();
            await page.getByRole('button',{name:/^About/}).click();
          }else{
            await page.getByRole('tab',{name:'Appearance',exact:true}).click();
            await page.getByRole('tab',{name:'Profile',exact:true}).click();
          }
        }else {await close();await about();}
        await check().waitFor();
        await run({state:'current',version:'v0.8.9',latest:'v0.8.9'});
        const held=pending.shift();await reply(held.route,held.value);await settle(page);
        await page.getByText('This app matches the latest stable release (v0.8.9).',{exact:true}).waitFor();
        assert.equal(await page.getByText(resultText(old),{exact:true}).count(),0,'old view completion cannot overwrite the current result');
        assert(await update().isDisabled());
      }
      const explicitChecks=checks.length;await page.clock.fastForward(60000);await settle(page);assert.equal(checks.length,explicitChecks,'no periodic follow-up check');
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'settings do not overflow');
      if(process.env.AGENTNET_SCREENSHOTS){
        const shot=path.join(process.env.AGENTNET_SCREENSHOTS,skin+'-release-check-'+width+'.png');await page.screenshot({path:shot});
        if(process.env.AGENTNET_RENDERED_RETAIN){const keep=path.resolve(process.env.AGENTNET_RENDERED_RETAIN);assert(keep.startsWith('/tmp/'));fs.mkdirSync(keep,{recursive:true,mode:0o700});fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
      }
      // Remount the same package over the public host contract, as an old host
      // or a browser would supply it. Neither may invoke a desktop check.
      for(const mode of ['old-host','browser']){
        await page.evaluate(async ({skin,mode})=>{
          const root=document.getElementById('skin').shadowRoot.querySelector('.skin-root'),module=await import('/assets/skins/'+skin+'/entry.mjs');
          await module.unmount(root);const host={...window.agentnet};
          if(mode==='old-host')delete host.appCheckUpdate;else host.platform='browser';
          await module.mount(root,host);
        },{skin,mode});
        await about();await settle(page);
        assert.equal(await check().count(),0,mode+' hides the optional desktop control');
        assert.equal(checks.length,explicitChecks,mode+' never calls app check');
      }
      assert.deepEqual(actions,[],'checking never posts update or CLI replacement');assert.deepEqual(errors,[],'no browser exceptions');
      console.log(`${skin} ${width}: published release check PASS (${checks.length} explicit GETs; no POSTs)`);
    }catch(e){console.error(JSON.stringify({skin,width,errors,text:await page.locator('#skin').first().evaluate(e=>e.shadowRoot?.textContent||e.textContent)}));throw e;}
    finally{for(const held of pending)await held.route.abort().catch(()=>{});await context.close();}
  }
}
(async () => {
  const browser = await chromium.launch({headless:true, executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium'});
  try {
    if(process.argv.includes('--release-check')){await releaseChecks(browser);return;}
    const page = await browser.newPage({viewport:{width:1280,height:900}});
    const errors = [], actions = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => { if (r.method() === 'POST' && new URL(r.url()).pathname.startsWith('/api/app/')) actions.push(r.url()); });
    await page.route('**/api/app/status', r => r.fulfill({status:404, contentType:'text/plain', body:'404 page not found'}));
    await page.goto(process.argv[2]);
    await page.getByRole('button', {name:'Settings',exact:true}).click();
    await page.getByRole('button', {name:/^About/}).click();
    const update = page.getByRole('button',{name:'Update AgentNet',exact:true});
    await update.waitFor();
    assert.equal(await update.isDisabled(),true,'unavailable updater must never enable an action');
    await page.getByText('This page does not expose the app’s update controls.',{exact:false}).waitFor();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth > innerWidth),false,'About must not overflow');
    if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'attached-updater-about.png')});
    assert.deepEqual(actions,[],'no update or command replacement requested');
    assert.deepEqual(errors,[],'no browser exceptions');
    console.log('attached updater check PASS');
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1;});
