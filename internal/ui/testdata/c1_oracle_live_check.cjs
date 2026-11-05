// Live observer regression: local presentation and another control must not await confirmation.
// Delayed DOM result is synthetic; this is not application or release qualification.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process');
const {measure,finishMeasurement,capturePresented,checkMeasurement,digest}=require('./c1_oracle.cjs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const out=process.env.C1_LIVE_OUT||process.env.C1_OUT;
assert(out&&path.isAbsolute(out),'C1_LIVE_OUT must be a new private absolute directory');
process.umask(0o077);fs.mkdirSync(out,{mode:0o700});
function snapshot(){return{pending:document.querySelector('#status').textContent==='Pending',done:document.querySelector('#status').textContent==='Done',other:document.querySelector('#other').textContent==='Changed'}}
(async()=>{
  let browser,server;
  const report={release_qualified:false,errors:[],checks_passed:false};
  try {
    browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM});
    const page=await browser.newPage();
    const html=`<button data-an-control="primary" id="start">Start</button><p id="status">Idle</p><button data-an-control="other" id="other">Unchanged</button><script>
      globalThis.__c1Fired=[];
      document.querySelector('#start').onclick=()=>{__c1Fired.push({id:'primary-handler',at_ms:performance.now()});document.querySelector('#status').textContent='Pending';setTimeout(()=>document.querySelector('#status').textContent='Done',1200)};
      document.querySelector('#other').onclick=()=>{__c1Fired.push({id:'other-handler',at_ms:performance.now()});document.querySelector('#other').textContent='Changed'};
    </script>`;
    server=http.createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end(html)});
    await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
    await page.goto('http://127.0.0.1:'+server.address().port);
    const session=await browser.newBrowserCDPSession(),info=await session.send('SystemInfo.getProcessInfo');
    const browserPID=info.processInfo.find(p=>p.type==='browser').id;
    const cgroup=fs.readFileSync('/proc/self/cgroup','utf8').trim().split('0::')[1];
    const pin=pid=>{const stat=fs.readFileSync('/proc/'+pid+'/stat','utf8').split(') ')[1].split(' ');return{pid,start_ticks:stat[19],uid:fs.statSync('/proc/'+pid).uid,exe:fs.realpathSync('/proc/'+pid+'/exe'),cgroup:fs.readFileSync('/proc/'+pid+'/cgroup','utf8').trim().split('0::')[1]}};
    fs.writeFileSync(path.join(out,'processes.json'),JSON.stringify({unit:process.env.C1_BOUND_UNIT||null,cgroup,pins:[pin(process.pid),pin(browserPID)],server_port:server.address().port},null,2));
    report.binding={revision:execFileSync('git',['-C',path.resolve(__dirname,'../../..'),'rev-parse','HEAD'],{encoding:'utf8'}).trim(),
      source_sha256:digest(fs.readFileSync(__filename)),observer_sha256:digest(fs.readFileSync(path.join(__dirname,'c1_oracle.cjs'))),
      browser_sha256:digest(fs.readFileSync(fs.realpathSync('/proc/'+browserPID+'/exe'))),browser_version:browser.version(),fixture_sha256:digest(html)};
    await session.detach();
    const expected={local:'s=>s.pending',confirmed:'s=>s.done'};
    const first=await measure(page,page.locator('#start'),{snapshot,local:s=>s.pending,confirmed:s=>s.done,handlers:['primary-handler'],target_definition_id:'primary'},t=>t.click());
    await capturePresented(page,path.join(out,'feedback-0.png'),snapshot,first);
    assert.equal(await page.evaluate(()=>document.querySelector('#status').textContent),'Pending','local capture waited for confirmation');
    const other=await measure(page,page.locator('#other'),{snapshot,local:s=>s.other,confirmed:s=>s.other,handlers:['other-handler'],target_definition_id:'other'},t=>t.click());
    await capturePresented(page,path.join(out,'feedback-0-other.png'),snapshot,other);
    assert.equal(await page.evaluate(()=>document.querySelector('#status').textContent),'Pending','independent control waited for confirmation');
    await finishMeasurement(page,first);await finishMeasurement(page,other);
    checkMeasurement(first,['primary-handler'],expected,'primary');
    checkMeasurement(other,['other-handler'],{local:'s=>s.other',confirmed:'s=>s.other'},'other');
    assert(first.confirmed.at_ms-first.input.at_ms>=1100,'delayed confirmation not exercised');
    assert(first.presented.at_ms-first.input.at_ms<1000,'local presentation waited for result');
    assert(other.presented.at_ms-other.input.at_ms<1000,'independent presentation unbounded');
    Object.assign(report,{first,other,checks_passed:true});
    console.log(JSON.stringify({local_presented_upper_ms:first.presented.at_ms-first.input.at_ms,confirmed_ms:first.confirmed.at_ms-first.input.at_ms,other_presented_upper_ms:other.presented.at_ms-other.input.at_ms,scope:'live observer regression only'}));
  } catch(e) {report.errors.push(e.message);throw e;}
  finally {
    report.cleanup={browser_closed:false,server_closed:false};const errors=[];
    try{await browser?.close();report.cleanup.browser_closed=!browser||!browser.isConnected()}catch(e){errors.push(e.message)}
    try{if(server)await new Promise((resolve,reject)=>server.close(e=>e?reject(e):resolve()));report.cleanup.server_closed=true}catch(e){errors.push(e.message)}
    report.errors.push(...errors);report.terminal_status=report.errors.length?'failed':'passed';
    fs.writeFileSync(path.join(out,'report.json'),JSON.stringify(report,null,2));
    if(errors.length)throw Error(errors.join('; '));
  }
})().catch(e=>{console.error(e.stack);process.exitCode=1});
