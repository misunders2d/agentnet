// Real Comic on a daemon page with no /api/app control surface.
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
(async () => {
  const browser = await chromium.launch({headless:true, executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium'});
  try {
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
