// Opt-in real relay + two production browser engines. No provider substitution.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {execFileSync}=require('node:child_process'),{X509Certificate,createHash}=require('node:crypto');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const world=process.env.AGENTNET_PERSON_WORLD,out=process.env.AGENTNET_SCREENSHOTS;
assert(world&&out&&process.env.AGENTNET_PERSON_BINARY);fs.mkdirSync(out,{recursive:true,mode:0o700});
const origin='https://127.0.0.1:'+fs.readFileSync(path.join(world,'mux-port'),'utf8').trim();
const cli=(who,...args)=>execFileSync(process.env.AGENTNET_PERSON_BINARY,['--home',path.join(world,who),...args],{encoding:'utf8',env:process.env});
const cert=new X509Certificate(fs.readFileSync(path.join(world,'cert.pem'))),spki=createHash('sha256').update(cert.publicKey.export({type:'spki',format:'der'})).digest('base64');
const api=(p,url,body)=>p.evaluate(({url,body})=>agentnet.api(url,body),{url,body});
const ready=p=>p.waitForFunction(()=>typeof state!=='undefined'&&state.overview?.device?.online&&state.overview?.directory?.current,null,{timeout:20000});
const identities=[],checks=[],shots=[],errors=[],external=[],assets={},packages=[];let browser,phase='launch';
const hash=b=>createHash('sha256').update(b).digest('hex');
async function appearance(p){await p.locator('#profile-btn').click();await p.getByRole('tab',{name:'Appearance',exact:true}).click();}
async function capture(p,name,width){await p.setViewportSize({width,height:900});assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,name+': document overflow');const file=path.join(out,name+'-'+width+'.png');await p.screenshot({path:file});shots.push(file);}
async function bothWidths(p,name){for(const width of [390,1280])await capture(p,name,width);}
async function snapshot(p,conv){return p.evaluate(async conv=>{const o=await agentnet.api('/api/overview'),dm=await agentnet.api('/api/dm?id='+conv);return {identity:{person:o.person.person,address:o.me.address,fingerprint:o.me.fingerprint,workspace:agentnet.workspace.id,realm:agentnet.workspace.realm},conv:dm.id,peer:dm.peer.person,history:dm.messages.map(m=>({id:m.id,from:m.from,body:m.body,kind:m.kind,reply_to:m.reply_to}))}},conv);}
async function enroll(p,who){
 const raw=cli(who,'person','link').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0],offer=JSON.parse(Buffer.from(raw.split(':')[1],'base64url')),invite=JSON.parse(Buffer.from(offer.invite.split(':')[1],'base64url'));
 delete invite.cert;offer.invite='agentnet-invite-v1:'+Buffer.from(JSON.stringify(invite)).toString('base64url');
 await p.goto(origin+'/#agentnet-link-v2:'+Buffer.from(JSON.stringify(offer)).toString('base64url'));await p.locator('#join-name').fill('skin-'+who);await p.locator('button.join-go').click();
 let request;for(let n=0;n<100&&!request;n++){request=cli(who,'person','links').match(new RegExp('^([a-f0-9]{32})\\s+pending\\s+[^\\n]*skin-'+who,'m'))?.[1];if(!request)await new Promise(r=>setTimeout(r,100));}
 assert(request,'real signed browser link request '+who);cli(who,'person','approve',request);await ready(p);assert.equal(await p.evaluate(()=>agentnet.platform),'browser');
}
function variant(who){const dir=path.resolve(__dirname,'../../..','examples/skins/notebook'),manifest=JSON.parse(fs.readFileSync(path.join(dir,'skin.json'),'utf8'));manifest.id='notebook-'+who;manifest.name='Notebook '+who+' (manifest-only fixture variant)';const payload=[{name:'skin.json',mimeType:'application/json',buffer:Buffer.from(JSON.stringify(manifest))},...manifest.files.map(name=>({name,mimeType:'application/octet-stream',buffer:fs.readFileSync(path.join(dir,name))}))];packages.push({who,id:manifest.id,name:manifest.name,files:Object.fromEntries(payload.map(x=>[x.name,hash(x.buffer)]))});return {payload,name:manifest.name,id:'local:'+manifest.id};}
async function local(p){return p.evaluate(async()=> (await import('/assets/local-skins.mjs')).catalog());}
async function escape(p){await p.getByRole('button',{name:'AgentNet ▾',exact:true}).click();await p.getByRole('menuitem',{name:'Back to AgentNet',exact:true}).click();await ready(p);assert.equal(new URL(p.url()).searchParams.get('skin'),'default');}
(async()=>{
 browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM,args:['--ignore-certificate-errors-spki-list='+spki]});
 const contexts=[],pages=[];
 for(let i=0;i<2;i++){const c=await browser.newContext({viewport:{width:1280,height:900}});contexts.push(c);await c.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():(external.push(new URL(r.request().url()).origin),r.abort()));const p=await c.newPage();pages.push(p);p.setDefaultTimeout(15000);p.on('pageerror',e=>errors.push(e.message));p.on('console',m=>{if(m.type()==='error')errors.push(m.text())});p.on('response',async r=>{const file=new URL(r.url()).pathname;if(file.startsWith('/assets/')&&!file.includes('/skins/'))try{assets[file]=hash(await r.body())}catch{}});}
 const [a,b]=pages;phase='real two-browser enrollment';await enroll(a,'alice');await enroll(b,'bob');
 // Initial optional workspace realm is populated by verified version fetch.
 // Reload the ordinary default page before taking immutable baseline.
 phase='enrolled default-page metadata readiness';const realms=[];
 for(const p of pages){await p.reload();await ready(p);const realm=await p.evaluate(()=>agentnet.workspace.realm);assert.match(realm,/^[a-f0-9]{32}$/,'verified nonempty workspace realm');realms.push(realm);}
 assert.equal(realms[0],realms[1],'both clients belong to same relay realm');
 const serverCatalog=await a.evaluate(async()=> (await fetch('/assets/skins/index.json')).json());assert(!serverCatalog.some(s=>s.id.startsWith('notebook-')));assert.equal(fs.existsSync(path.join(world,'hub/skins')),false,'no relay skin install');
 phase='real DM baseline';const conv=(await api(a,'/api/dm/new',{address:'bob/desk'})).id;await api(a,'/api/dm/send',{conv,body:'Synthetic baseline before browser-local interfaces'});
 await b.waitForFunction(id=>state.overview.dms.some(d=>d.id===id),conv);await b.waitForFunction(async id=>(await agentnet.api('/api/dm?id='+id)).messages.some(m=>m.body==='Synthetic baseline before browser-local interfaces'),conv);
 const baseline=await Promise.all(pages.map(p=>snapshot(p,conv)));identities.push(...baseline.map(s=>s.identity));assert.notEqual(identities[0].address,identities[1].address);
 const variants=[variant('amber'),variant('indigo')];
 for(const [i,p] of pages.entries()){
  phase='local import and consent '+i;await appearance(p);await p.getByLabel('Import package files',{exact:true}).setInputFiles(variants[i].payload);await p.getByText('Stored '+variants[i].name+'.',{exact:false}).waitFor();
  assert.equal(await p.getByRole('heading',{name:'Notebook',exact:true}).count(),0,'import did not mount');await bothWidths(p,'client-'+i+'-import');
  const c=await local(p);assert.equal(c.length,1);assert.equal(c[0].id,variants[i].id);packages[i].digest=c[0].digest;
  assert.equal((await local(pages[1-i])).some(s=>s.id===variants[i].id),false,'other client not installed');
  await p.getByRole('button',{name:variants[i].name,exact:true}).click();await p.getByRole('button',{name:'Use this UI',exact:true}).waitFor();await bothWidths(p,'client-'+i+'-consent');await p.getByRole('button',{name:'Use this UI',exact:true}).click();await p.getByRole('heading',{name:'Notebook',exact:true}).waitFor();await p.getByText('You are '+identities[i].address+' ('+(i?'Bob':'Alice')+')',{exact:true}).waitFor();await bothWidths(p,'client-'+i+'-selected');
  assert.deepEqual(await snapshot(p,conv),baseline[i]);
  phase='own selected package reload '+i;await p.reload();await p.getByRole('heading',{name:'Notebook',exact:true}).waitFor();assert.equal(new URL(p.url()).searchParams.get('skin'),variants[i].id);assert.equal(await p.getByRole('button',{name:'Use this UI',exact:true}).count(),0,'digest consent retained');assert.deepEqual(await snapshot(p,conv),baseline[i]);
 }
 assert.notEqual(packages[0].digest,packages[1].digest);checks.push({twoProductionBrowserDevicesSameRelay:true,manifestOnlyNotebookVariants:true,differentLocalDigests:true,identityAndBaselineHistoryPreserved:true,ownSelectionConsentReload:true,noRelaySkinInstall:true});
 phase='actual Notebook send and reply';
 for(const [i,p] of pages.entries()){const label=i?'Alice':'Bob';await p.locator('nav[aria-label="Conversations"] button').filter({has:p.locator('strong').filter({hasText:new RegExp('^'+label+'$')})}).click();await p.getByRole('heading',{name:label,exact:true}).waitFor();}
 await a.getByRole('textbox',{name:'Message',exact:true}).fill('Amber Notebook actual browser send');await a.getByRole('button',{name:'Send to Bob',exact:true}).click();await b.getByText('Amber Notebook actual browser send',{exact:true}).waitFor();
 await b.getByRole('textbox',{name:'Message',exact:true}).fill('Indigo Notebook actual browser reply');await b.getByRole('button',{name:'Send to Alice',exact:true}).click();await a.getByText('Indigo Notebook actual browser reply',{exact:true}).waitFor();
 const history=await Promise.all(pages.map(p=>snapshot(p,conv)));for(let i=0;i<2;i++){assert.deepEqual(history[i].identity,identities[i]);assert(history[i].history.some(m=>m.body==='Amber Notebook actual browser send'));assert(history[i].history.some(m=>m.body==='Indigo Notebook actual browser reply'));await bothWidths(pages[i],'client-'+i+'-messaging');}
 checks.push({actualNotebookSendReplyAcrossBrowsers:true});
 phase='host escape and owner-local remove';await escape(a);await appearance(a);await bothWidths(a,'client-0-escaped');
 a.once('dialog',d=>d.accept());await a.getByRole('button',{name:'Remove',exact:true}).click();await a.getByText('Removed from this browser.',{exact:true}).waitFor();assert.equal((await local(a)).length,0);await a.reload();await ready(a);assert.equal((await local(a)).length,0);assert.deepEqual(await snapshot(a,conv),history[0]);
 assert.equal(new URL(b.url()).searchParams.get('skin'),variants[1].id);assert.equal((await local(b))[0].digest,packages[1].digest);assert.deepEqual(await snapshot(b,conv),history[1]);await b.reload();await b.getByRole('heading',{name:'Notebook',exact:true}).waitFor();assert.equal(await b.getByRole('button',{name:'Use this UI',exact:true}).count(),0);assert.deepEqual(await snapshot(b,conv),history[1]);await bothWidths(b,'client-1-unaffected-after-other-remove');
 await escape(b);await appearance(b);await bothWidths(b,'client-1-escaped');assert.equal((await local(b))[0].digest,packages[1].digest);assert.deepEqual(await snapshot(b,conv),history[1]);checks.push({bothHostEscapes:true,removeOnlyOwner:true,otherConsentPackageIdentityHistoryRetained:true});
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);assert.equal(fs.existsSync(path.join(world,'hub/skins')),false);
 fs.writeFileSync(path.join(out,'result.json'),JSON.stringify({pass:true,checks,shots,identities,packages,assets,errors,external,actual:'two isolated Chromium contexts; production Engine/IDB/loader/app/Notebook; one real synthetic signed relay',limits:'full-trust manifest variants, not marketplace isolation/certification'},null,2),{mode:0o600});console.log('PASS same-relay browser-local packages / real Notebook send-reply / reload / host escape / local remove / identity-history /390,1280');
})().catch(async e=>{fs.writeFileSync(path.join(out,'result.json'),JSON.stringify({pass:false,phase,error:e.message,checks,shots,errors,external,assets},null,2),{mode:0o600});if(browser)for(const [i,c]of browser.contexts().entries())if(c.pages()[0])await c.pages()[0].screenshot({path:path.join(out,'first-failure-'+i+'.png')}).catch(()=>{});console.error(phase+': '+e.message);process.exitCode=1}).finally(async()=>{if(browser)await browser.close()});
