// Production skin packages over the public Host API, with inert local data.
// No relay, identity, model, private globals or external requests.
const fs = require('node:fs'), path = require('node:path'), http = require('node:http');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assets = path.resolve(process.env.AGENTNET_TOPIC_ASSETS || path.join(__dirname, '../static'));
const evidence = process.env.AGENTNET_SCREENSHOTS;
assert(evidence, 'AGENTNET_SCREENSHOTS must be outside the repository');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const boot = `
const kind=new URL(location).searchParams.get('kind'),skin=new URL(location).searchParams.get('skin');
const pendingMode=${!!process.env.P11_PENDING_ONLY},importMode=${!!process.env.P11_HISTORY_IMPORT_ONLY};
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(label,address,key)=>({person:label.toLowerCase().padEnd(32,'a'),label,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const me=person('Sergey','sergey/laptop','own-key'),peer=person('Vitalii','vitalii/desktop','peer-key');me.state='self';
const topics=Array.from({length:4},(_,i)=>({id:String(i+1).repeat(32),...(kind==='agent'?{peer:peer.address}:{conv}),title:'Topic '+(i+1),last:'Topic '+(i+1)+' message',last_at:at,count:1,unread:0,review:0,running:0,waiting:false,pending:false,state:'active',quiet_since:at}));
const message=(i)=>({id:topics[i].id,lid:topics[i].id,from:peer.address,dir:'in',body:'Topic '+(i+1)+' message',kind:'message',at,state:'stored',actions:[],...(kind==='agent'?{to:me.address,author:{label:'Vitalii',about:'Fixture author'}}:{topic:topics[i].id})});
const main={id:'a'.repeat(32),lid:'a'.repeat(32),from:peer.address,dir:'in',body:'Quick main flow message',kind:'message',at,state:'stored',actions:[]};
let thread=kind==='agent'?{id:topics[0].id,peer:peer.address,key:{pinned:'peer-key'},approved:false,task_grant:'',topic:topics[0],messages:[message(0)]}:{id:conv,kind:kind==='group'?'group':'dm',title:kind==='group'?'Team launch':'',peer:kind==='group'?{label:'Team launch',address:'',state:''}:peer,role:'member',members:[{...me,admin:true},peer],frozen:'',agents:[],guests:[],topics,messages:[main,...topics.map((_,i)=>message(i))]};
// Pending: requests this person sent to the peer's agent. Neither has a current
// executor word (one none, one stale), so the topic is unconfirmed, not Waiting;
// the DM copy still waits for the peer's program, which the relay lists offline.
const asked=m=>({...m,dir:'out',from:me.address,...(kind==='agent'?{to:peer.address}:{})});
const update={to:peer.address,state:'waiting',detail:'peer_update: named agent capability unavailable: '+peer.address+' cannot synchronize direct-agent history or resolve own requests yet; update all its active AgentNet sessions'};
if(pendingMode){topics[0].pending=true;topics[0].waiting=false;topics[0].unconfirmed=2;topics[0].pending_ids=[topics[0].id,'b'.repeat(32)];thread.messages=[...(kind==='agent'?[]:[main]),asked({...message(0),kind:'task',body:'Earlier unresolved task',target:{address:peer.address,fingerprint:peer.fingerprint},...(kind==='agent'?{}:{copies:[update]})}),asked({...message(0),id:'b'.repeat(32),lid:'b'.repeat(32),kind:'question',body:'Second pending question',exec:{state:'needs_human',host:peer.address,attempt:1,stale:true},target:{address:peer.address,fingerprint:peer.fingerprint}}),{...message(0),id:'d'.repeat(32),lid:'d'.repeat(32),kind:'answer',body:'Unrelated final answer'}];}
const overview={version:'fixture',topic_list:true,seq:1,me:{address:me.address,fingerprint:me.fingerprint},person:me,persons:true,agents:true,files:false,controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:kind==='agent'?topics:[],topics:kind==='agent'?[{peer:peer.address,total:4,archived:0,archived_unread:0,latest:topics[0]}]:[],dms:kind==='agent'?[]:[{id:conv,kind:thread.kind,title:thread.title,peer:thread.peer,count:5,unread:0}],directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[]};
if(pendingMode)overview.directory.members[1].presence='offline';
let destination;
if(importMode){
 main.kind='task';main.attachments=[{index:0,name:'terms.md',size:19,openable:true},{index:1,name:'unavailable.md',size:11,openable:false}];
 overview.files={max_file:1048576,max_message:2097152,max_count:8};
 destination={id:'d'.repeat(64),kind:'group',title:'Launch review',peer:{label:'Launch review',address:'',state:''},role:'member',members:[me,peer],frozen:'',agents:[],guests:[],topics:[{id:'5'.repeat(32),title:'Launch notes',state:'active',count:0}],messages:[]};
 overview.dms.push({id:destination.id,kind:'group',title:destination.title,peer:destination.peer,count:0,unread:0},{id:'e'.repeat(64),kind:'group',title:'Frozen archive',peer:{label:'Frozen archive',address:'',state:''},frozen:'frozen',count:0});
}
window.fixture={overview,thread,requests:[],topics,destination,previews:0,shares:{}};let open,changed;

window.resolvePending=()=>{topics[0].pending_ids=[];topics[0].pending=topics[0].waiting=false;changed?.({type:'change',seq:++overview.seq});};
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P11 local fixture',endpoint:location.origin,address:me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('No fixture files');},file:async()=>{throw Error('No fixture files');},api:async(p,body)=>{
 fixture.requests.push({path:p,body});const url=new URL(p,location.origin);
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?'))return structuredClone(importMode&&url.searchParams.get('id')===destination.id?destination:thread);
 if(p.startsWith('/api/thread?')){if(pendingMode)return structuredClone(thread);const t=topics.find(t=>t.id===url.searchParams.get('id'))||topics[0];return {id:t.id,peer:peer.address,key:{pinned:'peer-key'},approved:false,task_grant:'',topic:structuredClone(t),messages:[t.id==='f'.repeat(32)?window.freshMessage:{...message(Number(t.title.slice(-1))-1)}]};}
 if(p.startsWith('/api/topics?')){const state=url.searchParams.get('state'),q=(url.searchParams.get('q')||'').toLowerCase();const rows=topics.filter(t=>(!state||t.state===state)&&t.title.toLowerCase().includes(q));return {topics:structuredClone(rows),matched:rows.length};}
 if(importMode&&p==='/api/history/contribution/preview'){
  const operation=String(++fixture.previews+5).repeat(32),selected=new Set((body.files||[]).map(f=>f.id+':'+f.index));
  const items=body.ids.map(id=>{const m=thread.messages.find(m=>m.id===id);return {id:m.id,lid:m.lid,author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64),from:m.from,sent_at:at,body:m.body,files:(m.attachments||[]).map((f,i)=>({index:i,name:f.name,size:f.size,sha256:'9'.repeat(64),available:!!f.openable,selected:selected.has(m.id+':'+i)}))};});
  return {...body,operation,token:'7'.repeat(64),imported_at:at,body:['History from '+(kind==='dm'?'Vitalii chat':'Team launch'),'Vitalii: Quick main flow message','Vitalii: Topic 1 message'].join(String.fromCharCode(10,10)),items,audience:[{person:me.person,address:me.address,fingerprint:me.fingerprint,label:'Sergey',role:'member'},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii',role:'member'},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii’s Analyst',role:'agent',agent_id:'1'.repeat(32),pid:'a'.repeat(32)},{person:peer.person,address:peer.address,fingerprint:peer.fingerprint,label:'Vitalii’s Reviewer',role:'agent',agent_id:'2'.repeat(32),pid:'b'.repeat(32)}]};
 }
 if(importMode&&p==='/api/history/contribution/apply'){
  if(fixture.rejectStale){fixture.rejectStale=false;throw Error('The selected history or destination audience changed; review the contribution again.');}
  if(!fixture.shares[body.operation]){fixture.shares[body.operation]=1;destination.messages.push({id:body.operation,lid:body.operation,dir:'out',from:me.address,kind:'message',body:body.body,topic:body.topic,at,state:'queued'});}
  if(fixture.loseSuccess){fixture.loseSuccess=false;throw Error('Synthetic lost success response; retry this exact share');}
  return {id:body.operation,lid:body.operation,state:'queued'};
 }
 if(p==='/api/topic/organization/preview')return {...body,operation:String(++fixture.previews+5).repeat(32),token:'7'.repeat(64),moves:body.ids.map(id=>({lid:thread.messages.find(m=>m.id===id).lid,author:'11111111-22222222-33333333-44444444',hash:'8'.repeat(64),topic:thread.messages.find(m=>m.id===id).topic||''}))};
 if(p==='/api/topic/organization/apply'){if(fixture.rejectOrganizationStale){fixture.rejectOrganizationStale=false;throw Error('The selected messages, topic or audience changed; review the move again.');}for(const id of body.ids){const m=thread.messages.find(m=>m.id===id);m.topic=body.topic;}changed?.({type:'change',seq:++overview.seq});return {id:body.operation,lid:body.operation,state:'queued'};}

 if(p==='/api/dm/send'||p==='/api/send'){
  const id='f'.repeat(32),lid='e'.repeat(32),t={id, ...(kind==='agent'?{peer:peer.address}:{conv}),title:body.body,last:body.body,last_at:at,count:1,unread:0,review:0,running:0,waiting:false,pending:false,state:'active',quiet_since:at};
  topics.push(t);const m={...main,id:kind==='agent'?id:lid,lid,from:me.address,dir:'out',body:body.body,topic:kind==='agent'?'':id,...(kind==='agent'?{author:{label:'You',about:'Fixture sender'}}:{})};
  if(kind==='agent'){window.freshMessage=m;overview.threads=topics;}else{thread.messages.push(m);thread.topics=topics;}
  changed?.({type:'change',seq:++overview.seq});return {id:m.id,lid:m.lid,state:'delivered'};
 }
 if(p.startsWith('/api/topic/')){const what=url.pathname.split('/').at(-1),ids=body.ids||[body.id];for(const id of ids){const t=topics.find(t=>t.id===id);if(what==='create'){topics.push({id,conv,title:'Quick main flow message',last:'Quick main flow message',last_at:at,count:1,unread:0,state:'active',pending:false});for(const m of thread.messages)if(m.lid===id)m.topic=id;}else if(t){if(what==='delete'){topics.splice(topics.indexOf(t),1);if(kind!=='agent')thread.messages=thread.messages.filter(m=>m.topic!==id);}else t.state=what==='done'?'done':what==='archive'?'archived':'active';}}if(kind==='agent')overview.threads=topics;else thread.topics=topics;changed?.({type:'change',seq:++overview.seq});return {note:ids.length+' topics changed.'};}
 if(p.includes('/groups/invitations'))return [];if(p.startsWith('/api/typing/status'))return {send:false,scopes:[]};if(p.startsWith('/api/agents'))return {host:peer.address,agents:[],sessions:[],local:false};return {};
}};
const base='/assets/skins/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.document)).text());document.adoptedStyleSheets=[css];}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),css=new CSSStyleSheet();css.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[css];
const root=document.createElement('div');root.className='skin-root';shadow.append(root);await(await import(base+manifest.entry)).mount(root,host);
window.openChat=()=>open(kind==='agent'?topics[0].id:conv,kind==='agent'?'message':'conversation');window.ready=true;
`;
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://127.0.0.1');
  if (u.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>'); return; }
  if (u.pathname === '/boot.mjs') { res.setHeader('Content-Type', 'text/javascript'); res.end(boot); return; }
  if (u.pathname.startsWith('/assets/')) {
    const file = path.resolve(assets, '.' + u.pathname.slice(7));
    if (file.startsWith(assets + path.sep) && fs.existsSync(file) && fs.statSync(file).isFile()) {
      res.setHeader('Content-Type', file.endsWith('.mjs') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.json') ? 'application/json' : 'application/octet-stream');
      res.end(fs.readFileSync(file)); return;
    }
  }
  res.statusCode = 404; res.end('No fixture route');
});
(async () => {
  let browser;
  const errors = [];let combinations=0;
  try {
    await new Promise(r => server.listen(0, '127.0.0.1', r));
    const origin = 'http://127.0.0.1:' + server.address().port;
    browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
    for (const skin of (process.env.P11_SKINS||'comic,classic,zoom').split(',')) for (const kind of ((process.env.P11_ORGANIZE_ONLY||process.env.P11_HISTORY_IMPORT_ONLY)?['dm','group']:process.env.P11_PENDING_ONLY?['group','agent']:['dm', 'group', 'agent'])) for (const width of [1280, 390]) {
      const tag = skin + '-' + kind + '-' + width;
      const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
      await ctx.route('**/*', r => new URL(r.request().url()).origin === origin ? r.continue() : r.abort());
      const page = await ctx.newPage(); page.setDefaultTimeout(10000);
      page.on('pageerror', e => errors.push(tag + ': ' + e.stack));
      const shot = async name => { await page.waitForTimeout(400); await page.screenshot({ path: path.join(evidence, tag + '-' + name + '.png') }); };
      const changed = () => page.evaluate(() => fixture.requests.filter(r => r.path.startsWith('/api/topic/')));
      const all = () => page.getByRole('button', { name: process.env.P11_HISTORY_IMPORT_ONLY ? /^All topics/ : /^All(?: topics| \d|$)/ }).first();
      try {
        await page.goto(origin + '/?skin=' + skin + '&kind=' + kind);
        await page.waitForFunction(() => window.ready); await page.evaluate(() => openChat());
        await all().waitFor();
        if (kind !== 'agent' && !process.env.P11_PENDING_ONLY) {
          await page.getByText('Quick main flow message', { exact: false }).first().waitFor();
          assert.equal(await page.getByText('Topic 1 message', { exact: false }).isVisible(), false, tag + ': topic is separate from main');
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': page fits');
        await page.evaluate(() => document.fonts.ready); await shot('main');

        if(process.env.P11_HISTORY_IMPORT_ONLY){
          const original=await page.evaluate(()=>JSON.stringify(fixture.thread.messages));
          const mainRow=page.locator('[data-mid="'+ 'a'.repeat(32)+'"]');
          if(width>600){await mainRow.hover();await mainRow.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=mainRow.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.press('Enter');await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByText('1 message selected',{exact:true}).waitFor();
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          await page.locator('[data-mid="'+ '1'.repeat(32)+'"]').click();await page.getByText('2 messages selected',{exact:true}).waitFor();
          const launch=async()=>{await page.getByRole('button',{name:'Continue in a group',exact:true}).click();return page.getByRole('dialog',{name:'Continue in a group',exact:true});};
          const choose=async sheet=>{const select=sheet.getByRole('combobox',{name:'Destination group',exact:true});assert.equal(await select.getByRole('option',{name:'Frozen archive',exact:true}).count(),0);await select.selectOption('d'.repeat(64));await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('5'.repeat(32));assert(await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).isChecked());assert(await sheet.getByRole('checkbox',{name:/unavailable.md/}).isDisabled());};
          let sheet=await launch();await choose(sheet);await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await sheet.getByText('Share with “Launch review”',{exact:true}).waitFor();
          const audience=sheet.getByRole('region',{name:'Reviewed audience',exact:true});assert.match(await audience.innerText(),/Sergey.*member/s);assert.match(await audience.innerText(),/Vitalii’s Analyst.*agent/s);assert.match(await audience.innerText(),/Vitalii’s Reviewer.*agent/s);assert.equal(await audience.getByRole('listitem').count(),4,'Distinct same-host agents both remain in reviewed audience');
          assert.match(await sheet.innerText(),/terms.md.*included/s);await shot('history-review');
          await sheet.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/history/contribution/apply').length),0,tag+': cancel shares nothing');
          sheet=await launch();await choose(sheet);await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).uncheck();await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await page.evaluate(()=>fixture.rejectStale=true);await sheet.getByRole('button',{name:'Share selected history',exact:true}).click();await sheet.getByRole('alert').getByText(/review the contribution again/).waitFor();
          await sheet.getByRole('button',{name:'Review history and audience',exact:true}).waitFor();assert.equal(await sheet.getByRole('button',{name:'Retry this share',exact:true}).count(),0,'Canonical stale rejection permits a fresh review');
          await sheet.getByRole('checkbox',{name:'terms.md',exact:true}).check();await sheet.getByRole('button',{name:'Review history and audience',exact:true}).click();
          await page.evaluate(()=>fixture.loseSuccess=true);await sheet.getByRole('button',{name:'Share selected history',exact:true}).click();await sheet.getByRole('alert').getByText(/lost success response/).waitFor();
          assert.equal(await sheet.getByRole('button',{name:'Change selection or destination',exact:true}).count(),0,'Uncertain success retains immutable exact review');await shot('history-uncertain');
          await sheet.getByRole('button',{name:'Retry this share',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const sent=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/history/contribution/apply'));
          assert.equal(sent.length,3);assert.deepEqual(sent[1].body,sent[2].body,'Lost-success retry preserves exact review.operation/token/items/audience');assert.notEqual(sent[0].body.operation,sent[1].body.operation,'Stale rejection obtains a new reviewed operation');
          assert.deepEqual(sent[1].body.ids,['a'.repeat(32),'1'.repeat(32)]);assert.equal(sent[1].body.source,'c'.repeat(64));assert.equal(sent[1].body.destination,'d'.repeat(64));assert.equal(sent[1].body.topic,'5'.repeat(32));assert.deepEqual(sent[0].body.files,[]);assert.deepEqual(sent[1].body.files,[{id:'a'.repeat(32),index:0}]);
          assert.equal(await page.evaluate(()=>JSON.stringify(fixture.thread.messages)),original,'Original tasks/messages/files remain unchanged');assert.deepEqual(await page.evaluate(()=>Object.values(fixture.shares)),[1],'Lost response creates one attributed context message');
          assert.equal(await page.evaluate(()=>fixture.destination.messages[0].kind),'message','Old task contributes context, never executable work');assert.equal(await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'||r.path==='/api/send'||r.path==='/api/act'&&r.body?.do!=='read').length),0,'No old task is sent, accepted or rerun');
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,tag+': reviewed sharing fits narrow view');combinations++;console.log('HISTORY IMPORT PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_ORGANIZE_ONLY){
          const mainRow=page.locator('[data-mid="'+ 'a'.repeat(32)+'"]');
          if(width>600){await mainRow.hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Select',exact:true}).click();}
          else{const actions=page.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Select',exact:true}).click();}
          await page.getByText('1 message selected',{exact:true}).waitFor();
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          await page.locator('[data-mid="'+ '1'.repeat(32)+'"]').click();
          await page.getByText('2 messages selected',{exact:true}).waitFor();
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();
          let sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          await sheet.getByRole('textbox',{name:'New topic name',exact:true}).fill('Reviewed launch history');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();
          await sheet.getByText('Move 2 messages to “Reviewed launch history”?',{exact:true}).waitFor();
          await shot('organization-review');await sheet.getByRole('button',{name:'Cancel',exact:true}).click();
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply'))).length,0,tag+': cancel applies nothing');
          await page.getByRole('button',{name:'Move to topic',exact:true}).click();sheet=page.getByRole('dialog',{name:'Move selected messages',exact:true});
          await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();
          await page.evaluate(()=>fixture.rejectOrganizationStale=true);await sheet.getByRole('button',{name:'Move 2 messages',exact:true}).click();
          await sheet.getByRole('alert').getByText(/review the move again/).waitFor();await sheet.getByRole('button',{name:'Review move',exact:true}).waitFor();
          assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='a'.repeat(32)).topic||''),'','Stale reviewed move leaves original main flow intact');
          assert.equal(await sheet.getByRole('button',{name:'Retry this move',exact:true}).count(),0,'Deterministic stale move requires fresh review');
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await sheet.getByRole('button',{name:'Move 2 messages',exact:true}).click();
          await sheet.waitFor({state:'hidden'});
          const moved=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply'));assert.notEqual(moved[0].body.operation,moved[1].body.operation,'Fresh reviewed move captures a new operation');const sent=moved.at(-1);
          assert.deepEqual(sent.body.ids,['a'.repeat(32),'1'.repeat(32)],tag+': exact cross-topic selection');assert.equal(sent.body.topic,'2'.repeat(32));
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/dm/send'))).length,0,tag+': originals never resent');
          assert.equal(await page.evaluate(()=>fixture.thread.messages.find(m=>m.id==='3'.repeat(32)).topic),'3'.repeat(32),tag+': unselected message unchanged');
          await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 3(?:\b)/}).click();
          await page.getByRole('button',{name:/^Topic 3.*open topic, menu$/}).click();
          await page.getByRole('menuitem',{name:'Merge into another topic…',exact:true}).click();
          sheet=page.getByRole('dialog',{name:'Merge this topic',exact:true});await sheet.getByRole('combobox',{name:'Destination topic',exact:true}).selectOption('2'.repeat(32));
          await sheet.getByRole('button',{name:'Review move',exact:true}).click();await sheet.getByRole('button',{name:'Move 1 message',exact:true}).click();await sheet.waitFor({state:'hidden'});
          const merged=await page.evaluate(()=>fixture.requests.filter(r=>r.path==='/api/topic/organization/apply').at(-1));
          assert.deepEqual(merged.body.ids,['3'.repeat(32)]);assert.equal(merged.body.merge,'3'.repeat(32));
          assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,tag+': organization fits narrow view');
          combinations++;console.log('ORGANIZE PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_PENDING_ONLY){
          if(kind!=='agent'){
            await all().click();await page.getByRole('dialog',{name:'All topics',exact:true}).getByRole('button',{name:/^Topic 1(?:\b)/}).click();
          }
          const current=()=>page.locator('[data-topic="'+ '1'.repeat(32)+'"][aria-current="true"]');
          const label=String(await current().getAttribute('aria-label'));
          assert(label.includes('No result')&&!label.includes('Waiting'),tag+': pending with no current executor word is not shown as Waiting: '+label);
          await current().click();
          await page.getByRole('menuitem',{name:'Show pending requests (2)',exact:true}).click();
          const pending=page.getByRole('dialog',{name:'Pending requests',exact:true});
          await pending.waitFor();
          assert.equal(await pending.getByRole('button',{name:/Open pending/}).count(),2,tag+': every exact pending item listed');
          assert.equal(await pending.getByText('Unrelated final answer',{exact:true}).count(),0,tag+': unrelated reply is not a pending request');
          const text=await pending.innerText();
          assert.match(text,/No result recorded · on Vitalii’s \S+/,tag+': unknown is not invented running or done, and names its executor: '+text);
          assert.match(text,/last known/,tag+': reported stale status remains explicit');
          assert.match(text,kind==='agent'?/No result recorded · on [^\n]* · not connected now/:/No result recorded · on [^\n]* · needs an AgentNet update/,tag+': what is known of the executor: '+text);
          assert.match(text,/last known · on [^\n]* · not connected now/,tag+': the relay lists the executor offline');
          assert(!String(await pending.innerText()).includes('vitalii/desktop'),tag+': friendly host replaces raw address footer');
          await shot('pending-list');
          await pending.getByRole('button',{name:/Open pending question 2 Second pending question/}).click();
          await pending.waitFor({state:'hidden'});
          await page.waitForFunction(()=>document.querySelector('#skin').shadowRoot.querySelector('[data-mid="'+ 'b'.repeat(32)+'"]')?.getAnimations().length>0);
          assert.equal((await page.evaluate(()=>fixture.requests.filter(r=>r.body&&r.path!='/api/refresh'&&!(r.path==='/api/act'&&r.body.do==='read')))).length,0,tag+': navigation sends no grants/retries/cancel/topic decisions');
          await page.evaluate(()=>resolvePending());
          await page.waitForTimeout(100);
          await current().click();
          assert.equal(await page.getByRole('menuitem',{name:/Show pending requests/}).count(),0,tag+': refreshed exact final state removes pending navigation');
          combinations++;console.log('PENDING PASS '+tag);await ctx.close();continue;
        }

        if(process.env.P11_FLOW_ONLY){
          if(kind!=='agent'){
            await all().click();const list=page.getByRole('dialog',{name:'All topics',exact:true});
            await list.getByRole('button',{name:/^Topic 1(?:\b)/}).click();
            await page.getByText('Topic 1 message',{exact:false}).first().waitFor();
            assert.equal(await page.getByText('Quick main flow message',{exact:false}).first().isVisible(),false,tag+': selected flow is separate');
            await page.getByRole('button',{name:/^(Main|Main flow)$/}).click();
            await page.getByText('Quick main flow message',{exact:false}).first().waitFor();
            if(skin==='comic'){
              if(width>600){await page.getByText('Quick main flow message',{exact:false}).first().hover();await page.getByRole('button',{name:'More actions',exact:true}).click();await page.getByRole('menuitem',{name:'Make a topic',exact:true}).click();}
              else{const actions=page.getByRole('button',{name:'Message actions',exact:true});await actions.focus();await actions.click();await page.getByRole('button',{name:'Make a topic',exact:true}).click();}
            }else if(skin==='zoom'){
              await page.getByRole('button',{name:/Quick main flow message/}).click();await page.getByLabel('Message actions',{exact:true}).click();await page.getByRole('button',{name:'Make a topic',exact:true}).click();
            }else await page.getByRole('button',{name:'Make a topic',exact:true}).click();
            await page.waitForFunction(()=>fixture.requests.some(r=>r.path==='/api/topic/create'));
            await shot('promoted-flow');
            await page.getByRole('button',{name:/^(Main|Main flow)$/}).click();
            const mainRow=page.locator(skin==='comic'?'[data-mid="'+ 'a'.repeat(32)+'"]':skin==='classic'?'.timeline .body':'.zoom-message .body, .mini-chat .mc-text').filter({hasText:'Quick main flow message'});
            await mainRow.first().waitFor({state:'hidden'});
            assert.equal(await mainRow.first().isVisible(),false,tag+': promoted message leaves main flow');
          }
          await page.getByRole('button',{name:'New topic',exact:true}).click();
          await page.locator('textarea:visible').last().fill('Brand new topic message');
          await page.getByRole('button',{name:/^(Send\b|Ask )/}).click();
          await page.locator('text=Brand new topic message >> visible=true').first().waitFor();
          if(skin!=='comic')await page.locator('.topics-bar .topic-chip[aria-pressed=true]').waitFor();
          const sent=await page.evaluate(()=>fixture.requests.find(r=>r.path==='/api/dm/send'||r.path==='/api/send'));
          assert(sent,tag+': send through Host API');
          if(kind!=='agent'){assert.equal(sent.body.topic,'new');assert.equal(await page.getByText('Quick main flow message',{exact:false}).first().isVisible(),false,tag+': new topic opens after sending');}else assert(!sent.body.reply_to,tag+': new agent topic has no parent');
          await shot('new-flow');combinations++;console.log('FLOW PASS '+tag);await ctx.close();continue;
        }
        await all().click();
        let dialog = page.getByRole('dialog', { name: 'All topics', exact: true });
        // Owner (v0.8.17): names and Archive (and in agent chats Done) follow the person's linked devices.
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).waitFor();
        assert.match(await dialog.innerText(), kind === 'agent' ? /Names, Done and Archive sync across your linked devices\./ : /Names and Archive sync across your linked devices\./, tag + ': All topics says which changes follow linked devices');
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 2', exact: true }).check();
        await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        assert.equal(await dialog.getByRole('button', { name: 'Confirm', exact: true }).count(), 1, tag + ': one confirmation');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await dialog.getByRole('button', { name: 'Undo', exact: true }).click();
        assert.equal((await changed()).length, 0, tag + ': Undo stops mutations');
        if (skin === 'comic') await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        else await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        // Owner (v0.8.17): the confirmation says where Done applies, like the footer.
        const doneAsk = await dialog.innerText();
        assert.match(doneAsk, kind === 'agent' ? /Mark these topics done on your linked devices\?|Mark done 2 topics\? On your linked devices\./ : /Mark these topics done for everyone\?|Mark done 2 topics\? Shared with everyone\./, tag + ': Mark done says where it applies');
        assert.doesNotMatch(doneAsk, /on this device/i, tag + ': Mark done is not called this device\'s');
        await shot('bulk-confirm');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/done'), null, { timeout: 12000 });
        let req = (await changed()).at(-1);
        assert.equal(req.body.ids.length, 2, tag + ': one batch');
        assert.deepEqual(req.body.counts, Object.fromEntries(req.body.ids.map(id=>[id,1])),tag+': displayed counts preserved');
        assert.equal(kind === 'agent' ? req.body.peer : req.body.conv, kind === 'agent' ? 'vitalii/desktop' : 'c'.repeat(64), tag + ': scope');
        await dialog.getByRole('button', { name: /^Done(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).waitFor();
        await shot('done');
        await dialog.getByRole('button', { name: /^(Close|Back to the conversation)$/ }).click();
        await all().click(); dialog = page.getByRole('dialog', { name: 'All topics', exact: true });
        await dialog.getByRole('button', { name: /^Active(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 4', exact: true }).check();
        await dialog.getByRole('button', { name: 'Archive', exact: true }).click();
        const archiveAsk = await dialog.innerText();
        assert.match(archiveAsk, /Archive these topics on your linked devices\? Nothing deleted\.|Archive 2 topics\? On your linked devices\./, tag + ': Archive says where it applies');
        assert.doesNotMatch(archiveAsk, /on this device/i, tag + ': Archive is not called this device\'s');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/archive'), null, { timeout: 12000 });
        await dialog.getByRole('button', { name: /^Archived(?:\s|$)/ }).click();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).waitFor();
        await dialog.getByRole('checkbox', { name: 'Select Topic 3', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 4', exact: true }).check();
        await dialog.getByRole('button', { name: 'Delete for me', exact: true }).click();
        assert.match(await dialog.innerText(), /Others keep their copies|Other people keep their copies/);
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/topic/delete'), null, { timeout: 12000 });
        assert.deepEqual(await page.evaluate(() => fixture.topics.map(t => t.title)), ['Topic 1', 'Topic 2'], tag + ': batch delete');
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': modal fits');
        await shot('deleted');
        await dialog.getByRole('button', { name: /^(Close|Back to the conversation)$/ }).click();
        await page.getByRole('button', { name: 'New topic', exact: true }).click();
        combinations++;console.log('PASS ' + tag);
      } catch (e) { await shot('failure'); console.error(JSON.stringify({ tag, errors, requests: await page.evaluate(() => fixture.requests) })); throw e; }
      await ctx.close();
    }
    assert.deepEqual(errors, []); console.log('chat topics rendered PASS: '+combinations+' desktop/phone chat/skin combinations');
  } finally { if (browser) await browser.close(); server.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
