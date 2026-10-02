// Isolated rendered regression: actual Settings markup, CSS and Drive setup module.
// No existing browser, Hub or Google account. Supply installed Playwright with
// AGENTNET_PLAYWRIGHT; optional AGENTNET_SCREENSHOTS stays outside the repo.
const fs = require('fs'), http = require('http'), path = require('path');
const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const base = path.resolve(__dirname, '../static');
const settings = fs.readFileSync(path.join(base, 'default.html'), 'utf8').match(/<dialog id="settings"[\s\S]*?<\/dialog>/)[0];
const checks = [], errors = [], external = [];
let writes = 0;
const bootstrap = `
import {browserStorageSetupProvider, mountFileStorageOptions} from '/drivespace-setup.mjs';
const config = {enabled:false,project:'agentnet-layout-fixture',desktop_client_id:'',browser_client_id:'',browser_origins:[]};
let draft = {config,cloud_account:'synthetic-admin@example.invalid',existing_project:false,completed:{}};
window.fixture = {requests:[],saves:0};
const provider = browserStorageSetupProvider({
  call:async (method,url) => {fixture.requests.push({method,url});if(method!=='GET'||url!=='/v1/storage/drive')throw Error('Unexpected provider request');return {can_admin:true,config,revision:0};},
  readDraft:async()=>draft, writeDraft:async value=>{draft=value;fixture.saves++;}
});
document.querySelector('#settings-profile').hidden=true;
document.querySelector('#settings-storage').hidden=false;
document.querySelector('#settings-tab-profile').setAttribute('aria-pressed','false');
document.querySelector('#settings-tab-storage').setAttribute('aria-pressed','true');
mountFileStorageOptions(document.querySelector('#file-storage'),{provider});
document.querySelector('#settings').showModal();
document.querySelector('#settings-close').onclick=()=>document.querySelector('#settings').close();`;
const server = http.createServer((req, res) => {
  if (req.method !== 'GET') { writes++; res.writeHead(405).end(); return; }
  const file = new URL(req.url, 'http://fixture').pathname;
  let content, type;
  if (file === '/') {
    type = 'text/html'; content = '<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentNet storage setup layout fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/app.css">' + settings + '<script type="module" src="/fixture.mjs"></script>';
  } else if (file === '/fixture.mjs') { type = 'text/javascript'; content = bootstrap; }
  else if (['/app.css', '/drivespace-setup.mjs'].includes(file)) {
    type = file.endsWith('.css') ? 'text/css' : 'text/javascript'; content = fs.readFileSync(path.join(base, file.slice(1)));
  } else { res.writeHead(404).end(); return; }
  res.setHeader('Content-Type', type + '; charset=utf-8');
  res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:");
  res.end(content);
});
async function layout(page) {
  return page.evaluate(() => {
    const container = document.querySelector('#file-storage').getBoundingClientRect();
    const selectors = '#settings, #settings-storage, #file-storage, #file-storage .drive-space, #file-storage p, #file-storage pre, #file-storage details, #file-storage label';
    const overflow = [...document.querySelectorAll(selectors)].filter(el => el.getClientRects().length && el.scrollWidth > el.clientWidth + 1).map(el => ({tag:el.tagName,id:el.id,scroll:el.scrollWidth,width:el.clientWidth}));
    const outside = [...document.querySelectorAll('#file-storage p, #file-storage pre, #file-storage label, #file-storage button, #file-storage input')].filter(el => el.getClientRects().length).filter(el => {const r=el.getBoundingClientRect();return r.left<container.left-1||r.right>container.right+1;}).map(el=>el.tagName);
    const checkboxes = [...document.querySelectorAll('#file-storage input[type="checkbox"]')].map(el => {const r=el.getBoundingClientRect();return {width:r.width,height:r.height};});
    return {overflow,outside,checkboxes,documentOverflow:document.documentElement.scrollWidth>innerWidth};
  });
}
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = 'http://127.0.0.1:' + server.address().port;
  let browser;
  try {
    browser = await chromium.launch({executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',headless:true});
    for (const width of [390, 1280]) {
      const context = await browser.newContext({viewport:{width,height:width===390?844:900}});
      await context.route('**/*', route => {
        const url = route.request().url();
        if (url.startsWith(origin + '/') || url.startsWith('data:')) return route.continue();
        external.push(url); return route.abort();
      });
      const page = await context.newPage();
      page.on('pageerror', e => errors.push(e.message));
      page.on('console', m => {if (['error','warning'].includes(m.type())) errors.push(m.type()+': '+m.text());});
      await page.goto(origin);
      assert.equal(await page.title(), 'AgentNet storage setup layout fixture');
      const root = page.locator('#file-storage');
      await root.getByText('Google Drive off for workspace',{exact:true}).waitFor();
      const capture = async name => {
        if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({path:path.join(process.env.AGENTNET_SCREENSHOTS,`drive-setup-${name}-${width}.png`)});
      };
      for (const existing of [false, true]) {
        if (existing) {
          await root.getByLabel('Use existing Google Cloud project',{exact:true}).check();
          const saved = await page.evaluate(()=>fixture.saves);
          await root.getByRole('button',{name:'Save local setup progress',exact:true}).click();
          await page.waitForFunction(n=>fixture.saves>n,saved);
        }
        const mode = existing ? 'existing' : 'new';
        await root.getByText('File storage options',{exact:true}).scrollIntoViewIfNeeded();
        await capture(mode+'-form');
        const summaries = root.locator('summary');
        assert.equal(await summaries.count(),6);
        for (let i=0; i<await summaries.count(); i++) {
          const summary = summaries.nth(i), detail = summary.locator('..');
          await summary.click(); assert.equal(await detail.getAttribute('open'),'');
          const measured = await layout(page);
          assert.deepEqual(measured.overflow,[],`${width}/${mode}: internal overflow ${JSON.stringify(measured)}`);
          assert.deepEqual(measured.outside,[],`${width}/${mode}: escaped storage bounds`);
          assert.equal(measured.documentOverflow,false);
          assert(measured.checkboxes.every(r=>r.width>=10&&r.width<=24&&r.height<=24),`${width}/${mode}: stretched checkbox ${JSON.stringify(measured.checkboxes)}`);
          if (i===1 || i===2) {
            const commands = detail.locator('pre');
            assert(await commands.count()>0);
            if (i===1) assert.equal((await commands.allTextContents()).some(s=>s.includes('gcloud projects create')), !existing);
            await commands.last().scrollIntoViewIfNeeded(); await capture(mode+(i===1?'-project':'-api'));
          }
          await summary.click(); assert.equal(await detail.getAttribute('open'),null);
        }
        const enabled = root.getByLabel('Enable optional Google Drive',{exact:true});
        await enabled.check(); assert.equal(await enabled.isChecked(),true);
        await enabled.uncheck(); assert.equal(await enabled.isChecked(),false);
        assert.equal(await root.getByRole('alert').count(),0);
        checks.push({width,mode,expandersOpenedAndClosed:6,internalOverflow:false,checkboxesCompact:true});
      }
      assert((await page.evaluate(()=>fixture.requests)).every(r=>r.method==='GET'&&r.url==='/v1/storage/drive'));
      await page.getByRole('button',{name:'Close settings',exact:true}).click();
      assert.equal(await page.locator('#settings').getAttribute('open'),null);
      await context.close();
    }
    assert.equal(writes,0); assert.deepEqual(external,[]); assert.deepEqual(errors,[]);
    console.log(JSON.stringify({pass:true,checks,externalRequests:0,httpWrites:0,consoleErrorsAndWarnings:errors}));
  } finally { if (browser) await browser.close(); await new Promise(resolve=>server.close(resolve)); }
})().catch(error=>{console.error(error);process.exitCode=1;});
