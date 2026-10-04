// Classic standalone package: real production loader + browser-local import,
// against company_world.sh only. No user profile, daemon or real sends.
const {execFileSync}=require('node:child_process');
const fs=require('node:fs'), path=require('node:path'), assert=require('node:assert/strict');
const { chromium }=require(process.env.AGENTNET_PLAYWRIGHT || '/home/misunderstood/.npm/_npx/9833c18b2d85bc59/node_modules/playwright');
const evidence=path.resolve(process.env.AGENTNET_CLASSIC_EVIDENCE || '/tmp/agentnet-classic-evidence');
const world=path.join(evidence,'world');
const pkg=path.resolve(__dirname,'../static/skins/classic');
const manifest=JSON.parse(fs.readFileSync(path.join(pkg,'skin.json'),'utf8'));
const result={pass:false,adapter:'Classic production loader / browser-local package / disposable native provider',screenshots:[],checks:[],limitations:['Host-addition integration waits for host owner; current host lacks native reconnect/Drive provider/catalog subscription','390px is viewport simulation, not physical phone','No Google auth or real provider account']};
const runID=Date.now().toString(36);
let browser,context,page,stage='start';
(async()=>{
 const urls=JSON.parse(fs.readFileSync(path.join(world,'urls.json'),'utf8'));
 const seed=JSON.parse(fs.readFileSync(path.join(world,'seed.json'),'utf8'));
 const origin=new URL(urls.sergey.page).origin;
 assert(new URL(origin).hostname==='127.0.0.1'&&new URL(origin).port!=='18990');
 browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 context=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true});
 page=await context.newPage();const errors=[];
 result.pageErrors=errors;result.assetFailures=[];result.consoleErrors=[];
 page.on('pageerror',e=>errors.push(e.message));page.on('response',r=>{if(r.status()>=400&&new URL(r.url()).pathname.startsWith('/local-skins/'))result.assetFailures.push({path:new URL(r.url()).pathname,status:r.status()})});page.on('console',m=>{if(m.type()==='error')result.consoleErrors.push(m.text().replace(/([?&]token=)[^\s&]+/g,'$1[redacted]'))});
 fs.mkdirSync(path.join(evidence,'screenshots'),{recursive:true,mode:0o700});
 async function shot(name){const f=path.join(evidence,'screenshots',name+'.png');await page.screenshot({path:f});result.screenshots.push(f);}
 async function classic(){await page.waitForFunction(()=>!!document.querySelector('#skin')?.shadowRoot?.querySelector('.classic-root'));await page.locator('#conv-list').getByText('Vitalii',{exact:true}).first().waitFor();}
 async function list(){for(let i=0;i<3&&!(await page.locator('#nav-chats').isVisible());i++)await page.locator('#back').click();await page.locator('#nav-chats').click();await page.locator('#conv-list').getByText('Vitalii',{exact:true}).first().waitFor();}
 async function thread(){await page.locator('#conv-list').getByText('Vitalii',{exact:true}).first().click();await page.locator('#conv-name').filter({hasText:'Vitalii'}).waitFor();await page.locator('#composer').waitFor();}
 stage='import package';
 await page.goto(urls.sergey.page);const entry=new URL(origin);entry.searchParams.set('skin','classic');await page.goto(entry.href);
 await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();
 await page.getByLabel('Import package files',{exact:true}).setInputFiles(['skin.json',...manifest.files].map(n=>path.join(pkg,n)));
 await page.getByText('Stored Classic. Choose it in Interface above to review trust before running it.',{exact:true}).waitFor();
 await page.locator('#lens [data-skin="local:classic"]').click();await page.getByRole('button',{name:'Use this UI',exact:true}).click();await classic();
 result.checks.push('Unmodified public loader imports actual manifest/bytes through browser-local package path after explicit trust');
 for(const width of [390,1440])for(const theme of ['light','dark']){
  const tag=width+'-'+theme;stage=tag+' navigation';
  await page.setViewportSize({width,height:width===390?844:1000});await page.emulateMedia({colorScheme:theme,reducedMotion:'reduce'});
  await list();await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.locator('[data-theme="'+theme+'"]').click();await shot(tag+'-appearance');await page.locator('#settings-close').click();
  await list();for(const name of ['Vitalii','Anna','Bohdan']){const fits=await page.locator('#conv-list').getByText(name,{exact:true}).first().evaluate(n=>n.scrollWidth<=n.clientWidth+1);assert(fits,name+' truncated at '+tag)}assert(await page.locator('#demo').isHidden());await shot(tag+'-list');await page.locator('#nav-people').click();await page.getByText('Reading teams…',{exact:true}).waitFor({state:'hidden'});await shot(tag+'-people');await page.locator('#review-btn').click();await shot(tag+'-activity');await list();await thread();await shot(tag+'-thread');
  stage=tag+' send';const text='Classic package '+tag+' synthetic send '+runID;
  await page.locator('#body').fill(text);await page.locator('#composer').evaluate(el=>el.requestSubmit());
  await page.locator('#timeline').getByText(text,{exact:true}).waitFor();await shot(tag+'-send');
  stage=tag+' files';await page.locator('#file-input').setInputFiles({name:'classic-'+tag+'-'+runID+'.txt',mimeType:'text/plain',buffer:Buffer.from('Synthetic standalone Classic file '+tag)});
  await page.locator('#body').fill('Classic synthetic attachment '+tag+' '+runID);await page.locator('#composer').evaluate(el=>el.requestSubmit());
  await page.locator('#timeline').getByText('classic-'+tag+'-'+runID+'.txt',{exact:true}).waitFor();
  const message=page.locator('#timeline .msg').filter({hasText:'classic-'+tag+'-'+runID+'.txt'});
  await message.getByRole('button',{name:'Open',exact:true}).click();const link=message.getByRole('link',{name:'Download classic-'+tag+'-'+runID+'.txt',exact:true});await link.waitFor();const download=page.waitForEvent('download');await link.click();const file=await download;
  const saved=path.join(evidence,'classic-'+tag+'-'+runID+'.txt');await file.saveAs(saved);assert.equal(fs.readFileSync(saved,'utf8'),'Synthetic standalone Classic file '+tag);await shot(tag+'-files');
  stage=tag+' Comic switch';await list();await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.locator('#interfaces [data-skin="default"]').click();
  await page.waitForFunction(()=>!!document.querySelector('#messenger'));await page.getByText('Vitalii',{exact:true}).first().waitFor();await shot(tag+'-comic');
  const back=new URL(page.url());back.searchParams.set('skin','local:classic');await page.goto(back.href);await classic();await thread();await shot(tag+'-back-classic');
  const sizes=await page.locator('.classic-root').evaluate(root=>({width:root.clientWidth,scroll:root.scrollWidth,height:root.clientHeight,bodyOverflow:document.documentElement.scrollWidth>innerWidth}));
  assert(!sizes.bodyOverflow&&sizes.scroll<=sizes.width+1,'Classic horizontal overflow');assert(sizes.height>300,'Classic collapsed root');
  result.checks.push(tag+': list/thread/send/file bytes/Comic/back, no horizontal overflow');
 }
 stage='unchanged Notebook';const notebook=path.resolve(__dirname,'../../../examples/skins/notebook');const note=JSON.parse(fs.readFileSync(path.join(notebook,'skin.json'),'utf8'));
 const old=new URL(origin);old.searchParams.set('skin','classic');await page.goto(old.href);await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();
 await page.getByLabel('Import package files',{exact:true}).setInputFiles(['skin.json',...note.files].map(n=>path.join(notebook,n)));
 await page.getByText('Stored Notebook example. Choose it in Interface above to review trust before running it.',{exact:true}).waitFor();await page.locator('#lens [data-skin="local:notebook"]').click();await page.getByRole('button',{name:'Use this UI',exact:true}).click();
 await page.getByRole('heading',{name:'Notebook',exact:true}).waitFor();await page.getByRole('navigation',{name:'Conversations',exact:true}).getByRole('button').first().waitFor();await shot('notebook-unchanged');
 result.checks.push('Unmodified examples/skins/notebook imports through same production loader and renders native conversations');
 stage='manifest-only copy';const copy=path.join(evidence,'classic-copy-source'),copied=path.join(evidence,'classic-copy-package');fs.cpSync(path.resolve(__dirname,'../skins/classic'),copy,{recursive:true});const m=JSON.parse(fs.readFileSync(path.join(copy,'skin.json')));m.id='classic-copy';m.name='Classic copy';fs.writeFileSync(path.join(copy,'skin.json'),JSON.stringify(m,null,2));execFileSync(path.join(copy,'build.sh'),[copied]);
 await page.goto(old.href);await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();await page.getByLabel('Import package files',{exact:true}).setInputFiles(['skin.json',...m.files].map(n=>path.join(copied,n)));await page.getByText('Stored Classic copy. Choose it in Interface above to review trust before running it.',{exact:true}).waitFor();await page.locator('#lens [data-skin="local:classic-copy"]').click();await page.getByRole('button',{name:'Use this UI',exact:true}).click();await classic();await list();await page.locator('#profile-btn').click();await page.locator('#settings-tab-appearance').click();assert.equal(await page.locator('#interfaces [data-skin="local:classic-copy"]').getAttribute('aria-pressed'),'true');assert.equal(await page.locator('#interfaces [data-skin="local:classic"]').getAttribute('aria-pressed'),'false');await shot('classic-manifest-only-copy');result.checks.push('Only manifest changed: copied module builds and current renamed identity selects correctly without host.skin fallback');
 assert.deepEqual(errors,[]);result.checks.push('No page errors across all journeys');result.pass=true;
 fs.writeFileSync(path.join(evidence,'browser-result.json'),JSON.stringify(result,null,2),{mode:0o600});console.log('PASS Classic standalone rendered journey; '+result.screenshots.length+' screenshots');
})().catch(async e=>{await page?.screenshot({path:path.join(evidence,'browser-failure.png')}).catch(()=>{});result.stage=stage;result.error=e.message;fs.writeFileSync(path.join(evidence,'browser-result.json'),JSON.stringify(result,null,2),{mode:0o600});console.error('FAIL Classic journey at '+stage+': '+e.message);process.exitCode=1;}).finally(async()=>{await context?.close();await browser?.close();});
