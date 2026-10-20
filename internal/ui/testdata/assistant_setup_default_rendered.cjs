// "Choose your agents" with the owner's Zenbook catalog: Codex, Claude and Pi
// made together, and "Zenbook" (Codex too, same folder) an hour later; the
// existing Codex row is also the default, in another folder. The real Comic
// package on an inert host (no Hub, credentials or harness). It checks the
// default setting, exact existing-row selection, older Codex preselection,
// review without a required choice, and apply once without editing other
// agents. Run by TestComicDefaultAgentSetupRendered.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const root = path.resolve(__dirname, '../static'), evidence = process.env.AGENTNET_SETUP_SCREENSHOTS || process.env.AGENTNET_SCREENSHOTS;
assert(evidence, 'private screenshot directory required'); fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const local = 'sergey/zenbook', projects = '/home/sergey/projects', other = '/home/sergey/other';
const ids = { codex: '52e7fc64'+'0'.repeat(24), claude: 'c1a0de00'+'0'.repeat(24), pi: 'b1000000'+'0'.repeat(24), zenbook: '09a818fd'+'0'.repeat(24), automaticClaude: 'ca0def00'+'0'.repeat(24) };
const me = { person: 'sergey', label: 'Sergey', address: local, state: 'self', devices: [{ address: local, fingerprint: 'local-key' }] };
const overview = { version: 'fixture', seq: 1, me: { address: local, fingerprint: 'local-key', responder: 'codex', responder_dir: other, agent: true }, person: me, persons: true, agents: true, role: 'person', people: [], review: [], links: [], reminders: [], threads: [], dms: [], directory: { current: true, members: [] }, quarantine: [], files: { max_count: 8, max_file: 100000, max_message: 200000 } };
const found = [{ name: 'claude', found: true }, { name: 'codex', found: true }, { name: 'pi', found: true }];
const responder = { chosen: true, manual: false, agent_id: ids.codex, harness: 'codex', dir: other, ready: true, harnesses: found };
const named = (id, label, harness, ts, dir=projects) => ({ record: { v: 1, id, host: local, host_key: 'local-key', label, harness, ts }, enabled: true, responder: { chosen: true, manual: false, harness, dir, ready: true, harnesses: null } });
const catalog = { host: local, local: true, harnesses: found, agents: [{...named(ids.codex, 'Codex', 'codex', 1791280000, other),default:true}, named(ids.claude, 'Claude', 'claude', 1791280000), named(ids.pi, 'Pi', 'pi', 1791280000), named(ids.zenbook, 'Zenbook', 'codex', 1791283600)] };
const tool = (id, label) => ({ id, label, detected: true, configured: true, registered: true, supported: true, state: 'connected', note: 'Fixture native setup.' });
const tools = [tool('claude', 'Claude Code'), tool('codex', 'Codex'), tool('pi', 'Pi')];
const boot = `
const seed=${JSON.stringify({ overview, responder, catalog, tools, local, ids })};
window.fixture={...structuredClone(seed),requests:[]};
const host={version:1,platform:'daemon',workspace:{id:'default',name:'Default agent fixture',endpoint:location.origin,address:seed.local,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(){},listen(){return()=>{};},api:async(p,body)=>{
fixture.requests.push({path:p,body:structuredClone(body)});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/responder'&&body){
let selected=body.agent_id?fixture.catalog.agents.find(a=>a.record.id===body.agent_id):fixture.catalog.agents.find(a=>a.default&&a.responder.harness===body.harness);
if(!body.manual&&!selected){if(body.harness!=='claude')throw Error('fixture: unexpected new automatic program');selected={record:{v:1,id:seed.ids.automaticClaude,host:seed.local,host_key:'local-key',label:'Claude',harness:'claude',ts:1791287200},enabled:true,responder:{chosen:true,manual:false,harness:'claude',dir:body.dir,ready:true,harnesses:null}};fixture.catalog.agents.push(selected);}
if(selected&&!body.agent_id)selected.responder={...selected.responder,dir:body.dir};
for(const a of fixture.catalog.agents)a.default=a===selected&&!body.manual;
fixture.responder={...fixture.responder,...(selected?.responder||{}),chosen:true,manual:body.manual,agent_id:body.manual?'':selected.record.id,ready:!body.manual,harnesses:seed.responder.harnesses};fixture.overview.me.responder=fixture.responder.harness;fixture.overview.me.responder_dir=fixture.responder.dir;fixture.overview.seq++;return structuredClone(fixture.responder);}
if(p==='/api/responder')return structuredClone(fixture.responder);
if(p==='/api/agents'&&body){if(body.action==='publish')return {saved:true,published:true,note:''};throw Error('fixture: no agent change expected');}
if(p.startsWith('/api/agents'))return structuredClone(fixture.catalog);
if(p==='/api/assistant-setup')return {local:true,harnesses:structuredClone(fixture.tools),...(body?{review_id:'rev-1',note:''}:{})};
if(p==='/api/groups/invitations')return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.includes('/topics'))return {topics:[],placements:[]};if(p==='/api/permissions')return {questions:[],tasks:[]};return {};
}};
const base='/assets/skins/comic/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const skinRoot=document.createElement('div');skinRoot.className='skin-root';shadow.append(skinRoot);const module=await import(base+manifest.entry);await module.mount(skinRoot,host);window.ready=true;
`;
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://127.0.0.1');
  if (u.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>'); return; }
  if (u.pathname === '/boot.mjs') { res.setHeader('Content-Type', 'text/javascript'); res.end(boot); return; }
  if (u.pathname.startsWith('/assets/')) {
    const file = path.resolve(root, '.' + u.pathname.slice(7));
    if (file.startsWith(root + path.sep) && fs.existsSync(file) && fs.statSync(file).isFile()) {
      res.setHeader('Content-Type', file.endsWith('.mjs') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.json') ? 'application/json' : file.endsWith('.woff2') ? 'font/woff2' : 'application/octet-stream');
      res.end(fs.readFileSync(file)); return;
    }
  }
  res.statusCode = 404; res.end('fixture route missing');
});

