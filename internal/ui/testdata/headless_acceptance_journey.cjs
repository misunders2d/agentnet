// Real encrypted loopback providers; visible controls perform all user actions.
// CLI only seeds synthetic people/history and approves the synthetic browser link.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_HUMAN_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
assert(world && evidence && process.env.AGENTNET_HUMAN_BINARY && process.env.AGENTNET_HUMAN_ASSET_MANIFEST);
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const nativeURL = n => fs.readFileSync(path.join(world, n + '-ui'), 'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const allowed = new Set([origin, ...['alice', 'bob', 'outside'].map(n => new URL(nativeURL(n)).origin)]);
const cli = (n, ...args) => execFileSync(process.env.AGENTNET_HUMAN_BINARY, ['--home', path.join(world, n), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const expectedAssets = Object.fromEntries(fs.readFileSync(process.env.AGENTNET_HUMAN_ASSET_MANIFEST, 'utf8').trim().split('\n').map(line => { const [hash, name] = line.trim().split(/\s+/); return [path.basename(name), hash]; }));
const errors = [], external = [], shots = [], checks = [], assetReads = [], served = {};
let browser, stage = 'setup';
const idle = ms => new Promise(r => setTimeout(r, ms));
const ready = p => p.waitForFunction(() => typeof state !== 'undefined' && state.overview && state.overview.directory.current, null, { timeout: 25000 });
async function until(label, predicate) {
  for (let i = 0; i < 250; i++) { if (await predicate()) return; await idle(100); }
  throw Error('Timed out: ' + label);
}
async function capture(p, name, width) {
  assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name + ' overflow');
  assert(await p.evaluate(() => [...document.querySelectorAll('dialog[open]')].every(d => d.scrollWidth <= d.clientWidth + 1)), name + ' dialog overflow');
  const file = path.join(evidence, `headless-${name}-${width}.png`); await p.screenshot({ path: file }); shots.push(file);
}
async function home(p) {
  if (await p.locator('#settings').isVisible()) await p.locator('#settings-close').click();
  for (let i = 0; i < 3 && !(await p.locator('#nav-chats').isVisible()); i++) await p.locator('#back').click();
  await p.locator('#nav-chats').click(); await p.locator('#search').waitFor({ state: 'visible' });
}
const harnessRuns = () => fs.existsSync(path.join(world, 'harness.jsonl')) ? fs.readFileSync(path.join(world, 'harness.jsonl'), 'utf8').trim().split('\n').map(JSON.parse) : [];
async function newRequest(p, body, kind) {
  await home(p); await p.locator('#new-btn').click(); await p.locator('#new-to').fill('bob/host');
  await p.locator('#new-kind').selectOption(kind); await p.locator('#new-body').fill(body); await p.locator('#dialog-ok').click();
  await p.locator('#dialog').waitFor({state:'hidden'}); await p.waitForFunction(body=>state.data?.messages.some(m=>m.body===body),body);
  return p.evaluate(body=>state.data.messages.find(m=>m.body===body).id,body);
}
async function activity(p) { await home(p); await p.locator('#review-btn').click(); await p.locator('#review').waitFor({state:'visible'}); }
async function reportFor(p,id) {
  await p.waitForFunction(id=>state.overview.review.some(r=>r.notice&&r.report?.items.some(x=>x.id===id)),id);
  return p.evaluate(id=>state.overview.review.find(r=>r.notice&&r.report?.items.some(x=>x.id===id)),id);
}
async function fragment(p, target) { await p.evaluate(target=>{location.hash=target},target); }
(async () => {
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,args:['--ignore-certificate-errors-spki-list='+spki]});
  const pages={};
  function watch(p,name) {
    p.setDefaultTimeout(20000);p.on('pageerror',e=>errors.push(e.stack||e.message));p.on('console',m=>{if(m.type()==='error')errors.push(m.text())});
    p.on('response',r=>{const asset=new URL(r.url()).pathname.split('/').pop();if(expectedAssets[asset])assetReads.push((async()=>{const hash=digest(await r.body());assert.equal(hash,expectedAssets[asset],name+':'+asset);served[name+'/'+asset]=hash})().catch(e=>errors.push(e.message)))});
  }
  for(const name of ['alice','phone','outside']) {
    const c=await browser.newContext({viewport:{width:1280,height:900}});await c.route('**/*',r=>{if(allowed.has(new URL(r.request().url()).origin))return r.continue();external.push(new URL(r.request().url()).origin);return r.abort()});
    const p=pages[name]=await c.newPage();watch(p,name);
  }
  const {alice,phone,outside}=pages;
  const raw=fs.readFileSync(path.join(world,'link'),'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer=JSON.parse(Buffer.from(raw.split(':')[1],'base64url')),invite=JSON.parse(Buffer.from(offer.invite.split(':')[1],'base64url'));
  delete invite.cert;offer.invite='agentnet-invite-v1:'+Buffer.from(JSON.stringify(invite)).toString('base64url');
  await phone.goto(origin+'/#agentnet-link-v2:'+Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await phone.locator('#join-name').fill('phone');await phone.locator('button.join-go').click();let request;
  await until('browser link',()=>{request=cli('alice','person','links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*phone/m)?.[1];return !!request});cli('alice','person','approve',request);await ready(phone);
  await alice.goto(nativeURL('alice'));await ready(alice);await outside.goto(nativeURL('outside'));await ready(outside);
  cli('bob','approve','admin/laptop');cli('bob','approve','admin/phone');cli('bob','review-to','admin/phone');
  const operators=[];let lastNative,lastBrowser;
  for(const width of [390,1280]) {
    for(const p of Object.values(pages))await p.setViewportSize({width,height:width===390?844:900});
    for(const address of ['admin/phone','admin/laptop']) if(cli('bob','operator','list').split('\n').some(line=>line.startsWith(address+'  '))) cli('bob','operator','revoke',address);
    if(await phone.evaluate(()=>state.overview.review.some(r=>r.notice&&r.peer==='bob/host'))) {
      await activity(phone);await phone.locator('#report-list').getByRole('button',{name:/Dismiss .* here/}).click();
      await phone.waitForFunction(()=>!state.overview.review.some(r=>r.notice&&r.peer==='bob/host'));
    }
    for(const [platform,p] of [['native',alice],['browser',phone]]) {
      stage='clarification '+platform+' '+width;const baseline=harnessRuns().length;
      const q=await newRequest(p,'ambiguous weather '+platform+' '+width,'question');
      await p.waitForFunction(q=>state.data?.messages.some(m=>m.reply_to===q&&m.body==='Which city?'),q);
      const ask=await p.evaluate(q=>state.data.messages.find(m=>m.reply_to===q&&m.body==='Which city?'),q);
      assert.equal(ask.kind,'answer');assert.equal(harnessRuns().length,baseline+1);
      await capture(p,platform+'-clarification',width);
      await fragment(p,'#msg='+ask.id+'&dir=in');await p.waitForFunction(id=>state.data?.messages.some(m=>m.id===id),ask.id);assert.equal(harnessRuns().length,baseline+1);
      await p.getByRole('radio',{name:'Question',exact:true}).check();await p.locator('#body').fill('Riga');await p.locator('#send').click();
      await p.waitForFunction(()=>state.data?.messages.some(m=>m.body==='Riga: bring a jacket. Synthetic correlated answer.'));
      const result=await p.evaluate(()=>{const follow=state.data.messages.find(m=>m.body==='Riga'),final=state.data.messages.find(m=>m.body==='Riga: bring a jacket. Synthetic correlated answer.');return {follow,final,exec:state.data.messages.filter(m=>m.exec)}});
      assert.equal(result.follow.kind,'question');assert.equal(result.follow.reply_to,ask.id);assert.equal(result.final.reply_to,result.follow.id);assert.equal(harnessRuns().length,baseline+2);
      assert(harnessRuns().at(-1).prompt.includes('Which city?')&&harnessRuns().at(-1).prompt.includes('Riga'));
      assert.equal(await p.locator('#timeline .exec.running').count(),0,'terminal reply dominates prior run status');await capture(p,platform+'-correlated-answer',width);
      checks.push({gate:'H1/H4',width,platform,question:q,clarification:ask.id,follow:result.follow.id,result:result.final.id,runs:2,notificationFragmentReadOnly:true,terminalReplyWins:true});
    }
    stage='ungranted count-only '+width;const baseline=harnessRuns().length;
    const task=await newRequest(outside,'Operator task '+width,'task');
    await phone.waitForFunction(()=>state.overview.review.some(r=>r.notice&&!r.report));await activity(phone);
    assert.equal(await phone.locator('#review-list').innerText(),'Nothing here waits for your decision.');assert.equal(await phone.locator('#report-list .report-request').count(),0);
    assert.equal(await phone.locator('#report-list').getByRole('button',{name:'Accept and run there…',exact:true}).count(),0);assert.equal(harnessRuns().length,baseline);
    await capture(phone,'ungranted-count-only',width);await phone.locator('#report-list').getByRole('button',{name:/Dismiss .* here/}).click();
    await phone.waitForFunction(()=>!state.overview.review.some(r=>r.notice&&!r.report));
    await capture(phone,'count-only-dismissed-history',width);
    cli('bob','operator','grant','admin/phone');cli('bob','operator','grant','admin/laptop');const grants=cli('bob','operator','list');assert(grants.includes('admin/phone')&&grants.includes('active'));
    const report=await reportFor(phone,task),item=report.report.items.find(x=>x.id===task);assert(item.actionable&&item.from==='outside/host'&&item.blocker==='awaiting_acceptance'&&item.key&&item.excerpt==='Operator task '+width);
    await activity(phone);await capture(phone,'granted-operator-activity',width);
    await fragment(phone,'#msg='+report.id+'&dir=in');await phone.waitForFunction(id=>state.data?.messages.some(m=>m.id===id),report.id);assert.equal(harnessRuns().length,baseline);
    await fragment(phone,'#review');await phone.locator('#review').waitFor({state:'visible'});assert.equal(harnessRuns().length,baseline);
    const nativeReport=await reportFor(alice,task);await fragment(alice,'#msg='+nativeReport.id+'&dir=in');await alice.waitForFunction(id=>state.data?.messages.some(m=>m.id===id),nativeReport.id);
    await fragment(alice,'#review');await alice.locator('#review').waitFor({state:'visible'});assert.equal(harnessRuns().length,baseline);await capture(alice,'native-exact-report-activity',width);
    stage='browser exact operator acceptance '+width;
    const row=phone.locator('#report-list .report-request').filter({hasText:task.slice(0,8)});await row.getByRole('button',{name:'Accept and run there…',exact:true}).click();await capture(phone,'operator-accept-dialog',width);await phone.locator('#dialog-ok').click();
    await until('exactly one task execution',()=>harnessRuns().length===baseline+1);
    await outside.waitForFunction(task=>state.data?.messages.some(m=>m.reply_to===task&&m.kind==='result'),task);
    // One browser workspace stays in its supported owning window. A distinct
    // exact-key native operator settles another task while this browser holds
    // its old dialog; submitting that captured state must be refused.
    stage='stale browser operator '+width;
    const staleTask=await newRequest(outside,'Stale operator task '+width,'task');await reportFor(phone,staleTask);await reportFor(alice,staleTask);
    await activity(phone);await phone.locator('#report-list .report-request').filter({hasText:staleTask.slice(0,8)}).getByRole('button',{name:'Accept and run there…',exact:true}).click();
    await activity(alice);await alice.locator('#report-list .report-request').filter({hasText:staleTask.slice(0,8)}).getByRole('button',{name:'Decline there…',exact:true}).click();
    await alice.locator('#decide-text').fill('Synthetic native operator declined this stale-state fixture');await alice.locator('#dialog-ok').click();await alice.locator('#dialog').waitFor({state:'hidden'});
    await outside.waitForFunction(id=>state.data?.messages.some(m=>m.reply_to===id&&m.kind==='result'),staleTask);
    await phone.locator('#dialog-ok').click();await phone.locator('#dialog').waitFor({state:'hidden'});
    await phone.waitForFunction(id=>state.overview.review.some(r=>r.report?.items.some(x=>x.id===id&&x.result?.refused?.includes('no longer as you saw it'))),staleTask);
    assert.equal(harnessRuns().length,baseline+1);await activity(phone);await phone.locator('#report-list .report-request').filter({hasText:staleTask.slice(0,8)}).scrollIntoViewIfNeeded();await capture(phone,'stale-operator-refused',width);
    stage='revoked browser operator '+width;lastBrowser=await newRequest(phone,'Await revoked browser task '+width,'task');await reportFor(phone,lastBrowser);await activity(phone);
    const revoked=phone.locator('#report-list .report-request').filter({hasText:lastBrowser.slice(0,8)});await revoked.getByRole('button',{name:'Accept and run there…',exact:true}).click();cli('bob','operator','revoke','admin/phone');await phone.locator('#dialog-ok').click();
    await phone.waitForFunction(id=>state.overview.review.some(r=>r.report?.items.some(x=>x.id===id&&x.result?.refused?.includes('not an operator'))),lastBrowser);
    assert.equal(harnessRuns().length,baseline+1);await activity(phone);await phone.locator('#report-list .report-request').filter({hasText:lastBrowser.slice(0,8)}).scrollIntoViewIfNeeded();await capture(phone,'revoked-operator-refused',width);
    checks.push({gate:'H2/H3/H4',width,task,staleTask,report:report.id,exactGrant:true,countOnlyReadOnly:true,originalWaitingTaskPromoted:true,openOnlyRuns:0,taskRuns:1,staleRefused:true,revokedRefused:true});
    lastNative=await newRequest(alice,'Await native offline task '+width,'task');
  }
  stage='offline last-known status';process.kill(Number(fs.readFileSync(path.join(world,'bob-pid'),'utf8').trim()),'SIGTERM');
  for(const [platform,p,id] of [['native',alice,lastNative],['browser',phone,lastBrowser]]) {
    await fragment(p,'#msg='+id+'&dir=out');await p.waitForFunction(id=>state.data?.messages.some(m=>m.id===id&&m.exec?.stale),id,{timeout:25000});
    assert((await p.locator('#timeline').innerText()).includes('not confirmed now'));
    for(const width of [390,1280]){await p.setViewportSize({width,height:width===390?844:900});await capture(p,platform+'-offline-last-known',width)}
    checks.push({gate:'H4',platform,id,offlineLastKnown:true});
  }
  await Promise.all(assetReads);assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
  fs.writeFileSync(path.join(evidence,'result.json'),JSON.stringify({pass:true,kind:'real native/browser encrypted headless UI; deterministic synthetic executor, simulated notification fragments only',checks,shots,expectedAssets,served,errors,external,world,runs:harnessRuns().length},null,2),{mode:0o600});
  console.log('PASS real headless clarification/operator/Activity,390/1280,synthetic executor and fake notification semantics only');
})().catch(async e=>{if(browser){let i=0;const diagnostics=[];for(const c of browser.contexts())for(const p of c.pages())try{
  const name='failure-page-'+i++;await p.screenshot({path:path.join(evidence,name+'.png')});diagnostics.push({name,...await p.evaluate(()=>({address:state.overview?.me.address,announcement:document.getElementById('live')?.textContent,dialogError:document.getElementById('dialog-error')?.textContent,review:state.overview?.review?.map(r=>({id:r.id,notice:r.notice,excerpt:r.excerpt,report:r.report})),thread:state.thread}))});
}catch{} fs.writeFileSync(path.join(evidence,'failure-pages.json'),JSON.stringify(diagnostics,null,2),{mode:0o600});}
  fs.writeFileSync(path.join(evidence,'failure.json'),JSON.stringify({stage,error:e.stack,errors,external,world,shots},null,2),{mode:0o600});console.error('FAIL '+stage+': '+e.stack);process.exitCode=1;
}).finally(async()=>{if(browser){for(const c of browser.contexts())await c.close();await browser.close()}});
