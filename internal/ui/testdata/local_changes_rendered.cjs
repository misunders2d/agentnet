// Real Store, Timeline and message controls. Writes are deliberately held;
// all identities/data are invented. No generated shipping assets are touched.
const assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),http=require('node:http');
const {execFileSync}=require('node:child_process'),{chromium}=require(process.env.AGENTNET_PLAYWRIGHT);
const web=path.resolve(__dirname,'../web'),work=fs.mkdtempSync(path.join(os.tmpdir(),'agentnet-local-changes-'));
const evidence=process.env.AGENTNET_LOCAL_CHANGES_EVIDENCE;
const trustOnly=process.env.AGENTNET_LOCAL_CHANGES_MODE==='trust';
const overlay=process.env.AGENTNET_LOCAL_CHANGES_OVERLAY;
const entry=`
import React from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {Timeline} from './src/features/Conversation.timeline';import {TopicBar,TopicEnd} from './src/features/Conversation.topics';
import {TrustSheet} from './src/features/Trust';
const trustOnly=${JSON.stringify(process.env.AGENTNET_LOCAL_CHANGES_MODE==='trust')};
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const conv='c'.repeat(64),topic='b'.repeat(32),count=Number(new URL(location).searchParams.get('count'))||100;
const overview={seq:1,version:'fixture',topic_list:true,me:{address:'self/laptop',fingerprint:'self-key'},person:{person:'self',label:'Me',address:'self/laptop',devices:[]},people:[],dms:[],threads:[],needs_you:[],reminders:[]};
const view={seq:1,id:conv,kind:'dm',peer:{person:'peer',address:'peer/laptop',label:'Peer'},agents:[],guests:[],topics:[{id:topic,conv,peer:'',title:'Chosen topic',count,state:'active',last_at:'2026-10-26T12:00:00Z',unread:0,review:0}],
 messages:Array.from({length:count},(_,i)=>({id:String(i+1).padStart(32,'0'),lid:String(i+1).padStart(32,'0'),dir:'out',from:'self/laptop',kind:'message',body:'Original message '+(i+1),at:new Date(1793016000000+i*60000).toISOString(),revision:1,topic,can:['edit','delete','react'],reactions:[]}))};
view.messages.at(-2).dir='in';view.messages.at(-2).from='peer/laptop';view.messages.at(-2).body='Quoted message';
view.messages.at(-1).quote=view.messages.at(-2).lid;
const f=window.fixture={writes:[],listeners:new Set(),holdReads:false,reads:[],timings:[],editTimings:{},ready:false,trustCommits:[]};
const host={platform:'daemon',workspace:{id:'local-changes',state:'enrolled'},listen(fn){f.listeners.add(fn);return()=>f.listeners.delete(fn)},async api(p,b){
 if(p.startsWith('/api/overview'))return structuredClone(overview);
 if(p.startsWith('/api/dm?')){const snapshot=structuredClone(view);if(f.holdReads)await new Promise(resolve=>f.reads.push(resolve));return snapshot;}
 if(p.startsWith('/api/message/')||p.startsWith('/api/topic/'))return new Promise((resolve,reject)=>f.writes.push({p,b:structuredClone(b),resolve,reject}));
 if(trustOnly&&p==='/api/act')return new Promise((resolve,reject)=>f.writes.push({p,b:structuredClone(b),resolve,reject}));
 if(p.startsWith('/api/agents'))return {agents:[]};if(p.startsWith('/api/typing'))return {entries:[]};return {};
}};
const store=new Store(host);f.store=store;
f.finish=(index,fail=false)=>{const w=f.writes[index];if(fail){w.reject(Error('Invented backend refusal'));return;}
 const m=view.messages.find(m=>m.id===w.b.id);if(w.p==='/api/message/edit'){m.edited=true;m.text=w.b.text;m.revision++;}
 if(w.p==='/api/message/delete'){m.deleted=true;m.revision++;}
 if(w.p==='/api/topic/rename')view.topics[0].title=w.b.title;
 else if(w.p.startsWith('/api/topic/'))view.topics[0].state=w.p.endsWith('/done')?'done':'active';
 view.seq++;w.resolve({note:'Stored'});
};
f.release=()=>{f.holdReads=false;for(const resolve of f.reads.splice(0))resolve()};
f.newer=async()=>{view.messages.at(-1).text='Newer linked-device edit';view.messages.at(-1).edited=true;view.messages.at(-1).revision+=10;view.seq++;await store.refreshOpen()};
function App(){const dm=useStore(store,s=>s.dm),tab=useStore(store,s=>s.tab),o=useStore(store,s=>s.overview);if(!dm)return <p>Opening…</p>;
 const ctx={conv,dm,thread:null,overview:o,names:{},canReply:true};
 return <div className="flex h-full flex-col"><nav><button onClick={()=>store.showTab(tab==='settings'?'chats':'settings')}>Toggle settings</button><span data-current-tab>{tab}</span></nav>
 <TopicBar dm={dm}/><Timeline ctx={ctx} messages={dm.messages} selected={null} onSelect={()=>{}} empty={null} end={<TopicEnd ctx={ctx}/>}/>
 <form data-conv={conv}><textarea aria-label="Unrelated draft" onChange={e=>store.setDraft(conv,{...store.draft(conv),text:e.target.value})}/></form></div>;
}
function TrustFixture(){
 const [open,setOpen]=React.useState(false),[thread,setThread]=React.useState({peer:'fixture/a',key:{pinned:'old-key',pending:'key-a'},messages:[]});
 f.changeTrust=(peer,pending)=>setThread({...thread,peer,key:{...thread.key,pending}});
 React.useLayoutEffect(()=>{
  if(!open)return;
  const button=[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Trust the new identity');
  const checkbox=document.querySelector('[role="dialog"] input[type="checkbox"]');
  f.trustCommits.push({peer:thread.peer,key:thread.key.pending,disabled:button?.disabled,checked:checkbox?.checked});
 },[open,thread]);
 return <><button onClick={()=>setOpen(true)}>Compare identity</button><TrustSheet open={open} onOpenChange={setOpen} thread={thread}/></>;
}
await store.start();await store.open({kind:'dm',id:conv});store.setDraft(conv,{text:'',topic});
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}>{trustOnly?<TrustFixture/>:<App/>}</StoreContext.Provider></OwnedContext.Provider>);
f.ready=true;
`;
const aliases={'optimistic.mjs':'static/optimistic.mjs','pictures.mjs':'static/pictures.mjs','person-topics.mjs':'skins/shared/person-topics.mjs','reaction-preferences.mjs':'skins/shared/reaction-preferences.mjs','collective-mentions.mjs':'skins/shared/collective-mentions.mjs','drive-space.mjs':'skins/shared/drivespace.mjs','drive-setup.mjs':'skins/shared/drivespace-setup.mjs'};
fs.cpSync(path.join(web,'src'),path.join(work,'src'),{recursive:true});
if(overlay)fs.cpSync(overlay,path.join(work,'src','skin'),{recursive:true,filter:file=>path.basename(file)!=='fonts'});
for(const [a,b] of Object.entries(aliases))fs.copyFileSync(path.resolve(web,'../',b),path.join(work,'src',a));
fs.symlinkSync(path.join(web,'node_modules'),path.join(work,'node_modules'),'dir');
const js=require(path.join(web,'node_modules/esbuild')).buildSync({stdin:{contents:entry,resolveDir:work,loader:'tsx'},bundle:true,format:'esm',target:'es2022',jsx:'automatic',define:{'process.env.NODE_ENV':'"production"'},write:false}).outputFiles[0].text;
execFileSync(process.execPath,[path.join(web,'node_modules/@tailwindcss/cli/dist/index.mjs'),'-i',path.join(work,'src/styles.css'),'-o',path.join(work,'style.css'),'--minify'],{cwd:work,stdio:'pipe'});
const css=fs.readFileSync(path.join(work,'style.css'));
const server=http.createServer((req,res)=>{const u=new URL(req.url,'http://fixture');if(u.pathname==='/entry.mjs'){res.setHeader('Content-Type','text/javascript');return res.end(js)}if(u.pathname==='/style.css'){res.setHeader('Content-Type','text/css');return res.end(css)}res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Local changes fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><style>body{margin:0}#root,#app{height:100vh}nav,form{padding:12px}button{min-height:44px}form textarea{border:1px solid;width:95%}</style><div id="root" class="an-root"><div id="app" class="an-app"></div><div id="portals"></div></div><script type="module" src="/entry.mjs"></script>')});

async function checkEditBox(page, area, originalWidth, capture) {
  const viewportWidth = page.viewportSize().width;
  const bubble = area.locator('xpath=ancestor::*[@data-message-bubble]');
  const bubbleWidth = () => bubble.evaluate(el => el.getBoundingClientRect().width);
  const scrollPixels = () => area.evaluate(el => el.scrollHeight - el.clientHeight);
  const initial = await area.evaluate(el => {
    const bubble = el.closest('[data-message-bubble]');
    const column = bubble.parentElement.parentElement;
    const box = getComputedStyle(el);
    const row = getComputedStyle(column);
    return {
      bubbleWidth: bubble.getBoundingClientRect().width,
      columnWidth: column.clientWidth - parseFloat(row.paddingLeft) - parseFloat(row.paddingRight),
      fieldWidth: el.getBoundingClientRect().width,
      contentWidth: bubble.clientWidth - parseFloat(getComputedStyle(bubble).paddingLeft) - parseFloat(getComputedStyle(bubble).paddingRight),
      height: el.offsetHeight,
      lines: (el.clientHeight - parseFloat(box.paddingTop) - parseFloat(box.paddingBottom)) / parseFloat(box.lineHeight),
    };
  });
  assert(initial.bubbleWidth > originalWidth, 'editing expands the original bubble');
  if (capture) await bubble.screenshot({path:path.join(evidence,'edit-box-initial-'+viewportWidth+'.png')});
  assert(Math.abs(initial.bubbleWidth - initial.columnWidth) <= 1,
    'short quoted edit needs the whole column: ' + JSON.stringify({originalWidth, ...initial}));
  assert(Math.abs(initial.fieldWidth - initial.contentWidth) <= 1, 'field fills the bubble content');
  assert(initial.lines >= 2.5 && initial.lines <= 3.5, 'short edit starts with three visible lines');
  await area.fill('See the reply at https://example.test/project/notes');
  assert.equal(await bubbleWidth(), initial.bubbleWidth, 'editing a short URL keeps the full width');
  assert.equal(await scrollPixels(), 0, 'short edit fits without scrolling');
  if (capture) await bubble.screenshot({path:path.join(evidence,'edit-box-short-'+viewportWidth+'.png')});
  await area.fill('First line\nSecond line\nThird line');
  assert.equal(await bubbleWidth(), initial.bubbleWidth, 'three lines keep the starting width');
  assert.equal(await scrollPixels(), 0, 'three lines fit without scrolling');
  await area.fill('Longer text can wrap naturally in the editor at the same column width. It does not need to wait for the bubble to widen first.');
  assert.equal(await bubbleWidth(), initial.bubbleWidth, 'wrapping keeps the full width');
  assert.equal(await scrollPixels(), 0, 'wrapped edit fits without scrolling');
  if (capture) await bubble.screenshot({path:path.join(evidence,'edit-box-expanded-'+viewportWidth+'.png')});
  const limit = Math.min(viewportWidth >= 1024 ? 240 : 140, page.viewportSize().height * 0.45);
  await area.fill('More room to edit\n'.repeat(viewportWidth >= 1024 ? 8 : 5).trimEnd());
  const grown = await area.evaluate(el => el.offsetHeight);
  assert(grown > initial.height && grown < limit, 'ordinary multiline edit grows below the cap');
  assert.equal(await scrollPixels(), 0, 'ordinary multiline edit fits without scrolling');
  await area.fill('A long edit scrolls inside the field\n'.repeat(100).trimEnd());
  const capped = await area.evaluate(el => {
    el.scrollTop = el.scrollHeight;
    return {height: el.offsetHeight, scroll: el.scrollTop, overflow: getComputedStyle(el).overflowY};
  });
  assert(capped.height > grown && Math.abs(capped.height - limit) <= 1, 'editor caps growth at the composer limit');
  assert(capped.overflow === 'auto' && capped.scroll > 0, 'long edit text scrolls inside the field');
  if (capture) await bubble.screenshot({path:path.join(evidence,'edit-box-capped-'+viewportWidth+'.png')});
  const viewport = page.viewportSize();
  await page.setViewportSize({...viewport, height:280});
  const compactHeight = await area.evaluate(el => el.offsetHeight);
  assert(Math.abs(compactHeight - 126) <= 1, 'short windows cap the field at 45% of viewport height');
  await page.setViewportSize(viewport);
  return {originalWidth, bubbleWidth: initial.bubbleWidth, fieldWidth: initial.fieldWidth, columnWidth: initial.columnWidth, initialHeight: initial.height, grownHeight: grown, cappedHeight: capped.height, compactHeight, scrollable: true};
}

function measureEdit(page, id, event, selector, condition = 'visible', key) {
  return page.evaluate(({id,event,selector,condition,key}) => {
    const capture = input => {
      if (key && input.key !== key) return;
      document.removeEventListener(event,capture,true);
      const record = fixture.editTimings[id] = {start:performance.now()};
      const check = () => {
        const el = document.querySelector(selector);
        const ready = condition === 'closed' ? !el : condition === 'newline' ? el?.value.endsWith('\n') : el?.checkVisibility();
        if (ready) {
          record.readyFrameMs = performance.now() - record.start;
          requestAnimationFrame(() => { record.presentedUpperMs = performance.now() - record.start; });
        } else if (performance.now() - record.start < 1100) requestAnimationFrame(check);
      };
      requestAnimationFrame(check);
    };
    document.addEventListener(event,capture,true);
  }, {id,event,selector,condition,key});
}

(async()=>{let browser,lastPage;try{
 await new Promise(r=>server.listen(0,'127.0.0.1',r));browser=await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM});if(evidence)fs.mkdirSync(evidence,{recursive:true});
 if(trustOnly){
  for(const width of [1440,390]){
   const page=await browser.newPage({viewport:{width,height:900}}),errors=[];lastPage=page;page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
   await page.goto('http://127.0.0.1:'+server.address().port);await page.getByRole('button',{name:'Compare identity',exact:true}).click();
   const sheet=page.getByRole('dialog'),trust=sheet.getByRole('button',{name:'Trust the new identity',exact:true}),compare=sheet.locator('label',{hasText:'The new code matches what they told me'});
   await sheet.waitFor();assert.equal(await trust.isDisabled(),true);await compare.click();assert.equal(await trust.isEnabled(),true);
   const change=async(peer,key)=>{const before=await page.evaluate(()=>fixture.trustCommits.length);await page.evaluate(({peer,key})=>fixture.changeTrust(peer,key),{peer,key});await page.waitForFunction(({peer,key,before})=>fixture.trustCommits.slice(before).some(x=>x.peer===peer&&x.key===key),{peer,key,before});return page.evaluate(({peer,key,before})=>fixture.trustCommits.slice(before).find(x=>x.peer===peer&&x.key===key),{peer,key,before});};
   let commit=await change('fixture/a','key-b');assert.equal(commit.disabled,true,'changed pending key must invalidate comparison in its first commit');assert.equal(commit.checked,false);
   commit=await change('fixture/a','key-a');assert.equal(commit.disabled,true,'returning key still needs a fresh comparison');
   await compare.click();commit=await change('fixture/b','key-a');assert.equal(commit.disabled,true,'peer switch invalidates comparison');assert.equal(commit.checked,false);
   await compare.click();commit=await change('fixture/b','');assert.equal(commit.disabled,true,'absent pending key invalidates comparison');assert.equal(commit.checked,false);
   commit=await change('fixture/b','key-c');assert.equal(commit.disabled,true);await compare.click();
   await sheet.getByRole('button',{name:'Not now',exact:true}).click();await sheet.waitFor({state:'hidden'});await page.getByRole('button',{name:'Compare identity',exact:true}).click();await sheet.waitFor();assert.equal(await trust.isDisabled(),true,'reopening requires comparison again');
   await compare.click();await trust.click();await page.waitForFunction(()=>fixture.writes.length===1);assert.equal(await trust.isDisabled(),true);
   commit=await change('fixture/b','key-d');assert.equal(commit.disabled,true);await compare.click();assert.equal(await trust.isDisabled(),true,'a pending trust action cannot be duplicated after a key change');
   assert.deepEqual(await page.evaluate(()=>fixture.writes.map(x=>x.b)),[{do:'trust',id:'fixture/b',key:'key-c'}],'submit only the explicitly compared peer/key');
   await page.evaluate(()=>fixture.finish(0));await sheet.waitFor({state:'hidden'});await page.getByRole('button',{name:'Compare identity',exact:true}).click();await sheet.waitFor();assert.equal(await trust.isDisabled(),true);await compare.click();await trust.click();await page.waitForFunction(()=>fixture.writes.length===2);
   assert.deepEqual(await page.evaluate(()=>fixture.writes.map(x=>x.b)),[{do:'trust',id:'fixture/b',key:'key-c'},{do:'trust',id:'fixture/b',key:'key-d'}]);await page.evaluate(()=>fixture.finish(1));await sheet.waitFor({state:'hidden'});
   assert.deepEqual(errors,[]);console.log(JSON.stringify({width,changedKey:true,returnedKey:true,changedPeer:true,absentKey:true,reopened:true,pendingNoDuplicate:true,exactComparedSubmission:true,liveTrust:false}));await page.close();
  }
  console.log('trust comparison rendered PASS');return;
 }
 for(const width of [1440,390])for(const count of [100,1000]){
  const page=await browser.newPage({viewport:{width,height:900},hasTouch:width<600}),errors=[];page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(String(e)));
  lastPage=page;
  const measure=async(id,text)=>page.evaluate(({id,text})=>{
   document.addEventListener('click',()=>{const start=performance.now(),observer=new MutationObserver(()=>{
    if(!document.body.textContent.includes(text))return;observer.disconnect();const domMs=performance.now()-start;
    requestAnimationFrame(()=>requestAnimationFrame(()=>fixture.timings.push({id,domMs,presentedUpperMs:performance.now()-start})));
   });observer.observe(document.body,{subtree:true,childList:true,characterData:true});},{capture:true,once:true});
  },{id,text});
  await page.goto('http://127.0.0.1:'+server.address().port+'/?count='+count);const row=page.locator('[data-mid="'+String(count).padStart(32,'0')+'"]');await row.waitFor();await row.scrollIntoViewIfNeeded();
  const menu = async (name, beforePick) => {
    const dialog = page.getByRole('dialog');
    await dialog.waitFor({state:'hidden'});
    await row.scrollIntoViewIfNeeded();
    if (width > 600) {
      await row.hover();
      await row.getByRole('button',{name:'More actions',exact:true}).click();
      await beforePick?.();
      await page.getByRole('menuitem',{name,exact:true}).click();
      return;
    }
    const bubble = row.locator('[data-message-bubble]');
    await bubble.click({trial:true});
    const b = await bubble.boundingBox();
    const cdp = await page.context().newCDPSession(page);
    try {
      await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:b.x+b.width/2,y:b.y+b.height/2}]});
      try {
        // Check the rendered sheet while the contact is held. On Windows the sheet
        // appeared in the DOM while the role locator still waited for it.
        await page.waitForFunction(() => {
          const sheet = document.querySelector('#portals .an-sheet[data-open]');
          return !!sheet?.checkVisibility({visibilityProperty:true,opacityProperty:true});
        }, null, {polling:50});
      } finally {
        await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
      }
    } finally {
      await cdp.detach();
    }
    await beforePick?.();
    await dialog.getByRole('button',{name,exact:true}).click();
  };
  const originalWidth = (await row.locator('[data-message-bubble]').boundingBox()).width;
  const areaSelector = 'textarea[aria-label="New text"]';
  const pendingSelector = '[data-message-change="pending"]';
  await menu('Edit', () => measureEdit(page, 'open', 'click', areaSelector));
  const area = page.getByRole('textbox',{name:'New text'});
  await row.getByRole('button',{name:/Peer Quoted message/}).waitFor();
  const editBox = await checkEditBox(page, area, originalWidth, evidence && count === 100);
  await area.fill('Cancel this edit');
  assert.equal(await area.evaluate(el => el.offsetHeight), editBox.initialHeight, 'shortening text restores the starting height');
  assert.equal(await area.evaluate(el => el.scrollHeight - el.clientHeight),0,'shortening removes the need to scroll');
  assert.equal((await row.locator('[data-message-bubble]').boundingBox()).width, editBox.bubbleWidth, 'shortening text keeps full width');
  await measureEdit(page, 'escape', 'keydown', areaSelector, 'closed', 'Escape');
  await area.press('Escape');
  await area.waitFor({state:'hidden'});
  await row.getByText('Original message '+count,{exact:false}).waitFor();
  assert.equal(await page.evaluate(()=>fixture.writes.length),0,'Escape cancels without a write');
  await menu('Edit', () => measureEdit(page, 'reopen', 'click', areaSelector));
  assert.equal(await area.inputValue(),'Original message '+count,'reopening starts from the stored message');
  await area.fill('Visible before backend');
  await area.press('End');
  await measureEdit(page, 'shiftEnter', 'keydown', areaSelector, 'newline', 'Enter');
  await area.press('Shift+Enter');
  assert.equal(await area.inputValue(),'Visible before backend\n','Shift+Enter inserts a newline');
  assert.equal(await page.evaluate(()=>fixture.writes.length),0,'Shift+Enter does not save');
  await page.waitForFunction(()=>fixture.editTimings.shiftEnter?.presentedUpperMs !== undefined);
  await area.pressSequentially('Second line');
  const editedText = 'Visible before backend\nSecond line';
  await measureEdit(page, 'enter', 'keydown', pendingSelector, 'visible', 'Enter');
  await area.press('Enter');
  await row.getByText('Visible before backend',{exact:false}).waitFor();
  assert.deepEqual(await page.evaluate(()=>fixture.writes.map(w=>w.b.text)),[editedText]);
  await row.getByText('Saving edit…',{exact:true}).waitFor();
  await area.waitFor({state:'hidden'});
  await page.getByRole('textbox',{name:'Unrelated draft'}).fill('Keep typing during write');await page.getByRole('button',{name:'Toggle settings'}).click();assert.equal(await page.locator('[data-current-tab]').innerText(),'settings');
  if(evidence)await page.screenshot({path:path.join(evidence,'edit-pending-'+width+'-'+count+'.png')});
  await page.evaluate(()=>fixture.finish(0,true));await row.getByText('Original message '+count,{exact:false}).waitFor();
  await row.getByRole('button',{name:'Edit again'}).click();
  assert.equal(await area.inputValue(),editedText,'failed edit retains the newline and draft');
  await measureEdit(page, 'retry', 'click', pendingSelector);
  await page.getByRole('button',{name:'Save',exact:true}).click();
  await page.waitForFunction(()=>fixture.writes.length===2 && fixture.editTimings.retry?.presentedUpperMs !== undefined);
  assert.equal(await row.locator(pendingSelector).count(),1,'retry stays visibly pending during the hold');
  const confirmation = await page.evaluate(() => new Promise((resolve,reject) => {
    const {start} = fixture.editTimings.retry;
    setTimeout(() => {
      const releasedMs = performance.now() - start;
      const check = () => {
        if (!document.querySelector('[data-message-change]')) {
          resolve({releasedMs, confirmedMs:performance.now() - start});
        } else if (performance.now() - releasedMs - start < 5000) requestAnimationFrame(check);
        else reject(new Error('Held edit did not confirm'));
      };
      fixture.finish(1);
      requestAnimationFrame(check);
    }, Math.max(0, 400 - (performance.now() - start)));
  }));
  assert.equal(await row.getByText('Second line',{exact:false}).count(),1,'confirmed message keeps the edit');
  const editTimings = await page.evaluate(()=>Object.fromEntries(Object.entries(fixture.editTimings).map(([id,{readyFrameMs,presentedUpperMs}])=>[id,{readyFrameMs,presentedUpperMs}])));
  for (const id of ['open','escape','reopen','shiftEnter','enter','retry']) {
    const ms = editTimings[id]?.presentedUpperMs;
    assert(Number.isFinite(ms) && ms < 1100, id+' visible feedback must beat 1100 ms: '+ms);
  }
  await menu('Delete…');await measure('delete','Deleting message…');await page.getByRole('button',{name:'Delete',exact:true}).click();await row.getByText('Deleting message…',{exact:false}).waitFor();assert.equal(await row.getByText('Visible before backend',{exact:false}).count(),0);
  await page.getByRole('dialog').waitFor({state:'hidden'});
  if(evidence)await page.screenshot({path:path.join(evidence,'delete-pending-'+width+'-'+count+'.png')});
  await page.evaluate(()=>fixture.finish(2,true));await row.getByText('Visible before backend',{exact:false}).waitFor();
  await menu('Edit');await page.getByRole('textbox',{name:'New text'}).fill('Older local attempt');await page.getByRole('button',{name:'Save',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===4);await page.evaluate(()=>fixture.newer());await row.getByText('Newer linked-device edit',{exact:false}).waitFor();await page.evaluate(()=>fixture.finish(3,true));await row.getByText('Newer linked-device edit',{exact:false}).waitFor();
  await row.getByRole('button',{name:'Edit again'}).click();assert.equal(await page.getByRole('textbox',{name:'New text'}).inputValue(),'Older local attempt','newer linked content must not lose failed local text');
  await page.getByRole('button',{name:'Cancel',exact:true}).click();await row.getByText('Newer linked-device edit',{exact:false}).waitFor();assert.equal(await row.locator('[data-message-change]').count(),0);
  assert.equal(await page.getByRole('textbox',{name:'Unrelated draft'}).inputValue(),'Keep typing during write');assert.deepEqual(errors,[]);
  const topicMenu=async(name)=>{await page.locator('[data-topic]').click();await page.getByRole('menuitem',{name,exact:true}).click();};
  await topicMenu('Mark done');await page.getByRole('region',{name:'Topic state'}).getByText('Done',{exact:true}).waitFor();await page.getByText('Saving change…',{exact:true}).waitFor();
  assert.equal(await page.evaluate(()=>fixture.writes.length),5);await page.evaluate(()=>fixture.finish(4,true));await page.waitForFunction(()=>!document.querySelector('[aria-label="Topic state"]'));
  await topicMenu('Rename…');await page.getByRole('textbox',{name:'Name',exact:true}).fill('Local topic title');await page.getByRole('button',{name:'Save',exact:true}).click();await page.locator('[data-topic]').getByText('Local topic title',{exact:true}).waitFor();
  await page.evaluate(()=>fixture.finish(5,true));await page.locator('[data-topic]').getByText('Chosen topic',{exact:true}).waitFor();await topicMenu('Rename…');assert.equal(await page.getByRole('textbox',{name:'Name',exact:true}).inputValue(),'Local topic title');
  await page.getByRole('button',{name:'Save',exact:true}).click();await page.waitForFunction(()=>fixture.writes.length===7);await page.evaluate(()=>fixture.finish(6));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  await topicMenu('Mark done');await page.waitForFunction(()=>fixture.writes.length===8);await page.evaluate(()=>fixture.finish(7));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  await page.getByRole('region',{name:'Topic state'}).getByRole('button',{name:'Reopen',exact:true}).click();await page.getByText('Saving change…',{exact:true}).waitFor();assert.equal(await page.getByRole('region',{name:'Topic state'}).count(),0);await page.evaluate(()=>fixture.finish(8));await page.waitForFunction(()=>Object.keys(fixture.store.get().topicBusy).length===0);
  if(evidence)await page.screenshot({path:path.join(evidence,'recovery-'+width+'-'+count+'.png')});
  console.log(JSON.stringify({skin:overlay?path.basename(path.dirname(overlay)):'comic',width,count,editBox,editTimings,confirmation,editImmediate:true,deleteImmediate:true,topicImmediate:true,failedDraftsRetained:true,newerSyncPreserved:true,unrelatedInput:true,timings:await page.evaluate(()=>fixture.timings),errors}));await page.close();
 }
}catch(error){if(lastPage&&!lastPage.isClosed()){console.error(await lastPage.evaluate(()=>({text:document.body.innerText.slice(-2500),message:window.fixture?.store.get().dm?.messages.at(-1),writes:window.fixture?.writes.map(w=>({p:w.p,b:w.b}))})));if(evidence)await lastPage.screenshot({path:path.join(evidence,'failure.png')});}throw error;}finally{await browser?.close();await new Promise(r=>server.close(r));fs.rmSync(work,{recursive:true,force:true})}})().catch(e=>{console.error(e.stack);process.exitCode=1});
