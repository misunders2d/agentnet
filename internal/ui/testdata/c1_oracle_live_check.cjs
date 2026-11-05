// Live observer regression: local presentation and another control must not await confirmation.
// Delayed DOM result is synthetic; this is not application or release qualification.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {measure,finishMeasurement,capturePresented,checkMeasurement}=require('./c1_oracle.cjs');
const {chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const out=process.env.C1_LIVE_OUT;
assert(out&&path.isAbsolute(out),'C1_LIVE_OUT must be a new private absolute directory');
process.umask(0o077);fs.mkdirSync(out,{mode:0o700});
function snapshot(){return{pending:document.querySelector('#status').textContent==='Pending',done:document.querySelector('#status').textContent==='Done',other:document.querySelector('#other').textContent==='Changed'}}
(async()=>{
  let browser;
  try {
    browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM});
    const page=await browser.newPage();
    await page.setContent(`<button data-an-control="primary" id="start">Start</button><p id="status">Idle</p><button data-an-control="other" id="other">Unchanged</button><script>
      globalThis.__c1Fired=[];
      document.querySelector('#start').onclick=()=>{__c1Fired.push({id:'primary-handler',at_ms:performance.now()});document.querySelector('#status').textContent='Pending';setTimeout(()=>document.querySelector('#status').textContent='Done',1200)};
      document.querySelector('#other').onclick=()=>{__c1Fired.push({id:'other-handler',at_ms:performance.now()});document.querySelector('#other').textContent='Changed'};
    </script>`);
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
    fs.writeFileSync(path.join(out,'report.json'),JSON.stringify({first,other,release_qualified:false},null,2));
    console.log(JSON.stringify({local_presented_upper_ms:first.presented.at_ms-first.input.at_ms,confirmed_ms:first.confirmed.at_ms-first.input.at_ms,other_presented_upper_ms:other.presented.at_ms-other.input.at_ms,scope:'live observer regression only'}));
  } finally { await browser?.close(); }
})().catch(e=>{console.error(e.stack);process.exitCode=1});
