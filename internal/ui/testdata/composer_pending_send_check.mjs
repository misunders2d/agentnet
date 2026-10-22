// Actual desktop Composer action + production pendingSends. No server/model,
// generated bundle, fake ledger, timeout, or implicit retry is involved.
import assert from 'node:assert/strict';
import {registerHooks, stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {pendingSends} from '../static/optimistic.mjs';

const {errorText}=await import('../web/src/api.ts');
for(const message of ['Failed to fetch','NetworkError when attempting to fetch resource.','Load failed','fetch failed'])assert.equal(errorText(new TypeError(message)),'AgentNet is not reachable. Check the connection and try again.');
for(const message of ['Permission denied','Failed to fetch a native permission','Relay quota reached'])assert.equal(errorText(new Error(message)),message);
assert.equal(errorText(new DOMException('The operation was aborted.','AbortError')),'The operation was aborted.');

const root = new URL('../', import.meta.url);
registerHooks({
  load(url, context, next) {
    if (url.includes('/web/src/') && url.endsWith('.ts'))
      return {format:'module', shortCircuit:true, source:stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'))};
    return next(url, context);
  },
  resolve(specifier, context, next) {
    const parent = context.parentURL || '';
    if (parent.includes('/web/src/')) {
      if (specifier.endsWith('/collective-mentions.mjs')) return next(new URL('skins/shared/collective-mentions.mjs', root).href, context);
      if (specifier === './linkText') return next(new URL('skins/shared/link-text.mjs', root).href, context);
      if (specifier.startsWith('.') && !/\.(ts|tsx|mjs|js)$/.test(specifier)) return next(new URL(specifier + '.ts', parent).href, context);
    }
    return next(specifier, context);
  },
});
const {currentComposerFollowup} = await import('../web/src/features/Composer.followup.ts');
const {decode, encode, agentTargets, guestAuthor} = await import('../web/src/features/Composer.mentions.ts');
const source = readFileSync(new URL('web/src/features/Composer.tsx', root), 'utf8');
const sendSource = source.match(/^  async function send\([\s\S]*?^  }/m)?.[0];
const selectSource = source.match(/^  const selectedTargetFollowup = [\s\S]*?;$/m)?.[0];
const groupSelectSource = source.match(/^  const selectedFollowup = [\s\S]*?;$/m)?.[0];
assert.ok(sendSource && selectSource && groupSelectSource, 'execute unchanged actual Composer action and selectors');

const conv = 'a'.repeat(64), pid = 'b'.repeat(32), original = 'c'.repeat(32), agentID = 'd'.repeat(32);
const author = 'aaaaaaaa-aaaaaaaa-aaaaaaaa-aaaaaaaa', hostKey = 'bbbbbbbb-bbbbbbbb-bbbbbbbb-bbbbbbbb';
const overview = {me:{address:'fixture/laptop', fingerprint:author}, people:[]};
const dm = {id:conv, kind:'group', title:'Disposable group', agents:[{pid, agent_id:agentID, can_ask:true, host:{address:'fixture/host', fingerprint:hostKey}}], messages:[{
  id:original, lid:original, dir:'out', from:overview.me.address, kind:'question', body:'Synthetic original', pid,
  target:{address:'fixture/host', fingerprint:hostKey, agent_id:agentID}, exec:{state:'running', host:'fixture/host', attempt:1},
}]};
let serial = 0, release, firstSettled = false;
const held = new Promise(resolve => {release = resolve;});
const calls = [], notices = [];
const workspaceState = {}, state = {open:{kind:'dm', id:conv}, overview, drafts:{}};
const host = {platform:'daemon', workspace:{id:'held-send'}, workspaces:{state:() => workspaceState}, api:async (path, body) => {
  calls.push({path, body:structuredClone(body)});
  if (path === '/api/request/followup') {await held; firstSettled = true; return {note:'Synthetic follow-up saved'};}
  assert.equal(path, '/api/dm/send');
  assert(firstSettled, 'ordinary POST never overtakes the unresolved preceding save');
  return {id:body.id, lid:body.id, state:'queued'};
}};
const store = {host, get:() => state, isActive:() => true,
  setDraft:(id, draft) => state.drafts[id] = draft,
  api:{sendDM:body => host.api('/api/dm/send', body)},
  sends:pendingSends(host), toast:() => {}, refetch:async () => {},
};
const env = {store, conv, dm, thread:undefined, ready:true, overview, names:{}, words:{}, visible:{current:conv}, EMPTY:{text:''},
  target:{kind:'agent', pid, name:'Fixture agent'}, latest:() => state.drafts[conv],
  followupContext:{conv, dm, thread:null, overview, names:{}, canReply:true}, currentComposerFollowup,
  sendID:() => (++serial).toString(16).padStart(32, '0'),
  decode, encode, agentTargets, guestAuthor,
  clearSent:id => state.drafts[id] = {text:''}, setNotice:n => {if (n) notices.push(n);}, setBusy:() => {}, typing:{stop:() => {}},
  releaseFiles:() => {}, forgetStaged:() => {}, announce:() => {}, errorText:e => e.message,
};
runInNewContext(stripTypeScriptTypes(groupSelectSource) + '\n' + stripTypeScriptTypes(selectSource) + '\nglobalThis.choose = selectedTargetFollowup;', env);
env.selectedTargetFollowup = env.choose;
const send = runInNewContext('(' + stripTypeScriptTypes(sendSource) + ')', env);
const turn = () => new Promise(resolve => setImmediate(resolve));

state.drafts[conv] = {text:'Synthetic correction', agent:pid};
const first = send();
await turn();
assert.equal(calls.length, 1);
assert.equal(calls[0].path, '/api/request/followup');
assert.equal(calls[0].body.ref.id, original);
assert.equal(calls[0].body.ref.fingerprint, author);
assert.equal(calls[0].body.attempt, 1);

state.drafts[conv] = {text:'Synthetic ordinary message'};
env.target = {kind:'conversation'};
const second = send();
await turn();
const heldRows = store.sends.merge(conv, []);
assert.equal(heldRows.length, 2, 'both captured sends survive in the production ledger');
assert.equal(new Set(heldRows.map(m => m.id)).size, 2);
assert(heldRows.every(m => m._local), 'neither save has been proven by a durable projection');
assert.equal(heldRows[0].kind, 'question');
assert.equal(heldRows[1].kind, 'message', 'later send is ordinary, not another correction');
assert.equal(calls.length, 1, 'second same-chat POST waits on actual sends.ready');
assert(store.sends.unsent(), 'reload guard knows there are unresolved in-memory sends');
const before = heldRows.map(m => ({id:m.id, kind:m.kind, state:m.state, text:m.state_text}));
console.log(JSON.stringify({phase:'held-followup', posts:calls.map(c => c.path), rows:before}));

// Release, rather than cancel/timeout/retry, to prove this exact predecessor
// explains the observed block and that it causes no duplicate ordinary send.
release();
await Promise.all([first, second]);
assert.deepEqual(calls.map(c => c.path), ['/api/request/followup', '/api/dm/send']);
assert.equal(calls[0].body.id, heldRows[0].id);
assert.equal(calls[1].body.id, heldRows[1].id);
assert.equal(calls[1].body.conv, conv);
assert.equal(notices.length, 0);
assert.equal(store.sends.unsent(), false);
const stored = heldRows.map(m => ({id:m.id, lid:m.lid, dir:'out'}));
assert.equal(store.sends.merge(conv, stored).length, 2);
assert(!store.sends.has(heldRows[0].id) && !store.sends.has(heldRows[1].id), 'exact durable IDs retire previews');
console.log('PASS actual Composer followup -> ordinary same-chat ordering: held predecessor blocks POST; releasing it sends the ordinary message exactly once');

// This characterizes the existing memory-only dependency mechanism. The live
// incident had durable outbox rows, so this is not evidence of its cause.
assert.deepEqual(before.map(m => m.text), ['Sending…', 'Sending…']);
console.log('CHARACTERIZED: both previews say Sending before the second POST starts; this does not explain an incident with durable outbox rows');
