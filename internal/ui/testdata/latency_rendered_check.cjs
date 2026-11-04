const engines=require(process.env.AGENTNET_PLAYWRIGHT);
const fs=require('node:fs');
const {isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe}=require('./latency_media_probe.cjs');
const trustedFiles=(process.env.AGENTNET_TRUSTED_INTERACTION_FILES||'').split(',').filter(Boolean);
const trustedInventory=process.env.AGENTNET_INTERACTION_INVENTORY?JSON.parse(fs.readFileSync(process.env.AGENTNET_INTERACTION_INVENTORY,'utf8')):null;
const trustedOnly=process.env.AGENTNET_TRUSTED_ONLY_ID||'';
const trustedBackend=process.env.AGENTNET_TRUSTED_BACKEND||'';
const trustedControls=(trustedInventory?.controls||[]).filter(r=>r.population==='comic-react'&&trustedFiles.some(f=>r.source_ref.path.endsWith('/'+f))&&(!trustedOnly||r.definition_id===trustedOnly));
const trustedByID=new Map(trustedControls.map(r=>[r.definition_id,r]));
const esc=value=>value.replace(/\\/g,'\\\\').replace(/"/g,'\\"');
async function trustedControlIDs(page){
 return page.evaluate(files=>{const root=document.querySelector('#fixture')?.shadowRoot;const shown=el=>{if(!el||!el.getClientRects().length)return false;for(let p=el;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(s.display==='none'||s.visibility==='hidden'||s.opacity==='0')return false;}return true;};return [...root.querySelectorAll('[data-an-control]')].filter(el=>shown(el)&&files.some(file=>el.getAttribute('data-an-control').includes('/'+file+'#'))).map(el=>el.getAttribute('data-an-control'));},trustedFiles);
}
function intentFor(row,tag){
 const h=(row?.handlers||[]).join(',');
 if(/onSubmit/.test(h))return {class:'mutates',target:'named form submit result',input:'submit'};
 if(/onKeyDown/.test(h))return {class:'types',target:'focused control key response',input:'keyboard'};
 if(['input','textarea'].includes(tag)||/onChange|onSelect/.test(h))return {class:'types',target:'control value',input:'keyboard'};
 if(tag==='summary'||/onToggle|Switch|Radio/.test(h+row?.element))return {class:'toggles',target:'expanded/checked state',input:''};
 if(/Menu\.Trigger|Popover\.Trigger|onOpenChange/.test(row?.element+' '+h))return {class:'opens',target:'named menu or dialog',input:''};
 if(/onClick|onPointer/.test(h)||tag==='button')return {class:'opens',target:'named local panel/menu/dialog',input:''};
 return {class:'read-only',target:'focused rendered control',input:''};
}
async function oracleStart(page,control_id,intent){
 return page.evaluate(({id,intent})=>{const root=document.querySelector('#fixture').shadowRoot,el=root.querySelector(`[data-an-control="${CSS.escape(id)}"]`);const visible=e=>!!e&&!!e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden'&&getComputedStyle(e).display!=='none';const state=()=>({value:el?.value??'',open:!!el?.open,expanded:el?.getAttribute('aria-expanded')||'',pressed:el?.getAttribute('aria-pressed')||'',checked:el?.getAttribute('aria-checked')||'',dialogs:[...root.querySelectorAll('[role=dialog],[role=alertdialog],[role=menu],[data-open]')].filter(visible).map(e=>e.getAttribute('aria-label')||e.getAttribute('role')||e.textContent?.slice(0,80)||'').sort(),focused:el?.contains(root.activeElement)||root.activeElement===el,requests:(window.__anTrustedApiCalls||[]).length});const before=state();window.__anIntentOracle=new Promise(resolve=>{const end=performance.now()+1000,check=()=>{const after=state();let pass=false;if(intent==='types')pass=after.value!==before.value;if(intent==='toggles')pass=after.open!==before.open||after.expanded!==before.expanded||after.pressed!==before.pressed||after.checked!==before.checked;if(intent==='opens')pass=JSON.stringify(after.dialogs)!==JSON.stringify(before.dialogs)||after.expanded!==before.expanded;if(intent==='mutates')pass=after.requests>before.requests&&(JSON.stringify(after.dialogs)!==JSON.stringify(before.dialogs)||after.value!==before.value);if(intent==='read-only')pass=after.focused&&!before.focused;if(pass)return resolve({pass:true,at:performance.now(),before,after});if(performance.now()>=end)return resolve({pass:false,at:null,before,after});requestAnimationFrame(check);};requestAnimationFrame(check);});return performance.now();},{id:control_id,intent:intent.class});
}
async function trustedInteraction(page,control_id,{suite,width,height,touch,row,state_path}){
 const results=[];
  const base={control_id,suite,viewport:`${width}x${height}`,state_path,backend:trustedBackend||'warm',intent_class:'unassigned',intent_target:'unassigned',input_kind:touch?'tap':'click'};
  try{
   const locator=page.locator(`[data-an-control="${esc(control_id)}"]`).first();
   if(!await locator.isVisible({timeout:250})) { results.push({...base,status:'unreachable',reason:'state path completed but no DOM binding rendered for this source row'});return results; }
   const tag=await locator.evaluate(el=>el.tagName.toLowerCase());
   const intent=intentFor(row,tag);base.intent_class=intent.class;base.intent_target=intent.target;const before=await oracleStart(page,control_id,intent);
   if(intent.input==='submit'){base.input_kind='keyboard';const field=locator.locator('input,textarea').first();if(await field.count())await field.press('Enter',{timeout:1000});else await locator.locator('button[type=submit]').first().click({timeout:1000});}
   else if(intent.input==='keyboard'||['input','textarea'].includes(tag)){base.input_kind='keyboard';await locator.click({timeout:1000});if(intent.input==='keyboard')await locator.press('ArrowDown');else {await locator.press('Control+A');await locator.type(' trusted');}}
   else if(touch)await locator.tap({timeout:1000});else await locator.click({timeout:1000});
   const asserted=await page.evaluate(()=>window.__anIntentOracle),after=await page.evaluate(id=>({fired:(window.__anFiredControlIds||window.latencyReport?.firedControlIds||[]).some(x=>x===id||x.startsWith(id+'#')),at:performance.now()}),control_id),elapsed=asserted.at===null?null:asserted.at-before,within_bound=elapsed!==null&&elapsed<1000;
   const reason=!after.fired?'trusted input did not fire tagged handler':!asserted.pass?'intent oracle did not observe '+intent.target:!within_bound?'intent assertion exceeded 1000 ms':'intent assertion passed';
   results.push({...base,fired_trusted:after.fired,change_asserted:asserted.pass,assertion_kind:intent.class,within_bound,input_to_visible_local_change_ms:elapsed,observed_visible_change:asserted,status:after.fired&&asserted.pass&&within_bound?'executed-pass':'executed-fail',reason,fixture_only:true,native_db_sync_contention:'not_run'});
  }catch(error){results.push({...base,status:'executed-fail',reason:String(error.message||error),fired_trusted:false,change_asserted:false,within_bound:false,input_to_visible_local_change_ms:null,observed_visible_change:null,fixture_only:true,native_db_sync_contention:'not_run'});}
 return results;
}
(async()=>{
 const failures=[],qualificationCounts={executed_pass:0,failed:0,not_executed:0,diagnostic:0};
 for(const name of (process.env.AGENTNET_LATENCY_ENGINES||'chromium').split(',')){
  if(!engines[name])throw Error('Unknown Playwright engine '+name);
  const executablePath=process.env.AGENTNET_CHROMIUM||(!process.env.AGENTNET_LATENCY_CHANNEL&&process.platform==='linux'&&fs.existsSync('/usr/bin/chromium')?'/usr/bin/chromium':undefined);
  const browser=await engines[name].launch(name==='chromium'?{executablePath,channel:process.env.AGENTNET_LATENCY_CHANNEL,headless:true,args:['--no-sandbox']}:{headless:true});
  let mediaProbe; // One independent control pair per failing engine, reused across widths.
  try{for(const suite of (process.env.AGENTNET_LATENCY_SUITES||'history,navigation,settings,unread,actions').split(',')){
   const sizes=(process.env.AGENTNET_LATENCY_VIEWPORTS||'1440x960').split(',');
   if(['topics','compose','storage'].includes(suite))sizes.push('320x592');
   if(['topics','compose'].includes(suite))sizes.push('360x640');
   for(const size of new Set(sizes)){
   for(const teamsPart of suite==='teams'?['list','group']:['']){
   const [width,height]=size.split('x').map(Number);if(!(width>0&&height>0))throw Error('Invalid fixture viewport');
   const touch=process.env.AGENTNET_LATENCY_TOUCH==='1';
   const context=await browser.newContext({viewport:{width,height},hasTouch:touch,...(touch&&name!=='firefox'?{isMobile:true}:{})}),page=await context.newPage(),errors=[];
   let trace;if(process.env.AGENTNET_LATENCY_PROFILE){if(name!=='chromium')throw Error('CDP profiling requires Chromium');trace=await context.newCDPSession(page);await trace.send('Profiler.enable');await trace.send('Profiler.start');await trace.send('Tracing.start',{categories:'devtools.timeline,v8,blink.user_timing',transferMode:'ReturnAsStream'});}
   page.on('pageerror',e=>errors.push(String(e)));
   const url=new URL(process.env.LATENCY_URL);url.searchParams.set('token',process.env.LATENCY_TOKEN);url.searchParams.set('suite',suite);if(trustedFiles.length){url.searchParams.set('trusted','1');url.searchParams.set('trustedFile',trustedFiles[0]);if(trustedBackend)url.searchParams.set('trustedBackend',trustedBackend);}if(process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC)url.searchParams.set('paintDiagnostic',process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC);if(process.env.AGENTNET_LATENCY_CHAT_DELAY)url.searchParams.set('chatLoadDelay',process.env.AGENTNET_LATENCY_CHAT_DELAY);if(process.env.AGENTNET_LATENCY_PEOPLE)url.searchParams.set('people',process.env.AGENTNET_LATENCY_PEOPLE);url.searchParams.set('platform',process.env.AGENTNET_LATENCY_HOST_PLATFORM||'daemon');if(teamsPart)url.searchParams.set('teamsPart',teamsPart);
   try{let trustedRows=[],report;const planned=trustedControls.length?trustedControls:[];if(trustedFiles.length&&planned.length){for(const row of planned){url.searchParams.set('trustedID',row.definition_id);await page.goto(url.href);await page.waitForFunction(()=>window.__agentnetTrustedCheckpoint||window.latencyReport,null,{timeout:20000});if(!await page.evaluate(()=>!!window.__agentnetTrustedCheckpoint)){trustedRows.push({control_id:row.definition_id,suite,viewport:`${width}x${height}`,status:'unreachable',reason:'fixture route did not expose a trusted state checkpoint',state_path:'unavailable',intent_class:'unassigned',intent_target:'unassigned',fixture_only:true,native_db_sync_contention:'not_run'});report=await page.evaluate(()=>window.latencyReport);continue;}const checkpoint=await page.evaluate(()=>window.__agentnetTrustedCheckpoint);trustedRows.push(...await trustedInteraction(page,row.definition_id,{suite,width,height,touch,row,state_path:checkpoint.state_path}));await page.evaluate(rows=>{window.__anTrustedInteractions=rows;window.__agentnetTrustedResume?.();},trustedRows);await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});report=await page.evaluate(()=>window.latencyReport);}}else{await page.goto(url.href);await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});report=await page.evaluate(()=>window.latencyReport);if(trustedFiles.length){for(const id of await trustedControlIDs(page))trustedRows.push(...await trustedInteraction(page,id,{suite,width,height,touch,row:trustedByID.get(id),state_path:'completed-fixture'}));}}try{if(!report)report=await page.evaluate(()=>window.latencyReport); }catch(e){console.log(JSON.stringify({engine:name,suite,timeout:String(e),progress:await page.evaluate(()=>window.latencyProgress)}));throw e;}
   report.requestedViewport={width,height,isMobile:touch&&name!=='firefox'};if(report.viewport?.width!==width)throw Error('Fixture layout viewport '+report.viewport?.width+' differs from requested '+width);
   report.trusted_interactions=trustedRows;
   if(trace){const {profile}=await trace.send('Profiler.stop');fs.writeFileSync(process.env.AGENTNET_LATENCY_PROFILE.replace(/\.json$/,`-${suite}-${width}${teamsPart?'-'+teamsPart:''}-cpu.json`),JSON.stringify(profile));const finished=new Promise(resolve=>trace.once('Tracing.tracingComplete',resolve));await trace.send('Tracing.end');const {stream}=await finished;let data='';for(;;){const chunk=await trace.send('IO.read',{handle:stream});data+=chunk.data;if(chunk.eof)break;}await trace.send('IO.close',{handle:stream});fs.writeFileSync(process.env.AGENTNET_LATENCY_PROFILE.replace(/\.json$/,`-${suite}-${width}${teamsPart?'-'+teamsPart:''}.json`),data);}
   if(process.env.LATENCY_EVIDENCE)await page.screenshot({path:process.env.LATENCY_EVIDENCE.replace(/\.png$/,`-${name}-${suite}-${width}${teamsPart?'-'+teamsPart:''}.png`)});
   if(!report.ok){report.failureDOM=await page.evaluate(()=>document.querySelector('#fixture')?.shadowRoot?.innerHTML.slice(-16000));report.failureScroll=await page.evaluate(()=>({x:scrollX,y:scrollY,body:document.body.getBoundingClientRect().toJSON(),scrolling:document.scrollingElement?.scrollTop}));report.failureGeometry=await page.evaluate(()=>[...document.querySelector('#fixture').shadowRoot.querySelectorAll('h2,h3,button,input')].filter(e=>e.getClientRects().length).map(e=>({tag:e.tagName,label:e.getAttribute('aria-label')||e.textContent,disabled:e.disabled,top:e.getBoundingClientRect().top,bottom:e.getBoundingClientRect().bottom})).filter(e=>/Choose your agents|Check the changes|Review changes|Apply changes|Back|Set up Codex|Your agents|Connect an agent|Change default setting/.test(e.label)));}
   const candidate={platform:process.platform,engine:name,suite,report,driverErrors:errors};
   if(isWindowsMediaCandidate(candidate)&&!mediaProbe){try{mediaProbe=await runMediaProbe(browser);}catch(e){mediaProbe={probeError:String(e.stack||e)};}}
   const unsupported=classifyWindowsMedia({...candidate,probe:mediaProbe});
   const execution=unsupported?'not_executed':report.ok&&!errors.length&&!report.pageErrors?.length?(report.paintDiagnostic?'diagnostic':'executed_pass'):'failed';qualificationCounts[execution]++;
   console.log(JSON.stringify({engine:name,engineVersion:browser.version(),runnerOS:process.platform,...report,driverPageErrors:errors,execution,...(mediaProbe?{independentMediaProbe:mediaProbe}:{}),...(unsupported?{unsupportedTestPort:unsupported}:{})}));
   if(execution==='failed')failures.push(name+'/'+suite+'/'+width+(teamsPart?'/'+teamsPart:'')+': '+(report.error||[...(report.pageErrors||[]),...errors].join(';')));
   }catch(e){qualificationCounts.failed++;failures.push(name+'/'+suite+'/'+width+(teamsPart?'/'+teamsPart:'')+': '+e.message);console.log(JSON.stringify({engine:name,suite,viewport:{width,height},ok:false,execution:'failed',driverError:String(e),progress:await page.evaluate(()=>window.latencyProgress).catch(()=>null)}));}finally{await context.close();}
   }
  }}}finally{await browser.close();}
 }
 console.log(JSON.stringify({qualificationSummary:qualificationCounts,countedUnit:'engine/suite/viewport/page partition; functional guard outcomes, not per-control smoothness. not_executed cells retain ok:false and partial actions only.'}));
 if(failures.length)throw Error('Responsiveness fixture failures: '+failures.join('\n'));
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
