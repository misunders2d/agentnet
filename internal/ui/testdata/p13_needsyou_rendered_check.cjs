const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const fs = require('node:fs');
const mode = process.env.P13_MODE, full = process.env.P13_FULL;
const shots = process.env.AGENTNET_SCREENSHOTS;
// Document width misses overflow inside the skin's nested columns. Check
// every ancestor, including hidden frames that focus can scroll sideways.
async function noHorizontalScroll(target, label) {
  const boxes = await target.evaluate(async el => {
    const ancestors = [];
    for (let box=el; box; box=box.parentElement) ancestors.push(box);
    // Focus/navigation happen during the real slide; measure once it settles.
    await Promise.all(ancestors.flatMap(box=>box.getAnimations()).filter(a=>
      a instanceof CSSAnimation && a.effect.getTiming().iterations!==Infinity
    ).map(a=>a.finished.catch(()=>{})));
    return ancestors.map(box=>({
      tag:box.tagName, classes:box.className,
      width:box.clientWidth, scrollWidth:box.scrollWidth, left:box.scrollLeft
    })).filter(box=>box.width>0);
  });
  assert(boxes.length>0,label+': no containers measured');
  for (const box of boxes) {
    assert(box.scrollWidth<=box.width+1,label+': horizontal overflow '+JSON.stringify(box));
    assert(Math.abs(box.left)<=1,label+': shifted horizontally '+JSON.stringify(box));
  }
}
(async () => {
  const browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
  try {
    for (const skin of ['comic','classic','zoom']) {
      const ctx = await browser.newContext({viewport:mode==='phone'?{width:390,height:844}:{width:1440,height:900},isMobile:mode==='phone',hasTouch:mode==='phone'});
      const p = await ctx.newPage(), errors=[];
      console.log('P13 rendering '+mode+' '+skin);
      // The stand-in server answers only this check's routes; the page's other API
      // calls (agents, workspaces, typing…) 404 there and are not page errors.
      p.on('console',m=>{if(m.type()==='error' && !/^Failed to load resource: the server responded with a status of 404/.test(m.text())) errors.push(m.text());});
      p.on('pageerror', e=>errors.push(String(e)));
      const snap = async name => {if(shots){fs.mkdirSync(shots,{recursive:true});await p.screenshot({path:shots+'/p13-'+mode+'-'+skin+'-'+name+'.png'});}};
      // The token address becomes a cookie by a redirect that keeps no other
      // parameter, so the skin is chosen on a second visit.
      await p.goto(process.argv[2]);
      await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);
      if(skin==='comic') await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();
      else await p.locator('#review-btn').click();
      const summary=p.locator('summary').filter({hasText:'Read the agent’s whole message'}).first();
      await summary.waitFor();await summary.focus();await summary.press('Enter');
      assert.equal(await p.locator('details[open] p').first().textContent(),full,skin+': expansion keeps every paragraph');
      if(skin==='comic') await noHorizontalScroll(summary,'Comic OKs '+mode);
      await snap('oks-expanded');
      if(skin==='comic') await p.getByRole('button',{name:/^Your agent couldn’t finish/}).click();
      else await p.locator('#review-list button').filter({hasText:'Your agent couldn’t finish'}).click();
      const turn=p.getByRole('region',{name:'Your agent says'});
      // Zoom's motion keeps the old and new view for a moment: the turn must settle to one copy.
      for(let i=0;i<50 && await turn.getByText(full,{exact:true}).count()!==1;i++) await p.waitForTimeout(100);
      if(skin==='comic' && mode==='desktop') await p.locator('[data-mid="needs-request"]').getByText(full,{exact:true}).waitFor();
      else await turn.getByText(full,{exact:true}).waitFor();
      if(mode==='desktop') {
        await p.getByRole('button',{name:'Ask again',exact:true}).first().waitFor();
        await p.getByRole('button',{name:'Mark as handled',exact:true}).first().waitFor();
      } else {
        assert.equal(await p.getByRole('button',{name:'Ask again',exact:true}).count(),0,skin+': phone gained no execution action');
        await p.getByText(/Open it on/).locator('visible=true').first().waitFor(); // the closed OKs panel keeps a hidden copy
      }
      assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth > innerWidth+1),false,skin+': horizontal overflow');
      if(skin==='comic') await noHorizontalScroll(turn,'Comic chat '+mode);
      await snap('chat-agent-turn');
      assert.deepEqual(errors,[],skin+': runtime errors');
      await ctx.close();
    }
  } finally {await browser.close();}
  console.log('P13 rendered PASS '+mode+': all skins, keyboard expansion, full chat text, actions/read-only, overflow');
})().catch(e=>{console.error(e);process.exit(1);});
