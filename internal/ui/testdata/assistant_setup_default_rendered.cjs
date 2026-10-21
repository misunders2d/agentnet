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
const ids = { codex: '52e7fc64'+'0'.repeat(24), claude: 'c1a0de00'+'0'.repeat(24), pi: 'b1000000'+'0'.repeat(24), zenbook: '09a818fd'+'0'.repeat(24), legacyCodex: 'c0de0000'+'0'.repeat(24) };
const me = { person: 'sergey', label: 'Sergey', address: local, state: 'self', devices: [{ address: local, fingerprint: 'local-key' }] };
const overview = { version: 'fixture', seq: 1, me: { address: local, fingerprint: 'local-key', responder: 'codex', responder_dir: other, agent: true }, person: me, persons: true, agents: true, role: 'person', people: [], review: [], links: [], reminders: [], threads: [], dms: [], directory: { current: true, members: [] }, quarantine: [], files: { max_count: 8, max_file: 100000, max_message: 200000 } };
const found = [{ name: 'claude', found: true }, { name: 'codex', found: true }, { name: 'pi', found: true }];
const responder = { chosen: true, manual: false, agent_id: ids.codex, harness: 'codex', dir: other, ready: true, harnesses: found };
const named = (id, label, harness, ts, dir=projects) => ({ record: { v: 1, id, host: local, host_key: 'local-key', label, harness, ts }, enabled: true, responder: { chosen: true, manual: false, harness, dir, ready: true, harnesses: null } });
const catalog = { host: local, local: true, harnesses: found, agents: [{...named(ids.legacyCodex, 'Codex', 'codex', 1791280000),enabled:false,responder:null}, named(ids.claude, 'Claude', 'claude', 1791280000), named(ids.pi, 'Pi', 'pi', 1791280000), {...named(ids.zenbook, 'Zenbook', 'codex', 1791283600),enabled:false,responder:null}, {...named(ids.codex, 'Codex', 'codex', 1792529957, other),default:true}] };
const tool = (id, label) => ({ id, label, detected: true, configured: true, registered: true, supported: true, state: 'connected', note: 'Fixture native setup.' });
const tools = [tool('claude', 'Claude Code'), tool('codex', 'Codex'), tool('pi', 'Pi')];
const boot = `
const seed=${JSON.stringify({ overview, responder, catalog, tools, local, ids })};
window.fixture={...structuredClone(seed),requests:[]};
const host={version:1,platform:'daemon',workspace:{id:'default',name:'Default agent fixture',endpoint:location.origin,address:seed.local,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(){},listen(){return()=>{};},api:async(p,body)=>{
fixture.requests.push({path:p,body:structuredClone(body)});
if(p.startsWith('/api/overview'))return structuredClone(fixture.overview);
if(p==='/api/responder'&&body){
let selected=body.agent_id?fixture.catalog.agents.find(a=>a.enabled&&a.record.id===body.agent_id):fixture.catalog.agents.find(a=>a.enabled&&a.responder?.harness===body.harness);
if(!body.manual&&!selected)throw Error('fixture: existing harness identity must be reused');
if(selected&&!body.agent_id)selected.responder={...selected.responder,dir:body.dir};
if(fixture.managerMode&&selected&&body.agent_id)for(const a of fixture.catalog.agents)if(a!==selected&&a.record.harness===selected.record.harness){a.enabled=false;a.responder=null;}
for(const a of fixture.catalog.agents)a.default=a===selected&&!body.manual;
fixture.responder={...fixture.responder,...(selected?.responder||{}),chosen:true,manual:body.manual,agent_id:body.manual?'':selected.record.id,ready:!body.manual,harnesses:seed.responder.harnesses};fixture.overview.me.responder=fixture.responder.harness;fixture.overview.me.responder_dir=fixture.responder.dir;fixture.overview.seq++;return structuredClone(fixture.responder);}
if(p==='/api/responder')return structuredClone(fixture.responder);
if(p==='/api/agents'&&body){
if(body.action==='publish')return {saved:true,published:true,note:''};
if(fixture.managerMode){const a=fixture.catalog.agents.find(a=>a.record.id===body.id);if(!a)throw Error('fixture: manager must retain the exact existing ID');
if(body.action==='disable'){a.enabled=false;a.responder=null;a.default=false;if(fixture.responder.agent_id===a.record.id)fixture.responder={...fixture.responder,agent_id:'',manual:true,ready:false};}
else if(body.action==='update'){if(!body.harness||!body.dir?.startsWith('/'))throw Error('fixture: missing configuration must be entered explicitly');a.enabled=true;a.record.harness=body.harness;a.responder={chosen:true,manual:false,harness:body.harness,dir:body.dir,ready:true,harnesses:null};}
else throw Error('fixture: management must not create a new identity');fixture.overview.seq++;return {saved:true,published:true,note:''};}
throw Error('fixture: no agent change expected');}
if(p.startsWith('/api/agents'))return structuredClone(fixture.catalog);
if(p==='/api/assistant-setup')return {local:true,harnesses:structuredClone(fixture.tools),...(body?{review_id:'rev-1',note:''}:{})};
if(p==='/api/folders')return {path:'/home/sergey/explicit-repair',parent:'/home/sergey',home:'/home/sergey',roots:['/'],dirs:[],truncated:false};
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
      assert.equal(await yours.locator('article').count(),3,'only one usable agent per harness is listed');
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

      // One Codex program, using the exact current configuration. The two
      // older saved rows are not presented as additional Codex agents.
      const codexItem = page.locator('li').filter({ has: codexTool });
      await codexItem.getByText('Codex is set up for chats, questions and approved tasks.', { exact: true }).waitFor();
      assert.equal(await codexItem.getByText(/agents run Codex/).count(), 0);
      assert.equal(await codexItem.locator('input[type=radio]:visible').count(), 0, 'saved configurations are collapsed');
      assert.match(await codexItem.innerText(), new RegExp(other));
      assert.doesNotMatch(await codexItem.innerText(), /Zenbook/);
      assert.equal(await codexItem.getByRole('button', {name:/Saved .* configurations/}).count(), 0, 'no duplicate configuration picker');
      await shot('choose');
      { const f = path.join(evidence, 'default-agent-codex-' + width + '.png'); await codexItem.screenshot({ path: f }); shots.push(f); }

      // Change the default agent right there: Claude, same folder.
      await def.getByRole('button', { name: 'Change default agent', exact: true }).click();
      await def.locator('label').filter({ hasText: 'Claude' }).filter({ hasText: 'Installed here' }).click();
      await shot('change-default');
      await def.getByRole('button', { name: 'Save', exact: true }).click();
      await def.getByText(new RegExp('Claude works in ' + other)).waitFor();
      assert.deepEqual(await requests('/api/responder'), [{ manual: false, harness: 'claude', dir: other }]);
      assert.match(await codexItem.innerText(), new RegExp(other), 'Codex keeps its exact identity after changing default harness');

      // Review keeps each current identity; applying creates no duplicate.
      await page.getByRole('button', { name: 'Review changes', exact: true }).click();
      await page.getByRole('heading', { name: 'Check the changes', exact: true }).waitFor();
      const review = await page.getByRole('region', { name: 'Set up your agents', exact: true }).innerText();
      assert.match(review, /Existing setup: Codex/); assert.doesNotMatch(review, /Stays as it is: Zenbook|agents run Codex/);
      await shot('review');
      await page.getByRole('button', { name: 'Apply changes', exact: true }).click();
      await page.getByRole('heading', { name: 'Agent setup saved', exact: true }).waitFor();
      assert.deepEqual((await requests('/api/agents')).map((b) => b.action), ['publish'], 'no agent is created, updated or turned off');
      assert.deepEqual((await requests('/api/assistant-setup')).map(b=>b.action),['review','apply'],'review and apply each run once');
      const unchanged=await page.evaluate(()=>fixture.catalog.agents.filter(a=>Object.values(fixture.ids).slice(0,5).includes(a.record.id)).map(a=>({record:a.record,responder:a.responder})));
      assert.deepEqual(unchanged,catalog.agents.map(a=>({record:a.record,responder:a.record.id===ids.claude?{...a.responder,dir:other}:a.responder})),'only the explicitly changed Claude working folder differs; all original IDs and retired records remain');

      // Selecting another existing concrete row changes only the default pointer.
      await page.getByRole('button', { name: 'Done', exact: true }).click();
      const piCard=yours.locator('article').filter({has:page.getByRole('button',{name:'Chat with Pi',exact:true})});
      await piCard.getByRole('button',{name:'Use as default',exact:true}).click();
      await page.waitForFunction(id=>fixture.responder.agent_id===id,ids.pi);
      await piCard.getByText('Default',{exact:true}).waitFor();
      assert.deepEqual(await requests('/api/responder'),[{manual:false,harness:'claude',dir:other},{agent_id:ids.pi,manual:false,harness:'',dir:''}]);
      assert.equal(await page.evaluate(()=>fixture.responder.agent_id),ids.pi,'default points to the exact existing Pi ID');
      assert.equal(await yours.locator('article').count(),3,'default changes never add a second identity for a harness');
      await shot('existing-default');
      await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: width < 700 ? 'You' : 'Settings', exact: true }).click();
      await page.getByRole('button', {name: /^Coding agents/}).click();
      await page.getByRole('heading', {name:'Coding agents',exact:true}).waitFor();
      await shot('settings');

      // Management is a collapsed repair surface, separate from active agents.
      // Reproduce an old ambiguous catalog without assigning a current Codex.
      await page.evaluate(()=>{
        fixture.managerMode=true;
        for(const id of [fixture.ids.legacyCodex,fixture.ids.codex,fixture.ids.zenbook]){
          const a=fixture.catalog.agents.find(a=>a.record.id===id);a.enabled=true;a.default=false;
          a.responder={chosen:true,manual:false,harness:'codex',dir:id===fixture.ids.zenbook?'/home/sergey/chosen-saved':'/home/sergey/older-saved',ready:true,harnesses:null};
        }
      });
      await page.getByRole('button',{name:'Find them',exact:true}).click();
      await page.getByRole('heading',{name:'Choose your agents',exact:true}).waitFor();
      const manager=page.getByRole('button',{name:'Manage saved setups',exact:true});await manager.waitFor();
      const exact=page.locator('[data-agent-setup="'+ids.zenbook+'"]');
      assert.equal(await exact.isVisible(),false,'saved configurations do not leak out of collapsed management');
      await manager.click();await exact.getByText('/home/sergey/chosen-saved',{exact:true}).waitFor();
      const writesBefore=(await requests('/api/agents')).length;
      await exact.getByRole('button',{name:'Use as current',exact:true}).click();
      await exact.getByText('Zenbook · Current',{exact:true}).waitFor();
      assert.equal(await page.evaluate(()=>fixture.responder.agent_id),ids.zenbook,'the clicked saved ID becomes current, not the first Codex record');
      assert.deepEqual((await requests('/api/responder')).at(-1),{agent_id:ids.zenbook,manual:false,harness:'',dir:''});
      assert.deepEqual(await page.evaluate(()=>fixture.catalog.agents.filter(a=>a.enabled&&a.record.harness==='codex').map(a=>a.record.id)),[ids.zenbook],'explicit selection resolves all legacy enabled duplicates');
      await exact.getByRole('button',{name:'Disable',exact:true}).click();
      await exact.getByText('Disabled',{exact:true}).waitFor();
      assert.equal(await page.evaluate(()=>fixture.catalog.agents.find(a=>a.record.id===fixture.ids.zenbook).responder),null,'disabled configuration really is unavailable');
      await exact.getByRole('button',{name:'Repair or enable',exact:true}).click();
      const repair=page.getByRole('dialog',{name:'Turn on Zenbook',exact:true});await repair.waitFor();
      const folder=repair.getByRole('group',{name:'Works in',exact:true});
      assert(await folder.getByText('No folder chosen yet',{exact:true}).isVisible(),'vanished responder is not cloned from another agent or guessed history');
      assert(await repair.getByRole('button',{name:'Turn on',exact:true}).isDisabled(),'explicit working folder is required');
      assert.equal(await repair.getByRole('radio',{name:/codex/i}).count(),1,'only the installed unoccupied program is offered');
      await folder.getByRole('button',{name:'Choose folder: Works in',exact:true}).click();
      const chooser=page.getByRole('dialog',{name:'Choose a folder',exact:true});await chooser.waitFor();
      await chooser.getByText('/home/sergey/explicit-repair',{exact:true}).waitFor();
      await chooser.getByRole('button',{name:'Use “explicit-repair”',exact:true}).click();
      await chooser.waitFor({state:'hidden'});await folder.getByText('/home/sergey/explicit-repair',{exact:true}).waitFor();
      await repair.getByRole('button',{name:'Turn on',exact:true}).click();
      await repair.waitFor({state:'hidden'});await exact.getByText('/home/sergey/explicit-repair',{exact:true}).waitFor();
      assert.deepEqual((await requests('/api/agents')).slice(writesBefore),[{action:'publish'},{action:'disable',id:ids.zenbook},{action:'update',id:ids.zenbook,harness:'codex',dir:'/home/sergey/explicit-repair'}]);
      assert.deepEqual(await page.evaluate(()=>fixture.catalog.agents.map(a=>a.record.id)),catalog.agents.map(a=>a.record.id),'all original current/retired IDs remain, without creating a setup');
      assert.equal(await exact.getByRole('button',{name:/Chat/}).count(),0,'management contains no active chat target');
      await shot('saved-setup-manager');
      assert.deepEqual(errors, []);
    }
    console.log(JSON.stringify({ ok: true, shots }));
  } finally { if (browser) await browser.close(); server.close(); }
})().catch((e) => { console.error(e); process.exitCode = 1; });
