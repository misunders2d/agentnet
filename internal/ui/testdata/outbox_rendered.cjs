// Actual canonical Comic over an inert public native Host API. Lists are not
// reimplemented here: production navigation, rows, editors and draft resume run
// unchanged. Files live in the public workspace-state bag, as across remounts.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http'), assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const assets = path.resolve(process.env.AGENTNET_STATIC_ROOT || path.join(__dirname, '../static'));
const manifest = JSON.parse(fs.readFileSync(path.join(assets,'skins/comic/skin.json'),'utf8'));
const bundleSHA256 = require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(assets,'skins/comic',manifest.entry))).digest('hex');
const evidence = process.env.AGENTNET_SCREENSHOTS;
assert(evidence, 'private screenshot directory required');
fs.mkdirSync(evidence, {recursive:true, mode:0o700});
const id = n => n.toString(16).padStart(32, '0');
const at = '2026-10-20T09:00:00Z', due = n => new Date(Date.now() + (n + 2) * 3600000).toISOString();
const keys = {local:'11111111-11111111-11111111-11111111', host:'22222222-22222222-22222222-22222222', peer:'33333333-33333333-33333333-33333333'};
const ids = {human:'c'.repeat(64), group:'d'.repeat(64), pi:'e'.repeat(32), claude:'f'.repeat(32), topic:id(101), groupTopic:id(102), piAgent:id(201), claudeAgent:id(202), pid:id(301)};
const local = 'atlas/laptop', agentHost = 'atlas/host';
const me = {person:'atlas', label:'Atlas', address:local, fingerprint:keys.local, state:'self', devices:[{address:local, fingerprint:keys.local}, {address:agentHost, fingerprint:keys.host}]};
const peer = {person:'briar', label:'Briar', address:'briar/desktop', fingerprint:keys.peer, state:'pinned', devices:[{address:'briar/desktop', fingerprint:keys.peer}]};
const topic = (topicID, title, extra={}) => ({id:topicID, title, last:title, last_at:at, count:1, unread:0, review:0, running:0, waiting:false, state:'active', quiet_since:at, ...extra});
const humanTopics = [topic(ids.topic, 'Order notes', {conv:ids.human})], groupTopics = [topic(ids.groupTopic, 'Launch files', {conv:ids.group})];
const named = (agentID, label, harness) => ({record:{v:1, id:agentID, host:agentHost, host_key:keys.host, label, harness, ts:1}, enabled:true});
const catalogs = [named(ids.piAgent, 'Pi', 'pi'), named(ids.claudeAgent, 'Claude', 'claude')];
const helper = {pid:ids.pid, agent_id:ids.piAgent, host:{...me, address:agentHost, fingerprint:keys.host}, inviter:me, label:'Pi', state:'active', state_text:'Active', can_ask:true, can_decide:false, can_dismiss:false, host_here:false, shared:[], tasks_from:[], missing:0, held:0, invited:at};
const message = (mid, body, extra={}) => ({id:mid, lid:mid, dir:'in', from:peer.address, kind:'message', body, at, state:'stored', actions:[], ...extra});
const human = {id:ids.human, kind:'dm', title:'Briar orders', peer, role:'member', mine:true, created:at, members:[me, peer], agents:[helper], guests:[], topics:humanTopics, messages:[message(id(401), 'Original order context', {topic:ids.topic})]};
const group = {id:ids.group, kind:'group', title:'Launch team', peer:{person:'team', label:'Launch team', address:'', state:''}, role:'member', mine:true, created:at, members:[{...me, admin:true}, peer], agents:[], guests:[], topics:groupTopics, messages:[message(id(402), 'Original launch context', {topic:ids.groupTopic})]};
const agentSummary = (threadID, agentID, title) => topic(threadID, title, {peer:agentHost, agent_host:agentHost, agent_key:keys.host, agent_id:agentID});
const piSummary = agentSummary(ids.pi, ids.piAgent, 'Pi research'), claudeSummary = agentSummary(ids.claude, ids.claudeAgent, 'Claude notes');
const agentThread = summary => ({id:summary.id, peer:agentHost, key:{pinned:keys.host}, approved:true, task_grant:'', topic:summary, messages:[message(summary.id, summary.title+' original answer', {from:agentHost, to:local, from_key:keys.host, agent_id:summary.agent_id, kind:'answer', author:{label:summary.agent_id===ids.piAgent?'Pi':'Claude'}})]});
const audience = {persons:[me.person, peer.person], pids:[ids.pid]};
const schedules = Array.from({length:20}, (_, n) => ({id:id(501+n), conv:n%2?ids.group:ids.human, topic:n%2?ids.groupTopic:ids.topic, body:'Scheduled '+String(n).padStart(2,'0')+' — retained order and file context', reply_to:id(n%2?402:401), quote:id(n%2?402:401), due:due(n), timezone:'Europe/Kyiv', rev:7, state:n===18?'needs_review':'pending', audience:n%2?{persons:audience.persons,pids:[]}:audience, files:[{name:'scheduled-image-'+String(n).padStart(2,'0')+'.png', size:65536, sha256:'a'.repeat(64)}]}));
const history = Array.from({length:3}, (_, n) => ({...schedules[n], id:id(601+n), body:'History '+n+' kept separately', state:n===2?'sent':'cancelled'}));
const reminders = Array.from({length:20}, (_, n) => ({message:id(701+n), conv:n%2?ids.group:ids.human, from:peer.address, title:'Reminder '+String(n).padStart(2,'0')+' — original personal follow-up', due:n<3?new Date(Date.now()-3600000).toISOString():due(n), overdue:n<3}));
const summary = dm => ({id:dm.id, kind:dm.kind, title:dm.title, peer:dm.peer, role:'member', created:at, mine:true, count:1, unread:0, held:0, waiting:0, guests:0, decide:0, last:dm.messages[0].body, last_at:at});
const overview = {version:'fixture', seq:1, topic_list:true, schedules:true, remind:true, me:{address:local, fingerprint:keys.local, agent:false}, person:me, people:[peer], persons:true, agents:true, role:'person', agent_devices:[agentHost], threads:[piSummary, claudeSummary], topics:[{peer:agentHost, agent_host:agentHost, agent_key:keys.host, agent_id:ids.piAgent, total:1, archived:0, archived_unread:0, latest:piSummary}, {peer:agentHost, agent_host:agentHost, agent_key:keys.host, agent_id:ids.claudeAgent, total:1, archived:0, archived_unread:0, latest:claudeSummary}], dms:[summary(human), summary(group)], review:[], links:[], quarantine:[], reminders, directory:{current:true, members:[{address:local,presence:'connected'},{address:agentHost,presence:'connected'},{address:peer.address,presence:'connected'}]}, files:{max_count:8,max_file:1048576,max_message:2097152}};

