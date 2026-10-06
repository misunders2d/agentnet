// Browser parity for nonterminal responder progress (envelope.StatusProgress,
// protocol.CapProgress). Synthetic Engine/store only; no network or model.
// stdin: {cases: [{name, inner}], vectors: [{name, inner, valid}]} from
// progress_browser_test.go (vectors: envelope.TestProgressShapeVectors, Go
// field names); stdout: verdict JSON.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';

const { cases, vectors } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
const alice = await wire.newKeys(), alicePub = await wire.publicEntry(alice, 'alice/a');
const keys = await wire.newKeys(), pub = await wire.publicEntry(keys, 'bob/b');
const verdicts = {};
for (const { name, inner } of cases) {
  try {
    const env = await wire.seal(inner, alice, pub);
    const back = await wire.open(env, keys, 'bob/b', alicePub);
    assert.equal(back.status, inner.status || '');
    verdicts[name] = 'ok';
  } catch (e) { verdicts[name] = /^progress is a plain-text update replying to one request/.test(e.message) ? 'progress' : 'other: ' + e.message; }
}
// The shared Go shape vectors, judged by the browser's checkVersion2.
const shapes = {};
for (const { name, inner } of vectors) {
  const n = { reply_to: '', status: '', conv: '', lid: '', sub: '', replica: false, origin: '', emotion: '', target: null, pid: '', fan: null, ref: null, agent_id: '', receiver_route: null, human: null,
    ...inner, root: inner.root ? JSON.stringify(inner.root) : '', attachments: inner.attachments || [] };
  try { await wire.checkVersion2(n); shapes[name] = true; } catch (e) { shapes[name] = false; }
}

// Waiting: a progress reply keeps the question waiting; a terminal answer settles it.
const store = memoryStore(), e = new Engine({ store, base: 'https://synthetic.invalid', fetch: async () => { throw Error('no network'); } });
const q = '1'.repeat(32), p = '2'.repeat(32), a = '3'.repeat(32);
await store.write([{ s: 'outbox', k: q, v: { v: 1, id: q, to: 'bob/b', kind: 'question', body: 'check this', at: 1, state: 'delivered' } },
  { s: 'inbox', k: p, v: { v: 1, id: p, from: 'bob/b', kind: 'message', body: 'accepted; checking', reply_to: q, status: wire.StatusProgress, at: 2, read: true, state: '' } }]);
assert.equal((await e.threadSummaries())[0].waiting, true, 'progress must not clear Waiting');
await store.write([{ s: 'inbox', k: a, v: { v: 1, id: a, from: 'bob/b', kind: 'answer', body: 'done', reply_to: q, at: 3, read: true, state: '' } }]);
assert.equal((await e.threadSummaries())[0].waiting, false, 'terminal answer settles Waiting');

// Signed profile negotiation honors rm1 readers; new release capabilities stay explicit.
const puts = [];
const c = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch: async (url, o = {}) => {
  const path = new URL(url).pathname;
  if (path === '/v1/version') return new Response(JSON.stringify({ features: ['caps'], realm_id: 'test-realm' }));
  if (path === '/v1/caps' && o.method === 'PUT') { puts.push(o.body); return new Response(null, { status: 204 }); }
  throw Error('offline');
} });
c.keys = keys; c.address = 'bob/b'; c.session = '4'.repeat(32);
await c.onConnect().catch(() => {});
assert.equal(puts.length, 1);
const rec = wire.parseCaps(puts[0]);
await wire.verifyCaps(rec, pub.sign_key);
const profile={sessions:[rec.session],caps:[JSON.parse(puts[0])]},basic=[wire.CapProgress,wire.CapHumanParticipation],explicit=[wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapReadSync];
assert(rec.caps.length<=wire.MaxAdvertisedCaps,'published record fits advertisement bound');
for(const cap of [...basic,...explicit])assert(await wire.profileSupports(profile,c.address,pub.sign_key,cap),'signed profile reads '+cap);
const old=await wire.newCaps(keys,c.address,'5'.repeat(32),[wire.CapRoom]),mixed={sessions:[rec.session,old.session],caps:[...profile.caps,JSON.parse(wire.capsJSON(old))]};
for(const cap of basic)assert(await wire.profileSupports(mixed,c.address,pub.sign_key,cap),'rm1 implies '+cap);
for(const cap of explicit){
 assert(rec.caps.includes(cap)&&!wire.capsReads(old,cap),'new capability stays explicit: '+cap);
 assert(!await wire.profileSupports(mixed,c.address,pub.sign_key,cap),'old session blocks new capability: '+cap);
}
console.log(JSON.stringify({ verdicts, shapes, waiting: 'ok', caps: rec.caps }));
