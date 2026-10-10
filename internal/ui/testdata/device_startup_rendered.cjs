// Actual browser device, loader and Comic startup on synthetic persisted data.
// Every request is intercepted at the fixture origin; no live relay or user data.
// The page is the relay's device page (index.html, as static.devicePage makes
// it) with one instrumenting module in front of device.mjs. Every /v1/ request
// is suspended ("held") or refused ("offline"): the realm/version probe the
// workspace transport puts before every relay request is therefore unanswered,
// as with a stalled or unreachable relay. Static assets, including the skin
// catalog, are served at once: asset round trips are not modelled here.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium,devices}=require(process.env.AGENTNET_PLAYWRIGHT);
const phone={...devices['Pixel 7'],viewport:{width:390,height:844}}; // a phone's browser: landing.mjs takes its phone paths (no desktop app banner)
const assets=path.resolve(__dirname,'../static');
const messages=500,newestID=messages.toString(16).padStart(32,'0'),newestBody='Saved cached message '+(messages-1);
const loaderScript='<script src="/assets/loader.js" defer></script>';
const indexHTML=fs.readFileSync(path.join(assets,'index.html'),'utf8');
assert.ok(indexHTML.includes(loaderScript),'index.html still carries the loader script static.devicePage replaces');
const devicePage=indexHTML.replace(loaderScript,'<script type="module" src="/boot.mjs"></script>');
const instrument=`import {Engine} from '/assets/engine.mjs';window.stage=[];for(const name of ['load','overview','dm','start','streamOnce','notifyView','notifyInfo','features']){const old=Engine.prototype[name];Engine.prototype[name]=function(...a){const t=performance.now();stage.push({name,at:t,event:'start'});const r=old.apply(this,a);if(r&&typeof r.then==='function')return r.then(v=>{stage.push({name,at:performance.now(),event:'end',ms:performance.now()-t});return v;},e=>{stage.push({name,at:performance.now(),event:'error',error:String(e)});throw e;});stage.push({name,at:performance.now(),event:'end',ms:performance.now()-t});return r;};}import('/assets/device.mjs');`;
const seed=`import {Engine,openIDB} from '/assets/engine.mjs';import * as w from '/assets/wire.mjs';const s=await openIDB();const e=new Engine({store:s,base:location.origin});const keys=await w.newKeys(),pub=await w.publicEntry(keys,'fixture/phone'),fp=await w.fingerprint(pub);e.keys=keys;e.address=pub.address;e.fp=fp;const own=await e.personRecord([await w.newRoster(keys,pub.address,'Fixture owner')],'self',null);e.me=own;const pk=await w.newKeys(),pp=await w.publicEntry(pk,'peer/desktop'),pf=await w.fingerprint(pp),peer=await e.personRecord([await w.newRoster(pk,pp.address,'Saved colleague')],'pinned',null);const root=await w.newRoot(keys,{person:own.person,roster:own.hash,address:pub.address,fingerprint:fp},{person:peer.person,roster:peer.hash}),conv=await w.rootID(root);const ops=[{s:'kv',k:'identity',v:{keys,address:pub.address,fingerprint:fp}},{s:'kv',k:'person',v:own},{s:'kv',k:'notices_settled',v:true},{s:'kv',k:'delivered-carrier-repair-v1',v:{done:true}},{s:'persons',k:peer.person,v:peer},{s:'pins',k:pp.address,v:{address:pp.address,fingerprint:pf,json:w.marshalPublic(pp),pending:null}},{s:'convs',k:conv,v:{id:conv,root:w.rootJSON(root),peer:peer.person,created:root.created,creator:pub.address}}];for(let i=0;i<${messages};i++){const id=(i+1).toString(16).padStart(32,'0');ops.push({s:'inbox',k:id,v:{id,lid:id,v:2,conv,from:pp.address,fp:pf,kind:'message',body:'Saved cached message '+i,at:1791500000000+i*1000,ts:1791500000+i,read:true,own:false}});}await s.write(ops);s.close();window.seeded={messages:${messages},conv};`;
const srv=http.createServer((q,r)=>{const u=new URL(q.url,'http://x');const finish=(text,type='text/javascript')=>{r.setHeader('Content-Type',type);r.end(text);};if(u.pathname==='/seed')return finish('<script type="module" src="/seed.mjs"></script>','text/html');if(u.pathname==='/seed.mjs')return finish(seed);if(u.pathname==='/')return finish(devicePage,'text/html');if(u.pathname==='/boot.mjs')return finish(instrument);if(u.pathname==='/assets/skins/index.json')return finish(JSON.stringify([JSON.parse(fs.readFileSync(path.join(assets,'skins/comic/skin.json'),'utf8'))]),'application/json');if(u.pathname.startsWith('/assets/')){const file=path.resolve(assets,'.'+u.pathname.slice(7));if(file.startsWith(assets+'/')&&fs.existsSync(file)&&fs.statSync(file).isFile())return finish(fs.readFileSync(file),file.endsWith('.mjs')||file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream');}r.statusCode=404;r.end();});

// newestInView: the open conversation card shows the newest saved message on
// screen without the test scrolling. Comic opens a timeline at its bottom
// (Conversation.timeline.tsx); the message row is [data-mid] and its body
// shares a paragraph with its time, so the row is found by id, not exact text.
const newestInView=([id,body])=>{
 const sr=document.getElementById('skin')?.shadowRoot,card=sr?.querySelector('[data-card]:not([aria-hidden])');
 const rows=card?[...card.querySelectorAll('[data-mid]')]:[],row=rows.at(-1);
 if(!row||row.dataset.mid!==id||!row.textContent.includes(body))return false;
 const log=row.closest('[role=log]'),box=log?.parentElement;
 if(!box||box.scrollHeight-box.scrollTop-box.clientHeight>=80)return false; // Comic's own at-bottom threshold
 const r=row.getBoundingClientRect(),b=box.getBoundingClientRect();
 if(r.height<=0||r.top<Math.max(0,b.top)||r.bottom>Math.min(innerHeight,b.bottom)||r.left<0||r.right>innerWidth)return false;
 const hit=sr.elementFromPoint(r.left+r.width/2,r.top+r.height/2);
 return !!hit&&row.contains(hit)&&{rendered:rows.length,top:Math.round(r.top),bottom:Math.round(r.bottom)};
};

(async () => {
 let browser, current, errors = [], relay = null;
 try {
  await new Promise(resolve => srv.listen(0, '127.0.0.1', resolve));
  const local = 'http://127.0.0.1:' + srv.address().port, base = 'https://startup.example';
  browser = await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true});
  for (const mode of ['held', 'offline']) {
   const context = await browser.newContext({...phone,serviceWorkers:'block'});
   relay = {mode, held:[], refused:[]};
   const seen = relay;
   await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== base) return route.abort();
    if (url.pathname.startsWith('/v1/')) {
     if (mode === 'offline') { seen.refused.push(url.pathname); return route.abort('internetdisconnected'); }
     seen.held.push(url.pathname);
     return; // intentionally suspended: the UI must not await relay recovery
    }
    const response = await route.fetch({url:local + url.pathname + url.search});
    await route.fulfill({response});
   });
   const page = await context.newPage();
   errors = [];
   current = page;
   page.on('pageerror', error => errors.push(String(error)));
   await page.goto(base + '/seed');
   await page.waitForFunction(() => window.seeded, undefined, {timeout:10000});
   assert.equal(errors.length, 0, 'seeding: ' + errors.join('\n'));
   const started = Date.now();
   await page.goto(base + '/', {waitUntil:'domcontentloaded'});
   // The saved chat in the Chats list, with its newest saved message as preview.
   const chat = page.getByRole('button').filter({hasText:'Saved colleague'}).filter({hasText:newestBody});
   // Comic catches a failed overview and shows "Your chats didn't load", so an
   // engine exception is no pageerror: a timeout names any failed stage.
   const explain = async error => { const failed = await page.evaluate(() => (window.stage || []).filter(s => s.event === 'error')).catch(() => []); throw failed.length ? new Error('startup stage failed: ' + JSON.stringify(failed)) : error; };
   await chat.waitFor({timeout:5000}).catch(explain);
   const firstChatListMs = Date.now() - started;
   assert.equal(errors.length, 0, 'chat list: ' + errors.join('\n'));
   await chat.tap();
   const shown = await (await page.waitForFunction(newestInView, [newestID, newestBody], {timeout:5000}).catch(explain)).jsonValue();
   const firstSavedMessageMs = Date.now() - started;
   assert.equal(errors.length, 0, 'conversation: ' + errors.join('\n'));
   const overview = await page.evaluate(() => window.agentnetEngine.api('/api/overview?topics=1'));
   assert.equal(overview.notify.available, false, 'unverified notification support is not available');
   assert.equal(overview.notify.enabled, false);
   assert.match(overview.notify.reason, /not been checked/, 'unknown is distinct from unsupported');
   assert.equal(overview.device.online, false, 'no relay connection is claimed');
   const stage = await page.evaluate(() => window.stage);
   for (const name of ['load', 'overview', 'dm', 'notifyView']) assert.ok(!stage.some(s => s.name === name && s.event === 'error'), name + ' failed: ' + JSON.stringify(stage.filter(s => s.event === 'error')));
   // The workspace realm check (GET /v1/version) was really issued, and was
   // still unanswered (held) or refused (offline) while the saved chat showed.
   if (mode === 'held') assert.ok(relay.held.includes('/v1/version'), 'realm check held: ' + JSON.stringify(relay));
   else assert.ok(relay.refused.includes('/v1/version') && !relay.held.length, 'realm check refused: ' + JSON.stringify(relay));
   assert.equal(errors.length, 0, errors.join('\n'));
   console.log(JSON.stringify({mode,messages,firstChatListMs,firstSavedMessageMs,newest:shown,relay:{held:relay.held,refused:relay.refused},stage}));
   await context.close();
   current = null;
  }
  console.log('cached device Comic real IndexedDB startup PASS (' + messages + ' saved messages; held/offline relay)');
 } catch (error) {
  console.error(error.stack);
  console.log('diagnostic', JSON.stringify({errors, relay}));
  if(current) console.log('diagnostic page', JSON.stringify(await current.evaluate(() => ({stage:window.stage,body:document.body.innerText,skin:document.getElementById('skin')?.shadowRoot?.textContent?.slice(0,2000)})).catch(() => null)));
  process.exitCode = 1;
 } finally {
  if(browser) await browser.close();
  srv.closeAllConnections();
  await new Promise(resolve => srv.close(resolve));
 }
})();
