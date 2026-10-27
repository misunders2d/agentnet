// Render actual native/browser views captured by the isolated signed journey.
// No live identity, network endpoint or harness is accessible to this fixture.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const settle=async page=>page.evaluate(async()=>{await document.fonts.ready;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));await Promise.all(document.querySelector('#skin').shadowRoot.querySelector('.skin-root').getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
const snapshots=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
const root=path.resolve(__dirname,'../static'),evidence=process.env.AGENTNET_SCREENSHOTS;
fs.mkdirSync(evidence,{recursive:true,mode:0o700});
const boot=`
const views=${JSON.stringify(snapshots)};
const mode=new URL(location.href).searchParams.get('mode');
window.fixture={...structuredClone(views[mode+'-pending']),requests:[]};
let open,changed,seq=fixture.overview.seq;
const host={version:1,platform:fixture.platform,workspace:{id:'default',name:'Own invitation fixture',endpoint:location.origin,address:fixture.overview.me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{}},onOpen(fn){open=fn},listen(fn){changed=fn;return()=>{}},stage:async()=>{throw Error('no files')},file:async()=>{throw Error('no files')},api:async(p,body)=>{
fixture.requests.push({path:p,body});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p.startsWith('/api/dm?'))return structuredClone(fixture.thread);
if(p.startsWith('/api/approvals'))return {needs_you:[],held:[],remote:[],review:[],total:0};
if(p.startsWith('/api/agents'))return {host:fixture.overview.me.address,local:fixture.platform==='daemon',agents:[],sessions:[]};
if(p==='/api/groups/invitations')return [];
if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};
if(p.includes('/topics'))return {topics:[],placements:[]};
if(p==='/api/permissions')return {questions:[],tasks:[]};
if(body!==undefined&&p!=='/api/refresh')throw Error('Unexpected write '+p);
return {};
}};
const skin=new URL(location.href).searchParams.get('skin'),base='/assets/skins/'+skin+'/';
const manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const node=document.createElement('div');node.className='skin-root';shadow.append(node);await(await import(base+manifest.entry)).mount(node,host);
window.openGroup=()=>open(fixture.thread.id,'conversation');
window.transition=state=>{const next=structuredClone(views[mode+'-'+state]);fixture.overview=next.overview;fixture.thread=next.thread;changed?.({type:'change',seq:++seq});};window.ready=true;
`;
const server=http.createServer((req,res)=>{
 const u=new URL(req.url,'http://127.0.0.1');
 if(u.pathname==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>');return;}
 if(u.pathname==='/boot.mjs'){res.setHeader('Content-Type','text/javascript');res.end(boot);return;}
 if(u.pathname.startsWith('/assets/')){const file=path.resolve(root,'.'+u.pathname.slice(7));if(file.startsWith(root+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile()){res.setHeader('Content-Type',file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');res.end(fs.readFileSync(file));return;}}
 res.statusCode=404;res.end('missing fixture route');
});
(async()=>{
 let browser;const errors=[],shots=[];
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
  browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
  for(const mode of ['native','browser'])for(const skin of ['comic'])for(const width of [1280,390]){
   console.log('Checking '+mode+' '+skin+' '+width);
   const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage();page.setDefaultTimeout(6000);
   await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());page.on('pageerror',e=>errors.push(e.stack));
   await page.goto(origin+'/?skin='+skin+'&mode='+mode);await page.waitForFunction(()=>window.ready);
   if(skin==='comic'){
    const oks=page.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^Review/});await oks.click();
    await page.getByText('No decisions waiting',{exact:true}).waitFor();
    assert.equal(await page.getByRole('button',{name:'Let it join',exact:true}).count(),0,'own pending invite absent from OKs');
    const shot=path.join(evidence,mode+'-'+skin+'-oks-'+width+'.png');await settle(page);await page.screenshot({path:shot});shots.push(shot);
   }
   await page.evaluate(()=>openGroup());
   const panel=page.getByRole('button',{name:'In this chat',exact:true});
   if(await panel.isVisible())await panel.click();else await page.getByRole('button',{name:/Who’s in this chat/}).click();
   const text=mode==='native'?/Enable this agent on this computer/:/Joining on /;
   await page.getByText(text).and(page.locator(':visible')).first().waitFor();
   const verify=async()=>{
    assert.equal(await page.getByRole('button',{name:/^(Let it join|Accept…|Decline…|Decide in chat)$/}).count(),0,'no second consent control');
    assert.equal(await page.getByText(/Waiting for (?:your|.*’s) OK|Needs your OK/).and(page.locator(':visible')).count(),0,'no false approval state');
    assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined&&r.path!=='/api/refresh').length),0,'render sends no approvals');
   };
   await verify();
   const shot=path.join(evidence,mode+'-'+skin+'-pending-'+width+'.png');await settle(page);await page.screenshot({path:shot});shots.push(shot);
   if(mode==='browser'){await page.evaluate(()=>transition('delayed'));await page.getByText(text).and(page.locator(':visible')).first().waitFor();await verify();}
   await page.evaluate(()=>transition('active'));
   await page.getByText(/Member of this group/).and(page.locator(':visible')).first().waitFor();
   await verify();
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),'no document overflow');
   await context.close();
  }
  assert.deepEqual(errors,[]);
  const keep=process.env.AGENTNET_RENDERED_RETAIN;if(keep){assert(path.resolve(keep).startsWith('/tmp/'));fs.mkdirSync(keep,{recursive:true,mode:0o700});for(const shot of shots)fs.copyFileSync(shot,path.join(keep,path.basename(shot)));}
  console.log(JSON.stringify({ok:true,checks:'Real signed native/browser views; offline/disabled joining, delayed and signed-active transitions; no approval controls/counts; Comic 1280/390',shots}));
 }finally{if(browser)await browser.close();server.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
