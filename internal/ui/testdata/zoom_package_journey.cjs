// Real loader and actual standalone Zoom bytes; synthetic company world only.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || '/home/misunderstood/.npm/_npx/9833c18b2d85bc59/node_modules/playwright');
const evidence = path.resolve(process.env.AGENTNET_ZOOM_EVIDENCE || '/tmp/agentnet-zoom-evidence');
const world = path.resolve(process.env.AGENTNET_SKIN_WORLD || '/tmp/agentnet-classic-review-evidence/world');
const pkg = path.resolve(__dirname, '../static/skins/zoom');
const manifest = JSON.parse(fs.readFileSync(path.join(pkg, 'skin.json')));
const result = {pass:false, adapter:'production loader / browser-local Zoom / disposable native API', screenshots:[], checks:[], pageErrors:[], assetFailures:[], limitations:['Host additions await production host branch', '390px viewport simulation; no physical device', 'No Google login or real provider account']};
const runID = Date.now().toString(36);
let browser, context, page, stage = 'start';
(async () => {
 const urls = JSON.parse(fs.readFileSync(path.join(world, 'urls.json')));
 const origin = new URL(urls.sergey.page).origin;
 assert.equal(new URL(origin).hostname, '127.0.0.1'); assert.notEqual(new URL(origin).port, '18990');
 browser = await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless:true, args:['--no-sandbox']});
 context = await browser.newContext({viewport:{width:1440,height:1000}, acceptDownloads:true});
 page = await context.newPage(); page.on('pageerror', e => result.pageErrors.push(e.message));
 page.on('response', r => {if(r.status() >= 400 && new URL(r.url()).pathname.startsWith('/local-skins/')) result.assetFailures.push({path:new URL(r.url()).pathname,status:r.status()});});
 fs.mkdirSync(path.join(evidence, 'screenshots'), {recursive:true,mode:0o700});
 async function shot(name) {
  const bounds=await page.locator('#zoom .rung-name:visible').evaluateAll(labels=>labels.map(label=>{const box=label.getBoundingClientRect(),parent=label.closest('.rung').getBoundingClientRect();return {name:label.textContent,bounded:box.left>=parent.left-1&&box.right<=parent.right+1};}));
  assert(bounds.every(label=>label.bounded),'Zoom ladder labels must remain inside their own steps');
  const file=path.join(evidence,'screenshots',name+'.png');await page.screenshot({path:file});result.screenshots.push(file);
 }
 async function zoom() {await page.waitForFunction(() => !!document.querySelector('#skin')?.shadowRoot?.querySelector('.zoom-root'));await page.locator('#zoom .ladder').waitFor();}
 async function everyone() {await page.locator('#zoom .ladder .rung').nth(0).click();await page.locator('#zoom .person-card').filter({hasText:'Vitalii'}).waitFor();}
 async function person() {await page.locator('#zoom .person-card').filter({hasText:'Vitalii'}).click();await page.locator('#zoom').getByRole('heading',{name:'Vitalii',exact:true}).waitFor();await page.locator('#zoom .person-devices summary').click();await page.locator('#zoom .person-devices .device-list').waitFor();}
 async function thread() {await page.locator('#zoom').getByRole('list',{name:'DMs with Vitalii',exact:true}).getByRole('button').first().click();await page.locator('#zoom .mini-chat').waitFor();}
 async function write(text, file) {await page.locator('#zoom').getByRole('button',{name:'Write in this DM…',exact:true}).click();await page.locator('#write-body').fill(text);if(file) await page.locator('#write-files').setInputFiles(file);await page.locator('#dialog-ok').click();await page.locator('#dialog').waitFor({state:'hidden'});await page.locator('#zoom .mini-chat').getByText(text,{exact:true}).waitFor();await page.locator('#zoom .mini-chat').getByText(text,{exact:true}).evaluate(element=>element.scrollIntoView({block:'center'}));}
 async function importer(directory, m) {const old=new URL(origin);old.searchParams.set('skin','classic');await page.goto(old.href);await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.getByLabel('Import package files',{exact:true}).setInputFiles(['skin.json',...m.files].map(n=>path.join(directory,n)));await page.getByText('Stored '+m.name+'. Choose it in Interface above to review trust before running it.',{exact:true}).waitFor();await page.locator('#lens [data-skin="local:'+m.id+'"]').click();await page.getByRole('button',{name:'Use this UI',exact:true}).click();await zoom();}
 stage='import package';await page.goto(urls.sergey.page);await importer(pkg,manifest);await everyone();
 result.checks.push('Production loader imports actual declared Zoom package after explicit digest trust');
 for(const width of [390,1440]) for(const theme of ['light','dark']) {
  const tag=width+'-'+theme;stage=tag+' navigation';await page.setViewportSize({width,height:width===390?844:1000});await page.emulateMedia({colorScheme:theme,reducedMotion:'reduce'});
  await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.locator('[data-theme="'+theme+'"]').click();await shot(tag+'-appearance');await page.locator('#settings-close').click();
  await everyone();assert(await page.locator('#demo').isHidden());await shot(tag+'-list');await person();await shot(tag+'-person');await thread();await shot(tag+'-thread');
  stage=tag+' send';const text='Zoom package '+tag+' synthetic send '+runID;await write(text);await shot(tag+'-send');await page.locator('#zoom .mc-bubble').filter({hasText:text}).click();await page.locator('#zoom .zoom-message').waitFor();await shot(tag+'-message');
  // Keyboard zoom-out traverses Message -> Conversation -> Person -> Everyone.
  for(const index of [2,1,0]) {result.focusTrace ||= [];result.focusTrace.push(await page.evaluate(()=>{const shadow=document.querySelector('#skin')?.shadowRoot;const focus=shadow?.activeElement;return {docFocus:document.activeElement?.tagName,ownedFocus:focus?.tagName,ownedClass:focus?.className,level:[...shadow.querySelectorAll('#zoom .rung')].findIndex(e=>e.getAttribute('aria-current')==='step')}}));await page.keyboard.press('Escape');assert.equal(await page.locator('#zoom .ladder .rung').nth(index).getAttribute('aria-current'),'step','Escape must reach level '+index);}
  await person();await thread();stage=tag+' files';const name='zoom-'+tag+'-'+runID+'.txt', bytes='Synthetic standalone Zoom file '+tag;
  const fileText='Zoom synthetic attachment '+tag+' '+runID;await write(fileText,{name,mimeType:'text/plain',buffer:Buffer.from(bytes)});await page.locator('#zoom .mc-bubble').filter({hasText:name}).click();await page.locator('#zoom .zoom-message').waitFor();
  await page.locator('#zoom').getByRole('button',{name:'Open',exact:true}).click();const link=page.locator('#zoom').getByRole('link',{name:'Download '+name,exact:true});await link.waitFor();const pending=page.waitForEvent('download');await link.click();const download=await pending;const saved=path.join(evidence,name);await download.saveAs(saved);assert.equal(fs.readFileSync(saved,'utf8'),bytes);await shot(tag+'-files');
  await page.locator('#review-btn').click();await page.locator('#review').waitFor();await shot(tag+'-activity');await page.locator('#nav-people').click();await everyone();await shot(tag+'-people');
  stage=tag+' Comic/back';await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.locator('#interfaces [data-skin="default"]').click();await page.waitForFunction(()=>!!document.querySelector('#messenger'));await page.getByText('Vitalii',{exact:true}).first().waitFor();await shot(tag+'-comic');
  const back=new URL(page.url());back.searchParams.set('skin','local:zoom');await page.goto(back.href);await zoom();await everyone();await person();await thread();await shot(tag+'-back-zoom');
  const size=await page.locator('.zoom-root').evaluate(root=>({width:root.clientWidth,scroll:root.scrollWidth,height:root.clientHeight,outside:document.documentElement.scrollWidth>innerWidth}));assert(!size.outside&&size.scroll<=size.width+1,'Horizontal Zoom overflow');assert(size.height>300,'Collapsed Zoom root');
  result.checks.push(tag+': four levels, Esc traversal, synthetic send, downloaded file bytes, activity/people, Comic/back; no overflow');
 }
 stage='manifest-only copy';const copy=path.join(evidence,'zoom-copy-source'),copied=path.join(evidence,'zoom-copy-package');fs.cpSync(path.resolve(__dirname,'../skins/zoom'),copy,{recursive:true});const m=JSON.parse(fs.readFileSync(path.join(copy,'skin.json')));m.id='zoom-copy';m.name='Zoom copy';fs.writeFileSync(path.join(copy,'skin.json'),JSON.stringify(m,null,2));execFileSync(path.join(copy,'build.sh'),[copied]);await importer(copied,m);await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();assert.equal(await page.locator('#interfaces [data-skin="local:zoom-copy"]').getAttribute('aria-pressed'),'true');assert.equal(await page.locator('#interfaces [data-skin="local:zoom"]').getAttribute('aria-pressed'),'false');await shot('zoom-manifest-only-copy');result.checks.push('Only manifest changed: independently copied Zoom builds and renamed identity works with host.skin fallback');
 assert.deepEqual(result.pageErrors,[]);assert.deepEqual(result.assetFailures,[]);result.pass=true;fs.writeFileSync(path.join(evidence,'browser-result.json'),JSON.stringify(result,null,2),{mode:0o600});console.log('PASS Zoom rendered journey; '+result.screenshots.length+' screenshots');
})().catch(async error=>{await page?.screenshot({path:path.join(evidence,'browser-failure.png')}).catch(()=>{});result.stage=stage;result.error=error.message;fs.writeFileSync(path.join(evidence,'browser-result.json'),JSON.stringify(result,null,2),{mode:0o600});console.error('FAIL Zoom at '+stage+': '+error.message);process.exitCode=1;}).finally(async()=>{await context?.close();await browser?.close();});
