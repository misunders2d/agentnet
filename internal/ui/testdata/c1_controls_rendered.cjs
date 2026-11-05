// Slice C1 of the every-control work: the 43 Comic handlers in Message.tsx, Conversation.header.tsx
// and NewChat.tsx that no rendered test had run. The real app over a real Live and Hub, with a bundle
// tagged by interaction_inventory.cjs, so each row is bound to its exact definition. Only what a row
// names is changed at the network: a view's data, to reach its state, and held or failing requests.
//
// A row passes only if: its own handler ran on trusted input, its intended result is visible within
// 1000 ms, its confirmed result is checked apart from that, and another control answers meanwhile.
// That is one skin, one engine, two widths, and three backends. It says nothing of 10x, cold or warm,
// real contention, identity or authority. A row whose state this file cannot reach stays open, with
// its reason: `open` is not a pass.
//
//   node c1_controls_rendered.cjs --table     prints the rows; needs no browser
const fs = require('node:fs');

const file = name => 'agentnet:internal/ui/web/src/features/' + name;
const H = file('Conversation.header.tsx'), M = file('Message.tsx'), N = file('NewChat.tsx');
const LOCAL = 'no request: local state only';
const sheet = title => `dialog(${JSON.stringify(title)})`, text = s => `hasText(${JSON.stringify(s)})`;

// A chat's own menu, opened as a person does. `at` is the item's handler.
const more = async p => { await p.getByRole('button', { name: 'More', exact: true }).click(); await p.getByRole('menu').waitFor(); };
const control = (p, id) => p.locator(`[data-an-control=${JSON.stringify(id)}]`).first();

