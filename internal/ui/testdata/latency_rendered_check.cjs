const engines=require(process.env.AGENTNET_PLAYWRIGHT);
const fs=require('node:fs');
const {isWindowsMediaCandidate,classifyWindowsMedia,runMediaProbe}=require('./latency_media_probe.cjs');
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
   const url=new URL(process.env.LATENCY_URL);url.searchParams.set('token',process.env.LATENCY_TOKEN);url.searchParams.set('suite',suite);if(process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC)url.searchParams.set('paintDiagnostic',process.env.AGENTNET_LATENCY_PAINT_DIAGNOSTIC);if(process.env.AGENTNET_LATENCY_CHAT_DELAY)url.searchParams.set('chatLoadDelay',process.env.AGENTNET_LATENCY_CHAT_DELAY);if(process.env.AGENTNET_LATENCY_PEOPLE)url.searchParams.set('people',process.env.AGENTNET_LATENCY_PEOPLE);url.searchParams.set('platform',process.env.AGENTNET_LATENCY_HOST_PLATFORM||'daemon');if(teamsPart)url.searchParams.set('teamsPart',teamsPart);
   try{await page.goto(url.href);try{await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});}catch(e){console.log(JSON.stringify({engine:name,suite,timeout:String(e),progress:await page.evaluate(()=>window.latencyProgress)}));throw e;}
   const report=await page.evaluate(()=>window.latencyReport);report.requestedViewport={width,height,isMobile:touch&&name!=='firefox'};if(report.viewport?.width!==width)throw Error('Fixture layout viewport '+report.viewport?.width+' differs from requested '+width);
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
