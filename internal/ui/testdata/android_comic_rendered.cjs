const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const fs = require('node:fs');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  try {
    for (const width of [390, 320]) {
      const context = await browser.newContext({viewport:{width,height:844},colorScheme:'light',isMobile:true,hasTouch:true});
      const page = await context.newPage();
      const errors=[];page.on('pageerror',e=>errors.push(String(e)));
      await page.addInitScript(() => {
        window.AgentNetAndroid = { postMessage(raw) {
          const request=JSON.parse(raw);
          queueMicrotask(()=>this.onmessage?.({data:JSON.stringify({id:request.id,ok:true,enabled:false,granted:true,drafts:[]})}));
        }};
      });
      await page.goto(process.argv[2]);
      await page.locator('section[aria-label="Chats"]').waitFor();
      assert.equal(await page.evaluate(()=>window.agentnet.platform),'android');
      assert.equal(await page.evaluate(()=>typeof window.agentnetEngine),'undefined');
      // Android saves the shared UI's decrypted object URL through SAF. Exercise
      // the real response CSP: a mocked fetch misses WebView's policy rejection.
      assert.equal(await page.evaluate(async()=>{
        const url=URL.createObjectURL(new Blob(['Android download policy check']));
        try {return await (await fetch(url)).text();}
        finally {URL.revokeObjectURL(url);}
      }),'Android download policy check');
      const shots=process.env.AGENTNET_SCREENSHOTS;
      const snap=async name=>{if(shots){fs.mkdirSync(shots,{recursive:true});await page.screenshot({path:`${shots}/android-comic-${width}-${name}.png`});}};
      const fits=async label=>{
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth <= innerWidth+1),true,`${width}px ${label} document overflow`);
        assert.equal(await page.locator('.an-root').evaluate(root=>root.scrollWidth<=root.clientWidth+1),true,`${width}px ${label} Comic overflow`);
      };
      const back=async()=>assert.equal(await page.evaluate(()=>window.agentnetNativeBack()),true);
      await page.getByRole('button',{name:'All',exact:true}).waitFor();
      await snap('chats'); await fits('chats');
      // Open the wrapper's real Vitalii DM through its actual phone chat row.
      await page.getByRole('list',{name:'Chats',exact:true}).getByRole('button').filter({hasText:'Vitalii'}).first().click();
      const composer=page.getByRole('form',{name:'Write a message'});
      await composer.waitFor();
      await snap('conversation'); await fits('conversation');
      const field=composer.locator('textarea');
      await field.fill('@');
      const mentions=page.locator('[id^="mentions-"][id$="-label"]');
      await mentions.waitFor({state:'visible'});
      await fits('mentions');
      await back();
      await mentions.waitFor({state:'hidden'});
      // Inline picker Back must leave both the conversation and its draft intact.
      await composer.waitFor(); assert.equal(await field.inputValue(),'@');
      await field.fill('');
      const more=page.getByRole('button',{name:'More',exact:true});
      await more.click(); await page.getByRole('menu').waitFor(); await fits('menu');
      await back(); await page.getByRole('menu').waitFor({state:'hidden'}); await composer.waitFor();
      await more.click();
      await page.getByRole('menuitem',{name:/Delete (topic|conversation)/}).click();
      await page.getByRole('alertdialog').waitFor(); await snap('dialog'); await fits('dialog');
      await back(); await page.getByRole('alertdialog').waitFor({state:'hidden'}); await composer.waitFor();
      await back(); await page.locator('section[aria-label="Chats"]').waitFor();

      // The actual mobile tab navigation, without a replacement Kotlin menu.
      const nav=page.locator('nav').filter({has:page.getByRole('button',{name:'You',exact:true})}).last();
      await nav.getByRole('button',{name:'You',exact:true}).click();
      await page.getByRole('button',{name:/Notifications/}).first().click();
      await page.getByText('Stay connected in the background',{exact:true}).waitFor();
      await snap('notifications'); await fits('notifications');
      await back();
      await page.getByRole('button',{name:/Appearance/}).first().click();
      await page.getByRole('heading',{name:'Appearance',exact:true}).waitFor();
      await page.getByText('Dark',{exact:true}).click();
      const darkRoot=page.locator('.an-root[data-theme="dark"]');
      await darkRoot.waitFor({state:'visible'});
      assert.equal(await darkRoot.evaluate(root=>getComputedStyle(root).backgroundColor),'rgb(21, 17, 30)');
      assert.equal(await page.evaluate(()=>localStorage.getItem('agentnet.theme')),'dark');
      await snap('dark-appearance'); await fits('dark appearance');
      await back();
      await page.getByRole('button',{name:/Notifications/}).first().waitFor();
      await back();
      await page.locator('section[aria-label="Chats"]').waitFor();
      // Badge text is part of the tab's accessible name (for example, "OKs 3").
      const oks=nav.getByRole('button',{name:/^OKs(?:\s|$)/});
      console.log(`${width}px OKs control: ${await oks.ariaSnapshot()}`);
      await oks.click();
      await snap('oks');
      await fits('dark OKs'); await back();
      await page.locator('section[aria-label="Chats"]').waitFor();
      await snap('dark-chats'); await fits('dark chats');
      assert.equal(await page.evaluate(()=>window.agentnetNativeBack()),false);
      assert.deepEqual(errors,[]);
      await context.close();
    }
    console.log('390/320px shared Comic Android chat, mention/menu/dialog/settings Back, dark mode, notifications and overflow checks passed');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exit(1);});
