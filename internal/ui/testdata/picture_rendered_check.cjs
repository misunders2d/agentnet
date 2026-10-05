const assert=require('node:assert/strict');
const fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  for(const phone of [false,true])for(const skin of ['comic','classic','zoom']){
   const ctx=await browser.newContext({viewport:phone?{width:390,height:844}:{width:1440,height:900},isMobile:phone,hasTouch:phone});
   const p=await ctx.newPage(),errors=[];p.on('pageerror',e=>errors.push(String(e)));
   await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);
   const shot=async name=>{if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:process.env.AGENTNET_SCREENSHOTS+`/p22-${skin}-${phone?'phone':'desktop'}-${name}.png`});}};
   const profile=async()=>{
    if(skin!=='comic') { await p.locator('#profile-btn').click();return; }
    if(phone) {
     await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:'You',exact:true}).click();
     await p.getByRole('button',{name:/^Alice\b.*On 1 device/}).click();
    }else await p.getByRole('button',{name:'You: profile and devices'}).click();
   };
   await profile();await p.getByRole('button',{name:'Choose picture',exact:true}).click();
   const dialog=p.getByRole('dialog',{name:'Your profile picture'});await dialog.waitFor();
   const sample=await ctx.request.get(new URL(process.argv[2]).origin+'/api/files/picture-message/0?dir=in');assert(sample.ok());
   await dialog.getByLabel('Choose picture',{exact:true}).setInputFiles({name:'photo.png',mimeType:'image/png',buffer:await sample.body()});
   await dialog.getByRole('button',{name:'Save picture',exact:true}).click({trial:true});
   await dialog.getByLabel('Zoom',{exact:true}).press('ArrowRight');await dialog.getByLabel('Horizontal crop').press('Home');
   assert(await dialog.locator('canvas').evaluate(c=>c.width===256&&c.height===256),'square preview');
   await shot('crop');await dialog.getByRole('button',{name:'Save picture',exact:true}).click();await dialog.waitFor({state:'hidden'});
   await p.getByRole('button',{name:'Remove picture',exact:true}).waitFor();
   const selfPicture=skin==='comic'?p.locator('[data-size="72"] img').first():p.locator('#profile-card .avatar img').first();
   await selfPicture.waitFor({state:'visible'});await selfPicture.evaluate(im=>im.decode());assert(await selfPicture.evaluate(im=>im.naturalWidth>0&&im.naturalWidth===im.naturalHeight&&im.naturalWidth<=256),'rendered square profile picture');await shot('profile');
   const current=await (await ctx.request.get(new URL(process.argv[2]).origin+'/api/overview')).json();assert(current.person.picture&&current.person.picture_url,'saved shared host data');
   await p.getByRole('button',{name:'Remove picture',exact:true}).click();await p.getByRole('button',{name:'Remove picture',exact:true}).waitFor({state:'hidden'});
   if(skin==='comic')await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Chats/}).click();else await p.locator('#settings').evaluate(d=>d.close());
   if(skin==='zoom') {
    // Zoom opens the person, then the DM, then the message's file controls.
    await p.locator('.person-cluster').getByRole('button',{name:/^Bob/}).click();
    await p.getByRole('button',{name:/^Picture chat/}).click();
    await p.locator('.mini-chat').getByRole('button',{name:/avatar\.png/}).click();
   }else await p.getByRole('button',{name:/^Bob/}).first().click();
   // Comic exposes an image thumbnail; the legacy skins expose file chips.
   const openPicture=skin==='comic'
    ? p.getByRole('button',{name:'Open picture avatar.png',exact:true})
    : p.locator('.file').filter({hasText:'avatar.png'}).getByRole('button',{name:'Open',exact:true});
   await openPicture.click();
   await p.getByRole('button',{name:'Use as my picture',exact:true}).click();await dialog.waitFor();await shot('chat-picture');
   await dialog.getByRole('button',{name:'Save picture',exact:true}).click();await dialog.waitFor({state:'hidden'});
   const used=await (await ctx.request.get(new URL(process.argv[2]).origin+'/api/overview')).json();assert(used.person.picture,'chat image selected by person');
   assert.deepEqual(errors,[],skin+' errors');console.log(`P22 ${skin} ${phone?'phone':'desktop'} PASS`);await ctx.close();
  }
  console.log('P22 rendered PASS');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
