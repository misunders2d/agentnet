const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const fs = require('node:fs');
const mode = process.env.P13_MODE, full = process.env.P13_FULL;
const shots = process.env.AGENTNET_SCREENSHOTS;
(async () => {
  const browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
  try {
    for (const skin of ['comic','classic','zoom']) {
      const ctx = await browser.newContext({viewport:mode==='phone'?{width:390,height:844}:{width:1440,height:900},isMobile:mode==='phone',hasTouch:mode==='phone'});
      const p = await ctx.newPage(), errors=[];
      console.log('P13 rendering '+mode+' '+skin);
      p.on('console',m=>{if(m.type()==='error') errors.push(m.text());});
      p.on('pageerror', e=>errors.push(String(e)));
      const snap = async name => {if(shots){fs.mkdirSync(shots,{recursive:true});await p.screenshot({path:shots+'/p13-'+mode+'-'+skin+'-'+name+'.png'});}};
      await p.goto(process.argv[2]+'&skin='+skin);
      if(skin==='comic') await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();
      else await p.locator('#review-btn').click();
      const summary=p.locator('summary').filter({hasText:'Read the agent’s whole message'}).first();
      await summary.waitFor();await summary.focus();await summary.press('Enter');
      assert.equal(await p.locator('details[open] p').first().textContent(),full,skin+': expansion keeps every paragraph');
      await snap('oks-expanded');
      if(skin==='comic') await p.getByRole('button',{name:/^Your agent couldn’t finish/}).click();
      else await p.locator('#review-list button').filter({hasText:'Your agent couldn’t finish'}).click();
      const turn=p.getByRole('region',{name:'Your agent says'});
      if(skin==='comic' && mode==='desktop') await p.locator('[data-mid="needs-request"]').getByText(full,{exact:true}).waitFor();
      else await turn.getByText(full,{exact:true}).waitFor();
      if(mode==='desktop') {
        await p.getByRole('button',{name:'Ask again',exact:true}).first().waitFor();
        await p.getByRole('button',{name:'Mark as handled',exact:true}).first().waitFor();
      } else {
        assert.equal(await p.getByRole('button',{name:'Ask again',exact:true}).count(),0,skin+': phone gained no execution action');
        await p.getByText(/Open it on/).first().waitFor();
      }
      assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth > innerWidth+1),false,skin+': horizontal overflow');
      await snap('chat-agent-turn');
      assert.deepEqual(errors,[],skin+': runtime errors');
      await ctx.close();
    }
  } finally {await browser.close();}
  console.log('P13 rendered PASS '+mode+': all skins, keyboard expansion, full chat text, actions/read-only, overflow');
})().catch(e=>{console.error(e);process.exit(1);});
