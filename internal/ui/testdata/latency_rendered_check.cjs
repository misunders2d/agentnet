const engines=require(process.env.AGENTNET_PLAYWRIGHT);
const fs=require('node:fs');
const {isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe}=require('./latency_media_probe.cjs');
const trustedFiles=(process.env.AGENTNET_TRUSTED_INTERACTION_FILES||'').split(',').filter(Boolean);
const esc=value=>value.replace(/\\/g,'\\\\').replace(/"/g,'\\"');
async function trustedControlIDs(page){
 return page.evaluate(files=>{const root=document.querySelector('#fixture')?.shadowRoot;const shown=el=>{if(!el||!el.getClientRects().length)return false;for(let p=el;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(s.display==='none'||s.visibility==='hidden'||s.opacity==='0')return false;}return true;};return [...root.querySelectorAll('[data-an-control]')].filter(el=>shown(el)&&files.some(file=>el.getAttribute('data-an-control').includes('/'+file+'#'))).map(el=>el.getAttribute('data-an-control'));},trustedFiles);
}
async function trustedInteraction(page,control_id,{suite,width,height,touch}){
 const results=[];
  const base={control_id,suite,viewport:`${width}x${height}`,input_kind:touch?'tap':'click'};
  try{
   const locator=page.locator(`[data-an-control="${esc(control_id)}"]`).first();
   if(!await locator.isVisible({timeout:250})) { results.push({...base,status:'not_run',reason:'control disappeared before trusted input'});return results; }
   const tag=await locator.evaluate(el=>el.tagName.toLowerCase());
   const before=await page.evaluate(()=>{const root=document.querySelector('#fixture').shadowRoot;const digest=()=>[...root.querySelectorAll('button,input,textarea,select,summary,a[href],[role],[tabindex]')].filter(el=>el.getClientRects().length&&getComputedStyle(el).visibility!=='hidden'&&getComputedStyle(el).display!=='none').map(el=>`${el.tagName}:${el.getAttribute('aria-label')||el.textContent||''}:${el.getAttribute('aria-expanded')||''}:${el.getAttribute('aria-pressed')||''}:${el.value||''}:${el.open||''}`).join('\n');window.__anTrustedBefore=digest();window.__anTrustedChange=new Promise(resolve=>{const observer=new MutationObserver(()=>{const after=digest();if(after!==window.__anTrustedBefore){observer.disconnect();resolve({changed:true,before:window.__anTrustedBefore,after});}});observer.observe(root,{subtree:true,childList:true,characterData:true,attributes:true});setTimeout(()=>{observer.disconnect();resolve({changed:false,before:window.__anTrustedBefore,after:digest()});},1000);});return performance.now();});
   if(['input','textarea'].includes(tag)){base.input_kind='keyboard';await locator.click({timeout:1000});await locator.press('Control+A');await locator.type(' trusted');}
   else if(touch)await locator.tap({timeout:1000});else await locator.click({timeout:1000});
   const changed=await page.evaluate(()=>window.__anTrustedChange),after=await page.evaluate(id=>({fired:(window.__anFiredControlIds||window.latencyReport?.firedControlIds||[]).some(x=>x===id||x.startsWith(id+'#')),at:performance.now()}),control_id),elapsed=after.at-before;
   const within_bound=changed.changed&&elapsed<1000;
   const reason=!after.fired?'trusted input did not fire tagged handler':!changed.changed?'no visible local change observed':!within_bound?'visible local change exceeded 1000 ms':'visible interactive state changed';
   results.push({...base,fired_trusted:after.fired,change_asserted:changed.changed,assertion_kind:'visible-interactive-dom-diff',within_bound,input_to_visible_local_change_ms:changed.changed?elapsed:null,observed_visible_change:changed.changed?{before:changed.before.slice(0,600),after:changed.after.slice(0,600)}:null,status:after.fired&&changed.changed&&within_bound?'executed':'failed',reason});
  }catch(error){results.push({...base,status:'failed',reason:String(error.message||error),fired_trusted:false,change_asserted:false,within_bound:false,input_to_visible_local_change_ms:null,observed_visible_change:null});}
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
   const url=new URL(process.env.LATENCY_URL);url.searchParams.set('token',process.env.LATENCY_TOKEN);url.searchParams.set('suite',suite);if(trustedFiles.length){url.searchParams.set('trusted','1');url.searchParams.set('trustedFile',trustedFiles[0]);}if(process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC)url.searchParams.set('paintDiagnostic',process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC);if(process.env.AGENTNET_LATENCY_CHAT_DELAY)url.searchParams.set('chatLoadDelay',process.env.AGENTNET_LATENCY_CHAT_DELAY);if(process.env.AGENTNET_LATENCY_PEOPLE)url.searchParams.set('people',process.env.AGENTNET_LATENCY_PEOPLE);url.searchParams.set('platform',process.env.AGENTNET_LATENCY_HOST_PLATFORM||'daemon');if(teamsPart)url.searchParams.set('teamsPart',teamsPart);
   try{await page.goto(url.href);let trustedRows=[];if(trustedFiles.length){await page.waitForFunction(()=>window.__agentnetTrustedCheckpoint||window.latencyReport,null,{timeout:20000});if(await page.evaluate(()=>!!window.__agentnetTrustedCheckpoint)){const ids=[...new Set(await trustedControlIDs(page))];for(let i=0;i<ids.length;i++){const controlID=ids[i];trustedRows.push(...await trustedInteraction(page,controlID,{suite,width,height,touch}));await page.evaluate(rows=>{window.__anTrustedInteractions=rows;window.__agentnetTrustedResume?.();},trustedRows);await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});if(i+1<ids.length){await page.goto(url.href);await page.waitForFunction(()=>window.__agentnetTrustedCheckpoint||window.latencyReport,null,{timeout:20000});if(!await page.evaluate(()=>!!window.__agentnetTrustedCheckpoint))throw Error('trusted fixture checkpoint disappeared');}}}}try{await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});}catch(e){console.log(JSON.stringify({engine:name,suite,timeout:String(e),progress:await page.evaluate(()=>window.latencyProgress)}));throw e;}
   const report=await page.evaluate(()=>window.latencyReport);report.requestedViewport={width,height,isMobile:touch&&name!=='firefox'};if(report.viewport?.width!==width)throw Error('Fixture layout viewport '+report.viewport?.width+' differs from requested '+width);
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
