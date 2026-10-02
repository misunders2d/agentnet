const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const origin = process.env.AGENTNET_COMBINED_URL;
const evidence = process.env.AGENTNET_SCREENSHOTS || '/tmp/agentnet-typing-combined-evidence';
fs.mkdirSync(evidence, { recursive: true });

async function control(page, name, body = {}) {
  return page.evaluate(async ({name,body}) => {
    const response = await fetch('/control/' + name, {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if (!response.ok) throw new Error('control ' + response.status);
    return response.json();
  }, { name, body });
}

(async () => {
  const browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM});
  try {
    for (const width of [1280,390]) {
      const errors = [];
      const contexts = await Promise.all([0,1].map(async () => {
        const context = await browser.newContext({viewport:{width,height:900}});
        await context.addCookies([{name:'agentnet_ui',value:process.env.AGENTNET_COMBINED_TOKEN,url:origin}]);
        await context.route('**/*',route => route.request().url().startsWith(origin + '/') ? route.continue() : route.abort());
        return context;
      }));
      const sender = await contexts[0].newPage(), receiver = await contexts[1].newPage();
      for (const page of [sender,receiver]) {
        page.on('pageerror',error => errors.push(error.message));
        page.on('console',message => {if(message.type()==='error')errors.push(message.text());});
      }
      await sender.goto(origin+'/sender');await receiver.goto(origin+'/receiver');
      await sender.waitForFunction(()=>window.ready);await receiver.waitForFunction(()=>window.ready);
      let current = false;
      const currentDeadline = Date.now() + 30000;
      while (Date.now() < currentDeadline) {
        current = await sender.evaluate(() => api('/api/typing?' + new URLSearchParams(scope)).then(view => view.current));
        if (current === true) break;
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      assert.equal(current, true, 'provider becomes current');
      await sender.evaluate(()=>ui.refresh());
      assert.match(await sender.title(),/AgentNet/);
      assert.equal(await receiver.locator('#body').isVisible(),true);
      assert.equal(await sender.evaluate(()=>document.visibilityState),'visible','trusted composer is visible');
      await sender.locator('#body').fill('Human linked-device draft');
      await receiver.getByText('Alice is typing…',{exact:true}).waitFor();
      assert.equal(await receiver.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await receiver.screenshot({path:path.join(evidence,'typing-combined-'+width+'.png')});
      await receiver.evaluate(()=>ui.setScope(null));
      assert.equal(await receiver.locator('#typing-line').isHidden(),true,'leave room clears live line');
      await receiver.evaluate(()=>ui.setScope(scope));
      await receiver.getByText('Alice is typing…',{exact:true}).waitFor();
      await receiver.evaluate(()=>ui.destroy());
      assert.equal(await receiver.locator('#typing-line').isHidden(),true,'presenter unmount clears live line');
      await receiver.evaluate(()=>ui.refresh());
      assert.equal(await receiver.locator('#typing-line').isHidden(),true,'closed presenter cannot restore old workspace line');
      await receiver.evaluate(()=>remount());
      await receiver.getByText('Alice is typing…',{exact:true}).waitFor();

      await sender.locator('#body').fill('');
      await receiver.waitForFunction(()=>document.querySelector('#typing-line').hidden);

      // A real approved background stub runs without a trusted input event.
      if (width===1280) {
        await control(sender,'run-agent');
        let started = false;
        const startedDeadline = Date.now() + 30000;
        while (Date.now() < startedDeadline) {
          started = await sender.evaluate(async () => (await (await fetch('/control/agent-started')).json()).started);
          if (started === true) break;
          await new Promise(resolve => setTimeout(resolve, 100));
        }
        assert.equal(started, true, 'real stub executor has started');
        await sender.evaluate(()=>ui.refresh());
        assert.equal(await sender.locator('#typing-line').isHidden(),true,'running agent sends no typing to peer');
        assert.equal(await receiver.locator('#typing-line').isHidden(),true,'running agent is not human typing');
        await control(sender,'release-agent');
      }
      await sender.evaluate(()=>ui.showSettings());
      await sender.evaluate(()=>{document.querySelector('#settings-profile').hidden=true;document.querySelector('#settings-notifications').hidden=false;document.querySelector('#settings-tab-profile').setAttribute('aria-pressed','false');document.querySelector('#settings-tab-notifications').setAttribute('aria-pressed','true');document.querySelector('#settings').showModal();});
      await sender.getByLabel('Share when I’m typing').uncheck();
      await sender.evaluate(()=>document.querySelector('#settings').close());
      await sender.locator('#body').fill('Opted out draft');
      await sender.waitForTimeout(150);
      assert.equal(await receiver.locator('#typing-line').isHidden(),true,'opt-out stops sharing');
      await sender.evaluate(async()=>{await api('/api/typing/preferences',{send:true,show:true});await ui.refresh();});
      await sender.locator('#body').fill('Expiry after network disconnect');
      await receiver.getByText('Alice is typing…',{exact:true}).waitFor();
      const entry = await receiver.evaluate(async()=> (await api('/api/typing?'+new URLSearchParams(scope))).entries[0]);
      const expires = Date.parse(entry.expires);
      await control(sender,'offline',{on:true});
      await sender.evaluate(()=>ui.disconnect());
      await receiver.waitForFunction(()=>document.querySelector('#typing-line').hidden,{},{timeout:6500});
      assert.ok(Date.now()>=expires-100,'disconnect expiry reached declared deadline');
      await control(sender,'offline',{on:false});
      // Full-app reconnect/new-input subscription is a separate unresolved gate.
      await receiver.evaluate(()=>events.close());
      assert.deepEqual(errors,[],'no browser runtime errors');
      for (const context of contexts) await context.close();
    }
    console.log('PASS combined production presenter -> linked Node Engine -> signed TLS Hub -> native daemon -> SSE/render: trusted input, clear, five-second disconnect expiry, background stub not typing, opt-out, scope/unmount; desktop1280/390 screenshots. Full-app reconnect/workspace adapter and Node storage limits apply.');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
