const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:960}}),errors=[];
  page.on('pageerror',e=>errors.push(String(e)));
  const url=new URL(process.env.LATENCY_URL);url.searchParams.set('token',process.env.LATENCY_TOKEN);
  await page.goto(url.href);await page.waitForFunction(()=>window.latencyReport,null,{timeout:20000});
  const report=await page.evaluate(()=>window.latencyReport);await page.screenshot({path:process.env.LATENCY_EVIDENCE});
  console.log(JSON.stringify({engine:'Chromium',...report,pageErrors:errors}));
  if(!report.ok||errors.length)throw Error('Responsiveness fixture failed');
 }finally{await browser.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
