// Exercise the exact production catalog validator, not a copied policy.
'use strict';
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync(path.join(__dirname, '../static/app.js'), 'utf8');
const start = source.indexOf('async function localReplySessions('), end = source.indexOf('async function loadReceiverCatalog(', start);
assert(start >= 0 && end > start, 'production receiver validator seam');
const host = {}, expected = 'synthetic/laptop';
const choice = (harness, handle = 'exact-' + harness + '-handle') => ({ handle, harness, label: 'Synthetic ' + harness, active: true });
async function validate(catalog) {
  const context = vm.createContext({ api: async (url, body, transport) => {
    assert.equal(url, '/api/reply-sessions'); assert.equal(body, undefined); assert.equal(transport, host);
    return catalog;
  } });
  vm.runInContext(source.slice(start, end), context);
  return context.localReplySessions(host, expected);
}
(async () => {
  const mixed = [choice('pi'), { ...choice('omp'), active: false }, choice('codex'), choice('claude')];
  assert.equal(await validate({ local: true, host: expected, sessions: mixed }), mixed, 'mixed qualified handles stay exact, including inactive registration');
  const empty = []; assert.equal(await validate({ local: true, host: expected, sessions: empty }), empty);
  for (const catalog of [
    { local: false, host: expected, sessions: mixed },
    { local: true, host: 'other/host', sessions: mixed },
    { local: true, host: expected, sessions: [choice('unknown')] },
    { local: true, host: expected, sessions: [choice('codex', '/private/native-path')] },
    { local: true, host: expected, sessions: [choice('codex', '')] },
    { local: true, host: expected, sessions: [choice('codex', 'x'.repeat(129))] },
    { local: true, host: expected, sessions: [choice('pi'), choice('unknown')] }
  ]) await assert.rejects(validate(catalog), /Native session list does not match this workspace's host/);
  console.log('receiver catalog validator ok');
})().catch(error => { console.error(error); process.exitCode = 1; });