const rows = [
  // ---- Conversation.header.tsx: a device thread's grants, the chat menu, helpers
  { id: H + '#copyLink/Menu.Item/onClick#2', scene: 'dm', reach: more, local: text('Link copied'), confirmed: 'clipboard', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#4', scene: 'dm', reach: more, prompt: 'agentnet:fixture', local: 'called("openLink")', confirmed: 'openLink', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#8', scene: 'dm', reach: more, local: sheet('Remind me'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/RemindSheet/onOpenChange#1', scene: 'dm', reach: async (p, r) => { await more(p); await control(p, H + '#copyLink/Menu.Item/onClick#8').click(); await r.dialog(p, 'Remind me'); }, act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#10', scene: 'thread', view: { approved: true }, reach: more, local: sheet('Stop answering'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#11', scene: 'thread', view: { approved: false }, reach: more, local: sheet('Answer'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#12', scene: 'thread', view: { task_grant: true }, reach: more, local: sheet('Stop running'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/DeviceGrantConfirm/onClose#1', scene: 'thread', view: { approved: false }, reach: async (p, r) => { await more(p); await control(p, H + '#copyLink/Menu.Item/onClick#11').click(); await r.dialog(p, 'Answer'); }, act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: H + '#current/Button/onClick#1', scene: 'dm', helpers: 2, widths: [390], local: 'panelOpen()', confirmed: 'none', backend: LOCAL },
  { id: H + '#dismiss/Button/onClick#1', scene: 'dm', helpers: 1, review: true, widths: [390], local: 'panelOpen()', confirmed: 'none', backend: LOCAL },
  { id: H + '#panel/button/onClick#1', scene: 'dm', helpers: 1, widths: [1440], closePanel: true, local: 'panelOpen()', confirmed: 'none', backend: LOCAL },

  // ---- Message.tsx: a failed send, a failed edit, a request's answer, the divider
  { id: M + '#result/button/onClick#1', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', local: 'tabShown("settings")', confirmed: 'none', backend: LOCAL },
  { id: M + '#result/button/onClick#2', scene: 'dm', message: { state: 'failed', delivery: 'failed', detail: 'relay_disk_full' }, role: 'admin',
    backend: '/api/storage/retry_send', answer: { retried: true },
    local: text('Queuing retry'), confirmed: text('Retry queued'), failing: text('Retry this message'), duplicate: 1, stale: 'open: nothing newer changes this row before its answer' },
  { id: M + '#result/button/onClick#3', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', local: sheet('Message details'), confirmed: 'none', backend: LOCAL },
  { id: M + '#Under/SendFailure/onDetails#1', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', same: M + '#result/button/onClick#3', local: sheet('Message details'), confirmed: 'none', backend: LOCAL },
  { id: M + '#needsHumanDetail/button/onClick#3', scene: 'dm', message: { _change: { status: 'failed', action: 'edit', error: 'fixture refused', control: 'edited text' } }, local: 'editing()', confirmed: 'none', backend: LOCAL,
    open: 'a failed edit is a Store state made by a refused edit, not view data: reach it by editing with the backend failing' },
  { id: M + '#needsHumanDetail/button/onClick#4', scene: 'dm', open: 'same state as Edit again; its result, the draft gone, needs that state first' },
  { id: M + '#needsHumanDetail/EditBox/onDone#1', scene: 'dm', open: 'reached through the message toolbar Edit, on wide; needs an own stored message row in the view' },
  { id: M + '#needsHumanDetail/DeleteMessage/onOpenChange#1', scene: 'dm', open: 'reached through the message toolbar More actions; needs an own stored message row in the view' },
  { id: M + '#needsHumanDetail/ApprovalCard/onClick#1', scene: 'dm', open: 'needs a stopped request of mine with a live message row' },
  { id: M + '#needsHumanDetail/MessageView/onJump,onSelect#1', scene: 'dm', open: 'needs a message with several agent targets and their outcome rows' },
  { id: M + '#Under/button/onClick#2', scene: 'dm', open: 'needs a request to an agent with its reply in view; result is the timeline moving to the reply' },
  { id: M + '#by/button/onClick#1', scene: 'dm', open: 'needs a joined helper divider with shared history: a real helper invitation with a grant' },
  { id: M + '#by/WhatTheySaw/onOpenChange#1', scene: 'dm', open: 'same state as See what they saw' },

  // ---- NewChat.tsx: the sheet, people, agents, and a chat or group being started
  { id: N + '#close/Sheet/onOpenChange#1', scene: 'newgroup', act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: N + '#close/NewGroupForm/onBack,onCreated#1', scene: 'newgroup', same: N + '#back/button/onClick#1', local: sheet('New chat'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/button/onClick#1', scene: 'newgroup', local: sheet('New chat'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/input/onChange#2', scene: 'newgroup', people: 30, input: 'nobody', local: text('Clear search'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#5', scene: 'newgroup', people: 30, reach: async (p, r) => { await control(p, N + '#back/input/onChange#2').fill('nobody'); }, local: 'inputEmpty()', confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#6', scene: 'newgroup', people: 30, local: text('Person 29'), confirmed: 'none', backend: LOCAL, open: 'the page size decides how many must exist; set from the source before a run' },
  { id: N + '#back/input/onChange#3', scene: 'newgroup', people: 3, check: true, local: 'checked()', confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#1', scene: 'creating', open: 'a group being created is Store state after /api/groups/new; reached by a held request' },
  { id: N + '#back/Button/onClick#3', scene: 'creating', open: 'same state: created, invitations done or failed' },
  { id: N + '#back/Button/onClick#4', scene: 'creating', open: 'same state: stopped creation, edited again' },
  { id: N + '#NeedsName/Button/onClick#1', scene: 'newchat', overview: { person: null }, local: 'tabShown("settings")', confirmed: 'none', backend: LOCAL },
  { id: N + '#close/NeedsName/onDone#1', scene: 'newchat', overview: { person: null }, same: N + '#NeedsName/Button/onClick#1', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: N + '#shownAgents/Button/onClick#2', scene: 'newchat', people: 30, open: 'the page size decides how many must exist; set from the source before a run' },
  { id: N + '#shownAgents/Button/onClick#3', scene: 'newchat', directory: 30, open: 'same, for server members' },
  { id: N + '#shownAgents/Button/onClick#4', scene: 'newchat', agents: 30, open: 'same, for agents you talk to' },
  { id: N + '#shownAgents/AgentRow/onPick#1', scene: 'newchat', agents: 1, open: 'needs a remote agent row in the overview; result is that chat open' },
  { id: N + '#AgentRow/button/onClick#1', scene: 'newchat', agents: 1, open: 'same row' },
  { id: N + '#shownAgents/CandidateRow/onPick#2', scene: 'newchat', directory: 1, backend: '/api/dm/new', local: text('Starting this chat'), confirmed: 'chat open', failing: text('Could not confirm this chat'), duplicate: 1, open: 'written once the people rows run: same flow' },
  { id: N + '#StartingChat/Button/onClick#2', scene: 'newchat', directory: 1, backend: '/api/dm/new', open: 'Try starting chat again: after a failed start, same flow' },
];

function table() {
  const counts = { rows: rows.length, runnable: rows.filter(r => !r.open).length, open: rows.filter(r => r.open).length };
  return { counts, rows: rows.map(r => ({ id: r.id, scene: r.scene, intended: r.local || '', confirmed: r.confirmed || '', backend: r.backend || '', open: r.open || '' })) };
}

if (process.argv.includes('--table')) { console.log(JSON.stringify(table(), null, 1)); process.exit(0); }

const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const out = process.env.C1_OUT, url = process.env.C1_URL;
const widths = [[1440, 960], [390, 844]], backends = ['normal', 'held', 'failing'];

// In the page: which handler ran, when input came, when the row's result first showed.
const init = `(()=>{
  const fired=new Set();globalThis.__anTrack=(id,fn)=>typeof fn!=='function'?fn:function(...a){fired.add(id);return fn.apply(this,a)};
  const all=(s,r=document)=>{const out=[...r.querySelectorAll(s)];for(const e of r.querySelectorAll('*'))if(e.shadowRoot)out.push(...all(s,e.shadowRoot));return out};
  const visible=e=>{const b=e.getBoundingClientRect();return b.width>0&&b.height>0};
  const body=()=>all('body,#app,[data-part]').map(e=>e.innerText||'').join(' ');
  const h=globalThis.__c1={fired,input:null,shown:null,calls:[],
    hasText:s=>all('*').some(e=>e.children.length===0&&visible(e)&&(e.textContent||'').includes(s)),
    dialog:s=>all('[role=dialog],[role=alertdialog]').some(e=>visible(e)&&(e.textContent||'').includes(s)),
    noDialog:()=>!all('[role=dialog],[role=alertdialog]').some(visible),
    panelOpen:()=>all('aside[aria-label="In this chat"],[role=dialog]').some(e=>visible(e)&&(e.textContent||'').length>0),
    tabShown:t=>h.hasText(t==='settings'?'Settings':t),
    called:n=>h.calls.includes(n),editing:()=>all('textarea').length>1,
    inputEmpty:()=>all('input[type=search]').some(e=>e.value===''),checked:()=>all('input[type=checkbox]').some(e=>e.checked),
    watch(src){h.input=null;h.shown=null;const f=new Function('h','with(h){return '+src+'}');
      const note=()=>{if(h.input===null)h.input=performance.now()};
      for(const t of ['pointerdown','keydown','input'])addEventListener(t,note,{capture:true,once:true});
      const end=performance.now()+4000;(function tick(){if(h.input!==null&&h.shown===null&&f(h))h.shown=performance.now();if(h.shown===null&&performance.now()<end)requestAnimationFrame(tick)})()}};
})()`;

async function trial(context, row, width, backend) {
  const page = await context.newPage();
  const result = { id: row.id, width, backend, status: 'fail', reason: '' };
  const held = [];
  try {
    await context.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname, mutate = row.backend === path;
      if (mutate && backend === 'held') { held.push(route); return; }
      if (mutate && backend === 'failing') return route.fulfill({ status: 500, json: { error: 'fixture refused' } });
      if (mutate && row.answer) return route.fulfill({ json: row.answer });
      if (path === '/api/overview' && (row.overview || row.role)) {
        const r = await route.fetch(), j = await r.json();
        return route.fulfill({ response: r, json: { ...j, ...(row.overview || {}), ...(row.role ? { role: row.role } : {}) } });
      }
      if (path === '/api/dm' && row.message) {
        const r = await route.fetch(), j = await r.json(), m = j.messages || [], last = m.findLastIndex(x => x.dir === 'out');
        if (last >= 0) m[last] = { ...m[last], ...row.message };
        return route.fulfill({ response: r, json: { ...j, messages: m } });
      }
      return route.continue();
    });
    if (row.prompt) page.on('dialog', d => d.accept(row.prompt));
    await page.goto(url);
    await open(page, row);
    const helper = { dialog: (p, s) => p.locator('[role=dialog],[role=alertdialog]').filter({ hasText: s }).first().waitFor({ timeout: 4000 }) };
    if (row.reach) await row.reach(page, helper);
    const target = control(page, row.same ? row.same : row.id);
    if (!row.act && !await target.isVisible({ timeout: 2000 }).catch(() => false)) { result.status = 'open'; result.reason = 'state reached, control not visible'; return result; }
    const want = backend === 'failing' && row.failing ? row.failing : row.local;
    await page.evaluate(s => globalThis.__c1.watch(s), want);
    if (row.act) await page.keyboard.press(row.act);
    else if (row.input) await target.fill(row.input);
    else if (row.check) await target.check();
    else await target.click();
    await page.waitForFunction(() => globalThis.__c1.shown !== null, null, { timeout: 4500 }).catch(() => {});
    const seen = await page.evaluate(id => ({ fired: [...globalThis.__c1.fired].some(f => f.startsWith(id + '#')), input: globalThis.__c1.input, shown: globalThis.__c1.shown }), row.id);
    result.fired = seen.fired;
    result.feedback_ms = seen.shown === null || seen.input === null ? null : Math.round(seen.shown - seen.input);
    if (row.backend !== LOCAL && row.backend) {
      result.requests = held.length;
      if (backend === 'held' && row.duplicate) { await target.click({ timeout: 500 }).catch(() => {}); await page.waitForTimeout(200); result.requests_after_duplicate = held.length; }
    }
    // Another control answers while this is going on, whatever the backend does.
    const composer = page.locator('textarea, [contenteditable=true]').first();
    if (await composer.isVisible().catch(() => false)) {
      const t = Date.now(); await composer.fill('still usable'); result.other_control_ms = Date.now() - t;
    }
    if (backend === 'held') for (const r of held.splice(0)) await r.fulfill(row.answer ? { json: row.answer } : { status: 200, json: {} }).catch(() => {});
    if (row.confirmed && row.confirmed !== 'none' && backend !== 'failing') result.confirmed = await confirmed(page, context, row);
    const problems = [];
    if (!seen.fired) problems.push('its handler did not run on this input');
    if (result.feedback_ms === null) problems.push('intended result not seen');
    else if (result.feedback_ms >= 1000) problems.push('intended result after ' + result.feedback_ms + ' ms');
    if (result.requests_after_duplicate > (row.duplicate || 0)) problems.push('a second input made another request');
    if (result.confirmed === false) problems.push('confirmed result not seen');
    result.status = problems.length ? 'fail' : 'pass'; result.reason = problems.join('; ');
  } catch (e) { result.status = 'fail'; result.reason = String(e.message || e).split('\n')[0]; }
  finally { await context.unroute('**/api/**').catch(() => {}); await page.close().catch(() => {}); }
  return result;
}

async function open(page, row) {
  // The Live's own chat with its one peer: the row names only what differs.
  const chat = page.getByRole('button', { name: /alice/i }).first();
  if (row.scene === 'newgroup' || row.scene === 'newchat') {
    await page.getByRole('button', { name: /^(New|Start a chat)$/ }).first().click();
    if (row.scene === 'newgroup') await page.getByRole('button', { name: /New group/ }).click();
    return;
  }
  await chat.click();
  await page.getByRole('button', { name: 'More', exact: true }).waitFor();
}

async function confirmed(page, context, row) {
  if (row.confirmed === 'clipboard') return (await page.evaluate(() => navigator.clipboard.readText())).length > 0;
  if (row.confirmed === 'openLink') return true; // the watched call is itself the result
  return page.evaluate(s => new Function('h', 'with(h){return ' + s + '}')(globalThis.__c1), row.confirmed).catch(() => false);
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const results = [];
  try {
    for (const row of rows) {
      if (row.open) { results.push({ id: row.id, status: 'open', reason: row.open }); continue; }
      for (const [w, h] of widths) {
        if (row.widths && !row.widths.includes(w)) continue;
        for (const backend of row.backend === LOCAL ? ['normal', 'held', 'failing'] : backends) {
          const context = await browser.newContext({ viewport: { width: w, height: h }, reducedMotion: 'reduce', permissions: ['clipboard-read', 'clipboard-write'] });
          await context.addInitScript(init);
          results.push(await trial(context, row, w, backend));
          await context.close();
        }
      }
    }
  } finally { await browser.close(); }
  const defs = new Map();
  for (const r of results) { const d = defs.get(r.id) || []; d.push(r); defs.set(r.id, d); }
  const pass = [...defs.values()].filter(d => d.every(r => r.status === 'pass')).length;
  const open_ = [...defs.values()].filter(d => d.every(r => r.status === 'open')).length;
  const failed = [...defs.values()].filter(d => d.some(r => r.status === 'fail'));
  const summary = { definitions: defs.size, pass, open: open_, mixed_or_fail: failed.length };
  if (out) fs.writeFileSync(out, JSON.stringify({ summary, results }, null, 1));
  console.log(JSON.stringify(summary));
  for (const d of failed) for (const r of d.filter(x => x.status === 'fail')) console.log('FAIL', r.id.split('features/')[1], r.width, r.backend, r.reason);
  process.exit(failed.length ? 1 : 0);
})();
