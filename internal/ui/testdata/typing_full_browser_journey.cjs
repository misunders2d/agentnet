// Opt-in isolated full production browser device. Existing loopback TLS mux;
// real IndexedDB, device/loader/app/typing and signed Hub, no provider mocks.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.W;
const binary = '/tmp/agentnet-typing-full-owned/agentnet';
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world,'mux-port'),'utf8').trim();
const cli = (home,...args) => execFileSync(binary,['--home',path.join(world,home),...args],{encoding:'utf8',env:process.env});
const ready = page => page.waitForFunction(() => typeof state !== 'undefined' && state.overview);
const api = (page,url,body) => page.evaluate(({url,body})=>window.agentnet.api(url,body),{url,body});
const wait = ms => new Promise(resolve=>setTimeout(resolve,ms));
const shots = '/tmp/agentnet-typing-full-evidence';
fs.mkdirSync(shots,{recursive:true});
let browser;
(async function journey() {
  browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,env:{...process.env,HOME:process.env.AGENTNET_BROWSER_HOME}});
  const senderContext = await browser.newContext({viewport:{width:1280,height:900}});
  const peerContext = await browser.newContext({viewport:{width:1280,height:900}});
  for (const context of [senderContext,peerContext]) {
    await context.route('**/*',route=> {
      const url = new URL(route.request().url());
      return ['127.0.0.1','localhost'].includes(url.hostname) ? route.continue() : route.abort();
    });
  }
  await senderContext.addInitScript(() => {
    window.__typingReads = [];
    import('/assets/engine.mjs').then(({Engine}) => {
      const original = Engine.prototype.api;
      Engine.prototype.api = async function(url,body) {
        const presenter = new Error().stack.includes('typing.mjs');
        const value = await original.call(this,url,body);
        if (presenter && url.startsWith('/api/typing') && body === undefined) {
          window.__typingReads.push({scope:value.scope,current:value.current,supported:value.supported});
          window.__typingReads = window.__typingReads.slice(-20);
        }
        if (presenter && window.__holdTypingScope && value?.scope?.conv === window.__holdTypingScope) {
          window.__holdTypingScope = null;
          await new Promise(resolve => { window.__releaseTypingRead = resolve; });
        }
        return value;
      };
    });
  });
  const sender = await senderContext.newPage(), peer = await peerContext.newPage();
  const errors = [];
  for (const page of [sender,peer]) page.on('pageerror',error=>errors.push(error.message));
  const raw = fs.readFileSync(path.join(world,'link'),'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1],'base64url'));
  const invite = JSON.parse(Buffer.from(offer.invite.split(':')[1],'base64url'));
  delete invite.cert;
  offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  const link = 'agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url');
  await sender.goto(origin + '/#' + link);
  await sender.locator('#join-name').fill('tablet');
  await sender.locator('button.join-go').click();
  let request;
  for (let n=0;n<100&&!request;n++) {
    request = cli('alice','person','links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*tablet/m)?.[1];
    if (!request) await wait(100);
  }
  assert(request,'actual browser link request');
  cli('alice','person','approve',request);
  await ready(sender);
  await sender.reload();await ready(sender);
  const before = await sender.evaluate(()=>state.overview.me.address);
  const first = await api(sender,'/api/dm/new',{address:'bob/desk'});
  await api(sender,'/api/dm/send',{conv:first.id,body:'Known full browser conversation'});
  const peerURL = fs.readFileSync(path.join(world,'bob-ui'),'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
  await peer.goto(peerURL);await ready(peer);
  await peer.waitForFunction(id=>state.overview.dms.some(dm=>dm.id===id),first.id);
  await sender.evaluate(id=>openDM(id),first.id);
  await peer.evaluate(id=>openDM(id),first.id);
  await sender.bringToFront();
  {
    let current = false;
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      const view = await api(sender, '/api/typing?conv=' + first.id);
      current = view.current === true && view.supported === true;
      if (current) break;
      await wait(100);
    }
    assert.equal(current, true, 'signed typing provider becomes current and supported');
  }
  await sender.bringToFront();
  console.log('pre-input',JSON.stringify(await sender.evaluate(async id=>({scope:typingScope(),visibility:document.visibilityState,view:await window.agentnet.api('/api/typing?conv='+id)}),first.id)));
  sender.on('request',request=>{if(request.url().includes('signal'))console.log('signal request',new URL(request.url()).pathname);});
  await sender.locator('#body').fill('Real browser draft before disconnect');
  await peer.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  console.log('PASS trusted full app input -> native peer');
  await senderContext.setOffline(true);
  await peer.locator('#typing-line').waitFor({state:'hidden',timeout:6500});
  console.log('PASS disconnected signal expires');
  await senderContext.setOffline(false);
  await sender.bringToFront();
  {
    let current = false;
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      const view = await api(sender, '/api/typing?conv=' + first.id);
      current = view.current === true && view.supported === true;
      if (current) break;
      await wait(100);
    }
    assert.equal(current, true, 'signed typing provider becomes current and supported');
  }
  await sender.bringToFront();
  await sender.waitForFunction(()=>state.overview.device.online&&state.overview.directory.current&&!loading);
  await sender.locator('#body').fill('Fresh browser input after reconnect');
  await peer.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor({timeout:12000});
  console.log('PASS fresh reconnect input -> native peer');
  await sender.bringToFront();
  await sender.locator('#body').fill('');
  await peer.locator('#typing-line').waitFor({state:'hidden'});
  await sender.reload();await ready(sender);
  assert.equal(await sender.evaluate(()=>state.overview.me.address),before);
  assert.equal(await sender.evaluate(()=>window.agentnet.platform),'browser');
  await sender.evaluate(id=>openDM(id),first.id);
  await sender.bringToFront();
  await sender.waitForFunction(()=>state.overview.device.online&&state.overview.directory.current&&!loading);
  await sender.locator('#body').fill('Reloaded identity still typing');
  await peer.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  console.log('PASS IndexedDB reload retains identity and typing');
  for (const width of [1280,390]) {
    await sender.setViewportSize({width,height:900});
    await peer.setViewportSize({width,height:900});
    await peer.screenshot({path:path.join(shots,'typing-full-peer-'+width+'.png')});
    assert.equal(await peer.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  }
  const second = await api(sender,'/api/dm/new',{address:'bob/desk'});
  assert.notEqual(second.id,first.id);
  await api(sender,'/api/dm/send',{conv:second.id,body:'Second scope for navigation'});
  await sender.evaluate(id=>{window.__holdTypingScope=id;},first.id);
  cli('bob','dm','send',first.id,'Actual encrypted change while old typing read is pending');
  await sender.waitForFunction(()=>typeof window.__releaseTypingRead==='function');
  await sender.evaluate(id=>openDM(id),second.id);
  await sender.evaluate(()=>{window.__releaseTypingRead();window.__releaseTypingRead=null;});
  assert.equal(await sender.evaluate(()=>typingScope().conv),second.id);
  console.log('PASS pending old-scope read canceled by actual room switch');
  await peer.locator('#typing-line').waitFor({state:'hidden',timeout:2000});
  await sender.bringToFront();
  await sender.locator('#body').fill('Only second room');
  await peer.evaluate(id=>openDM(id),second.id);
  await peer.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  await sender.locator('#back').click();
  await peer.locator('#typing-line').waitFor({state:'hidden',timeout:2000});
  for (let n=0;n<3&&!await sender.locator('#nav-people').isVisible();n++) await sender.locator('#back').click();
  await sender.locator('#nav-people').click();
  await peer.locator('#typing-line').waitFor({state:'hidden',timeout:2000});
  console.log('PASS room switch exact scope and actual leave navigation stop');
  assert.deepEqual(errors,[]);
  fs.writeFileSync('/tmp/agentnet-typing-full-result.json',JSON.stringify({pass:true,identity:before,shots,errors,world},null,2));
})().catch(async error=> {
  console.error(error.message);
  if (browser) for (const [index,context] of browser.contexts().entries()) {
    const page=context.pages()[0];
    if(page) {
      console.error('Diagnostics '+index+': '+JSON.stringify(await page.evaluate(async()=>({reads:window.__typingReads,scope:typeof typingScope==='function'?typingScope():null,view:typeof state!=='undefined'&&state.dm?await window.agentnet.api('/api/typing?conv='+state.dm):null})).catch(()=>null)));
      await page.screenshot({path:path.join(shots,'failure-'+index+'.png')});
      console.error('Page '+index+': '+(await page.locator('body').innerText()).slice(0,1500));
    }
  }
  process.exitCode=1;
}).finally(async()=>{if(browser)await browser.close();});
