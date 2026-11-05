// C1 New Group selection/navigation, using the actual Comic components and Store.
// Only the transport and records are synthetic. Missing dimensions remain blockers.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http');
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { measure, finishMeasurement, capturePresented, checkBackend, checkReport, digest } = require('./c1_oracle.cjs');
const repo = path.resolve(__dirname, '../../..'), web = path.join(repo, 'internal/ui/web');
const prefix = 'agentnet:internal/ui/web/src/features/NewChat.tsx#';
const id = (suffix, event) => prefix + suffix + '#' + event;
const definition = handler => handler.slice(0, handler.lastIndexOf('#'));
const bindings = {
  back: [id('back/button/onClick#1', 'onClick'), id('close/NewGroupForm/onBack,onCreated#1', 'onBack')],
  close: [id('close/Sheet/onOpenChange#1', 'onOpenChange')],
  search: [id('back/input/onChange#2', 'onChange')],
  clear: [id('back/Button/onClick#5', 'onClick')],
  more: [id('back/Button/onClick#6', 'onClick')],
  checkbox: [id('back/input/onChange#3', 'onChange')],
};

// A concrete rendered snapshot; no label-level or arbitrary-body-change oracle.
function snapshot() {
  if (globalThis.__c1ThrowSnapshot) throw Error('Injected C1 snapshot failure');
  const visible = el => !!el && el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden' && getComputedStyle(el).display !== 'none';
  const buttons = [...document.querySelectorAll('button')];
  const dialog = [...document.querySelectorAll('[role=dialog]')].find(visible);
  const search = dialog?.querySelector('input[type=search]');
  const group = [...document.querySelectorAll('input')].find(e => e.placeholder === 'For example, Savannah rush order');
  const boxes = [...(dialog?.querySelectorAll('input[type=checkbox]') || [])];
  const selected = document.querySelector('[aria-label="Selected people"]');
  return {
    dialog: dialog?.textContent.includes('Give it a name.') ? 'group' : dialog ? 'chat' : null,
    search: search?.value ?? null,
    searchFocused: !!search && document.activeElement === search,
    groupName: group?.value ?? null,
    clear: buttons.some(b => visible(b) && b.textContent.trim() === 'Clear search'),
    more: buttons.some(b => visible(b) && /^Show more people/.test(b.textContent.trim())),
    noMatches: [...document.querySelectorAll('p')].some(e => visible(e) && e.textContent === 'No matching people.'),
    checkboxes: boxes.map(e => ({ name: e.closest('label')?.textContent.trim(), checked: e.checked })),
    selected: [...(selected?.querySelectorAll('button') || [])].map(e => e.textContent.trim()),
    newFocused: document.activeElement?.textContent?.trim() === 'New',
  };
}

function definitions(people) {
  const list = [
    { key: 'search.match', action: 'search', value: 'Person 00', local: s => s.search === 'Person 00', confirmed: s => s.checkboxes.length === 1 && s.checkboxes[0].name === 'Person 00' },
    { key: 'search.none', action: 'search', value: 'nobody', local: s => s.search === 'nobody', confirmed: s => s.noMatches && s.checkboxes.length === 0 },
    { key: 'clear.match', action: 'clear', prepare: 'match', local: s => s.search === '' && !s.clear, confirmed: s => s.searchFocused && s.checkboxes.length === 40 },
    { key: 'clear.none', action: 'clear', prepare: 'none', local: s => s.search === '' && !s.clear, confirmed: s => s.searchFocused && s.checkboxes.length === 40 },
    { key: 'search.resets-page', action: 'search', prepare: 'expanded', value: 'Person', local: s => s.search === 'Person', confirmed: s => s.checkboxes.length === 40 && s.more },
    { key: 'more', action: 'more', local: s => !s.more, confirmed: s => s.checkboxes.length === 41 },
    { key: 'back', action: 'back', local: s => s.dialog === 'chat', confirmed: s => s.groupName === null && s.search !== null },
    { key: 'close.escape', action: 'close', escape: true, local: s => s.dialog === null, confirmed: s => s.newFocused },
    { key: 'close.button', action: 'close', local: s => s.dialog === null, confirmed: s => s.newFocused },
  ];
  for (let n = 0; n < people; n++) for (const checked of [true, false]) {
    const name = 'Person ' + String(n).padStart(2, '0');
    list.push({ key: 'checkbox.' + n + '.' + (checked ? 'select' : 'unselect'), action: 'checkbox', name, checked,
      // Generated predicates bind the exact person instance, not only the shared handler.
      local: new Function('s', 'return s.checkboxes.some(p=>p.name===' + JSON.stringify(name) + '&&p.checked===' + checked + ')'),
      confirmed: new Function('s', 'return s.selected.includes(' + JSON.stringify(name) + ')===' + checked),
    });
  }
  return list;
}

