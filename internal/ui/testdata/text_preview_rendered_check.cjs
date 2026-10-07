const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const text='Agent result — UTF-8\n<script>window.previewExecuted=true</script>\n<img src=x onerror=window.previewExecuted=true>\n';
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {for(const phone of [false,true]){
  const ctx=await browser.newContext({viewport:phone?{width:390,height:844}:{width:1440,height:900},acceptDownloads:true});
  const p=await ctx.newPage(),errors=[];p.on('pageerror',e=>errors.push(String(e)));
  await p.goto(process.argv[2]);await p.getByRole('button',{name:/^Bob/}).first().click();
  await p.getByRole('button',{name:'Preview agent-result.txt',exact:true}).waitFor();
  assert.equal(await p.getByRole('button',{name:'Preview unsafe.html',exact:true}).count(),0);
  let fetched=0;p.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/files/'))fetched++;});
  await p.waitForTimeout(100);assert.equal(fetched,0,'text fetch is explicit');
  await p.getByRole('button',{name:'Preview agent-result.txt',exact:true}).click();
  const dialog=p.getByRole('dialog',{name:'agent-result.txt',exact:true});await dialog.waitFor();
  assert.equal(await dialog.locator('pre').textContent(),text);
  assert.equal(await dialog.locator('script,img').count(),0,'file bytes remain escaped text');
  assert.equal(await p.evaluate(()=>window.previewExecuted),undefined);
  if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true,mode:0o700});await p.evaluate(()=>document.fonts.ready);await p.waitForTimeout(350);assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'preview fits viewport');await p.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'text-preview-'+(phone?'390':'1440')+'.png')});}
  const download=p.waitForEvent('download');await dialog.getByRole('button',{name:'Download',exact:true}).click();
  const d=await download;assert.equal(d.suggestedFilename(),'agent-result.txt');assert.equal(fs.readFileSync(await d.path(),'utf8'),text);
  await dialog.getByRole('button',{name:'Close',exact:true}).click();await dialog.waitFor({state:'hidden'});
  await p.getByRole('button',{name:'Preview bad-utf8.txt',exact:true}).click();
  await p.getByRole('alert').filter({hasText:'not UTF-8 text'}).waitFor();
  const unsafe=p.waitForEvent('download');await p.getByRole('button',{name:'Download unsafe.html',exact:true}).click();
  const u=await unsafe;assert.equal(u.suggestedFilename(),'unsafe.html');assert.equal(fs.readFileSync(await u.path(),'utf8'),text);
  assert.equal(await p.evaluate(()=>window.previewExecuted),undefined);assert.equal(fetched,3);
  assert.deepEqual(errors,[]);await ctx.close();
 }console.log('text preview rendered PASS');}finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
