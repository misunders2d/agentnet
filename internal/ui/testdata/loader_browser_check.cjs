// Isolated browser regression for the host-owned notification fallback.
// Use installed Playwright via AGENTNET_PLAYWRIGHT; no existing browser is attached.
// Optional AGENTNET_SCREENSHOTS writes only to an explicitly supplied directory.
const fs=require('fs'), http=require('http'), assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const base=require('path').resolve(__dirname, '../static')+'/';
const mid='a'.repeat(32), conv='b'.repeat(64), wid='c'.repeat(32);
const skin=`export function mount(root,host){root.innerHTML='<h1>Fixture messages</h1><p>Read-only synthetic workspace</p><label>Draft <input aria-label="Draft"></label>';host.onOpen((id)=>{window.opened=id});}`;
const ws=`export class WorkspaceShell{constructor(){this.active='default';this.members=new Map([['default',{connected:true}],['${wid}',{connected:true}]]);this.handlers=[]}async load(){}list(){return []}state(){return {}}bind(id){return {platform:'daemon',workspace:{id,name:id},api:async()=>({me:{address:'fixture/desktop'}})}}onChange(f){this.handlers.push(f);return ()=>{this.handlers=this.handlers.filter(x=>x!==f)}}select(id){this.active=id;this.handlers.slice().forEach(f=>f({id}))}}export function mountWorkspaceSwitcher(root){root.innerHTML='<div class="workspace-bar"></div>';return {refresh(){}}}`;
let mutations=0;
const server=http.createServer((req,res)=>{
 if(req.method!=='GET'){mutations++;res.writeHead(405).end();return}
 const u=new URL(req.url,'http://fixture');let text='',type='text/javascript';
 if(u.pathname==='/'){type='text/html';text='<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/assets/core.css"><div id="skin"></div><script src="/assets/loader.js"></script>';}
 else if(u.pathname==='/assets/skins/index.json'){type='application/json';text=JSON.stringify([{id:'default',api:1,name:'AgentNet'},{id:'fixture',api:1,name:'Fixture',digest:'fixture-v1',entry:'skin.mjs'}]);}
 else if(u.pathname==='/assets/workspaces.mjs')text=ws;
 else if(u.pathname==='/assets/skins/fixture/skin.mjs')text=skin;
 else if(u.pathname==='/assets/default.html'){type='text/html';text='<body><h1>Default fixture</h1></body>';}
 else if(['/assets/app.js','/assets/lenses.js'].includes(u.pathname))text='';
 else if(['/assets/app.css','/assets/workspaces.css'].includes(u.pathname)){type='text/css';text='';}
 else if(['/assets/loader.js','/assets/core.css'].includes(u.pathname)){type=u.pathname.endsWith('.css')?'text/css':'text/javascript';text=fs.readFileSync(base+u.pathname.split('/').pop());}
 else {res.writeHead(404).end();return}
 res.setHeader('Content-Type',type+'; charset=utf-8');res.setHeader('Content-Security-Policy',"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' blob:");res.end(text);
});
(async()=>{await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;let browser;
try{browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true});const page=await browser.newPage({viewport:{width:1280,height:800}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.addInitScript(()=>localStorage.setItem('agentnet.skin.trusted.fixture','fixture-v1'));
 await page.goto(origin+'/?skin=fixture#review&workspace='+wid);const review=page.getByRole('button',{name:'Open review in AgentNet',exact:true});await review.waitFor();
 await page.getByLabel('Draft').fill('keep this draft');assert.equal(await page.evaluate(()=>window.agentnet.workspaces.active()),'default');
 for(const [name,width,height] of [['desktop',1280,800],['phone',390,844]]){await page.setViewportSize({width,height});if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:require('path').join(process.env.AGENTNET_SCREENSHOTS,'agentnet-notice-'+name+'.png')});const bounds=await review.boundingBox();assert(bounds.x>=0&&bounds.x+bounds.width<=width);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.getByRole('button',{name:'AgentNet ▾',exact:true}).click();await page.getByRole('menuitem',{name:'Back to AgentNet',exact:true}).waitFor();await page.keyboard.press('Escape');assert.equal(await review.isVisible(),true);}
 assert.equal(await page.getByLabel('Draft').inputValue(),'keep this draft');await review.click();await page.waitForURL(u=>u.searchParams.get('skin')==='default');assert.equal(new URL(page.url()).hash,'#review&workspace='+wid);
 await page.goto(origin+'/?skin=fixture#msg='+mid+'&dir=in&workspace='+wid);await page.getByRole('button',{name:'Open message in AgentNet',exact:true}).click();await page.waitForURL(u=>u.searchParams.get('skin')==='default');assert.equal(new URL(page.url()).hash,'#msg='+mid+'&dir=in&workspace='+wid);
 await page.goto(origin+'/?skin=fixture#review&workspace=unknown');await page.getByText('Notification workspace unavailable here',{exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Open review in AgentNet',exact:true}).count(),0);assert.equal(await page.evaluate(()=>window.agentnet.workspaces.active()),'default');
 await page.evaluate(()=>{location.hash='#msg=invalid'});await page.waitForFunction(()=>document.querySelector('#host-bar').shadowRoot.querySelector('.host-notification').hidden);
 await page.evaluate(id=>{location.hash='#conv='+id},conv);await page.waitForFunction(id=>window.opened===id,conv);assert.equal(new URL(page.url()).hash,'');assert.equal(mutations,0);assert.deepEqual(errors,[]);console.log('loader notification check PASS: desktop/390, exact message/review workspace, unknown refusal, draft/menu retained, conversation route, zero mutations');
}finally{if(browser)await browser.close();await new Promise(r=>server.close(r))}})().catch(e=>{console.error(e);process.exitCode=1});
