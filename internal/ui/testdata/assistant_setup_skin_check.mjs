// Classic's and Zoom's "Set up your agents" (skins/*/src/assistant-setup.mjs)
// on a tiny stand-in DOM: the working folder is chosen by browsing
// /api/folders, never typed; a folder that can't be read still leads
// somewhere (Up, Home); review comes before apply; the reviewed change is
// applied before the named agent is made in the chosen folder; a browser
// asks the server nothing, and a server that sets nothing up is shown by
// its own note. Run by TestSkinAssistantSetupFolders.
import assert from 'node:assert/strict';

class Node_ {
  constructor(doc, tag) {
    Object.assign(this, { ownerDocument: doc, tagName: tag.toUpperCase(), children: [], parent: null, className: '', id: '', attributes: {}, dataset: {}, disabled: false, checked: false, value: '', type: '', tabIndex: 0, text: '' });
  }
  set textContent(t) { this.text = String(t); this.children = []; }
  get textContent() { return this.text + this.children.map((c) => c.textContent).join(''); }
  append(...xs) { for (const x of xs) { x.parent = this; this.children.push(x); } }
  replaceChildren(...xs) { for (const c of this.children) c.parent = null; this.children = []; this.append(...xs); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return this.attributes[k] ?? null; }
  get isConnected() { let n = this; while (n.parent) n = n.parent; return n === this.ownerDocument.documentElement; }
  focus() { this.ownerDocument.activeElement = this; }
  all() { return this.children.flatMap((c) => [c, ...c.all()]); }
  matches(sel) {
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    if (sel.startsWith('.')) return this.className.split(/\s+/).includes(sel.slice(1));
    return this.tagName === sel.toUpperCase();
  }
  querySelector(sel) {
    const parts = sel.trim().split(/\s+/);
    let scope = [this];
    for (const p of parts) scope = scope.flatMap((n) => n.all().filter((m) => m.matches(p)));
    return scope[0] || null;
  }
}
class Text_ extends Node_ { constructor(doc, t) { super(doc, '#text'); this.text = t; } }
function makeDocument() {
  const doc = new EventTarget();
  Object.assign(doc, { activeElement: null, visibilityState: 'visible', defaultView: new EventTarget() });
  doc.createElement = (tag) => new Node_(doc, tag);
  doc.createTextNode = (t) => new Text_(doc, t);
  doc.documentElement = doc.createElement('html');
  doc.querySelector = (sel) => doc.documentElement.querySelector(sel);
  return doc;
}

const flush = () => new Promise((r) => setTimeout(r, 0));
const buttons = (root) => root.all().filter((n) => n.tagName === 'BUTTON');
const buttonNamed = (root, name) => {
  const b = buttons(root).find((n) => n.textContent === name || n.getAttribute('aria-label') === name);
  assert.ok(b, 'no button ' + name + ' in: ' + buttons(root).map((n) => n.getAttribute('aria-label') || n.textContent).join(' | '));
  return b;
};
const click = async (root, name) => { const b = buttonNamed(root, name); assert.equal(b.disabled, false, name + ' disabled'); await b.onclick(); await flush(); await flush(); };

