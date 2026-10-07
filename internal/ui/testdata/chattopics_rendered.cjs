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
const conv='c'.repeat(64),at='2026-10-05T10:00:00Z';
const person=(label,address,key)=>({person:label.toLowerCase().padEnd(32,'a'),label,address,fingerprint:key,state:'pinned',devices:[{address,fingerprint:key}]});
const me=person('Sergey','sergey/laptop','own-key'),peer=person('Vitalii','vitalii/desktop','peer-key');me.state='self';
const topics=Array.from({length:4},(_,i)=>({id:String(i+1).repeat(32),...(kind==='agent'?{peer:peer.address}:{conv}),title:'Topic '+(i+1),last:'Topic '+(i+1)+' message',last_at:at,count:1,unread:0,review:0,running:0,waiting:false,pending:false,state:'active',quiet_since:at}));
const message=(i)=>({id:topics[i].id,lid:topics[i].id,from:peer.address,dir:'in',body:'Topic '+(i+1)+' message',kind:'message',at,state:'stored',actions:[],...(kind==='agent'?{to:me.address,author:{label:'Vitalii',about:'Fixture author'}}:{topic:topics[i].id})});
const main={id:'a'.repeat(32),lid:'a'.repeat(32),from:peer.address,dir:'in',body:'Quick main flow message',kind:'message',at,state:'stored',actions:[]};
let thread=kind==='agent'?{id:topics[0].id,peer:peer.address,key:{pinned:'peer-key'},approved:false,task_grant:'',topic:topics[0],messages:[message(0)]}:{id:conv,kind:kind==='group'?'group':'dm',title:kind==='group'?'Team launch':'',peer:kind==='group'?{label:'Team launch',address:'',state:''}:peer,role:'member',members:[{...me,admin:true},peer],frozen:'',agents:[],guests:[],topics,messages:[main,...topics.map((_,i)=>message(i))]};
const overview={version:'fixture',topic_list:true,seq:1,me:{address:me.address,fingerprint:me.fingerprint},person:me,persons:true,agents:true,files:false,controls:false,role:'person',people:[peer],review:[],links:[],reminders:[],threads:kind==='agent'?topics:[],topics:kind==='agent'?[{peer:peer.address,total:4,archived:0,archived_unread:0,latest:topics[0]}]:[],dms:kind==='agent'?[]:[{id:conv,kind:thread.kind,title:thread.title,peer:thread.peer,count:5,unread:0}],directory:{current:true,members:[{address:me.address,presence:'connected'},{address:peer.address,presence:'connected'}]},quarantine:[]};
window.fixture={overview,thread,requests:[],topics};let open,changed;
const host={version:1,platform:'daemon',workspace:{id:'default',name:'P11 local fixture',endpoint:location.origin,address:me.address,realm:'',state:'enrolled'},workspaces:null,skins:[],onSkinsChange(){return()=>{};},onOpen(fn){open=fn;},listen(fn){changed=fn;return()=>{};},stage:async()=>{throw Error('No fixture files');},file:async()=>{throw Error('No fixture files');},api:async(p,body)=>{
 fixture.requests.push({path:p,body});const url=new URL(p,location.origin);
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?'))return structuredClone(thread);
 if(p.startsWith('/api/thread?')){const t=topics.find(t=>t.id===url.searchParams.get('id'))||topics[0];return {id:t.id,peer:peer.address,key:{pinned:'peer-key'},approved:false,task_grant:'',topic:structuredClone(t),messages:[t.id==='f'.repeat(32)?window.freshMessage:{...message(Number(t.title.slice(-1))-1)}]};}
 if(p.startsWith('/api/topics?')){const state=url.searchParams.get('state'),q=(url.searchParams.get('q')||'').toLowerCase();const rows=topics.filter(t=>(!state||t.state===state)&&t.title.toLowerCase().includes(q));return {topics:structuredClone(rows),matched:rows.length};}

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
    for (const skin of (process.env.P11_SKINS||'comic,classic,zoom').split(',')) for (const kind of ['dm', 'group', 'agent']) for (const width of [1280, 390]) {
      const tag = skin + '-' + kind + '-' + width;
      const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
      await ctx.route('**/*', r => new URL(r.request().url()).origin === origin ? r.continue() : r.abort());
      const page = await ctx.newPage(); page.setDefaultTimeout(10000);
      page.on('pageerror', e => errors.push(tag + ': ' + e.stack));
      const shot = async name => { await page.waitForTimeout(400); await page.screenshot({ path: path.join(evidence, tag + '-' + name + '.png') }); };
      const changed = () => page.evaluate(() => fixture.requests.filter(r => r.path.startsWith('/api/topic/')));
      const all = () => page.getByRole('button', { name: /^All(?: topics| \d|$)/ }).first();
      try {
        await page.goto(origin + '/?skin=' + skin + '&kind=' + kind);
        await page.waitForFunction(() => window.ready); await page.evaluate(() => openChat());
        await all().waitFor();
        if (kind !== 'agent') {
          await page.getByText('Quick main flow message', { exact: false }).first().waitFor();
          assert.equal(await page.getByText('Topic 1 message', { exact: false }).isVisible(), false, tag + ': topic is separate from main');
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': page fits');
        await page.evaluate(() => document.fonts.ready); await shot('main');

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
        await dialog.getByRole('checkbox', { name: 'Select Topic 1', exact: true }).check();
        await dialog.getByRole('checkbox', { name: 'Select Topic 2', exact: true }).check();
        await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        assert.equal(await dialog.getByRole('button', { name: 'Confirm', exact: true }).count(), 1, tag + ': one confirmation');
        await dialog.getByRole('button', { name: 'Confirm', exact: true }).click();
        await dialog.getByRole('button', { name: 'Undo', exact: true }).click();
        assert.equal((await changed()).length, 0, tag + ': Undo stops mutations');
        if (skin === 'comic') await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
        else await dialog.getByRole('button', { name: 'Mark done', exact: true }).click();
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
