// Local Chromium + real local Hub/UI; Google's script is intercepted and
// replaced with a button whose token is signed by the local RSA fixture.
const assert = require('node:assert/strict'), path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [native, relay, setup, signer, token] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
  const errors = [], externals = [];
  try {
    for (const width of [1280, 390]) {
      const context = await browser.newContext({ viewport: {width,height:900}, ignoreHTTPSErrors:true, colorScheme:width===390?'dark':'light' });
      await context.addCookies([native,setup].map(url=>({name:'agentnet_ui',value:token,url})));
      await context.route('**/*',async route=>{
        const url=route.request().url();
        if(url==='https://accounts.google.com/gsi/client') return route.fulfill({contentType:'text/javascript',body:`window.google={accounts:{id:{initialize(o){window.gisOptions=o},renderButton(root){const b=document.createElement('button');b.textContent='Sign in with Google';b.onclick=async()=>{const r=await fetch('/fixture-google-token?nonce='+encodeURIComponent(gisOptions.nonce));gisOptions.callback({credential:await r.text()})};root.append(b)}}}};`});
        if(url.startsWith(relay+'/fixture-google-token?')) {const nonce=new URL(url).searchParams.get('nonce');const r=await fetch(signer+'/?nonce='+encodeURIComponent(nonce));return route.fulfill({contentType:'text/plain',body:await r.text()});}
        if([native,relay,setup].some(o=>url.startsWith(o+'/'))) return route.continue();
        externals.push(url);return route.abort();
      });
      const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
      page.on('console',m=>{if(['error','warning'].includes(m.type())&&!m.text().startsWith('Failed to load resource:'))errors.push(m.type()+': '+m.text());});
      const capture=async name=>{
        await page.waitForTimeout(350); // settle the existing pane transition before visual evidence
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,name+' page overflow');
        if(shots) await page.screenshot({path:path.join(shots,`google-${name}-${width}.png`)});
      };
      const you=async()=>{if(width===1280)await page.getByRole('button',{name:'You: profile and devices'}).click();else {await page.getByRole('button',{name:'You',exact:true}).click();await page.getByRole('button',{name:/Fixture Person/}).click();}};
      await page.goto(native+'/?t='+token);assert.equal(await page.title(),'AgentNet');await you();
      await page.getByText('operator@example.com',{exact:true}).waitFor();await capture('profile');
      if(width===390)await page.getByRole('button',{name:'You',exact:true}).first().click();
      await page.getByRole('button',{name:/^Workspaces/}).click();
      await page.getByRole('heading',{name:'Invite people',exact:true}).waitFor();
      assert.equal(await page.getByRole('link',{name:'Get AgentNet',exact:true}).getAttribute('href'),relay+'/#google-signin');
      await page.getByRole('heading',{name:'Invite people',exact:true}).scrollIntoViewIfNeeded();await capture('membership-top');
      await page.getByLabel('Invite by email',{exact:true}).fill('new.person@example.com');await page.getByRole('button',{name:'Invite by email',exact:true}).click();
      await page.getByText('new.person@example.com',{exact:true}).waitFor();
      await page.getByLabel('Allow everyone at @domain',{exact:true}).fill('team.example.com');await page.getByRole('button',{name:'Allow domain',exact:true}).click();
      await page.getByText('@team.example.com',{exact:true}).waitFor();await capture('membership');
      await page.goto(setup+'/?t='+token+'#google-signin='+encodeURIComponent('https://workspace.example'));
      await page.getByLabel('Workspace address',{exact:true}).waitFor();assert.equal(await page.getByLabel('Workspace address').inputValue(),'https://workspace.example');
      await capture('setup');await page.getByRole('button',{name:'Sign in with Google',exact:true}).click();await page.getByText('Fixture sign-in declined. Try again.',{exact:true}).waitFor();
      assert.equal(await page.getByRole('button',{name:'Sign in with Google',exact:true}).isEnabled(),true);
      await page.goto(relay);await page.getByText('Sign in with Google in AgentNet',{exact:true}).waitFor();await capture('desktop-landing');
      for(const skin of ['classic','zoom']){
        await page.goto(native+'/?skin='+skin);await page.getByRole('button',{name:'Your profile and settings'}).click();await page.getByText('operator@example.com',{exact:true}).waitFor();await capture(skin+'-profile');
      }
      await context.close();
    }
    const context=await browser.newContext({viewport:{width:390,height:844},ignoreHTTPSErrors:true,userAgent:'Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36'});
    await context.route('**/*',async route=>{const url=route.request().url();if(url==='https://accounts.google.com/gsi/client')return route.fulfill({contentType:'text/javascript',body:`window.google={accounts:{id:{initialize(o){window.gisOptions=o},renderButton(root){const b=document.createElement('button');b.textContent='Sign in with Google';b.onclick=async()=>{const r=await fetch('/fixture-google-token?nonce='+encodeURIComponent(gisOptions.nonce));gisOptions.callback({credential:await r.text()})};root.append(b)}}}};`});if(url.startsWith(relay+'/fixture-google-token?')){const r=await fetch(signer+'/?nonce='+encodeURIComponent(new URL(url).searchParams.get('nonce')));return route.fulfill({contentType:'text/plain',body:await r.text()})}if(url.startsWith(relay+'/'))return route.continue();externals.push(url);return route.abort()});
    const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(['error','warning'].includes(m.type())&&!m.text().startsWith('Failed to load resource:'))errors.push(m.type()+': '+m.text());});await page.goto(relay);await page.getByRole('button',{name:'Sign in with Google',exact:true}).waitFor();if(shots)await page.screenshot({path:path.join(shots,'google-phone-landing-390.png')});
    await page.getByRole('button',{name:'Sign in with Google',exact:true}).click();await page.getByRole('button',{name:'You',exact:true}).waitFor();await page.getByRole('button',{name:'You',exact:true}).click();await page.getByRole('button',{name:/Fixture Person/}).click();await page.getByText('browser@example.com',{exact:true}).waitFor();await page.waitForTimeout(350);if(shots)await page.screenshot({path:path.join(shots,'google-phone-joined-390.png')});
    await context.close();assert.deepEqual(errors,[]);assert.deepEqual(externals,[]);console.log('Google UI PASS: 1280/light + 390/dark; real email/domain controls; signed profiles Comic/Classic/Zoom; setup retry; app handoff; mocked GIS browser enrollment; no external requests');
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1});
