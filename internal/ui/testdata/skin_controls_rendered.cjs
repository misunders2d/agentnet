// Disposable public-host fixtures with actual skin source and existing style/assets.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const repo=process.env.AGENTNET_SOURCE||path.resolve(__dirname,'../../..'),collection=process.env.AGENTNET_COLLECTION_SOURCE||path.resolve(repo,'../agentnet-skins'),preview=process.env.AGENTNET_UI_PREVIEW_DIR;
const boot=`
const skin=new URL(location.href).searchParams.get('skin');
const me={person:'atlas',label:'Atlas',state:'self',address:'atlas/desk',fingerprint:'atlas-key',devices:[]},peer={person:'briar',label:'Briar',state:'pinned',address:'briar/phone',fingerprint:'briar-key',devices:[]};
const at='2026-10-15T10:00:00Z',message=(id,body,extra={})=>({id,lid:id,body,from:peer.address,dir:'in',kind:'message',at,actions:[],...extra});
window.view={id:'c'.repeat(64),kind:'dm',role:'member',peer,created:at,mine:true,agents:[],guests:[{pid:'guest-current',host:{...peer,label:'Cedar',person:'cedar',address:'cedar/office'},inviter:me,host_here:false,state:'active',state_text:'Active guest',shared:[],can_end:true}],topics:[],messages:[message('1'.repeat(32),'Parent anchor'),message('2'.repeat(32),'Ordinary correlated turn',{reply_to:'1'.repeat(32)}),message('3'.repeat(32),'Separated agent report',{kind:'answer',verified_agent:true,reply_to:'1'.repeat(32)}),message('4'.repeat(32),'Intentional quote',{reply_to:'1'.repeat(32),quote:'1'.repeat(32)})]};
window.overview={seq:1,topic_list:true,me:{...me,agent:false},person:me,persons:true,people:[peer],peers:[],threads:[],review:[],links:[],held:[],needs_you:[],reminders:[],group_invitations:[],quarantine:[],dms:[{id:view.id,peer,count:4,unread:0,last_at:at}],directory:{current:true,members:[]},agent_devices:[],files:{max_file:1048576,max_count:8}};window.posts=[];
let open,changed;
const skins=['comic','classic','holonet','zoom','office','pulp'].map(id=>({id,name:id[0].toUpperCase()+id.slice(1),api:1}));
const host={version:1,platform:'daemon',workspace:{id:'default',address:me.address,name:'Fixture',endpoint:location.origin,realm:''},workspaces:null,skins,onSkinsChange:()=>()=>{},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},selectSkin(id){window.chosen=id;},api:async(p,body)=>{
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?'))return structuredClone(view);
 if(p.startsWith('/api/agents'))return {host:me.address,local:true,agents:[],sessions:[]};
 if(p==='/api/teams')return {teams:[]};if(p==='/api/groups/invitations')return [];
 if(p.startsWith('/api/typing'))return {current:false,supported:false,send:false,scopes:[]};
 if(p.includes('/topics'))return {topics:[],placements:[]};
 if(p==='/api/dm/send'){posts.push({p,body});const m=message(body.id,body.body,{...body,lid:body.id,dir:'out',from:me.address});view.messages.push(m);changed?.({type:'change',seq:++overview.seq});return{id:body.id,lid:body.id,state:'delivered'};}
 return {};
}};
const base='/skins/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const sheet=new CSSStyleSheet();sheet.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[sheet];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),sheet=new CSSStyleSheet();sheet.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[sheet];const node=document.createElement('div');node.className='skin-root';shadow.append(node);
if(skin!=='comic'){const {mountSkinBar}=await import('/assets/skinbar.mjs');window.bar=mountSkinBar(document.body,{skins:()=>skins,selected:{id:skin,name:skin},home:{id:'comic',name:'Comic'},choose:id=>window.chosen=id,host:()=>host,workspaces:null});await bar.ready;host.controls={open:trigger=>bar.open(trigger),claim:owner=>owner===node&&owner.isConnected?bar.claim():()=>{}};}
window.skinModule=await import(base+manifest.entry);await skinModule.mount(node,host);window.openChat=()=>open(view.id,'conversation');
window.ready=true;
`;
const server=http.createServer((req,res)=>{
 const p=new URL(req.url,'http://fixture').pathname;
 if(p==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Actual skins fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/core.css"><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}
 if(p==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}
 let f;
 const m=/^\/skins\/([^/]+)\/(.+)$/.exec(p);
 if(m){
  if(['classic','holonet','zoom'].includes(m[1])){
   if(m[2]==='skin.json')f=path.join(collection,'skins',m[1],'skin.json');
   else if(m[2]==='manifest.mjs'){res.setHeader('Content-Type','text/javascript');res.end('export default '+fs.readFileSync(path.join(collection,'skins',m[1],'skin.json'),'utf8'));return;}
   else f=path.join(collection,'skins',m[1],'src',m[2]);
  }else if(preview&&m[2]==='entry.mjs')f=path.join(preview,m[1]+'-entry.mjs');
  else f=m[1]==='comic'?path.join(repo,'internal/ui/static/skins/comic',m[2]):path.join(collection,'published',m[1],m[2]);
 }
 else if(p.startsWith('/assets/'))f=path.join(repo,'internal/ui/static',p.slice(8));
 if(!f||!fs.existsSync(f)||!fs.statSync(f).isFile()){res.writeHead(404).end();return;}
 res.setHeader('Content-Type',/\.(mjs|js)$/.test(f)?'text/javascript':f.endsWith('.css')?'text/css':f.endsWith('.json')?'application/json':f.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(f));
});
(async()=>{let browser;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM});
 for(const skin of (process.env.AGENTNET_TEST_SKINS||(process.env.AGENTNET_COLLECTION_SOURCE?'comic,classic,holonet,zoom,office,pulp':'comic')).split(','))for(const width of [1280,390]){
  const context=await browser.newContext({viewport:{width,height:844},reducedMotion:'reduce'}),page=await context.newPage(),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(e.stack));
  await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  await page.goto(origin+'/?skin='+skin);await page.waitForFunction(()=>window.ready);await page.evaluate(()=>openChat());await page.locator('#skin').getByText('Intentional quote',{exact:false}).first().waitFor();if(skin!=='comic')await page.waitForFunction(()=>bar.element.shadowRoot.querySelector('.launcher').hidden);
  const overlaps=await page.evaluate(()=>{const launcher=document.querySelector('#skin-bar')?.shadowRoot.querySelector('.launcher');if(!launcher||launcher.hidden)return[];const a=launcher.getBoundingClientRect();return [...document.querySelector('#skin').shadowRoot.querySelectorAll('button')].filter(b=>{const r=b.getBoundingClientRect();return r.width&&r.height&&r.left<a.right&&r.right>a.left&&r.top<a.bottom&&r.bottom>a.top;}).map(b=>({name:b.getAttribute('aria-label')||b.title||b.textContent,rect:b.getBoundingClientRect().toJSON()}));});
  assert.deepEqual(overlaps,[],skin+' has no host obstruction');
  const app=page.locator('#skin');
  const snap=async label=>{if(process.env.AGENTNET_SCREENSHOTS){await page.evaluate(async()=>{await document.fonts.ready;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));const root=document.querySelector('#skin').shadowRoot.querySelector('.skin-root');await Promise.all(root.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`actual-${skin}-${width}-${label}.png`)});}};
  await snap('default-closed');
  if(['comic','office','pulp'].includes(skin)){
   assert.equal(await app.locator('aside[aria-label="In this chat"]').count(),0,'participants closed by default even with an active guest');
   await app.getByRole('button',{name:'More',exact:true}).click();await app.getByRole('menuitem',{name:'In this chat',exact:true}).click();
   if(width>=1024){await app.locator('aside[aria-label="In this chat"]').waitFor();await snap('panel-open');await app.getByRole('button',{name:'Close panel',exact:true}).click();}
   else{await app.getByRole('dialog',{name:'In this chat',exact:true}).waitFor();await snap('panel-open');await app.getByRole('button',{name:'Close',exact:true}).click();}
   assert.equal(await app.locator('aside[aria-label="In this chat"]').count(),0);
   await app.getByRole('button',{name:/^All topics/}).click();await app.getByRole('dialog',{name:'All topics',exact:true}).waitFor();await snap('all-topics');await app.getByRole('button',{name:width>=1024?'Close':'Back to the conversation',exact:true}).click();
   const row=id=>app.locator('[data-mid="'+id.repeat(32)+'"]');
   assert.equal(await row('2').getByRole('button').filter({hasText:'Parent anchor'}).count(),0,'ordinary correlation is not a quote');
   assert.equal(await row('4').getByRole('button').filter({hasText:'Parent anchor'}).count(),1,'intentional quote appears once');
   assert.equal(await row('3').getByRole('button').filter({hasText:'answer to Parent anchor'}).count(),1,'distant answer navigation retained');
   const composer=app.getByRole('form',{name:'Write a message'}),field=composer.locator('textarea');
   await field.fill('Ordinary composer send');await composer.getByRole('button',{name:/^Send/}).click();await page.waitForFunction(()=>posts.length===1);
   assert.equal(await page.evaluate(()=>posts[0].body.quote||''),'','ordinary composer send has no quote');
   const parent=row('1');await parent.scrollIntoViewIfNeeded();
   if(width>=1024){await parent.hover();await parent.getByRole('button',{name:'Reply',exact:true}).click();}
   else{const actions=parent.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await app.getByRole('button',{name:'Reply',exact:true}).click();}
   await composer.getByRole('button',{name:'Cancel reply',exact:true}).waitFor();await field.fill('Chosen quoted send');await composer.getByRole('button',{name:/^Send/}).click();await page.waitForFunction(()=>posts.length===2);
   assert.deepEqual(await page.evaluate(()=>({quote:posts[1].body.quote,reply:posts[1].body.reply_to})),{quote:'1'.repeat(32),reply:'1'.repeat(32)},'explicit Reply preserves one quote and exact correlation');
   if(width<1024)await app.getByRole('button',{name:'Back to chats',exact:true}).click();
   await app.getByRole('button',{name:width>=1024?'Settings':'You',exact:true}).click();await app.getByRole('button',{name:/^Appearance/}).click();
  }else{
   if(skin==='holonet'){assert.equal(await app.locator('.crew-console').isVisible(),false,'crew sidebar starts collapsed');await app.locator('#crew-toggle').click();await app.locator('#crew-drawer').waitFor();await snap('crew-open');await app.locator('#crew-close').click();assert.equal(await app.locator('.crew-console').isVisible(),false);}
   const all=app.locator('#topics-bar button').filter({hasText:/^All/}).first();if(await all.count()){await all.click();await app.getByRole('dialog',{name:'All topics',exact:true}).waitFor();await app.getByRole('button',{name:'Close',exact:true}).click();}
   const details=app.locator('#conversation-details');if(await details.isVisible()){await details.click();await app.locator('#dialog').waitFor();await app.locator('#dialog-cancel').click();}
   const settings=app.locator('#settings-btn');if(!await settings.isVisible())await app.locator('#back').click();await settings.click();
  }
  if(skin!=='comic'){
   const controls=app.getByRole('button',{name:'AgentNet controls',exact:true});await controls.click();
   const nav=page.getByRole('navigation',{name:'Skin and workspace'});await nav.waitFor();await nav.getByRole('button',{name:/^Skin:/}).click();await page.getByRole('menu',{name:'Skins',exact:true}).waitFor();await snap('host-menu');await page.keyboard.press('Escape');await page.keyboard.press('Escape');
   assert.equal(await controls.evaluate(e=>e.getRootNode().activeElement===e),true,'Settings focus restored');
  }else assert.equal(await app.getByRole('button',{name:'AgentNet controls',exact:true}).count(),0,'Comic retains its existing controls');
  assert.deepEqual(errors,[],skin+' runtime health');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no page overflow');
  if(process.env.AGENTNET_SCREENSHOTS)await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`actual-${skin}-${width}-settings.png`)});
  console.log(JSON.stringify({skin,width,overlaps,errors,pass:true}));
  await context.close();
 }
 console.log('Actual skin controls PASS');
}finally{await browser?.close();await new Promise(r=>server.close(r));}})().catch(e=>{console.error(e);process.exitCode=1;});