function tool(id, over = {}) {
  return { id, label: { claude: 'Claude', codex: 'Codex', omp: 'OMP' }[id], detected: true, configured: false, registered: false, supported: true, state: 'detected', note: 'Installed.', ...over };
}
function server({ folderDelay, agents: start = [], local = true, unsupportedHooks=false } = {}) {
  const calls = [];
  let agents = start;
  let status = 'needs_setup';
  const view = () => ({ local: true, harnesses: [tool('claude', unsupportedHooks?{state:'unsupported',detected:false,supported:false}: {state:status, configured:status !== 'needs_setup', registered:status === 'connected'}), tool('codex'), tool('omp', { detected: false, state: 'not_detected' })] });
  const home = { path: '/home/me', parent: '/home', home: '/home/me', dirs: ['work', 'notes', 'locked'], truncated: true };
  const folders = {
    '/api/folders': home,
    '/api/folders?path=%2Fhome%2Fme': home,
    '/api/folders?path=%2Fhome%2Fme%2Fwork': { path: '/home/me/work', parent: '/home/me', home: '/home/me', dirs: [] },
  };
  const api = async (path, body) => {
    calls.push(body === undefined ? ['GET', path] : ['POST', path, body]);
    if (path.startsWith('/api/folders')) {
      if (folderDelay) await folderDelay;
      if (!folders[path]) throw new Error('That folder can’t be read.');   // deleted, or closed to this person
      return folders[path];
    }
    if (path === '/api/assistant-setup' && body === undefined && !local) return { local: false, harnesses: [], note: 'This browser cannot inspect or install tools. Open Settings on your AgentNet computer.' };
    if (path === '/api/agents' && !local) throw new Error('Agent catalog unavailable.');
    if (path === '/api/assistant-setup' && body === undefined) return view();
    if (path === '/api/assistant-setup' && body.action === 'review') return { ...view(), review_id: 'r1', note: 'Only selected.' };
    if (path === '/api/assistant-setup' && body.action === 'apply') { assert.equal(body.review_id, 'r1'); return { ...view(), note: 'Saved.' }; }
    if (path === '/api/agents' && body === undefined) return { host: 'me/laptop', local: true, agents, harnesses: [{ name: 'claude', found: true }, { name: 'codex', found: true }] };
    if (path === '/api/agents' && body.action === 'create') {
      const a = { record: { id: 'a1', label: body.label }, enabled: true, responder: { harness: body.harness, dir: body.dir } };
      agents = [a];
      return { saved: true, published: true, agent: a };
    }
    if (path === '/api/agents' && body.action === 'publish') return { saved: true, published: true };
    throw new Error('unexpected ' + path);
  };
  return { calls, api, status: value => { status = value; } };
}