function entry(config) {
  return `
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {NewChatButton} from './src/features/NewChat';
const config=${JSON.stringify(config)},mode=new URL(location).searchParams.get('backend');
const people=Array.from({length:config.people},(_,n)=>({person:'person-'+n,label:'Person '+String(n).padStart(2,'0'),state:'pinned',address:'person-'+n+'/laptop',devices:[]}));
people.push({person:'unverified',label:'Excluded unverified',state:'unknown',address:'unverified/laptop',devices:[]});
const person={person:'self',label:'Self',state:'self',address:'self/laptop',devices:[]};people.push(person);
const overview={seq:1,version:'synthetic-c1',role:'person',groups:true,persons:true,me:{address:'self/laptop'},person,people,dms:[],threads:[],needs_you:[]};
const f=globalThis.fixture={config,mode,requests:0,pending:0,failed:0};
const host={platform:'daemon',workspace:{id:'c1-synthetic',state:'enrolled'},listen(){return()=>{}},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p==='/api/teams'){f.requests++;if(mode==='failing'||config.fault==='backend-fails'){f.failed++;throw Error('Synthetic C1 team-list refusal')}if(mode==='held'){f.pending++;return new Promise(()=>{})}return {status:'available',current:true,teams:[]}}
 if(p==='/api/refresh'||p==='/api/topic/transfer/location')return {};
 if(p.includes('/groups/invitations'))return [];
 if(p.startsWith('/api/agents'))return {agents:[]};
 if(b)throw Error('Unexpected C1 fixture mutation '+p);return {};
}};
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const store=new Store(host);await store.start();
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><NewChatButton/></StoreContext.Provider></OwnedContext.Provider>);
`;
}

