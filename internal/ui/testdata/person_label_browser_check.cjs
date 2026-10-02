// Production profile/search/dialog UI; synthetic provider only, no live identities.
const fs=require('fs'),path=require('path'),http=require('http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT||'playwright-core');
const base=path.resolve(__dirname,'../static');
const person=(id,state='pinned')=>({person:id.repeat(32),label:'Same name',address:id+'/laptop',state,published:true,fingerprint:id.repeat(32),devices:[{name:'laptop',address:id+'/laptop',fingerprint:id.repeat(32),this:state==='self'}]});
const view={me:{address:'a/laptop',fingerprint:'a'.repeat(32)},person:person('a','self'),people:[person('b'),person('c','listed')],role:'person',persons:true,threads:[],dms:[],review:[],quarantine:[],seq:0};
const bootstrap=`window.view=${JSON.stringify(view)};window.calls=[];window.refuse=false;
function fixtureHost(id){return {platform:'daemon',workspace:{id},skins:[],listen:()=>()=>{},onOpen(){},api:async(path,body)=>{calls.push({path,body,workspace:id});if(path==='/api/overview')return structuredClone(view);if(path==='/api/person/label'){if(refuse)throw Error('Display label change was not confirmed. Refresh before retrying.');view.person.label=body.label;return structuredClone(view.person);}if(path==='/api/teams')return {supported:false,teams:[]};return {};}};}
window.agentnet=fixtureHost('A');window.changeWorkspace=id=>window.agentnet=fixtureHost(id);
window.agentnetModules={typing:{mountTyping:()=>({setScope:async()=>{},stop(){},disconnect(){},destroy(){},showSettings(){}})}};`;
let writes=0;
const server=http.createServer((req,res)=>{
 if(req.method!=='GET'){writes++;res.writeHead(405).end();return;}
 const p=new URL(req.url,'http://fixture').pathname;let data,type='text/javascript';
 if(p==='/'){type='text/html';data='<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/app.css">'+fs.readFileSync(path.join(base,'default.html'),'utf8')+'<script src="/fixture.js"></script><script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>';}
 else if(p==='/fixture.js')data=bootstrap;
 else if(['/assets/app.js','/assets/lenses.js','/assets/app.css','/assets/icon-192.png'].includes(p)){type=p.endsWith('.css')?'text/css':p.endsWith('.png')?'image/png':'text/javascript';data=fs.readFileSync(path.join(base,p.endsWith('.png')?'ant.png':path.basename(p)));}
 else{res.writeHead(404).end();return;}
 res.setHeader('Content-Type',type+(type.startsWith('text/')?'; charset=utf-8':''));res.setHeader('Content-Security-Policy',"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:");res.end(data);
});
(async()=>{await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;let browser;
try{browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 for(const width of [390,1280]){
  const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage(),errors=[],external=[];
  await context.route('**/*',r=>r.request().url().startsWith(origin+'/')?r.continue():(external.push(r.request().url()),r.abort()));page.on('pageerror',e=>errors.push(e.message));
  const capture=async name=>{assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`person-${name}-${width}.png`)});};
  await page.goto(origin);await page.getByRole('button',{name:'Your profile and settings'}).click();await page.getByText('@aaaaaaaa',{exact:true}).waitFor();
  await page.getByText('You on 1 device',{exact:true}).click();await page.getByText(/Your signed person record/).waitFor();assert.equal(await page.locator('#settings').evaluate(e=>e.scrollWidth>e.clientWidth),false);await capture('identity');
  await page.getByRole('button',{name:'Change display name',exact:true}).click();assert.equal(await page.getByLabel('Display name',{exact:true}).inputValue(),'Same name');await page.getByLabel('Display name',{exact:true}).fill('Renamed person');await capture('rename');await page.getByRole('button',{name:'Save name',exact:true}).click();await page.locator('#dialog').waitFor({state:'hidden'});await page.locator('#profile-card').getByText('Renamed person',{exact:true}).waitFor();assert.equal(await page.evaluate(()=>view.person.person),'a'.repeat(32));
  await page.getByRole('button',{name:'Change display name',exact:true}).click();await page.evaluate(()=>refuse=true);await page.getByLabel('Display name',{exact:true}).fill('Unconfirmed');await page.getByRole('button',{name:'Save name',exact:true}).click();await page.getByText(/Display label change was not confirmed/).waitFor();assert.equal(await page.evaluate(()=>view.person.label),'Renamed person');await capture('refusal');await page.locator('#dialog-cancel').click();
  await page.getByRole('button',{name:'Change display name',exact:true}).click();const before=await page.evaluate(()=>calls.filter(x=>x.path==='/api/person/label').length);await page.evaluate(()=>changeWorkspace('B'));await page.getByRole('button',{name:'Save name',exact:true}).click();await page.getByText(/Your workspace or identity changed/).waitFor();assert.equal(await page.evaluate(()=>calls.filter(x=>x.path==='/api/person/label').length),before);await page.locator('#dialog-cancel').click();await page.evaluate(()=>changeWorkspace('A'));
  await page.locator('#settings-close').click();await page.locator('#search').fill('Same name');await page.getByText('· @bbbbbbbb',{exact:true}).waitFor({timeout:10000}).catch(async e=>{console.error(await page.locator('body').innerText());console.error({errors});throw e;});await page.getByText('· @cccccccc',{exact:true}).waitFor();await capture('same-names');
  assert.deepEqual(errors,[]);assert.deepEqual(external,[]);await context.close();
 }
 assert.equal(writes,0);console.log('person UI PASS: same-name IDs, signed/claimed wording, rename/refusal, workspace-bound submit, desktop/390');
}finally{if(browser)await browser.close();await new Promise(r=>server.close(r));}})().catch(e=>{console.error(e);process.exitCode=1});
