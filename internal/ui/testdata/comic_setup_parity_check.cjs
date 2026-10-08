// P24: real Comic package and controls; public host with synthetic responses.
// No Google/network side effects. Backend/engine gates run in the focused Go
// suites. AGENTNET_SCREENSHOTS optionally receives each major screen.
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const fs = require('node:fs'), path=require('node:path');
const T = { timeout: 15000 }, shots = process.env.AGENTNET_SCREENSHOTS || '';

(async () => {
  if (shots) fs.mkdirSync(shots, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    for(const skin of (process.env.AGENTNET_SETUP_SKINS||'comic').split(',')) for (const platform of ['daemon', 'browser']) for (const [width, height, colorScheme] of [[1440, 900, 'light'], [390, 844, 'dark']]) {
      const name = `${skin==='comic'?'':skin+'-'}${platform}-${width}-${colorScheme}`;
      const context = await browser.newContext({ viewport: { width, height }, colorScheme, ...(width < 1024 ? { isMobile: true, hasTouch: true } : {}) });
      const p = await context.newPage(), errors = [];
      p.on('pageerror', e => errors.push(String(e)));
      if(process.env.AGENTNET_TOPIC_ASSETS)await p.route('**/assets/**',r=>{const rel=new URL(r.request().url()).pathname.slice('/assets/'.length),base=path.resolve(process.env.AGENTNET_TOPIC_ASSETS),file=path.resolve(base,rel);if(!file.startsWith(base+path.sep)||!fs.existsSync(file))return r.abort();return r.fulfill({contentType:file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':'application/octet-stream',body:fs.readFileSync(file)});});
      await p.goto(process.env.PARITY_URL); // establish the demo's local cookie
      await p.route('**/p24-root', r => r.fulfill({ contentType: 'text/html', body: '<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body></body></html>' }));
      await p.goto(new URL('/p24-root', process.env.PARITY_URL).href);
      await p.evaluate(async ({ platform, skin }) => {
        const pack = '/assets/skins/'+skin+'/', realFetch = window.fetch.bind(window);
        const json = async path => { const r = await realFetch(path); if (!r.ok) throw Error(await r.text()); return r.json(); };
        const original = await json('/api/overview');
        const id = c => c.repeat(64), own = { person: id('a'), label: 'Alice', address: 'alice/laptop', state: 'self', devices: [{ address: 'alice/laptop', name: 'laptop', this: true, fingerprint: '11111111-22222222-33333333-44444444' }] };
        const bob = { person: id('b'), label: 'Bob', address: 'bob/desk', state: 'pinned' }, carol = { person: id('c'), label: 'Carol', address: 'carol/desk', state: 'pinned' };
        const overview = { ...original, role: 'unset', person: null, persons: true, groups: true, agents: false, people: [bob, carol], threads: [], topics: [], dms: [], reminders: [], links: [], link: null, quarantine: [] };
        let team = { realm_id: 'realm', id: id('d'), name: 'Dock crew', seq: 1, hash: id('e'), members: [own.person, bob.person, carol.person], managers: [own.person], archived: false, member: true, manager: true, conflict: false, listed: true };
        let nativeHookSupported=true;let catalogPhase=true;let newGroups = 0, refuseCarol = true, refuseRename = false, failTeams = false, nativeSetupState = 'needs_setup';
        const invites = [], calls = [], listeners = new Set();
        const at = new Date().toISOString();
        const dm = { id: id('1'), peer: bob, created: at, mine: true, agents: [], guests: [], messages: [{ id: id('2'), lid: id('3'), dir: 'in', from: bob.address, kind: 'message', body: 'Order notes', state: 'delivered', state_text: 'Delivered', at, verified_agent: false, attachments: [{ index: 0, name: 'notes.txt', size: 4, openable: true }] }] };
        const group = { ...dm, id: id('4'), kind: 'group', title: 'Existing crew', role: 'member', members: [{ ...own, admin: true }], messages: [{ ...dm.messages[0], attachments: [], group_ref: { lid: id('3'), author: bob.person, hash: id('e') } }] };
        const driveView = { enabled: true, configured: true, connected: true, account: 'alice@example.test', full: false, space: { name: 'Order project', folder: 'folder_1' }, grants: {}, notice: 'Google Drive files are outside AgentNet end-to-end encryption.' };
        const setup = { settings: { can_admin: true, configured: false, revision: 0, config: { enabled: false } }, runtime: platform === 'browser' ? 'browser' : 'native', draft: { config: { enabled: false, project: '', desktop_client_id: '', browser_client_id: '', browser_origins: [] }, completed: {}, existing_project: false }, steps: ['api', 'consent', 'clients'].map(id => ({ id, title: 'Check ' + id, detail: 'Fixture checklist step', commands: [] })), local: { client_id: '' } };
        function emit() { for (const f of listeners) f({ type: 'change', seq: ++overview.seq }); }
        function summaries() { overview.dms = [dm, group].map(d => ({ ...d, count: d.messages.length, title: d.title || '', last: 'Order notes', last_at: at, unread: 0, held: 0, waiting: 0, guests: 0, decide: 0 })); }
        async function api(path, body) {
          calls.push({ path, body: structuredClone(body), platform });
          const route = path.split('?')[0];
          if (route === '/api/overview') return structuredClone(overview);
          if (route === '/api/device/service') { overview.role = 'service'; return { note: 'Service role recorded' }; }
          if(route==='/api/agents'&&body?.action==='publish')return{saved:true,published:true};
          if (route === '/api/agents') return !catalogPhase?{local:true,host:'alice/laptop',agents:[]}:{ local:true,host:'alice/laptop',harnesses:[{name:'codex',found:true}],agents:[{record:{id:id('9'),label:'Fictional helper'},enabled:true,responder:{harness:'codex',dir:'/fictional/work',ready:true}}] };
          if (route === '/api/assistant-setup' && body === undefined) return { local: true, harnesses: [{ id: 'codex', label: 'Codex', detected: nativeHookSupported, supported: nativeHookSupported, configured: nativeHookSupported && nativeSetupState !== 'needs_setup', registered: nativeHookSupported && nativeSetupState === 'connected', state: nativeSetupState, note: 'Synthetic native setup status.' }] };
          if (route === '/api/teams') { if (failTeams) throw Error('Server unavailable'); return { status: 'available', current: true, at, truncated: false, teams: [structuredClone(team)] }; }
          if (route === '/api/team') {
            if (body.op === 'leave' && team.managers.length === 1) throw Error('The last manager cannot leave.');
            if (body.op === 'rename' && refuseRename) throw Error('Server refused rename.');
            if (body.op === 'rename' || body.op === 'create') team.name = body.name;
            if (body.op === 'create') team.id = id('f');
            if (body.op === 'manager-add') team.managers.push(body.target);
            if (body.op === 'manager-remove') team.managers = team.managers.filter(x => x !== body.target);
            if (body.op === 'remove') team.members = team.members.filter(x => x !== body.target);
            if (body.op === 'archive' || body.op === 'restore') team.archived = body.op === 'archive';
            if (body.op === 'leave' || body.op === 'join') team.member = body.op === 'join';
            return structuredClone(team);
          }
          if (route === '/api/teams/snapshot') {
            return { realm_id: 'realm', sources: [{ id: team.id, seq: 1, hash: id('e') }], persons: [own, bob, carol].map(p => ({ id: p.person, seq: 1, hash: id('e') })), at: Math.floor(Date.now() / 1000) };
          }
          if (route === '/api/groups/new') { newGroups++; summaries(); return { id: group.id }; }
          if (route === '/api/groups/invitations') return structuredClone(invites);
          if (route === '/api/groups/invite') {
            if (body.person === carol.person && refuseCarol) { refuseCarol = false; throw Error('Carol unavailable; retry later.'); }
            const result = { id: String(invites.length), conv: body.conv, target: body.person, title: group.title, direction: 'out', status: 'pending', history: body.history.refs || [] };
            invites.push(result); return result;
          }
          if (route === '/api/dm') return structuredClone(new URL(path, location.origin).searchParams.get('id') === dm.id ? dm : group);
          if (route === '/api/drive/setup') { if (body?.action === 'save-draft') setup.draft = body.draft; return structuredClone(setup); }
          if (route === '/api/typing') return { participants: [], status: 'available' };
          if (route === '/api/act') return { note: 'Recorded' };
          return body === undefined ? json(path) : { note: 'Recorded' };
        }
        const provider = {
          async drive(r) { calls.push({ drive: structuredClone(r), platform }); return r.action === 'list' ? { ...structuredClone(driveView), page: { files: [], incompleteSearch: true } } : structuredClone(driveView); },
          async driveUpload(conv, file, confirm) { if (!confirm) throw Error('Missing Google confirmation'); calls.push({ upload: { conv, name: file.name, size: file.size, confirm }, platform }); return { id: 'copy' }; },
        };
        if (platform === 'browser') {
          provider.prepareGoogle = async () => { calls.push({ google: 'prepare', platform }); };
          provider.beginGoogleConsent = async r => { calls.push({ google: 'consent', request: r, gesture: navigator.userActivation.isActive, platform }); return structuredClone(driveView); };
        }
        const outer = document.createElement('div'); outer.style.height = '100dvh'; document.body.append(outer);
        document.body.style.cssText = 'margin:0;height:100dvh;overflow:hidden';
        const shadow = outer.attachShadow({ mode: 'open' }), root = document.createElement('div'); root.style.height = '100%';
        let styleSource=await(await realFetch(pack+'style.css')).text();
        for(const match of [...styleSource.matchAll(/@import url\("(.+?)"\);/g)])styleSource=styleSource.replace(match[0],await(await realFetch(pack+match[1].replace(/^\.\//,''))).text());
        const style = new CSSStyleSheet(); style.replaceSync(styleSource); shadow.adoptedStyleSheets = [style]; shadow.append(root);
        const manifest = await json(pack + 'skin.json');
        if (manifest.document) {
          const parsed = new CSSStyleSheet(), kept = new CSSStyleSheet(); parsed.replaceSync(await (await realFetch(pack + manifest.document)).text());
          for (const rule of parsed.cssRules) {
            if (rule instanceof CSSPropertyRule) kept.insertRule(rule.cssText);
            else if (rule instanceof CSSFontFaceRule) kept.insertRule(rule.cssText.replace(/url\(\s*(["']?)([^"')]*)\1\s*\)/g, (_, q, u) => 'url("' + new URL(u, location.origin + pack + manifest.document).href + '")'));
          }
          document.adoptedStyleSheets = [kept];
        }
        let onOpen;
        const host = { version: 1, platform, api, drive: provider, workspace: { id: 'p24-' + platform, name: 'Parity fixture', address: 'alice/laptop', endpoint: location.origin, realm: 'realm', state: 'enrolled' }, workspaces: null,
          listen(f) { listeners.add(f); return () => listeners.delete(f); }, onOpen(f) { onOpen = f; }, file: async () => ({ bytes: new Uint8Array([1, 2, 3, 4]) }), stage: async () => 'stage', skins: [{ id: 'comic', name: 'Comic', api: 1, builtin: true }], onSkinsChange: () => () => {}, selectSkin() {} };
        const renderer = await import(pack + 'entry.mjs'); await renderer.mount(root, host);
        window.__p24 = { calls, invites, get newGroups() { return newGroups; }, root, listeners,
          person() { overview.role = 'person'; overview.person = own; overview.link = null; summaries(); emit(); },
          link(state) { overview.link = { state }; emit(); },
          openDM() { onOpen(dm.id, 'conversation'); },
          openGroup() { onOpen(group.id, 'conversation'); },
          refuseRename() { refuseRename = true; },
          clearInvitations() { invites.length = 0; emit(); },
          failTeams(value) { failTeams = value; emit(); },
          nativeSetup(state) { nativeSetupState = state; },
          nativeUnsupported() { nativeHookSupported=false; nativeSetupState='unsupported'; },
          clearCatalog() { catalogPhase=false; },
          async unmount() { await renderer.unmount(root); },
        };
      }, { platform, skin });
      const snap = async label => { if (shots) { await p.waitForTimeout(300); await p.screenshot({ path: `${shots}/p24-${name}-${label}.png` }); } };
      if(skin!=='comic') {
        await p.locator('#profile-btn').click();await p.locator('#settings-tab-device').click();
        const setup=p.locator('#assistant-setup');
        if(platform==='browser') {await setup.getByRole('heading',{name:'Connect an agent on your computer',exact:true}).waitFor(T);assert.equal(await p.evaluate(()=>__p24.calls.filter(c=>c.path==='/api/assistant-setup').length),0);await snap('browser-agent-setup');}
        else {
          await setup.getByRole('button',{name:'Set up your agents',exact:true}).click();await setup.getByText('Ready',{exact:true}).waitFor(T);
          await p.evaluate(()=>{__p24.nativeUnsupported();window.dispatchEvent(new Event('focus'));});
          await setup.getByText(/Unsupported: Synthetic/).waitFor({state:'attached',...T});
          assert.equal(await setup.getByRole('checkbox',{name:'Set up Codex',exact:true}).isEnabled(),true);
          assert.equal(await setup.getByRole('combobox').count(),0,'sole exact agent needs no selector');
          await setup.getByText('Agent: Fictional helper',{exact:true}).waitFor(T);
          await setup.getByRole('button',{name:'Review changes',exact:true}).click();await setup.getByRole('button',{name:'Confirm setup',exact:true}).click();await setup.getByText('Ready',{exact:true}).waitFor(T);
          assert.ok(await p.evaluate(()=>__p24.calls.filter(c=>c.path==='/api/assistant-setup').every(c=>c.body===undefined)));
          assert.equal(await p.locator('#responder').isVisible(),false,'default answers remain subordinate');await snap('hook-unsupported-ready-agent');
        }
        assert.equal(await p.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);assert.deepEqual(errors,[]);await context.close();console.log('unified agent setup',name,'PASS');continue;
      }
      const noSideways = async scope => {
        const overflowing = await scope.evaluate(el => [el, ...el.querySelectorAll('*')].filter(x => x.clientWidth > 0 && x.scrollWidth > x.clientWidth + 2 && /auto|scroll/.test(getComputedStyle(x).overflowX)).map(x => x.tagName + '.' + x.className));
        assert.deepEqual(overflowing, [], `${name}: horizontal scroll`);
      };
      const settings = () => p.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: /^You/ });
      await settings().click();
      // Profile is the initial desktop section; phone opens it from the row.
      if (width < 1024) await p.getByRole('button', { name: /You.*Set up your person/ }).click();
      await p.getByRole('button', { name: 'Set up my person', exact: true }).waitFor(T);
      const service = p.getByRole('button', { name: 'It is a service or bot…', exact: true });
      if (platform === 'daemon') {
        await p.evaluate(() => __p24.link('pending'));
        await p.getByText('Waiting for your other device to approve this one.', { exact: false }).waitFor(T);
        assert.equal(await service.count(), 0, 'pending own-device link cannot change role');
        await p.evaluate(() => __p24.link('refused'));
        await service.waitFor(T);
        await service.click();
        const confirmation = p.getByRole('alertdialog', { name: 'A service or bot' });
        await confirmation.waitFor(T); assert.match(await confirmation.textContent(), /Nobody writes personal chats/);
        assert.equal(await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/device/service').length), 0);
        await snap('service-confirm'); await confirmation.getByRole('button', { name: 'It is a service or bot', exact: true }).click();
        await p.getByText('This computer is a service or bot, so it has no person.', { exact: false }).waitFor(T);
        assert.equal(await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/device/service').length), 1);
      } else assert.equal(await service.count(), 0, 'browser cannot choose service role');
      await p.evaluate(() => __p24.person());
      // Return from phone Profile through its section header. Main nav
      // also has a You button, which keeps the current section open.
      if (width < 1024) await p.locator('.an-tab-in > .sticky').getByRole('button', { name: 'You', exact: true }).click();
      if (platform === 'daemon') {
        await p.getByRole('button', { name: /^Your agent/ }).click();
        await p.getByRole('button', { name: 'Find them', exact: true }).click();
        await p.getByText('Ready', { exact: true }).waitFor(T);
        await p.getByText('Fictional helper is set up for chats, questions and approved tasks.',{exact:true}).waitFor(T);
        await p.getByText('Agent: Fictional helper',{exact:true}).waitFor(T);assert.equal(await p.getByRole('radio',{name:'Fictional helper',exact:true}).count(),0,'sole exact agent needs no selector');
        assert.equal(await p.getByRole('radio',{name:/Codex/}).isVisible(),false,'default responder is subordinate');
        await p.getByText('Native sessions',{exact:true}).click();
        const reads = await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/assistant-setup').length);
        await p.evaluate(() => { __p24.nativeSetup('needs_activation'); window.dispatchEvent(new Event('focus')); });
        await p.getByText(/Start a new session: Set up/).waitFor(T);
        await p.getByText('Ready',{exact:true}).waitFor(T);
        assert.equal(await p.getByRole('checkbox', { name: 'Set up Codex', exact: true }).isChecked(), true, 'refresh preserves existing named agent selection');
        await p.evaluate(() => { __p24.nativeSetup('connected'); window.dispatchEvent(new Event('focus')); });
        await p.getByText(/Connected: Set up/).waitFor(T);
        await p.getByText('Ready',{exact:true}).waitFor(T);
        assert.equal(await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/assistant-setup').length), reads + 2, 'one status read per focus');
        assert.ok(await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/assistant-setup').every(c => c.body === undefined)), 'focus never configures tools');
        await snap('native-setup-refreshed');
        await p.evaluate(()=>{__p24.nativeUnsupported();window.dispatchEvent(new Event('focus'));});
        await p.getByText(/Can’t be connected on this computer yet/).waitFor(T);
        assert.equal(await p.getByRole('checkbox',{name:'Set up Codex',exact:true}).isEnabled(),true,'runnable named agent remains configurable without native hooks');
        await p.getByRole('button',{name:'Review changes',exact:true}).click();
        await p.getByRole('button',{name:'Apply changes',exact:true}).click();
        await p.getByRole('heading',{name:'Agent setup saved',exact:true}).waitFor(T);
        await p.getByText('Ready',{exact:true}).waitFor(T);
        assert.ok(await p.evaluate(()=>__p24.calls.filter(c=>c.path==='/api/assistant-setup').every(c=>c.body===undefined)),'hook-unsupported configure makes no native changes');
        await snap('hook-unsupported-ready-agent');
        if (width < 1024) await p.locator('.an-tab-in > .sticky').getByRole('button', { name: 'You', exact: true }).click();
      }
      await p.evaluate(()=>__p24.clearCatalog());
      if(process.env.AGENTNET_SETUP_ONLY){
        if(platform==='browser'){await p.getByRole('button',{name:/^Your agent/}).click();await p.getByText(/A browser can’t look for programs/).waitFor(T);await snap('browser-agent-setup');assert.equal(await p.evaluate(()=>__p24.calls.filter(c=>c.path==='/api/assistant-setup').length),0,'browser does not inspect native tools');}
        assert.deepEqual(errors,[],name+': page errors');await context.close();console.log('unified agent setup',name,'PASS');continue;
      }
      await p.getByRole('button', { name: /^People lists/ }).click();
      await p.getByRole('heading', { name: 'People lists', exact: true }).waitFor(T);
      await snap('teams');
      await p.getByRole('button', { name: /Dock crew.*3 members/ }).click();
      let team = p.getByRole('dialog', { name: 'Dock crew', exact: true });
      await team.getByRole('button', { name: 'Leave list' }).click();
      await p.getByText('The last manager cannot leave.', { exact: true }).waitFor(T);
      await team.getByRole('button', { name: 'Make manager', exact: true }).first().click();
      await p.getByRole('alertdialog').getByRole('button', { name: 'Confirm change' }).click();
      await team.getByRole('button', { name: 'Take manager role' }).waitFor(T);
      await snap('team-members'); await noSideways(team);
      await p.evaluate(() => __p24.refuseRename());
      await team.getByRole('textbox', { name: 'New name', exact: true }).fill('Refused name');
      await team.getByRole('button', { name: 'Rename list' }).click();
      await p.getByText('Server refused rename.', { exact: true }).waitFor(T);
      assert.equal(await team.getByRole('heading', { name: 'Dock crew', exact: true }).count(), 1, 'refused rename leaves verified name');
      await team.getByRole('button', { name: 'Take manager role' }).click();
      await p.getByRole('alertdialog').getByRole('button', { name: 'Confirm change' }).click();
      await p.waitForFunction(() => __p24.calls.some(c => c.path === '/api/team' && c.body.op === 'manager-remove'));
      await team.getByRole('button', { name: 'Select for a conversation…' }).click();
      let draft = p.getByRole('dialog', { name: 'New group', exact: true });
      await draft.locator('[aria-label="Selected people"]').getByText('Carol', { exact: true }).waitFor(T);
      assert.equal(await p.evaluate(() => __p24.invites.length), 0, 'snapshot sends no invitation');
      await draft.getByRole('button', { name: 'Remove Carol', exact: true }).click();
      // The wrapping label's raw text includes option names. Match the
      // select's accessible name, which excludes its own option contents.
      await draft.getByRole('combobox', { name: 'People list', exact: true }).selectOption('d'.repeat(64));
      await p.evaluate(() => __p24.failTeams(true));
      await draft.getByText(/These are the last verified people lists/).waitFor(T);
      assert.equal(await draft.getByRole('button', { name: 'Add list’s people', exact: true }).isDisabled(), true, 'stale directory cannot expand a snapshot');
      await p.evaluate(() => __p24.failTeams(false));
      await draft.getByText(/These are the last verified people lists/).waitFor({ state: 'hidden', ...T });
      await draft.getByRole('button', { name: 'Add list’s people', exact: true }).click();
      await draft.locator('[aria-label="Selected people"]').getByText('Carol', { exact: true }).waitFor(T);
      assert.equal(await draft.locator('[aria-label="Selected people"]').getByRole('button').count(), 2, 'snapshot merge excludes own person and duplicates');
      await draft.getByRole('textbox', { name: 'Group name' }).fill('Order crew');
      await snap('team-snapshot'); await noSideways(draft);
      await draft.getByRole('button', { name: 'Create group', exact: true }).click();
      await draft.getByText(/Carol unavailable; retry later/).waitFor(T);
      assert.equal(await p.evaluate(() => __p24.newGroups), 1);
      assert.deepEqual(await p.evaluate(() => __p24.invites.map(i => i.target)), ['b'.repeat(64)]);
      await draft.getByRole('button', { name: 'Retry remaining invitations' }).click();
      await draft.waitFor({ state: 'hidden', ...T });
      assert.equal(await p.evaluate(() => __p24.newGroups), 1, 'retry keeps created group');
      assert.deepEqual(await p.evaluate(() => __p24.invites.map(i => i.target)), ['b'.repeat(64), 'c'.repeat(64)], 'no duplicate successful invitation');
      if (width >= 1024) {
        assert.equal(await p.getByRole('navigation', { name: 'Main', includeHidden: true }).getByRole('button', { name: 'Chats', exact: true, includeHidden: true }).getAttribute('aria-current'), 'page', 'creating a group from Teams leaves Settings for Chats');
      }
      // New group opens Bring in. The team selection is also present there.
      const bring = p.getByRole('dialog', { name: 'Bring someone in', exact: true });
      await bring.getByRole('radio',{name:/Add as a permanent member/}).check();
      await bring.getByText('Add people from a list', { exact: true }).waitFor(T);
      await bring.getByRole('button', { name: 'Close', exact: true }).click();
      // Existing group: reviewed people can receive reviewed history too.
      await p.evaluate(() => { __p24.clearInvitations(); __p24.openGroup(); });
      await p.getByRole('button', { name: width < 1024 ? 'Bring someone in' : 'Bring in', exact: true }).click();
      const existing = p.getByRole('dialog', { name: 'Bring someone in', exact: true });
      await existing.getByRole('radio',{name:/Add as a permanent member/}).check();
      const bulk = existing.getByRole('region', { name: 'Invite team people' });
      await bulk.getByRole('combobox', { name: 'People list', exact: true }).selectOption('d'.repeat(64));
      await bulk.getByRole('button', { name: 'Add list’s people', exact: true }).click();
      await bulk.getByRole('button', { name: 'Remove Carol', exact: true }).click();
      await bulk.getByLabel('Share reviewed history with these people', { exact: true }).check();
      await snap('existing-group-history');
      await bulk.getByRole('button', { name: 'Invite selected team people' }).click();
      await p.waitForFunction(() => __p24.invites.length === 1);
      assert.deepEqual(await p.evaluate(() => __p24.calls.filter(c => c.path === '/api/groups/invite').at(-1).body), { conv: '4'.repeat(64), person: 'b'.repeat(64), history: { refs: [{ lid: '3'.repeat(64), author: 'b'.repeat(64), hash: 'e'.repeat(64) }] } });
      await existing.getByRole('button', { name: 'Close', exact: true }).click();
      await p.evaluate(() => __p24.openDM());
      await p.getByRole('button', { name: 'Save to project space', exact: true }).waitFor(T);
      await p.getByRole('button', { name: 'More', exact: true }).click();
      await p.getByRole('menuitem', { name: 'Project space', exact: true }).click();
      const space = p.getByRole('dialog', { name: 'Project space', exact: true });
      await space.getByRole('button', { name: 'Upload file', exact: true }).waitFor(T);
      await space.getByRole('button', { name: 'Upload file', exact: true }).click();
      await space.getByText('Choose file and confirm Google plaintext storage').waitFor(T);
      assert.equal(await p.evaluate(() => __p24.calls.filter(c => c.upload).length), 0);
      await space.locator('input[type=file]').setInputFiles({ name: 'local.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') });
      await space.getByLabel('Upload plaintext to Google Drive').check();
      await space.getByRole('button', { name: 'Upload file', exact: true }).click();
      await p.waitForFunction(() => __p24.calls.some(c => c.upload));
      assert.deepEqual(await p.evaluate(() => __p24.calls.find(c => c.upload).upload), { conv: '1'.repeat(64), name: 'local.txt', size: 5, confirm: true });
      await space.getByText('Google returned incomplete search results.').waitFor(T);
      await snap('drive'); await noSideways(space);
      if (platform === 'browser') {
        await space.getByRole('button', { name: 'Reconnect Google' }).click();
        await p.waitForFunction(() => __p24.calls.some(c => c.google === 'consent'));
        assert.equal(await p.evaluate(() => __p24.calls.find(c => c.google === 'consent').gesture), true, 'Google retains direct click gesture');
        assert.equal(await p.evaluate(() => __p24.calls.find(c => c.google === 'consent').request.confirm_account), true);
      }
      await space.getByRole('button', { name: 'Close', exact: true }).click();
      await p.getByRole('button', { name: 'Save to project space', exact: true }).click();
      const copy = p.getByRole('alertdialog', { name: 'Save to Google Drive?' });
      assert.match(await copy.textContent(), /outside AgentNet end-to-end encryption/);
      assert.equal(await p.evaluate(() => __p24.calls.filter(c => c.drive?.action === 'save-attachment').length), 0);
      await snap('drive-copy-confirm'); await copy.getByRole('button', { name: 'Save to Google Drive', exact: true }).click();
      await p.waitForFunction(() => __p24.calls.some(c => c.drive?.action === 'save-attachment'));
      assert.deepEqual(await p.evaluate(() => __p24.calls.find(c => c.drive?.action === 'save-attachment').drive), { conv: '1'.repeat(64), action: 'save-attachment', dir: 'in', message: '2'.repeat(64), index: 0, confirm_outside_e2ee: true });
      if (width < 1024) await p.getByRole('button', { name: /^Back to chats/ }).click();
      await settings().click(); await p.getByRole('button', { name: /^Storage/ }).click();
      const storage = p.getByRole('region', { name: 'File storage options' });
      await storage.getByRole('button', { name: 'Save local setup progress' }).waitFor(T);
      await snap('storage-setup'); await noSideways(storage);
      await storage.getByLabel('Google Cloud project ID').fill('parity-project');
      await storage.getByRole('button', { name: 'Save local setup progress' }).click();
      await p.waitForFunction(() => __p24.calls.some(c => c.path === '/api/drive/setup' && c.body?.action === 'save-draft'));
      await p.evaluate(() => __p24.unmount());
      assert.equal(await p.evaluate(() => __p24.root.childElementCount), 0, 'unmount empties skin root');
      assert.equal(await p.evaluate(() => __p24.listeners.size), 0, 'unmount releases workspace listeners');
      assert.deepEqual(errors, [], name + ': page errors');
      await context.close(); console.log('ok', name);
    }
    console.log('comic setup parity check PASS');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
