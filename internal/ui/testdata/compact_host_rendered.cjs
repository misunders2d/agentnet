// Disposable public-host fixture: no live profile, Hub or permission changes.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const root=path.resolve(__dirname,'../static'),baseline=process.env.AGENTNET_COMPACT_BASELINE;
const boot=`
import {mountSkinBar} from '/assets/skinbar.mjs';
window.calls=[];window.chosen=[];
const workspace={id:'default',name:'A workspace with a deliberately long name',address:'fixture/laptop',endpoint:location.origin};
const host={workspace,api:async p=>{calls.push(p);return {me:{address:workspace.address},workspace:{name:workspace.name}};}};
const workspaces={active:()=>workspace.id,list:()=>[workspace],has:()=>true,onChange(){},join:async()=>{throw Error('inert fixture');},rename(){},disconnect(){},disconnected:async()=>[],reconnect(){}};
window.bar=mountSkinBar(document.body,{skins:()=>[{id:'comic',name:'Comic'},{id:'notebook',name:'Notebook'}],selected:{id:'notebook',name:'Notebook'},home:{id:'comic',name:'Comic'},choose:id=>chosen.push(id),host:()=>host,workspaces});
document.querySelector('#skin-action').onclick=e=>bar.open(e.currentTarget);
await bar.ready;window.ready=true;
`;
const server=http.createServer((req,res)=>{
 const p=new URL(req.url,'http://127.0.0.1').pathname;
 if(p==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Compact host fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/core.css"><div id="skin"><button id="skin-action">Skin content remains usable</button></div><script type="module" src="/boot.mjs"></script>');return;}
 if(p==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}
 const name=p.startsWith('/assets/')?p.slice(8):'';
 if(!['skinbar.mjs','skinbar.css','core.css'].includes(name)){res.writeHead(404).end();return;}
 res.setHeader('Content-Type',name.endsWith('.css')?'text/css':'text/javascript');res.end(fs.readFileSync(path.join(baseline||root,name)));
});
(async()=>{
 let browser;
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const [width,height] of [[1440,900],[760,900],[390,844],[390,320]]){
   const context=await browser.newContext({viewport:{width,height},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];page.setDefaultTimeout(4000);
   page.on('pageerror',e=>errors.push(e.message));page.on('console',m=>{if(m.type()==='error')errors.push(m.text());});
   await page.goto(origin);await page.waitForFunction(()=>window.ready);
   assert.equal(await page.title(),'Compact host fixture');assert.equal(new URL(page.url()).origin,origin);
   assert.match(await page.locator('#skin').innerText(),/Skin content remains usable/);
   const launcher=page.getByRole('button',{name:'AgentNet controls',exact:true});
   assert.equal(await launcher.count(),1,'compact host control exists');
   assert.equal(await launcher.getAttribute('aria-expanded'),'false');
   const layout=await page.locator('#skin').boundingBox();assert.equal(layout.y,0,'skin starts at viewport top');assert.equal(layout.height,height,'skin fills viewport');
   const compact=await launcher.boundingBox();assert.equal(compact.width,44);assert.equal(compact.height,44);
   await launcher.focus();await page.keyboard.press('Enter');
   const nav=page.getByRole('navigation',{name:'Skin and workspace'});await nav.waitFor();
   const skin=nav.getByRole('button',{name:/^Skin:/});await skin.focus();await page.keyboard.press('ArrowDown');
   const menu=page.getByRole('menu',{name:'Skins',exact:true});await menu.waitFor();
   const fits=async loc=>{const b=await loc.boundingBox();assert.ok(b&&b.x>=0&&b.y>=0&&b.x+b.width<=width+1&&b.y+b.height<=height+1,'panel fits viewport: '+JSON.stringify(b));};
   await fits(menu);assert.equal(await page.evaluate(()=>bar.element.shadowRoot.activeElement.getAttribute('aria-checked')),'true');
   await page.keyboard.press('Escape');assert.equal(await menu.isVisible(),false);assert.equal(await launcher.getAttribute('aria-expanded'),'true');
   await page.keyboard.press('Escape');assert.equal(await nav.isVisible(),false);assert.equal(await page.evaluate(()=>bar.element.shadowRoot.activeElement.getAttribute('aria-label')),'AgentNet controls');
   await launcher.click();await nav.getByRole('button',{name:/^Workspace:/}).click();
   await fits(page.getByRole('menu',{name:'Workspaces',exact:true}));
   await page.getByRole('menuitem',{name:/Join a workspace/}).click();
   const dialog=page.getByRole('dialog',{name:'Join a workspace',exact:true});await dialog.waitFor();await fits(dialog);
   await page.getByRole('button',{name:'Cancel',exact:true}).click();await dialog.waitFor({state:'hidden'});
   await launcher.click();await page.mouse.click(4,height-4);assert.equal(await nav.isVisible(),false,'outside pointer closes tools');
   await page.evaluate(()=>window.releaseClaim=bar.claim());assert.equal(await launcher.isVisible(),false,'native Settings claim hides floating fallback');
   await page.locator('#skin-action').click();await nav.waitFor();await page.keyboard.press('Escape');
   assert.equal(await page.locator('#skin-action').evaluate(e=>e===document.activeElement),true,'native trigger regains focus');
   await page.evaluate(()=>{releaseClaim();releaseClaim();});assert.equal(await launcher.isVisible(),true,'claim teardown is idempotent');
   await page.evaluate(()=>{window.staleRelease=bar.claim();bar.reset();window.currentRelease=bar.claim();staleRelease();});assert.equal(await launcher.isVisible(),false,'stale teardown cannot release the current skin claim');
   await page.evaluate(()=>currentRelease());assert.equal(await launcher.isVisible(),true,'failed/remounted skin restores recovery');
   await page.evaluate(()=>bar.notify('A notification is waiting.',{label:'Open it in Comic',run:()=>chosen.push('recovery')}));
   await page.getByRole('button',{name:'Open it in Comic'}).click();assert.deepEqual(await page.evaluate(()=>chosen),['recovery']);
   await page.getByRole('button',{name:'Dismiss',exact:true}).click();
   await launcher.click();await nav.getByRole('button',{name:'Switch to Comic',exact:true}).click();assert.deepEqual(await page.evaluate(()=>chosen),['recovery','comic']);
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no horizontal page overflow');
   assert.deepEqual(errors,[],'runtime and console health');
   if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`compact-host-${width}-${height}.png`)});
   console.log(width+'x'+height+' full viewport, keyboard, menus, dialog, recovery PASS');await context.close();
  }
  console.log('Compact host controls PASS');
 }finally{await browser?.close();await new Promise(r=>server.close(r));}
})().catch(e=>{console.error(e);process.exitCode=1;});
