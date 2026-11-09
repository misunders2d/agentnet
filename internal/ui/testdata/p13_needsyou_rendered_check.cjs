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
    for (const skin of ['comic']) {
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
      // The agent's needs_human result is no Review card: Review is clear.
      await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Review/}).click();
      await p.getByRole('heading',{name:'Review',exact:true}).waitFor();
      assert.equal(await p.locator('summary').filter({hasText:'Read the agent’s whole message'}).count(),0,skin+': no needs-attention card');
      assert.equal(await p.locator('[data-open]').filter({hasText:'Original request'}).count(),0,skin+': not listed in Review');
      await snap('oks-clear');
      if(mode==='phone'&&await p.getByRole('button',{name:'Back to chats',exact:true}).count())await p.getByRole('button',{name:'Back to chats',exact:true}).click();
      await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Chats/}).click();
      await p.locator('[data-part="chat-row"]').filter({hasText:'Original request'}).first().click();
      const turn=p.locator('[data-agent-needs-you]').filter({visible:true});
      // Markdown keeps paragraph nodes rather than one raw multiline text
      // node. Check every rendered word, including the final unbroken tail.
      const tail=full.split("\n").at(-1);
      await turn.getByText(tail,{exact:false}).first().waitFor();
      const normalize=s=>s.replace(/\s+/g,' ').trim();
      assert(normalize(await turn.innerText()).includes(normalize(full)),skin+': full Markdown content remains readable');
      for(const gone of ['Ask again','Mark as handled','Run it again'])assert.equal(await p.getByRole('button',{name:gone,exact:true}).count(),0,skin+': no '+gone);
      const page=await p.evaluate(()=>document.querySelector('#skin').shadowRoot.textContent);
      for(const gone of ['couldn’t finish','needs your attention','For a native permission','waits for you','Open it on'])assert(!page.includes(gone),skin+': removed '+gone);
      await turn.getByRole('button',{name:'Reply',exact:true}).waitFor();
      assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth > innerWidth+1),false,skin+': horizontal overflow');
      if(skin==='comic') await noHorizontalScroll(turn,'Comic chat '+mode);
      await snap('chat-agent-turn');
      assert.deepEqual(errors,[],skin+': runtime errors');
      await ctx.close();
    }
  } finally {await browser.close();}
  console.log('P13 rendered PASS '+mode+': Comic, Review clear, full agent message in chat, Reply only, overflow');
})().catch(e=>{console.error(e);process.exit(1);});
