// Comic's "Connect your coding sessions" rules (web/src/features/
// AssistantSetup.model.ts), run by node as they are (type imports only).
// Run by TestComicAssistantSetupModel.
import assert from 'node:assert/strict';
import {
  agentsFor, applySetup, browserDevice, canHaveAgent, firstPick, folderEntries, folderName, folderWayOut, notReady, parentFolder, pickFor, readSetup, savedLine, startChosen, STATE_SENTENCE, STATE_WORDS,
} from '../web/src/features/AssistantSetup.model.ts';

const tool = (id, over = {}) => ({ id, label: { claude: 'Claude', codex: 'Codex', pi: 'Pi', omp: 'OMP' }[id], detected: true, configured: false, registered: false, supported: true, state: 'detected', note: 'server note', ...over });
const agent = (id, label, harness, dir, enabled = true, ready = true) => ({ record: { v: 1, id, host: 'h', host_key: 'k', label, ts: 1 }, enabled, responder: { chosen: true, manual: false, harness, dir, ready, ...(ready ? {} : { problem: 'The responder directory ' + dir + ' does not exist.' }), harnesses: null } });
const catalogOf = (agents, found = ['claude', 'codex', 'pi']) => ({ host: 'h', local: true, agents, harnesses: ['claude', 'codex', 'pi'].map((name) => ({ name, found: found.includes(name) })) });

// The tools ticked when the list opens: set up already, found and safe.
{
  const v = { local: true, harnesses: [tool('claude', { configured: true }), tool('codex'), tool('pi', { configured: true, supported: false }), tool('omp', { configured: true, detected: false })] };
  assert.deepEqual([...startChosen(v)], ['claude']);
  assert.deepEqual([...startChosen({ local: true, harnesses: null })], []);
}

// A browser device installs nothing; a computer does.
assert.equal(browserDevice('browser', null), true);
assert.equal(browserDevice('daemon', { device: {} }), true);
assert.equal(browserDevice('daemon', { me: {} }), false);

// The agent a tool gets: its only agent, a new one named after the tool, or
// "choose" when there are several. Turned-off agents and other tools' do not count.
{
  const c = catalogOf([agent('a1', 'Stocky', 'claude', '/w/stock'), agent('a2', 'Old', 'claude', '/w/old', false), agent('a3', 'Coder', 'codex', '/w/c1'), agent('a4', 'Coder 2', 'codex', '/w/c2')]);
  assert.deepEqual(agentsFor(c, 'claude').map((a) => a.record.id), ['a1']);
  assert.deepEqual(firstPick(tool('claude'), c), { id: 'a1', label: 'Stocky', dir: '/w/stock', mustChoose: false });
  assert.deepEqual(firstPick(tool('pi'), c), { id: '', label: 'Pi', dir: '', mustChoose: false });
  assert.deepEqual(firstPick(tool('codex'), c), { id: '', label: 'Codex', dir: '', mustChoose: true });
  assert.equal(canHaveAgent(c, 'claude'), true);
  assert.equal(canHaveAgent(c, 'omp'), false);
  assert.equal(canHaveAgent(catalogOf([], ['codex']), 'claude'), false);
  const picks = new Map([['pi', { id: '', label: 'Helper', dir: '/w/h', mustChoose: false }]]);
  assert.equal(pickFor(picks, tool('pi'), c).label, 'Helper');
}

// Review needs a name and a folder (chosen, never typed) for every tool that
// gets an agent, and a decision when several agents run the same tool.
{
  const c = catalogOf([agent('a3', 'Coder', 'codex', '/w/c1'), agent('a4', 'Coder 2', 'codex', '/w/c2')]);
  assert.match(notReady([], c, new Map()), /at least one/);
  assert.match(notReady([tool('claude')], c, new Map()), /folder the agent for Claude/);
  assert.match(notReady([tool('claude')], c, new Map([['claude', { id: '', label: '  ', dir: '/w', mustChoose: false }]])), /name/);
  assert.match(notReady([tool('codex')], c, new Map()), /Choose which agent Codex uses/);
  assert.equal(notReady([tool('codex')], c, new Map([['codex', { id: 'a4', label: 'Coder 2', dir: '/w/c2', mustChoose: false }]])), '');
  // OMP is connected for its sessions only: no agent, so no folder is asked for.
  assert.equal(notReady([tool('omp')], c, new Map()), '');
}

