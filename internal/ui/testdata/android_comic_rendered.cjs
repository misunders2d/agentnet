const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const fs = require('node:fs');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  try {
    for (const width of [390, 320]) {
      const context = await browser.newContext({viewport:{width,height:844},colorScheme:'light',isMobile:true,hasTouch:true});
      const page = await context.newPage();
      const errors=[], appHTTP=[];page.on('pageerror',e=>errors.push(String(e)));
      page.on('request',request=>{if(new URL(request.url()).pathname.startsWith('/api/app/'))appHTTP.push(request.url());});
      await page.addInitScript(() => {
        window.androidAppCalls=[];window.androidNotificationGranted=true;window.androidNotificationCalls=[];
        window.androidNotificationChannel={importance:3,sound:true,vibration:true};
        window.androidUpdateResult='';
        let updateCalls=0;
        window.AgentNetAndroid = { postMessage(raw) {
          const request=JSON.parse(raw);
          if(request.type.startsWith('notifications:')) {
            window.androidNotificationCalls.push(request.type);
            const channel=window.androidNotificationChannel;
            const status={granted:window.androidNotificationGranted && channel.importance>0,app_granted:window.androidNotificationGranted,channel};
            queueMicrotask(()=>this.onmessage?.({data:JSON.stringify({id:request.id,ok:true,...(request.type==='notifications:settings'?{opened:true}:status)})}));return;
          }
          if (request.type.startsWith('app:')) {
            window.androidAppCalls.push(request.type);
            let result;
            if(request.type==='app:status') result={version:'v0.8.17',app_update_supported:true,update_result:window.androidUpdateResult};
            if(request.type==='app:check') result={state:'available',version:'v0.8.17',latest:'v0.8.18'};
            if(request.type==='app:update') {
              updateCalls++;
              if(updateCalls===1) {queueMicrotask(()=>this.onmessage?.({data:JSON.stringify({id:request.id,ok:false,error:'Release APK is unavailable. Try again later.'})}));return;}
              result={state:'installer',message:'Android installer opened.'};
            }
            queueMicrotask(()=>this.onmessage?.({data:JSON.stringify({id:request.id,ok:true,result})}));return;
          }
          queueMicrotask(()=>this.onmessage?.({data:JSON.stringify({id:request.id,ok:true,enabled:false,granted:true,drafts:[]})}));
        }};
      });
      await context.route('**/api/overview*', async route => {
        const response=await route.fetch(), value=await response.json();
        value.notify={...(value.notify||{}),enabled:true,available:true,native:true};
        await route.fulfill({response,json:value});
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
      // A relay lookup timeout keeps one exact saved request and gives the
      // phone a readable recovery action, not an overflowing raw URL toast.
      const attempts=[];
      const failure='Get "https://relay.example/v1/agents/owner/'+ 'x'.repeat(180) + '/profile": context deadline exceeded';
      await context.route('**/api/dm/send',async route=>{
        attempts.push(route.request().postDataJSON());
        if(attempts.length===1) await route.fulfill({status:500,contentType:'text/plain',body:failure});
        else await route.continue();
      });
      await field.fill('Keep this exact mobile send');
      await composer.getByRole('button',{name:'Send',exact:true}).click();
      await page.getByText('Message saved on this phone. Open Saved messages at the top to retry.',{exact:true}).waitFor();
      await page.getByRole('button',{name:'Saved messages (1) · Review',exact:true}).click();
      await page.getByRole('heading',{name:'Saved messages',exact:true}).waitFor();
      const why=page.getByText('Why sending wasn’t confirmed',{exact:true});
      await why.click();
      await page.getByText(failure,{exact:true}).waitFor();
      await fits('saved send and long failure detail');await snap('saved-send');
      assert.equal(attempts.length,1,'opening recovery must not replay');
      await page.getByRole('button',{name:'Retry saved send',exact:true}).click();
      await page.getByRole('heading',{name:'Saved messages',exact:true}).waitFor({state:'hidden'});
      assert.equal(attempts.length,2);assert.deepEqual(attempts[1],attempts[0],'retry must retain exact recipient, body and ID');
      await context.unroute('**/api/dm/send');
      await composer.waitFor();
      await back(); await page.locator('section[aria-label="Chats"]').waitFor();

      // The actual mobile tab navigation, without a replacement Kotlin menu.
      const nav=page.locator('nav').filter({has:page.getByRole('button',{name:'You',exact:true})}).last();
      await nav.getByRole('button',{name:'You',exact:true}).click();
      await page.getByRole('button',{name:/About/}).first().click();
      await page.getByRole('heading',{name:'Update this phone',exact:true}).waitFor();
      await page.getByRole('button',{name:'Check for updates',exact:true}).click();
      await page.getByRole('heading',{name:'Version v0.8.18 is available.',exact:true}).waitFor();
      await page.getByRole('button',{name:'Update AgentNet',exact:true}).click();
      await page.getByRole('alert').filter({hasText:'Release APK is unavailable.'}).waitFor();
      await page.getByRole('button',{name:'Update AgentNet',exact:true}).click();
      await page.getByText('Android installer opened.',{exact:true}).waitFor();
      await page.evaluate(()=>{
        window.androidUpdateResult='Installation canceled. You can try again.';
        window.agentnetNativeVisibility(true);
      });
      await page.getByText('Installation canceled. You can try again.',{exact:true}).waitFor();
      assert.equal(await page.getByText('Android installer opened.',{exact:true}).count(),0);
      assert.equal(await page.getByText('AgentNet command for your tools',{exact:true}).count(),0);
      assert.deepEqual(await page.evaluate(()=>window.androidAppCalls),['app:status','app:check','app:update','app:update','app:status']);
      assert.deepEqual(appHTTP,[],'Android updates must not call the absent desktop HTTP routes');
      await fits('About native update failure and retry'); await snap('about-update');
      await back();
      await page.evaluate(()=>{window.androidNotificationGranted=false;window.agentnetNativeVisibility(true);});
      await page.getByRole('button',{name:/Notifications/}).first().click();
      await page.getByText('Stay connected in the background',{exact:true}).waitFor();
      await page.getByText(/Notifications are blocked in Android settings/).waitFor();
      const notificationToggle=page.getByRole('switch',{name:'Notifications',exact:true});
      assert.equal(await notificationToggle.getAttribute('aria-checked'),'false','stored On never overrules OS denial');
      await notificationToggle.click();
      await page.getByText('Allow notifications in Android settings to receive alerts.',{exact:true}).waitFor();
      assert.equal(await notificationToggle.getAttribute('aria-checked'),'false');
      assert.equal(await page.evaluate(()=>window.androidNotificationCalls.filter(x=>x==='notifications:request').length),1,'only explicit click requests permission');
      await page.getByRole('button',{name:'Android notification settings',exact:true}).click();
      assert.equal(await page.evaluate(()=>window.androidNotificationCalls.filter(x=>x==='notifications:settings').length),1);
      await page.evaluate(()=>{window.androidNotificationGranted=true;window.androidNotificationChannel.vibration=false;window.agentnetNativeVisibility(true);});
      await page.getByText(/Chat activity vibration is off in Android/).waitFor();
      assert.equal(await notificationToggle.getAttribute('aria-checked'),'true','vibration-off does not disable permitted visual alerts');
      await page.getByRole('button',{name:'Android notification settings',exact:true}).click();
      assert.equal(await page.evaluate(()=>window.androidNotificationChannel.vibration),false,'recovery action preserves a legacy saved choice');
      await page.evaluate(()=>{window.androidNotificationChannel={importance:2,sound:false,vibration:false};window.agentnetNativeVisibility(true);});
      await page.getByText(/Chat activity is set to silent in Android/).waitFor();
      await page.evaluate(()=>{window.androidNotificationChannel={importance:0,sound:false,vibration:false};window.agentnetNativeVisibility(true);});
      await page.getByText(/Chat activity notifications are blocked in Android settings/).waitFor();
      assert.equal(await notificationToggle.getAttribute('aria-checked'),'false');
      await page.evaluate(()=>{window.androidNotificationChannel={importance:3,sound:true,vibration:true};window.agentnetNativeVisibility(true);});
      await page.getByText(/Chat activity notifications are blocked/).waitFor({state:'hidden'});
      assert.equal(await page.getByText(/Chat activity vibration is off/).count(),0);
      assert.equal(await notificationToggle.getAttribute('aria-checked'),'true');
      assert.equal(await page.evaluate(()=>window.androidNotificationCalls.filter(x=>x==='notifications:request').length),1,'status refresh and settings recovery never request a new permission');
      await context.unroute('**/api/overview*');
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
      const oks=nav.getByRole('button',{name:/^Review(?:\s|$)/});
      console.log(`${width}px OKs control: ${await oks.ariaSnapshot()}`);
      await oks.click();
      await snap('oks');
      await fits('dark OKs'); await back();
      await page.locator('section[aria-label="Chats"]').waitFor();
      await snap('dark-chats'); await fits('dark chats');
      assert.equal(await page.evaluate(()=>window.agentnetNativeBack()),false);

      // A suspended old APK offers the same native action from the banner,
      // and final installer feedback remains visible without navigation.
      await context.route('**/api/overview*', async route => {
        const response=await route.fetch(), value=await response.json();
        value.app=true; value.update_required={latest:'v0.8.18'};
        await route.fulfill({response,json:value});
      });
      await page.reload();
      await page.getByText('Update AgentNet to v0.8.18 to continue',{exact:true}).waitFor();
      const updateNow=page.getByRole('button',{name:'Update now',exact:true});
      await updateNow.click();
      await page.getByText('Release APK is unavailable. Try again later.',{exact:true}).waitFor();
      await updateNow.click();
      await page.getByText('Android installer opened.',{exact:true}).waitFor();
      await page.evaluate(()=>{
        window.androidUpdateResult='Installation canceled. You can try again.';
        window.agentnetNativeVisibility(true);
      });
      await page.getByText('Installation canceled. You can try again.',{exact:true}).waitFor();
      assert.equal(await updateNow.isEnabled(),true,'canceled installation remains retryable');
      assert.equal(await page.getByText('Android installer opened.',{exact:true}).count(),0);
      assert.deepEqual(appHTTP,[]);
      await fits('required update native result'); await snap('required-update');
      await context.unroute('**/api/overview*');

      // The native page stays available when its relay loop has stopped.
      // Project that failure honestly, and name the exact copied-link source.
      await context.route('**/api/overview*', async route => {
        const response = await route.fetch();
        const value = await response.json();
        value.person = null;
        value.role = 'person';
        value.link = {state:'pending', approver:'owner/linux-laptop', expires:'2026-10-10T23:59:00Z'};
        value.transport_error = 'Synthetic receiving connection failure';
        await route.fulfill({response,json:value});
      });
      await page.reload();
      await page.getByText('The receiving connection stopped. Your saved chats are still here.',{exact:true}).waitFor();
      await nav.getByRole('button',{name:'You',exact:true}).click();
      await page.getByRole('button',{name:/Waiting for your other device/}).click();
      await page.getByText('Waiting for approval on Linux laptop.',{exact:true}).waitFor();
      await page.getByText(/You → Your devices and approve this device/).waitFor();
      await page.getByText(/Approval window ends at/).waitFor();
      await fits('link source and transport failure');
      await snap('link-source');
      assert.deepEqual(errors,[]);
      await context.close();
    }
    console.log('390/320px shared Comic Android chat, mention/menu/dialog/settings Back, dark mode, notifications and overflow checks passed');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exit(1);});