(async () => {
  let browser; const shots = [];
  try {
    await new Promise((r) => server.listen(0, '127.0.0.1', r)); const origin = 'http://127.0.0.1:' + server.address().port;
    browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
    for (const width of [1280, 390]) {
      const context = await browser.newContext({ viewport: { width, height: 900 } }), page = await context.newPage(), errors = [];
      page.setDefaultTimeout(10000);
      await context.route('**/*', (r) => (new URL(r.request().url()).origin === origin ? r.continue() : r.abort()));
      page.on('pageerror', (e) => errors.push(e.stack));
      const requests = (route) => page.evaluate((route) => fixture.requests.filter((r) => r.path === route && r.body).map((r) => r.body), route);
      const fit = async () => assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'horizontal overflow');
      const shot = async (name) => { await fit(); const f = path.join(evidence, 'default-agent-' + name + '-' + width + '.png'); await page.screenshot({ path: f, fullPage: true }); shots.push(f); };
      await page.goto(origin); await page.waitForFunction(() => window.ready);
      await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'Agents', exact: true }).click();

      // The default is a setting on an existing concrete row, with no extra chat.
      const yours = page.getByRole('region', { name: 'Your agents', exact: true });
      const card = yours.getByText('Default for requests without an agent', {exact:true}).locator('..');
      await yours.locator('article').filter({hasText:'Codex'}).filter({hasText:'Default'}).getByText('Default',{exact:true}).waitFor();
      assert.equal(await yours.locator('article').count(),4,'only the four concrete agents have chat cards');
      assert.equal(await card.getByRole('button',{name:/Chat/}).count(),0,'default setting has no phantom chat');
      await card.getByRole('button', { name: 'Change default setting', exact: true }).click();
      await card.getByText('Answers for you', { exact: true }).waitFor();
      await shot('agents-card');
      await card.getByRole('button', { name: 'Cancel', exact: true }).click();
      await card.getByRole('button', { name: 'Change default setting', exact: true }).waitFor();
      assert.deepEqual(await requests('/api/responder'), [], 'Cancel saves nothing');

      // Choose your agents: the default agent first, with program, folder and state.
      await yours.getByRole('button', { name: 'Connect an agent', exact: true }).click();
      await page.getByRole('heading', { name: 'Choose your agents', exact: true }).waitFor();
      const def = page.getByRole('region', { name: 'Default agent', exact: true });
      await def.waitFor();
      const defText = await def.innerText();
      assert.match(defText, /Default agent\s*Ready/i);
      assert.match(defText, new RegExp('Codex works in ' + other));
      const codexTool = page.getByRole('checkbox', { name: 'Set up Codex', exact: true });
      assert((await def.boundingBox()).y < (await page.getByRole('checkbox', { name: 'Set up Claude Code', exact: true }).boundingBox()).y, 'default agent shows above the tools');
      assert(await codexTool.isChecked(), 'Codex starts chosen');

      // Two agents run Codex: preselected and explained, never a forced choice.
      const codexItem = page.locator('li').filter({ has: codexTool });
      await codexItem.getByText('Two agents run Codex on this computer: “Codex” and “Zenbook”. Using “Codex”; “Zenbook” stays available with its history. You can switch below.', { exact: true }).waitFor();
      const radios = codexItem.locator('input[type=radio]');
      assert.equal(await radios.count(), 2);
      const checked = await radios.evaluateAll((xs) => xs.map((x) => [x.closest('label').innerText.split('\n')[0], x.checked]));
      assert.deepEqual(checked, [['Codex', true], ['Zenbook', false]]);
      assert.equal(await page.getByText(/Choose the one to keep using|Choose which agent/).count(), 0);
      await shot('choose');
      { const f = path.join(evidence, 'default-agent-codex-' + width + '.png'); await codexItem.screenshot({ path: f }); shots.push(f); }

      // Change the default agent right there: Claude, same folder.
      await def.getByRole('button', { name: 'Change default agent', exact: true }).click();
      await def.locator('label').filter({ hasText: 'Claude' }).filter({ hasText: 'Installed here' }).click();
      await shot('change-default');
      await def.getByRole('button', { name: 'Save', exact: true }).click();
      await def.getByText(new RegExp('Claude works in ' + other)).waitFor();
      assert.deepEqual(await requests('/api/responder'), [{ manual: false, harness: 'claude', dir: other }]);
      // The duplicate's pick did not move with it.
      assert.deepEqual(await radios.evaluateAll((xs) => xs.map((x) => x.checked)), [true, false]);

      // Review keeps Codex, says Zenbook stays; applying touches no agent.
      await page.getByRole('button', { name: 'Review changes', exact: true }).click();
      await page.getByRole('heading', { name: 'Check the changes', exact: true }).waitFor();
      const review = await page.getByRole('region', { name: 'Set up your agents', exact: true }).innerText();
      assert.match(review, /Keeps its agent: Codex/); assert.match(review, /Stays as it is: Zenbook/);
      await shot('review');
      await page.getByRole('button', { name: 'Apply changes', exact: true }).click();
      await page.getByRole('heading', { name: 'Agent setup saved', exact: true }).waitFor();
      assert.deepEqual((await requests('/api/agents')).map((b) => b.action), ['publish'], 'no agent is created, updated or turned off');
      assert.deepEqual((await requests('/api/assistant-setup')).map(b=>b.action),['review','apply'],'review and apply each run once');
      const unchanged=await page.evaluate(()=>fixture.catalog.agents.filter(a=>Object.values(fixture.ids).slice(0,4).includes(a.record.id)).map(a=>({record:a.record,responder:a.responder})));
      assert.deepEqual(unchanged,catalog.agents.map(a=>({record:a.record,responder:a.responder})),'program change and applying leave every original agent configuration intact');

      // Selecting another existing concrete row changes only the default pointer.
      await page.getByRole('button', { name: 'Done', exact: true }).click();
      const piCard=yours.locator('article').filter({has:page.getByRole('button',{name:'Chat with Pi',exact:true})});
      await piCard.getByRole('button',{name:'Use as default',exact:true}).click();
      await page.waitForFunction(id=>fixture.responder.agent_id===id,ids.pi);
      await piCard.getByText('Default',{exact:true}).waitFor();
      assert.deepEqual(await requests('/api/responder'),[{manual:false,harness:'claude',dir:other},{agent_id:ids.pi,manual:false,harness:'',dir:''}]);
      assert.equal(await page.evaluate(()=>fixture.responder.agent_id),ids.pi,'default points to the exact existing Pi ID');
      assert.equal(await yours.locator('article').count(),5,'changing programs adds one concrete instance, not a default-only chat');
      await shot('existing-default');
      assert.deepEqual(errors, []);
    }
    console.log(JSON.stringify({ ok: true, shots }));
  } finally { if (browser) await browser.close(); server.close(); }
})().catch((e) => { console.error(e); process.exitCode = 1; });