// A fake host: records every call in order.
function host({ catalog, applyFails = false, saveFails = false, publish = true, local = true, readyAfter = true }) {
  const calls = [];
  let agents = catalog.agents.slice(), n = 0;
  return {
    calls,
    setup: async (r) => {
      calls.push(['setup', r]);
      if (applyFails) throw new Error('Setup changed since your review.');
      if (!local) return { local: false, harnesses: [], note: 'This browser cannot inspect or install tools.' };
      return { local: true, harnesses: [tool('claude', { configured: true, state: 'needs_activation' })], note: 'saved' };
    },
    agents: async () => { calls.push(['agents']); if (!local) throw new Error('Agent catalog unavailable.'); return { ...catalog, agents: agents.slice() }; },
    changeAgents: async (c) => {
      calls.push(['change', c]);
      if (c.action === 'publish') return { saved: true, published: publish, note: '' };
      if (saveFails) return { saved: false, published: false, note: '' };
      if (c.action === 'create') { const a = agent('new' + ++n, c.label, c.harness, c.dir, true, readyAfter); agents.push(a); return { saved: true, published: true, agent: agent(a.record.id, c.label, c.harness, c.dir), note: '' }; }
      const i = agents.findIndex((a) => a.record.id === c.id);
      agents[i] = agent(c.id, agents[i].record.label, c.harness, c.dir);
      return { saved: true, published: true, agent: agents[i], note: '' };
    },
  };
}
const changes = (h) => h.calls.filter((c) => c[0] === 'change').map((c) => c[1]);

// Fresh setup: the reviewed change is applied first (exact tools and review
// id), then a new agent with the trimmed name and chosen folder, then one share.
{
  const h = host({ catalog: catalogOf([]) });
  const picks = new Map([['claude', { id: '', label: '  Stocky ', dir: ' /w/stock ', mustChoose: false }]]);
  const r = await applySetup(h, [tool('claude'), tool('omp')], 'rev-1', catalogOf([]), picks);
  assert.deepEqual(h.calls[0], ['setup', { action: 'apply', harnesses: ['claude', 'omp'], review_id: 'rev-1' }]);
  assert.deepEqual(changes(h), [{ action: 'create', label: 'Stocky', harness: 'claude', dir: '/w/stock' }, { action: 'publish' }]);
  assert.deepEqual(r.agents, [{ label: 'Stocky', ready: true, problem: '' }]);
  assert.equal(r.shared, true);
  assert.deepEqual(r.picks.get('claude'), { id: 'new1', label: 'Stocky', dir: '/w/stock', mustChoose: false });
  assert.equal(r.view.note, 'saved');
}

// A rerun (or a retry after a lost answer) reuses the agent saved before:
// same tool, name and folder means no second agent.
{
  const c = catalogOf([agent('a1', 'Stocky', 'claude', '/w/stock')]);
  const h = host({ catalog: c });
  const r = await applySetup(h, [tool('claude')], 'rev', catalogOf([]), new Map([['claude', { id: '', label: 'Stocky', dir: '/w/stock', mustChoose: false }]]));
  assert.deepEqual(changes(h), [{ action: 'publish' }]);
  assert.deepEqual(r.agents.map((a) => a.label), ['Stocky']);
}

// "Ready" is the server's own check of the saved agent (its program here,
// its folder there), read after the save, never assumed from the save.
{
  const h = host({ catalog: catalogOf([]), readyAfter: false });
  const r = await applySetup(h, [tool('claude')], 'rev', catalogOf([]), new Map([['claude', { id: '', label: 'Stocky', dir: '/w/gone', mustChoose: false }]]));
  assert.deepEqual(r.agents, [{ label: 'Stocky', ready: false, problem: 'The responder directory /w/gone does not exist.' }]);
  assert.doesNotMatch(savedLine(r.agents[0]), /ready/);
  assert.match(savedLine(r.agents[0]), /Stocky is saved, but it can’t start/);
  assert.equal(savedLine({ label: 'Stocky', ready: true, problem: '' }), 'Your agent Stocky is ready.');
}

// Reading the list: an installation that sets nothing up answers with its
// own note, and the agent list is not asked for (it would only fail).
{
  const h = host({ catalog: catalogOf([]), local: false });
  const r = await readSetup(h);
  assert.equal(r.view.local, false);
  assert.match(r.view.note, /cannot inspect or install/);
  assert.equal(r.catalog, null);
  assert.deepEqual(h.calls.map((c) => c[0]), ['setup']);
  const h2 = host({ catalog: catalogOf([agent('a1', 'Stocky', 'claude', '/w')]) });
  const r2 = await readSetup(h2);
  assert.deepEqual(r2.catalog.agents.map((a) => a.record.id), ['a1']);
  assert.deepEqual(h2.calls.map((c) => c[0]), ['setup', 'agents']);
}

// Keeping an agent but choosing another folder updates it; its signed name is never sent.
{
  const c = catalogOf([agent('a1', 'Stocky', 'claude', '/w/old')]);
  const h = host({ catalog: c });
  await applySetup(h, [tool('claude')], 'rev', c, new Map([['claude', { id: 'a1', label: 'Stocky', dir: '/w/new', mustChoose: false }]]));
  assert.deepEqual(changes(h), [{ action: 'update', id: 'a1', harness: 'claude', dir: '/w/new' }, { action: 'publish' }]);
}

// An agent turned off (or gone, or moved to another tool) since the list was
// read stops the run: nothing is created in its place.
for (const now of [[agent('a1', 'Stocky', 'claude', '/w', false)], [], [agent('a1', 'Stocky', 'codex', '/w')]]) {
  const h = host({ catalog: catalogOf(now) });
  await assert.rejects(applySetup(h, [tool('claude')], 'rev', catalogOf([agent('a1', 'Stocky', 'claude', '/w')]), new Map([['claude', { id: 'a1', label: 'Stocky', dir: '/w', mustChoose: false }]])), /changed or was turned off/);
  assert.deepEqual(changes(h), []);
}

