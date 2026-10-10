// The relay's device page (index.html, device.mjs, loader, engine) with each
// built-in skin, on synthetic persisted data; every relay request is
// answered here, no live relay or user data. Its server then refuses this
// page's build (owner policy "latest only": the stream's update_required
// event, 426 to the rest) and serves a newer one. With a typed draft and an
// attached file in the open chat, the page does not reload over them: it
// says why, and keeps both. Sent then, they wait in this browser's outbox
// (IndexedDB), and with nothing unsent left the page reloads by itself to
// the server's files; the queued message and its file are still there.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium,devices}=require(process.env.AGENTNET_PLAYWRIGHT);
const phone={...devices['Pixel 7'],viewport:{width:390,height:844}};
const assets=path.resolve(__dirname,'../static');
const loaderScript='<script src="/assets/loader.js" defer></script>';
const indexHTML=fs.readFileSync(path.join(assets,'index.html'),'utf8');
assert.ok(indexHTML.includes(loaderScript),'index.html still carries the loader script static.devicePage replaces');
const devicePage=indexHTML.replace(loaderScript,'<script type="module" src="/assets/device.mjs"></script>'); // as static.devicePage makes it
const draft='Draft that must survive the update',fileName='plan.txt';
const seed=`import {Engine,openIDB} from '/assets/engine.mjs';import * as w from '/assets/wire.mjs';const s=await openIDB();const e=new Engine({store:s,base:location.origin});const keys=await w.newKeys(),pub=await w.publicEntry(keys,'fixture/phone'),fp=await w.fingerprint(pub);e.keys=keys;e.address=pub.address;e.fp=fp;const own=await e.personRecord([await w.newRoster(keys,pub.address,'Fixture owner')],'self',null);e.me=own;const pk=await w.newKeys(),pp=await w.publicEntry(pk,'peer/desktop'),pf=await w.fingerprint(pp),peer=await e.personRecord([await w.newRoster(pk,pp.address,'Saved colleague')],'pinned',null);const root=await w.newRoot(keys,{person:own.person,roster:own.hash,address:pub.address,fingerprint:fp},{person:peer.person,roster:peer.hash}),conv=await w.rootID(root);const ops=[{s:'kv',k:'identity',v:{keys,address:pub.address,fingerprint:fp}},{s:'kv',k:'person',v:own},{s:'kv',k:'notices_settled',v:true},{s:'kv',k:'delivered-carrier-repair-v1',v:{done:true}},{s:'persons',k:peer.person,v:peer},{s:'pins',k:pp.address,v:{address:pp.address,fingerprint:pf,json:w.marshalPublic(pp),pending:null}},{s:'convs',k:conv,v:{id:conv,root:w.rootJSON(root),peer:peer.person,created:root.created,creator:pub.address}}];const id='1'.padStart(32,'0');ops.push({s:'inbox',k:id,v:{id,lid:id,v:2,conv,from:pp.address,fp:pf,kind:'message',body:'Saved cached message',at:1791500000000,ts:1791500000,read:true,own:false}});await s.write(ops);s.close();window.seeded={conv};`;
const outbox=`import {openIDB} from '/assets/engine.mjs';const s=await openIDB();window.outbox=(await s.all('outbox')).map(r=>({body:r.body,files:(r.files||[]).map(f=>f.ct&&f.ct.byteLength?(f.attachment||f).name:'(no ciphertext) '+(f.attachment||f).name),state:r.state||''}));s.close();`;
const skins=['comic','classic','zoom'];
const catalog=JSON.stringify(skins.map(id=>JSON.parse(fs.readFileSync(path.join(assets,'skins',id,'skin.json'),'utf8'))));
const srv=http.createServer((q,r)=>{const u=new URL(q.url,'http://x');const finish=(text,type='text/javascript')=>{r.setHeader('Content-Type',type);r.end(text);};if(u.pathname==='/seed')return finish('<script type="module" src="/seed.mjs"></script>','text/html');if(u.pathname==='/seed.mjs')return finish(seed);if(u.pathname==='/outbox')return finish('<script type="module" src="/outbox.mjs"></script>','text/html');if(u.pathname==='/outbox.mjs')return finish(outbox);if(u.pathname==='/')return finish(devicePage,'text/html');if(u.pathname==='/assets/skins/index.json')return finish(catalog,'application/json');if(u.pathname.startsWith('/assets/')){const file=path.resolve(assets,'.'+u.pathname.slice(7));if(file.startsWith(assets+'/')&&fs.existsSync(file)&&fs.statSync(file).isFile())return finish(fs.readFileSync(file),file.endsWith('.mjs')||file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');}r.statusCode=404;r.end();});
const refusal=JSON.stringify({error:'update_required',latest:'v0.8.18',url:'https://github.com/misunders2d/agentnet/releases/tag/v0.8.18',message:'Update AgentNet to v0.8.18 to continue.'});

// Each skin's own controls for the open chat's composer.
// Zoom writes in a dialog over the chat; its draft is the chat's.
const ui={
 comic:{open:p=>p.getByRole('button').filter({hasText:'Saved colleague'}).first().click(),composer:p=>p.getByRole('textbox').last(),file:p=>p.locator('input[type=file]').first(),
  waiting:p=>p.getByText(/Send or clear your drafts here/).first(),send:p=>p.getByRole('button',{name:'Send',exact:true}).click()},
 classic:{open:p=>p.getByText('Saved colleague').first().click(),composer:p=>p.locator('#body'),file:p=>p.locator('#file-input'),
  waiting:p=>p.getByText(/Send or clear drafts in every workspace before reloading/).first(),send:p=>p.locator('#send').click()},
 zoom:{open:async p=>{await p.getByRole('button').filter({hasText:'Saved colleague'}).first().click();await p.getByRole('button',{name:/Write in this DM/}).first().dispatchEvent('click');},
  composer:p=>p.locator('#write-body'),file:p=>p.locator('#write-files'),
  waiting:p=>p.getByText(/Send or clear drafts in every workspace before reloading/).first(),send:p=>p.getByRole('button',{name:'Send',exact:true}).click()},
};

(async () => {
 let browser, current, errors = [];
 const results=[];
 try {
  await new Promise(resolve => srv.listen(0, '127.0.0.1', resolve));
  const local = 'http://127.0.0.1:' + srv.address().port, base = 'https://update.example';
  browser = await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true});
  for (const skin of (process.env.AGENTNET_UPDATE_SKINS || skins.join(',')).split(',')) {
   const context = await browser.newContext({...(process.env.AGENTNET_UPDATE_DESKTOP ? {viewport:{width:1280,height:900}} : phone),serviceWorkers:'block'});
   const relay = {version:'v0.8.17', suspended:false, connected:false, held:[], refused:0};
   await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== base) return route.abort();
    if (url.pathname === '/v1/version') return route.fulfill({json:{version:relay.version,protocol:1,features:[]}});
    if (url.pathname === '/v1/stream' && relay.suspended) return route.fulfill({status:200,headers:{'Content-Type':'text/event-stream'},body:'event: update_required\ndata: '+JSON.stringify({latest:'v0.8.18',url:'https://github.com/misunders2d/agentnet/releases/tag/v0.8.18'})+'\n\n'});
    if (url.pathname === '/v1/stream' && !relay.connected) { relay.connected = true; return route.fulfill({status:200,headers:{'Content-Type':'text/event-stream'},body:'event: ping\ndata: {"conn":"c1"}\n\n'}); } // the page connects (and asks the version) once
    if (url.pathname.startsWith('/v1/')) {
     if (relay.suspended) { relay.refused++; return route.fulfill({status:426,json:JSON.parse(refusal)}); }
     relay.held.push(route); return; // a relay that has not answered yet
    }
    const response = await route.fetch({url:local + url.pathname + url.search});
    await route.fulfill({response});
   });
   await context.addInitScript(id => { try { localStorage.setItem('agentnet.skin', id); localStorage.setItem('agentnet.skin.package', id); } catch (e) {} }, skin); // the person's chosen skin
   const page = await context.newPage();
   errors = []; current = page;
   page.on('pageerror', error => errors.push(String(error)));
   await page.goto(base + '/seed');
   await page.waitForFunction(() => window.seeded, undefined, {timeout:10000});
   await page.goto(base + '/', {waitUntil:'domcontentloaded'});
   const u = ui[skin];
   await u.open(page);
   const composer = u.composer(page);
   await composer.waitFor({timeout:8000});
   await composer.fill(draft);
   await u.file(page).setInputFiles({name:fileName,mimeType:'text/plain',buffer:Buffer.from('the plan')});
   await page.getByText(fileName).filter({visible:true}).first().waitFor({timeout:5000});
   await page.waitForFunction(() => true, undefined, {timeout:1000});
   assert.ok(relay.connected, skin + ': the page connected to its server');
   await page.evaluate(() => { window.notReloaded = true; });
   // The server is updated: it refuses this page's build and serves a newer one.
   relay.version = 'v0.8.18'; relay.suspended = true;
   for (const held of relay.held.splice(0)) {
    const p = new URL(held.request().url()).pathname;
    if (p === '/v1/stream') await held.fulfill({status:200,headers:{'Content-Type':'text/event-stream'},body:'event: update_required\ndata: {"latest":"v0.8.18"}\n\n'}).catch(() => {});
    else await held.fulfill({status:426,json:JSON.parse(refusal)}).catch(() => {});
   }
   await u.waiting(page).waitFor({timeout:15000});
   assert.equal(await page.evaluate(() => window.notReloaded), true, skin + ': the page reloaded over unsent work');
   assert.equal(await composer.inputValue(), draft, skin + ': the typed draft is kept');
   await page.getByText(fileName).filter({visible:true}).first().waitFor({timeout:2000});
   // Sent now, the message and its file wait in this browser; nothing
   // unsent is left, so the page reloads by itself.
   const reloaded = page.waitForEvent('framenavigated', {timeout:20000});
   await u.send(page);
   await reloaded;
   await page.waitForLoadState('domcontentloaded');
   assert.equal(await page.evaluate(() => window.notReloaded), undefined, skin + ': not reloaded');
   await page.goto(base + '/outbox');
   const queued = await (await page.waitForFunction(() => window.outbox, undefined, {timeout:10000})).jsonValue();
   const kept = queued.find(m => m.body === draft);
   assert.ok(kept && kept.files.includes(fileName), skin + ': the sent draft and its file wait in the outbox: ' + JSON.stringify(queued));
   assert.deepEqual(errors, [], skin + ': page errors');
   results.push({skin, outbox:kept, refused:relay.refused});
   await context.close();
   current = null;
  }
  console.log(JSON.stringify(results));
  console.log('update reload keeps drafts PASS');
 } catch (error) {
  console.error(error.stack);
  console.log('diagnostic', JSON.stringify({errors}));
  if(current) console.log('diagnostic page', JSON.stringify(await current.evaluate(() => ({body:document.body.innerText.slice(0,1500),skin:document.getElementById('skin')?.shadowRoot?.textContent?.slice(0,1500)})).catch(() => null)));
  process.exitCode = 1;
 } finally {
  if(browser) await browser.close();
  srv.closeAllConnections();
  await new Promise(resolve => srv.close(resolve));
 }
})();
