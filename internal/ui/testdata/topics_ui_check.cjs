// Actual TopicBar compact-current fallback, with the existing fit callback.
if(process.env.AGENTNET_TOPIC_COMPACT_CHECK==='1'){
 const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
 const esbuild=require('../web/node_modules/esbuild');
 const jsx=(tag,props,...children)=>({tag,props:{...props,children:children.flat(Infinity).filter(c=>c!==false&&c!=null)}});
 const nodes=t=>t&&typeof t==='object'?[t,...(t.props?.children||[]).flatMap(nodes)]:[];
 const source=fs.readFileSync(require('node:path').join(__dirname,'../web/src/features/Conversation.topics.tsx'),'utf8');
 function render(options={},text=source){
  const cfg={width:320,mainWidth:77,allWidth:80,newWidth:44,open:'named',listed:true,fresh:false,state:'active',...options};
  const topics=[{id:'named',conv:'chat',title:'Named topic',state:cfg.state,unread:5,lastAt:'2026-10-16T00:00:00Z',count:1,needsYou:0},{id:'other',conv:'chat',title:'Other',state:'active',unread:7,lastAt:'2026-10-15T00:00:00Z',count:1,needsYou:0},{id:'closed',conv:'chat',title:'Closed',state:'archived',unread:3,lastAt:'2026-10-14T00:00:00Z',count:1,needsYou:0}];
  const state={overview:{topic_list:cfg.listed},drafts:{chat:{topic:cfg.open||undefined,newTopic:cfg.fresh}},pending:null};
  let cells=[],refs=[],effects=[],pending=[],observers=[],reads=[],cursor=0,refCursor=0,effectCursor=0;
  const controls=Object.fromEntries(['nav','main','all','new'].map(key=>[key,{key,get clientWidth(){return cfg.width;},querySelector:()=>null,get offsetWidth(){return Math.round(cfg[key+'Width']);},getBoundingClientRect(){reads.push(key);return {width:cfg[key+'Width']};}}]));
  const globals={module:{exports:{}},jsx:(tag,props,...children)=>{const node=jsx(tag,props,...children);if(props?.ref){const label=props['aria-label']||'';props.ref.current=tag==='nav'?controls.nav:label.startsWith('Main')?controls.main:label.startsWith('All topics')?controls.all:controls.new;}return node;},Fragment:'Fragment',
   useState:init=>{const i=cursor++;if(!(i in cells))cells[i]=init;return[cells[i],v=>{cells[i]=typeof v==='function'?v(cells[i]):v;}];},
   useRef:init=>{const i=refCursor++;return refs[i]??(refs[i]={current:init});},
   useLayoutEffect:(fn,deps)=>{const i=effectCursor++,old=effects[i];if(!old||deps.some((d,j)=>!Object.is(d,old.deps[j])))pending.push(()=>{old?.cleanup?.();effects[i]={deps,cleanup:fn()};});},
   ResizeObserver:class{constructor(fn){this.fn=fn;this.observed=[];this.disconnected=false;observers.push(this);}observe(el){this.observed.push(el);}disconnect(){this.disconnected=true;}},getComputedStyle:()=>({columnGap:'8',paddingLeft:'12',paddingRight:'12'}),CSS:{escape:s=>s},
   useApp:()=>({}),useWide:()=>false,useAgentNames:()=>({}),useStore:(_s,f)=>f(state),
   useSteadyChoices:rows=>({choices:rows.map(row=>({row,present:true})),bind:{},release(){}}),unreadTopicOrder:()=>0,
   chatList:()=>[],newestFirst:(a,b)=>b.lastAt.localeCompare(a.lastAt),topicOf:t=>t,topicMark:()=>null,firstLine:t=>t,dayLabel:()=>'day',timeOf:()=>'time',
   TOPICS:{barMax:6,chipMinWidth:104,chipMaxWidth:'16rem',barTitle:40},AllTopics:'AllTopics',Menu:{Trigger:'Menu.Trigger'},
   IconPlus:'Icon',IconListSearch:'Icon',IconAlertCircle:'Icon',IconArchive:'Icon',IconCircleCheck:'Icon',IconClock:'Icon',IconClockQuestion:'Icon',IconChevronDown:'Icon'};
  if(cfg.switchTo){
   const choiceModule={exports:{}},choiceGlobals={...globals,module:choiceModule,exports:choiceModule.exports};
   const choiceSource=fs.readFileSync(require('node:path').join(__dirname,'../web/src/features/Conversation.choices.ts'),'utf8').replace(/^import[\s\S]*?from[^;]+;/gm,'');
   vm.runInNewContext(esbuild.transformSync(choiceSource,{loader:'ts',format:'cjs'}).code,choiceGlobals);
   globals.useSteadyChoices=choiceModule.exports.useSteadyChoices;
  }
  globals.exports=globals.module.exports;
  const code=esbuild.transformSync(text.replace(/^import[\s\S]*?from[^;]+;/gm,''),{loader:'tsx',jsxFactory:'jsx',jsxFragment:'Fragment',format:'cjs'}).code;
  vm.runInNewContext(code,globals);
  let tree;
  const rerender=()=>{for(let i=0;i<2;i++){cursor=0;refCursor=0;effectCursor=0;pending=[];for(const ref of refs)if(Object.values(controls).includes(ref.current))ref.current=null;tree=globals.module.exports.TopicBar({dm:{id:'chat',peer:{label:'Fixture'},main_topic:{unread:2},topics,frozen:cfg.frozen,role:cfg.role}});for(const effect of pending)effect();}};
  rerender();
  if(cfg.switchTo){
   nodes(tree).find(n=>n.tag==='nav').props.onFocusCapture();
   state.drafts.chat.topic=cfg.switchTo;
   rerender();
  }
  const menus=nodes(tree).filter(n=>n.tag?.name==='TopicMenu'),all=nodes(tree).find(n=>n.tag==='button'&&n.props['aria-label']?.startsWith('All topics'));
  return {tree,menus,all,buttons:nodes(tree).filter(n=>n.tag==='button'),cfg,controls,observers,reads,resize:()=>{observers.filter(o=>!o.disconnected).forEach(o=>o.fn());rerender();return nodes(tree).filter(n=>n.tag?.name==='TopicMenu');},change:changes=>{Object.assign(cfg,changes);rerender();return nodes(tree).filter(n=>n.tag?.name==='TopicMenu');},unmount:()=>effects.forEach(e=>e.cleanup?.())};
 }
 // Holding the bar's old focus must not hide the explicitly selected topic.
 const switched=render({width:361,switchTo:'other'});
 assert.equal(switched.menus.length,1,'one-slot focused bar keeps current topic menu after All topics selection');
 assert.equal(switched.menus[0].props.topic.id,'other','current exact selected topic replaces previous held chip');
 // Existing fixed controls cost249px in this isolated geometry: free=width-249.
 for(const state of ['active','done','archived']){
  const view=render({state});assert.equal(view.menus.length,1);const trigger=view.menus[0].props.trigger;assert.equal(view.menus[0].props.topic.state,state);
  assert.equal(trigger.props.style.minWidth,44);assert.equal(trigger.props['aria-current'],'true');assert.equal(trigger.props.title,'Named topic');assert.match(trigger.props['aria-label'],/^Named topic, 5 unread, open topic, menu$/);
  assert(view.buttons.some(n=>n.props['aria-label']==='Main, 2 unread'));assert(view.buttons.some(n=>n.props['aria-label']==='New topic'));assert.match(view.all.props['aria-label'],/10 unread in other topics$/,'open unread excluded exactly once, archived unread remains');
 }
 for(const options of [{open:''},{fresh:true},{listed:false,width:220},{width:300}]){
  const view=render(options);assert.equal(view.menus.length,0,JSON.stringify(options));
  assert.equal(nodes(view.tree).filter(n=>n.props['data-topic']).length,0,'no dead fallback plain chip');
  if(options.listed===false)assert.equal(view.all,undefined,'unlisted host has no All control');else assert.match(view.all.props['aria-label'],/15 unread in other topics$/);
 }
 assert.equal(render({width:301}).menus.length,1,'free52 equals44+gap, fits');
 assert.equal(render({width:300}).menus.length,0,'free51 must not clip fixed controls');
 const ci={mainWidth:68.15625,allWidth:89.0625,newWidth:130.453125};
 assert.equal(render({...ci,width:390}).menus.length,1,'actual CI fractional controls leave54.328125px for current menu+gap');
 assert.equal(render({...ci,width:382}).menus.length,0,'Main introduces its own gap: free46.328125 must not falsely fit');
 assert.equal(render({...ci,width:388}).menus.length,1,'nearby integer width leaves52.328125px, fits');
 assert.equal(render({...ci,width:387}).menus.length,0,'nearby integer width leaves51.328125px, does not fit');
 // Fractional controls must stay fractional: rounding All/New would cross these exact boundaries.
 const fractional={mainWidth:68.15625,allWidth:89.125,newWidth:130.71875};
 assert.equal(render({...fractional,width:388}).menus.length,1,'fractional control sum leaves exactly52px');
 assert.equal(render({...fractional,width:387}).menus.length,0,'fractional control sum leaves exactly51px');
 const observed=render({...ci,width:390});
 assert.deepEqual(observed.observers[0].observed.map(el=>el.key).sort(),['all','main','nav','new'],'observe all three known controls and nav');
 assert.deepEqual(observed.reads.sort(),['all','main','new'],'one rect each per fit; no history geometry');
 observed.cfg.mainWidth+=3;assert.equal(observed.resize().length,0,'Main badge/font width change removes menu that no longer fits');
 observed.cfg.mainWidth-=3;assert.equal(observed.resize().length,1,'Main width shrink restores current menu');
 observed.cfg.newWidth+=3;assert.equal(observed.resize().length,0,'New font width change updates fit');
 observed.cfg.newWidth-=3;assert.equal(observed.resize().length,1,'New width shrink restores current menu');
 observed.cfg.allWidth+=3;assert.equal(observed.resize().length,0,'All count width change updates fit');
 observed.cfg.allWidth-=3;assert.equal(observed.resize().length,1,'All width shrink restores current menu');
 for(const status of [{frozen:true},{role:'former_member'}]){
  const view=render({...ci,width:382});assert.equal(view.change(status).length,1,'removing New frees room without nav width change');
  assert(view.observers[0].disconnected,'old observer cleaned up on control removal');
  assert.deepEqual(view.observers.at(-1).observed.map(el=>el.key).sort(),['all','main','nav'],'removed New is not observed');
  view.unmount();assert(view.observers.every(o=>o.disconnected),'all observers disconnect on unmount');
 }
 observed.unmount();assert(observed.observers.every(o=>o.disconnected),'ordinary fit observer disconnects on unmount');
 for(const width of [361,473,1000]){
  const current=render({width});assert.equal(current.menus.length,1);assert.equal(current.menus[0].props.trigger.props.style.minWidth,104,'normal room path retains chip minimum');
  if(process.env.AGENTNET_TOPIC_BASE_SOURCE){
   const before=render({width},fs.readFileSync(process.env.AGENTNET_TOPIC_BASE_SOURCE,'utf8'));
   const visibleTree=tree=>JSON.stringify(tree,(key,value)=>key==='ref'?undefined:value);
   assert.equal(visibleTree(current.tree).replaceAll('tnum max-[359px]:hidden','tnum'),visibleTree(before.tree).replaceAll('tnum max-[359px]:hidden','tnum'),'normal tree unchanged except approved narrow All label CSS and measurement refs');
  }
 }
 console.log('PASS actual TopicBar: room0 current/state/menu44; Main/fresh/unlisted/no-fit guards; actual CI fractional widths and exact52/51 boundary; Main/All/New resize and frozen/former cleanup; unread excluded once; room>=1 unchanged');
}else // Cheap actual AllTopics component check; JSX is transformed by installed esbuild.
if(process.env.AGENTNET_TOPIC_SOURCE_CHECK==='1'){
 const {spawnSync}=require('node:child_process'),path=require('node:path');
 const result=spawnSync(process.execPath,[path.join(__dirname,'review_topics_ui_check.mjs')],{stdio:'inherit'});
 process.exitCode=result.status??1;
}else{
// Topics as the messenger renders them (topics_rendered_test.go): the bar,
// the open topic's menu, All topics and the end of a done topic, at
// 1800x960 and 390x844, light and dark. Needs AGENTNET_PLAYWRIGHT;
// optional AGENTNET_SCREENSHOTS (a directory outside the repository).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.TOPICS_URL, total = Number(process.env.TOPICS_TOTAL);
const shots = process.env.AGENTNET_SCREENSHOTS || '';
// slowly: the same conversation over a slow connection (every conversation
// load takes 700 ms). Opening it never shows an empty frame: what was there
// stays, then (after 400 ms) its header with who it is and a visible sketch
// of messages, then the messages, faded in place; the card or pane moves in
// once. Switching topic keeps the last topic's messages until the next's are there.
async function slowly(browser, w, h, scheme, name) {
  name += ' slow';
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  await ctx.route(/\/api\/(thread|dm)\?/, async (r) => { await new Promise((z) => setTimeout(z, 700)); await r.continue(); });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
  const watch = (ms) => p.evaluate((ms) => {
    const sr = document.getElementById('skin').shadowRoot;
    const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth; };
    const ids = new WeakMap(); let next = 0;
    const id = (e) => (e ? (ids.get(e) ?? (ids.set(e, ++next), next)) : 0);
    const moves = (window.__moves = []);
    const onAnim = (e) => { if (/^an-(push-in|nudge-in)$/.test(e.animationName)) moves.push(e.animationName); };
    sr.addEventListener('animationstart', onAnim);
    const bar = sr.querySelector('nav[aria-label^="Topics with"]'), head = bar?.parentElement?.firstElementChild || null;
    const frames = (window.__frames = []), t0 = performance.now();
    const look = () => {
      const logs = [...sr.querySelectorAll('[role="log"]')].filter(vis);
      const busy = [...sr.querySelectorAll('[aria-busy="true"]')].filter((b) => b.tagName === 'SECTION' && vis(b));
      // A sketch bubble is visible: outlined, and filled unlike the page under it.
      const sketch = busy.flatMap((b) => [...b.querySelectorAll('[data-skeleton]')].filter((e) => {
        const c = getComputedStyle(e);
        return vis(e) && parseFloat(c.borderTopWidth) >= 1 && !/, 0\)$/.test(c.borderTopColor) && c.backgroundColor !== getComputedStyle(b).backgroundColor && +c.opacity >= 0.5;
      })).length;
      frames.push({ t: Math.round(performance.now() - t0), busy: busy.length, sketch, title: busy.map((b) => b.querySelector('header')?.textContent || '').join('|'),
        logs: logs.length, msgs: Math.max(0, ...logs.map((l) => l.querySelectorAll('[data-mid]').length)),
        list: [...sr.querySelectorAll('section[aria-label="Chats"]')].some(vis),
        pick: [...sr.querySelectorAll('main p')].some((e) => e.textContent === 'Pick a chat' && vis(e)),
        card: id(sr.querySelector('[data-card]:not([aria-hidden])')),
        bar: !!bar && bar.isConnected && vis(bar) && sr.querySelector('nav[aria-label^="Topics with"]') === bar, head: !!head && head.isConnected });
      if (performance.now() - t0 < ms) requestAnimationFrame(look); else sr.removeEventListener('animationstart', onAnim);
    };
    requestAnimationFrame(look);
  }, ms);
  const frames = async (ms) => { await p.waitForTimeout(ms + 50); return p.evaluate(() => ({ frames: window.__frames, moves: window.__moves })); };

  await watch(1500);
  await p.getByRole('button', { name: /not linked to a person/i }).first().click();
  const { frames: opened, moves } = await frames(1500);
  const shownAt = opened.findIndex((f) => f.msgs > 0);
  assert.ok(shownAt >= 0, name + ': the conversation opened: ' + JSON.stringify(opened.slice(-3)));
  assert.ok(opened.some((f) => f.busy), name + ': a 700 ms load shows the opening header and sketch');
  for (const f of opened) {
    const sketched = f.busy && f.sketch >= 3 && /\S/.test(f.title);
    assert.ok((w >= 1000 ? f.pick : f.list) || f.msgs > 0 || sketched, name + ': open chat: a frame with nothing (or nothing visible) in it at ' + f.t + ' ms: ' + JSON.stringify(f));
  }
  assert.ok(opened.slice(shownAt).every((f) => f.msgs > 0), name + ': open chat: the messages never went away: ' + JSON.stringify(opened));
  assert.equal(moves.length, 1, name + ': the conversation moved in once: ' + JSON.stringify(moves));
  if (w < 1000) {
    const cards = new Set(opened.map((f) => f.card).filter(Boolean));
    assert.equal(cards.size, 1, name + ': one card from the placeholder to the messages: ' + JSON.stringify([...cards]));
  }

  // A topic switch over the same slow connection.
  const bar = p.locator('nav[aria-label^="Topics with"]');
  await bar.waitFor();
  await p.waitForTimeout(400);
  const other = bar.locator('button[data-topic]:not([aria-current])').first();
  if (await other.count()) {
    const chips = () => p.evaluate(() => [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"] [data-topic]')].map((c) => c.getAttribute('data-topic')));
    const order = await chips(), chosen = await other.getAttribute('data-topic');
    await watch(1200);
    await other.click();
    const { frames: switched } = await frames(1200);
    for (const f of switched) {
      assert.ok(f.bar && f.head && f.msgs > 0 && !f.busy, name + ': topic switch: a blank or redrawn frame at ' + f.t + ' ms: ' + JSON.stringify(f));
    }
    // The bar keeps its order: the chosen chip stays where it was clicked,
    // and chips that stay keep their places.
    const after = await chips();
    assert.equal(after.indexOf(chosen), order.indexOf(chosen), name + ': the chosen chip stays in its slot: ' + JSON.stringify([order, after]));
    const kept = order.filter((id) => after.includes(id));
    assert.deepEqual(after.filter((id) => kept.includes(id)), kept, name + ': chips that stay keep their order');
  }
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    let run = 0;
    for (const [w, h] of [[1800, 960], [390, 844]]) for (const scheme of ['light', 'dark']) {
      const aisle = ++run, title = 'Count the bath sets in aisle ' + aisle; // each run finishes with its own task reopened
      const name = w + 'x' + h + '-' + scheme;
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
      const p = await ctx.newPage();
      const errors = [];
      p.on('pageerror', (e) => errors.push(String(e)));
      await p.goto(url);
      await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
      const fits = async (what) => {
        const o = await p.evaluate(() => ({ page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          bar: [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"]')].map((n) => n.scrollWidth - n.clientWidth) }));
        assert.ok(o.page <= 0 && o.bar.every((x) => x <= 1), name + ' ' + what + ': nothing scrolls sideways ' + JSON.stringify(o));
      };
      const snap = async (what) => { if (shots) await p.screenshot({ path: shots + '/topics-' + name + '-' + what + '.png' }); };
      // Frames: from just before an action, every animation frame records what
      // is on screen: opening placeholders, the most messages any visible
      // timeline shows, and whether the conversation's header and topic bar
      // are still the very elements they were (not drawn again).
      const watch = () => p.evaluate(() => {
        const sr = document.getElementById('skin').shadowRoot;
        const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth; };
        const bar = sr.querySelector('nav[aria-label^="Topics with"]'), head = bar?.parentElement?.firstElementChild || null;
        const frames = (window.__frames = []), t0 = performance.now();
        const look = () => {
          const logs = [...sr.querySelectorAll('[role="log"]')].filter(vis);
          frames.push({ t: Math.round(performance.now() - t0), busy: [...sr.querySelectorAll('[aria-busy="true"]')].filter(vis).length,
            logs: logs.length, msgs: Math.max(0, ...logs.map((l) => l.querySelectorAll('[data-mid]').length)),
            list: [...sr.querySelectorAll('section[aria-label="Chats"]')].some(vis),
            pick: [...sr.querySelectorAll('main p')].some((e) => e.textContent === 'Pick a chat' && vis(e)),
            bar: !!bar && bar.isConnected && vis(bar) && sr.querySelector('nav[aria-label^="Topics with"]') === bar, head: !!head && head.isConnected });
          if (performance.now() - t0 < 900) requestAnimationFrame(look);
        };
        requestAnimationFrame(look);
      });
      const frames = async () => { await p.waitForTimeout(950); return p.evaluate(() => window.__frames); };

      // Opening a conversation never shows an empty frame: the list (or what
      // the pane showed) stays until its messages are there; only a slow load
      // (400 ms) may show the opening placeholder.
      await watch();
      await p.getByRole('button', { name: /not linked to a person/i }).first().click();
      const opened = await frames();
      const shownAt = opened.findIndex((f) => f.msgs > 0);
      assert.ok(shownAt >= 0, name + ': the conversation opened: ' + JSON.stringify(opened.slice(-3)));
      for (const f of opened) {
        assert.ok((w >= 1000 ? f.pick : f.list) || f.msgs > 0 || (f.busy && f.t >= 350), name + ': open chat: a frame with nothing in it at ' + f.t + ' ms: ' + JSON.stringify(f));
      }
      assert.ok(opened.slice(shownAt).every((f) => f.msgs > 0), name + ': open chat: the messages never went away: ' + JSON.stringify(opened));
      const bar = p.locator('nav[aria-label^="Topics with"]');
      await bar.waitFor();
      await fits('bar');
      await snap('bar');
      const chips = await bar.locator(':scope > button, :scope > [aria-haspopup="menu"]').count();
      assert.ok(chips >= 2 && chips <= 7, name + ': at most six chips and New topic, got ' + chips); // topics + All topics + New topic
      await bar.getByRole('button', { name: new RegExp('^All topics \\(' + total + '\\)') }).waitFor();
      if (w >= 1000) {
        assert.ok(await bar.getByRole('button', { name: /^May I move the Savannah order.*Needs you/ }).count() === 1, name + ': the question waiting for you has a chip saying so');
      } else {
        // A phone: one topic chip (the open one) keeps room for its name
        // beside "All N"; the questions held for the person that are not in
        // the bar mark the All topics chip, and its label says how many.
        const open = await bar.locator('[aria-current="true"]').boundingBox();
        assert.ok(open && open.width >= 180, name + ': the open topic chip keeps room for its name: ' + JSON.stringify(open));
        const label = await bar.getByRole('button', { name: /^All topics/ }).getAttribute('aria-label');
        const openNeeds = /Needs you/.test(await bar.locator('[aria-current="true"]').getAttribute('aria-label')); // the newest topic opens first: it may be one of the four questions
        assert.match(label, new RegExp('\\b' + (4 - openNeeds) + ' need you\\b'), name + ': the All topics chip says what needs you: ' + label);
        assert.match(await bar.getByRole('button', { name: /^All topics/ }).innerText(), new RegExp('^All ' + total + '$'), name + ': the short phone label');
      }
      // All topics: opened from the keyboard, focus in its search; lists, filters, searches, opens.
      await bar.getByRole('button', { name: /^All topics/ }).focus();
      await p.keyboard.press('Enter');
      const all = p.getByRole('dialog', { name: 'All topics' });
      await all.getByRole('list', { name: 'Active topics' }).waitFor();
      // Owner (v0.8.17): a topic's Done and Archive are the person's, on every linked device.
      assert.match(await all.innerText(), /Names, Done and Archive sync across your linked devices\./, name + ': All topics says marks follow linked devices');
      assert.doesNotMatch(await all.innerText(), /stays? on this device/, name + ': All topics no longer says marks stay on this device');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('type') === 'search');
      await fits('all topics');
      assert.ok(await all.getByRole('button', { name: /^Active/ }).getAttribute('aria-pressed') === 'true', name + ': Active is the first filter');
      await all.getByRole('searchbox').fill('NOTE 17:');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.querySelectorAll('[role="dialog"] ul li').length === 1, null, { timeout: 5000 });
      await snap('all-search');
      await all.getByRole('searchbox').fill('');
      await all.getByRole('button', { name: /^Done/ }).click();
      await all.getByRole('listitem').filter({ hasText: title }).waitFor();
      const done = all.getByRole('listitem').filter({ hasText: title });
      assert.equal(await all.getByRole('listitem').count(), 5 - aisle, name + ': the finished tasks not reopened yet are done');
      // The results were written by hand here (Reply): the person's words, never the agent's.
      assert.match(await done.first().innerText(), /41. bath sets/);
      // Switching topic swaps only the messages: the header and the topic bar
      // stay the same elements, and every frame shows messages (the last
      // topic's until the next one's are there), never a placeholder.
      await watch();
      await done.first().getByRole('button').click();
      const switched = await frames();
      for (const f of switched) {
        assert.ok(f.bar && f.head && f.msgs > 0 && !f.busy, name + ': topic switch: a blank or redrawn frame at ' + f.t + ' ms: ' + JSON.stringify(f));
      }
      await p.waitForSelector('section[aria-label="Topic state"]');
      const end = await p.locator('section[aria-label="Topic state"]').innerText();
      assert.match(end, /done/i); assert.doesNotMatch(end, /Your answer|conclusion/i);
      await fits('done topic');
      await snap('done');

      // The open topic's menu from the keyboard: Enter opens it on its first
      // item, Escape closes it back onto the chip.
      await bar.locator('[aria-current="true"]').focus();
      await p.keyboard.press('Enter');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('role') === 'menuitem' && /Rename/.test(document.getElementById('skin').shadowRoot.activeElement.textContent));
      await p.keyboard.press('Escape');
      await p.waitForFunction(() => document.getElementById('skin').shadowRoot.activeElement?.getAttribute('aria-current') === 'true');
      // Reopen, then Mark done, then Rename.
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Reopen' }).click();
      await p.waitForFunction(() => !document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]'));
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Mark done' }).click();
      await p.waitForFunction(() => /marked it done/.test(document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]')?.textContent || ''));
      // Owner (v0.8.17): the mark may come from another of the person's devices.
      const marked = await p.locator('section[aria-label="Topic state"]').innerText();
      assert.match(marked, /You marked it done\./, name + ': the end of a topic says the person marked it done');
      assert.doesNotMatch(marked, /on this device/, name + ': a synced Done is not called this device\'s');
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Rename…' }).click();
      await p.getByText('The name syncs across your linked devices. Other people keep their own names.', { exact: true }).waitFor();
      await p.getByRole('textbox', { name: 'Name' }).fill('Aisle 4 count ' + name);
      await snap('rename');
      await p.getByRole('button', { name: 'Save' }).click();
      await p.waitForFunction((t) => document.getElementById('skin').shadowRoot.querySelector('nav[aria-label^="Topics with"] [aria-current="true"]')?.getAttribute('aria-label')?.startsWith(t), 'Aisle 4 count ' + name);
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Rename…' }).click();
      await p.getByRole('button', { name: 'Use its first message' }).click();
      await p.waitForFunction((t) => document.getElementById('skin').shadowRoot.querySelector('nav[aria-label^="Topics with"] [aria-current="true"]')?.getAttribute('aria-label')?.startsWith(t), title);
      await bar.locator('[aria-current="true"]').click();
      await p.getByRole('menuitem', { name: 'Reopen' }).click();
      await p.waitForFunction(() => !document.getElementById('skin').shadowRoot.querySelector('section[aria-label="Topic state"]'));
      await fits('after changes');
      // Every button in the bar and the dialog has a name.
      const unnamed = await p.evaluate(() => [...document.getElementById('skin').shadowRoot.querySelectorAll('nav[aria-label^="Topics with"] button')].filter((b) => !(b.getAttribute('aria-label') || b.textContent.trim())).length);
      assert.equal(unnamed, 0, name + ': unnamed controls');
      assert.deepEqual(errors, [], name + ': page errors');
      await ctx.close();
      await slowly(browser, w, h, scheme, name);
      console.log('ok', name);
    }
    console.log('topics ui check PASS');
  } finally { await browser.close(); }
})().catch((e) => { console.error(e); process.exit(1); });

}
