// Actual browser device, loader and Comic startup on synthetic persisted data.
// Every request is intercepted at the fixture origin; no live relay or user data.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const assets=path.resolve(__dirname,'../static');
const traces=[],held=[];let catalogDelay=0;
const instrument=`import {Engine} from '/assets/engine.mjs';window.stage=[];for(const name of ['load','overview','dm','start','streamOnce','notifyView','notifyInfo','features']){const old=Engine.prototype[name];Engine.prototype[name]=function(...a){const t=performance.now();stage.push({name,at:t,event:'start'});const r=old.apply(this,a);if(r&&typeof r.then==='function')return r.then(v=>{stage.push({name,at:performance.now(),event:'end',ms:performance.now()-t});return v;},e=>{stage.push({name,at:performance.now(),event:'error',error:String(e)});throw e;});stage.push({name,at:performance.now(),event:'end',ms:performance.now()-t});return r;};}import('/assets/device.mjs');`;
const seed=`import {Engine,openIDB} from '/assets/engine.mjs';import * as w from '/assets/wire.mjs';const s=await openIDB();const e=new Engine({store:s,base:location.origin});const keys=await w.newKeys(),pub=await w.publicEntry(keys,'fixture/phone'),fp=await w.fingerprint(pub);e.keys=keys;e.address=pub.address;e.fp=fp;const own=await e.personRecord([await w.newRoster(keys,pub.address,'Fixture owner')],'self',null);e.me=own;const pk=await w.newKeys(),pp=await w.publicEntry(pk,'peer/desktop'),pf=await w.fingerprint(pp),peer=await e.personRecord([await w.newRoster(pk,pp.address,'Saved colleague')],'pinned',null);const root=await w.newRoot(keys,{person:own.person,roster:own.hash,address:pub.address,fingerprint:fp},{person:peer.person,roster:peer.hash}),conv=await w.rootID(root);const ops=[{s:'kv',k:'identity',v:{keys,address:pub.address,fingerprint:fp}},{s:'kv',k:'person',v:own},{s:'kv',k:'notices_settled',v:true},{s:'kv',k:'delivered-carrier-repair-v1',v:{done:true}},{s:'persons',k:peer.person,v:peer},{s:'pins',k:pp.address,v:{address:pp.address,fingerprint:pf,json:w.marshalPublic(pp),pending:null}},{s:'convs',k:conv,v:{id:conv,root:w.rootJSON(root),peer:peer.person,created:root.created,creator:pub.address}}];for(let i=0;i<500;i++){const id=(i+1).toString(16).padStart(32,'0');ops.push({s:'inbox',k:id,v:{id,lid:id,v:2,conv,from:pp.address,fp:pf,kind:'message',body:'Saved cached message '+i,at:1791500000000+i*1000,ts:1791500000+i,read:true,own:false}});}await s.write(ops);s.close();window.seeded={messages:500,conv};`;
const srv=http.createServer((q,r)=>{const u=new URL(q.url,'http://x');traces.push({path:u.pathname,at:Date.now()});const finish=(text,type='text/javascript')=>{r.setHeader('Content-Type',type);r.end(text);};if(u.pathname==='/seed')return finish('<script type="module" src="/seed.mjs"></script>','text/html');if(u.pathname==='/seed.mjs')return finish(seed);if(u.pathname==='/device')return finish('<!doctype html><meta name="viewport" content="width=device-width"><style>html,body{margin:0;height:100%}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>','text/html');if(u.pathname==='/boot.mjs')return finish(instrument);if(u.pathname.startsWith('/v1/')){held.push(r);q.on('close',()=>{});return;}if(u.pathname==='/assets/skins/index.json'){const m=JSON.parse(fs.readFileSync(path.join(assets,'skins/comic/skin.json'),'utf8'));return setTimeout(()=>finish(JSON.stringify([m]),'application/json'),catalogDelay);}if(u.pathname.startsWith('/assets/')){const file=path.resolve(assets,'.'+u.pathname.slice(7));if(file.startsWith(assets+'/')&&fs.existsSync(file)&&fs.statSync(file).isFile())return finish(fs.readFileSync(file),file.endsWith('.mjs')||file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');}r.statusCode=404;r.end();});

(async () => {
 let browser, current;
 try {
  await new Promise(resolve => srv.listen(0, '127.0.0.1', resolve));
  const local = 'http://127.0.0.1:' + srv.address().port, base = 'https://startup.example';
  browser = await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true});
  for (const mode of ['held', 'offline']) {
   const context = await browser.newContext({viewport:{width:390,height:844},serviceWorkers:'block'});
   await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== base) return route.abort();
    if (url.pathname.startsWith('/v1/')) {
     if (mode === 'offline') return route.abort();
     return; // intentionally suspended: the UI must not await relay recovery
    }
    const response = await route.fetch({url:local + url.pathname + url.search});
    await route.fulfill({response});
   });
   const page = await context.newPage(), errors = [];
   current = page;
   page.on('pageerror', error => errors.push(String(error)));
   await page.goto(base + '/seed');
   await page.waitForFunction(() => window.seeded, undefined, {timeout:10000});
   const started = Date.now();
   await page.goto(base + '/device', {waitUntil:'domcontentloaded'});
   const chat = page.getByText('Saved colleague', {exact:false}).first();
   await chat.waitFor({timeout:5000});
   const firstChatListMs = Date.now() - started;
   await chat.click();
   await page.locator('[data-card]').getByText('Saved cached message 499', {exact:true}).waitFor({timeout:5000});
   const firstSavedMessageMs = Date.now() - started;
   const overview = await page.evaluate(() => window.agentnetEngine.api('/api/overview?topics=1'));
   assert.equal(overview.notify.available, false, 'unverified notification support is not available');
   assert.equal(overview.notify.enabled, false);
   assert.match(overview.notify.reason, /not been checked/, 'unknown is distinct from unsupported');
   assert.equal(errors.length, 0, errors.join('\n'));
   console.log(JSON.stringify({mode,messages:500,firstChatListMs,firstSavedMessageMs,stage:await page.evaluate(() => window.stage)}));
   await context.close();
  }
  console.log('cached device Comic real IndexedDB startup PASS (500 saved messages; held/offline relay)');
 } catch (error) {
  console.error(error.stack);
  if(current) console.log('diagnostic', JSON.stringify(await current.evaluate(() => ({stage:window.stage,body:document.body.innerText,skin:document.getElementById('skin')?.shadowRoot?.textContent})).catch(() => null)));
  process.exitCode = 1;
 } finally {
  if(browser) await browser.close();
  for(const response of held) response.destroy();
  srv.closeAllConnections();
  await new Promise(resolve => srv.close(resolve));
 }
})();