for (const skin of ['classic', 'zoom']) {
  const { mountAssistantSetup, folderEntries, agentStatus } = await import('../skins/' + skin + '/src/assistant-setup.mjs');
  for(const state of ['needs_setup','needs_activation','connected','error'])assert.equal(agentStatus(tool('claude',{state}),{agents:[{record:{label:'Casey'},enabled:true,responder:{harness:'claude',ready:true}}]}).word,'Ready',skin+' hook state cannot downgrade named agent');
  assert.deepEqual(folderEntries({ path: 'C:\\Users', dirs: ['me'] }), [{ name: 'me', path: 'C:\\Users\\me' }], skin);
  assert.deepEqual(folderEntries({ path: '/', dirs: ['home', { name: 'x', path: '/x' }] }), [{ name: 'home', path: '/home' }, { name: 'x', path: '/x' }], skin);

  // Returning from native setup/session activation reads status only and
  // preserves the person's current choices; hidden/review views stay put.
  {
    const doc = makeDocument(), root = doc.createElement('div'), s = server();
    doc.documentElement.append(root);
    const controller = await mountAssistantSetup({ root, api: s.api });
    await click(root, 'Set up your agents');
    root.querySelector('#setup-tool-codex').checked = true;
    root.querySelector('#setup-tool-codex').onchange();
    s.status('needs_activation');
    const before = s.calls.length;
    doc.defaultView.dispatchEvent(new Event('focus')); await flush(); await flush();
    assert.deepEqual(s.calls.slice(before), [['GET', '/api/assistant-setup'],['GET','/api/agents']], skin);
    assert.match(root.textContent, /Choose a name and working folder/, skin);
    assert.match(root.textContent, /Needs activation/, skin);
    assert.equal(root.querySelector('#setup-tool-codex').checked, true, skin);
    doc.visibilityState = 'hidden'; s.status('connected');
    const hidden = s.calls.length;
    doc.dispatchEvent(new Event('visibilitychange')); await flush();
    assert.equal(s.calls.length, hidden, skin);
    doc.visibilityState = 'visible'; doc.dispatchEvent(new Event('visibilitychange')); await flush(); await flush();
    assert.match(root.textContent, /Registered context/, skin);
    assert.ok(s.calls.every(c => c[0] === 'GET'), skin);
    controller.dispose(); const ended = s.calls.length;
    doc.defaultView.dispatchEvent(new Event('focus')); await flush();
    assert.equal(s.calls.length, ended, skin);
  }

  // Hook installation is unsupported, but the existing named agent remains configurable.
  {
   const doc=makeDocument(),root=doc.createElement('div'),s=server({unsupportedHooks:true,agents:[{record:{id:'existing',label:'Casey'},enabled:true,responder:{harness:'claude',dir:'/home/me/work',ready:true}}]});doc.documentElement.append(root);
   await mountAssistantSetup({root,api:s.api});await click(root,'Set up your agents');
   assert.equal(root.querySelector('#setup-tool-claude').disabled,false,skin);
   assert.equal(root.querySelector('select'),null,skin+' sole exact agent needs no selector');
   assert.match(root.textContent,/Agent: Casey/);
   assert.match(root.textContent,/Ready.*Casey is set up/);
   await click(root,'Review changes');await click(root,'Confirm setup');
   assert.match(root.textContent,/Ready.*Casey is set up/);
   assert(s.calls.filter(c=>c[1]==='/api/assistant-setup').every(c=>c[0]==='GET'),'no unsupported native review/install');
   assert(s.calls.some(c=>c[1]==='/api/agents'&&c[2]?.action==='publish'),'existing named agent flow remains usable');
  }

  // A browser asks the server nothing and says where setup happens.
  {
    const doc = makeDocument(), root = doc.createElement('div'), s = server();
    doc.documentElement.append(root);
    await mountAssistantSetup({ root, api: s.api, isBrowser: true });
    assert.match(root.textContent, /native AgentNet computer/, skin);
    assert.deepEqual(s.calls, [], skin);
  }

  const doc = makeDocument(), root = doc.createElement('div'), s = server();
  doc.documentElement.append(root);
  await mountAssistantSetup({ root, api: s.api, suggestedHarness: 'claude' });
  assert.match(root.textContent, /Your answers use Claude/, skin);
  await click(root, 'Set up your agents');
  assert.match(root.textContent, /connection is out of date or still points to your previous AgentNet/, skin);
  // No typed folder anywhere: the only text field is a new agent's name.
  const claude = root.querySelector('#setup-tool-claude');
  claude.checked = true; claude.onchange(); await flush();
  const fields = root.all().filter((n) => n.tagName === 'INPUT' && n.type === 'text');
  assert.deepEqual(fields.map((f) => f.getAttribute('aria-label')), ['Claude assistant name'], skin);
  assert.match(root.textContent, /No folder chosen yet/, skin);

  // Review waits for a folder.
  await click(root, 'Review changes');
  assert.match(root.textContent, /working folder for Claude before reviewing/, skin);
  assert.ok(!s.calls.some((c) => c[0] === 'POST'), skin);

  // Browse: home first, then into a subfolder, then use it.
  await click(root, 'Choose working folder for Claude');
  assert.deepEqual(s.calls.at(-1), ['GET', '/api/folders'], skin);
  assert.match(root.textContent, /\/home\/me/, skin);
  assert.match(root.textContent, /Some folders in here are not shown/, skin);
  await click(root, 'work');
  assert.deepEqual(s.calls.at(-1), ['GET', '/api/folders?path=%2Fhome%2Fme%2Fwork'], skin);
  assert.match(root.textContent, /No folders inside this one/, skin);
  await click(root, 'Use this folder');
  assert.equal(root.querySelector('.setup-folders'), null, skin);
  assert.match(root.textContent, /\/home\/me\/work/, skin);
  assert.equal(buttonNamed(root, 'Change working folder for Claude').textContent, 'Change folder…', skin);

  // Review, then apply exactly the reviewed change, then the agent in that folder.
  await click(root, 'Review changes');
  assert.deepEqual(s.calls.at(-1), ['POST', '/api/assistant-setup', { action: 'review', harnesses: ['claude'] }], skin);
  assert.match(root.textContent, /Working folder: \/home\/me\/work/, skin);
  const before = s.calls.length;
  await click(root, 'Confirm setup');
  const posts = s.calls.slice(before).filter((c) => c[0] === 'POST').map((c) => c[2]);
  assert.deepEqual(posts, [
    { action: 'apply', harnesses: ['claude'], review_id: 'r1' },
    { action: 'create', label: 'Claude', harness: 'claude', dir: '/home/me/work' },
    { action: 'publish' },
  ], skin);
  assert.match(root.textContent, /Setup result/, skin);

  // An answer for a folder the person stopped browsing is dropped.
  {
    let release;
    const gate = new Promise((r) => { release = r; });
    const doc2 = makeDocument(), root2 = doc2.createElement('div'), s2 = server({ folderDelay: gate });
    doc2.documentElement.append(root2);
    await mountAssistantSetup({ root: root2, api: s2.api });
    await click(root2, 'Set up your agents');
    const c2 = root2.querySelector('#setup-tool-claude');
    c2.checked = true; c2.onchange(); await flush();
    const opening = buttonNamed(root2, 'Choose working folder for Claude').onclick();
    await flush();
    assert.match(root2.textContent, /Reading folders/, skin);
    await click(root2, 'Cancel');
    release(); await opening; await flush();
    assert.equal(root2.querySelector('.setup-folders'), null, skin);
    assert.match(root2.textContent, /No folder chosen yet/, skin);
  }

  // An agent whose folder was deleted: browsing starts there and fails, yet
  // Home and Up still lead to folders that can be used. Never only "Try again".
  {
    const gone = { record: { id: 'a0', label: 'Old' }, enabled: true, responder: { harness: 'claude', dir: '/home/me/gone' } };
    const doc3 = makeDocument(), root3 = doc3.createElement('div'), s3 = server({ agents: [gone] });
    doc3.documentElement.append(root3);
    await mountAssistantSetup({ root: root3, api: s3.api });
    await click(root3, 'Set up your agents');
    const c3 = root3.querySelector('#setup-tool-claude');
    c3.checked = true; c3.onchange(); await flush();
    await click(root3, 'Change working folder for Claude');
    assert.deepEqual(s3.calls.at(-1), ['GET', '/api/folders?path=%2Fhome%2Fme%2Fgone'], skin);
    assert.match(root3.textContent, /can’t be read/, skin);
    assert.deepEqual(buttons(root3.querySelector('.setup-folders')).map((b) => b.textContent), ['Up', 'Home', 'Cancel', 'Try again'], skin);
    await click(root3, 'Home');
    assert.deepEqual(s3.calls.at(-1), ['GET', '/api/folders'], skin);
    assert.deepEqual(buttons(root3).filter((b) => ['work', 'Use this folder'].includes(b.textContent)).map((b) => b.disabled), [false, false], skin);
    // A subfolder closed to this person: Up leads back to the folder it is in.
    await click(root3, 'locked');
    assert.deepEqual(s3.calls.at(-1), ['GET', '/api/folders?path=%2Fhome%2Fme%2Flocked'], skin);
    assert.deepEqual(buttons(root3.querySelector('.setup-folders')).map((b) => b.textContent), ['Up', 'Home', 'Cancel', 'Try again'], skin);
    await click(root3, 'Up');
    assert.deepEqual(s3.calls.at(-1), ['GET', '/api/folders?path=%2Fhome%2Fme'], skin);
    await click(root3, 'Use this folder');
    assert.equal(root3.querySelector('.setup-folders'), null, skin);
    assert.match(root3.textContent, /\/home\/me/, skin);
    assert.doesNotMatch(root3.textContent, /\/home\/me\/gone/, skin);
  }

  // A server that sets nothing up answers with its note; the agent list
  // (which would only fail) is not asked for.
  {
    const doc4 = makeDocument(), root4 = doc4.createElement('div'), s4 = server({ local: false });
    doc4.documentElement.append(root4);
    await mountAssistantSetup({ root: root4, api: s4.api });
    await click(root4, 'Set up your agents');
    assert.match(root4.textContent, /Open Settings on your AgentNet computer/, skin);
    assert.doesNotMatch(root4.textContent, /Agent catalog unavailable/, skin);
    assert.deepEqual(s4.calls, [['GET', '/api/assistant-setup']], skin);
  }
}
console.log('skin assistant setup ok');
