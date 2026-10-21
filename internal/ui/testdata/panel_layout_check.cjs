// Execute the actual shell and named-agent components without a browser or
// live host. The rendered desktop/phone journey supplies CSS/focus evidence.
const assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');
const esbuild = require('../web/node_modules/esbuild');
const base = process.env.AGENTNET_PANEL_SOURCE || path.join(__dirname, '../web/src');
const jsx = (tag, props, ...children) => ({tag, props:{...props, children:children.flat(Infinity).filter(x => x !== false && x != null)}});
const nodes = tree => tree && typeof tree === 'object' ? [tree, ...(tree.props?.children || []).flatMap(nodes)] : [];
const named = (tree, tag) => nodes(tree).filter(node => node.tag === tag);
function load(file, globals, extra = '') {
  const source = fs.readFileSync(path.join(base, file), 'utf8').replace(/^import[\s\S]*?from[^;]+;/gm, '');
  const env = {module:{exports:{}}, jsx, Fragment:'Fragment', console, Date, Map, Set, WeakMap, ...globals};
  env.exports = env.module.exports;
  vm.runInNewContext(esbuild.transformSync(source + extra, {loader:file.endsWith('tsx') ? 'tsx' : 'ts', jsxFactory:'jsx', jsxFragment:'Fragment', format:'cjs'}).code, env);
  return env.module.exports;
}
const {outboxAttention} = load('outbox-attention.ts', {});
const hooks = {
  useState: initial => [typeof initial === 'function' ? initial() : initial, () => assert.fail('render must not change state')],
  useEffect(){}, useLayoutEffect(){}, useRef: current => ({current}),
  useStore:(store, select) => select(store.state),
};
const icons = Object.fromEntries(['IconMessageCircle','IconRobot','IconCircleCheck','IconSettings','IconInbox'].map(name => [name, name]));
const globals = {...hooks, ...icons, outboxAttention,
  Swap:'Swap', Conversation:'Conversation', NothingOpen:'NothingOpen', RoomPanel:'RoomPanel',
  SettingsPane:'SettingsPane', ConnectionBanner:'ConnectionBanner', NativeRecovery:'NativeRecovery',
  WorkspaceCoin:'WorkspaceCoin', PersonAvatar:'PersonAvatar', Toasts:'Toasts',
  MOTION:{page:0,pane:0}, convKey:open => open.kind + ':' + open.id,
};
const shell = load('App.tsx', globals, '\nexport {Desktop, PhoneTabs};');
const file = {name:'original-file.txt', bytes:new Uint8Array([1,2,3])};
const open = {kind:'thread', id:'exact-current-agent-conversation'};
const draft = {text:'Unsent original text', files:[{file}], agent:'exact-agent-id'};
const overview = {remind:true, reminders:[{overdue:true}]};
const store = {state:{open, views:{}, drafts:{[open.id]:draft}}, showTab(){assert.fail('render must not navigate');}};
for (const tab of ['agents','oks','outbox','chats','settings']) {
  const page = jsx('CurrentPage', {tab});
  const tree = shell.Desktop({tab, oks:2, overview, store, main:page});
  const main = named(tree, 'main')[0], aside = named(tree, 'aside');
  assert(main, tab + ' has a workspace');
  if (['agents','oks','outbox'].includes(tab)) {
    assert.equal(aside.length, 0, tab + ' is not squeezed into the chat-list column');
    assert.equal(named(main, 'CurrentPage').length, 1, tab + ' occupies the main pane');
    assert.equal(named(main, 'Conversation').length, 0, tab + ' does not show an unrelated chat');
    assert.equal(named(main, 'RoomPanel').length, 0, tab + ' does not show an unrelated guest panel');
  } else {
    assert.equal(aside.length, 1, tab + ' retains its navigation/list column');
    assert.equal(named(aside[0], 'CurrentPage').length, 1);
    if (tab === 'chats') {
      assert.strictEqual(named(main, 'Conversation')[0].props.o, open, 'Chats restores the exact open conversation');
      assert.equal(named(main, 'RoomPanel').length, 1);
    } else {
      assert.equal(named(main, 'SettingsPane').length, 1, 'Settings keeps details in the main pane');
      assert.equal(named(main, 'Conversation').length, 0);
    }
  }
  const outbox = named(tree, 'button').find(node => node.props['aria-label']?.startsWith('Outbox,'));
  assert.equal(outbox.props['aria-label'], 'Outbox, 1 reminder due');
  assert.strictEqual(store.state.open, open);
  assert.strictEqual(store.state.drafts[open.id], draft);
  assert.strictEqual(draft.files[0].file, file, 'navigation leaves original File identity intact');
  const phone = shell.PhoneTabs({tab, oks:2, overview, store, main:page, scroll:{current:0}});
  assert.equal(named(phone, 'CurrentPage').length, 1);
  assert.equal(named(phone, 'nav').length, 1, 'phone retains bottom navigation');
  assert.equal(named(phone, 'button').find(node => node.props['aria-label']?.startsWith('Outbox,')).props['aria-label'], 'Outbox, 1 reminder due');
}

// A long concrete name cannot clip its default badge or alter its chat target.
const agents = load('features/AgentsView.tsx', {...hooks,
  Switch:{Root:'AgentSwitch', Thumb:'SwitchThumb'}, AgentAvatar:'AgentAvatar', AgentModelReport:'AgentModelReport',
  Tag:'Tag', AgentChatButton:'AgentChatButton', Details:'Details', Row:'Row',
  niceDevice:host => host, capital:s => s,
}, '\nexport {NamedAgent};');
const record = {id:'a'.repeat(32), host:'atlas/laptop', host_key:'original-pinned-key', harness:'codex', label:'Long agent name '.repeat(20)};
const row = agents.NamedAgent({a:{record, enabled:true, default:true, responder:{ready:true,harness:'codex',dir:'/original/project'}}, label:record.label, o:{person:{agents:[]}}, onToggle(){}});
const name = named(row, 'p').find(node => node.props.title === record.label);
assert(name, 'long name retains its full tooltip');
const nameLine = nodes(row).find(node => node.props?.children?.includes(name));
assert.equal(named(nameLine, 'Tag').length, 1, 'Default is beside the truncated name');
assert(!nameLine.props.className.includes('truncate'), 'the name cannot clip the Default badge');
assert(named(nameLine, 'Tag')[0].props.className.includes('shrink-0'), 'Default keeps its readable width');
assert.deepEqual({...named(row, 'AgentChatButton')[0].props.target}, {host:record.host,agentId:record.id,harness:record.harness,hostKey:record.host_key,label:record.label,local:true,unavailable:undefined});
console.log('panel layout component check PASS: desktop task panes, split lists/settings, phone tabs, exact open/draft/File state and long agent names');
