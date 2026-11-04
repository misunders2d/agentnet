// Real host and compiled Comic over two isolated fixture memberships. Native
// shell parsers have separate tests; this executes their cold/warm fragments.
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT),assert=require('node:assert/strict'),path=require('node:path');
const url=process.argv[2],realm='a'.repeat(32),otherRealm='f'.repeat(32),conv='b'.repeat(64),topic='c'.repeat(32),other='e'.repeat(32);
const link=(r,t)=>'agentnet://open#realm='+r+'&conv='+conv+'&topic='+t;
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
 try{
  for(const [name,width,height]of [['desktop',1280,800],['phone',390,844]]){
   const context=await browser.newContext({viewport:{width,height},permissions:['clipboard-read','clipboard-write']});
   const page=await context.newPage(),errors=[],posts=[];
   page.on('pageerror',e=>errors.push(e.message));
   page.on('request',r=>{if(r.method()==='POST')posts.push(new URL(r.url()).pathname);});
   await page.goto(url+'#'+link(realm,topic).split('#')[1]);
   const field=()=>page.getByRole('textbox',{name:'Message Bob',exact:true});
   try { await field().waitFor({timeout:10000}); }
   catch(e){console.error('cold entry',await page.evaluate(()=>({body:document.body.innerText,skin:document.getElementById('skin')?.shadowRoot?.textContent,workspace:window.agentnet?.workspace,hash:location.hash})),errors);if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'conversation-link-cold-failure-'+name+'.png')});throw e;}
   await page.locator('[data-mid="'+'4'.repeat(32)+'"]').waitFor();
   assert.equal(await page.locator('[data-mid="'+'3'.repeat(32)+'"]').count(),0,'cold entry selects exact topic');
   assert.equal(await page.evaluate(()=>{const a=document.createElement('a');a.href='agentnet:agent/existing-mention';document.body.append(a);let blocked;document.addEventListener('click',e=>{blocked=e.defaultPrevented;e.preventDefault();},{once:true});a.click();a.remove();return blocked;}),false,'portable routing leaves agent mention links to their existing handler');
   await page.getByRole('button',{name:'More',exact:true}).click();
   await page.getByRole('menuitem',{name:'Copy topic link',exact:true}).click();
   assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),link(realm,topic));
   await field().fill('unsent draft stays here');
   await page.locator('input[type=file]').setInputFiles({name:'draft.txt',mimeType:'text/plain',buffer:Buffer.from('unsent attachment')});
   await page.getByText('draft.txt',{exact:true}).waitFor();
   const bad=[link('9'.repeat(32),'main'),link(realm,'8'.repeat(32)),link(realm,'main')+'&realm='+realm,link(realm,'main').replace('realm','%72ealm')];
   const open=async(value)=>page.evaluate(async value=>{try{await window.agentnet.openLink(value);return true}catch{return false}},value);
   const draftBefore=await page.evaluate(()=>localStorage.getItem('agentnet.messenger.drafts.default'));
   for(const value of bad){
    assert.equal(await open(value),false);assert.equal(await page.evaluate(()=>window.agentnet.workspace.id),'default');
    assert.equal(await field().inputValue(),'unsent draft stays here');await page.getByText('draft.txt',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>localStorage.getItem('agentnet.messenger.drafts.default')),draftBefore);
    await page.locator('.skin-route-notice').waitFor({state:'visible'});
   }
   await page.evaluate(value=>{const a=document.createElement('a');a.href=value;document.body.append(a);a.click();a.remove();},link(realm,'main'));
   try{await page.locator('[data-mid="'+'3'.repeat(32)+'"]').waitFor({timeout:5000});}
   catch(e){console.error('Main landing',await page.evaluate(()=>({matches:[...document.getElementById('skin').shadowRoot.querySelectorAll('*')].filter(e=>e.textContent.trim()==='Main fixture message').map(e=>({html:e.outerHTML,rect:e.getBoundingClientRect().toJSON(),display:getComputedStyle(e).display})),drafts:Object.fromEntries(Object.entries(localStorage).filter(([k])=>k.includes('draft')))})));if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'conversation-link-main-failure-'+name+'.png')});throw e;}
   assert.equal(await page.locator('[data-mid="'+'4'.repeat(32)+'"]').count(),0);
   assert.equal(await field().inputValue(),'unsent draft stays here');
   await page.getByRole('link',{name:'Open renamed topic',exact:true}).click();
   await page.locator('[data-mid="'+'4'.repeat(32)+'"]').waitFor();
   assert.equal(await field().inputValue(),'unsent draft stays here');await page.getByText('draft.txt',{exact:true}).waitFor();
   const before=posts.length;
   assert.equal(await open(link(otherRealm,topic)),true);assert.equal(await page.evaluate(()=>window.agentnet.workspace.id),other);
   assert.equal(await field().inputValue(),'');assert.equal(await page.getByText('draft.txt',{exact:true}).count(),0,'draft belongs to original workspace');
   await page.evaluate(()=>window.agentnet.workspaces.select('default'));
   await field().waitFor();await page.waitForFunction(()=>window.agentnet.workspace.id==='default');
   assert.equal(await field().inputValue(),'unsent draft stays here');await page.getByText('draft.txt',{exact:true}).waitFor();
   // Disappeared after prevalidation: the second exact local read fails. The
   // host returns to the original binding with its open view and staged draft.
   let reads=0;
   await page.route('**/workspaces/'+other+'/*/api/dm?*',async route=>{if(++reads>1)await route.fulfill({status:404,body:'That topic was removed meanwhile.'});else await route.continue();});
   assert.equal(await open(link(otherRealm,topic)),false);
   await page.unroute('**/workspaces/'+other+'/*/api/dm?*');
   assert.equal(await page.evaluate(()=>window.agentnet.workspace.id),'default');
   assert.equal(await field().inputValue(),'unsent draft stays here');await page.getByText('draft.txt',{exact:true}).waitFor();
   assert(posts.slice(before).every(p=>p.endsWith('/api/act')||p.endsWith('/api/topic/transfer/location')),'navigation never joins, approves, sends, stages, or requests shared history: '+posts.slice(before));
   assert(!posts.some(p=>/\/api\/(refresh|send|dm\/send|.*\/invite|.*\/decide)/.test(p)));
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
   if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,'conversation-link-error-'+name+'.png')});
   assert.deepEqual(errors,[]);await context.close();
  }
  const context=await browser.newContext(),page=await context.newPage();
  await context.request.get(url); // acquire this fresh fixture context's UI cookie
  const skins=await(await context.request.get(new URL('/assets/skins/index.json',url).href)).json(),notebook=skins.find(s=>s.id==='notebook');
  assert(notebook,'fallback skin is installed');
  await page.addInitScript(d=>localStorage.setItem('agentnet.skin.trusted.notebook',d),notebook.digest);
  const fallbackURL=new URL(url);fallbackURL.search='?skin=notebook';fallbackURL.hash=link(otherRealm,topic).split('#')[1];
  await page.goto(fallbackURL.href);
  const fallback=page.getByRole('button',{name:'Open it in Comic',exact:true});
  try{await fallback.waitFor({timeout:5000});}catch(e){console.error('fallback fixture',await page.evaluate(()=>({body:document.body.innerText,skin:document.getElementById('skin')?.shadowRoot?.textContent,host:window.agentnet?.workspace})));throw e;}
  assert.equal(await page.evaluate(()=>window.agentnet.workspace.id),'default','unsupported skin does not select another workspace');
  await fallback.click();await page.locator('[data-mid="'+'4'.repeat(32)+'"]').waitFor();
  assert.equal(await page.evaluate(()=>window.agentnet.workspace.id),other,'explicit fallback retains exact destination');
  await context.close();
  console.log('conversation links rendered PASS: cold/warm exact Main/topic, copy, two realms/local aliases, failure rollback, drafts/files, host error desktop/phone, explicit fallback, no send/history refresh');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