async function run({ out, negative = null, part = null }) {
  assert(out && path.isAbsolute(out), 'C1_OUT must be a new absolute private artifact directory');
  assert(!path.resolve(out).startsWith(repo + path.sep), 'C1 artifacts must stay outside repository');
  assert([null, 'already-true', 'fixture', 'handler-noop', 'independent-noop', 'slow-paint', 'backend-fails', 'observer-throws'].includes(negative), 'Unknown C1 negative');
  assert(part===null||part===0||part===1,'C1 part must be 0 or 1');
  process.umask(0o077);
  fs.mkdirSync(out, { mode: 0o700 });
  const work = path.join(out, 'source');
  fs.mkdirSync(work);
  fs.cpSync(path.join(web, 'src'), path.join(work, 'src'), { recursive: true });
  execFileSync(process.execPath, [path.join(__dirname, 'interaction_inventory.cjs'), '--tag', path.join(work, 'src')], { stdio: 'pipe' });
  const aliases = { 'optimistic.mjs':'static/optimistic.mjs', 'pictures.mjs':'static/pictures.mjs', 'person-topics.mjs':'skins/shared/person-topics.mjs', 'reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs', 'collective-mentions.mjs':'skins/shared/collective-mentions.mjs', 'drive-space.mjs':'skins/shared/drivespace.mjs', 'drive-setup.mjs':'skins/shared/drivespace-setup.mjs' };
  for (const [a,b] of Object.entries(aliases)) fs.copyFileSync(path.resolve(web, '../', b), path.join(work, 'src', a));
  fs.symlinkSync(path.join(web, 'node_modules'), path.join(work, 'node_modules'), 'dir');
  const source = fs.readFileSync(path.join(web, 'src/features/NewChat.tsx'), 'utf8');
  const pageSize = Number(source.split('export function NewGroupForm')[1].match(/const pageSize = (\d+);/)[1]);
  assert.equal(pageSize, 40, 'C1 expected page-size contract changed; review cases');
  const config = { pageSize, people: pageSize + 1 };
  const selectedRows = require('./c1_controls_rendered.cjs').rows.filter(r => Object.values(bindings).flat().some(h => definition(h) === r.id));
  assert.equal(selectedRows.length,7,'C1 source-row selection changed');
  for (const row of selectedRows) {
    assert.equal(row.scene,'newgroup','C1 fixture scene not consumed');
    if (row.people !== undefined) assert.equal(row.people,config.people,'C1 people fixture metadata disagrees with actual source page size');
  }
  const fixtureEntry = entry({ ...config, fault:negative, ...(negative === 'fixture'?{people:0}:{}) });
  fs.writeFileSync(path.join(out, 'fixture.tsx'), fixtureEntry);
  const js = require(path.join(web, 'node_modules/esbuild')).buildSync({ absWorkingDir:work, stdin:{ contents:fixtureEntry, resolveDir:work, loader:'tsx' }, bundle:true, format:'esm', target:'es2022', jsx:'automatic', define:{'process.env.NODE_ENV':'"production"'}, write:false }).outputFiles[0].text;
  fs.writeFileSync(path.join(out, 'bundle.mjs'), js);
  execFileSync(process.execPath, [path.join(web, 'node_modules/@tailwindcss/cli/dist/index.mjs'), '-i', path.join(work, 'src/styles.css'), '-o', path.join(out, 'style.css'), '--minify'], { cwd:work, stdio:'pipe' });
  const css = fs.readFileSync(path.join(out, 'style.css'));
  const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
  const browserPath = process.env.AGENTNET_CHROMIUM || chromium.executablePath();
  const browserBytes = fs.readFileSync(fs.realpathSync(browserPath));
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding:'utf8' }).trim();
  const runnerFiles = ['c1_controls_rendered.cjs', 'c1_new_group.cjs', 'c1_oracle.cjs', 'interaction_inventory.cjs'];
  function sourceInputs(dir) {
    return fs.readdirSync(dir,{withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name)).flatMap(e=>{
      const file=path.join(dir,e.name);return e.isDirectory()?sourceInputs(file):[{path:path.relative(work,file),sha256:digest(fs.readFileSync(file))}];
    });
  }
  const inputs=sourceInputs(path.join(work,'src'));
  fs.writeFileSync(path.join(out,'source-inputs.json'),JSON.stringify(inputs,null,2));
  const runnerInputs=runnerFiles.map(n=>({path:n,sha256:digest(fs.readFileSync(path.join(__dirname,n)))}));
  const binding = { revision:git('rev-parse','HEAD'), tree:git('rev-parse','HEAD^{tree}'),
    source_digest:digest(JSON.stringify(inputs)), dirty:!!git('status','--porcelain'),
    bundle_sha256:digest(js), css_sha256:digest(css),
    runner_sha256:digest(JSON.stringify(runnerInputs)), runner_inputs:runnerInputs,
    launcher_sha256:digest(browserBytes), launcher_path:fs.realpathSync(browserPath), browser_sha256:null,
    dependencies:{node:process.version,playwright:require(path.join(process.env.AGENTNET_PLAYWRIGHT,'package.json')).version,web_lock_sha256:digest(fs.readFileSync(path.join(web,'package-lock.json')))},
    fixture_sha256:digest(fixtureEntry), config };
  const cases = [], defs = definitions(config.people);
  for (const width of [1440,390]) for (const backend of ['normal','held','failing']) for (const d of defs) {
    const other_handlers = d.action === 'back' ? [id('shownAgents/input/onChange,onKeyDown#1','onChange')]
      : d.action === 'close' ? [id('NewChatButton/Button/onClick#1','onClick')] : [id('back/input/onChange#1','onChange')];
    const value='Independent '+cases.length;
    const other_expected=d.action==='close'?{local:'s=>s.dialog===\'chat\'',confirmed:'s=>s.search!==null'}
      :d.action==='back'?{local:'s=>s.search===\'independent\'',confirmed:'s=>s.search===\'independent\'&&s.checkboxes.length===0'}
      :{local:'s=>s.groupName==='+JSON.stringify(value),confirmed:'s=>s.groupName==='+JSON.stringify(value)};
    cases.push({ case_id:`${d.key}::${width}::${backend}`, ordinal:cases.length, semantic_action:d.action, key:d.key, target_id:d.name || d.action,
      target_definition_id:d.action==='close'?'agentnet:internal/ui/web/src/ui/Sheet.tsx#Sheet/Dialog.Close/#1':definition(bindings[d.action][0]),
      target_label:d.name||null,other_definition_id:definition(other_handlers[0]),
      expected:{local:d.local.toString(),confirmed:d.confirmed.toString()},other_expected,
      handlers:bindings[d.action], other_handlers, dimensions:{width,backend,skin:'comic',platform:'Linux-Chromium',
        history:'not_exercised',people_fixture:41,app_cache:'warm-within-cell',
        view:d.action==='checkbox'&&(d.name!=='Person 00'||!d.checked)?'reused-sheet':'freshly-mounted-sheet',
        input:d.action==='search'?'native-text-insertion':d.escape?'keyboard-Escape':'pointer'} });
  }
  const widths=part===null?[1440,390]:[[1440,390][part]];
  const selectedCases=cases.filter(c=>widths.includes(c.dimensions.width));
  const manifest = { family:'new-group-selection', binding, cases:selectedCases, required_case_ids:selectedCases.map(c=>c.case_id),
    all_required_case_ids:cases.map(c=>c.case_id),partition:{index:part,count:2,rule:'0=1440px; 1=390px'},
    semantic_control_types:6, physical_target_instances:46, dynamic_checkbox_instances:41,
    semantic_controls:Object.keys(bindings).map(action=>({action,callback_definitions:bindings[action],state_cases:defs.filter(d=>d.action===action).map(d=>d.key)})),
    aliases:[{action:'back',input_definition:definition(bindings.back[0]),forwarded_callback:bindings.back[1],remaining_callback:'onCreated'}],
    source_callback_bindings:bindings, selected_source_rows:selectedRows.map(r=>r.id),
    c1_source_rows_total:43, c1_source_rows_outside_subset:36, source_rows_complete_across_all_required_dimensions:0,
    partial_source_row:'NewGroupForm onBack covered; onCreated remains missing',
    adjacent_controls_not_closed:['selected-person Remove chips','Create group and pending/recovery actions','people-list controls','other New Chat chooser actions'],
    missing_dimensions:['normal/10x real history','cold/warm real app','real delayed/failing backend and DB/sync contention','rapid/duplicate input','stale/newer remote state','touch and keyboard activation variants','offline/reconnect/restart','creation-pending branch','other skins/platforms','native identity/admission/E2EE qualification'],
    release_qualified:false };
  fs.writeFileSync(path.join(out,'plan.json'), JSON.stringify(manifest,null,2));
  const report = { binding, results:[], errors:[], global_coverage:'BLOCKED; explicit family subset only' };
  const server = http.createServer((req,res) => {
    if (req.url === '/bundle.mjs') { res.setHeader('Content-Type','text/javascript');return res.end(js); }
    if (req.url === '/style.css') { res.setHeader('Content-Type','text/css');return res.end(css); }
    res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/bundle.mjs"></script>');
  });
  let browser;
  try {
    await new Promise(r => server.listen(0,'127.0.0.1',r));
    browser = await chromium.launch({ headless:true, executablePath:browserPath });
    const session=await browser.newBrowserCDPSession();
    const processes=await session.send('SystemInfo.getProcessInfo');
    const processInfo=processes.processInfo.find(p=>p.type==='browser');
    assert(processInfo && Number.isSafeInteger(processInfo.id),'C1 actual browser process missing');
    binding.browser_path=fs.realpathSync('/proc/'+processInfo.id+'/exe');
    binding.browser_sha256=digest(fs.readFileSync(binding.browser_path));
    binding.browser_pid=processInfo.id;binding.browser_version=browser.version();
    if (process.env.C1_BOUND_UNIT) {
      const unit=process.env.C1_BOUND_UNIT;
      const cgroup=fs.readFileSync('/proc/self/cgroup','utf8').trim().split('0::')[1];
      assert(/^agr-c1-[a-z0-9-]+\.service$/.test(unit)&&cgroup.endsWith('/'+unit),'C1 task-owned cgroup binding missing');
      const pin=pid=>{const stat=fs.readFileSync('/proc/'+pid+'/stat','utf8').split(') ')[1].split(' ');return{pid,start_ticks:stat[19],uid:fs.statSync('/proc/'+pid).uid,exe:fs.realpathSync('/proc/'+pid+'/exe'),cgroup:fs.readFileSync('/proc/'+pid+'/cgroup','utf8').trim().split('0::')[1]}};
      fs.writeFileSync(path.join(out,'processes.json'),JSON.stringify({unit,cgroup,pins:[pin(process.pid),pin(processInfo.id)],server_port:server.address().port},null,2));
    }
    await session.detach();
    fs.writeFileSync(path.join(out,'manifest.json'),JSON.stringify(manifest,null,2));
    for (const width of widths) for (const backend of ['normal','held','failing']) {
      const page = await browser.newPage({ viewport:{width,height:960}, reducedMotion:'reduce' });
      page.setDefaultTimeout(4000);
      const errors = [];page.on('pageerror',e=>errors.push(String(e)));
      await page.addInitScript(({negative,searchHandler,independentHandler}) => {
        globalThis.__c1Fired=[];
        globalThis.__anTrack=(id,fn)=>typeof fn!=='function'?fn:function(...args){
          globalThis.__c1Fired.push({id,at_ms:performance.now()});
          if ((negative==='handler-noop'&&id===searchHandler)||(negative==='independent-noop'&&id===independentHandler)) return;
          if (negative==='slow-paint'&&id===searchHandler) { const until=performance.now()+1100;while(performance.now()<until){} }
          if (negative==='observer-throws'&&id===searchHandler) globalThis.__c1ThrowSnapshot=true;
          return fn.apply(this,args);
        };
      },{negative,searchHandler:bindings.search[0],independentHandler:id('back/input/onChange#1','onChange')});
      await page.goto('http://127.0.0.1:'+server.address().port+'/?backend='+backend);
      async function freshGroup() {
        if (await page.getByRole('button',{name:'Close',exact:true}).isVisible().catch(()=>false)) await page.getByRole('button',{name:'Close',exact:true}).click();
        await page.getByRole('button',{name:'New',exact:true}).click();
        await page.getByRole('button',{name:/^New group/}).click();
        await page.getByRole('textbox',{name:'Group name',exact:true}).waitFor();
        assert.equal(await page.getByRole('checkbox').count(), pageSize, 'C1 fixture people metadata not consumed');
        assert.equal(await page.getByRole('checkbox',{name:/Excluded|Self/}).count(), 0, 'C1 fixture eligibility violated');
        await page.waitForFunction(()=>fixture.requests>0);
      }
      const search = () => page.getByRole('searchbox',{name:'Search people',exact:true});
      await freshGroup();
      for (const d of defs) {
        const c = cases.find(c=>c.key===d.key && c.dimensions.width===width && c.dimensions.backend===backend);
        let phase='fixture';
        try {
          if (d.action !== 'checkbox' || (d.name==='Person 00' && d.checked)) await freshGroup();
          if (d.prepare === 'match') await search().fill('Person 00');
          if (d.prepare === 'none') await search().fill('nobody');
          if (d.prepare === 'expanded' || (d.action==='checkbox' && d.name==='Person 00' && d.checked)) await page.getByRole('button',{name:/^Show more people/}).click();
          if (d.action==='checkbox' && !d.checked) assert.equal(await page.getByRole('checkbox',{name:d.name,exact:true}).isChecked(),true);
          let target = d.action==='search' ? search() : d.action==='checkbox' ? page.getByRole('checkbox',{name:d.name,exact:true})
            : page.getByRole('button',{name:d.action==='clear'?'Clear search':d.action==='more'?/^Show more people/:d.action==='back'?'Back to people':'Close',exact: d.action!=='more'});
          phase='primary';
          const local = await measure(page,target,{ snapshot, local:negative==='already-true'?()=>true:d.local, confirmed:d.confirmed, handlers:c.handlers,target_definition_id:c.target_definition_id },
            t=>d.action==='search'?t.fill(d.value):d.action==='checkbox'?t.setChecked(d.checked):d.escape?t.press('Escape'):t.click());
          const screenshotFile = `feedback-${report.results.length}.png`;
          await capturePresented(page,path.join(out,screenshotFile),snapshot,local);
          let otherTarget, otherSpec, otherAct;
          if (d.action==='close') {
            otherTarget=page.getByRole('button',{name:'New',exact:true});otherSpec={local:s=>s.dialog==='chat',confirmed:s=>s.search!==null};otherAct=t=>t.click();
          } else if (d.action==='back') {
            otherTarget=search();otherSpec={local:s=>s.search==='independent',confirmed:s=>s.search==='independent'&&s.checkboxes.length===0};otherAct=t=>t.fill('independent');
          } else {
            otherTarget=page.getByRole('textbox',{name:'Group name',exact:true});const value='Independent '+c.ordinal;
            otherSpec={local:new Function('s','return s.groupName==='+JSON.stringify(value)),confirmed:new Function('s','return s.groupName==='+JSON.stringify(value))};otherAct=t=>t.fill(value);
          }
          phase='independent';
          const other=await measure(page,otherTarget,{snapshot,...otherSpec,handlers:c.other_handlers,target_definition_id:c.other_definition_id},otherAct);
          await capturePresented(page,path.join(out,`feedback-${report.results.length}-other.png`),snapshot,other);
          await finishMeasurement(page,local);await finishMeasurement(page,other);
          const backend_witness=await page.evaluate(()=>({mode:fixture.mode,requests:fixture.requests,pending:fixture.pending,failed:fixture.failed,config:fixture.config}));
          phase='backend';checkBackend(backend_witness,backend);
          assert.deepEqual(errors,[], 'C1 runtime error');
          report.results.push({case_id:c.case_id,target_id:c.target_id,dimensions:c.dimensions,status:'pass',local,other,backend_witness,
            confirmation_kind:'local-ui-result'});
        } catch (e) { e.message=phase+': '+e.message;report.results.push({case_id:c.case_id,status:'fail',reason:e.message});throw e; }
      }
      await page.close();
    }
    report.summary=checkReport(manifest,report,name=>fs.readFileSync(path.join(out,name)));
  } catch (e) { report.errors.push(e.message);throw e; }
  finally {
    report.cleanup={browser_closed:false,server_closed:false};
    const cleanupErrors=[];
    try { await browser?.close();report.cleanup.browser_closed=!browser||!browser.isConnected(); }
    catch (e) { cleanupErrors.push('browser cleanup: '+e.message); }
    try { await new Promise((resolve,reject)=>server.close(e=>e?reject(e):resolve()));report.cleanup.server_closed=true; }
    catch (e) { cleanupErrors.push('server cleanup: '+e.message); }
    report.errors.push(...cleanupErrors);
    report.terminal_status=report.errors.length?'failed':'passed';
    fs.writeFileSync(path.join(out,'report.json'),JSON.stringify(report,null,2));
    if (cleanupErrors.length) throw Error(cleanupErrors.join('; '));
  }
  console.log(JSON.stringify({summary:report.summary,binding,artifact_directory:out}));
}

module.exports = { run, definitions, snapshot };