async function bootFixture(seed) {
  const {ids, local, agentHost, keys} = seed;
  const humanFile = new File(['The original human file bytes'], 'human-file-only.txt', {type:'text/plain'});
  const agentFile = new File(['The original Claude file bytes'], 'claude-file-only.txt', {type:'text/plain'});
  const stagedFile = (file, key) => ({key, name:file.name, size:file.size, type:file.type, file});
  const drafts = {
    [ids.human]:{text:'Unsent order draft for Pi', topic:ids.topic, agent:ids.pid},
    [ids.group]:{text:'', topic:ids.groupTopic, files:[stagedFile(humanFile, 'human-original')]},
    [ids.pi]:{text:'Unsent Pi research draft'},
    [ids.claude]:{text:'', files:[stagedFile(agentFile, 'agent-original')]},
  };
  const workspace = {id:'default', name:'Private Outbox fixture', endpoint:location.origin, address:local, realm:'', handle:'outbox-exact-handle', state:'enrolled'};
  const bag = {comicDrafts:{handle:workspace.handle, drafts, open:null, dm:null, thread:null, tab:'chats'}};
  window.fixture = {...structuredClone(seed), requests:[], stages:0, fileReads:0, reminderPushes:0, originals:{humanFile, agentFile}, bag};
  let changed;
  const host = {
    version:1, platform:'daemon', workspace,
    workspaces:{list:()=>[workspace], active:()=>workspace.id, has:id=>id===workspace.id, select(){throw Error('Unexpected workspace switch');}, onChange(){return()=>{};}, state:id=>{if(id!==workspace.id)throw Error('Wrong workspace state');return bag;}},
    skins:[], onSkinsChange(){return()=>{};}, onOpen(){}, listen(fn){changed=fn;return()=>{};},
    stage(){fixture.stages++;throw Error('Browsing Outbox must not stage files');},
    file(){fixture.fileReads++;throw Error('Outbox lists must not read attachment bytes');},
    api:async(p,body)=>{
      fixture.requests.push({path:p, body:structuredClone(body)});
      const u = new URL(p, location.origin), conv=u.searchParams.get('id');
      if(body!==undefined){
        if(p==='/api/remind/done'){
          if(Object.keys(body).length!==1||!fixture.overview.reminders.some(r=>r.message===body.id&&r.overdue))throw Error('Done must name one current overdue reminder');
          fixture.overview.reminders=fixture.overview.reminders.filter(r=>r.message!==body.id);
          return new Promise(resolve=>{
            // The ordinary daemon push can arrive before its action response.
            // Keep the response pending so the test proves the badge follows it.
            fixture.finishReminder=()=>{delete fixture.finishReminder;resolve({});};
            fixture.reminderPushes++;changed({type:'change',seq:++fixture.overview.seq});
          });
        }
        // Ordinary explicit conversation opens refresh their retained receipts.
        // This is a lookup, never a send, decision, schedule or file stage.
        if(p==='/api/refresh'){
          if(![ids.human,ids.group,ids.pi,ids.claude].includes(body.id))throw Error('Refreshed another conversation');
          return {};
        }
        if(p!=='/api/schedule/edit')throw Error('Unexpected Outbox write '+p);
        const row=fixture.schedules.find(r=>r.id===body.id);
        if(!row || row.rev!==body.rev)throw Error('The scheduled message changed; review it again');
        if(body.conv!==row.conv || body.topic!==row.topic)throw Error('Edit changed original conversation');
        Object.assign(row, body, {rev:row.rev+1});
        return structuredClone(row);
      }
      if(u.pathname==='/api/overview')return structuredClone(fixture.overview);
      if(u.pathname==='/api/schedules')return structuredClone(u.searchParams.get('all')==='1'?[...fixture.schedules,...fixture.history]:fixture.schedules);
      if(u.pathname==='/api/schedule/audience')return structuredClone(seed.audience);
      if(u.pathname==='/api/dm'){if(conv!==ids.human&&conv!==ids.group)throw Error('Unexpected DM '+conv);return structuredClone(conv===ids.human?fixture.human:fixture.group);}
      if(u.pathname==='/api/thread'){if(conv!==ids.pi&&conv!==ids.claude)throw Error('Unexpected concrete thread '+conv);return structuredClone(conv===ids.pi?fixture.piThread:fixture.claudeThread);}
      if(u.pathname==='/api/agents'){const remote=u.searchParams.get('host');return {host:remote||local, local:!remote, agents:remote===agentHost?structuredClone(seed.catalogs):[], harnesses:[]};}
      if(u.pathname==='/api/responder')return {chosen:true,manual:true,ready:false,harnesses:[]};
      if(u.pathname==='/api/approvals')return {questions:[],tasks:[],own_devices:seed.me.devices.map(d=>({...d,questions:'own',tasks:'own'}))};
      if(u.pathname==='/api/groups/invitations')return [];
      if(u.pathname==='/api/teams')return {teams:[]};
      if(u.pathname==='/api/typing/status')return {send:false,scopes:[]};
      if(u.pathname==='/api/topics'){
        const rows=u.searchParams.get('conv')===ids.human?seed.human.topics:u.searchParams.get('conv')===ids.group?seed.group.topics:[seed.piThread.topic,seed.claudeThread.topic].filter(t=>['agent_host','agent_key','agent_id'].every(k=>!u.searchParams.has(k)||t[k]===u.searchParams.get(k)));
        return {topics:structuredClone(rows),matched:rows.length};
      }
      throw Error('Unexpected Outbox read '+p);
    },
  };
  fixture.pushChangedSchedule = () => {
    const row=fixture.schedules[0];row.body='Host changed Scheduled 00 while editing';row.rev++;
    row.due=new Date(new Date(row.due).getTime()+7200000).toISOString();
    fixture.schedules.reverse();
    changed({type:'change', seq:++fixture.overview.seq});
  };
  const base='/assets/skins/comic/', manifest=await(await fetch(base+'skin.json')).json();
  if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
  const shadow=document.querySelector('#skin').attachShadow({mode:'open'}), css=new CSSStyleSheet();
  css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
  const root=document.createElement('div');root.className='skin-root';shadow.append(root);
  const mod=await import(base+manifest.entry);await mod.mount(root,host);
  fixture.unmount=()=>mod.unmount(root);window.ready=true;
}
const seed={ids,local,agentHost,keys,me,peer,catalogs,human,group,piThread:agentThread(piSummary),claudeThread:agentThread(claudeSummary),overview,schedules,history,audience};
const boot='await ('+bootFixture.toString()+')('+JSON.stringify(seed)+');';
const server=http.createServer((req,res)=>{
  const u=new URL(req.url,'http://fixture');
  const send=(data,type)=>{res.setHeader('Content-Type',type);res.end(data);};
  if(u.pathname==='/')return send('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>','text/html');
  if(u.pathname==='/boot.mjs')return send(boot,'text/javascript');
  if(u.pathname.startsWith('/assets/')){
    const file=path.resolve(assets,'.'+u.pathname.slice(7));
    if(file.startsWith(assets+path.sep)&&fs.existsSync(file)&&fs.statSync(file).isFile())return send(fs.readFileSync(file),file.endsWith('.mjs')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.json')?'application/json':file.endsWith('.woff2')?'font/woff2':'application/octet-stream');
  }
  res.statusCode=404;res.end('Outbox fixture route missing');
});
const settle=async page=>{
  await page.evaluate(()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))));
  await page.evaluate(async()=>{await document.fonts.ready;const root=document.querySelector('#skin').shadowRoot.querySelector('.skin-root');await Promise.all(root.getAnimations({subtree:true}).filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));});
};
(async()=>{
  let browser,current;const results=[];
  try{
    await new Promise(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+server.address().port;
    browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM||'/usr/bin/chromium'});
    for(const width of [1280,390]){
      const context=await browser.newContext({viewport:{width,height:900},timezoneId:'Europe/Kyiv',locale:'en-US',reducedMotion:'reduce'}),page=await context.newPage(),errors=[];
      current=page;page.setDefaultTimeout(8000);page.on('pageerror',e=>errors.push(e.stack));
      await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
      await page.goto(origin);await page.waitForFunction(()=>window.ready);
      const nav=page.getByRole('navigation',{name:'Main',exact:true});
      const posts=()=>page.evaluate(()=>fixture.requests.filter(r=>r.body!==undefined&&r.path!=='/api/refresh'));
      const fit=async scope=>{
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'document fits viewport');
        for(const row of await scope.all()){const b=await row.boundingBox();assert(b&&b.x>=-1&&b.x+b.width<=width+1,'Outbox row fits viewport '+JSON.stringify(b));}
      };
      const shot=async name=>{await settle(page);const f=path.join(evidence,'comic-outbox-'+name+'-'+width+'.png');await page.screenshot({path:f});return f;};
      await nav.getByRole('button',{name:/^Outbox\b/}).waitFor();
      const dueTab=nav.getByRole('button',{name:'Outbox, 3 reminders due',exact:true});await dueTab.waitFor();
      assert.equal(await dueTab.locator('span[aria-hidden="true"]').innerText(),'3','due reminders remain visible from main navigation');
      assert.equal(await page.getByRole('heading',{name:/^Reminders\b/}).count(),0,'personal reminders are absent from Chats');
      assert.equal(await page.getByText(/^Reminder 00 —/).count(),0);
      if(width===390){
        assert.equal(await nav.getByRole('button').count(),5,'phone retains five main tabs');
        const boxes=await nav.getByRole('button').evaluateAll(ns=>ns.map(n=>{const b=n.getBoundingClientRect();return{x:b.x,right:b.right,width:b.width,height:b.height};}));
        assert(boxes.every((b,i)=>b.height>=44&&b.width>=48&&b.x>=0&&b.right<=width+1&&(!i||b.x>=boxes[i-1].right-1)),'five phone tabs fit without overlap');
      }
      await nav.getByRole('button',{name:/^Outbox\b/}).click();
      let outbox=page.getByRole('region',{name:'Outbox',exact:true});await outbox.getByRole('heading',{name:'Outbox',exact:true}).waitFor();
      const sections=()=>outbox.getByRole('navigation',{name:'Outbox',exact:true});
      const reminderTab=()=>sections().getByRole('button',{name:/^Reminders\b/});
      await reminderTab().getByText('3 due',{exact:true}).waitFor();
      assert.equal(await reminderTab().getAttribute('aria-label'),'Reminders, 3 reminders due','active Scheduled pane identifies the attention section');
      assert.equal(await nav.getByRole('button',{name:'Outbox, 3 reminders due',exact:true}).count(),1,'section and rail share the same attention count');
      assert.equal(await sections().getByRole('button',{name:'Drafts',exact:true}).locator('[aria-hidden=\"true\"]').count(),0,'drafts do not invent unread attention');
      await reminderTab().focus();await page.keyboard.press('Enter');
      await outbox.getByRole('region',{name:/^Reminders\b/}).waitFor();
      await sections().getByRole('button',{name:'Scheduled',exact:true}).focus();await page.keyboard.press('Enter');
      await settle(page);
      assert.equal(await page.getByRole('dialog').count(),0,'Outbox is a page, not a chat modal');
      const bounds=await outbox.boundingBox();assert(bounds.width>(width===1280?700:330),'Outbox uses the available page width');
      const active=()=>outbox.getByRole('region',{name:'Active scheduled messages',exact:true});
      await active().getByRole('article').last().waitFor();assert.equal(await active().getByRole('article').count(),20);
      assert.equal(await outbox.getByRole('textbox',{name:'Scheduled message text',exact:true}).count(),0,'twenty rows do not mount twenty editors');
      assert.equal(await outbox.locator('img').count(),0,'attachment metadata does not decode scheduled images');
      const heights=await active().getByRole('article').evaluateAll(ns=>ns.map(n=>n.getBoundingClientRect().height));assert(heights.every(h=>h>60&&h<160),'scheduled rows remain compact');
      await fit(active().getByRole('article'));await shot('scheduled');
      assert.equal(await outbox.getByRole('region',{name:'Scheduled message history',exact:true}).count(),0,'history is deferred');
      assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')&&new URL(r.path,location.origin).searchParams.get('all')==='1').length),0,'initial list does not fetch history');
      await outbox.getByRole('button',{name:'Show history',exact:true}).click();
      await outbox.getByRole('region',{name:'Scheduled message history',exact:true}).getByRole('article').last().waitFor();
      assert.equal(await outbox.getByRole('region',{name:'Scheduled message history',exact:true}).getByRole('article').count(),3);
      assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')&&new URL(r.path,location.origin).searchParams.get('all')==='1').length),1,'history fetch follows explicit expansion');
      await outbox.getByRole('button',{name:'Hide history',exact:true}).click();
      assert.equal(await outbox.getByRole('region',{name:'Scheduled message history',exact:true}).count(),0);
      const search=outbox.getByRole('searchbox',{name:'Find scheduled messages',exact:true});
      await search.fill('Scheduled 19');assert.equal(await active().getByRole('article').count(),1);
      await search.fill('scheduled-image-07.png');assert.equal(await active().getByRole('article').count(),1,'attachment names are searchable');
      await search.fill('no such scheduled message');await outbox.getByText('No pending messages match your search.',{exact:true}).waitFor();
      await search.fill('');assert.equal(await active().getByRole('article').count(),20);assert.deepEqual(await posts(),[]);
      assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>/^\/api\/(dm|thread)\?/.test(r.path)||r.path.startsWith('/api/schedule/audience')).length),0,'listing schedules performs no conversation or audience fetch per row');
      assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).some(r=>new URL(r.path,location.origin).searchParams.get('conv'))),false,'Outbox reads host-wide schedules across chats');

      const original=await page.evaluate(()=>structuredClone(fixture.schedules[0]));
      let editing=active().getByRole('article').filter({hasText:'Scheduled 00 —'});
      await editing.getByRole('button',{name:'Edit schedule',exact:true}).click();
      let field=outbox.getByRole('textbox',{name:'Scheduled message text',exact:true});await field.fill('My exact edit survives a newer host revision');
      const chosenDate=new Date(Date.now()+7*86400000).toISOString().slice(0,10),chosenTime='14:37';
      await outbox.getByLabel('Reschedule date',{exact:true}).fill(chosenDate);await outbox.getByLabel('Reschedule time',{exact:true}).fill(chosenTime);
      await field.focus();await field.evaluate(e=>{window.editNode=e;e.setSelectionRange(3,13,'forward');});
      assert.equal(await field.evaluate(e=>e.getRootNode().activeElement===e),true,'editor is actually focused before the push');
      const listReads=await page.evaluate(()=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).length);
      await page.evaluate(()=>fixture.pushChangedSchedule());
      await page.waitForFunction(count=>fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).length>count,listReads);
      await settle(page);
      assert.notEqual(await page.evaluate(()=>new URL(fixture.requests.filter(r=>r.path.startsWith('/api/schedules?')).at(-1).path,location.origin).searchParams.get('all')),'1','push does not fetch hidden history');
      assert.equal(await outbox.getByRole('textbox',{name:'Scheduled message text',exact:true}).count(),1,'only the selected edit is mounted');
      assert.deepEqual(await field.evaluate(e=>({same:e===window.editNode,focused:e.getRootNode().activeElement===e,text:e.value,start:e.selectionStart,end:e.selectionEnd})),{same:true,focused:true,text:'My exact edit survives a newer host revision',start:3,end:13});
      assert.equal(await outbox.getByLabel('Reschedule date',{exact:true}).inputValue(),chosenDate);assert.equal(await outbox.getByLabel('Reschedule time',{exact:true}).inputValue(),chosenTime);
      assert.equal(await editing.locator('time').first().getAttribute('datetime'),original.due,'opened details keep their captured row');
      await fit(editing);await shot('captured-edit');
      await editing.getByRole('button',{name:'Save schedule changes',exact:true}).click();
      await outbox.getByRole('status').filter({hasText:'revision changed'}).waitFor();
      let edits=(await posts()).filter(r=>r.path==='/api/schedule/edit');assert.equal(edits.length,1,'stale edit never retries automatically');
      const expectedDue=await page.evaluate(({date,time})=>new Date(date+'T'+time).toISOString(),{date:chosenDate,time:chosenTime});
      const expected={id:original.id,rev:original.rev,conv:original.conv,topic:original.topic,body:'My exact edit survives a newer host revision',reply_to:original.reply_to,quote:original.quote,due:expectedDue,timezone:'Europe/Kiev'};
      assert.deepEqual({...edits[0].body,timezone:'Europe/Kiev'},expected,'native edit retains exact ID/revision/scope/time');
      assert(['Europe/Kiev','Europe/Kyiv'].includes(edits[0].body.timezone));
      assert.equal(Object.hasOwn(edits[0].body,'files'),false);assert.equal(Object.hasOwn(edits[0].body,'audience'),false);
      assert.deepEqual(await page.evaluate(id=>fixture.schedules.find(r=>r.id===id).files,original.id),original.files);
      assert.deepEqual(await page.evaluate(id=>fixture.schedules.find(r=>r.id===id).audience,original.id),original.audience);
      editing=active().getByRole('article').filter({hasText:'Host changed Scheduled 00 while editing'});
      await editing.getByRole('button',{name:'Edit schedule',exact:true}).click();field=outbox.getByRole('textbox',{name:'Scheduled message text',exact:true});
      await field.fill('Reviewed exact Outbox edit');await outbox.getByLabel('Reschedule date',{exact:true}).fill(chosenDate);await outbox.getByLabel('Reschedule time',{exact:true}).fill(chosenTime);
      await editing.getByRole('button',{name:'Save schedule changes',exact:true}).click();
      await active().getByRole('article').filter({hasText:'Reviewed exact Outbox edit'}).waitFor();
      edits=(await posts()).filter(r=>r.path==='/api/schedule/edit');assert.equal(edits.length,2);
      assert.deepEqual({...edits[1].body,timezone:'Europe/Kiev'},{...expected,rev:original.rev+1,body:'Reviewed exact Outbox edit'});
      const saved=await page.evaluate(id=>fixture.schedules.find(r=>r.id===id),original.id);assert.deepEqual(saved.files,original.files);assert.deepEqual(saved.audience,original.audience);

      await sections().getByRole('button',{name:/^Reminders\b/}).click();
      const personal=outbox.getByRole('region',{name:/^Reminders\b/});await personal.getByRole('listitem').last().waitFor();
      assert.equal(await personal.getByRole('listitem').count(),20);assert.equal(await personal.locator('img').count(),0);
      const reminderHeights=await personal.getByRole('listitem').evaluateAll(ns=>ns.map(n=>n.getBoundingClientRect().height));assert(reminderHeights.every(h=>h>50&&h<130),'twenty reminders use compact rows');
      await fit(personal.getByRole('listitem'));await shot('reminders');
      const reminderSearch=personal.getByRole('searchbox',{name:'Find reminders',exact:true});await reminderSearch.fill('Reminder 19');assert.equal(await personal.getByRole('listitem').count(),1);await reminderSearch.fill('Briar');assert.equal(await personal.getByRole('listitem').count(),20);await reminderSearch.fill('no matching reminder');await personal.getByText('No reminders match your search.',{exact:true}).waitFor();await reminderSearch.fill('');
      for(let n=0;n<3;n++){
        const remaining=2-n;
        await personal.getByRole('listitem').filter({hasText:'Reminder '+String(n).padStart(2,'0')+' —'}).getByRole('button',{name:/^Reminder done:/}).click();
        await page.waitForFunction(()=>typeof fixture.finishReminder==='function');
        const tab=nav.getByRole('button',{name:remaining?'Outbox, '+remaining+' '+(remaining===1?'reminder':'reminders')+' due':'Outbox',exact:true});await tab.waitFor();
        if(remaining)assert.equal(await tab.locator('span[aria-hidden="true"]').innerText(),String(remaining));
        else assert.equal(await tab.locator('span[aria-hidden="true"]').count(),0,'last Done clears the badge');
        assert.equal(await personal.getByRole('listitem').count(),19-n,'each exact Done removes only its reminder');
        assert.equal(await reminderTab().getAttribute('aria-label'),remaining?'Reminders, '+remaining+' '+(remaining===1?'reminder':'reminders')+' due':null,'tab count follows the same committed snapshot as the rail');
        assert.equal(await page.evaluate(()=>typeof fixture.finishReminder),'function','badge updates through push while action response is pending');
        await page.evaluate(()=>fixture.finishReminder());await settle(page);
      }
      const completed=(await posts()).filter(r=>r.path==='/api/remind/done');
      assert.deepEqual(completed.map(r=>r.body),[0,1,2].map(n=>({id:id(701+n)})),'Done uses each exact reminder ID once');
      assert.equal(await page.evaluate(()=>fixture.reminderPushes),3);assert.equal(await personal.getByRole('listitem').count(),17);
      assert.equal((await posts()).filter(r=>!['/api/schedule/edit','/api/remind/done'].includes(r.path)).length,0,'Done never dispatches messages or work');
      await shot('reminders-cleared');

      await sections().getByRole('button',{name:'Drafts',exact:true}).click();
      let drafts=outbox.getByRole('region',{name:'Drafts',exact:true});await drafts.getByRole('listitem').last().waitFor();assert.equal(await drafts.getByRole('listitem').count(),4);
      assert.equal(await drafts.locator('img').count(),0);await fit(drafts.getByRole('listitem'));await shot('drafts');
      const draftSearch=drafts.getByRole('searchbox',{name:'Search drafts',exact:true});await draftSearch.fill('claude-file-only.txt');assert.equal(await drafts.getByRole('listitem').count(),1);await draftSearch.fill('Unsent order draft');assert.equal(await drafts.getByRole('listitem').count(),1);await draftSearch.fill('no matching draft');await drafts.getByText('No drafts match your search.',{exact:true}).waitFor();await draftSearch.fill('');
      const beforeResume=await posts();
      const cases=[{id:ids.human,match:'Unsent order draft for Pi',text:'Unsent order draft for Pi',topic:'Order notes',agent:'Pi'}, {id:ids.group,match:'human-file-only.txt',text:'',topic:'Launch files',file:'human-file-only.txt'}, {id:ids.pi,match:'Unsent Pi research draft',text:'Unsent Pi research draft',agent:'Pi'}, {id:ids.claude,match:'claude-file-only.txt',text:'',agent:'Claude',file:'claude-file-only.txt'}];
      for(const d of cases){
        await drafts.getByRole('listitem').filter({hasText:d.match}).getByRole('button').click();
        const composer=page.locator('form[data-conv="'+d.id+'"]');await composer.waitFor();await settle(page);
        assert.equal(await composer.getByRole('textbox').inputValue(),d.text,'draft text resumes in original conversation');
        if(d.file)await composer.getByText(d.file,{exact:true}).waitFor();
        const conversation=page.locator('[data-part="conversation"]').filter({has:composer});
        if(d.topic)await conversation.getByText(d.topic,{exact:true}).first().waitFor();
        if(d.agent)assert.match(await conversation.innerText(),new RegExp(d.agent),'concrete addressed agent is retained');
        assert.deepEqual(await posts(),beforeResume,'opening a draft never posts or dispatches work');
        if(width===390){await page.getByRole('button',{name:'Back to chats',exact:true}).click();await settle(page);}
        await nav.getByRole('button',{name:/^Outbox\b/}).click();outbox=page.getByRole('region',{name:'Outbox',exact:true});await outbox.getByRole('heading',{name:'Outbox',exact:true}).waitFor();
        await sections().getByRole('button',{name:'Drafts',exact:true}).click();drafts=outbox.getByRole('region',{name:'Drafts',exact:true});await drafts.getByRole('listitem').last().waitFor();assert.equal(await drafts.getByRole('listitem').count(),4,'resume retains every stored draft');
      }
      const state=await page.evaluate(async()=>{
        await fixture.unmount();const d=fixture.bag.comicDrafts.drafts;
        const refresh=fixture.requests.filter(r=>r.path==='/api/refresh').map(r=>r.body.id);
        return {human:{text:d[fixture.ids.human].text,topic:d[fixture.ids.human].topic,agent:d[fixture.ids.human].agent},groupTopic:d[fixture.ids.group].topic,piText:d[fixture.ids.pi].text,claudeText:d[fixture.ids.claude].text,humanFile:d[fixture.ids.group].files[0].file===fixture.originals.humanFile,agentFile:d[fixture.ids.claude].files[0].file===fixture.originals.agentFile,stages:fixture.stages,fileReads:fixture.fileReads,sendPosts:fixture.requests.filter(r=>r.body!==undefined&&!['/api/schedule/edit','/api/remind/done','/api/refresh'].includes(r.path)).length,refresh};
      });
      assert.deepEqual(state,{human:{text:'Unsent order draft for Pi',topic:ids.topic,agent:ids.pid},groupTopic:ids.groupTopic,piText:'Unsent Pi research draft',claudeText:'',humanFile:true,agentFile:true,stages:0,fileReads:0,sendPosts:0,refresh:[ids.human,ids.group,ids.pi,ids.claude]});
      assert.deepEqual(errors,[]);results.push({width,scheduled:20,history:3,reminders:20,remainingReminders:17,dueBadgeCleared:true,drafts:4,capturedStaleEdit:true,exactFiles:true});await context.close();current=null;
    }
    console.log(JSON.stringify({ok:true,bundleSHA256,results,evidence}));
  }catch(error){
    console.error(error.stack);
    if(current){await current.screenshot({path:path.join(evidence,'outbox-failure.png')}).catch(()=>{});console.error(await current.locator('#skin').evaluate(n=>n.shadowRoot?.innerText?.slice(0,12000)||n.shadowRoot?.textContent?.slice(-12000)).catch(()=>''));}
    process.exitCode=1;
  }finally{await browser?.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
})();
