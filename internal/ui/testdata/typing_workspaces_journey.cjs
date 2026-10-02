// Actual production browser + two verified loopback relays, isolated IDB.
// Existing fixture pattern, no synthetic workspace/provider or signal data.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.W;
const binary = '/tmp/agentnet-typing-full-owned/agentnet';
const cli = (home,...args) => execFileSync(binary,['--home',path.join(world,home),...args],{encoding:'utf8',env:process.env});
const origin = realm => 'https://127.0.0.1:'+fs.readFileSync(path.join(world,'mux-'+realm),'utf8').trim();
const nativeURL = realm => fs.readFileSync(path.join(world,'bob-'+realm+'-ui'),'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const ready = page => page.waitForFunction(()=>typeof state!=='undefined'&&state.overview);
const api = (page,url,body) => page.evaluate(({url,body})=>window.agentnet.api(url,body),{url,body});
const appReady = page => page.waitForFunction(()=>state.overview.device?.online&&state.overview.directory.current&&!loading);
async function select(page,id) {
  await page.selectOption('#workspace-shell select',id);
  await page.evaluate(()=>state.switching);
  await page.waitForFunction(id=>window.agentnet.workspaces.active()===id&&state.overview,id);
}
let browser;
const errors = [], signals = [];
let phase='enrollment';
(async function journey() {
  browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,env:{...process.env,HOME:process.env.AGENTNET_BROWSER_HOME}});
  const contexts = await Promise.all([0,1,2].map(()=>browser.newContext({viewport:{width:1280,height:900}})));
  for (const context of contexts) await context.route('**/*',route=>['127.0.0.1','localhost'].includes(new URL(route.request().url()).hostname)?route.continue():route.abort());
  await contexts[0].addInitScript(()=> {
    import('/assets/engine.mjs').then(({Engine})=> {
      const original = Engine.prototype.api;
      Engine.prototype.api = async function(url,body) {
        const presenter = new Error().stack.includes('typing.mjs');
        const value = await original.call(this,url,body);
        if(presenter&&window.__holdRealm===this.base&&value?.entries?.length) {
          window.__holdRealm=null;
          window.__heldTyping=value;
          await new Promise(resolve=>{window.__releaseTyping=resolve;});
        }
        return value;
      };
    });
  });
  const [sender,peerA,peerB] = await Promise.all(contexts.map(context=>context.newPage()));
  for(const page of [sender,peerA,peerB])page.on('pageerror',error=>errors.push(error.message));
  sender.on('request',request=>{if(new URL(request.url()).pathname==='/v1/signal')signals.push(new URL(request.url()).origin);});
  const raw=fs.readFileSync(path.join(world,'link-a'),'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  await sender.goto(origin('a')+'/#'+raw);
  await sender.locator('#join-name').fill('tablet');await sender.locator('button.join-go').click();
  let request;
  for(let n=0;n<100&&!request;n++) {
    request=cli('alice-a','person','links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*tablet/m)?.[1];
    if(!request)await new Promise(resolve=>setTimeout(resolve,100));
  }
  assert(request);cli('alice-a','person','approve',request);
  await ready(sender);await sender.reload();await ready(sender);
  const invite=fs.readFileSync(path.join(world,'invite-b'),'utf8').trim().split('#')[1];
  await sender.locator('#workspace-shell button').filter({hasText:'Join a workspace'}).click();
  await sender.locator('.workspace-join input[placeholder="e.g. Acme"]').fill('Relay B');
  await sender.locator('.workspace-join textarea').fill(decodeURIComponent(invite));
  await sender.locator('.workspace-join input[placeholder="laptop"]').fill('tablet');
  await sender.locator('.workspace-join .btn.primary').click();
  await sender.waitForFunction(()=>window.agentnet.workspaces.list().length===2);
  const idB=await sender.evaluate(()=>window.agentnet.workspaces.list().find(w=>w.id!=='default').id);
  await select(sender,idB);await api(sender,'/api/person',{label:'Alice'});await appReady(sender);
  const convB=await api(sender,'/api/dm/new',{address:'bob/desk'});
  await api(sender,'/api/dm/send',{conv:convB.id,body:'Known realm B conversation'});
  await peerB.goto(nativeURL('b'));await ready(peerB);
  await peerB.waitForFunction(id=>state.overview.dms.some(dm=>dm.id===id),convB.id);
  await peerB.evaluate(id=>openDM(id),convB.id);
  const meB=await sender.evaluate(()=>state.overview.me.address);
  await select(sender,'default');await appReady(sender);
  const meA=await sender.evaluate(()=>state.overview.me.address);assert.equal(meA,meB);
  const convA=await api(sender,'/api/dm/new',{address:'bob/desk'});
  await api(sender,'/api/dm/send',{conv:convA.id,body:'Known realm A conversation'});
  await peerA.goto(nativeURL('a'));await ready(peerA);
  await peerA.waitForFunction(id=>state.overview.dms.some(dm=>dm.id===id),convA.id);
  await peerA.evaluate(id=>openDM(id),convA.id);
  await sender.evaluate(id=>openDM(id),convA.id);await sender.bringToFront();await appReady(sender);
  phase='A input';
  console.log('A input view',JSON.stringify(await api(sender,'/api/typing?conv='+convA.id)));
  await sender.locator('#body').fill('Saved draft A');
  await peerA.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  assert.equal(await peerB.locator('#typing-line').isVisible(),false);
  phase='switch B clears A';
  await select(sender,idB);
  await peerA.locator('#typing-line').waitFor({state:'hidden',timeout:2000});
  assert.equal(await sender.locator('#typing-line').isVisible(),false);
  await sender.evaluate(id=>openDM(id),convB.id);await appReady(sender);
  phase='B input';
  console.log('B input view',JSON.stringify(await api(sender,'/api/typing?conv='+convB.id)));
  await sender.locator('#body').fill('Fresh B input');
  await peerB.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  assert.equal(await peerA.locator('#typing-line').isVisible(),false);
  assert.equal(signals.at(-1),origin('b'));
  console.log('PASS same addresses/labels: A stop on switch; B input bound to B only');
  await sender.locator('#body').fill('');await peerB.locator('#typing-line').waitFor({state:'hidden'});
  const beforeRestoreA=signals.filter(o=>o===origin('a')).length;
  await select(sender,'default');await appReady(sender);
  assert.equal(await sender.locator('#body').inputValue(),'Saved draft A');
  assert.equal(signals.filter(o=>o===origin('a')).length,beforeRestoreA,'restored draft never emits an A signal');
  assert.equal(await peerA.locator('#typing-line').isVisible(),false);
  const before=signals.filter(o=>o===origin('a')).length;
  await sender.locator('#body').fill('Saved draft A + new trusted input');
  await peerA.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  assert(signals.filter(o=>o===origin('a')).length>before);
  assert.equal(await peerB.locator('#typing-line').isVisible(),false);
  console.log('PASS restored A draft silent; new input sends A');
  await sender.locator('#body').fill('');await peerA.locator('#typing-line').waitFor({state:'hidden'});
  await sender.evaluate(realm=>{window.__holdRealm=realm;},origin('a'));
  await peerA.bringToFront();await peerA.locator('#body').fill('Bob incoming A only');
  await sender.waitForFunction(()=>typeof window.__releaseTyping==='function');
  assert.equal(await sender.evaluate(()=>window.__heldTyping.entries[0].label),'Bob');
  phase='delayed A response after B active';
  await sender.bringToFront();await select(sender,idB);
  await sender.evaluate(()=>{window.__releaseTyping();window.__releaseTyping=null;});
  await appReady(sender);
  assert.equal(await sender.locator('#typing-line').isVisible(),false);
  assert.equal(await sender.evaluate(()=>typingScope().conv),convB.id);
  console.log('PASS delayed real A typing response cannot enter B');
  phase='A disconnected B typing';
  process.kill(Number(fs.readFileSync(path.join(world,'hub-a-pid'),'utf8')),'SIGTERM');
  let offline = false;
  const offlineDeadline = Date.now() + 30000;
  while (Date.now() < offlineDeadline) {
    const view = await sender.evaluate(() => window.agentnetWorkspaces.shell.bind('default').api('/api/overview'));
    offline = view.device.online === false;
    if (offline) break;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.equal(offline, true, 'captured A engine observes its disconnected relay');
  await appReady(sender);
  await sender.locator('#body').fill('B stays current with A relay stopped');
  await peerB.locator('#typing-line').getByText('Alice is typing…',{exact:true}).waitFor();
  assert.equal(signals.at(-1),origin('b'));
  console.log('PASS A relay disconnect does not stale B');
  assert.deepEqual(errors,[]);
  fs.writeFileSync('/tmp/agentnet-typing-workspaces-result.json',JSON.stringify({pass:true,errors,sameAddress:meA,workspaceB:idB,world},null,2));
})().catch(async error=> {
  console.error('Phase '+phase+': '+error.message+'; signed destinations '+JSON.stringify(signals));
  if(browser)for(const [n,context]of browser.contexts().entries()) {
    const page=context.pages()[0];if(page){console.error('Scope '+n+': '+JSON.stringify(await page.evaluate(async()=>({visibility:document.visibilityState,scope:typingScope(),view:typingScope()?await window.agentnet.api('/api/typing?'+new URLSearchParams(typingScope())):null})).catch(()=>null)));console.error('Page '+n+': '+(await page.locator('body').innerText()).slice(0,1200));}
  }
  process.exitCode=1;
}).finally(async()=>{if(browser)await browser.close();});
