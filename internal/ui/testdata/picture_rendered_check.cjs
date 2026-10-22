const assert=require('node:assert/strict');
const fs=require('node:fs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
 try {
  if(process.env.AGENTNET_PICTURE_EDITOR_ONLY) {
   for(const [name,width,height] of [['desktop',1280,900],['phone',390,844]]) {
   const ctx=await browser.newContext({viewport:{width,height},hasTouch:true}),p=await ctx.newPage();
   const errors=[];p.on('pageerror',e=>errors.push(String(e)));await p.goto(process.argv[2]);
   await p.waitForFunction(()=>document.querySelector('#skin')?.shadowRoot?.querySelector('.an-root'));
   await p.evaluate(async()=>{const {openPictureEditor}=await import('/assets/pictures.mjs');
    void openPictureEditor({into:document.querySelector('#skin').shadowRoot.querySelector('.an-root'),src:'/api/files/picture-message/0?dir=in',save:async png=>{
     const r=await fetch('/api/person/picture',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({png})});if(!r.ok)throw Error(await r.text());
    }});
   });
   const dialog=p.getByRole('dialog',{name:'Your profile picture'}),canvas=dialog.locator('canvas');
   await dialog.getByRole('button',{name:'Save picture'}).click({trial:true});
   const geometry=await dialog.evaluate(d=>{const b=d.getBoundingClientRect(),h=getComputedStyle(d.querySelector('h2'));return{left:b.left,right:b.right,top:b.top,bottom:b.bottom,width:innerWidth,height:innerHeight,fontSize:h.fontSize,fontWeight:h.fontWeight};});
   assert(Math.abs(geometry.left-(geometry.width-geometry.right))<=2,name+': profile editor centered horizontally '+JSON.stringify(geometry));
   assert(Math.abs(geometry.top-(geometry.height-geometry.bottom))<=2,name+': profile editor centered vertically '+JSON.stringify(geometry));
   assert(geometry.left>=11&&geometry.right<=geometry.width-11&&geometry.top>=11&&geometry.bottom<=geometry.height-11,name+': profile editor fits viewport');
   assert(parseFloat(geometry.fontSize)>=20&&Number(geometry.fontWeight)>=700,name+': readable editor heading');
   assert.equal(await dialog.locator('input[type=range]').count(),0);
   await dialog.getByRole('button',{name:'Zoom in'}).click();await canvas.press('ArrowRight');
   const before=await canvas.evaluate(c=>c.toDataURL());const b=await canvas.boundingBox();
   await p.mouse.move(b.x+100,b.y+100);await p.mouse.down();await p.mouse.move(b.x+140,b.y+120);await p.mouse.up();
   assert.notEqual(await canvas.evaluate(c=>c.toDataURL()),before,'drag changes crop');
   await canvas.hover();await p.mouse.wheel(0,-120);await p.waitForTimeout(100);assert.notEqual(await dialog.getByLabel('Zoom level').textContent(),'115%');
   await dialog.getByRole('button',{name:'Reset',exact:true}).click();
   const cdp=await ctx.newCDPSession(p);
   const touches=(distance)=>[{x:b.x+128-distance/2,y:b.y+128},{x:b.x+128+distance/2,y:b.y+128}];
   await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:touches(60)});
   await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:touches(120)});
   await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
   assert(Number((await dialog.getByLabel('Zoom level').textContent()).replace('%',''))>100,'pinch zooms');
   assert(await dialog.evaluate(d=>d.getBoundingClientRect().left>=0&&d.getBoundingClientRect().right<=innerWidth),'phone dialog fits');
   if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:process.env.AGENTNET_SCREENSHOTS+'/p22-shared-crop-'+name+'.png'});}
   await dialog.getByRole('button',{name:'Save picture'}).click();await dialog.waitFor({state:'hidden'});
   const saved=await(await ctx.request.get(new URL(process.argv[2]).origin+'/api/overview')).json();assert(saved.person.picture,'strict server accepted edited picture');
   assert.deepEqual(errors,[]);await ctx.close();console.log('P22 shared crop editor '+name+' PASS');
   }
   console.log('P22 shared crop editor PASS');return;
  }
  for(const phone of [false,true])for(const skin of ['comic']){
   const ctx=await browser.newContext({viewport:phone?{width:390,height:844}:{width:1440,height:900},isMobile:phone,hasTouch:phone});
   await ctx.addInitScript(()=>{window.__pictureClipboardReads=0;window.__pictureClipboardMode='image';window.__agentnetNativeClipboardImage=async()=>{
    window.__pictureClipboardReads++;if(window.__pictureClipboardMode==='text')return null;
    const c=document.createElement('canvas');c.width=c.height=16;const x=c.getContext('2d');x.fillStyle='#ffd43b';x.fillRect(0,0,16,16);
    return new File([await new Promise(r=>c.toBlob(r,'image/png'))],'image.png',{type:'image/png'});
   };});
   const p=await ctx.newPage(),errors=[];p.on('pageerror',e=>errors.push(String(e)));
   await p.goto(process.argv[2]);await p.goto(new URL(process.argv[2]).origin+'/?skin='+skin);
   const shot=async name=>{if(process.env.AGENTNET_SCREENSHOTS){fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS,{recursive:true});await p.screenshot({path:process.env.AGENTNET_SCREENSHOTS+`/p22-${skin}-${phone?'phone':'desktop'}-${name}.png`});}};
   const profile=async()=>{
    if(phone) {
     await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:'You',exact:true}).click();
     await p.getByRole('button',{name:/^Alice\b.*On 1 device/}).click();
    }else await p.getByRole('button',{name:'You: profile and devices'}).click();
   };
   await profile();
   const dialog=p.getByRole('dialog',{name:'Your profile picture'});
   // Closing the editor puts focus back on what opened it, inside the skin's shadow root.
   await p.getByRole('button',{name:'Choose picture',exact:true}).click();await dialog.waitFor();
   await dialog.getByRole('button',{name:'Cancel',exact:true}).click();await dialog.waitFor({state:'hidden'});
   assert.equal(await p.evaluate(()=>{const a=document.getElementById('skin').shadowRoot.activeElement;return a?(a.getAttribute('aria-label')||a.textContent.trim()):null;}),'Choose picture',skin+': focus returns to the opener');
   await p.getByRole('button',{name:'Choose picture',exact:true}).click();await dialog.waitFor();
   const sample=await ctx.request.get(new URL(process.argv[2]).origin+'/api/files/picture-message/0?dir=in');assert(sample.ok());
   await dialog.getByLabel('Choose picture',{exact:true}).setInputFiles({name:'photo.png',mimeType:'image/png',buffer:await sample.body()});
   await dialog.getByRole('button',{name:'Save picture',exact:true}).click({trial:true});
   await dialog.getByRole('button',{name:'Zoom in',exact:true}).click();
   await dialog.locator('canvas').press('ArrowLeft');
   assert.equal(await dialog.locator('input[type=range]').count(),0,'crop has no sliders');
   assert.equal(await dialog.locator('canvas').evaluate(c=>getComputedStyle(c).borderRadius),'50%','round preview');
   const cropBox=await dialog.locator('canvas').boundingBox();
   await p.mouse.move(cropBox.x+128,cropBox.y+128);await p.mouse.down();await p.mouse.move(cropBox.x+160,cropBox.y+140);await p.mouse.up();
   await dialog.locator('canvas').hover();await p.mouse.wheel(0,-100);
   assert.notEqual(await dialog.getByLabel('Zoom level').textContent(),'100%','wheel zoom changes crop');
   assert(await dialog.locator('canvas').evaluate(c=>c.width===256&&c.height===256),'square preview');
   await shot('crop');await dialog.getByRole('button',{name:'Save picture',exact:true}).click();await dialog.waitFor({state:'hidden'});
   await p.getByRole('button',{name:'Remove picture',exact:true}).waitFor();
   const selfPicture=p.locator('[data-size="72"] img').first();
   await selfPicture.waitFor({state:'visible'});await selfPicture.evaluate(im=>im.decode());assert(await selfPicture.evaluate(im=>im.naturalWidth>0&&im.naturalWidth===im.naturalHeight&&im.naturalWidth<=256),'rendered square profile picture');await shot('profile');
   const current=await (await ctx.request.get(new URL(process.argv[2]).origin+'/api/overview')).json();assert(current.person.picture&&current.person.picture_url,'saved shared host data');
   await p.getByRole('button',{name:'Remove picture',exact:true}).click();await p.getByRole('button',{name:'Remove picture',exact:true}).waitFor({state:'hidden'});
   await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Chats/}).click();
   await p.getByRole('button',{name:/^Bob/}).first().click();
   const field=p.locator('#skin textarea').last();
   const pasted=p.getByRole('button',{name:/^Remove pasted-image-/});
   await field.fill('');
   await field.evaluate(el=>{const dt=new DataTransfer();dt.items.add(new File([new Uint8Array([1,2,3])],'image.png',{type:'image/png'}));dt.setData('text/plain','companion text');el.dispatchEvent(new ClipboardEvent('paste',{bubbles:true,cancelable:true,clipboardData:dt}));});
   await pasted.first().waitFor();assert.equal(await field.inputValue(),'','browser image wins text');assert.equal(await p.evaluate(()=>window.__pictureClipboardReads),0,'browser files skip native read');await pasted.first().click();
   await field.evaluate(el=>{const dt=new DataTransfer();dt.setData('text/plain','native companion text');el.dispatchEvent(new ClipboardEvent('paste',{bubbles:true,cancelable:true,clipboardData:dt}));});
   await pasted.first().waitFor();assert.equal(await field.inputValue(),'','native image wins text');await shot('paste');await pasted.first().click();
   await p.evaluate(()=>{window.__pictureClipboardMode='text';});
   await field.evaluate(el=>{const dt=new DataTransfer();dt.setData('text/plain','ordinary pasted text');el.dispatchEvent(new ClipboardEvent('paste',{bubbles:true,cancelable:true,clipboardData:dt}));});
   await p.waitForFunction(()=>[...document.querySelector('#skin').shadowRoot.querySelectorAll('textarea')].some(el=>el.value==='ordinary pasted text'));assert.equal(await field.inputValue(),'ordinary pasted text','native text-only paste preserved');await field.fill('');
   // Comic exposes an image thumbnail.
   const openPicture=p.getByRole('button',{name:'Open picture avatar.png',exact:true});
   await openPicture.click();
   await p.getByRole('button',{name:'Use as my picture',exact:true}).click();await dialog.waitFor();await shot('chat-picture');
   await dialog.getByRole('button',{name:'Save picture',exact:true}).click();await dialog.waitFor({state:'hidden'});
   const used=await (await ctx.request.get(new URL(process.argv[2]).origin+'/api/overview')).json();assert(used.person.picture,'chat image selected by person');
   assert.deepEqual(errors,[],skin+' errors');console.log(`P22 ${skin} ${phone?'phone':'desktop'} PASS`);await ctx.close();
  }
  console.log('P22 rendered PASS');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
