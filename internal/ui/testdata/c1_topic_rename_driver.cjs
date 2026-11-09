// Prepare is source-only. Run refuses to launch outside its task-owned unit.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http'),assert=require('node:assert/strict');
const {execFileSync}=require('node:child_process');
const {entry,snapshot,bindings}=require('./c1_topic_rename.cjs');
const {caseSpec,verify,faults}=require('./c1_topic_rename_verify.cjs');
const {measure,finishMeasurement,capturePresented,digest}=require('./c1_oracle.cjs');
const repo=path.resolve(__dirname,'../../..'),web=path.join(repo,'internal/ui/web');
const runners=['c1_topic_rename_driver.cjs','c1_topic_rename_verify.cjs','c1_topic_rename.cjs','c1_topic_rename_fixture.cjs','c1_oracle.cjs','interaction_inventory.cjs'];
const git=(...a)=>execFileSync('git',a,{cwd:repo,encoding:'utf8',timeout:5000}).trim();
function privateDir(out){assert(out&&path.isAbsolute(out)&&!out.startsWith(repo+path.sep),'new absolute private output outside repo required');process.umask(0o077);fs.mkdirSync(out,{mode:0o700});}
function sourceInputs(dir,base){return fs.readdirSync(dir,{withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name)).flatMap(e=>{const p=path.join(dir,e.name);return e.isDirectory()?sourceInputs(p,base):[{path:path.relative(base,p),sha256:digest(fs.readFileSync(p))}];});}
async function prepare(out,options){
 assert.deepEqual(Object.keys(options).sort(),['action','completion','history','negative','placement','width'],'rename prepare accepts only explicit consumed options');
 const spec=caseSpec(options);assert(spec.negative===null,'prepare the unchanged positive case; negatives selected only at run');privateDir(out);
 const work=path.join(out,'source');fs.mkdirSync(work);fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
 execFileSync(process.execPath,[path.join(__dirname,'interaction_inventory.cjs'),'--tag',path.join(work,'src')],{timeout:15000,stdio:'pipe'});
 const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
 for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
 fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
 const generated=entry({placement:spec.placement,mode:'held',history:spec.history});fs.writeFileSync(path.join(out,'fixture.tsx'),generated);
 const built=require(path.join(web,'node_modules/esbuild')).buildSync({absWorkingDir:work,stdin:{contents:generated,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false,metafile:true});
 fs.writeFileSync(path.join(out,'bundle.mjs'),built.outputFiles[0].contents);fs.writeFileSync(path.join(out,'metafile.json'),JSON.stringify(built.metafile,null,2));
 execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(out,'style.css'),'--minify'],{cwd:work,timeout:15000,stdio:'pipe'});
 // Embed only installed public font assets. No browser request may leave origin.
 const css=fs.readFileSync(path.join(out,'style.css'),'utf8').replace(/url\((['"]?)([^)'"\s]+)\1\)/g,(whole,q,url)=>{
  if(url.startsWith('data:')||url.startsWith('#'))return whole;
  const name=path.basename(url),dirs=['onest','rubik'].map(f=>path.join(web,'node_modules/@fontsource-variable',f,'files'));
  assert(/^[a-z0-9.-]+\.woff2$/i.test(name),'unreviewed CSS asset '+url);const matches=dirs.map(d=>path.join(d,name)).filter(fs.existsSync);assert.equal(matches.length,1,'font asset unresolved '+url);
  return 'url(data:font/woff2;base64,'+fs.readFileSync(matches[0]).toString('base64')+')';
 });fs.writeFileSync(path.join(out,'style.css'),css);
 const browser=fs.realpathSync(process.env.AGENTNET_CHROMIUM||'/usr/lib/chromium/chromium');
 const playwright=process.env.AGENTNET_PLAYWRIGHT;assert(playwright&&path.isAbsolute(playwright),'explicit installed Playwright package required for binding');
 const inputs=sourceInputs(path.join(work,'src'),work);fs.writeFileSync(path.join(out,'source-inputs.json'),JSON.stringify(inputs,null,2));
 const binding={revision:git('rev-parse','HEAD'),tree:git('rev-parse','HEAD^{tree}'),dirty:!!git('status','--porcelain=v1'),source_digest:digest(JSON.stringify(inputs)),
  fixture_sha256:digest(generated),bundle_sha256:digest(built.outputFiles[0].contents),css_sha256:digest(css),browser_path:browser,browser_sha256:digest(fs.readFileSync(browser)),
  runner_inputs:runners.map(p=>({path:p,sha256:digest(fs.readFileSync(path.join(__dirname,p)))})),lock_sha256:digest(fs.readFileSync(path.join(web,'package-lock.json'))),
  node:process.version,playwright_path:fs.realpathSync(playwright),playwright_package_sha256:digest(fs.readFileSync(path.join(playwright,'package.json')))};
 binding.runner_sha256=digest(JSON.stringify(binding.runner_inputs));
 const plan={schema:'agentnet-c1-rename-prepared/v1',case:spec,binding,required_case_ids:[spec.case_id],family_complete:false,release_qualified:false};
 fs.writeFileSync(path.join(out,'plan.json'),JSON.stringify(plan,null,2)+'\n');return plan;
}
function loadPlan(prepared,negative){
 const p=JSON.parse(fs.readFileSync(path.join(prepared,'plan.json')));assert(p.schema==='agentnet-c1-rename-prepared/v1');assert.deepEqual(caseSpec(p.case),p.case,'prepared case differs from owning source definition');
 assert.equal(digest(fs.readFileSync(path.join(prepared,'bundle.mjs'))),p.binding.bundle_sha256);assert.equal(digest(fs.readFileSync(path.join(prepared,'style.css'))),p.binding.css_sha256);
 assert.equal(digest(fs.readFileSync(p.binding.browser_path)),p.binding.browser_sha256);
 assert.equal(process.version,p.binding.node,'Node changed after preparation');assert.equal(fs.realpathSync(process.env.AGENTNET_PLAYWRIGHT),p.binding.playwright_path);
 assert.equal(digest(fs.readFileSync(path.join(p.binding.playwright_path,'package.json'))),p.binding.playwright_package_sha256);
 for(const f of p.binding.runner_inputs)assert.equal(digest(fs.readFileSync(path.join(__dirname,f.path))),f.sha256,'runner changed after preparation: '+f.path);
 assert.equal(digest(JSON.stringify(sourceInputs(path.join(prepared,'source/src'),path.join(prepared,'source')))),p.binding.source_digest);
 return{...p,case:caseSpec({...p.case,negative})};
}
function pin(pid){const f=fs.readFileSync('/proc/'+pid+'/stat','utf8').split(') ')[1].split(' ');return{pid,start_ticks:f[19],uid:fs.statSync('/proc/'+pid).uid,exe:fs.realpathSync('/proc/'+pid+'/exe'),cgroup:fs.readFileSync('/proc/'+pid+'/cgroup','utf8').trim().split('0::')[1]};}
async function run(prepared,out,negative){
 const unit=process.env.C1_BOUND_UNIT,cgroup=fs.readFileSync('/proc/self/cgroup','utf8').trim().split('0::')[1];
 assert(/^agr-c1-[a-z0-9-]+\.service$/.test(unit||'')&&cgroup.endsWith('/'+unit),'refuse browser outside exact task-owned unit');
 const plan=loadPlan(prepared,negative),spec=plan.case;privateDir(out);fs.writeFileSync(path.join(out,'manifest.json'),JSON.stringify(plan,null,2));
 const report={case_id:spec.case_id,negative,prepared_binding:plan.binding,status:'failed',terminal_status:'failed',phase:'bootstrap',errors:[],browser_errors:[],cleanup:{browser_closed:false,server_closed:false}};
 const assets=new Map([['/bundle.mjs',['text/javascript','bundle.mjs']],['/style.css',['text/css','style.css']]]);
 const server=http.createServer((req,res)=>{const asset=assets.get(req.url);if(asset){res.setHeader('Content-Type',asset[0]);res.end(fs.readFileSync(path.join(prepared,asset[1])));return;}if(req.url!=='/'){res.statusCode=404;res.end('No task route');return;}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/bundle.mjs"></script>');});
 let browser,page,primary,other;
 try{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));
  const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);browser=await chromium.launch({headless:true,executablePath:plan.binding.browser_path});
  const cdp=await browser.newBrowserCDPSession(),info=(await cdp.send('SystemInfo.getProcessInfo')).processInfo.find(p=>p.type==='browser');assert(info&&Number.isSafeInteger(info.id));
  const actual=fs.realpathSync('/proc/'+info.id+'/exe');report.browser={pid:info.id,path:actual,sha256:digest(fs.readFileSync(actual)),version:browser.version()};
  fs.writeFileSync(path.join(out,'processes.json'),JSON.stringify({unit,cgroup,pins:[pin(process.pid),pin(info.id)],server_port:server.address().port},null,2));await cdp.detach();
  assert.equal(report.browser.sha256,plan.binding.browser_sha256,'actual browser differs from prepared binary');
  page=await browser.newPage({viewport:{width:spec.width,height:960},reducedMotion:'reduce'});page.setDefaultTimeout(4000);
  page.on('pageerror',e=>report.browser_errors.push(String(e)));page.on('console',m=>{if(m.type()==='error')report.browser_errors.push(m.text());});
  const origin='http://127.0.0.1:'+server.address().port;await page.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
  await page.goto(origin+'/');await page.waitForFunction(()=>globalThis.__c1RenameMounted===true);await page.evaluate(()=>document.fonts.ready);
  const menu=page.locator('[data-an-control='+JSON.stringify(spec.menu_definition_id)+']');await menu.waitFor({state:'visible'});assert((await menu.innerText()).includes('Original renamed topic'));
  await menu.click();await page.getByRole('menuitem',{name:'Rename…',exact:true}).click();
  const dialog=page.getByRole('dialog',{name:'Rename topic',exact:true}),name=dialog.locator('#rename-topic-name');await name.waitFor({state:'visible'});
  assert.equal(await name.getAttribute('data-an-control'),bindings.name);
  if(spec.action!=='reset')await name.fill(spec.title); // Trusted setup; not a measured name-edit result.
  const initial=await page.evaluate(()=>fixture.backend.witness());assert.equal(initial.requests.length,0);assert.equal(initial.unexpected.length,0);assert.deepEqual(report.browser_errors,[]);
  report.bootstrap={complete:true,menu_definition_id:await menu.getAttribute('data-an-control'),unexpected:initial.unexpected,name_definition_id:await name.getAttribute('data-an-control'),setup_name_fill_measured:false,
   actual:await page.evaluate(()=>({width:innerWidth,height:innerHeight,state:fixture.state()}))};
  report.phase='observation';
  if(negative&&negative!=='wrong-target')await page.evaluate(({kind,handler})=>{fixture.fault={kind,handler};},{kind:negative,handler:negative==='independent-noop'?spec.other_handlers[0]:spec.handlers[0]});
  const target=negative==='wrong-target'?dialog.getByRole('button',{name:'Close',exact:true}):spec.action==='reset'?dialog.getByRole('button',{name:'Use automatic title for everyone',exact:true}):spec.action==='save-enter'?name:dialog.getByRole('button',{name:'Save',exact:true});
  const fn=s=>new Function('return ('+s+')')();
  primary=await measure(page,target,{snapshot,local:fn(spec.expected.local),confirmed:fn(spec.expected.confirmed),target_definition_id:spec.target_definition_id,handlers:spec.handlers},t=>spec.action==='save-enter'&&negative!=='wrong-target'?t.press('Enter'):t.click());
  await capturePresented(page,path.join(out,'feedback-0.png'),snapshot,primary);
  report.primary=primary;
  other=await measure(page,page.locator('form[aria-label="Write a message"] textarea'),{snapshot,local:fn(spec.other_expected.local),confirmed:fn(spec.other_expected.confirmed),target_definition_id:spec.other_definition_id,handlers:spec.other_handlers},t=>t.fill('Independent rename text'));
  await capturePresented(page,path.join(out,'feedback-0-other.png'),snapshot,other);await finishMeasurement(page,other);
  report.held_backend=await page.evaluate(()=>fixture.backend.witness());assert.equal(report.held_backend.pending,1);
  await page.evaluate(kind=>fixture.backend.settle(1,kind),spec.completion);await finishMeasurement(page,primary);
  report.primary=primary;report.other=other;report.final_backend=await page.evaluate(()=>fixture.backend.witness());
  assert.deepEqual(report.browser_errors,[]);report.phase='verification';report.status='passed';
 }catch(e){
  report.failure_phase=report.phase;report.error=e.message;report.errors.push(e.message);
  if(page)try{report.failure=await page.evaluate(()=>{const observations=[...(globalThis.__c1Observations?.values()||[])].map(o=>({before:o.before,input:o.input,target_definition_id:o.target_definition_id,error:o.error,feedback:o.feedback,confirmed:o.confirmed}));return{primary:observations[0]||null,other:observations[1]||null,fault_hits:globalThis.fixture?.fault_hits||[],backend:globalThis.fixture?.backend.witness()||null};});}catch(error){report.errors.push('failure evidence: '+error.message);}
 }finally{
  try{if(browser){await browser.close();report.cleanup.browser_closed=true;}}catch(e){report.errors.push('browser cleanup: '+e.message);report.status='failed';}
  try{await new Promise((r,j)=>server.close(e=>e?j(e):r()));report.cleanup.server_closed=true;}catch(e){report.errors.push('server cleanup: '+e.message);report.status='failed';}
 }
 try{report.summary=verify(plan,report,p=>fs.readFileSync(path.join(out,p)));report.verifier={accepted:true,accepted_negative:!!report.summary.accepted_negative};}
 catch(e){report.errors.push('verifier: '+e.message);report.verifier={accepted:false,error:e.message};report.status='failed';}
 report.terminal_status=report.status;fs.writeFileSync(path.join(out,'report.json'),JSON.stringify(report,null,2)+'\n');
 if(report.status!=='passed')throw Error(report.error||report.errors.join('; '));return report.summary;
}
module.exports={prepare,loadPlan,run};
if(require.main===module){
 const [mode,first,...rest]=process.argv.slice(2);
 (async()=>{if(mode==='--prepare'){assert(rest.length===1,'usage: --prepare new-output case-json');console.log(JSON.stringify(await prepare(first,JSON.parse(rest[0]))));}
 else if(mode==='--run'){assert(rest.length===2&&rest[0]==='--negative','usage: --run prepared --negative none|fault');console.log(JSON.stringify(await run(first,process.env.C1_OUT,rest[1]==='none'?null:rest[1])));}
 else throw Error('explicit --prepare or bounded --run required');})().catch(e=>{console.error(e.stack);process.exitCode=1;});
}
