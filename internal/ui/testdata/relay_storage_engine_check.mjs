// Browser relay attachment storage (engine.mjs relayAttachmentStorage): each
// call is signed by the captured workspace's own key and reaches only its own
// relay; malformed changes never reach the network; a read never yields a
// consent link; the relay's refusals keep their status.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
import { WorkspaceShell } from '../static/workspaces.mjs';

const subtle = globalThis.crypto.subtle, utf8 = new TextEncoder();
const hex = (b) => [...new Uint8Array(b)].map((x) => x.toString(16).padStart(2, '0')).join('');
const consent = 'https://accounts.google.com/o/oauth2/v2/auth?state=synthetic';
const status = (admin) => ({ can_admin: admin, configured: true, connected: false, state: 'local', local: { files: 1, bytes: 9 }, remote: { files: 0, bytes: 0 },
  pending: { files: 0, bytes: 0 }, remote_only_files: 0, migration: { running: false, files: 0, bytes: 0, total_files: 0, total_bytes: 0 }, consent_url: consent });

async function relay(base, address, reply) {
  const keys = await wire.newKeys(), calls = [];
  const fetch = async (url, o = {}) => {
    const u = new URL(url), body = o.body || '', h = o.headers || {};
    assert.equal(u.origin, base, 'a request left its own workspace relay');
    const msg = ['agentnet-request-v1', h['X-Agentnet-Agent'], o.method, u.pathname + u.search, h['X-Agentnet-Time'], h['X-Agentnet-Nonce'], hex(await subtle.digest('SHA-256', utf8.encode(body)))].join('\n');
    const signed = h['X-Agentnet-Agent'] === address && await subtle.verify('Ed25519', keys.signPublic, wire.unb64(h['X-Agentnet-Sig'] || '', 'signature'), utf8.encode(msg));
    calls.push({ method: o.method, path: u.pathname, body, signed });
    const [code, json] = reply(o.method, body);
    return new Response(JSON.stringify(json), { status: code, headers: { 'Content-Type': 'application/json' } });
  };
  const e = new Engine({ store: memoryStore(), base, fetch });
  e.keys = keys; e.address = address;
  return { e, calls };
}

const admin = await relay('https://a.example', 'admin/laptop', (method) => [200, status(method === 'POST')]);
const read = await admin.e.api('/api/storage/attachments');
assert.equal(read.consent_url, undefined, 'a read never yields a consent link');
assert.equal(read.state, 'local');
assert.deepEqual(admin.calls, [{ method: 'GET', path: '/v1/storage/attachments', body: '', signed: true }]);
for (const [change, body, link] of [
  [{ action: 'connect' }, '{"action":"connect"}', consent],
  [{ action: 'move', confirm: true }, '{"action":"move","confirm":true}', undefined],
  [{ action: 'disconnect', confirm: false }, '{"action":"disconnect"}', undefined],
]) {
  admin.calls.length = 0;
  assert.equal((await admin.e.api('/api/storage/attachments', change)).consent_url, link);
  assert.deepEqual(admin.calls, [{ method: 'POST', path: '/v1/admin/storage/attachments', body, signed: true }]);
}
admin.calls.length = 0;
for (const bad of [null, [], {}, 'connect', { action: 'delete' }, { action: 'Connect' }, { action: 'connect', account: 'someone@example.com' }, { action: 'move', confirm: 'yes' }]) {
  await assert.rejects(() => admin.e.api('/api/storage/attachments', bad), /Choose connect, move or disconnect\./);
}
assert.equal(admin.calls.length, 0, 'malformed changes reached the relay');

for (const [code, error, words] of [
  [403, 'admin only', 'Only an admin of your server can change workspace attachment storage.'],
  [404, 'not found', 'Your server does not offer workspace attachment storage.'],
  [409, '3 attachments exist only in Drive; move them back before disconnecting', '3 attachments exist only in Drive; move them back before disconnecting'],
]) {
  const refused = await relay('https://b.example', 'bob/phone', () => [code, { error }]);
  await assert.rejects(() => refused.e.api('/api/storage/attachments', { action: 'disconnect', confirm: true }), (e) => e.status === code && e.message === words);
  assert.equal(refused.calls.length, 1);
}

// Workspace capture: a handle bound before switching stays on its own relay
// and key; a daemon membership goes only to its own mount; a disconnected
// handle refuses without any request.
const member = await relay('https://b.example', 'bob/phone', () => [200, status(false)]);
const daemon = [];
const shell = new WorkspaceShell({ fetch: async (url, o = {}) => { daemon.push({ url, method: o.method || 'GET', body: o.body }); return new Response(JSON.stringify(url === '/api/workspaces/disconnect' ? {} : status(true)), { status: 200 }); } });
const binding = (id, endpoint, address, handle) => ({ id, name: '', endpoint, realm: id.slice(0, 32), handle, address, state: 'enrolled' });
const a = shell.register(binding('a'.repeat(32), 'https://a.example', 'admin/laptop', '1'.repeat(32)), admin.e);
const b = shell.register(binding('b'.repeat(32), 'https://b.example', 'bob/phone', '2'.repeat(32)), member.e);
const d = shell.register(binding('c'.repeat(32), 'http://127.0.0.1:1', 'carol/desk', '3'.repeat(32)));
shell.select('b'.repeat(32));
admin.calls.length = 0;
assert.equal((await a.api('/api/storage/attachments', { action: 'connect' })).can_admin, true);
assert.equal(admin.calls.length, 1); assert.equal(admin.calls[0].signed, true); assert.equal(member.calls.length, 0);
assert.equal((await b.api('/api/storage/attachments')).can_admin, false);
assert.equal(member.calls.length, 1); assert.equal(member.calls[0].signed, true); assert.equal(admin.calls.length, 1);
await d.api('/api/storage/attachments', { action: 'move', confirm: true });
assert.deepEqual(daemon, [{ url: '/workspaces/' + 'c'.repeat(32) + '/' + '3'.repeat(32) + '/api/storage/attachments', method: 'POST', body: '{"action":"move","confirm":true}' }]);
await shell.disconnect('c'.repeat(32));
const before = daemon.length;
await assert.rejects(() => d.api('/api/storage/attachments', { action: 'connect' }), /Stale workspace handle/);
assert.equal(daemon.length, before);
assert.equal(admin.calls.length + member.calls.length, 2);
console.log('PASS browser relay attachment storage: signed read/changes, consent only on change, malformed refused offline, 403/404/409 kept, workspace capture and stale handle');
