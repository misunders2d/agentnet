// Exercise the real Store's asynchronous load and the timeline's seen hook.
// No browser, network, React renderer, or model is needed for this lifecycle race.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import { runInNewContext } from 'node:vm';
import { api, errorText } from '../web/src/api.ts';
import model from './comic_model.mjs';
import { pendingSends } from '../static/optimistic.mjs';
import { share } from '../web/src/structural.ts';
import { mainPreferences, personRoots } from '../skins/shared/person-topics.mjs';
const { messageTarget } = model;

const storeSource = stripTypeScriptTypes(
  (await readFile(new URL('../web/src/store.ts', import.meta.url), 'utf8'))
    .replace(/^import .*;$/gm, '')
    // Node strips types but not parameter properties; preserve that assignment.
    .replace('constructor(readonly host: Host) {', 'constructor(host: Host) { this.host = host;'),
).replace(/^export /gm, '');
const timelineSource = await readFile(new URL('../web/src/features/Conversation.timeline.tsx', import.meta.url), 'utf8');
const seenSource = stripTypeScriptTypes(timelineSource.slice(
  timelineSource.indexOf('function useSeen('), timelineSource.indexOf('export function EmptyTimeline('),
));
const tick = () => new Promise(resolve => setImmediate(resolve));
class Events {
  listeners = new Map();
  addEventListener(name, fn) { if (!this.listeners.has(name)) this.listeners.set(name, new Set()); this.listeners.get(name).add(fn); }
  removeEventListener(name, fn) { this.listeners.get(name)?.delete(fn); }
  fire(name) { for (const fn of this.listeners.get(name) || []) fn(); }
}
// host.android's foreground signal, as static/android.js provides it.
class Native {
  visible = true; listeners = new Set();
  api() { return { visible: () => this.visible, onVisibility: fn => { this.listeners.add(fn); return () => { this.listeners.delete(fn); }; } }; }
  set(visible) { this.visible = visible; for (const fn of [...this.listeners]) fn(visible); }
}
function environment() {
  const window = new Events(), document = new Events(), native = new Native();
  document.visibilityState = 'visible'; document.focused = true;
  document.hasFocus = () => document.focused;
  const values = new Map();
  const localStorage = { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) };
  return { window, document, localStorage, setTimeout, clearTimeout, native };
}
async function storeJourney(platform) {
  const env = environment(), reads = [], messages = [{ id: 'message-one', dir: 'in', unread: true, topic: '' }];
  let push, completeLoad;
  const deferred = new Promise(resolve => { completeLoad = resolve; });
  let firstLoad = true;
  const view = () => ({ id: 'conversation', messages: messages.map(m => ({ ...m })), agents: [] });
  const host = { platform, ...(platform === 'android' ? { android: env.native.api() } : {}), workspace: { id: 'default' }, listen: fn => { push = fn; return () => { push = null; }; },
    async api(path, body) {
      if (path.startsWith('/api/overview')) return { version: 'test', me: {}, threads: [], dms: [] };
      if (path.startsWith('/api/agents')) return { agents: [] };
      if (path.startsWith('/api/dm?')) { if (firstLoad) { firstLoad = false; return deferred; } return view(); }
      if (path === '/api/act') {
        assert.equal(body.do, 'read'); reads.push(Array.from(body.ids));
        for (const m of messages) if (body.ids.includes(m.id)) m.unread = false;
        return {};
      }
      if (path.startsWith('/api/typing') || path === '/api/refresh') return {};
      throw Error('Unexpected route: ' + path);
    },
  };
  const { native, ...globals } = env;
  const Store = runInNewContext(storeSource + '\nStore', { ...globals, api, errorText, pendingSends, share, messageTarget, mainPreferences, personRoots, brand: { storage: 'agentnet.messenger' } });
  const store = new Store(host);
  await store.start();
  const opened = store.open({ kind: 'dm', id: 'conversation' });
  env.native.visible = false;
  completeLoad(view()); await opened; await tick();
  if (platform === 'daemon') {
    assert.equal(reads.length, 1, 'desktop behavior remains unchanged'); store.stop(); return;
  }
  assert.equal(reads.length, 0, 'an admitted load finishing after native pause leaves messages unread');
  env.document.focused = false;
  env.native.set(true); await tick();
  assert.equal(reads.length, 0, 'native resume alone cannot read an unfocused page');
  env.document.focused = true; env.document.visibilityState = 'hidden';
  env.window.fire('focus'); await tick();
  assert.equal(reads.length, 0, 'focus alone cannot read a document hidden by WebView');
  env.document.visibilityState = 'visible'; env.document.fire('visibilitychange'); await tick();
  assert.deepEqual(reads, [['message-one']], 'visible focused resume acknowledges the completed load without another push');
  env.native.set(false);
  messages.push({ id: 'message-two', dir: 'in', unread: true, topic: '' });
  push({ type: 'change', seq: 2 }); await tick();
  assert.equal(reads.length, 1, 'hidden changes do not read new messages');
  env.native.set(true); await tick(); await tick();
  assert.deepEqual(reads, [['message-one'], ['message-two']], 'resume refreshes and acknowledges the hidden change once');
  store.stop();
  assert.equal(env.window.listeners.get('focus').size, 0);
  assert.equal(env.native.listeners.size, 0);
}
await storeJourney('android');
await storeJourney('daemon');

const env = environment(), seen = [], cleanups = [];
env.native.visible = false;
const messages = [{ id: 'one', dir: 'in' }], bottom = { current: true };
const store = { host: { platform: 'android', android: env.native.api() }, api: { notify: async (what, request) => { assert.equal(what, 'seen'); seen.push(Array.from(request.ids)); } } };
const { native: _native, ...seenGlobals } = env;
const useSeen = runInNewContext(seenSource + '\nuseSeen', { ...seenGlobals, useApp: () => store, useRef: value => ({ current: value }), useEffect: fn => cleanups.push(fn()), ev: m => !!m.event });
const report = useSeen({ dm: { id: 'conversation' }, overview: { notify: { enabled: true } } }, messages, bottom);
assert.equal(seen.length, 0, 'native-hidden timeline cannot suppress an alert despite visible focused DOM');
env.document.focused = false;
env.native.set(true); assert.equal(seen.length, 0);
env.document.focused = true; env.window.fire('focus');
assert.deepEqual(seen, [['one']], 'focus after native resume reports the presented message');
env.native.set(true); assert.equal(seen.length, 1, 'visibility events cannot duplicate a seen report');
messages.push({ id: 'two', dir: 'in' }); bottom.current = false; report(); assert.equal(seen.length, 1);
bottom.current = true; report(); assert.deepEqual(seen, [['one'], ['one', 'two']]);
for (const cleanup of cleanups) cleanup();
assert.equal(env.native.listeners.size, 0);
assert.equal(env.window.listeners.get('focus').size, 0);
console.log('PASS Android in-flight unread pause/resume, focus, hidden push, seen suppression and listener cleanup; desktop unchanged');