// A refused apply (the change is no longer the reviewed one) touches no agent.
{
  const h = host({ catalog: catalogOf([]), applyFails: true });
  await assert.rejects(applySetup(h, [tool('claude')], 'stale', catalogOf([]), new Map([['claude', { id: '', label: 'S', dir: '/w', mustChoose: false }]])), /changed since your review/);
  assert.deepEqual(h.calls.map((c) => c[0]), ['setup']);
}

// A save that is not confirmed is not reported as saved.
{
  const h = host({ catalog: catalogOf([]), saveFails: true });
  await assert.rejects(applySetup(h, [tool('claude')], 'rev', catalogOf([]), new Map([['claude', { id: '', label: 'S', dir: '/w', mustChoose: false }]])), /wasn’t confirmed/);
  assert.ok(!changes(h).some((c) => c.action === 'publish'));
}

// Sessions only (OMP): no agent call, nothing to share.
{
  const h = host({ catalog: catalogOf([]) });
  const r = await applySetup(h, [tool('omp')], 'rev', catalogOf([]), new Map());
  assert.deepEqual(changes(h), []);
  assert.equal(r.shared, null);
  assert.deepEqual(r.agents, []);
}

// Saved here but not shared is said, not hidden.
{
  const h = host({ catalog: catalogOf([]), publish: false });
  const r = await applySetup(h, [tool('pi')], 'rev', catalogOf([]), new Map([['pi', { id: '', label: 'Pi', dir: '/w', mustChoose: false }]]));
  assert.equal(r.shared, false);
}

// Every tool state the server reports has its words.
for (const state of ['connected', 'needs_activation', 'detected', 'needs_setup', 'not_detected', 'unsupported', 'error']) {
  assert.ok(STATE_WORDS[state] && STATE_SENTENCE[state], state);
  assert.doesNotMatch(STATE_SENTENCE[state] + STATE_WORDS[state], /PATH|agentnet |responder|harness/i, state);
}

// Folders: names join the path with the separator it already uses; full
// entries pass through; the screen never types a path.
assert.deepEqual(folderEntries({ path: '/home/me', dirs: ['a b', 'c'] }), [{ name: 'a b', path: '/home/me/a b' }, { name: 'c', path: '/home/me/c' }]);
assert.deepEqual(folderEntries({ path: '/', dirs: ['home'] }), [{ name: 'home', path: '/home' }]);
assert.deepEqual(folderEntries({ path: 'C:\\Users\\me', dirs: ['Work'] }), [{ name: 'Work', path: 'C:\\Users\\me\\Work' }]);
assert.deepEqual(folderEntries({ path: 'D:\\', dirs: ['x'] }), [{ name: 'x', path: 'D:\\x' }]);
assert.deepEqual(folderEntries({ path: '/x', dirs: [{ name: 'y', path: '/x/y' }] }), [{ name: 'y', path: '/x/y' }]);
assert.deepEqual(folderEntries({ path: '/x', dirs: null }), []);
assert.equal(folderName('/home/me/projects/'), 'projects');

// A folder that can't be read still leaves a way on: up from it, home, the
// drives last shown. Its parent is worked out from the path alone.
for (const [path, up] of [
  ['/home/me/gone', '/home/me'], ['/home/me/gone/', '/home/me'], ['/root', '/'], ['/', ''],
  ['C:\\Users\\me\\gone', 'C:\\Users\\me'], ['C:\\Users', 'C:\\'], ['C:\\', ''], ['C:/Users', 'C:/'], ['\\\\server', ''], ['gone', ''],
]) assert.equal(parentFolder(path), up, path);
{
  // The start folder (an agent's deleted folder) fails before anything was shown.
  assert.deepEqual(folderWayOut('/home/me/gone', null), [{ kind: 'up', label: 'Up', path: '/home/me' }, { kind: 'home', label: 'Home' }]);
  // A closed subfolder: up leads back, home, and the drives last shown.
  const last = { path: 'C:\\', home: 'C:\\Users\\me', roots: ['C:\\', 'D:\\'], dirs: ['Locked'] };
  assert.deepEqual(folderWayOut('C:\\Locked', last), [{ kind: 'up', label: 'Up', path: 'C:\\' }, { kind: 'home', label: 'Home' }, { kind: 'root', label: 'D:\\', path: 'D:\\' }]);
  // Home itself can't be read: only the drives are left (and Try again).
  assert.deepEqual(folderWayOut(undefined, null), []);
  assert.deepEqual(folderWayOut('C:\\Users\\me', last).map((s) => s.label), ['Up', 'C:\\', 'D:\\']);
}
assert.equal(folderName('C:\\Users\\me'), 'me');
assert.equal(folderName('/'), '/');

console.log('assistant setup model ok');
